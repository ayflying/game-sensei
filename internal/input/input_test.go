//go:build windows

package input

import (
	"errors"
	"testing"
	"time"

	"github.com/ayflying/game-sensei/internal/agent"
)

// keyAction 构造一个「按住某键 dur 毫秒」的动作。
func keyAction(code string, ms int) agent.Action {
	return agent.Action{Kind: agent.ActionKey, Code: code, Dur: time.Duration(ms) * time.Millisecond}
}

// 键名表是「基础操作能不能覆盖不同游戏」的关键：没有这张表，
// PC 端就只能点四个方向键，玩不了任何真实游戏（跳跃、交互、背包全要按键）。
func TestKeyToVK(t *testing.T) {
	cases := []struct {
		code string
		want uint16
	}{
		// WASD：PC 游戏移动的事实标准
		{"w", 0x57}, {"a", 0x41}, {"s", 0x53}, {"d", 0x44},
		// 大小写不敏感
		{"W", 0x57}, {"Shift", 0x10},
		// 界面导航
		{"space", 0x20}, {"空格", 0x20},
		{"enter", 0x0D}, {"回车", 0x0D},
		{"esc", 0x1B},
		{"tab", 0x09},
		// 方向键与中文别名
		{"up", 0x26}, {"down", 0x28}, {"left", 0x25}, {"right", 0x27},
		{"上", 0x26}, {"左", 0x25},
		// 数字快捷栏
		{"1", 0x31}, {"9", 0x39},
		// 功能键
		{"f1", 0x70}, {"f12", 0x7B},
	}
	for _, c := range cases {
		if got := KeyToVK(c.code); got != c.want {
			t.Errorf("KeyToVK(%q) = 0x%02X, 期望 0x%02X", c.code, got, c.want)
		}
	}
}

func TestKeyToVK_未知键返回零(t *testing.T) {
	for _, code := range []string{"", "不存在的键", "ctrl+alt+del", "xyzw"} {
		if got := KeyToVK(code); got != 0 {
			t.Errorf("KeyToVK(%q) 应返回 0，实际 0x%02X", code, got)
		}
	}
}

// 中文别名不能被小写化过程破坏（lowerASCII 只动 ASCII 大写）。
func TestKeyToVK_中文别名不受大小写处理影响(t *testing.T) {
	if KeyToVK("空格") != 0x20 {
		t.Error("中文别名「空格」应能识别")
	}
	if KeyToVK("回车") != 0x0D {
		t.Error("中文别名「回车」应能识别")
	}
}

func TestPixel_归一化换算(t *testing.T) {
	a := NewActuator(false)
	a.Screen = func() (int, int, error) { return 2608, 1200, nil }

	cases := []struct {
		nx, ny float64
		wx, wy int
	}{
		{0, 0, 0, 0},
		{0.5, 0.5, 1304, 600},
		{1, 1, 2607, 1199}, // 右下角夹到 w-1/h-1，不能越界
		{0.8, 0.81, 2086, 972},
	}
	for _, c := range cases {
		x, y, err := a.pixel(c.nx, c.ny)
		if err != nil {
			t.Fatalf("pixel(%.2f,%.2f) 报错: %v", c.nx, c.ny, err)
		}
		if x != c.wx || y != c.wy {
			t.Errorf("pixel(%.2f,%.2f) = %d,%d, 期望 %d,%d", c.nx, c.ny, x, y, c.wx, c.wy)
		}
	}
}

func TestPixel_越界夹紧(t *testing.T) {
	a := NewActuator(false)
	a.Screen = func() (int, int, error) { return 100, 50, nil }
	x, y, err := a.pixel(-0.5, 1.7)
	if err != nil {
		t.Fatal(err)
	}
	if x != 0 || y != 49 {
		t.Errorf("越界坐标应夹到边界，实际 %d,%d", x, y)
	}
}

// 没注入屏幕尺寸时必须明确报错，而不是点到 (0,0) 去——
// 静默点到屏幕左上角会莫名其妙触发界面上的东西。
func TestPixel_未注入屏幕应报错(t *testing.T) {
	a := NewActuator(false)
	if _, _, err := a.pixel(0.5, 0.5); err == nil {
		t.Error("未注入 Screen 时应报错")
	}
}

func TestPixel_屏幕尺寸非法应报错(t *testing.T) {
	a := NewActuator(false)
	a.Screen = func() (int, int, error) { return 0, 0, nil }
	if _, _, err := a.pixel(0.5, 0.5); err == nil {
		t.Error("屏幕尺寸为 0 时应报错")
	}
	a.Screen = func() (int, int, error) { return 0, 0, errors.New("boom") }
	if _, _, err := a.pixel(0.5, 0.5); err == nil {
		t.Error("获取屏幕尺寸失败时应把错误透出")
	}
}

// dry-run 下不能真的按下任何键，也不能留下「按住的键」。
func TestDryRun_不留按键状态(t *testing.T) {
	a := NewActuator(false)
	if err := a.Apply(keyAction("w", 1000)); err != nil {
		t.Fatalf("dry-run 下 Apply 不该报错: %v", err)
	}
	if keys := a.PressedKeys(); len(keys) != 0 {
		t.Errorf("dry-run 不该留下按住的键: %v", keys)
	}
}

// ReleaseAll 必须幂等：退出路径可能被调用多次（正常退出 + 信号处理）。
func TestReleaseAll_幂等(t *testing.T) {
	a := NewActuator(false)
	a.ReleaseAll()
	a.ReleaseAll()
	if keys := a.PressedKeys(); len(keys) != 0 {
		t.Errorf("ReleaseAll 后不该有按住的键: %v", keys)
	}
}

func TestNewActuator_默认不发送(t *testing.T) {
	if NewActuator(false).Live {
		t.Error("NewActuator(false).Live 应为 false（安全默认）")
	}
	if !NewActuator(true).Live {
		t.Error("NewActuator(true).Live 应为 true")
	}
}
