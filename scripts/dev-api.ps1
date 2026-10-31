# Start the EzTest 0.2 API from source.
# Reads exactly the four startup variables from the repository-root .env.
$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
$envFile = Join-Path $repoRoot ".env"
if (-not (Test-Path $envFile)) {
    throw "Missing .env at $envFile. Copy .env.example to .env and set EZTEST_DATABASE_URL."
}
Get-Content $envFile | ForEach-Object {
    $line = $_.Trim()
    if ($line -eq "" -or $line.StartsWith("#")) { return }
    $eq = $line.IndexOf("=")
    if ($eq -lt 1) { return }
    [Environment]::SetEnvironmentVariable($line.Substring(0, $eq).Trim(), $line.Substring($eq + 1).Trim(), "Process")
}
if (-not $env:EZTEST_DATABASE_URL) { throw "EZTEST_DATABASE_URL is required." }

$go = Join-Path $repoRoot ".tools\go\bin\go.exe"
if (-not (Test-Path $go)) { $go = "go" }

Push-Location (Join-Path $repoRoot "backend")
try {
    & $go run ./cmd/eztest
    exit $LASTEXITCODE
} finally {
    Pop-Location
}
