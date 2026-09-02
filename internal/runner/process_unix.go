//go:build !windows

package runner

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// setProcessGroup makes the child its own process-group leader, so a negative
// pid signals the whole tree.
//
// Negative process-group signals are a POSIX primitive. Windows uses
// taskkill instead, so this is not set there.
func setProcessGroup(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminate kills the whole process tree.
//
// With the child as its own group leader, a negative pid signals the group --
// no getpgid call and so no race against the child being reaped. TERM first,
// then KILL unconditionally after a grace period: a shell that ignores TERM
// still has to die, or a hung task blocks its own next run.
//
// exec.CommandContext is not used for this: it kills only the direct child,
// leaving the command's own children running.
func terminate(p *os.Process) {
	if p == nil {
		return
	}
	pgid := -p.Pid
	// ESRCH (already gone) and EPERM are not failures worth reporting: the
	// goal is that the tree is dead, and both mean it is or we cannot help.
	if err := syscall.Kill(pgid, syscall.SIGTERM); err != nil {
		return
	}
	time.Sleep(300 * time.Millisecond)
	_ = syscall.Kill(pgid, syscall.SIGKILL)
}

// terminatingSignal reports the signal that killed the process, if one did.
func terminatingSignal(state *os.ProcessState) (int, bool) {
	ws, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return 0, false
	}
	return int(ws.Signal()), true
}
