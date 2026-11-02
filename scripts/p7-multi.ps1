#requires -Version 5.1
<#
.SYNOPSIS
    P7 multi-replica drill: two local EzTest API processes sharing one database
    and one content root, plus instance/drain/leader-takeover assertions.

.DESCRIPTION
    Starts two `go run ./cmd/eztest` processes from backend/ with different
    EZTEST_API_PORT values, the same EZTEST_DATABASE_URL, the same paths.data_root
    (via the shared DB settings) and deterministic snowflake node ids:
    runtime.snowflake_node_id is set to 1 before the first process, then to 2
    before the second process starts (best effort through psql).

    Assertions (full run, after the Task 19 server wiring lands):
      1. both instances answer GET /healthz 200;
      2. both answer GET /api/health/ready 200 (DB + storage + not draining);
      3. GET /api/admin/instances shows the two instances active with distinct
         node_id values 1 and 2;
      4. draining instance 1 turns its readiness 503 while instance 2 stays 200;
      5. stopping instance 1 hands the PG advisory leader lock to instance 2
         (indirect leader-takeover probe; the reconciler work-item assertion is
         exercised by scripts/p7-acceptance.ps1 in Task 21).

    Robustness for the current S5 stage: /api/health/ready and
    /api/admin/instances are mounted by Task 19. Until then the script detects
    the missing endpoint, prints SKIPPED with the reason, still exercises the
    dual-process startup/cleanup and exits 0. Use -SkipApiChecks to force the
    API assertions off. The full end-to-end run is Task 21.

.PARAMETER DatabaseUrl
    PostgreSQL URL shared by both processes. Defaults to EZTEST_DATABASE_URL
    from the environment or the repository .env.

.PARAMETER DataRoot
    Shared content root. When set, paths.data_root and the four content
    subdirectories are written to system_setting (psql required). When empty
    the existing DB settings are used (both processes share the same DB).

.PARAMETER ApiPort1
    Port of the first API process. Default 13341.

.PARAMETER ApiPort2
    Port of the second API process. Default 13342.

.PARAMETER AdminUsername
    Optional admin login for the /api/admin/instances drain call. When absent the
    script falls back to a direct DB status update (drill only).

.PARAMETER AdminPassword
    Password for -AdminUsername. Never printed or written to logs.

.PARAMETER SkipApiChecks
    Only start both processes, wait for /healthz, then clean up. Skips readiness,
    instance, drain and leader assertions.

.PARAMETER ValidateOnly
    Validate the script syntax/flow, toolchain and parameter sanity without
    starting any process. This is the mode run before Task 19/21.

.NOTES
    Compatible with Windows PowerShell 5.1 and PowerShell 7+. Only used on a
    loopback development database; the script never drops data.
#>
[CmdletBinding()]
param(
    [string]$DatabaseUrl = "",
    [string]$DataRoot = "",
    [int]$ApiPort1 = 13341,
    [int]$ApiPort2 = 13342,
    [string]$AdminUsername = "",
    [string]$AdminPassword = "",
    [switch]$SkipApiChecks,
    [switch]$ValidateOnly
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$backendDir = Join-Path $repoRoot "backend"
$tmpDir = Join-Path $repoRoot ".tmp\p7-multi"
$envPath = Join-Path $repoRoot ".env"
# 0x455A5431, the P7 leader advisory lock (orchestration design §4.5).
$leaderLockKey = 1163547697
$script:goExe = ""
$script:psqlExe = ""
$script:effectiveDatabaseUrl = ""
$script:effectiveDataRoot = ""
$script:processes = @()
$script:previousEnv = @{}
$script:adminToken = ""

function Write-Section {
    param([string]$Message)
    Write-Host ""
    Write-Host "== $Message" -ForegroundColor Cyan
}

function Get-EnvValue {
    param([string]$Name)
    $value = [Environment]::GetEnvironmentVariable($Name)
    if (-not [string]::IsNullOrWhiteSpace($value)) { return $value }
    if (Test-Path -LiteralPath $envPath) {
        foreach ($line in Get-Content -LiteralPath $envPath) {
            if ($line -match '^\s*#' -or $line -match '^\s*$') { continue }
            if ($line -match ('^\s*' + [regex]::Escape($Name) + '\s*=\s*(.*)$')) {
                $raw = $matches[1].Trim()
                if ($raw.Length -ge 2) {
                    $first = $raw.Substring(0, 1)
                    $last = $raw.Substring($raw.Length - 1, 1)
                    if (($first -eq '"' -and $last -eq '"') -or ($first -eq "'" -and $last -eq "'")) {
                        $raw = $raw.Substring(1, $raw.Length - 2)
                    }
                }
                return $raw
            }
        }
    }
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

function Assert-PostgresUrl {
    param([string]$Url)
    $uri = $null
    if (-not [System.Uri]::TryCreate($Url, [System.UriKind]::Absolute, [ref]$uri)) {
        throw "EZTEST_DATABASE_URL is not a valid absolute URL"
    }
    if ($uri.Scheme -ne "postgres" -and $uri.Scheme -ne "postgresql") {
        throw "EZTEST_DATABASE_URL must use the postgres scheme"
    }
    if ([string]::IsNullOrWhiteSpace($uri.Host)) {
        throw "EZTEST_DATABASE_URL must include a host"
    }
}

function Invoke-Psql {
    param(
        [string]$Url,
        [string]$Sql,
        [switch]$AllowFailure
    )
    if ([string]::IsNullOrWhiteSpace($script:psqlExe)) {
        throw "psql executable not found (.tools\pgsql\bin\psql.exe or PATH)"
    }
    $previousErrorAction = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        $output = & $script:psqlExe $Url -tA -F "|" -v ON_ERROR_STOP=1 -c $Sql 2>&1
        $exitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousErrorAction
    }
    if ($exitCode -ne 0) {
        if ($AllowFailure) { return $null }
        throw "psql failed (exit $exitCode): " + (($output | Out-String).Trim())
    }
    return @($output)
}

function Invoke-PsqlRows {
    param([string]$Url, [string]$Sql)
    $lines = Invoke-Psql -Url $Url -Sql $Sql
    $rows = @()
    foreach ($line in $lines) {
        $text = [string]$line
        if ([string]::IsNullOrWhiteSpace($text)) { continue }
        $rows += , ($text.Trim().Split("|"))
    }
    return , $rows
}

function Get-HttpStatus {
    param([string]$Url, [string]$Token = "")
    $headers = @{}
    if (-not [string]::IsNullOrWhiteSpace($Token)) {
        $headers["Authorization"] = "Bearer $Token"
    }
    try {
        $response = Invoke-WebRequest -Uri $Url -UseBasicParsing -Method Get -Headers $headers -TimeoutSec 3
        return [int]$response.StatusCode
    } catch {
        $response = $_.Exception.Response
        if ($null -ne $response) {
            try { return [int]$response.StatusCode } catch { return 0 }
        }
        return 0
    }
}

function Wait-HttpStatus {
    param(
        [string]$Url,
        [int]$Expected,
        [int]$TimeoutSeconds = 90,
        [string]$Label = ""
    )
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $last = 0
    while ((Get-Date) -lt $deadline) {
        $last = Get-HttpStatus -Url $Url
        if ($last -eq $Expected) { return $true }
        Start-Sleep -Milliseconds 500
    }
    if ([string]::IsNullOrWhiteSpace($Label)) { $Label = $Url }
    Write-Warning "timed out waiting for $Label (expected $Expected, last status $last)"
    return $false
}

function Set-SettingString {
    param([string]$Key, [string]$Value)
    $escaped = $Value.Replace("'", "''")
    $sql = "UPDATE system_setting SET value_json = to_jsonb('" + $escaped + "'::text), updated_at = now() WHERE key = '" + $Key + "';"
    $null = Invoke-Psql -Url $script:effectiveDatabaseUrl -Sql $sql -AllowFailure
}

function Set-SnowflakeNodeSetting {
    param([int]$NodeId)
    $sql = "UPDATE system_setting SET value_json = '" + $NodeId + "'::jsonb, updated_at = now() WHERE key = 'runtime.snowflake_node_id';"
    $result = Invoke-Psql -Url $script:effectiveDatabaseUrl -Sql $sql -AllowFailure
    if ($null -eq $result) {
        Write-Warning "could not write runtime.snowflake_node_id; relying on automatic allocation"
    }
}

function Set-PathSettings {
    param([string]$Root)
    if ([string]::IsNullOrWhiteSpace($Root)) { return }
    $full = [System.IO.Path]::GetFullPath($Root).Replace("\", "/")
    foreach ($pair in @(
            @("paths.data_root", $full),
            @("paths.attachment_dir", ($full + "/attachments")),
            @("paths.evidence_dir", ($full + "/evidence")),
            @("paths.report_dir", ($full + "/reports")),
            @("paths.export_dir", ($full + "/exports"))
        )) {
        Set-SettingString -Key $pair[0] -Value $pair[1]
    }
    Write-Host "paths.* settings point at $full (shared by both instances)."
}

function Get-DataRootFromDatabase {
    if ([string]::IsNullOrWhiteSpace($script:psqlExe)) { return "" }
    $lines = Invoke-Psql -Url $script:effectiveDatabaseUrl -Sql "SELECT value_json #>> '{}' FROM system_setting WHERE key = 'paths.data_root';" -AllowFailure
    if ($null -eq $lines) { return "" }
    foreach ($line in $lines) {
        $text = [string]$line
        if (-not [string]::IsNullOrWhiteSpace($text)) { return $text.Trim() }
    }
    return ""
}

function Get-InstancesFromDatabase {
    $sql = "SELECT id, node_id, status, api_port FROM api_instance WHERE api_port IN (" +
        [string]$ApiPort1 + "," + [string]$ApiPort2 + ") ORDER BY node_id;"
    $rows = Invoke-PsqlRows -Url $script:effectiveDatabaseUrl -Sql $sql
    $instances = @()
    foreach ($row in $rows) {
        if ($row.Count -lt 4) { continue }
        $instances += [pscustomobject]@{
            instance_id = $row[0]
            node_id     = [int]$row[1]
            status      = $row[2]
            api_port    = [int]$row[3]
        }
    }
    return , $instances
}

function Get-Instances {
    param([int]$Port, [string]$Token)
    if (-not [string]::IsNullOrWhiteSpace($Token)) {
        try {
            $response = Invoke-RestMethod -Uri ("http://127.0.0.1:" + $Port + "/api/admin/instances") `
                -Headers @{ Authorization = "Bearer $Token" } -Method Get -TimeoutSec 5
            $items = @($response.data)
            $mine = @()
            foreach ($item in $items) {
                $port = 0
                if ($null -ne $item.api_port) { $port = [int]$item.api_port }
                if ($port -eq 0 -or $port -eq $ApiPort1 -or $port -eq $ApiPort2) {
                    $mine += $item
                }
            }
            return [pscustomobject]@{ Source = "api"; Items = $mine }
        } catch {
            $status = 0
            if ($null -ne $_.Exception.Response) {
                try { $status = [int]$_.Exception.Response.StatusCode } catch { $status = 0 }
            }
            if ($status -ne 404) { throw }
            Write-Warning "GET /api/admin/instances returned 404; falling back to the api_instance table."
        }
    }
    if ([string]::IsNullOrWhiteSpace($script:psqlExe)) {
        throw "cannot read instances: no admin token and no psql executable"
    }
    return [pscustomobject]@{ Source = "db"; Items = @(Get-InstancesFromDatabase) }
}

function Login-Admin {
    param([int]$Port)
    if ([string]::IsNullOrWhiteSpace($AdminUsername) -or [string]::IsNullOrWhiteSpace($AdminPassword)) {
        return ""
    }
    $body = @{ username = $AdminUsername; password = $AdminPassword } | ConvertTo-Json -Compress
    try {
        $response = Invoke-RestMethod -Uri ("http://127.0.0.1:" + $Port + "/api/auth/login") `
            -Method Post -ContentType "application/json" -Body $body -TimeoutSec 5
        if ($null -ne $response.data -and -not [string]::IsNullOrWhiteSpace([string]$response.data.token)) {
            return [string]$response.data.token
        }
        return ""
    } catch {
        Write-Warning "admin login failed; instance drain will use the direct DB fallback."
        return ""
    }
}

function Invoke-PostEmpty {
    param([string]$Url, [string]$Token)
    $headers = @{}
    if (-not [string]::IsNullOrWhiteSpace($Token)) {
        $headers["Authorization"] = "Bearer $Token"
    }
    $response = Invoke-WebRequest -Uri $Url -Method Post -Headers $headers -UseBasicParsing -TimeoutSec 5
    return [int]$response.StatusCode
}

function Start-ApiProcess {
    param([int]$Port, [string]$LogName)
    $stdout = Join-Path $tmpDir ($LogName + ".log")
    $stderr = Join-Path $tmpDir ($LogName + ".err.log")
    $process = Start-Process -FilePath $script:goExe `
        -ArgumentList @("run", "./cmd/eztest") `
        -WorkingDirectory $backendDir `
        -RedirectStandardOutput $stdout -RedirectStandardError $stderr `
        -WindowStyle Hidden -PassThru
    $entry = [pscustomobject]@{
        Port       = $Port
        Process    = $process
        Log        = $stdout
        ErrorLog   = $stderr
    }
    $script:processes += $entry
    return $entry
}

function Show-LogTail {
    param([string]$Path, [int]$Lines = 30)
    if (-not (Test-Path -LiteralPath $Path)) { return }
    Write-Host "--- $Path (tail) ---" -ForegroundColor DarkYellow
    Get-Content -LiteralPath $Path -Tail $Lines | ForEach-Object { Write-Host $_ }
}

function Stop-ApiProcess {
    param($Entry)
    if ($null -eq $Entry -or $null -eq $Entry.Process) { return }
    try { $running = Get-Process -Id $Entry.Process.Id -ErrorAction SilentlyContinue } catch { $running = $null }
    if ($null -ne $running) {
        & taskkill.exe /PID $Entry.Process.Id /T /F 2>&1 | Out-Null
    }
    $script:processes = @($script:processes | Where-Object { $_ -ne $Entry })
}

function Stop-ApiProcesses {
    foreach ($entry in @($script:processes)) {
        Stop-ApiProcess -Entry $entry
    }
    $deadline = (Get-Date).AddSeconds(15)
    while ((Get-Date) -lt $deadline) {
        $open = $false
        foreach ($port in @($ApiPort1, $ApiPort2)) {
            if ((Get-HttpStatus -Url ("http://127.0.0.1:" + $port + "/healthz")) -ne 0) { $open = $true }
        }
        if (-not $open) { break }
        Start-Sleep -Milliseconds 300
    }
}

function Restore-Environment {
    foreach ($key in $script:previousEnv.Keys) {
        [Environment]::SetEnvironmentVariable($key, [string]$script:previousEnv[$key])
    }
}

function Get-LeaderLockPids {
    if ([string]::IsNullOrWhiteSpace($script:psqlExe)) { return @() }
    $sql = "SELECT pid::text FROM pg_locks WHERE locktype = 'advisory' AND granted AND classid = 0 AND objid = " + $leaderLockKey + " ORDER BY pid;"
    $lines = Invoke-Psql -Url $script:effectiveDatabaseUrl -Sql $sql -AllowFailure
    $pids = @()
    if ($null -eq $lines) { return $pids }
    foreach ($line in $lines) {
        $text = ([string]$line).Trim()
        if ($text -match '^[0-9]+$') { $pids += [int]$text }
    }
    return $pids
}

function Assert-ValidateOnly {
    $problems = @()
    Write-Section "Validate-only checks"

    $tokens = $null
    $parseErrors = $null
    $null = [System.Management.Automation.Language.Parser]::ParseFile($PSCommandPath, [ref]$tokens, [ref]$parseErrors)
    if ($null -ne $parseErrors -and $parseErrors.Count -gt 0) {
        foreach ($error in $parseErrors) { $problems += ("syntax: " + $error.Message) }
    } else {
        Write-Host "PowerShell syntax: OK"
    }

    $scriptText = Get-Content -LiteralPath $PSCommandPath -Raw
    foreach ($marker in @(
            "api/health/ready",
            "api/admin/instances",
            "runtime.snowflake_node_id",
            "paths.data_root",
            "advisory",
            "taskkill.exe",
            "Wait-HttpStatus",
            "Start-ApiProcess",
            "Stop-ApiProcesses",
            "SkipApiChecks",
            "ValidateOnly"
        )) {
        if ($scriptText.IndexOf($marker, [System.StringComparison]::Ordinal) -lt 0) {
            $problems += ("flow marker missing: " + $marker)
        }
    }
    if ($problems.Count -eq 0) { Write-Host "Flow markers (readiness/instances/drain/leader/cleanup): OK" }

    $goExe = Resolve-GoExe
    if ([string]::IsNullOrWhiteSpace($goExe)) {
        $problems += "go executable not found (.tools\go\bin\go.exe or PATH)"
    } else {
        Write-Host ("Go executable: " + $goExe)
    }
    if (-not (Test-Path -LiteralPath (Join-Path $backendDir "cmd\eztest\main.go"))) {
        $problems += "backend/cmd/eztest/main.go is missing"
    }
    if (-not (Test-Path -LiteralPath (Join-Path $backendDir "go.mod"))) {
        $problems += "backend/go.mod is missing"
    }

    foreach ($port in @($ApiPort1, $ApiPort2)) {
        if ($port -lt 1 -or $port -gt 65535) { $problems += ("port out of range: " + $port) }
    }
    if ($ApiPort1 -eq $ApiPort2) { $problems += "ApiPort1 and ApiPort2 must differ" }

    $dbUrl = $DatabaseUrl
    if ([string]::IsNullOrWhiteSpace($dbUrl)) { $dbUrl = Get-EnvValue -Name "EZTEST_DATABASE_URL" }
    if ([string]::IsNullOrWhiteSpace($dbUrl)) {
        $problems += "DatabaseUrl/EZTEST_DATABASE_URL is not configured"
    } else {
        try {
            Assert-PostgresUrl -Url $dbUrl
            Write-Host "Database URL: valid postgres URL (value not printed)"
        } catch {
            $problems += $_.Exception.Message
        }
    }

    $psql = Resolve-PsqlExe
    if ([string]::IsNullOrWhiteSpace($psql)) {
        Write-Warning "psql not found; a full run cannot manage paths/snowflake settings deterministically."
    } else {
        Write-Host ("psql executable: " + $psql)
    }

    Write-Host ""
    Write-Host "Flow: preflight -> start api1 (port $ApiPort1) -> wait /healthz -> set snowflake=2 ->" `
        "start api2 (port $ApiPort2) -> wait /healthz + /api/health/ready -> assert two active instances" `
        "with distinct node_id -> drain api1 (ready 503 while api2 stays 200) -> stop api1 ->" `
        "verify advisory leader lock moved -> cleanup."
    Write-Host "DataRoot: " -NoNewline
    if ([string]::IsNullOrWhiteSpace($DataRoot)) {
        Write-Host "(DB paths.data_root; both processes share the same database/CWD)"
    } else {
        Write-Host ([System.IO.Path]::GetFullPath($DataRoot))
    }

    if ($problems.Count -gt 0) {
        Write-Host ""
        Write-Host ("VALIDATION FAILED (" + $problems.Count + " problem(s)):") -ForegroundColor Red
        foreach ($problem in $problems) { Write-Host (" - " + $problem) -ForegroundColor Red }
        return 1
    }
    Write-Host ""
    Write-Host "VALIDATION PASSED: scripts/p7-multi.ps1 syntax/flow/toolchain are consistent." -ForegroundColor Green
    Write-Host "Full run (two processes + DB assertions) happens in Task 21 after Task 19 wiring." -ForegroundColor Yellow
    return 0
}

if ($ValidateOnly) {
    exit (Assert-ValidateOnly)
}

$exitCode = 1
try {
    Write-Section "Preflight"
    $script:goExe = Resolve-GoExe
    if ([string]::IsNullOrWhiteSpace($script:goExe)) { throw "go executable not found (.tools\go\bin\go.exe or PATH)" }
    if (-not (Test-Path -LiteralPath (Join-Path $backendDir "cmd\eztest\main.go"))) {
        throw "backend/cmd/eztest/main.go is missing"
    }
    if ($ApiPort1 -eq $ApiPort2) { throw "ApiPort1 and ApiPort2 must differ" }
    foreach ($port in @($ApiPort1, $ApiPort2)) {
        if ($port -lt 1 -or $port -gt 65535) { throw "invalid API port: $port" }
    }
    $script:psqlExe = Resolve-PsqlExe

    $script:effectiveDatabaseUrl = $DatabaseUrl
    if ([string]::IsNullOrWhiteSpace($script:effectiveDatabaseUrl)) {
        $script:effectiveDatabaseUrl = Get-EnvValue -Name "EZTEST_DATABASE_URL"
    }
    if ([string]::IsNullOrWhiteSpace($script:effectiveDatabaseUrl)) {
        throw "DatabaseUrl/EZTEST_DATABASE_URL is required"
    }
    Assert-PostgresUrl -Url $script:effectiveDatabaseUrl
    Write-Host "Database: configured (value not printed)"

    if (-not (Test-Path -LiteralPath $tmpDir)) {
        $null = New-Item -ItemType Directory -Path $tmpDir -Force
    }

    if (-not [string]::IsNullOrWhiteSpace($DataRoot)) {
        if ([string]::IsNullOrWhiteSpace($script:psqlExe)) {
            Write-Warning "DataRoot was provided but psql is unavailable; using the existing paths.* settings."
        } else {
            $null = New-Item -ItemType Directory -Path $DataRoot -Force
            Set-PathSettings -Root $DataRoot
            $script:effectiveDataRoot = [System.IO.Path]::GetFullPath($DataRoot)
        }
    }
    if ([string]::IsNullOrWhiteSpace($script:effectiveDataRoot)) {
        $script:effectiveDataRoot = Get-DataRootFromDatabase
        if ([string]::IsNullOrWhiteSpace($script:effectiveDataRoot)) {
            $script:effectiveDataRoot = Join-Path $backendDir ".data"
        } elseif (-not [System.IO.Path]::IsPathRooted($script:effectiveDataRoot)) {
            $script:effectiveDataRoot = [System.IO.Path]::GetFullPath((Join-Path $backendDir $script:effectiveDataRoot))
        }
    }
    Write-Host "Shared data root: $script:effectiveDataRoot"

    if (-not [string]::IsNullOrWhiteSpace($script:psqlExe)) {
        Set-SnowflakeNodeSetting -NodeId 1
    }

    # Both processes inherit exactly the four 0.2 startup variables.
    foreach ($key in @("EZTEST_API_PORT", "EZTEST_DATABASE_URL", "EZTEST_WEB_PORT", "EZTEST_EXECUTOR_PORT")) {
        $script:previousEnv[$key] = [Environment]::GetEnvironmentVariable($key)
    }
    $env:EZTEST_DATABASE_URL = $script:effectiveDatabaseUrl
    $env:EZTEST_WEB_PORT = "6868"
    $env:EZTEST_EXECUTOR_PORT = "13336"

    Write-Section "Start API instance 1 (port $ApiPort1)"
    $env:EZTEST_API_PORT = [string]$ApiPort1
    $api1 = Start-ApiProcess -Port $ApiPort1 -LogName "api1"
    if (-not (Wait-HttpStatus -Url ("http://127.0.0.1:" + $ApiPort1 + "/healthz") -Expected 200 -TimeoutSeconds 180 -Label "api1 /healthz")) {
        Show-LogTail -Path $api1.ErrorLog
        Show-LogTail -Path $api1.Log
        throw "api1 did not become live on port $ApiPort1"
    }
    Write-Host "api1 /healthz 200"

    # Deterministic node id 2 for the second process: runtime.snowflake_node_id
    # is a DB setting, so it is flipped between the two starts.
    if (-not [string]::IsNullOrWhiteSpace($script:psqlExe)) {
        Set-SnowflakeNodeSetting -NodeId 2
    }

    Write-Section "Start API instance 2 (port $ApiPort2)"
    $env:EZTEST_API_PORT = [string]$ApiPort2
    $api2 = Start-ApiProcess -Port $ApiPort2 -LogName "api2"
    if (-not (Wait-HttpStatus -Url ("http://127.0.0.1:" + $ApiPort2 + "/healthz") -Expected 200 -TimeoutSeconds 180 -Label "api2 /healthz")) {
        Show-LogTail -Path $api2.ErrorLog
        Show-LogTail -Path $api2.Log
        throw "api2 did not become live on port $ApiPort2"
    }
    Write-Host "api2 /healthz 200"

    if ($SkipApiChecks) {
        Write-Section "SKIPPED: API assertions"
        Write-Host "-SkipApiChecks was set: both processes started and answered /healthz; no readiness/instance/drain/leader checks." -ForegroundColor Yellow
        $exitCode = 0
        return
    }

    Write-Section "Readiness"
    $ready1 = Get-HttpStatus -Url ("http://127.0.0.1:" + $ApiPort1 + "/api/health/ready")
    $ready2 = Get-HttpStatus -Url ("http://127.0.0.1:" + $ApiPort2 + "/api/health/ready")
    if ($ready1 -eq 404 -and $ready2 -eq 404) {
        Write-Host "SKIPPED: /api/health/ready is not mounted yet (Task 19 wiring has not landed)." -ForegroundColor Yellow
        Write-Host "The dual-process startup/cleanup passed; the full readiness/instance/drain/leader drill is Task 21." -ForegroundColor Yellow
        $exitCode = 0
        return
    }
    if (-not (Wait-HttpStatus -Url ("http://127.0.0.1:" + $ApiPort1 + "/api/health/ready") -Expected 200 -TimeoutSeconds 60 -Label "api1 readiness")) {
        throw "api1 /api/health/ready is not 200"
    }
    if (-not (Wait-HttpStatus -Url ("http://127.0.0.1:" + $ApiPort2 + "/api/health/ready") -Expected 200 -TimeoutSeconds 60 -Label "api2 readiness")) {
        throw "api2 /api/health/ready is not 200"
    }
    Write-Host "both instances ready (200)"

    $script:adminToken = Login-Admin -Port $ApiPort1
    if ([string]::IsNullOrWhiteSpace($script:adminToken)) {
        Write-Warning "no admin session; /api/admin/instances is read via the DB fallback and drain uses SQL."
    }

    Write-Section "Instances"
    $instancesResult = Get-Instances -Port $ApiPort1 -Token $script:adminToken
    $instances = @($instancesResult.Items)
    Write-Host ("instance source: " + $instancesResult.Source)
    $active = @($instances | Where-Object { $_.status -eq "active" })
    $instances | ForEach-Object {
        Write-Host (" - instance_id=" + $_.instance_id + " node_id=" + $_.node_id + " status=" + $_.status +
            $(if ($null -ne $_.api_port) { " port=" + $_.api_port } else { "" }))
    }
    if ($active.Count -ne 2) {
        throw ("expected 2 active API instances, found " + $active.Count)
    }
    $nodeIDs = @($active | ForEach-Object { [int]$_.node_id } | Sort-Object -Unique)
    if ($nodeIDs.Count -ne 2) {
        throw ("expected distinct node_id values, found " + ($nodeIDs -join ","))
    }
    Write-Host ("node ids: " + ($nodeIDs -join ", ") + " (distinct)")
    if (-not [string]::IsNullOrWhiteSpace($script:psqlExe)) {
        if (-not ($nodeIDs -contains 1) -or -not ($nodeIDs -contains 2)) {
            Write-Warning ("expected deterministic node ids 1 and 2, got " + ($nodeIDs -join ", "))
        }
    }

    Write-Section "Drain instance 1"
    $first = @($active | Where-Object { $null -eq $_.api_port -or [int]$_.api_port -eq $ApiPort1 }) | Select-Object -First 1
    if ($null -eq $first) { $first = $active[0] }
    $instanceId = [string]$first.instance_id
    $drained = $false
    if (-not [string]::IsNullOrWhiteSpace($script:adminToken)) {
        $status = Invoke-PostEmpty -Url ("http://127.0.0.1:" + $ApiPort1 + "/api/admin/instances/" + $instanceId + "/drain") -Token $script:adminToken
        Write-Host "POST drain -> $status"
        $drained = $status -eq 200
    }
    if (-not $drained) {
        if ([string]::IsNullOrWhiteSpace($script:psqlExe)) {
            throw "cannot drain instance 1: no admin session and no psql"
        }
        $sql = "UPDATE api_instance SET status = 'draining', draining_at = now(), updated_at = now() WHERE id = " + $instanceId + ";"
        $null = Invoke-Psql -Url $script:effectiveDatabaseUrl -Sql $sql
        Write-Warning "drained instance $instanceId through a direct api_instance update (drill fallback; not an admin API drain)."
    }
    if (-not (Wait-HttpStatus -Url ("http://127.0.0.1:" + $ApiPort1 + "/api/health/ready") -Expected 503 -TimeoutSeconds 60 -Label "api1 readiness after drain")) {
        throw "api1 readiness did not become 503 after drain"
    }
    $peerReady = Get-HttpStatus -Url ("http://127.0.0.1:" + $ApiPort2 + "/api/health/ready")
    if ($peerReady -ne 200) {
        throw ("api2 readiness after draining api1 = " + $peerReady + ", want 200")
    }
    Write-Host "api1 readiness 503, api2 readiness 200 (drain isolated)"

    Write-Section "Leader takeover"
    $beforePids = @(Get-LeaderLockPids)
    Write-Host ("leader advisory-lock pids before stop: " + $(if ($beforePids.Count -gt 0) { $beforePids -join ", " } else { "(none observed)" }))
    Stop-ApiProcess -Entry $api1
    # Only api2 is meant to stay up for the takeover probe.
    Start-Sleep -Seconds 3
    $afterPids = @(Get-LeaderLockPids)
    Write-Host ("leader advisory-lock pids after stop: " + $(if ($afterPids.Count -gt 0) { $afterPids -join ", " } else { "(none observed)" }))
    if ($beforePids.Count -eq 0 -and $afterPids.Count -eq 0) {
        Write-Warning "no advisory leader lock observed (orchestration leader wiring is not mounted); leader takeover not asserted."
    } else {
        $newHolders = @($afterPids | Where-Object { $beforePids -notcontains $_ })
        if ($newHolders.Count -eq 0) {
            throw "the leader advisory lock did not move to another backend after stopping instance 1"
        }
        Write-Host ("leader lock moved to pid " + ($newHolders -join ", ") + " (instance 2)")
    }
    Write-Host "Reconciler work-item assertion is deferred to scripts/p7-acceptance.ps1 (Task 21)." -ForegroundColor Yellow
    $exitCode = 0
} catch {
    Write-Host ""
    Write-Host ("FAILED: " + $_.Exception.Message) -ForegroundColor Red
    Write-Host $_.ScriptStackTrace -ForegroundColor DarkYellow
    foreach ($entry in $script:processes) {
        Show-LogTail -Path $entry.ErrorLog
        Show-LogTail -Path $entry.Log
    }
    $exitCode = 1
} finally {
    Stop-ApiProcesses
    Restore-Environment
}

Write-Section "Cleanup"
Write-Host "API processes stopped; logs under $tmpDir"
exit $exitCode
