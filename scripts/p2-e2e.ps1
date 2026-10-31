#requires -Version 5.1
<#
.SYNOPSIS
    P2 browser E2E for the design workspace.

.DESCRIPTION
    Starts an isolated stack and runs frontend/e2e/p2.spec.ts:
      * database : eztest_v02_e2e (dropped and recreated)
      * fake LLM : 127.0.0.1:13335
      * API      : freshly built .tmp/p2/eztest.exe on 127.0.0.1:14333
      * Web      : Next dev server on 127.0.0.1:16868
    Screenshots/logs go to .tmp/p2-acceptance. The script never reuses the dev
    database or the P0 stack.
#>
[CmdletBinding()]
param(
    [int]$ApiPort = 14333,
    [int]$WebPort = 16868,
    [int]$FakeLLMPort = 13335,
    [switch]$SkipBuild
)

$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
$logDir = Join-Path $repoRoot ".tmp\p2-acceptance"
$exePath = Join-Path $repoRoot ".tmp\p2\eztest.exe"
New-Item -ItemType Directory -Force -Path $logDir | Out-Null
New-Item -ItemType Directory -Force -Path (Split-Path -Parent $exePath) | Out-Null
$apiUrl = "http://127.0.0.1:$ApiPort"
$webUrl = "http://127.0.0.1:$WebPort"
$fakeUrl = "http://127.0.0.1:$FakeLLMPort"
$started = @()
$e2eDatabase = "eztest_v02_e2e"

function Log([string]$message, [string]$color = "Gray") {
    $line = "[{0}] {1}" -f (Get-Date -Format "HH:mm:ss"), $message
    Write-Host $line -ForegroundColor $color
    Add-Content -LiteralPath (Join-Path $logDir "e2e.log") -Value $line -Encoding UTF8
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
        try {
            & taskkill /PID $process.Id /T /F *> $null
        } catch { }
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
    $devDb = [string]$envValues["EZTEST_DATABASE_URL"]
    if ([string]::IsNullOrWhiteSpace($devDb)) { throw "EZTEST_DATABASE_URL missing from .env" }
    $e2eDb = $devDb -replace '/eztest_v02(\?|$)', "/$e2eDatabase`$1"
    if ($e2eDb -eq $devDb) { throw "could not derive the e2e database URL" }
    if ((Test-Port $ApiPort) -or (Test-Port $WebPort) -or (Test-Port $FakeLLMPort)) {
        throw "ports $ApiPort/$WebPort/$FakeLLMPort must be free; stop the P0 stack or pass different ports"
    }

    # Reset the isolated database.
    $psql = Join-Path $repoRoot ".tools\pgsql\bin\psql.exe"
    if (-not (Test-Path $psql)) { throw "psql not found at $psql" }
    $dbUri = [uri]$e2eDb
    $userInfo = $dbUri.UserInfo.Split(":", 2)
    $oldPassword = $env:PGPASSWORD
    $env:PGPASSWORD = $userInfo[1]
    try {
        & $psql -h $dbUri.Host -p $dbUri.Port -U $userInfo[0] -d postgres -v ON_ERROR_STOP=1 `
            -c "DROP DATABASE IF EXISTS $e2eDatabase WITH (FORCE);" `
            -c "CREATE DATABASE $e2eDatabase;" *> (Join-Path $logDir "e2e-db.log")
        if ($LASTEXITCODE -ne 0) { throw "database reset failed (see e2e-db.log)" }
    } finally {
        $env:PGPASSWORD = $oldPassword
    }
    Log "isolated database $e2eDatabase ready" "Cyan"

    if (-not $SkipBuild) {
        Push-Location (Join-Path $repoRoot "backend")
        try {
            $go = Join-Path $repoRoot ".tools\go\bin\go.exe"
            if (-not (Test-Path $go)) { $go = "go" }
            & $go build -o $exePath ./cmd/eztest *> (Join-Path $logDir "e2e-build.log")
            if ($LASTEXITCODE -ne 0) { throw "go build failed (see e2e-build.log)" }
        } finally { Pop-Location }
    }

    $env:FAKE_LLM_PORT = "$FakeLLMPort"
    $fake = Start-Process -FilePath "node" -ArgumentList (Join-Path $repoRoot "scripts\fake-llm.cjs") `
        -WorkingDirectory $repoRoot -RedirectStandardOutput (Join-Path $logDir "e2e-fake.out.log") `
        -RedirectStandardError (Join-Path $logDir "e2e-fake.err.log") -PassThru
    $started += $fake
    if (-not (Wait-Http "$fakeUrl/health" 20)) { throw "fake LLM did not become ready" }

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
            -RedirectStandardOutput (Join-Path $logDir "e2e-api.out.log") `
            -RedirectStandardError (Join-Path $logDir "e2e-api.err.log") -PassThru
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
        & pnpm build *> (Join-Path $logDir "e2e-web-build.log")
        $buildCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousPreference
        Pop-Location
    }
    if ($buildCode -ne 0) { throw "frontend build failed (see e2e-web-build.log)" }
    $web = Start-Process -FilePath "pnpm.cmd" -ArgumentList "exec", "next", "start", "-p", "$WebPort" `
        -WorkingDirectory (Join-Path $repoRoot "frontend") `
        -RedirectStandardOutput (Join-Path $logDir "e2e-web.out.log") `
        -RedirectStandardError (Join-Path $logDir "e2e-web.err.log") -PassThru
    $started += $web
    if (-not (Wait-Http "$webUrl/login" 90)) { throw "Web did not become ready at $webUrl" }

    $env:EZTEST_E2E_API_URL = $apiUrl
    $env:EZTEST_E2E_BASE_URL = $webUrl
    $env:EZTEST_E2E_FAKE_LLM_URL = "$fakeUrl/v1"
    Push-Location (Join-Path $repoRoot "frontend")
    $previousPreference = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        & pnpm exec playwright test e2e/p2.spec.ts --reporter=list *>&1 |
            Tee-Object -FilePath (Join-Path $logDir "e2e-playwright.log")
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousPreference
        Pop-Location
    }
    if ($code -ne 0) { throw "Playwright failed (see e2e-playwright.log)" }
    Log "P2 browser E2E PASS" "Green"
    exit 0
} catch {
    Log "E2E FAIL: $($_.Exception.Message)" "Red"
    Write-Host $_.ScriptStackTrace -ForegroundColor DarkGray
    exit 1
} finally {
    Stop-Started
}


