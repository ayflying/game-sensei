package agent

import (
	"math"
	"testing"
)

func TestParseDir_各种写法(t *testing.T) {
	cases := []struct {
		in   string
		want Dir
	}{
		// 英文规范名与大小写/空白
		{"up", DirUp},
		{"UP", DirUp},
		{"  Up  ", DirUp},
		{"up_left", DirUpLeft},
		{"up-left", DirUpLeft},
		{"up left", DirUpLeft},
		{"UpLeft", DirUpLeft},
		// 游戏语汇：第三人称里「前」就是摇杆向上
		{"forward", DirUp},
		{"fwd", DirUp},
		{"back", DirDown},
		{"backward", DirDown},
		// WASD 单字母（按键位语义，不按罗盘缩写）
		{"w", DirUp},
		{"s", DirDown},
		{"a", DirLeft},
		{"d", DirRight},
		{"wa", DirUpLeft},
		{"wd", DirUpRight},
		{"sa", DirDownLeft},
		{"sd", DirDownRight},
		// 罗盘
		{"north", DirUp},
		{"nw", DirUpLeft},
		{"se", DirDownRight},
		// 中文
		{"上", DirUp},
		{"前", DirUp},
		{"左上", DirUpLeft},
		{"前左", DirUpLeft},
		{"左前", DirUpLeft},
		{"右下", DirDownRight},
		{"后右", DirDownRight},
	}
	for _, c := range cases {
		got, err := ParseDir(c.in)
		if err != nil {
			t.Errorf("ParseDir(%q) 意外报错: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseDir(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

func TestParseDir_非法输入(t *testing.T) {
	for _, in := range []string{"", "   ", "towards", "斜着走", "upleftright", "↑"} {
		if got, err := ParseDir(in); err == nil {
			t.Errorf("ParseDir(%q) 应报错，实际得到 %q", in, got)
		}
	}
}

// WASD 单字母与罗盘缩写在 w/d 上语义冲突，这里把取舍钉死：
// 游戏操作语境下 w 必须是「前进」而不是「西」，否则 PC 端全部反向。
func TestParseDir_WASD优先于罗盘(t *testing.T) {
	if d, _ := ParseDir("w"); d != DirUp {
		t.Errorf("w 应解析为 up（WASD 语义），实际 %q", d)
	}
	if d, _ := ParseDir("d"); d != DirRight {
		t.Errorf("d 应解析为 right（WASD 语义），实际 %q", d)
	}
	// 罗盘「西/东」改用全称，避免歧义
	if d, _ := ParseDir("west"); d != DirLeft {
		t.Errorf("west 应解析为 left，实际 %q", d)
	}
	if d, _ := ParseDir("east"); d != DirRight {
		t.Errorf("east 应解析为 right，实际 %q", d)
	}
}

func TestDir_Vector(t *testing.T) {
	cases := []struct {
		d    Dir
		x, y float64
	}{
		{DirUp, 0, -1},   // 屏幕 y 轴向下，所以「上」是 -y
		{DirDown, 0, 1},  //
		{DirLeft, -1, 0}, //
		{DirRight, 1, 0}, //
		{DirUpLeft, -math.Sqrt2 / 2, -math.Sqrt2 / 2},
		{DirDownRight, math.Sqrt2 / 2, math.Sqrt2 / 2},
	}
	for _, c := range cases {
		x, y := c.d.Vector()
		if math.Abs(x-c.x) > 1e-9 || math.Abs(y-c.y) > 1e-9 {
			t.Errorf("%s.Vector() = %.4f,%.4f, 期望 %.4f,%.4f", c.d, x, y, c.x, c.y)
		}
	}
}

// 八个方向的推杆幅度必须一致：斜向若按 1:1 给分量，会比正向多推 41%，
// 游戏里就表现为「斜着走更快」——移动速度不一致会让采集到的示范轨迹失真。
func TestDir_Vector八个方向等长(t *testing.T) {
	for _, d := range AllDirs {
		x, y := d.Vector()
		if l := math.Hypot(x, y); math.Abs(l-1) > 1e-9 {
			t.Errorf("%s 的向量长度 %.6f，期望 1（八向必须等长）", d, l)
		}
	}
}

func TestDir_IsValid(t *testing.T) {
	for _, d := range AllDirs {
		if !d.IsValid() {
			t.Errorf("%q 应合法", d)
		}
	}
	for _, d := range []Dir{"", "forward", "up-left", "UP"} {
		if d.IsValid() {
			t.Errorf("%q 不该被判为合法规范方向", d)
		}
	}
}

func TestDirListZh(t *testing.T) {
	got := DirListZh()
	want := "up|down|left|right|up_left|up_right|down_left|down_right"
	if got != want {
		t.Errorf("DirListZh() = %q, 期望 %q（顺序必须稳定，否则提示词不可复现）", got, want)
	}
}

func TestDir_Zh(t *testing.T) {
	if s := DirUp.Zh(); s == "" || s == string(DirUp) {
		t.Errorf("DirUp.Zh() 应返回中文说明，实际 %q", s)
	}
	if s := Dir("bogus").Zh(); s != "bogus" {
		t.Errorf("未知方向应原样返回，实际 %q", s)
	}
}
