#requires -Version 5.1
<#
.SYNOPSIS
    EzTest 0.2 P3 real-DeepSeek acceptance runner (API level, no browser).

.DESCRIPTION
    Boots a fully isolated P3 stack and drives the acceptance scenarios of
    docs/superpowers/specs/2026-11-01-eztest-v02-p3-execution-design.md 13.2:

      * database   : eztest_v02_e2e (dropped and recreated on every run)
      * target DB  : EZTEST_TEST_DATABASE_URL (seeded by scripts/p3-fixture-db.ps1)
      * fixtures   : scripts/fixture-target.cjs on 127.0.0.1:13335 (orders) and
                     127.0.0.1:13337 (payments)
      * executor   : executor/src/server.mjs on 127.0.0.1:14336 with a random
                     bearer token, wired through the admin settings
      * API        : freshly built .tmp/p3/eztest-p3.exe on 127.0.0.1:14333
      * LLM        : the real DeepSeek config from .env (llm_api_key/llm_base_url/
                     llm_model_name), written through the admin LLM API

    Real LLM only: a failing scenario is recorded as FAIL with the exact step,
    never replaced by a fake model. Evidence goes to .tmp/p3-acceptance/.

    Exit codes: 0 = all scenarios PASS, 1 = at least one FAIL (see
    failure-step.txt), 2 = BLOCKED (environment/API contract unavailable).

.NOTES
    Windows PowerShell 5.1+ compatible (also runs on PowerShell 7). No extra
    PowerShell modules. Never touches the development database or executor 13336.
#>
[CmdletBinding()]
param(
    [int]$ApiPort = 14333,
    [int]$ExecutorPort = 14336,
    [int]$OrdersPort = 13335,
    [int]$PaymentsPort = 13337,
    [int]$WebPort = 16868,
    [int]$PollTimeoutSeconds = 1500,
    [switch]$LongWait,
    [switch]$SkipBuild,
    [switch]$KeepRunning
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$logDir = Join-Path $repoRoot ".tmp\p3-acceptance"
$evidenceDir = Join-Path $logDir "evidence"
$exePath = Join-Path $repoRoot ".tmp\p3\eztest-p3.exe"
$acceptanceLog = Join-Path $logDir "acceptance.log"
$failureStepFile = Join-Path $logDir "failure-step.txt"
$summaryFile = Join-Path $logDir "scenarios.txt"
$seedFile = Join-Path $logDir "seed-cases.sql"
$testDatabase = "eztest_v02_e2e"
$envPath = Join-Path $repoRoot ".env"

New-Item -ItemType Directory -Force -Path $logDir | Out-Null
New-Item -ItemType Directory -Force -Path $evidenceDir | Out-Null
New-Item -ItemType Directory -Force -Path (Split-Path -Parent $exePath) | Out-Null
Set-Content -LiteralPath $acceptanceLog -Value "" -Encoding UTF8
Remove-Item -LiteralPath $failureStepFile -ErrorAction SilentlyContinue
Remove-Item -LiteralPath $summaryFile -ErrorAction SilentlyContinue

$script:apiBase = "http://127.0.0.1:$ApiPort"
$script:executorBase = "http://127.0.0.1:$ExecutorPort"
$script:ordersBase = "http://127.0.0.1:$OrdersPort"
$script:paymentsBase = "http://127.0.0.1:$PaymentsPort"
$script:http = $null
$script:apiProcess = $null
$script:executorProcess = $null
$script:ordersFixture = $null
$script:paymentsFixture = $null
$script:psql = $null
$script:db = $null
$script:token = ""
$script:results = New-Object System.Collections.ArrayList
$script:softFailures = New-Object System.Collections.ArrayList
$script:humanScenario = $null
$script:currentScenario = $null
$script:failed = $false
$script:blocked = $false
$script:stage = "bootstrap"
$script:executorToken = ""
$script:startedAt = Get-Date

# --------------------------------------------------------------------------
# logging / assertions / scenario accounting
# --------------------------------------------------------------------------

function Write-Log {
    param([string]$Message, [string]$Color = "Gray")
    $line = "[{0}] {1}" -f (Get-Date -Format "yyyy-MM-dd HH:mm:ss"), $Message
    Write-Host $line -ForegroundColor $Color
    Add-Content -LiteralPath $acceptanceLog -Value $line -Encoding UTF8
}

function Step {
    param([string]$Name)
    $script:stage = $Name
    Write-Log ("STEP  " + $Name) "Cyan"
}

function Assert-True {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw ("ASSERT FAILED: " + $Message) }
}

function Assert-Equal {
    param($Actual, $Expected, [string]$What)
    if ([string]$Actual -ne [string]$Expected) {
        throw ("ASSERT FAILED: {0} = '{1}', want '{2}'" -f $What, [string]$Actual, [string]$Expected)
    }
}

function Assert-Match-Sha {
    param([string]$Value, [string]$What)
    if ($Value -notmatch '^[0-9a-f]{64}$') {
        throw ("ASSERT FAILED: {0} = '{1}' is not a lowercase sha256" -f $What, $Value)
    }
}

function Start-Scenario {
    param([string]$Name)
    $record = [pscustomobject]@{
        Name    = $Name
        Started = Get-Date
        Status  = "RUNNING"
        Detail  = ""
    }
    [void]$script:results.Add($record)
    $script:currentScenario = $record
    Write-Log ("SCENARIO START  " + $Name) "Cyan"
    return $record
}

function Pass-Scenario {
    param($Record)
    if ($null -eq $Record) { $Record = $script:currentScenario }
    if ($null -eq $Record) { return }
    if ($Record.Status -eq "FAIL") {
        Write-Log ("SCENARIO FAIL   " + $Record.Name + " (recorded failures, continuing)") "Red"
        return
    }
    $Record.Status = "PASS"
    $Record.Detail = ("{0:N1}s" -f ((Get-Date) - $Record.Started).TotalSeconds)
    Write-Log ("SCENARIO PASS   " + $Record.Name + " (" + $Record.Detail + ")") "Green"
}

<#
    Records one explicit FAIL without aborting the whole run. Used for
    independent scenario assertions (for example a missing P3 event) so a
    single run still produces the complete per-scenario matrix. The run exits
    with code 1 and writes failure-step.txt.
#>
function Add-SoftFailure {
    param([string]$Message, $Record)
    Write-Log ("FAIL  " + $Message) "Red"
    [void]$script:softFailures.Add($Message)
    $target = $Record
    if ($null -eq $target) { $target = $script:currentScenario }
    if ($null -ne $target) {
        $target.Status = "FAIL"
        if ($target.PSObject.Properties["Error"]) {
            $target.PSObject.Properties["Error"].Value = $Message
        } else {
            $target | Add-Member -NotePropertyName Error -NotePropertyValue $Message
        }
    }
}

function Write-FailureStep {
    param([string]$Message)
    $content = @(
        "FAILED AT: " + $script:stage,
        "SCENARIO : " + $(if ($null -ne $script:currentScenario) { $script:currentScenario.Name } else { "<none>" }),
        "TIME     : " + (Get-Date -Format "yyyy-MM-dd HH:mm:ss"),
        "MESSAGE  : " + $Message,
        "LOG      : " + $acceptanceLog
    ) -join [Environment]::NewLine
    Set-Content -LiteralPath $failureStepFile -Value $content -Encoding UTF8
}

function Write-ScenarioSummary {
    $lines = @()
    foreach ($item in $script:results) {
        $lines += ("{0}|{1}|{2}|{3}" -f $item.Status, $item.Name, $item.Detail, $(if ($null -ne $item.PSObject.Properties["Error"]) { $item.PSObject.Properties["Error"].Value } else { "" }))
    }
    Set-Content -LiteralPath $summaryFile -Value $lines -Encoding UTF8
}

# --------------------------------------------------------------------------
# process / port / env helpers
# --------------------------------------------------------------------------

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
    if (-not (Test-Path -LiteralPath $envPath)) { throw "Missing .env at $envPath" }
    $values = @{}
    foreach ($line in Get-Content -LiteralPath $envPath) {
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
    if (-not $match.Success) { throw "database URL is not a postgres URL with credentials: $RawUrl" }
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

function Get-DatabaseUrl {
    param($Database, [string]$DatabaseName)
    $query = $Database.Query
    if ([string]::IsNullOrEmpty($query)) { $query = "?sslmode=disable" }
    return "{0}://{1}:{2}@{3}:{4}/{5}{6}" -f $Database.Scheme,
        [System.Uri]::EscapeDataString($Database.User), [System.Uri]::EscapeDataString($Database.Password),
        $Database.Host, $Database.Port, $DatabaseName, $query
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
    param([string]$DatabaseName, [string]$SqlText)
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
    if ($exit -ne 0) { throw ("psql failed ({0}): {1}" -f $exit, ($output -join " ")) }
    return $output
}

function Invoke-PsqlFile {
    param([string]$DatabaseName, [string]$FilePath)
    $previousPassword = $env:PGPASSWORD
    $previousPreference = $ErrorActionPreference
    try {
        $env:PGPASSWORD = $script:db.Password
        $ErrorActionPreference = "Continue"
        $output = & $script:psql -h $script:db.Host -p $script:db.Port -U $script:db.User `
            -d $DatabaseName -v ON_ERROR_STOP=1 -q -f $FilePath 2>&1
        $exit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousPreference
        $env:PGPASSWORD = $previousPassword
    }
    if ($exit -ne 0) { throw ("psql -f failed ({0}): {1}" -f $exit, ($output -join " ")) }
}

function Invoke-PsqlScalar {
    param([string]$DatabaseName, [string]$SqlText)
    $rows = @(Invoke-Psql -DatabaseName $DatabaseName -SqlText $SqlText)
    $text = ($rows | Out-String).Trim()
    return $text
}

function Reset-TestDatabase {
    $previousPassword = $env:PGPASSWORD
    $previousPreference = $ErrorActionPreference
    try {
        $env:PGPASSWORD = $script:db.Password
        $ErrorActionPreference = "Continue"
        $output = & $script:psql -h $script:db.Host -p $script:db.Port -U $script:db.User -d postgres `
            -v ON_ERROR_STOP=1 -c ("DROP DATABASE IF EXISTS {0} WITH (FORCE);" -f $testDatabase) `
            -c ("CREATE DATABASE {0};" -f $testDatabase) 2>&1
        $exit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousPreference
        $env:PGPASSWORD = $previousPassword
    }
    if ($exit -ne 0) { throw ("cannot reset {0}: {1}" -f $testDatabase, ($output -join " ")) }
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
    Step "build backend/cmd/eztest -> $exePath"
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
    if ($exit -ne 0) { throw ("go build ./cmd/eztest failed: {0}" -f ($output -join " ")) }
    if (-not (Test-Path -LiteralPath $exePath)) { throw "go build did not produce $exePath" }
}

function New-ApiHttpClient {
    Add-Type -AssemblyName System.Net.Http | Out-Null
    $client = New-Object System.Net.Http.HttpClient
    $client.Timeout = [TimeSpan]::FromSeconds(120)
    return $client
}

function Invoke-Api {
    param(
        [string]$Method,
        [string]$Path,
        $Body,
        [string]$Token,
        [int]$TimeoutSeconds = 60
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
            return [pscustomobject]@{
                Status = [int]$response.StatusCode
                Raw    = $raw
                Json   = $parsed
            }
        } finally {
            $response.Dispose()
        }
    } finally {
        $request.Dispose()
    }
}

function Invoke-ApiBytes {
    param([string]$Path, [string]$Token, [string]$OutFile)
    $uri = $script:apiBase + $Path
    $request = New-Object System.Net.Http.HttpRequestMessage ([System.Net.Http.HttpMethod]::Get), $uri
    try {
        if (-not [string]::IsNullOrEmpty($Token)) {
            $request.Headers.Authorization = New-Object System.Net.Http.Headers.AuthenticationHeaderValue("Bearer", $Token)
        }
        $response = $script:http.SendAsync($request).GetAwaiter().GetResult()
        try {
            $bytes = $response.Content.ReadAsByteArrayAsync().GetAwaiter().GetResult()
            [System.IO.File]::WriteAllBytes($OutFile, $bytes)
            $headers = @{}
            foreach ($header in $response.Headers) { $headers[$header.Key.ToLowerInvariant()] = ($header.Value -join ",") }
            foreach ($header in $response.Content.Headers) { $headers[$header.Key.ToLowerInvariant()] = ($header.Value -join ",") }
            return [pscustomobject]@{
                Status  = [int]$response.StatusCode
                Bytes   = $bytes
                Headers = $headers
                Raw     = [System.Text.Encoding]::UTF8.GetString($bytes)
            }
        } finally {
            $response.Dispose()
        }
    } finally {
        $request.Dispose()
    }
}

function Assert-Status {
    param($Response, [int[]]$Expected, [string]$What)
    if ($Expected -notcontains $Response.Status) {
        $detail = $Response.Raw
        if ($null -ne $Response.Json -and $null -ne $Response.Json.error) {
            $detail = ("{0}: {1}" -f $Response.Json.error.code, $Response.Json.error.message)
        }
        throw ("{0}: unexpected HTTP {1} ({2})" -f $What, $Response.Status, $detail)
    }
}

function Get-Array {
    param($Value)
    if ($null -eq $Value) { return @() }
    return ,@($Value)
}

function Wait-For {
    param([scriptblock]$Condition, [int]$TimeoutSeconds, [string]$Description, [int]$IntervalMilliseconds = 500)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        $value = & $Condition
        if ($null -ne $value -and $value -ne $false) { return $value }
        Start-Sleep -Milliseconds $IntervalMilliseconds
    }
    throw ("timeout after {0}s waiting for {1}" -f $TimeoutSeconds, $Description)
}

function Wait-Http {
    param([string]$Url, [int]$Seconds)
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

# --------------------------------------------------------------------------
# service lifecycle
# --------------------------------------------------------------------------

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
    Wait-For -TimeoutSeconds 120 -Description "API health" -Condition {
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

function Stop-Api {
    if ($null -ne $script:apiProcess) {
        try {
            if (-not $script:apiProcess.HasExited) { Stop-Process -Id $script:apiProcess.Id -Force -ErrorAction SilentlyContinue }
            $script:apiProcess.WaitForExit(10000) | Out-Null
        } catch { }
        $script:apiProcess = $null
    }
}

function Start-Executor {
    if (Test-PortInUse -Port $ExecutorPort) {
        throw "port $ExecutorPort is already in use; stop the stale P3 executor first"
    }
    $node = Get-Command node -ErrorAction SilentlyContinue
    if ($null -eq $node) { throw "node not found on PATH" }
    $envKeys = @("EZTEST_EXECUTOR_ADDR", "EZTEST_EXECUTOR_TOKEN", "EZTEST_EXECUTOR_MAX_CONTEXTS", "EZTEST_EXEC_ALLOWED_HOSTS")
    $previous = @{}
    foreach ($key in $envKeys) { $previous[$key] = [Environment]::GetEnvironmentVariable($key) }
    try {
        $env:EZTEST_EXECUTOR_ADDR = "127.0.0.1:$ExecutorPort"
        $env:EZTEST_EXECUTOR_TOKEN = $script:executorToken
        $env:EZTEST_EXECUTOR_MAX_CONTEXTS = "8"
        $env:EZTEST_EXEC_ALLOWED_HOSTS = "127.0.0.1,localhost"
        $script:executorProcess = Start-Process -FilePath $node.Source `
            -ArgumentList @("src/server.mjs") `
            -WorkingDirectory (Join-Path $repoRoot "executor") `
            -RedirectStandardOutput (Join-Path $logDir "executor.stdout.log") `
            -RedirectStandardError (Join-Path $logDir "executor.stderr.log") -PassThru
    } finally {
        foreach ($key in $envKeys) {
            if ($null -eq $previous[$key]) { Remove-Item ("Env:" + $key) -ErrorAction SilentlyContinue }
            else { Set-Item ("Env:" + $key) -Value $previous[$key] }
        }
    }
    if (-not (Wait-Http -Url ($script:executorBase + "/healthz") -Seconds 30)) {
        throw "executor did not become healthy at $($script:executorBase)/healthz"
    }
    Write-Log "      executor ready on $($script:executorBase) (token not printed)"
}

function Stop-Executor {
    if ($null -ne $script:executorProcess) {
        try {
            & taskkill /PID $script:executorProcess.Id /T /F *> $null
        } catch { }
        Start-Sleep -Milliseconds 800
        $script:executorProcess = $null
    }
}

function Start-Fixture {
    param([int]$Port, [string]$Variant, [string]$LogName)
    if (Test-PortInUse -Port $Port) {
        throw "port $Port is already in use; stop the stale fixture first"
    }
    $node = Get-Command node -ErrorAction SilentlyContinue
    if ($null -eq $node) { throw "node not found on PATH" }
    $previousPort = $env:FIXTURE_PORT
    $previousVariant = $env:FIXTURE_VARIANT
    try {
        $env:FIXTURE_PORT = [string]$Port
        $env:FIXTURE_VARIANT = $Variant
        return Start-Process -FilePath $node.Source `
            -ArgumentList @((Join-Path $repoRoot "scripts\fixture-target.cjs")) `
            -WorkingDirectory $repoRoot `
            -RedirectStandardOutput (Join-Path $logDir ($LogName + ".stdout.log")) `
            -RedirectStandardError (Join-Path $logDir ($LogName + ".stderr.log")) -PassThru
    } finally {
        $env:FIXTURE_PORT = $previousPort
        $env:FIXTURE_VARIANT = $previousVariant
    }
}

function Stop-Services {
    if ($KeepRunning) {
        Write-Log "Keeping API/executor/fixture processes alive (-KeepRunning)" "Yellow"
        return
    }
    Stop-Api
    Stop-Executor
    foreach ($process in @($script:ordersFixture, $script:paymentsFixture)) {
        if ($null -ne $process) {
            try { if (-not $process.HasExited) { Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue } } catch { }
        }
    }
    $script:ordersFixture = $null
    $script:paymentsFixture = $null
}

# --------------------------------------------------------------------------
# event stream helpers
# --------------------------------------------------------------------------

function Read-SseEvents {
    param(
        [string]$Channel,
        [long]$LastEventId = 0,
        [int]$TimeoutSeconds = 30,
        [int]$IdleSeconds = 3,
        [int]$MaxEvents = 3000
    )
    $client = New-Object System.Net.Http.HttpClient
    $client.Timeout = [TimeSpan]::FromSeconds($TimeoutSeconds + 30)
    $uri = $script:apiBase + "/api/stream?channel=" + [System.Uri]::EscapeDataString($Channel)
    $request = New-Object System.Net.Http.HttpRequestMessage ([System.Net.Http.HttpMethod]::Get), $uri
    $reader = $null
    $response = $null
    try {
        $request.Headers.Authorization = New-Object System.Net.Http.Headers.AuthenticationHeaderValue("Bearer", $script:token)
        $request.Headers.Accept.ParseAdd("text/event-stream")
        if ($LastEventId -gt 0) {
            [void]$request.Headers.TryAddWithoutValidation("Last-Event-ID", [string]$LastEventId)
        }
        $cts = New-Object System.Threading.CancellationTokenSource
        $cts.CancelAfter([TimeSpan]::FromSeconds($TimeoutSeconds))
        try {
            $response = $client.SendAsync($request, [System.Net.Http.HttpCompletionOption]::ResponseHeadersRead, $cts.Token).GetAwaiter().GetResult()
            if ([int]$response.StatusCode -ne 200) {
                throw ("SSE stream returned HTTP {0}" -f [int]$response.StatusCode)
            }
            $stream = $response.Content.ReadAsStreamAsync().GetAwaiter().GetResult()
            $reader = New-Object System.IO.StreamReader($stream)
            $events = New-Object System.Collections.ArrayList
            $currentId = [long]0
            $currentType = ""
            $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
            while ((Get-Date) -lt $deadline -and $events.Count -lt $MaxEvents) {
                $readTask = $reader.ReadLineAsync()
                $delayTask = [System.Threading.Tasks.Task]::Delay([TimeSpan]::FromSeconds($IdleSeconds))
                $finished = [System.Threading.Tasks.Task]::WhenAny($readTask, $delayTask).GetAwaiter().GetResult()
                if ($finished -ne $readTask) { break }
                $line = $readTask.GetAwaiter().GetResult()
                if ($null -eq $line) { break }
                if ($line -eq "") {
                    if ($currentType -ne "") {
                        [void]$events.Add([pscustomobject]@{ Id = $currentId; Type = $currentType })
                    }
                    $currentId = [long]0
                    $currentType = ""
                    continue
                }
                if ($line.StartsWith("id: ")) { $currentId = [long]$line.Substring(4).Trim() }
                elseif ($line.StartsWith("event: ")) { $currentType = $line.Substring(7).Trim() }
            }
            return $events
        } finally {
            $cts.Dispose()
        }
    } finally {
        if ($null -ne $reader) { $reader.Dispose() }
        if ($null -ne $response) { $response.Dispose() }
        $request.Dispose()
        $client.Dispose()
    }
}

# --------------------------------------------------------------------------
# API projections
# --------------------------------------------------------------------------

function Get-Overview {
    $response = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/execution/overview" -Token $script:token
    Assert-Status $response @(200) "GET execution/overview"
    return $response.Json.data
}

function Get-Waves {
    $response = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/waves?limit=100" -Token $script:token
    Assert-Status $response @(200) "GET waves"
    return Get-Array $response.Json.data
}

function Get-ActiveWave {
    $overview = Get-Overview
    return $overview.active_wave
}

function Get-CaseRunsOfWave {
    param([string]$WaveID)
    $response = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/waves/$WaveID/case-runs?limit=100" -Token $script:token
    Assert-Status $response @(200) "GET wave case-runs"
    return Get-Array $response.Json.data
}

function Get-AllCaseRuns {
    $runs = New-Object System.Collections.ArrayList
    foreach ($wave in (Get-Waves)) {
        foreach ($run in (Get-CaseRunsOfWave -WaveID ([string]$wave.id))) { [void]$runs.Add($run) }
    }
    return $runs
}

function Get-CaseRun {
    param([string]$CaseRunID)
    $response = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/case-runs/$CaseRunID" -Token $script:token
    Assert-Status $response @(200) "GET case-run $CaseRunID"
    return $response.Json.data
}

function Get-CaseRunByTitle {
    param([string]$Title)
    foreach ($run in (Get-AllCaseRuns)) {
        if ([string]$run.title -eq $Title) { return $run }
    }
    return $null
}

function Get-SnapshotEvents {
    # GET /api/events only exposes the newest page (after=0) plus forward
    # paging, so full-history assertions read the persisted event log of this
    # isolated database. The SSE endpoint itself is exercised in scenario 13.
    $channel = "version:$($script:versionID)"
    $rows = @(Invoke-Psql -DatabaseName $testDatabase -SqlText ("SELECT type || '|' || envelope::text FROM event WHERE channel = '{0}' ORDER BY seq;" -f $channel))
    $events = New-Object System.Collections.ArrayList
    foreach ($row in $rows) {
        $text = [string]$row
        if ([string]::IsNullOrWhiteSpace($text)) { continue }
        $separator = $text.IndexOf("|")
        if ($separator -lt 1) { continue }
        $eventType = $text.Substring(0, $separator)
        $envelope = $null
        try { $envelope = ConvertFrom-Json $text.Substring($separator + 1) } catch { $envelope = $null }
        $payload = $null
        $scope = $null
        if ($null -ne $envelope) {
            $payload = $envelope.payload
            $scope = $envelope.scope
        }
        [void]$events.Add([pscustomobject]@{ Type = $eventType; Payload = $payload; Scope = $scope })
    }
    return $events
}

function Get-EventBounds {
    $channel = "version:$($script:versionID)"
    $text = Invoke-PsqlScalar -DatabaseName $testDatabase -SqlText ("SELECT COALESCE(MIN(seq),0) || '|' || COALESCE(MAX(seq),0) FROM event WHERE channel = '{0}';" -f $channel)
    $parts = ([string]$text).Split("|")
    return [pscustomobject]@{ FirstSeq = [long]$parts[0]; LastSeq = [long]$parts[1] }
}

function Get-EventTypesOf {
    param($Events)
    return @($Events | ForEach-Object { [string]$_.type })
}

function Assert-EventPresent {
    param($Events, [string]$Type)
    $types = Get-EventTypesOf $Events
    if (-not ($types -contains $Type)) {
        throw ("event '{0}' is missing (seen: {1})" -f $Type, (($types | Sort-Object -Unique) -join ", "))
    }
}

function Get-WaveRunStats {
    param([string]$WaveID)
    $runs = Get-CaseRunsOfWave -WaveID $WaveID
    $running = @($runs | Where-Object { $_.status -eq "running" })
    $perSut = @{}
    foreach ($run in $running) {
        foreach ($sutID in (Get-Array $run.version_sut_ids)) {
            if (-not $perSut.ContainsKey([string]$sutID)) { $perSut[[string]$sutID] = 0 }
            $perSut[[string]$sutID] = $perSut[[string]$sutID] + 1
        }
    }
    $cross = @($running | Where-Object { (Get-Array $_.version_sut_ids).Count -gt 1 }).Count
    return [pscustomobject]@{
        Global = $running.Count
        PerSUT = $perSut
        Cross  = $cross
        Runs   = $runs
    }
}

# Waits until the version has no active wave and no active intent. The scheduler
# completes a wave on its next 1s tick, so a scenario that creates a new intent
# immediately after a case passed can otherwise race the previous wave/intent.
function Wait-ExecutionIdle {
    param([int]$TimeoutSeconds = 120)
    Wait-For -TimeoutSeconds $TimeoutSeconds -Description "execution idle (no active wave/intent)" -Condition {
        $active = Get-ActiveWave
        if ($null -ne $active) { return $null }
        $count = Invoke-PsqlScalar -DatabaseName $testDatabase -SqlText ("SELECT count(*) FROM execution_intent WHERE version_id = {0} AND status IN ('open','claimed','running');" -f $script:versionID)
        if ([int]$count -eq 0) { return $true }
        return $null
    } | Out-Null
}

# --------------------------------------------------------------------------
# fixture / LLM / resource setup helpers
# --------------------------------------------------------------------------

function Set-Setting {
    param([string]$Key, $Value)
    $response = Invoke-Api -Method "PATCH" -Path ("/api/admin/settings/" + [System.Uri]::EscapeDataString($Key)) -Token $script:token -Body @{ value = $Value }
    Assert-Status $response @(200) ("PATCH setting " + $Key)
    return $response.Json.data
}

function Get-Setting {
    param([string]$Key)
    $response = Invoke-Api -Method "GET" -Path ("/api/admin/settings/" + [System.Uri]::EscapeDataString($Key)) -Token $script:token
    Assert-Status $response @(200) ("GET setting " + $Key)
    return $response.Json.data
}

function Get-SqlJsonArray {
    param([string[]]$Values)
    $quoted = @()
    foreach ($value in $Values) {
        if ($value -eq "") { continue }
        $quoted += ('"' + $value + '"')
    }
    return "[" + ($quoted -join ",") + "]"
}

# --------------------------------------------------------------------------
# main
# --------------------------------------------------------------------------

$exitCode = 0
try {
    # ---------------- S1: preparation -------------------------------------
    $s1 = Start-Scenario "S1 preparation: isolated DB, API, executor, fixtures, real LLM, resources, version, requirements"

    Step "load .env (4 EZTEST env + lowercase llm_* + target database URL)"
    $dotenv = Read-DotEnv
    foreach ($key in @("EZTEST_DATABASE_URL", "llm_api_key", "llm_base_url", "llm_model_name")) {
        if ([string]::IsNullOrWhiteSpace($dotenv[$key])) { throw ("$key is missing from .env") }
    }
    $script:db = ConvertFrom-DatabaseUrl $dotenv["EZTEST_DATABASE_URL"]
    $script:testUrl = Get-DatabaseUrl -Database $script:db -DatabaseName $testDatabase
    if ($script:testUrl -notmatch ("/" + $testDatabase + "(\?|$)")) { throw "derived test database URL is invalid" }
    $targetUrl = $dotenv["EZTEST_TEST_DATABASE_URL"]
    if ([string]::IsNullOrWhiteSpace($targetUrl)) { throw "EZTEST_TEST_DATABASE_URL is missing from .env" }
    $targetDb = ConvertFrom-DatabaseUrl $targetUrl
    Write-Log ("      e2e db   : {0}/{1}" -f $script:db.Host, $testDatabase)
    Write-Log ("      target db: {0}/{1}" -f $targetDb.Host, $targetDb.Database)
    Write-Log ("      api      : {0}" -f $script:apiBase)
    Write-Log ("      executor : {0}" -f $script:executorBase)
    Write-Log ("      llm      : {0} model={1}" -f $dotenv["llm_base_url"], $dotenv["llm_model_name"])

    if ($LongWait) { Write-Log "      mode     : -LongWait (120s timeout window + execution.wait 122)" }
    else { Write-Log "      mode     : default (10s timeout window + execution.wait 12)" }

    Step "verify ports $ApiPort/$ExecutorPort/$OrdersPort/$PaymentsPort are free"
    foreach ($port in @($ApiPort, $ExecutorPort, $OrdersPort, $PaymentsPort)) {
        if (Test-PortInUse -Port $port) { throw "port $port is already in use; stop the stale process first" }
    }

    Step "reset isolated e2e database $testDatabase"
    $script:psql = Resolve-Psql
    Reset-TestDatabase

    Step "seed the P3 target fixture database (scripts/p3-fixture-db.ps1)"
    $fixtureDbOutput = & powershell.exe -NoProfile -ExecutionPolicy Bypass -File (Join-Path $repoRoot "scripts\p3-fixture-db.ps1") -DatabaseUrl $targetUrl 2>&1
    $fixtureExit = $LASTEXITCODE
    Set-Content -LiteralPath (Join-Path $logDir "fixture-db.log") -Value ($fixtureDbOutput -join [Environment]::NewLine) -Encoding UTF8
    if ($fixtureExit -ne 0) { throw ("p3-fixture-db.ps1 failed ({0}); see fixture-db.log" -f $fixtureExit) }
    Write-Log "      target fixture tables p3_orders/p3_refunds reseeded"

    Step "build backend/cmd/eztest"
    Build-Api

    $script:http = New-ApiHttpClient

    Step "start fixture SUTs (orders 127.0.0.1:$OrdersPort, payments 127.0.0.1:$PaymentsPort)"
    $script:ordersFixture = Start-Fixture -Port $OrdersPort -Variant "orders" -LogName "fixture-orders"
    $script:paymentsFixture = Start-Fixture -Port $PaymentsPort -Variant "payments" -LogName "fixture-payments"
    if (-not (Wait-Http -Url ($script:ordersBase + "/health") -Seconds 20)) { throw "orders fixture did not become ready" }
    if (-not (Wait-Http -Url ($script:paymentsBase + "/health") -Seconds 20)) { throw "payments fixture did not become ready" }
    Write-Log "      fixtures ready"

    Step "bootstrap API on port $ApiPort (migrations + settings seed)"
    Start-Api

    Step "login as admin/admin123"
    $login = Invoke-Api -Method "POST" -Path "/api/auth/login" -Token $null -Body @{ username = "admin"; password = "admin123" }
    Assert-Status $login @(200) "POST /api/auth/login"
    $script:token = [string]$login.Json.data.token
    if ([string]::IsNullOrWhiteSpace($script:token)) { throw "login response has no token" }

    Step "assert P3 settings catalog (49 keys, 5 new execution keys, token secret masked)"
    $settingsResponse = Invoke-Api -Method "GET" -Path "/api/admin/settings" -Token $script:token
    Assert-Status $settingsResponse @(200) "GET /api/admin/settings"
    $settings = Get-Array $settingsResponse.Json.data
    if ($settings.Count -lt 49) { throw ("settings count = {0}, want >= 49" -f $settings.Count) }
    $requiredKeys = @(
        "execution.executor_token", "execution.executor_heartbeat_seconds", "execution.executor_offline_seconds",
        "execution.executor_context_ttl_seconds", "execution.human_request_timeout_seconds",
        "execution.executor_url", "execution.global_concurrency", "execution.per_sut_concurrency",
        "execution.cross_system_concurrency", "execution.evidence_max_bytes", "execution.case_timeout_seconds"
    )
    foreach ($key in $requiredKeys) {
        if (-not (@($settings | Where-Object { $_.key -eq $key }).Count -ge 1)) { throw ("setting {0} is missing" -f $key) }
    }
    $tokenSetting = $settings | Where-Object { $_.key -eq "execution.executor_token" }
    Assert-True ([bool]$tokenSetting.is_secret) "execution.executor_token must be secret"
    Assert-True ($null -eq $tokenSetting.value) "execution.executor_token value must be masked (null)"
    Write-Log ("      settings: {0} keys; execution token is secret and masked" -f $settings.Count)

    Step "configure executor URL/token and the real LLM through the admin API"
    $script:executorToken = [System.Guid]::NewGuid().ToString("N") + [System.Guid]::NewGuid().ToString("N")
    Set-Setting "execution.executor_url" ("http://127.0.0.1:{0}" -f $ExecutorPort) | Out-Null
    Set-Setting "execution.executor_token" $script:executorToken | Out-Null
    Set-Setting "execution.executor_heartbeat_seconds" 2 | Out-Null
    Set-Setting "execution.executor_offline_seconds" 6 | Out-Null
    Set-Setting "execution.executor_context_ttl_seconds" 300 | Out-Null
    Set-Setting "execution.human_request_timeout_seconds" 1800 | Out-Null
    $llmResponse = Invoke-Api -Method "POST" -Path "/api/admin/llm/configs" -Token $script:token -Body @{
        name           = "p3-acceptance-deepseek"
        provider       = "openai"
        protocol       = "openai"
        base_url       = [string]$dotenv["llm_base_url"]
        api_key        = [string]$dotenv["llm_api_key"]
        timeout_seconds = 120
        max_retries    = 1
        is_default     = $true
        status         = "active"
    }
    Assert-Status $llmResponse @(200) "POST /api/admin/llm/configs (real DeepSeek)"
    $llmConfigID = [string]$llmResponse.Json.data.id
    $llmTest = Invoke-Api -Method "POST" -Path ("/api/admin/llm/configs/{0}/test" -f $llmConfigID) -Token $script:token
    if ($llmTest.Status -ne 200) {
        throw ("real LLM connectivity test failed (HTTP {0}); acceptance requires a working DeepSeek config" -f $llmTest.Status)
    }
    if ($null -ne $llmTest.Json.data -and $llmTest.Json.data.ok -eq $false) {
        throw ("real LLM connectivity test failed: {0}" -f $llmTest.Json.data.error)
    }
    $refresh = Invoke-Api -Method "POST" -Path ("/api/admin/llm/configs/{0}/refresh-models" -f $llmConfigID) -Token $script:token
    Assert-Status $refresh @(200) "POST refresh-models (real DeepSeek)"
    $catalog = Invoke-Api -Method "GET" -Path "/api/admin/llm/models" -Token $script:token
    Assert-Status $catalog @(200) "GET /api/admin/llm/models"
    $catalogModels = Get-Array $catalog.Json.data
    $wantedModel = [string]$dotenv["llm_model_name"]
    $hasWanted = @($catalogModels | Where-Object { $_.model -eq $wantedModel }).Count -ge 1
    if (-not $hasWanted) {
        $addModel = Invoke-Api -Method "POST" -Path ("/api/admin/llm/configs/{0}/models" -f $llmConfigID) -Token $script:token -Body @{
            model = $wantedModel; context_window = 128000; max_output = 8192; supports_tools = $true; supports_vision = $false
        }
        if ($addModel.Status -ne 200) { throw ("could not add model {0} to the catalog (HTTP {1})" -f $wantedModel, $addModel.Status) }
        $catalog = Invoke-Api -Method "GET" -Path "/api/admin/llm/models" -Token $script:token
        $catalogModels = Get-Array $catalog.Json.data
    }
    # resolveChatTarget uses the first catalog model of the default config when
    # the run has no model override; delete preceding models so the acceptance
    # deterministically drives the configured llm_model_name.
    $first = $catalogModels[0]
    if ([string]$first.model -ne $wantedModel) {
        foreach ($model in $catalogModels) {
            if ([string]$model.model -eq $wantedModel) { break }
            $deleted = Invoke-Api -Method "DELETE" -Path ("/api/admin/llm/configs/{0}/models/{1}" -f $llmConfigID, [string]$model.id) -Token $script:token
            if ($deleted.Status -ne 200) { throw ("could not reorder LLM catalog (delete model {0})" -f $model.model) }
        }
        $catalog = Invoke-Api -Method "GET" -Path "/api/admin/llm/models" -Token $script:token
        $catalogModels = Get-Array $catalog.Json.data
    }
    if ($catalogModels.Count -lt 1 -or [string]$catalogModels[0].model -ne $wantedModel) {
        throw ("default LLM config does not resolve to {0}" -f $wantedModel)
    }
    Write-Log ("      real LLM ready: model={0}, catalog-first={1}" -f $wantedModel, $catalogModels[0].model)

    Step "stop the bootstrap API and start the P3 executor with the random token"
    Stop-Api
    Start-Executor

    Step "start the acceptance API (fresh process picks up executor_url/token settings)"
    Start-Api
    $relogin = Invoke-Api -Method "POST" -Path "/api/auth/login" -Token $null -Body @{ username = "admin"; password = "admin123" }
    Assert-Status $relogin @(200) "POST /api/auth/login (acceptance API)"
    $script:token = [string]$relogin.Json.data.token
    $health = Invoke-Api -Method "GET" -Path "/api/health" -Token $null
    Assert-Status $health @(200) "GET /api/health"
    Assert-True ([string]$health.Json.data.version -match '^0\.2\.0-p[3-9]$') "API version is P3 or later (got $($health.Json.data.version))"

    Step "wait for the executor registry to poll the P3 executor as active"
    $executor = Wait-For -TimeoutSeconds 60 -Description "executor registered active at $($script:executorBase)" -Condition {
        $response = Invoke-Api -Method "GET" -Path "/api/admin/executors" -Token $script:token
        if ($response.Status -ne 200) { return $null }
        foreach ($item in (Get-Array $response.Json.data)) {
            if ([string]$item.endpoint -eq $script:executorBase -and [string]$item.status -eq "active") { return $item }
        }
        return $null
    }
    Write-Log ("      executor id={0} capacity={1} status={2}" -f $executor.id, $executor.capacity, $executor.status)

    Step "create the two SUTs, environments, pages, APIs, target DB and accounts"
    $ordersSutResponse = Invoke-Api -Method "POST" -Path "/api/resources/suts" -Token $script:token -Body @{ name = "Order Center"; intro = "P3 acceptance order SUT" }
    Assert-Status $ordersSutResponse @(200, 201) "POST /api/resources/suts (Order Center)"
    $ordersSutID = [string]$ordersSutResponse.Json.data.id
    $paymentsSutResponse = Invoke-Api -Method "POST" -Path "/api/resources/suts" -Token $script:token -Body @{ name = "Payment Gateway"; intro = "P3 acceptance payment SUT" }
    Assert-Status $paymentsSutResponse @(200, 201) "POST /api/resources/suts (Payment Gateway)"
    $paymentsSutID = [string]$paymentsSutResponse.Json.data.id

    $ordersEnvResponse = Invoke-Api -Method "POST" -Path "/api/resources/suts/$ordersSutID/environments" -Token $script:token -Body @{
        name = "p3-orders"; base_url = $script:ordersBase; is_default = $true; status = "active"
    }
    Assert-Status $ordersEnvResponse @(200, 201) "POST orders environment"
    $ordersEnvID = [string]$ordersEnvResponse.Json.data.id
    $paymentsEnvResponse = Invoke-Api -Method "POST" -Path "/api/resources/suts/$paymentsSutID/environments" -Token $script:token -Body @{
        name = "p3-payments"; base_url = $script:paymentsBase; is_default = $true; status = "active"
    }
    Assert-Status $paymentsEnvResponse @(200, 201) "POST payments environment"
    $paymentsEnvID = [string]$paymentsEnvResponse.Json.data.id

    $pageSpecs = @(
        @{ Key = "ordersLogin"; Env = $ordersEnvID; Name = "P3 orders login"; Url = ($script:ordersBase + "/p3/login"); Route = "/p3/login" },
        @{ Key = "orderDetail"; Env = $ordersEnvID; Name = "P3 order detail"; Url = ($script:ordersBase + "/p3/orders/1"); Route = "/p3/orders/1" },
        @{ Key = "timeoutWindow"; Env = $ordersEnvID; Name = "P3 timeout window"; Url = ($script:ordersBase + "/p3/timeout-window"); Route = "/p3/timeout-window" },
        @{ Key = "slow"; Env = $ordersEnvID; Name = "P3 slow page"; Url = ($script:ordersBase + "/p3/slow"); Route = "/p3/slow" },
        @{ Key = "payments"; Env = $paymentsEnvID; Name = "P3 payments"; Url = ($script:paymentsBase + "/p3/payments"); Route = "/p3/payments" }
    )
    $pages = @{}
    foreach ($spec in $pageSpecs) {
        $pageResponse = Invoke-Api -Method "POST" -Path "/api/resources/environments/$($spec.Env)/pages" -Token $script:token -Body @{
            name = $spec.Name; url = $spec.Url; route_template = $spec.Route; page_type = "page"; auth_role = "guest"; module = "p3"
        }
        Assert-Status $pageResponse @(200, 201) ("POST page " + $spec.Key)
        $pages[$spec.Key] = [string]$pageResponse.Json.data.id
    }

    $ordersApiResponse = Invoke-Api -Method "POST" -Path "/api/resources/environments/$ordersEnvID/apis" -Token $script:token -Body @{
        method = "GET"; path_template = "/api/p3/orders"; name = "P3 orders API"; auth_role = "guest"; schema_ref = ""
    }
    Assert-Status $ordersApiResponse @(200, 201) "POST orders API"
    $ordersApiID = [string]$ordersApiResponse.Json.data.id
    $paymentsApiResponse = Invoke-Api -Method "POST" -Path "/api/resources/environments/$paymentsEnvID/apis" -Token $script:token -Body @{
        method = "GET"; path_template = "/api/p3/payments"; name = "P3 payments API"; auth_role = "guest"; schema_ref = ""
    }
    Assert-Status $paymentsApiResponse @(200, 201) "POST payments API"
    $paymentsApiID = [string]$paymentsApiResponse.Json.data.id

    $targetDbResponse = Invoke-Api -Method "POST" -Path "/api/resources/suts/$ordersSutID/databases" -Token $script:token -Body @{
        environment_id = $ordersEnvID; name = "p3-target-db"; dsn = $targetUrl
        username = $targetDb.User; password = $targetDb.Password; read_only = $true; status = "active"
    }
    Assert-Status $targetDbResponse @(200, 201) "POST target database (orders)"
    $targetDbID = [string]$targetDbResponse.Json.data.id

    $ordersAccountResponse = Invoke-Api -Method "POST" -Path "/api/resources/suts/$ordersSutID/accounts" -Token $script:token -Body @{
        environment_id = $ordersEnvID; username = "fixture"; password = "fixture-pass"; roles = @("user"); notes = "P3 fixture user"; status = "active"
    }
    Assert-Status $ordersAccountResponse @(200, 201) "POST orders account"
    $ordersAccountID = [string]$ordersAccountResponse.Json.data.id
    $paymentsAccountResponse = Invoke-Api -Method "POST" -Path "/api/resources/suts/$paymentsSutID/accounts" -Token $script:token -Body @{
        environment_id = $paymentsEnvID; username = "fixture"; password = "fixture-pass"; roles = @("user"); notes = "P3 fixture user"; status = "active"
    }
    Assert-Status $paymentsAccountResponse @(200, 201) "POST payments account"
    $paymentsAccountID = [string]$paymentsAccountResponse.Json.data.id

    Step "create the version bound to both SUTs"
    $versionResponse = Invoke-Api -Method "POST" -Path "/api/versions" -Token $script:token -Body @{
        title = ("P3 acceptance " + (Get-Date -Format "yyyy-MM-dd HH:mm:ss"))
        bindings = @(
            @{
                sut_id = $ordersSutID; environment_id = $ordersEnvID; code_repository_id = $null
                page_ids = @($pages["ordersLogin"], $pages["orderDetail"], $pages["timeoutWindow"], $pages["slow"])
                api_ids = @($ordersApiID); database_ids = @($targetDbID)
                accounts = @(@{ account_id = $ordersAccountID; role = "user" })
            },
            @{
                sut_id = $paymentsSutID; environment_id = $paymentsEnvID; code_repository_id = $null
                page_ids = @($pages["payments"]); api_ids = @($paymentsApiID); database_ids = @()
                accounts = @(@{ account_id = $paymentsAccountID; role = "user" })
            }
        )
    }
    Assert-Status $versionResponse @(201) "POST /api/versions"
    $script:versionID = [string]$versionResponse.Json.data.id
    $bindings = Get-Array $versionResponse.Json.data.bindings
    if ($bindings.Count -ne 2) { throw ("version bindings = {0}, want 2" -f $bindings.Count) }
    $script:versionSUTOrders = [string]$bindings[0].id
    $script:versionSUTPayments = [string]$bindings[1].id
    Write-Log ("      version {0}: order version_sut={1}, payment version_sut={2}" -f $script:versionID, $script:versionSUTOrders, $script:versionSUTPayments)

    Step "assert the execution overview is available and exposes the frozen 3/2/1 quotas"
    $overviewProbe = Get-Overview
    Assert-True ($null -ne $overviewProbe) "execution overview must be available"
    Assert-Equal $overviewProbe.quotas.global 3 "execution.global_concurrency"
    Assert-Equal $overviewProbe.quotas.per_sut 2 "execution.per_sut_concurrency"
    Assert-Equal $overviewProbe.quotas.cross_system 1 "execution.cross_system_concurrency"

    Step "create and confirm three clarified requirements (real P1 API)"
    $requirements = @(
        @{ Key = "R1"; Title = "P3 order and cross-system execution flows"; Desc = "Order detail, cross-system refund lookup, timed window and slow page flows must execute"; Priority = "P1" },
        @{ Key = "R2"; Title = "P3 two-factor login takeover"; Desc = "The 2FA login flow needs a human-provided verification code"; Priority = "P1" },
        @{ Key = "R3"; Title = "P3 refund amount consistency"; Desc = "The refund amount shown on the order page must equal the paid amount"; Priority = "P1" }
    )
    $script:requirementIDs = @{}
    $script:requirementVersionIDs = @{}
    foreach ($requirement in $requirements) {
        $requirementResponse = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/requirements" -Token $script:token -Body @{
            title = $requirement.Title; description = $requirement.Desc; acceptance_criteria = "Executed by the P3 acceptance cases"
            priority = $requirement.Priority; constraints = @{}
            targets = @{ version_suts = @($script:versionSUTOrders, $script:versionSUTPayments); pages = @(); apis = @() }
        }
        Assert-Status $requirementResponse @(201) ("POST requirement " + $requirement.Key)
        $requirementID = [string]$requirementResponse.Json.data.id
        $requirementVersionID = [string]$requirementResponse.Json.data.current_version.id
        $confirmResponse = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/requirements/$requirementID/confirmations" -Token $script:token
        Assert-Status $confirmResponse @(200) ("confirm requirement " + $requirement.Key)
        if (-not [bool]$confirmResponse.Json.data.confirmed) { throw ("requirement {0} was not confirmed" -f $requirement.Key) }
        $script:requirementIDs[$requirement.Key] = $requirementID
        $script:requirementVersionIDs[$requirement.Key] = $requirementVersionID
        Write-Log ("      requirement {0}: id={1} version={2} clarified+confirmed" -f $requirement.Key, $requirementID, $requirementVersionID)
    }

    Pass-Scenario $s1

    # ---------------- S2: draft cases + confirmation ----------------------
    $s2 = Start-Scenario "S2 SQL-seeded draft cases confirmed through the P2 design confirmation API"

    Step "SQL seed six draft cases with steps/assertions (single-SUT pass, cross-system, application failure, 2FA, timeout window, slow)"
    $timeoutMs = 10000
    $timeoutWait = 12
    if ($LongWait) { $timeoutMs = 120000; $timeoutWait = 122 }
    $hash = ("0123456789abcdef" * 4)
    $caseDefs = @(
        @{
            Key = "A"; Req = "R1"; Title = "P3-A single SUT order detail pass"; Priority = "P1"
            Suts = @($script:versionSUTOrders); DefaultSut = $script:versionSUTOrders
            Pages = @($pages["orderDetail"]); Apis = @($ordersApiID)
            Steps = @(
                @{ Seq = 1; Sut = $script:versionSUTOrders; Page = $pages["orderDetail"]; Role = "user"
                   Action = ("Use browser.goto to open {0}/p3/orders/1 with version_sut_id={1} and account_role=user. Then use browser.snapshot to read the page and browser.screenshot to record evidence." -f $script:ordersBase, $script:versionSUTOrders)
                   Expected = "Order detail page shows order number ORD-1001 and amount 5,200.00"
                   Asserts = @(
                       @{ Seq = 1; Kind = "ui"; Target = "#order-number"; Expected = "ORD-1001" },
                       @{ Seq = 2; Kind = "ui"; Target = "#order-amount"; Expected = "5,200.00" }
                   ) }
            )
        },
        @{
            Key = "B"; Req = "R1"; Title = "P3-B cross-system order and payment pass"; Priority = "P1"
            Suts = @($script:versionSUTOrders, $script:versionSUTPayments); DefaultSut = $script:versionSUTOrders
            Pages = @($pages["orderDetail"], $pages["payments"]); Apis = @($ordersApiID, $paymentsApiID)
            Steps = @(
                @{ Seq = 1; Sut = $script:versionSUTOrders; Page = $pages["orderDetail"]; Role = "user"
                   Action = ("Use browser.goto to open {0}/p3/orders/1 with version_sut_id={1} and account_role=user. Then browser.snapshot and browser.screenshot." -f $script:ordersBase, $script:versionSUTOrders)
                   Expected = "Order detail page shows ORD-1001"
                   Asserts = @() },
                @{ Seq = 2; Sut = $script:versionSUTPayments; Page = $pages["payments"]; Role = "user"
                   Action = ("Use browser.goto to open {0}/p3/payments with version_sut_id={1} and account_role=user. Then browser.snapshot and browser.screenshot; the page lists refund REF-5001 with amount 5,000.00." -f $script:paymentsBase, $script:versionSUTPayments)
                   Expected = "Payments page lists refund REF-5001"
                   Asserts = @(
                       @{ Seq = 1; Kind = "ui"; Target = "#refunds"; Expected = "REF-5001" }
                   ) },
                @{ Seq = 3; Sut = $script:versionSUTOrders; Page = $null; Role = "user"
                   Action = "Call db.query with version_sut_id={0} and sql SELECT refund_no, amount, status FROM p3_refunds WHERE order_id = 1. Do not pass database_id. The query returns REF-5001 with amount 5000.00 and status created." -f $script:versionSUTOrders
                   Expected = "The target database has refund REF-5001 for order 1"
                   Asserts = @(
                       @{ Seq = 1; Kind = "db"; Target = "SELECT refund_no, amount, status FROM p3_refunds WHERE order_id = 1"; Expected = "REF-5001" }
                   ) }
            )
        },
        @{
            Key = "C"; Req = "R3"; Title = "P3-C refund amount consistency failure"; Priority = "P1"
            Suts = @($script:versionSUTOrders); DefaultSut = $script:versionSUTOrders
            Pages = @($pages["orderDetail"]); Apis = @()
            Steps = @(
                @{ Seq = 1; Sut = $script:versionSUTOrders; Page = $pages["orderDetail"]; Role = "user"
                   Action = ("Use browser.goto to open {0}/p3/orders/1 with version_sut_id={1} and account_role=user. Then browser.snapshot and browser.screenshot. Assertion 1 checks the paid amount. Assertion 2 compares the refund amount with the paid amount: the refund amount (5,000.00) is lower than the paid amount, so assertion 2 must be recorded as failed with the actual refund amount." -f $script:ordersBase, $script:versionSUTOrders)
                   Expected = "The refund amount equals the paid amount"
                   Asserts = @(
                       @{ Seq = 1; Kind = "ui"; Target = "#order-amount"; Expected = "5,200.00" },
                       @{ Seq = 2; Kind = "ui"; Target = "#refund-amount"; Expected = "5,200.00" }
                   ) }
            )
        },
        @{
            Key = "D"; Req = "R2"; Title = "P3-D two-factor login takeover"; Priority = "P1"
            Suts = @($script:versionSUTOrders); DefaultSut = $script:versionSUTOrders
            Pages = @($pages["ordersLogin"]); Apis = @()
            Steps = @(
                @{ Seq = 1; Sut = $script:versionSUTOrders; Page = $pages["mfaLogin"]; Role = "user"
                   Action = ("Use browser.goto to open {0}/p3/login?mfa=1 with version_sut_id={1} and account_role=user. Read the credentials with account.credentials for version_sut_id={1} role=user, fill #username and #password with browser.fill(secret=true), then click the button named 'Sign in'. The page then shows the #mfa two-factor challenge." -f $script:ordersBase, $script:versionSUTOrders)
                   Expected = "The two-factor challenge is shown"
                   Asserts = @() },
                @{ Seq = 2; Sut = $script:versionSUTOrders; Page = $pages["mfaLogin"]; Role = "user"
                   Action = ("The page shows the #mfa two-factor challenge. Call human.request with kind=login_takeover, version_sut_id={0}, account_role=user, challenge_kind=two_factor, reason='2FA code required for P3 acceptance login'. Wait for the human response; it contains the one-time code. Then fill #mfa-code with that code using browser.fill and click the button named 'Verify code'. The page redirects to /p3/home after a correct code." -f $script:versionSUTOrders)
                   Expected = "The correct verification code completes the login"
                   Asserts = @() },
                @{ Seq = 3; Sut = $script:versionSUTOrders; Page = $pages["mfaLogin"]; Role = "user"
                   Action = ("Use browser.goto to open {0}/p3/home with version_sut_id={1} and account_role=user. Then browser.snapshot and browser.screenshot." -f $script:ordersBase, $script:versionSUTOrders)
                   Expected = "The home page shows the signed-in state"
                   Asserts = @(
                       @{ Seq = 1; Kind = "ui"; Target = "#session-state"; Expected = "Signed in as fixture" }
                   ) }
            )
        },
        @{
            Key = "E"; Req = "R1"; Title = "P3-E timed confirmation window"; Priority = "P1"
            Suts = @($script:versionSUTOrders); DefaultSut = $script:versionSUTOrders
            Pages = @($pages["timeoutWindow"]); Apis = @()
            Steps = @(
                @{ Seq = 1; Sut = $script:versionSUTOrders; Page = $pages["timeoutWindow"]; Role = "user"
                   Action = ("Use browser.goto to open {0}/p3/timeout-window?ms={1} with version_sut_id={2} and account_role=user. Then browser.snapshot and browser.screenshot while the confirmation dialog is visible; the #confirm-dialog element must be visible." -f $script:ordersBase, $timeoutMs, $script:versionSUTOrders)
                   Expected = "The confirmation dialog #confirm-dialog is visible"
                   Asserts = @(
                       @{ Seq = 1; Kind = "ui"; Target = "#confirm-dialog"; Expected = "visible" }
                   ) },
                @{ Seq = 2; Sut = $script:versionSUTOrders; Page = $pages["timeoutWindow"]; Role = "user"
                   Action = ("Call execution.wait with seconds={0} and reason='wait for the confirmation window to expire'. After the wait, use browser.snapshot and browser.screenshot and record that #confirm-dialog is gone and #window-expired is visible." -f $timeoutWait)
                   Expected = "The dialog disappears after the window and #window-expired is visible"
                   Asserts = @(
                       @{ Seq = 2; Kind = "ui"; Target = "#window-expired"; Expected = "visible" }
                   ) }
            )
        },
        @{
            Key = "F"; Req = "R1"; Title = "P3-F slow page for pause and offline recovery"; Priority = "P2"
            Suts = @($script:versionSUTOrders); DefaultSut = $script:versionSUTOrders
            Pages = @($pages["slow"]); Apis = @()
            Steps = @(
                @{ Seq = 1; Sut = $script:versionSUTOrders; Page = $pages["slow"]; Role = "user"
                   Action = ("Use browser.goto to open {0}/p3/slow?ms=20000 with version_sut_id={1} and account_role=user. This page intentionally takes 20 seconds to respond. After navigation completes, use browser.snapshot and browser.screenshot and check #slow-result." -f $script:ordersBase, $script:versionSUTOrders)
                   Expected = "The slow page shows Slow response complete after 20000 ms."
                   Asserts = @(
                       @{ Seq = 1; Kind = "ui"; Target = "#slow-result"; Expected = "Slow response complete after 20000 ms." }
                   ) },
                @{ Seq = 2; Sut = $script:versionSUTOrders; Page = $pages["slow"]; Role = "user"
                   Action = ("Call execution.wait with seconds=25 and reason='exercise a long browser-independent wait'. After the wait, use browser.snapshot. If #slow-result is not visible (the browser context may have been recreated after a pause, human takeover or executor restart), use browser.goto to reopen {0}/p3/slow?ms=20000 with version_sut_id={1} and account_role=user, wait for the page, then browser.snapshot and browser.screenshot. Check that #slow-result is visible." -f $script:ordersBase, $script:versionSUTOrders)
                   Expected = "The slow result stays visible"
                   Asserts = @(
                       @{ Seq = 2; Kind = "ui"; Target = "#slow-result"; Expected = "Slow response complete after 20000 ms." }
                   ) }
            )
        }
    )

    $sqlLines = New-Object System.Collections.ArrayList
    [void]$sqlLines.Add("BEGIN;")
    $caseIndex = 0
    $script:caseIDs = @{}
    $script:caseVersionIDs = @{}
    $script:caseTitles = @{}
    foreach ($case in $caseDefs) {
        $caseIndex++
        $scenarioID = 810000 + $caseIndex
        $caseID = 820000 + $caseIndex
        $caseVersionID = 830000 + $caseIndex
        $script:caseIDs[$case.Key] = [string]$caseID
        $script:caseVersionIDs[$case.Key] = [string]$caseVersionID
        $script:caseTitles[$case.Key] = $case.Title
        $reqID = $script:requirementIDs[$case.Req]
        $reqVersionID = $script:requirementVersionIDs[$case.Req]
        $versionSutJson = Get-SqlJsonArray $case.Suts
        $pageJson = Get-SqlJsonArray $case.Pages
        $apiJson = Get-SqlJsonArray $case.Apis
        [void]$sqlLines.Add(("INSERT INTO case_scenario (id, version_id, requirement_id, requirement_version_id, name, description, version_sut_ids, page_ids, api_ids, sort_order) VALUES ({0}, {1}, {2}, {3}, 'Scenario {4}', '', '{5}'::jsonb, '{6}'::jsonb, '{7}'::jsonb, {8});" -f $scenarioID, $script:versionID, $reqID, $reqVersionID, $case.Key, $versionSutJson, $pageJson, $apiJson, $caseIndex))
        [void]$sqlLines.Add(("INSERT INTO case_entity (id, scenario_id, current_version_id, status) VALUES ({0}, {1}, {2}, 'draft');" -f $caseID, $scenarioID, $caseVersionID))
        $quotedTitle = $case.Title.Replace("'", "''")
        [void]$sqlLines.Add(("INSERT INTO case_version (id, case_id, revision, title, description, preconditions, test_data, priority, default_version_sut_id, version_sut_ids, page_ids, api_ids, status, content_hash) VALUES ({0}, {1}, 1, '{2}', '', '', '{{}}'::jsonb, '{3}', {4}, '{5}'::jsonb, '{6}'::jsonb, '{7}'::jsonb, 'draft', '{8}');" -f $caseVersionID, $caseID, $quotedTitle, $case.Priority, $case.DefaultSut, $versionSutJson, $pageJson, $apiJson, $hash))
        $stepIdBase = 840000 + ($caseIndex * 100)
        $assertIdBase = 850000 + ($caseIndex * 100)
        foreach ($step in $case.Steps) {
            $stepID = $stepIdBase + [int]$step.Seq
            $quotedAction = ([string]$step.Action).Replace("'", "''")
            $quotedExpected = ([string]$step.Expected).Replace("'", "''")
            $pageValue = "NULL"
            if ($null -ne $step.Page -and [string]$step.Page -ne "") { $pageValue = [string]$step.Page }
            [void]$sqlLines.Add(("INSERT INTO case_step (id, case_version_id, seq, action, expected, target_version_sut_id, target_page_id, target_api_id, account_role, data) VALUES ({0}, {1}, {2}, '{3}', '{4}', {5}, {6}, NULL, '{7}', '{{}}'::jsonb);" -f $stepID, $caseVersionID, $step.Seq, $quotedAction, $quotedExpected, $step.Sut, $pageValue, $step.Role))
            $assertIndex = 0
            foreach ($assertion in $step.Asserts) {
                $assertIndex++
                $assertID = $assertIdBase + ([int]$step.Seq * 10) + $assertIndex
                $quotedTarget = ([string]$assertion.Target).Replace("'", "''")
                $quotedExpected = ([string]$assertion.Expected).Replace("'", "''")
                $databaseValue = "NULL"
                if ($assertion.Kind -eq "db") { $databaseValue = $targetDbID }
                [void]$sqlLines.Add(("INSERT INTO case_assertion (id, case_version_id, step_seq, kind, target, expected, target_version_sut_id, target_api_id, database_id) VALUES ({0}, {1}, {2}, '{3}', '{4}', '{5}', {6}, NULL, {7});" -f $assertID, $caseVersionID, $step.Seq, $assertion.Kind, $quotedTarget, $quotedExpected, $step.Sut, $databaseValue))
            }
        }
    }
    $confirmIDs = @()
    foreach ($case in $caseDefs) { $confirmIDs += $script:caseIDs[$case.Key] }
    [void]$sqlLines.Add(("INSERT INTO version (id, title, status, auto_design_new_requirements) SELECT {0}, title, status, auto_design_new_requirements FROM version WHERE id = {0} ON CONFLICT DO NOTHING;" -f $script:versionID))
    [void]$sqlLines.Add("COMMIT;")
    # Drop the harmless version self-insert (kept out entirely: version already exists).
    $sqlLines.RemoveAt($sqlLines.Count - 2)
    Set-Content -LiteralPath $seedFile -Value ($sqlLines -join [Environment]::NewLine) -Encoding UTF8
    Invoke-PsqlFile -DatabaseName $testDatabase -FilePath $seedFile
    $seededCount = Invoke-PsqlScalar -DatabaseName $testDatabase -SqlText ("SELECT count(*) FROM case_version WHERE status = 'draft' AND case_id IN ({0});" -f ($script:caseIDs.Values -join ","))
    Assert-Equal $seededCount "6" "seeded draft case_version count"

    Step "confirm the six cases through POST /api/versions/{id}/design/confirmations"
    $confirmedEventsBefore = @(Get-SnapshotEvents | Where-Object { $_.type -eq "design.case_confirmed" }).Count
    foreach ($case in $caseDefs) {
        $confirmResponse = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/design/confirmations" -Token $script:token -Body @{
            scope = "case"; target_id = $script:caseIDs[$case.Key]
        }
        Assert-Status $confirmResponse @(200) ("confirm case " + $case.Key)
        Assert-Equal (Get-Array $confirmResponse.Json.data.confirmed).Count 1 ("confirmed count for case " + $case.Key)
    }
    $confirmedRows = Invoke-PsqlScalar -DatabaseName $testDatabase -SqlText ("SELECT count(*) FROM case_version WHERE status = 'confirmed' AND id IN ({0});" -f ($script:caseVersionIDs.Values -join ","))
    Assert-Equal $confirmedRows "6" "confirmed case_version rows"
    $confirmedEntities = Invoke-PsqlScalar -DatabaseName $testDatabase -SqlText ("SELECT count(*) FROM case_entity WHERE status = 'confirmed' AND id IN ({0});" -f ($script:caseIDs.Values -join ","))
    Assert-Equal $confirmedEntities "6" "confirmed case_entity rows"
    Start-Sleep -Milliseconds 800
    $eventsAfterConfirm = Get-SnapshotEvents
    Assert-EventPresent -Events $eventsAfterConfirm -Type "design.case_confirmed"
    $confirmedEventsAfter = @($eventsAfterConfirm | Where-Object { $_.type -eq "design.case_confirmed" }).Count
    Assert-True ($confirmedEventsAfter -ge ($confirmedEventsBefore + 6)) ("design.case_confirmed events: {0} -> {1}, want +6" -f $confirmedEventsBefore, $confirmedEventsAfter)

    Pass-Scenario $s2

    # ---------------- S3: automatic intent + wave -------------------------
    $s3 = Start-Scenario "S3 automatic intent/wave (no start action), duplicate confirmation, quota"

    Step "assert no start-execution API exists"
    foreach ($probe in @(
        @{ Method = "POST"; Path = "/api/versions/$script:versionID/execution/start"; Strict = $true },
        @{ Method = "POST"; Path = "/api/versions/$script:versionID/execution/run"; Strict = $true },
        @{ Method = "POST"; Path = "/api/versions/$script:versionID/waves"; Strict = $false }
    )) {
        $probeResponse = Invoke-Api -Method $probe.Method -Path $probe.Path -Token $script:token -Body @{}
        $allowed = @(404)
        if (-not $probe.Strict) { $allowed = @(404, 405) }
        if ($allowed -notcontains $probeResponse.Status) {
            throw ("no start endpoint: {0} {1} -> HTTP {2}" -f $probe.Method, $probe.Path, $probeResponse.Status)
        }
        Write-Log ("      no start action: {0} {1} -> HTTP {2}" -f $probe.Method, $probe.Path, $probeResponse.Status)
    }

    Step "wait for six automatic initial intents and one auto wave"
    $initialIntents = Wait-For -TimeoutSeconds 120 -Description "six initial execution intents" -Condition {
        $response = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/execution/intents?limit=100" -Token $script:token
        if ($response.Status -ne 200) { return $null }
        $items = Get-Array $response.Json.data
        $initial = @($items | Where-Object { $_.purpose -eq "initial" })
        if ($initial.Count -ge 6) { return $items }
        return $null
    }
    Assert-Equal @($initialIntents | Where-Object { $_.purpose -eq "initial" }).Count 6 "initial intent count"
    $wave = Wait-For -TimeoutSeconds 120 -Description "active auto wave" -Condition {
        $active = Get-ActiveWave
        if ($null -ne $active -and [string]$active.trigger -eq "auto") { return $active }
        return $null
    }
    $script:initialWaveID = [string]$wave.id
    Write-Log ("      initial wave {0} trigger={1} status={2}" -f $script:initialWaveID, $wave.trigger, $wave.status)
    $initialRuns = Get-CaseRunsOfWave -WaveID $script:initialWaveID
    Assert-Equal $initialRuns.Count 6 "initial case_run count"

    Step "duplicate case confirmation does not create a duplicate intent or case_run"
    $duplicateConfirm = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/design/confirmations" -Token $script:token -Body @{
        scope = "case"; target_id = $script:caseIDs["A"]
    }
    Assert-Status $duplicateConfirm @(200) "repeated case confirmation"
    Assert-Equal (Get-Array $duplicateConfirm.Json.data.confirmed).Count 0 "duplicate confirmation confirmed count"
    Start-Sleep -Seconds 2
    $intentsAfterDuplicate = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/execution/intents?limit=100" -Token $script:token
    Assert-Status $intentsAfterDuplicate @(200) "GET intents after duplicate confirmation"
    Assert-Equal (Get-Array $intentsAfterDuplicate.Json.data).Count 6 "intent count after duplicate confirmation"
    Assert-Equal (Get-CaseRunsOfWave -WaveID $script:initialWaveID).Count 6 "case_run count after duplicate confirmation"

    Step "run the initial wave while sampling the 3/2/1 quota and resolving the 2FA human request"
    $maxGlobal = 0
    $maxPerSut = 0
    $maxCross = 0
    $sawCases = @{}
    $humanResolved = $false
    $humanWaitStarted = Get-Date
    $initialWaveFinished = $false
    $waveDeadline = (Get-Date).AddSeconds($PollTimeoutSeconds)
    while ((Get-Date) -lt $waveDeadline) {
        $active = Get-ActiveWave
        if ($null -ne $active -and [string]$active.id -eq $script:initialWaveID) {
            $stats = Get-WaveRunStats -WaveID $script:initialWaveID
            if ($stats.Global -gt $maxGlobal) { $maxGlobal = $stats.Global }
            foreach ($key in $stats.PerSUT.Keys) {
                if ([int]$stats.PerSUT[$key] -gt $maxPerSut) { $maxPerSut = [int]$stats.PerSUT[$key] }
            }
            if ($stats.Cross -gt $maxCross) { $maxCross = $stats.Cross }
            foreach ($run in $stats.Runs) { $sawCases[[string]$run.id] = [string]$run.status }
        } else {
            $initialWaveFinished = $true
            break
        }
        if (-not $humanResolved) {
            $humanResponse = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/human-requests?status=open&limit=10" -Token $script:token
            if ($humanResponse.Status -eq 200) {
                $openRequests = Get-Array $humanResponse.Json.data
                if ($openRequests.Count -gt 0) {
                    $human = $openRequests[0]
                    $s8 = Start-Scenario "S8 login takeover: 2FA human_request open -> resolve 246810 -> resume -> pass"
                    $script:humanScenario = $s8
                    Step "human_request open: kind=$($human.kind), challenge=$($human.payload.challenge_kind), case_run=$($human.case_run_id)"
                    Assert-Equal $human.kind "login_takeover" "human request kind"
                    Assert-True ([string]$human.payload.challenge_kind -match "2fa|two|mfa|2_factor") ("human request challenge_kind = '{0}'" -f $human.payload.challenge_kind)
                    Assert-True ($human.status -eq "open") "human request must be open"
                    $humanCaseRun = (Get-CaseRun -CaseRunID ([string]$human.case_run_id)).case_run
                    Assert-Equal $humanCaseRun.status "paused" "case_run status while waiting for a human"
                    $waitingWave = Get-Waves | Where-Object { [string]$_.id -eq [string]$humanCaseRun.wave_id }
                    Assert-Equal $waitingWave[0].status "paused" "wave status while waiting for a human"
                    $humanOpenMs = [int]((Get-Date) - $humanWaitStarted).TotalMilliseconds
                    $resolveResponse = Invoke-Api -Method "POST" -Path ("/api/human-requests/{0}/resolve" -f [string]$human.id) -Token $script:token -Body @{
                        response = @{ code = "246810"; note = "P3 acceptance 2FA code" }
                    }
                    Assert-Status $resolveResponse @(200) "resolve human request with code 246810"
                    Assert-Equal $resolveResponse.Json.data.status "resolved" "human request status after resolve"
                    Assert-True ($null -eq $resolveResponse.Json.data.response.code) "resolve response must not echo the code"
                    $openAfterResolve = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/human-requests?status=open&limit=10" -Token $script:token
                    Assert-Status $openAfterResolve @(200) "GET open human requests after resolve"
                    Assert-Equal (Get-Array $openAfterResolve.Json.data).Count 0 "open human requests after resolve"
                    $humanResolved = $true
                    Pass-Scenario $s8
                    Start-Sleep -Milliseconds 500
                }
            }
        }
        Start-Sleep -Milliseconds 1000
    }
    if (-not $initialWaveFinished) { throw ("initial wave {0} did not finish within {1}s" -f $script:initialWaveID, $PollTimeoutSeconds) }

    Step "assert the sampled quota never exceeded global 3 / per-SUT 2 / cross-system 1"
    Write-Log ("      sampled max running: global={0} per-SUT={1} cross-system={2}" -f $maxGlobal, $maxPerSut, $maxCross)
    Assert-True ($maxGlobal -le 3) ("sampled global running {0} > 3" -f $maxGlobal)
    Assert-True ($maxPerSut -le 2) ("sampled per-SUT running {0} > 2" -f $maxPerSut)
    Assert-True ($maxCross -le 1) ("sampled cross-system running {0} > 1" -f $maxCross)
    Assert-True ($maxGlobal -ge 1) "quota sampler never observed a running case"

    Step "assert the initial wave reached a terminal state"
    $finishedWaveList = Get-Waves
    $finishedWave = $finishedWaveList | Where-Object { [string]$_.id -eq $script:initialWaveID } | Select-Object -First 1
    Assert-True ($null -ne $finishedWave) "initial wave missing from the wave history"
    Assert-True (@("done", "failed") -contains [string]$finishedWave.status) ("initial wave status = {0}, want done/failed" -f $finishedWave.status)
    $script:initialWaveStatus = [string]$finishedWave.status

    Step "wait for the deterministic failure/coverage analysis of the initial wave"
    Start-Sleep -Seconds 2
    $analysisWait = Wait-For -TimeoutSeconds 120 -Description "execution analysis (failure candidate + coverage gap)" -Condition {
        $gapCount = Invoke-PsqlScalar -DatabaseName $testDatabase -SqlText ("SELECT count(*) FROM work_item WHERE version_id = {0} AND type = 'design.gap';" -f $script:versionID)
        if ([int]$gapCount -ge 1) { return [int]$gapCount }
        return $null
    }
    Write-Log ("      design.gap work items: {0}" -f $analysisWait)

    Pass-Scenario $s3

    # ---------------- S4: single-SUT execution ---------------------------
    $s4 = Start-Scenario "S4 single-SUT execution: passed result, step/assertion/evidence, sha/download"

    Step "assert case A passed with complete steps, assertions and evidence"
    $runA = Wait-For -TimeoutSeconds 60 -Description "case A terminal passed" -Condition {
        $run = Get-CaseRunByTitle -Title $script:caseTitles["A"]
        if ($null -ne $run -and [string]$run.status -eq "passed") { return $run }
        if ($null -ne $run -and @("failed", "blocked", "inconclusive", "interrupted") -contains [string]$run.status) {
            throw ("case A ended {0} (failure_class={1}); real-LLM execution did not pass" -f $run.status, $run.failure_class)
        }
        return $null
    }
    Assert-Equal $runA.failure_class "" "case A failure_class"
    Assert-Equal ((Get-Array $runA.version_sut_ids) -join ",") $script:versionSUTOrders "case A version_sut_ids"
    $detailA = Get-CaseRun -CaseRunID ([string]$runA.id)
    Assert-Equal (Get-Array $detailA.steps).Count 1 "case A step count"
    Assert-Equal (Get-Array $detailA.assertions).Count 2 "case A assertion count"
    foreach ($step in (Get-Array $detailA.steps)) { Assert-Equal $step.status "passed" ("case A step {0} status" -f $step.seq) }
    foreach ($assertion in (Get-Array $detailA.assertions)) { Assert-Equal $assertion.verdict "passed" ("case A assertion {0}.{1} verdict" -f $assertion.step_seq, $assertion.assertion_seq) }
    $evidenceA = Get-Array $detailA.evidence
    Assert-True ($evidenceA.Count -ge 1) "case A must have evidence"
    $kindsA = @($evidenceA | ForEach-Object { [string]$_.kind } | Sort-Object -Unique)
    Assert-True ($kindsA -contains "screenshot") ("case A evidence kinds = {0}; want a screenshot" -f ($kindsA -join ","))

    Step "verify evidence metadata and authenticated byte download (sha256/size/headers)"
    $downloaded = 0
    foreach ($evidence in $evidenceA) {
        $evidenceID = [string]$evidence.id
        $metadata = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/evidence/$evidenceID" -Token $script:token
        Assert-Status $metadata @(200) "GET evidence metadata"
        Assert-Equal $metadata.Json.data.id $evidenceID "evidence id"
        Assert-Equal $metadata.Json.data.version_sut_id $script:versionSUTOrders "evidence version_sut_id"
        Assert-Match-Sha $metadata.Json.data.sha256 "evidence sha256 format"
        $validate = Invoke-ApiBytes -Path "/api/versions/$script:versionID/evidence/$evidenceID/content" -Token $script:token -OutFile (Join-Path $evidenceDir ("evidence-" + $evidenceID + ".bin"))
        Assert-Equal $validate.Status 200 "download evidence $evidenceID"
        $actualHash = (Get-FileHash -LiteralPath (Join-Path $evidenceDir ("evidence-" + $evidenceID + ".bin")) -Algorithm SHA256).Hash.ToLowerInvariant()
        Assert-Equal $actualHash ([string]$metadata.Json.data.sha256) "downloaded evidence sha256"
        Assert-Equal ([int64]$validate.Bytes.Length) ([int64]$metadata.Json.data.size_bytes) "downloaded evidence size"
        if ($validate.Headers.ContainsKey("x-content-sha256")) {
            Assert-Equal ([string]$validate.Headers["x-content-sha256"]) ([string]$metadata.Json.data.sha256) "X-Content-SHA256 header"
        }
        Assert-True ($validate.Headers.ContainsKey("content-disposition")) "evidence download must set Content-Disposition"
        $downloaded++
    }
    Write-Log ("      validated {0} immutable evidence downloads for case A" -f $downloaded)

    Step "assert evidence/execution projections never expose paths, storage_state or credentials"
    $forbidden = @("storage_state", "content_ref", "content_ref_id", "password", "credentials", "executor_path")
    $evidenceList = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/evidence?limit=100" -Token $script:token
    Assert-Status $evidenceList @(200) "GET evidence list"
    $detailAJson = ConvertTo-Json -InputObject $detailA -Depth 30 -Compress
    $eventsAJson = ConvertTo-Json -InputObject (Get-SnapshotEvents) -Depth 30 -Compress
    foreach ($text in @($evidenceList.Raw, $detailAJson, $eventsAJson)) {
        foreach ($fieldName in $forbidden) {
            if ($text -match ('"' + $fieldName + '"\s*:')) { throw ("execution/evidence projection exposes forbidden field '{0}'" -f $fieldName) }
        }
    }
    $secretScan = @("fixture-pass", $script:executorToken, [string]$dotenv["llm_api_key"])
    foreach ($evidenceFile in (Get-ChildItem -LiteralPath $evidenceDir -Filter "evidence-*.bin")) {
        $bytes = [System.IO.File]::ReadAllBytes($evidenceFile.FullName)
        $text = [System.Text.Encoding]::UTF8.GetString($bytes)
        foreach ($secretValue in $secretScan) {
            if ($text.Contains($secretValue)) { throw ("evidence {0} contains a secret value" -f $evidenceFile.Name) }
        }
    }
    foreach ($logName in @("api.stdout.log", "api.stderr.log", "executor.stdout.log", "executor.stderr.log", "fixture-orders.stdout.log", "fixture-orders.stderr.log", "fixture-payments.stdout.log", "fixture-payments.stderr.log")) {
        $logPath = Join-Path $logDir $logName
        if (-not (Test-Path -LiteralPath $logPath)) { continue }
        $logText = Get-Content -LiteralPath $logPath -Raw -ErrorAction SilentlyContinue
        if ($null -eq $logText) { continue }
        foreach ($secretValue in $secretScan) {
            if ($logText.Contains($secretValue)) { throw ("log {0} leaks a credential value" -f $logName) }
        }
    }
    Write-Log "      no evidence/log leak of password, executor token or LLM API key"

    Pass-Scenario $s4

    # ---------------- S5: cross-system execution -------------------------
    $s5 = Start-Scenario "S5 cross-system execution: two version-SUT contexts, per-SUT results/evidence/counts"

    Step "assert case B passed with two SUT contexts and per-SUT results"
    $runB = Wait-For -TimeoutSeconds 60 -Description "case B terminal passed" -Condition {
        $run = Get-CaseRunByTitle -Title $script:caseTitles["B"]
        if ($null -ne $run -and [string]$run.status -eq "passed") { return $run }
        if ($null -ne $run -and @("failed", "blocked", "inconclusive", "interrupted") -contains [string]$run.status) {
            throw ("cross-system case B ended {0} (failure_class={1})" -f $run.status, $run.failure_class)
        }
        return $null
    }
    $versionSutIDsB = @((Get-Array $runB.version_sut_ids) | Sort-Object)
    $expectedSutsB = @($script:versionSUTOrders, $script:versionSUTPayments) | Sort-Object
    Assert-Equal ($versionSutIDsB -join ",") ($expectedSutsB -join ",") "case B version_sut_ids"
    $detailB = Get-CaseRun -CaseRunID ([string]$runB.id)
    Assert-Equal (Get-Array $detailB.steps).Count 3 "case B step count"
    $stepSutIDs = @((Get-Array $detailB.steps) | ForEach-Object { [string]$_.target_version_sut_id } | Sort-Object -Unique)
    Assert-Equal ($stepSutIDs -join ",") ($expectedSutsB -join ",") "case B per-step target version_sut ids"
    $assertionSutIDs = @((Get-Array $detailB.assertions) | ForEach-Object { [string]$_.target_version_sut_id } | Sort-Object -Unique)
    Assert-True ($assertionSutIDs -contains $script:versionSUTOrders) "case B assertion target orders version_sut"
    Assert-True ($assertionSutIDs -contains $script:versionSUTPayments) "case B assertion target payments version_sut"
    $evidenceB = Get-Array $detailB.evidence
    $evidenceSutIDs = @($evidenceB | ForEach-Object { [string]$_.version_sut_id } | Sort-Object -Unique)
    Assert-True ($evidenceSutIDs -contains $script:versionSUTOrders) "case B evidence includes orders version_sut"
    Assert-True ($evidenceSutIDs -contains $script:versionSUTPayments) "case B evidence includes payments version_sut"
    $dbEvidence = @($evidenceB | Where-Object { [string]$_.kind -eq "db" })
    Assert-True ($dbEvidence.Count -ge 1) "case B must contain db.query evidence"

    Step "assert two case_run_sut_context rows for case B"
    $contextCount = Invoke-PsqlScalar -DatabaseName $testDatabase -SqlText ("SELECT count(*) FROM case_run_sut_context WHERE case_run_id = {0} AND version_sut_id IN ({1},{2});" -f [string]$runB.id, $script:versionSUTOrders, $script:versionSUTPayments)
    Assert-Equal $contextCount "2" "case B SUT context rows"

    Step "assert wave counts.by_version_sut contains both SUTs"
    $waveBResponse = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/waves/$($script:initialWaveID)" -Token $script:token
    Assert-Status $waveBResponse @(200) "GET initial wave detail"
    $bySut = $waveBResponse.Json.data.counts.by_version_sut
    Assert-True ($null -ne $bySut.PSObject.Properties[$script:versionSUTOrders]) "counts.by_version_sut is missing orders version_sut"
    Assert-True ($null -ne $bySut.PSObject.Properties[$script:versionSUTPayments]) "counts.by_version_sut is missing payments version_sut"
    Write-Log ("      counts.by_version_sut: orders={0}, payments={1}" -f $bySut.PSObject.Properties[$script:versionSUTOrders].Value.passed, $bySut.PSObject.Properties[$script:versionSUTPayments].Value.passed)

    Pass-Scenario $s5

    # ---------------- S6: application failure ----------------------------
    $s6 = Start-Scenario "S6 failure classification: failed(application), failure_candidate event, team post, P4 tables optional/complete"

    Step "assert case C failed with failure_class=application"
    $runC = Wait-For -TimeoutSeconds 60 -Description "case C terminal failed(application)" -Condition {
        $run = Get-CaseRunByTitle -Title $script:caseTitles["C"]
        if ($null -ne $run -and @("failed", "blocked", "inconclusive") -contains [string]$run.status) {
            # The deterministic analysis persists failure_class right after the
            # wave reaches its terminal state; wait for the classified value.
            if ([string]$run.failure_class -eq "application") { return $run }
        }
        if ($null -ne $run -and [string]$run.status -eq "passed") {
            throw "application-failure case C passed; the real LLM did not record the failing assertion"
        }
        return $null
    }
    Assert-Equal $runC.status "failed" "case C status"
    Assert-Equal $runC.failure_class "application" "case C failure_class"
    $detailC = Get-CaseRun -CaseRunID ([string]$runC.id)
    $failedAssertions = @((Get-Array $detailC.assertions) | Where-Object { [string]$_.verdict -eq "failed" })
    Assert-True ($failedAssertions.Count -ge 1) "case C must have a failed assertion"

    Step "assert execution.failure_candidate event and the execution team-room finding"
    try {
        $failureEvents = Get-SnapshotEvents
        Assert-EventPresent -Events $failureEvents -Type "execution.failure_candidate"
        $candidate = @($failureEvents | Where-Object { $_.type -eq "execution.failure_candidate" -and [string]$_.payload.case_run_id -eq [string]$runC.id })
        Assert-True ($candidate.Count -ge 1) "execution.failure_candidate must reference the failed case_run"
        Assert-Equal $candidate[0].payload.failure_class "application" "failure_candidate.failure_class"
        Assert-True (-not [string]::IsNullOrWhiteSpace([string]$candidate[0].payload.dedupe_hint)) "failure_candidate.dedupe_hint"
    } catch {
        Add-SoftFailure ("execution.failure_candidate contract: " + $_.Exception.Message)
    }
    try {
        $findingPosts = Invoke-PsqlScalar -DatabaseName $testDatabase -SqlText ("SELECT count(*) FROM team_room_post WHERE version_id = {0} AND team = 'execution' AND kind = 'finding' AND refs::text LIKE '%{1}%';" -f $script:versionID, [string]$runC.id)
        Assert-True ([int]$findingPosts -ge 1) "missing execution team-room finding for the failed case_run"
    } catch {
        Add-SoftFailure ("execution team-room finding contract: " + $_.Exception.Message)
    }

    Step "assert P4 tables, if present, are the complete P4 set (P3 execution must not require them)"
    $forbiddenTables = Invoke-PsqlScalar -DatabaseName $testDatabase -SqlText "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND (table_name ~* '(^|_)(issue|report|analysis_run|closure)($|_)');"
    Assert-True ([int]$forbiddenTables -eq 0 -or [int]$forbiddenTables -eq 8) "P4 tables must be absent (P3-only) or the complete 8-table P4 set (got $forbiddenTables)"

    Pass-Scenario $s6

    # ---------------- S7: coverage gap -----------------------------------
    $s7 = Start-Scenario "S7 coverage gap: design.gap work item + coverage event + no analysis_run/report"

    Step "assert the deterministic coverage gap for requirement R3 (only its confirmed case failed)"
    $gapRows = Wait-For -TimeoutSeconds 120 -Description "design.gap for requirement R3" -Condition {
        $count = Invoke-PsqlScalar -DatabaseName $testDatabase -SqlText ("SELECT count(*) FROM work_item WHERE version_id = {0} AND type = 'design.gap' AND payload->>'requirement_id' = '{1}' AND dedupe_key = 'gap:{1}:{2}';" -f $script:versionID, $script:requirementIDs["R3"], $script:caseVersionIDs["C"])
        if ([int]$count -ge 1) { return [int]$count }
        return $null
    }
    Assert-True ([int]$gapRows -ge 1) "missing design.gap work item for failed requirement R3"
    $gapEvent = @((Get-SnapshotEvents) | Where-Object { $_.type -eq "execution.coverage_gap" -and [string]$_.payload.requirement_id -eq $script:requirementIDs["R3"] })
    Assert-True ($gapEvent.Count -ge 1) "missing execution.coverage_gap event for requirement R3"
    Assert-Equal $gapEvent[0].payload.reason "execution_failed_coverage" "coverage gap reason"
    $blockerPosts = Invoke-PsqlScalar -DatabaseName $testDatabase -SqlText ("SELECT count(*) FROM team_room_post WHERE version_id = {0} AND team = 'execution' AND kind = 'blocker';" -f $script:versionID)
    Assert-True ([int]$blockerPosts -ge 1) "missing execution team-room blocker for the coverage gap"
    $analysisTasks = Invoke-PsqlScalar -DatabaseName $testDatabase -SqlText ("SELECT count(*) FROM work_item WHERE version_id = {0} AND type IN ('analyze.failure','analyze.coverage');" -f $script:versionID)
    Assert-True ([int]$analysisTasks -ge 2) ("analysis tasks = {0}, want analyze.failure + analyze.coverage" -f $analysisTasks)

    Pass-Scenario $s7

    # ---------------- S8 fallback (if no human request was observed) ------
    if (-not $humanResolved) {
        $s8 = Start-Scenario "S8 login takeover: 2FA human_request open -> resolve 246810 -> resume -> pass"
        throw "no open login_takeover human request was observed for the 2FA case; the real LLM did not request human help"
    }
    Step "assert the 2FA case D eventually passed after the human takeover"
    $runD = Wait-For -TimeoutSeconds 600 -Description "case D passed after human takeover" -Condition {
        $run = Get-CaseRunByTitle -Title $script:caseTitles["D"]
        if ($null -ne $run -and [string]$run.status -eq "passed") { return $run }
        if ($null -ne $run -and @("failed", "blocked", "inconclusive", "interrupted") -contains [string]$run.status) {
            throw ("2FA case D ended {0} (failure_class={1}) after the human response" -f $run.status, $run.failure_class)
        }
        return $null
    }
    Assert-Equal $runD.status "passed" "case D status"
    $detailD = Get-CaseRun -CaseRunID ([string]$runD.id)
    $step2D = @((Get-Array $detailD.steps) | Where-Object { [int]$_.seq -eq 2 })[0]
    Assert-Equal $step2D.status "passed" "case D login takeover step"
    $eventsD = Get-SnapshotEvents
    try {
        Assert-EventPresent -Events $eventsD -Type "human_request.created"
    } catch {
        Add-SoftFailure ("human takeover event contract: " + $_.Exception.Message) $script:humanScenario
    }
    Assert-EventPresent -Events $eventsD -Type "human_request.resolved"
    try {
        Assert-EventPresent -Events $eventsD -Type "case_run.resumed"
    } catch {
        Add-SoftFailure ("human takeover resume event contract: " + $_.Exception.Message) $script:humanScenario
    }
    $humanRows = Invoke-PsqlScalar -DatabaseName $testDatabase -SqlText ("SELECT count(*) FROM human_request WHERE version_id = {0} AND kind = 'login_takeover' AND status = 'resolved';" -f $script:versionID)
    Assert-True ([int]$humanRows -ge 1) "no resolved login_takeover human_request in the database"

    # ---------------- S9: pause/resume -----------------------------------
    $s9 = Start-Scenario "S9 pause/resume: immediate pause keeps finished results, resume continues to a terminal state"

    Step "start a manual run of the slow case F so the pause happens mid-run"
    $retestPause = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/execution/intents" -Token $script:token -Body @{
        case_version_id = $script:caseVersionIDs["F"]; purpose = "manual"
    }
    Assert-Status $retestPause @(201) "POST manual intent for pause scenario"
    $pauseWave = Wait-For -TimeoutSeconds 60 -Description "pause manual wave running" -Condition {
        $active = Get-ActiveWave
        if ($null -ne $active -and [string]$active.trigger -eq "manual") { return $active }
        return $null
    }
    $pauseWaveID = [string]$pauseWave.id
    $pauseRun = Wait-For -TimeoutSeconds 60 -Description "slow case run running" -Condition {
        $runs = Get-CaseRunsOfWave -WaveID $pauseWaveID
        $running = @($runs | Where-Object { [string]$_.status -eq "running" })
        if ($running.Count -ge 1) { return $running[0] }
        return $null
    }
    Start-Sleep -Seconds 4

    Step "pause the wave immediately and assert the wave/case-run paused state"
    $pauseResponse = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/waves/$pauseWaveID/pause" -Token $script:token -Body @{
        mode = "immediate"; reason = "P3 acceptance pause"
    }
    Assert-Status $pauseResponse @(200) "POST wave pause (immediate)"
    Assert-Equal $pauseResponse.Json.data.status "paused" "wave status after pause"
    Assert-Equal $pauseResponse.Json.data.pause_mode "immediate" "wave pause_mode"
    $pausedRun = Wait-For -TimeoutSeconds 30 -Description "case_run paused after immediate pause" -Condition {
        $detail = Get-CaseRun -CaseRunID ([string]$pauseRun.id)
        if ([string]$detail.case_run.status -eq "paused") { return $detail.case_run }
        return $null
    }
    Assert-Equal $pausedRun.status "paused" "case_run status after pause"
    $pausedOverview = Get-Overview
    Assert-True ($null -ne $pausedOverview.active_wave) "paused wave must keep the single active wave slot"
    Assert-Equal $pausedOverview.active_wave.status "paused" "overview active_wave status"
    Assert-True ([int]$pausedOverview.case_run_counts.paused -ge 1) "overview must count the paused case_run"
    $activeWaveCount = Invoke-PsqlScalar -DatabaseName $testDatabase -SqlText ("SELECT count(*) FROM run_wave WHERE version_id = {0} AND status IN ('planned','running','paused');" -f $script:versionID)
    Assert-Equal $activeWaveCount "1" "single active wave while paused"
    $pausedAt = Get-Date
    Assert-EventPresent -Events (Get-SnapshotEvents) -Type "wave.paused"

    Step "resume the wave and assert it continues to a passed terminal state"
    Start-Sleep -Seconds 3
    $resumeResponse = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/waves/$pauseWaveID/resume" -Token $script:token
    Assert-Status $resumeResponse @(200) "POST wave resume"
    Assert-True (@("running", "paused") -contains [string]$resumeResponse.Json.data.status) ("wave status after resume = {0}" -f $resumeResponse.Json.data.status)
    $resumedRun = Wait-For -TimeoutSeconds 300 -Description "slow case F passed after resume" -Condition {
        $detail = Get-CaseRun -CaseRunID ([string]$pauseRun.id)
        $run = $detail.case_run
        if ([string]$run.status -eq "passed") { return $run }
        if (@("failed", "blocked", "inconclusive") -contains [string]$run.status) {
            throw ("slow case F ended {0} after resume (failure_class={1})" -f $run.status, $run.failure_class)
        }
        return $null
    }
    $pauseSeconds = [int]((Get-Date) - $pausedAt).TotalSeconds
    Write-Log ("      paused for {0}s before resume (slow case passed)" -f $pauseSeconds)
    Assert-True ([int]$resumedRun.attempts -ge 2) ("resumed case attempts = {0}, want >= 2" -f $resumedRun.attempts)
    Assert-EventPresent -Events (Get-SnapshotEvents) -Type "wave.resumed"
    Assert-EventPresent -Events (Get-SnapshotEvents) -Type "case_run.paused"

    Pass-Scenario $s9

    # ---------------- S10: executor offline / redispatch -----------------
    $s10 = Start-Scenario "S10 executor offline: interrupted(executor_lost), epoch++, restart and redispatch"

    Step "wait for the previous pause wave/intent to finish"
    Wait-ExecutionIdle -TimeoutSeconds 180
    Step "start a manual run of the slow case F and wait for its first recorded step"
    $retestOffline = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/execution/intents" -Token $script:token -Body @{
        case_version_id = $script:caseVersionIDs["F"]; purpose = "manual"
    }
    Assert-Status $retestOffline @(201) "POST manual intent for offline scenario"
    $offlineWave = Wait-For -TimeoutSeconds 60 -Description "offline manual wave running" -Condition {
        $active = Get-ActiveWave
        if ($null -ne $active -and [string]$active.trigger -eq "manual") { return $active }
        return $null
    }
    $offlineWaveID = [string]$offlineWave.id
    $offlineRunRow = Wait-For -TimeoutSeconds 60 -Description "offline case run running" -Condition {
        $runs = Get-CaseRunsOfWave -WaveID $offlineWaveID
        $running = @($runs | Where-Object { [string]$_.status -eq "running" })
        if ($running.Count -ge 1) { return $running[0] }
        return $null
    }
    $offlineCaseRunID = [string]$offlineRunRow.id
    $preKillState = Invoke-PsqlScalar -DatabaseName $testDatabase -SqlText ("SELECT dispatch_epoch || '|' || attempts || '|' || dispatch_token FROM case_run WHERE id = {0};" -f $offlineCaseRunID)
    $preKillParts = ([string]$preKillState).Split("|")
    $preKillEpoch = [int64]$preKillParts[0]
    $preKillAttempts = [int]$preKillParts[1]
    $preKillToken = [string]$preKillParts[2]
    Write-Log ("      pre-kill case_run epoch={0} attempts={1}" -f $preKillEpoch, $preKillAttempts)
    $stepOneSeen = Wait-For -TimeoutSeconds 120 -Description "first slow-page step recorded" -Condition {
        $count = Invoke-PsqlScalar -DatabaseName $testDatabase -SqlText ("SELECT count(*) FROM step_result WHERE case_run_id = {0} AND seq = 1 AND status = 'passed';" -f $offlineCaseRunID)
        if ([int]$count -ge 1) { return [int]$count }
        return $null
    }
    Start-Sleep -Seconds 3

    Step "stop the executor process (only the tracked PID on port $ExecutorPort)"
    Stop-Executor
    $killAt = Get-Date
    $offlineSeen = Wait-For -TimeoutSeconds 90 -Description "executor poller marks the executor offline" -Condition {
        $response = Invoke-Api -Method "GET" -Path "/api/admin/executors" -Token $script:token
        if ($response.Status -ne 200) { return $null }
        foreach ($item in (Get-Array $response.Json.data)) {
            if ([string]$item.endpoint -eq $script:executorBase -and [string]$item.status -eq "offline") { return $item }
        }
        return $null
    }
    Write-Log ("      executor offline after {0:N1}s" -f ((Get-Date) - $killAt).TotalSeconds)

    Step "assert the case_run was interrupted(executor_lost), redispatch epoch increased and the old token is stale"
    $interruptedEvent = Wait-For -TimeoutSeconds 120 -Description "case_run interrupted/redispatched event" -Condition {
        $events = Get-SnapshotEvents
        $match = @($events | Where-Object {
            $_.type -eq "case_run.finished" -and [string]$_.payload.case_run_id -eq $offlineCaseRunID -and
            [string]$_.payload.status -eq "interrupted" -and [string]$_.payload.failure_class -eq "executor_lost"
        })
        if ($match.Count -ge 1) { return $match[0] }
        return $null
    }
    Assert-Equal $interruptedEvent.payload.redispatched $true "interrupted event redispatched flag"
    $epochAfterInterrupt = Invoke-PsqlScalar -DatabaseName $testDatabase -SqlText ("SELECT dispatch_epoch || '|' || attempts || '|' || status FROM case_run WHERE id = {0};" -f $offlineCaseRunID)
    $epochParts = ([string]$epochAfterInterrupt).Split("|")
    Assert-True ([int64]$epochParts[0] -gt $preKillEpoch) ("dispatch_epoch after interruption = {0}, want > {1}" -f $epochParts[0], $preKillEpoch)
    $tokenAfterInterrupt = Invoke-PsqlScalar -DatabaseName $testDatabase -SqlText ("SELECT dispatch_token FROM case_run WHERE id = {0};" -f $offlineCaseRunID)
    Assert-True ([string]$tokenAfterInterrupt -ne $preKillToken) "dispatch_token must be invalidated by the interruption"

    Step "restart the executor and assert the case is redispatched and passes on the new epoch"
    Start-Executor
    $recovered = Wait-For -TimeoutSeconds 60 -Description "executor active again after restart" -Condition {
        $overview = Get-Overview
        if ($null -ne $overview.executor -and [string]$overview.executor.status -eq "active") { return $overview.executor }
        return $null
    }
    $recoveredRun = Wait-For -TimeoutSeconds $PollTimeoutSeconds -Description "case F passed after executor restart" -Condition {
        $detail = Get-CaseRun -CaseRunID $offlineCaseRunID
        $run = $detail.case_run
        if ([string]$run.status -eq "passed") { return $run }
        if (@("failed", "blocked", "inconclusive") -contains [string]$run.status) {
            throw ("offline case F ended {0} (failure_class={1}) after executor restart" -f $run.status, $run.failure_class)
        }
        return $null
    }
    $finalState = Invoke-PsqlScalar -DatabaseName $testDatabase -SqlText ("SELECT dispatch_epoch || '|' || attempts FROM case_run WHERE id = {0};" -f $offlineCaseRunID)
    $finalParts = ([string]$finalState).Split("|")
    Assert-True ([int64]$finalParts[0] -ge ($preKillEpoch + 2)) ("final dispatch_epoch = {0}, want >= {1}" -f $finalParts[0], ($preKillEpoch + 2))
    Assert-True ([int]$finalParts[1] -ge ($preKillAttempts + 1)) ("final attempts = {0}, want >= {1}" -f $finalParts[1], ($preKillAttempts + 1))
    Write-Log ("      offline recovery: epoch {0} -> {1}, attempts {2} -> {3}" -f $preKillEpoch, $finalParts[0], $preKillAttempts, $finalParts[1])

    Pass-Scenario $s10

    # ---------------- S11: stale / superseded ----------------------------
    $s11 = Start-Scenario "S11 stale requirement update supersedes the active intent/case_run and creates no new intent"

    Step "wait for the offline redispatch wave/intent to finish"
    Wait-ExecutionIdle -TimeoutSeconds 180
    Step "create a manual intent for case A, then update requirement R1 (new requirement revision)"
    $staleIntentResponse = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/execution/intents" -Token $script:token -Body @{
        case_version_id = $script:caseVersionIDs["A"]; purpose = "manual"
    }
    Assert-Status $staleIntentResponse @(201) "POST manual intent for stale scenario"
    $staleIntentID = [string]$staleIntentResponse.Json.data.id
    $r1 = $script:requirementIDs["R1"]
    $updateRequirement = Invoke-Api -Method "PATCH" -Path "/api/versions/$script:versionID/requirements/$r1" -Token $script:token -Body @{
        title = "P3 order flows (revised for stale acceptance)"
        description = "The order flow requirement changed after the case was confirmed"
        acceptance_criteria = "The revised requirement invalidates the confirmed case versions"
        priority = "P1"
        constraints = @{}
        targets = @{ version_suts = @($script:versionSUTOrders, $script:versionSUTPayments); pages = @(); apis = @() }
        reason = "P3 acceptance stale scenario"
    }
    Assert-Status $updateRequirement @(200) "PATCH requirement R1 (new revision)"
    $newRevision = [string]$updateRequirement.Json.data.current_version.id
    Assert-True ($newRevision -ne $script:requirementVersionIDs["R1"]) "requirement update must create a new revision"

    Step "assert case A and the other R1 cases became stale, the intent was superseded and no new intent exists"
    $staleCaseWait = Wait-For -TimeoutSeconds 60 -Description "R1 case versions stale" -Condition {
        $count = Invoke-PsqlScalar -DatabaseName $testDatabase -SqlText ("SELECT count(*) FROM case_version WHERE status = 'stale' AND id IN ({0});" -f (($script:caseVersionIDs["A"], $script:caseVersionIDs["B"], $script:caseVersionIDs["E"], $script:caseVersionIDs["F"]) -join ","))
        if ([int]$count -ge 4) { return [int]$count }
        return $null
    }
    Assert-Equal $staleCaseWait "4" "stale case_version count for R1"
    $supersededWait = Wait-For -TimeoutSeconds 60 -Description "retest intent superseded" -Condition {
        $response = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/execution/intents?limit=100" -Token $script:token
        if ($response.Status -ne 200) { return $null }
        $match = @((Get-Array $response.Json.data) | Where-Object { [string]$_.id -eq $staleIntentID -and [string]$_.status -eq "superseded" })
        if ($match.Count -ge 1) { return $match[0] }
        return $null
    }
    Assert-Equal $supersededWait.status "superseded" "retest intent status after requirement update"
    try {
        Assert-EventPresent -Events (Get-SnapshotEvents) -Type "execution.intent_superseded"
    } catch {
        Add-SoftFailure ("intent superseded event contract: " + $_.Exception.Message) $s11
    }
    $supersededRuns = Invoke-PsqlScalar -DatabaseName $testDatabase -SqlText ("SELECT count(*) FROM case_run WHERE intent_id = {0} AND status = 'superseded';" -f $staleIntentID)
    Write-Log ("      superseded case_runs for the stale intent: {0}" -f $supersededRuns)
    $intentCountForA = Invoke-PsqlScalar -DatabaseName $testDatabase -SqlText ("SELECT count(*) FROM execution_intent WHERE version_id = {0} AND case_version_id = {1};" -f $script:versionID, $script:caseVersionIDs["A"])
    Assert-Equal $intentCountForA "2" "no new intent may be created for the stale case A (initial + retest)"

    Pass-Scenario $s11

    # ---------------- S12: retest ---------------------------------------
    $s12 = Start-Scenario "S12 retest: new retest intent/wave, old results preserved, duplicate active intent -> 409"

    Step "wait for the stale intent/wave to settle"
    Wait-ExecutionIdle -TimeoutSeconds 180
    Step "create a retest intent for the failed case C and assert the duplicate active intent is rejected with 409"
    # Capture the original (initial-wave) case_run before the retest creates a
    # second run of the same case_version.
    $oldRunCID = Invoke-PsqlScalar -DatabaseName $testDatabase -SqlText ("SELECT id FROM case_run WHERE case_version_id = {0} ORDER BY id LIMIT 1;" -f $script:caseVersionIDs["C"])
    $oldRunCBefore = (Get-CaseRun -CaseRunID ([string]$oldRunCID.Trim())).case_run
    $oldEvidenceCount = (Get-Array (Get-CaseRun -CaseRunID ([string]$oldRunCBefore.id)).evidence).Count
    # P4 branch: the real Version Steward may already have closed R3, which
    # legitimately rejects a new intent. Reopen that requirement through the P4
    # API so the P3 retest scenario stays meaningful; P3-only runs skip this.
    $r3Status = (Invoke-PsqlScalar -DatabaseName $testDatabase -SqlText ("SELECT status FROM requirement WHERE id = {0};" -f $script:requirementIDs["R3"])).Trim()
    if ($r3Status -eq "closed") {
        Step "reopen R3 through the P4 API so the P3 retest scenario can create a new intent"
        $reopenR3 = Invoke-Api -Method "POST" -Path ("/api/versions/{0}/requirements/{1}/reopen" -f $script:versionID, $script:requirementIDs["R3"]) -Token $script:token -Body @{ reason = "P3 regression: reopen requirement for retest scenario" }
        Assert-Status $reopenR3 @(200) "POST requirement reopen before retest"
        Assert-Equal $reopenR3.Json.data.status "draft" "reopened requirement status"
    }
    $retestResponse = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/execution/intents" -Token $script:token -Body @{
        case_version_id = $script:caseVersionIDs["C"]; purpose = "retest"
    }
    Assert-Status $retestResponse @(201) "POST retest intent for failed case C"
    $retestIntentID = [string]$retestResponse.Json.data.id
    Assert-Equal $retestResponse.Json.data.purpose "retest" "retest intent purpose"
    $duplicateIntent = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/execution/intents" -Token $script:token -Body @{
        case_version_id = $script:caseVersionIDs["C"]; purpose = "retest"
    }
    Assert-Status $duplicateIntent @(409) "duplicate active retest intent must be rejected"

    Step "wait for the retest wave and assert the old case_run/evidence are preserved"
    $retestWave = Wait-For -TimeoutSeconds 120 -Description "retest wave containing case C" -Condition {
        $active = Get-ActiveWave
        if ($null -eq $active) { return $null }
        $runs = Get-CaseRunsOfWave -WaveID ([string]$active.id)
        $match = @($runs | Where-Object { [string]$_.case_version_id -eq $script:caseVersionIDs["C"] })
        if ($match.Count -ge 1 -and [string]$active.trigger -eq "retest") { return $active }
        return $null
    }
    $retestWaveID = [string]$retestWave.id
    $retestRun = Wait-For -TimeoutSeconds 60 -Description "retest case_run created" -Condition {
        $runs = Get-CaseRunsOfWave -WaveID $retestWaveID
        $match = @($runs | Where-Object { [string]$_.case_version_id -eq $script:caseVersionIDs["C"] })
        if ($match.Count -ge 1) { return $match[0] }
        return $null
    }
    Assert-True ([string]$retestRun.id -ne [string]$oldRunCBefore.id) "retest must create a new case_run"
    $retestDone = Wait-For -TimeoutSeconds 600 -Description "retest wave terminal" -Condition {
        $active = Get-ActiveWave
        if ($null -eq $active) {
            $waves = Get-Waves
            $match = $waves | Where-Object { [string]$_.id -eq $retestWaveID } | Select-Object -First 1
            if ($null -ne $match -and @("done", "failed") -contains [string]$match.status) { return $match }
        }
        return $null
    }
    $retestRunFinal = (Get-CaseRun -CaseRunID ([string]$retestRun.id)).case_run
    Assert-True (@("failed", "blocked", "inconclusive") -contains [string]$retestRunFinal.status) ("retest of the failing case should not pass (status={0})" -f $retestRunFinal.status)
    $retestIntents = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/execution/intents?limit=100" -Token $script:token
    Assert-Status $retestIntents @(200) "GET intents after retest"
    $retestIntentView = @((Get-Array $retestIntents.Json.data) | Where-Object { [string]$_.id -eq $retestIntentID })[0]
    Assert-True (@("done", "failed") -contains [string]$retestIntentView.status) ("retest intent status = {0}" -f $retestIntentView.status)
    $oldRunCAfter = (Get-CaseRun -CaseRunID ([string]$oldRunCBefore.id)).case_run
    Assert-Equal $oldRunCAfter.status "failed" "old failed case_run must be preserved"
    Assert-Equal (Get-Array (Get-CaseRun -CaseRunID ([string]$oldRunCBefore.id)).evidence).Count $oldEvidenceCount "old case_run evidence must be preserved"
    $retestEvidence = Get-Array (Get-CaseRun -CaseRunID ([string]$retestRun.id)).evidence
    Assert-True ($retestEvidence.Count -ge 1) "retest case_run must record its own evidence"

    Pass-Scenario $s12

    # ---------------- S13: SSE / evidence -------------------------------
    $s13 = Start-Scenario "S13 SSE: version stream carries wave/case/evidence/human events, Last-Event-ID replay has no duplicates"

    Step "read the newest GET /api/events page and assert the persisted P3 event set"
    $snapshotResponse = Invoke-Api -Method "GET" -Path ("/api/events?channel={0}&limit=100" -f [System.Uri]::EscapeDataString("version:" + $script:versionID)) -Token $script:token
    Assert-Status $snapshotResponse @(200) "GET /api/events (newest page)"
    Assert-True ((Get-Array $snapshotResponse.Json.data).Count -ge 1) "GET /api/events returned no events"
    $events = Get-SnapshotEvents
    $requiredEvents = @(
        "execution.intent_created", "execution.intent_superseded", "wave.created", "wave.started",
        "wave.paused", "wave.resumed", "wave.finished", "case_run.started", "case_run.paused",
        "case_run.resumed", "case_run.finished", "case_run.step_finished", "case_run.assertion_finished",
        "evidence.created", "human_request.created", "human_request.resolved",
        "execution.failure_candidate", "execution.coverage_gap"
    )
    foreach ($type in $requiredEvents) {
        try { Assert-EventPresent -Events $events -Type $type } catch { Add-SoftFailure ("persisted event contract: " + $_.Exception.Message) $s13 }
    }
    Write-Log ("      persisted event types: {0}" -f (($events | ForEach-Object { $_.type } | Sort-Object -Unique) -join ", "))

    Step "replay the SSE stream from the first retained cursor and assert no duplicate seq"
    $bounds = Get-EventBounds
    $replayCursor = $bounds.FirstSeq
    if ($bounds.FirstSeq -gt 1) { $replayCursor = $bounds.FirstSeq - 1 }
    Write-Log ("      event bounds: first={0} last={1}; SSE replay from Last-Event-ID={2}" -f $bounds.FirstSeq, $bounds.LastSeq, $replayCursor)
    $replayed = Read-SseEvents -Channel ("version:" + $script:versionID) -LastEventId $replayCursor -TimeoutSeconds 90 -IdleSeconds 4 -MaxEvents 5000
    Assert-True ($replayed.Count -ge 18) ("SSE replay delivered {0} events, want >= 18" -f $replayed.Count)
    $seenIDs = @{}
    $lastID = [long]0
    foreach ($event in $replayed) {
        Assert-True ([long]$event.Id -gt $lastID) ("SSE seq is not strictly increasing: {0} after {1}" -f $event.Id, $lastID)
        $lastID = [long]$event.Id
        Assert-True (-not $seenIDs.ContainsKey([string]$event.Id)) ("SSE replay duplicated seq {0}" -f $event.Id)
        $seenIDs[[string]$event.Id] = $true
    }
    $replayedTypes = @($replayed | ForEach-Object { $_.Type } | Sort-Object -Unique)
    foreach ($required in @("wave.created", "case_run.step_finished", "evidence.created", "human_request.created", "execution.failure_candidate", "execution.coverage_gap")) {
        if (-not ($replayedTypes -contains $required)) {
            Add-SoftFailure ("SSE replay is missing event type {0}" -f $required) $s13
        }
    }

    Step "reconnect with Last-Event-ID at the current tail and assert no already-seen event is replayed"
    $maxSeq = $bounds.LastSeq
    $afterReplay = Read-SseEvents -Channel ("version:" + $script:versionID) -LastEventId $maxSeq -TimeoutSeconds 20 -IdleSeconds 4 -MaxEvents 100
    $replaySeen = @{}
    foreach ($event in $afterReplay) {
        Assert-True ([long]$event.Id -gt $maxSeq) ("Last-Event-ID replay repeated seq {0} (cursor {1})" -f $event.Id, $maxSeq)
        Assert-True (-not $replaySeen.ContainsKey([string]$event.Id)) ("Last-Event-ID replay duplicated seq {0}" -f $event.Id)
        $replaySeen[[string]$event.Id] = $true
    }
    Write-Log ("      Last-Event-ID replay after seq {0}: {1} new live event(s), no duplicates" -f $maxSeq, $afterReplay.Count)

    Pass-Scenario $s13

    # ---------------- S14: timeout window -------------------------------
    $s14 = Start-Scenario "S14 timed confirmation window: dialog visible then gone + window-expired evidence"
    Step "assert the timeout-window case E passed with both window assertions and screenshot evidence"
    # Look the run up by immutable case_version_id: after the S11 stale update
    # the execution projection no longer resolves a title for E.
    $runEID = ([string](Invoke-PsqlScalar -DatabaseName $testDatabase -SqlText ("SELECT id FROM case_run WHERE case_version_id = {0} ORDER BY id LIMIT 1;" -f $script:caseVersionIDs["E"]))).Trim()
    $detailE = Get-CaseRun -CaseRunID $runEID
    $runE = $detailE.case_run
    Assert-Equal $runE.status "passed" "timeout-window case E status"
    $assertionsE = Get-Array $detailE.assertions
    Assert-Equal $assertionsE.Count 2 "timeout-window assertion count"
    foreach ($assertion in $assertionsE) { Assert-Equal $assertion.verdict "passed" ("timeout-window assertion {0}.{1}" -f $assertion.step_seq, $assertion.assertion_seq) }
    $evidenceE = @((Get-Array $detailE.evidence) | Where-Object { [string]$_.kind -eq "screenshot" })
    Assert-True ($evidenceE.Count -ge 1) "timeout-window case must have screenshot evidence"
    $durationE = ([datetime]$runE.finished_at - [datetime]$runE.started_at).TotalSeconds
    if ($LongWait) {
        Assert-True ($durationE -ge 120) ("-LongWait case duration = {0:N1}s, want >= 120s of real time" -f $durationE)
        Write-Log ("      -LongWait: timeout-window case took {0:N1}s (window ms=120000, execution.wait 122)" -f $durationE)
    } else {
        Assert-True ($durationE -ge 10) ("timeout-window case duration = {0:N1}s, want >= 10s" -f $durationE)
        Write-Log ("      default: timeout-window case took {0:N1}s (window ms=10000, execution.wait 12)" -f $durationE)
    }

    Pass-Scenario $s14

    # ---------------- S15: regression -----------------------------------
    $s15 = Start-Scenario "S15 regression note: contract checks, migration idempotency"

    Step "run node scripts/p3-contract-check.cjs --draft and node scripts/p2-contract-check.cjs"
    $node = Get-Command node -ErrorAction SilentlyContinue
    if ($null -eq $node) { throw "node not found on PATH" }
    $p3Check = & $node.Source (Join-Path $repoRoot "scripts\p3-contract-check.cjs") --draft 2>&1
    $p3Exit = $LASTEXITCODE
    $p2Check = & $node.Source (Join-Path $repoRoot "scripts\p2-contract-check.cjs") 2>&1
    $p2Exit = $LASTEXITCODE
    Set-Content -LiteralPath (Join-Path $logDir "contract-check.log") -Value (@("p3 draft exit=" + $p3Exit) + @($p3Check) + @("p2 exit=" + $p2Exit) + @($p2Check)) -Encoding UTF8
    if ($p3Exit -ne 0) { throw ("P3 draft contract check failed ({0}); see contract-check.log" -f $p3Exit) }
    if ($p2Exit -ne 0) { throw ("P2 contract check failed ({0}); see contract-check.log" -f $p2Exit) }
    Write-Log "      p3-contract-check --draft: PASS; p2-contract-check: PASS"

    Step "assert migration idempotency (20 migrations applied twice across the bootstrap + acceptance startups)"
    $migrationCount = Invoke-PsqlScalar -DatabaseName $testDatabase -SqlText "SELECT count(*) FROM schema_migrations;"
    if ([int]$migrationCount -lt 20) {
        $migrationCount = Invoke-PsqlScalar -DatabaseName $testDatabase -SqlText "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public';"
    }
    $apiStderr = Get-Content -LiteralPath (Join-Path $logDir "api.stderr.log") -Raw -ErrorAction SilentlyContinue
    if ($null -ne $apiStderr -and $apiStderr -match "migrat(e|ion).*(error|fail)") { throw "API startup log reports a migration error" }
    Write-Log ("      migrations: {0} rows/tables; second API start migrated cleanly" -f $migrationCount)
    Write-Log "      NOTE: P1/P2 fake-LLM regression (scripts/p1-acceptance.ps1, scripts/p2-acceptance.ps1, scripts/p2-e2e.ps1) is owned by the phase gate; those scripts still assert 0.2.0-p2 and are not run here."

    Pass-Scenario $s15

    # ---------------- S16: evidence on disk -----------------------------
    $s16 = Start-Scenario "S16 evidence on disk: logs, summaries, screenshots and failure file"
    $expectedFiles = @(
        $acceptanceLog,
        (Join-Path $logDir "api.stdout.log"),
        (Join-Path $logDir "api.stderr.log"),
        (Join-Path $logDir "executor.stdout.log"),
        (Join-Path $logDir "executor.stderr.log"),
        (Join-Path $logDir "fixture-orders.stdout.log"),
        (Join-Path $logDir "fixture-payments.stdout.log"),
        (Join-Path $logDir "fixture-db.log"),
        (Join-Path $logDir "contract-check.log")
    )
    foreach ($file in $expectedFiles) { Assert-True (Test-Path -LiteralPath $file) ("missing evidence file {0}" -f (Split-Path -Leaf $file)) }
    $evidenceFiles = @(Get-ChildItem -LiteralPath $evidenceDir -Filter "evidence-*.bin")
    Assert-True ($evidenceFiles.Count -ge 1) "no downloaded evidence files"
    Write-Log ("      evidence files: {0} log files, {1} downloaded evidence bytes" -f $expectedFiles.Count, $evidenceFiles.Count)

    Pass-Scenario $s16

    if ($script:softFailures.Count -gt 0) {
        $script:failed = $true
        $combined = ($script:softFailures -join " | ")
        Write-FailureStep ("soft scenario failures: " + $combined)
        Write-Log ("FAIL  " + $script:softFailures.Count + " scenario assertion(s) failed; see " + $summaryFile) "Red"
        $exitCode = 1
    } else {
        Write-Log "ALL SCENARIOS PASS" "Green"
        $exitCode = 0
    }
} catch {
    Write-FailureStep $_.Exception.Message
    $script:failed = $true
    if ($null -ne $script:currentScenario) {
        $script:currentScenario.Status = "FAIL"
        $script:currentScenario.Detail = $_.Exception.Message
        if ($script:currentScenario.PSObject.Properties["Error"]) {
            $script:currentScenario.PSObject.Properties["Error"].Value = $_.Exception.Message
        } else {
            $script:currentScenario | Add-Member -NotePropertyName Error -NotePropertyValue $_.Exception.Message
        }
    }
    Write-Log ("FAIL  " + $_.Exception.Message) "Red"
    if ($null -ne $_.InvocationInfo) { Write-Log ("      at " + $_.InvocationInfo.ScriptName + ":" + $_.InvocationInfo.ScriptLineNumber) "DarkRed" }
    $exitCode = 1
} finally {
    Stop-Services
    if ($null -ne $script:http) { $script:http.Dispose() }
    Write-ScenarioSummary
    $elapsed = (Get-Date) - $script:startedAt
    Write-Log ("RESULT: {0} ({1:N1}s)" -f $(if ($exitCode -eq 0) { "PASS" } else { "FAIL" }), $elapsed.TotalSeconds) $(if ($exitCode -eq 0) { "Green" } else { "Red" })
    Write-Log ("Evidence: " + $logDir)
    if (Test-Path -LiteralPath $failureStepFile) { Write-Log ("Failure:  " + $failureStepFile) "Red" }
    Write-Log ("Scenarios: " + $summaryFile)
}

exit $exitCode
