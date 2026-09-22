package android

import (
	"strings"
	"testing"
	"time"
)

// TestParsePowerState_解析真机实测输出 用 MIX 3（Android 10）上
// powerProbeCmd 的真实回显做样例，确保字段名与顺序对得上。
func TestParsePowerState_解析真机实测输出(t *testing.T) {
	out := `brightness=1
brightness_mode=0
off_timeout=1800000
stay_on=7
  mWakefulness=Awake
  mBatteryLevel=74
  mBatteryLevel=74
  mPlugType=2
`
	st := ParsePowerState(out)

	if st.Brightness != 1 {
		t.Errorf("Brightness = %d，期望 1", st.Brightness)
	}
	if st.BrightnessMode != 0 {
		t.Errorf("BrightnessMode = %d，期望 0（手动）", st.BrightnessMode)
	}
	if st.ScreenOffTimeout != 30*time.Minute {
		t.Errorf("ScreenOffTimeout = %s，期望 30m", st.ScreenOffTimeout)
	}
	if st.StayOnPluggedIn != StayOnAll {
		t.Errorf("StayOnPluggedIn = %d，期望 7", st.StayOnPluggedIn)
	}
	if st.Wakefulness != Awake {
		t.Errorf("Wakefulness = %q，期望 %q", st.Wakefulness, Awake)
	}
	if !st.IsAwake() {
		t.Error("IsAwake() = false，期望 true")
	}
	if st.BatteryLevel != 74 {
		t.Errorf("BatteryLevel = %d，期望 74", st.BatteryLevel)
	}
	if st.PlugType != 2 {
		t.Errorf("PlugType = %d，期望 2（USB）", st.PlugType)
	}
}

// TestParsePowerState_熄屏时判定为未唤醒 覆盖省电档生效后的常态。
func TestParsePowerState_熄屏时判定为未唤醒(t *testing.T) {
	st := ParsePowerState("brightness=1\nmWakefulness=Asleep\n")

	if st.Wakefulness != Asleep {
		t.Errorf("Wakefulness = %q，期望 %q", st.Wakefulness, Asleep)
	}
	if st.IsAwake() {
		t.Error("熄屏时 IsAwake() = true，会让自动唤醒失效")
	}
}

// TestParsePowerState_缺字段时给安全默认 覆盖 settings 未设置（回显 null）
// 与 dumpsys 字段缺失两种退化，避免把 -1 当成真实亮度。
func TestParsePowerState_缺字段时给安全默认(t *testing.T) {
	st := ParsePowerState("brightness=null\nbrightness_mode=null\noff_timeout=null\nstay_on=null\n")

	if st.Brightness != -1 {
		t.Errorf("Brightness = %d，期望 -1（未知）", st.Brightness)
	}
	if st.ScreenOffTimeout != 0 {
		t.Errorf("ScreenOffTimeout = %s，期望 0", st.ScreenOffTimeout)
	}
	if st.BatteryLevel != -1 || st.PlugType != -1 {
		t.Errorf("电量/插电类型 = %d/%d，期望均为 -1", st.BatteryLevel, st.PlugType)
	}
	// 没有任何唤醒字段时不能假设亮屏——假设亮屏会让自动唤醒静默失效。
	if st.IsAwake() {
		t.Error("字段全缺时 IsAwake() = true，应保守判为未知/未唤醒")
	}
}

// TestParsePowerState_空值与异常行不panic 覆盖曾被写出的越界隐患：
// "mWakefulness=" 只有键没有值。
func TestParsePowerState_空值与异常行不panic(t *testing.T) {
	for _, out := range []string{"", "\n\n", "mWakefulness=\n", "mWakefulness=   \n", "乱七八糟\n"} {
		st := ParsePowerState(out)
		if st.Brightness != -1 {
			t.Errorf("输入 %q：Brightness = %d，期望 -1", out, st.Brightness)
		}
	}
}

// TestParsePowerState_唤醒状态优先于Interactive 锁住判定优先级：
// 有 mWakefulness 时以它为准，避免老设备退化成误判。
func TestParsePowerState_唤醒状态优先于Interactive(t *testing.T) {
	st := ParsePowerState("mWakefulness=Dozing\n")
	if st.IsAwake() {
		t.Error("Dozing 被误判为亮屏")
	}
	st = ParsePowerState("mWakefulness=Awake\n")
	if !st.IsAwake() {
		t.Error("Awake 被误判为熄屏")
	}
}

func TestClampInt_夹到合法区间(t *testing.T) {
	cases := []struct {
		in, lo, hi, want int
	}{
		{-1, 0, 255, 0},
		{0, 10, 2047, 10},
		{1, 10, 2047, 10},
		{414, 10, 2047, 414},
		{2047, 10, 2047, 2047},
		{9999, 10, 2047, 2047},
		{5, 10, 5, 10}, // hi<lo 时以 lo 为准，不返回越界值
	}
	for _, c := range cases {
		if got := ClampInt(c.in, c.lo, c.hi); got != c.want {
			t.Errorf("ClampInt(%d,%d,%d) = %d，期望 %d", c.in, c.lo, c.hi, got, c.want)
		}
	}
}

// TestParseBrightnessRange_解析MIUI实测值域 锁住值域来源：
// MIX 3（Android 10 / MIUI 12.5）是 10~2047，不是标准 Android 的 0~255。
func TestParseBrightnessRange_解析MIUI实测值域(t *testing.T) {
	out := `  mScreenBrightnessRangeMinimum=10
  mScreenBrightnessRangeMaximum=2047
  mScreenBrightnessDefault=536
  mScreenBrightnessRangeMinimum=10
  mScreenBrightnessRangeMaximum=2047
`
	min, max := ParseBrightnessRange(out)
	if min != 10 || max != 2047 {
		t.Errorf("值域 = [%d,%d]，期望 [10,2047]", min, max)
	}
}

// TestParseBrightnessRange_缺字段时返回零值 便于调用方据此报「设备不暴露值域」。
func TestParseBrightnessRange_缺字段时返回零值(t *testing.T) {
	if min, max := ParseBrightnessRange("无关内容\n"); min != 0 || max != 0 {
		t.Errorf("值域 = [%d,%d]，期望 [0,0]", min, max)
	}
}

// TestBrightnessPercent_跨值域换算 是「不能写死 0~255」的核心证据：
// 同一个 414 在标准值域里超上限、在 MIUI 值域里约 20%。
func TestBrightnessPercent_跨值域换算(t *testing.T) {
	miui := PowerState{Brightness: 414, BrightnessMin: 10, BrightnessMax: 2047}
	if got := miui.BrightnessPercent(); got < 19 || got > 21 {
		t.Errorf("MIUI 值域下 414 的百分比 = %d，期望约 20", got)
	}
	if got := (PowerState{Brightness: 10, BrightnessMin: 10, BrightnessMax: 2047}).BrightnessPercent(); got != 0 {
		t.Errorf("最低亮度百分比 = %d，期望 0", got)
	}
	if got := (PowerState{Brightness: 2047, BrightnessMin: 10, BrightnessMax: 2047}).BrightnessPercent(); got != 100 {
		t.Errorf("最高亮度百分比 = %d，期望 100", got)
	}
	// 值域未知时不得编造百分比
	if got := (PowerState{Brightness: 414, BrightnessMin: -1, BrightnessMax: -1}).BrightnessPercent(); got != -1 {
		t.Errorf("值域未知时百分比 = %d，期望 -1", got)
	}
}

func TestParseBrightnessSpec_绝对值与百分比(t *testing.T) {
	cases := []struct {
		in        string
		wantVal   int
		wantPct   bool
		wantError bool
	}{
		{"100", 100, false, false},
		{"5%", 5, true, false},
		{" 5% ", 5, true, false},
		{"0%", 0, true, false},
		{"120%", 100, true, false}, // 超上限夹到 100
		{"", 0, false, true},
		{"abc", 0, false, true},
		{"-1", 0, false, true}, // 绝对值不能为负
		{"%", 0, false, true},
	}
	for _, c := range cases {
		val, isPct, err := ParseBrightnessSpec(c.in)
		if c.wantError {
			if err == nil {
				t.Errorf("ParseBrightnessSpec(%q) 期望报错，却返回 %d/%v", c.in, val, isPct)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseBrightnessSpec(%q) 意外报错: %v", c.in, err)
			continue
		}
		if val != c.wantVal || isPct != c.wantPct {
			t.Errorf("ParseBrightnessSpec(%q) = (%d,%v)，期望 (%d,%v)", c.in, val, isPct, c.wantVal, c.wantPct)
		}
	}
}

// TestParseKeyguardShowing_锁屏与解锁 用实测的两行做样例。
//
// 实测（MIX 3 / MIUI 12.5）：锁屏时 mCurrentFocus=StatusBar 且
// mDreamingLockscreen=true；解锁后前台是游戏且该字段为 false。
func TestParseKeyguardShowing_锁屏与解锁(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"锁屏", "    mShowingDream=false mDreamingLockscreen=true mDreamingSleepToken=null", true},
		{"解锁", "    mShowingDream=false mDreamingLockscreen=false mDreamingSleepToken=null", false},
		{"空输出", "", false},
		{"无该字段", "  mCurrentFocus=Window{abc u0 StatusBar}", false},
		// 前缀相似的字段不能误判
		{"形近字段", "  mDreamingLockscreenFoo=true", false},
	}
	for _, c := range cases {
		if got := ParseKeyguardShowing(c.in); got != c.want {
			t.Errorf("%s: ParseKeyguardShowing(%q) = %v，期望 %v", c.name, c.in, got, c.want)
		}
	}
}

// TestPowerState_String 只是确保摘要里带上关键字段，便于人读日志。
func TestPowerState_String(t *testing.T) {
	st := PowerState{
		Wakefulness:      Asleep,
		Brightness:       414,
		BrightnessMode:   1,
		BrightnessMin:    10,
		BrightnessMax:    2047,
		ScreenOffTimeout: 2 * time.Minute,
		StayOnPluggedIn:  0,
		BatteryLevel:     74,
		PlugType:         2,
	}
	s := st.String()
	for _, want := range []string{Asleep, "414/2047", "20%", "2m0s", "允许休眠", "74%"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() = %q，缺少 %q", s, want)
		}
	}
}
