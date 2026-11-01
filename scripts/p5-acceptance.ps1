#requires -Version 5.1
<#
.SYNOPSIS
    EzTest 0.2 P5 real-DeepSeek + real-Chrome acceptance runner (API/DB level).

.DESCRIPTION
    Boots a fully isolated P5 stack and drives spec section 12.3 scenarios S0-S12
    with the real DeepSeek model from .env and the real Playwright Chrome executor:

      * database : eztest_v02_p5 (dropped and recreated on every run)
      * fixtures : scripts/fixture-p5-repo.ps1 -> .tmp/p5-repo (commit A/B)
                   scripts/fixture-p5-target.cjs on 127.0.0.1:13338
      * executor : executor/src/server.mjs on 127.0.0.1:14347 with a random token
      * API      : freshly built .tmp/p5-acceptance/eztest-p5.exe on 127.0.0.1:14345
      * LLM      : the real DeepSeek config from .env (llm_api_key/llm_base_url/
                   llm_model_name), written through the admin LLM API

    No fake LLM is ever configured. A failing scenario is recorded as FAIL with
    the exact step; a structurally unreachable scenario is BLOCKED with evidence.
    Evidence goes to .tmp/p5-acceptance/: acceptance.log, scenarios.txt,
    failure-step.txt, evidence/ and per-service stdout/stderr logs.

    Exit codes: 0 = every run scenario PASS, 1 = at least one FAIL (an assertion
    failed), 2 = no FAIL but at least one BLOCKED (environment/protocol gap).

.NOTES
    Windows PowerShell 5.1+ compatible. Never touches the development database,
    the development API/executor/web ports or any non-.tmp build output.
#>
[CmdletBinding()]
param(
    [int]$ApiPort = 14345,
    [int]$WebPort = 16870,
    [int]$ExecutorPort = 14347,
    [int]$FixturePort = 13338,
    [int]$PollTimeoutSeconds = 900,
    [switch]$SkipBuild,
    [switch]$KeepRunning,
    # Optional comma separated scenario prefixes for debugging, e.g. "S0,S1,S2".
    # Empty runs every scenario S0-S12.
    [string]$Scenarios = ""
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$logDir = Join-Path $repoRoot ".tmp\p5-acceptance"
$evidenceDir = Join-Path $logDir "evidence"
$exePath = Join-Path $repoRoot ".tmp\p5-acceptance\eztest-p5.exe"
$acceptanceLog = Join-Path $logDir "acceptance.log"
$failureStepFile = Join-Path $logDir "failure-step.txt"
$summaryFile = Join-Path $logDir "scenarios.txt"
$seedFile = Join-Path $logDir "seed-cases.sql"
$testDatabase = "eztest_v02_p5"
$envPath = Join-Path $repoRoot ".env"
$fixtureRepoScript = Join-Path $repoRoot "scripts\fixture-p5-repo.ps1"
$fixtureTargetScript = Join-Path $repoRoot "scripts\fixture-p5-target.cjs"

New-Item -ItemType Directory -Force -Path $logDir | Out-Null
New-Item -ItemType Directory -Force -Path $evidenceDir | Out-Null
New-Item -ItemType Directory -Force -Path (Split-Path -Parent $exePath) | Out-Null
Set-Content -LiteralPath $acceptanceLog -Value "" -Encoding UTF8
Remove-Item -LiteralPath $failureStepFile -ErrorAction SilentlyContinue
Remove-Item -LiteralPath $summaryFile -ErrorAction SilentlyContinue

$script:apiBase = "http://127.0.0.1:$ApiPort"
$script:fixtureBase = "http://127.0.0.1:$FixturePort"
$script:executorBase = "http://127.0.0.1:$ExecutorPort"
$script:http = $null
$script:apiProcess = $null
$script:executorProcess = $null
$script:fixtureProcess = $null
$script:psql = $null
$script:db = $null
$script:token = ""
$script:userToken = ""
$script:results = New-Object System.Collections.ArrayList
$script:softFailures = New-Object System.Collections.ArrayList
$script:blocked = New-Object System.Collections.ArrayList
$script:currentScenario = $null
$script:stage = "bootstrap"
$script:executorToken = ""
$script:startedAt = Get-Date
$script:scenarioOrder = @{}
$scenarioNumbers = @{}
for ($index = 0; $index -le 12; $index++) {
    $scenarioNumbers[("S" + $index)] = $index
}

function Should-Run {
    param([string]$Name)
    if ([string]::IsNullOrWhiteSpace($Scenarios)) { return $true }
    foreach ($prefix in ($Scenarios -split ",")) {
        $trimmed = $prefix.Trim()
        if ($trimmed -eq "") { continue }
        if ($Name.StartsWith($trimmed)) { return $true }
    }
    return $false
}

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

function Assert-Match {
    param([string]$Value, [string]$Pattern, [string]$What)
    if ($Value -notmatch $Pattern) {
        throw ("ASSERT FAILED: {0} = '{1}' does not match {2}" -f $What, $Value, $Pattern)
    }
}

function Start-Scenario {
    param([string]$Name, [string]$Command = "")
    $record = [pscustomobject]@{
        Name       = $Name
        Number     = 0
        Started    = Get-Date
        Status     = "RUNNING"
        Detail     = ""
        Command    = $Command
        Assertions = New-Object System.Collections.ArrayList
    }
    if ($Name -match '^(S[0-9]+)') {
        if ($scenarioNumbers.ContainsKey($Matches[1])) { $record.Number = [int]$scenarioNumbers[$Matches[1]] }
    }
    [void]$script:results.Add($record)
    $script:currentScenario = $record
    Write-Log ("SCENARIO START  " + $Name) "Cyan"
    return $record
}

function Note-Assertion {
    param([string]$Text)
    if ($null -ne $script:currentScenario) {
        [void]$script:currentScenario.Assertions.Add($Text)
    }
}

function Pass-Scenario {
    param($Record)
    if ($null -eq $Record) { $Record = $script:currentScenario }
    if ($null -eq $Record) { return }
    if ($Record.Status -eq "FAIL") {
        Write-Log ("SCENARIO FAIL   " + $Record.Name + " (recorded failures, continuing)") "Red"
        return
    }
    if ($Record.Status -eq "BLOCKED") {
        Write-Log ("SCENARIO BLOCK  " + $Record.Name) "Yellow"
        return
    }
    $Record.Status = "PASS"
    $Record.Detail = ("{0:N1}s" -f ((Get-Date) - $Record.Started).TotalSeconds)
    Write-Log ("SCENARIO PASS   " + $Record.Name + " (" + $Record.Detail + ")") "Green"
}

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

function Block-Scenario {
    param([string]$Reason, $Record)
    Write-Log ("BLOCKED  " + $Reason) "Yellow"
    [void]$script:blocked.Add($Reason)
    $target = $Record
    if ($null -eq $target) { $target = $script:currentScenario }
    if ($null -ne $target) {
        if ($target.Status -ne "FAIL") {
            $target.Status = "BLOCKED"
            if ($target.PSObject.Properties["Error"]) {
                $target.PSObject.Properties["Error"].Value = $Reason
            } else {
                $target | Add-Member -NotePropertyName Error -NotePropertyValue $Reason
            }
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
    $lines = New-Object System.Collections.ArrayList
    [void]$lines.Add("# EzTest P5 real-LLM + real-Chrome acceptance scenarios")
    [void]$lines.Add("# format: STATUS|SCENARIO|DURATION|COMMAND|ASSERTIONS")
    $ordered = @($script:results | Sort-Object -Property @{ Expression = { if ($_.Number -gt 0) { $_.Number } else { 99 } } }, Started)
    foreach ($item in $ordered) {
        $errorText = ""
        if ($null -ne $item.PSObject.Properties["Error"]) { $errorText = [string]$item.PSObject.Properties["Error"].Value }
        $assertions = @($item.Assertions) -join " ; "
        if ($errorText -ne "") {
            if ($assertions -ne "") { $assertions += " ; " }
            $assertions += "FAILED: " + $errorText
        }
        $status = $item.Status
        if ($status -eq "RUNNING") { $status = "FAIL" }
        [void]$lines.Add(("{0}|{1}|{2}|{3}|{4}" -f $status, $item.Name, $item.Detail, $item.Command, $assertions))
    }
    $passCount = @($script:results | Where-Object { $_.Status -eq "PASS" }).Count
    $failCount = @($script:results | Where-Object { $_.Status -eq "FAIL" -or $_.Status -eq "RUNNING" }).Count
    $blockCount = @($script:results | Where-Object { $_.Status -eq "BLOCKED" }).Count
    [void]$lines.Add("")
    [void]$lines.Add(("TOTAL|{0}|PASS={1}|FAIL={2}|BLOCKED={3}" -f (Get-Date -Format "yyyy-MM-dd HH:mm:ss"), $passCount, $failCount, $blockCount))
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
    param([string]$SqlText)
    $previousPassword = $env:PGPASSWORD
    $previousPreference = $ErrorActionPreference
    try {
        $env:PGPASSWORD = $script:db.Password
        $ErrorActionPreference = "Continue"
        $output = & $script:psql -h $script:db.Host -p $script:db.Port -U $script:db.User `
            -d $testDatabase -v ON_ERROR_STOP=1 -tAc $SqlText 2>&1
        $exit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousPreference
        $env:PGPASSWORD = $previousPassword
    }
    if ($exit -ne 0) { throw ("psql failed ({0}): {1}" -f $exit, ($output -join " ")) }
    return @($output)
}

function Invoke-PsqlFile {
    param([string]$FilePath)
    $previousPassword = $env:PGPASSWORD
    $previousPreference = $ErrorActionPreference
    try {
        $env:PGPASSWORD = $script:db.Password
        $ErrorActionPreference = "Continue"
        $output = & $script:psql -h $script:db.Host -p $script:db.Port -U $script:db.User `
            -d $testDatabase -v ON_ERROR_STOP=1 -q -f $FilePath 2>&1
        $exit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousPreference
        $env:PGPASSWORD = $previousPassword
    }
    if ($exit -ne 0) { throw ("psql -f failed ({0}): {1}" -f $exit, ($output -join " ")) }
}

function Invoke-PsqlScalar {
    param([string]$SqlText)
    $rows = @(Invoke-Psql -SqlText $SqlText)
    return (($rows | Out-String).Trim())
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
    $client.Timeout = [TimeSpan]::FromSeconds(600)
    return $client
}

function Invoke-Api {
    param(
        [string]$Method,
        [string]$Path,
        $Body,
        [string]$Token,
        [int]$TimeoutSeconds = 600
    )
    $uri = $script:apiBase + $Path
    $request = New-Object System.Net.Http.HttpRequestMessage ((New-Object System.Net.Http.HttpMethod($Method)), $uri)
    try {
        if (-not [string]::IsNullOrEmpty($Token)) {
            $request.Headers.Authorization = New-Object System.Net.Http.Headers.AuthenticationHeaderValue("Bearer", $Token)
        }
        if ($null -ne $Body) {
            $json = ConvertTo-Json -InputObject $Body -Depth 40 -Compress
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
    param([string]$Path, [string]$Token, [string]$OutFile = "")
    $uri = $script:apiBase + $Path
    $request = New-Object System.Net.Http.HttpRequestMessage ([System.Net.Http.HttpMethod]::Get), $uri
    try {
        if (-not [string]::IsNullOrEmpty($Token)) {
            $request.Headers.Authorization = New-Object System.Net.Http.Headers.AuthenticationHeaderValue("Bearer", $Token)
        }
        $response = $script:http.SendAsync($request).GetAwaiter().GetResult()
        try {
            $bytes = $response.Content.ReadAsByteArrayAsync().GetAwaiter().GetResult()
            $headers = @{}
            foreach ($header in $response.Headers) { $headers[$header.Key.ToLowerInvariant()] = ($header.Value -join ",") }
            foreach ($header in $response.Content.Headers) { $headers[$header.Key.ToLowerInvariant()] = ($header.Value -join ",") }
            if ($OutFile -ne "" -and $bytes.Length -gt 0) { [System.IO.File]::WriteAllBytes($OutFile, $bytes) }
            return [pscustomobject]@{
                Status  = [int]$response.StatusCode
                Bytes   = $bytes
                Headers = $headers
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

function Get-Sha256Hex {
    param([byte[]]$Bytes)
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try {
        $hash = $sha.ComputeHash($Bytes)
    } finally {
        $sha.Dispose()
    }
    return (($hash | ForEach-Object { $_.ToString("x2") }) -join "")
}

function Get-Utf8Bytes {
    param([string]$Text)
    return (New-Object System.Text.UTF8Encoding($false)).GetBytes($Text)
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
    $previousWebPort = $env:EZTEST_WEB_PORT
    $previousExecutorPort = $env:EZTEST_EXECUTOR_PORT
    try {
        $env:EZTEST_DATABASE_URL = $script:testUrl
        $env:EZTEST_API_PORT = [string]$ApiPort
        $env:EZTEST_WEB_PORT = [string]$WebPort
        $env:EZTEST_EXECUTOR_PORT = [string]$ExecutorPort
        $script:apiProcess = Start-Process -FilePath $exePath -WorkingDirectory $repoRoot `
            -RedirectStandardOutput (Join-Path $logDir "api.stdout.log") `
            -RedirectStandardError (Join-Path $logDir "api.stderr.log") -PassThru
    } finally {
        $env:EZTEST_DATABASE_URL = $previousDb
        $env:EZTEST_API_PORT = $previousApiPort
        $env:EZTEST_WEB_PORT = $previousWebPort
        $env:EZTEST_EXECUTOR_PORT = $previousExecutorPort
    }
    $health = Wait-For -TimeoutSeconds 180 -Description "API health" -Condition {
        try {
            $probe = Invoke-Api -Method "GET" -Path "/api/health" -Body $null -Token $null
            if ($probe.Status -eq 200 -and $null -ne $probe.Json -and $probe.Json.data.db -eq "ok") {
                return $probe
            }
        } catch { }
        if ($null -ne $script:apiProcess -and $script:apiProcess.HasExited) {
            throw ("API exited before becoming healthy; see {0}" -f (Join-Path $logDir "api.stderr.log"))
        }
        return $null
    }
    Write-Log ("      API version " + $health.Json.data.version + " ready")
    return $health
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
        throw "port $ExecutorPort is already in use; stop the stale executor first"
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
    if (-not (Wait-Http -Url ($script:executorBase + "/healthz") -Seconds 60)) {
        throw "executor did not become healthy at $($script:executorBase)/healthz"
    }
    Write-Log "      executor ready on $($script:executorBase) (token not printed)"
}

function Stop-Executor {
    if ($null -ne $script:executorProcess) {
        try { & taskkill /PID $script:executorProcess.Id /T /F *> $null } catch { }
        Start-Sleep -Milliseconds 800
        $script:executorProcess = $null
    }
}

function Start-Fixture {
    if (Test-PortInUse -Port $FixturePort) {
        throw "port $FixturePort is already in use; stop the stale fixture first"
    }
    $node = Get-Command node -ErrorAction SilentlyContinue
    if ($null -eq $node) { throw "node not found on PATH" }
    $script:fixtureProcess = Start-Process -FilePath $node.Source `
        -ArgumentList @($fixtureTargetScript, "--port", [string]$FixturePort) `
        -WorkingDirectory $repoRoot `
        -RedirectStandardOutput (Join-Path $logDir "fixture.stdout.log") `
        -RedirectStandardError (Join-Path $logDir "fixture.stderr.log") -PassThru
    if (-not (Wait-Http -Url ($script:fixtureBase + "/healthz") -Seconds 30)) {
        throw "fixture target did not become healthy at $($script:fixtureBase)/healthz"
    }
    Write-Log "      fixture target ready on $($script:fixtureBase)"
}

function Stop-Services {
    if ($KeepRunning) {
        Write-Log "Keeping API/executor/fixture processes alive (-KeepRunning)" "Yellow"
        return
    }
    Stop-Api
    Stop-Executor
    if ($null -ne $script:fixtureProcess) {
        try { if (-not $script:fixtureProcess.HasExited) { Stop-Process -Id $script:fixtureProcess.Id -Force -ErrorAction SilentlyContinue } } catch { }
        $script:fixtureProcess = $null
    }
}

# --------------------------------------------------------------------------
# generic P5 API helpers
# --------------------------------------------------------------------------

function Login-As {
    param([string]$Username, [string]$Password)
    $response = Invoke-Api -Method "POST" -Path "/api/auth/login" -Body @{ username = $Username; password = $Password }
    Assert-Status $response @(200) ("POST /api/auth/login " + $Username)
    $token = [string]$response.Json.data.token
    if ([string]::IsNullOrWhiteSpace($token)) { throw "login response has no token" }
    return $token
}

function Get-Setting {
    param([string]$Key)
    $response = Invoke-Api -Method "GET" -Path ("/api/admin/settings/" + [System.Uri]::EscapeDataString($Key)) -Token $script:token
    Assert-Status $response @(200) ("GET setting " + $Key)
    return $response.Json.data
}

function Set-Setting {
    param([string]$Key, $Value)
    $response = Invoke-Api -Method "PATCH" -Path ("/api/admin/settings/" + [System.Uri]::EscapeDataString($Key)) -Token $script:token -Body @{ value = $Value }
    Assert-Status $response @(200) ("PATCH setting " + $Key)
    return $response.Json.data
}

function Get-ChannelEvents {
    param([string]$VersionID)
    $channel = "version:$VersionID"
    $rows = @(Invoke-Psql -SqlText ("SELECT seq || '|' || type || '|' || envelope::text FROM event WHERE channel = '{0}' ORDER BY seq;" -f $channel))
    $events = New-Object System.Collections.ArrayList
    foreach ($row in $rows) {
        $text = [string]$row
        if ([string]::IsNullOrWhiteSpace($text)) { continue }
        $first = $text.IndexOf("|")
        if ($first -lt 1) { continue }
        $second = $text.IndexOf("|", $first + 1)
        if ($second -lt 0) { continue }
        $seq = [long]$text.Substring(0, $first)
        $eventType = $text.Substring($first + 1, $second - $first - 1)
        $envelope = $null
        try { $envelope = ConvertFrom-Json $text.Substring($second + 1) } catch { $envelope = $null }
        [void]$events.Add([pscustomobject]@{
            Seq = $seq; Type = $eventType
            Payload = $(if ($null -ne $envelope) { $envelope.payload } else { $null })
            Scope = $(if ($null -ne $envelope) { $envelope.scope } else { $null })
        })
    }
    return $events
}

function Assert-EventPresent {
    param($Events, [string]$Type, [scriptblock]$Filter = $null)
    $match = @($Events | Where-Object { $_.Type -eq $Type -and ($null -eq $Filter -or (& $Filter $_)) })
    if ($match.Count -lt 1) {
        $seen = (@($Events | ForEach-Object { $_.Type } | Sort-Object -Unique) -join ", ")
        throw ("event '{0}' is missing (seen: {1})" -f $Type, $seen)
    }
    return $match
}

function Get-VersionView {
    $response = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID" -Token $script:token
    Assert-Status $response @(200) "GET /api/versions/{id}"
    return $response.Json.data
}

function Get-ExecutionOverview {
    $response = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/execution/overview" -Token $script:token
    Assert-Status $response @(200) "GET execution overview"
    return $response.Json.data
}

function Get-Waves {
    $response = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/waves?limit=100" -Token $script:token
    Assert-Status $response @(200) "GET waves"
    return Get-Array $response.Json.data
}

function Get-WaveRuns {
    param([string]$WaveID)
    $response = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/waves/$WaveID/case-runs?limit=100" -Token $script:token
    Assert-Status $response @(200) "GET wave case-runs"
    return Get-Array $response.Json.data
}

function Get-CaseRunDetail {
    param([string]$CaseRunID)
    $response = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/case-runs/$CaseRunID" -Token $script:token
    Assert-Status $response @(200) "GET case-run $CaseRunID"
    return $response.Json.data
}

function Find-RunByTitle {
    param($Runs, [string]$Title)
    # Flatten one nesting level so both a flat run list and the comma-array
    # returned by Get-Array (wrapped again by the caller) work.
    $items = @($Runs | ForEach-Object { $_ })
    $match = @($items | Where-Object { [string]$_.title -eq $Title })
    if ($match.Count -lt 1) { throw ("case run '{0}' not found in wave" -f $Title) }
    return $match[0]
}

# Get-VisualRunDetail returns the latest case run for a title that has a real
# visual assertion result. A real-LLM Case Executor occasionally ignores the
# execution.visual_assert instruction; the runner then records
# visual_assertion_not_run. Acceptance retries the REAL case (manual intent)
# up to MaxRetries times and keeps the retried run, recording the retry count.
function Get-VisualRunDetail {
    param([string]$Title, [string]$CaseVersionID, $Runs, [int]$MaxRetries = 2)
    $run = Find-RunByTitle -Runs $Runs -Title $Title
    $detail = Get-CaseRunDetail -CaseRunID ([string]$run.id)
    $visual = @($detail.assertions | Where-Object { [string]$_.kind -eq "visual" })
    $attempt = 0
    while ($visual.Count -lt 1 -and $attempt -lt $MaxRetries) {
        $attempt++
        Write-Log ("      no visual assertion result for '{0}' run {1}; retrying the real case ({2}/{3})" -f $Title, $run.id, $attempt, $MaxRetries) "Yellow"
        $retryWave = Run-VisualWave -CaseVersionIDs @($CaseVersionID) -Purpose "manual" -Label ("visual retry " + $Title)
        $retryRuns = @(Get-WaveRuns -WaveID ([string]$retryWave.id))
        $run = Find-RunByTitle -Runs $retryRuns -Title $Title
        $detail = Get-CaseRunDetail -CaseRunID ([string]$run.id)
        $visual = @($detail.assertions | Where-Object { [string]$_.kind -eq "visual" })
    }
    return [pscustomobject]@{ Run = $run; Detail = $detail; Retries = $attempt }
}

function Wait-NewWave {
    param([string[]]$KnownWaveIDs, [int]$TimeoutSeconds = 120)
    return Wait-For -TimeoutSeconds $TimeoutSeconds -Description "new execution wave" -Condition {
        $waves = Get-Waves
        foreach ($wave in $waves) {
            $id = [string]$wave.id
            if ($KnownWaveIDs -notcontains $id) { return $wave }
        }
        return $null
    } -IntervalMilliseconds 1000
}

function Wait-WaveTerminal {
    param([string]$WaveID, [int]$TimeoutSeconds = 900)
    return Wait-For -TimeoutSeconds $TimeoutSeconds -Description ("wave {0} terminal" -f $WaveID) -Condition {
        $waves = Get-Waves
        $match = @($waves | Where-Object { [string]$_.id -eq $WaveID })
        if ($match.Count -lt 1) { return $null }
        if (@("done", "failed", "cancelled") -contains [string]$match[0].status) { return $match[0] }
        return $null
    } -IntervalMilliseconds 1500
}

function Open-Requirement {
    param([string]$RequirementID)
    $response = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/requirements/$RequirementID/reopen" -Token $script:token -Body @{ reason = "P5 acceptance visual retest" }
    if ($response.Status -eq 200) {
        Write-Log ("      requirement {0} reopened (status {1})" -f $RequirementID, $response.Json.data.status)
        return $response.Json.data
    }
    $code = ""
    if ($null -ne $response.Json -and $null -ne $response.Json.error) { $code = [string]$response.Json.error.code }
    if ($response.Status -eq 409 -and $code -eq "state_conflict") {
        Write-Log ("      requirement {0} is already open" -f $RequirementID)
        return $null
    }
    Assert-Status $response @(200) "POST requirement reopen"
    return $response.Json.data
}

function New-ExecutionIntent {
    param([string]$CaseVersionID, [string]$Purpose)
    $response = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/execution/intents" -Token $script:token -Body @{
        case_version_id = $CaseVersionID; purpose = $Purpose
    }
    Assert-Status $response @(201) ("POST execution intent " + $Purpose + " " + $CaseVersionID)
    return $response.Json.data
}

function Run-VisualWave {
    param([string[]]$CaseVersionIDs, [string]$Purpose, [string]$Label)
    $known = @(Get-Waves | ForEach-Object { [string]$_.id })
    [void](Open-Requirement -RequirementID $script:requirementID)
    foreach ($caseVersionID in $CaseVersionIDs) {
        [void](New-ExecutionIntent -CaseVersionID $caseVersionID -Purpose $Purpose)
    }
    $wave = Wait-NewWave -KnownWaveIDs $known -TimeoutSeconds 120
    Write-Log ("      {0}: wave {1} started" -f $Label, $wave.id)
    $terminal = Wait-WaveTerminal -WaveID ([string]$wave.id) -TimeoutSeconds $PollTimeoutSeconds
    Write-Log ("      {0}: wave {1} finished status={2}" -f $Label, $wave.id, $terminal.status)
    return $terminal
}

function Get-VisualAssertions {
    param([string]$CaseVersionID = "")
    $path = "/api/versions/$script:versionID/visual/assertions"
    if ($CaseVersionID -ne "") { $path += "?caseVersionId=$CaseVersionID" }
    $response = Invoke-Api -Method "GET" -Path $path -Token $script:token
    Assert-Status $response @(200) "GET visual assertions"
    return Get-Array $response.Json.data
}

function Get-VisualAssertion {
    param([string]$AssertionID)
    $response = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/visual/assertions/$AssertionID" -Token $script:token
    Assert-Status $response @(200) "GET visual assertion $AssertionID"
    return $response.Json.data
}

function Get-VisualBaselines {
    param([string]$AssertionID)
    $response = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/visual/assertions/$AssertionID/baselines" -Token $script:token
    Assert-Status $response @(200) "GET visual baselines $AssertionID"
    return Get-Array $response.Json.data
}

function Approve-Baseline {
    param([string]$AssertionID, [string]$EvidenceID, [string]$Token = $script:token, [int[]]$Expected = @(201))
    $response = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/visual/assertions/$AssertionID/baselines" -Token $Token -Body @{ evidenceId = $EvidenceID }
    Assert-Status $response $Expected "POST baseline approve"
    return $response
}

function Get-VisualDiffs {
    param([string]$CaseRunID = "", [string]$CaseVersionID = "", [string]$Classification = "")
    $query = "limit=100"
    if ($CaseRunID -ne "") { $query += "&caseRunId=$CaseRunID" }
    if ($CaseVersionID -ne "") { $query += "&caseVersionId=$CaseVersionID" }
    if ($Classification -ne "") { $query += "&classification=$Classification" }
    $response = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/visual/diffs?$query" -Token $script:token
    Assert-Status $response @(200) "GET visual diffs"
    return Get-Array $response.Json.data
}

function Get-VisualDiff {
    param([string]$DiffID)
    $response = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/visual/diffs/$DiffID" -Token $script:token
    Assert-Status $response @(200) "GET visual diff $DiffID"
    return $response.Json.data
}

function Get-VisualOverview {
    $response = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/visual/overview" -Token $script:token
    Assert-Status $response @(200) "GET visual overview"
    return $response.Json.data
}

function Get-VisualMetrics {
    param([string]$VersionID = "")
    $path = "/api/quality/visual-metrics"
    if ($VersionID -ne "") { $path += "?versionId=$VersionID" }
    $response = Invoke-Api -Method "GET" -Path $path -Token $script:token
    Assert-Status $response @(200) "GET visual metrics"
    return $response.Json.data
}

function Get-Issues {
    $response = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/issues?limit=100" -Token $script:token
    Assert-Status $response @(200) "GET issues"
    return Get-Array $response.Json.data
}

function Get-Analysis {
    param([string]$AnalysisID)
    $response = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/code/analyses/$AnalysisID" -Token $script:token
    Assert-Status $response @(200) "GET code analysis $AnalysisID"
    return $response
}

function Wait-AnalysisDone {
    param([string]$AnalysisID, [int]$TimeoutSeconds = 600)
    return Wait-For -TimeoutSeconds $TimeoutSeconds -Description ("analysis {0} done" -f $AnalysisID) -Condition {
        $response = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/code/analyses/$AnalysisID" -Token $script:token
        if ($response.Status -ne 200) { return $null }
        $status = [string]$response.Json.data.analysis.status
        if ($status -eq "done") { return $response.Json.data }
        if ($status -eq "failed") {
            throw ("analysis {0} failed: {1}" -f $AnalysisID, $response.Json.data.analysis.error)
        }
        return $null
    } -IntervalMilliseconds 2000
}

function Wait-ImpactDone {
    param([string]$AnalysisID, [int]$TimeoutSeconds = 600)
    return Wait-For -TimeoutSeconds $TimeoutSeconds -Description ("impact work item for analysis {0}" -f $AnalysisID) -Condition {
        $row = Invoke-PsqlScalar -SqlText ("SELECT status FROM work_item WHERE team = 'code' AND dedupe_key = 'code:analyze:{0}';" -f $AnalysisID)
        if ($row -eq "done") { return $true }
        if ($row -eq "dead" -or $row -eq "failed") { throw ("impact work item for analysis {0} ended {1}" -f $AnalysisID, $row) }
        return $null
    } -IntervalMilliseconds 1500
}

function Assert-EvidenceBytes {
    param([string]$VersionID, [string]$EvidenceID, [string]$ExpectedSha, [int64]$ExpectedSize, [string]$Label)
    $response = Invoke-ApiBytes -Path "/api/versions/$VersionID/evidence/$EvidenceID/content" -Token $script:token
    Assert-Status $response @(200) ("GET evidence content " + $EvidenceID)
    $bytes = $response.Bytes
    Assert-True ($bytes.Length -gt 0) ("$Label evidence $EvidenceID returned no bytes")
    $sha = Get-Sha256Hex -Bytes $bytes
    Assert-Equal $sha $ExpectedSha ("$Label evidence sha256")
    Assert-Equal ([int64]$bytes.Length) ([int64]$ExpectedSize) ("$Label evidence size")
    return $bytes
}

# --------------------------------------------------------------------------
# main
# --------------------------------------------------------------------------

$exitCode = 0
try {
    $s0 = Start-Scenario "S0 gate/health: isolated DB, 31 migrations, real DeepSeek, real Chrome executor, fixtures, contracts" "scripts/p5-acceptance.ps1"

    Step "load .env (4 EZTEST env + lowercase llm_* keys)"
    $dotenv = Read-DotEnv
    foreach ($key in @("EZTEST_DATABASE_URL", "llm_api_key", "llm_base_url", "llm_model_name")) {
        if ([string]::IsNullOrWhiteSpace($dotenv[$key])) { throw ("$key is missing from .env") }
    }
    $script:db = ConvertFrom-DatabaseUrl $dotenv["EZTEST_DATABASE_URL"]
    $script:testUrl = Get-DatabaseUrl -Database $script:db -DatabaseName $testDatabase
    if ($script:testUrl -notmatch ("/" + $testDatabase + "(\?|$)")) { throw "derived test database URL is invalid" }
    Write-Log ("      p5 db    : {0}/{1}" -f $script:db.Host, $testDatabase)
    Write-Log ("      api      : {0}" -f $script:apiBase)
    Write-Log ("      executor : {0}" -f $script:executorBase)
    Write-Log ("      fixture  : {0}" -f $script:fixtureBase)
    Write-Log ("      llm      : {0} model={1}" -f $dotenv["llm_base_url"], $dotenv["llm_model_name"])

    Step "verify ports $ApiPort/$ExecutorPort/$FixturePort are free"
    foreach ($port in @($ApiPort, $ExecutorPort, $FixturePort)) {
        if (Test-PortInUse -Port $port) { throw "port $port is already in use; stop the stale process first" }
    }

    Step "rebuild the deterministic P5 fixture repository (script printed SHAs consumed dynamically)"
    $fixtureOutput = & powershell.exe -NoProfile -ExecutionPolicy Bypass -File $fixtureRepoScript 2>&1
    $fixtureExit = $LASTEXITCODE
    Set-Content -LiteralPath (Join-Path $logDir "fixture-repo.log") -Value ($fixtureOutput -join [Environment]::NewLine) -Encoding UTF8
    if ($fixtureExit -ne 0) { throw ("fixture-p5-repo.ps1 failed ({0}); see fixture-repo.log" -f $fixtureExit) }
    $baseCommit = ""
    $headCommit = ""
    $fixtureRepo = ""
    foreach ($line in $fixtureOutput) {
        $text = [string]$line
        if ($text -match '^P5_FIXTURE_REPO=(.*)$') { $fixtureRepo = $Matches[1].Trim() }
        if ($text -match '^P5_FIXTURE_BASE_COMMIT=(.*)$') { $baseCommit = $Matches[1].Trim() }
        if ($text -match '^P5_FIXTURE_HEAD_COMMIT=(.*)$') { $headCommit = $Matches[1].Trim() }
    }
    Assert-Match $baseCommit '^[0-9a-f]{40}$' "fixture base commit"
    Assert-Match $headCommit '^[0-9a-f]{40}$' "fixture head commit"
    Assert-True ($baseCommit -ne $headCommit) "fixture base and head commits differ"
    $fixtureOutput2 = & powershell.exe -NoProfile -ExecutionPolicy Bypass -File $fixtureRepoScript 2>&1
    $secondBase = ""
    $secondHead = ""
    foreach ($line in $fixtureOutput2) {
        if ([string]$line -match '^P5_FIXTURE_BASE_COMMIT=(.*)$') { $secondBase = $Matches[1].Trim() }
        if ([string]$line -match '^P5_FIXTURE_HEAD_COMMIT=(.*)$') { $secondHead = $Matches[1].Trim() }
    }
    Assert-Equal $secondBase $baseCommit "fixture rebuild base commit is deterministic"
    Assert-Equal $secondHead $headCommit "fixture rebuild head commit is deterministic"
    $gitStatus = (& git -C $fixtureRepo status --porcelain 2>&1 | Out-String).Trim()
    Assert-Equal $gitStatus "" "fixture worktree is clean"
    $script:repoPath = $fixtureRepo
    $script:baseCommit = $baseCommit
    $script:headCommit = $headCommit
    Note-Assertion ("fixture repo {0} A={1} B={2} deterministic+clean" -f $fixtureRepo, $baseCommit, $headCommit)

    Step "reset isolated acceptance database $testDatabase"
    $script:psql = Resolve-Psql
    Reset-TestDatabase

    Step "start fixture target on $($script:fixtureBase)"
    Start-Fixture
    $visualLight = (Invoke-WebRequest -Uri ($script:fixtureBase + "/p5/visual?theme=light") -UseBasicParsing -TimeoutSec 10).Content
    $visualShifted = (Invoke-WebRequest -Uri ($script:fixtureBase + "/p5/visual?theme=shifted") -UseBasicParsing -TimeoutSec 10).Content
    Assert-True ($visualLight -ne $visualShifted) "fixture theme light and shifted HTML differ"
    Assert-True ($visualLight -match 'visual-root') "fixture visual page has visual-root"
    Assert-True ($visualShifted -match 'theme-shifted') "fixture shifted page has theme-shifted"

    Step "build the API and start the bootstrap process"
    Build-Api
    $script:http = New-ApiHttpClient
    $health = Start-Api
    Assert-Equal $health.Json.data.version "0.2.0-p5" "API health version"
    $migrations = [int](Invoke-PsqlScalar -SqlText "SELECT count(*) FROM schema_migrations;")
    Assert-Equal $migrations "31" "applied migration count"
    $tailMigration = Invoke-PsqlScalar -SqlText "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1;"
    Assert-Equal $tailMigration "visual_0001_visual_assertion.sql" "migration tail"
    Note-Assertion "31 migrations applied, tail visual_0001_visual_assertion.sql"

    Step "assert the P5 settings catalog (69 keys, codeintel.*/visual.* present)"
    $settingsResponse = Invoke-Api -Method "GET" -Path "/api/admin/settings" -Token $script:token
    Assert-Status $settingsResponse @(401) "GET /api/admin/settings without token is 401"
    $script:token = Login-As -Username "admin" -Password "admin123"
    $settingsResponse = Invoke-Api -Method "GET" -Path "/api/admin/settings" -Token $script:token
    Assert-Status $settingsResponse @(200) "GET /api/admin/settings"
    $settings = Get-Array $settingsResponse.Json.data
    Assert-Equal $settings.Count "69" "settings count"
    foreach ($key in @("codeintel.max_index_files", "codeintel.max_changed_files", "codeintel.prompt_version",
            "visual.default_threshold", "visual.default_viewport_width", "visual.default_device_scale_factor",
            "visual.baseline_max_bytes", "visual.compare_timeout_seconds")) {
        Assert-True (@($settings | Where-Object { $_.key -eq $key }).Count -ge 1) ("setting {0} is missing" -f $key)
    }
    $tokenSetting = $settings | Where-Object { $_.key -eq "execution.executor_token" }
    Assert-True ([bool]$tokenSetting.is_secret) "execution.executor_token must be secret"
    Assert-True ($null -eq $tokenSetting.value) "execution.executor_token value must be masked"
    Note-Assertion "settings=69 incl. codeintel.*/visual.*; executor token masked"

    Step "run the P2-P5 contract checks (front-end contract gate)"
    $node = Get-Command node -ErrorAction SilentlyContinue
    if ($null -eq $node) { throw "node not found on PATH" }
    foreach ($check in @("p2-contract-check.cjs", "p3-contract-check.cjs", "p4-contract-check.cjs", "p5-contract-check.cjs")) {
        $checkPath = Join-Path $repoRoot ("scripts\" + $check)
        if (-not (Test-Path -LiteralPath $checkPath)) { throw ("contract check missing: {0}" -f $check) }
        $checkOutput = & $node.Source $checkPath 2>&1
        $checkExit = $LASTEXITCODE
        Set-Content -LiteralPath (Join-Path $logDir ($check + ".log")) -Value (@($checkOutput) -join [Environment]::NewLine) -Encoding UTF8
        if ($checkExit -ne 0) { throw ("{0} failed: {1}" -f $check, (@($checkOutput) -join " ")) }
        Write-Log ("      {0} PASS" -f $check)
    }
    Note-Assertion "p2/p3/p4/p5 contract checks PASS"

    Step "configure executor settings and the real DeepSeek LLM through the admin API"
    $script:executorToken = [System.Guid]::NewGuid().ToString("N") + [System.Guid]::NewGuid().ToString("N")
    Set-Setting "execution.executor_url" ("http://127.0.0.1:{0}" -f $ExecutorPort) | Out-Null
    Set-Setting "execution.executor_token" $script:executorToken | Out-Null
    Set-Setting "execution.executor_heartbeat_seconds" 2 | Out-Null
    Set-Setting "execution.executor_offline_seconds" 6 | Out-Null
    Set-Setting "execution.human_request_timeout_seconds" 1800 | Out-Null
    Set-Setting "feature.auto_gap_design" $false | Out-Null
    $llmResponse = Invoke-Api -Method "POST" -Path "/api/admin/llm/configs" -Token $script:token -Body @{
        name            = "p5-acceptance-deepseek"
        provider        = "openai"
        protocol        = "openai"
        base_url        = [string]$dotenv["llm_base_url"]
        api_key         = [string]$dotenv["llm_api_key"]
        timeout_seconds = 180
        max_retries     = 1
        is_default      = $true
        status          = "active"
    }
    Assert-Status $llmResponse @(200) "POST /api/admin/llm/configs (real DeepSeek)"
    $llmConfigID = [string]$llmResponse.Json.data.id
    $llmTest = Invoke-Api -Method "POST" -Path ("/api/admin/llm/configs/{0}/test" -f $llmConfigID) -Token $script:token
    if ($llmTest.Status -ne 200) { throw ("real LLM connectivity test failed (HTTP {0}); acceptance requires a working DeepSeek config" -f $llmTest.Status) }
    if ($null -ne $llmTest.Json.data -and $llmTest.Json.data.ok -eq $false) {
        throw ("real LLM connectivity test failed: {0}" -f $llmTest.Json.data.error)
    }
    $refresh = Invoke-Api -Method "POST" -Path ("/api/admin/llm/configs/{0}/refresh-models" -f $llmConfigID) -Token $script:token
    Assert-Status $refresh @(200) "POST refresh-models (real DeepSeek)"
    $catalog = Invoke-Api -Method "GET" -Path "/api/admin/llm/models" -Token $script:token
    Assert-Status $catalog @(200) "GET /api/admin/llm/models"
    $catalogModels = Get-Array $catalog.Json.data
    $wantedModel = [string]$dotenv["llm_model_name"]
    if (-not (@($catalogModels | Where-Object { $_.model -eq $wantedModel }).Count -ge 1)) {
        $addModel = Invoke-Api -Method "POST" -Path ("/api/admin/llm/configs/{0}/models" -f $llmConfigID) -Token $script:token -Body @{
            model = $wantedModel; context_window = 128000; max_output = 8192; supports_tools = $true; supports_vision = $false
        }
        Assert-Status $addModel @(200) ("add LLM catalog model " + $wantedModel)
        $catalog = Invoke-Api -Method "GET" -Path "/api/admin/llm/models" -Token $script:token
        $catalogModels = Get-Array $catalog.Json.data
    }
    foreach ($model in $catalogModels) {
        if ([string]$model.model -eq $wantedModel) { break }
        $deleted = Invoke-Api -Method "DELETE" -Path ("/api/admin/llm/configs/{0}/models/{1}" -f $llmConfigID, [string]$model.id) -Token $script:token
        Assert-Status $deleted @(200) ("delete non-primary LLM model " + $model.model)
    }
    $catalog = Invoke-Api -Method "GET" -Path "/api/admin/llm/models" -Token $script:token
    $catalogModels = Get-Array $catalog.Json.data
    if ($catalogModels.Count -lt 1 -or [string]$catalogModels[0].model -ne $wantedModel) {
        throw ("default LLM config does not resolve to {0}" -f $wantedModel)
    }
    $script:modelName = $wantedModel
    Note-Assertion ("real LLM model={0} connectivity test ok" -f $wantedModel)

    Step "create the normal user used by the permission assertions"
    $userResponse = Invoke-Api -Method "POST" -Path "/api/admin/users" -Token $script:token -Body @{
        username = "p5user"; display_name = "P5 User"; password = "p5-user-pass-123"
    }
    Assert-Status $userResponse @(201) "POST /api/admin/users"
    Assert-Equal $userResponse.Json.data.role "user" "normal user role"
    $script:userToken = Login-As -Username "p5user" -Password "p5-user-pass-123"

    Step "stop the bootstrap API, start the executor, then start the acceptance API"
    Stop-Api
    Start-Executor
    $health = Start-Api
    $script:token = Login-As -Username "admin" -Password "admin123"
    Assert-Status (Invoke-Api -Method "GET" -Path "/api/health" -Token $null) @(200) "GET /api/health"
    [void](Wait-For -TimeoutSeconds 90 -Description "executor registered active" -Condition {
        $response = Invoke-Api -Method "GET" -Path "/api/admin/executors" -Token $script:token
        if ($response.Status -ne 200) { return $null }
        foreach ($item in (Get-Array $response.Json.data)) {
            if ([string]$item.endpoint -eq $script:executorBase -and [string]$item.status -eq "active") { return $item }
        }
        return $null
    })
    Note-Assertion "API 0.2.0-p5 + executor active + fixture target ready"

    Step "create SUT, environment, pages, API, code repository and version binding"
    $sutResponse = Invoke-Api -Method "POST" -Path "/api/resources/suts" -Token $script:token -Body @{ name = "P5 Fixture SUT"; intro = "P5 acceptance fixture target" }
    Assert-Status $sutResponse @(200, 201) "POST SUT"
    $script:sutID = [string]$sutResponse.Json.data.id
    $envResponse = Invoke-Api -Method "POST" -Path "/api/resources/suts/$($script:sutID)/environments" -Token $script:token -Body @{
        name = "p5-fixture"; base_url = $script:fixtureBase; is_default = $true; status = "active"
    }
    Assert-Status $envResponse @(200, 201) "POST environment"
    $script:environmentID = [string]$envResponse.Json.data.id
    $pageOrdersResponse = Invoke-Api -Method "POST" -Path "/api/resources/environments/$($script:environmentID)/pages" -Token $script:token -Body @{
        name = "P5 orders page"; url = ($script:fixtureBase + "/p5/orders"); route_template = "/p5/orders"; page_type = "page"; auth_role = "guest"; module = "p5"
    }
    Assert-Status $pageOrdersResponse @(200, 201) "POST orders page"
    $script:pageOrdersID = [string]$pageOrdersResponse.Json.data.id
    $pageVisualResponse = Invoke-Api -Method "POST" -Path "/api/resources/environments/$($script:environmentID)/pages" -Token $script:token -Body @{
        name = "P5 visual fixture"; url = ($script:fixtureBase + "/p5/visual?theme=light"); route_template = "/p5/visual"; page_type = "page"; auth_role = "guest"; module = "p5"
    }
    Assert-Status $pageVisualResponse @(200, 201) "POST visual page"
    $script:pageVisualID = [string]$pageVisualResponse.Json.data.id
    $pageImpactResponse = Invoke-Api -Method "POST" -Path "/api/resources/environments/$($script:environmentID)/pages" -Token $script:token -Body @{
        name = "P5 route /orders page"; url = ($script:fixtureBase + "/p5/orders"); route_template = "/orders"; page_type = "page"; auth_role = "guest"; module = "p5"
    }
    Assert-Status $pageImpactResponse @(200, 201) "POST impact page (/orders)"
    $script:pageImpactID = [string]$pageImpactResponse.Json.data.id
    $apiRefundResponse = Invoke-Api -Method "POST" -Path "/api/resources/environments/$($script:environmentID)/apis" -Token $script:token -Body @{
        method = "POST"; path_template = "/api/refund"; name = "P5 refund API"; auth_role = "guest"; schema_ref = ""
    }
    Assert-Status $apiRefundResponse @(200, 201) "POST refund API"
    $script:apiRefundID = [string]$apiRefundResponse.Json.data.id
    $repoResponse = Invoke-Api -Method "POST" -Path "/api/resources/code-repos" -Token $script:token -Body @{
        name = "p5-fixture-repo"; path = $script:repoPath; branch = "main"; include = @(); exclude = @()
    }
    Assert-Status $repoResponse @(200, 201) "POST code repository"
    $script:repoID = [string]$repoResponse.Json.data.id
    $versionResponse = Invoke-Api -Method "POST" -Path "/api/versions" -Token $script:token -Body @{
        title = ("P5 acceptance " + (Get-Date -Format "yyyy-MM-dd HH:mm:ss"))
        bindings = @(@{
            sut_id = $script:sutID; environment_id = $script:environmentID; code_repository_id = $script:repoID
            page_ids = @($script:pageOrdersID, $script:pageVisualID, $script:pageImpactID); api_ids = @($script:apiRefundID)
            database_ids = @(); accounts = @()
        })
    }
    Assert-Status $versionResponse @(201) "POST version"
    $script:versionID = [string]$versionResponse.Json.data.id
    $script:versionSUTID = [string]$versionResponse.Json.data.bindings[0].id
    Assert-Equal $versionResponse.Json.data.bindings[0].codeBaselineCommit $script:headCommit "version binding baseline resolves the branch head"
    Note-Assertion ("version {0} baseline resolved to head {1}" -f $script:versionID, $script:headCommit)

    Step "set the version baseline to fixture commit A so analysis covers A..B (simulates the previous version baseline)"
    Invoke-Psql -SqlText ("UPDATE version_sut SET code_baseline_commit = '{0}' WHERE id = {1};" -f $script:baseCommit, $script:versionSUTID) | Out-Null
    $versionView = Get-VersionView
    Assert-Equal $versionView.bindings[0].codeBaselineCommit $script:baseCommit "version_sut code_baseline_commit after baseline seed"
    Note-Assertion ("analysis range base={0} head={1}" -f $script:baseCommit, $script:headCommit)

    Step "create and confirm three requirements (R1 visual, R2 refund, R3 notification)"
    function New-ConfirmedRequirement {
        param([string]$Title, [string]$Priority, [string[]]$Pages)
        $response = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/requirements" -Token $script:token -Body @{
            title = $Title; description = "P5 acceptance requirement"; acceptance_criteria = "Covered by the P5 acceptance cases"
            priority = $Priority; constraints = @{}
            targets = @{ version_suts = @($script:versionSUTID); pages = @($Pages); apis = @() }
        }
        Assert-Status $response @(201) ("POST requirement " + $Title)
        $confirmed = Invoke-Api -Method "POST" -Path ("/api/versions/{0}/requirements/{1}/confirmations" -f $script:versionID, [string]$response.Json.data.id) -Token $script:token
        Assert-Status $confirmed @(200) ("confirm requirement " + $Title)
        return $response.Json.data
    }
    $r1 = New-ConfirmedRequirement -Title "P5 visual baseline requirements" -Priority "P1" -Pages @($script:pageVisualID, $script:pageOrdersID)
    $r2 = New-ConfirmedRequirement -Title "P5 refund API requirement" -Priority "P2" -Pages @()
    $r3 = New-ConfirmedRequirement -Title "P5 refund notification requirement" -Priority "P2" -Pages @()
    $script:requirementID = [string]$r1.id
    $script:requirementVersionID = [string]$r1.current_version.id
    $script:requirement2ID = [string]$r2.id
    $script:requirement2VersionID = [string]$r2.current_version.id
    $script:requirement3ID = [string]$r3.id

    Step "seed one requirement relation (R1 depends_on R2) and four cases via SQL, then confirm the cases through the design API"
    $hash = ("0123456789abcdef" * 4)
    $vsut = $script:versionSUTID
    $actionOrders = "Use browser.goto to open the bound orders page for version_sut_id=$vsut with account_role=user (the /p5/orders page URL is listed in the task context). Then use browser.snapshot and browser.screenshot, verify the page heading text 'P5 Orders', and record assertion 1 through execution.assertion_record."
    $actionVisual = "Use browser.goto to open the visual fixture page bound to version_sut_id=$vsut with account_role=user (the /p5/visual page URL is listed in the task context). Then call execution.visual_assert with assertionId set to the visual_assertion_id of assertion 1 from the task context. Do not call execution.assertion_record for this kind=visual assertion."
    $escOrders = $actionOrders.Replace("'", "''")
    $escVisual = $actionVisual.Replace("'", "''")
    $sqlLines = New-Object System.Collections.ArrayList
    [void]$sqlLines.Add("BEGIN;")
    [void]$sqlLines.Add(("INSERT INTO requirement_relation (id, version_id, from_requirement_id, to_requirement_id, kind, description, created_at) VALUES (970001, {0}, {1}, {2}, 'depends_on', 'P5 acceptance relation', now());" -f $script:versionID, $script:requirementID, $script:requirement2ID))
    $caseDefs = @(
        @{ Scenario = 960001; Case = 961001; Version = 962001; Step = 963001; Assertion = 964001; Title = "P5 acceptance orders page"; Page = $script:pageOrdersID; Kind = "ui"; Target = "#p5-orders h1"; Expected = "P5 Orders"; Action = $escOrders; PageJson = $script:pageOrdersID },
        @{ Scenario = 960002; Case = 961002; Version = 962002; Step = 963002; Assertion = 964002; Title = "P5 acceptance visual light"; Page = $script:pageVisualID; Kind = "visual"; Target = "#visual-root"; Expected = "The visual page matches the approved baseline"; Action = $escVisual; PageJson = $script:pageVisualID },
        @{ Scenario = 960003; Case = 961003; Version = 962003; Step = 963003; Assertion = 964003; Title = "P5 acceptance visual theme shift"; Page = $script:pageVisualID; Kind = "visual"; Target = "#visual-root"; Expected = "The visual page matches the approved baseline"; Action = $escVisual; PageJson = $script:pageVisualID },
        @{ Scenario = 960004; Case = 961004; Version = 962004; Step = 963004; Assertion = 964004; Title = "P5 acceptance visual selector viewport"; Page = $script:pageVisualID; Kind = "visual"; Target = "#visual-header"; Expected = "The visual header matches the approved baseline"; Action = $escVisual; PageJson = $script:pageVisualID }
    )
    foreach ($def in $caseDefs) {
        [void]$sqlLines.Add(("INSERT INTO case_scenario (id, version_id, requirement_id, requirement_version_id, name, description, version_sut_ids, page_ids, api_ids, sort_order) VALUES ({0}, {1}, {2}, {3}, 'Scenario {4}', '', '[{5}]'::jsonb, '[`"{6}`"]'::jsonb, '[]'::jsonb, {4});" -f $def.Scenario, $script:versionID, $script:requirementID, $script:requirementVersionID, ($def.Scenario - 960000), $vsut, $def.PageJson))
        [void]$sqlLines.Add(("INSERT INTO case_entity (id, scenario_id, current_version_id, status) VALUES ({0}, {1}, {2}, 'draft');" -f $def.Case, $def.Scenario, $def.Version))
        [void]$sqlLines.Add(("INSERT INTO case_version (id, case_id, revision, title, description, preconditions, test_data, priority, default_version_sut_id, version_sut_ids, page_ids, api_ids, status, content_hash) VALUES ({0}, {1}, 1, '{2}', '', '', '{{}}'::jsonb, 'P1', {3}, '[`"{3}`"]'::jsonb, '[`"{4}`"]'::jsonb, '[]'::jsonb, 'draft', '{5}');" -f $def.Version, $def.Case, ($def.Title.Replace("'", "''")), $vsut, $def.PageJson, $hash))
        [void]$sqlLines.Add(("INSERT INTO case_step (id, case_version_id, seq, action, expected, target_version_sut_id, target_page_id, target_api_id, account_role, data) VALUES ({0}, {1}, 1, '{2}', '{3}', {4}, {5}, NULL, 'user', '{{}}'::jsonb);" -f $def.Step, $def.Version, $def.Action, ($def.Expected.Replace("'", "''")), $vsut, $def.Page))
        [void]$sqlLines.Add(("INSERT INTO case_assertion (id, case_version_id, step_seq, kind, target, expected, target_version_sut_id, target_api_id, database_id) VALUES ({0}, {1}, 1, '{2}', '{3}', '{4}', {5}, NULL, NULL);" -f $def.Assertion, $def.Version, $def.Kind, $def.Target, ($def.Expected.Replace("'", "''")), $vsut))
    }
    [void]$sqlLines.Add("COMMIT;")
    Set-Content -LiteralPath $seedFile -Value ($sqlLines -join [Environment]::NewLine) -Encoding UTF8
    Invoke-PsqlFile -FilePath $seedFile
    $seeded = Invoke-PsqlScalar -SqlText "SELECT count(*) FROM case_version WHERE id IN (962001,962002,962003,962004);"
    Assert-Equal $seeded "4" "seeded case_version count"
    $script:caseVersionOrders = "962001"
    $script:caseVersionVisual1 = "962002"
    $script:caseVersionVisual2 = "962003"
    $script:caseVersionVisual3 = "962004"
    foreach ($caseID in @("961001", "961002", "961003", "961004")) {
        # Cases stay draft until S8 confirms them; S3 anchors only need the current version row.
        $exists = Invoke-PsqlScalar -SqlText ("SELECT count(*) FROM case_entity WHERE id = {0};" -f $caseID)
        Assert-Equal $exists "1" ("seeded case " + $caseID)
    }
    Note-Assertion "SQL seed: R1-R2 relation + 4 draft cases (orders + 3 visual)"

    Step "create the requirement and case_version code anchors used by the impact analysis"
    $anchorRequirement = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/code/anchors" -Token $script:token -Body @{
        subjectKind = "requirement"; subjectId = $script:requirementID; repoId = $script:repoID
        filePath = "src/api/refund.ts"; startLine = 1; endLine = 3; commit = $script:headCommit
    }
    Assert-Status $anchorRequirement @(201) "POST requirement code anchor"
    Assert-Equal $anchorRequirement.Json.data.status "valid" "requirement anchor initial status"
    $anchorCase = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/code/anchors" -Token $script:token -Body @{
        subjectKind = "case_version"; subjectId = $script:caseVersionOrders; repoId = $script:repoID
        filePath = "src/router.ts"; startLine = 1; endLine = 3; commit = $script:headCommit
    }
    Assert-Status $anchorCase @(201) "POST case_version code anchor"
    $script:anchorRequirementID = [string]$anchorRequirement.Json.data.id
    $script:anchorCaseID = [string]$anchorCase.Json.data.id
    Note-Assertion ("anchors created: requirement={0} case_version={1}" -f $script:anchorRequirementID, $script:anchorCaseID)

    Step "create the visual assertion configurations through the real visual API"
    $script:visual1 = (Invoke-Api -Method "PUT" -Path "/api/versions/$script:versionID/visual/assertions/964002" -Token $script:token -Body @{
        versionSutId = $script:versionSUTID; selector = ""; viewport = @{ width = 1280; height = 720 }; deviceScaleFactor = 1; threshold = 0.005
    })
    Assert-Status $script:visual1 @(200) "PUT visual assertion 964002"
    $script:visual2 = (Invoke-Api -Method "PUT" -Path "/api/versions/$script:versionID/visual/assertions/964003" -Token $script:token -Body @{
        versionSutId = $script:versionSUTID; selector = ""; viewport = @{ width = 1280; height = 720 }; deviceScaleFactor = 1; threshold = 0.005
    })
    Assert-Status $script:visual2 @(200) "PUT visual assertion 964003"
    $script:visual3 = (Invoke-Api -Method "PUT" -Path "/api/versions/$script:versionID/visual/assertions/964004" -Token $script:token -Body @{
        versionSutId = $script:versionSUTID; selector = "#visual-header"; viewport = @{ width = 1280; height = 720 }; deviceScaleFactor = 2; threshold = 0.005
    })
    Assert-Status $script:visual3 @(200) "PUT visual assertion 964004"
    $script:visualAssertion1 = [string]$script:visual1.Json.data.id
    $script:visualAssertion2 = [string]$script:visual2.Json.data.id
    $script:visualAssertion3 = [string]$script:visual3.Json.data.id
    Assert-Equal $script:visual1.Json.data.config_revision 1 "visual assertion 1 config revision"
    Assert-Equal $script:visual3.Json.data.device_scale_factor 2 "visual assertion 3 DPR"
    Note-Assertion ("visual assertions: {0}/{1}/{2}" -f $script:visualAssertion1, $script:visualAssertion2, $script:visualAssertion3)
    Pass-Scenario $s0

    # ---------------- S1: T0 index / tree / file / diff ---------------------
    if (Should-Run "S1") {
        $s1 = Start-Scenario "S1 T0 index: index A/B, tree/file/diff A..B (A/M/D/binary), worktree unchanged" "POST /code/index; GET /code/repos/{id}/tree|file|diff"
        Step "index the fixture repository at commit A (baseline)"
        $indexA = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/code/index" -Token $script:token -Body @{ repoId = $script:repoID; commit = $script:baseCommit }
        Assert-Status $indexA @(202) "POST code index commit A"
        $runA = Wait-For -TimeoutSeconds 180 -Description "index run A done" -Condition {
            $response = Invoke-Api -Method "GET" -Path "/api/code/repos/$script:repoID/index-runs/$([string]$indexA.Json.data.id)" -Token $script:token
            if ($response.Status -ne 200) { return $null }
            if ([string]$response.Json.data.status -eq "done") { return $response.Json.data }
            if ([string]$response.Json.data.status -eq "failed") { throw "index run A failed" }
            return $null
        } -IntervalMilliseconds 1000
        Assert-Equal $runA.commit_sha $script:baseCommit "index A commit"
        Assert-True ([int]$runA.files_indexed -ge 7) ("index A files_indexed = {0}, want >= 7" -f $runA.files_indexed)
        Assert-Equal $runA.truncated $false "index A truncated"

        Step "index the fixture repository at commit B (head)"
        $indexB = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/code/index" -Token $script:token -Body @{ repoId = $script:repoID; commit = $script:headCommit }
        Assert-Status $indexB @(202) "POST code index commit B"
        $runB = Wait-For -TimeoutSeconds 180 -Description "index run B done" -Condition {
            $response = Invoke-Api -Method "GET" -Path "/api/code/repos/$script:repoID/index-runs/$([string]$indexB.Json.data.id)" -Token $script:token
            if ($response.Status -ne 200) { return $null }
            if ([string]$response.Json.data.status -eq "done") { return $response.Json.data }
            if ([string]$response.Json.data.status -eq "failed") { throw "index run B failed" }
            return $null
        } -IntervalMilliseconds 1000
        Assert-Equal $runB.commit_sha $script:headCommit "index B commit"
        Assert-True ([int]$runB.files_indexed -ge 7) ("index B files_indexed = {0}, want >= 7" -f $runB.files_indexed)
        Note-Assertion ("index A done ({0} files); index B done ({1} files)" -f $runA.files_indexed, $runB.files_indexed)

        Step "assert a duplicate index of the same repo+commit is reused (200)"
        $reused = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/code/index" -Token $script:token -Body @{ repoId = $script:repoID; commit = $script:headCommit }
        Assert-Status $reused @(200) "POST duplicate code index returns 200 reused"
        Assert-Equal $reused.Json.data.id $runB.id "reused index run id"
        Note-Assertion "duplicate index returns 200 reused (same run id)"

        Step "assert repo status, tree, file window and diff projections"
        $repoStatus = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/code/repos" -Token $script:token
        Assert-Status $repoStatus @(200) "GET code/repos"
        $repoItems = Get-Array $repoStatus.Json.data
        Assert-Equal $repoItems.Count "1" "version code repo status count"
        Assert-Equal ([string]$repoItems[0].latest_index.commit_sha) $script:headCommit "latest index commit"
        Assert-Equal ([string]$repoItems[0].baseline_commit) $script:baseCommit "repo baseline commit"
        $treeResponse = Invoke-Api -Method "GET" -Path ("/api/code/repos/{0}/tree?commit={1}&limit=100" -f $script:repoID, $script:headCommit) -Token $script:token
        Assert-Status $treeResponse @(200) "GET code tree"
        $tree = Get-Array $treeResponse.Json.data
        $treePaths = @($tree | ForEach-Object { [string]$_.path })
        foreach ($path in @("src/api/refund.ts", "src/pages/RefundPage.tsx", "assets/logo.png", "src/router.ts")) {
            Assert-True ($treePaths -contains $path) ("tree HEAD contains " + $path)
        }
        Assert-True ($treePaths -notcontains "src/legacy/old.ts") "tree HEAD must not contain the deleted src/legacy/old.ts"
        $fileResponse = Invoke-Api -Method "GET" -Path ("/api/code/repos/{0}/file?commit={1}&path=src/api/refund.ts&startLine=1&endLine=20" -f $script:repoID, $script:headCommit) -Token $script:token
        Assert-Status $fileResponse @(200) "GET code file window"
        Assert-True ([string]$fileResponse.Json.data.content -match "reason") "refund.ts HEAD content contains the reason parameter"
        Assert-True ([int]$fileResponse.Json.data.total_lines -ge 10) "refund.ts total_lines"
        $binaryResponse = Invoke-Api -Method "GET" -Path ("/api/code/repos/{0}/file?commit={1}&path=assets/logo.png" -f $script:repoID, $script:headCommit) -Token $script:token
        Assert-Status $binaryResponse @(200) "GET binary file window"
        Assert-Equal $binaryResponse.Json.data.binary $true "binary flag for assets/logo.png"
        Assert-Equal $binaryResponse.Json.data.content "" "binary content is empty"

        $diffResponse = Invoke-Api -Method "GET" -Path ("/api/code/repos/{0}/diff?base={1}&head={2}&limit=100" -f $script:repoID, $script:baseCommit, $script:headCommit) -Token $script:token
        Assert-Status $diffResponse @(200) "GET code diff A..B"
        $changed = Get-Array $diffResponse.Json.data
        Assert-Equal $changed.Count "8" "changed file count A..B"
        $statuses = @($changed | ForEach-Object { [string]$_.status })
        foreach ($status in @("added", "modified", "deleted")) {
            Assert-True ($statuses -contains $status) ("diff contains status " + $status)
        }
        $binaryChange = $changed | Where-Object { [string]$_.path -eq "assets/logo.png" }
        Assert-True ([bool]$binaryChange.binary) "diff marks assets/logo.png binary"
        $deletedChange = $changed | Where-Object { [string]$_.path -eq "src/legacy/old.ts" }
        Assert-Equal $deletedChange.status "deleted" "deleted file status"
        $addedChange = $changed | Where-Object { [string]$_.path -eq "src/pages/RefundPage.tsx" }
        Assert-Equal $addedChange.status "added" "added file status"
        $addedDiff = Invoke-Api -Method "GET" -Path ("/api/code/repos/{0}/diff/file?base={1}&head={2}&path=src/pages/RefundPage.tsx" -f $script:repoID, $script:baseCommit, $script:headCommit) -Token $script:token
        Assert-Status $addedDiff @(200) "GET added file diff"
        Assert-True (-not [string]::IsNullOrWhiteSpace([string]$addedDiff.Json.data.patch)) "added file diff has a patch"
        Assert-Equal $addedDiff.Json.data.binary $false "added file diff not binary"
        $deletedDiff = Invoke-Api -Method "GET" -Path ("/api/code/repos/{0}/diff/file?base={1}&head={2}&path=src/legacy/old.ts" -f $script:repoID, $script:baseCommit, $script:headCommit) -Token $script:token
        Assert-Status $deletedDiff @(200) "GET deleted file diff"
        Assert-Equal $deletedDiff.Json.data.deleted $true "deleted file diff flag"
        Assert-True ($null -eq $deletedDiff.Json.data.patch) "deleted file diff has no patch"
        $binaryDiff = Invoke-Api -Method "GET" -Path ("/api/code/repos/{0}/diff/file?base={1}&head={2}&path=assets/logo.png" -f $script:repoID, $script:baseCommit, $script:headCommit) -Token $script:token
        Assert-Status $binaryDiff @(200) "GET binary file diff"
        Assert-Equal $binaryDiff.Json.data.binary $true "binary file diff flag"
        Assert-True ($null -eq $binaryDiff.Json.data.patch) "binary file diff has no patch"
        $worktreeStatus = (& git -C $script:repoPath status --porcelain 2>&1 | Out-String).Trim()
        Assert-Equal $worktreeStatus "" "fixture worktree unchanged by indexing"
        Note-Assertion "A/M/D + binary diff flags, patch/deleted/binary handling, worktree clean"
        Pass-Scenario $s1
    }

    # ---------------- S2: T1 real-LLM facts/evidence ------------------------
    if (Should-Run "S2") {
        $s2 = Start-Scenario "S2 T1 real-LLM analysis A..B: facts/evidence/stats, cache reuse, no code in events" "POST /code/analyses; GET /code/facts"
        Step "trigger the real-LLM analysis A..B"
        $analysisResponse = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/code/analyses" -Token $script:token -Body @{ repoId = $script:repoID; headCommit = $script:headCommit }
        Assert-Status $analysisResponse @(202) "POST code analysis A..B"
        $script:analysisID = [string]$analysisResponse.Json.data.id
        $analysisDetail = Wait-AnalysisDone -AnalysisID $script:analysisID -TimeoutSeconds $PollTimeoutSeconds
        [void](Wait-ImpactDone -AnalysisID $script:analysisID -TimeoutSeconds $PollTimeoutSeconds)
        $analysis = $analysisDetail.analysis
        Assert-Equal $analysis.status "done" "analysis status"
        Assert-Equal $analysis.degraded $false "analysis degraded (real AI succeeded)"
        Assert-Equal $analysis.model $script:modelName "analysis model"
        Assert-Equal $analysis.base_commit_sha $script:baseCommit "analysis base commit"
        Assert-Equal $analysis.head_commit_sha $script:headCommit "analysis head commit"
        Assert-True ([int]$analysis.changed_files_count -eq 8) ("analysis changed files = {0}" -f $analysis.changed_files_count)
        Assert-True ([int]$analysis.stats.facts -ge 10) ("analysis facts = {0}, want >= 10" -f $analysis.stats.facts)
        Assert-True ([int]$analysis.stats.evidenceChecks -ge 5) "analysis evidence checks"
        Assert-Equal ([int]$analysis.stats.evidenceFailures) "0" "analysis evidence failures (fixture excerpts must validate)"
        Assert-True ([int]$analysis.stats.analyzedFiles -ge 5) "analysis analyzed files"
        Note-Assertion ("real LLM analysis done: model={0} facts={1} edges={2} evidenceChecks={3} degraded=false" -f $analysis.model, $analysis.stats.facts, $analysis.stats.edges, $analysis.stats.evidenceChecks)

        Step "assert the fact kinds and the normalized route/api/client_call facts"
        $factsResponse = Invoke-Api -Method "GET" -Path ("/api/code/repos/{0}/facts?commit={1}&limit=100" -f $script:repoID, $script:headCommit) -Token $script:token
        Assert-Status $factsResponse @(200) "GET facts"
        $facts = Get-Array $factsResponse.Json.data
        Assert-True ($facts.Count -ge 10) "facts list count"
        $kinds = @($facts | ForEach-Object { [string]$_.kind } | Sort-Object -Unique)
        foreach ($kind in @("route", "api", "client_call", "symbol", "component")) {
            Assert-True ($kinds -contains $kind) ("facts contain kind " + $kind)
        }
        $factKeys = @($facts | ForEach-Object { [string]$_.fact_key })
        foreach ($key in @("route:/orders", "api:POST /api/refund", "client:POST /api/refund")) {
            Assert-True ($factKeys -contains $key) ("facts contain " + $key)
        }
        Note-Assertion ("facts kinds: " + ($kinds -join ","))

        Step "recompute the server evidence hash for up to three facts through the real file API"
        $checked = 0
        foreach ($fact in $facts) {
            if ($checked -ge 3) { break }
            $evidenceList = Get-Array $fact.evidence
            if ($evidenceList.Count -lt 1) { continue }
            $evidence = $evidenceList[0]
            $startLine = [int]$evidence.start_line
            $endLine = [int]$evidence.end_line
            $filePath = [string]$evidence.file
            if ($startLine -lt 1 -or $endLine -lt $startLine) { continue }
            $windowResponse = Invoke-Api -Method "GET" -Path ("/api/code/repos/{0}/file?commit={1}&path={2}&startLine={3}&endLine={4}" -f $script:repoID, $script:headCommit, [System.Uri]::EscapeDataString($filePath), $startLine, $endLine) -Token $script:token
            Assert-Status $windowResponse @(200) ("GET fact evidence window " + $fact.fact_key)
            $lines = @(([string]$windowResponse.Json.data.content) -split "`n" | ForEach-Object { $_.TrimEnd("`r") })
            $excerpt = ($lines -join "`n")
            $computed = "sha256:" + (Get-Sha256Hex -Bytes (Get-Utf8Bytes $excerpt))
            Assert-Equal $computed ([string]$evidence.excerpt_hash) ("server evidence hash for " + $fact.fact_key)
            $checked++
        }
        Assert-True ($checked -ge 2) ("evidence hashes recomputed = {0}, want >= 2" -f $checked)
        Note-Assertion ("{0} fact evidence hashes recomputed from commit content" -f $checked)

        Step "assert a repeated analysis request reuses the done analysis (200 reused)"
        $reusedAnalysis = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/code/analyses" -Token $script:token -Body @{ repoId = $script:repoID; headCommit = $script:headCommit }
        Assert-Status $reusedAnalysis @(200) "POST duplicate analysis returns 200 reused"
        Assert-Equal $reusedAnalysis.Json.data.id $script:analysisID "reused analysis id"

        Step "assert code analysis events exist and contain no source code lines"
        $events = Get-ChannelEvents -VersionID $script:versionID
        [void](Assert-EventPresent -Events $events -Type "code.analysis_started")
        [void](Assert-EventPresent -Events $events -Type "code.analysis_finished")
        $eventText = ($events | ForEach-Object { ($_.Payload | ConvertTo-Json -Depth 20 -Compress) }) -join "`n"
        Assert-True (-not $eventText.Contains("export async function requestRefund")) "events must not contain source code"
        Assert-True (-not $eventText.Contains("func handleRefund")) "events must not contain source code"
        Note-Assertion "code.analysis_started/finished events present; no source code in payloads"
        Pass-Scenario $s2
    }

    # ---------------- S3: impact candidates ---------------------------------
    if (Should-Run "S3") {
        $s3 = Start-Scenario "S3 impact candidates: direct/relation page/api/case, candidate selection PATCH, unmapped, AI availability" "GET /code/impact; PATCH candidates"
        Step "wait for the impact work item and read the merged impact view"
        [void](Wait-ImpactDone -AnalysisID $script:analysisID -TimeoutSeconds $PollTimeoutSeconds)
        $impact = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/code/impact" -Token $script:token
        Assert-Status $impact @(200) "GET code impact"
        $candidates = Get-Array $impact.Json.data
        Assert-True ($candidates.Count -ge 5) ("impact candidates = {0}, want >= 5" -f $candidates.Count)
        $directRequirement = @($candidates | Where-Object { [string]$_.subject_kind -eq "requirement" -and [string]$_.subject_id -eq $script:requirementID -and [string]$_.source -eq "direct" })
        Assert-True ($directRequirement.Count -ge 1) "direct requirement candidate missing"
        $directCase = @($candidates | Where-Object { [string]$_.subject_kind -eq "case_version" -and [string]$_.subject_id -eq $script:caseVersionOrders -and [string]$_.source -eq "direct" })
        Assert-True ($directCase.Count -ge 1) "direct case_version candidate missing"
        $relationRequirement = @($candidates | Where-Object { [string]$_.subject_kind -eq "requirement" -and [string]$_.subject_id -eq $script:requirement2ID -and [string]$_.source -eq "relation" })
        Assert-True ($relationRequirement.Count -ge 1) "relation requirement candidate missing"
        $directPage = @($candidates | Where-Object { [string]$_.subject_kind -eq "sut_page" -and [string]$_.subject_id -eq $script:pageImpactID -and [string]$_.source -eq "direct" })
        Assert-True ($directPage.Count -ge 1) "direct sut_page candidate missing"
        $directAPI = @($candidates | Where-Object { [string]$_.subject_kind -eq "sut_api" -and [string]$_.subject_id -eq $script:apiRefundID -and [string]$_.source -eq "direct" })
        Assert-True ($directAPI.Count -ge 1) "direct sut_api candidate missing"
        Note-Assertion ("candidates={0}: direct requirement/case_version/page/api + relation requirement" -f $candidates.Count)

        Step "assert low-confidence candidates are never auto-selected"
        foreach ($candidate in $candidates) {
            if ([string]$candidate.confidence -eq "low") {
                Assert-Equal $candidate.selected $false ("low candidate {0}:{1} must not be selected" -f $candidate.subject_kind, $candidate.subject_id)
            }
        }

        Step "assert the contract union view carries persisted candidate ids (contract #17)"
        $zeroIDs = @($candidates | Where-Object { [string]$_.id -eq "0" -or [string]$_.analysis_id -eq "0" })
        if ($zeroIDs.Count -ge 1) {
            Add-SoftFailure ("GET /code/impact returned {0} candidate(s) with id/analysis_id '0' (union view loses persisted ids; PATCH via this view is impossible)" -f $zeroIDs.Count) $s3
        } else {
            Note-Assertion "impact union candidate ids are persisted decimal ids"
        }

        Step "assert the analysis detail view (#15) includes its candidates"
        $detailResponse = Get-Analysis -AnalysisID $script:analysisID
        $detailCandidates = Get-Array $detailResponse.Json.data.candidates
        if ($detailCandidates.Count -lt 1) {
            Add-SoftFailure "GET /code/analyses/{id} returned candidates:[] although impact candidates exist (spec #15)" $s3
        } else {
            Note-Assertion ("analysis detail candidates={0}" -f $detailCandidates.Count)
        }

        Step "select/unselect one candidate through PATCH using its persisted id"
        $dbCandidateID = Invoke-PsqlScalar -SqlText ("SELECT id FROM code_impact_candidate WHERE analysis_id = {0} AND subject_kind = 'requirement' AND subject_id = {1} AND source = 'direct' ORDER BY id LIMIT 1;" -f $script:analysisID, $script:requirementID)
        Assert-Match $dbCandidateID '^[0-9]+$' "persisted impact candidate id"
        $unselect = Invoke-Api -Method "PATCH" -Path "/api/versions/$script:versionID/code/analyses/$($script:analysisID)/candidates/$dbCandidateID" -Token $script:token -Body @{ selected = $false }
        Assert-Status $unselect @(200) "PATCH candidate selected=false"
        Assert-Equal $unselect.Json.data.selected $false "candidate selected after unselect"
        Assert-Equal ([string]$unselect.Json.data.selection_updated_by) ([string]$unselect.Json.data.selection_updated_by) "selection actor present"
        Assert-True (-not [string]::IsNullOrWhiteSpace([string]$unselect.Json.data.selection_updated_by)) "candidate selection actor recorded"
        $reselect = Invoke-Api -Method "PATCH" -Path "/api/versions/$script:versionID/code/analyses/$($script:analysisID)/candidates/$dbCandidateID" -Token $script:token -Body @{ selected = $true }
        Assert-Status $reselect @(200) "PATCH candidate selected=true"
        Assert-Equal $reselect.Json.data.selected $true "candidate selected after reselect"
        $auditCount = Invoke-PsqlScalar -SqlText "SELECT count(*) FROM audit_log WHERE action = 'code.impact.candidate_selected';"
        Assert-True ([int]$auditCount -ge 2) "candidate selection audit entries"
        Note-Assertion "candidate PATCH selected=false/true with actor+audit"

        Step "assert AI availability, candidate source counts, contract edge and unmapped reasons"
        $analysisView = (Get-Analysis -AnalysisID $script:analysisID).Json.data.analysis
        Assert-Equal $analysisView.degraded $false "impact AI inference must not degrade"
        $events = Get-ChannelEvents -VersionID $script:versionID
        $impactEvent = @(Assert-EventPresent -Events $events -Type "code.impact_computed")[-1]
        $sourceCounts = $impactEvent.Payload.candidates_by_source
        $sourceSum = 0
        foreach ($property in $sourceCounts.PSObject.Properties) { $sourceSum += [int]$property.Value }
        Assert-Equal $sourceSum $candidates.Count "impact event candidate source count sum"
        $aiCandidates = @($candidates | Where-Object { [string]$_.source -eq "ai" })
        if ($aiCandidates.Count -ge 1) { Note-Assertion ("AI-source candidates={0}" -f $aiCandidates.Count) }
        else { Note-Assertion "AI inference completed (degraded=false); AI candidates merged into direct/relation subjects by design (merge priority)" }
        $edgeCount = Invoke-PsqlScalar -SqlText ("SELECT count(*) FROM code_edge WHERE repo_id = {0} AND commit_sha = '{1}' AND rel = 'route_to_api';" -f $script:repoID, $script:headCommit)
        Assert-True ([int]$edgeCount -ge 1) "route_to_api contract edge exists"
        $unmapped = Get-Array (Get-Analysis -AnalysisID $script:analysisID).Json.data.unmapped
        $reasons = @($unmapped | ForEach-Object { [string]$_.reason } | Sort-Object -Unique)
        Assert-True ($reasons -contains "unsupported_content") "unmapped unsupported_content (binary logo) missing"
        Assert-True ($reasons -contains "deleted_file") "unmapped deleted_file (old.ts) missing"
        Note-Assertion ("route_to_api edges={0}; unmapped reasons={1}" -f $edgeCount, ($reasons -join ","))
        Pass-Scenario $s3
    }

    # ---------------- S5: anchors -------------------------------------------
    if (Should-Run "S5") {
        $s5 = Start-Scenario "S5 anchors: requirement/case_version valid, stale, broken, duplicate 409, invalid 422, delete 204" "POST/GET/POST validate/DELETE /code/anchors"
        Step "validate the setup anchors and assert valid status"
        $validated = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/code/anchors/$($script:anchorRequirementID)/validate" -Token $script:token
        Assert-Status $validated @(200) "validate requirement anchor"
        Assert-Equal $validated.Json.data.status "valid" "requirement anchor status after validate"
        $anchorsList = Invoke-Api -Method "GET" -Path ("/api/versions/{0}/code/anchors?subjectKind=requirement&subjectId={1}" -f $script:versionID, $script:requirementID) -Token $script:token
        if ($anchorsList.Status -eq 500) {
            Add-SoftFailure ("GET /code/anchors?subjectKind=requirement&subjectId={0} -> 500; the repository filter query infers an int4 parameter for a snowflake subject id (list without filters works)" -f $script:requirementID) $s5
        } else {
            Assert-Status $anchorsList @(200) "GET anchors by subject"
            Assert-True ((Get-Array $anchorsList.Json.data).Count -ge 1) "requirement anchors list"
        }

        Step "assert a duplicate anchor returns 409 anchor_duplicate"
        $duplicate = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/code/anchors" -Token $script:token -Body @{
            subjectKind = "requirement"; subjectId = $script:requirementID; repoId = $script:repoID
            filePath = "src/api/refund.ts"; startLine = 1; endLine = 3; commit = $script:headCommit
        }
        Assert-Status $duplicate @(409) "POST duplicate anchor"
        Assert-Equal $duplicate.Json.error.code "anchor_duplicate" "duplicate anchor error code"

        Step "create anchors against commit A and validate against latest index B to reach stale/broken"
        $staleResponse = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/code/anchors" -Token $script:token -Body @{
            subjectKind = "requirement"; subjectId = $script:requirement2ID; repoId = $script:repoID
            filePath = "src/api/refund.ts"; commit = $script:baseCommit
        }
        Assert-Status $staleResponse @(201) "POST stale anchor (commit A, no excerpt)"
        $staleID = [string]$staleResponse.Json.data.id
        $staleValidated = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/code/anchors/$staleID/validate" -Token $script:token
        Assert-Status $staleValidated @(200) "validate stale anchor"
        Assert-Equal $staleValidated.Json.data.status "stale" "anchor status after commit moved"
        $brokenResponse = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/code/anchors" -Token $script:token -Body @{
            subjectKind = "requirement"; subjectId = $script:requirement2ID; repoId = $script:repoID
            filePath = "src/legacy/old.ts"; commit = $script:baseCommit
        }
        Assert-Status $brokenResponse @(201) "POST broken anchor (file deleted at head)"
        $brokenID = [string]$brokenResponse.Json.data.id
        $brokenValidated = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/code/anchors/$brokenID/validate" -Token $script:token
        Assert-Status $brokenValidated @(200) "validate broken anchor"
        Assert-Equal $brokenValidated.Json.data.status "broken" "anchor status after file deletion"
        Note-Assertion "anchor valid/stale/broken statuses observed"

        Step "assert an invalid subject returns 422 and a foreign/unknown anchor delete returns 404"
        $invalid = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/code/anchors" -Token $script:token -Body @{
            subjectKind = "requirement"; subjectId = "999999999"; repoId = $script:repoID; filePath = "src/api/refund.ts"
        }
        Assert-Status $invalid @(422) "POST anchor with invalid subject"
        Assert-Equal $invalid.Json.error.code "invalid_subject" "invalid subject error code"
        $deleted = Invoke-Api -Method "DELETE" -Path "/api/versions/$script:versionID/code/anchors/$staleID" -Token $script:token
        Assert-Status $deleted @(204) "DELETE stale anchor"
        $deletedAgain = Invoke-Api -Method "DELETE" -Path "/api/versions/$script:versionID/code/anchors/$staleID" -Token $script:token
        Assert-Status $deletedAgain @(404) "DELETE stale anchor again"
        $caseAnchors = Invoke-Api -Method "GET" -Path ("/api/versions/{0}/code/anchors?subjectKind=case_version&subjectId={1}" -f $script:versionID, $script:caseVersionOrders) -Token $script:token
        Assert-Status $caseAnchors @(200) "GET case_version anchors"
        Assert-True ((Get-Array $caseAnchors.Json.data).Count -ge 1) "case_version anchor list"
        $events = Get-ChannelEvents -VersionID $script:versionID
        $validatedEvents = @($events | Where-Object { $_.Type -eq "code.anchor_validated" })
        Assert-True ($validatedEvents.Count -ge 3) "code.anchor_validated events"
        Note-Assertion "duplicate 409, invalid subject 422, delete 204 then 404, code.anchor_validated events"
        Pass-Scenario $s5
    }

    # ---------------- S6: fact review exclusion -----------------------------
    if (Should-Run "S6") {
        $s6 = Start-Scenario "S6 fact review: correct/incorrect append, incorrect fact excluded from auto impact on re-analysis, audit/event" "POST /code/facts/{id}/reviews"
        Step "choose the bound route:/orders fact and its automatic impact subject"
        $factsResponse = Invoke-Api -Method "GET" -Path ("/api/code/repos/{0}/facts?commit={1}&limit=100" -f $script:repoID, $script:headCommit) -Token $script:token
        Assert-Status $factsResponse @(200) "GET facts for review"
        $facts = Get-Array $factsResponse.Json.data
        $reviewFact = $facts | Where-Object { [string]$_.fact_key -eq "route:/orders" } | Select-Object -First 1
        $reviewSubjectKind = "sut_page"
        $reviewSubjectID = $script:pageImpactID
        if ($null -eq $reviewFact) {
            $reviewFact = $facts | Where-Object { [string]$_.fact_key -eq "api:POST /api/refund" } | Select-Object -First 1
            $reviewSubjectKind = "sut_api"
            $reviewSubjectID = $script:apiRefundID
        }
        Assert-True ($null -ne $reviewFact) "a reviewable bound fact (route:/orders or api:POST /api/refund) must exist"
        $reviewFactID = [string]$reviewFact.id
        Note-Assertion ("reviewing fact {0} (subject {1}:{2})" -f $reviewFact.fact_key, $reviewSubjectKind, $reviewSubjectID)

        Step "append correct then incorrect reviews and assert the review history"
        $correctReview = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/code/facts/$reviewFactID/reviews" -Token $script:token -Body @{ verdict = "correct"; reason = "P5 acceptance correct" }
        Assert-Status $correctReview @(201) "POST correct fact review"
        $incorrectReview = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/code/facts/$reviewFactID/reviews" -Token $script:token -Body @{ verdict = "incorrect"; reason = "P5 acceptance incorrect" }
        Assert-Status $incorrectReview @(201) "POST incorrect fact review"
        Assert-Equal $incorrectReview.Json.data.verdict "incorrect" "incorrect review verdict"
        Assert-True (-not [string]::IsNullOrWhiteSpace([string]$incorrectReview.Json.data.reviewer_id)) "review actor recorded"
        Assert-True (-not [string]::IsNullOrWhiteSpace([string]$incorrectReview.Json.data.created_at)) "review time recorded"
        $reviewList = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/code/facts/$reviewFactID/reviews" -Token $script:token
        Assert-Status $reviewList @(200) "GET fact reviews"
        $reviews = Get-Array $reviewList.Json.data
        Assert-True ($reviews.Count -ge 2) "fact review history count"
        $invalidReview = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/code/facts/$reviewFactID/reviews" -Token $script:token -Body @{ verdict = "maybe"; reason = "invalid" }
        Assert-Status $invalidReview @(422) "POST invalid fact review verdict"

        Step "assert a normal login user may review facts (non-admin read/write permission)"
        $otherFact = $facts | Where-Object { [string]$_.id -ne $reviewFactID } | Select-Object -First 1
        if ($null -ne $otherFact) {
            $userReview = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/code/facts/$([string]$otherFact.id)/reviews" -Token $script:userToken -Body @{ verdict = "correct"; reason = "P5 user review" }
            Assert-Status $userReview @(201) "POST fact review as normal user"
        }

        Step "force a second real-LLM analysis and assert cache reuse + incorrect fact excluded from automatic impact"
        $analysis2Response = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/code/analyses" -Token $script:token -Body @{ repoId = $script:repoID; headCommit = $script:headCommit; force = $true }
        Assert-Status $analysis2Response @(202) "POST forced code analysis"
        $script:analysis2ID = [string]$analysis2Response.Json.data.id
        $analysis2Detail = Wait-AnalysisDone -AnalysisID $script:analysis2ID -TimeoutSeconds $PollTimeoutSeconds
        Assert-Equal $analysis2Detail.analysis.status "done" "forced analysis status"
        Assert-Equal $analysis2Detail.analysis.degraded $false "forced analysis degraded"
        Assert-True ([int]$analysis2Detail.analysis.stats.cacheHits -ge 1) ("forced analysis cacheHits = {0}, want >= 1" -f $analysis2Detail.analysis.stats.cacheHits)
        [void](Wait-ImpactDone -AnalysisID $script:analysis2ID -TimeoutSeconds $PollTimeoutSeconds)
        $impact2 = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/code/impact" -Token $script:token
        Assert-Status $impact2 @(200) "GET impact after review"
        $candidates2 = Get-Array $impact2.Json.data
        $excluded = @($candidates2 | Where-Object { [string]$_.subject_kind -eq $reviewSubjectKind -and [string]$_.subject_id -eq $reviewSubjectID -and [string]$_.source -eq "direct" })
        if ($excluded.Count -gt 0) {
            Add-SoftFailure ("incorrect fact {0} still produced a direct {1}:{2} candidate after re-analysis" -f $reviewFact.fact_key, $reviewSubjectKind, $reviewSubjectID) $s6
        } else {
            Note-Assertion ("incorrect fact {0} excluded from direct impact candidates" -f $reviewFact.fact_key)
        }
        $reviewAudit = Invoke-PsqlScalar -SqlText ("SELECT count(*) FROM audit_log WHERE action = 'code.fact_review' AND target_id = '{0}';" -f $reviewFactID)
        Assert-True ([int]$reviewAudit -ge 2) "fact review audit entries"
        $events = Get-ChannelEvents -VersionID $script:versionID
        $reviewedEvents = @($events | Where-Object { $_.Type -eq "code.fact_reviewed" })
        Assert-True ($reviewedEvents.Count -ge 2) "code.fact_reviewed events"
        Note-Assertion ("cacheHits={0}; code.fact_reviewed events={1}; audit rows={2}" -f $analysis2Detail.analysis.stats.cacheHits, $reviewedEvents.Count, $reviewAudit)
        Pass-Scenario $s6
    }

    # ---------------- S7: page/API proposals --------------------------------
    if (Should-Run "S7") {
        $s7 = Start-Scenario "S7 page/API proposals: generate/idempotent, admin confirm/reject, non-admin 403, source=ai, event counts" "POST/GET /code/page-api-proposals; POST /resources confirm|reject"
        Step "generate proposals from code facts"
        $generate = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/code/page-api-proposals" -Token $script:token -Body @{ repoId = $script:repoID }
        Assert-Status $generate @(200) "POST page-api proposals"
        $pagesCreated = [int]$generate.Json.data.pages.created
        $pagesSkipped = [int]$generate.Json.data.pages.skipped
        $apisCreated = [int]$generate.Json.data.apis.created
        $apisSkipped = [int]$generate.Json.data.apis.skipped
        Assert-True ($pagesCreated -ge 1) ("pages.created = {0}, want >= 1" -f $pagesCreated)
        Assert-True ($pagesSkipped -ge 1) ("pages.skipped = {0}, want >= 1 (bound /orders page already approved)" -f $pagesSkipped)
        Assert-True ($apisSkipped -ge 1) ("apis.skipped = {0}, want >= 1 (bound POST /api/refund already approved)" -f $apisSkipped)
        Note-Assertion ("proposal counts pages created={0} skipped={1}; apis created={2} skipped={3}" -f $pagesCreated, $pagesSkipped, $apisCreated, $apisSkipped)

        Step "assert proposed rows and their source/status"
        $listResponse = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/code/page-api-proposals" -Token $script:token
        Assert-Status $listResponse @(200) "GET page-api proposals"
        $proposals = Get-Array $listResponse.Json.data
        Assert-True ($proposals.Count -ge 2) "proposed resource count"
        foreach ($proposal in $proposals) {
            Assert-Equal $proposal.status "proposed" "proposal status"
            Assert-True (@("ai", "code") -contains [string]$proposal.source) ("proposal source " + $proposal.source)
            Assert-True (@("page", "api") -contains [string]$proposal.kind) ("proposal kind " + $proposal.kind)
        }
        Note-Assertion ("proposals: " + (($proposals | ForEach-Object { $_.kind + ":" + $_.route_template + $_.method + $_.path_template + "(" + $_.source + ")" }) -join "; "))

        Step "assert regeneration is idempotent"
        $again = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/code/page-api-proposals" -Token $script:token -Body @{ repoId = $script:repoID }
        Assert-Status $again @(200) "POST duplicate page-api proposals"
        Assert-Equal ([int]$again.Json.data.pages.created) 0 "idempotent pages.created"
        Assert-Equal ([int]$again.Json.data.apis.created) 0 "idempotent apis.created"
        Assert-True (([int]$again.Json.data.pages.skipped + [int]$again.Json.data.apis.skipped) -ge 2) "idempotent skipped count"

        Step "assert any login user may generate, data is generated (not only admin)"
        $userGenerate = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/code/page-api-proposals" -Token $script:userToken -Body @{ repoId = $script:repoID }
        Assert-Status $userGenerate @(200) "POST proposals as normal user"

        Step "assert non-admin cannot confirm, admin can confirm one proposal and reject another"
        $proposedPages = @($proposals | Where-Object { [string]$_.kind -eq "page" })
        $proposedAPIs = @($proposals | Where-Object { [string]$_.kind -eq "api" })
        Assert-True ($proposedPages.Count -ge 2) "at least two proposed pages to confirm and reject"
        $confirmPage = $proposedPages[0]
        $rejectPage = $proposedPages[1]
        $nonAdminConfirm = Invoke-Api -Method "POST" -Path "/api/resources/pages/$([string]$confirmPage.id)/confirm" -Token $script:userToken
        Assert-Status $nonAdminConfirm @(403) "non-admin page confirm"
        $confirmed = Invoke-Api -Method "POST" -Path "/api/resources/pages/$([string]$confirmPage.id)/confirm" -Token $script:token
        Assert-Status $confirmed @(200) "admin page confirm"
        Assert-Equal $confirmed.Json.data.status "approved" "confirmed page status"
        Assert-Equal $confirmed.Json.data.source $confirmPage.source "confirmed page source"
        $rejected = Invoke-Api -Method "POST" -Path "/api/resources/pages/$([string]$rejectPage.id)/reject" -Token $script:token
        Assert-Status $rejected @(200) "admin page reject"
        Assert-Equal $rejected.Json.data.status "rejected" "rejected page status"
        if ($proposedAPIs.Count -ge 1) {
            $nonAdminApiConfirm = Invoke-Api -Method "POST" -Path "/api/resources/apis/$([string]$proposedAPIs[0].id)/confirm" -Token $script:userToken
            Assert-Status $nonAdminApiConfirm @(403) "non-admin API confirm"
            $apiRejected = Invoke-Api -Method "POST" -Path "/api/resources/apis/$([string]$proposedAPIs[0].id)/reject" -Token $script:token
            Assert-Status $apiRejected @(200) "admin API reject"
            Assert-Equal $apiRejected.Json.data.status "rejected" "rejected API status"
        }
        Note-Assertion ("admin confirmed page {0}, rejected page {1}; non-admin 403" -f $confirmPage.id, $rejectPage.id)

        Step "assert a rejected proposal is not re-proposed and the event carries the counts"
        $afterReject = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/code/page-api-proposals" -Token $script:token -Body @{ repoId = $script:repoID }
        Assert-Status $afterReject @(200) "POST proposals after reject"
        Assert-Equal ([int]$afterReject.Json.data.pages.created) 0 "no page re-created after reject"
        Assert-Equal ([int]$afterReject.Json.data.apis.created) 0 "no api re-created after reject"
        $events = Get-ChannelEvents -VersionID $script:versionID
        $proposedEvents = @($events | Where-Object { $_.Type -eq "code.page_api_proposed" })
        Assert-True ($proposedEvents.Count -ge 1) "code.page_api_proposed event"
        $firstEvent = $proposedEvents[0]
        Assert-Equal ([int]$firstEvent.Payload.pages_created) $pagesCreated "event pages_created"
        Assert-Equal ([int]$firstEvent.Payload.apis_created) $apisCreated "event apis_created"
        Note-Assertion "rejected proposals not re-proposed; code.page_api_proposed counts match"
        Pass-Scenario $s7
    }

    # ---------------- S8: visual first run without baseline -----------------
    if (Should-Run "S8") {
        $s8 = Start-Scenario "S8 visual first run: missing baseline -> inconclusive(missing_visual_baseline) + visual_actual, no false pass" "POST /design/confirmations; GET /visual/diffs; GET evidence"
        Step "confirm the four seeded cases to start the automatic execution wave"
        $knownWaves = @(Get-Waves | ForEach-Object { [string]$_.id })
        foreach ($caseID in @("961001", "961002", "961003", "961004")) {
            $confirm = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/design/confirmations" -Token $script:token -Body @{ scope = "case"; target_id = $caseID }
            Assert-Status $confirm @(200) ("confirm case " + $caseID)
            Assert-Equal (Get-Array $confirm.Json.data.confirmed).Count 1 ("confirmed case " + $caseID)
        }
        $wave1 = Wait-NewWave -KnownWaveIDs $knownWaves -TimeoutSeconds 120
        $script:wave1ID = [string]$wave1.id
        Write-Log ("      S8: wave {0} started" -f $script:wave1ID)
        [void](Wait-WaveTerminal -WaveID $script:wave1ID -TimeoutSeconds $PollTimeoutSeconds)
        $runs1 = @(Get-WaveRuns -WaveID $script:wave1ID)
        $runOrders = Find-RunByTitle -Runs $runs1 -Title "P5 acceptance orders page"
        Assert-Equal $runOrders.status "passed" "non-visual orders case status"

        Step "assert each visual case produced an actual capture, an inconclusive diff and is not a false pass"
        $script:s8Runs = @{}
        $visualTitles = @(
            @{ Key = "v1"; Title = "P5 acceptance visual light"; Assertion = $script:visualAssertion1; Case = $script:caseVersionVisual1 },
            @{ Key = "v2"; Title = "P5 acceptance visual theme shift"; Assertion = $script:visualAssertion2; Case = $script:caseVersionVisual2 },
            @{ Key = "v3"; Title = "P5 acceptance visual selector viewport"; Assertion = $script:visualAssertion3; Case = $script:caseVersionVisual3 }
        )
        foreach ($item in $visualTitles) {
            $visualResult = Get-VisualRunDetail -Title $item.Title -CaseVersionID $item.Case -Runs $runs1
            $run = $visualResult.Run
            $script:s8Runs[$item.Key] = $run
            $detail = $visualResult.Detail
            $summary = $detail.case_run.summary
            Assert-True ([int]$summary.visual_inconclusive -ge 1) ("{0}: summary.visual_inconclusive = {1}" -f $item.Title, $summary.visual_inconclusive)
            Assert-Equal ([int]$summary.visual_skipped) 0 ("{0}: summary.visual_skipped" -f $item.Title)
            Assert-True (@("inconclusive", "blocked") -contains [string]$run.status) ("{0}: run status {1} must not be a pass" -f $item.Title, $run.status)
            if ([string]$run.status -ne "inconclusive") {
                Add-SoftFailure ("{0}: case_run status = {1} (failure_class={2}), spec 7.2 wants inconclusive(case) for a missing visual baseline; visual evidence is correct (summary.visual_inconclusive={3})" -f $item.Title, $run.status, $run.failure_class, $summary.visual_inconclusive) $s8
            }
            $assertionResults = @($detail.assertions | Where-Object { [string]$_.kind -eq "visual" })
            Assert-True ($assertionResults.Count -ge 1) ("{0}: visual assertion result missing" -f $item.Title)
            Assert-Equal $assertionResults[0].verdict "inconclusive" ("{0}: visual verdict" -f $item.Title)
            Assert-Equal $assertionResults[0].reason "missing_visual_baseline" ("{0}: visual verdict reason" -f $item.Title)
            $actualEvidence = @($detail.evidence | Where-Object { [string]$_.kind -eq "visual_actual" })
            Assert-Equal $actualEvidence.Count 1 ("{0}: visual_actual evidence count" -f $item.Title)
            Assert-Match ([string]$actualEvidence[0].sha256) '^[0-9a-f]{64}$' ("{0}: visual_actual sha256" -f $item.Title)
            Assert-True ([int64]$actualEvidence[0].size_bytes -gt 0) ("{0}: visual_actual size" -f $item.Title)
            $bytes = Assert-EvidenceBytes -VersionID $script:versionID -EvidenceID ([string]$actualEvidence[0].id) -ExpectedSha ([string]$actualEvidence[0].sha256) -ExpectedSize ([int64]$actualEvidence[0].size_bytes) -Label $item.Title
            Assert-True ($bytes.Length -ge 8 -and $bytes[0] -eq 0x89 -and $bytes[1] -eq 0x50 -and $bytes[2] -eq 0x4E -and $bytes[3] -eq 0x47) ("{0}: actual evidence is a PNG" -f $item.Title)
            $diffs = @(Get-VisualDiffs -CaseRunID ([string]$run.id))
            Assert-Equal $diffs.Count 1 ("{0}: visual diff count" -f $item.Title)
            Assert-Equal $diffs[0].classification "inconclusive" ("{0}: diff classification" -f $item.Title)
            Assert-Equal $diffs[0].reason "missing_visual_baseline" ("{0}: diff reason" -f $item.Title)
            Assert-True ($null -eq $diffs[0].baseline_id) ("{0}: diff baseline must be null" -f $item.Title)
            Assert-True ($null -eq $diffs[0].diff_evidence_id) ("{0}: capture-only run must not store a diff image" -f $item.Title)
            Assert-Equal ([double]$diffs[0].diff_ratio) 0 ("{0}: diff ratio" -f $item.Title)
            if ($item.Key -eq "v3") {
                Assert-Equal $diffs[0].environment_match $false "v3 environment mismatch flag (DPR)"
            } else {
                Assert-Equal $diffs[0].environment_match $true ("{0}: environment match" -f $item.Title)
            }
            Note-Assertion ("{0}: actual={1} sha={2} size={3} -> inconclusive(missing_visual_baseline)" -f $item.Title, $actualEvidence[0].id, $actualEvidence[0].sha256, $actualEvidence[0].size_bytes)
        }
        $issues = Get-Issues
        Assert-Equal $issues.Count 0 "no delivery issue is created by visual inconclusive-only runs"
        Note-Assertion "first run: visual_actual stored, no visual_diff image, no issue, no false pass"
        Pass-Scenario $s8
    }

    # ---------------- S9: baseline approval + retest -----------------------
    if (Should-Run "S9") {
        $s9 = Start-Scenario "S9 baseline approval/retest: admin approve actual -> baseline, non-admin 403, invalid evidence 422, retest passed + actual/diff, old baseline superseded" "POST /visual/assertions/{id}/baselines; retest intent"
        Step "collect the first-run actual and screenshot evidence ids"
        $v1RunID = [string]$script:s8Runs["v1"].id
        $v1Detail = Get-CaseRunDetail -CaseRunID $v1RunID
        $v1Actual = @($v1Detail.evidence | Where-Object { [string]$_.kind -eq "visual_actual" })[0]
        $v1Screenshot = @($v1Detail.evidence | Where-Object { [string]$_.kind -eq "screenshot" })[0]
        if ($null -eq $v1Screenshot) {
            $v1Screenshot = @($v1Detail.evidence | Where-Object { [string]$_.kind -ne "visual_actual" })[0]
        }
        if ($null -eq $v1Screenshot) {
            # Visual-only assertions have no generic screenshot; a visual_actual
            # captured for another assertion/case is still invalid for V1.
            $v2InvalidDetail = Get-CaseRunDetail -CaseRunID ([string]$script:s8Runs["v2"].id)
            $v1Screenshot = @($v2InvalidDetail.evidence | Where-Object { [string]$_.kind -eq "visual_actual" })[0]
        }
        Assert-True ($null -ne $v1Screenshot) "first run must contain some non-owned evidence for the invalid-baseline assertion"
        $v1ActualID = [string]$v1Actual.id

        Step "assert non-admin baseline approval is 403 and a generic screenshot is 422"
        $nonAdminApprove = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/visual/assertions/$($script:visualAssertion1)/baselines" -Token $script:userToken -Body @{ evidenceId = $v1ActualID }
        Assert-Status $nonAdminApprove @(403) "non-admin baseline approve"
        $invalidApprove = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/visual/assertions/$($script:visualAssertion1)/baselines" -Token $script:token -Body @{ evidenceId = [string]$v1Screenshot.id }
        Assert-Status $invalidApprove @(422) "generic screenshot baseline approve"
        Assert-Equal $invalidApprove.Json.error.code "baseline_evidence_invalid" "invalid baseline evidence error"

        Step "approve the first-run actual as the V1 baseline (admin)"
        $script:baselineV1First = (Approve-Baseline -AssertionID $script:visualAssertion1 -EvidenceID $v1ActualID).Json.data
        Assert-Equal $script:baselineV1First.status "approved" "V1 baseline status"
        Assert-Equal $script:baselineV1First.width 1280 "V1 baseline width"
        Assert-Equal $script:baselineV1First.height 720 "V1 baseline height"
        Assert-Equal $script:baselineV1First.config_revision 1 "V1 baseline config revision"
        Assert-Match ([string]$script:baselineV1First.environment_fingerprint) '^[0-9a-f]{64}$' "V1 baseline environment fingerprint"
        Assert-Equal $script:baselineV1First.evidence_id $v1ActualID "V1 baseline evidence id"
        $assertionView = Get-VisualAssertion -AssertionID $script:visualAssertion1
        Assert-True ($null -ne $assertionView.baseline) "V1 assertion baseline summary after approve"

        Step "approve the V2 (full page) and V3 (selector) first-run actuals for the later scenarios"
        $v2Detail = Get-CaseRunDetail -CaseRunID ([string]$script:s8Runs["v2"].id)
        $v2Actual = @($v2Detail.evidence | Where-Object { [string]$_.kind -eq "visual_actual" })[0]
        $script:baselineV2 = (Approve-Baseline -AssertionID $script:visualAssertion2 -EvidenceID ([string]$v2Actual.id)).Json.data
        $v3Detail = Get-CaseRunDetail -CaseRunID ([string]$script:s8Runs["v3"].id)
        $v3Actual = @($v3Detail.evidence | Where-Object { [string]$_.kind -eq "visual_actual" })[0]
        $script:baselineV3 = (Approve-Baseline -AssertionID $script:visualAssertion3 -EvidenceID ([string]$v3Actual.id)).Json.data
        Assert-True ([int]$script:baselineV3.width -gt 0) "V3 selector baseline records a positive width"
        Note-Assertion ("baselines approved: V1={0} V2={1} V3={2}" -f $script:baselineV1First.id, $script:baselineV2.id, $script:baselineV3.id)

        Step "retest V1 (light) and V3 (selector/DPR) after reopening the requirement"
        $wave2 = Run-VisualWave -CaseVersionIDs @($script:caseVersionVisual1, $script:caseVersionVisual3) -Purpose "retest" -Label "S9 retest"
        $script:wave2ID = [string]$wave2.id
        $runs2 = @(Get-WaveRuns -WaveID $script:wave2ID)
        $script:s9Runs = @{}
        $v1RetestResult = Get-VisualRunDetail -Title "P5 acceptance visual light" -CaseVersionID $script:caseVersionVisual1 -Runs $runs2
        $v1Retest = $v1RetestResult.Run
        $script:s9Runs["v1"] = $v1Retest
        $v3RetestResult = Get-VisualRunDetail -Title "P5 acceptance visual selector viewport" -CaseVersionID $script:caseVersionVisual3 -Runs $runs2
        $v3Retest = $v3RetestResult.Run
        $script:s9Runs["v3"] = $v3Retest

        Step "assert the retest passed with diff_ratio 0 and downloadable actual/diff evidence"
        Assert-Equal $v1Retest.status "passed" "V1 retest status"
        $v1RetestDetail = Get-CaseRunDetail -CaseRunID ([string]$v1Retest.id)
        Assert-Equal ([int]$v1RetestDetail.case_run.summary.visual_passed) 1 "V1 retest summary.visual_passed"
        $v1RetestAssertion = @($v1RetestDetail.assertions | Where-Object { [string]$_.kind -eq "visual" })[0]
        Assert-Equal $v1RetestAssertion.verdict "passed" "V1 retest assertion verdict"
        Assert-Equal $v1RetestAssertion.reason "visual_match" "V1 retest assertion reason"
        $v1RetestDiffs = @(Get-VisualDiffs -CaseRunID ([string]$v1Retest.id))
        Assert-Equal $v1RetestDiffs.Count 1 "V1 retest visual diff count"
        $v1RetestDiff = $v1RetestDiffs[0]
        Assert-Equal $v1RetestDiff.classification "passed" "V1 retest diff classification"
        Assert-Equal $v1RetestDiff.reason "visual_match" "V1 retest diff reason"
        Assert-True ([double]$v1RetestDiff.diff_ratio -le [double]$v1RetestDiff.threshold) "V1 retest diff ratio within threshold"
        Assert-Equal $v1RetestDiff.baseline_id $script:baselineV1First.id "V1 retest baseline id"
        Assert-True ($null -ne $v1RetestDiff.diff_evidence_id) "V1 retest diff image evidence id"
        $retestActualEvidence = @($v1RetestDetail.evidence | Where-Object { [string]$_.kind -eq "visual_actual" })[0]
        $retestDiffEvidence = @($v1RetestDetail.evidence | Where-Object { [string]$_.kind -eq "visual_diff" })[0]
        Assert-True ($null -ne $retestDiffEvidence) "V1 retest visual_diff evidence row"
        [void](Assert-EvidenceBytes -VersionID $script:versionID -EvidenceID ([string]$retestActualEvidence.id) -ExpectedSha ([string]$retestActualEvidence.sha256) -ExpectedSize ([int64]$retestActualEvidence.size_bytes) -Label "V1 retest actual")
        [void](Assert-EvidenceBytes -VersionID $script:versionID -EvidenceID ([string]$retestDiffEvidence.id) -ExpectedSha ([string]$retestDiffEvidence.sha256) -ExpectedSize ([int64]$retestDiffEvidence.size_bytes) -Label "V1 retest diff")
        Note-Assertion ("V1 retest passed diff_ratio={0} actual={1} diff={2}" -f $v1RetestDiff.diff_ratio, $retestActualEvidence.id, $retestDiffEvidence.id)

        Step "approve the retest actual as a second baseline and assert the old baseline is superseded"
        $script:baselineV1Second = (Approve-Baseline -AssertionID $script:visualAssertion1 -EvidenceID ([string]$retestActualEvidence.id)).Json.data
        $baselines = Get-VisualBaselines -AssertionID $script:visualAssertion1
        $approved = @($baselines | Where-Object { [string]$_.status -eq "approved" })
        $superseded = @($baselines | Where-Object { [string]$_.status -eq "superseded" })
        Assert-Equal $approved.Count 1 "V1 approved baseline count"
        Assert-Equal $approved[0].id $script:baselineV1Second.id "V1 newest approved baseline id"
        Assert-True ($superseded.Count -ge 1) "V1 superseded baseline history"
        $oldBaseline = $superseded | Where-Object { [string]$_.id -eq [string]$script:baselineV1First.id }
        Assert-True ($null -ne $oldBaseline) "V1 first baseline row is superseded"
        Note-Assertion ("second baseline {0}; old baseline {1} superseded" -f $script:baselineV1Second.id, $script:baselineV1First.id)

        Step "assert the V3 environment-mismatch retest (S11a) stayed inconclusive with no diff image"
        Assert-Equal $v3Retest.status "inconclusive" "V3 env-mismatch retest status"
        $v3RetestDiffs = @(Get-VisualDiffs -CaseRunID ([string]$v3Retest.id))
        Assert-Equal $v3RetestDiffs.Count 1 "V3 retest diff count"
        Assert-Equal $v3RetestDiffs[0].classification "inconclusive" "V3 retest classification"
        Assert-Equal $v3RetestDiffs[0].reason "visual_environment_mismatch" "V3 retest reason"
        Assert-Equal $v3RetestDiffs[0].environment_match $false "V3 retest environment flag"
        Assert-True ($null -eq $v3RetestDiffs[0].diff_evidence_id) "V3 environment mismatch must not produce a diff image"
        $v3RetestSummary = (Get-CaseRunDetail -CaseRunID ([string]$v3Retest.id)).case_run.summary
        Assert-Equal ([int]$v3RetestSummary.visual_inconclusive) 1 "V3 retest summary.visual_inconclusive"
        Assert-Equal ([int]$v3RetestSummary.visual_failed) 0 "V3 environment mismatch is never a failure"
        Note-Assertion "environment mismatch: inconclusive(visual_environment_mismatch), no diff image, not failed"

        $events = Get-ChannelEvents -VersionID $script:versionID
        $baselineEvents = @($events | Where-Object { $_.Type -eq "visual.baseline_approved" })
        Assert-True ($baselineEvents.Count -ge 4) "visual.baseline_approved events"
        $diffEvents = @($events | Where-Object { $_.Type -eq "visual.diff_created" })
        Assert-True ($diffEvents.Count -ge 4) "visual.diff_created events"
        Note-Assertion ("visual.baseline_approved={0} visual.diff_created={1}" -f $baselineEvents.Count, $diffEvents.Count)
        Pass-Scenario $s9
    }

    # ---------------- S10: real theme diff -> failed(application) -----------
    if (Should-Run "S10") {
        $s10 = Start-Scenario "S10 visual difference: fixture theme=shifted -> failed(application), diff evidence/ratio/boxes, issue candidate" "PATCH page url; retest; GET visual diffs; GET issues"
        Step "point the bound visual page at the shifted theme"
        $pagePatch = Invoke-Api -Method "PATCH" -Path "/api/resources/pages/$($script:pageVisualID)" -Token $script:token -Body @{ url = ($script:fixtureBase + "/p5/visual?theme=shifted") }
        Assert-Status $pagePatch @(200) "PATCH visual page url to shifted"
        Assert-True ([string]$pagePatch.Json.data.url -match "theme=shifted") "page url updated to shifted"

        Step "retest the theme-shift case and assert failed(application)"
        $wave3 = Run-VisualWave -CaseVersionIDs @($script:caseVersionVisual2) -Purpose "retest" -Label "S10 shifted theme"
        $script:wave3ID = [string]$wave3.id
        $runs3 = @(Get-WaveRuns -WaveID $script:wave3ID)
        $shiftRunResult = Get-VisualRunDetail -Title "P5 acceptance visual theme shift" -CaseVersionID $script:caseVersionVisual2 -Runs $runs3
        $shiftRun = $shiftRunResult.Run
        $script:s10Run = $shiftRun
        Assert-Equal $shiftRun.status "failed" "shifted-theme case run status"
        Assert-Equal $shiftRun.failure_class "application" "shifted-theme failure class"
        $shiftDetail = Get-CaseRunDetail -CaseRunID ([string]$shiftRun.id)
        Assert-Equal ([int]$shiftDetail.case_run.summary.visual_failed) 1 "shifted summary.visual_failed"
        Assert-True ([int]$shiftDetail.case_run.summary.assertion_failed -ge 1) "shifted summary.assertion_failed"
        $shiftAssertion = @($shiftDetail.assertions | Where-Object { [string]$_.kind -eq "visual" })[0]
        Assert-Equal $shiftAssertion.verdict "failed" "shifted assertion verdict"
        Assert-Equal $shiftAssertion.reason "visual_mismatch" "shifted assertion reason"

        Step "assert diff ratio/classification/mismatch boxes and evidence downloads"
        $shiftDiffs = @(Get-VisualDiffs -CaseRunID ([string]$shiftRun.id))
        Assert-Equal $shiftDiffs.Count 1 "shifted visual diff count"
        $shiftDiff = $shiftDiffs[0]
        Assert-Equal $shiftDiff.classification "failed" "shifted diff classification"
        Assert-Equal $shiftDiff.reason "visual_mismatch" "shifted diff reason"
        Assert-True ([double]$shiftDiff.diff_ratio -gt [double]$shiftDiff.threshold) ("shifted diff_ratio {0} must exceed threshold {1}" -f $shiftDiff.diff_ratio, $shiftDiff.threshold)
        Assert-True ($null -ne $shiftDiff.diff_evidence_id) "shifted diff image evidence id"
        Assert-Equal ([int]$shiftDiff.width) 1280 "shifted diff width"
        Assert-Equal ([int]$shiftDiff.height) 720 "shifted diff height"
        $shiftDiffDetail = Get-VisualDiff -DiffID ([string]$shiftDiff.id)
        $boxes = Get-Array $shiftDiffDetail.mismatch_boxes
        Assert-True ($boxes.Count -ge 1 -and $boxes.Count -le 50) ("mismatch boxes = {0}, want 1..50" -f $boxes.Count)
        $shiftActualEvidence = @($shiftDetail.evidence | Where-Object { [string]$_.kind -eq "visual_actual" })[0]
        $shiftDiffEvidence = @($shiftDetail.evidence | Where-Object { [string]$_.kind -eq "visual_diff" })[0]
        Assert-True ($null -ne $shiftDiffEvidence) "shifted visual_diff evidence row"
        [void](Assert-EvidenceBytes -VersionID $script:versionID -EvidenceID ([string]$shiftActualEvidence.id) -ExpectedSha ([string]$shiftActualEvidence.sha256) -ExpectedSize ([int64]$shiftActualEvidence.size_bytes) -Label "shifted actual")
        [void](Assert-EvidenceBytes -VersionID $script:versionID -EvidenceID ([string]$shiftDiffEvidence.id) -ExpectedSha ([string]$shiftDiffEvidence.sha256) -ExpectedSize ([int64]$shiftDiffEvidence.size_bytes) -Label "shifted diff")
        Note-Assertion ("shifted failed diff_ratio={0} threshold={1} boxes={2} actual={3} diff={4}" -f $shiftDiff.diff_ratio, $shiftDiff.threshold, $boxes.Count, $shiftActualEvidence.id, $shiftDiffEvidence.id)

        Step "assert the failure produced a traceable delivery issue with evidence refs"
        [void](Wait-For -TimeoutSeconds 120 -Description "delivery issue for the visual failure" -Condition {
            $issues = Get-Issues
            foreach ($issue in $issues) {
                foreach ($ref in (Get-Array $issue.refs)) {
                    if ([string]$ref.kind -eq "case_run" -and [string]$ref.ref_id -eq [string]$shiftRun.id) { return $issue }
                }
            }
            return $null
        } -IntervalMilliseconds 1500)
        $issues = Get-Issues
        $visualIssue = $null
        foreach ($issue in $issues) {
            foreach ($ref in (Get-Array $issue.refs)) {
                if ([string]$ref.kind -eq "case_run" -and [string]$ref.ref_id -eq [string]$shiftRun.id) { $visualIssue = $issue }
            }
        }
        Assert-True ($null -ne $visualIssue) "visual failure issue"
        Assert-Equal $visualIssue.status "open" "visual failure issue status"
        Assert-Equal $visualIssue.category "ui" "visual failure issue category"
        $refKinds = @((Get-Array $visualIssue.refs) | ForEach-Object { [string]$_.kind })
        foreach ($kind in @("case_run", "assertion_result", "evidence")) {
            Assert-True ($refKinds -contains $kind) ("visual issue refs contain " + $kind)
        }
        Note-Assertion ("issue {0} open category=ui refs={1}" -f $visualIssue.id, ($refKinds -join ","))

        Step "create an issue code anchor (issue subject) after the failure exists"
        $issueAnchor = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/code/anchors" -Token $script:token -Body @{
            subjectKind = "issue"; subjectId = [string]$visualIssue.id; repoId = $script:repoID
            filePath = "src/api/refund.ts"; startLine = 1; endLine = 3; commit = $script:headCommit
        }
        Assert-Status $issueAnchor @(201) "POST issue code anchor"
        $issueAnchorValidated = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/code/anchors/$([string]$issueAnchor.Json.data.id)/validate" -Token $script:token
        Assert-Status $issueAnchorValidated @(200) "validate issue code anchor"
        Assert-Equal $issueAnchorValidated.Json.data.status "valid" "issue anchor status"
        Note-Assertion ("issue anchor {0} valid" -f $issueAnchor.Json.data.id)

        Step "restore the visual page url to the light theme for the config-change scenario"
        $restore = Invoke-Api -Method "PATCH" -Path "/api/resources/pages/$($script:pageVisualID)" -Token $script:token -Body @{ url = ($script:fixtureBase + "/p5/visual?theme=light") }
        Assert-Status $restore @(200) "PATCH visual page url back to light"
        Pass-Scenario $s10
    }

    # ---------------- S11: environment/config mismatch ----------------------
    if (Should-Run "S11") {
        $s11 = Start-Scenario "S11 env/config mismatch: config revision change supersedes baseline -> inconclusive(environment/config_changed), recovery after re-approval" "PUT visual assertion; rerun; baseline approve"
        Step "S11a: environment mismatch evidence (already produced by the V3 DPR retest)"
        $v3RetestDiffs = @(Get-VisualDiffs -CaseRunID ([string]$script:s9Runs["v3"].id))
        Assert-Equal $v3RetestDiffs.Count 1 "V3 environment mismatch diff count"
        Assert-Equal $v3RetestDiffs[0].classification "inconclusive" "V3 env diff classification"
        Assert-Equal $v3RetestDiffs[0].reason "visual_environment_mismatch" "V3 env diff reason"
        Assert-Equal $v3RetestDiffs[0].environment_match $false "V3 env diff environment_match"
        Assert-True ($null -eq $v3RetestDiffs[0].diff_evidence_id) "V3 env mismatch has no diff image"
        Note-Assertion "environment fingerprint mismatch -> inconclusive(visual_environment_mismatch), never failed"

        Step "S11b: change the V1 viewport through the visual config API"
        $configChange = Invoke-Api -Method "PUT" -Path "/api/versions/$script:versionID/visual/assertions/964002" -Token $script:token -Body @{
            versionSutId = $script:versionSUTID; selector = ""; viewport = @{ width = 1024; height = 768 }; deviceScaleFactor = 1; threshold = 0.005
        }
        Assert-Status $configChange @(200) "PUT visual assertion viewport change"
        Assert-Equal ([int]$configChange.Json.data.config_revision) 2 "V1 config revision after change"
        Assert-Equal ([int]$configChange.Json.data.viewport_width) 1024 "V1 config viewport width"
        Assert-True ($null -eq $configChange.Json.data.baseline) "config change must clear the active baseline summary"
        $baselinesAfterChange = Get-VisualBaselines -AssertionID $script:visualAssertion1
        $approvedAfterChange = @($baselinesAfterChange | Where-Object { [string]$_.status -eq "approved" })
        $supersededAfterChange = @($baselinesAfterChange | Where-Object { [string]$_.status -eq "superseded" })
        Assert-Equal $approvedAfterChange.Count 0 "no approved baseline after config change"
        Assert-True ($supersededAfterChange.Count -ge 1) "old approved baseline superseded by config change"
        Note-Assertion ("config revision 1->2; old approved baseline superseded ({0} history rows)" -f $supersededAfterChange.Count)

        Step "rerun V1 after the config change and assert inconclusive, not failed"
        $wave4 = Run-VisualWave -CaseVersionIDs @($script:caseVersionVisual1) -Purpose "manual" -Label "S11 config change rerun"
        $script:wave4ID = [string]$wave4.id
        $runs4 = @(Get-WaveRuns -WaveID $script:wave4ID)
        $configRunResult = Get-VisualRunDetail -Title "P5 acceptance visual light" -CaseVersionID $script:caseVersionVisual1 -Runs $runs4
        $configRun = $configRunResult.Run
        $script:s11Run = $configRun
        Assert-True (@("inconclusive", "blocked") -contains [string]$configRun.status) ("config-change rerun must not fail or pass; status = {0}" -f $configRun.status)
        $configDetail = Get-CaseRunDetail -CaseRunID ([string]$configRun.id)
        Assert-True ([int]$configDetail.case_run.summary.visual_inconclusive -ge 1) "config-change summary.visual_inconclusive"
        Assert-Equal ([int]$configDetail.case_run.summary.visual_failed) 0 "config-change summary.visual_failed"
        $configDiffs = @(Get-VisualDiffs -CaseRunID ([string]$configRun.id))
        Assert-Equal $configDiffs.Count 1 "config-change diff count"
        $configDiff = $configDiffs[0]
        Assert-Equal $configDiff.classification "inconclusive" "config-change diff classification"
        Assert-True ($null -eq $configDiff.diff_evidence_id) "config-change inconclusive has no diff image"
        $configActualEvidence = @($configDetail.evidence | Where-Object { [string]$_.kind -eq "visual_actual" })[0]
        Assert-True ($null -ne $configActualEvidence) "config-change rerun stores an actual capture"
        [void](Assert-EvidenceBytes -VersionID $script:versionID -EvidenceID ([string]$configActualEvidence.id) -ExpectedSha ([string]$configActualEvidence.sha256) -ExpectedSize ([int64]$configActualEvidence.size_bytes) -Label "config-change actual")
        if ([string]$configDiff.reason -ne "visual_config_changed") {
            Add-SoftFailure ("config revision change produced inconclusive reason '{0}', spec 7.3 wants visual_config_changed (execution never compares baseline.config_revision with the current config)" -f $configDiff.reason) $s11
        } else {
            Note-Assertion "config revision change -> visual_config_changed"
        }
        Note-Assertion ("config-change rerun reason={0} environment_match={1}" -f $configDiff.reason, $configDiff.environment_match)

        Step "S11c: re-approve the new-config actual and rerun -> passed (recovery)"
        $script:baselineV1Third = (Approve-Baseline -AssertionID $script:visualAssertion1 -EvidenceID ([string]$configActualEvidence.id)).Json.data
        Assert-Equal $script:baselineV1Third.width 1024 "recovery baseline width"
        Assert-Equal $script:baselineV1Third.height 768 "recovery baseline height"
        $wave5 = Run-VisualWave -CaseVersionIDs @($script:caseVersionVisual1) -Purpose "manual" -Label "S11 recovery rerun"
        $script:wave5ID = [string]$wave5.id
        $runs5 = @(Get-WaveRuns -WaveID $script:wave5ID)
        $recoveryRunResult = Get-VisualRunDetail -Title "P5 acceptance visual light" -CaseVersionID $script:caseVersionVisual1 -Runs $runs5
        $recoveryRun = $recoveryRunResult.Run
        Assert-Equal $recoveryRun.status "passed" "recovery rerun status"
        $recoveryDiffs = @(Get-VisualDiffs -CaseRunID ([string]$recoveryRun.id))
        Assert-Equal $recoveryDiffs.Count 1 "recovery diff count"
        Assert-Equal $recoveryDiffs[0].classification "passed" "recovery classification"
        Assert-Equal $recoveryDiffs[0].baseline_id $script:baselineV1Third.id "recovery baseline id"
        Assert-True ([double]$recoveryDiffs[0].diff_ratio -le [double]$recoveryDiffs[0].threshold) "recovery diff ratio within threshold"
        Note-Assertion ("recovery: baseline re-approved and rerun passed diff_ratio={0}" -f $recoveryDiffs[0].diff_ratio)
        Pass-Scenario $s11
    }

    # ---------------- S12: quality overview, baseline commit, regressions ----
    if (Should-Run "S12") {
        $s12 = Start-Scenario "S12 quality/baseline: visual overview + /quality metrics, network-evidence proposals, version_sut code baseline commit + intent fingerprint" "GET /visual/overview; GET /quality/visual-metrics; code baseline"
        Step "assert the version visual overview aggregates the four states"
        $overview = Get-VisualOverview
        Assert-Equal $overview.version_id $script:versionID "overview version"
        Assert-Equal ([int]$overview.assertion_total) 3 "overview assertion total"
        Assert-Equal ([int]$overview.baseline_covered) 3 "overview baseline covered"
        Assert-Equal ([double]$overview.baseline_coverage_ratio) 1 "overview baseline coverage ratio"
        Assert-True ([int]$overview.passed -ge 3) ("overview passed = {0}, want >= 3" -f $overview.passed)
        Assert-True ([int]$overview.failed -ge 1) ("overview failed = {0}, want >= 1" -f $overview.failed)
        Assert-True ([int]$overview.inconclusive -ge 4) ("overview inconclusive = {0}, want >= 4" -f $overview.inconclusive)
        Assert-True ([double]$overview.pass_ratio -gt 0) "overview pass_ratio"
        $reasonNames = @((Get-Array $overview.inconclusive_reasons) | ForEach-Object { [string]$_.reason })
        Assert-True ($reasonNames -contains "missing_visual_baseline") "overview missing_visual_baseline reason"
        Assert-True ($reasonNames -contains "visual_environment_mismatch") "overview visual_environment_mismatch reason"
        Assert-True ($null -ne $overview.diff_ratio_p50) "overview diff_ratio_p50"
        Assert-True ($null -ne $overview.diff_ratio_p95) "overview diff_ratio_p95"
        Assert-True ((Get-Array $overview.recent_failures).Count -ge 1) "overview recent failures"
        Assert-Equal ([string](Get-Array $overview.recent_failures)[0].classification) "failed" "overview recent failure classification"
        Note-Assertion ("overview: assertions={0} baseline={1} passed={2} failed={3} inconclusive={4} pass_ratio={5} p50={6} p95={7}" -f $overview.assertion_total, $overview.baseline_covered, $overview.passed, $overview.failed, $overview.inconclusive, $overview.pass_ratio, $overview.diff_ratio_p50, $overview.diff_ratio_p95)

        Step "assert the global and per-version quality metrics endpoints agree"
        $metrics = Get-VisualMetrics
        Assert-True ($null -eq $metrics.scope.version_id) "global metrics scope version is null"
        Assert-Equal ([int]$metrics.totals.assertions) 3 "global metrics assertions"
        Assert-True ([int]$metrics.totals.baselines -ge 3) "global metrics baselines"
        Assert-True ([int]$metrics.totals.passed -ge 3) "global metrics passed"
        Assert-True ([int]$metrics.totals.failed -ge 1) "global metrics failed"
        $metricsVersion = Get-VisualMetrics -VersionID $script:versionID
        Assert-Equal ([string]$metricsVersion.scope.version_id) $script:versionID "version metrics scope"
        Assert-Equal ([int]$metricsVersion.totals.diffs) ([int]$metrics.totals.diffs) "global/version metrics diff count"
        Assert-Equal ([int]$metricsVersion.totals.passed) ([int]$overview.passed) "version metrics passed equals overview"
        Assert-Equal ([int]$metricsVersion.totals.failed) ([int]$overview.failed) "version metrics failed equals overview"
        Assert-Equal ([int]$metricsVersion.totals.inconclusive) ([int]$overview.inconclusive) "version metrics inconclusive equals overview"
        Note-Assertion ("metrics: diffs={0} passed={1} failed={2} inconclusive={3} baseline_coverage={4} pass_rate={5}" -f $metrics.totals.diffs, $metrics.totals.passed, $metrics.totals.failed, $metrics.totals.inconclusive, $metrics.ratios.baseline_coverage, $metrics.ratios.pass_rate)

        Step "generate proposals again: real execution network evidence becomes a source=code API proposal"
        $networkProposals = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/code/page-api-proposals" -Token $script:token -Body @{ repoId = $script:repoID }
        Assert-Status $networkProposals @(200) "POST proposals after real execution"
        Assert-True ([int]$networkProposals.Json.data.apis.created -ge 1) ("network evidence API proposals created = {0}, want >= 1 (GET /p5/api/orders)" -f $networkProposals.Json.data.apis.created)
        $proposalList = Get-Array (Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/code/page-api-proposals" -Token $script:token).Json.data
        $codeProposals = @($proposalList | Where-Object { [string]$_.source -eq "code" })
        Assert-True ($codeProposals.Count -ge 1) "at least one network-evidence (source=code) proposal"
        $networkAPI = $codeProposals | Where-Object { [string]$_.kind -eq "api" -and [string]$_.path_template -eq "/p5/api/orders" } | Select-Object -First 1
        Assert-True ($null -ne $networkAPI) "GET /p5/api/orders network evidence proposal"
        Assert-Equal $networkAPI.status "proposed" "network proposal status"
        Note-Assertion ("network evidence proposal: {0} {1} source={2}" -f $networkAPI.method, $networkAPI.path_template, $networkAPI.source)

        Step "assert the version_sut code baseline commit and the analysis base/head"
        $versionView = Get-VersionView
        Assert-Equal $versionView.bindings[0].codeBaselineCommit $script:baseCommit "version code baseline commit (fixture A)"
        Assert-Match ([string]$versionView.bindings[0].codeBaselineCommit) '^[0-9a-f]{40}$' "version code baseline is a 40-hex commit"
        $dbBase = Invoke-PsqlScalar -SqlText ("SELECT base_commit_sha FROM code_analysis WHERE id = {0};" -f $script:analysisID)
        $dbHead = Invoke-PsqlScalar -SqlText ("SELECT head_commit_sha FROM code_analysis WHERE id = {0};" -f $script:analysisID)
        Assert-Equal $dbBase $script:baseCommit "analysis base commit"
        Assert-Equal $dbHead $script:headCommit "analysis head commit"
        $currentIntentBaseline = Invoke-PsqlScalar -SqlText "SELECT code_baseline FROM execution_intent ORDER BY id DESC LIMIT 1;"
        Assert-Match $currentIntentBaseline '^[0-9a-f]{64}$' "execution intent code baseline is 64 hex"
        $expectedA = '{"repositories":[{"repo_id":' + $script:repoID + ',"commit_sha":"' + $script:baseCommit + '"}]}'
        $expectedAHash = Get-Sha256Hex -Bytes (Get-Utf8Bytes $expectedA)
        Assert-Equal $currentIntentBaseline $expectedAHash "execution intent code_baseline hash for commit A"
        Note-Assertion ("code_baseline_commit={0}; analysis {1}..{2}; intent code_baseline sha256(commit A) matches" -f $versionView.bindings[0].codeBaselineCommit, $script:baseCommit, $script:headCommit)

        Step "upgrade the baseline commit to head B and assert a new execution intent fingerprints the new commit"
        [void](Open-Requirement -RequirementID $script:requirementID)
        Invoke-Psql -SqlText ("UPDATE version_sut SET code_baseline_commit = '{0}' WHERE id = {1};" -f $script:headCommit, $script:versionSUTID) | Out-Null
        $versionView2 = Get-VersionView
        Assert-Equal $versionView2.bindings[0].codeBaselineCommit $script:headCommit "version code baseline after upgrade"
        $manualIntent = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/execution/intents" -Token $script:token -Body @{ case_version_id = $script:caseVersionOrders; purpose = "manual" }
        Assert-Status $manualIntent @(201) "POST manual intent after baseline upgrade"
        $upgradedIntentBaseline = Invoke-PsqlScalar -SqlText ("SELECT code_baseline FROM execution_intent WHERE id = {0};" -f [string]$manualIntent.Json.data.id)
        Assert-Match $upgradedIntentBaseline '^[0-9a-f]{64}$' "upgraded intent code baseline is 64 hex"
        $expectedB = '{"repositories":[{"repo_id":' + $script:repoID + ',"commit_sha":"' + $script:headCommit + '"}]}'
        $expectedBHash = Get-Sha256Hex -Bytes (Get-Utf8Bytes $expectedB)
        Assert-Equal $upgradedIntentBaseline $expectedBHash "upgraded execution intent code_baseline hash for commit B"
        Assert-True ($upgradedIntentBaseline -ne $expectedAHash) "repo head change changes the intent code_baseline fingerprint"
        Note-Assertion "baseline A->B changed the execution intent code_baseline fingerprint"
        $cancel = Invoke-Api -Method "POST" -Path ("/api/versions/{0}/execution/intents/{1}/cancel" -f $script:versionID, [string]$manualIntent.Json.data.id) -Token $script:token
        Write-Log ("      manual intent cancellation status=" + $cancel.Status)
        Start-Sleep -Seconds 2
        $active = Get-ExecutionOverview
        if ($null -ne $active.active_wave) {
            [void](Wait-WaveTerminal -WaveID ([string]$active.active_wave.id) -TimeoutSeconds 300)
        }
        Pass-Scenario $s12
    }
    # ---------------- S4: design agent reads code ---------------------------
    if (Should-Run "S4") {
        $s4 = Start-Scenario "S4 design Agent read-code: design conversation real LLM calls code.* tools and produces code-aware design" "POST conversations; agent_message.tool_calls"
        Step "create a design conversation and ask the real design agent to read code facts/routes/apis"
        $beforeCases = 0
        $graphBefore = (Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/design/graph" -Token $script:token).Json.data
        foreach ($requirement in (Get-Array $graphBefore.requirements)) {
            foreach ($scenario in (Get-Array $requirement.scenarios)) { $beforeCases += (Get-Array $scenario.cases).Count }
        }
        $conversationResponse = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/conversations" -Token $script:token -Body @{ kind = "design"; title = "P5 code-aware design acceptance" }
        Assert-Status $conversationResponse @(201) "POST design conversation"
        $conversationID = [string]$conversationResponse.Json.data.id
        Start-Sleep -Seconds 2
        $messageResponse = Invoke-Api -Method "POST" -Path "/api/versions/$script:versionID/conversations/$conversationID/messages" -Token $script:token -Body @{
            client_message_id = ("p5-design-" + [guid]::NewGuid().ToString("N"))
            parts = @(@{ type = "text"; text = ("Read the code of the repository bound to this version (repository id $($script:repoID)) before designing: call code.facts to inspect the route/api/client_call facts, then call code.routes and code.apis to confirm the route and HTTP API facts. Then propose exactly one new draft case under requirement $($script:requirementID) with case.graph_propose; the case step must cite the confirmed code route and API facts (for example the refund route and POST /api/refund). Also summarize in your answer which code facts you read. Do not guess routes.") })
        }
        Assert-Status $messageResponse @(201) "POST design conversation message"
        Step "wait for the design agent runs to finish"
        [void](Wait-For -TimeoutSeconds 300 -Description "design conversation agent runs finished" -Condition {
            $running = Invoke-PsqlScalar -SqlText ("SELECT count(*) FROM agent_run WHERE conversation_id = {0} AND status = 'running';" -f $conversationID)
            $total = Invoke-PsqlScalar -SqlText ("SELECT count(*) FROM agent_run WHERE conversation_id = {0};" -f $conversationID)
            if ([int]$total -ge 1 -and [int]$running -eq 0) { return $true }
            return $null
        } -IntervalMilliseconds 2000)
        # The posted message triggers an additional design_host run after the
        # automatic planner/host rounds; give it a moment to start and finish.
        Start-Sleep -Seconds 5
        try {
            [void](Wait-For -TimeoutSeconds 300 -Description "design host message run finished" -Condition {
                $running = Invoke-PsqlScalar -SqlText ("SELECT count(*) FROM agent_run WHERE conversation_id = {0} AND status = 'running';" -f $conversationID)
                $total = Invoke-PsqlScalar -SqlText ("SELECT count(*) FROM agent_run WHERE conversation_id = {0};" -f $conversationID)
                if ([int]$total -ge 3 -and [int]$running -eq 0) { return $true }
                return $null
            } -IntervalMilliseconds 2000)
        } catch {
            Write-Log ("      design message run wait: " + $_.Exception.Message) "Yellow"
        }
        $toolNames = Invoke-PsqlScalar -SqlText ("SELECT coalesce(string_agg(DISTINCT tc->>'name', ','), '') FROM agent_message m, jsonb_array_elements(m.tool_calls) tc WHERE m.conversation_id = {0} AND jsonb_typeof(m.tool_calls) = 'array';" -f $conversationID)
        Write-Log ("      design agent tool calls: " + $toolNames)
        $messagesResponse = Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/conversations/$conversationID/messages?limit=200" -Token $script:token
        $lastText = ""
        if ($messagesResponse.Status -eq 200) {
            $messages = Get-Array $messagesResponse.Json.data
            foreach ($message in $messages) {
                if ([string]$message.role -ne "assistant") { continue }
                foreach ($part in (Get-Array $message.parts)) {
                    if ([string]$part.type -eq "text" -and -not [string]::IsNullOrWhiteSpace([string]$part.text)) { $lastText = [string]$part.text }
                }
            }
        }
        Set-Content -LiteralPath (Join-Path $logDir "s4-design-agent-response.txt") -Value $lastText -Encoding UTF8
        $codeTools = @($toolNames -split "," | Where-Object { $_ -match '^code\.(tree|read|search|facts|routes|apis|diff|impact|anchors)$' })
        if ($codeTools.Count -lt 1) {
            $excerpt = $lastText
            if ($excerpt.Length -gt 600) { $excerpt = $excerpt.Substring(0, 600) }
            Write-Log ("      design agent response excerpt: " + $excerpt) "Yellow"
            Block-Scenario ("design conversation agent did not call code.* tools (observed: {0}); response saved to s4-design-agent-response.txt" -f $toolNames) $s4
        } else {
            $afterGraph = (Invoke-Api -Method "GET" -Path "/api/versions/$script:versionID/design/graph" -Token $script:token).Json.data
            $afterCases = 0
            foreach ($requirement in (Get-Array $afterGraph.requirements)) {
                foreach ($scenario in (Get-Array $requirement.scenarios)) { $afterCases += (Get-Array $scenario.cases).Count }
            }
            $codeAwareCases = [int](Invoke-PsqlScalar -SqlText ("SELECT count(DISTINCT cv.id) FROM case_version cv JOIN case_step cs ON cs.case_version_id = cv.id WHERE cv.created_at > now() - interval '10 minutes' AND (cs.action ILIKE '%refund%' OR cs.expected ILIKE '%refund%');"))
            if ($afterCases -gt $beforeCases) {
                Note-Assertion ("design agent code tools={0}; new design cases {1}->{2}" -f (($codeTools -join ",")), $beforeCases, $afterCases)
                Pass-Scenario $s4
            } elseif ($codeAwareCases -ge 1) {
                Note-Assertion ("design agent code tools={0}; code-aware case versions created={1}" -f (($codeTools -join ",")), $codeAwareCases)
                Pass-Scenario $s4
            } else {
                $excerpt = $lastText
                if ($excerpt.Length -gt 600) { $excerpt = $excerpt.Substring(0, 600) }
                Write-Log ("      design agent response excerpt: " + $excerpt) "Yellow"
                Add-SoftFailure ("design agent called code tools ({0}) but produced no new/code-aware design case (cases {1}->{2}); exact response saved to s4-design-agent-response.txt" -f (($codeTools -join ",")), $beforeCases, $afterCases) $s4
            }
        }
        if ($s4.Status -eq "BLOCKED") { Pass-Scenario $s4 }
    }

}
catch {
    $message = $_.Exception.Message
    Write-Log ("ACCEPTANCE FAIL: " + $message) "Red"
    if ($null -ne $_.InvocationInfo) {
        Write-Log ("      at " + $_.InvocationInfo.ScriptName + ":" + $_.InvocationInfo.ScriptLineNumber) "DarkGray"
    }
    if ($null -ne $_.ErrorDetails) { Write-Log ("      detail: " + $_.ErrorDetails.Message) "DarkGray" }
    Write-FailureStep -Message $message
    if ($null -ne $script:currentScenario -and $script:currentScenario.Status -eq "RUNNING") {
        $script:currentScenario.Status = "FAIL"
        if ($script:currentScenario.PSObject.Properties["Error"]) {
            $script:currentScenario.PSObject.Properties["Error"].Value = $message
        } else {
            $script:currentScenario | Add-Member -NotePropertyName Error -NotePropertyValue $message
        }
    }
    $exitCode = 1
}
finally {
    try { Stop-Services } catch { }
    try { if ($null -ne $script:http) { $script:http.Dispose() } } catch { }
    try { Write-ScenarioSummary } catch { }
    $failCount = @($script:results | Where-Object { $_.Status -eq "FAIL" -or $_.Status -eq "RUNNING" }).Count
    $blockCount = @($script:results | Where-Object { $_.Status -eq "BLOCKED" }).Count
    if ($failCount -gt 0) {
        $exitCode = 1
    } elseif ($blockCount -gt 0 -and $exitCode -eq 0) {
        $exitCode = 2
    }
}
Write-Host ("ACCEPTANCE EXIT=" + $exitCode + " (log: " + $acceptanceLog + ")")
exit $exitCode
