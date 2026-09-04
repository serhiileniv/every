package schedule

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The clock and zone scripts/golden.rb pinned. Both have to match or next_run
// lands on a different day, and the offsets in the fixtures disagree.
const (
	goldenZone = "America/New_York"
	goldenNow  = "2026-09-02T10:30:00-04:00"
)

func goldenClock(t *testing.T) time.Time {
	t.Helper()
	loc, err := time.LoadLocation(goldenZone)
	if err != nil {
		t.Skipf("zoneinfo unavailable: %v", err)
	}
	now, err := time.ParseInLocation(time.RFC3339, goldenNow, loc)
	if err != nil {
		t.Fatal(err)
	}
	return now
}

// scheduleGolden mirrors one testdata/golden/schedule/*.json file.
type scheduleGolden struct {
	Raw     string `json:"raw"`
	Kind    string `json:"kind"`
	ToH     Record `json:"to_h"`
	NextRun string `json:"next_run"` // empty string decodes from JSON null
	Human   string `json:"human"`
}

// Every schedule in the matrix, round-tripped and evaluated at the frozen
// clock, compared against what the Ruby produced. This covers the two things
// unit tests written from scratch would most likely get subtly wrong: the
// entry ordering of a day-set product, and which day next_run lands on when
// today's occurrence has already passed.
func TestScheduleGoldens(t *testing.T) {
	now := goldenClock(t)
	dir := goldenDir(t, "schedule")

	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no schedule fixtures; regenerate with scripts/golden.rb")
	}

	for _, f := range files {
		slug := strings.TrimSuffix(filepath.Base(f), ".json")
		t.Run(slug, func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var want scheduleGolden
			if err := json.Unmarshal(raw, &want); err != nil {
				t.Fatal(err)
			}

			// Rebuild from the stored record rather than by re-parsing: that
			// exercises FromRecord, including the legacy daily/weekly shapes
			// which have no parseable token form at all.
			s, err := FromRecord(want.ToH)
			if err != nil {
				t.Fatalf("FromRecord: %v", err)
			}

			assertRecordEqual(t, s.ToRecord(), want.ToH)

			if got := s.HumanInterval(); got != want.Human {
				t.Errorf("HumanInterval() = %q, want %q", got, want.Human)
			}

			next := s.NextRun(now)
			gotNext := ""
			if !next.IsZero() {
				gotNext = next.Format(time.RFC3339)
			}
			if gotNext != want.NextRun {
				t.Errorf("NextRun(%s) = %q, want %q", goldenNow, gotNext, want.NextRun)
			}
		})
	}
}

// shift_days is calendar arithmetic rather than +N*86400 precisely so the
// displayed wall-clock hour survives a DST edge. Adding seconds would show
// 08:00 or 10:00 on the transition day.
//
// The plan called for these two dates specifically; they are the spring-forward
// and fall-back boundaries in the fixture zone.
func TestNextRunKeepsWallClockAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation(goldenZone)
	if err != nil {
		t.Skipf("zoneinfo unavailable: %v", err)
	}

	daily9 := &Schedule{Raw: "day 9am", Kind: Calendar, Entries: []Entry{{Hour: 9, Minute: 0}}}

	cases := []struct {
		name string
		from time.Time
		want string // wall clock of the next occurrence
	}{
		{
			name: "across spring forward",
			from: time.Date(2026, 3, 7, 10, 0, 0, 0, loc), // day before the jump
			want: "2026-03-08 09:00",
		},
		{
			name: "across fall back",
			from: time.Date(2026, 10, 31, 10, 0, 0, 0, loc),
			want: "2026-11-01 09:00",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := daily9.NextRun(tc.from).Format("2006-01-02 15:04")
			if got != tc.want {
				t.Errorf("NextRun = %s, want %s (wall clock must not drift across the DST edge)", got, tc.want)
			}
		})
	}
}

// A weekly entry whose day is today but whose time has passed must move a full
// week, not fire again today. The `<=` comparison in the Ruby is what makes an
// exact second-boundary match roll forward too.
func TestNextRunRollsForwardOnExactMatch(t *testing.T) {
	loc := time.UTC
	mon := 1
	s := &Schedule{Raw: "monday 10:00", Kind: Calendar,
		Entries: []Entry{{Hour: 10, Minute: 0, Weekday: &mon}}}

	// A Monday, exactly at the scheduled instant.
	from := time.Date(2026, 9, 7, 10, 0, 0, 0, loc)
	if from.Weekday() != time.Monday {
		t.Fatalf("fixture date is a %v, not a Monday", from.Weekday())
	}
	got := s.NextRun(from)
	want := from.AddDate(0, 0, 7)
	if !got.Equal(want) {
		t.Errorf("NextRun at the exact scheduled instant = %s, want next week %s", got, want)
	}
}

// A calendar record with no entries yields no answer rather than panicking --
// a legacy store can contain one, and `list` renders it as "?".
func TestNextRunWithNoEntries(t *testing.T) {
	s := &Schedule{Raw: "?", Kind: Calendar}
	if got := s.NextRun(time.Now()); !got.IsZero() {
		t.Errorf("NextRun with no entries = %s, want the zero time", got)
	}
}
