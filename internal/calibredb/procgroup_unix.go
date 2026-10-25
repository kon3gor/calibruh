//go:build unix

package calibredb

import (
	"os/exec"
	"syscall"
)

// killGroup makes cancellation kill calibredb's worker processes too, which
// otherwise keep running and hold its output pipe open.
func killGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
