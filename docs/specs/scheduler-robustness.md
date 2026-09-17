# Scheduler robustness: three silent failures

## Goal

Close the three ways a task can stop running (or run in the wrong place) while
`every list` and `every doctor` both still say everything is fine. Visibility is
the product; a silent stop is the one bug class that voids it.

## Non-goals

- The staleness watchdog (ROADMAP, "Next"). Separate feature, separate spec.
- Reworking the migration's purpose. Re-pointing units at a moved install is
  correct and stays.
- Findings not covered by part one. The first two are designed in **Part two**
  below; the third shipped with part two as a fix that needed no design:
  - `list` prints `missed` for a one-shot that systemd (`Persistent=true`) and
    Task Scheduler (`StartWhenAvailable`) will still fire late.
  - A missed launchd one-shot stays loaded; the plist has no Year, so it fires
    on the same date next year.
  - `every log` never read `.log.old`, so a rotation hid recent history. Fixed:
    `internal/cli/logfiles.go` reads across the boundary for both the text and
    `--with-output` forms, reaching into the rotated generation only for what
    the live log cannot supply.

## Current state

**1. Any binary re-points every live unit at itself.** `migrate.Run` rewrites
and re-registers every unit whenever the rendered unit differs, and the unit
embeds `cfg.Launcher` — which is `os.Args[0]`, expanded but not resolved
(`cmd/every/launcher.go:16`). Migration runs from `list`, `run`, `doctor`,
`inspect` and `set` (`internal/cli/cli.go:135`).

So `go run ./cmd/every list` re-points every task at
`/var/folders/.../go-build*/b001/exe/every`, deleted the moment it exits.
Verified. Nothing reports it afterwards: `ResourceExists` is true, the agent is
still loaded, and the last run's exit code is still 0 — only its timestamp stops
advancing. `migrate.Report` says "repaired N scheduled tasks for the 0.4
runtime", which does not describe what happened and never names the new path.

**2. A command that backgrounds anything never finishes.** `capture` reads the
merged pipe to EOF and only then calls `Wait` (`internal/runner/runner.go:190`).
A grandchild that inherits the pipe holds it open after the direct child exits.
Verified: `sleep 20 & echo started` blocked past 5 s. The scheduler will not
start a second copy of a running task, so the task is dead until the grandchild
is, and `list` keeps reporting `ok`. With `--timeout` the group is killed and the
run ends at exit 124 — but the README frames timeouts as protection against "a
task that hangs", and this command exited instantly.

**3. A deleted working directory falls back to `$HOME` in silence.**
`Runner.workdir` returns a note when the directory is unreadable, and an empty
note when it is missing (`internal/runner/shell.go`). Verified:
`dir="/Users/<me>" note=""`. A task written as `rm -rf build` or `git clean -fd`
then runs in the home directory with nothing in the log saying so.

## Proposed design

### 1. Re-point only at a durable launcher; say so when it happens

Add to `migrate.Run`, before any scan:

- Resolve `filepath.Base(launcher)` with `exec.LookPath`. Re-point only if it
  resolves to the same file as `launcher` (compare with `os.SameFile`).
- Otherwise: skip the whole pass, return a `Result` carrying `SkippedLauncher`,
  and **do not write the stamp** — the stamp must keep naming the real launcher.
- `Report` writes one line to stderr for the skip: `· not re-pointing tasks at
  <path> (not on PATH — a dev build or a temp binary); scheduled tasks still use
  <recorded launcher>`.

The rule is one sentence: *a launcher the scheduler should invoke is one the
shell can find.* It admits every install path the installer and Homebrew use,
and excludes `go run`'s temp exe and `./every` from a checkout. It errs toward
under-re-pointing, which finding 1's doctor check then catches.

**The guard fires only when the launcher actually moves** — when the stamp
records a different path from the one running. Probing PATH unconditionally
would break scheduled runs: `run` migrates too, and launchd hands it a minimal
PATH with no `~/.local/bin` in it, so every fire would skip the pass and write
the warning into `_agent.log`. Covered by
`TestRecordedLauncherIsNeverProbed`.

When the migration does re-point because the launcher moved, `Report` names it:
`· re-pointed N task(s) at <launcher>` — distinct from the existing Ruby-era
repair line.

### 2. doctor checks the recorded launcher

`.runtime` already holds the launcher the units were last generated for
(`stampContent`, line 2). Add `migrate.RecordedLauncher(dirs) (string, bool)`
and one doctor check, before the per-task loop:

```
✓ scheduled tasks invoke an existing binary (/Users/me/.local/bin/every)
✗ scheduled tasks invoke a binary that is gone (/var/folders/.../exe/every)
  → re-install, then run any every command to re-point them
```

Stat + executable bit. Skipped silently when `.runtime` is absent. This check is
meaningful exactly in the skip case above: when the migration did run, the units
point at the binary now executing, and the check passes trivially.

### 3. Stop reading once the child is reaped

Restructure `capture`:

- `Wait` moves to a goroutine that signals a channel.
- The read loop stays on the main path. When the wait signal arrives, arm
  `pr.SetReadDeadline(time.Now().Add(detachGrace))` (`os.Pipe` files are
  poller-registered, so deadlines work); the loop then ends on the deadline
  error instead of blocking on a writer that is not our child.
- `detachGrace = 1 * time.Second`, one named constant.
- When the deadline fires rather than EOF, append to the output:
  `[every: command exited; something it started still holds the output pipe — stopped capturing after 1s]`

The timeout timer is unchanged and still arms across both phases, so a command
that ignores TERM still dies at the deadline. The grandchild is deliberately
**not** killed — a task whose job is to start a daemon is a legitimate use, and
killing it would break it. The exit code stays the direct child's.

### 4. Note the missing cwd, and check it in doctor

`Runner.workdir`: return a note on the missing/not-a-directory branch, worded
like the unreadable one:

```
note: cwd <path> no longer exists — ran from <home>
```

`doctorCwd`: add a check that the recorded cwd still exists, with the fix
`re-create the task from the directory it should run in`. Currently that
function only warns about TCC folders and only on darwin; the existence check
applies everywhere.

## Acceptance criteria

1. `go run ./cmd/every list` against a store with tasks leaves every unit
   pointing where it did, and prints the skip line. Test: `internal/migrate`,
   launcher under `t.TempDir()`.
2. A launcher moved between two on-PATH locations still re-points, and `Report`
   names the new path. (Extends `TestLauncherChangeTriggersMigration`.)
3. `doctor` fails with a named fix when `.runtime` records a launcher that no
   longer exists; passes when it does; stays silent when `.runtime` is absent.
4. `capture("sleep 20 & echo started", dir, 0)` returns inside 2 s, exit 0,
   output contains `started` and the detached-pipe note.
5. `capture` with a timeout is unchanged: still exit 124, still kills the group
   (existing tests stay green).
6. A task whose cwd was deleted logs the note, and `run --dry-run` reports it.
7. `doctor` flags a task whose cwd is gone.
8. `go test ./...` green; `testdata/golden/cli/cli.json` regenerated with
   `go test ./internal/cli -update` and the diff reviewed by hand.

## Risks

- The PATH probe reads the *interactive* shell's PATH, not the login shell's —
  the distinction this codebase is careful about elsewhere. Acceptable here: the
  question is "is this a durable install location", not "will the scheduler find
  it", and the launcher is always recorded as an absolute path either way.
- Someone who installs outside PATH (`--prefix ~/opt` with no PATH entry) and
  then moves the install no longer gets an automatic re-point. They get the skip
  line and the doctor check instead, which name the problem.
- `detachGrace` truncates output a legitimate background writer produces in the
  first second after the parent exits. The note in the log says so.

## Settled

1. **PATH rule**, not the narrower "refuse only under `os.TempDir()`" — it also
   covers `./every` from a checkout, the more likely way a user does this to
   themselves. Decided before implementation.
2. `detachGrace` = 1 s.
3. The skip is a note on stderr, not an error. The command the user asked for
   still runs.

## Outcome

Implemented. `go test ./...` green, `go vet` clean, `go test -race` clean on the
restructured capture. Verified end to end against a scratch `EVERY_HOME`:
`go run ./cmd/every list` prints the skip line, leaves `.runtime` naming the
real install, and `doctor` then reports on that install rather than on itself.

The golden CLI fixture needed no regeneration: `doctor`'s passing output is
deliberately absent from `testdata/golden/cli/cli.json` (it names host-specific
paths) and is covered by `test/e2e` per platform instead. The new checks have
unit coverage in `internal/cli/doctor_launcher_test.go`.

One residual, accepted: a store with tasks and no `.runtime` at all has no
recorded launcher to compare against, so the guard cannot fire and a throwaway
binary would still re-point. The window is one command wide — `add` is followed
by a `list`/`run`/`doctor` that stamps — and `doctor` names the bad launcher
afterwards.

---

# Part two: one-shots that were missed

Written after part one shipped. `every log` reading across the rotation
boundary (finding 6 below) is already implemented — it was a one-function fix
and needed no design. The two one-shot findings do.

## Current state

**`list` says `missed` on platforms that will still run the task.**
`nextDisplay` prints `missed` for any `once` whose instant has passed
(`internal/cli/list.go:157`). That is true on launchd and false on the other
two, deliberately so:

- systemd gets `Persistent=true` on the timer (`internal/backend/systemd.go:99`),
  which fires a calendar trigger the machine was off across at next boot.
- Task Scheduler gets `StartWhenAvailable` with no `EndBoundary`, and the code
  comment says why: an `EndBoundary` would let the service stop catching up, so
  "a missed one-shot would then be lost rather than run late"
  (`internal/backend/taskschd.go:238`).

So on Linux and Windows, `every list` reports a task as lost while the
scheduler intends to run it. The README repeats the claim without qualifying
it, and so does the CHANGELOG entry for `once`.

*Caveat on the evidence:* this is read from the generated units and the
schedulers' documented semantics, not from a live Linux or Windows box that was
powered off across a one-shot. The units are what CI checks; the catch-up
behavior is not.

**A missed launchd one-shot fires a year later.** The plist carries Month, Day,
Hour and Minute — launchd has no Year — so the trigger matches the same date
every year. `migrate.repair` refuses to *re-register* such a task
(`internal/migrate/migrate.go:140`), which stops an upgrade from re-arming it,
but nothing removes it: the plist stays on disk, stays loaded, and twelve months
later launchd fires it. `every run` then executes the command and retires the
task, a year late, with no indication that anything unusual happened.

## Proposed design

### 1. Backends declare whether they catch up

An optional interface beside `Retirer`, which is the pattern this file already
uses for a capability only some schedulers have:

```go
// CatchUpper is implemented by a backend whose scheduler runs a calendar
// trigger the machine slept or powered off through, rather than dropping it.
type CatchUpper interface{ CatchesUpMissed() bool }

func CatchesUpMissed(b Backend) bool { … default false … }
```

`Systemd` and `TaskScheduler` return true; `Launchd` does not implement it.
Keeps `Backend` at its current size rather than growing a tenth method every
implementation would have to carry.

### 2. A missed one-shot gets its own status

Today a passed `once` shows `ok` in STATUS (from whatever its last manual run
did) and `missed` in NEXT — the status column, which is the one people read,
says the opposite of what happened. Both columns should agree:

| Platform | STATUS | NEXT |
|---|---|---|
| launchd | `missed` | `—` |
| systemd, Task Scheduler | `late` | the instant it will catch up at |

`taskStatus` gains the case; it already takes everything it needs except the
catch-up answer.

**This widens a public vocabulary.** `list --json`'s `status` is documented as
`ok / FAIL / unscheduled / paused` in `man/every.1:65`, and a consumer matching
on it exactly will not know these two. The man page, README and the `schema`
command all need the new values, and it is a minor bump, not a patch.

### 3. launchd disarms what it cannot run

In `migrate.repair`, the branch that currently returns early for a passed
one-shot instead calls `b.Disable(name)` when the backend does not catch up.
The plist stops being loaded, so it cannot fire next year. The store entry
stays, so `list` still shows it as `missed` and `every rm` is still how it goes
away.

`Disable` is already tolerant of an agent that is not loaded, so this is
idempotent and costs one `launchctl bootout` the first time a command runs
after the miss.

## Acceptance criteria

1. A `once` whose instant has passed reports STATUS `missed` on a
   non-catching-up backend and `late` on a catching-up one; NEXT agrees.
2. `migrate` unloads a passed one-shot on launchd and leaves it registered on
   systemd and Task Scheduler.
3. The disarm is idempotent — a second pass neither errors nor reports work.
4. A `once` still in the future is untouched by all of the above.
5. `man/every.1`, README and `schema` list the new status values.
6. `go test ./...` and `go test -race ./...` green; golden CLI fixture
   regenerated only if `list` output for existing cases actually moves.

## Outcome

Implemented, both answers as recommended: the disarm is automatic in `migrate`,
and STATUS carries the new values.

Two deviations from the design above, both found while building it:

1. **NEXT shows `late`, not "the instant it will catch up at".** There is no
   such instant to print — systemd fires at the next boot and Task Scheduler at
   the next availability, neither of which is a time anyone can name ahead of
   it. Both columns show the same word.
2. **The disarm runs ahead of the stamp check, not inside the scan.** The stamp
   records what `every` itself last did; a one-shot goes missed because the
   clock moved, with nothing on disk changing — so a matching stamp would have
   hidden it forever. The pass now loads the store before the stamp check and
   shares that load with the scan, which costs one small file read on the
   settled path where the stamp used to buy a single stat. `migrate.Run` also
   gained an injected `now`, since whether a one-shot has passed is a question
   about the caller's clock and the CLI's is pinned in tests.

Idempotence comes from deleting the unit as well as unloading it: the next pass
sees no resource and skips, so a missed one-shot sitting in the store costs a
stat per command rather than a `launchctl` subprocess.

`doctor` exempts a disarmed one-shot from the resource and loaded checks in both
the text and JSON forms — the unit is absent on purpose, and reporting it would
turn a chosen state into two problems with a fix that re-creates something the
user probably no longer wants.

## Open question — answered

Both settled by the user before implementation; kept here as the record.


**Should the launchd disarm happen automatically, from any command that reads
the store?**

That is where it belongs mechanically: `migrate` already runs from `list`,
`run`, `doctor`, `inspect` and `set`, and already detects the condition. But it
means `every list` — a read command — unloads a launchd agent as a side effect.
The precedent exists (`migrate` already rewrites and re-registers units from
`list`), so this is consistent rather than new. The alternative is to leave the
plist armed and have `doctor` report it with a fix to run, which keeps read
commands read-only at the cost of the year-later fire staying possible for
anyone who never runs `doctor`.

Recommendation: do it automatically. A task that fires twelve months late is a
correctness bug, and the existing precedent already establishes that commands
repair the scheduler as they go.
