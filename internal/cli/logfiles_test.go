package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeLogs lays down a task's two log generations. Either may be empty, which
// means "absent".
func writeLogs(t *testing.T, c *CLI, name, older, current string) {
	t.Helper()
	if err := os.MkdirAll(c.Dirs.Logs, 0o755); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		c.rotatedLogPath(name): older,
		c.logPath(name):        current,
	} {
		if body == "" {
			continue
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func runBlock(stamp, body string) string {
	return "=== " + stamp + " exit=0 dur=0.1s ===\n" + body
}

// Rotation renames the live log aside and starts an empty one, so the moment
// after it `every log` had exactly one run to show on a task with thousands.
// That looked like history that had been thrown away.
func TestLogReadsAcrossTheRotationBoundary(t *testing.T) {
	c, out := stubCLI(t)
	writeLogs(t, c, "backup",
		runBlock("2026-09-01 09:00:00", "old one\n")+runBlock("2026-09-02 09:00:00", "old two\n"),
		runBlock("2026-09-03 09:00:00", "fresh\n"))

	if err := c.log([]string{"backup"}); err != nil {
		t.Fatal(err)
	}

	got := out.String()
	for _, want := range []string{"old one", "old two", "fresh"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "old two") > strings.Index(got, "fresh") {
		t.Error("rotated lines came out after the live ones, want oldest first")
	}
}

// -n is a budget across both generations, not per file.
func TestLogHonorsLineCountAcrossGenerations(t *testing.T) {
	c, out := stubCLI(t)
	writeLogs(t, c, "backup", "o1\no2\no3\n", "c1\nc2\n")

	if err := c.log([]string{"backup", "-n", "4"}); err != nil {
		t.Fatal(err)
	}

	got := out.String()
	if want := "o2\no3\nc1\nc2\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// The live log alone answering means the rotated one is never opened.
func TestLogStopsAtTheLiveLogWhenItSuffices(t *testing.T) {
	c, out := stubCLI(t)
	writeLogs(t, c, "backup", "older\n", "c1\nc2\n")

	if err := c.log([]string{"backup", "-n", "2"}); err != nil {
		t.Fatal(err)
	}

	if got, want := out.String(), "c1\nc2\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// A log that exists only as the rotated generation is still a log. Reporting
// "no logs yet" there would be a lie with an exit code attached.
func TestLogFindsAnOnlyRotatedGeneration(t *testing.T) {
	c, out := stubCLI(t)
	writeLogs(t, c, "backup", "rotated only\n", "")

	if !c.logExists("backup") {
		t.Fatal("logExists = false with a rotated generation present")
	}
	if err := c.log([]string{"backup"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "rotated only") {
		t.Errorf("output = %q, want the rotated content", got)
	}
}

func TestLogStillReportsNoLogsWhenNeitherExists(t *testing.T) {
	c, _ := stubCLI(t)
	// A real task: an unknown name is no_such_task instead.
	addOnceTask(t, c, "backup", time.Now().Add(time.Hour))

	err := c.log([]string{"backup"})
	if err == nil {
		t.Fatal("no error for a task with no logs at all")
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.errCode("") != CodeNoLogs {
		t.Errorf("error = %v, want one coded %q", err, CodeNoLogs)
	}
}

// --with-output pairs ledger entries with log blocks from the end, so the
// pairing has to reach back past a rotation too.
func TestLogBlocksReachIntoTheRotatedGeneration(t *testing.T) {
	c, _ := stubCLI(t)
	writeLogs(t, c, "backup",
		runBlock("2026-09-01 09:00:00", "old\n"),
		runBlock("2026-09-02 09:00:00", "new\n"))

	blocks, err := c.logBlocks("backup", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(blocks))
	}
	if blocks[0] != "old\n" || blocks[1] != "new\n" {
		t.Errorf("blocks = %q, want [old new] oldest first", blocks)
	}
}

// Asking for fewer blocks than the live log holds must not open the rotated
// one -- that is what keeps --with-output from holding 10 MB.
func TestLogBlocksStopAtTheLiveLog(t *testing.T) {
	c, _ := stubCLI(t)
	writeLogs(t, c, "backup",
		runBlock("2026-09-01 09:00:00", "old\n"),
		runBlock("2026-09-02 09:00:00", "new\n"))

	blocks, err := c.logBlocks("backup", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0] != "new\n" {
		t.Errorf("blocks = %q, want just the live one", blocks)
	}
}

// resetHistory already removed both generations; a name reused after a
// rotation must not inherit the previous task's output.
func TestResetHistoryClearsBothGenerations(t *testing.T) {
	c, _ := stubCLI(t)
	writeLogs(t, c, "backup", "old\n", "new\n")

	if err := c.resetHistory("backup"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{c.logPath("backup"), c.rotatedLogPath("backup")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived resetHistory", filepath.Base(p))
		}
	}
}
