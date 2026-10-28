#requires -Version 5.1
<#
.SYNOPSIS
    Loads the local .env and runs the EzTest Go API on http://localhost:13333.

.DESCRIPTION
    Imports every KEY=VALUE from the gitignored repository .env (without
    printing secrets), starts the portable PostgreSQL cluster if needed and runs
    the API with `go run ./cmd/eztest` from backend/. The API applies all pending
    embedded migrations and seeds the administrator on startup.

    EZTEST_OPENAPI_PATH is pinned to the repository's docs/openapi/api.yaml as an
    absolute path so GET /api/openapi.yaml works even though the process
    working directory is backend/.

.NOTES
    Compatible with Windows PowerShell 5.1 and PowerShell 7+.
#>
[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$backendDir = Join-Path $repoRoot "backend"
$envPath = Join-Path $repoRoot ".env"

function Import-DotEnv {
    param([string]$Path)
    if (-not (Test-Path -LiteralPath $Path)) {
        throw "Missing .env at $Path. Copy .env.example to .env and fill in the placeholders."
    }
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

Import-DotEnv -Path $envPath

function Write-ExecutorHealthHint {
    $executorUrl = $env:EZTEST_EXECUTOR_URL
    if ([string]::IsNullOrWhiteSpace($executorUrl)) {
        $executorUrl = "http://127.0.0.1:13336"
    }
    $healthUrl = $executorUrl.TrimEnd("/") + "/healthz"
    $headers = @{}
    if (-not [string]::IsNullOrWhiteSpace($env:EZTEST_EXECUTOR_TOKEN)) {
        $headers["Authorization"] = "Bearer " + $env:EZTEST_EXECUTOR_TOKEN
    }
    try {
        Invoke-WebRequest -Uri $healthUrl -Headers $headers -TimeoutSec 2 -UseBasicParsing | Out-Null
        Write-Host "Executor: healthy at $healthUrl" -ForegroundColor Green
    } catch {
        Write-Host "Executor: not reachable at $healthUrl - start it with scripts\dev-executor.ps1" -ForegroundColor Yellow
    }
}

# The executor is a separate process on purpose; only report whether it is up.
Write-ExecutorHealthHint

# Run from backend/ because the Go module root is there, but keep the OpenAPI
# document reachable at the configured default path.
$env:EZTEST_OPENAPI_PATH = Join-Path $repoRoot "docs\openapi\api.yaml"

# Local development roots the read-only Agent file tools at the repository;
# a non-empty value loaded from .env above takes precedence.
if ([string]::IsNullOrWhiteSpace($env:EZTEST_AGENT_FILE_ROOT)) {
    $env:EZTEST_AGENT_FILE_ROOT = $repoRoot
}

$dbScript = Join-Path $PSScriptRoot "db.ps1"
# db.ps1 throws on failure, so a normal return means PostgreSQL is ready.
& $dbScript start
if (-not $?) {
    throw "Could not start PostgreSQL; see scripts\db.ps1 status"
}

$goExe = Join-Path $repoRoot ".tools\go\bin\go.exe"
if (-not (Test-Path -LiteralPath $goExe)) {
    $goCommand = Get-Command go.exe -ErrorAction SilentlyContinue
    if (-not $goCommand) {
        throw "Go is not available. Run .tools/setup-toolchain.ps1 or install Go 1.24+."
    }
    $goExe = $goCommand.Source
}

Write-Host "Starting EzTest API on http://localhost:13333 (migrations run at startup)..." -ForegroundColor Cyan
Write-Host "OpenAPI document: http://localhost:13333/api/openapi.yaml" -ForegroundColor Cyan
Write-Host "Press Ctrl+C to stop." -ForegroundColor Cyan

Push-Location $backendDir
try {
    & $goExe run ./cmd/eztest
    if ($LASTEXITCODE -ne 0) {
        throw "go run ./cmd/eztest exited with code $LASTEXITCODE"
    }
} finally {
    Pop-Location
}
