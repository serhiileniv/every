package naming

import "testing"

// The names a scheduler must never be handed, and the ones it must accept.
//
// This is a security boundary, not a style check: a name becomes a plist path,
// a unit filename and a Task Scheduler URI. "../../../../tmp/x" written into
// tasks.json made `every resume` open a plist outside ~/Library/LaunchAgents --
// verified against both implementations before this package existed.
func TestValidate(t *testing.T) {
	reject := []struct{ name, why string }{
		{"", "empty"},
		{".", "not a usable filename"},
		{"..", "not a usable filename"},
		{"../evil", "traversal"},
		{"../../../../tmp/EVIL", "the traversal that motivated this package"},
		{"a/b", "separator"},
		{`a\b`, "windows separator"},
		{"/absolute", "absolute path"},
		{`C:\windows`, "drive-qualified"},
		{"UPPER", "sanitize lowercases, so this cannot come from add"},
		{"has space", "space"},
		{"semi;colon", "shell metacharacter"},
		{"quote\"", "quote, which would break XML and plist escaping"},
		{"new\nline", "newline"},
		{"null\x00byte", "NUL, which truncates a C path"},
		{"tab\there", "tab"},
		{"emoji😀", "non-ASCII"},
		{"$(whoami)", "command substitution"},
		{"`backtick`", "backtick"},
		{"a" + string(make([]byte, MaxLen)), "too long"},
	}
	for _, tc := range reject {
		t.Run("reject/"+tc.why, func(t *testing.T) {
			if err := Validate(tc.name); err == nil {
				t.Errorf("Validate(%q) accepted it; %s", tc.name, tc.why)
			}
		})
	}

	accept := []string{
		"backup", "sync-notes", "a", "a.b", "under_score",
		"dots.in.name", "trailing.", "123", "a-2",
		"weekly.report-2", "x" + string(repeat('y', MaxLen-1)),
	}
	for _, name := range accept {
		t.Run("accept/"+name, func(t *testing.T) {
			if err := Validate(name); err != nil {
				t.Errorf("Validate(%q) rejected a name add can produce: %v", name, err)
			}
		})
	}
}

// Exactly at the cap is fine; one over is not. The cap exists because the unit
// filename would otherwise exceed the 255-byte filesystem limit.
func TestLengthBoundary(t *testing.T) {
	at := string(repeat('a', MaxLen))
	if err := Validate(at); err != nil {
		t.Errorf("a name of exactly %d chars was rejected: %v", MaxLen, err)
	}
	over := string(repeat('a', MaxLen+1))
	if err := Validate(over); err == nil {
		t.Errorf("a name of %d chars was accepted", MaxLen+1)
	}
}

func repeat(c byte, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = c
	}
	return b
}
