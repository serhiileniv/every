require "minitest/autorun"
require "tmpdir"
$LOAD_PATH.unshift File.expand_path("../../lib", __FILE__)
require "every"

class WindowsTaskSchedulerTest < Minitest::Test
  S = Every::Schedule
  WS = Every::WindowsTaskScheduler

  def test_interval_xml
    xml = WS.task_xml("demo", S.parse(["15m"]))
    assert_includes xml, "<TimeTrigger>"
    assert_includes xml, "<Interval>PT900S</Interval>"
    assert_includes xml, "<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>"
    assert_includes xml, "<StartWhenAvailable>true</StartWhenAvailable>"
    assert_includes xml, "\\every\\demo"
  end

  def test_calendar_xml
    xml = WS.task_xml("demo", S.parse(%w[day 9am]))
    assert_includes xml, "<CalendarTrigger>"
    assert_includes xml, "<ScheduleByDay><DaysInterval>1</DaysInterval></ScheduleByDay>"
    assert_includes xml, "<Command>"
    assert_includes xml, "demo.runner.rb"
  end

  def test_weekly_xml
    xml = WS.task_xml("demo", S.parse(%w[monday,thursday 6pm]))
    assert_includes xml, "<Monday/>"
    assert_includes xml, "<Thursday/>"
    assert_equal 2, xml.scan("<CalendarTrigger>").length
  end

  def test_wrapper_pins_data_dir_and_loads_runtime
    wrapper = WS.runner_wrapper("demo")
    assert_includes wrapper, "ENV[\"EVERY_HOME\"]"
    assert_includes wrapper, Every::DATA_DIR.dump
    assert_includes wrapper, "require \"every\""
    assert_includes wrapper, "Every::Runner.run(\"demo\")"
  end

  def test_parse_tasks_filters_non_every_tasks
    out = <<~CSV
      "\\every\\backup","08/31/2026 09:00:00","Ready"
      "\\every\\paused","N/A","Disabled"
      "\\Microsoft\\Windows\\Other","N/A","Ready"
    CSV
    assert_equal [
      { name: "backup", status: "Ready" },
      { name: "paused", status: "Disabled" }
    ], WS.parse_tasks(out)
  end

  def test_parse_task_states_filters_non_every_tasks
    out = <<~CSV
      "TaskPath","TaskName","State"
      "\\every\\","backup","Ready"
      "\\every\\","paused","Disabled"
      "\\Microsoft\\Windows\\","Other","Ready"
    CSV
    assert_equal [
      { name: "backup", state: "Ready" },
      { name: "paused", state: "Disabled" }
    ], WS.parse_task_states(out)
  end

  def test_task_xml_uses_stable_windows_shim
    Every.stub(:windows?, true) do
      Every::Runtime.stub(:bin, "C:/Program Files/every/bin/every.cmd") do
        WS.stub(:current_user, "Alice") do
          xml = WS.task_xml("demo", S.parse(["15m"]))
          comspec = ENV["COMSPEC"].to_s
          comspec = "cmd.exe" if comspec.empty?
          assert_includes xml, "<Command>#{comspec}</Command>"
          assert_includes xml, %Q{set "EVERY_HOME=#{Every::DATA_DIR}" &amp;&amp; call "C:/Program Files/every/bin/every.cmd" run "demo"}
        end
      end
    end
  end

  def test_windows_runner_uses_a_temporary_script
    Every.stub(:windows?, true) do
      Every::Runner.stub(:windows_shell, ["cmd.exe", "/d", "/s", "/c"]) do
        argv, cleanup = Every::Runner.command_argv('echo "my file.txt"')
        begin
          assert_equal ["cmd.exe", "/d", "/s", "/c"], argv[0, 4]
          assert_match(/\.cmd\z/, argv.last)
          assert_equal "@echo off\r\necho \"my file.txt\"\r\n", File.binread(argv.last)
        ensure
          cleanup.call
        end
        refute File.exist?(argv.last)
      end
    end
  end

  # Regression guard. Written as an interpolating heredoc, the '\every\' path
  # literal this script used to carry collapsed to an ESC byte, so the query
  # matched nothing and every loaded_names call raised on real Windows. No CI
  # job could see it, because none of them run PowerShell against the service.
  def test_task_state_script_has_no_stray_control_characters
    script = WS.task_state_script
    assert_includes script, "Get-ScheduledTask"
    refute_match(/[^\P{Cc}\n]/, script,
                 "PowerShell script picked up a control byte from Ruby escaping")
  end

  def test_delete_units_tolerates_an_already_absent_task
    failed = [+"ERROR: The system cannot find the file specified.", Struct.new(:success?).new(false)]
    WS.stub(:schtasks, failed) do
      WS.stub(:loaded?, false) do
        assert WS.delete_units("gone"), "rm must still clear a task the service no longer has"
      end
    end
  end

  def test_delete_units_raises_when_the_task_survives_the_delete
    failed = [+"ERROR: Access is denied.", Struct.new(:success?).new(false)]
    WS.stub(:schtasks, failed) do
      WS.stub(:loaded?, true) do
        error = assert_raises(RuntimeError) { WS.delete_units("stubborn") }
        assert_match(/Access is denied/, error.message)
      end
    end
  end

  def test_disable_tolerates_an_already_absent_task
    failed = [+"ERROR: The system cannot find the file specified.", Struct.new(:success?).new(false)]
    WS.stub(:schtasks, failed) do
      WS.stub(:loaded?, false) do
        assert WS.disable("gone")
      end
    end
  end

  # Regression guard for the bug that made `every add` impossible on Windows:
  # schtasks hands the XML to MSXML, which needs a BOM to know the encoding.
  # Plain UTF-8 with no BOM was decoded as ANSI and rejected at the encoding
  # declaration. No unit test could see it -- they all assert the XML *string*,
  # and never the bytes that reach the service.
  def test_task_xml_is_written_as_utf16_with_a_bom
    dir = Dir.mktmpdir
    path = File.join(dir, "demo.xml")
    begin
      WS.atomic_write_utf16(path, WS.task_xml("demo", S.parse(["15m"])))
      bytes = File.binread(path)
      assert_equal [0xFF, 0xFE], bytes[0, 2].bytes, "missing UTF-16LE byte-order mark"
      text = bytes[2..].force_encoding(Encoding::UTF_16LE).encode(Encoding::UTF_8)
      assert_includes text, %(<?xml version="1.0" encoding="UTF-16"?>)
      assert_includes text, "\\every\\demo"
    ensure
      FileUtils.remove_entry(dir)
    end
  end

  # The declaration has to describe the bytes actually written, or MSXML fails
  # the same way from the other direction.
  def test_xml_declaration_matches_the_written_encoding
    assert_includes WS.task_xml("demo", S.parse(["15m"])),
                    %(<?xml version="1.0" encoding="UTF-16"?>)
  end

  def test_subminute_intervals_are_rejected
    error = assert_raises(ArgumentError) do
      WS.validate_schedule!(S.parse(["15s"]))
    end
    assert_includes error.message, "from 1m"
  end

  def test_windows_shell_defaults_to_cmd
    shell = Every::Runner.windows_shell
    assert_match(/cmd\.exe\z/i, shell.first)
    assert_equal ["/d", "/s", "/c"], shell.drop(1)
  end

  def test_backend_dispatches_to_windows_scheduler
    Every.stub(:darwin?, false) do
      Every.stub(:windows?, true) do
        assert_equal WS, Every::Backend.current
      end
    end
  end
end
