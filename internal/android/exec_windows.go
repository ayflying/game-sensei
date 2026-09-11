//go:build windows

package android

import (
	"context"
	"os/exec"
	"syscall"
)

// createNoWindow 让 adb 子进程不弹出控制台黑窗（CREATE_NO_WINDOW）。
const createNoWindow = 0x08000000

// newCommand 构造 adb 调用命令，Windows 下隐藏子进程窗口。
func newCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	return cmd
}
