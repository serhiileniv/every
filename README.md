<p align="center">
  <img src="mascot.svg" width="140" alt="every mascot — a pixel-art alarm clock whose face shows a green check">
</p>

<h1 align="center">every</h1>

<p align="center"><strong>Schedule anything on your computer. Actually know it ran.</strong></p>

<p align="center"><sub>launchd on macOS · systemd on Linux (beta) · Task Scheduler on Windows · zero dependencies</sub></p>

<p align="center">
  <a href="https://github.com/serhiileniv/every/actions/workflows/test.yml"><img src="https://github.com/serhiileniv/every/actions/workflows/test.yml/badge.svg" alt="test"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-green.svg" alt="license: MIT"></a>
  <img src="https://img.shields.io/badge/dependencies-zero-blue.svg" alt="dependencies: zero">
  <a href="https://send.monobank.ua/jar/3zo8nv9iuF"><img src="https://img.shields.io/badge/support-monobank_jar-172B35" alt="support: monobank jar"></a>
</p>

<p align="center">
  <img src="demo.gif" width="720" alt="every demo: schedule a task in one phrase, see ok/FAIL status, read run logs">
</p>

<p align="center">
  <a href="#install">Install</a> · <a href="#schedules">Schedules</a> · <a href="#commands">Commands</a> · <a href="#vs-cron--vs-raw-launchd">vs cron</a>
</p>

---

cron never tells you it silently skipped your backup. launchd wants 40 lines of
XML before ignoring you too. `every` is one human phrase — and a straight
answer to *"did it run?"*.

```bash
every day 9am -- brew update
every 30m -- '~/bin/sync-notes.sh'
every monday 10:00 -- './weekly-report.sh'
```

```
$ every list
NAME           SCHEDULE      LAST          STATUS   NEXT
brew           day 9am       24 Jul 09:00  ok       25 Jul 09:00
sync-notes     30m           24 Jul 14:30  ok       24 Jul 15:00
weekly-report  monday 10:00  21 Jul 10:00  FAIL(1)  28 Jul 10:00

$ every log weekly-report     # exact output of the run that broke
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
curl -fsSL …/install.sh | sh -s -- --prefix ~/opt --version 0.4.0

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

## Commands

```
every <schedule> [--name NAME] [--quiet] -- <command>   schedule it
every list [--json]                           status of everything
every log <name> [-n N]                       output of recent runs
every run <name>                              run it right now, see the output
every pause / resume <name>                   stop / start scheduling
every rm <name>                               remove (logs are kept)
every doctor                                  why isn't it running?
```

Failed runs pop a desktop notification (silence it per task with `--quiet`).

`every list --json` prints one object per task for scripting — pipe it to `jq`:

```bash
every list --json | jq -r '.[] | select(.status | test("FAIL")) | .name'   # failing tasks
```

### Exit codes

Follows `sysexits.h`, so scripts can branch on `$?`:

| Code | Meaning |
|---|---|
| `0` | success |
| `64` | usage error (bad arguments) |
| `66` | no such task / no logs yet |
| `1` | other failure |

`every run` (and scheduled runs) exit with the command's own code, or `124` on
`--timeout`, or `128+signum` if a signal killed it.

## Fine print

- **The command is a shell line** (like cron): tokens after `--` are run through
  your login shell, so env prefixes, pipes, `&&`, and globs all work. Your outer
  shell strips quotes first, so quote args with spaces or metacharacters as you
  would at a prompt — wrap the whole thing in one quoted string when in doubt:
  `every day 9am -- 'pg_dump db | gzip > ~/backup.gz'`,
  `every 1h -- 'touch "my file.txt"'`.
- **Timeouts:** add `--timeout 30m` to kill a run that overruns — otherwise a
  task that hangs will block its own next run (the OS won't start a second copy
  of the same task). The kill takes the whole process tree with it.
- **Output** is captured but bounded (first + last 32 KB per run), so a chatty
  command can't blow up memory or fill the disk.
- Tasks live in `~/Library/LaunchAgents/com.every.<name>.plist`; runs are
  recorded under `~/.local/share/every/` (logs rotate at 5 MB). launchd can't
  execute from TCC-protected folders, so agents run a copy of `every` from the
  data dir — see [DECISIONS.md](DECISIONS.md) for design notes.
- **Linux (beta):** systemd user timers, same commands; units live in
  `~/.config/systemd/user`. Timers stop at logout unless you run
  `loginctl enable-linger $USER` (the installer tells you if it's off). Failed
  runs notify through `notify-send`. Field reports very welcome — the units are
  tested, months of real desktop uptime aren't.
- **Windows:** native Windows Task Scheduler tasks, same commands; tasks live
  under the `\\every\\` task path and invoke the binary through the command
  wrapper. Calendar tasks use `StartWhenAvailable`; interval tasks require at
  least one minute. Tasks use the current interactive user by default, so they
  run while that user is logged in. The default shell is `cmd.exe` (set
  `EVERY_SHELL` to a PowerShell executable when needed). Failure notifications
  use best-effort `msg.exe` and are always recorded in the run log.
- **Where things live:** on macOS/Linux, tasks/logs/ledgers live under
  `~/.local/share/every` by default; native Windows uses
  `%LOCALAPPDATA%\every`. Honors `$XDG_DATA_HOME` (data) and
  `$XDG_CONFIG_HOME` (systemd units on Linux); `EVERY_HOME` overrides the data
  dir entirely. `NO_COLOR` is respected. If you set `XDG_DATA_HOME` *after*
  creating tasks, the old ones stay in the old dir.
- Uninstall: `every rm` each task, then `rm -rf ~/.local/share/every`.

## Roadmap

Where it's going and what's still rough: [ROADMAP.md](ROADMAP.md). Issues and
PRs welcome.

MIT © [Serhii Leniv](https://github.com/serhiileniv)
