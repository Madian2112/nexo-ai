//go:build !windows

package hookrun

import (
	"context"
	"os/exec"
	"syscall"
)

// shell runs command through sh in its own process group, so a timeout kills the
// command's children too instead of leaving them holding the output pipes.
func shell(ctx context.Context, command string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	return cmd
}
