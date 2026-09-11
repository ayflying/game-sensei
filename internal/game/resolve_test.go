package game

import (
	"reflect"
	"testing"
	"time"

	"github.com/ayflying/game-sensei/internal/agent"
)

func TestResolve_摇杆展开(t *testing.T) {
	p, err := Load("nrc")
	if err != nil {
		t.Fatal(err)
	}
	act, err := p.Resolve(agent.Action{
		Kind: agent.ActionMove, Dir: agent.DirUp, Dur: 800 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("解析 MOVE up 失败: %v", err)
	}
	if act.Kind != agent.ActionJoystick {
		t.Fatalf("Kind = %v, 期望 ActionJoystick（joystick 档案下 MOVE 应展开成推杆）", act.Kind)
	}
	// 起点必须是摇杆中心，终点朝上推一个 radius
	if !approx(act.Nx, 0.21) || !approx(act.Ny, 0.69) {
		t.Errorf("起点 = %.3f,%.3f, 期望摇杆中心 0.21,0.69", act.Nx, act.Ny)
	}
	if !approx(act.Nx2, 0.21) {
		t.Errorf("上推时 x 不该变，实际 %.3f", act.Nx2)
	}
	if act.Ny2 >= act.Ny {
		t.Errorf("上推时 y 应减小（屏幕 y 向下），实际 %.3f -> %.3f", act.Ny, act.Ny2)
	}
	if act.Dur != 800*time.Millisecond {
		t.Errorf("Dur = %v, 期望 800ms", act.Dur)
	}
}

func TestResolve_斜向推杆幅度一致(t *testing.T) {
	p, err := Load("nrc")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []agent.Dir{agent.DirUp, agent.DirUpRight, agent.DirRight} {
		act, err := p.Resolve(agent.Action{Kind: agent.ActionMove, Dir: d})
		if err != nil {
			t.Fatalf("%s 解析失败: %v", d, err)
		}
		// 终点相对中心的偏移量，八个方向都该落在同一椭圆半径上：
		// 正向推满 rx=0.09、斜向按 rx/√2=0.0636
		dx := (act.Nx2 - act.Nx) / p.Move.Radius[0]
		dy := (act.Ny2 - act.Ny) / p.Move.Radius[1]
		if l := dx*dx + dy*dy; l > 1.0001 {
			t.Errorf("%s 推杆幅度超界: %.4f（>1 会被游戏判定为滑出摇杆热区）", d, l)
		}
	}
}

func TestResolve_推杆不越屏(t *testing.T) {
	// 摇杆贴着屏幕边缘的档案：推杆终点必须夹紧在 0~1，不能算到屏幕外
	p := &Profile{
		Name: "贴边摇杆",
		Move: MoveProfile{Mode: MoveJoystick, Center: [2]float64{0.02, 0.98}, Radius: [2]float64{0.09, 0.08}},
	}
	if err := p.normalize(); err != nil {
		t.Fatal(err)
	}
	act, err := p.Resolve(agent.Action{Kind: agent.ActionMove, Dir: agent.DirDownRight})
	if err != nil {
		t.Fatal(err)
	}
	if act.Nx2 < 0 || act.Nx2 > 1 || act.Ny2 < 0 || act.Ny2 > 1 {
		t.Errorf("终点越出屏幕: %.3f,%.3f", act.Nx2, act.Ny2)
	}
}

func TestResolve_MOVE默认时长(t *testing.T) {
	p, err := Load("nrc")
	if err != nil {
		t.Fatal(err)
	}
	// 模型常常不给 dur；不给也要能动，且时长要够产生可观测位移
	act, err := p.Resolve(agent.Action{Kind: agent.ActionMove, Dir: agent.DirLeft})
	if err != nil {
		t.Fatal(err)
	}
	if act.Dur <= 0 {
		t.Errorf("Dur = %v，应回退到默认时长", act.Dur)
	}
}

func TestResolve_按键模式(t *testing.T) {
	p, err := Load("pc_generic")
	if err != nil {
		t.Fatal(err)
	}
	act, err := p.Resolve(agent.Action{
		Kind: agent.ActionMove, Dir: agent.DirUp, Dur: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("PC 档案解析 MOVE 失败: %v", err)
	}
	if act.Kind != agent.ActionKey || act.Code != "w" {
		t.Fatalf("得到 %v/%q, 期望 ActionKey/w", act.Kind, act.Code)
	}
	// Dur>0 表示「按住」：移动是持续行为，点一下等于原地抖
	if act.Dur != 500*time.Millisecond {
		t.Errorf("Dur = %v, 期望 500ms（必须保留，否则后端只会点一下）", act.Dur)
	}
}

// 斜向在 keys 模式下默认组合两个正向键（左上 = w+a）：
// PC 键盘游戏「斜着走」就是同时按两个键，ActionKey 的 Codes 表达多键。
// 早期 Codes 不存在时这里故意报错；多键支持落地后改为验证组合正确性。
func TestResolve_按键模式斜向组合双键(t *testing.T) {
	p, err := Load("pc_generic")
	if err != nil {
		t.Fatal(err)
	}
	act, err := p.Resolve(agent.Action{Kind: agent.ActionMove, Dir: agent.DirUpLeft, Dur: 300 * time.Millisecond})
	if err != nil {
		t.Fatalf("keys 模式斜向应默认组合双键: %v", err)
	}
	if act.Kind != agent.ActionKey || len(act.Codes) != 2 || act.Codes[0] != "w" || act.Codes[1] != "a" {
		t.Fatalf("得到 %v codes=%v, 期望 ActionKey codes=[w a]", act.Kind, act.Codes)
	}
	if act.Dur != 300*time.Millisecond {
		t.Errorf("Dur = %v, 期望 300ms", act.Dur)
	}
	// 档案显式配置单键覆盖默认组合（例如游戏只认一个键）
	p.Move.Keys = map[string]string{"up_left": "q"}
	act, err = p.Resolve(agent.Action{Kind: agent.ActionMove, Dir: agent.DirUpLeft})
	if err != nil {
		t.Fatalf("显式配置斜向后应能解析: %v", err)
	}
	if act.Kind != agent.ActionKey || act.Code != "q" {
		t.Fatalf("显式配置应走单键路径, 得到 %v/%q codes=%v", act.Kind, act.Code, act.Codes)
	}
}

// 键盘按钮（Key 非空）：PRESS 落成按键盘键，而不是点击坐标。
func TestResolve_键盘按钮落成按键(t *testing.T) {
	p := &Profile{Name: "PC游戏"}
	p.Move.Mode = MoveKeys
	p.Buttons = []Button{
		{Name: "interact", Key: "f", Note: "交互"},
		{Name: "jump", Key: "space"},
		{Name: "mobile_btn", Pos: [2]float64{0.8, 0.8}}, // 对照组：坐标按钮
	}
	if err := p.normalize(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		want agent.Action
	}{
		{"interact", agent.Action{Kind: agent.ActionKey, Code: "f"}},
		{"jump", agent.Action{Kind: agent.ActionKey, Code: "space"}},
		{"mobile_btn", agent.Action{Kind: agent.ActionTap, Nx: 0.8, Ny: 0.8}},
	} {
		got, err := p.Resolve(agent.Action{Kind: agent.ActionPress, Name: c.name})
		if err != nil {
			t.Fatalf("PRESS %s: %v", c.name, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("PRESS %s = %+v, 期望 %+v", c.name, got, c.want)
		}
	}
	// 既没 key 也没 pos 的按钮要在加载时报错，不许静默
	bad := &Profile{Name: "坏档案"}
	bad.Move.Mode = MoveKeys
	bad.Buttons = []Button{{Name: "ghost"}}
	if err := bad.normalize(); err == nil {
		t.Error("无 key 无 pos 的按钮应校验失败")
	}
}

func TestResolve_无移动档案应报错(t *testing.T) {
	p := &Profile{Name: "卡牌"}
	p.Move.Mode = MoveNone
	if _, err := p.Resolve(agent.Action{Kind: agent.ActionMove, Dir: agent.DirUp}); err == nil {
		t.Error("move.mode=none 下执行 MOVE 应报错，而不是静默不动")
	}
}

func TestResolve_非法方向应报错(t *testing.T) {
	p, err := Load("nrc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Resolve(agent.Action{Kind: agent.ActionMove, Dir: "northeast-ish"}); err == nil {
		t.Error("非法方向应报错")
	}
}

func TestResolve_PRESS展开成点击(t *testing.T) {
	p, err := Load("nrc")
	if err != nil {
		t.Fatal(err)
	}
	act, err := p.Resolve(agent.Action{Kind: agent.ActionPress, Name: "star"})
	if err != nil {
		t.Fatalf("解析 PRESS star 失败: %v", err)
	}
	if act.Kind != agent.ActionTap {
		t.Fatalf("Kind = %v, 期望 ActionTap", act.Kind)
	}
	btn, _ := p.Button("star")
	if !approx(act.Nx, btn.Pos[0]) || !approx(act.Ny, btn.Pos[1]) {
		t.Errorf("点击坐标 = %.3f,%.3f, 期望按钮位置 %.3f,%.3f", act.Nx, act.Ny, btn.Pos[0], btn.Pos[1])
	}
}

func TestResolve_PRESS别名可用(t *testing.T) {
	p, err := Load("nrc")
	if err != nil {
		t.Fatal(err)
	}
	act, err := p.Resolve(agent.Action{Kind: agent.ActionPress, Name: "星形"})
	if err != nil {
		t.Fatalf("中文别名应可用: %v", err)
	}
	if act.Kind != agent.ActionTap {
		t.Errorf("Kind = %v", act.Kind)
	}
}

func TestResolve_未定义按钮的报错要列出可用项(t *testing.T) {
	p, err := Load("nrc")
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Resolve(agent.Action{Kind: agent.ActionPress, Name: "nonexistent"})
	if err == nil {
		t.Fatal("未定义的按钮应报错")
	}
	// 报错必须告诉模型「有哪些能用」，否则它下次还猜错
	if !contains(err.Error(), "star") {
		t.Errorf("报错应列出可用按钮，实际: %v", err)
	}
}

// Resolve 必须是幂等的：后端可以无脑先 Resolve 再执行，
// 不必分辨动作来自模型（L1）还是来自别的环节（已是 L2）。
func TestResolve_幂等(t *testing.T) {
	p, err := Load("nrc")
	if err != nil {
		t.Fatal(err)
	}
	inputs := []agent.Action{
		{Kind: agent.ActionTap, Nx: 0.5, Ny: 0.5},
		{Kind: agent.ActionKey, Code: "back"},
		{Kind: agent.ActionJoystick, Nx: 0.21, Ny: 0.69, Nx2: 0.30, Ny2: 0.69, Dur: time.Second},
		{Kind: agent.ActionSwipe, Nx: 0.8, Ny: 0.5, Nx2: 0.2, Ny2: 0.5, Dur: 400 * time.Millisecond},
		{Kind: agent.ActionNone},
	}
	for _, in := range inputs {
		out, err := p.Resolve(in)
		if err != nil {
			t.Fatalf("已是 L2 的动作不该报错: %v", err)
		}
		if !reflect.DeepEqual(out, in) {
			t.Errorf("已是 L2 的动作被改动了: %+v -> %+v", in, out)
		}
	}
	// MOVE 解析后再解析应保持不变
	once, err := p.Resolve(agent.Action{Kind: agent.ActionMove, Dir: agent.DirUp})
	if err != nil {
		t.Fatal(err)
	}
	twice, err := p.Resolve(once)
	if err != nil {
		t.Fatalf("对已展开的动作再次 Resolve 不该报错: %v", err)
	}
	if !reflect.DeepEqual(twice, once) {
		t.Errorf("Resolve 不幂等: %+v -> %+v", once, twice)
	}
}

func TestProtocolOptions_按档案裁剪动作空间(t *testing.T) {
	nrc, err := Load("nrc")
	if err != nil {
		t.Fatal(err)
	}
	o := nrc.ProtocolOptions()
	if !o.HasMove {
		t.Error("nrc 有摇杆，应列出 MOVE")
	}
	if len(o.Buttons) == 0 {
		t.Error("nrc 配了按钮，应列出 PRESS")
	}
	if len(o.Hints) == 0 {
		t.Error("应带上界面先验")
	}
	if o.MoveNote == "" {
		t.Error("应说明移动方式")
	}

	// 没配按钮的档案不该列 PRESS：没有可信坐标，列了只会让模型瞎按
	card := &Profile{Name: "卡牌"}
	card.Move.Mode = MoveNone
	co := card.ProtocolOptions()
	if co.HasMove {
		t.Error("move.mode=none 不该列出 MOVE")
	}
	if len(co.Buttons) != 0 {
		t.Error("无按钮档案不该列出 PRESS")
	}
}

func TestProtocolOptions_空档案安全(t *testing.T) {
	var p *Profile
	o := p.ProtocolOptions()
	if o.HasMove || len(o.Buttons) != 0 {
		t.Error("nil Profile 应返回零值选项")
	}
}

// Test端到端_从模型输出到设备操作 串起完整的翻译链路：
//
//	老师的原始文本 → agent.ParseAction（L1）→ Profile.Resolve（L2）
//
// 这是分层设计最该被钉住的契约：换游戏只换档案，这条链路一个字都不用改。
func Test端到端_从模型输出到设备操作(t *testing.T) {
	nrc, err := Load("nrc")
	if err != nil {
		t.Fatal(err)
	}
	pc, err := Load("pc_generic")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("手游：朝前走 → 摇杆上推", func(t *testing.T) {
		act, err := agent.ParseAction("ACTION MOVE dir=前 dur=800")
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		if act.Kind != agent.ActionMove || act.Dir != agent.DirUp {
			t.Fatalf("解析结果异常: %+v", act)
		}
		out, err := nrc.Resolve(act)
		if err != nil {
			t.Fatalf("展开失败: %v", err)
		}
		if out.Kind != agent.ActionJoystick {
			t.Fatalf("手游上应展开为摇杆，得到 %v", out.Kind)
		}
		if out.Ny2 >= out.Ny {
			t.Errorf("上推应使 y 减小: %.3f -> %.3f", out.Ny, out.Ny2)
		}
		if out.Dur != 800*time.Millisecond {
			t.Errorf("时长应透传，得到 %v", out.Dur)
		}
	})

	t.Run("同一句话在 PC 上 → 按住 W", func(t *testing.T) {
		act, err := agent.ParseAction("ACTION MOVE dir=前 dur=800")
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		out, err := pc.Resolve(act)
		if err != nil {
			t.Fatalf("展开失败: %v", err)
		}
		if out.Kind != agent.ActionKey || out.Code != "w" {
			t.Fatalf("PC 上应展开为 W 键，得到 %v/%q", out.Kind, out.Code)
		}
		if out.Dur != 800*time.Millisecond {
			t.Errorf("时长应透传（否则只是点一下），得到 %v", out.Dur)
		}
	})

	t.Run("按命名按钮 → 点该按钮的坐标", func(t *testing.T) {
		act, err := agent.ParseAction("ACTION PRESS name=星形")
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		out, err := nrc.Resolve(act)
		if err != nil {
			t.Fatalf("展开失败: %v", err)
		}
		if out.Kind != agent.ActionTap {
			t.Fatalf("应展开为点击，得到 %v", out.Kind)
		}
		btn, _ := nrc.Button("star")
		if !approx(out.Nx, btn.Pos[0]) || !approx(out.Ny, btn.Pos[1]) {
			t.Errorf("应点到按钮位置 %.2f,%.2f，实际 %.2f,%.2f",
				btn.Pos[0], btn.Pos[1], out.Nx, out.Ny)
		}
	})

	t.Run("等待不需要档案", func(t *testing.T) {
		act, err := agent.ParseAction("ACTION WAIT")
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		out, err := nrc.Resolve(act)
		if err != nil {
			t.Fatalf("WAIT 不该报错: %v", err)
		}
		if out.Kind != agent.ActionNone {
			t.Errorf("得到 %v", out.Kind)
		}
	})
}
