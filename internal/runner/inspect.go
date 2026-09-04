package runner

import (
	"strconv"
	"strings"
)

// rubyInspect reproduces Ruby's String#inspect for the one message that uses
// it -- the unknown-task warning, which the e2e suite matches on.
//
// See internal/schedule for the same helper and the reasoning; it is duplicated
// rather than exported because both are three lines and a shared package for
// them would couple the parser to the runner for no benefit.
func rubyInspect(s string) string {
	q := strconv.Quote(s)
	q = strings.ReplaceAll(q, `\x1b`, `\e`)
	if !strings.Contains(q, "#") {
		return q
	}
	var b strings.Builder
	for i := 0; i < len(q); i++ {
		if q[i] == '#' && i+1 < len(q) {
			if n := q[i+1]; n == '{' || n == '$' || n == '@' {
				b.WriteByte('\\')
			}
		}
		b.WriteByte(q[i])
	}
	return b.String()
}
