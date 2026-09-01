module Every
  # `every run <name>` — what the platform scheduler invokes. Executes the task's
  # command through the user's login shell (so PATH matches the terminal),
  # captures all output, and records the run.
  module Runner
    MAX_LOG_BYTES = 5 * 1024 * 1024
    # The run ledger is a rolling window: enough history for status and a
    # staleness watchdog, but bounded so a task firing every minute for years
    # can't grow it without limit (and keep `list` fast). Detailed output lives
    # in the separately-rotated .log.
    MAX_RUN_RECORDS = 500
    RUN_TRIM_BYTES = 256 * 1024
    # Captured output is bounded so a chatty task can't OOM the run: keep the
    # first and last HALF_OUTPUT bytes (errors show up at both ends), drop the
    # middle. The full stream still flows to the command; we just don't hold it.
    HALF_OUTPUT = 32 * 1024

    module_function

    def run(name)
      task = Store.load[name]
      unless task
        warn "every: unknown task #{name.inspect} — orphaned agent? try: every doctor"
        exit EX_NOINPUT
      end

      FileUtils.mkdir_p(LOG_DIR)
      FileUtils.mkdir_p(RUNS_DIR)

      started = Time.now
      mono = Process.clock_gettime(Process::CLOCK_MONOTONIC)
      dir, note = workdir(task)
      out, exit_code = capture(task["cmd"], dir, task["timeout"])
      out = note.b + out if note   # note may hold a non-ASCII cwd path
      # Monotonic clock: an NTP/DST wall-clock jump mid-run can't make this
      # negative. The ledger timestamp still uses wall-clock `started`.
      duration = (Process.clock_gettime(Process::CLOCK_MONOTONIC) - mono).round(2)

      append_log(name, started, exit_code, duration, out)
      append_run(name, started, exit_code, duration)
      notify_failure(name, exit_code) if exit_code != 0 && !task["quiet"]

      if $stdout.tty?
        print out
        tail = "— exit #{exit_code} in #{duration}s (logged: every log #{name})"
        puts(exit_code.zero? ? Color.green(tail) : Color.red(tail))
      end
      exit exit_code
    end

    # Execute the command, capturing bounded output and enforcing an optional
    # timeout. The child runs in its own process group so a timeout kills the
    # whole tree (the login shell plus anything it spawned), never leaving a
    # hung process to block the next scheduled run.
    def capture(cmd, dir, timeout_sec)
      require "timeout"
      head = "".b           # first HALF_OUTPUT bytes, exactly
      tail = "".b           # last HALF_OUTPUT bytes (rolling)
      dropped = 0
      status = nil
      timed_out = false

      keep = lambda do |chunk|
        if head.bytesize < HALF_OUTPUT
          room = HALF_OUTPUT - head.bytesize
          head << chunk.byteslice(0, room)
          rest = chunk.byteslice(room, chunk.bytesize - room)
          chunk = rest
        end
        return if chunk.nil? || chunk.empty?
        tail << chunk
        return unless tail.bytesize > HALF_OUTPUT
        over = tail.bytesize - HALF_OUTPUT
        dropped += over
        tail = tail.byteslice(over, HALF_OUTPUT)
      end

      spawn_options = { chdir: dir }
      # Negative process-group signals are a POSIX primitive.  Windows uses
      # taskkill/Job Objects instead (see terminate), so do not pass pgroup
      # there; older RubyInstaller builds reject the option entirely.
      spawn_options[:pgroup] = true unless Every.windows?

      argv, cleanup = command_argv(cmd)
      begin
        Open3.popen2e(*argv, **spawn_options) do |stdin, out, wait|
          stdin.close
          pid = wait.pid
          drain = lambda do
            while (chunk = out.read(16 * 1024))
              keep.call(chunk)
            end
          end

          begin
            # wait.value lives INSIDE the timeout: a command that closes stdout
            # early but keeps running still gets killed at the deadline.
            if timeout_sec
              Timeout.timeout(timeout_sec) { drain.call; status = wait.value }
            else
              drain.call
              status = wait.value
            end
          rescue Timeout::Error
            timed_out = true
            terminate(pid)
            status = (wait.value rescue nil)
            head << "\n[every: killed after #{timeout_sec}s timeout]\n"
          end
        end
      ensure
        cleanup.call
      end

      # Append the tail whenever it exists; only inject the truncation marker
      # when bytes were actually dropped (32-64 KB output keeps head+tail with
      # nothing between). ASCII marker + binary body: no encoding crash.
      body = head
      unless tail.empty?
        body += "\n... [#{dropped} bytes truncated] ...\n".b if dropped.positive?
        body += tail
      end
      [body, exit_code_for(status, timed_out)]
    end

    # timeout -> 124; clean exit -> its code; signal death -> 128+signum.
    def exit_code_for(status, timed_out)
      return 124 if timed_out
      return status.exitstatus if status&.exitstatus
      return 128 + status.termsig if status.respond_to?(:signaled?) && status.signaled?
      1
    end

    # Kill the whole process tree: with pgroup:true the child is its own group
    # leader, so a negative pid signals the group (no getpgid/reap race).
    def terminate(pid)
      if Every.windows?
        # taskkill /T reaches the shell's descendants.  A future native Job
        # Object implementation can replace this without changing capture().
        system("taskkill.exe", "/PID", pid.to_s, "/T", "/F",
               out: File::NULL, err: File::NULL)
        return
      end

      Process.kill("TERM", -pid)
      sleep 0.3
      Process.kill("KILL", -pid)
    rescue Errno::ESRCH, Errno::EPERM
      nil
    end

    # Run through the user's login shell so PATH matches their terminal. Only
    # bash/zsh accept the bundled `-lc`; sh/dash/others reject `-l`, so use -c.
    def login_shell
      return windows_shell if Every.windows?
      return ["/bin/zsh", "-lc"] if Every.darwin?
      sh = ENV["SHELL"] || "/bin/bash"
      [sh, sh =~ /(bash|zsh)\z/ ? "-lc" : "-c"]
    end

    def windows_shell
      shell = ENV["EVERY_SHELL"].to_s
      shell = ENV["COMSPEC"].to_s if shell.empty?
      shell = "cmd.exe" if shell.empty?
      base = File.basename(shell).downcase.sub(/\.exe\z/, "")
      if base == "powershell" || base == "pwsh"
        [shell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command"]
      else
        [shell, "/d", "/s", "/c"]
      end
    end

    # Passing a quoted command as the final argv element of cmd.exe makes Ruby
    # add another Windows command-line escaping layer. A temporary script gives
    # cmd.exe and PowerShell the command text verbatim while preserving cwd,
    # output capture, and timeout behavior in capture().
    def command_argv(cmd)
      return [*login_shell, cmd], -> {} unless Every.windows?

      require "tempfile"
      shell = windows_shell
      powershell = shell.last == "-Command"
      suffix = powershell ? ".ps1" : ".cmd"
      temp = Tempfile.new(["every-command", suffix])
      temp.binmode
      # @echo off: a batch file runs with ECHO ON, so without it cmd copies
      # every line of the command into stdout and it lands in `every log`.
      content = powershell ? "\uFEFF#{cmd}\r\n" : "@echo off\r\n#{cmd}\r\n"
      temp.write(content.encode(Encoding::UTF_8))
      temp.close
      argv = if powershell
               [*shell[0...-1], "-File", temp.path]
             else
               [*shell, temp.path]
             end
      [argv, -> { temp.close! rescue nil }]
    rescue StandardError
      temp.close! rescue nil if temp
      raise
    end

    # Desktop notification so failures don't die silently in a log file.
    def notify_failure(name, exit_code)
      msg = "#{name} failed (exit #{exit_code}) — every log #{name}"
      if Every.darwin?
        script = "display notification \"#{osa_esc(msg)}\" with title \"every\""
        system("osascript", "-e", script, out: File::NULL, err: File::NULL)
      elsif Every.windows?
        notify_windows(msg)
      else
        system("notify-send", "every", msg, out: File::NULL, err: File::NULL)
      end
    end

    # Windows has no inbox notification utility guaranteed across editions.
    # `msg.exe` is a best-effort fallback for the current interactive user; a
    # failure to display it must never turn an already-recorded task failure
    # into another failure.
    def notify_windows(msg)
      user = ENV["USERNAME"].to_s
      return if user.empty?
      system("msg.exe", user, "/TIME:5", msg,
             out: File::NULL, err: File::NULL)
    rescue StandardError
      nil
    end

    def osa_esc(s)
      s.gsub("\\", "\\\\\\\\").gsub('"', '\"')
    end

    # Probe actual readability: under launchd, TCC-protected dirs (Documents…)
    # pass File.directory? but fail on access — fall back to HOME, loudly.
    def workdir(task)
      dir = task["cwd"]
      return [Dir.home, nil] unless dir && File.directory?(dir)
      Dir.entries(dir)
      [dir, nil]
    rescue SystemCallError
      [Dir.home,
      "note: cwd #{dir} not readable under scheduler — ran from #{Dir.home}\n"]
    end

    def append_log(name, started, exit_code, duration, out)
      path = File.join(LOG_DIR, "#{name}.log")
      rotate(path)
      File.open(path, "a") do |f|
        f.puts "=== #{started.strftime('%Y-%m-%d %H:%M:%S')} exit=#{exit_code} dur=#{duration}s ==="
        f.write(out)
        f.puts unless out.empty? || out.end_with?("\n")
      end
    end

    def append_run(name, started, exit_code, duration)
      path = File.join(RUNS_DIR, "#{name}.jsonl")
      File.open(path, "a") do |f|
        f.puts JSON.generate("ts" => started.iso8601,
                             "exit" => exit_code,
                             "dur" => duration)
      end
      trim_runs(path)
    end

    # Amortized-cheap bound: only touched once the file crosses the byte cap,
    # then rewritten to the last MAX_RUN_RECORDS lines.
    def trim_runs(path)
      return if File.size(path) <= RUN_TRIM_BYTES
      lines = File.readlines(path)
      return if lines.length <= MAX_RUN_RECORDS
      tmp = "#{path}.tmp.#{Process.pid}"
      File.write(tmp, lines.last(MAX_RUN_RECORDS).join)
      if Every.windows?
        # Do not use force: true: a failed replacement must raise instead of
        # silently dropping the freshly written trimmed ledger.
        FileUtils.mv(tmp, path)
      else
        File.rename(tmp, path)   # atomic: a crash mid-trim can't truncate history
      end
    end

    def rotate(path)
      return unless File.exist?(path) && File.size(path) > MAX_LOG_BYTES
      FileUtils.mv(path, "#{path}.old")
    end
  end
end
