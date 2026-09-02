package runner

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/serhiileniv/every/internal/store"
)

// The failures that only appear after months of running.
//
// Everything else in this suite asks whether a command works once. These ask
// whether it still works on the hundred-thousandth run -- which is the only
// kind of failure a scheduler can have that the user discovers by finding out
// their backups stopped, rather than by seeing an error.

// A task firing every minute for a year appends half a million ledger records.
// The file must stay bounded, and `list` must stay fast, or the tool degrades
// linearly over months in a way nobody attributes to the tool.
func TestLedgerStaysBoundedOverManyRuns(t *testing.T) {
	r, dirs := testRunner(t)
	if err := os.MkdirAll(dirs.Runs, 0o755); err != nil {
		t.Fatal(err)
	}

	const runs = 20000
	started := time.Now()
	for i := 0; i < runs; i++ {
		if err := r.appendRun("chatty", started, i%256, 1.5); err != nil {
			t.Fatal(err)
		}
	}

	path := filepath.Join(dirs.Runs, "chatty.jsonl")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > runTrimBytes*2 {
		t.Errorf("ledger grew to %d bytes after %d runs; the cap is %d",
			info.Size(), runs, runTrimBytes)
	}

	// Bounded is worthless if it stops being readable.
	s, err := store.Load(dirs.Data)
	if err != nil {
		t.Fatal(err)
	}
	last, err := s.LastRun("chatty")
	if err != nil {
		t.Fatal(err)
	}
	if last == nil {
		t.Fatal("the ledger became unreadable after trimming")
	}
	if want := (runs - 1) % 256; last.Exit != want {
		t.Errorf("last exit = %d, want %d -- trimming lost the newest record", last.Exit, want)
	}
}

// LastRun must not get slower as history accumulates. It reads the tail, so a
// large ledger and a small one should cost about the same; a naive
// read-everything implementation would show up here as a cliff.
func TestLastRunStaysFastOnALargeLedger(t *testing.T) {
	r, dirs := testRunner(t)
	if err := os.MkdirAll(dirs.Runs, 0o755); err != nil {
		t.Fatal(err)
	}

	// Bypass trimming to build a genuinely large file: a store that has been
	// carried across machines, or written by a version with a different cap.
	path := filepath.Join(dirs.Runs, "big.jsonl")
	var b strings.Builder
	for i := 0; i < 200000; i++ {
		fmt.Fprintf(&b, `{"ts":"2026-09-02T10:30:00-04:00","exit":%d,"dur":1.0}`+"\n", i%7)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	t.Logf("ledger under test: %.1f MB", float64(info.Size())/(1<<20))

	s, err := store.Load(dirs.Data)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	for i := 0; i < 50; i++ {
		if _, err := s.LastRun("big"); err != nil {
			t.Fatal(err)
		}
	}
	elapsed := time.Since(start)

	// Generous: the point is to catch a read-the-whole-file regression, which
	// would be seconds per call on a file this size, not milliseconds.
	if elapsed > 2*time.Second {
		t.Errorf("50 LastRun calls took %s on a large ledger; it is not reading the tail", elapsed)
	}
	t.Logf("50 reads of a %d-line ledger: %s", 200000, elapsed)
	_ = r
}

// The detailed log rotates at a size cap. Without it a chatty task fills the
// disk, which is a failure that takes out more than every.
func TestLogRotatesAndStaysBounded(t *testing.T) {
	r, dirs := testRunner(t)
	if err := os.MkdirAll(dirs.Logs, 0o755); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dirs.Logs, "loud.log")
	// Just over the cap, so the next append rotates.
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), maxLogBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := r.appendLog("loud", time.Now(), 0, 0.5, []byte("fresh\n")); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 1024 {
		t.Errorf("log is %d bytes after rotation; it should hold only the new entry", info.Size())
	}
	if _, err := os.Stat(path + ".old"); err != nil {
		t.Errorf("the rotated log was not kept: %v", err)
	}

	// Rotating twice must not accumulate generations -- one .old, forever.
	if err := os.WriteFile(path, bytes.Repeat([]byte("y"), maxLogBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.appendLog("loud", time.Now(), 0, 0.5, []byte("fresher\n")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dirs.Logs)
	if err != nil {
		t.Fatal(err)
	}
	generations := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "loud.log") {
			generations++
		}
	}
	if generations != 2 {
		t.Errorf("found %d log generations, want exactly 2 (current + .old)", generations)
	}
}

// Many tasks, each with history: `list` reads every ledger, so the cost is
// per-task and this is where it would show.
func TestManyTasksStayReadable(t *testing.T) {
	r, dirs := testRunner(t)
	if err := os.MkdirAll(dirs.Runs, 0o755); err != nil {
		t.Fatal(err)
	}

	const tasks = 200
	started := time.Now()
	for i := 0; i < tasks; i++ {
		name := fmt.Sprintf("task%03d", i)
		for j := 0; j < 20; j++ {
			if err := r.appendRun(name, started, 0, 1.0); err != nil {
				t.Fatal(err)
			}
		}
	}

	s, err := store.Load(dirs.Data)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for i := 0; i < tasks; i++ {
		last, err := s.LastRun(fmt.Sprintf("task%03d", i))
		if err != nil {
			t.Fatal(err)
		}
		if last == nil {
			t.Fatalf("task%03d lost its history", i)
		}
	}
	t.Logf("read the last run of %d tasks in %s", tasks, time.Since(start))
}

// A ledger whose final line is torn -- the machine lost power mid-append --
// must still report the previous run rather than "never ran". This is the
// scenario the backwards scan exists for, at realistic scale.
func TestTornLedgerAtScale(t *testing.T) {
	_, dirs := testRunner(t)
	if err := os.MkdirAll(dirs.Runs, 0o755); err != nil {
		t.Fatal(err)
	}

	var b strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&b, `{"ts":"2026-09-02T10:30:00-04:00","exit":%d,"dur":1.0}`+"\n", i%5)
	}
	// Power loss mid-write: a partial record with no newline.
	b.WriteString(`{"ts":"2026-09-02T11:00:00-04:00","ex`)

	path := filepath.Join(dirs.Runs, "torn.jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := store.Load(dirs.Data)
	if err != nil {
		t.Fatal(err)
	}
	last, err := s.LastRun("torn")
	if err != nil {
		t.Fatal(err)
	}
	if last == nil {
		t.Fatal("reported no runs because of a torn final line")
	}
	if want := 4999 % 5; last.Exit != want {
		t.Errorf("exit = %d, want %d (the last COMPLETE record)", last.Exit, want)
	}
}
