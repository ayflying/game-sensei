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
	fmt.Printf("学生: %s（训练验证准确率 %.1f%%）\n", weightsPath, net.MetaValAcc()*100)
	return &studentActor{net: net, prof: prof}, nil
}

func (s *studentActor) Name() string { return "student-cnn" }

// decideMs 是学生连续输出方向动作时的保持时长（毫秒）。
// 与规则学生的 ruleMoveMs 同量级：一次指令产生可观测位移。
const decideMs = 300

// Decide 看一帧灰度图输出 L1 动作。翻译规则：
//
//	up/down/left/right -> ActionMove（档案决定落地成 WASD 还是摇杆）
//	tap                -> ActionTap（学生回归的归一化坐标）
//	press              -> ActionPress（按档案第一个按钮：学生不区分按钮名，
//	                      具体点哪由档案与坐标头分担；当前版本保守映射为 WAIT）
//	wait / none        -> ActionNone
func (s *studentActor) Decide(frame *image.Gray) (agent.Action, error) {
	out, err := s.net.Decide(frame)
	if err != nil {
		return agent.Action{}, err
	}
	switch out.Class {
	case "up", "down", "left", "right":
		return agent.Action{
			Kind: agent.ActionMove,
			Dir:  agent.Dir(out.Class),
			Dur:  decideMs * time.Millisecond,
		}, nil
	case "tap":
		return agent.Action{Kind: agent.ActionTap, Nx: out.X, Ny: out.Y}, nil
	case "press":
		// 学生版本 1 不区分按钮名（按钮语义是档案知识，不是视觉知识）。
		// 若档案有按钮，取档案顺序第一个；没有就退化为 WAIT，避免瞎按。
		if s.prof != nil && len(s.prof.Buttons) > 0 {
			return agent.Action{Kind: agent.ActionPress, Name: s.prof.Buttons[0].Name}, nil
		}
		return agent.Action{Kind: agent.ActionNone}, nil
	default: // wait / none / 未知类别
		return agent.Action{Kind: agent.ActionNone}, nil
	}
}
