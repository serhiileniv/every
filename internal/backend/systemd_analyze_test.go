package backend

import (
	"os/exec"
	"strings"
	"testing"
)

// Every generated OnCalendar expression, validated by real systemd.
//
// Ported from test/systemd_calendar_check.rb. The unit tests prove the string
// has the shape we intended; only systemd can say whether systemd accepts it.
// That distinction is not academic -- an expression can look perfectly
// reasonable and be rejected at load time, and the user finds out because a
// timer never fires, on their machine, with no error anywhere every can see.
//
// Skips where systemd-analyze is absent, which is every developer machine that
// is not Linux. CI runs it on Linux and in a pinned container.
func TestGeneratedOnCalendarIsAcceptedBySystemd(t *testing.T) {
	analyze, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze not available; CI validates this on Linux")
	}

	seen := map[string]bool{}
	var exprs []string
	for _, sched := range loadSchedules(t) {
		if sched.Kind != "calendar" {
			continue
		}
		for _, line := range CalendarLines(sched) {
			if !seen[line] {
				seen[line] = true
				exprs = append(exprs, line)
			}
		}
	}
	if len(exprs) == 0 {
		t.Fatal("no calendar expressions to check; the fixtures are wrong")
	}

	for _, expr := range exprs {
		t.Run(expr, func(t *testing.T) {
			out, err := exec.Command(analyze, "calendar", expr).CombinedOutput()
			if err != nil {
				t.Errorf("systemd rejects %q:\n%s", expr, out)
				return
			}
			// A syntactically valid expression that can never fire is just as
			// broken as one systemd refuses.
			if strings.Contains(string(out), "never") {
				t.Errorf("%q parses but never elapses:\n%s", expr, out)
			}
		})
	}
	t.Logf("validated %d distinct OnCalendar expressions against systemd", len(exprs))
}
