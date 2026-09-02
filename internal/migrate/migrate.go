// Package migrate repairs scheduler units left behind by an older every.
//
// Units written before 0.4 invoke the tool through a Ruby interpreter:
//
//	[/usr/bin/ruby, /usr/local/bin/every, run, backup]
//
// After an upgrade the launcher at that path is a compiled binary. Ruby is
// handed it, fails to parse it as a script, and the task stops firing --
// silently, because the failure notification lives inside every's own runner,
// which never loads. Every scheduled task a user has would quietly stop, and
// the first they would know is a backup that had not run for a month.
//
// So the units are repaired automatically, from any command that is already
// reading the store. The alternative -- telling people to run a fix -- reaches
// exactly the users who read release notes, and the ones it misses are the ones
// whose tasks are already dead.
package migrate

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/serhiileniv/every/internal/backend"
	"github.com/serhiileniv/every/internal/paths"
	"github.com/serhiileniv/every/internal/schedule"
	"github.com/serhiileniv/every/internal/store"
)

// stampName records what the units were last generated for. When it matches,
// there is nothing to do and no scan happens at all -- so the check costs one
// small file read on the overwhelmingly common path.
const stampName = ".runtime"

// Result describes what a migration pass did.
type Result struct {
	Repaired []string
	// Failed maps a task name to why it could not be repaired. A task that
	// cannot be fixed must not stop the others from being fixed.
	Failed map[string]error
}

func (r Result) Any() bool { return len(r.Repaired) > 0 || len(r.Failed) > 0 }

// Run repairs any unit that does not match what this version would generate.
//
// It is idempotent by construction: rather than pattern-matching the old
// format, it regenerates each unit from the store and compares. That catches
// the Ruby-argv case it was written for, and equally any future change to unit
// contents, without needing to know what the old one looked like.
func Run(dirs paths.Dirs, b backend.Backend, launcher, version string) Result {
	res := Result{Failed: map[string]error{}}

	stamp := filepath.Join(dirs.Data, stampName)
	want := version + "\n" + launcher + "\n"
	if current, err := os.ReadFile(stamp); err == nil && string(current) == want {
		return res
	}

	s, err := store.Load(dirs.Data)
	if err != nil {
		// A store we cannot read is not a migration problem; the command the
		// caller is running will report it properly.
		return res
	}

	for _, name := range s.Tasks.Names() {
		task, _ := s.Tasks.Get(name)
		if task.Paused {
			// A paused task has no unit by design. Resuming rewrites it.
			continue
		}
		repaired, err := repair(b, name, task)
		switch {
		case err != nil:
			res.Failed[name] = err
		case repaired:
			res.Repaired = append(res.Repaired, name)
		}
	}

	// Stamp only when nothing failed, so a partial repair is retried next time
	// rather than being remembered as done.
	if len(res.Failed) == 0 {
		_ = os.MkdirAll(dirs.Data, 0o755)
		_ = os.WriteFile(stamp, []byte(want), 0o644)
	}

	cleanupRubyRuntime(dirs)
	return res
}

// repair rewrites and re-registers one task if its unit is stale.
func repair(b backend.Backend, name string, task *store.Task) (bool, error) {
	sched, err := schedule.FromRecord(task.Schedule)
	if err != nil {
		return false, err
	}

	current, err := currentUnit(b, name)
	if err != nil {
		return false, err
	}
	fresh, err := freshUnit(b, name, sched)
	if err != nil {
		return false, err
	}
	if current != "" && current == fresh {
		return false, nil
	}

	if err := b.Write(name, sched); err != nil {
		return false, err
	}
	// Re-register: launchd and Task Scheduler hold a copy of the definition,
	// so rewriting the file alone changes nothing until the service reloads it.
	if err := b.Disable(name); err != nil {
		return false, err
	}
	if err := b.Enable(name); err != nil {
		return false, err
	}
	return true, nil
}

// currentUnit reads what is on disk, or "" when there is nothing to compare.
func currentUnit(b backend.Backend, name string) (string, error) {
	raw, err := os.ReadFile(b.UnitPath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(raw), nil
}

// freshUnit renders what this version would write, without touching the
// scheduler or the real unit path.
func freshUnit(b backend.Backend, name string, s *schedule.Schedule) (string, error) {
	type renderer interface {
		Render(name string, s *schedule.Schedule) (string, error)
	}
	if r, ok := b.(renderer); ok {
		return r.Render(name, s)
	}
	return "", fmt.Errorf("backend %T cannot render a unit for comparison", b)
}

// cleanupRubyRuntime removes the mirrored Ruby tree an older every kept for
// TCC-protected installs. It is dead weight now -- there is one binary and
// nothing to mirror -- and leaving a stale interpreter tree in the data dir
// invites someone to wonder whether it is still load-bearing.
func cleanupRubyRuntime(dirs paths.Dirs) {
	runtimeDir := filepath.Join(dirs.Data, "runtime")
	if _, err := os.Stat(filepath.Join(runtimeDir, "lib", "every.rb")); err != nil {
		return // not the old layout; leave whatever this is alone
	}
	_ = os.RemoveAll(runtimeDir)
}

// Report writes a one-line summary, and nothing at all when there was nothing
// to do. A migration that announces itself on every invocation is noise.
func Report(w io.Writer, res Result) {
	if n := len(res.Repaired); n > 0 {
		fmt.Fprintf(w, "· repaired %d scheduled task%s for the %s runtime\n",
			n, plural(n), shortVersion)
	}
	for name, err := range res.Failed {
		fmt.Fprintf(w, "· could not repair %s: %v\n", name, err)
		fmt.Fprintf(w, "  → re-create it: every rm %s && every <schedule> -- <cmd>\n", name)
	}
}

// shortVersion is what the repair line names, deliberately the series rather
// than the exact patch: the user cares that it is the new runtime, not which
// point release did it.
const shortVersion = "0.4"

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
