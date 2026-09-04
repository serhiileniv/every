package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/serhiileniv/every/internal/jsonx"
)

// marshalJSON encodes without HTML escaping, so a command containing `&&` or
// `>` -- which is most of them -- survives verbatim. See internal/jsonx.
func marshalJSON(v any) ([]byte, error) { return jsonx.Marshal(v) }

// emitJSON writes one value and a newline.
func emitJSON(w io.Writer, v any) error {
	b, err := marshalJSON(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

// wantsJSON reports whether --json was asked for, WITHOUT consuming it.
//
// Only the tokens before `--` are considered. Everything after it is the user's
// command, and `every 15m -- echo --json` must print the flag, not switch this
// program's output format. That distinction is the whole reason the separator
// exists, and getting it wrong would silently corrupt a scheduled task.
func wantsJSON(argv []string) bool {
	// argv[0] is the command, never a flag. `every --json list` was a usage
	// error before this existed and stays one -- honoring the flag there would
	// change the output of an invocation that already had a defined answer,
	// which is exactly what 0.5.0 promised not to do. The frozen surface table
	// caught it the first time.
	if len(argv) == 0 {
		return false
	}
	for _, tok := range argv[1:] {
		if tok == "--" {
			return false
		}
		if tok == "--json" {
			return true
		}
	}
	return false
}

// stripJSONFlag removes --json from the tokens ahead of `--`, returning the
// rest and whether it was present.
func stripJSONFlag(argv []string) ([]string, bool) {
	out := make([]string, 0, len(argv))
	found := false
	for i, tok := range argv {
		if tok == "--" {
			out = append(out, argv[i:]...)
			return out, found
		}
		if tok == "--json" {
			found = true
			continue
		}
		out = append(out, tok)
	}
	return out, found
}

// okPayload is the acknowledgement for commands whose result is simply that
// they worked. A bare `true` would be valid JSON and useless to branch on.
type okPayload struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
}

// unmarshalJSON decodes, matching the encoder used everywhere else.
func unmarshalJSON(b []byte, v any) error { return json.Unmarshal(b, v) }
