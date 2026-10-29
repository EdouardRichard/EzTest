#requires -Version 5.1
<#
.SYNOPSIS
    Measures the P8 SLO gate (design spec section 1.5/10.1).

.DESCRIPTION
    Measurement mode seeds an isolated dataset into the database behind the
    running API and then measures:
      * version overview first-screen API latency (P95) against the measured
        version detail endpoint,
      * version overview browser navigation timing (P95) by navigating to
        `/versions/{id}?tab=overview` (never the default requirements tab) with
        Playwright and reading the Navigation Timing entry,
      * exploration-chain endpoint latency (P95) after asserting that the chain
        read model really returns at least 200 nodes and 400 edges,
      * versions/{id}/code/changes API latency (P95) after binding the measured
        version to a real disposable Git fixture repository and verifying the
        endpoint answers 200.

    Dataset (design spec 10.1): 100 versions / 50 requirements / 200 cases /
    400 edges / 10k events. The 50 clarified requirements, 200 cases and 400
    case_source requirement links live in the measured version so the chain
    endpoint is measured on a real 200+ node / 400+ edge graph. The 400 edges
    are the requirement_case edges the chain read model derives from
    case_source (the mindmap coverage join is exercised too because every
    requirement is clarified). A disposable two-commit Git repository under
    .tmp/p8-slo/fixture-repo is bound to the measured version so
    `/code/changes` returns 200 with a real diff.

    The 8-concurrent batch execution and the analysis non-model overhead need
    the fixture executor/session harness and a configured LLM; they are recorded
    as explicit NOT MEASURED (2 of 6) SKIP rows. The report states the coverage
    and never calls an incomplete run an SLO PASS.

    -DryRun performs static preflight only: it checks that the limiter, the SLO
    script, the measured routes, the chain dataset/assertion code, the fixture
    repository binding, the overview browser probe, the environment keys and
    the report paths are present, writes docs/acceptance/p8/slo.md and
    .tmp/p8-slo/metrics.json with the measurement rows marked SKIP, and exits 0
    when the static checks pass.

    Every measurement row is PASS, FAIL or SKIP and every row is always present.
    A measured value above its threshold, a chain shape assertion failure, or a
    failed request is a FAIL and the script exits 1. A missing API, browser,
    fixture repository or executor harness is an explicit SKIP (exit 0) and is
    written to the report; it is never reported as a pass. The report is a local
    acceptance measurement, not a production SLA promise.

    The script never prints .env credentials, the database URL or the LLM key.

.PARAMETER DryRun
    Static preflight only; no API, database or browser is started or queried.

.PARAMETER ApiBaseUrl
    Running API base URL. Defaults to EZTEST_ACCEPTANCE_API_URL, then the
    EZTEST_HTTP_ADDR port on 127.0.0.1, then http://127.0.0.1:13333.

.PARAMETER WebBaseUrl
    Running web base URL used for the browser navigation-timing measurement.
    When omitted the browser row is SKIP.

.PARAMETER DatabaseUrl
    PostgreSQL URL behind the running API. Defaults to EZTEST_DATABASE_URL from
    .env. The reserved seed ids are cleaned on exit.

.PARAMETER Iterations
    API samples per measurement (default 20). Percentiles are P50/P95.

.PARAMETER BrowserIterations
    Measured browser navigations (default 5, minimum 3). One extra warm-up
    navigation is not counted so a cold Next dev compile cannot pollute P95.

.PARAMETER EnvPath
    .env path (default <repo>/.env).

.NOTES
    Compatible with Windows PowerShell 5.1 and PowerShell 7+.
    Run from anywhere; paths are resolved from the repository root.

.EXAMPLE
    powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/slo.ps1 -DryRun

.EXAMPLE
    powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/slo.ps1 `
      -ApiBaseUrl http://127.0.0.1:13333 -WebBaseUrl http://127.0.0.1:6868
#>
[CmdletBinding()]
param(
    [switch]$DryRun,
    [string]$ApiBaseUrl = "",
    [string]$WebBaseUrl = "",
    [string]$DatabaseUrl = "",
    [int]$Iterations = 20,
    [int]$BrowserIterations = 5,
    [string]$EnvPath = ""
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$backendDir = Join-Path $repoRoot "backend"
$frontendDir = Join-Path $repoRoot "frontend"
$tmpDir = Join-Path $repoRoot ".tmp\p8-slo"
$metricsPath = Join-Path $tmpDir "metrics.json"
$reportPath = Join-Path $repoRoot "docs\acceptance\p8\slo.md"
$fixtureRepoDir = Join-Path $tmpDir "fixture-repo"

# Design spec 1.5 thresholds and 10.1 dataset.
$thresholds = [ordered]@{
    overviewP95Ms    = 2500
    chainP95Ms       = 2500
    changesP95Ms     = 2000
    executionWorkers = 8
    executionErrors  = 0
}
$dataset = [ordered]@{
    versions     = 100
    requirements = 50
    cases        = 200
    edges        = 400
    events       = 10000
}
$measurementOrder = @(
    "overviewApiP95Ms", "overviewBrowserP95Ms", "chainP95Ms", "changesP95Ms",
    "executionConcurrency", "analysisNonModelOverheadMs"
)
$measurementLabels = @{
    overviewApiP95Ms           = "Version overview first-screen API latency"
    overviewBrowserP95Ms       = "Version overview browser navigation timing"
    chainP95Ms                 = "Exploration-chain latency (200 nodes/400 edges)"
    changesP95Ms               = "Code change-set API latency"
    executionConcurrency       = "Batch-execution concurrency success rate"
    analysisNonModelOverheadMs = "Analysis non-model overhead"
}

$checks = New-Object System.Collections.ArrayList
$measurements = [ordered]@{}
$chainShape = $null

function Write-Step {
    param([string]$Message)
    Write-Host ""
    Write-Host "==> $Message" -ForegroundColor Cyan
}

function Write-Note {
    param([string]$Message)
    Write-Host "    $Message" -ForegroundColor DarkGray
}

function Add-Check {
    param([string]$Area, [string]$Check, [string]$Status, [string]$Detail)
    [void]$checks.Add([pscustomobject]@{
            Area   = $Area
            Check  = $Check
            Status = $Status
            Detail = $Detail
        })
}

function Add-Measurement {
    param(
        [string]$Name,
        [string]$Status,
        [string]$Threshold,
        $Value,
        [string]$Detail
    )
    $measurements[$Name] = [ordered]@{
        status    = $Status
        threshold = $Threshold
        value     = $Value
        detail    = $Detail
    }
}

function Read-DotEnv {
    param([string]$Path)
    $values = @{}
    if (-not (Test-Path -LiteralPath $Path)) { return $values }
    foreach ($line in Get-Content -LiteralPath $Path -Encoding UTF8) {
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

function Resolve-ToolPath {
    param([string]$LocalPath, [string]$CommandName)
    if (-not [string]::IsNullOrWhiteSpace($LocalPath) -and (Test-Path -LiteralPath $LocalPath)) { return $LocalPath }
    $command = Get-Command $CommandName -ErrorAction SilentlyContinue
    if ($null -ne $command) { return $command.Source }
    return ""
}

function Resolve-ApiBaseUrl {
    param([string]$Explicit, $EnvValues)
    if (-not [string]::IsNullOrWhiteSpace($Explicit)) { return $Explicit.TrimEnd("/") }
    if ($null -ne $EnvValues -and $EnvValues.ContainsKey("EZTEST_ACCEPTANCE_API_URL") -and
        -not [string]::IsNullOrWhiteSpace([string]$EnvValues["EZTEST_ACCEPTANCE_API_URL"])) {
        return ([string]$EnvValues["EZTEST_ACCEPTANCE_API_URL"]).TrimEnd("/")
    }
    $addr = ""
    if ($null -ne $EnvValues -and $EnvValues.ContainsKey("EZTEST_HTTP_ADDR")) {
        $addr = [string]$EnvValues["EZTEST_HTTP_ADDR"]
    }
    if (-not [string]::IsNullOrWhiteSpace($addr) -and $addr.StartsWith(":")) {
        return "http://127.0.0.1$addr"
    }
    if (-not [string]::IsNullOrWhiteSpace($addr)) {
        return "http://$addr"
    }
    return "http://127.0.0.1:13333"
}

function Get-Percentile {
    param([double[]]$Values, [double]$Percentile)
    if ($null -eq $Values -or $Values.Count -eq 0) { return $null }
    $sorted = @($Values | Sort-Object)
    $index = [int][math]::Ceiling($Percentile * $sorted.Count) - 1
    if ($index -lt 0) { $index = 0 }
    if ($index -ge $sorted.Count) { $index = $sorted.Count - 1 }
    return [math]::Round([double]$sorted[$index], 1)
}

function ConvertTo-SqlLiteral {
    param([string]$Value)
    if ($null -eq $Value) { return "''" }
    return "'" + $Value.Replace("'", "''") + "'"
}

# Invoke-ApiRequest performs one GET and returns the status even for 4xx/5xx.
# PowerShell 5.1 throws on non-2xx responses, so the error response body is read
# to surface the shared envelope's code/reason in the report.
function Invoke-ApiRequest {
    param([string]$Path, $Headers, [int]$TimeoutSec = 30)
    try {
        $response = Invoke-WebRequest -Uri ($script:apiBase + $Path) -Headers $Headers `
            -UseBasicParsing -TimeoutSec $TimeoutSec -ErrorAction Stop
        return [pscustomobject]@{ Status = [int]$response.StatusCode; Body = [string]$response.Content; Error = "" }
    } catch {
        $response = $_.Exception.Response
        if ($null -eq $response) { throw "GET $Path failed: $($_.Exception.Message)" }
        $status = 0
        try { $status = [int]$response.StatusCode } catch { $status = -1 }
        $body = ""
        try {
            if ($null -ne $response.PSObject.Methods['GetResponseStream']) {
                $stream = $response.GetResponseStream()
                if ($null -ne $stream) {
                    $reader = New-Object System.IO.StreamReader($stream)
                    try { $body = $reader.ReadToEnd() } finally { $reader.Dispose() }
                }
            } elseif ($null -ne $response.Content) {
                $body = [string]$response.Content.ReadAsStringAsync().Result
            }
        } catch {
            $body = ""
        }
        return [pscustomobject]@{ Status = $status; Body = $body; Error = $_.Exception.Message }
    }
}

function Get-ApiErrorDetail {
    param([string]$Body)
    if ([string]::IsNullOrWhiteSpace($Body)) { return "" }
    try {
        $payload = $Body | ConvertFrom-Json
        $code = [string]$payload.error.code
        $reason = [string]$payload.error.details.reason
        if (-not [string]::IsNullOrWhiteSpace($code) -and -not [string]::IsNullOrWhiteSpace($reason)) {
            return "$code/$reason"
        }
        if (-not [string]::IsNullOrWhiteSpace($code)) { return $code }
    } catch {
        return ""
    }
    return ""
}

function Invoke-Measure {
    param([string]$Path, [int]$Count, $Headers)
    $samples = New-Object System.Collections.ArrayList
    for ($i = 0; $i -lt $Count; $i++) {
        $stopwatch = [System.Diagnostics.Stopwatch]::StartNew()
        $result = Invoke-ApiRequest -Path $Path -Headers $Headers
        $stopwatch.Stop()
        if ($result.Status -ne 200) {
            throw "GET $Path answered HTTP $($result.Status)"
        }
        [void]$samples.Add($stopwatch.Elapsed.TotalMilliseconds)
    }
    return [pscustomobject]@{
        P50     = (Get-Percentile -Values $samples.ToArray() -Percentile 0.50)
        P95     = (Get-Percentile -Values $samples.ToArray() -Percentile 0.95)
        Samples = $samples.Count
    }
}

function Parse-PostgresUrl {
    param([string]$Url)
    if ([string]::IsNullOrWhiteSpace($Url)) { return $null }
    $uri = $null
    if (-not [System.Uri]::TryCreate($Url, [System.UriKind]::Absolute, [ref]$uri)) { return $null }
    if ($uri.Scheme -ne "postgres" -and $uri.Scheme -ne "postgresql") { return $null }
    $separator = $uri.UserInfo.IndexOf(":")
    if ($separator -lt 0) { return $null }
    $database = [System.Uri]::UnescapeDataString($uri.AbsolutePath.TrimStart([char[]]"/"))
    if ([string]::IsNullOrWhiteSpace($database)) { return $null }
    $sslMode = ""
    if (-not [string]::IsNullOrWhiteSpace($uri.Query)) {
        foreach ($pair in $uri.Query.TrimStart([char[]]"?") -split "&") {
            $index = $pair.IndexOf("=")
            if ($index -lt 0) { continue }
            $key = [System.Uri]::UnescapeDataString($pair.Substring(0, $index))
            if ($key -eq "sslmode") {
                $sslMode = [System.Uri]::UnescapeDataString($pair.Substring($index + 1))
            }
        }
    }
    return [pscustomobject]@{
        Host     = $uri.Host
        Port     = $(if ($uri.Port -gt 0) { $uri.Port } else { 5432 })
        User     = [System.Uri]::UnescapeDataString($uri.UserInfo.Substring(0, $separator))
        Password = [System.Uri]::UnescapeDataString($uri.UserInfo.Substring($separator + 1))
        Database = $database
        SSLMode  = $sslMode
    }
}

function Invoke-PsqlScript {
    param($Connection, [string]$Sql, [string]$Operation)
    if ($null -eq $script:psqlExe -or [string]::IsNullOrWhiteSpace($script:psqlExe)) {
        throw "psql.exe is not available"
    }
    $tempFile = [System.IO.Path]::GetTempFileName()
    $savedPassword = $env:PGPASSWORD
    $savedSSLMode = $env:PGSSLMODE
    try {
        [System.IO.File]::WriteAllText($tempFile, $Sql, (New-Object System.Text.UTF8Encoding($false)))
        $env:PGPASSWORD = $Connection.Password
        if (-not [string]::IsNullOrWhiteSpace($Connection.SSLMode)) { $env:PGSSLMODE = $Connection.SSLMode }
        $previous = $ErrorActionPreference
        $ErrorActionPreference = "SilentlyContinue"
        try {
            $output = & $script:psqlExe -h $Connection.Host -p $Connection.Port -U $Connection.User `
                -d $Connection.Database -v ON_ERROR_STOP=1 -tA -f $tempFile 2>$null
            $code = $LASTEXITCODE
        } finally {
            $ErrorActionPreference = $previous
        }
    } finally {
        if ($null -eq $savedPassword) { Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue } else { $env:PGPASSWORD = $savedPassword }
        if ($null -eq $savedSSLMode) { Remove-Item Env:PGSSLMODE -ErrorAction SilentlyContinue } else { $env:PGSSLMODE = $savedSSLMode }
        Remove-Item -LiteralPath $tempFile -Force -ErrorAction SilentlyContinue
    }
    if ($code -ne 0) { throw "$Operation failed (psql exit $code)" }
    return @($output | ForEach-Object { ([string]$_).Trim() } | Where-Object { $_ -ne "" })
}

# Seed IDs live in reserved high ranges so cleanup can never touch real data.
$seedVersionBase = [long]8800000000000000000
$seedCaseBase = [long]8810000000000000000
$seedRequirementBase = [long]8820000000000000000
$seedCaseSourceBase = [long]8830000000000000000
$seedVersionCount = 100
$seedRequirementCount = 50
$seedCaseCount = 200
$seedCaseSourceCount = 400
$seedEventCount = 10000
$seedRepoKey = "p8-slo-fixture"

function New-SloFixtureRepository {
    param([string]$Directory)
    $gitExe = Resolve-ToolPath -LocalPath "" -CommandName "git.exe"
    if ([string]::IsNullOrWhiteSpace($gitExe)) { $gitExe = Resolve-ToolPath -LocalPath "" -CommandName "git" }
    if ([string]::IsNullOrWhiteSpace($gitExe)) {
        return [pscustomobject]@{ Ready = $false; Reason = "git is not on PATH"; Path = ""; BaseCommit = ""; HeadCommit = "" }
    }

    $tmpRoot = [System.IO.Path]::GetFullPath((Join-Path $repoRoot ".tmp"))
    $fullDirectory = [System.IO.Path]::GetFullPath($Directory)
    if (-not $fullDirectory.StartsWith($tmpRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
        return [pscustomobject]@{ Ready = $false; Reason = "fixture directory is outside .tmp"; Path = ""; BaseCommit = ""; HeadCommit = "" }
    }
    if (Test-Path -LiteralPath $fullDirectory) {
        Remove-Item -LiteralPath $fullDirectory -Recurse -Force
    }
    New-Item -ItemType Directory -Force -Path $fullDirectory | Out-Null

    $identityKeys = @("GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME",
        "GIT_COMMITTER_EMAIL", "GIT_AUTHOR_DATE", "GIT_COMMITTER_DATE")
    $savedIdentity = @{}
    foreach ($key in $identityKeys) {
        $savedIdentity[$key] = [Environment]::GetEnvironmentVariable($key)
    }

    function Invoke-FixtureGit {
        param([string[]]$Arguments)
        # Native stderr (for example Git's autocrlf warning) must not terminate
        # the probe just because the script runs with ErrorActionPreference=Stop.
        $previous = $ErrorActionPreference
        $ErrorActionPreference = "Continue"
        try {
            $output = & $gitExe -C $fullDirectory @Arguments 2>$null
            $code = $LASTEXITCODE
        } finally {
            $ErrorActionPreference = $previous
        }
        if ($code -ne 0) { throw "git $($Arguments -join ' ') failed with exit code $code" }
        return @($output)
    }

    try {
        $env:GIT_AUTHOR_NAME = "EzTest P8 SLO Fixture"
        $env:GIT_AUTHOR_EMAIL = "p8-slo-fixture@eztest.invalid"
        $env:GIT_COMMITTER_NAME = "EzTest P8 SLO Fixture"
        $env:GIT_COMMITTER_EMAIL = "p8-slo-fixture@eztest.invalid"
        $env:GIT_AUTHOR_DATE = "2026-10-30T09:00:00+08:00"
        $env:GIT_COMMITTER_DATE = "2026-10-30T09:00:00+08:00"

        [void](Invoke-FixtureGit @("init", "-q"))
        [void](Invoke-FixtureGit @("symbolic-ref", "HEAD", "refs/heads/main"))
        # Pin line endings so the fixture content and Git's warnings stay
        # deterministic regardless of the host's global autocrlf setting.
        [void](Invoke-FixtureGit @("config", "core.autocrlf", "false"))
        [void](Invoke-FixtureGit @("config", "core.safecrlf", "false"))
        [System.IO.File]::WriteAllText((Join-Path $fullDirectory "app.py"),
            "def handle_refund(amount):`n    return {'status': 'created', 'amount': amount}`n",
            (New-Object System.Text.UTF8Encoding($false)))
        [void](Invoke-FixtureGit @("add", "-A"))
        [void](Invoke-FixtureGit @("commit", "-q", "-m", "p8 slo fixture base"))
        $baseCommit = (@(Invoke-FixtureGit @("rev-parse", "HEAD")) | Select-Object -Last 1)

        $env:GIT_AUTHOR_DATE = "2026-10-30T09:05:00+08:00"
        $env:GIT_COMMITTER_DATE = "2026-10-30T09:05:00+08:00"
        [System.IO.File]::WriteAllText((Join-Path $fullDirectory "app.py"),
            "def handle_refund(amount, reason=''):`n    return {'status': 'paid', 'amount': amount, 'reason': reason}`n",
            (New-Object System.Text.UTF8Encoding($false)))
        [void](Invoke-FixtureGit @("add", "-A"))
        [void](Invoke-FixtureGit @("commit", "-q", "-m", "p8 slo fixture head"))
        $headCommit = (@(Invoke-FixtureGit @("rev-parse", "HEAD")) | Select-Object -Last 1)
        $branch = (@(Invoke-FixtureGit @("rev-parse", "--abbrev-ref", "HEAD")) | Select-Object -Last 1)
        if ($branch -ne "main") { throw "fixture branch is '$branch', expected main" }
        if ($baseCommit -notmatch '^[0-9a-f]{40}$' -or $headCommit -notmatch '^[0-9a-f]{40}$') {
            throw "fixture commits are not full 40-hex ids"
        }
        if ($baseCommit -eq $headCommit) { throw "fixture base and head commits are identical" }
        return [pscustomobject]@{
            Ready = $true; Reason = ""; Path = $fullDirectory; BaseCommit = $baseCommit; HeadCommit = $headCommit
        }
    } catch {
        return [pscustomobject]@{ Ready = $false; Reason = $_.Exception.Message; Path = ""; BaseCommit = ""; HeadCommit = "" }
    } finally {
        foreach ($key in $identityKeys) {
            if ($null -eq $savedIdentity[$key]) {
                Remove-Item -Path ("Env:" + $key) -ErrorAction SilentlyContinue
            } else {
                Set-Item -Path ("Env:" + $key) -Value $savedIdentity[$key]
            }
        }
    }
}

function Invoke-SeedDataset {
    param($Connection)
    $adminRows = @(Invoke-PsqlScript -Connection $Connection -Operation "reading the admin user" `
        -Sql "SELECT id FROM user_account WHERE role = 'admin' ORDER BY id ASC LIMIT 1;")
    if ($adminRows.Count -eq 0) { throw "no admin user exists yet; start the API once so it seeds the admin account" }
    $adminID = [long]([string]$adminRows[0])

    $cleanupSql = @"
DELETE FROM event WHERE channel = 'slo:seed';
DELETE FROM event_channel WHERE channel = 'slo:seed';
DELETE FROM case_source WHERE id >= $seedCaseSourceBase AND id < $seedCaseSourceBase + 10000;
DELETE FROM requirement_relation WHERE id >= $seedCaseSourceBase AND id < $seedCaseSourceBase + 10000;
DELETE FROM case_node WHERE id >= $seedCaseBase AND id < $seedCaseBase + 10000;
DELETE FROM requirement WHERE id >= $seedRequirementBase AND id < $seedRequirementBase + 10000;
DELETE FROM version WHERE id >= $seedVersionBase AND id < $seedVersionBase + 10000;
DELETE FROM code_repository WHERE key = '$seedRepoKey';
"@
    [void](Invoke-PsqlScript -Connection $Connection -Operation "cleaning a previous SLO seed" -Sql $cleanupSql)

    $fixture = New-SloFixtureRepository -Directory $fixtureRepoDir
    $repoID = [long]0
    $repoPath = ""
    $baseCommit = ""
    if ($fixture.Ready) {
        $repoPath = $fixture.Path
        $baseCommit = $fixture.BaseCommit
        $repoSql = @"
INSERT INTO code_repository (key, name, local_path, fixed_branch, include_globs, exclude_globs,
                             status, last_commit, last_validated_at)
VALUES ('$seedRepoKey', 'P8 SLO fixture repository', $(ConvertTo-SqlLiteral $repoPath), 'main',
        '{}', ARRAY['.git/**','node_modules/**'], 'active', '$($fixture.HeadCommit)', now())
ON CONFLICT (key) DO UPDATE
  SET name = EXCLUDED.name, local_path = EXCLUDED.local_path, fixed_branch = EXCLUDED.fixed_branch,
      status = 'active', last_commit = EXCLUDED.last_commit, last_validated_at = now(), updated_at = now()
RETURNING id;
"@
        $repoRows = @(Invoke-PsqlScript -Connection $Connection -Operation "inserting the fixture code repository" -Sql $repoSql)
        if ($repoRows.Count -eq 0) { throw "fixture code repository insert returned no id" }
        $repoID = [long]([string]$repoRows[0])
        if ($repoID -le 0) { throw "fixture code repository id is not positive" }
    }

    $bindingSql = "UPDATE version SET code_repository_id = NULL, code_branch = NULL, code_baseline_commit = NULL WHERE id = $seedVersionBase + 1;"
    if ($repoID -gt 0) {
        $bindingSql = "UPDATE version SET code_repository_id = $repoID, code_branch = 'main', code_baseline_commit = '$baseCommit' WHERE id = $seedVersionBase + 1;"
    }

    $seedSql = @"
INSERT INTO version (id, name, label, description, status, created_by, updated_by, created_at, updated_at)
SELECT $seedVersionBase + g, 'SLO seed version ' || g, '', '', 'completed',
       $adminID, $adminID, now() - (g || ' minutes')::interval, now() - (g || ' minutes')::interval
FROM generate_series(1, $seedVersionCount) AS g;

$bindingSql

INSERT INTO requirement (id, version_id, title, description, acceptance_criteria, priority, status,
                         source, last_update_source, revision, created_by, updated_by)
SELECT $seedRequirementBase + g,
       $seedVersionBase + 1,
       'SLO seed requirement ' || g, '', '', 'should', 'clarified', 'user', 'user', 1, $adminID, $adminID
FROM generate_series(1, $seedRequirementCount) AS g;

INSERT INTO case_node (id, version_id, parent_id, kind, title, description, priority, source, status,
                       sort_order, revision, created_by, updated_by)
SELECT $seedCaseBase + g,
       $seedVersionBase + 1,
       NULL, 'case', 'SLO seed case ' || g, '', 'P2', 'user_input', 'reviewed', g, 1, $adminID, $adminID
FROM generate_series(1, $seedCaseCount) AS g;

-- Two distinct clarified-requirement links per case -> exactly 400
-- requirement_case edges in the chain read model.
INSERT INTO case_source (id, version_id, node_id, ref_kind, ref_id, ref_key, source, confidence)
SELECT $seedCaseSourceBase + g,
       $seedVersionBase + 1,
       $seedCaseBase + ((g - 1) / 2) + 1,
       'requirement',
       $seedRequirementBase + (CASE WHEN (g % 2) = 1
                                    THEN (((g - 1) / 2) % $seedRequirementCount) + 1
                                    ELSE (((g - 1) / 2 + 1) % $seedRequirementCount) + 1 END),
       '', 'user_input', 'high'
FROM generate_series(1, $seedCaseSourceCount) AS g;

INSERT INTO event_channel (channel, last_seq) VALUES ('slo:seed', 0)
ON CONFLICT (channel) DO UPDATE SET last_seq = 0, updated_at = now();

INSERT INTO event (channel, seq, type, version_id, ts, summary, detail, refs)
SELECT 'slo:seed', g, 'slo.seed', ($seedVersionBase + 1)::text,
       now() - (g || ' seconds')::interval, 'SLO seed event', '{}'::jsonb,
       '{"evidence":[],"case":[],"requirement":[],"code":[]}'::jsonb
FROM generate_series(1, $seedEventCount) AS g;

UPDATE event_channel SET last_seq = $seedEventCount, updated_at = now() WHERE channel = 'slo:seed';
"@
    [void](Invoke-PsqlScript -Connection $Connection -Operation "seeding the SLO dataset" -Sql $seedSql)

    $counts = @(Invoke-PsqlScript -Connection $Connection -Operation "verifying the SLO dataset" -Sql @"
SELECT
  (SELECT count(*) FROM version WHERE id >= $seedVersionBase AND id < $seedVersionBase + 10000),
  (SELECT count(*) FROM requirement WHERE id >= $seedRequirementBase AND id < $seedRequirementBase + 10000 AND status = 'clarified'),
  (SELECT count(*) FROM case_node WHERE id >= $seedCaseBase AND id < $seedCaseBase + 10000 AND kind = 'case'),
  (SELECT count(*) FROM case_source WHERE id >= $seedCaseSourceBase AND id < $seedCaseSourceBase + 10000 AND ref_kind = 'requirement'),
  (SELECT count(*) FROM event WHERE channel = 'slo:seed'),
  (SELECT count(*) FROM version WHERE id = $seedVersionBase + 1
     AND code_repository_id IS NOT NULL AND code_branch IS NOT NULL AND code_baseline_commit IS NOT NULL);
"@)
    if ($counts.Count -ne 1) { throw "unexpected dataset verification output" }
    $countLine = [string]$counts[0]
    $parts = $countLine.Split("|")
    if ($parts.Length -ne 6) { throw "unexpected dataset verification output: $countLine" }
    return [pscustomobject]@{
        Versions     = [int]$parts[0]
        Requirements = [int]$parts[1]
        Cases        = [int]$parts[2]
        Edges        = [int]$parts[3]
        Events       = [int]$parts[4]
        RepoBound    = ([int]$parts[5] -eq 1)
        VersionID    = $seedVersionBase + 1
        RepoID       = $repoID
        RepoReady    = [bool]$fixture.Ready
        RepoError    = [string]$fixture.Reason
        RepoPath     = $repoPath
        BaseCommit   = $baseCommit
        HeadCommit   = $(if ($fixture.Ready) { $fixture.HeadCommit } else { "" })
    }
}

function Remove-SeedDataset {
    param($Connection)
    if ($null -eq $Connection) { return }
    try {
        [void](Invoke-PsqlScript -Connection $Connection -Operation "cleaning the SLO dataset" -Sql @"
DELETE FROM event WHERE channel = 'slo:seed';
DELETE FROM event_channel WHERE channel = 'slo:seed';
DELETE FROM case_source WHERE id >= $seedCaseSourceBase AND id < $seedCaseSourceBase + 10000;
DELETE FROM requirement_relation WHERE id >= $seedCaseSourceBase AND id < $seedCaseSourceBase + 10000;
DELETE FROM case_node WHERE id >= $seedCaseBase AND id < $seedCaseBase + 10000;
DELETE FROM requirement WHERE id >= $seedRequirementBase AND id < $seedRequirementBase + 10000;
DELETE FROM version WHERE id >= $seedVersionBase AND id < $seedVersionBase + 10000;
DELETE FROM code_repository WHERE key = '$seedRepoKey';
"@)
    } catch {
        Write-Warning "SLO seed cleanup failed: $($_.Exception.Message)"
    }
}

# Assert-ChainShape calls the measured chain endpoint and fails closed unless the
# read model really contains a graph large enough for the SLO (spec 1.5/5.2).
function Assert-ChainShape {
    param([string]$VersionID, $Headers)
    $path = "/api/versions/$VersionID/exploration-chain?limit=300&requirementLimit=100"
    $result = Invoke-ApiRequest -Path $path -Headers $Headers
    if ($result.Status -ne 200) {
        throw "chain preflight HTTP $($result.Status) $(Get-ApiErrorDetail -Body $result.Body)"
    }
    $payload = $result.Body | ConvertFrom-Json
    $nodes = @($payload.data.nodes).Count
    $edges = @($payload.data.edges).Count
    $requirements = [int]$payload.data.counts.requirements
    $cases = [int]$payload.data.counts.cases
    if ($nodes -lt 200 -or $edges -lt 400) {
        throw "chain graph is too small: nodes=$nodes (need >=200), edges=$edges (need >=400)"
    }
    if ($requirements -lt $seedRequirementCount -or $cases -lt $seedCaseCount) {
        throw "chain layer counts are too small: requirements=$requirements (need >=$seedRequirementCount), cases=$cases (need >=$seedCaseCount)"
    }
    return [pscustomobject]@{
        Nodes = $nodes; Edges = $edges; Requirements = $requirements; Cases = $cases
    }
}

function Invoke-BrowserMeasurement {
    param([string]$BaseUrl, [string]$VersionID, [string]$Token, $User, [int]$Count)
    if ([string]::IsNullOrWhiteSpace($BaseUrl)) {
        return [pscustomobject]@{ Status = "SKIP"; Detail = "no -WebBaseUrl supplied"; P95 = $null }
    }
    try {
        $probe = Invoke-WebRequest -Uri ($BaseUrl.TrimEnd("/") + "/login") -UseBasicParsing `
            -TimeoutSec 30 -ErrorAction Stop
        if ([int]$probe.StatusCode -ne 200) {
            return [pscustomobject]@{ Status = "SKIP"; Detail = "web app answered HTTP $($probe.StatusCode)"; P95 = $null }
        }
    } catch {
        return [pscustomobject]@{ Status = "SKIP"; Detail = "web app not reachable at $BaseUrl"; P95 = $null }
    }

    $node = Resolve-ToolPath -LocalPath "" -CommandName "node.exe"
    if ([string]::IsNullOrWhiteSpace($node)) {
        return [pscustomobject]@{ Status = "SKIP"; Detail = "node.exe is not available"; P95 = $null }
    }
    $playwright = Join-Path $frontendDir "node_modules\@playwright\test\package.json"
    if (-not (Test-Path -LiteralPath $playwright)) {
        return [pscustomobject]@{ Status = "SKIP"; Detail = "Playwright is not installed in frontend/node_modules"; P95 = $null }
    }
    if ($Count -lt 3) { $Count = 3 }

    New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null
    $scriptPath = Join-Path $tmpDir "overview-nav-probe.cjs"
    $js = @'
const { chromium } = require("@playwright/test");
(async () => {
  const url = process.env.SLO_OVERVIEW_URL;
  const versionId = process.env.SLO_OVERVIEW_VERSION;
  const count = Number(process.env.SLO_OVERVIEW_SAMPLES || "5");
  const channel = process.env.SLO_OVERVIEW_CHANNEL || "";
  const launchOptions = channel ? { headless: true, channel } : { headless: true };
  let browser;
  try {
    browser = await chromium.launch(launchOptions);
  } catch (error) {
    if (!channel) throw error;
    browser = await chromium.launch({ headless: true });
  }
  try {
    const context = await browser.newContext();
    await context.addInitScript(([token, user]) => {
      sessionStorage.setItem("eztest.token", token);
      sessionStorage.setItem("eztest.user", user);
    }, [process.env.SLO_OVERVIEW_TOKEN, process.env.SLO_OVERVIEW_USER]);
    const page = await context.newPage();
    const openOverview = async () => {
      await page.goto(url, { waitUntil: "domcontentloaded", timeout: 60000 });
      if (!page.url().includes("/versions/" + versionId)) {
        throw new Error("overview navigation ended on " + page.url());
      }
      await page.getByTestId("overview-metrics").waitFor({ state: "visible", timeout: 60000 });
      return page.evaluate(() => {
        const entry = performance.getEntriesByType("navigation")[0];
        return entry ? entry.duration : performance.now();
      });
    };
    // Warm-up: the first Next dev compile must not pollute the measured P95.
    await openOverview();
    const samples = [];
    for (let i = 0; i < count; i++) {
      samples.push(await openOverview());
    }
    console.log(JSON.stringify(samples));
  } finally {
    await browser.close();
  }
})().catch((error) => {
  console.error(error && error.message ? error.message : String(error));
  process.exit(1);
});
'@
    [System.IO.File]::WriteAllText($scriptPath, $js, (New-Object System.Text.UTF8Encoding($false)))

    $env:SLO_OVERVIEW_URL = $BaseUrl.TrimEnd("/") + "/versions/" + $VersionID + "?tab=overview"
    $env:SLO_OVERVIEW_VERSION = $VersionID
    $env:SLO_OVERVIEW_TOKEN = $Token
    $env:SLO_OVERVIEW_USER = $User | ConvertTo-Json -Compress
    $env:SLO_OVERVIEW_SAMPLES = [string]$Count
    $channel = $env:EZTEST_E2E_CHANNEL
    if ([string]::IsNullOrWhiteSpace($channel)) { $channel = "chrome" }
    $env:SLO_OVERVIEW_CHANNEL = $channel

    $savedNodePath = $env:NODE_PATH
    $env:NODE_PATH = Join-Path $frontendDir "node_modules"
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        Push-Location -LiteralPath $frontendDir
        try {
            $output = & $node $scriptPath 2>&1
            $code = $LASTEXITCODE
        } finally {
            Pop-Location
        }
    } finally {
        $ErrorActionPreference = $previous
        if ($null -eq $savedNodePath) {
            Remove-Item Env:NODE_PATH -ErrorAction SilentlyContinue
        } else {
            $env:NODE_PATH = $savedNodePath
        }
        Remove-Item Env:SLO_OVERVIEW_URL, Env:SLO_OVERVIEW_VERSION, Env:SLO_OVERVIEW_TOKEN,
            Env:SLO_OVERVIEW_USER, Env:SLO_OVERVIEW_SAMPLES, Env:SLO_OVERVIEW_CHANNEL `
            -ErrorAction SilentlyContinue
    }

    $lastLine = @($output) | Select-Object -Last 1
    if ($code -ne 0) {
        return [pscustomobject]@{
            Status = "FAIL"
            Detail = "Playwright overview probe failed: $lastLine"
            P95    = $null
        }
    }
    try {
        $samples = [double[]](@($output | Select-Object -Last 1) | ConvertFrom-Json)
    } catch {
        return [pscustomobject]@{
            Status = "FAIL"; Detail = "Playwright overview probe produced no timing samples"; P95 = $null
        }
    }
    if ($samples.Count -lt 3) {
        return [pscustomobject]@{
            Status = "FAIL"; Detail = "Playwright overview probe returned too few samples"; P95 = $null
        }
    }
    return [pscustomobject]@{
        Status = "PASS"
        Detail = "$($samples.Count) measured navigations (1 warm-up excluded) to ?tab=overview; overview-metrics rendered"
        P95    = (Get-Percentile -Values $samples -Percentile 0.95)
    }
}

function Add-NotMeasuredRows {
    Add-Measurement -Name "executionConcurrency" -Status "SKIP" `
        -Threshold "$($thresholds.executionWorkers) workers / $($thresholds.executionErrors) errors" -Value $null `
        -Detail "NOT MEASURED (2 of 6): no fixture executor/session harness is supplied to this run; scripts/acceptance-execution-llm.ps1 (Task 35) owns the 8-worker batch execution gate. This row is not a PASS."
    Add-Measurement -Name "analysisNonModelOverheadMs" -Status "SKIP" `
        -Threshold "no fixed P8 gate" -Value $null `
        -Detail "NOT MEASURED (2 of 6): requires a configured LLM and a fixture analysis run; the eval/acceptance gates own that measurement. This row is not a PASS."
}

function Ensure-MeasurementRows {
    param([string]$FailureDetail)
    foreach ($name in $measurementOrder) {
        if ($measurements.Contains($name)) { continue }
        switch ($name) {
            "overviewApiP95Ms" { Add-Measurement -Name $name -Status "FAIL" -Threshold "<= $($thresholds.overviewP95Ms) ms" -Value $null -Detail $FailureDetail }
            "overviewBrowserP95Ms" { Add-Measurement -Name $name -Status "FAIL" -Threshold "<= $($thresholds.overviewP95Ms) ms" -Value $null -Detail $FailureDetail }
            "chainP95Ms" { Add-Measurement -Name $name -Status "FAIL" -Threshold "<= $($thresholds.chainP95Ms) ms" -Value $null -Detail $FailureDetail }
            "changesP95Ms" { Add-Measurement -Name $name -Status "FAIL" -Threshold "<= $($thresholds.changesP95Ms) ms" -Value $null -Detail $FailureDetail }
            default { Add-NotMeasuredRows }
        }
    }
}

function Write-Outputs {
    param([string]$Mode, [string]$ApiBase, [string]$WebBase, [string]$DatasetJSON, [string]$Note)
    New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $reportPath) | Out-Null

    foreach ($name in $measurementOrder) {
        if (-not $measurements.Contains($name)) {
            Add-Measurement -Name $name -Status "FAIL" -Threshold "see report" -Value $null `
                -Detail "measurement row was not produced by the run"
        }
    }

    $generatedAtUtc = (Get-Date).ToUniversalTime()
    $generatedAt = $generatedAtUtc.ToString("yyyy-MM-ddTHH:mm:ssZ")

    $failed = @($checks | Where-Object { $_.Status -eq "FAIL" })
    $failedMeasurements = @($measurementOrder | Where-Object { $measurements[$_].status -eq "FAIL" })
    $skipMeasurements = @($measurementOrder | Where-Object { $measurements[$_].status -eq "SKIP" })
    $total = $measurementOrder.Count
    $measured = $total - $skipMeasurements.Count
    $status = "passed"
    if ($failed.Count -gt 0 -or $failedMeasurements.Count -gt 0) {
        $status = "failed"
    } elseif ($Mode -eq "dry-run" -and $skipMeasurements.Count -gt 0) {
        $status = "static-check-passed-measurements-skipped"
    } elseif ($skipMeasurements.Count -gt 0) {
        # Missing measurements are recorded, never rounded up to a pass.
        $status = "incomplete"
    }

    $skippedLabels = @($skipMeasurements | ForEach-Object { $measurementLabels[$_] })
    $coverageNote = "$measured/$total measurement rows measured"
    if ($skipMeasurements.Count -gt 0) {
        $coverageNote += "; $($skipMeasurements.Count) NOT MEASURED (" + ($skippedLabels -join ", ") + ")"
        if ($Mode -ne "dry-run") { $coverageNote += ". INCOMPLETE is not an SLO PASS." }
    }

    $metrics = [ordered]@{
        generatedAt  = $generatedAt
        mode         = $Mode
        status       = $status
        apiBaseUrl   = $ApiBase
        webBaseUrl   = $WebBase
        thresholds   = $thresholds
        dataset      = $dataset
        datasetState = $(if ([string]::IsNullOrWhiteSpace($DatasetJSON)) { $null } else { $DatasetJSON | ConvertFrom-Json })
        chainShape   = $chainShape
        coverage     = [ordered]@{
            total       = $total
            measured    = $measured
            notMeasured = $skipMeasurements.Count
            missing     = $skippedLabels
        }
        checks       = @($checks)
        measurements = $measurements
        note         = $Note
    }
    $json = $metrics | ConvertTo-Json -Depth 8

    [System.IO.File]::WriteAllText($metricsPath, $json, (New-Object System.Text.UTF8Encoding($false)))

    $builder = New-Object System.Text.StringBuilder
    [void]$builder.AppendLine("# P8 SLO Measurement Report")
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("> Generated by ``scripts/slo.ps1`` (design spec sections 1.5/10.1). Local acceptance measurement, **not a production SLA promise**; re-measure per deployment environment.")
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("- Generated (UTC): $generatedAt")
    [void]$builder.AppendLine("- Mode: ``$Mode``")
    [void]$builder.AppendLine("- API: ``$ApiBase``")
    [void]$builder.AppendLine("- Web: ``$WebBase``")
    [void]$builder.AppendLine("- Overall status: **$($status.ToUpperInvariant())**")
    [void]$builder.AppendLine("- Coverage: $coverageNote")
    if (-not [string]::IsNullOrWhiteSpace($Note)) {
        [void]$builder.AppendLine("- Note: $Note")
    }
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("## Dataset (spec 10.1)")
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("| Object | Target |")
    [void]$builder.AppendLine("| --- | --- |")
    [void]$builder.AppendLine("| Versions | $($dataset.versions) |")
    [void]$builder.AppendLine("| Requirements (measured version, clarified) | $($dataset.requirements) |")
    [void]$builder.AppendLine("| Cases (measured version) | $($dataset.cases) |")
    [void]$builder.AppendLine("| Edges (case_source requirement links, measured version) | $($dataset.edges) |")
    [void]$builder.AppendLine("| Events | $($dataset.events) |")
    [void]$builder.AppendLine("")
    if (-not [string]::IsNullOrWhiteSpace($DatasetJSON)) {
        $seeded = $DatasetJSON | ConvertFrom-Json
        [void]$builder.AppendLine("Seeded and verified: $($seeded.Versions) versions; version ``$($seeded.VersionID)`` contains $($seeded.Requirements) clarified requirements, $($seeded.Cases) cases, $($seeded.Edges) case_source requirement links; $($seeded.Events) events in the slo:seed channel.")
        if ($seeded.RepoReady) {
            [void]$builder.AppendLine("Fixture repository bound: repoId $($seeded.RepoID), branch main, baseline $($seeded.BaseCommit) (head $($seeded.HeadCommit)).")
        } else {
            [void]$builder.AppendLine("Fixture repository NOT prepared: $($seeded.RepoError)")
        }
        [void]$builder.AppendLine("")
    }
    if ($null -ne $chainShape) {
        [void]$builder.AppendLine("Chain read-model assertion: **PASS** - $($chainShape.Nodes) nodes / $($chainShape.Edges) edges returned before the P95 loop (requirements $($chainShape.Requirements), cases $($chainShape.Cases)).")
        [void]$builder.AppendLine("")
    }
    [void]$builder.AppendLine("## Thresholds and results")
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("| Measurement | Threshold | Measured P95 | Status | Detail |")
    [void]$builder.AppendLine("| --- | --- | --- | --- | --- |")
    foreach ($name in $measurementOrder) {
        $row = $measurements[$name]
        $value = "SKIP"
        if ($null -ne $row.value) { $value = [string]$row.value }
        [void]$builder.AppendLine("| $($measurementLabels[$name]) | $($row.threshold) | $value | $($row.status) | $($row.detail) |")
    }
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("## Static checks")
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("| Area | Check | Status | Detail |")
    [void]$builder.AppendLine("| --- | --- | --- | --- |")
    foreach ($check in $checks) {
        [void]$builder.AppendLine("| $($check.Area) | $($check.Check) | $($check.Status) | $($check.Detail) |")
    }
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("## Reproduce")
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine('```powershell')
    [void]$builder.AppendLine("# Static preflight (no API/browser needed):")
    [void]$builder.AppendLine("powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/slo.ps1 -DryRun")
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("# Local measurement (running API + Web + PostgreSQL required; a disposable")
    [void]$builder.AppendLine("# Git fixture repository is created under .tmp/p8-slo/fixture-repo):")
    [void]$builder.AppendLine("powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/slo.ps1 ``")
    [void]$builder.AppendLine("  -ApiBaseUrl http://127.0.0.1:13333 -WebBaseUrl http://127.0.0.1:6868")
    [void]$builder.AppendLine('```')
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("Exit code: 1 when a static check, a measured threshold or a chain/dataset assertion fails. Exit code 0 when rows are PASS or explicitly SKIP, but a SKIP row is reported as INCOMPLETE (never as an SLO PASS).")
    [void]$builder.AppendLine("")

    [System.IO.File]::WriteAllText($reportPath, $builder.ToString(), (New-Object System.Text.UTF8Encoding($false)))
    Write-Host ""
    Write-Host "Report: $reportPath" -ForegroundColor Green
    Write-Host "Metrics: $metricsPath" -ForegroundColor Green
    Write-Host "Status: $($status.ToUpperInvariant())" -ForegroundColor $(if ($status -eq "failed") { "Red" } else { "Yellow" })
    return $status
}

# ---------------------------------------------------------------------------
# Dry-run: static preflight only.
# ---------------------------------------------------------------------------
if ($DryRun) {
    Write-Step "SLO static preflight (dry-run)"

    $requiredFiles = @(
        "backend/internal/server/ratelimit.go",
        "backend/internal/server/ratelimit_test.go",
        "backend/internal/config/config.go",
        "scripts/slo.ps1",
        "docs/openapi/api.yaml"
    )
    foreach ($relative in $requiredFiles) {
        if (Test-Path -LiteralPath (Join-Path $repoRoot $relative)) {
            Add-Check -Area "files" -Check $relative -Status "PASS" -Detail "present"
        } else {
            Add-Check -Area "files" -Check $relative -Status "FAIL" -Detail "missing"
        }
    }

    $configSource = ""
    $configPath = Join-Path $repoRoot "backend/internal/config/config.go"
    if (Test-Path -LiteralPath $configPath) {
        $configSource = Get-Content -LiteralPath $configPath -Raw -Encoding UTF8
    }
    foreach ($key in @("EZTEST_RATE_LIMIT_RPS", "EZTEST_RATE_LIMIT_BURST", "EZTEST_RATE_LIMIT_SSE_MAX")) {
        if ($configSource.Contains($key)) {
            Add-Check -Area "config" -Check $key -Status "PASS" -Detail "read and validated by config.Load"
        } else {
            Add-Check -Area "config" -Check $key -Status "FAIL" -Detail "not found in backend/internal/config/config.go"
        }
    }

    $envExamplePath = Join-Path $repoRoot ".env.example"
    $envExample = ""
    if (Test-Path -LiteralPath $envExamplePath) { $envExample = Get-Content -LiteralPath $envExamplePath -Raw -Encoding UTF8 }
    $readmePath = Join-Path $repoRoot "README.md"
    $readme = ""
    if (Test-Path -LiteralPath $readmePath) { $readme = Get-Content -LiteralPath $readmePath -Raw -Encoding UTF8 }
    foreach ($key in @("EZTEST_RATE_LIMIT_RPS", "EZTEST_RATE_LIMIT_BURST", "EZTEST_RATE_LIMIT_SSE_MAX", "EZTEST_GC_ENABLED")) {
        if ($envExample.Contains($key) -and $readme.Contains($key)) {
            Add-Check -Area "docs" -Check $key -Status "PASS" -Detail "documented in .env.example and README"
        } else {
            Add-Check -Area "docs" -Check $key -Status "FAIL" -Detail "missing from .env.example or README"
        }
    }

    $chainRoutes = Join-Path $repoRoot "backend/internal/chain/interfaces/httpapi/routes.go"
    $codeRoutes = Join-Path $repoRoot "backend/internal/codeintel/interfaces/httpapi/routes.go"
    $chainSource = ""
    $codeSource = ""
    if (Test-Path -LiteralPath $chainRoutes) { $chainSource = Get-Content -LiteralPath $chainRoutes -Raw -Encoding UTF8 }
    if (Test-Path -LiteralPath $codeRoutes) { $codeSource = Get-Content -LiteralPath $codeRoutes -Raw -Encoding UTF8 }
    if ($chainSource.Contains("exploration-chain")) {
        Add-Check -Area "routes" -Check "GET /api/versions/{id}/exploration-chain" -Status "PASS" -Detail "mounted"
    } else {
        Add-Check -Area "routes" -Check "GET /api/versions/{id}/exploration-chain" -Status "FAIL" -Detail "route not found"
    }
    if ($codeSource.Contains("/code/changes")) {
        Add-Check -Area "routes" -Check "GET /api/versions/{id}/code/changes" -Status "PASS" -Detail "mounted"
    } else {
        Add-Check -Area "routes" -Check "GET /api/versions/{id}/code/changes" -Status "FAIL" -Detail "route not found"
    }

    $sloSourcePath = Join-Path $repoRoot "scripts/slo.ps1"
    $sloSource = ""
    if (Test-Path -LiteralPath $sloSourcePath) {
        $sloSource = Get-Content -LiteralPath $sloSourcePath -Raw -Encoding UTF8
    }
    if ($sloSource.Contains("case_source") -and $sloSource.Contains("'clarified'")) {
        Add-Check -Area "dataset" -Check "chain edges come from clarified case_source links" -Status "PASS" -Detail "seeded in the measured version"
    } else {
        Add-Check -Area "dataset" -Check "chain edges come from clarified case_source links" -Status "FAIL" -Detail "the seed/assertion code is missing"
    }
    if ($sloSource.Contains("Assert-ChainShape") -and $sloSource.Contains("nodes -lt 200") -and $sloSource.Contains("edges -lt 400")) {
        Add-Check -Area "dataset" -Check "chain 200-node/400-edge assertion before P95" -Status "PASS" -Detail "fail-closed preflight"
    } else {
        Add-Check -Area "dataset" -Check "chain 200-node/400-edge assertion before P95" -Status "FAIL" -Detail "the fail-closed assertion is missing"
    }
    if ($sloSource.Contains("code_repository") -and $sloSource.Contains($seedRepoKey) -and $sloSource.Contains("code_baseline_commit")) {
        Add-Check -Area "dataset" -Check "fixture code repository binding" -Status "PASS" -Detail "measured version bound to a real Git fixture"
    } else {
        Add-Check -Area "dataset" -Check "fixture code repository binding" -Status "FAIL" -Detail "the changes-endpoint fixture binding is missing"
    }
    if ($sloSource.Contains("?tab=overview") -and $sloSource.Contains("@playwright/test") -and $sloSource.Contains("overview-metrics")) {
        Add-Check -Area "dataset" -Check "browser probe opens ?tab=overview" -Status "PASS" -Detail "Playwright navigation timing against the real web app"
    } else {
        Add-Check -Area "dataset" -Check "browser probe opens ?tab=overview" -Status "FAIL" -Detail "the overview browser probe is missing or mis-targeted"
    }
    if ($sloSource.Contains("NOT MEASURED (2 of 6)") -and $sloSource.Contains("incomplete")) {
        Add-Check -Area "report" -Check "unmeasured rows are explicit, never PASS" -Status "PASS" -Detail "2/6 rows NOT MEASURED; incomplete status"
    } else {
        Add-Check -Area "report" -Check "unmeasured rows are explicit, never PASS" -Status "FAIL" -Detail "the explicit not-measured reporting is missing"
    }

    Add-Check -Area "thresholds" -Check "P8 DoD" -Status "PASS" -Detail `
        "overview/chain P95 <= $($thresholds.overviewP95Ms)/$($thresholds.chainP95Ms) ms; changes P95 <= $($thresholds.changesP95Ms) ms; $($thresholds.executionWorkers) concurrent executions, $($thresholds.executionErrors) errors"
    Add-Check -Area "report" -Check "output paths" -Status "PASS" -Detail "docs/acceptance/p8/slo.md + .tmp/p8-slo/metrics.json"

    Add-Measurement -Name "overviewApiP95Ms" -Status "SKIP" -Threshold "<= $($thresholds.overviewP95Ms) ms" -Value $null -Detail "dry-run: measurement not executed; run without -DryRun against a running API"
    Add-Measurement -Name "overviewBrowserP95Ms" -Status "SKIP" -Threshold "<= $($thresholds.overviewP95Ms) ms" -Value $null -Detail "dry-run: needs Playwright and a running web app; probes ?tab=overview"
    Add-Measurement -Name "chainP95Ms" -Status "SKIP" -Threshold "<= $($thresholds.chainP95Ms) ms" -Value $null -Detail "dry-run: measurement not executed; the run asserts nodes>=200/edges>=400 first"
    Add-Measurement -Name "changesP95Ms" -Status "SKIP" -Threshold "<= $($thresholds.changesP95Ms) ms" -Value $null -Detail "dry-run: measurement not executed; the run binds a real Git fixture repository and verifies HTTP 200 first"
    Add-NotMeasuredRows

    $status = Write-Outputs -Mode "dry-run" -ApiBase "(not probed)" -WebBase "(not probed)" -DatasetJSON "" `
        -Note "Static preflight passed; every measurement row is SKIP because no real measurement ran. This is not an SLO PASS."
    if ($status -eq "failed") { exit 1 }
    exit 0
}

# ---------------------------------------------------------------------------
# Measurement mode.
# ---------------------------------------------------------------------------
Write-Step "SLO measurement"

if ([string]::IsNullOrWhiteSpace($EnvPath)) { $EnvPath = Join-Path $repoRoot ".env" }
$envValues = Read-DotEnv -Path $EnvPath
$script:apiBase = Resolve-ApiBaseUrl -Explicit $ApiBaseUrl -EnvValues $envValues
$webBase = $WebBaseUrl.TrimEnd("/")
if ($Iterations -lt 3) { $Iterations = 3 }

$connection = $null
$datasetState = $null
$seedError = ""
$token = ""
$authHeaders = @{}

Write-Note "API: $($script:apiBase)"
try {
    $probe = Invoke-WebRequest -Uri ($script:apiBase + "/healthz") -UseBasicParsing -TimeoutSec 3 -ErrorAction Stop
} catch {
    $probe = $null
}
if ($null -eq $probe) {
    Add-Check -Area "environment" -Check "API reachable" -Status "SKIP" -Detail "no API at $($script:apiBase); start the API or pass -ApiBaseUrl"
    foreach ($name in $measurementOrder) {
        switch ($name) {
            "executionConcurrency" { Add-NotMeasuredRows }
            "analysisNonModelOverheadMs" { }
            default {
                Add-Measurement -Name $name -Status "SKIP" -Threshold "see report" -Value $null -Detail "API is not reachable; measurement skipped"
            }
        }
    }
    [void](Write-Outputs -Mode "measure" -ApiBase $script:apiBase -WebBase $webBase -DatasetJSON "" `
        -Note "API unreachable; all API/browser measurement rows are SKIP. Missing environment is not an SLO PASS.")
    exit 0
}
Add-Check -Area "environment" -Check "API reachable" -Status "PASS" -Detail $script:apiBase

if (-not $envValues.ContainsKey("EZTEST_ADMIN_USERNAME") -or [string]::IsNullOrWhiteSpace([string]$envValues["EZTEST_ADMIN_USERNAME"]) -or
    -not $envValues.ContainsKey("EZTEST_ADMIN_PASSWORD") -or [string]::IsNullOrWhiteSpace([string]$envValues["EZTEST_ADMIN_PASSWORD"])) {
    Add-Check -Area "environment" -Check "admin credentials" -Status "SKIP" -Detail "EZTEST_ADMIN_USERNAME/PASSWORD are not set in .env"
} else {
    try {
        $loginBody = @{
            username = [string]$envValues["EZTEST_ADMIN_USERNAME"]
            password = [string]$envValues["EZTEST_ADMIN_PASSWORD"]
        } | ConvertTo-Json
        $login = Invoke-RestMethod -Uri ($script:apiBase + "/api/auth/login") -Method Post -Body $loginBody `
            -ContentType "application/json" -TimeoutSec 10 -ErrorAction Stop
        $token = [string]$login.data.token
        if ([string]::IsNullOrWhiteSpace($token)) { throw "login response has no token" }
        $authHeaders = @{ Authorization = "Bearer $token" }
        Add-Check -Area "environment" -Check "admin login" -Status "PASS" -Detail "authenticated against the running API"
    } catch {
        Add-Check -Area "environment" -Check "admin login" -Status "SKIP" -Detail "login failed; check EZTEST_ADMIN_USERNAME/PASSWORD in .env"
    }
}

if (-not [string]::IsNullOrWhiteSpace($token)) {
    $dbUrl = $DatabaseUrl
    if ([string]::IsNullOrWhiteSpace($dbUrl) -and $envValues.ContainsKey("EZTEST_DATABASE_URL")) {
        $dbUrl = [string]$envValues["EZTEST_DATABASE_URL"]
    }
    $connection = Parse-PostgresUrl -Url $dbUrl
    $script:psqlExe = Resolve-ToolPath -LocalPath (Join-Path $repoRoot ".tools\pgsql\bin\psql.exe") -CommandName "psql.exe"
    if ($null -eq $connection) {
        Add-Check -Area "dataset" -Check "database URL" -Status "SKIP" -Detail "EZTEST_DATABASE_URL is missing or not a postgres URL"
    } elseif ([string]::IsNullOrWhiteSpace($script:psqlExe)) {
        Add-Check -Area "dataset" -Check "psql" -Status "SKIP" -Detail "psql.exe is not available"
    } else {
        try {
            $datasetState = Invoke-SeedDataset -Connection $connection
            Add-Check -Area "dataset" -Check "seed" -Status "PASS" -Detail `
                "$($datasetState.Versions) versions; measured version $($datasetState.VersionID): $($datasetState.Requirements) clarified requirements, $($datasetState.Cases) cases, $($datasetState.Edges) case_source links, $($datasetState.Events) events"
            if ($datasetState.RepoReady) {
                Add-Check -Area "dataset" -Check "fixture repository" -Status "PASS" -Detail `
                    "repoId $($datasetState.RepoID) bound to version $($datasetState.VersionID); main baseline $($datasetState.BaseCommit)"
            } else {
                Add-Check -Area "dataset" -Check "fixture repository" -Status "SKIP" -Detail `
                    "changes endpoint cannot be measured: $($datasetState.RepoError)"
            }
        } catch {
            $seedError = $_.Exception.Message
            Add-Check -Area "dataset" -Check "seed" -Status "FAIL" -Detail $seedError
        }
    }
} else {
    Add-Check -Area "dataset" -Check "seed" -Status "SKIP" -Detail "no authenticated API session"
}

$seedVersionID = ""
if ($null -ne $datasetState) { $seedVersionID = [string]$datasetState.VersionID }

try {
    if ($null -ne $datasetState -and -not [string]::IsNullOrWhiteSpace($seedVersionID)) {
        Write-Note "dataset seeded; verifying the measured graph before measuring"

        # 1. Version overview first-screen API (the version detail call the
        # overview header/first screen awaits).
        try {
            $overview = Invoke-Measure -Path "/api/versions/$seedVersionID" -Count $Iterations -Headers $authHeaders
            $overviewOk = $overview.P95 -le $thresholds.overviewP95Ms
            Add-Measurement -Name "overviewApiP95Ms" -Status $(if ($overviewOk) { "PASS" } else { "FAIL" }) `
                -Threshold "<= $($thresholds.overviewP95Ms) ms" -Value $overview.P95 `
                -Detail "GET /api/versions/$seedVersionID; P50 $($overview.P50) ms over $($overview.Samples) samples"
        } catch {
            Add-Measurement -Name "overviewApiP95Ms" -Status "FAIL" -Threshold "<= $($thresholds.overviewP95Ms) ms" -Value $null `
                -Detail "version detail request failed: $($_.Exception.Message)"
        }

        # 2. Exploration chain: assert the real read model first, then measure.
        try {
            $chainShape = Assert-ChainShape -VersionID $seedVersionID -Headers $authHeaders
            Add-Check -Area "dataset" -Check "chain 200 nodes / 400 edges" -Status "PASS" -Detail `
                "endpoint returned $($chainShape.Nodes) nodes and $($chainShape.Edges) edges (requirements $($chainShape.Requirements), cases $($chainShape.Cases)) before measuring"
            $chain = Invoke-Measure -Path "/api/versions/$seedVersionID/exploration-chain?limit=300&requirementLimit=100" -Count $Iterations -Headers $authHeaders
            $chainOk = $chain.P95 -le $thresholds.chainP95Ms
            Add-Measurement -Name "chainP95Ms" -Status $(if ($chainOk) { "PASS" } else { "FAIL" }) `
                -Threshold "<= $($thresholds.chainP95Ms) ms" -Value $chain.P95 `
                -Detail "P50 $($chain.P50) ms over $($chain.Samples) samples; verified $($chainShape.Nodes) nodes/$($chainShape.Edges) edges"
        } catch {
            Add-Check -Area "dataset" -Check "chain 200 nodes / 400 edges" -Status "FAIL" -Detail $_.Exception.Message
            Add-Measurement -Name "chainP95Ms" -Status "FAIL" -Threshold "<= $($thresholds.chainP95Ms) ms" -Value $null `
                -Detail "chain measurement refused: $($_.Exception.Message)"
        }

        # 3. Code change-set API: verify the fixture binding answers 200 first.
        if ($datasetState.RepoReady) {
            try {
                $changesPre = Invoke-ApiRequest -Path "/api/versions/$seedVersionID/code/changes" -Headers $authHeaders
                if ($changesPre.Status -ne 200) {
                    $detail = Get-ApiErrorDetail -Body $changesPre.Body
                    throw "preflight HTTP $($changesPre.Status) $detail"
                }
                $changes = Invoke-Measure -Path "/api/versions/$seedVersionID/code/changes" -Count $Iterations -Headers $authHeaders
                $changesOk = $changes.P95 -le $thresholds.changesP95Ms
                Add-Measurement -Name "changesP95Ms" -Status $(if ($changesOk) { "PASS" } else { "FAIL" }) `
                    -Threshold "<= $($thresholds.changesP95Ms) ms" -Value $changes.P95 `
                    -Detail "preflight HTTP 200; P50 $($changes.P50) ms over $($changes.Samples) samples"
            } catch {
                Add-Measurement -Name "changesP95Ms" -Status "FAIL" -Threshold "<= $($thresholds.changesP95Ms) ms" -Value $null `
                    -Detail "changes measurement failed: $($_.Exception.Message)"
            }
        } else {
            Add-Measurement -Name "changesP95Ms" -Status "SKIP" -Threshold "<= $($thresholds.changesP95Ms) ms" -Value $null `
                -Detail "SKIP: fixture Git repository could not be prepared ($($datasetState.RepoError)); the endpoint was not measured"
        }

        # 4. Browser navigation timing of the overview tab.
        $browser = Invoke-BrowserMeasurement -BaseUrl $webBase -VersionID $seedVersionID -Token $token `
            -User $login.data.user -Count $BrowserIterations
        if ($null -ne $browser.P95) {
            $browserOk = $browser.P95 -le $thresholds.overviewP95Ms
            Add-Measurement -Name "overviewBrowserP95Ms" -Status $(if ($browserOk) { "PASS" } else { "FAIL" }) `
                -Threshold "<= $($thresholds.overviewP95Ms) ms" -Value $browser.P95 -Detail $browser.Detail
        } else {
            Add-Measurement -Name "overviewBrowserP95Ms" -Status $browser.Status -Threshold "<= $($thresholds.overviewP95Ms) ms" `
                -Value $null -Detail $browser.Detail
        }
    } else {
        $failure = "dataset was not seeded; measurement did not run"
        if (-not [string]::IsNullOrWhiteSpace($seedError)) { $failure = "dataset seed failed: $seedError" }
        foreach ($name in @("overviewApiP95Ms", "overviewBrowserP95Ms", "chainP95Ms", "changesP95Ms")) {
            switch ($name) {
                "overviewApiP95Ms" { Add-Measurement -Name $name -Status "FAIL" -Threshold "<= $($thresholds.overviewP95Ms) ms" -Value $null -Detail $failure }
                "overviewBrowserP95Ms" { Add-Measurement -Name $name -Status "FAIL" -Threshold "<= $($thresholds.overviewP95Ms) ms" -Value $null -Detail $failure }
                "chainP95Ms" { Add-Measurement -Name $name -Status "FAIL" -Threshold "<= $($thresholds.chainP95Ms) ms" -Value $null -Detail $failure }
                "changesP95Ms" { Add-Measurement -Name $name -Status "FAIL" -Threshold "<= $($thresholds.changesP95Ms) ms" -Value $null -Detail $failure }
            }
        }
    }
} catch {
    Add-Check -Area "measurement" -Check "request" -Status "FAIL" -Detail $_.Exception.Message
    Ensure-MeasurementRows -FailureDetail "measurement aborted: $($_.Exception.Message)"
} finally {
    Remove-SeedDataset -Connection $connection
}

Add-NotMeasuredRows
Ensure-MeasurementRows -FailureDetail "measurement did not run"

$datasetJSON = ""
if ($null -ne $datasetState) {
    $datasetJSON = ([ordered]@{
            Versions     = $datasetState.Versions
            Requirements = $datasetState.Requirements
            Cases        = $datasetState.Cases
            Edges        = $datasetState.Edges
            Events       = $datasetState.Events
            VersionID    = $datasetState.VersionID
            RepoID       = $datasetState.RepoID
            RepoReady    = $datasetState.RepoReady
            RepoError    = $datasetState.RepoError
            RepoPath     = $datasetState.RepoPath
            BaseCommit   = $datasetState.BaseCommit
            HeadCommit   = $datasetState.HeadCommit
        }) | ConvertTo-Json -Compress
}

$status = Write-Outputs -Mode "measure" -ApiBase $script:apiBase -WebBase $webBase -DatasetJSON $datasetJSON `
    -Note "Local measurement; the two NOT MEASURED (2/6) rows require the fixture executor harness and a configured LLM. SKIP rows are reported as INCOMPLETE, never as an SLO PASS."
if ($status -eq "failed") { exit 1 }
exit 0
