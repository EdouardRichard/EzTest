#requires -Version 5.1
<#
.SYNOPSIS
    S3 design acceptance for EzTest 0.2.1.

.DESCRIPTION
    Drives a fresh real-LLM version from requirements to design and asserts the
    S3 criteria:

      1. clarification converges at the requirement gate and the real user hard
         gate POST /stage/confirm-requirements confirms the drafts and advances
         requirement_review -> designing;
      2. the wrong gate (POST /stage/confirm-cases) is rejected with the typed
         409 invalid_stage_for_gate at requirement_review and moves nothing;
      3. a design conversation appears automatically after that gate (the script
         never creates a design conversation, never PATCHes the legacy
         auto_design_new_requirements toggle and never POSTs a legacy
         requirement confirmation as a fallback);
      4. job progress is visible in that conversation as job_card parts and
         design work items;
      5. the hard gate enqueued a design.cases stage-entry work item, and case
         nodes appear in the design graph/mindmap automatically (cases > 0);
      6. a case detail exposes executable steps, assertions (success criteria)
         and evidence sources.

    There is deliberately no downgrade path: when the version is not at the
    hard-gate source stage, the script records the typed 409 and fails instead
    of switching to the 0.2 requirement-confirmation API. The script writes no
    application data through SQL (the only DB use is the read-only stage-entry
    work-item projection). Case confirmation belongs to S4, which calls the
    case hard gate.

.PARAMETER VersionId
    Reuse an existing version instead of creating one.

.PARAMETER ClarifyTimeoutSeconds
    How long to wait for the LLM to write requirement drafts (default 180).

.PARAMETER DesignTimeoutSeconds
    How long to wait for the automatic design conversation (default 300).

.PARAMETER CaseTimeoutSeconds
    How long to wait for case nodes to appear (default 240).

.NOTES
    Exit codes: 0 = passed, 1 = failed acceptance assertion, 2 = blocked
    environment. Requirements: S0 stack + real LLM + executor; S3 itself
    creates the version when -VersionId is omitted.
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
    [string] $VersionTitle = "",
    [string] $StatePath = "",
    [int] $ClarifyTimeoutSeconds = 180,
    [int] $DesignTimeoutSeconds = 300,
    [int] $CaseTimeoutSeconds = 240
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
$scenario = New-AcceptanceScenario -Context $context -Name "s3" -Title "设计自动生成与脑图 (S3)"
$statePath = Get-AcceptanceStatePath -Context $context -StatePath $StatePath

$runSuffix = (Get-Date).ToString("yyyyMMdd-HHmmss")
$version = $null
$versionId = ([string]$VersionId).Trim()
$isReusedVersion = -not [string]::IsNullOrWhiteSpace($versionId)
$exitCode = 2
$requirementsBefore = @()
$requirementIds = @()
$caseCards = @()
$designConversation = $null
$stageGateError = ""
$requirementGatePassed = $false
$stageAtEndValue = ""

function Save-S3State {
    param([hashtable] $Values)
    if ([string]::IsNullOrWhiteSpace($versionId)) { return }
    $base = @{
        version_id     = $versionId
        s3_run_suffix  = $runSuffix
        s3_evidence    = Resolve-RepoRelativePath -Context $context -Path $scenario.Directory
    }
    foreach ($key in $Values.Keys) { $base[$key] = $Values[$key] }
    [void](Update-AcceptanceState -Context $context -Path $statePath -Values $base)
}

function Write-S3Evidence {
    param([string] $Name, $Value, [string] $Kind = "api", [string] $Note = "")
    $path = Join-Path $scenario.Directory $Name
    Write-EvidenceJson -Path $path -Value $Value
    Add-AcceptanceArtifact -Scenario $scenario -Path $path -Kind $Kind -Note $Note
    return $path
}

try {
    Write-AcceptanceLog -Scenario $scenario -Message "S3 design acceptance; clarifyTimeout=${ClarifyTimeoutSeconds}s designTimeout=${DesignTimeoutSeconds}s"

    $login = Connect-EzTest -Context $context
    Add-AcceptanceCheck -Scenario $scenario -Name "EzTest admin login" -Passed $login.Ok -Severity blocker `
        -Detail ($(if ($login.Ok) { "user=" + [string](Get-JsonProp -Object $login.User -Name 'username' -Default '?') } else { $login.Error }))
    if (-not $login.Ok) {
        $scenario.ErrorMessage = "S3 blocked: EzTest admin login failed"
    } else {
        # ------------------------------------------------------------------
        # Resolve or create the version.
        # ------------------------------------------------------------------
        if ($isReusedVersion) {
            $versionId = $VersionId.Trim()
            try {
                $version = Get-Version -Context $context -VersionId $versionId
                Add-AcceptanceCheck -Scenario $scenario -Name "version resolved" -Passed ($null -ne $version) -Severity blocker `
                    -Detail "reused version=$versionId"
            } catch {
                Add-AcceptanceCheck -Scenario $scenario -Name "version resolved" -Passed $false -Severity blocker -Detail $_.Exception.Message
                $scenario.ErrorMessage = "S3 blocked: version $versionId is not readable"
            }
        } else {
            $binding = $null
            try {
                $binding = Resolve-AcceptanceSutBinding -Context $context
                Add-AcceptanceCheck -Scenario $scenario -Name "SUT bound (SUT/environment/accounts/repository)" -Passed $true -Severity blocker `
                    -Detail ("env=" + [string](Get-JsonProp -Object $binding.Environment -Name 'base_url' -Default '?') + " accounts=$(@($binding.Accounts).Count) repo=" + [string](Get-JsonProp -Object $binding.Repository -Name 'path' -Default '?'))
            } catch {
                Add-AcceptanceCheck -Scenario $scenario -Name "SUT bound (SUT/environment/accounts/repository)" -Passed $false -Severity blocker -Detail $_.Exception.Message
                $scenario.ErrorMessage = "S3 blocked: SUT binding failed: $($_.Exception.Message)"
            }
            if ($null -ne $binding) {
                $title = if ([string]::IsNullOrWhiteSpace($VersionTitle)) { "墨栈博客 0.2.1 验收 · S3 设计 · $runSuffix" } else { $VersionTitle }
                try {
                    $version = New-AcceptanceVersion -Context $context -Title $title -Binding $binding.Binding
                    $versionId = [string](Get-JsonProp -Object $version -Name "id" -Default "")
                    Add-AcceptanceCheck -Scenario $scenario -Name "version created" -Passed ($versionId -ne "") -Severity blocker `
                        -Detail ("id=$versionId title=" + [string](Get-JsonProp -Object $version -Name 'title' -Default ''))
                } catch {
                    Add-AcceptanceCheck -Scenario $scenario -Name "version created" -Passed $false -Severity blocker -Detail $_.Exception.Message
                    $scenario.ErrorMessage = "S3 blocked: version creation failed: $($_.Exception.Message)"
                }
            }
        }

        if ($null -ne $version -and $versionId -ne "") {
            Save-S3State -Values @{ version_id = $versionId }
            Write-S3Evidence -Name "version.json" -Value $version -Note "version used by S3"

            # ------------------------------------------------------------------
            # Clarification: create the conversation and request a compact
            # requirement draft. The design conversation must NOT be created by
            # this script later.
            # ------------------------------------------------------------------
            $conversationId = ""
            $requirements = @()
            if (-not $isReusedVersion) {
                try {
                    $conversation = New-AcceptanceConversation -Context $context -VersionId $versionId -Kind "clarification" -Title "S3 需求澄清"
                    $conversationId = [string](Get-JsonProp -Object $conversation -Name "id" -Default "")
                    Add-AcceptanceCheck -Scenario $scenario -Name "clarification conversation created" -Passed ($conversationId -ne "") -Severity assertion -Detail "conversation=$conversationId"
                } catch {
                    Add-AcceptanceCheck -Scenario $scenario -Name "clarification conversation created" -Passed $false -Severity blocker -Detail $_.Exception.Message
                    $scenario.ErrorMessage = "S3 blocked: clarification conversation failed: $($_.Exception.Message)"
                }
            } else {
                $existing = @(Get-VersionConversations -Context $context -VersionId $versionId -Limit 100)
                $clarification = $existing | Where-Object { [string](Get-JsonProp -Object $_ -Name "kind" -Default "") -eq "clarification" } | Select-Object -First 1
                if ($null -ne $clarification) { $conversationId = [string](Get-JsonProp -Object $clarification -Name "id" -Default "") }
                Add-AcceptanceCheck -Scenario $scenario -Name "clarification conversation available" -Passed ($conversationId -ne "") -Severity warning -Detail "conversation=$conversationId"
            }

            if ($conversationId -ne "") {
                $goal = @"
【版本描述】墨栈博客：Vue 前端 + Go 后端博客系统，SUT 前端 $($context.SutWebBaseUrl)，后端 API $($context.SutApiBaseUrl)/api/v1，测试账号 admin/openClaw/Hermes。
【版本目标】验收登录与会话、文章列表与详情、文章评论、后台管理四项主流程，并覆盖未登录/越权与文章状态边界。
【本轮要求】请在澄清中直接整理并写入一份精简需求草案（4 条左右，覆盖上述主流程），不要反复提问；整理完停在需求确认门，等待用户确认，不要自动开始设计。
"@
                try {
                    $message = New-AcceptanceUserMessage -Context $context -VersionId $versionId -ConversationId $conversationId -Text $goal
                    Add-AcceptanceCheck -Scenario $scenario -Name "version description and goal accepted" -Passed ($null -ne $message) -Severity assertion `
                        -Detail ("message=" + [string](Get-JsonProp -Object $message -Name "id" -Default ""))
                } catch {
                    Add-AcceptanceCheck -Scenario $scenario -Name "version description and goal accepted" -Passed $false -Severity assertion -Detail $_.Exception.Message
                }
            }

            if ($conversationId -ne "") {
                $draftWait = Wait-Until -TimeoutSeconds $ClarifyTimeoutSeconds -IntervalSeconds 5 -Description "requirement drafts" -Condition {
                    $current = @(Get-VersionRequirements -Context $context -VersionId $versionId -Limit 200)
                    $drafts = @($current | Where-Object { [string](Get-JsonProp -Object $_ -Name "status" -Default "") -notin @("obsolete", "closed") })
                    if ($drafts.Count -ge 2) { return $drafts }
                    return $null
                }
                if (-not $draftWait.Ok) {
                    # One follow-up nudge: the model sometimes asks questions
                    # first instead of writing the draft.
                    try {
                        $nudge = New-AcceptanceUserMessage -Context $context -VersionId $versionId -ConversationId $conversationId `
                            -Text "请现在把需求草案写入需求列表（4 条左右，覆盖登录、文章、评论、后台管理），不需要继续提问；写完后停在需求确认门等待用户确认。"
                        Write-AcceptanceLog -Scenario $scenario -Message ("posted requirement-draft nudge message=" + [string](Get-JsonProp -Object $nudge -Name 'id' -Default ''))
                    } catch {
                        Write-AcceptanceLog -Scenario $scenario -Level warn -Message "nudge failed: $($_.Exception.Message)"
                    }
                    $draftWait = Wait-Until -TimeoutSeconds ([Math]::Max(60, [int]($ClarifyTimeoutSeconds / 2))) -IntervalSeconds 5 -Description "requirement drafts after nudge" -Condition {
                        $current = @(Get-VersionRequirements -Context $context -VersionId $versionId -Limit 200)
                        $drafts = @($current | Where-Object { [string](Get-JsonProp -Object $_ -Name "status" -Default "") -notin @("obsolete", "closed") })
                        if ($drafts.Count -ge 1) { return $drafts }
                        return $null
                    }
                }
                $requirements = @()
                if ($draftWait.Ok) { $requirements = @($draftWait.Value) }
                else { $requirements = @(Get-VersionRequirements -Context $context -VersionId $versionId -Limit 200) }
                $requirementsBefore = @($requirements)
                $requirementIds = @($requirements | ForEach-Object { [string](Get-JsonProp -Object $_ -Name "id" -Default "") } | Where-Object { $_ -ne "" })
                [void](Write-S3Evidence -Name "requirements-before.json" -Value $requirements -Note "requirement drafts before confirmation")
                Add-AcceptanceCheck -Scenario $scenario -Name "requirement drafts produced" -Passed ($requirementIds.Count -ge 1) -Severity assertion `
                    -Detail ("requirements=$($requirementIds.Count) statuses=" + ((@($requirements | ForEach-Object { [string](Get-JsonProp -Object $_ -Name 'status' -Default '') }) -join ",")))
                if ($requirementIds.Count -ge 2) {
                    Add-AcceptanceCheck -Scenario $scenario -Name "multiple requirements for design" -Passed $true -Severity warning -Detail "requirements=$($requirementIds.Count)"
                } else {
                    Add-AcceptanceCheck -Scenario $scenario -Name "multiple requirements for design" -Passed $false -Severity warning -Detail "requirements=$($requirementIds.Count)"
                }
                Save-S3State -Values @{ clarification_conversation_id = $conversationId; requirement_ids = $requirementIds }
            }

            if ($requirementIds.Count -eq 0) {
                Add-AcceptanceCheck -Scenario $scenario -Name "requirements available for design" -Passed $false -Severity assertion `
                    -Detail "no requirement draft was produced; the design chain cannot be exercised"
                $scenario.ErrorMessage = "S3 failed: no requirement draft was produced; design cannot be exercised"
            } else {
                # ------------------------------------------------------------------
                # The 0.2.1 hard gates. The version must sit at requirement_review
                # before the user confirmation can advance it. There is no
                # legacy requirement-confirmation / auto-design fallback.
                # ------------------------------------------------------------------
                $autoDesignView = $null
                try { $autoDesignView = Get-Version -Context $context -VersionId $versionId } catch { }
                $autoDesignEnabled = [bool](Get-JsonProp -Object $autoDesignView -Name "auto_design_new_requirements" -Default $false)
                Add-AcceptanceCheck -Scenario $scenario -Name "legacy auto-design toggle is not enabled" -Passed ($null -ne $autoDesignView -and -not $autoDesignEnabled) -Severity assertion `
                    -Detail "auto_design_new_requirements=$autoDesignEnabled; the script never PATCHes the legacy toggle"

                # Clarification convergence is owned by the product: the runner
                # advances clarifying -> requirement_review when the
                # clarification host's turn explicitly asks the user to confirm
                # the drafts. The script only asks for that declaration; it
                # never writes the stage.
                $stageWait = Wait-Until -TimeoutSeconds ([Math]::Min(60, $ClarifyTimeoutSeconds)) -IntervalSeconds 3 -Description "requirement_review stage" -Condition {
                    try { $current = Get-VersionStage -Context $context -VersionId $versionId } catch { return $null }
                    if ([string](Get-JsonProp -Object $current -Name "stage" -Default "") -eq "requirement_review") { return $current }
                    return $null
                }
                if (-not $stageWait.Ok -and $conversationId -ne "") {
                    try {
                        $convergeNudge = New-AcceptanceUserMessage -Context $context -VersionId $versionId -ConversationId $conversationId `
                            -Text "请现在把需求草案写入需求列表（4 条左右，覆盖登录、文章、评论、后台管理），并在回复末尾明确给出『需求确认请求』；随后停在需求确认门等待用户确认，不要开始设计。"
                        Write-AcceptanceLog -Scenario $scenario -Message ("posted requirement-confirmation nudge message=" + [string](Get-JsonProp -Object $convergeNudge -Name 'id' -Default ''))
                    } catch {
                        Write-AcceptanceLog -Scenario $scenario -Level warn -Message "confirmation nudge failed: $($_.Exception.Message)"
                    }
                    $stageWait = Wait-Until -TimeoutSeconds ([Math]::Max(90, [int]($ClarifyTimeoutSeconds / 2))) -IntervalSeconds 3 -Description "requirement_review stage after nudge" -Condition {
                        try { $current = Get-VersionStage -Context $context -VersionId $versionId } catch { return $null }
                        if ([string](Get-JsonProp -Object $current -Name "stage" -Default "") -eq "requirement_review") { return $current }
                        return $null
                    }
                }
                $stageBefore = $null
                if ($stageWait.Ok) { $stageBefore = $stageWait.Value }
                else { try { $stageBefore = Get-VersionStage -Context $context -VersionId $versionId } catch { } }
                [void](Write-S3Evidence -Name "stage-before.json" -Value $stageBefore -Note "stage at the requirement hard gate")
                $stageValue = [string](Get-JsonProp -Object $stageBefore -Name "stage" -Default "")
                Add-AcceptanceCheck -Scenario $scenario -Name "clarification converged at the requirement gate" -Passed ($stageValue -eq "requirement_review") -Severity assertion `
                    -Detail ("stage=$stageValue; expected requirement_review (the product owns the clarifying->requirement_review edge; the script only asks for the confirmation request)")

                $requirementsAtGate = @(Get-VersionRequirements -Context $context -VersionId $versionId -Limit 200)
                $draftAtGate = @($requirementsAtGate | Where-Object { [string](Get-JsonProp -Object $_ -Name "status" -Default "") -eq "draft" })
                $gateRequirementIds = @($draftAtGate | ForEach-Object { [string](Get-JsonProp -Object $_ -Name "id" -Default "") } | Where-Object { $_ -ne "" })
                Add-AcceptanceCheck -Scenario $scenario -Name "requirements are draft at the hard gate" -Passed ($stageValue -eq "requirement_review" -and $gateRequirementIds.Count -ge 1) -Severity assertion `
                    -Detail ("requirements=$($requirementsAtGate.Count) draft=$($draftAtGate.Count)")

                if ($stageValue -eq "requirement_review" -and $gateRequirementIds.Count -ge 1) {
                    # Negative assertion: the case gate must be rejected at the
                    # requirement gate because its only source stage is
                    # case_review. The rejection must not move the stage.
                    $wrongGate = Invoke-Api -Context $context -Method "POST" -Path "/api/versions/$versionId/stage/confirm-cases" `
                        -Token $context.Token -Body @{ case_ids = @() }
                    $wrongGateCode = [string](Get-JsonProp -Object $wrongGate.Error -Name "code" -Default "")
                    $stageAfterWrongGate = $null
                    try { $stageAfterWrongGate = Get-VersionStage -Context $context -VersionId $versionId } catch { }
                    $stageAfterWrongGateValue = [string](Get-JsonProp -Object $stageAfterWrongGate -Name "stage" -Default "")
                    [void](Write-S3Evidence -Name "wrong-gate-case-at-requirement-review.json" -Value ([pscustomobject]@{
                        http_status = $wrongGate.Status
                        ok          = $wrongGate.Ok
                        error_code  = $wrongGateCode
                        error       = $wrongGate.Error
                        stage_before = $stageValue
                        stage_after  = $stageAfterWrongGateValue
                    }) -Note "case hard gate called at requirement_review (must be rejected)")
                    Add-AcceptanceCheck -Scenario $scenario -Name "invalid_stage_for_gate rejected the case gate at requirement_review" `
                        -Passed ($wrongGate.Status -eq 409 -and $wrongGateCode -eq "invalid_stage_for_gate" -and $stageAfterWrongGateValue -eq "requirement_review") `
                        -Severity assertion `
                        -Detail ("HTTP $($wrongGate.Status) code=$wrongGateCode stage=$stageValue->$stageAfterWrongGateValue")

                    $conversationsBeforeGate = @(Get-VersionConversations -Context $context -VersionId $versionId -Limit 100)
                    $designBeforeGate = @($conversationsBeforeGate | Where-Object { [string](Get-JsonProp -Object $_ -Name "kind" -Default "") -eq "design" })
                    Add-AcceptanceCheck -Scenario $scenario -Name "no design conversation before the requirement hard gate" -Passed ($designBeforeGate.Count -eq 0) -Severity warning `
                        -Detail "designConversationsBeforeGate=$($designBeforeGate.Count)"

                    # Positive assertion: the real 0.2.1 requirement gate
                    # confirms the drafts and advances to designing.
                    $gate = Invoke-Api -Context $context -Method "POST" -Path "/api/versions/$versionId/stage/confirm-requirements" `
                        -Token $context.Token -Body @{ requirement_ids = $gateRequirementIds }
                    $gateCode = [string](Get-JsonProp -Object $gate.Error -Name "code" -Default "")
                    $gateStage = [string](Get-JsonProp -Object $gate.Data -Name "stage" -Default "")
                    $stageAfterGate = $null
                    try { $stageAfterGate = Get-VersionStage -Context $context -VersionId $versionId } catch { }
                    $stageAfterGateValue = [string](Get-JsonProp -Object $stageAfterGate -Name "stage" -Default "")
                    [void](Write-S3Evidence -Name "requirement-gate.json" -Value ([pscustomobject]@{
                        http_status  = $gate.Status
                        ok           = $gate.Ok
                        response     = $gate.Data
                        error_code   = $gateCode
                        error        = $gate.Error
                        stage_before = $stageValue
                        stage_after  = $stageAfterGateValue
                    }) -Note "0.2.1 requirement hard gate")
                    [void](Write-S3Evidence -Name "stage-after-gate.json" -Value $stageAfterGate -Note "stage after the requirement hard gate")
                    $requirementGatePassed = $gate.Ok -and $gateStage -eq "designing" -and $stageAfterGateValue -eq "designing"
                    Add-AcceptanceCheck -Scenario $scenario -Name "0.2.1 requirement hard gate advanced requirement_review to designing" `
                        -Passed $requirementGatePassed -Severity assertion `
                        -Detail ("HTTP $($gate.Status) code=$gateCode responseStage=$gateStage readbackStage=$stageAfterGateValue")
                    if (-not $requirementGatePassed) {
                        $stageGateError = "HTTP $($gate.Status) $gateCode"
                        $scenario.ErrorMessage = "S3 failed: the 0.2.1 requirement hard gate did not advance the version (HTTP $($gate.Status) $gateCode); no fallback is used"
                    }

                    # The stage machine must have dispatched the design.cases
                    # stage-entry work item for the confirmed scope.
                    $designCasesDb = Invoke-AcceptanceDbQuery -Context $context -VersionId ([long]$versionId) `
                        -Sql "SELECT id, team, type, kind, status, attempts, max_attempts, created_by, inputs::text FROM work_item WHERE version_id = $versionId AND kind = 'design.cases' ORDER BY id"
                    $designCasesRows = @($designCasesDb.Rows)
                    [void](Write-S3Evidence -Name "design-cases-job-db.json" -Value ([pscustomobject]@{
                        database = $designCasesDb.Database
                        ok       = $designCasesDb.Ok
                        error    = $designCasesDb.Error
                        rows     = $designCasesRows
                    }) -Kind "db" -Note "design.cases stage-entry work item (read-only DB projection)")
                    Add-AcceptanceCheck -Scenario $scenario -Name "requirement hard gate enqueued the design.cases stage-entry job" `
                        -Passed ($designCasesDb.Ok -and $designCasesRows.Count -ge 1) -Severity assertion `
                        -Detail ("database=$($designCasesDb.Database) rows=$($designCasesRows.Count) error=$($designCasesDb.Error)")
                } else {
                    # The version never reached its hard-gate source stage. The
                    # gate is still called once to record the exact typed
                    # rejection, and the scenario fails: it never falls back to
                    # the legacy 0.2 requirement-confirmation API.
                    $gate = Invoke-Api -Context $context -Method "POST" -Path "/api/versions/$versionId/stage/confirm-requirements" `
                        -Token $context.Token -Body @{ requirement_ids = $requirementIds }
                    $gateCode = [string](Get-JsonProp -Object $gate.Error -Name "code" -Default "")
                    [void](Write-S3Evidence -Name "requirement-gate.json" -Value ([pscustomobject]@{
                        http_status  = $gate.Status
                        ok           = $gate.Ok
                        response     = $gate.Data
                        error_code   = $gateCode
                        error        = $gate.Error
                        stage_before = $stageValue
                    }) -Note "requirement hard gate attempted at the wrong stage")
                    Add-AcceptanceCheck -Scenario $scenario -Name "0.2.1 requirement hard gate reachable" -Passed $false -Severity assertion `
                        -Detail ("stage=$stageValue HTTP $($gate.Status) code=$gateCode; the version must reach requirement_review before the gate; no fallback is used")
                    $stageGateError = "HTTP $($gate.Status) $gateCode"
                    $scenario.ErrorMessage = "S3 failed: version stage=$stageValue, requirement gate requires requirement_review (HTTP $($gate.Status) $gateCode); no fallback is used"
                }

                if ($requirementGatePassed) {
                # Requirements must leave draft before design can start.
                $clarifiedWait = Wait-Until -TimeoutSeconds 90 -IntervalSeconds 3 -Description "requirements non-draft" -Condition {
                    $current = @(Get-VersionRequirements -Context $context -VersionId $versionId -Limit 200)
                    $draft = @($current | Where-Object { [string](Get-JsonProp -Object $_ -Name "status" -Default "") -eq "draft" })
                    if ($draft.Count -eq 0 -and $current.Count -gt 0) { return $current }
                    return $null
                }
                $requirementsAfter = @()
                if ($clarifiedWait.Ok) { $requirementsAfter = @($clarifiedWait.Value) } else { $requirementsAfter = @(Get-VersionRequirements -Context $context -VersionId $versionId -Limit 200) }
                $statuses = @($requirementsAfter | ForEach-Object { [string](Get-JsonProp -Object $_ -Name "status" -Default "") })
                [void](Write-S3Evidence -Name "requirements-after.json" -Value $requirementsAfter -Note "requirements after confirmation")
                Add-AcceptanceCheck -Scenario $scenario -Name "requirements leave draft after confirmation" -Passed ($requirementsAfter.Count -gt 0 -and @($statuses | Where-Object { $_ -eq "draft" }).Count -eq 0) -Severity assertion `
                    -Detail "statuses=$($statuses -join ",")"

                # ------------------------------------------------------------------
                # The S3 criterion: a design conversation and case nodes appear
                # without the user creating a design chat. The design work is
                # driven by the design.cases stage-entry job the hard gate
                # dispatched above; the script only reads.
                # ------------------------------------------------------------------
                $designWait = Wait-Until -TimeoutSeconds $DesignTimeoutSeconds -IntervalSeconds 5 -Description "auto design conversation" -Condition {
                    $conversation = Get-AcceptanceDesignConversation -Context $context -VersionId $versionId
                    if ($null -ne $conversation) { return $conversation }
                    return $null
                }
                if ($designWait.Ok) { $designConversation = $designWait.Value }
                $designConversationId = ""
                if ($null -ne $designConversation) { $designConversationId = [string](Get-JsonProp -Object $designConversation -Name "id" -Default "") }
                Add-AcceptanceCheck -Scenario $scenario -Name "design conversation created automatically" -Passed ($designConversationId -ne "") -Severity assertion `
                    -Detail ("conversation=$designConversationId; no design conversation was created by the script")
                Save-S3State -Values @{ design_conversation_id = $designConversationId }

                $caseWait = Wait-Until -TimeoutSeconds $CaseTimeoutSeconds -IntervalSeconds 5 -Description "design cases" -Condition {
                    $graph = Get-AcceptanceDesignGraph -Context $context -VersionId $versionId
                    $cards = @(Get-AcceptanceCaseCards -Graph $graph)
                    if ($cards.Count -ge 1) { return [pscustomobject]@{ Graph = $graph; Cards = $cards } }
                    return $null
                }
                $graph = $null
                if ($caseWait.Ok -and $null -ne $caseWait.Value) {
                    $graph = $caseWait.Value.Graph
                    $caseCards = @($caseWait.Value.Cards)
                } else {
                    $graph = Get-AcceptanceDesignGraph -Context $context -VersionId $versionId
                    $caseCards = @(Get-AcceptanceCaseCards -Graph $graph)
                }
                $stats = Get-JsonProp -Object $graph -Name "stats" -Default $null
                [void](Write-S3Evidence -Name "design-graph.json" -Value $graph -Note "design graph after automatic generation")
                Add-AcceptanceCheck -Scenario $scenario -Name "case nodes appear in the design graph" -Passed ($caseCards.Count -ge 1) -Severity assertion `
                    -Detail ("cases=$($caseCards.Count) stats=" + $(if ($null -ne $stats) { $stats | ConvertTo-Json -Compress } else { "n/a" }))
                Save-S3State -Values @{ case_ids = @($caseCards | ForEach-Object { $_.case_id }) }

                $overview = $null
                try { $overview = Get-AcceptanceDesignOverview -Context $context -VersionId $versionId } catch { }
                [void](Write-S3Evidence -Name "design-overview.json" -Value $overview -Note "design round overview")

                # Job progress must be visible in the design conversation.
                $designMessages = @()
                if ($designConversationId -ne "") {
                    try { $designMessages = @(Get-AcceptanceMessages -Context $context -VersionId $versionId -ConversationId $designConversationId -Limit 200) } catch { }
                }
                $jobCards = @(Get-AcceptanceJobCards -Messages $designMessages)
                $designJobCards = @($jobCards | Where-Object { $_.kind -like "design.*" -or $_.kind -like "chat.*" })
                [void](Write-S3Evidence -Name "design-job-cards.json" -Value ([pscustomobject]@{
                    job_cards = $jobCards
                    tool_calls = @(Get-AcceptanceToolCalls -Messages $designMessages)
                }) -Kind "sse" -Note "job cards visible in the design conversation")
                Add-AcceptanceCheck -Scenario $scenario -Name "job progress visible in the design conversation" -Passed ($jobCards.Count -ge 1) -Severity assertion `
                    -Detail ("jobCards=$($jobCards.Count) designJobCards=$($designJobCards.Count) kinds=$((($jobCards | ForEach-Object { $_.kind }) -join ","))")

                # DB projection of the design work items (job/attempt visibility).
                $designJobsDb = Invoke-AcceptanceDbQuery -Context $context -VersionId ([long]$versionId) `
                    -Sql "SELECT id, type, status, attempts, max_attempts FROM work_item WHERE version_id = $versionId AND type LIKE 'design.%' ORDER BY id"
                $designJobRows = @($designJobsDb.Rows)
                [void](Write-S3Evidence -Name "design-work-items-db.json" -Value ([pscustomobject]@{
                    database = $designJobsDb.Database
                    ok       = $designJobsDb.Ok
                    error    = $designJobsDb.Error
                    rows     = $designJobRows
                }) -Kind "db" -Note "design.* work items (read-only DB projection)")
                Add-AcceptanceCheck -Scenario $scenario -Name "design work items visible to the job model" -Passed ($designJobsDb.Ok -and $designJobRows.Count -ge 1) -Severity assertion `
                    -Detail ("database=$($designJobsDb.Database) rows=$($designJobRows.Count) error=$($designJobsDb.Error)")

                # ------------------------------------------------------------------
                # Case detail facts: steps, assertions, evidence sources.
                # ------------------------------------------------------------------
                $firstCard = $caseCards | Where-Object { $_.status -eq "draft" } | Select-Object -First 1
                if ($null -eq $firstCard) { $firstCard = $caseCards | Select-Object -First 1 }
                if ($null -ne $firstCard) {
                    $detail = Get-AcceptanceCaseDetail -Context $context -VersionId $versionId -CaseId $firstCard.case_id
                    [void](Write-S3Evidence -Name "case-detail.json" -Value $detail -Note "first generated case")
                    $selected = Get-JsonProp -Object $detail -Name "selected" -Default $null
                    $steps = @(Get-JsonProp -Object $selected -Name "steps" -Default @())
                    $assertions = @(Get-JsonProp -Object $selected -Name "assertions" -Default @())
                    $sources = @(Get-JsonProp -Object $selected -Name "sources" -Default @())
                    $sourceKinds = @($sources | ForEach-Object { [string](Get-JsonProp -Object $_ -Name "kind" -Default "") } | Select-Object -Unique)
                    $successCriteria = @($assertions | Where-Object { -not [string]::IsNullOrWhiteSpace([string](Get-JsonProp -Object $_ -Name "expected" -Default "")) })
                    Add-AcceptanceCheck -Scenario $scenario -Name "case has executable steps" -Passed ($steps.Count -ge 1) -Severity assertion -Detail "steps=$($steps.Count)"
                    Add-AcceptanceCheck -Scenario $scenario -Name "case has success criteria" -Passed ($assertions.Count -ge 1 -and $successCriteria.Count -ge 1) -Severity assertion `
                        -Detail "assertions=$($assertions.Count) withExpected=$($successCriteria.Count)"
                    Add-AcceptanceCheck -Scenario $scenario -Name "case detail shows evidence sources" -Passed ($sources.Count -ge 1) -Severity assertion `
                        -Detail "sources=$($sources.Count) kinds=$($sourceKinds -join ",")"
                    $factSources = @($sourceKinds | Where-Object { $_ -in @("version_fact", "fact", "page", "api", "sut_page", "sut_api") })
                    Add-AcceptanceCheck -Scenario $scenario -Name "case evidence references discovered page/API/version facts" -Passed ($factSources.Count -ge 1) -Severity warning `
                        -Detail ("source kinds=$($sourceKinds -join ","); the discovery facts are missing on the current product (S1)")
                }

                # ------------------------------------------------------------------
                # S3 ends when automatic generation is complete. Case confirmation
                # (case_review -> executing) is the S4 case hard gate; S3 never
                # calls the legacy POST /design/confirmations endpoint.
                # ------------------------------------------------------------------
                $stageAtEnd = $null
                try { $stageAtEnd = Get-VersionStage -Context $context -VersionId $versionId } catch { }
                $stageAtEndValue = [string](Get-JsonProp -Object $stageAtEnd -Name "stage" -Default "")
                [void](Write-S3Evidence -Name "stage-at-end.json" -Value $stageAtEnd -Note "stage when S3 finished")
                Add-AcceptanceCheck -Scenario $scenario -Name "design generation reached case_review" -Passed ($stageAtEndValue -eq "case_review") -Severity warning `
                    -Detail ("stage=$stageAtEndValue cases=$($caseCards.Count); case confirmation belongs to S4")

                # ------------------------------------------------------------------
                # SSE: capture the persisted design/job events after the fact.
                # ------------------------------------------------------------------
                $cursor = 0
                try {
                    $cursor = Get-EventCursor -Context $context -Channel "version:$versionId"
                } catch { }
                $sseRecords = @()
                try {
                    $sse = Wait-SseEvent -Context $context -Channel "version:$versionId" -After $cursor -TimeoutSeconds 15 -MaxEvents 200
                    foreach ($eventObject in @($sse.Events)) {
                        $sseRecords += [pscustomobject]@{
                            id    = [string]$eventObject.Id
                            event = [string]$eventObject.Event
                            type  = [string](Get-JsonProp -Object $eventObject.Data -Name "type" -Default "")
                            kind  = [string](Get-JsonProp -Object (Get-JsonProp -Object $eventObject.Data -Name "payload" -Default $null) -Name "kind" -Default "")
                        }
                    }
                } catch {
                    Write-AcceptanceLog -Scenario $scenario -Level warn -Message "SSE capture failed: $($_.Exception.Message)"
                }
                [void](Write-S3Evidence -Name "sse-events.json" -Value $sseRecords -Kind "sse" -Note "version SSE events after design")
                $designEvents = @($sseRecords | Where-Object { $_.event -like "design.*" -or $_.event -eq "job.finished" -or $_.event -eq "job.progress" })
                Add-AcceptanceCheck -Scenario $scenario -Name "design/job SSE events recorded" -Passed ($designEvents.Count -ge 1) -Severity warning `
                    -Detail "events=$($designEvents.Count) ids=$((($designEvents | Select-Object -First 5 | ForEach-Object { $_.id }) -join ","))"

                # ------------------------------------------------------------------
                # Screenshot (evidence, not a pass criterion).
                # ------------------------------------------------------------------
                try {
                    $shot = Save-AcceptanceScreenshot -Context $context -Scenario $scenario -Route "/versions/${versionId}?tab=design" -FileName "s3-design-graph.png" `
                        -WaitForSelector '[data-testid="version-header"]' -Note "design graph after automatic case generation"
                    Add-AcceptanceCheck -Scenario $scenario -Name "design graph screenshot" -Passed $true -Severity warning -Detail (Resolve-RepoRelativePath -Context $context -Path $shot.Path)
                } catch {
                    Add-AcceptanceCheck -Scenario $scenario -Name "design graph screenshot" -Passed $false -Severity warning -Detail $_.Exception.Message
                }

                Save-S3State -Values @{
                    requirement_ids        = $requirementIds
                    case_ids               = @($caseCards | ForEach-Object { $_.case_id })
                    design_conversation_id = $designConversationId
                    stage_at_s3_end        = $stageAtEndValue
                    stage_gate_error       = $stageGateError
                }

                if ($caseCards.Count -eq 0 -or $designConversationId -eq "") {
                    $scenario.ErrorMessage = "S3 failed: designConversation=$designConversationId cases=$($caseCards.Count) stage=$stageAtEndValue"
                }
                }
            }
        }
    }
} catch {
    $scenario.ErrorMessage = $_.Exception.Message
    Add-AcceptanceCheck -Scenario $scenario -Name "S3 unexpected error" -Passed $false -Severity blocker -Detail ($_.Exception.Message + " | " + $_.ScriptStackTrace)
} finally {
    $exitCode = Complete-AcceptanceScenario -Scenario $scenario -Message "S3 requirements to design graph"
}

exit $exitCode
