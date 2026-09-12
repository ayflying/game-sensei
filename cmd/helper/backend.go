package main

import (
	"fmt"
	"image"
	"time"

	"github.com/ayflying/game-sensei/internal/agent"
	"github.com/ayflying/game-sensei/internal/android"
	"github.com/ayflying/game-sensei/internal/capture"
	"github.com/ayflying/game-sensei/internal/config"
	"github.com/ayflying/game-sensei/internal/game"
	"github.com/ayflying/game-sensei/internal/gamewin"
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
	// Resolve 把语义动作（L1）展开成平台动作（L2）。
	// 幂等：已是 L2 的动作原样返回。主要用于日志与预检——
	// Apply 内部也会先做一次，调用方不必先 Resolve 再 Apply。
	Resolve(act agent.Action) (agent.Action, error)
	// Apply 执行一个动作（dry-run 时为空操作）。入参可以是 L1 或 L2。
	Apply(act agent.Action) error
	// Profile 返回当前生效的游戏档案（未加载时为 nil）。
	Profile() *game.Profile
	// Size 返回当前屏幕像素尺寸（PC 为桌面尺寸；Android 为当前方向尺寸）。
	Size() (w, h int, err error)
	// Describe 返回用于日志的平台标识。
	Describe() string
	// Live 报告是否真实发送输入。
	Live() bool
	// Close 释放后端资源（PC 端会抬起所有仍按住的键）。
	Close() error
}

// actionResolver 给后端挂上游戏档案，负责 L1 → L2 的翻译。
//
// 把「档案」放在这一层而不是回路里，是为了让回路的代码完全不知道
// 游戏的存在：回路只说「朝前走」，能不能做到、怎么做到由后端 + 档案决定。
type actionResolver struct {
	profile *game.Profile
}

func (r actionResolver) Resolve(act agent.Action) (agent.Action, error) {
	if r.profile == nil {
		return act, nil // 没挂档案：只认 L2 动作，L1 会在执行层报错
	}
	return r.profile.Resolve(act)
}

func (r actionResolver) Profile() *game.Profile { return r.profile }

// newPCBackend 构造 PC 后端。winKeyword 非空 = 窗口域模式：
// 感知与点击都以该标题窗口的客户区为基准，且每帧现查（窗口可拖动/缩放）。
func newPCBackend(prof *game.Profile, live bool, winKeyword string) *pcBackend {
	kw := winKeyword
	act := input.NewActuator(live)
	// 注入点击域尺寸：点击类动作要把归一化坐标换算成鼠标绝对位置。
	// 全屏模式下是桌面尺寸；SetWindowRegion 后会换成窗口尺寸（见该函数）。
	// 不注入的话点击会明确报错，而不是点到 (0,0) 去。
	// 窗口域模式下点击换算基准动态查窗口客户区（窗口被拖动/缩放也不漂移）：
	// 尺寸 = 客户区宽高，偏移 = 客户区原点。winKeyword 为空时退回桌面尺寸。
	act.Screen = func() (int, int, error) {
		if kw != "" {
			if cr, found := gamewin.ClientRectByTitle(kw); found {
				return cr.Dx(), cr.Dy(), nil
			}
		}
		r, err := capture.Bounds()
		if err != nil {
			return 0, 0, err
		}
		return r.Dx(), r.Dy(), nil
	}
	act.OffsetFunc = func() image.Point {
		if kw != "" {
			if cr, found := gamewin.ClientRectByTitle(kw); found {
				return cr.Min
			}
		}
		return image.Point{}
	}
	return &pcBackend{actionResolver: actionResolver{profile: prof}, actuator: act, winKeyword: kw}
}

// SetWindowRegion 把 PC 后端的「感知 + 点击」域收敛到窗口矩形内。
//
// 空矩形 = 清除裁剪，回到全屏模式。
//
// 为什么要有这一层：窗口化游戏（如微信小游戏 427x782）只占桌面一小块，
// 全屏感知下老师的送审帧里游戏只占 28%、学生的 160px 观测里只剩 45px 噪声；
// 点击坐标若按全屏换算，档案里的归一化值还要叠加窗口偏移。收敛到窗口后
// 三者共享同一坐标系：截的是窗口、看的只有游戏、归一化坐标直接乘窗口宽高。
//
// ⚠️ 缓存陷阱：窗口可被用户**拖动/缩放**，启动时缓存的矩形会失效——
// 轻则截到桌面背景，重则点击全部错位。所以这里只记关键词，矩形每帧现查
// （currentRegion）。首次参数 r 仅用于启动时的一次性校验/日志。
func (b *pcBackend) SetWindowRegion(r image.Rectangle, titleKeyword string) {
	b.winKeyword = titleKeyword
	b.region = r
	// 点击域尺寸/偏移也走动态查询：与感知域同源，永不漂移。
	b.actuator.Offset = image.Point{} // 偏移在 pixel() 现查（见 Screen 注入）
	b.actuator.Screen = b.windowSize
}

// currentRegion 实时查询窗口客户区。
//
// 找不到窗口（用户关了游戏）时回退到上次缓存——此时抓屏/点击很快会失败，
// 但坐标系不至于跳变，日志里能看到「窗口丢了」的提示而不是静默错位。
func (b *pcBackend) currentRegion() image.Rectangle {
	if b.winKeyword == "" {
		return image.Rectangle{} // 全屏模式
	}
	if cr, found := gamewin.ClientRectByTitle(b.winKeyword); found {
		b.region = cr // 顺手刷新缓存，供窗口丢失时兜底
		return cr
	}
	fmt.Println("⚠️  游戏窗口找不到了（可能被关闭/最小化），沿用上次窗口位置")
	return b.region
}

// windowSize 是点击换算的动态基准：窗口模式返回当前客户区尺寸，否则桌面。
func (b *pcBackend) windowSize() (int, int, error) {
	r := b.currentRegion()
	if !r.Empty() {
		return r.Dx(), r.Dy(), nil
	}
	scr, err := capture.Bounds()
	if err != nil {
		return 0, 0, err
	}
	return scr.Dx(), scr.Dy(), nil
}

// pcBackend 控制本机：GDI 抓屏 + SendInput 键鼠。
type pcBackend struct {
	actionResolver
	actuator *input.Actuator
	// region 窗口域矩形（屏幕坐标系）；空 = 全屏模式。
	// ⚠️ 别直接读它——窗口可被拖动/缩放，实时值走 currentRegion()。
	region image.Rectangle
	// winKeyword 窗口标题关键词；非空 = 窗口域模式，矩形每帧现查。
	winKeyword string
	// beforeShot/afterShot 在每次抓屏前后调用（抓屏时隐藏日志浮窗，
	// 避免 WDA 在 GDI 截屏里留下黑块污染老师/学生的感知）。可为 nil。
	beforeShot func()
	afterShot  func()
}

func (b *pcBackend) shot() func() {
	if b.beforeShot != nil {
		b.beforeShot()
	}
	return b.afterShot
}

func (b *pcBackend) Grab(downWidth int) (*image.Gray, error) {
	restore := b.shot()
	if restore != nil {
		defer restore()
	}
	if r := b.currentRegion(); !r.Empty() {
		return capture.GrabRegion(r, downWidth)
	}
	return capture.Grab(downWidth)
}

func (b *pcBackend) GrabColor() (image.Image, error) {
	restore := b.shot()
	if restore != nil {
		defer restore()
	}
	if r := b.currentRegion(); !r.Empty() {
		return capture.GrabColorRegion(r)
	}
	return capture.GrabColor()
}

func (b *pcBackend) Apply(act agent.Action) error {
	resolved, err := b.Resolve(act)
	if err != nil {
		return err
	}
	return b.actuator.Apply(resolved)
}

// Size 返回「感知域」尺寸：窗口模式返回窗口客户区尺寸，否则返回桌面尺寸。
// 归一化坐标的换算基准必须与它一致——截到的是什么域，点击就换算到什么域。
func (b *pcBackend) Size() (int, int, error) {
	return b.windowSize()
}

func (b *pcBackend) Describe() string { return "pc（本机 GDI 截屏 + SendInput 键鼠）" }

func (b *pcBackend) Live() bool { return b.actuator.Live }

// Close 抬起仍按住的键：否则退出后键盘会卡在按下状态。
func (b *pcBackend) Close() error {
	b.actuator.ReleaseAll()
	return nil
}

// adbBackend 控制 Android 设备：ADB 截图 + 触摸输入。
//
// 触摸坐标由 agent 输出的归一化值换算，故同一套策略可跨设备分辨率复用。
type adbBackend struct {
	actionResolver
	dev  *android.Device
	live bool
}

func newADBBackend(dev *android.Device, prof *game.Profile, live bool) *adbBackend {
	return &adbBackend{actionResolver: actionResolver{profile: prof}, dev: dev, live: live}
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
	resolved, err := b.Resolve(act)
	if err != nil {
		return err
	}
	if !b.live {
		return nil // dry-run：吞掉动作，手机不会有任何触碰
	}
	return b.dev.Apply(resolved)
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

func (b *adbBackend) Close() error { return nil }

// openBackend 按 -target 构造后端；android 模式下可选自动拉起目标应用。
func openBackend(cfg config.Config, prof *game.Profile, launch bool) (backend, error) {
	switch cfg.Target {
	case "pc", "":
		return newPCBackend(prof, cfg.Live, gameKeyword(cfg, prof)), nil

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
		return newADBBackend(dev, prof, cfg.Live), nil

	default:
		return nil, fmt.Errorf("未知 -target %q（可选 pc | android）", cfg.Target)
	}
}
