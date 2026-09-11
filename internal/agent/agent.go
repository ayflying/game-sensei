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

const (
	ActionNone      ActionKind = iota // 不动作
	ActionKey                         // 离散键位短按（Code 指定，PC 键鼠）
	ActionMove                        // 鼠标相对移动（Dx/Dy 指定）
	ActionTap                         // 触摸点按（Nx/Ny 归一化坐标，手游）
	ActionSwipe                       // 触摸滑动（Nx,Ny 起点 -> Nx2,Ny2 终点，Dur 时长）
	ActionLongPress                   // 触摸长按（Nx,Ny 按下并保持 Dur）
)

// Action 是一次决策输出。字段按需取用。
//
// 坐标统一用归一化值（0~1，相对当前屏幕宽高）：同一套策略可跨分辨率、
// 跨设备复用，执行后端（PC 键鼠 / Android 触摸）负责换算成实际坐标。
type Action struct {
	Kind ActionKind
	Code string // ActionKey 时的键名：left/up/right/down（PC）或 back/home（Android）

	Dx int // ActionMove 时的相对位移
	Dy int

	Nx, Ny   float64       // ActionTap/ActionSwipe/ActionLongPress 的坐标或起点
	Nx2, Ny2 float64       // ActionSwipe 的终点
	Dur      time.Duration // ActionSwipe 的手势时长 / ActionLongPress 的按住时长
}

// String 返回可读的动作描述，用于日志与轨迹记录。
func (a Action) String() string {
	switch a.Kind {
	case ActionKey:
		return "key:" + a.Code
	case ActionMove:
		return fmt.Sprintf("move:%+d,%+d", a.Dx, a.Dy)
	case ActionTap:
		return fmt.Sprintf("tap:%.3f,%.3f", a.Nx, a.Ny)
	case ActionSwipe:
		return fmt.Sprintf("swipe:%.3f,%.3f->%.3f,%.3f/%dms",
			a.Nx, a.Ny, a.Nx2, a.Ny2, a.Dur.Milliseconds())
	case ActionLongPress:
		return fmt.Sprintf("hold:%.3f,%.3f/%dms", a.Nx, a.Ny, a.Dur.Milliseconds())
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
// 比较画面左半与右半的平均亮度，向「更亮」一侧按方向键，
// 模拟一个「朝目标移动」的最小可解释策略。
type RuleActor struct {
	step int // 内部计数器，用于产生周期性左右试探（保证有动作输出）
}

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
		return Action{Kind: ActionKey, Code: "right"}, nil
	case leftAvg-rightAvg > threshold:
		return Action{Kind: ActionKey, Code: "left"}, nil
	default:
		// 几乎持平：交替轻推，产生持续动作输出
		r.step++
		if r.step%2 == 0 {
			return Action{Kind: ActionKey, Code: "left"}, nil
		}
		return Action{Kind: ActionKey, Code: "right"}, nil
	}
}
