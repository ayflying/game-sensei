// Package game 用「游戏档案」（GameProfile）描述一款游戏**怎么操作**，
// 并把跨游戏通用的语义动作（agent 的 L1 层）翻译成平台可执行动作（L2 层）。
//
// # 为什么需要这一层
//
// 这个框架的目标是「换一个游戏就能玩」，而不是为某一款游戏写死。
// 但不同游戏的操作差异恰恰集中在最底层：摇杆在左下还是右下、半径多大、
// 是虚拟摇杆还是十字键、有哪些按钮、按钮各在什么位置。这些信息
//
//   - 属于**游戏常量**，同一个游戏同一分辨率下永远不变；
//   - 不该让视觉模型每帧去回归（它做不好，实测会在参数语义上纠结到超时）；
//   - 也不该硬编码进代码（那就等于为单一游戏设计）。
//
// 于是它们被抽成一份 JSON 档案。**换游戏 = 换一份档案**，
// 动作空间、提示词模板、决策代码都不动。
//
// 一份档案回答四个问题：
//
//  1. 怎么移动？        Move.Mode = joystick | dpad | keys | none
//  2. 摇杆在哪、推多远？  Move.Center / Move.Radius（或 Move.Keys 指定按键）
//  3. 有哪些按钮、在哪？  Buttons（供 ACTION PRESS name=... 使用）
//  4. 有什么界面先验？    Hints（直接拼进老师提示词）
package game

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed profiles/*.json
var builtinProfiles embed.FS

// MoveMode 是游戏的移动操控方式。
type MoveMode string

const (
	// MoveNone 该档案未配置移动（MOVE 动作会明确报错，不会静默乱推）。
	MoveNone MoveMode = "none"
	// MoveJoystick 虚拟摇杆（手游最常见）：按中心 + 推杆向量换算成一次 swipe。
	MoveJoystick MoveMode = "joystick"
	// MoveDpad 十字键/方向键：映射到 keyevent（Android）或按键（PC）。
	MoveDpad MoveMode = "dpad"
	// MoveKeys 键盘移动（PC 键鼠游戏）：默认 WASD。
	MoveKeys MoveMode = "keys"
)

// 摇杆推杆幅度的默认值（归一化，x 相对屏宽、y 相对屏高）。
//
// 取 0.09 的依据：手游玩家的拇指推杆幅度普遍在屏幕宽度的 7%~12% 之间，
// 太小会被游戏判定为死区，太大则可能滑出摇杆热区。
const (
	DefaultRadiusX = 0.09
	DefaultRadiusY = 0.08
)

// MoveProfile 描述「怎么移动」。
type MoveProfile struct {
	// Mode 移动方式。
	Mode MoveMode `json:"mode"`
	// Center 虚拟摇杆中心的归一化坐标 [x, y]；joystick 模式必填。
	Center [2]float64 `json:"center,omitempty"`
	// Radius 推杆幅度的归一化值 [x, y]；留空用 DefaultRadiusX/Y。
	Radius [2]float64 `json:"radius,omitempty"`
	// Floating 标记摇杆是「浮动摇杆」（手指按哪儿哪儿成为中心）。
	// 执行上与固定摇杆相同（都从 Center 起推），此字段只影响给老师的说明文字：
	// 浮动摇杆下，模型不该假设「摇杆中心固定」，但仍从这个默认落点推杆。
	Floating bool `json:"floating,omitempty"`
	// Keys 方向 → 键名的覆盖映射，键名取 agent.Dir 的规范值（up/down/left/right/...）。
	// 留空时 keys 模式用 WASD、dpad 模式用 Android 方向键。
	Keys map[string]string `json:"keys,omitempty"`
}

// Button 是一个命名按钮：ACTION PRESS name=<Name> 会点击 Pos。
//
// 用「名字」而不是坐标，是为了把小模型的决策压成一次短列表选择——
// 它选 `jump` 远比它算 `x=0.79 y=0.44` 可靠。
type Button struct {
	// Name 规范名（建议英文小写，作 ACTION PRESS 的取值）。
	Name string `json:"name"`
	// Aliases 别名（中英文均可），解析时一并接受，写日志仍用 Name。
	Aliases []string `json:"aliases,omitempty"`
	// Pos 按钮中心的归一化坐标 [x, y]。手游（触摸点击）必填；
	// PC 键盘按钮（Key 非空）可以省略，默认 (0,0)。
	Pos [2]float64 `json:"pos,omitempty"`
	// Key PC 键位：非空时 PRESS 这个按钮 = 按这个键盘键（而不是点击坐标）。
	// 同一款游戏的手游档案与 PC 档案因此可以只差在按钮的"落点"表达方式上，
	// 语义层（PRESS name=interact）完全不变。
	Key string `json:"key,omitempty"`
	// Note 一句话说明，会拼进老师提示词（模型需要知道这个按钮是干嘛的）。
	Note string `json:"note,omitempty"`
}

// Profile 是一款游戏的操作档案。
type Profile struct {
	// Name 游戏名（如「洛克王国：世界」），用于日志与提示词。
	Name string `json:"name"`
	// Package Android 包名；填了就能用 -game 直接带出 -app。
	Package string `json:"package,omitempty"`
	// Orientation 屏幕方向 landscape | portrait，仅作说明与校验提示。
	Orientation string `json:"orientation,omitempty"`
	// Zoom 该游戏支持缩放画面（PC=滚轮 / 安卓=双指捏合）。
	// 只有声明了 true，老师的动作协议里才列 ZOOM——
	// 缩放在不同游戏里实现差异大（有的根本不能缩），不声明就别让它输出。
	Zoom bool `json:"zoom,omitempty"`

	Move    MoveProfile `json:"move"`
	Buttons []Button    `json:"buttons,omitempty"`
	// Hints 额外的界面先验，逐条拼进老师提示词。
	Hints []string `json:"hints,omitempty"`
}

// Load 加载游戏档案。参数可以是
//
//	档案名  "nrc"                      → 依次找 ./profiles/nrc.json、
//	                                     .workbuddy/profiles/nrc.json、内置档案
//	文件路径 "./profiles/my.json"       → 直接读取该文件
//
// 外部文件优先于内置，便于在不改代码的前提下修正某个游戏的操作参数。
func Load(nameOrPath string) (*Profile, error) {
	spec := strings.TrimSpace(nameOrPath)
	if spec == "" {
		return nil, fmt.Errorf("game: 档案标识为空")
	}

	// 1) 显式路径：含路径分隔符，或以 .json 结尾
	if strings.ContainsAny(spec, `/\`) || strings.HasSuffix(strings.ToLower(spec), ".json") {
		data, err := os.ReadFile(spec)
		if err != nil {
			return nil, fmt.Errorf("game: 读取档案文件失败: %w", err)
		}
		return decode(spec, data)
	}

	// 2) 按名字找外部文件（工作目录优先，便于用户自带档案）
	var tried []string
	for _, p := range []string{
		filepath.Join("profiles", spec+".json"),
		filepath.Join(".workbuddy", "profiles", spec+".json"),
	} {
		if data, err := os.ReadFile(p); err == nil {
			return decode(p, data)
		}
		tried = append(tried, p)
	}

	// 3) 内置档案兜底
	if data, err := builtinProfiles.ReadFile("profiles/" + spec + ".json"); err == nil {
		return decode("内置档案 "+spec, data)
	}

	return nil, fmt.Errorf("game: 找不到档案 %q（找过 %s，也无同名内置档案；内置可用：%s）",
		spec, strings.Join(tried, "、"), strings.Join(Names(), "、"))
}

// Names 列出内置档案名（不含 .json），供 CLI 帮助与错误提示使用。
func Names() []string {
	entries, err := builtinProfiles.ReadDir("profiles")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		out = append(out, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(out)
	return out
}

func decode(src string, data []byte) (*Profile, error) {
	var p Profile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields() // 拼错的字段名要立刻报错，而不是静默失效
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("game: 解析档案 %s 失败: %w", src, err)
	}
	if err := p.normalize(); err != nil {
		return nil, fmt.Errorf("game: 档案 %s 不合法: %w", src, err)
	}
	return &p, nil
}

// normalize 填默认值并做基本校验。
func (p *Profile) normalize() error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return fmt.Errorf("缺少 name 字段")
	}
	p.Move.Mode = MoveMode(strings.ToLower(strings.TrimSpace(string(p.Move.Mode))))
	if p.Move.Mode == "" {
		p.Move.Mode = MoveNone
	}

	switch p.Move.Mode {
	case MoveNone:
		// 不移动的游戏（如纯卡牌/文字游戏）合法：MOVE 会明确报错
	case MoveJoystick:
		if !inUnitRange(p.Move.Center[0]) || !inUnitRange(p.Move.Center[1]) ||
			(p.Move.Center[0] == 0 && p.Move.Center[1] == 0) {
			return fmt.Errorf("joystick 模式必须提供合理的 move.center（归一化 [x,y]，不能为 0,0）")
		}
		if p.Move.Radius[0] <= 0 {
			p.Move.Radius[0] = DefaultRadiusX
		}
		if p.Move.Radius[1] <= 0 {
			p.Move.Radius[1] = DefaultRadiusY
		}
	case MoveDpad, MoveKeys:
		// 键位可用默认值，无需强校验
	default:
		return fmt.Errorf("未知的 move.mode %q（可选 %s|%s|%s|%s）",
			p.Move.Mode, MoveNone, MoveJoystick, MoveDpad, MoveKeys)
	}

	seen := map[string]bool{}
	for i := range p.Buttons {
		b := &p.Buttons[i]
		b.Name = strings.ToLower(strings.TrimSpace(b.Name))
		if b.Name == "" {
			return fmt.Errorf("第 %d 个按钮缺少 name", i+1)
		}
		if seen[b.Name] {
			return fmt.Errorf("按钮名 %q 重复", b.Name)
		}
		seen[b.Name] = true
		b.Key = strings.TrimSpace(b.Key)
		// 按钮要么有坐标（点击），要么有键位（PC 键盘），二者皆无则无法执行。
		// 校验放在这里而不是执行时：拼错字段名/漏写 pos 要立刻报错，不许静默退化。
		if b.Key == "" && (b.Pos[0] == 0 && b.Pos[1] == 0) {
			return fmt.Errorf("按钮 %q 既没有 key 也没有有效 pos，PRESS 无法执行", b.Name)
		}
		if !inUnitRange(b.Pos[0]) || !inUnitRange(b.Pos[1]) {
			return fmt.Errorf("按钮 %q 的 pos 必须是 0~1 的归一化坐标", b.Name)
		}
		for j := range b.Aliases {
			b.Aliases[j] = strings.ToLower(strings.TrimSpace(b.Aliases[j]))
		}
	}
	return nil
}

func inUnitRange(v float64) bool { return v >= 0 && v <= 1 }

// Button 按规范名或别名查找按钮（大小写不敏感）。
func (p *Profile) Button(name string) (Button, bool) {
	if p == nil {
		return Button{}, false
	}
	want := strings.ToLower(strings.TrimSpace(name))
	for _, b := range p.Buttons {
		if b.Name == want {
			return b, true
		}
		for _, a := range b.Aliases {
			if a == want {
				return b, true
			}
		}
	}
	return Button{}, false
}

// ButtonNames 返回按钮规范名列表，顺序同档案定义（保证提示词可复现）。
func (p *Profile) ButtonNames() []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(p.Buttons))
	for _, b := range p.Buttons {
		out = append(out, b.Name)
	}
	return out
}

// buttonList 返回带说明的按钮清单，如 "jump(跳跃)" 或 "interact(F)"，供提示词使用。
//
// PC 键盘按钮（有 Key）优先展示键位——老师看到「交互(F)」才知道该 PRESS interact，
// 看到坐标反而没用（PC 上不是靠点坐标交互的）。
func (p *Profile) buttonList() []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(p.Buttons))
	for _, b := range p.Buttons {
		label := b.Name
		switch {
		case b.Key != "" && b.Note != "":
			label += "(" + b.Key + "：" + b.Note + ")"
		case b.Key != "":
			label += "(" + b.Key + ")"
		case b.Note != "":
			label += "(" + b.Note + ")"
		case len(b.Aliases) > 0:
			label += "(" + b.Aliases[0] + ")"
		}
		out = append(out, label)
	}
	return out
}

// radius 返回推杆幅度，缺省值兜底。
func (m MoveProfile) radius() (float64, float64) {
	rx, ry := m.Radius[0], m.Radius[1]
	if rx <= 0 {
		rx = DefaultRadiusX
	}
	if ry <= 0 {
		ry = DefaultRadiusY
	}
	return rx, ry
}

// moveNote 生成给老师看的一句话移动方式说明。
func (m MoveProfile) moveNote() string {
	switch m.Mode {
	case MoveJoystick:
		if m.Floating {
			return "虚拟摇杆，手指按下处即中心"
		}
		return "虚拟摇杆，位置固定在屏幕左下"
	case MoveKeys:
		return "键盘移动"
	case MoveDpad:
		return "方向键移动"
	}
	return ""
}
