//go:build !windows

package video

import (
	"context"
	"os/exec"
)

// newCommand 构造外部进程调用命令（非 Windows 平台无窗口概念）。
func newCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}
