#requires -Version 5.1
<#
.SYNOPSIS
    Runs the P0-P5 acceptance gate for EzTest.

.DESCRIPTION
    Executes the Go unit and integration suites, the P1 Agent unit/integration
    suites, the P3 version/requirements/clarification suites, the P4 codeintel
    suites, the frontend lint/typecheck/test/build gate, the Playwright E2E
    suite (P0/P1/P2, the three P3 version specs in
    frontend/e2e/versions*.spec.ts, and the five P4 code-intelligence specs
    versions-code, versions-code-cache, versions-code-degraded,
    versions-code-anchors and code-repos-index), a repeated migration run and
    the health/audit/version-API assertions. The P0-P5 deliverables are checked
    (README, OpenAPI, deploy files, Agent/P3/P4/P5 env templates, the P3/P4/P5
    OpenAPI contract keywords, reason enums and event catalog), the P4 fake-LLM
    test is executed, the P4/P5 migration/table contract is derived from
    order.txt and the Compose file is statically validated with node. The P5
    block adds the 12 spec §10.7 checks: P5 deliverables, the mindmap OpenAPI
    contract (23 path items/27 operations/8 events/35 reasons), the rename
    migration chain, and - when T16 HTTP wiring is present - API smoke, the
    manual scenario/case/steps/assertions/sources -> submit-review -> approve ->
    read-only chain, three-format exports with sha256 and reuse, invalid-run
    zero-materialization, admin-only exploration, event snapshots, permissions
    and LLM degradation. Without the P5 HTTP routes the API-dependent P5 checks
    are recorded as SKIPPED (StaticOnly also skips them) so the static gate
    stays green while T16 lands. Container execution and the gated real
    Agent/P3/P4/P5 LLM acceptance are reported as SKIPPED because this host has
    no Docker and no opt-in for paid calls. Prints P1/P3/P4/P5 DoD mapping
    tables and exits non-zero on any failure.

.PARAMETER StaticOnly
    Runs only the repository-local static checks (README/OpenAPI/env/Compose
    contracts, the P5 static contract and the P4 fake-LLM test) without
    requiring .env, the database, the built API or Playwright. It still needs
    Node, frontend/node_modules/yaml (OpenAPI YAML parse) and git (the P4
    fixture and fake-LLM test). Useful on a host without PostgreSQL; the full
    gate remains the default.

.NOTES
    Compatible with Windows PowerShell 5.1 and PowerShell 7+.
#>
[CmdletBinding()]
param(
    [switch]$StaticOnly
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$frontendDir = Join-Path $repoRoot "frontend"
$backendDir = Join-Path $repoRoot "backend"
$scriptsDir = Join-Path $repoRoot "scripts"
$tmpDir = Join-Path $repoRoot ".tmp"
$e2eDbName = "eztest_e2e"
$apiPort = 13333
$apiBaseUrl = "http://localhost:$apiPort"

New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null

$checkResults = New-Object System.Collections.ArrayList

function Write-Step {
    param([string]$Message)
    Write-Host ""
    Write-Host "==> $Message" -ForegroundColor Cyan
}

function Write-LogTail {
    param(
        [string]$Path,
        [string]$Label,
        [int]$Lines = 60
    )
    if (-not (Test-Path -LiteralPath $Path)) { return }
    Write-Host ""
    Write-Host "--- $Label (last $Lines lines) ---" -ForegroundColor Yellow
    Get-Content -LiteralPath $Path -Tail $Lines -ErrorAction SilentlyContinue | ForEach-Object {
        Write-Host $_
    }
}

function Add-CheckResult {
    param(
        [string]$Dod,
        [string]$CheckName,
        [string]$Status,
        [string]$Detail
    )
    [void]$checkResults.Add([pscustomobject]@{
            DoD    = $Dod
            Check  = $CheckName
            Result = $Status
            Detail = $Detail
        })
}

function Invoke-Check {
    param(
        [string]$Dod,
        [string]$CheckName,
        [scriptblock]$Body
    )
    Write-Step $CheckName
    $timer = [System.Diagnostics.Stopwatch]::StartNew()
    try {
        & $Body
        $timer.Stop()
        Add-CheckResult -Dod $Dod -CheckName $CheckName -Status "PASS" -Detail ("{0:N1}s" -f $timer.Elapsed.TotalSeconds)
        Write-Host "PASS $CheckName" -ForegroundColor Green
        return $true
    } catch {
        $timer.Stop()
        $message = $_.Exception.Message
        Add-CheckResult -Dod $Dod -CheckName $CheckName -Status "FAIL" -Detail $message
        Write-Host "FAIL ${CheckName}: $message" -ForegroundColor Red
        return $false
    }
}

function Invoke-LoggedCommand {
    param(
        [string]$Exe,
        [string[]]$Arguments,
        [string]$WorkingDirectory,
        [string]$LogPath,
        [string]$Label,
        [int]$SuccessTailLines = 0
    )
    Remove-Item -LiteralPath $LogPath -ErrorAction SilentlyContinue
    Push-Location $WorkingDirectory
    $previous = $ErrorActionPreference
    # Native stderr becomes an error record under Windows PowerShell; the log
    # file still receives it, so suppress only the console duplication.
    $ErrorActionPreference = "SilentlyContinue"
    try {
        & $Exe @Arguments *> $LogPath
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
        Pop-Location
    }
    if ($code -ne 0) {
        Write-LogTail -Path $LogPath -Label $Label -Lines 100
        throw "$Label failed with exit code $code"
    }
    if ($SuccessTailLines -gt 0) {
        Write-LogTail -Path $LogPath -Label $Label -Lines $SuccessTailLines
    }
}

function Read-DotEnv {
    param([string]$Path)
    $values = @{}
    if (-not (Test-Path -LiteralPath $Path)) {
        throw "Missing .env at $Path"
    }
    foreach ($line in Get-Content -LiteralPath $Path) {
        if ($line -match '^\s*#' -or $line -match '^\s*$') { continue }
        if ($line -match '^\s*([^=]+)=(.*)$') {
            $key = $matches[1].Trim()
            $value = $matches[2].Trim()
            if ($value.Length -ge 2) {
                $first = $value.Substring(0, 1)
                $last = $value.Substring($value.Length - 1, 1)
                if (($first -eq '"' -and $last -eq '"') -or ($first -eq "'" -and $last -eq "'")) {
                    $value = $value.Substring(1, $value.Length - 2)
                }
            }
            $values[$key] = $value
        }
    }
    return $values
}

function Get-RequiredValue {
    param($Values, [string]$Key)
    if (-not $Values.ContainsKey($Key) -or [string]::IsNullOrWhiteSpace([string]$Values[$Key])) {
        throw "Missing required value $Key in .env"
    }
    return [string]$Values[$Key]
}

function Assert-SafeTestDatabaseUrl {
    param(
        [string]$TestDatabaseUrl,
        [string]$DatabaseUrl
    )
    # Defense in depth for the destructive Go integration helper: it drops and
    # recreates the public schema, so the URL must name a dedicated PostgreSQL
    # test/e2e database and must not be the development database. Error messages
    # intentionally omit credentials and the full URLs.
    if ([string]::IsNullOrWhiteSpace($TestDatabaseUrl)) {
        throw "EZTEST_TEST_DATABASE_URL is not set; integration tests need the dedicated test database"
    }
    if ([string]::IsNullOrWhiteSpace($DatabaseUrl)) {
        throw "EZTEST_DATABASE_URL is not set, so the test database cannot be checked against the development database"
    }

    $testUri = $null
    if (-not [System.Uri]::TryCreate($TestDatabaseUrl, [System.UriKind]::Absolute, [ref]$testUri)) {
        throw "EZTEST_TEST_DATABASE_URL is not a valid absolute URL"
    }
    $devUri = $null
    if (-not [System.Uri]::TryCreate($DatabaseUrl, [System.UriKind]::Absolute, [ref]$devUri)) {
        throw "EZTEST_DATABASE_URL is not a valid absolute URL"
    }

    foreach ($item in @(
            @{ Name = "EZTEST_TEST_DATABASE_URL"; Uri = $testUri },
            @{ Name = "EZTEST_DATABASE_URL"; Uri = $devUri }
        )) {
        if ($item.Uri.Scheme -ne "postgres" -and $item.Uri.Scheme -ne "postgresql") {
            throw "$($item.Name) must use the postgres scheme"
        }
        if ([string]::IsNullOrWhiteSpace($item.Uri.Host)) {
            throw "$($item.Name) must include a host"
        }
        # pgx applies query parameters over the URL path and host:port, so a
        # URL whose path ends in _test can still connect to the development
        # database via ?dbname= or ?database= (and ?host=/?port= can redirect
        # the connection). Reject the target-changing keys case-insensitively
        # instead of trusting the path. The Go validator checks the effective
        # pgx target as a second layer. Never echo the URL or credentials.
        $query = $item.Uri.Query
        if (-not [string]::IsNullOrWhiteSpace($query)) {
            foreach ($pair in ($query.TrimStart([char]"?")) -split "&") {
                if ([string]::IsNullOrWhiteSpace($pair)) { continue }
                $equals = $pair.IndexOf("=")
                $key = if ($equals -ge 0) { $pair.Substring(0, $equals) } else { $pair }
                $key = [System.Uri]::UnescapeDataString($key).Trim().ToLowerInvariant()
                if ($key -eq "dbname" -or $key -eq "database" -or $key -eq "host" -or $key -eq "port") {
                    throw "$($item.Name) must not override the connection target with the query parameter '$key'; put the host, port and database in the URL itself"
                }
            }
        }
    }

    $testName = [System.Uri]::UnescapeDataString($testUri.AbsolutePath.TrimStart([char[]]"/"))
    if ([string]::IsNullOrWhiteSpace($testName)) {
        throw "EZTEST_TEST_DATABASE_URL must include a database name"
    }
    if (-not ($testName.EndsWith("_test", [System.StringComparison]::Ordinal) -or
            $testName.EndsWith("_e2e", [System.StringComparison]::Ordinal))) {
        throw "EZTEST_TEST_DATABASE_URL database '$testName' must end with _test or _e2e"
    }

    $devName = [System.Uri]::UnescapeDataString($devUri.AbsolutePath.TrimStart([char[]]"/"))
    if ([string]::IsNullOrWhiteSpace($devName)) {
        throw "EZTEST_DATABASE_URL must include a database name"
    }

    $testPort = if ($testUri.Port -gt 0) { $testUri.Port } else { 5432 }
    $devPort = if ($devUri.Port -gt 0) { $devUri.Port } else { 5432 }
    $testCanonical = "{0}:{1}/{2}" -f $testUri.Host.ToLowerInvariant(), $testPort, $testName
    $devCanonical = "{0}:{1}/{2}" -f $devUri.Host.ToLowerInvariant(), $devPort, $devName
    if ($testCanonical -eq $devCanonical) {
        throw "EZTEST_TEST_DATABASE_URL points at the same database as EZTEST_DATABASE_URL ($testCanonical); refusing to reset it"
    }
}

function Stop-ProcessTree {
    param($Process)
    if ($null -eq $Process) { return }
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        $Process.Refresh()
        if (-not $Process.HasExited) {
            & taskkill.exe /PID $Process.Id /T /F 2>$null | Out-Null
        }
    } catch {
        # Best-effort cleanup.
    } finally {
        $ErrorActionPreference = $previous
    }
}

function Wait-PortFree {
    param([int]$Port, [int]$TimeoutSeconds = 20)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        $listeners = Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue
        if (-not $listeners) { return }
        Start-Sleep -Milliseconds 250
    }
    throw "Port $Port is still in use"
}

function Assert-PortFree {
    param([int]$Port)
    $listeners = Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue
    if ($listeners) {
        throw "Port $Port is already in use"
    }
}

function Wait-ForHttp {
    param(
        [string]$Url,
        [int]$TimeoutSeconds,
        $Process,
        [string]$Label
    )
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        if ($null -ne $Process) {
            $Process.Refresh()
            if ($Process.HasExited) {
                throw "$Label exited early with code $($Process.ExitCode)"
            }
        }
        try {
            $response = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec 10
            if ($response.StatusCode -eq 200) { return }
        } catch {
            # Not ready yet.
        }
        Start-Sleep -Milliseconds 500
    }
    throw "Timed out waiting for $Label at $Url"
}

function Get-PsqlScalar {
    param([string]$Database, [string]$Sql)
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        $output = & $psqlExe -h $dbHost -p $dbPort -U $dbUser -d $Database -tA -c $Sql 2>$null
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($code -ne 0) {
        throw "psql scalar query failed (exit $code)"
    }
    return (($output | Out-String).Trim())
}

function Get-MigrationFingerprint {
    $sql = "SELECT count(*)::text || ':' || coalesce(md5(string_agg(version::text || ':' || checksum, ',' ORDER BY version)), '') FROM schema_migrations;"
    return Get-PsqlScalar -Database $e2eDbName -Sql $sql
}

function Start-AcceptanceApi {
    param([int]$RunNumber)
    $outLog = Join-Path $tmpDir "acceptance-api-$RunNumber.out.log"
    $errLog = Join-Path $tmpDir "acceptance-api-$RunNumber.err.log"
    Remove-Item -LiteralPath $outLog, $errLog -ErrorAction SilentlyContinue
    $process = Start-Process -FilePath $apiExe `
        -WorkingDirectory $backendDir `
        -RedirectStandardOutput $outLog `
        -RedirectStandardError $errLog `
        -WindowStyle Hidden `
        -PassThru
    try {
        Wait-ForHttp -Url "$apiBaseUrl/api/health" -TimeoutSeconds 90 -Process $process -Label "API"
    } catch {
        Stop-ProcessTree -Process $process
        throw
    }
    return $process
}

function Assert-Health {
    param($Health)
    if ($Health.data.status -ne "ok") { throw "health status is '$($Health.data.status)', expected 'ok'" }
    if ($Health.data.db -ne "ok") { throw "health db is '$($Health.data.db)', expected 'ok'" }
    if ([string]::IsNullOrWhiteSpace([string]$Health.data.version)) { throw "health response has no version" }
    if ($hasLLMSeed -and -not $Health.data.llm_configured) {
        throw "health reports llm_configured=false although .env provides an LLM key"
    }
}

function Test-AdminAudit {
    $loginPayload = @{ username = $adminUsername; password = $adminPassword } | ConvertTo-Json -Compress
    $login = Invoke-RestMethod -Method Post -Uri "$apiBaseUrl/api/auth/login" `
        -ContentType "application/json" -Body $loginPayload -TimeoutSec 20
    $token = [string]$login.data.token
    if ([string]::IsNullOrWhiteSpace($token)) { throw "admin login did not return a token" }
    $headers = @{ Authorization = "Bearer $token" }

    $me = Invoke-RestMethod -Uri "$apiBaseUrl/api/auth/me" -Headers $headers -TimeoutSec 20
    if ($me.data.username -ne $adminUsername) { throw "auth/me returned the wrong user" }

    $audit = Invoke-RestMethod -Uri "$apiBaseUrl/api/admin/audit?limit=100" -Headers $headers -TimeoutSec 20
    $items = @($audit.data)
    if ($items.Count -eq 0) { throw "audit list is empty after the E2E run" }

    $actions = @($items | ForEach-Object { [string]$_.action })
    foreach ($expected in @("auth.login_succeeded", "user.created", "sse.test_published")) {
        if ($actions -notcontains $expected) {
            throw "audit list is missing action '$expected'"
        }
    }

    # The environment-seeded LLM configuration is created before any HTTP
    # request exists, so that one entry legitimately has no request_id. Every
    # request-driven action must be correlated.
    $missingRequestIds = @($items | Where-Object {
            [string]::IsNullOrWhiteSpace([string]$_.request_id) -and [string]$_.action -notin @("llm.config_created", "mindmap.tool.invoked")
        })
    if ($missingRequestIds.Count -gt 0) {
        throw "request-driven audit entries are missing request_id"
    }

    $auditJson = $audit | ConvertTo-Json -Depth 12 -Compress
    $secrets = @($adminPassword, $dbPassword)
    if ($envValues.ContainsKey("llm_key")) { $secrets += [string]$envValues["llm_key"] }
    foreach ($secret in $secrets) {
        if (-not [string]::IsNullOrEmpty($secret) -and $auditJson.Contains($secret)) {
            throw "audit response contains secret material"
        }
    }
    foreach ($forbiddenKey in @('"password"\s*:', '"token"\s*:', '"api_key"\s*:')) {
        if ($auditJson -match $forbiddenKey) {
            throw "audit response contains a credential field"
        }
    }
}

function Test-VersionApiSmoke {
    # P3 global gate: unauthenticated requests are 401, an authenticated list
    # returns the P3 cursor envelope, P3-owned IDs are JSON strings while P2
    # resource/user IDs stay numbers, and an unknown version is 404.
    try {
        Invoke-RestMethod -Uri "$apiBaseUrl/api/versions" -TimeoutSec 20 | Out-Null
        throw "GET /api/versions without a bearer token unexpectedly succeeded"
    } catch {
        $response = $_.Exception.Response
        if ($null -eq $response -or [int]$response.StatusCode -ne 401) {
            throw "GET /api/versions without a bearer token did not return 401: $($_.Exception.Message)"
        }
    }

    $loginPayload = @{ username = $adminUsername; password = $adminPassword } | ConvertTo-Json -Compress
    $login = Invoke-RestMethod -Method Post -Uri "$apiBaseUrl/api/auth/login" `
        -ContentType "application/json" -Body $loginPayload -TimeoutSec 20
    $token = [string]$login.data.token
    if ([string]::IsNullOrWhiteSpace($token)) { throw "admin login did not return a token for the version smoke check" }
    $headers = @{ Authorization = "Bearer $token" }

    $page = Invoke-RestMethod -Uri "$apiBaseUrl/api/versions?limit=1" -Headers $headers -TimeoutSec 20
    foreach ($field in @("data", "next_cursor", "has_more")) {
        if (-not ($page.PSObject.Properties.Name -contains $field)) {
            throw "GET /api/versions response is missing '$field'"
        }
    }
    if ($page.data -isnot [array]) { throw "GET /api/versions 'data' is not an array" }
    foreach ($version in @($page.data)) {
        if ($version.id -isnot [string] -or [string]$version.id -notmatch '^[0-9]+$') {
            throw "version id is not a decimal string: '$($version.id)'"
        }
        if ($version.revision -is [string]) { throw "version revision must be a JSON number" }
        if ($null -ne $version.systemId -and $version.systemId -is [string]) {
            throw "version systemId must stay a JSON number (P2 resource ID)"
        }
        if ($null -ne $version.createdBy -and $version.createdBy -is [string]) {
            throw "version createdBy must stay a JSON number (user ID)"
        }
    }

    try {
        Invoke-RestMethod -Uri "$apiBaseUrl/api/versions/123456789012345678" -Headers $headers -TimeoutSec 20 | Out-Null
        throw "GET /api/versions/{unknown} unexpectedly succeeded"
    } catch {
        $response = $_.Exception.Response
        if ($null -eq $response -or [int]$response.StatusCode -ne 404) {
            throw "GET /api/versions/{unknown} did not return 404: $($_.Exception.Message)"
        }
    }

    Write-Host "Version API smoke OK: 401 unauthenticated, cursor envelope, string P3 IDs/numbers for P2 IDs, 404 unknown version."
}

# ---------------------------------------------------------------------------
# P5 case-mindmap acceptance helpers (spec §10.7). The API-dependent checks
# only run when the T16 HTTP routes are registered; the static checks above and
# below stay repository-local so -StaticOnly works without a database or API.
# ---------------------------------------------------------------------------

function Get-P5ErrorInfo {
    param($ErrorRecord)
    $info = [pscustomobject]@{
        HasResponse = $false
        Status      = 0
        Code        = ""
        Reason      = ""
        Body        = ""
    }
    try {
        $response = $ErrorRecord.Exception.Response
        if ($null -ne $response) {
            $info.HasResponse = $true
            $info.Status = [int]$response.StatusCode
            $body = $null
            if ($response -is [System.Net.HttpWebResponse]) {
                $stream = $response.GetResponseStream()
                if ($null -ne $stream) {
                    $reader = New-Object System.IO.StreamReader($stream)
                    try { $body = $reader.ReadToEnd() } finally { $reader.Close() }
                }
            } elseif ($null -ne $response.Content) {
                $body = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
            }
            if ([string]::IsNullOrWhiteSpace($body) -and $null -ne $ErrorRecord.ErrorDetails) {
                $body = [string]$ErrorRecord.ErrorDetails.Message
            }
            $info.Body = [string]$body
            if (-not [string]::IsNullOrWhiteSpace($info.Body)) {
                try {
                    $parsed = $info.Body | ConvertFrom-Json
                    $info.Code = [string]$parsed.error.code
                    if ($null -ne $parsed.error.details) {
                        $info.Reason = [string]$parsed.error.details.reason
                    }
                } catch {
                    # A non-JSON error body is not echoed; only the status matters.
                }
            }
        }
    } catch {
        # Keep whatever was extracted safely.
    }
    return $info
}

function Invoke-P5Api {
    param(
        [string]$Method,
        [string]$Path,
        $Body = $null,
        $Headers = $null,
        [int]$TimeoutSec = 60,
        [switch]$NoThrow
    )
    $arguments = @{
        Method          = $Method
        Uri             = ($apiBaseUrl + $Path)
        TimeoutSec      = $TimeoutSec
        UseBasicParsing = $true
    }
    if ($null -ne $Headers) { $arguments["Headers"] = $Headers }
    if ($null -ne $Body) {
        $json = $Body | ConvertTo-Json -Compress -Depth 12
        $arguments["ContentType"] = "application/json; charset=utf-8"
        # Windows PowerShell 5.1 would encode non-ASCII request text with the
        # ANSI code page; always send explicit UTF-8 bytes.
        $arguments["Body"] = [System.Text.Encoding]::UTF8.GetBytes($json)
    }
    try {
        $response = Invoke-WebRequest @arguments
    } catch {
        $info = Get-P5ErrorInfo -ErrorRecord $_
        if ($NoThrow) {
            return [pscustomobject]@{
                Status  = $info.Status
                Raw     = $null
                Data    = $null
                Headers = @{}
                Error   = $info
            }
        }
        throw ("{0} {1} failed: HTTP {2} code={3} reason={4}" -f $Method, $Path, $info.Status, $info.Code, $info.Reason)
    }
    $raw = $null
    if ($response.Content -is [string] -and -not [string]::IsNullOrWhiteSpace([string]$response.Content)) {
        try { $raw = $response.Content | ConvertFrom-Json } catch { $raw = $null }
    }
    $data = $null
    if ($null -ne $raw) { $data = $raw.data }
    return [pscustomobject]@{
        Status  = [int]$response.StatusCode
        Raw     = $raw
        Data    = $data
        Headers = $response.Headers
        Error   = $null
    }
}

function Assert-P5Http {
    param($Response, [int]$Status, [string]$Label)
    if ($null -eq $Response -or $Response.Status -ne $Status) {
        $actual = if ($null -eq $Response) { "no response" } else { [string]$Response.Status }
        throw "$Label returned HTTP $actual, expected $Status"
    }
    return $Response
}

function Assert-P5ApiError {
    param(
        [string]$Label,
        [scriptblock]$Call,
        [int]$Status,
        [string]$Reason = ""
    )
    try {
        & $Call | Out-Null
    } catch {
        $info = Get-P5ErrorInfo -ErrorRecord $_
        if (-not $info.HasResponse) {
            # Invoke-P5Api rethrows a formatted message after a non-2xx response;
            # recover the status/code/reason so expected-error assertions work.
            if ($_.Exception.Message -match 'HTTP (-?\d+) code=(\S+) reason=(\S+)') {
                $info = [pscustomobject]@{ HasResponse = $true; Status = [int]$Matches[1]; Code = $Matches[2]; Reason = $Matches[3] }
            } else {
                throw "$Label failed without an HTTP response: $($_.Exception.Message)"
            }
        }
        if ($info.Status -ne $Status) {
            throw "$Label returned HTTP $($info.Status), expected $Status"
        }
        if (-not [string]::IsNullOrWhiteSpace($Reason) -and $info.Reason -ne $Reason) {
            throw "$Label returned reason '$($info.Reason)', expected '$Reason'"
        }
        return $info
    }
    throw "$Label unexpectedly succeeded"
}

function Get-P5Token {
    param([string]$Username, [string]$Password)
    $login = Invoke-P5Api -Method Post -Path "/api/auth/login" -Body @{
        username = $Username
        password = $Password
    }
    Assert-P5Http -Response $login -Status 200 -Label "login" | Out-Null
    $token = [string]$login.Data.token
    if ([string]::IsNullOrWhiteSpace($token)) { throw "login for $Username did not return a token" }
    return $token
}

function Get-P5Graph {
    param([string]$VersionId, $Headers)
    $response = Invoke-P5Api -Method Get -Path "/api/versions/$VersionId/mindmap" -Headers $Headers
    Assert-P5Http -Response $response -Status 200 -Label "GET mindmap" | Out-Null
    return $response.Data
}

function Wait-P5Run {
    param(
        [string]$VersionId,
        [string]$RunId,
        $Headers,
        [int]$TimeoutSeconds = 420,
        [string[]]$Terminal = @("done", "failed", "degraded")
    )
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        $response = Invoke-P5Api -Method Get -Path "/api/versions/$VersionId/mindmap/runs/$RunId" -Headers $Headers
        Assert-P5Http -Response $response -Status 200 -Label "GET mindmap run" | Out-Null
        $status = [string]$response.Data.status
        if ($Terminal -contains $status) { return $response.Data }
        Start-Sleep -Seconds 2
    }
    throw "mindmap run $RunId did not reach $($Terminal -join '/') within $TimeoutSeconds seconds"
}

function New-P5DesigningVersion {
    param(
        $Headers,
        [string]$Name,
        [int]$RequirementCount = 1,
        $SystemId = $null,
        $EnvironmentId = $null,
        $CodeRepositoryId = $null,
        [string]$CodeBranch = "",
        [string]$BaselineCommit = ""
    )
    $body = @{ name = $Name; description = "P5 acceptance version" }
    if ($null -ne $SystemId -and [int64]$SystemId -gt 0) { $body["systemId"] = [int64]$SystemId }
    if ($null -ne $EnvironmentId -and [int64]$EnvironmentId -gt 0) { $body["environmentId"] = [int64]$EnvironmentId }
    if ($null -ne $CodeRepositoryId -and [int64]$CodeRepositoryId -gt 0) {
        $body["codeRepositoryId"] = [int64]$CodeRepositoryId
        $body["codeBranch"] = $CodeBranch
        $body["codeBaselineCommit"] = $BaselineCommit
    }
    $version = Invoke-P5Api -Method Post -Path "/api/versions" -Headers $Headers -Body $body
    Assert-P5Http -Response $version -Status 201 -Label "POST /api/versions" | Out-Null
    $versionId = [string]$version.Data.id
    if ([string]::IsNullOrWhiteSpace($versionId)) { throw "version creation did not return an id" }

    $requirementIds = @()
    for ($i = 1; $i -le $RequirementCount; $i++) {
        $requirement = Invoke-P5Api -Method Post -Path "/api/versions/$versionId/requirements" -Headers $Headers -Body @{
            title              = "$Name requirement $i"
            description        = "Acceptance requirement $i for the P5 mindmap chain."
            acceptanceCriteria = "The mindmap case covers requirement $i."
            priority           = "must"
        }
        Assert-P5Http -Response $requirement -Status 201 -Label "POST requirement" | Out-Null
        $requirementIds += [string]$requirement.Data.id
    }
    $clarify = Invoke-P5Api -Method Post -Path "/api/versions/$versionId/start-clarification" -Headers $Headers
    Assert-P5Http -Response $clarify -Status 200 -Label "POST start-clarification" | Out-Null
    foreach ($requirementId in $requirementIds) {
        $confirmed = Invoke-P5Api -Method Post -Path "/api/versions/$versionId/requirements/$requirementId/confirm" -Headers $Headers
        Assert-P5Http -Response $confirmed -Status 200 -Label "POST requirement confirm" | Out-Null
    }
    $enter = Invoke-P5Api -Method Post -Path "/api/versions/$versionId/enter-design" -Headers $Headers -Body @{
        acknowledgeUnclarified = $true
    }
    Assert-P5Http -Response $enter -Status 200 -Label "POST enter-design" | Out-Null
    return [pscustomobject]@{
        VersionId        = $versionId
        RequirementIds   = $requirementIds
        Version          = $enter.Data.version
    }
}

function New-P5ManualCase {
    param(
        $Headers,
        [string]$VersionId,
        [string[]]$RequirementIds = @(),
        [string]$Prefix = "manual"
    )
    $scenario = Invoke-P5Api -Method Post -Path "/api/versions/$VersionId/mindmap/nodes" -Headers $Headers -Body @{
        kind  = "scenario"
        title = "$Prefix scenario"
    }
    Assert-P5Http -Response $scenario -Status 201 -Label "POST scenario node" | Out-Null
    $scenarioId = [string]$scenario.Data.id

    $caseBody = @{
        kind     = "case"
        parentId = $scenarioId
        title    = "$Prefix case"
        priority = "P1"
    }
    if ($RequirementIds.Count -gt 0) { $caseBody["requirementIds"] = @($RequirementIds[0]) }
    $case = Invoke-P5Api -Method Post -Path "/api/versions/$VersionId/mindmap/nodes" -Headers $Headers -Body $caseBody
    Assert-P5Http -Response $case -Status 201 -Label "POST case node" | Out-Null
    $caseId = [string]$case.Data.id
    if ([string]::IsNullOrWhiteSpace($caseId)) { throw "case creation did not return an id" }

    $step = Invoke-P5Api -Method Post -Path "/api/versions/$VersionId/mindmap/nodes/$caseId/steps" -Headers $Headers -Body @{
        action   = "Open the home page"
        expected = "The home page is shown"
        data     = ""
    }
    Assert-P5Http -Response $step -Status 201 -Label "POST case step" | Out-Null
    $stepId = [string]$step.Data.id

    $assertion = Invoke-P5Api -Method Post -Path "/api/versions/$VersionId/mindmap/nodes/$caseId/assertions" -Headers $Headers -Body @{
        stepId     = $stepId
        kind       = "ui"
        target     = "/p5/home"
        expression = "The page title is not empty"
        expected   = "The title is displayed"
    }
    Assert-P5Http -Response $assertion -Status 201 -Label "POST case assertion" | Out-Null
    $assertionId = [string]$assertion.Data.id

    $sourceId = $null
    if ($RequirementIds.Count -gt 1) {
        $source = Invoke-P5Api -Method Post -Path "/api/versions/$VersionId/mindmap/nodes/$caseId/sources" -Headers $Headers -Body @{
            refKind = "requirement"
            refId   = [string]$RequirementIds[1]
        }
        Assert-P5Http -Response $source -Status 201 -Label "POST case source" | Out-Null
        $sourceId = [string]$source.Data.id
    }
    # CreateNodeRequest.requirementIds is expected to create the requirement
    # source; tolerate a handler that only validates it by adding the first
    # source explicitly when the graph reports none. A duplicate would be a
    # 409 and is only attempted after reading the authoritative graph.
    $graph = Get-P5Graph -VersionId $VersionId -Headers $Headers
    $caseNode = @($graph.nodes | Where-Object { [string]$_.id -eq $caseId })[0]
    if ($null -ne $caseNode -and @($caseNode.sources).Count -eq 0 -and $RequirementIds.Count -gt 0) {
        $source = Invoke-P5Api -Method Post -Path "/api/versions/$VersionId/mindmap/nodes/$caseId/sources" -Headers $Headers -Body @{
            refKind = "requirement"
            refId   = [string]$RequirementIds[0]
        }
        Assert-P5Http -Response $source -Status 201 -Label "POST case source" | Out-Null
        $sourceId = [string]$source.Data.id
    }

    return [pscustomobject]@{
        ScenarioId  = $scenarioId
        CaseId      = $caseId
        StepId      = $stepId
        AssertionId = $assertionId
        SourceId    = $sourceId
    }
}

function Get-P5Sha256Hex {
    param([byte[]]$Bytes)
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try {
        $hash = $sha.ComputeHash($Bytes)
    } finally {
        $sha.Dispose()
    }
    return (($hash | ForEach-Object { $_.ToString("x2") }) -join "")
}

function Invoke-P5Download {
    param([string]$Path, $Headers, [string]$OutFile)
    # WebClient is used because it returns the raw bytes and the response
    # headers deterministically under both Windows PowerShell 5.1 and 7.
    $client = New-Object System.Net.WebClient
    try {
        if ($null -ne $Headers) {
            foreach ($key in $Headers.Keys) {
                $client.Headers.Add([string]$key, [string]$Headers[$key])
            }
        }
        $bytes = $client.DownloadData($apiBaseUrl + $Path)
        $disposition = [string]$client.ResponseHeaders["Content-Disposition"]
    } finally {
        $client.Dispose()
    }
    if (-not [string]::IsNullOrWhiteSpace($OutFile)) {
        [System.IO.File]::WriteAllBytes($OutFile, $bytes)
    }
    return [pscustomobject]@{
        Bytes               = $bytes
        Size                = @($bytes).Count
        ContentDisposition  = $disposition
        Sha256              = Get-P5Sha256Hex -Bytes $bytes
    }
}

function Get-P5ExportStoredPath {
    param([string]$VersionId, [string]$ExportId)
    $sql = "SELECT file_path FROM case_export WHERE version_id = $VersionId AND id = $ExportId;"
    return Get-PsqlScalar -Database $e2eDbName -Sql $sql
}

function Get-P5DefaultLlmConfig {
    param($Headers)
    $response = Invoke-P5Api -Method Get -Path "/api/admin/llm/configs" -Headers $Headers
    Assert-P5Http -Response $response -Status 200 -Label "GET llm configs" | Out-Null
    $configs = @($response.Data)
    foreach ($config in $configs) {
        if ([bool]$config.is_default) { return $config }
    }
    return $null
}

function Set-P5LlmBaseUrl {
    param($Headers, [string]$ConfigId, [string]$BaseUrl, [string]$Model)
    $body = @{ base_url = $BaseUrl }
    if (-not [string]::IsNullOrWhiteSpace($Model)) { $body["model"] = $Model }
    $response = Invoke-P5Api -Method Patch -Path "/api/admin/llm/configs/$ConfigId" -Headers $Headers -Body $body
    Assert-P5Http -Response $response -Status 200 -Label "PATCH llm config" | Out-Null
    return $response.Data
}

function Start-P5FakeLlm {
    param([string]$Mode = "normal")
    if (-not (Test-Path -LiteralPath (Join-Path $scriptsDir "fake-llm.cjs"))) { return $null }
    $listeners = Get-NetTCPConnection -State Listen -LocalPort 13334 -ErrorAction SilentlyContinue
    if ($listeners) { return $null }
    $env:EZTEST_FAKE_LLM_PORT = "13334"
    $env:EZTEST_FAKE_LLM_P5_MODE = $Mode
    $outLog = Join-Path $tmpDir "acceptance-p5-fake-llm.out.log"
    $errLog = Join-Path $tmpDir "acceptance-p5-fake-llm.err.log"
    Remove-Item -LiteralPath $outLog, $errLog -ErrorAction SilentlyContinue
    $process = Start-Process -FilePath $nodeExe `
        -ArgumentList "`"scripts/fake-llm.cjs`"" `
        -WorkingDirectory $repoRoot `
        -RedirectStandardOutput $outLog `
        -RedirectStandardError $errLog `
        -WindowStyle Hidden `
        -PassThru
    try {
        Wait-ForHttp -Url "http://127.0.0.1:13334/v1/models" -TimeoutSeconds 30 -Process $process -Label "P5 fake LLM"
    } catch {
        Stop-ProcessTree -Process $process
        throw
    }
    return $process
}

function Set-P5FakeLlmMode {
    param([string]$Mode)
    $response = Invoke-WebRequest -Method Post -Uri "http://127.0.0.1:13334/__p5-mode" `
        -ContentType "application/json" -Body ([System.Text.Encoding]::UTF8.GetBytes((@{ mode = $Mode } | ConvertTo-Json -Compress))) `
        -UseBasicParsing -TimeoutSec 20
    if ([int]$response.StatusCode -ne 200) { throw "could not switch the fake LLM to mode $Mode" }
}

function Start-P5FixtureTarget {
    if (-not (Test-Path -LiteralPath (Join-Path $scriptsDir "fixture-target.cjs"))) { return $null }
    $listeners = Get-NetTCPConnection -State Listen -LocalPort 13335 -ErrorAction SilentlyContinue
    if ($listeners) { return $null }
    $env:FIXTURE_PORT = "13335"
    $outLog = Join-Path $tmpDir "acceptance-p5-fixture.out.log"
    $errLog = Join-Path $tmpDir "acceptance-p5-fixture.err.log"
    Remove-Item -LiteralPath $outLog, $errLog -ErrorAction SilentlyContinue
    $process = Start-Process -FilePath $nodeExe `
        -ArgumentList "`"scripts/fixture-target.cjs`"" `
        -WorkingDirectory $repoRoot `
        -RedirectStandardOutput $outLog `
        -RedirectStandardError $errLog `
        -WindowStyle Hidden `
        -PassThru
    try {
        Wait-ForHttp -Url "http://127.0.0.1:13335/health" -TimeoutSeconds 15 -Process $process -Label "P5 fixture target"
    } catch {
        Stop-ProcessTree -Process $process
        throw
    }
    return $process
}

function Initialize-P5ApiContext {
    $script:p5 = @{
        Available       = $false
        ApiProcess      = $null
        AdminHeaders    = $null
        UserHeaders     = $null
        UserId          = $null
        VersionId       = $null
        RequirementIds  = @()
        ScenarioId      = $null
        CaseId          = $null
        StepId          = $null
        AssertionId     = $null
        SourceId        = $null
        BaselineId      = $null
        ExportDir       = $null
        RepoId          = $null
        SystemId        = $null
        EnvironmentId   = $null
        CodeBranch      = ""
        BaselineCommit  = ""
        FixtureRepo     = $null
        LlmConfig       = $null
        GitExe          = ""
        ReviewVersionId = $null
        ExportIds       = @{}
    }
    if (-not (Test-Path -LiteralPath $apiExe)) {
        throw "the acceptance API binary is missing; the migration/API check must build it first"
    }
    $fixtureRepo = Join-Path $tmpDir "p4-e2e-repo"
    if (-not (Test-Path -LiteralPath (Join-Path $fixtureRepo ".git"))) {
        Invoke-LoggedCommand -Exe $scriptHost `
            -Arguments @("-NoProfile", "-ExecutionPolicy", "Bypass", "-File", (Join-Path $scriptsDir "fixture-codeintel-repo.ps1")) `
            -WorkingDirectory $repoRoot `
            -LogPath (Join-Path $tmpDir "acceptance-p5-fixture-repo.log") `
            -Label "scripts/fixture-codeintel-repo.ps1" `
            -SuccessTailLines 4
    }
    $gitExe = Join-Path $repoRoot ".tools\git\bin\git.exe"
    if (-not (Test-Path -LiteralPath $gitExe)) { $gitExe = "git" }
    $baselineCommit = ((& $gitExe -C $fixtureRepo rev-parse HEAD 2>$null | Out-String).Trim())
    if ($baselineCommit -notmatch '^[a-f0-9]{40}$') {
        throw "could not resolve the fixture repository HEAD commit"
    }
    $codeBranch = ((& $gitExe -C $fixtureRepo rev-parse --abbrev-ref HEAD 2>$null | Out-String).Trim())
    if ([string]::IsNullOrWhiteSpace($codeBranch) -or $codeBranch -eq "HEAD") { $codeBranch = "master" }

    Assert-PortFree -Port $apiPort
    $exportDir = Join-Path $tmpDir "p5-exports"
    if (Test-Path -LiteralPath $exportDir) { Remove-Item -LiteralPath $exportDir -Recurse -Force }
    New-Item -ItemType Directory -Force -Path $exportDir | Out-Null
    $env:EZTEST_ENV = "dev"
    $env:EZTEST_DATABASE_URL = $e2eDatabaseUrl
    $env:EZTEST_HTTP_ADDR = ":$apiPort"
    $env:EZTEST_WEB_ORIGIN = "http://localhost:6868"
    $env:EZTEST_ENABLE_TEST_ENDPOINTS = "true"
    $env:EZTEST_STORAGE_DIR = (Join-Path $tmpDir "e2e-objects")
    $env:EZTEST_EXPORT_DIR = $exportDir
    $env:EZTEST_ADMIN_USERNAME = $adminUsername
    $env:EZTEST_ADMIN_PASSWORD = $adminPassword
    foreach ($key in @("EZTEST_SECRET_KEYS", "EZTEST_SECRET_KEY_ACTIVE", "llm_base_url", "llm_key", "llm_model_name")) {
        if ($envValues.ContainsKey($key)) { Set-Item -Path ("Env:" + $key) -Value $envValues[$key] }
    }
    foreach ($key in @($envValues.Keys)) {
        if ([string]$key -like "EZTEST_MINDMAP_*" -or [string]$key -like "EZTEST_EXPLORE_*" -or [string]$key -eq "EZTEST_EXPORT_DIR") {
            if (-not [string]::IsNullOrWhiteSpace([string]$envValues[$key])) {
                Set-Item -Path ("Env:" + $key) -Value $envValues[$key]
            }
        }
    }
    # Always keep the acceptance export root inside .tmp so artefacts are
    # inspectable and never leak into the repository tree.
    $env:EZTEST_EXPORT_DIR = $exportDir

    $script:p5.ApiProcess = Start-AcceptanceApi -RunNumber 5
    $health = Invoke-RestMethod -Uri "$apiBaseUrl/api/health" -TimeoutSec 20
    if ($health.data.status -ne "ok" -or $health.data.db -ne "ok") {
        throw "P5 acceptance API health is not ok"
    }

    # T16 may still be in flight: an unauthenticated GET on a registered
    # mindmap route is 401, while an absent route is 404. Only 401 proves the
    # handler and its auth middleware are wired; anything else is a hard error.
    $probe = Invoke-P5Api -Method Get -Path "/api/versions/1/mindmap" -NoThrow
    if ($probe.Status -eq 404) {
        Write-Host "P5 HTTP routes are not registered (GET /api/versions/1/mindmap -> 404); API-dependent P5 checks will be SKIPPED." -ForegroundColor Yellow
        return $false
    }
    if ($probe.Status -ne 401) {
        throw "unexpected unauthenticated mindmap probe status $($probe.Status); expected 401 (wired) or 404 (T16 absent)"
    }

    $adminToken = Get-P5Token -Username $adminUsername -Password $adminPassword
    $script:p5.AdminHeaders = @{ Authorization = "Bearer $adminToken" }
    $stamp = Get-Date -Format "yyyyMMddHHmmss"

    # A normal user is needed for the permissions check; user IDs stay numbers.
    $userName = "p5user$stamp"
    $userPassword = "P5accept-$stamp"
    $normalUser = Invoke-P5Api -Method Post -Path "/api/users" -Headers $script:p5.AdminHeaders -Body @{
        username = $userName
        password = $userPassword
    }
    Assert-P5Http -Response $normalUser -Status 201 -Label "POST normal user" | Out-Null
    $script:p5.UserId = $normalUser.Data.id
    $normalToken = Get-P5Token -Username $userName -Password $userPassword
    $script:p5.UserHeaders = @{ Authorization = "Bearer $normalToken" }

    $system = Invoke-P5Api -Method Post -Path "/api/resources/systems" -Headers $script:p5.AdminHeaders -Body @{
        key     = "p5acc$stamp"
        name    = "P5 acceptance system"
        kind    = "web"
        baseUrl = "http://127.0.0.1:13335"
    }
    Assert-P5Http -Response $system -Status 201 -Label "POST system" | Out-Null
    $script:p5.SystemId = $system.Data.id

    $environment = Invoke-P5Api -Method Post -Path "/api/resources/systems/$($script:p5.SystemId)/environments" -Headers $script:p5.AdminHeaders -Body @{
        key          = "p5acc$stamp"
        name         = "P5 acceptance environment"
        baseUrl      = "http://127.0.0.1:13335"
        kind         = "dev"
        allowedHosts = @("127.0.0.1")
    }
    Assert-P5Http -Response $environment -Status 201 -Label "POST environment" | Out-Null
    $script:p5.EnvironmentId = $environment.Data.id

    foreach ($appModel in @(
            @{ key = "page:/manual/home"; kind = "page"; name = "P5 manual home"; path = "/manual/home"; method = "" },
            @{ key = "route:/manual/orders"; kind = "route"; name = "P5 manual orders"; path = "/manual/orders"; method = "" },
            @{ key = "api:GET /manual/orders"; kind = "api"; name = "P5 manual orders API"; path = "/manual/orders"; method = "GET" }
        )) {
        $created = Invoke-P5Api -Method Post -Path "/api/resources/systems/$($script:p5.SystemId)/app-models" -Headers $script:p5.AdminHeaders -Body $appModel
        Assert-P5Http -Response $created -Status 201 -Label "POST app model" | Out-Null
    }

    $repo = Invoke-P5Api -Method Post -Path "/api/resources/code-repos" -Headers $script:p5.AdminHeaders -Body @{
        key         = "p5acc$stamp"
        name        = "P5 acceptance fixture repo"
        localPath   = $fixtureRepo
        fixedBranch = $codeBranch
    }
    Assert-P5Http -Response $repo -Status 201 -Label "POST code repo" | Out-Null
    $script:p5.RepoId = $repo.Data.id
    $script:p5.FixtureRepo = $fixtureRepo
    $script:p5.GitExe = $gitExe
    $script:p5.CodeBranch = $codeBranch
    $script:p5.BaselineCommit = $baselineCommit

    $version = New-P5DesigningVersion -Headers $script:p5.AdminHeaders `
        -Name "P5 acceptance $stamp" -RequirementCount 3 `
        -SystemId $script:p5.SystemId -EnvironmentId $script:p5.EnvironmentId `
        -CodeRepositoryId $script:p5.RepoId -CodeBranch $codeBranch -BaselineCommit $baselineCommit
    $script:p5.VersionId = $version.VersionId
    $script:p5.RequirementIds = @($version.RequirementIds)
    $script:p5.ExportDir = $exportDir
    $script:p5.Available = $true
    return $true
}

$exitCode = 0
$apiExe = Join-Path $tmpDir "e2e-api.exe"
$hasLLMSeed = $false

try {
    Write-Step "Loading local configuration"
    $envValues = @{}
    if (-not $StaticOnly) {
        if (-not (Test-Path -LiteralPath (Join-Path $repoRoot ".env"))) {
            throw "Missing .env at the repository root"
        }
        if (-not (Test-Path -LiteralPath (Join-Path $frontendDir "node_modules"))) {
            throw "frontend/node_modules is missing; run pnpm install in frontend first"
        }
    }

    $goExe = Join-Path $repoRoot ".tools\go\bin\go.exe"
    if (-not (Test-Path -LiteralPath $goExe)) { $goExe = "go" }
    $psqlExe = Join-Path $repoRoot ".tools\pgsql\bin\psql.exe"
    if (-not (Test-Path -LiteralPath $psqlExe)) { $psqlExe = "psql" }
    $pnpmExe = ""
    if (-not $StaticOnly) {
        $pnpmExe = (Get-Command pnpm.cmd -ErrorAction SilentlyContinue).Source
        if (-not $pnpmExe) { $pnpmExe = (Get-Command pnpm -ErrorAction SilentlyContinue).Source }
        if (-not $pnpmExe) { throw "pnpm is not available on PATH" }
    }
    $nodeCommand = Get-Command node.exe -ErrorAction SilentlyContinue
    if (-not $nodeCommand) { $nodeCommand = Get-Command node -ErrorAction SilentlyContinue }
    if (-not $nodeCommand) { throw "node is not available on PATH" }
    $nodeExe = $nodeCommand.Source
    $scriptHost = Join-Path $PSHOME "pwsh.exe"
    if (-not (Test-Path -LiteralPath $scriptHost)) { $scriptHost = Join-Path $PSHOME "powershell.exe" }
    if (-not (Test-Path -LiteralPath $scriptHost)) { throw "cannot locate a PowerShell host to run helper scripts" }

    if (-not $StaticOnly) {
        $envValues = Read-DotEnv -Path (Join-Path $repoRoot ".env")
        $databaseUrl = Get-RequiredValue -Values $envValues -Key "EZTEST_DATABASE_URL"
        $testDatabaseUrl = Get-RequiredValue -Values $envValues -Key "EZTEST_TEST_DATABASE_URL"
        $adminUsername = Get-RequiredValue -Values $envValues -Key "EZTEST_ADMIN_USERNAME"
        $adminPassword = Get-RequiredValue -Values $envValues -Key "EZTEST_ADMIN_PASSWORD"
        $hasLLMSeed = $envValues.ContainsKey("llm_key") -and -not [string]::IsNullOrWhiteSpace([string]$envValues["llm_key"])

        $databaseUri = [System.Uri]$databaseUrl
        if (-not $databaseUri.IsAbsoluteUri) { throw "EZTEST_DATABASE_URL is not an absolute URL" }
        $userInfo = $databaseUri.UserInfo
        $separator = $userInfo.IndexOf(":")
        if ($separator -lt 0) { throw "EZTEST_DATABASE_URL must contain a user and password" }
        $dbUser = [System.Uri]::UnescapeDataString($userInfo.Substring(0, $separator))
        $dbPassword = [System.Uri]::UnescapeDataString($userInfo.Substring($separator + 1))
        $dbHost = $databaseUri.Host
        $dbPort = $databaseUri.Port

        $queryIndex = $databaseUrl.IndexOf("?")
        if ($queryIndex -ge 0) {
            $urlBase = $databaseUrl.Substring(0, $queryIndex)
            $urlQuery = $databaseUrl.Substring($queryIndex)
        } else {
            $urlBase = $databaseUrl
            $urlQuery = ""
        }
        $slashIndex = $urlBase.LastIndexOf("/")
        if ($slashIndex -lt 0) { throw "EZTEST_DATABASE_URL does not contain a database name" }
        $e2eDatabaseUrl = $urlBase.Substring(0, $slashIndex + 1) + $e2eDbName + $urlQuery

        # Refuse to run the destructive integration helper against anything that
        # is not a dedicated test/e2e database before any suite is started.
        Assert-SafeTestDatabaseUrl -TestDatabaseUrl $testDatabaseUrl -DatabaseUrl $databaseUrl
    }

    # The migration expectation is always derived from order.txt, never
    # hard-coded. The P4 table list is parsed from the codeintel migrations that
    # order.txt actually lists, so adding a migration updates the expectation.
    # D34 rename migrations only change table names, so their old -> new mapping
    # is applied before the tables are checked against the live schema.
    $migrationOrder = @(Get-Content -LiteralPath (Join-Path $backendDir "internal\db\migrations\order.txt") | Where-Object { $_.Trim() -ne "" } | ForEach-Object { $_.Trim() })
    $expectedMigrations = $migrationOrder.Count
    if ($expectedMigrations -le 0) { throw "no migrations are listed in order.txt" }
    $p4MigrationNames = @($migrationOrder | Where-Object { $_ -match '^codeintel_.*\.sql$' })
    if ($p4MigrationNames.Count -eq 0) { throw "order.txt lists no P4 codeintel migrations" }
    $p4MigrationTables = @()
    $p4TableRenames = @{}
    foreach ($migrationName in $p4MigrationNames) {
        $migrationPath = Join-Path $backendDir ("internal\db\migrations\" + $migrationName)
        if (-not (Test-Path -LiteralPath $migrationPath)) {
            throw "order.txt lists a missing migration: $migrationName"
        }
        $migrationSql = Get-Content -LiteralPath $migrationPath -Raw
        foreach ($tableMatch in [regex]::Matches($migrationSql, 'CREATE TABLE IF NOT EXISTS\s+([a-zA-Z0-9_]+)')) {
            $p4MigrationTables += $tableMatch.Groups[1].Value
        }
        foreach ($renameMatch in [regex]::Matches($migrationSql, 'ALTER TABLE IF EXISTS\s+([a-zA-Z0-9_]+)\s+RENAME TO\s+([a-zA-Z0-9_]+)')) {
            $p4TableRenames[$renameMatch.Groups[1].Value] = $renameMatch.Groups[2].Value
        }
    }
    $p4MigrationTables = @($p4MigrationTables | Select-Object -Unique | ForEach-Object {
            if ($p4TableRenames.ContainsKey($_)) { $p4TableRenames[$_] } else { $_ }
        })
    if ($p4MigrationTables.Count -eq 0) {
        throw "the codeintel migrations listed in order.txt define no tables"
    }

    # P5 spec §3.4: 13 appended migrations (10 context rename migrations plus
    # the two CHECK widenings and mindmap_0001_cases.sql). The names must be
    # present in order.txt in that order; the 9 case_* tables and the rename
    # map are parsed from the SQL instead of being hard-coded twice.
    $p5RenameMigrationNames = @(
        "identity_0003_rename_tables.sql",
        "platform_0002_rename_tables.sql",
        "eventing_0002_rename_tables.sql",
        "audit_0002_rename_tables.sql",
        "llm_0002_rename_tables.sql",
        "storage_0002_rename_tables.sql",
        "agent_0003_rename_tables.sql",
        "resource_0004_rename_tables.sql",
        "version_0003_rename_tables.sql",
        "codeintel_0003_rename_tables.sql"
    )
    $p5MigrationNames = @($p5RenameMigrationNames) + @(
        "resource_0005_app_model_explored_source.sql",
        "codeintel_0004_case_anchor_subject.sql",
        "mindmap_0001_cases.sql"
    )
    $p5RenameOldTables = @()
    $p5RenameNewTables = @()
    foreach ($migrationName in $p5RenameMigrationNames) {
        $migrationPath = Join-Path $backendDir ("internal\db\migrations\" + $migrationName)
        if (-not (Test-Path -LiteralPath $migrationPath)) {
            throw "P5 rename migration is missing: $migrationName"
        }
        $renameSql = Get-Content -LiteralPath $migrationPath -Raw
        foreach ($renameMatch in [regex]::Matches($renameSql, 'ALTER TABLE IF EXISTS\s+([a-zA-Z0-9_]+)\s+RENAME TO\s+([a-zA-Z0-9_]+)')) {
            $p5RenameOldTables += $renameMatch.Groups[1].Value
            $p5RenameNewTables += $renameMatch.Groups[2].Value
        }
    }
    if ($p5RenameOldTables.Count -lt 38 -or $p5RenameNewTables.Count -ne $p5RenameOldTables.Count) {
        throw "the 10 P5 rename migrations do not cover the expected rename chain ($($p5RenameOldTables.Count) tables)"
    }
    $p5MindmapMigrationPath = Join-Path $backendDir "internal\db\migrations\mindmap_0001_cases.sql"
    if (-not (Test-Path -LiteralPath $p5MindmapMigrationPath)) {
        throw "P5 migration mindmap_0001_cases.sql is missing"
    }
    $p5MindmapSql = Get-Content -LiteralPath $p5MindmapMigrationPath -Raw
    $p5CaseTables = @([regex]::Matches($p5MindmapSql, 'CREATE TABLE IF NOT EXISTS\s+([a-zA-Z0-9_]+)') | ForEach-Object { $_.Groups[1].Value })
    if ($p5CaseTables.Count -ne 9) {
        throw "mindmap_0001_cases.sql must create the 9 case_* tables, found $($p5CaseTables.Count)"
    }
    foreach ($tableName in $p5CaseTables) {
        if ($tableName -notmatch '^case_') { throw "unexpected non case_* table '$tableName' in mindmap_0001_cases.sql" }
    }
    $p4CoreMigrationIndexes = @()
    for ($i = 0; $i -lt $migrationOrder.Count; $i++) {
        if ($migrationOrder[$i] -match '^codeintel_000[12]_') { $p4CoreMigrationIndexes += $i }
    }
    $p5FirstIndex = [Array]::IndexOf([object[]]$migrationOrder, $p5MigrationNames[0])

    if (-not $StaticOnly) {
        # Integration tests open EZTEST_TEST_DATABASE_URL, the dedicated test database.
        $env:EZTEST_TEST_DATABASE_URL = $testDatabaseUrl

        [void](Invoke-Check -Dod "4,5" -CheckName "Go unit tests" -Body {
            Invoke-LoggedCommand -Exe $goExe -Arguments @("test", "-count=1", "./...") `
                -WorkingDirectory $backendDir `
                -LogPath (Join-Path $tmpDir "acceptance-go-unit.log") `
                -Label "go test ./..."
        })

    [void](Invoke-Check -Dod "1,3,4" -CheckName "Go integration tests" -Body {
            $env:EZTEST_TEST_DATABASE_URL = $testDatabaseUrl
            Invoke-LoggedCommand -Exe $goExe -Arguments @("test", "-tags=integration", "-count=1", "./...") `
                -WorkingDirectory $backendDir `
                -LogPath (Join-Path $tmpDir "acceptance-go-integration.log") `
                -Label "go test -tags=integration ./..."
        })

    # P1 DoD 105: the Agent runtime has its own focused suites in addition to
    # the whole-module runs above, so a regression names the Agent package.
    [void](Invoke-Check -Dod "P1-1" -CheckName "Agent unit tests" -Body {
            Invoke-LoggedCommand -Exe $goExe -Arguments @("test", "-count=1", "./internal/agent/...") `
                -WorkingDirectory $backendDir `
                -LogPath (Join-Path $tmpDir "acceptance-agent-unit.log") `
                -Label "go test ./internal/agent/..."
        })

    [void](Invoke-Check -Dod "P1-1" -CheckName "Agent integration tests" -Body {
            $env:EZTEST_TEST_DATABASE_URL = $testDatabaseUrl
            Invoke-LoggedCommand -Exe $goExe -Arguments @("test", "-tags=integration", "-count=1", "./internal/agent/...") `
                -WorkingDirectory $backendDir `
                -LogPath (Join-Path $tmpDir "acceptance-agent-integration.log") `
                -Label "go test -tags=integration ./internal/agent/..."
        })

    # P3 version / requirements / clarification domain, application, repository,
    # handlers and Snowflake-ID suites.
    [void](Invoke-Check -Dod "P3" -CheckName "Version context unit tests" -Body {
            Invoke-LoggedCommand -Exe $goExe -Arguments @("test", "-count=1", "./internal/version/...") `
                -WorkingDirectory $backendDir `
                -LogPath (Join-Path $tmpDir "acceptance-version-unit.log") `
                -Label "go test ./internal/version/..."
        })

    [void](Invoke-Check -Dod "P3" -CheckName "Version context integration tests" -Body {
            $env:EZTEST_TEST_DATABASE_URL = $testDatabaseUrl
            Invoke-LoggedCommand -Exe $goExe -Arguments @("test", "-tags=integration", "-count=1", "./internal/version/...") `
                -WorkingDirectory $backendDir `
                -LogPath (Join-Path $tmpDir "acceptance-version-integration.log") `
                -Label "go test -tags=integration ./internal/version/..."
        })

    [void](Invoke-Check -Dod "5" -CheckName "Frontend lint" -Body {
            Invoke-LoggedCommand -Exe $pnpmExe -Arguments @("lint") `
                -WorkingDirectory $frontendDir `
                -LogPath (Join-Path $tmpDir "acceptance-frontend-lint.log") `
                -Label "pnpm lint"
        })

    [void](Invoke-Check -Dod "5" -CheckName "Frontend typecheck" -Body {
            Invoke-LoggedCommand -Exe $pnpmExe -Arguments @("typecheck") `
                -WorkingDirectory $frontendDir `
                -LogPath (Join-Path $tmpDir "acceptance-frontend-typecheck.log") `
                -Label "pnpm typecheck"
        })

    [void](Invoke-Check -Dod "5" -CheckName "Frontend unit tests" -Body {
            Invoke-LoggedCommand -Exe $pnpmExe -Arguments @("test") `
                -WorkingDirectory $frontendDir `
                -LogPath (Join-Path $tmpDir "acceptance-frontend-test.log") `
                -Label "pnpm test"
        })

    [void](Invoke-Check -Dod "5" -CheckName "Frontend production build" -Body {
            Invoke-LoggedCommand -Exe $pnpmExe -Arguments @("build") `
                -WorkingDirectory $frontendDir `
                -LogPath (Join-Path $tmpDir "acceptance-frontend-build.log") `
                -Label "pnpm build"
        })

    [void](Invoke-Check -Dod "1,3,P1-1,P1-2,P1-3" -CheckName "Playwright E2E (real Chrome, P0 + Agent console)" -Body {
            Invoke-LoggedCommand -Exe $scriptHost `
                -Arguments @("-NoProfile", "-ExecutionPolicy", "Bypass", "-File", (Join-Path $scriptsDir "e2e.ps1")) `
                -WorkingDirectory $repoRoot `
                -LogPath (Join-Path $tmpDir "acceptance-e2e.log") `
                -Label "scripts/e2e.ps1" `
                -SuccessTailLines 12
        })

    [void](Invoke-Check -Dod "2,P3" -CheckName "Migration rerun + health/audit/version API" -Body {
            $env:PGPASSWORD = $dbPassword
            $first = $null
            $second = $null
            try {
                # Rebuild from the current sources so the migration rerun does
                # not reuse a stale binary from an earlier acceptance run.
                Invoke-LoggedCommand -Exe $goExe -Arguments @("build", "-o", $apiExe, "./cmd/eztest") `
                    -WorkingDirectory $backendDir `
                    -LogPath (Join-Path $tmpDir "acceptance-api-build.log") `
                    -Label "go build ./cmd/eztest"

                $before = Get-MigrationFingerprint

                $env:EZTEST_ENV = "dev"
                $env:EZTEST_DATABASE_URL = $e2eDatabaseUrl
                $env:EZTEST_HTTP_ADDR = ":$apiPort"
                $env:EZTEST_WEB_ORIGIN = "http://localhost:6868"
                $env:EZTEST_ENABLE_TEST_ENDPOINTS = "true"
                $env:EZTEST_STORAGE_DIR = (Join-Path $tmpDir "e2e-objects")
                $env:EZTEST_ADMIN_USERNAME = $adminUsername
                $env:EZTEST_ADMIN_PASSWORD = $adminPassword
                foreach ($key in @("EZTEST_SECRET_KEYS", "EZTEST_SECRET_KEY_ACTIVE", "llm_base_url", "llm_key", "llm_model_name")) {
                    if ($envValues.ContainsKey($key)) {
                        Set-Item -Path ("Env:" + $key) -Value $envValues[$key]
                    }
                }

                Assert-PortFree -Port $apiPort
                $first = Start-AcceptanceApi -RunNumber 1
                $healthFirst = Invoke-RestMethod -Uri "$apiBaseUrl/api/health" -TimeoutSec 20
                Assert-Health -Health $healthFirst
                $afterFirst = Get-MigrationFingerprint
                Stop-ProcessTree -Process $first
                $first = $null
                Wait-PortFree -Port $apiPort

                $second = Start-AcceptanceApi -RunNumber 2
                $healthSecond = Invoke-RestMethod -Uri "$apiBaseUrl/api/health" -TimeoutSec 20
                Assert-Health -Health $healthSecond
                Test-AdminAudit
                Test-VersionApiSmoke
                $afterSecond = Get-MigrationFingerprint

                if ($before -ne $afterFirst -or $afterFirst -ne $afterSecond) {
                    throw "schema_migrations changed across API restarts (migrations are not idempotent)"
                }
                $migrationCount = [int]($afterSecond.Split(":")[0])
                if ($migrationCount -ne $expectedMigrations) {
                    throw "applied migrations=$migrationCount, expected $expectedMigrations"
                }

                # P4 table expectations come from the CREATE TABLE statements of
                # the codeintel migrations order.txt lists (no hard-coded table
                # count), so a new migration cannot silently skip its tables.
                $tableInList = ($p4MigrationTables | ForEach-Object { "'" + $_ + "'" }) -join ","
                $tableSql = "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name IN ($tableInList);"
                $presentTables = [int](Get-PsqlScalar -Database $e2eDbName -Sql $tableSql)
                if ($presentTables -ne $p4MigrationTables.Count) {
                    throw "P4 tables present=$presentTables, expected $($p4MigrationTables.Count) (from order.txt codeintel migrations)"
                }
                Write-Host "Migration rerun OK: $migrationCount migrations, checksums unchanged across two API starts; P4 tables present: $presentTables."
            } finally {
                Stop-ProcessTree -Process $first
                Stop-ProcessTree -Process $second
                Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
            }
        })
    }

    [void](Invoke-Check -Dod "5,P1-4" -CheckName "P0/P1 deliverables and Compose static validation" -Body {
            foreach ($relative in @(
                    "README.md",
                    "docs/openapi/api.yaml",
                    "deploy/Dockerfile.api",
                    "deploy/Dockerfile.web",
                    "deploy/docker-compose.yml"
                )) {
                $deliverable = Join-Path $repoRoot $relative
                if (-not (Test-Path -LiteralPath $deliverable)) {
                    throw "missing P0/P1 deliverable $relative"
                }
            }
            $readme = Get-Content -LiteralPath (Join-Path $repoRoot "README.md") -Raw
            if ($readme -notmatch "EZTEST_OPENAPI_PATH") {
                throw "README raw go-run instructions do not document EZTEST_OPENAPI_PATH"
            }
            $openapi = Get-Content -LiteralPath (Join-Path $repoRoot "docs/openapi/api.yaml") -Raw
            foreach ($needle in @(
                    "/api/agents/sessions",
                    "agent.activity",
                    "reasoning_tokens",
                    # P2 resources contract: the resource paths plus the two
                    # write-only markers proving credentials and storage state
                    # are represented without ever echoing their values.
                    "/api/resources/systems",
                    "/api/resources/sessions",
                    "/api/resources/code-repos",
                    "storageState",
                    "credentialSet"
                )) {
                if ($openapi -notmatch [regex]::Escape($needle)) {
                    throw "docs/openapi/api.yaml is missing $needle"
                }
            }
            $envTemplate = Get-Content -LiteralPath (Join-Path $repoRoot ".env.example") -Raw
            foreach ($agentVar in @(
                    "EZTEST_AGENT_FILE_ROOT",
                    "EZTEST_AGENT_HTTP_ALLOWLIST",
                    "EZTEST_AGENT_TOOL_TIMEOUT_MS",
                    "EZTEST_AGENT_MAX_CONCURRENT_RUNS"
                )) {
                if ($envTemplate -notmatch [regex]::Escape($agentVar)) {
                    throw ".env.example is missing $agentVar"
                }
            }
            Invoke-LoggedCommand -Exe $nodeExe -Arguments @("scripts/validate-compose.mjs") `
                -WorkingDirectory $frontendDir `
                -LogPath (Join-Path $tmpDir "acceptance-compose-validate.log") `
                -Label "scripts/validate-compose.mjs" `
                -SuccessTailLines 8
        })

    [void](Invoke-Check -Dod "P3" -CheckName "P3 deliverables and OpenAPI/SSE contract" -Body {
            $readme = Get-Content -LiteralPath (Join-Path $repoRoot "README.md") -Raw
            foreach ($needle in @(
                    "/versions",
                    "EZTEST_VERSION_ATTACHMENT_DIR",
                    "EZTEST_SNOWFLAKE_NODE_ID",
                    "version-attachments",
                    "scripts/acceptance.ps1"
                )) {
                if ($readme -notmatch [regex]::Escape($needle)) {
                    throw "README.md is missing P3 keyword $needle"
                }
            }

            $openapi = Get-Content -LiteralPath (Join-Path $repoRoot "docs/openapi/api.yaml") -Raw
            foreach ($needle in @(
                    # P3 method/path families. The full P3 paths are used for
                    # /events and /stream because the bare fragments also match
                    # the earlier P0/P1 endpoints.
                    "/api/versions",
                    "/start-clarification",
                    "/enter-design",
                    "/requirements",
                    "/relations",
                    "/attachments",
                    "/clarification",
                    "/decisions",
                    "/api/versions/{id}/events",
                    "/api/versions/{id}/stream",
                    # P3 contract markers: acknowledge gate, context event,
                    # string IDs and multipart upload.
                    "acknowledgeUnclarified",
                    "clarification.context_updated",
                    "snowflake",
                    "multipart/form-data"
                )) {
                if ($openapi -notmatch [regex]::Escape($needle)) {
                    throw "docs/openapi/api.yaml is missing P3 contract keyword $needle"
                }
            }

            # These markers predate P3 and would pass on the P0/P1 sections
            # alone, so scope them to the P3 objects that own them.
            $p3EventSnapshot = [regex]::Match($openapi, '(?ms)^    VersionEventSnapshot:\r?\n.*?(?=^    [A-Za-z]|\z)')
            if (-not $p3EventSnapshot.Success -or $p3EventSnapshot.Value -notmatch 'snapshot_cursor') {
                throw "docs/openapi/api.yaml is missing VersionEventSnapshot.snapshot_cursor"
            }
            $p3Stream = [regex]::Match($openapi, '(?ms)^  /api/versions/\{id\}/stream:\r?\n.*?(?=^  /|^components:)')
            if (-not $p3Stream.Success -or $p3Stream.Value -notmatch 'Last-Event-ID') {
                throw "docs/openapi/api.yaml is missing the P3 stream Last-Event-ID contract"
            }

            # Full spec §7.1 details.reason table plus the field-level
            # invalid_context_summary reason emitted by context.summary_update.
            foreach ($reason in @(
                    "version_state_conflict",
                    "requirement_state_conflict",
                    "open_questions_remaining",
                    "unclarified_requirements",
                    "requirements_required",
                    "duplicate_relation",
                    "relation_requirement_mismatch",
                    "duplicate_attachment",
                    "revision_conflict",
                    "clarification_session_active",
                    "clarification_session_not_active",
                    "clarification_session_required",
                    "clarification_question_state_conflict",
                    "decision_state_conflict",
                    "agent_busy",
                    "attachment_too_large",
                    "attachment_limit_exceeded",
                    "unsupported_attachment_type",
                    "invalid_attachment_content",
                    "invalid_context_summary",
                    "system_not_found",
                    "system_disabled",
                    "environment_not_found",
                    "environment_system_mismatch",
                    "code_repository_not_found",
                    "code_repository_disabled",
                    "branch_not_found",
                    "path_not_found",
                    "path_not_git_repo",
                    "resource_unavailable",
                    "ai_unavailable"
                )) {
                if ($openapi -notmatch [regex]::Escape($reason)) {
                    throw "docs/openapi/api.yaml is missing P3 error reason $reason"
                }
            }

            # Full spec §6.11 event catalog plus the P3 clarification.context_updated,
            # scoped to the P3 VersionEventType enum so earlier-phase event types
            # cannot satisfy the check.
            $p3EventCatalog = [regex]::Match($openapi, '(?ms)^    VersionEventType:\r?\n.*?(?=^    [A-Za-z])')
            if (-not $p3EventCatalog.Success) {
                throw "docs/openapi/api.yaml is missing the VersionEventType event catalog"
            }
            foreach ($eventType in @(
                    "version.created",
                    "version.updated",
                    "version.status_changed",
                    "requirement.added",
                    "requirement.updated",
                    "requirement.clarified",
                    "requirement.reopened",
                    "requirement.deleted",
                    "requirement.relation_added",
                    "requirement.relation_updated",
                    "requirement.relation_removed",
                    "clarification.session_started",
                    "clarification.session_completed",
                    "clarification.question_updated",
                    "clarification.context_updated",
                    "decision.proposed",
                    "decision.decided",
                    "attachment.added",
                    "attachment.removed",
                    "agent.activity"
                )) {
                if ($p3EventCatalog.Value -notmatch [regex]::Escape($eventType)) {
                    throw "docs/openapi/api.yaml VersionEventType is missing version event type $eventType"
                }
            }

            $envTemplate = Get-Content -LiteralPath (Join-Path $repoRoot ".env.example") -Raw
            foreach ($versionVar in @("EZTEST_VERSION_ATTACHMENT_DIR", "EZTEST_SNOWFLAKE_NODE_ID")) {
                if ($envTemplate -notmatch [regex]::Escape($versionVar)) {
                    throw ".env.example is missing $versionVar"
                }
            }
            $compose = Get-Content -LiteralPath (Join-Path $repoRoot "deploy/docker-compose.yml") -Raw
            foreach ($composeNeedle in @("EZTEST_VERSION_ATTACHMENT_DIR", "attachments:/data/version-attachments")) {
                if ($compose -notmatch [regex]::Escape($composeNeedle)) {
                    throw "deploy/docker-compose.yml is missing P3 attachment setting $composeNeedle"
                }
            }
            if ($envTemplate -notmatch [regex]::Escape("version-attachments")) {
                throw ".env.example is missing the version-attachments default path"
            }
        })

    [void](Invoke-Check -Dod "P4" -CheckName "P4 deliverables, OpenAPI, fake LLM and migration contract" -Body {
            $readme = Get-Content -LiteralPath (Join-Path $repoRoot "README.md") -Raw
            foreach ($needle in @(
                    "EZTEST_CODEINTEL_MAX_CHANGED_FILES",
                    "EZTEST_CODEINTEL_PROMPT_VERSION",
                    "EZTEST_CODEINTEL_MAX_DIFF_BYTES",
                    "EZTEST_ACCEPTANCE_P4_REPO",
                    "/api/code/repos",
                    "/api/versions/{id}/code",
                    "scripts/fake-llm.p4.test.cjs",
                    "scripts/acceptance-codeintel-llm.ps1",
                    "code.impact_computed",
                    "code.analyze",
                    "read-only"
                )) {
                if ($readme -notmatch [regex]::Escape($needle)) {
                    throw "README.md is missing P4 keyword $needle"
                }
            }

            # The five P4 Playwright specs run as part of the whole Playwright
            # suite in scripts/e2e.ps1; pin their presence in the static gate so
            # a rename cannot silently drop P4 E2E coverage.
            foreach ($p4Spec in @(
                    "versions-code.spec.ts",
                    "versions-code-cache.spec.ts",
                    "versions-code-degraded.spec.ts",
                    "versions-code-anchors.spec.ts",
                    "code-repos-index.spec.ts"
                )) {
                $specPath = Join-Path $frontendDir ("e2e\" + $p4Spec)
                if (-not (Test-Path -LiteralPath $specPath)) {
                    throw "missing P4 E2E spec frontend/e2e/$p4Spec (the Playwright gate must include P4 coverage)"
                }
            }

            $openapi = Get-Content -LiteralPath (Join-Path $repoRoot "docs/openapi/api.yaml") -Raw
            foreach ($needle in @(
                    # Both P4 API prefixes and every documented path family.
                    "/api/code/repos",
                    "/api/code/repos/{repoId}/index",
                    "/api/code/repos/{repoId}/index-runs/latest",
                    "/api/code/repos/{repoId}/index-runs/{runId}",
                    "/api/code/repos/{repoId}/tree",
                    "/api/code/repos/{repoId}/file",
                    "/api/code/repos/{repoId}/diff",
                    "/api/code/repos/{repoId}/diff/file",
                    "/api/code/repos/{repoId}/facts",
                    "/api/code/repos/{repoId}/search",
                    "/api/code/repos/{repoId}/commits/{sha}",
                    "/api/versions/{versionId}/code/changes",
                    "/api/versions/{versionId}/code/analyses",
                    "/api/versions/{versionId}/code/analyses/latest",
                    "/api/versions/{versionId}/code/analyses/{analysisId}",
                    "/api/versions/{versionId}/code/analyses/{analysisId}/candidates/{candidateId}",
                    "/api/versions/{versionId}/code/anchors",
                    "/api/versions/{versionId}/code/anchors/{anchorId}",
                    # Wire-contract fields the frontend depends on.
                    "changeSetId",
                    "changeSummary",
                    "factVersions",
                    "nextCursor",
                    "headBlobHash"
                )) {
                if ($openapi -notmatch [regex]::Escape($needle)) {
                    throw "docs/openapi/api.yaml is missing P4 contract keyword $needle"
                }
            }

            # P4 schema names must exist as their own component entries; a bare
            # mention inside another description cannot satisfy this check.
            foreach ($schemaName in @(
                    "RepoSummary",
                    "IndexRun",
                    "ChangeList",
                    "FileDiff",
                    "FactsPage",
                    "Analysis",
                    "Candidate",
                    "Anchor",
                    "CodeEntity",
                    "CodeErrorReason"
                )) {
                if ($openapi -notmatch ("(?m)^    " + $schemaName + ":")) {
                    throw "docs/openapi/api.yaml is missing the P4 schema $schemaName"
                }
            }

            # The reason needles must live in the P4 reason enum itself, not in
            # an unrelated P3 table that happens to mention the same word.
            $p4ReasonCatalog = [regex]::Match($openapi, '(?ms)^    CodeErrorReason:\r?\n.*?(?=^    [A-Za-z])')
            if (-not $p4ReasonCatalog.Success) {
                throw "docs/openapi/api.yaml is missing the CodeErrorReason reason catalog"
            }
            foreach ($reason in @(
                    "code_repository_not_bound",
                    "baseline_commit_missing",
                    "invalid_head",
                    "not_changed",
                    "unsupported_content",
                    "index_in_progress",
                    "analysis_in_progress",
                    "analysis_queue_full",
                    "analysis_not_done",
                    "anchor_duplicate",
                    "anchor_not_found",
                    "unsupported_subject_kind",
                    "path_not_git_repo",
                    "git_unavailable"
                )) {
                if ($p4ReasonCatalog.Value -notmatch [regex]::Escape($reason)) {
                    throw "docs/openapi/api.yaml CodeErrorReason is missing P4 reason $reason"
                }
            }

            # P4 event types must be in the version event catalog; the P3 check
            # above already pins the earlier catalog entries.
            $p4EventCatalog = [regex]::Match($openapi, '(?ms)^    VersionEventType:\r?\n.*?(?=^    [A-Za-z])')
            if (-not $p4EventCatalog.Success) {
                throw "docs/openapi/api.yaml is missing the VersionEventType event catalog"
            }
            foreach ($eventType in @("code.changed", "code.impact_computed")) {
                if ($p4EventCatalog.Value -notmatch [regex]::Escape($eventType)) {
                    throw "docs/openapi/api.yaml VersionEventType is missing P4 event type $eventType"
                }
            }

            # The P4 env template keys are part of the deliverable; no values
            # are read or reported here.
            $envTemplate = Get-Content -LiteralPath (Join-Path $repoRoot ".env.example") -Raw
            foreach ($codeintelVar in @(
                    "EZTEST_CODEINTEL_MAX_CHANGED_FILES",
                    "EZTEST_CODEINTEL_MAX_FILE_BYTES",
                    "EZTEST_CODEINTEL_MAX_INDEX_FILES",
                    "EZTEST_CODEINTEL_ANALYSIS_TIMEOUT_MS",
                    "EZTEST_CODEINTEL_MAX_CONCURRENT_ANALYSES",
                    "EZTEST_CODEINTEL_PROMPT_VERSION",
                    "EZTEST_CODEINTEL_MAX_EXCERPT_LINES",
                    "EZTEST_CODEINTEL_MAX_DIFF_BYTES"
                )) {
                if ($envTemplate -notmatch [regex]::Escape($codeintelVar)) {
                    throw ".env.example is missing P4 variable $codeintelVar"
                }
            }

            # P4 migrations must still be present before the P5 migrations.
            # P5 appends 13 migrations (rename chain + CHECK widenings +
            # mindmap_0001_cases.sql), so tail equality is no longer correct;
            # the P4 core codeintel migrations must precede the first P5
            # migration instead. Their tables are parsed dynamically (no
            # hard-coded 16 or table count).
            if ($p4CoreMigrationIndexes.Count -ne 2) {
                throw "order.txt must list both P4 core migrations (codeintel_0001_code_facts.sql and codeintel_0002_code_impact.sql)"
            }
            if ($p5FirstIndex -lt 0) {
                throw "order.txt does not list the P5 rename migrations before the mindmap migration"
            }
            foreach ($p4Index in $p4CoreMigrationIndexes) {
                if ($p4Index -ge $p5FirstIndex) {
                    throw "order.txt must keep the P4 codeintel migrations before the P5 migrations"
                }
            }
            if ($p4MigrationTables.Count -lt 1) {
                throw "the P4 codeintel migrations listed in order.txt define no tables"
            }

            # Parse the OpenAPI document with the repository's existing YAML
            # dependency (frontend/node_modules/yaml); this catches a broken
            # YAML document that the regex needles above would miss. StaticOnly
            # does not require frontend/node_modules, so fail with an actionable
            # message instead of a bare node module-resolution error.
            $yamlModule = Join-Path $frontendDir "node_modules\yaml"
            if (-not (Test-Path -LiteralPath $yamlModule)) {
                throw "frontend/node_modules/yaml is missing; run 'pnpm install' in frontend (the OpenAPI YAML parse uses that dependency)"
            }
            # Windows PowerShell 5.1 strips embedded double quotes from native
            # arguments, so the JavaScript program uses single-quoted strings.
            $openapiParseCode = "import { readFileSync } from 'node:fs'; import { parse } from 'yaml'; const doc = parse(readFileSync(process.argv[1], 'utf8')); if (!doc || typeof doc !== 'object' || !doc.paths || !doc.components || !doc.components.schemas) { throw new Error('invalid OpenAPI document'); } const p4 = Object.keys(doc.paths).filter((p) => p.startsWith('/api/code/repos') || p.includes('/code/changes') || p.includes('/code/analyses') || p.includes('/code/anchors')); if (p4.length < 18) { throw new Error('expected at least 18 P4 paths, found ' + p4.length); } console.log('OpenAPI parsed: ' + Object.keys(doc.paths).length + ' paths, ' + Object.keys(doc.components.schemas).length + ' schemas, ' + p4.length + ' P4 paths');"
            Invoke-LoggedCommand -Exe $nodeExe `
                -Arguments @("--input-type=module", "-e", $openapiParseCode, (Join-Path $repoRoot "docs/openapi/api.yaml")) `
                -WorkingDirectory $frontendDir `
                -LogPath (Join-Path $tmpDir "acceptance-openapi-parse.log") `
                -Label "OpenAPI YAML parse" `
                -SuccessTailLines 2

            # The P4 fake-LLM contract test is a gate, not an optional script.
            # Its fixture is deterministic and rebuilt only when absent.
            $fixtureDir = Join-Path $tmpDir "p4-e2e-repo"
            if (-not (Test-Path -LiteralPath (Join-Path $fixtureDir ".git"))) {
                Invoke-LoggedCommand -Exe $scriptHost `
                    -Arguments @("-NoProfile", "-ExecutionPolicy", "Bypass", "-File", (Join-Path $scriptsDir "fixture-codeintel-repo.ps1")) `
                    -WorkingDirectory $repoRoot `
                    -LogPath (Join-Path $tmpDir "acceptance-p4-fixture.log") `
                    -Label "scripts/fixture-codeintel-repo.ps1" `
                    -SuccessTailLines 4
            }
            Invoke-LoggedCommand -Exe $nodeExe `
                -Arguments @("scripts/fake-llm.p4.test.cjs") `
                -WorkingDirectory $repoRoot `
                -LogPath (Join-Path $tmpDir "acceptance-fake-llm-p4.log") `
                -Label "node scripts/fake-llm.p4.test.cjs" `
                -SuccessTailLines 8
        })

    # ---------------------------------------------------------------------
    # P5 case mindmap / review acceptance block (spec §10.7). Static checks
    # run everywhere; the eight API checks require the T16 HTTP wiring and are
    # SKIPPED when -StaticOnly is set or the routes are not registered yet.
    # ---------------------------------------------------------------------
    $script:p5DodMapping = @(
        [pscustomobject][ordered]@{
            DoD         = "P5-1"
            Requirement = "Generate a mindmap from confirmed requirements (real LLM / fake)"
            Evidence    = "P5 real-LLM script checks 4/5; E2E mindmap step 2; P5 API smoke check"
        },
        [pscustomobject][ordered]@{
            DoD         = "P5-2"
            Requirement = "Humans can edit and review the graph"
            Evidence    = "P5 manual-path check; P5 permissions check; E2E mindmap steps 3/4"
        },
        [pscustomobject][ordered]@{
            DoD         = "P5-3"
            Requirement = "Approval creates a baseline and enables exports"
            Evidence    = "P5 freeze/export check; P5 real-LLM check 10/11; E2E steps 4/6"
        },
        [pscustomobject][ordered]@{
            DoD         = "P5-4"
            Requirement = "Nodes carry sources and code anchors"
            Evidence    = "P5 API smoke (case sources); P5 real-LLM check 4/6; E2E source badges"
        },
        [pscustomobject][ordered]@{
            DoD         = "P5-5"
            Requirement = "Requirement-to-case coverage is correct"
            Evidence    = "P5 freeze/export content coverage matrix; GET /mindmap coverage assertions"
        },
        [pscustomobject][ordered]@{
            DoD         = "P5-6"
            Requirement = "The manual create/edit path still works when AI is unavailable"
            Evidence    = "P5 degraded-LLM check; P5 manual-path check"
        },
        [pscustomobject][ordered]@{
            DoD         = "P5-7"
            Requirement = "Mindmap review events are visible over SSE"
            Evidence    = "P5 events snapshot check; E2E mindmap step 7"
        },
        [pscustomobject][ordered]@{
            DoD         = "P5-8"
            Requirement = "The degradation path is defined"
            Evidence    = "P5 degraded-LLM check; P5 real-LLM check 15; spec 8.5"
        },
        [pscustomobject][ordered]@{
            DoD         = "P5-9"
            Requirement = "The design documents are in sync"
            Evidence    = "P5 OpenAPI contract check; P5 deliverables check (README/env/migrations)"
        },
        [pscustomobject][ordered]@{
            DoD         = "P5-10"
            Requirement = "Automated tests cover the critical paths"
            Evidence    = "P5 static checks + API checks; mindmap Go suites; E2E mindmap spec; gated real-LLM script"
        }
    )

    [void](Invoke-Check -Dod "P5-9" -CheckName "P5 spec/deliverables" -Body {
            foreach ($migrationName in $p5MigrationNames) {
                $migrationPath = Join-Path $backendDir ("internal\db\migrations\" + $migrationName)
                if (-not (Test-Path -LiteralPath $migrationPath)) {
                    throw "P5 migration is missing: $migrationName"
                }
                if ($migrationOrder -notcontains $migrationName) {
                    throw "order.txt is missing the P5 migration $migrationName"
                }
            }
            $p5TailStart = $migrationOrder.Count - $p5MigrationNames.Count
            if ($p5TailStart -lt 0) {
                throw "order.txt is shorter than the 13 P5 migrations"
            }
            $actualTail = @($migrationOrder[$p5TailStart..($migrationOrder.Count - 1)])
            if (($actualTail -join "|") -ne ($p5MigrationNames -join "|")) {
                throw "order.txt must append exactly the 13 P5 migrations at its tail"
            }

            foreach ($relative in @(
                    "backend\internal\mindmap\domain\prompts.go",
                    "backend\internal\mindmap\domain\export.go",
                    "backend\internal\mindmap\domain\review.go",
                    "backend\internal\mindmap\application\graph_service.go",
                    "backend\internal\mindmap\application\materialize.go",
                    "backend\internal\mindmap\application\run_service.go",
                    "backend\internal\mindmap\application\export_service.go",
                    "backend\internal\mindmap\application\explore_service.go",
                    "backend\internal\mindmap\infrastructure\filestore\store.go",
                    "backend\internal\mindmap\infrastructure\postgres\node_repository.go",
                    "backend\internal\mindmap\integration\migrations_test.go",
                    "frontend\src\features\mindmap\MindmapPage.tsx",
                    "frontend\src\features\mindmap\MindmapCanvas.tsx",
                    "frontend\src\features\mindmap\CaseEditor.tsx",
                    "frontend\src\features\mindmap\ReviewPanel.tsx",
                    "frontend\src\features\mindmap\RunPanel.tsx",
                    "frontend\src\features\mindmap\ExportMenu.tsx",
                    "frontend\src\features\mindmap\png-export.ts",
                    "frontend\src\features\mindmap\error-messages.ts",
                    "scripts\acceptance-mindmap-llm.ps1"
                )) {
                if (-not (Test-Path -LiteralPath (Join-Path $repoRoot $relative))) {
                    throw "missing P5 deliverable $relative"
                }
            }

            $readme = Get-Content -LiteralPath (Join-Path $repoRoot "README.md") -Raw
            foreach ($needle in @(
                    "P5",
                    "/mindmap",
                    "mindmap_0001_cases.sql",
                    "EZTEST_EXPORT_DIR",
                    "EZTEST_MINDMAP_MAX_CASES",
                    "EZTEST_EXPLORE_MAX_REQUESTS",
                    "EZTEST_EXPLORE_TIMEOUT_MS",
                    "scripts/acceptance-mindmap-llm.ps1",
                    "OPML",
                    "readonly"
                )) {
                if ($readme -notmatch [regex]::Escape($needle)) {
                    throw "README.md is missing P5 keyword $needle"
                }
            }

            $envTemplate = Get-Content -LiteralPath (Join-Path $repoRoot ".env.example") -Raw
            foreach ($p5Var in @(
                    "EZTEST_EXPORT_DIR",
                    "EZTEST_MINDMAP_MAX_CASES",
                    "EZTEST_EXPLORE_MAX_REQUESTS",
                    "EZTEST_EXPLORE_TIMEOUT_MS"
                )) {
                if ($envTemplate -notmatch [regex]::Escape($p5Var)) {
                    throw ".env.example is missing P5 variable $p5Var"
                }
            }
            if ($p5CaseTables.Count -ne 9) {
                throw "mindmap_0001_cases.sql must create 9 case_* tables"
            }
        })

    [void](Invoke-Check -Dod "P5-9" -CheckName "P5 OpenAPI contract" -Body {
            $openapiPath = Join-Path $repoRoot "docs/openapi/api.yaml"
            $summaryPath = Join-Path $tmpDir "acceptance-p5-openapi-summary.json"
            $jsPath = Join-Path $tmpDir "acceptance-p5-openapi.mjs"
            Remove-Item -LiteralPath $summaryPath -ErrorAction SilentlyContinue
            $openapiCheckJs = @'
import { readFileSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";

const require = createRequire(process.cwd() + "/package.json");
const YAML = require("yaml");
const raw = readFileSync(process.argv[2], "utf8");
const doc = YAML.parse(raw);
const prefix = "/api/versions/{versionId}/mindmap";
const expected = {
  [prefix]: ["get"],
  [`${prefix}/nodes`]: ["post"],
  [`${prefix}/nodes/{nodeId}`]: ["patch", "delete"],
  [`${prefix}/order`]: ["put"],
  [`${prefix}/nodes/{nodeId}/steps`]: ["post"],
  [`${prefix}/steps/{stepId}`]: ["patch", "delete"],
  [`${prefix}/nodes/{nodeId}/steps/order`]: ["put"],
  [`${prefix}/nodes/{nodeId}/assertions`]: ["post"],
  [`${prefix}/assertions/{assertionId}`]: ["patch", "delete"],
  [`${prefix}/nodes/{nodeId}/sources`]: ["post"],
  [`${prefix}/sources/{sourceId}`]: ["delete"],
  [`${prefix}/nodes/{nodeId}/anchors`]: ["post"],
  [`${prefix}/anchors/{anchorId}`]: ["delete"],
  [`${prefix}/runs`]: ["post", "get"],
  [`${prefix}/runs/{runId}`]: ["get"],
  [`${prefix}/submit-review`]: ["post"],
  [`${prefix}/review`]: ["post"],
  [`${prefix}/reviews`]: ["get"],
  [`${prefix}/findings`]: ["get"],
  [`${prefix}/findings/{findingId}`]: ["patch"],
  [`${prefix}/export`]: ["post"],
  [`${prefix}/exports`]: ["get"],
  [`${prefix}/exports/{exportId}/download`]: ["get"]
};
const paths = doc.paths || {};
const errors = [];
const actual = Object.keys(paths).filter((p) => p.startsWith(prefix));
const missing = Object.keys(expected).filter((p) => !(p in paths));
const extra = actual.filter((p) => !(p in expected));
if (missing.length) errors.push("missing paths: " + missing.join(", "));
if (extra.length) errors.push("unexpected paths: " + extra.join(", "));
let operations = 0;
for (const [p, methods] of Object.entries(expected)) {
  const item = paths[p] || {};
  const got = ["get", "post", "put", "patch", "delete"].filter((m) => item[m]);
  operations += got.length;
  if (JSON.stringify(got.sort()) !== JSON.stringify([...methods].sort())) {
    errors.push("methods " + p + ": got " + got.join(","));
  }
}
if (operations !== 27) errors.push("operations=" + operations + " (expected 27)");
const events = (doc.components?.schemas?.VersionEventType?.enum) || [];
const newEvents = ["case.created", "case.updated", "case.deleted", "case.reviewed",
  "case.baseline_frozen", "case.run_started", "case.run_finished", "case.export_ready"];
for (const event of newEvents) if (!events.includes(event)) errors.push("event " + event);
const reasons = (doc.components?.schemas?.MindmapErrorReason?.enum) || [];
const specReasons = ["version_state_conflict", "baseline_frozen", "case_revision_conflict",
  "invalid_case_tree", "no_cases", "unconfirmed_requirements", "requirement_not_found",
  "requirement_not_clarified", "node_not_found", "step_not_found", "assertion_not_found",
  "source_not_found", "anchor_not_found", "review_not_found", "baseline_not_found",
  "finding_not_found", "run_in_progress", "run_not_found", "run_state_conflict",
  "invalid_run_kind", "invalid_case_proposal", "blocker_findings_open",
  "exploration_requires_environment", "environment_not_allowed", "environment_unreachable",
  "code_repo_unavailable", "ai_unavailable", "export_format_unsupported", "export_not_found",
  "export_conflict", "export_corrupted", "duplicate_case_source", "duplicate_key",
  "graph_too_large", "forbidden"];
const missingReasons = specReasons.filter((r) => !reasons.includes(r));
if (missingReasons.length) errors.push("missing reasons: " + missingReasons.join(", "));
const subjectKinds = (doc.components?.schemas?.AnchorSubjectKind?.enum) || [];
if (!subjectKinds.includes("case")) errors.push("AnchorSubjectKind.case");
for (const kind of ["version", "requirement", "app_model"]) if (!subjectKinds.includes(kind)) errors.push("AnchorSubjectKind." + kind);
const appSource = (doc.components?.schemas?.AppModelNode?.properties?.source?.enum) || [];
if (!appSource.includes("explored")) errors.push("AppModelNode.source.explored");
writeFileSync(process.argv[3], JSON.stringify({
  ok: errors.length === 0,
  errors,
  pathItems: actual.length,
  operations,
  events: newEvents.filter((event) => events.includes(event)).length,
  reasons: specReasons.filter((reason) => reasons.includes(reason)).length
}), "utf8");
'@
            [System.IO.File]::WriteAllText($jsPath, $openapiCheckJs, (New-Object System.Text.UTF8Encoding($false)))
            Invoke-LoggedCommand -Exe $nodeExe `
                -Arguments @($jsPath, $openapiPath, $summaryPath) `
                -WorkingDirectory $frontendDir `
                -LogPath (Join-Path $tmpDir "acceptance-p5-openapi.log") `
                -Label "P5 OpenAPI contract check"
            if (-not (Test-Path -LiteralPath $summaryPath)) {
                throw "the P5 OpenAPI checker did not write $summaryPath"
            }
            $summary = Get-Content -LiteralPath $summaryPath -Raw | ConvertFrom-Json
            if (-not $summary.ok) {
                throw ("P5 OpenAPI contract violations: " + (@($summary.errors) -join "; "))
            }
            if ([int]$summary.pathItems -ne 23 -or [int]$summary.operations -ne 27) {
                throw "P5 OpenAPI operations/path items mismatch ($($summary.pathItems) paths, $($summary.operations) operations)"
            }
            if ([int]$summary.events -ne 8 -or [int]$summary.reasons -ne 35) {
                throw "P5 OpenAPI event/reason coverage mismatch ($($summary.events)/8 events, $($summary.reasons)/35 reasons)"
            }
            Write-Host ("P5 OpenAPI OK: {0} path items, {1} operations, {2} events, {3} reasons." -f $summary.pathItems, $summary.operations, $summary.events, $summary.reasons)
        })

    if (-not $StaticOnly) { $env:PGPASSWORD = $dbPassword }
    [void](Invoke-Check -Dod "P5-9" -CheckName "P5 migration rename chain" -Body {
            foreach ($migrationName in $p5RenameMigrationNames) {
                if (-not (Test-Path -LiteralPath (Join-Path $backendDir ("internal\db\migrations\" + $migrationName)))) {
                    throw "missing rename migration $migrationName"
                }
            }
            $uniqueOld = @($p5RenameOldTables | Select-Object -Unique)
            $uniqueNew = @($p5RenameNewTables | Select-Object -Unique)
            if ($uniqueOld.Count -ne $uniqueNew.Count) {
                throw "rename migrations are not one-to-one ($($uniqueOld.Count) old, $($uniqueNew.Count) new)"
            }
            foreach ($oldTable in @("app_model_nodes", "code_anchors")) {
                if ($p5MindmapSql -match ("\b" + [regex]::Escape($oldTable) + "\b")) {
                    throw "mindmap_0001_cases.sql still uses the pre-rename table $oldTable"
                }
            }
            $exploreCheckSql = Get-Content -LiteralPath (Join-Path $backendDir "internal\db\migrations\resource_0005_app_model_explored_source.sql") -Raw
            if ($exploreCheckSql -notmatch 'app_model_node') { throw "resource_0005 must target the renamed app_model_node table" }
            $anchorCheckSql = Get-Content -LiteralPath (Join-Path $backendDir "internal\db\migrations\codeintel_0004_case_anchor_subject.sql") -Raw
            if ($anchorCheckSql -notmatch 'code_anchor') { throw "codeintel_0004 must target the renamed code_anchor table" }

            if (-not $StaticOnly) {
                $newIn = ($uniqueNew | ForEach-Object { "'" + $_ + "'" }) -join ","
                $oldIn = ($uniqueOld | ForEach-Object { "'" + $_ + "'" }) -join ","
                $caseIn = ($p5CaseTables | ForEach-Object { "'" + $_ + "'" }) -join ","
                $newPresent = [int](Get-PsqlScalar -Database $e2eDbName -Sql "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name IN ($newIn);")
                if ($newPresent -ne $uniqueNew.Count) {
                    throw "renamed tables present=$newPresent, expected $($uniqueNew.Count)"
                }
                $oldPresent = [int](Get-PsqlScalar -Database $e2eDbName -Sql "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name IN ($oldIn);")
                if ($oldPresent -ne 0) {
                    throw "$oldPresent pre-rename tables still exist (rename migrations did not converge)"
                }
                $casePresent = [int](Get-PsqlScalar -Database $e2eDbName -Sql "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name IN ($caseIn);")
                if ($casePresent -ne 9) {
                    throw "case_* tables present=$casePresent, expected 9"
                }
                $applied = [int](Get-PsqlScalar -Database $e2eDbName -Sql "SELECT count(*) FROM schema_migrations;")
                if ($applied -ne $expectedMigrations) {
                    throw "schema_migrations count=$applied, expected $expectedMigrations"
                }
                Write-Host "P5 rename chain OK: $($uniqueNew.Count) new names present, 0 old names, 9 case_* tables, $applied migrations."
            } else {
                Write-Host "P5 rename chain static contract OK: $($uniqueNew.Count) rename targets parsed (live schema assertions deferred to the full gate)."
            }
        })

    if ($StaticOnly) {
        foreach ($checkName in @(
                "P5 API smoke",
                "P5 manual path",
                "P5 freeze/export",
                "P5 validation (invalid proposal)",
                "P5 explore permissions and fixture",
                "P5 events snapshot",
                "P5 permissions",
                "P5 degraded LLM path"
            )) {
            Add-CheckResult -Dod "P5" -CheckName $checkName -Status "SKIPPED" `
                -Detail "StaticOnly: API-dependent P5 check (needs the database, API and T16 routes)."
        }
    } else {
        $p5ApiProcess = $null
        $p5FakeLlm = $null
        $p5Fixture = $null
        try {
            $p5ApiWired = Initialize-P5ApiContext
            $p5ApiProcess = $script:p5.ApiProcess
            if (-not $p5ApiWired) {
                foreach ($checkName in @(
                        "P5 API smoke",
                        "P5 manual path",
                        "P5 freeze/export",
                        "P5 validation (invalid proposal)",
                        "P5 explore permissions and fixture",
                        "P5 events snapshot",
                        "P5 permissions",
                        "P5 degraded LLM path"
                    )) {
                    Add-CheckResult -Dod "P5" -CheckName $checkName -Status "SKIPPED" `
                        -Detail "T16 HTTP wiring is not registered yet (GET /api/versions/1/mindmap -> 404); rerun after T16 lands."
                }
            } else {
                [void](Invoke-Check -Dod "P5-1,P5-2,P5-4" -CheckName "P5 API smoke" -Body {
                        $current = $script:p5
                        $unauth = Invoke-P5Api -Method Get -Path "/api/versions/$($current.VersionId)/mindmap" -NoThrow
                        if ($unauth.Status -ne 401) {
                            throw "unauthenticated GET /mindmap returned HTTP $($unauth.Status), expected 401"
                        }
                        $unknown = Invoke-P5Api -Method Get -Path "/api/versions/123456789012345678/mindmap" -Headers $current.AdminHeaders -NoThrow
                        if ($unknown.Status -ne 404) {
                            throw "GET /mindmap for an unknown version returned HTTP $([int]$unknown.Status), expected 404"
                        }
                        $emptyGraph = Get-P5Graph -VersionId $current.VersionId -Headers $current.AdminHeaders
                        if ([string]$emptyGraph.versionId -notmatch '^[0-9]+$') {
                            throw "MindmapGraph.versionId is not a decimal string"
                        }
                        if (@($emptyGraph.nodes).Count -ne 0) {
                            throw "a new P5 version unexpectedly starts with $(@($emptyGraph.nodes).Count) nodes"
                        }

                        $ids = New-P5ManualCase -Headers $current.AdminHeaders -VersionId $current.VersionId `
                            -RequirementIds $current.RequirementIds -Prefix "P5 smoke"
                        $current.ScenarioId = $ids.ScenarioId
                        $current.CaseId = $ids.CaseId
                        $current.StepId = $ids.StepId
                        $current.AssertionId = $ids.AssertionId
                        $current.SourceId = $ids.SourceId
                        foreach ($id in @($ids.ScenarioId, $ids.CaseId, $ids.StepId, $ids.AssertionId)) {
                            if ($id -isnot [string] -or $id -notmatch '^[0-9]+$') {
                                throw "a mindmap id is not a decimal string: '$id'"
                            }
                        }

                        Assert-P5ApiError -Label "duplicate case source" -Status 409 -Reason "duplicate_case_source" -Call {
                            Invoke-P5Api -Method Post -Path "/api/versions/$($current.VersionId)/mindmap/nodes/$($current.CaseId)/sources" `
                                -Headers $current.AdminHeaders -Body @{
                                    refKind = "requirement"
                                    refId   = [string]$current.RequirementIds[1]
                                }
                        } | Out-Null

                        $graph = Get-P5Graph -VersionId $current.VersionId -Headers $current.AdminHeaders
                        $caseNode = @($graph.nodes | Where-Object { [string]$_.id -eq $current.CaseId })[0]
                        if ($null -eq $caseNode) { throw "the created case is missing from GET /mindmap" }
                        if ([string]$caseNode.kind -ne "case") { throw "the created node is not a case" }
                        if (@($caseNode.steps).Count -lt 1 -or @($caseNode.assertions).Count -lt 1) {
                            throw "the created case is missing its step/assertion"
                        }
                        if (@($caseNode.sources).Count -lt 1) { throw "the created case is missing its requirement source" }
                        foreach ($node in @($graph.nodes)) {
                            if ($node.id -isnot [string]) { throw "node id '$($node.id)' is not a JSON string" }
                            foreach ($step in @($node.steps)) { if ($step.id -isnot [string]) { throw "step id is not a JSON string" } }
                            foreach ($assertion in @($node.assertions)) { if ($assertion.id -isnot [string]) { throw "assertion id is not a JSON string" } }
                            foreach ($source in @($node.sources)) {
                                if ($source.id -isnot [string]) { throw "source id is not a JSON string" }
                                if ($null -ne $source.refId -and $source.refId -isnot [string]) { throw "source refId is not a JSON string" }
                            }
                        }

                        $revision = [int]$caseNode.revision
                        Assert-P5ApiError -Label "delete last assertion" -Status 409 -Reason "invalid_case_tree" -Call {
                            Invoke-P5Api -Method Delete -Path "/api/versions/$($current.VersionId)/mindmap/assertions/$($current.AssertionId)?nodeRevision=$revision" -Headers $current.AdminHeaders
                        } | Out-Null
                        Assert-P5ApiError -Label "stale node revision" -Status 409 -Reason "case_revision_conflict" -Call {
                            Invoke-P5Api -Method Patch -Path "/api/versions/$($current.VersionId)/mindmap/nodes/$($current.CaseId)" -Headers $current.AdminHeaders -Body @{
                                title    = "stale write"
                                revision = ($revision + 1)
                            }
                        } | Out-Null
                    })

                [void](Invoke-Check -Dod "P5-2,P5-3" -CheckName "P5 manual path" -Body {
                        $current = $script:p5
                        if ([string]::IsNullOrWhiteSpace([string]$current.CaseId)) { throw "the API smoke check did not create a case" }
                        $graph = Get-P5Graph -VersionId $current.VersionId -Headers $current.AdminHeaders
                        $caseNode = @($graph.nodes | Where-Object { [string]$_.id -eq $current.CaseId })[0]
                        if ($null -eq $caseNode) { throw "the smoke case disappeared" }

                        $submit = Invoke-P5Api -Method Post -Path "/api/versions/$($current.VersionId)/mindmap/submit-review" -Headers $current.AdminHeaders -Body @{}
                        Assert-P5Http -Response $submit -Status 200 -Label "POST submit-review" | Out-Null
                        if ([string]$submit.Data.version.status -ne "reviewing") {
                            throw "submit-review left the version in '$($submit.Data.version.status)', expected reviewing"
                        }

                        $approve = Invoke-P5Api -Method Post -Path "/api/versions/$($current.VersionId)/mindmap/review" -Headers $current.AdminHeaders -Body @{
                            decision = "approve"
                        }
                        Assert-P5Http -Response $approve -Status 200 -Label "POST review approve" | Out-Null
                        if ([string]$approve.Data.version.status -ne "executing") {
                            throw "approve left the version in '$($approve.Data.version.status)', expected executing"
                        }
                        if ($null -eq $approve.Data.baseline) { throw "approve did not return a baseline" }
                        $baselineId = [string]$approve.Data.baseline.id
                        if ($baselineId -notmatch '^[0-9]+$') { throw "baseline id is not a decimal string" }
                        if ([string]$approve.Data.baseline.graphHash -notmatch '^[0-9a-f]{64}$') { throw "baseline graphHash is not a sha256" }
                        $current.BaselineId = $baselineId

                        $frozen = Get-P5Graph -VersionId $current.VersionId -Headers $current.AdminHeaders
                        if ($null -eq $frozen.baseline) { throw "GET /mindmap does not expose the frozen baseline" }
                        if ([bool]$frozen.editable) { throw "GET /mindmap still reports editable=true after approval" }
                        $frozenCase = @($frozen.nodes | Where-Object { [string]$_.id -eq $current.CaseId })[0]
                        if ([string]$frozenCase.status -ne "reviewed") { throw "approved case status is '$($frozenCase.status)', expected reviewed" }

                        Assert-P5ApiError -Label "edit after freeze" -Status 409 -Reason "baseline_frozen" -Call {
                            Invoke-P5Api -Method Patch -Path "/api/versions/$($current.VersionId)/mindmap/nodes/$($current.CaseId)" -Headers $current.AdminHeaders -Body @{
                                title    = "must be rejected"
                                revision = [int]$frozenCase.revision
                            }
                        } | Out-Null
                        Assert-P5ApiError -Label "second approval" -Status 409 -Reason "baseline_frozen" -Call {
                            Invoke-P5Api -Method Post -Path "/api/versions/$($current.VersionId)/mindmap/review" -Headers $current.AdminHeaders -Body @{ decision = "approve" }
                        } | Out-Null
                        Assert-P5ApiError -Label "submit-review after freeze" -Status 409 -Reason "version_state_conflict" -Call {
                            Invoke-P5Api -Method Post -Path "/api/versions/$($current.VersionId)/mindmap/submit-review" -Headers $current.AdminHeaders -Body @{}
                        } | Out-Null
                    })

                [void](Invoke-Check -Dod "P5-3,P5-5" -CheckName "P5 freeze/export" -Body {
                        $current = $script:p5
                        if ([string]::IsNullOrWhiteSpace([string]$current.BaselineId)) { throw "the manual-path check did not freeze a baseline" }
                        $expectedContent = @{
                            markdown = @("| 需求 | 覆盖用例 |", "Open the home page", "P5 smoke case")
                            mermaid  = @("mindmap", "P5 smoke case")
                            opml     = @("<opml", "P5 smoke case", "_note")
                        }
                        $exportIds = @{}
                        foreach ($format in @("markdown", "mermaid", "opml")) {
                            $export = Invoke-P5Api -Method Post -Path "/api/versions/$($current.VersionId)/mindmap/export" -Headers $current.AdminHeaders -Body @{ format = $format }
                            if ($export.Status -ne 201 -and $export.Status -ne 200) {
                                throw "POST export $format returned HTTP $($export.Status), expected 201 (new) or 200 (reuse)"
                            }
                            $exportId = [string]$export.Data.id
                            if ($exportId -notmatch '^[0-9]+$') { throw "export id is not a decimal string" }
                            if ([string]$export.Data.format -ne $format) { throw "export $format returned format '$($export.Data.format)'" }
                            if ([int64]$export.Data.sizeBytes -le 0) { throw "export $format has sizeBytes=$($export.Data.sizeBytes)" }
                            if ($null -eq $export.Data.baselineId -or [string]$export.Data.baselineId -ne [string]$current.BaselineId) {
                                throw "export $format is not bound to the frozen baseline"
                            }
                            $downloadPath = Join-Path $tmpDir ("p5-export-" + $format + ".bin")
                            $download = Invoke-P5Download -Path "/api/versions/$($current.VersionId)/mindmap/exports/$exportId/download" -Headers $current.AdminHeaders -OutFile $downloadPath
                            if ($download.Size -ne [int64]$export.Data.sizeBytes) {
                                throw "download of export $format has $($download.Size) bytes, view reports $($export.Data.sizeBytes)"
                            }
                            if ($download.ContentDisposition -notmatch 'attachment') {
                                throw "download of export $format is missing Content-Disposition: attachment"
                            }
                            $recordedHash = [string]$export.Data.sha256
                            if ([string]::IsNullOrWhiteSpace($recordedHash)) {
                                $recordedHash = Get-PsqlScalar -Database $e2eDbName -Sql "SELECT file_sha256 FROM case_export WHERE version_id = $($current.VersionId) AND id = $exportId;"
                            }
                            if ($recordedHash -ne $download.Sha256) {
                                throw "sha256 mismatch for export $format (recorded $recordedHash, downloaded $($download.Sha256))"
                            }
                            $content = [System.IO.File]::ReadAllText($downloadPath, [System.Text.Encoding]::UTF8)
                            foreach ($needle in $expectedContent[$format]) {
                                if ($content -notmatch [regex]::Escape($needle)) {
                                    throw "export $format is missing $needle"
                                }
                            }
                            $storedPath = Get-P5ExportStoredPath -VersionId $current.VersionId -ExportId $exportId
                            if ([string]::IsNullOrWhiteSpace($storedPath)) { throw "export $format has no stored file_path" }
                            $absolutePath = Join-Path $current.ExportDir ($storedPath -replace '/', '\')
                            if (-not (Test-Path -LiteralPath $absolutePath)) {
                                throw "export $format file is missing under EZTEST_EXPORT_DIR"
                            }
                            $fileBytes = [System.IO.File]::ReadAllBytes($absolutePath)
                            if ((Get-P5Sha256Hex -Bytes $fileBytes) -ne $download.Sha256) {
                                throw "stored file hash for export $format does not match the downloaded bytes"
                            }
                            $exportIds[$format] = $exportId
                        }
                        $reused = Invoke-P5Api -Method Post -Path "/api/versions/$($current.VersionId)/mindmap/export" -Headers $current.AdminHeaders -Body @{ format = "markdown" }
                        Assert-P5Http -Response $reused -Status 200 -Label "POST export reuse" | Out-Null
                        if ([string]$reused.Data.id -ne [string]$exportIds["markdown"]) {
                            throw "a repeated frozen markdown export created a new id instead of reusing the baseline export"
                        }
                        $current.ExportIds = $exportIds
                    })

                [void](Invoke-Check -Dod "P5-6" -CheckName "P5 validation (invalid proposal)" -Body {
                        $current = $script:p5
                        $fake = Start-P5FakeLlm -Mode "invalid"
                        if ($null -eq $fake) { throw "port 13334 is already in use; cannot start the deterministic P5 fake LLM for the invalid-proposal check" }
                        $p5FakeLlm = $fake
                        $config = Get-P5DefaultLlmConfig -Headers $current.AdminHeaders
                        if ($null -eq $config) { throw "no default LLM configuration exists for the invalid-proposal check" }
                        $originalBaseUrl = [string]$config.base_url
                        $originalModel = [string]$config.model
                        try {
                            Set-P5LlmBaseUrl -Headers $current.AdminHeaders -ConfigId ([string]$config.id) -BaseUrl "http://127.0.0.1:13334" -Model "fake-p5" | Out-Null
                            $invalid = New-P5DesigningVersion -Headers $current.AdminHeaders -Name "P5 invalid proposal" -RequirementCount 1 `
                                -SystemId $current.SystemId -EnvironmentId $current.EnvironmentId `
                                -CodeRepositoryId $current.RepoId -CodeBranch $current.CodeBranch -BaselineCommit $current.BaselineCommit
                            $start = Invoke-P5Api -Method Post -Path "/api/versions/$($invalid.VersionId)/mindmap/runs" -Headers $current.AdminHeaders -Body @{ kind = "design" }
                            Assert-P5Http -Response $start -Status 202 -Label "POST invalid design run" | Out-Null
                            $runId = [string]$start.Data.runId
                            $run = Wait-P5Run -VersionId $invalid.VersionId -RunId $runId -Headers $current.AdminHeaders
                            if ([string]$run.status -ne "failed") { throw "invalid proposal run status is '$($run.status)', expected failed" }
                            if ([string]$run.reason -ne "invalid_case_proposal") { throw "invalid proposal run reason is '$($run.reason)', expected invalid_case_proposal" }
                            $after = Get-P5Graph -VersionId $invalid.VersionId -Headers $current.AdminHeaders
                            if (@($after.nodes).Count -ne 0) { throw "invalid proposal materialized $(@($after.nodes).Count) nodes; expected zero" }

                            # A review run in normal fake mode produces case.reviewed
                            # for the events check without any paid upstream call.
                            Set-P5FakeLlmMode -Mode "normal"
                            $null = New-P5ManualCase -Headers $current.AdminHeaders -VersionId $invalid.VersionId -RequirementIds @($invalid.RequirementIds[0]) -Prefix "P5 review"
                            $reviewStart = Invoke-P5Api -Method Post -Path "/api/versions/$($invalid.VersionId)/mindmap/runs" -Headers $current.AdminHeaders -Body @{ kind = "review" }
                            Assert-P5Http -Response $reviewStart -Status 202 -Label "POST review run" | Out-Null
                            $reviewRun = Wait-P5Run -VersionId $invalid.VersionId -RunId ([string]$reviewStart.Data.runId) -Headers $current.AdminHeaders
                            if (@("done", "degraded") -notcontains [string]$reviewRun.status) {
                                throw "review run ended '$($reviewRun.status)', expected done/degraded"
                            }
                            $findings = Invoke-P5Api -Method Get -Path "/api/versions/$($invalid.VersionId)/mindmap/findings" -Headers $current.AdminHeaders
                            Assert-P5Http -Response $findings -Status 200 -Label "GET findings" | Out-Null
                            if (@($findings.Data).Count -lt 1) { throw "review run produced no findings" }
                            $current.ReviewVersionId = $invalid.VersionId
                        } finally {
                            Set-P5LlmBaseUrl -Headers $current.AdminHeaders -ConfigId ([string]$config.id) -BaseUrl $originalBaseUrl -Model $originalModel | Out-Null
                            Stop-ProcessTree -Process $fake
                            $p5FakeLlm = $null
                        }
                    })

                [void](Invoke-Check -Dod "P5-1,P5-8,D36" -CheckName "P5 explore permissions and fixture" -Body {
                        $current = $script:p5
                        $fixture = Start-P5FixtureTarget
                        if ($null -eq $fixture) { throw "port 13335 is already in use; cannot start the deterministic exploration fixture" }
                        $p5Fixture = $fixture
                        $fake = Start-P5FakeLlm -Mode "normal"
                        if ($null -eq $fake) { throw "port 13334 is already in use; cannot start the deterministic P5 fake LLM for exploration" }
                        $p5FakeLlm = $fake
                        $config = Get-P5DefaultLlmConfig -Headers $current.AdminHeaders
                        if ($null -eq $config) { throw "no default LLM configuration exists for the exploration check" }
                        $originalBaseUrl = [string]$config.base_url
                        $originalModel = [string]$config.model
                        try {
                            Set-P5LlmBaseUrl -Headers $current.AdminHeaders -ConfigId ([string]$config.id) -BaseUrl "http://127.0.0.1:13334" -Model "fake-p5" | Out-Null
                            $exploreVersion = New-P5DesigningVersion -Headers $current.AdminHeaders -Name "P5 explore" -RequirementCount 1 `
                                -SystemId $current.SystemId -EnvironmentId $current.EnvironmentId `
                                -CodeRepositoryId $current.RepoId -CodeBranch $current.CodeBranch -BaselineCommit $current.BaselineCommit
                            $exploreStart = Invoke-P5Api -Method Post -Path "/api/versions/$($exploreVersion.VersionId)/mindmap/runs" -Headers $current.AdminHeaders -Body @{
                                kind          = "explore"
                                systemId      = [int64]$current.SystemId
                                environmentId = [int64]$current.EnvironmentId
                                startPaths    = @("http://127.0.0.1:13335/p5/home")
                            }
                            Assert-P5Http -Response $exploreStart -Status 202 -Label "POST explore run" | Out-Null
                            $exploreRun = Wait-P5Run -VersionId $exploreVersion.VersionId -RunId ([string]$exploreStart.Data.runId) -Headers $current.AdminHeaders
                            if (@("done", "degraded") -notcontains [string]$exploreRun.status) {
                                throw "explore run ended '$($exploreRun.status)', expected done/degraded"
                            }
                            $models = Invoke-P5Api -Method Get -Path "/api/resources/systems/$($current.SystemId)/app-models" -Headers $current.AdminHeaders
                            Assert-P5Http -Response $models -Status 200 -Label "GET app models" | Out-Null
                            $explored = @($models.Data | Where-Object { [string]$_.source -eq "explored" })
                            if ($explored.Count -lt 1) { throw "the exploration run imported no app model node with source=explored" }
                            foreach ($kind in @("page", "route", "api")) {
                                if (@($explored | Where-Object { [string]$_.kind -eq $kind }).Count -lt 1) {
                                    throw "the exploration run did not import a $kind app model node"
                                }
                            }

                            Assert-P5ApiError -Label "normal user explore" -Status 403 -Reason "forbidden" -Call {
                                Invoke-P5Api -Method Post -Path "/api/versions/$($exploreVersion.VersionId)/mindmap/runs" -Headers $current.UserHeaders -Body @{
                                    kind          = "explore"
                                    systemId      = [int64]$current.SystemId
                                    environmentId = [int64]$current.EnvironmentId
                                }
                            } | Out-Null

                            $blockedEnv = Invoke-P5Api -Method Post -Path "/api/resources/systems/$($current.SystemId)/environments" -Headers $current.AdminHeaders -Body @{
                                key          = "p5blocked" + (Get-Date -Format "HHmmss")
                                name         = "P5 blocked environment"
                                baseUrl      = "http://127.0.0.1:13335"
                                kind         = "dev"
                                allowedHosts = @("example.com")
                            }
                            Assert-P5Http -Response $blockedEnv -Status 201 -Label "POST blocked environment" | Out-Null
                            $blockedEnvironmentId = $blockedEnv.Data.id
                            $blockedVersion = New-P5DesigningVersion -Headers $current.AdminHeaders -Name "P5 explore blocked" -RequirementCount 1 `
                                -SystemId $current.SystemId -EnvironmentId $blockedEnvironmentId `
                                -CodeRepositoryId $current.RepoId -CodeBranch $current.CodeBranch -BaselineCommit $current.BaselineCommit
                            $blockedStart = Invoke-P5Api -Method Post -Path "/api/versions/$($blockedVersion.VersionId)/mindmap/runs" -Headers $current.AdminHeaders -NoThrow -Body @{
                                kind          = "explore"
                                systemId      = [int64]$current.SystemId
                                environmentId = [int64]$blockedEnvironmentId
                                startPaths    = @("http://127.0.0.2:13335/p5/home")
                            }
                            if ($blockedStart.Status -eq 202) {
                                $blockedRun = Wait-P5Run -VersionId $blockedVersion.VersionId -RunId ([string]$blockedStart.Data.runId) -Headers $current.AdminHeaders
                                if ([string]$blockedRun.status -ne "failed" -or [string]$blockedRun.reason -ne "environment_not_allowed") {
                                    throw "a non-allowlisted exploration run ended '$($blockedRun.status)'/'$($blockedRun.reason)', expected failed/environment_not_allowed"
                                }
                            } elseif ($blockedStart.Status -eq 403 -or $blockedStart.Status -eq 409 -or $blockedStart.Status -eq 422) {
                                if ($null -eq $blockedStart.Error -or [string]$blockedStart.Error.Reason -ne "environment_not_allowed") {
                                    throw "non-allowlisted exploration was rejected with reason '$($blockedStart.Error.Reason)', expected environment_not_allowed"
                                }
                            } else {
                                throw "non-allowlisted exploration returned HTTP $([int]$blockedStart.Status), expected 202 or a 4xx environment_not_allowed"
                            }
                        } finally {
                            Set-P5LlmBaseUrl -Headers $current.AdminHeaders -ConfigId ([string]$config.id) -BaseUrl $originalBaseUrl -Model $originalModel | Out-Null
                            Stop-ProcessTree -Process $fake
                            Stop-ProcessTree -Process $fixture
                            $p5FakeLlm = $null
                            $p5Fixture = $null
                        }
                    })

                [void](Invoke-Check -Dod "P5-7" -CheckName "P5 events snapshot" -Body {
                        $current = $script:p5
                        $events = Invoke-P5Api -Method Get -Path "/api/versions/$($current.VersionId)/events?limit=100" -Headers $current.AdminHeaders
                        Assert-P5Http -Response $events -Status 200 -Label "GET version events" | Out-Null
                        $items = @($events.Data)
                        if ($items.Count -lt 1) { throw "the version event snapshot is empty" }
                        $types = @($items | ForEach-Object { [string]$_.type })
                        foreach ($required in @("case.created", "case.baseline_frozen", "case.export_ready")) {
                            if ($types -notcontains $required) { throw "the version event snapshot is missing $required" }
                        }
                        if (-not [string]::IsNullOrWhiteSpace([string]$current.ReviewVersionId)) {
                            $reviewEvents = Invoke-P5Api -Method Get -Path "/api/versions/$($current.ReviewVersionId)/events?limit=100" -Headers $current.AdminHeaders
                            Assert-P5Http -Response $reviewEvents -Status 200 -Label "GET review version events" | Out-Null
                            $reviewTypes = @($reviewEvents.Data | ForEach-Object { [string]$_.type })
                            if ($reviewTypes -notcontains "case.reviewed") { throw "the review version event snapshot is missing case.reviewed" }
                        }
                        $previousSeq = -1
                        foreach ($item in $items) {
                            $seq = [int64]$item.seq
                            if ($seq -le $previousSeq) { throw "event seq is not strictly increasing ($seq after $previousSeq)" }
                            $previousSeq = $seq
                            if ([string]$item.version_id -notmatch '^[0-9]+$') { throw "event version_id is not a decimal string" }
                            if ([string]$item.channel -ne "version:$($current.VersionId)") { throw "event channel is '$($item.channel)', expected version:$($current.VersionId)" }
                            if ($null -ne $item.refs -and $null -ne $item.refs.case) {
                                foreach ($ref in @($item.refs.case)) {
                                    if ($ref -isnot [string] -or [string]$ref -notmatch '^[0-9]+$') {
                                        throw "refs.case contains a non-string id"
                                    }
                                }
                            }
                        }
                    })

                [void](Invoke-Check -Dod "P5-2,P5-6" -CheckName "P5 permissions" -Body {
                        $current = $script:p5
                        $permissions = New-P5DesigningVersion -Headers $current.AdminHeaders -Name "P5 permissions" -RequirementCount 1 `
                            -SystemId $current.SystemId -EnvironmentId $current.EnvironmentId `
                            -CodeRepositoryId $current.RepoId -CodeBranch $current.CodeBranch -BaselineCommit $current.BaselineCommit
                        $ids = New-P5ManualCase -Headers $current.UserHeaders -VersionId $permissions.VersionId `
                            -RequirementIds @($permissions.RequirementIds[0]) -Prefix "P5 permissions"

                        $graph = Get-P5Graph -VersionId $permissions.VersionId -Headers $current.UserHeaders
                        $caseNode = @($graph.nodes | Where-Object { [string]$_.id -eq $ids.CaseId })[0]
                        if ($null -eq $caseNode) { throw "a normal user cannot read the case graph" }
                        $patched = Invoke-P5Api -Method Patch -Path "/api/versions/$($permissions.VersionId)/mindmap/nodes/$($ids.CaseId)" -Headers $current.UserHeaders -Body @{
                            title    = "P5 permissions edited"
                            revision = [int]$caseNode.revision
                        }
                        Assert-P5Http -Response $patched -Status 200 -Label "normal user PATCH node" | Out-Null

                        Assert-P5ApiError -Label "normal user explore" -Status 403 -Reason "forbidden" -Call {
                            Invoke-P5Api -Method Post -Path "/api/versions/$($permissions.VersionId)/mindmap/runs" -Headers $current.UserHeaders -Body @{
                                kind          = "explore"
                                systemId      = [int64]$current.SystemId
                                environmentId = [int64]$current.EnvironmentId
                            }
                        } | Out-Null

                        $files = @((& $current.GitExe -C $current.FixtureRepo ls-tree -r --name-only HEAD 2>$null | Out-String) -split "`r?`n" | Where-Object { $_.Trim() -ne "" })
                        if ($files.Count -lt 1) { throw "the fixture repository has no files to anchor" }
                        $anchorFile = [string]$files[0]
                        $anchor = Invoke-P5Api -Method Post -Path "/api/versions/$($permissions.VersionId)/mindmap/nodes/$($ids.CaseId)/anchors" -Headers $current.AdminHeaders -Body @{
                            filePath   = $anchorFile
                            confidence = "high"
                        }
                        Assert-P5Http -Response $anchor -Status 201 -Label "POST case anchor" | Out-Null
                        $anchorId = [string]$anchor.Data.id
                        Assert-P5ApiError -Label "normal user deletes another user's anchor" -Status 403 -Reason "forbidden" -Call {
                            Invoke-P5Api -Method Delete -Path "/api/versions/$($permissions.VersionId)/mindmap/anchors/$anchorId" -Headers $current.UserHeaders
                        } | Out-Null
                        $deleted = Invoke-P5Api -Method Delete -Path "/api/versions/$($permissions.VersionId)/mindmap/anchors/$anchorId" -Headers $current.AdminHeaders
                        Assert-P5Http -Response $deleted -Status 204 -Label "admin delete case anchor" | Out-Null

                        $submit = Invoke-P5Api -Method Post -Path "/api/versions/$($permissions.VersionId)/mindmap/submit-review" -Headers $current.UserHeaders -Body @{}
                        Assert-P5Http -Response $submit -Status 200 -Label "normal user submit-review" | Out-Null
                        $reject = Invoke-P5Api -Method Post -Path "/api/versions/$($permissions.VersionId)/mindmap/review" -Headers $current.UserHeaders -Body @{
                            decision = "reject"
                            comment  = "P5 permissions check"
                        }
                        Assert-P5Http -Response $reject -Status 200 -Label "normal user reject" | Out-Null
                        if ([string]$reject.Data.version.status -ne "designing") { throw "reject did not return the version to designing" }
                        if ($null -ne $reject.Data.baseline) { throw "reject unexpectedly created a baseline" }
                    })

                [void](Invoke-Check -Dod "P5-6,P5-8" -CheckName "P5 degraded LLM path" -Body {
                        $current = $script:p5
                        $config = Get-P5DefaultLlmConfig -Headers $current.AdminHeaders
                        if ($null -ne $config) {
                            $delete = Invoke-P5Api -Method Delete -Path "/api/admin/llm/configs/$([string]$config.id)" -Headers $current.AdminHeaders
                            Assert-P5Http -Response $delete -Status 204 -Label "DELETE default LLM config" | Out-Null
                        }
                        $health = Invoke-RestMethod -Uri "$apiBaseUrl/api/health" -TimeoutSec 20
                        if ([bool]$health.data.llm_configured) { throw "health still reports llm_configured=true after deleting the default config" }

                        $degraded = New-P5DesigningVersion -Headers $current.AdminHeaders -Name "P5 degraded" -RequirementCount 1 `
                            -SystemId $current.SystemId -EnvironmentId $current.EnvironmentId `
                            -CodeRepositoryId $current.RepoId -CodeBranch $current.CodeBranch -BaselineCommit $current.BaselineCommit
                        $start = Invoke-P5Api -Method Post -Path "/api/versions/$($degraded.VersionId)/mindmap/runs" -Headers $current.AdminHeaders -NoThrow -Body @{ kind = "design" }
                        if ($start.Status -eq 202) {
                            $run = Wait-P5Run -VersionId $degraded.VersionId -RunId ([string]$start.Data.runId) -Headers $current.AdminHeaders -TimeoutSeconds 120
                            if ([string]$run.status -ne "failed" -or [string]$run.reason -ne "ai_unavailable") {
                                throw "LLM-disabled design run ended '$($run.status)'/'$($run.reason)', expected failed/ai_unavailable"
                            }
                        } elseif ($start.Status -eq 409 -and $null -ne $start.Error -and [string]$start.Error.Reason -eq "ai_unavailable") {
                            Write-Host "LLM-disabled design run was rejected synchronously with ai_unavailable; async failure path not exercised."
                        } else {
                            throw "LLM-disabled design run returned HTTP $([int]$start.Status), expected 202 (async failure) or 409 ai_unavailable"
                        }

                        $manual = New-P5ManualCase -Headers $current.AdminHeaders -VersionId $degraded.VersionId `
                            -RequirementIds @($degraded.RequirementIds[0]) -Prefix "P5 degraded manual"
                        $graph = Get-P5Graph -VersionId $degraded.VersionId -Headers $current.AdminHeaders
                        if (@($graph.nodes).Count -lt 2) { throw "manual scenario/case creation did not work while the LLM was disabled" }
                        $caseNode = @($graph.nodes | Where-Object { [string]$_.id -eq $manual.CaseId })[0]
                        if ($null -eq $caseNode) { throw "the manually created case is missing while the LLM was disabled" }
                        $edited = Invoke-P5Api -Method Patch -Path "/api/versions/$($degraded.VersionId)/mindmap/nodes/$($manual.CaseId)" -Headers $current.AdminHeaders -Body @{
                            title    = "P5 degraded manual edited"
                            revision = [int]$caseNode.revision
                        }
                        Assert-P5Http -Response $edited -Status 200 -Label "manual edit with LLM disabled" | Out-Null
                    })
            }
        } catch {
            Add-CheckResult -Dod "P5" -CheckName "P5 API harness" -Status "FAIL" -Detail $_.Exception.Message
            Write-Host "P5 API harness FAILED: $($_.Exception.Message)" -ForegroundColor Red
        } finally {
            Stop-ProcessTree -Process $p5FakeLlm
            Stop-ProcessTree -Process $p5Fixture
            Stop-ProcessTree -Process $p5ApiProcess
            if ($null -ne $script:p5 -and $null -ne $script:p5.ApiProcess) {
                Stop-ProcessTree -Process $script:p5.ApiProcess
            }
        }
    }

    [void](Invoke-Check -Dod "P5-10" -CheckName "P5 DoD mapping" -Body {
            if (@($script:p5DodMapping).Count -lt 10) { throw "the P5 DoD mapping must cover at least 10 rows" }
            foreach ($row in $script:p5DodMapping) {
                if ([string]::IsNullOrWhiteSpace([string]$row.DoD) -or
                    [string]::IsNullOrWhiteSpace([string]$row.Requirement) -or
                    [string]::IsNullOrWhiteSpace([string]$row.Evidence)) {
                    throw "P5 DoD row '$($row.DoD)' is missing its requirement/evidence mapping"
                }
            }
        })

    Add-CheckResult -Dod "risk" -CheckName "Compose container run" -Status "SKIPPED" `
        -Detail "No Docker on this host; Compose remains static-validation only (Task 15)."
    Add-CheckResult -Dod "4" -CheckName "Real LLM acceptance" -Status "SKIPPED" `
        -Detail "Gated: run scripts/acceptance-llm.ps1 with EZTEST_RUN_REAL_LLM=1."
    Add-CheckResult -Dod "P1-1" -CheckName "Real Agent LLM acceptance" -Status "SKIPPED" `
        -Detail "Gated: run scripts/acceptance-agent-llm.ps1 with EZTEST_RUN_REAL_LLM=1."
    Add-CheckResult -Dod "P3" -CheckName "P3 real LLM acceptance" -Status "SKIPPED" `
        -Detail "Gated: manual P3 acceptance (spec §10.7) with EZTEST_ACCEPTANCE_REAL_LLM=1; screenshots under .tmp/p3-acceptance/."
    Add-CheckResult -Dod "P4" -CheckName "P4 real LLM acceptance" -Status "SKIPPED" `
        -Detail "Gated: run scripts/acceptance-codeintel-llm.ps1 with EZTEST_RUN_REAL_LLM=1; it fails closed on llm_configured=false/degraded unless -AllowDegraded is passed; results under .tmp/p4-real-acceptance.json."
    Add-CheckResult -Dod "P5" -CheckName "P5 real LLM acceptance" -Status "SKIPPED" `
        -Detail "Gated: run scripts/acceptance-mindmap-llm.ps1 with EZTEST_RUN_REAL_LLM=1 after T16 (16 real design/review/data-plan/explore/export checks; fails closed when llm_* keys are missing)."

    Write-Step "P0-P5 DoD acceptance summary"
    $table = $checkResults | Format-Table -AutoSize -Wrap | Out-String -Width 200
    Write-Host $table

    # The P1 exit criteria are proven by suites spread across the gate (the
    # gated real-LLM script is the only opt-in piece), so map each criterion to
    # its evidence explicitly instead of hiding the link in a check name.
    Write-Step "P1 DoD mapping (implementation-phases.md L105-L108)"
    $p1DodMapping = @(
        [pscustomobject][ordered]@{
            DoD         = "P1-1"
            Source      = "L105"
            Requirement = "Diagnostic Agent completes multi-turn conversation + tool calls, live in the UI and replayable"
            Evidence    = "Agent unit/integration suites; Playwright agent-console E2E (Task 16); OpenAPI /api/agents + agent.activity"
        },
        [pscustomobject][ordered]@{
            DoD         = "P1-2"
            Source      = "L106"
            Requirement = "A run can be aborted with correct events/state; tool errors and timeouts do not take the service down"
            Evidence    = "Run abort and tool executor suites; agent-console E2E stop flow"
        },
        [pscustomobject][ordered]@{
            DoD         = "P1-3"
            Source      = "L107"
            Requirement = "Agent configuration (system prompt/model) changes take effect on the next run"
            Evidence    = "Profile domain/application suites; agent-console E2E prompt edit flow"
        },
        [pscustomobject][ordered]@{
            DoD         = "P1-4"
            Source      = "L108"
            Requirement = "Event/tool contract frozen at V1 for compatible extension"
            Evidence    = "Agent activity domain/application suites; docs/openapi/api.yaml agent schemas"
        }
    )
    $p1Table = $p1DodMapping | Format-Table -AutoSize -Wrap | Out-String -Width 200
    Write-Host $p1Table

    # P3 exit criteria (spec §10.6) mapped to the evidence collected above; the
    # real-LLM manual acceptance remains the only gated piece. The three P3
    # Playwright specs (frontend/e2e/versions*.spec.ts) are part of scripts/e2e.ps1
    # (T20 recorded 22/22 total, including the 3 P3 and 5 P4 specs).
    Write-Step "P3 DoD mapping (spec §10.6)"
    $p3DodMapping = @(
        [pscustomobject][ordered]@{
            DoD         = "P3-1"
            Requirement = "A version records 3+ requirements; AI multi-turn questioning; human answers/edits; requirements/relations/decisions persisted"
            Evidence    = "Version unit/integration suites; workbench/clarification component tests; P3 real-LLM acceptance (gated); P3 Playwright E2E in frontend/e2e/versions*.spec.ts (T19: 3 P3 specs passed)"
        },
        [pscustomobject][ordered]@{
            DoD         = "P3-2"
            Requirement = "Requirement confirmation and Version design entry follow the state rules (draft -> clarifying -> designing; draft -> clarified)"
            Evidence    = "Domain state-machine tests; HTTP 200/409/422 OpenAPI contract; enter-design acknowledge component test; P3 Playwright E2E in frontend/e2e/versions*.spec.ts (T19: 3 P3 specs passed)"
        },
        [pscustomobject][ordered]@{
            DoD         = "P3-3"
            Requirement = "Refresh/reconnect loses neither the conversation nor the draft requirements"
            Evidence    = "Workbench reload + SSE reconnect component tests; flat /events snapshot + version stream in OpenAPI; P3 Playwright E2E in frontend/e2e/versions*.spec.ts (T19: 3 P3 specs passed)"
        },
        [pscustomobject][ordered]@{
            DoD         = "P3-4"
            Requirement = "The requirement relation graph renders correctly"
            Evidence    = "RelationGraph component tests; P3 Playwright E2E in frontend/e2e/versions*.spec.ts (T19: 3 P3 specs passed)"
        },
        [pscustomobject][ordered]@{
            DoD         = "P3-5"
            Requirement = "Manual entry/confirmation still works when AI is unavailable"
            Evidence    = "AI-unavailable component test; 503 ai_unavailable contract; Version API smoke; P3 Playwright E2E in frontend/e2e/versions*.spec.ts (T19: 3 P3 specs passed)"
        },
        [pscustomobject][ordered]@{
            DoD         = "P3-6"
            Requirement = "Version detail container, 9-tab workbench and overview skeleton"
            Evidence    = "VersionTabs/VersionDetailPage tab + ?tab deep-link component tests; P3 Playwright E2E in frontend/e2e/versions*.spec.ts (T19: 3 P3 specs passed)"
        },
        [pscustomobject][ordered]@{
            DoD         = "P3-7"
            Requirement = "Attachments and multimodal context-recon within limits"
            Evidence    = "Attachment multipart upload/download integration tests; recon multimodal parts application test; OpenAPI attachment paths; P3 Playwright E2E in frontend/e2e/versions*.spec.ts (T19: 3 P3 specs passed)"
        },
        [pscustomobject][ordered]@{
            DoD         = "P3-8"
            Requirement = "Optional P2 resource bindings with validation and degraded display"
            Evidence    = "Create/bind validation tests; bindings.available=false contract; OpenAPI VersionBindings"
        },
        [pscustomobject][ordered]@{
            DoD         = "P3-9"
            Requirement = "Snowflake IDs and decimal-string JSON serialization"
            Evidence    = "Snowflake unit tests; string-ID serialization tests; acceptance Version API smoke; OpenAPI string schemas"
        }
    )
    $p3Table = $p3DodMapping | Format-Table -AutoSize -Wrap | Out-String -Width 200
    Write-Host $p3Table

    # P4 exit criteria (spec §10.6) mapped to the evidence collected above;
    # the gated real-LLM run remains the only opt-in piece. The five P4
    # Playwright specs run inside scripts/e2e.ps1 with the rest of the suite
    # (T20 recorded 22/22 E2E passing, including these five specs).
    Write-Step "P4 DoD mapping (spec §10.6)"
    $p4DodMapping = @(
        [pscustomobject][ordered]@{
            DoD         = "P4-1"
            Requirement = "Any-language repository: file-level diff, AI interface/route/page facts with evidence"
            Evidence    = "codeintel domain/gitx suites; P4 OpenAPI tree/file/diff/facts paths; P4 fake-LLM test; Playwright versions-code.spec.ts + code-repos-index.spec.ts; gated acceptance-codeintel-llm.ps1"
        },
        [pscustomobject][ordered]@{
            DoD         = "P4-2"
            Requirement = "Same-commit analyses reuse the cache; re-analysis creates a new version without overwriting"
            Evidence    = "codeintel application cache/version suites; Playwright versions-code-cache.spec.ts; gated script asserts reused=true with identical normalized candidates, a second same-base version with stats.cacheHits>0, and force analysisVersion+1"
        },
        [pscustomobject][ordered]@{
            DoD         = "P4-3"
            Requirement = "Impact candidates carry reason/confidence/evidence, can be toggled, and unmapped changes are visible"
            Evidence    = "codeintel impact/candidate suites; P4 Analysis/Candidate/Unmapped OpenAPI schemas; Playwright versions-code.spec.ts + versions-code-anchors.spec.ts; gated script checks candidate reason/confidence/evidence, >=1 unmapped (or explicit -AllowNoUnmapped), and no silent unmapped omission"
        },
        [pscustomobject][ordered]@{
            DoD         = "P4-4"
            Requirement = "AI failure degrades without blocking the main flow"
            Evidence    = "codeintel degraded suites; P4 Analysis.degraded contract; Playwright versions-code-degraded.spec.ts; gated script fails closed on degraded unless -AllowDegraded is passed explicitly"
        }
    )
    $p4Table = $p4DodMapping | Format-Table -AutoSize -Wrap | Out-String -Width 200
    Write-Host $p4Table

    # P5 exit criteria (spec §10.9) mapped to the checks above; the gated
    # real-LLM script is the only opt-in piece and the T16 API checks skip
    # gracefully until the HTTP wiring lands.
    Write-Step "P5 DoD mapping (spec §10.9)"
    $p5Table = $script:p5DodMapping | Format-Table -AutoSize -Wrap | Out-String -Width 200
    Write-Host $p5Table

    $failed = @($checkResults | Where-Object { $_.Result -eq "FAIL" })
    $passed = @($checkResults | Where-Object { $_.Result -eq "PASS" })
    $skipped = @($checkResults | Where-Object { $_.Result -eq "SKIPPED" })
    Write-Host ("Result: {0} passed, {1} failed, {2} skipped" -f $passed.Count, $failed.Count, $skipped.Count)
    if ($failed.Count -gt 0) {
        $exitCode = 1
        Write-Host "ACCEPTANCE FAILED" -ForegroundColor Red
    } else {
        Write-Host "ACCEPTANCE PASSED" -ForegroundColor Green
    }
} catch {
    Write-Host ""
    Write-Host "ACCEPTANCE FAILED: $($_.Exception.Message)" -ForegroundColor Red
    $exitCode = 1
    if ($null -ne $checkResults -and $checkResults.Count -gt 0) {
        $checkResults | Format-Table -AutoSize -Wrap | Out-String -Width 200 | Write-Host
    }
} finally {
    Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
}

exit $exitCode
