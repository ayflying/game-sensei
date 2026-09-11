package game

import (
	"fmt"
	"strings"
	"time"

	"github.com/ayflying/game-sensei/internal/agent"
)

// wasdKeys 是 keys（PC 键鼠）模式下的默认方向键位。
//
// 斜向**故意不给默认值**：PC 上斜向移动要同时按两个键，而 ActionKey 只表达单键。
// 与其静默退化成一个方向（模型以为在斜着走、实际只走了直行），
// 不如让它在执行前明确报错——错误可见比错误隐蔽好。
// 需要斜向的游戏请在档案里显式配置 move.keys。
var wasdKeys = map[agent.Dir]string{
	agent.DirUp:    "w",
	agent.DirDown:  "s",
	agent.DirLeft:  "a",
	agent.DirRight: "d",
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
		key := p.Move.keyFor(a.Dir)
		if key == "" {
			return agent.Action{}, fmt.Errorf(
				"game: 档案 %q（%s 模式）没有为方向 %s 配置按键；"+
					"斜向移动需要同时按多个键，请在档案的 move.keys 里显式指定",
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

// resolvePress 把按钮名翻译成一次点击。
func (p *Profile) resolvePress(a agent.Action) (agent.Action, error) {
	if p == nil {
		return agent.Action{}, fmt.Errorf("game: 未加载游戏档案，无法执行 PRESS（用 -game 指定档案）")
	}
	btn, ok := p.Button(a.Name)
	if !ok {
		avail := strings.Join(p.ButtonNames(), "|")
		if avail == "" {
			avail = "（该档案未定义任何按钮）"
		}
		return agent.Action{}, fmt.Errorf("game: 档案 %q 里没有按钮 %q；可用按钮：%s",
			p.Name, a.Name, avail)
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

// ProtocolOptions 生成「给老师看的动作协议」配置。
//
// 只把该游戏真正能做的动作列出去：没有摇杆就别提 MOVE、没配按钮就别提 PRESS。
// 让模型看见的动作空间越小越准，是提升小模型输出质量最省事的手段。
func (p *Profile) ProtocolOptions() agent.ProtocolOptions {
	if p == nil {
		return agent.ProtocolOptions{}
	}
	return agent.ProtocolOptions{
		Hints:    p.Hints,
		Buttons:  p.buttonList(),
		HasMove:  p.Move.Mode != MoveNone && p.Move.Mode != "",
		MoveNote: p.Move.moveNote(),
	}
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
