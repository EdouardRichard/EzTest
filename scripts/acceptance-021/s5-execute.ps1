#requires -Version 5.1
<#
.SYNOPSIS
    S5 real-execution acceptance for EzTest 0.2.1.

.DESCRIPTION
    Runs the confirmed cases of the S3/S4 version through the real executor and
    asserts:

      1. execution waves, intents and case runs are visible through the API;
      2. the execute.case job and its attempt rows are visible in the job model
         (read-only DB projection, because the public work-item API fences out
         legacy execution rows);
      3. the real Chrome executor was healthy (browserVersion present) and at
         least one case run reached a terminal verdict;
      4. executor_lost = 0 across case runs, work items and attempts;
      5. a running wave can be paused and resumed through the API;
      6. every executed case has a per-case verdict projected into the design
         graph;
      7. the execution really produced per-case step results, assertion results
         and captured evidence. A wave where every selected case failed as
         automation/agent_failure with zero recorded steps, assertions and
         evidence is a control-plane-only run and FAILS S5 (Task 73/QA-F1).

    A failing case is a valid S5 result (the acceptance checks the execution
    path, not the SUT's functional quality); a missing execution record is not.
    Pause/resume is attempted against a live wave; when the wave finishes too
    fast the script creates a fresh manual intent and retries.

.NOTES
    Exit codes: 0 = passed, 1 = failed acceptance assertion, 2 = blocked
    environment.
#>
[CmdletBinding()]
param(
    [string] $ApiBaseUrl = "",
    [string] $WebBaseUrl = "",
    [string] $ExecutorUrl = "",
    [string] $SutWebBaseUrl = "",
    [string] $SutApiBaseUrl = "",
    [string] $AdminUsername = "",
    [string] $AdminPassword = "",
    [string] $SutPassword = "",
    [string] $EvidenceRoot = "",
    [string] $VersionId = "",
    [string] $StatePath = "",
    [int] $ExecutionTimeoutSeconds = 240,
    [int] $PauseProbeSeconds = 25
)

Set-StrictMode -Version 3.0
$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
if ([string]::IsNullOrWhiteSpace($EvidenceRoot)) {
    $EvidenceRoot = Join-Path $repoRoot "docs/version-V0.2.1/acceptance/evidence/s3-s6"
}

. (Join-Path $PSScriptRoot "lib.ps1")
. (Join-Path $PSScriptRoot "lib-s3-s6.ps1")
. (Join-Path $PSScriptRoot "lib-s5-evidence.ps1")

$context = Initialize-AcceptanceContext -ApiBaseUrl $ApiBaseUrl -WebBaseUrl $WebBaseUrl `
    -ExecutorUrl $ExecutorUrl -SutWebBaseUrl $SutWebBaseUrl -SutApiBaseUrl $SutApiBaseUrl `
    -AdminUsername $AdminUsername -AdminPassword $AdminPassword -SutPassword $SutPassword `
    -EvidenceRoot $EvidenceRoot
$scenario = New-AcceptanceScenario -Context $context -Name "s5" -Title "真实执行、暂停恢复与逐例判定 (S5)"
$statePath = Get-AcceptanceStatePath -Context $context -StatePath $StatePath
$state = Read-AcceptanceState -Path $statePath
$exitCode = 2
$versionId = $VersionId
if ([string]::IsNullOrWhiteSpace($versionId) -and $state.ContainsKey("version_id")) { $versionId = [string]$state["version_id"] }

function Write-S5Evidence {
    param([string] $Name, $Value, [string] $Kind = "api", [string] $Note = "")
    $path = Join-Path $scenario.Directory $Name
    Write-EvidenceJson -Path $path -Value $Value
    Add-AcceptanceArtifact -Scenario $scenario -Path $path -Kind $Kind -Note $Note
    return $path
}

$terminalRunStatuses = @("passed", "failed", "blocked", "inconclusive", "skipped", "superseded", "interrupted")
$terminalWaveStatuses = @("done", "cancelled")
$activeWaveStatuses = @("planned", "pending", "running", "paused")

try {
    Write-AcceptanceLog -Scenario $scenario -Message "S5 execution acceptance; version=$versionId state=$statePath"

    $login = Connect-EzTest -Context $context
    Add-AcceptanceCheck -Scenario $scenario -Name "EzTest admin login" -Passed $login.Ok -Severity blocker `
        -Detail ($(if ($login.Ok) { "user=" + [string](Get-JsonProp -Object $login.User -Name 'username' -Default '?') } else { $login.Error }))
    if (-not $login.Ok) {
        $scenario.ErrorMessage = "S5 blocked: EzTest admin login failed"
    } elseif ($versionId -eq "") {
        Add-AcceptanceCheck -Scenario $scenario -Name "S4 version available" -Passed $false -Severity blocker `
            -Detail "no -VersionId and no state.version_id at $statePath; run s3-design.ps1 and s4-confirm.ps1 first"
        $scenario.ErrorMessage = "S5 blocked: no version from S4"
    } else {
        $executor = Test-ExecutorReachable -Context $context -TimeoutSeconds 15
        Add-AcceptanceCheck -Scenario $scenario -Name "real Chrome executor reachable" -Passed ($executor.Ok -and -not [string]::IsNullOrWhiteSpace($executor.BrowserVersion)) -Severity blocker `
            -Detail ($(if ($executor.Ok) { "browser=$($executor.BrowserVersion)" } else { "HTTP $($executor.Status) $($executor.Error)" }))
        if (-not $executor.Ok) {
            $scenario.ErrorMessage = "S5 blocked: executor is not healthy"
        } else {
            # ------------------------------------------------------------------
            # Confirmed cases and the starting execution state.
            # ------------------------------------------------------------------
            $graph = Get-AcceptanceDesignGraph -Context $context -VersionId $versionId
            $cards = @(Get-AcceptanceCaseCards -Graph $graph)
            $confirmedCards = @($cards | Where-Object { $_.status -eq "confirmed" })
            $confirmedCaseVersions = @()
            foreach ($card in $confirmedCards) {
                $caseVersionId = $card.case_version_id
                if ([string]::IsNullOrWhiteSpace($caseVersionId)) {
                    try {
                        $detail = Get-AcceptanceCaseDetail -Context $context -VersionId $versionId -CaseId $card.case_id
                        $caseVersionId = [string](Get-JsonProp -Object (Get-JsonProp -Object $detail -Name "selected" -Default $null) -Name "case_version_id" -Default "")
                    } catch { }
                }
                if ($caseVersionId) { $confirmedCaseVersions += $caseVersionId }
            }
            $confirmedCaseVersions = @($confirmedCaseVersions | Select-Object -Unique)
            Add-AcceptanceCheck -Scenario $scenario -Name "confirmed cases available for execution" -Passed ($confirmedCards.Count -ge 1) -Severity assertion `
                -Detail "confirmedCases=$($confirmedCards.Count) confirmedVersions=$($confirmedCaseVersions.Count)"
            [void](Write-S5Evidence -Name "confirmed-cases.json" -Value ([pscustomobject]@{
                confirmed_case_ids = @($confirmedCards | ForEach-Object { $_.case_id })
                confirmed_case_versions = $confirmedCaseVersions
            }) -Note "confirmed selection entering S5")

            # The completion worker may have closed every requirement after an
            # earlier execution. Retest needs an open requirement, so reopen the
            # closed requirements through the user API before driving a new wave.
            $requirements = @(Get-VersionRequirements -Context $context -VersionId $versionId -Limit 200)
            $closedRequirements = @($requirements | Where-Object { [string](Get-JsonProp -Object $_ -Name 'status' -Default '') -eq 'closed' })
            $reopenRecords = @()
            foreach ($requirement in ($closedRequirements | Select-Object -First 6)) {
                $requirementId = [string](Get-JsonProp -Object $requirement -Name 'id' -Default '')
                if (-not $requirementId) { continue }
                $reopen = Invoke-AcceptanceRequirementReopen -Context $context -VersionId $versionId -RequirementId $requirementId -Reason "S5 acceptance retest"
                $reopenRecords += [pscustomobject]@{ requirement_id = $requirementId; http_status = $reopen.Status; ok = $reopen.Ok; error = $reopen.Error }
            }
            [void](Write-S5Evidence -Name "requirement-reopen.json" -Value $reopenRecords -Note "requirements reopened for S5 retest")
            Add-AcceptanceCheck -Scenario $scenario -Name "requirements reopened for retest" -Passed (@($reopenRecords | Where-Object { $_.ok }).Count -eq $reopenRecords.Count) -Severity warning `
                -Detail "reopened=$(@($reopenRecords | Where-Object { $_.ok }).Count)/$($reopenRecords.Count)"

            $overviewBefore = Get-AcceptanceExecutionOverview -Context $context -VersionId $versionId
            [void](Write-S5Evidence -Name "execution-overview-before.json" -Value $overviewBefore -Note "execution overview before S5 driving")
            Add-AcceptanceCheck -Scenario $scenario -Name "execution executor registry is active" -Passed ([string](Get-JsonProp -Object (Get-JsonProp -Object $overviewBefore -Name "executor" -Default $null) -Name "status" -Default "") -eq "active") -Severity warning `
                -Detail ((Get-JsonProp -Object $overviewBefore -Name "executor" -Default $null) | ConvertTo-Json -Compress)

            $wavesBefore = @(Get-AcceptanceWaves -Context $context -VersionId $versionId)
            $waveIdsBefore = @($wavesBefore | ForEach-Object { [string](Get-JsonProp -Object $_ -Name "id" -Default "") })
            $cursor = 0
            try { $cursor = Get-EventCursor -Context $context -Channel "version:$versionId" } catch { }

            # ------------------------------------------------------------------
            # Pause/resume: wait for an active wave (create a manual intent when
            # the S4 wave already finished), pause it, resume it.
            # ------------------------------------------------------------------
            $pauseExercised = $false
            $pauseRecords = @()
            for ($attempt = 1; $attempt -le 3 -and -not $pauseExercised; $attempt++) {
                $activeWaves = @(Get-AcceptanceWaves -Context $context -VersionId $versionId | Where-Object { $activeWaveStatuses -contains [string](Get-JsonProp -Object $_ -Name "status" -Default "") })
                if ($activeWaves.Count -eq 0) {
                    $versionForIntent = $confirmedCaseVersions[($attempt - 1) % [Math]::Max(1, $confirmedCaseVersions.Count)]
                    if (-not [string]::IsNullOrWhiteSpace($versionForIntent)) {
                        $manual = New-AcceptanceManualIntent -Context $context -VersionId $versionId -CaseVersionId $versionForIntent -Purpose "manual"
                        Write-AcceptanceLog -Scenario $scenario -Message ("pause probe attempt {0}: manual intent HTTP {1}" -f $attempt, $manual.Status)
                    }
                    $pollDeadline = (Get-Date).AddSeconds($PauseProbeSeconds)
                    while ((Get-Date) -lt $pollDeadline) {
                        Start-Sleep -Milliseconds 200
                        $activeWaves = @(Get-AcceptanceWaves -Context $context -VersionId $versionId | Where-Object { $activeWaveStatuses -contains [string](Get-JsonProp -Object $_ -Name "status" -Default "") })
                        if ($activeWaves.Count -gt 0) { break }
                    }
                }
                if ($activeWaves.Count -eq 0) {
                    $pauseRecords += [pscustomobject]@{ attempt = $attempt; result = "no active wave"; status = "" }
                    continue
                }
                $waveId = [string](Get-JsonProp -Object $activeWaves[0] -Name "id" -Default "")
                $pause = Invoke-AcceptanceWaveAction -Context $context -VersionId $versionId -WaveId $waveId -Action "pause" -Reason "S5 acceptance pause"
                $pauseStatus = [string](Get-JsonProp -Object $pause.Data -Name "status" -Default "")
                $pauseRecord = [pscustomobject]@{
                    attempt         = $attempt
                    wave_id         = $waveId
                    pause_http      = $pause.Status
                    pause_status    = $pauseStatus
                    pause_mode      = [string](Get-JsonProp -Object $pause.Data -Name "pause_mode" -Default "")
                    pause_reason    = [string](Get-JsonProp -Object $pause.Data -Name "pause_reason" -Default "")
                    resume_http     = 0
                    resume_status   = ""
                    wave_after_pause = $pause.Data
                }
                if ($pause.Ok -and $pauseStatus -eq "paused") {
                    Start-Sleep -Seconds 2
                    $pausedWave = Get-AcceptanceWave -Context $context -VersionId $versionId -WaveId $waveId
                    $afterPauseStatus = [string](Get-JsonProp -Object $pausedWave -Name "status" -Default "")
                    $resume = Invoke-AcceptanceWaveAction -Context $context -VersionId $versionId -WaveId $waveId -Action "resume"
                    $resumeRecord = [pscustomobject]@{
                        paused_status = $afterPauseStatus
                        resume_http   = $resume.Status
                        resume_status = [string](Get-JsonProp -Object $resume.Data -Name "status" -Default "")
                        paused_wave   = $pausedWave
                        resumed_wave  = $resume.Data
                    }
                    $pauseRecord | Add-Member -NotePropertyName "pause_after_2s" -NotePropertyValue $pausedWave -Force
                    $pauseRecord | Add-Member -NotePropertyName "resume" -NotePropertyValue $resumeRecord -Force
                    $pauseExercised = ($afterPauseStatus -eq "paused" -and $resume.Ok)
                }
                $pauseRecords += $pauseRecord
            }
            [void](Write-S5Evidence -Name "pause-resume.json" -Value $pauseRecords -Note "wave pause/resume attempts")
            Add-AcceptanceCheck -Scenario $scenario -Name "running wave can be paused" -Passed ($pauseExercised) -Severity assertion `
                -Detail ("attempts=$($pauseRecords.Count) paused=$pauseExercised")
            if ($pauseExercised) {
                $lastRecord = $pauseRecords[$pauseRecords.Count - 1]
                Add-AcceptanceCheck -Scenario $scenario -Name "paused wave can be resumed" -Passed ([int]$lastRecord.resume.resume_http -eq 200 -and [string]$lastRecord.resume.resume_status -eq "running") -Severity assertion `
                    -Detail ("resumeHTTP=$($lastRecord.resume.resume_http) status=$($lastRecord.resume.resume_status)")
            } else {
                Add-AcceptanceCheck -Scenario $scenario -Name "paused wave can be resumed" -Passed $false -Severity assertion -Detail "no live wave to resume"
            }

            # ------------------------------------------------------------------
            # Wait for a terminal state and gather all case runs.
            # ------------------------------------------------------------------
            $terminalWait = Wait-Until -TimeoutSeconds $ExecutionTimeoutSeconds -IntervalSeconds 3 -Description "terminal execution wave" -Condition {
                $currentWaves = @(Get-AcceptanceWaves -Context $context -VersionId $versionId)
                if ($currentWaves.Count -eq 0) { return $null }
                $active = @($currentWaves | Where-Object { $activeWaveStatuses -contains [string](Get-JsonProp -Object $_ -Name "status" -Default "") })
                if ($active.Count -eq 0) { return $currentWaves }
                return $null
            }
            $waves = @(Get-AcceptanceWaves -Context $context -VersionId $versionId)
            [void](Write-S5Evidence -Name "waves.json" -Value $waves -Note "execution waves after S5")
            Add-AcceptanceCheck -Scenario $scenario -Name "execution waves visible" -Passed ($waves.Count -ge 1) -Severity assertion -Detail "waves=$($waves.Count)"
            $activeAfter = @($waves | Where-Object { $activeWaveStatuses -contains [string](Get-JsonProp -Object $_ -Name "status" -Default "") })
            Add-AcceptanceCheck -Scenario $scenario -Name "execution reaches a terminal wave state" -Passed ($activeAfter.Count -eq 0 -and $waves.Count -ge 1) -Severity assertion `
                -Detail "activeWaves=$($activeAfter.Count) statuses=$((($waves | ForEach-Object { [string](Get-JsonProp -Object $_ -Name 'status' -Default '') }) -join ','))"

            $intents = @(Get-AcceptanceExecutionIntents -Context $context -VersionId $versionId)
            [void](Write-S5Evidence -Name "intents.json" -Value $intents -Note "execution intents after S5")
            Add-AcceptanceCheck -Scenario $scenario -Name "execution intents visible" -Passed ($intents.Count -ge 1) -Severity assertion -Detail "intents=$($intents.Count)"

            $runMap = @{}
            $runSummaries = @()
            $runDetails = @()
            foreach ($wave in $waves) {
                $waveId = [string](Get-JsonProp -Object $wave -Name "id" -Default "")
                $runs = @(Get-AcceptanceWaveCaseRuns -Context $context -VersionId $versionId -WaveId $waveId)
                foreach ($run in $runs) {
                    $runId = [string](Get-JsonProp -Object $run -Name "id" -Default "")
                    if ($runId -eq "" -or $runMap.ContainsKey($runId)) { continue }
                    $runMap[$runId] = $true
                    $summary = [pscustomobject]@{
                        id             = $runId
                        wave_id        = $waveId
                        intent_id      = [string](Get-JsonProp -Object $run -Name "intent_id" -Default "")
                        case_id        = [string](Get-JsonProp -Object $run -Name "case_id" -Default "")
                        case_version_id = [string](Get-JsonProp -Object $run -Name "case_version_id" -Default "")
                        title          = [string](Get-JsonProp -Object $run -Name "title" -Default "")
                        status         = [string](Get-JsonProp -Object $run -Name "status" -Default "")
                        failure_class  = [string](Get-JsonProp -Object $run -Name "failure_class" -Default "")
                        attempts       = [int](Get-JsonProp -Object $run -Name "attempts" -Default 0)
                        evidence_count = [int](Get-JsonProp -Object $run -Name "evidence_count" -Default 0)
                        verdict_reason = [string](Get-JsonProp -Object (Get-JsonProp -Object $run -Name "summary" -Default $null) -Name "verdict_reason" -Default "")
                    }
                    $runSummaries += $summary
                    try {
                        $detail = Get-AcceptanceCaseRun -Context $context -VersionId $versionId -CaseRunId $runId
                        $runDetails += [pscustomobject]@{ summary = $summary; detail = $detail }
                    } catch {
                        $runDetails += [pscustomobject]@{ summary = $summary; detail = $null }
                    }
                }
            }
            [void](Write-S5Evidence -Name "case-runs.json" -Value $runSummaries -Note "case runs across waves")
            [void](Write-S5Evidence -Name "case-run-details.json" -Value $runDetails -Note "case run details (steps/assertions/evidence)")

            # Task 73 / QA-F1: the old gate accepted a wave where every selected
            # case ended as automation/agent_failure with zero recorded
            # steps/assertions/evidence (control plane only). PASS now requires
            # at least one case run that really recorded steps, assertions and
            # evidence; an all-silent-agent-failure wave FAILS.
            $recordedByRun = @{}
            foreach ($entry in $runDetails) {
                $recordedRunId = [string]$entry.summary.id
                $detail = $entry.detail
                $recordedSteps = 0
                $recordedAssertions = 0
                $recordedEvidence = 0
                if ($null -ne $detail) {
                    $recordedSteps = @($detail.steps).Count
                    $recordedAssertions = @($detail.assertions).Count
                    $recordedEvidence = @($detail.evidence).Count
                }
                $recordedByRun[$recordedRunId] = @{
                    steps      = $recordedSteps
                    assertions = $recordedAssertions
                    evidence   = $recordedEvidence
                }
            }
            $evidenceGate = Get-S5ExecutionEvidenceGate -Runs $runSummaries -Recorded $recordedByRun
            [void](Write-S5Evidence -Name "execution-evidence-gate.json" -Value $evidenceGate -Note "real-execution steps/assertions/evidence gate (Task 73)")
            Add-AcceptanceCheck -Scenario $scenario -Name "executed cases recorded step results" -Passed ($evidenceGate.runs_with_steps -ge 1) -Severity assertion `
                -Detail "runsWithSteps=$($evidenceGate.runs_with_steps)/$($evidenceGate.terminal_runs)"
            Add-AcceptanceCheck -Scenario $scenario -Name "executed cases recorded assertion results" -Passed ($evidenceGate.runs_with_assertions -ge 1) -Severity assertion `
                -Detail "runsWithAssertions=$($evidenceGate.runs_with_assertions)/$($evidenceGate.terminal_runs)"
            Add-AcceptanceCheck -Scenario $scenario -Name "executed cases captured evidence" -Passed ($evidenceGate.runs_with_evidence -ge 1) -Severity assertion `
                -Detail "runsWithEvidence=$($evidenceGate.runs_with_evidence)/$($evidenceGate.terminal_runs)"
            Add-AcceptanceCheck -Scenario $scenario -Name "real execution produced steps, assertions and evidence" -Passed ($evidenceGate.runs_with_steps_assertions_evidence -ge 1) -Severity assertion `
                -Detail "runsWithAll=$($evidenceGate.runs_with_steps_assertions_evidence) reason=$($evidenceGate.reason)"
            Add-AcceptanceCheck -Scenario $scenario -Name "selected cases are not all silent agent failures" -Passed (-not $evidenceGate.all_agent_failure_zero_evidence) -Severity assertion `
                -Detail "agentFailureZeroEvidence=$($evidenceGate.agent_failure_zero_evidence_runs)/$($evidenceGate.terminal_runs)"

            $terminalRuns = @($runSummaries | Where-Object { $terminalRunStatuses -contains $_.status })
            Add-AcceptanceCheck -Scenario $scenario -Name "case runs visible" -Passed ($runSummaries.Count -ge 1) -Severity assertion -Detail "caseRuns=$($runSummaries.Count)"
            Add-AcceptanceCheck -Scenario $scenario -Name "at least one case reached a terminal verdict" -Passed ($terminalRuns.Count -ge 1) -Severity assertion `
                -Detail "terminal=$($terminalRuns.Count) statuses=$((($runSummaries | ForEach-Object { $_.status }) -join ','))"
            $attemptedRuns = @($terminalRuns | Where-Object { $_.attempts -ge 1 })
            Add-AcceptanceCheck -Scenario $scenario -Name "case runs record execution attempts" -Passed ($attemptedRuns.Count -eq $terminalRuns.Count) -Severity assertion `
                -Detail "terminal=$($terminalRuns.Count) withAttempts=$($attemptedRuns.Count)"
            $evidenceTotal = 0
            foreach ($run in $runSummaries) { $evidenceTotal += $run.evidence_count }
            Add-AcceptanceCheck -Scenario $scenario -Name "execution produced evidence" -Passed ($evidenceTotal -ge 1) -Severity warning `
                -Detail "evidence=$evidenceTotal (a case can fail as an automation/tool failure before evidence capture)"

            # ------------------------------------------------------------------
            # Job/attempt projection via the read-only DB view.
            # ------------------------------------------------------------------
            $jobQuery = Invoke-AcceptanceDbQuery -Context $context -VersionId ([long]$versionId) `
                -Sql "SELECT id, type, status, attempts, max_attempts, COALESCE(LEFT(last_error, 200), '') FROM work_item WHERE version_id = $versionId AND type = 'execute.case' ORDER BY id"
            $jobRows = @()
            foreach ($line in $jobQuery.Rows) { $jobRows += ,(Get-AcceptanceSplitRow -Line $line) }
            $jobEvidence = @()
            foreach ($row in $jobRows) { $jobEvidence += [pscustomobject]@{ id = $row[0]; type = $row[1]; status = $row[2]; attempts = $row[3]; max_attempts = $row[4]; last_error = $row[5] } }
            [void](Write-S5Evidence -Name "execute-jobs-db.json" -Value ([pscustomobject]@{ database = $jobQuery.Database; ok = $jobQuery.Ok; error = $jobQuery.Error; jobs = $jobEvidence }) -Kind "db" -Note "execute.case work items (read-only DB projection)")
            $attemptQuery = Invoke-AcceptanceDbQuery -Context $context -VersionId ([long]$versionId) `
                -Sql "SELECT a.id, a.work_item_id, a.attempt, a.status, COALESCE(LEFT(a.error, 200), '') FROM work_item_attempt a JOIN work_item w ON w.id = a.work_item_id WHERE w.version_id = $versionId AND w.type = 'execute.case' ORDER BY a.id"
            $attemptRows = @()
            foreach ($line in $attemptQuery.Rows) { $attemptRows += ,(Get-AcceptanceSplitRow -Line $line) }
            $attemptEvidence = @()
            foreach ($row in $attemptRows) { $attemptEvidence += [pscustomobject]@{ id = $row[0]; work_item_id = $row[1]; attempt = $row[2]; status = $row[3]; error = $row[4] } }
            [void](Write-S5Evidence -Name "execute-attempts-db.json" -Value ([pscustomobject]@{ database = $attemptQuery.Database; ok = $attemptQuery.Ok; error = $attemptQuery.Error; attempts = $attemptEvidence }) -Kind "db" -Note "work_item_attempt rows for execute.case")
            Add-AcceptanceCheck -Scenario $scenario -Name "execute.case job visible in the job model" -Passed ($jobQuery.Ok -and $jobEvidence.Count -ge 1) -Severity assertion `
                -Detail "database=$($jobQuery.Database) jobs=$($jobEvidence.Count) error=$($jobQuery.Error)"
            Add-AcceptanceCheck -Scenario $scenario -Name "job attempt rows visible" -Passed ($attemptQuery.Ok -and $attemptEvidence.Count -ge 1) -Severity assertion `
                -Detail "attempts=$($attemptEvidence.Count) error=$($attemptQuery.Error)"

            # ------------------------------------------------------------------
            # executor_lost = 0 and per-case verdict.
            # ------------------------------------------------------------------
            $lostRuns = @($runSummaries | Where-Object { $_.failure_class -eq "executor_lost" })
            $lostJobs = @($jobEvidence | Where-Object { $_.last_error -match 'executor_lost' })
            $lostAttempts = @($attemptEvidence | Where-Object { $_.error -match 'executor_lost' })
            $lostTotal = $lostRuns.Count + $lostJobs.Count + $lostAttempts.Count
            Add-AcceptanceCheck -Scenario $scenario -Name "executor_lost = 0" -Passed ($lostTotal -eq 0) -Severity assertion `
                -Detail "lostRuns=$($lostRuns.Count) lostJobs=$($lostJobs.Count) lostAttempts=$($lostAttempts.Count)"

            $graphAfter = Get-AcceptanceDesignGraph -Context $context -VersionId $versionId
            $cardsAfter = @(Get-AcceptanceCaseCards -Graph $graphAfter)
            $verdictByCase = @{}
            foreach ($card in $cardsAfter) { if ($card.execution_verdict) { $verdictByCase[$card.case_id] = $card.execution_verdict } }
            $executedCaseIds = @($runSummaries | ForEach-Object { $_.case_id } | Where-Object { $_ -ne "" } | Select-Object -Unique)
            $verdictEvidence = @()
            $missingVerdict = @()
            foreach ($caseId in $executedCaseIds) {
                $verdict = ""
                if ($verdictByCase.ContainsKey($caseId)) { $verdict = $verdictByCase[$caseId] }
                $verdictEvidence += [pscustomobject]@{ case_id = $caseId; execution_verdict = $verdict }
                if (-not $verdict) { $missingVerdict += $caseId }
            }
            [void](Write-S5Evidence -Name "per-case-verdicts.json" -Value $verdictEvidence -Note "graph execution_verdict per executed case")
            Add-AcceptanceCheck -Scenario $scenario -Name "per-case verdict visible in the design graph" -Passed ($missingVerdict.Count -eq 0 -and $executedCaseIds.Count -ge 1) -Severity assertion `
                -Detail "executedCases=$($executedCaseIds.Count) missingVerdict=$($missingVerdict -join ',')"

            # SSE events persisted around the execution.
            $sseRecords = @()
            try {
                $sse = Wait-SseEvent -Context $context -Channel "version:$versionId" -After $cursor -TimeoutSeconds 15 -MaxEvents 300
                foreach ($eventObject in @($sse.Events)) {
                    $sseRecords += [pscustomobject]@{ id = [string]$eventObject.Id; event = [string]$eventObject.Event; type = [string](Get-JsonProp -Object $eventObject.Data -Name 'type' -Default '') }
                }
            } catch { }
            [void](Write-S5Evidence -Name "sse-events.json" -Value $sseRecords -Kind "sse" -Note "execution SSE events")
            $executionEvents = @($sseRecords | Where-Object { $_.event -like 'execution.*' -or $_.event -like 'case_run.*' -or $_.event -eq 'job.finished' -or $_.event -eq 'job.progress' })
            Add-AcceptanceCheck -Scenario $scenario -Name "execution/job SSE events recorded" -Passed ($executionEvents.Count -ge 1) -Severity warning -Detail "events=$($executionEvents.Count)"

            try {
                $shot = Save-AcceptanceScreenshot -Context $context -Scenario $scenario -Route "/versions/${versionId}?tab=execution" -FileName "s5-execution.png" `
                    -WaitForSelector '[data-testid="version-header"]' -Note "execution page with wave/case-run verdicts"
                Add-AcceptanceCheck -Scenario $scenario -Name "execution screenshot" -Passed $true -Severity warning -Detail (Resolve-RepoRelativePath -Context $context -Path $shot.Path)
            } catch {
                Add-AcceptanceCheck -Scenario $scenario -Name "execution screenshot" -Passed $false -Severity warning -Detail $_.Exception.Message
            }

            [void](Update-AcceptanceState -Context $context -Path $statePath -Values @{
                version_id         = $versionId
                executed_case_ids  = $executedCaseIds
                executed_case_runs = @($runSummaries | ForEach-Object { $_.id })
                wave_ids           = @($waves | ForEach-Object { [string](Get-JsonProp -Object $_ -Name 'id' -Default '') })
                pause_exercised    = $pauseExercised
                executor_browser   = $executor.BrowserVersion
                executor_lost      = $lostTotal
                evidence_total     = $evidenceTotal
            })

            if (-not $terminalWait.Ok) { $scenario.ErrorMessage = "S5 failed: no terminal wave within ${ExecutionTimeoutSeconds}s" }
            elseif ($terminalRuns.Count -eq 0) { $scenario.ErrorMessage = "S5 failed: no terminal case run" }
            elseif ($lostTotal -gt 0) { $scenario.ErrorMessage = "S5 failed: executor_lost=$lostTotal" }
            elseif (-not $pauseExercised) { $scenario.ErrorMessage = "S5 failed: pause/resume could not be exercised" }
            elseif ($missingVerdict.Count -gt 0) { $scenario.ErrorMessage = "S5 failed: missing per-case verdict for $($missingVerdict -join ',')" }
            elseif (-not $evidenceGate.passed) { $scenario.ErrorMessage = "S5 failed: real execution evidence gate - $($evidenceGate.reason)" }
        }
    }
} catch {
    $scenario.ErrorMessage = $_.Exception.Message
    Add-AcceptanceCheck -Scenario $scenario -Name "S5 unexpected error" -Passed $false -Severity blocker -Detail ($_.Exception.Message + " | " + $_.ScriptStackTrace)
} finally {
    $exitCode = Complete-AcceptanceScenario -Scenario $scenario -Message "S5 real execution, pause/resume and per-case verdict"
}

exit $exitCode
