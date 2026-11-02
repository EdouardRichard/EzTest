#requires -Version 5.1
<#
.SYNOPSIS
    P5 browser E2E: real API/DB/executor/fixture + real DeepSeek + Playwright Chrome.

.DESCRIPTION
    Boots an isolated P5 stack (database eztest_v02_p5, API 14345, Web 16870,
    executor 14347, fixture target 13338), prepares a real code-intelligence +
    visual scenario, and runs frontend/e2e/p5.spec.ts with the installed Chrome
    channel:

      * fixture repo : scripts/fixture-p5-repo.ps1 (.tmp/p5-repo, commits A/B)
      * fixture SUT  : scripts/fixture-p5-target.cjs (13338, light/shifted themes)
      * real LLM     : DeepSeek from .env through the admin LLM API
      * executor     : real Playwright Chrome on 127.0.0.1:14347
      * web          : Next production server on 127.0.0.1:16870
      * data         : SUT/env/pages/API/code repo/version/requirement/orders case/
                       visual case; real T0 index A/B; real T1 analysis A..B;
                       real first visual run (missing baseline); proposals and
                       anchors.

    Playwright covers the Code tab (index/changed files/diff/facts/anchors/
    proposals/deep links), the execution visual sub-view (missing baseline,
    admin baseline approval, retest -> passed, actual/diff viewer), the quality
    page and the resource page proposals confirm/reject, plus non-admin
    permission hiding. Screenshots go to .tmp/p5-acceptance/screenshots/ and the
    Playwright/console evidence to .tmp/p5-acceptance/e2e.log.

    Exit 0 = PASS, 1 = FAIL.
#>
[CmdletBinding()]
param(
    [int]$ApiPort = 14345,
    [int]$WebPort = 16870,
    [int]$ExecutorPort = 14347,
    [int]$FixturePort = 13338,
    [int]$PrepareTimeoutSeconds = 900,
    [switch]$SkipBuild
)

$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
$logDir = Join-Path $repoRoot ".tmp\p5-acceptance"
$screenshotDir = Join-Path $logDir "screenshots"
$exePath = Join-Path $repoRoot ".tmp\p5-acceptance\eztest-p5.exe"
$e2eLog = Join-Path $logDir "e2e.log"
$seedFile = Join-Path $logDir "e2e-seed.sql"
$testDatabase = "eztest_v02_p5"
$envPath = Join-Path $repoRoot ".env"
$fixtureRepoScript = Join-Path $repoRoot "scripts\fixture-p5-repo.ps1"
New-Item -ItemType Directory -Force -Path $logDir | Out-Null
New-Item -ItemType Directory -Force -Path $screenshotDir | Out-Null
New-Item -ItemType Directory -Force -Path (Split-Path -Parent $exePath) | Out-Null
Set-Content -LiteralPath $e2eLog -Value "" -Encoding UTF8

$apiUrl = "http://127.0.0.1:$ApiPort"
$webUrl = "http://127.0.0.1:$WebPort"
$executorUrl = "http://127.0.0.1:$ExecutorPort"
$fixtureUrl = "http://127.0.0.1:$FixturePort"
$started = New-Object System.Collections.ArrayList
$apiProcess = $null
$executorProcess = $null
$fixtureProcess = $null
$webProcess = $null
$token = ""
$http = $null
$versionID = ""
$executorToken = ""
$script:exitCode = 1
$dotenv = $null
$db = $null

function Log([string]$Message, [string]$Color = "Gray") {
    $line = "[{0}] {1}" -f (Get-Date -Format "HH:mm:ss"), $Message
    Write-Host $line -ForegroundColor $Color
    Add-Content -LiteralPath $e2eLog -Value $line -Encoding UTF8
}

function Test-Port([int]$Port) {
    $client = New-Object System.Net.Sockets.TcpClient
    try { $client.Connect("127.0.0.1", $Port); return $true } catch { return $false } finally { $client.Close() }
}

function Wait-Http([string]$Url, [int]$Seconds) {
    $deadline = (Get-Date).AddSeconds($Seconds)
    while ((Get-Date) -lt $deadline) {
        try {
            $response = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec 3
            if ($response.StatusCode -eq 200) { return $true }
        } catch { }
        Start-Sleep -Milliseconds 500
    }
    return $false
}

function Read-DotEnv {
    $path = Join-Path $repoRoot ".env"
    if (-not (Test-Path -LiteralPath $path)) { throw "Missing .env at $path" }
    $values = @{}
    foreach ($line in Get-Content -LiteralPath $path) {
        $trimmed = $line.Trim()
        if ($trimmed -eq "" -or $trimmed.StartsWith("#")) { continue }
        $eq = $trimmed.IndexOf("=")
        if ($eq -lt 1) { continue }
        $key = $trimmed.Substring(0, $eq).Trim()
        $value = $trimmed.Substring($eq + 1).Trim()
        if ($value.Length -ge 2 -and (($value.StartsWith('"') -and $value.EndsWith('"')) -or ($value.StartsWith("'") -and $value.EndsWith("'")))) {
            $value = $value.Substring(1, $value.Length - 2)
        }
        $values[$key] = $value
    }
    return $values
}

function ConvertFrom-DatabaseUrl([string]$RawUrl) {
    $pattern = '^(?<scheme>postgres(?:ql)?)://(?<user>[^:/@]+)(?::(?<password>[^@]*))?@(?<host>[^:/?]+)(?::(?<port>\d+))?/(?<database>[^?]+)(?<query>\?.*)?$'
    $match = [regex]::Match($RawUrl, $pattern)
    if (-not $match.Success) { throw "database URL is not a postgres URL: $RawUrl" }
    $port = 5432
    if ($match.Groups['port'].Success) { $port = [int]$match.Groups['port'].Value }
    return [pscustomobject]@{
        Scheme = $match.Groups['scheme'].Value
        User = [System.Uri]::UnescapeDataString($match.Groups['user'].Value)
        Password = [System.Uri]::UnescapeDataString($match.Groups['password'].Value)
        Host = $match.Groups['host'].Value
        Port = $port
        Database = $match.Groups['database'].Value
        Query = $match.Groups['query'].Value
    }
}

function Get-DatabaseUrl($Database, [string]$DatabaseName) {
    $query = $Database.Query
    if ([string]::IsNullOrEmpty($query)) { $query = "?sslmode=disable" }
    return "{0}://{1}:{2}@{3}:{4}/{5}{6}" -f $Database.Scheme,
        [System.Uri]::EscapeDataString($Database.User), [System.Uri]::EscapeDataString($Database.Password),
        $Database.Host, $Database.Port, $DatabaseName, $query
}

function Invoke-Api {
    param([string]$Method, [string]$Path, $Body, [string]$Token)
    $uri = $apiUrl + $Path
    $request = New-Object System.Net.Http.HttpRequestMessage ((New-Object System.Net.Http.HttpMethod($Method)), $uri)
    try {
        if (-not [string]::IsNullOrEmpty($Token)) {
            $request.Headers.Authorization = New-Object System.Net.Http.Headers.AuthenticationHeaderValue("Bearer", $Token)
        }
        if ($null -ne $Body) {
            $json = ConvertTo-Json -InputObject $Body -Depth 40 -Compress
            $request.Content = New-Object System.Net.Http.StringContent($json, [System.Text.Encoding]::UTF8, "application/json")
        }
        $response = $http.SendAsync($request).GetAwaiter().GetResult()
        try {
            $raw = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
            $parsed = $null
            if (-not [string]::IsNullOrWhiteSpace($raw)) {
                try { $parsed = ConvertFrom-Json $raw } catch { $parsed = $null }
            }
            return [pscustomobject]@{ Status = [int]$response.StatusCode; Raw = $raw; Json = $parsed }
        } finally { $response.Dispose() }
    } finally { $request.Dispose() }
}

function Assert-Status($Response, [int[]]$Expected, [string]$What) {
    if ($Expected -notcontains $Response.Status) {
        $detail = $Response.Raw
        if ($null -ne $Response.Json -and $null -ne $Response.Json.error) {
            $detail = ("{0}: {1}" -f $Response.Json.error.code, $Response.Json.error.message)
        }
        throw ("{0}: unexpected HTTP {1} ({2})" -f $What, $Response.Status, $detail)
    }
}

function Get-Array($Value) {
    if ($null -eq $Value) { return @() }
    return ,@($Value)
}

function Invoke-PsqlScalar([string]$SqlText) {
    $psql = Join-Path $repoRoot ".tools\pgsql\bin\psql.exe"
    $previousPassword = $env:PGPASSWORD
    $previousPreference = $ErrorActionPreference
    try {
        $env:PGPASSWORD = $db.Password
        $ErrorActionPreference = "Continue"
        $output = & $psql -h $db.Host -p $db.Port -U $db.User -d $testDatabase -v ON_ERROR_STOP=1 -tAc $SqlText 2>&1
        $exit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousPreference
        $env:PGPASSWORD = $previousPassword
    }
    if ($exit -ne 0) { throw ("psql failed ({0}): {1}" -f $exit, ($output -join " ")) }
    return (($output | Out-String).Trim())
}

function Invoke-PsqlFile([string]$FilePath) {
    $psql = Join-Path $repoRoot ".tools\pgsql\bin\psql.exe"
    $previousPassword = $env:PGPASSWORD
    $previousPreference = $ErrorActionPreference
    try {
        $env:PGPASSWORD = $db.Password
        $ErrorActionPreference = "Continue"
        $output = & $psql -h $db.Host -p $db.Port -U $db.User -d $testDatabase -v ON_ERROR_STOP=1 -q -f $FilePath 2>&1
        $exit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousPreference
        $env:PGPASSWORD = $previousPassword
    }
    if ($exit -ne 0) { throw ("psql -f failed ({0}): {1}" -f $exit, ($output -join " ")) }
}

function Wait-For {
    param([scriptblock]$Condition, [int]$TimeoutSeconds, [string]$Description, [int]$IntervalMilliseconds = 1000)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        $value = & $Condition
        if ($null -ne $value -and $value -ne $false) { return $value }
        Start-Sleep -Milliseconds $IntervalMilliseconds
    }
    throw ("timeout after {0}s waiting for {1}" -f $TimeoutSeconds, $Description)
}

function Stop-Started {
    if ($null -ne $script:apiProcess) {
        try { if (-not $script:apiProcess.HasExited) { Stop-Process -Id $script:apiProcess.Id -Force -ErrorAction SilentlyContinue } } catch { }
        $script:apiProcess = $null
    }
    if ($null -ne $executorProcess) {
        try { & taskkill /PID $executorProcess.Id /T /F *> $null } catch { }
        $executorProcess = $null
    }
    if ($null -ne $fixtureProcess) {
        try { if (-not $fixtureProcess.HasExited) { Stop-Process -Id $fixtureProcess.Id -Force -ErrorAction SilentlyContinue } } catch { }
        $fixtureProcess = $null
    }
    if ($null -ne $webProcess) {
        try { & taskkill /PID $webProcess.Id /T /F *> $null } catch { }
        $webProcess = $null
    }
    foreach ($process in $started) {
        try { & taskkill /PID $process.Id /T /F *> $null } catch { }
    }
    $started.Clear()
}

function Set-Setting([string]$Key, $Value) {
    $response = Invoke-Api -Method "PATCH" -Path ("/api/admin/settings/" + [System.Uri]::EscapeDataString($Key)) -Token $token -Body @{ value = $Value }
    Assert-Status $response @(200) ("PATCH setting " + $Key)
}

function Start-Api {
    if (Test-Port $ApiPort) { throw "API port $ApiPort is already in use before start; stale process would mask the new API/token" }
    $previousDb = $env:EZTEST_DATABASE_URL
    $previousApi = $env:EZTEST_API_PORT
    $previousWeb = $env:EZTEST_WEB_PORT
    $previousExecutor = $env:EZTEST_EXECUTOR_PORT
    try {
        $env:EZTEST_DATABASE_URL = $e2eDbUrl
        $env:EZTEST_API_PORT = "$ApiPort"
        $env:EZTEST_WEB_PORT = "$WebPort"
        $env:EZTEST_EXECUTOR_PORT = "$ExecutorPort"
        $script:apiProcess = Start-Process -FilePath $exePath -WorkingDirectory $repoRoot `
            -RedirectStandardOutput (Join-Path $logDir "e2e-api.out.log") `
            -RedirectStandardError (Join-Path $logDir "e2e-api.err.log") -PassThru
    } finally {
        $env:EZTEST_DATABASE_URL = $previousDb
        $env:EZTEST_API_PORT = $previousApi
        $env:EZTEST_WEB_PORT = $previousWeb
        $env:EZTEST_EXECUTOR_PORT = $previousExecutor
    }
    if ($script:apiProcess.HasExited) { throw ("API exited immediately; see " + (Join-Path $logDir "e2e-api.err.log")) }
    if (-not (Wait-Http "$apiUrl/api/health" 180)) { throw "API did not become ready" }
}

try {
    $dotenv = Read-DotEnv
    foreach ($key in @("EZTEST_DATABASE_URL", "llm_api_key", "llm_base_url", "llm_model_name")) {
        if ([string]::IsNullOrWhiteSpace($dotenv[$key])) { throw ("$key is missing from .env") }
    }
    $db = ConvertFrom-DatabaseUrl $dotenv["EZTEST_DATABASE_URL"]
    $e2eDbUrl = Get-DatabaseUrl -Database $db -DatabaseName $testDatabase
    if ($e2eDbUrl -notmatch ("/" + $testDatabase + "(\?|$)")) { throw "derived test database URL is invalid" }

    if ((Test-Port $ApiPort) -or (Test-Port $WebPort) -or (Test-Port $ExecutorPort) -or (Test-Port $FixturePort)) {
        throw "ports $ApiPort/$WebPort/$ExecutorPort/$FixturePort must be free (stop the acceptance stack first)"
    }

    $psql = Join-Path $repoRoot ".tools\pgsql\bin\psql.exe"
    if (-not (Test-Path $psql)) { throw "psql not found at $psql" }
    $oldPassword = $env:PGPASSWORD
    $oldPreference = $ErrorActionPreference
    $env:PGPASSWORD = $db.Password
    try {
        $ErrorActionPreference = "Continue"
        & $psql -h $db.Host -p $db.Port -U $db.User -d postgres -v ON_ERROR_STOP=1 -c "DROP DATABASE IF EXISTS $testDatabase WITH (FORCE);" *> (Join-Path $logDir "e2e-db.log")
        if ($LASTEXITCODE -ne 0) { throw "database drop failed (see e2e-db.log)" }
        & $psql -h $db.Host -p $db.Port -U $db.User -d postgres -v ON_ERROR_STOP=1 -c "CREATE DATABASE $testDatabase;" *>> (Join-Path $logDir "e2e-db.log")
        if ($LASTEXITCODE -ne 0) { throw "database create failed (see e2e-db.log)" }
    } finally {
        $env:PGPASSWORD = $oldPassword
        $ErrorActionPreference = $oldPreference
    }
    Log "isolated database $testDatabase ready" "Cyan"

    Log "build the deterministic fixture repo"
    $fixtureOutput = & powershell.exe -NoProfile -ExecutionPolicy Bypass -File $fixtureRepoScript 2>&1
    if ($LASTEXITCODE -ne 0) {
        Set-Content -LiteralPath (Join-Path $logDir "e2e-fixture-repo.log") -Value ($fixtureOutput -join [Environment]::NewLine) -Encoding UTF8
        throw "fixture-p5-repo.ps1 failed (see e2e-fixture-repo.log)"
    }
    $baseCommit = ""
    $headCommit = ""
    $fixtureRepo = ""
    foreach ($line in $fixtureOutput) {
        if ([string]$line -match '^P5_FIXTURE_REPO=(.*)$') { $fixtureRepo = $Matches[1].Trim() }
        if ([string]$line -match '^P5_FIXTURE_BASE_COMMIT=(.*)$') { $baseCommit = $Matches[1].Trim() }
        if ([string]$line -match '^P5_FIXTURE_HEAD_COMMIT=(.*)$') { $headCommit = $Matches[1].Trim() }
    }
    if ($baseCommit -notmatch '^[0-9a-f]{40}$' -or $headCommit -notmatch '^[0-9a-f]{40}$') { throw "fixture repo did not print valid SHAs" }
    Log ("fixture repo A={0} B={1}" -f $baseCommit.Substring(0, 7), $headCommit.Substring(0, 7))

    $node = Get-Command node -ErrorAction SilentlyContinue
    if ($null -eq $node) { throw "node not found on PATH" }
    Log "start fixture target on $fixtureUrl"
    $fixtureProcess = Start-Process -FilePath $node.Source -ArgumentList @((Join-Path $repoRoot "scripts\fixture-p5-target.cjs"), "--port", "$FixturePort") `
        -WorkingDirectory $repoRoot -RedirectStandardOutput (Join-Path $logDir "e2e-fixture.out.log") `
        -RedirectStandardError (Join-Path $logDir "e2e-fixture.err.log") -PassThru
    if (-not (Wait-Http "$fixtureUrl/healthz" 30)) { throw "fixture target did not become ready" }

    if (-not $SkipBuild -or -not (Test-Path $exePath)) {
        Push-Location (Join-Path $repoRoot "backend")
        try {
            $go = Join-Path $repoRoot ".tools\go\bin\go.exe"
            if (-not (Test-Path $go)) { $go = "go" }
            & $go build -o $exePath ./cmd/eztest *> (Join-Path $logDir "e2e-build.log")
            if ($LASTEXITCODE -ne 0) { throw "go build failed (see e2e-build.log)" }
        } finally { Pop-Location }
    }

    Log "start executor on $executorUrl with a random token"
    $executorToken = [System.Guid]::NewGuid().ToString("N") + [System.Guid]::NewGuid().ToString("N")
    $previousExecutorEnv = @{}
    foreach ($key in @("EZTEST_EXECUTOR_ADDR", "EZTEST_EXECUTOR_TOKEN", "EZTEST_EXECUTOR_MAX_CONTEXTS", "EZTEST_EXEC_ALLOWED_HOSTS")) {
        $previousExecutorEnv[$key] = [Environment]::GetEnvironmentVariable($key)
    }
    try {
        $env:EZTEST_EXECUTOR_ADDR = "127.0.0.1:$ExecutorPort"
        $env:EZTEST_EXECUTOR_TOKEN = $executorToken
        $env:EZTEST_EXECUTOR_MAX_CONTEXTS = "8"
        $env:EZTEST_EXEC_ALLOWED_HOSTS = "127.0.0.1,localhost"
        $executorProcess = Start-Process -FilePath $node.Source -ArgumentList @("src/server.mjs") `
            -WorkingDirectory (Join-Path $repoRoot "executor") `
            -RedirectStandardOutput (Join-Path $logDir "e2e-executor.out.log") `
            -RedirectStandardError (Join-Path $logDir "e2e-executor.err.log") -PassThru
    } finally {
        foreach ($key in @("EZTEST_EXECUTOR_ADDR", "EZTEST_EXECUTOR_TOKEN", "EZTEST_EXECUTOR_MAX_CONTEXTS", "EZTEST_EXEC_ALLOWED_HOSTS")) {
            if ($null -eq $previousExecutorEnv[$key]) { Remove-Item ("Env:" + $key) -ErrorAction SilentlyContinue }
            else { Set-Item ("Env:" + $key) -Value $previousExecutorEnv[$key] }
        }
    }
    if (-not (Wait-Http "$executorUrl/healthz" 60)) { throw "executor did not become healthy" }

    Log "bootstrap API: migrations, executor settings and the real DeepSeek config"
    Start-Api
    Add-Type -AssemblyName System.Net.Http | Out-Null
    $http = New-Object System.Net.Http.HttpClient
    $http.Timeout = [TimeSpan]::FromSeconds(600)
    $login = Invoke-Api -Method "POST" -Path "/api/auth/login" -Token $null -Body @{ username = "admin"; password = "admin123" }
    Assert-Status $login @(200) "POST /api/auth/login (bootstrap)"
    $token = [string]$login.Json.data.token
    foreach ($setting in @(
        @{ Key = "execution.executor_url"; Value = $executorUrl },
        @{ Key = "execution.executor_token"; Value = $executorToken },
        @{ Key = "execution.executor_heartbeat_seconds"; Value = 2 },
        @{ Key = "execution.executor_offline_seconds"; Value = 6 },
        @{ Key = "execution.human_request_timeout_seconds"; Value = 1800 },
        @{ Key = "feature.auto_gap_design"; Value = $false }
    )) { Set-Setting $setting.Key $setting.Value }
    $config = Invoke-Api -Method "POST" -Path "/api/admin/llm/configs" -Token $token -Body @{
        name = "p5-e2e-deepseek"; provider = "openai"; protocol = "openai"
        base_url = [string]$dotenv["llm_base_url"]; api_key = [string]$dotenv["llm_api_key"]
        timeout_seconds = 180; max_retries = 1; is_default = $true; status = "active"
    }
    Assert-Status $config @(200) "POST /api/admin/llm/configs (real DeepSeek)"
    $configID = [string]$config.Json.data.id
    $test = Invoke-Api -Method "POST" -Path ("/api/admin/llm/configs/{0}/test" -f $configID) -Token $token
    if ($test.Status -ne 200 -or ($null -ne $test.Json.data -and $test.Json.data.ok -eq $false)) {
        throw "real DeepSeek LLM connectivity test failed; P5 E2E never uses a fake LLM"
    }
    $refresh = Invoke-Api -Method "POST" -Path ("/api/admin/llm/configs/{0}/refresh-models" -f $configID) -Token $token
    Assert-Status $refresh @(200) "POST refresh-models (real DeepSeek)"
    $catalog = Invoke-Api -Method "GET" -Path "/api/admin/llm/models" -Token $token
    $models = Get-Array $catalog.Json.data
    $wanted = [string]$dotenv["llm_model_name"]
    if (-not (@($models | Where-Object { $_.model -eq $wanted }).Count -ge 1)) {
        $addModel = Invoke-Api -Method "POST" -Path ("/api/admin/llm/configs/{0}/models" -f $configID) -Token $token -Body @{
            model = $wanted; context_window = 128000; max_output = 8192; supports_tools = $true; supports_vision = $false
        }
        Assert-Status $addModel @(200) ("add LLM catalog model " + $wanted)
        $catalog = Invoke-Api -Method "GET" -Path "/api/admin/llm/models" -Token $token
        $models = Get-Array $catalog.Json.data
    }
    foreach ($model in $models) {
        if ([string]$model.model -eq $wanted) { break }
        $deleted = Invoke-Api -Method "DELETE" -Path ("/api/admin/llm/configs/{0}/models/{1}" -f $configID, [string]$model.id) -Token $token
        Assert-Status $deleted @(200) ("delete non-primary LLM model " + $model.model)
    }
    $user = Invoke-Api -Method "POST" -Path "/api/admin/users" -Token $token -Body @{ username = "p5user"; display_name = "P5 User"; password = "p5-user-pass-123" }
    Assert-Status $user @(201) "POST normal user"

    if ($null -ne $script:apiProcess) {
        try { if (-not $script:apiProcess.HasExited) { Stop-Process -Id $script:apiProcess.Id -Force -ErrorAction SilentlyContinue } } catch { }
        $script:apiProcess.WaitForExit(10000) | Out-Null
        $script:apiProcess = $null
    }
    if (Test-Port $ApiPort) { throw "API port $ApiPort is still in use after stopping the bootstrap API" }
    Log "start the acceptance API"
    Start-Api
    $login = Invoke-Api -Method "POST" -Path "/api/auth/login" -Token $null -Body @{ username = "admin"; password = "admin123" }
    Assert-Status $login @(200) "POST /api/auth/login"
    $token = [string]$login.Json.data.token
    Wait-For -TimeoutSeconds 120 -Description "executor registered active" -Condition {
        $response = Invoke-Api -Method "GET" -Path "/api/admin/executors" -Token $token
        if ($response.Status -ne 200) { return $null }
        foreach ($item in (Get-Array $response.Json.data)) {
            if ([string]$item.endpoint -eq $executorUrl -and [string]$item.status -eq "active") { return $item }
        }
        return $null
    } | Out-Null

    Log "restart executor AFTER the final API settings so the token/registry match"
    if ($null -ne $executorProcess) {
        try { & taskkill /PID $executorProcess.Id /T /F *> $null } catch { }
        $executorProcess = $null
    }
    Start-Sleep -Seconds 1
    $executorEnv = @{}
    foreach ($key in @("EZTEST_EXECUTOR_ADDR", "EZTEST_EXECUTOR_TOKEN", "EZTEST_EXECUTOR_MAX_CONTEXTS", "EZTEST_EXEC_ALLOWED_HOSTS")) {
        $executorEnv[$key] = [Environment]::GetEnvironmentVariable($key)
    }
    try {
        $env:EZTEST_EXECUTOR_ADDR = "127.0.0.1:$ExecutorPort"
        $env:EZTEST_EXECUTOR_TOKEN = $executorToken
        $env:EZTEST_EXECUTOR_MAX_CONTEXTS = "8"
        $env:EZTEST_EXEC_ALLOWED_HOSTS = "127.0.0.1,localhost"
        $executorProcess = Start-Process -FilePath $node.Source -ArgumentList @("src/server.mjs") `
            -WorkingDirectory (Join-Path $repoRoot "executor") `
            -RedirectStandardOutput (Join-Path $logDir "e2e-executor.out.log") `
            -RedirectStandardError (Join-Path $logDir "e2e-executor.err.log") -PassThru
    } finally {
        foreach ($key in @("EZTEST_EXECUTOR_ADDR", "EZTEST_EXECUTOR_TOKEN", "EZTEST_EXECUTOR_MAX_CONTEXTS", "EZTEST_EXEC_ALLOWED_HOSTS")) {
            if ($null -eq $executorEnv[$key]) { Remove-Item ("Env:" + $key) -ErrorAction SilentlyContinue }
            else { Set-Item ("Env:" + $key) -Value $executorEnv[$key] }
        }
    }
    if (-not (Wait-Http "$executorUrl/healthz" 60)) { throw "executor did not become healthy after restart" }
    Wait-For -TimeoutSeconds 120 -Description "executor registered active" -Condition {
        $probe = Invoke-Api -Method "GET" -Path "/api/admin/executors" -Token $token
        if ($probe.Status -ne 200) { return $null }
        foreach ($item in (Get-Array $probe.Json.data)) {
            if ([string]$item.endpoint -eq $executorUrl -and [string]$item.status -eq "active") { return $item }
        }
        return $null
    } | Out-Null
    Log "create SUT/env/pages/API/code repo/version/requirement and seed cases"
    $sut = Invoke-Api -Method "POST" -Path "/api/resources/suts" -Token $token -Body @{ name = "P5 E2E Fixture SUT"; intro = "P5 browser E2E" }
    Assert-Status $sut @(200, 201) "POST SUT"
    $sutID = [string]$sut.Json.data.id
    $environment = Invoke-Api -Method "POST" -Path "/api/resources/suts/$sutID/environments" -Token $token -Body @{
        name = "p5-e2e"; base_url = $fixtureUrl; is_default = $true; status = "active"
    }
    Assert-Status $environment @(200, 201) "POST environment"
    $environmentID = [string]$environment.Json.data.id
    $pageOrders = Invoke-Api -Method "POST" -Path "/api/resources/environments/$environmentID/pages" -Token $token -Body @{
        name = "P5 E2E orders"; url = ($fixtureUrl + "/p5/orders"); route_template = "/p5/orders"; page_type = "page"; auth_role = "guest"; module = "p5"
    }
    Assert-Status $pageOrders @(200, 201) "POST orders page"
    $pageOrdersID = [string]$pageOrders.Json.data.id
    $pageVisual = Invoke-Api -Method "POST" -Path "/api/resources/environments/$environmentID/pages" -Token $token -Body @{
        name = "P5 E2E visual"; url = ($fixtureUrl + "/p5/visual?theme=light"); route_template = "/p5/visual"; page_type = "page"; auth_role = "guest"; module = "p5"
    }
    Assert-Status $pageVisual @(200, 201) "POST visual page"
    $pageVisualID = [string]$pageVisual.Json.data.id
    $pageImpact = Invoke-Api -Method "POST" -Path "/api/resources/environments/$environmentID/pages" -Token $token -Body @{
        name = "P5 E2E /orders route"; url = ($fixtureUrl + "/p5/orders"); route_template = "/orders"; page_type = "page"; auth_role = "guest"; module = "p5"
    }
    Assert-Status $pageImpact @(200, 201) "POST impact page"
    $pageImpactID = [string]$pageImpact.Json.data.id
    $apiRefund = Invoke-Api -Method "POST" -Path "/api/resources/environments/$environmentID/apis" -Token $token -Body @{
        method = "POST"; path_template = "/api/refund"; name = "P5 E2E refund API"; auth_role = "guest"; schema_ref = ""
    }
    Assert-Status $apiRefund @(200, 201) "POST refund API"
    $apiRefundID = [string]$apiRefund.Json.data.id
    $repo = Invoke-Api -Method "POST" -Path "/api/resources/code-repos" -Token $token -Body @{
        name = "p5-e2e-repo"; path = $fixtureRepo; branch = "main"; include = @(); exclude = @()
    }
    Assert-Status $repo @(200, 201) "POST code repository"
    $repoID = [string]$repo.Json.data.id
    $version = Invoke-Api -Method "POST" -Path "/api/versions" -Token $token -Body @{
        title = ("P5 E2E " + (Get-Date -Format "yyyy-MM-dd HH:mm:ss"))
        bindings = @(@{
            sut_id = $sutID; environment_id = $environmentID; code_repository_id = $repoID
            page_ids = @($pageOrdersID, $pageVisualID, $pageImpactID); api_ids = @($apiRefundID)
            database_ids = @(); accounts = @()
        })
    }
    Assert-Status $version @(201) "POST version"
    $versionID = [string]$version.Json.data.id
    $versionSUTID = [string]$version.Json.data.bindings[0].id
    Invoke-PsqlScalar "UPDATE version_sut SET code_baseline_commit = '$baseCommit' WHERE id = $versionSUTID;" | Out-Null

    $requirement = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/requirements" -Token $token -Body @{
        title = "P5 E2E visual requirement"; description = "P5 browser E2E requirement"
        acceptance_criteria = "Code tab and visual view are exercised"; priority = "P1"; constraints = @{}
        targets = @{ version_suts = @($versionSUTID); pages = @($pageVisualID, $pageOrdersID); apis = @() }
    }
    Assert-Status $requirement @(201) "POST requirement"
    $requirementID = [string]$requirement.Json.data.id
    $requirementVersionID = [string]$requirement.Json.data.current_version.id
    $confirmRequirement = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/requirements/$requirementID/confirmations" -Token $token
    Assert-Status $confirmRequirement @(200) "confirm requirement"

    $hash = ("0123456789abcdef" * 4)
    $actionOrders = "Use browser.goto to open the bound orders page for version_sut_id=$versionSUTID with account_role=user (the /p5/orders page URL is listed in the task context). Then use browser.snapshot and browser.screenshot, verify the heading text 'P5 Orders', and record assertion 1 through execution.assertion_record."
    $actionVisual = "Use browser.goto to open the visual fixture page bound to version_sut_id=$versionSUTID with account_role=user (the /p5/visual page URL is listed in the task context). Then call execution.visual_assert with assertionId set to the visual_assertion_id of assertion 1 from the task context. Do not call execution.assertion_record for this kind=visual assertion."
    $escOrders = $actionOrders.Replace("'", "''")
    $escVisual = $actionVisual.Replace("'", "''")
    $seedSql = @"
BEGIN;
INSERT INTO case_scenario (id, version_id, requirement_id, requirement_version_id, name, description, version_sut_ids, page_ids, api_ids, sort_order) VALUES (965001, $versionID, $requirementID, $requirementVersionID, 'E2E orders scenario', '', '["$versionSUTID"]'::jsonb, '["$pageOrdersID"]'::jsonb, '[]'::jsonb, 1);
INSERT INTO case_scenario (id, version_id, requirement_id, requirement_version_id, name, description, version_sut_ids, page_ids, api_ids, sort_order) VALUES (965002, $versionID, $requirementID, $requirementVersionID, 'E2E visual scenario', '', '["$versionSUTID"]'::jsonb, '["$pageVisualID"]'::jsonb, '[]'::jsonb, 2);
INSERT INTO case_entity (id, scenario_id, current_version_id, status) VALUES (966001, 965001, 967001, 'draft');
INSERT INTO case_entity (id, scenario_id, current_version_id, status) VALUES (966002, 965002, 967002, 'draft');
INSERT INTO case_version (id, case_id, revision, title, description, preconditions, test_data, priority, default_version_sut_id, version_sut_ids, page_ids, api_ids, status, content_hash) VALUES (967001, 966001, 1, 'E2E orders page', '', '', '{}'::jsonb, 'P1', $versionSUTID, '["$versionSUTID"]'::jsonb, '["$pageOrdersID"]'::jsonb, '[]'::jsonb, 'draft', '$hash');
INSERT INTO case_version (id, case_id, revision, title, description, preconditions, test_data, priority, default_version_sut_id, version_sut_ids, page_ids, api_ids, status, content_hash) VALUES (967002, 966002, 1, 'E2E visual light', '', '', '{}'::jsonb, 'P1', $versionSUTID, '["$versionSUTID"]'::jsonb, '["$pageVisualID"]'::jsonb, '[]'::jsonb, 'draft', '$hash');
INSERT INTO case_step (id, case_version_id, seq, action, expected, target_version_sut_id, target_page_id, target_api_id, account_role, data) VALUES (968001, 967001, 1, '$escOrders', 'The P5 orders page shows P5 Orders', $versionSUTID, $pageOrdersID, NULL, 'user', '{}'::jsonb);
INSERT INTO case_step (id, case_version_id, seq, action, expected, target_version_sut_id, target_page_id, target_api_id, account_role, data) VALUES (968002, 967002, 1, '$escVisual', 'The visual page is captured and compared with its baseline', $versionSUTID, $pageVisualID, NULL, 'user', '{}'::jsonb);
INSERT INTO case_assertion (id, case_version_id, step_seq, kind, target, expected, target_version_sut_id, target_api_id, database_id) VALUES (969001, 967001, 1, 'ui', '#p5-orders h1', 'P5 Orders', $versionSUTID, NULL, NULL);
INSERT INTO case_assertion (id, case_version_id, step_seq, kind, target, expected, target_version_sut_id, target_api_id, database_id) VALUES (969002, 967002, 1, 'visual', '#visual-root', 'The visual page matches the approved baseline', $versionSUTID, NULL, NULL);
COMMIT;
"@
    Set-Content -LiteralPath $seedFile -Value $seedSql -Encoding UTF8
    Invoke-PsqlFile $seedFile

    $visualConfig = Invoke-Api -Method "PUT" -Path "/api/versions/$versionID/visual/assertions/969002" -Token $token -Body @{
        versionSutId = $versionSUTID; selector = ""; viewport = @{ width = 1280; height = 720 }; deviceScaleFactor = 1; threshold = 0.005
    }
    Assert-Status $visualConfig @(200) "PUT visual assertion"
    $visualAssertionID = [string]$visualConfig.Json.data.id

    $anchor = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/code/anchors" -Token $token -Body @{
        subjectKind = "requirement"; subjectId = $requirementID; repoId = $repoID
        filePath = "src/api/refund.ts"; startLine = 1; endLine = 3; commit = $headCommit
    }
    Assert-Status $anchor @(201) "POST requirement anchor"
    $anchorID = [string]$anchor.Json.data.id

    $knownWaves = @()
    $waves = Invoke-Api -Method "GET" -Path "/api/versions/$versionID/waves?limit=100" -Token $token
    if ($waves.Status -eq 200) { $knownWaves = @((Get-Array $waves.Json.data) | ForEach-Object { [string]$_.id }) }
    foreach ($caseID in @(966001, 966002)) {
        $confirmCase = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/design/confirmations" -Token $token -Body @{ scope = "case"; target_id = "$caseID" }
        Assert-Status $confirmCase @(200) ("confirm case " + $caseID)
    }
    $wave1 = Wait-For -TimeoutSeconds 120 -Description "first execution wave" -Condition {
        $response = Invoke-Api -Method "GET" -Path "/api/versions/$versionID/waves?limit=100" -Token $token
        if ($response.Status -ne 200) { return $null }
        foreach ($wave in (Get-Array $response.Json.data)) {
            if ($knownWaves -notcontains [string]$wave.id) { return $wave }
        }
        return $null
    }
    $wave1ID = [string]$wave1.id
    Log ("wave1 {0} started; indexing and analysing in parallel" -f $wave1ID)

    Log "T0 index A/B + real-LLM analysis A..B"
    $indexA = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/code/index" -Token $token -Body @{ repoId = $repoID; commit = $baseCommit }
    Assert-Status $indexA @(202) "POST index A"
    Wait-For -TimeoutSeconds 180 -Description "index A done" -Condition {
        $response = Invoke-Api -Method "GET" -Path "/api/code/repos/$repoID/index-runs/$([string]$indexA.Json.data.id)" -Token $token
        if ($response.Status -eq 200 -and [string]$response.Json.data.status -eq "done") { return $true }
        return $null
    } | Out-Null
    $indexB = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/code/index" -Token $token -Body @{ repoId = $repoID; commit = $headCommit }
    Assert-Status $indexB @(202) "POST index B"
    Wait-For -TimeoutSeconds 180 -Description "index B done" -Condition {
        $response = Invoke-Api -Method "GET" -Path "/api/code/repos/$repoID/index-runs/$([string]$indexB.Json.data.id)" -Token $token
        if ($response.Status -eq 200 -and [string]$response.Json.data.status -eq "done") { return $true }
        return $null
    } | Out-Null
    $analysis = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/code/analyses" -Token $token -Body @{ repoId = $repoID; headCommit = $headCommit }
    Assert-Status $analysis @(202) "POST analysis A..B"
    $analysisID = [string]$analysis.Json.data.id
    $analysisDetail = Wait-For -TimeoutSeconds $PrepareTimeoutSeconds -Description "analysis done" -Condition {
        $response = Invoke-Api -Method "GET" -Path "/api/versions/$versionID/code/analyses/$analysisID" -Token $token
        if ($response.Status -ne 200) { return $null }
        if ([string]$response.Json.data.analysis.status -eq "done") { return $response.Json.data }
        if ([string]$response.Json.data.analysis.status -eq "failed") { throw "analysis failed" }
        return $null
    }
    Log ("analysis done degraded={0} facts={1}" -f $analysisDetail.analysis.degraded, $analysisDetail.analysis.stats.facts)

    Log "wait for the first visual wave to finish (missing baseline)"
    Wait-For -TimeoutSeconds $PrepareTimeoutSeconds -Description "wave1 terminal" -Condition {
        $response = Invoke-Api -Method "GET" -Path "/api/versions/$versionID/waves?limit=100" -Token $token
        if ($response.Status -ne 200) { return $null }
        foreach ($wave in (Get-Array $response.Json.data)) {
            if ([string]$wave.id -eq $wave1ID -and @("done", "failed", "cancelled") -contains [string]$wave.status) { return $wave }
        }
        return $null
    } | Out-Null
    $runsResponse = Invoke-Api -Method "GET" -Path "/api/versions/$versionID/waves/$wave1ID/case-runs?limit=100" -Token $token
    Assert-Status $runsResponse @(200) "GET wave1 case-runs"
    $runs = Get-Array $runsResponse.Json.data
    $visualRun = @($runs | Where-Object { [string]$_.case_version_id -eq "967002" })[0]
    if ($null -eq $visualRun) { throw "visual case run not found in wave1" }
    $visualRunID = [string]$visualRun.id
    $visualDiffs = Invoke-Api -Method "GET" -Path "/api/versions/$versionID/visual/diffs?caseRunId=$visualRunID&limit=100" -Token $token
    Assert-Status $visualDiffs @(200) "GET visual diffs"
    $diffs = Get-Array $visualDiffs.Json.data
    if ($diffs.Count -lt 1) { throw "visual diff missing for the first run" }
    $actualEvidenceID = [string]$diffs[0].actual_evidence_id

    Log "generate proposals from code facts and network evidence"
    $proposals = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/code/page-api-proposals" -Token $token -Body @{ repoId = $repoID }
    Assert-Status $proposals @(200) "POST proposals"

    Log "start the web app on $webUrl"
    $env:NEXT_PUBLIC_API_BASE_URL = $apiUrl
    Remove-Item Env:FORCE_COLOR -ErrorAction SilentlyContinue
    $env:NO_COLOR = "1"
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
    $webProcess = Start-Process -FilePath "pnpm.cmd" -ArgumentList "exec", "next", "start", "-p", "$WebPort" `
        -WorkingDirectory (Join-Path $repoRoot "frontend") `
        -RedirectStandardOutput (Join-Path $logDir "e2e-web.out.log") `
        -RedirectStandardError (Join-Path $logDir "e2e-web.err.log") -PassThru
    [void]$started.Add($webProcess)
    if (-not (Wait-Http "$webUrl/login" 180)) { throw "Web did not become ready at $webUrl" }
    Log "web app ready"

    $env:EZTEST_E2E_API_URL = $apiUrl
    $env:EZTEST_E2E_BASE_URL = $webUrl
    $env:EZTEST_E2E_P5_VERSION_ID = $versionID
    $env:EZTEST_E2E_P5_REPO_ID = $repoID
    $env:EZTEST_E2E_P5_REQUIREMENT_ID = $requirementID
    $env:EZTEST_E2E_P5_VISUAL_CASE_VERSION_ID = "967002"
    $env:EZTEST_E2E_P5_VISUAL_ASSERTION_ID = $visualAssertionID
    $env:EZTEST_E2E_P5_VISUAL_CASE_RUN_ID = $visualRunID
    $env:EZTEST_E2E_P5_WAVE1_ID = $wave1ID
    $env:EZTEST_E2E_P5_ANCHOR_ID = $anchorID
    $env:EZTEST_E2E_P5_ANALYSIS_ID = $analysisID
    $env:EZTEST_E2E_P5_BASELINE_COMMIT = $baseCommit
    $env:EZTEST_E2E_P5_HEAD_COMMIT = $headCommit
    $env:EZTEST_E2E_P5_SCREENSHOT_DIR = $screenshotDir
    $env:EZTEST_E2E_P5_TIMEOUT_MS = "900000"
    Push-Location (Join-Path $repoRoot "frontend")
    $previousPreference = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        & pnpm exec playwright test e2e/p5.spec.ts --reporter=list *>&1 |
            Tee-Object -FilePath (Join-Path $logDir "e2e-playwright.log")
        $playwrightCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousPreference
        Pop-Location
    }
    if ($playwrightCode -ne 0) { throw "Playwright p5.spec.ts failed (see e2e-playwright.log)" }
    Log "P5 browser E2E PASS (code tab, visual approve/retest/diff, quality, resource proposals, permissions)" "Green"
    $script:exitCode = 0
} catch {
    Log "E2E FAIL: $($_.Exception.Message)" "Red"
    if ($null -ne $_.InvocationInfo) { Write-Host ("      at " + $_.InvocationInfo.ScriptName + ":" + $_.InvocationInfo.ScriptLineNumber) -ForegroundColor DarkGray }
    $script:exitCode = 1
} finally {
    try { Stop-Started } catch { }
    try { if ($null -ne $http) { $http.Dispose() } } catch { }
    try {
        $summary = @(
            "P5 browser E2E",
            ("base commit : " + $baseCommit),
            ("head commit : " + $headCommit),
            ("version     : " + $versionID),
            ("visual run  : " + $visualRunID),
            ("screenshots : " + $screenshotDir),
            ("playwright  : " + (Join-Path $logDir "e2e-playwright.log"))
        ) -join [Environment]::NewLine
        Set-Content -LiteralPath (Join-Path $logDir "e2e-summary.txt") -Value $summary -Encoding UTF8
    } catch { }
}
exit $script:exitCode
