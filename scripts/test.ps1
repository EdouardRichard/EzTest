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

function Assert-SafeTestDatabaseUrl {
    param(
        [string]$TestDatabaseUrl,
        [string]$DatabaseUrl
    )
    # Defense in depth for the destructive Go integration helper: it drops and
    # recreates the public schema, so the URL must name a dedicated PostgreSQL
    # test/e2e database and must not be the development database. Error messages
    # intentionally omit credentials and the full URLs.
    if ([string]::IsNullOrWhiteSpace($TestDatabaseUrl)) {
        throw "EZTEST_TEST_DATABASE_URL is not set; integration tests need the dedicated test database"
    }
    if ([string]::IsNullOrWhiteSpace($DatabaseUrl)) {
        throw "EZTEST_DATABASE_URL is not set, so the test database cannot be checked against the development database"
    }

    $testUri = $null
    if (-not [System.Uri]::TryCreate($TestDatabaseUrl, [System.UriKind]::Absolute, [ref]$testUri)) {
        throw "EZTEST_TEST_DATABASE_URL is not a valid absolute URL"
    }
    $devUri = $null
    if (-not [System.Uri]::TryCreate($DatabaseUrl, [System.UriKind]::Absolute, [ref]$devUri)) {
        throw "EZTEST_DATABASE_URL is not a valid absolute URL"
    }

    foreach ($item in @(
            @{ Name = "EZTEST_TEST_DATABASE_URL"; Uri = $testUri },
            @{ Name = "EZTEST_DATABASE_URL"; Uri = $devUri }
        )) {
        if ($item.Uri.Scheme -ne "postgres" -and $item.Uri.Scheme -ne "postgresql") {
            throw "$($item.Name) must use the postgres scheme"
        }
        if ([string]::IsNullOrWhiteSpace($item.Uri.Host)) {
            throw "$($item.Name) must include a host"
        }
    }

    $testName = [System.Uri]::UnescapeDataString($testUri.AbsolutePath.TrimStart([char[]]"/"))
    if ([string]::IsNullOrWhiteSpace($testName)) {
        throw "EZTEST_TEST_DATABASE_URL must include a database name"
    }
    if (-not ($testName.EndsWith("_test", [System.StringComparison]::Ordinal) -or
            $testName.EndsWith("_e2e", [System.StringComparison]::Ordinal))) {
        throw "EZTEST_TEST_DATABASE_URL database '$testName' must end with _test or _e2e"
    }

    $devName = [System.Uri]::UnescapeDataString($devUri.AbsolutePath.TrimStart([char[]]"/"))
    if ([string]::IsNullOrWhiteSpace($devName)) {
        throw "EZTEST_DATABASE_URL must include a database name"
    }

    $testPort = if ($testUri.Port -gt 0) { $testUri.Port } else { 5432 }
    $devPort = if ($devUri.Port -gt 0) { $devUri.Port } else { 5432 }
    $testCanonical = "{0}:{1}/{2}" -f $testUri.Host.ToLowerInvariant(), $testPort, $testName
    $devCanonical = "{0}:{1}/{2}" -f $devUri.Host.ToLowerInvariant(), $devPort, $devName
    if ($testCanonical -eq $devCanonical) {
        throw "EZTEST_TEST_DATABASE_URL points at the same database as EZTEST_DATABASE_URL ($testCanonical); refusing to reset it"
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
    Assert-SafeTestDatabaseUrl -TestDatabaseUrl ([string]$env:EZTEST_TEST_DATABASE_URL) `
        -DatabaseUrl ([string]$env:EZTEST_DATABASE_URL)
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
