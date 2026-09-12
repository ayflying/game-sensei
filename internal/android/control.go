package android

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ayflying/game-sensei/internal/agent"
)

// 触摸时长约束。input swipe 的 duration 决定「按住多久」：
// 极短 = 轻点，较长 = 长按 / 推摇杆。
const (
	// MinTouchMs 是 input 命令对 duration 可接受的下限。
	MinTouchMs = 1
	// DefaultLongPressMs 长按默认时长。
	DefaultLongPressMs = 800
	// DefaultSwipeMs 滑动默认时长。
	DefaultSwipeMs = 200
)

// Tap 在像素坐标 (x,y) 轻点一次。
func (d *Device) Tap(x, y int) error {
	return d.tap(x, y)
}

func (d *Device) tap(x, y int) error {
	_, err := d.Shell(fmt.Sprintf("input tap %d %d", x, y))
	return err
}

// TapNorm 以归一化坐标（0~1，相对当前屏幕宽高）轻点，返回实际像素坐标便于日志。
//
// 归一化坐标是决策层的统一输出形式：同一套策略可跨分辨率、跨设备复用。
func (d *Device) TapNorm(nx, ny float64) (int, int, error) {
	x, y, err := d.normToPixel(nx, ny)
	if err != nil {
		return 0, 0, err
	}
	return x, y, d.tap(x, y)
}

// Swipe 从 (x1,y1) 滑到 (x2,y2)，dur 为整个手势耗时。
//
// 超时按手势时长放宽：推摇杆一按可能就是好几秒（move 动辄 2000~3000ms），
// 用默认 15s 掐掉会把「按住持续移动」变成「划一下就松开」——静默变味，
// 比直接报错更难查。
func (d *Device) Swipe(x1, y1, x2, y2 int, dur time.Duration) error {
	ms := durMs(dur, DefaultSwipeMs)
	_, err := d.ShellTimeout(fmt.Sprintf("input swipe %d %d %d %d %d", x1, y1, x2, y2, ms),
		msDuration(ms))
	return err
}

// SwipeNorm 归一化坐标版滑动。
func (d *Device) SwipeNorm(nx1, ny1, nx2, ny2 float64, dur time.Duration) error {
	x1, y1, err := d.normToPixel(nx1, ny1)
	if err != nil {
		return err
	}
	x2, y2, err := d.normToPixel(nx2, ny2)
	if err != nil {
		return err
	}
	return d.Swipe(x1, y1, x2, y2, dur)
}

// LongPress 在 (x,y) 按住 dur（用原地 swipe 实现，无需 root）。
//
// 用 ShellTimeout 放宽超时：按住时长本身就计入命令耗时，默认 15s 不够用。
func (d *Device) LongPress(x, y int, dur time.Duration) error {
	ms := durMs(dur, DefaultLongPressMs)
	_, err := d.ShellTimeout(fmt.Sprintf("input swipe %d %d %d %d %d", x, y, x, y, ms),
		msDuration(ms))
	return err
}

// LongPressNorm 归一化坐标版长按。
func (d *Device) LongPressNorm(nx, ny float64, dur time.Duration) (int, int, error) {
	x, y, err := d.normToPixel(nx, ny)
	if err != nil {
		return 0, 0, err
	}
	return x, y, d.LongPress(x, y, dur)
}

// Joystick 从摇杆中心 (cx,cy) 朝 (dx,dy) 像素偏移方向按住 dur，
// 模拟虚拟摇杆的「持续推方向」。
//
// 手游移动普遍是摇杆驱动：按下并保持 = 持续移动，偏移量决定速度。
// 用带 duration 的 swipe 表达（起点≠终点且耗时长），一次调用即一段持续移动。
func (d *Device) Joystick(cx, cy, dx, dy int, dur time.Duration) error {
	return d.Swipe(cx, cy, cx+dx, cy+dy, dur)
}

// JoystickNorm 归一化版摇杆：center 为摇杆中心，offX/offY 为归一化偏移量。
func (d *Device) JoystickNorm(cx, cy, offX, offY float64, dur time.Duration) error {
	px, py, err := d.normToPixel(cx, cy)
	if err != nil {
		return err
	}
	size, err := d.ScreenSize()
	if err != nil {
		return err
	}
	return d.Joystick(px, py, int(offX*float64(size.X)), int(offY*float64(size.Y)), dur)
}

// Key 发送按键事件，接受任意 keyevent 写法：
//
//	"back" / "KEYCODE_BACK" / "4"  →  input keyevent 4
//
// 常用：back、home、app_switch、enter、del、dpad_up/dpad_down/dpad_left/dpad_right。
func (d *Device) Key(code string) error {
	name := normalizeKeyCode(code)
	if name == "" {
		return fmt.Errorf("android: 空按键名")
	}
	_, err := d.Shell("input keyevent " + name)
	return err
}

// KeyHold 长按一个键（对应 `input keyevent --longpress`）。
//
// 用于「按住方向键持续移动」这类需求——某些游戏（尤其带虚拟十字键的）
// 只有长按才会连续移动，点一下只走一格。
func (d *Device) KeyHold(code string, dur time.Duration) error {
	name := normalizeKeyCode(code)
	if name == "" {
		return fmt.Errorf("android: 空按键名")
	}
	// 短按没必要走 longpress（有些 ROM 对 --longpress 的实现有差异）
	if dur < 400*time.Millisecond {
		return d.Key(code)
	}
	_, err := d.Shell("input keyevent --longpress " + name)
	return err
}

// normalizeKeyCode 归一化按键写法。数字原样返回；纯字母名自动补 KEYCODE_ 前缀。
func normalizeKeyCode(code string) string {
	c := strings.TrimSpace(code)
	if c == "" {
		return ""
	}
	if _, err := strconv.Atoi(c); err == nil {
		return c
	}
	up := strings.ToUpper(c)
	if strings.HasPrefix(up, "KEYCODE_") {
		return up
	}
	// 把 home / app_switch 这类写法转成 HOME / APP_SWITCH
	return "KEYCODE_" + strings.ToUpper(strings.ReplaceAll(c, " ", "_"))
}

// Launch 启动应用（包名为空则报错）。优先解析主 Activity 后 am start，
// 失败再回退 monkey，兼容未导出 LAUNCHER 入口的应用。
func (d *Device) Launch(pkg string) error {
	if strings.TrimSpace(pkg) == "" {
		return fmt.Errorf("android: 启动应用需要包名（-app）")
	}
	out, err := d.Shell("cmd package resolve-activity --brief " + pkg)
	if err == nil {
		if comp := parseComponent(out); comp != "" {
			if _, err := d.Shell("am start -n " + comp); err == nil {
				return nil
			}
		}
	}
	if _, err := d.Shell(fmt.Sprintf(
		"monkey -p %s -c android.intent.category.LAUNCHER 1", pkg)); err != nil {
		return fmt.Errorf("android: 启动 %s 失败: %w", pkg, err)
	}
	return nil
}

// Foreground 返回当前前台应用的包名。
func (d *Device) Foreground() (string, error) {
	out, err := d.Shell("dumpsys window | grep -E 'mCurrentFocus|mFocusedApp' | head -2")
	if err != nil || strings.TrimSpace(out) == "" {
		// 回退：部分系统上 dumpsys window 的焦点行受限
		out, err = d.Shell("dumpsys activity activities | grep -E 'mResumedActivity' | head -1")
		if err != nil {
			return "", err
		}
	}
	pkg := parseForegroundPkg(out)
	if pkg == "" {
		return "", fmt.Errorf("android: 无法解析前台应用: %q", strings.TrimSpace(out))
	}
	return pkg, nil
}

// IsForeground 判断指定包名当前是否在前台。
func (d *Device) IsForeground(pkg string) (bool, error) {
	cur, err := d.Foreground()
	if err != nil {
		return false, err
	}
	return cur == pkg, nil
}

// WaitForeground 轮询等待 pkg 成为前台应用（用于启动游戏后确认真的起来了）。
func (d *Device) WaitForeground(pkg string, timeout, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Second
	}
	deadline := time.Now().Add(timeout)
	var last string
	for {
		cur, err := d.Foreground()
		if err == nil {
			last = cur
			if cur == pkg {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("android: 等待 %s 进入前台超时（当前 %q）", pkg, last)
		}
		time.Sleep(interval)
	}
}

// Apply 把 agent.Action 落到触摸屏；坐标按当前屏幕尺寸换算成像素。
//
// 这样同一套动作空间既能驱动 PC（键鼠）也能驱动手机（触摸），
// 决策层无需关心运行平台。
func (d *Device) Apply(act agent.Action) error {
	switch act.Kind {
	case agent.ActionNone:
		// L1「不动，等画面变化」：手机端本来就该什么都不做，是合法动作。
		return nil
	case agent.ActionTap:
		_, _, err := d.TapNorm(act.Nx, act.Ny)
		return err
	case agent.ActionSwipe:
		return d.SwipeNorm(act.Nx, act.Ny, act.Nx2, act.Ny2, act.Dur)
	case agent.ActionJoystick:
		// L2 摇杆：从中心 (Nx,Ny) 推到目标点 (Nx2,Ny2)，用一次带时长的
		// swipe 表达「按住并保持推杆」。
		//
		// ⚠️ 这一支曾经是漏的（2026-09-12 真机修复）。档案里 move.mode=joystick 时，
		// MOVE 会被展开成 ActionJoystick，而它掉进 default 被静默吞掉：
		// 日志照常打印「执行 move:up_right/2500ms → joy:…」、applied 计数照加、
		// 没有任何报错，但手机上一次触摸都没发生。现象是角色一步都不走、
		// 画面变化量恒在 1~3（只有环境动画），采集回路把整场示范都耗在
		// 「老师反复给方向 → 世界纹丝不动 → 判定卡死 → 脱困也无效」上。
		// 教训：设备层的 default 绝不能静默返回 nil。
		return d.SwipeNorm(act.Nx, act.Ny, act.Nx2, act.Ny2, act.Dur)
	case agent.ActionLongPress:
		_, _, err := d.LongPressNorm(act.Nx, act.Ny, act.Dur)
		return err
	case agent.ActionKey:
		if act.Dur > 0 {
			return d.KeyHold(act.Code, act.Dur)
		}
		return d.Key(act.Code)
	case agent.ActionMouseMove:
		// 手机没有鼠标，这个 L2 动作在 Android 上无意义。
		// 明确报错而不是静默忽略：动作协议里根本不会出现它，
		// 出现了就说明上游解析或档案配置出了问题，早暴露比悄悄吞掉好。
		return fmt.Errorf("android: 不支持鼠标相对移动动作（ActionMouseMove）")
	default:
		// 明确报错而不是静默吞掉：动作协议里不会出现未知类型，出现了就说明
		// 上游解析或档案配置漏了映射。静默返回 nil 会让「设备毫无反应」这一
		// 现象被伪装成「动作执行成功」，排查成本极高（真实教训见 ActionJoystick 分支）。
		return fmt.Errorf("android: 不支持的动作类型 %v（L1 动作应先由档案解析成 L2）", act.Kind)
	}
}

// normToPixel 把归一化坐标换算为像素坐标，越界值截断到屏幕内。
func (d *Device) normToPixel(nx, ny float64) (int, int, error) {
	size, err := d.ScreenSize()
	if err != nil {
		return 0, 0, err
	}
	x := int(clampF(nx, 0, 1) * float64(size.X-1))
	y := int(clampF(ny, 0, 1) * float64(size.Y-1))
	return x, y, nil
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func durMs(d time.Duration, def int) int {
	ms := int(d / time.Millisecond)
	if ms < MinTouchMs {
		ms = def
	}
	return ms
}

// msDuration 给长按这类需要放宽 shell 超时的操作留出余量。
func msDuration(ms int) time.Duration {
	return time.Duration(ms)*time.Millisecond + defaultShellTimeout
}

// parseComponent 从 resolve-activity 输出里取出 "包名/Activity" 组件名。
func parseComponent(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "priority=") || strings.HasPrefix(line, "No activity") {
			continue
		}
		if i := strings.Index(line, "/"); i > 0 && strings.Contains(line[:i], ".") {
			return line
		}
	}
	return ""
}

// parseForegroundPkg 从 dumpsys 焦点行里取出包名。
// 兼容格式：
//
//	mCurrentFocus=Window{773d6db u0 com.tencent.nrc/com.epicgames.ue4.GameActivity}
//	mResumedActivity: ActivityRecord{... com.tencent.nrc/.MainActivity t2}
func parseForegroundPkg(out string) string {
	for _, line := range strings.Split(out, "\n") {
		i := strings.Index(line, "/")
		if i < 0 {
			continue
		}
		// 向左取到空格或 { 为止，得到候选包名
		j := i - 1
		for j >= 0 && line[j] != ' ' && line[j] != '{' {
			j--
		}
		cand := line[j+1 : i]
		// 去掉可能的 "u0" 之类前面残留（按空格切分时已排除）
		if strings.Contains(cand, ".") && !strings.ContainsAny(cand, "{}") {
			return cand
		}
	}
	return ""
}
