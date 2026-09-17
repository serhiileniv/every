package store

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestRunningFollowsHoldRun(t *testing.T) {
	dir := t.TempDir()
	if Running(dir, "backup") {
		t.Fatal("a task that never ran reads as running")
	}
	if _, err := os.Stat(filepath.Join(dir, runDir)); !os.IsNotExist(err) {
		t.Error("Running created the lock directory")
	}

	lock, err := HoldRun(dir, "backup")
	if err != nil {
		t.Fatal(err)
	}
	if !Running(dir, "backup") {
		t.Error("held, but Running is false")
	}
	if Running(dir, "other") {
		t.Error("one task's run marks another as running")
	}
	// Probing must not steal or release the holder's lock.
	if !Running(dir, "backup") {
		t.Error("a second probe lost the lock")
	}

	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if Running(dir, "backup") {
		t.Error("released, but Running is still true")
	}
}

// Overlapping runs of one task were allowed before the lock existed; holding
// it must not make the second wait for the first.
func TestHoldRunIsShared(t *testing.T) {
	dir := t.TempDir()
	first, err := HoldRun(dir, "backup")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	done := make(chan error, 1)
	go func() {
		second, err := HoldRun(dir, "backup")
		if err == nil {
			second.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a second run of the same task blocked behind the first")
	}
	if !Running(dir, "backup") {
		t.Error("closing the second holder cleared the first")
	}
}

func TestRunLockRefusesPaths(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"", ".", "..", "../x", "a/b", `a\b`} {
		if _, err := HoldRun(dir, name); err == nil {
			t.Errorf("HoldRun(%q) accepted a path", name)
		}
		if Running(dir, name) {
			t.Errorf("Running(%q) = true", name)
		}
	}
}

// The property the design rests on: launchd ends a retiring one-shot's process
// outright, and a timeout or a kill -9 does the same. None may leave the task
// reading as running forever -- that would stop missed one-shots from ever
// being disarmed.
func TestRunLockDiesWithTheProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a subprocess")
	}
	dir := t.TempDir()
	helper := filepath.Join(t.TempDir(), "runholder")
	if runtime.GOOS == "windows" {
		helper += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", helper,
		"github.com/serhiileniv/every/internal/store/testdata/runholder").CombinedOutput(); err != nil {
		t.Skipf("cannot build the helper: %v\n%s", err, out)
	}

	cmd := exec.Command(helper, dir, "backup")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	if line, _ := bufio.NewReader(stdout).ReadString('\n'); line != "held\n" {
		t.Fatalf("helper said %q", line)
	}

	if !Running(dir, "backup") {
		t.Fatal("another process holds the run, but Running is false")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	deadline := time.Now().Add(5 * time.Second)
	for Running(dir, "backup") {
		if time.Now().After(deadline) {
			t.Fatal("the run was killed, but the task still reads as running")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
