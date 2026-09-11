package agent

import (
	"image"
	"image/color"
	"testing"
)

// makeGray 构造 width x height 的灰度图，左右两半分别填充亮度 l/r。
func makeGray(width, height int, l, r uint8) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, width, height))
	mid := width / 2
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			v := l
			if x >= mid {
				v = r
			}
			img.SetGray(x, y, color.Gray{Y: v})
		}
	}
	return img
}

func TestRuleActorPrefersBrighterSide(t *testing.T) {
	a := NewRule()
	// 右半更亮 -> 应向右
	act, err := a.Decide(makeGray(100, 50, 10, 200))
	if err != nil {
		t.Fatal(err)
	}
	if act.Kind != ActionKey || act.Code != "right" {
		t.Fatalf("右亮应输出 right，得到 %+v", act)
	}
	// 左半更亮 -> 应向左
	act, _ = a.Decide(makeGray(100, 50, 200, 10))
	if act.Code != "left" {
		t.Fatalf("左亮应输出 left，得到 %+v", act)
	}
}

func TestRuleActorNilFrame(t *testing.T) {
	act, err := NewRule().Decide(nil)
	if err != nil {
		t.Fatal(err)
	}
	if act.Kind != ActionNone {
		t.Fatalf("nil 帧应输出 ActionNone，得到 %+v", act)
	}
}
