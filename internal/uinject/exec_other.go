//go:build !windows

package uinject

import "os/exec"

func newCommand(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}
