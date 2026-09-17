package cli

import (
	"fmt"
	"sort"
	"strings"
)

// Per-command help.
//
// `every help log` and `every log --help` both land here. The full help stays
// what `every help` alone prints: a page listing sixteen commands answers
// "what can this do", and a page about one command answers "how do I use this
// one", and neither substitutes for the other.
//
// Kept beside the command list rather than generated from it: a synopsis with
// real flags and a worked example is the point, and there is nothing in the
// dispatcher to generate that from. TestEveryCommandHasAHelpTopic is what keeps
// the two from drifting.

// helpTopics is keyed by CANONICAL command name. Aliases resolve through
// suggestable before lookup, so `every help ls` finds list's page.
var helpTopics = map[string]string{
	"list": `every list [--failing] [--json]          (alias: ls)

  What is scheduled, when it last ran, whether it worked, and when it runs
  next. Times are relative -- 2h ago, in 18h. The exact instant is in
  every inspect and in --json.

  --failing   only the tasks needing attention: failed, unscheduled, invalid
              or missed. Not paused, and not merely never-run. Exits 0
              whether or not any matched.
  --json      one object per task on stdout

  every list
  every list --failing
  every list --json | jq -r '.[] | select(.status | test("FAIL")) | .name'`,

	"log": `every log <name> [-n N] [-f] [--json] [--with-output]

  What a task printed. Reads across a log rotation, so the moment just after
  one does not look like history that was lost.

  -n N            how many lines (default 40)
  -f, --follow    keep printing as the task writes, like tail -f. Waits if
                  the task has not run yet. Ctrl-C ends it, exit 0.
                  Cannot be combined with --json.
  --json          run history from the ledger
  --with-output   include captured output in --json (omitted by default,
                  which keeps the response bounded)

  every log backup
  every log backup -n 200
  every log backup -f`,

	"run": `every run <name> [--dry-run] [--json]

  Run a task now, in your terminal, to see it work before trusting the
  schedule. The run is logged like any other. Exits with the command's own
  exit code.

  --dry-run   print the shell, directory, timeout and exact command that
              would be used, and execute nothing

  every run backup
  every run backup --dry-run`,

	"pause": `every pause <name> [--json]

  Stop scheduling a task without deleting it. The task, its history and its
  logs stay; only the scheduler stops firing it. every resume undoes it.

  every pause backup`,

	"resume": `every resume <name> [--json]

  Start scheduling a paused task again. Also the fix for a task showing
  "unscheduled" in every list -- one the scheduler has lost.

  every resume backup`,

	"rm": `every rm <name> [--json]                 (alias: remove)

  Remove a task and its scheduler unit. Logs and run history are KEPT, so
  every log <name> still answers afterwards.

  every rm backup`,

	"doctor": `every doctor [--json]

  Why isn't it running? Checks the whole chain -- scheduler reachable, data
  dir writable, unit present, task loaded, command resolvable in the login
  shell -- and names the command that fixes anything broken.

  The command check is the one that catches the classic trap: a PATH line in
  ~/.zshrc works when you type the command and fails under the scheduler,
  which reads ~/.zprofile.

  every doctor`,

	"inspect": `every inspect <name> [--json]            (alias: show)

  Everything about one task: schedule, command, directory, timeout, on-fail,
  whether the scheduler actually has it, and when it runs next. This is where
  exact times live -- every list rounds them off for scanning.

  every inspect backup
  every inspect backup --json | jq -r .next`,

	"exists": `every exists <name> [--json]

  Exit 0 if the task exists, 66 if it does not. Prints nothing. For scripts
  that need an idempotent add.

  every exists backup || every day 3am --name backup -- ~/bin/backup.sh`,

	"set": `every set <when> --name <name> [flags] -- <command>

  Add a task, or update one that already exists, in one step. Unlike rm
  followed by a re-add there is no window in which the task does not exist:
  the store lock is held across the whole operation and the previous unit is
  restored if the scheduler refuses. Run history and creation time survive.

  --name is required -- it is what identifies the task to update.
  Takes the same flags as adding: --quiet, --timeout, --on-fail.

  every set day 9am --name backup -- ~/bin/backup.sh`,

	"schema": `every schema [command] [--json]

  The JSON shape a command emits, generated from the types themselves rather
  than written by hand, so it cannot go stale.

  With no argument, every shape at once. With one, just that command's.

  every schema
  every schema list
  every schema error`,

	"version": `every version [--json]                   (also: -V)

  Version, tagline and homepage.

  every version
  every version --json | jq -r .version`,

	"help": `every help [command]                     (also: -h)

  With no argument, everything every can do. With a command, that command's
  page. Also reachable as a flag: every log --help.

  every help
  every help log
  every log --help`,

	"schedules": `Schedule forms

  90s  15m  2h                fixed interval
  hourly                      every hour
  day 9am     day 17:30       daily at that time
  day 9am,6pm                 daily, several times
  weekdays 9:30               Mon-Fri
  weekends 11am               Sat+Sun
  monday 10:00                weekly
  monday,thursday 6pm         weekly, several days
  monthly 1st 9am             a day of the month (29-31 skip short months)
  monthly 1,15 18:00          several days of the month
  once 15:30                  one time only: today, or tomorrow if passed
  once tomorrow 9am           one time only: a day
  once friday 5pm             one time only: a weekday
  once 2026-12-24 18:00       one time only: a date
  once 45m                    one time only: a delay from now (>= 1 minute)

  A once task fires and then removes itself, keeping its logs and history.

  every 15m -- ~/bin/sync.sh
  every weekdays 9:30 -- ~/bin/standup.sh
  every once tomorrow 9am -- ~/bin/remind.sh`,
}

// helpTopicFor resolves a topic name, following aliases, and reports whether
// there is a page for it.
func helpTopicFor(name string) (string, bool) {
	if canonical, ok := suggestable[name]; ok {
		name = canonical
	}
	page, ok := helpTopics[name]
	return page, ok
}

// topicHelpText is one command's page, with the footer that tells the reader
// where the rest is.
func topicHelpText(name string) string {
	page, ok := helpTopicFor(name)
	if !ok {
		return ""
	}
	return page + "\n\nall commands:  every help\n"
}

// helpTopicNames is every page that can be asked for, sorted, for the "no such
// topic" message and for the completions test.
func helpTopicNames() []string {
	out := make([]string, 0, len(helpTopics))
	for n := range helpTopics {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// unknownTopic explains that there is no page for what was asked, and offers
// the nearest command when there is an obvious one.
//
// A usage error rather than silently printing the full help: `every help lst`
// answered with the whole manual looks like it worked, and the reader is left
// hunting for a "lst" section that was never there.
func unknownTopic(name string) error {
	msg := fmt.Sprintf("no help for %s", rubyInspect(name))
	if guess := suggestCommand(name); guess != "" {
		msg += fmt.Sprintf("\n  did you mean:  every help %s", guess)
	}
	msg += "\n  topics:        " + strings.Join(helpTopicNames(), ", ")
	return coded(CodeUsage, "", "%s", msg)
}
