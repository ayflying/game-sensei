package game

import (
	"fmt"
	"strings"
	"time"

	"github.com/ayflying/game-sensei/internal/agent"
)

// wasdKeys 是 keys（PC 键鼠）模式下的默认方向键位。
//
// 四个正向给默认值；斜向由 wasdDiagonal 组合两个正向键（右下 = s+d）。
// 若档案在 move.keys 里显式给了某个方向（含斜向），以档案为准。
var wasdKeys = map[agent.Dir]string{
	agent.DirUp:    "w",
	agent.DirDown:  "s",
	agent.DirLeft:  "a",
	agent.DirRight: "d",
}

// wasdDiagonal 是 keys 模式下斜向移动的默认双键组合。
//
// PC 键盘游戏斜着走要同时按两个方向键，单个 Code 表达不了，
// 故这里返回一对键，由后端用 Codes 同时按住。
var wasdDiagonal = map[agent.Dir][2]string{
	agent.DirUpLeft:    {"w", "a"},
	agent.DirUpRight:   {"w", "d"},
	agent.DirDownLeft:  {"s", "a"},
	agent.DirDownRight: {"s", "d"},
}

// dpadKeys 是 dpad 模式下的默认键位（Android keyevent 名，后端会补 KEYCODE_ 前缀）。
var dpadKeys = map[agent.Dir]string{
	agent.DirUp:        "dpad_up",
	agent.DirDown:      "dpad_down",
	agent.DirLeft:      "dpad_left",
	agent.DirRight:     "dpad_right",
	agent.DirUpLeft:    "dpad_up_left",
	agent.DirUpRight:   "dpad_up_right",
	agent.DirDownLeft:  "dpad_down_left",
	agent.DirDownRight: "dpad_down_right",
}

// Resolve 把语义动作（L1）展开为平台可执行动作（L2）。
//
// 分工：L1 是「说得出口的游戏操作」（朝前走、按跳跃），与游戏、分辨率、
// 平台都无关；L2 是「怎么让这台设备做到」（推摇杆到某坐标、按住某键）。
// 翻译需要的信息全在档案里，所以换游戏只需换档案。
//
// 已经是 L2 的动作原样返回，因此本函数是**幂等**的：
// 后端可以无脑先 Resolve 再执行，不必分辨动作来自模型还是来自别的环节。
func (p *Profile) Resolve(a agent.Action) (agent.Action, error) {
	switch a.Kind {
	case agent.ActionMove:
		return p.resolveMove(a)
	case agent.ActionPress:
		return p.resolvePress(a)
	default:
		return a, nil
	}
}

// resolveMove 把「方向 + 时长」翻译成摇杆推杆或按键按住。
func (p *Profile) resolveMove(a agent.Action) (agent.Action, error) {
	if p == nil {
		return agent.Action{}, fmt.Errorf("game: 未加载游戏档案，无法执行 MOVE（用 -game 指定档案）")
	}
	if !a.Dir.IsValid() {
		return agent.Action{}, fmt.Errorf("game: MOVE 的方向 %q 不合法", string(a.Dir))
	}
	dur := a.Dur
	if dur <= 0 {
		dur = time.Duration(agent.DefaultMoveMs) * time.Millisecond
	}

	switch p.Move.Mode {
	case MoveJoystick:
		vx, vy := a.Dir.Vector()
		cx, cy := p.Move.Center[0], p.Move.Center[1]
		rx, ry := p.Move.radius()
		// 摇杆推杆：从中心朝方向推一个幅度。屏幕 y 轴向下，Vector 已按屏幕坐标返回。
		tx := clamp01(cx + vx*rx)
		ty := clamp01(cy + vy*ry)
		return agent.Action{
			Kind: agent.ActionJoystick,
			Nx:   cx, Ny: cy,
			Nx2: tx, Ny2: ty,
			Dur: dur,
		}, nil

	case MoveKeys, MoveDpad:
		// keys 模式斜向：需要同时按两个键（右下 = s+d），走 Codes。
		if p.Move.Mode == MoveKeys {
			if combo, ok := p.moveCombo(a.Dir); ok {
				return agent.Action{Kind: agent.ActionKey, Codes: combo, Dur: dur}, nil
			}
		}
		key := p.Move.keyFor(a.Dir)
		if key == "" {
			return agent.Action{}, fmt.Errorf(
				"game: 档案 %q（%s 模式）没有为方向 %s 配置按键",
				p.Name, p.Move.Mode, a.Dir)
		}
		// Dur>0 让后端「按住」而不是点一下：移动是持续行为，点一下等于原地抖。
		return agent.Action{Kind: agent.ActionKey, Code: key, Dur: dur}, nil

	case MoveNone, "":
		return agent.Action{}, fmt.Errorf(
			"game: 档案 %q 未配置移动方式（move.mode=none），MOVE 动作无法执行", p.Name)
	}
	return agent.Action{}, fmt.Errorf("game: 未知的移动方式 %q", p.Move.Mode)
}

// resolvePress 把按钮名翻译成一次按键或点击。
//
// 优先用 Key（PC 键盘游戏：PRESS jump = 按空格），没有配 Key 才退回点击坐标
// （手游：PRESS jump = 点右下角那个按钮）。这样同一份语义动作在两个平台上
// 各自落到正确的执行方式，模型完全不用知道底层是键还是触摸。
func (p *Profile) resolvePress(a agent.Action) (agent.Action, error) {
	if p == nil {
		return agent.Action{}, fmt.Errorf("game: 未加载游戏档案，无法执行 PRESS（用 -game 指定档案）")
	}
	// 宏是「多步序列」，不能被 Resolve 拍成单个点击。正常路径下回路会先拦截宏、
	// 逐步展开执行，根本不会走到这里；走到了说明调用方漏了宏展开，明确报错而不是
	// 静默点宏第一步（那正是小模型在技能盘上反复犯的错）。
	if _, ok := p.Macro(a.Name); ok {
		return agent.Action{}, fmt.Errorf(
			"game: %q 是宏而非单按钮，应由回路展开执行（不能直接 Resolve）", a.Name)
	}
	btn, ok := p.Button(a.Name)
	if !ok {
		avail := strings.Join(p.PressNames(), "|")
		if avail == "" {
			avail = "（该档案未定义任何按钮或宏）"
		}
		return agent.Action{}, fmt.Errorf("game: 档案 %q 里没有按钮或宏 %q；可用：%s",
			p.Name, a.Name, avail)
	}
	if btn.Key != "" {
		// PRESS 是"点一下"语义（不像 MOVE 要按住），Dur 留 0 → 后端做按下+抬起。
		return agent.Action{Kind: agent.ActionKey, Code: btn.Key}, nil
	}
	return agent.Action{Kind: agent.ActionTap, Nx: btn.Pos[0], Ny: btn.Pos[1]}, nil
}

// keyFor 取方向对应的键名：档案显式配置优先，否则用该模式的默认键位。
func (m MoveProfile) keyFor(d agent.Dir) string {
	if k := strings.TrimSpace(m.Keys[string(d)]); k != "" {
		return k
	}
	switch m.Mode {
	case MoveKeys:
		return wasdKeys[d]
	case MoveDpad:
		return dpadKeys[d]
	}
	return ""
}

// moveCombo 返回斜向移动的双键组合（仅 keys 模式有意义）。
//
// 优先查档案 move.keys 里方向值写成 "a+d" 这类组合的显式配置；
// 没有则用 WASD 默认组合。正向/非 keys 模式返回 ok=false，
// 交回 keyFor 的单键路径处理。
func (p *Profile) moveCombo(d agent.Dir) ([]string, bool) {
	if p.Move.Mode != MoveKeys {
		return nil, false
	}
	if !d.IsDiagonal() {
		return nil, false
	}
	// 档案显式给了这个方向就完全尊重：组合键（"s+d"）走 Codes，
	// 单键（"q"）返回 false 交给 keyFor 的单键路径，不与默认组合混用。
	if spec := strings.TrimSpace(p.Move.Keys[string(d)]); spec != "" {
		if parts := splitKeys(spec); len(parts) >= 2 {
			return parts, true
		}
		return nil, false
	}
	if combo, ok := wasdDiagonal[d]; ok {
		return []string{combo[0], combo[1]}, true
	}
	return nil, false
}

// splitKeys 把 "s+d" / "s,d" / "s d" 拆成键名列表（去空白、去空项）。
func splitKeys(s string) []string {
	raw := strings.NewReplacer(",", "+", " ", "+", "\t", "+").Replace(s)
	parts := strings.Split(raw, "+")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ProtocolOptions 生成「给老师看的动作协议」配置。
//
// 只把该游戏真正能做的动作列出去：没有摇杆就别提 MOVE、没配按钮就别提 PRESS。
// 让模型看见的动作空间越小越准，是提升小模型输出质量最省事的手段。
func (p *Profile) ProtocolOptions() agent.ProtocolOptions {
	return p.ProtocolOptionsForState("")
}

// ProtocolOptionsForState 与 ProtocolOptions 相同，但按当前界面态收窄动作空间：
//   - battle 态：只列 State=battle 的可 PRESS 项（技能宏/捕捉/更换/背包/逃跑），
//     并关闭 MOVE 与自由坐标（TAP/SWIPE/HOLD）——战斗界面没有摇杆也没有
//     可自由点击的区域，pet_run10 实测老师在战斗里连点 7 步自由坐标全部空耗；
//   - world 态或未知（""）：列出全部非 hidden 项，保留档案的移动方式。
//
// 只在强信号的战斗态收窄是刻意的：战斗态判据（底部≥3 个圆钮）几乎不会误判，
// 而把「其实在战斗」误当大世界、仍给一堆世界按钮和 MOVE 的代价要大得多。
func (p *Profile) ProtocolOptionsForState(state string) agent.ProtocolOptions {
	if p == nil {
		// 无档案也要能跑：自由坐标保留（通用协议），MOVE/PRESS 无从谈起。
		return agent.ProtocolOptions{AllowFreePointer: true}
	}
	// 自由坐标默认放开；只有明确的战斗态才收。
	o := agent.ProtocolOptions{
		// 先验按界面态分层注入：通用 + 该态专属。原为 p.Hints（无条件全量注入，
		// 大世界态白背 1404 字符战斗先验）。未知态按 world 处理，见 HintsForState。
		Hints:            p.HintsForState(state),
		HasMove:          p.Move.Mode != MoveNone && p.Move.Mode != "",
		MoveNote:         p.Move.moveNote(),
		AllowFreePointer: true,
	}
	if normalizeState(state) == StateBattle {
		// 战斗按钮/宏的带说明清单：复用 buttonLabel 保证格式与全量一致。
		labels := make([]string, 0)
		for _, b := range p.Buttons {
			if !b.Hidden && b.State == StateBattle {
				labels = append(labels, buttonLabel(b.Name, b.Key, b.Note, b.Aliases))
			}
		}
		for _, m := range p.Macros {
			if !m.Hidden && m.State == StateBattle {
				labels = append(labels, buttonLabel(m.Name, "", m.Note, m.Aliases))
			}
		}
		o.Buttons = labels
		o.HasMove = false
		o.MoveNote = ""
		o.AllowFreePointer = false
		return o
	}
	o.Buttons = p.buttonList()
	return o
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
