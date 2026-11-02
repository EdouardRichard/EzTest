#requires -Version 5.1
<#
.SYNOPSIS
    P2 API acceptance runner (no browser) for EzTest 0.2.

.DESCRIPTION
    Starts a completely isolated stack:
      * database   : eztest_v02_e2e (dropped and recreated on every run)
      * fake LLM   : scripts/fake-llm.cjs on 127.0.0.1:13334
      * API        : freshly built .tmp/p2/eztest.exe on 127.0.0.1:14333
    Then exercises the P2 design API journey against admin/admin123:
      LLM config -> SUT/env/page/API -> version -> clarified requirements ->
      design conversation -> planner/author draft graph -> case/scenario/
      requirement confirmations -> design chat modification -> stale/redesign ->
      auto-join -> design attachment -> 4-format export/download -> SSE.

    The script never fakes a pass. If a P2 endpoint is not registered yet it
    prints BLOCKED with the exact method/path list and exits with code 2.
    Unexpected behavior exits with code 1. A full green run exits with code 0
    and writes evidence to .tmp/p2-acceptance/api.log.

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
$logDir = Join-Path $repoRoot ".tmp\p2-acceptance"
$logFile = Join-Path $logDir "api.log"
$exePath = Join-Path $repoRoot ".tmp\p2\eztest.exe"
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

function Get-RequiredP2Routes {
    param([string]$VersionID)
    return @(
        @{ Method = "GET";  Path = "/api/versions/$VersionID/design/graph";            Label = "design graph" },
        @{ Method = "GET";  Path = "/api/versions/$VersionID/design/overview";         Label = "design overview" },
        @{ Method = "GET";  Path = "/api/versions/$VersionID/design/cases/1";          Label = "design case detail" },
        @{ Method = "POST"; Path = "/api/versions/$VersionID/design/confirmations";    Label = "design confirmation" },
        @{ Method = "POST"; Path = "/api/versions/$VersionID/exports";                 Label = "create export" },
        @{ Method = "GET";  Path = "/api/versions/$VersionID/exports";                 Label = "list exports" },
        @{ Method = "GET";  Path = "/api/exports/1";                                   Label = "get export" },
        @{ Method = "GET";  Path = "/api/exports/1/download";                          Label = "download export" }
    )
}

function Find-MissingP2Routes {
    param([string]$VersionID)
    $missing = New-Object System.Collections.ArrayList
    foreach ($route in Get-RequiredP2Routes -VersionID $VersionID) {
        $probe = Invoke-Api -Method $route.Method -Path $route.Path -Body ($(if ($route.Method -eq "POST") { @{} } else { $null })) -Token $null
        if ($probe.Status -eq 404) { [void]$missing.Add(("{0} {1}  ({2})" -f $route.Method, $route.Path, $route.Label)) }
    }
    return $missing
}

function Get-DesignGraph {
    param([string]$VersionID, [string]$Token)
    $response = Invoke-Api -Method "GET" -Path "/api/versions/$VersionID/design/graph" -Token $Token
    Assert-Status $response @(200) "GET /api/versions/{id}/design/graph"
    return $response.Json.data
}

function Get-AllGraphCases {
    param($Graph)
    $cases = @()
    foreach ($requirement in (Get-Array $Graph.requirements)) {
        foreach ($scenario in (Get-Array $requirement.scenarios)) {
            foreach ($case in (Get-Array $scenario.cases)) { $cases += $case }
        }
    }
    return $cases
}

function Find-GraphCase {
    param($Graph, [string]$CaseID)
    foreach ($case in (Get-AllGraphCases $Graph)) {
        if ([string]$case.case_id -eq $CaseID) { return $case }
    }
    return $null
}

function Get-DesignConversations {
    param([string]$VersionID, [string]$Token)
    $response = Invoke-Api -Method "GET" -Path "/api/versions/$VersionID/conversations?kind=design&limit=50" -Token $Token
    Assert-Status $response @(200) "GET conversations?kind=design"
    return @($response.Json.data)
}

function Invoke-ExportDownload {
    param([string]$ExportID, [string]$Token, [string]$OutFile)
    $response = Invoke-WebRequest -Uri ($script:apiBase + "/api/exports/$ExportID/download") -Headers @{ Authorization = "Bearer $Token" } -OutFile $OutFile -PassThru -UseBasicParsing
    return $response
}

try {
    Step "load .env and derive the isolated eztest_v02_e2e database"
    $dotenv = Read-DotEnv
    if ([string]::IsNullOrWhiteSpace($dotenv["EZTEST_DATABASE_URL"])) { throw "EZTEST_DATABASE_URL is missing from .env" }
    $script:db = ConvertFrom-DatabaseUrl $dotenv["EZTEST_DATABASE_URL"]
    $script:testUrl = Get-TestDatabaseUrl $script:db
    if ($script:testUrl -notmatch "/$testDatabase(\?|$)") { throw "derived test database URL is invalid: $script:testUrl" }
    Write-Log ("      e2e db:  {0}/{1}" -f $script:db.Host, $testDatabase)
    Write-Log ("      api:     {0}" -f $script:apiBase)
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

    Step "login as admin/admin123"
    $login = Invoke-Api -Method "POST" -Path "/api/auth/login" -Body @{ username = "admin"; password = "admin123" } -Token $null
    Assert-Status $login @(200) "POST /api/auth/login"
    $token = [string]$login.Json.data.token
    if ([string]::IsNullOrWhiteSpace($token)) { throw "login response has no token" }
    StepPassed "login as admin/admin123"

    Step "create admin LLM config pointing at the fake LLM and refresh models"
    $config = Invoke-Api -Method "POST" -Path "/api/admin/llm/configs" -Token $token -Body @{
        name = "p2-acceptance-fake"; provider = "openai"; protocol = "openai"
        base_url = "http://127.0.0.1:$FakeLLMPort/v1"; api_key = "fake-llm-key"
        timeout_seconds = 30; max_retries = 1; is_default = $true; status = "active"
    }
    Assert-Status $config @(200) "POST /api/admin/llm/configs"
    $configID = [string]$config.Json.data.id
    $models = Invoke-Api -Method "POST" -Path "/api/admin/llm/configs/$configID/refresh-models" -Token $token -Body $null
    Assert-Status $models @(200) "POST refresh-models"
    StepPassed "create admin LLM config pointing at the fake LLM and refresh models"

    Step "create SUT/environment/page/API"
    $sut = Invoke-Api -Method "POST" -Path "/api/resources/suts" -Token $token -Body @{ name = "P2 Acceptance SUT"; intro = "Created by scripts/p2-acceptance.ps1" }
    Assert-Status $sut @(200, 201) "POST /api/resources/suts"
    $sutID = [string]$sut.Json.data.id
    $environment = Invoke-Api -Method "POST" -Path "/api/resources/suts/$sutID/environments" -Token $token -Body @{ name = "acceptance"; base_url = "https://acceptance.example.test"; is_default = $true; status = "active" }
    Assert-Status $environment @(200, 201) "POST environments"
    $environmentID = [string]$environment.Json.data.id
    $page = Invoke-Api -Method "POST" -Path "/api/resources/environments/$environmentID/pages" -Token $token -Body @{ name = "Login page"; url = "https://acceptance.example.test/login"; route_template = "/login"; page_type = "page"; auth_role = "guest"; module = "auth" }
    Assert-Status $page @(200, 201) "POST pages"
    $pageID = [string]$page.Json.data.id
    $apiResource = Invoke-Api -Method "POST" -Path "/api/resources/environments/$environmentID/apis" -Token $token -Body @{ method = "POST"; path_template = "/api/login"; name = "Login API"; auth_role = "guest"; schema_ref = "" }
    Assert-Status $apiResource @(200, 201) "POST apis"
    $apiID = [string]$apiResource.Json.data.id
    StepPassed "create SUT/environment/page/API"

    Step "create a version bound to the acceptance SUT"
    $version = Invoke-Api -Method "POST" -Path "/api/versions" -Token $token -Body @{
        title = ("P2 acceptance " + (Get-Date -Format "yyyy-MM-dd HH:mm:ss"))
        bindings = @(@{ sut_id = $sutID; environment_id = $environmentID; code_repository_id = $null; page_ids = @($pageID); api_ids = @($apiID); database_ids = @(); accounts = @() })
    }
    Assert-Status $version @(201) "POST /api/versions"
    $versionID = [string]$version.Json.data.id
    $versionSUTID = [string]$version.Json.data.bindings[0].id
    StepPassed "create a version bound to the acceptance SUT"

    Step "preflight P2 route registration"
    $missingRoutes = Find-MissingP2Routes -VersionID $versionID
    if ($missingRoutes.Count -gt 0) {
        Write-Log "      P2 endpoints not registered:" "Yellow"
        foreach ($route in $missingRoutes) { Write-Log ("      - " + $route) "Yellow" }
        $script:blocked = $true
        throw "P2 API is not fully wired; acceptance stopped before design steps"
    }
    StepPassed "preflight P2 route registration"

    Step "create and confirm two clarified requirements"
    $requirementIDs = @()
    foreach ($index in @(1, 2)) {
        $requirement = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/requirements" -Token $token -Body @{
            title = "P2 验收需求 $index"
            description = "用于 P2 设计验收的需求 $index"
            acceptance_criteria = "需求 $index 可由设计团队生成场景与用例"
            priority = if ($index -eq 1) { "P1" } else { "P2" }
            constraints = @{}
            targets = @{ version_suts = @($versionSUTID); pages = @($pageID); apis = @($apiID) }
        }
        Assert-Status $requirement @(201) "POST requirement $index"
        $rid = [string]$requirement.Json.data.id
        $confirm = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/requirements/$rid/confirmations" -Token $token -Body $null
        Assert-Status $confirm @(200) "confirm requirement $index"
        $requirementIDs += $rid
    }
    $detailBeforeDesign = Invoke-Api -Method "GET" -Path "/api/versions/$versionID" -Token $token -Body $null
    Assert-Status $detailBeforeDesign @(200) "GET version detail"
    if ([int]$detailBeforeDesign.Json.data.pending_design_count -ne 2) { throw ("pending_design_count={0}, want 2" -f $detailBeforeDesign.Json.data.pending_design_count) }
    StepPassed "create and confirm two clarified requirements"

    Step "create design conversation and wait for the initial mindmap"
    $designConversation = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/conversations" -Token $token -Body @{ kind = "design"; title = "P2 acceptance design" }
    Assert-Status $designConversation @(201) "POST design conversation"
    $conversationID = [string]$designConversation.Json.data.id
    $graph = Wait-For -TimeoutSeconds $PollTimeoutSeconds -Description "initial design graph (planner + author)" -Condition {
        $g = Get-DesignGraph -VersionID $versionID -Token $token
        $cases = Get-AllGraphCases $g
        if ($cases.Count -ge 2) { return $g }
        return $null
    }
    $allCases = Get-AllGraphCases $graph
    if ($allCases.Count -lt 2) { throw "design graph has fewer than 2 cases" }
    $firstCase = $allCases[0]
    $firstCaseID = [string]$firstCase.case_id
    $firstCaseVersionID = [string]$firstCase.current_version_id
    if ($firstCase.status -ne "draft") { throw ("initial case status is {0}, want draft" -f $firstCase.status) }
    $caseDetail = Invoke-Api -Method "GET" -Path "/api/versions/$versionID/design/cases/$firstCaseID" -Token $token -Body $null
    Assert-Status $caseDetail @(200) "GET design case detail"
    if ((Get-Array $caseDetail.Json.data.selected.steps).Count -lt 1) { throw "case detail has no steps" }
    $detailAfterDesign = Invoke-Api -Method "GET" -Path "/api/versions/$versionID" -Token $token -Body $null
    if ([int]$detailAfterDesign.Json.data.pending_design_count -ne 0) { throw ("pending_design_count after design={0}, want 0" -f $detailAfterDesign.Json.data.pending_design_count) }
    StepPassed "create design conversation and wait for the initial mindmap"

    $scenarioID = $null
    foreach ($requirement in (Get-Array $graph.requirements)) {
        foreach ($scenario in (Get-Array $requirement.scenarios)) {
            foreach ($case in (Get-Array $scenario.cases)) {
                if ($case.status -eq "draft" -and [string]$case.case_id -ne $firstCaseID) {
                    $scenarioID = [string]$scenario.id
                    break
                }
            }
            if ($null -ne $scenarioID) { break }
        }
        if ($null -ne $scenarioID) { break }
    }
    if ($null -eq $scenarioID) { throw "no scenario with a second draft case for scenario-scope confirmation" }

    Step "confirm one case, one scenario and one requirement (idempotent)"
    $confirmCase = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/design/confirmations" -Token $token -Body @{ scope = "case"; target_id = $firstCaseID }
    Assert-Status $confirmCase @(200) "confirm case"
    if ((Get-Array $confirmCase.Json.data.confirmed).Count -ne 1) { throw "case confirmation did not confirm one version" }
    $replay = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/design/confirmations" -Token $token -Body @{ scope = "case"; target_id = $firstCaseID }
    Assert-Status $replay @(200) "idempotent case confirmation"
    if ((Get-Array $replay.Json.data.confirmed).Count -ne 0) { throw "repeated case confirmation was not idempotent" }
    $confirmScenario = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/design/confirmations" -Token $token -Body @{ scope = "scenario"; target_id = $scenarioID }
    Assert-Status $confirmScenario @(200) "confirm scenario scope"
    if ((Get-Array $confirmScenario.Json.data.confirmed).Count -lt 1) { throw "scenario confirmation confirmed no draft case" }
    $confirmRequirement = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/design/confirmations" -Token $token -Body @{ scope = "requirement"; target_id = $requirementIDs[1] }
    Assert-Status $confirmRequirement @(200) "confirm requirement scope"
    if ((Get-Array $confirmRequirement.Json.data.confirmed).Count -lt 1) { throw "requirement confirmation confirmed no draft case" }
    StepPassed "confirm one case, one scenario and one requirement (idempotent)"

    Step "design chat modifies the confirmed case, then user confirms the new draft"
    $message = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/conversations/$conversationID/messages" -Token $token -Body @{
        client_message_id = "p2-design-modify-" + [guid]::NewGuid().ToString("N")
        parts = @(@{ type = "text"; text = "请修改用例：补充异常提示，并新增一条边界用例。" })
    }
    Assert-Status $message @(201) "POST design chat message"
    $updatedCase = Wait-For -TimeoutSeconds $PollTimeoutSeconds -Description "design Host case.graph_propose update" -Condition {
        $g = Get-DesignGraph -VersionID $versionID -Token $token
        $c = Find-GraphCase -Graph $g -CaseID $firstCaseID
        if ($null -ne $c -and [int]$c.revision -gt [int]$firstCase.revision) { return $c }
        return $null
    }
    if ($updatedCase.status -ne "draft") { throw ("updated case status is {0}, want draft" -f $updatedCase.status) }
    $confirmUpdated = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/design/confirmations" -Token $token -Body @{ scope = "case"; target_id = $firstCaseID }
    Assert-Status $confirmUpdated @(200) "confirm updated case"
    StepPassed "design chat modifies the confirmed case, then user confirms the new draft"

    Step "requirement rollback marks cases stale; redesign produces new draft"
    $rollback = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/requirements/$($requirementIDs[0])/rollback" -Token $token -Body @{ reason = "P2 acceptance stale" }
    Assert-Status $rollback @(200) "rollback requirement"
    $staleGraph = Wait-For -TimeoutSeconds $PollTimeoutSeconds -Description "stale case for rolled-back requirement" -Condition {
        $g = Get-DesignGraph -VersionID $versionID -Token $token
        foreach ($case in (Get-AllGraphCases $g)) { if ($case.status -eq "stale") { return $g } }
        return $null
    }
    $reconfirm = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/requirements/$($requirementIDs[0])/confirmations" -Token $token -Body $null
    Assert-Status $reconfirm @(200) "reconfirm rolled-back requirement"
    $redesigned = Wait-For -TimeoutSeconds $PollTimeoutSeconds -Description "redesigned draft for new requirement version" -Condition {
        $g = Get-DesignGraph -VersionID $versionID -Token $token
        foreach ($requirement in (Get-Array $g.requirements)) {
            if ([string]$requirement.requirement_id -ne $requirementIDs[0]) { continue }
            if ([string]$requirement.status -ne "clarified") { continue }
            foreach ($scenario in (Get-Array $requirement.scenarios)) {
                if ([string]$scenario.requirement_version_id -ne [string]$requirement.requirement_version_id) { continue }
                foreach ($case in (Get-Array $scenario.cases)) { if ($case.status -eq "draft") { return $g } }
            }
        }
        return $null
    }
    StepPassed "requirement rollback marks cases stale; redesign produces new draft"

    Step "new clarified requirement auto-joins the current design round"
    $thirdRequirement = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/requirements" -Token $token -Body @{
        title = "P2 验收需求 3（自动并入）"
        description = "设计对话已存在时应自动并入当前轮次"
        acceptance_criteria = "自动生成场景与用例"
        priority = "P2"
        constraints = @{}
        targets = @{ version_suts = @($versionSUTID); pages = @($pageID); apis = @($apiID) }
    }
    Assert-Status $thirdRequirement @(201) "POST third requirement"
    $thirdID = [string]$thirdRequirement.Json.data.id
    $thirdConfirm = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/requirements/$thirdID/confirmations" -Token $token -Body $null
    Assert-Status $thirdConfirm @(200) "confirm third requirement"
    $autoJoined = Wait-For -TimeoutSeconds $PollTimeoutSeconds -Description "auto-joined requirement graph" -Condition {
        $g = Get-DesignGraph -VersionID $versionID -Token $token
        foreach ($requirement in (Get-Array $g.requirements)) {
            if ([string]$requirement.requirement_id -eq $thirdID) {
                foreach ($scenario in (Get-Array $requirement.scenarios)) { if ((Get-Array $scenario.cases).Count -gt 0) { return $g } }
            }
        }
        return $null
    }
    $detailAfterJoin = Invoke-Api -Method "GET" -Path "/api/versions/$versionID" -Token $token -Body $null
    if ([int]$detailAfterJoin.Json.data.pending_design_count -ne 0) { throw "pending_design_count after auto-join is not 0" }
    StepPassed "new clarified requirement auto-joins the current design round"

    Step "upload an attachment to the design conversation and wait for extraction"
    $markdown = "# Design attachment`n`n- case export must include steps`n- assertions must be explicit`n"
    Set-Content -LiteralPath $attachmentPath -Value $markdown -Encoding UTF8
    $upload = Invoke-MultipartApi -Path "/api/content" -FilePath $attachmentPath -MimeType "text/markdown" -Token $token
    Assert-Status $upload @(201) "POST /api/content"
    $sha256 = [string]$upload.Json.data.sha256
    $size = [int64]$upload.Json.data.size
    $attachMessage = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/conversations/$conversationID/messages" -Token $token -Body @{
        client_message_id = "p2-design-attach-" + [guid]::NewGuid().ToString("N")
        parts = @(@{ type = "text"; text = "请参考附件补充设计。" }, @{ type = "attachment"; sha256 = $sha256; name = "design-spec.md"; mime = "text/markdown"; size = $size })
    }
    Assert-Status $attachMessage @(201) "POST design attachment message"
    $attachmentStatus = Wait-For -TimeoutSeconds $PollTimeoutSeconds -Description "design attachment extraction status" -Condition {
        $sql = "SELECT extraction_status FROM requirement_attachment WHERE version_id = $versionID AND name = 'design-spec.md' ORDER BY id DESC LIMIT 1;"
        $text = (@(Invoke-Psql -DatabaseName $testDatabase -SqlText $sql) -join "").Trim()
        if ($text -eq "done") { return "done" }
        if ($text -like "failed*") { throw ("attachment extraction failed: {0}" -f $text) }
        return $null
    }
    if ($attachmentStatus -ne "done") { throw "design attachment extraction did not reach done" }
    StepPassed "upload an attachment to the design conversation and wait for extraction"

    Step "export all four formats and verify download content/hashes"
    $formats = @("markdown", "mermaid", "opml", "csv")
    $exportIDs = @{}
    foreach ($format in $formats) {
        $export = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/exports" -Token $token -Body @{ owner_kind = "case"; owner_id = $versionID; format = $format }
        Assert-Status $export @(201) "POST export $format"
        $exportID = [string]$export.Json.data.id
        $exportIDs[$format] = $exportID
        if ([string]$export.Json.data.format -ne $format) { throw "export format mismatch for $format" }
        $outFile = Join-Path $fixtureDir ("export-$format.bin")
        $download = Invoke-ExportDownload -ExportID $exportID -Token $token -OutFile $outFile
        if ($download.StatusCode -ne 200) { throw "download $format status = $($download.StatusCode)" }
        $hash = (Get-FileHash -LiteralPath $outFile -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($hash -ne [string]$export.Json.data.sha256) { throw "download sha mismatch for $format" }
        if ((Get-Item -LiteralPath $outFile).Length -ne [int64]$export.Json.data.size) { throw "download size mismatch for $format" }
    }
    $markdownFile = Join-Path $fixtureDir "export-markdown.bin"
    if (-not ((Get-Content -LiteralPath $markdownFile -Raw -Encoding UTF8).Contains("AI 用例"))) { throw "markdown export does not contain generated case titles" }
    $csvFile = Join-Path $fixtureDir "export-csv.bin"
    if (-not ((Get-Content -LiteralPath $csvFile -Raw -Encoding UTF8).Contains("step_seq"))) { throw "csv export header missing step_seq" }
    $exportList = Invoke-Api -Method "GET" -Path "/api/versions/$versionID/exports" -Token $token -Body $null
    Assert-Status $exportList @(200) "GET exports"
    if ((Get-Array $exportList.Json.data).Count -lt 4) { throw "export list has fewer than 4 artifacts" }
    $badExport = Invoke-Api -Method "POST" -Path "/api/versions/$versionID/exports" -Token $token -Body @{ owner_kind = "case"; owner_id = $versionID; format = "pdf" }
    Assert-Status $badExport @(400) "reject unsupported P2 export format"
    StepPassed "export all four formats and verify download content/hashes"

    Step "auto_design_new_requirements creates the first design conversation"
    $version2 = Invoke-Api -Method "POST" -Path "/api/versions" -Token $token -Body @{
        title = ("P2 auto design " + (Get-Date -Format "yyyy-MM-dd HH:mm:ss"))
        bindings = @(@{ sut_id = $sutID; environment_id = $environmentID; code_repository_id = $null; page_ids = @($pageID); api_ids = @($apiID); database_ids = @(); accounts = @() })
    }
    Assert-Status $version2 @(201) "POST auto-design version"
    $version2ID = [string]$version2.Json.data.id
    $version2SUTID = [string]$version2.Json.data.bindings[0].id
    $autoRequirement = Invoke-Api -Method "POST" -Path "/api/versions/$version2ID/requirements" -Token $token -Body @{
        title = "P2 自动设计需求"; description = "auto design"; acceptance_criteria = "auto design"
        priority = "P2"; constraints = @{}; targets = @{ version_suts = @($version2SUTID); pages = @($pageID); apis = @($apiID) }
    }
    Assert-Status $autoRequirement @(201) "POST auto-design requirement"
    $autoRequirementID = [string]$autoRequirement.Json.data.id
    $autoConfirm = Invoke-Api -Method "POST" -Path "/api/versions/$version2ID/requirements/$autoRequirementID/confirmations" -Token $token -Body $null
    Assert-Status $autoConfirm @(200) "confirm auto-design requirement"
    $patch = Invoke-Api -Method "PATCH" -Path "/api/versions/$version2ID" -Token $token -Body @{ auto_design_new_requirements = $true }
    Assert-Status $patch @(200) "PATCH auto_design_new_requirements"
    $autoConversationCount = Wait-For -TimeoutSeconds $PollTimeoutSeconds -Description "auto-created design conversation" -Condition {
        $sql = "SELECT count(*) FROM conversation WHERE version_id = $version2ID AND kind = 'design';"
        $countText = (@(Invoke-Psql -DatabaseName $testDatabase -SqlText $sql) -join "").Trim()
        if ([int]$countText -ge 1) { return [int]$countText }
        return $null
    }
    if ([int]$autoConversationCount -lt 1) { throw "no auto-created design conversation" }
    $autoList = Get-DesignConversations -VersionID $version2ID -Token $token
    Write-Log ("      auto design conversations: " + @($autoList).Count) "Gray"
    StepPassed "auto_design_new_requirements creates the first design conversation"

    Step "assert the P2 SSE event stream"
    $channel = "version:$versionID"
    # P3 automatically starts execution for confirmed cases, so later phases add
    # many events to the same version channel. Page through the channel instead
    # of assuming the first 200-event snapshot still contains the P2 events.
    $eventTypes = @()
    $after = 1
    $pages = 0
    while ($pages -lt 100) {
        $eventsPath = "/api/events?channel={0}&after={1}&limit=200" -f ([System.Uri]::EscapeDataString($channel)), $after
        $snapshot = Invoke-Api -Method "GET" -Path $eventsPath -Token $token -Body $null
        Assert-Status $snapshot @(200) "GET /api/events"
        $page = Get-Array $snapshot.Json.data
        if ($page.Count -eq 0) { break }
        foreach ($event in $page) { $eventTypes += [string]$event.type }
        $lastEvent = $page[$page.Count - 1]
        $after = [int64]$lastEvent.seq
        if (-not $snapshot.Json.has_more) { break }
        $pages++
    }
    foreach ($required in @("design.round_started", "design.scope_activated", "design.graph_delta", "design.case_version_created", "design.case_confirmed", "design.case_version_stale", "design.case_version_superseded", "design.export_ready")) {
        Assert-EventPresent -Types $eventTypes -Type $required
    }
    Write-Log ("      design events: " + (($eventTypes | Where-Object { $_ -like "design.*" } | Sort-Object -Unique) -join ", "))
    StepPassed "assert the P2 SSE event stream"

    Step "version projection initial -> running after design conversation"
    $versionAfter = Invoke-Api -Method "GET" -Path "/api/versions/$versionID" -Token $token -Body $null
    Assert-Status $versionAfter @(200) "GET version"
    if ($versionAfter.Json.data.status -ne "running") { throw ("version status is {0}, want running" -f $versionAfter.Json.data.status) }
    StepPassed "version projection initial -> running after design conversation"

    $exitCode = 0
} catch {
    if ($script:blocked) {
        Write-Log ("BLOCKED  " + $_.Exception.Message) "Yellow"
        $exitCode = 2
    } else {
        $script:failed = $true
        Write-Log ("FAIL  " + $_.Exception.Message) "Red"
        if ($null -ne $_.InvocationInfo) { Write-Log ("      at line " + $_.InvocationInfo.ScriptLineNumber) "DarkRed" }
        $exitCode = 1
    }
} finally {
    if ($KeepServices) { Write-Log "Keeping API/fake LLM processes alive (-KeepServices)" "Yellow" } else { Stop-Services }
    if ($null -ne $script:http) { $script:http.Dispose() }
    if ($exitCode -eq 0) { Write-Log ("RESULT: PASS ({0} steps)" -f $script:completedSteps) "Green" }
    elseif ($exitCode -eq 2) { Write-Log "RESULT: BLOCKED (P2 API wiring incomplete; no fake pass)" "Yellow" }
    else { Write-Log ("RESULT: FAIL ({0} steps completed)" -f $script:completedSteps) "Red" }
    Write-Log ("Evidence log: " + $logFile)
    Write-Log ("API stderr:   " + (Join-Path $logDir "api.stderr.log"))
}

exit $exitCode



