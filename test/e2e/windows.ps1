# End-to-end test for the Windows backend, against the real Task Scheduler
# service. The unit tests cover XML and CSV shapes in isolation; this is the
# only thing that exercises schtasks and Get-ScheduledTask for real, which is
# where the bugs actually lived (an interpolated heredoc turned the '\every\'
# task path into an ESC byte, and no unit test could see it).
#
#   pwsh test/e2e/windows.ps1 -Prefix <install prefix>
#
# Expects `every` already installed at -Prefix (install.ps1), so the installed
# binary is what gets exercised -- the path real users get. Before 0.4 this
# drove a generated every.cmd shim whose only job was to keep a Ruby
# interpreter path out of every task; there is no interpreter now.
param(
  [Parameter(Mandatory = $true)][string]$Prefix
)

$ErrorActionPreference = "Stop"

# PowerShell 7+ only, and it fails fast rather than lying.
#
# Windows PowerShell 5.1 re-tokenises native-command arguments that contain
# quotes: `-- 'echo "my file.txt"'` arrives at every.exe as several arguments,
# and the seeding and quoting assertions below fail for a reason that has
# nothing to do with every. PowerShell 7 fixed it
# ($PSNativeCommandArgumentPassing = 'Standard'), which is what CI runs, so the
# failures only ever appeared on a developer's own machine -- where they read
# exactly like real bugs.
if ($PSVersionTable.PSVersion.Major -lt 6) {
  Write-Host "every E2E needs PowerShell 7 (pwsh); this is $($PSVersionTable.PSVersion)." -ForegroundColor Red
  Write-Host "  5.1 re-tokenises native arguments containing quotes, so the quoting"
  Write-Host "  assertions would fail for reasons unrelated to every."
  Write-Host "  Run: pwsh -File test/e2e/windows.ps1 -Prefix <prefix>"
  exit 2
}
$EveryCmd = Join-Path $Prefix "bin\every.exe"
$script:pass = 0
$script:fail = 0
$TaskPath = "\every\"

function Ok($m)  { Write-Host "  PASS $m" -ForegroundColor Green; $script:pass++ }
function Bad($m, $d) {
  Write-Host "  FAIL $m" -ForegroundColor Red
  if ($d) { Write-Host "       $d" -ForegroundColor DarkGray }
  $script:fail++
}
function Sec($m) { Write-Host ""; Write-Host "-- $m --" -ForegroundColor Cyan }

# Collapse whitespace and clip, so a failure prints one readable line.
function Trunc($text) {
  $s = ($text | Out-String) -replace '\s+', ' '
  if ($s.Length -gt 220) { return $s.Substring(0, 220) }
  return $s
}
function Same($m, $want, $got) {
  if ("$want" -eq "$got") { Ok $m } else { Bad $m "expected [$want] got [$got]" }
}
function Has($m, $hay, $needle) {
  if ("$hay" -like "*$needle*") { Ok $m }
  else { Bad $m ("missing [" + $needle + "] in: " + (Trunc $hay)) }
}
function Hasnt($m, $hay, $needle) {
  if ("$hay" -like "*$needle*") { Bad $m ("unexpectedly found [" + $needle + "] in: " + (Trunc $hay)) }
  else { Ok $m }
}

# Arguments arrive as ONE explicit array, never as loose tokens: PowerShell
# treats a bare `--` as its own end-of-parameters marker and swallows it, and
# `--` is exactly what separates an `every` schedule from its command. Passing
# the array keeps the separator intact.
#
# ErrorActionPreference is relaxed around the call because a native command
# writing to stderr can otherwise raise a terminating error, aborting the suite
# instead of letting one assertion record a failure.
function Every([string[]]$A) {
  $prev = $ErrorActionPreference
  $ErrorActionPreference = "Continue"
  try {
    $out = & $EveryCmd @A 2>&1 | Out-String
    $code = $LASTEXITCODE
  } finally {
    $ErrorActionPreference = $prev
  }
  return [pscustomobject]@{ Out = $out; Code = $code }
}

# Ask the service directly. This is the oracle `every list` is checked against,
# so it must never go through `every` itself.
function EveryTaskNames() {
  $t = Get-ScheduledTask -TaskPath $TaskPath -ErrorAction SilentlyContinue
  if ($null -eq $t) { return @() }
  return @($t | ForEach-Object { $_.TaskName })
}

function TaskState($name) {
  $t = Get-ScheduledTask -TaskPath $TaskPath -TaskName $name -ErrorAction SilentlyContinue
  if ($null -eq $t) { return "<absent>" }
  return "$($t.State)"
}

function RemoveAllEveryTasks() {
  Get-ScheduledTask -TaskPath $TaskPath -ErrorAction SilentlyContinue |
    ForEach-Object {
      Unregister-ScheduledTask -TaskPath $_.TaskPath -TaskName $_.TaskName `
        -Confirm:$false -ErrorAction SilentlyContinue
    }
}

Write-Host "every E2E (Windows / Task Scheduler)" -ForegroundColor White
Write-Host "prefix      : $Prefix"
Write-Host "LOCALAPPDATA: $($env:LOCALAPPDATA)"

RemoveAllEveryTasks

# ---------------------------------------------------------------- environment
Sec "0. environment"
$r = Every @('version')
Same "every version exits 0" 0 $r.Code
Has "reports a version" $r.Out "every 0."
if (Get-Service -Name Schedule -ErrorAction SilentlyContinue) { Ok "Task Scheduler service present" }
else { Bad "Task Scheduler service" "service 'Schedule' not found" }

$doc = (Every @('doctor')).Out
Has "data dir is under LOCALAPPDATA" $doc "AppData/Local/every"
Hasnt "data dir has no mixed separators" $doc "Local/every\"

# ------------------------------------------------------------------ lifecycle
Sec "1. registration lifecycle against the real service"
$r = Every @('15m', '--name', 'probe', '--', 'echo probe-ran')
Same "add exits 0" 0 $r.Code
if ($r.Code -ne 0) { Write-Host "       add said: $(Trunc $r.Out)" -ForegroundColor DarkGray }

$names = EveryTaskNames
if ($names -contains "probe") { Ok "task really exists under \every\ (asked the service)" }
else { Bad "task registered" "Get-ScheduledTask under $TaskPath returned: $($names -join ', ')" }

# THE regression that shipped in 0.3.0-rc: loaded_names could never succeed.
$r = Every @('list')
Same "list exits 0" 0 $r.Code
Has "list names the task" $r.Out "probe"
Hasnt "list did not fall over on the state query" $r.Out "state query failed"
Hasnt "list shows no raw exception" $r.Out "RuntimeError"

$r = Every @('doctor')
Same "doctor exits 0 on a healthy task" 0 $r.Code
Hasnt "doctor did not fall over on the state query" $r.Out "state query failed"

$r = Every @('pause', 'probe')
Same "pause exits 0" 0 $r.Code
Same "service itself reports Disabled after pause" "Disabled" (TaskState "probe")
Has "list reflects paused" (Every @('list')).Out "paused"

$r = Every @('resume', 'probe')
Same "resume exits 0" 0 $r.Code
$st = TaskState "probe"
if ($st -ne "Disabled" -and $st -ne "<absent>") { Ok "service no longer reports Disabled after resume (State=$st)" }
else { Bad "resume re-enabled the task" "State is $st" }

# --------------------------------------------------------------- the XML/args
Sec "2. task action targets the installed binary, with no interpreter"
$xml = & schtasks.exe /Query /TN "\every\probe" /XML 2>&1 | Out-String
# cmd.exe is still the Command: Task Scheduler exposes no per-task environment
# block, so EVERY_HOME is set inline before the binary is called.
Has "action invokes cmd.exe" $xml "cmd.exe"
Has "action calls the installed every.exe" $xml "every.exe"
Has "action pins EVERY_HOME inline" $xml "EVERY_HOME="
Hasnt "action does NOT hardcode a Ruby interpreter path" $xml "ruby.exe"
Hasnt "action does not reference the retired .cmd shim" $xml "every.cmd"

# ------------------------------------------------------------------ execution
Sec "3. command execution and quoting (the temp-script path)"
$r = Every @('run', 'probe')
Same "run exits 0" 0 $r.Code
Has "run output reaches the log" (Every @('log', 'probe')).Out "probe-ran"

# The bug the whole temporary-script design exists to fix: cmd.exe cannot be
# handed a quoted command as an argv tail without a second escaping layer.
# Passed as ONE token, the documented form: -- 'echo "my file.txt"'
$null = Every @('15m', '--name', 'quoted', '--', 'echo "my file.txt"')
$r = Every @('run', 'quoted')
Same "quoted command exits 0" 0 $r.Code
$log = (Every @('log', 'quoted')).Out
Has "quoted argument survives intact" $log "my file.txt"
Hasnt "no 'not recognized' failure" $log "is not recognized"
# @echo off: a batch file runs with ECHO ON, so without it cmd copies every
# command line into the captured output.
Hasnt "command is not echoed into the log" $log 'echo "my file.txt"'

# A caret-escaped ampersand is how a batch file quotes one. It has to reach the
# generated .cmd verbatim -- the old argv approach mangled exactly this.
# (Plain `cmd /c "echo a&b"` is NOT a valid check: cmd strips the outer quotes
# before the inner cmd sees them, so & separates commands. That is cmd being
# correct, not `every` losing anything.)
$null = Every @('15m', '--name', 'amper', '--', 'echo a^&b')
$r = Every @('run', 'amper')
Same "caret-escaped ampersand exits 0" 0 $r.Code
Has "ampersand reaches the batch file intact" (Every @('log', 'amper')).Out "a&b"

# Runs of spaces cannot be delivered through PowerShell -> .cmd -> argv: the
# chain re-tokenises before `every` sees anything, and `every` documents that
# tokens after `--` are joined with single spaces, like cron. So seed the store
# directly to test the thing that IS `every`'s: that whatever command it holds
# reaches the generated temp script byte-for-byte.
# Seeded through every's own hidden hook rather than a helper script, so this
# no longer needs an interpreter or knowledge of the install layout.
$null = Every @('__seed', 'spaced', 'cmd /c "echo one   two"')
if ($LASTEXITCODE -eq 0) { Ok "seeded a command with runs of spaces" }
else { Bad "seed failed" "__seed exited $LASTEXITCODE" }
$null = Every @('run', 'spaced')
Has "runs of spaces survive into the temp script" (Every @('log', 'spaced')).Out "one   two"

$null = Every @('15m', '--name', 'failing', '--', 'cmd /c "exit 42"')
$null = Every @('run', 'failing')
Has "non-zero exit is recorded in the ledger" (Every @('list', '--json')).Out '"exit":42'

$null = Every @('15m', '--name', 'onerr', '--', 'cmd /c "echo to-stderr 1>&2"')
$null = Every @('run', 'onerr')
Has "stderr is merged into the log" (Every @('log', 'onerr')).Out "to-stderr"

# Timeout has to actually kill the process on Windows.
$null = Every @('15m', '--name', 'slowpoke', '--timeout', '5s', '--', 'cmd /c "ping -n 30 127.0.0.1 >nul"')
$sw = [Diagnostics.Stopwatch]::StartNew()
$null = Every @('run', 'slowpoke')
$sw.Stop()
if ($sw.Elapsed.TotalSeconds -lt 25) { Ok "timeout kills the run ($([int]$sw.Elapsed.TotalSeconds)s)" }
else { Bad "timeout" "took $([int]$sw.Elapsed.TotalSeconds)s" }
Has "timeout marker written to the log" (Every @('log', 'slowpoke')).Out "killed after"

# ------------------------------------------------------------------- contract
Sec "4. --json contract"
$j = (Every @('list', '--json')).Out
$parsed = $null
try { $parsed = $j | ConvertFrom-Json; Ok "list --json parses" }
catch { Bad "list --json parses" $_.Exception.Message }
if ($parsed) {
  $all = @($parsed)
  if (@($all | Where-Object { $_.name -eq "probe" }).Count -eq 1) { Ok "json contains the probe task" }
  else { Bad "json probe" "not present" }
  $first = $all[0]
  foreach ($k in @("name", "schedule", "command", "status")) {
    if ($first.PSObject.Properties.Name -contains $k) { Ok "json has '$k'" }
    else { Bad "json field '$k'" "absent" }
  }
}

# --------------------------------------------------------------- error paths
Sec "5. error paths and exit codes"
Same "log of unknown task exits 66"   66 (Every @('log', 'nosuch')).Code
Same "rm of unknown task exits 66"    66 (Every @('rm', 'nosuch')).Code
Same "pause of unknown task exits 66" 66 (Every @('pause', 'nosuch')).Code
$r = Every @('banana', '--', 'echo hi')
Same "bad schedule exits 64" 64 $r.Code
# Language-neutral: a Ruby backtrace names a .rb file, a Go panic names a .go
# file and a goroutine. Neither may reach a user.
Hasnt "bad schedule prints no backtrace" $r.Out ".rb:"
Hasnt "bad schedule prints no stack trace" $r.Out "goroutine"

# Documented Windows floor: interval schedules start at 1m.
$r = Every @('15s', '--name', 'toofast', '--', 'echo nope')
Same "sub-minute interval is rejected" 64 $r.Code
Has "rejection explains the 1m floor" $r.Out "1m"
if ((EveryTaskNames) -notcontains "toofast") { Ok "rejected task was not registered" }
else { Bad "rejected task leaked" "toofast exists in the scheduler" }

# ------------------------------------------------------------------- removal
Sec "6. removal, including the already-absent case"
$r = Every @('rm', 'probe')
Same "rm exits 0" 0 $r.Code
if ((EveryTaskNames) -notcontains "probe") { Ok "task really gone from the service" }
else { Bad "rm left the task registered" "probe still under $TaskPath" }
Hasnt "rm'd task is gone from list" (Every @('list')).Out "probe"

# The #7 guard: a task the service no longer has must still `rm` cleanly, or
# the store entry is stranded with no way to remove it.
$null = Every @('15m', '--name', 'orphan', '--', 'echo orphan')
Unregister-ScheduledTask -TaskPath $TaskPath -TaskName orphan -Confirm:$false -ErrorAction SilentlyContinue
$r = Every @('rm', 'orphan')
Same "rm of a task the service already dropped exits 0" 0 $r.Code
Hasnt "orphan cleared from the store" (Every @('list')).Out "orphan"

# ----------------------------------------------------------------- PowerShell
Sec "7. PowerShell command shell (EVERY_SHELL)"
$psExe = (Get-Command powershell.exe -ErrorAction SilentlyContinue).Source
if ($psExe) {
  $env:EVERY_SHELL = $psExe
  $null = Every @('15m', '--name', 'psjob', '--', "Write-Output 'from-powershell'")
  $r = Every @('run', 'psjob')
  Same "PowerShell-shell task exits 0" 0 $r.Code
  Has "PowerShell task output captured" (Every @('log', 'psjob')).Out "from-powershell"
  $null = Every @('rm', 'psjob')
  Remove-Item Env:\EVERY_SHELL -ErrorAction SilentlyContinue
} else {
  Write-Host "  SKIP powershell.exe not found"
}

# -------------------------------------------------------------------- hygiene
Sec "8. cleanup leaves the scheduler clean"
foreach ($n in @('quoted', 'amper', 'spaced', 'failing', 'onerr', 'slowpoke')) {
  $null = Every @('rm', $n)
}
$left = EveryTaskNames
if ($left.Count -eq 0) { Ok "no tasks left under \every\" }
else { Bad "leftover tasks" ($left -join ", ") }

$taskDir = Join-Path $env:LOCALAPPDATA "every\windows-tasks"
$leftFiles = @()
if (Test-Path $taskDir) {
  $leftFiles = @(Get-ChildItem $taskDir -Filter *.xml -ErrorAction SilentlyContinue)
}
if ($leftFiles.Count -eq 0) { Ok "no task XML left behind" }
else { Bad "leftover XML" (($leftFiles | ForEach-Object { $_.Name }) -join ", ") }

# The generated .runner.rb wrappers are gone with the interpreter. If any
# appear, something is still writing the pre-0.4 layout.
$wrappers = @()
if (Test-Path $taskDir) {
  $wrappers = @(Get-ChildItem $taskDir -Filter *.runner.rb -ErrorAction SilentlyContinue)
}
if ($wrappers.Count -eq 0) { Ok "no Ruby wrapper scripts generated" }
else { Bad "wrapper scripts" (($wrappers | ForEach-Object { $_.Name }) -join ", ") }

$temp = @(Get-ChildItem $env:TEMP -Filter "every-command*" -ErrorAction SilentlyContinue)
if ($temp.Count -eq 0) { Ok "no leaked every-command temp scripts" }
else { Bad "leaked temp scripts" "$($temp.Count) left in $env:TEMP" }

RemoveAllEveryTasks

Write-Host ""
Write-Host "-- $($script:pass) passed, $($script:fail) failed --" -ForegroundColor White
if ($script:fail -gt 0) { exit 1 }
exit 0
