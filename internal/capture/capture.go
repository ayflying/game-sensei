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
	procSelectObject           = gdi32.NewProc("SelectObject")
	procBitBlt               = gdi32.NewProc("BitBlt")
	procGetDIBits            = gdi32.NewProc("GetDIBits")
	procDeleteDC             = gdi32.NewProc("DeleteDC")
	procDeleteObject         = gdi32.NewProc("DeleteObject")
)

const (
	horzRes = 8  // GetDeviceCaps: 桌面宽度（像素）
	vertRes = 10 // GetDeviceCaps: 桌面高度（像素）

	srccopy          = 0x00CC0020 // BitBlt 光栅操作：直接复制
	dibRgbWindows    = 0          // BITMAPINFO 使用 Windows V4 头（此处用 V3，值为 0）
	biRgb            = 0          // 未压缩
	dibItOpBottomUp  = 0          // 按 DIB 自带方向读取
)

// bitmapInfoHeader 对应 Win32 BITMAPINFOHEADER（40 字节）。
type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32 // 负值 = top-down（第一行对应屏幕顶部）
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPPelMeter    int32
	YPelMeter     int32
	ClrUsed       uint32
	ClrImportant  uint32
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

// Grab 抓取整个主屏并返回灰度图；若 downWidth>0 则等比降采样到该宽度。
// 输出 pixel 为 image.Gray（单通道），直接可作为学生网络输入的前体。
func Grab(downWidth int) (*image.Gray, error) {
	rect, err := Bounds()
	if err != nil {
		return nil, err
	}
	w, h := rect.Dx(), rect.Dy()

	screenDC, _, err := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, wrapCallError("GetDC", err)
	}
	defer procReleaseDC.Call(0, screenDC)

	memDC, _, err := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return nil, wrapCallError("CreateCompatibleDC", err)
	}
	defer procDeleteDC.Call(memDC)

	bmp, _, err := procCreateCompatibleBitm.Call(screenDC, uintptr(w), uintptr(h))
	if bmp == 0 {
		return nil, wrapCallError("CreateCompatibleBitmap", err)
	}
	defer procDeleteObject.Call(bmp)

	oldObj, _, err := procSelectObject.Call(memDC, bmp)
	if oldObj == 0 {
		return nil, wrapCallError("SelectObject", err)
	}
	defer procSelectObject.Call(memDC, oldObj)

	// 屏幕 (0,0,w,h) -> 内存位图 (0,0)
	ret, _, err := procBitBlt.Call(memDC, 0, 0, uintptr(w), uintptr(h), screenDC, 0, 0, srccopy)
	if ret == 0 {
		return nil, wrapCallError("BitBlt", err)
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
		return nil, wrapCallError("GetDIBits", err)
	}

	return toGrayDownsampled(buf, w, h, downWidth), nil
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
