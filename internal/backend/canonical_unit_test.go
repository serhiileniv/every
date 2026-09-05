package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A unit written to disk must compare equal to a freshly rendered one when
// nothing about the task has changed.
//
// It did not, for two compounding reasons, and the migration in
// internal/migrate consequently rewrote and re-registered every Windows task on
// every pass. Because an interval trigger's StartBoundary is now+interval, each
// re-registration pushed the next run further out; writing the store more often
// than the interval starved the task completely while `every list` still said
// ok. Both halves are asserted here.
func TestCanonicalUnitSurvivesRoundTripAndClock(t *testing.T) {
	w := goldenTaskScheduler(t)
	t.Setenv("COMSPEC", "cmd.exe")

	scheds := loadSchedules(t)
	sched, ok := scheds["15m"]
	if !ok {
		t.Fatal("no 15m schedule fixture")
	}

	xml, err := w.Render("probe", sched)
	if err != nil {
		t.Fatal(err)
	}

	// Exactly what Write puts on disk: UTF-16LE with a BOM.
	path := filepath.Join(t.TempDir(), "probe.xml")
	if err := writeUTF16(path, xml); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Half one: the encoding. The bytes on disk are not the string Render
	// returns, so a raw comparison can never succeed.
	if string(raw) == xml {
		t.Fatal("precondition failed: the on-disk unit is supposed to be UTF-16")
	}
	if got := w.CanonicalUnit(string(raw)); got != w.CanonicalUnit(xml) {
		t.Errorf("a unit does not survive its own encoding round trip:\n--- disk ---\n%s\n--- render ---\n%s",
			got, w.CanonicalUnit(xml))
	}

	// Half two: the clock. A render 37 seconds later moves StartBoundary and
	// nothing else, and must still count as the same unit.
	base := w.Now()
	w.Now = func() time.Time { return base.Add(37 * time.Second) }
	later, err := w.Render("probe", sched)
	if err != nil {
		t.Fatal(err)
	}
	if later == xml {
		t.Fatal("precondition failed: StartBoundary is supposed to be clock-derived")
	}
	if w.CanonicalUnit(string(raw)) != w.CanonicalUnit(later) {
		t.Error("a later render of an unchanged task compares as drift; " +
			"every migration pass would re-register it and reset its interval phase")
	}
}

// Canonicalizing must not blind the comparison to changes that matter. Only
// StartBoundary is dropped; everything else still has to register as drift.
func TestCanonicalUnitStillNoticesRealDrift(t *testing.T) {
	w := goldenTaskScheduler(t)
	t.Setenv("COMSPEC", "cmd.exe")

	scheds := loadSchedules(t)
	fifteen, hourly := scheds["15m"], scheds["hourly"]
	if fifteen == nil || hourly == nil {
		t.Fatal("missing schedule fixtures")
	}

	a, err := w.Render("probe", fifteen)
	if err != nil {
		t.Fatal(err)
	}

	// A changed interval.
	b, err := w.Render("probe", hourly)
	if err != nil {
		t.Fatal(err)
	}
	if w.CanonicalUnit(a) == w.CanonicalUnit(b) {
		t.Error("a changed interval is not detected as drift")
	}

	// A changed command, which is what the 0.4 repair existed to fix.
	tampered := strings.Replace(a, "<Command>", "<Command>ruby.exe ", 1)
	if tampered == a {
		t.Fatal("precondition failed: no Command element to tamper with")
	}
	if w.CanonicalUnit(a) == w.CanonicalUnit(tampered) {
		t.Error("a changed command is not detected as drift")
	}
}
