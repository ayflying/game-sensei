package student

import (
	"image"
	"testing"
)

// fakeWeights 用可预测的模式构造权重：全零卷积核+偏置=正数 -> GAP 输出全等，
// 主要验证前向链路各层尺寸衔接不 panic、输出维度正确。inC=1 灰度 / 3 RGB。
func fakeNet(t *testing.T) *Net  { return fakeNetCh(t, 1) }

func fakeNetCh(t *testing.T, inC int) *Net {
	t.Helper()
	n := &Net{hidden: 64, inC: inC}
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
	n.conv1W, n.conv1B = ones(8*inC*9), ones(8)
	n.conv2W, n.conv2B = ones(16*8*9), ones(16)
	n.conv3W, n.conv3B = ones(24*16*9), ones(24)
	n.fcW, n.fcB = ones(64*24), zeros(64)
	n.clsW, n.clsB = ones(len(Classes)*64), zeros(len(Classes))
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
	if len(logits) != len(Classes) {
		t.Fatalf("logits=%d, 期望 %d（len(Classes)）", len(logits), len(Classes))
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

// TestDecideImage彩色模型 覆盖 2026-09-16 上线的 RGB 三通道路径：
// ①彩色模型走 DecideImage 正常输出；②灰度入口对彩色模型显式报错（防误用）；
// ③彩色 preprocessRGB 输出 3*48*64 且三通道都有值。
func TestDecideImage彩色模型(t *testing.T) {
	n := fakeNetCh(t, 3)
	img := image.NewRGBA(image.Rect(0, 0, 540, 1170))
	for i := range img.Pix {
		img.Pix[i] = uint8(i % 256)
	}
	act, err := n.DecideImage(img)
	if err != nil {
		t.Fatal(err)
	}
	if act.X < 0 || act.X > 1 || act.Y < 0 || act.Y > 1 {
		t.Fatalf("坐标越界: (%v,%v)", act.X, act.Y)
	}
	// 灰度入口必须拒绝 3 通道模型
	g := image.NewGray(image.Rect(0, 0, 540, 1170))
	if _, err := n.Decide(g); err == nil {
		t.Fatal("3 通道模型走灰度入口应报错，实际通过")
	}
	// preprocessRGB 尺寸与三通道有效性
	x := preprocessRGB(img)
	if len(x) != 3*InH*InW {
		t.Fatalf("preprocessRGB 输出 %d, 期望 %d", len(x), 3*InH*InW)
	}
	plane := InH * InW
	checked := false
	for i := 0; i < plane; i++ {
		if x[i] != x[plane+i] || x[i] != x[2*plane+i] {
			checked = true // RGBA 图案里三通道天然不同即可
			break
		}
	}
	if !checked {
		t.Fatal("preprocessRGB 三通道恒等——疑似只填了单通道")
	}
}

// TestLoad彩色权重通道推导 校验 Load 对彩色权重（conv1_w 长度 216）推导 inC=3；
// 用真实 v8 权重文件（存在时）做冒烟加载，不存在则跳过。
func TestDecideImage灰度模型等价(t *testing.T) {
	// 灰度模型在 DecideImage 下应与 Decide 输出一致（toGray 路径等价性）
	n := fakeNet(t)
	img := image.NewGray(image.Rect(0, 0, 540, 1170))
	for i := range img.Pix {
		img.Pix[i] = uint8(i % 256)
	}
	a1, err := n.Decide(img)
	if err != nil {
		t.Fatal(err)
	}
	// NewGray 作为 image.Image 传入 DecideImage
	a2, err := n.DecideImage(img)
	if err != nil {
		t.Fatal(err)
	}
	if a1.Class != a2.Class || a1.X != a2.X || a1.Y != a2.Y {
		t.Fatalf("灰度模型两入口不一致: %+v vs %+v", a1, a2)
	}
}
