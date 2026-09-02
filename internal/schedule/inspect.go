package schedule

import (
	"strconv"
	"strings"
)

// inspect reproduces Ruby's String#inspect, which appears inside several
// user-facing error messages ("cannot parse time \"9:5\""). Those messages are
// frozen by testdata/golden/cli/grammar.json, so the quoting has to match
// rather than merely look similar.
//
// strconv.Quote agrees with Ruby on the cases that occur here -- double quotes,
// backslash escapes, printable Unicode passed through unescaped -- and differs
// on two:
//
//	Ruby escapes # before {, $ or @, because those interpolate in a
//	double-quoted literal.
//	Ruby writes \e for U+001B where Go writes \x1b.
//
// Both are handled so an odd token in an error message cannot drift.
func inspect(s string) string {
	q := strconv.Quote(s)
	q = strings.ReplaceAll(q, `\x1b`, `\e`)

	if !strings.Contains(q, "#") {
		return q
	}
	var b strings.Builder
	b.Grow(len(q) + 4)
	for i := 0; i < len(q); i++ {
		c := q[i]
		if c == '#' && i+1 < len(q) {
			if n := q[i+1]; n == '{' || n == '$' || n == '@' {
				b.WriteByte('\\')
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}
