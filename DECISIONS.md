# every — design decisions

Dated log; append, don't rewrite.

- **2026-07-24 — Pure stdlib, system-Ruby compatible (2.6+).** No gems, so the
  tool runs on the Ruby that ships with macOS and needs zero install steps.
  Distribution later = Homebrew formula (brew itself vendors Ruby).
- **2026-07-24 — launchd agent calls `every run <name>`, not the command
  directly.** The runner is what captures output/exit/duration into
  `~/.local/share/every` — visibility is the product; plists alone can't do it.
- **2026-07-24 — Commands execute via `/bin/zsh -lc` (login shell), chdir'd to
  the directory where the task was added.** Kills the two classic launchd traps
  (PATH differs from terminal; wrong cwd). Known limit: `-l` reads `.zprofile`,
  not `.zshrc` — `doctor` explains this when a command doesn't resolve.
- **2026-07-24 — `launchctl bootstrap/bootout` (modern API) with `load -w`
  fallback.** On conflict, bootout + retry.
- **2026-07-24 — State layout:** tasks in `tasks.json`; per-task run history as
  JSONL (`runs/<name>.jsonl`); raw output in `logs/<name>.log` with `===`
  headers; 5 MB rotation to `.old`. `EVERY_HOME` env var overrides the data dir
  (used by tests).
- **2026-07-24 — Schedule DSL kept tiny:** `Ns/Nm/Nh`, `hourly`, `day <time>`,
  `<weekday> <time>`. No cron expressions in v1 — the whole point is not being
  cron.
- **2026-07-24 — Plists execute a runtime copy in `~/.local/share/every/runtime`,
  never the checkout.** Found by a live fire test: launchd-spawned ruby gets
  "Operation not permitted" reading anything under TCC-protected folders
  (Documents/Desktop/Downloads), so an agent pointing into the project checkout
  dies before our code loads. The copy is refreshed on every add/resume;
  `doctor` checks for its presence and warns about TCC-protected task cwd.
- **2026-07-24 — Calendar schedules are entry lists.** `Schedule` normalizes
  every calendar form (`day 9am,6pm`, `weekdays 9:30`, `monday,thursday 10:00`) into a
  list of `{weekday?, hour, minute}` entries → launchd `StartCalendarInterval` array.
  `from_h` migrates pre-0.2 `daily`/`weekly` task records, so existing tasks survive.
  Bounded intervals ("every 5m, 9–18, weekdays") deliberately NOT supported — launchd
  has no such primitive and faking it (108 dicts / runner-side guard) isn't worth it yet.
- **2026-07-24 — Failures notify by default.** A failed run fires a macOS
  notification via `osascript` (`--quiet` per task to opt out). Rationale: the product
  is visibility; a FAIL that waits for the user to run `list` is still silence.
- **2026-07-24 — No git yet.** Working account on this machine is a work
  account; repo stays local until published from the right identity.
- **2026-07-24 — Multi-agent code review pass (5 parallel reviewers).** Fixed
  the real findings across correctness/process/portability/CLI:
  - **Command reconstruction:** one token = shell command line (verbatim, so
    pipes/globs/vars work); multiple tokens = `Shellwords.join` (argv-faithful,
    so `touch "a b"` makes one file and `echo 'a; rm'` can't reactivate `;`).
  - **capture():** keep the tail for 32–64 KB output (was silently dropped);
    ASCII truncation marker + binary concat (no UTF-8/ASCII-8BIT crash on binary
    output); `wait.value` moved inside the timeout (a command that closes stdout
    but keeps running is now killed); exit code = 124 timeout / 128+signum on
    signal death / real exitstatus otherwise; kill the process *group* via
    negative pid (no getpgid reap race); `login_shell` uses `-c` for non-bash/zsh
    (dash rejects `-lc`).
  - **schedule:** empty/comma-only time list now errors (was a never-firing
    task); `13pm`/`0am` rejected; duplicate times deduped; `next_run` adds
    calendar days (DST-safe display); legacy weekday 7 clamped to 0.
  - **systemd:** `AccuracySec=1s` (sub-minute intervals no longer batch to 1 min);
    `Environment=EVERY_HOME` propagated; weekday index bounded.
  - **cli:** `--flag=value` supported; `--name --quiet` rejected (no silent
    mis-name); `--timeout 0s` rejected; `log -n N <name>` reads the right task;
    corrupt run timestamp no longer kills `list`; non-ArgumentError prints a clean
    line, not a backtrace; derived names re-truncated under the length cap; `.`/`..`
    rejected as names.
  - **runtime:** TCC copy gated to macOS (a `~/Documents` install on Linux stays
    live); atomic staged swap + version stamp (no `rm_rf` of the live dir mid-copy).
  - **doctor:** Full-Disk-Access hint macOS-only; actually checks `loginctl`
    linger on Linux. **launchd:** `logs/` created at add-time so the first fire's
    `_agent.log` redirect can open. **trim_runs** writes tmp+rename (crash-atomic).
  - **Deliberately deferred (documented, low real-world risk for a single-user
    tool):** no lock around the store's load→save (concurrent `add`/`rm` is a
    human serial action); a `setsid` grandchild can still escape the timeout kill;
    the launchd legacy `load -w` fallback can mask a failed bootstrap over bare
    SSH. Verified on macOS + a real Linux (ruby:3.2) container.
- **2026-07-24 — Lifecycle hardening (found by an operational/round-3 harness).**
  Four fixes: (1) the runtime copy is made ONLY from TCC-protected folders
  (`Runtime.needs_copy?`); from Homebrew/`~/code` the scheduler points at the
  live launcher (`File.expand_path($PROGRAM_NAME)`), so `brew upgrade` reaches
  scheduled runs instead of freezing stale code — verified end-to-end with a
  real launchd fire from a non-TCC install; (2) `last_run` scans from the end
  for the last *complete* JSONL record, so a crash-torn trailing line no longer
  shows "no runs"; (3) run duration uses `CLOCK_MONOTONIC`, immune to NTP/DST
  wall-clock jumps; (4) task names bounded to 100 chars so the unit filename
  can't exceed the 255-byte limit (was an ENAMETOOLONG crash).
- **2026-07-24 — Adversarial hardening (found by a hostile-input harness).**
  Four real fixes: (1) `--name ""` and traversal-y names now rejected/sanitized
  before touching the filesystem; (2) `Store#save` writes tmp+fsync+rename so a
  crash mid-write can't truncate the task registry; (3) captured output is
  bounded to first+last 32 KB (`Runner.capture` replaces `capture2e`) so a
  chatty task can't OOM; (4) optional `--timeout` kills an overrunning run —
  and its whole process group (`pgroup: true`) — so a hung task can't block its
  own next run. Command strings never reach plist XML (only sanitized name +
  escaped paths do), so injection/XML-breakout attempts already failed safely.
- **2026-07-24 — Run ledger is bounded (found by a stability stress test).**
  A long-term harness (100k runs, calendar boundaries, DST, log rotation, 40x
  add/rm churn, 200-task store) surfaced one real degradation: `runs/*.jsonl`
  grew unbounded, slowing `list` linearly over months. Fixed: `trim_runs`
  caps the file at RUN_TRIM_BYTES (~256 KB) after each append, rewriting to the
  last MAX_RUN_RECORDS (500). Guarantee is bounded file *size*, not exact line
  count. Everything else (calendar math across year/leap/DST — display only,
  launchd fires the real time; log rotation; churn; store round-trip) passed.
- **2026-07-24 — Backend abstraction + Linux beta.** `Backend.current`
  dispatches launchd (darwin) / systemd user timers (linux); both implement
  write/enable/disable/delete_units/loaded?/unit_path. systemd side:
  service+timer pair per task, `Persistent=true` mirrors launchd's
  missed-run-on-wake catch-up, runner uses `$SHELL -lc` on Linux and
  notify-send for failure alerts. Beta honesty: unit generation is tested
  (unit tests + systemd-analyze in CI on ubuntu-latest), live end-to-end on a
  real Linux desktop is NOT yet — hence "beta" and a call for field reports.
- **2026-07-24 — Published (supersedes "No git yet"):**
  github.com/Serhii-Leniv/every, authored as Serhii-Leniv via a dedicated
  `github-serhii` ssh alias; repo-local identity + pre-commit/pre-push guard
  hooks prevent any other account from committing or pushing here.
- **2026-09-01 — Canonical repo URL is `github.com/serhiileniv/every`.** The
  earlier `Serhii-Leniv` slug (see 2026-07-24) still resolves only because
  GitHub redirects a renamed account, which stops the moment anyone registers
  that name. Every URL a user fetches through — `install.sh`/`install.ps1`
  release downloads, the README one-liner, `HOMEPAGE` — now names the current
  account directly. The 2026-07-24 entry is left as written: it records what
  was true then.
- **2026-07-25 — Linux distribution is a POSIX `install.sh`, not packages.**
  `every` is one Ruby tree with no build step, so a `.deb`/`.rpm`/AUR set would
  be three release pipelines and three review queues to ship a `cp -R` — and
  each distro's Ruby packaging (`ruby` vs `ruby3.1` vs `ruby-full`) is a
  dependency argument we'd have to win five times. The script installs a
  self-contained tree at `<prefix>/lib/every` and symlinks `<prefix>/bin/every`
  at it; default prefix `~/.local` (no sudo), `/usr/local` as root. Specifics
  worth keeping:
  - **The symlink is the interface.** `Runtime.bin` records
    `File.expand_path($PROGRAM_NAME)` in each unit's `ExecStart`, which is the
    *unresolved* symlink — so re-installing (upgrading) reaches already-scheduled
    tasks without rewriting a single unit. `bin/every` finds its own `lib/` via
    `__dir__` (realpath), so the symlink still resolves the tree.
  - **Staged swap, never in-place.** New tree lands in `lib/every.new.$$`, the
    old one moves aside, then two renames — an interrupted upgrade can't leave a
    half-copied tree where a working install was. Same reasoning as
    `Runtime.ensure!`.
  - **Uninstall refuses while timers are live** (unless `--force`): pulling the
    launcher out from under a running timer is a task that silently stops
    working, which is the exact failure this tool exists to make impossible.
    An install manifest inside the tree records what to remove, so completions
    scattered across XDG dirs don't get orphaned.
  - **Completions follow the prefix's audience:** a `$HOME` prefix writes to the
    per-user dirs bash/fish actually read (zsh gets an fpath line, since there's
    no user dir it reads by default); a system prefix writes the vendor dirs.
  - Ruby is checked (2.6+, the macOS floor) with per-distro install hints before
    anything is written, and the install ends by running `every version` — if
    the tree can't load, the installer says so instead of leaving a broken
    symlink. Exercised end to end in CI: install, upgrade, uninstall,
    `curl | sh` download path, unwritable prefix, plus `shellcheck -s sh`.
- **2026-07-25 — `.gitattributes` pins LF on everything Unix executes.** The
  repo is edited from Windows, where `core.autocrlf=true` yields a CRLF
  worktree; a CRLF shebang fails as `env: 'ruby\r': No such file or directory`
  before any of our code runs. Found while testing the installer against a
  Windows checkout mounted into a container.
- **2026-08-31 — Native Windows uses Task Scheduler.** Windows tasks are
  registered under `\\every\\` from Task Scheduler XML and invoke a generated
  Ruby wrapper that pins `EVERY_HOME` before loading the runtime. Calendar
  triggers use `StartWhenAvailable`, overlapping runs are ignored, and interval
  schedules below one minute are rejected because Task Scheduler does not
  provide a reliable 10-second repetition primitive. Tasks use the current
  interactive user (and therefore run while that user is logged in). Native
  Windows defaults to `cmd.exe`; `EVERY_SHELL` can select PowerShell. WSL
  remains the Linux systemd path rather than being treated as native Windows.
