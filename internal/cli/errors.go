package cli

import (
	"errors"
	"fmt"

	"github.com/serhiileniv/every/internal/backend"
	"github.com/serhiileniv/every/internal/paths"
	"github.com/serhiileniv/every/internal/store"
)

// Error codes.
//
// A closed vocabulary, so a program can branch on what went wrong instead of
// matching prose. The CODE is the contract: the human message beside it may be
// reworded at any time, the code may not, and neither may the exit status it
// maps to -- those were frozen in 0.4.0 and are asserted by the surface table.
const (
	CodeUsage               = "usage"
	CodeBadSchedule         = "bad_schedule"
	CodeBadDuration         = "bad_duration"
	CodeBadName             = "bad_name"
	CodeAlreadyExists       = "already_exists"
	CodeUnsupportedSchedule = "unsupported_schedule"
	CodeNoSuchTask          = "no_such_task"
	CodeNoLogs              = "no_logs"
	CodeCorruptStore        = "corrupt_store"
	CodeSchedulerFailed     = "scheduler_failed"
	CodeInternal            = "internal"
)

// errorPayload is what `--json` writes for a failure.
//
// To STDERR, deliberately, never stdout. stdout stays the data channel, so
// `every list --json | jq` never has to defend against an error object turning
// up where an array was promised. The exit code still carries the outcome; this
// only explains it.
type errorPayload struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Name    string `json:"name,omitempty"`
}

// classify maps an error to its code and exit status.
//
// One place, so the mapping cannot drift between the text and JSON renderers --
// they must agree on the exit code or an agent and a human would disagree about
// whether the same command failed.
func classify(err error) (code string, exit int) {
	var exitErr *exitError
	if errors.As(err, &exitErr) {
		if exitErr.code == paths.ExitNoInput {
			return exitErr.errCode(CodeNoSuchTask), exitErr.code
		}
		return exitErr.errCode(CodeInternal), exitErr.code
	}

	var invocation *invocationError
	if errors.As(err, &invocation) {
		return CodeUsage, paths.ExitUsage
	}

	var unsupported *backend.UnsupportedScheduleError
	if errors.As(err, &unsupported) {
		return CodeUnsupportedSchedule, paths.ExitUsage
	}

	var usage *usageError
	if errors.As(err, &usage) {
		return usage.errCode(CodeUsage), paths.ExitUsage
	}

	var corrupt *store.ErrCorrupt
	if errors.As(err, &corrupt) {
		return CodeCorruptStore, 1
	}

	return CodeInternal, 1
}

// named pulls the task name out of an error that carries one, so the payload
// can name it as a field rather than only inside a sentence.
func named(err error) string {
	var exitErr *exitError
	if errors.As(err, &exitErr) {
		return exitErr.name
	}
	var usage *usageError
	if errors.As(err, &usage) {
		return usage.name
	}
	return ""
}

// renderError writes a failure in whichever form the caller asked for, and
// returns the exit code. Both paths go through classify, so they cannot
// disagree.
func (c *CLI) renderError(err error, asJSON bool) int {
	code, exit := classify(err)

	if !asJSON {
		c.renderErrorText(err)
		return exit
	}

	payload := errorPayload{Error: code, Message: humanMessage(err), Name: named(err)}
	b, mErr := marshalJSON(payload)
	if mErr != nil {
		fmt.Fprintf(c.Stderr, "every: %s\n", err)
		return exit
	}
	fmt.Fprintln(c.Stderr, string(b))
	return exit
}

// humanMessage is the sentence a person would read, without the "every: "
// prefix or the usage second line -- those are presentation, and JSON callers
// supply their own.
func humanMessage(err error) string {
	var invocation *invocationError
	if errors.As(err, &invocation) {
		return "usage: every " + invocation.msg
	}
	var exitErr *exitError
	if errors.As(err, &exitErr) {
		return trimEveryPrefix(exitErr.msg)
	}
	return trimEveryPrefix(err.Error())
}

func trimEveryPrefix(s string) string {
	const p = "every: "
	if len(s) > len(p) && s[:len(p)] == p {
		return s[len(p):]
	}
	return s
}
