//go:build windows

package uinject

import (
	"os/exec"
	"syscall"
)

// newCommand 在 Windows 上隐藏子进程窗口：跑批时不断闪黑框会打断人工核对。
func newCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}
