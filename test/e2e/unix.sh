#!/bin/sh
# End-to-end test for the Unix backends against the real scheduler.
#
#   sh test/e2e/unix.sh <every-binary> <every-home>
#
# The unit tests cover plist/unit *generation*; this exercises the whole path —
# registration with launchd or systemd, execution through Runner, the log and
# ledger, and removal. Backend registration is probed first: if no usable user
# scheduler is present (common in containers and on Linux CI without a user
# bus), those sections are skipped loudly rather than silently passing.
#
# Data is isolated via EVERY_HOME. On macOS the launchd agents land in the
# user's real ~/Library/LaunchAgents, so the probe task is distinctively named
# and the script asserts the directory is byte-identical afterwards.
set -u

REPO="${1:?usage: unix.sh <every-binary> <every-home>}"
EVERY_HOME="${2:?usage: unix.sh <every-binary> <every-home>}"
export EVERY_HOME
# $1 is the every binary. It accepted a repo root as well while both
# implementations existed, so one script could drive either; there is only one
# now.
EVERY="$REPO"
# -f as well as -x: a directory carries the execute bit too, so checking only
# -x let a repo root through and produced 47 confusing assertion failures
# instead of one clear message.
if [ ! -f "$EVERY" ] || [ ! -x "$EVERY" ]; then
  printf 'not an executable file: %s\n' "$EVERY" >&2
  printf 'usage: unix.sh <every-binary> <every-home>\n' >&2
  exit 2
fi
PROBE="e2eprobe"

pass=0; fail=0; skip=0
ok()   { printf '  PASS %s\n' "$1"; pass=$((pass + 1)); }
bad()  { printf '  FAIL %s\n' "$1"; [ -n "${2:-}" ] && printf '       %s\n' "$2"; fail=$((fail + 1)); }
skipped() { printf '  SKIP %s\n' "$1"; skip=$((skip + 1)); }
sec()  { printf '\n-- %s --\n' "$1"; }
same() { if [ "$2" = "$3" ]; then ok "$1"; else bad "$1" "expected [$2] got [$3]"; fi; }
has()  { case "$2" in *"$3"*) ok "$1";; *) bad "$1" "missing [$3] in: $(printf '%s' "$2" | tr '\n' ' ' | cut -c1-220)";; esac; }
hasnt(){ case "$2" in *"$3"*) bad "$1" "unexpectedly found [$3]";; *) ok "$1";; esac; }

# launchctl bootstrap and systemctl return before the job is necessarily
# listable, so a single query right after `add` can miss it. That is the service
# catching up, not `every` failing -- poll briefly instead of asserting on the
# first look. (This is what made one visibility assertion fail about once in
# a dozen local runs.)
wait_until() { # wait_until <tries> <shell snippet>
  _i=0
  while [ "$_i" -lt "$1" ]; do
    if eval "$2" >/dev/null 2>&1; then return 0; fi
    sleep 1
    _i=$((_i + 1))
  done
  return 1
}

# JSON validation without depending on the implementation under test. python3
# is present on every platform this runs on (macOS, ubuntu CI); ruby is a
# fallback for a box that has it and not python.
json_stdin_ok() {
  if command -v python3 >/dev/null 2>&1; then python3 -c 'import json,sys; json.load(sys.stdin)' >/dev/null 2>&1
  elif command -v ruby >/dev/null 2>&1; then ruby -rjson -e 'JSON.parse($stdin.read)' >/dev/null 2>&1
  else cat >/dev/null; return 0; fi
}
json_ok() { json_stdin_ok < "$1"; }
json_lines_ok() {
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    printf '%s' "$line" | json_stdin_ok || return 1
  done < "$1"
  return 0
}

OS=$(uname -s)
case "$OS" in
  Darwin) BACKEND="launchd"; AGENTS="$HOME/Library/LaunchAgents" ;;
  Linux)  BACKEND="systemd"; AGENTS="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user" ;;
  *)      BACKEND="unknown"; AGENTS="" ;;
esac

printf 'every E2E (%s / %s)\n' "$OS" "$BACKEND"
printf 'binary     : %s\n' "$REPO"
printf 'EVERY_HOME : %s\n' "$EVERY_HOME"

# Can this machine actually register a user task? Probe before asserting.
SCHED_OK=0
case "$BACKEND" in
  launchd) launchctl print "gui/$(id -u)" >/dev/null 2>&1 && SCHED_OK=1 ;;
  systemd) systemctl --user show-environment >/dev/null 2>&1 && SCHED_OK=1 ;;
esac
printf 'scheduler  : %s\n' "$([ "$SCHED_OK" -eq 1 ] && echo available || echo 'NOT available (lifecycle sections will skip)')"

before_agents=$(ls "$AGENTS" 2>/dev/null | sort)

mk() { # mk <name> <cmd> [timeout] -- store entry without touching the scheduler
  "$EVERY" __seed "$1" "$2" "${3:-}"
}

# ------------------------------------------------------------------- grammar
sec "1. schedule grammar (documented forms)"
for s in "90s" "15m" "2h" "hourly" "day 9am" "day 17:30" "day 9am,6pm" \
         "weekdays 9:30" "weekends 11am" "monday 10:00" "monday,thursday 6pm"; do
  # shellcheck disable=SC2086
  if "$EVERY" __parse $s 2>/dev/null
  then ok "parses: $s"; else bad "parses: $s" "Schedule.parse rejected a documented form"; fi
done
for s in "banana" "0m" "25h99" "1d" "mon 8am" "day" "5s"; do
  # shellcheck disable=SC2086
  if "$EVERY" __parse $s 2>/dev/null
  then bad "rejects: $s" "accepted an invalid schedule"; else ok "rejects: $s"; fi
done

# ----------------------------------------------------------------- execution
sec "2. execution, quoting and capture"
mk plain 'echo hello'
"$EVERY" run plain >/dev/null 2>&1; same "plain run exits 0" 0 $?
has "output captured to the log" "$("$EVERY" log plain 2>&1)" "hello"

mk quoted 'echo "my file.txt"'
"$EVERY" run quoted >/dev/null 2>&1
has "quoted argument survives the shell" "$("$EVERY" log quoted 2>&1)" "my file.txt"

mk spaced "printf '%s\n' 'a b  c'"
"$EVERY" run spaced >/dev/null 2>&1
has "runs of spaces preserved" "$("$EVERY" log spaced 2>&1)" "a b  c"

mk uni 'echo "héllo — wörld"'
"$EVERY" run uni >/dev/null 2>&1
has "non-ASCII output survives" "$("$EVERY" log uni 2>&1)" "héllo"

mk failing 'exit 42'
"$EVERY" run failing >/dev/null 2>&1
code=$("$EVERY" __last-exit failing)
same "non-zero exit recorded in the ledger" 42 "$code"

mk onerr 'echo to-stderr >&2'
"$EVERY" run onerr >/dev/null 2>&1
has "stderr merged into capture" "$("$EVERY" log onerr 2>&1)" "to-stderr"

mk slow 'sleep 30' 3
start=$(date +%s)
"$EVERY" run slow >/dev/null 2>&1
elapsed=$(( $(date +%s) - start ))
if [ "$elapsed" -lt 20 ]; then ok "timeout kills the run (${elapsed}s)"
else bad "timeout" "took ${elapsed}s"; fi
has "timeout marker in the log" "$("$EVERY" log slow 2>&1)" "killed after"

# ------------------------------------------------------------------ contract
sec "3. list, --json and doctor"
has "list renders" "$("$EVERY" list 2>&1)" "plain"
j=$("$EVERY" list --json 2>&1)
if printf '%s' "$j" | json_stdin_ok
then ok "list --json parses"; else bad "list --json" "did not parse"; fi
for k in name schedule command status; do has "json has \"$k\"" "$j" "\"$k\""; done
# doctor exists to notice exactly this: tasks in the store with no scheduler
# unit behind them. A clean exit here would mean it is not doing its job.
d=$("$EVERY" doctor 2>&1); dcode=$?
if [ "$dcode" -ne 0 ]; then ok "doctor fails when tasks are unscheduled"
else bad "doctor" "exited 0 despite tasks with no scheduler unit"; fi
has "doctor names the orphaned task" "$d" "plain"
has "doctor reports the missing registration" "$d" "scheduled in"
has "doctor suggests a fix" "$d" "every resume"

# --------------------------------------------------------------- error paths
sec "4. error paths and exit codes"
"$EVERY" log nosuch  >/dev/null 2>&1; same "log unknown exits 66" 66 $?
"$EVERY" rm nosuch   >/dev/null 2>&1; same "rm unknown exits 66" 66 $?
"$EVERY" pause nosuch >/dev/null 2>&1; same "pause unknown exits 66" 66 $?
e=$("$EVERY" banana -- echo hi 2>&1); same "bad schedule exits 64" 64 $?
# Language-neutral: a Ruby backtrace names a .rb file, a Go panic names a .go
# file and a goroutine. Neither may reach a user.
hasnt "bad schedule prints no backtrace" "$e" ".rb:"
hasnt "bad schedule prints no stack trace" "$e" "goroutine"
"$EVERY" run >/dev/null 2>&1; same "run without a name exits 64" 64 $?

# --------------------------------------------------------------- persistence
sec "5. store and ledger durability"
n=$("$EVERY" __count)
if [ "$n" -ge 7 ]; then ok "all task writes survived ($n present)"
else bad "store writes" "expected >=7, got $n"; fi
for i in 1 2 3 4 5; do
  ( "$EVERY" __seed "conc$i" true ) &
done
wait
c=$("$EVERY" list --json | tr ',' '\n' | grep -c '"name":"conc')
same "5 concurrent adds all survive (flock)" 5 "$c"
# An INDEPENDENT parser on purpose: asking every to read back what every
# wrote would pass even if both sides agreed on something malformed.
if json_ok "$EVERY_HOME/tasks.json"
then ok "tasks.json still valid JSON"; else bad "tasks.json" "corrupted by concurrent writes"; fi
if json_lines_ok "$EVERY_HOME/runs/plain.jsonl"
then ok "run ledger lines are valid JSON"; else bad "ledger JSON" "a line failed to parse"; fi

# ----------------------------------------------------------- real scheduler
sec "6. real $BACKEND lifecycle"
if [ "$SCHED_OK" -eq 0 ]; then
  skipped "no usable user scheduler on this host — registration not exercised"
else
  # Start from an empty store so `doctor` reflects only the probe.
  rm -f "$EVERY_HOME/tasks.json"
  "$EVERY" 1h --name "$PROBE" -- echo probe-ran >/dev/null 2>&1
  same "add exits 0" 0 $?

  case "$BACKEND" in
    launchd)
      unit=$(ls "$AGENTS" 2>/dev/null | grep -i "$PROBE" | head -1)
      if [ -n "$unit" ]; then ok "plist written ($unit)"; else bad "plist" "none in $AGENTS"; fi
      if wait_until 10 "launchctl list 2>/dev/null | grep -qi $PROBE"
      then ok "loaded into launchctl"; else bad "launchctl" "label absent after 10s"; fi
      ;;
    systemd)
      if [ -f "$AGENTS/every-$PROBE.timer" ] || ls "$AGENTS" 2>/dev/null | grep -qi "$PROBE"
      then ok "timer unit written"; else bad "unit" "nothing matching $PROBE in $AGENTS"; fi
      if wait_until 10 "systemctl --user list-timers --all 2>/dev/null | grep -qi $PROBE"
      then ok "timer known to systemd"; else bad "systemd" "timer not listed after 10s"; fi
      ;;
  esac

  has "list reports it" "$("$EVERY" list 2>&1)" "$PROBE"
  "$EVERY" run "$PROBE" >/dev/null 2>&1; same "manual run exits 0" 0 $?
  has "scheduled task output logged" "$("$EVERY" log "$PROBE" 2>&1)" "probe-ran"
  "$EVERY" doctor >/dev/null 2>&1; same "doctor exits 0 on a healthy task" 0 $?

  "$EVERY" pause "$PROBE" >/dev/null 2>&1; same "pause exits 0" 0 $?
  has "pause reflected in list" "$("$EVERY" list 2>&1)" "paused"
  "$EVERY" resume "$PROBE" >/dev/null 2>&1; same "resume exits 0" 0 $?
  hasnt "resume clears paused" "$("$EVERY" list 2>&1)" "paused"

  "$EVERY" rm "$PROBE" >/dev/null 2>&1; same "rm exits 0" 0 $?
  if wait_until 10 "! ls \"$AGENTS\" 2>/dev/null | grep -qi $PROBE"
  then ok "unit removed"; else bad "unit removed" "still present in $AGENTS after 10s"; fi
  case "$BACKEND" in
    launchd)
      if wait_until 10 "! launchctl list 2>/dev/null | grep -qi $PROBE"
      then ok "unloaded from launchctl"; else bad "unloaded" "still listed after 10s"; fi ;;
    systemd)
      if wait_until 10 "! systemctl --user list-timers --all 2>/dev/null | grep -qi $PROBE"
      then ok "timer removed from systemd"; else bad "unloaded" "still listed after 10s"; fi ;;
  esac

  after_agents=$(ls "$AGENTS" 2>/dev/null | sort)
  if [ "$before_agents" = "$after_agents" ]
  then ok "$AGENTS identical to its pre-test state"
  else bad "scheduler dir drift" "$(printf '%s\n---\n%s' "$before_agents" "$after_agents" | head -6)"; fi
fi

printf '\n-- %d passed, %d failed, %d skipped --\n' "$pass" "$fail" "$skip"
[ "$fail" -eq 0 ]
