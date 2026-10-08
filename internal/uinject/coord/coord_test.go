package coord

import "testing"

func TestNormalizeRot_只接受四个方向(t *testing.T) {
	cases := map[int]int{
		0: 0, 90: 90, 180: 180, 270: 270,
		360: 0, -90: 270, 450: 90, 45: 0, 999: 0,
	}
	for in, want := range cases {
		if got := NormalizeRot(in); got != want {
			t.Errorf("NormalizeRot(%d) = %d，期望 %d", in, got, want)
		}
	}
}

func TestRotatedSize_横屏互换宽高(t *testing.T) {
	cases := []struct {
		rot  int
		w, h int
	}{
		{0, 1080, 2400},
		{180, 1080, 2400},
		{90, 2400, 1080},
		{270, 2400, 1080},
	}
	for _, c := range cases {
		w, h := RotatedSize(1080, 2400, c.rot)
		if w != c.w || h != c.h {
			t.Errorf("RotatedSize(rot=%d) = %dx%d，期望 %dx%d", c.rot, w, h, c.w, c.h)
		}
	}
}

// 旋转 0 度时是恒等映射，且四角分别落在原始值域的四角。
func TestRotate_零度是恒等映射(t *testing.T) {
	cases := []struct {
		x, y         int
		wantX, wantY int32
	}{
		{0, 0, 0, 0},
		{1080, 0, 1080, 0},
		{0, 2400, 0, 2400},
		{1080, 2400, 1080, 2400},
		{540, 1200, 540, 1200},
	}
	for _, c := range cases {
		gx, gy := Rotate(0, 1080, 2400, 1080, 2400, c.x, c.y)
		if gx != c.wantX || gy != c.wantY {
			t.Errorf("Rotate(0, %d, %d) = (%d, %d)，期望 (%d, %d)", c.x, c.y, gx, gy, c.wantX, c.wantY)
		}
	}
}

// 横屏（rot=90，屏幕 2400×1080）时原始坐标的横轴对应屏幕的纵轴：
// 屏幕左上角 (0,0) → 原始右上角 (rx, 0)；屏幕右下角 (2400,1080) → 原始左下角 (0, ry)。
func TestRotate_九十度横屏轴向互换(t *testing.T) {
	const (
		rot    = 90
		w, h   = 2400, 1080
		rx, ry = 1080, 2400
	)
	cases := []struct {
		name         string
		x, y         int
		wantX, wantY int32
	}{
		{"左上角", 0, 0, 1080, 0},
		{"右下角", 2400, 1080, 0, 2400},
		{"屏幕正中", 1200, 540, 540, 1200},
		{"右上角", 2400, 0, 1080, 2400},
		{"左下角", 0, 1080, 0, 0},
	}
	for _, c := range cases {
		gx, gy := Rotate(rot, w, h, rx, ry, c.x, c.y)
		if gx != c.wantX || gy != c.wantY {
			t.Errorf("%s: Rotate(90, %d, %d) = (%d, %d)，期望 (%d, %d)", c.name, c.x, c.y, gx, gy, c.wantX, c.wantY)
		}
	}
}

func TestRotate_一百八十度对角翻转(t *testing.T) {
	gx, gy := Rotate(180, 1080, 2400, 1080, 2400, 0, 0)
	if gx != 1080 || gy != 2400 {
		t.Errorf("Rotate(180, 0, 0) = (%d, %d)，期望 (1080, 2400)", gx, gy)
	}
}

func TestRotate_越界输入被夹到值域内(t *testing.T) {
	gx, gy := Rotate(0, 1080, 2400, 1080, 2400, 99999, -50)
	if gx != 1080 || gy != 0 {
		t.Errorf("越界输入 = (%d, %d)，期望 (1080, 0)", gx, gy)
	}
}

func TestRotate_非法尺寸返回零点而不是panic(t *testing.T) {
	gx, gy := Rotate(0, 0, 2400, 1080, 2400, 10, 10)
	if gx != 0 || gy != 0 {
		t.Errorf("非法尺寸 = (%d, %d)，期望 (0, 0)", gx, gy)
	}
}

func TestInferRot_按键屏横竖推断(t *testing.T) {
	if got := InferRot(1080, 2400); got != 0 {
		t.Errorf("竖屏推断 = %d，期望 0", got)
	}
	if got := InferRot(2400, 1080); got != 90 {
		t.Errorf("横屏推断 = %d，期望 90", got)
	}
}
