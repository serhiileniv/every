package cli

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// The CLI has two distinct bad-argument shapes, and they are not
// interchangeable -- the e2e suite and the frozen surface table both match on
// the exact text.
//
//	usageError      "every: <msg>" + "see: every help"   -- a value was wrong
//	invocationError "usage: every <msg>"                 -- the form was wrong
//
// The split is the difference between "you asked for a schedule I cannot
// parse", where pointing at help is useful, and "you typed `every rm` with no
// name", where the correct form IS the message and a second line would just be
// noise.
type usageError struct {
	msg string
	// code and name are additive: they feed the --json renderer and change
	// nothing about the text, which the frozen surface table asserts byte for
	// byte. An empty code falls back to the generic one.
	code string
	name string
}

func (e *usageError) Error() string { return e.msg }

func (e *usageError) errCode(fallback string) string {
	if e.code == "" {
		return fallback
	}
	return e.code
}

func usagef(format string, a ...any) error {
	return &usageError{msg: fmt.Sprintf(format, a...)}
}

// coded is usagef with an explicit error code, for the failures a program
// needs to tell apart.
func coded(code, name, format string, a ...any) error {
	return &usageError{msg: fmt.Sprintf(format, a...), code: code, name: name}
}

type invocationError struct{ msg string }

func (e *invocationError) Error() string { return e.msg }

func invocationf(format string, a ...any) error {
	return &invocationError{msg: fmt.Sprintf(format, a...)}
}

// extractValueFlag pulls `--flag value` or `--flag=value` out of the tokens.
//
// A missing value, or a value that is itself a flag, is rejected -- otherwise
// `--name --quiet` would silently name the task "quiet" and swallow the flag.
func extractValueFlag(tokens []string, flag string) (rest []string, value string, found bool, err error) {
	for i, tok := range tokens {
		if tok == flag {
			if i+1 >= len(tokens) || strings.HasPrefix(tokens[i+1], "--") {
				return nil, "", false, usagef("%s needs a value", flag)
			}
			rest = append(append([]string{}, tokens[:i]...), tokens[i+2:]...)
			return rest, tokens[i+1], true, nil
		}
	}
	for i, tok := range tokens {
		if strings.HasPrefix(tok, flag+"=") {
			v := strings.SplitN(tok, "=", 2)[1]
			if v == "" {
				return nil, "", false, usagef("%s needs a value", flag)
			}
			rest = append(append([]string{}, tokens[:i]...), tokens[i+1:]...)
			return rest, v, true, nil
		}
	}
	return tokens, "", false, nil
}

// removeFlag deletes every occurrence of a boolean flag, reporting whether it
// was present. `--json` is accepted anywhere in argv, not just in one position.
func removeFlag(tokens []string, flag string) ([]string, bool) {
	var out []string
	found := false
	for _, t := range tokens {
		if t == flag {
			found = true
			continue
		}
		out = append(out, t)
	}
	return out, found
}

var (
	sanitizeRe   = regexp.MustCompile(`[^a-z0-9_.-]`)
	trimDashesRe = regexp.MustCompile(`^-+|-+$`)
	allDotsRe    = regexp.MustCompile(`^\.+$`)
	durationRe   = regexp.MustCompile(`^(\d+)(s|m|h)$`)
)

// sanitize reduces a string to something safe as a filename component.
//
// The result becomes part of a plist label and a unit filename, so anything
// outside the allowed set becomes a dash. "." and ".." sanitize to empty rather
// than to themselves: they are syntactically fine but are not usable filenames.
func sanitize(s string) string {
	s = strings.ToLower(s)
	s = sanitizeRe.ReplaceAllString(s, "-")
	s = trimDashesRe.ReplaceAllString(s, "")
	if allDotsRe.MatchString(s) {
		return ""
	}
	return s
}

// truncate cuts to n characters. Bytes would split a multi-byte rune and leave
// an invalid name.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// deriveName invents a task name from the command when --name was not given.
//
// The basename of the first word, sanitized. On a collision it appends -2, -3
// and so on, re-truncating so the total stays within the cap -- otherwise a
// name at exactly the limit would grow past it with every collision.
func deriveName(cmd string, exists func(string) bool) string {
	first := ""
	if fields := strings.Fields(strings.TrimSpace(cmd)); len(fields) > 0 {
		first = fields[0]
	}
	base := truncate(sanitize(filepath.Base(first)), maxName)
	if base == "" {
		base = "task"
	}

	name := base
	for i := 2; exists(name); i++ {
		suffix := "-" + strconv.Itoa(i)
		name = truncate(base, maxName-len(suffix)) + suffix
	}
	return name
}

// parseDuration reads a --timeout value. Deliberately the same tiny grammar as
// a schedule interval, so there is only one duration syntax to learn.
func parseDuration(raw string) (int, error) {
	m := durationRe.FindStringSubmatch(raw)
	if m == nil {
		return 0, coded(CodeBadDuration, "", "bad duration %s (e.g. 90s, 30m, 2h)", rubyInspect(raw))
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, coded(CodeBadDuration, "", "bad duration %s (e.g. 90s, 30m, 2h)", rubyInspect(raw))
	}
	secs := n * map[string]int{"s": 1, "m": 60, "h": 3600}[m[2]]
	if secs == 0 {
		return 0, coded(CodeBadDuration, "", "--timeout must be greater than 0")
	}
	return secs, nil
}

// taskStatus is what `list` shows in the STATUS column.
//
// It reflects the SCHEDULER's reality, not just our ledger: a task the
// scheduler does not have shows "unscheduled" rather than a stale "ok" from
// the last time it did run.
func taskStatus(paused, scheduled bool, lastExit *int) string {
	switch {
	case paused:
		return "paused"
	case !scheduled:
		return "unscheduled"
	case lastExit == nil:
		return "·"
	case *lastExit == 0:
		return "ok"
	default:
		return fmt.Sprintf("FAIL(%d)", *lastExit)
	}
}

// rubyInspect matches the quoting in the frozen error messages.
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
