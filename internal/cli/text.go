// Package cli is the command-line interface: argument dispatch, rendering, and
// the exit-code contract.
//
// Ported from lib/every/cli.rb. Everything a user types and everything every
// says back is frozen -- testdata/golden/cli replays it, so a reworded message
// is a build failure rather than a bug report.
package cli

import "fmt"

// Version is stamped at build time by the release pipeline
// (-X ...internal/cli.Version=X.Y.Z), so `every version` reports the tag that
// produced the binary rather than a constant somebody forgot to bump. It is a
// var, not a const, for exactly that reason -- the linker cannot rewrite a
// const. The fallback value is what a plain `go build` reports.
var Version = "0.4.0"

// Tagline and Homepage identify the tool in `version` and `help`.
const (
	Tagline  = "humane task scheduler for macOS (launchd), Linux (systemd), and Windows (Task Scheduler)"
	Homepage = "https://github.com/serhiileniv/every"
)

// maxName bounds a task name so the generated unit filename cannot exceed the
// 255-byte limit, which used to be an ENAMETOOLONG crash.
const maxName = 100

func versionText() string {
	return fmt.Sprintf("every %s\n%s\n%s\n", Version, Tagline, Homepage)
}

func helpText(dataDir string) string {
	return fmt.Sprintf(`every %s — %s
%s

schedule anything on your computer, humanely

add a task:
  every 15m -- ~/bin/sync-notes.sh
  every hourly -- brew update
  every day 9am,6pm -- ruby ~/bin/report.rb
  every weekdays 9:30 -- ~/bin/standup-prep.sh
  every monday,thursday 10:00 --name reports -- ~/bin/weekly.sh

  Flags: --name NAME, --quiet (no failure notification),
         --timeout 30m (kill a run that overruns, so it can't block
         the next one).
  The command runs through your platform shell (PATH works), in the
  directory where you added it. Missed calendar runs fire when the
  scheduler becomes available. Failed runs notify when supported unless
  --quiet.

manage:
  every list                what's scheduled, last/next run, ok/FAIL
  every log <name> [-n N]   output of recent runs
  every run <name>          run it right now (prints output, logs too)
  every pause <name>        stop scheduling (keeps the task)
  every resume <name>       start again
  every rm <name>           remove task (logs are kept)
  every doctor              explain why something isn't running

data:  %s
more:  %s
`, Version, Tagline, Homepage, dataDir, Homepage)
}
