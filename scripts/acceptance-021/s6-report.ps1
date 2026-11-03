#requires -Version 5.1
<#
.SYNOPSIS
    S6 report, cancel and per-case evidence acceptance for EzTest 0.2.1.

.DESCRIPTION
    Runs on the S5 version (or -VersionId) and asserts:

      1. a version without execution data does not get a report (no empty
         report);
      2. the executed version does get a published report revision with HTML,
         case rows, evidence and problems sections;
      3. cancelling a wave stops new dispatch: the wave is cancelled, its active
         intents are cancelled, every non-terminal case run becomes superseded,
         and no case run starts after the cancel;
      4. a case report Markdown file is written for every executed case run;
      5. failed cases are checked for a problem report; application failures
         must have a delivery issue, non-application failures are reported as a
         warning with the exact failure class (the product intentionally drafts
         problems only for application defects).

    Evidence is written under evidence/s3-s6/s6/, including report HTML,
    report text, cancellation records and case-*.md files.

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
    [int] $ReportTimeoutSeconds = 120,
    [int] $CancelProbeSeconds = 25
)

Set-StrictMode -Version 3.0
$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
if ([string]::IsNullOrWhiteSpace($EvidenceRoot)) {
    $EvidenceRoot = Join-Path $repoRoot "docs/version-V0.2.1/acceptance/evidence/s3-s6"
}

. (Join-Path $PSScriptRoot "lib.ps1")
. (Join-Path $PSScriptRoot "lib-s3-s6.ps1")

$context = Initialize-AcceptanceContext -ApiBaseUrl $ApiBaseUrl -WebBaseUrl $WebBaseUrl `
    -ExecutorUrl $ExecutorUrl -SutWebBaseUrl $SutWebBaseUrl -SutApiBaseUrl $SutApiBaseUrl `
    -AdminUsername $AdminUsername -AdminPassword $AdminPassword -SutPassword $SutPassword `
    -EvidenceRoot $EvidenceRoot
$scenario = New-AcceptanceScenario -Context $context -Name "s6" -Title "报告、取消与逐例报告 (S6)"
$statePath = Get-AcceptanceStatePath -Context $context -StatePath $StatePath
$state = Read-AcceptanceState -Path $statePath
$exitCode = 2
$versionId = $VersionId
if ([string]::IsNullOrWhiteSpace($versionId) -and $state.ContainsKey("version_id")) { $versionId = [string]$state["version_id"] }

function Write-S6Evidence {
    param([string] $Name, $Value, [string] $Kind = "api", [string] $Note = "")
    $path = Join-Path $scenario.Directory $Name
    Write-EvidenceJson -Path $path -Value $Value
    Add-AcceptanceArtifact -Scenario $scenario -Path $path -Kind $Kind -Note $Note
    return $path
}

function ConvertTo-ReportText {
    param([Parameter(Mandatory = $false)] [AllowEmptyString()] [string] $Html)
    if ([string]::IsNullOrEmpty($Html)) { return "" }
    $text = $Html -replace '<style[\s\S]*?</style>', ' '
    $text = $text -replace '<script[\s\S]*?</script>', ' '
    $text = $text -replace '<[^>]+>', ' '
    $text = $text -replace '&nbsp;', ' ' -replace '&amp;', '&' -replace '&lt;', '<' -replace '&gt;', '>' -replace '&quot;', '"'
    $text = $text -replace '\s+', ' '
    return $text.Trim()
}

$terminalRunStatuses = @("passed", "failed", "blocked", "inconclusive", "skipped", "superseded", "interrupted")
$activeWaveStatuses = @("planned", "pending", "running", "paused")

try {
    Write-AcceptanceLog -Scenario $scenario -Message "S6 report acceptance; version=$versionId state=$statePath"

    $login = Connect-EzTest -Context $context
    Add-AcceptanceCheck -Scenario $scenario -Name "EzTest admin login" -Passed $login.Ok -Severity blocker `
        -Detail ($(if ($login.Ok) { "user=" + [string](Get-JsonProp -Object $login.User -Name 'username' -Default '?') } else { $login.Error }))
    if (-not $login.Ok) {
        $scenario.ErrorMessage = "S6 blocked: EzTest admin login failed"
    } elseif ($versionId -eq "") {
        Add-AcceptanceCheck -Scenario $scenario -Name "S5 version available" -Passed $false -Severity blocker `
            -Detail "no -VersionId and no state.version_id at $statePath; run s3-design.ps1 .. s5-execute.ps1 first"
        $scenario.ErrorMessage = "S6 blocked: no version from S5"
    } else {
        # ------------------------------------------------------------------
        # Negative gate: a version without execution data must not get a report.
        # ------------------------------------------------------------------
        $emptyVersion = $null
        try {
            $binding = Resolve-AcceptanceSutBinding -Context $context
            $emptyVersion = New-AcceptanceVersion -Context $context -Title ("墨栈博客 0.2.1 验收 · S6 空版本 · " + (Get-Date).ToString("yyyyMMdd-HHmmss")) -Binding $binding.Binding
            $emptyVersionId = [string](Get-JsonProp -Object $emptyVersion -Name "id" -Default "")
            $emptyReports = @(Get-AcceptanceVersionReports -Context $context -VersionId $emptyVersionId)
            $emptyOverview = Get-AcceptanceExecutionOverview -Context $context -VersionId $emptyVersionId
            $emptyGraph = Get-AcceptanceDesignGraph -Context $context -VersionId $emptyVersionId
            $emptyCases = @(Get-AcceptanceCaseCards -Graph $emptyGraph)
            [void](Write-S6Evidence -Name "empty-version.json" -Value ([pscustomobject]@{
                version_id = $emptyVersionId
                reports = $emptyReports
                execution = $emptyOverview
                case_count = $emptyCases.Count
            }) -Note "version without execution data")
            Add-AcceptanceCheck -Scenario $scenario -Name "no report is generated for a version without execution data" -Passed ($emptyReports.Count -eq 0) -Severity assertion `
                -Detail "emptyVersion=$emptyVersionId reports=$($emptyReports.Count)"
            Add-AcceptanceCheck -Scenario $scenario -Name "empty version has no execution data" -Passed ([int](Get-JsonProp -Object (Get-JsonProp -Object $emptyOverview -Name "case_run_counts" -Default $null) -Name "total" -Default 0) -eq 0) -Severity assertion `
                -Detail "caseRuns=$([string](Get-JsonProp -Object (Get-JsonProp -Object $emptyOverview -Name "case_run_counts" -Default $null) -Name "total" -Default 0))"
        } catch {
            Add-AcceptanceCheck -Scenario $scenario -Name "no report is generated for a version without execution data" -Passed $false -Severity blocker -Detail $_.Exception.Message
        }

        $version = $null
        try {
            $version = Get-Version -Context $context -VersionId $versionId
            Add-AcceptanceCheck -Scenario $scenario -Name "executed version resolved" -Passed ($null -ne $version) -Severity blocker -Detail "version=$versionId"
        } catch {
            Add-AcceptanceCheck -Scenario $scenario -Name "executed version resolved" -Passed $false -Severity blocker -Detail $_.Exception.Message
            $scenario.ErrorMessage = "S6 blocked: version $versionId is not readable"
        }

        if ($null -ne $version) {
            # ------------------------------------------------------------------
            # Report data gate: wait for a published wave report with HTML.
            # ------------------------------------------------------------------
            $reportWait = Wait-Until -TimeoutSeconds $ReportTimeoutSeconds -IntervalSeconds 5 -Description "published report with HTML" -Condition {
                $current = @(Get-AcceptanceVersionReports -Context $context -VersionId $versionId)
                foreach ($report in $current) {
                    $revision = Get-JsonProp -Object $report -Name "current_revision" -Default $null
                    if ($null -ne $revision -and [bool](Get-JsonProp -Object $revision -Name "html_available" -Default $false)) {
                        return [pscustomobject]@{ Reports = $current; Report = $report; Revision = $revision }
                    }
                }
                return $null
            }
            $reports = @(Get-AcceptanceVersionReports -Context $context -VersionId $versionId)
            [void](Write-S6Evidence -Name "reports.json" -Value $reports -Note "version reports")
            Add-AcceptanceCheck -Scenario $scenario -Name "executed version has a report" -Passed ($reports.Count -ge 1) -Severity assertion -Detail "reports=$($reports.Count)"
            $publishedReport = $null
            $publishedRevision = $null
            if ($reportWait.Ok -and $null -ne $reportWait.Value) {
                $publishedReport = $reportWait.Value.Report
                $publishedRevision = $reportWait.Value.Revision
            }
            Add-AcceptanceCheck -Scenario $scenario -Name "report has a published revision with HTML" -Passed ($null -ne $publishedReport -and $null -ne $publishedRevision) -Severity assertion `
                -Detail ("reports=$($reports.Count); waitOk=$($reportWait.Ok)")

            $reportHtml = ""
            if ($null -ne $publishedReport) {
                $reportId = [string](Get-JsonProp -Object $publishedReport -Name "id" -Default "")
                $reportDetail = Get-AcceptanceReportDetail -Context $context -VersionId $versionId -ReportId $reportId
                [void](Write-S6Evidence -Name "report-detail.json" -Value $reportDetail -Note "published report detail")
                $revisionNo = [string](Get-JsonProp -Object $publishedRevision -Name "revision" -Default "1")
                $htmlResponse = Invoke-Api -Context $context -Method "GET" -Path "/api/versions/$versionId/reports/$reportId/revisions/$revisionNo/html" -Token $context.Token -TimeoutSeconds 60
                if ($htmlResponse.Ok -and -not [string]::IsNullOrWhiteSpace($htmlResponse.Text)) {
                    $reportHtml = [string]$htmlResponse.Text
                    $htmlPath = Join-Path $scenario.Directory "report.html"
                    Write-EvidenceText -Path $htmlPath -Text $reportHtml
                    Add-AcceptanceArtifact -Scenario $scenario -Path $htmlPath -Kind "report" -Note "published report HTML"
                    $textPath = Join-Path $scenario.Directory "report.txt"
                    $reportText = ConvertTo-ReportText -Html $reportHtml
                    Write-EvidenceText -Path $textPath -Text $reportText
                    Add-AcceptanceArtifact -Scenario $scenario -Path $textPath -Kind "report" -Note "published report text"
                }
                Add-AcceptanceCheck -Scenario $scenario -Name "report HTML is downloadable" -Passed ($htmlResponse.Ok -and $reportHtml.Length -gt 500) -Severity assertion `
                    -Detail "HTTP $($htmlResponse.Status) bytes=$($reportHtml.Length)"
                $refs = @(Get-JsonProp -Object $reportDetail -Name "refs" -Default @())
                $refKinds = @($refs | ForEach-Object { [string](Get-JsonProp -Object $_ -Name "kind" -Default "") } | Select-Object -Unique)
                Add-AcceptanceCheck -Scenario $scenario -Name "report references cases, waves and evidence rows" -Passed ($refs.Count -ge 1 -and ($refKinds -contains "case_run" -or $refKinds -contains "case_version")) -Severity assertion `
                    -Detail "refs=$($refs.Count) kinds=$($refKinds -join ",")"
            }

            # ------------------------------------------------------------------
            # Gather all case runs (source for report content and per-case md).
            # ------------------------------------------------------------------
            $waves = @(Get-AcceptanceWaves -Context $context -VersionId $versionId)
            $allRuns = @()
            $runIds = @{}
            foreach ($wave in $waves) {
                $waveId = [string](Get-JsonProp -Object $wave -Name "id" -Default "")
                foreach ($run in @(Get-AcceptanceWaveCaseRuns -Context $context -VersionId $versionId -WaveId $waveId)) {
                    $runId = [string](Get-JsonProp -Object $run -Name "id" -Default "")
                    if ($runId -eq "" -or $runIds.ContainsKey($runId)) { continue }
                    $runIds[$runId] = $true
                    $allRuns += [pscustomobject]@{
                        id              = $runId
                        wave_id         = $waveId
                        intent_id       = [string](Get-JsonProp -Object $run -Name "intent_id" -Default "")
                        case_id         = [string](Get-JsonProp -Object $run -Name "case_id" -Default "")
                        case_version_id = [string](Get-JsonProp -Object $run -Name "case_version_id" -Default "")
                        title           = [string](Get-JsonProp -Object $run -Name "title" -Default "")
                        status          = [string](Get-JsonProp -Object $run -Name "status" -Default "")
                        failure_class   = [string](Get-JsonProp -Object $run -Name "failure_class" -Default "")
                        attempts        = [int](Get-JsonProp -Object $run -Name "attempts" -Default 0)
                        evidence_count  = [int](Get-JsonProp -Object $run -Name "evidence_count" -Default 0)
                        verdict_reason  = [string](Get-JsonProp -Object (Get-JsonProp -Object $run -Name "summary" -Default $null) -Name "verdict_reason" -Default "")
                    }
                }
            }
            Add-AcceptanceCheck -Scenario $scenario -Name "executed case runs available for reporting" -Passed ($allRuns.Count -ge 1) -Severity assertion -Detail "caseRuns=$($allRuns.Count)"

            if ($reportHtml.Length -gt 0) {
                $titles = @($allRuns | ForEach-Object { $_.title } | Where-Object { $_ -ne "" } | Select-Object -Unique)
                $titleFound = $false
                foreach ($title in $titles) {
                    if ($reportHtml -match [regex]::Escape($title)) { $titleFound = $true; break }
                }
                Add-AcceptanceCheck -Scenario $scenario -Name "report contains executed case rows" -Passed ($titleFound) -Severity assertion `
                    -Detail "caseTitles=$($titles.Count) titleFound=$titleFound"
                Add-AcceptanceCheck -Scenario $scenario -Name "report contains an execution-result section" -Passed ($reportHtml -match '执行结果') -Severity assertion -Detail "section=执行结果"
                Add-AcceptanceCheck -Scenario $scenario -Name "report contains a problems section" -Passed ($reportHtml -match '问题') -Severity assertion -Detail "section=问题"
                Add-AcceptanceCheck -Scenario $scenario -Name "report contains an evidence section" -Passed ($reportHtml -match '证据') -Severity assertion -Detail "section=证据"
            }

            $issuesResult = Get-AcceptanceVersionIssues -Context $context -VersionId $versionId
            [void](Write-S6Evidence -Name "issues.json" -Value ([pscustomobject]@{ ok = $issuesResult.Ok; error = $issuesResult.Error; items = $issuesResult.Items }) -Note "delivery issues/problem reports")
            $issuesJson = ""
            if ($issuesResult.Items.Count -gt 0) { $issuesJson = $issuesResult.Items | ConvertTo-Json -Depth 20 -Compress }
            $failedRuns = @($allRuns | Where-Object { $_.status -eq "failed" })
            $applicationFailures = @($failedRuns | Where-Object { $_.failure_class -eq "application" })
            $nonApplicationFailures = @($failedRuns | Where-Object { $_.failure_class -ne "application" })
            $unreportedApplication = @()
            foreach ($run in $applicationFailures) {
                if ($issuesJson -notmatch [regex]::Escape($run.id)) { $unreportedApplication += $run.id }
            }
            if ($applicationFailures.Count -gt 0) {
                Add-AcceptanceCheck -Scenario $scenario -Name "application failures have a problem report" -Passed ($unreportedApplication.Count -eq 0) -Severity assertion `
                    -Detail "application=$($applicationFailures.Count) unreported=$($unreportedApplication -join ',')"
            } elseif ($failedRuns.Count -gt 0) {
                Add-AcceptanceCheck -Scenario $scenario -Name "application failures have a problem report" -Passed $true -Severity warning `
                    -Detail ("no application failure; failed cases are non-application classes: " + (($nonApplicationFailures | ForEach-Object { $_.failure_class }) -join ",") + "; the product drafts problems only for application defects (D47)")
            } else {
                Add-AcceptanceCheck -Scenario $scenario -Name "application failures have a problem report" -Passed $true -Severity warning -Detail "no failed case"
            }
            Add-AcceptanceCheck -Scenario $scenario -Name "report problem section is populated when issues exist" -Passed ($issuesResult.Items.Count -eq 0 -or ($reportHtml -match [regex]::Escape([string](Get-JsonProp -Object $issuesResult.Items[0] -Name 'title' -Default '')) -or $reportHtml -match '问题')) -Severity warning `
                -Detail "issues=$($issuesResult.Items.Count)"

            # ------------------------------------------------------------------
            # Cancel: create a fresh wave, hold it paused and cancel it. Assert
            # that cancellation is terminal for the wave and stops dispatch.
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

            # S5's execution may have closed the requirements. A retest wave
            # (needed for the cancel probe) requires an open requirement, so
            # reopen them through the user API exactly as S5 does.
            $requirements = @(Get-VersionRequirements -Context $context -VersionId $versionId -Limit 200)
            $closedRequirements = @($requirements | Where-Object { [string](Get-JsonProp -Object $_ -Name 'status' -Default '') -eq 'closed' })
            $reopenRecords = @()
            foreach ($requirement in ($closedRequirements | Select-Object -First 6)) {
                $requirementId = [string](Get-JsonProp -Object $requirement -Name 'id' -Default '')
                if (-not $requirementId) { continue }
                $reopen = Invoke-AcceptanceRequirementReopen -Context $context -VersionId $versionId -RequirementId $requirementId -Reason "S6 acceptance cancel probe"
                $reopenRecords += [pscustomobject]@{ requirement_id = $requirementId; http_status = $reopen.Status; ok = $reopen.Ok; error = $reopen.Error }
            }
            [void](Write-S6Evidence -Name "requirement-reopen.json" -Value $reopenRecords -Note "requirements reopened for the S6 cancel probe")
            Add-AcceptanceCheck -Scenario $scenario -Name "requirements reopened for the cancel probe" -Passed (@($reopenRecords | Where-Object { $_.ok }).Count -eq $reopenRecords.Count) -Severity warning `
                -Detail "reopened=$(@($reopenRecords | Where-Object { $_.ok }).Count)/$($reopenRecords.Count)"

            $wavesBefore = @(Get-AcceptanceWaves -Context $context -VersionId $versionId)
            $waveIdsBefore = @($wavesBefore | ForEach-Object { [string](Get-JsonProp -Object $_ -Name "id" -Default "") })
            $cancelRecord = [pscustomobject]@{
                attempt              = 0
                result               = "not attempted"
                wave_id              = ""
                created_intents      = @()
                pause_http           = 0
                pause_status         = ""
                cancel_http          = 0
                cancel_status        = ""
                wave_after           = $null
                runs_after           = @()
                active_runs_after    = 0
                started_after_cancel = @()
                intent_statuses      = @()
                cancelled_at         = ""
            }
            $cancelExercised = $false
            for ($attempt = 1; $attempt -le 2 -and -not $cancelExercised; $attempt++) {
                $createdIntents = @()
                foreach ($caseVersion in ($confirmedCaseVersions | Select-Object -First 3)) {
                    $manual = New-AcceptanceManualIntent -Context $context -VersionId $versionId -CaseVersionId $caseVersion -Purpose "manual"
                    if ($manual.Ok) { $createdIntents += [string](Get-JsonProp -Object $manual.Data -Name "id" -Default "") }
                }
                $newWave = $null
                $pollDeadline = (Get-Date).AddSeconds($CancelProbeSeconds)
                while ((Get-Date) -lt $pollDeadline) {
                    Start-Sleep -Milliseconds 200
                    $currentWaves = @(Get-AcceptanceWaves -Context $context -VersionId $versionId)
                    $newWave = $currentWaves | Where-Object {
                        $waveIdsBefore -notcontains [string](Get-JsonProp -Object $_ -Name "id" -Default "") -and
                        $activeWaveStatuses -contains [string](Get-JsonProp -Object $_ -Name "status" -Default "")
                    } | Select-Object -First 1
                    if ($null -ne $newWave) { break }
                }
                if ($null -eq $newWave) {
                    # Keep the full record shape: the no-wave path is reported
                    # below as a failed assertion, not as an unexpected error.
                    $cancelRecord = [pscustomobject]@{
                        attempt              = $attempt
                        result               = "no new active wave"
                        wave_id              = ""
                        created_intents      = $createdIntents
                        pause_http           = 0
                        pause_status         = ""
                        cancel_http          = 0
                        cancel_status        = ""
                        wave_after           = $null
                        runs_after           = @()
                        active_runs_after    = 0
                        started_after_cancel = @()
                        intent_statuses      = @()
                        cancelled_at         = ""
                    }
                    continue
                }
                $newWaveId = [string](Get-JsonProp -Object $newWave -Name "id" -Default "")
                $pause = Invoke-AcceptanceWaveAction -Context $context -VersionId $versionId -WaveId $newWaveId -Action "pause" -Reason "S6 cancel probe"
                Start-Sleep -Milliseconds 500
                $cancel = Invoke-AcceptanceWaveAction -Context $context -VersionId $versionId -WaveId $newWaveId -Action "cancel" -Reason "S6 cancel probe"
                $cancelAt = (Get-Date).ToUniversalTime()
                Start-Sleep -Seconds 5
                $waveAfter = Get-AcceptanceWave -Context $context -VersionId $versionId -WaveId $newWaveId
                $runsAfter = @(Get-AcceptanceWaveCaseRuns -Context $context -VersionId $versionId -WaveId $newWaveId)
                $activeAfter = @($runsAfter | Where-Object { $activeWaveStatuses -contains [string](Get-JsonProp -Object $_ -Name "status" -Default "") })
                $startedAfterCancel = @()
                foreach ($run in $runsAfter) {
                    $startedAt = [string](Get-JsonProp -Object $run -Name "started_at" -Default "")
                    if ($startedAt) {
                        try {
                            if ([DateTime]::Parse($startedAt).ToUniversalTime() -gt $cancelAt) { $startedAfterCancel += [string](Get-JsonProp -Object $run -Name "id" -Default "") }
                        } catch { }
                    }
                }
                $intentsAfter = @(Get-AcceptanceExecutionIntents -Context $context -VersionId $versionId)
                $intentStatuses = @($intentsAfter | Where-Object { $createdIntents -contains [string](Get-JsonProp -Object $_ -Name "id" -Default "") } | ForEach-Object { [string](Get-JsonProp -Object $_ -Name "status" -Default "") })
                $cancelRecord = [pscustomobject]@{
                    attempt            = $attempt
                    wave_id            = $newWaveId
                    created_intents    = $createdIntents
                    pause_http         = $pause.Status
                    pause_status       = [string](Get-JsonProp -Object $pause.Data -Name "status" -Default "")
                    cancel_http        = $cancel.Status
                    cancel_status      = [string](Get-JsonProp -Object $cancel.Data -Name "status" -Default "")
                    wave_after         = $waveAfter
                    runs_after         = $runsAfter
                    active_runs_after  = $activeAfter.Count
                    started_after_cancel = $startedAfterCancel
                    intent_statuses    = $intentStatuses
                    cancelled_at       = $cancelAt.ToString("o")
                }
                $cancelExercised = ([string](Get-JsonProp -Object $waveAfter -Name "status" -Default "") -eq "cancelled")
            }
            [void](Write-S6Evidence -Name "cancel-probe.json" -Value $cancelRecord -Note "wave cancel probe")
            Add-AcceptanceCheck -Scenario $scenario -Name "execution wave can be cancelled" -Passed $cancelExercised -Severity assertion `
                -Detail ("attempts=$($cancelRecord.attempt) status=$($cancelRecord.cancel_status)")
            if ($cancelExercised) {
                Add-AcceptanceCheck -Scenario $scenario -Name "cancel stops new dispatch" -Passed (@($cancelRecord.started_after_cancel).Count -eq 0 -and [int]$cancelRecord.active_runs_after -eq 0) -Severity assertion `
                    -Detail "startedAfterCancel=$(@($cancelRecord.started_after_cancel).Count) activeRuns=$($cancelRecord.active_runs_after)"
                $superseded = @($cancelRecord.runs_after | Where-Object { [string](Get-JsonProp -Object $_ -Name "status" -Default "") -in @("superseded", "cancelled", "passed", "failed", "blocked", "inconclusive", "skipped", "interrupted") })
                Add-AcceptanceCheck -Scenario $scenario -Name "cancelled wave leaves only terminal case runs" -Passed ($superseded.Count -eq @($cancelRecord.runs_after).Count) -Severity assertion `
                    -Detail "runs=$(@($cancelRecord.runs_after).Count) terminal=$($superseded.Count)"
                # QA-F2: cancel must propagate to the wave's intents even when a
                # case_run already reached a terminal status before the cancel.
                # Check the intents referenced by the cancelled wave's case runs:
                # they must not stay open/claimed/running after the cancel.
                $activeIntentStatuses = @("open", "claimed", "running")
                $waveIntentIds = @($cancelRecord.runs_after | ForEach-Object { [string](Get-JsonProp -Object $_ -Name "intent_id" -Default "") } | Where-Object { $_ -ne "" } | Select-Object -Unique)
                $intentStatusById = @{}
                foreach ($intent in $intentsAfter) {
                    $intentStatusById[[string](Get-JsonProp -Object $intent -Name "id" -Default "")] = [string](Get-JsonProp -Object $intent -Name "status" -Default "")
                }
                $missingWaveIntents = @($waveIntentIds | Where-Object { -not $intentStatusById.ContainsKey($_) })
                $waveIntentStatuses = @($waveIntentIds | ForEach-Object { if ($intentStatusById.ContainsKey($_)) { [string]$intentStatusById[$_] } })
                $activeIntentsAfter = @($waveIntentStatuses | Where-Object { $activeIntentStatuses -contains [string]$_ })
                Add-AcceptanceCheck -Scenario $scenario -Name "cancelled wave leaves no active intents" -Passed ($waveIntentIds.Count -gt 0 -and $missingWaveIntents.Count -eq 0 -and $activeIntentsAfter.Count -eq 0) -Severity assertion `
                    -Detail "waveIntents=$($waveIntentIds.Count) missing=$($missingWaveIntents.Count) activeAfter=$($activeIntentsAfter.Count) statuses=$($waveIntentStatuses -join ',')"
            } else {
                Add-AcceptanceCheck -Scenario $scenario -Name "cancel stops new dispatch" -Passed $false -Severity assertion -Detail "no wave could be cancelled"
                Add-AcceptanceCheck -Scenario $scenario -Name "cancelled wave leaves only terminal case runs" -Passed $false -Severity assertion -Detail "no wave could be cancelled"
                Add-AcceptanceCheck -Scenario $scenario -Name "cancelled wave leaves no active intents" -Passed $false -Severity assertion -Detail "no wave could be cancelled"
            }

            # Direct intent cancel (open intent) as a separate API assertion.
            $cancelIntentRecord = $null
            if ($confirmedCaseVersions.Count -ge 1) {
                $manualIntent = New-AcceptanceManualIntent -Context $context -VersionId $versionId -CaseVersionId $confirmedCaseVersions[0] -Purpose "retest"
                if ($manualIntent.Ok) {
                    $manualIntentId = [string](Get-JsonProp -Object $manualIntent.Data -Name "id" -Default "")
                    $intentCancel = Invoke-AcceptanceIntentCancel -Context $context -VersionId $versionId -IntentId $manualIntentId
                    $cancelIntentRecord = [pscustomobject]@{ intent_id = $manualIntentId; http_status = $intentCancel.Status; status = [string](Get-JsonProp -Object $intentCancel.Data -Name "status" -Default "") }
                } else {
                    $cancelIntentRecord = [pscustomobject]@{ http_status = $manualIntent.Status; status = ""; error = $manualIntent.Error }
                }
            }
            [void](Write-S6Evidence -Name "intent-cancel.json" -Value $cancelIntentRecord -Note "direct intent cancel")
            Add-AcceptanceCheck -Scenario $scenario -Name "execution intent can be cancelled" -Passed ($null -ne $cancelIntentRecord -and $cancelIntentRecord.http_status -eq 200 -and [string]$cancelIntentRecord.status -eq "cancelled") -Severity assertion `
                -Detail ("HTTP " + $(if ($null -ne $cancelIntentRecord) { [string]$cancelIntentRecord.http_status } else { "n/a" }) + " status=" + $(if ($null -ne $cancelIntentRecord) { [string]$cancelIntentRecord.status } else { "" }))

            # ------------------------------------------------------------------
            # One Markdown case report per executed case run.
            # ------------------------------------------------------------------
            $caseDir = Join-Path $scenario.Directory "cases"
            New-Item -ItemType Directory -Force -Path $caseDir | Out-Null
            $reportsWritten = 0
            $failedIds = @($failedRuns | ForEach-Object { $_.id })
            foreach ($run in $allRuns) {
                $detail = $null
                try { $detail = Get-AcceptanceCaseRun -Context $context -VersionId $versionId -CaseRunId $run.id } catch { }
                $steps = @(Get-JsonProp -Object $detail -Name "steps" -Default @())
                $assertions = @(Get-JsonProp -Object $detail -Name "assertions" -Default @())
                $evidenceRows = @(Get-JsonProp -Object $detail -Name "evidence" -Default @())
                $linkedReport = ""
                if ($null -ne $publishedReport) { $linkedReport = [string](Get-JsonProp -Object $publishedReport -Name "id" -Default "") }
                $linkedIssue = ($issuesJson -match [regex]::Escape($run.id))
                $md = @()
                $md += "# Case report - $($run.id)"
                $md += ""
                $md += "- Version: $versionId"
                $md += "- Case: $($run.case_id) (case version $($run.case_version_id))"
                $md += "- Title: $($run.title)"
                $md += "- Wave: $($run.wave_id)"
                $md += "- Intent: $($run.intent_id)"
                $md += "- Status: $($run.status)"
                $md += "- Failure class: $($run.failure_class)"
                $md += "- Attempts: $($run.attempts)"
                $md += "- Verdict reason: $($run.verdict_reason)"
                $md += "- Evidence rows: $($run.evidence_count)"
                if ($linkedReport) { $md += "- Report: $linkedReport" }
                $md += "- Problem report linked: $linkedIssue"
                $md += ""
                $md += "## Steps"
                if ($steps.Count -eq 0) { $md += "- (none recorded by the execution)" }
                foreach ($step in $steps) {
                    $md += "- Step $([string](Get-JsonProp -Object $step -Name 'seq' -Default '')): $([string](Get-JsonProp -Object $step -Name 'status' -Default '')) - $([string](Get-JsonProp -Object $step -Name 'error' -Default ''))"
                }
                $md += ""
                $md += "## Assertions"
                if ($assertions.Count -eq 0) { $md += "- (none recorded by the execution)" }
                foreach ($assertion in $assertions) {
                    $md += "- $([string](Get-JsonProp -Object $assertion -Name 'kind' -Default '')): $([string](Get-JsonProp -Object $assertion -Name 'verdict' -Default '')) expected=$([string](Get-JsonProp -Object $assertion -Name 'expected' -Default '')) actual=$([string](Get-JsonProp -Object $assertion -Name 'actual' -Default ''))"
                }
                $md += ""
                $md += "## Evidence"
                if ($evidenceRows.Count -eq 0) { $md += "- (no evidence captured for this run)" }
                foreach ($evidenceRow in $evidenceRows) {
                    $md += "- $([string](Get-JsonProp -Object $evidenceRow -Name 'kind' -Default '')) id=$([string](Get-JsonProp -Object $evidenceRow -Name 'id' -Default '')) sha256=$([string](Get-JsonProp -Object $evidenceRow -Name 'sha256' -Default ''))"
                }
                $md += ""
                $md += "## Problem report"
                if ($linkedIssue) { $md += "A delivery issue references this case run." }
                elseif ($run.failure_class -eq "application") { $md += "MISSING: an application failure must have a problem report." }
                else { $md += "No delivery issue is expected for failure_class=$($run.failure_class) (the product drafts problems only for application defects, D47)." }
                $mdPath = Join-Path $caseDir ("case-" + $run.id + ".md")
                Write-EvidenceText -Path $mdPath -Text ($md -join [Environment]::NewLine)
                Add-AcceptanceArtifact -Scenario $scenario -Path $mdPath -Kind "case-report" -Note "per-case report for $($run.case_id)"
                $reportsWritten++
            }
            Add-AcceptanceCheck -Scenario $scenario -Name "per-case report Markdown written for every executed case" -Passed ($reportsWritten -eq $allRuns.Count -and $reportsWritten -ge 1) -Severity assertion `
                -Detail "caseReports=$reportsWritten runs=$($allRuns.Count)"

            try {
                $shot = Save-AcceptanceScreenshot -Context $context -Scenario $scenario -Route "/versions/${versionId}?tab=reports" -FileName "s6-report.png" `
                    -WaitForSelector '[data-testid="version-header"]' -Note "published report page"
                Add-AcceptanceCheck -Scenario $scenario -Name "report screenshot" -Passed $true -Severity warning -Detail (Resolve-RepoRelativePath -Context $context -Path $shot.Path)
            } catch {
                Add-AcceptanceCheck -Scenario $scenario -Name "report screenshot" -Passed $false -Severity warning -Detail $_.Exception.Message
            }

            [void](Update-AcceptanceState -Context $context -Path $statePath -Values @{
                version_id       = $versionId
                empty_version_id = if ($null -ne $emptyVersion) { [string](Get-JsonProp -Object $emptyVersion -Name "id" -Default "") } else { "" }
                report_id        = if ($null -ne $publishedReport) { [string](Get-JsonProp -Object $publishedReport -Name "id" -Default "") } else { "" }
                report_revision  = if ($null -ne $publishedRevision) { [string](Get-JsonProp -Object $publishedRevision -Name "revision" -Default "") } else { "" }
                case_reports      = $reportsWritten
                report_issues     = $issuesResult.Items.Count
                cancel_exercised  = $cancelExercised
            })

            if ($null -eq $publishedReport) { $scenario.ErrorMessage = "S6 failed: no published report with HTML" }
            elseif ($allRuns.Count -eq 0) { $scenario.ErrorMessage = "S6 failed: no executed case runs to report" }
        }
    }
} catch {
    $scenario.ErrorMessage = $_.Exception.Message
    Add-AcceptanceCheck -Scenario $scenario -Name "S6 unexpected error" -Passed $false -Severity blocker -Detail ($_.Exception.Message + " | " + $_.ScriptStackTrace)
} finally {
    $exitCode = Complete-AcceptanceScenario -Scenario $scenario -Message "S6 report, cancel and per-case reports"
}

exit $exitCode
