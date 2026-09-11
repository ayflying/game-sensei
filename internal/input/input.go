//go:build windows

// Package input 通过 Windows 的 SendInput（纯 Go syscall，零 CGO）模拟键鼠。
//
// 这是框架的「手」。Phase 0 用一个极简动作集（离散方向键），
// 后续按游戏动作空间扩展；所有真实发送受 Config.Live 开关保护（默认 dry-run）。
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
	"syscall"
	"unsafe"

	"github.com/ayflying/game-sensei/internal/agent"
)

var (
	user32        = syscall.NewLazyDLL("user32.dll")
	procSendInput = user32.NewProc("SendInput")
)

// Win32 常量。
const (
	inputMouse    = 0
	inputKeyboard = 1

	keyeventfKeyup = 0x0002

	// 方向键虚拟键码
	vkLeft  = 0x25
	vkUp    = 0x26
	vkRight = 0x27
	vkDown  = 0x28
)

// mouseInput 对应 Win32 MOUSEINPUT（x64 下 32 字节）。Phase 0.5 鼠标动作使用。
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

// Actuator 执行动作。Live=false 时只记录、不真正发送（dry-run）。
type Actuator struct {
	Live bool
}

// NewActuator 构造执行器；live=false 为安全默认。
func NewActuator(live bool) *Actuator { return &Actuator{Live: live} }

// Apply 把 agent.Action 落到键鼠。Phase 0 支持方向键点按（按下+抬起）。
func (a *Actuator) Apply(act agent.Action) error {
	if !a.Live {
		return nil // dry-run：吞掉动作，仅用于验证链路与延迟
	}
	switch act.Kind {
	case agent.ActionKey:
		vk := keyToVK(act.Code)
		if vk == 0 {
			return fmt.Errorf("input: 未知按键 %q", act.Code)
		}
		var down, up winInput
		down.Type = inputKeyboard
		down.setKeybd(keybdInput{Vk: vk})
		up.Type = inputKeyboard
		up.setKeybd(keybdInput{Vk: vk, Flags: keyeventfKeyup})
		return send([]winInput{down, up})
	default:
		return nil // Phase 0 不主动移动鼠标
	}
}

func keyToVK(code string) uint16 {
	switch code {
	case "left":
		return vkLeft
	case "up":
		return vkUp
	case "right":
		return vkRight
	case "down":
		return vkDown
	default:
		return 0
	}
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
