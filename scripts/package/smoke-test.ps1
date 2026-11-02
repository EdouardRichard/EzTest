#requires -Version 5.1
<#
.SYNOPSIS
    End-to-end smoke test for an extracted, running EzTest package.

.DESCRIPTION
    Runs the ten packaging checks from the release checklist against a stack
    started with start-eztest.ps1:

      1. PostgreSQL TCP reachable
      2. GET /healthz = 200 {status:ok}
      3. GET /api/health = 200, data.db = ok, data.version equals VERSION
      4. GET /api/openapi.yaml = 200, Content-Type application/yaml, > 100 kB
      5. GET /api/versions without a token = 401
      6. Admin login = 200 and GET /api/auth/me = 200 username=admin
      7. Executor GET /healthz = 200 status=ok with a browserVersion
      8. Executor round-trip: context -> goto web /login -> aria snapshot ->
         PNG screenshot artifact -> delete context (all ok:true)
      9. GET /login = 200 and the body contains "EzTest"
     10. The data/ directories exist after start

    Prints PASS/FAIL per check and a summary; exits non-zero when any check
    fails. Secrets (the admin password and the executor token) are read from
    .env but never printed.

    Windows PowerShell 5.1 compatible. On Linux use smoke-test.sh.
#>
[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

if ([System.IO.Path]::DirectorySeparatorChar -ne '\') {
    Write-Host "This is the Windows smoke test." -ForegroundColor Red
    Write-Host "On Linux run: bash smoke-test.sh" -ForegroundColor Yellow
    exit 2
}

$PackageRoot = $PSScriptRoot
$envPath = Join-Path $PackageRoot ".env"
$versionFile = Join-Path $PackageRoot "VERSION"

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

function Get-DatabaseEndpoint {
    param([string]$Url)
    $rest = $Url
    $scheme = $rest.IndexOf("://")
    if ($scheme -ge 0) { $rest = $rest.Substring($scheme + 3) }
    $slash = $rest.IndexOf("/")
    if ($slash -ge 0) { $rest = $rest.Substring(0, $slash) }
    $at = $rest.LastIndexOf("@")
    if ($at -ge 0) { $rest = $rest.Substring($at + 1) }
    if ($rest -match '^\[(.+)\]:(\d+)$') { return [pscustomobject]@{ Host = $matches[1]; Port = [int]$matches[2] } }
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
    param([string]$TargetHost, [int]$Port, [int]$TimeoutMs = 3000)
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
        [int]$TimeoutSec = 15
    )
    $request = [System.Net.HttpWebRequest]::Create($Url)
    $request.Method = $Method
    $request.Timeout = $TimeoutSec * 1000
    $request.ReadWriteTimeout = $TimeoutSec * 1000
    $request.AllowAutoRedirect = $false
    $request.UserAgent = "EzTest-smoke-test"
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

function ConvertFrom-JsonSafe {
    param([string]$Text)
    return ($Text | ConvertFrom-Json)
}

function Assert-True {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw $Message }
}

$script:TotalChecks = 10
$script:PassedChecks = 0
$script:Failures = New-Object System.Collections.ArrayList

function Invoke-SmokeCheck {
    param([int]$Number, [string]$Name, [scriptblock]$Body)
    Write-Host ("[{0,2}/{1}] {2,-64}" -f $Number, $script:TotalChecks, $Name) -NoNewline
    try {
        & $Body | Out-Null
        $script:PassedChecks++
        Write-Host " PASS" -ForegroundColor Green
    } catch {
        [void]$script:Failures.Add("$Number. $Name - $($_.Exception.Message)")
        Write-Host " FAIL" -ForegroundColor Red
        Write-Host ("         " + $_.Exception.Message) -ForegroundColor Red
    }
}

if (-not (Test-Path -LiteralPath $envPath -PathType Leaf)) {
    Write-Host "ERROR: $envPath not found. Run init-config.ps1 and start-eztest.ps1 first." -ForegroundColor Red
    exit 2
}
Import-DotEnv -Path $envPath

$packageVersion = "dev"
if (Test-Path -LiteralPath $versionFile -PathType Leaf) { $packageVersion = (Get-Content -LiteralPath $versionFile -TotalCount 1).Trim() }
$adminUsername = Get-EnvValue -Name "EZTEST_ADMIN_USERNAME" -Default "admin"
$adminPassword = Get-EnvValue -Name "EZTEST_ADMIN_PASSWORD"
$executorToken = Get-EnvValue -Name "EZTEST_EXECUTOR_TOKEN"
if ($adminPassword.Length -eq 0 -or $adminPassword.Contains("<")) {
    Write-Host "ERROR: EZTEST_ADMIN_PASSWORD is missing/placeholder in .env; run init-config.ps1 first." -ForegroundColor Red
    exit 2
}
if ($executorToken.Length -eq 0) {
    Write-Host "ERROR: EZTEST_EXECUTOR_TOKEN is empty in .env; run init-config.ps1 first." -ForegroundColor Red
    exit 2
}
$databaseUrl = Get-EnvValue -Name "EZTEST_DATABASE_URL"
if ($databaseUrl.Length -eq 0 -or $databaseUrl.Contains("<")) {
    Write-Host "ERROR: EZTEST_DATABASE_URL is not configured in .env." -ForegroundColor Red
    exit 2
}

$apiEndpoint = ConvertTo-HostPort -Value (Get-EnvValue -Name "EZTEST_HTTP_ADDR" -Default "127.0.0.1:13333") -DefaultPort 13333
$execEndpoint = ConvertTo-HostPort -Value (Get-EnvValue -Name "EZTEST_EXECUTOR_ADDR" -Default "127.0.0.1:13336") -DefaultPort 13336
$webPortValue = Get-EnvValue -Name "PORT" -Default "6868"
if ($webPortValue -notmatch '^\d+$') { $webPortValue = "6868" }
$webHost = Get-EnvValue -Name "HOSTNAME" -Default "127.0.0.1"
$apiBase = "http://$(Get-ProbeHost -BindHost $apiEndpoint.Host):$($apiEndpoint.Port)"
$execBase = "http://$(Get-ProbeHost -BindHost $execEndpoint.Host):$($execEndpoint.Port)"
$webBase = "http://$(Get-ProbeHost -BindHost $webHost):$([int]$webPortValue)"
$dbEndpoint = Get-DatabaseEndpoint -Url $databaseUrl

Write-Host ""
Write-Host "EzTest package smoke test" -ForegroundColor Cyan
Write-Host "  package : $PackageRoot"
Write-Host "  version : $packageVersion"
Write-Host "  API     : $apiBase"
Write-Host "  web     : $webBase"
Write-Host "  executor: $execBase"
Write-Host "  database: $($dbEndpoint.Host):$($dbEndpoint.Port) (credentials are not displayed)"
Write-Host ""

Invoke-SmokeCheck -Number 1 -Name "PostgreSQL TCP reachable" -Body {
    Assert-True (Test-TcpEndpoint -TargetHost $dbEndpoint.Host -Port $dbEndpoint.Port) `
        "Cannot open a TCP connection to $($dbEndpoint.Host):$($dbEndpoint.Port). Is PostgreSQL 17 running and is EZTEST_DATABASE_URL correct?"
}

Invoke-SmokeCheck -Number 2 -Name "API GET /healthz is 200 {status:ok}" -Body {
    $response = Invoke-RawHttp -Url "$apiBase/healthz"
    Assert-True ($response.Status -eq 200) "GET /healthz returned HTTP $($response.Status)."
    $json = ConvertFrom-JsonSafe $response.Body
    Assert-True ($json.status -eq "ok") "GET /healthz status field is '$($json.status)'."
}

Invoke-SmokeCheck -Number 3 -Name "API GET /api/health has db=ok and the VERSION version" -Body {
    $response = Invoke-RawHttp -Url "$apiBase/api/health"
    Assert-True ($response.Status -eq 200) "GET /api/health returned HTTP $($response.Status)."
    $json = ConvertFrom-JsonSafe $response.Body
    Assert-True ($json.data.db -eq "ok") "GET /api/health db field is '$($json.data.db)'."
    Assert-True ("$($json.data.version)" -eq $packageVersion) "GET /api/health version is '$($json.data.version)', expected '$packageVersion' from the VERSION file."
}

Invoke-SmokeCheck -Number 4 -Name "API GET /api/openapi.yaml is YAML and >100 kB" -Body {
    $response = Invoke-RawHttp -Url "$apiBase/api/openapi.yaml"
    Assert-True ($response.Status -eq 200) "GET /api/openapi.yaml returned HTTP $($response.Status) (docs\openapi\api.yaml missing?)."
    Assert-True ($response.ContentType -match 'application/yaml') "GET /api/openapi.yaml Content-Type is '$($response.ContentType)', expected application/yaml."
    Assert-True ($response.Bytes -gt 100000) "GET /api/openapi.yaml returned only $($response.Bytes) bytes, expected more than 100000."
}

Invoke-SmokeCheck -Number 5 -Name "API GET /api/versions without a token is 401" -Body {
    $response = Invoke-RawHttp -Url "$apiBase/api/versions"
    Assert-True ($response.Status -eq 401) "GET /api/versions without a token returned HTTP $($response.Status), expected 401."
}

Invoke-SmokeCheck -Number 6 -Name "Admin login works and GET /api/auth/me is admin" -Body {
    $loginBody = ConvertTo-Json -Compress @{ username = $adminUsername; password = $adminPassword }
    $login = Invoke-RawHttp -Url "$apiBase/api/auth/login" -Method "POST" -BodyJson $loginBody
    Assert-True ($login.Status -eq 200) "POST /api/auth/login returned HTTP $($login.Status) (wrong password in .env or the admin was not seeded?)."
    $loginJson = ConvertFrom-JsonSafe $login.Body
    $token = "$($loginJson.data.token)"
    Assert-True ($token.Length -gt 0) "POST /api/auth/login did not return a token."
    $me = Invoke-RawHttp -Url "$apiBase/api/auth/me" -Headers @{ Authorization = "Bearer $token" }
    Assert-True ($me.Status -eq 200) "GET /api/auth/me returned HTTP $($me.Status)."
    $meJson = ConvertFrom-JsonSafe $me.Body
    Assert-True ("$($meJson.data.username)" -eq $adminUsername) "GET /api/auth/me username is '$($meJson.data.username)', expected '$adminUsername'."
}

Invoke-SmokeCheck -Number 7 -Name "Executor /healthz is 200 with a browserVersion" -Body {
    $response = Invoke-RawHttp -Url "$execBase/healthz"
    Assert-True ($response.Status -eq 200) "Executor /healthz returned HTTP $($response.Status): $(if ($response.Body.Length -gt 400) { $response.Body.Substring(0, 400) } else { $response.Body })"
    $json = ConvertFrom-JsonSafe $response.Body
    Assert-True ($json.status -eq "ok") "Executor status is '$($json.status)'."
    Assert-True (-not [string]::IsNullOrWhiteSpace("$($json.browserVersion)")) "Executor reports no browserVersion: the bundled Chromium did not launch."
}

Invoke-SmokeCheck -Number 8 -Name "Executor round-trip: context, goto /login, snapshot, screenshot, delete" -Body {
    $executorHeaders = @{ Authorization = "Bearer $executorToken" }
    $createBody = ConvertTo-Json -Compress -Depth 6 @{
        key          = "smoke-" + [DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds()
        allowedHosts = @("localhost", "127.0.0.1")
        baseUrl      = $webBase
        viewport     = @{ width = 1280; height = 720 }
    }
    $created = Invoke-RawHttp -Url "$execBase/v1/contexts" -Method "POST" -Headers $executorHeaders -BodyJson $createBody
    Assert-True ($created.Status -eq 200) "POST /v1/contexts returned HTTP $($created.Status): $($created.Body)"
    $createdJson = ConvertFrom-JsonSafe $created.Body
    $contextId = "$($createdJson.contextId)"
    Assert-True ($contextId.Length -gt 0) "POST /v1/contexts returned no contextId."
    try {
        $actionUrl = "$execBase/v1/contexts/$contextId/actions"
        $gotoBody = ConvertTo-Json -Compress -Depth 5 @{ action = "goto"; args = @{ url = "$webBase/login" } }
        $goto = Invoke-RawHttp -Url $actionUrl -Method "POST" -Headers $executorHeaders -BodyJson $gotoBody
        $gotoJson = ConvertFrom-JsonSafe $goto.Body
        Assert-True ($gotoJson.ok -eq $true) "goto failed: $($goto.Body)"

        $snapshotBody = ConvertTo-Json -Compress -Depth 5 @{ action = "snapshot"; args = @{} }
        $snapshot = Invoke-RawHttp -Url $actionUrl -Method "POST" -Headers $executorHeaders -BodyJson $snapshotBody
        $snapshotJson = ConvertFrom-JsonSafe $snapshot.Body
        Assert-True ($snapshotJson.ok -eq $true) "snapshot failed: $($snapshot.Body)"
        Assert-True (-not [string]::IsNullOrWhiteSpace("$($snapshotJson.result.aria)")) "snapshot returned an empty aria tree for $webBase/login."

        $screenshotBody = ConvertTo-Json -Compress -Depth 5 @{ action = "screenshot"; args = @{} }
        $screenshot = Invoke-RawHttp -Url $actionUrl -Method "POST" -Headers $executorHeaders -BodyJson $screenshotBody
        $screenshotJson = ConvertFrom-JsonSafe $screenshot.Body
        Assert-True ($screenshotJson.ok -eq $true) "screenshot failed: $($screenshot.Body)"
        $artifacts = @($screenshotJson.artifacts)
        Assert-True ($artifacts.Count -gt 0) "screenshot returned no artifacts."
        $base64 = "$($artifacts[0].base64)"
        Assert-True ($base64.Length -gt 0) "screenshot artifact has no base64 payload."
        $bytes = [Convert]::FromBase64String($base64)
        Assert-True ($bytes.Length -gt 0) "screenshot artifact decoded to 0 bytes."
        Assert-True (($bytes.Length -ge 8 -and $bytes[0] -eq 0x89 -and $bytes[1] -eq 0x50 -and $bytes[2] -eq 0x4E -and $bytes[3] -eq 0x47)) "screenshot artifact is not a PNG (magic bytes differ)."
    } finally {
        $deleted = Invoke-RawHttp -Url "$execBase/v1/contexts/$contextId" -Method "DELETE" -Headers $executorHeaders
        $deletedJson = $null
        try { $deletedJson = ConvertFrom-JsonSafe $deleted.Body } catch { }
        if ($deleted.Status -ne 200 -or $null -eq $deletedJson -or $deletedJson.ok -ne $true) {
            Write-Host ("         WARNING: could not delete executor context {0} (HTTP {1})." -f $contextId, $deleted.Status) -ForegroundColor Yellow
        }
    }
}

Invoke-SmokeCheck -Number 9 -Name "Web GET /login is 200 and renders EzTest" -Body {
    $response = Invoke-RawHttp -Url "$webBase/login"
    Assert-True ($response.Status -eq 200) "GET /login returned HTTP $($response.Status) (web .next/static assets missing?)."
    Assert-True ($response.Body -match 'EzTest') "GET /login returned 200 but the body does not contain 'EzTest'."
}

Invoke-SmokeCheck -Number 10 -Name "Data directories exist" -Body {
    $missing = New-Object System.Collections.ArrayList
    foreach ($name in @("objects", "version-attachments", "evidence", "reports", "exports")) {
        $path = Join-Path (Join-Path $PackageRoot "data") $name
        if (-not (Test-Path -LiteralPath $path -PathType Container)) { [void]$missing.Add($name) }
    }
    Assert-True ($missing.Count -eq 0) ("Missing data directories: " + ($missing -join ", "))
}

Write-Host ""
if ($script:Failures.Count -eq 0) {
    Write-Host ("Smoke test PASSED ({0}/{1} checks)." -f $script:PassedChecks, $script:TotalChecks) -ForegroundColor Green
    exit 0
}
Write-Host ("Smoke test FAILED ({0}/{1} checks passed)." -f $script:PassedChecks, $script:TotalChecks) -ForegroundColor Red
foreach ($failure in $script:Failures) { Write-Host "  - $failure" -ForegroundColor Red }
Write-Host ""
Write-Host "Check logs\ and PACKAGE.md troubleshooting. Common causes:" -ForegroundColor Yellow
Write-Host "  - PostgreSQL 17 not running or EZTEST_DATABASE_URL uses the placeholder."
Write-Host "  - The admin password in .env does not match the seeded admin."
Write-Host "  - Missing Linux browser system libraries (Linux package: install-linux-deps.sh)."
exit 1
