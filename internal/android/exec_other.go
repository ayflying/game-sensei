//go:build !windows

package android

import (
	"context"
	"os/exec"
)

// newCommand 构造 adb 调用命令（非 Windows 平台无窗口概念）。
func newCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}
