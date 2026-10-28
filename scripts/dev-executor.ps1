#requires -Version 5.1
<#
.SYNOPSIS
    Loads the local .env and runs the EzTest Playwright executor.

.DESCRIPTION
    Imports every KEY=VALUE from the gitignored repository .env (without printing
    secrets) and starts the executor HTTP service with `pnpm start` from
    executor/. The service binds 127.0.0.1:13336 by default and requires
    EZTEST_EXECUTOR_TOKEN on every /v1 endpoint.

    EZTEST_EXECUTOR_TOKEN must match the value the API process uses; when .env
    does not define it this script falls back to a local placeholder and prints a
    warning. EZTEST_EXECUTOR_URL (default http://127.0.0.1:13336) is what the API
    should be pointed at.

.NOTES
    Compatible with Windows PowerShell 5.1 and PowerShell 7+.
#>
[CmdletBinding()]
param(
    [switch]$SkipInstall
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

if (Test-Path -LiteralPath $envPath) {
    Import-DotEnv -Path $envPath
} else {
    Write-Warning "Missing .env at $envPath; using the process environment only."
}

if ([string]::IsNullOrWhiteSpace($env:EZTEST_EXECUTOR_TOKEN)) {
    $env:EZTEST_EXECUTOR_TOKEN = "dev-executor-token"
    Write-Warning "EZTEST_EXECUTOR_TOKEN is not set; using the placeholder 'dev-executor-token'. Set the same value in .env so the API can authenticate."
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
