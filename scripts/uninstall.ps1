param(
    [switch]$RemoveLocalCAFiles,
    [switch]$RemoveAuditLogs
)
$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
$TaskName = "TimeSaviorWatchdog"
$CaCommonName = "TimeSavior Local CA"

Write-Host "==> Remove autostart (before killing, so mutual restart cannot linger)"
Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue
& schtasks.exe /Delete /TN $TaskName /F 2>$null | Out-Null
$startupCmd = Join-Path ([Environment]::GetFolderPath("Startup")) "TimeSavior.cmd"
if (Test-Path $startupCmd) { Remove-Item $startupCmd -Force; Write-Host "  removed $startupCmd" }

Write-Host "==> Stop processes (loop; core and watchdog restart each other)"
for ($i = 0; $i -lt 40; $i++) {
    Get-Process -Name "timesavior","timesavior-watchdog" -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
    Start-Sleep -Milliseconds 250
}
$left = @(Get-Process -Name "timesavior","timesavior-watchdog" -ErrorAction SilentlyContinue)
if ($left.Count -gt 0) {
    Write-Host "  warning: still running: $($left.Name -join ', ')"
} else {
    Write-Host "  stopped"
}

Write-Host "==> Remove CA from cert store"
$removed = @{}
$StatePath = Join-Path $Root "data\ca\trusted.thumbprint"
if (Test-Path $StatePath) {
    $Prev = (Get-Content $StatePath -Raw).Trim()
    if ($Prev) {
        & certutil.exe -user -delstore Root $Prev 2>$null | Out-Null
        $removed[$Prev] = $true
        Write-Host "  removed $Prev"
    }
    Remove-Item $StatePath -Force -ErrorAction SilentlyContinue
}
$store = New-Object System.Security.Cryptography.X509Certificates.X509Store("Root", "CurrentUser")
$store.Open("ReadOnly")
$thumbs = @()
try {
    foreach ($c in @($store.Certificates)) {
        if ($c.Subject -match [regex]::Escape("CN=$CaCommonName")) { $thumbs += $c.Thumbprint }
    }
} finally { $store.Close() }
foreach ($t in $thumbs) {
    if (-not $removed.ContainsKey($t)) {
        & certutil.exe -user -delstore Root $t 2>$null | Out-Null
        Write-Host "  removed $t"
        $removed[$t] = $true
    }
}
if ($removed.Count -eq 0) { Write-Host "  (none)" }

try {
    $listen = "127.0.0.1:18888"
    $enable = (Get-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -Name ProxyEnable -ErrorAction SilentlyContinue).ProxyEnable
    $server = (Get-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -Name ProxyServer -ErrorAction SilentlyContinue).ProxyServer
    if ($enable -eq 1 -and $server -eq $listen) {
        Set-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -Name ProxyEnable -Value 0
        Write-Host "==> Disabled system proxy pointing to TimeSavior"
    }
} catch {}

if ($RemoveLocalCAFiles) {
    $caDir = Join-Path $Root "data\ca"
    if (Test-Path $caDir) { Remove-Item $caDir -Recurse -Force }
}
if ($RemoveAuditLogs) {
    $audit = Join-Path $Root "data\audit"
    if (Test-Path $audit) { Remove-Item $audit -Recurse -Force }
}
Write-Host "Uninstall done."