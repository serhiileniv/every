# CLI polish: strict flags, honest streams, complete docs

## Goal

Close the gap between what `every` does and what it says it does, in three
places that drifted apart: argument parsing (accepts typos silently), output
streams (chrome on stdout), and the docs (README stopped at 0.3).

## Non-goals

- `every log -f`, relative times in `list`, "did you mean", `list --failing`,
  per-command help pages. Real wants, separate change.
- Per-command help pages. `every help <command>` prints the full help.

## Current state

- `every list --jsn` prints the human table and exits 0. Same for
  `--verbose`, `inspect x --oops`, `log x -n abc` (silently uses n=40).
  A script that typos `--json` gets table output and success.
- `every log --help` is parsed as a task named `--help`.
- `-v`/`-V` fall through to "isn't a command"; only `-h` works.
- Colour is decided only by isatty + `NO_COLOR`. `every list | less -R` and
  demo capture cannot get colour.
- The migration notice (`cli.go:774`) and the unscheduled hint
  (`list.go:241`) print to **stdout**, so `every list > file` captures them.
  `--json` special-cases them away, which is the workaround, not the fix.
- README documents 7 of 15 commands; `--on-fail` appears nowhere in it;
  `--json` is shown only on `list`; `EVERY_POWERSHELL` is undocumented.
- `every help` omits exit codes, the command aliases, and `-h`/`-V`.
- Completions list 9 commands; `set`, `inspect`, `exists`, `schema` missing.

## Design

### 1. Unknown flags and stray positionals are usage errors

One helper in `args.go`:

```go
func rejectUnknownFlags(tokens []string) error
```

Called by every subcommand *after* its known flags are extracted. Any
remaining token with a `-` prefix yields `CodeUsage` / exit 64:

```
every: unknown flag "--jsn"
see: every help
```

Known-flag sets per command are listed in `surface_cases.go` coverage. `--`
still ends flag parsing, so a command's own flags after `--` are untouched.

`-n` gains real validation: a non-integer, zero, or negative value is
`CodeUsage`, not a silent fallback to 40.

A matching `rejectExtraArgs` closes the positional half: `every list extra`,
`every version extra` and `every rm backup extra` were tolerated from 0.4
through 0.5 and exited 0. Commands taking one name reject a second; commands
taking none reject any. `help` is the single carve-out -- `every help log` is
a request for help, and erroring on the way to showing it is the wrong trade.

This was a non-goal in the first draft of this spec, on the grounds that the
break was wider than the change earned. Revisited during implementation: the
tolerance is the same defect as the flag tolerance, and leaving one half fixed
would have made the rule harder to state than to follow.

### 2. `-h` / `--help` anywhere

`-h` and `--help` before `--` print the help text and exit 0, from any
subcommand. Checked *before* unknown-flag rejection, so `every log --help`
helps instead of erroring.

### 3. `-V` / `-v` → version

Aliases of `version`. `-v` is deliberately version, not verbose: `every` has
no verbose mode to collide with.

### 4. `--color=auto|always|never`

Explicit override of the isatty decision, plus `CLICOLOR_FORCE`. Precedence,
highest first: `--color` flag, `NO_COLOR`, `CLICOLOR_FORCE`, `TERM=dumb`,
isatty. `NO_COLOR` stays above `CLICOLOR_FORCE` because the spec says an
opt-out beats an opt-in.

### 5. stdout is data, stderr is chatter

The migration notice and the unscheduled hint move to stderr,
unconditionally. The `--json` suppression gate is then deleted rather than
kept — it existed only to protect stdout, and stderr needs no protection.

`every log` output and `every run` command output stay on stdout: that is
the data those commands exist to produce.

### 6. Docs

- **`every help`** gains: exit codes, the aliases (`ls`, `show`, `remove`),
  `-h`/`-V`, `--color`, and the environment variables.
- **README** gains the eight undocumented commands, `--on-fail`, `--json` as
  a whole-CLI fact rather than a `list` footnote, `EVERY_POWERSHELL`, and a
  sample `doctor` run. Fine print splits into "How it runs" (semantics) and
  a collapsed "Platform notes" (launchd/systemd/Task Scheduler detail the
  man page already carries). Nav reordered to match page order.
- **macOS shell**: the README claims runs go through "`zsh -l` / `bash -l`".
  `LoginShellFor` hardcodes `/bin/zsh -lc` on macOS and ignores `$SHELL`.
  Say so.
- **ROADMAP.md** heading `Known gaps in v0.3` → the shipping version.
- **Completions** get the four missing commands, the flags, and the schedule
  keywords, in all three shells.

### 7. Drift tests

`docs_test.go` gains one test: every `case` in `dispatch` (minus the `__`
hooks) must appear in `every help`, the man page, and all three completion
files. This class of staleness is what produced half of this spec.

## Acceptance criteria

- `every list --jsn` → exit 64, `unknown flag "--jsn"` on stderr.
- `every list extra`, `every rm backup extra` → exit 64; `every help log` → 0.
- `every log x -n abc` → exit 64.
- `every log --help`, `every list -h` → help, exit 0.
- `every -V` → version, exit 0.
- `every list --color=always | cat` → ANSI present; `--color=never` on a tty
  → absent.
- `every list > f` with a repaired unit → `f` holds only the table.
- The new drift test fails if a command is added to `dispatch` alone.
- `go test ./...` green; surface fixture regenerated with the diff read.

## Open questions

None. The deferred items in Non-goals are tracked in ROADMAP.md.
