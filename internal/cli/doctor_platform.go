package cli

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"runtime"
	"strings"

	"github.com/serhiileniv/every/internal/backend"
)

// doctorPlatform checks that the platform's scheduler is reachable at all.
//
// It comes first because every later check is meaningless without it: a task
// can be perfectly defined and still never fire if there is no session to fire
// it in. Each platform fails in its own way, so each gets its own wording.
func (c *CLI) doctorPlatform(check func(string, bool, string)) {
	switch runtime.GOOS {
	case "darwin":
		uid := os.Getuid()
		err := exec.Command("launchctl", "print", fmt.Sprintf("gui/%d", uid)).Run()
		check(fmt.Sprintf("launchd user session reachable (gui/%d)", uid), err == nil,
			"no GUI session — launchd agents don't run over bare SSH sessions")

	case "windows":
		ok := false
		if ts, isTS := c.Backend.(*backend.TaskScheduler); isTS {
			out, err := ts.SchedulerStatus()
			// STATE : 4 is RUNNING. Matched numerically rather than by the
			// adjacent word, which is localized.
			ok = err == nil && regexp.MustCompile(`(?i)STATE\s*:\s*4\b`).MatchString(out)
		}
		check("Windows Task Scheduler service running", ok,
			"start the Task Scheduler service, then run `every doctor` again")

	case "linux":
		out, err := exec.Command("systemctl", "--user", "is-system-running").CombinedOutput()
		// "degraded" still schedules: some unrelated unit failed, which is not
		// every's problem and not a reason to report a broken session.
		ok := err == nil || strings.Contains(string(out), "degraded")
		check("systemd user session reachable", ok,
			"no user systemd — is this a desktop/loginctl session?")

		// The username has to be resolved properly, not just read from $USER.
		// The implementation being replaced used $USER alone, which is empty in
		// plenty of real contexts -- a bare `docker exec`, some terminal
		// emulators, anything not started by a login shell. The result was
		// `loginctl show-user ""`, which always fails, so doctor reported a
		// problem on a perfectly healthy machine and advised
		// "loginctl enable-linger" with no name. Caught by running the suite
		// against real systemd in a container.
		user := currentUsername()
		if user == "" {
			// Better to say nothing than to fail a check we cannot perform or
			// give advice we cannot complete.
			break
		}
		lingerOut, _ := exec.Command("loginctl", "show-user", user, "-p", "Linger").CombinedOutput()
		check("lingering enabled (timers fire at boot / after logout)",
			strings.Contains(string(lingerOut), "Linger=yes"),
			fmt.Sprintf("run: loginctl enable-linger %s", user))
	}
}

// currentUsername resolves who we are, falling back through the environment to
// the passwd database. os/user works without cgo -- it parses /etc/passwd --
// so this stays correct in a static binary.
func currentUsername() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u := os.Getenv("LOGNAME"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}
