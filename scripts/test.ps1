# EzTest 0.2 test gate: Go unit + integration, then the frontend, executor and
# renderer suites that hosted CI also runs. Keep the gates in sync with
# .github/workflows/ci.yml.
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

# Runs one pnpm gate in a package directory and exits the script on failure.
# pnpm/Biome/tsc/node write progress to stderr; with ErrorActionPreference=Stop
# Windows PowerShell 5.1 turns that into a NativeCommandError even when the
# command succeeded. Judge each gate by $LASTEXITCODE instead.
function Invoke-PnpmGate {
    param(
        [string]$Directory,
        [string]$Label,
        [string]$Command
    )
    $package = Split-Path -Leaf $Directory
    if (-not (Test-Path (Join-Path $Directory "node_modules"))) {
        Write-Host ("{0}/node_modules missing; skipping {1} (run pnpm install in {0}/ first)" -f $package, $Label)
        return
    }
    $previousErrorAction = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    Push-Location $Directory
    try {
        Write-Host ("== {0} ==" -f $Label)
        & pnpm $Command
        $gateExit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousErrorAction
        Pop-Location
    }
    if ($gateExit -ne 0) {
        if ($Command -eq "lint") {
            Write-Host ("{0} lint failed; run 'cd {0}; pnpm lint:fix' to apply Biome's safe fixes, then re-run." -f $package) -ForegroundColor Yellow
        }
        exit $gateExit
    }
}

$frontend = Join-Path $repoRoot "frontend"
$executor = Join-Path $repoRoot "executor"
$renderer = Join-Path $repoRoot "renderer"

# A missing pnpm must fail loudly: `& pnpm` under Continue would not throw and
# could leave $LASTEXITCODE pointing at the previous gate, hiding the failure.
if (-not (Get-Command pnpm -ErrorAction SilentlyContinue)) {
    throw "pnpm is required (repo baseline: pnpm 11); install it before running the test gate."
}

Invoke-PnpmGate -Directory $frontend -Label "frontend lint" -Command "lint"
Invoke-PnpmGate -Directory $frontend -Label "frontend typecheck" -Command "typecheck"
Invoke-PnpmGate -Directory $frontend -Label "frontend test" -Command "test"
Invoke-PnpmGate -Directory $executor -Label "executor test" -Command "test"
Invoke-PnpmGate -Directory $renderer -Label "renderer test" -Command "test"
