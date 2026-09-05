package backend

import (
	"strings"
	"testing"

	"github.com/serhiileniv/every/internal/schedule"
)

// Every real backend owns its retire ordering; see the Retirer doc comment.
var (
	_ Retirer = (*Launchd)(nil)
	_ Retirer = (*Systemd)(nil)
	_ Retirer = (*TaskScheduler)(nil)
)

// recorder is a Backend with no Retire of its own, so package Retire falls
// back to the rm ordering.
type recorder struct {
	Backend
	calls []string
}

func (r *recorder) Disable(string) error     { r.calls = append(r.calls, "disable"); return nil }
func (r *recorder) DeleteUnits(string) error { r.calls = append(r.calls, "delete"); return nil }

func TestRetireFallsBackToRmOrder(t *testing.T) {
	r := &recorder{}
	if err := Retire(r, "x"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.calls, ","); got != "disable,delete" {
		t.Errorf("calls = %s, want disable,delete", got)
	}
}

// A once schedule must produce a real trigger on every backend: the entries
// loop it bypasses would otherwise render an empty one that never fires.
func TestOnceRendersATrigger(t *testing.T) {
	sched, err := schedule.ParseAt([]string{"once", "2026-09-05", "9am"}, goldenNow(t))
	if err != nil {
		t.Fatal(err)
	}

	plist := NewLaunchd(goldenCfg()).PlistXML("o", sched)
	for _, want := range []string{"<key>Month</key><integer>9</integer>", "<key>Day</key><integer>5</integer>",
		"<key>Hour</key><integer>9</integer>", "<key>Minute</key><integer>0</integer>"} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist lacks %s:\n%s", want, plist)
		}
	}
	if strings.Contains(plist, "<array>\n\n") {
		t.Error("plist has an empty calendar array")
	}

	if lines := CalendarLines(sched); len(lines) != 1 || lines[0] != "2026-09-05 09:00:00" {
		t.Errorf("CalendarLines = %v", lines)
	}

	t.Setenv("COMSPEC", "cmd.exe")
	xml, err := goldenTaskScheduler(t).TaskXML("o", sched)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(xml, "<StartBoundary>2026-09-05T09:00:00</StartBoundary>") {
		t.Errorf("task xml lacks the boundary:\n%s", xml)
	}
	if strings.Contains(xml, "<Repetition>") || strings.Contains(xml, "<CalendarTrigger>") {
		t.Errorf("once task is not a bare TimeTrigger:\n%s", xml)
	}
}

func TestMonthlyRendersDayOfMonth(t *testing.T) {
	sched, err := schedule.ParseAt([]string{"monthly", "1,15", "9am"}, goldenNow(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(CalendarLines(sched), "|"); got != "*-*-01 09:00:00|*-*-15 09:00:00" {
		t.Errorf("CalendarLines = %s", got)
	}
	plist := NewLaunchd(goldenCfg()).PlistXML("m", sched)
	if strings.Count(plist, "<key>Day</key>") != 2 || strings.Contains(plist, "<key>Weekday</key>") {
		t.Errorf("plist:\n%s", plist)
	}
	t.Setenv("COMSPEC", "cmd.exe")
	xml, err := goldenTaskScheduler(t).TaskXML("m", sched)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(xml, "<ScheduleByMonth>") != 2 || strings.Count(xml, "<December/>") != 2 {
		t.Errorf("task xml:\n%s", xml)
	}
}
