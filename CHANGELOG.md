# Changelog

## 0.5.0 — unreleased

Two pieces of work, released together because the first was never published on
its own: `every` is now a single static binary, and it is legible to programs
as well as to people.

Upgrading from 0.3.x needs nothing. Scheduled tasks are repaired automatically
the first time any command runs — units written before this release invoke a
Ruby interpreter that is no longer there — and `every list` says so in one line
when it happens. Rolling back works too: an older `every` reads a store this one
wrote, and keeps firing tasks whose units this one rewrote.

### No runtime required

Nothing to install alongside it, nothing to keep in sync, and the
*dependencies: zero* badge is finally literal.

The reason was distribution, not speed. macOS ships Ruby 2.6 deprecated and
slated for removal, Windows has none at all, and Linux distributions disagree
about what the package is called. Roughly half the installer existed to find an
interpreter, and both bugs shipped since 0.3.0 — `every add` on Windows, and the
wrapper load path — were distribution bugs rather than logic bugs.

Behavior is unchanged on purpose: same commands, same flags, same schedule
grammar, same on-disk format, same generated scheduler units. That was verified
against the implementation being replaced rather than against a description of
it, while both existed side by side — 1,794 schedule inputs compared on the
accept/reject decision, the exact rejection message and the serialized record;
1,177 unit renderings compared byte for byte; 81 CLI invocations compared on
stdout, stderr and exit code; and eight stateful command sequences compared step
by step. Zero disagreements.

`install.sh` downloads one binary and verifies it against the release
checksums. `install.ps1` drops the generated `every.cmd` shim: with no
interpreter to pin, Task Scheduler invokes the binary directly. Releases are
built and published by GoReleaser.

### Legible to programs

`every` was readable by people and opaque to anything else. An agent had to
scrape a table, guess at failures from prose on stderr, and do a read-then-write
dance that raced with itself.

Everything here is additive. Every invocation that worked before produces the
same bytes and the same exit code, asserted for 107 invocations; the only
exception is `every help`, which documents the new commands.

- **`--json` on every command**, not just `list`. Each emits its natural shape;
  `list --json` is unchanged, because the completion scripts scrape it.
- **Failures are objects too**, on stderr, with a closed vocabulary of codes —
  `no_such_task`, `no_logs`, `bad_schedule`, `bad_duration`, `bad_name`,
  `already_exists`, `unsupported_schedule`, `corrupt_store`,
  `scheduler_failed`. The code is the contract; the message beside it may be
  reworded. Exit codes are unchanged, and the two forms of a command are tested
  to agree about whether it failed.
- **`every set <when> --name <n> -- <cmd>`** adds or updates in place. It exists
  because `rm` then `add` leaves a window where the task does not exist, and if
  the second half fails it never comes back. `set` holds the store lock across
  the whole operation, writes the registry last, and restores the previous unit
  if the scheduler refuses. It preserves run history and the creation time,
  because an update is not a new task.
- **`every inspect <name>`** — everything about one task, including whether the
  scheduler actually has it and when it runs next.
- **`every exists <name>`** — exit 0 or 66, and nothing on stdout.
- **`every run <name> --dry-run`** — the shell, directory, timeout and exact
  command that would be used, without running it.
- **`--on-fail '<command>'`** per task, run after a failure with `EVERY_TASK`,
  `EVERY_EXIT` and `EVERY_LOG` in its environment. Its output goes into the
  task's own log and its exit code is never propagated: a broken notifier must
  not disguise a working task.
- **`every schema [command]`** — the JSON shape a command emits, generated from
  the types rather than hand-written, so it cannot describe a field that does
  not exist.
- `every log --json` omits captured output unless `--with-output` is given. The
  default path never opens the log file, so it costs the same whether the log is
  empty or at its 5 MB rotation limit.

`--machine` was proposed and dropped. The premise was ANSI leaking into non-TTY
output; measured, every command through a pipe emits zero escape bytes, because
color is already gated on an interactive terminal, `NO_COLOR` and `TERM`. The
real hazard was the `—` and `·` glyphs in the table, and `--json` answers that
better than a second text format nobody would keep aligned.

### Fixed

All four were present in 0.3.1:

- A task name written directly into `tasks.json` could escape the units
  directory: `every resume` on a name containing `../` opened a plist outside
  `~/Library/LaunchAgents`. Names are now validated wherever they become a path.
- `doctor` reported a problem on healthy Linux machines whenever `$USER` was
  unset — a bare `docker exec`, some terminals, anything not started by a login
  shell — and then advised `loginctl enable-linger` with no name.
- The installer told macOS users "systemd not found", on the platform where
  launchd is the backend.
- A timed-out run could signal a process group after the child had been reaped
  and its pid possibly recycled.

Two more were Windows-only and found by CI during the port: `every 15s` exited 1
instead of 64, and `~` was never expanded in a path built with the host
separator.

## 0.3.1 — 2026-09-01

A Windows fix that 0.3.0 needed badly, and the tests that would have caught it.

### Fixed
- **`every add` could never register a task on Windows.** `schtasks /Create
  /XML` hands the file to MSXML, which relies on a byte-order mark to know how
  it is encoded. The XML was written as plain UTF-8 with no BOM, so it was
  decoded as ANSI, reached the encoding declaration and failed with
  `The task XML is malformed. (1,40)::ERROR: unable to switch the encoding`.
  Nothing else in the tool works without a task, so the Windows backend was
  effectively unusable in 0.3.0. The XML is now written UTF-16LE with a BOM,
  which is what schtasks documents.
- **Windows data paths no longer mix separators.** `LOCALAPPDATA` comes back
  with backslashes and `File.join` adds a forward slash, so the data directory
  printed as `C:\Users\me\AppData\Local/every` in `doctor`, `list` and error
  messages. Same location on disk, so nothing moves.

### Added
- **End-to-end CI on all three schedulers.** Every backend was previously
  verified only by generation tests — the suite asserted the shape of a plist,
  a systemd unit and a Task Scheduler XML, then stopped. Nothing registered a
  task with a real scheduler, ran it, and removed it again, which is exactly
  how both bugs above reached a release.

  `test/e2e/windows.ps1` drives the installed `every` against the live Task
  Scheduler: registration confirmed by asking `Get-ScheduledTask` directly
  rather than trusting `every list`, pause/resume checked against the service's
  own `State`, quoting through the temp-script path, `@echo off`, timeouts,
  exit codes, and `rm` of a task the service has already dropped.
  `test/e2e/unix.sh` does the same for launchd and systemd and asserts the
  agents directory is byte-identical afterwards. 59 assertions on Windows, 62
  each on macOS and Linux.

## 0.3.0 — 2026-09-01

Windows becomes a first-class platform, and Linux gets an install path that
isn't `git clone`.

### Added
- **Native Windows support** — tasks register with the Windows Task Scheduler
  under the `\every\` task path, so `every` now speaks all three system
  schedulers: launchd, systemd, and Task Scheduler. Data lives under
  `%LOCALAPPDATA%\every`. The command runs through a temporary `.cmd` (or
  `.ps1`, when `EVERY_SHELL` points at PowerShell) rather than being passed as
  an argv tail, which is what keeps quoting like `echo "my file.txt"` intact.
  Tasks invoke a stable `every.cmd` shim instead of the Ruby interpreter path,
  so upgrading Ruby doesn't silently orphan them. Interval schedules need at
  least one minute on Windows. Thanks to [@OnlyPiglet](https://github.com/OnlyPiglet)
  for the implementation.
- **`install.ps1`** — PowerShell installer, staged and swapped like the Unix
  one, with the checkout path exercised in Windows CI.
- **A Linux install path that isn't source-only** — `install.sh`:

  ```bash
  curl -fsSL https://raw.githubusercontent.com/serhiileniv/every/main/install.sh | sh
  ```

  Installs into `~/.local` without sudo (`--prefix /usr/local` for system-wide),
  places the man page and bash/zsh/fish completions where each shell looks, and
  checks for a usable Ruby first with per-distro hints if it's missing. Re-run
  it to upgrade — scheduled tasks keep firing, because units point at the
  `<prefix>/bin/every` symlink rather than the tree behind it. `--uninstall`
  removes exactly what was installed and refuses to strand a live timer.
  POSIX `sh`, `shellcheck`-clean, exercised end to end in CI.
- **`.gitattributes`** — pins LF on everything a Unix box executes, so a commit
  from a Windows checkout can't ship a CRLF shebang that fails to run on Linux.

### Fixed
- **`every list` and `every doctor` on Windows** — the PowerShell query that
  reads task state was built in an interpolating heredoc, so the `'\every\'`
  path literal collapsed to an ESC byte and the query could never match. It no
  longer filters by task path at all: `Get-ScheduledTask -TaskPath` throws when
  nothing matches, and having no tasks yet is a normal state, not an error.
- **`every rm` on Windows** could strand a task. `disable` and `delete_units`
  raised whenever `schtasks` failed, including when the task was already gone —
  which left a registry entry with no way to remove it. Both now raise only if
  the task is still registered afterwards.
- **Scheduled output on Windows** no longer carries an echo of the command
  itself; the generated `.cmd` starts with `@echo off`.
- **`require "csv"`** is no longer loaded eagerly on every platform. It became a
  bundled rather than default gem in Ruby 3.4, so under Bundler without it in
  the Gemfile this was a `LoadError` before any `every` code ran.

## 0.2.0 — 2026-07-25

A Unix-philosophy pass: more composable, more optimized, more conventional.

### Added
- **`every list --json`** — one JSON object per task (name, schedule, command,
  status, last run, next), so you can script it: `every list --json | jq …`.
- **XDG Base Directory support** — data honors `$XDG_DATA_HOME` and systemd
  units honor `$XDG_CONFIG_HOME`; `EVERY_HOME` still overrides. The default
  (`~/.local/share/every`) is unchanged, so existing installs don't move.
- **`man every`** and **shell completions** for bash, zsh, and fish (subcommands
  + task names), installed by the Homebrew formula.

### Changed
- **Consistent exit codes** (sysexits): `0` ok · `64` usage · `66` no such
  task/log · `1` other. "No such task" was `1` in some commands and `66` in
  `run`; now `66` everywhere. Documented in the README.

### Fixed / internal
- **Faster `list` and `doctor`** — one bulk scheduler query (`launchctl list` /
  `systemctl list-units`) instead of one subprocess per task.
- **Concurrent-safe writes** — `add`/`rm`/`pause`/`resume` hold an exclusive
  `flock` on the registry, so parallel commands can't lose an update.

## 0.1.3 — 2026-07-24

### Changed
- Color is now consistent and honest across the CLI. Success `✓` marks
  (`scheduled`/`paused`/`resumed`/`removed`), `doctor`'s `✓`/`✗` checks and
  summary, and `run`'s exit line all pick up green/red — matching what `list`
  already did. A new `Every::Color` helper centralizes the ANSI so it's applied
  the same way everywhere, and it **respects [`NO_COLOR`](https://no-color.org)
  and `TERM=dumb`, and never colors non-TTY output** (pipes, files, CI logs stay
  clean). Color is only ever a hint — every message still reads with the codes
  stripped.

## 0.1.2 — 2026-07-24

Dogfooding pass — small fixes from actually using it.

### Fixed
- `list` now reflects the scheduler's real state, not just the run ledger: a
  task whose agent isn't loaded shows **`unscheduled`** (with a hint) instead of
  a stale `ok`/`NEXT`. Closes a gap in the core "know it ran" promise — a task
  that silently stopped firing no longer looks healthy.

### Changed
- After scheduling a task, the confirmation spells out that **output goes to the
  log** (`every log <name>`), since the task runs detached and its output won't
  appear in your terminal — the #1 first-timer confusion.
- A mistyped command (e.g. `every update`) now gets a helpful message listing
  the real commands and the `-- <command>` form, instead of a cryptic
  "expected schedule".

## 0.1.1 — 2026-07-24

Hardening release. Same features as 0.1.0, made dependable after several rounds
of stress testing and code review.

### Added
- **Linux support (beta)** — systemd user timers, same commands; units in
  `~/.config/systemd/user`. Run `loginctl enable-linger $USER` so timers fire
  at boot / after logout.
- **`--timeout 30m`** — kill a run that overruns (and its whole process group)
  so a hung task can't block its own next run.
- **Richer schedules** — `day 9am,6pm` (several times a day), `weekdays 9:30`,
  `weekends 11am`, `monday,thursday 10:00`.
- **Failure notifications** — a failed run pops a desktop notification
  (`--quiet` to opt out).
- **`every run <name>`** is a documented command; prints output on a terminal.
- **Self-identifying** `every version` / `every help` (name, tagline, homepage).

### Fixed
- Output and the run ledger are bounded (no OOM on a chatty task, no unbounded
  disk growth); log/ledger/registry writes are crash-atomic.
- Run duration uses a monotonic clock (immune to NTP/DST jumps); `next run`
  display is DST-safe.
- Scheduled runs use the live installed code, so `brew upgrade` takes effect
  (a `~/Documents` install on macOS still runs from a TCC-safe copy).
- `doctor` no longer false-fails a working `~/path/to/script` task; checks
  systemd linger on Linux; macOS-only hints stay on macOS.
- Re-adding a task with a previously-removed name starts with a clean history.
- One corrupt/forward-incompatible task no longer hides the whole `list`.
- Empty/invalid schedules (`day ,`, `13pm`) are rejected instead of silently
  creating a task that never fires.
- Clean error messages instead of Ruby backtraces; `--timeout 0s`, flag-eating
  `--name`, and over-long names are rejected.

### Changed
- The command is a shell command line (like cron): tokens after `--` run in
  your login shell, so env prefixes, pipes, `&&`, and globs work. Quote args
  with spaces or metacharacters as you would at a prompt.

## 0.1.0 — 2026-07-24

Initial release. Schedule anything on your Mac with one phrase; `list` / `log`
/ `doctor` show whether it actually ran. Pure Ruby stdlib, macOS/launchd.
