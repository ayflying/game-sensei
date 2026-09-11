// Package agent 定义「学生」Actor 接口及其规则实现（Phase 0）。
//
// Phase 0 不接神经网络：用一个可解释的规则策略先打通
// capture -> Decide -> input 的实时回路并验证延迟。真正的量化 ONNX
// 学生将在 Phase 2 通过实现同一 Actor 接口接入（onnxruntime.dll 绑定，见 README §11.2）。
//
// 本包与平台无关（不依赖 syscall），便于单测。
package agent

import (
	"fmt"
	"image"
	"time"
)

// ActionKind 是动作类型。
type ActionKind int

// 动作分两层。理解这两层的分工，是理解整个动作空间的关键。
//
//	L1 语义层（跨游戏通用，老师/学生输出的就是它）
//	  ActionMove   朝某方向持续移动（Dir + Dur）
//	  ActionPress  按「命名按钮」（Name，坐标由游戏档案给出）
//	  ActionTap    点画面某处（归一化坐标）
//	  ActionSwipe  拖动/转视角
//	  ActionLongPress 长按
//	  ActionKey    发送一个按键（返回/确认等系统键）
//	  ActionNone   不动，等画面变化
//
//	L2 执行层（平台相关，由后端消费）
//	  ActionJoystick  触摸虚拟摇杆（中心的推杆坐标）
//	  ActionMouseMove 鼠标相对位移
//
// L1 是「说得出口的游戏操作」，与具体游戏、分辨率、平台都无关；
// L2 是「怎么让这台设备做到」。中间由 GameProfile 负责翻译
// （internal/game.Resolve），换游戏只换一份档案，动作空间本身不动。
const (
	ActionNone      ActionKind = iota // 不动作
	ActionKey                         // 离散键位（Code 指定）；Dur>0 表示按住，否则点按
	ActionMove                        // L1 方向移动（Dir 方向 + Dur 时长）
	ActionTap                         // 触摸点按（Nx/Ny 归一化坐标，手游）
	ActionSwipe                       // 触摸滑动（Nx,Ny 起点 -> Nx2,Ny2 终点，Dur 时长）
	ActionLongPress                   // 触摸长按（Nx,Ny 按下并保持 Dur）
	ActionJoystick                    // L2 虚拟摇杆（Nx,Ny 摇杆中心 -> Nx2,Ny2 推到的目标点，Dur 保持时长）
	ActionPress                       // L1 按命名按钮（Name 指定，坐标由游戏档案解析）
	ActionMouseMove                   // L2 鼠标相对移动（Dx/Dy 指定）
)

// Action 是一次决策输出。字段按需取用。
//
// 坐标统一用归一化值（0~1，相对当前屏幕宽高）：同一套策略可跨分辨率、
// 跨设备复用，执行后端（PC 键鼠 / Android 触摸）负责换算成实际坐标。
type Action struct {
	Kind ActionKind

	// Dir 是 ActionMove 的移动方向（8 向之一）。
	// 用方向而非摇杆坐标：摇杆中心是游戏常量，不该由模型每帧回归。
	Dir Dir

	// Name 是 ActionPress 的按钮名（游戏档案里定义的规范名或别名，如 jump）。
	Name string

	Code string // ActionKey 时的键名：w/a/s/d（PC）或 back/home（Android）

	Dx int // ActionMouseMove 时的相对位移
	Dy int

	Nx, Ny   float64       // ActionTap/ActionSwipe/ActionLongPress/ActionJoystick 的坐标或起点
	Nx2, Ny2 float64       // ActionSwipe 的终点；ActionJoystick 的推杆目标点
	Dur      time.Duration // 手势时长 / 按住时长 / 摇杆保持时长；ActionKey 上 >0 表示按住
}

// String 返回可读的动作描述，用于日志与轨迹记录。
func (a Action) String() string {
	switch a.Kind {
	case ActionKey:
		if a.Dur > 0 {
			return fmt.Sprintf("key:%s/%dms", a.Code, a.Dur.Milliseconds())
		}
		return "key:" + a.Code
	case ActionMove:
		if a.Dur > 0 {
			return fmt.Sprintf("move:%s/%dms", a.Dir, a.Dur.Milliseconds())
		}
		return "move:" + string(a.Dir)
	case ActionPress:
		return "press:" + a.Name
	case ActionMouseMove:
		return fmt.Sprintf("mouse:%+d,%+d", a.Dx, a.Dy)
	case ActionTap:
		return fmt.Sprintf("tap:%.3f,%.3f", a.Nx, a.Ny)
	case ActionSwipe:
		return fmt.Sprintf("swipe:%.3f,%.3f->%.3f,%.3f/%dms",
			a.Nx, a.Ny, a.Nx2, a.Ny2, a.Dur.Milliseconds())
	case ActionLongPress:
		return fmt.Sprintf("hold:%.3f,%.3f/%dms", a.Nx, a.Ny, a.Dur.Milliseconds())
	case ActionJoystick:
		return fmt.Sprintf("joy:%.3f,%.3f->%.3f,%.3f/%dms",
			a.Nx, a.Ny, a.Nx2, a.Ny2, a.Dur.Milliseconds())
	default:
		return "none"
	}
}

// Actor 是「学生」的抽象：给定一帧灰度观测，输出一个动作。
// 未来量化网络实现该接口即可无缝替换规则策略。
type Actor interface {
	Decide(frame *image.Gray) (Action, error)
	// Name 返回策略标识（用于日志）。
	Name() string
}

// RuleActor 是 Phase 0 的规则学生：
// 比较画面左半与右半的平均亮度，向「更亮」一侧移动，
// 模拟一个「朝目标移动」的最小可解释策略。
//
// ⚠️ 它输出的是 **L1 语义动作**（ActionMove + 方向），不是具体按键。
// 这一点很重要：早期它直接输出 key:left / key:right，在 PC 上碰巧能用
// （方向键），到了 Android 就被翻译成 KEYCODE_LEFT 这种无效键码。
// 走 L1 之后，同一套策略在 PC 上落成 WASD、在手机上落成虚拟摇杆推杆，
// 平台差异由游戏档案吸收——这正是分层的意义。
type RuleActor struct {
	step int // 内部计数器，用于产生周期性左右试探（保证有动作输出）
}

// ruleMoveMs 是规则学生每次移动指令的保持时长。
//
// 取 300ms 的理由：实时回路最高 30FPS（约 33ms/帧），300ms 确保一次指令
// 能产生肉眼与像素都可观测的位移；同时后端对「按住」做了代次刷新，
// 每帧重复下达会持续按住，不会因为时长偏短而一顿一顿。
const ruleMoveMs = 300

// NewRule 构造规则学生。
func NewRule() *RuleActor { return &RuleActor{} }

// Name 实现 Actor。
func (r *RuleActor) Name() string { return "rule-stub" }

// Decide 实现 Actor。演示用启发式：亮度偏向哪侧就往哪侧走一步；
// 若左右几乎相等，则交替轻推左右，保证动作链路持续有输出。
func (r *RuleActor) Decide(frame *image.Gray) (Action, error) {
	if frame == nil || frame.Bounds().Dx() == 0 {
		return Action{Kind: ActionNone}, nil
	}
	b := frame.Bounds()
	mid := (b.Min.X + b.Max.X) / 2

	var leftSum, rightSum int
	var leftN, rightN int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		row := y * frame.Stride
		for x := b.Min.X; x < b.Max.X; x++ {
			v := int(frame.Pix[row+x])
			if x < mid {
				leftSum += v
				leftN++
			} else {
				rightSum += v
				rightN++
			}
		}
	}
	var leftAvg, rightAvg int
	if leftN > 0 {
		leftAvg = leftSum / leftN
	}
	if rightN > 0 {
		rightAvg = rightSum / rightN
	}

	const threshold = 5 // 亮度差阈值（0-255）
	switch {
	case rightAvg-leftAvg > threshold:
		return moveAction(DirRight), nil
	case leftAvg-rightAvg > threshold:
		return moveAction(DirLeft), nil
	default:
		// 几乎持平：交替轻推，产生持续动作输出
		r.step++
		if r.step%2 == 0 {
			return moveAction(DirLeft), nil
		}
		return moveAction(DirRight), nil
	}
}

// moveAction 构造一次方向移动（L1）。
func moveAction(d Dir) Action {
	return Action{Kind: ActionMove, Dir: d, Dur: ruleMoveMs * time.Millisecond}
}
