# Run the EzTest 0.2 P0 Playwright journey against a local API + Web stack.
# It reuses an already-running stack when healthy; otherwise it starts the API
# binary from .tmp/p0 and the Next dev server, then stops only what it started.
$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
$envFile = Join-Path $repoRoot ".env"
if (-not (Test-Path $envFile)) { throw "Missing .env; copy .env.example to .env first." }
Get-Content $envFile | ForEach-Object {
    $line = $_.Trim()
    if ($line -eq "" -or $line.StartsWith("#")) { return }
    $eq = $line.IndexOf("=")
    if ($eq -lt 1) { return }
    [Environment]::SetEnvironmentVariable($line.Substring(0, $eq).Trim(), $line.Substring($eq + 1).Trim(), "Process")
}
$apiUrl = "http://127.0.0.1:$($env:EZTEST_API_PORT)"
$webUrl = "http://localhost:$($env:EZTEST_WEB_PORT)"
$started = @()

function Test-Api {
    try {
        $health = Invoke-RestMethod -Uri "$apiUrl/api/health" -TimeoutSec 3
        return $health.data.db -eq "ok" -and $health.data.version -like "0.2*"
    } catch { return $false }
}
function Test-Web {
    try {
        return (Invoke-WebRequest -Uri "$webUrl/login" -UseBasicParsing -TimeoutSec 5).StatusCode -eq 200
    } catch { return $false }
}

try {
    if (-not (Test-Api)) {
        $exe = Join-Path $repoRoot ".tmp\p0\eztest.exe"
        if (-not (Test-Path $exe)) {
            Push-Location (Join-Path $repoRoot "backend")
            try {
                $go = Join-Path $repoRoot ".tools\go\bin\go.exe"
                if (-not (Test-Path $go)) { $go = "go" }
                & $go build -o $exe ./cmd/eztest
                if ($LASTEXITCODE -ne 0) { throw "go build failed" }
            } finally { Pop-Location }
        }
        $api = Start-Process -FilePath $exe -WorkingDirectory (Join-Path $repoRoot "backend") `
            -RedirectStandardOutput (Join-Path $repoRoot ".tmp\p0\e2e-api.out.log") `
            -RedirectStandardError (Join-Path $repoRoot ".tmp\p0\e2e-api.err.log") -PassThru
        $started += $api
        for ($i = 0; $i -lt 40 -and -not (Test-Api); $i++) { Start-Sleep -Milliseconds 500 }
        if (-not (Test-Api)) { throw "API did not become ready at $apiUrl" }
    }
    if (-not (Test-Web)) {
        $env:NEXT_PUBLIC_API_BASE_URL = $apiUrl
        $web = Start-Process -FilePath "pnpm.cmd" -ArgumentList "exec", "next", "dev", "-p", "$($env:EZTEST_WEB_PORT)" `
            -WorkingDirectory (Join-Path $repoRoot "frontend") `
            -RedirectStandardOutput (Join-Path $repoRoot ".tmp\p0\e2e-web.out.log") `
            -RedirectStandardError (Join-Path $repoRoot ".tmp\p0\e2e-web.err.log") -PassThru
        $started += $web
        for ($i = 0; $i -lt 60 -and -not (Test-Web); $i++) { Start-Sleep -Seconds 1 }
        if (-not (Test-Web)) { throw "Web did not become ready at $webUrl" }
    }

    $logDir = Join-Path $repoRoot ".tmp\p0-acceptance"
    New-Item -ItemType Directory -Force -Path $logDir | Out-Null
    Push-Location (Join-Path $repoRoot "frontend")
    $previousPreference = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        & pnpm exec playwright test e2e/p0.spec.ts --reporter=list 2>&1 |
            Tee-Object -FilePath (Join-Path $logDir "e2e.log")
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousPreference
        Pop-Location
    }
    exit $code
} finally {
    foreach ($process in $started) {
        try { Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue } catch { }
    }
}
