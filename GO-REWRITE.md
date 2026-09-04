# every → Go: rewrite plan

**Why:** not speed. Distribution. `every` has one dependency and it's the Ruby
runtime — deprecated on macOS, absent on Windows, apt/dnf/pacman on Linux.
Roughly half the hard logic in `install.sh` exists to find a Ruby ≥ 2.6, and the
last two shipped bugs (`every add` on Windows, the wrapper load path) were both
distribution bugs, not logic bugs. A single static binary deletes that whole
class of problem and makes the *dependencies: zero* badge literally true.

**Scope:** behavior-identical port of 1,986 lines of Ruby to Go, same CLI, same
on-disk format, same generated scheduler units. No new features. Ships as 0.4.0.

**Estimate:** ~2–3 weeks part-time. Expect ~3,000–3,500 lines of Go.

---

## The one trick that makes this verifiable

Before writing a line of Go, freeze the current output as golden files.

```sh
# scripts/golden.rb — run under the Ruby tree, once
# For a matrix of schedules, dump what each backend generates:
#   testdata/golden/launchd/<slug>.plist
#   testdata/golden/systemd/<slug>.timer  + .service
#   testdata/golden/taskschd/<slug>.xml    (UTF-16LE + BOM, compared as bytes)
#   testdata/golden/schedule/<slug>.json   (Schedule#to_h + next_run at a fixed clock)
```

Matrix (from `schedule_test.rb` plus the doc comment in `schedule.rb`):
`15m`, `90s`, `2h`, `hourly`, `day 9am`, `day 9am,6pm`, `weekdays 9:30`,
`weekends 11am`, `monday 10:00`, `monday,thursday 10:00`, plus the legacy
`daily`/`weekly` record shapes `Schedule.from_h` still accepts.

The Go tests then assert **byte-equality** against those files. This turns "I
think I ported it right" into a build failure. It is the difference between a
two-week rewrite and a two-month one.

Pin the clock for `next_run` — inject a `now time.Time` instead of calling
`time.Now()` internally, which the Ruby already half-does (`next_run(from = Time.now)`).

---

## What the port actually buys, per file

Two modules substantially collapse. Worth knowing up front, because they're
where the Ruby is at its most defensive:

| Ruby | Go | Note |
|---|---|---|
| `runtime.rb` (65 lines) | ~20 lines | Mirrors `bin/` **and** `lib/` into the data dir for TCC-protected installs. Becomes: copy one file. |
| `Runtime.bin` Windows shim | **gone** | `bin/every.cmd` exists only so Task Scheduler doesn't pin a Ruby path. There's no Ruby path any more. |
| `RbConfig.ruby` in every unit | **gone** | plist `ProgramArguments`, systemd `ExecStart`, and the task XML all drop from `[ruby, script, run, name]` to `[every, run, name]`. |
| `install.sh` Ruby preflight | **gone** | ~30 lines, plus the whole `lib/` tree copy. |
| `Process.clock_gettime(CLOCK_MONOTONIC)` | `time.Since` | Go durations are monotonic by construction. |

---

## Layout

```
cmd/every/main.go            # arg dispatch, exit codes  (← cli.rb)
internal/paths/              # EVERY_HOME / XDG / LOCALAPPDATA  (← every.rb)
internal/schedule/           # parse, to/from JSON, next_run  (← schedule.rb)
internal/store/              # tasks.json, run ledger, flock  (← store.rb)
internal/tail/               # seek-from-end line reader      (← tail.rb)
internal/ui/                 # color + table rendering        (← color.rb, cli.rb)
internal/runner/             # exec, capture, timeout, notify (← runner.rb)
internal/backend/            # interface + platform selection (← backend.rb)
internal/backend/launchd/
internal/backend/systemd/
internal/backend/taskschd/
internal/doctor/             # (← doctor.rb)
```

Backends get build tags (`//go:build darwin`) — they're mutually exclusive and
the Windows one won't compile elsewhere. Everything else uses `runtime.GOOS`
checks so the tests still run cross-platform.

### Dependencies: keep it at zero

Everything this codebase needs is Go stdlib, including the two that look like
they'd need a crate:

- `encoding/csv` — the `schtasks /FO CSV` parsing in `windows_task_scheduler.rb`.
  Set `LazyQuotes: true` for Ruby's `liberal_parsing: true`.
- `unicode/utf16` + `encoding/binary` — the UTF-16LE + BOM the task XML needs
  (`windows_task_scheduler.rb:289`). ~6 lines, no `x/text`.
- File locking is the only real gap: `syscall.Flock` on unix, `LockFileEx` via
  `syscall` on Windows. Write it yourself in `internal/store/lock_*.go` (~40
  lines total) rather than taking `x/sys`.

**Trade-off:** staying zero-dep means no cobra/clap, so `completions/` (76
hand-written lines) stays hand-written. That's the right call — the badge is a
stated design value, and the current CLI is already a hand-rolled `switch` that
ports directly. Revisit only if the flag surface grows.

---

## Phases

### Phase 0 — scaffold + golden files (½ day)
- `go mod init github.com/serhiileniv/every`; `CGO_ENABLED=0`.
- Generate `testdata/golden/` from the Ruby tree. Commit it.
- Point `test/e2e/unix.sh` at a `$EVERY` env var instead of `$REPO/bin/every`,
  so the same script can drive either implementation. One-line change.

### Phase 1 — the pure core (2–3 days)
`paths`, `schedule`, `store`, `tail`, `ui`. No OS interaction, all
table-testable against the golden files. Port the assertions from
`schedule_test.rb`, `store_test.rb`, `xdg_test.rb` as Go table tests.

Watch for:
- `shift_days` is deliberately DST-safe (calendar-day arithmetic, not +86400s).
  Go: `t.AddDate(0, 0, n)` has the same property. Keep the comment.
- `Store.last_run` scans backwards past a torn final JSONL line, growing the
  read window 4×. Port the algorithm, not a naive `ReadAll`.
- Atomic save = temp file + `fsync` + rename. On Windows `os.Rename` over an
  existing file works (unlike POSIX assumptions in some code) — but keep the
  separate branch, and keep it erroring rather than force-replacing.
- `sanitize` / `derive_name` collision suffixes are user-visible. Golden-test them.

### Phase 2 — runner (2–3 days) ⚠️ hardest file
`runner.rb:capture` is the highest-risk port in the project. Four things happen
at once and each has bitten this codebase before:

1. **Process-group kill.** `spawn(pgroup: true)` → `SysProcAttr{Setpgid: true}`,
   then `syscall.Kill(-cmd.Process.Pid, SIGTERM)`, 300ms, `SIGKILL`. On Windows
   keep the `taskkill /T /F` shell-out for now; a Job Object is a later refactor.
2. **Timeout must wrap the wait, not just the read.** The Ruby comment is
   explicit: a command that closes stdout early but keeps running still has to
   die at the deadline. Do **not** reach for `exec.CommandContext` alone — it
   kills only the direct child. Wire the deadline to the process-group kill.
3. **Bounded output.** First 32 KB + last 32 KB with a byte count in between.
   Port the head/tail ring as-is; it's already correct and non-obvious.
4. **Windows command quoting.** The temp `.cmd`/`.ps1` script trick exists
   because passing the command as a final argv element adds an escaping layer.
   Go's `exec` has its *own* Windows quoting rules — keep the temp-script
   approach, don't try to be clever.

Exit code mapping is a contract: `124` timeout, `128+signum` signalled,
otherwise the child's code. Test each.

### Phase 3 — backends, one OS at a time (3–4 days)
Order: **macOS → Linux → Windows**. macOS is the largest user base and has the
best existing e2e coverage.

Each backend is done when: golden files match byte-for-byte, *and*
`test/e2e/unix.sh` passes against the Go binary on real launchd/systemd.

- launchd: plist is string-built with three XML escapes. Keep it string-built —
  `encoding/xml` will reorder and reformat and break golden comparison.
- systemd: `test/systemd_calendar_check.rb` validates every generated
  `OnCalendar=` against real `systemd-analyze`. Port it as a Go test that
  shells out to `systemd-analyze calendar`. Don't drop this — it's cheap and it
  catches the errors that only appear on a user's machine.
- Task Scheduler: the UTF-16LE + BOM detail (`0.3.0` shipped broken without it)
  and the PowerShell `State` enum query rather than parsing localized text.
  Both are load-bearing; both are commented in the Ruby. Carry the comments over.

### Phase 4 — CLI + doctor (1–2 days)
Direct port of the `switch`. Preserve exactly:
- exit codes `0 / 64 / 66 / 1` (sysexits),
- `--json` output shape (it's an API — someone scripts against it),
- the table column widths and the `unscheduled` hint line,
- `build_record`'s rescue: one corrupt task shows as `invalid`, never aborts `list`.

`doctor` is where Go's error verbosity will hurt most — it's 156 lines of
"check, explain in plain language". Define one `check(label string, ok bool, hint string)`
helper up front and it stays about the same size.

### Phase 5 — distribution + migration (2–3 days)

**GoReleaser.** Matrix: `darwin/{amd64,arm64}` (+ universal via lipo),
`linux/{amd64,arm64}`, `windows/{amd64,arm64}`. It also emits checksums, the
GitHub release, and — the part that matters — auto-PRs the Homebrew tap formula,
which is currently a manual out-of-repo step on every release.

**Rewrite `install.sh`.** It goes from ~430 lines to maybe 120: detect os/arch,
download one tarball, place binary + man + completions. Delete the Ruby
preflight, the `lib/` tree copy, and the wrapper. Keep the uninstall manifest
and the unwritable-prefix error path — both are tested and both are good.

**⚠️ The migration nobody will think of.** Existing users have plist/unit files
on disk that say `[/usr/bin/ruby, /somewhere/bin/every, run, foo]`. After they
upgrade, that Ruby path may not exist — and if it does, it points at a `lib/`
tree the installer just deleted. **Every one of their scheduled tasks silently
stops firing.**

So: on first run, detect any unit whose program arguments reference a Ruby or a
path under the old install, and rewrite it to invoke the binary directly.
Cheapest correct place is a `migrateUnits()` called from `list`, `doctor`, and
`run`. Make `doctor` report it loudly. **Write the e2e test for this before the
code:** install 0.3.1, add three tasks, upgrade in place, assert all three still
fire.

### Phase 6 — ship (1 day)
- `release-verify.yml` stays exactly as-is in spirit — it already proves the
  *published* artifact works, which is the lesson 0.3.0 taught. Just drop the
  `setup-ruby` steps.
- CHANGELOG entry for 0.4.0 leading with "no runtime required".
- README: the *dependencies: zero* badge is now literal. Say so.

---

## Test strategy

| Layer | Fate |
|---|---|
| `test/e2e/unix.sh` (228 lines) | **Keep verbatim.** Shell driving the CLI externally — already language-agnostic. One-line path change. This is the safety net. |
| `test/e2e/windows.ps1` (301) | **Keep verbatim.** Same reason. |
| `test/*_test.rb` (895 lines) | **Do not port the code — port the assertions.** They test Ruby internals; the cases they encode (schedule matrix, CSV shapes, XDG precedence, torn-ledger recovery) are the actual value. |
| `systemd_calendar_check.rb` | Port to a Go test shelling out to `systemd-analyze`. |

Rule for the whole rewrite: **the Ruby tree stays on `main` until the Go tree
passes the same e2e scripts on all three platforms.** Develop on a `go` branch.
No big-bang deletion.

---

## What NOT to do

- **No new features.** Not one. Every rewrite dies from "while I'm in here."
  `ROADMAP.md` items wait for 0.5.0.
- **Don't reformat the generated output.** Byte-identical plists/units mean an
  upgrade is a no-op for a user's existing tasks. Restructure later, separately.
- **Don't drop the comments.** The Ruby is unusually well-commented and most of
  those comments encode a bug someone already paid for — TCC folders, UTF-16
  BOMs, `ECHO ON` leaking into logs, monotonic clocks, `launchctl bootstrap`
  falling back to `load -w`. Those facts are the real asset here; the code is
  the cheap part. Carry every one across.
- **Don't take a CLI framework** unless you decide the zero-dep badge isn't
  worth 76 lines of completions.

---

## Order of operations, condensed

1. Golden files from Ruby. Commit.
2. `go` branch. Core packages green against golden.
3. Runner green, including timeout + process-tree kill.
4. macOS backend → e2e passes → Linux → Windows.
5. CLI + doctor.
6. Unit migration + its e2e test.
7. GoReleaser, new `install.sh`, `release-verify` green.
8. Merge, tag 0.4.0.
