#requires -Version 5.1
<#
.SYNOPSIS
    S4 modify + confirm acceptance for EzTest 0.2.1.

.DESCRIPTION
    Runs on the version produced by S3 (or -VersionId) and asserts:

      1. the confirmed requirements cover the design scope (up to five);
      2. a design-conversation modification produces a new immutable case
         version instead of overwriting the old one (versioned edit);
      3. the version reached case_review and the wrong gate (POST
         /stage/confirm-requirements) is rejected there with the typed 409
         invalid_stage_for_gate without moving the stage;
      4. a selected subset of draft cases is confirmed through the real 0.2.1
         case hard gate POST /stage/confirm-cases: exactly the selected cases
         become confirmed, each selected case is completely confirmed, and the
         unselected cases remain draft;
      5. the case hard gate advances case_review -> executing and enqueues
         execute.batch for exactly the selected case ids, and the confirmed
         selection automatically enqueues execution intents for exactly those
         cases (partial execution), without extra user guidance.

    There is deliberately no 0.2 fallback: when the version is not at
    case_review, the script records the typed 409 invalid_stage_for_gate and
    fails. It never falls back to POST /design/confirmations and never writes a
    stage or confirmation through SQL.

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
    [int] $DesignReplyTimeoutSeconds = 240,
    [int] $IntentTimeoutSeconds = 90,
    [int] $CaseReviewTimeoutSeconds = 120
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
$scenario = New-AcceptanceScenario -Context $context -Name "s4" -Title "修改版本化与部分确认 (S4)"
$statePath = Get-AcceptanceStatePath -Context $context -StatePath $StatePath
$state = Read-AcceptanceState -Path $statePath
$exitCode = 2
$versionId = $VersionId
if ([string]::IsNullOrWhiteSpace($versionId) -and $state.ContainsKey("version_id")) { $versionId = [string]$state["version_id"] }
$selectedCases = @()
$selectedCaseVersions = @()
$confirmedCaseIds = @()

function Write-S4Evidence {
    param([string] $Name, $Value, [string] $Kind = "api", [string] $Note = "")
    $path = Join-Path $scenario.Directory $Name
    Write-EvidenceJson -Path $path -Value $Value
    Add-AcceptanceArtifact -Scenario $scenario -Path $path -Kind $Kind -Note $Note
    return $path
}

function Get-JsonIdSet {
    param([Parameter(Mandatory = $false)] [AllowEmptyCollection()] $Items, [string] $Name = "id")
    return @(@($Items) | ForEach-Object { [string](Get-JsonProp -Object $_ -Name $Name -Default "") } | Where-Object { $_ -ne "" })
}

try {
    Write-AcceptanceLog -Scenario $scenario -Message "S4 confirm acceptance; version=$versionId state=$statePath"

    $login = Connect-EzTest -Context $context
    Add-AcceptanceCheck -Scenario $scenario -Name "EzTest admin login" -Passed $login.Ok -Severity blocker `
        -Detail ($(if ($login.Ok) { "user=" + [string](Get-JsonProp -Object $login.User -Name 'username' -Default '?') } else { $login.Error }))
    if (-not $login.Ok) {
        $scenario.ErrorMessage = "S4 blocked: EzTest admin login failed"
    } elseif ($versionId -eq "") {
        Add-AcceptanceCheck -Scenario $scenario -Name "S3 version available" -Passed $false -Severity blocker `
            -Detail "no -VersionId and no state.version_id at $statePath; run s3-design.ps1 first"
        $scenario.ErrorMessage = "S4 blocked: no version from S3"
    } else {
        $version = $null
        try {
            $version = Get-Version -Context $context -VersionId $versionId
            Add-AcceptanceCheck -Scenario $scenario -Name "version resolved" -Passed ($null -ne $version) -Severity blocker -Detail "version=$versionId"
        } catch {
            Add-AcceptanceCheck -Scenario $scenario -Name "version resolved" -Passed $false -Severity blocker -Detail $_.Exception.Message
            $scenario.ErrorMessage = "S4 blocked: version $versionId is not readable"
        }

        if ($null -ne $version) {
            # ------------------------------------------------------------------
            # Requirements and stage.
            # ------------------------------------------------------------------
            $requirements = @(Get-VersionRequirements -Context $context -VersionId $versionId -Limit 200)
            # The design generation (S3) owns designing -> case_review. Wait for
            # the product to reach the case hard gate's only source stage; the
            # script never writes the stage itself.
            $stageWait = Wait-Until -TimeoutSeconds $CaseReviewTimeoutSeconds -IntervalSeconds 3 -Description "case_review stage" -Condition {
                try { $current = Get-VersionStage -Context $context -VersionId $versionId } catch { return $null }
                if ([string](Get-JsonProp -Object $current -Name "stage" -Default "") -eq "case_review") { return $current }
                return $null
            }
            $stage = $null
            if ($stageWait.Ok) { $stage = $stageWait.Value }
            else { try { $stage = Get-VersionStage -Context $context -VersionId $versionId } catch { } }
            $stageValue = [string](Get-JsonProp -Object $stage -Name "stage" -Default "")
            [void](Write-S4Evidence -Name "requirements.json" -Value $requirements -Note "requirements at S4")
            [void](Write-S4Evidence -Name "stage.json" -Value $stage -Note "stage at S4")
            Add-AcceptanceCheck -Scenario $scenario -Name "version reached case_review for the case hard gate" -Passed ($stageValue -eq "case_review") -Severity assertion `
                -Detail ("stage=$stageValue; expected case_review (design generation completion owns designing -> case_review; no fallback is used)")
            $confirmedStatuses = @("clarified", "confirmed", "closed")
            $nonDraft = @($requirements | Where-Object { $confirmedStatuses -contains [string](Get-JsonProp -Object $_ -Name "status" -Default "") })
            Add-AcceptanceCheck -Scenario $scenario -Name "requirements confirmed before design" -Passed ($requirements.Count -gt 0 -and $nonDraft.Count -eq $requirements.Count) -Severity assertion `
                -Detail ("requirements=$($requirements.Count) nonDraft=$($nonDraft.Count) statuses=$((($requirements | ForEach-Object { [string](Get-JsonProp -Object $_ -Name 'status' -Default '') }) -join ','))")
            if ($nonDraft.Count -ge 5) {
                Add-AcceptanceCheck -Scenario $scenario -Name "five requirements confirmed" -Passed $true -Severity assertion -Detail "confirmed=$($nonDraft.Count)"
            } else {
                Add-AcceptanceCheck -Scenario $scenario -Name "five requirements confirmed" -Passed $false -Severity warning `
                    -Detail "confirmed=$($nonDraft.Count); the real clarification run produced fewer than five requirements"
            }
            $accountRoles = @()
            foreach ($binding in @(Get-JsonProp -Object $version -Name "bindings" -Default @())) {
                foreach ($account in @(Get-JsonProp -Object (Get-JsonProp -Object $binding -Name "environment" -Default $null) -Name "accounts" -Default @())) {
                    foreach ($role in @(Get-JsonProp -Object $account -Name "roles" -Default @())) { $accountRoles += [string]$role }
                }
            }
            $accountRoles = @($accountRoles | Select-Object -Unique)
            Add-AcceptanceCheck -Scenario $scenario -Name "two SUT roles available for the selected cases" -Passed ($accountRoles.Count -ge 2) -Severity warning `
                -Detail "roles=$($accountRoles -join ",")"

            # ------------------------------------------------------------------
            # Versioned modification through the design conversation.
            # ------------------------------------------------------------------
            $designConversation = Get-AcceptanceDesignConversation -Context $context -VersionId $versionId
            $designConversationId = ""
            if ($null -ne $designConversation) { $designConversationId = [string](Get-JsonProp -Object $designConversation -Name "id" -Default "") }
            Add-AcceptanceCheck -Scenario $scenario -Name "design conversation available for modification" -Passed ($designConversationId -ne "") -Severity assertion `
                -Detail "conversation=$designConversationId"

            $graph = Get-AcceptanceDesignGraph -Context $context -VersionId $versionId
            $beforeCards = @(Get-AcceptanceCaseCards -Graph $graph)
            $beforeIds = @($beforeCards | ForEach-Object { $_.case_id })
            $modifyTarget = $beforeCards | Where-Object { $_.status -eq "draft" } | Select-Object -First 1
            if ($null -eq $modifyTarget) { $modifyTarget = $beforeCards | Select-Object -First 1 }
            $modifyCaseId = ""
            $beforeRevisionCount = 0
            $beforeCaseCount = $beforeCards.Count
            if ($null -ne $modifyTarget) {
                $modifyCaseId = $modifyTarget.case_id
                $targetDetail = $null
                try {
                    $targetDetail = Get-AcceptanceCaseDetail -Context $context -VersionId $versionId -CaseId $modifyCaseId
                    $beforeRevisionCount = @(Get-JsonProp -Object $targetDetail -Name "versions" -Default @()).Count
                } catch { }
                [void](Write-S4Evidence -Name "modification-before.json" -Value ([pscustomobject]@{
                    case_id = $modifyCaseId
                    title = $modifyTarget.title
                    versions = $beforeRevisionCount
                    graph_cases = $beforeCaseCount
                    case = $targetDetail
                }) -Note "case before design-conversation modification")
            }

            if ($designConversationId -ne "" -and $modifyCaseId -ne "") {
                $modifyText = @"
请只修改现有用例（case_id=$modifyCaseId，标题「$($modifyTarget.title)」），不要新建用例、不要确认：
1. 保留原有步骤与断言；
2. 为每一步补上 target_version_sut_id=$([string](Get-JsonProp -Object (Get-JsonProp -Object $version -Name "bindings" -Default @())[0] -Name "id" -Default ""))；
3. 把第 1 步动作改为「打开墨栈博客首页 $($context.SutWebBaseUrl)/」，expected 改为「首页加载成功」；
4. 用 case.graph_propose 更新这个 case，并说明新版本号；
5. 其他用例保持不动。
"@
                $modifyMessage = $null
                try {
                    $modifyMessage = New-AcceptanceUserMessage -Context $context -VersionId $versionId -ConversationId $designConversationId -Text $modifyText
                    Add-AcceptanceCheck -Scenario $scenario -Name "design modification message accepted" -Passed ($null -ne $modifyMessage) -Severity assertion `
                        -Detail ("message=" + [string](Get-JsonProp -Object $modifyMessage -Name 'id' -Default ''))
                } catch {
                    Add-AcceptanceCheck -Scenario $scenario -Name "design modification message accepted" -Passed $false -Severity assertion -Detail $_.Exception.Message
                }
                $modifyWait = Wait-Until -TimeoutSeconds $DesignReplyTimeoutSeconds -IntervalSeconds 10 -Description "versioned case modification" -Condition {
                    $currentDetail = Get-AcceptanceCaseDetail -Context $context -VersionId $versionId -CaseId $modifyCaseId
                    $revisions = @(Get-JsonProp -Object $currentDetail -Name "versions" -Default @()).Count
                    $currentGraph = Get-AcceptanceDesignGraph -Context $context -VersionId $versionId
                    $currentCards = @(Get-AcceptanceCaseCards -Graph $currentGraph)
                    if ($revisions -gt $beforeRevisionCount -or $currentCards.Count -gt $beforeCaseCount) {
                        return [pscustomobject]@{ Detail = $currentDetail; Revisions = $revisions; Cards = $currentCards; Graph = $currentGraph }
                    }
                    return $null
                }
                $modifyResult = $null
                if ($modifyWait.Ok -and $null -ne $modifyWait.Value) { $modifyResult = $modifyWait.Value }
                $afterDetail = $null
                try { $afterDetail = Get-AcceptanceCaseDetail -Context $context -VersionId $versionId -CaseId $modifyCaseId } catch { }
                $afterRevisionCount = 0
                if ($null -ne $afterDetail) { $afterRevisionCount = @(Get-JsonProp -Object $afterDetail -Name "versions" -Default @()).Count }
                $modifyMessages = @()
                try { $modifyMessages = @(Get-AcceptanceMessages -Context $context -VersionId $versionId -ConversationId $designConversationId -Limit 200) } catch { }
                $toolCalls = @(Get-AcceptanceToolCalls -Messages $modifyMessages)
                $graphProposeCalls = @($toolCalls | Where-Object { $_.tool -eq "case.graph_propose" })
                $lastAssistantText = ""
                $assistantMsgs = @($modifyMessages | Where-Object { [string](Get-JsonProp -Object $_ -Name "role" -Default "") -eq "assistant" })
                if ($assistantMsgs.Count -gt 0) { $lastAssistantText = Get-AssistantText -Message ($assistantMsgs[$assistantMsgs.Count - 1]) }
                [void](Write-S4Evidence -Name "design-modification.json" -Value ([pscustomobject]@{
                    case_id = $modifyCaseId
                    message_id = [string](Get-JsonProp -Object $modifyMessage -Name 'id' -Default '')
                    before_revisions = $beforeRevisionCount
                    after_revisions = $afterRevisionCount
                    graph_propose_calls = $graphProposeCalls
                    last_assistant_text = $lastAssistantText
                    new_versions = if ($null -ne $afterDetail) { Get-JsonProp -Object $afterDetail -Name "versions" -Default @() } else { @() }
                }) -Note "design-conversation modification result")
                Add-AcceptanceCheck -Scenario $scenario -Name "conversation modification creates a new case version" -Passed ($afterRevisionCount -gt $beforeRevisionCount) -Severity assertion `
                    -Detail "before=$beforeRevisionCount after=$afterRevisionCount case=$modifyCaseId"
                Add-AcceptanceCheck -Scenario $scenario -Name "modification used case.graph_propose" -Passed ($graphProposeCalls.Count -ge 1) -Severity warning `
                    -Detail "calls=$($graphProposeCalls.Count)"
                if ($afterRevisionCount -le $beforeRevisionCount) {
                    $scenario.ErrorMessage = "S4 failed: design conversation did not create a new case version for $modifyCaseId"
                }
            } else {
                Add-AcceptanceCheck -Scenario $scenario -Name "conversation modification creates a new case version" -Passed $false -Severity assertion `
                    -Detail "no design conversation or no case to modify"
            }

            # ------------------------------------------------------------------
            # Partial case confirmation with a success criterion.
            # ------------------------------------------------------------------
            $graph = Get-AcceptanceDesignGraph -Context $context -VersionId $versionId
            $cards = @(Get-AcceptanceCaseCards -Graph $graph)
            $draftCards = @($cards | Where-Object { $_.status -eq "draft" })
            $candidateDetails = @()
            foreach ($card in ($draftCards | Select-Object -First 6)) {
                try {
                    $detail = Get-AcceptanceCaseDetail -Context $context -VersionId $versionId -CaseId $card.case_id
                    $selected = Get-JsonProp -Object $detail -Name "selected" -Default $null
                    $candidateDetails += [pscustomobject]@{
                        card       = $card
                        detail     = $detail
                        selected   = $selected
                        case_version_id = [string](Get-JsonProp -Object $selected -Name 'case_version_id' -Default $card.case_version_id)
                        steps      = @(Get-JsonProp -Object $selected -Name 'steps' -Default @())
                        assertions = @(Get-JsonProp -Object $selected -Name 'assertions' -Default @())
                        sources    = @(Get-JsonProp -Object $selected -Name 'sources' -Default @())
                    }
                } catch {
                    Write-AcceptanceLog -Scenario $scenario -Level warn -Message "case detail $($card.case_id) failed: $($_.Exception.Message)"
                }
            }
            $eligible = @($candidateDetails | Where-Object { $_.assertions.Count -ge 1 })
            if ($eligible.Count -eq 0) { $eligible = $candidateDetails }
            # A partial selection is only honest when at least two eligible draft
            # cases exist: select a strict subset and leave the rest draft. A
            # version with a single eligible case exercises the gate's complete
            # ("all selected") path instead, recorded as such, never as partial.
            $partialSelection = $eligible.Count -ge 2
            $selectCount = 0
            if ($eligible.Count -ge 2) { $selectCount = [Math]::Min(2, $eligible.Count - 1) }
            elseif ($eligible.Count -eq 1) { $selectCount = 1 }
            $selectedDetails = @($eligible | Select-Object -First $selectCount)
            $selectedCases = @($selectedDetails | ForEach-Object { $_.card.case_id })
            $selectedCaseVersions = @($selectedDetails | ForEach-Object { $_.case_version_id })
            $unselectedDraft = @($draftCards | Where-Object { $selectedCases -notcontains $_.case_id })
            [void](Write-S4Evidence -Name "case-selection.json" -Value ([pscustomobject]@{
                draft_total = $draftCards.Count
                eligible = $eligible.Count
                partial = $partialSelection
                selected_case_ids = $selectedCases
                selected_case_versions = $selectedCaseVersions
                unselected_draft = @($unselectedDraft | ForEach-Object { $_.case_id })
            }) -Note "case gate selection (strict subset when at least two eligible draft cases exist)")
            if ($partialSelection) {
                Add-AcceptanceCheck -Scenario $scenario -Name "partial selection leaves at least one draft case" -Passed ($selectedCases.Count -ge 1 -and $unselectedDraft.Count -ge 1) -Severity assertion `
                    -Detail "selected=$($selectedCases.Count) unselectedDraft=$($unselectedDraft.Count) draftTotal=$($draftCards.Count)"
            } elseif ($selectedCases.Count -ge 1) {
                Add-AcceptanceCheck -Scenario $scenario -Name "complete selection confirms every draft case" -Passed ($unselectedDraft.Count -eq 0) -Severity warning `
                    -Detail "only one eligible draft case exists; the gate confirms it completely (the partial variant cannot be exercised on this version)"
            } else {
                Add-AcceptanceCheck -Scenario $scenario -Name "eligible draft case available for the case hard gate" -Passed $false -Severity assertion `
                    -Detail "draftCards=$($draftCards.Count) eligible=0"
            }
            foreach ($item in $selectedDetails) {
                $successCriteria = @($item.assertions | Where-Object { -not [string]::IsNullOrWhiteSpace([string](Get-JsonProp -Object $_ -Name 'expected' -Default '')) })
                Add-AcceptanceCheck -Scenario $scenario -Name "selected case has a success criterion" -Passed ($successCriteria.Count -ge 1) -Severity assertion `
                    -Detail "case=$($item.card.case_id) assertions=$($item.assertions.Count) withExpected=$($successCriteria.Count)"
            }
            $stepRoles = @()
            foreach ($item in $selectedDetails) {
                foreach ($step in $item.steps) {
                    $role = [string](Get-JsonProp -Object $step -Name "account_role" -Default "")
                    if ($role) { $stepRoles += $role }
                }
            }
            $stepRoles = @($stepRoles | Select-Object -Unique)
            Add-AcceptanceCheck -Scenario $scenario -Name "selected cases cover two account roles" -Passed ($stepRoles.Count -ge 2) -Severity warning `
                -Detail "stepRoles=$($stepRoles -join ",")"

            $stageGateAttempt = $null
            $gateAdvanced = $false
            $stageAfterGateValue = $stageValue
            $intentsBefore = @()
            $intentsBeforeIds = @()
            $intents = @()
            $waves = @()
            $intentTargetVersions = @($selectedCaseVersions | Select-Object -Unique)
            $confirmedCaseIds = @()

            # ------------------------------------------------------------------
            # Negative gate assertion: the requirement gate may only run at
            # requirement_review. Calling it here must return the typed 409
            # invalid_stage_for_gate and must not move the stage.
            # ------------------------------------------------------------------
            if ($stageValue -ne "requirement_review") {
                $wrongGate = Invoke-Api -Context $context -Method "POST" -Path "/api/versions/$versionId/stage/confirm-requirements" `
                    -Token $context.Token -Body @{ requirement_ids = @() }
                $wrongGateCode = [string](Get-JsonProp -Object $wrongGate.Error -Name "code" -Default "")
                $stageAfterWrongGate = $null
                try { $stageAfterWrongGate = Get-VersionStage -Context $context -VersionId $versionId } catch { }
                $stageAfterWrongGateValue = [string](Get-JsonProp -Object $stageAfterWrongGate -Name "stage" -Default "")
                [void](Write-S4Evidence -Name "wrong-gate-requirement.json" -Value ([pscustomobject]@{
                    http_status  = $wrongGate.Status
                    ok           = $wrongGate.Ok
                    error_code   = $wrongGateCode
                    error        = $wrongGate.Error
                    stage_before = $stageValue
                    stage_after  = $stageAfterWrongGateValue
                }) -Note "requirement hard gate called after the requirement gate (must be rejected)")
                Add-AcceptanceCheck -Scenario $scenario -Name "invalid_stage_for_gate rejected the requirement gate at $stageValue" `
                    -Passed ($wrongGate.Status -eq 409 -and $wrongGateCode -eq "invalid_stage_for_gate" -and $stageAfterWrongGateValue -eq $stageValue) `
                    -Severity assertion `
                    -Detail ("HTTP $($wrongGate.Status) code=$wrongGateCode stage=$stageValue->$stageAfterWrongGateValue")
            } else {
                Add-AcceptanceCheck -Scenario $scenario -Name "version passed the requirement hard gate before S4" -Passed $false -Severity assertion `
                    -Detail "stage=requirement_review; S4 requires the version to be past the requirement gate (case_review)"
            }

            if ($selectedCases.Count -eq 0) {
                Add-AcceptanceCheck -Scenario $scenario -Name "eligible case selected for the case hard gate" -Passed $false -Severity assertion `
                    -Detail "no eligible draft case was available; the case hard gate cannot be exercised honestly"
                $scenario.ErrorMessage = "S4 failed: no eligible draft case available for the case hard gate"
            } elseif ($stageValue -ne "case_review") {
                # Record the typed rejection with the exact source stage. The
                # script never falls back to POST /design/confirmations.
                $gate = Invoke-Api -Context $context -Method "POST" -Path "/api/versions/$versionId/stage/confirm-cases" `
                    -Token $context.Token -Body @{ case_ids = $selectedCases }
                $gateCode = [string](Get-JsonProp -Object $gate.Error -Name "code" -Default "")
                $stageAfterGate = $null
                try { $stageAfterGate = Get-VersionStage -Context $context -VersionId $versionId } catch { }
                $stageAfterGateValue = [string](Get-JsonProp -Object $stageAfterGate -Name "stage" -Default "")
                $stageGateAttempt = [pscustomobject]@{
                    http_status  = $gate.Status
                    ok           = $gate.Ok
                    error_code   = $gateCode
                    error        = $gate.Error
                    stage_before = $stageValue
                    stage_after  = $stageAfterGateValue
                }
                [void](Write-S4Evidence -Name "case-gate.json" -Value $stageGateAttempt -Note "0.2.1 case hard-gate attempt at the wrong source stage")
                Add-AcceptanceCheck -Scenario $scenario -Name "invalid_stage_for_gate rejected the case gate at $stageValue" `
                    -Passed ($gate.Status -eq 409 -and $gateCode -eq "invalid_stage_for_gate" -and $stageAfterGateValue -eq $stageValue) `
                    -Severity assertion `
                    -Detail ("HTTP $($gate.Status) code=$gateCode stage=$stageValue->$stageAfterGateValue; the case gate requires case_review")
                $scenario.ErrorMessage = "S4 failed: case gate requires case_review but stage=$stageValue (HTTP $($gate.Status) $gateCode); no fallback is used"
            } else {
                $intentsBefore = @(Get-AcceptanceExecutionIntents -Context $context -VersionId $versionId)
                $intentsBeforeIds = @(Get-JsonIdSet -Items $intentsBefore)

                # Positive gate assertion: the real 0.2.1 case hard gate confirms
                # exactly the selected subset and advances case_review ->
                # executing. It is the only confirmation path used by S4.
                $gate = Invoke-Api -Context $context -Method "POST" -Path "/api/versions/$versionId/stage/confirm-cases" `
                    -Token $context.Token -Body @{ case_ids = $selectedCases }
                $gateCode = [string](Get-JsonProp -Object $gate.Error -Name "code" -Default "")
                $gateStage = [string](Get-JsonProp -Object $gate.Data -Name "stage" -Default "")
                $stageAfterGate = $null
                try { $stageAfterGate = Get-VersionStage -Context $context -VersionId $versionId } catch { }
                $stageAfterGateValue = [string](Get-JsonProp -Object $stageAfterGate -Name "stage" -Default "")
                $stageGateAttempt = [pscustomobject]@{
                    http_status  = $gate.Status
                    ok           = $gate.Ok
                    response     = $gate.Data
                    error_code   = $gateCode
                    error        = $gate.Error
                    stage_before = $stageValue
                    stage_after  = $stageAfterGateValue
                    case_ids     = $selectedCases
                }
                [void](Write-S4Evidence -Name "case-gate.json" -Value $stageGateAttempt -Note "0.2.1 case hard gate (partial selection)")
                [void](Write-S4Evidence -Name "stage-after-case-gate.json" -Value $stageAfterGate -Note "stage after the case hard gate")
                $gateAdvanced = $gate.Ok -and $gateStage -eq "executing" -and $stageAfterGateValue -eq "executing"
                Add-AcceptanceCheck -Scenario $scenario -Name "0.2.1 case hard gate advanced case_review to executing" `
                    -Passed $gateAdvanced -Severity assertion `
                    -Detail ("HTTP $($gate.Status) code=$gateCode responseStage=$gateStage readbackStage=$stageAfterGateValue selected=$($selectedCases.Count)")

                if (-not $gateAdvanced) {
                    $scenario.ErrorMessage = "S4 failed: the 0.2.1 case hard gate did not advance the version (HTTP $($gate.Status) $gateCode); no fallback is used"
                } else {
                    # Read the graph back: the selected cases must be completely
                    # confirmed and the unselected cases must remain draft.
                    $confirmWait = Wait-Until -TimeoutSeconds 60 -IntervalSeconds 3 -Description "graph confirmed selection" -Condition {
                        $currentGraph = Get-AcceptanceDesignGraph -Context $context -VersionId $versionId
                        $currentCards = @(Get-AcceptanceCaseCards -Graph $currentGraph)
                        $confirmedNow = @($currentCards | Where-Object { $selectedCases -contains $_.case_id -and $_.status -eq "confirmed" })
                        if ($confirmedNow.Count -ge $selectedCases.Count) { return [pscustomobject]@{ Graph = $currentGraph; Cards = $currentCards } }
                        return $null
                    }
                    if ($confirmWait.Ok -and $null -ne $confirmWait.Value) { $graph = $confirmWait.Value.Graph; $cards = @($confirmWait.Value.Cards) }
                    $graphConfirmed = @($cards | Where-Object { $_.status -eq "confirmed" } | ForEach-Object { $_.case_id })
                    $remainingDraft = @($cards | Where-Object { $_.status -eq "draft" } | ForEach-Object { $_.case_id })
                    $selectedStillDraft = @($selectedCases | Where-Object { $remainingDraft -contains $_ })
                    $selectedNotConfirmed = @($selectedCases | Where-Object { $graphConfirmed -notcontains $_ })
                    $graphSelectedVersions = @($cards | Where-Object { $selectedCases -contains $_.case_id } | ForEach-Object { $_.case_version_id } | Where-Object { $_ -ne "" } | Select-Object -Unique)
                    if ($graphSelectedVersions.Count -ge $selectedCases.Count) { $intentTargetVersions = $graphSelectedVersions }
                    [void](Write-S4Evidence -Name "graph-after-confirm.json" -Value $graph -Note "graph after the partial case hard gate")
                    Add-AcceptanceCheck -Scenario $scenario -Name "selected cases are completely confirmed in the graph" `
                        -Passed ($selectedStillDraft.Count -eq 0 -and $selectedNotConfirmed.Count -eq 0) -Severity assertion `
                        -Detail "selected=$($selectedCases.Count) confirmedInGraph=$(@($selectedCases | Where-Object { $graphConfirmed -contains $_ }).Count) stillDraft=$($selectedStillDraft.Count)"
                    Add-AcceptanceCheck -Scenario $scenario -Name "unselected cases stay draft after the partial hard gate" `
                        -Passed ($unselectedDraft.Count -eq 0 -or @($unselectedDraft | Where-Object { $remainingDraft -contains $_.case_id }).Count -eq $unselectedDraft.Count) -Severity assertion `
                        -Detail "unselectedDraft=$($unselectedDraft.Count) remainingDraft=$($remainingDraft.Count)"
                    $confirmedCaseIds = @($graphConfirmed)

                    # The gate must have dispatched execute.batch for exactly the
                    # selected case ids (read-only DB projection).
                    $executeBatchDb = Invoke-AcceptanceDbQuery -Context $context -VersionId ([long]$versionId) `
                        -Sql "SELECT id, team, type, kind, status, created_by, inputs::text FROM work_item WHERE version_id = $versionId AND kind = 'execute.batch' ORDER BY id DESC"
                    $executeBatchRows = @($executeBatchDb.Rows)
                    $executeBatchCovers = $false
                    $executeBatchForeign = @()
                    $executeBatchIds = @()
                    foreach ($row in $executeBatchRows) {
                        $columns = @(Get-AcceptanceSplitRow -Line ([string]$row))
                        if ($columns.Count -lt 1) { continue }
                        $rawInputs = [string]$columns[$columns.Count - 1]
                        try { $parsedInputs = $rawInputs | ConvertFrom-Json -ErrorAction Stop } catch { continue }
                        $ids = @(Get-JsonProp -Object $parsedInputs -Name "case_ids" -Default @())
                        $idsAsStrings = @($ids | ForEach-Object { [string]$_ })
                        $missing = @($selectedCases | Where-Object { $idsAsStrings -notcontains $_ })
                        if ($missing.Count -eq 0 -and $idsAsStrings.Count -ge $selectedCases.Count) {
                            $executeBatchCovers = $true
                            $executeBatchIds = $idsAsStrings
                            $executeBatchForeign = @($idsAsStrings | Where-Object { $selectedCases -notcontains $_ })
                            break
                        }
                    }
                    [void](Write-S4Evidence -Name "execute-batch-job-db.json" -Value ([pscustomobject]@{
                        database = $executeBatchDb.Database
                        ok       = $executeBatchDb.Ok
                        error    = $executeBatchDb.Error
                        rows     = $executeBatchRows
                        selected_case_ids = $selectedCases
                        matched_case_ids  = $executeBatchIds
                        foreign_case_ids  = $executeBatchForeign
                    }) -Kind "db" -Note "execute.batch stage-entry work item (read-only DB projection)")
                    Add-AcceptanceCheck -Scenario $scenario -Name "case hard gate enqueued execute.batch for the selected cases" `
                        -Passed ($executeBatchDb.Ok -and $executeBatchRows.Count -ge 1 -and $executeBatchCovers) -Severity assertion `
                        -Detail ("database=$($executeBatchDb.Database) rows=$($executeBatchRows.Count) selected=$($selectedCases.Count) matchedIds=$($executeBatchIds -join ',')")
                    Add-AcceptanceCheck -Scenario $scenario -Name "case hard gate did not enqueue unselected cases" `
                        -Passed ($executeBatchForeign.Count -eq 0) -Severity assertion `
                        -Detail "foreignCaseIds=$($executeBatchForeign -join ',')"

                    # ------------------------------------------------------------------
                    # Confirm -> automatic execution intents for the selection only.
                    # ------------------------------------------------------------------
                    $intentWait = Wait-Until -TimeoutSeconds $IntentTimeoutSeconds -IntervalSeconds 3 -Description "execution intents for selection" -Condition {
                        $current = @(Get-AcceptanceExecutionIntents -Context $context -VersionId $versionId)
                        $covered = @($current | Where-Object { $intentTargetVersions -contains [string](Get-JsonProp -Object $_ -Name 'case_version_id' -Default '') })
                        if ($covered.Count -ge $selectedCases.Count) { return $current }
                        return $null
                    }
                    $intents = @(Get-AcceptanceExecutionIntents -Context $context -VersionId $versionId)
                    $newIntents = @($intents | Where-Object { $intentsBeforeIds -notcontains [string](Get-JsonProp -Object $_ -Name 'id' -Default '') })
                    $coveredIntents = @($intents | Where-Object { $intentTargetVersions -contains [string](Get-JsonProp -Object $_ -Name 'case_version_id' -Default '') })
                    $intentVersions = @($newIntents | ForEach-Object { [string](Get-JsonProp -Object $_ -Name 'case_version_id' -Default '') } | Select-Object -Unique)
                    $foreignIntents = @($intentVersions | Where-Object { $intentTargetVersions -notcontains $_ })
                    [void](Write-S4Evidence -Name "execution-intents.json" -Value ([pscustomobject]@{
                        before = $intentsBefore
                        after = $intents
                        new = $newIntents
                        selected_case_versions = $intentTargetVersions
                        covered = $coveredIntents
                        foreign_versions = $foreignIntents
                    }) -Note "intents auto-enqueued by the case hard gate")
                    Add-AcceptanceCheck -Scenario $scenario -Name "confirmed selection is automatically enqueued for execution" `
                        -Passed ($coveredIntents.Count -ge $selectedCases.Count) -Severity assertion `
                        -Detail "selected=$($selectedCases.Count) coveredIntents=$($coveredIntents.Count) newIntents=$($newIntents.Count)"
                    Add-AcceptanceCheck -Scenario $scenario -Name "partial confirmation enqueues only the selected cases" `
                        -Passed ($foreignIntents.Count -eq 0) -Severity assertion `
                        -Detail "foreignVersions=$($foreignIntents -join ',')"

                    # The wave is created asynchronously by the intent scan; wait
                    # for it before asserting, so a fast executor cannot race the
                    # assertion (the criterion is "wave exists", not "wave exists
                    # in the same millisecond as the intents").
                    [void](Wait-Until -TimeoutSeconds $IntentTimeoutSeconds -IntervalSeconds 2 -Description "execution wave for confirmed selection" -Condition {
                        $currentWaves = @(Get-AcceptanceWaves -Context $context -VersionId $versionId)
                        if ($currentWaves.Count -ge 1) { return $currentWaves }
                        return $null
                    })
                    $waves = @(Get-AcceptanceWaves -Context $context -VersionId $versionId)
                    [void](Write-S4Evidence -Name "waves.json" -Value $waves -Note "waves created by the confirmation chain")
                    Add-AcceptanceCheck -Scenario $scenario -Name "execution wave created for the confirmed selection" -Passed ($waves.Count -ge 1) -Severity assertion -Detail "waves=$($waves.Count)"

                    $cursorBefore = 0
                    try { $cursorBefore = Get-EventCursor -Context $context -Channel "version:$versionId" } catch { }
                    $sseRecords = @()
                    try {
                        $sse = Wait-SseEvent -Context $context -Channel "version:$versionId" -After $cursorBefore -TimeoutSeconds 15 -MaxEvents 200
                        foreach ($eventObject in @($sse.Events)) {
                            $sseRecords += [pscustomobject]@{ id = [string]$eventObject.Id; event = [string]$eventObject.Event; type = [string](Get-JsonProp -Object $eventObject.Data -Name 'type' -Default '') }
                        }
                    } catch { }
                    [void](Write-S4Evidence -Name "sse-events.json" -Value $sseRecords -Kind "sse" -Note "confirmation/execution SSE events")
                    $confirmationEvents = @($sseRecords | Where-Object { $_.event -like '*confirm*' -or $_.event -like 'execution.*' -or $_.event -eq 'job.finished' })
                    Add-AcceptanceCheck -Scenario $scenario -Name "confirmation/execution SSE events recorded" -Passed ($confirmationEvents.Count -ge 1) -Severity warning `
                        -Detail "events=$($confirmationEvents.Count)"

                    try {
                        $shot = Save-AcceptanceScreenshot -Context $context -Scenario $scenario -Route "/versions/${versionId}?tab=design" -FileName "s4-case-confirmation.png" `
                            -WaitForSelector '[data-testid="version-header"]' -Note "design graph after the partial case hard gate"
                        Add-AcceptanceCheck -Scenario $scenario -Name "case confirmation screenshot" -Passed $true -Severity warning -Detail (Resolve-RepoRelativePath -Context $context -Path $shot.Path)
                    } catch {
                        Add-AcceptanceCheck -Scenario $scenario -Name "case confirmation screenshot" -Passed $false -Severity warning -Detail $_.Exception.Message
                    }
                }
            }

            [void](Update-AcceptanceState -Context $context -Path $statePath -Values @{
                version_id             = $versionId
                selected_case_ids      = $selectedCases
                unselected_case_ids    = @($unselectedDraft | ForEach-Object { $_.case_id })
                selected_case_versions = $intentTargetVersions
                confirmed_case_ids     = @($confirmedCaseIds)
                execution_intent_ids   = @(Get-JsonIdSet -Items $intents)
                wave_ids               = @(Get-JsonIdSet -Items $waves)
                stage_at_s4_end        = $stageAfterGateValue
                case_gate_advanced     = $gateAdvanced
                case_gate_error        = [string](Get-JsonProp -Object $stageGateAttempt -Name 'error_code' -Default '')
            })
        }
    }
} catch {
    $scenario.ErrorMessage = $_.Exception.Message
    Add-AcceptanceCheck -Scenario $scenario -Name "S4 unexpected error" -Passed $false -Severity blocker -Detail ($_.Exception.Message + " | " + $_.ScriptStackTrace)
} finally {
    $exitCode = Complete-AcceptanceScenario -Scenario $scenario -Message "S4 versioned modification and partial confirmation"
}

exit $exitCode
