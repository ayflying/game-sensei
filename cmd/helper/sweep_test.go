package main

// 扫掠序列守卫。
//
// 为什么值得单独测：sweep 的全部价值在于「标签是确定性真值」。一旦 SweepStep
// 漏掉某个方向、顺序错位，或把 hold 时长丢掉（零位移），采出来的就不是
// 均衡的 8 向数据，而是带偏差的废样本——而它同样「不报错」。

import (
	"testing"
	"time"

	"github.com/ayflying/game-sensei/internal/agent"
)

// TestSweepStep覆盖全部方向：一轮 len(AllDirs) 步必须恰好遍历每个方向一次。
func TestSweepStep覆盖全部方向(t *testing.T) {
	n := len(agent.AllDirs)
	if n == 0 {
		t.Fatal("AllDirs 为空")
	}
	seen := make(map[agent.Dir]int, n)
	for i := 0; i < n; i++ {
		act, name := SweepStep(i, 1000)
		if act.Kind != agent.ActionMove {
			t.Fatalf("第 %d 步动作类型是 %v，期望 Move", i, act.Kind)
		}
		if name != string(act.Dir) {
			t.Fatalf("第 %d 步方向名 %q 与动作方向 %q 不一致（标签会错标）", i, name, act.Dir)
		}
		if act.Dir != agent.AllDirs[i] {
			t.Fatalf("第 %d 步方向是 %q，期望 %q（顺序错位）", i, act.Dir, agent.AllDirs[i])
		}
		seen[act.Dir]++
	}
	for _, d := range agent.AllDirs {
		if seen[d] != 1 {
			t.Fatalf("方向 %q 在一轮里出现 %d 次，期望恰好 1 次", d, seen[d])
		}
	}
	if len(seen) != n {
		t.Fatalf("一轮覆盖了 %d 个方向，期望 %d 个（有方向被漏掉）", len(seen), n)
	}
}

// TestSweepStep按轮循环：第 i 步与第 i+n 步应完全一致。
func TestSweepStep按轮循环(t *testing.T) {
	n := len(agent.AllDirs)
	for i := 0; i < n*3; i++ {
		a, na := SweepStep(i, 1200)
		b, nb := SweepStep(i+n, 1200)
		if a.Kind != b.Kind || a.Dir != b.Dir || a.Dur != b.Dur || na != nb {
			t.Fatalf("第 %d 步 (%v,%q,%v) 与第 %d 步 (%v,%q,%v) 不一致，说明不是按轮循环",
				i, a.Kind, a.Dir, a.Dur, i+n, b.Kind, b.Dir, b.Dur)
		}
	}
}

// TestSweepStep时长生效：hold 时长决定位移，丢了就是零位移废样本。
func TestSweepStep时长生效(t *testing.T) {
	act, _ := SweepStep(0, 1500)
	if act.Dur != 1500*time.Millisecond {
		t.Fatalf("hold=1500 时 Dur=%v，期望 1.5s", act.Dur)
	}
	// 非正 hold 不设时限（由后端默认值决定），不应 panic 或产生负时长
	act, _ = SweepStep(0, 0)
	if act.Dur != 0 {
		t.Fatalf("hold=0 时 Dur=%v，期望 0", act.Dur)
	}
}
