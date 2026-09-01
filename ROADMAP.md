# Roadmap

Where `every` is going, and the honest gaps in it today. Order is rough
priority, not a promise. Issues and PRs welcome — items marked **[good first
issue]** are self-contained.

## Known gaps in v0.3

These are real limitations, not hidden ones:

- **Linux is beta.** CI now registers a real systemd user timer, runs it,
  pauses, resumes and removes it on every commit, and validates every generated
  `OnCalendar` expression with `systemd-analyze`. What is still unproven is
  long-running use on a real desktop over days. Needs field reports before it
  loses the "beta" label.
- **Windows is new.** CI drives the live Task Scheduler on every commit —
  register, run, pause, resume, remove — and checks state against the service
  itself rather than through `every`. Long-running use on a real desktop is
  still unproven, so the same caveat as Linux applies: field reports wanted.
  Interval schedules are floored at one minute there, and sub-minute intervals
  need WSL.
- **No bounded intervals.** "every 15m, but only 9–18 on weekdays" has no
  launchd primitive and isn't supported. Would need a runner-side time-window
  guard.
- **Log rotation is crude** — a single 5 MB cutoff to `.log.old`. No
  compression, no retention policy. (The run *ledger* is bounded to the last
  500 runs; the detailed `.log` is what's still coarse.)
- **Schedule DSL is small.** No "last day of month", no "every other week", no
  cron expressions (by design — but some of these are worth adding).
- **Long-term durability is unproven.** The tool is new; behavior across macOS
  upgrades and months of uptime hasn't been observed yet.

## Next

- **Staleness watchdog** — warn when a task hasn't had a *successful* run in N
  days/intervals (a backup that silently stopped is the exact pain `every`
  exists to kill). Builds on the existing run ledger.
- **`every edit <name>`** — change a task's schedule or command in place. Today
  you must `every rm` + re-add with the same name (works cleanly, but it's an
  extra step and easy to forget the original flags). **[good first issue]**
- **`every run --all` / run-on-add** — trigger a task once immediately so you
  can confirm it works before trusting the schedule.
- **Richer failure notifications** — include the last error line, click to open
  the log.

## Later

- **Linux out of beta** — once field-tested. (The install path is no longer
  source-only: `install.sh`, one `curl | sh`. Distro packages — deb/rpm/AUR —
  only if there's demand; see [DECISIONS.md](DECISIONS.md) for why the script
  came first.)
- **More schedule forms** — `monthly`, `last day`, `every 2 weeks`,
  `weekdays 9-18/1h` (bounded intervals).
- **`every export` / `import`** — dump tasks to a portable file, re-create them
  on another machine (dotfiles-friendly).
- **Config file** — declare tasks in a checked-in file, `every sync` to apply
  (infrastructure-as-code for your personal cron).
- **Log retention policy** — configurable size/age, optional compression.
  **[good first issue]**

## Non-goals

Kept out on purpose, so scope stays honest:

- Root / system-wide daemons (LaunchDaemons, system systemd) — `every` is a
  personal, user-space tool.
- A cron-expression parser as the primary syntax — the whole point is *not*
  being cron.
- A GUI or menu-bar app — `every list --json` is the integration point; someone
  else can build the UI.
- Remote/cloud scheduling — this is for your own machine.
