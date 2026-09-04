<#
.SYNOPSIS
  every — installer for native Windows.

.DESCRIPTION
  Downloads one static every.exe for this platform, verifies it against the
  release checksums, and puts it at <Prefix>\bin\every.exe.

  There is no Ruby to find, no runtime tree to stage, and no every.cmd shim.
  The shim existed only so Task Scheduler would not pin a Ruby interpreter path
  into every task; with a single executable there is nothing to pin.

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File install.ps1
  powershell -ExecutionPolicy Bypass -File install.ps1 -Prefix C:\tools\every
  powershell -ExecutionPolicy Bypass -File install.ps1 -Uninstall
#>
param(
  [string]$Prefix = "",
  [string]$Version = "",
  [switch]$Uninstall,
  [switch]$Force
)

$ErrorActionPreference = "Stop"
$repo = "serhiileniv/every"

function Say([string]$Message) { Write-Host $Message }
function Step([string]$Message) { Write-Host "  $Message" -ForegroundColor DarkGray }
function Die([string]$Message) {
  Write-Host "every: $Message" -ForegroundColor Red
  exit 1
}

if (-not $Prefix) {
  $Prefix = Join-Path $env:LOCALAPPDATA "Programs\every"
}
$Prefix   = [IO.Path]::GetFullPath($Prefix)
$binDir   = Join-Path $Prefix "bin"
$binary   = Join-Path $binDir "every.exe"
$shareDir = Join-Path $Prefix "share"
$manifest = Join-Path $shareDir "every\.install-manifest"
$dataDir  = Join-Path $env:LOCALAPPDATA "every"
# The shim a pre-0.4 install left behind. Removed on upgrade, because a task
# still pointing at it would invoke a Ruby that is no longer there.
$legacyShim   = Join-Path $binDir "every.cmd"
$legacyLibDir = Join-Path $Prefix "lib\every"

function Get-EveryTasks {
  try {
    $rows = & schtasks.exe /Query /FO CSV /NH 2>$null
    if ($LASTEXITCODE -ne 0) { return @() }
    return @($rows | ConvertFrom-Csv -Header TaskName,NextRun,Status |
      Where-Object { $_.TaskName -match '^\\every\\' })
  } catch {
    return @()
  }
}

# ---- uninstall ------------------------------------------------------------

if ($Uninstall) {
  if (-not ((Test-Path $binary) -or (Test-Path $legacyShim))) {
    Die "no every install found at $Prefix"
  }

  # Removing the launcher out from under live tasks is exactly the silent
  # breakage every exists to kill: the service keeps firing and every run dies.
  $tasks = @(Get-EveryTasks)
  if ($tasks.Count -gt 0 -and -not $Force) {
    Say "Still scheduled:"
    $tasks | ForEach-Object { Say "  $($_.TaskName)" }
    Say ""
    Say "Remove them first, so nothing is left pointing at a deleted every:"
    $tasks | ForEach-Object { Say "  every rm $($_.TaskName -replace '^\\every\\','')" }
    Die "nothing removed (use -Force to uninstall anyway)"
  }
  if ($tasks.Count -gt 0) {
    Write-Warning "leaving live Task Scheduler tasks behind because -Force was used"
  }

  if (Test-Path $Prefix) { Remove-Item -Recurse -Force $Prefix }
  Say "every uninstalled from $Prefix"
  Say "Tasks and logs were kept in $dataDir"
  exit 0
}

# ---- platform -------------------------------------------------------------
# Matches the archive names pinned in .goreleaser.yaml. Changing either without
# the other breaks every download.

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
  "AMD64" { "amd64" }
  "ARM64" { "arm64" }
  "x86"   { Die "32-bit Windows is not supported — every ships amd64 and arm64" }
  default { Die "unsupported architecture: $($env:PROCESSOR_ARCHITECTURE)" }
}

# ---- source: local checkout, or download ----------------------------------

$tempDir = ""
$srcDir  = ""

try {
  # $PSCommandPath is empty when the script is piped rather than run from a
  # file -- `irm ... | iex`, which is the one-liner the README documents. Left
  # unguarded, Split-Path throws on $null under ErrorActionPreference=Stop and
  # the install dies before doing anything, which is precisely the kind of
  # distribution bug 0.3.0 shipped.
  $here = if ($PSCommandPath) { Split-Path -Parent $PSCommandPath } else { $null }
  $canBuildLocally = $here -and
                     (-not $Version) -and
                     (Test-Path (Join-Path $here "go.mod")) -and
                     (Test-Path (Join-Path $here "cmd\every")) -and
                     ($null -ne (Get-Command go.exe -ErrorAction SilentlyContinue))

  $tempDir = Join-Path ([IO.Path]::GetTempPath()) ("every-install-" + [Guid]::NewGuid().ToString("N"))
  New-Item -ItemType Directory -Path $tempDir -Force | Out-Null

  if ($canBuildLocally) {
    # A checkout with a Go toolchain builds what is in front of it, so running
    # the installer in a working tree tests the code being changed rather than
    # silently installing the last release over it.
    Step "building from $here"
    $env:CGO_ENABLED = "0"
    & go.exe build -trimpath -ldflags "-s -w" -o (Join-Path $tempDir "every.exe") (Join-Path $here "cmd\every")
    if ($LASTEXITCODE -ne 0) { Die "go build failed" }
    $srcDir = $here
    $stagedBinary = Join-Path $tempDir "every.exe"
  }
  else {
    if (-not $Version) {
      try {
        $latest = Invoke-RestMethod -UseBasicParsing `
          -Uri "https://api.github.com/repos/$repo/releases/latest"
        $Version = $latest.tag_name
      } catch {
        Die "couldn't resolve the latest release (rate-limited or offline) — retry with -Version X.Y.Z"
      }
    }
    $Version = $Version -replace '^v',''
    $name = "every_${Version}_windows_${arch}.zip"
    $url  = "https://github.com/$repo/releases/download/v$Version/$name"
    $zip  = Join-Path $tempDir $name

    Step "downloading v$Version (windows/$arch)"
    try {
      Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $zip
    } catch {
      Die "download failed: $url`nIf v$Version predates 0.4.0 it has no binaries — those releases installed from source."
    }

    # Verify against the release checksums. A truncated or tampered download
    # must fail loudly rather than install a broken binary.
    #
    # Downloaded to a file rather than read from .Content: GitHub serves release
    # assets as application/octet-stream, and PowerShell hands back a BYTE ARRAY
    # for a non-text content type. Splitting that on newlines matched nothing,
    # so verification was silently skipped with a warning nobody would read --
    # a checksum that never runs is worse than no checksum, because it looks
    # like one ran.
    $sumsFile = Join-Path $tempDir "checksums.txt"
    $haveSums = $true
    try {
      Invoke-WebRequest -UseBasicParsing -OutFile $sumsFile `
        -Uri "https://github.com/$repo/releases/download/v$Version/checksums.txt"
    } catch {
      $haveSums = $false
      Write-Warning "no checksums.txt for v$Version — skipping verification"
    }

    if ($haveSums) {
      $expected = $null
      foreach ($line in (Get-Content $sumsFile)) {
        # "<sha256>  <name>" -- split on whitespace instead of matching a
        # regex against the whole line, which is one fewer thing to get wrong.
        $parts = $line.Trim() -split '\s+'
        if ($parts.Count -ge 2 -and $parts[1] -eq $name) { $expected = $parts[0]; break }
      }
      if (-not $expected) {
        # The file exists and this archive is not in it. That is not a release
        # too old to have checksums; it is a mismatch worth stopping for.
        Die "$name is not listed in checksums.txt for v$Version — refusing to install an unverified download"
      }
      $actual = (Get-FileHash -Algorithm SHA256 $zip).Hash.ToLower()
      if ($actual -ne $expected.ToLower()) {
        Die "checksum mismatch for $name`n  expected $expected`n  got      $actual`nThis is either a corrupted download or a tampered release. Nothing was installed."
      }
      Step "checksum verified"
    }

    $extract = Join-Path $tempDir "x"
    Expand-Archive -Path $zip -DestinationPath $extract -Force
    $stagedBinary = Join-Path $extract "every.exe"
    if (-not (Test-Path $stagedBinary)) { Die "unexpected archive layout for $name" }
    $srcDir = $extract
  }

  # ---- install ------------------------------------------------------------

  New-Item -ItemType Directory -Path $binDir -Force | Out-Null
  New-Item -ItemType Directory -Path (Split-Path -Parent $manifest) -Force | Out-Null

  # Write beside the target and move over it. Replacing a running executable in
  # place is what breaks a task that fires mid-upgrade.
  $tempBinary = Join-Path $binDir ".every.new.$PID.exe"
  Copy-Item -Force $stagedBinary $tempBinary
  Move-Item -Force $tempBinary $binary
  Set-Content -Encoding ASCII -Path $manifest -Value $binary
  Step "installed $binary"

  foreach ($pair in @(
      @{ From = "man\every.1";              To = "share\man\man1\every.1" },
      @{ From = "completions\every.bash";   To = "share\bash-completion\completions\every" },
      @{ From = "completions\_every";       To = "share\zsh\site-functions\_every" },
      @{ From = "completions\every.fish";   To = "share\fish\vendor_completions.d\every.fish" })) {
    $from = Join-Path $srcDir $pair.From
    if (Test-Path $from) {
      $to = Join-Path $Prefix $pair.To
      New-Item -ItemType Directory -Path (Split-Path -Parent $to) -Force | Out-Null
      Copy-Item -Force $from $to
      Add-Content -Encoding ASCII -Path $manifest -Value $to
      Step "installed $to"
    }
  }

  # A pre-0.4 install left a Ruby tree and a .cmd shim. The shim is what tasks
  # written by that version invoke, so it has to go before anything can be
  # confused about which launcher is current -- every's own migration rewrites
  # those tasks to name the binary on its next run.
  if (Test-Path $legacyShim) {
    Remove-Item -Force $legacyShim
    Step "removed $legacyShim (no longer needed)"
  }
  if (Test-Path $legacyLibDir) {
    Remove-Item -Recurse -Force $legacyLibDir
    Step "removed $legacyLibDir (no longer needed)"
  }

  # ---- report -------------------------------------------------------------
  # Running what we just installed is the smoke test.
  # Captured whole, THEN trimmed to one line. Piping a native command into
  # Select-Object -First 1 stops the pipeline, which terminates the process
  # early and leaves a non-zero $LASTEXITCODE -- so a perfectly good install
  # reported "installed, but 'every.exe version' failed" and printed the
  # correct version underneath. Racy, so it passed locally and failed in CI.
  $output = & $binary version 2>&1
  if ($LASTEXITCODE -ne 0) { Die "installed, but '$binary version' failed:`n$output" }
  $version = ($output | Select-Object -First 1)

  Say ""
  Say "$version -> $Prefix" 
  Say ""

  $onPath = ($env:PATH -split ';') -contains $binDir
  if (-not $onPath) {
    Write-Warning "$binDir isn't on your PATH. Add it for this user:"
    Say "  [Environment]::SetEnvironmentVariable('PATH', `"$binDir;`" + [Environment]::GetEnvironmentVariable('PATH','User'), 'User')"
  }

  Say ""
  Say "  every day 9am -- echo it ran     schedule something"
  Say "  every list                       did it run?"
  Say "  every doctor                     why isn't it running?"
}
finally {
  if ($tempDir -and (Test-Path $tempDir)) {
    Remove-Item -Recurse -Force $tempDir -ErrorAction SilentlyContinue
  }
}
