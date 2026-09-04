package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// Whole command SEQUENCES run through both implementations, with every step's
// stdout, stderr and exit code compared.
//
// The frozen surface table checks one invocation at a time against a recorded
// answer. This checks what happens when commands build on each other -- a task
// added then listed then paused then logged then removed -- against the other
// implementation live. State is where a port drifts in ways a single-shot
// table cannot see: a field that round-trips wrong, an ordering that only
// shows up with three tasks, a status that is right until something is paused.
func TestCommandSequencesMatchRuby(t *testing.T) {
	root := rubyTreeOrSkip(t)
	goBin := buildBinary(t)
	rubyBin := filepath.Join(root, "bin", "every")

	sequences := map[string][][]string{
		"list lifecycle": {
			{"list"},
			{"list", "--json"},
			{"__seed", "alpha", "echo one"},
			{"list"},
			{"list", "--json"},
			{"__seed", "beta", "echo two"},
			{"__seed", "gamma", "echo three"},
			// Order must be creation order, not alphabetical, and not random.
			{"list"},
			{"list", "--json"},
			{"__count"},
		},
		"re-seed keeps position": {
			{"__seed", "zulu", "echo z"},
			{"__seed", "alpha", "echo a"},
			{"__seed", "zulu", "echo z2"},
			{"list"},
		},
		"unknown task error paths": {
			{"log", "nope"},
			{"rm", "nope"},
			{"pause", "nope"},
			{"resume", "nope"},
			{"run", "nope"},
		},
		"usage error paths": {
			{"log"}, {"rm"}, {"pause"}, {"resume"}, {"run"},
			{"remove"}, {"ls", "--json"},
		},
		"add validation": {
			{"15m", "--name", "", "--", "true"},
			{"15m", "--name", ".", "--", "true"},
			{"15m", "--name", "..", "--", "true"},
			{"15m", "--name", "///", "--", "true"},
			{"15m", "--timeout", "0s", "--", "true"},
			{"15m", "--timeout", "abc", "--", "true"},
			{"15m", "--timeout=0m", "--", "true"},
			{"15m", "--name", "--quiet", "--", "true"},
			{"15m", "--"},
			{"15m"},
			{"nonsense", "--", "true"},
		},
		"json shape with runs": {
			{"__seed", "withrun", "echo hi"},
			{"run", "withrun"},
			{"list", "--json"},
			{"__last-exit", "withrun"},
			{"log", "withrun"},
			{"log", "-n", "1", "withrun"},
			{"log", "withrun", "-n", "1"},
		},
		"failing task": {
			{"__seed", "failer", "exit 42"},
			{"run", "failer"},
			{"__last-exit", "failer"},
			{"list", "--json"},
			{"list"},
		},
		"paused task status": {
			{"__seed", "p1", "true"},
			{"pause", "p1"},
			{"list"},
			{"list", "--json"},
		},
	}

	for name, steps := range sequences {
		t.Run(name, func(t *testing.T) {
			goHome, rubyHome := t.TempDir(), t.TempDir()

			for i, argv := range steps {
				gotOut, gotErr, gotCode := runCLI(t, goBin, goHome, argv)
				wantOut, wantErr, wantCode := runCLI(t, rubyBin, rubyHome, argv)

				gotOut, gotErr = scrub(gotOut, goHome), scrub(gotErr, goHome)
				wantOut, wantErr = scrub(wantOut, rubyHome), scrub(wantErr, rubyHome)

				label := strings.Join(argv, " ")
				if gotCode != wantCode {
					t.Errorf("step %d (%s): exit go=%d ruby=%d", i, label, gotCode, wantCode)
				}
				if gotOut != wantOut {
					t.Errorf("step %d (%s) stdout:\n--- go ---\n%s\n--- ruby ---\n%s", i, label, gotOut, wantOut)
				}
				if gotErr != wantErr {
					t.Errorf("step %d (%s) stderr:\n--- go ---\n%s\n--- ruby ---\n%s", i, label, gotErr, wantErr)
				}
			}
		})
	}
}

func runCLI(t *testing.T, bin, home string, argv []string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(bin, argv...)
	cmd.Env = append(os.Environ(),
		"EVERY_HOME="+home, "NO_COLOR=1", "TZ=America/New_York")
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	return out.String(), errBuf.String(), exitCodeOf(cmd, runErr)
}

var (
	// Timestamps and durations differ between two runs of the same command by
	// definition; the shapes are compared, the instants cannot be.
	reTimestamp = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[-+]\d{2}:\d{2}|Z)?`)
	reDur       = regexp.MustCompile(`dur=[0-9.]+s`)
	reSeconds   = regexp.MustCompile(`"seconds":[0-9.]+`)
	reAt        = regexp.MustCompile(`"at":"[^"]*"`)
	reNext      = regexp.MustCompile(`"next":(?:"[^"]*"|null)`)
	reLastCol   = regexp.MustCompile(`\d{2} [A-Z][a-z]{2} \d{2}:\d{2}`)
	reVersion   = regexp.MustCompile(`every \d+\.\d+\.\d+`)
	reExitLine  = regexp.MustCompile(`— exit (\d+) in [0-9.]+s`)
)

// scrub removes what legitimately differs between two runs -- the data dir,
// the version, and every wall-clock instant -- so what remains is the part
// that must be identical.
func scrub(s, home string) string {
	s = strings.ReplaceAll(scrubHome(s, home), "$EVERY_HOME", "$HOME")
	s = reVersion.ReplaceAllString(s, "every $VERSION")
	s = reTimestamp.ReplaceAllString(s, "$TS")
	s = reDur.ReplaceAllString(s, "dur=$D")
	s = reSeconds.ReplaceAllString(s, `"seconds":$D`)
	s = reAt.ReplaceAllString(s, `"at":"$TS"`)
	s = reNext.ReplaceAllString(s, `"next":"$TS"`)
	s = reLastCol.ReplaceAllString(s, "$DATE")
	s = reExitLine.ReplaceAllString(s, "— exit $C in ${D}s")
	return s
}

func rubyTreeOrSkip(t *testing.T) string {
	t.Helper()
	// Windows cannot execute bin/every: it is a shebang script, and there is no
	// interpreter association for an extensionless file. The comparison is
	// meaningful on the platforms that can run both launchers, and those are
	// where it runs.
	if runtime.GOOS == "windows" {
		t.Skip("bin/every is a shebang script; Windows cannot execute it")
	}
	if _, err := exec.LookPath("ruby"); err != nil {
		t.Skip("no ruby on PATH; the Ruby tree is what this port replaces")
	}
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "bin", "every")); err != nil {
		t.Skip("Ruby tree removed; differential comparison no longer applies")
	}
	return root
}
