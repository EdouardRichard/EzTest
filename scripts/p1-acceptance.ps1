#requires -Version 5.1
<#
.SYNOPSIS
    P1 API acceptance runner (no browser) for EzTest 0.2.

.DESCRIPTION
    Starts a completely isolated stack:
      * database   : eztest_v02_e2e (dropped and recreated on every run)
      * fake LLM   : scripts/fake-llm.cjs on 127.0.0.1:13334
      * API        : freshly built .tmp/p1/eztest.exe on 127.0.0.1:14333
    Then exercises the P1 API journey against admin/admin123:
      LLM config -> fake model catalog -> SUT/env/page/API -> version ->
      clarification conversation -> user message + MD attachment -> AI draft ->
      single confirm -> batch confirm -> rollback -> team room read ->
      attachment extraction -> SSE snapshot event assertions.

    The script never fakes a pass. If a P1 endpoint is not registered yet it
    prints BLOCKED with the exact method/path list and a T13 wiring TODO, and
    exits with code 2. Unexpected behavior exits with code 1. A full green run
    exits with code 0 and writes evidence to .tmp/p1-acceptance/api.log.

.NOTES
    Windows PowerShell 5.1+ compatible. No extra PowerShell modules required.
#>
[CmdletBinding()]
param(
    [int]$ApiPort = 14333,
    [int]$FakeLLMPort = 13334,
    [int]$PollTimeoutSeconds = 120,
    [switch]$SkipBuild,
    [switch]$KeepServices
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$logDir = Join-Path $repoRoot ".tmp\p1-acceptance"
$logFile = Join-Path $logDir "api.log"
$exePath = Join-Path $repoRoot ".tmp\p1\eztest.exe"
$fixtureDir = Join-Path $logDir "fixtures"
$attachmentPath = Join-Path $fixtureDir "login-spec.md"
$testDatabase = "eztest_v02_e2e"
$e2eDatabaseName = $testDatabase

New-Item -ItemType Directory -Force -Path $logDir | Out-Null
New-Item -ItemType Directory -Force -Path $fixtureDir | Out-Null
New-Item -ItemType Directory -Force -Path (Split-Path -Parent $exePath) | Out-Null
Set-Content -LiteralPath $logFile -Value "" -Encoding UTF8

$script:failed = $false
$script:blocked = $false
$script:completedSteps = 0
$script:apiBase = "http://127.0.0.1:$ApiPort"
$script:fakeBase = "http://127.0.0.1:$FakeLLMPort"
$script:apiProcess = $null
$script:fakeProcess = $null
$script:http = $null
$script:psql = $null
$script:db = $null

function Write-Log {
    param(
        [string]$Message,
        [string]$Color = "Gray"
    )
    $line = "[{0}] {1}" -f (Get-Date -Format "yyyy-MM-dd HH:mm:ss"), $Message
    Write-Host $line -ForegroundColor $Color
    Add-Content -LiteralPath $logFile -Value $line -Encoding UTF8
}

function Step {
    param([string]$Name)
    Write-Log ("STEP  " + $Name) "Cyan"
}

function StepPassed {
    param([string]$Name)
    $script:completedSteps++
    Write-Log ("PASS  " + $Name) "Green"
}

function Test-PortInUse {
    param([int]$Port)
    $client = New-Object System.Net.Sockets.TcpClient
    try {
        $client.Connect("127.0.0.1", $Port)
        return $true
    } catch {
        return $false
    } finally {
        $client.Close()
    }
}

function Read-DotEnv {
    $envFile = Join-Path $repoRoot ".env"
    if (-not (Test-Path -LiteralPath $envFile)) {
        throw "Missing .env at $envFile (copy .env.example first)"
    }
    $values = @{}
    foreach ($line in Get-Content -LiteralPath $envFile) {
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

function ConvertFrom-DatabaseUrl {
    param([string]$RawUrl)
    $pattern = '^(?<scheme>postgres(?:ql)?)://(?<user>[^:/@]+)(?::(?<password>[^@]*))?@(?<host>[^:/?]+)(?::(?<port>\d+))?/(?<database>[^?]+)(?<query>\?.*)?$'
    $match = [regex]::Match($RawUrl, $pattern)
    if (-not $match.Success) {
        throw "EZTEST_DATABASE_URL is not a postgres URL with credentials: $RawUrl"
    }
    $port = 5432
    if ($match.Groups['port'].Success) { $port = [int]$match.Groups['port'].Value }
    return [pscustomobject]@{
        Scheme   = $match.Groups['scheme'].Value
        User     = [System.Uri]::UnescapeDataString($match.Groups['user'].Value)
        Password = [System.Uri]::UnescapeDataString($match.Groups['password'].Value)
        Host     = $match.Groups['host'].Value
        Port     = $port
        Database = $match.Groups['database'].Value
        Query    = $match.Groups['query'].Value
    }
}

function Get-TestDatabaseUrl {
    param($Database)
    $query = $Database.Query
    if ([string]::IsNullOrEmpty($query)) {
        $query = "?sslmode=disable"
    }
    return "{0}://{1}:{2}@{3}:{4}/{5}{6}" -f $Database.Scheme, [System.Uri]::EscapeDataString($Database.User),
        [System.Uri]::EscapeDataString($Database.Password), $Database.Host, $Database.Port, $testDatabase, $query
}

function Resolve-Psql {
    $candidates = @(
        (Join-Path $repoRoot ".tools\pgsql\bin\psql.exe"),
        "psql"
    )
    foreach ($candidate in $candidates) {
        if ($candidate -eq "psql") {
            $command = Get-Command psql -ErrorAction SilentlyContinue
            if ($null -ne $command) { return $command.Source }
            continue
        }
        if (Test-Path -LiteralPath $candidate) { return $candidate }
    }
    throw "psql not found (.tools/pgsql/bin/psql.exe or PATH)"
}

function Invoke-Psql {
    param(
        [string]$DatabaseName,
        [string]$SqlText
    )
    $previousPassword = $env:PGPASSWORD
    $previousPreference = $ErrorActionPreference
    try {
        $env:PGPASSWORD = $script:db.Password
        $ErrorActionPreference = "Continue"
        $output = & $script:psql -h $script:db.Host -p $script:db.Port -U $script:db.User `
            -d $DatabaseName -v ON_ERROR_STOP=1 -tAc $SqlText 2>&1
        $exit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousPreference
        $env:PGPASSWORD = $previousPassword
    }
    if ($exit -ne 0) {
        throw ("psql failed ({0}): {1}" -f $exit, ($output -join " "))
    }
    return $output
}

function Reset-TestDatabase {
    $previousPassword = $env:PGPASSWORD
    $previousPreference = $ErrorActionPreference
    try {
        $env:PGPASSWORD = $script:db.Password
        $ErrorActionPreference = "Continue"
        $drop = "DROP DATABASE IF EXISTS $testDatabase WITH (FORCE);"
        $create = "CREATE DATABASE $testDatabase;"
        $output = & $script:psql -h $script:db.Host -p $script:db.Port -U $script:db.User -d postgres `
            -v ON_ERROR_STOP=1 -c $drop -c $create 2>&1
        $exit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousPreference
        $env:PGPASSWORD = $previousPassword
    }
    if ($exit -ne 0) {
        throw ("cannot reset {0}: {1}" -f $testDatabase, ($output -join " "))
    }
}

function Resolve-Go {
    $portable = Join-Path $repoRoot ".tools\go\bin\go.exe"
    if (Test-Path -LiteralPath $portable) { return $portable }
    $command = Get-Command go -ErrorAction SilentlyContinue
    if ($null -eq $command) { throw "go toolchain not found" }
    return $command.Source
}

function Build-Api {
    if ($SkipBuild -and (Test-Path -LiteralPath $exePath)) {
        Write-Log "Using existing $exePath (-SkipBuild)"
        return
    }
    $go = Resolve-Go
    Push-Location (Join-Path $repoRoot "backend")
    $previousPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = "Continue"
        $output = & $go build -o $exePath ./cmd/eztest 2>&1
        $exit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousPreference
        Pop-Location
    }
    if ($exit -ne 0) {
        throw ("go build ./cmd/eztest failed: {0}" -f ($output -join " "))
    }
    if (-not (Test-Path -LiteralPath $exePath)) {
        throw "go build did not produce $exePath"
    }
}

function New-ApiHttpClient {
    Add-Type -AssemblyName System.Net.Http | Out-Null
    $client = New-Object System.Net.Http.HttpClient
    $client.Timeout = [TimeSpan]::FromSeconds(60)
    return $client
}

function Invoke-Api {
    param(
        [string]$Method,
        [string]$Path,
        $Body,
        [string]$Token
    )
    $uri = $script:apiBase + $Path
    $request = New-Object System.Net.Http.HttpRequestMessage ((New-Object System.Net.Http.HttpMethod($Method)), $uri)
    try {
        if (-not [string]::IsNullOrEmpty($Token)) {
            $request.Headers.Authorization = New-Object System.Net.Http.Headers.AuthenticationHeaderValue("Bearer", $Token)
        }
        if ($null -ne $Body) {
            $json = ConvertTo-Json -InputObject $Body -Depth 30 -Compress
            $request.Content = New-Object System.Net.Http.StringContent($json, [System.Text.Encoding]::UTF8, "application/json")
        }
        $response = $script:http.SendAsync($request).GetAwaiter().GetResult()
        try {
            $raw = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
            $parsed = $null
            if (-not [string]::IsNullOrWhiteSpace($raw)) {
                try { $parsed = ConvertFrom-Json $raw } catch { $parsed = $null }
            }
            $snapshotCursor = $null
            $headerValues = $null
            if ($response.Headers.TryGetValues("X-Snapshot-Cursor", [ref]$headerValues)) {
                $snapshotCursor = (@($headerValues) | Select-Object -First 1)
            }
            return [pscustomobject]@{
                Status         = [int]$response.StatusCode
                Raw            = $raw
                Json           = $parsed
                SnapshotCursor = $snapshotCursor
            }
        } finally {
            $response.Dispose()
        }
    } finally {
        $request.Dispose()
    }
}

function Invoke-MultipartApi {
    param(
        [string]$Path,
        [string]$FilePath,
        [string]$MimeType,
        [string]$Token,
        [string]$FieldName = "file"
    )
    $uri = $script:apiBase + $Path
    $request = New-Object System.Net.Http.HttpRequestMessage ((New-Object System.Net.Http.HttpMethod("POST")), $uri)
    $stream = $null
    try {
        if (-not [string]::IsNullOrEmpty($Token)) {
            $request.Headers.Authorization = New-Object System.Net.Http.Headers.AuthenticationHeaderValue("Bearer", $Token)
        }
        $form = New-Object System.Net.Http.MultipartFormDataContent
        $stream = [System.IO.File]::OpenRead($FilePath)
        $fileContent = New-Object System.Net.Http.StreamContent($stream)
        $fileContent.Headers.ContentType = [System.Net.Http.Headers.MediaTypeHeaderValue]::Parse($MimeType)
        [void]$form.Add($fileContent, $FieldName, [System.IO.Path]::GetFileName($FilePath))
        $request.Content = $form
        $response = $script:http.SendAsync($request).GetAwaiter().GetResult()
        try {
            $raw = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
            $parsed = $null
            if (-not [string]::IsNullOrWhiteSpace($raw)) {
                try { $parsed = ConvertFrom-Json $raw } catch { $parsed = $null }
            }
            return [pscustomobject]@{ Status = [int]$response.StatusCode; Raw = $raw; Json = $parsed }
        } finally {
            $response.Dispose()
        }
    } finally {
        if ($null -ne $stream) { $stream.Dispose() }
        $request.Dispose()
    }
}

function Assert-Status {
    param(
        $Response,
        [int[]]$Expected,
        [string]$What
    )
    if ($Expected -notcontains $Response.Status) {
        $detail = $Response.Raw
        if ($null -ne $Response.Json -and $null -ne $Response.Json.error) {
            $detail = ("{0}: {1}" -f $Response.Json.error.code, $Response.Json.error.message)
        }
        throw ("{0}: unexpected HTTP {1} ({2})" -f $What, $Response.Status, $detail)
    }
}

function Wait-For {
    param(
        [scriptblock]$Condition,
        [int]$TimeoutSeconds,
        [string]$Description
    )
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        $value = & $Condition
        if ($null -ne $value) { return $value }
        Start-Sleep -Milliseconds 500
    }
    throw ("timeout after {0}s waiting for {1}" -f $TimeoutSeconds, $Description)
}

function Get-Array {
    param($Value)
    if ($null -eq $Value) { return @() }
    # The comma operator keeps a single-element JSON array as an array; a plain
    # `return @($Value)` would be unrolled to the object and `.Count` is empty.
    return ,@($Value)
}

function Test-FakeLlmHealth {
    $request = New-Object System.Net.Http.HttpRequestMessage ((New-Object System.Net.Http.HttpMethod("GET")), ($script:fakeBase + "/health"))
    try {
        $response = $script:http.SendAsync($request).GetAwaiter().GetResult()
        try {
            return ([int]$response.StatusCode -eq 200)
        } finally {
            $response.Dispose()
        }
    } catch {
        return $false
    } finally {
        $request.Dispose()
    }
}

function Start-FakeLlm {
    if (Test-PortInUse -Port $FakeLLMPort) {
        throw "port $FakeLLMPort is already in use; stop the stale fake LLM first"
    }
    $node = Get-Command node -ErrorAction SilentlyContinue
    if ($null -eq $node) { throw "node not found on PATH" }
    $previousPort = $env:FAKE_LLM_PORT
    try {
        $env:FAKE_LLM_PORT = [string]$FakeLLMPort
        $script:fakeProcess = Start-Process -FilePath $node.Source `
            -ArgumentList @((Join-Path $repoRoot "scripts\fake-llm.cjs")) `
            -WorkingDirectory $repoRoot `
            -RedirectStandardOutput (Join-Path $logDir "fake-llm.out.log") `
            -RedirectStandardError (Join-Path $logDir "fake-llm.err.log") -PassThru
    } finally {
        $env:FAKE_LLM_PORT = $previousPort
    }
    Wait-For -TimeoutSeconds 20 -Description "fake LLM health" -Condition {
        if (Test-FakeLlmHealth) { return $true }
        return $null
    } | Out-Null
}

function Start-Api {
    if (Test-PortInUse -Port $ApiPort) {
        throw "port $ApiPort is already in use; stop the stale API first"
    }
    $previousDb = $env:EZTEST_DATABASE_URL
    $previousApiPort = $env:EZTEST_API_PORT
    try {
        $env:EZTEST_DATABASE_URL = $script:testUrl
        $env:EZTEST_API_PORT = [string]$ApiPort
        $script:apiProcess = Start-Process -FilePath $exePath -WorkingDirectory $repoRoot `
            -RedirectStandardOutput (Join-Path $logDir "api.stdout.log") `
            -RedirectStandardError (Join-Path $logDir "api.stderr.log") -PassThru
    } finally {
        $env:EZTEST_DATABASE_URL = $previousDb
        $env:EZTEST_API_PORT = $previousApiPort
    }
    Wait-For -TimeoutSeconds 60 -Description "API health" -Condition {
        try {
            $probe = Invoke-Api -Method "GET" -Path "/api/health" -Body $null -Token $null
            if ($probe.Status -eq 200 -and $null -ne $probe.Json -and $probe.Json.data.db -eq "ok") {
                Write-Log ("      API version " + $probe.Json.data.version + " ready")
                return $true
            }
        } catch { }
        if ($null -ne $script:apiProcess -and $script:apiProcess.HasExited) {
            throw ("API exited before becoming healthy; see {0}" -f (Join-Path $logDir "api.stderr.log"))
        }
        return $null
    } | Out-Null
}

function Stop-Services {
    foreach ($process in @($script:apiProcess, $script:fakeProcess)) {
        if ($null -ne $process) {
            try {
                if (-not $process.HasExited) { Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue }
            } catch { }
        }
    }
}

function Get-RequiredP1Routes {
    param([string]$VersionID)
    return @(
        @{ Method = "GET";  Path = "/api/versions/$VersionID/conversations";                 Label = "list conversations" },
        @{ Method = "POST"; Path = "/api/versions/$VersionID/conversations";                 Label = "create conversation" },
        @{ Method = "GET";  Path = "/api/versions/$VersionID/conversations/1";               Label = "get conversation" },
        @{ Method = "GET";  Path = "/api/versions/$VersionID/conversations/1/messages";      Label = "list messages" },
        @{ Method = "POST"; Path = "/api/versions/$VersionID/conversations/1/messages";      Label = "post message" },
        @{ Method = "GET";  Path = "/api/versions/$VersionID/requirements";                  Label = "list requirements" },
        @{ Method = "POST"; Path = "/api/versions/$VersionID/requirements";                  Label = "create requirement" },
        @{ Method = "POST"; Path = "/api/versions/$VersionID/requirements/confirmations";     Label = "batch confirm requirements" },
        @{ Method = "POST"; Path = "/api/versions/$VersionID/requirements/1/confirmations";  Label = "confirm requirement" },
        @{ Method = "POST"; Path = "/api/versions/$VersionID/requirements/1/rollback";       Label = "rollback requirement" },
        @{ Method = "GET";  Path = "/api/versions/$VersionID/team-rooms/clarification";      Label = "read team room" }
    )
}

function Find-MissingRoutes {
    param([string]$VersionID)
    $missing = New-Object System.Collections.ArrayList
    foreach ($route in Get-RequiredP1Routes -VersionID $VersionID) {
        # Without a token an authenticated route answers 401, an unregistered
        # path answers 404 from the ServeMux. A 405 also proves registration.
        $probe = Invoke-Api -Method $route.Method -Path $route.Path -Body ($(if ($route.Method -eq "POST") { @{} } else { $null })) -Token $null
        if ($probe.Status -eq 404) {
            [void]$missing.Add(("{0} {1}  ({2})" -f $route.Method, $route.Path, $route.Label))
        }
    }
    return $missing
}

function Get-EventTypes {
    param($Snapshot)
    $types = @()
    foreach ($event in (Get-Array $Snapshot.Json.data)) { $types += [string]$event.type }
    return $types
}

function Assert-EventPresent {
    param([string[]]$Types, [string]$Type)
    if (-not ($Types -contains $Type)) {
        throw ("SSE snapshot is missing event type '{0}' (events: {1})" -f $Type, (($Types | Sort-Object -Unique) -join ", "))
    }
}

$exitCode = 0
try {
    # ---------------------------------------------------------------- setup --
    Step "load .env and derive the isolated eztest_v02_e2e database"
    $dotenv = Read-DotEnv
    if ([string]::IsNullOrWhiteSpace($dotenv["EZTEST_DATABASE_URL"])) {
        throw "EZTEST_DATABASE_URL is missing from .env"
    }
    $script:db = ConvertFrom-DatabaseUrl $dotenv["EZTEST_DATABASE_URL"]
    $script:testUrl = Get-TestDatabaseUrl $script:db
    if ($script:testUrl -notmatch "/$testDatabase(\?|$)") {
        throw "derived test database URL is invalid: $script:testUrl"
    }
    $logLine = "      dev db:  {0}/{1}" -f $script:db.Host, $script:db.Database
    Write-Log $logLine
    Write-Log ("      e2e db:  {0}/{1}" -f $script:db.Host, $testDatabase)
    Write-Log ("      api:     {0}" -f $script:apiBase)
    Write-Log ("      fake LLM: http://127.0.0.1:{0}" -f $FakeLLMPort)
    StepPassed "load .env and derive the isolated eztest_v02_e2e database"

    Step "reset the isolated e2e database"
    $script:psql = Resolve-Psql
    Reset-TestDatabase
    StepPassed "reset the isolated e2e database"

    Step "build backend/cmd/eztest"
    Build-Api
    StepPassed "build backend/cmd/eztest"

    $script:http = New-ApiHttpClient

    Step "start deterministic fake LLM on port $FakeLLMPort"
    Start-FakeLlm
    StepPassed "start deterministic fake LLM on port $FakeLLMPort"

    Step "start API on port $ApiPort"
    Start-Api
    StepPassed "start API on port $ApiPort"

    # ------------------------------------------------------------- identity --
    Step "login as admin/admin123"
    $login = Invoke-Api -Method "POST" -Path "/api/auth/login" -Body @{ username = "admin"; password = "admin123" } -Token $null
    Assert-Status $login @(200) "POST /api/auth/login"
    $token = [string]$login.Json.data.token
    if ([string]::IsNullOrWhiteSpace($token)) { throw "login response has no token" }
    StepPassed "login as admin/admin123"

    # ------------------------------------------------------------------ LLM --
    Step "create admin LLM config pointing at the fake LLM and refresh models"
    $config = Invoke-Api -Method "POST" -Path "/api/admin/llm/configs" -Token $token -Body @{
        name           = "p1-acceptance-fake"
        provider       = "openai"
        protocol       = "openai"
        base_url       = "http://127.0.0.1:$FakeLLMPort/v1"
        api_key        = "fake-llm-key"
        timeout_seconds = 30
        max_retries    = 1
        is_default     = $true
        status         = "active"
    }
    Assert-Status $config @(200) "POST /api/admin/llm/configs"
    $configID = [string]$config.Json.data.id
    if ([string]::IsNullOrWhiteSpace($configID)) { throw "LLM config response has no id" }

    $models = Invoke-Api -Method "POST" -Path "/api/admin/llm/configs/$configID/refresh-models" -Token $token -Body $null
    Assert-Status $models @(200) "POST /api/admin/llm/configs/$configID/refresh-models"
    $modelList = Get-Array $models.Json.data
    if ($modelList.Count -lt 1) { throw "fake LLM model catalog is empty after refresh-models" }
    $modelNames = @($modelList | ForEach-Object { [string]$_.model })
    if (-not ($modelNames -contains "fake-llm")) {
        throw ("fake LLM model catalog has no 'fake-llm' model (got: {0})" -f ($modelNames -join ", "))
    }
    Write-Log ("      models: " + ($modelNames -join ", "))
    StepPassed "create admin LLM config pointing at the fake LLM and refresh models"

    # -------------------------------------------------------------- catalog --
    Step "create SUT/execution environment/page/API"
    $sut = Invoke-Api -Method "POST" -Path "/api/resources/suts" -Token $token -Body @{
        name  = "P1 Acceptance SUT"
        intro = "Created by scripts/p1-acceptance.ps1"
    }
    Assert-Status $sut @(200, 201) "POST /api/resources/suts"
    $sutID = [string]$sut.Json.data.id

    $environment = Invoke-Api -Method "POST" -Path "/api/resources/suts/$sutID/environments" -Token $token -Body @{
        name       = "acceptance"
        base_url   = "https://acceptance.example.test"
        is_default = $true
        status     = "active"
    }
    Assert-Status $environment @(200, 201) "POST /api/resources/suts/{id}/environments"
    $environmentID = [string]$environment.Json.data.id

    $page = Invoke-Api -Method "POST" -Path "/api/resources/environments/$environmentID/pages" -Token $token -Body @{
        name           = "Login page"
        url            = "https://acceptance.example.test/login"
        route_template = "/login"
        page_type      = "page"
        auth_role      = "guest"
        module         = "auth"
    }
    Assert-Status $page @(200, 201) "POST /api/resources/environments/{id}/pages"
    $pageID = [string]$page.Json.data.id

    $apiResource = Invoke-Api -Method "POST" -Path "/api/resources/environments/$environmentID/apis" -Token $token -Body @{
        method        = "POST"
        path_template = "/api/login"
        name          = "Login API"
        auth_role     = "guest"
        schema_ref    = ""
    }
    Assert-Status $apiResource @(200, 201) "POST /api/resources/environments/{id}/apis"
    $apiID = [string]$apiResource.Json.data.id
    StepPassed "create SUT/execution environment/page/API"

    Step "create a version bound to the acceptance SUT"
    $version = Invoke-Api -Method "POST" -Path "/api/versions" -Token $token -Body @{
        title    = ("P1 acceptance " + (Get-Date -Format "yyyy-MM-dd HH:mm:ss"))
        bindings = @(
            @{
                sut_id             = $sutID
                environment_id     = $environmentID
                code_repository_id = $null
                page_ids           = @($pageID)
                api_ids            = @($apiID)
                database_ids       = @()
                accounts           = @()
            }
        )
    }
    Assert-Status $version @(201) "POST /api/versions"
    $versionID = [string]$version.Json.data.id
    if ([string]::IsNullOrWhiteSpace($versionID)) { throw "version response has no id" }
    if ($version.Json.data.status -ne "initial") {
        throw ("new version status is '{0}', want initial" -f $version.Json.data.status)
    }
    StepPassed "create a version bound to the acceptance SUT"

    # ------------------------------------------------------------ preflight --
    Step "preflight P1 route registration"
    $missingRoutes = Find-MissingRoutes -VersionID $versionID
    if ($missingRoutes.Count -gt 0) {
        Write-Log "      P1 endpoints not registered yet:" "Yellow"
        foreach ($route in $missingRoutes) { Write-Log ("      - " + $route) "Yellow" }
        Write-Log "      TODO (Lead T13 wiring):" "Yellow"
        Write-Log "      - server.go: register ConversationHandler routes (GET/POST conversations, GET detail, GET/POST messages)" "Yellow"
        Write-Log "      - server.go: register agentapi.NewTeamRoom(teamRoom, versionsAdapter) at GET /api/versions/{id}/team-rooms/{team}" "Yellow"
        Write-Log "      - cmd/eztest/main.go: construct ConversationService/AttachmentService/RequirementService, agent work queue/team room/runner/worker/reconciler" "Yellow"
        Write-Log "      - cmd/eztest/main.go: wire Deps.Requirements/Deps.Conversations/Deps.TeamRoom and start the worker/reconciler" "Yellow"
        Write-Log "      - agent versionadapter (backend-runtime) must exist before the worker can run P1 profiles" "Yellow"
        $script:blocked = $true
        throw "P1 API is not fully wired; acceptance stopped before the conversation steps"
    }
    StepPassed "preflight P1 route registration"

    # ------------------------------------------------------- conversation ----
    Step "create clarification conversation"
    $conversation = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/conversations" -Token $token -Body @{
        kind  = "clarification"
        title = "P1 acceptance conversation"
    }
    Assert-Status $conversation @(201) "POST /api/versions/{id}/conversations"
    $conversationID = [string]$conversation.Json.data.id
    if ([string]::IsNullOrWhiteSpace($conversationID)) { throw "conversation response has no id" }
    StepPassed "create clarification conversation"

    Step "upload an MD attachment and post a user message referencing it"
    $markdown = @"
# Login clarification

- Supports password and SMS login.
- Remember-me is optional.
- Error messages must not reveal whether an account exists.
"@
    Set-Content -LiteralPath $attachmentPath -Value $markdown -Encoding UTF8
    $fileInfo = Get-Item -LiteralPath $attachmentPath
    $upload = Invoke-MultipartApi -Path "/api/content" -FilePath $attachmentPath -MimeType "text/markdown" -Token $token
    Assert-Status $upload @(201) "POST /api/content"
    $sha256 = [string]$upload.Json.data.sha256
    $size = [int64]$upload.Json.data.size
    if ($sha256 -notmatch '^[0-9a-f]{64}$' -or $size -le 0) { throw "content upload response is invalid" }

    $clientMessageID = "p1-acceptance-" + [guid]::NewGuid().ToString("N")
    $message = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/conversations/$conversationID/messages" -Token $token -Body @{
        client_message_id = $clientMessageID
        parts             = @(
            @{ type = "text"; text = "Please clarify the login requirement based on the attached login-spec.md." },
            @{ type = "attachment"; sha256 = $sha256; name = "login-spec.md"; mime = "text/markdown"; size = $size }
        )
    }
    Assert-Status $message @(201) "POST /api/versions/{id}/conversations/{cid}/messages"
    if ($message.Json.data.role -ne "user") { throw "posted message role is not user" }
    $userMessageID = [string]$message.Json.data.id
    StepPassed "upload an MD attachment and post a user message referencing it"

    Step "wait for the Host assistant reply and the Curator draft requirement"
    $assistantMessage = Wait-For -TimeoutSeconds $PollTimeoutSeconds -Description "Host assistant reply (agent worker + fake LLM)" -Condition {
        $list = Invoke-Api -Method "GET" -Path "/api/versions/$versionID/conversations/$conversationID/messages?limit=200" -Token $token
        Assert-Status $list @(200) "GET /api/versions/{id}/conversations/{cid}/messages"
        foreach ($item in (Get-Array $list.Json.data)) {
            if ($item.role -eq "assistant") { return $item }
        }
        return $null
    }
    $assistantText = ""
    foreach ($part in (Get-Array $assistantMessage.parts)) {
        if ($part.type -eq "text") { $assistantText += [string]$part.text }
    }
    if ([string]::IsNullOrWhiteSpace($assistantText)) { throw "assistant reply has no text part" }
    Write-Log ("      assistant: " + $assistantText.Substring(0, [Math]::Min(80, $assistantText.Length)))

    $draft = Wait-For -TimeoutSeconds $PollTimeoutSeconds -Description "Curator draft requirement (reconciler + requirement.curate task)" -Condition {
        $list = Invoke-Api -Method "GET" -Path "/api/versions/$versionID/requirements?limit=50" -Token $token
        Assert-Status $list @(200) "GET /api/versions/{id}/requirements"
        foreach ($item in (Get-Array $list.Json.data)) {
            if ($item.status -eq "draft") { return $item }
        }
        return $null
    }
    $aiRequirementID = [string]$draft.id
    $aiRevision = [int]$draft.revision
    if ([string]::IsNullOrWhiteSpace($aiRequirementID) -or $aiRevision -ne 1) {
        throw "AI draft requirement has an unexpected id/revision"
    }
    Write-Log ("      AI draft requirement: id=$aiRequirementID title='" + $draft.title + "'")
    StepPassed "wait for the Host assistant reply and the Curator draft requirement"

    # ------------------------------------------------------------ confirms --
    Step "confirm the AI draft requirement (single)"
    $singleConfirm = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/requirements/$aiRequirementID/confirmations" -Token $token -Body $null
    Assert-Status $singleConfirm @(200) "POST /api/versions/{id}/requirements/{rid}/confirmations"
    if ($singleConfirm.Json.data.confirmed -ne $true) { throw "single confirmation did not confirm the draft" }
    if ($singleConfirm.Json.data.requirement.status -ne "clarified") {
        throw ("single-confirmed requirement status is '{0}', want clarified" -f $singleConfirm.Json.data.requirement.status)
    }
    $confirmedRevision = [int]$singleConfirm.Json.data.requirement.current_version.revision

    $replay = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/requirements/$aiRequirementID/confirmations" -Token $token -Body $null
    Assert-Status $replay @(200) "idempotent POST confirmations"
    if ($replay.Json.data.confirmed -ne $false) { throw "repeated single confirmation is not idempotent" }
    StepPassed "confirm the AI draft requirement (single)"

    Step "create two manual drafts and batch-confirm them"
    $manualIDs = @()
    foreach ($suffix in @("A", "B")) {
        $manual = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/requirements" -Token $token -Body @{
            title               = "Acceptance manual requirement $suffix"
            description         = "Manual draft created by scripts/p1-acceptance.ps1 ($suffix)."
            acceptance_criteria = "Confirmable requirement $suffix."
            priority            = "P2"
            constraints         = @{}
            targets             = @{ version_suts = @(); pages = @(); apis = @() }
        }
        Assert-Status $manual @(201) "POST /api/versions/{id}/requirements"
        $manualIDs += [string]$manual.Json.data.id
    }
    $batch = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/requirements/confirmations" -Token $token -Body @{
        requirement_ids = $manualIDs
    }
    Assert-Status $batch @(200) "POST /api/versions/{id}/requirements/confirmations"
    $batchConfirmed = Get-Array $batch.Json.data.confirmed
    if ($batchConfirmed.Count -ne 2) {
        throw ("batch confirmation confirmed {0} requirements, want 2" -f $batchConfirmed.Count)
    }
    if ([string]::IsNullOrWhiteSpace([string]$batch.Json.data.batch_id)) { throw "batch confirmation has no batch_id" }
    StepPassed "create two manual drafts and batch-confirm them"

    Step "roll the clarified AI requirement back to a new draft revision"
    $rollback = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/requirements/$aiRequirementID/rollback" -Token $token -Body @{
        reason = "P1 acceptance rollback"
    }
    Assert-Status $rollback @(200) "POST /api/versions/{id}/requirements/{rid}/rollback"
    if ($rollback.Json.data.status -ne "draft") {
        throw ("rolled-back requirement status is '{0}', want draft" -f $rollback.Json.data.status)
    }
    $rollbackRevision = [int]$rollback.Json.data.current_version.revision
    if ($rollbackRevision -ne ($confirmedRevision + 1)) {
        throw ("rolled-back revision is {0}, want {1}" -f $rollbackRevision, ($confirmedRevision + 1))
    }
    StepPassed "roll the clarified AI requirement back to a new draft revision"

    Step "assert the version projection moved initial -> running"
    $versionAfter = Invoke-Api -Method "GET" -Path "/api/versions/$versionID" -Token $token -Body $null
    Assert-Status $versionAfter @(200) "GET /api/versions/{id}"
    if ($versionAfter.Json.data.status -ne "running") {
        throw ("version status is '{0}', want running" -f $versionAfter.Json.data.status)
    }
    StepPassed "assert the version projection moved initial -> running"

    # ----------------------------------------------------------- team room --
    Step "read the clarification team room and wait for at least one post"
    $script:lastTeamRoomStatus = 0
    $script:lastTeamRoomRaw = ""
    try {
        $teamRoom = Wait-For -TimeoutSeconds $PollTimeoutSeconds -Description "team-room post (Scout finding from attachment ingest)" -Condition {
            $view = Invoke-Api -Method "GET" -Path "/api/versions/$versionID/team-rooms/clarification?limit=20" -Token $token
            $script:lastTeamRoomStatus = $view.Status
            $script:lastTeamRoomRaw = [string]$view.Raw
            Assert-Status $view @(200) "GET /api/versions/{id}/team-rooms/{team}"
            $team = [string]$view.Json.data.team
            if ($team -ne "clarification") { throw ("team-room team is '{0}', want clarification" -f $team) }
            $posts = Get-Array $view.Json.data.posts
            if ($posts.Count -gt 0) { return $view.Json.data }
            return $null
        }
    } catch {
        $raw = [string]$script:lastTeamRoomRaw
        if ($raw.Length -gt 500) { $raw = $raw.Substring(0, 500) }
        Write-Log ("      team-room last HTTP {0}: {1}" -f $script:lastTeamRoomStatus, $raw) "Red"
        try {
            $dbCount = (@(Invoke-Psql -DatabaseName $testDatabase -SqlText "SELECT count(*) FROM team_room_post WHERE version_id = $versionID;") -join "").Trim()
            Write-Log ("      DB team_room_post count for version {0}: {1}" -f $versionID, $dbCount) "Red"
        } catch {
            Write-Log ("      DB team_room_post probe failed: " + $_.Exception.Message) "Red"
        }
        throw
    }
    $teamPosts = Get-Array $teamRoom.posts
    if ($null -ne $teamRoom.digest) {
        if ([string]::IsNullOrWhiteSpace([string]$teamRoom.digest.summary)) {
            throw "team-room digest is present but has no summary"
        }
    }
    Write-Log ("      team-room posts: {0}" -f $teamPosts.Count)
    StepPassed "read the clarification team room and wait for at least one post"

    # --------------------------------------------------------- attachment ---
    Step "poll MD attachment extraction (DB row + ingest event)"
    $attachmentStatus = Wait-For -TimeoutSeconds $PollTimeoutSeconds -Description "attachment extraction status" -Condition {
        $sql = "SELECT extraction_status || '|' || COALESCE(extraction_error, '') FROM requirement_attachment WHERE version_id = $versionID AND name = 'login-spec.md' ORDER BY id DESC LIMIT 1;"
        $output = Invoke-Psql -DatabaseName $testDatabase -SqlText $sql
        $text = (@($output) -join "").Trim()
        if ([string]::IsNullOrEmpty($text)) { return $null }
        if ($text -like "done|*" -or $text -eq "done") { return "done" }
        if ($text -like "failed|*") { throw ("attachment extraction failed: {0}" -f $text) }
        return $null
    }
    if ($attachmentStatus -ne "done") { throw "attachment extraction did not reach done" }
    Write-Log "      attachment extraction_status=done (requirement_attachment row via psql probe)"

    # ---------------------------------------------------------------- SSE ---
    Step "assert the SSE snapshot contains the P1 event stream"
    $channel = "version:$versionID"
    $snapshot = Invoke-Api -Method "GET" -Path ("/api/events?channel={0}&limit=200" -f [System.Uri]::EscapeDataString($channel)) -Token $token -Body $null
    Assert-Status $snapshot @(200) "GET /api/events"
    if ($null -eq $snapshot.Json -or $null -eq $snapshot.Json.snapshot_cursor) { throw "SSE snapshot has no snapshot_cursor" }
    $cursor = [string]$snapshot.Json.snapshot_cursor
    if ($cursor -notmatch '^[0-9]+$') { throw ("SSE snapshot_cursor is not an integer: {0}" -f $cursor) }
    if ([string]::IsNullOrWhiteSpace([string]$snapshot.SnapshotCursor) -or [string]$snapshot.SnapshotCursor -notmatch '^[0-9]+$') {
        throw ("X-Snapshot-Cursor header is missing or not an integer: {0}" -f $snapshot.SnapshotCursor)
    }
    $events = Get-Array $snapshot.Json.data
    $eventTypes = Get-EventTypes $snapshot
    foreach ($required in @(
        "conversation.message",
        "requirement.created",
        "requirement.clarified",
        "requirement.rolled_back",
        "team_room.post"
    )) {
        Assert-EventPresent -Types $eventTypes -Type $required
    }
    $attachmentEventPresent = ($eventTypes -contains "requirement.attachment_ingested") -or ($eventTypes -contains "requirement.attachment_failed")
    if (-not $attachmentEventPresent) {
        throw ("SSE snapshot is missing requirement.attachment_ingested/failed (events: {0})" -f (($eventTypes | Sort-Object -Unique) -join ", "))
    }
    $conversationEvents = @($events | Where-Object { $_.type -eq "conversation.message" })
    if (-not ($conversationEvents | Where-Object { $_.payload.role -eq "assistant" })) {
        throw "conversation.message snapshot has no assistant message"
    }
    $requirementEvents = @($events | Where-Object { $_.type -like "requirement.*" })
    foreach ($event in $requirementEvents) {
        if ($null -eq $event.scope.version_id) { throw ("requirement event {0} has no scope.version_id" -f $event.type) }
    }
    Write-Log ("      SSE events: " + (($eventTypes | Sort-Object -Unique) -join ", "))
    Write-Log ("      snapshot_cursor: " + $cursor)
    StepPassed "assert the SSE snapshot contains the P1 event stream"

    $exitCode = 0
} catch {
    if ($script:blocked) {
        Write-Log ("BLOCKED  " + $_.Exception.Message) "Yellow"
        $exitCode = 2
    } else {
        $script:failed = $true
        Write-Log ("FAIL  " + $_.Exception.Message) "Red"
        if ($null -ne $_.InvocationInfo) {
            Write-Log ("      at line " + $_.InvocationInfo.ScriptLineNumber) "DarkRed"
        }
        $exitCode = 1
    }
} finally {
    if ($KeepServices) {
        Write-Log "Keeping API/fake LLM processes alive (-KeepServices)" "Yellow"
    } else {
        Stop-Services
    }
    if ($null -ne $script:http) { $script:http.Dispose() }
    if ($exitCode -eq 0) {
        Write-Log ("RESULT: PASS ({0} steps)" -f $script:completedSteps) "Green"
    } elseif ($exitCode -eq 2) {
        Write-Log "RESULT: BLOCKED (P1 API wiring incomplete; no fake pass)" "Yellow"
    } else {
        Write-Log ("RESULT: FAIL ({0} steps completed)" -f $script:completedSteps) "Red"
    }
    Write-Log ("Evidence log: " + $logFile)
    Write-Log ("API stderr:   " + (Join-Path $logDir "api.stderr.log"))
}

exit $exitCode
