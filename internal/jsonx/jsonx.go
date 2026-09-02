// Package jsonx encodes JSON the way Ruby's JSON library does.
//
// Go's encoding/json escapes <, > and & as <, > and & by
// default, on the assumption the output is going into an HTML document. every's
// output goes into a state file and onto a terminal, and the values are shell
// commands: `borg create && echo done > /tmp/log` is an ordinary task. Left at
// the default, the port would rewrite the bytes of every affected store the
// first time it saved, and change what `every list --json` emits for anyone
// scripting against it.
//
// Turning the escaping off requires an Encoder, which appends a newline that
// Marshal does not, so both are wrapped here rather than at each call site.
package jsonx

import (
	"bytes"
	"encoding/json"
)

// Marshal is encoding/json.Marshal without HTML escaping.
func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// MarshalIndent is the pretty form, matching Ruby's JSON.pretty_generate: two
// spaces of indent and ": " between key and value.
func MarshalIndent(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
