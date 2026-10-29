#requires -Version 5.1
<#
.SYNOPSIS
    First-run configuration for an extracted EzTest package.

.DESCRIPTION
    Copies the packaged `.env.example` template to `.env`, rewrites the
    `__PACKAGE_ROOT__` tokens to the absolute path of this package, generates
    the administrator password, the AES-256 secret keyring and the executor
    Bearer token, and stores the PostgreSQL 17 connection string passed with
    -DatabaseUrl (or EZTEST_DATABASE_URL). Secret values are never printed.

    The resulting `.env` is written with owner-only permissions and is the only
    file that has to be edited to point the package at a PostgreSQL 17 server.

    Re-running with -Force backs up the current `.env` to `.env.bak` and
    regenerates the secrets. Regenerating EZTEST_SECRET_KEYS makes LLM
    credentials already stored in PostgreSQL undecryptable, so only do it on a
    fresh installation (or after clearing the stored LLM configuration).

.PARAMETER DatabaseUrl
    PostgreSQL 17 URL, for example
    postgres://eztest:secret@localhost:5433/eztest?sslmode=disable.
    When omitted, EZTEST_DATABASE_URL from the environment is used, then the
    value of an existing `.env` (with -Force), then the template placeholder.

.PARAMETER Force
    Recreate `.env` even when it already exists (the previous file is kept as
    `.env.bak`).

.PARAMETER PackageRoot
    Package root; defaults to the directory containing this script.

.NOTES
    Compatible with Windows PowerShell 5.1 and PowerShell 7+.
#>
[CmdletBinding()]
param(
    [string]$DatabaseUrl = "",
    [switch]$Force,
    [string]$PackageRoot = ""
)

$ErrorActionPreference = "Stop"

if ([string]::IsNullOrWhiteSpace($PackageRoot)) {
    $PackageRoot = $PSScriptRoot
}
$PackageRoot = (Resolve-Path -LiteralPath $PackageRoot).Path
$isWindows = ([System.IO.Path]::DirectorySeparatorChar -eq '\')

$templatePath = Join-Path $PackageRoot ".env.example"
$envPath = Join-Path $PackageRoot ".env"
$backupPath = Join-Path $PackageRoot ".env.bak"

function Fail([string]$Message) {
    Write-Host "ERROR: $Message" -ForegroundColor Red
    exit 1
}

if (-not (Test-Path -LiteralPath $templatePath -PathType Leaf)) {
    Fail "Missing .env.example next to this script ($templatePath). Extract the complete package archive and re-run."
}
if ((Test-Path -LiteralPath $envPath -PathType Leaf) -and -not $Force) {
    Write-Host "ERROR: $envPath already exists." -ForegroundColor Red
    Write-Host "       Use -Force to back it up to .env.bak and regenerate it, or edit the existing .env." -ForegroundColor Red
    exit 1
}

function Read-DotEnvValue {
    param([string]$Path, [string]$Name)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return "" }
    foreach ($line in [System.IO.File]::ReadAllLines($Path, [System.Text.Encoding]::UTF8)) {
        $trimmed = $line.Trim()
        if ($trimmed.Length -eq 0 -or $trimmed.StartsWith("#")) { continue }
        $index = $trimmed.IndexOf("=")
        if ($index -lt 1) { continue }
        if ($trimmed.Substring(0, $index).Trim() -eq $Name) {
            return $trimmed.Substring($index + 1).Trim()
        }
    }
    return ""
}

function Get-DatabaseHostPort {
    param([string]$Url)
    $rest = $Url
    $scheme = $rest.IndexOf("://")
    if ($scheme -ge 0) { $rest = $rest.Substring($scheme + 3) }
    $slash = $rest.IndexOf("/")
    if ($slash -ge 0) { $rest = $rest.Substring(0, $slash) }
    $at = $rest.LastIndexOf("@")
    if ($at -ge 0) { $rest = $rest.Substring($at + 1) }
    if ($rest -match '^\[(.+)\]:(\d+)$') {
        return [pscustomobject]@{ Host = $matches[1]; Port = [int]$matches[2] }
    }
    $colon = $rest.LastIndexOf(":")
    if ($colon -gt 0) {
        $hostPart = $rest.Substring(0, $colon)
        $portPart = $rest.Substring($colon + 1)
        if ($hostPart.Length -gt 0 -and $portPart -match '^\d+$') {
            return [pscustomobject]@{ Host = $hostPart; Port = [int]$portPart }
        }
    }
    return [pscustomobject]@{ Host = $rest; Port = 5432 }
}

# ---------------------------------------------------------------------------
# Resolve the database URL: parameter > environment > existing .env > template.
# ---------------------------------------------------------------------------
$dbUrl = $DatabaseUrl.Trim()
if ($dbUrl.Length -eq 0 -and -not [string]::IsNullOrWhiteSpace($env:EZTEST_DATABASE_URL)) {
    $dbUrl = $env:EZTEST_DATABASE_URL.Trim()
}
if ($dbUrl.Length -eq 0 -and (Test-Path -LiteralPath $envPath -PathType Leaf)) {
    $dbUrl = (Read-DotEnvValue -Path $envPath -Name "EZTEST_DATABASE_URL").Trim()
}
$dbFromTemplate = $false
if ($dbUrl.Length -eq 0 -or $dbUrl.Contains("<")) {
    $dbUrl = "postgres://eztest:<db-password>@localhost:5433/eztest?sslmode=disable"
    $dbFromTemplate = $true
} elseif ($dbUrl -notmatch '^postgres(ql)?://') {
    Fail "The database URL must start with postgres:// or postgresql:// (got a '$(($dbUrl -split ':')[0])://' URL)."
}

if (Test-Path -LiteralPath $envPath -PathType Leaf) {
    Move-Item -LiteralPath $envPath -Destination $backupPath -Force
    Write-Host "Backed up the previous .env to .env.bak"
}

# ---------------------------------------------------------------------------
# Generate secrets (Windows PowerShell 5.1 has no RandomNumberGenerator.Fill).
# ---------------------------------------------------------------------------
$rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()

function New-RandomBytes([int]$Count) {
    $bytes = New-Object byte[] $Count
    $rng.GetBytes($bytes)
    return $bytes
}

function New-Base64Key {
    return [Convert]::ToBase64String((New-RandomBytes 32))
}

function New-HexToken([int]$ByteCount) {
    $bytes = New-RandomBytes $ByteCount
    $builder = New-Object System.Text.StringBuilder
    foreach ($byte in $bytes) { [void]$builder.Append($byte.ToString("x2")) }
    return $builder.ToString()
}

function New-RandomPassword([int]$Length) {
    $alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
    # 56-char alphabet; accept bytes below 224 (56 * 4) to avoid modulo bias.
    $limit = [math]::Floor(256 / $alphabet.Length) * $alphabet.Length
    $builder = New-Object System.Text.StringBuilder
    while ($builder.Length -lt $Length) {
        foreach ($byte in (New-RandomBytes 32)) {
            if ($byte -lt $limit) {
                [void]$builder.Append($alphabet[$byte % $alphabet.Length])
                if ($builder.Length -ge $Length) { break }
            }
        }
    }
    return $builder.ToString()
}

$adminPassword = New-RandomPassword 20
$executorToken = New-HexToken 24
$secretKey = New-Base64Key
$secretKeys = '{"1":"' + $secretKey + '"}'

# ---------------------------------------------------------------------------
# Render .env from the template.
# ---------------------------------------------------------------------------
$packageRootForward = $PackageRoot.Replace('\', '/')
$lines = [System.IO.File]::ReadAllLines($templatePath, [System.Text.Encoding]::UTF8)
$output = New-Object System.Collections.ArrayList
foreach ($line in $lines) {
    $rendered = $line.Replace('__PACKAGE_ROOT__', $packageRootForward)
    if ($rendered -match '^EZTEST_DATABASE_URL=') {
        $rendered = "EZTEST_DATABASE_URL=" + $dbUrl
    } elseif ($rendered -match '^EZTEST_ADMIN_PASSWORD=') {
        $rendered = "EZTEST_ADMIN_PASSWORD=" + $adminPassword
    } elseif ($rendered -match '^EZTEST_SECRET_KEYS=') {
        $rendered = "EZTEST_SECRET_KEYS=" + $secretKeys
    } elseif ($rendered -match '^EZTEST_EXECUTOR_TOKEN=') {
        $rendered = "EZTEST_EXECUTOR_TOKEN=" + $executorToken
    }
    [void]$output.Add($rendered)
}

$utf8NoBom = New-Object System.Text.UTF8Encoding($false)
[System.IO.File]::WriteAllText($envPath, (($output -join "`n") + "`n"), $utf8NoBom)

# Owner-only permissions (never widen .env access).
$permissionsOk = $false
try {
    if ($isWindows) {
        $identity = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name
        & icacls.exe $envPath /inheritance:r /grant:r "${identity}:(F)" | Out-Null
        $permissionsOk = ($LASTEXITCODE -eq 0)
    } else {
        & chmod 600 $envPath
        $permissionsOk = ($LASTEXITCODE -eq 0)
    }
} catch {
    $permissionsOk = $false
}
if (-not $permissionsOk) {
    Write-Host "WARNING: could not restrict .env permissions to the current user; restricting it manually is recommended." -ForegroundColor Yellow
}

# ---------------------------------------------------------------------------
# Summary (never print secret values).
# ---------------------------------------------------------------------------
Write-Host ""
Write-Host "EzTest package initialized." -ForegroundColor Green
Write-Host "  package root : $PackageRoot"
Write-Host "  .env         : $envPath (owner-only permissions)"
if ($dbFromTemplate) {
    Write-Host "  database     : NOT SET - edit EZTEST_DATABASE_URL in .env before starting" -ForegroundColor Yellow
    Write-Host "                 example: postgres://eztest:<password>@localhost:5433/eztest?sslmode=disable" -ForegroundColor Yellow
    Write-Host "                 (PostgreSQL 17 must be running; see PACKAGE.md)" -ForegroundColor Yellow
} else {
    $endpoint = Get-DatabaseHostPort -Url $dbUrl
    Write-Host "  database     : $($endpoint.Host):$($endpoint.Port) (credentials are in .env, not shown)"
}
Write-Host "  admin user   : admin (password is in .env, not shown)"
Write-Host "  secrets      : generated EZTEST_SECRET_KEYS, EZTEST_ADMIN_PASSWORD, EZTEST_EXECUTOR_TOKEN"
Write-Host ""
Write-Host "Next steps:"
if ($dbFromTemplate) {
    Write-Host "  1. Edit $envPath and set EZTEST_DATABASE_URL."
    Write-Host "  2. Run .\start-eztest.ps1 (or: bash start-eztest.sh on Linux)."
} else {
    Write-Host "  1. Run .\start-eztest.ps1 (or: bash start-eztest.sh on Linux)."
}
Write-Host "  2. Open http://localhost:6868 and sign in as admin."
Write-Host "  3. Optional: .\smoke-test.ps1 to verify the whole stack."
exit 0
