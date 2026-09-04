package store

import (
	"os"
	"path/filepath"
	"testing"
)

func goldenPath(t *testing.T, parts ...string) string {
	t.Helper()
	root := filepath.Join("..", "..", "testdata", "golden")
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("golden fixtures missing (regenerate with scripts/golden.rb): %v", err)
	}
	return filepath.Join(append([]string{root}, parts...)...)
}

// Read the Ruby-written registry and write it straight back out. The bytes
// must be identical.
//
// This is the check that catches the whole class of encoding drift at once:
// insertion order lost to a Go map, HTML escaping turning `&&` into &&,
// struct fields reordered, indentation off by a level. Any one of them rewrites
// every user's file the first time 0.4 saves it.
func TestTasksJSONRoundTripsByteForByte(t *testing.T) {
	want, err := os.ReadFile(goldenPath(t, "store", "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.json")
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("tasks.json changed on round-trip.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// The fixture deliberately contains a command with `&&`, `>` and `<tag>`, plus
// non-ASCII. Assert the raw characters survive rather than trusting the
// round-trip alone to have covered it.
func TestNoHTMLEscapingOrUnicodeEscaping(t *testing.T) {
	dir := t.TempDir()
	raw, err := os.ReadFile(goldenPath(t, "store", "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tasks.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}

	for _, needle := range []string{
		`&& echo done > /tmp/log 2>&1`, // the default encoder would \u-escape & and >
		`printf '<tag>'`,               // and < too
		`héllo — wörld`,                // non-ASCII stays raw, as in Ruby
	} {
		if !contains(string(got), needle) {
			t.Errorf("expected %q to survive encoding verbatim; got:\n%s", needle, got)
		}
	}
}

// Row order is creation order, and `every list` iterates the registry directly.
// A Go map would reshuffle this on every invocation.
func TestTaskOrderIsPreserved(t *testing.T) {
	dir := t.TempDir()
	raw, err := os.ReadFile(goldenPath(t, "store", "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tasks.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	// The fixture's insertion order is deliberately not alphabetical, so a
	// sorted or map-backed implementation cannot pass by coincidence.
	want := []string{"nightly", "backup", "archive"}
	// Repeat: a map-backed implementation can accidentally agree once.
	for i := 0; i < 50; i++ {
		s, err := Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		got := s.Tasks.Names()
		if len(got) != len(want) {
			t.Fatalf("got %d tasks, want %d", len(got), len(want))
		}
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("iteration %d: order = %v, want %v", i, got, want)
			}
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
