require "minitest/autorun"
$LOAD_PATH.unshift File.expand_path("../../lib", __FILE__)
require "every"

# Path resolution honors the XDG Base Directory spec without breaking the
# existing default or the EVERY_HOME override.
class XdgTest < Minitest::Test
  def dd(env) Every.resolve_data_dir(env) end
  def cd(env) Every.resolve_config_dir(env) end

  def test_every_home_overrides_everything
    Every.stub(:windows?, false) do
      assert_equal File.expand_path("/custom/x"),
                   dd("EVERY_HOME" => "/custom/x", "XDG_DATA_HOME" => "/xdg")
    end
  end

  # Expected side is expanded too: resolve_data_dir runs the result through
  # File.expand_path, which on Windows prefixes the current drive ("D:/xdg/every").
  # Stubbing windows? does not change that, so a bare "/xdg/every" fails there.
  def test_xdg_data_home
    Every.stub(:windows?, false) do
      assert_equal File.expand_path("/xdg/every"), dd("XDG_DATA_HOME" => "/xdg")
    end
  end

  def test_default_when_neither_set
    Every.stub(:windows?, false) do
      assert_equal File.expand_path("~/.local/share/every"), dd({})
    end
  end

  # XDG spec: a non-absolute XDG_DATA_HOME must be ignored.
  def test_relative_xdg_data_home_ignored
    Every.stub(:windows?, false) do
      assert_equal File.expand_path("~/.local/share/every"),
                   dd("XDG_DATA_HOME" => "relative/path")
    end
  end

  def test_config_dir_honors_xdg
    Every.stub(:windows?, false) do
      assert_equal "/cfg/systemd/user", cd("XDG_CONFIG_HOME" => "/cfg")
    end
  end

  def test_config_dir_default
    Every.stub(:windows?, false) do
      assert_equal File.expand_path("~/.config/systemd/user"), cd({})
    end
  end

  def test_windows_data_dir_uses_localappdata
    Every.stub(:windows?, true) do
      assert_equal "C:/Users/Alice/AppData/Local/every",
                   dd("LOCALAPPDATA" => "C:/Users/Alice/AppData/Local")
    end
  end

  # Real Windows hands back backslashes; File.join then adds a forward slash.
  # The result must not be a mixed-separator path, since it is what doctor and
  # error messages print.
  def test_windows_data_dir_normalises_backslashes
    Every.stub(:windows?, true) do
      assert_equal "C:/Users/Alice/AppData/Local/every",
                   dd("LOCALAPPDATA" => "C:\\Users\\Alice\\AppData\\Local")
    end
  end

  def test_windows_config_dir_normalises_backslashes
    Every.stub(:windows?, true) do
      assert_equal "C:/Users/Alice/AppData/Roaming/every",
                   cd("APPDATA" => "C:\\Users\\Alice\\AppData\\Roaming")
    end
  end

  def test_windows_config_dir_uses_appdata
    Every.stub(:windows?, true) do
      assert_equal "C:/Users/Alice/AppData/Roaming/every",
                   cd("APPDATA" => "C:/Users/Alice/AppData/Roaming")
    end
  end
end
