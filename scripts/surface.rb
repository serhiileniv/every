#!/usr/bin/env ruby
# Freeze the user-visible CLI surface: every schedule form the DSL accepts and
# rejects, every subcommand and flag spelling, every error path, and the exact
# text and exit code each produces.
#
#   ruby scripts/surface.rb
#
# The Go port replays the identical table against the built binary and diffs
# all three streams. A syntax change becomes a build failure, not a bug report.
#
# Deliberately excluded: anything that registers a task with a real scheduler.
# Those paths are covered by test/e2e/{unix.sh,windows.ps1} against the live
# launchd/systemd/Task Scheduler, which is where they belong -- a golden table
# that shells out to launchctl would be neither hermetic nor portable. What is
# here is everything a user types and everything `every` says back before any
# backend is touched.

ENV["TZ"] = "America/New_York"

require "fileutils"
require "json"
require "tmpdir"

ROOT = File.expand_path("..", __dir__)
OUT  = File.join(ROOT, "testdata", "golden", "cli")

$LOAD_PATH.unshift File.join(ROOT, "lib")
require "every"

# ---------------------------------------------------------------------------
# Part 1 -- the schedule grammar, captured from the parser directly.
#
# Pure and side-effect free, so every accepted form and every rejection message
# is pinned without registering anything. This IS the syntax: `every <this>`
# either works or fails with exactly this text.
# ---------------------------------------------------------------------------

ACCEPT = [
  # intervals
  %w[15m], %w[90s], %w[2h], %w[10s], %w[hourly], %w[3600s],
  # daily, single and multiple times
  ["day", "9am"], ["day", "17:30"], ["day", "9am,6pm"], ["day", "12am"],
  ["day", "12pm"], ["day", "9"], ["day", "09:05"], ["daily", "7am"],
  # day sets
  ["weekdays", "9:30"], ["weekends", "11am"],
  # named weekdays
  ["monday", "10:00"], ["sunday", "12am"], ["saturday", "11pm"],
  ["monday,thursday", "6pm"], ["monday,monday", "6pm"],
  # the quirk: days use split(","), which drops a trailing empty field
  ["monday,", "10:00"],
  # dedup: repeated times collapse
  ["day", "9am,9am"],
  # case: the first token is downcased before dispatch, so the unit is too
  %w[HOURLY], ["MONDAY", "10:00"], %w[5M],
].freeze

REJECT = [
  [], %w[banana], %w[0m], %w[0s], %w[9s], %w[25h99], %w[1d], %w[15], %w[m],
  %w[-5m], %w[15m extra],
  ["mon", "8am"], ["day", "5s"], ["day", "13pm"], ["day", "0am"],
  ["day", "9:5"], ["day", "24:00"], ["day", "9:60"], ["day", "25"],
  ["day", ""], ["day", ","], ["day", "9am,"], ["day", "9am,,6pm"],
  [",monday", "10:00"], ["monday,banana", "10:00"], ["", "9am"],
  ["day", "9am", "6pm"],
].freeze

def probe(tokens)
  s = Every::Schedule.parse(tokens)
  { "tokens" => tokens, "ok" => true, "to_h" => s.to_h }
rescue ArgumentError => e
  { "tokens" => tokens, "ok" => false, "error" => e.message }
end

FileUtils.rm_rf(OUT)
FileUtils.mkdir_p(OUT)

grammar = (ACCEPT + REJECT).map { |t| probe(t) }
File.write File.join(OUT, "grammar.json"), JSON.pretty_generate(grammar) + "\n"

bad = grammar.select { |g| ACCEPT.include?(g["tokens"]) && !g["ok"] } +
      grammar.select { |g| REJECT.include?(g["tokens"]) && g["ok"] }
abort "surface: table disagrees with the parser: #{bad.inspect}" unless bad.empty?

puts "grammar: #{ACCEPT.length} accepted, #{REJECT.length} rejected"

# ---------------------------------------------------------------------------
# Part 2 -- the CLI, captured by actually running it.
#
# Each case runs the real launcher in a scratch EVERY_HOME and records
# (stdout, stderr, exit code). Only paths that never reach a backend are
# included, so this is hermetic: no launchctl, no systemctl, no schtasks.
#
# Two normalizations keep it reproducible; both are applied identically on the
# Go side:
#   the scratch home path -> $EVERY_HOME   (it is a fresh mktmpdir each run,
#                                           and `help` prints it)
#   the version string    -> $VERSION      (so a release bump is not a diff)
# ---------------------------------------------------------------------------

CASES = [
  # -- help and version, in every spelling -------------------------------
  [],                 %w[help],    %w[-h],      %w[--help],
  %w[version],        %w[--version],

  # -- usage errors: a subcommand that needs a name and did not get one ---
  %w[log], %w[rm], %w[remove], %w[pause], %w[resume], %w[run],

  # -- unknown task: exit 66 (EX_NOINPUT), not 1 -------------------------
  %w[log nosuch], %w[rm nosuch], %w[pause nosuch], %w[resume nosuch],
  %w[run nosuch],

  # -- -n accepted before and after the name, and rejected values --------
  %w[log -n 5 nosuch], %w[log nosuch -n 5], %w[log -n 0 nosuch],
  %w[log -n notanumber nosuch],

  # -- list on an empty store, both renderers ----------------------------
  %w[list], %w[ls], %w[list --json], %w[ls --json],
  # --json is deleted from anywhere in argv
  %w[list --json extra],

  # -- add: the "you forgot --" funnel -----------------------------------
  %w[frobnicate], %w[15m], ["day", "9am"], %w[15m -- ],

  # -- add: bad schedules reach the same ArgumentError funnel as part 1,
  #    but through the CLI, so this pins the "see: every help" second line
  %w[banana -- true], %w[0m -- true], ["day", "13pm", "--", "true"],

  # -- add: flag validation, both --flag value and --flag=value ----------
  %w[15m --name -- true],
  %w[15m --name --quiet -- true],
  %w[15m --name= -- true],
  %w[15m --name ... -- true],
  %w[15m --name . -- true],
  %w[15m --name .. -- true],
  %w[15m --timeout -- true],
  %w[15m --timeout 0s -- true],
  %w[15m --timeout 5x -- true],
  %w[15m --timeout=0s -- true],
  %w[15m --timeout notaduration -- true],
].freeze

def norm(s, home, version)
  s.to_s.gsub(home, "$EVERY_HOME").gsub(version, "$VERSION")
end

launcher = File.join(ROOT, "bin", "every")
version  = Every::VERSION
records  = []

Dir.mktmpdir("every-surface") do |home|
  CASES.each do |argv|
    r_out, w_out = IO.pipe
    r_err, w_err = IO.pipe
    pid = spawn({ "EVERY_HOME" => home, "NO_COLOR" => "1", "TZ" => "America/New_York" },
                RbConfig.ruby, launcher, *argv,
                out: w_out, err: w_err, unsetenv_others: false)
    w_out.close
    w_err.close
    out = r_out.read
    err = r_err.read
    r_out.close
    r_err.close
    _, status = Process.wait2(pid)

    records << {
      "argv"   => argv,
      "exit"   => status.exitstatus,
      "stdout" => norm(out, home, version),
      "stderr" => norm(err, home, version),
    }
  end
end

File.write File.join(OUT, "cli.json"), JSON.pretty_generate(records) + "\n"
puts "cli:     #{records.length} invocations"
puts "         exit codes seen: #{records.map { |r| r['exit'] }.uniq.sort.inspect}"
