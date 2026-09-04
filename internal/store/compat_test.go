package store

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/serhiileniv/every/internal/schedule"
)

// State written by every version this tool has shipped must still load.
//
// A user upgrading from 0.1 has a tasks.json in a shape 0.4 never writes. If
// it does not load, their tasks are gone -- not broken, gone -- and the store
// is the only record that they ever existed. These fixtures are the real
// historical shapes, taken from DECISIONS.md and Schedule.from_h's migration
// branches.
func TestReadsEveryHistoricalStoreShape(t *testing.T) {
	cases := []struct {
		name    string
		json    string
		task    string
		wantCmd string
		// wantKind is what the schedule normalizes to after loading.
		wantKind schedule.Kind
	}{
		{
			name: "0.1 daily record",
			json: `{"tasks":{"report":{"cmd":"echo hi","schedule":{"raw":"daily 9:00","kind":"daily","hour":9,"minute":0},"cwd":"/tmp","created_at":"2026-07-24T09:00:00+03:00","paused":false,"quiet":false}}}`,
			task: "report", wantCmd: "echo hi", wantKind: schedule.Calendar,
		},
		{
			name: "0.1 weekly record",
			json: `{"tasks":{"weekly":{"cmd":"echo hi","schedule":{"raw":"weekly","kind":"weekly","hour":18,"minute":30,"weekday":3},"cwd":"/tmp","created_at":"2026-07-24T09:00:00+03:00","paused":false,"quiet":false}}}`,
			task: "weekly", wantCmd: "echo hi", wantKind: schedule.Calendar,
		},
		{
			// A legacy weekday 7 means Sunday, and must clamp rather than
			// index past the end of a day table.
			name: "legacy weekday 7",
			json: `{"tasks":{"sun":{"cmd":"echo hi","schedule":{"raw":"weekly","kind":"weekly","hour":8,"minute":0,"weekday":7},"cwd":"/tmp","created_at":"2026-07-24T09:00:00+03:00","paused":false,"quiet":false}}}`,
			task: "sun", wantCmd: "echo hi", wantKind: schedule.Calendar,
		},
		{
			// Pre-0.2 records had no timeout key at all.
			name: "no timeout key",
			json: `{"tasks":{"t":{"cmd":"echo hi","schedule":{"raw":"15m","kind":"interval","interval":900},"cwd":"/tmp","created_at":"2026-07-24T09:00:00+03:00","paused":false,"quiet":false}}}`,
			task: "t", wantCmd: "echo hi", wantKind: schedule.Interval,
		},
		{
			// An interval past int64, which Ruby's integers accepted.
			name: "oversized interval",
			json: `{"tasks":{"huge":{"cmd":"echo hi","schedule":{"raw":"99999999999999999999s","kind":"interval","interval":99999999999999999999},"cwd":"/tmp","created_at":"2026-07-24T09:00:00+03:00","paused":false,"quiet":false}}}`,
			task: "huge", wantCmd: "echo hi", wantKind: schedule.Interval,
		},
		{
			// A command containing every character Go's JSON encoder would
			// escape by default.
			name: "shell metacharacters",
			json: `{"tasks":{"m":{"cmd":"a && b > c < d & e","schedule":{"raw":"15m","kind":"interval","interval":900},"cwd":"/tmp","created_at":"2026-07-24T09:00:00+03:00","paused":false,"quiet":false}}}`,
			task: "m", wantCmd: "a && b > c < d & e", wantKind: schedule.Interval,
		},
		{
			name: "empty registry",
			json: `{"tasks":{}}`,
		},
		{
			// A file with no tasks key at all: 0.1 wrote one on first save.
			name: "no tasks key",
			json: `{}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "tasks.json"), []byte(tc.json), 0o644); err != nil {
				t.Fatal(err)
			}

			s, err := Load(dir)
			if err != nil {
				t.Fatalf("historical store failed to load: %v", err)
			}
			if tc.task == "" {
				return
			}

			task, ok := s.Tasks.Get(tc.task)
			if !ok {
				t.Fatalf("task %q vanished on load", tc.task)
			}
			if task.Cmd != tc.wantCmd {
				t.Errorf("cmd = %q, want %q", task.Cmd, tc.wantCmd)
			}

			sched, err := schedule.FromRecord(task.Schedule)
			if err != nil {
				t.Fatalf("schedule did not migrate: %v", err)
			}
			if sched.Kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", sched.Kind, tc.wantKind)
			}

			// And it must survive a save without becoming unloadable.
			if err := s.Save(); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(dir); err != nil {
				t.Errorf("store became unloadable after a save: %v", err)
			}
		})
	}
}

// A store the Go writes must be readable by the Ruby, for as long as both
// exist. Someone will roll back.
func TestGoWrittenStoreIsReadableByRuby(t *testing.T) {
	ruby, err := exec.LookPath("ruby")
	if err != nil {
		t.Skip("no ruby on PATH")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "lib", "every.rb")); err != nil {
		t.Skip("Ruby tree removed")
	}

	dir := t.TempDir()
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	for name, spec := range map[string][]string{
		"interval": {"15m"},
		"calendar": {"day", "9am,6pm"},
		"weekly":   {"monday,thursday", "10:00"},
	} {
		sched, err := schedule.Parse(spec)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Add(name, &Task{
			// Deliberately awkward: metacharacters and non-ASCII.
			Cmd:      `echo "héllo — wörld" && ls > /tmp/x 2>&1`,
			Schedule: sched.ToRecord(), Cwd: dir,
			CreatedAt: "2026-09-02T10:30:00-04:00", Timeout: 1800,
		}); err != nil {
			t.Fatal(err)
		}
	}

	const script = `
$LOAD_PATH.unshift File.join(ARGV[0], "lib")
ENV["EVERY_HOME"] = ARGV[1]
require "every"
require "json"
store = Every::Store.load
out = store.tasks.map do |name, t|
  s = Every::Schedule.from_h(t["schedule"])
  { "name" => name, "cmd" => t["cmd"], "raw" => s.raw, "kind" => s.kind.to_s,
    "timeout" => t["timeout"] }
end
puts JSON.generate(out)
`
	cmd := exec.Command(ruby, "-e", script, root, dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ruby could not read a Go-written store: %v\n%s", err, out)
	}

	var got []struct {
		Name, Cmd, Raw, Kind string
		Timeout              int
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decoding: %v\n%s", err, out)
	}
	if len(got) != 3 {
		t.Fatalf("ruby saw %d tasks, want 3", len(got))
	}
	for _, g := range got {
		if !strings.Contains(g.Cmd, "héllo — wörld") || !strings.Contains(g.Cmd, "&&") {
			t.Errorf("%s: command mangled in transit: %q", g.Name, g.Cmd)
		}
		if g.Timeout != 1800 {
			t.Errorf("%s: timeout = %d, want 1800", g.Name, g.Timeout)
		}
		if g.Raw == "" || g.Kind == "" {
			t.Errorf("%s: schedule did not survive: %+v", g.Name, g)
		}
	}
}

// A ledger written by the Ruby must be read identically by the Go: same last
// run, same exit code, same duration.
func TestReadsRubyWrittenLedger(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Exactly what Ruby's JSON.generate emits, including the integral float.
	ledger := strings.Join([]string{
		`{"ts":"2026-09-01T09:00:00+03:00","exit":0,"dur":12.0}`,
		`{"ts":"2026-09-01T09:15:00+03:00","exit":7,"dur":0.03}`,
		`{"ts":"2026-09-01T09:30:00+03:00","exit":124,"dur":30.5}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "runs", "t.jsonl"), []byte(ledger), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.LastRun("t")
	if err != nil {
		t.Fatal(err)
	}
	if run == nil {
		t.Fatal("no run found in a Ruby-written ledger")
	}
	if run.Exit != 124 {
		t.Errorf("exit = %d, want 124", run.Exit)
	}
	if got := run.Dur.String(); got != "30.5" {
		t.Errorf("dur = %s, want 30.5", got)
	}
	// And re-emitting it must produce the same bytes Ruby would.
	line, err := json.Marshal(*run)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"ts":"2026-09-01T09:30:00+03:00","exit":124,"dur":30.5}`
	if string(line) != want {
		t.Errorf("re-emitted %s, want %s", line, want)
	}
}
