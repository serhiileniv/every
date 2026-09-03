package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/serhiileniv/every/internal/paths"
	"github.com/serhiileniv/every/internal/schedule"
)

// The environment scripts/golden.rb pinned when it generated the fixtures.
const (
	goldenData     = "/every-golden/home"
	goldenLauncher = "__LAUNCHER__"
	// The interpreter argv element the port deletes outright. Dropping it is
	// the one sanctioned difference between the fixtures and the Go output.
	goldenRuby = "__RUBY__"
)

func goldenCfg() Config {
	return Config{
		Dirs: paths.Dirs{
			Data:   goldenData,
			Logs:   goldenData + "/logs",
			Runs:   goldenData + "/runs",
			Config: goldenData + "/config",
			Agents: goldenData + "/agents",
		},
		Launcher: goldenLauncher,
	}
}

func goldenRoot(t *testing.T, parts ...string) string {
	t.Helper()
	root := filepath.Join("..", "..", "testdata", "golden")
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("fixtures missing (regenerate with scripts/fixtures.sh): %v", err)
	}
	return filepath.Join(append([]string{root}, parts...)...)
}

// loadSchedules rebuilds every fixture schedule from its stored record, keyed
// by slug. FromRecord rather than Parse, because the legacy daily/weekly shapes
// have no parseable token form.
func loadSchedules(t *testing.T) map[string]*schedule.Schedule {
	t.Helper()
	files, err := filepath.Glob(goldenRoot(t, "schedule", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no schedule fixtures")
	}

	out := map[string]*schedule.Schedule{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			ToH schedule.Record `json:"to_h"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		s, err := schedule.FromRecord(doc.ToH)
		if err != nil {
			t.Fatal(err)
		}
		out[strings.TrimSuffix(filepath.Base(f), ".json")] = s
	}
	return out
}

// dropRubyArgv removes the interpreter element from a fixture so it can be
// compared against output that no longer has one.
//
// This is the ONLY transformation applied to a golden. Everything else --
// whitespace, ordering, escaping, the trailing newline -- must match exactly,
// because a plist or unit that differs cosmetically still makes an upgrade
// rewrite every user's agents for no reason.
func dropRubyArgv(golden string) string {
	var kept []string
	for _, line := range strings.Split(golden, "\n") {
		trimmed := strings.TrimSpace(line)
		// The launchd form: a whole <string> element that is just the token.
		if trimmed == "<string>"+goldenRuby+"</string>" {
			continue
		}
		// The systemd form: the token opens the ExecStart value.
		if strings.HasPrefix(trimmed, "ExecStart="+goldenRuby+" ") {
			line = strings.Replace(line, "ExecStart="+goldenRuby+" ", "ExecStart=", 1)
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func TestLaunchdPlistsMatchGolden(t *testing.T) {
	l := NewLaunchd(goldenCfg())
	for slug, s := range loadSchedules(t) {
		t.Run(slug, func(t *testing.T) {
			raw, err := os.ReadFile(goldenRoot(t, "launchd", slug+".plist"))
			if err != nil {
				t.Fatal(err)
			}
			want := dropRubyArgv(string(raw))
			got := l.PlistXML(slug, s)
			if got != want {
				t.Errorf("plist mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}
		})
	}
}

// launchctl list rows are "PID<TAB>Status<TAB>Label"; only every's own labels
// are ours, and the prefix is stripped from the first occurrence only.
func TestParseLabels(t *testing.T) {
	out := "-\t0\tcom.apple.something\n" +
		"123\t0\tcom.every.backup\n" +
		"-\t0\tcom.every.sync\n" +
		"-\t0\tcom.google.keystone.agent\n"
	got := parseLabels(out)
	want := []string{"backup", "sync"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("parseLabels = %v, want %v", got, want)
	}
}

func TestParseLabelsEmpty(t *testing.T) {
	if got := parseLabels(""); len(got) != 0 {
		t.Errorf("parseLabels(\"\") = %v, want empty", got)
	}
}

// The data dir must be pinned in every plist, or an EVERY_HOME install's
// scheduled runs recompute the default dir and never find the task.
func TestPlistAlwaysPinsDataDir(t *testing.T) {
	l := NewLaunchd(goldenCfg())
	for slug, s := range loadSchedules(t) {
		xml := l.PlistXML(slug, s)
		if !strings.Contains(xml, "<key>EVERY_HOME</key><string>"+goldenData+"</string>") {
			t.Errorf("%s: plist does not pin EVERY_HOME", slug)
		}
	}
}

// No backend may turn an unsafe name into a path, whatever the caller does.
//
// The CLI sanitizes names on the way in, but that is one door. The store is a
// plain JSON file, and a task written by a hand-edit -- or by some future
// version -- reaches Write unsanitized. This is the gate that holds when the
// other one is bypassed.
func TestBackendsRefuseUnsafeNames(t *testing.T) {
	unsafe := []string{
		"../evil",
		"../../../../tmp/EVIL",
		"a/b",
		`a\b`,
		"",
		".",
		"..",
		"has space",
	}

	sched := loadSchedules(t)["15m"]
	dir := t.TempDir()
	cfg := goldenCfg()
	cfg.Dirs.Agents = dir
	cfg.Dirs.Config = dir
	cfg.Dirs.Data = dir
	cfg.Dirs.Logs = filepath.Join(dir, "logs")

	backends := map[string]Backend{
		"launchd": NewLaunchd(cfg),
		"systemd": NewSystemd(cfg),
		"taskschd": func() Backend {
			w := NewTaskScheduler(cfg)
			w.User = func() (string, error) { return "u", nil }
			return w
		}(),
	}

	for backendName, b := range backends {
		for _, name := range unsafe {
			t.Run(backendName+"/"+name, func(t *testing.T) {
				if err := b.Write(name, sched); err == nil {
					t.Errorf("%s.Write(%q) was allowed", backendName, name)
				}
			})
		}
	}

	// Nothing may have escaped the directory under test.
	escaped, err := filepath.Glob(filepath.Join(dir, "..", "*EVIL*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(escaped) > 0 {
		t.Errorf("files written outside the target directory: %v", escaped)
	}
}
