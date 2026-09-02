// Package schedule parses human schedule tokens into scheduler triggers.
//
//	15m / 2h / 90s            -> interval (seconds)
//	hourly                    -> interval 3600
//	day 9am                   -> daily at 9:00
//	day 9am,6pm               -> daily at 9:00 and 18:00
//	weekdays 9:30             -> Mon-Fri at 9:30
//	weekends 11am             -> Sat+Sun at 11:00
//	monday 10:00              -> weekly
//	monday,thursday 10:00     -> twice a week
//
// Calendar schedules normalize to a list of {weekday?, hour, minute} entries --
// one launchd StartCalendarInterval dict each.
//
// Ported from lib/every/schedule.rb. This is the file the syntax freeze rests
// on: every form accepted here and every message rejected here is pinned by
// testdata/golden/cli/grammar.json.
package schedule

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Kind distinguishes the two trigger shapes. It is a string because that is
// what it is on disk.
type Kind string

const (
	Interval Kind = "interval"
	Calendar Kind = "calendar"
)

// MinInterval is the floor for interval schedules on Unix. Windows floors at
// one minute instead; that check lives in the Task Scheduler backend, since it
// is a property of that scheduler rather than of the grammar.
const MinInterval = 10

var weekdays = map[string]int{
	"sunday": 0, "monday": 1, "tuesday": 2, "wednesday": 3,
	"thursday": 4, "friday": 5, "saturday": 6,
}

// daySets maps the collective day words. A nil element means "no weekday
// constraint" -- i.e. every day -- and is what distinguishes `day 9am` from
// `sunday 9am`.
var daySets = map[string][]*int{
	"day":      {nil},
	"daily":    {nil},
	"weekdays": {intp(1), intp(2), intp(3), intp(4), intp(5)},
	"weekends": {intp(0), intp(6)},
}

var unitSeconds = map[string]int64{"s": 1, "m": 60, "h": 3600}

func intp(v int) *int { return &v }

// Entry is one calendar occurrence. Field order is the JSON key order, which
// is a compatibility surface: Ruby built these hashes as hour, minute, then
// weekday-if-present, and tasks.json is compared byte for byte.
type Entry struct {
	Hour    int  `json:"hour"`
	Minute  int  `json:"minute"`
	Weekday *int `json:"weekday,omitempty"`
}

// Schedule is a parsed schedule. Exactly one of Interval / Entries is
// meaningful, per Kind.
type Schedule struct {
	Raw      string
	Kind     Kind
	Interval int64 // seconds; interval schedules only
	Entries  []Entry
}

var (
	intervalRe = regexp.MustCompile(`^(\d+)(s|m|h)$`)
	timeRe     = regexp.MustCompile(`^(\d{1,2})(?::(\d{2}))?(am|pm)?$`)
)

// Parse turns command-line tokens into a Schedule.
func Parse(tokens []string) (*Schedule, error) {
	raw := strings.Join(tokens, " ")
	if len(tokens) == 0 {
		return nil, errors.New("empty schedule")
	}

	// Only the first token is lowercased here, which is why "5M" is accepted
	// as five minutes. The second token is lowercased inside parseTime.
	first := strings.ToLower(tokens[0])

	if len(tokens) == 1 {
		if m := intervalRe.FindStringSubmatch(first); m != nil {
			n, err := strconv.ParseInt(m[1], 10, 64)
			if err != nil {
				// Ruby's integers are arbitrary-precision, so a digit string
				// past int64 parsed there and wrote a nonsense value into the
				// unit. Rejecting is the intended behavior change; it is the
				// only one in the port, and no real input reaches it.
				return nil, fmt.Errorf("interval out of range %q", tokens[0])
			}
			secs := n * unitSeconds[m[2]]
			if secs < MinInterval {
				return nil, fmt.Errorf("interval too small (min %ds)", MinInterval)
			}
			return &Schedule{Raw: raw, Kind: Interval, Interval: secs}, nil
		}
		if first == "hourly" {
			return &Schedule{Raw: raw, Kind: Interval, Interval: 3600}, nil
		}
	}

	if len(tokens) == 2 {
		days, err := parseDays(first)
		if err != nil {
			return nil, err
		}
		times, err := parseTimes(tokens[1])
		if err != nil {
			return nil, err
		}
		// Cartesian product, days-major: `weekdays 9am,6pm` yields Mon9, Mon18,
		// Tue9, Tue18, ... The ordering is observable in the generated units.
		var entries []Entry
		for _, wd := range days {
			for _, hm := range times {
				entries = appendUnique(entries, Entry{Hour: hm[0], Minute: hm[1], Weekday: wd})
			}
		}
		return &Schedule{Raw: raw, Kind: Calendar, Entries: entries}, nil
	}

	return nil, fmt.Errorf(
		"cannot parse schedule %s "+
			"(examples: 15m | hourly | day 9am,6pm | weekdays 9:30 | monday,thursday 10:00)",
		inspect(raw))
}

// appendUnique deduplicates whole entries after the product, matching Ruby's
// .uniq on the built hashes -- distinct from the per-list dedup that parseDays
// and parseTimes already did.
func appendUnique(entries []Entry, e Entry) []Entry {
	for _, existing := range entries {
		if existing.Hour == e.Hour && existing.Minute == e.Minute && sameWeekday(existing.Weekday, e.Weekday) {
			return entries
		}
	}
	return append(entries, e)
}

func sameWeekday(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func parseDays(spec string) ([]*int, error) {
	if set, ok := daySets[spec]; ok {
		// Return a copy: the package-level sets must not be aliased into a
		// caller's Schedule.
		out := make([]*int, len(set))
		copy(out, set)
		return out, nil
	}

	// Ruby's split(",") without a limit drops trailing empty fields, so
	// "monday," parses while ",monday" does not. Times use a different splitter
	// and reject both. The asymmetry is observable, so it is reproduced.
	parts := splitDropTrailingEmpty(spec, ",")
	var out []*int
	seen := map[int]bool{}
	for _, p := range parts {
		n, ok := weekdays[p]
		if !ok {
			return nil, fmt.Errorf(
				"cannot parse days %s (day | weekdays | weekends | monday | monday,thursday)",
				inspect(spec))
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, intp(n))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf(
			"cannot parse days %s (day | weekdays | weekends | monday | monday,thursday)",
			inspect(spec))
	}
	return out, nil
}

// splitDropTrailingEmpty is Ruby's String#split(sep) with no limit: trailing
// empty fields are discarded, interior and leading ones are kept.
func splitDropTrailingEmpty(s, sep string) []string {
	parts := strings.Split(s, sep)
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

// parseTimes splits a comma time list, rejecting empty segments (",", "9am,",
// "9am,,6pm") -- each of which would otherwise become a task that never fires.
func parseTimes(spec string) ([][2]int, error) {
	bad := fmt.Errorf("cannot parse time %s (e.g. 9am or 9am,6pm)", inspect(spec))

	// Ruby's split(",", -1) KEEPS trailing empties, which is precisely how
	// "9am," is rejected here while "monday," is accepted above.
	parts := strings.Split(spec, ",")
	if spec == "" {
		return nil, bad
	}
	for _, p := range parts {
		if p == "" {
			return nil, bad
		}
	}

	var out [][2]int
	for _, p := range parts {
		hm, err := parseTime(p)
		if err != nil {
			return nil, err
		}
		dup := false
		for _, existing := range out {
			if existing == hm {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, hm)
		}
	}
	return out, nil
}

func parseTime(s string) ([2]int, error) {
	m := timeRe.FindStringSubmatch(strings.ToLower(s))
	if m == nil {
		return [2]int{}, fmt.Errorf("cannot parse time %s", inspect(s))
	}

	hour, _ := strconv.Atoi(m[1])
	minute := 0
	if m[2] != "" {
		minute, _ = strconv.Atoi(m[2])
	}
	ampm := m[3]

	if ampm != "" && (hour < 1 || hour > 12) {
		return [2]int{}, fmt.Errorf("hour out of range for am/pm: %s", inspect(s))
	}
	if ampm == "pm" && hour < 12 {
		hour += 12
	}
	if ampm == "am" && hour == 12 {
		hour = 0
	}
	if hour > 23 || minute > 59 {
		return [2]int{}, fmt.Errorf("time out of range: %s", inspect(s))
	}
	return [2]int{hour, minute}, nil
}

// HumanInterval renders the interval the way the user most likely typed it.
// Nil-equivalent (empty string) for calendar schedules.
func (s *Schedule) HumanInterval() string {
	if s.Kind != Interval {
		return ""
	}
	switch {
	case s.Interval%3600 == 0:
		return fmt.Sprintf("%dh", s.Interval/3600)
	case s.Interval%60 == 0:
		return fmt.Sprintf("%dm", s.Interval/60)
	default:
		return fmt.Sprintf("%ds", s.Interval)
	}
}

// NextRun is the earliest next calendar occurrence, or the zero Time for an
// interval schedule (which has no calendar answer) and for a calendar schedule
// with no entries (which a legacy record can produce).
func (s *Schedule) NextRun(from time.Time) time.Time {
	if s.Kind == Interval {
		return time.Time{}
	}
	var best time.Time
	for _, e := range s.Entries {
		t := s.NextForEntry(e, from)
		if best.IsZero() || t.Before(best) {
			best = t
		}
	}
	return best
}

// NextForEntry is the next occurrence of one entry at or after `from`.
func (s *Schedule) NextForEntry(e Entry, from time.Time) time.Time {
	t := time.Date(from.Year(), from.Month(), from.Day(), e.Hour, e.Minute, 0, 0, from.Location())

	if e.Weekday != nil {
		// Floored modulo, as in Ruby: Go's % truncates toward zero, so a
		// backwards weekday difference would land in -6..-1 instead of 0..6.
		// The week-push below happens to absorb that -- a negative shift lands
		// in the past, fails the After test, and gets the same seven days added
		// back -- so here this is belt-and-braces rather than load-bearing. It
		// IS load-bearing in clampWeekday, where nothing compensates.
		delta := ((*e.Weekday-int(from.Weekday()))%7 + 7) % 7
		t = shiftDays(t, delta)
		// Not strictly in the future means it is this week's occurrence and
		// already gone; push a full week.
		if !t.After(from) {
			t = shiftDays(t, 7)
		}
	} else if !t.After(from) {
		t = shiftDays(t, 1)
	}
	return t
}

// shiftDays adds whole calendar days keeping wall-clock hour:minute -- DST-safe,
// unlike adding N*86400 seconds, which drifts the displayed hour across a DST
// edge. AddDate has the same property.
func shiftDays(t time.Time, days int) time.Time {
	if days == 0 {
		return t
	}
	return t.AddDate(0, 0, days)
}
