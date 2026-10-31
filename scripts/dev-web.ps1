# Start the EzTest 0.2 Next.js dev server.
# Uses EZTEST_WEB_PORT from the repository-root .env (default 6868).
$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
$envFile = Join-Path $repoRoot ".env"
$webPort = "6868"
if (Test-Path $envFile) {
    Get-Content $envFile | ForEach-Object {
        $line = $_.Trim()
        if ($line -match '^EZTEST_WEB_PORT\s*=\s*(.+)$') { $webPort = $Matches[1].Trim() }
    }
}
$env:NEXT_PUBLIC_API_BASE_URL = if ($env:NEXT_PUBLIC_API_BASE_URL) { $env:NEXT_PUBLIC_API_BASE_URL } else { "http://localhost:13333" }
Push-Location (Join-Path $repoRoot "frontend")
try {
    & pnpm exec next dev -p $webPort
    exit $LASTEXITCODE
} finally {
    Pop-Location
}
