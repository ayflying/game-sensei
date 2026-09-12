// game-sensei —— 泛型游戏辅助：大模型教师 + 量化学生自学习闭环（见 README.md）。
//
// 两条回路：
//   - 实时执行回路（goroutine A）：capture(截屏) -> agent(决策) -> input(键鼠)，
//     要求几十毫秒级延迟，绝不能阻塞；
//   - 异步教学回路（goroutine B，Phase 1）：周期性抽样关键帧交给老师（VLM）评估，
//     打印文字反馈、可选落盘报告。老师在另一台/另一个进程，慢一点无所谓。
//
// 用法：
//
//	helper [-live] [-fps 30] [-down 160] [-frames 0] [-report 30]
//	       [-teacher] [-teacher-url http://127.0.0.1:11435] [-teacher-model qwen3.5:9b]
//	       [-eval-every 300] [-eval-frames 6] [-eval-width 640] [-goal "..."] [-eval-out dir]
//	       [-target pc|android] [-adb path] [-serial sn] [-app 包名] [-launch]
//	       [-game nrc] [-demo] [-demo-steps 20] [-demo-width 1024] [-demo-wait 2s]
//	       [-demo-out dir] [-demo-color] [-demo-hints "a;b"]
//
// 默认 dry-run（不真正发送输入），Ctrl+C 或达到 -frames 后干净退出。
// -target android 时通过 ADB 遥控手机：截屏为感知、触摸 tap/swipe 为行动。
// -game 指定游戏档案（internal/game/profiles/*.json）：它把「朝前走 / 按跳跃」
// 这类**跨游戏通用的语义动作**翻译成本平台的具体操作，因此换游戏只需换档案。
//
// 三种运行形态：
//
//	实时回路（默认）  capture -> 学生 -> input，固定节拍，验证延迟与输入链路；
//	异步教学（-teacher）  旁路抽样关键帧交给老师评估，只出评语，不影响游戏；
//	在线示范（-demo）     老师看着画面直接出动作并驱动游戏，同时把
//	                      (学生观测, 老师动作) 存成示范数据集供后续蒸馏学生。
//
// -demo 由「秒级」节拍驱动（老师单次约 1~2s），与 30FPS 的实时回路互斥。
package main

import (
	"context"
	"flag"
	"fmt"
	"image"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ayflying/game-sensei/internal/agent"
	"github.com/ayflying/game-sensei/internal/config"
	"github.com/ayflying/game-sensei/internal/game"
	"github.com/ayflying/game-sensei/internal/gamewin"
	"github.com/ayflying/game-sensei/internal/memory"
	"github.com/ayflying/game-sensei/internal/overlay"
	"github.com/ayflying/game-sensei/internal/teacher"
)

// 重定向标准流到 f。
//
// 为什么这么简单就行：os.Stdout/os.Stderr 本身就是 *os.File 变量，
// fmt 每次输出都重新读这个变量，直接换掉即可（进程内最小用例已验证）。
// log 包在 init 时把 os.Stderr 的**指针值**存进了默认 logger，
// 所以必须额外 log.SetOutput，否则 Fatal/Print 仍走旧 stderr。
// 运行时 panic 输出写的是原始 fd 2，仍走启动方给的流——日志场景可接受。
//
// （曾试过 SetStdHandle+DuplicateHandle 路线：实机 PowerShell 后台场景
// 报 "The handle is invalid"，且收益只有 panic 也落盘——不值得为它
// 引入 kernel32 绑定，已删。）
func redirectStd(f *os.File) {
	os.Stdout = f
	os.Stderr = f
	log.SetOutput(f)
}

// printProfile 打印游戏档案摘要，让「现在按哪套操作在跑」一目了然。
func printProfile(p *game.Profile) {
	fmt.Printf("游戏档案: %s", p.Name)
	if p.Package != "" {
		fmt.Printf("（%s）", p.Package)
	}
	fmt.Println()

	switch p.Move.Mode {
	case game.MoveJoystick:
		fmt.Printf("  移动: 虚拟摇杆，中心 %.2f,%.2f 幅度 %.2f,%.2f\n",
			p.Move.Center[0], p.Move.Center[1], p.Move.Radius[0], p.Move.Radius[1])
	case game.MoveKeys, game.MoveDpad:
		fmt.Printf("  移动: %s\n", p.Move.Mode)
	default:
		fmt.Println("  移动: 未配置（MOVE 动作会报错）")
	}
	if names := p.ButtonNames(); len(names) > 0 {
		fmt.Printf("  按钮: %s\n", strings.Join(names, "、"))
	}
	// 宏与界面态判据单列一行：运行前一眼能确认「档案里的技能宏到底有没有加载进来」，
	// 以及回路能不能自动区分大世界/回合战斗——这两件事没生效的话，
	// 现象只会是「老师反复点同一个钮」，很难从日志反推。
	if len(p.Macros) > 0 {
		names := make([]string, 0, len(p.Macros))
		for _, m := range p.Macros {
			names = append(names, m.Name)
		}
		fmt.Printf("  宏: %s（各含 %s）\n", strings.Join(names, "、"), macroStepsSummary(p))
	}
	if p.CanDetectBattle() {
		fmt.Println("  界面态: 可自动判定（大世界 / 回合战斗）")
	}
	fmt.Printf("  界面先验: %d 条\n", len(p.Hints))
}

// macroStepsSummary 汇总宏的步数区间，形如「2~2 步」。
func macroStepsSummary(p *game.Profile) string {
	lo, hi := 0, 0
	for _, m := range p.Macros {
		n := len(m.Steps)
		if lo == 0 || n < lo {
			lo = n
		}
		if n > hi {
			hi = n
		}
	}
	if lo == 0 && hi == 0 {
		return "0 步"
	}
	if lo == hi {
		return fmt.Sprintf("%d 步", hi)
	}
	return fmt.Sprintf("%d~%d 步", lo, hi)
}

func main() {
	cfg := config.Default()
	flag.IntVar(&cfg.TargetFPS, "fps", cfg.TargetFPS, "目标帧率")
	flag.IntVar(&cfg.DownsampleWidth, "down", cfg.DownsampleWidth, "感知输入降采样宽度（像素，0=不降采样）")
	flag.BoolVar(&cfg.Live, "live", cfg.Live, "真实发送键鼠输入（默认 dry-run）")
	flag.IntVar(&cfg.MaxFrames, "frames", cfg.MaxFrames, "运行帧数上限（0=直到 Ctrl+C）")
	flag.IntVar(&cfg.ReportEvery, "report", cfg.ReportEvery, "每 N 帧打印一次统计")

	flag.BoolVar(&cfg.TeacherEnabled, "teacher", cfg.TeacherEnabled, "启用老师（异步教学回路）")
	teacherURLFlag := flag.String("teacher-url", cfg.TeacherURL,
		"老师服务地址（Ollama）。默认取环境变量 "+config.EnvTeacherURL+"，未设置则为本机 11435")
	flag.StringVar(&cfg.TeacherModel, "teacher-model", cfg.TeacherModel, "老师模型名")
	flag.IntVar(&cfg.EvalEvery, "eval-every", cfg.EvalEvery, "每 N 帧抽一帧送审")
	flag.IntVar(&cfg.EvalFrames, "eval-frames", cfg.EvalFrames, "一次评估覆盖的关键帧数")
	flag.IntVar(&cfg.EvalWidth, "eval-width", cfg.EvalWidth, "送审帧降采样宽度（像素）")
	flag.DurationVar(&cfg.EvalTimeout, "eval-timeout", cfg.EvalTimeout, "单次送审超时")
	flag.StringVar(&cfg.Goal, "goal", cfg.Goal, "游戏目标描述（写入老师提示词）")
	flag.StringVar(&cfg.EvalOutDir, "eval-out", cfg.EvalOutDir, "评估报告落盘目录（空=仅控制台）")

	// ---- Phase 2：老师在线示范 ----
	flag.BoolVar(&cfg.Demo, "demo", cfg.Demo, "启用老师在线示范：老师看着画面出动作，并采集示范数据")
	studentWeights := flag.String("student", "", "学生权重文件路径（trainer/train.py 产物）。指定后实时回路由学生驱动而非规则 stub")
	flag.IntVar(&cfg.DemoSteps, "demo-steps", cfg.DemoSteps, "示范步数上限（0=直到 Ctrl+C）")
	flag.IntVar(&cfg.DemoWidth, "demo-width", cfg.DemoWidth, "送审老师的彩色帧降采样宽度（像素）")
	flag.DurationVar(&cfg.DemoWait, "demo-wait", cfg.DemoWait, "每步动作后等待游戏响应的时间")
	flag.StringVar(&cfg.DemoOut, "demo-out", cfg.DemoOut, "示范数据落盘目录（空=.workbuddy/demos/<时间戳>）")
	flag.BoolVar(&cfg.DemoColor, "demo-color", cfg.DemoColor, "同时保存老师看到的彩色帧，便于人工复核")
	demoHints := flag.String("demo-hints", "", "额外的界面先验，多条用 ; 分隔（如摇杆中心坐标）")

	flag.StringVar(&cfg.Target, "target", cfg.Target, "控制目标：pc（本机键鼠）| android（ADB 遥控手机）")
	flag.StringVar(&cfg.ADBPath, "adb", cfg.ADBPath, "adb 可执行文件路径（空=自动查找）")
	flag.StringVar(&cfg.Serial, "serial", cfg.Serial, "ADB 设备序列号（空=取唯一在线设备）")
	flag.StringVar(&cfg.AppPackage, "app", cfg.AppPackage, "android 模式要操作的应用包名")
	launchApp := flag.Bool("launch", false, "android 模式下先启动 -app 指定的应用")
	flag.StringVar(&cfg.Game, "game", cfg.Game,
		"游戏档案：档案名（如 nrc、pc_generic）或 JSON 路径；空=不带游戏知识")
	// ---- PC 实测体验：日志浮窗 + 退出最小化游戏 ----
	// ⚠️ 浮窗默认关闭（2026-09-12 实测）：浮窗开启时第一次「Hide→BitBlt→Show」
	// 会整进程挂死（探针 overlayprobe 复现，卡在 GDI/ShowWindow 交互，
	// 具体是 user32 内部锁还是 DIB 冲突未深究）。demo/教学模式必须关。
	// 需要 watch 日志就用 stdout 重定向：... > run.log 2>&1 后 tail -f。
	overlayOn := flag.Bool("overlay", false, "PC 模式显示右下角日志浮窗（⚠️ 实测与 GDI 抓屏互斥会挂死，默认关闭）")
	minimizeOnExit := flag.Bool("minimize-on-exit", true, "运行结束时把标题含游戏名的窗口最小化，方便看终端输出")
	focusOnStart := flag.Bool("focus-on-start", true, "PC 模式启动时把标题含游戏名的窗口还原并切到前台（否则抓屏/按键会落到别的窗口）")
	logFile := flag.String("log", "", "把全部 stdout 同步落盘到该文件（UTF-8）。强烈建议 android 模式使用：\n"+
		"PowerShell 重定向会经 GBK 中间层毁掉中文、吞掉换行，统计工具直接失效")
	flag.Parse()

	// 用户未显式指定帧率时，按平台给不同默认值：ADB 截图单帧约 200~400ms，
	// 30FPS 只会让 ticker 空转刷屏，降到 5FPS 更贴近实际吞吐。
	fpsSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "fps" {
			fpsSet = true
		}
	})
	if !fpsSet && cfg.Target == "android" {
		cfg.TargetFPS = 5
	}
	// -teacher-url 优先级：显式传参 > 环境变量 > 内置默认。
	// flag 的默认值已在 parse 前从环境变量取好（config.DefaultTeacherURL），
	// parse 后把结果写回；只有用户真的传了 flag 才覆盖——否则保持环境变量值。
	teacherURLSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "teacher-url" {
			teacherURLSet = true
		}
	})
	if teacherURLSet {
		cfg.TeacherURL = *teacherURLFlag
	}

	if cfg.TargetFPS <= 0 {
		cfg.TargetFPS = 30
	}
	if cfg.ReportEvery <= 0 {
		cfg.ReportEvery = 30
	}
	// -demo 蕴含 -teacher：示范本身就是老师在工作，分开设只会让人漏配。
	if cfg.Demo {
		cfg.TeacherEnabled = true
	}
	tickInterval := time.Second / time.Duration(cfg.TargetFPS)

	mode := "dry-run（不发送真实输入）"
	if cfg.Live {
		mode = "LIVE（真实输入）"
	}
	// 自落盘日志（-log）：进程自己以 UTF-8 写文件，不经任何 shell 编码层。
	// 动机：Windows PowerShell 5.1 重定向原生命令输出时会用本地代码页（GBK）
	// 解码 UTF-8 再落盘，中文毁成替换字符不说，还会吞掉部分换行——
	// pet_run9 的日志 40 步只剩 27 个行首标记，统计工具全部失真。
	//
	// 实现取舍：os.Stdout 是 *os.File 具体类型，无法原地挂第二个写出端，
	// 包级变量替换也过不了类型检查。所以 -log 模式下把 stdout 的**文件描述符**
	// 直接 dup2 到日志文件：fmt/log 的所有输出字节级落盘，零中间层。
	// 代价是终端不再回显——但实况采集本来就走后台 + 落盘，回显无人消费。
	// 若要同时看终端，用 `tail -f` 或等 runstats 出结果。
	if *logFile != "" {
		f, err := os.OpenFile(*logFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			log.Fatalf("打不开日志文件 %s: %v", *logFile, err)
		}
		redirectStd(f)
		// f 的生命周期与进程一致：不 Close，靠退出时 Sync 保平安。
		defer func() {
			fmt.Println() // 收尾行与前面日志保持同流
			fmt.Println("日志已保存:", *logFile)
			_ = f.Sync()
		}()
	}
	fmt.Printf("game-sensei | 模式=%s | 目标=%dFPS | 降采样宽=%dpx\n", mode, cfg.TargetFPS, cfg.DownsampleWidth)

	// 日志浮窗：右下角置顶小窗，人眼可见、截屏拍不到（WDA_EXCLUDEFROMCAPTURE），
	// 游戏全屏时也能看到 agent 在干什么，且不污染老师/学生的感知画面。
	var ov *overlay.Window
	if *overlayOn && cfg.Target != "android" {
		o, err := overlay.Start(overlay.Options{MaxLines: 6, WidthPx: 620, FontSize: 15})
		if err != nil {
			fmt.Printf("⚠️  日志浮窗启动失败（不影响主流程）: %v\n", err)
		} else {
			ov = o
			defer o.Close()
			o.Push("game-sensei 日志浮窗已启动")
			fmt.Println("日志浮窗: 屏幕右下角（对截屏不可见）")
		}
	}
	// logHook：把 fmt.Printf 的关键输出同步喂给浮窗。
	// 不劫持全局 stdout——实时回路的每一帧统计太吵，浮窗只收大事。
	logHook := func(line string) {
		if ov != nil {
			ov.Push(time.Now().Format("15:04:05 ") + line)
		}
	}

	// 游戏档案：把「语义动作」（朝前走 / 按跳跃）翻译成「这台设备上的具体操作」
	// （把虚拟摇杆推到哪里 / 按哪个键）。换游戏只需换一份档案。
	var prof *game.Profile
	if cfg.Game != "" {
		p, err := game.Load(cfg.Game)
		if err != nil {
			log.Fatalf("加载游戏档案失败: %v", err)
		}
		prof = p
		printProfile(p)
		// 档案带了包名而用户没显式指定 -app 时直接采用：少一处要记的配置
		if cfg.AppPackage == "" {
			cfg.AppPackage = p.Package
		}
	} else {
		fmt.Println("未指定游戏档案（-game）：只能做 TAP/SWIPE/HOLD/KEY/WAIT，")
		fmt.Printf("  MOVE/PRESS 会明确报错。内置档案：%s\n", strings.Join(game.Names(), "、"))
	}

	be, err := openBackend(cfg, prof, *launchApp)
	if err != nil {
		log.Fatalf("后端初始化失败: %v", err)
	}
	// 抓屏时隐藏浮窗：WDA_EXCLUDEFROMCAPTURE 对 GDI BitBlt 是「变黑」不是「消失」，
	// 不隐藏的话老师/学生的每帧画面里都有一块黑矩形（感知污染）。
	if pcb, ok := be.(*pcBackend); ok && ov != nil {
		pcb.beforeShot = ov.Hide
		pcb.afterShot = ov.Show
	}
	// 退出时释放后端资源：PC 端会把仍按住的键抬起来，
	// 否则「按住移动」的最后一步会让键盘卡在按下状态。
	defer func() { _ = be.Close() }()
	// 运行结束把游戏窗口最小化：让用户立刻看到终端里的对话/评估输出。
	defer func() {
		if !*minimizeOnExit {
			return
		}
		kw := gameKeyword(cfg, prof)
		if kw == "" {
			return
		}
		if n, err := gamewin.MinimizeByTitle(kw); err == nil && n > 0 {
			logHook(fmt.Sprintf("已最小化 %d 个游戏窗口（关键词 %q）", n, kw))
		}
	}()
	fmt.Printf("控制目标: %s\n", be.Describe())

	// PC 模式：游戏窗口必须在**前台且非最小化**，抓屏才拿得到画面、
	// 按键才会进到游戏里（后台窗口收不到键盘焦点）。跑过一次退出会
	// 把游戏最小化，下次启动如果不还原，Agent 就在对着桌面盲操作。
	if *focusOnStart && cfg.Target != "android" {
		if kw := gameKeyword(cfg, prof); kw != "" {
			gamewin.EnsureDPIAware()
			// 锁屏/系统 UI 在前台时置顶必败、点击会落到锁屏上，
			// 还会引发 GDI 会话失效（BitBlt 连续 handle invalid）。
			// 直接拒绝启动，别让 agent 对着锁屏空跑。
			if gamewin.ForegroundIsSystemUI() {
				log.Fatalf("前台是锁屏/系统 UI，无法安全操作。请解锁屏幕（动一下鼠标键盘）后重新运行。")
			}
			if n, err := gamewin.FocusByTitle(kw); err != nil {
				fmt.Printf("⚠️  置顶游戏窗口失败（关键词 %q）: %v\n", kw, err)
				logHook("置顶失败: " + err.Error())
			} else if n > 0 {
				fmt.Printf("已把 %d 个游戏窗口还原并切到前台（关键词 %q）\n", n, kw)
				logHook(fmt.Sprintf("已置顶游戏窗口（%q）", kw))
			} else {
				fmt.Printf("未找到标题含 %q 的窗口——游戏可能还没启动\n", kw)
				logHook("没找到游戏窗口，等待启动…")
			}
			// 窗口化游戏：把感知与点击收敛到窗口客户区。
			//
			// 全屏感知在窗口化游戏上是双重劣化——老师的送审帧里游戏只占
			// 一小块、学生的降采样观测里游戏只剩噪声；点击也要叠加窗口偏移。
			// 找到窗口就把三者统一到窗口坐标系（失败不致命，退回全屏模式）。
			if pcb, ok := be.(*pcBackend); ok {
				if cr, found := gamewin.ClientRectByTitle(kw); found {
					pcb.SetWindowRegion(cr)
					fmt.Printf("感知域: 窗口客户区 %dx%d@(%d,%d)（仅截取游戏画面，点击按窗口坐标换算）\n",
						cr.Dx(), cr.Dy(), cr.Min.X, cr.Min.Y)
					logHook(fmt.Sprintf("窗口域 %dx%d@(%d,%d)", cr.Dx(), cr.Dy(), cr.Min.X, cr.Min.Y))
				} else {
					fmt.Println("⚠️  未能定位游戏窗口客户区，退回全屏感知模式")
				}
			}
		}
	}

	scrW, scrH, err := be.Size()
	if err != nil {
		log.Fatalf("获取屏幕尺寸失败: %v", err)
	}
	fmt.Printf("屏幕: %dx%d | Ctrl+C 退出\n", scrW, scrH)

	// 老师自检：不可达就降级（实时回路照跑，只是不做评估）。
	var evaluator *teacher.Evaluator
	if cfg.TeacherEnabled {
		client := teacher.NewClient(cfg.TeacherURL, cfg.TeacherModel, teacher.Options{
			MaxTokens:   teacher.DefaultMaxTokens,
			Temperature: teacher.DefaultTemperature,
			Timeout:     cfg.EvalTimeout,
		})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		ver, pingErr := client.Ping(ctx)
		cancel()
		if pingErr != nil {
			fmt.Printf("⚠️  老师不可达（%v），本次运行将不做评估，实时回路不受影响\n", pingErr)
			fmt.Println("   提示：先启动本地实例 —— bash tools/serve_ollama.sh")
		} else {
			evaluator = teacher.NewEvaluator(client, cfg.Goal)
			fmt.Printf("老师: %s @ %s (Ollama %s) | 每 %d 帧抽 1 帧，攒 %d 帧评估一次\n",
				cfg.TeacherModel, cfg.TeacherURL, ver, cfg.EvalEvery, cfg.EvalFrames)
			if cfg.EvalOutDir != "" {
				fmt.Printf("评估报告落盘: %s\n", cfg.EvalOutDir)
			}
			fmt.Println("提示：首次评估需把模型加载进显存，可能耗时数十秒。")
		}
	} else {
		fmt.Println("老师未启用（加 -teacher 开启异步教学回路）")
	}

	// ---- Phase 2：老师在线示范 ----
	//
	// 这条分支与下面的实时回路互斥：示范由老师「一步一决策」驱动，
	// 节拍是秒级（老师单次 1.2s），塞进 30FPS 的回路只会不停丢帧。
	if cfg.Demo {
		demoClient := teacher.NewClient(cfg.TeacherURL, cfg.TeacherModel, func() teacher.Options {
			o := teacher.ActionOptions()
			o.Timeout = cfg.EvalTimeout
			return o
		}())
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		ver, pingErr := demoClient.Ping(ctx)
		cancel()
		if pingErr != nil {
			log.Fatalf("示范模式需要老师在线，但连接 %s 失败: %v\n"+
				"   提示：先启动本地实例 —— bash tools/serve_ollama.sh", cfg.TeacherURL, pingErr)
		}

		var hints []string
		if s := strings.TrimSpace(*demoHints); s != "" {
			for _, h := range strings.Split(s, ";") {
				if h = strings.TrimSpace(h); h != "" {
					hints = append(hints, h)
				}
			}
		}
		dem := &teacher.Demonstrator{
			Client:  demoClient,
			Profile: prof,
			Goal:    cfg.Goal,
			Hints:   hints,
		}

		fmt.Printf("老师: %s @ %s (Ollama %s) | 已关思考（think=false），单步约 1~2s\n",
			cfg.TeacherModel, cfg.TeacherURL, ver)
		if cfg.Goal != "" {
			fmt.Printf("目标: %s\n", cfg.Goal)
			logHook("目标: " + cfg.Goal)
		}
		for _, h := range hints {
			fmt.Printf("先验: %s\n", h)
		}
		if !cfg.Live {
			fmt.Println("⚠️  dry-run：老师照常决策但不会真的操作，加 -live 才发送触摸")
		}

		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
		logHook("示范模式启动（" + mode + "）")
		if err := runDemo(cfg, be, dem, prof, stop, logHook); err != nil {
			log.Fatalf("示范回路异常: %v", err)
		}
		return
	}

	var actor agent.Actor = agent.NewRule()
	if *studentWeights != "" {
		sa, err := NewStudentActor(*studentWeights, prof)
		if err != nil {
			log.Fatalf("加载学生权重失败: %v", err)
		}
		actor = sa
	}
	traj := memory.NewBuffer(4096)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	frameCh := make(chan memory.Record, 16)
	done := make(chan struct{})
	go reporter(frameCh, traj, cfg, done)

	// 抽样通道：实时回路只做非阻塞投递，抽帧与推理都在教学协程里做。
	sampleCh := make(chan sample)
	var teachDone chan struct{}
	if evaluator != nil {
		teachDone = make(chan struct{})
		go teachLoop(cfg, evaluator, traj, be, sampleCh, stop, teachDone)
	}

	loopErr := make(chan error, 1)
	go func() {
		loopErr <- runLoop(cfg, tickInterval, actor, be, traj, frameCh, sampleCh, evaluator != nil, stop)
	}()

	select {
	case err := <-loopErr:
		if err != nil {
			log.Fatalf("实时回路异常: %v", err)
		}
	case <-stop:
		fmt.Println("\n收到退出信号，正在收尾…")
		if err := <-loopErr; err != nil {
			log.Fatalf("实时回路异常: %v", err)
		}
	}

	close(frameCh)
	<-done
	close(sampleCh)
	if teachDone != nil {
		<-teachDone // 等老师收尾（in-flight 评估允许跑完）
	}
	fmt.Println("已退出。")
}

// sample 是实时回路投递给教学回路的抽样信号（只带元信息，不带像素）。
type sample struct {
	No     int64
	At     time.Time
	Action agent.Action
}

// runLoop 是实时执行回路（goroutine A）：抓屏 -> 决策 -> 输入，按节拍节流。
// runLoop 是实时执行回路（goroutine A）：抓屏 -> 决策 -> 输入，按节拍节流。
//
// 感知与行动都通过 backend 抽象，因此同一段回路既能驱动本机键鼠，
// 也能通过 ADB 驱动手机——换后端不改逻辑。
func runLoop(cfg config.Config, tick time.Duration, actor agent.Actor,
	be backend, traj *memory.Buffer,
	frameCh chan<- memory.Record, sampleCh chan<- sample, sampling bool,
	stop <-chan os.Signal) error {

	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	var n int64
	var dropped int64
	for {
		select {
		case <-stop:
			return nil
		case <-ticker.C:
		}

		t0 := time.Now()
		frame, err := be.Grab(cfg.DownsampleWidth)
		if err != nil {
			return err
		}
		t1 := time.Now()

		act, err := actor.Decide(frame)
		if err != nil {
			return err
		}
		t2 := time.Now()

		if err := be.Apply(act); err != nil {
			return err
		}
		t3 := time.Now()

		n++
		rec := memory.Record{
			FrameNo:   n,
			At:        t3,
			CaptureMs: ms(t0, t1),
			DecideMs:  ms(t1, t2),
			InputMs:   ms(t2, t3),
			TotalMs:   ms(t0, t3),
			Action:    act,
			GrayMean:  meanGray(frame),
		}
		traj.Push(rec)
		// 学生调试：每个动作打一行（30FPS 下每秒 30 行，量可控；
		// 与老师 demo 的日志格式对齐，便于 grep 统计动作分布）。
		fmt.Printf("[%4d] %s\n", n, act.String())
		select {
		case frameCh <- rec:
		default: // 统计协程积压时丢弃，不影响实时回路
		}

		// 抽样送审：只做非阻塞投递；教学回路忙（正在评估）就丢弃本次抽样。
		if sampling && cfg.EvalEvery > 0 && n%int64(cfg.EvalEvery) == 0 {
			select {
			case sampleCh <- sample{No: n, At: t3, Action: act}:
			default:
				dropped++
			}
		}

		if cfg.MaxFrames > 0 && n >= int64(cfg.MaxFrames) {
			if dropped > 0 {
				fmt.Printf("[抽样] 因教学回路繁忙丢弃 %d 次抽样\n", dropped)
			}
			return nil
		}
	}
}

// teachLoop 是异步教学回路（goroutine B）：按抽样节奏抓教师分辨率关键帧，
// 攒够 EvalFrames 帧后送审老师，打印反馈并可选落盘报告。
//
// 抓帧与推理都在本协程内完成，实时回路的每帧延迟不受影响。
func teachLoop(cfg config.Config, ev *teacher.Evaluator, traj *memory.Buffer, be backend,
	sampleCh <-chan sample, stop <-chan os.Signal, done chan<- struct{}) {

	defer close(done)
	ring := make([]teacher.Frame, 0, cfg.EvalFrames)

	for {
		select {
		case <-stop:
			return
		case s, ok := <-sampleCh:
			if !ok {
				return
			}
			img, err := be.Grab(cfg.EvalWidth)
			if err != nil {
				fmt.Printf("[老师] 抓取送审帧失败: %v\n", err)
				continue
			}
			ring = append(ring, teacher.Frame{
				No:       s.No,
				At:       s.At,
				Action:   s.Action,
				GrayMean: meanGray(img),
				Gray:     img,
			})
			if len(ring) < cfg.EvalFrames {
				continue
			}

			ep := teacher.Episode{
				Goal:   cfg.Goal,
				Frames: append([]teacher.Frame(nil), ring...),
				Window: ring[len(ring)-1].At.Sub(ring[0].At),
			}
			ring = ring[:0] // 每次评估覆盖一个新的时间窗

			// 串行评估：本协程内直接跑，避免并发压垮老师；实时回路不受影响。
			evaluate(cfg, ev, traj, ep)
		}
	}
}

func evaluate(cfg config.Config, ev *teacher.Evaluator, traj *memory.Buffer, ep teacher.Episode) {
	fmt.Printf("\n[老师] 送审 %d 帧 / %.1f 秒，等待 %s 反馈…（首次需加载模型）\n",
		len(ep.Frames), ep.Window.Seconds(), cfg.TeacherModel)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.EvalTimeout)
	defer cancel()

	rep, err := ev.Evaluate(ctx, ep)
	if err != nil {
		fmt.Printf("[老师] 评估失败: %v\n\n", err)
		return
	}

	fmt.Printf("[老师] 耗时 %.1fs | %d tok (%.0f tok/s)", rep.Stats.TotalMs/1000,
		rep.Stats.OutputTokens, rep.Stats.TokPerSec)
	if rep.Score >= 0 {
		fmt.Printf(" | 评分 %d", rep.Score)
	}
	fmt.Printf(" | 缓冲 %d 帧\n", traj.Len())
	for _, line := range strings.Split(strings.TrimSpace(rep.Text), "\n") {
		if strings.TrimSpace(line) != "" {
			fmt.Printf("        %s\n", line)
		}
	}

	if cfg.EvalOutDir != "" {
		if path, err := rep.SaveMarkdown(cfg.EvalOutDir); err != nil {
			fmt.Printf("[老师] 报告落盘失败: %v\n", err)
		} else {
			fmt.Printf("[老师] 报告已保存: %s\n", path)
		}
	}
	fmt.Println()
}

// reporter 是轻量统计协程：定期打印平均/最大延迟。
func reporter(frameCh <-chan memory.Record, traj *memory.Buffer, cfg config.Config, done chan<- struct{}) {
	defer close(done)
	var (
		n              int
		sumCapture     float64
		sumDecide      float64
		sumInput       float64
		sumTotal       float64
		maxTotal       float64
		worstTotalLine string
	)
	for rec := range frameCh {
		n++
		sumCapture += rec.CaptureMs
		sumDecide += rec.DecideMs
		sumInput += rec.InputMs
		sumTotal += rec.TotalMs
		if rec.TotalMs > maxTotal {
			maxTotal = rec.TotalMs
			worstTotalLine = fmt.Sprintf("帧#%d", rec.FrameNo)
		}
		if n%cfg.ReportEvery == 0 {
			fmt.Printf("[统计] 帧#%d 缓冲=%d | 平均: 截屏%.1fms 决策%.1fms 输入%.1fms 总计%.1fms | 最差 %s (%.1fms)\n",
				rec.FrameNo, traj.Len(),
				sumCapture/float64(n), sumDecide/float64(n),
				sumInput/float64(n), sumTotal/float64(n),
				worstTotalLine, maxTotal)
		}
	}
}

func ms(a, b time.Time) float64 {
	return float64(b.Sub(a).Microseconds()) / 1000.0
}

// gameKeyword 返回用于窗口匹配的游戏关键词：优先档案名，
// 其次包名段。找不到就返回空（不做最小化）。
func gameKeyword(cfg config.Config, prof *game.Profile) string {
	if prof != nil && prof.Name != "" {
		return prof.Name
	}
	if cfg.Game != "" {
		return cfg.Game
	}
	return ""
}

func meanGray(img *image.Gray) uint8 {
	if img == nil || img.Bounds().Dx() == 0 {
		return 0
	}
	b := img.Bounds()
	var sum uint64
	for y := b.Min.Y; y < b.Max.Y; y++ {
		row := y * img.Stride
		for x := b.Min.X; x < b.Max.X; x++ {
			sum += uint64(img.Pix[row+x])
		}
	}
	return uint8(sum / uint64(b.Dx()*b.Dy()))
}
