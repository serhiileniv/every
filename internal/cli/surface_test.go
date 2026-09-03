package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// surfaceCase is one entry of testdata/golden/cli/cli.json, captured from the
// Ruby by scripts/surface.rb.
type surfaceCase struct {
	Argv   []string `json:"argv"`
	Exit   int      `json:"exit"`
	Stdout string   `json:"stdout"`
	Stderr string   `json:"stderr"`
}

// The syntax freeze, enforced against the real binary.
//
// Every subcommand, every flag spelling, every error path -- replayed with the
// exact stdout, stderr and exit code the Ruby produced. A reworded message or a
// changed code is a build failure here, not a bug report from a user whose
// script broke.
//
// Only invocations that never reach a scheduler are in the table, so this stays
// hermetic. The live lifecycle is test/e2e's job.
func TestCLISurfaceMatchesFrozen(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "cli", "cli.json"))
	if err != nil {
		t.Fatalf("fixtures missing (regenerate with scripts/fixtures.sh): %v", err)
	}
	var cases []surfaceCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("surface fixture is empty")
	}

	bin := buildBinary(t)

	for _, tc := range cases {
		t.Run(strings.Join(tc.Argv, "_"), func(t *testing.T) {
			home := t.TempDir()
			cmd := exec.Command(bin, tc.Argv...)
			cmd.Env = append(os.Environ(),
				"EVERY_HOME="+home,
				"NO_COLOR=1",
				"TZ=America/New_York",
			)
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			runErr := cmd.Run()

			gotExit := exitCodeOf(cmd, runErr)
			gotOut := normalize(stdout.String(), home)
			gotErr := normalize(stderr.String(), home)

			if gotExit != tc.Exit {
				t.Errorf("exit = %d, want %d", gotExit, tc.Exit)
			}
			if gotOut != tc.Stdout {
				t.Errorf("stdout mismatch\n--- got ---\n%s\n--- want ---\n%s", gotOut, tc.Stdout)
			}
			if gotErr != tc.Stderr {
				t.Errorf("stderr mismatch\n--- got ---\n%s\n--- want ---\n%s", gotErr, tc.Stderr)
			}
		})
	}
	t.Logf("replayed %d CLI invocations", len(cases))
}

// normalize applies the same two substitutions scripts/surface.rb did: the
// scratch home (which `help` prints) and the version string (so a release bump
// is not a diff).
func normalize(s, home string) string {
	s = strings.ReplaceAll(s, home, "$EVERY_HOME")
	return strings.ReplaceAll(s, Version, "$VERSION")
}
