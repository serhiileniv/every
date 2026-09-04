package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// testBinary is the compiled every, built once for the whole package.
//
// Five tests drive the real binary, and each used to build its own copy. On
// Windows that meant five `go build` runs plus a virus scan of each output,
// which is a large part of why the Windows unit job ran for twenty minutes
// without finishing. Building once takes it to one.
var testBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "every-cli-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, "creating a temp dir:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	// The .exe matters on Windows: without it the file is still executable via
	// CreateProcess, but anything that resolves it through PATHEXT -- a shell,
	// a spawned helper -- will not find it.
	name := "every"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	testBinary = filepath.Join(dir, name)

	out, err := exec.Command("go", "build", "-o", testBinary,
		"github.com/serhiileniv/every/cmd/every").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "building the test binary: %v\n%s", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// buildBinary returns the shared binary. Kept as a function so the call sites
// read the same as before.
func buildBinary(t *testing.T) string {
	t.Helper()
	if testBinary == "" {
		t.Fatal("the test binary was not built; see TestMain")
	}
	return testBinary
}

// exitCodeOf reads a finished command's exit code without assuming it ever
// started. cmd.ProcessState is nil when the process could not be launched at
// all -- a missing file, a bad executable format -- and dereferencing it there
// panics inside the test rather than reporting what went wrong.
func exitCodeOf(cmd *exec.Cmd, runErr error) int {
	if cmd.ProcessState != nil {
		return cmd.ProcessState.ExitCode()
	}
	if runErr != nil {
		// A code no path of every returns, so it cannot be mistaken for one.
		return -1
	}
	return 0
}

// scrubHome replaces a temp data dir with a stable token, in BOTH separator
// forms.
//
// t.TempDir() returns a native path -- backslashes on Windows -- while every
// normalizes the data dir to forward slashes, because that value is printed by
// `help`, `doctor` and error messages and a mixed-separator path is ugly to
// read. So the string in the output and the string the test holds are the same
// location spelled two ways, and replacing only one of them leaks an absolute
// machine-specific path into a comparison that is supposed to be portable.
func scrubHome(s, home string) string {
	s = strings.ReplaceAll(s, home, "$EVERY_HOME")
	// Explicit, not filepath.ToSlash: that is a no-op off Windows, so this
	// helper could not be tested anywhere but the platform it exists for.
	// Replacing the separator directly works on any host and lets the test
	// below feed it a real Windows path from a Mac.
	if slashed := strings.ReplaceAll(home, `\`, "/"); slashed != home {
		s = strings.ReplaceAll(s, slashed, "$EVERY_HOME")
	}
	return s
}
