package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/serhiileniv/every/internal/paths"
	"github.com/serhiileniv/every/internal/schedule"
	"github.com/serhiileniv/every/internal/store"
)

// fakeBackend records what the migration asked it to do, and renders units the
// way a real backend would, so the staleness comparison is exercised for real.
type fakeBackend struct {
	dir      string
	launcher string

	written  []string
	disabled []string
	enabled  []string

	writeErr error
}

func (f *fakeBackend) Name() string { return "fake" }

func (f *fakeBackend) UnitPath(name string) string {
	return filepath.Join(f.dir, "com.every."+name+".plist")
}

func (f *fakeBackend) Render(name string, s *schedule.Schedule) (string, error) {
	// The shape that matters: the launcher argv. A 0.3.1 unit has an
	// interpreter in front of it; a 0.4 unit does not.
	return "ARGV=" + f.launcher + " run " + name + " KIND=" + string(s.Kind) + "\n", nil
}

func (f *fakeBackend) Write(name string, s *schedule.Schedule) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.written = append(f.written, name)
	body, _ := f.Render(name, s)
	return os.WriteFile(f.UnitPath(name), []byte(body), 0o644)
}

func (f *fakeBackend) Enable(name string) error  { f.enabled = append(f.enabled, name); return nil }
func (f *fakeBackend) Disable(name string) error { f.disabled = append(f.disabled, name); return nil }
func (f *fakeBackend) DeleteUnits(name string) error {
	return os.Remove(f.UnitPath(name))
}
func (f *fakeBackend) Loaded(string) bool             { return true }
func (f *fakeBackend) LoadedNames() ([]string, error) { return nil, nil }
func (f *fakeBackend) ResourceExists(name string) bool {
	_, err := os.Stat(f.UnitPath(name))
	return err == nil
}

func setup(t *testing.T) (paths.Dirs, *fakeBackend) {
	t.Helper()
	dir := t.TempDir()
	dirs := paths.Dirs{
		Data:   dir,
		Logs:   filepath.Join(dir, "logs"),
		Runs:   filepath.Join(dir, "runs"),
		Agents: filepath.Join(dir, "agents"),
		Config: filepath.Join(dir, "config"),
	}
	if err := os.MkdirAll(dirs.Agents, 0o755); err != nil {
		t.Fatal(err)
	}
	return dirs, &fakeBackend{dir: dirs.Agents, launcher: "/usr/local/bin/every"}
}

func addTask(t *testing.T, dirs paths.Dirs, name string) {
	t.Helper()
	s, err := store.Load(dirs.Data)
	if err != nil {
		t.Fatal(err)
	}
	sched, err := schedule.Parse([]string{"day", "9am"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Add(name, &store.Task{
		Cmd: "echo hi", Schedule: sched.ToRecord(), Cwd: dirs.Data, Quiet: true,
	}); err != nil {
		t.Fatal(err)
	}
}

// A unit written by 0.3.1 invokes the tool THROUGH ruby. After the upgrade that
// interpreter is handed a compiled binary, fails to parse it, and the task
// silently stops firing. This is the failure the whole package exists for.
func writeRubyEraUnit(t *testing.T, b *fakeBackend, name string) {
	t.Helper()
	body := "ARGV=/System/Library/Frameworks/Ruby.framework/Versions/2.6/usr/bin/ruby " +
		"/usr/local/bin/every run " + name + " KIND=calendar\n"
	if err := os.WriteFile(b.UnitPath(name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRepairsRubyEraUnits(t *testing.T) {
	dirs, b := setup(t)
	for _, n := range []string{"backup", "sync", "notes"} {
		addTask(t, dirs, n)
		writeRubyEraUnit(t, b, n)
	}

	res := Run(dirs, b, b.launcher, "0.4.0")

	if len(res.Failed) != 0 {
		t.Fatalf("unexpected failures: %v", res.Failed)
	}
	if len(res.Repaired) != 3 {
		t.Fatalf("repaired %v, want all three", res.Repaired)
	}

	// Rewritten AND re-registered: launchd holds its own copy of the
	// definition, so rewriting the file alone would change nothing.
	if len(b.enabled) != 3 {
		t.Errorf("re-registered %v, want all three", b.enabled)
	}

	for _, n := range []string{"backup", "sync", "notes"} {
		raw, err := os.ReadFile(b.UnitPath(n))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "ruby") {
			t.Errorf("%s still invokes ruby after migration:\n%s", n, raw)
		}
	}
}

// A second pass must do nothing: the stamp matches, so the store is not even
// opened.
func TestSecondPassIsANoOp(t *testing.T) {
	dirs, b := setup(t)
	addTask(t, dirs, "backup")
	writeRubyEraUnit(t, b, "backup")

	if res := Run(dirs, b, b.launcher, "0.4.0"); len(res.Repaired) != 1 {
		t.Fatalf("first pass repaired %v, want one", res.Repaired)
	}

	b.written, b.enabled, b.disabled = nil, nil, nil
	res := Run(dirs, b, b.launcher, "0.4.0")

	if res.Any() {
		t.Errorf("second pass reported %+v, want silence", res)
	}
	if len(b.written) != 0 || len(b.enabled) != 0 {
		t.Errorf("second pass touched the scheduler: written=%v enabled=%v", b.written, b.enabled)
	}
}

// Moving the launcher -- a re-install to a different prefix -- must re-migrate
// even though the version is unchanged.
func TestLauncherChangeTriggersMigration(t *testing.T) {
	dirs, b := setup(t)
	addTask(t, dirs, "backup")
	// A unit has to exist for there to be anything to repair -- migration
	// rewrites, it never creates.
	sched, err := schedule.Parse([]string{"day", "9am"})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Write("backup", sched); err != nil {
		t.Fatal(err)
	}
	Run(dirs, b, b.launcher, "0.4.0")

	b.written = nil
	b.launcher = "/opt/homebrew/bin/every"
	res := Run(dirs, b, b.launcher, "0.4.0")

	if len(res.Repaired) != 1 {
		t.Errorf("repaired %v, want the task rewritten for the new launcher", res.Repaired)
	}
}

// A paused task has no unit by design; resuming writes one. Migration must not
// resurrect it.
func TestPausedTasksAreLeftAlone(t *testing.T) {
	dirs, b := setup(t)
	addTask(t, dirs, "backup")

	s, err := store.Load(dirs.Data)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPaused("backup", true); err != nil {
		t.Fatal(err)
	}

	res := Run(dirs, b, b.launcher, "0.4.0")
	if res.Any() {
		t.Errorf("touched a paused task: %+v", res)
	}
	if len(b.enabled) != 0 {
		t.Errorf("re-enabled a paused task: %v", b.enabled)
	}
}

// One task that cannot be repaired must not stop the others, and must not
// stamp the run as done -- otherwise the failure is remembered as success and
// never retried.
func TestOneFailureDoesNotStopTheRestOrStamp(t *testing.T) {
	dirs, b := setup(t)
	addTask(t, dirs, "good")

	// A record whose schedule cannot be rebuilt.
	s, err := store.Load(dirs.Data)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Add("broken", &store.Task{
		Cmd:      "echo hi",
		Schedule: schedule.Record{Raw: "?", Kind: "from-the-future"},
		Cwd:      dirs.Data,
	}); err != nil {
		t.Fatal(err)
	}
	writeRubyEraUnit(t, b, "good")

	res := Run(dirs, b, b.launcher, "0.4.0")

	if len(res.Repaired) != 1 || res.Repaired[0] != "good" {
		t.Errorf("repaired %v, want the healthy task fixed anyway", res.Repaired)
	}
	if _, ok := res.Failed["broken"]; !ok {
		t.Errorf("failures %v, want the broken task reported", res.Failed)
	}
	if _, err := os.Stat(filepath.Join(dirs.Data, stampName)); err == nil {
		t.Error("stamped despite a failure; the repair would never be retried")
	}
}

// The mirrored Ruby tree an older every kept for TCC-protected installs is
// dead weight once there is one binary.
func TestRemovesTheOldRubyRuntimeTree(t *testing.T) {
	dirs, b := setup(t)
	runtimeLib := filepath.Join(dirs.Data, "runtime", "lib")
	if err := os.MkdirAll(runtimeLib, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeLib, "every.rb"), []byte("module Every; end"), 0o644); err != nil {
		t.Fatal(err)
	}

	Run(dirs, b, b.launcher, "0.4.0")

	if _, err := os.Stat(filepath.Join(dirs.Data, "runtime")); !os.IsNotExist(err) {
		t.Error("the stale Ruby runtime tree survived the migration")
	}
}

// A directory named runtime that is NOT the old layout must be left alone.
func TestLeavesUnrecognisedRuntimeDirAlone(t *testing.T) {
	dirs, b := setup(t)
	keep := filepath.Join(dirs.Data, "runtime")
	if err := os.MkdirAll(keep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keep, "something.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	Run(dirs, b, b.launcher, "0.4.0")

	if _, err := os.Stat(filepath.Join(keep, "something.txt")); err != nil {
		t.Error("deleted a runtime dir that was not the old Ruby layout")
	}
}

// An empty store migrates cleanly and still stamps, so a fresh install does
// not scan on every command forever.
func TestFreshInstallStamps(t *testing.T) {
	dirs, b := setup(t)
	if res := Run(dirs, b, b.launcher, "0.4.0"); res.Any() {
		t.Errorf("fresh install reported %+v, want silence", res)
	}
	if _, err := os.Stat(filepath.Join(dirs.Data, stampName)); err != nil {
		t.Error("a fresh install did not stamp; every command would rescan")
	}
}

// A task in the store with no unit is not migration's problem. It happens --
// the user unloaded it by hand, a previous add failed halfway, the scheduler
// dropped it -- and silently scheduling it here would resurrect something
// somebody deliberately stopped. doctor reports it; `every resume` fixes it.
//
// This was a real bug: the first version wrote units for these, which broke an
// e2e assertion and left a stray plist in a real ~/Library/LaunchAgents.
func TestNeverCreatesAMissingUnit(t *testing.T) {
	dirs, b := setup(t)
	addTask(t, dirs, "unscheduled-on-purpose")

	res := Run(dirs, b, b.launcher, "0.4.0")

	if res.Any() {
		t.Errorf("reported %+v, want silence for a task with no unit", res)
	}
	if len(b.written) != 0 || len(b.enabled) != 0 {
		t.Errorf("scheduled a task that had no unit: written=%v enabled=%v", b.written, b.enabled)
	}
	if _, err := os.Stat(b.UnitPath("unscheduled-on-purpose")); !os.IsNotExist(err) {
		t.Error("created a unit file for a task that had none")
	}
}

// The rollback-then-add hole.
//
// Upgrade to 0.4, roll back to an older every for any reason, add a task --
// which gets an old-format unit -- then upgrade again. If the stamp only
// recorded version and launcher it would still match, the scan would be
// skipped, and that one task would silently never fire again. Which is
// precisely the failure this package exists to prevent, reintroduced by the
// optimization that makes it cheap.
//
// Found by running the full upgrade / rollback / upgrade cycle against real
// launchd. Every test that only moved forwards passed.
func TestRepairsTasksAddedByAnOlderEveryAfterMigrating(t *testing.T) {
	dirs, b := setup(t)

	// First upgrade: one task, migrated and stamped.
	addTask(t, dirs, "existing")
	writeRubyEraUnit(t, b, "existing")
	if res := Run(dirs, b, b.launcher, "0.4.0"); len(res.Repaired) != 1 {
		t.Fatalf("first migration repaired %v, want one", res.Repaired)
	}

	// Rolled back; an older every adds a task and writes an old-format unit.
	addTask(t, dirs, "added-while-rolled-back")
	writeRubyEraUnit(t, b, "added-while-rolled-back")

	// Upgraded again. Same version, same launcher -- only the store changed.
	b.written, b.enabled = nil, nil
	res := Run(dirs, b, b.launcher, "0.4.0")

	if len(res.Repaired) != 1 || res.Repaired[0] != "added-while-rolled-back" {
		t.Fatalf("repaired %v, want the task the older every added", res.Repaired)
	}
	raw, err := os.ReadFile(b.UnitPath("added-while-rolled-back"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "ruby") {
		t.Errorf("the unit still invokes ruby:\n%s", raw)
	}
}

// The stamp must still suppress the scan when genuinely nothing has changed,
// or the optimization is gone and every command pays for a full pass.
func TestStampStillSuppressesWhenNothingChanged(t *testing.T) {
	dirs, b := setup(t)
	addTask(t, dirs, "backup")
	sched, err := schedule.Parse([]string{"day", "9am"})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Write("backup", sched); err != nil {
		t.Fatal(err)
	}

	Run(dirs, b, b.launcher, "0.4.0")
	b.written, b.enabled = nil, nil

	for i := 0; i < 5; i++ {
		if res := Run(dirs, b, b.launcher, "0.4.0"); res.Any() {
			t.Fatalf("pass %d reported %+v, want silence", i, res)
		}
	}
	if len(b.written) != 0 {
		t.Errorf("rescanned with nothing changed: %v", b.written)
	}
}
