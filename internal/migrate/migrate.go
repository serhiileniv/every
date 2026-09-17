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
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/serhiileniv/every/internal/backend"
	"github.com/serhiileniv/every/internal/naming"
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
	// Relaunched is the launcher the units were re-pointed at, set only when
	// the repair happened because the launcher moved rather than because the
	// unit format changed. The two deserve different words.
	Relaunched string
	// SkippedLauncher is a launcher the pass refused to re-point at, and
	// KeptLauncher the one the units go on using. See durableLauncher.
	SkippedLauncher string
	KeptLauncher    string
	// Disarmed names the missed one-shots this pass unloaded. See
	// disarmMissedOnce.
	Disarmed []string
}

func (r Result) Any() bool {
	return len(r.Repaired) > 0 || len(r.Failed) > 0 ||
		r.SkippedLauncher != "" || len(r.Disarmed) > 0
}

// Run repairs any unit that does not match what this version would generate.
//
// It is idempotent by construction: rather than pattern-matching the old
// format, it regenerates each unit from the store and compares. That catches
// the Ruby-argv case it was written for, and equally any future change to unit
// contents, without needing to know what the old one looked like.
// `now` is injected rather than read from the clock: whether a one-shot has
// passed is a question about the caller's idea of the time, and the CLI's is
// pinned in tests.
func Run(dirs paths.Dirs, b backend.Backend, launcher, version string, now time.Time) Result {
	res := Result{Failed: map[string]error{}}

	s, err := store.Load(dirs.Data)
	if err != nil {
		// A store we cannot read is not a migration problem; the command the
		// caller is running will report it properly.
		return res
	}

	// Ahead of the stamp check, because this condition is made by the clock
	// rather than by anything every wrote: a one-shot becomes missed while
	// nothing on disk changes, so a stamp that still matches would hide it
	// forever. The cost is the store read above on the settled path, where the
	// stamp used to buy a single small file read -- and it is a read the
	// commands that reach here are about to do anyway.
	res.Disarmed = disarmMissedOnce(b, s, dirs.Data, now)

	stamp := filepath.Join(dirs.Data, stampName)
	want := stampContent(dirs, version, launcher)
	if current, err := os.ReadFile(stamp); err == nil && string(current) == want {
		return res
	}

	// Every unit this pass writes embeds `launcher`, so a pass run from the
	// wrong binary re-points every task at it. Refuse when that would move the
	// units to a launcher the shell cannot find -- see durableLauncher -- and
	// return before the stamp is written, so the recorded launcher keeps
	// naming the real install.
	recorded, wasRecorded := RecordedLauncher(dirs)
	moved := wasRecorded && recorded != launcher
	if moved && !durableLauncher(launcher) {
		res.SkippedLauncher, res.KeptLauncher = launcher, recorded
		return res
	}

	deferred := false
	for _, name := range s.Tasks.Names() {
		task, _ := s.Tasks.Get(name)
		if task.Paused {
			// A paused task has no unit by design. Resuming rewrites it.
			continue
		}
		// Never turn an unsafe name into a path, even to repair it. Reported
		// rather than skipped silently, so the user learns the task exists and
		// is unusable instead of wondering why it never fires.
		if err := naming.Validate(name); err != nil {
			res.Failed[name] = err
			continue
		}
		// Re-registering unloads first, and launchd answers that by killing a
		// running job -- after an upgrade, the first scheduled fire of each
		// task would repair itself and die. Left for a later pass instead.
		if store.Running(dirs.Data, name) {
			deferred = true
			continue
		}
		repaired, err := repair(b, name, task, now)
		switch {
		case err != nil:
			res.Failed[name] = err
		case repaired:
			res.Repaired = append(res.Repaired, name)
		}
	}

	if moved && len(res.Repaired) > 0 {
		res.Relaunched = launcher
	}

	// Stamp only when nothing failed or was deferred, so a partial repair is
	// retried next time rather than being remembered as done. Recomputed rather
	// than reusing `want`, because repairing may itself have touched the store.
	if len(res.Failed) == 0 && !deferred {
		_ = os.MkdirAll(dirs.Data, 0o755)
		_ = os.WriteFile(stamp, []byte(stampContent(dirs, version, launcher)), 0o644)
	}

	cleanupRubyRuntime(dirs)
	return res
}

// repair rewrites and re-registers one task if its unit is stale.
func repair(b backend.Backend, name string, task *store.Task, now time.Time) (bool, error) {
	sched, err := schedule.FromRecord(task.Schedule)
	if err != nil {
		return false, err
	}
	// A once task whose moment has passed is either about to retire itself
	// or was missed. Re-registering it would arm launchd for next year.
	if sched.Kind == schedule.Once && !sched.At.After(now) {
		return false, nil
	}

	current, err := currentUnit(b, name)
	if err != nil {
		return false, err
	}
	// No unit at all is not something to repair. A task can be in the store
	// with nothing scheduled -- the user unloaded it by hand, a previous add
	// failed halfway, the scheduler dropped it -- and creating one here would
	// silently resurrect it. That case is `doctor`'s to report and `every
	// resume` to fix, both of which say so out loud. Migration only ever
	// rewrites a unit that already exists.
	if current == "" {
		return false, nil
	}

	fresh, err := freshUnit(b, name, sched)
	if err != nil {
		return false, err
	}
	if sameUnit(b, current, fresh) {
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

// disarmMissedOnce unloads one-shots whose moment has passed on a scheduler
// that will not run them, and returns the names it unloaded.
//
// launchd's plist carries Month, Day, Hour and Minute -- there is no Year
// field -- so a trigger the machine was powered off across still matches the
// same date twelve months later. The task would then fire a year late, run its
// command, and retire itself, with nothing anywhere saying that a one-shot
// scheduled for last December had just gone off.
//
// The store entry is deliberately left alone: `list` goes on reporting the
// task as missed, which is the signal the user needs, and `every rm` is still
// how it goes away. Only the trigger is removed.
//
// Not done on systemd or Task Scheduler. Persistent=true and
// StartWhenAvailable mean those schedulers intend to run a missed one-shot
// late, and that is the behavior their backends were deliberately written for.
//
// Gated on the unit file still existing, so once a task is disarmed every later
// pass costs one stat rather than a launchctl subprocess -- and a missed
// one-shot can sit in the store for a long time before anyone removes it.
//
// Never a task that is running, or still inside schedule.OnceGrace. The
// scheduled `every run` of a one-shot starts with this very pass, and unloading
// a launchd job kills it: without both guards a one-shot is killed by its own
// firing, or by an `every list` typed before launchd got round to spawning it.
func disarmMissedOnce(b backend.Backend, s *store.Store, dataDir string, now time.Time) []string {
	if backend.CatchesUpMissed(b) {
		return nil
	}

	var disarmed []string
	for _, name := range s.Tasks.Names() {
		task, _ := s.Tasks.Get(name)
		if task.Paused || naming.Validate(name) != nil {
			continue
		}
		sched, err := schedule.FromRecord(task.Schedule)
		if err != nil || !sched.OnceOverdue(now) {
			continue
		}
		if !b.ResourceExists(name) || store.Running(dataDir, name) {
			continue
		}
		// Disable first: DeleteUnits is what makes this idempotent, and doing
		// it the other way round would leave a loaded job with no file behind
		// it if the process died in between.
		_ = b.Disable(name)
		if err := b.DeleteUnits(name); err != nil {
			continue
		}
		disarmed = append(disarmed, name)
	}
	return disarmed
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

// sameUnit reports whether the unit on disk is what this version would write.
//
// A backend may store its unit in an encoding of its own, or embed a field that
// cannot survive being rendered twice -- Task Scheduler does both. Where it
// says so, both sides are reduced before comparing; everywhere else the
// comparison stays byte-for-byte.
//
// Getting this wrong is not cosmetic. A backend whose unit never compares equal
// is re-registered on every migration pass, and for a scheduler that derives an
// interval trigger's phase from the moment of registration, that pushes the
// next run back each time -- far enough, often enough, and the task stops
// firing at all while still reporting ok.
func sameUnit(b backend.Backend, current, fresh string) bool {
	type canonicalizer interface {
		CanonicalUnit(unit string) string
	}
	if c, ok := b.(canonicalizer); ok {
		return c.CanonicalUnit(current) == c.CanonicalUnit(fresh)
	}
	return current == fresh
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

// stampContent identifies the state the units were last generated for.
//
// Version and launcher are the obvious parts. The store's modification time is
// the part that is easy to leave out and wrong to: without it, the stamp claims
// "already migrated" forever, and a task added by an OLDER every after a
// migration is never repaired.
//
// That is not hypothetical. Upgrade to 0.4, roll back to 0.3.1 for any reason,
// add a task -- it gets a Ruby-era unit -- then upgrade again. The stamp still
// matches, the scan is skipped, and that one task silently never fires. Found
// by running the full upgrade / rollback / upgrade cycle against real launchd;
// it survived every test that only went forwards.
//
// The cost is one extra stat, and one rescan after each of every's own writes,
// which then re-stamps and settles.
func stampContent(dirs paths.Dirs, version, launcher string) string {
	storeStamp := "none"
	if info, err := os.Stat(filepath.Join(dirs.Data, "tasks.json")); err == nil {
		storeStamp = fmt.Sprintf("%d/%d", info.ModTime().UnixNano(), info.Size())
	}
	return version + "\n" + launcher + "\n" + storeStamp + "\n"
}

// RecordedLauncher is the launcher the units were last generated for, read
// back from the stamp. Reports false when no pass has run yet, which is the
// only honest answer -- the units may name anything.
//
// Exported because doctor checks that this path still exists: a launcher that
// has gone is a task that silently never fires, and every other check in that
// report still passes.
func RecordedLauncher(dirs paths.Dirs) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(dirs.Data, stampName))
	if err != nil {
		return "", false
	}
	// stampContent's second line. Split rather than a scanner: three lines.
	lines := strings.Split(string(raw), "\n")
	if len(lines) < 2 || lines[1] == "" {
		return "", false
	}
	return lines[1], true
}

// durableLauncher reports whether a launcher is one worth writing into every
// unit on the machine.
//
// The rule is that the shell can find it: LookPath on its base name resolves
// to this same file. That admits every install the installer and Homebrew
// produce -- including through the symlink, which Stat follows for both sides
// -- and excludes the two paths that are gone a moment later: `go run`'s temp
// build directory, and a binary invoked as ./every from a checkout.
//
// Getting this wrong in the permissive direction is expensive and silent. A
// pass run from a throwaway binary re-points every task at a path that stops
// existing, the units stay present and loaded, the last run stays exit 0, and
// the first sign is a backup that has not run for a month. Refusing to move
// the units costs at most an automatic re-point the user can trigger by
// running any every command from the real install.
func durableLauncher(launcher string) bool {
	found, err := exec.LookPath(filepath.Base(launcher))
	if err != nil {
		return false
	}
	onPath, err := os.Stat(found)
	if err != nil {
		return false
	}
	invoked, err := os.Stat(launcher)
	if err != nil {
		return false
	}
	return os.SameFile(onPath, invoked)
}

// Report writes a one-line summary, and nothing at all when there was nothing
// to do. A migration that announces itself on every invocation is noise.
func Report(w io.Writer, res Result) {
	if res.SkippedLauncher != "" {
		fmt.Fprintf(w, "· not re-pointing tasks at %s (not on PATH — a dev build or a temp binary)\n",
			res.SkippedLauncher)
		fmt.Fprintf(w, "  scheduled tasks still run %s\n", res.KeptLauncher)
	}
	for _, name := range res.Disarmed {
		fmt.Fprintf(w, "· %s was a one-shot the machine was off across — unscheduled it, "+
			"so it cannot fire on the same date next year\n", name)
		fmt.Fprintf(w, "  → every rm %s to clear it (the log is kept either way)\n", name)
	}
	if n := len(res.Repaired); n > 0 {
		if res.Relaunched != "" {
			fmt.Fprintf(w, "· re-pointed %d scheduled task%s at %s\n",
				n, plural(n), res.Relaunched)
		} else {
			fmt.Fprintf(w, "· repaired %d scheduled task%s for the %s runtime\n",
				n, plural(n), shortVersion)
		}
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
