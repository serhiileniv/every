package store

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// Duration is a run duration in seconds, rendered the way Ruby renders a Float.
//
// The difference is small and entirely observable: Ruby's JSON writes an
// integral float as "12.0", Go's encoding/json writes "12". That value reaches
// users through `every list --json` (as `seconds`) and through the `dur=` field
// of every log header, and a ledger written by the Ruby is read back by the Go.
// Formatting it by hand is the only way to keep both directions stable.
type Duration float64

// MarshalJSON always emits at least one decimal place, matching Ruby.
func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(d.String()), nil
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	// Accept a bare number; a null leaves the zero value, which is what a
	// missing dur meant in Ruby too.
	if bytes.Equal(b, []byte("null")) {
		return nil
	}
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	*d = Duration(f)
	return nil
}

// String renders the value as Ruby's Float#to_s would: shortest representation
// that round-trips, but never bare integer syntax.
func (d Duration) String() string {
	s := strconv.FormatFloat(float64(d), 'f', -1, 64)
	if !bytes.ContainsAny([]byte(s), ".eE") {
		s += ".0"
	}
	return s
}
