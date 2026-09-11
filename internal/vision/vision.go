// Package vision 提供给「老师」VLM 用的画面预处理：降采样与编码。
//
// 与 internal/capture（抓屏）和 internal/android（ADB 截图）解耦：
// 后端只负责把像素取回来，怎么缩放、编成什么格式由这里统一决定，
// 避免每个后端各写一份缩放逻辑。
//
// 本包与平台无关，便于单测。
package vision

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
)

// Downscale 把 img 等比缩放到宽度不超过 maxWidth，采用**区域平均**而不是最近邻。
//
// 为什么在意缩放质量：老师要判读的界面元素（小图标、任务文字）在
// 2608→1024 这种大比例缩放下，最近邻会丢笔画甚至把按钮糊成色块，
// 区域平均能保住可读性。maxWidth<=0 或原图不超宽时原样返回。
func Downscale(img image.Image, maxWidth int) image.Image {
	if img == nil {
		return nil
	}
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw == 0 || sh == 0 {
		return img
	}
	if maxWidth <= 0 || sw <= maxWidth {
		return img
	}
	dw := maxWidth
	dh := sh * maxWidth / sw
	if dh < 1 {
		dh = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	// 每个目标像素覆盖的源区域（用浮点边界，避免整数累积误差）
	xRatio := float64(sw) / float64(dw)
	yRatio := float64(sh) / float64(dh)

	// *image.RGBA 是 ADB 截图（PNG 解码）与 GDI 抓屏最常见的落地类型，走快路径
	src, isRGBA := img.(*image.RGBA)

	for dy := 0; dy < dh; dy++ {
		y0 := int(float64(dy) * yRatio)
		y1 := int(float64(dy+1) * yRatio)
		if y1 <= y0 {
			y1 = y0 + 1
		}
		if y1 > sh {
			y1 = sh
		}
		for dx := 0; dx < dw; dx++ {
			x0 := int(float64(dx) * xRatio)
			x1 := int(float64(dx+1) * xRatio)
			if x1 <= x0 {
				x1 = x0 + 1
			}
			if x1 > sw {
				x1 = sw
			}

			var sr, sg, sb, sa, n uint64
			if isRGBA {
				for y := y0; y < y1; y++ {
					row := (y-b.Min.Y)*src.Stride + (x0-b.Min.X)*4
					for x := x0; x < x1; x++ {
						sr += uint64(src.Pix[row])
						sg += uint64(src.Pix[row+1])
						sb += uint64(src.Pix[row+2])
						sa += uint64(src.Pix[row+3])
						row += 4
						n++
					}
				}
			} else {
				for y := y0; y < y1; y++ {
					for x := x0; x < x1; x++ {
						r, g, bl, a := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
						sr += uint64(r >> 8)
						sg += uint64(g >> 8)
						sb += uint64(bl >> 8)
						sa += uint64(a >> 8)
						n++
					}
				}
			}
			if n == 0 {
				n = 1
			}
			i := dy*dst.Stride + dx*4
			dst.Pix[i] = uint8(sr / n)
			dst.Pix[i+1] = uint8(sg / n)
			dst.Pix[i+2] = uint8(sb / n)
			dst.Pix[i+3] = uint8(sa / n)
		}
	}
	return dst
}

// EncodeJPEG 把图像编成 JPEG 字节，供送审 VLM 使用。
//
// 选 JPEG 而不是 PNG：1024 宽的截图 PNG 约 1.5MB、JPEG(q88) 约 130KB，
// 编码与传输都快一个量级，而界面判读并不需要无损。
// quality<=0 时用 88（实测在清晰度与体积间比较平衡）。
func EncodeJPEG(img image.Image, quality int) ([]byte, error) {
	if img == nil {
		return nil, nil
	}
	if quality <= 0 || quality > 100 {
		quality = 88
	}
	// JPEG 不支持 alpha，先铺到不透明 RGBA 再编码，避免透明区域变黑
	if _, ok := img.(*image.RGBA); !ok {
		img = toOpaque(img)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func toOpaque(img image.Image) image.Image {
	b := img.Bounds()
	dst := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			i := dst.PixOffset(x, y)
			dst.Pix[i] = uint8(r >> 8)
			dst.Pix[i+1] = uint8(g >> 8)
			dst.Pix[i+2] = uint8(bl >> 8)
			dst.Pix[i+3] = 255
		}
	}
	return dst
}

// MeanGray 返回灰度均值，用于判断画面是否有变化（复用 capture 的语义）。
func MeanGray(img image.Image) uint8 {
	if img == nil {
		return 0
	}
	b := img.Bounds()
	if b.Dx() == 0 || b.Dy() == 0 {
		return 0
	}
	var sum uint64
	var n uint64
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.GrayModel.Convert(img.At(x, y)).(color.Gray)
			sum += uint64(c.Y)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return uint8(sum / n)
}
