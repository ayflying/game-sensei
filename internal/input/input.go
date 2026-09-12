//go:build windows

// Package input 通过 Windows 的 SendInput / SetCursorPos / mouse_event
// （纯 Go syscall，零 CGO）模拟键鼠。
//
// 这是框架的「手」。动作空间分两层（见 internal/agent）：
//
//	L1 语义层（模型输出的）  MOVE dir=up / PRESS name=jump
//	L2 执行层（本包消费的）  ActionKey / ActionTap / ActionSwipe / ActionMouseMove
//
// L1→L2 的翻译由游戏档案（internal/game）完成，本包只认 L2。
// 所有真实发送受 Actuator.Live 开关保护（默认 dry-run）。
//
// 关键点（Win32 SendInput x64 结构布局）：
//
//	MOUSEINPUT  = LONG*2 + DWORD*3 + ULONG_PTR = 32 字节（含对齐 padding）
//	KEYBDINPUT  = WORD*2 + DWORD*2 + ULONG_PTR = 24 字节
//	INPUT       = DWORD type + 4 字节对齐 + union(32) = 40 字节
//
// cbSize 必须传 unsafe.Sizeof(INPUT)，结构逐字节错位会导致 SendInput 静默失败。
// 本文件底部有编译期断言把尺寸钉死。
package input

import (
	"fmt"
	"image"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/ayflying/game-sensei/internal/agent"
)

var (
	user32             = syscall.NewLazyDLL("user32.dll")
	procSendInput      = user32.NewProc("SendInput")
	procSetCursorPos   = user32.NewProc("SetCursorPos")
	procMapVirtualKeyW = user32.NewProc("MapVirtualKeyW")
)

// Win32 常量。
const (
	inputMouse    = 0
	inputKeyboard = 1

	keyeventfKeyup    = 0x0002
	keyeventfScancode = 0x0008 // 事件携带扫描码（游戏引擎只认这个）
	mapvkVKToVSC      = 0      // MapVirtualKeyW: 虚拟键码 → 扫描码

	mouseeventfMove       = 0x0001
	mouseeventfLeftdown   = 0x0002
	mouseeventfLeftup     = 0x0004
	mouseeventfAbsolute   = 0x8000
	mouseeventfRightdown  = 0x0008
	mouseeventfRightup    = 0x0010
	mouseeventfMiddledown = 0x0020
	mouseeventfMiddleup   = 0x0040
)

// mouseInput 对应 Win32 MOUSEINPUT（x64 下 32 字节）。
type mouseInput struct {
	Dx        int32
	Dy        int32
	MouseData uint32
	Flags     uint32
	Time      uint32
	_         uint32 // 对齐 padding（ULONG_PTR 需 8 字节边界）
	ExtraInfo uintptr
}

// keybdInput 对应 Win32 KEYBDINPUT（x64 下 24 字节）。
type keybdInput struct {
	Vk        uint16
	Scan      uint16
	Flags     uint32
	Time      uint32
	_         uint32 // 对齐 padding
	ExtraInfo uintptr
}

// winInput 对应 Win32 INPUT（x64 下 40 字节）。联合体按最大成员 MOUSEINPUT 取 32 字节，
// 用 [4]uint64 承载以强制 8 字节对齐（ULONG_PTR 要求），否则数组元素可能 4 字节错位、
// 导致 SendInput 静默失败。
type winInput struct {
	Type  uint32
	pad   uint32 // 对齐 padding：union 从 8 字节边界开始
	union [4]uint64
}

// setKeybd 把 KEYBDINPUT 拷贝进联合体（只拷贝实际大小，不越界读）。
func (i *winInput) setKeybd(k keybdInput) {
	i.union = [4]uint64{}
	dst := unsafe.Slice((*byte)(unsafe.Pointer(&i.union[0])), 32)
	b := unsafe.Slice((*byte)(unsafe.Pointer(&k)), unsafe.Sizeof(k))
	copy(dst, b)
}

// setMouse 把 MOUSEINPUT 拷贝进联合体。
func (i *winInput) setMouse(m mouseInput) {
	i.union = [4]uint64{}
	dst := unsafe.Slice((*byte)(unsafe.Pointer(&i.union[0])), 32)
	b := unsafe.Slice((*byte)(unsafe.Pointer(&m)), unsafe.Sizeof(m))
	copy(dst, b)
}

// send 调用 SendInput 发送一组 INPUT。
func send(inputs []winInput) error {
	if len(inputs) == 0 {
		return nil
	}
	r1, _, err := procSendInput.Call(
		uintptr(len(inputs)),
		uintptr(unsafe.Pointer(&inputs[0])),
		unsafe.Sizeof(winInput{}),
	)
	if int(r1) != len(inputs) {
		return fmt.Errorf("SendInput 注入 %d/%d 条: %w", r1, len(inputs), err)
	}
	return nil
}

// keyDown/keyUp 构造单条键盘 INPUT。
//
// ⚠️ 必须带扫描码（KEYEVENTF_SCANCODE）：只给 Vk 的注入事件，
// 系统消息循环（WM_KEYDOWN）认，但 UE4/DirectInput/Raw Input 这类
// **按扫描码轮询键盘的游戏直接忽略**——实测洛克王国：世界（UE4）里
// 裸 VK 的 W 按住 1 秒角色纹丝不动，带上扫描码才生效（2026-09-12）。
// Vk 字段保留：两类消费者各取所需。
func keyDown(vk uint16) winInput {
	var in winInput
	in.Type = inputKeyboard
	in.setKeybd(keybdInput{Vk: vk, Scan: vkToScan(vk), Flags: keyeventfScancode})
	return in
}

func keyUp(vk uint16) winInput {
	var in winInput
	in.Type = inputKeyboard
	in.setKeybd(keybdInput{Vk: vk, Scan: vkToScan(vk), Flags: keyeventfScancode | keyeventfKeyup})
	return in
}

// vkToScan 把虚拟键码换算成扫描码（MAPVK_VK_TO_VSC）。
func vkToScan(vk uint16) uint16 {
	sc, _, _ := procMapVirtualKeyW.Call(uintptr(vk), mapvkVKToVSC)
	return uint16(sc)
}

// Actuator 执行动作。Live=false 时只记录、不真正发送（dry-run）。
type Actuator struct {
	Live bool

	// Screen 返回「归一化坐标的基准域」尺寸，供归一化坐标换算成像素。
	// 为 nil 时点击类动作会明确报错，而不是点到 (0,0) 去。
	Screen func() (w, h int, err error)

	// Offset 是归一化域原点在屏幕坐标系下的偏移（窗口模式的窗口左上角）。
	//
	// 窗口化游戏里，归一化坐标以窗口客户区为基准（Screen 返回窗口尺寸），
	// 但 SetCursorPos 要的是屏幕绝对坐标——pixel() 算出域内像素后加上
	// Offset 才是真正该点的位置。全屏模式 Offset 为零值，行为不变。
	Offset image.Point

	mu sync.Mutex
	// gen 记录每个键的「按住代次」：只有最新代次的协程才有权抬起按键，
	// 否则频繁刷新 hold 时新老协程会互相把键抬起来，表现为「按不住」。
	gen map[string]uint64
	// down 记录某键当前是否处于按下状态（用于避免重复 down 与退出时兜底抬起）。
	down map[string]bool
}

// NewActuator 构造执行器；live=false 为安全默认。
func NewActuator(live bool) *Actuator {
	return &Actuator{
		Live: live,
		gen:  map[string]uint64{},
		down: map[string]bool{},
	}
}

// Apply 把 L2 动作落到键鼠。
//
// 关于「按住」：Dur>0 的 ActionKey 表示持续按住该键。实现是**异步**的——
// 按下后立刻返回，由后台在 Dur 到期时抬起。
// 之所以不在这里 sleep：实时回路是 30FPS 的节拍，一次阻塞 1 秒会让整个回路停摆。
// 语义上这也更对：持续移动是「状态」而不是「一次持续 1 秒的调用」，
// 每个 tick 重复下达 MOVE 会不断刷新代次，键就一直按着；停止下达后自然松开。
func (a *Actuator) Apply(act agent.Action) error {
	if !a.Live {
		return nil // dry-run：吞掉动作，仅用于验证链路与延迟
	}
	switch act.Kind {
	case agent.ActionKey:
		// 多键（PC 斜向移动 = 同时按住两键）：逐个走同一套按住逻辑，
		// holdKey 本身按 code 独立记账，两个键各自刷新代次，互不干扰。
		if len(act.Codes) > 0 {
			for _, c := range act.Codes {
				vk := KeyToVK(c)
				if vk == 0 {
					return fmt.Errorf("input: 未知按键 %q", c)
				}
				if act.Dur > 0 {
					a.holdKey(c, vk, act.Dur)
					continue
				}
				if err := send([]winInput{keyDown(vk), keyUp(vk)}); err != nil {
					return err
				}
			}
			return nil
		}
		vk := KeyToVK(act.Code)
		if vk == 0 {
			return fmt.Errorf("input: 未知按键 %q", act.Code)
		}
		if act.Dur > 0 {
			a.holdKey(act.Code, vk, act.Dur)
			return nil
		}
		return send([]winInput{keyDown(vk), keyUp(vk)})

	case agent.ActionTap:
		x, y, err := a.pixel(act.Nx, act.Ny)
		if err != nil {
			return err
		}
		if err := moveCursor(x, y); err != nil {
			return err
		}
		return send([]winInput{mouseBtn(mouseeventfLeftdown), mouseBtn(mouseeventfLeftup)})

	case agent.ActionLongPress:
		x, y, err := a.pixel(act.Nx, act.Ny)
		if err != nil {
			return err
		}
		if err := moveCursor(x, y); err != nil {
			return err
		}
		if err := send([]winInput{mouseBtn(mouseeventfLeftdown)}); err != nil {
			return err
		}
		time.Sleep(act.Dur)
		return send([]winInput{mouseBtn(mouseeventfLeftup)})

	case agent.ActionSwipe:
		return a.swipe(act)

	case agent.ActionMouseMove:
		return send([]winInput{mouseMove(act.Dx, act.Dy)})

	default:
		return nil
	}
}

// swipe 用「按下 → 分步移动 → 抬起」模拟拖拽（转视角 / 拖动界面）。
//
// 分步而不是一步跳到位：多数游戏按帧采样鼠标位移来算转动速度，
// 瞬移会被判定为「没动过」或转动量极大，分步才能得到可控的转视角。
func (a *Actuator) swipe(act agent.Action) error {
	x0, y0, err := a.pixel(act.Nx, act.Ny)
	if err != nil {
		return err
	}
	x1, y1, err := a.pixel(act.Nx2, act.Ny2)
	if err != nil {
		return err
	}
	if err := moveCursor(x0, y0); err != nil {
		return err
	}
	if err := send([]winInput{mouseBtn(mouseeventfLeftdown)}); err != nil {
		return err
	}

	steps := 12
	total := act.Dur
	if total <= 0 {
		total = 300 * time.Millisecond
	}
	stepDelay := total / time.Duration(steps)
	for i := 1; i <= steps; i++ {
		x := x0 + (x1-x0)*i/steps
		y := y0 + (y1-y0)*i/steps
		if err := moveCursor(x, y); err != nil {
			return err
		}
		if stepDelay > 0 && i < steps {
			time.Sleep(stepDelay)
		}
	}
	return send([]winInput{mouseBtn(mouseeventfLeftup)})
}

// pixel 把归一化坐标换算成像素；Screen 未注入时明确报错。
func (a *Actuator) pixel(nx, ny float64) (int, int, error) {
	if a.Screen == nil {
		return 0, 0, fmt.Errorf("input: 未注入屏幕尺寸，无法把归一化坐标 (%.3f,%.3f) 换算成像素", nx, ny)
	}
	w, h, err := a.Screen()
	if err != nil {
		return 0, 0, fmt.Errorf("input: 获取屏幕尺寸失败: %w", err)
	}
	if w <= 0 || h <= 0 {
		return 0, 0, fmt.Errorf("input: 屏幕尺寸非法 %dx%d", w, h)
	}
	x := int(nx * float64(w))
	y := int(ny * float64(h))
	// 夹紧到域内：越界点击会被系统丢弃，宁可靠边也不要丢
	if x < 0 {
		x = 0
	}
	if x > w-1 {
		x = w - 1
	}
	if y < 0 {
		y = 0
	}
	if y > h-1 {
		y = h - 1
	}
	// 域内像素 → 屏幕绝对坐标（窗口模式加窗口偏移；全屏模式偏移为零）
	return x + a.Offset.X, y + a.Offset.Y, nil
}

// holdKey 按下键并在 d 之后抬起；期间同键的新请求会延长按住时间。
func (a *Actuator) holdKey(code string, vk uint16, d time.Duration) {
	a.mu.Lock()
	if a.gen == nil {
		a.gen = map[string]uint64{}
		a.down = map[string]bool{}
	}
	a.gen[code]++
	my := a.gen[code]
	already := a.down[code]
	a.mu.Unlock()

	if !already {
		if err := send([]winInput{keyDown(vk)}); err != nil {
			return
		}
		a.mu.Lock()
		a.down[code] = true
		a.mu.Unlock()
	}

	go func() {
		time.Sleep(d)
		a.mu.Lock()
		latest := a.gen[code] == my
		a.mu.Unlock()
		if !latest {
			return // 有更新的按住请求，抬起责任交给它
		}
		_ = send([]winInput{keyUp(vk)})
		a.mu.Lock()
		a.down[code] = false
		a.mu.Unlock()
	}()
}

// ReleaseAll 抬起所有仍处于按下状态的键。
//
// 退出时必须调用：否则最后一步的「按住」会把键卡在按下状态，
// 回到桌面后表现为键盘失灵（用户在记事本里会一直输出同一个字符）。
func (a *Actuator) ReleaseAll() {
	a.mu.Lock()
	codes := make([]string, 0, len(a.down))
	for c, d := range a.down {
		if d {
			codes = append(codes, c)
		}
	}
	a.gen = map[string]uint64{} // 作废所有在途的抬起协程
	for _, c := range codes {
		a.down[c] = false
	}
	a.mu.Unlock()

	if !a.Live {
		return
	}
	for _, c := range codes {
		if vk := KeyToVK(c); vk != 0 {
			_ = send([]winInput{keyUp(vk)})
		}
	}
}

// PressedKeys 返回当前仍按住的键（供退出提示与测试）。
func (a *Actuator) PressedKeys() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for c, d := range a.down {
		if d {
			out = append(out, c)
		}
	}
	return out
}

func moveCursor(x, y int) error {
	r1, _, err := procSetCursorPos.Call(uintptr(x), uintptr(y))
	if r1 == 0 {
		return fmt.Errorf("SetCursorPos(%d,%d): %w", x, y, err)
	}
	return nil
}

// mouseBtn 构造一次鼠标按键事件（绝对坐标由 SetCursorPos 控制，这里不带位移）。
func mouseBtn(flag uint32) winInput {
	var in winInput
	in.Type = inputMouse
	in.setMouse(mouseInput{Flags: flag})
	return in
}

// mouseMove 构造相对位移事件。
func mouseMove(dx, dy int) winInput {
	var in winInput
	in.Type = inputMouse
	in.setMouse(mouseInput{Dx: int32(dx), Dy: int32(dy), Flags: mouseeventfMove})
	return in
}

// vkTable 是键名 → 虚拟键码。
//
// 覆盖三类：PC 游戏常用（WASD / 空格 / Shift / Esc）、界面导航
// （方向键 / 回车 / Tab）与系统键（F1~F12 略）。键名大小写不敏感。
//
// 之所以要这么一张表而不是只认方向键：MOVE 之外的游戏操作（跳跃、交互、
// 打开背包）在 PC 上都是按键，「只有四个方向键」的框架玩不了任何真实游戏。
var vkTable = map[string]uint16{
	// 字母（WASD 移动是 PC 游戏的事实标准）
	"a": 0x41, "b": 0x42, "c": 0x43, "d": 0x44, "e": 0x45, "f": 0x46,
	"g": 0x47, "h": 0x48, "i": 0x49, "j": 0x4A, "k": 0x4B, "l": 0x4C,
	"m": 0x4D, "n": 0x4E, "o": 0x4F, "p": 0x50, "q": 0x51, "r": 0x52,
	"s": 0x53, "t": 0x54, "u": 0x55, "v": 0x56, "w": 0x57, "x": 0x58,
	"y": 0x59, "z": 0x5A,

	// 数字（快捷栏 1~9 是 RPG 的通用约定）
	"0": 0x30, "1": 0x31, "2": 0x32, "3": 0x33, "4": 0x34,
	"5": 0x35, "6": 0x36, "7": 0x37, "8": 0x38, "9": 0x39,

	// 功能区与修饰键
	"space": 0x20, "空格": 0x20,
	"enter": 0x0D, "回车": 0x0D, "return": 0x0D,
	"esc": 0x1B, "escape": 0x1B, "退出": 0x1B,
	"tab":    0x09,
	"shift":  0x10,
	"ctrl":   0x11,
	"alt":    0x12,
	"caps":   0x14,
	"back":   0x08, // 退格
	"delete": 0x2E,
	"insert": 0x2D,
	"home":   0x24,
	"end":    0x23,
	"pgup":   0x21,
	"pgdn":   0x22,

	// 方向键（同时保留「上下左右」中文别名，模型偶尔会直接给中文）
	"up": 0x26, "down": 0x28, "left": 0x25, "right": 0x27,
	"上": 0x26, "下": 0x28, "左": 0x25, "右": 0x27,

	// F1~F12
	"f1": 0x70, "f2": 0x71, "f3": 0x72, "f4": 0x73, "f5": 0x74,
	"f6": 0x75, "f7": 0x76, "f8": 0x77, "f9": 0x78, "f10": 0x79,
	"f11": 0x7A, "f12": 0x7B,

	// 鼠标左右键（部分游戏用鼠标键做攻击）
	"mouse_left":  0x01,
	"mouse_right": 0x02,
}

// KeyToVK 把键名翻译成虚拟键码；未知键名返回 0。
func KeyToVK(code string) uint16 {
	if code == "" {
		return 0
	}
	// 无条件做 ASCII 小写化。
	// ⚠️ 不要加「长度 ≤3 才处理」这类优化：键名里有 shift/enter/space
	// 这种长名字，漏掉它们就是「按了没反应」——单测正是这么抓到的。
	// lowerASCII 只动 A-Z，中文别名（空格/回车/上）不受影响。
	c := lowerASCII(code)
	if vk, ok := vkTable[c]; ok {
		return vk
	}
	return 0
}

// lowerASCII 只把 ASCII 大写转小写（不能直接用 strings.ToLower：
// 它会把中文也过一遍，虽然结果相同但没必要）。
func lowerASCII(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// 编译期断言：三个结构尺寸必须与 Win32 x64 定义逐字节一致，
// 否则数组长度出现负数，编译失败。
var (
	_ [int(unsafe.Sizeof(winInput{})) - 40]byte
	_ [40 - int(unsafe.Sizeof(winInput{}))]byte
	_ [int(unsafe.Sizeof(keybdInput{})) - 24]byte
	_ [24 - int(unsafe.Sizeof(keybdInput{}))]byte
	_ [int(unsafe.Sizeof(mouseInput{})) - 32]byte
	_ [32 - int(unsafe.Sizeof(mouseInput{}))]byte
)
