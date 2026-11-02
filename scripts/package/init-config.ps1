#requires -Version 5.1
<#
.SYNOPSIS
    First-run configuration for an extracted EzTest 0.2 package.

.DESCRIPTION
    Copies the packaged `.env.example` template to `.env`, rewrites the
    `__PACKAGE_ROOT__` tokens to the absolute path of this package and stores the
    PostgreSQL 17 connection string passed with -DatabaseUrl (or
    EZTEST_DATABASE_URL). Secret values are never printed.

    EzTest 0.2 keeps runtime configuration in the database settings center; the
    API only reads the four startup variables EZTEST_DATABASE_URL,
    EZTEST_API_PORT, EZTEST_WEB_PORT and EZTEST_EXECUTOR_PORT. The first API
    start seeds the fixed administrator `admin` / `admin123` (change it in the UI
    after the first login). The executor and PDF-renderer tokens in `.env` must
    match the packaged settings-center defaults; if you change one, change the
    same value in the settings center.

    Re-running with -Force backs up the current `.env` to `.env.bak` and renders
    it again from the template.

    The resulting `.env` is written with owner-only permissions and is the only
    file that has to be edited to point the package at a PostgreSQL 17 server.

.PARAMETER DatabaseUrl
    PostgreSQL 17 URL, for example
    postgres://eztest:secret@localhost:5433/eztest?sslmode=disable.
    When omitted, EZTEST_DATABASE_URL from the environment is used, then the
    value of an existing `.env` (with -Force), then the template placeholder
    (a warning tells you to edit `.env` before starting).

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
# Rendered values.
#
# 0.2 seeds a fixed administrator (admin / admin123) on the first API start, so
# the template values are rendered as that seed: the API ignores the admin
# variables, smoke-test.* uses them to log in. The executor/renderer tokens are
# the packaged settings-center defaults; change both sides if you change them.
# ---------------------------------------------------------------------------
$adminUsername = "admin"
$adminPassword = "admin123"
$executorToken = "eztest-dev-executor-token"
$renderToken = "eztest-dev-render-token"

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
    } elseif ($rendered -match '^EZTEST_ADMIN_USERNAME=') {
        $rendered = "EZTEST_ADMIN_USERNAME=" + $adminUsername
    } elseif ($rendered -match '^EZTEST_ADMIN_PASSWORD=') {
        $rendered = "EZTEST_ADMIN_PASSWORD=" + $adminPassword
    } elseif ($rendered -match '^EZTEST_EXECUTOR_TOKEN=') {
        $rendered = "EZTEST_EXECUTOR_TOKEN=" + $executorToken
    } elseif ($rendered -match '^EZTEST_RENDER_TOKEN=') {
        $rendered = "EZTEST_RENDER_TOKEN=" + $renderToken
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
Write-Host "  admin user   : admin / admin123 (fixed first-start seed; smoke-test.* uses it)"
Write-Host "                 Change the password in the UI after the first login and update"
Write-Host "                 EZTEST_ADMIN_PASSWORD in .env afterwards."
Write-Host "  tokens       : executor/renderer use the packaged settings-center defaults;"
Write-Host "                 if you change a token here, change the settings center value too."
Write-Host ""
Write-Host "Next steps:"
if ($dbFromTemplate) {
    Write-Host "  1. Edit $envPath and set EZTEST_DATABASE_URL."
    Write-Host "  2. Run .\start-eztest.ps1 (or: bash start-eztest.sh on Linux)."
} else {
    Write-Host "  1. Run .\start-eztest.ps1 (or: bash start-eztest.sh on Linux)."
}
Write-Host "  2. Open http://localhost:6868, sign in as admin/admin123 and change the password."
Write-Host "  3. Optional: .\smoke-test.ps1 to verify the whole stack."
exit 0
