//go:build windows

// Package overlay 提供一个右下角的置顶日志浮窗（纯 syscall，零 CGO）。
//
// 设计目标：游戏全屏时也能看见 agent 的运行日志。做法是一个
// WS_EX_TOPMOST | WS_EX_LAYERED | WS_EX_NOACTIVATE | WS_EX_TOOLWINDOW 的
// 无边框小窗，钉在主屏右下角、永远压在其它窗口之上（包括全屏游戏）。
//
// 刷新用 UpdateLayeredWindow：先在内存位图上用 GDI 画文字，再整窗提交。
// 每alpha通道做半透明底，文字不透明，观感是「游戏画面上一块半透明黑框」。
//
// 本包仅编译于 Windows。
package overlay

import (
	"fmt"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32 = syscall.NewLazyDLL("user32.dll")
	gdi32  = syscall.NewLazyDLL("gdi32.dll")

	procRegisterClassExW    = user32.NewProc("RegisterClassExW")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procDefWindowProcW      = user32.NewProc("DefWindowProcW")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procUpdateLayeredWindow = user32.NewProc("UpdateLayeredWindow")
	procGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	procSetWindowPos        = user32.NewProc("SetWindowPos")
	procShowWindow          = user32.NewProc("ShowWindow")
	procDeleteObject        = gdi32.NewProc("DeleteObject")
	procCreateCompatibleDC  = gdi32.NewProc("CreateCompatibleDC")
	procCreateDIBSection    = gdi32.NewProc("CreateDIBSection")
	procSelectObject        = gdi32.NewProc("SelectObject")
	procDeleteDC            = gdi32.NewProc("DeleteDC")
	procCreateFontW         = gdi32.NewProc("CreateFontW")
	procSetTextColor        = gdi32.NewProc("SetTextColor")
	procSetBkMode           = gdi32.NewProc("SetBkMode")
	procDrawTextW           = user32.NewProc("DrawTextW")
	procGetDC               = user32.NewProc("GetDC")
	procReleaseDC           = user32.NewProc("ReleaseDC")
	// SetWindowDisplayAffinity：让浮窗对屏幕抓取不可见（WDA_EXCLUDEFROMCAPTURE）。
	// 关键：浮窗是给「人」看的，绝不能被 capture.Grab / GrabColor 拍进去，
	// 否则老师和学生看到的画面被日志污染，感知数据直接作废。
	procSetWindowDisplayAffinity = user32.NewProc("SetWindowDisplayAffinity")
	procPostThreadMessageW       = user32.NewProc("PostThreadMessageW")
	procGetCurrentThreadId       = syscall.NewLazyDLL("kernel32.dll").NewProc("GetCurrentThreadId")
)

// wdaExcludeFromCapture：Win10 2004+ 的「仅对人眼可见」标志。
const wdaExcludeFromCapture = 0x00000011

const (
	// 窗口样式
	wsPopup         = 0x80000000
	wsVisible       = 0x10000000
	wsDisabled      = 0x08000000
	wsExLayered     = 0x00080000
	wsExTopmost     = 0x00000008
	wsExToolWindow  = 0x00000080
	wsExNoActivate  = 0x08000000
	wsExTranslucent = 0x00000020

	// UpdateLayeredWindow
	ulwAlpha = 2

	// SetWindowPos
	hwndTopmost   = ^uintptr(0) // (HWND)-1
	swpNoActivate = 0x0010
	swpShowWindow = 0x0040
	swpNoSize     = 0x0001
	swpNoMove     = 0x0002

	// GetSystemMetrics
	smCxScreen = 0
	smCyScreen = 1

	// WM
	wmDestroy = 0x0002
	wmPaint   = 0x000F
	wmQuit    = 0x0012

	// DrawText
	dtSingleLine = 0x0020
	dtCalcRect   = 0x0400
	dtLeft       = 0x0000
	dtVcenter    = 0x0004

	// SetBkMode
	transparent = 1

	// 字体粗细
	fwBold   = 700
	fwNormal = 400

	// CreateFont 的 DEFAULT_CHARSET / CLIP 默认值
	defaultCharset    = 1
	outDefaultPrecis  = 0
	clipDefaultPrecis = 0
	qualityDefault    = 0
	pitchFF           = 0
)

// rect 对应 Win32 RECT。
type rect struct {
	Left, Top, Right, Bottom int32
}

// msg 对应 Win32 MSG。
type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
	_       uint32 // padding
}

// bitmapInfoHeader 对应 BITMAPINFOHEADER（与 capture 包一致，独立声明避免跨包耦合）。
type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

// wndClassExW 对应 WNDCLASSEXW。
type wndClassExW struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  uintptr
	LpszClassName uintptr
	HIconSm       uintptr
}

// point 对应 Win32 POINT。
type point struct {
	X, Y int32
}

// Window 是右下角日志浮窗。
type Window struct {
	mu         sync.Mutex
	closeOnce  sync.Once
	lines      []string
	maxLines   int
	widthPx    int
	lineH      int32
	font       uintptr
	hwnd       uintptr
	className  string
	pumpThread uintptr
	dcMem      uintptr
	bmp        uintptr
	bits       *byte
	w, h       int32
	dirty      chan struct{}
	quit       chan struct{}
}

// Options 是浮窗配置。
type Options struct {
	MaxLines int // 最多显示几行日志（默认 6）
	WidthPx  int // 浮窗宽度（默认 560）
	FontSize int // 字号（默认 15）
}

func (o *Options) fill() {
	if o.MaxLines <= 0 {
		o.MaxLines = 6
	}
	if o.WidthPx <= 0 {
		o.WidthPx = 560
	}
	if o.FontSize <= 0 {
		o.FontSize = 15
	}
}

// Start 创建浮窗并启动消息泵，失败返回错误。返回的 *Window 用 Push 写日志。
// 调用 Close 停止。
func Start(opt Options) (*Window, error) {
	opt.fill()
	w := &Window{
		maxLines: opt.MaxLines,
		widthPx:  opt.WidthPx,
		lineH:    int32(opt.FontSize) * 18 / 10,
		dirty:    make(chan struct{}, 1),
		quit:     make(chan struct{}),
	}
	if err := w.create(opt); err != nil {
		return nil, err
	}
	go w.pump()
	// 定时重画顶置，防止游戏切换模式后浮窗被压下去
	go w.keepTop()
	return w, nil
}

// Push 追加一行日志（自动裁剪过长行、淘汰旧行并触发重画）。线程安全。
func (w *Window) Push(line string) {
	line = strings.ReplaceAll(line, "\t", "    ")
	// 单行最多 110 字符（按字节截，中文一格按 3 字节考虑已足够）
	if len(line) > 330 {
		line = line[:327] + "…"
	}
	line = strings.ToValidUTF8(line, "?")

	w.mu.Lock()
	w.lines = append(w.lines, line)
	if len(w.lines) > w.maxLines {
		w.lines = w.lines[len(w.lines)-w.maxLines:]
	}
	w.mu.Unlock()

	select {
	case w.dirty <- struct{}{}:
	default:
	}
}

// Close 关闭浮窗（幂等）。
func (w *Window) Close() {
	w.closeOnce.Do(func() {
		close(w.quit)
		// 向消息泵**所在线程**投 WM_QUIT。PostQuitMessage 只对调用线程生效，
		// 从这里（别的线程）调它是无效的——这是「进程退不出去」的元凶。
		if w.pumpThread != 0 {
			procPostThreadMessageW.Call(w.pumpThread, wmQuit, 0, 0)
		}
	})
}

// Hide / Show 抓屏期间临时隐藏/恢复浮窗。
//
// 为什么需要：WDA_EXCLUDEFROMCAPTURE 对 Windows.Graphics.Capture 是「窗口消失」，
// 但对 **GDI BitBlt 是「该区域变黑」**——实测截屏里留下一块纯黑矩形，
// 老师 VLM 每帧都会看到它，感知照样被污染（2026-09-12 实拍确认）。
// 所以抓屏前隐藏、抓完立刻恢复：人眼只在抓屏瞬间（几十毫秒）看到浮窗闪一下，
// 截屏里则完全干净。demo/教学模式的抓屏频率（秒级）下闪烁无感；
// 30FPS 实时回路里浮窗基本不可见，介意就在那种场景加 -overlay=false。
func (w *Window) Hide() {
	if w != nil && w.hwnd != 0 {
		procShowWindow.Call(w.hwnd, 0) // SW_HIDE
	}
}

func (w *Window) Show() {
	if w != nil && w.hwnd != 0 {
		procShowWindow.Call(w.hwnd, 5) // SW_SHOW
	}
}

func utf16(s string) uintptr {
	p, _ := syscall.UTF16PtrFromString(s)
	return uintptr(unsafe.Pointer(p))
}

func (w *Window) create(opt Options) error {
	w.className = "GameSenseiOverlay"

	hInstance, _, _ := procGetModuleHandleW.Call(0)

	//WNDPROC 用汇编级 callback 需 syscall.NewCallback（仅 Windows，可用）
	wndProc := syscall.NewCallback(func(hwnd, msg, wParam, lParam uintptr) uintptr {
		switch msg {
		case wmDestroy:
			procPostQuitMessage.Call(0)
			return 0
		}
		ret, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
		return ret
	})

	wc := wndClassExW{
		CbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		Style:         0,
		LpfnWndProc:   wndProc,
		HInstance:     hInstance,
		LpszClassName: utf16(w.className),
	}
	if ret, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); ret == 0 {
		return fmt.Errorf("overlay: RegisterClassExW 失败: %v", err)
	}

	sw, _, _ := procGetSystemMetrics.Call(smCxScreen)
	sh, _, _ := procGetSystemMetrics.Call(smCyScreen)
	if sw == 0 || sh == 0 {
		return fmt.Errorf("overlay: 获取屏幕尺寸失败")
	}

	// 浮窗高度 = 行数 × 行高 + 上下留白
	w.h = int32(w.maxLines)*w.lineH + 16
	w.w = int32(w.widthPx)
	x := int32(sw) - w.w - 12
	y := int32(sh) - w.h - 12

	exStyle := uintptr(wsExLayered | wsExTopmost | wsExToolWindow | wsExNoActivate | wsExTranslucent)
	style := uintptr(wsPopup | wsVisible | wsDisabled)

	hwnd, _, err := procCreateWindowExW.Call(
		exStyle,
		utf16(w.className),
		utf16("game-sensei 日志"),
		style,
		uintptr(x), uintptr(y), uintptr(w.w), uintptr(w.h),
		0, 0, hInstance, 0,
	)
	if hwnd == 0 {
		return fmt.Errorf("overlay: CreateWindowExW 失败: %v", err)
	}
	w.hwnd = hwnd

	// 对屏幕抓取隐藏：人眼可见，GDI/DXGI 截屏拍不到。
	// 失败不致命（老系统没有这个 API），但会在日志里提示。
	if ret, _, _ := procSetWindowDisplayAffinity.Call(hwnd, wdaExcludeFromCapture); ret == 0 {
		fmt.Println("overlay: ⚠️  SetWindowDisplayAffinity 失败，浮窗可能被截屏拍进去（需 Win10 2004+）")
	}

	// 字体：中文用「Microsoft YaHei UI」，失败退回默认。
	// ⚠️ 返回值必须接住：之前这里没接，w.font 永远是 0，
	// SelectObject 选了个 NULL 字体进去，浮窗只剩黑底没有字。
	font, _, _ := procCreateFontW.Call(
		uintptr(int32(-opt.FontSize)*96/72), // 高度：负值表示字符高度
		0, 0, 0,
		fwBold,
		0, 0, 0,
		defaultCharset, outDefaultPrecis, clipDefaultPrecis, qualityDefault, pitchFF,
		utf16("Microsoft YaHei UI"),
	)
	w.font = font

	// 内存 DC + DIB（ARGB premultiplied），供 UpdateLayeredWindow 使用
	screenDC, _, _ := procGetDC.Call(0)
	defer procReleaseDC.Call(0, screenDC)
	w.dcMem, _, _ = procCreateCompatibleDC.Call(screenDC)
	if w.dcMem == 0 {
		return fmt.Errorf("overlay: CreateCompatibleDC 失败")
	}
	bih := bitmapInfoHeader{
		Size:     uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width:    w.w,
		Height:   -w.h, // top-down
		Planes:   1,
		BitCount: 32,
	}
	var bitsPtr unsafe.Pointer
	bmp, _, err := procCreateDIBSection.Call(
		w.dcMem, uintptr(unsafe.Pointer(&bih)), 0, uintptr(unsafe.Pointer(&bitsPtr)), 0, 0,
	)
	if bmp == 0 {
		return fmt.Errorf("overlay: CreateDIBSection 失败: %v", err)
	}
	w.bmp = bmp
	w.bits = (*byte)(bitsPtr)
	procSelectObject.Call(w.dcMem, bmp)
	procSelectObject.Call(w.dcMem, w.font)
	procSetBkMode.Call(w.dcMem, transparent)

	// SetWindowPos 钉顶 + 显示
	procSetWindowPos.Call(hwnd, hwndTopmost, 0, 0, 0, 0,
		swpNoActivate|swpShowWindow|swpNoSize|swpNoMove)

	return nil
}

// pump 是消息泵 + 重画循环（消息与重画合用一个 goroutine，避免 GDI 跨线程问题）。
func (w *Window) pump() {
	done := make(chan struct{})
	go func() {
		// 消息泵：记录本线程 id，Close 时向它投 WM_QUIT
		tid, _, _ := procGetCurrentThreadId.Call()
		w.pumpThread = tid
		defer close(done)
		var m msg
		for {
			// 只取本窗口的消息，减少干扰；WM_QUIT 让 GetMessage 返回 0
			r, _, _ := procGetMessageW.Call(
				uintptr(unsafe.Pointer(&m)), 0, 0, 0,
			)
			if r == 0 || int32(r) == -1 || m.Message == wmQuit {
				return
			}
			// 窗口销毁等消息交给 DefWindowProc（在 wndProc 里已处理 WM_DESTROY）
		}
	}()
	for {
		select {
		case <-w.quit:
			return
		case <-w.dirty:
		}
		w.render()
	}
}

// keepTop 周期性把浮窗重新钉顶：全屏独占模式可能在浮窗之后创建，
// 不补钉的话会被游戏压下去。
func (w *Window) keepTop() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-w.quit:
			return
		case <-t.C:
		}
		if w.hwnd != 0 {
			procSetWindowPos.Call(w.hwnd, hwndTopmost, 0, 0, 0, 0,
				swpNoActivate|swpNoSize|swpNoMove)
			// 周期性重申抓取排除：游戏切换全屏/独占模式后该标志可能失效
			procSetWindowDisplayAffinity.Call(w.hwnd, wdaExcludeFromCapture)
		}
	}
}

// render 把当前日志行画进 DIB 并提交 UpdateLayeredWindow。
func (w *Window) render() {
	w.mu.Lock()
	lines := append([]string(nil), w.lines...)
	w.mu.Unlock()

	total := int(w.w) * int(w.h)

	// 1) 清成不透明黑底（不透明的 RGB，透明度交给 ULW 的整窗 SrcConstantAlpha）。
	//
	// 为什么不按像素画 alpha：**GDI（DrawTextW）根本不写 alpha 字节**——
	// 它只改 RGB，alpha 保持底色值。之前注释里"DrawText 直接写不透明的 255"
	// 是错的，实际文字像素 alpha=底色的 165， premultiplied 语义被破坏，
	// 实测整窗糊成一块看不清字的黑块（2026-09-12 用户实拍）。
	// 整窗统一半透明（ULW 阶段做）就没有这个问题，文字是清清楚楚的白字。
	buf := unsafe.Slice(w.bits, total*4)
	for i := 0; i < total*4; i += 4 {
		buf[i] = 0
		buf[i+1] = 0
		buf[i+2] = 0
		buf[i+3] = 255
	}

	// 2) 逐行画文字（白色，alpha 由 DrawText 直接写不透明的 255）
	procSetTextColor.Call(w.dcMem, 0x00FFFFFF) // COLORREF 是 0x00BBGGRR，白色 = 0xFFFFFF
	for i, ln := range lines {
		if i >= int(w.maxLines) {
			break
		}
		rc := rect{
			Left:   8,
			Top:    8 + int32(i)*w.lineH,
			Right:  w.w - 8,
			Bottom: 8 + int32(i+1)*w.lineH,
		}
		procDrawTextW.Call(w.dcMem, utf16(ln), ^(uintptr(0)), // (int)-1
			uintptr(unsafe.Pointer(&rc)),
			dtSingleLine|dtLeft|dtVcenter)
	}

	// 3) 提交。
	//    ⚠️ pptDst 必须传 NULL：这个参数不是「忽略位置」而是「把窗口移到该坐标」，
	//    之前传 (0,0) 把浮窗拖到了屏幕左上角（2026-09-12 实拍：黑块出现在左上）。
	//    位置只由 CreateWindowExW 决定，这里不碰。
	//    半透明改用整窗 SrcConstantAlpha=170（≈67% 不透明），AlphaFormat=0
	//    （不按像素 alpha 混合），DIB 本身保持不透明。
	screenDC, _, _ := procGetDC.Call(0)
	defer procReleaseDC.Call(0, screenDC)
	ptSrc := point{X: 0, Y: 0}
	size := struct{ CX, CY int32 }{w.w, w.h}
	blend := blendFunction{Op: 0 /*AC_SRC_OVER*/, Flags: 0, SrcConstantAlpha: 170, AlphaFormat: 0}
	procUpdateLayeredWindow.Call(
		w.hwnd,
		screenDC,
		0, // pptDst = NULL：不移动窗口
		uintptr(unsafe.Pointer(&size)),
		w.dcMem,
		uintptr(unsafe.Pointer(&ptSrc)),
		0,
		uintptr(unsafe.Pointer(&blend)),
		ulwAlpha,
	)
}

// blendFunction 对应 Win32 BLENDFUNCTION。
type blendFunction struct {
	Op               byte
	Flags            byte
	SrcConstantAlpha byte
	AlphaFormat      byte
}

// getModuleHandleW 的 LazyDLL 声明放在文件尾部，保持 var 块干净。
var procGetModuleHandleW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetModuleHandleW")
