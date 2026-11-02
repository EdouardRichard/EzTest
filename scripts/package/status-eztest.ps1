#requires -Version 5.1
<#
.SYNOPSIS
    Shows the status of an extracted EzTest package.

.DESCRIPTION
    Reports the API/executor/web/renderer pid files, the five HTTP health probes
    (API /healthz, API /api/health with db=ok, executor /healthz with a browser
    version, renderer /healthz with a browser version, web /login) and the
    PostgreSQL TCP reachability from EZTEST_DATABASE_URL as a table. Exits 0
    when everything is healthy, 1 when a component is stopped or degraded
    (details in the table).

    Windows PowerShell 5.1 compatible. On Linux use status-eztest.sh.
#>
[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

if ([System.IO.Path]::DirectorySeparatorChar -ne '\') {
    Write-Host "This is the Windows status script." -ForegroundColor Red
    Write-Host "On Linux run: bash status-eztest.sh" -ForegroundColor Yellow
    exit 2
}

$PackageRoot = $PSScriptRoot
$envPath = Join-Path $PackageRoot ".env"
$runDir = Join-Path $PackageRoot "run"

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
    param([string]$Value, [int]$DefaultPort)
    $raw = "$Value".Trim()
    if ($raw.Length -eq 0) { return [pscustomobject]@{ Host = "127.0.0.1"; Port = $DefaultPort } }
    if ($raw -match '^\d+$') { return [pscustomobject]@{ Host = "127.0.0.1"; Port = [int]$raw } }
    $colon = $raw.LastIndexOf(":")
    if ($colon -lt 0) { return [pscustomobject]@{ Host = "127.0.0.1"; Port = $DefaultPort } }
    $hostPart = $raw.Substring(0, $colon).Trim()
    $portPart = $raw.Substring($colon + 1).Trim()
    if ($hostPart.StartsWith("[") -and $hostPart.EndsWith("]")) {
        $hostPart = $hostPart.Substring(1, $hostPart.Length - 2)
    }
    if ($portPart -notmatch '^\d+$') { return [pscustomobject]@{ Host = "127.0.0.1"; Port = $DefaultPort } }
    if ($hostPart.Length -eq 0) { $hostPart = "0.0.0.0" }
    return [pscustomobject]@{ Host = $hostPart; Port = [int]$portPart }
}

function Get-ProbeHost {
    param([string]$BindHost)
    if ($BindHost -eq "" -or $BindHost -eq "0.0.0.0" -or $BindHost -eq "::" -or $BindHost -eq "*") { return "127.0.0.1" }
    return $BindHost
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
    param([string]$Url, [int]$TimeoutSec = 5)
    $request = [System.Net.HttpWebRequest]::Create($Url)
    $request.Method = "GET"
    $request.Timeout = $TimeoutSec * 1000
    $request.ReadWriteTimeout = $TimeoutSec * 1000
    $request.AllowAutoRedirect = $false
    $request.UserAgent = "EzTest-package-script"
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

function Get-RecordedPid {
    param([string]$Name)
    $pidPath = Join-Path $runDir ($Name + ".pid")
    if (-not (Test-Path -LiteralPath $pidPath -PathType Leaf)) { return 0 }
    $text = Get-Content -LiteralPath $pidPath -TotalCount 1 -ErrorAction SilentlyContinue
    if ($null -eq $text) { return 0 }
    if ("$text".Trim() -match '^\d+$') { return [int]"$text".Trim() }
    return 0
}

function Test-PidAlive {
    param([int]$ProcessId)
    if ($ProcessId -le 0) { return $false }
    try {
        return ($null -ne (Get-Process -Id $ProcessId -ErrorAction Stop))
    } catch {
        return $false
    }
}

function Write-Row {
    param([string]$Component, [string]$State, [string]$Detail, [string]$Color = "Gray")
    Write-Host ("{0,-20} {1,-10} {2}" -f $Component, $State, $Detail) -ForegroundColor $Color
}

if (-not (Test-Path -LiteralPath $envPath -PathType Leaf)) {
    Write-Host "ERROR: $envPath not found. Run init-config.ps1 first." -ForegroundColor Red
    exit 2
}
Import-DotEnv -Path $envPath

$apiPortValue = Get-EnvValue -Name "EZTEST_API_PORT" -Default "13333"
if ($apiPortValue -notmatch '^\d+$') { $apiPortValue = "13333" }
$execEndpoint = ConvertTo-HostPort -Value (Get-EnvValue -Name "EZTEST_EXECUTOR_ADDR" -Default "127.0.0.1:13336") -DefaultPort 13336
$renderEndpoint = ConvertTo-HostPort -Value (Get-EnvValue -Name "EZTEST_RENDERER_ADDR" -Default "127.0.0.1:13337") -DefaultPort 13337
$webPortValue = Get-EnvValue -Name "PORT" -Default "6868"
if ($webPortValue -notmatch '^\d+$') { $webPortValue = "6868" }
$webHost = Get-EnvValue -Name "HOSTNAME" -Default "127.0.0.1"
$apiBase = "http://127.0.0.1:$([int]$apiPortValue)"
$execBase = "http://$(Get-ProbeHost -BindHost $execEndpoint.Host):$($execEndpoint.Port)"
$renderBase = "http://$(Get-ProbeHost -BindHost $renderEndpoint.Host):$($renderEndpoint.Port)"
$webBase = "http://$(Get-ProbeHost -BindHost $webHost):$([int]$webPortValue)"
$versionFile = Join-Path $PackageRoot "VERSION"
$packageVersion = ""
if (Test-Path -LiteralPath $versionFile -PathType Leaf) { $packageVersion = (Get-Content -LiteralPath $versionFile -TotalCount 1).Trim() }

$failures = 0

Write-Host ""
Write-Host "EzTest package status" -ForegroundColor Cyan
if ($packageVersion.Length -gt 0) { Write-Host "  version : $packageVersion" }
Write-Host "  package : $PackageRoot"
Write-Host ""
Write-Row -Component "Component" -State "State" -Detail "Detail" -Color "DarkGray"
Write-Row -Component "---------" -State "-----" -Detail "------" -Color "DarkGray"

# Processes.
$processStates = @{}
foreach ($component in @("api", "executor", "web", "renderer")) {
    $processId = Get-RecordedPid -Name $component
    $alive = Test-PidAlive -ProcessId $processId
    $processStates[$component] = $alive
    if ($alive) {
        Write-Row -Component "$component process" -State "RUNNING" -Detail "pid $processId" -Color "Green"
    } elseif ($processId -gt 0) {
        Write-Row -Component "$component process" -State "STOPPED" -Detail "stale pid file (pid $processId)" -Color "Red"
        $failures++
    } else {
        Write-Row -Component "$component process" -State "STOPPED" -Detail "no pid file" -Color "Red"
        $failures++
    }
}

# API probes.
try {
    $healthz = Invoke-RawHttp -Url "$apiBase/healthz" -TimeoutSec 4
    if ($healthz.Status -eq 200) {
        Write-Row -Component "API /healthz" -State "OK" -Detail "200" -Color "Green"
    } else {
        Write-Row -Component "API /healthz" -State "DEGRADED" -Detail "HTTP $($healthz.Status)" -Color "Red"
        $failures++
    }
} catch {
    Write-Row -Component "API /healthz" -State "UNREACHABLE" -Detail $apiBase -Color "Red"
    $failures++
}
try {
    $ready = Invoke-RawHttp -Url "$apiBase/api/health" -TimeoutSec 6
    $readyJson = $null
    try { $readyJson = $ready.Body | ConvertFrom-Json } catch { }
    if ($ready.Status -eq 200 -and $null -ne $readyJson -and $readyJson.data.db -eq "ok") {
        $detail = "db=ok version=$($readyJson.data.version) commit=$($readyJson.data.commit)"
        if ($packageVersion.Length -gt 0 -and "$($readyJson.data.version)" -ne $packageVersion) {
            $detail = $detail + " (VERSION file says $packageVersion)"
        }
        Write-Row -Component "API /api/health" -State "OK" -Detail $detail -Color "Green"
    } else {
        Write-Row -Component "API /api/health" -State "DEGRADED" -Detail "HTTP $($ready.Status)" -Color "Red"
        $failures++
    }
} catch {
    Write-Row -Component "API /api/health" -State "UNREACHABLE" -Detail $apiBase -Color "Red"
    $failures++
}

# Executor probe.
try {
    $execHealth = Invoke-RawHttp -Url "$execBase/healthz" -TimeoutSec 6
    $execJson = $null
    try { $execJson = $execHealth.Body | ConvertFrom-Json } catch { }
    if ($execHealth.Status -eq 200 -and $null -ne $execJson -and $execJson.status -eq "ok") {
        Write-Row -Component "executor /healthz" -State "OK" -Detail "browser=$($execJson.browserVersion)" -Color "Green"
    } else {
        $errorText = ""
        if ($null -ne $execJson -and $null -ne $execJson.error) { $errorText = " error=$($execJson.error)" }
        Write-Row -Component "executor /healthz" -State "DEGRADED" -Detail "HTTP $($execHealth.Status)$errorText" -Color "Red"
        $failures++
    }
} catch {
    Write-Row -Component "executor /healthz" -State "UNREACHABLE" -Detail $execBase -Color "Red"
    $failures++
}

# Renderer probe.
try {
    $renderHealth = Invoke-RawHttp -Url "$renderBase/healthz" -TimeoutSec 8
    $renderJson = $null
    try { $renderJson = $renderHealth.Body | ConvertFrom-Json } catch { }
    $renderBrowser = ""
    if ($null -ne $renderJson) {
        $renderBrowser = "$($renderJson.browser)"
        if ([string]::IsNullOrWhiteSpace($renderBrowser)) { $renderBrowser = "$($renderJson.browserVersion)" }
    }
    if ($renderHealth.Status -eq 200 -and $null -ne $renderJson -and $renderJson.status -eq "ok" -and -not [string]::IsNullOrWhiteSpace($renderBrowser)) {
        Write-Row -Component "renderer /healthz" -State "OK" -Detail "browser=$renderBrowser" -Color "Green"
    } else {
        $errorText = ""
        if ($null -ne $renderJson -and $null -ne $renderJson.error) { $errorText = " error=$($renderJson.error)" }
        Write-Row -Component "renderer /healthz" -State "DEGRADED" -Detail "HTTP $($renderHealth.Status)$errorText" -Color "Red"
        $failures++
    }
} catch {
    Write-Row -Component "renderer /healthz" -State "UNREACHABLE" -Detail $renderBase -Color "Red"
    $failures++
}

# Web probe.
try {
    $login = Invoke-RawHttp -Url "$webBase/login" -TimeoutSec 5
    if ($login.Status -eq 200) {
        Write-Row -Component "web /login" -State "OK" -Detail "200 ($webBase)" -Color "Green"
    } else {
        Write-Row -Component "web /login" -State "DEGRADED" -Detail "HTTP $($login.Status)" -Color "Red"
        $failures++
    }
} catch {
    Write-Row -Component "web /login" -State "UNREACHABLE" -Detail $webBase -Color "Red"
    $failures++
}

# PostgreSQL probe (TCP only; the API /api/health check above proves the login).
$databaseUrl = Get-EnvValue -Name "EZTEST_DATABASE_URL"
if ($databaseUrl.Length -eq 0 -or $databaseUrl.Contains("<")) {
    Write-Row -Component "PostgreSQL" -State "UNCONFIGURED" -Detail "EZTEST_DATABASE_URL is empty or a placeholder" -Color "Red"
    $failures++
} else {
    $dbRest = $databaseUrl
    $schemeIndex = $dbRest.IndexOf("://")
    if ($schemeIndex -ge 0) { $dbRest = $dbRest.Substring($schemeIndex + 3) }
    $slashIndex = $dbRest.IndexOf("/")
    if ($slashIndex -ge 0) { $dbRest = $dbRest.Substring(0, $slashIndex) }
    $atIndex = $dbRest.LastIndexOf("@")
    if ($atIndex -ge 0) { $dbRest = $dbRest.Substring($atIndex + 1) }
    $dbHost = $dbRest
    $dbPort = 5432
    $colonIndex = $dbRest.LastIndexOf(":")
    if ($colonIndex -gt 0) {
        $portPart = $dbRest.Substring($colonIndex + 1)
        if ($portPart -match '^\d+$') {
            $dbHost = $dbRest.Substring(0, $colonIndex)
            $dbPort = [int]$portPart
        }
    }
    if (Test-TcpEndpoint -TargetHost $dbHost -Port $dbPort) {
        Write-Row -Component "PostgreSQL" -State "REACHABLE" -Detail "${dbHost}:${dbPort}" -Color "Green"
    } else {
        Write-Row -Component "PostgreSQL" -State "UNREACHABLE" -Detail "${dbHost}:${dbPort} (from EZTEST_DATABASE_URL)" -Color "Red"
        $failures++
    }
}

Write-Host ""
if ($failures -eq 0) {
    Write-Host "Overall: healthy" -ForegroundColor Green
    exit 0
}
Write-Host "Overall: $failures check(s) failed or degraded. See the table above and logs\." -ForegroundColor Red
exit 1
