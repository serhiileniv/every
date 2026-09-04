package schedule

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// next_run compared against the Ruby across many clocks rather than the single
// frozen one the goldens capture. This is where the port is most likely to
// drift quietly: floored-vs-truncated modulo on the weekday delta, the
// strictly-in-the-future comparison, and calendar-day arithmetic over DST
// edges. Spot-checking a few dates by hand would not find any of those.
//
// Skips once the Ruby tree is gone, which is the goal of the rewrite.
func TestNextRunMatchesRuby(t *testing.T) {
	ruby, err := exec.LookPath("ruby")
	if err != nil {
		t.Skip("no ruby on PATH; the Ruby tree is what this port replaces")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "lib", "every.rb")); err != nil {
		t.Skip("Ruby tree removed; differential comparison no longer applies")
	}

	const zone = "America/New_York"
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Skipf("zoneinfo unavailable: %v", err)
	}

	// Every calendar form, including the ones whose entry lists are longest.
	specs := [][]string{
		{"day", "9am"}, {"day", "9am,6pm"}, {"day", "00:00"}, {"day", "23:59"},
		{"weekdays", "9:30"}, {"weekends", "11am"},
		{"monday", "10:00"}, {"sunday", "12am"}, {"saturday", "11pm"},
		{"monday,thursday", "6pm"},
	}

	// Clocks chosen to straddle both DST transitions, a leap day, a year end,
	// and every weekday, at times before/at/after typical schedule points.
	var clocks []time.Time
	for _, d := range []struct{ y, m, day int }{
		{2026, 3, 7}, {2026, 3, 8}, {2026, 3, 9}, // spring forward
		{2026, 10, 31}, {2026, 11, 1}, {2026, 11, 2}, // fall back
		{2026, 12, 31}, {2027, 1, 1},
		{2028, 2, 28}, {2028, 2, 29}, // leap day
		{2026, 9, 1}, {2026, 9, 2}, {2026, 9, 3}, {2026, 9, 4},
		{2026, 9, 5}, {2026, 9, 6}, {2026, 9, 7}, // a full week
	} {
		for _, hm := range [][2]int{{0, 0}, {9, 0}, {10, 30}, {18, 0}, {23, 59}} {
			clocks = append(clocks, time.Date(d.y, time.Month(d.m), d.day, hm[0], hm[1], 0, 0, loc))
		}
	}

	type query struct {
		Spec []string `json:"spec"`
		From string   `json:"from"`
	}
	var queries []query
	for _, s := range specs {
		for _, c := range clocks {
			queries = append(queries, query{Spec: s, From: c.Format(time.RFC3339)})
		}
	}

	payload, err := json.Marshal(queries)
	if err != nil {
		t.Fatal(err)
	}
	// Written to a file rather than passed as an argument: Windows caps a
	// command line at ~32 KB and this corpus is larger, which failed as
	// "The filename or extension is too long" -- a message that names neither.
	in := filepath.Join(t.TempDir(), "queries.json")
	if err := os.WriteFile(in, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	const script = `
$LOAD_PATH.unshift File.join(ARGV[0], "lib")
require "every"
require "json"
require "time"
puts JSON.generate(JSON.parse(File.read(ARGV[1])).map { |q|
  s = Every::Schedule.parse(q["spec"])
  n = s.next_run(Time.parse(q["from"]))
  n && n.iso8601
})
`
	cmd := exec.Command(ruby, "-e", script, root, in)
	cmd.Env = append(os.Environ(), "TZ="+zone)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ruby: %v\n%s", err, out)
	}

	var want []string
	if err := json.Unmarshal(out, &want); err != nil {
		t.Fatalf("decoding ruby output: %v\n%s", err, out)
	}
	if len(want) != len(queries) {
		t.Fatalf("ruby returned %d results for %d queries", len(want), len(queries))
	}

	mismatches := 0
	for i, q := range queries {
		s, err := Parse(q.Spec)
		if err != nil {
			t.Fatalf("Parse(%q): %v", q.Spec, err)
		}
		from, err := time.ParseInLocation(time.RFC3339, q.From, loc)
		if err != nil {
			t.Fatal(err)
		}
		got := s.NextRun(from).Format(time.RFC3339)
		if got != want[i] {
			mismatches++
			if mismatches <= 10 {
				t.Errorf("%v from %s: got %s, ruby says %s", q.Spec, q.From, got, want[i])
			}
		}
	}
	if mismatches > 10 {
		t.Errorf("... and %d more mismatches", mismatches-10)
	}
	t.Logf("compared %d next_run results across %d clocks", len(queries), len(clocks))
}
