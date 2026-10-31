#requires -Version 5.1
<#
.SYNOPSIS
    Loads the local .env and runs the EzTest Playwright executor.

.DESCRIPTION
    Imports every KEY=VALUE from the gitignored repository .env (without printing
    secrets) and starts the executor HTTP service with `pnpm start` from
    executor/. The service binds 127.0.0.1:13336 by default and requires
    EZTEST_EXECUTOR_TOKEN on every /v1 endpoint.

    EZTEST_EXECUTOR_TOKEN is read from the database setting
    execution.executor_token through psql (system_setting.value_json) and exported
    to the executor process, so it always matches the token the API uses. The
    setting is read-only here; it is seeded by the P3 migrations and can be
    rotated from the admin settings. If the setting cannot be read the script
    stops with a clear message unless EZTEST_EXECUTOR_TOKEN was provided
    explicitly (temporary override). EZTEST_EXECUTOR_URL (default
    http://127.0.0.1:13336) is what the API should be pointed at.

    -DatabaseUrl points the lookup at another database (for example an isolated
    acceptance database); it defaults to EZTEST_DATABASE_URL from .env.

.NOTES
    Compatible with Windows PowerShell 5.1 and PowerShell 7+.
#>
[CmdletBinding()]
param(
    [switch]$SkipInstall,
    [string]$DatabaseUrl = ""
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$executorDir = Join-Path $repoRoot "executor"
$envPath = Join-Path $repoRoot ".env"

function Import-DotEnv {
    param([string]$Path)
    foreach ($line in Get-Content -LiteralPath $Path) {
        if ($line -match '^\s*#' -or $line -match '^\s*$') { continue }
        if ($line -match '^\s*([^=]+)=(.*)$') {
            $key = $matches[1].Trim()
            $value = $matches[2].Trim()
            if ($value.Length -ge 2) {
                $first = $value.Substring(0, 1)
                $last = $value.Substring($value.Length - 1, 1)
                if (($first -eq '"' -and $last -eq '"') -or ($first -eq "'" -and $last -eq "'")) {
                    $value = $value.Substring(1, $value.Length - 2)
                }
            }
            Set-Item -Path ("Env:" + $key) -Value $value
        }
    }
}

function Resolve-PsqlExe {
    $localPsql = Join-Path $repoRoot ".tools\pgsql\bin\psql.exe"
    if (Test-Path -LiteralPath $localPsql) { return $localPsql }
    $command = Get-Command psql.exe -ErrorAction SilentlyContinue
    if ($null -ne $command) { return $command.Source }
    return ""
}

<#
    Reads execution.executor_token from system_setting. Returns an object with
    Token and Error; exactly one of them is non-empty. The value is never printed
    or logged by this script.
#>
function Get-ExecutorTokenFromDatabase {
    param([string]$Url)

    $uri = $null
    if (-not [System.Uri]::TryCreate($Url, [System.UriKind]::Absolute, [ref]$uri)) {
        return [pscustomobject]@{ Token = ""; Error = "EZTEST_DATABASE_URL is not a valid absolute URL" }
    }
    if ($uri.Scheme -ne "postgres" -and $uri.Scheme -ne "postgresql") {
        return [pscustomobject]@{ Token = ""; Error = "EZTEST_DATABASE_URL must use the postgres scheme" }
    }
    if ([string]::IsNullOrWhiteSpace($uri.Host)) {
        return [pscustomobject]@{ Token = ""; Error = "EZTEST_DATABASE_URL must include a host" }
    }
    $database = [System.Uri]::UnescapeDataString($uri.AbsolutePath.TrimStart([char[]]"/"))
    if ([string]::IsNullOrWhiteSpace($database) -or $database.Contains("/")) {
        return [pscustomobject]@{ Token = ""; Error = "EZTEST_DATABASE_URL must include a single database name" }
    }
    $userInfo = $uri.UserInfo
    $separator = $userInfo.IndexOf(":")
    if ($separator -lt 0) {
        $user = $userInfo
        $password = ""
    } else {
        $user = $userInfo.Substring(0, $separator)
        $password = $userInfo.Substring($separator + 1)
    }
    $user = [System.Uri]::UnescapeDataString($user)
    $password = [System.Uri]::UnescapeDataString($password)
    if ([string]::IsNullOrWhiteSpace($user)) {
        return [pscustomobject]@{ Token = ""; Error = "EZTEST_DATABASE_URL must include a database user" }
    }

    $psqlExe = Resolve-PsqlExe
    if ([string]::IsNullOrWhiteSpace($psqlExe)) {
        return [pscustomobject]@{ Token = ""; Error = "psql executable not found (.tools\pgsql\bin\psql.exe or PATH)" }
    }

    $port = if ($uri.Port -gt 0) { $uri.Port } else { 5432 }
    $query = "SELECT value_json #>> '{}' FROM system_setting WHERE key = 'execution.executor_token';"
    $env:PGPASSWORD = $password
    $previousErrorAction = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        $output = & $psqlExe -h $uri.Host -p $port -U $user -d $database `
            -tA -v ON_ERROR_STOP=1 -c $query 2>&1
        $exitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousErrorAction
        Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
    }
    if ($exitCode -ne 0) {
        $detail = ($output | Out-String).Trim()
        return [pscustomobject]@{
            Token = ""
            Error = "psql query failed (exit $exitCode): " + $detail
        }
    }
    $token = ($output | Out-String).Trim()
    if ([string]::IsNullOrWhiteSpace($token)) {
        return [pscustomobject]@{
            Token = ""
            Error = "system_setting.execution.executor_token is missing or empty"
        }
    }
    return [pscustomobject]@{ Token = $token; Error = "" }
}

if (Test-Path -LiteralPath $envPath) {
    Import-DotEnv -Path $envPath
} else {
    Write-Warning "Missing .env at $envPath; using the process environment only."
}

$lookupUrl = $DatabaseUrl
if ([string]::IsNullOrWhiteSpace($lookupUrl)) { $lookupUrl = $env:EZTEST_DATABASE_URL }
$tokenSource = ""
$tokenError = ""
if ([string]::IsNullOrWhiteSpace($lookupUrl)) {
    $tokenError = "EZTEST_DATABASE_URL is not set"
} else {
    $lookup = Get-ExecutorTokenFromDatabase -Url $lookupUrl
    if ([string]::IsNullOrWhiteSpace($lookup.Error)) {
        $env:EZTEST_EXECUTOR_TOKEN = $lookup.Token
        $tokenSource = "database setting execution.executor_token"
    } else {
        $tokenError = $lookup.Error
    }
}
if ([string]::IsNullOrWhiteSpace($env:EZTEST_EXECUTOR_TOKEN)) {
    throw "EZTEST_EXECUTOR_TOKEN is unavailable and the database setting execution.executor_token could not be read ($tokenError). Start the API once so the P3 migrations seed system_setting, set the token in the admin settings, or export EZTEST_EXECUTOR_TOKEN explicitly as a temporary override; then retry."
}
if ($tokenSource -ne "") {
    Write-Host "Using EZTEST_EXECUTOR_TOKEN from the $tokenSource (value not printed)." -ForegroundColor Cyan
} else {
    Write-Warning "Could not read execution.executor_token from the database ($tokenError); using the EZTEST_EXECUTOR_TOKEN provided by the environment/.env."
}
if ([string]::IsNullOrWhiteSpace($env:EZTEST_EXECUTOR_ADDR)) {
    $env:EZTEST_EXECUTOR_ADDR = "127.0.0.1:13336"
}
if ([string]::IsNullOrWhiteSpace($env:EZTEST_EXECUTOR_MAX_CONTEXTS)) {
    $env:EZTEST_EXECUTOR_MAX_CONTEXTS = "8"
}

$pnpm = Get-Command pnpm -ErrorAction SilentlyContinue
if (-not $pnpm) {
    throw "pnpm is not available. Install pnpm 11+ and retry."
}

if (-not $SkipInstall -and -not (Test-Path -LiteralPath (Join-Path $executorDir "node_modules"))) {
    Write-Host "Installing executor dependencies (playwright@1.64.1)..." -ForegroundColor Cyan
    Push-Location $executorDir
    try {
        & pnpm install
        if ($LASTEXITCODE -ne 0) {
            throw "pnpm install exited with code $LASTEXITCODE"
        }
    } finally {
        Pop-Location
    }
}

Write-Host "Starting EzTest executor on http://$($env:EZTEST_EXECUTOR_ADDR) (max contexts $($env:EZTEST_EXECUTOR_MAX_CONTEXTS))..." -ForegroundColor Cyan
Write-Host "Health: http://$($env:EZTEST_EXECUTOR_ADDR)/healthz (no token required)" -ForegroundColor Cyan
Write-Host "The API needs the same EZTEST_EXECUTOR_TOKEN (see scripts\dev-api.ps1)." -ForegroundColor Yellow
Write-Host "Press Ctrl+C to stop." -ForegroundColor Cyan

Push-Location $executorDir
try {
    & pnpm start
    if ($LASTEXITCODE -ne 0) {
        throw "pnpm start exited with code $LASTEXITCODE"
    }
} finally {
    Pop-Location
}
