package runner

import (
	"fmt"
	"os/exec"
)

// notifyFailure tells the user a scheduled task failed.
//
// Failures notify by default because the product is visibility: a FAIL that
// waits for the user to run `list` is still silence. --quiet opts out per task.
//
// Every notifier here is best-effort. A missing notify-send, a machine with no
// interactive session, a locked screen -- none of those may turn an
// already-recorded task failure into a second failure, so errors are ignored
// deliberately rather than by omission.
func (r *Runner) notifyFailure(name string, exitCode int) {
	msg := fmt.Sprintf("%s failed (exit %d) — every log %s", name, exitCode, name)

	switch r.goos {
	case "darwin":
		script := fmt.Sprintf(`display notification "%s" with title "every"`, osaEscape(msg))
		_ = exec.Command("osascript", "-e", script).Run()
	case "windows":
		r.notifyWindows(msg)
	default:
		_ = exec.Command("notify-send", "every", msg).Run()
	}
}

// notifyWindows is a best-effort message to the current interactive user.
//
// Windows has no inbox notification utility guaranteed across editions. msg.exe
// is absent on Home, and present-but-refusing on a machine with no interactive
// session, so both the lookup and the run are allowed to fail silently.
func (r *Runner) notifyWindows(msg string) {
	user := r.env("USERNAME")
	if user == "" {
		return
	}
	_ = exec.Command("msg.exe", user, "/TIME:5", msg).Run()
}
