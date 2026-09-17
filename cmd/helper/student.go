// 学生模式：加载量化学生权重接入实时回路（Phase 2 蒸馏闭环）。
//
// 适配层职责：把 internal/student 的原始输出（8 类 + 坐标）翻译成
// agent.Action（L1 语义动作），保持「学生只管看画面，档案负责落地」的分层。
package main

import (
	"fmt"
	"image"
	"time"

	"github.com/ayflying/game-sensei/internal/agent"
	"github.com/ayflying/game-sensei/internal/game"
	"github.com/ayflying/game-sensei/internal/student"
)

// studentActor 把 student.Net 包装成 agent.Actor。
type studentActor struct {
	net  *student.Net
	prof *game.Profile
}

// NewStudentActor 加载权重文件构造学生 Actor。
func NewStudentActor(weightsPath string, prof *game.Profile) (agent.Actor, error) {
	net, err := student.Load(weightsPath)
	if err != nil {
		return nil, err
	}
	kind := "灰度"
	if net.InChannels() == 3 {
		kind = "彩色"
	}
	fmt.Printf("学生: %s（%s输入 inC=%d，训练验证准确率 %.1f%%）\n",
		weightsPath, kind, net.InChannels(), net.MetaValAcc()*100)
	return &studentActor{net: net, prof: prof}, nil
}

func (s *studentActor) Name() string { return "student-cnn" }

// NeedsColor 实现 agent.Actor：彩色模型必须拿到彩色观测——
// v8/v9 实测彩色是分类头摆脱塌缩的决定性变量，喂灰度帧等于让模型失明。
func (s *studentActor) NeedsColor() bool { return s.net.InChannels() == 3 }

// decideMs 是学生连续输出方向动作时的保持时长（毫秒）。
// 与规则学生的 ruleMoveMs 同量级：一次指令产生可观测位移。
const decideMs = 300

// Decide 看一帧画面输出 L1 动作。帧类型自适应（灰度/彩色权重都走这里），
// 翻译规则见 classToAction。
func (s *studentActor) Decide(img image.Image) (agent.Action, error) {
	out, err := s.net.DecideImage(img)
	if err != nil {
		return agent.Action{}, err
	}
	act, ok := classToAction(out.Class, out.X, out.Y, s.prof)
	if !ok {
		// 无法映射不能静默变成「什么都不做」——那会让「权重与代码版本不匹配」
		// 或「档案缺 student_semantics 映射」这种严重问题表现成「模型有点笨」，
		// 没人会去查。student.Load 已校验类别顺序，正常不会走到这里；
		// 真走到了就必须在日志里可见。
		fmt.Printf("⚠️ 学生类别 %q 无法映射为动作（权重类别不匹配，或档案缺 student_semantics 映射？）本步按 WAIT 处理\n", out.Class)
		return agent.Action{Kind: agent.ActionNone}, nil
	}
	return act, nil
}

// classToAction 是 class → L1 动作的**唯一**映射点，纯函数，便于测试穷举。
//
//	8 向（与 agent.AllDirs 同集合）-> ActionMove（档案决定落地成摇杆还是 WASD）
//	tap                        -> ActionTap（学生回归的归一化坐标）
//	tap_pk/tap_start/tap_pick  -> ActionTap（坐标从档案 student_semantics 查按钮）
//	back                       -> ActionKey（系统返回键，导航广告场景）
//	press                      -> ActionPress（按档案第一个按钮）
//	wait / none                -> ActionNone
//
// 无法落地（未知类名、档案未配 student_semantics、按钮名拼错）一律返回
// ok=false，由 Decide 打可见警告并按 WAIT 处理——不许静默乱点。
//
// ⚠️ 已知天花板（press）：学生头只输出「按一下」这个意图，不带按钮名，
// 这里只能退回「按档案 Buttons[0]」。老师的战斗宏（聚能/赫突/逃跑）都是
// **不同的**命名按钮，所以学生即便学会了「此刻该按」，也按不对按钮。
// 要真正复现战斗策略，需要给学生加一个「按钮头」（对档案 Buttons 多分类）。
func classToAction(cls string, x, y float64, prof *game.Profile) (agent.Action, bool) {
	switch cls {
	case "up", "down", "left", "right",
		"up_left", "up_right", "down_left", "down_right":
		return agent.Action{
			Kind: agent.ActionMove,
			Dir:  agent.Dir(cls),
			Dur:  decideMs * time.Millisecond,
		}, true
	case "tap":
		return agent.Action{Kind: agent.ActionTap, Nx: x, Ny: y}, true
	case "tap_pk", "tap_start", "tap_pick":
		// 命名按钮语义类（端到端扩展 2026-09-16）：学生输出「点哪个语义按钮」，
		// 坐标由档案的 student_semantics 映射到按钮名再查坐标——学生只管看画面
		// 决策，坐标是档案知识（换游戏只改 JSON）。档案没配或按钮名拼错时不猜，
		// 返回 false 让 Decide 打可见警告（宁可不点，不能点错）。
		if prof == nil {
			return agent.Action{}, false
		}
		name, ok := prof.StudentSemantics[cls]
		if !ok {
			return agent.Action{}, false
		}
		b, ok := prof.Button(name)
		if !ok {
			return agent.Action{}, false
		}
		if b.Key != "" {
			// PC 键盘档案：语义类落地成按该命名键（与 PRESS 同一条路径）。
			return agent.Action{Kind: agent.ActionPress, Name: b.Name}, true
		}
		return agent.Action{Kind: agent.ActionTap, Nx: b.Pos[0], Ny: b.Pos[1]}, true
	case "back":
		// 系统返回键（导航广告场景）：L1 的 ActionKey 就是「按一下 Android back」。
		return agent.Action{Kind: agent.ActionKey, Code: "back"}, true
	case "press":
		// 学生版本 1 不区分按钮名（按钮语义是档案知识，不是视觉知识）。
		// 若档案有按钮，取档案顺序第一个；没有就退化为 WAIT，避免瞎按。
		if prof != nil && len(prof.Buttons) > 0 {
			return agent.Action{Kind: agent.ActionPress, Name: prof.Buttons[0].Name}, true
		}
		return agent.Action{Kind: agent.ActionNone}, true
	case "wait", "none":
		return agent.Action{Kind: agent.ActionNone}, true
	default:
		return agent.Action{}, false
	}
}
