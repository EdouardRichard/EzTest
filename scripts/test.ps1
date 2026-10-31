# EzTest 0.2 P0 test gate: Go unit + integration, then frontend lint/typecheck/tests.
$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot

$envFile = Join-Path $repoRoot ".env"
if (Test-Path $envFile) {
    Get-Content $envFile | ForEach-Object {
        $line = $_.Trim()
        if ($line -eq "" -or $line.StartsWith("#")) { return }
        $eq = $line.IndexOf("=")
        if ($eq -lt 1) { return }
        [Environment]::SetEnvironmentVariable($line.Substring(0, $eq).Trim(), $line.Substring($eq + 1).Trim(), "Process")
    }
}
if (-not $env:EZTEST_DATABASE_URL) { throw "EZTEST_DATABASE_URL is required (copy .env.example to .env)." }
if (-not $env:EZTEST_TEST_DATABASE_URL) {
    $env:EZTEST_TEST_DATABASE_URL = $env:EZTEST_DATABASE_URL -replace '/eztest_v02(\?|$)', '/eztest_v02_test$1'
}

$go = Join-Path $repoRoot ".tools\go\bin\go.exe"
if (-not (Test-Path $go)) { $go = "go" }

Push-Location (Join-Path $repoRoot "backend")
try {
    Write-Host "== go test =="
    & $go test ./...
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    Write-Host "== go test -tags=integration =="
    & $go test -tags=integration ./...
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
} finally {
    Pop-Location
}

$frontend = Join-Path $repoRoot "frontend"
if (Test-Path (Join-Path $frontend "node_modules")) {
    Push-Location $frontend
    try {
        Write-Host "== frontend lint =="
        & pnpm lint
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
        Write-Host "== frontend typecheck =="
        & pnpm typecheck
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
        Write-Host "== frontend test =="
        & pnpm test
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    } finally {
        Pop-Location
    }
} else {
    Write-Host "frontend/node_modules missing; skipping frontend tests"
}
