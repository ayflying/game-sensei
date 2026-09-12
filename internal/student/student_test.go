package student

import (
	"image"
	"testing"
)

// fakeWeights 用可预测的模式构造权重：全零卷积核+偏置=正数 -> GAP 输出全等，
// 主要验证前向链路各层尺寸衔接不 panic、输出维度正确。
func fakeNet(t *testing.T) *Net {
	t.Helper()
	n := &Net{hidden: 64}
	zeros := func(sz int) []float32 {
		v := make([]float32, sz)
		return v
	}
	ones := func(sz int) []float32 {
		v := make([]float32, sz)
		for i := range v {
			v[i] = 0.01
		}
		return v
	}
	n.conv1W, n.conv1B = ones(8*1*9), ones(8)
	n.conv2W, n.conv2B = ones(16*8*9), ones(16)
	n.conv3W, n.conv3B = ones(24*16*9), ones(24)
	n.fcW, n.fcB = ones(64*24), zeros(64)
	n.clsW, n.clsB = ones(8*64), zeros(8)
	n.coordW, n.coordB = ones(2*64), zeros(2)
	return n
}

func TestForward尺寸衔接(t *testing.T) {
	n := fakeNet(t)
	x := make([]float32, InH*InW)
	for i := range x {
		x[i] = 0.5
	}
	logits, coords := n.forward(x)
	if len(logits) != 8 {
		t.Fatalf("logits=%d, 期望 8", len(logits))
	}
	if len(coords) != 2 {
		t.Fatalf("coords=%d, 期望 2", len(coords))
	}
	for i, c := range coords {
		if c < 0 || c > 1 {
			t.Fatalf("coords[%d]=%v 超出 0~1", i, c)
		}
	}
}

func TestDecide输出合法类别(t *testing.T) {
	n := fakeNet(t)
	frame := image.NewGray(image.Rect(0, 0, 427, 782)) // 竖屏原始尺寸
	for i := range frame.Pix {
		frame.Pix[i] = uint8(i % 256)
	}
	act, err := n.Decide(frame)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range Classes {
		if c == act.Class {
			found = true
		}
	}
	if !found {
		t.Fatalf("非法类别: %q", act.Class)
	}
	if act.X < 0 || act.X > 1 || act.Y < 0 || act.Y > 1 {
		t.Fatalf("坐标越界: (%v,%v)", act.X, act.Y)
	}
}

func TestPreprocess横竖屏一致(t *testing.T) {
	// 同一内容不同长宽比的帧，中心裁剪后应落到相同输入尺寸
	h := image.NewGray(image.Rect(0, 0, 800, 600))
	v := image.NewGray(image.Rect(0, 0, 427, 782))
	a := preprocess(h)
	b := preprocess(v)
	if len(a) != InH*InW || len(b) != InH*InW {
		t.Fatalf("preprocess 输出尺寸错误: %d / %d", len(a), len(b))
	}
}
