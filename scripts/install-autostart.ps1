$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
$Core = Join-Path $Root "timesavior.exe"
$Config = Join-Path $Root "configs\default.yaml"

if (-not (Test-Path $Core)) { throw "Missing $Core" }
if (-not (Test-Path $Config)) { throw "Missing $Config" }

$startup = [Environment]::GetFolderPath("Startup")
$cmdPath = Join-Path $startup "TimeSavior.cmd"
# Start the core (tray) first; it will keep watchdog alive. /B avoids an extra console.
@(
    "@echo off",
    "cd /d `"$Root`"",
    "start `"`" /B `"$Core`" -config `"$Config`""
) | Set-Content -Path $cmdPath -Encoding ASCII

Write-Host "Autostart OK: $cmdPath"
