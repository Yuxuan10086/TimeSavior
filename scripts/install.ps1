param(
    [switch]$SkipBuild,
    [switch]$SkipAutostart,
    [switch]$NoStart
)
$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
Set-Location $Root
New-Item -ItemType Directory -Force -Path (Join-Path $Root "data") | Out-Null

Write-Host "========================================"
Write-Host " TimeSavior one-click setup"
Write-Host " Root: $Root"
Write-Host "========================================"

Get-Process -Name "timesavior","timesavior-watchdog" -ErrorAction SilentlyContinue | ForEach-Object {
    Write-Host "==> Stopping $($_.ProcessName) (pid $($_.Id))"
    Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue
}
Start-Sleep -Seconds 1

if (-not $SkipBuild) {
    Write-Host "==> [1/4] Build"
    & "$PSScriptRoot\build.ps1"
} else {
    Write-Host "==> [1/4] Build skipped"
}

$Exe = Join-Path $Root "timesavior.exe"
$Watchdog = Join-Path $Root "timesavior-watchdog.exe"
if (-not (Test-Path $Exe)) { throw "Build failed: missing $Exe" }
if (-not (Test-Path $Watchdog)) { throw "Build failed: missing $Watchdog" }

$Cert = Join-Path $Root "data\ca\ca.crt"
$Key = Join-Path $Root "data\ca\ca.key"
Write-Host "==> [2/4] Ensure CA files"
if (-not ((Test-Path $Cert) -and (Test-Path $Key))) {
    $proc = Start-Process -FilePath $Exe -ArgumentList @(
        "-config", "configs\default.yaml",
        "-no-tray", "-no-sysproxy", "-probe"
    ) -PassThru -WindowStyle Hidden
    $deadline = (Get-Date).AddSeconds(20)
    while ((Get-Date) -lt $deadline) {
        if ((Test-Path $Cert) -and (Test-Path $Key)) { break }
        Start-Sleep -Milliseconds 300
    }
    if (-not $proc.HasExited) {
        Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
    }
    Start-Sleep -Milliseconds 500
    if (-not ((Test-Path $Cert) -and (Test-Path $Key))) {
        throw "CA generation failed."
    }
    Write-Host "    CA generated."
} else {
    Write-Host "    CA files exist."
}

Write-Host "==> [3/4] Trust CA (idempotent)"
& "$PSScriptRoot\install-ca.ps1"

if (-not $SkipAutostart) {
    Write-Host "==> [4/4] Autostart (idempotent)"
    & "$PSScriptRoot\install-autostart.ps1"
} else {
    Write-Host "==> [4/4] Autostart skipped"
}

if (-not $NoStart) {
    Write-Host "==> Starting TimeSavior..."
    Start-Process -FilePath $Exe -ArgumentList @("-config", "configs\default.yaml") -WorkingDirectory $Root -WindowStyle Hidden
    Start-Sleep -Seconds 3
    $alive = Get-Process -Name "timesavior" -ErrorAction SilentlyContinue
    if (-not $alive) { throw "timesavior.exe failed to stay running." }
    Write-Host "    Running. pid=$($alive.Id)"

    # SoftBlock probe: avoid opening a managed feed inside the unlock window
    # (that would consume today's 30-minute feed-triggered unlock).
    $status = $null
    try {
        $status = Invoke-RestMethod -Uri "http://127.0.0.1:18889/__timesavior/status" -TimeoutSec 5
    } catch {
        Write-Host "    status check failed: $_"
    }
    if ($status -and $status.feed_unlock_window -and -not $status.temp_unlock_started) {
        Write-Host "    SoftBlock check skipped (inside unlock window; feed open would start 30m unlock)"
        Write-Host "    status: feed_unlock_window=$($status.feed_unlock_window) policy_day=$($status.policy_day)"
    } else {
        $code = & curl.exe -x http://127.0.0.1:18888 -k -s -o NUL -w "%{http_code}" --connect-timeout 10 "https://www.bilibili.com/x/web-interface/wbi/index"
        Write-Host "    SoftBlock check (bilibili feed): HTTP $code (expect 403)"
    }
    Write-Host "    Dashboard: http://127.0.0.1:18889/"
}

Write-Host ""
Write-Host "DONE. Re-run anytime: .\scripts\install.ps1"
Write-Host "Exit password default: timesavior"
Write-Host "Clash: System Proxy OFF, TUN OFF."