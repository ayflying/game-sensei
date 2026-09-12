package android

import (
	"testing"
	"time"

	"github.com/ayflying/game-sensei/internal/agent"
)

// 设备层的动作分发只有两种可接受结果：**真的发出触摸**，或**明确报错**。
// 绝不能静默返回 nil —— 那会把「手机上什么都没发生」伪装成「动作执行成功」。
//
// 这条规矩是拿一整晚的采集数据换来的（2026-09-12）：档案里 move.mode=joystick
// 时 MOVE 会被展开成 ActionJoystick，而 Device.Apply 当时没有这一支，
// 于是所有移动都掉进 default 被吞掉——日志照打「执行 move:up_right/2500ms」，
// 手机上却一次触摸都没有，角色一步不走，卡死自愈跟着反复空转。
func TestApply_不认识的动作必须报错(t *testing.T) {
	// 故意不接任何设备：没有 adb 时，凡是真想执行的分支都必然失败。
	// 所以「返回 nil」只可能来自静默吞掉，正是本测试要抓的。
	d := &Device{}

	if err := d.Apply(agent.Action{Kind: agent.ActionNone}); err != nil {
		t.Errorf("ActionNone 是合法动作（手机端就该什么都不做），不该报错，得到 %v", err)
	}

	cases := []agent.Action{
		// L2 摇杆：曾经缺失的那一支
		{Kind: agent.ActionJoystick, Nx: 0.21, Ny: 0.79, Nx2: 0.274, Ny2: 0.733, Dur: 2 * time.Second},
		{Kind: agent.ActionTap, Nx: 0.5, Ny: 0.5},
		{Kind: agent.ActionSwipe, Nx: 0.2, Ny: 0.8, Nx2: 0.6, Ny2: 0.8, Dur: 300 * time.Millisecond},
		{Kind: agent.ActionLongPress, Nx: 0.5, Ny: 0.5, Dur: 800 * time.Millisecond},
		{Kind: agent.ActionKey, Code: "back"},
		// L1 动作漏到设备层：必须报错，说明上游 Resolve 没做
		{Kind: agent.ActionMove, Dir: agent.DirUp, Dur: time.Second},
	}
	for _, act := range cases {
		if err := d.Apply(act); err == nil {
			t.Errorf("%v 在无设备时也必须是错误：返回 nil 意味着这个动作被静默吞掉了", act.Kind)
		}
	}
}
