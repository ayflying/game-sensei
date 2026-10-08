//go:build linux

// Command uinject-device 是设备侧的 uinput 触摸注入守护进程（只在 Android/arm64 上编译）。
//
// 为什么需要它：部分 ROM（实测 Redmi K50 / Android 14 / HyperOS V816）对 adb shell
// 关闭了 INJECT_EVENTS，`adb shell input tap|swipe|keyevent` 直接抛
//
//	java.lang.SecurityException: Injecting input events requires the caller ... INJECT_EVENTS permission
//
// `sendevent` 也被 SELinux 拒绝；但 /dev/uinput 仍对 shell 组可写。于是自己建一块
// 虚拟触摸屏，由内核 input 子系统投递触摸事件——只是换了一个合法的输入设备，
// 不读内存、不接封包、不绕反作弊。
//
// 协议（stdin 逐行、stdout 逐行，便于宿主用 adb shell 直接驱动）：
//
//	启动即打印：READY rot=NRXxNRY raw=RXxRY
//	tap X Y [MS]                 点击（默认 60ms）
//	swipe X1 Y1 X2 Y2 MS [STEPS] 滑动
//	down X Y                     按住
//	move X Y                     拖动
//	up                           抬起
//	key CODE|NAME                键盘按键（如 wakeup=143，仅 -kb 时可用）
//	quit                         退出
//
// 每条命令回答 `OK <原命令>` 或 `ERR <原因>`；坐标一律是「当前旋转下的屏幕像素」。
package main

import (
	"bufio"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/ayflying/game-sensei/internal/uinject/coord"
)

// Linux input 子系统常量（不依赖外部头文件，保持零 CGO）。
const (
	evSyn = 0x00
	evKey = 0x01
	evAbs = 0x03

	synReport = 0x00

	btnTouch = 0x14a // BTN_TOUCH

	absMTSlot       = 0x2f
	absMTPositionX  = 0x35
	absMTPositionY  = 0x36
	absMTTrackingID = 0x39
	absMTPressure   = 0x3a

	inputPropDirect = 0x01

	busVirtual = 0x06

	uiDevCreate  = 0x5501
	uiDevDestroy = 0x5502
	uiDevSetup   = 0x405c5503
	uiAbsSetup   = 0x401c5504
	uiSetEvBit   = 0x40045564
	uiSetKeyBit  = 0x40045565
	uiSetAbsBit  = 0x40045567
	uiSetPropBit = 0x4004556e

	evMax = 0x1f

	// 跟踪 id 与压力值域：0..65535 / 0..255，够用且与常见 .kl 兼容。
	trackingMax = 65535
	pressureMax = 255
	slotMax     = 9
)

// keyAliases 是常用 Linux 键码别名（注意：这是 Linux keycode，不是 Android keycode；
// Generic.kl 会把 143 映射成 Android 的 WAKEUP）。
var keyAliases = map[string]int{
	"wakeup":     143,
	"power":      116,
	"enter":      28,
	"back":       158,
	"home":       172,
	"menu":       139,
	"volumeup":   115,
	"volumedown": 114,
	"space":      57,
	"tab":        15,
}

// defaultKeys 是虚拟键盘默认使能的键码：唤醒/电源/常用导航与数字键。
var defaultKeys = []int{143, 116, 28, 158, 172, 139, 115, 114, 57, 15, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}

func ioctlPtr(fd uintptr, req uintptr, arg unsafe.Pointer) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg)); errno != 0 {
		return errno
	}
	return nil
}

// ioctlVal 用于「按值传参」的 uinput ioctl（UI_SET_EVBIT / UI_SET_KEYBIT /
// UI_SET_ABSBIT / UI_SET_PROPBIT / UI_DEV_CREATE / UI_DEV_DESTROY）。
//
// 这里踩过一次坑：新版内核这几个命令的 arg 是**值本身**（uinput_set_bit(arg, ...)），
// 把值当指针传会得到 EINVAL（invalid argument），看起来完全不像参数用法问题。
func ioctlVal(fd uintptr, req uintptr, v int32) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(uint32(v))); errno != 0 {
		return errno
	}
	return nil
}

func openUinput() (*os.File, error) {
	f, err := os.OpenFile("/dev/uinput", os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("打开 /dev/uinput 失败（该 ROM 可能禁止 shell 使用 uinput）: %w", err)
	}
	return f, nil
}

// wantEvBits 是本设备真正用得到的 EV 类型。
//
// 坑（已踩）：不能图省事把 0..EV_MAX 全部打开。EV_FF 被打开而 ff_effects_max=0 时，
// 内核在建设备阶段直接返回 EINVAL，报错只有 `UI_DEV_CREATE 失败: invalid argument`，
// 完全看不出是「多开了一个 EV 位」导致的。
var wantEvBits = []int{evSyn, evKey, evAbs}

// setEvBits 逐位使能指定的 EV 类型（每位一个 ioctl，且参数传值而非指针）。
func setEvBits(fd uintptr, bits []int) error {
	for _, b := range bits {
		if err := ioctlVal(fd, uiSetEvBit, int32(b)); err != nil {
			return fmt.Errorf("使能 EV 类型 %d 失败: %w", b, err)
		}
	}
	return nil
}

// absSetup 填 struct uinput_abs_setup{code(u16)+pad(2) + input_absinfo{6×i32}}。
func absSetup(fd uintptr, code int32, max int32) error {
	var buf [28]byte
	binary.LittleEndian.PutUint16(buf[0:], uint16(code))
	// value/min 保持 0，max 在偏移 12 处，fuzz/flat/resolution 留 0。
	binary.LittleEndian.PutUint32(buf[12:], uint32(max))
	return ioctlPtr(fd, uiAbsSetup, unsafe.Pointer(&buf[0]))
}

// devSetup 填 struct uinput_setup{input_id{4×u16} + name[80] + ff_effects_max(u32)}。
func devSetup(fd uintptr, name string, vendor, product, version uint16) error {
	var buf [92]byte
	binary.LittleEndian.PutUint16(buf[0:], busVirtual)
	binary.LittleEndian.PutUint16(buf[2:], vendor)
	binary.LittleEndian.PutUint16(buf[4:], product)
	binary.LittleEndian.PutUint16(buf[6:], version)
	copy(buf[8:88], name)
	return ioctlPtr(fd, uiDevSetup, unsafe.Pointer(&buf[0]))
}

func createTouch(name string, rx, ry int) (*os.File, error) {
	f, err := openUinput()
	if err != nil {
		return nil, err
	}
	fd := f.Fd()
	if err := setEvBits(fd, wantEvBits); err != nil {
		f.Close()
		return nil, err
	}
	if err := ioctlVal(fd, uiSetKeyBit, btnTouch); err != nil {
		f.Close()
		return nil, fmt.Errorf("使能 BTN_TOUCH 失败: %w", err)
	}
	if err := ioctlVal(fd, uiSetPropBit, inputPropDirect); err != nil {
		f.Close()
		return nil, fmt.Errorf("设置 INPUT_PROP_DIRECT 失败: %w", err)
	}
	for _, a := range []int32{absMTSlot, absMTPositionX, absMTPositionY, absMTTrackingID, absMTPressure} {
		if err := ioctlVal(fd, uiSetAbsBit, a); err != nil {
			f.Close()
			return nil, fmt.Errorf("使能 ABS 轴 0x%x 失败: %w", a, err)
		}
	}
	for _, s := range []struct {
		code int32
		max  int32
	}{
		{absMTPositionX, int32(rx)},
		{absMTPositionY, int32(ry)},
		{absMTSlot, slotMax},
		{absMTTrackingID, trackingMax},
		{absMTPressure, pressureMax},
	} {
		if err := absSetup(fd, s.code, s.max); err != nil {
			f.Close()
			return nil, fmt.Errorf("设置 ABS 轴 0x%x 值域失败: %w", s.code, err)
		}
	}
	if err := devSetup(fd, name, 0x4e52, 0x0001, 1); err != nil {
		f.Close()
		return nil, fmt.Errorf("创建虚拟设备失败: %w", err)
	}
	if err := ioctlVal(fd, uiDevCreate, 0); err != nil {
		f.Close()
		return nil, fmt.Errorf("UI_DEV_CREATE 失败: %w", err)
	}
	return f, nil
}

func createKeyboard(name string, keys []int) (*os.File, error) {
	f, err := openUinput()
	if err != nil {
		return nil, err
	}
	fd := f.Fd()
	// 键盘只要 EV_SYN + EV_KEY，同样不能全开。
	if err := setEvBits(fd, []int{evSyn, evKey}); err != nil {
		f.Close()
		return nil, err
	}
	for _, k := range keys {
		if err := ioctlVal(fd, uiSetKeyBit, int32(k)); err != nil {
			f.Close()
			return nil, fmt.Errorf("使能按键 %d 失败: %w", k, err)
		}
	}
	if err := devSetup(fd, name, 0x4e52, 0x0002, 1); err != nil {
		f.Close()
		return nil, fmt.Errorf("创建虚拟键盘失败: %w", err)
	}
	if err := ioctlVal(fd, uiDevCreate, 0); err != nil {
		f.Close()
		return nil, fmt.Errorf("UI_DEV_CREATE(键盘) 失败: %w", err)
	}
	return f, nil
}

// emit 写一个 input_event（24 字节：sec/usec + type/code/value）。
func emit(f *os.File, typ, code uint16, val int32) error {
	if f == nil {
		// 踩过的坑：句柄为 nil 时 os.File.Write 返回裸 os.ErrInvalid，
		// 字面量就是 "invalid argument"，和内核 uinput 拒绝事件时的 EINVAL
		// 一模一样，非常容易误判成「内核不接受这类事件」。
		return fmt.Errorf("内部错误：事件句柄为空（虚拟设备未绑定）")
	}
	var buf [24]byte
	now := time.Now()
	binary.LittleEndian.PutUint64(buf[0:], uint64(now.Unix()))
	binary.LittleEndian.PutUint64(buf[8:], uint64(now.Nanosecond()/1000))
	binary.LittleEndian.PutUint16(buf[16:], typ)
	binary.LittleEndian.PutUint16(buf[18:], code)
	binary.LittleEndian.PutUint32(buf[20:], uint32(val))
	if _, err := f.Write(buf[:]); err != nil {
		// 把事件内容一起报出来：内核只回 EINVAL（invalid argument），
		// 不说是哪一条事件、哪个 type/code 被拒，定位起来很费劲。
		return fmt.Errorf("写事件 type=0x%x code=0x%x value=%d 失败: %w", typ, code, val, err)
	}
	return nil
}

func emitSyn(f *os.File) error { return emit(f, evSyn, synReport, 0) }

// injector 持有虚拟触摸屏，并把屏幕像素换算成原始坐标。
type injector struct {
	f    *os.File
	rot  int
	w, h int
	rx   int
	ry   int
}

func (in *injector) raw(x, y int) (int32, int32) {
	return coord.Rotate(in.rot, in.w, in.h, in.rx, in.ry, x, y)
}

func (in *injector) down(x, y int) error {
	px, py := in.raw(x, y)
	steps := []struct {
		typ  uint16
		code uint16
		val  int32
	}{
		{evAbs, absMTSlot, 0},
		{evAbs, absMTTrackingID, 1},
		{evKey, btnTouch, 1},
		{evAbs, absMTPositionX, px},
		{evAbs, absMTPositionY, py},
		{evAbs, absMTPressure, 128},
	}
	for _, s := range steps {
		if err := emit(in.f, s.typ, s.code, s.val); err != nil {
			return err
		}
	}
	return emitSyn(in.f)
}

func (in *injector) move(x, y int) error {
	px, py := in.raw(x, y)
	if err := emit(in.f, evAbs, absMTPositionX, px); err != nil {
		return err
	}
	if err := emit(in.f, evAbs, absMTPositionY, py); err != nil {
		return err
	}
	return emitSyn(in.f)
}

func (in *injector) up() error {
	if err := emit(in.f, evAbs, absMTTrackingID, -1); err != nil {
		return err
	}
	if err := emit(in.f, evKey, btnTouch, 0); err != nil {
		return err
	}
	return emitSyn(in.f)
}

func (in *injector) tap(x, y, ms int) error {
	if err := in.down(x, y); err != nil {
		return err
	}
	time.Sleep(time.Duration(ms) * time.Millisecond)
	return in.up()
}

// swipe 用分步 move 模拟人手：一步到位容易被上层识别成跳变。
func (in *injector) swipe(x1, y1, x2, y2, ms, steps int) error {
	if steps < 2 {
		steps = 2
	}
	if steps > 120 {
		steps = 120
	}
	if err := in.down(x1, y1); err != nil {
		return err
	}
	per := time.Duration(ms) * time.Millisecond / time.Duration(steps)
	for i := 1; i < steps; i++ {
		x := x1 + (x2-x1)*i/steps
		y := y1 + (y2-y1)*i/steps
		if err := in.move(x, y); err != nil {
			return err
		}
		if per > 0 {
			time.Sleep(per)
		}
	}
	if err := in.move(x2, y2); err != nil {
		return err
	}
	return in.up()
}

func (in *injector) key(f *os.File, code int) error {
	if f == nil {
		return fmt.Errorf("未启用虚拟键盘（启动时加 -kb）")
	}
	if err := emit(f, evKey, uint16(code), 1); err != nil {
		return err
	}
	if err := emitSyn(f); err != nil {
		return err
	}
	time.Sleep(40 * time.Millisecond)
	if err := emit(f, evKey, uint16(code), 0); err != nil {
		return err
	}
	return emitSyn(f)
}

func main() {
	var (
		rot     = flag.Int("rot", 0, "显示旋转角度 0/90/180/270")
		w       = flag.Int("w", 1080, "当前旋转下的屏幕逻辑宽（像素）")
		h       = flag.Int("h", 2400, "当前旋转下的屏幕逻辑高（像素）")
		rx      = flag.Int("rx", 1080, "虚拟设备原始 X 值域上限（默认物理竖屏宽）")
		ry      = flag.Int("ry", 2400, "虚拟设备原始 Y 值域上限（默认物理竖屏高）")
		name    = flag.String("name", "gs-touch", "虚拟触摸屏设备名")
		kb      = flag.Bool("kb", false, "同时创建虚拟键盘（用于唤醒键等）")
		delayed = flag.Bool("delayed", false, "创建后等待 500ms 再打印 READY（给 InputReader 注册留时间）")
	)
	flag.Parse()

	in := &injector{rot: coord.NormalizeRot(*rot), w: *w, h: *h, rx: *rx, ry: *ry}

	f, err := createTouch(*name, in.rx, in.ry)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERR "+err.Error())
		os.Exit(1)
	}
	// 必须把创建出来的触摸设备句柄绑到 injector 上，
	// 否则所有触摸事件都写进 nil 文件句柄（报错是 Go 的 os.ErrInvalid）。
	in.f = f
	defer func() {
		_ = ioctlVal(f.Fd(), uiDevDestroy, 0)
		f.Close()
	}()

	var kbf *os.File
	if *kb {
		kbf, err = createKeyboard(*name+"-kb", defaultKeys)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ERR "+err.Error())
			os.Exit(1)
		}
		defer func() {
			_ = ioctlVal(kbf.Fd(), uiDevDestroy, 0)
			kbf.Close()
		}()
	}

	if *delayed {
		time.Sleep(500 * time.Millisecond)
	}
	fmt.Printf("READY rot=%d %dx%d raw=%dx%d kb=%v\n", in.rot, in.w, in.h, in.rx, in.ry, kbf != nil)

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if line == "quit" {
			fmt.Println("OK quit")
			return
		}
		if err := runLine(in, kbf, line); err != nil {
			fmt.Println("ERR " + err.Error())
			continue
		}
		fmt.Println("OK " + line)
	}
}

func runLine(in *injector, kbf *os.File, line string) error {
	fields := strings.Fields(line)
	cmd := strings.ToLower(fields[0])
	args := fields[1:]
	need := func(n int) error {
		if len(args) < n {
			return fmt.Errorf("%s 需要 %d 个参数，收到 %d 个", cmd, n, len(args))
		}
		return nil
	}
	atoi := func(s string) (int, error) {
		v, err := strconv.Atoi(s)
		if err != nil {
			return 0, fmt.Errorf("参数 %q 不是整数", s)
		}
		return v, nil
	}

	switch cmd {
	case "tap":
		if err := need(2); err != nil {
			return err
		}
		x, err := atoi(args[0])
		if err != nil {
			return err
		}
		y, err := atoi(args[1])
		if err != nil {
			return err
		}
		ms := 60
		if len(args) >= 3 {
			if ms, err = atoi(args[2]); err != nil {
				return err
			}
		}
		return in.tap(x, y, ms)
	case "swipe":
		if err := need(5); err != nil {
			return err
		}
		nums := make([]int, 5)
		for i := range nums {
			v, err := atoi(args[i])
			if err != nil {
				return err
			}
			nums[i] = v
		}
		steps := 0
		if len(args) >= 6 {
			var err error
			if steps, err = atoi(args[5]); err != nil {
				return err
			}
		}
		if steps == 0 {
			steps = nums[4] / 16
		}
		return in.swipe(nums[0], nums[1], nums[2], nums[3], nums[4], steps)
	case "down":
		if err := need(2); err != nil {
			return err
		}
		x, err := atoi(args[0])
		if err != nil {
			return err
		}
		y, err := atoi(args[1])
		if err != nil {
			return err
		}
		return in.down(x, y)
	case "move":
		if err := need(2); err != nil {
			return err
		}
		x, err := atoi(args[0])
		if err != nil {
			return err
		}
		y, err := atoi(args[1])
		if err != nil {
			return err
		}
		return in.move(x, y)
	case "up":
		return in.up()
	case "key":
		if err := need(1); err != nil {
			return err
		}
		code, err := resolveKey(args[0])
		if err != nil {
			return err
		}
		return in.key(kbf, code)
	case "ev":
		// 原始事件注入（诊断用）：ev TYPE CODE VALUE，支持 0x 前缀。
		if err := need(3); err != nil {
			return err
		}
		t, err := strconv.ParseInt(args[0], 0, 32)
		if err != nil {
			return fmt.Errorf("type %q 不是整数（可用 0x 前缀）", args[0])
		}
		c, err := strconv.ParseInt(args[1], 0, 32)
		if err != nil {
			return fmt.Errorf("code %q 不是整数（可用 0x 前缀）", args[1])
		}
		v, err := strconv.ParseInt(args[2], 0, 32)
		if err != nil {
			return fmt.Errorf("value %q 不是整数（可用 0x 前缀）", args[2])
		}
		return emit(in.f, uint16(t), uint16(c), int32(v))
	default:
		return fmt.Errorf("未知命令 %q", cmd)
	}
}

func resolveKey(spec string) (int, error) {
	if v, err := strconv.Atoi(spec); err == nil {
		return v, nil
	}
	if v, ok := keyAliases[strings.ToLower(spec)]; ok {
		return v, nil
	}
	return 0, fmt.Errorf("未知按键 %q（可用别名或 Linux keycode）", spec)
}
