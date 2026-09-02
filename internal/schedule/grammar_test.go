package schedule

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// goldenDir locates testdata/golden at the repo root. Tests run with the
// package directory as the working directory, so the path is relative to that.
func goldenDir(t *testing.T, parts ...string) string {
	t.Helper()
	root := filepath.Join("..", "..", "testdata", "golden")
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("golden fixtures missing (regenerate with scripts/golden.rb): %v", err)
	}
	return filepath.Join(append([]string{root}, parts...)...)
}

// grammarCase mirrors one entry of testdata/golden/cli/grammar.json, captured
// from the Ruby parser by scripts/surface.rb.
type grammarCase struct {
	Tokens []string `json:"tokens"`
	OK     bool     `json:"ok"`
	ToH    *Record  `json:"to_h,omitempty"`
	Error  string   `json:"error,omitempty"`
}

// The syntax freeze, enforced. Every schedule form the DSL accepts and every
// one it rejects, with the exact rejection message, replayed against the port.
// A change to what `every <schedule>` does is a build failure here, not a bug
// report later.
func TestGrammarMatchesFrozenSurface(t *testing.T) {
	raw, err := os.ReadFile(goldenDir(t, "cli", "grammar.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []grammarCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("grammar fixture is empty")
	}

	accepted, rejected := 0, 0
	for _, tc := range cases {
		t.Run(name(tc.Tokens), func(t *testing.T) {
			got, err := Parse(tc.Tokens)

			if !tc.OK {
				if err == nil {
					t.Fatalf("Parse(%q) succeeded; Ruby rejected it with %q", tc.Tokens, tc.Error)
				}
				if err.Error() != tc.Error {
					t.Errorf("Parse(%q) error =\n  %q\nwant\n  %q", tc.Tokens, err.Error(), tc.Error)
				}
				return
			}

			if err != nil {
				t.Fatalf("Parse(%q) = %v; Ruby accepted it", tc.Tokens, err)
			}
			assertRecordEqual(t, got.ToRecord(), *tc.ToH)
		})
		if tc.OK {
			accepted++
		} else {
			rejected++
		}
	}
	t.Logf("replayed %d accepted and %d rejected schedule forms", accepted, rejected)
}

func assertRecordEqual(t *testing.T, got, want Record) {
	t.Helper()
	gotJSON, err := MarshalRecord(got)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := MarshalRecord(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("record =\n  %s\nwant\n  %s", gotJSON, wantJSON)
	}
}

func name(tokens []string) string {
	if len(tokens) == 0 {
		return "(empty)"
	}
	s := ""
	for i, tok := range tokens {
		if i > 0 {
			s += "_"
		}
		if tok == "" {
			s += "(empty)"
			continue
		}
		s += tok
	}
	return s
}
