package runner

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/serhiileniv/every/internal/paths"
)

func testRunner(t *testing.T) (*Runner, paths.Dirs) {
	t.Helper()
	dir := t.TempDir()
	dirs := paths.Dirs{
		Data: dir,
		Logs: filepath.Join(dir, "logs"),
		Runs: filepath.Join(dir, "runs"),
	}
	r := &Runner{
		Dirs: dirs, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
		Now: time.Now, env: os.Getenv, goos: runtime.GOOS,
	}
	return r, dirs
}

// Assertions ported from test/runner_test.rb.

// Output under the cap survives verbatim, with no marker.
func TestCaptureSmallOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell differs on windows; covered by test/e2e/windows.ps1")
	}
	r, _ := testRunner(t)
	res, err := r.capture("echo hello", t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(res.Output); got != "hello\n" {
		t.Errorf("output = %q, want %q", got, "hello\n")
	}
	if res.ExitCode != 0 {
		t.Errorf("exit = %d, want 0", res.ExitCode)
	}
}

// stdout and stderr share one pipe, so both are captured and their
// interleaving is preserved.
func TestCaptureMergesStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell differs on windows")
	}
	r, _ := testRunner(t)
	res, err := r.capture("echo out; echo err 1>&2", t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	got := string(res.Output)
	if !strings.Contains(got, "out") || !strings.Contains(got, "err") {
		t.Errorf("output = %q, want both streams", got)
	}
}

func TestCaptureExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell differs on windows")
	}
	r, _ := testRunner(t)
	res, err := r.capture("exit 42", t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 42 {
		t.Errorf("exit = %d, want 42", res.ExitCode)
	}
}

// The truncation marker appears only when bytes were actually dropped. Output
// in the 32-64 KB band keeps head and tail with nothing between -- a marker
// claiming "0 bytes truncated" was a real bug.
func TestBoundedOutputBands(t *testing.T) {
	cases := []struct {
		name       string
		size       int
		wantMarker bool
	}{
		{"under the cap", 1000, false},
		{"exactly head", halfOutput, false},
		{"mid band keeps tail without a marker", halfOutput + 1000, false},
		{"exactly head+tail", 2 * halfOutput, false},
		{"over both, marker appears", 3 * halfOutput, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &bounded{}
			payload := bytes.Repeat([]byte("x"), tc.size)
			// Feed in realistic chunks, not one slab.
			for i := 0; i < len(payload); i += readChunk {
				end := i + readChunk
				if end > len(payload) {
					end = len(payload)
				}
				b.write(payload[i:end])
			}
			out := b.bytes()

			hasMarker := bytes.Contains(out, []byte("bytes truncated"))
			if hasMarker != tc.wantMarker {
				t.Errorf("marker present = %v, want %v (output %d bytes)", hasMarker, tc.wantMarker, len(out))
			}
			if bytes.Contains(out, []byte("[0 bytes truncated]")) {
				t.Error("emitted a zero-byte truncation marker")
			}
			if !tc.wantMarker && len(out) != tc.size {
				t.Errorf("output = %d bytes, want all %d kept", len(out), tc.size)
			}
			if tc.wantMarker && len(out) >= tc.size {
				t.Errorf("output = %d bytes, want less than the %d written", len(out), tc.size)
			}
		})
	}
}

// Both ends are kept, because errors show up at both ends.
func TestBoundedKeepsBothEnds(t *testing.T) {
	b := &bounded{}
	b.write([]byte("STARTMARKER"))
	b.write(bytes.Repeat([]byte("x"), 3*halfOutput))
	b.write([]byte("ENDMARKER"))
	out := b.bytes()

	if !bytes.HasPrefix(out, []byte("STARTMARKER")) {
		t.Error("lost the head")
	}
	if !bytes.HasSuffix(out, []byte("ENDMARKER")) {
		t.Error("lost the tail")
	}
}

// Binary output must not crash or be mangled.
func TestBoundedHandlesBinary(t *testing.T) {
	b := &bounded{}
	payload := make([]byte, 3*halfOutput)
	for i := range payload {
		payload[i] = byte(i % 256)
	}
	b.write(payload)
	out := b.bytes()
	if len(out) == 0 {
		t.Fatal("no output")
	}
	if !bytes.Contains(out, []byte("bytes truncated")) {
		t.Error("expected a truncation marker")
	}
}

// A timeout must kill the command AND its children, and report 124.
func TestTimeoutKillsProcessTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups are POSIX; windows is covered by the e2e suite")
	}
	r, _ := testRunner(t)

	// A shell that spawns a child and waits: killing only the direct child
	// would leave the sleep running and the read blocked.
	start := time.Now()
	res, err := r.capture("sleep 30 & sleep 30", t.TempDir(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	if res.ExitCode != 124 {
		t.Errorf("exit = %d, want 124 for a timeout", res.ExitCode)
	}
	if elapsed > 10*time.Second {
		t.Errorf("took %s; the deadline was 2s, so the tree was not killed promptly", elapsed)
	}
	if !bytes.Contains(res.Output, []byte("killed after 2s timeout")) {
		t.Errorf("output = %q, want the timeout marker", res.Output)
	}
}

// The deadline covers the WAIT, not just the read. A command that closes its
// output early and keeps running must still die on time.
func TestTimeoutAppliesAfterOutputCloses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell differs on windows")
	}
	r, _ := testRunner(t)

	start := time.Now()
	// Closes stdout and stderr immediately, then sleeps well past the deadline.
	res, err := r.capture("exec 1>&- 2>&-; sleep 30", t.TempDir(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	if elapsed > 10*time.Second {
		t.Errorf("took %s; the wait was not covered by the deadline", elapsed)
	}
	if res.ExitCode != 124 {
		t.Errorf("exit = %d, want 124", res.ExitCode)
	}
}

// A command that finishes well inside its timeout is unaffected.
func TestTimeoutNotTriggered(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell differs on windows")
	}
	r, _ := testRunner(t)
	res, err := r.capture("echo quick", t.TempDir(), 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit = %d, want 0", res.ExitCode)
	}
	if bytes.Contains(res.Output, []byte("killed after")) {
		t.Error("marked as timed out when it was not")
	}
}

// A signalled death reports 128+signum, not -1 as Go's ExitCode would.
func TestSignalledExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows processes are not signalled")
	}
	r, _ := testRunner(t)
	res, err := r.capture("kill -TERM $$", t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := 128 + 15; res.ExitCode != want {
		t.Errorf("exit = %d, want %d (128+SIGTERM)", res.ExitCode, want)
	}
}

// Ported from the login_shell assertions: only bash and zsh get -l.
func TestLoginShellFlags(t *testing.T) {
	cases := []struct {
		goos, shell, wantShell, wantFlag string
	}{
		{"darwin", "/bin/fish", "/bin/zsh", "-lc"}, // darwin ignores $SHELL
		{"linux", "/bin/bash", "/bin/bash", "-lc"},
		{"linux", "/usr/bin/zsh", "/usr/bin/zsh", "-lc"},
		{"linux", "/bin/dash", "/bin/dash", "-c"}, // dash rejects -l
		{"linux", "/bin/sh", "/bin/sh", "-c"},
		{"linux", "", "/bin/bash", "-lc"}, // unset $SHELL
	}
	for _, tc := range cases {
		t.Run(tc.goos+"_"+tc.shell, func(t *testing.T) {
			r := &Runner{goos: tc.goos, env: func(k string) string {
				if k == "SHELL" {
					return tc.shell
				}
				return ""
			}}
			got := r.LoginShell()
			if got[0] != tc.wantShell || got[1] != tc.wantFlag {
				t.Errorf("LoginShell() = %v, want [%s %s]", got, tc.wantShell, tc.wantFlag)
			}
		})
	}
}

func TestWindowsShellSelection(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{"default", map[string]string{}, []string{"cmd.exe", "/d", "/s", "/c"}},
		{"COMSPEC", map[string]string{"COMSPEC": `C:\Windows\System32\cmd.exe`},
			[]string{`C:\Windows\System32\cmd.exe`, "/d", "/s", "/c"}},
		{"EVERY_SHELL powershell", map[string]string{"EVERY_SHELL": "powershell.exe"},
			[]string{"powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command"}},
		{"EVERY_SHELL pwsh", map[string]string{"EVERY_SHELL": "pwsh"},
			[]string{"pwsh", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command"}},
		// EVERY_SHELL wins over COMSPEC.
		{"EVERY_SHELL beats COMSPEC",
			map[string]string{"EVERY_SHELL": "pwsh.exe", "COMSPEC": "cmd.exe"},
			[]string{"pwsh.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &Runner{goos: "windows", env: func(k string) string { return tc.env[k] }}
			got := r.WindowsShell()
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("WindowsShell() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOsaEscape(t *testing.T) {
	cases := []struct{ in, want string }{
		{`plain`, `plain`},
		{`say "hi"`, `say \"hi\"`},
		{`back\slash`, `back\\slash`},
		{`both "\"`, `both \"\\\"`},
	}
	for _, tc := range cases {
		if got := osaEscape(tc.in); got != tc.want {
			t.Errorf("osaEscape(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
