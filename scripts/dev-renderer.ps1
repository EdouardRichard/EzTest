#requires -Version 5.1
<#
.SYNOPSIS
    Loads the local .env and runs the EzTest PDF renderer service.

.DESCRIPTION
    Imports every KEY=VALUE from the gitignored repository .env (without printing
    secrets) and starts the headless HTML-to-PDF renderer with `pnpm start` from
    renderer/. The service binds 127.0.0.1:13337 by default and requires
    EZTEST_RENDER_TOKEN on every /v1 endpoint.

    EZTEST_RENDER_TOKEN is read from the database setting report.render_token
    through psql (system_setting.value_json) and exported to the renderer
    process, so it always matches the token the API uses. The setting is
    read-only here; it is seeded by the API settings catalog on startup and can
    be rotated from the admin settings. If the setting cannot be read the script
    stops with a clear message unless EZTEST_RENDER_TOKEN was provided
    explicitly (temporary override). EZTEST_RENDERER_ADDR (default
    http://127.0.0.1:13337) is what the API setting report.render_worker_url
    should point at.

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
$rendererDir = Join-Path $repoRoot "renderer"
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
    Reads report.render_token from system_setting. Returns an object with Token
    and Error; exactly one of them is non-empty. The value is never printed or
    logged by this script.
#>
function Get-RenderTokenFromDatabase {
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
    $query = "SELECT value_json #>> '{}' FROM system_setting WHERE key = 'report.render_token';"
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
            Error = "system_setting.report.render_token is missing or empty"
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
    $lookup = Get-RenderTokenFromDatabase -Url $lookupUrl
    if ([string]::IsNullOrWhiteSpace($lookup.Error)) {
        $env:EZTEST_RENDER_TOKEN = $lookup.Token
        $tokenSource = "database setting report.render_token"
    } else {
        $tokenError = $lookup.Error
    }
}
if ([string]::IsNullOrWhiteSpace($env:EZTEST_RENDER_TOKEN)) {
    throw "EZTEST_RENDER_TOKEN is unavailable and the database setting report.render_token could not be read ($tokenError). Start the API once so the settings catalog seeds the P7 keys, set the token in the admin settings, or export EZTEST_RENDER_TOKEN explicitly as a temporary override; then retry."
}
if ($tokenSource -ne "") {
    Write-Host "Using EZTEST_RENDER_TOKEN from the $tokenSource (value not printed)." -ForegroundColor Cyan
} else {
    Write-Warning "Could not read report.render_token from the database ($tokenError); using the EZTEST_RENDER_TOKEN provided by the environment/.env."
}
if ([string]::IsNullOrWhiteSpace($env:EZTEST_RENDERER_ADDR)) {
    $env:EZTEST_RENDERER_ADDR = "127.0.0.1:13337"
}
if ([string]::IsNullOrWhiteSpace($env:EZTEST_RENDER_TIMEOUT_SECONDS)) {
    $env:EZTEST_RENDER_TIMEOUT_SECONDS = "60"
}

$pnpm = Get-Command pnpm -ErrorAction SilentlyContinue
if (-not $pnpm) {
    throw "pnpm is not available. Install pnpm 11+ and retry."
}

if (-not $SkipInstall -and -not (Test-Path -LiteralPath (Join-Path $rendererDir "node_modules"))) {
    Write-Host "Installing renderer dependencies (playwright@1.64.1)..." -ForegroundColor Cyan
    Push-Location $rendererDir
    try {
        & pnpm install
        if ($LASTEXITCODE -ne 0) {
            throw "pnpm install exited with code $LASTEXITCODE"
        }
    } finally {
        Pop-Location
    }
}

Write-Host "Starting EzTest PDF renderer on http://$($env:EZTEST_RENDERER_ADDR) (timeout $($env:EZTEST_RENDER_TIMEOUT_SECONDS)s)..." -ForegroundColor Cyan
Write-Host "Health: http://$($env:EZTEST_RENDERER_ADDR)/healthz (no token required)" -ForegroundColor Cyan
Write-Host "The API needs the same EZTEST_RENDER_TOKEN and report.render_worker_url (see scripts\dev-api.ps1)." -ForegroundColor Yellow
Write-Host "Press Ctrl+C to stop." -ForegroundColor Cyan

Push-Location $rendererDir
try {
    & pnpm start
    if ($LASTEXITCODE -ne 0) {
        throw "pnpm start exited with code $LASTEXITCODE"
    }
} finally {
    Pop-Location
}
