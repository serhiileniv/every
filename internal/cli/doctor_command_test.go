package cli

import (
	"runtime"
	"testing"
)

// A command that is a cmd.exe builtin must not be reported as unresolvable.
//
// where.exe only finds files, and a builtin is not one, so doctor called every
// task built on `echo`, `dir`, `copy` and friends broken -- exiting 1 and
// printing a problem directly above "last run ok" for the very same task. The
// installer's own suggestion, `every day 9am -- echo it ran`, hit it.
func TestBuiltinCommandsResolveOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("cmd.exe builtins are a Windows concern")
	}
	t.Setenv("EVERY_SHELL", "")
	t.Setenv("COMSPEC", "cmd.exe")

	for _, word := range []string{"echo", "dir", "copy", "del", "type", "set", "ECHO"} {
		if !resolveCommand(word).login {
			t.Errorf("resolveCommand(%q).login = false, want true: it is a cmd.exe builtin", word)
		}
	}
}

// The check still has to fail for something that genuinely is not there, or it
// stops being a check at all.
func TestUnknownCommandStillFailsOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows resolution path")
	}
	t.Setenv("EVERY_SHELL", "")
	t.Setenv("COMSPEC", "cmd.exe")

	if resolveCommand("every-no-such-command-vzzt").login {
		t.Error("an absent command resolved; the check is not checking anything")
	}
}

// A real executable on PATH must keep resolving.
func TestRealExecutableResolvesOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows resolution path")
	}
	t.Setenv("EVERY_SHELL", "")
	t.Setenv("COMSPEC", "cmd.exe")

	if !resolveCommand("where").login {
		t.Error("where.exe did not resolve; the PATH branch is broken")
	}
}
