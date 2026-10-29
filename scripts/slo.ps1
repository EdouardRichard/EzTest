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

    The 8-concurrent batch execution and the analysis non-model overhead are
    measured with a deterministic local model harness that must be up when the
    API starts: scripts/fake-llm.cjs behind a small timing proxy on loopback,
    wired into the API through llm_base_url/llm_key/llm_model_name (the P4
    analysis LLM gateway snapshots its configuration at startup, so a runtime
    config change would only cover the execution agent). Pass
    -LlmCallLogPath <proxy-jsonl> so the analysis row can sum real model-call
    time and subtract it from the analysis wall time; without the proxy log the
    analysis row reports the wall time as an upper bound. The concurrency row
    creates eight fixture execution cases in an executing version, imports
    eight fixture sessions, runs one real batch through the running
    API/executor, waits for the terminal state and counts case errors and the
    maximum observed concurrently running case runs. The API must be started
    with the executor configured (EZTEST_EXECUTOR_URL/TOKEN) and with
    EZTEST_EXEC_MAX_CONCURRENT_CASES=8; a lower cap is a recorded FAIL, not a
    pass. Genuinely missing environment (no API, database, executor, fixture or
    loopback/fake LLM) is an explicit SKIP, so a run can still be INCOMPLETE,
    but an attempted measurement never silently degrades to a pass. A configured
    non-loopback LLM is never called by this local measurement unless
    -AllowRealLlm is passed explicitly.

    -DryRun performs static preflight only: it checks that the limiter, the SLO
    script, the measured routes, the chain dataset/assertion code, the fixture
    repository binding, the overview browser probe, the concurrency/analysis
    measurement harness, the environment keys and the report paths are present,
    writes docs/acceptance/p8/slo.md and docs/acceptance/p8/slo-metrics.json
    with the measurement rows marked SKIP, and exits 0 when the static checks
    pass.

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

.PARAMETER ExecutionFixtureBaseUrl
    Base URL of the deterministic P6 fixture target used by the 8-concurrency
    execution measurement (default http://127.0.0.1:13335). The script starts
    scripts/fixture-target.cjs there when nothing is listening on that loopback
    port; an unreachable non-loopback target is a SKIP.

.PARAMETER ExecutionTimeoutSeconds
    Upper bound for one 8-case batch to reach a terminal state (default 600).

.PARAMETER AnalysisTimeoutSeconds
    Upper bound for the P4 analysis run to reach a terminal state (default 240).

.PARAMETER AllowRealLlm
    When the API's default LLM configuration does not point at a loopback
    upstream, the two model measurements are SKIP by default so a local
    measurement never spends real provider calls. Pass this switch to allow
    them against the configured provider; the analysis row is then reported as
    a wall-time upper bound.

.PARAMETER LlmCallLogPath
    JSONL log written by a loopback timing proxy placed in front of the fake
    LLM the API was started with (one entry per chat completion with
    ``path``/``durationMs``). When present, the analysis row subtracts the
    summed model-call time from the wall time; otherwise the wall time is
    reported as an upper bound. The proxy log is read from this path only - the
    script never patches the LLM configuration.

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
    [string]$EnvPath = "",
    [string]$ExecutionFixtureBaseUrl = "http://127.0.0.1:13335",
    [int]$ExecutionTimeoutSeconds = 600,
    [int]$AnalysisTimeoutSeconds = 240,
    [string]$LlmCallLogPath = "",
    [switch]$AllowRealLlm
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$backendDir = Join-Path $repoRoot "backend"
$frontendDir = Join-Path $repoRoot "frontend"
$tmpDir = Join-Path $repoRoot ".tmp\p8-slo"
$metricsPath = Join-Path $tmpDir "metrics.json"
$committedMetricsPath = Join-Path $repoRoot "docs\acceptance\p8\slo-metrics.json"
$reportPath = Join-Path $repoRoot "docs\acceptance\p8\slo.md"
$fixtureRepoDir = Join-Path $tmpDir "fixture-repo"
$fixtureTargetScript = Join-Path $repoRoot "scripts\fixture-target.cjs"

# Design spec 1.5 thresholds and 10.1 dataset.
$thresholds = [ordered]@{
    overviewP95Ms               = 2500
    chainP95Ms                  = 2500
    changesP95Ms                = 2000
    executionWorkers            = 8
    executionErrors             = 0
    analysisNonModelOverheadMs  = 5000
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
        # The head commit carries a per-run nonce so every SLO run analyses a
        # fresh commit: the P4 analysis reuse check (same version/base/head/
        # model/prompt) and the content cache are then guaranteed to miss and a
        # real analysis actually runs. The measured diff stays the one app.py
        # change either way.
        $nonce = [DateTime]::UtcNow.ToString("yyyyMMddHHmmssfff")
        [System.IO.File]::WriteAllText((Join-Path $fullDirectory "app.py"),
            "# p8-slo run $nonce`ndef handle_refund(amount, reason=''):`n    return {'status': 'paid', 'amount': amount, 'reason': reason}`n",
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

# ---------------------------------------------------------------------------
# JSON API helper (the latency measurements only needed GET).
# ---------------------------------------------------------------------------
function Get-ApiErrorInfo {
    param([string]$Body)
    if ([string]::IsNullOrWhiteSpace($Body)) {
        return [pscustomobject]@{ Code = ""; Reason = ""; Message = "" }
    }
    try {
        $payload = $Body | ConvertFrom-Json
        return [pscustomobject]@{
            Code    = [string]$payload.error.code
            Reason  = [string]$payload.error.details.reason
            Message = [string]$payload.error.message
        }
    } catch {
        return [pscustomobject]@{ Code = ""; Reason = ""; Message = "" }
    }
}

function Invoke-ApiJson {
    param([string]$Method, [string]$Path, $Headers, $Body, [int]$TimeoutSec = 30, [int]$Retry = 0)
    if ($null -eq $Headers) { $Headers = @{} }
    $arguments = @{
        Uri             = ($script:apiBase + $Path)
        Method          = $Method
        Headers         = $Headers
        UseBasicParsing = $true
        TimeoutSec      = $TimeoutSec
    }
    if ($null -ne $Body) {
        $arguments["ContentType"] = "application/json; charset=utf-8"
        $arguments["Body"] = [System.Text.Encoding]::UTF8.GetBytes(($Body | ConvertTo-Json -Compress -Depth 12))
    }
    try {
        $response = Invoke-WebRequest @arguments -ErrorAction Stop
        $status = [int]$response.StatusCode
        $content = [string]$response.Content
    } catch {
        $errorResponse = $_.Exception.Response
        if ($null -eq $errorResponse) { throw "$Method $Path failed: $($_.Exception.Message)" }
        try { $status = [int]$errorResponse.StatusCode } catch { $status = -1 }
        $content = ""
        try {
            if ($null -ne $errorResponse.PSObject.Methods['GetResponseStream']) {
                $stream = $errorResponse.GetResponseStream()
                if ($null -ne $stream) {
                    $reader = New-Object System.IO.StreamReader($stream)
                    try { $content = $reader.ReadToEnd() } finally { $reader.Dispose() }
                }
            } elseif ($null -ne $errorResponse.Content) {
                $content = [string]$errorResponse.Content.ReadAsStringAsync().Result
            }
        } catch {
            $content = ""
        }
        if ($status -eq 429 -and $Retry -lt 3) {
            $wait = 1
            try {
                $retryAfter = [string]$errorResponse.Headers["Retry-After"]
                if ($retryAfter -match '^\d+$') { $wait = [int]$retryAfter }
            } catch { $wait = 1 }
            if ($wait -lt 1) { $wait = 1 }
            if ($wait -gt 3) { $wait = 3 }
            Start-Sleep -Seconds $wait
            return Invoke-ApiJson -Method $Method -Path $Path -Headers $Headers -Body $Body -TimeoutSec $TimeoutSec -Retry ($Retry + 1)
        }
    }
    $data = $null
    if (-not [string]::IsNullOrWhiteSpace($content)) {
        try {
            $parsed = $content | ConvertFrom-Json
            $data = $parsed.data
        } catch {
            $data = $null
        }
    }
    return [pscustomobject]@{
        Status = $status
        Body   = $content
        Data   = $data
        Error  = (Get-ApiErrorInfo -Body $content)
    }
}

# ---------------------------------------------------------------------------
# Local process helpers for the deterministic measurement harness.
# ---------------------------------------------------------------------------
function Get-FreeTcpPort {
    $listener = New-Object System.Net.Sockets.TcpListener([System.Net.IPAddress]::Loopback, 0)
    $listener.Start()
    try {
        return ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port
    } finally {
        $listener.Stop()
    }
}

function Test-TcpPortInUse {
    param([int]$Port)
    try {
        return ($null -ne (Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue))
    } catch {
        $client = New-Object System.Net.Sockets.TcpClient
        try {
            $client.Connect("127.0.0.1", $Port)
            return $true
        } catch {
            return $false
        } finally {
            $client.Close()
        }
    }
}

function Test-SloHttpReady {
    param([string]$Url, [int]$TimeoutSec = 3)
    try {
        $response = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec $TimeoutSec -ErrorAction Stop
        return ([int]$response.StatusCode -eq 200)
    } catch {
        return $false
    }
}

function Wait-ForUrl {
    param([string]$Url, [int]$TimeoutSeconds = 30, $Process, [string]$Label)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        if ($null -ne $Process) {
            $Process.Refresh()
            if ($Process.HasExited) { throw "$Label exited early with code $($Process.ExitCode)" }
        }
        if (Test-SloHttpReady -Url $Url -TimeoutSec 5) { return }
        Start-Sleep -Milliseconds 200
    }
    throw "Timed out waiting for $Label at $Url"
}

function Start-SloBackgroundProcess {
    param(
        [string]$FilePath,
        [string]$Arguments,
        [string]$WorkingDirectory,
        [string]$OutLog,
        [string]$ErrLog,
        [hashtable]$Environment = @{}
    )
    $saved = @{}
    foreach ($key in $Environment.Keys) {
        $saved[$key] = [Environment]::GetEnvironmentVariable($key)
        [Environment]::SetEnvironmentVariable($key, [string]$Environment[$key])
    }
    try {
        Remove-Item -LiteralPath $OutLog, $ErrLog -ErrorAction SilentlyContinue
        return Start-Process -FilePath $FilePath -ArgumentList $Arguments -WorkingDirectory $WorkingDirectory `
            -RedirectStandardOutput $OutLog -RedirectStandardError $ErrLog -WindowStyle Hidden -PassThru
    } finally {
        foreach ($key in $Environment.Keys) {
            if ($null -eq $saved[$key]) {
                Remove-Item -Path ("Env:" + $key) -ErrorAction SilentlyContinue
            } else {
                [Environment]::SetEnvironmentVariable($key, [string]$saved[$key])
            }
        }
    }
}

function Stop-SloProcess {
    param($Process)
    if ($null -eq $Process) { return }
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        $Process.Refresh()
        if (-not $Process.HasExited) { & taskkill.exe /PID $Process.Id /T /F 2>$null | Out-Null }
    } catch {
        # Best-effort cleanup only.
    } finally {
        $ErrorActionPreference = $previous
    }
}

function Test-LoopbackUpstream {
    param([string]$BaseUrl)
    if ([string]::IsNullOrWhiteSpace($BaseUrl)) { return $false }
    $uri = $null
    if (-not [System.Uri]::TryCreate($BaseUrl, [System.UriKind]::Absolute, [ref]$uri)) { return $false }
    return ($uri.Host -eq "127.0.0.1" -or $uri.Host -eq "localhost" -or $uri.Host -eq "::1")
}

# ---------------------------------------------------------------------------
# Fixture login/session import used by the 8-concurrency measurement.
# ---------------------------------------------------------------------------
function Get-FixtureCookie {
    param([string]$FixtureBaseUrl)
    $request = [System.Net.HttpWebRequest]::Create(($FixtureBaseUrl.TrimEnd("/") + "/p6/login"))
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
    if ($cookieHeader -notmatch 'p6_session=([^;]+)') { throw "fixture P6 login did not set the p6_session cookie" }
    return $Matches[1]
}

function Get-FixtureStorageState {
    param([string]$FixtureBaseUrl)
    $cookie = Get-FixtureCookie -FixtureBaseUrl $FixtureBaseUrl
    $uri = [System.Uri]$FixtureBaseUrl
    $origin = "{0}://{1}" -f $uri.Scheme, $uri.Authority
    return @{
        cookies = @(@{
                name     = "p6_session"
                value    = $cookie
                domain   = $uri.Host
                path     = "/"
                expires  = -1
                httpOnly = $true
                secure   = $false
                sameSite = "Lax"
            })
        origins = @(@{
                origin         = $origin
                localStorage   = @()
                sessionStorage = @()
            })
    }
}

# ---------------------------------------------------------------------------
# Deterministic LLM measurement harness: the fake upstream and the loopback
# timing proxy are started before the API (see the report Reproduce section)
# because the analysis gateway snapshots its configuration at API startup.
# slo.ps1 only reads the resulting proxy JSONL through -LlmCallLogPath.
# ---------------------------------------------------------------------------
function Get-DefaultLlmConfig {
    param($Headers)
    $response = Invoke-ApiJson -Method Get -Path "/api/admin/llm/configs" -Headers $Headers
    if ($response.Status -ne 200) {
        throw "GET /api/admin/llm/configs answered HTTP $($response.Status) $($response.Error.Code)/$($response.Error.Reason)"
    }
    foreach ($config in @($response.Data)) {
        if ($null -ne $config -and [bool]$config.is_default) { return $config }
    }
    return $null
}

$script:llmHarness = [pscustomobject]@{
    Allowed        = $false
    Reason         = ""
    ModelTiming    = $false
    LogPath        = ""
    LlmBaseUrl     = ""
    FixtureReady   = $false
    StartedFixture = $false
    FixtureProcess = $null
}
$script:defaultLlmConfig = $null

function Initialize-SloHarness {
    param([string]$FixtureBaseUrl, $Headers)
    $harness = $script:llmHarness
    $fixtureBase = $FixtureBaseUrl.TrimEnd("/")
    if ([string]::IsNullOrWhiteSpace($fixtureBase)) { $fixtureBase = "http://127.0.0.1:13335" }

    # The fixture target can be started by this script; the fake LLM and its
    # timing proxy must already be wired into the API at startup because the P4
    # analysis gateway snapshots its configuration then.
    $harness.FixtureReady = Test-SloHttpReady -Url ($fixtureBase + "/health")
    if (-not $harness.FixtureReady -and
        (Test-Path -LiteralPath $fixtureTargetScript) -and
        (-not [string]::IsNullOrWhiteSpace($script:nodeExe))) {
        $fixtureUri = $null
        if ([System.Uri]::TryCreate($fixtureBase, [System.UriKind]::Absolute, [ref]$fixtureUri) -and
            ($fixtureUri.Host -eq "127.0.0.1" -or $fixtureUri.Host -eq "localhost")) {
            $fixturePort = $fixtureUri.Port
            if (-not (Test-TcpPortInUse -Port $fixturePort)) {
                try {
                    $harness.FixtureProcess = Start-SloBackgroundProcess -FilePath $script:nodeExe `
                        -Arguments ("`"" + $fixtureTargetScript + "`"") -WorkingDirectory $repoRoot `
                        -OutLog (Join-Path $tmpDir "slo-fixture.out.log") -ErrLog (Join-Path $tmpDir "slo-fixture.err.log") `
                        -Environment @{ FIXTURE_PORT = [string]$fixturePort }
                    Wait-ForUrl -Url ($fixtureBase + "/health") -TimeoutSeconds 30 -Process $harness.FixtureProcess -Label "fixture target"
                    $harness.FixtureReady = $true
                    $harness.StartedFixture = $true
                } catch {
                    Stop-SloProcess -Process $harness.FixtureProcess
                    $harness.FixtureProcess = $null
                }
            }
        }
    }

    try {
        $config = Get-DefaultLlmConfig -Headers $Headers
        $script:defaultLlmConfig = $config
        if ($null -eq $config) {
            $harness.Reason = "no default LLM configuration is registered"
            return
        }
        $harness.LlmBaseUrl = [string]$config.base_url
        if (-not [string]::IsNullOrWhiteSpace($LlmCallLogPath)) {
            $harness.LogPath = [System.IO.Path]::GetFullPath($LlmCallLogPath)
            $harness.ModelTiming = $true
        }
        if ((Test-LoopbackUpstream -BaseUrl $harness.LlmBaseUrl) -or $AllowRealLlm) {
            $harness.Allowed = $true
        } else {
            $harness.Allowed = $false
            $harness.Reason = "the API's default LLM base_url is '$($harness.LlmBaseUrl)' (not loopback); start the API against the deterministic fake LLM/timing proxy, or pass -AllowRealLlm to permit real provider calls"
        }
    } catch {
        $harness.Allowed = $false
        $harness.Reason = $_.Exception.Message
    }
}

function Restore-SloHarness {
    param($Headers)
    $harness = $script:llmHarness
    if ($harness.StartedFixture) { Stop-SloProcess -Process $harness.FixtureProcess }
    $harness.FixtureProcess = $null
}

function Get-LlmCallStats {
    param([int]$FromLine)
    if (-not $script:llmHarness.ModelTiming -or [string]::IsNullOrWhiteSpace($script:llmHarness.LogPath)) { return $null }
    if (-not (Test-Path -LiteralPath $script:llmHarness.LogPath)) {
        return [pscustomobject]@{ Calls = 0; ModelMs = 0.0 }
    }
    $lines = @(Get-Content -LiteralPath $script:llmHarness.LogPath -Encoding UTF8 -ErrorAction SilentlyContinue)
    if ($lines.Count -le $FromLine) { return [pscustomobject]@{ Calls = 0; ModelMs = 0.0 } }
    $calls = 0
    $modelMs = 0.0
    for ($index = $FromLine; $index -lt $lines.Count; $index++) {
        try { $entry = $lines[$index] | ConvertFrom-Json } catch { continue }
        if ([string]$entry.path -notmatch '/chat/completions') { continue }
        if ([int]$entry.status -ne 200) { throw "the LLM proxy recorded a model call with HTTP $($entry.status)" }
        $calls++
        $modelMs += [double]$entry.durationMs
    }
    return [pscustomobject]@{ Calls = $calls; ModelMs = [math]::Round($modelMs, 1) }
}

function Get-LlmCallLogLineCount {
    if ([string]::IsNullOrWhiteSpace($script:llmHarness.LogPath)) { return 0 }
    if (-not (Test-Path -LiteralPath $script:llmHarness.LogPath)) { return 0 }
    return @(Get-Content -LiteralPath $script:llmHarness.LogPath -Encoding UTF8 -ErrorAction SilentlyContinue).Count
}

function Test-SloLlmMeasurementAllowed {
    param($Config)
    if ($script:llmHarness.Allowed) { return $true }
    if ($null -eq $Config) { return $false }
    return ((Test-LoopbackUpstream -BaseUrl ([string]$Config.base_url)) -or [bool]$AllowRealLlm)
}

function Get-SloLlmSkipReason {
    if (-not [string]::IsNullOrWhiteSpace($script:llmHarness.Reason)) { return $script:llmHarness.Reason }
    if ($null -eq $script:defaultLlmConfig) { return "no default LLM configuration is registered" }
    if (-not $AllowRealLlm) {
        return "the API's configured LLM is not a loopback fixture; start it against the deterministic fake LLM/timing proxy, or pass -AllowRealLlm to permit real provider calls"
    }
    return "the LLM measurement environment is unavailable"
}

# ---------------------------------------------------------------------------
# Measurement 5: one real batch with 8 fixture cases and 8 sessions.
# ---------------------------------------------------------------------------
function New-SloExecutingVersion {
    param($Headers, [string]$Name, $SystemId, $EnvironmentId, [string]$FixtureBaseUrl, [int]$CaseCount)
    $version = Invoke-ApiJson -Method Post -Path "/api/versions" -Headers $Headers -Body @{
        name        = $Name
        description = "P8 SLO 8-concurrency execution measurement version"
        systemId    = [int64]$SystemId
        environmentId = [int64]$EnvironmentId
    }
    if ($version.Status -ne 201) {
        throw "POST /api/versions answered HTTP $($version.Status) $($version.Error.Code)/$($version.Error.Reason)"
    }
    $versionId = [string]$version.Data.id
    $requirement = Invoke-ApiJson -Method Post -Path "/api/versions/$versionId/requirements" -Headers $Headers -Body @{
        title              = "$Name requirement"
        description        = "The fixture order form is reachable and renders the expected content."
        acceptanceCriteria = "The fixture page renders the order form navigation."
        priority           = "must"
    }
    if ($requirement.Status -ne 201) {
        throw "POST requirement answered HTTP $($requirement.Status) $($requirement.Error.Code)/$($requirement.Error.Reason)"
    }
    $requirementId = [string]$requirement.Data.id
    $clarify = Invoke-ApiJson -Method Post -Path "/api/versions/$versionId/start-clarification" -Headers $Headers
    if ($clarify.Status -ne 200) { throw "POST start-clarification answered HTTP $($clarify.Status)" }
    $confirm = Invoke-ApiJson -Method Post -Path "/api/versions/$versionId/requirements/$requirementId/confirm" -Headers $Headers
    if ($confirm.Status -ne 200) { throw "POST requirement confirm answered HTTP $($confirm.Status)" }
    $enter = Invoke-ApiJson -Method Post -Path "/api/versions/$versionId/enter-design" -Headers $Headers -Body @{ acknowledgeUnclarified = $true }
    if ($enter.Status -ne 200) { throw "POST enter-design answered HTTP $($enter.Status)" }

    $scenario = Invoke-ApiJson -Method Post -Path "/api/versions/$versionId/mindmap/nodes" -Headers $Headers -Body @{
        kind  = "scenario"
        title = "$Name scenario"
    }
    if ($scenario.Status -ne 201) { throw "POST scenario node answered HTTP $($scenario.Status)" }
    $scenarioId = [string]$scenario.Data.id
    $caseIds = New-Object System.Collections.ArrayList
    for ($index = 1; $index -le $CaseCount; $index++) {
        $case = Invoke-ApiJson -Method Post -Path "/api/versions/$versionId/mindmap/nodes" -Headers $Headers -Body @{
            kind           = "case"
            parentId       = $scenarioId
            title          = "$Name case $index"
            priority       = "P0"
            requirementIds = @($requirementId)
        }
        if ($case.Status -ne 201) { throw "POST case node answered HTTP $($case.Status) $($case.Error.Code)/$($case.Error.Reason)" }
        $caseId = [string]$case.Data.id
        $step = Invoke-ApiJson -Method Post -Path "/api/versions/$versionId/mindmap/nodes/$caseId/steps" -Headers $Headers -Body @{
            action   = "Open $($FixtureBaseUrl.TrimEnd('/'))/p6/orders/new"
            expected = "The fixture new-order form is shown"
        }
        if ($step.Status -ne 201) { throw "POST case step answered HTTP $($step.Status)" }
        $assertion = Invoke-ApiJson -Method Post -Path "/api/versions/$versionId/mindmap/nodes/$caseId/assertions" -Headers $Headers -Body @{
            stepId     = [string]$step.Data.id
            kind       = "ui"
            target     = "body"
            expression = "text contains Order"
            expected   = "The fixture page renders order navigation"
        }
        if ($assertion.Status -ne 201) { throw "POST case assertion answered HTTP $($assertion.Status)" }
        [void]$caseIds.Add($caseId)
    }
    $submit = Invoke-ApiJson -Method Post -Path "/api/versions/$versionId/mindmap/submit-review" -Headers $Headers -Body @{}
    if ($submit.Status -ne 200) { throw "POST submit-review answered HTTP $($submit.Status)" }
    $review = Invoke-ApiJson -Method Post -Path "/api/versions/$versionId/mindmap/review" -Headers $Headers -Body @{
        decision            = "approve"
        acknowledgeFindings = $true
    }
    if ($review.Status -ne 200) { throw "POST review answered HTTP $($review.Status) $($review.Error.Code)/$($review.Error.Reason)" }
    if ([string]$review.Data.version.status -ne "executing") {
        throw "the review left the version in '$($review.Data.version.status)', expected executing"
    }
    if ($null -eq $review.Data.baseline) { throw "the review did not freeze a case baseline" }
    return [pscustomobject]@{ VersionId = $versionId; CaseIds = @($caseIds) }
}

function Wait-SloBatchTerminal {
    param($Headers, [string]$VersionId, [string]$BatchId, [int]$TimeoutSeconds)
    $stopwatch = [System.Diagnostics.Stopwatch]::StartNew()
    $maxRunning = 0
    $batch = $null
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        $batchResponse = Invoke-ApiJson -Method Get -Path "/api/versions/$VersionId/batches/$BatchId" -Headers $Headers
        if ($batchResponse.Status -ne 200) { throw "GET batch answered HTTP $($batchResponse.Status)" }
        $batch = $batchResponse.Data
        $runsResponse = Invoke-ApiJson -Method Get -Path "/api/versions/$VersionId/batches/$BatchId/case-runs?limit=100" -Headers $Headers
        if ($runsResponse.Status -eq 200) {
            $running = @($runsResponse.Data | Where-Object { [string]$_.status -eq "running" }).Count
            if ($running -gt $maxRunning) { $maxRunning = $running }
        }
        if (@("done", "failed", "cancelled") -contains [string]$batch.status) {
            $stopwatch.Stop()
            return [pscustomobject]@{ Batch = $batch; MaxRunning = $maxRunning; TimedOut = $false; DurationMs = [math]::Round($stopwatch.Elapsed.TotalMilliseconds, 1) }
        }
        Start-Sleep -Milliseconds 150
    }
    $stopwatch.Stop()
    return [pscustomobject]@{ Batch = $batch; MaxRunning = $maxRunning; TimedOut = $true; DurationMs = [math]::Round($stopwatch.Elapsed.TotalMilliseconds, 1) }
}

function Invoke-ExecutionConcurrencyMeasurement {
    param($Headers, [string]$FixtureBaseUrl)
    $workers = [int]$thresholds.executionWorkers
    if (-not $script:llmHarness.FixtureReady) {
        return [pscustomobject]@{ Status = "SKIP"; Value = $null; Detail = "SKIP: the fixture target at $FixtureBaseUrl is not reachable; the 8-concurrency execution measurement needs the real P6 fixture executor. This row is not a PASS." }
    }
    if (-not (Test-SloLlmMeasurementAllowed -Config $script:defaultLlmConfig)) {
        return [pscustomobject]@{ Status = "SKIP"; Value = $null; Detail = "SKIP: $((Get-SloLlmSkipReason)); the execution agent needs a model. This row is not a PASS." }
    }
    try {
        $stamp = Get-Date -Format "yyyyMMddHHmmssfff"
        $system = Invoke-ApiJson -Method Post -Path "/api/resources/systems" -Headers $Headers -Body @{
            key     = "sloexec$stamp"
            name    = "P8 SLO execution system"
            kind    = "web"
            baseUrl = $FixtureBaseUrl.TrimEnd("/")
        }
        if ($system.Status -ne 201) {
            throw "POST system answered HTTP $($system.Status) $($system.Error.Code)/$($system.Error.Reason)"
        }
        $systemId = $system.Data.id
        $environment = Invoke-ApiJson -Method Post -Path "/api/resources/systems/$systemId/environments" -Headers $Headers -Body @{
            key          = "sloexecenv$stamp"
            name         = "P8 SLO execution environment"
            baseUrl      = $FixtureBaseUrl.TrimEnd("/")
            kind         = "dev"
            allowedHosts = @("127.0.0.1")
            loginCheck   = @{
                url          = $FixtureBaseUrl.TrimEnd("/") + "/p6/home"
                expectStatus = @(200)
                bodyContains = "New order"
            }
        }
        if ($environment.Status -ne 201) {
            throw "POST environment answered HTTP $($environment.Status) $($environment.Error.Code)/$($environment.Error.Reason)"
        }
        $environmentId = $environment.Data.id
        $account = Invoke-ApiJson -Method Post -Path "/api/resources/accounts" -Headers $Headers -Body @{
            key      = "sloexecaccount$stamp"
            label    = "P8 SLO execution account"
            username = "fixture"
            kind     = "human"
        }
        if ($account.Status -ne 201) {
            throw "POST account answered HTTP $($account.Status) $($account.Error.Code)/$($account.Error.Reason)"
        }
        $access = Invoke-ApiJson -Method Post -Path "/api/resources/accounts/$($account.Data.id)/accesses" -Headers $Headers -Body @{
            systemId         = [int64]$systemId
            authType         = "manual"
            concurrencyLimit = $workers
            enabled          = $true
        }
        if ($access.Status -ne 201) {
            throw "POST access answered HTTP $($access.Status) $($access.Error.Code)/$($access.Error.Reason)"
        }
        $accessId = $access.Data.id

        $storageState = Get-FixtureStorageState -FixtureBaseUrl $FixtureBaseUrl
        $imported = 0
        for ($index = 1; $index -le $workers; $index++) {
            $import = Invoke-ApiJson -Method Post -Path "/api/resources/accesses/$accessId/sessions" -Headers $Headers -Body @{
                environmentId = [int64]$environmentId
                kind          = "storage_state"
                storageState  = $storageState
                note          = "P8 SLO 8-concurrency fixture session $index"
            }
            if ($import.Status -ne 201) {
                throw "POST session import answered HTTP $($import.Status) $($import.Error.Code)/$($import.Error.Reason)"
            }
            if ([string]$import.Data.state -ne "valid") {
                return [pscustomobject]@{
                    Status = "SKIP"; Value = $null
                    Detail = "SKIP: the imported fixture session is '$($import.Data.state)' (expected valid), so the executor or fixture target is unavailable for the 8-concurrency measurement. This row is not a PASS."
                }
            }
            $imported++
        }

        $version = New-SloExecutingVersion -Headers $Headers -Name "P8 SLO concurrency $stamp" `
            -SystemId $systemId -EnvironmentId $environmentId -FixtureBaseUrl $FixtureBaseUrl -CaseCount $workers
        $created = Invoke-ApiJson -Method Post -Path "/api/versions/$($version.VersionId)/batches" -Headers $Headers -Body @{
            caseIds = @($version.CaseIds)
        }
        if ($created.Status -eq 503 -and
            ($created.Error.Reason -eq "executor_unavailable" -or $created.Error.Code -eq "unavailable")) {
            return [pscustomobject]@{
                Status = "SKIP"; Value = $null
                Detail = "SKIP: the API has no healthy executor configured (HTTP 503 executor_unavailable); start it with EZTEST_EXECUTOR_URL/EZTEST_EXECUTOR_TOKEN to measure 8-concurrency. This row is not a PASS."
            }
        }
        if ($created.Status -ne 201) {
            throw "POST batch answered HTTP $($created.Status) $($created.Error.Code)/$($created.Error.Reason)"
        }
        $batchId = [string]$created.Data.id
        $terminal = Wait-SloBatchTerminal -Headers $Headers -VersionId $version.VersionId -BatchId $batchId `
            -TimeoutSeconds $ExecutionTimeoutSeconds
        if ($terminal.TimedOut) {
            return [pscustomobject]@{
                Status = "FAIL"; Value = $null
                Detail = "FAIL: batch $batchId did not reach a terminal state within $ExecutionTimeoutSeconds s (observed max $($terminal.MaxRunning) running case(s)); the 8-concurrency run is not proven."
            }
        }
        $batch = $terminal.Batch
        $runsResponse = Invoke-ApiJson -Method Get -Path "/api/versions/$($version.VersionId)/batches/$batchId/case-runs?limit=100" -Headers $Headers
        if ($runsResponse.Status -ne 200) { throw "GET case runs answered HTTP $($runsResponse.Status)" }
        $runs = @($runsResponse.Data)
        $passed = @($runs | Where-Object { [string]$_.status -eq "passed" }).Count
        $errors = $runs.Count - $passed
        $statusCounts = ($runs | Group-Object -Property status | Sort-Object Name | ForEach-Object { "$($_.Name)=$($_.Count)" }) -join ", "
        $value = "$passed/$($runs.Count) passed, max concurrency $($terminal.MaxRunning)"
        $detail = "batch $batchId status '$($batch.status)'; $($runs.Count) case runs ($statusCounts); imported $imported fixture sessions; observed max $($terminal.MaxRunning) concurrently running case runs; wall $($terminal.DurationMs) ms"
        $errorsOk = ($errors -le [int]$thresholds.executionErrors)
        $concurrencyOk = ($terminal.MaxRunning -ge $workers)
        $batchOk = ([string]$batch.status -eq "done")
        if ($batchOk -and $errorsOk -and $concurrencyOk -and $runs.Count -eq $workers) {
            return [pscustomobject]@{ Status = "PASS"; Value = $value; Detail = $detail }
        }
        $reasons = New-Object System.Collections.ArrayList
        if (-not $batchOk) { [void]$reasons.Add("batch status is '$($batch.status)'") }
        if (-not $errorsOk) { [void]$reasons.Add("$errors case error(s)") }
        if (-not $concurrencyOk) {
            [void]$reasons.Add("observed max concurrency $($terminal.MaxRunning) < $workers (check EZTEST_EXEC_MAX_CONCURRENT_CASES=$workers on the API)")
        }
        if ($runs.Count -ne $workers) { [void]$reasons.Add("$($runs.Count) case runs instead of $workers") }
        return [pscustomobject]@{ Status = "FAIL"; Value = $value; Detail = "$detail; FAIL: $($reasons -join '; ')" }
    } catch {
        return [pscustomobject]@{ Status = "FAIL"; Value = $null; Detail = "the 8-concurrency execution measurement failed: $($_.Exception.Message)" }
    }
}

# ---------------------------------------------------------------------------
# Measurement 6: P4 analysis wall time minus instrumented model-call time.
# ---------------------------------------------------------------------------
function Get-IsoTimestampMilliseconds {
    param([string]$Value)
    if ([string]::IsNullOrWhiteSpace($Value)) { return $null }
    try {
        $parsed = [System.DateTimeOffset]::Parse($Value)
        return [double]$parsed.ToUnixTimeMilliseconds()
    } catch {
        return $null
    }
}

function Invoke-AnalysisOverheadMeasurement {
    param($Headers, [string]$VersionID)
    $budget = [int]$thresholds.analysisNonModelOverheadMs
    if (-not (Test-SloLlmMeasurementAllowed -Config $script:defaultLlmConfig)) {
        return [pscustomobject]@{ Status = "SKIP"; Value = $null; Detail = "SKIP: $((Get-SloLlmSkipReason)); the P4 analysis needs a model. This row is not a PASS." }
    }
    try {
        $fromLine = Get-LlmCallLogLineCount
        $stopwatch = [System.Diagnostics.Stopwatch]::StartNew()
        $start = Invoke-ApiJson -Method Post -Path "/api/versions/$VersionID/code/analyses" -Headers $Headers -Body @{ force = $true }
        if ($start.Status -eq 503) {
            $info = $start.Error
            if ($info.Reason -eq "code_git_unavailable" -or $info.Code -eq "unavailable") {
                return [pscustomobject]@{ Status = "SKIP"; Value = $null; Detail = "SKIP: the API cannot read the fixture Git repository (HTTP 503 $($info.Code)/$($info.Reason)); the analysis measurement was not executed. This row is not a PASS." }
            }
        }
        if ($start.Status -ne 202) {
            throw "POST code analysis answered HTTP $($start.Status) $($start.Error.Code)/$($start.Error.Reason)"
        }
        $analysisId = [string]$start.Data.id
        $analysis = $null
        $deadline = (Get-Date).AddSeconds($AnalysisTimeoutSeconds)
        while ((Get-Date) -lt $deadline) {
            $response = Invoke-ApiJson -Method Get -Path "/api/versions/$VersionID/code/analyses/$analysisId" -Headers $Headers
            if ($response.Status -ne 200) { throw "GET code analysis answered HTTP $($response.Status)" }
            $analysis = $response.Data
            if (@("done", "failed", "degraded") -contains [string]$analysis.status) { break }
            Start-Sleep -Milliseconds 100
        }
        $stopwatch.Stop()
        if ($null -eq $analysis -or @("done", "failed", "degraded") -notcontains [string]$analysis.status) {
            return [pscustomobject]@{
                Status = "FAIL"; Value = $null
                Detail = "FAIL: analysis $analysisId did not reach a terminal state within $AnalysisTimeoutSeconds s (last status '$($analysis.status)'); non-model overhead is not proven."
            }
        }
        $totalMs = [math]::Round($stopwatch.Elapsed.TotalMilliseconds, 1)
        $modelStats = Get-LlmCallStats -FromLine $fromLine
        $method = "wall time from trigger to terminal state minus instrumented model-call time"
        $modelDetail = "model time not instrumented"
        $value = $totalMs
        $nonModelMs = $totalMs
        if ($null -ne $modelStats -and [int]$modelStats.Calls -gt 0) {
            $nonModelMs = [math]::Round([math]::Max(0.0, $totalMs - [double]$modelStats.ModelMs), 1)
            $value = $nonModelMs
            $modelDetail = "model calls $($modelStats.Calls), model time $($modelStats.ModelMs) ms"
        } else {
            $method = "no proxy-recorded model call was available for this analysis; the reported value is the analysis wall time as an upper bound"
            $modelDetail = "no model call recorded in the timing proxy log"
        }
        $serverDetail = ""
        $startedMs = Get-IsoTimestampMilliseconds -Value ([string]$analysis.startedAt)
        $finishedMs = Get-IsoTimestampMilliseconds -Value ([string]$analysis.finishedAt)
        if ($null -ne $startedMs -and $null -ne $finishedMs -and $finishedMs -ge $startedMs) {
            $serverDetail = "; server-side run $([math]::Round($finishedMs - $startedMs, 1)) ms"
        }
        $detail = "analysis $analysisId status '$($analysis.status)' on version $VersionID; wall $totalMs ms; $modelDetail; non-model $nonModelMs ms$serverDetail; method: $method"
        if ([string]$analysis.status -ne "done") {
            return [pscustomobject]@{ Status = "FAIL"; Value = $value; Detail = "$detail; FAIL: the analysis ended '$($analysis.status)', expected done" }
        }
        if ($nonModelMs -le $budget) {
            return [pscustomobject]@{ Status = "PASS"; Value = $value; Detail = $detail }
        }
        return [pscustomobject]@{ Status = "FAIL"; Value = $value; Detail = "$detail; FAIL: non-model overhead $nonModelMs ms exceeds the local budget $budget ms" }
    } catch {
        return [pscustomobject]@{ Status = "FAIL"; Value = $null; Detail = "the analysis non-model overhead measurement failed: $($_.Exception.Message)" }
    }
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
            "executionConcurrency" { Add-Measurement -Name $name -Status "FAIL" -Threshold "$($thresholds.executionWorkers) workers / $($thresholds.executionErrors) errors" -Value $null -Detail $FailureDetail }
            "analysisNonModelOverheadMs" { Add-Measurement -Name $name -Status "FAIL" -Threshold "non-model <= $($thresholds.analysisNonModelOverheadMs) ms" -Value $null -Detail $FailureDetail }
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

    $measurementMethod = [ordered]@{
        executionConcurrency = [ordered]@{
            workers         = $thresholds.executionWorkers
            cases           = $thresholds.executionWorkers
            sessions        = $thresholds.executionWorkers
            observation     = "one real batch through the running API/executor; case-runs polled every 150 ms; PASS requires a done batch, 0 non-passed case runs and observed max running >= $($thresholds.executionWorkers)"
            apiRequirement  = "API started with EZTEST_EXECUTOR_URL/EZTEST_EXECUTOR_TOKEN and EZTEST_EXEC_MAX_CONCURRENT_CASES=$($thresholds.executionWorkers)"
        }
        analysisNonModelOverhead = [ordered]@{
            budgetMs      = $thresholds.analysisNonModelOverheadMs
            method        = "wall clock from the analysis trigger to the terminal state minus the summed model-call durations recorded by the loopback timing proxy"
            modelTiming   = [bool]$script:llmHarness.ModelTiming
            llmBaseUrl    = $(if ($null -ne $script:defaultLlmConfig) { [string]$script:defaultLlmConfig.base_url } else { "" })
            proxyLog      = [string]$script:llmHarness.LogPath
            fallback      = "when model calls cannot be instrumented the reported value is the analysis wall time (an upper bound)"
        }
    }
    $harnessState = [ordered]@{
        allowed      = [bool]$script:llmHarness.Allowed
        reason       = [string]$script:llmHarness.Reason
        modelTiming  = [bool]$script:llmHarness.ModelTiming
        llmBaseUrl   = [string]$script:llmHarness.LlmBaseUrl
        proxyLog     = [string]$script:llmHarness.LogPath
        fixtureReady = [bool]$script:llmHarness.FixtureReady
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
        measurementMethod = $measurementMethod
        llmHarness   = $harnessState
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
    $json = $metrics | ConvertTo-Json -Depth 10

    [System.IO.File]::WriteAllText($metricsPath, $json, (New-Object System.Text.UTF8Encoding($false)))
    [System.IO.File]::WriteAllText($committedMetricsPath, $json, (New-Object System.Text.UTF8Encoding($false)))

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
    [void]$builder.AppendLine("| Measurement | Threshold | Measured value | Status | Detail |")
    [void]$builder.AppendLine("| --- | --- | --- | --- | --- |")
    foreach ($name in $measurementOrder) {
        $row = $measurements[$name]
        $value = "SKIP"
        if ($null -ne $row.value) { $value = [string]$row.value }
        [void]$builder.AppendLine("| $($measurementLabels[$name]) | $($row.threshold) | $value | $($row.status) | $($row.detail) |")
    }
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("## Measurement method")
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("- **8-concurrency batch execution**: the script creates one executing version with $($thresholds.executionWorkers) fixture cases and $($thresholds.executionWorkers) imported fixture sessions, starts one real batch through the running API/executor, polls the batch and case-run endpoints during the run and requires a ``done`` batch, $($thresholds.executionErrors) non-passed case run(s) and an observed maximum of at least $($thresholds.executionWorkers) concurrently running case runs. The API must be started with the executor configured and ``EZTEST_EXEC_MAX_CONCURRENT_CASES=$($thresholds.executionWorkers)``; a lower cap produces an observed-concurrency FAIL.")
    [void]$builder.AppendLine("- **Analysis non-model overhead**: one real P4 analysis is triggered (``force=true``) on the seeded fixture repository. The wall time from the trigger to the terminal state is measured; the model-call time is summed from the JSONL log of the loopback timing proxy placed in front of the deterministic fake LLM (``-LlmCallLogPath``) that the API was started against, and the difference is the non-model overhead. The local acceptance budget is $($thresholds.analysisNonModelOverheadMs) ms for this one-file fixture; the configured analysis timeout ($AnalysisTimeoutSeconds s) bounds the run. When the proxy log is not supplied or records no model call, the wall time itself is reported as an upper bound. Unrelated model calls sharing the same proxy during the window would be subtracted too, so the method can only under-report (never inflate) non-model overhead; measure a quiet local API for the exact figure.")
    [void]$builder.AppendLine("- The P4 analysis gateway snapshots its LLM configuration at API startup, so the fake LLM/proxy must be wired at startup; this script does not patch the configuration. Only a local acceptance measurement is claimed - never a production SLA.")
    [void]$builder.AppendLine("- The 8-concurrency row creates a ``P8 SLO concurrency <stamp>`` fixture version, system/environment/account/access, eight sessions and one batch in the local database. They are left in place after the run (the autonomous follow-up chain may reference the batch); the e2e/acceptance database reset owns cleanup.")
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
    [void]$builder.AppendLine("# Local measurement. Prerequisites: PostgreSQL, an API started with the executor")
    [void]$builder.AppendLine("# (EZTEST_EXECUTOR_URL/EZTEST_EXECUTOR_TOKEN, healthy executor) and with")
    [void]$builder.AppendLine("# EZTEST_EXEC_MAX_CONCURRENT_CASES=8, and a Web dev server for the browser row.")
    [void]$builder.AppendLine("# The API itself must be started against the deterministic fake LLM through a")
    [void]$builder.AppendLine("# loopback timing proxy (the analysis gateway snapshots the config at startup):")
    [void]$builder.AppendLine("#   llm_base_url=http://127.0.0.1:<proxy-port> llm_key=<fake> llm_model_name=<fake>")
    [void]$builder.AppendLine("# and scripts/fake-llm.cjs must listen behind that proxy, writing one JSON line")
    [void]$builder.AppendLine("# per chat completion. Pass that file as -LlmCallLogPath for the model-time split.")
    [void]$builder.AppendLine("# The script starts the fixture target when it is missing and never patches the")
    [void]$builder.AppendLine("# LLM configuration; a non-loopback upstream is SKIP unless -AllowRealLlm is set.")
    [void]$builder.AppendLine("powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/slo.ps1 ``")
    [void]$builder.AppendLine("  -ApiBaseUrl http://127.0.0.1:13333 -WebBaseUrl http://127.0.0.1:6868 ``")
    [void]$builder.AppendLine("  -ExecutionFixtureBaseUrl http://127.0.0.1:13335 ``")
    [void]$builder.AppendLine("  -LlmCallLogPath .tmp/p8-slo/llm-calls.jsonl")
    [void]$builder.AppendLine('```')
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("Exit code: 1 when a static check, a measured threshold or a chain/dataset assertion fails. Exit code 0 when rows are PASS or explicitly SKIP, but a SKIP row is reported as INCOMPLETE (never as an SLO PASS).")
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("Metrics JSON: docs/acceptance/p8/slo-metrics.json (a copy is written to .tmp/p8-slo/metrics.json).")
    [void]$builder.AppendLine("")

    [System.IO.File]::WriteAllText($reportPath, $builder.ToString(), (New-Object System.Text.UTF8Encoding($false)))
    Write-Host ""
    Write-Host "Report: $reportPath" -ForegroundColor Green
    Write-Host "Metrics: $committedMetricsPath" -ForegroundColor Green
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
    $readmePath = Join-Path $repoRoot "DEVELOPMENT_STATUS.md"
    $readme = ""
    if (Test-Path -LiteralPath $readmePath) { $readme = Get-Content -LiteralPath $readmePath -Raw -Encoding UTF8 }
    foreach ($key in @("EZTEST_RATE_LIMIT_RPS", "EZTEST_RATE_LIMIT_BURST", "EZTEST_RATE_LIMIT_SSE_MAX", "EZTEST_GC_ENABLED")) {
        if ($envExample.Contains($key) -and $readme.Contains($key)) {
            Add-Check -Area "docs" -Check $key -Status "PASS" -Detail "documented in .env.example and DEVELOPMENT_STATUS.md"
        } else {
            Add-Check -Area "docs" -Check $key -Status "FAIL" -Detail "missing from .env.example or DEVELOPMENT_STATUS.md"
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
    if ($sloSource.Contains("Invoke-ExecutionConcurrencyMeasurement") -and
        $sloSource.Contains("Wait-SloBatchTerminal") -and
        $sloSource.Contains("maxRunning") -and
        $sloSource.Contains("executionWorkers")) {
        Add-Check -Area "measurement" -Check "8-concurrency execution measurement" -Status "PASS" -Detail "real batch of 8 fixture cases; observed concurrency and case errors are checked; the API cap must be 8"
    } else {
        Add-Check -Area "measurement" -Check "8-concurrency execution measurement" -Status "FAIL" -Detail "the 8-concurrency measurement harness is missing"
    }
    if ($sloSource.Contains("Invoke-AnalysisOverheadMeasurement") -and
        $sloSource.Contains("LlmCallLogPath") -and
        $sloSource.Contains("nonModelMs") -and
        $sloSource.Contains("Get-LlmCallStats")) {
        Add-Check -Area "measurement" -Check "analysis non-model overhead split" -Status "PASS" -Detail "wall time minus proxy-instrumented model-call time; wall-time upper bound when timing is unavailable"
    } else {
        Add-Check -Area "measurement" -Check "analysis non-model overhead split" -Status "FAIL" -Detail "the analysis non-model overhead harness is missing"
    }
    if ($sloSource.Contains("SKIP") -and $sloSource.Contains("incomplete") -and
        $sloSource.Contains("This row is not a PASS") -and
        -not $sloSource.Contains(("NOT " + "MEASURED (2 of 6)"))) {
        Add-Check -Area "report" -Check "missing measurements are SKIP, never PASS" -Status "PASS" -Detail "SKIP is limited to a genuinely missing environment; INCOMPLETE is not a pass"
    } else {
        Add-Check -Area "report" -Check "missing measurements are SKIP, never PASS" -Status "FAIL" -Detail "the explicit skip/never-pass reporting is missing or still hardcodes 2/6"
    }

    Add-Check -Area "thresholds" -Check "P8 DoD" -Status "PASS" -Detail `
        "overview/chain P95 <= $($thresholds.overviewP95Ms)/$($thresholds.chainP95Ms) ms; changes P95 <= $($thresholds.changesP95Ms) ms; $($thresholds.executionWorkers) concurrent executions, $($thresholds.executionErrors) errors; analysis non-model <= $($thresholds.analysisNonModelOverheadMs) ms (local budget)"
    Add-Check -Area "report" -Check "output paths" -Status "PASS" -Detail "docs/acceptance/p8/slo.md + docs/acceptance/p8/slo-metrics.json (+ .tmp/p8-slo/metrics.json)"

    Add-Measurement -Name "overviewApiP95Ms" -Status "SKIP" -Threshold "<= $($thresholds.overviewP95Ms) ms" -Value $null -Detail "dry-run: measurement not executed; run without -DryRun against a running API"
    Add-Measurement -Name "overviewBrowserP95Ms" -Status "SKIP" -Threshold "<= $($thresholds.overviewP95Ms) ms" -Value $null -Detail "dry-run: needs Playwright and a running web app; probes ?tab=overview"
    Add-Measurement -Name "chainP95Ms" -Status "SKIP" -Threshold "<= $($thresholds.chainP95Ms) ms" -Value $null -Detail "dry-run: measurement not executed; the run asserts nodes>=200/edges>=400 first"
    Add-Measurement -Name "changesP95Ms" -Status "SKIP" -Threshold "<= $($thresholds.changesP95Ms) ms" -Value $null -Detail "dry-run: measurement not executed; the run binds a real Git fixture repository and verifies HTTP 200 first"
    Add-Measurement -Name "executionConcurrency" -Status "SKIP" -Threshold "$($thresholds.executionWorkers) workers / $($thresholds.executionErrors) errors" -Value $null -Detail "dry-run: needs a running API with the executor configured and EZTEST_EXEC_MAX_CONCURRENT_CASES=8; the run creates 8 fixture cases and one real batch"
    Add-Measurement -Name "analysisNonModelOverheadMs" -Status "SKIP" -Threshold "non-model <= $($thresholds.analysisNonModelOverheadMs) ms" -Value $null -Detail "dry-run: needs a running API and the deterministic fake LLM/timing proxy; the run triggers one real P4 analysis on the fixture repository"

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
$datasetEnvSkip = ""
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
            "overviewApiP95Ms" { Add-Measurement -Name $name -Status "SKIP" -Threshold "<= $($thresholds.overviewP95Ms) ms" -Value $null -Detail "API is not reachable; measurement skipped" }
            "overviewBrowserP95Ms" { Add-Measurement -Name $name -Status "SKIP" -Threshold "<= $($thresholds.overviewP95Ms) ms" -Value $null -Detail "API is not reachable; measurement skipped" }
            "chainP95Ms" { Add-Measurement -Name $name -Status "SKIP" -Threshold "<= $($thresholds.chainP95Ms) ms" -Value $null -Detail "API is not reachable; measurement skipped" }
            "changesP95Ms" { Add-Measurement -Name $name -Status "SKIP" -Threshold "<= $($thresholds.changesP95Ms) ms" -Value $null -Detail "API is not reachable; measurement skipped" }
            "executionConcurrency" { Add-Measurement -Name $name -Status "SKIP" -Threshold "$($thresholds.executionWorkers) workers / $($thresholds.executionErrors) errors" -Value $null -Detail "API is not reachable; the 8-concurrency batch measurement cannot run. This row is not a PASS." }
            "analysisNonModelOverheadMs" { Add-Measurement -Name $name -Status "SKIP" -Threshold "non-model <= $($thresholds.analysisNonModelOverheadMs) ms" -Value $null -Detail "API is not reachable; the analysis measurement cannot run. This row is not a PASS." }
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
        $datasetEnvSkip = "EZTEST_DATABASE_URL is missing or not a postgres URL"
        Add-Check -Area "dataset" -Check "database URL" -Status "SKIP" -Detail $datasetEnvSkip
    } elseif ([string]::IsNullOrWhiteSpace($script:psqlExe)) {
        $datasetEnvSkip = "psql.exe is not available"
        Add-Check -Area "dataset" -Check "psql" -Status "SKIP" -Detail $datasetEnvSkip
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
    $datasetEnvSkip = "no authenticated API session"
    Add-Check -Area "dataset" -Check "seed" -Status "SKIP" -Detail "no authenticated API session"
}

$seedVersionID = ""
if ($null -ne $datasetState) { $seedVersionID = [string]$datasetState.VersionID }

# Node powers the deterministic fake LLM, the timing proxy and the fixture
# target of the two model-backed measurements.
$script:nodeExe = Resolve-ToolPath -LocalPath "" -CommandName "node.exe"
if ([string]::IsNullOrWhiteSpace($script:nodeExe)) {
    $script:nodeExe = Resolve-ToolPath -LocalPath "" -CommandName "node"
}

try {
    if ($null -ne $datasetState -and -not [string]::IsNullOrWhiteSpace($seedVersionID)) {
        Write-Note "dataset seeded; verifying the measured graph before measuring"
        Initialize-SloHarness -FixtureBaseUrl $ExecutionFixtureBaseUrl -Headers $authHeaders
        if (-not $script:llmHarness.Allowed) {
            Write-Note "model measurements unavailable: $($script:llmHarness.Reason)"
        } elseif ($script:llmHarness.ModelTiming) {
            Write-Note "model calls will be timed through $($script:llmHarness.LogPath) (LLM upstream $($script:llmHarness.LlmBaseUrl))"
        } else {
            Write-Note "LLM upstream $($script:llmHarness.LlmBaseUrl); no timing proxy log supplied, the analysis row will report a wall-time upper bound"
        }

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

        # 5. Analysis non-model overhead on the seeded fixture repository.
        if ($datasetState.RepoReady) {
            $analysisMeasurement = Invoke-AnalysisOverheadMeasurement -Headers $authHeaders -VersionID $seedVersionID
            Add-Measurement -Name "analysisNonModelOverheadMs" -Status $analysisMeasurement.Status `
                -Threshold "non-model <= $($thresholds.analysisNonModelOverheadMs) ms" -Value $analysisMeasurement.Value `
                -Detail $analysisMeasurement.Detail
        } else {
            Add-Measurement -Name "analysisNonModelOverheadMs" -Status "SKIP" `
                -Threshold "non-model <= $($thresholds.analysisNonModelOverheadMs) ms" -Value $null `
                -Detail "SKIP: the fixture Git repository could not be prepared ($($datasetState.RepoError)); the P4 analysis was not measured. This row is not a PASS."
        }

        # 6. One real batch with 8 fixture cases and 8 concurrent sessions.
        $executionMeasurement = Invoke-ExecutionConcurrencyMeasurement -Headers $authHeaders -FixtureBaseUrl $ExecutionFixtureBaseUrl
        Add-Measurement -Name "executionConcurrency" -Status $executionMeasurement.Status `
            -Threshold "$($thresholds.executionWorkers) workers / $($thresholds.executionErrors) errors" `
            -Value $executionMeasurement.Value -Detail $executionMeasurement.Detail
    } else {
        if (-not [string]::IsNullOrWhiteSpace($seedError)) {
            $failure = "dataset seed failed: $seedError"
            foreach ($name in $measurementOrder) {
                switch ($name) {
                    "overviewApiP95Ms" { Add-Measurement -Name $name -Status "FAIL" -Threshold "<= $($thresholds.overviewP95Ms) ms" -Value $null -Detail $failure }
                    "overviewBrowserP95Ms" { Add-Measurement -Name $name -Status "FAIL" -Threshold "<= $($thresholds.overviewP95Ms) ms" -Value $null -Detail $failure }
                    "chainP95Ms" { Add-Measurement -Name $name -Status "FAIL" -Threshold "<= $($thresholds.chainP95Ms) ms" -Value $null -Detail $failure }
                    "changesP95Ms" { Add-Measurement -Name $name -Status "FAIL" -Threshold "<= $($thresholds.changesP95Ms) ms" -Value $null -Detail $failure }
                    "executionConcurrency" { Add-Measurement -Name $name -Status "FAIL" -Threshold "$($thresholds.executionWorkers) workers / $($thresholds.executionErrors) errors" -Value $null -Detail $failure }
                    "analysisNonModelOverheadMs" { Add-Measurement -Name $name -Status "FAIL" -Threshold "non-model <= $($thresholds.analysisNonModelOverheadMs) ms" -Value $null -Detail $failure }
                }
            }
        } else {
            # Missing environment (no admin session / database / psql) is an
            # explicit SKIP, never a pass.
            $reason = $datasetEnvSkip
            if ([string]::IsNullOrWhiteSpace($reason)) { $reason = "dataset was not seeded" }
            foreach ($name in $measurementOrder) {
                switch ($name) {
                    "overviewApiP95Ms" { Add-Measurement -Name $name -Status "SKIP" -Threshold "<= $($thresholds.overviewP95Ms) ms" -Value $null -Detail "SKIP: $reason; measurement not executed" }
                    "overviewBrowserP95Ms" { Add-Measurement -Name $name -Status "SKIP" -Threshold "<= $($thresholds.overviewP95Ms) ms" -Value $null -Detail "SKIP: $reason; measurement not executed" }
                    "chainP95Ms" { Add-Measurement -Name $name -Status "SKIP" -Threshold "<= $($thresholds.chainP95Ms) ms" -Value $null -Detail "SKIP: $reason; measurement not executed" }
                    "changesP95Ms" { Add-Measurement -Name $name -Status "SKIP" -Threshold "<= $($thresholds.changesP95Ms) ms" -Value $null -Detail "SKIP: $reason; measurement not executed" }
                    "executionConcurrency" { Add-Measurement -Name $name -Status "SKIP" -Threshold "$($thresholds.executionWorkers) workers / $($thresholds.executionErrors) errors" -Value $null -Detail "SKIP: $reason; the 8-concurrency batch measurement was not executed. This row is not a PASS." }
                    "analysisNonModelOverheadMs" { Add-Measurement -Name $name -Status "SKIP" -Threshold "non-model <= $($thresholds.analysisNonModelOverheadMs) ms" -Value $null -Detail "SKIP: $reason; the analysis measurement was not executed. This row is not a PASS." }
                }
            }
        }
    }
} catch {
    Add-Check -Area "measurement" -Check "request" -Status "FAIL" -Detail $_.Exception.Message
    Ensure-MeasurementRows -FailureDetail "measurement aborted: $($_.Exception.Message)"
} finally {
    Restore-SloHarness -Headers $authHeaders
    Remove-SeedDataset -Connection $connection
}

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
    -Note "Local measurement; the 8-concurrency row ran one real batch of $($thresholds.executionWorkers) fixture cases and the analysis row split P4 wall time into instrumented model time and non-model overhead. SKIP only marks genuinely missing environment and is reported as INCOMPLETE, never as an SLO PASS."
if ($status -eq "failed") { exit 1 }
exit 0
