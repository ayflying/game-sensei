package main

// 适配层穷举守卫：学生能输出的每一个类别都必须有明确的 L1 动作落点。
//
// 为什么值得单独测：适配层的 default 分支曾经把「未知类别」和
// wait/none 合并处理，都是「什么都不做」。于是权重类别与代码不一致时，
// 症状表现成「学生有点笨」，而不是一个能定位的错误。

import (
	"testing"

	"github.com/ayflying/game-sensei/internal/agent"
	"github.com/ayflying/game-sensei/internal/game"
	"github.com/ayflying/game-sensei/internal/student"
)

// TestClassToAction穷举全部类别 保证没有类别落进「未识别」分支。
func TestClassToAction穷举全部类别(t *testing.T) {
	prof := &game.Profile{}
	for _, c := range student.Classes {
		if _, ok := classToAction(c, 0.5, 0.5, prof); !ok {
			t.Fatalf("类别 %q 没有映射到 L1 动作（会静默退化成 WAIT）", c)
		}
	}
	// 真·未知类别必须被识别为未知，而不是悄悄当 WAIT
	if _, ok := classToAction("bogus", 0.5, 0.5, prof); ok {
		t.Fatal("未知类别应当返回 ok=false")
	}
}

// TestClassToAction8向落成Move 保证 8 个方向都被翻译成 ActionMove 且方向保真。
func TestClassToAction8向落成Move(t *testing.T) {
	prof := &game.Profile{}
	for _, d := range agent.AllDirs {
		act, ok := classToAction(string(d), 0, 0, prof)
		if !ok {
			t.Fatalf("方向 %q 未被识别", d)
		}
		if act.Kind != agent.ActionMove {
			t.Fatalf("方向 %q 的动作类型是 %v，期望 Move", d, act.Kind)
		}
		if act.Dir != d {
			t.Fatalf("方向 %q 被翻成 %q（方向丢失）", d, act.Dir)
		}
		if act.Dur <= 0 {
			t.Fatalf("方向 %q 的持续时长为 %v，会导致零位移", d, act.Dur)
		}
	}
}

// TestPress退回档案首个按钮 固化当前已知天花板的行为，避免无意变更。
// 学术上的正解是「按钮头」，但那是下一版；在换掉之前，行为要稳定可见。
func TestPress退回档案首个按钮(t *testing.T) {
	prof := &game.Profile{Buttons: []game.Button{{Name: "gather_energy"}, {Name: "cast_hetu"}}}
	act, ok := classToAction("press", 0, 0, prof)
	if !ok {
		t.Fatal("press 未被识别")
	}
	if act.Kind != agent.ActionPress || act.Name != "gather_energy" {
		t.Fatalf("press 落点 = (%v,%q)，期望 (Press,gather_energy)", act.Kind, act.Name)
	}

	// 无档案/无按钮时不得瞎按
	act, _ = classToAction("press", 0, 0, nil)
	if act.Kind != agent.ActionNone {
		t.Fatalf("无档案时 press 应退化为 None，实际 %v", act.Kind)
	}
}
