package store

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// The lock is mutually exclusive within a process.
func TestLockIsExclusive(t *testing.T) {
	dir := t.TempDir()

	first, err := AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}

	acquired := make(chan struct{})
	go func() {
		second, err := AcquireLock(dir)
		if err != nil {
			t.Errorf("second AcquireLock: %v", err)
			close(acquired)
			return
		}
		close(acquired)
		second.Close()
	}()

	select {
	case <-acquired:
		t.Fatal("the second lock was granted while the first was held")
	case <-time.After(150 * time.Millisecond):
		// Still blocked, which is the point.
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("the second lock was never granted after the first was released")
	}
}

func TestLockCloseIsIdempotent(t *testing.T) {
	l, err := AcquireLock(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// The property that matters is cross-PROCESS, which is what the lock exists
// for: five `every add` runs at once must all survive, rather than the last
// writer clobbering the rest. test/e2e/unix.sh asserts the same thing against
// the real CLI; this is the unit-level version, and it fails without a lock.
func TestConcurrentWritersAcrossProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns subprocesses")
	}
	dir := t.TempDir()

	// A helper binary that takes the lock, does a read-modify-write, releases.
	// The .exe matters on Windows -- without it anything resolving the path
	// through PATHEXT will not find it.
	helper := filepath.Join(t.TempDir(), "adder")
	if runtime.GOOS == "windows" {
		helper += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", helper,
		"github.com/serhiileniv/every/internal/store/testdata/adder").CombinedOutput(); err != nil {
		t.Skipf("cannot build the helper: %v\n%s", err, out)
	}

	const writers = 5
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out, err := exec.Command(helper, dir, fmt.Sprintf("conc%d", i)).CombinedOutput()
			if err != nil {
				errs <- fmt.Errorf("writer %d: %v\n%s", i, err, out)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, n := range s.Tasks.Names() {
		if strings.HasPrefix(n, "conc") {
			found = append(found, n)
		}
	}
	if len(found) != writers {
		raw, _ := os.ReadFile(filepath.Join(dir, "tasks.json"))
		t.Errorf("%d of %d concurrent writes survived (%v)\n%s", len(found), writers, found, raw)
	}
}
