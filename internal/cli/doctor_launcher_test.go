package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// collect drives one doctor check function and returns what it claimed.
type claim struct {
	label string
	ok    bool
	fix   string
}

func collect(claims *[]claim) func(string, bool, string) {
	return func(label string, ok bool, fix string) {
		*claims = append(*claims, claim{label, ok, fix})
	}
}

// stampWith writes the migration stamp by hand, in its real three-line shape,
// so the launcher check has something to read.
func stampWith(t *testing.T, dataDir, launcher string) {
	t.Helper()
	body := "0.5.1\n" + launcher + "\nnone\n"
	if err := os.WriteFile(filepath.Join(dataDir, ".runtime"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The whole point of the check: a launcher that has gone is a machine where
// every task has silently stopped, and every other check still passes.
func TestDoctorLauncherFlagsAMissingBinary(t *testing.T) {
	c, _ := stubCLI(t)
	gone := filepath.Join(t.TempDir(), "uninstalled", "every")
	stampWith(t, c.Dirs.Data, gone)

	var claims []claim
	c.doctorLauncher(collect(&claims))

	if len(claims) != 1 {
		t.Fatalf("claims = %v, want exactly one", claims)
	}
	if claims[0].ok {
		t.Errorf("claim passed, want it to fail for %q", gone)
	}
	if !strings.Contains(claims[0].label, gone) {
		t.Errorf("label = %q, want it to name the launcher", claims[0].label)
	}
	if claims[0].fix == "" {
		t.Error("no fix offered — a diagnosis without one is half a diagnosis")
	}
}

func TestDoctorLauncherPassesForAnInstalledBinary(t *testing.T) {
	c, _ := stubCLI(t)
	bin := filepath.Join(t.TempDir(), "every")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	stampWith(t, c.Dirs.Data, bin)

	var claims []claim
	c.doctorLauncher(collect(&claims))

	if len(claims) != 1 || !claims[0].ok {
		t.Fatalf("claims = %v, want one passing claim", claims)
	}
}

// No stamp means no pass has run yet and the units may name anything. Saying
// nothing is the only honest answer.
func TestDoctorLauncherIsSilentWithoutAStamp(t *testing.T) {
	c, _ := stubCLI(t)

	var claims []claim
	c.doctorLauncher(collect(&claims))

	if len(claims) != 0 {
		t.Errorf("claims = %v, want none", claims)
	}
}

func TestLauncherUsable(t *testing.T) {
	dir := t.TempDir()

	exe := filepath.Join(dir, "every")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !launcherUsable(exe) {
		t.Error("an executable file is not usable")
	}

	if launcherUsable(filepath.Join(dir, "absent")) {
		t.Error("a missing file is usable")
	}
	if launcherUsable(dir) {
		t.Error("a directory is usable")
	}

	plain := filepath.Join(dir, "not-executable")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Windows decides executability by extension, so the mode bit says nothing
	// there and the check is existence only.
	if got, want := launcherUsable(plain), runtime.GOOS == "windows"; got != want {
		t.Errorf("launcherUsable(non-executable) = %v, want %v", got, want)
	}
}

// A task whose directory was deleted still runs -- from $HOME. Doctor says so,
// because the log note only reaches someone already reading the log.
func TestDoctorCwdFlagsAMissingDirectory(t *testing.T) {
	c, _ := stubCLI(t)
	gone := filepath.Join(t.TempDir(), "deleted-project")

	var claims []claim
	c.doctorCwd(gone, collect(&claims), func(string, ...any) {})

	if len(claims) != 1 || claims[0].ok {
		t.Fatalf("claims = %v, want one failing claim", claims)
	}
	if !strings.Contains(claims[0].label, gone) {
		t.Errorf("label = %q, want it to name the directory", claims[0].label)
	}
}

func TestDoctorCwdPassesForALiveDirectory(t *testing.T) {
	c, _ := stubCLI(t)
	dir := t.TempDir()

	var claims []claim
	c.doctorCwd(dir, collect(&claims), func(string, ...any) {})

	if len(claims) != 1 || !claims[0].ok {
		t.Fatalf("claims = %v, want one passing claim", claims)
	}
}

// A task added before cwd was recorded has none. There is nothing to claim.
func TestDoctorCwdIsSilentWhenUnset(t *testing.T) {
	c, _ := stubCLI(t)

	var claims []claim
	c.doctorCwd("", collect(&claims), func(string, ...any) {})

	if len(claims) != 0 {
		t.Errorf("claims = %v, want none", claims)
	}
}
