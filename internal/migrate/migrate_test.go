package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	res := Run(dirs, b, b.launcher, "0.4.0", time.Now())

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

	if res := Run(dirs, b, b.launcher, "0.4.0", time.Now()); len(res.Repaired) != 1 {
		t.Fatalf("first pass repaired %v, want one", res.Repaired)
	}

	b.written, b.enabled, b.disabled = nil, nil, nil
	res := Run(dirs, b, b.launcher, "0.4.0", time.Now())

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
	Run(dirs, b, b.launcher, "0.4.0", time.Now())

	b.written = nil
	// The new prefix has to be a real on-PATH binary: units are only ever
	// re-pointed at a launcher the shell can find. See durableLauncher.
	b.launcher = installOnPath(t, "every")
	res := Run(dirs, b, b.launcher, "0.4.0", time.Now())

	if len(res.Repaired) != 1 {
		t.Errorf("repaired %v, want the task rewritten for the new launcher", res.Repaired)
	}
	if res.Relaunched != b.launcher {
		t.Errorf("Relaunched = %q, want the new launcher %q", res.Relaunched, b.launcher)
	}
}

// installOnPath puts an executable in a fresh directory, prepends it to PATH,
// and returns its path -- an install the shell can find, which is what the
// launcher guard tests for.
func installOnPath(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, name)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return bin
}

// A launcher the shell cannot find is a dev build or a temp binary: `go run`
// leaves one in a build directory that is deleted seconds later, and a
// checkout's ./every goes when the checkout does. Re-pointing every unit at one
// kills every task on the machine, silently -- the unit stays loaded and the
// last run stays exit 0, so nothing reports it. So the pass refuses.
func TestEphemeralLauncherDoesNotRepointUnits(t *testing.T) {
	dirs, b := setup(t)
	addTask(t, dirs, "backup")
	sched, err := schedule.Parse([]string{"day", "9am"})
	if err != nil {
		t.Fatal(err)
	}
	installed := installOnPath(t, "every")
	b.launcher = installed
	if err := b.Write("backup", sched); err != nil {
		t.Fatal(err)
	}
	Run(dirs, b, b.launcher, "0.5.1", time.Now())

	before, err := os.ReadFile(b.UnitPath("backup"))
	if err != nil {
		t.Fatal(err)
	}

	// `go run ./cmd/every list`: a binary under a build directory, on no PATH.
	ephemeral := filepath.Join(t.TempDir(), "b001", "exe", "every")
	if err := os.MkdirAll(filepath.Dir(ephemeral), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ephemeral, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	b.launcher = ephemeral
	b.written = nil
	res := Run(dirs, b, b.launcher, "0.5.1", time.Now())

	if len(b.written) != 0 {
		t.Errorf("wrote units %v, want none touched", b.written)
	}
	after, err := os.ReadFile(b.UnitPath("backup"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("unit changed:\n before %q\n after  %q", before, after)
	}
	if res.SkippedLauncher != ephemeral || res.KeptLauncher != installed {
		t.Errorf("skip = (%q, %q), want (%q, %q)",
			res.SkippedLauncher, res.KeptLauncher, ephemeral, installed)
	}

	// And the stamp still names the real install, so doctor reports on that
	// rather than on the throwaway binary.
	if got, ok := RecordedLauncher(dirs); !ok || got != installed {
		t.Errorf("RecordedLauncher = %q (%v), want %q", got, ok, installed)
	}
}

// The guard must not fire for the binary the units already name. A scheduled
// run goes through `every run`, which migrates too, and launchd hands it a
// minimal PATH that will not contain ~/.local/bin -- so probing PATH there
// would skip the pass and warn into the agent log on every single fire.
func TestRecordedLauncherIsNeverProbed(t *testing.T) {
	dirs, b := setup(t)
	addTask(t, dirs, "backup")
	sched, err := schedule.Parse([]string{"day", "9am"})
	if err != nil {
		t.Fatal(err)
	}
	b.launcher = filepath.Join(t.TempDir(), "bin", "every")
	if err := os.MkdirAll(filepath.Dir(b.launcher), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := b.Write("backup", sched); err != nil {
		t.Fatal(err)
	}
	Run(dirs, b, b.launcher, "0.5.1", time.Now())

	// Same launcher, new version, empty PATH: nothing is moving, so nothing is
	// probed, and the upgrade's repair still happens.
	t.Setenv("PATH", "")
	writeRubyEraUnit(t, b, "backup")
	res := Run(dirs, b, b.launcher, "0.6.0", time.Now())

	if res.SkippedLauncher != "" {
		t.Errorf("skipped %q, want the pass to run", res.SkippedLauncher)
	}
	if len(res.Repaired) != 1 {
		t.Errorf("repaired %v, want the stale unit repaired", res.Repaired)
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

	res := Run(dirs, b, b.launcher, "0.4.0", time.Now())
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

	res := Run(dirs, b, b.launcher, "0.4.0", time.Now())

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

	Run(dirs, b, b.launcher, "0.4.0", time.Now())

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

	Run(dirs, b, b.launcher, "0.4.0", time.Now())

	if _, err := os.Stat(filepath.Join(keep, "something.txt")); err != nil {
		t.Error("deleted a runtime dir that was not the old Ruby layout")
	}
}

// An empty store migrates cleanly and still stamps, so a fresh install does
// not scan on every command forever.
func TestFreshInstallStamps(t *testing.T) {
	dirs, b := setup(t)
	if res := Run(dirs, b, b.launcher, "0.4.0", time.Now()); res.Any() {
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

	res := Run(dirs, b, b.launcher, "0.4.0", time.Now())

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
	if res := Run(dirs, b, b.launcher, "0.4.0", time.Now()); len(res.Repaired) != 1 {
		t.Fatalf("first migration repaired %v, want one", res.Repaired)
	}

	// Rolled back; an older every adds a task and writes an old-format unit.
	addTask(t, dirs, "added-while-rolled-back")
	writeRubyEraUnit(t, b, "added-while-rolled-back")

	// Upgraded again. Same version, same launcher -- only the store changed.
	b.written, b.enabled = nil, nil
	res := Run(dirs, b, b.launcher, "0.4.0", time.Now())

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

	Run(dirs, b, b.launcher, "0.4.0", time.Now())
	b.written, b.enabled = nil, nil

	for i := 0; i < 5; i++ {
		if res := Run(dirs, b, b.launcher, "0.4.0", time.Now()); res.Any() {
			t.Fatalf("pass %d reported %+v, want silence", i, res)
		}
	}
	if len(b.written) != 0 {
		t.Errorf("rescanned with nothing changed: %v", b.written)
	}
}

// addOnce puts a one-shot in the store at `at`, with a unit on disk.
func addOnce(t *testing.T, dirs paths.Dirs, b *fakeBackend, name string, at time.Time) {
	t.Helper()
	s, err := store.Load(dirs.Data)
	if err != nil {
		t.Fatal(err)
	}
	sched, err := schedule.ParseAt([]string{"once", at.Format("2006-01-02"), at.Format("15:04")},
		at.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Add(name, &store.Task{
		Cmd: "echo hi", Schedule: sched.ToRecord(), Cwd: dirs.Data, Quiet: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.Write(name, sched); err != nil {
		t.Fatal(err)
	}
	b.written, b.enabled, b.disabled = nil, nil, nil
}

// catchingUp is a backend whose scheduler runs a missed trigger late, the way
// systemd (Persistent=true) and Task Scheduler (StartWhenAvailable) do.
type catchingUp struct{ *fakeBackend }

func (catchingUp) CatchesUpMissed() bool { return true }

// The launchd plist has Month and Day but no Year, so a one-shot the machine
// was powered off across still matches the same date twelve months later. It
// would fire a year late, run, and retire itself, with nothing saying why.
func TestMissedOneShotIsDisarmed(t *testing.T) {
	dirs, b := setup(t)
	missedAt := time.Now().Add(-48 * time.Hour)
	addOnce(t, dirs, b, "remind", missedAt)

	res := Run(dirs, b, b.launcher, "0.5.1", time.Now())

	if len(res.Disarmed) != 1 || res.Disarmed[0] != "remind" {
		t.Fatalf("Disarmed = %v, want [remind]", res.Disarmed)
	}
	if len(b.disabled) != 1 {
		t.Errorf("disabled = %v, want the agent unloaded", b.disabled)
	}
	if b.ResourceExists("remind") {
		t.Error("the unit is still on disk, so it can still fire next year")
	}

	// The store entry stays: `list` reports it as missed and `every rm` is
	// still how it goes away.
	s, err := store.Load(dirs.Data)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Tasks.Get("remind"); !ok {
		t.Error("the task was removed from the store, want it kept and reported")
	}
}

// Idempotent, and cheaply so: once the unit is gone the pass must not keep
// shelling out to the scheduler for a task the user has not removed yet.
func TestDisarmIsIdempotent(t *testing.T) {
	dirs, b := setup(t)
	addOnce(t, dirs, b, "remind", time.Now().Add(-48*time.Hour))

	Run(dirs, b, b.launcher, "0.5.1", time.Now())
	b.disabled = nil

	res := Run(dirs, b, b.launcher, "0.5.1", time.Now())

	if len(res.Disarmed) != 0 {
		t.Errorf("Disarmed = %v on the second pass, want none", res.Disarmed)
	}
	if len(b.disabled) != 0 {
		t.Errorf("disabled = %v on the second pass, want no scheduler call", b.disabled)
	}
}

// systemd and Task Scheduler mean to run it late. Disarming there would break
// the behavior those backends were deliberately written for.
func TestCatchingUpBackendKeepsAMissedOneShot(t *testing.T) {
	dirs, b := setup(t)
	addOnce(t, dirs, b, "remind", time.Now().Add(-48*time.Hour))

	res := Run(dirs, catchingUp{b}, b.launcher, "0.5.1", time.Now())

	if len(res.Disarmed) != 0 {
		t.Errorf("Disarmed = %v, want none on a catching-up scheduler", res.Disarmed)
	}
	if !b.ResourceExists("remind") {
		t.Error("the unit was removed, want it left to fire late")
	}
}

// A one-shot still ahead of its moment is an ordinary scheduled task.
func TestFutureOneShotIsUntouched(t *testing.T) {
	dirs, b := setup(t)
	addOnce(t, dirs, b, "remind", time.Now().Add(48*time.Hour))

	res := Run(dirs, b, b.launcher, "0.5.1", time.Now())

	if len(res.Disarmed) != 0 {
		t.Errorf("Disarmed = %v, want none", res.Disarmed)
	}
	if !b.ResourceExists("remind") {
		t.Error("a future one-shot lost its unit")
	}
}

// The stamp is written from what every itself last did; a one-shot goes missed
// because time passed. A stamped pass must still notice.
func TestDisarmRunsEvenWhenStamped(t *testing.T) {
	dirs, b := setup(t)
	addOnce(t, dirs, b, "remind", time.Now().Add(time.Hour))

	// Settle the stamp while the one-shot is still in the future.
	Run(dirs, b, b.launcher, "0.5.1", time.Now())
	if !b.ResourceExists("remind") {
		t.Fatal("disarmed a future one-shot")
	}

	// Nothing on disk changed; only the clock moved past the instant.
	res := Run(dirs, b, b.launcher, "0.5.1", time.Now().Add(2*time.Hour))

	if len(res.Disarmed) != 1 {
		t.Errorf("Disarmed = %v, want the one-shot noticed despite the stamp", res.Disarmed)
	}
}

// launchd fires a one-shot as `every run <name>`, and that run starts with this
// pass. Unloading the job from inside it is launchd killing the process before
// the command ran -- one-shots never ran at all. The run holds its lock before
// the pass, so the pass must see it and leave the task alone.
func TestRunningOneShotIsNotDisarmed(t *testing.T) {
	dirs, b := setup(t)
	at := time.Now().Add(-10 * time.Minute)
	addOnce(t, dirs, b, "remind", at)

	lock, err := store.HoldRun(dirs.Data, "remind")
	if err != nil {
		t.Fatal(err)
	}
	res := Run(dirs, b, b.launcher, "0.5.1", time.Now())
	if len(res.Disarmed) != 0 || len(b.disabled) != 0 {
		t.Fatalf("disarmed a running one-shot: Disarmed=%v disabled=%v (on launchd that kills it)",
			res.Disarmed, b.disabled)
	}
	if !b.ResourceExists("remind") {
		t.Error("the running one-shot lost its unit")
	}

	// A run that ended without retiring the task -- killed, say -- is missed
	// again, and the next pass disarms it.
	lock.Close()
	res = Run(dirs, b, b.launcher, "0.5.1", time.Now())
	if len(res.Disarmed) != 1 {
		t.Errorf("Disarmed = %v after the run ended, want [remind]", res.Disarmed)
	}
}

// launchd starts a calendar job some way into its minute, so a task whose
// moment has just arrived may simply not have been spawned yet. `every list`
// typed while watching the first one-shot must not unload it in that gap.
func TestOneShotIsNotDisarmedInsideItsGrace(t *testing.T) {
	for _, tc := range []struct {
		name   string
		after  time.Duration
		disarm bool
	}{
		{"at the moment", 0, false},
		{"30s in", 30 * time.Second, false},
		{"just inside the grace", schedule.OnceGrace - time.Second, false},
		{"at the end of the grace", schedule.OnceGrace, true},
		{"well past", time.Hour, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dirs, b := setup(t)
			at := time.Now().Add(time.Hour).Truncate(time.Minute)
			addOnce(t, dirs, b, "remind", at)

			res := Run(dirs, b, b.launcher, "0.5.1", at.Add(tc.after))
			if got := len(res.Disarmed) == 1; got != tc.disarm {
				t.Errorf("disarmed = %v, want %v", got, tc.disarm)
			}
			if b.ResourceExists("remind") == tc.disarm {
				t.Errorf("unit exists = %v, want %v", b.ResourceExists("remind"), !tc.disarm)
			}
		})
	}
}

// Repair re-registers a stale unit with Disable then Enable. On launchd that
// Disable is a bootout, which kills a running job: after an upgrade, the first
// scheduled fire of every task would repair itself and die. A running task is
// left for a later pass, and the stamp is withheld so that pass happens.
func TestRunningTaskIsNotReRegistered(t *testing.T) {
	dirs, b := setup(t)
	addTask(t, dirs, "backup")
	writeRubyEraUnit(t, b, "backup")

	lock, err := store.HoldRun(dirs.Data, "backup")
	if err != nil {
		t.Fatal(err)
	}
	res := Run(dirs, b, b.launcher, "0.5.1", time.Now())
	if len(b.disabled) != 0 || len(b.written) != 0 {
		t.Fatalf("re-registered a running task: written=%v disabled=%v", b.written, b.disabled)
	}
	if len(res.Repaired) != 0 {
		t.Errorf("Repaired = %v, want none while running", res.Repaired)
	}
	if _, err := os.Stat(filepath.Join(dirs.Data, stampName)); !os.IsNotExist(err) {
		t.Error("stamped with a stale unit outstanding; it would never be repaired")
	}

	lock.Close()
	res = Run(dirs, b, b.launcher, "0.5.1", time.Now())
	if len(res.Repaired) != 1 || res.Repaired[0] != "backup" {
		t.Errorf("Repaired = %v after the run ended, want [backup]", res.Repaired)
	}
	if _, err := os.Stat(filepath.Join(dirs.Data, stampName)); err != nil {
		t.Errorf("not stamped after the deferred repair: %v", err)
	}
}
