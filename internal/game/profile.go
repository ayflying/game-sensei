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

// 界面态常量：游戏可在「大世界探索」与「回合战斗」两态间切换，两态动作空间不同。
const (
	// StateWorld 大世界探索态（有摇杆、交互/坐骑/跳跃等）。
	StateWorld = "world"
	// StateBattle 回合战斗态（无摇杆，只有技能/捕捉/更换/背包/逃跑）。
	StateBattle = "battle"
)

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
	// State 限定这个按钮只在某个界面态下存在/可点：world（大世界探索）或
	// battle（回合战斗）。留空表示两态通用。回路据此做两件事：
	//   - 战斗态的复读兜底只在 battle 按钮里轮换，不会跨态去点大世界的坐骑/跳跃；
	//   - 给老师的先验可按态收窄（动作空间越小，小模型决策越稳）。
	State string `json:"state,omitempty"`
	// Hidden 为 true 时这个按钮**不进老师的动作协议**（不出现在 PRESS 可选名里），
	// 但仍可被档案内部的宏按坐标调用。
	//
	// 典型用途：技能盘的「展开钮」和展开后才出现的「技能卡」是一条多步序列的
	// 中转步骤，直接暴露给小模型时，它只会反复点第一步（展开）而不会接着点卡。
	// 把这些中转钮标 hidden、再用一个宏把整序列打包成 cast_xxx，老师只做一次
	// 「释放技能 X」决策，序列的展开由回路确定性地完成。
	Hidden bool `json:"hidden,omitempty"`
}

// Profile 是一款游戏的操作档案。
type Profile struct {
	// Name 游戏名（如「洛克王国：世界」），用于日志与提示词。
	Name string `json:"name"`
	// Package Android 包名；填了就能用 -game 直接带出 -app。
	Package string `json:"package,omitempty"`
	// Orientation 屏幕方向 landscape | portrait，仅作说明与校验提示。
	Orientation string `json:"orientation,omitempty"`

	Move    MoveProfile `json:"move"`
	Buttons []Button    `json:"buttons,omitempty"`
	// Macros 命名宏（macro）：把一条「点展开钮 → 等界面 → 点子项 → 等结算」的
	// 多步序列打包成一个虚拟按钮。老师只需一次 PRESS name=<宏名>，回路就按
	// Steps 确定性地跑完整个序列，且只落一条示范样本。
	//
	// 为什么必须在框架层做这件事，而不是靠提示词教模型：技能盘是**切换式**的，
	// 「展开 → 选卡 → 等回合结算」带隐藏界面状态，关思考的小模型无法跨多步维持
	// 这个状态，只会复读第一步（实测 2026-09-12：连续点 battle_skill 却从不点卡）。
	// 把状态机收进档案，模型的决策退化成一次「放哪个技能」的短列表选择。
	Macros []Macro `json:"macros,omitempty"`
	// Hints 额外的界面先验，逐条拼进老师提示词。
	Hints []string `json:"hints,omitempty"`
	// BattleDetect 可选的「回合战斗态」像素判据（移植自 tools/detect_state.py 实测逻辑）。
	// 配了之后回路能只靠灰度帧判断当前是大世界还是战斗，从而在战斗态禁止 MOVE、
	// 卡死时不推摇杆、复读兜底只在战斗按钮里轮换。不配则这些按态保护全部关闭。
	BattleDetect *BattleDetect `json:"battle_detect,omitempty"`
	// Escape 卡死自愈脚本（可选）。见 EscapeProfile。
	Escape *EscapeProfile `json:"escape,omitempty"`
}

// BattleDetect 是「是否处于回合战斗态」的像素判据。
//
// 洛克王国实测（2026-09-12，平板 3200x2136）：战斗态底部横排五个奶油色圆钮
// 【逃跑 背包 捕捉 更换 技能】落在屏幕右下一条带里，亮于深蓝半透明底条。
// 在该带内取「亮像素列投影」，连通亮列簇（=圆钮）达到 MinClusters 即判战斗。
// 所有坐标/阈值都做成档案常量，换游戏只改档案。
type BattleDetect struct {
	// Band 检测带（归一化矩形）：[x0, y0, x1, y1]，左下/右上两点。
	Band [4]float64 `json:"band"`
	// Bright 灰度亮阈值（0~255）：高于它算「亮像素」（奶油圆钮）。
	Bright int `json:"bright,omitempty"`
	// SatMax 饱和度上限（0~255）：max(R,G,B)-min(R,G,B) 小于它才算「低饱和奶油色」，
	// 用来把高饱和的技能特效/血条从按钮候选里排掉。<=0 用 70（Python 实测值）。
	SatMax int `json:"sat_max,omitempty"`
	// MinPixels 一个亮列簇至少含多少亮像素才算一个候选圆钮（滤噪点）。
	MinPixels int `json:"min_pixels,omitempty"`
	// MinClusters 至少几个亮列簇才判战斗态（实测 5 钮，阈值取 3）。
	MinClusters int `json:"min_clusters,omitempty"`
	// MinGap 分簇用的最小列间隔保留参数（当前实现按相邻亮列连通分簇，字段预留）。
	MinGap int `json:"min_gap,omitempty"`
	// Positions 期望的圆钮中心（归一化 [x,y] 列表）；配了它才启用**位置判据**。
	//
	// 为什么必须有这一层：只数「簇的个数」会在大地图上严重误报——地图上散布着
	// 大量又亮又低饱和的圆形图标，在检测带里同样能凑出 ≥MinClusters 个簇。
	// 实测（2026-09-12）esc_map7 / probe_map3 / p_map_fresh 等地图帧全部被判成
	// 战斗，导致整轮采集都在地图界面上空跑（提示词被剥掉 MOVE、兜底还在图上乱点）。
	// 真实战斗圆钮固定在底部一条横排上，加上位置判据后散乱图标无法同时落位。
	Positions [][2]float64 `json:"positions,omitempty"`
	// TolX / TolY 位置匹配容差（归一化，缺省 0.015）。实测真实战斗圆钮质心与标定值
	// 只差约 0.0004，而相邻按钮间距约 0.06，所以 0.015 既稳又不会互相串位。
	TolX float64 `json:"tol_x,omitempty"`
	TolY float64 `json:"tol_y,omitempty"`
	// MinHits 至少要命中的标定位置数；缺省取 MinClusters（洛克王国=3）。
	MinHits int `json:"min_hits,omitempty"`
}

// tolerances 返回位置匹配容差，缺省 0.015。
func (d *BattleDetect) tolerances() (tolX, tolY float64) {
	tolX, tolY = d.TolX, d.TolY
	if tolX <= 0 {
		tolX = 0.015
	}
	if tolY <= 0 {
		tolY = 0.015
	}
	return
}

// CanDetectBattle 报告档案是否配了可用的战斗态判据。
func (p *Profile) CanDetectBattle() bool {
	return p != nil && p.BattleDetect != nil &&
		inUnitRange(p.BattleDetect.Band[0]) && inUnitRange(p.BattleDetect.Band[1]) &&
		inUnitRange(p.BattleDetect.Band[2]) && inUnitRange(p.BattleDetect.Band[3]) &&
		p.BattleDetect.Band[2] > p.BattleDetect.Band[0] &&
		p.BattleDetect.Band[3] > p.BattleDetect.Band[1]
}

// battleDetectDefaults 填判据默认值（bright/min_pixels/min_clusters 缺省时）。
func (d *BattleDetect) defaults() (bright, minPixels, minClusters int) {
	bright = d.Bright
	if bright <= 0 {
		bright = 150
	}
	minPixels = d.MinPixels
	if minPixels <= 0 {
		minPixels = 500
	}
	minClusters = d.MinClusters
	if minClusters <= 0 {
		minClusters = 3
	}
	return
}

// MacroStep 是宏里的一步：点一下（或拖一段）某处，然后等一会儿。
// 字段与 EscapeStep 同形（刻意只支持「点击/拖拽 + 等待」两种原语）：
// 宏要的是最大确定性，它在老师已经把决策交出来之后，必须无条件跑得通。
type MacroStep struct {
	// Pos 点击位置，或拖拽起点。归一化坐标 [x, y]。
	Pos [2]float64 `json:"pos"`
	// To 拖拽终点。非空时本步是「从 Pos 拖到 To」而不是点击。
	To [2]float64 `json:"to,omitempty"`
	// DragMs 拖拽时长（毫秒），仅 To 非空时有效；<=0 用 600。
	DragMs int `json:"drag_ms,omitempty"`
	// WaitMs 这一步之后等待的毫秒数（等技能盘展开/回合结算）；<=0 时用 -demo-wait。
	WaitMs int `json:"wait_ms,omitempty"`
	// Note 这一步在做什么，只用于日志与排障。
	Note string `json:"note,omitempty"`
}

// Macro 是一个命名的多步操作序列，以「虚拟按钮」的身份暴露给老师：
// 老师 PRESS name=<Name> 即触发整条 Steps，语义上等价于一次按钮动作。
type Macro struct {
	// Name 宏名（英文小写，作 ACTION PRESS 的取值），如 cast_hetu。
	Name string `json:"name"`
	// Aliases 别名，解析时一并接受。
	Aliases []string `json:"aliases,omitempty"`
	// Note 一句话说明，会拼进老师提示词（告诉模型这个宏会做什么）。
	Note string `json:"note,omitempty"`
	// State 限定宏只在某界面态可用：world | battle；留空表示通用。
	State string `json:"state,omitempty"`
	// Hidden 为 true 时不进老师协议（保留给系统/未来内部触发用）。
	Hidden bool `json:"hidden,omitempty"`
	// Steps 按顺序执行的点击/拖拽步骤。
	Steps []MacroStep `json:"steps"`
}

// EscapeStep 是脱困脚本里的一步：点一下（或拖一段）某处，然后等一会儿。
//
// 只支持「点击/拖拽 + 等待」这两种，是刻意的：脱困脚本要的是**最大确定性**，
// 它必须在老师已经失灵、画面可能卡住的情况下仍然跑得通。
// 引入条件分支、图像判断只会让它在最需要它的时候失效。
type EscapeStep struct {
	// Pos 点击位置，或拖拽起点。归一化坐标 [x, y]（相对屏幕宽高，同其它坐标）。
	Pos [2]float64 `json:"pos"`
	// To 拖拽终点。非空时本步是「从 Pos 拖到 To」而不是点击。
	// 用来把大地图拖到指定视野（地图边界会钳住，因此拖到底=视野确定）。
	To [2]float64 `json:"to,omitempty"`
	// DragMs 拖拽时长（毫秒），仅 To 非空时有效；<=0 用 600。
	DragMs int `json:"drag_ms,omitempty"`
	// WaitMs 这一步之后等待的毫秒数（等界面弹出/转场完成）；<=0 时用 -demo-wait。
	WaitMs int `json:"wait_ms,omitempty"`
	// Note 这一步在做什么，只用于日志与排障。
	Note string `json:"note,omitempty"`
}

// EscapeProfile 是「卡死自愈」脚本。
//
// 为什么放在档案里而不是代码里：这是**游戏专属**知识
// （洛克王国是「主菜单 → 地图 → 魔力之源锚点 → 传送」，
// 换个游戏可能是「按 M 开地图 → 点城镇 → 确认」），
// 而整个框架的立身之本是「换游戏只换档案」。
//
// 触发判据由代码给（同一方向连续移动若干步仍无推进），脚本内容由档案给。
type EscapeProfile struct {
	// Note 一句话说明这套脚本的用途与已知限制。
	Note string `json:"note,omitempty"`
	// MinRepeat 触发阈值：同一个方向连续移动这么多步仍未推进目标，就执行脚本。
	// <=0 时用默认 6。
	MinRepeat int `json:"min_repeat,omitempty"`
	// Cooldown 两次脱困之间至少间隔多少步，避免反复传送把示范回路刷成传送到饱。
	// <=0 时用默认 20。
	Cooldown int `json:"cooldown,omitempty"`
	// Steps 脚本步骤，按顺序执行。
	Steps []EscapeStep `json:"steps"`
}

// 脱困脚本的默认触发参数。
const (
	DefaultEscapeMinRepeat = 6
	DefaultEscapeCooldown  = 20
)

// EscapeMinRepeat 返回触发阈值（带默认值兜底）。
func (p *Profile) EscapeMinRepeat() int {
	if p == nil || p.Escape == nil || p.Escape.MinRepeat <= 0 {
		return DefaultEscapeMinRepeat
	}
	return p.Escape.MinRepeat
}

// EscapeCooldown 返回脱困冷却步数（带默认值兜底）。
func (p *Profile) EscapeCooldown() int {
	if p == nil || p.Escape == nil || p.Escape.Cooldown <= 0 {
		return DefaultEscapeCooldown
	}
	return p.Escape.Cooldown
}

// CanEscape 报告该档案是否配了可执行的脱困脚本。
func (p *Profile) CanEscape() bool {
	return p != nil && p.Escape != nil && len(p.Escape.Steps) > 0
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
		b.State = normalizeState(b.State)
		if b.State == stateInvalid {
			return fmt.Errorf("按钮 %q 的 state %q 不合法（可选 world|battle 或留空）", b.Name, b.State)
		}
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

	// 脱困脚本：坐标必须合法，否则宁可在装载时报错——
	// 卡死自愈是「最后一道保险」，跑到一半发现坐标拼错等于没有保险。
	if p.Escape != nil {
		if len(p.Escape.Steps) == 0 {
			return fmt.Errorf("escape 配了但 steps 为空（要么整段删掉，要么至少给一步）")
		}
		for i := range p.Escape.Steps {
			s := &p.Escape.Steps[i]
			if !inUnitRange(s.Pos[0]) || !inUnitRange(s.Pos[1]) {
				return fmt.Errorf("escape.steps[%d] 的 pos 必须是 0~1 的归一化坐标", i)
			}
			if s.WaitMs < 0 {
				return fmt.Errorf("escape.steps[%d] 的 wait_ms 不能为负", i)
			}
			if s.DragMs < 0 {
				return fmt.Errorf("escape.steps[%d] 的 drag_ms 不能为负", i)
			}
			if (s.To[0] != 0 || s.To[1] != 0) && (!inUnitRange(s.To[0]) || !inUnitRange(s.To[1])) {
				return fmt.Errorf("escape.steps[%d] 的 to 必须是 0~1 的归一化坐标", i)
			}
		}
	}

	// 宏：名校验、与按钮名不冲突、步骤坐标合法。宏是「最后交出去执行的确定序列」，
	// 拼错坐标必须在装载时就炸，而不是等老师选了这个宏才在战斗里点空。
	for i := range p.Macros {
		m := &p.Macros[i]
		m.Name = strings.ToLower(strings.TrimSpace(m.Name))
		if m.Name == "" {
			return fmt.Errorf("第 %d 个宏缺少 name", i+1)
		}
		if seen[m.Name] {
			return fmt.Errorf("宏名 %q 与按钮或其它宏重名", m.Name)
		}
		seen[m.Name] = true
		m.State = normalizeState(m.State)
		if m.State == stateInvalid {
			return fmt.Errorf("宏 %q 的 state %q 不合法（可选 world|battle 或留空）", m.Name, m.State)
		}
		for j := range m.Aliases {
			m.Aliases[j] = strings.ToLower(strings.TrimSpace(m.Aliases[j]))
		}
		if !m.Hidden && len(m.Steps) == 0 {
			return fmt.Errorf("宏 %q 至少要有一步（steps 为空）", m.Name)
		}
		for j := range m.Steps {
			s := &m.Steps[j]
			if !inUnitRange(s.Pos[0]) || !inUnitRange(s.Pos[1]) {
				return fmt.Errorf("宏 %q 的 steps[%d] 的 pos 必须是 0~1 的归一化坐标", m.Name, j)
			}
			if s.WaitMs < 0 {
				return fmt.Errorf("宏 %q 的 steps[%d] 的 wait_ms 不能为负", m.Name, j)
			}
			if s.DragMs < 0 {
				return fmt.Errorf("宏 %q 的 steps[%d] 的 drag_ms 不能为负", m.Name, j)
			}
			if (s.To[0] != 0 || s.To[1] != 0) && (!inUnitRange(s.To[0]) || !inUnitRange(s.To[1])) {
				return fmt.Errorf("宏 %q 的 steps[%d] 的 to 必须是 0~1 的归一化坐标", m.Name, j)
			}
		}
	}

	// 战斗态判据：检测带必须是合法矩形；阈值给了就得为正。
	if d := p.BattleDetect; d != nil {
		for k, v := range map[string]float64{
			"band.x0": d.Band[0], "band.y0": d.Band[1],
			"band.x1": d.Band[2], "band.y1": d.Band[3],
		} {
			if !inUnitRange(v) {
				return fmt.Errorf("battle_detect.%s 必须是 0~1 的归一化坐标", k)
			}
		}
		if d.Band[2] <= d.Band[0] || d.Band[3] <= d.Band[1] {
			return fmt.Errorf("battle_detect.band 必须是 [x0,y0,x1,y1] 且 x1>x0、y1>y0")
		}
		if d.Bright < 0 || d.MinPixels < 0 || d.MinClusters < 0 || d.MinGap < 0 {
			return fmt.Errorf("battle_detect 的 bright/min_pixels/min_clusters/min_gap 不能为负")
		}
		// 位置判据：坐标必须归一化，容差必须为正且小于按钮间距量级，
		// 否则会互相串位（把 A 钮的位置匹配到 B 钮上），比不配还危险。
		for i, pos := range d.Positions {
			if !inUnitRange(pos[0]) || !inUnitRange(pos[1]) {
				return fmt.Errorf("battle_detect.positions[%d] 必须是 0~1 的归一化坐标", i)
			}
		}
		if d.TolX < 0 || d.TolY < 0 || d.MinHits < 0 {
			return fmt.Errorf("battle_detect 的 tol_x/tol_y/min_hits 不能为负")
		}
		tolX, tolY := d.tolerances()
		if tolX > 0.05 || tolY > 0.05 {
			return fmt.Errorf("battle_detect 的 tol_x/tol_y 过大（%.3f/%.3f）；超过按钮间距的一半会互相串位", tolX, tolY)
		}
	}
	return nil
}

// 界面态常量。空字符串 = 两态通用；stateInvalid 是 normalizeState 的非法哨值。
const stateInvalid = "__invalid__"

// normalizeState 归一化 state 字段：转小写、去空白；空串保留（表示通用），
// 只接受 world/battle，其它值返回 stateInvalid 由调用方报错。
func normalizeState(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "", StateWorld, StateBattle:
		return s
	default:
		return stateInvalid
	}
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

// Macro 按规范名或别名查找宏（大小写不敏感）。
func (p *Profile) Macro(name string) (Macro, bool) {
	if p == nil {
		return Macro{}, false
	}
	want := strings.ToLower(strings.TrimSpace(name))
	for _, m := range p.Macros {
		if m.Name == want {
			return m, true
		}
		for _, a := range m.Aliases {
			if a == want {
				return m, true
			}
		}
	}
	return Macro{}, false
}

// PressTarget 把一个 PRESS 名解析成「它到底是普通按钮还是宏」。
// 回路据此决定：宏 → 展开 Steps 逐步执行；按钮 → 走原有点击/按键翻译。
//
// 按钮优先于宏：normalize 已保证二者名字不相交，这里的顺序只是防御性约定。
func (p *Profile) PressTarget(name string) (isMacro bool, ok bool) {
	if _, ok := p.Button(name); ok {
		return false, true
	}
	if _, ok := p.Macro(name); ok {
		return true, true
	}
	return false, false
}

// ButtonNames 返回按钮规范名列表，顺序同档案定义（保证提示词可复现）。
// 注意：不含宏、也不过滤 hidden——它是按钮切片的直映，主要用于校验/测试。
// 给老师的协议清单请用 PressList()，那里会合并宏并剔除 hidden。
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

// PressNames 返回老师「可以 PRESS 的全部名字」：非 hidden 的按钮 + 非 hidden 的宏，
// 按档案定义顺序。用于「没有这个按钮」类错误的可用项提示。
func (p *Profile) PressNames() []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(p.Buttons)+len(p.Macros))
	for _, b := range p.Buttons {
		if !b.Hidden {
			out = append(out, b.Name)
		}
	}
	for _, m := range p.Macros {
		if !m.Hidden {
			out = append(out, m.Name)
		}
	}
	return out
}

// buttonLabel 生成单个可 PRESS 项在提示词里的展示文案。
// key 按钮优先展示键位（PC），否则展示 note / 首个别名。
func buttonLabel(name, key, note string, aliases []string) string {
	label := name
	switch {
	case key != "" && note != "":
		label += "(" + key + "：" + note + ")"
	case key != "":
		label += "(" + key + ")"
	case note != "":
		label += "(" + note + ")"
	case len(aliases) > 0:
		label += "(" + aliases[0] + ")"
	}
	return label
}

// PressList 返回带说明的可 PRESS 清单（按钮 + 宏，剔除 hidden），供提示词使用。
//
// 宏以「虚拟按钮」身份出现：模型看到的是 cast_hetu(释放赫突…) 这样一个可选项，
// 完全不需要知道背后是「展开技能盘→点卡→等结算」的多步序列——序列由回路展开。
// hidden 的中转钮（技能展开钮、技能卡）不出现在这里，动作空间因此显著收窄。
func (p *Profile) PressList() []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(p.Buttons)+len(p.Macros))
	for _, b := range p.Buttons {
		if b.Hidden {
			continue
		}
		out = append(out, buttonLabel(b.Name, b.Key, b.Note, b.Aliases))
	}
	for _, m := range p.Macros {
		if m.Hidden {
			continue
		}
		out = append(out, buttonLabel(m.Name, "", m.Note, m.Aliases))
	}
	return out
}

// buttonList 是 PressList 的旧名别名，供原有内部调用与测试习惯沿用。
func (p *Profile) buttonList() []string { return p.PressList() }

// PressNamesForState 返回某界面态下「合法可 PRESS 的名字」（非 hidden 按钮 + 宏），
// 供复读兜底按当前态轮换，避免战斗态跨态去点大世界的坐骑/跳跃。
//
//   - battle 态：只取 State==battle 的项（战斗中世界按钮根本不存在，点了是空操作）；
//   - world  态：取 State==world 与未标态（State==""，默认即大世界）的项。
func (p *Profile) PressNamesForState(state string) []string {
	if p == nil {
		return nil
	}
	want := normalizeState(state)
	ok := func(s string) bool {
		switch want {
		case StateBattle:
			return s == StateBattle
		default: // world 或未知：世界按钮 + 未标态
			return s == StateWorld || s == ""
		}
	}
	out := make([]string, 0)
	for _, b := range p.Buttons {
		if !b.Hidden && ok(b.State) {
			out = append(out, b.Name)
		}
	}
	for _, m := range p.Macros {
		if !m.Hidden && ok(m.State) {
			out = append(out, m.Name)
		}
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
