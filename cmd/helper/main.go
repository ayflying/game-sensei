// game-sensei —— 泛型游戏辅助：大模型教师 + 量化学生自学习闭环（见 README.md）。
//
// Phase 0 实时回路：capture(截屏) -> agent(规则 stub 决策) -> input(键鼠)，
// 并统计每段耗时验证延迟。teacher 异步回路 Phase 1 接入。
//
// 用法：
//
//	helper [-live] [-fps 30] [-down 160] [-frames 0] [-report 30]
//
// 默认 dry-run（不真正发送键鼠），Ctrl+C 或达到 -frames 后干净退出。
package main

import (
	"flag"
	"fmt"
	"image"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ayflying/game-sensei/internal/agent"
	"github.com/ayflying/game-sensei/internal/capture"
	"github.com/ayflying/game-sensei/internal/config"
	"github.com/ayflying/game-sensei/internal/input"
	"github.com/ayflying/game-sensei/internal/memory"
)

func main() {
	cfg := config.Default()
	flag.IntVar(&cfg.TargetFPS, "fps", cfg.TargetFPS, "目标帧率")
	flag.IntVar(&cfg.DownsampleWidth, "down", cfg.DownsampleWidth, "感知输入降采样宽度（像素，0=不降采样）")
	flag.BoolVar(&cfg.Live, "live", cfg.Live, "真实发送键鼠输入（默认 dry-run）")
	flag.IntVar(&cfg.MaxFrames, "frames", cfg.MaxFrames, "运行帧数上限（0=直到 Ctrl+C）")
	flag.IntVar(&cfg.ReportEvery, "report", cfg.ReportEvery, "每 N 帧打印一次统计")
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
	fmt.Printf("game-sensei Phase 0 | 模式=%s | 目标=%dFPS | 降采样宽=%dpx\n", mode, cfg.TargetFPS, cfg.DownsampleWidth)

	rect, err := capture.Bounds()
	if err != nil {
		log.Fatalf("截屏初始化失败: %v", err)
	}
	fmt.Printf("主屏: %dx%d | Ctrl+C 退出\n", rect.Dx(), rect.Dy())

	actor := agent.NewRule()
	actuator := input.NewActuator(cfg.Live)
	traj := memory.NewBuffer(4096)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	frameCh := make(chan memory.Record, 16)
	done := make(chan struct{})
	go reporter(frameCh, traj, cfg, done)

	loopErr := make(chan error, 1)
	go func() {
		loopErr <- runLoop(cfg, tickInterval, actor, actuator, traj, frameCh, stop)
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
	fmt.Println("已退出。轨迹缓冲保留最近记录，供 Phase 1 老师回放。")
}

// runLoop 是实时执行回路（goroutine A）：抓屏 -> 决策 -> 输入，按节拍节流。
func runLoop(cfg config.Config, tick time.Duration, actor agent.Actor,
	actuator *input.Actuator, traj *memory.Buffer,
	frameCh chan<- memory.Record, stop <-chan os.Signal) error {

	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	var n int64
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

		if cfg.MaxFrames > 0 && n >= int64(cfg.MaxFrames) {
			return nil
		}
	}
}

// reporter 是轻量统计协程：定期打印平均/最大延迟。
// （异步教学回路 goroutine B 在 Phase 1 从这里扩展。）
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
