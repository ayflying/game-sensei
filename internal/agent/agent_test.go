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

// 规则学生输出的必须是 **L1 语义动作**（方向移动），而不是具体按键：
// 早期它直接给 key:left/right，PC 上碰巧能用（方向键），
// 到 Android 就被翻成 KEYCODE_LEFT 这种无效键码。
// 这条测试把「学生也走 L1」钉死——它是跨平台复用的前提。
func TestRuleActorPrefersBrighterSide(t *testing.T) {
	a := NewRule()
	// 右半更亮 -> 应朝右移动
	act, err := a.Decide(makeGray(100, 50, 10, 200))
	if err != nil {
		t.Fatal(err)
	}
	if act.Kind != ActionMove {
		t.Fatalf("应输出 L1 的 ActionMove，得到 %+v", act)
	}
	if act.Dir != DirRight {
		t.Fatalf("右亮应朝右，得到 %s", act.Dir)
	}
	if act.Dur <= 0 {
		t.Fatalf("移动指令必须带保持时长，否则等于原地抖：%+v", act)
	}

	// 左半更亮 -> 应朝左移动
	act, _ = a.Decide(makeGray(100, 50, 200, 10))
	if act.Kind != ActionMove || act.Dir != DirLeft {
		t.Fatalf("左亮应朝左，得到 %+v", act)
	}
}

// 左右几乎持平时必须仍然有动作输出（Phase 0 靠它验证延迟链路）。
func TestRuleActor平局也有动作(t *testing.T) {
	a := NewRule()
	for i := 0; i < 4; i++ {
		act, err := a.Decide(makeGray(100, 50, 100, 100))
		if err != nil {
			t.Fatal(err)
		}
		if act.Kind != ActionMove || !act.Dir.IsValid() {
			t.Fatalf("第 %d 次：平局时应输出合法方向移动，得到 %+v", i, act)
		}
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
