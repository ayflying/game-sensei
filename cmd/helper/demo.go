package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ayflying/game-sensei/internal/agent"
	"github.com/ayflying/game-sensei/internal/config"
	"github.com/ayflying/game-sensei/internal/dataset"
	"github.com/ayflying/game-sensei/internal/game"
	"github.com/ayflying/game-sensei/internal/teacher"
	"github.com/ayflying/game-sensei/internal/vision"
)

// maxFailStreak 是连续执行失败的容忍上限。
//
// 偶发一次失败不该中断整段示范——已经采到的数据是真金白银；
// 但连续失败说明配置或链路坏了（例如档案里没有模型给的那个按钮），
// 继续跑只是在刷屏，早停早排查。
const maxFailStreak = 5

// ---- 卡死自愈（stuck escape）----
//
// 实测背景（2026-09-12 洛克王国：世界 / 小米平板）：老师连续给 move:up_right，
// 角色被水晶树/岩壁挡住原地蹭，60 步里 45 步是同一个方向；而单帧 VLM 看不见
// 「没动」这件事，于是继续给同一个方向，形成无限复读。
//
// 触发后两级交替上：① 档案里的脱困脚本（洛克王国 = 开地图 → 点魔力之源锚点 → 传送）；
// ② 摇杆长推换方向。理由见下面两处 if 的注释。
//
// 阈值是**实测定标**出来的（2026-09-12 同场景四方向对照，见 cal_* 截图）：
//
//	向下推 2500ms → Δ12.29 / 向左 → Δ13.98 / 向右 → Δ12.32   ← 真在走
//	向上推 2500ms → Δ 3.26                                   ← 被坡面挡住
//
// 所以「在走」与「被挡住」的分界落在 3.3~12.3 之间，取 6 足够安全。
// 之前把阈值设成 4 是错的：当时误把「角色原地蹭」当成「缓慢推进」，
// 结论一度是「像素差分辨不了」——标定之后发现分得干干净净。
//
// ⚠️ 第二次修正（2026-09-12 晚，真机采集复盘）：**必须跨窗口比较，不能比相邻两帧**。
// 老师偏爱给 move:xxx/500ms 这种小步，500ms 移动的画面变化只有 Δ1.0~1.8，
// 与「卡住」同级——用单步 Δ 判卡死会把正常走动全判成卡死。
// 改成「拿当前帧和 stuckWindowSteps 步之前的帧比」后两边重新拉开：
// 5 步（约 2.5 秒）正常走动累计位移足够大（Δ>12），卡住则仍在 3 左右。
const (
	// stuckDiffEps 一个卡死窗口内的画面变化量（平均像素差）低于它，
	// 就认为这一窗口没推动世界。
	stuckDiffEps = 6.0
	// stuckWindowSteps 卡死判据的比较窗口（步）。与动作时长无关：
	// 只要窗口里的累计位移够大就是「在走」，够小就是「被钉住」。
	stuckWindowSteps = 5
	// stuckStreakLimit 连续这么多个窗口都没推动世界，判定卡死、开始脱困。
	stuckStreakLimit = 1
	// escapeHoldMs 摇杆兜底脱困的推杆时长：比常规一步更长，确保走出卡点。
	escapeHoldMs = 1800
	// escapeLogTag 日志前缀，便于事后 grep 统计「这场示范卡了几次」。
	escapeLogTag = "🆘"
	// escapeScriptEvery 每这么多次脱困，才动用一次「档案脱困脚本」（地图传送）。
	//
	// 摇杆长推是默认手段，因为**它不依赖任何界面坐标**：换一个方向推 1800ms
	// 就能沿障碍边缘蹭出去。档案脚本要贵得多——3 次点击 + 约 13 秒等待，
	// 而且中间那步「点地图上的魔力之源图标」用的是固定归一化坐标，
	// 一旦地图视野和标定时不一致就会点空，非但没脱困，还把角色留在大地图里
	// （实测 2026-09-12 第四轮：脱困脚本开图后点空，随后 4 帧画面完全静止）。
	// 所以脚本退居二线，只在摇杆反复推不动时才兜底。
	escapeScriptEvery = 4
	// recentActionWindow 回路侧保留的最近动作条数。
	//
	// 比提示词上限（teacher.recentActionPromptMax=8）略大：横跳判据要看到
	// 6 步窗口，留 8 步给提示词用。多留无益，只会让老师分心。
	recentActionWindow = 8
	// moveStallDiffEps 判定「这一步几乎没挪窝」的单步画面变化量阈值。
	//
	// 与 stuckDiffEps（跨窗口，6.0）不同量级：单步 Δ 天生小（老师爱给 500ms 小步，
	// 正常走动也只有 1~2），所以这里只用来识别**持续**的极小 Δ（连续多步都很小）。
	// 取 4.0：正常走动偶有低值但不会连续，撞墙则会连着很多步低于它。
	moveStallDiffEps = 4.0
	// moveStallStreakWarn 连续多少步「同向 + 小 Δ」就认定在撞墙（提示词据此点名换方向）。
	moveStallStreakWarn = 2
)

// escapeDirs 是脱困时依次尝试的方向。
//
// 刻意从「横向/反向」开头：卡死几乎总是因为一路朝同一侧顶，
// 先沿障碍边缘横着挪出去，比继续往前顶有效得多。
// 八个方向轮完一圈还没脱困就从头再来（对应「绕障碍一圈」的直觉）。
var escapeDirs = []agent.Dir{
	agent.DirDown, agent.DirLeft, agent.DirRight,
	agent.DirDownLeft, agent.DirDownRight,
	agent.DirUpLeft, agent.DirUpRight, agent.DirUp,
}

// frameDiff 返回两帧灰度图的逐像素平均绝对差（0~255）。
//
// 实现已收敛到 internal/vision.FrameDiff（hunt/shot/诊断脚本共用同一份），
// 这里只保留一个薄壳，避免回路的调用点全部改名。量纲见该函数的注释。
func frameDiff(a, b *image.Gray) float64 {
	return vision.FrameDiff(a, b)
}

// runTapScript 顺序执行一条「点/拖 + 等待」脚本。
//
// 卡死脱困脚本与技能宏共用同一个执行内核——二者原语完全一样（在某归一化坐标
// 点一下或拖一段，然后等界面变化），只是触发来源与日志前缀不同。每一步都已是
// L2 的 Tap/Swipe，Apply 会幂等透传，不会再被当成命名按钮去查坐标。
//
// 返回 aborted=true 表示收到退出信号（调用方应立即收尾）；ok=false 表示某步执行失败。
func runTapScript(be backend, prefix string, steps []game.MacroStep, defWait time.Duration,
	stop <-chan os.Signal, tag string, logHook func(string)) (aborted, ok bool) {
	for i, st := range steps {
		wait := time.Duration(st.WaitMs) * time.Millisecond
		if wait <= 0 {
			wait = defWait
		}
		stepAct := agent.Action{Kind: agent.ActionTap, Nx: st.Pos[0], Ny: st.Pos[1]}
		desc := fmt.Sprintf("点 %.3f,%.3f", st.Pos[0], st.Pos[1])
		if st.To[0] != 0 || st.To[1] != 0 {
			dragMs := st.DragMs
			if dragMs <= 0 {
				dragMs = 600
			}
			stepAct = agent.Action{
				Kind: agent.ActionSwipe,
				Nx:   st.Pos[0], Ny: st.Pos[1],
				Nx2: st.To[0], Ny2: st.To[1],
				Dur: time.Duration(dragMs) * time.Millisecond,
			}
			desc = fmt.Sprintf("拖 %.3f,%.3f→%.3f,%.3f/%dms", st.Pos[0], st.Pos[1], st.To[0], st.To[1], dragMs)
		}
		if err := be.Apply(stepAct); err != nil {
			fmt.Printf("[%s] %s 第 %d 步执行失败: %v\n", tag, prefix, i+1, err)
			logHook(fmt.Sprintf("%s 第 %d 步失败: %v", prefix, i+1, err))
			return false, false
		}
		tail := ""
		if st.Note != "" {
			tail = "（" + st.Note + "）"
		}
		fmt.Printf("[%s] %s %d/%d %s%s\n", tag, prefix, i+1, len(steps), desc, tail)
		select {
		case <-stop:
			return true, true
		case <-time.After(wait):
		}
	}
	return false, true
}

// escapeToScript 把脱困步骤转成统一脚本步骤（两种步骤字段同形）。
func escapeToScript(in []game.EscapeStep) []game.MacroStep {
	out := make([]game.MacroStep, 0, len(in))
	for _, s := range in {
		out = append(out, game.MacroStep{
			Pos: s.Pos, To: s.To, DragMs: s.DragMs, WaitMs: s.WaitMs, Note: s.Note,
		})
	}
	return out
}

// maybeRunMacro 判断动作是否为 PRESS 宏；是则展开执行其整条步骤序列。
//
// 返回 handled=true 表示它确实是宏（此时 aborted/ok 有意义）；handled=false
// 表示它是普通按钮/其它动作，调用方按原路径 Resolve+Apply。宏整段只对应老师的
// 一次决策，因此只落一条示范样本，步骤由这里确定性跑完。
func maybeRunMacro(be backend, prof *game.Profile, act agent.Action, defWait time.Duration,
	stop <-chan os.Signal, tag string, logHook func(string)) (handled, aborted, ok bool) {
	if act.Kind != agent.ActionPress {
		return false, false, true
	}
	m, isMacro := prof.Macro(act.Name)
	if !isMacro {
		return false, false, true
	}
	aborted, ranOK := runTapScript(be, "宏 "+m.Name, m.Steps, defWait, stop, tag, logHook)
	return true, aborted, ranOK
}

// blackFrameMean 是「这一帧基本全黑」的灰度均值上限（0~255）。
//
// 实测（2026-09-12 真机排查「点了没反应」）：小米平板 screen_off_timeout=60s 且
// stay_on_while_plugged_in=0，任何超过一分钟的停顿都会息屏。息屏后 screencap 只能
// 拿到全黑图，而且此时 input tap/swipe 会被系统静默吞掉。黑帧不拦住会有三重危害：
//  1. 白送老师一次推理（它看的是纯黑图，只会瞎给动作），并污染示范数据；
//  2. 卡死判据看的是跨窗口画面差，「黑→黑」恒为 0，会被判成「被钉住」，
//     于是触发一串毫无意义的脱困（传送/摇杆长推）——全都在黑屏上打水漂；
//  3. 日志里看不出异常，现象只是「执行成功但世界没变」。
//
// 全黑帧的均值约 2~8；正常大世界/战斗画面在 60 以上，取 12 留足余量。
const blackFrameMean = 12.0

// imageMeanGray 返回一张灰度图的平均亮度（0~255）。nil 或空图返回 255（视为正常，
// 宁可漏判一次黑屏，也不要在无帧时误判息屏而打乱主循环）。
func imageMeanGray(g *image.Gray) float64 {
	if g == nil {
		return 255
	}
	b := g.Bounds()
	if b.Dx() == 0 || b.Dy() == 0 {
		return 255
	}
	var sum uint64
	for y := b.Min.Y; y < b.Max.Y; y++ {
		row := y * g.Stride
		for x := b.Min.X; x < b.Max.X; x++ {
			sum += uint64(g.Pix[row+x])
		}
	}
	return float64(sum) / float64(b.Dx()*b.Dy())
}

// appendRecent 把 v 追加到历史尾部，并只保留最后 capN 条。
//
// 「只保留最后 N 条」而不是无限累积：卡死/脱困几轮之后，老师真正需要的
// 只是最近几步的上下文；无限累积既浪费 token，也会让早期无关动作干扰判断。
//
// 结果存在新底层数组里，不与入参共享——调用方常把这个切片直接交给老师，
// 而后续的 append 可能原地改写底层数组，共享会导致「提示词里的历史被追改」
// 这种极难排查的串味 bug。
func appendRecent(history []string, v string, capN int) []string {
	if capN <= 0 {
		return []string{v}
	}
	start := 0
	if len(history) >= capN {
		start = len(history) - capN + 1
	}
	out := make([]string, 0, capN)
	out = append(out, history[start:]...)
	out = append(out, v)
	return out
}

// moveDirOf 从动作串里抽出移动方向，供复读兜底避免换到同一个方向。
//
// 动作串形如 "move:up_right/1500ms"（agent.Action.String()）；非移动动作返回 ""。
// 只做字符串解析而不回传 Action：兜底分支手上只有动作串（prevAction），
// 为了拿方向去反序列化整条动作没必要，也容易与 String() 的格式漂移耦合。
func moveDirOf(action string) string {
	const p = "move:"
	if !strings.HasPrefix(action, p) {
		return ""
	}
	rest := action[len(p):]
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[:i]
	}
	return rest
}

// moveStallStep 推进「移动停滞」计数，返回 (新的连续步数, 新的方向)。
//
// 抽成纯函数是为了单测能钉死语义——这段判据出错的代价很高：错一格就会让
// 「顶着墙走」在日志里静默（pet_run12 43 步死磕同一方向而无人察觉）。
// 三条同时成立才累加：上一步是移动、本步画面变化量小、且方向与上次相同；
// 换方向视为「重新开始」，非移动动作直接清零。
func moveStallStep(prevKind agent.ActionKind, diff float64, prevDir string, streak int, dir string) (int, string) {
	if prevKind != agent.ActionMove || prevDir == "" || diff >= moveStallDiffEps {
		return 0, ""
	}
	if prevDir == dir {
		return streak + 1, dir
	}
	return 1, prevDir // 首次出现，或老师换向后重新计数
}

// battleHoldFrames 是「战斗态判定」的滞回帧数：连续这么多帧都没检出战斗按钮排，
// 才真正切回世界态。进入战斗态仍按单帧即时判（宁可早一点按战斗收窄动作空间）。
//
// 为什么必须滞回：IsBattle 是逐帧像素判据，而战斗里的若干子界面会临时把
// 底部那排圆钮遮掉或换形——2026-09-13 pet_run15 实测最典型的是
// **投球瞄准态**（点 battle_catch 后进入，界面收起五钮、右下换成世界三钮，
// 但敌方精灵仍在场、血条不变，战斗并没结束）。
//
// 后果不是「少认了一次战斗」这么轻：state 每翻转一次，老师的**整套动作空间**
// 都会换掉（战斗态隐藏 MOVE、只列战斗按钮与技能宏；世界态反之）。实测同一批帧
// 上翻转 28 次/92 帧，老师只能跟着在「推摇杆」和「点战斗按钮」之间横跳，
// 而两步都注定无效——这是「战斗段动作横跳」的**上游成因**，比提示词措辞更根本。
//
// 取 3 而不是 2：瞄准态在采集节奏（约 1 步/1.2~2s）下通常持续 1~2 帧，
// 3 帧能覆盖它，又不至于把「战斗真的结束」拖得太久（世界态下多按 2 步战斗按钮，
// 代价是白耗 2 步，可接受）。
const battleHoldFrames = 3

// battleStateStep 是战斗态判定的滞回状态机。
//
// 抽成纯函数是为了单测能钉住「什么时候允许切回世界态」——这段判据写错会让
// 动作空间在错误的状态下收窄，而那种错误在日志里只表现为「老师乱点」，很难归因。
//
// prev 是上一步的判定；detected 是本帧的逐帧像素判据；streak 是连续未检出帧数。
// 返回本步判定与新的 streak。
func battleStateStep(prev, detected bool, streak int) (bool, int) {
	if detected {
		return true, 0 // 检出即战斗，并清空未检出计数
	}
	streak++
	if prev && streak < battleHoldFrames {
		return true, streak // 滞回：再等几帧，别急着切回世界
	}
	return false, streak
}

// runDemo 是「老师在线示范」回路（Phase 2 的第一块）。
//
// 与 Phase 1 的 teachLoop 有本质区别：
//   - teachLoop 在**旁路**观察学生，只产出评语，不影响游戏；
//   - runDemo 直接**驱动**游戏，每一步都是老师看着画面给出的动作，
//     同时把这些 (学生观测, 老师动作) 对存成示范数据集，供后续蒸馏学生。
//
// 节拍由老师推理速度决定（实测 qwen3.5:9b 关思考后约 1.2s/步），
// 动作之间再留 DemoWait 让游戏把状态变完——否则下一帧拍的还是旧画面，
// 老师会基于「没变的画面」重复下同一个动作。
func runDemo(cfg config.Config, be backend, dem *teacher.Demonstrator, prof *game.Profile, stop <-chan os.Signal,
	logHook func(string)) error {
	sw, sh, err := be.Size()
	if err != nil {
		return fmt.Errorf("获取屏幕尺寸失败: %w", err)
	}

	dir := cfg.DemoOut
	if dir == "" {
		dir = filepath.Join(".workbuddy", "demos", time.Now().Format("20060102-150405"))
	}
	w, err := dataset.NewWriter(dir, dataset.Meta{
		Goal:      cfg.Goal,
		Model:     cfg.TeacherModel,
		Backend:   be.Describe(),
		ScreenW:   sw,
		ScreenH:   sh,
		DownWidth: cfg.DownsampleWidth,
		DemoWidth: cfg.DemoWidth,
		Live:      be.Live(),
	})
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = w.Close()
		}
	}()

	steps := cfg.DemoSteps
	if steps > 0 {
		fmt.Printf("示范数据: %s | 计划 %d 步 | 每步等待 %v\n", w.Dir(), steps, cfg.DemoWait)
	} else {
		fmt.Printf("示范数据: %s | 不限步数（Ctrl+C 退出）| 每步等待 %v\n", w.Dir(), cfg.DemoWait)
	}
	if cfg.DemoColor {
		fmt.Println("同时保存老师彩色帧到 color/（用于人工复核）")
	}
	fmt.Println()

	var parsedOK, applied, failStreak int
	var prevAction string
	var prevRepeat int // prevAction 已连续执行的次数（喂给老师做重复禁令）
	// prevKind 是 prevAction 的动作种类。重复禁令按它分流：移动不受禁令
	// （见 repeatForceSwitchMove 与 Demonstrator.PrevKind 的注释）。
	var prevKind agent.ActionKind
	// recentActions 是最近已执行的动作串（由早到晚，最多 recentActionWindow 条）。
	// 单给老师「上一步」时它察觉不到自己在 A/B 之间横跳——两步之间看不出重复。
	recentActions := make([]string, 0, recentActionWindow)
	// oscActive 记录「老师当前是否处于横跳状态」，用于把横跳日志压成一次。
	var oscActive bool
	// 画面变化量有两个口径：
	//   diff  —— 相邻两步，只用于日志（老师爱给 500ms 小步，这个值天生很小）
	//   windowDiff —— 当前帧 vs stuckWindowSteps 步前的锚点帧，卡死判据看它
	var prevGray *image.Gray   // 上一步的学生观测
	var anchorGray *image.Gray // 卡死窗口的锚点帧
	var anchorStep int         // 锚点帧所在的步号
	var windowDiff float64     // 一个窗口内的画面变化量（卡死判据用）
	var stuckStreak int        // 连续多少个窗口没推动世界
	// moveStallStreak 是「连续朝同一方向移动、但单步画面变化量都很小」的步数。
	//
	// 与 stuckStreak 的分工：stuckStreak 看**跨窗口**变化量（判「整段时间世界没动」），
	// moveStallStreak 看**相邻两步**变化量（判「这一步几乎没挪窝」）。前者漏掉的正是
	// 「顶着墙持续走」——角色/粒子动画仍在变，窗口Δ压不到阈值以下，于是脱困永不触发。
	// 2026-09-13 pet_run12：老师 43 步 move:up_right/1500ms，单步 Δ 多次 1~3，
	// 而窗口Δ只在 step21/56 两次跌破 6.0。把这个信号喂给老师才是解法。
	var moveStallStreak int
	// moveStallDir 是上一个「小 Δ 移动」的方向。换方向即清零 streak——
	// 否则老师刚换了向、Δ 仍小，提示词会继续说「禁止再沿原方向走」，与它刚做的事矛盾。
	var moveStallDir string
	var escapeIdx int     // 摇杆兜底脱困的方向轮换游标
	var escapeAttempt int // 脱困次数：奇数轮跑档案脚本、偶数轮摇杆长推
	var inBattle bool     // 当前帧是否处于回合战斗态（底部 5 圆钮判据）
	// nonBattleStreak 是「连续多少帧没检测到战斗」。只有它攒够 battleHoldFrames
	// 才真正切回世界态——见 battleStateStep 的说明（治状态闪断）。
	var nonBattleStreak int
	// 冷却是「两次脱困之间至少隔这么多步」。初值取 -cooldown 而不是 0：
	// 否则开局前 cooldown 步里 1-lastEscapeStep < cooldown，明明卡住也不脱困
	// （实测：老师连发 15 步 up_right、Δ 全在 1~2，脱困却一次没触发）。
	lastEscapeStep := -prof.EscapeCooldown()
	for step := 1; ; step++ {
		select {
		case <-stop:
			fmt.Println("\n收到退出信号，正在收尾…")
			return finishDemo(w, &closed, parsedOK, applied)
		default:
		}
		if steps > 0 && step > steps {
			return finishDemo(w, &closed, parsedOK, applied)
		}

		// 1) 感知：灰度图是「学生观测」，彩色图是「老师视野」。
		//    Android 后端下两次调用共用同一张截图缓存，不会多截一次。
		//    GDI 抓屏偶发失败（前台切换/锁屏过渡会让 DC 短暂失效），
		//    重试 2 次、间隔 500ms——一次抖动不该让整段示范报废。
		var gray *image.Gray
		for attempt := 0; ; attempt++ {
			gray, err = be.Grab(cfg.DownsampleWidth)
			if err == nil {
				break
			}
			if attempt >= 2 {
				return fmt.Errorf("第 %d 步抓取灰度帧失败（已重试 %d 次）: %w", step, attempt, err)
			}
			fmt.Printf("[%s] ⚠️  抓屏失败将重试: %v\n", progress(step, steps), err)
			select {
			case <-stop:
				return finishDemo(w, &closed, parsedOK, applied)
			case <-time.After(500 * time.Millisecond):
			}
		}
		// 息屏保护：见 blackFrameMean 的注释。黑帧既送不出有效动作，又会把卡死
		// 判据喂成「跨窗口零变化」。所以先尝试唤醒；唤醒不了就跳过本步（不落样）。
		if imageMeanGray(gray) < blackFrameMean {
			fmt.Printf("[%s] 💤 画面几乎全黑（疑息屏/锁屏），发一次唤醒键后重取…\n", progress(step, steps))
			logHook("⚠️ 画面全黑，疑息屏；已尝试唤醒")
			if err := be.Apply(agent.Action{Kind: agent.ActionKey, Code: "wakeup"}); err != nil {
				fmt.Printf("[%s] 💤 唤醒键发送失败: %v\n", progress(step, steps), err)
			}
			select {
			case <-stop:
				return finishDemo(w, &closed, parsedOK, applied)
			case <-time.After(2 * time.Second):
			}
			if g2, err2 := be.Grab(cfg.DownsampleWidth); err2 == nil {
				gray = g2
			}
			if imageMeanGray(gray) < blackFrameMean {
				fmt.Printf("[%s] 💤 仍为黑屏（很可能已锁屏，需人工解锁）→ 跳过本步，不落样\n",
					progress(step, steps))
				logHook("⚠️ 仍黑屏，跳过本步（可能已锁屏）")
				// 黑帧不参与卡死判据与相邻差：重置锚点，避免「黑→黑零变化」被判卡死。
				prevGray, anchorGray, anchorStep = nil, nil, step
				stuckStreak = 0
				select {
				case <-stop:
					return finishDemo(w, &closed, parsedOK, applied)
				case <-time.After(cfg.DemoWait):
				}
				continue
			}
			fmt.Printf("[%s] 💤 唤醒成功，继续本步\n", progress(step, steps))
			logHook("💤 屏幕已唤醒，继续")
		}
		// 画面变化量：diff 是相邻两步（老师爱给 500ms 小步，这个值天生很小，
		// 只看它会把正常走动误判成卡死），windowDiff 才是卡死判据。
		// 两个口径的定标依据见文件头 stuckWindowSteps 的注释。
		diff := frameDiff(prevGray, gray)
		prevGray = gray
		// moveStallStreak：只在「上一步是移动」「这一步的画面变化量很小」「方向没变」
		// 三条同时成立时累加。判据必须用**本步算出的 diff**——diff = frameDiff(gray_{k-1}, gray_k)
		// 正是「上一步动作造成的画面变化量」，与 PrevKind（上一步动作种类）严格对齐。
		// ⚠️ 别用上一轮存的 Δ：那描述的是上上一步，会和 PrevKind 错开一格（实测过）。
		moveStallStreak, moveStallDir = moveStallStep(prevKind, diff, moveDirOf(prevAction), moveStallStreak, moveStallDir)
		// 撞墙告警：够步数就点名一次，把「Stop walking into the wall」写进日志
		// （提示词里也会带 MoveStallStreak，模型看得到）。
		if moveStallStreak == moveStallStreakWarn {
			msg := fmt.Sprintf("检测到疑似撞墙：连续 %d 步朝 %s 移动但单步 Δ<%.1f（本步 Δ%.1f）→ 提示词要求换方向",
				moveStallStreak, moveStallDir, moveStallDiffEps, diff)
			fmt.Printf("[%s] 🧱 %s\n", progress(step, steps), msg)
			logHook(msg)
		}
		if anchorGray == nil {
			anchorGray, anchorStep = gray, step
		} else if step-anchorStep >= stuckWindowSteps {
			windowDiff = frameDiff(anchorGray, gray)
			if windowDiff < stuckDiffEps {
				stuckStreak++
			} else {
				stuckStreak = 0
			}
			anchorGray, anchorStep = gray, step
		}
		color, err := be.GrabColor()
		if err != nil {
			return fmt.Errorf("第 %d 步抓取彩色帧失败: %w", step, err)
		}
		// 界面态判定（只用灰度/像素，不依赖模型）：战斗态底部有 5 个奶油色圆钮。
		// 后续三处按态保护都读它：卡死不推摇杆、兜底不跨态轮播、提示词隐藏 MOVE。
		wasBattle := inBattle
		if prof.CanDetectBattle() {
			inBattle, nonBattleStreak = battleStateStep(
				inBattle, prof.IsBattle(color), nonBattleStreak)
			if inBattle != wasBattle {
				msg := "进入回合战斗态（底部检测到战斗按钮排）"
				if !inBattle {
					msg = "回到大世界探索态（战斗按钮排消失）"
				}
				fmt.Printf("[%s] ⚔️  %s\n", progress(step, steps), msg)
				logHook(msg)
			}
		} else {
			inBattle, nonBattleStreak = false, 0
		}
		uiState := ""
		if inBattle {
			uiState = game.StateBattle
		}
		dem.UIState = uiState
		// 2) 降采样 + JPEG 编码后送审。JPEG 而非 PNG：1024 宽下 130KB vs 1.5MB。
		small := vision.Downscale(color, cfg.DemoWidth)
		jpg, err := vision.EncodeJPEG(small, 88)
		if err != nil {
			return fmt.Errorf("第 %d 步编码送审帧失败: %w", step, err)
		}
		grayPNG := encodeGrayPNG(gray)

		// 3a) 卡死自愈：连续 stuckStreakLimit 个窗口「画面变化量低于阈值」，
		//     说明老师给的动作压根没推动世界（角色被地形/空气墙钉住，或战斗里复读）。
		//     此时问老师是白问——它看到的是同一张画面，只会再给一次同动作。直接接管。
		//
		//     战斗态与大世界走两套手段（战斗里没有摇杆，推 MOVE 是纯空操作，实测会
		//     在战斗界面空蹭）：
		//       战斗 → 点 battle_flee 脱离（打不过/卡住就跑，回头再来），没有该按钮就 WAIT；
		//       世界 → 两级交替：① 档案传送脚本（每 escapeScriptEvery 次）② 摇杆长推换方向。
		//
		//     冷却（Cooldown）是必须的：脱困本身好几秒，
		//     不加冷却会在脱困失败时每步都来一次，把示范回路刷成脱困演示。
		stuck := stuckStreak >= stuckStreakLimit && step-lastEscapeStep >= prof.EscapeCooldown()

		// —— 战斗态卡死：不推摇杆，优先逃跑脱离 ——
		if stuck && inBattle {
			escapeAttempt++
			n := stuckStreak
			lastEscapeStep = step
			stuckStreak = 0
			if _, hasFlee := prof.Button("battle_flee"); hasFlee {
				msg := fmt.Sprintf("战斗中 %d 个窗口画面没推进（窗口Δ%.1f）→ 点逃跑脱离", n, windowDiff)
				fmt.Printf("[%s] %s %s\n", progress(step, steps), escapeLogTag, msg)
				logHook(msg)
				if err := be.Apply(agent.Action{Kind: agent.ActionPress, Name: "battle_flee"}); err != nil {
					fmt.Printf("[%s] %s 战斗逃跑失败: %v\n", progress(step, steps), escapeLogTag, err)
					logHook(fmt.Sprintf("战斗逃跑失败: %v", err))
				}
			} else {
				msg := fmt.Sprintf("战斗中 %d 个窗口画面没推进且无逃跑钮 → 等待一个回合", n)
				fmt.Printf("[%s] %s %s\n", progress(step, steps), escapeLogTag, msg)
				logHook(msg)
			}
			select {
			case <-stop:
				return finishDemo(w, &closed, parsedOK, applied)
			case <-time.After(cfg.DemoWait):
			}
			// 脱困是「系统动作」而非老师示范，不落样。
			continue
		}

		// 配比见 escapeScriptEvery：默认摇杆长推，每 N 次才跑一次地图脚本。
		// 取模落在 N-1 上，是为了让「第一次卡死」走便宜的摇杆分支。
		if stuck && prof.CanEscape() && escapeAttempt%escapeScriptEvery == escapeScriptEvery-1 {
			escapeAttempt++
			n := stuckStreak
			lastEscapeStep = step
			stuckStreak = 0
			note := prof.Escape.Note
			if note == "" {
				note = "档案 escape 脚本"
			}
			tag := progress(step, steps)
			msg := fmt.Sprintf("%d 个窗口画面没推进（窗口Δ%.1f）→ 脱困①脚本：%s", n, windowDiff, note)
			fmt.Printf("[%s] %s %s\n", tag, escapeLogTag, msg)
			logHook(msg)
			aborted, _ := runTapScript(be, "脱困", escapeToScript(prof.Escape.Steps),
				cfg.DemoWait, stop, tag, func(m string) {
					logHook(m)
				})
			if aborted {
				return finishDemo(w, &closed, parsedOK, applied)
			}
			// 脱困是「系统动作」而非老师示范，不落样——把它当训练样本会教坏学生
			// （观测-动作对里那条动作根本不是老师根据画面做出的）。
			continue
		}

		// 3a-② 脱困的物理兜底（默认手段）：上一个 if 每 escapeScriptEvery 次
		//       才接管一次，其余情况（含档案没配脚本）都在这里换方向长推。
		//       两级交替的意义见上一段注释：脚本依赖地图视野，可能点空；
		//       摇杆长推不依赖任何界面坐标，保证总有一条路能真正挪动角色。
		if stuck {
			escapeAttempt++
			n := stuckStreak
			lastEscapeStep = step
			stuckStreak = 0
			d := escapeDirs[escapeIdx%len(escapeDirs)]
			escapeIdx++
			msg := fmt.Sprintf("%d 个窗口画面没推进（窗口Δ%.1f）→ 脱困②摇杆长推：朝 %s %dms",
				n, windowDiff, d, escapeHoldMs)
			fmt.Printf("[%s] %s %s\n", progress(step, steps), escapeLogTag, msg)
			logHook(msg)
			if err := be.Apply(agent.Action{Kind: agent.ActionMove, Dir: d, Dur: escapeHoldMs * time.Millisecond}); err != nil {
				fmt.Printf("[%s] %s 摇杆脱困失败: %v\n", progress(step, steps), escapeLogTag, err)
				logHook(fmt.Sprintf("摇杆脱困失败: %v", err))
			}
			select {
			case <-stop:
				return finishDemo(w, &closed, parsedOK, applied)
			case <-time.After(cfg.DemoWait):
			}
			continue
		}

		// 3b) 复读兜底：同一动作串连续 repeatForceSwitch 步但画面在变（策略打转）。
		//     从**当前界面态合法**的 PRESS 项（按钮+宏，已剔除 hidden）里轮换一个，
		//     强行制造动作多样性——示范数据的价值在覆盖。战斗态只在战斗项里轮换，
		//     绝不跨态去点坐骑/跳跃（那些钮在战斗界面不存在，点了是空操作甚至误触）。
		//
		// ⚠️ 移动（MOVE）另算阈值：走远本来就要连续走同一方向，3 步就强换成
		// 一个按钮等于把「赶路」打断成「原地乱按」。2026-09-13 实况即因此出现
		// up_right/up_left 交替、40 步净位移≈0。给移动放宽到 repeatForceSwitchMove，
		// 「走了但被挡住」由前面的卡死判据（窗口Δ）负责，不靠复读兜底。
		const repeatForceSwitch = 3
		const repeatForceSwitchMove = 8
		forceLimit := repeatForceSwitch
		if prevKind == agent.ActionMove {
			forceLimit = repeatForceSwitchMove
		}
		var forced *agent.Action
		if prevRepeat >= forceLimit {
			// ⚠️ 移动复读要**换方向**，不能按按钮。
			// 2026-09-13 pet_run12：老师 43 步 move:up_right/1500ms，8 步复读兜底
			// 却去点了 mount/star（世界按钮），既没解开卡点还白送一步。
			// 移动卡住的唯一出路是换一个方向绕行——这与「策略打转要制造动作多样性」
			// 是两种病，用药不同。真·瞬移/穿墙之外的场景，换向永远比按按钮有效。
			if prevKind == agent.ActionMove && prof.CanMove() {
				// ⚠️ 还要看**是不是真没挪窝**：反复同向但画面一直在推进（Δ 不小），
				// 那只是正常赶路，强行拐弯会把一次直行拆成一路乱拐。
				// 2026-09-13 pet_run13：老师连走 17 步 up_right 且 Δ 稳在 13~33，
				// 仍是正常推进，不该被复读计数打断。只在这条 Δ 判据下才换向。
				if diff < moveStallDiffEps {
					d := escapeDirs[escapeIdx%len(escapeDirs)]
					// 避开正在复读的那个方向（比如当前一直 up_right，就先试 down/left）。
					for k := 0; k < len(escapeDirs); k++ {
						if string(d) != moveDirOf(prevAction) {
							break
						}
						escapeIdx++
						d = escapeDirs[escapeIdx%len(escapeDirs)]
					}
					escapeIdx++
					forced = &agent.Action{Kind: agent.ActionMove, Dir: d, Dur: escapeHoldMs * time.Millisecond}
					msg := fmt.Sprintf("老师连续 %d 步重复移动 %s 且画面未推进（Δ%.1f<%.1f）→ 强制换方向：朝 %s 长推 %dms",
						prevRepeat, prevAction, diff, moveStallDiffEps, d, escapeHoldMs)
					fmt.Printf("[%s] %s %s\n", progress(step, steps), escapeLogTag, msg)
					logHook(msg)
				}
				// Δ 不小：交回老师继续（提示词已带 Δ 与移动停滞计数），此处不打断。
			} else {
				candState := game.StateWorld
				if inBattle {
					candState = game.StateBattle
				}
				candidates := prof.PressNamesForState(candState)
				switch {
				case len(candidates) > 0:
					// 轮换选一个，且尽量避开正在复读的那个动作（否则等于没换）。
					pick := candidates[step%len(candidates)]
					if len(candidates) > 1 {
						for k := 1; k <= len(candidates); k++ {
							cand := candidates[(step+k)%len(candidates)]
							if "press:"+cand != prevAction {
								pick = cand
								break
							}
						}
					}
					forced = &agent.Action{Kind: agent.ActionPress, Name: pick}
					fmt.Printf("[%s] ⚠️  老师连续 %d 步重复 %s，本步强制换%s项 %s\n",
						progress(step, steps), prevRepeat, prevAction, candState, forced.String())
					logHook(fmt.Sprintf("强制换动作 %s（老师复读 %d 步）", forced.String(), prevRepeat))
				case inBattle:
					// 战斗态没有可轮换项也没有摇杆：等一个回合，让敌方/动画推进画面。
					forced = &agent.Action{Kind: agent.ActionNone}
					msg := fmt.Sprintf("老师连续 %d 步重复 %s、战斗态无可用替换项 → 等待一个回合",
						prevRepeat, prevAction)
					fmt.Printf("[%s] %s %s\n", progress(step, steps), escapeLogTag, msg)
					logHook(msg)
				case prof.CanMove():
					// 大世界且档案没按钮可换：摇杆长推兜底（换方向本身就是一种脱困）。
					d := escapeDirs[escapeIdx%len(escapeDirs)]
					escapeIdx++
					forced = &agent.Action{Kind: agent.ActionMove, Dir: d, Dur: escapeHoldMs * time.Millisecond}
					msg := fmt.Sprintf("老师连续 %d 步重复 %s、档案又没按钮可换 → 强制朝 %s 长推 %dms",
						prevRepeat, prevAction, d, escapeHoldMs)
					fmt.Printf("[%s] %s %s\n", progress(step, steps), escapeLogTag, msg)
					logHook(msg)
				default:
					forced = &agent.Action{Kind: agent.ActionNone}
				}
			}
		}
		if forced != nil {
			act := *forced
			// 落样里的 PrevAction 记的是「执行本步之前的那一步」，与正常分支语义一致
			// （喂给老师的上一动作），所以要在更新 prevAction 之前先取出来。
			prevActionBefore := prevAction
			// 宏不能 Resolve 成单点：这里判一下，宏走多步展开，其它走普通预检/执行。
			_, isMacro := prof.Macro(act.Name)
			var expanded agent.Action
			var expErr error
			if !isMacro && act.Kind != agent.ActionNone {
				expanded, expErr = be.Resolve(act)
			}
			// case 子句不支持 `:=`（只有 if/switch 头部可以），所以预检/执行
			// 的结果都在 switch 之前算好，case 只做分类判定。
			var applyErr error
			if !isMacro && act.Kind != agent.ActionNone && expErr == nil {
				applyErr = be.Apply(act)
			}
			if act.String() == prevAction {
				prevRepeat++
			} else {
				prevRepeat = 1
			}
			prevAction = act.String()
			prevKind = act.Kind
			recentActions = appendRecent(recentActions, prevAction, recentActionWindow)
			tag := progress(step, steps)
			switch {
			case act.Kind == agent.ActionNone:
				applied++
				line := fmt.Sprintf("[%s] 兜底 %-20s | %s", tag, act.String(), agent.ExplainAction(act))
				fmt.Println(line)
				logHook(line)
			case isMacro:
				if _, aborted, ranOK := maybeRunMacro(be, prof, act, cfg.DemoWait, stop, tag, logHook); aborted {
					return finishDemo(w, &closed, parsedOK, applied)
				} else if ranOK {
					applied++
				}
			case expErr != nil:
				fmt.Printf("[%s] ⚠️  兜底动作无法执行: %v\n", tag, expErr)
			case applyErr != nil:
				fmt.Printf("[%s] ⚠️  兜底动作执行失败: %v\n", tag, applyErr)
			default:
				applied++
				line := fmt.Sprintf("[%s] 兜底 %-20s → %-28s | %s",
					tag, act.String(), expanded.String(), agent.ExplainAction(act))
				fmt.Println(line)
				logHook(line)
			}
			if err := w.Step(act, dataset.StepInfo{Raw: "(forced " + act.String() + ")", PrevAction: prevActionBefore, Parsed: true}, grayPNG, jpg, cfg.DemoColor); err != nil {
				return fmt.Errorf("第 %d 步写示范数据失败: %w", step, err)
			}
			select {
			case <-stop:
				return finishDemo(w, &closed, parsedOK, applied)
			case <-time.After(cfg.DemoWait):
			}
			continue
		}
		dem.PrevAction = prevAction // 把上一步喂回去，否则老师会无限重复同一动作
		// 单单一步看不出「横跳」：模型需要最近几步才能发现自己在原地打转。
		// 复制一份再交给老师——后面 append 会改动底层数组，这里不能共享。
		dem.RecentActions = append([]string(nil), recentActions...)

		if x, y, osc := teacher.DetectOscillation(recentActions); osc {
			// 只在「刚开始打转」时记一次：打转期间几乎每步都命中，
			// 逐步刷屏会把日志里真正有价值的动作行挤没。
			if !oscActive {
				oscActive = true
				msg := fmt.Sprintf("老师开始横跳（%s ↔ %s，净位移≈原地）→ 提示词改策略", x, y)
				fmt.Printf("[%s] 🔁 %s\n", progress(step, steps), msg)
				logHook(msg)
			}
		} else {
			oscActive = false
		}
		dem.PrevKind = prevKind
		dem.RepeatCount = prevRepeat
		// 进度反馈：单帧 VLM 看不出「往前走」和「顶着墙走」的区别，
		// 把回路测得的画面变化量直接告诉它（pet_run12 43 步死磕同一方向的解法）。
		// 用本步的 diff：它正是「上一步动作造成的画面变化量」，与 PrevAction 对齐。
		dem.LastDiff = diff
		dem.MoveStallStreak = moveStallStreak
		act, res, actErr := askTeacher(cfg, dem, jpg)

		info := dataset.StepInfo{Raw: "", LatencyMs: 0, PrevAction: prevAction}
		if res != nil {
			info.Raw = res.Raw
			info.Parsed = res.Used
			if res.Reply != nil {
				info.LatencyMs = res.Reply.Stats.TotalMs
				info.OutTokens = res.Reply.Stats.OutputTokens
			}
		}

		if actErr != nil {
			fmt.Printf("[%s] ⚠️  老师无响应: %v\n", progress(step, steps), actErr)
			logHook(fmt.Sprintf("⚠️ 老师无响应: %v", actErr))
		} else if !res.Used {
			fmt.Printf("[%s] ⚠️  动作解析失败（%.1fs）| 原始输出: %s\n",
				progress(step, steps), info.LatencyMs/1000, oneLine(res.Raw, 80))
			logHook(fmt.Sprintf("⚠️ 动作解析失败: %s", oneLine(res.Raw, 60)))
		} else {
			parsedOK++
			// 4) 展开 + 执行。
			//
			// 普通动作先单独展开一次只为日志：把「朝前走」显示成
			// 「摇杆 0.21,0.69 推向 0.21,0.61」，排查动作对不对时这行信息量最大。
			// （Apply 内部还会再展开一次，展开是纯计算，代价可忽略。）
			//
			// 宏（PRESS cast_xxx 之类）是档案里打包好的多点脚本，Resolve 会拒绝
			// 把它塌成单点，因此这里先判宏：宏交给 maybeRunMacro 确定性跑完，整段
			// 只对应老师的一次决策、只落一条样本。
			mMacro, isMacroName := prof.Macro(act.Name)
			isMacroName = isMacroName && act.Kind == agent.ActionPress
			var expanded agent.Action
			var expErr error
			if !isMacroName {
				expanded, expErr = be.Resolve(act)
			}
			// 展开通过后才真正执行（宏在自己的分支里展开执行）；先把错误算出来，
			// 下面的 switch 只负责分类与日志。
			var applyErr error
			if !isMacroName && expErr == nil {
				applyErr = be.Apply(act)
			}
			// 无论成功与否都告诉老师「你刚给的是这个动作」，
			// 否则失败的动作下一轮还会被重复提出。
			// 连续重复计数：同动作 +1，换动作清零（供下一轮的重复禁令）。
			if act.String() == prevAction {
				prevRepeat++
			} else {
				prevRepeat = 1
			}
			prevAction = act.String()
			prevKind = act.Kind
			recentActions = appendRecent(recentActions, prevAction, recentActionWindow)
			tag := progress(step, steps)

			switch {
			case isMacroName:
				_, aborted, ranOK := maybeRunMacro(be, prof, act, cfg.DemoWait, stop, tag, logHook)
				if aborted {
					return finishDemo(w, &closed, parsedOK, applied)
				}
				if !ranOK {
					failStreak++
					fmt.Printf("[%s] ⚠️  宏 %s 执行失败\n", tag, mMacro.Name)
					logHook(fmt.Sprintf("⚠️ 宏 %s 执行失败", mMacro.Name))
					if failStreak >= maxFailStreak {
						return fmt.Errorf("连续 %d 步宏执行失败，最后宏: %s", failStreak, mMacro.Name)
					}
					break
				}
				failStreak = 0
				applied++
				mark := "执行宏"
				if !be.Live() {
					mark = "dry-run宏"
				}
				line := fmt.Sprintf("[%s] %s %-16s（%d 步）| %s | Δ%.1f | %.1fs %dtok",
					tag, mark, act.String(), len(mMacro.Steps),
					agent.ExplainAction(act), diff, info.LatencyMs/1000, info.OutTokens)
				fmt.Println(line)
				logHook(line)
			case expErr != nil:
				// 展开失败通常是配置问题：档案里没有这个按钮、斜向没配键位。
				failStreak++
				fmt.Printf("[%s] ⚠️  动作无法执行: %v\n", tag, expErr)
				logHook(fmt.Sprintf("⚠️ 动作无法执行: %v", expErr))
				if failStreak >= maxFailStreak {
					return fmt.Errorf("连续 %d 步动作无法执行，最后错误: %w", failStreak, expErr)
				}
			case applyErr != nil:
				failStreak++
				fmt.Printf("[%s] ⚠️  执行失败: %v\n", tag, applyErr)
				logHook(fmt.Sprintf("⚠️ 执行失败: %v", applyErr))
				if failStreak >= maxFailStreak {
					return fmt.Errorf("连续 %d 步执行失败，最后错误: %w", failStreak, applyErr)
				}
			default:
				failStreak = 0
				applied++
				mark := "执行"
				if !be.Live() {
					mark = "dry-run"
				}
				repeat := ""
				if step > 1 && act.String() == info.PrevAction {
					repeat = " ⚠️与上一步相同"
				}
				// L1 → L2 都打出来：左边是模型的意图，右边是最终落到设备上的操作。
				// 末尾 Δ 是相对上一步的画面变化量（诊断用；卡死判据看的是跨窗口的
				// windowDiff，因为老师常给 500ms 小步，单步 Δ 天生就小）。
				line := fmt.Sprintf("[%s] %s %-20s → %-28s | %s | Δ%.1f | %.1fs %dtok%s",
					tag, mark, act.String(), expanded.String(),
					agent.ExplainAction(act), diff, info.LatencyMs/1000, info.OutTokens, repeat)
				fmt.Println(line)
				logHook(line)
			}
		}

		// 5) 落样。解析失败也存：这是衡量老师输出稳定性的原始数据。
		if err := w.Step(act, info, grayPNG, jpg, cfg.DemoColor); err != nil {
			return fmt.Errorf("第 %d 步写示范数据失败: %w", step, err)
		}

		// 6) 等游戏响应。
		select {
		case <-stop:
			return finishDemo(w, &closed, parsedOK, applied)
		case <-time.After(cfg.DemoWait):
		}
	}
}

// askTeacher 包一层超时；保持 runDemo 主循环干净。
func askTeacher(cfg config.Config, dem *teacher.Demonstrator, jpg []byte) (agent.Action, *teacher.DemoResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.EvalTimeout)
	defer cancel()
	res, err := dem.Act(ctx, jpg)
	if err != nil {
		return agent.Action{Kind: agent.ActionNone}, nil, err
	}
	return res.Action, res, nil
}

func finishDemo(w *dataset.Writer, closed *bool, parsedOK, applied int) error {
	if err := w.Close(); err != nil {
		return err
	}
	*closed = true
	fmt.Printf("\n示范结束：解析成功 %d 步，实际执行 %d 步\n", parsedOK, applied)
	fmt.Printf("数据已保存到: %s\n", w.Dir())
	fmt.Printf("  meta.json        会话元信息与统计\n")
	fmt.Printf("  trajectory.jsonl 逐样本动作（trainer/ 的输入）\n")
	fmt.Printf("  frames/          学生观测灰度帧\n")
	return nil
}

// encodeGrayPNG 把学生观测编码为 PNG。灰度 160 宽时只有几十 KB，
// 用 PNG 是为了无损——训练数据不该引入 JPEG 块效应。
func encodeGrayPNG(g *image.Gray) []byte {
	if g == nil {
		return nil
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, g); err != nil {
		return nil
	}
	return buf.Bytes()
}

// progress 生成进度标签：限定步数时显示 "3/20"，不限时只显示 "3"。
func progress(step, total int) string {
	if total > 0 {
		return fmt.Sprintf("%2d/%d", step, total)
	}
	return fmt.Sprintf("%2d", step)
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
