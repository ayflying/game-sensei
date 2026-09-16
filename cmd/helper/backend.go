package main

import (
	"fmt"
	"image"
	"strings"
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
	//
	// ⚠️ 安卓侧可能复用最近一次截图的缓存（同一拍老师/学生共用同一张），
	// 需要「现在这一刻」的画面必须用 GrabColorFresh。
	GrabColor() (image.Image, error)
	// GrabColorFresh 抓一帧「现在」的全分辨率彩色画面（安卓强制重新截图）。
	// 实时回路给彩色学生喂观测、plan 轮询判据都依赖它。
	GrabColorFresh() (image.Image, error)
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

func newPCBackend(prof *game.Profile, live bool) *pcBackend {
	act := input.NewActuator(live)
	// 注入点击域尺寸：点击类动作要把归一化坐标换算成鼠标绝对位置。
	// 全屏模式下是桌面尺寸；SetWindowRegion 后会换成窗口尺寸（见该函数）。
	// 不注入的话点击会明确报错，而不是点到 (0,0) 去。
	act.Screen = func() (int, int, error) {
		r, err := capture.Bounds()
		if err != nil {
			return 0, 0, err
		}
		return r.Dx(), r.Dy(), nil
	}
	return &pcBackend{actionResolver: actionResolver{profile: prof}, actuator: act}
}

// SetWindowRegion 把 PC 后端的「感知 + 点击」域收敛到窗口矩形内。
//
// 空矩形 = 清除裁剪，回到全屏模式。
//
// 为什么要有这一层：窗口化游戏（如微信小游戏 427x782）只占桌面一小块，
// 全屏感知下老师的送审帧里游戏只占 28%、学生的 160px 观测里只剩 45px 噪声；
// 点击坐标若按全屏换算，档案里的归一化值还要叠加窗口偏移。收敛到窗口后
// 三者共享同一坐标系：截的是窗口、看的只有游戏、归一化坐标直接乘窗口宽高。
func (b *pcBackend) SetWindowRegion(r image.Rectangle) {
	b.region = r
	if r.Empty() {
		b.actuator.Offset = image.Point{}
		return
	}
	b.actuator.Offset = r.Min
	// 点击域尺寸换成窗口客户区尺寸，归一化坐标的换算基准与感知域一致。
	b.actuator.Screen = func() (int, int, error) {
		return r.Dx(), r.Dy(), nil
	}
}

// windowRegion 返回当前生效的裁剪矩形（空 = 全屏）。
func (b *pcBackend) windowRegion() image.Rectangle { return b.region }

// pcBackend 控制本机：GDI 抓屏 + SendInput 键鼠。
type pcBackend struct {
	actionResolver
	actuator *input.Actuator
	// region 非空时：感知与点击都限制在窗口矩形内（屏幕坐标系）。
	region image.Rectangle
	// windowKeyword 是游戏窗口标题关键词；非空时 CheckSafe 会在每次动作前
	// 校验前台窗口仍是它（见 CheckSafe）。空 = 不做前台校验（全屏模式）。
	windowKeyword string
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
	if !b.region.Empty() {
		return capture.GrabRegion(b.region, downWidth)
	}
	return capture.Grab(downWidth)
}

func (b *pcBackend) GrabColor() (image.Image, error) {
	restore := b.shot()
	if restore != nil {
		defer restore()
	}
	if !b.region.Empty() {
		return capture.GrabColorRegion(b.region)
	}
	return capture.GrabColor()
}

// GrabColorFresh 与 GrabColor 等价：PC 侧本来就是每次真做一次 BitBlt，
// 没有缓存概念。分开命名只为满足 plan 的显式诉求（见 plan.ColorGrabber）。
func (b *pcBackend) GrabColorFresh() (image.Image, error) { return b.GrabColor() }

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
	if !b.region.Empty() {
		return b.region.Dx(), b.region.Dy(), nil
	}
	r, err := capture.Bounds()
	if err != nil {
		return 0, 0, err
	}
	return r.Dx(), r.Dy(), nil
}

func (b *pcBackend) Describe() string { return "pc（本机 GDI 截屏 + SendInput 键鼠）" }

func (b *pcBackend) Live() bool { return b.actuator.Live }

// CheckSafe 实现 plan.SafetyChecker：PC 端确认「输入会落到游戏窗口」。
//
// 两条判据（DEVELOPMENT_PLAN §8）：
//  1. dry-run 不发真实输入，无需检查；
//  2. 窗口模式下（有 windowKeyword），前台窗口标题必须仍含该关键词——
//     否则 SendInput 会打到别的程序上（IDE、浏览器、甚至聊天窗口）。
//
// 未配窗口关键词时退化为「不检查」：全屏模式没有可靠的目标判据，
// 此时检查只会制造假警报。这是已知限制，不是遗漏。
func (b *pcBackend) CheckSafe() error {
	if !b.actuator.Live || b.windowKeyword == "" {
		return nil
	}
	if gamewin.ForegroundIsSystemUI() {
		return fmt.Errorf("前台是锁屏/系统 UI，输入会被吞掉或落到锁屏上")
	}
	title := gamewin.ForegroundTitle()
	if !strings.Contains(strings.ToLower(title), strings.ToLower(b.windowKeyword)) {
		return fmt.Errorf("前台窗口是 %q，已不含游戏关键词 %q——可能被切到别的程序",
			title, b.windowKeyword)
	}
	return nil
}

// SetWindowKeyword 记录游戏窗口标题关键词，供 CheckSafe 在每次动作前校验。
func (b *pcBackend) SetWindowKeyword(kw string) { b.windowKeyword = kw }

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
//
// ⚠️ 这是「可能过期」的一帧，只适合知道自己在要同一拍画面的调用方（老师）。
// 需要「现在这一刻」的画面必须用 GrabColorFresh。
func (b *adbBackend) GrabColor() (image.Image, error) {
	if c := b.dev.LastColor(); c != nil {
		return c, nil
	}
	return b.dev.Screenshot()
}

// GrabColorFresh 强制重新截一帧。
//
// 为什么必须和 GrabColor 分开（2026-09-13 实测）：plan 的 ratio 条件靠**轮询**
// 判断「某 UI 是否出现」。安卓 screencap 单帧约 0.94s，GrabColor 会把它缓存起来
// 复用；若轮询每次拿到的都是同一张缓存帧，占比结果恒定不变——条件要么第一轮
// 立刻成立、要么永远不成立，循环判据彻底失效。这里宁可多花一次截图时间，
// 也要保证每次判定基于真实当前画面。
func (b *adbBackend) GrabColorFresh() (image.Image, error) {
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

// CheckSafe 实现 plan.SafetyChecker：安卓端确认「触摸会落到目标应用」。
//
// 为什么每次动作前都要查（DEVELOPMENT_PLAN §8「应用不在前台立即停止」）：
// 手机是「共享前台」的设备——激励广告、系统更新弹窗、锁屏都会把游戏挤到后台。
// 此时再发 tap，点的是**别的应用**（实测遇到过点「换发型」触发激励广告，
// 广告把前台切到 Google Play；若那一刻继续按计划点，就会误触商店界面）。
//
// 三种放行情形，除此之外一律拦下：
//  1. dry-run：不发真实输入；
//  2. 档案没配 package：无从判断目标，退回不检查（已知限制）；
//  3. 查询报错：**不**放行——连「现在前台是谁」都问不到，说明设备已断连，
//     按 §8 属于「设备断连」必须停，不能赌它其实没事。
func (b *adbBackend) CheckSafe() error {
	if !b.live {
		return nil
	}
	pkg := ""
	if b.profile != nil {
		pkg = b.profile.Package
	}
	if pkg == "" {
		return nil
	}
	ok, err := b.dev.IsForeground(pkg)
	if err != nil {
		return fmt.Errorf("无法确认前台应用（%v）——设备可能已断连", err)
	}
	if !ok {
		cur, cerr := b.dev.Foreground()
		if cerr != nil || cur == "" {
			cur = "（查询失败）"
		}
		return fmt.Errorf("目标应用 %s 不在前台（当前前台：%s）——输入会落到别的应用上", pkg, cur)
	}
	return nil
}

func (b *adbBackend) Close() error { return nil }

// openBackend 按 -target 构造后端；android 模式下可选自动拉起目标应用。
func openBackend(cfg config.Config, prof *game.Profile, launch bool) (backend, error) {
	switch cfg.Target {
	case "pc", "":
		return newPCBackend(prof, cfg.Live), nil

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
