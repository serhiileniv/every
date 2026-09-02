package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/serhiileniv/every/internal/schedule"
)

func sampleTask() *Task {
	s, _ := schedule.Parse([]string{"15m"})
	return &Task{
		Cmd: "true", Schedule: s.ToRecord(), Cwd: "/tmp",
		CreatedAt: "2026-09-02T10:30:00-04:00", Quiet: true,
	}
}

// Assertions ported from test/store_test.rb.

func TestAddPersistsAndReloads(t *testing.T) {
	dir := t.TempDir()
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Add("demo", sampleTask()); err != nil {
		t.Fatal(err)
	}

	again, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := again.Tasks.Get("demo")
	if !ok {
		t.Fatal("task did not survive the round trip")
	}
	if got.Cmd != "true" {
		t.Errorf("cmd = %q, want %q", got.Cmd, "true")
	}
}

func TestMissingFileIsAnEmptyRegistry(t *testing.T) {
	s, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("a missing tasks.json must not be an error, got %v", err)
	}
	if s.Tasks.Len() != 0 {
		t.Errorf("got %d tasks, want 0", s.Tasks.Len())
	}
}

// The atomic write must leave no litter and must leave a valid file, however
// many times it runs.
func TestAtomicWriteLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if err := s.Add("demo", sampleTask()); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Errorf("left a temp file behind: %s", e.Name())
		}
	}

	raw, err := os.ReadFile(filepath.Join(dir, "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Errorf("tasks.json is not valid JSON after 50 writes: %v", err)
	}
}

func TestCorruptRegistryIsReported(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tasks.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected an error for a corrupt registry")
	}
	var corrupt *ErrCorrupt
	if !asErrCorrupt(err, &corrupt) {
		t.Fatalf("got %T, want *ErrCorrupt", err)
	}
	// The CLI prints this without an error-class suffix, matching Ruby's
	// abort(), which bypassed its rescue.
	if !strings.Contains(err.Error(), "is corrupted") ||
		!strings.Contains(err.Error(), "fix or delete it") {
		t.Errorf("message = %q, want the corrupted/fix-or-delete wording", err.Error())
	}
}

func asErrCorrupt(err error, target **ErrCorrupt) bool {
	c, ok := err.(*ErrCorrupt)
	if ok {
		*target = c
	}
	return ok
}

// A crash mid-append leaves a torn final line. Reporting "no runs" because of
// it would be a lie about the exact thing this tool exists to tell you.
func TestLastRunSkipsTornTrailingLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	ledger := `{"ts":"2026-09-02T10:00:00-04:00","exit":0,"dur":1.0}` + "\n" +
		`{"ts":"2026-09-02T10:15:00-04:00","exit":7,"dur":2.5}` + "\n" +
		`{"ts":"2026-09-02T10:30:00-04:00","exi` // torn
	if err := os.WriteFile(filepath.Join(dir, "runs", "demo.jsonl"), []byte(ledger), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.LastRun("demo")
	if err != nil {
		t.Fatal(err)
	}
	if run == nil {
		t.Fatal("got no run; the torn line must be skipped, not reported as empty")
	}
	if run.Exit != 7 {
		t.Errorf("exit = %d, want 7 (the last complete record)", run.Exit)
	}
}

// Only blank and unparseable lines: the window grows until the file is
// exhausted, then reports nothing rather than looping forever.
func TestLastRunWithNothingUsable(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := 0; i < 500; i++ {
		b.WriteString("\n")
		b.WriteString("{garbage\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "runs", "demo.jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	s, _ := Load(dir)
	run, err := s.LastRun("demo")
	if err != nil {
		t.Fatal(err)
	}
	if run != nil {
		t.Errorf("got %+v, want no run", run)
	}
}

func TestLastRunWithNoLedger(t *testing.T) {
	s, _ := Load(t.TempDir())
	run, err := s.LastRun("never-ran")
	if err != nil {
		t.Fatalf("a missing ledger must not be an error, got %v", err)
	}
	if run != nil {
		t.Errorf("got %+v, want no run", run)
	}
}

// Ruby renders an integral Float as "12.0"; Go's encoding/json renders it as
// "12". The value is user-visible through `every list --json` and the log
// header, and a ledger written by either implementation is read by the other.
func TestDurationRendersLikeRuby(t *testing.T) {
	cases := []struct {
		in   Duration
		want string
	}{
		{12.0, "12.0"},
		{0, "0.0"},
		{0.03, "0.03"},
		{30.5, "30.5"},
		{1.25, "1.25"},
		{100, "100.0"},
	}
	for _, tc := range cases {
		if got := tc.in.String(); got != tc.want {
			t.Errorf("Duration(%v).String() = %q, want %q", float64(tc.in), got, tc.want)
		}
		b, err := json.Marshal(Run{At: "t", Exit: 0, Dur: tc.in})
		if err != nil {
			t.Fatal(err)
		}
		if want := `"dur":` + tc.want; !strings.Contains(string(b), want) {
			t.Errorf("marshalled %s, want it to contain %s", b, want)
		}
	}
}

// Key order in the ledger is ts, exit, dur -- as Ruby emitted it.
func TestRunKeyOrder(t *testing.T) {
	b, err := json.Marshal(Run{At: "2026-09-02T10:30:00-04:00", Exit: 7, Dur: 1.5})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"ts":"2026-09-02T10:30:00-04:00","exit":7,"dur":1.5}`
	if string(b) != want {
		t.Errorf("got  %s\nwant %s", b, want)
	}
}

// A key written by a future version must survive a save by this one, or a
// downgrade becomes silently destructive.
func TestUnknownTopLevelKeysArePreserved(t *testing.T) {
	dir := t.TempDir()
	original := `{
  "tasks": {},
  "schema": 9,
  "future": {"a": 1}
}
`
	if err := os.WriteFile(filepath.Join(dir, "tasks.json"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"tasks", "schema", "future"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("key %q was dropped on save:\n%s", k, raw)
		}
	}
}

// A re-add under an existing name keeps its position rather than jumping to
// the end, so `every list` does not reorder when a task is replaced.
func TestReAddKeepsPosition(t *testing.T) {
	dir := t.TempDir()
	s, _ := Load(dir)
	for _, n := range []string{"first", "second", "third"} {
		if err := s.Add(n, sampleTask()); err != nil {
			t.Fatal(err)
		}
	}
	replacement := sampleTask()
	replacement.Cmd = "replaced"
	if err := s.Add("second", replacement); err != nil {
		t.Fatal(err)
	}

	want := []string{"first", "second", "third"}
	got := s.Tasks.Names()
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}
