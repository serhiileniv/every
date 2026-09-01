module Every
  # Linux backend (beta): systemd user timers. Each task becomes a pair of
  # units in ~/.config/systemd/user — every-<name>.service (oneshot, invokes
  # `every run <name>`) and every-<name>.timer. Persistent=true mirrors
  # launchd's behavior of firing missed calendar runs on wake/boot.
  module Systemd
    UNIT_DIR = ENV["EVERY_SYSTEMD_DIR"] ? File.expand_path(ENV["EVERY_SYSTEMD_DIR"]) : Every.resolve_config_dir
    DAYS = %w[Sun Mon Tue Wed Thu Fri Sat].freeze

    module_function

    def unit_base(name)
      "every-#{name}"
    end

    def unit_path(name)
      File.join(UNIT_DIR, "#{unit_base(name)}.timer")
    end

    # Generic backend capability used by `doctor`.  File-backed schedulers can
    # answer this locally; registry/service-backed schedulers provide their own
    # implementation.
    def resource_exists?(name)
      File.exist?(unit_path(name))
    end

    def service_path(name)
      File.join(UNIT_DIR, "#{unit_base(name)}.service")
    end

    def write(name, _schedule = nil)
      schedule = _schedule
      FileUtils.mkdir_p(UNIT_DIR)
      File.write(service_path(name), service_unit(name))
      File.write(unit_path(name), timer_unit(name, schedule))
    end

    def service_unit(name)
      # systemd user services start from a clean environment; pin the resolved
      # data dir so the timer-spawned run reads the SAME store the task was
      # added under (EVERY_HOME or XDG_DATA_HOME won't be inherited).
      <<~UNIT
        [Unit]
        Description=every task #{name}

        [Service]
        Type=oneshot
        Environment=EVERY_HOME=#{DATA_DIR}
        ExecStart=#{RbConfig.ruby} "#{Runtime.bin}" run "#{name}"
      UNIT
    end

    def timer_unit(name, schedule)
      lines = ["[Unit]", "Description=every timer #{name}", "", "[Timer]"]
      # Tighten the default 1-minute slack so sub-minute intervals and calendar
      # times fire close to when launchd would on macOS.
      lines << "AccuracySec=1s"
      if schedule.kind == :interval
        lines << "OnActiveSec=#{schedule.interval}"
        lines << "OnUnitActiveSec=#{schedule.interval}"
      else
        calendar_lines(schedule).each { |c| lines << "OnCalendar=#{c}" }
        lines << "Persistent=true"
      end
      (lines + ["", "[Install]", "WantedBy=timers.target"]).join("\n") + "\n"
    end

    def calendar_lines(schedule)
      schedule.entries.map do |e|
        t = format("%02d:%02d:00", e["hour"], e["minute"])
        e["weekday"] ? "#{DAYS[e['weekday'] % 7]} *-*-* #{t}" : "*-*-* #{t}"
      end
    end

    def enable(name)
      sysctl("daemon-reload")
      out, st = sysctl("enable", "--now", "#{unit_base(name)}.timer")
      raise "systemctl enable failed: #{out.strip}" unless st.success?
      true
    end

    def disable(name)
      sysctl("disable", "--now", "#{unit_base(name)}.timer")
    end

    def loaded?(name)
      _out, st = sysctl("is-active", "--quiet", "#{unit_base(name)}.timer")
      st.success?
    end

    # All active every-timers in ONE call (vs one `is-active` per task).
    def loaded_names
      out, st = sysctl("list-units", "--type=timer", "--state=active",
                       "--no-legend", "--plain", "every-*.timer")
      return [] unless st.success?
      parse_units(out)
    end

    # `list-units --plain --no-legend` prints "UNIT LOAD ACTIVE SUB DESC" rows.
    def parse_units(out)
      out.lines.each_with_object([]) do |line, acc|
        # no-arg split discards leading blanks, so an indented row still parses
        m = line.split.first.to_s.match(/\Aevery-(.+)\.timer\z/)
        acc << m[1] if m
      end
    end

    def delete_units(name)
      [service_path(name), unit_path(name)].each do |p|
        File.delete(p) if File.exist?(p)
      end
      sysctl("daemon-reload")
    end

    def sysctl(*args)
      Open3.capture2e("systemctl", "--user", *args)
    end
  end
end
