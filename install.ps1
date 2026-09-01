param(
  [string]$Prefix = "",
  [string]$Version = "",
  [string]$Ref = "",
  [switch]$Uninstall,
  [switch]$Force
)

$ErrorActionPreference = "Stop"
$repo = "Serhii-Leniv/every"

function Say([string]$Message) { Write-Host $Message }
function Die([string]$Message) {
  Write-Error "every: $Message"
  exit 1
}

if (-not $Prefix) {
  $Prefix = Join-Path $env:LOCALAPPDATA "Programs\every"
}
$Prefix = [IO.Path]::GetFullPath($Prefix)
$libDir = Join-Path $Prefix "lib\every"
$binDir = Join-Path $Prefix "bin"
$launcher = Join-Path $binDir "every.cmd"
$dataDir = Join-Path $env:LOCALAPPDATA "every"
$downloadTemp = ""

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

if ($Uninstall) {
  if (-not (Test-Path (Join-Path $libDir "bin\every"))) {
    Die "no every install found at $Prefix"
  }

  $tasks = @(Get-EveryTasks)
  if ($tasks.Count -gt 0 -and -not $Force) {
    Say "Still scheduled:"
    $tasks | ForEach-Object { Say "  $($_.TaskName)" }
    Say "Remove them first with: every rm <name>"
    Die "nothing removed (use -Force to uninstall anyway)"
  }
  if ($tasks.Count -gt 0) {
    Write-Warning "leaving live Task Scheduler tasks behind because -Force was used"
  }

  if (Test-Path $launcher) { Remove-Item -Force $launcher }
  if (Test-Path $Prefix) { Remove-Item -Recurse -Force $Prefix }
  Say "every uninstalled from $Prefix"
  Say "Tasks and logs were kept in $dataDir"
  exit 0
}

if (-not (Get-Command ruby.exe -ErrorAction SilentlyContinue)) {
  Die "ruby.exe not found — install RubyInstaller for Windows, then retry"
}
$rubyPath = (Get-Command ruby.exe).Source
$rubyVersion = & ruby.exe -e 'print RUBY_VERSION'
if ([version]$rubyVersion -lt [version]"2.6") {
  Die "Ruby $rubyVersion is too old — every needs Ruby 2.6+"
}

function Resolve-Source {
  $here = $PSScriptRoot
  if (-not $Version -and -not $Ref -and
      (Test-Path (Join-Path $here "lib\every.rb")) -and
      (Test-Path (Join-Path $here "bin\every"))) {
    return $here
  }

  if (-not $Version -and -not $Ref) {
    $release = Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest"
    $Ref = $release.tag_name
  }
  if ($Version) {
    $Ref = $Version
    if ($Ref -notmatch '^v') { $Ref = "v$Ref" }
  }
  if ($Ref -match '^v[0-9]') {
    $url = "https://github.com/$repo/archive/refs/tags/$Ref.zip"
  } else {
    $url = "https://github.com/$repo/archive/refs/heads/$Ref.zip"
  }

  $tmp = Join-Path ([IO.Path]::GetTempPath()) ("every-" + [guid]::NewGuid().ToString())
  New-Item -ItemType Directory -Path $tmp | Out-Null
  $archive = Join-Path $tmp "every.zip"
  Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $archive
  Expand-Archive -Path $archive -DestinationPath $tmp -Force
  $source = Get-ChildItem $tmp -Directory |
    Where-Object { $_.Name -ne "" } | Select-Object -First 1
  if (-not $source -or -not (Test-Path (Join-Path $source.FullName "lib\every.rb"))) {
    Die "unexpected archive layout for $Ref"
  }
  $script:downloadTemp = $tmp
  return $source.FullName
}

$source = Resolve-Source
$parent = Split-Path -Parent $Prefix
New-Item -ItemType Directory -Path $parent -Force | Out-Null

$libParent = Split-Path -Parent $libDir
New-Item -ItemType Directory -Path $libParent -Force | Out-Null
$staging = "${libDir}.new.$PID"
$previous = "${libDir}.old.$PID"
if (Test-Path $staging) { Remove-Item -Recurse -Force $staging }
if (Test-Path $previous) { Remove-Item -Recurse -Force $previous }
New-Item -ItemType Directory -Path $staging -Force | Out-Null
Copy-Item (Join-Path $source "bin") (Join-Path $staging "bin") -Recurse
Copy-Item (Join-Path $source "lib") (Join-Path $staging "lib") -Recurse

if (Test-Path $libDir) { Move-Item $libDir $previous }
Move-Item $staging $libDir
if (Test-Path $previous) { Remove-Item -Recurse -Force $previous }

New-Item -ItemType Directory -Path $binDir -Force | Out-Null
$shim = @"
@echo off
"$rubyPath" "%~dp0..\lib\every\bin\every" %*
"@
$shim | Set-Content -Encoding ASCII $launcher

$versionOutput = & $launcher version 2>&1
if ($LASTEXITCODE -ne 0) {
  Die "installed, but '$launcher version' failed: $versionOutput"
}
Say "every installed to $Prefix"
if (($env:PATH -split ';') -notcontains $binDir) {
  Say "Add this directory to PATH if needed: $binDir"
}
Say "Data and logs: $dataDir"
Say "  every day 9am -- 'echo it ran'"
Say "  every list"
Say "  every doctor"
if ($downloadTemp -and (Test-Path $downloadTemp)) {
  Remove-Item -Recurse -Force $downloadTemp
}
