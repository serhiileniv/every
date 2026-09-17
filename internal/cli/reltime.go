package cli

import (
	"fmt"
	"time"
)

// Relative time, for the two columns of `list` that ask relative questions.
//
// "Did it run?" and "when next?" are both asked about now, and `24 Jul 09:00`
// makes the reader do the subtraction. The absolute instant is still the right
// answer somewhere -- it is in `inspect` and in every --json payload, which is
// where precision belongs.

// farHorizon is where relative stops helping. "in 217d" tells a reader less
// than a date does, and a schedule that far out is a calendar entry, not a
// thing about to happen.
const farHorizon = 90 * 24 * time.Hour

// absoluteFormat is the one absolute rendering the human surfaces share, so
// `list` past the horizon and `inspect` cannot disagree about what a time
// looks like.
const absoluteFormat = "02 Jan 15:04"

// humanPast renders a past instant as "2h ago".
func humanPast(t, now time.Time) string {
	d := now.Sub(t)
	if d < 0 {
		// Clock skew, or a run recorded by a machine whose time moved. Saying
		// "in 3s" about the last run reads as a bug; "just now" is true enough.
		return "just now"
	}
	if d >= farHorizon {
		return t.Format(absoluteFormat)
	}
	if d < time.Minute {
		return "just now"
	}
	return humanDuration(d) + " ago"
}

// humanFuture renders a future instant as "in 18h".
func humanFuture(t, now time.Time) string {
	d := t.Sub(now)
	if d < 0 {
		// The moment has passed and the scheduler has not fired it yet, which
		// is normal within the minute a calendar trigger lands on.
		return "due"
	}
	if d >= farHorizon {
		return t.Format(absoluteFormat)
	}
	if d < time.Minute {
		return "now"
	}
	return "in " + humanDuration(d)
}

// humanDuration is one unit, the largest that fits, no decimals.
//
// Deliberately coarse: the column exists to be scanned, and "in 2h" and
// "in 2h14m" answer the same question. The precise instant is in inspect.
func humanDuration(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
