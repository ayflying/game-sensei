package android

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Screenshot 抓取设备当前画面并返回彩色原图。
//
// 走 `adb exec-out screencap -p`：exec-out 不做换行转换，PNG 二进制可安全传输。
// 抓取结果会缓存，供 SavePNG / LastColor 复用，避免同一帧重复拉取。
//
// 抓帧前先做一次带节流的休眠自检：熄屏时 screencap 返回的是纯黑图，
// 上层判据会静默失效而非报错，所以这里必须兜住（见 power.go）。
func (d *Device) Screenshot() (image.Image, error) {
	d.ensureAwakeThrottled()
	raw, err := d.execOut("screencap", "-p")
	if err != nil {
		return nil, err
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("android: 截图 PNG 解码失败（%d 字节）: %w", len(raw), err)
	}

	d.mu.Lock()
	d.color = img
	d.size = image.Pt(img.Bounds().Dx(), img.Bounds().Dy())
	d.mu.Unlock()

	return img, nil
}

// LastColor 返回最近一次截图的彩色原图；尚未截图时返回 nil。
func (d *Device) LastColor() image.Image {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.color
}

// Grab 抓屏并转为灰度图，是实时回路与教学回路共用的感知入口。
//
// 与 internal/capture.Grab 语义一致：downWidth>0 时等比降采样到该宽度，
// 返回 *image.Gray，保证换后端不改变学生网络的输入形态。
func (d *Device) Grab(downWidth int) (*image.Gray, error) {
	img, err := d.Screenshot()
	if err != nil {
		return nil, err
	}
	return ToGrayDownsampled(img, downWidth), nil
}

// ScreenSize 返回当前方向的屏幕像素尺寸（即触摸输入的坐标系范围）。
//
// 关键：横屏时 `wm size` 报的仍是物理的竖屏尺寸（如 1200x2608），而触摸坐标
// 实际用的是横屏坐标系（2608x1200）。所以优先取最近一次截图的尺寸——
// 截图反映的是真实的当前坐标系；没有截图缓存时先截一帧来确定，
// 只有连截图都失败才退化为 wm size + 方向推断。
func (d *Device) ScreenSize() (image.Point, error) {
	if p, ok := d.cachedSize(); ok {
		return p, nil
	}

	// 无缓存：截一帧拿到真实坐标系（首次调用多花一次截图的时间，之后走缓存）。
	if _, err := d.Screenshot(); err == nil {
		if p, ok := d.cachedSize(); ok {
			return p, nil
		}
	}

	// 兜底：解析 wm size，并按旋转角交换宽高。
	return d.screenSizeFromWM()
}

func (d *Device) cachedSize() (image.Point, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.size.X > 0 && d.size.Y > 0 {
		return d.size, true
	}
	return image.Point{}, false
}

// screenSizeFromWM 在无法截图时的兜底：读 wm size 物理尺寸，
// 再依据 SurfaceOrientation（0/90/180/270）判断是否需要交换宽高。
func (d *Device) screenSizeFromWM() (image.Point, error) {
	out, err := d.Shell("wm size")
	if err != nil {
		return image.Point{}, err
	}
	var size image.Point
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "size") {
			continue
		}
		i := strings.LastIndex(line, ":")
		if i < 0 {
			continue
		}
		wh := strings.Split(strings.TrimSpace(line[i+1:]), "x")
		if len(wh) != 2 {
			continue
		}
		w, err1 := strconv.Atoi(strings.TrimSpace(wh[0]))
		h, err2 := strconv.Atoi(strings.TrimSpace(wh[1]))
		if err1 != nil || err2 != nil {
			continue
		}
		size = image.Pt(w, h) // Override 行在后面，覆盖 Physical
	}
	if size.X == 0 || size.Y == 0 {
		return image.Point{}, fmt.Errorf("android: 无法解析屏幕尺寸: %q", strings.TrimSpace(out))
	}

	// 旋转 90/270 度时触摸坐标系与物理尺寸的宽高相反。
	if deg, err := d.rotationDegrees(); err == nil && (deg == 90 || deg == 270) {
		size = image.Pt(size.Y, size.X)
	}
	return size, nil
}

// rotationDegrees 读取屏幕旋转角（0/90/180/270）。失败时返回错误，由调用方决定是否忽略。
func (d *Device) rotationDegrees() (int, error) {
	out, err := d.Shell("dumpsys input | grep -i SurfaceOrientation | head -1")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(out, "\n") {
		if i := strings.LastIndex(line, ":"); i >= 0 {
			if v, err := strconv.Atoi(strings.TrimSpace(line[i+1:])); err == nil {
				return v, nil
			}
		}
	}
	return 0, fmt.Errorf("android: 无法解析屏幕方向: %q", strings.TrimSpace(out))
}

// SaveLastPNG 把最近一次截图（彩色原图）写盘，用于存档与人工核对。
func (d *Device) SaveLastPNG(path string) error {
	img := d.LastColor()
	if img == nil {
		return fmt.Errorf("android: 尚无截图可保存，请先调用 Screenshot/Grab")
	}
	return SavePNG(img, path)
}

// SavePNG 把任意图像编码为 PNG 写盘（自动创建父目录）。
func SavePNG(img image.Image, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("android: 创建目录失败: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("android: 创建文件失败: %w", err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		return fmt.Errorf("android: PNG 编码失败: %w", err)
	}
	return nil
}

// ToGrayDownsampled 把任意图像转为灰度图并按宽度等比降采样（BT.601 加权）。
//
// 对 screenshot 常见的 NRGBA/RGBA 走直读像素的快速路径；其余类型回退 image.Image.At。
func ToGrayDownsampled(src image.Image, downWidth int) *image.Gray {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return image.NewGray(image.Rect(0, 0, 1, 1))
	}

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

	switch s := src.(type) {
	case *image.NRGBA: // 4 字节/像素，non-premultiplied
		for y := 0; y < oh; y++ {
			sy := clampInt(b.Min.Y+int(float64(y)*syRatio), b.Min.Y, b.Max.Y-1)
			rowBase := (sy-b.Min.Y)*s.Stride - b.Min.X*4
			out := y * dst.Stride
			for x := 0; x < ow; x++ {
				sx := clampInt(b.Min.X+int(float64(x)*sxRatio), b.Min.X, b.Max.X-1)
				i := rowBase + sx*4
				dst.Pix[out+x] = luma(s.Pix[i], s.Pix[i+1], s.Pix[i+2])
			}
		}
	case *image.RGBA: // 4 字节/像素，premultiplied（screencap 全不透明，等价）
		for y := 0; y < oh; y++ {
			sy := clampInt(b.Min.Y+int(float64(y)*syRatio), b.Min.Y, b.Max.Y-1)
			rowBase := (sy-b.Min.Y)*s.Stride - b.Min.X*4
			out := y * dst.Stride
			for x := 0; x < ow; x++ {
				sx := clampInt(b.Min.X+int(float64(x)*sxRatio), b.Min.X, b.Max.X-1)
				i := rowBase + sx*4
				dst.Pix[out+x] = luma(s.Pix[i], s.Pix[i+1], s.Pix[i+2])
			}
		}
	default:
		for y := 0; y < oh; y++ {
			sy := clampInt(b.Min.Y+int(float64(y)*syRatio), b.Min.Y, b.Max.Y-1)
			out := y * dst.Stride
			for x := 0; x < ow; x++ {
				sx := clampInt(b.Min.X+int(float64(x)*sxRatio), b.Min.X, b.Max.X-1)
				r, g, bl, _ := src.At(sx, sy).RGBA()
				dst.Pix[out+x] = luma(uint8(r>>8), uint8(g>>8), uint8(bl>>8))
			}
		}
	}
	return dst
}

// luma 按 BT.601 加权求灰度，用整数运算避免浮点开销。
func luma(r, g, b uint8) uint8 {
	return uint8((int(r)*299 + int(g)*587 + int(b)*114) / 1000)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
