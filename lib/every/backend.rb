module Every
  # Platform dispatch: launchd on macOS, systemd user timers on Linux, and the
  # Windows Task Scheduler on native Windows Ruby builds.
  # Backends implement the same interface:
  #   write(name, schedule)  enable(name)  disable(name)
  #   delete_units(name)     loaded?(name) loaded_names
  #   resource_exists?(name) unit_path(name)
  module Backend
    module_function

    def current
      return Launchd if Every.darwin?
      return WindowsTaskScheduler if Every.windows?
      return Systemd if Every.linux?

      raise "unsupported platform: #{RbConfig::CONFIG['host_os']}"
    end
  end
end
