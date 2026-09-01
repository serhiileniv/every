require "minitest/autorun"
$LOAD_PATH.unshift File.expand_path("../../lib", __FILE__)
require "every"

# The bulk-query parsers turn one scheduler dump (`launchctl list`,
# `systemctl list-units`, or `schtasks /FO CSV`) into the set of loaded
# every-task names — so `list`/`doctor` make one subprocess instead of one per
# task.
class LoadedNamesTest < Minitest::Test
  def test_launchd_parses_only_every_labels
    out = <<~OUT
      PID\tStatus\tLabel
      1234\t0\tcom.every.backup
      -\t0\tcom.every.sync-notes
      5678\t0\tcom.apple.Finder
      -\t0\tcom.every.db.dump
    OUT
    assert_equal %w[backup sync-notes db.dump], Every::Launchd.parse_labels(out)
  end

  def test_launchd_empty
    assert_equal [], Every::Launchd.parse_labels("")
  end

  def test_systemd_parses_only_every_timers
    out = <<~OUT
      every-backup.timer    loaded active waiting Timer every backup
      every-sync-notes.timer loaded active waiting Timer every sync-notes
      other.timer           loaded active waiting Some other timer
    OUT
    assert_equal %w[backup sync-notes], Every::Systemd.parse_units(out)
  end

  def test_systemd_empty
    assert_equal [], Every::Systemd.parse_units("")
  end

  # An indented row (leading whitespace) must still parse, not drop the task.
  def test_systemd_tolerates_leading_whitespace
    assert_equal %w[backup], Every::Systemd.parse_units("  every-backup.timer loaded active waiting d\n")
  end

  # The plist always pins the data dir so scheduled runs read the same store.
  def test_launchd_env_block_always_pins_data_dir
    assert_includes Every::Launchd.env_block, "<key>EVERY_HOME</key>"
    assert_includes Every::Launchd.env_block, Every::DATA_DIR
  end
end
