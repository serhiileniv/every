package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The trap doctor exists to catch: a command on the terminal's PATH that a
// clean login shell -- the scheduler's shell -- cannot see.
func TestResolveCommandDistinguishesTerminalFromLoginShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no login-shell split on Windows")
	}
	bin := t.TempDir()
	fake := filepath.Join(bin, "every-doctor-probe-fake")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	res := resolveCommand("every-doctor-probe-fake")
	if res.terminal != fake {
		t.Fatalf("terminal lookup = %q, want %q", res.terminal, fake)
	}
	if res.login {
		t.Error("a PATH entry set only in this process must not be visible to the clean login shell")
	}

	// Something every login shell has.
	res = resolveCommand("sh")
	if !res.login || res.terminal == "" {
		t.Errorf("sh: %+v", res)
	}

	res = resolveCommand("every-doctor-probe-missing")
	if res.login || res.terminal != "" {
		t.Errorf("missing command: %+v", res)
	}
}
