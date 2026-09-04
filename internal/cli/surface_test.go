package cli

import (
	"bytes"
	"encoding/json"
	"flag"
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
// update regenerates the fixture instead of comparing against it.
//
// A deliberate act, never a reflex: the fixture stopped being a differential
// when the Ruby was deleted, so regenerating it to clear a failure records
// whatever the code now does as correct. Read the diff.
var update = flag.Bool("update", false, "rewrite testdata/golden/cli/cli.json from this binary")

func TestCLISurfaceMatchesFrozen(t *testing.T) {
	fixture := filepath.Join("..", "..", "testdata", "golden", "cli", "cli.json")
	bin := buildBinary(t)

	if *update {
		regenerateSurface(t, bin, fixture)
		return
	}

	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("fixture missing (regenerate with -update): %v", err)
	}
	var cases []surfaceCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("surface fixture is empty")
	}
	if len(cases) != len(SurfaceCases) {
		t.Fatalf("fixture has %d cases, SurfaceCases has %d -- regenerate with -update",
			len(cases), len(SurfaceCases))
	}

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
	return strings.ReplaceAll(scrubHome(s, home), Version, "$VERSION")
}

// regenerateSurface captures every case in SurfaceCases from the built binary.
//
// It refuses to record an invocation that reaches a scheduler. That guard
// exists because a previous edit added succeeding `add` cases and left eleven
// launchd agents on the machine of whoever ran the generator.
func regenerateSurface(t *testing.T, bin, fixture string) {
	t.Helper()

	out := make([]surfaceCase, 0, len(SurfaceCases))
	for _, argv := range SurfaceCases {
		home := t.TempDir()
		cmd := exec.Command(bin, argv...)
		cmd.Env = append(os.Environ(),
			"EVERY_HOME="+home, "NO_COLOR=1", "TZ=America/New_York")
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		runErr := cmd.Run()

		gotOut := normalize(stdout.String(), home)
		if strings.Contains(gotOut, "\u2713 scheduled ") || strings.Contains(gotOut, "\u2713 created ") {
			t.Fatalf("%v registered a real task -- remove it from SurfaceCases, "+
				"or the table is not hermetic", argv)
		}

		out = append(out, surfaceCase{
			Argv:   argv,
			Exit:   exitCodeOf(cmd, runErr),
			Stdout: gotOut,
			Stderr: normalize(stderr.String(), home),
		})
	}

	blob, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture, append(blob, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("rewrote %s with %d cases -- READ THE DIFF", fixture, len(out))
}
