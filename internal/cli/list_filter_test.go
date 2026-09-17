package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/serhiileniv/every/internal/schedule"
	"github.com/serhiileniv/every/internal/store"
)

// mustAdd registers a scheduled task and, when exit >= 0, gives it one run
// with that exit status. A negative exit leaves it with no history, which is
// the "never run" case --failing has to leave alone.
func mustAdd(t *testing.T, c *CLI, name string, exit int) {
	t.Helper()

	sched, err := schedule.Parse([]string{"15m"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Load(c.Dirs.Data)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Add(name, &store.Task{
		Cmd: "true", Schedule: sched.ToRecord(), Cwd: c.Dirs.Data,
		CreatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	// Through the backend, so the task counts as loaded and does not read as
	// "unscheduled" -- which is itself a failing status and would mask the
	// distinction under test.
	if err := c.Backend.Write(name, sched); err != nil {
		t.Fatal(err)
	}
	if exit < 0 {
		return
	}

	if err := os.MkdirAll(c.Dirs.Runs, 0o755); err != nil {
		t.Fatal(err)
	}
	rec, err := json.Marshal(store.Run{At: time.Now().Format(time.RFC3339), Exit: exit})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.Dirs.Runs, name+".jsonl"), append(rec, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// --failing selects the tasks that need attention, and nothing else.
//
// The distinctions are the point: a paused task is doing what was asked of it,
// and a task that has never run has not failed. Folding either into "failing"
// turns the flag into "everything that is not ok", which is a noisier question
// than the one it is meant to answer.
func TestListFailingSelectsOnlyTasksNeedingAttention(t *testing.T) {
	c, out := stubCLI(t)

	mustAdd(t, c, "healthy", 0)
	mustAdd(t, c, "broken", 3)
	mustAdd(t, c, "neverrun", -1) // -1: no run recorded
	mustAdd(t, c, "resting", 0)
	if err := c.setPaused([]string{"resting"}, true); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if err := c.list([]string{"--failing"}); err != nil {
		t.Fatal(err)
	}
	got := out.String()

	for _, want := range []string{"broken"} {
		if !strings.Contains(got, want) {
			t.Errorf("--failing omitted %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"healthy", "neverrun", "resting"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("--failing included %q, which is not failing:\n%s", unwanted, got)
		}
	}
}

// No matches is the good outcome, not an error.
func TestListFailingExitsZeroWhenNothingIsFailing(t *testing.T) {
	c, out := stubCLI(t)
	mustAdd(t, c, "healthy", 0)

	out.Reset()
	if err := c.list([]string{"--failing"}); err != nil {
		t.Fatalf("--failing with no matches returned %v, want nil", err)
	}
	if got := out.String(); !strings.Contains(got, "nothing failing") {
		t.Errorf("said %q, want a plain statement that nothing is failing", got)
	}

	out.Reset()
	if err := c.list([]string{"--failing", "--json"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "[]" {
		t.Errorf("--failing --json with no matches printed %q, want []", got)
	}
}

func TestIsFailing(t *testing.T) {
	for status, want := range map[string]bool{
		"ok":          false,
		"paused":      false,
		"·":           false,
		"FAIL(1)":     true,
		"FAIL(127)":   true,
		"unscheduled": true,
		"invalid":     true,
		"missed":      true,
		"late":        false,
		"running":     false,
	} {
		if got := isFailing(status); got != want {
			t.Errorf("isFailing(%q) = %v, want %v", status, got, want)
		}
	}
}
