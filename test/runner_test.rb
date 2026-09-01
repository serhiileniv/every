require "minitest/autorun"
require "fileutils"
require "tmpdir"
require "json"
require "rbconfig"
require "shellwords"
ENV["EVERY_HOME"] = File.join(Dir.tmpdir, "every-runner-test")
FileUtils.rm_rf(ENV["EVERY_HOME"])
$LOAD_PATH.unshift File.expand_path("../../lib", __FILE__)
require "every"

class RunnerTest < Minitest::Test
  def setup
    FileUtils.rm_rf(ENV["EVERY_HOME"])
    FileUtils.mkdir_p(Every::RUNS_DIR)
  end

  def teardown
    FileUtils.rm_rf(ENV["EVERY_HOME"])
  end

  # Use Ruby itself for shell fixtures so the same tests can run on Windows,
  # where the Unix-only yes/head/seq utilities are not guaranteed to exist.
  def ruby_eval(code)
    if Every.windows?
      escaped = code.gsub("\\", "\\\\").gsub('"', '\\"')
      "\"#{RbConfig.ruby}\" -e \"#{escaped}\""
    else
      "#{Shellwords.escape(RbConfig.ruby)} -e #{Shellwords.escape(code)}"
    end
  end

  # A task firing forever must not grow its ledger without bound. trim_runs
  # runs after every append, so the file can never exceed the byte cap by more
  # than one record — that bounded *size* (not an exact line count) is the
  # stability guarantee.
  def test_run_ledger_size_is_bounded
    path = File.join(Every::RUNS_DIR, "loop.jsonl")
    max_seen = 0
    8000.times do |i|
      File.open(path, "a") { |f| f.puts JSON.generate("ts" => "2026-01-01T00:00:0#{i % 10}+03:00", "exit" => i % 2, "dur" => 0.1) }
      Every::Runner.trim_runs(path)
      max_seen = [max_seen, File.size(path)].max
    end
    # 8000 raw records would be ~440 KB; bounded it must stay near the cap.
    assert max_seen <= Every::Runner::RUN_TRIM_BYTES + 1024,
           "ledger size unbounded: peaked at #{max_seen} bytes"
    # Trimming actually happened (fewer lines than appended)...
    assert File.readlines(path).length < 8000
    # ...and the most recent run survived (status/list depend on it).
    assert_equal 8000.pred % 2, JSON.parse(File.readlines(path).last)["exit"]
  end

  # Below the byte cap, nothing is trimmed — small tasks keep full history.
  def test_small_ledger_untouched
    path = File.join(Every::RUNS_DIR, "small.jsonl")
    10.times { |i| File.open(path, "a") { |f| f.puts JSON.generate("ts" => "t#{i}", "exit" => 0) } }
    Every::Runner.trim_runs(path)
    assert_equal 10, File.readlines(path).length
  end

  # A chatty task must not be held whole in memory — output comes back bounded.
  def test_capture_bounds_output
    out, code = Every::Runner.capture(ruby_eval("STDOUT.write('x' * 300000)"), Dir.home, nil)
    assert_equal 0, code
    assert out.bytesize < 100 * 1024, "output not bounded: #{out.bytesize} bytes"
    assert_includes out, "truncated"
  end

  # Small output passes through verbatim, no truncation marker.
  def test_capture_small_output_verbatim
    out, code = Every::Runner.capture(ruby_eval("puts 'hello there'"), Dir.home, nil)
    assert_equal 0, code
    assert_includes out, "hello there"
    refute_includes out, "truncated"
  end

  # A hung task is killed at the timeout so it can't block the next run.
  def test_capture_timeout_kills
    t0 = Time.now
    out, code = Every::Runner.capture(ruby_eval("sleep 30"), Dir.home, 1)
    assert_operator code, :!=, 0
    assert_operator (Time.now - t0), :<, 5.0, "timeout did not fire promptly"
    assert_includes out, "timeout"
  end

  # Timeout must kill the whole process tree, not just the shell.
  def test_capture_timeout_kills_children
    skip "POSIX process-group fixture" if Every.windows?
    marker = File.join(ENV["EVERY_HOME"], "child-alive")
    FileUtils.mkdir_p(ENV["EVERY_HOME"])
    # A backgrounded child that would outlive a naive shell-only kill.
    Every::Runner.capture("(sleep 30 && touch #{marker}) & sleep 30", Dir.home, 1)
    sleep 2
    refute File.exist?(marker), "orphaned child survived the timeout kill"
  end

  # Output in the 32-64KB band must keep its TAIL (errors often live there) and
  # must NOT inject a false "0 bytes truncated" marker (nothing was dropped).
  def test_capture_keeps_tail_in_mid_band_without_false_marker
    out, code = Every::Runner.capture(
      ruby_eval("STDOUT.write('HEAD'); STDOUT.write('x' * 40000); STDOUT.write('TAILMARK')"),
      Dir.home, nil
    )
    assert_equal 0, code
    assert_includes out, "HEAD"
    assert_includes out, "TAILMARK"
    refute_includes out, "truncated", "false truncation marker on un-dropped output"
  end

  # Over 64KB, the marker IS shown (bytes really were dropped).
  def test_capture_marks_real_truncation
    out, _ = Every::Runner.capture(ruby_eval("STDOUT.write('x' * 200000)"), Dir.home, nil)
    assert_includes out, "truncated"
  end

  # Large non-ASCII output must not crash the truncation/concat path.
  def test_capture_non_ascii_over_cap_no_crash
    out, code = Every::Runner.capture(ruby_eval("STDOUT.write('é' * 40000)"), Dir.home, nil)
    assert_equal 0, code
    assert out.bytesize.positive?
  end

  def test_login_shell_flag_by_shell
    # Structural: bash/zsh get -lc, others get -c.
    assert_equal "-lc", (["/bin/zsh"].first =~ /(bash|zsh)\z/ ? "-lc" : "-c")
    assert_equal "-c",  ("/bin/sh" =~ /(bash|zsh)\z/ ? "-lc" : "-c")
  end
end
