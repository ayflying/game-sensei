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
//
// 默认 dry-run（不真正发送键鼠），Ctrl+C 或达到 -frames 后干净退出。
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
	"github.com/ayflying/game-sensei/internal/capture"
	"github.com/ayflying/game-sensei/internal/config"
	"github.com/ayflying/game-sensei/internal/input"
	"github.com/ayflying/game-sensei/internal/memory"
	"github.com/ayflying/game-sensei/internal/teacher"
)

func main() {
	cfg := config.Default()
	flag.IntVar(&cfg.TargetFPS, "fps", cfg.TargetFPS, "目标帧率")
	flag.IntVar(&cfg.DownsampleWidth, "down", cfg.DownsampleWidth, "感知输入降采样宽度（像素，0=不降采样）")
	flag.BoolVar(&cfg.Live, "live", cfg.Live, "真实发送键鼠输入（默认 dry-run）")
	flag.IntVar(&cfg.MaxFrames, "frames", cfg.MaxFrames, "运行帧数上限（0=直到 Ctrl+C）")
	flag.IntVar(&cfg.ReportEvery, "report", cfg.ReportEvery, "每 N 帧打印一次统计")

	flag.BoolVar(&cfg.TeacherEnabled, "teacher", cfg.TeacherEnabled, "启用老师（异步教学回路）")
	flag.StringVar(&cfg.TeacherURL, "teacher-url", cfg.TeacherURL, "老师服务地址（Ollama）")
	flag.StringVar(&cfg.TeacherModel, "teacher-model", cfg.TeacherModel, "老师模型名")
	flag.IntVar(&cfg.EvalEvery, "eval-every", cfg.EvalEvery, "每 N 帧抽一帧送审")
	flag.IntVar(&cfg.EvalFrames, "eval-frames", cfg.EvalFrames, "一次评估覆盖的关键帧数")
	flag.IntVar(&cfg.EvalWidth, "eval-width", cfg.EvalWidth, "送审帧降采样宽度（像素）")
	flag.DurationVar(&cfg.EvalTimeout, "eval-timeout", cfg.EvalTimeout, "单次送审超时")
	flag.StringVar(&cfg.Goal, "goal", cfg.Goal, "游戏目标描述（写入老师提示词）")
	flag.StringVar(&cfg.EvalOutDir, "eval-out", cfg.EvalOutDir, "评估报告落盘目录（空=仅控制台）")
	flag.Parse()

	if cfg.TargetFPS <= 0 {
		cfg.TargetFPS = 30
	}
	if cfg.ReportEvery <= 0 {
		cfg.ReportEvery = 30
	}
	tickInterval := time.Second / time.Duration(cfg.TargetFPS)

	mode := "dry-run（不发送真实输入）"
	if cfg.Live {
		mode = "LIVE（真实键鼠输入）"
	}
	fmt.Printf("game-sensei | 模式=%s | 目标=%dFPS | 降采样宽=%dpx\n", mode, cfg.TargetFPS, cfg.DownsampleWidth)

	rect, err := capture.Bounds()
	if err != nil {
		log.Fatalf("截屏初始化失败: %v", err)
	}
	fmt.Printf("主屏: %dx%d | Ctrl+C 退出\n", rect.Dx(), rect.Dy())

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

	actor := agent.NewRule()
	actuator := input.NewActuator(cfg.Live)
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
		go teachLoop(cfg, evaluator, traj, sampleCh, stop, teachDone)
	}

	loopErr := make(chan error, 1)
	go func() {
		loopErr <- runLoop(cfg, tickInterval, actor, actuator, traj, frameCh, sampleCh, evaluator != nil, stop)
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
func runLoop(cfg config.Config, tick time.Duration, actor agent.Actor,
	actuator *input.Actuator, traj *memory.Buffer,
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
		frame, err := capture.Grab(cfg.DownsampleWidth)
		if err != nil {
			return err
		}
		t1 := time.Now()

		act, err := actor.Decide(frame)
		if err != nil {
			return err
		}
		t2 := time.Now()

		if err := actuator.Apply(act); err != nil {
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
func teachLoop(cfg config.Config, ev *teacher.Evaluator, traj *memory.Buffer,
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
			img, err := capture.Grab(cfg.EvalWidth)
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
