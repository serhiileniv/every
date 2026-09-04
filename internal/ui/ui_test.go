package ui

import (
	"bytes"
	"strings"
	"testing"
)

func envFrom(m map[string]string) (func(string) string, func(string) bool) {
	return func(k string) string { return m[k] },
		func(k string) bool { _, ok := m[k]; return ok }
}

// Ported from the policy stated in lib/every/color.rb.
func TestColorEnabling(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		// A buffer is never a terminal, so every case here is false. What is
		// being pinned is that the env checks come FIRST and short-circuit.
		{"not a terminal", map[string]string{}, false},
		{"NO_COLOR set", map[string]string{"NO_COLOR": "1"}, false},
		// Presence, not truthiness: NO_COLOR="" still disables.
		{"NO_COLOR empty still disables", map[string]string{"NO_COLOR": ""}, false},
		{"TERM dumb", map[string]string{"TERM": "dumb"}, false},
		// An unset TERM is not "dumb"; it does not disable on its own.
		{"TERM unset", map[string]string{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, has := envFrom(tc.env)
			if got := NewColor(&bytes.Buffer{}, env, has).Enabled; got != tc.want {
				t.Errorf("Enabled = %v, want %v", got, tc.want)
			}
		})
	}
}

// Disabled color must return the string untouched -- every colored string has
// to stay readable with the codes stripped.
func TestDisabledColorIsIdentity(t *testing.T) {
	c := Color{Enabled: false}
	for _, s := range []string{"ok", "FAIL(7)", "", "héllo"} {
		if got := c.Green(s); got != s {
			t.Errorf("Green(%q) = %q, want it unchanged", s, got)
		}
	}
}

func TestEnabledColorWraps(t *testing.T) {
	c := Color{Enabled: true}
	if got, want := c.Green("ok"), "\x1b[32mok\x1b[0m"; got != want {
		t.Errorf("Green = %q, want %q", got, want)
	}
	if got, want := c.Red("x"), "\x1b[31mx\x1b[0m"; got != want {
		t.Errorf("Red = %q, want %q", got, want)
	}
}

func TestTableLayout(t *testing.T) {
	tbl := Table{
		Headers: []string{"NAME", "SCHEDULE", "LAST", "STATUS", "NEXT"},
		Rows: [][]string{
			{"backup", "day 9am", "02 Sep 09:00", "ok", "03 Sep 09:00"},
			{"a", "15m", "—", "·", "soon"},
		},
	}
	var buf bytes.Buffer
	if err := tbl.Render(&buf, Color{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}

	// Two spaces between columns, and every column padded to its widest cell --
	// including the last, so rows carry trailing spaces.
	wantHeader := "NAME    SCHEDULE  LAST          STATUS  NEXT        "
	if lines[0] != wantHeader {
		t.Errorf("header =\n%q\nwant\n%q", lines[0], wantHeader)
	}
	for i, l := range lines {
		if len([]rune(l)) != len([]rune(wantHeader)) {
			t.Errorf("line %d is %d columns, want %d (all rows pad to the same width)",
				i, len([]rune(l)), len([]rune(wantHeader)))
		}
	}
}

// Padding is by character, not by byte. A name with non-ASCII would otherwise
// be padded too narrowly and visibly misalign the grid.
func TestTablePadsByRuneNotByte(t *testing.T) {
	tbl := Table{
		Headers: []string{"NAME", "X"},
		Rows: [][]string{
			{"héllo", "1"}, // 5 runes, 6 bytes
			{"abcde", "2"}, // 5 runes, 5 bytes
		},
	}
	var buf bytes.Buffer
	if err := tbl.Render(&buf, Color{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	a, b := []rune(lines[1]), []rune(lines[2])
	if len(a) != len(b) {
		t.Errorf("rows misaligned: %q is %d columns, %q is %d", lines[1], len(a), lines[2], len(b))
	}
}

// Colorization replaces only the first match, as Ruby's sub does. A task whose
// NAME contains the status word must not have its name painted.
func TestColorizeReplacesOnlyTheFirstMatch(t *testing.T) {
	c := Color{Enabled: true}
	got := colorizeStatus("ok-task  15m  —  ok  soon", c)
	if strings.Count(got, "\x1b[32m") != 1 {
		t.Errorf("painted %d times, want 1: %q", strings.Count(got, "\x1b[32m"), got)
	}
}

// The word boundaries keep a substring from matching at all.
func TestColorizeRespectsWordBoundaries(t *testing.T) {
	c := Color{Enabled: true}
	if got := colorizeStatus("broken  15m  —  invalid  —", c); strings.Contains(got, "\x1b[") {
		t.Errorf("painted a substring match: %q", got)
	}
}

func TestColorizeFailWithExitCode(t *testing.T) {
	c := Color{Enabled: true}
	got := colorizeStatus("job  15m  —  FAIL(7)  soon", c)
	if !strings.Contains(got, "\x1b[31mFAIL(7)\x1b[0m") {
		t.Errorf("FAIL(7) not painted: %q", got)
	}
}
