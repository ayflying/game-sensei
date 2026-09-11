//go:build windows

package video

import (
	"context"
	"os/exec"
	"syscall"
)

// createNoWindow 让 ffmpeg / ffprobe 子进程不弹出控制台黑窗（CREATE_NO_WINDOW）。
//
// 与 internal/android 的做法一致：批处理会连着调几十次子进程，
// 每次闪一下黑窗既难看又抢焦点（抢焦点对实时回路是实打实的干扰）。
const createNoWindow = 0x08000000

// newCommand 构造外部进程调用命令，Windows 下隐藏子进程窗口。
func newCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	return cmd
}
