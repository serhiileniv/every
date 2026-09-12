package cli

import (
	"strings"
	"testing"
)

// `log` must not tell someone who mistyped a name that the task "has not run
// yet". Every other command answers no_such_task; log answered no_logs, so a
// program could not tell a typo from a task awaiting its first run.
func TestLogDistinguishesUnknownTaskFromNoRuns(t *testing.T) {
	bin := buildBinary(t)
	home := t.TempDir()

	// A task that exists but has never run.
	if _, _, code := runBin(t, bin, home, "__seed", "neverran", "true"); code != 0 {
		t.Fatal("seed failed")
	}

	cases := []struct {
		name     string
		argv     []string
		wantCode string
	}{
		{"unknown task", []string{"log", "ghost", "--json"}, `"error":"no_such_task"`},
		{"exists, never ran", []string{"log", "neverran", "--json"}, `"error":"no_logs"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runBin(t, bin, home, tc.argv...)
			// Both remain exit 66: the codes distinguish them, the exit status
			// is a frozen contract and must not move.
			if code != 66 {
				t.Errorf("exit = %d, want 66", code)
			}
			out := stdout + stderr
			if !strings.Contains(out, tc.wantCode) {
				t.Errorf("got %s, want %s", out, tc.wantCode)
			}
		})
	}
}

// The text form must agree with the JSON form about which of the two it is,
// or a person and a program reading the same command disagree.
func TestLogTextFormAgreesWithJSON(t *testing.T) {
	bin := buildBinary(t)
	home := t.TempDir()

	if _, _, code := runBin(t, bin, home, "__seed", "neverran", "true"); code != 0 {
		t.Fatal("seed failed")
	}

	_, stderr, code := runBin(t, bin, home, "log", "ghost")
	if code != 66 {
		t.Errorf("exit = %d, want 66", code)
	}
	if !strings.Contains(stderr, "no task") {
		t.Errorf("text form said %q, want it to say the task does not exist", stderr)
	}

	_, stderr, code = runBin(t, bin, home, "log", "neverran")
	if code != 66 {
		t.Errorf("exit = %d, want 66", code)
	}
	if !strings.Contains(stderr, "no logs yet") {
		t.Errorf("text form said %q, want it to say it has not run", stderr)
	}
}
