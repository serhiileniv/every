package tail

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f.log")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLines(t *testing.T) {
	cases := []struct {
		name    string
		content string
		n       int
		want    []string
	}{
		{"empty file", "", 3, nil},
		{"fewer lines than asked", "a\nb\n", 5, []string{"a\n", "b\n"}},
		{"exactly n", "a\nb\nc\n", 3, []string{"a\n", "b\n", "c\n"}},
		{"more than n", "a\nb\nc\nd\n", 2, []string{"c\n", "d\n"}},
		// The separator is kept, and the final line keeps its lack of one.
		{"no trailing newline", "a\nb", 2, []string{"a\n", "b"}},
		{"single line no newline", "solo", 1, []string{"solo"}},
		// A blank final line is a line.
		{"trailing blank line", "a\n\n", 2, []string{"a\n", "\n"}},
		// n <= 0 short-circuits before the file is even opened.
		{"zero n", "a\nb\n", 0, nil},
		{"negative n", "a\nb\n", -1, nil},
		// CRLF must survive: a Windows-written log would otherwise lose the \r.
		{"crlf preserved", "a\r\nb\r\n", 2, []string{"a\r\n", "b\r\n"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Lines(write(t, tc.content), tc.n)
			if err != nil {
				t.Fatal(err)
			}
			if !equal(got, tc.want) {
				t.Errorf("Lines(%q, %d) = %q, want %q", tc.content, tc.n, got, tc.want)
			}
		})
	}
}

// n <= 0 is answered before the file is opened, so a missing path is not an
// error. store.LastRun depends on the ordering of those two checks.
func TestNonPositiveNDoesNotOpenTheFile(t *testing.T) {
	got, err := Lines(filepath.Join(t.TempDir(), "does-not-exist"), 0)
	if err != nil {
		t.Fatalf("expected no error for n=0 on a missing file, got %v", err)
	}
	if got != nil {
		t.Errorf("got %q, want nil", got)
	}
}

func TestMissingFileIsAnError(t *testing.T) {
	if _, err := Lines(filepath.Join(t.TempDir(), "nope"), 5); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

// The window grows a chunk at a time until it holds more than n newlines. A
// file whose last n lines span several chunks exercises the growth path, which
// a small fixture would leave untested.
func TestLinesSpanningMultipleChunks(t *testing.T) {
	var b strings.Builder
	const lines = 400
	for i := 0; i < lines; i++ {
		// ~100 bytes each, so 400 lines is far past the 16 KB first window.
		fmt.Fprintf(&b, "%03d %s\n", i, strings.Repeat("x", 96))
	}
	path := write(t, b.String())

	for _, n := range []int{1, 5, 200, 399, 400, 401, 1000} {
		got, err := Lines(path, n)
		if err != nil {
			t.Fatal(err)
		}
		want := n
		if want > lines {
			want = lines
		}
		if len(got) != want {
			t.Fatalf("Lines(n=%d) returned %d lines, want %d", n, len(got), want)
		}
		// The last line must always be the true last line of the file.
		if last := got[len(got)-1]; !strings.HasPrefix(last, "399 ") {
			t.Errorf("Lines(n=%d) last line = %.20q, want the 399 line", n, last)
		}
		// And the first returned line must be a whole line, never a fragment.
		if first := got[0]; len(first) != 101 {
			t.Errorf("Lines(n=%d) first line is %d bytes, want a whole 101-byte line: %.20q",
				n, len(first), first)
		}
	}
}

// The same files, through both implementations. The chunked window and the
// strictly-greater-than break condition are exactly the kind of thing that
// looks right and is off by one line at a boundary.
func TestMatchesRuby(t *testing.T) {
	ruby, err := exec.LookPath("ruby")
	if err != nil {
		t.Skip("no ruby on PATH; the Ruby tree is what this port replaces")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "lib", "every.rb")); err != nil {
		t.Skip("Ruby tree removed; differential comparison no longer applies")
	}

	contents := []string{
		"", "a", "a\n", "a\nb", "a\nb\n", "\n", "\n\n\n",
		strings.Repeat("line\n", 10),
		strings.Repeat("line\n", 5000),               // spans many chunks
		strings.Repeat("x", 40000) + "\n" + "tail\n", // one huge line
		strings.Repeat("x", 20000),                   // huge, no newline at all
		"a\r\nb\r\n",
	}
	ns := []int{1, 2, 3, 4, 5, 16, 17, 100}

	// One Ruby process for every pair rather than one per pair: the naive
	// loop spawned ~100 interpreters and put five seconds into every run of
	// `go test ./...`.
	dir := t.TempDir()
	type probe struct {
		Path string `json:"path"`
		N    int    `json:"n"`
	}
	var probes []probe
	for ci, content := range contents {
		path := filepath.Join(dir, fmt.Sprintf("f%d.log", ci))
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, n := range ns {
			probes = append(probes, probe{Path: path, N: n})
		}
	}

	payload, err := json.Marshal(probes)
	if err != nil {
		t.Fatal(err)
	}

	const script = `
$LOAD_PATH.unshift File.join(ARGV[0], "lib")
require "every"
require "json"
puts JSON.generate(JSON.parse(ARGV[1]).map { |p|
  Every::Tail.lines(p["path"], p["n"]).join
})
`
	out, err := exec.Command(ruby, "-e", script, root, string(payload)).CombinedOutput()
	if err != nil {
		t.Fatalf("ruby: %v\n%s", err, out)
	}
	var want []string
	if err := json.Unmarshal(out, &want); err != nil {
		t.Fatalf("decoding ruby output: %v\n%s", err, out)
	}
	if len(want) != len(probes) {
		t.Fatalf("ruby returned %d results for %d probes", len(want), len(probes))
	}

	for i, pr := range probes {
		got, err := Lines(pr.Path, pr.N)
		if err != nil {
			t.Fatal(err)
		}
		if joined := strings.Join(got, ""); joined != want[i] {
			t.Errorf("%s n=%d:\n got %q\nruby %q",
				filepath.Base(pr.Path), pr.N, truncate(joined), truncate(want[i]))
		}
	}
	t.Logf("compared %d tail reads", len(probes))
}

func truncate(s string) string {
	if len(s) > 80 {
		return s[:40] + "..." + s[len(s)-40:]
	}
	return s
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
