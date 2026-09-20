package vision

import (
	"image"
	"image/color"
	"testing"
)

// makeZoomQuad 造一张 2x2 四色图，用于验证放大后的采样位置。
func makeZoomQuad() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 200, A: 255})
	img.Set(1, 0, color.RGBA{G: 200, A: 255})
	img.Set(0, 1, color.RGBA{B: 200, A: 255})
	img.Set(1, 1, color.RGBA{R: 200, G: 200, B: 200, A: 255})
	return img
}

func TestZoomSize(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 100, 50))
	got := Zoom(src, 3)
	if b := got.Bounds(); b.Dx() != 300 || b.Dy() != 150 {
		t.Fatalf("放大尺寸不对: %v", b)
	}

	// factor<=1 原样返回：调用方不必自己判断要不要放大
	if Zoom(src, 1) != image.Image(src) {
		t.Error("factor=1 应原样返回")
	}
	if Zoom(src, 0.5) != image.Image(src) {
		t.Error("factor<1 应原样返回（缩小交给 Downscale）")
	}
	if Zoom(nil, 2) != nil {
		t.Error("nil 图应返回 nil")
	}
}

// TestZoomCorner 验证「中心对齐」的采样映射。
//
// 这是最容易写错的一处：若按左上角对齐（x = dx/factor），整幅图会向右下
// 偏半个源像素；放大 3 倍再映射回原图坐标时，就是系统性偏移几个像素。
// 中心对齐下，2x2 放大 4 倍后四角应各自贴近原来的四个像素。
func TestZoomCorner(t *testing.T) {
	got := Zoom(makeZoomQuad(), 4)
	if b := got.Bounds(); b.Dx() != 8 || b.Dy() != 8 {
		t.Fatalf("尺寸不对: %v", b)
	}
	rgba := got.(*image.RGBA)
	cases := []struct {
		x, y int
		want color.RGBA
	}{
		{0, 0, color.RGBA{R: 200, A: 255}},
		{7, 0, color.RGBA{G: 200, A: 255}},
		{0, 7, color.RGBA{B: 200, A: 255}},
		{7, 7, color.RGBA{R: 200, G: 200, B: 200, A: 255}},
	}
	for _, c := range cases {
		r, g, b, _ := rgba.At(c.x, c.y).RGBA()
		got8 := color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 255}
		// 允许插值带来的少量串色（角点处仍有 1/8 的邻域权重）
		if diff(got8.R, c.want.R) > 40 || diff(got8.G, c.want.G) > 40 || diff(got8.B, c.want.B) > 40 {
			t.Errorf("(%d,%d) 得到 %v，期望接近 %v", c.x, c.y, got8, c.want)
		}
	}
}

func diff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}
