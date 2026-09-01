# End-to-end test for the Windows backend, against the real Task Scheduler
# service. The unit tests cover XML and CSV shapes in isolation; this is the
# only thing that exercises schtasks and Get-ScheduledTask for real, which is
# where the bugs actually lived (an interpolated heredoc turned the '\every\'
# task path into an ESC byte, and no unit test could see it).
#
#   pwsh test/e2e/windows.ps1 -Prefix <install prefix>
#
# Expects `every` already installed at -Prefix (install.ps1), so the shim
# branch of Runtime.bin is what gets exercised — the path real users get.
param(
  [Parameter(Mandatory = $true)][string]$Prefix
)

$ErrorActionPreference = "Stop"
$Every = Join-Path $Prefix "bin\every.cmd"
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
function Same($m, $want, $got) {
  if ("$want" -eq "$got") { Ok $m } else { Bad $m "expected [$want] got [$got]" }
}
function Has($m, $hay, $needle) {
  if ("$hay" -like "*$needle*") { Ok $m }
  else { Bad $m "missing [$needle] in: $((""+$hay) -replace '\s+',' ' | ForEach-Object { $_.Substring(0, [Math]::Min(220, $_.Length)) })" }
}
function Hasnt($m, $hay, $needle) {
  if ("$hay" -like "*$needle*") { Bad $m "unexpectedly found [$needle]" } else { Ok $m }
}

# Run `every` and capture merged output + exit code.
function Every() {
  $out = & $Every @args 2>&1 | Out-String
  return [pscustomobject]@{ Out = $out; Code = $LASTEXITCODE }
}

function EveryTaskNames() {
  # Ask the service directly, not through `every` -- this is the oracle the
  # product's own loaded_names is checked against.
  $t = Get-ScheduledTask -TaskPath $TaskPath -ErrorAction SilentlyContinue
  if ($null -eq $t) { return @() }
  return @($t | ForEach-Object { $_.TaskName })
}

function RemoveAllEveryTasks() {
  Get-ScheduledTask -TaskPath $TaskPath -ErrorAction SilentlyContinue |
    ForEach-Object {
      Unregister-ScheduledTask -TaskPath $_.TaskPath -TaskName $_.TaskName -Confirm:$false -ErrorAction SilentlyContinue
    }
}

Write-Host "every E2E (Windows / Task Scheduler)" -ForegroundColor White
Write-Host "prefix     : $Prefix"
Write-Host "EVERY_HOME : $($env:EVERY_HOME)"
Write-Host "LOCALAPPDATA: $($env:LOCALAPPDATA)"

RemoveAllEveryTasks

# ---------------------------------------------------------------- environment
Sec "0. environment"
$r = Every version
Same "every version exits 0" 0 $r.Code
Has "reports 0.3.x" $r.Out "every 0."
if (Get-Service -Name Schedule -ErrorAction SilentlyContinue) { Ok "Task Scheduler service present" }
else { Bad "Task Scheduler service" "service 'Schedule' not found" }

$dataDir = (Every doctor).Out
Has "data dir is under LOCALAPPDATA" $dataDir "AppData\Local\every"

# ------------------------------------------------------------------ lifecycle
Sec "1. registration lifecycle against the real service"
$r = Every 15m --name probe -- echo probe-ran
Same "add exits 0" 0 $r.Code

$names = EveryTaskNames
if ($names -contains "probe") { Ok "task really exists under \every\ (asked the service)" }
else { Bad "task registered" "Get-ScheduledTask under $TaskPath returned: $($names -join ', ')" }

# THE regression that shipped in 0.3.0-rc: loaded_names could never succeed.
$r = Every list
Same "list exits 0" 0 $r.Code
Has "list names the task" $r.Out "probe"
Hasnt "list did not fall over on the state query" $r.Out "state query failed"
Hasnt "list shows no raw exception" $r.Out "RuntimeError"

$r = Every doctor
Same "doctor exits 0" 0 $r.Code
Hasnt "doctor did not fall over on the state query" $r.Out "state query failed"

# pause -> the service should report Disabled; resume -> back to Ready.
$r = Every pause probe
Same "pause exits 0" 0 $r.Code
$state = (Get-ScheduledTask -TaskPath $TaskPath -TaskName probe).State
Same "service reports Disabled after pause" "Disabled" $state
Has "list reflects paused" (Every list).Out "paused"

$r = Every resume probe
Same "resume exits 0" 0 $r.Code
$state = (Get-ScheduledTask -TaskPath $TaskPath -TaskName probe).State
if ("$state" -ne "Disabled") { Ok "service no longer reports Disabled after resume (State=$state)" }
else { Bad "resume re-enabled the task" "State is still Disabled" }

# --------------------------------------------------------------- the XML/args
Sec "2. task action (stable shim, not a pinned Ruby path)"
$xml = & schtasks.exe /Query /TN "\every\probe" /XML 2>&1 | Out-String
Has "action invokes cmd.exe" $xml "cmd.exe"
Has "action calls the installed every.cmd shim" $xml "every.cmd"
Hasnt "action does NOT hardcode the Ruby interpreter path" $xml "ruby.exe"

# ------------------------------------------------------------------ execution
Sec "3. command execution and quoting (the temp-script path)"
$r = Every run probe
Same "run exits 0" 0 $r.Code
Has "run output reaches the log" (Every log probe).Out "probe-ran"

# The bug the whole temporary-script design exists to fix: cmd.exe cannot be
# handed a quoted command as an argv tail without a second escaping layer.
$null = Every 15m --name quoted -- echo "my file.txt"
$r = Every run quoted
Same "quoted command exits 0" 0 $r.Code
$log = (Every log quoted).Out
Has "quoted argument survives intact" $log "my file.txt"
Hasnt "no cmd escaping artefact" $log '\"'
Hasnt "no 'not recognized' failure" $log "is not recognized"

# @echo off: a batch file runs with ECHO ON, so without it every command line
# is copied into the captured output.
Hasnt "command is not echoed into the log" $log "echo ""my file.txt"""

# Metacharacters that cmd treats specially.
$null = Every 15m --name amper -- cmd /c "echo a^&b"
$r = Every run amper
Same "ampersand command exits 0" 0 $r.Code

$null = Every 15m --name percent -- echo 100%%
$r = Every run percent
Same "percent command exits 0" 0 $r.Code

$null = Every 15m --name spaced -- cmd /c "echo one   two"
$r = Every run spaced
Has "runs of spaces are preserved" (Every log spaced).Out "one   two"

# Failure propagation.
$null = Every 15m --name failing -- cmd /c "exit 42"
$null = Every run failing
Has "non-zero exit is recorded" (Every list --json).Out '"exit":42'

# stderr must be captured too.
$null = Every 15m --name onerr -- cmd /c "echo to-stderr 1>&2"
$null = Every run onerr
Has "stderr is merged into the log" (Every log onerr).Out "to-stderr"

# Timeout has to actually kill the process on Windows.
$null = Every 15m --name slowpoke --timeout 5s -- cmd /c "ping -n 30 127.0.0.1 >nul"
$sw = [Diagnostics.Stopwatch]::StartNew()
$null = Every run slowpoke
$sw.Stop()
if ($sw.Elapsed.TotalSeconds -lt 25) { Ok "timeout kills the run ($([int]$sw.Elapsed.TotalSeconds)s)" }
else { Bad "timeout" "took $([int]$sw.Elapsed.TotalSeconds)s" }
Has "timeout marker written to the log" (Every log slowpoke).Out "killed after"

# ------------------------------------------------------------------- contract
Sec "4. --json contract"
$j = (Every list --json).Out
try { $parsed = $j | ConvertFrom-Json; Ok "list --json parses" }
catch { Bad "list --json parses" $_.Exception.Message; $parsed = $null }
if ($parsed) {
  $probe = @($parsed) | Where-Object { $_.name -eq "probe" }
  if ($probe) { Ok "json contains the probe task" } else { Bad "json probe" "not present" }
  foreach ($k in @("name","schedule","command","status")) {
    if ($parsed[0].PSObject.Properties.Name -contains $k) { Ok "json has '$k'" }
    else { Bad "json field '$k'" "absent" }
  }
}

# --------------------------------------------------------------- error paths
Sec "5. error paths and exit codes"
Same "log of unknown task exits 66" 66 (Every log nosuch).Code
Same "rm of unknown task exits 66"  66 (Every rm nosuch).Code
Same "pause of unknown task exits 66" 66 (Every pause nosuch).Code
$r = Every banana -- echo hi
Same "bad schedule exits 64" 64 $r.Code
Hasnt "bad schedule prints no backtrace" $r.Out ".rb:"

# Documented Windows floor: interval schedules start at 1m.
$r = Every 15s --name toofast -- echo nope
Same "sub-minute interval is rejected" 64 $r.Code
Has "rejection explains the 1m floor" $r.Out "1m"
if ((EveryTaskNames) -notcontains "toofast") { Ok "rejected task was not registered" }
else { Bad "rejected task leaked" "toofast exists in the scheduler" }

# ------------------------------------------------------------------- removal
Sec "6. removal, including the already-absent case"
$r = Every rm probe
Same "rm exits 0" 0 $r.Code
if ((EveryTaskNames) -notcontains "probe") { Ok "task really gone from the service" }
else { Bad "rm left the task registered" "probe still under $TaskPath" }
Hasnt "rm'd task is gone from list" (Every list).Out "probe"

# The #7 guard: a task the service no longer has must still `rm` cleanly,
# or the store entry is stranded with no way to remove it.
$null = Every 15m --name orphan -- echo orphan
Unregister-ScheduledTask -TaskPath $TaskPath -TaskName orphan -Confirm:$false -ErrorAction SilentlyContinue
$r = Every rm orphan
Same "rm of a task the service already dropped exits 0" 0 $r.Code
Hasnt "orphan cleared from the store" (Every list).Out "orphan"

# ------------------------------------------------------------------ PowerShell
Sec "7. PowerShell command shell (EVERY_SHELL)"
$pwshPath = (Get-Command powershell.exe -ErrorAction SilentlyContinue).Source
if ($pwshPath) {
  $env:EVERY_SHELL = $pwshPath
  $null = Every 15m --name psjob -- Write-Output 'from-powershell'
  $r = Every run psjob
  Same "PowerShell-shell task exits 0" 0 $r.Code
  Has "PowerShell task output captured" (Every log psjob).Out "from-powershell"
  $null = Every rm psjob
  Remove-Item Env:\EVERY_SHELL -ErrorAction SilentlyContinue
} else {
  Write-Host "  SKIP powershell.exe not found"
}

# -------------------------------------------------------------------- hygiene
Sec "8. cleanup leaves the scheduler clean"
foreach ($n in @("quoted","amper","percent","spaced","failing","onerr","slowpoke")) {
  $null = Every rm $n
}
$left = EveryTaskNames
if ($left.Count -eq 0) { Ok "no tasks left under \every\" }
else { Bad "leftover tasks" ($left -join ", ") }

$leftFiles = @()
$taskDir = Join-Path $env:LOCALAPPDATA "every\windows-tasks"
if (Test-Path $taskDir) {
  $leftFiles = @(Get-ChildItem $taskDir -Filter *.xml -ErrorAction SilentlyContinue)
}
if ($leftFiles.Count -eq 0) { Ok "no task XML left behind" }
else { Bad "leftover XML" (($leftFiles | ForEach-Object { $_.Name }) -join ", ") }

$temp = @(Get-ChildItem $env:TEMP -Filter "every-command*" -ErrorAction SilentlyContinue)
if ($temp.Count -eq 0) { Ok "no leaked every-command temp scripts" }
else { Bad "leaked temp scripts" "$($temp.Count) left in $env:TEMP" }

RemoveAllEveryTasks

Write-Host ""
Write-Host "-- $($script:pass) passed, $($script:fail) failed --" -ForegroundColor White
if ($script:fail -gt 0) { exit 1 }
exit 0
