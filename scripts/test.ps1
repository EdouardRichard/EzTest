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
    $previousErrorAction = $ErrorActionPreference
    # pnpm/Biome/tsc write progress to stderr; with ErrorActionPreference=Stop
    # Windows PowerShell 5.1 turns that into a NativeCommandError even when the
    # command succeeded. Judge the gate by $LASTEXITCODE instead.
    $ErrorActionPreference = "Continue"
    try {
        Write-Host "== frontend lint =="
        & pnpm lint
        $lintExit = $LASTEXITCODE
        if ($lintExit -ne 0) { exit $lintExit }
        Write-Host "== frontend typecheck =="
        & pnpm typecheck
        $typeExit = $LASTEXITCODE
        if ($typeExit -ne 0) { exit $typeExit }
        Write-Host "== frontend test =="
        & pnpm test
        $testExit = $LASTEXITCODE
        if ($testExit -ne 0) { exit $testExit }
    } finally {
        $ErrorActionPreference = $previousErrorAction
        Pop-Location
    }
} else {
    Write-Host "frontend/node_modules missing; skipping frontend tests"
}
