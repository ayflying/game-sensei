package vision

import "image"

// Zoom 把 img 等比放大 factor 倍，双线性插值；factor<=1 或空图原样返回。
//
// 为什么需要它：PP-OCR 的检测/识别网络对字高有下限，真机截图（900x1600）上
// 「价值 1,400」这类小字直接送进去常被漏检或读错；把目标区域先放大 2~3 倍
// 再识别，命中率明显提高——这与「先裁后认」配合，是读数值/价签的标准做法。
//
// 为什么双线性而不是最近邻：最近邻只是复制像素，笔画边缘仍是硬阶梯，
// 检测网络对阶梯不敏感；双线性给出连续灰度过渡，等价于一次轻度低通，
// 小字放大后更接近训练时的字形分布。LANCZOS 质量更好但标准库没有，
// 为它手写窗函数不划算，实测双线性已够用。
func Zoom(img image.Image, factor float64) image.Image {
	if img == nil || factor <= 1 {
		return img
	}
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 {
		return img
	}
	dw := int(float64(sw)*factor + 0.5)
	dh := int(float64(sh)*factor + 0.5)
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	// 目标像素中心反算回源坐标中心：x = (dx+0.5)/factor - 0.5。
	// 用中心对齐而不是左上角对齐，否则整幅图会向右下偏移半个像素，
	// 放大倍数越高偏移越明显，映射回原图坐标时就会系统性偏。
	scale := 1 / factor

	for dy := 0; dy < dh; dy++ {
		// 必须先夹到 [0, sh-1] 再算整数部分与小数权重：第一行反算出来是负数
		// （如 0.25 倍映射下 sy=-0.375），若只夹 int(sy) 而放任 fy<0，
		// 插值就变成**外插**，权重 (1-fy)>1 会把亮像素推过 255 造成过曝白边。
		sy := (float64(dy)+0.5)*scale - 0.5
		if sy < 0 {
			sy = 0
		}
		if sy > float64(sh-1) {
			sy = float64(sh - 1)
		}
		y0 := int(sy)
		fy := sy - float64(y0)
		y1 := y0 + 1
		if y1 > sh-1 {
			y1 = sh - 1
		}
		for dx := 0; dx < dw; dx++ {
			sx := (float64(dx)+0.5)*scale - 0.5
			if sx < 0 {
				sx = 0
			}
			if sx > float64(sw-1) {
				sx = float64(sw - 1)
			}
			x0 := int(sx)
			fx := sx - float64(x0)
			x1 := x0 + 1
			if x1 > sw-1 {
				x1 = sw - 1
			}

			r00, g00, b00 := rgbAt(img, b.Min.X+x0, b.Min.Y+y0)
			r01, g01, b01 := rgbAt(img, b.Min.X+x1, b.Min.Y+y0)
			r10, g10, b10 := rgbAt(img, b.Min.X+x0, b.Min.Y+y1)
			r11, g11, b11 := rgbAt(img, b.Min.X+x1, b.Min.Y+y1)

			i := dst.PixOffset(dx, dy)
			dst.Pix[i] = bilerp(r00, r01, r10, r11, fx, fy)
			dst.Pix[i+1] = bilerp(g00, g01, g10, g11, fx, fy)
			dst.Pix[i+2] = bilerp(b00, b01, b10, b11, fx, fy)
			dst.Pix[i+3] = 255
		}
	}
	return dst
}

// bilerp 是四个角的双线性插值：先横向混合两行，再纵向混合两个结果。
func bilerp(v00, v01, v10, v11 uint8, fx, fy float64) uint8 {
	top := float64(v00)*(1-fx) + float64(v01)*fx
	bottom := float64(v10)*(1-fx) + float64(v11)*fx
	v := top*(1-fy) + bottom*fy
	if v < 0 {
		v = 0
	}
	if v > 255 {
		v = 255
	}
	return uint8(v + 0.5)
}

func rgbAt(img image.Image, x, y int) (uint8, uint8, uint8) {
	r, g, b, _ := img.At(x, y).RGBA()
	return uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)
}
