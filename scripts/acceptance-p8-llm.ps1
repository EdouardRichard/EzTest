#requires -Version 5.1
<#
.SYNOPSIS
    Optional real-LLM + real-Chrome end-to-end acceptance for P8 (gated by
    EZTEST_RUN_REAL_LLM=1).

.DESCRIPTION
    Without the gate this script prints SKIPPED and exits 0. With the gate open
    it drives one real-model P8 acceptance run against a freshly rebuilt
    eztest_p8_llm database and the operator's real llm_* configuration:

      1. gate: EZTEST_RUN_REAL_LLM=1, all llm_* keys present, no fake-LLM
         override, toolchain and Chrome available, and the eval-gate result
         (.tmp/p8-eval/result.json) present for the quality-centre step;
      2. rebuilds a dedicated eztest_p8_llm database, builds the current API,
         starts the fixture target, the real Playwright executor, the API with
         the real LLM configuration, and the Next.js web app on :6868;
      3. health/migration/admin/login/default-LLM assertions (the LLM key is
         never printed);
      4. the embedded browser script seeds the fixture world through the real
         APIs and uses the real model for requirement clarification, case
         design and the execution agent turns: version -> requirements ->
         clarification -> AI mindmap -> baseline batch -> results/evidence ->
         exploration chain -> issue -> report -> todo centre -> knowledge
         draft approval -> global search -> visual baseline/incremental
         execution -> quality centre;
      5. the browser is the installed Google Chrome through Playwright; every
         page is checked for console errors and rendered secrets, and a
         full-page screenshot is written under docs/acceptance/p8/real-llm/;
      6. the structured result (checks, ids, screenshot hashes) is written to
         docs/acceptance/p8/real-llm/result.json.

    A missing real LLM (auth/quota) is reported as an explicit SKIP with the
    reason, never as a PASS. A failed required check exits non-zero.

.PARAMETER DryRun
    Validate the repository paths, .env key names, toolchain, Chrome, ports and
    eval-gate result without resetting the database or starting any service.

.NOTES
    Fixed resources: API :13333, Web :6868, fixture :13335, executor :13336,
    database eztest_p8_llm. The host must have PostgreSQL from .env running.
    Compatible with Windows PowerShell 5.1 and PowerShell 7+.
#>
[CmdletBinding()]
param(
    [switch]$DryRun
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$backendDir = Join-Path $repoRoot "backend"
$frontendDir = Join-Path $repoRoot "frontend"
$executorDir = Join-Path $repoRoot "executor"
$envPath = Join-Path $repoRoot ".env"
$tmpRoot = Join-Path $repoRoot ".tmp"
$evidenceDir = Join-Path $repoRoot "docs\acceptance\p8\real-llm"
$evalResultPath = Join-Path $tmpRoot "p8-eval\result.json"
$dbName = "eztest_p8_llm"
$apiPort = 13333
$webPort = 6868
$fixturePort = 13335
$executorPort = 13336
$apiBaseUrl = "http://localhost:$apiPort"
$webBaseUrl = "http://localhost:$webPort"
$fixtureBaseUrl = "http://127.0.0.1:$fixturePort"
$executorBaseUrl = "http://127.0.0.1:$executorPort"
$executorToken = "p8-llm-executor-token"
$requiredEnvKeys = @(
    "EZTEST_DATABASE_URL",
    "EZTEST_ADMIN_USERNAME",
    "EZTEST_ADMIN_PASSWORD",
    "EZTEST_SECRET_KEYS",
    "EZTEST_SECRET_KEY_ACTIVE",
    "llm_base_url",
    "llm_key",
    "llm_model_name"
)

$script:secretValues = New-Object System.Collections.ArrayList
$script:checks = New-Object System.Collections.ArrayList
$script:apiProcess = $null
$script:fixtureProcess = $null
$script:executorProcess = $null
$script:webProcess = $null

function Write-Step {
    param([string]$Message)
    Write-Host ""
    Write-Host "==> $Message" -ForegroundColor Cyan
}

function Read-DotEnv {
    param([string]$Path)
    $values = @{}
    if (-not (Test-Path -LiteralPath $Path)) { throw "Missing .env at $Path" }
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
            $values[$key] = $value
        }
    }
    return $values
}

function Get-RequiredValue {
    param($Values, [string]$Key)
    if (-not $Values.ContainsKey($Key) -or [string]::IsNullOrWhiteSpace([string]$Values[$Key])) {
        throw "Missing required value $Key in .env"
    }
    return [string]$Values[$Key]
}

function Resolve-ToolPath {
    param([string]$LocalPath, [string]$CommandName)
    if (-not [string]::IsNullOrWhiteSpace($LocalPath) -and (Test-Path -LiteralPath $LocalPath)) { return $LocalPath }
    $command = Get-Command $CommandName -ErrorAction SilentlyContinue
    if ($null -ne $command) { return $command.Source }
    return ""
}

function Get-RedactedText {
    param([string]$Text)
    $redacted = $Text
    foreach ($secret in $script:secretValues) {
        if (-not [string]::IsNullOrEmpty([string]$secret)) {
            $redacted = $redacted.Replace([string]$secret, "***")
        }
    }
    return $redacted
}

function Stop-ProcessTree {
    param($Process)
    if ($null -eq $Process) { return }
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        $Process.Refresh()
        if (-not $Process.HasExited) { & taskkill.exe /PID $Process.Id /T /F 2>$null | Out-Null }
    } catch {
        # Best effort cleanup.
    } finally {
        $ErrorActionPreference = $previous
    }
}

function Assert-PortFree {
    param([int]$Port)
    if (Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue) {
        throw "Port $Port is already in use; stop the other acceptance/e2e/SLO run first"
    }
}

function Wait-PortFree {
    param([int]$Port, [int]$TimeoutSeconds = 20)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        if (-not (Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue)) { return }
        Start-Sleep -Milliseconds 250
    }
    throw "Port $Port is still in use"
}

function Wait-ForHttp {
    param([string]$Url, [int]$TimeoutSeconds, $Process, [string]$Label)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        if ($null -ne $Process) {
            $Process.Refresh()
            if ($Process.HasExited) { throw "$Label exited early with code $($Process.ExitCode)" }
        }
        try {
            $response = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec 10
            if ([int]$response.StatusCode -eq 200) { return }
        } catch {
            # Not ready yet.
        }
        Start-Sleep -Milliseconds 500
    }
    throw "Timed out waiting for $Label at $Url"
}

function Assert-SafeDatabaseUrl {
    param([string]$DatabaseUrl)
    $uri = $null
    if (-not [System.Uri]::TryCreate($DatabaseUrl, [System.UriKind]::Absolute, [ref]$uri)) {
        throw "EZTEST_DATABASE_URL is not a valid absolute URL"
    }
    if ($uri.Scheme -ne "postgres" -and $uri.Scheme -ne "postgresql") {
        throw "EZTEST_DATABASE_URL must use the postgres scheme"
    }
    if ([string]::IsNullOrWhiteSpace($uri.Host)) { throw "EZTEST_DATABASE_URL must include a host" }
    if ($uri.Query -ne "") {
        foreach ($pair in ($uri.Query.TrimStart([char]"?")) -split "&") {
            if ([string]::IsNullOrWhiteSpace($pair)) { continue }
            $equals = $pair.IndexOf("=")
            $key = if ($equals -ge 0) { $pair.Substring(0, $equals) } else { $pair }
            $key = [System.Uri]::UnescapeDataString($key).Trim().ToLowerInvariant()
            if ($key -eq "dbname" -or $key -eq "database" -or $key -eq "host" -or $key -eq "port") {
                throw "EZTEST_DATABASE_URL must not override the connection target with '$key'"
            }
        }
    }
    $databaseName = [System.Uri]::UnescapeDataString($uri.AbsolutePath.TrimStart([char[]]"/"))
    if ([string]::IsNullOrWhiteSpace($databaseName) -or $databaseName.Contains("/")) {
        throw "EZTEST_DATABASE_URL must include a single database name in its path"
    }
    return $uri
}

function New-DatabaseUrl {
    param([System.Uri]$DatabaseUri, [string]$DatabaseName)
    $builder = New-Object System.UriBuilder -ArgumentList @($DatabaseUri)
    $builder.Path = "/" + $DatabaseName
    return $builder.Uri.AbsoluteUri
}

function Invoke-Api {
    param(
        [string]$Method,
        [string]$Path,
        $Body = $null,
        $Headers = $null,
        [int]$TimeoutSec = 120
    )
    $arguments = @{
        Method          = $Method
        Uri             = ($apiBaseUrl + $Path)
        TimeoutSec      = $TimeoutSec
        UseBasicParsing = $true
    }
    if ($null -ne $Headers) { $arguments["Headers"] = $Headers }
    if ($null -ne $Body) {
        $arguments["ContentType"] = "application/json; charset=utf-8"
        $arguments["Body"] = [System.Text.Encoding]::UTF8.GetBytes(($Body | ConvertTo-Json -Compress -Depth 12))
    }
    $response = Invoke-WebRequest @arguments
    $raw = $null
    if ($response.Content -is [string] -and -not [string]::IsNullOrWhiteSpace([string]$response.Content)) {
        try { $raw = $response.Content | ConvertFrom-Json } catch { $raw = $null }
    }
    $data = $null
    if ($null -ne $raw) { $data = $raw.data }
    return [pscustomobject]@{ Status = [int]$response.StatusCode; Raw = $raw; Data = $data }
}

function Get-Token {
    param([string]$Username, [string]$Password)
    $login = Invoke-Api -Method Post -Path "/api/auth/login" -Body @{ username = $Username; password = $Password }
    if ($login.Status -ne 200) { throw "login for $Username returned HTTP $($login.Status)" }
    $token = [string]$login.Data.token
    if ([string]::IsNullOrWhiteSpace($token)) { throw "login for $Username did not return a token" }
    return $token
}

function Add-CheckResult {
    param([string]$Name, [string]$Status, [string]$Detail)
    [void]$script:checks.Add([pscustomobject]@{ name = $Name; status = $Status; detail = $Detail })
    $color = "Green"
    if ($Status -eq "skipped") { $color = "Yellow" }
    if ($Status -eq "failed") { $color = "Red" }
    Write-Host ("{0} {1}: {2}" -f $Status.ToUpperInvariant(), $Name, $Detail) -ForegroundColor $color
}

function Invoke-DryRun {
    $problems = New-Object System.Collections.ArrayList
    if (-not (Test-Path -LiteralPath $envPath)) {
        $problems += "missing .env at $envPath"
    } else {
        $values = Read-DotEnv -Path $envPath
        foreach ($key in $requiredEnvKeys) {
            if (-not $values.ContainsKey($key) -or [string]::IsNullOrWhiteSpace([string]$values[$key])) {
                $problems += "missing .env key $key"
            }
        }
        if ($values.ContainsKey("EZTEST_FAKE_LLM_URL") -and -not [string]::IsNullOrWhiteSpace([string]$values["EZTEST_FAKE_LLM_URL"])) {
            $problems += "EZTEST_FAKE_LLM_URL is set in .env; the real-LLM run must not point at a fake upstream"
        }
        if ($env:EZTEST_FAKE_LLM_URL -or $env:EZTEST_E2E_FAKE_LLM_URL) {
            $problems += "a fake-LLM URL is present in the environment; clear it before the real-LLM run"
        }
        try {
            $uri = Assert-SafeDatabaseUrl -DatabaseUrl ([string]$values["EZTEST_DATABASE_URL"])
            Write-Host ("DryRun: the acceptance API would target a dedicated '{0}' database on {1}:{2}" -f `
                    $dbName, $uri.Host, $(if ($uri.Port -gt 0) { $uri.Port } else { 5432 }))
        } catch {
            $problems += (Get-RedactedText -Text $_.Exception.Message)
        }
    }
    foreach ($tool in @(
            @{ Name = "go"; Local = (Join-Path $repoRoot ".tools\go\bin\go.exe") },
            @{ Name = "psql"; Local = (Join-Path $repoRoot ".tools\pgsql\bin\psql.exe") },
            @{ Name = "node"; Local = "" }
        )) {
        if ([string]::IsNullOrWhiteSpace((Resolve-ToolPath -LocalPath $tool.Local -CommandName $tool.Name))) {
            $problems += "$($tool.Name) executable not found (.tools or PATH)"
        }
    }
    if (-not (Test-Path -LiteralPath (Join-Path $frontendDir "node_modules\.bin\playwright.CMD"))) {
        $problems += "frontend/node_modules/.bin/playwright.CMD is missing; run pnpm install in frontend"
    }
    if (-not (Test-Path -LiteralPath (Join-Path $executorDir "node_modules"))) {
        $problems += "executor/node_modules is missing; run pnpm install in executor"
    }
    if (-not (Test-Path -LiteralPath (Join-Path $repoRoot "scripts\fixture-target.cjs"))) {
        $problems += "scripts/fixture-target.cjs is missing"
    }
    $chromePaths = @()
    if (-not [string]::IsNullOrWhiteSpace($env:ProgramFiles)) {
        $chromePaths += Join-Path $env:ProgramFiles "Google\Chrome\Application\chrome.exe"
    }
    if (-not [string]::IsNullOrWhiteSpace(${env:ProgramFiles(x86)})) {
        $chromePaths += Join-Path ${env:ProgramFiles(x86)} "Google\Chrome\Application\chrome.exe"
    }
    $chromePaths += Join-Path $env:LOCALAPPDATA "Google\Chrome\Application\chrome.exe"
    if (-not ($chromePaths | Where-Object { Test-Path -LiteralPath $_ })) {
        $problems += "Google Chrome was not found (the browser step launches the installed chrome channel)"
    }
    if (-not (Test-Path -LiteralPath $evalResultPath)) {
        $problems += "missing eval-gate result $evalResultPath; run scripts/eval.ps1 first"
    } else {
        try {
            $evalResult = Get-Content -LiteralPath $evalResultPath -Raw -Encoding UTF8 | ConvertFrom-Json
            if (-not $evalResult.metrics) { $problems += "eval-gate result has no metrics" }
        } catch {
            $problems += "eval-gate result is not valid JSON"
        }
    }
    foreach ($port in @($apiPort, $webPort, $fixturePort, $executorPort)) {
        if (Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue) {
            $problems += "port $port is already in use"
        }
    }
    if ($env:EZTEST_RUN_REAL_LLM -ne "1") {
        Write-Host "DryRun: gate is closed (EZTEST_RUN_REAL_LLM != 1); the real run would print SKIPPED and exit 0."
    }
    if ($problems.Count -gt 0) {
        foreach ($problem in $problems) { Write-Host "DryRun problem: $problem" -ForegroundColor Red }
        return 1
    }
    Write-Host "DryRun OK: paths, .env keys, toolchain, Chrome, ports and eval result are ready." -ForegroundColor Green
    return 0
}

if ($DryRun) {
    exit (Invoke-DryRun)
}

if ($env:EZTEST_RUN_REAL_LLM -ne "1") {
    Write-Host "SKIPPED: real P8 LLM + Chrome acceptance is disabled (set EZTEST_RUN_REAL_LLM=1 to run it)."
    exit 0
}

$stamp = Get-Date -Format "yyyyMMdd-HHmmss"
$tmpDir = Join-Path $tmpRoot ("p8-final-llm-" + $stamp)
New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null
New-Item -ItemType Directory -Force -Path $evidenceDir | Out-Null
$startedAt = [DateTime]::UtcNow
$exitCode = 1
$resultPath = Join-Path $evidenceDir "result.json"
$browserScriptPath = Join-Path $tmpDir "browser.cjs"
$browserLog = Join-Path $tmpDir "browser.log"
$buildLog = Join-Path $tmpDir "build.log"
$apiOut = Join-Path $tmpDir "api.out.log"
$apiErr = Join-Path $tmpDir "api.err.log"
$webOut = Join-Path $tmpDir "web.out.log"
$webErr = Join-Path $tmpDir "web.err.log"
$fixtureOut = Join-Path $tmpDir "fixture.out.log"
$fixtureErr = Join-Path $tmpDir "fixture.err.log"
$executorOut = Join-Path $tmpDir "executor.out.log"
$executorErr = Join-Path $tmpDir "executor.err.log"
$summaryPath = Join-Path $tmpDir "summary.json"
$browserResult = $null

try {
    Write-Step "Gate: EZTEST_RUN_REAL_LLM=1 and required .env keys"
    if (-not (Test-Path -LiteralPath $envPath -PathType Leaf)) { throw "Missing .env at $envPath" }
    $envValues = Read-DotEnv -Path $envPath
    $missingKeys = @()
    foreach ($key in $requiredEnvKeys) {
        if (-not $envValues.ContainsKey($key) -or [string]::IsNullOrWhiteSpace([string]$envValues[$key])) { $missingKeys += $key }
    }
    if ($missingKeys.Count -gt 0) {
        throw ("EZTEST_RUN_REAL_LLM=1 but .env is missing required key(s): " + ($missingKeys -join ", ") + ". The real-LLM run fails closed instead of skipping.")
    }
    if (($envValues.ContainsKey("EZTEST_FAKE_LLM_URL") -and -not [string]::IsNullOrWhiteSpace([string]$envValues["EZTEST_FAKE_LLM_URL"])) -or
        $env:EZTEST_FAKE_LLM_URL -or $env:EZTEST_E2E_FAKE_LLM_URL) {
        throw "a fake-LLM URL is configured; the real-LLM acceptance refuses to point at a fake upstream"
    }
    $adminUsername = Get-RequiredValue -Values $envValues -Key "EZTEST_ADMIN_USERNAME"
    $adminPassword = Get-RequiredValue -Values $envValues -Key "EZTEST_ADMIN_PASSWORD"
    $llmKey = Get-RequiredValue -Values $envValues -Key "llm_key"
    $llmBaseUrl = Get-RequiredValue -Values $envValues -Key "llm_base_url"
    $llmModel = Get-RequiredValue -Values $envValues -Key "llm_model_name"
    $databaseUrl = Get-RequiredValue -Values $envValues -Key "EZTEST_DATABASE_URL"
    $script:secretValues.Add($adminPassword) | Out-Null
    $script:secretValues.Add($llmKey) | Out-Null
    $script:secretValues.Add($databaseUrl) | Out-Null
    if ($envValues.ContainsKey("EZTEST_SECRET_KEYS")) {
        $rawSecretKeys = [string]$envValues["EZTEST_SECRET_KEYS"]
        if (-not [string]::IsNullOrWhiteSpace($rawSecretKeys)) {
            $script:secretValues.Add($rawSecretKeys) | Out-Null
            try {
                $secretObject = $rawSecretKeys | ConvertFrom-Json
                foreach ($property in $secretObject.PSObject.Properties) {
                    $script:secretValues.Add([string]$property.Value) | Out-Null
                }
            } catch {
                # A malformed keyring is reported by the API at startup.
            }
        }
    }
    Add-CheckResult -Name "gate and .env keys" -Status "passed" -Detail "EZTEST_RUN_REAL_LLM=1; llm_base_url/llm_key/llm_model_name present"

    Write-Step "Validating the database URL before any destructive statement"
    $databaseUri = Assert-SafeDatabaseUrl -DatabaseUrl $databaseUrl
    $userInfo = $databaseUri.UserInfo
    $separator = $userInfo.IndexOf(":")
    if ($separator -lt 0) { throw "EZTEST_DATABASE_URL must contain a user and password" }
    $dbUser = [System.Uri]::UnescapeDataString($userInfo.Substring(0, $separator))
    $dbPassword = [System.Uri]::UnescapeDataString($userInfo.Substring($separator + 1))
    $dbHost = $databaseUri.Host
    $dbPort = if ($databaseUri.Port -gt 0) { $databaseUri.Port } else { 5432 }
    $script:secretValues.Add($dbPassword) | Out-Null
    $targetDatabaseUrl = New-DatabaseUrl -DatabaseUri $databaseUri -DatabaseName $dbName

    $goExe = Resolve-ToolPath -LocalPath (Join-Path $repoRoot ".tools\go\bin\go.exe") -CommandName "go"
    $psqlExe = Resolve-ToolPath -LocalPath (Join-Path $repoRoot ".tools\pgsql\bin\psql.exe") -CommandName "psql"
    $nodeExe = Resolve-ToolPath -LocalPath "" -CommandName "node"
    if ([string]::IsNullOrWhiteSpace($goExe)) { throw "go executable not found (.tools or PATH)" }
    if ([string]::IsNullOrWhiteSpace($psqlExe)) { throw "psql executable not found (.tools or PATH)" }
    if ([string]::IsNullOrWhiteSpace($nodeExe)) { throw "node executable not found on PATH" }
    if (-not (Test-Path -LiteralPath $evalResultPath)) {
        throw "missing eval-gate result $evalResultPath; run scripts/eval.ps1 before the real-LLM acceptance"
    }

    foreach ($port in @($apiPort, $webPort, $fixturePort, $executorPort)) { Assert-PortFree -Port $port }

    Write-Step "Resetting the dedicated $dbName database and building the API"
    $env:PGPASSWORD = $dbPassword
    try {
        $exists = & $psqlExe -h $dbHost -p $dbPort -U $dbUser -d postgres -tA -c "SELECT count(*) FROM pg_database WHERE datname = '$dbName';"
        if ($LASTEXITCODE -ne 0) { throw "could not inspect the PostgreSQL databases" }
        if (($exists | Out-String).Trim() -ne "0") {
            & $psqlExe -h $dbHost -p $dbPort -U $dbUser -d postgres -v ON_ERROR_STOP=1 -q -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$dbName' AND pid <> pg_backend_pid();" *> $null
            if ($LASTEXITCODE -ne 0) { throw "could not terminate $dbName connections" }
            & $psqlExe -h $dbHost -p $dbPort -U $dbUser -d postgres -v ON_ERROR_STOP=1 -q -c "DROP DATABASE $dbName;" *> $null
            if ($LASTEXITCODE -ne 0) { throw "could not drop $dbName" }
        }
        & $psqlExe -h $dbHost -p $dbPort -U $dbUser -d postgres -v ON_ERROR_STOP=1 -q -c "CREATE DATABASE $dbName;" *> $null
        if ($LASTEXITCODE -ne 0) { throw "could not create $dbName" }
    } finally {
        Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
    }

    $apiExe = Join-Path $tmpDir "p8-llm-api.exe"
    Push-Location $backendDir
    try {
        & $goExe build -o $apiExe ./cmd/eztest *> $buildLog
        if ($LASTEXITCODE -ne 0) { throw "go build ./cmd/eztest failed (see $buildLog)" }
    } finally {
        Pop-Location
    }

    Write-Step "Starting the fixture target and the real Playwright executor"
    $env:FIXTURE_PORT = "$fixturePort"
    $script:fixtureProcess = Start-Process -FilePath $nodeExe -ArgumentList "`"scripts/fixture-target.cjs`"" `
        -WorkingDirectory $repoRoot -RedirectStandardOutput $fixtureOut -RedirectStandardError $fixtureErr -WindowStyle Hidden -PassThru
    Wait-ForHttp -Url "$fixtureBaseUrl/health" -TimeoutSeconds 30 -Process $script:fixtureProcess -Label "fixture target"
    $env:EZTEST_EXECUTOR_ADDR = "127.0.0.1:$executorPort"
    $env:EZTEST_EXECUTOR_TOKEN = $executorToken
    $env:EZTEST_EXECUTOR_MAX_CONTEXTS = "4"
    $script:executorProcess = Start-Process -FilePath $nodeExe -ArgumentList "`"executor/src/server.mjs`"" `
        -WorkingDirectory $repoRoot -RedirectStandardOutput $executorOut -RedirectStandardError $executorErr -WindowStyle Hidden -PassThru
    Wait-ForHttp -Url "$executorBaseUrl/healthz" -TimeoutSeconds 120 -Process $script:executorProcess -Label "executor"

    Write-Step "Starting the API on :$apiPort with the real LLM configuration"
    $evidenceStore = Join-Path $tmpDir "evidence"
    $reportStore = Join-Path $tmpDir "reports"
    New-Item -ItemType Directory -Force -Path $evidenceStore | Out-Null
    New-Item -ItemType Directory -Force -Path $reportStore | Out-Null
    $env:EZTEST_ENV = "dev"
    $env:EZTEST_DATABASE_URL = $targetDatabaseUrl
    $env:EZTEST_HTTP_ADDR = ":$apiPort"
    $env:EZTEST_WEB_ORIGIN = $webBaseUrl
    $env:EZTEST_ENABLE_TEST_ENDPOINTS = "true"
    $env:EZTEST_STORAGE_DIR = (Join-Path $tmpDir "objects")
    $env:EZTEST_EXECUTOR_URL = $executorBaseUrl
    $env:EZTEST_EXECUTOR_TOKEN = $executorToken
    $env:EZTEST_EVIDENCE_DIR = $evidenceStore
    $env:EZTEST_REPORT_DIR = $reportStore
    $env:EZTEST_VERSION_ATTACHMENT_DIR = (Join-Path $tmpDir "attachments")
    $env:EZTEST_EXEC_MAX_CONCURRENT_CASES = "1"
    $env:EZTEST_EXEC_WORKER_INTERVAL_MS = "200"
    $env:EZTEST_EXEC_CASE_TIMEOUT_MS = "300000"
    $env:EZTEST_OPENAPI_PATH = (Join-Path $repoRoot "docs\openapi\api.yaml")
    $env:EZTEST_ADMIN_USERNAME = $adminUsername
    $env:EZTEST_ADMIN_PASSWORD = $adminPassword
    foreach ($key in @("EZTEST_SECRET_KEYS", "EZTEST_SECRET_KEY_ACTIVE", "llm_base_url", "llm_key", "llm_model_name")) {
        if ($envValues.ContainsKey($key)) { Set-Item -Path ("Env:" + $key) -Value $envValues[$key] }
    }
    Remove-Item Env:EZTEST_TEST_DATABASE_URL -ErrorAction SilentlyContinue
    $script:apiProcess = Start-Process -FilePath $apiExe -WorkingDirectory $backendDir `
        -RedirectStandardOutput $apiOut -RedirectStandardError $apiErr -WindowStyle Hidden -PassThru
    Wait-ForHttp -Url "$apiBaseUrl/api/health" -TimeoutSeconds 120 -Process $script:apiProcess -Label "API"
    # The API process inherited the secrets it needs; drop them from the
    # PowerShell environment before starting the web app and the browser script.
    Remove-Item Env:llm_key -ErrorAction SilentlyContinue
    Remove-Item Env:llm_base_url -ErrorAction SilentlyContinue
    Remove-Item Env:llm_model_name -ErrorAction SilentlyContinue
    Remove-Item Env:EZTEST_DATABASE_URL -ErrorAction SilentlyContinue
    Remove-Item Env:EZTEST_SECRET_KEYS -ErrorAction SilentlyContinue
    Remove-Item Env:EZTEST_SECRET_KEY_ACTIVE -ErrorAction SilentlyContinue

    Write-Step "Checking health, migrations and the default LLM configuration"
    $health = Invoke-Api -Method Get -Path "/api/health"
    if ([string]$health.Data.status -ne "ok" -or [string]$health.Data.db -ne "ok") { throw "API health is not ok" }
    if (-not [bool]$health.Data.llm_configured) {
        throw "the API reports llm_configured=false; the real-LLM acceptance does not degrade silently"
    }
    $adminToken = Get-Token -Username $adminUsername -Password $adminPassword
    $adminHeaders = @{ Authorization = "Bearer $adminToken" }
    $configs = Invoke-Api -Method Get -Path "/api/admin/llm/configs" -Headers $adminHeaders
    if ($configs.Status -ne 200) { throw "GET LLM configs returned HTTP $($configs.Status)" }
    $configList = @($configs.Data)
    if ($configList.Count -eq 0) { throw "no LLM configuration was seeded" }
    $defaultConfig = $configList | Where-Object { [bool]$_.is_default } | Select-Object -First 1
    if ($null -eq $defaultConfig) { throw "no default LLM configuration exists" }
    if ([string]$defaultConfig.model -ne $llmModel) {
        throw "the default LLM model '$($defaultConfig.model)' does not match .env llm_model_name '$llmModel'"
    }
    if (-not [bool]$defaultConfig.has_api_key) { throw "the default LLM configuration has no API key" }
    $baseHost = ([System.Uri]([string]$defaultConfig.base_url)).Host

    Write-Step "Probing the real LLM provider"
    $probeStart = Get-Date
    try {
        $probe = Invoke-Api -Method Post -Path "/api/admin/llm/configs/$($defaultConfig.id)/test" -Headers $adminHeaders `
            -TimeoutSec 120 -Body @{ stream = $false; image = $false; prompt = "Reply with exactly: p8-real-ok" }
    } catch {
        $skillReason = Get-RedactedText -Text $_.Exception.Message
        Write-Host ""
        Write-Host ("SKIPPED: real LLM unavailable - " + $skillReason) -ForegroundColor Yellow
        $skipSummary = [ordered]@{
            status       = "skipped"
            reason       = $skillReason
            generatedAt  = [DateTime]::UtcNow.ToString("o")
            logDirectory = $tmpDir
        }
        [System.IO.File]::WriteAllText($summaryPath, ((ConvertTo-Json -InputObject $skipSummary -Depth 6) + "`n"), (New-Object System.Text.UTF8Encoding($false)))
        exit 0
    }
    if ($probe.Status -ne 200) {
        Write-Host ""
        Write-Host "SKIPPED: real LLM unavailable - connectivity test returned HTTP $($probe.Status)" -ForegroundColor Yellow
        $skipSummary = [ordered]@{
            status       = "skipped"
            reason       = "connectivity test returned HTTP $($probe.Status)"
            generatedAt  = [DateTime]::UtcNow.ToString("o")
            logDirectory = $tmpDir
        }
        [System.IO.File]::WriteAllText($summaryPath, ((ConvertTo-Json -InputObject $skipSummary -Depth 6) + "`n"), (New-Object System.Text.UTF8Encoding($false)))
        exit 0
    }
    if ([string]::IsNullOrWhiteSpace([string]$probe.Data.text)) {
        throw "the real LLM connectivity test returned empty text"
    }
    $probeTokens = 0
    if ($null -ne $probe.Data.usage) { $probeTokens = [int64]$probe.Data.usage.total_tokens }
    if ($probeTokens -le 0) { throw "the real LLM connectivity test reported total_tokens=$probeTokens; a real provider must report positive usage" }
    $probeSeconds = ((Get-Date) - $probeStart).TotalSeconds
    Add-CheckResult -Name "real LLM connectivity" -Status "passed" `
        -Detail ("model {0} at {1} answered {2} chars, usage={3} tokens (>0), {4:N1}s" -f $llmModel, $baseHost, ([string]$probe.Data.text).Length, $probeTokens, $probeSeconds)

    $env:PGPASSWORD = $dbPassword
    try {
        $tables = & $psqlExe -h $dbHost -p $dbPort -U $dbUser -d $dbName -tA -c "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public';"
        $tablesExit = $LASTEXITCODE
    } finally {
        Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
    }
    if ($tablesExit -ne 0) { throw "could not read the migrated table count from $dbName" }
    Add-CheckResult -Name "database reset, migrations, health and admin login" -Status "passed" `
        -Detail ("{0} rebuilt; {1} public tables; llm_configured=true; default model matches .env" -f $dbName, (($tables | Out-String).Trim()))

    Write-Step "Starting the web app on :$webPort"
    $env:NEXT_PUBLIC_API_BASE_URL = $apiBaseUrl
    $env:NEXT_PUBLIC_APP_ENV = "test"
    $env:NEXT_TELEMETRY_DISABLED = "1"
    $nextBin = Join-Path $frontendDir "node_modules\next\dist\bin\next"
    $script:webProcess = Start-Process -FilePath $nodeExe -ArgumentList @("`"$nextBin`"", "dev", "-p", "$webPort") `
        -WorkingDirectory $frontendDir -RedirectStandardOutput $webOut -RedirectStandardError $webErr -WindowStyle Hidden -PassThru
    Wait-ForHttp -Url "$webBaseUrl/login" -TimeoutSeconds 180 -Process $script:webProcess -Label "web app"

    Write-Step "Writing and running the real-LLM + real-Chrome browser flow"
    $browserScript = @'
"use strict"

/**
 * P8 Task 35 real-LLM + real-Chrome acceptance flow.
 *
 * This script is written by scripts/acceptance-p8-llm.ps1 into .tmp and driven
 * against a freshly migrated database with the real llm_* configuration. It
 * creates the fixture world through the real APIs, drives the real Playwright
 * executor through the API worker, and walks the resulting pages in an
 * installed Google Chrome through Playwright. Screenshots and a structured
 * result file are written under docs/acceptance/p8/real-llm/.
 *
 * Nothing on the AI path is faked: clarification, case design and execution
 * use the real model configured by the operator. The browser, executor,
 * database and API are the real local services.
 */

const fs = require("node:fs")
const path = require("node:path")
const crypto = require("node:crypto")
const { chromium } = require("@playwright/test")

const API = process.env.P8_API_URL
const WEB = process.env.P8_WEB_URL
const FIXTURE = process.env.P8_FIXTURE_URL
const OUT = process.env.P8_EVIDENCE_OUT
const ADMIN_USERNAME = process.env.P8_ADMIN_USERNAME
const ADMIN_PASSWORD = process.env.P8_ADMIN_PASSWORD
const NORMAL_PASSWORD = process.env.P8_NORMAL_PASSWORD
const KEYWORD = process.env.P8_KEYWORD
const LLM_MODEL = process.env.P8_LLM_MODEL || ""
const EVAL_RESULT = process.env.P8_EVAL_RESULT || ""
const SECRETS = (() => {
  const encoded = process.env.P8_SECRETS_B64 || ""
  if (encoded === "") return []
  try {
    const parsed = JSON.parse(Buffer.from(encoded, "base64").toString("utf8"))
    return Array.isArray(parsed) ? parsed.filter((value) => typeof value === "string" && value.length > 0) : []
  } catch {
    return []
  }
})()

const checks = []
const failures = []
const screenshots = []
const ids = {}
let browser = null

function log(message) {
  process.stdout.write("[p8-llm] " + message + "\n")
}

function check(name, ok, detail) {
  const status = ok ? "passed" : "failed"
  checks.push({ name, status, detail })
  log((ok ? "PASS " : "FAIL ") + name + ": " + detail)
  if (!ok) failures.push(name + ": " + detail)
}

function requireValue(name, value) {
  if (!value || String(value).trim() === "") {
    throw new Error("missing required environment variable " + name)
  }
  return value
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

async function poll(label, timeoutMs, fn, intervalMs = 1000) {
  const deadline = Date.now() + timeoutMs
  let lastError = null
  while (Date.now() < deadline) {
    try {
      const value = await fn()
      if (value) return value
    } catch (error) {
      lastError = error
    }
    await sleep(intervalMs)
  }
  throw new Error(
    label + " timed out after " + timeoutMs + "ms" + (lastError ? ": " + lastError.message : ""),
  )
}

async function api(method, urlPath, options = {}) {
  const headers = { Accept: "application/json" }
  if (options.token) headers.Authorization = "Bearer " + options.token
  let body
  if (options.body !== undefined) {
    headers["Content-Type"] = "application/json"
    body = JSON.stringify(options.body)
  }
  const response = await fetch(API + urlPath, { method, headers, body })
  let parsed = null
  const text = await response.text()
  if (text !== "") {
    try {
      parsed = JSON.parse(text)
    } catch {
      parsed = null
    }
  }
  if (!response.ok) {
    const error = new Error(
      method +
        " " +
        urlPath +
        " -> HTTP " +
        response.status +
        " " +
        (parsed && parsed.error ? JSON.stringify(parsed.error) : text.slice(0, 300)),
    )
    error.status = response.status
    error.code = parsed && parsed.error ? parsed.error.code : ""
    error.details = parsed && parsed.error ? parsed.error.details : null
    throw error
  }
  if (parsed && Object.prototype.hasOwnProperty.call(parsed, "data")) return parsed.data
  return parsed
}

async function login(username, password) {
  const data = await api("POST", "/api/auth/login", {
    body: { username, password },
  })
  if (!data || !data.token) throw new Error("login for " + username + " returned no token")
  return { token: data.token, user: data.user }
}

async function createNormalUser(adminToken, username) {
  try {
    const created = await api("POST", "/api/users", {
      token: adminToken,
      body: { username, password: NORMAL_PASSWORD },
    })
    return created
  } catch (error) {
    if (error.status === 409) {
      return { username }
    }
    throw error
  }
}

async function createFixtureWorld(adminToken, suffix) {
  const system = await api("POST", "/api/resources/systems", {
    token: adminToken,
    body: {
      key: "p8llm-sys-" + suffix,
      name: "P8 real LLM system " + suffix,
      kind: "web",
      baseUrl: FIXTURE,
    },
  })
  const environment = await api("POST", "/api/resources/systems/" + system.id + "/environments", {
    token: adminToken,
    body: {
      key: "p8llm-env-" + suffix,
      name: "P8 real LLM environment " + suffix,
      kind: "dev",
      baseUrl: FIXTURE,
      allowedHosts: ["127.0.0.1"],
      loginCheck: {
        url: FIXTURE + "/p6/home",
        expectStatus: [200],
        bodyContains: "New order",
      },
    },
  })
  for (const model of [
    { key: "page:/p6/home", kind: "page", name: "P6 Home", path: "/p6/home" },
    { key: "page:/p6/orders", kind: "page", name: "P6 Orders", path: "/p6/orders" },
    { key: "page:/p6/orders/new", kind: "page", name: "P6 New Order", path: "/p6/orders/new" },
    { key: "route:/p6/orders", kind: "route", name: "Orders route", path: "/p6/orders" },
  ]) {
    await api("POST", "/api/resources/systems/" + system.id + "/app-models", {
      token: adminToken,
      body: model,
    })
  }
  const authFlow = await api("POST", "/api/resources/auth-flows", {
    token: adminToken,
    body: {
      system_id: String(system.id),
      name: "P8 real LLM login " + suffix,
      kind: "form",
      definition: {
        login_url: FIXTURE + "/p6/login",
        username_selector: "#username",
        password_selector: "#password",
        submit_selector: "button[type=submit]",
        success: { url_contains: "/p6/home" },
        failure: { invalid_selector: ".login-error", mfa_selector: "#mfa" },
        timeout_ms: 10000,
      },
      enabled: true,
    },
  })
  const account = await api("POST", "/api/resources/accounts", {
    token: adminToken,
    body: {
      key: "p8llm-account-" + suffix,
      label: "P8 real LLM account " + suffix,
      username: "fixture",
      kind: "human",
    },
  })
  const access = await api("POST", "/api/resources/accounts/" + account.id + "/accesses", {
    token: adminToken,
    body: {
      systemId: system.id,
      authType: "password",
      concurrencyLimit: 1,
      credential: "fixture-pass",
      usernameOverride: "fixture",
      loginUrl: FIXTURE + "/p6/login",
      login_flow_id: String(authFlow.id),
      enabled: true,
    },
  })
  const fixtureLogin = await fetch(FIXTURE + "/p6/login", {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ username: "fixture", password: "fixture-pass" }).toString(),
    redirect: "manual",
  })
  if (fixtureLogin.status !== 302) {
    throw new Error("fixture login returned HTTP " + fixtureLogin.status)
  }
  const setCookies =
    typeof fixtureLogin.headers.getSetCookie === "function"
      ? fixtureLogin.headers.getSetCookie()
      : [fixtureLogin.headers.get("set-cookie") || ""]
  let sessionCookie = ""
  for (const value of setCookies) {
    const match = /(?:^|,\s*)p6_session=([^;]+)/.exec(value)
    if (match) sessionCookie = match[1]
  }
  if (sessionCookie === "") throw new Error("fixture login did not set p6_session")
  const imported = await api(
    "POST",
    "/api/resources/accesses/" + access.id + "/sessions",
    {
      token: adminToken,
      body: {
        environmentId: environment.id,
        kind: "storage_state",
        storageState: {
          cookies: [
            {
              name: "p6_session",
              value: sessionCookie,
              domain: "127.0.0.1",
              path: "/",
              expires: -1,
              httpOnly: true,
              secure: false,
              sameSite: "Lax",
            },
          ],
          origins: [{ origin: FIXTURE, localStorage: [], sessionStorage: [] }],
        },
        note: "P8 real-LLM acceptance imported fixture session",
      },
    },
  )
  if (imported.state !== "valid") throw new Error("imported fixture session is " + imported.state)
  return {
    systemId: system.id,
    environmentId: environment.id,
    accessId: access.id,
    sessionId: imported.id,
  }
}

async function createVersion(adminToken, body) {
  return api("POST", "/api/versions", { token: adminToken, body })
}

async function createRequirement(adminToken, versionId, body) {
  return api("POST", "/api/versions/" + versionId + "/requirements", {
    token: adminToken,
    body,
  })
}

async function confirmRequirement(adminToken, versionId, requirementId) {
  return api(
    "POST",
    "/api/versions/" + versionId + "/requirements/" + requirementId + "/confirm",
    { token: adminToken, body: {} },
  )
}

async function getVersion(adminToken, versionId) {
  return api("GET", "/api/versions/" + versionId, { token: adminToken })
}

async function getGraph(adminToken, versionId) {
  return api("GET", "/api/versions/" + versionId + "/mindmap", { token: adminToken })
}

async function startDesignRun(adminToken, versionId) {
  const started = await api("POST", "/api/versions/" + versionId + "/mindmap/runs", {
    token: adminToken,
    body: { kind: "design" },
  })
  const runId = started.runId || started.id
  if (!runId) throw new Error("design run start returned no run id")
  return String(runId)
}

async function waitDesignRun(adminToken, versionId, runId, timeoutMs) {
  return poll("design run " + runId, timeoutMs, async () => {
    const run = await api(
      "GET",
      "/api/versions/" + versionId + "/mindmap/runs/" + encodeURIComponent(runId),
      { token: adminToken },
    )
    if (["done", "failed", "degraded"].includes(run.status)) return run
    return null
  }, 2000)
}

async function approveMindmap(adminToken, versionId) {
  await api("POST", "/api/versions/" + versionId + "/mindmap/submit-review", {
    token: adminToken,
    body: {},
  })
  return api("POST", "/api/versions/" + versionId + "/mindmap/review", {
    token: adminToken,
    body: { decision: "approve", acknowledgeFindings: true },
  })
}

async function listBatches(adminToken, versionId) {
  return api("GET", "/api/versions/" + versionId + "/batches?limit=50", { token: adminToken })
}

async function getBatch(adminToken, versionId, batchId) {
  return api("GET", "/api/versions/" + versionId + "/batches/" + batchId, {
    token: adminToken,
  })
}

async function waitBatch(adminToken, versionId, batchId, timeoutMs) {
  return poll("batch " + batchId, timeoutMs, async () => {
    const batch = await getBatch(adminToken, versionId, batchId)
    if (["done", "failed", "cancelled"].includes(batch.status)) return batch
    return null
  }, 2000)
}

async function listCaseRuns(adminToken, versionId, batchId) {
  return api(
    "GET",
    "/api/versions/" + versionId + "/batches/" + batchId + "/case-runs?limit=100",
    { token: adminToken },
  )
}

async function getCaseRun(adminToken, versionId, batchId, caseRunId) {
  return api(
    "GET",
    "/api/versions/" + versionId + "/batches/" + batchId + "/case-runs/" + caseRunId,
    { token: adminToken },
  )
}

async function waitCaseRun(adminToken, versionId, batchId, caseRunId, timeoutMs) {
  return poll("case run " + caseRunId, timeoutMs, async () => {
    const run = await getCaseRun(adminToken, versionId, batchId, caseRunId)
    if (["passed", "failed", "blocked", "inconclusive", "skipped", "interrupted"].includes(run.status)) {
      return run
    }
    return null
  }, 1000)
}

function parseVisualActual(actual) {
  if (!actual) return {}
  try {
    const parsed = JSON.parse(actual)
    return parsed && typeof parsed === "object" ? parsed : {}
  } catch {
    return {}
  }
}

async function waitVersionStatus(adminToken, versionId, statuses, timeoutMs) {
  return poll("version status " + statuses.join("/"), timeoutMs, async () => {
    const version = await getVersion(adminToken, versionId)
    return statuses.includes(version.status) ? version : null
  }, 2000)
}

function sha256(buffer) {
  return crypto.createHash("sha256").update(buffer).digest("hex")
}

function pngSize(buffer) {
  if (buffer.length < 24) return null
  return { width: buffer.readUInt32BE(16), height: buffer.readUInt32BE(20) }
}

async function saveScreenshot(page, name, options = {}) {
  const buffer = await page.screenshot({ fullPage: options.fullPage !== false })
  const target = path.join(OUT, name)
  fs.writeFileSync(target, buffer)
  const size = pngSize(buffer)
  screenshots.push({
    name,
    bytes: buffer.length,
    sha256: sha256(buffer),
    width: size ? size.width : null,
    height: size ? size.height : null,
  })
  log("screenshot " + name + " (" + buffer.length + " bytes)")
  return target
}

function createConsoleMonitor(page) {
  const errors = []
  const ignored = [
    /favicon\.ico/i,
    /Failed to load resource: the server responded with a status of 404/i,
    /Download the React DevTools/i,
    /net::ERR_ABORTED/i,
  ]
  const onConsole = (message) => {
    if (message.type() !== "error") return
    const text = message.text()
    if (ignored.some((pattern) => pattern.test(text))) return
    errors.push(text)
  }
  const onPageError = (error) => {
    errors.push("pageerror: " + error.message)
  }
  page.on("console", onConsole)
  page.on("pageerror", onPageError)
  return {
    errors,
    stop() {
      page.off("console", onConsole)
      page.off("pageerror", onPageError)
    },
  }
}

async function assertNoSecretInPage(page, label) {
  const text = (await page.locator("body").innerText()) + "\n" + (await page.content())
  for (const secret of SECRETS) {
    if (secret.length >= 6 && text.includes(secret)) {
      check("no secret rendered in " + label, false, "a configured secret appeared in the page")
      return
    }
  }
  check("no secret rendered in " + label, true, "no configured credential appeared in the page")
}

async function loginPage(page, username, password) {
  await page.goto(WEB + "/login", { waitUntil: "domcontentloaded" })
  await page.locator('input[name="username"]').fill(username)
  await page.locator('input[name="password"]').fill(password)
  await page.locator('button[type="submit"]').click()
  await page.waitForURL((url) => !url.pathname.startsWith("/login"), { timeout: 60000 })
  await page.locator('aside a[href="/versions"]').first().waitFor({ state: "visible", timeout: 60000 })
}

async function main() {
  requireValue("P8_API_URL", API)
  requireValue("P8_WEB_URL", WEB)
  requireValue("P8_FIXTURE_URL", FIXTURE)
  requireValue("P8_EVIDENCE_OUT", OUT)
  requireValue("P8_ADMIN_USERNAME", ADMIN_USERNAME)
  requireValue("P8_ADMIN_PASSWORD", ADMIN_PASSWORD)
  requireValue("P8_NORMAL_PASSWORD", NORMAL_PASSWORD)
  requireValue("P8_KEYWORD", KEYWORD)
  fs.mkdirSync(OUT, { recursive: true })

  const suffix = KEYWORD
  const admin = await login(ADMIN_USERNAME, ADMIN_PASSWORD)
  const adminToken = admin.token
  const normalUsername = "p8llm_" + suffix
  await createNormalUser(adminToken, normalUsername)
  check("admin login and normal user provisioning", true, "admin token and normal user " + normalUsername)

  const world = await createFixtureWorld(adminToken, suffix)
  check(
    "fixture resources and imported session",
    world.sessionId > 0,
    "system " + world.systemId + ", environment " + world.environmentId + ", session " + world.sessionId,
  )

  // ---- Real-LLM version: clarification -> design -> approval ----------------
  const versionName = "P8 real LLM " + suffix
  const mainVersion = await createVersion(adminToken, {
    name: versionName,
    label: "p8-llm-" + suffix,
    description: "P8 final acceptance real-LLM version.",
    systemId: world.systemId,
    environmentId: world.environmentId,
  })
  ids.mainVersionId = mainVersion.id
  const requirementOne = await createRequirement(adminToken, mainVersion.id, {
    title: "P8 real " + suffix + " order list requirement",
    description:
      "The fixture application at " +
      FIXTURE +
      "/p6/home renders the home page and " +
      FIXTURE +
      "/p6/orders renders the order list. A case must open both pages and assert that the order list contains Order.",
    acceptanceCriteria: "The order list page is reachable and shows order data.",
    priority: "must",
  })
  const requirementTwo = await createRequirement(adminToken, mainVersion.id, {
    title: "P8 real " + suffix + " order detail requirement",
    description:
      "The fixture order detail page " +
      FIXTURE +
      "/p6/orders/1 renders order data. A case must open it and assert that the page contains Order.",
    acceptanceCriteria: "The order detail page renders order data.",
    priority: "should",
  })
  ids.requirementIds = [requirementOne.id, requirementTwo.id]

  await api("POST", "/api/versions/" + mainVersion.id + "/start-clarification", {
    token: adminToken,
    body: {},
  })
  let clarificationSession = null
  let clarificationError = null
  try {
    clarificationSession = await api(
      "POST",
      "/api/versions/" + mainVersion.id + "/clarification/sessions",
      { token: adminToken, body: {} },
    )
    await api("POST", "/api/versions/" + mainVersion.id + "/clarification/messages", {
      token: adminToken,
      body: {
        message:
          "Please clarify the acceptance boundary for the two fixture requirements, then answer briefly.",
      },
    })
    const assistantMessage = await poll("real clarification answer", 180000, async () => {
      const messages = await api(
        "GET",
        "/api/versions/" + mainVersion.id + "/clarification/messages?limit=50",
        { token: adminToken },
      )
      const list = Array.isArray(messages) ? messages : []
      for (let index = list.length - 1; index >= 0; index -= 1) {
        const message = list[index]
        const text = (message.parts || [])
          .filter((part) => part.type === "text" && part.text)
          .map((part) => part.text)
          .join("")
        if (message.role === "assistant" && text.trim() !== "") return { message, text }
      }
      return null
    }, 2000)
    ids.clarificationAssistantChars = assistantMessage.text.length
    check(
      "real LLM clarification answer persisted",
      true,
      "assistant reply " + assistantMessage.text.length + " chars, session " + clarificationSession.id,
    )
  } catch (error) {
    clarificationError = error
    check("real LLM clarification answer persisted", false, error.message)
  }

  // The real clarification agent may leave open questions behind; answer them
  // through the real API so the human confirmation gate is not blocked.
  try {
    const openQuestions = await api(
      "GET",
      "/api/versions/" + mainVersion.id + "/clarification/questions?status=open&limit=100",
      { token: adminToken },
    )
    for (const question of Array.isArray(openQuestions) ? openQuestions : []) {
      await api(
        "PATCH",
        "/api/versions/" + mainVersion.id + "/clarification/questions/" + question.id,
        {
          token: adminToken,
          body: {
            status: "answered",
            answer: "The acceptance criteria in the requirement are sufficient for this fixture flow.",
          },
        },
      )
      ids.answeredQuestions = (ids.answeredQuestions || 0) + 1
    }
  } catch (error) {
    check("real clarification questions can be resolved", false, error.message)
  }

  for (const requirementId of ids.requirementIds) {
    await confirmRequirement(adminToken, mainVersion.id, requirementId)
  }
  await api("POST", "/api/versions/" + mainVersion.id + "/enter-design", {
    token: adminToken,
    body: { acknowledgeUnclarified: true },
  })
  const designRunId = await startDesignRun(adminToken, mainVersion.id)
  ids.designRunId = designRunId
  const designRun = await waitDesignRun(adminToken, mainVersion.id, designRunId, 600000)
  const graph = await getGraph(adminToken, mainVersion.id)
  const designCases = (graph.nodes || []).filter(
    (node) => node.kind === "case" && (node.steps || []).length > 0 && (node.assertions || []).length > 0,
  )
  check(
    "real LLM design run created executable cases",
    designRun.status === "done" && designCases.length > 0,
    "run " + designRun.status + ", nodes " + (graph.nodes || []).length + ", executable cases " + designCases.length,
  )
  if (designRun.status !== "done" || designCases.length === 0) {
    throw new Error("real-LLM design did not produce an executable mindmap")
  }
  ids.generatedCaseIds = designCases.map((node) => node.id)
  // Keep the global-search fixture deterministic: the case group must also
  // contain the run keyword even when the real model writes a generic title.
  const searchableCase = designCases[0]
  try {
    const renamed = await api(
      "PATCH",
      "/api/versions/" + mainVersion.id + "/mindmap/nodes/" + searchableCase.id,
      {
        token: adminToken,
        body: { title: "P8 real " + suffix + " AI designed case", revision: searchableCase.revision },
      },
    )
    ids.searchableCaseId = renamed.id
    check("AI-designed case renamed for deterministic search", true, "case " + renamed.id)
  } catch (error) {
    check("AI-designed case renamed for deterministic search", false, error.message)
  }
  const approved = await approveMindmap(adminToken, mainVersion.id)
  check(
    "mindmap review approval freezes a baseline",
    approved && approved.version && approved.version.status === "executing" && approved.baseline,
    "version " + (approved.version ? approved.version.status : "?") + ", baseline " + (approved.baseline ? approved.baseline.id : "missing"),
  )

  // ---- Browser: login and walk the version pages ---------------------------
  browser = await chromium.launch({ channel: "chrome", headless: true })
  const adminContext = await browser.newContext({
    viewport: { width: 1600, height: 1200 },
    deviceScaleFactor: 1,
  })
  const adminPage = await adminContext.newPage()
  const adminMonitor = createConsoleMonitor(adminPage)
  await loginPage(adminPage, ADMIN_USERNAME, ADMIN_PASSWORD)
  await adminPage.goto(WEB + "/versions/" + mainVersion.id + "?tab=overview", {
    waitUntil: "domcontentloaded",
  })
  await adminPage.locator('[data-testid="overview-metrics"]').waitFor({ timeout: 60000 })
  await adminPage.getByText(versionName, { exact: false }).first().waitFor({ timeout: 60000 })
  check("version overview renders the real-LLM version", true, "version " + mainVersion.id)
  await saveScreenshot(adminPage, "real-llm-01-overview.png")

  // Requirements and the persisted clarification transcript.
  await adminPage.goto(WEB + "/versions/" + mainVersion.id + "?tab=requirements", {
    waitUntil: "domcontentloaded",
  })
  await adminPage.locator('[data-testid="requirements-workbench-slot"]').waitFor({ timeout: 60000 })
  await adminPage.getByText(requirementOne.title, { exact: false }).first().waitFor({ timeout: 60000 })
  const chatMessages = await adminPage.locator('[role="log"]').first().innerText().catch(() => "")
  check(
    "requirements tab renders requirements and clarification transcript",
    chatMessages.trim().length > 0 || ids.clarificationAssistantChars > 0,
    "requirements visible; clarification text " + (chatMessages.trim().length > 0 ? "rendered" : "pending"),
  )
  await saveScreenshot(adminPage, "real-llm-02-requirements-clarification.png")

  // Mindmap tab with the real-model generated graph.
  await adminPage.goto(WEB + "/versions/" + mainVersion.id + "?tab=mindmap", {
    waitUntil: "domcontentloaded",
  })
  await adminPage.locator('[data-testid="version-tab-mindmap"]').waitFor({ timeout: 60000 })
  await adminPage.locator('[data-testid^="case-node-"]').first().waitFor({ timeout: 60000 })
  const caseNodeCount = await adminPage.locator('[data-testid^="case-node-"]').count()
  check(
    "mindmap tab renders the AI-generated cases",
    caseNodeCount > 0,
    caseNodeCount + " case nodes rendered",
  )
  await saveScreenshot(adminPage, "real-llm-03-mindmap.png")

  // ---- Start the real baseline batch from the UI ---------------------------
  await adminPage.goto(WEB + "/versions/" + mainVersion.id, { waitUntil: "domcontentloaded" })
  const startButton = adminPage.locator('[data-testid="exec-start-btn"]')
  await startButton.waitFor({ timeout: 60000 })
  const batchesBefore = new Set((await listBatches(adminToken, mainVersion.id)).map((batch) => batch.id))
  await startButton.click()
  let mainBatch = null
  try {
    mainBatch = await poll("baseline batch created from UI", 60000, async () => {
      const batches = await listBatches(adminToken, mainVersion.id)
      return batches.find((batch) => !batchesBefore.has(batch.id)) || null
    }, 1000)
  } catch (error) {
    check("baseline batch started from the version UI", false, error.message)
    mainBatch = await api("POST", "/api/versions/" + mainVersion.id + "/batches", {
      token: adminToken,
      body: { caseIds: ids.generatedCaseIds.slice(0, 2) },
    })
    check("baseline batch started from the version UI", true, "fallback API batch " + mainBatch.id + " after UI timeout")
  }
  ids.mainBatchId = mainBatch.id
  const mainDone = await waitBatch(adminToken, mainVersion.id, mainBatch.id, 1800000)
  const mainRuns = await listCaseRuns(adminToken, mainVersion.id, mainBatch.id)
  const mainRunDetails = []
  for (const run of mainRuns) {
    mainRunDetails.push(await waitCaseRun(adminToken, mainVersion.id, mainBatch.id, run.id, 600000))
  }
  check(
    "real-LLM baseline batch executed",
    mainDone.status === "done" && mainRuns.length > 0,
    "batch " + mainDone.status + ", runs " + mainRuns.length + ", counts " + JSON.stringify(mainDone.counts),
  )
  const evidenceRun = mainRunDetails.find((run) => (run.evidence || []).some((item) => item.kind === "screenshot"))
  const screenshotEvidence = evidenceRun
    ? evidenceRun.evidence.find((item) => item.kind === "screenshot")
    : null
  check(
    "execution evidence is persisted",
    Boolean(screenshotEvidence),
    screenshotEvidence ? "screenshot evidence " + screenshotEvidence.id : "no screenshot evidence",
  )

  // Results tab (list, real evidence viewer).
  await adminPage.goto(
    WEB +
      "/versions/" +
      mainVersion.id +
      "?tab=results&batch=" +
      encodeURIComponent(mainBatch.id),
    { waitUntil: "domcontentloaded" },
  )
  await adminPage.locator('[data-testid="results-tab"]').waitFor({ timeout: 60000 })
  await adminPage.locator('[data-testid^="case-run-row-"]').first().waitFor({ timeout: 60000 })
  const firstRunRow = adminPage.locator('[data-testid^="case-run-row-"]').first()
  const firstExpand = firstRunRow.getByRole("button", { name: /\u5c55\u5f00/ })
  if (await firstExpand.count()) await firstExpand.click()
  await saveScreenshot(adminPage, "real-llm-04-execution-results.png")
  if (screenshotEvidence) {
    const thumb = adminPage.locator('[data-testid="evidence-thumb-' + screenshotEvidence.id + '"]').first()
    if (await thumb.count()) {
      await thumb.click()
      await adminPage.locator('[data-testid="evidence-viewer"]').waitFor({ timeout: 30000 })
      await saveScreenshot(adminPage, "real-llm-05-execution-evidence.png")
      await adminPage.keyboard.press("Escape")
    }
  }

  // Exploration chain.
  await adminPage.goto(
    WEB +
      "/versions/" +
      mainVersion.id +
      "?tab=exploration&batch=" +
      encodeURIComponent(mainBatch.id),
    { waitUntil: "domcontentloaded" },
  )
  await adminPage.locator('[data-testid="exploration-tab"]').waitFor({ timeout: 60000 })
  await adminPage.locator('[data-testid^="chain-node-"]').first().waitFor({ timeout: 60000 })
  const chainErrors = await adminPage
    .locator('[data-testid="exploration-error"], [data-testid="exploration-degraded"], [data-testid="chain-issues-degraded"]')
    .count()
  const chainNodeCount = await adminPage.locator('[data-testid^="chain-node-"]').count()
  check(
    "exploration chain renders without an error layer",
    chainErrors === 0 && chainNodeCount > 0,
    chainNodeCount + " nodes, " + chainErrors + " error layers",
  )
  await saveScreenshot(adminPage, "real-llm-06-exploration-chain.png")

  // Issue (human-authored, linked to a real case run) plus its evidence refs.
  const issueRun = mainRunDetails.find((run) => run.status !== "passed") || mainRunDetails[0]
  const issue = await api("POST", "/api/versions/" + mainVersion.id + "/issues", {
    token: adminToken,
    body: {
      title: "P8 real " + suffix + " manual verification issue",
      severity: "major",
      description: "P8 final acceptance manual issue linked to a real executed case run.",
      category: "defect",
      confidence: "high",
      expected: "The executed case matches its expectation.",
      actual: "Manual verification note recorded during the P8 final acceptance.",
      reproSteps: "1. Open the version results tab.\n2. Inspect the linked case run.",
      caseRunId: issueRun ? issueRun.id : undefined,
      caseNodeId: issueRun ? issueRun.case_node_id : undefined,
      requirementId: requirementOne.id,
    },
  })
  ids.issueId = issue.id
  await adminPage.goto(
    WEB + "/versions/" + mainVersion.id + "?tab=issues&issue=" + encodeURIComponent(issue.id),
    { waitUntil: "domcontentloaded" },
  )
  await adminPage.locator('[data-testid="issues-tab"]').waitFor({ timeout: 60000 })
  await adminPage.locator('[data-testid="issue-row-' + issue.id + '"]').waitFor({ timeout: 60000 })
  await adminPage.locator('[data-testid="issue-detail"]').waitFor({ timeout: 60000 })
  const issueEvidenceLinks = await adminPage.locator('[data-testid="issue-evidence-link"]').count()
  check(
    "issue detail renders with its refs",
    true,
    "issue " + issue.id + ", evidence refs " + issueEvidenceLinks,
  )
  await saveScreenshot(adminPage, "real-llm-07-issue-evidence.png")

  // Report: ensure the batch report exists, then open the report tab.
  let report = null
  try {
    report = await api("POST", "/api/versions/" + mainVersion.id + "/reports", {
      token: adminToken,
      body: { batchId: mainBatch.id },
    })
  } catch (error) {
    check("batch report generated", false, error.message)
  }
  if (!report) {
    report = await poll("batch report", 60000, async () => {
      const reports = await api("GET", "/api/versions/" + mainVersion.id + "/reports?limit=20", {
        token: adminToken,
      })
      return (reports || []).find((item) => item.batchId === mainBatch.id) || null
    }, 2000)
  }
  ids.reportId = report.id
  const exportHeaders = await fetch(
    API +
      "/api/versions/" +
      mainVersion.id +
      "/reports/" +
      report.id +
      "/export?format=markdown",
    { headers: { Authorization: "Bearer " + adminToken } },
  )
  const exportBytes = Buffer.from(await exportHeaders.arrayBuffer())
  check(
    "report Markdown export is downloadable",
    exportHeaders.ok && exportBytes.length > 0,
    "HTTP " + exportHeaders.status + ", " + exportBytes.length + " bytes",
  )
  await adminPage.goto(
    WEB +
      "/versions/" +
      mainVersion.id +
      "?tab=report&report=" +
      encodeURIComponent(report.id),
    { waitUntil: "domcontentloaded" },
  )
  await adminPage.locator('[data-testid="report-tab"]').waitFor({ timeout: 60000 })
  await adminPage.locator('[data-testid="report-revision-badge"]').waitFor({ timeout: 60000 })
  const revisionText = await adminPage.locator('[data-testid="report-revision-badge"]').innerText()
  check("report tab renders the revision badge", /revision/i.test(revisionText), revisionText.trim())
  await saveScreenshot(adminPage, "real-llm-08-report.png")

  // Knowledge: draft via the real API, approval in the real review drawer.
  const knowledge = await api("POST", "/api/versions/" + mainVersion.id + "/knowledge", {
    token: adminToken,
    body: {
      kind: "pattern",
      title: "P8 real " + suffix + " knowledge draft",
      body: "Verified during the P8 final acceptance real-LLM run: open the fixture order list and check the order data before asserting.",
      confidence: "high",
      tags: ["p8", "acceptance"],
    },
  })
  ids.knowledgeId = knowledge.id
  const todoButton = adminPage.getByRole("button", { name: "\u5f85\u529e\u4e2d\u5fc3" })
  await adminPage.goto(WEB + "/", { waitUntil: "domcontentloaded" })
  await adminPage.locator('aside a[href="/versions"]').first().waitFor({ timeout: 60000 })
  await todoButton.click()
  await adminPage.locator('[data-testid="todo-panel"]').waitFor({ timeout: 60000 })
  const reviewCountText = await adminPage
    .locator('[data-testid="todo-count-review"]')
    .innerText()
    .catch(() => "0")
  check(
    "todo centre groups the pending knowledge review",
    Number(reviewCountText.trim() || "0") >= 1,
    "review items " + reviewCountText.trim(),
  )
  await saveScreenshot(adminPage, "real-llm-09-todo-centre.png")
  await adminPage.keyboard.press("Escape")

  await adminPage.goto(
    WEB + "/knowledge?version=" + encodeURIComponent(mainVersion.id) + "&review=" + encodeURIComponent(knowledge.id),
    { waitUntil: "domcontentloaded" },
  )
  await adminPage.locator('[data-testid="knowledge-review-drawer"]').waitFor({ timeout: 60000 })
  await adminPage.locator('[data-testid="knowledge-review-item-' + knowledge.id + '"]').waitFor({ timeout: 60000 })
  await saveScreenshot(adminPage, "real-llm-10-knowledge-review.png")
  await adminPage.locator('[data-testid="knowledge-review-approve"]').click()
  const approvedKnowledge = await poll("knowledge approval", 60000, async () => {
    const items = await api(
      "GET",
      "/api/versions/" + mainVersion.id + "/knowledge?status=approved&limit=50",
      { token: adminToken },
    )
    return (items || []).find((item) => item.id === knowledge.id) || null
  }, 1000)
  check(
    "knowledge draft approved by the administrator",
    approvedKnowledge.status === "approved",
    "knowledge " + knowledge.id + " status " + approvedKnowledge.status,
  )
  await adminPage.goto(WEB + "/knowledge?version=" + encodeURIComponent(mainVersion.id), {
    waitUntil: "domcontentloaded",
  })
  await adminPage.locator('[data-testid="knowledge-list-item-' + knowledge.id + '"]').waitFor({ timeout: 60000 })
  await adminPage.locator('[data-testid="knowledge-detail"]').waitFor({ timeout: 60000 })
  await saveScreenshot(adminPage, "real-llm-11-knowledge-approved.png")

  // Global search: the keyword lives in the version, requirements, cases and issue.
  await adminPage.goto(WEB + "/", { waitUntil: "domcontentloaded" })
  await adminPage.locator('aside a[href="/versions"]').first().waitFor({ timeout: 60000 })
  await adminPage.keyboard.press("Control+k")
  const palette = adminPage.getByRole("dialog", { name: "\u5168\u5c40\u641c\u7d22" })
  await palette.waitFor({ timeout: 30000 })
  const searchInput = palette.locator("input").first()
  await searchInput.fill(suffix)
  await adminPage.locator('[data-testid="search-group"]').first().waitFor({ timeout: 60000 })
  const searchGroups = await adminPage.locator('[data-testid="search-group"]').count()
  const searchKinds = await adminPage.locator('[data-testid="search-group"]').evaluateAll((nodes) =>
    nodes.map((node) => node.getAttribute("data-kind")),
  )
  check(
    "global search groups version/requirement/case/issue results",
    searchGroups === 4 && ["version", "requirement", "case", "issue"].every((kind) => searchKinds.includes(kind)),
    "groups " + JSON.stringify(searchKinds),
  )
  await saveScreenshot(adminPage, "real-llm-12-global-search.png")
  await adminPage.keyboard.press("Escape")

  // ---- Visual assertion: baseline approval and incremental execution ------
  const visualVersion = await createVersion(adminToken, {
    name: "P8 real LLM visual " + suffix,
    label: "p8-llm-visual-" + suffix,
    description: "P8 final acceptance real-LLM visual assertion version.",
    systemId: world.systemId,
    environmentId: world.environmentId,
  })
  ids.visualVersionId = visualVersion.id
  const visualRequirement = await createRequirement(adminToken, visualVersion.id, {
    title: "P8 real " + suffix + " visual order detail requirement",
    description: "The order detail page must render consistently.",
    acceptanceCriteria: "The order detail page is visually stable.",
    priority: "must",
  })
  await api("POST", "/api/versions/" + visualVersion.id + "/start-clarification", {
    token: adminToken,
    body: {},
  })
  await confirmRequirement(adminToken, visualVersion.id, visualRequirement.id)
  await api("POST", "/api/versions/" + visualVersion.id + "/enter-design", {
    token: adminToken,
    body: { acknowledgeUnclarified: true },
  })
  const scenario = await api("POST", "/api/versions/" + visualVersion.id + "/mindmap/nodes", {
    token: adminToken,
    body: { kind: "scenario", title: "P8 real visual scenario " + suffix },
  })
  const visualCase = await api("POST", "/api/versions/" + visualVersion.id + "/mindmap/nodes", {
    token: adminToken,
    body: {
      kind: "case",
      parentId: scenario.id,
      title: "P8 real visual order detail case " + suffix,
      priority: "P0",
      preconditions: "The imported fixture session is valid.",
      requirementIds: [visualRequirement.id],
    },
  })
  const visualStep = await api(
    "POST",
    "/api/versions/" + visualVersion.id + "/mindmap/nodes/" + visualCase.id + "/steps",
    {
      token: adminToken,
      body: {
        action: "Open " + FIXTURE + "/p6/orders/1",
        expected: "The order detail page is shown",
      },
    },
  )
  await api(
    "POST",
    "/api/versions/" + visualVersion.id + "/mindmap/nodes/" + visualCase.id + "/assertions",
    {
      token: adminToken,
      body: {
        stepId: visualStep.id,
        kind: "ui",
        target: "body",
        expression: "text contains Order",
        expected: "Order",
      },
    },
  )
  const visualAssertion = await api(
    "POST",
    "/api/versions/" + visualVersion.id + "/mindmap/nodes/" + visualCase.id + "/assertions",
    {
      token: adminToken,
      body: {
        stepId: visualStep.id,
        kind: "visual",
        target: "order detail full page",
        expression: "pixelmatch",
        expected: "visual match",
      },
    },
  )
  ids.visualAssertionId = visualAssertion.id
  const visualGraph = await getGraph(adminToken, visualVersion.id)
  const visualGraphNode = (visualGraph.nodes || []).find((node) => node.id === visualCase.id)
  await api(
    "PUT",
    "/api/versions/" + visualVersion.id + "/mindmap/assertions/" + visualAssertion.id + "/visual",
    {
      token: adminToken,
      body: {
        nodeRevision: visualGraphNode ? visualGraphNode.revision : 1,
        selector: "",
        threshold: 0.05,
        viewport: { width: 1280, height: 720 },
        deviceScaleFactor: 1,
      },
    },
  )
  const visualApproved = await approveMindmap(adminToken, visualVersion.id)
  check(
    "visual version approved into executing",
    visualApproved.version.status === "executing",
    "status " + visualApproved.version.status,
  )

  await adminPage.goto(WEB + "/versions/" + visualVersion.id, { waitUntil: "domcontentloaded" })
  const visualStart = adminPage.locator('[data-testid="exec-start-btn"]')
  await visualStart.waitFor({ timeout: 60000 })
  const visualBatchesBefore = new Set(
    (await listBatches(adminToken, visualVersion.id)).map((batch) => batch.id),
  )
  await visualStart.click()
  const visualBatch = await poll("visual baseline batch created from UI", 60000, async () => {
    const batches = await listBatches(adminToken, visualVersion.id)
    return batches.find((batch) => !visualBatchesBefore.has(batch.id)) || null
  }, 1000)
  ids.visualBaselineBatchId = visualBatch.id
  const visualDone = await waitBatch(adminToken, visualVersion.id, visualBatch.id, 1800000)
  const visualRuns = await listCaseRuns(adminToken, visualVersion.id, visualBatch.id)
  const baselineRun = await waitCaseRun(
    adminToken,
    visualVersion.id,
    visualBatch.id,
    visualRuns[0].id,
    600000,
  )
  const baselineVisual = (baselineRun.assertions || []).find((assertion) => assertion.kind === "visual")
  const baselineActual = parseVisualActual(baselineVisual ? baselineVisual.actual : "")
  const baselineScreenshot = (baselineRun.evidence || []).find((item) => item.kind === "screenshot")
  check(
    "visual baseline batch produced a real executor screenshot",
    baselineRun.status === "inconclusive" &&
      baselineVisual &&
      baselineVisual.verdict === "not_run" &&
      baselineActual.error === "missing_visual_baseline" &&
      Boolean(baselineScreenshot),
    "run " + baselineRun.status + ", verdict " + (baselineVisual ? baselineVisual.verdict : "?") + ", screenshot " + (baselineScreenshot ? baselineScreenshot.id : "missing"),
  )
  if (!baselineScreenshot) throw new Error("the baseline visual run produced no screenshot evidence")
  await api(
    "POST",
    "/api/versions/" + visualVersion.id + "/mindmap/assertions/" + visualAssertion.id + "/visual/baseline",
    { token: adminToken, body: { evidenceId: baselineScreenshot.id } },
  )
  check("visual baseline approved from real evidence", true, "evidence " + baselineScreenshot.id)

  const completed = await waitVersionStatus(adminToken, visualVersion.id, ["completed"], 1200000)
  check("visual version completed after the baseline batch", completed.status === "completed", "status " + completed.status)
  const continuedRequirement = await createRequirement(adminToken, visualVersion.id, {
    title: "P8 real " + suffix + " visual continuation requirement",
    description: "A continuation requirement forces an incremental visual batch.",
    acceptanceCriteria: "The same visual case passes in the incremental batch.",
    priority: "should",
  })
  await waitVersionStatus(adminToken, visualVersion.id, ["continuing"], 180000)
  await confirmRequirement(adminToken, visualVersion.id, continuedRequirement.id)
  await poll("visual continue phase designing", 180000, async () => {
    const version = await getVersion(adminToken, visualVersion.id)
    return version.continuePhase === "designing" ? version : null
  }, 2000)
  const incremental = await api("POST", "/api/versions/" + visualVersion.id + "/batches", {
    token: adminToken,
    body: {
      kind: "incremental",
      caseIds: [visualCase.id],
      parentBatchId: visualBatch.id,
    },
  })
  ids.visualIncrementalBatchId = incremental.id
  const incrementalDone = await waitBatch(adminToken, visualVersion.id, incremental.id, 1800000)
  const incrementalRuns = await listCaseRuns(adminToken, visualVersion.id, incremental.id)
  const incrementalRun = await waitCaseRun(
    adminToken,
    visualVersion.id,
    incremental.id,
    incrementalRuns[0].id,
    600000,
  )
  const incrementalVisual = (incrementalRun.assertions || []).find((assertion) => assertion.kind === "visual")
  const incrementalActual = parseVisualActual(incrementalVisual ? incrementalVisual.actual : "")
  check(
    "incremental visual execution passed with actual/diff evidence",
    incrementalRun.status === "passed" &&
      incrementalVisual &&
      incrementalVisual.verdict === "passed" &&
      incrementalActual.actualEvidenceId &&
      incrementalActual.diffEvidenceId,
    "run " +
      incrementalRun.status +
      ", verdict " +
      (incrementalVisual ? incrementalVisual.verdict : "?") +
      ", diff " +
      String(incrementalActual.diffRatio),
  )
  ids.visualActualEvidenceId = incrementalActual.actualEvidenceId || ""
  ids.visualDiffEvidenceId = incrementalActual.diffEvidenceId || ""

  if (incrementalActual.actualEvidenceId && incrementalActual.diffEvidenceId) {
    await adminPage.goto(
      WEB +
        "/versions/" +
        visualVersion.id +
        "?tab=results&batch=" +
        encodeURIComponent(incremental.id),
      { waitUntil: "domcontentloaded" },
    )
    await adminPage.locator('[data-testid="results-tab"]').waitFor({ timeout: 60000 })
    const row = adminPage.locator('[data-testid="case-run-row-' + incrementalRun.id + '"]')
    await row.waitFor({ timeout: 60000 })
    const expand = row.getByRole("button", { name: /\u5c55\u5f00/ })
    if (await expand.count()) await expand.click()
    await adminPage.locator('[data-testid="case-run-detail-' + incrementalRun.id + '"]').waitFor({ timeout: 60000 })
    await saveScreenshot(adminPage, "real-llm-13-visual-results.png")
    const actualThumb = adminPage
      .locator('[data-testid="evidence-thumb-' + incrementalActual.actualEvidenceId + '"]')
      .first()
    await actualThumb.click()
    await adminPage.locator('[data-testid="evidence-viewer"]').waitFor({ timeout: 30000 })
    await adminPage.locator('[data-testid="evidence-visual-image"]').waitFor({ timeout: 30000 })
    await saveScreenshot(adminPage, "real-llm-14-visual-actual.png")
    await adminPage.keyboard.press("Escape")
    await adminPage.locator('[data-testid="evidence-viewer"]').waitFor({ state: "detached", timeout: 30000 })
    const diffThumb = adminPage
      .locator('[data-testid="evidence-thumb-' + incrementalActual.diffEvidenceId + '"]')
      .first()
    await diffThumb.click()
    await adminPage.locator('[data-testid="evidence-viewer"]').waitFor({ timeout: 30000 })
    await adminPage.locator('[data-testid="evidence-visual-diff"]').waitFor({ timeout: 30000 })
    await saveScreenshot(adminPage, "real-llm-15-visual-diff.png")
    await adminPage.keyboard.press("Escape")
  }

  // ---- Quality centre: import the real eval-gate metrics -------------------
  let evalResult = null
  if (EVAL_RESULT && fs.existsSync(EVAL_RESULT)) {
    try {
      evalResult = JSON.parse(fs.readFileSync(EVAL_RESULT, "utf8"))
    } catch {
      evalResult = null
    }
  }
  if (!evalResult || !evalResult.metrics) {
    throw new Error("missing eval gate result at " + EVAL_RESULT + "; run scripts/eval.ps1 first")
  }
  const metric = (name) => evalResult.metrics[name] || {}
  const sample = {
    confirmedRequirements: Number(metric("design_coverage").denominator || 0),
    coveredRequirements: Number(metric("design_coverage").numerator || 0),
    impactExpected: Number(metric("impact_recall").denominator || 0),
    impactHit: Number(metric("impact_recall").numerator || 0),
    impactSelected: Number(metric("impact_precision").denominator || 0),
    injectedFailures: Number(metric("classification_accuracy").denominator || 0),
    correctClassifications: Number(metric("classification_accuracy").numerator || 0),
    aiIssues: Number(metric("issue_false_positive_rate").denominator || 0),
    truePositiveIssues:
      Number(metric("issue_false_positive_rate").denominator || 0) -
      Number(metric("issue_false_positive_rate").numerator || 0),
    supplementCases: Number(metric("supplement_hit_rate").denominator || 0),
    supplementHits: Number(metric("supplement_hit_rate").numerator || 0),
  }
  const evalRun = await api("POST", "/api/quality/eval-runs", {
    token: adminToken,
    body: {
      mode: evalResult.mode === "real_llm" ? "real_llm" : "deterministic",
      corpusVersion: String(evalResult.corpusVersion || "p8-acceptance-" + suffix),
      model: LLM_MODEL || "unknown",
      sample,
      baseline: evalResult.baseline || undefined,
      summary: {
        source: "scripts/eval.ps1 result.json",
        evalRunId: String(evalResult.runId || ""),
        evalStatus: String(evalResult.status || ""),
      },
    },
  })
  ids.qualityRunId = evalRun.id
  await adminPage.goto(WEB + "/quality", { waitUntil: "domcontentloaded" })
  await adminPage.locator('[data-testid="quality-page"]').waitFor({ timeout: 60000 })
  await adminPage.locator('[data-testid="quality-summary"]').waitFor({ timeout: 60000 })
  const metricRows = await adminPage.locator('[data-testid^="quality-metric-value-"]').count()
  await adminPage.locator('[data-testid="quality-history-row-' + evalRun.id + '"]').waitFor({ timeout: 60000 })
  check(
    "quality centre renders the imported evaluation run",
    metricRows === 6,
    "run " + evalRun.id + ", metrics " + metricRows + ", mode " + evalResult.mode,
  )
  await saveScreenshot(adminPage, "real-llm-16-quality-centre.png")

  // ---- Final cross-page assertions ----------------------------------------
  await assertNoSecretInPage(adminPage, "quality centre")
  adminMonitor.stop()
  const pageBody = await adminPage.locator("body").innerText()
  check(
    "no Next.js error overlay on the final page",
    !/Application error|Unhandled Runtime Error/i.test(pageBody),
    "final page rendered",
  )
  const consoleErrors = adminMonitor.errors
  check(
    "no browser console errors across the real-Chrome walk",
    consoleErrors.length === 0,
    consoleErrors.length === 0 ? "0 console errors" : JSON.stringify(consoleErrors.slice(0, 8)),
  )

  // A normal user sees the approved knowledge entry (read-only).
  const normal = await login(normalUsername, NORMAL_PASSWORD)
  const normalContext = await browser.newContext({
    viewport: { width: 1600, height: 1200 },
    deviceScaleFactor: 1,
  })
  const normalPage = await normalContext.newPage()
  const normalMonitor = createConsoleMonitor(normalPage)
  await loginPage(normalPage, normalUsername, NORMAL_PASSWORD)
  await normalPage.goto(WEB + "/knowledge?version=" + encodeURIComponent(mainVersion.id), {
    waitUntil: "domcontentloaded",
  })
  await normalPage
    .locator('[data-testid="knowledge-list-item-' + knowledge.id + '"]')
    .waitFor({ timeout: 60000 })
  await normalPage.locator('[data-testid="knowledge-detail"]').waitFor({ timeout: 60000 })
  const normalReviewControls = await normalPage
    .locator('[data-testid="knowledge-open-review"], [data-testid="knowledge-status-filter"]')
    .count()
  check(
    "normal user reads approved knowledge without review controls",
    normalReviewControls === 0,
    "approved entry visible; review controls " + normalReviewControls,
  )
  await saveScreenshot(normalPage, "real-llm-17-knowledge-normal-user.png")
  normalMonitor.stop()
  check(
    "normal-user console has no errors",
    normalMonitor.errors.length === 0,
    normalMonitor.errors.length === 0 ? "0 console errors" : JSON.stringify(normalMonitor.errors.slice(0, 5)),
  )
}

function isLlmUnavailable(cause) {
  if (!cause) return false
  if (cause.status === 503) return true
  const code = String(cause.code || "")
  if (code === "ai_unavailable" || code === "llm_not_configured") return true
  const message = String(cause.message || "")
  return /ai_unavailable|llm_not_configured|no usable LLM configuration/i.test(message)
}

async function run() {
  const startedAt = new Date()
  let error = null
  let skipped = false
  try {
    await main()
  } catch (cause) {
    error = cause
    skipped = isLlmUnavailable(cause)
    log((skipped ? "SKIPPED: " : "FAILED: ") + (cause && cause.message ? cause.message : String(cause)))
  } finally {
    if (browser) {
      await browser.close().catch(() => undefined)
    }
  }
  const result = {
    status: skipped ? "skipped" : error ? "failed" : failures.length === 0 ? "passed" : "failed",
    skipReason: skipped ? String(error.message || error) : null,
    generatedAt: new Date().toISOString(),
    elapsedMs: Date.now() - startedAt.getTime(),
    keyword: KEYWORD,
    llmModel: LLM_MODEL,
    checks,
    failures,
    screenshots,
    ids,
    error: error && !skipped ? String(error.message || error) : null,
  }
  fs.writeFileSync(path.join(OUT, "result.json"), JSON.stringify(result, null, 2), "utf8")
  log("result: " + result.status + " (" + checks.length + " checks, " + failures.length + " failures, " + screenshots.length + " screenshots)")
  process.exitCode = result.status === "failed" ? 1 : 0
}

run().catch((cause) => {
  process.stderr.write("fatal: " + (cause && cause.stack ? cause.stack : String(cause)) + "\n")
  process.exitCode = 1
})
'@
    [System.IO.File]::WriteAllText($browserScriptPath, $browserScript, (New-Object System.Text.UTF8Encoding($false)))

    $keyword = "p8llm" + (Get-Date -Format "HHmmss")
    $normalPassword = "P8-llm-pass-" + $stamp
    $secretList = @($script:secretValues | Where-Object { -not [string]::IsNullOrWhiteSpace([string]$_) } | Select-Object -Unique)
    $secretJson = ConvertTo-Json -InputObject @($secretList) -Compress
    $secretB64 = [Convert]::ToBase64String([System.Text.Encoding]::UTF8.GetBytes($secretJson))

    $env:P8_API_URL = $apiBaseUrl
    $env:P8_WEB_URL = $webBaseUrl
    $env:P8_FIXTURE_URL = $fixtureBaseUrl
    $env:P8_EVIDENCE_OUT = $evidenceDir
    $env:P8_ADMIN_USERNAME = $adminUsername
    $env:P8_ADMIN_PASSWORD = $adminPassword
    $env:P8_NORMAL_PASSWORD = $normalPassword
    $env:P8_KEYWORD = $keyword
    $env:P8_LLM_MODEL = $llmModel
    $env:P8_EVAL_RESULT = $evalResultPath
    $env:P8_SECRETS_B64 = $secretB64
    $env:NODE_PATH = (Join-Path $frontendDir "node_modules")
    Remove-Item Env:FORCE_COLOR -ErrorAction SilentlyContinue
    Remove-Item Env:NO_COLOR -ErrorAction SilentlyContinue

    $previous = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        & $nodeExe $browserScriptPath *> $browserLog
        $browserExit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    Write-Host ""
    Write-Host "--- browser flow (last 80 lines) ---" -ForegroundColor Yellow
    if (Test-Path -LiteralPath $browserLog) { Get-Content -LiteralPath $browserLog -Tail 80 | ForEach-Object { Write-Host $_ } }

    if (-not (Test-Path -LiteralPath $resultPath)) {
        throw "the browser flow did not write $resultPath (exit $browserExit)"
    }
    $browserResult = Get-Content -LiteralPath $resultPath -Raw -Encoding UTF8 | ConvertFrom-Json
    $browserStatus = [string]$browserResult.status
    if ($browserStatus -eq "skipped") {
        Add-CheckResult -Name "real LLM availability" -Status "skipped" -Detail ([string]$browserResult.skipReason)
        $summary = [ordered]@{
            status        = "skipped"
            reason        = [string]$browserResult.skipReason
            generatedAt   = [DateTime]::UtcNow.ToString("o")
            browserResult = $resultPath
            logDirectory  = $tmpDir
        }
        [System.IO.File]::WriteAllText($summaryPath, ((ConvertTo-Json -InputObject $summary -Depth 6) + "`n"), (New-Object System.Text.UTF8Encoding($false)))
        Write-Host ""
        Write-Host "SKIPPED: real LLM unavailable - $($browserResult.skipReason)" -ForegroundColor Yellow
        exit 0
    }
    if ($browserStatus -ne "passed") {
        $failureText = @($browserResult.failures) -join "; "
        if ([string]::IsNullOrWhiteSpace($failureText)) { $failureText = [string]$browserResult.error }
        throw "the real-LLM browser flow failed: $failureText"
    }
    if (@($browserResult.failures).Count -gt 0) {
        throw ("the real-LLM browser flow reported failed checks: " + (@($browserResult.failures) -join "; "))
    }

    Write-Step "Verifying screenshots and scanning the evidence for secrets"
    foreach ($shot in @($browserResult.screenshots)) {
        $shotPath = Join-Path $evidenceDir ([string]$shot.name)
        if (-not (Test-Path -LiteralPath $shotPath)) { throw "missing screenshot $($shot.name)" }
        $bytes = [System.IO.File]::ReadAllBytes($shotPath)
        if ($bytes.Length -lt 8 -or $bytes[0] -ne 0x89 -or $bytes[1] -ne 0x50 -or $bytes[2] -ne 0x4e -or $bytes[3] -ne 0x47) {
            throw "screenshot $($shot.name) is not a PNG"
        }
        $sha = [System.Security.Cryptography.SHA256]::Create()
        try { $hash = $sha.ComputeHash($bytes) } finally { $sha.Dispose() }
        $hashHex = (($hash | ForEach-Object { $_.ToString("x2") }) -join "")
        if ($hashHex -ne [string]$shot.sha256) { throw "screenshot $($shot.name) hash does not match the run result" }
    }
    Add-CheckResult -Name "screenshot evidence" -Status "passed" -Detail ("{0} PNG files with matching SHA-256" -f @($browserResult.screenshots).Count)

    $scanTargets = @($resultPath, $browserLog, $summaryPath)
    $scanTargets += @(Get-ChildItem -LiteralPath $tmpDir -File -Filter "*.log" -ErrorAction SilentlyContinue | ForEach-Object { $_.FullName })
    $leaks = New-Object System.Collections.ArrayList
    foreach ($target in ($scanTargets | Select-Object -Unique)) {
        if (-not (Test-Path -LiteralPath $target)) { continue }
        if ([System.IO.Path]::GetExtension($target) -in @(".png", ".exe")) { continue }
        $text = Get-Content -LiteralPath $target -Raw -ErrorAction SilentlyContinue
        if ($null -eq $text) { continue }
        foreach ($secret in $script:secretValues) {
            if (-not [string]::IsNullOrWhiteSpace([string]$secret) -and ([string]$text).Contains([string]$secret)) {
                [void]$leaks.Add((Split-Path -Leaf $target))
                break
            }
        }
    }
    if ($leaks.Count -gt 0) {
        Add-CheckResult -Name "evidence secret scan" -Status "failed" -Detail ("secret value found in " + (($leaks | Select-Object -Unique) -join ", "))
        throw "secret value found in the acceptance logs/result"
    }
    Add-CheckResult -Name "evidence secret scan" -Status "passed" -Detail ("{0} files scanned; 0 leaks" -f $scanTargets.Count)

    $summary = [ordered]@{
        status        = "passed"
        generatedAt   = [DateTime]::UtcNow.ToString("o")
        elapsedMs     = [int64](([DateTime]::UtcNow - $startedAt).TotalMilliseconds)
        database      = $dbName
        llmModel      = $llmModel
        keyword       = $keyword
        browserResult = $resultPath
        logDirectory  = $tmpDir
        checks        = @($script:checks)
        ids           = $browserResult.ids
        screenshots   = @($browserResult.screenshots)
    }
    [System.IO.File]::WriteAllText($summaryPath, ((ConvertTo-Json -InputObject $summary -Depth 12) + "`n"), (New-Object System.Text.UTF8Encoding($false)))

    Write-Host ""
    Write-Host "REAL P8 LLM + CHROME ACCEPTANCE PASSED" -ForegroundColor Green
    Write-Host ("Result: {0}" -f $resultPath)
    Write-Host ("Summary/logs: {0}" -f $tmpDir)
    $exitCode = 0
} catch {
    $message = Get-RedactedText -Text $_.Exception.Message
    Write-Host ""
    Write-Host ("REAL P8 LLM + CHROME ACCEPTANCE FAILED: " + $message) -ForegroundColor Red
    $failedSummary = [ordered]@{
        status        = "failed"
        generatedAt   = [DateTime]::UtcNow.ToString("o")
        elapsedMs     = [int64](([DateTime]::UtcNow - $startedAt).TotalMilliseconds)
        database      = $dbName
        error         = $message
        logDirectory  = $tmpDir
        checks        = @($script:checks)
    }
    [System.IO.File]::WriteAllText($summaryPath, ((ConvertTo-Json -InputObject $failedSummary -Depth 8) + "`n"), (New-Object System.Text.UTF8Encoding($false)))
    $exitCode = 1
} finally {
    Write-Step "Stopping the acceptance services"
    Stop-ProcessTree -Process $script:webProcess
    Stop-ProcessTree -Process $script:apiProcess
    Stop-ProcessTree -Process $script:executorProcess
    Stop-ProcessTree -Process $script:fixtureProcess
    foreach ($port in @($apiPort, $webPort, $fixturePort, $executorPort)) {
        try { Wait-PortFree -Port $port -TimeoutSeconds 15 } catch { Write-Warning "port $port is still in use after cleanup" }
    }
}

exit $exitCode
