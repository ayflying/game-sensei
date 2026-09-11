package vision

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"testing"
)

// 造一张 4x4 的图：左半红、右半蓝，便于验证缩放后颜色没串。
func makeQuad() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			i := img.PixOffset(x, y)
			if x < 2 {
				img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 255, 0, 0, 255
			} else {
				img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 0, 0, 255, 255
			}
		}
	}
	return img
}

func TestDownscale不改宽图(t *testing.T) {
	img := makeQuad()
	got := Downscale(img, 8)
	if got.Bounds().Dx() != 4 {
		t.Errorf("原图未超宽时不该缩放，得到宽度 %d", got.Bounds().Dx())
	}
}

func TestDownscale为零返回原图(t *testing.T) {
	img := makeQuad()
	if got := Downscale(img, 0); got.Bounds().Dx() != 4 {
		t.Errorf("maxWidth=0 应原样返回")
	}
}

func TestDownscale保持宽高比(t *testing.T) {
	// 2608x1200 → 宽 1024，高应为 1200*1024/2608 = 471
	img := image.NewRGBA(image.Rect(0, 0, 2608, 1200))
	got := Downscale(img, 1024)
	b := got.Bounds()
	if b.Dx() != 1024 {
		t.Errorf("宽度 %d, 期望 1024", b.Dx())
	}
	if b.Dy() != 471 {
		t.Errorf("高度 %d, 期望 471（保持宽高比）", b.Dy())
	}
}

// 区域平均的核心价值：2x2 缩到 1x1 时，黑与白应混成中间灰，而不是取到某一侧。
func TestDownscale区域平均而非最近邻(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	// (0,0) 黑，(1,0) 白，(0,1) 白，(1,1) 黑 → 平均应约 127
	img.Pix[0], img.Pix[1], img.Pix[2], img.Pix[3] = 0, 0, 0, 255
	img.Pix[4], img.Pix[5], img.Pix[6], img.Pix[7] = 255, 255, 255, 255
	img.Pix[8], img.Pix[9], img.Pix[10], img.Pix[11] = 255, 255, 255, 255
	img.Pix[12], img.Pix[13], img.Pix[14], img.Pix[15] = 0, 0, 0, 255

	got := Downscale(img, 1)
	if got.Bounds().Dx() != 1 {
		t.Fatalf("宽度 %d, 期望 1", got.Bounds().Dx())
	}
	r, _, _, _ := got.At(0, 0).RGBA()
	rv := int(r >> 8)
	if rv < 120 || rv > 135 {
		t.Errorf("区域平均结果 R=%d, 期望约 127（取平均而非最近邻）", rv)
	}
}

func TestDownscale非RGBA类型也支持(t *testing.T) {
	// 走 At() 慢路径，确认不会 panic 且尺寸正确
	g := image.NewGray(image.Rect(0, 0, 100, 50))
	got := Downscale(g, 10)
	if got.Bounds().Dx() != 10 || got.Bounds().Dy() != 5 {
		t.Errorf("尺寸 %v, 期望 10x5", got.Bounds())
	}
}

func TestDownscale空图(t *testing.T) {
	if got := Downscale(nil, 100); got != nil {
		t.Error("nil 输入应返回 nil")
	}
}

func TestEncodeJPEG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	b, err := EncodeJPEG(img, 88)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("编码结果为空")
	}
	// 解回来确认是合法 JPEG
	back, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if back.Bounds().Dx() != 64 {
		t.Errorf("解回尺寸 %v", back.Bounds())
	}
}

func TestEncodeJPEG质量默认值(t *testing.T) {
	img := makeQuad()
	b1, _ := EncodeJPEG(img, 0)  // 走默认 88
	b2, _ := EncodeJPEG(img, 88) // 显式 88
	if !bytes.Equal(b1, b2) {
		t.Error("quality<=0 时应与显式 88 一致")
	}
}

// JPEG 不支持 alpha；带透明的图若直接编码，透明区会变黑，视觉上像有黑洞。
func TestEncodeJPEG透明图不塌成黑色(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	// 全透明但底色是白
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 255, 255, 255, 0
	}
	b, err := EncodeJPEG(img, 90)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	back, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	r, g, bl, _ := back.At(2, 2).RGBA()
	if r>>8 < 200 || g>>8 < 200 || bl>>8 < 200 {
		t.Errorf("透明区未铺白，得到 R=%d G=%d B=%d", r>>8, g>>8, bl>>8)
	}
}

func TestEncodeJPEG空图(t *testing.T) {
	b, err := EncodeJPEG(nil, 0)
	if err != nil || b != nil {
		t.Errorf("nil 输入应返回 nil, nil；得到 %v, %v", b, err)
	}
}

func TestMeanGray(t *testing.T) {
	white := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := 0; i < len(white.Pix); i += 4 {
		white.Pix[i], white.Pix[i+1], white.Pix[i+2], white.Pix[i+3] = 255, 255, 255, 255
	}
	if got := MeanGray(white); got != 255 {
		t.Errorf("全白 MeanGray=%d, 期望 255", got)
	}
	black := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := 3; i < len(black.Pix); i += 4 {
		black.Pix[i] = 255
	}
	if got := MeanGray(black); got != 0 {
		t.Errorf("全黑 MeanGray=%d, 期望 0", got)
	}
	if got := MeanGray(nil); got != 0 {
		t.Errorf("nil MeanGray=%d, 期望 0", got)
	}
}

// 确认 PNG 解码（ADB 路径的落地类型）能被正常缩放
func TestDownscale真实PNG解码结果(t *testing.T) {
	src := makeQuad()
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatalf("PNG 编码失败: %v", err)
	}
	decoded, err := png.Decode(&buf)
	if err != nil {
		t.Fatalf("PNG 解码失败: %v", err)
	}
	got := Downscale(decoded, 2)
	if got.Bounds().Dx() != 2 {
		t.Errorf("宽度 %d, 期望 2", got.Bounds().Dx())
	}
	// 左半红右半蓝，缩到 2 宽后应仍是一红一蓝
	r0, g0, b0, _ := got.At(0, 0).RGBA()
	r1, g1, b1, _ := got.At(1, 0).RGBA()
	if r0>>8 < 200 || g0>>8 > 60 || b0>>8 > 60 {
		t.Errorf("左半应偏红，得到 %d,%d,%d", r0>>8, g0>>8, b0>>8)
	}
	if b1>>8 < 200 || r1>>8 > 60 || g1>>8 > 60 {
		t.Errorf("右半应偏蓝，得到 %d,%d,%d", r1>>8, g1>>8, b1>>8)
	}
}
