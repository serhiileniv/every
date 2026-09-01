module Every
  # Scheduler-spawned processes can't read TCC-protected folders on macOS
  # (Documents, Desktop, Downloads) — if the tool lives there, every scheduled
  # run dies with "Operation not permitted" before our code even loads. So when
  # (and ONLY when) the install sits in such a folder, we mirror bin/ + lib/
  # into the unprotected data dir and point the scheduler at that copy.
  #
  # Everywhere else — Homebrew, /usr/local, ~/code — the scheduler invokes the
  # installed launcher directly, so `brew upgrade` (or a git pull) takes effect
  # on the next run instead of freezing old code into a copy.
  module Runtime
    RUNTIME_DIR = File.join(DATA_DIR, "runtime")
    BIN = File.join(RUNTIME_DIR, "bin", "every")

    module_function

    # TCC only exists on macOS; on Linux these folder names carry no
    # restriction, so never copy there (a ~/Documents install stays live).
    def tcc_protected?(path)
      RUBY_PLATFORM.include?("darwin") &&
        !(path.to_s =~ %r{/(Documents|Desktop|Downloads)(/|\z)}).nil?
    end

    def needs_copy?
      tcc_protected?(ROOT) && !ROOT.start_with?(DATA_DIR)
    end

    # Rebuild the copy on every add/resume (so a `git pull` propagates even
    # without a version bump — the project ships code under the same version), and
    # swap it in with two renames so the live dir is never missing mid-copy for
    # more than the instant between them.
    def ensure!
      return unless needs_copy?

      staging = "#{RUNTIME_DIR}.new"
      old = "#{RUNTIME_DIR}.old"
      FileUtils.rm_rf(staging)
      FileUtils.rm_rf(old)
      FileUtils.mkdir_p(staging)
      FileUtils.cp_r(File.join(ROOT, "bin"), staging)
      FileUtils.cp_r(File.join(ROOT, "lib"), staging)
      FileUtils.chmod(0o755, File.join(staging, "bin", "every"))
      File.rename(RUNTIME_DIR, old) if File.exist?(RUNTIME_DIR)
      File.rename(staging, RUNTIME_DIR)
      FileUtils.rm_rf(old)
      BIN
    end

    # Path the scheduler should invoke. When a copy is required, the stable copy.
    # Otherwise the launcher exactly as invoked — for a Homebrew install that's
    # the /opt/homebrew/bin/every symlink, which survives version upgrades.
    def bin
      return BIN if needs_copy?
      if Every.windows?
        # The installer owns this shim and refreshes it when Ruby is upgraded.
        # Task Scheduler therefore keeps a stable entrypoint instead of
        # persisting the current Ruby executable path in every task.
        shim = File.expand_path("../../bin/every.cmd", ROOT)
        return shim if File.file?(shim)
      end
      launcher = File.expand_path($PROGRAM_NAME)
      File.exist?(launcher) ? launcher : Every::BIN
    end
  end
end
