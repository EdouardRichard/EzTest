#requires -Version 5.1
<#
.SYNOPSIS
    S2 clarification acceptance for EzTest 0.2.1.

.DESCRIPTION
    Drives a real-LLM clarification conversation for a freshly created 墨栈博客
    version and asserts the four S2 criteria:

      1. every requirement gets at most six clarification questions
         (aggregate budget = 6 * current requirement count);
      2. no question is asked twice (normalized duplicate detection);
      3. the version stops at the requirement confirmation gate
         (stage == requirement_review, requirements still draft);
      4. it does not auto-advance into design or execution.

    The script treats assistant text parts as user-visible questions; private
    thinking/tool_call parts are excluded. The version is driven for at least
    two clarification turns so that "multi-turn" is actually exercised.

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
    [int] $MaxTurns = 4,
    [int] $ReplyTimeoutSeconds = 150,
    [int] $QuestionBudgetPerRequirement = 6,
    [int] $VersionWideQuestionBudget = 30
)

Set-StrictMode -Version 3.0
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "lib.ps1")

function Wait-NewAssistantText {
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $VersionId,
        [Parameter(Mandatory = $true)] [string] $ConversationId,
        [Parameter(Mandatory = $true)] [string] $AfterMessageId,
        [Parameter(Mandatory = $false)] [int] $TimeoutSeconds = 150
    )
    $after = [long]$AfterMessageId
    $watcher = Wait-Until -TimeoutSeconds $TimeoutSeconds -IntervalSeconds 2 -Description "assistant reply" -Condition {
        $messages = @(Get-ConversationMessages -Context $Context -VersionId $VersionId -ConversationId $ConversationId -Limit 200)
        $candidate = $messages | Where-Object {
            [string](Get-JsonProp -Object $_ -Name "role" -Default "") -eq "assistant" -and
            [long](Get-JsonProp -Object $_ -Name "id" -Default "0") -gt $after -and
            (Get-AssistantText -Message $_).Trim().Length -gt 0
        } | Sort-Object { [long](Get-JsonProp -Object $_ -Name "id" -Default "0") } | Select-Object -First 1
        if ($null -ne $candidate) { return $candidate }
        return $null
    }
    return $watcher
}

function Test-RequirementConfirmationRequest {
    param([Parameter(Mandatory = $false)] [string] $Text)
    if ([string]::IsNullOrWhiteSpace($Text)) { return $false }
    return ($Text -match '需求确认' -or $Text -match '确认.{0,12}(需求|草案|设计)' -or $Text -match 'confirm_request')
}

$context = Initialize-AcceptanceContext -ApiBaseUrl $ApiBaseUrl -WebBaseUrl $WebBaseUrl `
    -ExecutorUrl $ExecutorUrl -SutWebBaseUrl $SutWebBaseUrl -SutApiBaseUrl $SutApiBaseUrl `
    -AdminUsername $AdminUsername -AdminPassword $AdminPassword -SutPassword $SutPassword `
    -EvidenceRoot $EvidenceRoot
$scenario = New-AcceptanceScenario -Context $context -Name "s2" -Title "澄清预算与需求门 (S2)"

$runSuffix = (Get-Date).ToString("yyyyMMdd-HHmmss")
$script:S2JobKinds = @{}
$exitCode = 2

try {
    Write-AcceptanceLog -Scenario $scenario -Message "S2 clarification acceptance; maxTurns=$MaxTurns replyTimeout=${ReplyTimeoutSeconds}s"

    $login = Connect-EzTest -Context $context
    Add-AcceptanceCheck -Scenario $scenario -Name "EzTest admin login" -Passed $login.Ok -Severity blocker `
        -Detail ($(if ($login.Ok) { "user=$([string](Get-JsonProp -Object $login.User -Name "username" -Default "?"))" } else { $login.Error }))
    if (-not $login.Ok) {
        $scenario.ErrorMessage = "S2 blocked: EzTest admin login failed"
    } else {
        $bindingResult = $null
        try {
            $bindingResult = Resolve-AcceptanceSutBinding -Context $context
            Add-AcceptanceCheck -Scenario $scenario -Name "SUT bound (SUT/environment/accounts/repository)" -Passed $true -Severity blocker `
                -Detail ("env=$([string](Get-JsonProp -Object $bindingResult.Environment -Name "base_url" -Default "?")) accounts=$(@($bindingResult.Accounts).Count)")
        } catch {
            Add-AcceptanceCheck -Scenario $scenario -Name "SUT bound (SUT/environment/accounts/repository)" -Passed $false -Severity blocker -Detail $_.Exception.Message
            $scenario.ErrorMessage = "S2 blocked: could not prepare the SUT binding: $($_.Exception.Message)"
        }

        if ($null -ne $bindingResult) {
            $version = $null
            try {
                $title = "墨栈博客 0.2.1 验收 · S2 澄清 · $runSuffix"
                $version = New-AcceptanceVersion -Context $context -Title $title -Binding $bindingResult.Binding
                Write-EvidenceJson -Path (Join-Path $scenario.Directory "version.json") -Value $version
                Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "version.json") -Kind "api" -Note "created version"
                Add-AcceptanceCheck -Scenario $scenario -Name "version created" -Passed ($null -ne $version -and [string](Get-JsonProp -Object $version -Name "id" -Default "") -ne "") -Severity assertion `
                    -Detail ("id=" + [string](Get-JsonProp -Object $version -Name "id" -Default ""))
            } catch {
                Add-AcceptanceCheck -Scenario $scenario -Name "version created" -Passed $false -Severity blocker -Detail $_.Exception.Message
                $scenario.ErrorMessage = "S2 blocked: version creation failed: $($_.Exception.Message)"
            }

            if ($null -ne $version) {
                $versionId = [string](Get-JsonProp -Object $version -Name "id" -Default "")
                $channel = "version:$versionId"
                $conversation = $null
                $conversationId = ""
                $initialMessage = $null
                $cursor = 0
                try {
                    $conversation = New-AcceptanceConversation -Context $context -VersionId $versionId -Kind "clarification" -Title "S2 澄清验收"
                    $conversationId = [string](Get-JsonProp -Object $conversation -Name "id" -Default "")
                    Add-AcceptanceCheck -Scenario $scenario -Name "clarification conversation created" -Passed ($conversationId -ne "") -Severity assertion -Detail "conversation=$conversationId"
                } catch {
                    Add-AcceptanceCheck -Scenario $scenario -Name "clarification conversation created" -Passed $false -Severity blocker -Detail $_.Exception.Message
                    $scenario.ErrorMessage = "S2 blocked: conversation creation failed: $($_.Exception.Message)"
                }

                if ($conversationId -ne "") {
                    $initialText = @"
【版本描述】墨栈博客：Vue 前端 + Go 后端博客系统，前端 $($context.SutWebBaseUrl)，后端 API $($context.SutApiBaseUrl)/api/v1。
【版本目标】验收登录与会话、文章列表与详情、文章评论、后台管理四项主流程，并覆盖未登录/越权与文章状态边界。
请开始多轮澄清：每轮只问最关键的少量问题，每个问题给出默认假设；把结论整理成需求草案，最后停在需求确认门，等待用户确认，不要自动开始设计。
"@
                    try {
                        $cursor = Get-EventCursor -Context $context -Channel $channel
                        $initialMessage = New-AcceptanceUserMessage -Context $context -VersionId $versionId -ConversationId $conversationId -Text $initialText
                        Add-AcceptanceCheck -Scenario $scenario -Name "initial clarification message accepted" -Passed ($null -ne $initialMessage -and [string](Get-JsonProp -Object $initialMessage -Name "id" -Default "") -ne "") -Severity assertion `
                            -Detail ("message=" + [string](Get-JsonProp -Object $initialMessage -Name "id" -Default ""))
                    } catch {
                        Add-AcceptanceCheck -Scenario $scenario -Name "initial clarification message accepted" -Passed $false -Severity blocker -Detail $_.Exception.Message
                        $scenario.ErrorMessage = "S2 blocked: initial message could not be posted: $($_.Exception.Message)"
                    }
                }

                if ($null -ne $initialMessage) {
                    $lastUserMessageId = [string](Get-JsonProp -Object $initialMessage -Name "id" -Default "")
                    $answerText = "同意以上默认假设。已确认事实：SUT 前端 $($context.SutWebBaseUrl)，后端 API $($context.SutApiBaseUrl)/api/v1；测试账号为 admin/openClaw/Hermes（密码按验收约定，不在对话中发送）；范围按 P0=登录+文章列表/详情、P1=评论+后台管理；纳入未登录/越权与文章草稿/发布/下线边界。其余细节采用默认假设，不要重复提问。请把结论写入需求草案并给出需求确认请求；在用户确认前不要开始设计。"
                    $assistantRecords = @()
                    $sseRecords = @()
                    $answers = 0
                    $stopReason = ""
                    $replyFailures = 0

                    while ($true) {
                        # Wait for the background jobs this message started, using
                        # SSE so the evidence carries real event ids.
                        $script:S2JobKinds = @{}
                        $sse = $null
                        try {
                            $sse = Wait-SseEvent -Context $context -Channel $channel -After $cursor -TimeoutSeconds $ReplyTimeoutSeconds `
                                -Predicate { param($eventObject) $eventObject.Event -eq "job.finished" } `
                                -StopWhen {
                                    param($eventObject)
                                    $kind = [string](Get-JsonProp -Object (Get-JsonProp -Object $eventObject.Data -Name "payload" -Default $null) -Name "kind" -Default "")
                                    if ($kind -ne "") { $script:S2JobKinds[$kind] = $true }
                                    return ($script:S2JobKinds.ContainsKey("chat.turn") -and $script:S2JobKinds.ContainsKey("requirement.curate"))
                                }
                        } catch {
                            Add-AcceptanceCheck -Scenario $scenario -Name "SSE job events observed" -Passed $false -Severity warning -Detail $_.Exception.Message
                        }
                        if ($null -ne $sse) {
                            foreach ($eventObject in @($sse.Events)) {
                                $sseRecords += [pscustomobject]@{
                                    id = [string]$eventObject.Id
                                    event = [string]$eventObject.Event
                                    seq = [string](Get-JsonProp -Object $eventObject.Data -Name "seq" -Default "")
                                    kind = [string](Get-JsonProp -Object (Get-JsonProp -Object $eventObject.Data -Name "payload" -Default $null) -Name "kind" -Default "")
                                    status = [string](Get-JsonProp -Object (Get-JsonProp -Object $eventObject.Data -Name "payload" -Default $null) -Name "status" -Default "")
                                }
                            }
                            if ($sse.LastEventId -gt $cursor) { $cursor = [long]$sse.LastEventId }
                        }

                        $reply = Wait-NewAssistantText -Context $context -VersionId $versionId -ConversationId $conversationId `
                            -AfterMessageId $lastUserMessageId -TimeoutSeconds $ReplyTimeoutSeconds
                        if (-not $reply.Ok -or $null -eq $reply.Value) {
                            $replyFailures++
                            Add-AcceptanceCheck -Scenario $scenario -Name "assistant replied to user turn $($answers + 1)" -Passed $false -Severity assertion `
                                -Detail "no assistant text within ${ReplyTimeoutSeconds}s; check work items/agent_run errors"
                            break
                        }
                        $assistant = $reply.Value
                        $text = Get-AssistantText -Message $assistant
                        $questions = @(Get-QuestionTexts -Message $assistant)
                        $assistantRecords += [pscustomobject]@{
                            id = [string](Get-JsonProp -Object $assistant -Name "id" -Default "")
                            text = $text
                            questions = $questions
                        }
                        $script:S2JobKinds = @{}

                        # Gate / stop detection after this reply.
                        $stage = $null
                        try { $stage = Get-VersionStage -Context $context -VersionId $versionId } catch { }
                        $currentRequirements = @()
                        try { $currentRequirements = @(Get-VersionRequirements -Context $context -VersionId $versionId) } catch { }
                        $stageValue = [string](Get-JsonProp -Object $stage -Name "stage" -Default "")
                        if ($stageValue -eq "requirement_review") {
                            $stopReason = "requirement_review"
                            break
                        }
                        if ($answers -ge 2 -and $currentRequirements.Count -gt 0 -and (Test-RequirementConfirmationRequest -Text $text)) {
                            $stopReason = "assistant_requested_confirmation"
                            break
                        }
                        if ($answers -ge $MaxTurns) {
                            $stopReason = "max_turns"
                            break
                        }

                        # Answer and continue the multi-turn clarification.
                        $answers++
                        try {
                            $answer = New-AcceptanceUserMessage -Context $context -VersionId $versionId -ConversationId $conversationId -Text $answerText
                            $lastUserMessageId = [string](Get-JsonProp -Object $answer -Name "id" -Default "")
                        } catch {
                            Add-AcceptanceCheck -Scenario $scenario -Name "clarification answer $answers accepted" -Passed $false -Severity assertion -Detail $_.Exception.Message
                            break
                        }
                    }

                    # ------------------------------------------------------------------
                    # Evaluate the four S2 criteria from the final state.
                    # ------------------------------------------------------------------
                    $messages = @()
                    try { $messages = @(Get-ConversationMessages -Context $context -VersionId $versionId -ConversationId $conversationId) } catch { }
                    $messageSummary = @($messages | ForEach-Object {
                        $text = Get-AssistantText -Message $_
                        [pscustomobject]@{
                            id = [string](Get-JsonProp -Object $_ -Name "id" -Default "")
                            role = [string](Get-JsonProp -Object $_ -Name "role" -Default "")
                            text = $text
                            questions = @(Get-QuestionTexts -Message $_)
                        }
                    })
                    Write-EvidenceJson -Path (Join-Path $scenario.Directory "messages.json") -Value $messageSummary
                    Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "messages.json") -Kind "api" -Note "conversation messages (text only)"

                    $allQuestions = @()
                    $turnQuestionCounts = @()
                    foreach ($record in $assistantRecords) {
                        $turnQuestions = @($record.questions)
                        $turnQuestionCounts += [pscustomobject]@{ message_id = $record.id; questions = $turnQuestions.Count }
                        $allQuestions += $turnQuestions
                    }
                    $normalized = @()
                    foreach ($question in $allQuestions) { $normalized += (Get-NormalizedQuestion -Question $question) }
                    $uniqueNormalized = @(Select-NormalizedUnique -Items $normalized)
                    $duplicates = @()
                    $seen = [System.Collections.Generic.HashSet[string]]::new()
                    foreach ($item in $normalized) {
                        if (-not $seen.Add($item)) { $duplicates += $item }
                    }

                    $requirements = @()
                    try { $requirements = @(Get-VersionRequirements -Context $context -VersionId $versionId) } catch { }
                    $stage = $null
                    try { $stage = Get-VersionStage -Context $context -VersionId $versionId } catch { }
                    $currentVersion = $null
                    try { $currentVersion = Get-Version -Context $context -VersionId $versionId } catch { }
                    $workItems = @()
                    try { $workItems = @(Get-VersionWorkItems -Context $context -VersionId $versionId) } catch { }
                    $conversations = @()
                    try { $conversations = @(Get-VersionConversations -Context $context -VersionId $versionId) } catch { }

                    $stageValue = [string](Get-JsonProp -Object $stage -Name "stage" -Default "")
                    $requirementCount = $requirements.Count
                    $budget = $QuestionBudgetPerRequirement * [Math]::Max(1, $requirementCount)
                    $draftRequirements = @($requirements | Where-Object { [string](Get-JsonProp -Object $_ -Name "status" -Default "") -eq "draft" })
                    $nonDraftRequirements = @($requirements | Where-Object { [string](Get-JsonProp -Object $_ -Name "status" -Default "") -ne "draft" })
                    $caseCounts = Get-JsonProp -Object $currentVersion -Name "case_counts" -Default $null
                    $caseTotal = 0
                    if ($null -ne $caseCounts) {
                        $caseTotal = [int](Get-JsonProp -Object $caseCounts -Name "draft" -Default 0) +
                            [int](Get-JsonProp -Object $caseCounts -Name "confirmed" -Default 0) +
                            [int](Get-JsonProp -Object $caseCounts -Name "stale" -Default 0)
                    }
                    $designJobs = @($workItems | Where-Object {
                        $kind = [string](Get-JsonProp -Object $_ -Name "kind" -Default "")
                        $type = [string](Get-JsonProp -Object $_ -Name "type" -Default "")
                        ($kind -eq "design.cases" -or $kind -eq "execute.batch" -or $type -eq "design.cases" -or $type -eq "execute.batch")
                    })
                    $designConversations = @($conversations | Where-Object { [string](Get-JsonProp -Object $_ -Name "kind" -Default "") -eq "design" })
                    $confirmationAsked = $false
                    foreach ($record in $assistantRecords) {
                        if (Test-RequirementConfirmationRequest -Text $record.text) { $confirmationAsked = $true; break }
                    }

                    Write-EvidenceJson -Path (Join-Path $scenario.Directory "requirements.json") -Value $requirements
                    Write-EvidenceJson -Path (Join-Path $scenario.Directory "stage.json") -Value $stage
                    Write-EvidenceJson -Path (Join-Path $scenario.Directory "work-items.json") -Value $workItems
                    Write-EvidenceJson -Path (Join-Path $scenario.Directory "conversations.json") -Value $conversations
                    Write-EvidenceJson -Path (Join-Path $scenario.Directory "sse-events.json") -Value $sseRecords
                    Write-EvidenceJson -Path (Join-Path $scenario.Directory "questions.json") -Value ([pscustomobject]@{
                        total = $allQuestions.Count
                        unique = $uniqueNormalized.Count
                        duplicates = $duplicates
                        per_turn = $turnQuestionCounts
                        requirement_count = $requirementCount
                        budget = $budget
                        stop_reason = $stopReason
                    })
                    Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "requirements.json") -Kind "api" -Note "requirement drafts at the gate"
                    Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "stage.json") -Kind "api" -Note "final stage"
                    Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "sse-events.json") -Kind "sse" -Note "job.finished SSE event ids"
                    Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "questions.json") -Kind "api" -Note "question budget and duplicates"

                    Add-AcceptanceCheck -Scenario $scenario -Name "clarification questions asked" -Passed ($uniqueNormalized.Count -gt 0) -Severity assertion `
                        -Detail "uniqueQuestions=$($uniqueNormalized.Count) total=$($allQuestions.Count) turns=$($assistantRecords.Count)"
                    Add-AcceptanceCheck -Scenario $scenario -Name "question budget per requirement (<= $QuestionBudgetPerRequirement)" -Passed ($uniqueNormalized.Count -le $budget) -Severity assertion `
                        -Detail "uniqueQuestions=$($uniqueNormalized.Count) requirements=$requirementCount budget=$budget"
                    Add-AcceptanceCheck -Scenario $scenario -Name "version-wide question budget (<= $VersionWideQuestionBudget)" -Passed ($uniqueNormalized.Count -le $VersionWideQuestionBudget) -Severity assertion `
                        -Detail "uniqueQuestions=$($uniqueNormalized.Count) versionBudget=$VersionWideQuestionBudget"
                    Add-AcceptanceCheck -Scenario $scenario -Name "no duplicate clarification questions" -Passed ($duplicates.Count -eq 0) -Severity assertion `
                        -Detail ("duplicates=" + $(if ($duplicates.Count -gt 0) { ($duplicates -join " | ") } else { "none" }))
                    Add-AcceptanceCheck -Scenario $scenario -Name "at most 3 questions per assistant turn" -Passed (-not (@($turnQuestionCounts | Where-Object { $_.questions -gt 3 }).Count -gt 0)) -Severity warning `
                        -Detail (($turnQuestionCounts | ForEach-Object { "$($_.message_id)=$($_.questions)" }) -join ", ")

                    Add-AcceptanceCheck -Scenario $scenario -Name "requirement drafts produced" -Passed ($requirements.Count -gt 0) -Severity assertion `
                        -Detail "requirements=$($requirements.Count) drafts=$($draftRequirements.Count)"
                    Add-AcceptanceCheck -Scenario $scenario -Name "all requirements remain draft at the gate" -Passed ($requirements.Count -gt 0 -and $nonDraftRequirements.Count -eq 0) -Severity assertion `
                        -Detail ("draft=$($draftRequirements.Count) nonDraft=$($nonDraftRequirements.Count)")
                    Add-AcceptanceCheck -Scenario $scenario -Name "stopped at the requirement confirmation gate" -Passed ($stageValue -eq "requirement_review") -Severity assertion `
                        -Detail "stage=$stageValue (expected requirement_review; stopReason=$stopReason)"
                    Add-AcceptanceCheck -Scenario $scenario -Name "no automatic design or execution" -Passed ($stageValue -notin @("designing", "case_review", "executing", "reporting", "completed")) -Severity assertion `
                        -Detail "stage=$stageValue"
                    Add-AcceptanceCheck -Scenario $scenario -Name "no cases generated before the case gate" -Passed ($caseTotal -eq 0) -Severity assertion -Detail "case_total=$caseTotal"
                    Add-AcceptanceCheck -Scenario $scenario -Name "no design/execute jobs before the case gate" -Passed ($designJobs.Count -eq 0) -Severity assertion `
                        -Detail ("designJobs=$($designJobs.Count) totalWorkItems=$($workItems.Count)")
                    Add-AcceptanceCheck -Scenario $scenario -Name "no design conversation created" -Passed ($designConversations.Count -eq 0) -Severity assertion -Detail "designConversations=$($designConversations.Count)"
                    Add-AcceptanceCheck -Scenario $scenario -Name "assistant requested requirement confirmation" -Passed $confirmationAsked -Severity warning -Detail "confirmationRequested=$confirmationAsked"

                    if ($stageValue -ne "requirement_review" -or $requirements.Count -eq 0 -or $duplicates.Count -gt 0 -or $uniqueNormalized.Count -gt $budget) {
                        $scenario.ErrorMessage = "S2 failed: stage=$stageValue requirements=$($requirements.Count) uniqueQuestions=$($uniqueNormalized.Count) budget=$budget duplicates=$($duplicates.Count)"
                    }

                    try {
                        $shot = Save-AcceptanceScreenshot -Context $context -Scenario $scenario -Route "/versions/${versionId}?tab=requirements" -FileName "s2-requirements-gate.png" `
                            -WaitForSelector '[data-testid="version-header"]' -Note "requirements + clarification gate"
                        Add-AcceptanceCheck -Scenario $scenario -Name "requirements gate screenshot" -Passed $true -Severity warning -Detail (Resolve-RepoRelativePath -Context $context -Path $shot.Path)
                    } catch {
                        Add-AcceptanceCheck -Scenario $scenario -Name "requirements gate screenshot" -Passed $false -Severity warning -Detail $_.Exception.Message
                    }
                }
            }
        }
    }
} catch {
    $scenario.ErrorMessage = $_.Exception.Message
    Add-AcceptanceCheck -Scenario $scenario -Name "S2 unexpected error" -Passed $false -Severity blocker -Detail ($_.Exception.Message + " | " + $_.ScriptStackTrace)
} finally {
    $exitCode = Complete-AcceptanceScenario -Scenario $scenario -Message "S2 clarification budget and requirement gate"
}

exit $exitCode
