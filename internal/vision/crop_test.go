package vision

import (
	"image"
	"image/color"
	"testing"
)

func TestParseRect(t *testing.T) {
	ok, err := ParseRect(" 10,20,110,220 ", 900, 1600)
	if err != nil {
		t.Fatalf("正常区域不该报错: %v", err)
	}
	if ok != image.Rect(10, 20, 110, 220) {
		t.Fatalf("解析结果不对: %v", ok)
	}

	// 越界必须报错：裁剪错位置会让 OCR 结果悄悄指到别的区域，
	// 那种错误在结果里看不出来，比直接失败危险得多。
	if _, err := ParseRect("0,0,901,1600", 900, 1600); err == nil {
		t.Error("超出宽度应报错")
	}
	if _, err := ParseRect("0,0,900,1601", 900, 1600); err == nil {
		t.Error("超出高度应报错")
	}
	for _, bad := range []string{"", "1,2,3", "1,2,3,4,5", "a,b,c,d", "10,10,10,20", "10,20,5,30", "-1,0,10,10"} {
		if _, err := ParseRect(bad, 900, 1600); err == nil {
			t.Errorf("%q 应该报错", bad)
		}
	}
}

func TestCrop(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 10, 10))
	src.Set(3, 4, color.RGBA{R: 255, A: 255})

	got := Crop(src, image.Rect(2, 3, 6, 8))
	if b := got.Bounds(); b.Dx() != 4 || b.Dy() != 5 {
		t.Fatalf("裁剪尺寸不对: %v", b)
	}
	// 原图 (3,4) 在新图里是 (1,1)
	r, _, _, _ := got.At(1, 1).RGBA()
	if r>>8 != 255 {
		t.Errorf("裁剪后像素没跟过来: r=%d", r>>8)
	}

	// 与原图不共享像素：改新图不该动到原图
	got.(*image.RGBA).Set(1, 1, color.RGBA{B: 255, A: 255})
	r2, _, _, _ := src.At(3, 4).RGBA()
	if r2>>8 != 255 {
		t.Error("裁剪结果与原图共享了像素")
	}

	if empty := Crop(src, image.Rect(100, 100, 110, 110)); empty.Bounds().Dx() != 0 {
		t.Error("完全越界的裁剪应返回空图")
	}
	if Crop(nil, image.Rect(0, 0, 1, 1)) != nil {
		t.Error("nil 图应返回 nil")
	}
}
