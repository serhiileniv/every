require "json"
require "fileutils"
require "time"
require "open3"
require "rbconfig"

module Every
  VERSION = "0.3.1".freeze
  HOMEPAGE = "https://github.com/serhiileniv/every".freeze
  TAGLINE = "humane task scheduler for macOS (launchd), Linux (systemd), and Windows (Task Scheduler)".freeze

  # Exit codes (sysexits.h convention): 0 ok · 64 usage/bad args ·
  # 66 no such task/log · 1 other failure. Runs also surface 124 (timeout) and
  # 128+signum (killed by a signal); see runner.rb.
  EX_USAGE = 64
  EX_NOINPUT = 66

  ROOT = File.expand_path("..", __dir__)
  BIN  = File.join(ROOT, "bin", "every")

  # Keep platform checks in one place.  `RUBY_PLATFORM` also identifies
  # mingw/mswin builds, while host_os is useful on Ruby implementations that
  # use a less specific platform string.
  def self.windows?
    host = RbConfig::CONFIG["host_os"].to_s
    !!(host =~ /mswin|mingw/ || RUBY_PLATFORM =~ /mswin|mingw/)
  end

  def self.darwin?
    RUBY_PLATFORM.include?("darwin")
  end

  def self.linux?
    RUBY_PLATFORM.include?("linux")
  end

  # Data dir precedence: EVERY_HOME (explicit) → $XDG_DATA_HOME/every →
  # ~/.local/share/every (the XDG default anyway, so existing installs are
  # unchanged). Per the XDG spec, a non-absolute XDG_DATA_HOME is ignored.
  def self.resolve_data_dir(env = ENV)
    explicit = env["EVERY_HOME"].to_s
    return File.expand_path(explicit) unless explicit.empty?

    xdg = env["XDG_DATA_HOME"].to_s
    if windows? && xdg.empty?
      local = env["LOCALAPPDATA"].to_s
      local = File.join(Dir.home, "AppData", "Local") if local.empty?
      return windows_path(File.join(local, "every"))
    end

    File.expand_path(
      xdg.start_with?("/") ? File.join(xdg, "every") : "~/.local/share/every"
    )
  end

  # LOCALAPPDATA/APPDATA come back with backslashes, and File.join adds a
  # forward slash, so the raw result is mixed: "C:\\Users\\me\\AppData\\Local/every".
  # It works -- Ruby and Windows both accept either separator -- but it is what
  # `doctor`, `list` and error messages print. One separator throughout is the
  # same location on disk, so nothing moves.
  def self.windows_path(path)
    path.tr("\\", "/")
  end

  # systemd user units live under $XDG_CONFIG_HOME/systemd/user (default
  # ~/.config/systemd/user). Non-absolute XDG_CONFIG_HOME is ignored.
  def self.resolve_config_dir(env = ENV)
    if windows? && env["XDG_CONFIG_HOME"].to_s.empty?
      appdata = env["APPDATA"].to_s
      appdata = File.join(Dir.home, "AppData", "Roaming") if appdata.empty?
      return windows_path(File.join(appdata, "every"))
    end

    xdg = env["XDG_CONFIG_HOME"].to_s
    xdg.start_with?("/") ? File.join(xdg, "systemd", "user")
                         : File.expand_path("~/.config/systemd/user")
  end

  DATA_DIR   = resolve_data_dir
  LOG_DIR    = File.join(DATA_DIR, "logs")
  RUNS_DIR   = File.join(DATA_DIR, "runs")
  AGENTS_DIR = File.expand_path("~/Library/LaunchAgents")
end

require "every/color"
require "every/tail"
require "every/schedule"
require "every/store"
require "every/runtime"
require "every/launchd"
require "every/systemd"
require "every/windows_task_scheduler"
require "every/backend"
require "every/runner"
require "every/doctor"
require "every/cli"
