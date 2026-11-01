//go:build windows

package hookrun

import (
	"context"
	"os/exec"
	"syscall"
)

// shell runs command through cmd.exe with the raw command line: exec.Command would
// re-quote it, and cmd.exe parses quotes differently from other programs.
func shell(ctx context.Context, command string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /S /C "` + command + `"`}
	return cmd
}
