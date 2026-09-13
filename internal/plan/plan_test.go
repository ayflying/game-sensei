package plan

import (
	"image"
	"testing"
	"time"

	"github.com/ayflying/game-sensei/internal/agent"
)

// fakeExec 是测试用的假后端：按脚本逐帧吐出画面，并记录收到的动作。
//
// frames 每被 Grab 一次就前进一帧（到末尾后停在最后一帧），
// 这样「画面变化」的时序完全由测试控制，不依赖真实设备。
type fakeExec struct {
	frames []*image.Gray
	idx    int
	acts   []agent.Action
	grabs  int
	// grabErr 非空时 Grab 一律返回它（模拟抓帧失败）。
	grabErr error
}

func (f *fakeExec) Grab(downWidth int) (*image.Gray, error) {
	f.grabs++
	if f.grabErr != nil {
		return nil, f.grabErr
	}
	if len(f.frames) == 0 {
		return nil, nil
	}
	i := f.idx
	if i >= len(f.frames) {
		i = len(f.frames) - 1
	}
	f.idx++
	return f.frames[i], nil
}

func (f *fakeExec) Apply(a agent.Action) error {
	f.acts = append(f.acts, a)
	return nil
}

// frame 生成一张纯色灰度帧（value 越小越暗）。
func frame(value uint8) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, 32, 32))
	for i := range img.Pix {
		img.Pix[i] = value
	}
	return img
}

// noSleep 让等待瞬间返回（测试里不真的睡）。
func noSleep(*testing.T) Option {
	return WithSleep(func(time.Duration) {})
}

func TestValidateRejectsBadPlans(t *testing.T) {
	cases := []struct {
		name string
		p    *Plan
	}{
		{"空计划", &Plan{}},
		{"缺 action", &Plan{Steps: []Step{{Name: "x"}}}},
		{"动作不可解析", &Plan{Steps: []Step{{Action: "ACTION 乱七八糟"}}}},
		{"until 类型非法", &Plan{Steps: []Step{
			{Action: "TAP x=0.5 y=0.5", Until: &Until{Type: "explode"}},
		}}},
	}
	for _, c := range cases {
		if err := c.p.Validate(); err == nil {
			t.Errorf("%s：本应报错却通过了", c.name)
		}
	}
	if err := (&Plan{Name: "ok", Steps: []Step{
		{Action: "ACTION TAP x=0.5 y=0.9", Until: &Until{Type: TypeChange}},
	}}).Validate(); err != nil {
		t.Errorf("合法计划被判非法: %v", err)
	}
}

// TestRunWithoutUntil 验证「无条件步骤」按顺序发动作，Repeat 生效。
func TestRunWithoutUntil(t *testing.T) {
	fe := &fakeExec{}
	r := NewRunner(fe, noSleep(t))
	p := &Plan{Steps: []Step{
		{Action: "ACTION TAP x=0.1 y=0.1"},
		{Action: "ACTION PRESS name=confirm", Repeat: 3},
		{Action: "ACTION KEY code=back"},
	}}
	stats, err := r.Run(p, nil)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if stats.Steps != 3 {
		t.Errorf("步数 = %d，期望 3", stats.Steps)
	}
	if stats.Actions != 5 { // 1 + 3 + 1
		t.Errorf("动作数 = %d，期望 5", stats.Actions)
	}
	if len(fe.acts) != 5 {
		t.Fatalf("后端收到 %d 个动作，期望 5", len(fe.acts))
	}
	if fe.acts[1].Kind != agent.ActionPress || fe.acts[1].Name != "confirm" {
		t.Errorf("第二个动作 = %+v，期望 press confirm", fe.acts[1])
	}
	if fe.acts[4].Kind != agent.ActionKey || fe.acts[4].Code != "back" {
		t.Errorf("最后一个动作 = %+v，期望 key back", fe.acts[4])
	}
}

// TestUntilChangeAdvances 验证 change 条件在画面变化后立刻推进——
// 这是「不等死时长、靠反馈提速」的核心行为。
func TestUntilChangeAdvances(t *testing.T) {
	// 序列：基准帧(暗) -> 轮询仍暗 -> 变化(亮)。
	fe := &fakeExec{frames: []*image.Gray{
		frame(10), frame(10), frame(10), frame(200),
	}}
	r := NewRunner(fe, noSleep(t))
	p := &Plan{Steps: []Step{
		{Name: "点继续", Action: "ACTION TAP x=0.5 y=0.9",
			Until: &Until{Type: TypeChange, Diff: 4}, TimeoutMs: 5000},
	}}
	stats, err := r.Run(p, nil)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if stats.Timeouts != 0 {
		t.Errorf("不该超时，timeouts = %d", stats.Timeouts)
	}
	if fe.grabs < 3 {
		t.Errorf("采样次数 = %d，期望至少 3（基准+2次轮询）", fe.grabs)
	}
}

// TestUntilChangeTimeoutIsLenient 验证默认宽松：条件超时不中断整个计划，
// 因为手游随时会插播弹窗，硬失败会让计划一步都走不完。
func TestUntilChangeTimeoutIsLenient(t *testing.T) {
	// 画面恒定不变 -> change 永远不满足 -> 超时。
	fe := &fakeExec{frames: []*image.Gray{frame(50), frame(50), frame(50)}}
	var logs []string
	r := NewRunner(fe, noSleep(t), WithLogf(func(f string, a ...any) {
		logs = append(logs, f)
	}))
	p := &Plan{Steps: []Step{
		{Name: "第一步", Action: "ACTION TAP x=0.5 y=0.5",
			Until: &Until{Type: TypeChange}, TimeoutMs: 1},
		{Name: "第二步", Action: "ACTION TAP x=0.5 y=0.6", GapMs: 0},
	}}
	stats, err := r.Run(p, nil)
	if err != nil {
		t.Fatalf("宽松模式下不该失败: %v", err)
	}
	if stats.Timeouts != 1 {
		t.Errorf("超时次数 = %d，期望 1", stats.Timeouts)
	}
	if stats.Steps != 2 {
		t.Errorf("步数 = %d，期望 2（超时后仍要继续）", stats.Steps)
	}
	if len(logs) == 0 {
		t.Error("超时应有日志记录")
	}
}

// TestStrictUntilFails 验证 Strict 时超时会让计划失败。
func TestStrictUntilFails(t *testing.T) {
	fe := &fakeExec{frames: []*image.Gray{frame(50), frame(50)}}
	r := NewRunner(fe, noSleep(t))
	p := &Plan{Steps: []Step{
		{Name: "严格步", Action: "ACTION TAP x=0.5 y=0.5",
			Until: &Until{Type: TypeChange}, TimeoutMs: 1, Strict: true},
	}}
	if _, err := r.Run(p, nil); err == nil {
		t.Error("Strict 步超时本应返回错误")
	}
}

// TestUntilStable 验证 stable 条件：画面不再变化时推进（等动画/加载结束）。
func TestUntilStable(t *testing.T) {
	fe := &fakeExec{frames: []*image.Gray{
		frame(0), frame(100), frame(100), // 基准 -> 变化 -> 稳住
	}}
	r := NewRunner(fe, noSleep(t))
	p := &Plan{Steps: []Step{
		{Action: "ACTION WAIT", Until: &Until{Type: TypeStable, Diff: 4}, TimeoutMs: 5000},
	}}
	stats, err := r.Run(p, nil)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if stats.Timeouts != 0 {
		t.Errorf("画面已稳定，不该超时（timeouts=%d）", stats.Timeouts)
	}
}

// TestUntilTime 验证 time 条件只等时长、不抓帧判据。
func TestUntilTime(t *testing.T) {
	fe := &fakeExec{}
	r := NewRunner(fe, noSleep(t))
	p := &Plan{Steps: []Step{
		{Action: "ACTION WAIT", Until: &Until{Type: TypeTime, Ms: 500}},
	}}
	stats, err := r.Run(p, nil)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if stats.Waited != 500*time.Millisecond {
		t.Errorf("等待 = %v，期望 500ms", stats.Waited)
	}
	if fe.grabs != 0 {
		t.Errorf("time 条件不该抓帧，实际抓了 %d 次", fe.grabs)
	}
}

// TestStopChannelHalts 验证 stop 通道能中途停止循环计划。
func TestStopChannelHalts(t *testing.T) {
	fe := &fakeExec{}
	stop := make(chan struct{})
	// 第一轮跑完 1 步后就关闭 stop（用日志回调触发）。
	r := NewRunner(fe, noSleep(t), WithLogf(func(string, ...any) {
		select {
		case <-stop:
		default:
			close(stop)
		}
	}))
	p := &Plan{Loop: true, Steps: []Step{{Action: "ACTION WAIT"}}}
	stats, err := r.Run(p, stop)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if stats.Steps > 2 {
		t.Errorf("stop 已关闭却继续跑了 %d 步", stats.Steps)
	}
}

// TestGrayDiffAndRegion 验证差异计算与区域裁剪。
func TestGrayDiffAndRegion(t *testing.T) {
	if d := grayDiff(frame(10), frame(10), image.Rect(0, 0, 32, 32)); d != 0 {
		t.Errorf("同帧差异 = %v，期望 0", d)
	}
	if d := grayDiff(frame(0), frame(100), image.Rect(0, 0, 32, 32)); d != 100 {
		t.Errorf("0 vs 100 差异 = %v，期望 100", d)
	}
	// 空区域退化为全画面。
	r := normRegion([4]float64{}, image.Rect(0, 0, 100, 200))
	if r.Dx() != 100 || r.Dy() != 200 {
		t.Errorf("空区域应回退全画面，得到 %v", r)
	}
	// 左下角 1/4 区域。
	r2 := normRegion([4]float64{0, 0.5, 0.5, 1}, image.Rect(0, 0, 100, 200))
	if r2 != image.Rect(0, 100, 50, 200) {
		t.Errorf("归一化区域换算错误: %v", r2)
	}
}

// TestGrabErrorTolerated 验证抓帧失败不会让计划崩掉（继续轮询）。
func TestGrabErrorTolerated(t *testing.T) {
	fe := &fakeExec{grabErr: errFake}
	r := NewRunner(fe, noSleep(t))
	p := &Plan{Steps: []Step{
		{Action: "ACTION WAIT", Until: &Until{Type: TypeChange}, TimeoutMs: 1},
	}}
	stats, err := r.Run(p, nil)
	if err != nil {
		t.Fatalf("抓帧失败不该让计划报错: %v", err)
	}
	if stats.Timeouts != 1 {
		t.Errorf("抓帧一直失败应记一次超时，得到 %d", stats.Timeouts)
	}
}

var errFake = &fakeErr{}

type fakeErr struct{}

func (*fakeErr) Error() string { return "fake grab error" }

// fakeColorExec 在图 Grab 之外还能吐彩色帧，用于测 ratio 条件。
//
// colorFrames 每被取一次就前进一帧（到末尾停住），
// 于是「目标色什么时候出现」完全由测试决定。
//
// 方法名是 GrabColorFresh：与后端一致，强调每次都是「新的一帧」——
// 这正是 ratio 轮询成立的前提（若返回缓存帧，轮询判据会失效，
// 见 plan.ColorGrabber 的说明）。
type fakeColorExec struct {
	fakeExec
	colorFrames []image.Image
	colorIdx    int
}

func (f *fakeColorExec) GrabColorFresh() (image.Image, error) {
	if len(f.colorFrames) == 0 {
		return nil, nil
	}
	i := f.colorIdx
	if i >= len(f.colorFrames) {
		i = len(f.colorFrames) - 1
	}
	f.colorIdx++
	return f.colorFrames[i], nil
}

// solid 生成一张纯色彩色帧。
func solid(r, g, b uint8) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 40, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 40; x++ {
			i := img.PixOffset(x, y)
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = r, g, b, 255
		}
	}
	return img
}

// TestValidateRatioAndLoopRules 验证 ratio / 循环模式的静态校验。
func TestValidateRatioAndLoopRules(t *testing.T) {
	cases := []struct {
		name string
		st   Step
	}{
		{"ratio 缺 color", Step{Action: "ACTION TAP x=0.5 y=0.5",
			Until: &Until{Type: TypeRatio}}},
		{"ratio color 越界", Step{Action: "ACTION TAP x=0.5 y=0.5",
			Until: &Until{Type: TypeRatio, Color: [3]int{300, 0, 0}}}},
		{"repeat 与 max_repeat 并存", Step{Action: "ACTION TAP x=0.5 y=0.5",
			Repeat: 3, MaxRepeat: 10, Until: &Until{Type: TypeRatio, Color: [3]int{255, 0, 0}}}},
		{"max_repeat 用 change（会退化）", Step{Action: "ACTION TAP x=0.5 y=0.5",
			MaxRepeat: 10, Until: &Until{Type: TypeChange}}},
		{"max_repeat 缺 until", Step{Action: "ACTION TAP x=0.5 y=0.5", MaxRepeat: 10}},
	}
	for _, c := range cases {
		if err := (&Plan{Steps: []Step{c.st}}).Validate(); err == nil {
			t.Errorf("%s：本应报错却通过了", c.name)
		}
	}
}

// TestLoopUntilRatioStops 验证循环原语：反复点击，目标色出现即停。
func TestLoopUntilRatioStops(t *testing.T) {
	fe := &fakeColorExec{colorFrames: []image.Image{
		solid(30, 30, 30),    // 第1轮后：还没出现
		solid(30, 30, 30),    // 第2轮后：还没出现
		solid(255, 105, 180), // 第3轮后：目标粉色出现
	}}
	r := NewRunner(fe, noSleep(t))
	p := &Plan{Steps: []Step{{
		Name:   "点对话直到出现粉色 UI",
		Action: "ACTION TAP x=0.5 y=0.9",
		// 粉色 #FF69B4，容差 40
		MaxRepeat: 20,
		Until: &Until{Type: TypeRatio,
			Color: [3]int{255, 105, 180}, MinRatio: 0.3},
	}}}
	stats, err := r.Run(p, nil)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if stats.Actions != 3 {
		t.Errorf("动作数 = %d，期望 3（第 3 轮命中后停）", stats.Actions)
	}
	if stats.Timeouts != 0 {
		t.Errorf("命中后不该记超时，timeouts = %d", stats.Timeouts)
	}
}

// TestLoopHitsLimitLenient 验证循环跑满上限时宽松收尾（默认不失败）。
func TestLoopHitsLimitLenient(t *testing.T) {
	fe := &fakeColorExec{colorFrames: []image.Image{solid(0, 0, 0)}}
	r := NewRunner(fe, noSleep(t))
	p := &Plan{Steps: []Step{{
		Action:    "ACTION TAP x=0.5 y=0.9",
		MaxRepeat: 4,
		Until: &Until{Type: TypeRatio,
			Color: [3]int{255, 105, 180}, MinRatio: 0.5},
	}}}
	stats, err := r.Run(p, nil)
	if err != nil {
		t.Fatalf("宽松模式不该失败: %v", err)
	}
	if stats.Actions != 4 {
		t.Errorf("动作数 = %d，期望跑满 4", stats.Actions)
	}
	if stats.Timeouts != 1 {
		t.Errorf("跑满上限应记一次，得到 %d", stats.Timeouts)
	}
}

// TestLoopHitsLimitStrict 验证 Strict 时跑满上限会失败。
func TestLoopHitsLimitStrict(t *testing.T) {
	fe := &fakeColorExec{colorFrames: []image.Image{solid(0, 0, 0)}}
	r := NewRunner(fe, noSleep(t))
	p := &Plan{Steps: []Step{{
		Action:    "ACTION TAP x=0.5 y=0.9",
		MaxRepeat: 2, Strict: true,
		Until: &Until{Type: TypeRatio,
			Color: [3]int{255, 105, 180}, MinRatio: 0.5},
	}}}
	if _, err := r.Run(p, nil); err == nil {
		t.Error("Strict 循环跑满上限本应报错")
	}
}

// TestRatioNeedsColorBackend 验证后端不支持彩色抓帧时给出明确报错，
// 而不是静默地「永远不满足」。
// TestRatioNeedsColorBackend 验证「后端没有新鲜彩帧能力」时给明确报错，不静默失败。
func TestRatioNeedsColorBackend(t *testing.T) {
	fe := &fakeExec{} // 没有 GrabColorFresh
	r := NewRunner(fe, noSleep(t))
	p := &Plan{Steps: []Step{{
		Action:    "ACTION TAP x=0.5 y=0.9",
		MaxRepeat: 2,
		Until: &Until{Type: TypeRatio,
			Color: [3]int{255, 0, 0}, MinRatio: 0.5},
	}}}
	if _, err := r.Run(p, nil); err == nil {
		t.Error("后端不支持彩色抓帧时应报错")
	}
}

// TestRatioRequiresFreshFrames 锁定 2026-09-13 修掉的缺陷：// ratio 是轮询判据，**每一轮都必须基于新画面**。这里用「只返回缓存帧、
// 永不更新」的假后端模拟安卓 GrabColor 的旧行为，正确实现下应当
// 在轮数上限内拿不到目标色而宽松结束（Timeouts>0），
// 而不是靠同一张旧帧「命中」。
//
// 若哪天有人把 GrabColorFresh 改回返回缓存，本测试会立刻变红：
// 因为缓存帧是「非目标色」，永远不该命中；命中即实现出错。
func TestRatioRequiresFreshFrames(t *testing.T) {
	cached := solid(0, 0, 255) // 缓存帧：蓝色，不是目标色
	f := &cachedOnlyExec{cached: cached}
	r := NewRunner(f, noSleep(t))
	p := &Plan{Steps: []Step{{
		Action:    "ACTION TAP x=0.5 y=0.9",
		MaxRepeat: 3,
		GapMs:     1,
		Until: &Until{Type: TypeRatio,
			Color: [3]int{255, 0, 0}, MinRatio: 0.5},
	}}}
	st, err := r.Run(p, nil)
	if err != nil {
		t.Fatalf("宽松模式不该失败: %v", err)
	}
	if st.Timeouts != 1 {
		t.Errorf("画面始终不含目标色时应记一次超时，得到 Timeouts=%d", st.Timeouts)
	}
	if f.calls != 3 {
		t.Errorf("每轮都应取一次新鲜帧，期望 3 次抓帧，实际 %d", f.calls)
	}
}

// cachedOnlyExec 模拟「后端有彩帧能力但每轮返回同一张缓存」的行为。
type cachedOnlyExec struct {
	fakeExec
	cached image.Image
	calls  int
}

func (f *cachedOnlyExec) GrabColorFresh() (image.Image, error) {
	f.calls++
	return f.cached, nil
}

// TestColorRatio 验证占比计算与区域裁剪。
func TestColorRatio(t *testing.T) {
	img := solid(255, 105, 180)
	u := &Until{Type: TypeRatio, Color: [3]int{255, 105, 180}, MinRatio: 0.5}
	if got := colorRatio(img, image.Rect(0, 0, 40, 40), u); got != 1 {
		t.Errorf("纯目标色占比 = %v，期望 1", got)
	}
	// 容差内（偏移 20）仍算命中。
	img2 := solid(235, 125, 200)
	if got := colorRatio(img2, image.Rect(0, 0, 40, 40), u); got != 1 {
		t.Errorf("容差内应算命中，占比 = %v", got)
	}
	// 明显不同色 -> 0。
	img3 := solid(0, 0, 255)
	if got := colorRatio(img3, image.Rect(0, 0, 40, 40), u); got != 0 {
		t.Errorf("异色占比 = %v，期望 0", got)
	}
	// 空区域 -> 0，不 panic。
	if got := colorRatio(img, image.Rect(0, 0, 0, 0), u); got != 0 {
		t.Errorf("空区域占比 = %v，期望 0", got)
	}
}

// TestRatioDisappear 验证 max_ratio 的「目标色消失」语义。
//
// 缘起（2026-09-13 实测）：评审页的绿色进度条消失 = 本局结束，
// 而结算页没有专属颜色（紫色按钮在结算页 46%、SALE 弹窗价格按钮 54%，
// 分不开），所以「等绿条消失」是唯一可靠的推进判据。
func TestRatioDisappear(t *testing.T) {
	u := &Until{Type: TypeRatio, Color: [3]int{77, 224, 114}, MaxRatio: 0.01}

	// 画面还有绿条（占比高）-> 未满足「消失」。
	has := solid(77, 224, 114)
	if u.ratioSatisfied(has, image.Rect(0, 0, 40, 40)) {
		t.Error("绿条仍在时不该算满足")
	}
	// 画面没有绿条 -> 满足。
	gone := solid(20, 30, 40)
	if !u.ratioSatisfied(gone, image.Rect(0, 0, 40, 40)) {
		t.Error("绿条消失后应算满足")
	}
}

// TestRatioDisappearInWait 验证 waitUntil 走的是同一条 max_ratio 语义
// （两个调用点共用 ratioSatisfied，这里守一道，避免将来只改一处）。
func TestRatioDisappearInWait(t *testing.T) {
	f := &cachedOnlyExec{cached: solid(20, 30, 40)} // 无绿条
	r := NewRunner(f, noSleep(t))
	p := &Plan{Steps: []Step{{
		Action: "ACTION WAIT",
		Until: &Until{Type: TypeRatio, Color: [3]int{77, 224, 114}, MaxRatio: 0.01,
			Region: [4]float64{0, 0, 1, 1}, Ms: 1},
		TimeoutMs: 500,
	}}}
	st, err := r.Run(p, nil)
	if err != nil {
		t.Fatalf("不该失败: %v", err)
	}
	if st.Timeouts != 0 {
		t.Errorf("目标色已消失，等待应立即满足，Timeouts=%d", st.Timeouts)
	}
}

// TestValidateRejectsBothRatioBounds 验证 min_ratio 与 max_ratio 互斥。
func TestValidateRejectsBothRatioBounds(t *testing.T) {
	p := &Plan{Steps: []Step{{
		Action: "ACTION WAIT",
		Until: &Until{Type: TypeRatio, Color: [3]int{77, 224, 114},
			MinRatio: 0.05, MaxRatio: 0.01},
	}}}
	if err := p.Validate(); err == nil {
		t.Error("min_ratio 与 max_ratio 同时给出时应报错")
	}
}
