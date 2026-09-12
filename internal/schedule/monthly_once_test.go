package schedule

import (
	"strings"
	"testing"
	"time"
)

func at(t *testing.T, s string) time.Time {
	t.Helper()
	loc, err := time.LoadLocation(goldenZone)
	if err != nil {
		t.Skipf("zoneinfo unavailable: %v", err)
	}
	v, err := time.ParseInLocation("2006-01-02T15:04", s, loc)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Day-of-month next runs: months without the day are skipped, not
// normalized, and an occurrence at the exact instant counts as gone.
func TestNextMonthDaySkipsShortMonths(t *testing.T) {
	cases := []struct {
		day, hour, minute int
		from, want        string
	}{
		{1, 9, 0, "2026-09-02T10:30", "2026-10-01T09:00"},
		{2, 9, 0, "2026-09-02T10:30", "2026-10-02T09:00"},  // today, already passed
		{2, 11, 0, "2026-09-02T10:30", "2026-09-02T11:00"}, // today, still ahead
		{31, 9, 0, "2026-09-02T10:30", "2026-10-31T09:00"}, // September has 30 days
		{30, 9, 0, "2027-02-10T10:30", "2027-03-30T09:00"},
		{29, 9, 0, "2027-02-01T10:30", "2027-03-29T09:00"}, // 2027 is not a leap year
		{29, 9, 0, "2028-02-01T10:30", "2028-02-29T09:00"}, // 2028 is
		{1, 9, 0, "2026-10-01T09:00", "2026-11-01T09:00"},  // exact instant rolls over
		{1, 9, 0, "2026-10-31T10:30", "2026-11-01T09:00"},  // across the DST edge, wall clock holds
	}
	for _, tc := range cases {
		got := nextMonthDay(tc.day, tc.hour, tc.minute, at(t, tc.from))
		if want := at(t, tc.want); !got.Equal(want) {
			t.Errorf("day %d from %s: got %s, want %s", tc.day, tc.from, got, want)
		}
	}
}

func TestMonthlyNextRunIsEarliestEntry(t *testing.T) {
	now := goldenClock(t)
	s, err := ParseAt([]string{"monthly", "1,15", "9am,6pm"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := s.NextRun(now), at(t, "2026-09-15T09:00"); !got.Equal(want) {
		t.Errorf("NextRun = %s, want %s", got, want)
	}
	if s.HumanInterval() != "" {
		t.Errorf("HumanInterval = %q, want empty", s.HumanInterval())
	}
}

func TestOnceNextRunEndsAtTheInstant(t *testing.T) {
	now := goldenClock(t)
	s, err := ParseAt([]string{"once", "tomorrow", "9am"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !s.NextRun(now).Equal(s.At) {
		t.Errorf("before the instant, NextRun = %s, want %s", s.NextRun(now), s.At)
	}
	if !s.NextRun(s.At).IsZero() {
		t.Errorf("at the instant, NextRun = %s, want zero", s.NextRun(s.At))
	}
	if !s.NextRun(s.At.Add(time.Hour)).IsZero() {
		t.Error("after the instant, NextRun should be zero")
	}
	if s.At.Second() != 0 || s.At.Nanosecond() != 0 {
		t.Errorf("At has sub-minute precision: %s", s.At)
	}
}

// A relative once lands on a whole minute, rounding up, so the launchd
// calendar dict (minute resolution) is never already in the past.
func TestOnceDelayRoundsUpToTheMinute(t *testing.T) {
	base := goldenClock(t).Add(17 * time.Second) // 10:30:17
	cases := map[string]string{
		"60s": "2026-09-02T10:32", // 10:31:17 -> 10:32
		"90s": "2026-09-02T10:32", // 10:31:47 -> 10:32
		"30m": "2026-09-02T11:01", // 11:00:17 -> 11:01
	}
	for tok, want := range cases {
		s, err := ParseAt([]string{"once", tok}, base)
		if err != nil {
			t.Fatalf("%s: %v", tok, err)
		}
		if !s.At.Equal(at(t, want)) {
			t.Errorf("once %s from %s: At = %s, want %s", tok, base, s.At, want)
		}
	}
	// Already on the minute: no rounding.
	s, err := ParseAt([]string{"once", "60s"}, goldenClock(t))
	if err != nil {
		t.Fatal(err)
	}
	if !s.At.Equal(at(t, "2026-09-02T10:31")) {
		t.Errorf("on-the-minute base rounded: %s", s.At)
	}
}

// The weekday form is "the next one, including today if the time is still
// ahead"; the bare-time form is "today, else tomorrow".
func TestOnceResolvesAgainstTheClock(t *testing.T) {
	now := goldenClock(t) // Wednesday 2026-09-02 10:30
	cases := map[string]string{
		"once 9am":            "2026-09-03T09:00",
		"once 10:30":          "2026-09-03T10:30", // not strictly after now
		"once 10:31":          "2026-09-02T10:31",
		"once wednesday 9am":  "2026-09-09T09:00",
		"once wednesday 11am": "2026-09-02T11:00",
		"once tuesday 9am":    "2026-09-08T09:00",
		"once sunday 12am":    "2026-09-06T00:00",
	}
	for raw, want := range cases {
		s, err := ParseAt(strings.Fields(raw), now)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if !s.At.Equal(at(t, want)) {
			t.Errorf("%s: At = %s, want %s", raw, s.At, want)
		}
	}
}

// The record keeps the offset it was written with, so the instant survives a
// round trip on a machine in another zone.
func TestOnceRecordRoundTripKeepsTheOffset(t *testing.T) {
	now := goldenClock(t)
	s, err := ParseAt([]string{"once", "2026-09-05", "9am"}, now)
	if err != nil {
		t.Fatal(err)
	}
	r := s.ToRecord()
	if r.At == nil || *r.At != "2026-09-05T09:00:00-04:00" {
		t.Fatalf("record at = %v", r.At)
	}
	if r.Entries != nil || r.Interval != nil {
		t.Errorf("once record carries entries/interval: %+v", r)
	}
	back, err := FromRecord(r)
	if err != nil {
		t.Fatal(err)
	}
	if !back.At.Equal(s.At) || back.Kind != Once {
		t.Errorf("round trip: %+v", back)
	}
}

func TestMonthlyRecordRoundTrip(t *testing.T) {
	s, err := ParseAt([]string{"monthly", "31st", "17:30"}, goldenClock(t))
	if err != nil {
		t.Fatal(err)
	}
	r := s.ToRecord()
	if r.Kind != "monthly" || len(r.Entries) != 1 || r.Entries[0].Day == nil || *r.Entries[0].Day != 31 {
		t.Fatalf("record: %+v", r)
	}
	back, err := FromRecord(r)
	if err != nil {
		t.Fatal(err)
	}
	if back.Kind != Monthly || *back.Entries[0].Day != 31 || back.Entries[0].Hour != 17 {
		t.Errorf("round trip: %+v", back)
	}
}

// Records an older binary could have mangled -- a once with its instant
// dropped, a monthly entry with no day -- fail to load rather than schedule
// something else.
func TestDamagedRecordsAreRejected(t *testing.T) {
	empty := ""
	bad := "not a time"
	cases := []struct {
		name string
		rec  Record
		want string
	}{
		{"once without at", Record{Raw: "once 9am", Kind: "once"}, "without an instant"},
		{"once with empty at", Record{Raw: "once 9am", Kind: "once", At: &empty}, "without an instant"},
		{"once with garbage at", Record{Raw: "once 9am", Kind: "once", At: &bad}, "unreadable instant"},
		{"monthly without day", Record{Raw: "monthly 1st 9am", Kind: "monthly", Entries: []Entry{{Hour: 9}}}, "without a day of month"},
		{"monthly day 0", Record{Raw: "monthly 0 9am", Kind: "monthly", Entries: []Entry{{Hour: 9, Day: intp(0)}}}, "without a day of month"},
		{"monthly day 32", Record{Raw: "monthly 32 9am", Kind: "monthly", Entries: []Entry{{Hour: 9, Day: intp(32)}}}, "without a day of month"},
	}
	for _, tc := range cases {
		_, err := FromRecord(tc.rec)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.want)
		}
	}
}

// Parse (the wall-clock entry point) must still route the new keywords; the
// only difference from ParseAt is the clock.
func TestParseRoutesKeywords(t *testing.T) {
	if s, err := Parse([]string{"monthly", "1st", "9am"}); err != nil || s.Kind != Monthly {
		t.Errorf("monthly via Parse: %v %v", s, err)
	}
	if s, err := Parse([]string{"once", "2h"}); err != nil || s.Kind != Once || !s.At.After(time.Now()) {
		t.Errorf("once via Parse: %v %v", s, err)
	}
}
