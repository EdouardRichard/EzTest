#requires -Version 5.1
<#
.SYNOPSIS
    One-command development stack for EzTest: PostgreSQL + API + web + Playwright executor.

.DESCRIPTION
    Brings up everything a developer needs with a single command:
      - imports the gitignored repository .env (secrets are never printed),
      - starts the portable PostgreSQL cluster when it is the configured database,
      - builds and runs the Go API from backend/ (embedded migrations and the
        administrator seed run automatically on the first start),
      - runs the Next.js dev server from frontend/,
      - runs the Playwright executor from executor/.

    The three application processes run in the background. Their output goes to
    .tmp/dev/logs/{api,executor,web}.log (and *.err.log) and their pids are
    recorded in .tmp/dev/*.pid.json, so -Stop and -Status can manage the stack
    later without keeping this terminal open.

    Always open the printed web URL (default http://localhost:6868, derived from
    EZTEST_WEB_ORIGIN). The API accepts exactly one browser origin (CORS), so
    opening the same dev server via http://127.0.0.1:6868 cannot sign in; with the
    default Next.js dev settings it also blocked HMR/dev resources and left the UI
    stuck on the "正在验证会话…" (session verification) spinner.

    Startup waits for real readiness before reporting the URLs:
      - API /api/health with db=ok (liveness alone is not enough),
      - executor /healthz with a browserVersion (the browser must launch),
      - web /login returning 200.
    If any component fails, the already-started processes are stopped again and
    the recent log lines are printed.

    Use -Status to inspect a running stack and -Stop to shut it down.
    PostgreSQL is intentionally left running by -Stop; use scripts\db.ps1 stop
    when you also want to stop the database.

    Windows PowerShell 5.1 compatible.

.PARAMETER Stop
    Stop the API, executor and web processes started by this script.

.PARAMETER Status
    Print the recorded pids and probe each component without starting anything.
    Exits 0 only when every component is running and healthy.

.PARAMETER SkipInstall
    Do not run pnpm install automatically when frontend/ or executor/node_modules
    are missing (the script fails instead).

.PARAMETER Open
    Open the web UI in the default browser after the stack is healthy.

.EXAMPLE
    powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts\dev-all.ps1

    Starts the full development stack and prints the URLs.

.EXAMPLE
    powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts\dev-all.ps1 -Status

.EXAMPLE
    powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts\dev-all.ps1 -Stop

.NOTES
    Single-component alternatives: scripts\dev-api.ps1, scripts\dev-web.ps1 and
    scripts\dev-executor.ps1 (foreground, one process each). PostgreSQL:
    scripts\db.ps1 init|start|stop|status. E2E test stack: scripts\e2e.ps1.
#>
[CmdletBinding(DefaultParameterSetName = "Start")]
param(
    [Parameter(ParameterSetName = "Stop")]
    [switch]$Stop,

    [Parameter(ParameterSetName = "Status")]
    [switch]$Status,

    [Parameter(ParameterSetName = "Start")]
    [switch]$SkipInstall,

    [Parameter(ParameterSetName = "Start")]
    [switch]$Open
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$backendDir = Join-Path $repoRoot "backend"
$frontendDir = Join-Path $repoRoot "frontend"
$executorDir = Join-Path $repoRoot "executor"
$envPath = Join-Path $repoRoot ".env"
$stateDir = Join-Path $repoRoot ".tmp\dev"
$logDir = Join-Path $stateDir "logs"
$apiBinary = Join-Path $stateDir "eztest-dev.exe"
$dbScript = Join-Path $PSScriptRoot "db.ps1"
$script:Started = New-Object System.Collections.ArrayList

# ---------------------------------------------------------------------------
# Helpers (shared shapes with scripts\package\start-eztest.ps1)
# ---------------------------------------------------------------------------
function Import-DotEnv {
    param([string]$Path)
    foreach ($line in [System.IO.File]::ReadAllLines($Path, [System.Text.Encoding]::UTF8)) {
        $trimmed = $line.Trim()
        if ($trimmed.Length -eq 0 -or $trimmed.StartsWith("#")) { continue }
        if ($trimmed -match '^([^=]+)=(.*)$') {
            $rawKey = $matches[1]
            $rawValue = $matches[2]
            $key = $rawKey.Trim()
            if ($key -notmatch '^[A-Za-z_][A-Za-z0-9_]*$') { continue }
            $value = $rawValue.Trim()
            if ($value.Length -ge 2) {
                $first = $value.Substring(0, 1)
                $last = $value.Substring($value.Length - 1, 1)
                if (($first -eq '"' -and $last -eq '"') -or ($first -eq "'" -and $last -eq "'")) {
                    $value = $value.Substring(1, $value.Length - 2)
                }
            }
            [Environment]::SetEnvironmentVariable($key, $value, "Process")
        }
    }
}

function Get-EnvValue {
    param([string]$Name, [string]$Default = "")
    $value = [Environment]::GetEnvironmentVariable($Name, "Process")
    if ([string]::IsNullOrWhiteSpace($value)) { return $Default }
    return $value.Trim()
}

function ConvertTo-HostPort {
    param([string]$Value, [int]$DefaultPort, [string]$SettingName)
    $raw = "$Value".Trim()
    if ($raw.Length -eq 0) { return [pscustomobject]@{ Host = "127.0.0.1"; Port = $DefaultPort } }
    if ($raw -match '^\d+$') { return [pscustomobject]@{ Host = "127.0.0.1"; Port = [int]$raw } }
    $colon = $raw.LastIndexOf(":")
    if ($colon -lt 0) { throw "$SettingName must be host:port (got '$raw')" }
    $hostPart = $raw.Substring(0, $colon).Trim()
    $portPart = $raw.Substring($colon + 1).Trim()
    if ($hostPart.StartsWith("[") -and $hostPart.EndsWith("]")) {
        $hostPart = $hostPart.Substring(1, $hostPart.Length - 2)
    }
    if ($portPart -notmatch '^\d+$') { throw "$SettingName has an invalid port '$portPart'" }
    $port = [int]$portPart
    if ($port -lt 1 -or $port -gt 65535) { throw "$SettingName has an out-of-range port $port" }
    if ($hostPart.Length -eq 0) { $hostPart = "0.0.0.0" }
    return [pscustomobject]@{ Host = $hostPart; Port = $port }
}

function Get-ProbeHost {
    param([string]$BindHost)
    if ($BindHost -eq "" -or $BindHost -eq "0.0.0.0" -or $BindHost -eq "::" -or $BindHost -eq "*") { return "127.0.0.1" }
    return $BindHost
}

function Get-DatabaseEndpoint {
    param([string]$Url)
    $rest = $Url
    $scheme = $rest.IndexOf("://")
    if ($scheme -ge 0) { $rest = $rest.Substring($scheme + 3) }
    $slash = $rest.IndexOf("/")
    if ($slash -ge 0) { $rest = $rest.Substring(0, $slash) }
    $at = $rest.LastIndexOf("@")
    if ($at -ge 0) { $rest = $rest.Substring($at + 1) }
    if ($rest -match '^\[(.+)\]:(\d+)$') {
        return [pscustomobject]@{ Host = $matches[1]; Port = [int]$matches[2] }
    }
    $colon = $rest.LastIndexOf(":")
    if ($colon -gt 0) {
        $hostPart = $rest.Substring(0, $colon)
        $portPart = $rest.Substring($colon + 1)
        if ($hostPart.Length -gt 0 -and $portPart -match '^\d+$') {
            return [pscustomobject]@{ Host = $hostPart; Port = [int]$portPart }
        }
    }
    return [pscustomobject]@{ Host = $rest; Port = 5432 }
}

function Test-TcpEndpoint {
    param([string]$TargetHost, [int]$Port, [int]$TimeoutMs = 1500)
    $client = New-Object System.Net.Sockets.TcpClient
    try {
        $iar = $client.BeginConnect($TargetHost, $Port, $null, $null)
        if (-not $iar.AsyncWaitHandle.WaitOne($TimeoutMs, $false)) { return $false }
        $client.EndConnect($iar)
        return $true
    } catch {
        return $false
    } finally {
        $client.Close()
    }
}

function Invoke-RawHttp {
    param([string]$Url, [int]$TimeoutSec = 5)
    $request = [System.Net.HttpWebRequest]::Create($Url)
    $request.Method = "GET"
    $request.Timeout = $TimeoutSec * 1000
    $request.ReadWriteTimeout = $TimeoutSec * 1000
    $request.AllowAutoRedirect = $false
    $request.Proxy = $null
    $request.UserAgent = "EzTest-dev-all"
    $response = $null
    try {
        $response = $request.GetResponse()
    } catch [System.Net.WebException] {
        if ($null -eq $_.Exception.Response) { throw }
        $response = $_.Exception.Response
    }
    try {
        $status = [int]$response.StatusCode
        $reader = New-Object System.IO.StreamReader($response.GetResponseStream(), [System.Text.Encoding]::UTF8)
        try { $body = $reader.ReadToEnd() } finally { $reader.Close() }
        return [pscustomobject]@{ Status = $status; Body = $body }
    } finally {
        $response.Close()
    }
}

function Get-ProcessLogPaths {
    param([string]$Name)
    return [pscustomobject]@{
        Out = Join-Path $logDir ($Name + ".log")
        Err = Join-Path $logDir ($Name + ".err.log")
    }
}

function Rotate-Log {
    param([string]$Path)
    if (Test-Path -LiteralPath $Path -PathType Leaf) {
        Move-Item -LiteralPath $Path -Destination ($Path + ".prev") -Force
    }
}

function Show-LogTail {
    param([string]$Path, [int]$Lines = 15)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return }
    Write-Host "--- $Path (last $Lines lines) ---" -ForegroundColor DarkGray
    $tail = Get-Content -LiteralPath $Path -Tail $Lines -ErrorAction SilentlyContinue
    foreach ($line in $tail) {
        if ($null -eq $line) { continue }
        Write-Host ("  " + ($line -replace '[A-Za-z0-9+/=]{40,}', '<redacted>'))
    }
}

function Get-LogHint {
    param([string]$Name)
    $logs = Get-ProcessLogPaths -Name $Name
    $lines = @()
    if (Test-Path -LiteralPath $logs.Err -PathType Leaf) {
        $lines += (Get-Content -LiteralPath $logs.Err -Tail 10 -ErrorAction SilentlyContinue)
    }
    if (Test-Path -LiteralPath $logs.Out -PathType Leaf) {
        $lines += (Get-Content -LiteralPath $logs.Out -Tail 10 -ErrorAction SilentlyContinue)
    }
    $rendered = New-Object System.Collections.ArrayList
    foreach ($line in $lines) {
        if ($null -eq $line) { continue }
        [void]$rendered.Add("  " + ($line -replace '[A-Za-z0-9+/=]{40,}', "<redacted>"))
    }
    if ($rendered.Count -eq 0) { return "No log output was written; see $logDir." }
    return "Recent log lines:" + [Environment]::NewLine + ($rendered -join [Environment]::NewLine)
}

function Get-StatePath {
    param([string]$Name)
    return Join-Path $stateDir ($Name + ".pid.json")
}

function Save-State {
    param([string]$Name, [System.Diagnostics.Process]$Process)
    $state = [pscustomobject]@{
        pid        = $Process.Id
        name       = $Process.ProcessName
        started_at = (Get-Date).ToString("o")
    }
    $state | ConvertTo-Json -Compress | Set-Content -LiteralPath (Get-StatePath $Name) -Encoding ASCII
}

function Get-RecordedEntry {
    param([string]$Name)
    $path = Get-StatePath $Name
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { return $null }
    $state = $null
    try {
        $state = Get-Content -LiteralPath $path -Raw | ConvertFrom-Json
    } catch {
        $state = $null
    }
    if ($null -eq $state -or $null -eq $state.pid) {
        Remove-Item -LiteralPath $path -Force -ErrorAction SilentlyContinue
        return $null
    }
    $process = Get-Process -Id ([int]$state.pid) -ErrorAction SilentlyContinue
    if ($null -eq $process) {
        Remove-Item -LiteralPath $path -Force -ErrorAction SilentlyContinue
        return $null
    }
    return [pscustomobject]@{ Name = $Name; State = $state; Process = $process }
}

function Stop-ProcessTree {
    param([int]$ProcessId)
    try {
        & taskkill.exe /PID $ProcessId /T /F 2>$null | Out-Null
    } catch {
        try { Stop-Process -Id $ProcessId -Force -ErrorAction SilentlyContinue } catch { }
    }
}

function Stop-NamedComponent {
    param([string]$Name)
    $entry = Get-RecordedEntry -Name $Name
    if ($null -eq $entry) { return $false }
    if ($entry.Process.ProcessName -ne $entry.State.name) {
        Write-Warning ("Skipping {0}: pid {1} now belongs to '{2}' (stale state file)." -f $Name, $entry.State.pid, $entry.Process.ProcessName)
        Remove-Item -LiteralPath (Get-StatePath $Name) -Force -ErrorAction SilentlyContinue
        return $false
    }
    Write-Host ("Stopping {0} (pid {1})..." -f $Name, $entry.State.pid) -ForegroundColor DarkGray
    Stop-ProcessTree -ProcessId ([int]$entry.State.pid)
    $deadline = (Get-Date).AddSeconds(10)
    while ((Get-Date) -lt $deadline) {
        if ($null -eq (Get-Process -Id ([int]$entry.State.pid) -ErrorAction SilentlyContinue)) { break }
        Start-Sleep -Milliseconds 200
    }
    Remove-Item -LiteralPath (Get-StatePath $Name) -Force -ErrorAction SilentlyContinue
    return $true
}

function Start-Component {
    param(
        [string]$Name,
        [string]$FilePath,
        [string[]]$ArgumentList,
        [string]$WorkingDirectory
    )
    $logs = Get-ProcessLogPaths -Name $Name
    Rotate-Log -Path $logs.Out
    Rotate-Log -Path $logs.Err
    if ($ArgumentList.Count -eq 0) {
        $process = Start-Process -FilePath $FilePath -WorkingDirectory $WorkingDirectory -WindowStyle Hidden `
            -RedirectStandardOutput $logs.Out -RedirectStandardError $logs.Err -PassThru
    } else {
        $process = Start-Process -FilePath $FilePath -ArgumentList $ArgumentList -WorkingDirectory $WorkingDirectory -WindowStyle Hidden `
            -RedirectStandardOutput $logs.Out -RedirectStandardError $logs.Err -PassThru
    }
    Save-State -Name $Name -Process $process
    [void]$script:Started.Add($Name)
    Write-Host ("     {0,-9} started (pid {1})" -f $Name, $process.Id) -ForegroundColor DarkGray
    return $process
}

function Stop-StartedComponents {
    # Rollback for a failed start: stop only what this invocation started, in
    # reverse start order (web, executor, then the API).
    $any = $false
    foreach ($name in @($script:Started)) {
        if ($name -eq "api") { continue }
        if (Stop-NamedComponent -Name $name) { $any = $true }
    }
    if ($script:Started -contains "api") {
        if (Stop-NamedComponent -Name "api") { $any = $true }
    }
    $script:Started.Clear()
    return $any
}

function Stop-RecordedComponents {
    # -Stop: stop every component that has a live pid record, regardless of who
    # started it (each invocation of this script is a separate process).
    $any = $false
    foreach ($name in @("web", "executor", "api")) {
        if (Stop-NamedComponent -Name $name) { $any = $true }
    }
    return $any
}

function Wait-ApiReady {
    param([string]$BaseUrl, [System.Diagnostics.Process]$Process, [int]$Seconds = 180)
    $deadline = (Get-Date).AddSeconds($Seconds)
    $lastDetail = "no response"
    while ((Get-Date) -lt $deadline) {
        try { $Process.Refresh() } catch { }
        if ($Process.HasExited) {
            throw "The API exited during startup (exit code $($Process.ExitCode)).$([Environment]::NewLine)$(Get-LogHint api)"
        }
        try {
            $healthz = Invoke-RawHttp -Url "$BaseUrl/healthz" -TimeoutSec 3
            if ($healthz.Status -eq 200) {
                $ready = Invoke-RawHttp -Url "$BaseUrl/api/health" -TimeoutSec 5
                if ($ready.Status -eq 200) {
                    $json = $ready.Body | ConvertFrom-Json
                    if ($null -ne $json.data -and $json.data.db -eq "ok") { return $json.data }
                    $lastDetail = "/api/health db=$($json.data.db) status=$($json.data.status)"
                } else {
                    $lastDetail = "/api/health returned HTTP $($ready.Status)"
                }
            } else {
                $lastDetail = "/healthz returned HTTP $($healthz.Status)"
            }
        } catch {
            $lastDetail = "connection not ready"
        }
        Start-Sleep -Milliseconds 500
    }
    throw "The API did not become healthy within $Seconds seconds (last: $lastDetail).$([Environment]::NewLine)$(Get-LogHint api)"
}

function Wait-ExecutorReady {
    param([string]$BaseUrl, [System.Diagnostics.Process]$Process, [int]$Seconds = 180)
    $deadline = (Get-Date).AddSeconds($Seconds)
    $lastDetail = "no response"
    while ((Get-Date) -lt $deadline) {
        try { $Process.Refresh() } catch { }
        if ($Process.HasExited) {
            throw "The executor exited during startup (exit code $($Process.ExitCode)).$([Environment]::NewLine)$(Get-LogHint executor)"
        }
        try {
            $response = Invoke-RawHttp -Url "$BaseUrl/healthz" -TimeoutSec 5
            if ($response.Status -eq 200) {
                $json = $response.Body | ConvertFrom-Json
                if ($json.status -eq "ok" -and -not [string]::IsNullOrWhiteSpace("$($json.browserVersion)")) {
                    return $json
                }
                $lastDetail = "status=$($json.status) browserVersion=$($json.browserVersion)"
            } else {
                $body = $response.Body
                if ($body.Length -gt 300) { $body = $body.Substring(0, 300) }
                $lastDetail = "HTTP $($response.Status) $body"
            }
        } catch {
            $lastDetail = "connection not ready"
        }
        Start-Sleep -Milliseconds 500
    }
    $message = "The Playwright executor did not become ready within $Seconds seconds (last: $lastDetail)." +
        "$([Environment]::NewLine)A Chromium-based browser is required: install Playwright Chromium (cd executor; pnpm exec playwright install chromium) or Google Chrome / Microsoft Edge.$([Environment]::NewLine)$(Get-LogHint executor)"
    throw $message
}

function Wait-WebReady {
    param([string]$BaseUrl, [System.Diagnostics.Process]$Process, [int]$Seconds = 240)
    $deadline = (Get-Date).AddSeconds($Seconds)
    $lastDetail = "no response"
    while ((Get-Date) -lt $deadline) {
        try { $Process.Refresh() } catch { }
        if ($Process.HasExited) {
            throw "The web server exited during startup (exit code $($Process.ExitCode)).$([Environment]::NewLine)$(Get-LogHint web)"
        }
        try {
            $response = Invoke-RawHttp -Url "$BaseUrl/login" -TimeoutSec 5
            if ($response.Status -eq 200) { return $response }
            $lastDetail = "HTTP $($response.Status)"
        } catch {
            $lastDetail = "connection not ready"
        }
        Start-Sleep -Milliseconds 500
    }
    throw "The web server did not answer /login within $Seconds seconds (last: $lastDetail).$([Environment]::NewLine)$(Get-LogHint web)"
}

function Wait-DatabaseReady {
    param([string]$TargetHost, [int]$Port, [int]$Seconds = 15)
    $deadline = (Get-Date).AddSeconds($Seconds)
    while ((Get-Date) -lt $deadline) {
        if (Test-TcpEndpoint -TargetHost $TargetHost -Port $Port) { return }
        Start-Sleep -Milliseconds 300
    }
    throw "PostgreSQL is not reachable at ${TargetHost}:${Port}. Start it with scripts\db.ps1 start (or fix EZTEST_DATABASE_URL in .env)."
}

function Get-WebPort {
    $packageJson = Join-Path $frontendDir "package.json"
    if (Test-Path -LiteralPath $packageJson -PathType Leaf) {
        try {
            $devScript = (Get-Content -LiteralPath $packageJson -Raw | ConvertFrom-Json).scripts.dev
            if ("$devScript" -match '-p\s+(\d+)') { return [int]$matches[1] }
        } catch { }
    }
    return 6868
}

function Get-CommandPath {
    param([string]$Name)
    $command = Get-Command $Name -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($null -eq $command) { return $null }
    if (-not [string]::IsNullOrWhiteSpace($command.Source)) { return $command.Source }
    return $command.Path
}

# ---------------------------------------------------------------------------
# Load .env when present (also for -Status so the probes match the stack).
# ---------------------------------------------------------------------------
if (Test-Path -LiteralPath $envPath -PathType Leaf) {
    Import-DotEnv -Path $envPath
}

if ([string]::IsNullOrWhiteSpace($env:EZTEST_OPENAPI_PATH)) {
    $env:EZTEST_OPENAPI_PATH = Join-Path $repoRoot "docs\openapi\api.yaml"
}

$apiEndpoint = ConvertTo-HostPort -Value (Get-EnvValue -Name "EZTEST_HTTP_ADDR" -Default ":13333") -DefaultPort 13333 -SettingName "EZTEST_HTTP_ADDR"
$executorEndpoint = ConvertTo-HostPort -Value (Get-EnvValue -Name "EZTEST_EXECUTOR_ADDR" -Default "127.0.0.1:13336") -DefaultPort 13336 -SettingName "EZTEST_EXECUTOR_ADDR"
$webPort = Get-WebPort
$apiBaseUrl = "http://$(Get-ProbeHost -BindHost $apiEndpoint.Host):$($apiEndpoint.Port)"
$executorBaseUrl = "http://$(Get-ProbeHost -BindHost $executorEndpoint.Host):$($executorEndpoint.Port)"
$webProbeUrl = "http://127.0.0.1:$webPort"

# The API accepts exactly one browser origin: EZTEST_WEB_ORIGIN (default
# http://localhost:6868). The URL handed to the developer must use that origin's
# host. Opening the same dev server via 127.0.0.1 is blocked by the Next.js dev
# cross-origin protection (HMR/dev resources) and by API CORS, which leaves the
# SPA stuck on the "正在验证会话…" spinner, so always print/open the origin host.
$webOrigin = (Get-EnvValue -Name "EZTEST_WEB_ORIGIN" -Default "http://localhost:6868").TrimEnd("/")
$webBrowserHost = "localhost"
if ($webOrigin -match '^https?://([^/:]+)') { $webBrowserHost = $matches[1] }
$webBrowserUrl = "http://${webBrowserHost}:${webPort}"
$webOriginWarning = ""
if ($webOrigin -ne $webBrowserUrl) {
    $webOriginWarning = "EZTEST_WEB_ORIGIN is '$webOrigin' but the browser URL is '$webBrowserUrl'. The API only accepts the configured origin (CORS), so sign-in would fail. Set EZTEST_WEB_ORIGIN=$webBrowserUrl in .env and restart the API."
}

# ---------------------------------------------------------------------------
# -Status
# ---------------------------------------------------------------------------
if ($Status) {
    $healthy = $true
    Write-Host "EzTest dev stack status" -ForegroundColor Cyan

    $apiEntry = Get-RecordedEntry -Name "api"
    if ($null -eq $apiEntry) {
        Write-Host ("  {0,-9} not running" -f "api") -ForegroundColor Yellow
        $healthy = $false
    } else {
        $detail = "no response"
        try {
            $response = Invoke-RawHttp -Url "$apiBaseUrl/api/health" -TimeoutSec 3
            $json = $response.Body | ConvertFrom-Json
            if ($response.Status -eq 200 -and $json.data.db -eq "ok") {
                $detail = "healthy (db=ok, version=$($json.data.version))"
            } else {
                $detail = "unhealthy (HTTP $($response.Status), db=$($json.data.db))"
                $healthy = $false
            }
        } catch {
            $detail = "unreachable at $apiBaseUrl"
            $healthy = $false
        }
        Write-Host ("  {0,-9} pid {1,-7} {2}" -f "api", $apiEntry.State.pid, $detail)
    }

    $executorEntry = Get-RecordedEntry -Name "executor"
    if ($null -eq $executorEntry) {
        Write-Host ("  {0,-9} not running" -f "executor") -ForegroundColor Yellow
        $healthy = $false
    } else {
        $detail = "no response"
        try {
            $response = Invoke-RawHttp -Url "$executorBaseUrl/healthz" -TimeoutSec 3
            $json = $response.Body | ConvertFrom-Json
            if ($response.Status -eq 200 -and $json.status -eq "ok") {
                $detail = "healthy (browser=$($json.browserVersion))"
            } else {
                $detail = "degraded (HTTP $($response.Status), $($json.status))"
                $healthy = $false
            }
        } catch {
            $detail = "unreachable at $executorBaseUrl"
            $healthy = $false
        }
        Write-Host ("  {0,-9} pid {1,-7} {2}" -f "executor", $executorEntry.State.pid, $detail)
    }

    $webEntry = Get-RecordedEntry -Name "web"
    if ($null -eq $webEntry) {
        Write-Host ("  {0,-9} not running" -f "web") -ForegroundColor Yellow
        $healthy = $false
    } else {
        $detail = "no response"
        try {
            $response = Invoke-RawHttp -Url "$webProbeUrl/login" -TimeoutSec 5
            if ($response.Status -eq 200) {
                $detail = "healthy (HTTP 200 /login)"
            } else {
                $detail = "unhealthy (HTTP $($response.Status) /login)"
                $healthy = $false
            }
        } catch {
            $detail = "unreachable at $webProbeUrl"
            $healthy = $false
        }
        Write-Host ("  {0,-9} pid {1,-7} {2}" -f "web", $webEntry.State.pid, $detail)
    }

    $databaseUrl = Get-EnvValue -Name "EZTEST_DATABASE_URL"
    if ([string]::IsNullOrWhiteSpace($databaseUrl)) {
        Write-Host ("  {0,-9} EZTEST_DATABASE_URL is not set" -f "postgres") -ForegroundColor Yellow
        $healthy = $false
    } else {
        $dbEndpoint = Get-DatabaseEndpoint -Url $databaseUrl
        if (Test-TcpEndpoint -TargetHost $dbEndpoint.Host -Port $dbEndpoint.Port) {
            Write-Host ("  {0,-9} {1,-7} reachable at {2}:{3}" -f "postgres", "-", $dbEndpoint.Host, $dbEndpoint.Port)
        } else {
            Write-Host ("  {0,-9} {1,-7} unreachable at {2}:{3}" -f "postgres", "-", $dbEndpoint.Host, $dbEndpoint.Port) -ForegroundColor Yellow
            $healthy = $false
        }
    }

    Write-Host ""
    if ($healthy) {
        Write-Host "All components are healthy." -ForegroundColor Green
        exit 0
    }
    Write-Host "Some components are not healthy. Start the stack with scripts\dev-all.ps1." -ForegroundColor Yellow
    exit 1
}

# ---------------------------------------------------------------------------
# -Stop
# ---------------------------------------------------------------------------
if ($Stop) {
    Write-Host "Stopping the EzTest dev stack..." -ForegroundColor Cyan
    $stopped = Stop-RecordedComponents
    if ($stopped) {
        Write-Host "PostgreSQL is left running; stop it with scripts\db.ps1 stop when needed." -ForegroundColor DarkGray
    } else {
        Write-Host "No process started by scripts\dev-all.ps1 is running." -ForegroundColor DarkGray
    }
    exit 0
}

# ---------------------------------------------------------------------------
# Start (default parameter set)
# ---------------------------------------------------------------------------
try {
    Write-Host "EzTest dev stack" -ForegroundColor Cyan

    if (-not (Test-Path -LiteralPath $envPath -PathType Leaf)) {
        throw "Missing .env at $envPath. Copy .env.example to .env and fill in the placeholders (scripts\dev-api.ps1 has the same requirement)."
    }
    if (-not (Test-Path -LiteralPath $backendDir -PathType Container)) { throw "The backend/ directory is missing." }
    if (-not (Test-Path -LiteralPath $frontendDir -PathType Container)) { throw "The frontend/ directory is missing." }
    if (-not (Test-Path -LiteralPath $executorDir -PathType Container)) { throw "The executor/ directory is missing." }

    $databaseUrl = Get-EnvValue -Name "EZTEST_DATABASE_URL"
    if ([string]::IsNullOrWhiteSpace($databaseUrl) -or $databaseUrl.Contains("<")) {
        throw "EZTEST_DATABASE_URL is not configured in .env. Set it to a PostgreSQL 17 URL, for example: postgres://eztest:<password>@localhost:5433/eztest?sslmode=disable"
    }
    if ($databaseUrl -notmatch '^postgres(ql)?://') {
        throw "EZTEST_DATABASE_URL must start with postgres:// or postgresql://."
    }
    $databaseEndpoint = Get-DatabaseEndpoint -Url $databaseUrl

    # Existing stack / busy ports fail fast with a clear hint.
    foreach ($name in @("api", "executor", "web")) {
        $entry = Get-RecordedEntry -Name $name
        if ($null -ne $entry) {
            throw "$name is already running (pid $($entry.State.pid)). Use -Status to inspect it or -Stop to stop the stack first."
        }
    }
    $portOwners = @(
        @{ Name = "api"; Port = $apiEndpoint.Port; Setting = "EZTEST_HTTP_ADDR" },
        @{ Name = "executor"; Port = $executorEndpoint.Port; Setting = "EZTEST_EXECUTOR_ADDR" },
        @{ Name = "web"; Port = $webPort; Setting = "frontend/package.json (next dev -p)" }
    )
    foreach ($owner in $portOwners) {
        if (Test-TcpEndpoint -TargetHost "127.0.0.1" -Port ([int]$owner.Port)) {
            throw ("Port {0} ({1}) is already in use. Stop the other stack (for example scripts\dev-all.ps1 -Stop, or a foreground scripts\dev-api.ps1) or change {2}." -f $owner.Port, $owner.Name, $owner.Setting)
        }
    }

    # Dependencies.
    $nodeExe = Get-CommandPath -Name "node.exe"
    if ($null -eq $nodeExe) {
        $nodeExe = Get-CommandPath -Name "node"
    }
    if ($null -eq $nodeExe) { throw "Node.js is not available on PATH. Install Node.js 22+ and retry." }

    $pnpmExe = Get-CommandPath -Name "pnpm.cmd"
    if ($null -eq $pnpmExe) { $pnpmExe = Get-CommandPath -Name "pnpm" }

    foreach ($pair in @(@{ Dir = $frontendDir; Name = "frontend" }, @{ Dir = $executorDir; Name = "executor" })) {
        $modules = Join-Path $pair.Dir "node_modules"
        if (Test-Path -LiteralPath $modules -PathType Container) { continue }
        if ($SkipInstall) {
            throw ("{0}/node_modules is missing and -SkipInstall was given. Run 'pnpm install' in {0}/ first." -f $pair.Name)
        }
        if ($null -eq $pnpmExe) {
            throw ("{0}/node_modules is missing and pnpm is not available. Install pnpm (or enable Corepack) and run 'pnpm install' in {0}/." -f $pair.Name)
        }
        Write-Host "     installing $($pair.Name) dependencies (pnpm install)..." -ForegroundColor DarkGray
        Push-Location $pair.Dir
        try {
            & $pnpmExe install
            if ($LASTEXITCODE -ne 0) { throw "pnpm install failed in $($pair.Name)/ (exit code $LASTEXITCODE)." }
        } finally {
            Pop-Location
        }
    }

    $nextBin = Join-Path $frontendDir "node_modules\next\dist\bin\next"
    if (-not (Test-Path -LiteralPath $nextBin -PathType Leaf)) {
        throw "The Next.js CLI is missing at $nextBin. Run 'pnpm install' in frontend/ first."
    }

    $goExe = Join-Path $repoRoot ".tools\go\bin\go.exe"
    if (-not (Test-Path -LiteralPath $goExe -PathType Leaf)) {
        $goExe = Get-CommandPath -Name "go.exe"
        if ($null -eq $goExe) { $goExe = Get-CommandPath -Name "go" }
    }
    if ($null -eq $goExe) {
        throw "Go is not available. Run .tools/setup-toolchain.ps1 or install Go 1.24+."
    }

    # Local portable PostgreSQL first, otherwise require an already reachable one.
    $portablePg = Test-Path -LiteralPath (Join-Path $repoRoot ".tools\pgsql\bin\pg_ctl.exe") -PathType Leaf
    $localDbHosts = @("localhost", "127.0.0.1", "::1", "0.0.0.0", "")
    if ($portablePg -and ($localDbHosts -contains $databaseEndpoint.Host) -and $databaseEndpoint.Port -eq 5433) {
        Write-Host "     starting portable PostgreSQL (scripts\db.ps1 start)..." -ForegroundColor DarkGray
        # db.ps1 throws on failure (it sets $ErrorActionPreference = "Stop" too).
        & $dbScript start
    } else {
        Wait-DatabaseReady -TargetHost $databaseEndpoint.Host -Port $databaseEndpoint.Port
    }

    # Build the API binary so the recorded pid is the API process itself (not a
    # transient `go run` wrapper), which makes -Stop reliable.
    New-Item -ItemType Directory -Force -Path $stateDir | Out-Null
    New-Item -ItemType Directory -Force -Path $logDir | Out-Null
    Write-Host "     building API binary (first run can take a while)..." -ForegroundColor DarkGray
    $commit = ""
    $gitExe = Get-CommandPath -Name "git.exe"
    if ($null -eq $gitExe) { $gitExe = Get-CommandPath -Name "git" }
    if ($null -ne $gitExe) {
        try { $commit = (& $gitExe -C $repoRoot rev-parse --short HEAD 2>$null | Select-Object -First 1) } catch { $commit = "" }
    }
    $ldflags = "-X github.com/EdouardRichard/EzTest/backend/internal/server.Version=dev"
    if (-not [string]::IsNullOrWhiteSpace("$commit")) {
        $ldflags += " -X github.com/EdouardRichard/EzTest/backend/internal/server.Commit=$commit"
    }
    Push-Location $backendDir
    try {
        & $goExe build -o $apiBinary -ldflags $ldflags ./cmd/eztest
        if ($LASTEXITCODE -ne 0) { throw "go build ./cmd/eztest failed (exit code $LASTEXITCODE)." }
    } finally {
        Pop-Location
    }
    if (-not (Test-Path -LiteralPath $apiBinary -PathType Leaf)) { throw "The API binary was not produced at $apiBinary." }

    # Executor settings: the API and the executor must share the token.
    $executorToken = Get-EnvValue -Name "EZTEST_EXECUTOR_TOKEN"
    $tokenFallback = $false
    if ([string]::IsNullOrWhiteSpace($executorToken)) {
        $executorToken = "dev-executor-token"
        $tokenFallback = $true
    }
    [Environment]::SetEnvironmentVariable("EZTEST_EXECUTOR_TOKEN", $executorToken, "Process")
    if ([string]::IsNullOrWhiteSpace((Get-EnvValue -Name "EZTEST_EXECUTOR_MAX_CONTEXTS"))) {
        [Environment]::SetEnvironmentVariable("EZTEST_EXECUTOR_MAX_CONTEXTS", "8", "Process")
    }
    if ([string]::IsNullOrWhiteSpace((Get-EnvValue -Name "EZTEST_AGENT_FILE_ROOT"))) {
        # Local development only: let the agent file tools read the repository.
        [Environment]::SetEnvironmentVariable("EZTEST_AGENT_FILE_ROOT", $repoRoot, "Process")
    }

    Write-Host "     launching executor, API and web..." -ForegroundColor DarkGray
    # Start the executor first and wait for it: the API probes the executor while
    # starting up, so an already-healthy executor avoids a confusing one-time
    # "executor_unavailable" warning in the API log.
    $executorProcess = Start-Component -Name "executor" -FilePath $nodeExe -ArgumentList @("src/server.mjs") -WorkingDirectory $executorDir
    Write-Host "     waiting for the executor (Chromium launch)..." -ForegroundColor DarkGray
    $executorHealth = Wait-ExecutorReady -BaseUrl $executorBaseUrl -Process $executorProcess -Seconds 180

    $apiProcess = Start-Component -Name "api" -FilePath $apiBinary -ArgumentList @() -WorkingDirectory $backendDir
    $webProcess = Start-Component -Name "web" -FilePath $nodeExe -ArgumentList @("node_modules/next/dist/bin/next", "dev", "-p", "$webPort") -WorkingDirectory $frontendDir

    Write-Host "     waiting for the API and the web server..." -ForegroundColor DarkGray
    $apiHealth = Wait-ApiReady -BaseUrl $apiBaseUrl -Process $apiProcess -Seconds 180
    $webResponse = Wait-WebReady -BaseUrl $webProbeUrl -Process $webProcess -Seconds 240

    Write-Host ""
    Write-Host "EzTest development stack is up." -ForegroundColor Green
    Write-Host ("  web        {0}   (open this exact URL; it must match EZTEST_WEB_ORIGIN)" -f $webBrowserUrl)
    Write-Host ("  API        {0}   (/healthz, /api/health, /api/openapi.yaml; version {1})" -f $apiBaseUrl, $apiHealth.version)
    Write-Host ("  executor   {0}   (internal only; browser {1})" -f $executorBaseUrl, $executorHealth.browserVersion)
    Write-Host ("  postgres   {0}:{1}" -f $databaseEndpoint.Host, $databaseEndpoint.Port)
    Write-Host ("  sign in    {0}   (password: EZTEST_ADMIN_PASSWORD in .env)" -f (Get-EnvValue -Name "EZTEST_ADMIN_USERNAME" -Default "admin"))
    if ($tokenFallback) {
        Write-Host "  note       EZTEST_EXECUTOR_TOKEN is not set in .env; a dev placeholder is used for both the API and the executor." -ForegroundColor Yellow
        Write-Host "             Add EZTEST_EXECUTOR_TOKEN to .env to make this permanent across manual restarts." -ForegroundColor Yellow
    }
    if ($webOriginWarning) {
        Write-Host "  warning    $webOriginWarning" -ForegroundColor Yellow
    } else {
        Write-Host "  note       Open $webBrowserUrl, not http://127.0.0.1:${webPort}: the dev server and API CORS accept only the configured origin." -ForegroundColor DarkGray
    }
    Write-Host ""
    Write-Host ("  logs       {0}" -f $logDir)
    Write-Host ("             tail -f: Get-Content .tmp\dev\logs\api.log -Wait")
    Write-Host ("  status     powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts\dev-all.ps1 -Status")
    Write-Host ("  stop       powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts\dev-all.ps1 -Stop")
    Write-Host ""

    if ($Open) {
        Start-Process $webBrowserUrl | Out-Null
    }
    exit 0
} catch {
    Write-Host ""
    Write-Host ("ERROR: " + $_.Exception.Message) -ForegroundColor Red
    if ($script:Started.Count -gt 0) {
        Write-Host "Stopping the components that were already started..." -ForegroundColor Yellow
        foreach ($name in @($script:Started)) {
            Show-LogTail -Path (Get-ProcessLogPaths -Name $name).Err
            Show-LogTail -Path (Get-ProcessLogPaths -Name $name).Out
        }
        [void](Stop-StartedComponents)
    }
    Write-Host ""
    Write-Host "See $logDir for the full logs." -ForegroundColor DarkGray
    exit 1
}
