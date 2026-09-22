package android

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// 省电与休眠控制（2026-09-22）。
//
// 背景：安卓设备跑批时通常被设成「插电永不休眠」，代价是屏幕长期点亮。
// 真机连跑一晚很容易掉到没电。省电方向应该反过来——把亮度压到最低、
// 允许系统按时熄屏，由程序在需要抓帧时自己唤醒（安酱 2026-09-22 要求）。
//
// 为什么唤醒必须挂在抓帧入口而不是只做成一条命令：**屏幕熄灭时
// screencap 抓到的是纯黑图**。这不会报错，只会让所有基于画面的判据
// 静默失效（「判不出来」而不是「报失败」），属于最难定位的一类故障。
// 所以 Screenshot 里会先做一次带节流的 ensureAwakeThrottled。

// 设备唤醒状态（dumpsys power 的 mWakefulness）。
const (
	Awake  = "Awake"
	Asleep = "Asleep"
	Dozing = "Dozing"
)

// 插电保持唤醒的位掩码（settings global stay_on_while_plugged_in）。
const (
	StayOnAC       = 1
	StayOnUSB      = 2
	StayOnWireless = 4
	StayOnAll      = StayOnAC | StayOnUSB | StayOnWireless
)

// autoWakeInterval 是抓帧前自动检查休眠的最小间隔。
//
// 每次抓帧都查一次 dumpsys power 会拖慢安卓链路（实时回路 5FPS）；
// 而熄屏是秒级事件，5 秒的检查窗口足够及时。
const autoWakeInterval = 5 * time.Second

// powerProbeCmd 一次 shell 取齐全部电源状态，避免多次往返。
//
// 用 key=value 输出而非裸值：settings get 对未设置的项会打印 "null"，
// 裸值形式无法区分「取到 null」与「行数错位」。
const powerProbeCmd = `echo "brightness=$(settings get system screen_brightness)"; ` +
	`echo "brightness_mode=$(settings get system screen_brightness_mode)"; ` +
	`echo "off_timeout=$(settings get system screen_off_timeout)"; ` +
	`echo "stay_on=$(settings get global stay_on_while_plugged_in)"; ` +
	`dumpsys power | grep -E "mWakefulness=|mBatteryLevel=|mPlugType=" | head -4`

// PowerState 是设备当前的电源与显示省电状态。
type PowerState struct {
	Wakefulness      string        // Awake / Asleep / Dozing；取不到为空
	Interactive      bool          // 屏幕是否点亮可交互
	Brightness       int           // 绝对值；-1 表示取不到
	BrightnessMode   int           // 0=手动 1=自动
	BrightnessMin    int           // 设备亮度值域下限；-1 表示未知
	BrightnessMax    int           // 设备亮度值域上限；-1 表示未知
	ScreenOffTimeout time.Duration // 熄屏超时
	StayOnPluggedIn  int           // 位掩码，0=允许休眠，7=插电永不休眠
	BatteryLevel     int           // 电量百分比；-1 表示取不到
	PlugType         int           // 0=未插电 1=AC 2=USB 3=无线；-1 表示取不到
}

// BrightnessPercent 把当前亮度换算成值域内的百分比；范围未知时返回 -1。
//
// 为什么必须有百分比：亮度绝对值不可跨设备比较——Android 标准是 0~255，
// 而 MIUI 实测是 10~2047（MIX 3 / Android 10）。同一个「414」在前者是超上限、
// 在后者是约 20%，只看绝对值会得出相反结论。
//
// 用四舍五入而非截断：截断会把 19.8% 显示成 19%，用于「是否已压到目标档」
// 的判定时容易与阈值擦边。
func (p PowerState) BrightnessPercent() int {
	if p.Brightness < 0 || p.BrightnessMax <= p.BrightnessMin {
		return -1
	}
	span := p.BrightnessMax - p.BrightnessMin
	return ClampInt(((p.Brightness-p.BrightnessMin)*100+span/2)/span, 0, 100)
}

// IsAwake 报告屏幕是否点亮。
//
// 优先信 mWakefulness；老设备取不到该字段时退化为 Interactive。
func (p PowerState) IsAwake() bool {
	if p.Wakefulness != "" {
		return p.Wakefulness == Awake
	}
	return p.Interactive
}

// String 返回一行人类可读摘要（供命令直接打印）。
func (p PowerState) String() string {
	wake := p.Wakefulness
	if wake == "" {
		wake = "未知"
	}
	lit := "熄屏"
	if p.IsAwake() {
		lit = "亮屏"
	}
	bright := "未知"
	if p.Brightness >= 0 {
		mode := "手动"
		if p.BrightnessMode == 1 {
			mode = "自动"
		}
		if pct := p.BrightnessPercent(); pct >= 0 {
			bright = fmt.Sprintf("%d/%d(≈%d%% %s)", p.Brightness, p.BrightnessMax, pct, mode)
		} else {
			bright = fmt.Sprintf("%d(%s)", p.Brightness, mode)
		}
	}
	stay := "允许休眠"
	switch p.StayOnPluggedIn {
	case StayOnAll:
		stay = "插电永不休眠(7)"
	case 0:
	default:
		stay = fmt.Sprintf("插电保持唤醒(%d)", p.StayOnPluggedIn)
	}
	batt := "未知"
	if p.BatteryLevel >= 0 {
		batt = fmt.Sprintf("%d%%", p.BatteryLevel)
		if p.PlugType > 0 {
			batt += " 充电中"
		}
	}
	off := "未知"
	if p.ScreenOffTimeout > 0 {
		off = p.ScreenOffTimeout.String()
	}
	return fmt.Sprintf("%s(%s) 亮度=%s 熄屏超时=%s %s 电量=%s",
		wake, lit, bright, off, stay, batt)
}

// PowerState 读取设备当前电源与显示状态。
func (d *Device) PowerState() (PowerState, error) {
	out, err := d.Shell(powerProbeCmd)
	if err != nil {
		return PowerState{}, err
	}
	st := ParsePowerState(out)
	// 亮度值域是静态信息，取到就缓存；失败不影响其余字段（仅亮度百分比不可用）。
	if min, max, err := d.BrightnessRange(); err == nil {
		st.BrightnessMin, st.BrightnessMax = min, max
	}
	return st, nil
}

// ParsePowerState 解析 powerProbeCmd 的输出（纯函数，便于单测）。
func ParsePowerState(out string) PowerState {
	st := PowerState{Brightness: -1, BrightnessMin: -1, BrightnessMax: -1, BatteryLevel: -1, PlugType: -1}
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "brightness="):
			st.Brightness = atoiOr(strings.TrimPrefix(line, "brightness="), -1)
		case strings.HasPrefix(line, "brightness_mode="):
			st.BrightnessMode = atoiOr(strings.TrimPrefix(line, "brightness_mode="), 0)
		case strings.HasPrefix(line, "off_timeout="):
			ms := atoiOr(strings.TrimPrefix(line, "off_timeout="), 0)
			if ms > 0 {
				st.ScreenOffTimeout = time.Duration(ms) * time.Millisecond
			}
		case strings.HasPrefix(line, "stay_on="):
			st.StayOnPluggedIn = atoiOr(strings.TrimPrefix(line, "stay_on="), 0)
		case strings.HasPrefix(line, "mWakefulness="):
			// 形如 "mWakefulness=Awake"；用 Fields 容忍尾随空格，
			// 且必须判空——该行可能只有键没有值，直接取 [0] 会越界。
			if f := strings.Fields(strings.TrimPrefix(line, "mWakefulness=")); len(f) > 0 {
				st.Wakefulness = f[0]
			}
		case strings.HasPrefix(line, "mBatteryLevel="):
			if st.BatteryLevel < 0 {
				st.BatteryLevel = atoiOr(strings.TrimPrefix(line, "mBatteryLevel="), -1)
			}
		case strings.HasPrefix(line, "mPlugType="):
			if st.PlugType < 0 {
				st.PlugType = atoiOr(strings.TrimPrefix(line, "mPlugType="), -1)
			}
		}
	}
	st.Interactive = st.Wakefulness == Awake
	return st
}

// atoiOr 解析十进制整数，失败或为 "null" 时返回默认值。
func atoiOr(s string, def int) int {
	s = strings.TrimSpace(s)
	if s == "" || s == "null" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}

// ClampInt 把 v 夹到 [lo, hi]；hi<lo 时以 lo 为准，避免返回越界值。
func ClampInt(v, lo, hi int) int {
	if hi < lo {
		hi = lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// brightnessRangeCmd 读取设备的亮度值域。
//
// 单独一条命令、结果缓存：dumpsys display 输出极大（数千行），
// 而抓帧自检 5 秒一次，不能把这条塞进热路径。
const brightnessRangeCmd = `dumpsys display | grep -m2 -E "mScreenBrightnessRange(Minimum|Maximum)"`

// BrightnessRange 返回设备亮度值域 [min,max]，结果在设备对象内缓存。
func (d *Device) BrightnessRange() (int, int, error) {
	d.mu.Lock()
	if d.brightMax > 0 {
		min, max := d.brightMin, d.brightMax
		d.mu.Unlock()
		return min, max, nil
	}
	d.mu.Unlock()

	out, err := d.Shell(brightnessRangeCmd)
	if err != nil {
		return 0, 0, err
	}
	min, max := ParseBrightnessRange(out)
	if max <= 0 {
		return 0, 0, fmt.Errorf("android: 未能从 dumpsys display 解析亮度值域（设备可能不暴露该字段）")
	}

	d.mu.Lock()
	d.brightMin, d.brightMax = min, max
	d.mu.Unlock()
	return min, max, nil
}

// ParseBrightnessRange 解析 dumpsys display 的亮度值域输出（纯函数，便于单测）。
func ParseBrightnessRange(out string) (int, int) {
	min, max := 0, 0
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "mScreenBrightnessRangeMinimum="):
			min = atoiOr(strings.TrimPrefix(line, "mScreenBrightnessRangeMinimum="), 0)
		case strings.HasPrefix(line, "mScreenBrightnessRangeMaximum="):
			if max == 0 { // 多显示器会重复出现，取第一块屏
				max = atoiOr(strings.TrimPrefix(line, "mScreenBrightnessRangeMaximum="), 0)
			}
		}
	}
	return min, max
}

// ParseBrightnessSpec 解析亮度参数：纯数字为绝对值，带 % 后缀为百分比。
//
// 支持两种写法是必要的：绝对值适合「照抄实测值」，百分比适合「跨设备
// 表达最暗」——标准 Android 是 0~255，MIUI 实测 10~2047，同一个数字
// 在两套值域里含义完全不同。
func ParseBrightnessSpec(s string) (val int, isPercent bool, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false, fmt.Errorf("android: 亮度参数为空")
	}
	if strings.HasSuffix(s, "%") {
		n, convErr := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(s, "%")))
		if convErr != nil {
			return 0, false, fmt.Errorf("android: 百分比亮度格式错误: %q（应形如 5%%）", s)
		}
		return ClampInt(n, 0, 100), true, nil
	}
	n, convErr := strconv.Atoi(s)
	if convErr != nil {
		return 0, false, fmt.Errorf("android: 亮度格式错误: %q（应为绝对值如 100，或百分比如 5%%）", s)
	}
	if n < 0 {
		return 0, false, fmt.Errorf("android: 亮度不能为负: %d", n)
	}
	return n, false, nil
}

// SetBrightness 按设备实际值域夹取后设置屏幕亮度，并关闭自动亮度。
//
// 必须先关自动亮度：screen_brightness_mode=1 时系统按环境光覆盖手动值，
// 在亮环境下「设成最暗」形同没设（实测 MIX 3 默认就是自动亮度 414/2047）。
// 两者在同一条 shell 里执行，避免中途被系统改回。
func (d *Device) SetBrightness(v int) error {
	min, max, err := d.BrightnessRange()
	if err != nil {
		return err
	}
	v = ClampInt(v, min, max)
	cmd := fmt.Sprintf("settings put system screen_brightness_mode 0; settings put system screen_brightness %d", v)
	_, err = d.Shell(cmd)
	return err
}

// SetBrightnessPercent 按值域百分比设置亮度，返回实际写入的绝对值。
func (d *Device) SetBrightnessPercent(p int) (int, error) {
	min, max, err := d.BrightnessRange()
	if err != nil {
		return 0, err
	}
	p = ClampInt(p, 0, 100)
	abs := min + (max-min)*p/100
	if err := d.SetBrightness(abs); err != nil {
		return 0, err
	}
	return abs, nil
}

// SetScreenOffTimeout 设置熄屏超时。
//
// 注意设备侧有下限（mMinimumScreenOffTimeoutConfig，实测 6000ms），
// 传更小的值会被系统抬回下限，读回值与设定值不一致属正常。
func (d *Device) SetScreenOffTimeout(t time.Duration) error {
	ms := int(t / time.Millisecond)
	if ms < 0 {
		return fmt.Errorf("android: 熄屏超时不能为负: %s", t)
	}
	_, err := d.Shell(fmt.Sprintf("settings put system screen_off_timeout %d", ms))
	return err
}

// SetStayOnPluggedIn 设置「插电时是否保持唤醒」的位掩码。
// 0 = 允许休眠（省电）；StayOnAll(7) = 插电永不休眠。
func (d *Device) SetStayOnPluggedIn(mode int) error {
	if mode < 0 || mode > StayOnAll {
		return fmt.Errorf("android: stay_on_while_plugged_in 取值应在 0~7，收到 %d", mode)
	}
	_, err := d.Shell(fmt.Sprintf("settings put global stay_on_while_plugged_in %d", mode))
	return err
}

// 解除锁屏的重试参数。
//
// 为什么要重试而不是固定等待：熄屏唤醒后 keyguard 需要一点时间才就绪，
// 太早发出的 dismiss 会被系统忽略——实测等待 1.2 秒仍会停在锁屏，
// 而屏幕稳定后同一条命令立刻有效。靠固定等待既慢又不可靠，
// 改成「发命令 → 读判据 → 未解除就再试」。
const (
	keyguardRetries      = 4
	keyguardPollInterval = 700 * time.Millisecond
)

// keyguardCmd 取回「锁屏是否在屏」的判据。
//
// 判据来自实测（2026-09-22，MIX 3 / MIUI 12.5）：
//
//	解锁（游戏在前台）: mCurrentFocus=com.tencent.nrc/...GameActivity, mDreamingLockscreen=false
//	锁屏            : mCurrentFocus=StatusBar,                        mDreamingLockscreen=true
//
// 用 mDreamingLockscreen 而不是 mCurrentFocus：后者在不同 ROM 上
// 锁屏时的窗口名不一致（StatusBar / NotificationShade / KeyguardHostView），
// 前者是干净的布尔值。stderr 重定向是为了压掉 grep -m1 提前退出导致的
// "Failed to write while dumping service window: Broken pipe" 噪声。
const keyguardCmd = `dumpsys window | grep -m1 mDreamingLockscreen 2>/dev/null`

// KeyguardShowing 报告锁屏是否在屏。
func (d *Device) KeyguardShowing() (bool, error) {
	out, err := d.Shell(keyguardCmd)
	if err != nil {
		return false, err
	}
	return ParseKeyguardShowing(out), nil
}

// ParseKeyguardShowing 解析 dumpsys window 的 mDreamingLockscreen 行（纯函数）。
func ParseKeyguardShowing(out string) bool {
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		idx := strings.Index(line, "mDreamingLockscreen=")
		if idx < 0 {
			continue
		}
		return strings.HasPrefix(line[idx+len("mDreamingLockscreen="):], "true")
	}
	return false
}

// unlockKeyguard 反复尝试解除滑动锁屏，直到确认离开锁屏或重试耗尽。
//
// 只对「无密码的滑动锁屏」有效：设备设了 PIN/图案/密码时 adb 无法自动解锁，
// 此时返回明确错误而不是静默继续——否则上层会以为设备可用，
// 实际所有输入都落在锁屏上。
func (d *Device) unlockKeyguard() error {
	var lastErr error
	for i := 0; i < keyguardRetries; i++ {
		time.Sleep(keyguardPollInterval)
		if _, err := d.Shell("wm dismiss-keyguard"); err != nil {
			lastErr = err
			continue
		}
		showing, err := d.KeyguardShowing()
		if err != nil {
			lastErr = err
			continue
		}
		if !showing {
			return nil
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("android: 连续 %d 次未能解除锁屏，设备可能设了密码（adb 无法自动解锁）", keyguardRetries)
}

// wakeIfAsleep 仅在熄屏时唤醒并解除锁屏，返回本次是否真的执行过唤醒。
//
// 用 KEYCODE_WAKEUP 而非 KEYCODE_POWER：前者在已亮屏时是空操作，
// 后者会把亮着的屏幕按灭——跑批时误发 POWER 等于自己把设备弄熄。
//
// 唤醒必须连带解锁：这台设备没设密码（dumpsys trust 报 deviceLocked=0），
// 但唤醒后停在滑动锁屏，此时触摸与按键全部落到锁屏上，表现为「操作无效」，
// 比熄屏更难定位。实测解锁后前台仍是游戏（com.tencent.nrc），说明熄屏
// 期间游戏没被杀也没被切后台，唤醒即可继续。
func (d *Device) wakeIfAsleep() (bool, error) {
	st, err := d.PowerState()
	if err != nil {
		return false, err
	}
	if st.IsAwake() {
		return false, nil
	}

	if _, err := d.Shell("input keyevent KEYCODE_WAKEUP"); err != nil {
		return false, err
	}
	if err := d.unlockKeyguard(); err != nil {
		return true, err
	}
	return true, nil
}

// EnsureAwake 确保设备处于「点亮且已解除锁屏」的可操作状态，
// 返回本次是否执行过唤醒（已亮屏时为 false）。
func (d *Device) EnsureAwake() (bool, error) {
	woken, err := d.wakeIfAsleep()
	if err != nil {
		return woken, err
	}
	if !woken {
		// 已亮屏也可能停在滑动锁屏（例如上一轮唤醒只点亮没解锁），
		// 此时任何输入都会落到锁屏上，静默失效。先读判据再决定，
		// 非锁屏时只多一次 shell，不打扰正常跑批。
		showing, kerr := d.KeyguardShowing()
		if kerr == nil && showing {
			return false, d.unlockKeyguard()
		}
	}
	return woken, nil
}

// Sleep 立即熄屏（仅用于验证自动唤醒链路）。
func (d *Device) Sleep() error {
	_, err := d.Shell("input keyevent KEYCODE_SLEEP")
	return err
}

// PowerSavingOptions 是省电档参数。
type PowerSavingOptions struct {
	BrightnessPercent int           // 目标亮度（值域百分比 0~100）
	OffTimeout        time.Duration // 目标熄屏超时
}

// DefaultPowerSaving 返回保守的省电档：亮度 5%、2 分钟熄屏、允许插电休眠。
//
// 亮度用百分比而非绝对值：值域因 ROM 而异（Android 标准 0~255，
// MIUI 实测 10~2047），写死绝对值会在一半设备上得到完全不同的亮度。
// 取 5% 而不是 0%：屏幕背光功耗与亮度近似线性，5% 已省掉绝大部分，
// 同时留一点余量给人工核对画面——0% 时肉眼几乎看不见。
func DefaultPowerSaving() PowerSavingOptions {
	return PowerSavingOptions{BrightnessPercent: 5, OffTimeout: 2 * time.Minute}
}

// ApplyPowerSaving 应用省电档并读回实际状态。
//
// 三步顺序有意义：先允许休眠（否则设了超时也不会熄），再压亮度，
// 最后设超时；读回是为了暴露「系统抬回下限」这类静默差异。
func (d *Device) ApplyPowerSaving(opt PowerSavingOptions) (PowerState, error) {
	if err := d.SetStayOnPluggedIn(0); err != nil {
		return PowerState{}, err
	}
	if _, err := d.SetBrightnessPercent(opt.BrightnessPercent); err != nil {
		return PowerState{}, err
	}
	if err := d.SetScreenOffTimeout(opt.OffTimeout); err != nil {
		return PowerState{}, err
	}
	return d.PowerState()
}

// SetAutoWake 开关抓帧前的自动唤醒（默认开启）。
//
// 关掉它的唯一合理场景是「就是要拍黑屏」（例如测熄屏时的帧差基线）。
func (d *Device) SetAutoWake(on bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.autoWakeOff = !on
}

// AutoWake 报告抓帧前自动唤醒是否开启。
func (d *Device) AutoWake() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.autoWakeOff
}

// ensureAwakeThrottled 是抓帧前的带节流自检：确认屏幕点亮，熄屏则唤醒。
//
// 节流与「不阻断」两条设计取舍：
//   - 节流 5 秒：实时回路 5FPS，每帧查一次 dumpsys power 会明显拖慢；
//   - 唤醒失败不返回错误：抓帧链路已有多处依赖，让唤醒失败直接中断抓帧
//     会把「adb 权限不足」放大成「整个回路崩掉」。失败时抓到的黑屏会在
//     判据层暴露（std≈0），需要精确诊断时用 cmd/power -status 单查。
func (d *Device) ensureAwakeThrottled() {
	d.mu.Lock()
	off := d.autoWakeOff
	recent := time.Since(d.lastWakeCheck) < autoWakeInterval
	if !off && !recent {
		d.lastWakeCheck = time.Now()
	}
	d.mu.Unlock()

	if off || recent {
		return
	}
	// 热路径调 wakeIfAsleep：它内部只在「确实熄屏」时才发唤醒键并解锁，
	// 已亮屏时是一次 dumpsys 就返回，不做多余 adb 往返。
	// 若已亮屏却停在锁屏（少见），这里不额外补解，交给显式 EnsureAwake 兜底——
	// 热路径每帧都查 keyguard 会明显拖慢实时回路。
	_, _ = d.wakeIfAsleep()
}
