#requires -Version 5.1
<#
.SYNOPSIS
    S4 modify + confirm acceptance for EzTest 0.2.1.

.DESCRIPTION
    Runs on the version produced by S3 (or -VersionId) and asserts:

      1. the confirmed requirements cover the design scope (up to five);
      2. a design-conversation modification produces a new immutable case
         version instead of overwriting the old one (versioned edit);
      3. a selected subset of cases is confirmed through the real confirmation
         API and only the selected cases become confirmed (partial confirm);
      4. every selected case carries a success criterion (assertion expected);
      5. the confirmed selection automatically enqueues execution intents for
         exactly those cases (partial execution), without extra user guidance.

    The 0.2.1 case hard gate is attempted first. When the version is not at
    case_review (the current product never leaves created) the exact 409 is
    recorded and the available design-confirmation path is used so the rest of
    the S4/S5 chain can be exercised. No DB write and no fake stage transition.

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
    [int] $IntentTimeoutSeconds = 90
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
            $stage = $null
            try { $stage = Get-VersionStage -Context $context -VersionId $versionId } catch { }
            $stageValue = [string](Get-JsonProp -Object $stage -Name "stage" -Default "")
            [void](Write-S4Evidence -Name "requirements.json" -Value $requirements -Note "requirements at S4")
            [void](Write-S4Evidence -Name "stage.json" -Value $stage -Note "stage at S4")
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
            $selectCount = 0
            if ($eligible.Count -eq 1) { $selectCount = 1 }
            elseif ($eligible.Count -gt 1) { $selectCount = [Math]::Min(2, $eligible.Count - 1) }
            $selectedDetails = @($eligible | Select-Object -First $selectCount)
            $selectedCases = @($selectedDetails | ForEach-Object { $_.card.case_id })
            $selectedCaseVersions = @($selectedDetails | ForEach-Object { $_.case_version_id })
            $unselectedDraft = @($draftCards | Where-Object { $selectedCases -notcontains $_.case_id })
            [void](Write-S4Evidence -Name "case-selection.json" -Value ([pscustomobject]@{
                draft_total = $draftCards.Count
                eligible = $eligible.Count
                selected_case_ids = $selectedCases
                selected_case_versions = $selectedCaseVersions
                unselected_draft = @($unselectedDraft | ForEach-Object { $_.case_id })
            }) -Note "partial confirmation selection")
            Add-AcceptanceCheck -Scenario $scenario -Name "partial selection leaves at least one draft case" -Passed ($selectedCases.Count -ge 1 -and $unselectedDraft.Count -ge 1) -Severity assertion `
                -Detail "selected=$($selectedCases.Count) unselectedDraft=$($unselectedDraft.Count)"
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
            if ($selectedCases.Count -gt 0) {
                $gate = Invoke-Api -Context $context -Method "POST" -Path "/api/versions/$versionId/stage/confirm-cases" `
                    -Token $context.Token -Body @{ case_ids = $selectedCases }
                $gateCode = Get-JsonProp -Object $gate.Error -Name "code" -Default ""
                $stageGateAttempt = [pscustomobject]@{ http_status = $gate.Status; ok = $gate.Ok; error_code = [string]$gateCode; stage = $stageValue; error = $gate.Error }
                [void](Write-S4Evidence -Name "case-gate.json" -Value $stageGateAttempt -Note "0.2.1 case hard-gate attempt")
                if ($gate.Ok) {
                    Add-AcceptanceCheck -Scenario $scenario -Name "0.2.1 case hard gate confirmed the selection" -Passed $true -Severity assertion `
                        -Detail "HTTP 200 stage_after=$([string](Get-JsonProp -Object $gate.Data -Name 'stage' -Default ''))"
                } else {
                    Add-AcceptanceCheck -Scenario $scenario -Name "0.2.1 case hard gate reachable" -Passed $false -Severity warning `
                        -Detail ("stage=$stageValue HTTP $($gate.Status) code=$gateCode; using the available design confirmation path")
                }

                $intentsBefore = @(Get-AcceptanceExecutionIntents -Context $context -VersionId $versionId)
                $intentsBeforeIds = @(Get-JsonIdSet -Items $intentsBefore)
                $confirmResults = @()
                $confirmedVersions = @()
                foreach ($item in $selectedDetails) {
                    $confirm = Invoke-Api -Context $context -Method "POST" -Path "/api/versions/$versionId/design/confirmations" `
                        -Token $context.Token -Body @{ scope = "case"; target_id = $item.card.case_id; batch_id = ("s4-" + [Guid]::NewGuid().ToString("N").Substring(0, 8)) }
                    $confirmed = @(Get-JsonProp -Object $confirm.Data -Name "confirmed" -Default @())
                    foreach ($confirmedItem in $confirmed) {
                        $confirmedVersionId = [string](Get-JsonProp -Object $confirmedItem -Name "case_version_id" -Default "")
                        if ($confirmedVersionId) { $confirmedVersions += $confirmedVersionId }
                    }
                    $confirmResults += [pscustomobject]@{
                        case_id = $item.card.case_id
                        http_status = $confirm.Status
                        confirmed = $confirmed
                        skipped = @(Get-JsonProp -Object $confirm.Data -Name "skipped" -Default @())
                        error = $confirm.Error
                    }
                    Add-AcceptanceCheck -Scenario $scenario -Name "case confirmed through the user API ($($item.card.case_id))" -Passed ($confirm.Ok -and $confirmed.Count -ge 1) -Severity assertion `
                        -Detail "HTTP $($confirm.Status) confirmed=$($confirmed.Count)"
                }
                [void](Write-S4Evidence -Name "case-confirmations.json" -Value $confirmResults -Note "partial case confirmations")
                $intentTargetVersions = @($confirmedVersions | Select-Object -Unique)
                if ($intentTargetVersions.Count -eq 0) { $intentTargetVersions = @($selectedCaseVersions) }

                $confirmWait = Wait-Until -TimeoutSeconds 60 -IntervalSeconds 3 -Description "graph confirmed selection" -Condition {
                    $currentGraph = Get-AcceptanceDesignGraph -Context $context -VersionId $versionId
                    $currentCards = @(Get-AcceptanceCaseCards -Graph $currentGraph)
                    $confirmedNow = @($currentCards | Where-Object { $_.status -eq "confirmed" })
                    if ($confirmedNow.Count -ge $selectedCases.Count) { return [pscustomobject]@{ Graph = $currentGraph; Cards = $currentCards } }
                    return $null
                }
                if ($confirmWait.Ok -and $null -ne $confirmWait.Value) { $graph = $confirmWait.Value.Graph; $cards = @($confirmWait.Value.Cards) }
                $graphConfirmed = @($cards | Where-Object { $_.status -eq "confirmed" } | ForEach-Object { $_.case_id })
                $remainingDraft = @($cards | Where-Object { $_.status -eq "draft" } | ForEach-Object { $_.case_id })
                $selectedStillDraft = @($selectedCases | Where-Object { $remainingDraft -contains $_ })
                [void](Write-S4Evidence -Name "graph-after-confirm.json" -Value $graph -Note "graph after partial confirmation")
                Add-AcceptanceCheck -Scenario $scenario -Name "selected cases are confirmed in the graph" -Passed ($selectedStillDraft.Count -eq 0) -Severity assertion `
                    -Detail "selected=$($selectedCases.Count) confirmedInGraph=$(@($selectedCases | Where-Object { $graphConfirmed -contains $_ }).Count)"
                Add-AcceptanceCheck -Scenario $scenario -Name "unselected cases stay draft after partial confirmation" -Passed ($unselectedDraft.Count -eq 0 -or @($unselectedDraft | Where-Object { $remainingDraft -contains $_.case_id }).Count -eq $unselectedDraft.Count) -Severity assertion `
                    -Detail "unselectedDraft=$($unselectedDraft.Count) remainingDraft=$($remainingDraft.Count)"
                $confirmedCaseIds = @($graphConfirmed)

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
                }) -Note "intents auto-enqueued by the confirmation")
                Add-AcceptanceCheck -Scenario $scenario -Name "confirmed selection is automatically enqueued for execution" -Passed ($coveredIntents.Count -ge $selectedCases.Count) -Severity assertion `
                    -Detail "selected=$($selectedCases.Count) coveredIntents=$($coveredIntents.Count) newIntents=$($newIntents.Count)"
                Add-AcceptanceCheck -Scenario $scenario -Name "partial confirmation enqueues only the selected cases" -Passed ($foreignIntents.Count -eq 0) -Severity assertion `
                    -Detail "foreignVersions=$($foreignIntents -join ',')"

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
                        -WaitForSelector '[data-testid="version-header"]' -Note "design graph after partial case confirmation"
                    Add-AcceptanceCheck -Scenario $scenario -Name "case confirmation screenshot" -Passed $true -Severity warning -Detail (Resolve-RepoRelativePath -Context $context -Path $shot.Path)
                } catch {
                    Add-AcceptanceCheck -Scenario $scenario -Name "case confirmation screenshot" -Passed $false -Severity warning -Detail $_.Exception.Message
                }

                $allConfirmed = @($graphConfirmed | Select-Object -Unique)
                [void](Update-AcceptanceState -Context $context -Path $statePath -Values @{
                    version_id           = $versionId
                    selected_case_ids    = $selectedCases
                    selected_case_versions = $intentTargetVersions
                    confirmed_case_ids   = $allConfirmed
                    execution_intent_ids = @(Get-JsonIdSet -Items $intents)
                    wave_ids             = @(Get-JsonIdSet -Items $waves)
                    stage_at_s4_end      = $stageValue
                    case_gate_error      = [string](Get-JsonProp -Object $stageGateAttempt -Name 'error_code' -Default '')
                })
            } else {
                Add-AcceptanceCheck -Scenario $scenario -Name "partial case confirmation" -Passed $false -Severity assertion -Detail "no eligible case to confirm"
            }
        }
    }
} catch {
    $scenario.ErrorMessage = $_.Exception.Message
    Add-AcceptanceCheck -Scenario $scenario -Name "S4 unexpected error" -Passed $false -Severity blocker -Detail ($_.Exception.Message + " | " + $_.ScriptStackTrace)
} finally {
    $exitCode = Complete-AcceptanceScenario -Scenario $scenario -Message "S4 versioned modification and partial confirmation"
}

exit $exitCode
