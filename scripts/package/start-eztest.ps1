#requires -Version 5.1
<#
.SYNOPSIS
    Starts the EzTest stack of an extracted Windows package.

.DESCRIPTION
    Starts, in order: the Go API (bin\eztest.exe), the Playwright executor
    (runtime\node\node.exe executor\src\server.mjs) and the Next.js web server
    (runtime\node\node.exe web\server.js). Each process gets a pid file under
    run\ and a log under logs\; startup waits for the documented health checks
    (API /healthz + /api/health db=ok, executor /healthz with a browserVersion,
    web /login) before reporting success.

    Before starting anything the script refuses to run when:
      - .env is missing (run init-config.ps1 first),
      - a required package file or the OpenAPI document is missing,
      - EZTEST_DATABASE_URL is unset/placeholder or PostgreSQL is not reachable
        over TCP,
      - one of the packaged ports (13333/6868/13336 by default) is in use,
      - a pid recorded in run\ belongs to a live process (use stop first).

    If any component fails to become healthy the already-started processes are
    stopped again and a non-zero exit code is returned. Secrets are never
    printed; the administrator password lives in .env.

    Windows PowerShell 5.1 compatible. On Linux use start-eztest.sh.
#>
[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

function Write-FailAndExit([string]$Message) {
    Write-Host ""
    Write-Host "ERROR: $Message" -ForegroundColor Red
    exit 1
}

if ([System.IO.Path]::DirectorySeparatorChar -ne '\') {
    Write-Host "This is the Windows start script." -ForegroundColor Red
    Write-Host "On Linux run: bash start-eztest.sh" -ForegroundColor Yellow
    exit 2
}

$PackageRoot = $PSScriptRoot
$paths = @{
    Env        = Join-Path $PackageRoot ".env"
    Api        = Join-Path $PackageRoot "bin\eztest.exe"
    Node       = Join-Path $PackageRoot "runtime\node\node.exe"
    WebServer  = Join-Path $PackageRoot "web\server.js"
    ExecServer = Join-Path $PackageRoot "executor\src\server.mjs"
    OpenApi    = Join-Path $PackageRoot "docs\openapi\api.yaml"
    Logs       = Join-Path $PackageRoot "logs"
    Run        = Join-Path $PackageRoot "run"
    Data       = Join-Path $PackageRoot "data"
    Browsers   = Join-Path $PackageRoot "playwright-browsers"
}
$script:Started = New-Object System.Collections.ArrayList

function Import-DotEnv {
    param([string]$Path)
    foreach ($line in [System.IO.File]::ReadAllLines($Path, [System.Text.Encoding]::UTF8)) {
        $trimmed = $line.Trim()
        if ($trimmed.Length -eq 0 -or $trimmed.StartsWith("#")) { continue }
        if ($trimmed -match '^([^=]+)=(.*)$') {
            # Capture the groups before any other -match operator (PowerShell
            # clears $matches when a later -notmatch/-match fails).
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
    param([string]$TargetHost, [int]$Port, [int]$TimeoutMs = 2500)
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
    param(
        [string]$Url,
        [string]$Method = "GET",
        [hashtable]$Headers = @{},
        [string]$BodyJson = "",
        [int]$TimeoutSec = 10
    )
    $request = [System.Net.HttpWebRequest]::Create($Url)
    $request.Method = $Method
    $request.Timeout = $TimeoutSec * 1000
    $request.ReadWriteTimeout = $TimeoutSec * 1000
    $request.AllowAutoRedirect = $false
    $request.UserAgent = "EzTest-package-script"
    foreach ($name in $Headers.Keys) { $request.Headers[$name] = [string]$Headers[$name] }
    if ($BodyJson.Length -gt 0) {
        $bytes = [System.Text.Encoding]::UTF8.GetBytes($BodyJson)
        $request.ContentType = "application/json"
        $request.ContentLength = $bytes.Length
        $stream = $request.GetRequestStream()
        try { $stream.Write($bytes, 0, $bytes.Length) } finally { $stream.Close() }
    }
    $response = $null
    try {
        $response = $request.GetResponse()
    } catch [System.Net.WebException] {
        if ($null -eq $_.Exception.Response) { throw }
        $response = $_.Exception.Response
    }
    try {
        $status = [int]$response.StatusCode
        $contentType = [string]$response.ContentType
        $reader = New-Object System.IO.StreamReader($response.GetResponseStream(), [System.Text.Encoding]::UTF8)
        try { $body = $reader.ReadToEnd() } finally { $reader.Close() }
        return [pscustomobject]@{
            Status      = $status
            Body        = $body
            ContentType = $contentType
            Bytes       = [System.Text.Encoding]::UTF8.GetByteCount($body)
        }
    } finally {
        $response.Close()
    }
}

function Show-LogTail {
    param([string]$Path, [int]$Lines = 20)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return }
    Write-Host "--- $Path (last $Lines lines) ---" -ForegroundColor DarkGray
    $tail = Get-Content -LiteralPath $Path -Tail $Lines -ErrorAction SilentlyContinue
    foreach ($line in $tail) {
        Write-Host ("  " + ($line -replace '[A-Za-z0-9+/=]{40,}', '<redacted>'))
    }
}

function Get-ProcessLogPaths {
    param([string]$Name)
    return [pscustomobject]@{
        Out = Join-Path $paths.Logs ($Name + ".log")
        Err = Join-Path $paths.Logs ($Name + ".err.log")
    }
}

function Rotate-Log {
    param([string]$Path)
    if (Test-Path -LiteralPath $Path -PathType Leaf) {
        Move-Item -LiteralPath $Path -Destination ($Path + ".prev") -Force
    }
}

function Start-EztestProcess {
    param(
        [string]$Name,
        [string]$Exe,
        [string]$Arguments,
        [string]$WorkingDirectory
    )
    $logPaths = Get-ProcessLogPaths -Name $Name
    Rotate-Log -Path $logPaths.Out
    Rotate-Log -Path $logPaths.Err
    if ($Arguments.Length -eq 0) {
        $process = Start-Process -FilePath $Exe -WorkingDirectory $WorkingDirectory -WindowStyle Hidden `
            -RedirectStandardOutput $logPaths.Out -RedirectStandardError $logPaths.Err -PassThru
    } else {
        $process = Start-Process -FilePath $Exe -ArgumentList $Arguments -WorkingDirectory $WorkingDirectory -WindowStyle Hidden `
            -RedirectStandardOutput $logPaths.Out -RedirectStandardError $logPaths.Err -PassThru
    }
    $pidPath = Join-Path $paths.Run ($Name + ".pid")
    Set-Content -LiteralPath $pidPath -Value $process.Id -Encoding ASCII
    [void]$script:Started.Add([pscustomobject]@{ Name = $Name; Process = $process; PidPath = $pidPath })
    return $process
}

function Stop-ProcessTree {
    param([int]$ProcessId)
    try {
        & taskkill.exe /PID $ProcessId /T /F 2>$null | Out-Null
    } catch {
        try { Stop-Process -Id $ProcessId -Force -ErrorAction SilentlyContinue } catch { }
    }
}

function Stop-StartedProcesses {
    if ($script:Started.Count -eq 0) { return }
    # Started order is api, executor, web; stop in reverse order, API last.
    $ordered = @($script:Started | Sort-Object { $_.Name -eq "api" })
    foreach ($entry in $ordered) {
        $process = $entry.Process
        try {
            $process.Refresh()
            if (-not $process.HasExited) {
                Write-Host ("Stopping {0} (pid {1})..." -f $entry.Name, $process.Id) -ForegroundColor DarkGray
                Stop-ProcessTree -ProcessId $process.Id
                $deadline = (Get-Date).AddSeconds(10)
                while ((Get-Date) -lt $deadline) {
                    $process.Refresh()
                    if ($process.HasExited) { break }
                    Start-Sleep -Milliseconds 200
                }
            }
        } catch { }
        if (Test-Path -LiteralPath $entry.PidPath) {
            Remove-Item -LiteralPath $entry.PidPath -Force -ErrorAction SilentlyContinue
        }
    }
    $script:Started.Clear()
}

function Get-RecordedPid {
    param([string]$Name)
    $pidPath = Join-Path $paths.Run ($Name + ".pid")
    if (-not (Test-Path -LiteralPath $pidPath -PathType Leaf)) { return 0 }
    $text = (Get-Content -LiteralPath $pidPath -TotalCount 1 -ErrorAction SilentlyContinue)
    if ($null -eq $text) { return 0 }
    if ("$text".Trim() -match '^\d+$') { return [int]"$text".Trim() }
    return 0
}

function Test-PidAlive {
    param([int]$ProcessId)
    if ($ProcessId -le 0) { return $false }
    try {
        $process = Get-Process -Id $ProcessId -ErrorAction Stop
        return ($null -ne $process)
    } catch {
        return $false
    }
}

function Wait-ApiReady {
    param([string]$BaseUrl, [System.Diagnostics.Process]$Process, [int]$Seconds = 60)
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
                    $lastDetail = "/api/health returned $($ready.Status)"
                }
            } else {
                $lastDetail = "/healthz returned $($healthz.Status)"
            }
        } catch {
            $lastDetail = "connection not ready"
        }
        Start-Sleep -Milliseconds 500
    }
    throw "The API did not become healthy within $Seconds seconds (last: $lastDetail).$([Environment]::NewLine)$(Get-LogHint api)"
}

function Wait-ExecutorReady {
    param([string]$BaseUrl, [System.Diagnostics.Process]$Process, [int]$Seconds = 90)
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
    throw "The Playwright executor did not become ready within $Seconds seconds (last: $lastDetail).$([Environment]::NewLine)Bundled Chromium could not be launched; check logs\executor.err.log and the PACKAGE.md troubleshooting section.$([Environment]::NewLine)$(Get-LogHint executor)"
}

function Wait-WebReady {
    param([string]$BaseUrl, [System.Diagnostics.Process]$Process, [int]$Seconds = 60)
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

function Get-LogHint {
    param([string]$Name)
    $logPaths = Get-ProcessLogPaths -Name $Name
    $lines = @()
    if (Test-Path -LiteralPath $logPaths.Err -PathType Leaf) {
        $lines += (Get-Content -LiteralPath $logPaths.Err -Tail 10 -ErrorAction SilentlyContinue)
    }
    if (Test-Path -LiteralPath $logPaths.Out -PathType Leaf) {
        $lines += (Get-Content -LiteralPath $logPaths.Out -Tail 10 -ErrorAction SilentlyContinue)
    }
    if ($lines.Count -eq 0) { return "No log output was written; see the logs\ directory." }
    $rendered = New-Object System.Collections.ArrayList
    foreach ($line in $lines) {
        if ($null -eq $line) { continue }
        [void]$rendered.Add("  " + ($line -replace '[A-Za-z0-9+/=]{40,}', "<redacted>"))
    }
    return "Recent log lines:`n" + ($rendered -join [Environment]::NewLine)
}

function Resolve-PackagePath {
    param([string]$Value, [string]$DefaultRelative, [string]$SettingName)
    $raw = "$Value".Trim()
    if ($raw.Length -eq 0) { $raw = $DefaultRelative }
    if ($raw.Length -eq 0) { return "" }
    if (-not [System.IO.Path]::IsPathRooted($raw)) { $raw = Join-Path $PackageRoot $raw }
    return [System.IO.Path]::GetFullPath($raw)
}

# ---------------------------------------------------------------------------
# Preconditions
# ---------------------------------------------------------------------------
if (-not (Test-Path -LiteralPath $paths.Env -PathType Leaf)) {
    Write-FailAndExit "Missing $($paths.Env). Run init-config.ps1 first (it creates .env and generates the secrets)."
}
Import-DotEnv -Path $paths.Env

$required = @(
    @{ Path = $paths.Api; Hint = "The API binary is missing; extract the complete package archive." },
    @{ Path = $paths.Node; Hint = "The bundled Node runtime is missing; extract the complete package archive." },
    @{ Path = $paths.WebServer; Hint = "web\server.js is missing; extract the complete package archive." },
    @{ Path = $paths.ExecServer; Hint = "executor\src\server.mjs is missing; extract the complete package archive." },
    @{ Path = $paths.OpenApi; Hint = "docs\openapi\api.yaml is missing; it is required by GET /api/openapi.yaml." },
    @{ Path = $paths.Browsers; Hint = "playwright-browsers\ is missing; extract the complete package archive." }
)
foreach ($entry in $required) {
    if (-not (Test-Path -LiteralPath $entry.Path)) {
        Write-FailAndExit "$($entry.Path) was not found. $($entry.Hint)"
    }
}

# Absolute package paths: repair hand-written relative values in .env.
$pathSettings = @(
    @{ Key = "EZTEST_OPENAPI_PATH"; Default = "docs/openapi/api.yaml" },
    @{ Key = "EZTEST_STORAGE_DIR"; Default = "data/objects" },
    @{ Key = "EZTEST_VERSION_ATTACHMENT_DIR"; Default = "data/version-attachments" },
    @{ Key = "EZTEST_EVIDENCE_DIR"; Default = "data/evidence" },
    @{ Key = "EZTEST_REPORT_DIR"; Default = "data/reports" },
    @{ Key = "EZTEST_EXPORT_DIR"; Default = "data/exports" }
)
foreach ($setting in $pathSettings) {
    $resolved = Resolve-PackagePath -Value (Get-EnvValue -Name $setting.Key) -DefaultRelative $setting.Default -SettingName $setting.Key
    [Environment]::SetEnvironmentVariable($setting.Key, $resolved, "Process")
    if ($setting.Key -ne "EZTEST_OPENAPI_PATH") {
        New-Item -ItemType Directory -Force -Path $resolved | Out-Null
    }
}
$browsersPath = Resolve-PackagePath -Value (Get-EnvValue -Name "PLAYWRIGHT_BROWSERS_PATH") -DefaultRelative "playwright-browsers" -SettingName "PLAYWRIGHT_BROWSERS_PATH"
[Environment]::SetEnvironmentVariable("PLAYWRIGHT_BROWSERS_PATH", $browsersPath, "Process")
$agentRoot = Get-EnvValue -Name "EZTEST_AGENT_FILE_ROOT"
if ($agentRoot.Length -gt 0) {
    $agentRoot = Resolve-PackagePath -Value $agentRoot -DefaultRelative "" -SettingName "EZTEST_AGENT_FILE_ROOT"
    [Environment]::SetEnvironmentVariable("EZTEST_AGENT_FILE_ROOT", $agentRoot, "Process")
}
foreach ($directory in @($paths.Logs, $paths.Run)) {
    New-Item -ItemType Directory -Force -Path $directory | Out-Null
}

# Endpoints and ports.
$apiEndpoint = ConvertTo-HostPort -Value (Get-EnvValue -Name "EZTEST_HTTP_ADDR" -Default "127.0.0.1:13333") -DefaultPort 13333 -SettingName "EZTEST_HTTP_ADDR"
$execEndpoint = ConvertTo-HostPort -Value (Get-EnvValue -Name "EZTEST_EXECUTOR_ADDR" -Default "127.0.0.1:13336") -DefaultPort 13336 -SettingName "EZTEST_EXECUTOR_ADDR"
$webPortValue = Get-EnvValue -Name "PORT" -Default "6868"
if ($webPortValue -notmatch '^\d+$') { Write-FailAndExit "PORT in .env must be a number (got '$webPortValue')." }
$webPort = [int]$webPortValue
$webHost = Get-EnvValue -Name "HOSTNAME" -Default "127.0.0.1"
$apiBaseUrl = "http://$(Get-ProbeHost -BindHost $apiEndpoint.Host):$($apiEndpoint.Port)"
$execBaseUrl = "http://$(Get-ProbeHost -BindHost $execEndpoint.Host):$($execEndpoint.Port)"
$webBaseUrl = "http://$(Get-ProbeHost -BindHost $webHost):$webPort"

# Database preflight.
$databaseUrl = Get-EnvValue -Name "EZTEST_DATABASE_URL"
if ($databaseUrl.Length -eq 0 -or $databaseUrl.Contains("<")) {
    Write-FailAndExit "EZTEST_DATABASE_URL is not configured in .env. Set it to a PostgreSQL 17 URL, for example: postgres://eztest:<password>@localhost:5433/eztest?sslmode=disable"
}
if ($databaseUrl -notmatch '^postgres(ql)?://') {
    Write-FailAndExit "EZTEST_DATABASE_URL must start with postgres:// or postgresql://."
}
$dbEndpoint = Get-DatabaseEndpoint -Url $databaseUrl
if (-not (Test-TcpEndpoint -TargetHost $dbEndpoint.Host -Port $dbEndpoint.Port)) {
    Write-FailAndExit @"
PostgreSQL is not reachable at $($dbEndpoint.Host):$($dbEndpoint.Port) (from EZTEST_DATABASE_URL).

Install/start PostgreSQL 17 and make sure the database and user exist, for example:
  Windows: install PostgreSQL 17 (EDB installer or the portable zip), then start the service.
  Linux:   sudo apt install postgresql-17 && sudo systemctl start postgresql
  Create the database/user with psql, then update EZTEST_DATABASE_URL in .env.

See PACKAGE.md ("PostgreSQL 17") for the exact commands.
"@
}
$executorToken = Get-EnvValue -Name "EZTEST_EXECUTOR_TOKEN"
if ($executorToken.Length -eq 0) {
    Write-FailAndExit "EZTEST_EXECUTOR_TOKEN is empty in .env. Re-run init-config.ps1 -Force or set it manually."
}

# Already-running and port checks.
$components = @("api", "executor", "web")
foreach ($component in $components) {
    $recordedPid = Get-RecordedPid -Name $component
    if (Test-PidAlive -ProcessId $recordedPid) {
        Write-FailAndExit "EzTest is already running ($component pid $recordedPid). Run .\stop-eztest.ps1 first, or .\status-eztest.ps1 to inspect it."
    }
    $stalePidPath = Join-Path $paths.Run ($component + ".pid")
    if (Test-Path -LiteralPath $stalePidPath) { Remove-Item -LiteralPath $stalePidPath -Force -ErrorAction SilentlyContinue }
}
$portChecks = @(
    @{ Host = (Get-ProbeHost -BindHost $apiEndpoint.Host); Port = $apiEndpoint.Port; Label = "API (EZTEST_HTTP_ADDR)" },
    @{ Host = (Get-ProbeHost -BindHost $execEndpoint.Host); Port = $execEndpoint.Port; Label = "executor (EZTEST_EXECUTOR_ADDR)" },
    @{ Host = (Get-ProbeHost -BindHost $webHost); Port = $webPort; Label = "web (PORT)" }
)
foreach ($check in $portChecks) {
    if (Test-TcpEndpoint -TargetHost $check.Host -Port $check.Port) {
        Write-FailAndExit "Port $($check.Port) is already in use ($($check.Label)). Stop the process using it or change the port in .env."
    }
}

Write-Host "Starting EzTest from $PackageRoot" -ForegroundColor Cyan
Write-Host "  API      : $apiBaseUrl"
Write-Host "  executor : $execBaseUrl"
Write-Host "  web      : $webBaseUrl"
Write-Host "  database : $($dbEndpoint.Host):$($dbEndpoint.Port) (credentials from .env)"
Write-Host ""

try {
    # --- API ---------------------------------------------------------------
    Write-Host "[1/3] API ..." -NoNewline
    $apiProcess = Start-EztestProcess -Name "api" -Exe $paths.Api -Arguments "" -WorkingDirectory $PackageRoot
    $health = Wait-ApiReady -BaseUrl $apiBaseUrl -Process $apiProcess -Seconds 60
    Write-Host " ready (version $($health.version), db=$($health.db))" -ForegroundColor Green

    # --- executor ----------------------------------------------------------
    Write-Host "[2/3] Playwright executor ..." -NoNewline
    [Environment]::SetEnvironmentVariable("EZTEST_EXECUTOR_TOKEN", $executorToken, "Process")
    [Environment]::SetEnvironmentVariable("EZTEST_EXECUTOR_ADDR", (Get-EnvValue -Name "EZTEST_EXECUTOR_ADDR" -Default "127.0.0.1:13336"), "Process")
    $maxContexts = Get-EnvValue -Name "EZTEST_EXECUTOR_MAX_CONTEXTS" -Default "8"
    [Environment]::SetEnvironmentVariable("EZTEST_EXECUTOR_MAX_CONTEXTS", $maxContexts, "Process")
    $allowedHosts = Get-EnvValue -Name "EZTEST_EXEC_ALLOWED_HOSTS" -Default "localhost,127.0.0.1"
    [Environment]::SetEnvironmentVariable("EZTEST_EXEC_ALLOWED_HOSTS", $allowedHosts, "Process")
    $baselineBytes = Get-EnvValue -Name "EZTEST_VISUAL_BASELINE_MAX_BYTES" -Default "5242880"
    [Environment]::SetEnvironmentVariable("EZTEST_VISUAL_BASELINE_MAX_BYTES", $baselineBytes, "Process")
    $execArgs = '"' + $paths.ExecServer + '"'
    $execProcess = Start-EztestProcess -Name "executor" -Exe $paths.Node -Arguments $execArgs -WorkingDirectory $PackageRoot
    $execHealth = Wait-ExecutorReady -BaseUrl $execBaseUrl -Process $execProcess -Seconds 90
    Write-Host " ready (browser $($execHealth.browserVersion))" -ForegroundColor Green

    # --- web ---------------------------------------------------------------
    Write-Host "[3/3] web ..." -NoNewline
    [Environment]::SetEnvironmentVariable("NODE_ENV", "production", "Process")
    [Environment]::SetEnvironmentVariable("PORT", "$webPort", "Process")
    [Environment]::SetEnvironmentVariable("HOSTNAME", $webHost, "Process")
    $webArgs = '"' + $paths.WebServer + '"'
    $webProcess = Start-EztestProcess -Name "web" -Exe $paths.Node -Arguments $webArgs -WorkingDirectory (Join-Path $PackageRoot "web")
    Wait-WebReady -BaseUrl $webBaseUrl -Process $webProcess -Seconds 60 | Out-Null
    Write-Host " ready" -ForegroundColor Green
} catch {
    Write-Host ""
    Write-Host "EzTest failed to start." -ForegroundColor Red
    Stop-StartedProcesses
    Write-FailAndExit $_.Exception.Message
}

Write-Host ""
Write-Host "EzTest is running." -ForegroundColor Green
Write-Host "  Web UI        : http://localhost:$webPort"
Write-Host "  API health    : $apiBaseUrl/api/health"
Write-Host "  API liveness  : $apiBaseUrl/healthz"
Write-Host "  OpenAPI       : $apiBaseUrl/api/openapi.yaml"
Write-Host "  Executor      : $execBaseUrl/healthz"
Write-Host "  Administrator : admin (password is in .env - never printed)"
Write-Host ""
Write-Host "  Logs          : $(Join-Path $PackageRoot 'logs')"
Write-Host "  Pid files     : $(Join-Path $PackageRoot 'run')"
Write-Host "  Status/stop   : .\status-eztest.ps1 / .\stop-eztest.ps1"
exit 0
