$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
$CertPath = Join-Path $Root "data\ca\ca.crt"
$StatePath = Join-Path $Root "data\ca\trusted.thumbprint"
$CaCommonName = "TimeSavior Local CA"

function Remove-UserRootByThumb([string]$Thumb) {
    if (-not $Thumb) { return }
    & certutil.exe -user -delstore Root $Thumb 2>$null | Out-Null
    Write-Host "Removed old CA attempt: $Thumb"
}

if (-not (Test-Path $CertPath)) { throw "CA file missing: $CertPath" }

$Incoming = New-Object System.Security.Cryptography.X509Certificates.X509Certificate2((Resolve-Path $CertPath).Path)
$WantThumb = $Incoming.Thumbprint.ToUpperInvariant()

if (Test-Path $StatePath) {
    $Prev = (Get-Content $StatePath -Raw).Trim().ToUpperInvariant()
    if ($Prev -and $Prev -ne $WantThumb) { Remove-UserRootByThumb $Prev }
}

$store = New-Object System.Security.Cryptography.X509Certificates.X509Store("Root", "CurrentUser")
$store.Open("ReadOnly")
try {
    foreach ($c in @($store.Certificates)) {
        if ($c.Subject -match [regex]::Escape("CN=$CaCommonName")) {
            if ($c.Thumbprint.ToUpperInvariant() -ne $WantThumb) { Remove-UserRootByThumb $c.Thumbprint }
        }
    }
} finally { $store.Close() }

$exists = $false
$store2 = New-Object System.Security.Cryptography.X509Certificates.X509Store("Root", "CurrentUser")
$store2.Open("ReadOnly")
try {
    foreach ($c in @($store2.Certificates)) {
        if ($c.Thumbprint.ToUpperInvariant() -eq $WantThumb) { $exists = $true; break }
    }
} finally { $store2.Close() }

if ($exists) {
    Write-Host "CA already trusted ($WantThumb), skip import."
} else {
    $out = & certutil.exe -user -addstore Root "$CertPath" 2>&1 | Out-String
    if ($LASTEXITCODE -ne 0) { throw "certutil import failed: $out" }
    Write-Host "Imported CA ($WantThumb)."
}

Set-Content -Path $StatePath -Value $WantThumb -NoNewline -Encoding ascii
Write-Host "Clash tips: System Proxy OFF, TUN OFF, keep port 7890."
