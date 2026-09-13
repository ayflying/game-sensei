// Package plan 提供「确定性执行计划」：把一次摸索出来的操作流程固化成
// 可重复执行、**不调用任何大模型**的步骤序列。
//
// 为什么需要它（2026-09-13 安酱明确提出）：
//
//	老师 VLM 单步决策 1~2s、每次都要花钱，用来「探索未知界面」是合适的，
//	但用来「重复已学会的流程」既慢又贵。真正的学习闭环应该是：
//	  老师探索（贵，一次性） -> 把流程写进档案 plan（免费） -> 确定性执行器跑
//	本包就是那个「确定性执行器」。
//
// 与 macro（档案宏）的分工：
//   - macro 是「一次 PRESS 展开成一串固定动作」，仍需要有人（模型）决定按它；
//   - plan 是「整段流程自己决定什么时候进入下一步」，完全不需要模型参与。
//
// 与 student（量化学生）的分工：
//   - student 是「看画面出动作」的模型，快但需要训练数据、且跨游戏泛化差；
//   - plan 是「照着写好的流程走」，零训练、零推理、零成本，适合流程固定的场景。
//
// 核心设计：用「画面反馈」代替「死等固定时长」。
//
//	死等 sleep 2000ms × 20 步 = 40s 的纯浪费；改成「等到画面变化就推进」，
//	通常 200~400ms 即可继续，整体提速 5~10 倍。这是本包存在的第二理由。
package plan

import (
	"fmt"
	"image"
	"strings"
	"time"

	"github.com/ayflying/game-sensei/internal/agent"
)

// Plan 是一段确定性执行计划：按顺序跑 Steps，跑完可循环。
type Plan struct {
	// Name 计划名（日志用），如「打一局 Win VS」。
	Name string `json:"name,omitempty"`
	// Loop 跑完最后一步后是否从头再来。适合「反复刷同一关」的固化场景。
	Loop bool `json:"loop,omitempty"`
	// Steps 步骤序列。
	Steps []Step `json:"steps"`
}

// Step 是一步：一个动作（可重复）+ 可选的推进条件。
type Step struct {
	// Name 步骤名（日志用）。留空则用动作描述。
	Name string `json:"name,omitempty"`

	// Action 是要执行的动作，语法与老师输出一致（由 agent.ParseAction 解析）：
	//   "ACTION TAP x=0.5 y=0.9" / "ACTION PRESS name=confirm" / "ACTION WAIT"
	//   "ACTION KEY code=back" / "ACTION MOVE dir=前 dur=300"
	// 直接写 "TAP x=0.5 y=0.9" 也可以，前缀 ACTION 可省。
	Action string `json:"action"`

	// Repeat 连做几次（默认 1）。典型的「连点继续推进对话」用 Repeat+Until。
	Repeat int `json:"repeat,omitempty"`

	// MaxRepeat > 0 时进入「循环模式」：反复执行 Action，每轮立即检查 Until，
	// 满足即停，最多跑 MaxRepeat 轮。这是「连点对话直到出现某界面」的原语——
	// 对话条数不固定（剧情长短、是否插播），写死 Repeat 必然要么点不够要么白点。
	//
	// 与 Repeat 的分工：Repeat 是「固定的几次」，MaxRepeat 是「不定次数、看画面收手」。
	MaxRepeat int `json:"max_repeat,omitempty"`

	// GapMs 重复之间的间隔（毫秒）。无 Until 时也兼作「动作后的固定等待」。
	GapMs int `json:"gap_ms,omitempty"`

	// Until 推进条件：满足它才进入下一步。为空则按 GapMs 固定等待。
	Until *Until `json:"until,omitempty"`

	// TimeoutMs 等待 Until 的超时（毫秒，默认 3000）。超时不一定致命：
	// 界面可能没按预期变（比如弹了活动弹窗），默认记日志继续，见 Strict。
	TimeoutMs int `json:"timeout_ms,omitempty"`

	// Strict 为 true 时，Until 超时会让整个计划失败；默认 false（宽松跳过）。
	// 宽松是默认，因为手游随时可能插播广告/活动弹窗，硬失败会让计划一步都走不完。
	Strict bool `json:"strict,omitempty"`
}

// Until 是推进条件——用画面反馈决定何时继续，代替写死的 sleep。
type Until struct {
	// Type 条件类型：
	//   "change" 画面相对基准发生变化（默认，最常用：点完等界面切换）
	//   "stable" 画面连续两帧不再变化（等动画播完/等加载结束）
	//   "ratio"  指定区域内「目标颜色」的像素占比达到阈值
	//            （等某个 UI 出现，如按钮/图标；需后端支持彩色抓帧）
	//   "time"   只等 Ms 毫秒（等价于 sleep，用于确实没有可观测变化的场景）
	//   ""       同 "change"
	Type string `json:"type,omitempty"`

	// Diff 灰度平均绝对差阈值（0~255），change/stable 用。默认 4。
	// 太小会被压缩噪声误触，太大则迟钝；4 在降采样灰度帧上是实测较稳的值。
	Diff float64 `json:"diff,omitempty"`

	// Region 关注区域（归一化 [x0,y0,x1,y1]），只比较这一块。
	// 留空（或宽高为 0）= 全屏。用于忽略固定不动的 HUD（顶栏货币、底部按钮栏），
	// 避免它们把「画面变化」的判据压平。
	Region [4]float64 `json:"region,omitempty"`

	// Color 是 ratio 类型的目标颜色（[R,G,B]，0~255）。
	Color [3]int `json:"color,omitempty"`
	// Tolerance 是颜色匹配的每通道容差（0~255，默认 40）。
	// 40 的取舍：既要容忍压缩/抗锯齿带来的色偏，又不能把相邻配色算进来。
	Tolerance int `json:"tolerance,omitempty"`
	// MinRatio 是 ratio 类型的占比下限（0~1，默认 0.05）。
	MinRatio float64 `json:"min_ratio,omitempty"`

	// MaxRatio > 0 时把 ratio 语义**反转**为「目标色消失」：占比 <= MaxRatio 才算满足。
	//
	// 为什么需要（2026-09-13 实测遇到的真实缺口）：多数推进是「等某 UI 出现」，
	// 但也有大量场景只能靠「某 UI 消失」判断——例如
	//   - 评审页的绿色进度条消失 = 本局评审结束，可以进结算；
	//   - 加载转圈消失 = 场景切换完成。
	// 这些「离开某状态」的信号，用「出现了什么」表达不了：结算页并没有一个
	// 独属于自己的颜色（实测紫色按钮在结算页与 SALE 弹窗价格按钮上同时出现，
	// 占比 46% vs 54%，根本分不开）。判「绿条没了」反而是唯一稳的判据。
	//
	// 与 MinRatio 互斥：同时给会报错，避免出现「既要 >=a 又要 <=b」的歧义条件。
	MaxRatio float64 `json:"max_ratio,omitempty"`

	// Ms time 类型的时长；change/stable/ratio 类型下作为轮询间隔。
	// 留空时 change/stable/ratio 用 120ms，time 用 Step.GapMs（再不行 300ms）。
	Ms int `json:"ms,omitempty"`
}

// defaults 返回条件默认值。空 Type 视为 change（最常用：点完等界面切换）。
func (u *Until) defaults() (diff float64, poll int) {
	diff = u.Diff
	if diff <= 0 {
		diff = 4
	}
	poll = u.Ms
	if poll <= 0 {
		if u.Type == TypeTime {
			poll = 300
		} else {
			poll = 120
		}
	}
	return
}

// 推进条件类型。
const (
	TypeChange = "change"
	TypeStable = "stable"
	TypeRatio  = "ratio"
	TypeTime   = "time"
)

// Executor 是执行器需要的最小后端能力。
//
// 刻意收窄到两个方法（而不是复用 cmd/helper 的 backend 接口）：
// 本包不该知道「窗口域 / live / 平台描述」这些概念，只要「能抓帧、能发动作」
// 就够跑一份计划。pcBackend 与 adbBackend 天然满足它。
type Executor interface {
	// Grab 抓一帧灰度观测（downWidth>0 时等比降采样）。
	Grab(downWidth int) (*image.Gray, error)
	// Apply 执行一个动作（dry-run 时由后端自行吞掉）。
	Apply(act agent.Action) error
}

// ColorGrabber 是可选能力：能抓**当前**彩色帧。
//
// ratio 条件需要颜色信息，灰度帧做不到。做成**可选接口**而不是塞进 Executor，
// 是为了不强迫每个后端实现它（测试用的假后端就不需要），
// 用到 ratio 时再断言，缺了就给明确的报错。
//
// 注意方法名带 Fresh：必须是「现在抓一帧」，不能返回缓存。
// 起因（2026-09-13 实测）：安卓后端的 GrabColor 会复用最近一次截图缓存
// （那是给老师用的——老师与感知在同拍上只差几毫秒，复用可省一次约 0.94s
// 的 screencap）。但 ratio 是**轮询**判据：若每次都拿同一张缓存帧，
// 条件要么立刻成立要么永不成立，轮询完全失去意义。故这里刻意用不同方法名，
// 把「要新鲜帧」这个诉求显式化——命名不同，实现者必须单独想一次。
type ColorGrabber interface {
	GrabColorFresh() (image.Image, error)
}

// 默认抓帧降采样宽度。差异检测不需要清晰画面，越小越快：
// 64px 宽的灰度帧足以判断「界面有没有切换」。
const defaultDownWidth = 64

// Stats 汇报一次执行的统计，供调用方判断「固化是否真的省下来了」。
type Stats struct {
	Steps    int           // 执行步数
	Actions  int           // 实际发出的动作次数（含 Repeat）
	Waited   time.Duration // 累计等待时长
	Timeouts int           // Until 超时次数（宽松跳过）
}

// Runner 执行一份 Plan。
type Runner struct {
	exec  Executor
	logf  func(format string, args ...any)
	sleep func(time.Duration)
	now   func() time.Time
	downW int
}

// Option 调整 Runner 行为（测试与非默认场景用）。
type Option func(*Runner)

// WithLogf 注入日志函数（默认丢弃）。
func WithLogf(f func(string, ...any)) Option {
	return func(r *Runner) {
		if f != nil {
			r.logf = f
		}
	}
}

// WithSleep 注入等待实现（测试用，默认 time.Sleep）。
func WithSleep(f func(time.Duration)) Option {
	return func(r *Runner) {
		if f != nil {
			r.sleep = f
		}
	}
}

// WithDownWidth 指定差异检测的抓帧降采样宽度（默认 64）。
func WithDownWidth(w int) Option {
	return func(r *Runner) {
		if w > 0 {
			r.downW = w
		}
	}
}

// NewRunner 构造执行器。
func NewRunner(exec Executor, opts ...Option) *Runner {
	r := &Runner{
		exec:  exec,
		logf:  func(string, ...any) {},
		sleep: time.Sleep,
		now:   time.Now,
		downW: defaultDownWidth,
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Validate 静态校验计划：动作能否解析、条件类型是否认识。
// 让「档案写错」在启动时就报出来，而不是跑到一半才发现。
func (p *Plan) Validate() error {
	if p == nil {
		return fmt.Errorf("plan 为空")
	}
	if len(p.Steps) == 0 {
		return fmt.Errorf("plan %q 没有任何步骤", p.Name)
	}
	for i, st := range p.Steps {
		if strings.TrimSpace(st.Action) == "" {
			return fmt.Errorf("plan %q 第 %d 步（%s）没有 action", p.Name, i+1, st.Name)
		}
		if _, err := agent.ParseAction(st.Action); err != nil {
			return fmt.Errorf("plan %q 第 %d 步（%s）动作无法解析: %q（%v）",
				p.Name, i+1, st.Name, st.Action, err)
		}
		if st.Until != nil {
			switch st.Until.Type {
			case "", TypeChange, TypeStable, TypeRatio, TypeTime:
			default:
				return fmt.Errorf("plan %q 第 %d 步（%s）until.type 不合法: %q（可选 change|stable|ratio|time）",
					p.Name, i+1, st.Name, st.Until.Type)
			}
			if st.Until.Type == TypeRatio {
				r := st.Until
				if r.Color == [3]int{} {
					return fmt.Errorf("plan %q 第 %d 步（%s）ratio 条件缺 color [R,G,B]",
						p.Name, i+1, st.Name)
				}
				for _, c := range r.Color {
					if c < 0 || c > 255 {
						return fmt.Errorf("plan %q 第 %d 步（%s）ratio 的 color 分量须在 0~255",
							p.Name, i+1, st.Name)
					}
				}
				// min_ratio 与 max_ratio 语义相反（出现 vs 消失），同时给是配置错误：
				// 若两个都写，判据会变成「a<=x<=b」这种谁也不想要的东西。
				if r.MinRatio > 0 && r.MaxRatio > 0 {
					return fmt.Errorf("plan %q 第 %d 步（%s）ratio 的 min_ratio 与 max_ratio 互斥："+
						"min_ratio 判「目标色出现」，max_ratio 判「目标色消失」，只能给一个",
						p.Name, i+1, st.Name)
				}
			}
			if st.MaxRepeat < 0 {
				return fmt.Errorf("plan %q 第 %d 步（%s）max_repeat 不能为负", p.Name, i+1, st.Name)
			}
			if st.MaxRepeat > 0 && st.Repeat > 1 {
				return fmt.Errorf("plan %q 第 %d 步（%s）repeat 与 max_repeat 互斥，"+
					"固定次数用 repeat，不定次数看画面收手用 max_repeat", p.Name, i+1, st.Name)
			}
			// 循环模式必须有「绝对特征」判据：change 需要基准帧（每轮动作后
			// 基准已被自己破坏），stable 在循环里每轮本就该变，二者都会退化。
			// 只有 ratio（指定区域出现目标色）是可靠的停止条件。
			if st.MaxRepeat > 0 && st.Until.Type != TypeRatio {
				return fmt.Errorf("plan %q 第 %d 步（%s）循环模式（max_repeat）的 until.type 必须是 ratio，"+
					"当前是 %q —— 见 README：循环停止条件要判「出现什么」，不能判「变了没有」",
					p.Name, i+1, st.Name, st.Until.Type)
			}
		} else if st.MaxRepeat > 0 {
			return fmt.Errorf("plan %q 第 %d 步（%s）有 max_repeat 但没有 until，"+
				"循环模式必须给出停止条件，否则会一直点到上限", p.Name, i+1, st.Name)
		}
	}
	return nil
}

// Run 执行整份计划。Loop 为 true 时，直到 stop 通道关闭才返回。
//
// 单个 Step 的 Until 超时默认不致命（记录并继续），以便跨过偶发弹窗；
// Step.Strict 可改为致命。
func (r *Runner) Run(p *Plan, stop <-chan struct{}) (Stats, error) {
	if err := p.Validate(); err != nil {
		return Stats{}, err
	}
	var st Stats
	for {
		for i := range p.Steps {
			select {
			case <-stop:
				return st, nil
			default:
			}
			if err := r.runStep(&p.Steps[i], &st); err != nil {
				return st, fmt.Errorf("第 %d 步（%s）: %w", i+1, p.Steps[i].Name, err)
			}
			st.Steps++
		}
		if !p.Loop {
			return st, nil
		}
	}
}

// runStep 执行一步。
func (r *Runner) runStep(st *Step, stats *Stats) error {
	act, err := agent.ParseAction(st.Action)
	if err != nil {
		return fmt.Errorf("动作无法解析 %q: %w", st.Action, err)
	}
	label := st.Name
	if label == "" {
		label = act.String()
	}

	// 循环模式：反复执行，每轮立即判定 Until，满足即停。
	switch {
	case st.MaxRepeat > 0:
		return r.runLoopStep(st, act, label, stats)
	default:
		return r.runOnceStep(st, act, label, stats)
	}
}

// runOnceStep 是普通步骤：执行（可 Repeat 次）后按 Until 等待推进。
func (r *Runner) runOnceStep(st *Step, act agent.Action, label string, stats *Stats) error {
	// change 条件需要「动作之前」的基准帧：动作可能瞬间改变画面，
	// 动作后再抓基准会把变化本身当成基准，永远等不到差异。
	// ratio 条件不需要基准（它判定绝对特征），所以不必预先抓帧。
	var base *image.Gray
	needBase := st.Until != nil && (st.Until.Type == TypeChange || st.Until.Type == "")
	if needBase {
		if b, err := r.exec.Grab(r.downW); err == nil {
			base = b
		}
	}

	n := st.Repeat
	if n <= 0 {
		n = 1
	}
	for i := 0; i < n; i++ {
		if err := r.exec.Apply(act); err != nil {
			return err
		}
		stats.Actions++
		if i < n-1 && st.GapMs > 0 {
			r.sleep(time.Duration(st.GapMs) * time.Millisecond)
			stats.Waited += time.Duration(st.GapMs) * time.Millisecond
		}
	}

	if st.Until == nil {
		if st.GapMs > 0 {
			r.sleep(time.Duration(st.GapMs) * time.Millisecond)
			stats.Waited += time.Duration(st.GapMs) * time.Millisecond
		}
		r.logf("step %s: %s%s", label, act.String(), repeatNote(n))
		return nil
	}

	ok, d := r.waitUntil(st, base)
	stats.Waited += d
	if !ok {
		stats.Timeouts++
		if st.Strict {
			return fmt.Errorf("推进条件超时（%s）", describeUntil(st.Until))
		}
		r.logf("step %s: %s —— 条件超时，宽松跳过（耗时 %v）",
			label, act.String(), d.Round(time.Millisecond))
		return nil
	}
	r.logf("step %s: %s —— 条件满足（%v）", label, act.String(), d.Round(time.Millisecond))
	return nil
}

// runLoopStep 是循环步骤：反复执行直到 Until 立即判定成立，或达到 MaxRepeat。
//
// 这是「连点对话直到出现某界面」的原语。与 waitUntil 的区别：这里是
// **每轮动作后立刻判定**（不等固定时间），因为每轮动作本身就可能带来变化；
// 而 waitUntil 是「动作已做完，等条件后续成立」。
func (r *Runner) runLoopStep(st *Step, act agent.Action, label string, stats *Stats) error {
	_, poll := st.Until.defaults()
	gap := poll
	if st.GapMs > 0 {
		gap = st.GapMs
	}
	for i := 0; i < st.MaxRepeat; i++ {
		if err := r.exec.Apply(act); err != nil {
			return err
		}
		stats.Actions++
		// 动作到界面刷新通常需要一点时间，先给一个短间隔再判定，
		// 否则会把「点击还没生效」误判成「条件未满足」而多点一次。
		r.sleep(time.Duration(gap) * time.Millisecond)
		stats.Waited += time.Duration(gap) * time.Millisecond

		ok, err := r.checkOnce(st.Until)
		if err != nil {
			return err
		}
		if ok {
			r.logf("step %s: %s —— 第 %d 轮命中（%s），停止循环",
				label, act.String(), i+1, describeUntil(st.Until))
			return nil
		}
	}
	// 跑到上限仍未命中：宽松收尾（手游多一轮点击通常无害），并留痕。
	stats.Timeouts++
	if st.Strict {
		return fmt.Errorf("循环 %d 轮仍未满足（%s）", st.MaxRepeat, describeUntil(st.Until))
	}
	r.logf("step %s: %s —— 循环 %d 轮未命中（%s），宽松结束",
		label, act.String(), st.MaxRepeat, describeUntil(st.Until))
	return nil
}

// checkOnce 立即判定一次条件（不做超时等待）。
//
// 循环模式下 Validate 已保证条件是 ratio，所以这里只实现 ratio；
// 其他类型给明确报错而不是静默 false——静默会让「条件永远不满足」
// 表现为「点到上限才停」，很难查。
func (r *Runner) checkOnce(u *Until) (bool, error) {
	if u.Type != TypeRatio {
		return false, fmt.Errorf("循环模式只支持 ratio 条件，当前 %q", u.Type)
	}
	cg, ok := r.exec.(ColorGrabber)
	if !ok {
		return false, fmt.Errorf("ratio 条件需要后端支持彩色抓帧（GrabColorFresh），当前后端没有")
	}
	img, err := cg.GrabColorFresh()
	if err != nil || img == nil {
		return false, nil
	}
	region := normRegion(u.Region, img.Bounds())
	return u.ratioSatisfied(img, region), nil
}

// ratioSatisfied 判定一张彩帧是否满足 ratio 条件。
//
// 正向（默认）：目标色占比 >= min_ratio —— 「某 UI 出现了」。
// 反向（给了 max_ratio）：占比 <= max_ratio —— 「某 UI 消失了」。
// 两种语义共用一个实现，保证 checkOnce（循环）与 waitUntil（等待）行为一致。
func (u *Until) ratioSatisfied(img image.Image, region image.Rectangle) bool {
	got := colorRatio(img, region, u)
	if u.MaxRatio > 0 {
		return got <= u.MaxRatio
	}
	return got >= u.minRatio()
}

// minRatio 返回占比阈值（默认 0.05）。
func (u *Until) minRatio() float64 {
	if u.MinRatio > 0 {
		return u.MinRatio
	}
	return 0.05
}

// tolerance 返回颜色匹配的每通道容差（默认 40）。
func (u *Until) tolerance() int {
	if u.Tolerance > 0 {
		return u.Tolerance
	}
	return 40
}

// colorRatio 计算区域内「接近目标色」的像素占比（0~1）。
//
// 为什么要用「占比」而不是「平均色」：目标 UI 通常只占区域一小部分
// （一颗粉色心形、一个紫色按钮），平均色会被背景稀释到几乎不变；
// 占比对「小块 UI 出现」敏感得多，且阈值语义直观（“5% 的像素是粉色”）。
func colorRatio(img image.Image, region image.Rectangle, u *Until) float64 {
	b := img.Bounds()
	r := region.Intersect(b)
	if r.Empty() {
		return 0
	}
	tol := u.tolerance()
	tr, tg, tb := u.Color[0], u.Color[1], u.Color[2]
	var hit, total int64
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			dr := int(cr>>8) - tr
			dg := int(cg>>8) - tg
			db := int(cb>>8) - tb
			if dr < 0 {
				dr = -dr
			}
			if dg < 0 {
				dg = -dg
			}
			if db < 0 {
				db = -db
			}
			if dr <= tol && dg <= tol && db <= tol {
				hit++
			}
			total++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(hit) / float64(total)
}

// waitUntil 轮询直到条件满足或超时。返回 (是否满足, 实际耗时)。
func (r *Runner) waitUntil(st *Step, base *image.Gray) (bool, time.Duration) {
	u := st.Until
	diff, poll := u.defaults()
	timeout := time.Duration(st.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	start := r.now()
	deadline := start.Add(timeout)

	// time 条件：直接等，不抓帧。
	if u.Type == TypeTime {
		ms := u.Ms
		if ms <= 0 {
			ms = st.GapMs
		}
		if ms <= 0 {
			ms = 300
		}
		r.sleep(time.Duration(ms) * time.Millisecond)
		return true, time.Duration(ms) * time.Millisecond
	}

	// ratio 条件：不抓灰度帧，改抓彩色帧判目标色占比。
	// 单独一条路径是因为它需要的输入（颜色）与 change/stable（灰度）不同。
	if u.Type == TypeRatio {
		cg, ok := r.exec.(ColorGrabber)
		if !ok {
			return false, r.now().Sub(start)
		}
		for {
			if !r.now().Before(deadline) {
				return false, r.now().Sub(start)
			}
			img, err := cg.GrabColorFresh()
			if err == nil && img != nil {
				if u.ratioSatisfied(img, normRegion(u.Region, img.Bounds())) {
					return true, r.now().Sub(start)
				}
			}
			r.sleep(time.Duration(poll) * time.Millisecond)
		}
	}

	// 轮询采样：每次抓一帧，与基准（change）或上一帧（stable）比较。
	region := image.Rectangle{}
	prev := base
	for {
		if !r.now().Before(deadline) {
			return false, r.now().Sub(start)
		}
		cur, err := r.exec.Grab(r.downW)
		if err != nil || cur == nil {
			r.sleep(time.Duration(poll) * time.Millisecond)
			continue
		}
		if region.Empty() {
			region = normRegion(u.Region, cur.Bounds())
		}
		switch u.Type {
		case TypeStable:
			// change 需要基准帧；若动作前没抓到（抓帧失败），用首次采样兜底。
			if prev == nil {
				prev = cur
				r.sleep(time.Duration(poll) * time.Millisecond)
				continue
			}
			if grayDiff(prev, cur, region) < diff {
				return true, r.now().Sub(start)
			}
			prev = cur
		default: // change（含空 Type）
			// ratio 不走灰度路径（见下方前置分支），到这里的只有 change。
			if prev == nil {
				prev = cur
				r.sleep(time.Duration(poll) * time.Millisecond)
				continue
			}
			if grayDiff(prev, cur, region) >= diff {
				return true, r.now().Sub(start)
			}
		}
		r.sleep(time.Duration(poll) * time.Millisecond)
	}
}

// repeatNote 生成「×N」后缀，单次时为空。
func repeatNote(n int) string {
	if n > 1 {
		return fmt.Sprintf(" ×%d", n)
	}
	return ""
}

// describeUntil 给条件配一句可读描述（日志/报错用）。
func describeUntil(u *Until) string {
	if u == nil {
		return "无"
	}
	switch u.Type {
	case TypeStable:
		return "画面稳定"
	case TypeRatio:
		if u.MaxRatio > 0 {
			return fmt.Sprintf("区域 RGB(%d,%d,%d) 消失（占比<=%.1f%%）",
				u.Color[0], u.Color[1], u.Color[2], u.MaxRatio*100)
		}
		return fmt.Sprintf("区域出现 RGB(%d,%d,%d) 占比>=%.1f%%",
			u.Color[0], u.Color[1], u.Color[2], u.minRatio()*100)
	case TypeTime, "":
		return fmt.Sprintf("等待 %dms", u.Ms)
	default:
		return fmt.Sprintf("画面变化(diff>=%.1f)", u.Diff)
	}
}

// normRegion 把归一化区域换算成像素矩形；区域为空（宽高<=0）时返回全画面。
func normRegion(a [4]float64, b image.Rectangle) image.Rectangle {
	if a[2] <= a[0] || a[3] <= a[1] {
		return b
	}
	w, h := float64(b.Dx()), float64(b.Dy())
	return image.Rect(
		b.Min.X+int(a[0]*w), b.Min.Y+int(a[1]*h),
		b.Min.X+int(a[2]*w), b.Min.Y+int(a[3]*h),
	)
}

// grayDiff 计算两帧在指定区域内的灰度平均绝对差（0~255）。
//
// 用平均绝对差而不是均方误差：前者对「整块界面切换」与「局部弹窗出现」
// 都给出与面积成正比的线性响应，阈值好定；后者会被少数极端像素带偏。
func grayDiff(a, b *image.Gray, region image.Rectangle) float64 {
	if a == nil || b == nil {
		return 0
	}
	r := region.Intersect(a.Bounds()).Intersect(b.Bounds())
	if r.Empty() {
		return 0
	}
	var sum int64
	var n int64
	for y := r.Min.Y; y < r.Max.Y; y++ {
		offA := (y-a.Rect.Min.Y)*a.Stride - a.Rect.Min.X
		offB := (y-b.Rect.Min.Y)*b.Stride - b.Rect.Min.X
		for x := r.Min.X; x < r.Max.X; x++ {
			d := int(a.Pix[offA+x]) - int(b.Pix[offB+x])
			if d < 0 {
				d = -d
			}
			sum += int64(d)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return float64(sum) / float64(n)
}
