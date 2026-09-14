package plan

import (
	"errors"
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

// countingExec 固定返回一张彩图，并单独统计 Apply 次数。
//
// 为什么需要单独计数（而不是复用 fakeExec.acts 的长度）：守卫测试的核心断言是
// 「一个动作都没发出去」——acts 可能被别处写入，用独立计数器才能锁死这一点。
type countingExec struct {
	fakeExec
	img     image.Image
	applied int
}

func (f *countingExec) GrabColorFresh() (image.Image, error) { return f.img, nil }

func (f *countingExec) Apply(a agent.Action) error {
	f.applied++
	return f.fakeExec.Apply(a)
}

// flakyColorExec 模拟安卓 screencap 的**偶发**失败：前 failN 次返回错误，
// 之后正常返回 frame。
//
// 缘起（2026-09-13 真机实测）：对同一界面连抓 6 帧，第 1 次返回
// rc=4294967295 / 0 字节，随后 5 次全部成功——这是瞬时故障，重试即可恢复。
// 若不重试，这一次失败会被 checkOnce 当成「条件不满足」，
// 让整个循环白跑到上限，而日志上看不出任何异常。
type flakyColorExec struct {
	fakeExec
	failN int
	calls int
	frame image.Image
	err   error
}

func (f *flakyColorExec) GrabColorFresh() (image.Image, error) {
	f.calls++
	if f.calls <= f.failN {
		if f.err != nil {
			return nil, f.err
		}
		return nil, errors.New("screencap 失败（模拟偶发故障）")
	}
	return f.frame, nil
}

// TestRatioRetriesOnTransientGrabFailure 验证「抓帧偶发失败」不会让循环白跑。
//
// 场景：目标是「清弹窗直到出现 ADS 红标」，真实画面**第一帧就该命中**，
// 但抓帧恰好前 2 次失败。有重试时应第 1 轮即停（只按 1 次动作）；
// 无重试则要按 3 次才命中——这正是修复前会发生的退化。
func TestRatioRetriesOnTransientGrabFailure(t *testing.T) {
	f := &flakyColorExec{failN: 2, frame: solid(139, 1, 64)}
	r := NewRunner(f, noSleep(t))
	p := &Plan{Steps: []Step{{
		Name:      "清弹窗",
		Action:    "ACTION PRESS name=close",
		MaxRepeat: 5,
		Until: &Until{Type: TypeRatio, Color: [3]int{139, 1, 64}, MinRatio: 0.5,
			Region: [4]float64{0, 0, 1, 1}},
	}}}
	st, err := r.Run(p, nil)
	if err != nil {
		t.Fatalf("不该失败: %v", err)
	}
	if st.Timeouts != 0 {
		t.Errorf("偶发抓帧失败应被重试吃掉，不该记超时；Timeouts=%d", st.Timeouts)
	}
	if st.Actions != 1 {
		t.Errorf("应第 1 轮即命中（只按 1 次），实际按了 %d 次（calls=%d）", st.Actions, f.calls)
	}
}

// TestRatioGivesUpOnPersistentGrabFailure 验证「持续抓帧失败」的处理：
// 重试耗尽后按未满足处理（宽松），循环正常跑满上限并记一次超时，
// 但**不能**把整份计划炸掉——设备短暂掉线不该让流程崩。
func TestRatioGivesUpOnPersistentGrabFailure(t *testing.T) {
	f := &flakyColorExec{failN: 999, frame: solid(139, 1, 64)}
	r := NewRunner(f, noSleep(t))
	p := &Plan{Steps: []Step{{
		Name:      "清弹窗",
		Action:    "ACTION PRESS name=close",
		MaxRepeat: 4,
		Until: &Until{Type: TypeRatio, Color: [3]int{139, 1, 64}, MinRatio: 0.5,
			Region: [4]float64{0, 0, 1, 1}},
	}}}
	st, err := r.Run(p, nil)
	if err != nil {
		t.Fatalf("持续抓帧失败不该让计划报错（应宽松收尾）: %v", err)
	}
	if st.Actions != 4 {
		t.Errorf("应跑满 4 轮动作，实际 %d 次", st.Actions)
	}
	if st.Timeouts != 1 {
		t.Errorf("跑满上限应记 1 次超时，实际 %d", st.Timeouts)
	}
	// 每轮 1 次 + 重试 (grabRetries-1)，轮数 grabRetries。
	wantCalls := 4 * grabRetries
	if f.calls != wantCalls {
		t.Errorf("抓帧次数 = %d，期望 %d（每轮重试 %d 次）", f.calls, wantCalls, grabRetries)
	}
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

// ---- 前置条件 when（2026-09-13 新增，语义＝「满足才执行」）----

// TestWhenSkipsStepWhenPreconditionUnmet 验证前置条件不满足时整步跳过：
// 不发出任何动作。这是「清残留弹窗」这类步骤的幂等前提。
//
// 缘起（真机实测）：plan 顺序执行、无「当前在哪屏」概念，「清弹窗」在已经
// 干净的主界面上按下去会跳进别的界面（Main -> MY CLOSET），把后面全带偏。
func TestWhenSkipsStepWhenPreconditionUnmet(t *testing.T) {
	// 画面没有 ADS 红标：前置条件「ADS 可见」不满足 → 整步跳过、一个动作都不发。
	f := &countingExec{img: solid(20, 30, 40)}
	r := NewRunner(f, noSleep(t))
	p := &Plan{Steps: []Step{{
		Action:    "ACTION PRESS name=popup_close",
		MaxRepeat: 3,
		GapMs:     1,
		When: &Until{Type: TypeRatio, Color: [3]int{139, 1, 64},
			Region: [4]float64{0, 0, 1, 1}, Tolerance: 30, MinRatio: 0.04},
		Until: &Until{Type: TypeRatio, Color: [3]int{139, 1, 64},
			Region: [4]float64{0, 0, 1, 1}, Tolerance: 30, MinRatio: 0.04},
	}}}
	st, err := r.Run(p, nil)
	if err != nil {
		t.Fatalf("不该失败: %v", err)
	}
	if f.applied != 0 {
		t.Errorf("前置条件不满足时应一个动作都不发，实际发了 %d 次", f.applied)
	}
	if st.Skipped != 1 {
		t.Errorf("Skipped 应为 1，实际 %d", st.Skipped)
	}
}

// TestWhenRunsStepWhenPreconditionMet 验证前置条件满足时照常执行。
func TestWhenRunsStepWhenPreconditionMet(t *testing.T) {
	// 画面有 ADS 红标：前置条件「ADS 可见」满足 → 照常跑满循环。
	f := &countingExec{img: solid(139, 1, 64)}
	r := NewRunner(f, noSleep(t))
	p := &Plan{Steps: []Step{{
		Action:    "ACTION PRESS name=popup_close",
		MaxRepeat: 2,
		GapMs:     1,
		When: &Until{Type: TypeRatio, Color: [3]int{139, 1, 64},
			Region: [4]float64{0, 0, 1, 1}, Tolerance: 30, MinRatio: 0.04},
		// until 用一个画面里**不存在**的颜色，保证循环不会提前命中、能跑满 2 轮。
		Until: &Until{Type: TypeRatio, Color: [3]int{110, 93, 236},
			Region: [4]float64{0, 0, 1, 1}, Tolerance: 20, MinRatio: 0.8},
	}}}
	st, err := r.Run(p, nil)
	if err != nil {
		t.Fatalf("不该失败: %v", err)
	}
	if f.applied != 2 {
		t.Errorf("前置条件满足时应跑满 2 轮，实际 %d 次", f.applied)
	}
	if st.Skipped != 0 {
		t.Errorf("Skipped 应为 0，实际 %d", st.Skipped)
	}
}

// TestWhenPreconditionGatesResultExit 是 2026-09-13 真机踩坑的回归测试。
//
// 早期 when 语义写成「满足则跳过」，于是退出步骤写 when=「Exit 可见」就变成了
// 「结算页在就跳过」——恰好写反。真机日志里表现为「前置条件未满足，执行本步」，
// 在非结算页照样点了一次 exit_result。改成「满足才执行」后，非结算页必须一次都不点。
func TestWhenPreconditionGatesResultExit(t *testing.T) {
	// 不在结算页：区域里没有紫色 Exit 按钮（占比 0% < 80%）。
	f := &countingExec{img: solid(20, 30, 40)}
	r := NewRunner(f, noSleep(t))
	p := &Plan{Steps: []Step{{
		Action: "ACTION PRESS name=exit_result",
		When: &Until{Type: TypeRatio, Color: [3]int{110, 93, 236},
			Region: [4]float64{0.36, 0.888, 0.64, 0.935}, Tolerance: 20, MinRatio: 0.8},
		Until: &Until{Type: TypeRatio, Color: [3]int{139, 1, 64},
			Region: [4]float64{0, 0, 1, 1}, Tolerance: 30, MinRatio: 0.04},
		TimeoutMs: 1,
	}}}
	st, err := r.Run(p, nil)
	if err != nil {
		t.Fatalf("不该失败: %v", err)
	}
	if f.applied != 0 {
		t.Errorf("不在结算页时绝不能点 Exit，实际点了 %d 次", f.applied)
	}
	if st.Skipped != 1 {
		t.Errorf("Skipped 应为 1，实际 %d", st.Skipped)
	}
}

// TestValidateRejectsNonRatioWhen 验证前置条件只接受 ratio：change 是相对量，
// 没有基准帧无从判断「画面现在是不是处于这个状态」。
func TestValidateRejectsNonRatioWhen(t *testing.T) {
	for _, typ := range []string{TypeChange, TypeStable, TypeTime} {
		p := &Plan{Steps: []Step{{
			Action: "ACTION WAIT",
			When:   &Until{Type: typ},
		}}}
		if err := p.Validate(); err == nil {
			t.Errorf("when.type=%q 应被拒绝", typ)
		}
	}
}

// ---- 安全停止（DEVELOPMENT_PLAN §8）与动作重试（P0）----

// unsafeExec 模拟「安全检查不通过」的后端：CheckSafe 一直返回错误。
type unsafeExec struct {
	fakeExec
	img     image.Image
	applied int
}

func (f *unsafeExec) GrabColorFresh() (image.Image, error) { return f.img, nil }
func (f *unsafeExec) CheckSafe() error                     { return errors.New("目标应用不在前台") }
func (f *unsafeExec) Apply(a agent.Action) error {
	f.applied++
	return nil
}

// TestSafetyCheckStopsBeforeAnyAction 验证 §8：安全检查不通过时**一个动作都不发**。
//
// 这是「先安全后智能」的底线——宁可计划不执行，也不能把输入打到别的应用上。
func TestSafetyCheckStopsBeforeAnyAction(t *testing.T) {
	f := &unsafeExec{img: solid(0, 0, 0)}
	r := NewRunner(f, noSleep(t))
	p := &Plan{Steps: []Step{{
		Name:      "点一下",
		Action:    "ACTION PRESS name=x",
		MaxRepeat: 3,
		Until:     &Until{Type: TypeRatio, Color: [3]int{1, 2, 3}, MinRatio: 0.5},
	}}}
	_, err := r.Run(p, nil)
	if err == nil {
		t.Fatal("安全检查不通过时应报错停止，而不是继续跑")
	}
	if f.applied != 0 {
		t.Errorf("安全检查不通过时不该发出任何动作，实际发了 %d 次", f.applied)
	}
}

// flakyActionExec 模拟「动作下发偶发失败」：前 failN 次 Apply 返回错误。
type flakyActionExec struct {
	fakeExec
	failN   int
	calls   int
	applied int
}

func (f *flakyActionExec) Apply(a agent.Action) error {
	f.calls++
	if f.calls <= f.failN {
		return errors.New("adb input 注入失败（模拟瞬时故障）")
	}
	f.applied++
	return nil
}

// TestActionRetriesTransientFailure 验证动作重试：瞬时失败被重试吃掉，计划照常完成。
//
// 缘起（P0 任务「增加动作超时/重试」）：ADB input 与 screencap 同源偶发失败，
// 而 plan 的动作大多是幂等点击，重点一次通常无害；不重试会让一次抖动终止整份计划。
func TestActionRetriesTransientFailure(t *testing.T) {
	f := &flakyActionExec{failN: 1}
	r := NewRunner(f, noSleep(t))
	p := &Plan{Steps: []Step{{
		Name:   "点一下",
		Action: "ACTION TAP x=0.5 y=0.5",
	}}}
	st, err := r.Run(p, nil)
	if err != nil {
		t.Fatalf("瞬时失败应被重试吃掉，不该报错: %v", err)
	}
	if st.Actions != 1 {
		t.Errorf("重试成功后应记 1 个动作，实际 %d", st.Actions)
	}
	if st.ActionRetries != 1 {
		t.Errorf("应记 1 次重试，实际 %d", st.ActionRetries)
	}
	if st.ActionErrors != 0 {
		t.Errorf("重试成功不该记动作失败，实际 %d", st.ActionErrors)
	}
}

// TestActionGivesUpAfterPersistentFailure 验证持续失败时如实报错并记失败数。
func TestActionGivesUpAfterPersistentFailure(t *testing.T) {
	f := &flakyActionExec{failN: 999}
	r := NewRunner(f, noSleep(t))
	p := &Plan{Steps: []Step{{
		Name:   "点一下",
		Action: "ACTION TAP x=0.5 y=0.5",
	}}}
	st, err := r.Run(p, nil)
	if err == nil {
		t.Fatal("持续失败应报错")
	}
	if st.ActionErrors != 1 {
		t.Errorf("应记 1 次动作失败，实际 %d", st.ActionErrors)
	}
	if f.calls != actRetries {
		t.Errorf("应连试 %d 次，实际 %d", actRetries, f.calls)
	}
}

// TestMetricsCollected 验证 P0 要求的延迟指标确实被采集（端到端/抓帧/动作）。
func TestMetricsCollected(t *testing.T) {
	f := &countingExec{img: solid(139, 1, 64)}
	r := NewRunner(f, noSleep(t))
	p := &Plan{Steps: []Step{{
		Name:      "清弹窗",
		Action:    "ACTION PRESS name=close",
		MaxRepeat: 3,
		GapMs:     1,
		Until: &Until{Type: TypeRatio, Color: [3]int{139, 1, 64},
			Region: [4]float64{0, 0, 1, 1}, MinRatio: 0.04},
	}}}
	st, err := r.Run(p, nil)
	if err != nil {
		t.Fatalf("不该失败: %v", err)
	}
	if st.Grabs == 0 {
		t.Error("应记录抓帧次数")
	}
	if st.Actions == 0 {
		t.Error("应记录动作数")
	}
}

// ---- 超时补偿 on_timeout（2026-09-13 新增）----

// compensatedExec 模拟「等目标界面 -> 超时 -> 补偿动作（返回键）-> 界面出现」：
// 收到返回键之前返回「不满足色」，之后返回「目标色」。
//
// 缘起（真机/模拟器实测的差异）：点 Change 触发激励视频后，真机广告播完自动回游戏，
// 模拟器无真实广告源、广告 Activity 永久停在屏幕上（实测 45s 不消失）。用返回键
// 关掉广告是安全的，于是「超时按一下返回键」能把两种环境统一成一条路径。
type compensatedExec struct {
	fakeExec
	applied   []agent.Action
	satisfied bool
}

func (f *compensatedExec) GrabColorFresh() (image.Image, error) {
	if f.satisfied {
		return solid(139, 1, 64), nil // 目标界面（ADS 红标）已出现
	}
	return solid(0, 0, 0), nil // 还卡在广告/加载页
}

func (f *compensatedExec) Apply(a agent.Action) error {
	f.applied = append(f.applied, a)
	if a.Kind == agent.ActionKey {
		f.satisfied = true // 按了返回键 -> 广告关掉、目标界面露出
	}
	return f.fakeExec.Apply(a)
}

// TestOnTimeoutRunsCompensation 验证超时补偿：第一次等待超时后执行 on_timeout 动作，
// 再按同一条件重等；补偿生效则本步成功，且补偿动作只发一次（不是反复发）。
func TestOnTimeoutRunsCompensation(t *testing.T) {
	f := &compensatedExec{}
	r := NewRunner(f, noSleep(t))
	p := &Plan{Steps: []Step{{
		Name:   "等结算页出现（超时按返回键关广告）",
		Action: "ACTION PRESS name=change",
		Until: &Until{Type: TypeRatio, Color: [3]int{139, 1, 64}, MinRatio: 0.5,
			Region: [4]float64{0, 0, 1, 1}},
		TimeoutMs: 1,
		OnTimeout: "ACTION KEY code=back",
	}}}
	stats, err := r.Run(p, nil)
	if err != nil {
		t.Fatalf("补偿生效后不该报错: %v", err)
	}
	if stats.Timeouts != 0 {
		t.Errorf("补偿生效后不该记超时，实际 %d", stats.Timeouts)
	}
	if len(f.applied) != 2 {
		t.Fatalf("期望 2 个动作（change + back），实际 %d", len(f.applied))
	}
	if f.applied[0].Kind != agent.ActionPress {
		t.Errorf("第一个动作应是 PRESS，实际 kind=%v", f.applied[0].Kind)
	}
	if f.applied[1].Kind != agent.ActionKey || f.applied[1].Code != "back" {
		t.Errorf("第二个动作应是 back 键，实际 kind=%v code=%q", f.applied[1].Kind, f.applied[1].Code)
	}
}

// multiActionExec 模拟「背靠背两层广告」：收到 back 键关掉播放层但仍停在结束页，
// 收到按「关闭」按钮（ActionPress）才真正回到游戏。
//
// 缘起（2026-09-13 实测）：模拟器上激励视频卡成两层——播放层 back 可关、
// 结束后露出的「Reward granted」页只能点右上角 X。单一补偿动作覆盖不了，
// 所以 on_timeout 支持 `;` 分隔的多个动作。
type multiActionExec struct {
	fakeExec
	applied   []agent.Action
	satisfied bool
}

func (f *multiActionExec) GrabColorFresh() (image.Image, error) {
	if f.satisfied {
		return solid(139, 1, 64), nil
	}
	return solid(0, 0, 0), nil
}

func (f *multiActionExec) Apply(a agent.Action) error {
	f.applied = append(f.applied, a)
	// 只有点「关闭」按钮才关掉结束页；back 只关播放层，不足以让判据成立。
	if a.Kind == agent.ActionPress && a.Name == "ad_close" {
		f.satisfied = true
	}
	return f.fakeExec.Apply(a)
}

// TestOnTimeoutRunsMultipleCompensations 验证补偿动作序列按顺序全发出去。
func TestOnTimeoutRunsMultipleCompensations(t *testing.T) {
	f := &multiActionExec{}
	r := NewRunner(f, noSleep(t))
	p := &Plan{Steps: []Step{{
		Name:      "等结算页（超时先 back 再点关闭）",
		Action:    "ACTION PRESS name=change",
		Until:     &Until{Type: TypeRatio, Color: [3]int{139, 1, 64}, MinRatio: 0.5, Region: [4]float64{0, 0, 1, 1}},
		TimeoutMs: 1,
		OnTimeout: "ACTION KEY code=back; ACTION PRESS name=ad_close",
	}}}
	stats, err := r.Run(p, nil)
	if err != nil {
		t.Fatalf("补偿生效后不该报错: %v", err)
	}
	if stats.Timeouts != 0 {
		t.Errorf("补偿生效后不该记超时，实际 %d", stats.Timeouts)
	}
	if len(f.applied) != 3 {
		t.Fatalf("期望 3 个动作（change + back + ad_close），实际 %d", len(f.applied))
	}
	if f.applied[1].Kind != agent.ActionKey || f.applied[1].Code != "back" {
		t.Errorf("第二个动作应是 back，实际 kind=%v code=%q", f.applied[1].Kind, f.applied[1].Code)
	}
	if f.applied[2].Kind != agent.ActionPress || f.applied[2].Name != "ad_close" {
		t.Errorf("第三个动作应是 PRESS ad_close，实际 kind=%v name=%q", f.applied[2].Kind, f.applied[2].Name)
	}
}

// TestOnTimeoutStillLenientWhenCompensationFails 验证补偿也没能等到时，
// 仍按既定策略宽松跳过并记一次超时（行为与没有 on_timeout 时一致）。
func TestOnTimeoutStillLenientWhenCompensationFails(t *testing.T) {
	f := &countingExec{img: solid(0, 0, 0)} // 永远不满足
	r := NewRunner(f, noSleep(t))
	p := &Plan{Steps: []Step{{
		Name:   "等不到就宽松跳过",
		Action: "ACTION TAP x=0.5 y=0.5",
		Until: &Until{Type: TypeRatio, Color: [3]int{139, 1, 64}, MinRatio: 0.5,
			Region: [4]float64{0, 0, 1, 1}},
		TimeoutMs: 1,
		OnTimeout: "ACTION KEY code=back",
	}}}
	stats, err := r.Run(p, nil)
	if err != nil {
		t.Fatalf("宽松模式不该失败: %v", err)
	}
	if stats.Timeouts != 1 {
		t.Errorf("补偿后仍没等到，应记 1 次超时，实际 %d", stats.Timeouts)
	}
	if f.applied != 2 { // 1 次 TAP + 1 次 back
		t.Errorf("期望 2 个动作（tap + back），实际 %d", f.applied)
	}
}

// TestValidateRejectsOnTimeoutWithoutUntil 验证 on_timeout 必须配合 until：
// 没有 until 就没有超时，补偿动作永远不会触发，属档案写错。
func TestValidateRejectsOnTimeoutWithoutUntil(t *testing.T) {
	p := &Plan{Steps: []Step{{
		Action:    "ACTION TAP x=0.5 y=0.5",
		OnTimeout: "ACTION KEY code=back",
	}}}
	if err := p.Validate(); err == nil {
		t.Error("有 on_timeout 但无 until，本应报错却通过了")
	}
}

// TestValidateRejectsBadOnTimeout 验证 on_timeout 动作本身也要能解析
// （序列里任一动作非法即报错）。
func TestValidateRejectsBadOnTimeout(t *testing.T) {
	p := &Plan{Steps: []Step{{
		Action:    "ACTION TAP x=0.5 y=0.5",
		Until:     &Until{Type: TypeRatio, Color: [3]int{139, 1, 64}, MinRatio: 0.5},
		OnTimeout: "ACTION FLYAROUND",
	}}}
	if err := p.Validate(); err == nil {
		t.Error("on_timeout 动作无法解析，本应报错却通过了")
	}
	// 序列里第二个动作非法也要能查出来。
	p2 := &Plan{Steps: []Step{{
		Action:    "ACTION TAP x=0.5 y=0.5",
		Until:     &Until{Type: TypeRatio, Color: [3]int{139, 1, 64}, MinRatio: 0.5},
		OnTimeout: "ACTION KEY code=back; ACTION FLYAROUND",
	}}}
	if err := p2.Validate(); err == nil {
		t.Error("on_timeout 序列中第二个动作非法，本应报错却通过了")
	}
}

// stubbornAdExec 模拟「补一次不够、要补三次才关掉的广告」：每收到一个返回键
// 只向上推进一层，直到第 3 次才让判据成立。
//
// 缘起（2026-09-13 实测）：模拟器上的激励视频卡法不止一种——可能卡在播放层、
// 可能卡在「Reward granted」结束页、偶发还会跳去应用商店详情页。单补一次只能
// 处理其中一层，必须允许按同一条件多等几轮、每轮补一手。
type stubbornAdExec struct {
	fakeExec
	applied []agent.Action
	stage   int // 已关掉的广告层数
}

func (f *stubbornAdExec) GrabColorFresh() (image.Image, error) {
	if f.stage >= 3 {
		return solid(139, 1, 64), nil
	}
	return solid(0, 0, 0), nil
}

func (f *stubbornAdExec) Apply(a agent.Action) error {
	f.applied = append(f.applied, a)
	if a.Kind == agent.ActionKey && a.Code == "back" {
		f.stage++
	}
	return f.fakeExec.Apply(a)
}

// TestOnTimeoutCompRetriesConverges 验证多轮补偿：补偿动作在限定轮数内反复执行，
// 直到判据成立为止，且不会多补（第 3 轮命中后立刻停）。
func TestOnTimeoutCompRetriesConverges(t *testing.T) {
	f := &stubbornAdExec{}
	r := NewRunner(f, noSleep(t))
	p := &Plan{Steps: []Step{{
		Name:        "等结算页（广告顽固，最多补 5 轮）",
		Action:      "ACTION PRESS name=change",
		Until:       &Until{Type: TypeRatio, Color: [3]int{139, 1, 64}, MinRatio: 0.5, Region: [4]float64{0, 0, 1, 1}},
		TimeoutMs:   1,
		OnTimeout:   "ACTION KEY code=back",
		CompRetries: 5,
	}}}
	stats, err := r.Run(p, nil)
	if err != nil {
		t.Fatalf("补偿收敛后不该报错: %v", err)
	}
	if stats.Timeouts != 0 {
		t.Errorf("补偿收敛后不该记超时，实际 %d", stats.Timeouts)
	}
	// 1 次 change + 3 次 back
	if len(f.applied) != 4 {
		t.Fatalf("期望 4 个动作（change + back×3），实际 %d", len(f.applied))
	}
	if f.stage != 3 {
		t.Errorf("应恰好补 3 轮，实际补了 %d 轮", f.stage)
	}
}

// TestOnTimeoutCompRetriesExhausted 验证补满轮数仍未等到时，按宽松策略收尾，
// 且补偿总次数等于 comp_retries（不多不少）。
func TestOnTimeoutCompRetriesExhausted(t *testing.T) {
	f := &countingExec{img: solid(0, 0, 0)} // 永远不满足
	r := NewRunner(f, noSleep(t))
	p := &Plan{Steps: []Step{{
		Name:        "永远等不到（补 2 轮）",
		Action:      "ACTION TAP x=0.5 y=0.5",
		Until:       &Until{Type: TypeRatio, Color: [3]int{139, 1, 64}, MinRatio: 0.5, Region: [4]float64{0, 0, 1, 1}},
		TimeoutMs:   1,
		OnTimeout:   "ACTION KEY code=back",
		CompRetries: 2,
	}}}
	stats, err := r.Run(p, nil)
	if err != nil {
		t.Fatalf("宽松模式不该失败: %v", err)
	}
	if stats.Timeouts != 1 {
		t.Errorf("补满仍没等到，应记 1 次超时，实际 %d", stats.Timeouts)
	}
	// 1 次 TAP + 2 轮 × 1 个 back = 3
	if f.applied != 3 {
		t.Errorf("期望 3 个动作（tap + back×2），实际 %d", f.applied)
	}
}

// TestValidateRejectsCompRetriesWithoutOnTimeout 验证 comp_retries 必须配合
// on_timeout：没有补偿动作就没有可重试的东西。
func TestValidateRejectsCompRetriesWithoutOnTimeout(t *testing.T) {
	p := &Plan{Steps: []Step{{
		Action:      "ACTION TAP x=0.5 y=0.5",
		Until:       &Until{Type: TypeRatio, Color: [3]int{139, 1, 64}, MinRatio: 0.5},
		CompRetries: 3,
	}}}
	if err := p.Validate(); err == nil {
		t.Error("有 comp_retries 但无 on_timeout，本应报错却通过了")
	}
}

// TestValidateRejectsCompRetriesOutOfRange 验证 comp_retries 的取值范围校验：
// 写错一个 0 会让一步卡到天亮（轮数 × 超时 = 最长等待）。
func TestValidateRejectsCompRetriesOutOfRange(t *testing.T) {
	bad := []int{-1, 21, 999}
	for _, n := range bad {
		p := &Plan{Steps: []Step{{
			Action:      "ACTION TAP x=0.5 y=0.5",
			Until:       &Until{Type: TypeRatio, Color: [3]int{139, 1, 64}, MinRatio: 0.5},
			OnTimeout:   "ACTION KEY code=back",
			CompRetries: n,
		}}}
		if err := p.Validate(); err == nil {
			t.Errorf("comp_retries=%d 越界，本应报错却通过了", n)
		}
	}
}
