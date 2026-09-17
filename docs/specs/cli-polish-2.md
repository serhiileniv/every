# CLI polish, round 2: following, suggesting, and reading times

## Goal

The four items round 1 deferred, plus the human/machine time leak it found and
did not fix. All four are things a person does at a prompt; none change the
`--json` contract.

## Non-goals

- Cron expressions, bounded intervals — ROADMAP items, unrelated.
- `every edit` — a ROADMAP item, unrelated.

## Current state

- **No `every log -f`.** After "did it run?" the next question is "what is it
  doing now", and the answer is `tail -f ~/.local/share/every/logs/<n>.log`
  typed by hand. Round 1 made the swallowed `-f` an error, which is honest but
  still a dead end.
- **No suggestion on a typo.** `every lst` prints the generic "isn't a command"
  block. `git`, `cargo` and `gh` all suggest.
- **`list` prints absolute times** (`14 Sep 10:00`) for two columns that answer
  relative questions: did it run recently, is the next run soon.
- **`inspect` prints RFC 3339 in its HUMAN output** (`2026-09-14T10:00:00+03:00`)
  while `list` prints `14 Sep 10:00`. Reported in the round 1 audit, not fixed.
- **No way to filter `list`.** `--json | jq` covers scripts; the table has
  nothing.

## Design

### 1. `every log -f` / `--follow`

Print the tail, then poll the live log for growth and write what appears.

Polling, not fsevents/inotify: the file is appended by a different process, the
platforms are three, and a 200 ms poll is indistinguishable from instant for a
human reading output. `tail -f` itself polls on most platforms.

- Rotation: if the live log's size shrinks or its inode changes between polls,
  reopen from zero — a rotation happened mid-follow and the bytes after it are
  the ones wanted. Reuses `logPath`/`rotatedLogPath`.
- A log that does not exist yet is not an error under `-f`: the task may simply
  not have run. Wait for it to appear. Without `-f` the `no_logs` error stands.
- `-f` with `--json` is a usage error. A stream of prose is not the log JSON
  shape, and pretending otherwise would break the schema contract.
- SIGINT exits 0. Following is not a failure, and a script doing
  `timeout 5 every log x -f` should not see 130 as an error.

### 2. Did you mean

On the "isn't a command" path only, when the typed token is within Levenshtein
distance 2 of a command AND does not parse as a schedule, add one line:

```
every: "lst" isn't a command, and there's no `--` before a task.
  did you mean:  every list
  to schedule:   every <when> -- <command>   (e.g. every day 9am -- brew update)
  commands:      list, log, run, pause, resume, rm, doctor, version
```

The schedule guard is load-bearing: `every 15m` must not be met with "did you
mean list". Distance 2 on short words over-matches (`rm`/`run` are distance 2),
so the threshold scales: distance 1 for tokens under 5 characters.

### 3. Relative times in `list`

LAST and NEXT become relative — `2h ago`, `in 18h`, `just now`, `now`. That is
the question each column answers; the tool exists to answer "did it run" and
"when next", both of which are relative.

Absolute times remain available and unchanged in `every inspect` and in every
`--json` payload, which is where a precise answer belongs.

Format: `<n>s/m/h/d` with one unit, largest that fits, no decimals. Over 90
days, fall back to the absolute date — "in 217d" is not useful.

### 4. `every list --failing`

Show only tasks whose status is not `ok`: FAIL(n), unscheduled, invalid,
missed. Not paused (a paused task is not failing) and not `·` (never run is not
a failure). Composes with `--json`. Exits 0 whether or not any matched —
`--failing` asks a question, and "none" is a valid answer, not an error.

### 5. Per-command help

Added after the first four, when the non-goal stopped being defensible: round 1
had made `every log --help` stop being a bug (it used to look up a task named
`--help`) but it still answered with the whole manual, which looks like it
worked and leaves the reader hunting.

`every help <command>` and `every <command> --help` print the same page:
synopsis, flags, worked examples. `every help` alone stays the index — "what
can this do" and "how do I use this one" are different questions. Aliases
resolve (`every help ls` → list's page). One page, `schedules`, is not a
command: the DSL is the part people actually need to look up.

An unknown topic is a usage error with a suggestion, not the full help.

Pages live beside the command list rather than being generated from it -- there
is nothing in the dispatcher to generate a worked example from. Three tests
keep them honest: every command has a page, every page names a real command,
and every example in a page is an invocation the parser accepts.

### 6. `inspect` human output uses human times

`last run` and `next run` render like `list`'s absolute form
(`14 Sep 10:00`), plus the relative form in parentheses. `--json` keeps
RFC 3339 untouched.

## Acceptance criteria

- `every log <name> -f` streams appended output; survives a rotation mid-follow;
  waits for a log that does not exist yet; Ctrl-C exits 0.
- `every log <name> -f --json` → exit 64.
- `every lst` suggests `list`; `every 15m` suggests nothing; `every rm` (a real
  command) is unaffected.
- `every list` shows `2h ago` / `in 18h`; `--json` still carries RFC 3339.
- `every list --failing` filters; exits 0 on no matches.
- `every inspect <name>` shows no RFC 3339 in its human output.
- `every help log` and `every log --help` print log's page; `every help` prints
  the index; `every help lst` is exit 64 with a suggestion.
- `go test ./...` green; surface fixture regenerated with the diff read.

## Open questions

None.
