package backend

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/serhiileniv/every/internal/schedule"
)

// Every unit type, for every schedule the grammar accepts, rendered by BOTH
// implementations and compared byte for byte.
//
// The golden fixtures pin sixteen schedules captured once. This renders
// hundreds, live, on both sides -- so it covers the combinations nobody chose
// to freeze: every weekday against every time format, multi-day sets crossed
// with multi-time lists, the boundaries of the interval floor, midnight and
// noon, and the entry orderings those produce.
//
// A unit that differs is not a cosmetic problem. It is what a scheduler reads,
// and a byte that moves is a task that fires at a different time or not at all.
func TestAllUnitsMatchRubyAcrossTheGrammar(t *testing.T) {
	ruby, root := rubyForUnits(t)

	// Before the subprocess: it inherits this environment, and COMSPEC reaches
	// the generated <Command>. Setting it afterwards let the Ruby render an
	// absolute C:\Windows\system32\cmd.exe while the Go side rendered the
	// bare name -- 292 mismatches that were purely the harness's ordering.
	t.Setenv("COMSPEC", "cmd.exe")

	specs := unitMatrix()
	t.Logf("matrix: %d schedules x 4 unit renderings = %d comparisons",
		len(specs), len(specs)*4)

	env, rendered := rubyRenderUnits(t, ruby, root, specs)

	launchd := NewLaunchd(dualCfgFor(env, goldenLauncher))
	systemd := NewSystemd(dualCfgFor(env, goldenLauncher))

	// The Task Scheduler action reproduces the shim branch, whose launcher
	// carries a .cmd suffix; see scripts/golden.rb for why.
	winCfg := dualCfgFor(env, goldenLauncher+".cmd")
	win := NewTaskScheduler(winCfg)
	win.Now = func() time.Time { return dualClock(t) }
	win.User = func() (string, error) { return `GOLDEN\goldenuser`, nil }

	var mismatches, compared int
	report := func(kind, slug, got, want string) {
		compared++
		if got == want {
			return
		}
		mismatches++
		if mismatches <= 8 {
			t.Errorf("%s mismatch for %q:\n--- go ---\n%s\n--- ruby ---\n%s",
				kind, slug, got, want)
		}
	}

	for _, spec := range specs {
		want, ok := rendered[spec.Slug]
		if !ok {
			t.Fatalf("ruby returned nothing for %q", spec.Slug)
		}
		sched, err := schedule.Parse(spec.Tokens)
		if err != nil {
			t.Fatalf("Parse(%q): %v", spec.Tokens, err)
		}

		report("plist", spec.Slug, launchd.PlistXML(spec.Slug, sched), dropRubyArgv(want.Plist))
		report("service", spec.Slug, systemd.ServiceUnit(spec.Slug), dropRubyArgv(want.Service))
		report("timer", spec.Slug, systemd.TimerUnit(spec.Slug, sched), want.Timer)

		if want.TaskXML != "" {
			got, err := win.TaskXML(spec.Slug, sched)
			if err != nil {
				t.Fatalf("TaskXML(%q): %v", spec.Slug, err)
			}
			report("task XML", spec.Slug, got, want.TaskXML)
		}
	}

	if mismatches > 8 {
		t.Errorf("... and %d more mismatches", mismatches-8)
	}
	t.Logf("compared %d unit renderings, %d mismatched", compared, mismatches)
}

type unitSpec struct {
	Slug   string   `json:"slug"`
	Tokens []string `json:"tokens"`
}

// rubyEnv is what the Ruby resolved for itself, so the Go side can be
// configured identically.
//
// EVERY_HOME goes through File.expand_path, which on Windows prefixes the
// current drive: "/every-golden/home" becomes "D:/every-golden/home". Pinning
// the literal on the Go side therefore compared two different data dirs and
// reported 882 mismatches that were entirely the harness's fault. Asking the
// other implementation what it decided is both simpler and more honest than
// reimplementing its rule in the test.
type rubyEnv struct {
	DataDir string `json:"data_dir"`
}

type rubyUnits struct {
	Plist   string `json:"plist"`
	Service string `json:"service"`
	Timer   string `json:"timer"`
	TaskXML string `json:"task_xml"`
}

// unitMatrix enumerates the schedules whose unit output can differ: every day
// word, every weekday, several multi-day sets, several time formats and
// multi-time lists, and intervals around the boundaries that change how the
// trigger is written.
func unitMatrix() []unitSpec {
	var specs []unitSpec
	add := func(tokens ...string) {
		// The slug is also the task NAME, so it has to satisfy internal/naming.
		slug := strings.ToLower(strings.Join(tokens, "-"))
		slug = strings.NewReplacer(":", "", ",", "-", " ", "-").Replace(slug)
		specs = append(specs, unitSpec{Slug: fmt.Sprintf("m%03d-%s", len(specs), slug), Tokens: tokens})
	}

	for _, iv := range []string{
		"10s", "11s", "59s", "60s", "61s", "90s", "1m", "15m", "59m", "60m",
		"1h", "2h", "24h", "3600s", "86400s", "hourly",
	} {
		add(iv)
	}

	days := []string{
		"day", "daily", "weekdays", "weekends",
		"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday",
		"monday,thursday", "sunday,saturday", "monday,wednesday,friday",
		"monday,tuesday,wednesday,thursday,friday,saturday,sunday",
	}
	times := []string{
		"00:00", "9am", "12am", "12pm", "9", "09:05", "17:30", "23:59",
		"9am,6pm", "0:00,12:00,23:59",
	}
	for _, d := range days {
		for _, tm := range times {
			add(d, tm)
		}
	}

	// Every hour of the day, and the minutes at the edges of two-digit
	// formatting. This is where a %d that should be %02d hides: it only shows
	// below ten, and only in some of the four renderings.
	for h := 0; h < 24; h++ {
		add("day", fmt.Sprintf("%d:00", h))
		add("day", fmt.Sprintf("%02d:00", h))
		add("monday", fmt.Sprintf("%d:05", h))
	}
	for _, m := range []string{"00", "01", "05", "09", "10", "30", "59"} {
		add("day", "0:"+m)
		add("day", "23:"+m)
		add("weekends", "7:"+m)
	}

	// am/pm across the whole twelve-hour range, both cases, since the hour is
	// rewritten before it reaches a unit.
	for h := 1; h <= 12; h++ {
		add("day", fmt.Sprintf("%dam", h))
		add("day", fmt.Sprintf("%dpm", h))
		add("sunday", fmt.Sprintf("%dAM", h))
	}

	return specs
}

// dualCfgFor mirrors the data dir the Ruby resolved. Paths inside a unit are
// POSIX because launchd and systemd are POSIX, whatever host is running this.
func dualCfgFor(env rubyEnv, launcher string) Config {
	cfg := goldenCfg()
	cfg.Dirs.Data = env.DataDir
	cfg.Dirs.Logs = env.DataDir + "/logs"
	cfg.Dirs.Runs = env.DataDir + "/runs"
	cfg.Launcher = launcher
	return cfg
}

func dualClock(t *testing.T) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("zoneinfo unavailable: %v", err)
	}
	now, err := time.ParseInLocation(time.RFC3339, "2026-09-02T10:30:00-04:00", loc)
	if err != nil {
		t.Fatal(err)
	}
	return now
}

func rubyForUnits(t *testing.T) (ruby, root string) {
	t.Helper()
	ruby, err := exec.LookPath("ruby")
	if err != nil {
		t.Skip("no ruby on PATH; the Ruby tree is what this port replaces")
	}
	root, err = filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "lib", "every.rb")); err != nil {
		t.Skip("Ruby tree removed; dual unit comparison no longer applies")
	}
	return ruby, root
}

// rubyRenderUnits renders every unit type for the whole matrix in one
// interpreter, under the same pinned environment the fixtures use: a fixed
// data dir, user, clock and zone, so nothing machine-specific leaks in.
func rubyRenderUnits(t *testing.T, ruby, root string, specs []unitSpec) (rubyEnv, map[string]rubyUnits) {
	t.Helper()

	payload, err := json.Marshal(specs)
	if err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(t.TempDir(), "specs.json")
	if err := os.WriteFile(in, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	const script = `
ENV["TZ"] = "America/New_York"
ENV["EVERY_HOME"] = "/every-golden/home"
ENV["USERNAME"]   = "goldenuser"
ENV["USERDOMAIN"] = "GOLDEN"
# Pinned here, not just on the Go side: COMSPEC reaches the generated
# <Command>, and a real Windows box has it set to an absolute path.
ENV["COMSPEC"] = "cmd.exe"
$LOAD_PATH.unshift File.join(ARGV[0], "lib")
require "every"
require "json"

FIXED = Time.new(2026, 9, 2, 10, 30, 0)
class << Time
  def now; FIXED; end
end
module RbConfig; def self.ruby; "__RUBY__"; end; end
module Every
  module Runtime; def self.bin; "__LAUNCHER__"; end; end
end

specs = JSON.parse(File.read(ARGV[1]))
out = {}
specs.each do |spec|
  s = Every::Schedule.parse(spec["tokens"])
  out[spec["slug"]] = {
    "plist"   => Every::Launchd.plist_xml(spec["slug"], s),
    "service" => Every::Systemd.service_unit(spec["slug"]),
    "timer"   => Every::Systemd.timer_unit(spec["slug"], s),
  }
end

# The Windows action has two branches and only the shim one survives the port,
# so both conditions are stubbed to reach it. See scripts/golden.rb.
module Every
  def self.windows?; true; end
  module Runtime; def self.bin; "__LAUNCHER__.cmd"; end; end
end
specs.each do |spec|
  s = Every::Schedule.parse(spec["tokens"])
  next if s.kind == :interval && s.interval < 60
  out[spec["slug"]]["task_xml"] = Every::WindowsTaskScheduler.task_xml(spec["slug"], s)
end

puts JSON.generate({ "data_dir" => Every::DATA_DIR, "units" => out })
`
	cmd := exec.Command(ruby, "-e", script, root, in)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("ruby: %v\n%s", err, ee.Stderr)
		}
		t.Fatalf("ruby: %v", err)
	}

	var payloadOut struct {
		DataDir string               `json:"data_dir"`
		Units   map[string]rubyUnits `json:"units"`
	}
	if err := json.Unmarshal(out, &payloadOut); err != nil {
		t.Fatalf("decoding ruby output: %v", err)
	}
	if len(payloadOut.Units) != len(specs) {
		t.Fatalf("ruby returned %d results for %d specs", len(payloadOut.Units), len(specs))
	}
	if payloadOut.DataDir == "" {
		t.Fatal("ruby did not report its data dir")
	}
	return rubyEnv{DataDir: payloadOut.DataDir}, payloadOut.Units
}

// The Task Scheduler file is UTF-16LE with a BOM, and that encoding is what
// schtasks actually reads -- 0.3.0 shipped it as plain UTF-8 and could not
// register a single task.
//
// The dual test above compares the XML as text. This closes the remaining
// step: for every schedule in the same matrix, the encoded file must carry the
// BOM and decode back to exactly the text that was compared. Checked as a
// property rather than against a Ruby-produced blob, because the encoding is a
// pure function of the text and 295 base64 round-trips through an interpreter
// would be noise rather than evidence.
func TestEveryTaskXMLEncodesAndDecodesLosslessly(t *testing.T) {
	cfg := goldenCfg()
	cfg.Launcher = goldenLauncher + ".cmd"
	win := NewTaskScheduler(cfg)
	win.Now = func() time.Time { return dualClock(t) }
	win.User = func() (string, error) { return `GOLDEN\goldenuser`, nil }
	t.Setenv("COMSPEC", "cmd.exe")

	dir := t.TempDir()
	checked := 0
	for _, spec := range unitMatrix() {
		sched, err := schedule.Parse(spec.Tokens)
		if err != nil {
			t.Fatalf("Parse(%q): %v", spec.Tokens, err)
		}
		if err := win.ValidateSchedule(sched); err != nil {
			continue // sub-minute: refused on Windows, never written
		}

		xml, err := win.TaskXML(spec.Slug, sched)
		if err != nil {
			t.Fatalf("TaskXML(%q): %v", spec.Slug, err)
		}
		path := filepath.Join(dir, spec.Slug+".xml")
		if err := writeUTF16(path, xml); err != nil {
			t.Fatalf("writeUTF16(%q): %v", spec.Slug, err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		if len(raw) < 2 || raw[0] != 0xFF || raw[1] != 0xFE {
			t.Fatalf("%s: missing the UTF-16LE BOM", spec.Slug)
		}
		if len(raw)%2 != 0 {
			t.Fatalf("%s: odd byte count, so it is not whole UTF-16 code units", spec.Slug)
		}
		if decoded := decodeUTF16(raw); decoded != xml {
			t.Fatalf("%s: the encoded file does not decode back to its source", spec.Slug)
		}
		checked++
	}
	t.Logf("encoded and decoded %d task XML files losslessly", checked)
}

func decodeUTF16(raw []byte) string {
	units := make([]uint16, 0, (len(raw)-2)/2)
	for i := 2; i+1 < len(raw); i += 2 {
		units = append(units, binary.LittleEndian.Uint16(raw[i:i+2]))
	}
	return string(utf16.Decode(units))
}
