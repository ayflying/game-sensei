package main

import (
	"fmt"
	"image"
	"time"

	"github.com/ayflying/game-sensei/internal/agent"
	"github.com/ayflying/game-sensei/internal/android"
	"github.com/ayflying/game-sensei/internal/capture"
	"github.com/ayflying/game-sensei/internal/config"
	"github.com/ayflying/game-sensei/internal/input"
)

// backend 是「感知 + 行动」的平台后端抽象。
//
// 两条回路（实时执行 / 异步教学）都只依赖这个接口，因此换平台——本机键鼠
// 还是 ADB 遥控手机上的游戏——不需要改动回路的任何逻辑。
//
//	dry-run 语义在每个实现里各自保证：Grab 是只读的照常执行，
//	真实输入（键鼠 / 触摸）在 Live=false 时被吞掉。
type backend interface {
	// Grab 抓一帧灰度观测（downWidth>0 时等比降采样）。
	// 这是「学生」的输入：小、灰度、快。
	Grab(downWidth int) (*image.Gray, error)
	// GrabColor 抓一帧全分辨率彩色画面，供「老师」VLM 判读。
	// 与 Grab 分开是刻意的：学生要的是低延迟小张量，老师要的是能认字的清晰画面，
	// 两者的分辨率与色彩诉求正好相反，混用一个接口必然有一方将就。
	GrabColor() (image.Image, error)
	// Apply 执行一个动作（dry-run 时为空操作）。
	Apply(act agent.Action) error
	// Size 返回当前屏幕像素尺寸（PC 为桌面尺寸；Android 为当前方向尺寸）。
	Size() (w, h int, err error)
	// Describe 返回用于日志的平台标识。
	Describe() string
	// Live 报告是否真实发送输入。
	Live() bool
}

// pcBackend 控制本机：GDI 抓屏 + SendInput 键鼠。
type pcBackend struct {
	actuator *input.Actuator
}

func newPCBackend(live bool) *pcBackend {
	return &pcBackend{actuator: input.NewActuator(live)}
}

func (b *pcBackend) Grab(downWidth int) (*image.Gray, error) { return capture.Grab(downWidth) }

func (b *pcBackend) GrabColor() (image.Image, error) { return capture.GrabColor() }

func (b *pcBackend) Apply(act agent.Action) error { return b.actuator.Apply(act) }

func (b *pcBackend) Size() (int, int, error) {
	r, err := capture.Bounds()
	if err != nil {
		return 0, 0, err
	}
	return r.Dx(), r.Dy(), nil
}

func (b *pcBackend) Describe() string { return "pc（本机 GDI 截屏 + SendInput 键鼠）" }

func (b *pcBackend) Live() bool { return b.actuator.Live }

// adbBackend 控制 Android 设备：ADB 截图 + 触摸输入。
//
// 触摸坐标由 agent 输出的归一化值换算，故同一套策略可跨设备分辨率复用。
type adbBackend struct {
	dev  *android.Device
	live bool
}

func newADBBackend(dev *android.Device, live bool) *adbBackend {
	return &adbBackend{dev: dev, live: live}
}

func (b *adbBackend) Grab(downWidth int) (*image.Gray, error) { return b.dev.Grab(downWidth) }

// GrabColor 直接复用已解码的截图：Screenshot 会缓存最近一帧彩色图，
// 老师路径与感知路径在同一拍上通常只差几毫秒，没必要再截一次。
func (b *adbBackend) GrabColor() (image.Image, error) {
	if c := b.dev.LastColor(); c != nil {
		return c, nil
	}
	return b.dev.Screenshot()
}

func (b *adbBackend) Apply(act agent.Action) error {
	if !b.live {
		return nil // dry-run：吞掉动作，手机不会有任何触碰
	}
	return b.dev.Apply(act)
}

func (b *adbBackend) Size() (int, int, error) {
	p, err := b.dev.ScreenSize()
	if err != nil {
		return 0, 0, err
	}
	return p.X, p.Y, nil
}

func (b *adbBackend) Describe() string {
	return fmt.Sprintf("android %s（ADB 截图 + 触摸）", b.dev.Serial())
}

func (b *adbBackend) Live() bool { return b.live }

// openBackend 按 -target 构造后端；android 模式下可选自动拉起目标应用。
func openBackend(cfg config.Config, launch bool) (backend, error) {
	switch cfg.Target {
	case "pc", "":
		return newPCBackend(cfg.Live), nil

	case "android":
		dev, err := android.Open(cfg.ADBPath, cfg.Serial)
		if err != nil {
			return nil, err
		}
		if launch && cfg.AppPackage != "" {
			fmt.Printf("启动应用: %s …\n", cfg.AppPackage)
			if err := dev.Launch(cfg.AppPackage); err != nil {
				return nil, err
			}
			if err := dev.WaitForeground(cfg.AppPackage, 60*time.Second, time.Second); err != nil {
				// 启动确认失败不致命：可能已在后台运行，继续抓屏看看
				fmt.Printf("⚠️  %v（继续尝试抓屏）\n", err)
			}
		}
		return newADBBackend(dev, cfg.Live), nil

	default:
		return nil, fmt.Errorf("未知 -target %q（可选 pc | android）", cfg.Target)
	}
}
