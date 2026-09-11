package agent

import (
	"errors"
	"testing"
	"time"
)

func TestParseAction_Tap(t *testing.T) {
	cases := []struct {
		in string
		nx float64
		ny float64
	}{
		{"ACTION TAP x=0.79 y=0.75", 0.79, 0.75},
		{"action tap x=0.79 y=0.75", 0.79, 0.75},
		{"ACTION TAP 0.79 0.75", 0.79, 0.75},
		{"动作: TAP x=0.79 y=0.75", 0.79, 0.75},
		{"动作：点击 x=0.79，y=0.75", 0.79, 0.75},
		{"ACTION CLICK x=0.79 y=0.75  # 点一下按钮", 0.79, 0.75},
		{"- **ACTION TAP x=0.79 y=0.75**", 0.79, 0.75},
		{"点击 x=0.79 y=0.75 处的交互按钮", 0.79, 0.75},
		{"ACTION TAP nx=0.79 ny=0.75", 0.79, 0.75},
	}
	for _, c := range cases {
		got, err := ParseAction(c.in)
		if err != nil {
			t.Errorf("ParseAction(%q) 意外报错: %v", c.in, err)
			continue
		}
		if got.Kind != ActionTap {
			t.Errorf("ParseAction(%q) Kind=%v, 期望 ActionTap", c.in, got.Kind)
			continue
		}
		if !almost(got.Nx, c.nx) || !almost(got.Ny, c.ny) {
			t.Errorf("ParseAction(%q) = %.3f,%.3f, 期望 %.3f,%.3f",
				c.in, got.Nx, got.Ny, c.nx, c.ny)
		}
	}
}

func TestParseAction_Swipe(t *testing.T) {
	got, err := ParseAction("ACTION SWIPE x=0.80 y=0.50 x2=0.20 y2=0.50 dur=400")
	if err != nil {
		t.Fatalf("意外报错: %v", err)
	}
	if got.Kind != ActionSwipe {
		t.Fatalf("Kind=%v, 期望 ActionSwipe", got.Kind)
	}
	if !almost(got.Nx, 0.80) || !almost(got.Ny, 0.50) ||
		!almost(got.Nx2, 0.20) || !almost(got.Ny2, 0.50) {
		t.Errorf("坐标错: %.2f,%.2f->%.2f,%.2f", got.Nx, got.Ny, got.Nx2, got.Ny2)
	}
	if got.Dur != 400*time.Millisecond {
		t.Errorf("Dur=%v, 期望 400ms", got.Dur)
	}
}

func TestParseAction_Swipe缺少终点应拒绝(t *testing.T) {
	// 只有起点没有终点，不该被当成滑动
	if _, err := ParseAction("ACTION SWIPE x=0.80 y=0.50"); !errors.Is(err, ErrNoAction) {
		t.Errorf("期望 ErrNoAction，实际 %v", err)
	}
}

func TestParseAction_LongPress(t *testing.T) {
	got, err := ParseAction("ACTION HOLD x=0.50 y=0.50 dur=800")
	if err != nil {
		t.Fatalf("意外报错: %v", err)
	}
	if got.Kind != ActionLongPress {
		t.Fatalf("Kind=%v, 期望 ActionLongPress", got.Kind)
	}
	if got.Dur != 800*time.Millisecond {
		t.Errorf("Dur=%v, 期望 800ms", got.Dur)
	}

	got2, err := ParseAction("长按 x=0.50 y=0.50")
	if err != nil || got2.Kind != ActionLongPress {
		t.Errorf("中文长按解析失败: %v %v", got2.Kind, err)
	}
	if got2.Dur != 600*time.Millisecond {
		t.Errorf("默认长按时长应为 600ms，实际 %v", got2.Dur)
	}
}

func TestParseAction_Joystick(t *testing.T) {
	// 推荐写法 cx/cy + tx/ty
	got, err := ParseAction("ACTION JOYSTICK cx=0.21 cy=0.69 tx=0.81 ty=0.69 dur=1200")
	if err != nil {
		t.Fatalf("意外报错: %v", err)
	}
	if got.Kind != ActionJoystick {
		t.Fatalf("Kind=%v, 期望 ActionJoystick", got.Kind)
	}
	if !almost(got.Nx, 0.21) || !almost(got.Ny, 0.69) ||
		!almost(got.Nx2, 0.81) || !almost(got.Ny2, 0.69) {
		t.Errorf("坐标错: %.2f,%.2f->%.2f,%.2f", got.Nx, got.Ny, got.Nx2, got.Ny2)
	}
	if got.Dur != 1200*time.Millisecond {
		t.Errorf("Dur=%v, 期望 1200ms", got.Dur)
	}

	// 兼容写法 x/y + x2/y2
	got2, err := ParseAction("ACTION JOYSTICK x=0.21 y=0.69 x2=0.81 y2=0.69 dur=1000")
	if err != nil {
		t.Fatalf("兼容写法意外报错: %v", err)
	}
	if !almost(got2.Nx, 0.21) || !almost(got2.Nx2, 0.81) {
		t.Errorf("兼容写法坐标错: %.2f->%.2f", got2.Nx, got2.Nx2)
	}
}

func TestParseAction_Key(t *testing.T) {
	got, err := ParseAction("ACTION KEY code=back")
	if err != nil {
		t.Fatalf("意外报错: %v", err)
	}
	if got.Kind != ActionKey || got.Code != "back" {
		t.Errorf("得到 %v/%q, 期望 ActionKey/back", got.Kind, got.Code)
	}

	// 数字键码
	got2, err := ParseAction("ACTION KEY code=4")
	if err != nil || got2.Kind != ActionKey || got2.Code != "4" {
		t.Errorf("数字键码解析失败: %v/%q %v", got2.Kind, got2.Code, err)
	}

	// 没有 code 应拒绝
	if _, err := ParseAction("ACTION KEY"); !errors.Is(err, ErrNoAction) {
		t.Errorf("无 code 时应 ErrNoAction，实际 %v", err)
	}
}

func TestParseAction_Wait(t *testing.T) {
	for _, in := range []string{"ACTION WAIT", "ACTION NONE", "等待"} {
		got, err := ParseAction(in)
		if err != nil {
			t.Errorf("ParseAction(%q) 意外报错: %v", in, err)
			continue
		}
		if got.Kind != ActionNone {
			t.Errorf("ParseAction(%q) Kind=%v, 期望 ActionNone", in, got.Kind)
		}
	}
}

// 这是本解析器最关键的一条：没有坐标的叙述性中文不能被当成动作。
// 实测里 qwen3.5 经常在 thinking 里写「建议：点击右下角的交互按钮」，
// 若把它当 tap(0,0)，机器人就会莫名其妙去点左上角。
func TestParseAction_无坐标叙述应拒绝(t *testing.T) {
	bad := []string{
		"建议: 点击右下角的交互按钮",
		"建议：点击右下角技能轮盘上的交互按钮",
		"下一步应该走向左边的黄色目标",
		"ACTION TAP",
		"ACTION TAP x=0.5",         // 只有 x 没有 y
		"ACTION SWIPE x=0.8 y=0.5", // 只有起点没有终点
		"摇杆: x=0.21 y=0.69",        // 只是描述摇杆位置，不是一个动作
		"按钮: 星星(x=0.80,y=0.81)",    // 同理，只是描述按钮位置
		"这行完全无关",
		"",
	}
	for _, in := range bad {
		got, err := ParseAction(in)
		if !errors.Is(err, ErrNoAction) {
			t.Errorf("ParseAction(%q) 应返回 ErrNoAction，实际 err=%v act=%v", in, err, got)
		}
	}
}

func TestParseAction_坐标越界应夹紧(t *testing.T) {
	got, err := ParseAction("ACTION TAP x=1.5 y=-0.2")
	if err != nil {
		t.Fatalf("意外报错: %v", err)
	}
	if got.Nx != 1.0 || got.Ny != 0.0 {
		t.Errorf("夹紧失败: %.2f,%.2f，期望 1.0,0.0", got.Nx, got.Ny)
	}
}

func TestParseAction_多行取第一个动作(t *testing.T) {
	text := `场景: 开放世界探索
摇杆: x=0.21 y=0.69
建议: 先点交互按钮
ACTION TAP x=0.79 y=0.75
ACTION WAIT`
	got, err := ParseAction(text)
	if err != nil {
		t.Fatalf("意外报错: %v", err)
	}
	if got.Kind != ActionTap {
		t.Fatalf("Kind=%v, 期望 ActionTap（应取第一个动作行）", got.Kind)
	}
	if !almost(got.Nx, 0.79) {
		t.Errorf("Nx=%.2f, 期望 0.79", got.Nx)
	}
}

func TestParseAllActions(t *testing.T) {
	text := `ACTION TAP x=0.79 y=0.75
一些说明文字
ACTION JOYSTICK cx=0.21 cy=0.69 tx=0.81 ty=0.69 dur=1200
ACTION WAIT`
	got := ParseAllActions(text)
	if len(got) != 3 {
		t.Fatalf("解析出 %d 个动作，期望 3 个: %v", len(got), got)
	}
	if got[0].Kind != ActionTap || got[1].Kind != ActionJoystick || got[2].Kind != ActionNone {
		t.Errorf("动作序列错: %v", got)
	}
}

func TestActionString(t *testing.T) {
	cases := []struct {
		act  Action
		want string
	}{
		{Action{Kind: ActionTap, Nx: 0.5, Ny: 0.8}, "tap:0.500,0.800"},
		{Action{Kind: ActionKey, Code: "back"}, "key:back"},
		{Action{Kind: ActionNone}, "none"},
		{Action{Kind: ActionJoystick, Nx: 0.21, Ny: 0.69, Nx2: 0.81, Ny2: 0.69,
			Dur: 1200 * time.Millisecond}, "joy:0.210,0.690->0.810,0.690/1200ms"},
	}
	for _, c := range cases {
		if got := c.act.String(); got != c.want {
			t.Errorf("String() = %q, 期望 %q", got, c.want)
		}
	}
}

func TestExplainAction(t *testing.T) {
	if s := ExplainAction(Action{Kind: ActionTap, Nx: 0.5, Ny: 0.8}); s == "" {
		t.Error("ExplainAction 返回空串")
	}
	if s := ExplainAction(Action{Kind: ActionNone}); s == "" {
		t.Error("ExplainAction(ActionNone) 返回空串")
	}
}

func TestActionProtocol(t *testing.T) {
	p := ActionProtocol()
	for _, want := range []string{"ACTION TAP", "ACTION SWIPE", "ACTION HOLD",
		"ACTION JOYSTICK", "ACTION KEY", "ACTION WAIT"} {
		if !contains(p, want) {
			t.Errorf("ActionProtocol() 缺少 %q", want)
		}
	}
}

func almost(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-6
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
