#requires -Version 5.1
<#
.SYNOPSIS
    P8 multi-replica drill (S8): two API replicas, rolling drain/leader takeover,
    cross-instance SSE, hard-kill recovery and real executor loss/redispatch.

.DESCRIPTION
    Starts two local EzTest API processes from one built binary, sharing one
    database and one content root, plus two real Playwright executors. The drill
    asserts:
      1. both replicas ready, registered with snowflake node ids 1 and 2, and
         exactly one PG advisory leader lock;
      2. an admin setting updated through A is visible on B within 2s (P8 H1
         cross-instance settings reload);
      3. while a real case run and a slow in-flight HTTP request are running on
         A, draining A flips its readiness to 503, exposes active_requests /
         active_work through GET /api/admin/instances, and both counters return
         to zero before A is stopped; B stays ready;
      4. after A stops, the leader advisory lock moves to B; work_item/agent_run/
         case_run counts are unchanged, their uniqueness invariants hold and the
         migration ledger was applied once;
      5. B's SSE stream receives an event published through A; a reconnect with
         Last-Event-ID replays the same event;
      6. after a hard (taskkill /F) kill the stale api_instance row becomes
         offline, the surviving replica stays ready, and restarting the replica
         does not duplicate work or migrations;
      7. a real case run dispatched to executor B is interrupted when B is
         killed: case_run failure_class=executor_lost, dispatch_epoch increments,
         redispatch to executor A succeeds, and other running case runs are not
         batch-interrupted. If the database has no current+confirmed case with a
         SUT binding (the P8 S5 data), the scenario fails with the exact blocker
         instead of inserting a synthetic case_run.

    Evidence is written under .tmp/p8-multi/ (scenarios.txt + multi.log + API/
    executor/fixture logs + JSON snapshots). The script exits 0 only when every
    check passes; optional checks that cannot be exercised in the current data
    set are recorded as SKIP with the reason.

.PARAMETER DatabaseName
    Target database (default eztest_v02_p8). The database part of
    EZTEST_DATABASE_URL (or -DatabaseUrl) is replaced by this name. The database
    must already exist; this script never creates or drops it.

.PARAMETER ApiPort1
    Port of API replica A (default 13341).

.PARAMETER ApiPort2
    Port of API replica B (default 13342).

.PARAMETER ExecutorPortA
    Port of real executor A (default 13345).

.PARAMETER ExecutorPortB
    Port of real executor B (default 13346).

.PARAMETER KeepRunning
    Leave the surviving API/executor/fixture processes running for inspection.
    Executor B is intentionally killed by the executor-loss scenario and is not
    restarted.

.PARAMETER DatabaseUrl
    Optional base PostgreSQL URL. Defaults to EZTEST_DATABASE_URL from the
    environment or the repository .env. The value is never printed or written to
    evidence.

.PARAMETER AdminUsername
    Admin account used for the drill (default admin).

.PARAMETER AdminPassword
    Password for -AdminUsername. Never printed or written to evidence.

.PARAMETER ValidateOnly
    Parse the script with the PowerShell AST, validate parameters/toolchain/
    flow markers, optionally probe the database read-only, and exit without
    starting processes or modifying data.

.NOTES
    Windows PowerShell 5.1 and PowerShell 7+. Loopback development/acceptance
    database only. The script never drops or truncates data, never edits product
    code, and never prints secrets.
#>
[CmdletBinding()]
param(
    [string]$DatabaseName = "eztest_v02_p8",
    [int]$ApiPort1 = 13341,
    [int]$ApiPort2 = 13342,
    [int]$ExecutorPortA = 13345,
    [int]$ExecutorPortB = 13346,
    [switch]$KeepRunning,
    [string]$DatabaseUrl = "",
    [string]$AdminUsername = "admin",
    [string]$AdminPassword = "admin123",
    [switch]$ValidateOnly
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$backendDir = Join-Path $repoRoot "backend"
$executorDir = Join-Path $repoRoot "executor"
$tmpDir = Join-Path $repoRoot ".tmp\p8-multi"
$logPath = Join-Path $tmpDir "multi.log"
$scenarioPath = Join-Path $tmpDir "scenarios.txt"
$envPath = Join-Path $repoRoot ".env"
$expectedAPIVersion = "0.2.0-p8"
# 0x455A5431, the P7/P8 leader advisory lock (orchestration design section 4.5).
$leaderLockKey = 1163547697

$script:goExe = ""
$script:psqlExe = ""
$script:nodeExe = ""
$script:curlExe = ""
$script:dbHost = ""
$script:dbPort = "5432"
$script:dbUser = ""
$script:dbPassword = ""
$script:dbName = $DatabaseName
$script:effectiveDatabaseUrl = ""
$script:executorToken = ""
$script:dataRoot = ""
$script:apiBinary = ""
$script:token = ""
$script:expectedMigrations = 0
$script:previousEnv = @{}
$script:processes = @()
$script:fixtures = @()
$script:createdWaves = @()
$script:checks = @()
$script:passCount = 0
$script:failCount = 0
$script:skipCount = 0
$script:apiA = $null
$script:apiB = $null
$script:apiAStarts = 0
$script:apiBStarts = 0
$script:executorA = $null
$script:executorB = $null
$script:executorAID = ""
$script:executorBID = ""
$script:duplicateBaseline = $null
$script:slowHoldJob = $null
$script:sseProcesses = @()
$script:leaderPidBeforeStop = ""
$script:leaderPidAfterTakeover = ""
$script:closed = $false

# ---------------------------------------------------------------------------
# logging / check primitives
# ---------------------------------------------------------------------------

function Initialize-Evidence {
    if (-not (Test-Path -LiteralPath $tmpDir)) {
        $null = New-Item -ItemType Directory -Path $tmpDir -Force
    }
    $header = @(
        "EzTest P8 multi-replica drill (S8)",
        ("started: " + (Get-Date).ToString("yyyy-MM-ddTHH:mm:sszzz")),
        ("command: scripts/p8-multi.ps1 -DatabaseName " + $DatabaseName +
            " -ApiPort1 " + $ApiPort1 + " -ApiPort2 " + $ApiPort2 +
            " -ExecutorPortA " + $ExecutorPortA + " -ExecutorPortB " + $ExecutorPortB +
            $(if ($KeepRunning) { " -KeepRunning" } else { "" })),
        ("host: " + $env:COMPUTERNAME + " | powershell: " + $PSVersionTable.PSVersion.ToString()),
        ("database: " + $DatabaseName),
        ("api ports: " + $ApiPort1 + "," + $ApiPort2),
        ("executor ports: " + $ExecutorPortA + "," + $ExecutorPortB),
        ("keep running: " + [bool]$KeepRunning),
        ""
    )
    Set-Content -LiteralPath $scenarioPath -Value $header -Encoding UTF8
    Set-Content -LiteralPath $logPath -Value "" -Encoding UTF8
}

function Write-Log {
    param([string]$Message, [string]$Color = "")
    $line = "[{0}] {1}" -f (Get-Date).ToString("HH:mm:ss"), $Message
    if ([string]::IsNullOrWhiteSpace($Color)) {
        Write-Host $line
    } else {
        Write-Host $line -ForegroundColor $Color
    }
    try { Add-Content -LiteralPath $logPath -Value $line -Encoding UTF8 } catch { }
}

function Write-Section {
    param([string]$Message)
    Write-Log ""
    Write-Log ("== " + $Message) "Cyan"
}

function Write-Evidence {
    param([string]$Name, [string]$Content)
    if (-not (Test-Path -LiteralPath $tmpDir)) {
        $null = New-Item -ItemType Directory -Path $tmpDir -Force
    }
    $path = Join-Path $tmpDir $Name
    Set-Content -LiteralPath $path -Value $Content -Encoding UTF8
    return $path
}

function Add-Check {
    param([bool]$Ok, [string]$Label, [string]$Detail = "")
    $status = "FAIL"
    if ($Ok) {
        $script:passCount++
        $status = "PASS"
    } else {
        $script:failCount++
    }
    $line = "[{0}] {1}{2}" -f $status, $Label, $(if ([string]::IsNullOrWhiteSpace($Detail)) { "" } else { " :: " + $Detail })
    Write-Host $line -ForegroundColor $(if ($Ok) { "Green" } else { "Red" })
    Add-Content -LiteralPath $scenarioPath -Value $line -Encoding UTF8
    return $Ok
}

function Add-Skip {
    param([string]$Label, [string]$Reason)
    $script:skipCount++
    $line = "[SKIP] {0} :: {1}" -f $Label, $Reason
    Write-Host $line -ForegroundColor Yellow
    Add-Content -LiteralPath $scenarioPath -Value $line -Encoding UTF8
}

function Invoke-Scenario {
    param([string]$Name, [scriptblock]$Body)
    Write-Section $Name
    try {
        & $Body
    } catch {
        Add-Check $false ($Name + " aborted") $_.Exception.Message
        Write-Log ($_.ScriptStackTrace) "DarkYellow"
    }
}

# ---------------------------------------------------------------------------
# env / toolchain
# ---------------------------------------------------------------------------

function Read-DotEnv {
    param([string]$Path)
    $map = @{}
    if (-not (Test-Path -LiteralPath $Path)) { return $map }
    foreach ($line in Get-Content -LiteralPath $Path) {
        $trim = $line.Trim()
        if ($trim -eq "" -or $trim.StartsWith("#") -or -not $trim.Contains("=")) { continue }
        $parts = $trim.Split("=", 2)
        $key = $parts[0].Trim()
        $value = $parts[1].Trim()
        if ($value.Length -ge 2) {
            $first = $value.Substring(0, 1)
            $last = $value.Substring($value.Length - 1, 1)
            if (($first -eq '"' -and $last -eq '"') -or ($first -eq "'" -and $last -eq "'")) {
                $value = $value.Substring(1, $value.Length - 2)
            }
        }
        $map[$key] = $value
    }
    return $map
}

function Get-EnvValue {
    param([string]$Name)
    $value = [Environment]::GetEnvironmentVariable($Name)
    if (-not [string]::IsNullOrWhiteSpace($value)) { return $value }
    $dotenv = Read-DotEnv -Path $envPath
    if ($dotenv.ContainsKey($Name)) { return [string]$dotenv[$Name] }
    return ""
}

function Resolve-GoExe {
    $local = Join-Path $repoRoot ".tools\go\bin\go.exe"
    if (Test-Path -LiteralPath $local) { return $local }
    $command = Get-Command go.exe -ErrorAction SilentlyContinue
    if ($null -ne $command) { return $command.Source }
    return ""
}

function Resolve-PsqlExe {
    $local = Join-Path $repoRoot ".tools\pgsql\bin\psql.exe"
    if (Test-Path -LiteralPath $local) { return $local }
    $command = Get-Command psql.exe -ErrorAction SilentlyContinue
    if ($null -ne $command) { return $command.Source }
    return ""
}

function Resolve-NodeExe {
    $command = Get-Command node.exe -ErrorAction SilentlyContinue
    if ($null -ne $command) { return $command.Source }
    $command = Get-Command node -ErrorAction SilentlyContinue
    if ($null -ne $command) { return $command.Source }
    return ""
}

function Resolve-CurlExe {
    $command = Get-Command curl.exe -ErrorAction SilentlyContinue
    if ($null -ne $command) { return $command.Source }
    return ""
}

# ---------------------------------------------------------------------------
# database helpers
# ---------------------------------------------------------------------------

function Resolve-DatabaseConfig {
    $baseUrl = $DatabaseUrl
    if ([string]::IsNullOrWhiteSpace($baseUrl)) { $baseUrl = Get-EnvValue -Name "EZTEST_DATABASE_URL" }
    if ([string]::IsNullOrWhiteSpace($baseUrl)) {
        throw "DatabaseUrl/EZTEST_DATABASE_URL is required"
    }
    $pattern = '^(?<scheme>postgres(?:ql)?)://(?<user>[^:/@]+)(?::(?<password>[^@]*))?@(?<host>[^:/?]+)(?::(?<port>\d+))?/(?<database>[^?]+)(?<query>\?.*)?$'
    $match = [regex]::Match($baseUrl, $pattern)
    if (-not $match.Success) {
        throw "EZTEST_DATABASE_URL is not a supported postgres URL"
    }
    $script:dbUser = [uri]::UnescapeDataString($match.Groups["user"].Value)
    $script:dbPassword = [uri]::UnescapeDataString($match.Groups["password"].Value)
    $script:dbHost = $match.Groups["host"].Value
    if ($match.Groups["port"].Success) { $script:dbPort = $match.Groups["port"].Value } else { $script:dbPort = "5432" }
    $query = ""
    if ($match.Groups["query"].Success) { $query = $match.Groups["query"].Value }
    $userInfo = [uri]::EscapeDataString($script:dbUser)
    if ($script:dbPassword -ne "") { $userInfo += ":" + [uri]::EscapeDataString($script:dbPassword) }
    $script:effectiveDatabaseUrl = "{0}://{1}@{2}:{3}/{4}{5}" -f `
        $match.Groups["scheme"].Value, $userInfo, $script:dbHost, $script:dbPort, $script:dbName, $query
    $script:previousEnv["PGPASSWORD"] = [Environment]::GetEnvironmentVariable("PGPASSWORD")
    $script:previousEnv["PGCLIENTENCODING"] = [Environment]::GetEnvironmentVariable("PGCLIENTENCODING")
    $env:PGPASSWORD = $script:dbPassword
    $env:PGCLIENTENCODING = "UTF8"
    return $script:effectiveDatabaseUrl
}

function Invoke-Psql {
    param(
        [Parameter(Mandatory = $true)][string]$Sql,
        [switch]$AllowFailure
    )
    if ([string]::IsNullOrWhiteSpace($script:psqlExe)) {
        throw "psql executable not found (.tools\pgsql\bin\psql.exe or PATH)"
    }
    $sqlPath = Join-Path $tmpDir ("sql-" + [guid]::NewGuid().ToString("N") + ".sql")
    Set-Content -LiteralPath $sqlPath -Value $Sql -Encoding UTF8
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    $exitCode = 0
    $output = @()
    try {
        $output = & $script:psqlExe -h $script:dbHost -p $script:dbPort -U $script:dbUser -d $script:dbName `
            -tA -F "|" -v ON_ERROR_STOP=1 -P pager=off -f $sqlPath 2>&1
        $exitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
        Remove-Item -LiteralPath $sqlPath -Force -ErrorAction SilentlyContinue
    }
    if ($exitCode -ne 0) {
        if ($AllowFailure) { return $null }
        throw ("psql failed: " + (($output | Out-String).Trim()))
    }
    return @($output | ForEach-Object { [string]$_ } | Where-Object { $_ -ne "" })
}

function Invoke-PsqlRows {
    param([Parameter(Mandatory = $true)][string]$Sql)
    $lines = @(Invoke-Psql -Sql $Sql)
    $rows = @()
    foreach ($line in $lines) {
        $text = [string]$line
        if ([string]::IsNullOrWhiteSpace($text)) { continue }
        $rows += , ($text.Trim().Split("|"))
    }
    return , $rows
}

function Get-Scalar {
    param([string]$Sql)
    $rows = @(Invoke-Psql -Sql $Sql)
    if ($rows.Count -eq 0) { return "" }
    return ([string]$rows[0]).Trim()
}

function SqlText {
    param([string]$Value)
    return $Value.Replace("'", "''")
}

function Wait-SqlValue {
    param([string]$Sql, [string]$Want, [int]$TimeoutSeconds = 120, [int]$IntervalMs = 1000)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $last = ""
    while ((Get-Date) -lt $deadline) {
        $last = Get-Scalar $Sql
        if ($last -eq $Want) { return $true }
        Start-Sleep -Milliseconds $IntervalMs
    }
    $script:lastWaitValue = $last
    return $false
}

function Wait-SqlNonEmpty {
    param([string]$Sql, [int]$TimeoutSeconds = 120, [int]$IntervalMs = 1000)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $last = ""
    while ((Get-Date) -lt $deadline) {
        $last = Get-Scalar $Sql
        if (-not [string]::IsNullOrWhiteSpace($last) -and $last -ne "0") { return $last }
        Start-Sleep -Milliseconds $IntervalMs
    }
    $script:lastWaitValue = $last
    return ""
}

function Get-ExpectedMigrationCount {
    $orderPath = Join-Path $backendDir "internal\db\migrations\order.txt"
    if (-not (Test-Path -LiteralPath $orderPath)) {
        throw "migrations order.txt is missing: $orderPath"
    }
    $count = 0
    foreach ($line in Get-Content -LiteralPath $orderPath) {
        $trim = $line.Trim()
        if ($trim -eq "" -or $trim.StartsWith("#")) { continue }
        $count++
    }
    return $count
}

function Get-DataRootFromDatabase {
    $value = Get-Scalar "SELECT value_json #>> '{}' FROM system_setting WHERE key = 'paths.data_root';"
    return $value.Trim()
}

function Set-SettingDbJson {
    param([string]$Key, [string]$JsonLiteral)
    $escapedKey = SqlText $Key
    $exists = Get-Scalar ("SELECT count(*) FROM system_setting WHERE key = '" + $escapedKey + "';")
    if ([string]::IsNullOrWhiteSpace($exists) -or [int]$exists -eq 0) {
        Write-Log ("setting seed skipped (key missing): " + $Key) "Yellow"
        return $false
    }
    $sql = "UPDATE system_setting SET value_json = " + $JsonLiteral + "::jsonb, updated_at = now() WHERE key = '" + $escapedKey + "';"
    $null = Invoke-Psql -Sql $sql
    return $true
}

function Set-SettingDbString {
    param([string]$Key, [string]$Value)
    return Set-SettingDbJson -Key $Key -JsonLiteral ("to_jsonb('" + (SqlText $Value) + "'::text)")
}

function Set-SettingDbNumber {
    param([string]$Key, [int]$Value)
    return Set-SettingDbJson -Key $Key -JsonLiteral ([string]$Value)
}

function Initialize-Database {
    Write-Section "Database: shared schema and setting seeds"
    $script:expectedMigrations = Get-ExpectedMigrationCount
    $probe = Invoke-Psql -Sql "SELECT 1;" -AllowFailure
    if ($null -eq $probe) { throw ("database " + $script:dbName + " is not reachable") }
    $applied = [int](Get-Scalar "SELECT count(*) FROM schema_migrations;")
    if ($applied -ne $script:expectedMigrations) {
        Write-Log ("schema_migrations=" + $applied + ", expected " + $script:expectedMigrations + " (migrations run on API startup)") "Yellow"
    }
    $null = Set-SettingDbNumber -Key "instance.heartbeat_seconds" -Value 1
    $null = Set-SettingDbNumber -Key "instance.offline_seconds" -Value 5
    $null = Set-SettingDbNumber -Key "execution.executor_heartbeat_seconds" -Value 2
    $null = Set-SettingDbNumber -Key "execution.executor_offline_seconds" -Value 5
    $null = Set-SettingDbString -Key "execution.executor_url" -Value ("http://127.0.0.1:" + $ExecutorPortA)
    $null = Set-SettingDbString -Key "execution.executor_urls" -Value `
        ("http://127.0.0.1:" + $ExecutorPortA + ",http://127.0.0.1:" + $ExecutorPortB)
    $null = Set-SettingDbNumber -Key "runtime.snowflake_node_id" -Value 1
    $null = Set-SettingDbNumber -Key "security.rate_limit_per_minute" -Value 0
    foreach ($required in @(
            @("instance.heartbeat_seconds", "1"),
            @("instance.offline_seconds", "5"),
            @("execution.executor_heartbeat_seconds", "2"),
            @("execution.executor_offline_seconds", "5"))) {
        $actual = Get-Scalar ("SELECT value_json #>> '{}' FROM system_setting WHERE key = '" + (SqlText $required[0]) + "';")
        if ($actual -ne $required[1]) {
            throw ("setting " + $required[0] + " = " + $actual + " after the seed, expected " + $required[1] +
                "; a deterministic drill requires the P8 settings catalog")
        }
    }
    $script:executorToken = Get-Scalar "SELECT value_json #>> '{}' FROM system_setting WHERE key = 'execution.executor_token';"
    if ([string]::IsNullOrWhiteSpace($script:executorToken)) {
        throw "execution.executor_token is missing from system_setting"
    }
    Write-Log "schema/settings/runtime seeds prepared (no secret values printed)"
}

function Prepare-DataRoot {
    $configured = Get-DataRootFromDatabase
    if ([string]::IsNullOrWhiteSpace($configured)) {
        $configured = Join-Path $tmpDir "data"
        $full = [System.IO.Path]::GetFullPath($configured).Replace("\", "/")
        $null = Set-SettingDbString -Key "paths.data_root" -Value $full
        $null = Set-SettingDbString -Key "paths.attachment_dir" -Value ($full + "/attachments")
        $null = Set-SettingDbString -Key "paths.evidence_dir" -Value ($full + "/evidence")
        $null = Set-SettingDbString -Key "paths.report_dir" -Value ($full + "/reports")
        $null = Set-SettingDbString -Key "paths.export_dir" -Value ($full + "/exports")
        Write-Log "paths.* seeded to the shared .tmp/p8-multi/data root"
    } elseif (-not [System.IO.Path]::IsPathRooted($configured)) {
        $configured = Join-Path $repoRoot $configured
    }
    $full = [System.IO.Path]::GetFullPath($configured)
    $script:dataRoot = $full
    foreach ($child in @("attachments", "evidence", "reports", "exports", "objects")) {
        $dir = Join-Path $full $child
        if (-not (Test-Path -LiteralPath $dir)) {
            $null = New-Item -ItemType Directory -Path $dir -Force
        }
    }
    Write-Log ("shared content root: " + $full)
}

function Assert-NoLiveForeignReplica {
    Write-Section "Replica safety checks"
    $ports = [string]$ApiPort1 + "," + [string]$ApiPort2
    $liveRows = Invoke-PsqlRows -Sql (
        "SELECT id::text, api_port::text, status, extract(epoch from (now() - heartbeat_at))::int::text " +
        "FROM api_instance WHERE status IN ('active','draining') " +
        "AND api_port NOT IN (" + $ports + ") " +
        "AND heartbeat_at > now() - interval '30 seconds' ORDER BY id;")
    if ($liveRows.Count -gt 0) {
        $detail = @()
        foreach ($row in $liveRows) {
            $detail += ("id=" + $row[0] + " port=" + $row[1] + " status=" + $row[2] + " heartbeat_age_s=" + $row[3])
        }
        throw ("another live API replica is registered in " + $script:dbName + " (" + ($detail -join "; ") +
            "); stop it before running p8-multi.ps1 so node ids 1/2 and hot settings are deterministic")
    }
    $null = Invoke-Psql -Sql (
        "UPDATE api_instance SET status = 'offline', updated_at = now() " +
        "WHERE status IN ('active','draining') AND api_port NOT IN (" + $ports + ") " +
        "AND heartbeat_at <= now() - interval '30 seconds';")
    foreach ($port in @($ApiPort1, $ApiPort2)) {
        Stop-PortProcesses -Port $port
        $null = Wait-PortClosed -Port $port -TimeoutSeconds 10
    }
    $null = Invoke-Psql -Sql (
        "UPDATE api_instance SET status = 'offline', updated_at = now() " +
        "WHERE api_port IN (" + $ports + ") AND status IN ('active','draining');")
    Write-Log "no live foreign API replica; stale rows for the drill ports marked offline"
}

# ---------------------------------------------------------------------------
# HTTP helpers
# ---------------------------------------------------------------------------

function Invoke-ApiOnPort {
    param(
        [int]$Port,
        [string]$Method,
        [string]$Path,
        $Body = $null,
        [string]$Token = "",
        [int]$TimeoutSec = 60
    )
    if ([string]::IsNullOrEmpty($Token) -and -not $PSBoundParameters.ContainsKey("Token")) { $Token = $script:token }
    $headers = @{}
    if (-not [string]::IsNullOrWhiteSpace($Token)) { $headers["Authorization"] = "Bearer $Token" }
    $uri = "http://127.0.0.1:" + $Port + $Path
    $params = @{
        Uri             = $uri
        Method          = $Method
        Headers         = $headers
        UseBasicParsing = $true
        TimeoutSec      = $TimeoutSec
    }
    if ($null -ne $Body) {
        $json = if ($Body -is [string]) { $Body } else { $Body | ConvertTo-Json -Depth 20 -Compress }
        $params["Body"] = [System.Text.Encoding]::UTF8.GetBytes($json)
        $params["ContentType"] = "application/json; charset=utf-8"
    }
    try {
        $response = Invoke-WebRequest @params
        $parsed = $null
        if (-not [string]::IsNullOrWhiteSpace($response.Content)) {
            try { $parsed = $response.Content | ConvertFrom-Json } catch { $parsed = $null }
        }
        return [pscustomobject]@{
            Status  = [int]$response.StatusCode
            Json    = $parsed
            Text    = [string]$response.Content
            Headers = $response.Headers
        }
    } catch {
        $status = 0
        $text = $_.Exception.Message
        if ($null -ne $_.ErrorDetails -and -not [string]::IsNullOrWhiteSpace($_.ErrorDetails.Message)) {
            $text = $_.ErrorDetails.Message
        }
        if ($null -ne $_.Exception.Response) {
            try { $status = [int]$_.Exception.Response.StatusCode } catch { $status = 0 }
            if ([string]::IsNullOrWhiteSpace($text)) {
                try {
                    $reader = New-Object System.IO.StreamReader($_.Exception.Response.GetResponseStream())
                    $text = $reader.ReadToEnd()
                } catch { }
            }
        }
        $parsed = $null
        try { $parsed = $text | ConvertFrom-Json } catch { $parsed = $null }
        return [pscustomobject]@{
            Status  = $status
            Json    = $parsed
            Text    = $text
            Headers = @{}
        }
    }
}

function Invoke-ApiPostEmptyOnPort {
    param([int]$Port, [string]$Path, [string]$Token = "")
    if ([string]::IsNullOrEmpty($Token) -and -not $PSBoundParameters.ContainsKey("Token")) { $Token = $script:token }
    $headers = @{}
    if (-not [string]::IsNullOrWhiteSpace($Token)) { $headers["Authorization"] = "Bearer $Token" }
    return Invoke-ApiOnPort -Port $Port -Method "POST" -Path $Path -Token $Token -TimeoutSec 30
}

function Get-HttpStatus {
    param([int]$Port, [string]$Path)
    try {
        $response = Invoke-WebRequest -Uri ("http://127.0.0.1:" + $Port + $Path) -UseBasicParsing -Method Get -TimeoutSec 4
        return [int]$response.StatusCode
    } catch {
        if ($null -ne $_.Exception.Response) {
            try { return [int]$_.Exception.Response.StatusCode } catch { return 0 }
        }
        return 0
    }
}

function Wait-HttpStatus {
    param(
        [int]$Port,
        [string]$Path,
        [int]$Expected,
        [int]$TimeoutSeconds = 60,
        [string]$Label = ""
    )
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $last = 0
    while ((Get-Date) -lt $deadline) {
        $last = Get-HttpStatus -Port $Port -Path $Path
        if ($last -eq $Expected) { return $true }
        Start-Sleep -Milliseconds 400
    }
    if ([string]::IsNullOrWhiteSpace($Label)) { $Label = "port $Port $Path" }
    Write-Log ("timed out waiting for " + $Label + " (expected " + $Expected + ", last " + $last + ")") "Yellow"
    return $false
}

function Login-Admin {
    param([int]$Port = 0)
    if ($Port -le 0) { $Port = $ApiPort1 }
    $body = @{ username = $AdminUsername; password = $AdminPassword }
    $response = Invoke-ApiOnPort -Port $Port -Method "POST" -Path "/api/auth/login" -Body $body -Token "" -TimeoutSec 30
    if ($response.Status -ne 200 -or $null -eq $response.Json -or $null -eq $response.Json.data) {
        return ""
    }
    return [string]$response.Json.data.token
}

function Set-SettingApi {
    param([string]$Key, $Value, [int]$Port = 0)
    if ($Port -le 0) { $Port = $ApiPort1 }
    $path = "/api/admin/settings/" + [uri]::EscapeDataString($Key)
    $response = Invoke-ApiOnPort -Port $Port -Method "PATCH" -Path $path -Body @{ value = $Value }
    if ($response.Status -ne 200) {
        throw ("PATCH setting " + $Key + " failed with status " + $response.Status)
    }
    return $response.Json.data
}

function Get-SettingApi {
    param([string]$Key, [int]$Port = 0)
    if ($Port -le 0) { $Port = $ApiPort1 }
    $path = "/api/admin/settings/" + [uri]::EscapeDataString($Key)
    $response = Invoke-ApiOnPort -Port $Port -Method "GET" -Path $path
    if ($response.Status -ne 200) { return $null }
    return $response.Json.data
}

function Get-SettingValueApi {
    param([string]$Key, [int]$Port = 0)
    $setting = Get-SettingApi -Key $Key -Port $Port
    if ($null -eq $setting) { return $null }
    return $setting.value
}

function Get-InstanceItems {
    param([int]$Port)
    $response = Invoke-ApiOnPort -Port $Port -Method "GET" -Path "/api/admin/instances"
    if ($response.Status -ne 200) {
        throw ("GET /api/admin/instances via " + $Port + " returned " + $response.Status)
    }
    return @($response.Json.data)
}

function Find-InstanceByPort {
    param($Items, [int]$Port)
    foreach ($item in @($Items)) {
        if ($null -ne $item.api_port -and [int]$item.api_port -eq $Port) { return $item }
    }
    return $null
}

function Get-ExecutorItems {
    param([int]$Port)
    $response = Invoke-ApiOnPort -Port $Port -Method "GET" -Path "/api/admin/executors"
    if ($response.Status -ne 200) {
        throw ("GET /api/admin/executors via " + $Port + " returned " + $response.Status)
    }
    return @($response.Json.data)
}

function Find-ExecutorByEndpoint {
    param($Items, [string]$Endpoint)
    $wanted = $Endpoint.TrimEnd("/")
    foreach ($item in @($Items)) {
        if ([string]$item.endpoint -eq $wanted) { return $item }
        if (([string]$item.endpoint).TrimEnd("/") -eq $wanted) { return $item }
    }
    return $null
}

# ---------------------------------------------------------------------------
# process / port helpers
# ---------------------------------------------------------------------------

function Stop-OwnedProcess {
    param($Process)
    if ($null -eq $Process) { return }
    try {
        if (-not $Process.HasExited) {
            & taskkill.exe /PID $Process.Id /T /F 2>&1 | Out-Null
        }
    } catch { }
}

function Test-PortListening {
    param([int]$Port)
    try {
        $connections = Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue
        return ($null -ne $connections -and @($connections).Count -gt 0)
    } catch {
        return $false
    }
}

function Stop-PortProcesses {
    param([int]$Port)
    try {
        $connections = Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue
        foreach ($connection in @($connections)) {
            Stop-Process -Id $connection.OwningProcess -Force -ErrorAction SilentlyContinue
        }
    } catch { }
}

function Wait-PortClosed {
    param([int]$Port, [int]$TimeoutSeconds = 15)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        if (-not (Test-PortListening -Port $Port)) { return $true }
        Start-Sleep -Milliseconds 300
    }
    return -not (Test-PortListening -Port $Port)
}

# ---------------------------------------------------------------------------
# API / executor / fixture processes
# ---------------------------------------------------------------------------

function Build-ApiBinary {
    Write-Section "Build API binary"
    $binary = Join-Path $tmpDir "eztest-p8.exe"
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    $output = @()
    $code = 1
    $pushed = $false
    try {
        Push-Location $backendDir
        $pushed = $true
        $output = & $script:goExe build -o $binary ./cmd/eztest 2>&1
        $code = $LASTEXITCODE
    } finally {
        if ($pushed) { Pop-Location }
        $ErrorActionPreference = $previous
    }
    if ($code -ne 0) {
        throw ("go build failed with exit code " + $code + ": " + (($output | Out-String).Trim()))
    }
    if (-not (Test-Path -LiteralPath $binary)) { throw "go build produced no binary" }
    $script:apiBinary = $binary
    Write-Log ("built " + $binary)
}

function Set-ApiEnvironment {
    param([int]$Port)
    $env:EZTEST_DATABASE_URL = $script:effectiveDatabaseUrl
    $env:EZTEST_API_PORT = [string]$Port
    $env:EZTEST_WEB_PORT = "6868"
    $env:EZTEST_EXECUTOR_PORT = "13336"
}

function Start-ApiAProcess {
    $script:apiAStarts++
    $name = "api-a-" + $script:apiAStarts
    $log = Join-Path $tmpDir ($name + ".log")
    $err = Join-Path $tmpDir ($name + ".err.log")
    Set-ApiEnvironment -Port $ApiPort1
    $process = Start-Process -FilePath $script:apiBinary -WorkingDirectory $repoRoot `
        -PassThru -NoNewWindow -RedirectStandardOutput $log -RedirectStandardError $err
    $script:apiA = [pscustomobject]@{ Kind = "api-a"; Port = $ApiPort1; Process = $process; Log = $log; ErrorLog = $err }
    $script:processes += $script:apiA
    if (-not (Wait-HttpStatus -Port $ApiPort1 -Path "/healthz" -Expected 200 -TimeoutSeconds 120 -Label "api A /healthz")) {
        Show-LogTail -Path $err
        Show-LogTail -Path $log
        throw "api A did not become live on port $ApiPort1"
    }
    if (-not (Wait-HttpStatus -Port $ApiPort1 -Path "/api/health/ready" -Expected 200 -TimeoutSeconds 60 -Label "api A readiness")) {
        throw "api A readiness is not 200"
    }
    Write-Log ("api A ready on " + $ApiPort1 + " (pid " + $process.Id + ")")
}

function Start-ApiBProcess {
    $script:apiBStarts++
    $name = "api-b-" + $script:apiBStarts
    $log = Join-Path $tmpDir ($name + ".log")
    $err = Join-Path $tmpDir ($name + ".err.log")
    Set-ApiEnvironment -Port $ApiPort2
    $process = Start-Process -FilePath $script:apiBinary -WorkingDirectory $repoRoot `
        -PassThru -NoNewWindow -RedirectStandardOutput $log -RedirectStandardError $err
    $script:apiB = [pscustomobject]@{ Kind = "api-b"; Port = $ApiPort2; Process = $process; Log = $log; ErrorLog = $err }
    $script:processes += $script:apiB
    if (-not (Wait-HttpStatus -Port $ApiPort2 -Path "/healthz" -Expected 200 -TimeoutSeconds 120 -Label "api B /healthz")) {
        Show-LogTail -Path $err
        Show-LogTail -Path $log
        throw "api B did not become live on port $ApiPort2"
    }
    if (-not (Wait-HttpStatus -Port $ApiPort2 -Path "/api/health/ready" -Expected 200 -TimeoutSeconds 60 -Label "api B readiness")) {
        throw "api B readiness is not 200"
    }
    Write-Log ("api B ready on " + $ApiPort2 + " (pid " + $process.Id + ")")
}

function Show-LogTail {
    param([string]$Path, [int]$Lines = 30)
    if ([string]::IsNullOrWhiteSpace($Path) -or -not (Test-Path -LiteralPath $Path)) { return }
    Write-Log ("--- " + $Path + " (tail) ---") "DarkYellow"
    Get-Content -LiteralPath $Path -Tail $Lines | ForEach-Object { Write-Log ("    " + $_) }
}

function Start-ExecutorProcess {
    param([int]$Port, [string]$Name, [string]$LogName)
    $log = Join-Path $tmpDir ($LogName + ".log")
    $err = Join-Path $tmpDir ($LogName + ".err.log")
    $env:EZTEST_EXECUTOR_TOKEN = $script:executorToken
    $env:EZTEST_EXECUTOR_ADDR = "127.0.0.1:" + $Port
    $env:EZTEST_EXECUTOR_MAX_CONTEXTS = "8"
    $env:EZTEST_EXECUTOR_HEARTBEAT_SECONDS = "2"
    $env:EZTEST_API_URL = "http://127.0.0.1:" + $ApiPort1
    $process = Start-Process -FilePath $script:nodeExe `
        -ArgumentList @((Join-Path $executorDir "src\server.mjs")) `
        -WorkingDirectory $executorDir -PassThru -NoNewWindow `
        -RedirectStandardOutput $log -RedirectStandardError $err
    $entry = [pscustomobject]@{ Kind = $Name; Port = $Port; Process = $process; Log = $log; ErrorLog = $err }
    $script:processes += $entry
    if (-not (Wait-HttpStatus -Port $Port -Path "/healthz" -Expected 200 -TimeoutSeconds 90 -Label ($Name + " /healthz"))) {
        Show-LogTail -Path $err
        Show-LogTail -Path $log
        throw ($Name + " did not become healthy on port " + $Port)
    }
    Write-Log ($Name + " healthy on " + $Port + " (pid " + $process.Id + ")")
    return $entry
}

function Ensure-ExecutorRoster {
    Write-Section "Executor roster (multi-endpoint)"
    $deadline = (Get-Date).AddSeconds(60)
    $items = @()
    while ((Get-Date) -lt $deadline) {
        $items = @(Get-ExecutorItems -Port $ApiPort1)
        $a = Find-ExecutorByEndpoint -Items $items -Endpoint ("http://127.0.0.1:" + $ExecutorPortA)
        $b = Find-ExecutorByEndpoint -Items $items -Endpoint ("http://127.0.0.1:" + $ExecutorPortB)
        if ($null -ne $a -and $null -ne $b) { break }
        Start-Sleep -Milliseconds 500
    }
    $a = Find-ExecutorByEndpoint -Items $items -Endpoint ("http://127.0.0.1:" + $ExecutorPortA)
    $b = Find-ExecutorByEndpoint -Items $items -Endpoint ("http://127.0.0.1:" + $ExecutorPortB)
    if ($null -eq $a -or $null -eq $b) {
        throw "both drill executors did not register in executor_instance"
    }
    $script:executorAID = [string]$a.id
    $script:executorBID = [string]$b.id
    # Stale rows from earlier runs are drained so registry selection cannot pick
    # an endpoint that is not part of this drill (draining also survives polling).
    foreach ($item in $items) {
        $endpoint = ([string]$item.endpoint).TrimEnd("/")
        $mine = ($endpoint -eq ("http://127.0.0.1:" + $ExecutorPortA)) -or ($endpoint -eq ("http://127.0.0.1:" + $ExecutorPortB))
        if (-not $mine -and ($item.status -eq "active" -or $item.status -eq "draining")) {
            $null = Invoke-ApiPostEmptyOnPort -Port $ApiPort1 -Path ("/api/admin/executors/" + $item.id + "/drain")
            Write-Log ("drained foreign executor " + $item.id + " (" + $item.endpoint + ") so only the drill executors are selectable") "Yellow"
        }
    }
    foreach ($id in @($script:executorAID, $script:executorBID)) {
        $null = Invoke-ApiPostEmptyOnPort -Port $ApiPort1 -Path ("/api/admin/executors/" + $id + "/activate")
    }
    $active = Wait-SqlValue -Sql ("SELECT count(*)::text FROM executor_instance WHERE status = 'active' AND endpoint IN ('http://127.0.0.1:" +
        $ExecutorPortA + "', 'http://127.0.0.1:" + $ExecutorPortB + "');") -Want "2" -TimeoutSeconds 30
    if (-not $active) {
        throw "the two drill executors are not both active"
    }
    Write-Log ("executor A id=" + $script:executorAID + ", executor B id=" + $script:executorBID + " (both active)")
}

function Start-FixtureTarget {
    param([int]$Port, [string]$Variant, [string]$Name)
    $log = Join-Path $tmpDir ("fixture-" + $Port + ".log")
    $err = Join-Path $tmpDir ("fixture-" + $Port + ".err.log")
    $previousPort = $env:FIXTURE_PORT
    $previousVariant = $env:FIXTURE_VARIANT
    try {
        $env:FIXTURE_PORT = [string]$Port
        $env:FIXTURE_VARIANT = $Variant
        $process = Start-Process -FilePath $script:nodeExe `
            -ArgumentList @((Join-Path $repoRoot "scripts\fixture-target.cjs")) `
            -WorkingDirectory $repoRoot -PassThru -NoNewWindow `
            -RedirectStandardOutput $log -RedirectStandardError $err
    } finally {
        $env:FIXTURE_PORT = $previousPort
        $env:FIXTURE_VARIANT = $previousVariant
    }
    $entry = [pscustomobject]@{ Kind = ("fixture-" + $Name); Port = $Port; Process = $process; Log = $log; ErrorLog = $err }
    $script:fixtures += $entry
    if (-not (Wait-HttpStatus -Port $Port -Path "/health" -Expected 200 -TimeoutSeconds 30 -Label ($Name + " fixture /health"))) {
        Show-LogTail -Path $err
        throw ("fixture target did not start on " + $Port)
    }
    Write-Log ("fixture target " + $Name + " healthy on " + $Port + " (" + $Variant + ")")
    return $entry
}

function Ensure-VersionFixtures {
    param([int64]$VersionID)
    $rows = Invoke-PsqlRows -Sql (
        "SELECT DISTINCT e.base_url, coalesce(s.family, ''), s.name " +
        "FROM version_sut vs JOIN sut_environment e ON e.id = vs.environment_id JOIN sut s ON s.id = vs.sut_id " +
        "WHERE vs.version_id = " + $VersionID + " AND e.status = 'active';")
    foreach ($row in $rows) {
        if ($row.Count -lt 3) { continue }
        $baseUrl = [string]$row[0]
        $family = [string]$row[1]
        $name = [string]$row[2]
        $uri = $null
        if (-not [System.Uri]::TryCreate($baseUrl, [System.UriKind]::Absolute, [ref]$uri)) { continue }
        if ($uri.Host -ne "127.0.0.1" -and $uri.Host -ne "localhost") { continue }
        $port = $uri.Port
        if ($port -le 0 -or $port -gt 65535) { continue }
        if ($port -eq $ApiPort1 -or $port -eq $ApiPort2 -or $port -eq $ExecutorPortA -or $port -eq $ExecutorPortB) { continue }
        if (Test-PortListening -Port $port) {
            Write-Log ("reusing existing fixture/listener on " + $port + " for " + $name)
            continue
        }
        $variant = "orders"
        if (($family + " " + $name) -match "payment") { $variant = "payments" }
        $fixtureName = ("sut-" + $port)
        $null = Start-FixtureTarget -Port $port -Variant $variant -Name $fixtureName
    }
}

# ---------------------------------------------------------------------------
# SSE / slow in-flight request
# ---------------------------------------------------------------------------

function Start-SseProcess {
    param([int]$Port, [string]$Channel, [string]$OutFile, [string]$LastEventId = "")
    if (Test-Path -LiteralPath $OutFile) {
        Remove-Item -LiteralPath $OutFile -Force -ErrorAction SilentlyContinue
    }
    $url = "http://127.0.0.1:" + $Port + "/api/stream?channel=" + [uri]::EscapeDataString($Channel)
    $argString = "-s -N --max-time 30 -H `"Authorization: Bearer $script:token`" -H `"Accept: text/event-stream`""
    if (-not [string]::IsNullOrWhiteSpace($LastEventId)) {
        $argString += " -H `"Last-Event-ID: " + $LastEventId + "`""
    }
    $argString += " " + $url
    $errFile = $OutFile + ".err"
    $process = Start-Process -FilePath $script:curlExe -ArgumentList $argString -PassThru -NoNewWindow `
        -RedirectStandardOutput $OutFile -RedirectStandardError $errFile
    $script:sseProcesses += $process
    return $process
}

function Wait-FilePattern {
    param([string]$Path, [string]$Pattern, [int]$TimeoutSeconds = 15)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $text = ""
    while ((Get-Date) -lt $deadline) {
        if (Test-Path -LiteralPath $Path) {
            try { $text = [string](Get-Content -LiteralPath $Path -Raw) } catch { $text = "" }
            if (-not [string]::IsNullOrWhiteSpace($text) -and $text -match $Pattern) { return $text }
        }
        Start-Sleep -Milliseconds 250
    }
    return ""
}

function Start-SlowHttpHold {
    param([int]$Port, [int]$HoldSeconds = 180)
    $job = Start-Job -Name ("p8-slow-http-" + $Port) -ScriptBlock {
        param([int]$TargetPort, [int]$Hold)
        $client = $null
        try {
            $client = New-Object System.Net.Sockets.TcpClient
            $client.Connect("127.0.0.1", $TargetPort)
            $stream = $client.GetStream()
            $header = "POST /api/auth/login HTTP/1.1`r`nHost: 127.0.0.1:" + $TargetPort +
                "`r`nContent-Type: application/json`r`nContent-Length: 65536`r`nConnection: close`r`n`r`n"
            $payload = $header + '{"username":"'
            $bytes = [System.Text.Encoding]::ASCII.GetBytes($payload)
            $stream.Write($bytes, 0, $bytes.Length)
            $stream.Flush()
            # One-second slices so Stop-Job can abort the hold promptly.
            for ($i = 0; $i -lt $Hold; $i++) { Start-Sleep -Seconds 1 }
        } finally {
            if ($null -ne $client) {
                try { $client.Close() } catch { }
            }
        }
    } -ArgumentList $Port, $HoldSeconds
    $script:slowHoldJob = $job
    Write-Log ("slow in-flight HTTP request started against api A port " + $Port)
    return $job
}

function Stop-SlowHttpHold {
    if ($null -eq $script:slowHoldJob) { return }
    try { Stop-Job -Job $script:slowHoldJob -ErrorAction SilentlyContinue } catch { }
    try { Remove-Job -Job $script:slowHoldJob -Force -ErrorAction SilentlyContinue } catch { }
    $script:slowHoldJob = $null
    Write-Log "slow in-flight HTTP request closed"
}

# ---------------------------------------------------------------------------
# cluster / state probes
# ---------------------------------------------------------------------------

function Get-LeaderLockPids {
    $sql = "SELECT pid::text FROM pg_locks WHERE locktype = 'advisory' AND granted " +
        "AND database = (SELECT oid FROM pg_database WHERE datname = current_database()) " +
        "AND objsubid = 1 AND classid = 0 AND objid = " + $leaderLockKey + " ORDER BY pid;"
    $lines = Invoke-Psql -Sql $sql -AllowFailure
    $pids = @()
    if ($null -eq $lines) { return $pids }
    foreach ($line in $lines) {
        $text = ([string]$line).Trim()
        if ($text -match '^[0-9]+$') { $pids += [int]$text }
    }
    return $pids
}

function Get-CountersSnapshot {
    return (Get-Scalar (
        "SELECT (SELECT count(*) FROM work_item)::text || '|' || " +
        "(SELECT count(*) FROM agent_run)::text || '|' || " +
        "(SELECT count(*) FROM case_run)::text || '|' || " +
        "(SELECT count(*) FROM run_wave)::text || '|' || " +
        "(SELECT count(*) FROM schema_migrations)::text;"))
}

function Get-DbTimestamp {
    return (Get-Scalar "SELECT now()::text;")
}

# Counts only rows that existed at the captured timestamp, so unrelated
# background workers (steward/GC/analysis) cannot masquerade as duplicates.
function Get-CountsAsOf {
    param([string]$Cutoff)
    $escaped = SqlText $Cutoff
    return (Get-Scalar (
        "SELECT (SELECT count(*) FROM work_item WHERE created_at <= '" + $escaped + "'::timestamptz)::text || '|' || " +
        "(SELECT count(*) FROM agent_run WHERE started_at <= '" + $escaped + "'::timestamptz)::text || '|' || " +
        "(SELECT count(*) FROM case_run WHERE created_at <= '" + $escaped + "'::timestamptz)::text || '|' || " +
        "(SELECT count(*) FROM run_wave WHERE created_at <= '" + $escaped + "'::timestamptz)::text || '|' || " +
        "(SELECT count(*) FROM schema_migrations)::text;"))
}

function Get-DuplicateSnapshot {
    $workDupes = [int](Get-Scalar (
        "SELECT count(*) FROM (SELECT 1 FROM work_item GROUP BY version_id, type, dedupe_key " +
        "HAVING count(*) > 1) d;"))
    $runDupes = [int](Get-Scalar (
        "SELECT count(*) FROM (SELECT 1 FROM case_run GROUP BY wave_id, intent_id " +
        "HAVING count(*) > 1) d;"))
    $agentDupes = [int](Get-Scalar (
        "SELECT count(*) FROM (SELECT 1 FROM agent_run WHERE status = 'running' AND work_item_id IS NOT NULL " +
        "GROUP BY work_item_id HAVING count(*) > 1) d;"))
    return [pscustomobject]@{ Work = $workDupes; Case = $runDupes; Agent = $agentDupes }
}

function Assert-NoDuplicateRows {
    if ($null -eq $script:duplicateBaseline) {
        $script:duplicateBaseline = Get-DuplicateSnapshot
    }
    $current = Get-DuplicateSnapshot
    $baseline = $script:duplicateBaseline
    $baselineText = "baseline work=" + $baseline.Work + " case=" + $baseline.Case + " agent=" + $baseline.Agent
    $null = Add-Check ($current.Work -le $baseline.Work) "no new duplicate work_item dedupe rows" (
        "current=" + $current.Work + " " + $baselineText)
    $null = Add-Check ($current.Case -le $baseline.Case) "no new duplicate case_run per (wave,intent)" (
        "current=" + $current.Case + " " + $baselineText)
    $null = Add-Check ($current.Agent -le $baseline.Agent) "no new duplicate running agent_run per work_item" (
        "current=" + $current.Agent + " " + $baselineText)
}

function Test-MigrationsOnce {
    $count = [int](Get-Scalar "SELECT count(*) FROM schema_migrations;")
    $distinct = [int](Get-Scalar "SELECT count(DISTINCT version) FROM schema_migrations;")
    $null = Add-Check (($count -eq $script:expectedMigrations) -and ($distinct -eq $count)) `
        "migration ledger applied once" ("rows=" + $count + " expected=" + $script:expectedMigrations + " distinct=" + $distinct)
}

# ---------------------------------------------------------------------------
# case-run helpers (real API-created execution data only)
# ---------------------------------------------------------------------------

function Get-CaseCandidates {
    $sql = @'
SELECT s.version_id::text || '|' || cv.id::text || '|' || replace(cv.title, '|', '/') || '|' || v.status || '|' ||
       (CASE WHEN EXISTS (SELECT 1 FROM case_step st WHERE st.case_version_id = cv.id AND st.action ILIKE '%slow%') THEN '1' ELSE '0' END) || '|' ||
       (CASE WHEN EXISTS (SELECT 1 FROM case_step st WHERE st.case_version_id = cv.id AND st.action ILIKE '%human.request%') THEN '1' ELSE '0' END) || '|' ||
       (SELECT count(*) FROM case_step st WHERE st.case_version_id = cv.id)::text || '|' ||
       GREATEST(jsonb_array_length(cv.version_sut_ids),
                CASE WHEN cv.default_version_sut_id IS NOT NULL THEN 1 ELSE 0 END)::text
FROM case_entity ce
JOIN case_version cv ON cv.id = ce.current_version_id
JOIN case_scenario s ON s.id = ce.scenario_id
JOIN version v ON v.id = s.version_id
WHERE ce.status = 'confirmed'
  AND cv.status = 'confirmed'
  AND (cv.default_version_sut_id IS NOT NULL OR jsonb_array_length(cv.version_sut_ids) > 0)
  AND EXISTS (SELECT 1 FROM version_sut vs WHERE vs.version_id = s.version_id AND vs.status = 'active')
  AND NOT EXISTS (SELECT 1 FROM run_wave w WHERE w.version_id = s.version_id AND w.status IN ('planned','running','paused'))
  AND NOT EXISTS (SELECT 1 FROM execution_intent i WHERE i.version_id = s.version_id AND i.status IN ('open','claimed','running'))
ORDER BY cv.id DESC;
'@
    $rows = Invoke-PsqlRows -Sql $sql
    $items = @()
    foreach ($row in $rows) {
        if ($row.Count -lt 8) { continue }
        $items += [pscustomobject]@{
            VersionID      = [int64]$row[0]
            CaseVersionID  = [int64]$row[1]
            Title          = [string]$row[2]
            VersionStatus  = [string]$row[3]
            HasSlow        = ([string]$row[4] -eq "1")
            HasHuman       = ([string]$row[5] -eq "1")
            StepCount      = [int]$row[6]
            SUTCount       = [int]$row[7]
        }
    }
    return , $items
}

function Select-CaseCandidate {
    param(
        $Candidates,
        [int64]$ExcludeVersionID = 0,
        [switch]$PreferSlow,
        [switch]$PreferSingleSUT,
        [switch]$ForbidHuman
    )
    $pool = @($Candidates | Where-Object { [int64]$_.VersionID -ne $ExcludeVersionID -and [int]$_.StepCount -gt 0 })
    if ($pool.Count -eq 0) { return $null }
    # Prefer the newest version (the P8 S0-S6 data has the newest snowflake IDs)
    # so the drill exercises the current scenario data; older acceptance versions
    # remain a real (never synthetic) fallback.
    $newestVersion = [int64](($pool | Sort-Object -Property @{ Expression = { [int64]$_.VersionID }; Descending = $true } | Select-Object -First 1).VersionID)
    $pool = @($pool | Where-Object { [int64]$_.VersionID -eq $newestVersion })
    if ($PreferSingleSUT) {
        $single = @($pool | Where-Object { [int]$_.SUTCount -le 1 })
        if ($single.Count -gt 0) { $pool = $single }
    }
    if ($ForbidHuman) {
        $withoutHuman = @($pool | Where-Object { -not $_.HasHuman })
        if ($withoutHuman.Count -gt 0) { $pool = $withoutHuman }
    }
    if ($PreferSlow) {
        $slow = @($pool | Where-Object { $_.HasSlow })
        if ($slow.Count -gt 0) { $pool = $slow }
    }
    # Newest version first (the P8 S0-S6 data has the newest snowflake IDs);
    # reopening a completed version through a manual intent is supported.
    $ordered = @($pool | Sort-Object -Property `
        @{ Expression = { [int64]$_.VersionID }; Descending = $true }, `
        @{ Expression = { [int64]$_.CaseVersionID }; Descending = $true })
    return $ordered[0]
}

function Ensure-CaseRequirementOpen {
    param([int64]$VersionID, [int64]$CaseVersionID, [string]$Label)
    $text = Get-Scalar (
        "SELECT r.id::text || '|' || r.status FROM case_version cv " +
        "JOIN case_entity ce ON ce.id = cv.case_id " +
        "JOIN case_scenario s ON s.id = ce.scenario_id " +
        "JOIN requirement r ON r.id = s.requirement_id " +
        "WHERE cv.id = " + $CaseVersionID + " AND s.version_id = " + $VersionID + ";")
    if ([string]::IsNullOrWhiteSpace($text)) { return }
    $parts = $text.Split("|")
    if ($parts.Count -lt 2) { return }
    if ($parts[1] -ne "closed") { return }
    $requirementID = [int64]$parts[0]
    $path = "/api/versions/" + $VersionID + "/requirements/" + $requirementID + "/reopen"
    $response = Invoke-ApiOnPort -Port $ApiPort1 -Method "POST" -Path $path -Body @{
        reason = "P8 multi-replica drill: reopen the requirement to create a real case run for S8"
    } -TimeoutSec 30
    if ($response.Status -ne 200) {
        throw ($Label + ": reopen closed requirement " + $requirementID + " returned " + $response.Status + " :: " + $response.Text)
    }
    Write-Log ("reopened closed requirement " + $requirementID + " for " + $Label + " (S6 closure state is test data)")
}

function Start-IntentForCase {
    param([int64]$VersionID, [int64]$CaseVersionID, [string]$Label)
    Ensure-CaseRequirementOpen -VersionID $VersionID -CaseVersionID $CaseVersionID -Label $Label
    $path = "/api/versions/" + $VersionID + "/execution/intents"
    $response = Invoke-ApiOnPort -Port $ApiPort1 -Method "POST" -Path $path `
        -Body @{ case_version_id = [string]$CaseVersionID; purpose = "manual" } -TimeoutSec 30
    if ($response.Status -ne 201) {
        throw ($Label + ": POST execution intent returned " + $response.Status + " :: " + $response.Text)
    }
    $intentID = [int64]$response.Json.data.id
    $caseRunIDText = Wait-SqlNonEmpty -Sql (
        "SELECT id::text FROM case_run WHERE intent_id = " + $intentID + " ORDER BY id DESC LIMIT 1;") -TimeoutSeconds 45
    if ([string]::IsNullOrWhiteSpace($caseRunIDText)) {
        throw ($Label + ": intent " + $intentID + " produced no case_run within 45s")
    }
    $caseRunID = [int64]$caseRunIDText
    $row = Get-CaseRunRow -CaseRunID $caseRunID
    if ($null -ne $row -and $row.WaveID -gt 0) {
        $script:createdWaves += [pscustomobject]@{ VersionID = $VersionID; WaveID = $row.WaveID; Label = $Label }
    }
    return [pscustomobject]@{
        IntentID  = $intentID
        CaseRunID = $caseRunID
        WaveID    = $(if ($null -ne $row) { $row.WaveID } else { [int64]0 })
        VersionID = $VersionID
    }
}

function Get-CaseRunRow {
    param([int64]$CaseRunID)
    $text = Get-Scalar (
        "SELECT status || '|' || failure_class || '|' || dispatch_epoch::text || '|' || attempts::text || '|' || " +
        "coalesce(owner_executor_id::text, '0') || '|' || coalesce(wave_id::text, '0') || '|' || " +
        "coalesce(summary->>'verdict_reason', '') || '|' || coalesce(owner_instance, '') " +
        "FROM case_run WHERE id = " + $CaseRunID + ";")
    if ([string]::IsNullOrWhiteSpace($text)) { return $null }
    $parts = $text.Split("|")
    if ($parts.Count -lt 8) { return $null }
    return [pscustomobject]@{
        Status          = [string]$parts[0]
        FailureClass    = [string]$parts[1]
        DispatchEpoch   = [int64]$parts[2]
        Attempts        = [int]$parts[3]
        OwnerExecutorID = [string]$parts[4]
        WaveID          = [int64]$parts[5]
        VerdictReason   = [string]$parts[6]
        OwnerInstance   = [string]$parts[7]
    }
}

function Wait-CaseRunRunning {
    param([int64]$CaseRunID, [string]$ExpectedOwner = "", [int]$TimeoutSeconds = 45)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $last = ""
    while ((Get-Date) -lt $deadline) {
        $row = Get-CaseRunRow -CaseRunID $CaseRunID
        if ($null -ne $row) {
            $last = $row.Status + "/" + $row.OwnerExecutorID + "/epoch" + $row.DispatchEpoch
            if ($row.Status -eq "running") {
                if ([string]::IsNullOrWhiteSpace($ExpectedOwner) -or $row.OwnerExecutorID -eq $ExpectedOwner) {
                    return $row
                }
            }
            if ($row.Status -eq "passed" -or $row.Status -eq "failed" -or $row.Status -eq "interrupted" -or
                $row.Status -eq "superseded" -or $row.Status -eq "cancelled") {
                Write-Log ("case_run " + $CaseRunID + " reached " + $row.Status + " before it could be observed running") "Yellow"
                return $row
            }
        }
        Start-Sleep -Milliseconds 200
    }
    Write-Log ("timed out waiting for case_run " + $CaseRunID + " to run (last " + $last + ")") "Yellow"
    return $null
}

function Wait-CaseRunTerminal {
    param([int64]$CaseRunID, [int]$TimeoutSeconds = 180)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $last = $null
    while ((Get-Date) -lt $deadline) {
        $row = Get-CaseRunRow -CaseRunID $CaseRunID
        if ($null -ne $row) {
            $last = $row
            switch ($row.Status) {
                "passed" { return $row }
                "failed" { return $row }
                "blocked" { return $row }
                "inconclusive" { return $row }
                "skipped" { return $row }
                "interrupted" { return $row }
                "superseded" { return $row }
            }
        }
        Start-Sleep -Milliseconds 500
    }
    if ($null -ne $last) { Write-Log ("case_run " + $CaseRunID + " still " + $last.Status + " after timeout") "Yellow" }
    return $last
}

# ---------------------------------------------------------------------------
# scenarios
# ---------------------------------------------------------------------------

function Scenario-Startup {
    Write-Section "S8.1 dual replica startup, readiness, node ids, leader"
    $healthA = Invoke-ApiOnPort -Port $ApiPort1 -Method "GET" -Path "/api/health" -Token "" -TimeoutSec 10
    $healthB = Invoke-ApiOnPort -Port $ApiPort2 -Method "GET" -Path "/api/health" -Token "" -TimeoutSec 10
    $null = Add-Check ($healthA.Status -eq 200) "api A /health 200" ("status=" + $healthA.Status)
    $null = Add-Check ($healthB.Status -eq 200) "api B /health 200" ("status=" + $healthB.Status)
    $versionA = ""
    $versionB = ""
    if ($null -ne $healthA.Json -and $null -ne $healthA.Json.data) { $versionA = [string]$healthA.Json.data.version }
    if ($null -ne $healthB.Json -and $null -ne $healthB.Json.data) { $versionB = [string]$healthB.Json.data.version }
    $null = Add-Check (($versionA -eq $expectedAPIVersion) -and ($versionB -eq $expectedAPIVersion)) `
        "both replicas report the frozen API version" ("a=" + $versionA + " b=" + $versionB + " expected=" + $expectedAPIVersion)
    $readyA = Get-HttpStatus -Port $ApiPort1 -Path "/api/health/ready"
    $readyB = Get-HttpStatus -Port $ApiPort2 -Path "/api/health/ready"
    $null = Add-Check (($readyA -eq 200) -and ($readyB -eq 200)) "both replicas ready (200)" ("a=" + $readyA + " b=" + $readyB)

    $instances = @(Get-InstanceItems -Port $ApiPort1)
    $instanceA = Find-InstanceByPort -Items $instances -Port $ApiPort1
    $instanceB = Find-InstanceByPort -Items $instances -Port $ApiPort2
    $active = @($instances | Where-Object { $_.status -eq "active" })
    $null = Add-Check ($active.Count -eq 2) "two active API instances registered" ("active=" + $active.Count)
    $nodeA = ""
    $nodeB = ""
    if ($null -ne $instanceA) { $nodeA = [string]$instanceA.node_id }
    if ($null -ne $instanceB) { $nodeB = [string]$instanceB.node_id }
    $null = Add-Check (($nodeA -eq "1") -and ($nodeB -eq "2")) "deterministic node ids 1 and 2" ("a=" + $nodeA + " b=" + $nodeB)
    $null = Add-Check ($nodeA -ne $nodeB) "node ids are distinct" ("a=" + $nodeA + " b=" + $nodeB)

    $leaderPids = @(Get-LeaderLockPids)
    $null = Add-Check ($leaderPids.Count -eq 1) "exactly one leader advisory lock" ("holders=" + $leaderPids.Count)
    $script:leaderPidBeforeStop = ""
    if ($leaderPids.Count -eq 1) { $script:leaderPidBeforeStop = [string]$leaderPids[0] }
    $null = Add-Check (-not [string]::IsNullOrWhiteSpace($script:leaderPidBeforeStop)) "leader lock holder observed before failover" ("pg_pid=" + $script:leaderPidBeforeStop)

    $executors = @(Get-ExecutorItems -Port $ApiPort1)
    $execA = Find-ExecutorByEndpoint -Items $executors -Endpoint ("http://127.0.0.1:" + $ExecutorPortA)
    $execB = Find-ExecutorByEndpoint -Items $executors -Endpoint ("http://127.0.0.1:" + $ExecutorPortB)
    $execOk = ($null -ne $execA) -and ($null -ne $execB) -and ($execA.status -eq "active") -and ($execB.status -eq "active")
    $null = Add-Check $execOk "two real executors registered and active" (
        "a=" + $(if ($null -ne $execA) { $execA.status } else { "missing" }) +
        " b=" + $(if ($null -ne $execB) { $execB.status } else { "missing" }))

    Test-MigrationsOnce
    $instanceJson = $instances | ConvertTo-Json -Depth 8
    $null = Write-Evidence -Name "instances-startup.json" -Content $instanceJson
}

function Scenario-SettingsReload {
    Write-Section "S8.2 cross-instance settings hot reload (H1)"
    $key = "retention.gc_dry_run"
    $before = Get-SettingValueApi -Key $key -Port $ApiPort1
    if ($null -eq $before) {
        $null = Add-Check $false "settings hot reload probe readable" ("GET " + $key + " failed on A")
        return
    }
    $newValue = -not [bool]$before
    $watch = [System.Diagnostics.Stopwatch]::StartNew()
    $null = Set-SettingApi -Key $key -Value $newValue -Port $ApiPort1
    $observed = $null
    $deadline = (Get-Date).AddSeconds(6)
    while ((Get-Date) -lt $deadline) {
        $observed = Get-SettingValueApi -Key $key -Port $ApiPort2
        if ($null -ne $observed -and [bool]$observed -eq [bool]$newValue) { break }
        Start-Sleep -Milliseconds 100
    }
    $watch.Stop()
    $elapsedMs = [int]$watch.ElapsedMilliseconds
    $match = ($null -ne $observed -and [bool]$observed -eq [bool]$newValue)
    $null = Add-Check ($match -and $elapsedMs -le 2000) `
        "setting updated through A is visible on B within 2s" ("key=" + $key + " elapsed_ms=" + $elapsedMs + " observed=" + $observed)
    $alsoA = Get-SettingValueApi -Key $key -Port $ApiPort1
    $null = Add-Check ($null -ne $alsoA -and [bool]$alsoA -eq [bool]$newValue) "setting updated on A as well" ("value=" + $alsoA)
    $restored = $false
    try {
        $null = Set-SettingApi -Key $key -Value $before -Port $ApiPort1
        $restored = $true
    } catch {
        Write-Log ("restore " + $key + " failed: " + $_.Exception.Message) "Yellow"
    }
    $null = Add-Check $restored "original setting value restored" ("key=" + $key)
    $null = Write-Evidence -Name "settings-reload.txt" -Content (
        "key=" + $key + "`nbefore=" + $before + "`nnew=" + $newValue + "`nobserved_b=" + $observed + "`nelapsed_ms=" + $elapsedMs + "`n")
}

function Scenario-Sse {
    Write-Section "S8.3 cross-instance SSE and Last-Event-ID replay"
    if ([string]::IsNullOrWhiteSpace($script:token)) {
        $null = Add-Check $false "SSE admin session available" "no token"
        return
    }
    $sseFile = Join-Path $tmpDir "sse-b.txt"
    $replayFile = Join-Path $tmpDir "sse-b-replay.txt"
    $null = Start-SseProcess -Port $ApiPort2 -Channel "system" -OutFile $sseFile
    Start-Sleep -Seconds 2
    $key = "retention.event_days"
    $before = Get-SettingValueApi -Key $key -Port $ApiPort1
    if ($null -eq $before) {
        $null = Add-Check $false "SSE publication probe" ("cannot read " + $key)
        return
    }
    $newValue = 91
    if ([int]$before -eq 91) { $newValue = 90 }
    $null = Set-SettingApi -Key $key -Value $newValue -Port $ApiPort1

    $text = Wait-FilePattern -Path $sseFile -Pattern "event: settings\.updated" -TimeoutSeconds 20
    $received = -not [string]::IsNullOrWhiteSpace($text)
    $eventID = ""
    $dataLine = ""
    if ($received) {
        $frameMatch = [regex]::Match($text, "(?s)id: (\d+)\r?\nevent: settings\.updated\r?\ndata: ([^\r\n]+)")
        if ($frameMatch.Success) {
            $eventID = $frameMatch.Groups[1].Value
            $dataLine = $frameMatch.Groups[2].Value
        } else {
            $idMatch = [regex]::Match($text, "(?m)^id: (\d+)\s*$")
            if ($idMatch.Success) { $eventID = $idMatch.Groups[1].Value }
        }
    }
    $null = Add-Check ($received -and $eventID -ne "") "replica B SSE received a settings.updated event published through A" (
        "event_id=" + $eventID + " data_len=" + $dataLine.Length)
    foreach ($process in $script:sseProcesses) { Stop-OwnedProcess -Process $process }
    $script:sseProcesses = @()

    if ($eventID -ne "") {
        $cursor = [string]([int64]$eventID - 1)
        $null = Start-SseProcess -Port $ApiPort2 -Channel "system" -OutFile $replayFile -LastEventId $cursor
        $replayText = Wait-FilePattern -Path $replayFile -Pattern ("id: " + $eventID + "\r?\nevent: settings\.updated") -TimeoutSeconds 15
        $replayed = -not [string]::IsNullOrWhiteSpace($replayText)
        $replayData = ""
        if ($replayed) {
            $replayMatch = [regex]::Match($replayText, "(?s)id: " + $eventID + "\r?\nevent: settings\.updated\r?\ndata: ([^\r\n]+)")
            if ($replayMatch.Success) { $replayData = $replayMatch.Groups[1].Value }
        }
        $consistent = $replayed -and ($replayData -eq $dataLine)
        $null = Add-Check $consistent "Last-Event-ID replay is consistent on B" (
            "cursor=" + $cursor + " id=" + $eventID + " data_match=" + ($replayData -eq $dataLine))
        foreach ($process in $script:sseProcesses) { Stop-OwnedProcess -Process $process }
        $script:sseProcesses = @()
    } else {
        $null = Add-Check $false "Last-Event-ID replay is consistent on B" "no event id captured"
    }
    try { $null = Set-SettingApi -Key $key -Value $before -Port $ApiPort1 } catch { }
}

function Scenario-DrainRolling {
    Write-Section "S8.4 drain counters and rolling/leader takeover"
    $instances = @(Get-InstanceItems -Port $ApiPort1)
    $instanceA = Find-InstanceByPort -Items $instances -Port $ApiPort1
    $instanceB = Find-InstanceByPort -Items $instances -Port $ApiPort2
    if ($null -eq $instanceA -or $null -eq $instanceB) {
        $null = Add-Check $false "instance rows available for drain" "missing A or B row"
        return
    }
    if ($instanceB.status -ne "active") {
        $null = Invoke-ApiPostEmptyOnPort -Port $ApiPort1 -Path ("/api/admin/instances/" + $instanceB.id + "/activate")
    }
    $null = Add-Check (Wait-HttpStatus -Port $ApiPort2 -Path "/api/health/ready" -Expected 200 -TimeoutSeconds 15 -Label "B ready before drain") `
        "B is ready before draining A" ("status=" + (Get-HttpStatus -Port $ApiPort2 -Path "/api/health/ready"))

    $candidates = Get-CaseCandidates
    $drainCase = Select-CaseCandidate -Candidates $candidates -PreferSlow -PreferSingleSUT -ForbidHuman
    $drainRun = $null
    $workSeen = $false
    if ($null -ne $drainCase) {
        Ensure-VersionFixtures -VersionID $drainCase.VersionID
        Write-Log ("drain in-flight case: version=" + $drainCase.VersionID + " case_version=" + $drainCase.CaseVersionID + " slow=" + $drainCase.HasSlow)
        $null = Invoke-ApiPostEmptyOnPort -Port $ApiPort1 -Path ("/api/admin/instances/" + $instanceB.id + "/drain")
        $bDrained = Wait-HttpStatus -Port $ApiPort2 -Path "/api/health/ready" -Expected 503 -TimeoutSeconds 15 -Label "B readiness after local drain"
        $null = Add-Check $bDrained "B readiness 503 after a cross-instance drain" "status=$(Get-HttpStatus -Port $ApiPort2 -Path '/api/health/ready')"
        # The remote drain is mirrored into B's local worker gate by its 1s
        # heartbeat; give it one beat before creating work so only A can claim.
        Start-Sleep -Seconds 2
        try {
            $drainRun = Start-IntentForCase -VersionID $drainCase.VersionID -CaseVersionID $drainCase.CaseVersionID -Label "drain-case"
            $running = Wait-CaseRunRunning -CaseRunID $drainRun.CaseRunID -TimeoutSeconds 45
            $null = Add-Check ($null -ne $running -and $running.Status -eq "running") "real case_run running while A is drained" (
                "case_run=" + $drainRun.CaseRunID + " status=" + $(if ($null -ne $running) { $running.Status } else { "missing" }))
            $deadline = (Get-Date).AddSeconds(20)
            while ((Get-Date) -lt $deadline) {
                $readyJson = Invoke-ApiOnPort -Port $ApiPort1 -Method "GET" -Path "/api/health/ready" -Token "" -TimeoutSec 5
                if ($readyJson.Status -eq 200 -and $null -ne $readyJson.Json -and $null -ne $readyJson.Json.data) {
                    if ([int]$readyJson.Json.data.inflight_work -ge 1) { $workSeen = $true; break }
                }
                Start-Sleep -Milliseconds 250
            }
            $null = Add-Check $workSeen "A reports inflight_work while the case run executes" ("case_run=" + $drainRun.CaseRunID)
        } catch {
            Add-Check $false "real drain case_run created through the API" $_.Exception.Message
            $drainRun = $null
        }
        $null = Invoke-ApiPostEmptyOnPort -Port $ApiPort1 -Path ("/api/admin/instances/" + $instanceB.id + "/activate")
        $null = Add-Check (Wait-HttpStatus -Port $ApiPort2 -Path "/api/health/ready" -Expected 200 -TimeoutSeconds 15 -Label "B ready after activation") `
            "B ready again before rolling A" "status=$(Get-HttpStatus -Port $ApiPort2 -Path '/api/health/ready')"
    } else {
        Add-Skip "real case_run for drain counting" "no current+confirmed case_version with SUT binding in an idle version"
    }

    $job = Start-SlowHttpHold -Port $ApiPort1 -HoldSeconds 180
    $requestSeen = $false
    $workSeenOnA = $workSeen
    $deadline = (Get-Date).AddSeconds(15)
    while ((Get-Date) -lt $deadline) {
        $readyJson = Invoke-ApiOnPort -Port $ApiPort1 -Method "GET" -Path "/api/health/ready" -Token "" -TimeoutSec 5
        if ($readyJson.Status -eq 200 -and $null -ne $readyJson.Json -and $null -ne $readyJson.Json.data) {
            if ([int]$readyJson.Json.data.inflight_requests -ge 1) { $requestSeen = $true }
            if ([int]$readyJson.Json.data.inflight_work -ge 1) { $workSeenOnA = $true }
        }
        if ($requestSeen) { break }
        Start-Sleep -Milliseconds 250
    }
    $null = Add-Check $requestSeen "slow HTTP request counted as inflight_requests on A" ("seen=" + $requestSeen)

    $drainResp = Invoke-ApiPostEmptyOnPort -Port $ApiPort2 -Path ("/api/admin/instances/" + $instanceA.id + "/drain")
    $null = Add-Check ($drainResp.Status -eq 200) "drain A through B accepted" ("status=" + $drainResp.Status)
    $null = Add-Check (Wait-HttpStatus -Port $ApiPort1 -Path "/api/health/ready" -Expected 503 -TimeoutSeconds 20 -Label "A readiness after drain") `
        "A readiness 503 after drain" "status=$(Get-HttpStatus -Port $ApiPort1 -Path '/api/health/ready')"
    $peerReady = Get-HttpStatus -Port $ApiPort2 -Path "/api/health/ready"
    $null = Add-Check ($peerReady -eq 200) "B stays ready (200) while A drains" ("b=" + $peerReady)

    $observedRequests = 0
    $observedWork = 0
    $deadline = (Get-Date).AddSeconds(12)
    $lastSnapshot = @()
    while ((Get-Date) -lt $deadline) {
        $snapshot = @(Get-InstanceItems -Port $ApiPort2)
        $lastSnapshot = $snapshot
        $currentA = Find-InstanceByPort -Items $snapshot -Port $ApiPort1
        if ($null -ne $currentA) {
            $observedRequests = [Math]::Max($observedRequests, [int]$currentA.active_requests)
            $observedWork = [Math]::Max($observedWork, [int]$currentA.active_work)
        }
        if ($observedRequests -ge 1 -and ($null -eq $drainRun -or $observedWork -ge 1)) { break }
        Start-Sleep -Milliseconds 300
    }
    $null = Add-Check ($observedRequests -ge 1) "admin instances exposes A active_requests > 0" ("max_seen=" + $observedRequests)
    if ($null -ne $drainRun) {
        $null = Add-Check ($observedWork -ge 1) "admin instances exposes A active_work > 0" (
            "max_seen=" + $observedWork + " direct_inflight_work_seen=" + [bool]$workSeenOnA)
    } else {
        Add-Skip "admin instances exposes A active_work > 0" "no real case run was available for the drain window"
    }
    $snapshotJson = $lastSnapshot | ConvertTo-Json -Depth 8
    $null = Write-Evidence -Name "instances-during-drain.json" -Content $snapshotJson

    Stop-SlowHttpHold
    if ($null -ne $drainRun) {
        $finalDrainRun = Wait-CaseRunTerminal -CaseRunID $drainRun.CaseRunID -TimeoutSeconds 120
        if ($null -ne $finalDrainRun) {
            $null = Add-Check (-not ($finalDrainRun.Status -eq "running" -or $finalDrainRun.Status -eq "pending")) `
                "drain case_run completed before A stops" ("status=" + $finalDrainRun.Status + " failure=" + $finalDrainRun.FailureClass)
        }
    }
    $zeroSeen = $false
    $deadline = (Get-Date).AddSeconds(120)
    $lastZero = ""
    while ((Get-Date) -lt $deadline) {
        $snapshot = @(Get-InstanceItems -Port $ApiPort2)
        $currentA = Find-InstanceByPort -Items $snapshot -Port $ApiPort1
        if ($null -ne $currentA) {
            $lastZero = "requests=" + $currentA.active_requests + " work=" + $currentA.active_work
            if ([int]$currentA.active_requests -eq 0 -and [int]$currentA.active_work -eq 0) { $zeroSeen = $true; break }
        }
        Start-Sleep -Milliseconds 500
    }
    $null = Add-Check $zeroSeen "A in-flight counters reached zero" ($lastZero + " status=" + $(if ($zeroSeen) { "zero" } else { "timeout" }))
    $readyAfterZero = Get-HttpStatus -Port $ApiPort1 -Path "/api/health/ready"
    $null = Add-Check ($readyAfterZero -eq 503) "A readiness stays 503 after counters reach zero" ("status=" + $readyAfterZero)

    $countsCutoff = Get-DbTimestamp
    $countsBefore = Get-CountersSnapshot
    $countsBeforeAsOf = Get-CountsAsOf -Cutoff $countsCutoff
    $null = Write-Evidence -Name "counts-before-roll.txt" -Content ($countsBefore + "`ncutoff=" + $countsCutoff + "`nas_of=" + $countsBeforeAsOf)
    $leaderPidsBefore = @(Get-LeaderLockPids)
    $null = Add-Check ($leaderPidsBefore.Count -eq 1) "leader lock held before stopping A" ("holders=" + $leaderPidsBefore.Count)

    Write-Log "hard-stopping api A (rolling upgrade step)"
    if ($null -ne $script:apiA) { Stop-OwnedProcess -Process $script:apiA.Process }
    $portClosed = Wait-PortClosed -Port $ApiPort1 -TimeoutSeconds 20
    $null = Add-Check $portClosed "api A process stopped and port released" ("port=" + $ApiPort1)

    $leaderMoved = $false
    $leaderAfter = @()
    $deadline = (Get-Date).AddSeconds(20)
    while ((Get-Date) -lt $deadline) {
        $leaderAfter = @(Get-LeaderLockPids)
        if ($leaderAfter.Count -eq 1) {
            $leaderMoved = $true
            break
        }
        Start-Sleep -Milliseconds 400
    }
    $newHolder = ""
    if ($leaderAfter.Count -eq 1) { $newHolder = [string]$leaderAfter[0] }
    $changed = $leaderMoved -and ($newHolder -ne $script:leaderPidBeforeStop)
    $script:leaderPidAfterTakeover = $newHolder
    $null = Add-Check $changed "leader advisory lock moved to the surviving replica B" (
        "before=" + $script:leaderPidBeforeStop + " after=" + $newHolder)
    $null = Add-Check (Wait-HttpStatus -Port $ApiPort2 -Path "/api/health/ready" -Expected 200 -TimeoutSeconds 15 -Label "B readiness after A stop") `
        "B is ready after A stops" "status=$(Get-HttpStatus -Port $ApiPort2 -Path '/api/health/ready')"

    $offline = $false
    $deadline = (Get-Date).AddSeconds(30)
    $lastStatus = ""
    while ((Get-Date) -lt $deadline) {
        $snapshot = @(Get-InstanceItems -Port $ApiPort2)
        $currentA = Find-InstanceByPort -Items $snapshot -Port $ApiPort1
        if ($null -ne $currentA) {
            $lastStatus = [string]$currentA.status
            if ($currentA.status -eq "offline" -or $currentA.status -eq "stopped") { $offline = $true; break }
        }
        Start-Sleep -Milliseconds 500
    }
    $null = Add-Check $offline "stopped replica A is no longer active in the registry" ("status=" + $lastStatus)

    $countsAfter = Get-CountersSnapshot
    $countsAfterAsOf = Get-CountsAsOf -Cutoff $countsCutoff
    $null = Write-Evidence -Name "counts-after-roll.txt" -Content ($countsAfter + "`ncutoff=" + $countsCutoff + "`nas_of=" + $countsAfterAsOf)
    $null = Add-Check ($countsBeforeAsOf -eq $countsAfterAsOf) "no duplicate work/agent/case rows across rolling restart" (
        "before_as_of=" + $countsBeforeAsOf + " after_as_of=" + $countsAfterAsOf + " cutoff=" + $countsCutoff)
    Assert-NoDuplicateRows
    Test-MigrationsOnce
    $null = Write-Evidence -Name "leader-before-after.txt" -Content (
        "before_stop_pg_pid=" + $script:leaderPidBeforeStop + "`nafter_takeover_pg_pid=" + $script:leaderPidAfterTakeover + "`n")
}

function Scenario-KillRestart {
    Write-Section "S8.5 hard-kill recovery and stale instance offline"
    if ([string]::IsNullOrWhiteSpace($script:leaderPidAfterTakeover)) {
        $null = Add-Check $false "leader takeover state available" "previous scenario did not capture a leader holder"
        return
    }
    try {
        $null = Set-SettingApi -Key "runtime.snowflake_node_id" -Value 1 -Port $ApiPort2
    } catch {
        Write-Log ("could not set preferred node id 1: " + $_.Exception.Message) "Yellow"
    }
    Start-ApiAProcess
    $token = Login-Admin -Port $ApiPort1
    if (-not [string]::IsNullOrWhiteSpace($token)) { $script:token = $token }
    $ready = Wait-HttpStatus -Port $ApiPort1 -Path "/api/health/ready" -Expected 200 -TimeoutSeconds 60 -Label "restarted A readiness"
    $null = Add-Check $ready "restarted replica A is ready" "status=$(Get-HttpStatus -Port $ApiPort1 -Path '/api/health/ready')"
    $instances = @(Get-InstanceItems -Port $ApiPort2)
    $restartedA = Find-InstanceByPort -Items $instances -Port $ApiPort1
    $null = Add-Check ($null -ne $restartedA -and $restartedA.status -eq "active") "restarted A is active in the registry" (
        "status=" + $(if ($null -ne $restartedA) { $restartedA.status } else { "missing" }))
    $leaderNow = @(Get-LeaderLockPids)
    $null = Add-Check ($leaderNow.Count -eq 1 -and [string]$leaderNow[0] -eq $script:leaderPidAfterTakeover) `
        "leader lock stays with B after A restart" ("holder=" + ($leaderNow -join ",") + " expected=" + $script:leaderPidAfterTakeover)

    $countsCutoff = Get-DbTimestamp
    $countsBefore = Get-CountersSnapshot
    $countsBeforeAsOf = Get-CountsAsOf -Cutoff $countsCutoff
    $null = Write-Evidence -Name "counts-before-kill9.txt" -Content ($countsBefore + "`ncutoff=" + $countsCutoff + "`nas_of=" + $countsBeforeAsOf)
    $instanceA = Find-InstanceByPort -Items $instances -Port $ApiPort1
    $instanceAID = ""
    if ($null -ne $instanceA) { $instanceAID = [string]$instanceA.id }

    Write-Log "hard-killing api A (taskkill /F /T, closest Windows equivalent of kill -9)"
    if ($null -ne $script:apiA) { Stop-OwnedProcess -Process $script:apiA.Process }
    $closed = Wait-PortClosed -Port $ApiPort1 -TimeoutSeconds 20
    $null = Add-Check $closed "killed replica A port released" ("port=" + $ApiPort1)

    $offline = $false
    $lastStatus = ""
    $deadline = (Get-Date).AddSeconds(40)
    while ((Get-Date) -lt $deadline) {
        $snapshot = @(Get-InstanceItems -Port $ApiPort2)
        $currentA = Find-InstanceByPort -Items $snapshot -Port $ApiPort1
        if ($null -ne $currentA) {
            $lastStatus = [string]$currentA.status
            if ($currentA.status -eq "offline" -or $currentA.status -eq "stopped") { $offline = $true; break }
        }
        Start-Sleep -Milliseconds 500
    }
    $null = Add-Check $offline "stale killed instance is marked offline" ("status=" + $lastStatus)
    if ($instanceAID -ne "") {
        $offlineEvent = [int](Get-Scalar (
            "SELECT count(*) FROM event WHERE type = 'instance.offline' AND envelope->'payload'->>'instance_id' = '" + $instanceAID + "';"))
        $null = Add-Check ($offlineEvent -ge 1) "instance.offline event recorded for the killed replica" ("rows=" + $offlineEvent)
    }
    $null = Add-Check (Wait-HttpStatus -Port $ApiPort2 -Path "/api/health/ready" -Expected 200 -TimeoutSeconds 15 -Label "B readiness after kill") `
        "surviving replica B stays ready after the kill" "status=$(Get-HttpStatus -Port $ApiPort2 -Path '/api/health/ready')"

    $countsAfter = Get-CountersSnapshot
    $countsAfterAsOf = Get-CountsAsOf -Cutoff $countsCutoff
    $null = Write-Evidence -Name "counts-after-kill9.txt" -Content ($countsAfter + "`ncutoff=" + $countsCutoff + "`nas_of=" + $countsAfterAsOf)
    $null = Add-Check ($countsBeforeAsOf -eq $countsAfterAsOf) "no duplicate work/agent/case rows after hard kill" (
        "before_as_of=" + $countsBeforeAsOf + " after_as_of=" + $countsAfterAsOf + " cutoff=" + $countsCutoff)
    Assert-NoDuplicateRows
    Test-MigrationsOnce

    try {
        $null = Set-SettingApi -Key "runtime.snowflake_node_id" -Value 1 -Port $ApiPort2
    } catch { }
    Start-ApiAProcess
    $token = Login-Admin -Port $ApiPort1
    if (-not [string]::IsNullOrWhiteSpace($token)) { $script:token = $token }
    $null = Add-Check (Wait-HttpStatus -Port $ApiPort1 -Path "/api/health/ready" -Expected 200 -TimeoutSeconds 60 -Label "A ready after kill-9 restart") `
        "replica A restarts after the hard kill" "status=$(Get-HttpStatus -Port $ApiPort1 -Path '/api/health/ready')"
    $instances = @(Get-InstanceItems -Port $ApiPort2)
    $active = @($instances | Where-Object { $_.status -eq "active" })
    $null = Add-Check ($active.Count -eq 2) "two active replicas after kill-9 restart" ("active=" + $active.Count)
    $countsFinal = Get-CountersSnapshot
    $countsFinalAsOf = Get-CountsAsOf -Cutoff $countsCutoff
    $null = Add-Check ($countsBeforeAsOf -eq $countsFinalAsOf) "no duplicate rows after kill-9 restart" (
        "before_as_of=" + $countsBeforeAsOf + " final_as_of=" + $countsFinalAsOf)
    $leaderNow = @(Get-LeaderLockPids)
    $null = Add-Check ($leaderNow.Count -eq 1 -and [string]$leaderNow[0] -eq $script:leaderPidAfterTakeover) `
        "leader lock remains with B after kill-9 restart" ("holder=" + ($leaderNow -join ","))
    Test-MigrationsOnce
}

function Scenario-ExecutorLoss {
    Write-Section "S8.6 real executor loss, interrupted(executor_lost), redispatch"
    if ([string]::IsNullOrWhiteSpace($script:executorAID) -or [string]::IsNullOrWhiteSpace($script:executorBID)) {
        $null = Add-Check $false "executor ids available" "executor roster was not initialized"
        return
    }
    $executors = @(Get-ExecutorItems -Port $ApiPort1)
    $execA = Find-ExecutorByEndpoint -Items $executors -Endpoint ("http://127.0.0.1:" + $ExecutorPortA)
    $execB = Find-ExecutorByEndpoint -Items $executors -Endpoint ("http://127.0.0.1:" + $ExecutorPortB)
    if ($null -eq $execA -or $null -eq $execB) {
        $null = Add-Check $false "both drill executors present" "missing executor row"
        return
    }
    $execAHealth = Get-HttpStatus -Port $ExecutorPortA -Path "/healthz"
    $execBHealth = Get-HttpStatus -Port $ExecutorPortB -Path "/healthz"
    $null = Add-Check (($execAHealth -eq 200) -and ($execBHealth -eq 200)) "both executor processes are healthy before the loss drill" (
        "a=" + $execAHealth + " b=" + $execBHealth)
    if ($execAHealth -ne 200 -or $execBHealth -ne 200) { return }
    if ($execB.status -ne "active") {
        $null = Invoke-ApiPostEmptyOnPort -Port $ApiPort1 -Path ("/api/admin/executors/" + $script:executorBID + "/activate")
    }
    if ($execA.status -ne "active") {
        $null = Invoke-ApiPostEmptyOnPort -Port $ApiPort1 -Path ("/api/admin/executors/" + $script:executorAID + "/activate")
    }
    Start-Sleep -Seconds 1

    $candidates = Get-CaseCandidates
    $targetCase = Select-CaseCandidate -Candidates $candidates -PreferSlow -PreferSingleSUT -ForbidHuman
    if ($null -eq $targetCase) {
        $null = Add-Check $false "real case run available for executor-loss drill" (
            "BLOCKER: no current+confirmed case_version with a version_sut binding in an idle version of " + $script:dbName +
            "; run p8-acceptance.ps1 (S0-S6) first. Synthetic case_run rows are intentionally not substituted.")
        return
    }
    Ensure-VersionFixtures -VersionID $targetCase.VersionID
    Write-Log ("target case: version=" + $targetCase.VersionID + " case_version=" + $targetCase.CaseVersionID +
        " slow=" + $targetCase.HasSlow + " title=" + $targetCase.Title)

    $controlIntent = $null
    $controlRunID = [int64]0
    $controlCase = Select-CaseCandidate -Candidates $candidates -ExcludeVersionID $targetCase.VersionID -PreferSlow -PreferSingleSUT -ForbidHuman
    if ($null -ne $controlCase -and $targetCase.SUTCount -gt 1 -and $controlCase.SUTCount -gt 1) {
        Write-Log "skipping the control run: both cases are cross-system and the cross-system quota is 1" "Yellow"
        $controlCase = $null
    }
    if ($null -ne $controlCase) {
        try {
            Ensure-VersionFixtures -VersionID $controlCase.VersionID
            $null = Invoke-ApiPostEmptyOnPort -Port $ApiPort1 -Path ("/api/admin/executors/" + $script:executorBID + "/drain")
            $null = Wait-SqlValue -Sql ("SELECT status FROM executor_instance WHERE id = " + $script:executorBID + ";") -Want "draining" -TimeoutSeconds 15
            $controlIntent = Start-IntentForCase -VersionID $controlCase.VersionID -CaseVersionID $controlCase.CaseVersionID -Label "control-case"
            $controlLive = Wait-CaseRunRunning -CaseRunID $controlIntent.CaseRunID -ExpectedOwner $script:executorAID -TimeoutSeconds 45
            if ($null -ne $controlLive -and $controlLive.Status -eq "running") {
                $controlRunID = $controlIntent.CaseRunID
                Write-Log ("control case_run " + $controlRunID + " running on executor A (same-database concurrent run)")
            } else {
                Write-Log "control case_run did not stay running on executor A; batch-interruption check will be reported as skipped"
            }
            $null = Invoke-ApiPostEmptyOnPort -Port $ApiPort1 -Path ("/api/admin/executors/" + $script:executorBID + "/activate")
        } catch {
            Write-Log ("control case setup failed: " + $_.Exception.Message) "Yellow"
            $controlRunID = [int64]0
            try { $null = Invoke-ApiPostEmptyOnPort -Port $ApiPort1 -Path ("/api/admin/executors/" + $script:executorBID + "/activate") } catch { }
        }
    } else {
        Write-Log "no second idle version with a confirmed case; batch-interruption control run is not available" "Yellow"
    }

    $null = Invoke-ApiPostEmptyOnPort -Port $ApiPort1 -Path ("/api/admin/executors/" + $script:executorAID + "/drain")
    $null = Wait-SqlValue -Sql ("SELECT status FROM executor_instance WHERE id = " + $script:executorAID + ";") -Want "draining" -TimeoutSeconds 15
    $targetRun = Start-IntentForCase -VersionID $targetCase.VersionID -CaseVersionID $targetCase.CaseVersionID -Label "executor-loss"
    $running = Wait-CaseRunRunning -CaseRunID $targetRun.CaseRunID -TimeoutSeconds 45
    if ($null -eq $running -or $running.Status -ne "running") {
        $null = Add-Check $false "target case_run running on executor B before the kill" (
            "case_run=" + $targetRun.CaseRunID + " status=" + $(if ($null -ne $running) { $running.Status + "/" + $running.OwnerExecutorID } else { "missing" }))
        return
    }
    $ownerOK = ($running.OwnerExecutorID -eq $script:executorBID)
    $null = Add-Check $ownerOK "target case_run dispatched to the selected executor B" (
        "case_run=" + $targetRun.CaseRunID + " owner=" + $running.OwnerExecutorID + " expected=" + $script:executorBID + " epoch=" + $running.DispatchEpoch)
    $initialEpoch = $running.DispatchEpoch
    $initialAttempts = $running.Attempts
    $null = Write-Evidence -Name "case-run-before-executor-loss.json" -Content (
        Get-Scalar ("SELECT row_to_json(cr)::text FROM case_run cr WHERE id = " + $targetRun.CaseRunID + ";"))

    $controlSnapshot = $null
    if ($controlRunID -gt 0) {
        $controlSnapshot = Get-CaseRunRow -CaseRunID $controlRunID
    }
    $otherRunning = Invoke-PsqlRows -Sql (
        "SELECT id::text, status, dispatch_epoch::text, coalesce(owner_executor_id::text, '0') " +
        "FROM case_run WHERE status IN ('running','paused') AND id <> " + $targetRun.CaseRunID +
        " AND (owner_executor_id IS NULL OR owner_executor_id <> " + $script:executorBID + ") ORDER BY id;")
    $otherRunningText = ($otherRunning | ForEach-Object { $_ -join "|" }) -join "`n"
    $null = Write-Evidence -Name "other-running-case-runs-before.json" -Content $otherRunningText

    Write-Log "hard-killing executor B (taskkill /F /T) while case_run " + $targetRun.CaseRunID + " is running"
    if ($null -ne $script:executorB) { Stop-OwnedProcess -Process $script:executorB.Process }
    $null = Wait-PortClosed -Port $ExecutorPortB -TimeoutSeconds 15
    $null = Add-Check (-not (Test-PortListening -Port $ExecutorPortB)) "executor B process is dead" ("port=" + $ExecutorPortB)
    $null = Invoke-ApiPostEmptyOnPort -Port $ApiPort1 -Path ("/api/admin/executors/" + $script:executorAID + "/activate")

    $execBRow = ""
    $offline = $false
    $deadline = (Get-Date).AddSeconds(60)
    while ((Get-Date) -lt $deadline) {
        $execBRow = Get-Scalar ("SELECT status FROM executor_instance WHERE id = " + $script:executorBID + ";")
        if ($execBRow -eq "offline") { $offline = $true; break }
        Start-Sleep -Milliseconds 500
    }
    $null = Add-Check $offline "executor B heartbeat goes offline after the kill" ("status=" + $execBRow)

    $redispatched = $null
    $interruptedObserved = $false
    $deadline = (Get-Date).AddSeconds(150)
    $lastRow = ""
    while ((Get-Date) -lt $deadline) {
        $row = Get-CaseRunRow -CaseRunID $targetRun.CaseRunID
        if ($null -ne $row) {
            $lastRow = $row.Status + "/" + $row.FailureClass + "/epoch" + $row.DispatchEpoch + "/attempts" + $row.Attempts + "/owner" + $row.OwnerExecutorID
            if ($row.Status -eq "interrupted" -or $row.FailureClass -eq "executor_lost") { $interruptedObserved = $true }
            if ($row.DispatchEpoch -ge ($initialEpoch + 2) -and $row.OwnerExecutorID -eq $script:executorAID) {
                $redispatched = $row
                break
            }
        }
        Start-Sleep -Milliseconds 500
    }
    $null = Add-Check ($null -ne $redispatched) "dispatch_epoch incremented and the run was redispatched to executor A" (
        "initial_epoch=" + $initialEpoch + " last=" + $lastRow)
    $final = Wait-CaseRunTerminal -CaseRunID $targetRun.CaseRunID -TimeoutSeconds 180
    $finalOK = $null -ne $final -and -not ($final.Status -eq "interrupted" -or $final.Status -eq "superseded" -or
        $final.Status -eq "pending" -or $final.Status -eq "running")
    $null = Add-Check $finalOK "redispatched case_run reached a terminal result on executor A" (
        "status=" + $(if ($null -ne $final) { $final.Status } else { "missing" }) +
        " failure=" + $(if ($null -ne $final) { $final.FailureClass } else { "" }) +
        " owner=" + $(if ($null -ne $final) { $final.OwnerExecutorID } else { "" }) +
        " attempts=" + $(if ($null -ne $final) { $final.Attempts } else { "" }))
    if ($null -ne $final) {
        $null = Add-Check ($final.Attempts -ge ($initialAttempts + 1)) "case_run was executed again after the loss (attempts incremented)" (
            "initial_attempts=" + $initialAttempts + " final_attempts=" + $final.Attempts)
        $null = Add-Check ($final.OwnerExecutorID -eq $script:executorAID) "final owner executor is A" ("owner=" + $final.OwnerExecutorID + " expected=" + $script:executorAID)
    }
    $null = Write-Evidence -Name "case-run-after-redispatch.json" -Content (
        Get-Scalar ("SELECT row_to_json(cr)::text FROM case_run cr WHERE id = " + $targetRun.CaseRunID + ";"))

    $interruptedEvents = [int](Get-Scalar (
        "SELECT count(*) FROM event WHERE type = 'case_run.finished' AND envelope->'payload'->>'case_run_id' = '" +
        $targetRun.CaseRunID + "' AND envelope->'payload'->>'failure_class' = 'executor_lost';"))
    $redispatchedEvents = [int](Get-Scalar (
        "SELECT count(*) FROM event WHERE type = 'case_run.finished' AND envelope->'payload'->>'case_run_id' = '" +
        $targetRun.CaseRunID + "' AND envelope->'payload'->>'redispatched' = 'true';"))
    $null = Add-Check ($interruptedEvents -ge 1) "case_run.finished interrupted(executor_lost) event recorded" ("rows=" + $interruptedEvents)
    $null = Add-Check ($interruptedObserved -or $interruptedEvents -ge 1) "case_run was observed interrupted(executor_lost)" (
        "row_observed=" + $interruptedObserved + " event_rows=" + $interruptedEvents)
    $null = Add-Check ($redispatchedEvents -ge 1) "case_run.finished redispatched event recorded" ("rows=" + $redispatchedEvents)
    $auditRows = [int](Get-Scalar (
        "SELECT count(*) FROM audit_log WHERE action = 'execution.case_run_redispatched' AND metadata->>'case_run_id' = '" +
        $targetRun.CaseRunID + "';"))
    $null = Add-Check ($auditRows -ge 1) "execution.case_run_redispatched audit row recorded" ("rows=" + $auditRows)

    $sameIntentRuns = [int](Get-Scalar "SELECT count(*) FROM case_run WHERE intent_id = " + $targetRun.IntentID + ";")
    $null = Add-Check ($sameIntentRuns -eq 1) "redispatch reused the single case_run row (no duplicate execution)" ("rows=" + $sameIntentRuns)
    $epochWorkDupes = [int](Get-Scalar (
        "SELECT count(*) FROM (SELECT 1 FROM work_item WHERE type = 'execute.case' AND version_id = " + $targetCase.VersionID +
        " GROUP BY dedupe_key HAVING count(*) > 1) d;"))
    $null = Add-Check ($epochWorkDupes -eq 0) "no duplicate execute.case work items per dispatch epoch" ("duplicates=" + $epochWorkDupes)

    if ($null -ne $controlSnapshot) {
        $controlAfter = Get-CaseRunRow -CaseRunID $controlRunID
        $controlOK = $null -ne $controlAfter -and $controlAfter.FailureClass -ne "executor_lost" -and
            $controlAfter.Status -ne "interrupted" -and $controlAfter.Status -ne "superseded"
        $null = Add-Check $controlOK "concurrent case_run on executor A was not batch-interrupted" (
            "case_run=" + $controlRunID + " before=" + $controlSnapshot.Status + "/epoch" + $controlSnapshot.DispatchEpoch +
            " after=" + $(if ($null -ne $controlAfter) { $controlAfter.Status + "/epoch" + $controlAfter.DispatchEpoch } else { "missing" }))
    } else {
        Add-Skip "concurrent case_run on executor A was not batch-interrupted" `
            "no second idle version with a confirmed case (single-active-wave planner forbids a second wave in the target version)"
    }
    if ($otherRunning.Count -eq 0) {
        Add-Skip "other running case_runs unaffected" "no other running case_run existed at kill time"
    } else {
        $otherIDs = @($otherRunning | ForEach-Object { $_[0] })
        $otherInterrupted = [int](Get-Scalar (
            "SELECT count(*) FROM case_run WHERE status = 'interrupted' AND failure_class = 'executor_lost' AND id <> " +
            $targetRun.CaseRunID + " AND id IN (" + ($otherIDs -join ",") + ");"))
        $null = Add-Check ($otherInterrupted -eq 0) "other running case_runs were not batch-interrupted" (
            "snapshot=" + $otherRunning.Count + " wrongly_interrupted=" + $otherInterrupted)
    }
}

# ---------------------------------------------------------------------------
# cleanup / summary
# ---------------------------------------------------------------------------

function Stop-SseProcesses {
    foreach ($process in $script:sseProcesses) {
        Stop-OwnedProcess -Process $process
    }
    $script:sseProcesses = @()
}

function Cancel-CreatedWaves {
    if ($KeepRunning) { return }
    foreach ($entry in $script:createdWaves) {
        $status = Get-Scalar ("SELECT status FROM run_wave WHERE id = " + $entry.WaveID + ";")
        if ($status -eq "planned" -or $status -eq "running" -or $status -eq "paused") {
            $port = $ApiPort2
            if ($null -ne $script:apiA -and -not $script:apiA.Process.HasExited) { $port = $ApiPort1 }
            $path = "/api/versions/" + $entry.VersionID + "/waves/" + $entry.WaveID + "/cancel"
            $resp = Invoke-ApiPostEmptyOnPort -Port $port -Path $path
            Write-Log ("cancelled drill wave " + $entry.WaveID + " (" + $entry.Label + ") status=" + $resp.Status) "Yellow"
        }
    }
}

function Cleanup {
    if ($script:closed) { return }
    $script:closed = $true
    Write-Section "Cleanup"
    try { Stop-SlowHttpHold } catch { }
    try { Stop-SseProcesses } catch { }
    try { Cancel-CreatedWaves } catch { }
    if ($KeepRunning) {
        Write-Log "KeepRunning set: leaving surviving API/executor/fixture processes alive" "Yellow"
        if ($null -ne $script:apiA -and -not $script:apiA.Process.HasExited) {
            Write-Log ("api A pid=" + $script:apiA.Process.Id + " port=" + $ApiPort1)
        }
        if ($null -ne $script:apiB -and -not $script:apiB.Process.HasExited) {
            Write-Log ("api B pid=" + $script:apiB.Process.Id + " port=" + $ApiPort2)
        }
        if ($null -ne $script:executorA -and -not $script:executorA.Process.HasExited) {
            Write-Log ("executor A pid=" + $script:executorA.Process.Id + " port=" + $ExecutorPortA)
        }
        return
    }
    foreach ($entry in @($script:processes)) {
        if ($null -eq $entry -or $null -eq $entry.Process) { continue }
        Stop-OwnedProcess -Process $entry.Process
    }
    $script:processes = @()
    foreach ($entry in @($script:fixtures)) {
        if ($null -eq $entry -or $null -eq $entry.Process) { continue }
        Stop-OwnedProcess -Process $entry.Process
    }
    $script:fixtures = @()
    foreach ($port in @($ApiPort1, $ApiPort2, $ExecutorPortA, $ExecutorPortB)) {
        $null = Wait-PortClosed -Port $port -TimeoutSeconds 10
    }
    foreach ($key in $script:previousEnv.Keys) {
        [Environment]::SetEnvironmentVariable($key, [string]$script:previousEnv[$key])
    }
    Write-Log "API/executor/fixture processes stopped; evidence preserved under $tmpDir"
}

function Write-Summary {
    $status = "FAIL"
    if ($script:failCount -eq 0) { $status = "PASS" }
    $summary = @(
        "",
        ("RESULT: " + $status),
        ("PASS=" + $script:passCount + " FAIL=" + $script:failCount + " SKIP=" + $script:skipCount),
        ("exit_code=" + $(if ($script:failCount -eq 0) { 0 } else { 1 })),
        ("finished: " + (Get-Date).ToString("yyyy-MM-ddTHH:mm:sszzz")),
        ("evidence: " + $tmpDir)
    )
    foreach ($line in $summary) {
        Write-Host $line -ForegroundColor $(if ($status -eq "PASS") { "Green" } else { "Red" })
        Add-Content -LiteralPath $scenarioPath -Value $line -Encoding UTF8
    }
    try {
        Add-Content -LiteralPath $logPath -Value ($summary -join "`r`n") -Encoding UTF8
    } catch { }
    if ($script:failCount -eq 0) { return 0 }
    return 1
}

# ---------------------------------------------------------------------------
# preflight / validate-only
# ---------------------------------------------------------------------------

function Test-Parameters {
    $problems = @()
    foreach ($port in @($ApiPort1, $ApiPort2, $ExecutorPortA, $ExecutorPortB)) {
        if ($port -lt 1 -or $port -gt 65535) { $problems += ("port out of range: " + $port) }
    }
    if ($ApiPort1 -eq $ApiPort2) { $problems += "ApiPort1 and ApiPort2 must differ" }
    if ($ExecutorPortA -eq $ExecutorPortB) { $problems += "ExecutorPortA and ExecutorPortB must differ" }
    if ((@($ApiPort1, $ApiPort2, $ExecutorPortA, $ExecutorPortB) | Select-Object -Unique | Measure-Object).Count -ne 4) {
        $problems += "API and executor ports must not overlap"
    }
    if ([string]::IsNullOrWhiteSpace($DatabaseName)) { $problems += "DatabaseName is required" }
    return $problems
}

function Assert-ValidateOnly {
    $problems = @()
    Write-Section "Validate-only checks"

    $tokens = $null
    $parseErrors = $null
    $null = [System.Management.Automation.Language.Parser]::ParseFile($PSCommandPath, [ref]$tokens, [ref]$parseErrors)
    if ($null -ne $parseErrors -and $parseErrors.Count -gt 0) {
        foreach ($parseError in $parseErrors) { $problems += ("syntax: " + $parseError.Message) }
    } else {
        Write-Host "PowerShell syntax: OK"
    }

    $scriptText = Get-Content -LiteralPath $PSCommandPath -Raw
    foreach ($marker in @(
            "/api/health/ready",
            "/api/admin/instances",
            "/api/admin/executors",
            "/api/stream",
            "Last-Event-ID",
            "settings.updated",
            "active_requests",
            "active_work",
            "inflight_requests",
            "inflight_work",
            "executor_lost",
            "dispatch_epoch",
            "schema_migrations",
            "instance.offline",
            "pg_locks",
            "advisory",
            "taskkill.exe",
            "Start-IntentForCase",
            "Wait-CaseRunRunning",
            "KeepRunning"
        )) {
        if ($scriptText.IndexOf($marker, [System.StringComparison]::Ordinal) -lt 0) {
            $problems += ("flow marker missing: " + $marker)
        }
    }
    if ($problems.Count -eq 0) {
        Write-Host "Flow markers (readiness/instances/drain/leader/SSE/executor-loss/kill): OK"
    }

    $problems += @(Test-Parameters)
    if ($problems.Count -eq 0) { Write-Host "Parameter checks: OK" }

    $script:goExe = Resolve-GoExe
    $script:psqlExe = Resolve-PsqlExe
    $script:nodeExe = Resolve-NodeExe
    $script:curlExe = Resolve-CurlExe
    if ([string]::IsNullOrWhiteSpace($script:goExe)) { $problems += "go executable not found (.tools\go\bin\go.exe or PATH)" } else { Write-Host ("Go: " + $script:goExe) }
    if ([string]::IsNullOrWhiteSpace($script:psqlExe)) { $problems += "psql executable not found (.tools\pgsql\bin\psql.exe or PATH)" } else { Write-Host ("psql: " + $script:psqlExe) }
    if ([string]::IsNullOrWhiteSpace($script:nodeExe)) { $problems += "node executable not found" } else { Write-Host ("node: " + $script:nodeExe) }
    if ([string]::IsNullOrWhiteSpace($script:curlExe)) { $problems += "curl.exe not found" } else { Write-Host ("curl: " + $script:curlExe) }
    if (-not (Test-Path -LiteralPath (Join-Path $backendDir "cmd\eztest\main.go"))) { $problems += "backend/cmd/eztest/main.go is missing" }
    if (-not (Test-Path -LiteralPath (Join-Path $executorDir "src\server.mjs"))) { $problems += "executor/src/server.mjs is missing" }
    if (-not (Test-Path -LiteralPath (Join-Path $repoRoot "scripts\fixture-target.cjs"))) { $problems += "scripts/fixture-target.cjs is missing" }

    $dbProblemCount = $problems.Count
    try {
        $null = Resolve-DatabaseConfig
        Write-Host ("Database: " + $DatabaseName + " (URL redacted)")
        $script:expectedMigrations = Get-ExpectedMigrationCount
        Write-Host ("Expected migrations: " + $script:expectedMigrations)
        if (-not [string]::IsNullOrWhiteSpace($script:psqlExe)) {
            $probe = Invoke-Psql -Sql "SELECT count(*) FROM schema_migrations;" -AllowFailure
            if ($null -eq $probe) {
                Write-Warning ("database " + $DatabaseName + " is not reachable read-only; full run requires the S0-S6 clone")
            } else {
                Write-Host ("Database probe: reachable, schema_migrations=" + (($probe | Select-Object -First 1)))
            }
        }
    } catch {
        $problems += ("database config: " + $_.Exception.Message)
    }
    if ($problems.Count -eq $dbProblemCount) { Write-Host "Database config: OK (connectivity is checked again before the full run)" }

    Write-Host ""
    Write-Host "Planned flow:"
    Write-Host ("  0. build one binary; seed heartbeat/offline/executor settings in " + $DatabaseName)
    Write-Host ("  1. start api A :" + $ApiPort1 + " (node 1, leader) -> api B :" + $ApiPort2 + " (node 2)")
    Write-Host ("  2. start real executors :" + $ExecutorPortA + "/:" + $ExecutorPortB + " and drain foreign executor rows")
    Write-Host "  3. settings hot reload A -> B <= 2s; SSE B receives A event + Last-Event-ID replay"
    Write-Host "  4. real case run + slow HTTP request on A; drain A; assert counters 503 -> zero; stop A -> B leader"
    Write-Host "  5. hard kill A; assert stale offline, no duplicate rows; restart A"
    Write-Host ("  6. real case run on executor B; kill B; assert interrupted(executor_lost), epoch++, redispatch to A")
    Write-Host ("  7. evidence -> " + $tmpDir + "; exit 0 only when no check failed")
    Write-Host ""

    if ($problems.Count -gt 0) {
        Write-Host ("VALIDATION FAILED (" + $problems.Count + " problem(s)):") -ForegroundColor Red
        foreach ($problem in $problems) { Write-Host (" - " + $problem) -ForegroundColor Red }
        return 1
    }
    Write-Host "VALIDATION PASSED: scripts/p8-multi.ps1 syntax/flow/toolchain are consistent." -ForegroundColor Green
    return 0
}

# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------

if ($ValidateOnly) {
    $script:scenarioPath = Join-Path $tmpDir "validate-scenarios.txt"
    $script:logPath = Join-Path $tmpDir "validate-multi.log"
    Initialize-Evidence
    exit (Assert-ValidateOnly)
}

$exitCode = 1
Initialize-Evidence
try {
    Write-Section "Preflight"
    $parameterProblems = @(Test-Parameters)
    if ($parameterProblems.Count -gt 0) { throw ($parameterProblems -join "; ") }
    $script:goExe = Resolve-GoExe
    $script:psqlExe = Resolve-PsqlExe
    $script:nodeExe = Resolve-NodeExe
    $script:curlExe = Resolve-CurlExe
    if ([string]::IsNullOrWhiteSpace($script:goExe)) { throw "go executable not found" }
    if ([string]::IsNullOrWhiteSpace($script:psqlExe)) { throw "psql executable not found" }
    if ([string]::IsNullOrWhiteSpace($script:nodeExe)) { throw "node executable not found" }
    if ([string]::IsNullOrWhiteSpace($script:curlExe)) { throw "curl.exe not found" }
    $null = Resolve-DatabaseConfig
    foreach ($key in @("EZTEST_API_PORT", "EZTEST_DATABASE_URL", "EZTEST_WEB_PORT", "EZTEST_EXECUTOR_PORT",
            "EZTEST_EXECUTOR_TOKEN", "EZTEST_EXECUTOR_ADDR", "EZTEST_EXECUTOR_MAX_CONTEXTS",
            "EZTEST_EXECUTOR_HEARTBEAT_SECONDS", "EZTEST_API_URL", "FIXTURE_PORT", "FIXTURE_VARIANT")) {
        $script:previousEnv[$key] = [Environment]::GetEnvironmentVariable($key)
    }
    Write-Log ("database " + $DatabaseName + " configured (URL redacted); ports " + $ApiPort1 + "/" + $ApiPort2 +
        " api, " + $ExecutorPortA + "/" + $ExecutorPortB + " executor")

    Assert-NoLiveForeignReplica
    Initialize-Database
    Prepare-DataRoot
    Build-ApiBinary

    Write-Section "Start API replica A (node 1)"
    Start-ApiAProcess
    $script:token = Login-Admin -Port $ApiPort1
    if ([string]::IsNullOrWhiteSpace($script:token)) { throw "admin login failed on api A" }
    $nodeReady = Wait-SqlValue -Sql ("SELECT node_id::text FROM api_instance WHERE api_port = " + $ApiPort1 + " AND status = 'active';") -Want "1" -TimeoutSeconds 30
    if (-not $nodeReady) { throw "api A did not register node id 1" }
    $leaderPids = @()
    $leaderDeadline = (Get-Date).AddSeconds(30)
    while ((Get-Date) -lt $leaderDeadline) {
        $leaderPids = @(Get-LeaderLockPids)
        if ($leaderPids.Count -eq 1) { break }
        Start-Sleep -Milliseconds 500
    }
    if ($leaderPids.Count -ne 1) { throw "api A did not acquire the leader advisory lock" }
    $script:leaderPidBeforeStop = [string]$leaderPids[0]
    Write-Log ("api A node=1 and holds the leader advisory lock (pg pid " + $script:leaderPidBeforeStop + ")")

    try {
        $null = Set-SettingApi -Key "runtime.snowflake_node_id" -Value 2 -Port $ApiPort1
    } catch {
        throw ("could not set preferred snowflake node 2 for B: " + $_.Exception.Message)
    }

    Write-Section "Start API replica B (node 2)"
    Start-ApiBProcess
    $nodeBReady = Wait-SqlValue -Sql ("SELECT node_id::text FROM api_instance WHERE api_port = " + $ApiPort2 + " AND status = 'active';") -Want "2" -TimeoutSeconds 30
    if (-not $nodeBReady) { throw "api B did not register node id 2" }

    Write-Section "Start real executors"
    foreach ($port in @($ExecutorPortA, $ExecutorPortB)) {
        if (Test-PortListening -Port $port) {
            Write-Log ("stopping stale listener on executor port " + $port) "Yellow"
            Stop-PortProcesses -Port $port
            $null = Wait-PortClosed -Port $port -TimeoutSeconds 10
        }
    }
    $script:executorA = Start-ExecutorProcess -Port $ExecutorPortA -Name "executor-a" -LogName "executor-a"
    $script:executorB = Start-ExecutorProcess -Port $ExecutorPortB -Name "executor-b" -LogName "executor-b"
    Ensure-ExecutorRoster
    $script:duplicateBaseline = Get-DuplicateSnapshot
    Write-Log ("duplicate baseline (pre-existing rows are not caused by this drill): work=" +
        $script:duplicateBaseline.Work + " case=" + $script:duplicateBaseline.Case +
        " agent=" + $script:duplicateBaseline.Agent)

    Invoke-Scenario "S8.1 dual replica startup" { Scenario-Startup }
    Invoke-Scenario "S8.2 settings hot reload" { Scenario-SettingsReload }
    Invoke-Scenario "S8.3 cross-instance SSE" { Scenario-Sse }
    Invoke-Scenario "S8.4 drain and rolling upgrade" { Scenario-DrainRolling }
    Invoke-Scenario "S8.5 hard-kill recovery" { Scenario-KillRestart }
    Invoke-Scenario "S8.6 real executor loss" { Scenario-ExecutorLoss }

    $exitCode = 0
    if ($script:failCount -gt 0) { $exitCode = 1 }
} catch {
    Add-Check $false "drill execution aborted" $_.Exception.Message
    Write-Log ($_.ScriptStackTrace) "DarkYellow"
    if ($null -ne $script:apiA) { Show-LogTail -Path $script:apiA.ErrorLog }
    if ($null -ne $script:apiB) { Show-LogTail -Path $script:apiB.ErrorLog }
    if ($null -ne $script:executorA) { Show-LogTail -Path $script:executorA.ErrorLog }
    if ($null -ne $script:executorB) { Show-LogTail -Path $script:executorB.ErrorLog }
    $exitCode = 1
} finally {
    try { Cleanup } catch { Write-Log ("cleanup error: " + $_.Exception.Message) "Yellow" }
    $summaryExit = Write-Summary
    if ($exitCode -eq 0) { $exitCode = $summaryExit }
}
exit $exitCode
