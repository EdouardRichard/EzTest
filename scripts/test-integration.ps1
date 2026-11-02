#requires -Version 5.1
<#
.SYNOPSIS
    Runs the Go integration tests for the Agent packages (or any forwarded
    `go test` target) against the dedicated test database.

.DESCRIPTION
    Imports the repository .env so EZTEST_TEST_DATABASE_URL (and the rest of the
    configuration) is available, then runs from backend/:

        go test -tags=integration -count=1 <forwarded arguments>

    Every remaining argument is passed through to `go test`, for example:

        powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-integration.ps1 ./internal/agent/infrastructure/postgres -v
        powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-integration.ps1 ./internal/agent/infrastructure/postgres -run 'TestFoo|TestBar' -v

    With no arguments it runs the whole backend integration suite (./...).

    PostgreSQL must be running. The tests reset the database named by
    EZTEST_TEST_DATABASE_URL; the Go helper refuses anything that is not a
    dedicated *_test/*_e2e database and also refuses the development URL.

.NOTES
    Compatible with Windows PowerShell 5.1 and PowerShell 7+.
    This script deliberately has no param() block: the automatic $args variable
    forwards every argument (including -run/-v style flags) verbatim to go test.
#>

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$backendDir = Join-Path $repoRoot "backend"
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
}

if ([string]::IsNullOrWhiteSpace($env:EZTEST_TEST_DATABASE_URL)) {
    throw "EZTEST_TEST_DATABASE_URL is not set. Add it to .env (or export it) before running integration tests."
}

$goExe = Join-Path $repoRoot ".tools\go\bin\go.exe"
if (-not (Test-Path -LiteralPath $goExe)) {
    $goCommand = Get-Command go.exe -ErrorAction SilentlyContinue
    if (-not $goCommand) {
        throw "Go is not available. Run .tools/setup-toolchain.ps1 or install Go 1.24+."
    }
    $goExe = $goCommand.Source
}

$testTargets = @($args)
if ($testTargets.Count -eq 0) {
    $testTargets = @("./...")
}

Write-Host ""
Write-Host "==> Go integration tests: $($testTargets -join ' ')" -ForegroundColor Cyan
Push-Location $backendDir
try {
    & $goExe test -tags=integration -count=1 @testTargets
    $exitCode = $LASTEXITCODE
} finally {
    Pop-Location
}

exit $exitCode
