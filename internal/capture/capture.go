//go:build windows

// Package capture 提供 Windows 平台的屏幕抓取（纯 Go syscall，零 CGO）。
//
// Phase 0 走 GDI（GetDC + BitBlt + GetDIBits）：实现简单、通用性好，
// 全屏抓一次约几毫秒，足够验证实时回路。DXGI Desktop Duplication
// 作为 Phase 0.5 的加速选项（见 README §11.1）。
//
// 本包仅编译于 Windows（依赖 gdi32/user32 的 LazyDLL 绑定）。
package capture

import (
	"fmt"
	"image"
	"syscall"
	"unsafe"
)

var (
	user32 = syscall.NewLazyDLL("user32.dll")
	gdi32  = syscall.NewLazyDLL("gdi32.dll")

	procGetDC                = user32.NewProc("GetDC")
	procReleaseDC            = user32.NewProc("ReleaseDC")
	procGetDeviceCaps        = gdi32.NewProc("GetDeviceCaps")
	procCreateCompatibleDC   = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitm = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject         = gdi32.NewProc("SelectObject")
	procBitBlt               = gdi32.NewProc("BitBlt")
	procGetDIBits            = gdi32.NewProc("GetDIBits")
	procDeleteDC             = gdi32.NewProc("DeleteDC")
	procDeleteObject         = gdi32.NewProc("DeleteObject")
)

const (
	horzRes = 8  // GetDeviceCaps: 桌面宽度（像素）
	vertRes = 10 // GetDeviceCaps: 桌面高度（像素）

	srccopy         = 0x00CC0020 // BitBlt 光栅操作：直接复制
	dibRgbWindows   = 0          // BITMAPINFO 使用 Windows V4 头（此处用 V3，值为 0）
	biRgb           = 0          // 未压缩
	dibItOpBottomUp = 0          // 按 DIB 自带方向读取
)

// bitmapInfoHeader 对应 Win32 BITMAPINFOHEADER（40 字节）。
type bitmapInfoHeader struct {
	Size         uint32
	Width        int32
	Height       int32 // 负值 = top-down（第一行对应屏幕顶部）
	Planes       uint16
	BitCount     uint16
	Compression  uint32
	SizeImage    uint32
	XPPelMeter   int32
	YPelMeter    int32
	ClrUsed      uint32
	ClrImportant uint32
}

// Bounds 返回主屏尺寸。
func Bounds() (image.Rectangle, error) {
	hdc, _, err := procGetDC.Call(0)
	if hdc == 0 {
		return image.Rectangle{}, wrapCallError("GetDC", err)
	}
	defer procReleaseDC.Call(0, hdc)
	w, _, _ := procGetDeviceCaps.Call(hdc, horzRes)
	h, _, _ := procGetDeviceCaps.Call(hdc, vertRes)
	if w == 0 || h == 0 {
		return image.Rectangle{}, fmt.Errorf("capture: 获取桌面尺寸失败")
	}
	return image.Rect(0, 0, int(w), int(h)), nil
}

// grabBGRA 抓取整个主屏，返回 top-down 的 32bpp BGRA 原始字节与宽高。
//
// 抽出来是为了让灰度（学生观测）与彩色（老师 VLM 判读）两条路共用同一份
// BitBlt/GetDIBits 逻辑——两边各写一遍最容易出现「一个改了另一个忘了」。
func grabBGRA() ([]byte, int, int, error) {
	rect, err := Bounds()
	if err != nil {
		return nil, 0, 0, err
	}
	w, h := rect.Dx(), rect.Dy()

	screenDC, _, err := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, 0, 0, wrapCallError("GetDC", err)
	}
	defer procReleaseDC.Call(0, screenDC)

	memDC, _, err := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return nil, 0, 0, wrapCallError("CreateCompatibleDC", err)
	}
	defer procDeleteDC.Call(memDC)

	bmp, _, err := procCreateCompatibleBitm.Call(screenDC, uintptr(w), uintptr(h))
	if bmp == 0 {
		return nil, 0, 0, wrapCallError("CreateCompatibleBitmap", err)
	}
	defer procDeleteObject.Call(bmp)

	oldObj, _, err := procSelectObject.Call(memDC, bmp)
	if oldObj == 0 {
		return nil, 0, 0, wrapCallError("SelectObject", err)
	}
	defer procSelectObject.Call(memDC, oldObj)

	// 屏幕 (0,0,w,h) -> 内存位图 (0,0)
	ret, _, err := procBitBlt.Call(memDC, 0, 0, uintptr(w), uintptr(h), screenDC, 0, 0, srccopy)
	if ret == 0 {
		return nil, 0, 0, wrapCallError("BitBlt", err)
	}

	// top-down 32bpp BGRA，避免手动翻转行序
	bih := bitmapInfoHeader{
		Size:     uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width:    int32(w),
		Height:   -int32(h),
		Planes:   1,
		BitCount: 32,
	}
	buf := make([]byte, w*h*4)
	ret, _, err = procGetDIBits.Call(
		screenDC, bmp, 0, uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&bih)),
		dibItOpBottomUp,
	)
	if ret == 0 {
		return nil, 0, 0, wrapCallError("GetDIBits", err)
	}
	return buf, w, h, nil
}

// Grab 抓取整个主屏并返回灰度图；若 downWidth>0 则等比降采样到该宽度。
// 输出 pixel 为 image.Gray（单通道），直接可作为学生网络输入的前体。
func Grab(downWidth int) (*image.Gray, error) {
	buf, w, h, err := grabBGRA()
	if err != nil {
		return nil, err
	}
	return toGrayDownsampled(buf, w, h, downWidth), nil
}

// GrabColor 抓取整个主屏并返回**全分辨率**彩色图。
//
// 与 Grab 的用途不同：Grab 给「学生」当感知输入（灰度、极小），
// GrabColor 给「老师」VLM 判读画面——彩色与可读细节才够它认界面元素。
// 降采样交给调用方（internal/vision.Downscale），后端保持「只负责取像素」。
func GrabColor() (image.Image, error) {
	buf, w, h, err := grabBGRA()
	if err != nil {
		return nil, err
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for i, j := 0, 0; i < len(buf); i, j = i+4, j+4 {
		dst.Pix[j] = buf[i+2]   // R
		dst.Pix[j+1] = buf[i+1] // G
		dst.Pix[j+2] = buf[i]   // B
		dst.Pix[j+3] = 255      // A
	}
	return dst, nil
}

// clipBGRA 从整屏 BGRA 缓冲里裁出 region 子矩形，返回新缓冲与子图宽高。
//
// 为什么需要它：窗口化游戏只占屏幕一小块（如 427x782 的微信小游戏嵌在
// 1920x1080 桌面里）。全屏感知有两个代价——①老师看到的游戏只占画面 28%，
// 降采样后界面元素糊成一团；②学生观测 160px 宽里游戏只剩 45px，全是噪声。
// 裁剪让「感知域」与「点击域」都收敛到游戏窗口内，坐标换算也更简单。
func clipBGRA(bgra []byte, w, h int, region image.Rectangle) ([]byte, int, int, error) {
	r := region.Intersect(image.Rect(0, 0, w, h))
	if r.Empty() {
		return nil, 0, 0, fmt.Errorf("capture: 裁剪区域 %s 与屏幕 %dx%d 无交集", region, w, h)
	}
	cw, ch := r.Dx(), r.Dy()
	out := make([]byte, cw*ch*4)
	for y := 0; y < ch; y++ {
		srcRow := (r.Min.Y+y)*w*4 + r.Min.X*4
		copy(out[y*cw*4:(y+1)*cw*4], bgra[srcRow:srcRow+cw*4])
	}
	return out, cw, ch, nil
}

// GrabRegion 抓取主屏的 region 子区域并返回灰度图（downWidth 语义同 Grab）。
func GrabRegion(region image.Rectangle, downWidth int) (*image.Gray, error) {
	buf, w, h, err := grabBGRA()
	if err != nil {
		return nil, err
	}
	cbuf, cw, ch, err := clipBGRA(buf, w, h, region)
	if err != nil {
		return nil, err
	}
	return toGrayDownsampled(cbuf, cw, ch, downWidth), nil
}

// GrabColorRegion 抓取主屏的 region 子区域并返回全分辨率彩色图。
func GrabColorRegion(region image.Rectangle) (image.Image, error) {
	buf, w, h, err := grabBGRA()
	if err != nil {
		return nil, err
	}
	cbuf, cw, ch, err := clipBGRA(buf, w, h, region)
	if err != nil {
		return nil, err
	}
	dst := image.NewRGBA(image.Rect(0, 0, cw, ch))
	for i, j := 0, 0; i < len(cbuf); i, j = i+4, j+4 {
		dst.Pix[j] = cbuf[i+2]
		dst.Pix[j+1] = cbuf[i+1]
		dst.Pix[j+2] = cbuf[i]
		dst.Pix[j+3] = 255
	}
	return dst, nil
}

// toGrayDownsampled 将 BGRA 字节流转为灰度图；downWidth<=0 时保持原尺寸。
// 采用整数加权（BT.601 近似），避免浮点开销。
func toGrayDownsampled(bgra []byte, w, h, downWidth int) *image.Gray {
	ow, oh := w, h
	if downWidth > 0 && downWidth < w {
		ow = downWidth
		oh = h * downWidth / w
		if oh < 1 {
			oh = 1
		}
	}
	dst := image.NewGray(image.Rect(0, 0, ow, oh))
	sxRatio := float64(w) / float64(ow)
	syRatio := float64(h) / float64(oh)
	for y := 0; y < oh; y++ {
		sy := int(float64(y) * syRatio)
		if sy >= h {
			sy = h - 1
		}
		rowBase := sy * w * 4
		for x := 0; x < ow; x++ {
			sx := int(float64(x) * sxRatio)
			if sx >= w {
				sx = w - 1
			}
			i := rowBase + sx*4
			b, g, r := bgra[i], bgra[i+1], bgra[i+2]
			dst.Pix[y*dst.Stride+x] = uint8((int(r)*299 + int(g)*587 + int(b)*114) / 1000)
		}
	}
	return dst
}

func wrapCallError(op string, err error) error {
	if err != nil && err != syscall.Errno(0) {
		return fmt.Errorf("capture: %s 失败: %w", op, err)
	}
	return fmt.Errorf("capture: %s 失败（Win32 返回 0）", op)
}
