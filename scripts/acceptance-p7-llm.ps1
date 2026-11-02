#requires -Version 5.1
<#
.SYNOPSIS
    Optional real-LLM acceptance for the P7 autonomous loop (gated by
    EZTEST_RUN_REAL_LLM=1).

.DESCRIPTION
    Without the gate this script prints SKIPPED and exits 0 (an explicit,
    intentional gate-closed skip). With the gate open it drives one real-model P7
    acceptance run against the local PostgreSQL and the saved DeepSeek
    configuration in .env. Nothing is faked on the AI side:

      1. gate: EZTEST_RUN_REAL_LLM=1, .env LLM keys, a reachable local database,
         the Go/psql/git/node toolchain and a healthy real LLM configuration;
      2. a dedicated eztest_p7_llm database is rebuilt; the current API is built
         and started on a fixed port with the real DeepSeek configuration;
      3. startup wiring is asserted: the three P7 agent profiles, the delivery /
         supplement tools and the P7 HTTP routes must be present;
      4. seed: a deterministic fixture git repository (commit A), a
         system/environment, a version bound to the repository at commit A, three
         clarified requirements (one deliberately uncovered) and two baseline
         cases;
      5. baseline batch: created through the real API (T17d pins
         run_batch.head_commit), executed by a deterministic "fake executor" that
         only writes the execution result rows - no browser and no execution LLM
         is used - then the real autonomous chain runs: failure analysis (real
         DeepSeek), issue triage (real DeepSeek), report publish and the post-batch
         supplement agent (real DeepSeek);
      6. the supplement agent's proposed cases become an automatic supplement
         batch; the fake executor results it, the report exposes source=ai_supplement
         and the version completes;
      7. continuing: commit B is created AFTER the baseline batch, a new
         requirement moves completed -> continuing/clarifying, the autonomy worker
         automatically starts the P4 impact analysis from the pinned parent head
         (commit A -> commit B), the requirement is confirmed (clarifying ->
         designing) and the real incremental-design agent proposes new/affected
         cases; the incremental batch is created, paused and resumed once,
         fake-executed, and the report/diff/export plus the plan warnings/metadata
         are verified; the version completes again;
      8. scenario 4 (degraded): a SECOND API process with a separate database and
         no llm_* keys is started on a fixed degraded port; a baseline batch runs
         without any LLM, the autonomy chain must degrade instead of crashing, the
         batch must complete, report.content.degraded must be true, and the
         issues/report views (list/detail/export/analysis endpoints) must render;
      9. token usage (per real agent run and totals, must be > 0), wall time,
         event evidence, report/markdown artifacts (report row hash == export
         hash), a final recursive secret scan of ALL evidence (run.json and
         verification.md included) and a post-write re-scan of their exact final
         bytes are written under .tmp/p7-real-llm/ (plus a copy under
         .tmp/p7-llm-latest/).

    Check semantics: every check is required unless it is explicitly optional.
    A failed required check exits non-zero. A skipped required check also exits
    non-zero unless -AllowSkip is passed; with -AllowSkip the run exits 0 but its
    final status is non-green (passed_with_allowed_skips) and the skip reason is
    recorded in run.json/verification.md. Optional skips are reported but never
    turn a run green.

    Evidence ownership: .tmp/p7-real-llm/ and .tmp/p7-llm-latest/ are written
    only by the process that owns the machine-wide run lock. A copy that loses
    the lock race or fails before acquiring it (missing .env/keys or the
    fake-LLM guard) writes .tmp/p7-real-llm-contender.log instead and never
    overwrites or deletes the owner's evidence/latest copies.

    Scenario 4 is evidence for spec §12.7 scenario 4 (degraded/no-LLM): the
    no-LLM failure analysis must end status=degraded AND errors.reason=
    ai_unavailable; a failed status or any other/missing reason is a failure.
    The P4 plan check asserts the plan warnings AND metadata. The T16b run
    starter preseed reads the automatic P4 impact result, so a materialized
    incremental_design run must carry impactAvailable=true without an
    impact_unavailable warning; any other state is a required skip/failure and
    is never claimed as consumed.

.PARAMETER StaticOnly
    Validate the repository layout, the static P7 wiring needles (including the
    T17d CodeHistory wiring) and the .env key presence without starting the
    database, the API or any LLM call.

.PARAMETER AllowSkip
    Accept skips of required checks (for a documented known limitation/budget
    stop). The final status stays non-green and the skip reason is recorded.

.NOTES
    Fixed resources: a MACHINE-WIDE cross-process lock
    (%TEMP%\eztest-p7-real-llm-eztest_p7_llm.lock, keyed by the fixed main
    database and covering both fixed databases plus ports :14343/:14344/:14346)
    prevents two copies from running concurrently even from different
    checkouts; it is held until every evidence/latest write and the final secret
    scans complete and is released only by its owner (including on error). Only
    the lock owner writes .tmp/p7-real-llm/ and .tmp/p7-llm-latest/; a copy that
    loses the lock or fails before acquiring it writes only
    .tmp/p7-real-llm-contender.log and leaves the owner's evidence untouched.
    The API :14343 / fake executor
    :14346 ports are checked before the main database eztest_p7_llm is rebuilt;
    the degraded API :14344 port is checked before its database
    eztest_p7_llm_degraded is rebuilt. The database host
    MUST be loopback (localhost/127.0.0.1/::1) - Assert-LocalDatabaseUri refuses
    anything else, and only the two fixed eztest_p7_llm* database names are ever
    dropped. T29's real-DB acceptance uses its own target database/ports; this
    script never touches eztest_p7_target. Evidence under .tmp/ is gitignored.
    This script makes paid upstream calls (budget guard: 3,000,000 input tokens).
    Secrets are read from .env and never printed; only lengths/statuses are
    reported. Compatible with Windows PowerShell 5.1 and PowerShell 7+.
#>
[CmdletBinding()]
param(
    [switch]$StaticOnly,
    [switch]$AllowSkip,
    [int]$LlmTimeoutSeconds = 900
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$backendDir = Join-Path $repoRoot "backend"
$tmpDir = Join-Path $repoRoot ".tmp"
$envPath = Join-Path $repoRoot ".env"
$evidenceRoot = Join-Path $tmpDir "p7-real-llm"
$latestRoot = Join-Path $tmpDir "p7-llm-latest"
$reportRoot = Join-Path $tmpDir "p7-real-reports"
$evidenceDir = Join-Path $tmpDir "p7-real-evidence"
$storageDir = Join-Path $tmpDir "p7-real-storage"
$fixtureRepoDir = Join-Path $tmpDir "p7-real-llm-repo"
$dbName = "eztest_p7_llm"
$apiPort = 14343
$executorPort = 14346
$executorToken = "p7-real-acceptance-executor"
$apiBaseUrl = "http://127.0.0.1:$apiPort"
# Scenario 4 (degraded/no-LLM) fixed resources. Distinct database and API port;
# the fake executor health service on :14346 is shared read-only by both APIs.
$degradedDbName = "eztest_p7_llm_degraded"
$degradedApiPort = 14344
$degradedBaseUrl = "http://127.0.0.1:$degradedApiPort"
$degradedEvidenceDir = Join-Path $tmpDir "p7-real-evidence-degraded"
$degradedReportRoot = Join-Path $tmpDir "p7-real-reports-degraded"
$degradedStorageDir = Join-Path $tmpDir "p7-real-storage-degraded"
$allowedDatabaseNames = @($dbName, $degradedDbName)
# The run lock is machine-wide (temp directory) and keyed by the fixed main
# database, because the lock protects the fixed databases AND the fixed ports
# regardless of which checkout the script copy lives in. A per-checkout .tmp
# lock would let two checkouts race the same database/ports.
$lockKey = ($dbName -replace '[^A-Za-z0-9_.-]', '_')
$machineLockPath = Join-Path ([System.IO.Path]::GetTempPath()) ("eztest-p7-real-llm-{0}.lock" -f $lockKey)
$contenderLogPath = Join-Path $tmpDir "p7-real-llm-contender.log"
$requiredEnvKeys = @(
    "EZTEST_DATABASE_URL",
    "EZTEST_ADMIN_USERNAME",
    "EZTEST_ADMIN_PASSWORD",
    "llm_base_url",
    "llm_key",
    "llm_model_name"
)
$requiredProfiles = @("failure-analysis-agent", "issue-triage-agent", "supplement-agent")
$requiredTools = @(
    "analysis.case_list", "analysis.case_detail", "analysis.evidence_meta",
    "analysis.code_read", "analysis.candidate_propose", "analysis.finish",
    "triage.candidate_list", "triage.issue_list", "triage.context_read",
    "triage.issue_draft", "triage.finish",
    "supplement.context_read", "supplement.case_propose",
    "supplement.affected_cases_propose", "supplement.escalate", "supplement.finish"
)

$script:secretValues = @()
$script:checks = New-Object System.Collections.ArrayList
$script:timings = New-Object System.Collections.ArrayList
$script:phases = New-Object System.Collections.ArrayList
$script:fakeIdSeed = [int64]900000000000000000
$script:skipReason = ""
$script:skipNotAllowed = $false
$script:failureMessage = $null
$script:apiProcess = $null
$script:executorProcess = $null
$script:degradedApiProcess = $null
$script:lockPath = $machineLockPath
$script:lockStream = $null
$script:lockAttempted = $false
$script:lockAcquired = $false
$script:adminHeaders = $null
$script:envBackup = @{}
$script:allowSkip = [bool]$AllowSkip
$script:startedAt = [DateTime]::UtcNow
$script:result = [ordered]@{
    status          = "running"
    generatedAt     = [DateTime]::UtcNow.ToString("o")
    mode            = "real-llm"
    llmModel        = $null
    llmBaseHost     = $null
    database        = $dbName
    apiPort         = $apiPort
    versionId       = $null
    systemId        = $null
    environmentId   = $null
    codeRepoId      = $null
    codeBaseCommit  = $null
    codeHeadCommit  = $null
    baselineBatchId = $null
    supplementBatch = $null
    issueId         = $null
    analysisIds     = @()
    reportIds       = @()
    baselineReport  = $null
    incrementalReport = $null
    baselineReportHash = $null
    incrementalReportHash = $null
    codeImpact      = $null
    planImpact      = $null
    scenario4       = $null
    allowSkip       = [bool]$AllowSkip
    tokenUsage      = $null
    elapsedMs       = $null
    checks          = @()
    skippedChecks   = @()
    timings         = @()
    secretScan      = $null
    error           = $null
}

function Write-Step {
    param([string]$Message)
    Write-Host ""
    Write-Host "==> $Message" -ForegroundColor Cyan
}

function Write-Note {
    param([string]$Message)
    Write-Host "    $Message" -ForegroundColor DarkGray
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

function Set-ScopedEnv {
    param([string]$Name, [string]$Value)
    if (-not $script:envBackup.ContainsKey($Name)) {
        $script:envBackup[$Name] = [System.Environment]::GetEnvironmentVariable($Name, [System.EnvironmentVariableTarget]::Process)
    }
    Set-Item -Path ("Env:" + $Name) -Value $Value
}

function Clear-ScopedEnv {
    param([string]$Name)
    if (-not $script:envBackup.ContainsKey($Name)) {
        $script:envBackup[$Name] = [System.Environment]::GetEnvironmentVariable($Name, [System.EnvironmentVariableTarget]::Process)
    }
    Remove-Item -Path ("Env:" + $Name) -ErrorAction SilentlyContinue
}

# Restore every env var this script touched (or only the named subset). Used both
# at the end of the run and right after the degraded API process has inherited a
# no-llm_* environment.
function Restore-ScopedEnv {
    param([string[]]$Names = @())
    $targets = if ($Names.Count -gt 0) { $Names } else { @($script:envBackup.Keys) }
    foreach ($name in $targets) {
        if (-not $script:envBackup.ContainsKey($name)) { continue }
        $previous = $script:envBackup[$name]
        if ($null -eq $previous) {
            Remove-Item -Path ("Env:" + $name) -ErrorAction SilentlyContinue
        } else {
            Set-Item -Path ("Env:" + $name) -Value $previous
        }
        $script:envBackup.Remove($name)
    }
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
        # Best-effort cleanup.
    } finally {
        $ErrorActionPreference = $previous
    }
}

function Assert-PortFree {
    param([int]$Port)
    if (Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue) {
        throw "Port $Port is already in use"
    }
}

# Acquire the machine-wide cross-process run lock. The lock is a small JSON file
# (temp directory, keyed by the fixed main database name) opened with
# FileShare.None; a stale file whose recorded PID is no longer alive is removed
# and retried once. Because the fixed databases AND ports are shared by every
# checkout, the lock must be machine-wide: two copies of this script in
# different checkouts must not run concurrently either. A copy that loses the
# race never writes owner evidence; it records a contender log instead.
function Acquire-P7RunLock {
    param([string]$Path)
    if (Test-Path -LiteralPath $Path) {
        $ownerPid = 0
        try {
            $document = [System.IO.File]::ReadAllText($Path) | ConvertFrom-Json
            $ownerPid = [int]$document.pid
        } catch {
            # A truncated/corrupt lock is treated as stale.
        }
        if ($ownerPid -gt 0 -and $null -ne (Get-Process -Id $ownerPid -ErrorAction SilentlyContinue)) {
            throw "another P7 real-LLM acceptance is running (lock $Path, pid $ownerPid); the fixed ports and databases ($dbName, $degradedDbName; :$apiPort/:$degradedApiPort/:$executorPort) cannot be shared"
        }
        Remove-Item -LiteralPath $Path -Force
    }
    try {
        $stream = [System.IO.File]::Open($Path, [System.IO.FileMode]::CreateNew, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
    } catch {
        throw "another P7 real-LLM acceptance is running (lock $Path); the fixed ports and databases cannot be shared"
    }
    $script:lockStream = $stream
    $script:lockAcquired = $true
    $bytes = [System.Text.Encoding]::UTF8.GetBytes((ConvertTo-Json -Compress -InputObject ([ordered]@{
                    pid = $PID; startedAt = [DateTime]::UtcNow.ToString("o"); repoRoot = $repoRoot
                    database = $dbName; degradedDatabase = $degradedDbName
                    ports = @($apiPort, $degradedApiPort, $executorPort)
                })))
    $stream.Write($bytes, 0, $bytes.Length)
    $stream.Flush()
}

function Release-P7RunLock {
    # Only the owner may delete the lock file. A copy that lost the acquisition
    # race must not remove the active run's lock on its error path.
    $owned = [bool]$script:lockAcquired -and $null -ne $script:lockStream
    if ($owned) {
        try { $script:lockStream.Dispose() } catch { }
        $script:lockStream = $null
        $script:lockAcquired = $false
    }
    if (-not $owned) { return }
    if (-not [string]::IsNullOrWhiteSpace($script:lockPath) -and (Test-Path -LiteralPath $script:lockPath)) {
        try { Remove-Item -LiteralPath $script:lockPath -Force -ErrorAction Stop } catch { }
    }
}

# A copy that does not own the run lock must NEVER touch .tmp/p7-real-llm or
# .tmp/p7-llm-latest (they belong to the live owner). Its diagnostics go to a
# separate per-checkout contender log instead. This function is deliberately
# best-effort: a failure to log must not mask the original failure.
function Write-P7ContenderLog {
    param([int]$ExitCode = 1, [string]$Phase = "finalize")
    try {
        $parent = Split-Path -Parent $contenderLogPath
        if (-not [string]::IsNullOrWhiteSpace($parent) -and -not (Test-Path -LiteralPath $parent)) {
            New-Item -ItemType Directory -Force -Path $parent | Out-Null
        }
        $errorText = if ($null -ne $script:result.error) { Get-RedactedText -Text ([string]$script:result.error) } else { "" }
        $skipText = if ([string]::IsNullOrWhiteSpace($script:skipReason)) { "" } else { Get-RedactedText -Text ([string]$script:skipReason) }
        $line = "contender {0} pid={1} phase={2} status={3} exit={4} lock={5} owned=False evidenceUntouched=True skip='{6}' error='{7}' repo='{8}'" -f ([DateTime]::UtcNow.ToString("o")), $PID, $Phase, $script:result.status, $ExitCode, $script:lockPath, $skipText, $errorText, $repoRoot
        $encoding = New-Object System.Text.UTF8Encoding($false)
        [System.IO.File]::AppendAllText($contenderLogPath, $line + [Environment]::NewLine, $encoding)
    } catch {
        # Never fail the contender path because the diagnostic log failed.
    }
}

# The verification.md body is generated by this function so a post-write scan or
# latest-publish failure can rewrite BOTH public evidence files with the same
# failed status (run.json and verification.md must not disagree).
function New-VerificationMarkdown {
    param(
        [object]$FinalScan,
        [bool]$PostWriteScanPassed = $true,
        [string]$PostWriteScanError = ""
    )
    $finalFiles = if ($null -ne $FinalScan -and $null -ne $FinalScan.scannedFiles) { [int]$FinalScan.scannedFiles } elseif ($null -ne $script:result.secretScan -and $null -ne $script:result.secretScan.finalFiles) { [int]$script:result.secretScan.finalFiles } else { 0 }
    $secretCount = if ($null -ne $FinalScan -and $null -ne $FinalScan.secretCount) { [int]$FinalScan.secretCount } else { $script:secretValues.Count }
    $scanPassed = if ($null -ne $FinalScan) { [bool]$FinalScan.passed } elseif ($null -ne $script:result.secretScan -and $null -ne $script:result.secretScan.passed) { [bool]$script:result.secretScan.passed } else { $false }
    $markdown = New-Object System.Text.StringBuilder
    [void]$markdown.AppendLine("# P7 real-LLM acceptance run")
    [void]$markdown.AppendLine("")
    [void]$markdown.AppendLine("- Status: **$($script:result.status)**")
    if ($script:skipReason -ne "") { [void]$markdown.AppendLine("- Skip reason: $($script:skipReason)") }
    if ($null -ne $script:result.error) { [void]$markdown.AppendLine("- Error: $($script:result.error)") }
    [void]$markdown.AppendLine("- Generated: $($script:result.generatedAt)")
    [void]$markdown.AppendLine("- Model: $($script:result.llmModel) at $($script:result.llmBaseHost)")
    [void]$markdown.AppendLine("- Database: $($script:result.database) on local PostgreSQL")
    [void]$markdown.AppendLine("- Version: $($script:result.versionId)")
    [void]$markdown.AppendLine("- Baseline batch: $($script:result.baselineBatchId) (head_commit pinned)")
    [void]$markdown.AppendLine("- Supplement batch: $($script:result.supplementBatch)")
    [void]$markdown.AppendLine("- Incremental batch: $($script:result.incrementalBatchId)")
    [void]$markdown.AppendLine("- AI issue: $($script:result.issueId)")
    [void]$markdown.AppendLine("- Baseline report: $($script:result.baselineReport) sha256=$($script:result.baselineReportHash)")
    [void]$markdown.AppendLine("- Incremental report: $($script:result.incrementalReport) sha256=$($script:result.incrementalReportHash)")
    [void]$markdown.AppendLine("- AllowSkip: $($script:result.allowSkip); required skipped=$($script:result.requiredChecksSkipped), optional skipped=$($script:result.optionalChecksSkipped)")
    [void]$markdown.AppendLine("- Elapsed: $([math]::Round($script:result.elapsedMs / 1000.0, 1))s")
    if ($null -ne $script:result.tokenUsage) {
        $t = $script:result.tokenUsage.total
        [void]$markdown.AppendLine("- Tokens: input=$($t.inputTokens), output=$($t.outputTokens), reasoning=$($t.reasoningTokens), runs=$($t.agentRuns), withinBudget=$($t.withinBudget)")
    }
    if ($null -ne $script:result.planImpact) {
        [void]$markdown.AppendLine("- Plan impact: impactAvailable=$($script:result.planImpact.impactAvailable), consumed=$($script:result.planImpact.consumed), planUnmapped=$($script:result.planImpact.unmappedChanges), p4Candidates=$($script:result.planImpact.p4Candidates), warnings=$(($script:result.planImpact.warnings | ForEach-Object { [string]$_.code }) -join ',')")
    }
    if ($null -ne $script:result.scenario4) {
        [void]$markdown.AppendLine("- Scenario 4 (degraded): database=$($script:result.scenario4.database), apiPort=$($script:result.scenario4.apiPort), batch=$($script:result.scenario4.batchId), batchStatus=$($script:result.scenario4.batchStatus), report=$($script:result.scenario4.reportId), reportDegraded=$($script:result.scenario4.reportDegraded), reportSha256=$($script:result.scenario4.reportHash), failureAnalysis=$($script:result.scenario4.failureAnalysisStatus), failureReason=$($script:result.scenario4.failureAnalysisReason), agentRuns=$($script:result.scenario4.agentRuns)")
    }
    if ($script:result.skippedChecks.Count -gt 0) {
        [void]$markdown.AppendLine("")
        [void]$markdown.AppendLine("## Skipped checks")
        foreach ($skipped in $script:result.skippedChecks) {
            $requiredLabel = if ([bool]$skipped.required) { "required" } else { "optional" }
            [void]$markdown.AppendLine("- [$requiredLabel] $($skipped.name): $($skipped.detail)")
        }
    }
    [void]$markdown.AppendLine("")
    [void]$markdown.AppendLine("## Checks")
    [void]$markdown.AppendLine("")
    [void]$markdown.AppendLine("| Check | Required | Status | Detail |")
    [void]$markdown.AppendLine("|---|---|---|---|")
    foreach ($check in $script:checks) {
        $detailText = ([string]$check.detail).Replace("|", "\|")
        [void]$markdown.AppendLine("| $($check.name) | $([bool]$check.required) | $($check.status) | $detailText |")
    }
    [void]$markdown.AppendLine("")
    [void]$markdown.AppendLine("## Phases")
    foreach ($timing in $script:timings) {
        [void]$markdown.AppendLine("- $($timing.phase): $([math]::Round($timing.elapsedMs / 1000.0, 1))s")
    }
    if ($null -ne $script:result.tokenUsage) {
        [void]$markdown.AppendLine("")
        [void]$markdown.AppendLine("## Real LLM runs")
        [void]$markdown.AppendLine("")
        [void]$markdown.AppendLine("| Source | Kind | Status | Model | Input | Output | Reasoning | Turns |")
        [void]$markdown.AppendLine("|---|---|---|---|---|---|---|---|")
        foreach ($run in $script:result.tokenUsage.allRuns) {
            [void]$markdown.AppendLine("| $($run.source) | $($run.kind) | $($run.status) | $($run.model) | $($run.inputTokens) | $($run.outputTokens) | $($run.reasoningTokens) | $($run.turns) |")
        }
    }
    [void]$markdown.AppendLine("")
    [void]$markdown.AppendLine("## Secret scan")
    [void]$markdown.AppendLine("- Final recursive scan: $finalFiles files (run.json + verification.md included), $secretCount secret values checked, passed=$scanPassed.")
    if ($PostWriteScanPassed) {
        [void]$markdown.AppendLine("- Post-write exact-bytes re-scan: passed=True (the exact final bytes of both files were re-scanned after this write).")
    } else {
        [void]$markdown.AppendLine("- Post-write exact-bytes re-scan: passed=False; error: $PostWriteScanError")
    }
    return $markdown.ToString()
}

# Mark both public evidence files failed after the initial write (post-write scan
# failure or latest-copy failure). Best-effort: the caller still exits non-zero.
function Sync-EvidenceFailure {
    param(
        [string]$Reason,
        [object]$FinalScan = $null,
        [bool]$PostWriteScanPassed = $false,
        [string]$PostWriteScanError = ""
    )
    if (-not $script:lockAcquired) { return }
    $script:result.status = "failed"
    $script:result.error = $Reason
    try {
        if (-not (Test-Path -LiteralPath $evidenceRoot -PathType Container)) { return }
        $runJsonPath = Join-Path $evidenceRoot "run.json"
        $verificationPath = Join-Path $evidenceRoot "verification.md"
        if (Test-Path -LiteralPath $runJsonPath -PathType Leaf) {
            Write-Utf8NoBom -Path $runJsonPath -Text ((ConvertTo-Json -InputObject $script:result -Depth 20) + "`n")
        }
        if (Test-Path -LiteralPath $verificationPath -PathType Leaf) {
            Write-Utf8NoBom -Path $verificationPath -Text (New-VerificationMarkdown -FinalScan $FinalScan -PostWriteScanPassed $PostWriteScanPassed -PostWriteScanError $PostWriteScanError)
        }
    } catch {
        # Best-effort only: the run still fails and prints the original reason.
    }
}

# Print the final verdict only after the evidence (run.json, verification.md and
# the final secret scans) has been written and the run lock released.
function Write-FinalVerdict {
    $requiredSkippedNames = @($script:checks | Where-Object { $_.status -eq "skipped" -and [bool]$_.required } | ForEach-Object { [string]$_.name })
    $optionalSkippedNames = @($script:checks | Where-Object { $_.status -eq "skipped" -and -not [bool]$_.required } | ForEach-Object { [string]$_.name })
    Write-Host ""
    switch ([string]$script:result.status) {
        "passed" { Write-Host "P7 REAL LLM ACCEPTANCE PASSED" -ForegroundColor Green }
        "passed_with_allowed_skips" {
            Write-Host ("P7 REAL LLM ACCEPTANCE PASSED WITH ALLOWED SKIPS (NON-GREEN): " + ($requiredSkippedNames -join ", ")) -ForegroundColor Yellow
        }
        "passed_with_skips" {
            Write-Host ("P7 REAL LLM ACCEPTANCE PASSED WITH OPTIONAL SKIPS (NON-GREEN): " + ($optionalSkippedNames -join ", ")) -ForegroundColor Yellow
        }
        "failed" { Write-Host "P7 REAL LLM ACCEPTANCE FAILED: $($script:result.error)" -ForegroundColor Red }
        "blocked" { Write-Host "BLOCKED (non-zero): $($script:result.error)" -ForegroundColor Red }
        "skipped" { Write-Host "SKIPPED (allowed via -AllowSkip, non-green): $($script:result.skipReason)" -ForegroundColor Yellow }
        default { Write-Host "P7 real-LLM acceptance finished: status=$($script:result.status)" -ForegroundColor Yellow }
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
        $output = & $script:psqlExe -h $script:dbHost -p $script:dbPort -U $script:dbUser -d $Database -tA -c $Sql 2>$null
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
        & $script:psqlExe -h $script:dbHost -p $script:dbPort -U $script:dbUser -d $Database -v ON_ERROR_STOP=1 -q -c $Sql *> $null
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($code -ne 0) { throw "$Operation failed (psql exit $code)" }
}

function Invoke-PsqlFile {
    param([string]$Database, [string]$Path, [string]$Operation)
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        & $script:psqlExe -h $script:dbHost -p $script:dbPort -U $script:dbUser -d $Database -v ON_ERROR_STOP=1 -q -f $Path *> $null
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

# This acceptance DROPS a database. Refuse anything that is not a loopback host so
# a copied .env can never point the drop at a shared/remote PostgreSQL.
function Assert-LocalDatabaseUri {
    param([System.Uri]$DatabaseUri)
    $hostName = [string]$DatabaseUri.Host
    if ($DatabaseUri.IsLoopback) { return }
    if ($hostName -in @("localhost", "127.0.0.1", "::1", "[::1]")) { return }
    throw "EZTEST_DATABASE_URL host '$hostName' is not loopback; the real acceptance only drops local databases"
}

function Assert-AcceptanceDatabaseName {
    param([string]$DatabaseName)
    if ($allowedDatabaseNames -notcontains $DatabaseName) {
        throw "refusing to drop unexpected database '$DatabaseName' (allowed: $($allowedDatabaseNames -join ', '))"
    }
}

# A skip is only allowed (exit 0, non-green) when -AllowSkip was passed. A missing
# LLM/database/toolchain on a gate-open machine is an error by default.
function Complete-Skip {
    param([string]$Reason)
    $script:skipReason = $Reason
    if (-not $script:allowSkip) {
        $script:skipNotAllowed = $true
        return 1
    }
    return 0
}

function Add-SkippedCheck {
    param([string]$Name, [string]$Detail, [bool]$Required = $true)
    Add-CheckResult -Name $Name -Status "skipped" -Detail $Detail -Required $Required
}

function Get-ApiErrorSummary {
    param($ErrorRecord)
    $status = 0
    $reason = ""
    $rawBody = $null
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
    return [pscustomobject]@{
        Status = $status
        Reason = $reason
        Text   = (Get-RedactedText -Text ([string]$ErrorRecord.Exception.Message))
    }
}

function Invoke-Api {
    param(
        [string]$Method,
        [string]$Path,
        $Body = $null,
        $Headers = $null,
        [int]$TimeoutSec = 180,
        [switch]$NoThrow
    )
    $requestArgs = @{
        Method          = $Method
        Uri             = ($apiBaseUrl + $Path)
        TimeoutSec      = $TimeoutSec
        UseBasicParsing = $true
    }
    if ($null -ne $Headers) { $requestArgs["Headers"] = $Headers }
    if ($null -ne $Body) {
        $requestArgs["ContentType"] = "application/json; charset=utf-8"
        $requestArgs["Body"] = [System.Text.Encoding]::UTF8.GetBytes(($Body | ConvertTo-Json -Compress -Depth 20))
    }
    try {
        $response = Invoke-WebRequest @requestArgs
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

function New-FakeId {
    $script:fakeIdSeed = $script:fakeIdSeed + 1
    return $script:fakeIdSeed
}

function ConvertTo-SqlLiteral {
    param([string]$Value)
    return ("'" + $Value.Replace("'", "''") + "'")
}

function Write-Utf8NoBom {
    param([string]$Path, [string]$Text)
    $parent = Split-Path -Parent $Path
    if (-not [string]::IsNullOrWhiteSpace($parent)) { New-Item -ItemType Directory -Force -Path $parent | Out-Null }
    [System.IO.File]::WriteAllText($Path, $Text, (New-Object System.Text.UTF8Encoding($false)))
}

function Read-FileShared {
    param([string]$Path)
    $stream = [System.IO.File]::Open($Path, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read,
        [System.IO.FileShare]::ReadWrite)
    try {
        $reader = New-Object System.IO.StreamReader($stream)
        try { return $reader.ReadToEnd() } finally { $reader.Dispose() }
    } finally {
        $stream.Dispose()
    }
}

function Add-CheckResult {
    param([string]$Name, [string]$Status, [string]$Detail, [bool]$Required = $true)
    [void]$script:checks.Add([pscustomobject]@{ name = $Name; status = $Status; detail = $Detail; required = $Required })
    $color = "Green"
    if ($Status -eq "skipped") { $color = "Yellow" }
    if ($Status -eq "failed") { $color = "Red" }
    $suffix = if ($Status -eq "skipped" -and $Required) { " (required)" } elseif ($Status -eq "skipped") { " (optional)" } else { "" }
    Write-Host ("{0} {1}{2}: {3}" -f $Status.ToUpperInvariant(), $Name, $suffix, $Detail) -ForegroundColor $color
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

function Invoke-OptionalCheck {
    param([string]$Name, [scriptblock]$Body)
    Write-Step "$Name (optional)"
    try {
        & $Body
        Add-CheckResult -Name $Name -Status "passed" -Detail "ok" -Required $false
    } catch {
        Add-CheckResult -Name $Name -Status "skipped" -Detail (Get-RedactedText -Text $_.Exception.Message) -Required $false
    }
}

function Get-EvidencePath {
    param([string]$Name)
    return (Join-Path $evidenceRoot $Name)
}

function Write-EvidenceJson {
    param([string]$Name, $Value)
    $path = Get-EvidencePath -Name $Name
    $json = ConvertTo-Json -InputObject $Value -Depth 20
    Write-Utf8NoBom -Path $path -Text ($json + "`n")
    return $path
}

function Write-EvidenceText {
    param([string]$Name, [string]$Text)
    $path = Get-EvidencePath -Name $Name
    Write-Utf8NoBom -Path $path -Text $Text
    return $path
}

function Complete-Phase {
    param([string]$Name, [datetime]$StartedAt)
    $elapsed = ([DateTime]::UtcNow - $StartedAt).TotalMilliseconds
    [void]$script:timings.Add([pscustomobject]@{ phase = $Name; elapsedMs = [int64]$elapsed })
    [void]$script:phases.Add([pscustomobject]@{
            phase     = $Name
            startedAt = $StartedAt.ToString("o")
            endedAt   = [DateTime]::UtcNow.ToString("o")
            elapsedMs = [int64]$elapsed
        })
    Write-Note ("{0}: {1:N1}s" -f $Name, ($elapsed / 1000.0))
}

function Wait-Until {
    param([scriptblock]$Probe, [int]$TimeoutSeconds, [string]$Label, [int]$IntervalMs = 500)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $last = ""
    while ((Get-Date) -lt $deadline) {
        try {
            $value = & $Probe
            if ($null -ne $value -and $value -ne $false) { return $value }
        } catch {
            $last = Get-RedactedText -Text $_.Exception.Message
        }
        Start-Sleep -Milliseconds $IntervalMs
    }
    throw "$Label timed out after $TimeoutSeconds seconds (last: $last)"
}

function Get-Batch {
    param([string]$VersionId, [string]$BatchId)
    $response = Invoke-Api -Method Get -Path "/api/versions/$VersionId/batches/$BatchId" -Headers $script:adminHeaders
    if ($response.Status -ne 200) { throw "GET batch returned HTTP $($response.Status)" }
    return $response.Data
}

function Get-BatchList {
    param([string]$VersionId)
    $response = Invoke-Api -Method Get -Path "/api/versions/$VersionId/batches?limit=100" -Headers $script:adminHeaders
    if ($response.Status -ne 200) { throw "GET batches returned HTTP $($response.Status)" }
    return @($response.Data)
}

function Get-Version {
    param([string]$VersionId)
    $response = Invoke-Api -Method Get -Path "/api/versions/$VersionId" -Headers $script:adminHeaders
    if ($response.Status -ne 200) { throw "GET version returned HTTP $($response.Status)" }
    return $response.Data
}

function Get-VersionEvents {
    param([string]$VersionId)
    $items = @()
    $beforeSeq = 0
    $pages = 0
    while ($true) {
        $path = "/api/versions/$VersionId/events?limit=100"
        if ($beforeSeq -gt 0) { $path += "&before_seq=$beforeSeq" }
        $events = Invoke-Api -Method Get -Path $path -Headers $script:adminHeaders
        if ($events.Status -ne 200) { throw "GET events returned HTTP $($events.Status)" }
        $page = @($events.Data)
        if ($page.Count -eq 0) { break }
        $items = @($page) + $items
        $beforeSeq = [int64]$page[0].seq
        $pages++
        if ($pages -gt 100) { throw "event snapshot pagination did not terminate" }
        if (-not [bool]$events.Raw.has_more) { break }
    }
    return $items
}

function Get-AnalysisList {
    param([string]$VersionId, [string]$BatchId)
    $response = Invoke-Api -Method Get -Path "/api/versions/$VersionId/batches/$BatchId/analysis" -Headers $script:adminHeaders
    if ($response.Status -ne 200) { throw "GET analysis runs returned HTTP $($response.Status)" }
    return @($response.Data)
}

function Wait-Analysis {
    param([string]$VersionId, [string]$BatchId, [string]$Kind, [int]$TimeoutSeconds = 600)
    return (Wait-Until -Label "$Kind analysis of batch $BatchId" -TimeoutSeconds $TimeoutSeconds -IntervalMs 1000 -Probe {
            $runs = Get-AnalysisList -VersionId $VersionId -BatchId $BatchId
            $run = @($runs | Where-Object { [string]$_.kind -eq $Kind } | Select-Object -First 1)
            if ($run.Count -eq 0) { return $null }
            $status = [string]$run[0].status
            if ($status -in @("done", "failed", "degraded")) { return $run[0] }
            return $null
        })
}

function Get-IssueList {
    param([string]$VersionId)
    $response = Invoke-Api -Method Get -Path "/api/versions/$VersionId/issues?limit=100" -Headers $script:adminHeaders
    if ($response.Status -ne 200) { throw "GET issues returned HTTP $($response.Status)" }
    return @($response.Data)
}

function Wait-Issue {
    param([string]$VersionId, [scriptblock]$Predicate, [int]$TimeoutSeconds = 300)
    return (Wait-Until -Label "AI issue of version $VersionId" -TimeoutSeconds $TimeoutSeconds -IntervalMs 1000 -Probe {
            $issues = Get-IssueList -VersionId $VersionId
            $match = @($issues | Where-Object { & $Predicate $_ } | Select-Object -First 1)
            if ($match.Count -eq 0) { return $null }
            return $match[0]
        })
}

function Get-ReportList {
    param([string]$VersionId, [string]$BatchId)
    $response = Invoke-Api -Method Get -Path "/api/versions/$VersionId/reports?batchId=$BatchId&limit=100" -Headers $script:adminHeaders
    if ($response.Status -ne 200) { throw "GET reports returned HTTP $($response.Status)" }
    return @($response.Data)
}

function Wait-Report {
    param([string]$VersionId, [string]$BatchId, [int]$TimeoutSeconds = 300)
    $summary = Wait-Until -Label "report of batch $BatchId" -TimeoutSeconds $TimeoutSeconds -IntervalMs 1000 -Probe {
        $reports = Get-ReportList -VersionId $VersionId -BatchId $BatchId
        if ($reports.Count -eq 0) { return $null }
        return $reports[0]
    }
    $detail = Invoke-Api -Method Get -Path "/api/versions/$VersionId/reports/$($summary.id)" -Headers $script:adminHeaders
    if ($detail.Status -ne 200) { throw "GET report returned HTTP $($detail.Status)" }
    return $detail.Data
}

function Get-ReportExport {
    param([string]$VersionId, [string]$ReportId, [string]$FileName)
    $client = New-Object System.Net.WebClient
    try {
        $client.Headers.Add("Authorization", [string]$script:adminHeaders["Authorization"])
        $bytes = $client.DownloadData("$apiBaseUrl/api/versions/$VersionId/reports/$ReportId/export?format=markdown")
        $headerSha = [string]$client.ResponseHeaders["X-Content-SHA256"]
    } finally {
        $client.Dispose()
    }
    $sha = Get-Sha256Hex -Bytes $bytes
    if ([string]::IsNullOrWhiteSpace($headerSha)) { throw "Markdown export has no X-Content-SHA256 header" }
    if ($headerSha -ne $sha) { throw "Markdown export sha256 header does not match the bytes" }
    $path = Get-EvidencePath -Name $FileName
    [System.IO.File]::WriteAllBytes($path, $bytes)
    return [pscustomobject]@{
        Bytes = $bytes
        Sha256 = $sha
        HeaderSha256 = $headerSha
        Text = [System.Text.Encoding]::UTF8.GetString($bytes)
        Path = $path
    }
}

function New-FixtureRepository {
    param([string]$Path)
    $tmpRoot = Join-Path $repoRoot ".tmp"
    if ((Split-Path -Leaf $Path) -ne "p7-real-llm-repo") { throw "refusing to rebuild an unexpected fixture directory: $Path" }
    if (-not $Path.StartsWith($tmpRoot, [System.StringComparison]::OrdinalIgnoreCase)) { throw "refusing to rebuild a fixture outside .tmp" }
    if (-not (Get-Command git -ErrorAction SilentlyContinue)) { throw "git was not found on PATH" }
    if (Test-Path -LiteralPath $Path) {
        $resolved = (Resolve-Path -LiteralPath $Path).Path
        if (-not $resolved.StartsWith($tmpRoot, [System.StringComparison]::OrdinalIgnoreCase)) { throw "refusing to delete $resolved" }
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }
    New-Item -ItemType Directory -Force -Path $Path | Out-Null

    $branch = "main"
    $authorName = "EzTest P7 Fixture"
    $authorEmail = "p7-fixture@eztest.invalid"
    $identityEnv = [ordered]@{
        GIT_AUTHOR_NAME     = $authorName
        GIT_AUTHOR_EMAIL    = $authorEmail
        GIT_COMMITTER_NAME  = $authorName
        GIT_COMMITTER_EMAIL = $authorEmail
    }
    $identityKeys = @($identityEnv.Keys) + @("GIT_AUTHOR_DATE", "GIT_COMMITTER_DATE")
    $previousEnvironment = @{}
    foreach ($key in $identityKeys) {
        $previousEnvironment[$key] = [System.Environment]::GetEnvironmentVariable($key, [System.EnvironmentVariableTarget]::Process)
    }
    foreach ($key in @($identityEnv.Keys)) { Set-Item -Path ("Env:" + $key) -Value $identityEnv[$key] }

    function Invoke-FixtureGit {
        param([string[]]$Arguments)
        & git -C $Path @Arguments | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "git $($Arguments -join ' ') failed with exit code $LASTEXITCODE" }
    }
    function Get-FixtureGitText {
        param([string[]]$Arguments)
        $output = & git -C $Path @Arguments
        if ($LASTEXITCODE -ne 0) { throw "git $($Arguments -join ' ') failed with exit code $LASTEXITCODE" }
        return (($output | Out-String).Trim())
    }
    function Write-FixtureFile {
        param([string]$RelativePath, [string[]]$Lines)
        $full = Join-Path $Path $RelativePath
        New-Item -ItemType Directory -Force -Path (Split-Path -Parent $full) | Out-Null
        [System.IO.File]::WriteAllText($full, (($Lines -join "`n") + "`n"), (New-Object System.Text.UTF8Encoding($false)))
    }
    function New-FixtureCommit {
        param([string]$Message, [string]$Timestamp)
        $env:GIT_AUTHOR_DATE = $Timestamp
        $env:GIT_COMMITTER_DATE = $Timestamp
        Invoke-FixtureGit @("add", "--all")
        Invoke-FixtureGit @("commit", "--quiet", "--no-gpg-sign", "-m", $Message)
    }

    try {
        Invoke-FixtureGit @("init", "--quiet")
        Invoke-FixtureGit @("symbolic-ref", "HEAD", "refs/heads/$branch")
        Invoke-FixtureGit @("config", "user.name", $authorName)
        Invoke-FixtureGit @("config", "user.email", $authorEmail)
        Invoke-FixtureGit @("config", "core.autocrlf", "false")
        Invoke-FixtureGit @("config", "commit.gpgsign", "false")

        Write-FixtureFile "src/api/orders.ts" @(
            '// Order API client used by the orders page.'
            'export type Order = { id: string; status: string; amount: string }'
            ''
            'export async function fetchOrder(orderId: string): Promise<Order> {'
            '  const response = await fetch("/api/orders/" + orderId)'
            '  if (!response.ok) throw new Error("load order failed: " + response.status)'
            '  return (await response.json()) as Order'
            '}'
        )
        Write-FixtureFile "src/pages/OrdersPage.tsx" @(
            'import { fetchOrder } from "../api/orders"'
            ''
            'export function OrdersPage({ orderId }: { orderId: string }) {'
            '  const order = fetchOrder(orderId)'
            '  return <OrderDetail order={order} />'
            '}'
        )
        Write-FixtureFile "server/orders.go" @(
            'package server'
            ''
            'import ('
            '	"encoding/json"'
            '	"net/http"'
            ')'
            ''
            'type order struct {'
            '	ID     string `json:"id"`'
            '	Status string `json:"status"`'
            '	Amount string `json:"amount"`'
            '}'
            ''
            'func registerOrderRoutes(mux *http.ServeMux) {'
            '	mux.HandleFunc("GET /api/orders/{id}", handleOrder)'
            '}'
            ''
            'func handleOrder(w http.ResponseWriter, r *http.Request) {'
            '	_ = json.NewEncoder(w).Encode(order{ID: r.PathValue("id"), Status: "created", Amount: "100.00"})'
            '}'
        )
        Write-FixtureFile "src/theme/colors.ts" @(
            'export const colors = { primary: "#2563eb", danger: "#dc2626" }'
        )
        New-FixtureCommit -Message "p7 fixture commit A: orders page and API" -Timestamp "2026-10-01T09:00:00+08:00"
        $baseCommit = Get-FixtureGitText @("rev-parse", "HEAD")
    } finally {
        foreach ($key in $identityKeys) {
            $previous = $previousEnvironment[$key]
            if ($null -eq $previous) { Remove-Item -Path ("Env:" + $key) -ErrorAction SilentlyContinue }
            else { Set-Item -Path ("Env:" + $key) -Value $previous }
        }
    }
    if ($baseCommit -notmatch '^[0-9a-f]{40}$') {
        throw "fixture repository produced an unexpected commit A"
    }
    # Commit B is created LATER, after the baseline batch has been pinned to commit
    # A. That keeps the T17d automatic impact range (parent head A -> current HEAD
    # B) non-empty instead of A -> A; see Add-FixtureCommitB.
    return [pscustomobject]@{ Path = ($Path -replace '\\', '/'); Branch = $branch; BaseCommit = $baseCommit; HeadCommit = $baseCommit }
}

# Add the "cancel order" increment to the existing fixture repository AFTER the
# baseline batch. The baseline batch head_commit was pinned to commit A at batch
# creation; the autonomy worker then analyzes A -> B for the continuing version.
function Add-FixtureCommitB {
    param($Fixture)
    $path = [string]$Fixture.Path
    $tmpRoot = Join-Path $repoRoot ".tmp"
    if ([string]::IsNullOrWhiteSpace($path) -or -not (Test-Path -LiteralPath $path)) { throw "the fixture repository is not ready" }
    $resolved = (Resolve-Path -LiteralPath $path).Path
    if (-not $resolved.StartsWith($tmpRoot, [System.StringComparison]::OrdinalIgnoreCase)) { throw "refusing to commit outside .tmp: $resolved" }
    if ((Get-FixtureHead -Path $path) -ne [string]$Fixture.BaseCommit) {
        throw "the fixture repository is not at commit A; commit B must be added exactly once"
    }

    $authorName = "EzTest P7 Fixture"
    $authorEmail = "p7-fixture@eztest.invalid"
    $envNames = @("GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL",
        "GIT_AUTHOR_DATE", "GIT_COMMITTER_DATE")
    $previous = @{}
    foreach ($name in $envNames) {
        $previous[$name] = [System.Environment]::GetEnvironmentVariable($name, [System.EnvironmentVariableTarget]::Process)
    }
    function Invoke-IncrementGit {
        param([string[]]$Arguments)
        & git -C $path @Arguments | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "git $($Arguments -join ' ') failed with exit code $LASTEXITCODE" }
    }
    function Write-IncrementFile {
        param([string]$RelativePath, [string[]]$Lines)
        $full = Join-Path $path $RelativePath
        New-Item -ItemType Directory -Force -Path (Split-Path -Parent $full) | Out-Null
        [System.IO.File]::WriteAllText($full, (($Lines -join "`n") + "`n"), (New-Object System.Text.UTF8Encoding($false)))
    }
    try {
        Set-Item -Path "Env:GIT_AUTHOR_NAME" -Value $authorName
        Set-Item -Path "Env:GIT_AUTHOR_EMAIL" -Value $authorEmail
        Set-Item -Path "Env:GIT_COMMITTER_NAME" -Value $authorName
        Set-Item -Path "Env:GIT_COMMITTER_EMAIL" -Value $authorEmail
        Set-Item -Path "Env:GIT_AUTHOR_DATE" -Value "2026-10-02T09:00:00+08:00"
        Set-Item -Path "Env:GIT_COMMITTER_DATE" -Value "2026-10-02T09:00:00+08:00"

        Write-IncrementFile "src/api/orders.ts" @(
            '// Order API client used by the orders page.'
            'export type Order = { id: string; status: string; amount: string }'
            ''
            'export async function fetchOrder(orderId: string): Promise<Order> {'
            '  const response = await fetch("/api/orders/" + orderId)'
            '  if (!response.ok) throw new Error("load order failed: " + response.status)'
            '  return (await response.json()) as Order'
            '}'
            ''
            'export async function cancelOrder(orderId: string): Promise<void> {'
            '  const response = await fetch("/api/orders/" + orderId + "/cancel", { method: "POST" })'
            '  if (!response.ok) throw new Error("cancel order failed: " + response.status)'
            '}'
        )
        Write-IncrementFile "src/pages/OrdersPage.tsx" @(
            'import { cancelOrder, fetchOrder } from "../api/orders"'
            ''
            'export function OrdersPage({ orderId }: { orderId: string }) {'
            '  const order = fetchOrder(orderId)'
            '  const cancel = async () => { await cancelOrder(orderId) }'
            '  return <OrderDetail order={order} onCancel={cancel} />'
            '}'
        )
        Write-IncrementFile "server/orders.go" @(
            'package server'
            ''
            'import ('
            '	"encoding/json"'
            '	"net/http"'
            ')'
            ''
            'type order struct {'
            '	ID     string `json:"id"`'
            '	Status string `json:"status"`'
            '	Amount string `json:"amount"`'
            '}'
            ''
            'func registerOrderRoutes(mux *http.ServeMux) {'
            '	mux.HandleFunc("GET /api/orders/{id}", handleOrder)'
            '	mux.HandleFunc("POST /api/orders/{id}/cancel", handleCancelOrder)'
            '}'
            ''
            'func handleOrder(w http.ResponseWriter, r *http.Request) {'
            '	_ = json.NewEncoder(w).Encode(order{ID: r.PathValue("id"), Status: "created", Amount: "100.00"})'
            '}'
            ''
            'func handleCancelOrder(w http.ResponseWriter, r *http.Request) {'
            '	_ = json.NewEncoder(w).Encode(order{ID: r.PathValue("id"), Status: "cancelled", Amount: "100.00"})'
            '}'
        )
        Write-IncrementFile "src/theme/colors.ts" @(
            'export const colors = { primary: "#2563eb", danger: "#b91c1c", cancelled: "#6b7280" }'
        )
        Invoke-IncrementGit @("add", "--all")
        Invoke-IncrementGit @("commit", "--quiet", "--no-gpg-sign", "-m", "p7 fixture commit B: cancel order flow")
        $headCommit = Get-FixtureHead -Path $path
    } finally {
        foreach ($name in $envNames) {
            if ($null -eq $previous[$name]) { Remove-Item -Path ("Env:" + $name) -ErrorAction SilentlyContinue }
            else { Set-Item -Path ("Env:" + $name) -Value $previous[$name] }
        }
    }
    if ($headCommit -notmatch '^[0-9a-f]{40}$' -or $headCommit -eq [string]$Fixture.BaseCommit) {
        throw "fixture commit B did not advance the repository"
    }
    return [pscustomobject]@{ HeadCommit = $headCommit }
}

function Get-FixtureHead {
    param([string]$Path)
    $output = & git -C $Path rev-parse HEAD
    if ($LASTEXITCODE -ne 0) { throw "git rev-parse HEAD failed with exit code $LASTEXITCODE" }
    return (($output | Out-String).Trim())
}

function New-P7World {
    $stamp = Get-Date -Format "HHmmssfff"
    $fixtureUrl = "http://127.0.0.1:13335"

    $system = Invoke-Api -Method Post -Path "/api/resources/systems" -Headers $script:adminHeaders -Body @{
        key     = "p7llm-sys-$stamp"
        name    = "P7 real LLM system"
        kind    = "web"
        baseUrl = $fixtureUrl
    }
    if ($system.Status -ne 201) { throw "POST system returned HTTP $($system.Status)" }

    $environment = Invoke-Api -Method Post -Path "/api/resources/systems/$($system.Data.id)/environments" -Headers $script:adminHeaders -Body @{
        key          = "p7llm-env-$stamp"
        name         = "P7 real LLM environment"
        baseUrl      = $fixtureUrl
        kind         = "dev"
        allowedHosts = @("127.0.0.1")
    }
    if ($environment.Status -ne 201) { throw "POST environment returned HTTP $($environment.Status)" }

    if ([string]::IsNullOrWhiteSpace($script:repoFixture.Path)) { throw "the fixture repository is not ready" }
    $codeRepo = Invoke-Api -Method Post -Path "/api/resources/code-repos" -Headers $script:adminHeaders -Body @{
        key         = "p7llm-repo-$stamp"
        name        = "P7 real LLM repository"
        localPath   = $script:repoFixture.Path
        fixedBranch = $script:repoFixture.Branch
    }
    if ($codeRepo.Status -ne 201) { throw "POST code repository returned HTTP $($codeRepo.Status)" }

    $version = Invoke-Api -Method Post -Path "/api/versions" -Headers $script:adminHeaders -Body @{
        name               = "P7 real LLM version $stamp"
        label              = "p7-llm-$stamp"
        description        = "P7 autonomous-loop real-LLM acceptance version"
        systemId           = [int64]$system.Data.id
        environmentId      = [int64]$environment.Data.id
        codeRepositoryId   = [int64]$codeRepo.Data.id
        codeBranch         = $script:repoFixture.Branch
        codeBaselineCommit = $script:repoFixture.BaseCommit
    }
    if ($version.Status -ne 201) { throw "POST version returned HTTP $($version.Status)" }
    $versionId = [string]$version.Data.id

    $requirementSpecs = @(
        @{ title = "订单列表与状态展示"; description = "用户打开订单列表和订单详情，页面展示订单状态与金额。"; acceptanceCriteria = "订单状态与金额来自订单服务并在页面正确展示。"; priority = "must" },
        @{ title = "取消订单并恢复库存"; description = "用户可以在订单详情取消未支付订单，取消后状态为 cancelled 且库存恢复。"; acceptanceCriteria = "取消成功后订单状态变为 cancelled，库存数量恢复。"; priority = "must" },
        @{ title = "退款原因校验与提示"; description = "用户申请退款时必须填写原因，原因缺失时页面提示错误。"; acceptanceCriteria = "未填写原因提交退款时显示校验错误且不发起请求。"; priority = "should" }
    )
    $requirementIds = @()
    foreach ($spec in $requirementSpecs) {
        $requirement = Invoke-Api -Method Post -Path "/api/versions/$versionId/requirements" -Headers $script:adminHeaders -Body @{
            title              = $spec.title
            description        = $spec.description
            acceptanceCriteria = $spec.acceptanceCriteria
            priority           = $spec.priority
        }
        if ($requirement.Status -ne 201) { throw "POST requirement returned HTTP $($requirement.Status)" }
        $requirementIds += [string]$requirement.Data.id
    }

    $clarify = Invoke-Api -Method Post -Path "/api/versions/$versionId/start-clarification" -Headers $script:adminHeaders
    if ($clarify.Status -ne 200) { throw "POST start-clarification returned HTTP $($clarify.Status)" }
    foreach ($requirementId in $requirementIds) {
        $confirmed = Invoke-Api -Method Post -Path "/api/versions/$versionId/requirements/$requirementId/confirm" -Headers $script:adminHeaders
        if ($confirmed.Status -ne 200) { throw "POST requirement confirm returned HTTP $($confirmed.Status)" }
    }
    $enter = Invoke-Api -Method Post -Path "/api/versions/$versionId/enter-design" -Headers $script:adminHeaders -Body @{ acknowledgeUnclarified = $true }
    if ($enter.Status -ne 200) { throw "POST enter-design returned HTTP $($enter.Status)" }

    $scenario = Invoke-Api -Method Post -Path "/api/versions/$versionId/mindmap/nodes" -Headers $script:adminHeaders -Body @{
        kind  = "scenario"
        title = "P7 订单场景"
    }
    if ($scenario.Status -ne 201) { throw "POST scenario returned HTTP $($scenario.Status)" }

    $caseSpecs = @(
        @{
            title       = "订单列表展示"
            actions     = @("打开 /p6/orders", "检查订单列表")
            expected    = @("订单列表页可见", "列表中有至少一条订单")
            target      = "body"
            expression  = "text contains P6-ORDER-1"
            assertion   = "订单列表包含 P6-ORDER-1"
        },
        @{
            title       = "订单状态展示：已支付订单"
            actions     = @("打开 /p6/orders/1", "检查订单状态")
            expected    = @("订单详情页可见", "订单状态显示 paid")
            target      = "#order-status"
            expression  = "text equals paid"
            assertion   = "paid"
        }
    )
    $caseIds = @()
    foreach ($spec in $caseSpecs) {
        $caseBody = @{
            kind           = "case"
            parentId       = [string]$scenario.Data.id
            title          = $spec.title
            priority       = "P0"
            requirementIds = @($requirementIds[0])
        }
        $case = Invoke-Api -Method Post -Path "/api/versions/$versionId/mindmap/nodes" -Headers $script:adminHeaders -Body $caseBody
        if ($case.Status -ne 201) { throw "POST case returned HTTP $($case.Status)" }
        $caseId = [string]$case.Data.id
        $stepIds = @()
        for ($i = 0; $i -lt $spec.actions.Count; $i++) {
            $step = Invoke-Api -Method Post -Path "/api/versions/$versionId/mindmap/nodes/$caseId/steps" -Headers $script:adminHeaders -Body @{
                action   = $spec.actions[$i]
                expected = $spec.expected[$i]
            }
            if ($step.Status -ne 201) { throw "POST case step returned HTTP $($step.Status)" }
            $stepIds += [string]$step.Data.id
        }
        $assertion = Invoke-Api -Method Post -Path "/api/versions/$versionId/mindmap/nodes/$caseId/assertions" -Headers $script:adminHeaders -Body @{
            stepId     = $stepIds[$stepIds.Count - 1]
            kind       = "ui"
            target     = $spec.target
            expression = $spec.expression
            expected   = $spec.assertion
        }
        if ($assertion.Status -ne 201) { throw "POST case assertion returned HTTP $($assertion.Status)" }
        $caseIds += $caseId
    }

    $submit = Invoke-Api -Method Post -Path "/api/versions/$versionId/mindmap/submit-review" -Headers $script:adminHeaders -Body @{}
    if ($submit.Status -ne 200) { throw "POST submit-review returned HTTP $($submit.Status)" }
    $approve = Invoke-Api -Method Post -Path "/api/versions/$versionId/mindmap/review" -Headers $script:adminHeaders -Body @{
        decision            = "approve"
        acknowledgeFindings = $true
    }
    if ($approve.Status -ne 200) { throw "POST review approve returned HTTP $($approve.Status)" }
    if ([string]$approve.Data.version.status -ne "executing") {
        throw "approval left the version in '$($approve.Data.version.status)'"
    }

    return [pscustomobject]@{
        SystemId      = [string]$system.Data.id
        EnvironmentId = [string]$environment.Data.id
        CodeRepoId    = [string]$codeRepo.Data.id
        VersionId     = $versionId
        CaseIds       = $caseIds
        RequirementIds = $requirementIds
    }
}

function Freeze-Version {
    param([string]$VersionId)
    Invoke-PsqlQuiet -Database $dbName -Sql "UPDATE version SET paused = true, updated_at = now() WHERE id = $VersionId;" -Operation "freeze version $VersionId"
}

function Unfreeze-Version {
    param([string]$VersionId)
    Invoke-PsqlQuiet -Database $dbName -Sql "UPDATE version SET paused = false, pause_mode = 'graceful', updated_at = now() WHERE id = $VersionId;" -Operation "unfreeze version $VersionId"
}

function Get-BatchCaseRuns {
    param([string]$BatchId)
    $sql = "SELECT COALESCE(json_agg(json_build_object('id', cr.id, 'caseNodeId', cr.case_node_id, 'status', cr.status, 'snapshot', cr.case_snapshot) ORDER BY cr.id), '[]'::json)::text FROM case_run cr WHERE cr.batch_id = $BatchId;"
    $raw = Get-PsqlScalar -Database $dbName -Sql $sql
    if ([string]::IsNullOrWhiteSpace($raw)) { return @() }
    return @($raw | ConvertFrom-Json)
}

function Invoke-FakeExecution {
    param(
        [string]$VersionId,
        [string]$BatchId,
        [string]$Label,
        [string[]]$FailTitlePatterns = @()
    )
    $runs = Get-BatchCaseRuns -BatchId $BatchId
    if ($runs.Count -eq 0) { throw "batch $BatchId has no case runs" }
    $pngBytes = [System.Convert]::FromBase64String(
        "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
    )
    $sql = New-Object System.Text.StringBuilder
    [void]$sql.AppendLine("BEGIN;")
    $passed = 0
    $failed = 0
    $failedCaseTitles = @()

    foreach ($run in $runs) {
        if ([string]$run.status -ne "pending") {
            throw "case run $($run.id) is '$($run.status)', not pending; the scheduler raced the fake executor"
        }
        $runId = [string]$run.id
        $runIdNum = [int64]$run.id
        $snapshot = $run.snapshot
        $title = [string]$snapshot.node.title
        $isFail = $false
        foreach ($pattern in $FailTitlePatterns) {
            if ($title -like "*$pattern*") { $isFail = $true; break }
        }
        $assertions = @($snapshot.assertions)
        $steps = @($snapshot.steps)
        $failedAssertion = $null
        if ($isFail) {
            if ($assertions.Count -eq 0) { throw "failed case '$title' has no assertion to fail" }
            $failedAssertion = $assertions[0]
        }
        $failedSeq = 0
        if ($null -ne $failedAssertion) { $failedSeq = [int]$failedAssertion.step_seq }

        $stepIds = @{}
        $failedStepId = [int64]0
        foreach ($step in $steps) {
            $seq = [int]$step.seq
            $stepId = New-FakeId
            $stepIds[$seq] = $stepId
            $stepStatus = "passed"
            $actual = "页面确认：" + [string]$step.expected
            if ($isFail -and $seq -eq $failedSeq) {
                $stepStatus = "failed"
                $actual = "订单状态为 created，与期望的 paid 不一致"
                $failedStepId = $stepId
            }
            $evidenceCount = 1
            [void]$sql.AppendLine(("INSERT INTO step_result (id, batch_id, case_run_id, seq, action, expected, actual, status, evidence_count, started_at, finished_at) VALUES ({0},{1},{2},{3},{4},{5},{6},{7},{8}, now() - interval '1 second', now());" -f `
                        $stepId, $BatchId, $runIdNum, $seq,
                        (ConvertTo-SqlLiteral ([string]$step.action)),
                        (ConvertTo-SqlLiteral ([string]$step.expected)),
                        (ConvertTo-SqlLiteral $actual), (ConvertTo-SqlLiteral $stepStatus), $evidenceCount))
        }
        if ($failedStepId -eq 0 -and $steps.Count -gt 0) {
            $failedStepId = [int64]$stepIds[[int]$steps[$steps.Count - 1].seq]
        }

        $assertionResultIds = @()
        $failedAssertionResultId = [int64]0
        foreach ($assertion in $assertions) {
            $assertionResultId = New-FakeId
            $assertionResultIds += $assertionResultId
            $stepResultId = "NULL"
            if ($null -ne $assertion.step_seq -and $stepIds.ContainsKey([int]$assertion.step_seq)) {
                $stepResultId = [string]$stepIds[[int]$assertion.step_seq]
            }
            $verdict = "passed"
            $actual = [string]$assertion.expected
            $failureReason = ""
            if ($isFail -and $null -ne $failedAssertion -and [string]$assertion.id -eq [string]$failedAssertion.id) {
                $verdict = "failed"
                $actual = "created"
                $failureReason = "assertion_mismatch"
                $failedAssertionResultId = $assertionResultId
            }
            [void]$sql.AppendLine(("INSERT INTO assertion_result (id, batch_id, case_run_id, step_result_id, assertion_id, kind, target, expected, actual, verdict, failure_reason) VALUES ({0},{1},{2},{3},{4},{5},{6},{7},{8},{9},{10});" -f `
                        $assertionResultId, $BatchId, $runIdNum, $stepResultId, [string]$assertion.id,
                        (ConvertTo-SqlLiteral ([string]$assertion.kind)),
                        (ConvertTo-SqlLiteral ([string]$assertion.target)),
                        (ConvertTo-SqlLiteral ([string]$assertion.expected)),
                        (ConvertTo-SqlLiteral $actual), (ConvertTo-SqlLiteral $verdict),
                        (ConvertTo-SqlLiteral $failureReason)))
        }

        $evidenceId = New-FakeId
        $relativePath = "$BatchId/$runId/p7-fake-$evidenceId.png"
        $absolutePath = Join-Path $evidenceDir $relativePath
        New-Item -ItemType Directory -Force -Path (Split-Path -Parent $absolutePath) | Out-Null
        [System.IO.File]::WriteAllBytes($absolutePath, $pngBytes)
        $sha = Get-Sha256Hex -Bytes $pngBytes
        [void]$sql.AppendLine(("INSERT INTO evidence (id, batch_id, case_run_id, step_result_id, kind, path, sha256, size_bytes, mime, metadata) VALUES ({0},{1},{2},{3},'screenshot',{4},{5},{6},'image/png',jsonb_build_object('probe','p7-fake-executor'));" -f `
                    $evidenceId, $BatchId, $runIdNum, $failedStepId,
                    (ConvertTo-SqlLiteral $relativePath), (ConvertTo-SqlLiteral $sha), $pngBytes.Length))
        [void]$sql.AppendLine(("INSERT INTO evidence_ref (evidence_id, ref_kind, ref_id) VALUES ({0},'case_run',{1});" -f $evidenceId, $runIdNum))
        if ($failedStepId -gt 0) {
            [void]$sql.AppendLine(("INSERT INTO evidence_ref (evidence_id, ref_kind, ref_id) VALUES ({0},'step_result',{1});" -f $evidenceId, $failedStepId))
        }
        if ($failedAssertionResultId -gt 0) {
            [void]$sql.AppendLine(("INSERT INTO evidence_ref (evidence_id, ref_kind, ref_id) VALUES ({0},'assertion_result',{1});" -f $evidenceId, $failedAssertionResultId))
        }

        if ($isFail) {
            $failed++
            $failedCaseTitles += $title
            $failureReason = "断言失败：订单状态为 created，期望 paid"
            [void]$sql.AppendLine(("UPDATE case_run SET status = 'failed', failure_class = 'application', failure_reason = {0}, summary = {1}, started_at = now() - interval '2 seconds', finished_at = now(), duration_ms = 2000, updated_at = now() WHERE id = {2};" -f `
                        (ConvertTo-SqlLiteral $failureReason), (ConvertTo-SqlLiteral ("失败：" + $title)), $runIdNum))
        } else {
            $passed++
            [void]$sql.AppendLine(("UPDATE case_run SET status = 'passed', failure_class = NULL, failure_reason = '', summary = {0}, started_at = now() - interval '2 seconds', finished_at = now(), duration_ms = 2000, updated_at = now() WHERE id = {1};" -f `
                        (ConvertTo-SqlLiteral ("通过：" + $title)), $runIdNum))
        }
    }

    $caseCount = $passed + $failed
    [void]$sql.AppendLine(("UPDATE run_batch SET status = 'done', paused = false, passed_count = {0}, failed_count = {1}, blocked_count = 0, inconclusive_count = 0, skipped_count = 0, interrupted_count = 0, case_count = {2}, started_at = COALESCE(started_at, now() - interval '3 seconds'), finished_at = now(), updated_at = now() WHERE id = {3};" -f `
                $passed, $failed, $caseCount, $BatchId))
    [void]$sql.AppendLine("COMMIT;")

    $sqlPath = Join-Path $tmpDir ("p7-fake-execution-" + $BatchId + ".sql")
    Write-Utf8NoBom -Path $sqlPath -Text $sql.ToString()
    try {
        Invoke-PsqlFile -Database $dbName -Path $sqlPath -Operation "fake execution of batch $BatchId"
    } finally {
        Remove-Item -LiteralPath $sqlPath -ErrorAction SilentlyContinue
    }
    $batch = Get-Batch -VersionId $VersionId -BatchId $BatchId
    if ([string]$batch.status -ne "done") { throw "fake execution left batch $BatchId in '$($batch.status)'" }
    Write-Note ("{0}: fake executor wrote {1} passed / {2} failed case results" -f $Label, $passed, $failed)
    return [pscustomobject]@{ Passed = $passed; Failed = $failed; FailedTitles = $failedCaseTitles }
}

function Get-AgentUsage {
    param([string]$VersionId)
    $analysisSql = @"
SELECT COALESCE(json_agg(json_build_object(
  'source', 'analysis_run', 'kind', ar.kind, 'status', ar.status, 'model', ar.model,
  'promptVersion', ar.prompt_version, 'agentRunId', COALESCE(ar.agent_run_id, 0),
  'inputTokens', COALESCE(r.input_tokens, 0), 'outputTokens', COALESCE(r.output_tokens, 0),
  'reasoningTokens', COALESCE(r.reasoning_tokens, 0), 'turns', COALESCE(r.turns, 0),
  'startedAtMs', COALESCE((extract(epoch from r.started_at) * 1000)::bigint, 0),
  'endedAtMs', COALESCE((extract(epoch from r.ended_at) * 1000)::bigint, 0)
) ORDER BY ar.id), '[]'::json)::text
FROM analysis_run ar
LEFT JOIN agent_run r ON r.id = ar.agent_run_id
WHERE ar.version_id = $VersionId;
"@
    $designSql = @"
SELECT COALESCE(json_agg(json_build_object(
  'source', 'case_design_run', 'kind', dr.kind, 'status', dr.status, 'model', dr.model,
  'promptVersion', dr.prompt_version, 'agentRunId', COALESCE(dr.agent_run_id, 0),
  'inputTokens', COALESCE(r.input_tokens, 0), 'outputTokens', COALESCE(r.output_tokens, 0),
  'reasoningTokens', COALESCE(r.reasoning_tokens, 0), 'turns', COALESCE(r.turns, 0),
  'startedAtMs', COALESCE((extract(epoch from r.started_at) * 1000)::bigint, 0),
  'endedAtMs', COALESCE((extract(epoch from r.ended_at) * 1000)::bigint, 0)
) ORDER BY dr.id), '[]'::json)::text
FROM case_design_run dr
LEFT JOIN agent_run r ON r.id = dr.agent_run_id
WHERE dr.version_id = $VersionId AND dr.kind IN ('supplement', 'incremental_design');
"@
    $totalsSql = @"
SELECT COALESCE(SUM(r.input_tokens), 0)::text || '|' ||
       COALESCE(SUM(r.output_tokens), 0)::text || '|' ||
       COALESCE(SUM(r.reasoning_tokens), 0)::text || '|' ||
       COUNT(r.id)::text
FROM agent_run r
WHERE r.session_id IN (
  SELECT agent_session_id FROM analysis_run
    WHERE version_id = $VersionId AND agent_session_id IS NOT NULL
  UNION
  SELECT agent_session_id FROM case_design_run
    WHERE version_id = $VersionId AND agent_session_id IS NOT NULL
);
"@
    # PS 5.1: do not wrap the ConvertFrom-Json pipeline in @(); it would keep the
    # decoded array as a single nested element. Only the direct assignment is
    # enumerated correctly, then the null case is normalized to an empty list.
    $analysis = (Get-PsqlScalar -Database $dbName -Sql $analysisSql) | ConvertFrom-Json
    if ($null -eq $analysis) { $analysis = @() }
    $design = (Get-PsqlScalar -Database $dbName -Sql $designSql) | ConvertFrom-Json
    if ($null -eq $design) { $design = @() }
    $totalRaw = Get-PsqlScalar -Database $dbName -Sql $totalsSql
    $parts = $totalRaw -split '\|'
    $total = [pscustomobject]@{
        inputTokens     = [int64]$parts[0]
        outputTokens    = [int64]$parts[1]
        reasoningTokens = [int64]$parts[2]
        agentRuns       = [int64]$parts[3]
        budgetInput     = 3000000
        withinBudget    = ([int64]$parts[0] -le 3000000)
    }
    return [pscustomobject]@{
        analysisRuns     = @($analysis)
        caseDesignRuns   = @($design)
        total            = $total
        allRuns          = @($analysis) + @($design)
    }
}

function Assert-NoSecretsInText {
    param([string]$Label, [string]$Text)
    foreach ($secret in $script:secretValues) {
        if ([string]::IsNullOrEmpty($secret) -or $secret.Length -lt 6) { continue }
        if ($Text.Contains($secret)) { throw "secret leaked into $Label" }
    }
}

function Invoke-SecretScan {
    $files = @()
    if (Test-Path -LiteralPath $evidenceRoot) {
        $files += @(Get-ChildItem -LiteralPath $evidenceRoot -Recurse -File | Where-Object {
                $_.Extension -in @(".log", ".json", ".md", ".txt")
            })
    }
    foreach ($file in $files) {
        $text = Read-FileShared -Path $file.FullName
        Assert-NoSecretsInText -Label $file.FullName -Text $text
    }
    return [pscustomobject]@{ scannedFiles = $files.Count; secretCount = $script:secretValues.Count; passed = $true }
}

# Scenario 4 of spec §12.7: no LLM at all. A second API process on a separate
# database/port starts without the llm_* environment, a baseline batch is fake
# executed, and the autonomous chain must degrade (not crash), still publish a
# report with content.degraded=true and keep the issues/report views readable.
function Invoke-DegradedAcceptance {
    param(
        [string]$ApiExe,
        [System.Uri]$DatabaseUri,
        [string]$AdminUsername,
        [string]$AdminPassword
    )
    $savedDbName = [string]$script:dbName
    $savedApiBaseUrl = [string]$script:apiBaseUrl
    $savedHeaders = $script:adminHeaders
    $savedEvidenceDir = [string]$script:evidenceDir
    $phaseStart = [DateTime]::UtcNow
    $degradedTargetUrl = New-DatabaseUrl -DatabaseUri $DatabaseUri -DatabaseName $degradedDbName

    try {
        Write-Step "Scenario 4: degraded baseline without any LLM (separate DB :$degradedDbName, API :$degradedApiPort)"
        if ($allowedDatabaseNames -notcontains $degradedDbName) { throw "unexpected degraded database name" }
        Assert-PortFree -Port $degradedApiPort
        New-Item -ItemType Directory -Force -Path $degradedEvidenceDir | Out-Null
        New-Item -ItemType Directory -Force -Path $degradedReportRoot | Out-Null
        New-Item -ItemType Directory -Force -Path $degradedStorageDir | Out-Null

        $script:dbName = $degradedDbName
        $script:apiBaseUrl = $degradedBaseUrl
        $script:evidenceDir = $degradedEvidenceDir
        $script:adminHeaders = $null

        $exists = Get-PsqlScalar -Database "postgres" -Sql "SELECT count(*) FROM pg_database WHERE datname = '$degradedDbName';"
        if ($exists -ne "0") {
            Invoke-PsqlQuiet -Database "postgres" -Sql "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$degradedDbName' AND pid <> pg_backend_pid();" -Operation "terminating $degradedDbName connections"
            Invoke-PsqlQuiet -Database "postgres" -Sql "DROP DATABASE $degradedDbName;" -Operation "dropping $degradedDbName"
        }
        Invoke-PsqlQuiet -Database "postgres" -Sql "CREATE DATABASE $degradedDbName;" -Operation "creating $degradedDbName"
        Add-CheckResult -Name "scenario 4 database rebuilt" -Status "passed" -Detail "$degradedDbName dropped/recreated on the local PostgreSQL (never the main or T29 target database)"

        # Give the second API process its own no-llm_* environment. Start-Process
        # snapshots the environment, so restoring llm_* right after the start does
        # not affect the already-started degraded process.
        Set-ScopedEnv -Name "EZTEST_ENV" -Value "dev"
        Set-ScopedEnv -Name "EZTEST_DATABASE_URL" -Value $degradedTargetUrl
        Set-ScopedEnv -Name "EZTEST_HTTP_ADDR" -Value ":$degradedApiPort"
        Set-ScopedEnv -Name "EZTEST_WEB_ORIGIN" -Value "http://localhost:6868"
        Set-ScopedEnv -Name "EZTEST_ENABLE_TEST_ENDPOINTS" -Value "true"
        Set-ScopedEnv -Name "EZTEST_STORAGE_DIR" -Value $degradedStorageDir
        Set-ScopedEnv -Name "EZTEST_REPORT_DIR" -Value $degradedReportRoot
        Set-ScopedEnv -Name "EZTEST_EVIDENCE_DIR" -Value $degradedEvidenceDir
        Set-ScopedEnv -Name "EZTEST_EXECUTOR_URL" -Value "http://127.0.0.1:$executorPort"
        Set-ScopedEnv -Name "EZTEST_EXECUTOR_TOKEN" -Value $executorToken
        Set-ScopedEnv -Name "EZTEST_ADMIN_USERNAME" -Value $AdminUsername
        Set-ScopedEnv -Name "EZTEST_ADMIN_PASSWORD" -Value $AdminPassword
        Set-ScopedEnv -Name "EZTEST_EXEC_WORKER_INTERVAL_MS" -Value "60000"
        Set-ScopedEnv -Name "EZTEST_AUTONOMY_BATCHES_PER_TICK" -Value "4"
        Set-ScopedEnv -Name "EZTEST_LOG_LEVEL" -Value "info"
        Clear-ScopedEnv -Name "EZTEST_TEST_DATABASE_URL"
        Clear-ScopedEnv -Name "EZTEST_FAKE_LLM_URL"
        Clear-ScopedEnv -Name "EZTEST_E2E_FAKE_LLM_URL"
        foreach ($llmName in @("llm_base_url", "llm_key", "llm_model_name")) { Clear-ScopedEnv -Name $llmName }

        $degradedOut = Join-Path $evidenceRoot "logs\degraded-api.out.log"
        $degradedErr = Join-Path $evidenceRoot "logs\degraded-api.err.log"
        Remove-Item -LiteralPath $degradedOut, $degradedErr -ErrorAction SilentlyContinue
        $script:degradedApiProcess = Start-Process -FilePath $ApiExe -WorkingDirectory $backendDir `
            -RedirectStandardOutput $degradedOut -RedirectStandardError $degradedErr -WindowStyle Hidden -PassThru
        Restore-ScopedEnv -Names @("llm_base_url", "llm_key", "llm_model_name")

        try {
            Wait-ForHttp -Url "$degradedBaseUrl/api/health" -TimeoutSeconds 180 -Process $script:degradedApiProcess -Label "degraded API"
        } catch {
            Stop-ProcessTree -Process $script:degradedApiProcess
            $script:degradedApiProcess = $null
            throw
        }
        $degradedHealth = Invoke-Api -Method Get -Path "/api/health"
        if ([string]$degradedHealth.Data.status -ne "ok" -or [string]$degradedHealth.Data.db -ne "ok") {
            throw "the degraded API health is not ok"
        }
        if ([bool]$degradedHealth.Data.llm_configured) {
            throw "the degraded API reports llm_configured=true although no llm_* environment was provided"
        }
        $script:adminHeaders = @{ Authorization = "Bearer $(Get-Token -Username $AdminUsername -Password $AdminPassword)" }
        $degradedConfigs = Invoke-Api -Method Get -Path "/api/admin/llm/configs" -Headers $script:adminHeaders
        if ($degradedConfigs.Status -ne 200) { throw "GET degraded LLM configs returned HTTP $($degradedConfigs.Status)" }
        $degradedConfigCount = @($degradedConfigs.Data | Where-Object { $null -ne $_ }).Count
        if ($degradedConfigCount -ne 0) { throw "the degraded API seeded $degradedConfigCount LLM configuration(s) although llm_* was unset" }
        Write-EvidenceJson -Name "scenario4-degraded-health.json" -Value @{ health = $degradedHealth.Data; llmConfigs = @() } | Out-Null
        Add-CheckResult -Name "scenario 4 no-LLM API" -Status "passed" -Detail ("separate API on :{0} reports llm_configured=false and 0 LLM configurations; database {1}" -f $degradedApiPort, $degradedDbName)

        Write-Step "Scenario 4: seeding a baseline version and running the chain without an LLM"
        $degradedWorld = New-P7World
        Add-CheckResult -Name "scenario 4 fixture version" -Status "passed" -Detail ("version {0} executing with {1} baseline cases in the no-LLM database" -f $degradedWorld.VersionId, $degradedWorld.CaseIds.Count)

        Freeze-Version -VersionId $degradedWorld.VersionId
        try {
            $degradedCreated = Invoke-Api -Method Post -Path "/api/versions/$($degradedWorld.VersionId)/batches" -Headers $script:adminHeaders -Body @{
                caseIds = @($degradedWorld.CaseIds)
            }
            if ($degradedCreated.Status -ne 201) { throw "POST degraded baseline batch returned HTTP $($degradedCreated.Status)" }
            $degradedBatchId = [string]$degradedCreated.Data.id
            $degradedFake = Invoke-FakeExecution -VersionId $degradedWorld.VersionId -BatchId $degradedBatchId -Label "degraded baseline batch" -FailTitlePatterns @("订单状态")
        } finally {
            Unfreeze-Version -VersionId $degradedWorld.VersionId
        }
        $degradedBatch = Get-Batch -VersionId $degradedWorld.VersionId -BatchId $degradedBatchId
        if ([string]$degradedBatch.status -ne "done") { throw "the degraded baseline batch is '$($degradedBatch.status)', expected done" }
        if ([string]::IsNullOrWhiteSpace([string]$degradedBatch.head_commit)) {
            throw "the degraded baseline batch has head_commit=NULL; T17d pinning must work without an LLM"
        }
        if ([string]$degradedBatch.head_commit -ne [string]$script:repoFixture.HeadCommit) {
            throw "the degraded baseline head_commit '$($degradedBatch.head_commit)' is not fixture commit B '$($script:repoFixture.HeadCommit)'"
        }
        Add-CheckResult -Name "scenario 4 baseline batch completed" -Status "passed" -Detail ("batch {0} done, {1} passed / {2} failed; head_commit pinned to {3} without any LLM" -f $degradedBatchId, $degradedFake.Passed, $degradedFake.Failed, $degradedBatch.head_commit)

        $degradedCompleted = Wait-Until -Label "degraded version completed" -TimeoutSeconds 300 -IntervalMs 1000 -Probe {
            $script:degradedApiProcess.Refresh()
            if ($script:degradedApiProcess.HasExited) { throw "the degraded API exited with code $($script:degradedApiProcess.ExitCode)" }
            $version = Get-Version -VersionId $degradedWorld.VersionId
            if ([string]$version.status -eq "completed") { return $version }
            return $null
        }
        if ($null -ne $degradedCompleted.continuePhase) { throw "the degraded completed version still carries continuePhase '$($degradedCompleted.continuePhase)'" }

        $degradedAnalyses = Get-AnalysisList -VersionId $degradedWorld.VersionId -BatchId $degradedBatchId
        $degradedFailure = @($degradedAnalyses | Where-Object { [string]$_.kind -eq "failure" } | Select-Object -First 1)
        if ($degradedFailure.Count -eq 0) { throw "the degraded chain created no failure analysis run" }
        $degradedFailureStatus = [string]$degradedFailure[0].status
        $degradedFailureErrors = $degradedFailure[0].errors
        $degradedFailureReason = ""
        if ($null -ne $degradedFailureErrors) {
            if ($degradedFailureErrors -is [string]) {
                try {
                    $degradedFailureReason = [string](($degradedFailureErrors | ConvertFrom-Json).reason)
                } catch {
                    $degradedFailureReason = ""
                }
            } else {
                $degradedFailureReason = [string]$degradedFailureErrors.reason
            }
        }
        $degradedFailureErrorText = ($degradedFailureErrors | ConvertTo-Json -Compress -Depth 10)
        if ([string]::IsNullOrWhiteSpace($degradedFailureErrorText)) { $degradedFailureErrorText = "" }
        Write-EvidenceJson -Name "scenario4-analysis.json" -Value @($degradedAnalyses) | Out-Null
        # The no-LLM chain must degrade for exactly the unavailable-AI reason: a
        # failed status or any other/missing errors.reason is not scenario-4 PASS.
        if ($degradedFailureStatus -ne "degraded") {
            throw "the no-LLM failure analysis ended '$degradedFailureStatus', expected degraded (errors: $degradedFailureErrorText)"
        }
        if ($degradedFailureReason -ne "ai_unavailable") {
            throw "the no-LLM failure analysis errors.reason is '$degradedFailureReason', expected 'ai_unavailable' (errors: $degradedFailureErrorText)"
        }
        Add-CheckResult -Name "scenario 4 failure analysis degraded" -Status "passed" -Detail ("failure analysis {0} ended 'degraded' with errors.reason='ai_unavailable' (errors: {1}); the chain continued instead of crashing" -f $degradedFailure[0].id, $degradedFailureErrorText)

        $degradedReport = Wait-Report -VersionId $degradedWorld.VersionId -BatchId $degradedBatchId -TimeoutSeconds 300
        if ([string]$degradedReport.status -ne "published") { throw "the degraded report is '$($degradedReport.status)', expected published" }
        if (-not [bool]$degradedReport.content.degraded) { throw "the no-LLM report content.degraded is false, expected true" }
        $degradedRisks = @($degradedReport.content.risks)
        if (-not (@($degradedRisks | Where-Object { [string]$_ -match '降级' }).Count -ge 1)) {
            throw "the no-LLM report risks do not mention the AI degradation"
        }
        if ([int]$degradedReport.content.summary.failed -lt 1) { throw "the degraded report records no failed case" }
        $degradedReportHead = [string]$degradedReport.content.batch.headCommit
        if ($degradedReportHead -ne [string]$degradedBatch.head_commit) {
            throw "the degraded report headCommit '$degradedReportHead' does not match the batch head '$($degradedBatch.head_commit)'"
        }
        $degradedExport = Get-ReportExport -VersionId $degradedWorld.VersionId -ReportId $degradedReport.id -FileName "scenario4-degraded-report.md"
        if ($degradedExport.Text -notmatch '```mermaid') { throw "the degraded Markdown export has no Mermaid block" }
        if ($degradedExport.Text -notmatch '降级') { throw "the degraded Markdown export has no degradation warning" }
        if ([string]$degradedReport.markdown.sha256 -ne $degradedExport.Sha256) {
            throw "the degraded report row sha256 does not match the exported bytes"
        }
        if ([int64]$degradedReport.markdown.sizeBytes -ne [int64]$degradedExport.Bytes.Length) {
            throw "the degraded report row size does not match the exported bytes"
        }
        Write-EvidenceJson -Name "scenario4-degraded-report.json" -Value $degradedReport | Out-Null
        Add-CheckResult -Name "scenario 4 degraded report" -Status "passed" -Detail ("report {0} published, content.degraded=true, risks=[{1}], {2} bytes sha256={3}; degradation warning present in Markdown" -f $degradedReport.id, ($degradedRisks -join " | "), $degradedExport.Bytes.Length, $degradedExport.Sha256)

        # Issues and report "views": the version-scoped and global list endpoints,
        # the analysis list and the report detail/export must all render (HTTP 200)
        # when every AI step degraded.
        $degradedIssues = Invoke-Api -Method Get -Path "/api/versions/$($degradedWorld.VersionId)/issues?limit=100" -Headers $script:adminHeaders
        if ($degradedIssues.Status -ne 200) { throw "the degraded issues view returned HTTP $($degradedIssues.Status)" }
        $degradedIssueCount = @($degradedIssues.Data | Where-Object { $null -ne $_ }).Count
        $degradedGlobalIssues = Invoke-Api -Method Get -Path "/api/issues?limit=100" -Headers $script:adminHeaders
        if ($degradedGlobalIssues.Status -ne 200) { throw "the degraded global issues view returned HTTP $($degradedGlobalIssues.Status)" }
        $degradedReports = Invoke-Api -Method Get -Path "/api/versions/$($degradedWorld.VersionId)/reports?limit=100" -Headers $script:adminHeaders
        if ($degradedReports.Status -ne 200) { throw "the degraded reports view returned HTTP $($degradedReports.Status)" }
        $degradedReportCount = @($degradedReports.Data | Where-Object { $null -ne $_ }).Count
        if ($degradedReportCount -lt 1) { throw "the degraded reports view is empty" }
        $degradedEvents = Get-VersionEvents -VersionId $degradedWorld.VersionId
        $degradedEventTypes = @($degradedEvents | ForEach-Object { [string]$_.type } | Select-Object -Unique)
        foreach ($requiredEvent in @("analysis.finished", "report.published")) {
            if ($degradedEventTypes -notcontains $requiredEvent) { throw "the degraded event snapshot is missing $requiredEvent" }
        }
        Write-EvidenceJson -Name "scenario4-views.json" -Value @{
            issues         = @($degradedIssues.Data)
            globalIssues   = @($degradedGlobalIssues.Data)
            reports        = @($degradedReports.Data)
            eventTypes     = $degradedEventTypes
        } | Out-Null
        Add-CheckResult -Name "scenario 4 issues/report views render" -Status "passed" -Detail ("version issues=200 ({0} rows), global issues=200, reports=200 ({1} rows), analysis=200, report=200, export=200, events=200; required event types present" -f $degradedIssueCount, $degradedReportCount)

        $degradedHealthAfter = Invoke-Api -Method Get -Path "/api/health"
        if ([string]$degradedHealthAfter.Data.status -ne "ok") { throw "the degraded API health is not ok after the chain" }
        $script:degradedApiProcess.Refresh()
        if ($script:degradedApiProcess.HasExited) { throw "the degraded API exited after the chain with code $($script:degradedApiProcess.ExitCode)" }
        $degradedUsage = Get-AgentUsage -VersionId $degradedWorld.VersionId
        if ($degradedUsage.total.agentRuns -ne 0) {
            throw "the no-LLM chain recorded $($degradedUsage.total.agentRuns) agent run(s), expected 0"
        }
        Write-EvidenceJson -Name "scenario4-usage.json" -Value $degradedUsage | Out-Null
        Add-CheckResult -Name "scenario 4 no LLM usage" -Status "passed" -Detail "0 agent runs and 0 tokens in the no-LLM chain; the API responded ok after the chain (no crash)"

        $script:result.scenario4 = [ordered]@{
            database             = $degradedDbName
            apiPort              = $degradedApiPort
            llmConfigured        = [bool]$degradedHealth.Data.llm_configured
            versionId            = [string]$degradedWorld.VersionId
            batchId              = $degradedBatchId
            batchStatus          = [string]$degradedBatch.status
            headCommit           = [string]$degradedBatch.head_commit
            failureAnalysisId    = [string]$degradedFailure[0].id
            failureAnalysisStatus = $degradedFailureStatus
            failureAnalysisReason = $degradedFailureReason
            reportId             = [string]$degradedReport.id
            reportStatus         = [string]$degradedReport.status
            reportDegraded       = [bool]$degradedReport.content.degraded
            reportHash           = $degradedExport.Sha256
            reportBytes          = [int64]$degradedExport.Bytes.Length
            issueRows            = $degradedIssueCount
            reportRows           = $degradedReportCount
            agentRuns            = [int64]$degradedUsage.total.agentRuns
            exitCrashed          = $false
        }
        Write-EvidenceJson -Name "scenario4-degraded.json" -Value $script:result.scenario4 | Out-Null
        Complete-Phase -Name "scenario 4 (degraded/no-LLM)" -StartedAt $phaseStart
    } finally {
        Stop-ProcessTree -Process $script:degradedApiProcess
        $script:degradedApiProcess = $null
        try { Wait-PortFree -Port $degradedApiPort -TimeoutSeconds 20 } catch { }
        $script:dbName = $savedDbName
        $script:apiBaseUrl = $savedApiBaseUrl
        $script:adminHeaders = $savedHeaders
        $script:evidenceDir = $savedEvidenceDir
    }
}

function Invoke-StaticChecks {
    Write-Step "StaticOnly: validating repository layout, P7 wiring needles and .env key presence"
    $problems = @()
    foreach ($relative in @(
            "backend\cmd\eztest\main.go",
            "backend\internal\execution\application\autonomy_worker.go",
            "backend\internal\delivery\interfaces\httpapi\routes.go",
            "backend\internal\mindmap\interfaces\httpapi\routes.go",
            "backend\internal\db\migrations\order.txt",
            "docs\openapi\api.yaml",
            "scripts\acceptance-p7-llm.ps1"
        )) {
        if (-not (Test-Path -LiteralPath (Join-Path $repoRoot $relative))) { $problems += "missing $relative" }
    }
    $skipProblems = @()
    $values = @{}
    if (-not (Test-Path -LiteralPath $envPath -PathType Leaf)) {
        $skipProblems += ".env is missing at $envPath"
    } else {
        $values = Read-DotEnv -Path $envPath
        foreach ($key in $requiredEnvKeys) {
            if (-not $values.ContainsKey($key) -or [string]::IsNullOrWhiteSpace([string]$values[$key])) {
                $skipProblems += ".env is missing $key"
            }
        }
    }
    $mainGo = [System.IO.File]::ReadAllText((Join-Path $repoRoot "backend\cmd\eztest\main.go"))
    foreach ($needle in @("deliveryhttp.NewHandlers", "ensureDeliveryProfiles", "ensureSupplementProfile", "NewAutonomyWorker", "executiondelivery.NewAnalysisPort", "executionmindmap.NewSupplementPort", "CodeHistory:", "executionCodeHistory(")) {
        if (-not $mainGo.Contains($needle)) { $problems += "main.go is missing '$needle'" }
    }
    $deliveryRouteDir = Join-Path $repoRoot "backend\internal\delivery\interfaces\httpapi"
    $deliveryRoutes = (@(Get-ChildItem -LiteralPath $deliveryRouteDir -Filter "*.go" | ForEach-Object { [System.IO.File]::ReadAllText($_.FullName) }) -join "`n")
    foreach ($needle in @("/issues", "/reports", "/analysis")) {
        if (-not $deliveryRoutes.Contains($needle)) { $problems += "delivery routes are missing '$needle'" }
    }
    $mindmapRouteDir = Join-Path $repoRoot "backend\internal\mindmap\interfaces\httpapi"
    $mindmapRoutes = (@(Get-ChildItem -LiteralPath $mindmapRouteDir -Filter "*.go" | ForEach-Object { [System.IO.File]::ReadAllText($_.FullName) }) -join "`n")
    if (-not $mindmapRoutes.Contains("incremental-plan")) { $problems += "mindmap routes are missing incremental-plan" }
    $order = [System.IO.File]::ReadAllText((Join-Path $repoRoot "backend\internal\db\migrations\order.txt"))
    foreach ($migration in @("execution_0002_autonomy.sql", "mindmap_0002_supplement.sql", "delivery_0003_analysis.sql", "resource_0007_db_target.sql")) {
        if (-not $order.Contains($migration)) { $problems += "order.txt is missing $migration" }
    }
    $openapi = [System.IO.File]::ReadAllText((Join-Path $repoRoot "docs\openapi\api.yaml"))
    foreach ($needle in @("/api/versions/{id}/issues", "/api/versions/{id}/reports", "/incremental-plan", "continuePhase", "issue_triage")) {
        if (-not $openapi.Contains($needle)) { $problems += "OpenAPI is missing '$needle'" }
    }
    if (-not [string]::IsNullOrWhiteSpace($env:EZTEST_FAKE_LLM_URL) -or -not [string]::IsNullOrWhiteSpace($env:EZTEST_E2E_FAKE_LLM_URL)) {
        $problems += "a fake-LLM environment variable is set; real acceptance must not use a fake LLM"
    }
    $toolchain = @(
        @{ Name = "go"; Local = (Join-Path $repoRoot ".tools\go\bin\go.exe") },
        @{ Name = "psql"; Local = (Join-Path $repoRoot ".tools\pgsql\bin\psql.exe") },
        @{ Name = "node"; Local = "" },
        @{ Name = "git"; Local = "" }
    )
    foreach ($tool in $toolchain) {
        if ([string]::IsNullOrWhiteSpace((Resolve-ToolPath -LocalPath $tool.Local -CommandName $tool.Name))) {
            $problems += "$($tool.Name) executable not found (.tools or PATH)"
        }
    }
    if ($values.ContainsKey("EZTEST_DATABASE_URL") -and -not [string]::IsNullOrWhiteSpace([string]$values["EZTEST_DATABASE_URL"])) {
        try {
            $uri = Assert-SafeDatabaseUrl -DatabaseUrl ([string]$values["EZTEST_DATABASE_URL"])
            Assert-LocalDatabaseUri -DatabaseUri $uri
            Write-Host ("StaticOnly: real acceptance would target local database '{0}' on {1}:{2} (loopback OK); scenario 4 uses '{3}'" -f $dbName, $uri.Host, $(if ($uri.Port -gt 0) { $uri.Port } else { 5432 }), $degradedDbName)
        } catch {
            $problems += (Get-RedactedText -Text $_.Exception.Message)
        }
    } else {
        $skipProblems += "EZTEST_DATABASE_URL is not configured"
    }
    # Static self-check of the scenario-4 / status / T17d implementation needles.
    $selfText = [System.IO.File]::ReadAllText((Join-Path $repoRoot "scripts\acceptance-p7-llm.ps1"))
    foreach ($needle in @("scenario4-degraded.json", "llm_configured", "AllowSkip", "Add-FixtureCommitB", "T17d automatic P4 impact", "passed_with_allowed_skips", "errors.reason='ai_unavailable'", "eztest-p7-real-llm-", "p7-real-llm-contender.log")) {
        if (-not $selfText.Contains($needle)) { $problems += "acceptance-p7-llm.ps1 is missing the '$needle' implementation needle" }
    }
    # Lock/ownership self-check: the final Release-P7RunLock call must come after
    # the evidence/latest writes (so a second copy cannot clear the evidence while
    # the owner is still writing it), the lock must be machine-wide and keyed by
    # the fixed database, the evidence/latest writes must be inside the
    # $script:lockAcquired gate, and the non-owner path may only write the
    # separate contender log.
    $lastLockRelease = $selfText.LastIndexOf("Release-P7RunLock", [System.StringComparison]::Ordinal)
    $lastLatestWrite = $selfText.LastIndexOf('$latestRoot', [System.StringComparison]::Ordinal)
    if ($lastLockRelease -lt 0 -or $lastLatestWrite -lt 0 -or $lastLockRelease -lt $lastLatestWrite) {
        $problems += "acceptance-p7-llm.ps1 releases the run lock before the evidence/latest writes complete"
    }
    # Scope the structural checks to the Invoke-Acceptance body and build the
    # marker literals by concatenation so this self-check does not match itself.
    $invokeStart = $selfText.IndexOf('function Invoke-' + 'Acceptance {', [System.StringComparison]::Ordinal)
    $ownerStartMarker = '# --- ' + 'OWNER-ONLY EVIDENCE WRITES'
    $ownerEndMarker = '# --- END ' + 'OWNER-ONLY EVIDENCE WRITES'
    $contenderMarker = '# ' + 'NON-OWNER CONTENDER'
    $ownerStart = if ($invokeStart -ge 0) { $selfText.IndexOf($ownerStartMarker, $invokeStart, [System.StringComparison]::Ordinal) } else { -1 }
    $ownerEnd = if ($ownerStart -ge 0) { $selfText.IndexOf($ownerEndMarker, $ownerStart, [System.StringComparison]::Ordinal) } else { -1 }
    $ownerGateNeedle = 'if (' + '$script:lockAcquired) {'
    $ownerGate = if ($ownerStart -ge 0) { $selfText.LastIndexOf($ownerGateNeedle, $ownerStart, [System.StringComparison]::Ordinal) } else { -1 }
    if ($ownerGate -lt $invokeStart) { $ownerGate = -1 }
    $nonOwnerBranch = if ($ownerEnd -ge 0) { $selfText.IndexOf($contenderMarker, $ownerEnd, [System.StringComparison]::Ordinal) } else { -1 }
    $contenderFn = $selfText.IndexOf('function Write-' + 'P7ContenderLog {', [System.StringComparison]::Ordinal)
    if ($invokeStart -lt 0) {
        $problems += "acceptance-p7-llm.ps1 is missing the Invoke-Acceptance body"
    } elseif ($ownerStart -lt 0 -or $ownerEnd -le $ownerStart) {
        $problems += "acceptance-p7-llm.ps1 is missing the OWNER-ONLY evidence-write block markers"
    } elseif ($ownerGate -lt 0) {
        $problems += 'acceptance-p7-llm.ps1 evidence/latest writes are not gated by $script:lockAcquired'
    } elseif ($nonOwnerBranch -lt $ownerEnd) {
        $problems += "acceptance-p7-llm.ps1 is missing the non-owner contender branch after the owner block"
    } elseif ($contenderFn -lt 0) {
        $problems += "acceptance-p7-llm.ps1 is missing the Write-P7ContenderLog implementation"
    } else {
        foreach ($needle in @('Write-Utf8NoBom -Path $runJsonPath', 'Write-Utf8NoBom -Path $verificationPath', 'secret-scan-final.json')) {
            $needleIndex = $selfText.IndexOf($needle, $ownerStart, [System.StringComparison]::Ordinal)
            if ($needleIndex -lt 0 -or $needleIndex -gt $ownerEnd) {
                $problems += "acceptance-p7-llm.ps1 writes '$needle' outside the owner-only evidence block"
            }
        }
        if ($lastLatestWrite -lt $ownerStart -or $lastLatestWrite -gt $ownerEnd) {
            $problems += "acceptance-p7-llm.ps1 writes the latest copy outside the owner-only evidence block"
        }
        $nextFunction = $selfText.IndexOf("`nfunction ", $contenderFn + 1, [System.StringComparison]::Ordinal)
        $contenderBody = if ($nextFunction -gt $contenderFn) { $selfText.Substring($contenderFn, $nextFunction - $contenderFn) } else { "" }
        if ($contenderBody -match '\$evidenceRoot|\$latestRoot') {
            $problems += "acceptance-p7-llm.ps1 contender log references the owner evidence/latest paths"
        }
        if (-not $contenderBody.Contains("AppendAllText")) {
            $problems += "acceptance-p7-llm.ps1 contender log is not append-only via AppendAllText"
        }
        $releaseTail = if ($lastLockRelease -gt $ownerEnd) { $selfText.Substring($ownerEnd, $lastLockRelease - $ownerEnd) } else { "" }
        if (-not $releaseTail.Contains("finally {")) {
            $problems += "acceptance-p7-llm.ps1 does not release the run lock in a finally after the owner block"
        }
    }
    $firstEvidenceWrite = if ($invokeStart -ge 0) { $selfText.IndexOf('Write-EvidenceJson -Name', $invokeStart, [System.StringComparison]::Ordinal) } else { -1 }
    $lockAcquireCall = if ($invokeStart -ge 0) { $selfText.IndexOf('Acquire-P7RunLock -Path $script:lockPath', $invokeStart, [System.StringComparison]::Ordinal) } else { -1 }
    if ($firstEvidenceWrite -ge 0 -and $lockAcquireCall -ge 0 -and $firstEvidenceWrite -lt $lockAcquireCall) {
        $problems += "acceptance-p7-llm.ps1 writes evidence before acquiring the run lock"
    }
    $ownedNeedle = '$owned = ' + '[bool]$script:lockAcquired -and $null -ne $script:lockStream'
    if (-not $selfText.Contains($ownedNeedle)) {
        $problems += "acceptance-p7-llm.ps1 release is not ownership-tracked"
    }
    $machineLockNeedle = '$machineLockPath = ' + 'Join-Path ([System.IO.Path]::GetTempPath())'
    $scriptLockNeedle = '$script:lockPath = ' + '$machineLockPath'
    $lockKeyNeedle = 'eztest-p7-real-llm-' + '{0}.lock'
    if (-not $selfText.Contains($machineLockNeedle) -or -not $selfText.Contains($scriptLockNeedle) -or -not $selfText.Contains($lockKeyNeedle)) {
        $problems += "acceptance-p7-llm.ps1 run lock is not machine-wide and keyed by the fixed database"
    }
    if ($selfText -match '\$script:lockPath\s*=\s*Join-Path \$tmpDir') {
        $problems += "acceptance-p7-llm.ps1 still uses a per-checkout lock path"
    }
    # Error-path status-consistency self-check: a post-write scan failure or a
    # latest-publish failure must rewrite BOTH run.json and verification.md as
    # failed so the two public evidence files never disagree.
    $postWriteFailureMarker = 'post-write secret scan failed: '
    $postWriteFailure = if ($invokeStart -ge 0) { $selfText.IndexOf($postWriteFailureMarker, $invokeStart, [System.StringComparison]::Ordinal) } else { -1 }
    $postWriteVerificationRewrite = if ($postWriteFailure -ge 0) { $selfText.IndexOf('New-VerificationMarkdown -FinalScan $finalScan -PostWriteScanPassed $false', $postWriteFailure, [System.StringComparison]::Ordinal) } else { -1 }
    if ($postWriteFailure -lt 0 -or $postWriteVerificationRewrite -lt 0) {
        $problems += "acceptance-p7-llm.ps1 post-write scan failure does not rewrite verification.md as failed"
    }
    $latestFailureMarker = 'Could not publish the latest evidence copy'
    $latestFailure = if ($invokeStart -ge 0) { $selfText.IndexOf($latestFailureMarker, $invokeStart, [System.StringComparison]::Ordinal) } else { -1 }
    $latestSyncCall = if ($latestFailure -ge 0) { $selfText.IndexOf('Sync-EvidenceFailure -Reason "could not publish the latest evidence copy', $latestFailure, [System.StringComparison]::Ordinal) } else { -1 }
    if ($latestFailure -lt 0 -or $latestSyncCall -lt 0) {
        $problems += "acceptance-p7-llm.ps1 latest-copy failure does not sync both public evidence files"
    }
    $syncFnStart = $selfText.IndexOf('function Sync-' + 'EvidenceFailure {', [System.StringComparison]::Ordinal)
    $syncNextFunction = if ($syncFnStart -ge 0) { $selfText.IndexOf("`nfunction ", $syncFnStart + 1, [System.StringComparison]::Ordinal) } else { -1 }
    $syncBody = if ($syncNextFunction -gt $syncFnStart) { $selfText.Substring($syncFnStart, $syncNextFunction - $syncFnStart) } else { "" }
    if ($syncFnStart -lt 0 -or -not $syncBody.Contains('Write-Utf8NoBom -Path $runJsonPath') -or -not $syncBody.Contains('Write-Utf8NoBom -Path $verificationPath')) {
        $problems += "acceptance-p7-llm.ps1 Sync-EvidenceFailure does not rewrite both run.json and verification.md"
    }
    if ($skipProblems.Count -gt 0) {
        if ($script:allowSkip) {
            foreach ($problem in $skipProblems) { Write-Host "StaticOnly warning (-AllowSkip): $problem" -ForegroundColor Yellow }
        } else {
            foreach ($problem in $skipProblems) { $problems += $problem }
        }
    }
    if ($env:EZTEST_RUN_REAL_LLM -ne "1") {
        Write-Host "StaticOnly: gate is closed; the real run prints SKIPPED and exits 0 (intentional closed-gate skip)."
    }
    if ($problems.Count -gt 0) {
        foreach ($problem in $problems) { Write-Host "StaticOnly problem: $problem" -ForegroundColor Red }
        if ($script:allowSkip) { Write-Host "StaticOnly (-AllowSkip) still fails on wiring/layout problems." -ForegroundColor Red }
        return 1
    }
    if ($skipProblems.Count -gt 0) {
        Write-Host "StaticOnly OK with allowed configuration skips (-AllowSkip, non-green): $($skipProblems -join '; ')" -ForegroundColor Yellow
        return 0
    }
    Write-Host "StaticOnly OK: layout, T17d/P7 wiring needles, migrations, OpenAPI, loopback DB and toolchain are ready." -ForegroundColor Green
    return 0
}

function Invoke-Acceptance {
    $exitCode = 1
    $script:apiProcess = $null
    $script:psqlExe = ""
    $script:goExe = ""
    $script:dbHost = ""
    $script:dbPort = 5432
    $script:dbUser = ""
    $script:repoFixture = $null
    $phaseStart = [DateTime]::UtcNow
    $scriptEnvKeys = @()

    try {
        if (-not (Test-Path -LiteralPath $envPath -PathType Leaf)) {
            return (Complete-Skip ".env is missing at $envPath")
        }
        $envValues = Read-DotEnv -Path $envPath

        if (-not $envValues.ContainsKey("EZTEST_DATABASE_URL") -or [string]::IsNullOrWhiteSpace([string]$envValues["EZTEST_DATABASE_URL"])) {
            return (Complete-Skip ".env does not provide EZTEST_DATABASE_URL")
        }
        $missingKeys = @()
        foreach ($key in $requiredEnvKeys) {
            if (-not $envValues.ContainsKey($key) -or [string]::IsNullOrWhiteSpace([string]$envValues[$key])) { $missingKeys += $key }
        }
        if ($missingKeys.Count -gt 0) {
            return (Complete-Skip ("EZTEST_RUN_REAL_LLM=1 but .env is missing required key(s): " + ($missingKeys -join ", ")))
        }
        if ([string]::IsNullOrWhiteSpace($env:EZTEST_RUN_REAL_LLM) -or $env:EZTEST_RUN_REAL_LLM -ne "1") {
            return (Complete-Skip "real LLM acceptance is disabled (set EZTEST_RUN_REAL_LLM=1 to run it)")
        }
        if (-not [string]::IsNullOrWhiteSpace($env:EZTEST_FAKE_LLM_URL) -or -not [string]::IsNullOrWhiteSpace($env:EZTEST_E2E_FAKE_LLM_URL)) {
            throw "a fake-LLM environment variable is set; the P7 real acceptance refuses to mix a fake LLM"
        }

        New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null
        $script:lockAttempted = $true
        Acquire-P7RunLock -Path $script:lockPath
        Add-CheckResult -Name "cross-process run lock" -Status "passed" -Detail ("acquired machine-wide {0} (pid {1}); concurrent copies from any checkout are rejected before any fixed port or database is touched" -f $script:lockPath, $PID)
        if (Test-Path -LiteralPath $evidenceRoot) {
            if (-not $evidenceRoot.StartsWith($tmpDir, [System.StringComparison]::OrdinalIgnoreCase)) { throw "refusing to clear an unexpected evidence directory" }
            Remove-Item -LiteralPath $evidenceRoot -Recurse -Force
        }
        New-Item -ItemType Directory -Force -Path $evidenceRoot | Out-Null
        New-Item -ItemType Directory -Force -Path $reportRoot | Out-Null
        New-Item -ItemType Directory -Force -Path $evidenceDir | Out-Null
        New-Item -ItemType Directory -Force -Path $storageDir | Out-Null
        New-Item -ItemType Directory -Force -Path (Join-Path $evidenceRoot "logs") | Out-Null

        $adminUsername = Get-RequiredValue -Values $envValues -Key "EZTEST_ADMIN_USERNAME"
        $adminPassword = Get-RequiredValue -Values $envValues -Key "EZTEST_ADMIN_PASSWORD"
        $llmKey = Get-RequiredValue -Values $envValues -Key "llm_key"
        $llmBaseUrl = Get-RequiredValue -Values $envValues -Key "llm_base_url"
        $llmModel = Get-RequiredValue -Values $envValues -Key "llm_model_name"
        $databaseUrl = Get-RequiredValue -Values $envValues -Key "EZTEST_DATABASE_URL"
        $script:secretValues = @($adminPassword, $llmKey, $databaseUrl)
        $script:result.llmModel = $llmModel

        Write-Step "Gate: EZTEST_RUN_REAL_LLM=1 and required .env keys"
        Add-CheckResult -Name "gate and .env keys" -Status "passed" -Detail "EZTEST_RUN_REAL_LLM=1; llm_base_url/llm_key/llm_model_name present"

        Write-Step "Validating the database URL (loopback only) and the local PostgreSQL connection"
        $databaseUri = Assert-SafeDatabaseUrl -DatabaseUrl $databaseUrl
        Assert-LocalDatabaseUri -DatabaseUri $databaseUri
        Assert-AcceptanceDatabaseName -DatabaseName $dbName
        Assert-AcceptanceDatabaseName -DatabaseName $degradedDbName
        $userInfo = $databaseUri.UserInfo
        $separator = $userInfo.IndexOf(":")
        if ($separator -lt 0) { throw "EZTEST_DATABASE_URL must contain a user and password" }
        $script:dbUser = [System.Uri]::UnescapeDataString($userInfo.Substring(0, $separator))
        $dbPassword = [System.Uri]::UnescapeDataString($userInfo.Substring($separator + 1))
        $script:dbHost = $databaseUri.Host
        $script:dbPort = if ($databaseUri.Port -gt 0) { $databaseUri.Port } else { 5432 }
        $script:secretValues += $dbPassword
        $targetDatabaseUrl = New-DatabaseUrl -DatabaseUri $databaseUri -DatabaseName $dbName

        $script:goExe = Resolve-ToolPath -LocalPath (Join-Path $repoRoot ".tools\go\bin\go.exe") -CommandName "go"
        $script:psqlExe = Resolve-ToolPath -LocalPath (Join-Path $repoRoot ".tools\pgsql\bin\psql.exe") -CommandName "psql"
        $nodeExe = Resolve-ToolPath -LocalPath "" -CommandName "node"
        if ([string]::IsNullOrWhiteSpace($script:goExe)) { return (Complete-Skip "go executable not found (.tools or PATH)") }
        if ([string]::IsNullOrWhiteSpace($script:psqlExe)) { return (Complete-Skip "psql executable not found (.tools or PATH)") }
        if ([string]::IsNullOrWhiteSpace($nodeExe)) { return (Complete-Skip "node executable not found on PATH") }
        if (-not (Get-Command git -ErrorAction SilentlyContinue)) { return (Complete-Skip "git executable not found on PATH") }

        Set-ScopedEnv -Name "PGPASSWORD" -Value $dbPassword
        try {
            $probe = Get-PsqlScalar -Database "postgres" -Sql "SELECT 1;"
        } catch {
            return (Complete-Skip ("local PostgreSQL is not reachable at $($script:dbHost):$($script:dbPort): " + (Get-RedactedText -Text $_.Exception.Message)))
        }
        if ($probe -ne "1") { throw "PostgreSQL SELECT 1 returned '$probe'" }
        Add-CheckResult -Name "PostgreSQL reachable" -Status "passed" -Detail "local PostgreSQL answered SELECT 1"

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

        Assert-PortFree -Port $apiPort
        Assert-PortFree -Port $executorPort
        Assert-AcceptanceDatabaseName -DatabaseName $dbName
        Write-Step "Resetting $dbName and building the API"
        $exists = Get-PsqlScalar -Database "postgres" -Sql "SELECT count(*) FROM pg_database WHERE datname = '$dbName';"
        if ($exists -ne "0") {
            Invoke-PsqlQuiet -Database "postgres" -Sql "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$dbName' AND pid <> pg_backend_pid();" -Operation "terminating $dbName connections"
            Invoke-PsqlQuiet -Database "postgres" -Sql "DROP DATABASE $dbName;" -Operation "dropping $dbName"
        }
        Invoke-PsqlQuiet -Database "postgres" -Sql "CREATE DATABASE $dbName;" -Operation "creating $dbName"
        Add-CheckResult -Name "database rebuilt" -Status "passed" -Detail "$dbName dropped/recreated on the local PostgreSQL (only fixed names; T29's target DB is never touched)"

        $apiExe = Join-Path $tmpDir "acceptance-p7-real-llm-api.exe"
        $buildLog = Join-Path $evidenceRoot "logs\build.log"
        Push-Location $backendDir
        $previous = $ErrorActionPreference
        $ErrorActionPreference = "SilentlyContinue"
        try {
            & $script:goExe build -o $apiExe ./cmd/eztest *> $buildLog
            $buildExit = $LASTEXITCODE
        } finally {
            $ErrorActionPreference = $previous
            Pop-Location
        }
        if ($buildExit -ne 0) { throw "go build ./cmd/eztest failed (see .tmp/p7-real-llm/logs/build.log)" }
        Add-CheckResult -Name "API build" -Status "passed" -Detail "go build ./cmd/eztest produced the acceptance binary"

        $script:repoFixture = New-FixtureRepository -Path $fixtureRepoDir
        $script:result.codeBaseCommit = $script:repoFixture.BaseCommit
        $script:result.codeHeadCommit = $script:repoFixture.BaseCommit
        Add-CheckResult -Name "fixture repository" -Status "passed" -Detail "deterministic commit A (orders page/API); commit B is added after the baseline batch so the T17d impact range is A -> B"

        Write-Step "Starting the deterministic fake executor health service on :$executorPort"
        $executorStub = Join-Path $tmpDir "p7-fake-executor.cjs"
        Write-Utf8NoBom -Path $executorStub -Text @'
"use strict";
// Deterministic stand-in for the Playwright executor in the P7 real-LLM
// acceptance. Case execution is represented by the acceptance script's
// DB-seeded result rows, so the API only needs the health gate required before
// a batch may be created. No browser is launched and no execution LLM is used.
const http = require("node:http");
const port = Number(process.env.P7_FAKE_EXECUTOR_PORT || 14346);
const server = http.createServer((req, res) => {
  if (req.method === "GET" && req.url === "/healthz") {
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(JSON.stringify({ status: "ok", version: "p7-fake-executor", contexts: 0, maxContexts: 1 }));
    return;
  }
  res.writeHead(404, { "Content-Type": "application/json" });
  res.end(JSON.stringify({ error: { code: "not_found", message: "fake executor only serves /healthz" } }));
});
server.listen(port, "127.0.0.1");
'@
        Set-ScopedEnv -Name "P7_FAKE_EXECUTOR_PORT" -Value "$executorPort"
        $executorOut = Join-Path $evidenceRoot "logs\fake-executor.out.log"
        $executorErr = Join-Path $evidenceRoot "logs\fake-executor.err.log"
        $script:executorProcess = Start-Process -FilePath $nodeExe -ArgumentList "`"$executorStub`"" -WorkingDirectory $repoRoot `
            -RedirectStandardOutput $executorOut -RedirectStandardError $executorErr -WindowStyle Hidden -PassThru
        Wait-ForHttp -Url "http://127.0.0.1:$executorPort/healthz" -TimeoutSeconds 30 -Process $script:executorProcess -Label "fake executor"
        Add-CheckResult -Name "fake executor health" -Status "passed" -Detail "deterministic health service on :$executorPort; no browser or execution LLM used"

        Write-Step "Starting the API on :$apiPort with the real LLM configuration"
        Set-ScopedEnv -Name "EZTEST_ENV" -Value "dev"
        Set-ScopedEnv -Name "EZTEST_DATABASE_URL" -Value $targetDatabaseUrl
        Set-ScopedEnv -Name "EZTEST_HTTP_ADDR" -Value ":$apiPort"
        Set-ScopedEnv -Name "EZTEST_WEB_ORIGIN" -Value "http://localhost:6868"
        Set-ScopedEnv -Name "EZTEST_ENABLE_TEST_ENDPOINTS" -Value "true"
        Set-ScopedEnv -Name "EZTEST_STORAGE_DIR" -Value $storageDir
        Set-ScopedEnv -Name "EZTEST_REPORT_DIR" -Value $reportRoot
        Set-ScopedEnv -Name "EZTEST_EVIDENCE_DIR" -Value $evidenceDir
        Set-ScopedEnv -Name "EZTEST_EXECUTOR_URL" -Value "http://127.0.0.1:$executorPort"
        Set-ScopedEnv -Name "EZTEST_EXECUTOR_TOKEN" -Value $executorToken
        Set-ScopedEnv -Name "EZTEST_ADMIN_USERNAME" -Value $adminUsername
        Set-ScopedEnv -Name "EZTEST_ADMIN_PASSWORD" -Value $adminPassword
        Set-ScopedEnv -Name "EZTEST_EXEC_WORKER_INTERVAL_MS" -Value "60000"
        Set-ScopedEnv -Name "EZTEST_EXEC_MAX_CONCURRENT_CASES" -Value "1"
        Set-ScopedEnv -Name "EZTEST_EXEC_CASE_TIMEOUT_MS" -Value "300000"
        Set-ScopedEnv -Name "EZTEST_AUTONOMY_BATCHES_PER_TICK" -Value "4"
        Set-ScopedEnv -Name "EZTEST_LOG_LEVEL" -Value "info"
        foreach ($key in @("EZTEST_SECRET_KEYS", "EZTEST_SECRET_KEY_ACTIVE", "llm_base_url", "llm_key", "llm_model_name")) {
            if ($envValues.ContainsKey($key)) { Set-ScopedEnv -Name $key -Value ([string]$envValues[$key]) }
        }
        Clear-ScopedEnv -Name "EZTEST_TEST_DATABASE_URL"
        Clear-ScopedEnv -Name "EZTEST_FAKE_LLM_URL"
        Clear-ScopedEnv -Name "EZTEST_E2E_FAKE_LLM_URL"

        $apiOut = Join-Path $evidenceRoot "logs\api.out.log"
        $apiErr = Join-Path $evidenceRoot "logs\api.err.log"
        Remove-Item -LiteralPath $apiOut, $apiErr -ErrorAction SilentlyContinue
        $script:apiProcess = Start-Process -FilePath $apiExe -WorkingDirectory $backendDir `
            -RedirectStandardOutput $apiOut -RedirectStandardError $apiErr -WindowStyle Hidden -PassThru
        try {
            Wait-ForHttp -Url "$apiBaseUrl/api/health" -TimeoutSeconds 180 -Process $script:apiProcess -Label "API"
        } catch {
            Stop-ProcessTree -Process $script:apiProcess
            $script:apiProcess = $null
            throw
        }

        $health = Invoke-Api -Method Get -Path "/api/health"
        if ([string]$health.Data.status -ne "ok" -or [string]$health.Data.db -ne "ok") { throw "API health is not ok" }
        if (-not [bool]$health.Data.llm_configured) {
            return (Complete-Skip "API reports llm_configured=false; .env llm_* cannot reach a real provider")
        }
        $script:adminHeaders = @{ Authorization = "Bearer $(Get-Token -Username $adminUsername -Password $adminPassword)" }

        $tables = [int](Get-PsqlScalar -Database $dbName -Sql "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name IN ('issue','report','analysis_run','db_target','run_batch','case_run','case_design_run');")
        if ($tables -ne 7) { throw "the $dbName database has $tables/7 P7 tables after boot" }
        Add-CheckResult -Name "migrations and health" -Status "passed" -Detail "7/7 P7 tables present; health ok; llm_configured=true"

        Write-Step "Asserting the real LLM configuration and the P7 agent wiring"
        $configs = Invoke-Api -Method Get -Path "/api/admin/llm/configs" -Headers $script:adminHeaders
        if ($configs.Status -ne 200) { throw "GET LLM configs returned HTTP $($configs.Status)" }
        $default = @($configs.Data | Where-Object { [bool]$_.is_default } | Select-Object -First 1)
        if ($default.Count -eq 0) { throw "no default LLM configuration exists after boot" }
        $defaultConfig = $default[0]
        $baseHost = ([System.Uri]([string]$defaultConfig.base_url)).Host
        if ($baseHost -notmatch 'deepseek\.com$') { throw "default LLM base_url host '$baseHost' is not a DeepSeek endpoint" }
        if ([string]$defaultConfig.model -ne $llmModel) { throw "default LLM model '$($defaultConfig.model)' does not match .env llm_model_name '$llmModel'" }
        if (-not [bool]$defaultConfig.has_api_key) { throw "the default LLM configuration has no API key" }
        $script:result.llmBaseHost = $baseHost

        $probeStart = Get-Date
        $probe = Invoke-Api -Method Post -Path "/api/admin/llm/configs/$($defaultConfig.id)/test" -Headers $script:adminHeaders -TimeoutSec 180 -Body @{
            stream = $false
            image  = $false
            prompt = "Reply with exactly: p7-real-ok"
        }
        if ($probe.Status -ne 200) { throw "LLM connectivity test returned HTTP $($probe.Status)" }
        if ([string]::IsNullOrWhiteSpace([string]$probe.Data.text)) { throw "LLM connectivity test returned empty text" }
        $probeElapsed = ([DateTime]::Now - $probeStart).TotalMilliseconds
        $probeUsage = $probe.Data.usage
        $probeTokens = if ($null -ne $probeUsage) { [int64]$probeUsage.total_tokens } else { 0 }
        if ($probeTokens -le 0) { throw "LLM connectivity test reported total_tokens=$probeTokens; a real provider must report positive usage" }
        $script:result.llmProbeTokens = [int64]$probeTokens
        Add-CheckResult -Name "real LLM connectivity" -Status "passed" -Detail ("model {0} at {1} answered {2} chars, usage={3} tokens (>0), {4:N1}s" -f $llmModel, $baseHost, ([string]$probe.Data.text).Length, $probeTokens, ($probeElapsed / 1000.0))

        $profiles = Invoke-Api -Method Get -Path "/api/agents/profiles" -Headers $script:adminHeaders
        if ($profiles.Status -ne 200) { throw "GET agent profiles returned HTTP $($profiles.Status)" }
        $profileKeys = @($profiles.Data | ForEach-Object { [string]$_.key })
        foreach ($profile in $requiredProfiles) {
            if ($profileKeys -notcontains $profile) { throw "BLOCKED: P7 agent profile '$profile' is not seeded (P7 wiring incomplete)" }
        }
        $tools = Invoke-Api -Method Get -Path "/api/agents/tools" -Headers $script:adminHeaders
        if ($tools.Status -ne 200) { throw "GET agent tools returned HTTP $($tools.Status)" }
        $toolNames = @($tools.Data | ForEach-Object { [string]$_.name })
        foreach ($tool in $requiredTools) {
            if ($toolNames -notcontains $tool) { throw "BLOCKED: P7 agent tool '$tool' is not registered (P7 wiring incomplete)" }
        }
        Add-CheckResult -Name "P7 agent profiles and tools" -Status "passed" -Detail ("3 profiles and {0} P7 tools are wired" -f $requiredTools.Count)

        Write-Step "Seeding the fixture version (repository commit A, three requirements, two baseline cases)"
        $world = New-P7World
        $script:result.systemId = $world.SystemId
        $script:result.environmentId = $world.EnvironmentId
        $script:result.versionId = $world.VersionId
        $script:result.codeRepoId = $world.CodeRepoId
        Add-CheckResult -Name "seed fixture version" -Status "passed" -Detail ("version {0} executing with a frozen baseline, {1} cases, 3 clarified requirements" -f $world.VersionId, $world.CaseIds.Count)
        Complete-Phase -Name "preflight/seed" -StartedAt $phaseStart

        # --- Scenario 1+2: baseline failure analysis, triage, report, supplement -----
        $phaseStart = [DateTime]::UtcNow
        Write-Step "Scenario 1+2: baseline batch -> real failure analysis/triage -> report -> real supplement"
        Freeze-Version -VersionId $world.VersionId
        try {
            $created = Invoke-Api -Method Post -Path "/api/versions/$($world.VersionId)/batches" -Headers $script:adminHeaders -Body @{
                caseIds = @($world.CaseIds)
            }
            if ($created.Status -ne 201) { throw "POST baseline batch returned HTTP $($created.Status)" }
            $baselineBatchId = [string]$created.Data.id
            $script:result.baselineBatchId = $baselineBatchId
            $fake = Invoke-FakeExecution -VersionId $world.VersionId -BatchId $baselineBatchId -Label "baseline batch" -FailTitlePatterns @("订单状态")
            if ($fake.Failed -lt 1) { throw "the baseline fake executor produced no failed(application) case" }
        } finally {
            Unfreeze-Version -VersionId $world.VersionId
        }
        Add-CheckResult -Name "baseline batch created and fake-executed" -Status "passed" -Detail ("baseline batch {0}: {1} passed / {2} failed(application); no execution LLM or browser used" -f $baselineBatchId, $fake.Passed, $fake.Failed)

        # T17d: CodeHistory must pin the fixed-branch HEAD at batch creation. The
        # fixture has only commit A at this point, so head_commit must be A.
        $baselineBatch = Get-Batch -VersionId $world.VersionId -BatchId $baselineBatchId
        $baselineBatchHead = [string]$baselineBatch.head_commit
        if ([string]::IsNullOrWhiteSpace($baselineBatchHead)) { throw "BLOCKED: baseline batch $baselineBatchId has head_commit=NULL (T17d CodeHistory wiring missing)" }
        if ($baselineBatchHead -ne [string]$script:repoFixture.BaseCommit) {
            throw "baseline head_commit $baselineBatchHead is not fixture commit A $($script:repoFixture.BaseCommit)"
        }
        Add-CheckResult -Name "T17d baseline head_commit pinned" -Status "passed" -Detail ("baseline batch head_commit={0} equals fixture commit A (CodeHistory fixed-branch HEAD at create)" -f $baselineBatchHead)

        $failureRun = Wait-Analysis -VersionId $world.VersionId -BatchId $baselineBatchId -Kind "failure" -TimeoutSeconds 600
        $failureResult = $failureRun.result
        $failureCandidates = @()
        if ($null -ne $failureResult -and $null -ne $failureResult.candidates) { $failureCandidates = @($failureResult.candidates) }
        if ([string]$failureRun.status -ne "done") { throw "failure analysis ended '$($failureRun.status)' (errors: $($failureRun.errors | ConvertTo-Json -Compress))" }
        if ($failureCandidates.Count -lt 1) { throw "the real failure-analysis agent proposed no candidate" }
        $script:result.analysisIds += [string]$failureRun.id
        Write-EvidenceJson -Name "analysis-failure.json" -Value $failureRun | Out-Null
        Add-CheckResult -Name "real failure analysis" -Status "passed" -Detail ("analysis {0} done with {1} candidate(s), model {2}" -f $failureRun.id, $failureCandidates.Count, $failureRun.model)

        $triageRun = Wait-Analysis -VersionId $world.VersionId -BatchId $baselineBatchId -Kind "issue_triage" -TimeoutSeconds 600
        $triageResult = $triageRun.result
        $issueDrafts = @()
        if ($null -ne $triageResult -and $null -ne $triageResult.issues) { $issueDrafts = @($triageResult.issues) }
        if ([string]$triageRun.status -ne "done") { throw "issue triage ended '$($triageRun.status)' (errors: $($triageRun.errors | ConvertTo-Json -Compress))" }
        if ($issueDrafts.Count -lt 1) { throw "the real triage agent materialized no issue draft" }
        $script:result.analysisIds += [string]$triageRun.id
        Write-EvidenceJson -Name "analysis-triage.json" -Value $triageRun | Out-Null
        Add-CheckResult -Name "real issue triage" -Status "passed" -Detail ("triage {0} done with {1} issue draft(s), model {2}" -f $triageRun.id, $issueDrafts.Count, $triageRun.model)

        $issue = Wait-Issue -VersionId $world.VersionId -TimeoutSeconds 300 -Predicate {
            param($candidate)
            return ([string]$candidate.source -eq "ai" -and [string]$candidate.category -eq "defect")
        }
        $issueDetailResponse = Invoke-Api -Method Get -Path "/api/versions/$($world.VersionId)/issues/$($issue.id)" -Headers $script:adminHeaders
        if ($issueDetailResponse.Status -ne 200) { throw "GET issue detail returned HTTP $($issueDetailResponse.Status)" }
        $issue = $issueDetailResponse.Data
        $script:result.issueId = [string]$issue.id
        $refKinds = @($issue.refs | ForEach-Object { [string]$_.kind })
        if ($refKinds -notcontains "evidence") { throw "the AI issue carries no evidence reference (refs: $($refKinds -join ','))" }
        if ($refKinds -notcontains "case_run" -and $refKinds -notcontains "case") { throw "the AI issue carries no case reference (refs: $($refKinds -join ','))" }
        $codeRefCount = @($issue.codeRefs).Count
        Write-EvidenceJson -Name "issue.json" -Value $issue | Out-Null
        Add-CheckResult -Name "AI issue persisted" -Status "passed" -Detail ("issue {0} source=ai category=defect severity={1} refs=[{2}] codeRefs={3}" -f $issue.id, $issue.severity, ($refKinds -join ","), $codeRefCount)
        $proseFields = @(
            @{ Name = "description"; Length = ([string]$issue.description).Length },
            @{ Name = "reproSteps"; Length = ([string]$issue.reproSteps).Length },
            @{ Name = "expected"; Length = ([string]$issue.expected).Length },
            @{ Name = "actual"; Length = ([string]$issue.actual).Length }
        )
        $emptyProse = @($proseFields | Where-Object { $_.Length -lt 1 } | ForEach-Object { $_.Name })
        if ($emptyProse.Count -gt 0) {
            throw "the real triage agent left mandatory issue prose field(s) empty: $($emptyProse -join ', ')"
        }
        Add-CheckResult -Name "AI issue prose mandatory fields" -Status "passed" -Detail ("description={0} chars, reproSteps={1} chars, expected={2} chars, actual={3} chars" -f $proseFields[0].Length, $proseFields[1].Length, $proseFields[2].Length, $proseFields[3].Length)

        $baselineReport = Wait-Report -VersionId $world.VersionId -BatchId $baselineBatchId -TimeoutSeconds 300
        if ([string]$baselineReport.status -ne "published") { throw "baseline report is '$($baselineReport.status)', expected published" }
        if ([bool]$baselineReport.content.degraded) { throw "baseline report is degraded although the real LLM is configured" }
        if ([int]$baselineReport.content.summary.failed -lt 1) { throw "baseline report records no failed case" }
        $reportIssueIds = @($baselineReport.content.issues | ForEach-Object { [string]$_.issueId })
        if ($reportIssueIds -notcontains [string]$issue.id) { throw "baseline report does not include AI issue $($issue.id)" }
        $baselineReportHead = [string]$baselineReport.content.batch.headCommit
        if ($baselineReportHead -ne $baselineBatchHead) {
            throw "baseline report content.batch.headCommit '$baselineReportHead' does not match the pinned head '$baselineBatchHead'"
        }
        $script:result.reportIds += [string]$baselineReport.id
        $script:result.baselineReport = [string]$baselineReport.id
        Write-EvidenceJson -Name "report-baseline.json" -Value $baselineReport | Out-Null
        $baselineExport = Get-ReportExport -VersionId $world.VersionId -ReportId $baselineReport.id -FileName "report-baseline.md"
        if ($baselineExport.Text -notmatch '```mermaid') { throw "baseline Markdown export has no Mermaid block" }
        Assert-NoSecretsInText -Label "baseline report export" -Text $baselineExport.Text
        # Hash correction: the report row hash and the downloaded Markdown bytes must
        # agree (not just the response header).
        if ([string]$baselineReport.markdown.sha256 -ne $baselineExport.Sha256) {
            throw "baseline report row sha256 $($baselineReport.markdown.sha256) does not match exported bytes $($baselineExport.Sha256)"
        }
        if ([int64]$baselineReport.markdown.sizeBytes -ne [int64]$baselineExport.Bytes.Length) {
            throw "baseline report row size $($baselineReport.markdown.sizeBytes) does not match exported bytes $($baselineExport.Bytes.Length)"
        }
        $script:result.baselineReportHash = $baselineExport.Sha256
        Add-CheckResult -Name "baseline report published" -Status "passed" -Detail ("report {0} rev {1}, batch #{2}, status {3}, issues={4}, headCommit={5}, markdown {6} bytes sha256={7}" -f $baselineReport.id, $baselineReport.revision, $baselineReport.batchNo, $baselineReport.status, $reportIssueIds.Count, $baselineReportHead, $baselineExport.Bytes.Length, $baselineExport.Sha256)

        $supplementChild = Wait-Until -Label "automatic supplement batch" -TimeoutSeconds 600 -IntervalMs 1000 -Probe {
            $batches = Get-BatchList -VersionId $world.VersionId
            $child = @($batches | Where-Object { [string]$_.kind -eq "supplement" -and [string]$_.parentBatchId -eq $baselineBatchId } | Select-Object -First 1)
            if ($child.Count -eq 0) { return $null }
            return $child[0]
        }
        $supplementBatchId = [string]$supplementChild.id
        $script:result.supplementBatch = $supplementBatchId
        $supplementBatch = Get-Batch -VersionId $world.VersionId -BatchId $supplementBatchId
        if ([string]$supplementBatch.head_commit -ne $baselineBatchHead) {
            throw "supplement batch head_commit '$($supplementBatch.head_commit)' is not the parent head '$baselineBatchHead'"
        }
        Add-CheckResult -Name "T17d supplement head_commit copied" -Status "passed" -Detail ("supplement batch {0} head_commit={1} copied from its parent" -f $supplementBatchId, $supplementBatch.head_commit)
        Freeze-Version -VersionId $world.VersionId
        try {
            $supplementFake = Invoke-FakeExecution -VersionId $world.VersionId -BatchId $supplementBatchId -Label "supplement batch" -FailTitlePatterns @()
        } finally {
            Unfreeze-Version -VersionId $world.VersionId
        }
        $supplementRuns = Wait-Until -Label "supplement case runs" -TimeoutSeconds 60 -IntervalMs 500 -Probe {
            $response = Invoke-Api -Method Get -Path "/api/versions/$($world.VersionId)/batches/$supplementBatchId/case-runs?limit=100" -Headers $script:adminHeaders
            if ($response.Status -ne 200) { throw "GET supplement case runs returned HTTP $($response.Status)" }
            $runs = @($response.Data)
            if ($runs.Count -eq 0) { return $null }
            return $runs
        }
        foreach ($run in $supplementRuns) {
            if ([string]$run.source -ne "ai_supplement") { throw "supplement case run $($run.id) has source '$($run.source)'" }
        }
        $nodes = Invoke-Api -Method Get -Path "/api/versions/$($world.VersionId)/mindmap" -Headers $script:adminHeaders
        if ($nodes.Status -ne 200) { throw "GET mindmap returned HTTP $($nodes.Status)" }
        $supplementNodes = @($nodes.Data.nodes | Where-Object { [string]$_.source -eq "ai_supplement" })
        if ($supplementNodes.Count -lt 1) { throw "the real supplement agent proposed no ai_supplement node" }
        Write-EvidenceJson -Name "supplement-batch.json" -Value @{ batch = $supplementChild; runs = @($supplementRuns); nodes = @($supplementNodes) } | Out-Null
        Add-CheckResult -Name "real supplement proposal and child batch" -Status "passed" -Detail ("supplement batch {0} with {1} fake-executed ai_supplement run(s); {2} supplement node(s) materialized" -f $supplementBatchId, $supplementRuns.Count, $supplementNodes.Count)

        $baselineCompleted = Wait-Until -Label "version completed after baseline" -TimeoutSeconds 600 -IntervalMs 1000 -Probe {
            $version = Get-Version -VersionId $world.VersionId
            if ([string]$version.status -eq "completed") { return $version }
            return $null
        }
        if ($null -ne $baselineCompleted.continuePhase) { throw "the completed version still carries continuePhase '$($baselineCompleted.continuePhase)'" }
        $supplementReport = Wait-Report -VersionId $world.VersionId -BatchId $supplementBatchId -TimeoutSeconds 300
        $supplementSources = @($supplementReport.content.cases | ForEach-Object { [string]$_.source })
        if ($supplementSources -notcontains "ai_supplement") { throw "the supplement report has no ai_supplement case row" }
        $script:result.reportIds += [string]$supplementReport.id
        Write-EvidenceJson -Name "report-supplement.json" -Value $supplementReport | Out-Null
        Add-CheckResult -Name "supplement report and baseline completion" -Status "passed" -Detail ("report {0} batchKind={1} ai_supplement rows={2}; version completed" -f $supplementReport.id, $supplementReport.batchKind, @($supplementReport.content.cases | Where-Object { [string]$_.source -eq "ai_supplement" }).Count)
        Complete-Phase -Name "scenario 1+2 (baseline chain)" -StartedAt $phaseStart

        # --- Scenario 3: continuing -> incremental design -> incremental batch ------
        $phaseStart = [DateTime]::UtcNow
        Write-Step "Scenario 3: completed version -> commit B -> new requirement -> continuing -> real incremental design -> incremental batch"
        # Commit B must be created only AFTER the baseline batch, so the baseline
        # head is pinned to A and the T17d automatic impact range is A -> B.
        $increment = Add-FixtureCommitB -Fixture $script:repoFixture
        $script:repoFixture.HeadCommit = [string]$increment.HeadCommit
        $script:result.codeHeadCommit = [string]$increment.HeadCommit
        Add-CheckResult -Name "fixture commit B after baseline" -Status "passed" -Detail ("commit A={0} -> commit B={1}; the baseline batch stays pinned at A" -f $script:repoFixture.BaseCommit, $script:repoFixture.HeadCommit)

        $newRequirement = Invoke-Api -Method Post -Path "/api/versions/$($world.VersionId)/requirements" -Headers $script:adminHeaders -Body @{
            title              = "退款原因校验（继续中需求）"
            description        = "申请退款时必须填写原因，缺失原因时阻止提交并提示错误。"
            acceptanceCriteria = "未填写退款原因时页面显示校验错误且不发起退款请求。"
            priority           = "must"
        }
        if ($newRequirement.Status -ne 201) { throw "POST continuing requirement returned HTTP $($newRequirement.Status)" }
        $newRequirementId = [string]$newRequirement.Data.id
        $continuing = Get-Version -VersionId $world.VersionId
        if ([string]$continuing.status -ne "continuing") { throw "adding a requirement left the version '$($continuing.status)', expected continuing" }
        if ([string]$continuing.continuePhase -ne "clarifying") { throw "the continuing version phase is '$($continuing.continuePhase)', expected clarifying" }
        Add-CheckResult -Name "completed -> continuing/clarifying" -Status "passed" -Detail ("new requirement {0} transitioned version {1} to continuing/clarifying" -f $newRequirementId, $world.VersionId)

        $confirm = Invoke-Api -Method Post -Path "/api/versions/$($world.VersionId)/requirements/$newRequirementId/confirm" -Headers $script:adminHeaders
        if ($confirm.Status -ne 200) { throw "POST continuing requirement confirm returned HTTP $($confirm.Status)" }
        $designing = Get-Version -VersionId $world.VersionId
        if ([string]$designing.continuePhase -ne "designing") { throw "confirming the only draft left phase '$($designing.continuePhase)', expected designing" }
        Add-CheckResult -Name "clarifying -> designing" -Status "passed" -Detail "requirement confirmed; continue_phase=designing"

        # T17d: the autonomy worker must start the P4 impact analysis by itself from
        # the pinned parent head. This script does NOT POST /code/analyses.
        $autoImpact = Wait-Until -Label "T17d automatic P4 impact analysis" -TimeoutSeconds 420 -IntervalMs 1000 -Probe {
            $response = Invoke-Api -Method Get -Path "/api/versions/$($world.VersionId)/code/analyses/latest" -Headers $script:adminHeaders -NoThrow
            if ($response.Status -eq 404) { return $null }
            if ($response.Status -ne 200) { throw "GET latest code analysis returned HTTP $($response.Status)" }
            $status = [string]$response.Data.status
            if ($status -in @("done", "failed", "degraded")) { return $response.Data }
            return $null
        }
        Write-EvidenceJson -Name "code-impact-analysis.json" -Value $autoImpact | Out-Null
        if ([string]$autoImpact.status -ne "done") {
            throw "the automatic P4 impact analysis ended '$($autoImpact.status)' (errors: $($autoImpact.errors | ConvertTo-Json -Compress))"
        }
        if ([string]$autoImpact.baseCommit -ne [string]$script:repoFixture.BaseCommit) {
            throw "the automatic P4 impact baseCommit '$($autoImpact.baseCommit)' is not the pinned parent head '$($script:repoFixture.BaseCommit)'"
        }
        if ([string]$autoImpact.headCommit -ne [string]$script:repoFixture.HeadCommit) {
            throw "the automatic P4 impact headCommit '$($autoImpact.headCommit)' is not fixture commit B '$($script:repoFixture.HeadCommit)'"
        }
        $autoImpactCandidates = @($autoImpact.candidates)
        if ($autoImpactCandidates.Count -lt 1) { throw "the automatic P4 impact analysis produced no candidate" }
        $autoImpactUnmapped = @($autoImpact.unmapped).Count
        $script:result.codeImpact = $autoImpact
        Add-CheckResult -Name "T17d automatic P4 impact" -Status "passed" -Detail ("analysis {0} started by the autonomy worker (no manual POST): base={1} head={2}, {3} candidate(s), {4} unmapped change(s)" -f $autoImpact.id, $autoImpact.baseCommit, $autoImpact.headCommit, $autoImpactCandidates.Count, $autoImpactUnmapped)

        $plan = Wait-Until -Label "real incremental design plan" -TimeoutSeconds 600 -IntervalMs 1000 -Probe {
            $response = Invoke-Api -Method Get -Path "/api/versions/$($world.VersionId)/mindmap/incremental-plan" -Headers $script:adminHeaders
            if ($response.Status -ne 200) { throw "GET incremental plan returned HTTP $($response.Status)" }
            $candidate = $response.Data
            if ($null -eq $candidate.run) { return $null }
            if ([string]$candidate.run.status -notin @("done", "failed", "degraded")) { return $null }
            return $candidate
        }
        Write-EvidenceJson -Name "incremental-plan.json" -Value $plan | Out-Null
        if ([string]$plan.run.status -ne "done") { throw "incremental design run ended '$($plan.run.status)' (degraded=$($plan.degraded))" }
        $planNewCount = @($plan.newCases).Count
        $planAffectedCount = @($plan.affectedCases).Count
        if (($planNewCount + $planAffectedCount) -lt 1) { throw "the real incremental-design agent proposed no new or affected case" }

        # Assert the plan warnings AND metadata, not just the affected reasons. A
        # plan that consumed the automatic P4 impact must carry
        # run.result.impactAvailable=true and must not warn impact_unavailable.
        $planWarningCodes = @($plan.warnings | ForEach-Object { [string]$_.code })
        $planImpactAvailable = $null
        if ($null -ne $plan.run.result -and $null -ne $plan.run.result.PSObject.Properties["impactAvailable"]) {
            $planImpactAvailable = [bool]$plan.run.result.impactAvailable
        }
        $planUnmappedCount = @($plan.unmappedChanges).Count
        $impactWarning = $planWarningCodes -contains "impact_unavailable"
        $script:result.planImpact = [ordered]@{
            runId            = [string]$plan.run.id
            impactAvailable  = $planImpactAvailable
            warnings         = @($plan.warnings)
            unmappedChanges  = $planUnmappedCount
            p4AnalysisId     = [string]$autoImpact.id
            p4Candidates     = $autoImpactCandidates.Count
            p4Unmapped       = $autoImpactUnmapped
            consumed         = ($planImpactAvailable -eq $true -and -not $impactWarning)
        }
        Write-EvidenceJson -Name "incremental-plan-impact.json" -Value $script:result.planImpact | Out-Null
        if ($planImpactAvailable -eq $true) {
            if ($impactWarning) { throw "the plan reports run.result.impactAvailable=true but also warns impact_unavailable" }
            Add-CheckResult -Name "incremental plan impact metadata" -Status "passed" -Detail ("run.result.impactAvailable=true; no impact_unavailable warning; plan unmappedChanges={0}; P4 analysis {1} ({2} candidates) is consumed" -f $planUnmappedCount, $autoImpact.id, $autoImpactCandidates.Count)
        } elseif ($impactWarning) {
            Add-SkippedCheck -Name "incremental plan impact metadata" -Required $true -Detail ("run.result.impactAvailable={0} and the plan warns impact_unavailable: P4 impact was NOT consumed by the plan. Exact blocker: the T16b starter-side impact preseed (RunDeps.Impact -> LatestImpactSummary) is missing or ineffective in this tree/build, so materialization stores impactAvailable=false. The automatic P4 analysis {1} (base={2} head={3}, {4} candidates, {5} unmapped) did run and is only visible through supplement.context_read." -f $planImpactAvailable, $autoImpact.id, $autoImpact.baseCommit, $autoImpact.headCommit, $autoImpactCandidates.Count, $autoImpactUnmapped)
        } else {
            throw "the plan carries neither impactAvailable=true nor an impact_unavailable warning (warnings: $($planWarningCodes -join ', '))"
        }

        $planCaseIds = @($plan.defaultCaseIds)
        if ($planCaseIds.Count -lt 1) { throw "the incremental plan has no default case ids" }
        $incrementalParentId = [string]$plan.parentBatchId
        $allBatches = Get-BatchList -VersionId $world.VersionId
        $latestTerminal = @($allBatches | Where-Object {
                [string]$_.status -in @("done", "failed", "cancelled")
            } | Sort-Object { [int]$_.batch_no } -Descending | Select-Object -First 1)
        if ($latestTerminal.Count -eq 0) { throw "the version has no terminal batch to parent the incremental batch" }
        if ([string]::IsNullOrWhiteSpace($incrementalParentId)) { $incrementalParentId = [string]$latestTerminal[0].id }
        if ([string]$latestTerminal[0].id -ne $incrementalParentId) {
            throw "the plan parent batch $incrementalParentId is not the latest terminal batch $($latestTerminal[0].id)"
        }
        $parentReportId = if ($incrementalParentId -eq $supplementBatchId) { [string]$supplementReport.id } else { [string]$baselineReport.id }
        Add-CheckResult -Name "real incremental design plan" -Status "passed" -Detail ("incremental_design run {0} done: {1} new case(s), {2} affected case(s), {3} default case id(s), parent batch {4}" -f $plan.run.id, $planNewCount, $planAffectedCount, $planCaseIds.Count, $incrementalParentId)

        Freeze-Version -VersionId $world.VersionId
        try {
            $incrementalCreated = Invoke-Api -Method Post -Path "/api/versions/$($world.VersionId)/batches" -Headers $script:adminHeaders -Body @{
                kind          = "incremental"
                caseIds       = $planCaseIds
                parentBatchId = $incrementalParentId
            }
            if ($incrementalCreated.Status -ne 201) { throw "POST incremental batch returned HTTP $($incrementalCreated.Status)" }
            $incrementalBatchId = [string]$incrementalCreated.Data.id
            if ([string]$incrementalCreated.Data.kind -ne "incremental") { throw "the created batch kind is '$($incrementalCreated.Data.kind)'" }
            if ([string]$incrementalCreated.Data.parentBatchId -ne $incrementalParentId) { throw "the incremental batch parent is '$($incrementalCreated.Data.parentBatchId)', expected $incrementalParentId" }
            $incrementalBatch = Get-Batch -VersionId $world.VersionId -BatchId $incrementalBatchId
            if ([string]$incrementalBatch.head_commit -ne [string]$script:repoFixture.HeadCommit) {
                throw "incremental head_commit '$($incrementalBatch.head_commit)' is not fixture commit B '$($script:repoFixture.HeadCommit)'"
            }
            $script:result["incrementalBatchId"] = $incrementalBatchId

            $paused = Invoke-Api -Method Post -Path "/api/versions/$($world.VersionId)/batches/$incrementalBatchId/pause" -Headers $script:adminHeaders -Body @{ mode = "graceful" }
            if ($paused.Status -ne 200) { throw "POST incremental pause returned HTTP $($paused.Status)" }
            if (-not [bool]$paused.Data.paused) { throw "the incremental batch did not report paused=true" }
            $resumed = Invoke-Api -Method Post -Path "/api/versions/$($world.VersionId)/batches/$incrementalBatchId/resume" -Headers $script:adminHeaders -Body @{}
            if ($resumed.Status -ne 200) { throw "POST incremental resume returned HTTP $($resumed.Status)" }
            if ([bool]$resumed.Data.paused) { throw "the incremental batch did not report paused=false after resume" }
            Add-CheckResult -Name "incremental batch pause/resume" -Status "passed" -Detail "batch paused (graceful) and resumed once before the deterministic execution"

            Freeze-Version -VersionId $world.VersionId
            try {
                $incrementalFake = Invoke-FakeExecution -VersionId $world.VersionId -BatchId $incrementalBatchId -Label "incremental batch" -FailTitlePatterns @()
            } finally {
                Unfreeze-Version -VersionId $world.VersionId
            }
            Add-CheckResult -Name "incremental batch created and fake-executed" -Status "passed" -Detail ("incremental batch {0} parent {1} with {2} case(s) executed deterministically" -f $incrementalBatchId, $incrementalParentId, $planCaseIds.Count)
            Add-CheckResult -Name "T17d incremental head_commit pinned" -Status "passed" -Detail ("incremental batch head_commit={0} equals fixture commit B (CodeHistory fixed-branch HEAD at create)" -f $incrementalBatch.head_commit)
        } finally {
            Unfreeze-Version -VersionId $world.VersionId
        }

        $incrementalCompleted = Wait-Until -Label "version completed after incremental batch" -TimeoutSeconds 600 -IntervalMs 1000 -Probe {
            $version = Get-Version -VersionId $world.VersionId
            if ([string]$version.status -eq "completed") { return $version }
            return $null
        }
        if ($null -ne $incrementalCompleted.continuePhase) { throw "the completed version still carries continuePhase '$($incrementalCompleted.continuePhase)'" }

        $incrementalReport = Wait-Report -VersionId $world.VersionId -BatchId $incrementalBatchId -TimeoutSeconds 300
        if ([string]$incrementalReport.batchKind -ne "incremental") { throw "incremental report batchKind is '$($incrementalReport.batchKind)'" }
        $incrementalReportHead = [string]$incrementalReport.content.batch.headCommit
        if ($incrementalReportHead -ne [string]$script:repoFixture.HeadCommit) {
            throw "incremental report content.batch.headCommit '$incrementalReportHead' is not fixture commit B '$($script:repoFixture.HeadCommit)'"
        }
        $incrementalSources = @($incrementalReport.content.cases | ForEach-Object { [string]$_.source })
        if ($planNewCount -gt 0) {
            $aiRows = @($incrementalSources | Where-Object { $_ -eq "ai_supplement" }).Count
            if ($aiRows -lt 1) { throw "the incremental report has no ai_supplement case row" }
        }
        $script:result.reportIds += [string]$incrementalReport.id
        $script:result.incrementalReport = [string]$incrementalReport.id
        Write-EvidenceJson -Name "report-incremental.json" -Value $incrementalReport | Out-Null

        $diff = Invoke-Api -Method Get -Path "/api/versions/$($world.VersionId)/reports/$($incrementalReport.id)/diff?baseReportId=$parentReportId" -Headers $script:adminHeaders
        if ($diff.Status -ne 200) { throw "GET report diff returned HTTP $($diff.Status)" }
        Write-EvidenceJson -Name "report-diff.json" -Value $diff.Data | Out-Null
        if ($planNewCount -gt 0 -and [int]$diff.Data.summary.newCases -lt 1) {
            throw "the report diff records no new case although the plan proposed $planNewCount"
        }
        $incrementalExport = Get-ReportExport -VersionId $world.VersionId -ReportId $incrementalReport.id -FileName "report-incremental.md"
        if ($incrementalExport.Text -notmatch '```mermaid') { throw "incremental Markdown export has no Mermaid block" }
        Assert-NoSecretsInText -Label "incremental report export" -Text $incrementalExport.Text
        if ([string]$incrementalReport.markdown.sha256 -ne $incrementalExport.Sha256) {
            throw "incremental report row sha256 $($incrementalReport.markdown.sha256) does not match exported bytes $($incrementalExport.Sha256)"
        }
        if ([int64]$incrementalReport.markdown.sizeBytes -ne [int64]$incrementalExport.Bytes.Length) {
            throw "incremental report row size $($incrementalReport.markdown.sizeBytes) does not match exported bytes $($incrementalExport.Bytes.Length)"
        }
        $script:result.incrementalReportHash = $incrementalExport.Sha256
        Add-CheckResult -Name "incremental report, diff and export" -Status "passed" -Detail ("report {0} batch #{1} cases={2}, headCommit={3}, diff newCases={4}, {5} bytes sha256={6}" -f $incrementalReport.id, $incrementalReport.batchNo, @($incrementalReport.content.cases).Count, $incrementalReportHead, $diff.Data.summary.newCases, $incrementalExport.Bytes.Length, $incrementalExport.Sha256)
        Complete-Phase -Name "scenario 3 (continuing/incremental)" -StartedAt $phaseStart

        # --- Scenario 4: degraded baseline without any LLM -------------------------
        Invoke-DegradedAcceptance -ApiExe $apiExe -DatabaseUri $databaseUri -AdminUsername $adminUsername -AdminPassword $adminPassword

        # --- Evidence: events, token usage, timing, secret scan ---------------------
        $phaseStart = [DateTime]::UtcNow
        Write-Step "Collecting events, token usage and timing evidence"
        $events = Get-VersionEvents -VersionId $world.VersionId
        Write-EvidenceJson -Name "events.json" -Value @($events) | Out-Null
        $eventTypes = @($events | ForEach-Object { [string]$_.type } | Select-Object -Unique)
        foreach ($required in @("analysis.started", "analysis.finished", "issue.created", "report.published", "case.supplemented", "requirement.added")) {
            if ($eventTypes -notcontains $required) { throw "the version event snapshot is missing $required" }
        }
        $analysisStarted = @($events | Where-Object { [string]$_.type -eq "analysis.started" }).Count
        if ($analysisStarted -lt 2) { throw "expected a started event for failure analysis and issue triage, found $analysisStarted" }
        Add-CheckResult -Name "P7 event chain" -Status "passed" -Detail ("{0} events; required types present; {1} analysis.started events" -f $events.Count, $analysisStarted)

        $usage = Get-AgentUsage -VersionId $world.VersionId
        $script:result.tokenUsage = $usage
        Write-EvidenceJson -Name "token-usage.json" -Value $usage | Out-Null
        if ($usage.allRuns.Count -lt 3) { throw "expected at least 3 real agent runs (failure, triage, supplement/design), found $($usage.allRuns.Count)" }
        if ([int64]$usage.total.inputTokens -le 0) { throw "real agent runs recorded no input tokens; the real LLM path did not execute" }
        if (-not [bool]$usage.total.withinBudget) { throw "input tokens $($usage.total.inputTokens) exceed the 3,000,000 budget; user confirmation is required" }
        Add-CheckResult -Name "real LLM token usage" -Status "passed" -Detail ("{0} agent runs, input={1}, output={2}, reasoning={3}, within 3,000,000 input budget" -f $usage.total.agentRuns, $usage.total.inputTokens, $usage.total.outputTokens, $usage.total.reasoningTokens)

        $secretScan = Invoke-SecretScan
        $script:result.secretScan = $secretScan
        Add-CheckResult -Name "no secrets in logs/evidence" -Status "passed" -Detail ("preliminary scan of {0} evidence/log files for {1} secret values found none; the final recursive scan runs after run.json/verification.md are written" -f $secretScan.scannedFiles, $secretScan.secretCount)
        Complete-Phase -Name "evidence collection" -StartedAt $phaseStart

        $script:result.elapsedMs = [int64](([DateTime]::UtcNow - $script:startedAt).TotalMilliseconds)
        $script:result.timings = @($script:timings)
        $exitCode = 0
    } catch {
        $script:failureMessage = Get-RedactedText -Text $_.Exception.Message
        Add-CheckResult -Name "harness" -Status "failed" -Detail $script:failureMessage
        $exitCode = 1
    } finally {
        Stop-ProcessTree -Process $script:degradedApiProcess
        Stop-ProcessTree -Process $script:apiProcess
        Stop-ProcessTree -Process $script:executorProcess
        try {
            Restore-ScopedEnv
            $script:result.checks = @($script:checks)
            $script:result.timings = @($script:timings)
            $failedList = @($script:checks | Where-Object { $_.status -eq "failed" })
            $requiredSkippedList = @($script:checks | Where-Object { $_.status -eq "skipped" -and [bool]$_.required })
            $optionalSkippedList = @($script:checks | Where-Object { $_.status -eq "skipped" -and -not [bool]$_.required })
            $script:result.requiredChecksSkipped = $requiredSkippedList.Count
            $script:result.optionalChecksSkipped = $optionalSkippedList.Count
            $script:result.skippedChecks = @($requiredSkippedList + $optionalSkippedList | ForEach-Object {
                    [ordered]@{ name = [string]$_.name; required = [bool]$_.required; detail = [string]$_.detail }
                })
            if ($script:skipReason -ne "") {
                $script:result.skipReason = $script:skipReason
                if ($script:skipNotAllowed) {
                    $script:result.status = "blocked"
                    $script:result.error = "configuration skip is not accepted: $($script:skipReason) (pass -AllowSkip to accept a non-green skip)"
                    $exitCode = 1
                } else {
                    $script:result.status = "skipped"
                    $exitCode = 0
                }
            } elseif ($failedList.Count -gt 0 -or $null -ne $script:failureMessage) {
                $script:result.status = "failed"
                if ($null -ne $script:failureMessage) { $script:result.error = $script:failureMessage }
                else { $script:result.error = "failed check(s): " + (($failedList | ForEach-Object { [string]$_.name }) -join ", ") }
                $exitCode = 1
            } elseif ($requiredSkippedList.Count -gt 0) {
                $requiredSkipNames = ($requiredSkippedList | ForEach-Object { [string]$_.name }) -join ", "
                if ($script:allowSkip) {
                    $script:result.status = "passed_with_allowed_skips"
                    $script:result.error = "required check(s) skipped with -AllowSkip (non-green): $requiredSkipNames"
                    $exitCode = 0
                } else {
                    $script:result.status = "failed"
                    $script:result.error = "required check(s) skipped: $requiredSkipNames (pass -AllowSkip to accept a non-green skip)"
                    $exitCode = 1
                }
            } elseif ($optionalSkippedList.Count -gt 0) {
                $script:result.status = "passed_with_skips"
                $exitCode = 0
            } else {
                $script:result.status = "passed"
                $exitCode = 0
            }
            if ($null -eq $script:result.elapsedMs) {
                $script:result.elapsedMs = [int64](([DateTime]::UtcNow - $script:startedAt).TotalMilliseconds)
            }
            $script:result.requiredChecksSkipped = $requiredSkippedList.Count
            $script:result.optionalChecksSkipped = $optionalSkippedList.Count
            $script:result.allowSkip = [bool]$script:allowSkip
            $passed = @($script:checks | Where-Object { $_.status -eq "passed" }).Count
            $failed = $failedList.Count
            $skippedChecks = $requiredSkippedList.Count + $optionalSkippedList.Count

            # OWNER-ONLY: a copy that does not own the run lock must never write
            # run.json/verification.md/secret-scan-final.json or touch
            # .tmp/p7-llm-latest. Without this gate a losing contender or a
            # pre-lock failure would overwrite the live owner's evidence.
            $finalScan = $null
            $postWriteScanPassed = $false
            $postWriteScanError = ""
            $runJsonPath = Join-Path $evidenceRoot "run.json"
            $verificationPath = Join-Path $evidenceRoot "verification.md"
            if ($script:lockAcquired) {
                # --- OWNER-ONLY EVIDENCE WRITES ---
                try {
            # Write run.json and a provisional verification.md first, then run a
            # FINAL recursive secret scan that counts/scans BOTH public evidence
            # files. The full verification.md (including the final scan counts)
            # is written after the scan; the exact final bytes of both files are
            # re-scanned before the latest copy is published.
            if (-not (Test-Path -LiteralPath $evidenceRoot)) { New-Item -ItemType Directory -Force -Path $evidenceRoot | Out-Null }
            $preScan = $script:result.secretScan
            $script:result.secretScan = [ordered]@{
                preliminaryFiles = if ($null -ne $preScan) { [int]$preScan.scannedFiles } else { 0 }
                secretCount      = $script:secretValues.Count
                finalScanPending = $true
            }
            $runJson = ConvertTo-Json -InputObject $script:result -Depth 20
            Write-Utf8NoBom -Path $runJsonPath -Text ($runJson + "`n")
            Write-Utf8NoBom -Path $verificationPath -Text "# P7 real-LLM acceptance run`n`n- Status: provisional (the full verification.md is written after the final secret scan).`n"
            # Final scan: run.json and the provisional verification.md exist now.
            $scanError = $null
            try {
                $finalScan = Invoke-SecretScan
            } catch {
                $scanError = Get-RedactedText -Text $_.Exception.Message
                $exitCode = 1
                $script:result.status = "failed"
                $script:result.error = "final secret scan failed: $scanError"
                $finalScan = [pscustomobject]@{ scannedFiles = 0; secretCount = $script:secretValues.Count; passed = $false }
            }
            if (-not [bool]$finalScan.passed) {
                $exitCode = 1
                $script:result.status = "failed"
                if ([string]::IsNullOrWhiteSpace([string]$script:result.error)) { $script:result.error = "final secret scan did not pass" }
            }
            $script:result.secretScan = [ordered]@{
                preliminaryFiles     = if ($null -ne $preScan) { [int]$preScan.scannedFiles } else { 0 }
                finalFiles           = [int]$finalScan.scannedFiles
                secretCount          = [int]$finalScan.secretCount
                includesRunJson      = $true
                includesVerification = $true
                passed               = [bool]$finalScan.passed
            }
            $runJson = ConvertTo-Json -InputObject $script:result -Depth 20
            Write-Utf8NoBom -Path $runJsonPath -Text ($runJson + "`n")
            Write-Utf8NoBom -Path $verificationPath -Text (New-VerificationMarkdown -FinalScan $finalScan -PostWriteScanPassed $true -PostWriteScanError "")
            # Validate the exact final bytes of both public evidence files: run.json
            # was rewritten after the recursive scan and verification.md existed
            # only as the provisional placeholder during that scan.
            $postWriteScanPassed = $true
            $postWriteScanError = ""
            try {
                Assert-NoSecretsInText -Label "final run.json" -Text (Read-FileShared -Path $runJsonPath)
                Assert-NoSecretsInText -Label "final verification.md" -Text (Read-FileShared -Path $verificationPath)
            } catch {
                $postWriteScanPassed = $false
                $postWriteScanError = Get-RedactedText -Text $_.Exception.Message
                $exitCode = 1
                $script:result.status = "failed"
                $script:result.error = "post-write secret scan failed: $postWriteScanError"
            }
            $script:result.secretScan["postWriteFiles"] = @($runJsonPath, $verificationPath)
            $script:result.secretScan["postWritePassed"] = $postWriteScanPassed
            if (-not $postWriteScanPassed) {
                $script:result.secretScan["passed"] = $false
                $runJson = ConvertTo-Json -InputObject $script:result -Depth 20
                Write-Utf8NoBom -Path $runJsonPath -Text ($runJson + "`n")
                # Keep both public evidence files status-consistent: verification.md
                # must also say failed, not passed.
                Write-Utf8NoBom -Path $verificationPath -Text (New-VerificationMarkdown -FinalScan $finalScan -PostWriteScanPassed $false -PostWriteScanError $postWriteScanError)
            }
            Write-Utf8NoBom -Path (Join-Path $evidenceRoot "secret-scan-final.json") -Text ((ConvertTo-Json -InputObject $script:result.secretScan -Depth 10) + "`n")
            Write-Host ("Post-write secret re-scan (run.json + verification.md): passed={0}" -f $postWriteScanPassed)

            try {
                if (Test-Path -LiteralPath $latestRoot) {
                    if (-not $latestRoot.StartsWith($tmpDir, [System.StringComparison]::OrdinalIgnoreCase)) { throw "refusing to clear an unexpected latest directory" }
                    Remove-Item -LiteralPath $latestRoot -Recurse -Force
                }
                New-Item -ItemType Directory -Force -Path $latestRoot | Out-Null
                Copy-Item -LiteralPath $runJsonPath -Destination (Join-Path $latestRoot "run.json") -Force
                Copy-Item -LiteralPath $verificationPath -Destination (Join-Path $latestRoot "verification.md") -Force
            } catch {
                $latestError = Get-RedactedText -Text $_.Exception.Message
                Write-Host "Could not publish the latest evidence copy: $latestError" -ForegroundColor Yellow
                $exitCode = 1
                # Keep run.json and verification.md consistent: both must say failed.
                Sync-EvidenceFailure -Reason "could not publish the latest evidence copy: $latestError" -FinalScan $finalScan -PostWriteScanPassed $postWriteScanPassed -PostWriteScanError $postWriteScanError
                try {
                    if (Test-Path -LiteralPath $latestRoot -PathType Container) {
                        Copy-Item -LiteralPath $runJsonPath -Destination (Join-Path $latestRoot "run.json") -Force
                        Copy-Item -LiteralPath $verificationPath -Destination (Join-Path $latestRoot "verification.md") -Force
                    }
                } catch { }
            }
            Write-Host ""
            Write-Host ("P7 real-LLM acceptance: {0} passed, {1} failed, {2} skipped ({3} required); status={4}; evidence {5}" -f $passed, $failed, $skippedChecks, $requiredSkippedList.Count, $script:result.status, $evidenceRoot)
            if ($null -ne $finalScan) {
                Write-Host ("Secret scan: {0} files including run.json + verification.md, {1} secret values, passed={2}" -f $finalScan.scannedFiles, $finalScan.secretCount, $finalScan.passed)
            }
                } catch {
                    $evidenceError = Get-RedactedText -Text $_.Exception.Message
                    Write-Host "Could not write the acceptance evidence: $evidenceError" -ForegroundColor Yellow
                    $exitCode = 1
                    Sync-EvidenceFailure -Reason "could not write the acceptance evidence: $evidenceError" -FinalScan $finalScan -PostWriteScanPassed $postWriteScanPassed -PostWriteScanError $postWriteScanError
                }
                # --- END OWNER-ONLY EVIDENCE WRITES ---
            } else {
                # NON-OWNER CONTENDER: the live owner owns the evidence/latest
                # directories; only the separate contender log may be written.
                Write-P7ContenderLog -ExitCode $exitCode -Phase "finalize"
                Write-Host ("P7 real-LLM contender (pid {0}, status={1}, exit={2}): no evidence or latest copy was written; see {3}" -f $PID, $script:result.status, $exitCode, $contenderLogPath) -ForegroundColor Yellow
            }
        } catch {
            $evidenceError = Get-RedactedText -Text $_.Exception.Message
            if ($script:lockAcquired) {
                Write-Host "Could not finalize the acceptance evidence: $evidenceError" -ForegroundColor Yellow
                $exitCode = 1
                Sync-EvidenceFailure -Reason "could not finalize the acceptance evidence: $evidenceError" -FinalScan $finalScan -PostWriteScanPassed $postWriteScanPassed -PostWriteScanError $postWriteScanError
            } else {
                # NON-OWNER CONTENDER: record the failure without touching the
                # owner's evidence/latest directories.
                $script:result.status = "failed"
                $script:result.error = "non-owner contender failed while finalizing: $evidenceError"
                $exitCode = 1
                Write-P7ContenderLog -ExitCode $exitCode -Phase "finalize-error"
            }
        } finally {
            # The lock is held until every evidence/latest write and the final
            # secret scans have completed; this finally also releases it on error.
            Release-P7RunLock
        }
        Write-FinalVerdict
    }
    return $exitCode
}

if ($StaticOnly) {
    exit (Invoke-StaticChecks)
}

if ($env:EZTEST_RUN_REAL_LLM -ne "1") {
    Write-Host "SKIPPED: real P7 LLM acceptance is disabled (set EZTEST_RUN_REAL_LLM=1 to run it)."
    exit 0
}

exit (Invoke-Acceptance)
