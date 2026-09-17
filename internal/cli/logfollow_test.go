package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/serhiileniv/every/internal/paths"
)

// syncBuf is an io.Writer a test can read while the follow loop writes to it.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// followHarness runs c.follow in the background and stops it on cleanup.
func followHarness(t *testing.T, logDir, name string) (*syncBuf, func()) {
	t.Helper()
	out := &syncBuf{}
	c := &CLI{Dirs: paths.Dirs{Data: logDir, Logs: logDir}, Stdout: out, Stderr: io.Discard}
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- c.follow(name, 40, stop) }()

	return out, func() {
		close(stop)
		select {
		case err := <-done:
			if err != nil && err != errFollowStopped {
				t.Errorf("follow returned %v, want nil", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("follow did not stop within 3s")
		}
	}
}

// waitFor polls until cond holds, so the test never sleeps longer than it must.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func appendTo(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// Output appended after the follow starts reaches the reader.
func TestFollowStreamsAppendedOutput(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "backup.log")
	appendTo(t, logPath, "first line\n")

	out, stop := followHarness(t, dir, "backup")
	defer stop()

	waitFor(t, "the initial tail", func() bool {
		return strings.Contains(out.String(), "first line")
	})

	appendTo(t, logPath, "second line\n")
	waitFor(t, "the appended line", func() bool {
		return strings.Contains(out.String(), "second line")
	})
}

// Rotation mid-follow must not silence the stream.
//
// This is the case worth testing: the log is renamed aside and a new one starts
// at the same path, which happens exactly when a task is chattiest -- the
// moment someone is most likely to be watching it.
func TestFollowSurvivesRotation(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "backup.log")
	appendTo(t, logPath, "before rotation\n")

	out, stop := followHarness(t, dir, "backup")
	defer stop()

	waitFor(t, "the pre-rotation tail", func() bool {
		return strings.Contains(out.String(), "before rotation")
	})

	// Exactly what the runner's rotation does.
	if err := os.Rename(logPath, logPath+".old"); err != nil {
		t.Fatal(err)
	}
	appendTo(t, logPath, "after rotation\n")

	waitFor(t, "output from the new generation", func() bool {
		return strings.Contains(out.String(), "after rotation")
	})
}

// A task that has never run has no log, and -f waits for it rather than
// erroring the way a plain `every log` correctly does.
func TestFollowWaitsForALogThatDoesNotExistYet(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "backup.log")

	out, stop := followHarness(t, dir, "backup")
	defer stop()

	// Nothing should have been written, and follow must still be running.
	time.Sleep(50 * time.Millisecond)
	if got := out.String(); got != "" {
		t.Errorf("wrote %q before the log existed, want nothing", got)
	}

	appendTo(t, logPath, "first run\n")
	waitFor(t, "the log to be picked up once created", func() bool {
		return strings.Contains(out.String(), "first run")
	})
}

// Truncation in place rewinds rather than stalling past the new end.
func TestFollowHandlesTruncation(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "backup.log")
	appendTo(t, logPath, "long original content\n")

	out, stop := followHarness(t, dir, "backup")
	defer stop()

	waitFor(t, "the initial tail", func() bool {
		return strings.Contains(out.String(), "long original content")
	})

	if err := os.WriteFile(logPath, []byte("tiny\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "content after truncation", func() bool {
		return strings.Contains(out.String(), "tiny")
	})
}
