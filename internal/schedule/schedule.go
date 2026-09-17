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
//	monthly 1st 9am           -> day of month
//	monthly 1,15 9am,6pm      -> several days, several times
//	once 15:30                -> today (or tomorrow, if 15:30 has passed), then gone
//	once tomorrow 9am         -> a moment; also today/<weekday>/YYYY-MM-DD
//	once 45m                  -> now + 45m, on a whole minute
//
// Calendar schedules normalize to a list of {weekday?, hour, minute} entries --
// one launchd StartCalendarInterval dict each. Monthly schedules are the same
// list with a day-of-month per entry instead of a weekday, under their own
// kind so that a binary which predates them refuses the record rather than
// silently reading it as daily. A once schedule is a single instant.
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
	// Monthly is a calendar schedule keyed by day of month. Its own kind, not
	// Calendar with an extra field: an older binary decoding Calendar entries
	// would drop the unknown day key, read the task as daily, and its migration
	// would then rewrite the unit that way. An unknown kind fails loudly.
	Monthly Kind = "monthly"
	// Once is a single instant. The task removes itself after that run.
	Once Kind = "once"
)

// MinInterval is the floor for interval schedules on Unix. Windows floors at
// one minute instead; that check lives in the Task Scheduler backend, since it
// is a property of that scheduler rather than of the grammar.
const MinInterval = 10

// MinOnceDelay is the floor for `once <interval>`. launchd calendar triggers
// are minute-resolution, so a delay under a minute would have to round to an
// instant that has either passed or is further away than asked.
const MinOnceDelay = 60

// OnceGrace is how long after its moment a one-shot counts as still firing
// rather than overdue. launchd starts a calendar job some way into its minute,
// so a task whose moment has just arrived may not have been spawned yet;
// calling it missed then -- or unloading it -- would lose it.
const OnceGrace = 2 * time.Minute

// maxMonthWalk bounds the search for the next day-of-month occurrence. The
// 29th recurs at least every fourth year; 96 months covers a century skip.
const maxMonthWalk = 96

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
	// Day is a day of month, 1-31, set only on Monthly entries. Appended after
	// Weekday because field order is key order and existing records must
	// round-trip unchanged; omitempty keeps it out of every other kind.
	Day *int `json:"day,omitempty"`
}

// Schedule is a parsed schedule. Exactly one of Interval / Entries / At is
// meaningful, per Kind.
type Schedule struct {
	Raw      string
	Kind     Kind
	Interval Seconds   // interval schedules only
	Entries  []Entry   // calendar and monthly
	At       time.Time // once only; seconds are always zero
}

var (
	intervalRe = regexp.MustCompile(`^(\d+)(s|m|h)$`)
	timeRe     = regexp.MustCompile(`^(\d{1,2})(?::(\d{2}))?(am|pm)?$`)
	monthDayRe = regexp.MustCompile(`^(\d{1,2})(st|nd|rd|th)?$`)
	isoDateRe  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// Parse turns command-line tokens into a Schedule, resolving a once schedule
// against the wall clock.
func Parse(tokens []string) (*Schedule, error) {
	return ParseAt(tokens, time.Now())
}

// ParseAt is Parse with an explicit clock. Only once schedules consult it:
// "once 9am" means today or tomorrow depending on what time it is, and the
// instant is what gets stored, not the phrase.
func ParseAt(tokens []string, now time.Time) (*Schedule, error) {
	raw := strings.Join(tokens, " ")
	if len(tokens) == 0 {
		return nil, errors.New("empty schedule")
	}

	// Only the first token is lowercased here, which is why "5M" is accepted
	// as five minutes. The second token is lowercased inside parseTime.
	first := strings.ToLower(tokens[0])

	// The keyword forms dispatch before the length checks below, so that every
	// other token sequence keeps the exact acceptance and message it has
	// always had -- those are frozen by the grammar table.
	switch first {
	case "monthly":
		return parseMonthly(raw, tokens[1:])
	case "once":
		return parseOnce(raw, tokens[1:], now)
	}

	if len(tokens) == 1 {
		if m := intervalRe.FindStringSubmatch(first); m != nil {
			// Arbitrary precision, because Ruby's integers have none and the
			// grammar is frozen. See the Seconds doc comment.
			n, err := parseSeconds(m[1])
			if err != nil {
				return nil, fmt.Errorf("cannot parse schedule %s", inspect(raw))
			}
			secs := n.Mul(unitSeconds[m[2]])
			if secs.Cmp(MinInterval) < 0 {
				return nil, fmt.Errorf("interval too small (min %ds)", MinInterval)
			}
			return &Schedule{Raw: raw, Kind: Interval, Interval: secs}, nil
		}
		if first == "hourly" {
			return &Schedule{Raw: raw, Kind: Interval, Interval: SecondsOf(3600)}, nil
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
		if existing.Hour == e.Hour && existing.Minute == e.Minute &&
			sameIntp(existing.Weekday, e.Weekday) && sameIntp(existing.Day, e.Day) {
			return entries
		}
	}
	return append(entries, e)
}

func sameIntp(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// parseMonthly handles `monthly <days> <times>`: a days-major product, like
// the weekday forms, with a day of month in place of the weekday.
func parseMonthly(raw string, rest []string) (*Schedule, error) {
	if len(rest) != 2 {
		return nil, fmt.Errorf(
			"cannot parse schedule %s (monthly <day[,day]> <time[,time]>, e.g. monthly 1st 9am | monthly 1,15 18:00)",
			inspect(raw))
	}
	days, err := parseMonthDays(rest[0])
	if err != nil {
		return nil, err
	}
	times, err := parseTimes(rest[1])
	if err != nil {
		return nil, err
	}
	var entries []Entry
	for _, d := range days {
		for _, hm := range times {
			entries = appendUnique(entries, Entry{Hour: hm[0], Minute: hm[1], Day: intp(d)})
		}
	}
	return &Schedule{Raw: raw, Kind: Monthly, Entries: entries}, nil
}

// parseMonthDays accepts "1", "1st", "1,15", "1st,15th". Empty segments are
// rejected as in parseTimes: this grammar is new, so it gets the strict rule
// on both sides. Days 29-31 are allowed and simply skip shorter months, which
// is what all three schedulers do with them.
func parseMonthDays(spec string) ([]int, error) {
	bad := fmt.Errorf("cannot parse day of month %s (1-31, e.g. 1st or 1,15)", inspect(spec))
	if spec == "" {
		return nil, bad
	}
	var out []int
	seen := map[int]bool{}
	for _, p := range strings.Split(spec, ",") {
		m := monthDayRe.FindStringSubmatch(strings.ToLower(p))
		if m == nil {
			return nil, bad
		}
		n, _ := strconv.Atoi(m[1])
		if n < 1 || n > 31 {
			return nil, fmt.Errorf("day of month out of range: %s (1-31)", inspect(p))
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out, nil
}

// parseOnce resolves `once <when>` to an instant strictly after now:
//
//	once 15:30              today, or tomorrow if 15:30 has passed
//	once today 6pm          today only; an error if 6pm has passed
//	once tomorrow 9am
//	once friday 10:00       the next Friday; today's if 10:00 is still ahead
//	once 2026-09-10 15:30
//	once 45m                now + 45m, rounded up to a whole minute
//
// Seconds are always zero because launchd calendar triggers have no seconds
// field; an instant with 45 seconds on it would be written as :00 and already
// be in the past when it fired.
func parseOnce(raw string, rest []string, now time.Time) (*Schedule, error) {
	usage := fmt.Errorf(
		"cannot parse schedule %s (once <time> | once today|tomorrow|<weekday>|YYYY-MM-DD <time> | once 45m)",
		inspect(raw))
	loc := now.Location()
	var at time.Time

	switch len(rest) {
	case 1:
		tok := strings.ToLower(rest[0])
		if m := intervalRe.FindStringSubmatch(tok); m != nil {
			n, err := parseSeconds(m[1])
			if err != nil {
				return nil, usage
			}
			secs := n.Mul(unitSeconds[m[2]])
			if secs.Cmp(MinOnceDelay) < 0 {
				return nil, fmt.Errorf("once delay too small (min %ds)", MinOnceDelay)
			}
			// A delay far enough out to overflow a Duration is not a real
			// request; the interval grammar accepts arbitrary digits, this
			// form does not need to.
			if secs.Cmp(1<<31) > 0 {
				return nil, fmt.Errorf("once delay too large: %s", inspect(rest[0]))
			}
			at = now.Add(time.Duration(secs.Int64()) * time.Second)
			at = ceilMinute(at)
			return &Schedule{Raw: raw, Kind: Once, At: at}, nil
		}
		hm, err := parseTime(rest[0])
		if err != nil {
			return nil, err
		}
		at = time.Date(now.Year(), now.Month(), now.Day(), hm[0], hm[1], 0, 0, loc)
		if !at.After(now) {
			at = shiftDays(at, 1)
		}

	case 2:
		day := strings.ToLower(rest[0])
		hm, err := parseTime(rest[1])
		if err != nil {
			return nil, err
		}
		today := time.Date(now.Year(), now.Month(), now.Day(), hm[0], hm[1], 0, 0, loc)
		switch {
		case day == "today":
			at = today
		case day == "tomorrow":
			at = shiftDays(today, 1)
		case isoDateRe.MatchString(day):
			d, err := time.ParseInLocation("2006-01-02", day, loc)
			// ParseInLocation normalizes nothing: 2026-02-30 is an error, not
			// March 2, which is what a one-shot wants.
			if err != nil {
				return nil, fmt.Errorf("cannot parse date %s (YYYY-MM-DD)", inspect(rest[0]))
			}
			at = time.Date(d.Year(), d.Month(), d.Day(), hm[0], hm[1], 0, 0, loc)
		default:
			wd, ok := weekdays[day]
			if !ok {
				return nil, fmt.Errorf(
					"cannot parse day %s (today | tomorrow | monday | 2026-09-10)", inspect(rest[0]))
			}
			delta := ((wd-int(now.Weekday()))%7 + 7) % 7
			at = shiftDays(today, delta)
			if !at.After(now) {
				at = shiftDays(at, 7)
			}
		}

	default:
		return nil, usage
	}

	if !at.After(now) {
		return nil, fmt.Errorf("one-shot time has already passed: %s", inspect(strings.Join(rest, " ")))
	}
	return &Schedule{Raw: raw, Kind: Once, At: at}, nil
}

// ceilMinute rounds up to the next whole minute, leaving a time already on
// the minute alone.
func ceilMinute(t time.Time) time.Time {
	t = t.Truncate(0) // drop the monotonic reading, so Equal below is by wall clock
	floor := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, t.Location())
	if floor.Equal(t) {
		return floor
	}
	return floor.Add(time.Minute)
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
	case s.Interval.Mod(3600) == 0:
		return s.Interval.Div(3600) + "h"
	case s.Interval.Mod(60) == 0:
		return s.Interval.Div(60) + "m"
	default:
		return s.Interval.String() + "s"
	}
}

// OnceOverdue reports whether a once schedule's moment, plus OnceGrace, has
// gone by. False for every other kind.
func (s *Schedule) OnceOverdue(now time.Time) bool {
	return s.Kind == Once && !now.Before(s.At.Add(OnceGrace))
}

// NextRun is the earliest next calendar occurrence, or the zero Time for an
// interval schedule (which has no calendar answer) and for a calendar schedule
// with no entries (which a legacy record can produce).
func (s *Schedule) NextRun(from time.Time) time.Time {
	switch s.Kind {
	case Interval:
		return time.Time{}
	case Once:
		// Once the instant has passed there is no next run: either it fired
		// and the task is about to remove itself, or it was missed.
		if s.At.After(from) {
			return s.At
		}
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
	if e.Day != nil {
		return nextMonthDay(*e.Day, e.Hour, e.Minute, from)
	}

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

// nextMonthDay is the next occurrence of day-of-month `day` at hour:minute
// strictly after `from`. Months that lack the day (the 31st of September, the
// 29th of most Februaries) are skipped rather than normalized: time.Date would
// quietly turn February 31 into March 3, so the day count is checked first.
func nextMonthDay(day, hour, minute int, from time.Time) time.Time {
	loc := from.Location()
	year, month := from.Year(), from.Month()
	for i := 0; i < maxMonthWalk; i++ {
		// Day zero of the following month is the last day of this one.
		last := time.Date(year, month+1, 0, 0, 0, 0, 0, loc).Day()
		if day <= last {
			t := time.Date(year, month, day, hour, minute, 0, 0, loc)
			if t.After(from) {
				return t
			}
		}
		month++
		if month > time.December {
			month = time.January
			year++
		}
	}
	return time.Time{}
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
