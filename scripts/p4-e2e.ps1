#requires -Version 5.1
<#
.SYNOPSIS
    P4 browser E2E: real-DeepSeek delivery workspace (frontend/e2e/p4.spec.ts).

.DESCRIPTION
    Starts an isolated P4 stack and runs Playwright:
      * database : eztest_v02_p4 (dropped and recreated on every run)
      * target DB: EZTEST_TEST_DATABASE_URL (seeded by scripts/p3-fixture-db.ps1)
      * fixtures : scripts/fixture-target.cjs on 127.0.0.1:13335 (orders) and
                   127.0.0.1:13337 (payments)
      * executor : executor/src/server.mjs on 127.0.0.1:14346 with a random token
      * API      : freshly built .tmp/p4/eztest-p4.exe on 127.0.0.1:14344
      * Web      : Next production server on 127.0.0.1:16869
      * LLM      : the real DeepSeek config from .env (never a fake LLM)

    The script prepares one version with a passing case, a failing case (for the
    issue/report tabs) and a 2FA takeover case. It waits until the 2FA human
    request is open and the failing issue was auto-created, exports the version
    and case ids, then runs frontend/e2e/p4.spec.ts. The spec resolves the human
    request, waits for the real Steward to complete the version, and exercises
    the version tabs, issue actions, report revision/stale/regenerate, chain
    filters/history/deep-link/truncation, completed/reopen and the todo drawer.
    Screenshots go to .tmp/p4-acceptance/screenshots/. Logs go to
    .tmp/p4-acceptance/.

.NOTES
    Windows PowerShell 5.1+ compatible. Never touches the development database,
    the development web server or executor 14346 outside this isolated stack.
#>
[CmdletBinding()]
param(
    [int]$ApiPort = 14344,
    [int]$WebPort = 16869,
    [int]$ExecutorPort = 14346,
    [int]$OrdersPort = 13335,
    [int]$PaymentsPort = 13337,
    [int]$PrepareTimeoutSeconds = 900,
    [switch]$SkipBuild
)

$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
$logDir = Join-Path $repoRoot ".tmp\p4-acceptance"
$screenshotDir = Join-Path $logDir "screenshots"
$exePath = Join-Path $repoRoot ".tmp\p4\eztest-p4.exe"
$e2eLog = Join-Path $logDir "e2e.log"
$seedFile = Join-Path $logDir "e2e-seed.sql"
$testDatabase = "eztest_v02_p4"
New-Item -ItemType Directory -Force -Path $logDir | Out-Null
New-Item -ItemType Directory -Force -Path $screenshotDir | Out-Null
New-Item -ItemType Directory -Force -Path (Split-Path -Parent $exePath) | Out-Null
Set-Content -LiteralPath $e2eLog -Value "" -Encoding UTF8

$apiUrl = "http://127.0.0.1:$ApiPort"
$webUrl = "http://127.0.0.1:$WebPort"
$executorUrl = "http://127.0.0.1:$ExecutorPort"
$ordersUrl = "http://127.0.0.1:$OrdersPort"
$paymentsUrl = "http://127.0.0.1:$PaymentsPort"
$started = New-Object System.Collections.ArrayList
$apiProcess = $null
$executorProcess = $null
$token = ""
$http = $null
$versionID = ""
$versionSUTID = ""
$caseCVersionID = ""
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
            $json = ConvertTo-Json -InputObject $Body -Depth 30 -Compress
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
    param([scriptblock]$Condition, [int]$TimeoutSeconds, [string]$Description)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        $value = & $Condition
        if ($null -ne $value -and $value -ne $false) { return $value }
        Start-Sleep -Milliseconds 1000
    }
    throw ("timeout after {0}s waiting for {1}" -f $TimeoutSeconds, $Description)
}

function Stop-Started {
    if ($null -ne $apiProcess) {
        try { if (-not $apiProcess.HasExited) { Stop-Process -Id $apiProcess.Id -Force -ErrorAction SilentlyContinue } } catch { }
        $apiProcess = $null
    }
    if ($null -ne $executorProcess) {
        try { & taskkill /PID $executorProcess.Id /T /F *> $null } catch { }
        $executorProcess = $null
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

try {
    $dotenv = Read-DotEnv
    foreach ($key in @("EZTEST_DATABASE_URL", "llm_api_key", "llm_base_url", "llm_model_name")) {
        if ([string]::IsNullOrWhiteSpace($dotenv[$key])) { throw ("$key is missing from .env") }
    }
    $targetUrl = $dotenv["EZTEST_TEST_DATABASE_URL"]
    if ([string]::IsNullOrWhiteSpace($targetUrl)) { throw "EZTEST_TEST_DATABASE_URL is missing from .env" }
    $db = ConvertFrom-DatabaseUrl $dotenv["EZTEST_DATABASE_URL"]
    $e2eDbUrl = Get-DatabaseUrl -Database $db -DatabaseName $testDatabase

    if ((Test-Port $ApiPort) -or (Test-Port $WebPort) -or (Test-Port $ExecutorPort) -or (Test-Port $OrdersPort) -or (Test-Port $PaymentsPort)) {
        throw "ports $ApiPort/$WebPort/$ExecutorPort/$OrdersPort/$PaymentsPort must be free (stop the acceptance stack first)"
    }

    $psql = Join-Path $repoRoot ".tools\pgsql\bin\psql.exe"
    if (-not (Test-Path $psql)) { throw "psql not found at $psql" }
    $oldPassword = $env:PGPASSWORD
    $oldPreference = $ErrorActionPreference
    $env:PGPASSWORD = $db.Password
    try {
        $ErrorActionPreference = "Continue"
        $resetLog = New-Object System.Collections.ArrayList
        for ($attempt = 1; $attempt -le 2; $attempt++) {
            $dropOutput = & $psql -h $db.Host -p $db.Port -U $db.User -d postgres -v ON_ERROR_STOP=1 `
                -c "DROP DATABASE IF EXISTS $testDatabase WITH (FORCE);" 2>&1
            $dropExit = $LASTEXITCODE
            [void]$resetLog.Add(("attempt {0} drop exit={1}: {2}" -f $attempt, $dropExit, ($dropOutput -join " ")))
            $createOutput = & $psql -h $db.Host -p $db.Port -U $db.User -d postgres -v ON_ERROR_STOP=1 `
                -c "CREATE DATABASE $testDatabase;" 2>&1
            $createExit = $LASTEXITCODE
            [void]$resetLog.Add(("attempt {0} create exit={1}: {2}" -f $attempt, $createExit, ($createOutput -join " ")))
            if ($dropExit -eq 0 -and $createExit -eq 0) { break }
            Start-Sleep -Milliseconds 800
        }
        Set-Content -LiteralPath (Join-Path $logDir "e2e-db.log") -Value ($resetLog -join [Environment]::NewLine) -Encoding UTF8
        if ($dropExit -ne 0 -or $createExit -ne 0) { throw "database reset failed (see e2e-db.log)" }
    } finally {
        $env:PGPASSWORD = $oldPassword
        $ErrorActionPreference = $oldPreference
    }
    Log "isolated database $testDatabase ready" "Cyan"

    Log "seeding the P3 target fixture database"
    $fixtureDbOutput = & powershell.exe -NoProfile -ExecutionPolicy Bypass -File (Join-Path $repoRoot "scripts\p3-fixture-db.ps1") -DatabaseUrl $targetUrl 2>&1
    if ($LASTEXITCODE -ne 0) {
        Set-Content -LiteralPath (Join-Path $logDir "e2e-fixture-db.log") -Value ($fixtureDbOutput -join [Environment]::NewLine) -Encoding UTF8
        throw "p3-fixture-db.ps1 failed (see e2e-fixture-db.log)"
    }

    if (-not $SkipBuild -or -not (Test-Path $exePath)) {
        Push-Location (Join-Path $repoRoot "backend")
        try {
            $go = Join-Path $repoRoot ".tools\go\bin\go.exe"
            if (-not (Test-Path $go)) { $go = "go" }
            & $go build -o $exePath ./cmd/eztest *> (Join-Path $logDir "e2e-build.log")
            if ($LASTEXITCODE -ne 0) { throw "go build failed (see e2e-build.log)" }
        } finally { Pop-Location }
    }

    Log "starting fixture SUTs (orders $ordersUrl, payments $paymentsUrl)"
    $previousFixturePort = $env:FIXTURE_PORT
    $previousFixtureVariant = $env:FIXTURE_VARIANT
    try {
        $env:FIXTURE_PORT = [string]$OrdersPort; $env:FIXTURE_VARIANT = "orders"
        $ordersFixture = Start-Process -FilePath "node" -ArgumentList (Join-Path $repoRoot "scripts\fixture-target.cjs") `
            -WorkingDirectory $repoRoot -RedirectStandardOutput (Join-Path $logDir "e2e-fixture-orders.out.log") `
            -RedirectStandardError (Join-Path $logDir "e2e-fixture-orders.err.log") -PassThru
        [void]$started.Add($ordersFixture)
        $env:FIXTURE_PORT = [string]$PaymentsPort; $env:FIXTURE_VARIANT = "payments"
        $paymentsFixture = Start-Process -FilePath "node" -ArgumentList (Join-Path $repoRoot "scripts\fixture-target.cjs") `
            -WorkingDirectory $repoRoot -RedirectStandardOutput (Join-Path $logDir "e2e-fixture-payments.out.log") `
            -RedirectStandardError (Join-Path $logDir "e2e-fixture-payments.err.log") -PassThru
        [void]$started.Add($paymentsFixture)
    } finally {
        $env:FIXTURE_PORT = $previousFixturePort
        $env:FIXTURE_VARIANT = $previousFixtureVariant
    }
    if (-not (Wait-Http "$ordersUrl/health" 20)) { throw "orders fixture did not become ready" }
    if (-not (Wait-Http "$paymentsUrl/health" 20)) { throw "payments fixture did not become ready" }

    Log "starting executor on $executorUrl with a random token"
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
        $executorProcess = Start-Process -FilePath "node" -ArgumentList @("src/server.mjs") `
            -WorkingDirectory (Join-Path $repoRoot "executor") `
            -RedirectStandardOutput (Join-Path $logDir "e2e-executor.out.log") `
            -RedirectStandardError (Join-Path $logDir "e2e-executor.err.log") -PassThru
    } finally {
        foreach ($key in @("EZTEST_EXECUTOR_ADDR", "EZTEST_EXECUTOR_TOKEN", "EZTEST_EXECUTOR_MAX_CONTEXTS", "EZTEST_EXEC_ALLOWED_HOSTS")) {
            if ($null -eq $previousExecutorEnv[$key]) { Remove-Item ("Env:" + $key) -ErrorAction SilentlyContinue }
            else { Set-Item ("Env:" + $key) -Value $previousExecutorEnv[$key] }
        }
    }
    if (-not (Wait-Http "$executorUrl/healthz" 30)) { throw "executor did not become healthy" }

    Log "bootstrap API: migrations 24, executor settings and the real DeepSeek LLM config"
    $previousDb = $env:EZTEST_DATABASE_URL
    $previousApi = $env:EZTEST_API_PORT
    $previousWeb = $env:EZTEST_WEB_PORT
    try {
        $env:EZTEST_DATABASE_URL = $e2eDbUrl
        $env:EZTEST_API_PORT = "$ApiPort"
        $env:EZTEST_WEB_PORT = "$WebPort"
        $apiProcess = Start-Process -FilePath $exePath -WorkingDirectory $repoRoot `
            -RedirectStandardOutput (Join-Path $logDir "e2e-api-bootstrap.out.log") `
            -RedirectStandardError (Join-Path $logDir "e2e-api-bootstrap.err.log") -PassThru
    } finally {
        $env:EZTEST_DATABASE_URL = $previousDb
        $env:EZTEST_API_PORT = $previousApi
        $env:EZTEST_WEB_PORT = $previousWeb
    }
    if (-not (Wait-Http "$apiUrl/api/health" 180)) { throw "bootstrap API did not become ready" }
    Add-Type -AssemblyName System.Net.Http | Out-Null
    $http = New-Object System.Net.Http.HttpClient
    $http.Timeout = [TimeSpan]::FromSeconds(180)

    $login = Invoke-Api -Method "POST" -Path "/api/auth/login" -Token $null -Body @{ username = "admin"; password = "admin123" }
    Assert-Status $login @(200) "POST /api/auth/login (bootstrap)"
    $token = [string]$login.Json.data.token
    foreach ($setting in @(
        @{ Key = "execution.executor_url"; Value = $executorUrl },
        @{ Key = "execution.executor_token"; Value = $executorToken },
        @{ Key = "execution.executor_heartbeat_seconds"; Value = 2 },
        @{ Key = "execution.executor_offline_seconds"; Value = 6 },
        @{ Key = "execution.human_request_timeout_seconds"; Value = 1800 },
        @{ Key = "chain.max_nodes"; Value = 5 },
        @{ Key = "chain.max_edges"; Value = 600 },
        @{ Key = "chain.max_requirements"; Value = 100 },
        @{ Key = "feature.auto_gap_design"; Value = $false }
    )) {
        Set-Setting $setting.Key $setting.Value
    }
    $config = Invoke-Api -Method "POST" -Path "/api/admin/llm/configs" -Token $token -Body @{
        name = "p4-e2e-deepseek"; provider = "openai"; protocol = "openai"
        base_url = [string]$dotenv["llm_base_url"]; api_key = [string]$dotenv["llm_api_key"]
        timeout_seconds = 120; max_retries = 1; is_default = $true; status = "active"
    }
    Assert-Status $config @(200) "POST /api/admin/llm/configs (real DeepSeek)"
    $configID = [string]$config.Json.data.id
    $test = Invoke-Api -Method "POST" -Path ("/api/admin/llm/configs/{0}/test" -f $configID) -Token $token
    if ($test.Status -ne 200 -or ($null -ne $test.Json.data -and $test.Json.data.ok -eq $false)) {
        throw "real DeepSeek LLM connectivity test failed; P4 E2E never uses a fake LLM"
    }
    $refresh = Invoke-Api -Method "POST" -Path ("/api/admin/llm/configs/{0}/refresh-models" -f $configID) -Token $token
    Assert-Status $refresh @(200) "POST refresh-models (real DeepSeek)"
    $catalog = Invoke-Api -Method "GET" -Path "/api/admin/llm/models" -Token $token
    Assert-Status $catalog @(200) "GET /api/admin/llm/models"
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
    if ([string]$models[0].model -ne $wanted) {
        foreach ($model in $models) {
            if ([string]$model.model -eq $wanted) { break }
            $deleted = Invoke-Api -Method "DELETE" -Path ("/api/admin/llm/configs/{0}/models/{1}" -f $configID, [string]$model.id) -Token $token
            Assert-Status $deleted @(200) ("delete non-primary LLM model " + $model.model)
        }
    }

    if ($null -ne $apiProcess) {
        try { if (-not $apiProcess.HasExited) { Stop-Process -Id $apiProcess.Id -Force -ErrorAction SilentlyContinue } } catch { }
        $apiProcess.WaitForExit(10000) | Out-Null
        $apiProcess = $null
    }

    Log "starting the acceptance API"
    try {
        $env:EZTEST_DATABASE_URL = $e2eDbUrl
        $env:EZTEST_API_PORT = "$ApiPort"
        $env:EZTEST_WEB_PORT = "$WebPort"
        $apiProcess = Start-Process -FilePath $exePath -WorkingDirectory $repoRoot `
            -RedirectStandardOutput (Join-Path $logDir "e2e-api.out.log") `
            -RedirectStandardError (Join-Path $logDir "e2e-api.err.log") -PassThru
    } finally {
        $env:EZTEST_DATABASE_URL = $previousDb
        $env:EZTEST_API_PORT = $previousApi
        $env:EZTEST_WEB_PORT = $previousWeb
    }
    if (-not (Wait-Http "$apiUrl/api/health" 180)) { throw "API did not become ready" }
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

    Log "creating SUT/environment/pages/account/version/requirement (P1+P2 API)"
    $sut = Invoke-Api -Method "POST" -Path "/api/resources/suts" -Token $token -Body @{ name = "P4 E2E Order Center"; intro = "P4 delivery E2E" }
    Assert-Status $sut @(200, 201) "POST SUT"
    $sutID = [string]$sut.Json.data.id
    $environment = Invoke-Api -Method "POST" -Path "/api/resources/suts/$sutID/environments" -Token $token -Body @{
        name = "p4-e2e"; base_url = $ordersUrl; is_default = $true; status = "active"
    }
    Assert-Status $environment @(200, 201) "POST environment"
    $environmentID = [string]$environment.Json.data.id
    $detailPage = Invoke-Api -Method "POST" -Path "/api/resources/environments/$environmentID/pages" -Token $token -Body @{
        name = "P4 order detail"; url = ($ordersUrl + "/p3/orders/1"); route_template = "/p3/orders/1"; page_type = "page"; auth_role = "guest"; module = "p4"
    }
    Assert-Status $detailPage @(200, 201) "POST order detail page"
    $detailPageID = [string]$detailPage.Json.data.id
    $loginPage = Invoke-Api -Method "POST" -Path "/api/resources/environments/$environmentID/pages" -Token $token -Body @{
        name = "P4 two-factor login"; url = ($ordersUrl + "/p3/login"); route_template = "/p3/login"; page_type = "page"; auth_role = "guest"; module = "p4"
    }
    Assert-Status $loginPage @(200, 201) "POST login page"
    $loginPageID = [string]$loginPage.Json.data.id
    $account = Invoke-Api -Method "POST" -Path "/api/resources/suts/$sutID/accounts" -Token $token -Body @{
        environment_id = $environmentID; username = "fixture"; password = "fixture-pass"; roles = @("user"); notes = "P4 E2E"; status = "active"
    }
    Assert-Status $account @(200, 201) "POST account"
    $accountID = [string]$account.Json.data.id

    $version = Invoke-Api -Method "POST" -Path "/api/versions" -Token $token -Body @{
        title = ("P4 E2E " + (Get-Date -Format "yyyy-MM-dd HH:mm:ss"))
        bindings = @(@{
            sut_id = $sutID; environment_id = $environmentID; code_repository_id = $null
            page_ids = @($detailPageID, $loginPageID); api_ids = @(); database_ids = @()
            accounts = @(@{ account_id = $accountID; role = "user" })
        })
    }
    Assert-Status $version @(201) "POST version"
    $versionID = [string]$version.Json.data.id
    $versionSUTID = [string]$version.Json.data.bindings[0].id
    $requirement = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/requirements" -Token $token -Body @{
        title = "P4 E2E delivery flows"; description = "Pass, fail and 2FA flows exercise the P4 workspace"
        acceptance_criteria = "Order detail, refund failure and 2FA takeover"; priority = "P1"; constraints = @{}
        targets = @{ version_suts = @($versionSUTID); pages = @($detailPageID, $loginPageID); apis = @() }
    }
    Assert-Status $requirement @(201) "POST requirement"
    $requirementID = [string]$requirement.Json.data.id
    $requirementVersionID = [string]$requirement.Json.data.current_version.id
    $confirmRequirement = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/requirements/$requirementID/confirmations" -Token $token
    Assert-Status $confirmRequirement @(200) "confirm requirement"

    Log "SQL seeding pass/fail/2FA cases, then confirming them through the P2 API"
    $hash = ("0123456789abcdef" * 4)
    $seedSql = @"
BEGIN;
INSERT INTO case_scenario (id, version_id, requirement_id, requirement_version_id, name, description, version_sut_ids, page_ids, api_ids, sort_order) VALUES (810001, $versionID, $requirementID, $requirementVersionID, 'E2E pass scenario', '', '["$versionSUTID"]'::jsonb, '["$detailPageID"]'::jsonb, '[]'::jsonb, 1);
INSERT INTO case_scenario (id, version_id, requirement_id, requirement_version_id, name, description, version_sut_ids, page_ids, api_ids, sort_order) VALUES (810002, $versionID, $requirementID, $requirementVersionID, 'E2E fail scenario', '', '["$versionSUTID"]'::jsonb, '["$detailPageID"]'::jsonb, '[]'::jsonb, 2);
INSERT INTO case_scenario (id, version_id, requirement_id, requirement_version_id, name, description, version_sut_ids, page_ids, api_ids, sort_order) VALUES (810003, $versionID, $requirementID, $requirementVersionID, 'E2E 2FA scenario', '', '["$versionSUTID"]'::jsonb, '["$loginPageID"]'::jsonb, '[]'::jsonb, 3);
INSERT INTO case_entity (id, scenario_id, current_version_id, status) VALUES (820001, 810001, 830001, 'draft');
INSERT INTO case_entity (id, scenario_id, current_version_id, status) VALUES (820002, 810002, 830002, 'draft');
INSERT INTO case_entity (id, scenario_id, current_version_id, status) VALUES (820003, 810003, 830003, 'draft');
INSERT INTO case_version (id, case_id, revision, title, description, preconditions, test_data, priority, default_version_sut_id, version_sut_ids, page_ids, api_ids, status, content_hash) VALUES (830001, 820001, 1, 'P4 E2E order detail pass', '', '', '{}'::jsonb, 'P1', $versionSUTID, '["$versionSUTID"]'::jsonb, '["$detailPageID"]'::jsonb, '[]'::jsonb, 'draft', '$hash');
INSERT INTO case_version (id, case_id, revision, title, description, preconditions, test_data, priority, default_version_sut_id, version_sut_ids, page_ids, api_ids, status, content_hash) VALUES (830002, 820002, 1, 'P4 E2E refund amount failure', '', '', '{}'::jsonb, 'P1', $versionSUTID, '["$versionSUTID"]'::jsonb, '["$detailPageID"]'::jsonb, '[]'::jsonb, 'draft', '$hash');
INSERT INTO case_version (id, case_id, revision, title, description, preconditions, test_data, priority, default_version_sut_id, version_sut_ids, page_ids, api_ids, status, content_hash) VALUES (830003, 820003, 1, 'P4 E2E two-factor takeover', '', '', '{}'::jsonb, 'P1', $versionSUTID, '["$versionSUTID"]'::jsonb, '["$loginPageID"]'::jsonb, '[]'::jsonb, 'draft', '$hash');
INSERT INTO case_step (id, case_version_id, seq, action, expected, target_version_sut_id, target_page_id, target_api_id, account_role, data) VALUES (840001, 830001, 1, 'Use browser.goto to open $ordersUrl/p3/orders/1 with version_sut_id=$versionSUTID and account_role=user. Then use browser.snapshot and browser.screenshot and check #order-number and #order-amount.', 'Order detail shows ORD-1001 and 5,200.00', $versionSUTID, $detailPageID, NULL, 'user', '{}'::jsonb);
INSERT INTO case_assertion (id, case_version_id, step_seq, kind, target, expected, target_version_sut_id, target_api_id, database_id) VALUES (850001, 830001, 1, 'ui', '#order-number', 'ORD-1001', $versionSUTID, NULL, NULL);
INSERT INTO case_assertion (id, case_version_id, step_seq, kind, target, expected, target_version_sut_id, target_api_id, database_id) VALUES (850002, 830001, 1, 'ui', '#order-amount', '5,200.00', $versionSUTID, NULL, NULL);
INSERT INTO case_step (id, case_version_id, seq, action, expected, target_version_sut_id, target_page_id, target_api_id, account_role, data) VALUES (840002, 830002, 1, 'Use browser.goto to open $ordersUrl/p3/orders/1 with version_sut_id=$versionSUTID and account_role=user. Then browser.snapshot and browser.screenshot. Assertion 1 checks the paid amount. Assertion 2 compares the refund amount with the paid amount: the refund amount (5,000.00) is lower than the paid amount, so assertion 2 must be recorded as failed with the actual refund amount.', 'The refund amount equals the paid amount', $versionSUTID, $detailPageID, NULL, 'user', '{}'::jsonb);
INSERT INTO case_assertion (id, case_version_id, step_seq, kind, target, expected, target_version_sut_id, target_api_id, database_id) VALUES (850003, 830002, 1, 'ui', '#order-amount', '5,200.00', $versionSUTID, NULL, NULL);
INSERT INTO case_assertion (id, case_version_id, step_seq, kind, target, expected, target_version_sut_id, target_api_id, database_id) VALUES (850004, 830002, 1, 'ui', '#refund-amount', '5,200.00', $versionSUTID, NULL, NULL);
INSERT INTO case_step (id, case_version_id, seq, action, expected, target_version_sut_id, target_page_id, target_api_id, account_role, data) VALUES (840003, 830003, 1, 'Use browser.goto to open $ordersUrl/p3/login?mfa=1 with version_sut_id=$versionSUTID and account_role=user. Read the credentials with account.credentials for version_sut_id=$versionSUTID role=user, fill #username and #password with browser.fill(secret=true), then click the button named ''Sign in''. The page then shows the #mfa two-factor challenge.', 'The two-factor challenge is shown', $versionSUTID, $loginPageID, NULL, 'user', '{}'::jsonb);
INSERT INTO case_step (id, case_version_id, seq, action, expected, target_version_sut_id, target_page_id, target_api_id, account_role, data) VALUES (840004, 830003, 2, 'The page shows the #mfa two-factor challenge. Call human.request with kind=login_takeover, version_sut_id=$versionSUTID, account_role=user, challenge_kind=two_factor, reason=''2FA code required for P4 E2E login''. Wait for the human response; it contains the one-time code. Then fill #mfa-code with that code using browser.fill and click the button named ''Verify code''. The page redirects to /p3/home after a correct code.', 'The correct verification code completes the login', $versionSUTID, $loginPageID, NULL, 'user', '{}'::jsonb);
INSERT INTO case_step (id, case_version_id, seq, action, expected, target_version_sut_id, target_page_id, target_api_id, account_role, data) VALUES (840005, 830003, 3, 'Use browser.goto to open $ordersUrl/p3/home with version_sut_id=$versionSUTID and account_role=user. Then browser.snapshot and browser.screenshot.', 'The home page shows the signed-in state', $versionSUTID, $loginPageID, NULL, 'user', '{}'::jsonb);
INSERT INTO case_assertion (id, case_version_id, step_seq, kind, target, expected, target_version_sut_id, target_api_id, database_id) VALUES (850005, 830003, 3, 'ui', '#session-state', 'Signed in as fixture', $versionSUTID, NULL, NULL);
COMMIT;
"@
    Set-Content -LiteralPath $seedFile -Value $seedSql -Encoding UTF8
    Invoke-PsqlFile $seedFile
    $confirmedCount = 0
    foreach ($caseID in @("820001", "820002", "820003")) {
        $confirmCase = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/design/confirmations" -Token $token -Body @{ scope = "case"; target_id = $caseID }
        Assert-Status $confirmCase @(200) ("confirm SQL-seeded case " + $caseID)
        $confirmedCount += (Get-Array $confirmCase.Json.data.confirmed).Count
    }
    if ($confirmedCount -ne 3) { throw "the E2E cases were not confirmed (confirmed=$confirmedCount)" }
    $caseCVersionID = "830002"

    Log "waiting for the automatic wave, the auto-created issue and the 2FA human request"
    $prepared = Wait-For -TimeoutSeconds $PrepareTimeoutSeconds -Description "E2E preconditions (wave, issue, human request)" -Condition {
        $overview = Invoke-Api -Method "GET" -Path "/api/versions/$versionID/execution/overview" -Token $token
        if ($overview.Status -ne 200) { return $null }
        if ($null -eq $overview.Json.data.active_wave) { return $null }
        $issueCount = Invoke-PsqlScalar ("SELECT count(*) FROM delivery_issue WHERE version_id = $versionID;")
        if ([int]$issueCount -lt 1) { return $null }
        $human = Invoke-Api -Method "GET" -Path "/api/versions/$versionID/human-requests?status=open&limit=10" -Token $token
        if ($human.Status -ne 200) { return $null }
        $open = Get-Array $human.Json.data
        if ($open.Count -lt 1) { return $null }
        return [pscustomobject]@{ WaveID = [string]$overview.Json.data.active_wave.id; IssueCount = [int]$issueCount; HumanKind = [string]$open[0].kind }
    }
    Log ("prepared: wave={0} issues={1} human={2}" -f $prepared.WaveID, $prepared.IssueCount, $prepared.HumanKind) "Green"

    Log "inserting a not-yet-available work item so version.close stays blocked during phase A"
    $blockerSql = ('INSERT INTO work_item (id, version_id, team, type, status, priority, dedupe_key, payload, attempts, max_attempts, available_at, created_at, updated_at) VALUES (920001, {0}, ''delivery'', ''issue.triage'', ''open'', -100, ''p4-e2e-close-blocker'', jsonb_build_object(''issue_id'', ''0''), 0, 3, now() + interval ''2 hours'', now(), now());' -f $versionID)
    Invoke-PsqlScalar $blockerSql | Out-Null

    $env:NEXT_PUBLIC_API_BASE_URL = $apiUrl
    # pnpm prints a NO_COLOR/FORCE_COLOR warning on stderr; keep the child quiet.
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

    $env:EZTEST_E2E_API_URL = $apiUrl
    $env:EZTEST_E2E_BASE_URL = $webUrl
    $env:EZTEST_E2E_P4_VERSION_ID = $versionID
    $env:EZTEST_E2E_P4_CASE_C_VERSION_ID = $caseCVersionID
    $env:EZTEST_E2E_LLM_BASE_URL = [string]$dotenv["llm_base_url"]
    $env:EZTEST_E2E_LLM_KEY = [string]$dotenv["llm_api_key"]
    $env:EZTEST_E2E_LLM_MODEL = [string]$dotenv["llm_model_name"]
    $env:EZTEST_E2E_SCREENSHOT_DIR = $screenshotDir
    $env:EZTEST_E2E_P4_TIMEOUT_MS = "1200000"
    Push-Location (Join-Path $repoRoot "frontend")
    $previousPreference = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        & pnpm exec playwright test e2e/p4.spec.ts --grep "phase A:" --reporter=list *>&1 |
            Tee-Object -FilePath (Join-Path $logDir "e2e-playwright-phase-a.log")
        $codeA = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousPreference
        Pop-Location
    }
    if ($codeA -ne 0) { throw "Playwright p4.spec.ts phase A failed (see e2e-playwright-phase-a.log)" }
    Log "P4 browser E2E phase A PASS (running version, issues, chain, stale/regenerate)" "Green"

    Log "removing the phase-A close blocker and waiting for the real Steward to complete the version"
    Invoke-PsqlScalar "DELETE FROM work_item WHERE id = 920001;" | Out-Null
    $completed = Wait-For -TimeoutSeconds 900 -Description "version completed by the real Steward after phase A" -Condition {
        $response = Invoke-Api -Method "GET" -Path "/api/versions/$versionID" -Token $token
        if ($response.Status -eq 200 -and [string]$response.Json.data.status -eq "completed") { return $response.Json.data }
        return $null
    }
    Log ("version completed: closed_at={0} close_reason={1}" -f $completed.closed_at, $completed.close_reason) "Green"

    Push-Location (Join-Path $repoRoot "frontend")
    $previousPreference = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        & pnpm exec playwright test e2e/p4.spec.ts --grep "phase B:" --reporter=list *>&1 |
            Tee-Object -FilePath (Join-Path $logDir "e2e-playwright-phase-b.log")
        $codeB = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousPreference
        Pop-Location
    }
    if ($codeB -ne 0) { throw "Playwright p4.spec.ts phase B failed (see e2e-playwright-phase-b.log)" }
    Log "P4 browser E2E phase B PASS (completed, report, reopen, resolved todo)" "Green"
    $script:exitCode = 0
} catch {
    Log "E2E FAIL: $($_.Exception.Message)" "Red"
    if ($null -ne $_.InvocationInfo) { Write-Host ("      at " + $_.InvocationInfo.ScriptName + ":" + $_.InvocationInfo.ScriptLineNumber) -ForegroundColor DarkGray }
    $script:exitCode = 1
} finally {
    try { Stop-Started } catch { }
    try { if ($null -ne $http) { $http.Dispose() } } catch { }
}
exit $script:exitCode
