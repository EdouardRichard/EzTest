#requires -Version 5.1
<#
.SYNOPSIS
    P2 real-LLM browser E2E (DeepSeek endpoint, no fake model).

.DESCRIPTION
    Starts an isolated API + web stack backed by the acceptance-only provider
    values in .env (llm_api_key / llm_base_url / llm_model_name), then runs
    frontend/e2e/p2.spec.ts with EZTEST_E2E_LLM_* overrides. The API itself still
    reads only the four EZTEST_* runtime variables. Evidence goes to
    .tmp/p2-acceptance/e2e-llm.log.
#>
[CmdletBinding()]
param(
    [int]$ApiPort = 14344,
    [int]$WebPort = 16869,
    [switch]$SkipBuild
)

$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
$logDir = Join-Path $repoRoot ".tmp\p2-acceptance"
$exePath = Join-Path $repoRoot ".tmp\p2\eztest.exe"
$logFile = Join-Path $logDir "e2e-llm.log"
$e2eDatabase = "eztest_v02_llm_e2e"
$apiUrl = "http://127.0.0.1:$ApiPort"
$webUrl = "http://127.0.0.1:$WebPort"
$started = @()
New-Item -ItemType Directory -Force -Path $logDir | Out-Null
New-Item -ItemType Directory -Force -Path (Split-Path -Parent $exePath) | Out-Null
Set-Content -LiteralPath $logFile -Value "" -Encoding UTF8

function Log([string]$message, [string]$color = "Gray") {
    $line = "[{0}] {1}" -f (Get-Date -Format "HH:mm:ss"), $message
    Write-Host $line -ForegroundColor $color
    Add-Content -LiteralPath $logFile -Value $line -Encoding UTF8
}

function Test-Port([int]$port) {
    $client = New-Object System.Net.Sockets.TcpClient
    try { $client.Connect("127.0.0.1", $port); return $true } catch { return $false } finally { $client.Close() }
}

function Wait-Http([string]$url, [int]$seconds) {
    for ($i = 0; $i -lt ($seconds * 2); $i++) {
        try {
            $response = Invoke-WebRequest -Uri $url -UseBasicParsing -TimeoutSec 3
            if ($response.StatusCode -eq 200) { return $true }
        } catch { }
        Start-Sleep -Milliseconds 500
    }
    return $false
}

function Stop-Started {
    foreach ($process in $started) {
        try { & taskkill /PID $process.Id /T /F *> $null } catch { }
    }
}

try {
    if (-not (Test-Path (Join-Path $repoRoot ".env"))) { throw "Missing .env" }
    $envValues = @{}
    Get-Content (Join-Path $repoRoot ".env") | ForEach-Object {
        $line = $_.Trim()
        if ($line -eq "" -or $line.StartsWith("#")) { return }
        $eq = $line.IndexOf("=")
        if ($eq -lt 1) { return }
        $envValues[$line.Substring(0, $eq).Trim()] = $line.Substring($eq + 1).Trim()
    }
    $llmKey = [string]$envValues["llm_api_key"]
    $llmBase = [string]$envValues["llm_base_url"]
    $llmModel = [string]$envValues["llm_model_name"]
    if ([string]::IsNullOrWhiteSpace($llmKey)) { throw "llm_api_key missing from .env" }
    if ([string]::IsNullOrWhiteSpace($llmBase)) { $llmBase = "https://api.deepseek.com" }
    if ([string]::IsNullOrWhiteSpace($llmModel)) { throw "llm_model_name missing from .env" }
    Log "real LLM browser target: $llmBase model=$llmModel (key redacted)" "Cyan"

    $devDb = [string]$envValues["EZTEST_DATABASE_URL"]
    if ([string]::IsNullOrWhiteSpace($devDb)) { throw "EZTEST_DATABASE_URL missing from .env" }
    $e2eDb = $devDb -replace '/eztest_v02(\?|$)', "/$e2eDatabase`$1"
    if ($e2eDb -eq $devDb) { throw "could not derive the real-LLM e2e database URL" }
    if ((Test-Port $ApiPort) -or (Test-Port $WebPort)) {
        throw "ports $ApiPort/$WebPort must be free"
    }

    $psql = Join-Path $repoRoot ".tools\pgsql\bin\psql.exe"
    if (-not (Test-Path $psql)) { throw "psql not found at $psql" }
    $dbUri = [uri]$e2eDb
    $userInfo = $dbUri.UserInfo.Split(":", 2)
    $oldPassword = $env:PGPASSWORD
    $env:PGPASSWORD = $userInfo[1]
    try {
        & $psql -h $dbUri.Host -p $dbUri.Port -U $userInfo[0] -d postgres -v ON_ERROR_STOP=1 `
            -c "DROP DATABASE IF EXISTS $e2eDatabase WITH (FORCE);" `
            -c "CREATE DATABASE $e2eDatabase;" *> (Join-Path $logDir "e2e-llm-db.log")
        if ($LASTEXITCODE -ne 0) { throw "database reset failed (see e2e-llm-db.log)" }
    } finally { $env:PGPASSWORD = $oldPassword }
    Log "isolated database $e2eDatabase ready" "Cyan"

    if (-not $SkipBuild) {
        Push-Location (Join-Path $repoRoot "backend")
        try {
            $go = Join-Path $repoRoot ".tools\go\bin\go.exe"
            if (-not (Test-Path $go)) { $go = "go" }
            & $go build -o $exePath ./cmd/eztest *> (Join-Path $logDir "e2e-llm-build.log")
            if ($LASTEXITCODE -ne 0) { throw "go build failed (see e2e-llm-build.log)" }
        } finally { Pop-Location }
    }

    $previousDb = $env:EZTEST_DATABASE_URL
    $previousApi = $env:EZTEST_API_PORT
    $previousWeb = $env:EZTEST_WEB_PORT
    $previousExecutor = $env:EZTEST_EXECUTOR_PORT
    try {
        $env:EZTEST_DATABASE_URL = $e2eDb
        $env:EZTEST_API_PORT = "$ApiPort"
        $env:EZTEST_WEB_PORT = "$WebPort"
        $env:EZTEST_EXECUTOR_PORT = "13336"
        $api = Start-Process -FilePath $exePath -WorkingDirectory (Join-Path $repoRoot "backend") `
            -RedirectStandardOutput (Join-Path $logDir "e2e-llm-api.out.log") `
            -RedirectStandardError (Join-Path $logDir "e2e-llm-api.err.log") -PassThru
        $started += $api
    } finally {
        $env:EZTEST_DATABASE_URL = $previousDb
        $env:EZTEST_API_PORT = $previousApi
        $env:EZTEST_WEB_PORT = $previousWeb
        $env:EZTEST_EXECUTOR_PORT = $previousExecutor
    }
    if (-not (Wait-Http "$apiUrl/api/health" 60)) { throw "API did not become ready" }

    $env:NEXT_PUBLIC_API_BASE_URL = $apiUrl
    Push-Location (Join-Path $repoRoot "frontend")
    $previousPreference = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        & pnpm build *> (Join-Path $logDir "e2e-llm-web-build.log")
        $buildCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousPreference
        Pop-Location
    }
    if ($buildCode -ne 0) { throw "frontend build failed (see e2e-llm-web-build.log)" }
    $web = Start-Process -FilePath "pnpm.cmd" -ArgumentList "exec", "next", "start", "-p", "$WebPort" `
        -WorkingDirectory (Join-Path $repoRoot "frontend") `
        -RedirectStandardOutput (Join-Path $logDir "e2e-llm-web.out.log") `
        -RedirectStandardError (Join-Path $logDir "e2e-llm-web.err.log") -PassThru
    $started += $web
    if (-not (Wait-Http "$webUrl/login" 90)) { throw "Web did not become ready at $webUrl" }

    $env:EZTEST_E2E_API_URL = $apiUrl
    $env:EZTEST_E2E_BASE_URL = $webUrl
    $env:EZTEST_E2E_LLM_BASE_URL = $llmBase
    $env:EZTEST_E2E_LLM_KEY = $llmKey
    $env:EZTEST_E2E_LLM_TIMEOUT_SECONDS = "300"
    $env:EZTEST_E2E_CASE_TIMEOUT_MS = "240000"
    Push-Location (Join-Path $repoRoot "frontend")
    $previousPreference = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        & pnpm exec playwright test e2e/p2.spec.ts --reporter=list *>&1 |
            Tee-Object -FilePath (Join-Path $logDir "e2e-llm-playwright.log")
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousPreference
        Pop-Location
    }
    if ($code -ne 0) { throw "Playwright failed (see e2e-llm-playwright.log)" }
    Log "P2 real-LLM browser E2E PASS" "Green"
    exit 0
} catch {
    Log "E2E FAIL: $($_.Exception.Message)" "Red"
    Write-Host $_.ScriptStackTrace -ForegroundColor DarkGray
    exit 1
} finally {
    Stop-Started
}
