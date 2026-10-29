#requires -Version 5.1
<#
.SYNOPSIS
    Optional real-LLM acceptance for P6 execution (gated by
    EZTEST_RUN_REAL_LLM=1).

.DESCRIPTION
    Without the gate this script prints SKIPPED and exits 0. With the gate open
    it drives one real-model P6 acceptance run against the deterministic P6
    fixture target (spec §10.7):

      1. gate: EZTEST_RUN_REAL_LLM=1, all llm_base_url/llm_key/llm_model_name
         keys present, a healthy API/executor and a reachable fixture; a missing
         prerequisite is a failure, never a skip;
      2. a dedicated eztest_p6_llm database is dropped/recreated, the current
         API is built and started on :13333, migrations apply, health reports
         llm_configured=true and the administrator logs in;
      3. seed: fixture system/environment (allow auto login), an auth flow, an
         account/access, one imported valid fixture session, a version with a
         confirmed requirement, three execution cases and an approved baseline
         (the version moves to executing);
      4. the real model executes the three cases (login/home -> orders list ->
         order detail): passed, steps/assertions recorded and screenshot
         evidence present;
      5. a deliberately impossible assertion case is classified
         failed(application) with evidence;
      6. an environment whose base URL is not reachable/allowed is classified
         blocked(environment);
      7. a session world with a wrong credential is classified blocked(session);
      8. graceful pause -> pending checkpoint -> resume from the checkpoint;
      9. immediate pause -> interrupted -> resume with cleanup;
     10. killing the API mid-case and restarting it recovers the case to
         interrupted and the batch to paused(restart);
     11. the Batch 1 report JSON and the Markdown export (sha256) are verified;
     12. the version event snapshot has strictly increasing sequence numbers and
         the expected execution event types;
     13. a normal user can create/pause a batch while auth-flow CRUD stays
         admin-only;
     14. elapsed time and per-case durations are recorded as optional metrics.

    The script is fail-closed: a degraded result, a missing evidence artifact or
    an unexpected classification fails the run. Results (statuses, ids and
    counts only - never prompts, keys or evidence bytes) are written to
    .tmp/p6-real-acceptance.json.

    -ConcurrentCases N (1..8, default 1) keeps the baseline acceptance at one
    case/session but makes the same fixture harness able to exercise the P8
    8-concurrency gate: N sessions are imported on an access whose concurrency
    limit is N, a version with N cases is approved and one batch is run while
    the harness samples concurrently running case runs. The extra check only
    runs when N >= 2; the default path is unchanged.

.PARAMETER DryRun
    Validate paths, the .env key presence (names only), the toolchain, the
    fixture/executor scripts and the database URL shape without resetting the
    database, starting processes or spending an LLM call. Exits 0 when the
    preflight passes.

.PARAMETER ConcurrentCases
    Case/session count for the optional concurrency gate (1..8, default 1).
    Values >= 2 add the "<N>-concurrency fixture batch" check against the real
    configured model; the baseline run keeps its single-session semantics.

.NOTES
    This script makes paid upstream calls and is deliberately not part of
    scripts/acceptance.ps1 (which reports it as gated). It only drops
    eztest_p6_llm after Assert-SafeDatabaseUrl rejected target-changing query
    parameters. Compatible with Windows PowerShell 5.1 and PowerShell 7+.
#>
[CmdletBinding()]
param(
    [switch]$DryRun,
    [ValidateRange(1, 8)]
    [int]$ConcurrentCases = 1
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$backendDir = Join-Path $repoRoot "backend"
$scriptsDir = Join-Path $repoRoot "scripts"
$tmpDir = Join-Path $repoRoot ".tmp"
$envPath = Join-Path $repoRoot ".env"
$resultPath = Join-Path $tmpDir "p6-real-acceptance.json"
$dbName = "eztest_p6_llm"
$apiPort = 13333
$apiBaseUrl = "http://localhost:$apiPort"
$fixturePort = 13335
$fixtureBaseUrl = "http://127.0.0.1:$fixturePort"
$executorPort = 13336
$executorToken = "p6-real-executor-token"
$requiredEnvKeys = @(
    "EZTEST_DATABASE_URL",
    "EZTEST_ADMIN_USERNAME",
    "EZTEST_ADMIN_PASSWORD",
    "EZTEST_SECRET_KEYS",
    "EZTEST_SECRET_KEY_ACTIVE",
    "llm_base_url",
    "llm_key",
    "llm_model_name"
)

$script:secretValues = @()
$script:checks = New-Object System.Collections.ArrayList
$script:resultData = [ordered]@{
    status         = "running"
    generatedAt    = [DateTime]::UtcNow.ToString("o")
    database       = $dbName
    llmModel       = $null
    versionId      = $null
    batchId        = $null
    caseCount      = $null
    passedCount    = $null
    failedClass    = $null
    blockedClasses = @()
    pauseModes     = @()
    restartReason  = $null
    reportSha256   = $null
    eventCount     = $null
    elapsedMs      = $null
    caseDurations  = @()
    concurrentCases    = $ConcurrentCases
    concurrentObserved = $null
    concurrentErrors   = $null
    checks         = @()
    error          = $null
}

function Write-Step {
    param([string]$Message)
    Write-Host ""
    Write-Host "==> $Message" -ForegroundColor Cyan
}

function Read-DotEnv {
    param([string]$Path)
    $values = @{}
    if (-not (Test-Path -LiteralPath $Path)) { throw "Missing .env at $Path" }
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

function Resolve-ToolPath {
    param([string]$LocalPath, [string]$CommandName)
    if (-not [string]::IsNullOrWhiteSpace($LocalPath) -and (Test-Path -LiteralPath $LocalPath)) { return $LocalPath }
    $command = Get-Command $CommandName -ErrorAction SilentlyContinue
    if ($null -ne $command) { return $command.Source }
    return ""
}

function Get-RedactedText {
    param([string]$Text)
    $redacted = $Text
    foreach ($secret in $script:secretValues) {
        if (-not [string]::IsNullOrEmpty($secret)) {
            $redacted = $redacted.Replace($secret, "***")
        }
    }
    return $redacted
}

function Stop-ProcessTree {
    param($Process)
    if ($null -eq $Process) { return }
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        $Process.Refresh()
        if (-not $Process.HasExited) { & taskkill.exe /PID $Process.Id /T /F 2>$null | Out-Null }
    } catch {
        # Best effort cleanup.
    } finally {
        $ErrorActionPreference = $previous
    }
}

function Wait-PortFree {
    param([int]$Port, [int]$TimeoutSeconds = 20)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        if (-not (Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue)) { return }
        Start-Sleep -Milliseconds 250
    }
    throw "Port $Port is still in use"
}

function Assert-PortFree {
    param([int]$Port)
    if (Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue) {
        throw "Port $Port is already in use"
    }
}

function Wait-ForHttp {
    param([string]$Url, [int]$TimeoutSeconds, $Process, [string]$Label)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        if ($null -ne $Process) {
            $Process.Refresh()
            if ($Process.HasExited) { throw "$Label exited early with code $($Process.ExitCode)" }
        }
        try {
            $response = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec 10
            if ([int]$response.StatusCode -eq 200) { return }
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
    if ($code -ne 0) { throw "psql scalar query failed (exit $code)" }
    return (($output | Out-String).Trim())
}

function Invoke-PsqlQuiet {
    param([string]$Database, [string]$Sql, [string]$Operation)
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        & $psqlExe -h $dbHost -p $dbPort -U $dbUser -d $Database -v ON_ERROR_STOP=1 -q -c $Sql *> $null
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($code -ne 0) { throw "$Operation failed (psql exit $code)" }
}

function Assert-SafeDatabaseUrl {
    param([string]$DatabaseUrl)
    $uri = $null
    if (-not [System.Uri]::TryCreate($DatabaseUrl, [System.UriKind]::Absolute, [ref]$uri)) {
        throw "EZTEST_DATABASE_URL is not a valid absolute URL"
    }
    if ($uri.Scheme -ne "postgres" -and $uri.Scheme -ne "postgresql") {
        throw "EZTEST_DATABASE_URL must use the postgres scheme"
    }
    if ([string]::IsNullOrWhiteSpace($uri.Host)) { throw "EZTEST_DATABASE_URL must include a host" }
    if ($uri.Query -ne "") {
        foreach ($pair in ($uri.Query.TrimStart([char]"?")) -split "&") {
            if ([string]::IsNullOrWhiteSpace($pair)) { continue }
            $equals = $pair.IndexOf("=")
            $key = if ($equals -ge 0) { $pair.Substring(0, $equals) } else { $pair }
            $key = [System.Uri]::UnescapeDataString($key).Trim().ToLowerInvariant()
            if ($key -eq "dbname" -or $key -eq "database" -or $key -eq "host" -or $key -eq "port") {
                throw "EZTEST_DATABASE_URL must not override the connection target with '$key'"
            }
        }
    }
    $databaseName = [System.Uri]::UnescapeDataString($uri.AbsolutePath.TrimStart([char[]]"/"))
    if ([string]::IsNullOrWhiteSpace($databaseName) -or $databaseName.Contains("/")) {
        throw "EZTEST_DATABASE_URL must include a single database name in its path"
    }
    return $uri
}

function New-DatabaseUrl {
    param([System.Uri]$DatabaseUri, [string]$DatabaseName)
    $builder = New-Object System.UriBuilder -ArgumentList @($DatabaseUri)
    $builder.Path = "/" + $DatabaseName
    return $builder.Uri.AbsoluteUri
}

function Get-ApiErrorSummary {
    param($ErrorRecord)
    $status = 0
    $reason = ""
    $rawBody = $null
    # Invoke-Api rethrows the HTTP failure as a plain exception so callers get a
    # short message. Preserve the status/reason on that exception so
    # Assert-ApiError can still verify an expected error response.
    if ($null -ne $ErrorRecord.Exception -and $null -ne $ErrorRecord.Exception.Data -and
        $ErrorRecord.Exception.Data.Contains("apiStatus")) {
        return [pscustomobject]@{
            Status = [int]$ErrorRecord.Exception.Data["apiStatus"]
            Reason = [string]$ErrorRecord.Exception.Data["apiReason"]
            Text   = (Get-RedactedText -Text ([string]$ErrorRecord.Exception.Message))
        }
    }
    try {
        $response = $ErrorRecord.Exception.Response
        if ($null -ne $response) {
            $status = [int]$response.StatusCode
            if ($response -is [System.Net.HttpWebResponse]) {
                $stream = $response.GetResponseStream()
                if ($null -ne $stream) {
                    $reader = New-Object System.IO.StreamReader($stream)
                    try { $rawBody = $reader.ReadToEnd() } finally { $reader.Close() }
                }
            } elseif ($null -ne $response.Content) {
                $rawBody = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
            }
        }
    } catch {
        # Keep what was extracted safely.
    }
    if ([string]::IsNullOrWhiteSpace($rawBody) -and $null -ne $ErrorRecord.ErrorDetails) {
        $rawBody = [string]$ErrorRecord.ErrorDetails.Message
    }
    if (-not [string]::IsNullOrWhiteSpace($rawBody)) {
        try {
            $parsed = $rawBody | ConvertFrom-Json
            if ($null -ne $parsed.error.details) { $reason = [string]$parsed.error.details.reason }
        } catch {
            $reason = ""
        }
    }
    return [pscustomobject]@{ Status = $status; Reason = $reason; Text = (Get-RedactedText -Text ([string]$ErrorRecord.Exception.Message)) }
}

function Invoke-Api {
    param(
        [string]$Method,
        [string]$Path,
        $Body = $null,
        $Headers = $null,
        [int]$TimeoutSec = 120,
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
        $arguments["ContentType"] = "application/json; charset=utf-8"
        $arguments["Body"] = [System.Text.Encoding]::UTF8.GetBytes(($Body | ConvertTo-Json -Compress -Depth 12))
    }
    try {
        $response = Invoke-WebRequest @arguments
    } catch {
        $info = Get-ApiErrorSummary -ErrorRecord $_
        if ($NoThrow) {
            return [pscustomobject]@{ Status = $info.Status; Raw = $null; Data = $null; Error = $info }
        }
        $failure = New-Object System.Exception ("{0} {1} failed: HTTP {2} reason={3}" -f $Method, $Path, $info.Status, $info.Reason)
        $failure.Data["apiStatus"] = $info.Status
        $failure.Data["apiReason"] = $info.Reason
        throw $failure
    }
    $raw = $null
    if ($response.Content -is [string] -and -not [string]::IsNullOrWhiteSpace([string]$response.Content)) {
        try { $raw = $response.Content | ConvertFrom-Json } catch { $raw = $null }
    }
    $data = $null
    if ($null -ne $raw) { $data = $raw.data }
    return [pscustomobject]@{ Status = [int]$response.StatusCode; Raw = $raw; Data = $data; Error = $null }
}

function Assert-ApiError {
    param([string]$Label, [scriptblock]$Call, [int]$Status, [string]$Reason = "")
    try {
        & $Call | Out-Null
    } catch {
        $info = Get-ApiErrorSummary -ErrorRecord $_
        if ($info.Status -eq 0) { throw "$Label failed without an HTTP response: $($info.Text)" }
        if ($info.Status -ne $Status) { throw "$Label returned HTTP $($info.Status), expected $Status" }
        if (-not [string]::IsNullOrWhiteSpace($Reason) -and $info.Reason -ne $Reason) {
            throw "$Label returned reason '$($info.Reason)', expected '$Reason'"
        }
        return $info
    }
    throw "$Label unexpectedly succeeded"
}

function Get-Token {
    param([string]$Username, [string]$Password)
    $login = Invoke-Api -Method Post -Path "/api/auth/login" -Body @{ username = $Username; password = $Password }
    if ($login.Status -ne 200) { throw "login for $Username returned HTTP $($login.Status)" }
    $token = [string]$login.Data.token
    if ([string]::IsNullOrWhiteSpace($token)) { throw "login for $Username did not return a token" }
    return $token
}

function Get-Sha256Hex {
    param([byte[]]$Bytes)
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try { $hash = $sha.ComputeHash($Bytes) } finally { $sha.Dispose() }
    return (($hash | ForEach-Object { $_.ToString("x2") }) -join "")
}

function Invoke-Download {
    param([string]$Path, $Headers)
    $client = New-Object System.Net.WebClient
    try {
        if ($null -ne $Headers) {
            foreach ($key in $Headers.Keys) { $client.Headers.Add([string]$key, [string]$Headers[$key]) }
        }
        $bytes = $client.DownloadData($apiBaseUrl + $Path)
        $sha256 = [string]$client.ResponseHeaders["X-Content-SHA256"]
        $disposition = [string]$client.ResponseHeaders["Content-Disposition"]
    } finally {
        $client.Dispose()
    }
    return [pscustomobject]@{
        Bytes              = $bytes
        Size               = @($bytes).Count
        Sha256             = (Get-Sha256Hex -Bytes $bytes)
        HeaderSha256       = $sha256
        ContentDisposition = $disposition
    }
}

function Get-FixtureCookie {
    $request = [System.Net.HttpWebRequest]::Create("$fixtureBaseUrl/p6/login")
    $request.Method = "POST"
    $request.AllowAutoRedirect = $false
    $request.ContentType = "application/x-www-form-urlencoded"
    $body = [System.Text.Encoding]::UTF8.GetBytes("username=fixture&password=fixture-pass")
    $request.ContentLength = $body.Length
    $stream = $request.GetRequestStream()
    try { $stream.Write($body, 0, $body.Length) } finally { $stream.Close() }
    $response = $null
    try {
        $response = $request.GetResponse()
    } catch [System.Net.WebException] {
        $response = $_.Exception.Response
    }
    if ($null -eq $response) { throw "fixture P6 login returned no response" }
    try {
        $status = [int]$response.StatusCode
        $cookieHeader = [string]$response.Headers["Set-Cookie"]
    } finally {
        $response.Close()
    }
    if ($status -ne 302) { throw "fixture P6 login returned HTTP $status, expected 302" }
    if ($cookieHeader -notmatch 'p6_session=([^;]+)') { throw "fixture P6 login did not set p6_session" }
    return $Matches[1]
}

function Import-FixtureSession {
    param($Headers, $AccessId, $EnvironmentId)
    $cookie = Get-FixtureCookie
    $state = @{
        cookies = @(@{
                name     = "p6_session"
                value    = $cookie
                domain   = "127.0.0.1"
                path     = "/"
                expires  = -1
                httpOnly = $true
                secure   = $false
                sameSite = "Lax"
            })
        origins = @(@{ origin = $fixtureBaseUrl; localStorage = @(); sessionStorage = @() })
    }
    $response = Invoke-Api -Method Post -Path "/api/resources/accesses/$AccessId/sessions" -Headers $Headers -Body @{
        environmentId = [int64]$EnvironmentId
        kind          = "storage_state"
        storageState  = $state
        note          = "P6 real-LLM acceptance imported fixture session"
    }
    if ($response.Status -ne 201) { throw "POST import session returned HTTP $($response.Status)" }
    if ([string]$response.Data.state -ne "valid") { throw "the imported fixture session is '$($response.Data.state)'" }
    return $response.Data.id
}

function New-DesigningVersion {
    param($Headers, [string]$Name, $SystemId, $EnvironmentId)
    $version = Invoke-Api -Method Post -Path "/api/versions" -Headers $Headers -Body @{
        name        = $Name
        description = "P6 real-LLM acceptance version"
        systemId    = [int64]$SystemId
        environmentId = [int64]$EnvironmentId
    }
    if ($version.Status -ne 201) { throw "POST /api/versions returned HTTP $($version.Status)" }
    $versionId = [string]$version.Data.id
    $requirement = Invoke-Api -Method Post -Path "/api/versions/$versionId/requirements" -Headers $Headers -Body @{
        title              = "$Name requirement"
        description        = "Real-LLM acceptance requirement for the P6 fixture flow."
        acceptanceCriteria = "The execution agent completes the fixture order flow."
        priority           = "must"
    }
    if ($requirement.Status -ne 201) { throw "POST requirement returned HTTP $($requirement.Status)" }
    $requirementId = [string]$requirement.Data.id
    $clarify = Invoke-Api -Method Post -Path "/api/versions/$versionId/start-clarification" -Headers $Headers
    if ($clarify.Status -ne 200) { throw "POST start-clarification returned HTTP $($clarify.Status)" }
    $confirmed = Invoke-Api -Method Post -Path "/api/versions/$versionId/requirements/$requirementId/confirm" -Headers $Headers
    if ($confirmed.Status -ne 200) { throw "POST requirement confirm returned HTTP $($confirmed.Status)" }
    $enter = Invoke-Api -Method Post -Path "/api/versions/$versionId/enter-design" -Headers $Headers -Body @{ acknowledgeUnclarified = $true }
    if ($enter.Status -ne 200) { throw "POST enter-design returned HTTP $($enter.Status)" }
    return [pscustomobject]@{ VersionId = $versionId; RequirementId = $requirementId }
}

function New-RealCase {
    param($Headers, [string]$VersionId, [string]$Title, [string[]]$Actions, [string[]]$Expected, [string]$AssertionTarget, [string]$AssertionExpression, [string]$AssertionExpected, [string]$RequirementId = "")
    $scenario = Invoke-Api -Method Post -Path "/api/versions/$VersionId/mindmap/nodes" -Headers $Headers -Body @{
        kind  = "scenario"
        title = "$Title scenario"
    }
    if ($scenario.Status -ne 201) { throw "POST scenario returned HTTP $($scenario.Status)" }
    $caseBody = @{
        kind     = "case"
        parentId = [string]$scenario.Data.id
        title    = $Title
        priority = "P0"
    }
    # A case node must reference at least one clarified requirement; the
    # requirement was created and confirmed by New-DesigningVersion.
    if (-not [string]::IsNullOrWhiteSpace($RequirementId)) { $caseBody["requirementIds"] = @($RequirementId) }
    $case = Invoke-Api -Method Post -Path "/api/versions/$VersionId/mindmap/nodes" -Headers $Headers -Body $caseBody
    if ($case.Status -ne 201) { throw "POST case returned HTTP $($case.Status)" }
    $caseId = [string]$case.Data.id
    $stepIds = @()
    for ($i = 0; $i -lt $Actions.Count; $i++) {
        $step = Invoke-Api -Method Post -Path "/api/versions/$VersionId/mindmap/nodes/$caseId/steps" -Headers $Headers -Body @{
            action   = $Actions[$i]
            expected = $Expected[$i]
        }
        if ($step.Status -ne 201) { throw "POST case step returned HTTP $($step.Status)" }
        $stepIds += [string]$step.Data.id
    }
    $assertion = Invoke-Api -Method Post -Path "/api/versions/$VersionId/mindmap/nodes/$caseId/assertions" -Headers $Headers -Body @{
        stepId     = $stepIds[$stepIds.Count - 1]
        kind       = "ui"
        target     = $AssertionTarget
        expression = $AssertionExpression
        expected   = $AssertionExpected
    }
    if ($assertion.Status -ne 201) { throw "POST case assertion returned HTTP $($assertion.Status)" }
    return [pscustomobject]@{ CaseId = $caseId; ScenarioId = [string]$scenario.Data.id }
}

function New-RealExecutingVersion {
    param($Headers, [string]$Name, $SystemId, $EnvironmentId, [string]$Kind)
    $version = New-DesigningVersion -Headers $Headers -Name $Name -SystemId $SystemId -EnvironmentId $EnvironmentId
    $versionId = $version.VersionId
    $requirementId = $version.RequirementId
    $caseIds = @()
    $cases = @()
    switch ($Kind) {
        "three" {
            $cases += @{
                Title  = "P6 home and orders list"
                Actions = @("Open $fixtureBaseUrl/p6/home", "Open $fixtureBaseUrl/p6/orders")
                Expected = @("The fixture home page is shown", "The orders list is shown")
                Target = "body"
                Expression = "text contains Order"
                AssertionExpected = "The orders list contains at least one order"
            }
            $cases += @{
                Title  = "P6 create order"
                Actions = @("Open $fixtureBaseUrl/p6/orders/new", "Fill the order number field with P6-REAL-1 and submit the form")
                Expected = @("The new-order form is shown", "The order is created and the detail page is shown")
                Target = "body"
                Expression = "text contains P6-REAL-1"
                AssertionExpected = "The created order number is visible"
            }
            $cases += @{
                Title  = "P6 order detail"
                Actions = @("Open $fixtureBaseUrl/p6/orders/1")
                Expected = @("The first order detail page is shown")
                Target = "body"
                Expression = "text contains Order"
                AssertionExpected = "The order detail page renders order data"
            }
        }
        "fail" {
            $cases += @{
                Title  = "P6 impossible assertion"
                Actions = @("Open $fixtureBaseUrl/p6/home")
                Expected = @("The fixture home page is shown")
                Target = "body"
                Expression = "text contains P6-THIS-TEXT-DOES-NOT-EXIST"
                AssertionExpected = "P6-THIS-TEXT-DOES-NOT-EXIST"
            }
        }
        "slow" {
            $cases += @{
                Title  = "P6 slow page for pause and restart"
                Actions = @("Open $fixtureBaseUrl/p6/slow?ms=8000", "Open $fixtureBaseUrl/p6/home")
                Expected = @("The slow fixture page eventually responds", "The home page is shown")
                Target = "body"
                Expression = "text contains Order"
                AssertionExpected = "The home page renders navigation"
            }
        }
        "concurrent" {
            for ($concurrentIndex = 1; $concurrentIndex -le $ConcurrentCases; $concurrentIndex++) {
                $cases += @{
                    Title  = "P6 concurrent case $concurrentIndex"
                    Actions = @("Open $fixtureBaseUrl/p6/orders")
                    Expected = @("The fixture orders list is shown")
                    Target = "body"
                    Expression = "text contains Order"
                    AssertionExpected = "The orders page renders order data"
                }
            }
        }
        default { throw "unknown real-LLM case kind '$Kind'" }
    }
    foreach ($caseSpec in $cases) {
        $created = New-RealCase -Headers $Headers -VersionId $versionId -Title $caseSpec.Title `
            -Actions $caseSpec.Actions -Expected $caseSpec.Expected `
            -AssertionTarget $caseSpec.Target -AssertionExpression $caseSpec.Expression -AssertionExpected $caseSpec.AssertionExpected `
            -RequirementId $requirementId
        $caseIds += [string]$created.CaseId
    }
    $submit = Invoke-Api -Method Post -Path "/api/versions/$versionId/mindmap/submit-review" -Headers $Headers -Body @{}
    if ($submit.Status -ne 200) { throw "POST submit-review returned HTTP $($submit.Status)" }
    $approve = Invoke-Api -Method Post -Path "/api/versions/$versionId/mindmap/review" -Headers $Headers -Body @{
        decision            = "approve"
        acknowledgeFindings = $true
    }
    if ($approve.Status -ne 200) { throw "POST approve returned HTTP $($approve.Status)" }
    if ([string]$approve.Data.version.status -ne "executing") { throw "approval left the version in '$($approve.Data.version.status)'" }
    if ($null -eq $approve.Data.baseline) { throw "approval did not freeze a baseline" }
    return [pscustomobject]@{
        VersionId    = $versionId
        CaseIds      = @($caseIds)
        BaselineHash = [string]$approve.Data.baseline.graphHash
        BaselineId   = [string]$approve.Data.baseline.id
    }
}

function New-RealWorld {
    param(
        [string]$Name,
        [string]$BaseUrl = "",
        [string[]]$AllowedHosts = @("127.0.0.1"),
        [switch]$AutoLogin,
        [string]$LoginUrl = "",
        [string]$Credential = "fixture-pass",
        [switch]$ImportSession,
        [string]$Kind = "three",
        [int]$ConcurrencyLimit = 1,
        [int]$SessionCount = 1
    )
    $stamp = Get-Date -Format "HHmmssfff"
    $system = Invoke-Api -Method Post -Path "/api/resources/systems" -Headers $adminHeaders -Body @{
        key     = "p6real$stamp"
        name    = "$Name system"
        kind    = "web"
        baseUrl = $fixtureBaseUrl
    }
    if ($system.Status -ne 201) { throw "POST system returned HTTP $($system.Status)" }
    if ([string]::IsNullOrWhiteSpace($BaseUrl)) { $BaseUrl = $fixtureBaseUrl }
    $environment = Invoke-Api -Method Post -Path "/api/resources/systems/$($system.Data.id)/environments" -Headers $adminHeaders -Body @{
        key          = "p6realenv$stamp"
        name         = "$Name environment"
        baseUrl      = $BaseUrl
        kind         = "dev"
        allowedHosts = @($AllowedHosts)
        loginCheck   = @{
            url          = "$fixtureBaseUrl/p6/home"
            expectStatus = @(200)
            bodyContains = "New order"
        }
    }
    if ($environment.Status -ne 201) { throw "POST environment returned HTTP $($environment.Status)" }
    $environmentId = $environment.Data.id
    if ($AutoLogin) {
        $patched = Invoke-Api -Method Patch -Path "/api/resources/environments/$environmentId" -Headers $adminHeaders -Body @{ allowAutoLogin = $true }
        if ($patched.Status -ne 200) { throw "PATCH allowAutoLogin returned HTTP $($patched.Status)" }
    }
    $flow = Invoke-Api -Method Post -Path "/api/resources/auth-flows" -Headers $adminHeaders -Body @{
        system_id  = [string]$system.Data.id
        name       = "$Name login"
        kind       = "form"
        definition = @{
            login_url         = $LoginUrl
            username_selector = "#username"
            password_selector = "#password"
            submit_selector   = "button[type=submit]"
            success           = @{ url_contains = "/p6/home" }
            failure           = @{ invalid_selector = ".login-error"; mfa_selector = "#mfa" }
            timeout_ms        = 10000
        }
        enabled    = $true
    }
    if ($flow.Status -ne 201) { throw "POST auth flow returned HTTP $($flow.Status)" }
    $account = Invoke-Api -Method Post -Path "/api/resources/accounts" -Headers $adminHeaders -Body @{
        key      = "p6realaccount$stamp"
        label    = "$Name account"
        username = "fixture"
        kind     = "human"
    }
    if ($account.Status -ne 201) { throw "POST account returned HTTP $($account.Status)" }
    $access = Invoke-Api -Method Post -Path "/api/resources/accounts/$($account.Data.id)/accesses" -Headers $adminHeaders -Body @{
        systemId         = [int64]$system.Data.id
        authType         = "password"
        credential       = $Credential
        usernameOverride = "fixture"
        concurrencyLimit = $ConcurrencyLimit
        loginUrl         = $LoginUrl
        login_flow_id    = [string]$flow.Data.id
        enabled          = $true
    }
    if ($access.Status -ne 201) { throw "POST access returned HTTP $($access.Status)" }
    $sessionIds = @()
    if ($ImportSession) {
        for ($sessionIndex = 1; $sessionIndex -le $SessionCount; $sessionIndex++) {
            $sessionIds += Import-FixtureSession -Headers $adminHeaders -AccessId $access.Data.id -EnvironmentId $environmentId
        }
    }
    $version = New-RealExecutingVersion -Headers $adminHeaders -Name "$Name version" `
        -SystemId $system.Data.id -EnvironmentId $environmentId -Kind $Kind
    return [pscustomobject]@{
        SystemId      = $system.Data.id
        EnvironmentId = $environmentId
        AuthFlowId    = [string]$flow.Data.id
        AccountId     = $account.Data.id
        AccessId      = $access.Data.id
        SessionId     = $(if ($sessionIds.Count -gt 0) { $sessionIds[0] } else { $null })
        SessionIds    = @($sessionIds)
        VersionId     = $version.VersionId
        CaseIds       = @($version.CaseIds)
        BaselineHash  = $version.BaselineHash
    }
}

function Get-Batch {
    param([string]$VersionId, [string]$BatchId)
    $response = Invoke-Api -Method Get -Path "/api/versions/$VersionId/batches/$BatchId" -Headers $adminHeaders
    if ($response.Status -ne 200) { throw "GET batch returned HTTP $($response.Status)" }
    return $response.Data
}

function Wait-Batch {
    param([string]$VersionId, [string]$BatchId, [int]$TimeoutSeconds = 1800, [switch]$AllowPaused)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        $batch = Get-Batch -VersionId $VersionId -BatchId $BatchId
        if ($AllowPaused -and [bool]$batch.paused) { return $batch }
        if (@("done", "failed", "cancelled") -contains [string]$batch.status) { return $batch }
        Start-Sleep -Seconds 1
    }
    throw "batch $BatchId did not finish within $TimeoutSeconds seconds"
}

function Get-CaseRuns {
    param([string]$VersionId, [string]$BatchId)
    $response = Invoke-Api -Method Get -Path "/api/versions/$VersionId/batches/$BatchId/case-runs?limit=100" -Headers $adminHeaders
    if ($response.Status -ne 200) { throw "GET case runs returned HTTP $($response.Status)" }
    return @($response.Data)
}

function Get-CaseRun {
    param([string]$VersionId, [string]$BatchId, [string]$CaseRunId)
    $response = Invoke-Api -Method Get -Path "/api/versions/$VersionId/batches/$BatchId/case-runs/$CaseRunId" -Headers $adminHeaders
    if ($response.Status -ne 200) { throw "GET case run returned HTTP $($response.Status)" }
    return $response.Data
}

function Wait-CaseRun {
    param([string]$VersionId, [string]$BatchId, [string]$CaseRunId, [string[]]$Until, [int]$TimeoutSeconds = 900)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        $run = Get-CaseRun -VersionId $VersionId -BatchId $BatchId -CaseRunId $CaseRunId
        if ($Until -contains [string]$run.status) { return $run }
        Start-Sleep -Milliseconds 500
    }
    throw "case run $CaseRunId did not reach $($Until -join '/') within $TimeoutSeconds seconds"
}

function Add-CheckResult {
    param([string]$Name, [string]$Status, [string]$Detail)
    [void]$script:checks.Add([pscustomobject]@{ name = $Name; status = $Status; detail = $Detail })
    $color = "Green"
    if ($Status -eq "skipped") { $color = "Yellow" }
    if ($Status -eq "failed") { $color = "Red" }
    Write-Host ("{0} {1}: {2}" -f $Status.ToUpperInvariant(), $Name, $Detail) -ForegroundColor $color
}

function Invoke-Check {
    param([string]$Name, [scriptblock]$Body)
    Write-Step $Name
    try {
        & $Body
        Add-CheckResult -Name $Name -Status "passed" -Detail "ok"
    } catch {
        Add-CheckResult -Name $Name -Status "failed" -Detail (Get-RedactedText -Text $_.Exception.Message)
    }
}

function Write-AcceptanceResult {
    $script:resultData.checks = @($script:checks)
    $json = ConvertTo-Json -InputObject $script:resultData -Depth 10
    [System.IO.File]::WriteAllText($resultPath, $json + "`n", (New-Object System.Text.UTF8Encoding($false)))
}

function Invoke-DryRun {
    Write-Step "DryRun: validating paths and environment (no database reset, no API, no LLM call)"
    $problems = @()
    foreach ($relative in @("backend", "scripts\fixture-target.cjs", "executor\src\server.mjs", "docs\openapi\api.yaml")) {
        if (-not (Test-Path -LiteralPath (Join-Path $repoRoot $relative))) { $problems += "missing $relative" }
    }
    if (-not (Test-Path -LiteralPath $envPath -PathType Leaf)) {
        $problems += "missing .env"
    } else {
        $values = Read-DotEnv -Path $envPath
        foreach ($key in $requiredEnvKeys) {
            if (-not $values.ContainsKey($key) -or [string]::IsNullOrWhiteSpace([string]$values[$key])) {
                $problems += "missing .env key $key"
            }
        }
        if ($values.ContainsKey("EZTEST_DATABASE_URL") -and -not [string]::IsNullOrWhiteSpace([string]$values["EZTEST_DATABASE_URL"])) {
            try {
                $uri = Assert-SafeDatabaseUrl -DatabaseUrl ([string]$values["EZTEST_DATABASE_URL"])
                # Never print the URL: it carries credentials. Only the effective
                # host/port/database target is useful for the preflight.
                Write-Host ("DryRun: the API would target database '{0}' on {1}:{2}" -f `
                        $dbName, $uri.Host, $(if ($uri.Port -gt 0) { $uri.Port } else { 5432 }))
            } catch {
                $problems += (Get-RedactedText -Text $_.Exception.Message)
            }
        }
    }
    foreach ($tool in @(
            @{ Name = "go"; Local = (Join-Path $repoRoot ".tools\go\bin\go.exe") },
            @{ Name = "psql"; Local = (Join-Path $repoRoot ".tools\pgsql\bin\psql.exe") },
            @{ Name = "node"; Local = "" }
        )) {
        if ([string]::IsNullOrWhiteSpace((Resolve-ToolPath -LocalPath $tool.Local -CommandName $tool.Name))) {
            $problems += "$($tool.Name) executable not found (.tools or PATH)"
        }
    }
    if ($env:EZTEST_RUN_REAL_LLM -ne "1") {
        Write-Host "DryRun: gate is closed (EZTEST_RUN_REAL_LLM != 1); the real run would print SKIPPED and exit 0."
    }
    if ($problems.Count -gt 0) {
        foreach ($problem in $problems) { Write-Host "DryRun problem: $problem" -ForegroundColor Red }
        return 1
    }
    Write-Host "DryRun OK: paths, .env keys, database URL shape and toolchain are ready." -ForegroundColor Green
    return 0
}

if ($DryRun) {
    exit (Invoke-DryRun)
}

if ($env:EZTEST_RUN_REAL_LLM -ne "1") {
    Write-Host "SKIPPED: real P6 execution LLM acceptance is disabled (set EZTEST_RUN_REAL_LLM=1 to run it)."
    exit 0
}

$exitCode = 0
$apiProcess = $null
$fixtureProcess = $null
$executorProcess = $null
$apiExe = ""
$goExe = ""
$psqlExe = ""
$nodeExe = ""
$adminHeaders = $null
$startedAt = [DateTime]::UtcNow

try {
    New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null

    Write-Step "Gate: EZTEST_RUN_REAL_LLM=1 and required .env keys"
    if (-not (Test-Path -LiteralPath $envPath -PathType Leaf)) { throw "Missing .env at $envPath" }
    $envValues = Read-DotEnv -Path $envPath
    $missingKeys = @()
    foreach ($key in $requiredEnvKeys) {
        if (-not $envValues.ContainsKey($key) -or [string]::IsNullOrWhiteSpace([string]$envValues[$key])) { $missingKeys += $key }
    }
    if ($missingKeys.Count -gt 0) {
        throw ("EZTEST_RUN_REAL_LLM=1 but .env is missing required key(s): " + ($missingKeys -join ", ") + ". The real-LLM run fails closed instead of skipping.")
    }
    $adminUsername = Get-RequiredValue -Values $envValues -Key "EZTEST_ADMIN_USERNAME"
    $adminPassword = Get-RequiredValue -Values $envValues -Key "EZTEST_ADMIN_PASSWORD"
    $llmKey = Get-RequiredValue -Values $envValues -Key "llm_key"
    $llmBaseUrl = Get-RequiredValue -Values $envValues -Key "llm_base_url"
    $llmModel = Get-RequiredValue -Values $envValues -Key "llm_model_name"
    $databaseUrl = Get-RequiredValue -Values $envValues -Key "EZTEST_DATABASE_URL"
    $script:secretValues = @($adminPassword, $llmKey, $databaseUrl)
    $script:resultData.llmModel = $llmModel
    if ($envValues.ContainsKey("EZTEST_SECRET_KEYS")) {
        $rawSecretKeys = [string]$envValues["EZTEST_SECRET_KEYS"]
        if (-not [string]::IsNullOrWhiteSpace($rawSecretKeys)) {
            $script:secretValues += $rawSecretKeys
            try {
                $secretObject = $rawSecretKeys | ConvertFrom-Json
                foreach ($property in $secretObject.PSObject.Properties) { $script:secretValues += [string]$property.Value }
            } catch {
                # A malformed keyring is reported by the API at startup.
            }
        }
    }
    Add-CheckResult -Name "gate and .env keys" -Status "passed" -Detail "EZTEST_RUN_REAL_LLM=1; llm_base_url/llm_key/llm_model_name present"

    Write-Step "Validating the database URL before any destructive statement"
    $databaseUri = Assert-SafeDatabaseUrl -DatabaseUrl $databaseUrl
    $userInfo = $databaseUri.UserInfo
    $separator = $userInfo.IndexOf(":")
    if ($separator -lt 0) { throw "EZTEST_DATABASE_URL must contain a user and password" }
    $dbUser = [System.Uri]::UnescapeDataString($userInfo.Substring(0, $separator))
    $dbPassword = [System.Uri]::UnescapeDataString($userInfo.Substring($separator + 1))
    $dbHost = $databaseUri.Host
    $dbPort = if ($databaseUri.Port -gt 0) { $databaseUri.Port } else { 5432 }
    $script:secretValues += $dbPassword
    $targetDatabaseUrl = New-DatabaseUrl -DatabaseUri $databaseUri -DatabaseName $dbName

    $goExe = Resolve-ToolPath -LocalPath (Join-Path $repoRoot ".tools\go\bin\go.exe") -CommandName "go"
    $psqlExe = Resolve-ToolPath -LocalPath (Join-Path $repoRoot ".tools\pgsql\bin\psql.exe") -CommandName "psql"
    $nodeExe = Resolve-ToolPath -LocalPath "" -CommandName "node"
    if ([string]::IsNullOrWhiteSpace($goExe)) { throw "go executable not found (.tools or PATH)" }
    if ([string]::IsNullOrWhiteSpace($psqlExe)) { throw "psql executable not found (.tools or PATH)" }
    if ([string]::IsNullOrWhiteSpace($nodeExe)) { throw "node executable not found on PATH" }

    Assert-PortFree -Port $apiPort
    Write-Step "Resetting $dbName and building the API"
    $env:PGPASSWORD = $dbPassword
    try {
        $exists = Get-PsqlScalar -Database "postgres" -Sql "SELECT count(*) FROM pg_database WHERE datname = '$dbName';"
        if ($exists -ne "0") {
            Invoke-PsqlQuiet -Database "postgres" -Sql "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$dbName' AND pid <> pg_backend_pid();" -Operation "terminating $dbName connections"
            Invoke-PsqlQuiet -Database "postgres" -Sql "DROP DATABASE $dbName;" -Operation "dropping $dbName"
        }
        Invoke-PsqlQuiet -Database "postgres" -Sql "CREATE DATABASE $dbName;" -Operation "creating $dbName"
    } finally {
        $env:PGPASSWORD = $dbPassword
    }

    $apiExe = Join-Path $tmpDir "acceptance-p6-real-llm-api.exe"
    $buildLog = Join-Path $tmpDir "acceptance-p6-real-llm-build.log"
    Push-Location $backendDir
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        & $goExe build -o $apiExe ./cmd/eztest *> $buildLog
        $buildExit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
        Pop-Location
    }
    if ($buildExit -ne 0) { throw "go build ./cmd/eztest failed (see .tmp/acceptance-p6-real-llm-build.log)" }

    Write-Step "Starting the fixture target and the Playwright executor"
    Assert-PortFree -Port $fixturePort
    $env:FIXTURE_PORT = "$fixturePort"
    $fixtureOut = Join-Path $tmpDir "acceptance-p6-real-fixture.out.log"
    $fixtureErr = Join-Path $tmpDir "acceptance-p6-real-fixture.err.log"
    Remove-Item -LiteralPath $fixtureOut, $fixtureErr -ErrorAction SilentlyContinue
    $fixtureProcess = Start-Process -FilePath $nodeExe -ArgumentList "`"scripts/fixture-target.cjs`"" `
        -WorkingDirectory $repoRoot -RedirectStandardOutput $fixtureOut -RedirectStandardError $fixtureErr -WindowStyle Hidden -PassThru
    Wait-ForHttp -Url "$fixtureBaseUrl/health" -TimeoutSeconds 30 -Process $fixtureProcess -Label "P6 fixture target"

    Assert-PortFree -Port $executorPort
    $env:EZTEST_EXECUTOR_ADDR = "127.0.0.1:$executorPort"
    $env:EZTEST_EXECUTOR_TOKEN = $executorToken
    $env:EZTEST_EXECUTOR_MAX_CONTEXTS = "4"
    $executorOut = Join-Path $tmpDir "acceptance-p6-real-executor.out.log"
    $executorErr = Join-Path $tmpDir "acceptance-p6-real-executor.err.log"
    Remove-Item -LiteralPath $executorOut, $executorErr -ErrorAction SilentlyContinue
    $executorProcess = Start-Process -FilePath $nodeExe -ArgumentList "`"executor/src/server.mjs`"" `
        -WorkingDirectory $repoRoot -RedirectStandardOutput $executorOut -RedirectStandardError $executorErr -WindowStyle Hidden -PassThru
    $executorHealthy = $false
    $deadline = (Get-Date).AddSeconds(60)
    while ((Get-Date) -lt $deadline) {
        try {
            $health = Invoke-WebRequest -Uri "http://127.0.0.1:$executorPort/healthz" -UseBasicParsing -TimeoutSec 10
            if ([int]$health.StatusCode -eq 200) { $executorHealthy = $true; break }
        } catch {
            $executorProcess.Refresh()
            if ($executorProcess.HasExited) { break }
        }
        Start-Sleep -Milliseconds 500
    }
    if (-not $executorHealthy) { throw "the Playwright executor did not become healthy on :$executorPort (see .tmp/acceptance-p6-real-executor.err.log)" }

    Write-Step "Starting the API on :$apiPort with the real LLM configuration"
    $evidenceDir = Join-Path $tmpDir "p6-real-evidence"
    if (Test-Path -LiteralPath $evidenceDir) { Remove-Item -LiteralPath $evidenceDir -Recurse -Force }
    New-Item -ItemType Directory -Force -Path $evidenceDir | Out-Null
    $env:EZTEST_ENV = "dev"
    $env:EZTEST_DATABASE_URL = $targetDatabaseUrl
    $env:EZTEST_HTTP_ADDR = ":$apiPort"
    $env:EZTEST_WEB_ORIGIN = "http://localhost:6868"
    $env:EZTEST_ENABLE_TEST_ENDPOINTS = "true"
    $env:EZTEST_STORAGE_DIR = (Join-Path $tmpDir "p6-real-objects")
    $env:EZTEST_EXECUTOR_URL = "http://127.0.0.1:$executorPort"
    $env:EZTEST_EXECUTOR_TOKEN = $executorToken
    $env:EZTEST_EVIDENCE_DIR = $evidenceDir
    $env:EZTEST_EXEC_WORKER_INTERVAL_MS = "200"
    # One case at a time: the imported fixture session has one lease slot, so
    # this keeps the real-LLM acceptance deterministic and serial.
    $env:EZTEST_EXEC_MAX_CONCURRENT_CASES = "1"
    $env:EZTEST_EXEC_CASE_TIMEOUT_MS = "300000"
    $env:EZTEST_OPENAPI_PATH = (Join-Path $repoRoot "docs\openapi\api.yaml")
    $env:EZTEST_ADMIN_USERNAME = $adminUsername
    $env:EZTEST_ADMIN_PASSWORD = $adminPassword
    foreach ($key in @("EZTEST_SECRET_KEYS", "EZTEST_SECRET_KEY_ACTIVE", "llm_base_url", "llm_key", "llm_model_name")) {
        if ($envValues.ContainsKey($key)) { Set-Item -Path ("Env:" + $key) -Value $envValues[$key] }
    }
    Remove-Item Env:EZTEST_TEST_DATABASE_URL -ErrorAction SilentlyContinue
    $apiOut = Join-Path $tmpDir "acceptance-p6-real-api.out.log"
    $apiErr = Join-Path $tmpDir "acceptance-p6-real-api.err.log"
    Remove-Item -LiteralPath $apiOut, $apiErr -ErrorAction SilentlyContinue
    $apiProcess = Start-Process -FilePath $apiExe -WorkingDirectory $backendDir `
        -RedirectStandardOutput $apiOut -RedirectStandardError $apiErr -WindowStyle Hidden -PassThru
    try {
        Wait-ForHttp -Url "$apiBaseUrl/api/health" -TimeoutSeconds 120 -Process $apiProcess -Label "API"
    } catch {
        Stop-ProcessTree -Process $apiProcess
        $apiProcess = $null
        throw
    }

    Write-Step "Checking migrations, health and the administrator login"
    $health = Invoke-Api -Method Get -Path "/api/health"
    if ([string]$health.Data.status -ne "ok" -or [string]$health.Data.db -ne "ok") { throw "API health is not ok" }
    if (-not [bool]$health.Data.llm_configured) { throw "health reports llm_configured=false; refusing to claim real-LLM acceptance (check llm_* in .env)" }
    $tables = [int](Get-PsqlScalar -Database $dbName -Sql "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name IN ('run_batch','case_run','step_result','assertion_result','evidence','evidence_ref');")
    if ($tables -ne 6) { throw "the $dbName database has $tables/6 execution tables after boot" }
    $adminToken = Get-Token -Username $adminUsername -Password $adminPassword
    $adminHeaders = @{ Authorization = "Bearer $adminToken" }
    Add-CheckResult -Name "database reset, migrations, health and admin login" -Status "passed" `
        -Detail "eztest_p6_llm rebuilt; 6 execution tables; llm_configured=true; admin token obtained"

    Write-Step "Seeding the fixture world (system/environment/auth flow/account/session/version)"
    $main = New-RealWorld -Name "P6 real" -AutoLogin -LoginUrl "$fixtureBaseUrl/p6/login" -ImportSession -Kind "three"
    $script:resultData.versionId = $main.VersionId
    $script:resultData.caseCount = @($main.CaseIds).Count
    Add-CheckResult -Name "seed fixture world and approved baseline" -Status "passed" `
        -Detail ("version {0}, {1} cases, imported session {2}, executing with a frozen baseline" -f $main.VersionId, @($main.CaseIds).Count, $main.SessionId)

    Invoke-Check -Name "real LLM executes the three baseline cases" -Body {
        $created = Invoke-Api -Method Post -Path "/api/versions/$($main.VersionId)/batches" -Headers $adminHeaders -Body @{
            caseIds = @($main.CaseIds)
        }
        if ($created.Status -ne 201) { throw "POST batch returned HTTP $($created.Status)" }
        $batchId = [string]$created.Data.id
        $script:resultData.batchId = $batchId
        $done = Wait-Batch -VersionId $main.VersionId -BatchId $batchId -TimeoutSeconds 1800
        if ([string]$done.status -ne "done") { throw "the real-LLM batch ended '$($done.status)' reason='$($done.failure_reason)'" }
        if ([int]$done.counts.passed -ne 3 -or [int]$done.counts.failed -ne 0 -or [int]$done.counts.blocked -ne 0) {
            throw "real-LLM counts are passed=$($done.counts.passed) failed=$($done.counts.failed) blocked=$($done.counts.blocked), expected 3 passed"
        }
        $runs = @(Get-CaseRuns -VersionId $main.VersionId -BatchId $batchId)
        if ($runs.Count -ne 3) { throw "batch has $($runs.Count) case runs, expected 3" }
        $evidenceCount = 0
        $stepCount = 0
        $durations = @()
        foreach ($run in $runs) {
            if ([string]$run.status -ne "passed") { throw "case run $($run.id) is '$($run.status)', expected passed" }
            $detail = Get-CaseRun -VersionId $main.VersionId -BatchId $batchId -CaseRunId ([string]$run.id)
            if (@($detail.steps).Count -lt 1) { throw "case run $($run.id) recorded no steps" }
            if (@($detail.assertions).Count -lt 1) { throw "case run $($run.id) recorded no assertions" }
            $stepCount += @($detail.steps).Count
            $evidenceCount += @($detail.evidence | Where-Object { [string]$_.kind -eq "screenshot" }).Count
            $durations += [pscustomobject]@{ case = [string]$run.id; duration_ms = [int64]$run.duration_ms }
        }
        if ($evidenceCount -lt 3) { throw "the three passed cases produced $evidenceCount screenshot artifacts, expected at least 3" }
        $script:resultData.passedCount = 3
        $script:resultData.caseDurations = @($durations)
        Add-CheckResult -Name "real execution evidence and steps" -Status "passed" `
            -Detail ("3 cases passed, {0} steps/assertions, {1} screenshots" -f $stepCount, $evidenceCount)
    }

    Invoke-Check -Name "failure classification is application with evidence" -Body {
        $failWorld = New-RealWorld -Name "P6 real fail" -AutoLogin -LoginUrl "$fixtureBaseUrl/p6/login" -ImportSession -Kind "fail"
        $created = Invoke-Api -Method Post -Path "/api/versions/$($failWorld.VersionId)/batches" -Headers $adminHeaders -Body @{}
        if ($created.Status -ne 201) { throw "POST fail batch returned HTTP $($created.Status)" }
        $batchId = [string]$created.Data.id
        $done = Wait-Batch -VersionId $failWorld.VersionId -BatchId $batchId -TimeoutSeconds 1200
        $runs = @(Get-CaseRuns -VersionId $failWorld.VersionId -BatchId $batchId)
        $run = Get-CaseRun -VersionId $failWorld.VersionId -BatchId $batchId -CaseRunId ([string]$runs[0].id)
        if ([string]$run.status -ne "failed") { throw "the impossible assertion case is '$($run.status)', expected failed" }
        if ([string]$run.failure_class -ne "application") { throw "the impossible assertion class is '$($run.failure_class)', expected application" }
        $failedAssertions = @($run.assertions | Where-Object { [string]$_.verdict -eq "failed" })
        if ($failedAssertions.Count -lt 1) { throw "the application failure has no failed assertion" }
        foreach ($assertion in $failedAssertions) {
            if (@($assertion.evidence_ids).Count -lt 1) { throw "the failed assertion carries no evidence" }
        }
        $script:resultData.failedClass = [string]$run.failure_class
    }

    Invoke-Check -Name "environment failure is blocked(environment)" -Body {
        $blockedWorld = New-RealWorld -Name "P6 real blocked env" -BaseUrl "http://127.0.0.2:13335" `
            -AllowedHosts @("example.com") -AutoLogin -LoginUrl "$fixtureBaseUrl/p6/login" -ImportSession -Kind "three"
        $created = Invoke-Api -Method Post -Path "/api/versions/$($blockedWorld.VersionId)/batches" -Headers $adminHeaders -Body @{ caseIds = @($blockedWorld.CaseIds[0]) }
        if ($created.Status -ne 201) { throw "POST blocked-env batch returned HTTP $($created.Status)" }
        $batchId = [string]$created.Data.id
        $done = Wait-Batch -VersionId $blockedWorld.VersionId -BatchId $batchId -TimeoutSeconds 1200
        $runs = @(Get-CaseRuns -VersionId $blockedWorld.VersionId -BatchId $batchId)
        $run = Get-CaseRun -VersionId $blockedWorld.VersionId -BatchId $batchId -CaseRunId ([string]$runs[0].id)
        if ([string]$run.status -ne "blocked" -or [string]$run.failure_class -ne "environment") {
            throw "the unreachable environment case is '$($run.status)'/'$($run.failure_class)', expected blocked/environment"
        }
        $script:resultData.blockedClasses += [string]$run.failure_class
    }

    Invoke-Check -Name "session failure is blocked(session)" -Body {
        $sessionWorld = New-RealWorld -Name "P6 real bad session" -AutoLogin -LoginUrl "$fixtureBaseUrl/p6/login" `
            -Credential "wrong-fixture-password" -Kind "three"
        $created = Invoke-Api -Method Post -Path "/api/versions/$($sessionWorld.VersionId)/batches" -Headers $adminHeaders -Body @{ caseIds = @($sessionWorld.CaseIds[0]) }
        if ($created.Status -ne 201) { throw "POST session batch returned HTTP $($created.Status)" }
        $batchId = [string]$created.Data.id
        $done = Wait-Batch -VersionId $sessionWorld.VersionId -BatchId $batchId -TimeoutSeconds 600
        $runs = @(Get-CaseRuns -VersionId $sessionWorld.VersionId -BatchId $batchId)
        $run = Get-CaseRun -VersionId $sessionWorld.VersionId -BatchId $batchId -CaseRunId ([string]$runs[0].id)
        if ([string]$run.status -ne "blocked" -or [string]$run.failure_class -ne "session") {
            throw "the wrong-credential case is '$($run.status)'/'$($run.failure_class)', expected blocked/session"
        }
        $script:resultData.blockedClasses += [string]$run.failure_class
    }

    Invoke-Check -Name "graceful pause and checkpoint resume" -Body {
        $pauseWorld = New-RealWorld -Name "P6 real graceful" -AutoLogin -LoginUrl "$fixtureBaseUrl/p6/login" -ImportSession -Kind "slow"
        $created = Invoke-Api -Method Post -Path "/api/versions/$($pauseWorld.VersionId)/batches" -Headers $adminHeaders -Body @{}
        if ($created.Status -ne 201) { throw "POST graceful batch returned HTTP $($created.Status)" }
        $batchId = [string]$created.Data.id
        $runId = $null
        $deadline = (Get-Date).AddSeconds(120)
        while ((Get-Date) -lt $deadline) {
            $runs = @(Get-CaseRuns -VersionId $pauseWorld.VersionId -BatchId $batchId)
            if ($runs.Count -ge 1 -and [string]$runs[0].status -eq "running") { $runId = [string]$runs[0].id; break }
            Start-Sleep -Milliseconds 300
        }
        if ($null -eq $runId) { throw "the graceful-pause case never started" }
        $pause = Invoke-Api -Method Post -Path "/api/versions/$($pauseWorld.VersionId)/batches/$batchId/pause" -Headers $adminHeaders -Body @{ mode = "graceful" }
        if ($pause.Status -ne 200) { throw "POST graceful pause returned HTTP $($pause.Status)" }
        $paused = Wait-Batch -VersionId $pauseWorld.VersionId -BatchId $batchId -AllowPaused -TimeoutSeconds 300
        if (-not [bool]$paused.paused) { throw "the batch is not paused" }
        $parked = Wait-CaseRun -VersionId $pauseWorld.VersionId -BatchId $batchId -CaseRunId $runId -Until @("pending", "interrupted", "passed", "failed") -TimeoutSeconds 300
        if ([string]$parked.status -eq "interrupted") { throw "graceful pause interrupted a case" }
        $completedBefore = @($parked.checkpoint.completed_seqs)
        $resume = Invoke-Api -Method Post -Path "/api/versions/$($pauseWorld.VersionId)/batches/$batchId/resume" -Headers $adminHeaders -Body @{}
        if ($resume.Status -ne 200) { throw "POST graceful resume returned HTTP $($resume.Status)" }
        $done = Wait-Batch -VersionId $pauseWorld.VersionId -BatchId $batchId -TimeoutSeconds 1800
        if ([string]$done.status -ne "done") { throw "the gracefully resumed batch ended '$($done.status)'" }
        $final = Get-CaseRun -VersionId $pauseWorld.VersionId -BatchId $batchId -CaseRunId $runId
        if ([string]$final.status -ne "passed") { throw "the gracefully resumed case finished '$($final.status)'" }
        $seqs = @($final.steps | ForEach-Object { [int]$_.seq })
        if (@($seqs | Select-Object -Unique).Count -ne $seqs.Count) { throw "the resumed case re-recorded a step sequence" }
        $script:resultData.pauseModes += ("graceful;completed_before=" + ($completedBefore -join ","))
    }

    Invoke-Check -Name "immediate pause interrupts and resume cleans up" -Body {
        $pauseWorld = New-RealWorld -Name "P6 real immediate" -AutoLogin -LoginUrl "$fixtureBaseUrl/p6/login" -ImportSession -Kind "slow"
        $created = Invoke-Api -Method Post -Path "/api/versions/$($pauseWorld.VersionId)/batches" -Headers $adminHeaders -Body @{}
        if ($created.Status -ne 201) { throw "POST immediate batch returned HTTP $($created.Status)" }
        $batchId = [string]$created.Data.id
        $runId = $null
        $deadline = (Get-Date).AddSeconds(120)
        while ((Get-Date) -lt $deadline) {
            $runs = @(Get-CaseRuns -VersionId $pauseWorld.VersionId -BatchId $batchId)
            if ($runs.Count -ge 1 -and [string]$runs[0].status -eq "running") { $runId = [string]$runs[0].id; break }
            Start-Sleep -Milliseconds 300
        }
        if ($null -eq $runId) { throw "the immediate-pause case never started" }
        Start-Sleep -Milliseconds 800
        $pause = Invoke-Api -Method Post -Path "/api/versions/$($pauseWorld.VersionId)/batches/$batchId/pause" -Headers $adminHeaders -Body @{ mode = "immediate" }
        if ($pause.Status -ne 200) { throw "POST immediate pause returned HTTP $($pause.Status)" }
        $interrupted = Wait-CaseRun -VersionId $pauseWorld.VersionId -BatchId $batchId -CaseRunId $runId -Until @("interrupted") -TimeoutSeconds 120
        if (-not [bool]$interrupted.checkpoint.cleanup_required) { throw "the immediate pause did not request cleanup" }
        $resume = Invoke-Api -Method Post -Path "/api/versions/$($pauseWorld.VersionId)/batches/$batchId/resume" -Headers $adminHeaders -Body @{}
        if ($resume.Status -ne 200) { throw "POST immediate resume returned HTTP $($resume.Status)" }
        $done = Wait-Batch -VersionId $pauseWorld.VersionId -BatchId $batchId -TimeoutSeconds 1800
        if ([string]$done.status -ne "done") { throw "the interrupted batch ended '$($done.status)'" }
        $final = Get-CaseRun -VersionId $pauseWorld.VersionId -BatchId $batchId -CaseRunId $runId
        if ([string]$final.status -ne "passed") { throw "the interrupted case finished '$($final.status)' after resume" }
        $script:resultData.pauseModes += "immediate"
    }

    Invoke-Check -Name "API restart recovers interrupted state and releases leases" -Body {
        $restartWorld = New-RealWorld -Name "P6 real restart" -AutoLogin -LoginUrl "$fixtureBaseUrl/p6/login" -ImportSession -Kind "slow"
        $created = Invoke-Api -Method Post -Path "/api/versions/$($restartWorld.VersionId)/batches" -Headers $adminHeaders -Body @{}
        if ($created.Status -ne 201) { throw "POST restart batch returned HTTP $($created.Status)" }
        $batchId = [string]$created.Data.id
        $runId = $null
        $deadline = (Get-Date).AddSeconds(120)
        while ((Get-Date) -lt $deadline) {
            $runs = @(Get-CaseRuns -VersionId $restartWorld.VersionId -BatchId $batchId)
            if ($runs.Count -ge 1 -and [string]$runs[0].status -eq "running") { $runId = [string]$runs[0].id; break }
            Start-Sleep -Milliseconds 300
        }
        if ($null -eq $runId) { throw "the restart case never started" }
        Start-Sleep -Milliseconds 800
        Stop-ProcessTree -Process $apiProcess
        # Invoke-Check runs the body in a child scope; use the script scope so
        # the finally block stops the restarted API instead of leaving an
        # orphan on :13333.
        $script:apiProcess = $null
        Wait-PortFree -Port $apiPort
        $script:apiProcess = Start-Process -FilePath $apiExe -WorkingDirectory $backendDir `
            -RedirectStandardOutput $apiOut -RedirectStandardError $apiErr -WindowStyle Hidden -PassThru
        Wait-ForHttp -Url "$apiBaseUrl/api/health" -TimeoutSeconds 120 -Process $apiProcess -Label "API"
        $paused = Wait-Batch -VersionId $restartWorld.VersionId -BatchId $batchId -AllowPaused -TimeoutSeconds 300
        if ([string]$paused.pause_reason -notmatch 'restart') { throw "restart pause_reason is '$($paused.pause_reason)'" }
        $recovered = Get-CaseRun -VersionId $restartWorld.VersionId -BatchId $batchId -CaseRunId $runId
        if ([string]$recovered.status -ne "interrupted") { throw "the recovered case is '$($recovered.status)', expected interrupted" }
        if (-not [bool]$recovered.checkpoint.cleanup_required) { throw "the recovered case does not request cleanup" }
        $leaseDeadline = (Get-Date).AddSeconds(30)
        $openLeases = -1
        while ((Get-Date) -lt $leaseDeadline) {
            $openLeases = [int](Get-PsqlScalar -Database $dbName -Sql "SELECT count(*) FROM session_lease WHERE released_at IS NULL AND holder LIKE 'execution:case_run:%';")
            if ($openLeases -eq 0) { break }
            Start-Sleep -Milliseconds 250
        }
        if ($openLeases -ne 0) { throw "restart recovery left $openLeases open case-run leases" }
        $resume = Invoke-Api -Method Post -Path "/api/versions/$($restartWorld.VersionId)/batches/$batchId/resume" -Headers $adminHeaders -Body @{}
        if ($resume.Status -ne 200) { throw "POST restart resume returned HTTP $($resume.Status)" }
        $done = Wait-Batch -VersionId $restartWorld.VersionId -BatchId $batchId -TimeoutSeconds 1800
        if ([string]$done.status -ne "done") { throw "the restarted batch ended '$($done.status)'" }
        $script:resultData.restartReason = [string]$paused.pause_reason
    }

    Invoke-Check -Name "Batch 1 report JSON and Markdown export" -Body {
        $batchId = [string]$script:resultData.batchId
        $report = Invoke-Api -Method Get -Path "/api/versions/$($main.VersionId)/batches/$batchId/report" -Headers $adminHeaders
        if ($report.Status -ne 200) { throw "GET report returned HTTP $($report.Status)" }
        if ([int]$report.Data.summary.case_count -ne 3) { throw "report case_count is $($report.Data.summary.case_count), expected 3" }
        if ([int]$report.Data.summary.by_status.passed -ne 3) { throw "report by_status.passed is $($report.Data.summary.by_status.passed), expected 3" }
        $export = Invoke-Download -Path "/api/versions/$($main.VersionId)/batches/$batchId/report/export?format=markdown" -Headers $adminHeaders
        if ([string]::IsNullOrWhiteSpace($export.HeaderSha256)) { throw "the Markdown export has no X-Content-SHA256 header" }
        if ($export.HeaderSha256 -ne $export.Sha256) { throw "the Markdown export sha256 header does not match the bytes" }
        if ($export.ContentDisposition -notmatch 'attachment') { throw "the Markdown export is missing Content-Disposition" }
        $markdown = [System.Text.Encoding]::UTF8.GetString($export.Bytes)
        if ($markdown -notmatch 'passed') { throw "the Markdown export has no passed count" }
        if ($markdown -notmatch 'P6 home and orders list') { throw "the Markdown export has no case row" }
        $script:resultData.reportSha256 = $export.Sha256
        # Persist the verified report artifacts under .tmp/p6-real-llm/ so the
        # acceptance evidence bundle carries the exact report file and hash.
        $artifactDir = Join-Path $tmpDir "p6-real-llm"
        New-Item -ItemType Directory -Force -Path $artifactDir | Out-Null
        [System.IO.File]::WriteAllBytes((Join-Path $artifactDir "batch1-report.md"), $export.Bytes)
        [System.IO.File]::WriteAllText(
            (Join-Path $artifactDir "batch1-report.json"),
            ((ConvertTo-Json -InputObject $report.Raw -Depth 20) + "`n"),
            (New-Object System.Text.UTF8Encoding($false)))
    }

    Invoke-Check -Name "version events have strictly increasing seq" -Body {
        # The snapshot endpoint returns the newest page and caps limit at 100
        # (platform.MaxLimit); a long agent transcript pushes batch.created out
        # of a single page. Page backwards with before_seq so the check sees the
        # complete version history instead of a truncated window.
        $items = @()
        $beforeSeq = 0
        $pages = 0
        while ($true) {
            $path = "/api/versions/$($main.VersionId)/events?limit=100"
            if ($beforeSeq -gt 0) { $path += "&before_seq=$beforeSeq" }
            $events = Invoke-Api -Method Get -Path $path -Headers $adminHeaders
            if ($events.Status -ne 200) { throw "GET events returned HTTP $($events.Status)" }
            $page = @($events.Data)
            if ($page.Count -eq 0) { break }
            $items = @($page) + $items
            $beforeSeq = [int64]$page[0].seq
            $pages++
            if ($pages -gt 100) { throw "the event snapshot pagination did not terminate" }
            if (-not [bool]$events.Raw.has_more) { break }
        }
        if ($items.Count -lt 1) { throw "the version event snapshot is empty" }
        $previous = -1
        foreach ($item in $items) {
            if ([int64]$item.seq -le $previous) { throw "event seq is not strictly increasing" }
            $previous = [int64]$item.seq
        }
        $types = @($items | ForEach-Object { [string]$_.type })
        foreach ($required in @("batch.created", "batch.started", "run.started", "run.finished", "batch.finished")) {
            if ($types -notcontains $required) { throw "the event snapshot is missing $required" }
        }
        $script:resultData.eventCount = $items.Count
    }

    Invoke-Check -Name "normal user permissions and admin-only auth flows" -Body {
        $stamp = Get-Date -Format "HHmmss"
        $userName = "p6realuser$stamp"
        $userPassword = "P6Real-$stamp!"
        $createdUser = Invoke-Api -Method Post -Path "/api/users" -Headers $adminHeaders -Body @{ username = $userName; password = $userPassword }
        if ($createdUser.Status -ne 201) { throw "POST normal user returned HTTP $($createdUser.Status)" }
        $token = Get-Token -Username $userName -Password $userPassword
        $userHeaders = @{ Authorization = "Bearer $token" }
        $version = New-RealExecutingVersion -Headers $adminHeaders -Name "P6 real permissions" -SystemId $main.SystemId -EnvironmentId $main.EnvironmentId -Kind "three"
        $created = Invoke-Api -Method Post -Path "/api/versions/$($version.VersionId)/batches" -Headers $userHeaders -Body @{ caseIds = @($version.CaseIds[0]) }
        if ($created.Status -ne 201) { throw "normal user POST batch returned HTTP $($created.Status)" }
        $batchId = [string]$created.Data.id
        $pause = Invoke-Api -Method Post -Path "/api/versions/$($version.VersionId)/batches/$batchId/pause" -Headers $userHeaders -Body @{ mode = "graceful" }
        if ($pause.Status -ne 200) { throw "normal user pause returned HTTP $($pause.Status)" }
        $cancel = Invoke-Api -Method Post -Path "/api/versions/$($version.VersionId)/batches/$batchId/cancel" -Headers $userHeaders -Body @{}
        if ($cancel.Status -ne 200) { throw "normal user cancel returned HTTP $($cancel.Status)" }
        Assert-ApiError -Label "normal user lists auth flows" -Call {
            Invoke-Api -Method Get -Path "/api/resources/auth-flows" -Headers $userHeaders
        } -Status 403 | Out-Null
        Assert-ApiError -Label "normal user creates an auth flow" -Call {
            Invoke-Api -Method Post -Path "/api/resources/auth-flows" -Headers $userHeaders -Body @{
                system_id = [string]$main.SystemId; name = "forbidden"; kind = "form"; definition = @{}
            }
        } -Status 403 | Out-Null
    }

    if ($ConcurrentCases -ge 2) {
        Invoke-Check -Name "$ConcurrentCases-concurrency fixture batch" -Body {
            $world = New-RealWorld -Name "P6 real concurrency" -AutoLogin `
                -LoginUrl "$fixtureBaseUrl/p6/login" -ImportSession -Kind "concurrent" `
                -ConcurrencyLimit $ConcurrentCases -SessionCount $ConcurrentCases
            if (@($world.SessionIds).Count -ne $ConcurrentCases) {
                throw "the concurrency world imported $(@($world.SessionIds).Count) sessions, expected $ConcurrentCases"
            }
            if (@($world.CaseIds).Count -ne $ConcurrentCases) {
                throw "the concurrency world has $(@($world.CaseIds).Count) cases, expected $ConcurrentCases"
            }
            $created = Invoke-Api -Method Post -Path "/api/versions/$($world.VersionId)/batches" -Headers $adminHeaders -Body @{
                caseIds = @($world.CaseIds)
            }
            if ($created.Status -ne 201) { throw "POST concurrency batch returned HTTP $($created.Status)" }
            $batchId = [string]$created.Data.id
            $observedMax = 0
            $batch = $null
            $deadline = (Get-Date).AddSeconds(1800)
            while ((Get-Date) -lt $deadline) {
                $batch = Get-Batch -VersionId $world.VersionId -BatchId $batchId
                $runs = @(Get-CaseRuns -VersionId $world.VersionId -BatchId $batchId)
                $running = @($runs | Where-Object { [string]$_.status -eq "running" }).Count
                if ($running -gt $observedMax) { $observedMax = $running }
                if (@("done", "failed", "cancelled") -contains [string]$batch.status) { break }
                Start-Sleep -Milliseconds 250
            }
            if ($null -eq $batch -or @("done", "failed", "cancelled") -notcontains [string]$batch.status) {
                throw "the $ConcurrentCases-concurrency batch did not reach a terminal state within 1800 s"
            }
            $counts = $batch.counts
            $errors = [int]$counts.failed + [int]$counts.blocked + [int]$counts.inconclusive +
                [int]$counts.interrupted + [int]$counts.skipped
            $script:resultData.concurrentObserved = $observedMax
            $script:resultData.concurrentErrors = $errors
            if ([string]$batch.status -ne "done") { throw "the concurrency batch ended '$($batch.status)'" }
            if ([int]$counts.passed -ne $ConcurrentCases -or $errors -ne 0) {
                throw "concurrency counts are passed=$($counts.passed) failed=$($counts.failed) blocked=$($counts.blocked) inconclusive=$($counts.inconclusive) interrupted=$($counts.interrupted) skipped=$($counts.skipped), expected $ConcurrentCases passed and 0 errors"
            }
            if ($observedMax -lt $ConcurrentCases) {
                throw "observed max concurrency $observedMax < $ConcurrentCases; check EZTEST_EXEC_MAX_CONCURRENT_CASES and the agent run budget"
            }
            Write-Host ("concurrency: {0} cases passed, 0 errors, observed max {1} running, {2} imported sessions" -f `
                    $ConcurrentCases, $observedMax, @($world.SessionIds).Count)
        }
    }

    Invoke-Check -Name "optional token/time metrics recorded" -Body {
        $elapsed = ([DateTime]::UtcNow - $startedAt).TotalMilliseconds
        $script:resultData.elapsedMs = [int64]$elapsed
        if (@($script:resultData.caseDurations).Count -lt 1) { throw "no case durations were recorded" }
        foreach ($metric in @($script:resultData.caseDurations)) {
            if ([int64]$metric.duration_ms -lt 0) { throw "a case duration is negative" }
        }
    }
} catch {
    $script:resultData.error = Get-RedactedText -Text $_.Exception.Message
    Add-CheckResult -Name "harness" -Status "failed" -Detail $script:resultData.error
    Write-Host "REAL P6 EXECUTION LLM ACCEPTANCE FAILED: $($script:resultData.error)" -ForegroundColor Red
} finally {
    Stop-ProcessTree -Process $apiProcess
    Stop-ProcessTree -Process $fixtureProcess
    Stop-ProcessTree -Process $executorProcess
    Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
    try {
        $failedChecks = @($script:checks | Where-Object { $_.status -eq "failed" })
        if ($failedChecks.Count -gt 0 -or $null -ne $script:resultData.error) {
            $script:resultData.status = "failed"
            $exitCode = 1
        } else {
            $script:resultData.status = "passed"
        }
        Write-AcceptanceResult
    } catch {
        Write-Host "Could not write $resultPath" -ForegroundColor Yellow
        $exitCode = 1
    }
    $passed = @($script:checks | Where-Object { $_.status -eq "passed" }).Count
    $failed = @($script:checks | Where-Object { $_.status -eq "failed" }).Count
    Write-Step "Result summary"
    Write-Host ("P6 real-LLM acceptance: {0} passed, {1} failed" -f $passed, $failed)
    if ($exitCode -eq 0) {
        Write-Host "P6 REAL EXECUTION LLM ACCEPTANCE PASSED" -ForegroundColor Green
    } else {
        Write-Host "P6 REAL EXECUTION LLM ACCEPTANCE FAILED" -ForegroundColor Red
    }
}

exit $exitCode
