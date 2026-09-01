module Every
  # `every doctor` — explains, in plain language, why scheduled tasks
  # might not be running. Covers the classic launchd traps.
  module Doctor
    module_function

    def run
      failures = 0
      darwin = Every.darwin?
      windows = Every.windows?

      if darwin
        _out, st = Open3.capture2e("launchctl", "print", "gui/#{Launchd.uid}")
        failures += report("launchd user session reachable (gui/#{Launchd.uid})",
                           st.success?,
                           "no GUI session — launchd agents don't run over bare SSH sessions")
      elsif windows
        out, st = WindowsTaskScheduler.scheduler_status
        scheduler_ok = st.success? && out =~ /STATE\s*:\s*4\b/i
        failures += report("Windows Task Scheduler service running", !!scheduler_ok,
                           "start the Task Scheduler service, then run `every doctor` again")
      else
        out, st = Systemd.sysctl("is-system-running")
        ok = st.success? || out.strip == "degraded"
        failures += report("systemd user session reachable", ok,
                           "no user systemd — is this a desktop/loginctl session?")
        linger, lst = Open3.capture2e("loginctl", "show-user", ENV["USER"].to_s, "-p", "Linger")
        lingering = lst.success? && linger.include?("Linger=yes")
        failures += report("lingering enabled (timers fire at boot / after logout)",
                           lingering,
                           "run: loginctl enable-linger #{ENV['USER']}")
      end

      dir_ok = begin
        FileUtils.mkdir_p(DATA_DIR)
        File.writable?(DATA_DIR)
      rescue StandardError
        false
      end
      failures += report("data dir writable (#{DATA_DIR})", dir_ok,
                         "fix permissions on #{DATA_DIR}")

      store = Store.load
      if store.tasks.empty?
        puts "  (no tasks registered yet)"
      else
        failures += report("runtime copy present (#{Runtime.bin})",
                           File.exist?(Runtime.bin),
                           "refresh it: every resume <name> (or re-add any task)")
      end

      backend = Backend.current
      loaded = backend.loaded_names   # one scheduler query for all tasks, not N
      store.tasks.each do |name, task|
        puts "\ntask: #{name}"
        unit = backend.unit_path(name)
        resource_exists = if backend.respond_to?(:resource_exists?)
                            backend.resource_exists?(name)
                          else
                            File.exist?(unit)
                          end
        failures += report("scheduler resource exists (#{unit})", resource_exists,
                           "re-create the task: every rm #{name} && every <schedule> -- <cmd>")

        if task["paused"]
          puts "  · paused — resume with: every resume #{name}"
        else
          scheduler_name = if darwin
                             "launchd"
                           elsif windows
                             "Windows Task Scheduler"
                           else
                             "systemd"
                           end
          failures += report("scheduled in #{scheduler_name}",
                             loaded.include?(name),
                             "load it: every resume #{name}")
        end

        first_word = task["cmd"].to_s.strip.split(/\s+/).first.to_s
        if first_word =~ /\A[\w][\w.-]*\z/
          # A plain command name — check PATH the way the runner will resolve it.
          probe = if windows
                    ["where.exe", first_word]
                  else
                    [*Runner.login_shell, "command -v #{shellword(first_word)}"]
                  end
          _o, st = Open3.capture2e(*probe)
          failures += report("command resolvable in login shell (#{first_word})",
                             st.success?,
                             windows ?
                               "tasks use the Windows shell; check the user/system PATH, or use an absolute path" :
                               "tasks run in a LOGIN shell: PATH set only in the interactive rc file " \
                               "(~/.zshrc / ~/.bashrc, e.g. mise/rbenv hooks) is not visible — move it " \
                               "to ~/.zprofile / ~/.profile, or use an absolute path")
        elsif first_word.include?("/") ||
              (windows && (first_word.include?("\\") || first_word =~ /\A[A-Za-z]:/))
          # A path (possibly ~-relative) — expand ~ the way the shell does.
          expanded = if windows
                       first_word.sub(/\A%USERPROFILE%/i, Dir.home)
                     else
                       first_word.sub(/\A~/, Dir.home)
                     end
          failures += report("command file exists (#{first_word})", File.exist?(expanded),
                             "not found: #{expanded} — check the path")
        else
          # env prefix / shell expression (e.g. FOO=bar cmd) — can't reliably probe.
          puts "  · command begins with #{first_word.inspect} — not probed (shell expression)"
        end

        if darwin && task["cwd"].to_s =~ %r{/(Documents|Desktop|Downloads)(/|\z)}
          puts "  · note: added inside #{Regexp.last_match(1)} (TCC-protected) — if the command touches"
          puts "    that folder, grant Full Disk Access to ruby (System Settings → Privacy & Security)"
        end

        last = store.last_run(name)
        if last.nil?
          puts "  · no runs recorded yet (next: check `every list`)"
        elsif last["exit"] != 0
          failures += report("last run succeeded", false,
                             "exit=#{last['exit']} — see: every log #{name}")
          log_path = File.join(LOG_DIR, "#{name}.log")
          if darwin && File.exist?(log_path) && File.read(log_path).include?("Operation not permitted")
            puts "    hint: 'Operation not permitted' usually means the ruby binary needs"
            puts "    Full Disk Access (System Settings → Privacy & Security) to touch that folder"
          end
        else
          puts "  #{Color.green('✓')} last run ok (#{last['ts']})"
        end
      end

      puts
      if failures.zero?
        puts Color.green("all good ✓")
      else
        puts Color.red("#{failures} problem(s) found")
        exit 1
      end
    end

    def report(label, ok, fix)
      if ok
        puts "  #{Color.green('✓')} #{label}"
        0
      else
        puts "  #{Color.red('✗')} #{label}"
        puts "    → #{fix}"
        1
      end
    end

    def shellword(w)
      "'" + w.gsub("'", "'\\\\''") + "'"
    end
  end
end
