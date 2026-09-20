package vision

import (
	"fmt"
	"image"
	"image/draw"
	"strconv"
	"strings"
)

// ParseRect 解析 "x0,y0,x1,y1" 形式的像素区域，并校验它落在 w×h 之内。
//
// 坐标一律是**原图像素**（左上原点、右下开区间），与 `cmd/shot` 的归一化
// 坐标是两套口径：标定阶段用归一化，识别阶段用像素，换算由调用方负责。
// 越界直接报错而不是静默夹取——裁剪错位置会把 OCR 结果引到别处，
// 那种错误在结果里看不出来，代价远大于一条明确的报错。
func ParseRect(spec string, w, h int) (image.Rectangle, error) {
	parts := strings.Split(spec, ",")
	if len(parts) != 4 {
		return image.Rectangle{}, fmt.Errorf("区域必须是 x0,y0,x1,y1 四个整数")
	}
	var n [4]int
	for i, p := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return image.Rectangle{}, fmt.Errorf("区域第 %d 个坐标不是整数: %q", i+1, p)
		}
		n[i] = v
	}
	if n[0] < 0 || n[1] < 0 || n[2] <= n[0] || n[3] <= n[1] {
		return image.Rectangle{}, fmt.Errorf("区域为空或坐标为负: %s", spec)
	}
	if w > 0 && n[2] > w || h > 0 && n[3] > h {
		return image.Rectangle{}, fmt.Errorf("区域 %s 超出图像范围 %dx%d", spec, w, h)
	}
	return image.Rect(n[0], n[1], n[2], n[3]), nil
}

// Crop 按矩形裁剪，返回独立的新图（不与原图共享像素）。
func Crop(img image.Image, r image.Rectangle) image.Image {
	if img == nil {
		return nil
	}
	inter := r.Intersect(img.Bounds())
	if inter.Empty() {
		return image.NewRGBA(image.Rect(0, 0, 0, 0))
	}
	dst := image.NewRGBA(image.Rect(0, 0, inter.Dx(), inter.Dy()))
	draw.Draw(dst, dst.Bounds(), img, inter.Min, draw.Src)
	return dst
}
