$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
Set-Location $Root

$env:GOPROXY = "https://goproxy.cn,direct"
$goCandidates = @(
    (Get-Command go -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Source),
    "$env:USERPROFILE\sdk\go\bin\go.exe",
    "C:\Program Files\Go\bin\go.exe"
) | Where-Object { $_ -and (Test-Path $_) }

if (-not $goCandidates) {
    throw "go.exe not found. Install Go or put it on PATH."
}
$GoExe = $goCandidates | Select-Object -First 1
$env:GOROOT = Split-Path (Split-Path $GoExe)
$env:PATH = "$(Split-Path $GoExe);$env:PATH"
if (-not ($env:HTTP_PROXY -or $env:HTTPS_PROXY)) {
    $ok = $false
    try {
        $ok = (Test-NetConnection -ComputerName 127.0.0.1 -Port 7890 -WarningAction SilentlyContinue).TcpTestSucceeded
    } catch {}
    if ($ok) {
        $env:HTTP_PROXY = "http://127.0.0.1:7890"
        $env:HTTPS_PROXY = "http://127.0.0.1:7890"
    }
}

Write-Host "==> go test"
& $GoExe test ./...
Write-Host "==> go build (windowsgui, no console window)"
$ld = "-H=windowsgui"
& $GoExe build -ldflags $ld -o timesavior.exe ./cmd/timesavior
& $GoExe build -ldflags $ld -o timesavior-watchdog.exe ./cmd/watchdog

$legacy = Join-Path $Root "watchdog.exe"
if (Test-Path $legacy) { Remove-Item $legacy -Force }

Write-Host "OK: timesavior.exe + timesavior-watchdog.exe (no CMD window)"