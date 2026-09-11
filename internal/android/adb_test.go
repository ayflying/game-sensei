package android

import (
	"image"
	"image/color"
	"testing"
	"time"

	"github.com/ayflying/game-sensei/internal/agent"
)

// 灰度转换：降采样后的尺寸应与 capture 包的等比规则一致。
func TestToGrayDownsampled(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 400, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 400; x++ {
			// 左半黑、右半白，便于验证采样与灰度权重
			c := uint8(0)
			if x >= 200 {
				c = 255
			}
			src.SetNRGBA(x, y, color.NRGBA{R: c, G: c, B: c, A: 255})
		}
	}

	g := ToGrayDownsampled(src, 100)
	if got, want := g.Bounds().Dx(), 100; got != want {
		t.Fatalf("降采样宽度 = %d, 期望 %d", got, want)
	}
	if got, want := g.Bounds().Dy(), 50; got != want {
		t.Fatalf("降采样高度 = %d, 期望 %d", got, want)
	}

	if v := g.GrayAt(10, 25).Y; v != 0 {
		t.Errorf("左侧像素 = %d, 期望 0", v)
	}
	if v := g.GrayAt(90, 25).Y; v != 255 {
		t.Errorf("右侧像素 = %d, 期望 255", v)
	}
}

// downWidth<=0 或大于原宽时应保持原尺寸。
func TestToGrayDownsampledNoShrink(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 64, 32))
	if g := ToGrayDownsampled(src, 0); g.Bounds().Dx() != 64 || g.Bounds().Dy() != 32 {
		t.Errorf("down=0 时应保持原尺寸, 得到 %v", g.Bounds())
	}
	if g := ToGrayDownsampled(src, 128); g.Bounds().Dx() != 64 {
		t.Errorf("down 大于原宽时应保持原尺寸, 得到 %v", g.Bounds())
	}
}

// BT.601 权重：绿色对亮度贡献最大。
func TestLumaWeights(t *testing.T) {
	if luma(255, 0, 0) != 76 {
		t.Errorf("红通道亮度 = %d, 期望 76", luma(255, 0, 0))
	}
	if luma(0, 255, 0) != 149 {
		t.Errorf("绿通道亮度 = %d, 期望 149", luma(0, 255, 0))
	}
	if luma(0, 0, 255) != 29 {
		t.Errorf("蓝通道亮度 = %d, 期望 29", luma(0, 0, 255))
	}
}

// 前台应用解析：兼容 mCurrentFocus 与 mResumedActivity 两种格式。
func TestParseForegroundPkg(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "mCurrentFocus",
			in:   "  mCurrentFocus=Window{773d6db u0 com.tencent.nrc/com.epicgames.ue4.GameActivity}",
			want: "com.tencent.nrc",
		},
		{
			name: "mFocusedApp",
			in:   "  mFocusedApp=ActivityRecord{67272624 u0 com.miui.home/.launcher.Launcher t2}",
			want: "com.miui.home",
		},
		{
			name: "mResumedActivity",
			in:   "    mResumedActivity: ActivityRecord{abc123 u0 com.tencent.nrc/.MainActivity t5}",
			want: "com.tencent.nrc",
		},
		{
			name: "无焦点行",
			in:   "WINDOW MANAGER DISPLAY CONTENTS (dumpsys window displays)",
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseForegroundPkg(c.in); got != c.want {
				t.Errorf("parseForegroundPkg = %q, 期望 %q", got, c.want)
			}
		})
	}
}

// 主 Activity 解析：跳过 resolve-activity 的元信息行。
func TestParseComponent(t *testing.T) {
	out := "priority=0 preferredOrder=0 match=0x10800000 specificIndex=-1 isDefault=false\n" +
		"com.tencent.nrc/com.tencent.gcloud.msdk.core.policy.MSDKPolicyV3Activity"
	if got, want := parseComponent(out), "com.tencent.nrc/com.tencent.gcloud.msdk.core.policy.MSDKPolicyV3Activity"; got != want {
		t.Errorf("parseComponent = %q, 期望 %q", got, want)
	}
	if got := parseComponent("priority=0 isDefault=false"); got != "" {
		t.Errorf("无组件行时应返回空, 得到 %q", got)
	}
}

// 按键名归一化。
func TestNormalizeKeyCode(t *testing.T) {
	cases := map[string]string{
		"back":         "KEYCODE_BACK",
		"BACK":         "KEYCODE_BACK",
		"KEYCODE_HOME": "KEYCODE_HOME",
		"app_switch":   "KEYCODE_APP_SWITCH",
		"4":            "4",
		"":             "",
	}
	for in, want := range cases {
		if got := normalizeKeyCode(in); got != want {
			t.Errorf("normalizeKeyCode(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

// 手势时长：未指定时取默认值，指定时保留。
func TestDurMs(t *testing.T) {
	if got := durMs(0, DefaultSwipeMs); got != DefaultSwipeMs {
		t.Errorf("未指定时长应取默认值, 得到 %d", got)
	}
	if got := durMs(1500*time.Millisecond, DefaultSwipeMs); got != 1500 {
		t.Errorf("指定时长应保留, 得到 %d", got)
	}
}

// 动作的可读输出：日志与轨迹里要能一眼看懂决策内容。
func TestActionString(t *testing.T) {
	cases := []struct {
		act  agent.Action
		want string
	}{
		{agent.Action{Kind: agent.ActionTap, Nx: 0.5, Ny: 0.25}, "tap:0.500,0.250"},
		{agent.Action{Kind: agent.ActionKey, Code: "back"}, "key:back"},
		{agent.Action{Kind: agent.ActionNone}, "none"},
		{
			agent.Action{Kind: agent.ActionSwipe, Nx: 0.2, Ny: 0.8, Nx2: 0.5, Ny2: 0.3, Dur: 300 * time.Millisecond},
			"swipe:0.200,0.800->0.500,0.300/300ms",
		},
		{
			agent.Action{Kind: agent.ActionLongPress, Nx: 0.5, Ny: 0.5, Dur: 800 * time.Millisecond},
			"hold:0.500,0.500/800ms",
		},
	}
	for _, c := range cases {
		if got := c.act.String(); got != c.want {
			t.Errorf("Action.String = %q, 期望 %q", got, c.want)
		}
	}
}
