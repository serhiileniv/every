#!/usr/bin/env ruby
# Freeze the Ruby implementation's generated output as golden files.
#
#   ruby scripts/golden.rb
#
# The Go port asserts byte-equality against these. That is what turns "I think
# I ported it right" into a build failure.
#
# Two substitutions make the output deterministic and comparable across the
# language boundary:
#
#   __RUBY__      replaces RbConfig.ruby -- the argv element that DISAPPEARS in
#                 the port. The Go test deletes it from the golden before
#                 comparing, which is the one sanctioned difference.
#   __LAUNCHER__  replaces Runtime.bin -- the program path, which survives but
#                 is install-specific. Stubbed on both sides rather than
#                 compared.
#   FIXED_NOW     replaces Time.now. next_run and the Task Scheduler's
#                 StartBoundary are clock-dependent; the Go side injects the
#                 same instant.
#
# Run from the repo root, under the Ruby tree, with a scratch EVERY_HOME so
# nothing touches the developer's real store.

# Pinned BEFORE anything constructs a Time: FIXED_NOW below is a local-time
# literal, so a TZ set later would leave it in the developer's zone while
# next_run computed in this one. iso8601 offsets, next_run and the Task
# Scheduler's StartBoundary are all local-time. America/New_York is chosen
# deliberately -- it observes DST, so the fixtures exercise a non-UTC offset and
# the DST-edge tests in internal/schedule can reuse the zone.
ENV["TZ"] = "America/New_York"

require "fileutils"
require "json"
require "time"

ROOT = File.expand_path("..", __dir__)
OUT  = File.join(ROOT, "testdata", "golden")

# A fixed local instant for every clock-dependent value. Chosen mid-week
# (Wednesday) and mid-morning so "next occurrence" lands on a different day for
# some entries and the same day for others -- both branches of next_for_entry
# get exercised. TZ is pinned by the caller (see the header of the Go test).
FIXED_NOW = Time.new(2026, 9, 2, 10, 30, 0)

LAUNCHER = "__LAUNCHER__".freeze
# The interpreter argv element that the port drops entirely. Kept distinct from
# LAUNCHER so the Go test can delete exactly this one and diff the rest.
RUBY_BIN = "__RUBY__".freeze

# The schedule matrix: every form the DSL accepts, from schedule_test.rb plus
# the doc comment at the top of schedule.rb.
MATRIX = {
  "15m"                   => %w[15m],
  "90s"                   => %w[90s],
  "2h"                    => %w[2h],
  "hourly"                => %w[hourly],
  "day-9am"               => ["day", "9am"],
  "day-1730"              => ["day", "17:30"],
  "day-9am-6pm"           => ["day", "9am,6pm"],
  "weekdays-930"          => ["weekdays", "9:30"],
  "weekends-11am"         => ["weekends", "11am"],
  "monday-1000"           => ["monday", "10:00"],
  "monday-thursday-1000"  => ["monday,thursday", "10:00"],
  "sunday-12am"           => ["sunday", "12am"],
  "day-12pm"              => ["day", "12pm"],
}.freeze

# Pre-0.2 task records that Schedule.from_h still migrates. These never come
# from parse(), so they are expressed as raw hashes.
LEGACY = {
  "legacy-daily"    => { "raw" => "daily 9:00", "kind" => "daily",
                         "hour" => 9, "minute" => 0 },
  "legacy-weekly"   => { "raw" => "weekly", "kind" => "weekly",
                         "hour" => 18, "minute" => 30, "weekday" => 3 },
  # A legacy weekday 7 must clamp to 0 (Sunday), not blow up.
  "legacy-weekday7" => { "raw" => "weekly", "kind" => "weekly",
                         "hour" => 8, "minute" => 0, "weekday" => 7 },
}.freeze

# Pin every machine-dependent input BEFORE requiring the tree: DATA_DIR and
# friends are computed at require time, and they are interpolated into the
# plist's EnvironmentVariables, the systemd Environment= line, and the log
# redirect paths. Without this the goldens differ per developer and per CI run.
ENV["EVERY_HOME"] = "/every-golden/home"
ENV["USERNAME"]   = "goldenuser"     # Windows task Author/UserId/Principal
ENV["USERDOMAIN"] = "GOLDEN"

$LOAD_PATH.unshift File.join(ROOT, "lib")
require "every"

# ---------------------------------------------------------------------------
# Stubs. Applied before anything is generated.
# ---------------------------------------------------------------------------

# Pin the launcher on both sides of the argv change.
module Every
  module Runtime
    def self.bin
      LAUNCHER
    end
  end
end

# RbConfig.ruby is the argv[0] that disappears entirely in the port. Stub it to
# the same token so the two-element form collapses visibly in the golden.
module RbConfig
  def self.ruby
    RUBY_BIN
  end
end

# Pin the clock. Schedule#next_run already takes `from`; the Windows backend
# calls Time.now internally, so that one needs the class method stubbed.
class << Time
  alias_method :real_now, :now
  def now
    FIXED_NOW
  end
end

def write(rel, content, binary: false)
  path = File.join(OUT, rel)
  FileUtils.mkdir_p(File.dirname(path))
  if binary
    File.binwrite(path, content)
  else
    File.write(path, content)
  end
  puts "  #{rel} (#{content.bytesize} bytes)"
end

def schedules
  MATRIX.map { |slug, tokens| [slug, Every::Schedule.parse(tokens)] } +
    LEGACY.map { |slug, h| [slug, Every::Schedule.from_h(h)] }
end

# Remove only the directories this script owns. Wiping OUT wholesale would
# also delete cli/, which scripts/surface.rb writes -- the two are separate
# generators sharing one fixture tree.
%w[schedule launchd systemd taskschd store].each do |sub|
  FileUtils.rm_rf(File.join(OUT, sub))
end
puts "golden -> #{OUT}"
puts "clock  -> #{FIXED_NOW.iso8601}"
puts

# ---------------------------------------------------------------------------
# schedule/ -- to_h round-trip and next_run at the fixed clock
# ---------------------------------------------------------------------------
puts "schedule:"
schedules.each do |slug, sched|
  nr = sched.next_run(FIXED_NOW)
  doc = {
    "raw"      => sched.raw,
    "kind"     => sched.kind.to_s,
    "to_h"     => sched.to_h,
    "next_run" => nr && nr.iso8601,
    "human"    => sched.human_interval,
  }
  write "schedule/#{slug}.json", JSON.pretty_generate(doc) + "\n"
end
puts
# ---------------------------------------------------------------------------
# launchd/ -- the plist, exactly as written to ~/Library/LaunchAgents
# ---------------------------------------------------------------------------
puts "launchd:"
schedules.each do |slug, sched|
  write "launchd/#{slug}.plist", Every::Launchd.plist_xml(slug, sched)
end
puts

# ---------------------------------------------------------------------------
# systemd/ -- the .service and .timer pair
# ---------------------------------------------------------------------------
puts "systemd:"
schedules.each do |slug, sched|
  write "systemd/#{slug}.service", Every::Systemd.service_unit(slug)
  write "systemd/#{slug}.timer",   Every::Systemd.timer_unit(slug, sched)
end
puts

# ---------------------------------------------------------------------------
# taskschd/ -- Task Scheduler XML, as UTF-8 text AND as the UTF-16LE+BOM bytes
# actually handed to schtasks. 0.3.0 shipped broken without the BOM, so the
# encoded form is compared as raw bytes, not just the text.
# ---------------------------------------------------------------------------
# The Windows action has two branches. On a Mac, Every.windows? is false, so
# the Ruby always takes the Ruby-wrapper branch -- which is precisely the
# branch the port deletes, since there is no interpreter to wrap. Stub both
# conditions so the fixture captures the SHIM branch instead: cmd.exe setting
# EVERY_HOME inline and calling the launcher. That is the shape the Go emits,
# so the two are actually comparable.
#
# The launcher placeholder carries a .cmd suffix here only because that suffix
# is what selects the branch. It stays opaque on both sides.
module Every
  def self.windows?
    true
  end

  module Runtime
    def self.bin
      "#{LAUNCHER}.cmd"
    end
  end
end

puts "taskschd:"
schedules.each do |slug, sched|
  # Sub-minute intervals are rejected on Windows; skip rather than rescue, so a
  # future change that starts accepting them shows up as a missing golden.
  next if sched.kind == :interval && sched.interval < 60

  xml = Every::WindowsTaskScheduler.task_xml(slug, sched)
  write "taskschd/#{slug}.xml", xml
  write "taskschd/#{slug}.utf16", "\xFF\xFE".b + xml.encode(Encoding::UTF_16LE).b,
        binary: true
end
puts

# ---------------------------------------------------------------------------
# store/ -- tasks.json bytes, including key order and the JSON escaping of a
# command full of the characters Go's encoder mangles by default.
# ---------------------------------------------------------------------------
puts "store:"
sample = {
  "tasks" => {
    # Insertion order is the contract: `every list` prints rows in this order.
    # Deliberately NOT alphabetical -- with sorted names a map-backed port
    # passes the ordering test by coincidence, which is how this fixture read
    # before someone checked.
    "nightly"  => {
      "cmd" => "borg create ::{now} ~/src && echo done > /tmp/log 2>&1",
      "schedule" => Every::Schedule.parse(["day", "9am"]).to_h,
      "cwd" => "/Users/me/src", "created_at" => FIXED_NOW.iso8601,
      "paused" => false, "quiet" => false, "timeout" => 1800
    },
    "backup"   => {
      "cmd" => "rsync -a a/ b/",
      "schedule" => Every::Schedule.parse(["15m"]).to_h,
      "cwd" => "/Users/me", "created_at" => FIXED_NOW.iso8601,
      "paused" => true, "quiet" => true
    },
    "archive"  => {
      "cmd" => "echo 'héllo — wörld' && printf '<tag>'",
      "schedule" => Every::Schedule.parse(["monday,thursday", "10:00"]).to_h,
      "cwd" => "/Users/me", "created_at" => FIXED_NOW.iso8601,
      "paused" => false, "quiet" => false
    }
  }
}
write "store/tasks.json", JSON.pretty_generate(sample) + "\n"

# The run ledger. Float formatting here is the trap: Ruby writes 12.0, Go
# writes 12 unless you format it yourself.
ledger = [
  { "ts" => FIXED_NOW.iso8601,             "exit" => 0,  "dur" => 12.0 },
  { "ts" => (FIXED_NOW + 900).iso8601,     "exit" => 7,  "dur" => 0.03 },
  { "ts" => (FIXED_NOW + 1800).iso8601,    "exit" => 124, "dur" => 30.5 },
].map { |r| JSON.generate(r) }.join("\n") + "\n"
write "store/runs.jsonl", ledger
puts

puts "done."
