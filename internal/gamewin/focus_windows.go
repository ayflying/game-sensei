//go:build windows

// 窗口的「查询 / 恢复 / 置顶」能力。
//
// 为什么需要这个：MinimizeByTitle 只解决「跑完把游戏收起来」，
// 但反过来——游戏被最小化后，Agent 必须先把它还原并切到前台，
// 抓屏才拿得到游戏画面（最小化的窗口 GDI 抓不到内容），
// SendInput 的按键才会真正进到游戏里（后台窗口收不到键盘焦点）。
//
// 前台切换在 Windows 上不是无条件成功的：系统有「前台锁」，
// 只有最近有过输入事件的进程才允许 SetForegroundWindow。
// 所以这里用两个官方推荐的绕法叠加：
//  1. 先按一次 Alt（keybd_event），让系统认为「用户刚按过键」；
//  2. AttachThreadInput 把自己的消息队列接到前台线程上，借它的前台权。
package gamewin

import (
	"fmt"
	"image"
	"syscall"
	"unsafe"
)

var (
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	procSetForegroundWindow   = user32.NewProc("SetForegroundWindow")
	procBringWindowToTop      = user32.NewProc("BringWindowToTop")
	procGetForegroundWindow   = user32.NewProc("GetForegroundWindow")
	procGetWindowRect         = user32.NewProc("GetWindowRect")
	procGetWindowThreadProcId = user32.NewProc("GetWindowThreadProcessId")
	procIsIconic              = user32.NewProc("IsIconic")
	procAttachThreadInput     = user32.NewProc("AttachThreadInput")
	procGetCurrentThreadId    = kernel32.NewProc("GetCurrentThreadId")
	procKeybdEvent            = user32.NewProc("keybd_event")
	procSetProcessDPIAware    = user32.NewProc("SetProcessDPIAware")
)

const (
	swRestore = 9 // 还原（最小化/最大化都回到正常尺寸）

	vkMenu = 0x12 // Alt
	// keybd_event 的标志位：0 = 按下，KEYUP = 抬起
	keyEVENTFKeyup = 0x0002
)

// Window 是一个顶层窗口的快照，供人工确认「Agent 找的是不是这个」。
type Window struct {
	HWND    uintptr
	PID     uint32
	Title   string
	Visible bool
	Iconic  bool            // 已最小化
	Bounds  image.Rectangle // 虚拟桌面坐标系下的矩形
}

func (w Window) String() string {
	state := "可见"
	if w.Iconic {
		state = "最小化"
	} else if !w.Visible {
		state = "隐藏"
	}
	return fmt.Sprintf("hwnd=%08x pid=%d [%s] %dx%d@(%d,%d) 标题=%q",
		uintptr(w.HWND), w.PID, state, w.Bounds.Dx(), w.Bounds.Dy(),
		w.Bounds.Min.X, w.Bounds.Min.Y, w.Title)
}

// EnsureDPIAware 让本进程的坐标与真实像素一致。
//
// 不调它的话：缩放 125%/150% 的屏幕上，GetWindowRect 与抓到的图都会
// 被系统虚拟化成「逻辑像素」，于是 Agent 点 (1000,500) 实际落在别处。
// 必须在任何查询之前调用一次（重复调用无害）。
func EnsureDPIAware() {
	procSetProcessDPIAware.Call()
}

// windowStyleIconic 读 GWL_STYLE 判断 WS_ICONIC（两补码写法见 MinimizeByTitle）。
func windowIsIconic(hwnd uintptr) bool {
	ret, _, _ := procIsIconic.Call(hwnd)
	return ret != 0
}

// collect 枚举所有带标题的顶层窗口（过滤掉无标题的工具窗，减少噪音）。
func collect() []Window {
	EnsureDPIAware()
	var out []Window
	cb := syscall.NewCallback(func(hwnd, lParam uintptr) uintptr {
		buf := make([]uint16, 512)
		n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if n == 0 {
			return 1
		}
		var pid uint32
		procGetWindowThreadProcId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		var vis uintptr
		vis, _, _ = procIsWindowVisible.Call(hwnd)

		// RECT 是有符号的：副屏在主屏左边时坐标为负
		var r struct{ left, top, right, bottom int32 }
		procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))

		out = append(out, Window{
			HWND:    hwnd,
			PID:     pid,
			Title:   syscall.UTF16ToString(buf[:n]),
			Visible: vis != 0,
			Iconic:  windowIsIconic(hwnd),
			Bounds:  image.Rect(int(r.left), int(r.top), int(r.right), int(r.bottom)),
		})
		return 1
	})
	procEnumWindows.Call(cb, 0)
	return out
}

// List 返回所有带标题的顶层窗口。
func List() []Window { return collect() }

// FindByTitle 按标题关键词（大小写不敏感子串）找窗口。
func FindByTitle(keyword string) []Window {
	if keyword == "" {
		return nil
	}
	var out []Window
	for _, w := range collect() {
		if containsFold(w.Title, keyword) {
			out = append(out, w)
		}
	}
	return out
}

// FocusByTitle 把标题含 keyword 的窗口还原并切到前台，返回成功的个数。
//
// 找不到返回 0 且 err==nil——调用方自己决定是报错还是继续等游戏启动。
//
// 只作用于「可见或已最小化」的窗口：同款游戏常有多个同名顶层窗
// （启动器、反作弊壳、隐藏的消息窗都算），对隐藏窗口置顶既无意义，
// 还可能把焦点抢到一个没有画面的空壳上。
func FocusByTitle(keyword string) (int, error) {
	all := FindByTitle(keyword)
	var wins []Window
	for _, w := range all {
		if w.Visible || w.Iconic {
			wins = append(wins, w)
		}
	}
	if len(wins) == 0 {
		return 0, nil
	}
	n := 0
	for _, w := range wins {
		if focusWindow(w.HWND) {
			n++
		}
	}
	if n == 0 {
		return 0, fmt.Errorf("gamewin: 找到 %d 个匹配 %q 的窗口，但置顶失败（可能被前台锁拒绝）", len(wins), keyword)
	}
	return n, nil
}

// focusWindow 还原 + 置顶单个窗口。
func focusWindow(hwnd uintptr) bool {
	// 最小化状态直接 SetForegroundWindow 不一定出画面，先 SW_RESTORE。
	// 用 ShowWindow 而非 Async：这里需要同步拿到结果再继续置顶。
	procShowWindowAsync.Call(hwnd, swRestore)

	// 绕开前台锁之一：补一次 Alt 的按下+抬起，
	// 让系统记录「刚刚有键盘输入」，此时才允许跨进程置顶。
	procKeybdEvent.Call(vkMenu, 0, 0, 0)
	procKeybdEvent.Call(vkMenu, 0, keyEVENTFKeyup, 0)

	// 绕开前台锁之二：把自己的输入队列接到当前前台线程上，借它的前台权限。
	fg, _, _ := procGetForegroundWindow.Call()
	var fgPID uint32
	fgThread, _, _ := procGetWindowThreadProcId.Call(fg, uintptr(unsafe.Pointer(&fgPID)))
	ourThread, _, _ := procGetCurrentThreadId.Call()
	attached := false
	if fgThread != 0 && fgThread != ourThread {
		ret, _, _ := procAttachThreadInput.Call(ourThread, fgThread, 1)
		attached = ret != 0
	}

	procBringWindowToTop.Call(hwnd)
	ret, _, _ := procSetForegroundWindow.Call(hwnd)

	if attached {
		procAttachThreadInput.Call(ourThread, fgThread, 0)
	}
	if ret != 0 {
		return true
	}
	// 少数情况下 SetForegroundWindow 返回 0 但窗口其实已经在前台，
	// 用「现在的前台是不是它」兜一次判断，避免误报失败。
	now, _, _ := procGetForegroundWindow.Call()
	return now == hwnd
}

// ForegroundTitle 返回当前前台窗口的标题（调试用：确认按键会落到哪个窗口）。
func ForegroundTitle() string {
	h, _, _ := procGetForegroundWindow.Call()
	if h == 0 {
		return ""
	}
	buf := make([]uint16, 512)
	n, _, _ := procGetWindowTextW.Call(h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf[:n])
}
