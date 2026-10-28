#requires -Version 5.1
<#
.SYNOPSIS
    Optional real-LLM acceptance for the P5 case mindmap and design review
    (gated by EZTEST_RUN_REAL_LLM=1).

.DESCRIPTION
    Without the gate this script prints SKIPPED and exits 0. With the gate open
    it drives one real-LLM P5 acceptance run against the deterministic fixture
    target and code repository (spec §10.8):

      1. gate: EZTEST_RUN_REAL_LLM=1 and all llm_base_url/llm_key/llm_model_name
         keys present; a missing key is a failure, never a skip;
      2. reset the dedicated eztest_e2e database, build and start the current
         API (or reuse EZTEST_ACCEPTANCE_API_URL), check health and log in as
         the administrator;
      3. seed a system, environment, manual page/route/api app model nodes, a
         code repository bound to the fixture Git repository, a version, three
         confirmed requirements and one requirement relation;
      4. a real design run: 202 -> poll -> done; at least 3 cases, every case
         with >=2 steps, >=1 assertion and a clarified requirement source, all
         AI-authored;
      5. coverage: at least 2 of the 3 requirements are covered and every
         uncovered requirement is listed explicitly in
         coverage.uncoveredRequirementIds;
      6. code anchors: at least one anchor points at a file that exists at the
         version's baseline commit, or the run reports anchorsDropped with a
         reason (the two outcomes are mutually explainable);
      7. a real review run: done, findings >=1 and at least one finding in the
         coverage or code_uncovered category; blocker findings gate approval;
      8. manual editing: title/step/assertion edits survive a graph reload and a
         stale node revision is 409 case_revision_conflict;
      9. reject: a fresh review run followed by decision=reject returns the
         version to designing, creates no baseline and keeps the graph editable;
     10. resubmit and approve (with acknowledgeFindings when blockers exist):
         executing, baseline frozen, nodes reviewed and writes 409
         baseline_frozen;
     11. Markdown/Mermaid/OPML exports: files exist, sha256 matches the
         downloaded bytes, content carries the real case title/steps/coverage
         matrix/baseline, and a repeated frozen export reuses the same id;
     12. events: case.created/case.reviewed/case.baseline_frozen/
         case.export_ready are visible and seq is strictly increasing;
     13. a real data-plan run: done and at least one case's preconditions or
         test data was filled in;
     14. exploration against the fixture: the run is done/degraded and the
         imported app model contains explored page/route/api nodes; a second
         concurrent explore run is 409 run_in_progress;
     15. degradation: after the default LLM configuration is deleted, a design
         run fails with reason ai_unavailable while the manual create/edit path
         still works;
     16. idempotency/concurrency: the run_in_progress 409 was observed, the
         repeated export reused the same id and a second approval is 409
         baseline_frozen.

    The script is fail-closed: a degraded design/review/data-plan run, a
    missing real anchor (without a recorded anchorsDropped reason) or a missing
    LLM key fails rather than being reported as a pass. Results (statuses, ids,
    counts and error codes only - never prompts, source bodies or keys) are
    written to .tmp/p5-real-acceptance.json.

.PARAMETER DryRun
    Validate paths, the .env key presence (names only), the database
    target-changing-query/psql-vs-API preflight, the Go/psql/git/node
    availability and the fixture scripts without resetting the database,
    starting the API or spending an LLM call. Exits 0 when the preflight
    passes. DryRun also works while the gate is closed.

.NOTES
    This script makes paid upstream calls and is deliberately not part of
    scripts/acceptance.ps1, which reports it as gated. It resets only the
    dedicated eztest_e2e database after Assert-SafeDatabaseUrl has rejected
    target-changing query parameters. Compatible with Windows PowerShell 5.1
    and PowerShell 7+; PostgreSQL must already be running.
#>
[CmdletBinding()]
param(
    [switch]$DryRun
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$backendDir = Join-Path $repoRoot "backend"
$frontendDir = Join-Path $repoRoot "frontend"
$scriptsDir = Join-Path $repoRoot "scripts"
$tmpDir = Join-Path $repoRoot ".tmp"
$envPath = Join-Path $repoRoot ".env"
$resultPath = Join-Path $tmpDir "p5-real-acceptance.json"
$e2eDbName = "eztest_e2e"
$apiPort = 13333
$apiBaseUrl = "http://localhost:$apiPort"
$fixturePort = 13335
$fixtureBaseUrl = "http://127.0.0.1:$fixturePort"
$pollIntervalSeconds = 2
$pollTimeoutSeconds = 480
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

# Secrets parsed from .env live here only so error text can be redacted. They
# are never printed, logged or written to the result JSON.
$script:secretValues = @()
$script:checks = New-Object System.Collections.ArrayList
$script:warnings = New-Object System.Collections.ArrayList
$script:resultData = [ordered]@{
    status         = "running"
    generatedAt    = [DateTime]::UtcNow.ToString("o")
    versionId      = $null
    requirementIds = @()
    designRunId    = $null
    reviewRunId    = $null
    dataPlanRunId  = $null
    exploreRunId   = $null
    baselineId     = $null
    exportIds      = @{}
    caseCount      = $null
    coveredCount   = $null
    findingCount   = $null
    blockerCount   = $null
    anchorCount    = $null
    llmConfigured  = $null
    degraded       = $null
    runConcurrency409 = $false
    checks         = @()
    warnings       = @()
    error          = $null
}
$script:state = @{
    VersionId          = $null
    RequirementIds     = @()
    RelationId         = $null
    SystemId           = $null
    EnvironmentId      = $null
    RepoId             = $null
    FixtureRepo        = $null
    FixtureBaseCommit  = $null
    FixtureBranch      = $null
    CaseIds            = @()
    CaseNode           = $null
    BaselineId         = $null
    FindingCount       = 0
    BlockerCount       = 0
    RunConcurrency409  = $false
    ExportIds          = @{}
    DataPlanRunId      = $null
    ReviewRunId        = $null
    DesignRunId        = $null
    ExploreRunId       = $null
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

function Assert-SafeDatabaseUrl {
    param([string]$DatabaseUrl)
    # pgx applies query parameters over the URL path and host:port, so a URL
    # whose path names eztest_e2e can still connect elsewhere via ?dbname= or
    # ?database= (or redirect the host/port). The script resets eztest_e2e
    # through psql authority while the API receives the URL, so reject
    # target-changing keys before any destructive statement. Error messages
    # never echo the URL or credentials.
    $uri = $null
    if (-not [System.Uri]::TryCreate($DatabaseUrl, [System.UriKind]::Absolute, [ref]$uri)) {
        throw "EZTEST_DATABASE_URL is not a valid absolute URL"
    }
    if ($uri.Scheme -ne "postgres" -and $uri.Scheme -ne "postgresql") {
        throw "EZTEST_DATABASE_URL must use the postgres scheme"
    }
    if ([string]::IsNullOrWhiteSpace($uri.Host)) {
        throw "EZTEST_DATABASE_URL must include a host"
    }
    if ($uri.Port -ne -1 -and ($uri.Port -lt 1 -or $uri.Port -gt 65535)) {
        throw "EZTEST_DATABASE_URL port must be between 1 and 65535 when present"
    }
    $query = $uri.Query
    if (-not [string]::IsNullOrWhiteSpace($query)) {
        foreach ($pair in ($query.TrimStart([char]"?")) -split "&") {
            if ([string]::IsNullOrWhiteSpace($pair)) { continue }
            $equals = $pair.IndexOf("=")
            $key = if ($equals -ge 0) { $pair.Substring(0, $equals) } else { $pair }
            $key = [System.Uri]::UnescapeDataString($key).Trim().ToLowerInvariant()
            if ($key -eq "dbname" -or $key -eq "database" -or $key -eq "host" -or $key -eq "port") {
                throw "EZTEST_DATABASE_URL must not override the connection target with the query parameter '$key'; put the host, port and database in the URL itself"
            }
        }
    }
    $databaseName = [System.Uri]::UnescapeDataString($uri.AbsolutePath.TrimStart([char[]]"/"))
    if ([string]::IsNullOrWhiteSpace($databaseName) -or $databaseName.Contains("/")) {
        throw "EZTEST_DATABASE_URL must include a single database name in its path"
    }
    return $uri
}

function New-E2eDatabaseUrl {
    param([System.Uri]$DatabaseUri, [string]$DatabaseName)
    $builder = New-Object System.UriBuilder -ArgumentList @($DatabaseUri)
    $builder.Path = "/" + $DatabaseName
    return $builder.Uri.AbsoluteUri
}

function Get-DatabaseTargetName {
    param([System.Uri]$Uri)
    $name = [System.Uri]::UnescapeDataString($Uri.AbsolutePath.TrimStart([char[]]"/"))
    $port = if ($Uri.Port -gt 0) { $Uri.Port } else { 5432 }
    return ("{0}:{1}/{2}" -f $Uri.Host.ToLowerInvariant(), $port, $name)
}

function Resolve-ToolPath {
    param([string]$LocalPath, [string]$CommandName)
    if (-not [string]::IsNullOrWhiteSpace($LocalPath) -and (Test-Path -LiteralPath $LocalPath -PathType Leaf)) {
        return $LocalPath
    }
    $command = Get-Command $CommandName -ErrorAction SilentlyContinue
    if ($null -ne $command -and -not [string]::IsNullOrWhiteSpace([string]$command.Source)) {
        return [string]$command.Source
    }
    return ""
}

function Get-RedactedText {
    param([string]$Text)
    if ([string]::IsNullOrEmpty($Text)) { return "" }
    foreach ($secret in $script:secretValues) {
        if (-not [string]::IsNullOrEmpty($secret)) {
            $Text = $Text.Replace($secret, "[redacted]")
        }
    }
    $Text = $Text -replace "[\r\n]+", " "
    if ($Text.Length -gt 400) { $Text = $Text.Substring(0, 400) + "..." }
    return $Text
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

function Assert-PortFree {
    param([int]$Port)
    $listeners = Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue
    if ($listeners) { throw "Port $Port is already in use; stop the other process or set EZTEST_ACCEPTANCE_API_URL" }
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

function Invoke-Api {
    param(
        [string]$Method,
        [string]$Uri,
        $Body = $null,
        $Headers = $null,
        [int]$TimeoutSec = 60
    )
    $arguments = @{
        Method          = $Method
        Uri             = $Uri
        TimeoutSec      = $TimeoutSec
        UseBasicParsing = $true
    }
    if ($null -ne $Headers) { $arguments["Headers"] = $Headers }
    if ($null -ne $Body) {
        $json = $Body | ConvertTo-Json -Compress -Depth 12
        $arguments["ContentType"] = "application/json; charset=utf-8"
        $arguments["Body"] = [System.Text.Encoding]::UTF8.GetBytes($json)
    }
    $response = Invoke-WebRequest @arguments
    $parsed = $null
    if (-not [string]::IsNullOrWhiteSpace([string]$response.Content) -and $response.Content -is [string]) {
        $parsed = $response.Content | ConvertFrom-Json
    }
    $data = $null
    if ($null -ne $parsed) { $data = $parsed.data }
    return [pscustomobject]@{
        Status = [int]$response.StatusCode
        Raw    = $parsed
        Data   = $data
    }
}

function Get-ApiErrorSummary {
    param($ErrorRecord)
    $status = 0
    $code = ""
    $reason = ""
    $rawBody = $null
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
        # Keep only what was extracted safely.
    }
    if ([string]::IsNullOrWhiteSpace($rawBody) -and $null -ne $ErrorRecord.ErrorDetails) {
        $rawBody = [string]$ErrorRecord.ErrorDetails.Message
    }
    if (-not [string]::IsNullOrWhiteSpace($rawBody)) {
        try {
            $parsed = $rawBody | ConvertFrom-Json
            $code = [string]$parsed.error.code
            if ($null -ne $parsed.error.details) { $reason = [string]$parsed.error.details.reason }
        } catch {
            $code = ""
            $reason = ""
        }
    }
    return [pscustomobject]@{
        Status = $status
        Code   = $code
        Reason = $reason
        Text   = (Get-RedactedText -Text ([string]$ErrorRecord.Exception.Message))
    }
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
    $login = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/auth/login" -Body @{
        username = $Username
        password = $Password
    }
    if ($login.Status -ne 200) { throw "login for $Username returned HTTP $($login.Status)" }
    $token = [string]$login.Data.token
    if ([string]::IsNullOrWhiteSpace($token)) { throw "login for $Username did not return a token" }
    return $token
}

function Get-MindmapGraph {
    param([string]$VersionId, $Headers)
    $response = Invoke-Api -Method Get -Uri "$apiBaseUrl/api/versions/$VersionId/mindmap" -Headers $Headers
    if ($response.Status -ne 200) { throw "GET /mindmap returned HTTP $($response.Status)" }
    return $response.Data
}

function Wait-Run {
    param([string]$VersionId, [string]$RunId, $Headers, [int]$TimeoutSeconds = $pollTimeoutSeconds)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        $response = Invoke-Api -Method Get -Uri "$apiBaseUrl/api/versions/$VersionId/mindmap/runs/$RunId" -Headers $Headers
        if ($response.Status -ne 200) { throw "GET mindmap run returned HTTP $($response.Status)" }
        $status = [string]$response.Data.status
        if (@("done", "failed", "degraded") -contains $status) { return $response.Data }
        Start-Sleep -Seconds $pollIntervalSeconds
    }
    throw "mindmap run $RunId did not finish within $TimeoutSeconds seconds"
}

function Get-Sha256Hex {
    param([byte[]]$Bytes)
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try { $hash = $sha.ComputeHash($Bytes) } finally { $sha.Dispose() }
    return (($hash | ForEach-Object { $_.ToString("x2") }) -join "")
}

function Invoke-Download {
    param([string]$Path, $Headers, [string]$OutFile)
    $client = New-Object System.Net.WebClient
    try {
        if ($null -ne $Headers) {
            foreach ($key in $Headers.Keys) { $client.Headers.Add([string]$key, [string]$Headers[$key]) }
        }
        $bytes = $client.DownloadData($apiBaseUrl + $Path)
        $disposition = [string]$client.ResponseHeaders["Content-Disposition"]
    } finally {
        $client.Dispose()
    }
    if (-not [string]::IsNullOrWhiteSpace($OutFile)) { [System.IO.File]::WriteAllBytes($OutFile, $bytes) }
    return [pscustomobject]@{
        Bytes              = $bytes
        Size               = @($bytes).Count
        ContentDisposition = $disposition
        Sha256             = Get-Sha256Hex -Bytes $bytes
    }
}

function New-DesigningVersion {
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
    $body = @{ name = $Name; description = "P5 real-LLM acceptance version" }
    if ($null -ne $SystemId -and [int64]$SystemId -gt 0) { $body["systemId"] = [int64]$SystemId }
    if ($null -ne $EnvironmentId -and [int64]$EnvironmentId -gt 0) { $body["environmentId"] = [int64]$EnvironmentId }
    if ($null -ne $CodeRepositoryId -and [int64]$CodeRepositoryId -gt 0) {
        $body["codeRepositoryId"] = [int64]$CodeRepositoryId
        $body["codeBranch"] = $CodeBranch
        $body["codeBaselineCommit"] = $BaselineCommit
    }
    $version = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions" -Headers $Headers -Body $body
    if ($version.Status -ne 201) { throw "POST /api/versions returned HTTP $($version.Status)" }
    $versionId = [string]$version.Data.id
    $requirementIds = @()
    for ($i = 1; $i -le $RequirementCount; $i++) {
        $requirement = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$versionId/requirements" -Headers $Headers -Body @{
            title              = "$Name requirement $i"
            description        = "Acceptance requirement $i for the P5 mindmap real-LLM run."
            acceptanceCriteria = "The generated case covers requirement $i."
            priority           = "must"
        }
        if ($requirement.Status -ne 201) { throw "POST requirement returned HTTP $($requirement.Status)" }
        $requirementIds += [string]$requirement.Data.id
    }
    $clarify = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$versionId/start-clarification" -Headers $Headers
    if ($clarify.Status -ne 200) { throw "POST start-clarification returned HTTP $($clarify.Status)" }
    foreach ($requirementId in $requirementIds) {
        $confirmed = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$versionId/requirements/$requirementId/confirm" -Headers $Headers
        if ($confirmed.Status -ne 200) { throw "POST requirement confirm returned HTTP $($confirmed.Status)" }
    }
    $enter = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$versionId/enter-design" -Headers $Headers -Body @{
        acknowledgeUnclarified = $true
    }
    if ($enter.Status -ne 200) { throw "POST enter-design returned HTTP $($enter.Status)" }
    return [pscustomobject]@{
        VersionId      = $versionId
        RequirementIds = $requirementIds
        Version        = $enter.Data.version
    }
}

function New-ManualCase {
    param($Headers, [string]$VersionId, [string[]]$RequirementIds = @(), [string]$Prefix = "manual")
    $scenario = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$VersionId/mindmap/nodes" -Headers $Headers -Body @{
        kind  = "scenario"
        title = "$Prefix scenario"
    }
    if ($scenario.Status -ne 201) { throw "POST scenario node returned HTTP $($scenario.Status)" }
    $caseBody = @{ kind = "case"; parentId = [string]$scenario.Data.id; title = "$Prefix case"; priority = "P1" }
    if ($RequirementIds.Count -gt 0) { $caseBody["requirementIds"] = @($RequirementIds[0]) }
    $case = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$VersionId/mindmap/nodes" -Headers $Headers -Body $caseBody
    if ($case.Status -ne 201) { throw "POST case node returned HTTP $($case.Status)" }
    $caseId = [string]$case.Data.id
    $step = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$VersionId/mindmap/nodes/$caseId/steps" -Headers $Headers -Body @{
        action   = "Open the fixture home page"
        expected = "The fixture home page is shown"
    }
    if ($step.Status -ne 201) { throw "POST case step returned HTTP $($step.Status)" }
    $assertion = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$VersionId/mindmap/nodes/$caseId/assertions" -Headers $Headers -Body @{
        stepId     = [string]$step.Data.id
        kind       = "ui"
        target     = "/p5/home"
        expression = "The fixture page title is not empty"
        expected   = "The fixture page title is shown"
    }
    if ($assertion.Status -ne 201) { throw "POST case assertion returned HTTP $($assertion.Status)" }
    $graph = Get-MindmapGraph -VersionId $VersionId -Headers $Headers
    $node = @($graph.nodes | Where-Object { [string]$_.id -eq $caseId })[0]
    if ($null -ne $node -and @($node.sources).Count -eq 0 -and $RequirementIds.Count -gt 0) {
        $source = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$VersionId/mindmap/nodes/$caseId/sources" -Headers $Headers -Body @{
            refKind = "requirement"
            refId   = [string]$RequirementIds[0]
        }
        if ($source.Status -ne 201) { throw "POST case source returned HTTP $($source.Status)" }
    }
    return [pscustomobject]@{
        ScenarioId  = [string]$scenario.Data.id
        CaseId      = $caseId
        StepId      = [string]$step.Data.id
        AssertionId = [string]$assertion.Data.id
    }
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
    $script:resultData.warnings = @($script:warnings)
    $script:resultData.exportIds = $script:state.ExportIds
    $json = ConvertTo-Json -InputObject $script:resultData -Depth 10
    [System.IO.File]::WriteAllText($resultPath, $json + "`n", (New-Object System.Text.UTF8Encoding($false)))
}

function Invoke-DryRun {
    Write-Step "DryRun: validating paths and environment (no database reset, no API, no LLM call)"
    $problems = @()
    if (-not (Test-Path -LiteralPath $repoRoot -PathType Container)) { $problems += "repository root not found" }
    if (-not (Test-Path -LiteralPath $backendDir -PathType Container)) { $problems += "backend directory not found" }
    if (-not (Test-Path -LiteralPath (Join-Path $scriptsDir "fixture-target.cjs"))) { $problems += "scripts/fixture-target.cjs is missing" }
    if (-not (Test-Path -LiteralPath (Join-Path $scriptsDir "fixture-codeintel-repo.ps1"))) { $problems += "scripts/fixture-codeintel-repo.ps1 is missing" }

    $gitExe = Resolve-ToolPath -LocalPath "" -CommandName "git"
    $nodeExe = Resolve-ToolPath -LocalPath "" -CommandName "node"
    if ([string]::IsNullOrWhiteSpace($gitExe)) { $problems += "git executable not found on PATH" }
    if ([string]::IsNullOrWhiteSpace($nodeExe)) { $problems += "node executable not found on PATH" }

    $presentKeys = @()
    $dryRunTarget = $null
    if (-not (Test-Path -LiteralPath $envPath -PathType Leaf)) {
        $problems += ".env not found at $envPath"
    } else {
        $envValues = Read-DotEnv -Path $envPath
        foreach ($key in $requiredEnvKeys) {
            if (-not $envValues.ContainsKey($key) -or [string]::IsNullOrWhiteSpace([string]$envValues[$key])) {
                $problems += "missing .env key: $key"
            } else {
                $presentKeys += $key
            }
        }
        if ($envValues.ContainsKey("EZTEST_DATABASE_URL") -and
            -not [string]::IsNullOrWhiteSpace([string]$envValues["EZTEST_DATABASE_URL"])) {
            try {
                $dryRunDatabaseUri = Assert-SafeDatabaseUrl -DatabaseUrl ([string]$envValues["EZTEST_DATABASE_URL"])
                $dryRunE2eUrl = New-E2eDatabaseUrl -DatabaseUri $dryRunDatabaseUri -DatabaseName $e2eDbName
                $dryRunE2eUri = [System.Uri]$dryRunE2eUrl
                $dryRunTarget = Get-DatabaseTargetName -Uri $dryRunE2eUri
                $dryRunPort = if ($dryRunDatabaseUri.Port -gt 0) { $dryRunDatabaseUri.Port } else { 5432 }
                $psqlTarget = "{0}:{1}/{2}" -f $dryRunDatabaseUri.Host.ToLowerInvariant(), $dryRunPort, $e2eDbName
                if ($psqlTarget -ne $dryRunTarget) {
                    $problems += "EZTEST_DATABASE_URL preflight: the rebuilt API target differs from the psql target"
                }
            } catch {
                $problems += ("EZTEST_DATABASE_URL preflight: " + $_.Exception.Message)
            }
        }
    }

    if ([string]::IsNullOrWhiteSpace($env:EZTEST_ACCEPTANCE_API_URL)) {
        $goExe = Resolve-ToolPath -LocalPath (Join-Path $repoRoot ".tools\go\bin\go.exe") -CommandName "go"
        $psqlExe = Resolve-ToolPath -LocalPath (Join-Path $repoRoot ".tools\pgsql\bin\psql.exe") -CommandName "psql"
        if ([string]::IsNullOrWhiteSpace($goExe)) { $problems += "go executable not found (.tools or PATH)" }
        if ([string]::IsNullOrWhiteSpace($psqlExe)) { $problems += "psql executable not found (.tools or PATH)" }
    }

    if ($problems.Count -gt 0) {
        Write-Host "DRYRUN FAIL:" -ForegroundColor Red
        foreach ($problem in $problems) { Write-Host "  - $problem" -ForegroundColor Red }
        return 1
    }
    Write-Host "DryRun: repository root: $repoRoot"
    Write-Host ("DryRun: .env found; required keys present ({0}/{1})" -f $presentKeys.Count, $requiredEnvKeys.Count)
    if ($null -ne $dryRunTarget) {
        Write-Host ("DryRun: database preflight OK; API and psql both target {0} (no target-changing query keys)" -f $dryRunTarget)
    }
    Write-Host "DryRun: git executable found: $gitExe"
    Write-Host "DryRun: node executable found: $nodeExe"
    Write-Host "DryRun: fixture scripts found (fixture-target.cjs, fixture-codeintel-repo.ps1)"
    Write-Host "DryRun: export root will be .tmp/p5-real-exports"
    if ($env:EZTEST_RUN_REAL_LLM -ne "1") {
        Write-Host "DryRun: gate is closed (EZTEST_RUN_REAL_LLM != 1); the real run would print SKIPPED and exit 0."
    } else {
        Write-Host "DryRun: gate is open; the real run will reset $e2eDbName, build/start the API and make paid LLM calls."
    }
    Write-Host "DRYRUN OK" -ForegroundColor Green
    return 0
}

if ($DryRun) {
    exit (Invoke-DryRun)
}

if ($env:EZTEST_RUN_REAL_LLM -ne "1") {
    Write-Host "SKIPPED: real P5 mindmap LLM acceptance is disabled (set EZTEST_RUN_REAL_LLM=1 to run it)."
    exit 0
}

$exitCode = 0
$apiProcess = $null
$fixtureProcess = $null
$apiExe = ""
$goExe = ""
$psqlExe = ""
$gitExe = ""
$nodeExe = ""
$adminHeaders = $null

try {
    New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null

    Write-Step "Loading .env and validating required keys"
    if (-not (Test-Path -LiteralPath $envPath -PathType Leaf)) { throw "Missing .env at $envPath" }
    $envValues = Read-DotEnv -Path $envPath
    $missingKeys = @()
    foreach ($key in $requiredEnvKeys) {
        if (-not $envValues.ContainsKey($key) -or [string]::IsNullOrWhiteSpace([string]$envValues[$key])) {
            $missingKeys += $key
        }
    }
    if ($missingKeys.Count -gt 0) {
        throw ("EZTEST_RUN_REAL_LLM=1 but .env is missing required key(s): " + ($missingKeys -join ", ") + ". Add the missing key(s) to .env or unset EZTEST_RUN_REAL_LLM.")
    }
    $adminUsername = Get-RequiredValue -Values $envValues -Key "EZTEST_ADMIN_USERNAME"
    $adminPassword = Get-RequiredValue -Values $envValues -Key "EZTEST_ADMIN_PASSWORD"
    $llmKey = Get-RequiredValue -Values $envValues -Key "llm_key"
    $llmBaseUrl = Get-RequiredValue -Values $envValues -Key "llm_base_url"
    $llmModel = Get-RequiredValue -Values $envValues -Key "llm_model_name"
    $databaseUrl = Get-RequiredValue -Values $envValues -Key "EZTEST_DATABASE_URL"
    $script:secretValues = @($adminPassword, $llmKey, $databaseUrl)
    if ($envValues.ContainsKey("EZTEST_SECRET_KEYS")) {
        $rawSecretKeys = [string]$envValues["EZTEST_SECRET_KEYS"]
        if (-not [string]::IsNullOrWhiteSpace($rawSecretKeys)) {
            $script:secretValues += $rawSecretKeys
            try {
                $secretObject = $rawSecretKeys | ConvertFrom-Json
                foreach ($property in $secretObject.PSObject.Properties) {
                    if ($null -ne $property.Value -and -not [string]::IsNullOrWhiteSpace([string]$property.Value)) {
                        $script:secretValues += [string]$property.Value
                    }
                }
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
    $e2eDatabaseUrl = New-E2eDatabaseUrl -DatabaseUri $databaseUri -DatabaseName $e2eDbName
    $e2eDatabaseUri = [System.Uri]$e2eDatabaseUrl
    $psqlTarget = "{0}:{1}/{2}" -f $dbHost.ToLowerInvariant(), $dbPort, $e2eDbName
    $apiTarget = Get-DatabaseTargetName -Uri $e2eDatabaseUri
    if ($psqlTarget -ne $apiTarget) {
        throw "EZTEST_DATABASE_URL preflight failed: the API target and the psql reset target differ; refusing to reset a database the API might not use"
    }

    $gitExe = Resolve-ToolPath -LocalPath "" -CommandName "git"
    if ([string]::IsNullOrWhiteSpace($gitExe)) { throw "git executable not found on PATH" }
    $nodeExe = Resolve-ToolPath -LocalPath "" -CommandName "node"
    if ([string]::IsNullOrWhiteSpace($nodeExe)) { throw "node executable not found on PATH" }

    $reuseApiUrl = [string]$env:EZTEST_ACCEPTANCE_API_URL
    if ([string]::IsNullOrWhiteSpace($reuseApiUrl)) {
        $goExe = Resolve-ToolPath -LocalPath (Join-Path $repoRoot ".tools\go\bin\go.exe") -CommandName "go"
        if ([string]::IsNullOrWhiteSpace($goExe)) { throw "go executable not found (.tools or PATH)" }
        $psqlExe = Resolve-ToolPath -LocalPath (Join-Path $repoRoot ".tools\pgsql\bin\psql.exe") -CommandName "psql"
        if ([string]::IsNullOrWhiteSpace($psqlExe)) { throw "psql executable not found (.tools or PATH)" }

        Assert-PortFree -Port $apiPort
        Write-Step "Resetting $e2eDbName and building the API"
        $env:PGPASSWORD = $dbPassword
        try {
            $exists = Get-PsqlScalar -Database "postgres" -Sql "SELECT count(*) FROM pg_database WHERE datname = '$e2eDbName';"
            if ($exists -ne "0") {
                $terminateSql = "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$e2eDbName' AND pid <> pg_backend_pid();"
                Invoke-PsqlQuiet -Database "postgres" -Sql $terminateSql -Operation "terminating connections to $e2eDbName"
                Invoke-PsqlQuiet -Database "postgres" -Sql "DROP DATABASE $e2eDbName;" -Operation "dropping database $e2eDbName"
            }
            Invoke-PsqlQuiet -Database "postgres" -Sql "CREATE DATABASE $e2eDbName;" -Operation "creating database $e2eDbName"
        } finally {
            $env:PGPASSWORD = $dbPassword
        }

        $apiExe = Join-Path $tmpDir "acceptance-mindmap-llm-api.exe"
        $buildLog = Join-Path $tmpDir "acceptance-mindmap-llm-build.log"
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
        if ($buildExit -ne 0) { throw "go build ./cmd/eztest failed (see .tmp/acceptance-mindmap-llm-build.log)" }

        $exportDir = Join-Path $tmpDir "p5-real-exports"
        if (Test-Path -LiteralPath $exportDir) { Remove-Item -LiteralPath $exportDir -Recurse -Force }
        New-Item -ItemType Directory -Force -Path $exportDir | Out-Null
        Write-Step "Starting API on :$apiPort with the real LLM configuration from .env"
        $env:EZTEST_ENV = "dev"
        $env:EZTEST_DATABASE_URL = $e2eDatabaseUrl
        $env:EZTEST_HTTP_ADDR = ":$apiPort"
        $env:EZTEST_WEB_ORIGIN = "http://localhost:6868"
        $env:EZTEST_ENABLE_TEST_ENDPOINTS = "true"
        $env:EZTEST_STORAGE_DIR = (Join-Path $tmpDir "e2e-objects")
        $env:EZTEST_EXPORT_DIR = $exportDir
        $env:EZTEST_OPENAPI_PATH = (Join-Path $repoRoot "docs\openapi\api.yaml")
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
        $env:EZTEST_EXPORT_DIR = $exportDir
        Remove-Item Env:EZTEST_TEST_DATABASE_URL -ErrorAction SilentlyContinue

        $apiOut = Join-Path $tmpDir "acceptance-mindmap-llm-api.out.log"
        $apiErr = Join-Path $tmpDir "acceptance-mindmap-llm-api.err.log"
        Remove-Item -LiteralPath $apiOut, $apiErr -ErrorAction SilentlyContinue
        $apiProcess = Start-Process -FilePath $apiExe -WorkingDirectory $backendDir `
            -RedirectStandardOutput $apiOut -RedirectStandardError $apiErr -WindowStyle Hidden -PassThru
        try {
            Wait-ForHttp -Url "$apiBaseUrl/api/health" -TimeoutSeconds 90 -Process $apiProcess -Label "API"
        } catch {
            Stop-ProcessTree -Process $apiProcess
            $apiProcess = $null
            throw
        }
    } else {
        $apiBaseUrl = $reuseApiUrl.TrimEnd("/")
        Write-Step "Reusing the API at $apiBaseUrl (EZTEST_ACCEPTANCE_API_URL)"
        Wait-ForHttp -Url "$apiBaseUrl/api/health" -TimeoutSeconds 30 -Process $null -Label "API"
    }

    Write-Step "Checking health and the LLM configuration"
    $health = Invoke-Api -Method Get -Uri "$apiBaseUrl/api/health"
    if ([string]$health.Raw.data.status -ne "ok" -or [string]$health.Raw.data.db -ne "ok") {
        throw "API health is not ok"
    }
    $llmConfigured = [bool]$health.Raw.data.llm_configured
    $script:resultData.llmConfigured = $llmConfigured
    if (-not $llmConfigured) {
        throw "health reports llm_configured=false; refusing to claim real-LLM acceptance (add valid llm_* keys to .env)"
    }

    Write-Step "Logging in as admin"
    $adminToken = Get-Token -Username $adminUsername -Password $adminPassword
    $adminHeaders = @{ Authorization = "Bearer $adminToken" }
    Add-CheckResult -Name "database reset, migrations, health and admin login" -Status "passed" -Detail "API healthy, llm_configured=true, admin token obtained"

    Write-Step "Seeding the P5 acceptance world"
    $fixtureRepo = Join-Path $tmpDir "p4-e2e-repo"
    if (-not (Test-Path -LiteralPath (Join-Path $fixtureRepo ".git"))) {
        $fixtureLog = Join-Path $tmpDir "acceptance-mindmap-llm-fixture.log"
        Push-Location $repoRoot
        $previous = $ErrorActionPreference
        $ErrorActionPreference = "SilentlyContinue"
        try {
            & powershell.exe -NoProfile -ExecutionPolicy Bypass -File (Join-Path $scriptsDir "fixture-codeintel-repo.ps1") *> $fixtureLog
            $fixtureExit = $LASTEXITCODE
        } finally {
            $ErrorActionPreference = $previous
            Pop-Location
        }
        if ($fixtureExit -ne 0 -or -not (Test-Path -LiteralPath (Join-Path $fixtureRepo ".git"))) {
            throw "fixture-codeintel-repo.ps1 failed; the P5 anchors/exploration seed needs the fixture repository"
        }
    }
    $fixtureBaseCommit = ((& $gitExe -C $fixtureRepo rev-parse HEAD 2>$null | Out-String).Trim())
    $fixtureBranch = ((& $gitExe -C $fixtureRepo rev-parse --abbrev-ref HEAD 2>$null | Out-String).Trim())
    if ($fixtureBaseCommit -notmatch '^[a-f0-9]{40}$') { throw "could not resolve the fixture repository HEAD commit" }
    if ([string]::IsNullOrWhiteSpace($fixtureBranch) -or $fixtureBranch -eq "HEAD") { $fixtureBranch = "master" }
    $script:state.FixtureRepo = $fixtureRepo
    $script:state.FixtureBaseCommit = $fixtureBaseCommit
    $script:state.FixtureBranch = $fixtureBranch

    $stamp = Get-Date -Format "yyyyMMddHHmmss"
    $system = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/resources/systems" -Headers $adminHeaders -Body @{
        key     = "p5real$stamp"
        name    = "P5 real-LLM acceptance system"
        kind    = "web"
        baseUrl = $fixtureBaseUrl
    }
    if ($system.Status -ne 201) { throw "POST system returned HTTP $($system.Status)" }
    $script:state.SystemId = $system.Data.id
    $environment = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/resources/systems/$($script:state.SystemId)/environments" -Headers $adminHeaders -Body @{
        key          = "p5real$stamp"
        name         = "P5 real-LLM acceptance environment"
        baseUrl      = $fixtureBaseUrl
        kind         = "dev"
        allowedHosts = @("127.0.0.1")
    }
    if ($environment.Status -ne 201) { throw "POST environment returned HTTP $($environment.Status)" }
    $script:state.EnvironmentId = $environment.Data.id
    foreach ($appModel in @(
            @{ key = "page:/manual/home"; kind = "page"; name = "P5 manual home"; path = "/manual/home"; method = "" },
            @{ key = "route:/manual/orders"; kind = "route"; name = "P5 manual orders"; path = "/manual/orders"; method = "" },
            @{ key = "api:GET /manual/orders"; kind = "api"; name = "P5 manual orders API"; path = "/manual/orders"; method = "GET" }
        )) {
        $created = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/resources/systems/$($script:state.SystemId)/app-models" -Headers $adminHeaders -Body $appModel
        if ($created.Status -ne 201) { throw "POST app model returned HTTP $($created.Status)" }
    }
    $repo = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/resources/code-repos" -Headers $adminHeaders -Body @{
        key         = "p5real$stamp"
        name        = "P5 real-LLM acceptance fixture repo"
        localPath   = $fixtureRepo
        fixedBranch = $fixtureBranch
    }
    if ($repo.Status -ne 201) { throw "POST code repo returned HTTP $($repo.Status)" }
    $script:state.RepoId = $repo.Data.id

    $version = New-DesigningVersion -Headers $adminHeaders -Name "P5 real-LLM acceptance $stamp" -RequirementCount 3 `
        -SystemId $script:state.SystemId -EnvironmentId $script:state.EnvironmentId `
        -CodeRepositoryId $script:state.RepoId -CodeBranch $fixtureBranch -BaselineCommit $fixtureBaseCommit
    $script:state.VersionId = $version.VersionId
    $script:state.RequirementIds = @($version.RequirementIds)
    $script:resultData.versionId = $version.VersionId
    $script:resultData.requirementIds = @($version.RequirementIds)

    $relation = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$($script:state.VersionId)/relations" -Headers $adminHeaders -Body @{
        fromRequirementId = [string]$script:state.RequirementIds[0]
        toRequirementId   = [string]$script:state.RequirementIds[1]
        relationType      = "depends_on"
        description       = "P5 real-LLM acceptance relation"
    }
    if ($relation.Status -ne 201) { throw "POST requirement relation returned HTTP $($relation.Status)" }
    $script:state.RelationId = [string]$relation.Data.id
    Add-CheckResult -Name "seed system/environment/app model/code repo/version/3 requirements/relation" -Status "passed" `
        -Detail ("version {0}, 3 confirmed requirements, fixture repo {1}" -f $script:state.VersionId, $fixtureBaseCommit.Substring(0, 12))

    Invoke-Check -Name "real design run produces a high-quality graph" -Body {
        $start = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$($script:state.VersionId)/mindmap/runs" -Headers $adminHeaders -Body @{
            kind       = "design"
            mode       = "append"
            headCommit = $script:state.FixtureBaseCommit
        }
        if ($start.Status -ne 202) { throw "POST design run returned HTTP $($start.Status)" }
        $runId = [string]$start.Data.runId
        $script:state.DesignRunId = $runId
        $script:resultData.designRunId = $runId
        $run = Wait-Run -VersionId $script:state.VersionId -RunId $runId -Headers $adminHeaders
        if ([string]$run.status -ne "done") { throw "design run ended '$($run.status)' reason='$($run.reason)'; expected done" }
        $graph = Get-MindmapGraph -VersionId $script:state.VersionId -Headers $adminHeaders
        $cases = @($graph.nodes | Where-Object { [string]$_.kind -eq "case" })
        if ($cases.Count -lt 3) { throw "design produced $($cases.Count) cases, expected at least 3" }
        foreach ($case in $cases) {
            if (@($case.steps).Count -lt 2) { throw "case '$($case.title)' has $(@($case.steps).Count) steps, expected at least 2" }
            if (@($case.assertions).Count -lt 1) { throw "case '$($case.title)' has no assertions" }
            if (@($case.sources | Where-Object { [string]$_.refKind -eq "requirement" }).Count -lt 1) {
                throw "case '$($case.title)' has no clarified requirement source"
            }
            if ([string]$case.source -ne "ai_generated") { throw "case '$($case.title)' source is '$($case.source)', expected ai_generated" }
        }
        $script:state.CaseIds = @($cases | ForEach-Object { [string]$_.id })
        $script:resultData.caseCount = $cases.Count
    }

    Invoke-Check -Name "requirement coverage is explicit" -Body {
        $graph = Get-MindmapGraph -VersionId $script:state.VersionId -Headers $adminHeaders
        $coverage = $graph.coverage
        $covered = @()
        foreach ($cell in @($coverage.matrix)) {
            if (@($cell.caseIds).Count -gt 0) { $covered += [string]$cell.requirementId }
        }
        if ($covered.Count -lt 2) { throw "only $($covered.Count) of 3 requirements are covered, expected at least 2" }
        $expectedUncovered = @($script:state.RequirementIds | Where-Object { $covered -notcontains [string]$_ })
        $actualUncovered = @($coverage.uncoveredRequirementIds | ForEach-Object { [string]$_ })
        foreach ($id in $expectedUncovered) {
            if ($actualUncovered -notcontains $id) { throw "uncovered requirement $id is not listed in coverage.uncoveredRequirementIds" }
        }
        $script:resultData.coveredCount = $covered.Count
    }

    Invoke-Check -Name "nodes carry real code anchors" -Body {
        $graph = Get-MindmapGraph -VersionId $script:state.VersionId -Headers $adminHeaders
        $anchors = @()
        foreach ($node in @($graph.nodes)) { $anchors += @($node.anchors) }
        $anchors = @($anchors | Where-Object { $null -ne $_ })
        $script:resultData.anchorCount = $anchors.Count
        if ($anchors.Count -eq 0) {
            $run = Invoke-Api -Method Get -Uri "$apiBaseUrl/api/versions/$($script:state.VersionId)/mindmap/runs/$($script:state.DesignRunId)" -Headers $adminHeaders
            $dropped = $null
            if ($null -ne $run.Data.stats) { $dropped = $run.Data.stats.anchorsDropped }
            if ($null -eq $dropped -or [int]$dropped -le 0) {
                throw "no anchor was materialized and the run does not report anchorsDropped; the anchor outcome is unexplained"
            }
            Write-Host "No anchor materialized; run explains it with stats.anchorsDropped=$dropped."
            [void]$script:warnings.Add("design run materialized zero anchors (anchorsDropped=$dropped)")
            return
        }
        $valid = 0
        foreach ($anchor in $anchors) {
            $path = [string]$anchor.filePath
            if ([string]::IsNullOrWhiteSpace($path)) { continue }
            $previous = $ErrorActionPreference
            $ErrorActionPreference = "SilentlyContinue"
            try {
                & $gitExe -C $script:state.FixtureRepo cat-file -e ("{0}:{1}" -f $script:state.FixtureBaseCommit, $path) 2>$null | Out-Null
                $exists = $LASTEXITCODE -eq 0
            } finally {
                $ErrorActionPreference = $previous
            }
            if ($exists) { $valid++ }
        }
        if ($valid -lt 1) { throw "none of the $($anchors.Count) anchors points at a file that exists at baseline $($script:state.FixtureBaseCommit.Substring(0, 12))" }
    }

    Invoke-Check -Name "real review run produces coverage findings" -Body {
        $start = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$($script:state.VersionId)/mindmap/runs" -Headers $adminHeaders -Body @{ kind = "review" }
        if ($start.Status -ne 202) { throw "POST review run returned HTTP $($start.Status)" }
        $runId = [string]$start.Data.runId
        $script:state.ReviewRunId = $runId
        $script:resultData.reviewRunId = $runId
        $run = Wait-Run -VersionId $script:state.VersionId -RunId $runId -Headers $adminHeaders
        if ([string]$run.status -ne "done") { throw "review run ended '$($run.status)' reason='$($run.reason)'; expected done" }
        $findings = Invoke-Api -Method Get -Uri "$apiBaseUrl/api/versions/$($script:state.VersionId)/mindmap/findings" -Headers $adminHeaders
        if ($findings.Status -ne 200) { throw "GET findings returned HTTP $($findings.Status)" }
        $items = @($findings.Data)
        if ($items.Count -lt 1) { throw "review run produced no findings" }
        $categories = @($items | ForEach-Object { [string]$_.category })
        if (-not ($categories -contains "coverage" -or $categories -contains "code_uncovered")) {
            throw "review findings do not include a coverage or code_uncovered category"
        }
        $script:state.FindingCount = $items.Count
        $script:state.BlockerCount = @($items | Where-Object { [string]$_.severity -eq "blocker" -and [string]$_.status -eq "open" }).Count
        $script:resultData.findingCount = $items.Count
        $script:resultData.blockerCount = $script:state.BlockerCount
    }

    Invoke-Check -Name "manual edit persists and stale revision is 409" -Body {
        $graph = Get-MindmapGraph -VersionId $script:state.VersionId -Headers $adminHeaders
        $case = @($graph.nodes | Where-Object { [string]$_.id -eq [string]$script:state.CaseIds[0] })[0]
        if ($null -eq $case) { throw "the first generated case disappeared" }
        $title = "P5 real acceptance edited case"
        $patched = Invoke-Api -Method Patch -Uri "$apiBaseUrl/api/versions/$($script:state.VersionId)/mindmap/nodes/$($case.id)" -Headers $adminHeaders -Body @{
            title    = $title
            revision = [int]$case.revision
        }
        if ($patched.Status -ne 200) { throw "PATCH node returned HTTP $($patched.Status)" }
        $step = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$($script:state.VersionId)/mindmap/nodes/$($case.id)/steps" -Headers $adminHeaders -Body @{
            action   = "Manual acceptance step"
            expected = "The manual step is persisted"
        }
        if ($step.Status -ne 201) { throw "POST manual step returned HTTP $($step.Status)" }
        $assertion = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$($script:state.VersionId)/mindmap/nodes/$($case.id)/assertions" -Headers $adminHeaders -Body @{
            stepId     = [string]$step.Data.id
            kind       = "text"
            target     = "body"
            expression = "Manual acceptance assertion"
        }
        if ($assertion.Status -ne 201) { throw "POST manual assertion returned HTTP $($assertion.Status)" }
        $reloaded = Get-MindmapGraph -VersionId $script:state.VersionId -Headers $adminHeaders
        $reloadedCase = @($reloaded.nodes | Where-Object { [string]$_.id -eq [string]$case.id })[0]
        if ([string]$reloadedCase.title -ne $title) { throw "the edited title did not survive a graph reload" }
        if (@($reloadedCase.steps | Where-Object { [string]$_.action -eq "Manual acceptance step" }).Count -lt 1) {
            throw "the added step did not survive a graph reload"
        }
        Assert-ApiError -Label "stale revision edit" -Call {
            Invoke-Api -Method Patch -Uri "$apiBaseUrl/api/versions/$($script:state.VersionId)/mindmap/nodes/$($case.id)" -Headers $adminHeaders -Body @{
                title    = "stale"
                revision = ([int]$reloadedCase.revision + 1)
            }
        } -Status 409 -Reason "case_revision_conflict" | Out-Null
    }

    Invoke-Check -Name "real data-plan run supplements cases" -Body {
        $graph = Get-MindmapGraph -VersionId $script:state.VersionId -Headers $adminHeaders
        $cases = @($graph.nodes | Where-Object { [string]$_.kind -eq "case" })
        $before = @{}
        foreach ($case in $cases) { $before[[string]$case.id] = ("{0}|{1}" -f [string]$case.preconditions, [string](ConvertTo-Json -InputObject $case.testData -Compress -Depth 6)) }
        $nodeIds = @($cases | Select-Object -First 60 | ForEach-Object { [string]$_.id })
        $start = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$($script:state.VersionId)/mindmap/runs" -Headers $adminHeaders -Body @{
            kind    = "data_plan"
            nodeIds = $nodeIds
        }
        if ($start.Status -ne 202) { throw "POST data_plan run returned HTTP $($start.Status)" }
        $runId = [string]$start.Data.runId
        $script:state.DataPlanRunId = $runId
        $script:resultData.dataPlanRunId = $runId
        $run = Wait-Run -VersionId $script:state.VersionId -RunId $runId -Headers $adminHeaders
        if ([string]$run.status -ne "done") { throw "data_plan run ended '$($run.status)' reason='$($run.reason)'; expected done" }
        $after = Get-MindmapGraph -VersionId $script:state.VersionId -Headers $adminHeaders
        $changed = 0
        foreach ($case in @($after.nodes | Where-Object { [string]$_.kind -eq "case" })) {
            $key = [string]$case.id
            if (-not $before.ContainsKey($key)) { continue }
            $currentValue = ("{0}|{1}" -f [string]$case.preconditions, [string](ConvertTo-Json -InputObject $case.testData -Compress -Depth 6))
            if ($currentValue -ne $before[$key]) { $changed++ }
        }
        if ($changed -lt 1) { throw "data_plan run completed but no case preconditions/test data changed" }
    }

    Invoke-Check -Name "reject returns the version to designing" -Body {
        $start = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$($script:state.VersionId)/mindmap/runs" -Headers $adminHeaders -Body @{ kind = "review" }
        if ($start.Status -ne 202) { throw "POST review run for reject returned HTTP $($start.Status)" }
        $runId = [string]$start.Data.runId
        $run = Wait-Run -VersionId $script:state.VersionId -RunId $runId -Headers $adminHeaders
        if ([string]$run.status -ne "done") { throw "review run for reject ended '$($run.status)'" }
        $reject = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$($script:state.VersionId)/mindmap/review" -Headers $adminHeaders -Body @{
            decision = "reject"
            comment  = "P5 real-LLM acceptance reject round"
        }
        if ($reject.Status -ne 200) { throw "POST review reject returned HTTP $($reject.Status)" }
        if ([string]$reject.Data.version.status -ne "designing") { throw "reject left the version in '$($reject.Data.version.status)'" }
        if ($null -ne $reject.Data.baseline) { throw "reject unexpectedly created a baseline" }
        $graph = Get-MindmapGraph -VersionId $script:state.VersionId -Headers $adminHeaders
        if ([bool]$graph.editable -ne $true) { throw "the graph is not editable after reject" }
    }

    Invoke-Check -Name "exploration imports app model and enforces single-flight" -Body {
        $fixture = $null
        $listeners = Get-NetTCPConnection -State Listen -LocalPort $fixturePort -ErrorAction SilentlyContinue
        if (-not $listeners) {
            $fixtureOut = Join-Path $tmpDir "acceptance-mindmap-llm-fixture-target.out.log"
            $fixtureErr = Join-Path $tmpDir "acceptance-mindmap-llm-fixture-target.err.log"
            $env:FIXTURE_PORT = "$fixturePort"
            $fixture = Start-Process -FilePath $nodeExe -WorkingDirectory $repoRoot `
                -ArgumentList "`"scripts/fixture-target.cjs`"" `
                -RedirectStandardOutput $fixtureOut -RedirectStandardError $fixtureErr -WindowStyle Hidden -PassThru
            $fixtureProcess = $fixture
            Wait-ForHttp -Url "$fixtureBaseUrl/health" -TimeoutSeconds 15 -Process $fixture -Label "fixture target"
        }
        try {
            $first = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$($script:state.VersionId)/mindmap/runs" -Headers $adminHeaders -Body @{
                kind          = "explore"
                systemId      = [int64]$script:state.SystemId
                environmentId = [int64]$script:state.EnvironmentId
                startPaths    = @("http://127.0.0.1:13335/p5/slow")
            }
            if ($first.Status -ne 202) { throw "POST explore run returned HTTP $($first.Status)" }
            $runId = [string]$first.Data.runId
            $script:state.ExploreRunId = $runId
            $script:resultData.exploreRunId = $runId
            Assert-ApiError -Label "concurrent explore run" -Call {
                Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$($script:state.VersionId)/mindmap/runs" -Headers $adminHeaders -Body @{
                    kind          = "explore"
                    systemId      = [int64]$script:state.SystemId
                    environmentId = [int64]$script:state.EnvironmentId
                }
            } -Status 409 -Reason "run_in_progress" | Out-Null
            $script:state.RunConcurrency409 = $true
            $script:resultData.runConcurrency409 = $true
            $run = Wait-Run -VersionId $script:state.VersionId -RunId $runId -Headers $adminHeaders
            if (@("done", "degraded") -notcontains [string]$run.status) { throw "explore run ended '$($run.status)' reason='$($run.reason)'" }
            $models = Invoke-Api -Method Get -Uri "$apiBaseUrl/api/resources/systems/$($script:state.SystemId)/app-models" -Headers $adminHeaders
            if ($models.Status -ne 200) { throw "GET app models returned HTTP $($models.Status)" }
            $explored = @($models.Data | Where-Object { [string]$_.source -eq "explored" })
            if ($explored.Count -lt 1) { throw "exploration imported no source=explored app model node" }
            foreach ($kind in @("page", "route", "api")) {
                if (@($explored | Where-Object { [string]$_.kind -eq $kind }).Count -lt 1) {
                    throw "exploration did not import a $kind app model node"
                }
            }
        } finally {
            Stop-ProcessTree -Process $fixture
            $fixtureProcess = $null
        }
    }

    Invoke-Check -Name "submit-review and approve freeze the baseline" -Body {
        $submit = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$($script:state.VersionId)/mindmap/submit-review" -Headers $adminHeaders -Body @{}
        if ($submit.Status -ne 200) { throw "POST submit-review returned HTTP $($submit.Status)" }
        if ([string]$submit.Data.version.status -ne "reviewing") { throw "submit-review left the version in '$($submit.Data.version.status)'" }
        # Re-read the current findings: review materialization supersedes older
        # AI findings, so the approve gate must be judged from the latest set.
        $findings = Invoke-Api -Method Get -Uri "$apiBaseUrl/api/versions/$($script:state.VersionId)/mindmap/findings" -Headers $adminHeaders
        if ($findings.Status -ne 200) { throw "GET findings before approve returned HTTP $($findings.Status)" }
        $openBlockers = @($findings.Data | Where-Object { [string]$_.severity -eq "blocker" -and [string]$_.status -eq "open" })
        $script:state.BlockerCount = $openBlockers.Count
        $script:resultData.blockerCount = $openBlockers.Count
        if ($openBlockers.Count -gt 0) {
            Assert-ApiError -Label "approve with open blockers and no acknowledgement" -Call {
                Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$($script:state.VersionId)/mindmap/review" -Headers $adminHeaders -Body @{ decision = "approve" }
            } -Status 409 -Reason "blocker_findings_open" | Out-Null
        }
        $approve = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$($script:state.VersionId)/mindmap/review" -Headers $adminHeaders -Body @{
            decision            = "approve"
            acknowledgeFindings = $true
        }
        if ($approve.Status -ne 200) { throw "POST review approve returned HTTP $($approve.Status)" }
        if ([string]$approve.Data.version.status -ne "executing") { throw "approve left the version in '$($approve.Data.version.status)'" }
        if ($null -eq $approve.Data.baseline) { throw "approve did not return a baseline" }
        $baselineId = [string]$approve.Data.baseline.id
        $script:state.BaselineId = $baselineId
        $script:resultData.baselineId = $baselineId
        $graph = Get-MindmapGraph -VersionId $script:state.VersionId -Headers $adminHeaders
        if ($null -eq $graph.baseline) { throw "GET /mindmap does not expose the frozen baseline" }
        if ([bool]$graph.editable) { throw "the graph is still editable after approval" }
        foreach ($node in @($graph.nodes | Where-Object { [string]$_.kind -eq "case" })) {
            if ([string]$node.status -ne "reviewed") { throw "case '$($node.title)' status is '$($node.status)', expected reviewed" }
        }
        $case = @($graph.nodes | Where-Object { [string]$_.kind -eq "case" })[0]
        Assert-ApiError -Label "write after freeze" -Call {
            Invoke-Api -Method Patch -Uri "$apiBaseUrl/api/versions/$($script:state.VersionId)/mindmap/nodes/$($case.id)" -Headers $adminHeaders -Body @{
                title    = "must be rejected"
                revision = [int]$case.revision
            }
        } -Status 409 -Reason "baseline_frozen" | Out-Null
    }

    Invoke-Check -Name "three export formats with sha256, content and reuse" -Body {
        $graph = Get-MindmapGraph -VersionId $script:state.VersionId -Headers $adminHeaders
        $case = @($graph.nodes | Where-Object { [string]$_.kind -eq "case" })[0]
        $caseTitle = [string]$case.title
        $stepAction = [string]@($case.steps)[0].action
        $expectedContent = @{
            markdown = @($caseTitle, $stepAction, "| 需求 | 覆盖用例 |", "baseline-$($script:state.BaselineId)")
            mermaid  = @("mindmap", $caseTitle)
            opml     = @("<opml", $caseTitle, "_note")
        }
        $exportDir = Join-Path $tmpDir "p5-real-exports"
        foreach ($format in @("markdown", "mermaid", "opml")) {
            $export = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$($script:state.VersionId)/mindmap/export" -Headers $adminHeaders -Body @{ format = $format }
            if ($export.Status -ne 201 -and $export.Status -ne 200) { throw "POST export $format returned HTTP $($export.Status)" }
            $exportId = [string]$export.Data.id
            $download = Invoke-Download -Path "/api/versions/$($script:state.VersionId)/mindmap/exports/$exportId/download" -Headers $adminHeaders -OutFile (Join-Path $tmpDir ("p5-real-export-$format.bin"))
            if ($download.ContentDisposition -notmatch "attachment") { throw "export $format download is not an attachment" }
            $recordedHash = [string]$export.Data.sha256
            if ([string]::IsNullOrWhiteSpace($recordedHash)) {
                $recordedHash = Get-PsqlScalar -Database $e2eDbName -Sql "SELECT file_sha256 FROM case_export WHERE version_id = $($script:state.VersionId) AND id = $exportId;"
            }
            if ($recordedHash -ne $download.Sha256) { throw "export $format sha256 mismatch" }
            $storedPath = Get-PsqlScalar -Database $e2eDbName -Sql "SELECT file_path FROM case_export WHERE version_id = $($script:state.VersionId) AND id = $exportId;"
            if (-not (Test-Path -LiteralPath (Join-Path $exportDir ($storedPath -replace "/", "\")))) { throw "export $format file is missing under EZTEST_EXPORT_DIR" }
            $content = if (-not [string]::IsNullOrWhiteSpace($downloadPath) -and (Test-Path -LiteralPath $downloadPath)) { [System.IO.File]::ReadAllText($downloadPath, [System.Text.Encoding]::UTF8) } else { [System.Text.Encoding]::UTF8.GetString($download.Bytes) }
            foreach ($needle in $expectedContent[$format]) {
                if ($content -notmatch [regex]::Escape($needle)) { throw "export $format is missing '$needle'" }
            }
            $script:state.ExportIds[$format] = $exportId
        }
        $reused = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$($script:state.VersionId)/mindmap/export" -Headers $adminHeaders -Body @{ format = "markdown" }
        if ($reused.Status -ne 200) { throw "repeated frozen export returned HTTP $($reused.Status), expected 200 reuse" }
        if ([string]$reused.Data.id -ne [string]$script:state.ExportIds["markdown"]) { throw "repeated frozen export did not reuse the stored id" }
    }

    Invoke-Check -Name "events carry the mindmap lifecycle" -Body {
        $events = Invoke-Api -Method Get -Uri "$apiBaseUrl/api/versions/$($script:state.VersionId)/events?limit=100" -Headers $adminHeaders
        if ($events.Status -ne 200) { throw "GET version events returned HTTP $($events.Status)" }
        $items = @($events.Data)
        $types = @($items | ForEach-Object { [string]$_.type })
        foreach ($required in @("case.reviewed", "case.baseline_frozen", "case.export_ready")) {
            if ($types -notcontains $required) { throw "the event snapshot is missing $required" }
        }
        $previousSeq = -1
        foreach ($item in $items) {
            $seq = [int64]$item.seq
            if ($seq -le $previousSeq) { throw "event seq is not strictly increasing" }
            $previousSeq = $seq
            if ([string]$item.refs.case -match '[^0-9]') { throw "refs.case carries a non-decimal id" }
        }
    }

    Invoke-Check -Name "idempotency and repeated decisions" -Body {
        if (-not [bool]$script:state.RunConcurrency409) { throw "the explore run_in_progress 409 was not observed" }
        $reused = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$($script:state.VersionId)/mindmap/export" -Headers $adminHeaders -Body @{ format = "markdown" }
        if ($reused.Status -ne 200 -or [string]$reused.Data.id -ne [string]$script:state.ExportIds["markdown"]) {
            throw "the repeated markdown export did not reuse the same id"
        }
        Assert-ApiError -Label "repeated approval" -Call {
            Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$($script:state.VersionId)/mindmap/review" -Headers $adminHeaders -Body @{ decision = "approve"; acknowledgeFindings = $true }
        } -Status 409 -Reason "baseline_frozen" | Out-Null
    }

    Invoke-Check -Name "degradation: ai_unavailable and the manual path" -Body {
        $configs = Invoke-Api -Method Get -Uri "$apiBaseUrl/api/admin/llm/configs" -Headers $adminHeaders
        if ($configs.Status -ne 200) { throw "GET llm configs returned HTTP $($configs.Status)" }
        $defaultConfig = @($configs.Data | Where-Object { [bool]$_.is_default })[0]
        if ($null -ne $defaultConfig) {
            $delete = Invoke-Api -Method Delete -Uri "$apiBaseUrl/api/admin/llm/configs/$([string]$defaultConfig.id)" -Headers $adminHeaders
            if ($delete.Status -ne 204) { throw "DELETE default LLM config returned HTTP $($delete.Status)" }
        }
        $health = Invoke-Api -Method Get -Uri "$apiBaseUrl/api/health"
        if ([bool]$health.Raw.data.llm_configured) { throw "health still reports llm_configured=true after deleting the default config" }

        $degraded = New-DesigningVersion -Headers $adminHeaders -Name "P5 real-LLM degraded" -RequirementCount 1 `
            -SystemId $script:state.SystemId -EnvironmentId $script:state.EnvironmentId `
            -CodeRepositoryId $script:state.RepoId -CodeBranch $script:state.FixtureBranch -BaselineCommit $script:state.FixtureBaseCommit
        $syncUnavailable = $false
        try {
            $start = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$($degraded.VersionId)/mindmap/runs" -Headers $adminHeaders -Body @{ kind = "design" }
            if ($start.Status -ne 202) { throw "degraded design run returned HTTP $($start.Status), expected 202 or 409" }
            $run = Wait-Run -VersionId $degraded.VersionId -RunId ([string]$start.Data.runId) -Headers $adminHeaders -TimeoutSeconds 120
            if ([string]$run.status -ne "failed" -or [string]$run.reason -ne "ai_unavailable") {
                throw "degraded design run ended '$($run.status)'/'$($run.reason)', expected failed/ai_unavailable"
            }
        } catch {
            if ($_.Exception.Message -match "409" -and $_.Exception.Message -match "ai_unavailable") {
                # The runtime refuses to start without an LLM; documented synchronous failure.
                $syncUnavailable = $true
            } else { throw }
        }
        $manual = New-ManualCase -Headers $adminHeaders -VersionId $degraded.VersionId -RequirementIds @($degraded.RequirementIds[0]) -Prefix "P5 real degraded manual"
        $graph = Get-MindmapGraph -VersionId $degraded.VersionId -Headers $adminHeaders
        if (@($graph.nodes).Count -lt 2) { throw "manual scenario/case creation failed while the LLM was disabled" }
        $case = @($graph.nodes | Where-Object { [string]$_.id -eq $manual.CaseId })[0]
        $edit = Invoke-Api -Method Patch -Uri "$apiBaseUrl/api/versions/$($degraded.VersionId)/mindmap/nodes/$($manual.CaseId)" -Headers $adminHeaders -Body @{
            title    = "P5 real degraded manual edited"
            revision = [int]$case.revision
        }
        if ($edit.Status -ne 200) { throw "manual edit with the LLM disabled returned HTTP $($edit.Status)" }
    }
} catch {
    $script:resultData.error = Get-RedactedText -Text $_.Exception.Message
    Add-CheckResult -Name "harness" -Status "failed" -Detail $script:resultData.error
    Write-Host "REAL P5 MINDMAP LLM ACCEPTANCE FAILED: $($script:resultData.error)" -ForegroundColor Red
} finally {
    Stop-ProcessTree -Process $apiProcess
    Stop-ProcessTree -Process $fixtureProcess
    try {
        $failedChecks = @($script:checks | Where-Object { $_.status -eq "failed" })
        if ($failedChecks.Count -gt 0) {
            $script:resultData.status = "failed"
            $exitCode = 1
        } elseif ($null -ne $script:resultData.error) {
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
    Write-Step "Result summary"
    $passed = @($script:checks | Where-Object { $_.status -eq "passed" }).Count
    $failed = @($script:checks | Where-Object { $_.status -eq "failed" }).Count
    Write-Host ("P5 real-LLM acceptance: {0} passed, {1} failed" -f $passed, $failed)
    if ($exitCode -eq 0) {
        Write-Host "P5 REAL LLM ACCEPTANCE PASSED" -ForegroundColor Green
    } else {
        Write-Host "P5 REAL LLM ACCEPTANCE FAILED" -ForegroundColor Red
    }
}

exit $exitCode
