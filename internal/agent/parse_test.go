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
	// 通用部分：不带档案时也要能给出基础动作说明
	// （AllowFreePointer 要显式开：协议的零值语义是「战斗态式收窄」，
	//   而通用兜底协议应是全开的——两者故意相反，见 ProtocolOptions 注释。）
	p := ActionProtocol(ProtocolOptions{AllowFreePointer: true})
	for _, want := range []string{"ACTION TAP", "ACTION SWIPE", "ACTION HOLD",
		"ACTION KEY", "ACTION WAIT"} {
		if !contains(p, want) {
			t.Errorf("ActionProtocol() 缺少 %q", want)
		}
	}
	// 没配移动/按钮时不该列出 MOVE / PRESS：
	// 让模型看见的动作空间越小越准，列了它就会去用做不了的动作。
	if contains(p, "ACTION MOVE") {
		t.Error("未配置移动时不该列出 MOVE")
	}
	if contains(p, "ACTION PRESS") {
		t.Error("未配置按钮时不该列出 PRESS")
	}
	// 协议里绝不能出现摇杆四坐标：实测模型会在 cx/cy/tx/ty 的语义上
	// 纠结到打满思考预算（74s）仍给不出动作。
	if contains(p, "JOYSTICK") {
		t.Error("协议里不该出现 JOYSTICK 写法（实测会让模型陷入参数语义纠结）")
	}
}

// AllowFreePointer=false（战斗态）必须收掉自由坐标动作：
// pet_run10 实测老师在战斗里连点 7 步自由坐标全部空耗（Δ≈0.8），
// 战斗界面只有按钮和技能宏是有意义的交互点。
func TestActionProtocol_战斗态收掉自由坐标(t *testing.T) {
	p := ActionProtocol(ProtocolOptions{
		Buttons:          []string{"battle_flee(逃跑)", "cast_hetu(技能)"},
		AllowFreePointer: false,
	})
	for _, bad := range []string{"ACTION TAP", "ACTION SWIPE", "ACTION HOLD"} {
		if contains(p, bad) {
			t.Errorf("战斗态协议不该列出 %s", bad)
		}
	}
	for _, want := range []string{"ACTION PRESS", "ACTION KEY", "ACTION WAIT"} {
		if !contains(p, want) {
			t.Errorf("战斗态协议缺少 %q", want)
		}
	}
}

func TestActionProtocol_带档案(t *testing.T) {
	p := ActionProtocol(ProtocolOptions{
		Hints:    []string{"左下角是虚拟摇杆"},
		Buttons:  []string{"jump(跳跃)", "attack(攻击)"},
		HasMove:  true,
		MoveNote: "虚拟摇杆，位置固定在屏幕左下",
	})
	for _, want := range []string{"ACTION MOVE", "ACTION PRESS", "jump(跳跃)", "虚拟摇杆"} {
		if !contains(p, want) {
			t.Errorf("带档案的协议缺少 %q", want)
		}
	}
}

// 提示词里的示例必须写成占位符。踩过的坑：早期示例写了具体坐标
// （ACTION JOYSTICK cx=0.21 cy=0.69 ...），模型逐字节照抄这一行当答案，
// 8 步实况动作完全一致——看着像「模型不会决策」，实际是「把示例当答案抄了」。
func TestActionProtocol_不含可照抄的具体数字(t *testing.T) {
	p := ActionProtocol(ProtocolOptions{HasMove: true, Buttons: []string{"jump"}})
	for _, bad := range []string{"0.21", "0.69", "0.81", "1200"} {
		if contains(p, bad) {
			t.Errorf("协议里出现了可被照抄的具体数字 %q", bad)
		}
	}
	if !contains(p, "<") {
		t.Error("协议应使用尖括号占位符")
	}
}

func TestParseAction_Move方向(t *testing.T) {
	cases := []struct {
		in   string
		dir  Dir
		dur  time.Duration
		desc string
	}{
		{"ACTION MOVE dir=up dur=800", DirUp, 800 * time.Millisecond, "键值对写法"},
		{"ACTION MOVE dir=前", DirUp, DefaultMoveMs * time.Millisecond, "中文方向 + 默认时长"},
		{"action move dir=UP_LEFT", DirUpLeft, DefaultMoveMs * time.Millisecond, "大小写不敏感"},
		{"ACTION MOVE dir=up-left dur=500", DirUpLeft, 500 * time.Millisecond, "连字符写法"},
		{"ACTION MOVE forward", DirUp, DefaultMoveMs * time.Millisecond, "省略 dir= 且不带时长"},
		{"移动 方向=右", DirRight, DefaultMoveMs * time.Millisecond, "中文动词"},
	}
	for _, c := range cases {
		got, err := ParseAction(c.in)
		if err != nil {
			t.Errorf("%s: ParseAction(%q) 意外报错: %v", c.desc, c.in, err)
			continue
		}
		if got.Kind != ActionMove {
			t.Errorf("%s: Kind=%v, 期望 ActionMove", c.desc, got.Kind)
			continue
		}
		if got.Dir != c.dir {
			t.Errorf("%s: Dir=%q, 期望 %q", c.desc, got.Dir, c.dir)
		}
		if got.Dur != c.dur {
			t.Errorf("%s: Dur=%v, 期望 %v", c.desc, got.Dur, c.dur)
		}
	}
}

// 无方向的 MOVE 不能瞎猜成某个方向——那会让角色自己走起来。
func TestParseAction_Move缺方向应拒绝(t *testing.T) {
	for _, in := range []string{"ACTION MOVE", "ACTION MOVE dur=800", "移动"} {
		if _, err := ParseAction(in); !errors.Is(err, ErrNoAction) {
			t.Errorf("ParseAction(%q) 应返回 ErrNoAction", in)
		}
	}
}

func TestParseAction_Move非法方向应拒绝(t *testing.T) {
	for _, in := range []string{"ACTION MOVE dir=towards", "ACTION MOVE dir=斜着"} {
		if _, err := ParseAction(in); !errors.Is(err, ErrNoAction) {
			t.Errorf("ParseAction(%q) 非法方向应拒绝", in)
		}
	}
}

// L2 的鼠标相对移动是保留能力：MOVE dx/dy 仍认，但降级成 ActionMouseMove，
// 不会和 L1 的方向移动混淆。
func TestParseAction_Move回退到鼠标位移(t *testing.T) {
	got, err := ParseAction("ACTION MOVE dx=10 dy=-5")
	if err != nil {
		t.Fatalf("意外报错: %v", err)
	}
	if got.Kind != ActionMouseMove {
		t.Fatalf("Kind=%v, 期望 ActionMouseMove", got.Kind)
	}
	if got.Dx != 10 || got.Dy != -5 {
		t.Errorf("位移 = %d,%d, 期望 10,-5", got.Dx, got.Dy)
	}
}

func TestParseAction_Press按钮(t *testing.T) {
	cases := []struct {
		in   string
		name string
	}{
		{"ACTION PRESS name=jump", "jump"},
		{"ACTION PRESS jump", "jump"},
		{"action press btn=Attack", "attack"},
		{"ACTION BUTTON name=星形", "星形"},
		{"按下 按钮=jump", "jump"},
	}
	for _, c := range cases {
		got, err := ParseAction(c.in)
		if err != nil {
			t.Errorf("ParseAction(%q) 意外报错: %v", c.in, err)
			continue
		}
		if got.Kind != ActionPress {
			t.Errorf("ParseAction(%q) Kind=%v, 期望 ActionPress", c.in, got.Kind)
			continue
		}
		if got.Name != c.name {
			t.Errorf("ParseAction(%q) Name=%q, 期望 %q", c.in, got.Name, c.name)
		}
	}
}

// 没有按钮名的 PRESS 不能当成「按了个空按钮」，纯数字也不是按钮名。
func TestParseAction_Press缺名字应拒绝(t *testing.T) {
	for _, in := range []string{"ACTION PRESS", "ACTION PRESS name=", "ACTION PRESS 123"} {
		if _, err := ParseAction(in); !errors.Is(err, ErrNoAction) {
			t.Errorf("ParseAction(%q) 应返回 ErrNoAction", in)
		}
	}
}

func TestParseAction_无坐标叙述仍应拒绝(t *testing.T) {
	// 加了 MOVE/PRESS 之后，原有的「叙述行不能被当动作」的保证必须仍然成立
	bad := []string{
		"ACTION TAP x=0.5",
		"摇杆: x=0.21 y=0.69",
		"建议: 点击右下角的交互按钮",
		"方向: up",
	}
	for _, in := range bad {
		if _, err := ParseAction(in); !errors.Is(err, ErrNoAction) {
			t.Errorf("ParseAction(%q) 应返回 ErrNoAction", in)
		}
	}
}

func TestActionString_新动作(t *testing.T) {
	cases := []struct {
		act  Action
		want string
	}{
		{Action{Kind: ActionMove, Dir: DirUp}, "move:up"},
		{Action{Kind: ActionMove, Dir: DirUpLeft, Dur: 800 * time.Millisecond}, "move:up_left/800ms"},
		{Action{Kind: ActionPress, Name: "jump"}, "press:jump"},
		{Action{Kind: ActionMouseMove, Dx: 10, Dy: -5}, "mouse:+10,-5"},
		{Action{Kind: ActionKey, Code: "w", Dur: 500 * time.Millisecond}, "key:w/500ms"},
	}
	for _, c := range cases {
		if got := c.act.String(); got != c.want {
			t.Errorf("String() = %q, 期望 %q", got, c.want)
		}
	}
}

func TestExplainAction_新动作(t *testing.T) {
	if s := ExplainAction(Action{Kind: ActionMove, Dir: DirUpLeft, Dur: time.Second}); s == "" {
		t.Error("MOVE 的说明为空")
	}
	if s := ExplainAction(Action{Kind: ActionPress, Name: "jump"}); !contains(s, "jump") {
		t.Errorf("PRESS 的说明应含按钮名，实际 %q", s)
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
