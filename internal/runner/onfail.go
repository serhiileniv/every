package runner

import (
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"github.com/serhiileniv/every/internal/store"
)

// onFailTimeout bounds a callback.
//
// Short on purpose. A callback is a notification, not a second task: if it
// needs longer than this it should be scheduling work rather than doing it,
// and a slow one must not delay the next run of the task it is reporting on.
const onFailTimeout = 30 * time.Second

// runOnFail invokes a task's --on-fail command after a failed run.
//
// The rules follow the notification path, for the same reason: a callback that
// fails must never turn an already-recorded task failure into a second one.
// So it runs AFTER the ledger is written, its output goes into the same log
// rather than anywhere new, it gets a bounded timeout of its own, and its exit
// code is recorded but never propagated -- the task's exit code is the task's,
// and a broken notifier must not disguise a working task or vice versa.
//
// Per task rather than global config: the callback for a failing backup is
// rarely the one for a failing sync, and a global hook makes every task pay
// for the noisiest one. It also lives in the task record, which already
// survives an upgrade, instead of a second kind of state with its own
// migration story.
func (r *Runner) runOnFail(name string, task *store.Task, exitCode int) {
	if task.OnFail == "" {
		return
	}

	dir, _ := r.workdir(task.Cwd)

	// The failing task is described through the environment rather than by
	// interpolating into the command string: a task name is sanitized, but a
	// log path is not, and building a shell command out of paths is how
	// injection happens.
	env := []string{
		"EVERY_TASK=" + name,
		"EVERY_EXIT=" + strconv.Itoa(exitCode),
		"EVERY_LOG=" + filepath.Join(r.Dirs.Logs, name+".log"),
		"EVERY_HOME=" + r.Dirs.Data,
	}

	res, err := r.captureWithEnv(task.OnFail, dir, onFailTimeout, env)
	if err != nil {
		r.appendOnFailNote(name, fmt.Sprintf("on-fail callback could not start: %v", err))
		return
	}

	note := fmt.Sprintf("on-fail callback exited %d", res.ExitCode)
	if len(res.Output) > 0 {
		note += "\n" + string(res.Output)
	}
	r.appendOnFailNote(name, note)
}

// appendOnFailNote records what the callback did, in the task's own log.
//
// Nowhere new: a second log file would be a second thing to find, rotate and
// explain, and the callback's output belongs beside the failure that caused it.
func (r *Runner) appendOnFailNote(name, note string) {
	_ = r.appendLogRaw(name, "--- on-fail ---\n"+note+"\n")
}
