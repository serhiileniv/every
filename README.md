<p align="center">
  <img src="mascot.svg" width="140" alt="every mascot — a pixel-art alarm clock whose face shows a green check">
</p>

<h1 align="center">every</h1>

<p align="center"><strong>Schedule anything on your computer. Actually know it ran.</strong></p>

<p align="center"><sub>launchd on macOS · systemd on Linux · Task Scheduler on Windows · zero dependencies</sub></p>

<p align="center">
  <a href="https://github.com/serhiileniv/every/actions/workflows/test.yml"><img src="https://github.com/serhiileniv/every/actions/workflows/test.yml/badge.svg" alt="test"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-green.svg" alt="license: MIT"></a>
  <img src="https://img.shields.io/badge/dependencies-zero-blue.svg" alt="dependencies: zero">
  <a href="https://send.monobank.ua/jar/3zo8nv9iuF"><img src="https://img.shields.io/badge/support-monobank_jar-172B35" alt="support: monobank jar"></a>
</p>

<p align="center">
  <a href="https://trendshift.io/repositories/196320?utm_source=trendshift-badge&amp;utm_medium=badge&amp;utm_campaign=badge-trendshift-196320" target="_blank" rel="noopener noreferrer"><img src="https://trendshift.io/api/badge/trendshift/repositories/196320/daily?language=Ruby" alt="serhiileniv%2Fevery | Trendshift" width="250" height="55"/></a>
</p>

<p align="center">
  <img src="demo.gif" width="720" alt="every demo: schedule a task in one phrase, see ok/FAIL status, read run logs">
</p>

<p align="center">
  <a href="#vs-cron--vs-raw-launchd">vs cron</a> · <a href="#install">Install</a> · <a href="#schedules">Schedules</a> · <a href="#commands">Commands</a> · <a href="#for-scripts-and-agents">For scripts</a>
</p>

---

cron never tells you it silently skipped your backup. launchd wants 40 lines of
XML before ignoring you too. `every` is one human phrase — and a straight
answer to *"did it run?"*.

```bash
every day 9am -- brew update
every 30m -- '~/bin/sync-notes.sh'
every monday 10:00 -- './weekly-report.sh'
every monthly 1st 9am -- './invoice.sh'
every once tomorrow 9am -- './remind-me.sh'     # runs once, then removes itself
```

```
$ every list
NAME           SCHEDULE      LAST      STATUS   NEXT
brew           day 9am       5h ago    ok       in 19h
sync-notes     30m           12m ago   ok       in 18m
weekly-report  monday 10:00  3d ago    FAIL(1)  in 4d

$ every log weekly-report     # exact output of the run that broke
$ every log weekly-report -f  # follow it live, like tail -f
$ every list --failing        # just the ones that need attention
$ every doctor                # plain-language diagnosis
```

## vs cron · vs raw launchd

|  | cron | raw launchd | **every** |
|---|---|---|---|
| Add a job | `30 9 * * 1` in `crontab -e` | ~40 lines of XML + `launchctl` | `every monday 9:30 -- cmd` |
| Did it run? | silence | silence | `every list` → ok / FAIL |
| What did it print? | a local mailbox nobody reads | wire log paths yourself | `every log <name>` |
| Mac was asleep at 9am | run **lost forever** | runs on wake | runs on wake — and you can verify it |
| PATH | minimal, brew tools "not found" | minimal | your login shell, as in your terminal |
| Working directory | `$HOME`, always | configure it yourself | the directory you added the task from |
| A run fails | nothing happens | nothing happens | macOS notification + `FAIL` in `list` |
| When it breaks | Console.app archaeology | Console.app archaeology | `every doctor` tells you why |

Apple deprecated cron on macOS years ago. `every` is launchd — with a human
interface and a memory.

## Install

**macOS and Linux** — one line, no sudo (installs into `~/.local`):

```bash
curl -fsSL https://raw.githubusercontent.com/serhiileniv/every/main/install.sh | sh
```

**Windows** — one line, PowerShell:

```powershell
irm https://raw.githubusercontent.com/serhiileniv/every/main/install.ps1 | iex
```

Re-run either to upgrade. The installer verifies the download against the
release checksums, and puts the binary where your scheduler already expects it,
so an upgrade reaches tasks you scheduled with an earlier version.

<details>
<summary>Prefer Homebrew on macOS? It upgrades in place.</summary>

```bash
brew tap serhiileniv/tap && brew install every
```

Both install the same binary. Homebrew gives you `brew upgrade`; the one-liner
works anywhere, including machines without Homebrew.

</details>

Native Windows tasks use the Windows Task Scheduler and store data under
`%LOCALAPPDATA%\every`. Interval schedules on Windows require at least one
minute; use WSL if you need the Unix backends or sub-minute intervals.

<details>
<summary>Options: system-wide, a pinned version, uninstall</summary>

```bash
# system-wide
curl -fsSL …/install.sh | sudo sh -s -- --prefix /usr/local

# somewhere else, or a specific release
curl -fsSL …/install.sh | sh -s -- --prefix ~/opt --version 0.5.1

# from a checkout (same flags; builds what's in front of it if Go is present)
git clone https://github.com/serhiileniv/every.git && ./every/install.sh

# uninstall (tasks and logs are kept; it won't strand a live timer)
curl -fsSL …/install.sh | sh -s -- --uninstall
```

Re-running the installer upgrades in place. Scheduled tasks keep working
across an upgrade — units point at `<prefix>/bin/every`, which stays put.

</details>

Zero dependencies, literally: `every` is a single static binary with nothing to
install alongside it — no runtime, no interpreter, no shared libraries. The
installer verifies the download against the release checksums, and sets up
`man every` and tab completion for bash, zsh, and fish.

## Schedules

| You type | It means |
|---|---|
| `90s` · `15m` · `2h` | fixed interval |
| `hourly` | every hour |
| `day 9am` · `day 17:30` | daily at that time |
| `day 9am,6pm` | daily, several times |
| `weekdays 9:30` · `weekends 11am` | Mon–Fri / Sat+Sun |
| `monday 10:00` · `monday,thursday 6pm` | weekly on those days |
| `monthly 1st 9am` · `monthly 1,15 18:00` | on those days of the month (29–31 skip shorter months) |
| `once 15:30` · `once tomorrow 9am` · `once friday 5pm` | one time only: today (or tomorrow if it's passed), a day, a weekday |
| `once 2026-12-24 18:00` · `once 45m` | one time only: a date, or a delay from now (≥ 1 minute) |

A `once` task fires and then removes itself, logs and all history kept — `every log <name>`
still works after it's gone. Until its moment, it's an ordinary task: `every run <name>`
beforehand checks the command without using it up.

## Commands

```
every <schedule> [flags] -- <command>    schedule it
every list [--failing]                   status of everything          (alias: ls)
every log <name> [-n N] [-f]             output of recent runs, or follow it live
every run <name>                         run it right now, see the output
every pause <name> / resume <name>       stop / start scheduling
every rm <name>                          remove (logs are kept)        (alias: remove)
every doctor                             why isn't it running?
every version                            what's installed              (also: -V)
every help [command]                     this list, or one command's page (also: -h)
```

Flags when adding a task:

| Flag | What it does |
|---|---|
| `--name NAME` | name it explicitly (otherwise derived from the command) |
| `--quiet` | no desktop notification when this task fails |
| `--timeout 30m` | kill a run that overruns, so it can't block the next one |
| `--on-fail '<cmd>'` | run something when it fails — gets `EVERY_TASK`, `EVERY_EXIT`, `EVERY_LOG` in its environment |

And anywhere: `--json`, `--color auto\|always\|never`, `-h`, `-V`.

`every help <command>` (or `every <command> --help`) prints that command's
flags and examples; `every help schedules` lists every schedule form.

Unknown flags and unexpected arguments are a usage error, never ignored — a
typo'd `--json` fails loudly instead of quietly printing the wrong format, and
`every rm backup extra` refuses rather than guessing which half you meant.

Failed runs pop a desktop notification (silence it per task with `--quiet`).

### When it doesn't run

`every doctor` checks the whole chain and names the fix for anything broken:

```
$ every doctor
  ✓ launchd user session reachable (gui/501)
  ✓ data dir writable (/Users/you/.local/share/every)

task: weekly-report
  ✓ scheduler resource exists (~/Library/LaunchAgents/com.every.weekly-report.plist)
  ✗ scheduled in launchd
    → load it: every resume weekly-report
  ✓ command resolvable in login shell (zsh)

1 problem
```

## For scripts and agents

`--json` works on **every** command, not just `list`. Failures emit an object on
stderr with a stable `error` code, and the exit status is unchanged:

```bash
every list --json | jq -r '.[] | select(.status | test("FAIL")) | .name'   # failing tasks
every inspect backup --json | jq -r .next                                  # when's the next run
every exists backup || every day 3am --name backup -- ~/bin/backup.sh      # idempotent add
```

```
$ every rm nosuch --json
{"error":"no_such_task","message":"no task \"nosuch\"","name":"nosuch"}   # on stderr, exit 66
```

Codes: `usage`, `unknown_flag`, `bad_schedule`, `bad_duration`, `bad_name`,
`already_exists`, `unsupported_schedule`, `no_such_task`, `no_logs`,
`corrupt_store`, `scheduler_failed`, `missed`.

| Command | What it's for |
|---|---|
| `every set <when> --name <n> -- <cmd>` | add, or update in place — no window where the task doesn't exist |
| `every inspect <name>` | everything about one task (alias: `show`) |
| `every exists <name>` | exit 0 if it exists, 66 if not; prints nothing |
| `every run <name> --dry-run` | the shell, directory, timeout and exact command that would run |
| `every schema [command]` | the JSON shape a command emits, generated from the types |
| `every log <name> --json [--with-output]` | run history from the ledger; output omitted unless asked |

### Exit codes

Follows `sysexits.h`, so scripts can branch on `$?`:

| Code | Meaning |
|---|---|
| `0` | success |
| `64` | usage error (bad arguments, unknown flag) |
| `66` | no such task / no logs yet |
| `1` | other failure |

`every run` (and scheduled runs) exit with the command's own code, or `124` on
`--timeout`, or `128+signum` if a signal killed it.

## How it runs

- **The command is a shell line** (like cron): tokens after `--` are run through
  your login shell, so env prefixes, pipes, `&&`, and globs all work. Your outer
  shell strips quotes first, so quote args with spaces or metacharacters as you
  would at a prompt — wrap the whole thing in one quoted string when in doubt:
  `every day 9am -- 'pg_dump db | gzip > ~/backup.gz'`,
  `every 1h -- 'touch "my file.txt"'`.
- **PATH is the login shell's, not your terminal's.** Scheduled runs go through
  a login shell, which reads `~/.zprofile` / `~/.bash_profile` — not `~/.zshrc`.
  A `PATH` line that lives only in `~/.zshrc` works when you type the command
  and fails under the scheduler with `command not found`. `every doctor` probes
  in a clean login shell and tells you which file to move the line to.
  On macOS the shell is always `/bin/zsh -l`, whatever your `$SHELL` is —
  launchd gives a task the same login shell every time. On Linux it follows
  `$SHELL`.
- **The working directory** is the one you added the task from, not `$HOME`. If
  that directory is gone by the time the task fires, the run falls back to
  `$HOME` and says so at the top of the log.
- **Timeouts:** add `--timeout 30m` to kill a run that overruns — otherwise a
  task that hangs will block its own next run (the OS won't start a second copy
  of the same task). The kill takes the whole process tree with it.
- **A command that backgrounds something** (`server &`, anything that
  daemonizes) finishes when the command itself exits, not when its child does.
  `every` stops capturing a second later and notes it in the log; the child is
  left running.
- **Output** is captured but bounded (first + last 32 KB per run), so a chatty
  command can't blow up memory or fill the disk. Logs rotate at 5 MB.
- **One-shots** land on a whole minute (launchd calendar triggers have no
  seconds), so `once 90s` means "the next whole minute at least 90 s away". A
  one-shot the machine slept through fires on wake like any calendar task. One
  it was powered off across depends on the scheduler: macOS drops it, and it
  shows as `missed` — unscheduled so it can't fire on the same date a year
  later, and re-added with `every rm` then `every once …`. Linux and Windows
  run it late instead, and it shows as `late` until they do. While it fires,
  `list` shows `running`.

<details>
<summary>Platform notes: macOS, Linux (beta), Windows</summary>

- **macOS:** launchd user agents at
  `~/Library/LaunchAgents/com.every.<name>.plist`. launchd can't execute from
  TCC-protected folders, so agents run a copy of `every` from the data dir —
  see [DECISIONS.md](DECISIONS.md) for why.
- **Linux (beta):** systemd user timers, same commands; units live in
  `~/.config/systemd/user`. Timers stop at logout unless you run
  `loginctl enable-linger $USER` (the installer tells you if it's off). Failed
  runs notify through `notify-send`. Field reports very welcome — the units are
  tested, months of real desktop uptime aren't.
- **Windows:** native Task Scheduler tasks under the `\every\` task path,
  invoked through the command wrapper. Calendar tasks use `StartWhenAvailable`;
  interval tasks require at least one minute (use WSL for sub-minute). Tasks run
  as the current interactive user, so they run while that user is logged in.
  The default shell is `cmd.exe`. Failure notifications use best-effort
  `msg.exe` and are always recorded in the run log.

</details>

<details>
<summary>Where things live, and the environment variables</summary>

On macOS and Linux, tasks, logs and ledgers live under `~/.local/share/every`;
native Windows uses `%LOCALAPPDATA%\every`.

| Variable | Effect |
|---|---|
| `EVERY_HOME` | override the data dir entirely |
| `XDG_DATA_HOME` | data dir parent (set it *before* creating tasks — existing ones stay put) |
| `XDG_CONFIG_HOME` | where systemd user units go, on Linux |
| `NO_COLOR` | disable color ([no-color.org](https://no-color.org)) |
| `CLICOLOR_FORCE` | keep color when stdout isn't a terminal |
| `EVERY_SHELL` | Windows only: the shell tasks run through (default `%COMSPEC%`) |
| `EVERY_POWERSHELL` | Windows only: the PowerShell used for `-Command` tasks |

`--color` beats both color variables; `NO_COLOR` beats `CLICOLOR_FORCE`.

Uninstall: `every rm` each task, then `rm -rf ~/.local/share/every`.

</details>

## Roadmap

Where it's going and what's still rough: [ROADMAP.md](ROADMAP.md). Issues and
PRs welcome.

MIT © [Serhii Leniv](https://github.com/serhiileniv)
