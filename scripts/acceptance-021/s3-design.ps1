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

    Task 86 (P10-T11) adds the design-input resource gate: the version the
    design runs on must bind the pages/APIs P9 discovery proposed (pages > 0,
    APIs > 0, at least one page route_template locator). A version created
    before discovery carries an immutable empty binding, so it fails loudly
    instead of producing an empty VersionPack. Run S1 discovery first so the
    environment catalog has discovered resources when this version is created;
    a reused version created before the fix must be recreated (fresh S2/S3
    rerun on a version created after discovery). This script never creates,
    approves or fakes a resource.

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
    [string] $RequirementDocumentPath = "D:\project\deliverables\墨栈博客系统需求文档（用户故事与BDD版）.md",
    [string] $MatrixPath = "",
    [int] $ClarifyTimeoutSeconds = 180,
    [int] $DesignTimeoutSeconds = 300,
    [int] $CaseTimeoutSeconds = 240,
    [int] $WaitForP0CasesSeconds = 0
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
if ([string]::IsNullOrWhiteSpace($MatrixPath)) {
    $MatrixPath = Join-Path $repoRoot "docs/version-V0.2.1/acceptance/sut-requirements-matrix.md"
} elseif (-not [System.IO.Path]::IsPathRooted($MatrixPath)) {
    $MatrixPath = Join-Path $repoRoot $MatrixPath
}
$matrixPath = [System.IO.Path]::GetFullPath($MatrixPath)

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
$designResourceCounts = $null
$designResourceCheck = $null

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

function Get-CaseSutTraceability {
    <#
        Joins the design graph case cards to the version requirements and then
        to the FR/NFR IDs. A case card carries requirement_id/title; the join is
        by requirement ID, and the FR/NFR ID comes from the requirement title
        (or its description fallback). No fuzzy title matching is used.
    #>
    param(
        [Parameter(Mandatory = $false)] $Requirements = @(),
        [Parameter(Mandatory = $false)] $CaseCards = @()
    )
    $byRequirementId = @{}
    foreach ($requirement in @($Requirements)) {
        $byRequirementId[[string](Get-JsonProp -Object $requirement -Name "id" -Default "")] = $requirement
    }
    $rows = @()
    $caseCountBySutId = @{}
    $traceableCases = 0
    $untraceableCases = @()
    foreach ($card in @($CaseCards)) {
        $requirementId = [string](Get-JsonProp -Object $card -Name "requirement_id" -Default "")
        $requirement = $null
        if ($byRequirementId.ContainsKey($requirementId)) { $requirement = $byRequirementId[$requirementId] }
        $sutId = ""
        $title = [string](Get-JsonProp -Object $card -Name "title" -Default "")
        if ($null -ne $requirement) { $sutId = Get-RequirementSutId -Requirement $requirement }
        if ($sutId -eq "") {
            $untraceableCases += [pscustomobject]@{ case_id = [string](Get-JsonProp -Object $card -Name "case_id" -Default ""); title = $title; requirement_id = $requirementId }
        } else {
            $traceableCases++
            if (-not $caseCountBySutId.ContainsKey($sutId)) { $caseCountBySutId[$sutId] = 0 }
            $caseCountBySutId[$sutId] = $caseCountBySutId[$sutId] + 1
        }
        $rows += [pscustomobject]@{
            sut_id            = $sutId
            requirement_id    = $requirementId
            requirement_title = if ($null -ne $requirement) { [string](Get-JsonProp -Object $requirement -Name "title" -Default "") } else { [string](Get-JsonProp -Object $card -Name "requirement_title" -Default "") }
            case_id           = [string](Get-JsonProp -Object $card -Name "case_id" -Default "")
            case_title        = $title
            status            = [string](Get-JsonProp -Object $card -Name "status" -Default "")
            traceable         = ($sutId -ne "")
        }
    }
    return [pscustomobject]@{
        Rows            = @($rows)
        CaseCountBySutId = $caseCountBySutId
        TraceableCases  = $traceableCases
        UntraceableCases = @($untraceableCases)
    }
}

function New-SutRequirementsMatrix {
    <#
        Writes the Task 72 FR/NFR -> requirement -> case -> status matrix from
        live API data. Rows without a requirement are reported as missing; V2
        rows are reported as 延展·未实现（未交付） and are never counted as a
        defect or as missing delivery.
    #>
    param(
        [Parameter(Mandatory = $true)] $BaselineEntries,
        [Parameter(Mandatory = $false)] $Requirements = @(),
        [Parameter(Mandatory = $false)] $CaseCards = @(),
        [Parameter(Mandatory = $true)] [string] $VersionId,
        [Parameter(Mandatory = $true)] [string] $Path,
        [Parameter(Mandatory = $false)] [string] $DocumentPath = "",
        [Parameter(Mandatory = $false)] [string] $Stage = "",
        [Parameter(Mandatory = $false)] [string] $VersionTitle = ""
    )
    $requirementById = @{}
    $requirementBySutId = @{}
    foreach ($requirement in @($Requirements)) {
        $requirementById[[string](Get-JsonProp -Object $requirement -Name "id" -Default "")] = $requirement
        $sutId = Get-RequirementSutId -Requirement $requirement
        if ($sutId -ne "" -and -not $requirementBySutId.ContainsKey($sutId)) { $requirementBySutId[$sutId] = $requirement }
    }
    $caseCountBySutId = @{}
    $caseIdsBySutId = @{}
    foreach ($card in @($CaseCards)) {
        $requirementId = [string](Get-JsonProp -Object $card -Name "requirement_id" -Default "")
        if (-not $requirementById.ContainsKey($requirementId)) { continue }
        $sutId = Get-RequirementSutId -Requirement $requirementById[$requirementId]
        if ($sutId -eq "") { continue }
        if (-not $caseCountBySutId.ContainsKey($sutId)) { $caseCountBySutId[$sutId] = 0; $caseIdsBySutId[$sutId] = @() }
        $caseCountBySutId[$sutId] = $caseCountBySutId[$sutId] + 1
        $caseIdsBySutId[$sutId] += [string](Get-JsonProp -Object $card -Name "case_id" -Default "")
    }

    $v1Entries = @($BaselineEntries | Where-Object { [string]$_.version -eq "V1" })
    $v2Entries = @($BaselineEntries | Where-Object { [string]$_.version -eq "V2" })
    $v1Covered = 0
    $p0Total = 0
    $p0WithCases = 0
    $casesTotal = @($CaseCards).Count
    $lines = @()
    $lines += "# SUT 需求追溯矩阵（FR/NFR → requirement → case → 状态）"
    $lines += ""
    $lines += "> 由真实运行数据生成，不是手工填写：需求来自 EzTest 版本 $VersionId 的 requirement API，用例来自 design graph。"
    $lines += "> 生成时间：$((Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ'))；版本标题：$VersionTitle；stage：$Stage。"
    $lines += "> 需求文档：``$DocumentPath``（v3.0，V1 58 FR + 9 NFR，V2 32 延展·未实现）。"
    $lines += ""
    $lines += "## 1. 汇总"
    $lines += ""
    $lines += "| 指标 | 数值 |"
    $lines += "|---|---|"
    $lines += "| V1 需求总数（FR+NFR） | $($v1Entries.Count) |"
    $lines += "| V1 需求已导入为 requirement | $(@($v1Entries | Where-Object { $requirementBySutId.ContainsKey([string]$_.id) }).Count) |"
    $lines += "| V1 功能需求 FR-001~FR-058 | $(@($v1Entries | Where-Object { $_.id -like 'FR-0*' }).Count) |"
    $lines += "| 非功能需求 NFR-001~NFR-009 | $(@($v1Entries | Where-Object { $_.id -like 'NFR-*' }).Count) |"
    $lines += "| P0 需求（FR+NFR） | $($v1Entries | Where-Object { $_.priority -eq 'P0' } | Measure-Object | Select-Object -ExpandProperty Count) |"
    $lines += "| 设计用例总数 | $casesTotal |"
    $lines += "| 追溯不到 FR/NFR 的用例数 | $(@((Get-CaseSutTraceability -Requirements $Requirements -CaseCards $CaseCards).UntraceableCases).Count) |"
    $lines += "| V2 延展·未实现 | $($v2Entries.Count)（未交付，不计为缺失或缺陷） |"
    $lines += ""
    $lines += "## 2. V1 需求追溯（FR/NFR）"
    $lines += ""
    $lines += "| 需求编号 | 优先级 | 标题 | requirement id | 需求状态 | 用例数 | 用例 ID | 追溯状态 |"
    $lines += "|---|---|---|---|---|---|---|---|"
    foreach ($entry in $v1Entries) {
        $sutId = [string]$entry.id
        $requirement = $null
        if ($requirementBySutId.ContainsKey($sutId)) { $requirement = $requirementBySutId[$sutId] }
        $requirementId = if ($null -ne $requirement) { [string](Get-JsonProp -Object $requirement -Name "id" -Default "") } else { "" }
        $requirementStatus = if ($null -ne $requirement) { [string](Get-JsonProp -Object $requirement -Name "status" -Default "") } else { "缺失" }
        $caseCount = 0
        $caseText = "-"
        if ($caseCountBySutId.ContainsKey($sutId)) { $caseCount = [int]$caseCountBySutId[$sutId]; $caseText = (@($caseIdsBySutId[$sutId]) -join ", ") }
        $trace = if ($null -eq $requirement) { "需求未导入" } elseif ($caseCount -gt 0) { "需求→用例" } else { "仅需求（无用例）" }
        if ($null -ne $requirement) { $v1Covered++ }
        if ([string]$entry.priority -eq "P0") {
            $p0Total++
            if ($caseCount -gt 0) { $p0WithCases++ }
        }
        $lines += "| $sutId | $([string]$entry.priority) | $([string]$entry.title) | $requirementId | $requirementStatus | $caseCount | $caseText | $trace |"
    }
    $lines += ""
    $lines += "## 3. V2 延展·未实现（未交付）"
    $lines += ""
    $lines += "| 需求编号 | 优先级 | 标题 | 交付状态 | 说明 |"
    $lines += "|---|---|---|---|---|"
    foreach ($entry in $v2Entries) {
        $lines += "| $([string]$entry.id) | $([string]$entry.priority) | $([string]$entry.title) | 延展·未实现（未交付） | 当前系统不存在，不属于 V1 验收范围，不计为缺陷 |"
    }
    $lines += ""
    $lines += "## 4. 覆盖结论"
    $lines += ""
    $frEntries = @($v1Entries | Where-Object { $_.id -like "FR-0*" })
    $nfrEntries = @($v1Entries | Where-Object { $_.id -like "NFR-*" })
    $frWithCases = @($frEntries | Where-Object { $caseCountBySutId.ContainsKey([string]$_.id) }).Count
    $nfrWithCases = @($nfrEntries | Where-Object { $caseCountBySutId.ContainsKey([string]$_.id) }).Count
    $lines += "- V1 需求导入：$v1Covered/$($v1Entries.Count)；P0 需求导入：$(@($v1Entries | Where-Object { $_.priority -eq 'P0' -and $requirementBySutId.ContainsKey([string]$_.id) }).Count)/$p0Total。"
    $lines += "- 设计用例覆盖的 FR：$frWithCases/$($frEntries.Count)；NFR：$nfrWithCases/$($nfrEntries.Count)；P0：$p0WithCases/$p0Total。"
    $lines += "- V2：$($v2Entries.Count) 条均为延展·未实现，未创建需求/用例，未计为缺陷。"
    $lines += "- 用例覆盖是设计阶段的真实产出；若少于 100%，矩阵如实显示剩余项，不伪造用例。"
    $lines += ""
    $directory = Split-Path -Parent $Path
    if (-not [string]::IsNullOrWhiteSpace($directory)) { New-Item -ItemType Directory -Force -Path $directory | Out-Null }
    [System.IO.File]::WriteAllText($Path, ($lines -join [Environment]::NewLine) + [Environment]::NewLine, [System.Text.UTF8Encoding]::new($false))
    return [pscustomobject]@{
        Path            = $Path
        V1Total         = $v1Entries.Count
        V1Covered       = $v1Covered
        P0Total         = $p0Total
        P0WithCases     = $p0WithCases
        CasesTotal      = $casesTotal
        FrWithCases     = $frWithCases
        NfrWithCases    = $nfrWithCases
        V2Total         = $v2Entries.Count
    }
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

        $designResourceCounts = $null
        $designResourceCheck = $null
        if ($null -ne $version -and $versionId -ne "") {
            Save-S3State -Values @{ version_id = $versionId }
            Write-S3Evidence -Name "version.json" -Value $version -Note "version used by S3"

            # ------------------------------------------------------------------
            # Task 86 (P10-T11): the design input must be a version that binds
            # the pages/APIs P9 discovery proposed. A version created before
            # discovery (e.g. the Task 72 S2 version) has an immutable empty
            # binding and can never gain pages; fail loudly here instead of
            # burning a design run on an empty VersionPack.
            # ------------------------------------------------------------------
            $designResourceCounts = Get-AcceptanceVersionResourceCounts -Version $version
            $designResourceCheck = Get-AcceptanceVersionResourceCheck -Counts $designResourceCounts -RequireApis -RequireLocators -RequirePageUrls
            [void](Write-S3Evidence -Name "design-input-resources.json" -Value ([pscustomobject]@{
                version_id = $versionId
                reused     = $isReusedVersion
                counts     = [pscustomobject]@{
                    bindings       = $designResourceCounts.Bindings
                    pages          = $designResourceCounts.PageCount
                    apis           = $designResourceCounts.ApiCount
                    route_locators = $designResourceCounts.RoutePageCount
                    page_urls      = $designResourceCounts.UrlPageCount
                }
                pages      = @($designResourceCounts.Pages)
                apis       = @($designResourceCounts.APIs)
                check      = $designResourceCheck
            }) -Kind "binding" -Note "pages/APIs bound to the design version (VersionPack locator source)")
            Add-AcceptanceCheck -Scenario $scenario -Name "version_pages > 0" -Passed $designResourceCheck.PagePassed -Severity assertion -Detail $designResourceCheck.PageDetail
            Add-AcceptanceCheck -Scenario $scenario -Name "version_apis > 0" -Passed $designResourceCheck.ApiPassed -Severity assertion -Detail $designResourceCheck.ApiDetail
            Add-AcceptanceCheck -Scenario $scenario -Name "bound pages provide route locators for design" -Passed $designResourceCheck.LocatorPassed -Severity assertion -Detail $designResourceCheck.LocatorDetail
            Add-AcceptanceCheck -Scenario $scenario -Name "bound pages provide SUT page URLs for execution" -Passed $designResourceCheck.UrlPassed -Severity assertion -Detail $designResourceCheck.UrlDetail
            if (-not $designResourceCheck.Passed) {
                $scenario.ErrorMessage = "S3 failed: version $versionId does not bind usable discovery resources (version_pages=$($designResourceCounts.PageCount) version_apis=$($designResourceCounts.ApiCount) route_locators=$($designResourceCounts.RoutePageCount)); version bindings are immutable, so run fresh S1 discovery first and use a version created after it (fresh S2/S3 rerun)"
            }
        }

        if ($null -ne $designResourceCheck -and $designResourceCheck.Passed) {
            # ------------------------------------------------------------------
            # Clarification: create the conversation and request a compact
            # requirement draft. The design conversation must NOT be created by
            # this script later.
            # ------------------------------------------------------------------
            $conversationId = ""
            $requirements = @()
            $requirementsAlreadyImported = $false
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
                $reusedRequirements = @(Get-VersionRequirements -Context $context -VersionId $versionId -Limit 200)
                if ($reusedRequirements.Count -gt 0) {
                    $requirements = $reusedRequirements
                    $requirementsAlreadyImported = $true
                    Add-AcceptanceCheck -Scenario $scenario -Name "reused version already carries imported requirements" -Passed $true -Severity assertion `
                        -Detail "requirements=$($requirements.Count); the S3 script does not post the legacy 4-requirement goal over an imported baseline"
                }
            }

            if (-not $requirementsAlreadyImported) {
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
                }
            }

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

            # ------------------------------------------------------------------
            # Task 72: SUT requirement baseline and FR/NFR traceability at the
            # design input. This reads the real requirement document (read-only)
            # and joins it to the live requirement list; it never writes data.
            # ------------------------------------------------------------------
            $baselineEntries = @()
            $baselineError = ""
            try {
                $baselineEntries = @(Read-SutRequirementDocument -Path $RequirementDocumentPath)
            } catch {
                $baselineError = $_.Exception.Message
            }
            $v1Baseline = @($baselineEntries | Where-Object { [string]$_.version -eq "V1" })
            $v2Baseline = @($baselineEntries | Where-Object { [string]$_.version -eq "V2" })
            $v1FrBaseline = @($v1Baseline | Where-Object { [string]$_.id -like "FR-0*" })
            $nfrBaseline = @($v1Baseline | Where-Object { [string]$_.id -like "NFR-*" })
            $p0Baseline = @($v1Baseline | Where-Object { [string]$_.priority -eq "P0" })
            $coverageBefore = Get-SutRequirementCoverage -Entries $baselineEntries -Requirements $requirements -Version "V1"
            $v2CoverageBefore = Get-SutRequirementCoverage -Entries $baselineEntries -Requirements $requirements -Version "V2"
            $v2CreatedBefore = @($v2CoverageBefore.Rows | Where-Object { $_.covered })
            $untraceableBefore = @($requirements | Where-Object { (Get-RequirementSutId -Requirement $_) -eq "" })
            [void](Write-S3Evidence -Name "sut-baseline.json" -Value ([pscustomobject]@{
                path        = $RequirementDocumentPath
                parse_error = $baselineError
                v1_total    = $v1Baseline.Count
                v1_fr       = $v1FrBaseline.Count
                nfr         = $nfrBaseline.Count
                p0          = $p0Baseline.Count
                v2          = $v2Baseline.Count
                entries     = @($baselineEntries | Select-Object id, title, priority, version, version_label)
            }) -Kind "baseline" -Note "SUT requirement document baseline used by S3")
            [void](Write-S3Evidence -Name "sut-requirements-before.json" -Value ([pscustomobject]@{
                generated_at = (Get-Date).ToUniversalTime().ToString("o")
                summary      = [pscustomobject]@{
                    v1_total   = $coverageBefore.Total
                    v1_covered = $coverageBefore.Covered
                    v1_missing = @($coverageBefore.Missing)
                    p0_total   = $coverageBefore.P0Total
                    p0_covered = $coverageBefore.P0Covered
                    p0_missing = @($coverageBefore.P0Missing)
                    v2_created = $v2CreatedBefore.Count
                    untraceable = $untraceableBefore.Count
                }
                rows = @($coverageBefore.Rows)
            }) -Kind "coverage" -Note "FR/NFR -> requirement coverage at the S3 design input")
            [void](Write-S3Evidence -Name "v2-handling.json" -Value ([pscustomobject]@{
                v2_total = $v2Baseline.Count
                v2_created_as_requirements = $v2CreatedBefore.Count
                v2_status = "延展·未实现（当前系统不存在，不要求交付，不算缺陷）"
                rows = @($v2CoverageBefore.Rows)
            }) -Kind "coverage" -Note "V2 延展·未实现 handling at S3")
            Add-AcceptanceCheck -Scenario $scenario -Name "SUT requirement baseline available (V1 58 FR + 9 NFR + V2 32)" `
                -Passed ($baselineError -eq "" -and $v1FrBaseline.Count -eq 58 -and $nfrBaseline.Count -eq 9 -and $v2Baseline.Count -eq 32) -Severity assertion `
                -Detail ("error=$baselineError v1fr=$($v1FrBaseline.Count) nfr=$($nfrBaseline.Count) v2=$($v2Baseline.Count) p0=$($p0Baseline.Count)")
            $frCoveredBefore = @($coverageBefore.Rows | Where-Object { $_.sut_id -like "FR-0*" -and $_.covered }).Count
            $nfrCoveredBefore = @($coverageBefore.Rows | Where-Object { $_.sut_id -like "NFR-*" -and $_.covered }).Count
            Add-AcceptanceCheck -Scenario $scenario -Name "design input is traceable to V1 FR/NFR (67/67)" `
                -Passed ($frCoveredBefore -eq 58 -and $nfrCoveredBefore -eq 9) -Severity assertion `
                -Detail "fr=$frCoveredBefore/58 nfr=$nfrCoveredBefore/9 missing=$(@($coverageBefore.Missing) -join ',')"
            Add-AcceptanceCheck -Scenario $scenario -Name "design input covers all P0 requirements ($($p0Baseline.Count)/$($p0Baseline.Count))" `
                -Passed ($coverageBefore.P0Covered -eq $coverageBefore.P0Total -and $coverageBefore.P0Total -gt 0) -Severity assertion `
                -Detail "p0=$($coverageBefore.P0Covered)/$($coverageBefore.P0Total) missing=$(@($coverageBefore.P0Missing) -join ',')"
            Add-AcceptanceCheck -Scenario $scenario -Name "V2 延展·未实现 not imported as design requirements" `
                -Passed ($v2CreatedBefore.Count -eq 0) -Severity assertion `
                -Detail "v2_created=$($v2CreatedBefore.Count)/$($v2Baseline.Count)"
            Add-AcceptanceCheck -Scenario $scenario -Name "requirements are traceable to FR/NFR IDs" `
                -Passed ($untraceableBefore.Count -eq 0) -Severity assertion `
                -Detail "untraceable=$($untraceableBefore.Count)"

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
                # The matrix and case traceability must show the post-gate
                # requirement status, not the pre-gate draft snapshot.
                $requirements = @($requirementsAfter)

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

                # Task 72 (optional): the automatic design fans out one case-author
                # task per confirmed requirement. When requested, wait until every
                # P0 baseline requirement has at least one case, so the matrix is
                # generated from a meaningful P0 sample. This never invents cases:
                # it only waits for real draft cases and reports a timeout honestly.
                if ($WaitForP0CasesSeconds -gt 0 -and $p0Baseline.Count -gt 0) {
                    $p0Wait = Wait-Until -TimeoutSeconds $WaitForP0CasesSeconds -IntervalSeconds 10 -Description "P0 case coverage" -Condition {
                        $currentGraph = Get-AcceptanceDesignGraph -Context $context -VersionId $versionId
                        $currentCards = @(Get-AcceptanceCaseCards -Graph $currentGraph)
                        $currentTrace = Get-CaseSutTraceability -Requirements $requirements -CaseCards $currentCards
                        $p0Ids = @($p0Baseline | ForEach-Object { [string]$_.id })
                        $coveredP0 = @($p0Ids | Where-Object { $currentTrace.CaseCountBySutId.ContainsKey($_) })
                        if ($coveredP0.Count -ge $p0Ids.Count) {
                            return [pscustomobject]@{ Graph = $currentGraph; Cards = $currentCards }
                        }
                        return $null
                    }
                    if ($p0Wait.Ok -and $null -ne $p0Wait.Value) {
                        $graph = $p0Wait.Value.Graph
                        $caseCards = @($p0Wait.Value.Cards)
                        Add-AcceptanceCheck -Scenario $scenario -Name "P0 case coverage wait completed" -Passed $true -Severity warning `
                            -Detail ("p0=$($p0Baseline.Count)/$($p0Baseline.Count) cases=$($caseCards.Count)")
                    } else {
                        $graph = Get-AcceptanceDesignGraph -Context $context -VersionId $versionId
                        $caseCards = @(Get-AcceptanceCaseCards -Graph $graph)
                        $latestTrace = Get-CaseSutTraceability -Requirements $requirements -CaseCards $caseCards
                        $coveredP0 = @($p0Baseline | Where-Object { $latestTrace.CaseCountBySutId.ContainsKey([string]$_.id) })
                        Add-AcceptanceCheck -Scenario $scenario -Name "P0 case coverage wait completed" -Passed $false -Severity warning `
                            -Detail ("p0_with_cases=$($coveredP0.Count)/$($p0Baseline.Count) after ${WaitForP0CasesSeconds}s; matrix records the real remainder")
                    }
                }
                $stats = Get-JsonProp -Object $graph -Name "stats" -Default $null
                [void](Write-S3Evidence -Name "design-graph.json" -Value $graph -Note "design graph after automatic generation")
                Add-AcceptanceCheck -Scenario $scenario -Name "case nodes appear in the design graph" -Passed ($caseCards.Count -ge 1) -Severity assertion `
                    -Detail ("cases=$($caseCards.Count) stats=" + $(if ($null -ne $stats) { $stats | ConvertTo-Json -Compress } else { "n/a" }))
                Save-S3State -Values @{ case_ids = @($caseCards | ForEach-Object { $_.case_id }) }

                # ------------------------------------------------------------------
                # Task 72: case -> requirement -> FR/NFR traceability. Untraceable
                # cases and any V2 case are hard failures; incomplete case coverage
                # of the P0/FR baseline is reported as a warning with exact IDs.
                # ------------------------------------------------------------------
                $caseTrace = Get-CaseSutTraceability -Requirements $requirements -CaseCards $caseCards
                $v2Ids = @($v2Baseline | ForEach-Object { [string]$_.id })
                $v2CaseRows = @($caseTrace.Rows | Where-Object { $v2Ids -contains $_.sut_id })
                $caseSutIds = @($caseTrace.CaseCountBySutId.Keys)
                $p0WithCases = @($p0Baseline | Where-Object { $caseSutIds -contains [string]$_.id })
                $frWithCases = @($v1FrBaseline | Where-Object { $caseSutIds -contains [string]$_.id })
                $nfrWithCases = @($nfrBaseline | Where-Object { $caseSutIds -contains [string]$_.id })
                [void](Write-S3Evidence -Name "case-traceability.json" -Value ([pscustomobject]@{
                    generated_at       = (Get-Date).ToUniversalTime().ToString("o")
                    cases_total        = @($caseCards).Count
                    traceable_cases    = $caseTrace.TraceableCases
                    untraceable_cases  = @($caseTrace.UntraceableCases)
                    fr_with_cases      = @($frWithCases | ForEach-Object { $_.id })
                    nfr_with_cases     = @($nfrWithCases | ForEach-Object { $_.id })
                    p0_with_cases      = @($p0WithCases | ForEach-Object { $_.id })
                    p0_total           = $p0Baseline.Count
                    v2_case_rows       = @($v2CaseRows)
                    rows               = @($caseTrace.Rows)
                }) -Kind "coverage" -Note "case -> requirement -> FR/NFR traceability")
                Add-AcceptanceCheck -Scenario $scenario -Name "design cases traceable to FR/NFR requirements" `
                    -Passed ($caseTrace.TraceableCases -ge 1 -and @($caseTrace.UntraceableCases).Count -eq 0) -Severity assertion `
                    -Detail ("cases=$(@($caseCards).Count) traceable=$($caseTrace.TraceableCases) untraceable=$(@($caseTrace.UntraceableCases).Count)")
                Add-AcceptanceCheck -Scenario $scenario -Name "V2 延展·未实现 not designed as delivered cases" `
                    -Passed (@($v2CaseRows).Count -eq 0) -Severity assertion `
                    -Detail "v2_cases=$(@($v2CaseRows).Count)"
                Add-AcceptanceCheck -Scenario $scenario -Name "P0 requirements have design cases" `
                    -Passed ($p0WithCases.Count -ge 1) -Severity warning `
                    -Detail ("p0_with_cases=$($p0WithCases.Count)/$($p0Baseline.Count) covered=$(@($p0WithCases | ForEach-Object { $_.id }) -join ',')")
                Add-AcceptanceCheck -Scenario $scenario -Name "all V1 requirements have design cases" `
                    -Passed ($frWithCases.Count -eq $v1FrBaseline.Count -and $nfrWithCases.Count -eq $nfrBaseline.Count) -Severity warning `
                    -Detail ("fr_with_cases=$($frWithCases.Count)/$($v1FrBaseline.Count) nfr_with_cases=$($nfrWithCases.Count)/$($nfrBaseline.Count)")

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
                # Task 72 deliverable: the FR/NFR -> requirement -> case matrix.
                # ------------------------------------------------------------------
                $matrixResult = $null
                try {
                    $matrixResult = New-SutRequirementsMatrix -BaselineEntries $baselineEntries -Requirements $requirements `
                        -CaseCards $caseCards -VersionId $versionId -Path $matrixPath -DocumentPath $RequirementDocumentPath `
                        -Stage $stageAtEndValue -VersionTitle ([string](Get-JsonProp -Object $version -Name "title" -Default ""))
                    Add-AcceptanceArtifact -Scenario $scenario -Path $matrixPath -Kind "matrix" -Note "FR/NFR -> requirement -> case -> status traceability matrix"
                    Add-AcceptanceCheck -Scenario $scenario -Name "SUT requirement traceability matrix written" -Passed (Test-Path -LiteralPath $matrixPath) -Severity assertion `
                        -Detail ("path=" + (Resolve-RepoRelativePath -Context $context -Path $matrixPath) + " v1=$($matrixResult.V1Covered)/$($matrixResult.V1Total) p0=$($matrixResult.P0WithCases)/$($matrixResult.P0Total) cases=$($matrixResult.CasesTotal)")
                } catch {
                    Add-AcceptanceCheck -Scenario $scenario -Name "SUT requirement traceability matrix written" -Passed $false -Severity assertion -Detail $_.Exception.Message
                }
                [void](Write-S3Evidence -Name "sut-matrix-summary.json" -Value ([pscustomobject]@{
                    matrix_path = if ($null -ne $matrixResult) { $matrixResult.Path } else { $matrixPath }
                    summary     = $matrixResult
                    coverage    = [pscustomobject]@{
                        fr_with_cases = @($frWithCases | ForEach-Object { $_.id })
                        nfr_with_cases = @($nfrWithCases | ForEach-Object { $_.id })
                        p0_with_cases = @($p0WithCases | ForEach-Object { $_.id })
                        v2_total = $v2Baseline.Count
                        v2_cases = @($v2CaseRows).Count
                    }
                }) -Kind "summary" -Note "matrix generation summary")

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
