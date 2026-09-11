//go:build windows

// Package gamewin 提供「运行结束时把游戏窗口最小化」的能力（纯 syscall，零 CGO）。
//
// 用途：agent 跑完一轮后，把全屏的游戏窗口最小化，让用户能立刻看到
// 终端里的对话/评估输出，而不用手动 Alt+Tab 找窗口。
//
// 本包仅编译于 Windows。
package gamewin

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	user32              = syscall.NewLazyDLL("user32.dll")
	procEnumWindows     = user32.NewProc("EnumWindows")
	procGetWindowTextW  = user32.NewProc("GetWindowTextW")
	procIsWindowVisible = user32.NewProc("IsWindowVisible")
	procShowWindowAsync = user32.NewProc("ShowWindowAsync")
	procGetWindowLongW  = user32.NewProc("GetWindowLongW")
)

const swMinimize = 6

// gwStyleGetWindowLong 标志：GWL_STYLE
const gwlStyle = -16
const wsIconic = 0x20000000 // 已最小化

// MinimizeByTitle 把标题包含 keyword 的所有可见窗口最小化。
// 返回最小化的窗口数；找不到时返回 0 与 nil（不报错——游戏可能还没开）。
func MinimizeByTitle(keyword string) (int, error) {
	if keyword == "" {
		return 0, fmt.Errorf("gamewin: 关键词为空")
	}

	n := 0
	cb := syscall.NewCallback(func(hwnd, lParam uintptr) uintptr {
		if vis, _, _ := procIsWindowVisible.Call(hwnd); vis == 0 {
			return 1 // 继续枚举
		}
		// 已经最小化的不用再动
		style, _, _ := procGetWindowLongW.Call(hwnd, ^uintptr(15)) // GWL_STYLE = -16（两补码）
		if style&wsIconic != 0 {
			return 1
		}
		buf := make([]uint16, 256)
		n_, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if n_ == 0 {
			return 1
		}
		title := syscall.UTF16ToString(buf[:n_])
		if containsFold(title, keyword) {
			// ShowWindowAsync 而非 ShowWindow：不阻塞、不等待游戏主循环响应
			procShowWindowAsync.Call(hwnd, swMinimize)
			n++
		}
		return 1
	})
	ret, _, err := procEnumWindows.Call(cb, 0)
	if ret == 0 {
		return n, fmt.Errorf("gamewin: EnumWindows 失败: %v", err)
	}
	return n, nil
}

// containsFold 大小写不敏感的子串判断（只处理 ASCII 折叠，中文标题不受影响）。
func containsFold(s, sub string) bool {
	if len(sub) == 0 {
		return false
	}
	sLower := asciiLower(s)
	subLower := asciiLower(sub)
	for i := 0; i+len(subLower) <= len(sLower); i++ {
		if sLower[i:i+len(subLower)] == subLower {
			return true
		}
	}
	return false
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
