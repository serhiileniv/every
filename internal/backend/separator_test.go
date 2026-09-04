package backend

import (
	"strings"
	"testing"
)

// The plist is read by launchd, which is macOS-only. The separator in it must
// be "/" no matter which platform built the string -- otherwise the generator
// is not actually testable off macOS, which is the whole point of keeping it
// free of build tags.
func TestPlistPathsAreAlwaysPOSIX(t *testing.T) {
	l := NewLaunchd(goldenCfg())
	for slug, sched := range loadSchedules(t) {
		xml := l.PlistXML(slug, sched)
		if strings.Contains(xml, `\`) {
			t.Fatalf("%s: plist contains a backslash separator:\n%s", slug, xml)
		}
	}
}
