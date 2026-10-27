#requires -Version 5.1
<#
.SYNOPSIS
    Runs the local EzTest test gate: Go unit, Go integration and frontend checks.

.DESCRIPTION
    Imports the repository .env, then runs:
      - go test -count=1 ./...
      - go test -tags=integration -count=1 ./...  (uses EZTEST_TEST_DATABASE_URL)
      - pnpm lint, pnpm typecheck, pnpm test
    The frontend production build, Playwright E2E and the full acceptance gate
    are run by scripts/acceptance.ps1 (and scripts/e2e.ps1 for E2E alone).

    PostgreSQL must be running and the eztest/eztest_test databases must exist
    (scripts/db.ps1 init is idempotent).

.NOTES
    Compatible with Windows PowerShell 5.1 and PowerShell 7+.
#>
[CmdletBinding()]
param(
    [switch]$SkipIntegration,
    [switch]$SkipFrontend
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$backendDir = Join-Path $repoRoot "backend"
$frontendDir = Join-Path $repoRoot "frontend"
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

function Invoke-Checked {
    param(
        [string]$Label,
        [string]$Exe,
        [string[]]$Arguments,
        [string]$WorkingDirectory
    )
    Write-Host ""
    Write-Host "==> $Label" -ForegroundColor Cyan
    Push-Location $WorkingDirectory
    try {
        & $Exe @Arguments
        $code = $LASTEXITCODE
    } finally {
        Pop-Location
    }
    if ($code -ne 0) {
        throw "$Label failed with exit code $code"
    }
}

Import-DotEnv -Path $envPath

$goExe = Join-Path $repoRoot ".tools\go\bin\go.exe"
if (-not (Test-Path -LiteralPath $goExe)) {
    $goCommand = Get-Command go.exe -ErrorAction SilentlyContinue
    if (-not $goCommand) {
        throw "Go is not available. Run .tools/setup-toolchain.ps1 or install Go 1.24+."
    }
    $goExe = $goCommand.Source
}

Invoke-Checked -Label "Go unit tests" -Exe $goExe `
    -Arguments @("test", "-count=1", "./...") -WorkingDirectory $backendDir

if (-not $SkipIntegration) {
    if ([string]::IsNullOrWhiteSpace([string]$env:EZTEST_TEST_DATABASE_URL)) {
        throw "EZTEST_TEST_DATABASE_URL is not set in .env; integration tests need the eztest_test database"
    }
    Invoke-Checked -Label "Go integration tests" -Exe $goExe `
        -Arguments @("test", "-tags=integration", "-count=1", "./...") -WorkingDirectory $backendDir
} else {
    Write-Host ""
    Write-Host "==> Go integration tests skipped (-SkipIntegration)" -ForegroundColor Yellow
}

if (-not $SkipFrontend) {
    if (-not (Test-Path -LiteralPath (Join-Path $frontendDir "node_modules"))) {
        throw "frontend/node_modules is missing. Run 'pnpm install' in frontend/ first."
    }
    $pnpmCommand = Get-Command pnpm.cmd -ErrorAction SilentlyContinue
    if (-not $pnpmCommand) {
        $pnpmCommand = Get-Command pnpm -ErrorAction SilentlyContinue
    }
    if (-not $pnpmCommand) {
        throw "pnpm is not on PATH. Install pnpm (or enable Corepack) first."
    }
    $pnpmExe = $pnpmCommand.Source

    Invoke-Checked -Label "Frontend lint" -Exe $pnpmExe -Arguments @("lint") -WorkingDirectory $frontendDir
    Invoke-Checked -Label "Frontend typecheck" -Exe $pnpmExe -Arguments @("typecheck") -WorkingDirectory $frontendDir
    Invoke-Checked -Label "Frontend unit tests" -Exe $pnpmExe -Arguments @("test") -WorkingDirectory $frontendDir
} else {
    Write-Host ""
    Write-Host "==> Frontend checks skipped (-SkipFrontend)" -ForegroundColor Yellow
}

Write-Host ""
Write-Host "All selected checks passed." -ForegroundColor Green
