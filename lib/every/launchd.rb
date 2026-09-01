module Every
  # Everything that touches launchd: plist rendering + launchctl calls.
  # Agents live at ~/Library/LaunchAgents/com.every.<name>.plist and invoke
  # `every run <name>`, which does the actual execution + logging.
  module Launchd
    module_function

    # Common backend interface (see Backend)
    def write(name, schedule)
      write_plist(name, schedule)
    end

    def enable(name)
      bootstrap(name)
    end

    def disable(name)
      bootout(name)
    end

    def unit_path(name)
      plist_path(name)
    end

    # Generic backend capability used by `doctor`.  File-backed schedulers can
    # answer this locally; registry/service-backed schedulers provide their own
    # implementation.
    def resource_exists?(name)
      File.exist?(unit_path(name))
    end

    def delete_units(name)
      p = plist_path(name)
      File.delete(p) if File.exist?(p)
    end

    def label(name)
      "com.every.#{name}"
    end

    def plist_path(name)
      File.join(AGENTS_DIR, "#{label(name)}.plist")
    end

    def uid
      Process.uid
    end

    def write_plist(name, schedule)
      FileUtils.mkdir_p(AGENTS_DIR)
      File.write(plist_path(name), plist_xml(name, schedule))
      plist_path(name)
    end

    def plist_xml(name, schedule)
      agent_log = File.join(LOG_DIR, "_agent.log")
      trigger =
        if schedule.kind == :interval
          "  <key>StartInterval</key>\n" \
          "  <integer>#{schedule.interval}</integer>"
        else
          dicts = schedule.entries.map do |e|
            lines = []
            if e["weekday"]
              lines << "      <key>Weekday</key><integer>#{e['weekday']}</integer>"
            end
            lines << "      <key>Hour</key><integer>#{e['hour']}</integer>"
            lines << "      <key>Minute</key><integer>#{e['minute']}</integer>"
            "    <dict>\n#{lines.join("\n")}\n    </dict>"
          end
          "  <key>StartCalendarInterval</key>\n  <array>\n#{dicts.join("\n")}\n  </array>"
        end

      <<~XML
        <?xml version="1.0" encoding="UTF-8"?>
        <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
        <plist version="1.0">
        <dict>
          <key>Label</key>
          <string>#{xesc(label(name))}</string>
          <key>ProgramArguments</key>
          <array>
            <string>#{xesc(RbConfig.ruby)}</string>
            <string>#{xesc(Runtime.bin)}</string>
            <string>run</string>
            <string>#{xesc(name)}</string>
          </array>
        #{trigger}
        #{env_block}  <key>RunAtLoad</key>
          <false/>
          <key>StandardOutPath</key>
          <string>#{xesc(agent_log)}</string>
          <key>StandardErrorPath</key>
          <string>#{xesc(agent_log)}</string>
        </dict>
        </plist>
      XML
    end

    # Pin the resolved data dir so a launchd-spawned run reads the SAME store the
    # task was added under. launchd doesn't inherit the shell's EVERY_HOME or
    # XDG_DATA_HOME, so without this an XDG (or EVERY_HOME) install's scheduled
    # runs would recompute the default dir, not find the task, and never fire.
    def env_block
      "  <key>EnvironmentVariables</key>\n" \
      "  <dict>\n" \
      "    <key>EVERY_HOME</key><string>#{xesc(DATA_DIR)}</string>\n" \
      "  </dict>\n"
    end

    def xesc(s)
      s.to_s.gsub("&", "&amp;").gsub("<", "&lt;").gsub(">", "&gt;")
    end

    def bootstrap(name)
      path = plist_path(name)
      out, st = Open3.capture2e("launchctl", "bootstrap", "gui/#{uid}", path)
      return true if st.success?

      # Most common failure: an agent with this label is already loaded.
      bootout(name)
      out, st = Open3.capture2e("launchctl", "bootstrap", "gui/#{uid}", path)
      return true if st.success?

      _legacy, st2 = Open3.capture2e("launchctl", "load", "-w", path)
      return true if st2.success?

      raise "launchctl bootstrap failed: #{out.strip}"
    end

    def bootout(name)
      Open3.capture2e("launchctl", "bootout", "gui/#{uid}/#{label(name)}")
    end

    def loaded?(name)
      _out, st = Open3.capture2e("launchctl", "print", "gui/#{uid}/#{label(name)}")
      st.success?
    end

    # All loaded every-agent names in ONE call (vs one `print` per task) — let
    # `list`/`doctor` check membership instead of forking a subprocess per task.
    def loaded_names
      out, st = Open3.capture2e("launchctl", "list")
      return [] unless st.success?
      parse_labels(out)
    end

    # `launchctl list` prints "PID<TAB>Status<TAB>Label" rows; keep our labels.
    def parse_labels(out)
      out.lines.each_with_object([]) do |line, acc|
        lbl = line.split("\t").last.to_s.strip
        acc << lbl.sub("com.every.", "") if lbl.start_with?("com.every.")
      end
    end
  end
end
