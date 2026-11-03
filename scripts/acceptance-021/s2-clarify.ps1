#requires -Version 5.1
<#
.SYNOPSIS
    S2 clarification acceptance for EzTest 0.2.1 (Task 88 small-input profile,
    with the Task 72 full-document traceability kept as an explicit opt-in).

.DESCRIPTION
    Drives a real-LLM clarification conversation for a freshly created 墨栈博客
    version, imports exactly one explicit requirement source as an attachment,
    and asserts the S2 criteria plus the traceability criteria:

      1. the requirement input is uploaded through POST /api/content and attached
         to the clarification message (sha256/size verified); the server extracts
         the text (requirement_attachment.extraction_status = done) instead of
         the script only passing a path;
      2. every requirement in the resolved input is created as a draft whose
         title is traceable to its FR/NFR ID, via bounded, real input-text
         batches. In the recommended small-input mode the input is one of
         small-a.md / small-b.feature / small-b.txt / small-c.docx and the
         expected count/FR ID set comes from that file only; the legacy
         -FullSutDoc mode keeps the previous full-document behavior. V2
         requirements are never created from the input;
      3. the requirement count equals the expected count, the imported FR ID set
         equals the input set, there are no duplicates/untraceable rows, and all
         requirements stay draft at the requirement_review gate;
      4. every requirement gets at most six clarification questions (aggregate
         budget = 6 * current requirement count), no normalized duplicate
         questions, and the version stops at requirement_review without
         automatic design/execution.

    The script never writes requirements through SQL and never fabricates a
    requirement. The only DB use is a read-only projection of the attachment
    parse state and the per-conversation agent-run usage. If a batch of the
    model only ingests a subset, the evidence records exactly which FR/NFR IDs
    are missing and the assertion fails instead of claiming full coverage.
    S2 records the imported version/expected/actual set in the shared state so
    S3 designs the same version instead of creating an unrelated requirement set.

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
    [int] $ReplyTimeoutSeconds = 180,
    [int] $QuestionBudgetPerRequirement = 6,
    # Product clarification budget per version (spec §5.5/§15). S2 harness
    # extraction conversations are not user clarification turns, so the budget is
    # asserted on the primary+convergence conversations only and the
    # extraction-session exception is recorded in questions.json/result warnings.
    [int] $UserClarificationQuestionBudget = 30,
    # Task 88 (P10-T13): one explicit requirement source. The final E2E passes
    # -RequirementInput <small-a.md|small-b.feature|small-b.txt|small-c.docx>
    # (and usually -ExpectedRequirements 3). -FullSutDoc keeps the legacy full
    # SUT document import as an explicit opt-in; there is no implicit 67 fallback.
    [string] $RequirementInput = "",
    [int] $ExpectedRequirements = 0,
    [string] $ExpectedFrIds = "",
    [switch] $FullSutDoc,
    [string] $FullSutDocPath = "",
    [string] $RequirementDocumentPath = "D:\project\deliverables\墨栈博客系统需求文档（用户故事与BDD版）.md",
    [int] $ImportMaxEntriesPerBatch = 8,
    [int] $ImportMaxCharsPerBatch = 9000,
    [int] $ImportBatchTimeoutSeconds = 240,
    [int] $ImportMaxBatches = 0,
    [int] $ImportConsecutiveEmptyBatchStop = 2
)

Set-StrictMode -Version 3.0
$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
if ([string]::IsNullOrWhiteSpace($EvidenceRoot)) {
    $EvidenceRoot = Join-Path $repoRoot "docs/version-V0.2.1/acceptance/evidence/sut-requirements"
}

. (Join-Path $PSScriptRoot "lib.ps1")
. (Join-Path $PSScriptRoot "lib-s3-s6.ps1")

function Wait-NewAssistantText {
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $VersionId,
        [Parameter(Mandatory = $true)] [string] $ConversationId,
        [Parameter(Mandatory = $true)] [string] $AfterMessageId,
        [Parameter(Mandatory = $false)] [int] $TimeoutSeconds = 180
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

function Get-ConversationAssistantRecords {
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $VersionId,
        [Parameter(Mandatory = $true)] [string] $ConversationId
    )
    $messages = @(Get-ConversationMessages -Context $Context -VersionId $VersionId -ConversationId $ConversationId -Limit 200)
    $records = @()
    foreach ($message in $messages) {
        if ([string](Get-JsonProp -Object $message -Name "role" -Default "") -ne "assistant") { continue }
        $text = Get-AssistantText -Message $message
        if ($text.Trim().Length -eq 0) { continue }
        $records += [pscustomobject]@{
            conversation_id = $ConversationId
            message_id      = [string](Get-JsonProp -Object $message -Name "id" -Default "")
            text            = $text
            questions       = @(Get-QuestionTexts -Message $message)
        }
    }
    return $records
}

function Get-SutAttachmentProjection {
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $VersionId
    )
    $sql = "SELECT a.id, a.name, a.mime, a.size, a.extraction_status, coalesce(a.extraction_error,''), coalesce(a.extracted_content_ref_id,0), coalesce(a.conversation_message_id,0) FROM requirement_attachment a WHERE a.version_id = $VersionId ORDER BY a.id"
    $query = Invoke-AcceptanceDbQuery -Context $Context -VersionId ([long]$VersionId) -Sql $sql
    $rows = @()
    foreach ($line in @($query.Rows)) {
        $fields = @(Get-AcceptanceSplitRow -Line $line)
        if ($fields.Count -lt 8) { continue }
        $rows += [pscustomobject]@{
            attachment_id       = [string]$fields[0]
            name                = [string]$fields[1]
            mime                = [string]$fields[2]
            size                = [long]$fields[3]
            extraction_status   = [string]$fields[4]
            extraction_error    = [string]$fields[5]
            extracted_ref_id    = [long]$fields[6]
            conversation_message_id = [long]$fields[7]
        }
    }
    $content = $null
    $contentQuery = @{}
    if ($rows.Count -gt 0 -and $rows[0].extracted_ref_id -gt 0) {
        $ref = [long]$rows[0].extracted_ref_id
        $sqlContent = "SELECT o.sha256, o.size, o.mime, o.relative_path FROM content_ref r JOIN content_object o ON o.sha256 = r.object_id WHERE r.id = $ref"
        $contentQuery = Invoke-AcceptanceDbQuery -Context $Context -VersionId ([long]$VersionId) -Sql $sqlContent
        $contentRows = @($contentQuery.Rows)
        if ($contentRows.Count -gt 0) {
            $fields = @(Get-AcceptanceSplitRow -Line ([string]$contentRows[0]))
            if ($fields.Count -ge 4) {
                $content = [pscustomobject]@{
                    sha256        = [string]$fields[0]
                    size          = [long]$fields[1]
                    mime          = [string]$fields[2]
                    relative_path = [string]$fields[3]
                }
            }
        }
    }
    return [pscustomobject]@{
        Ok       = $query.Ok
        Database = $query.Database
        Error    = $query.Error
        Rows     = @($rows)
        Content  = $content
    }
}

function New-SutBatchPrompt {
    param(
        [Parameter(Mandatory = $true)] [string] $DocumentName,
        [Parameter(Mandatory = $true)] [int] $Index,
        [Parameter(Mandatory = $true)] [int] $Total,
        [Parameter(Mandatory = $true)] $Entries,
        [Parameter(Mandatory = $false)] [string[]] $ExpectedIds = @(),
        [Parameter(Mandatory = $false)] [switch] $SmallInput,
        [Parameter(Mandatory = $false)] [int] $V2Count = 0
    )
    $entryList = @($Entries)
    $ids = if (@($ExpectedIds).Count -gt 0) { @($ExpectedIds) } else { @($entryList | ForEach-Object { [string]$_.id }) }
    $raw = ($entryList | ForEach-Object { [string]$_.text }) -join "`n`n"
    $scopeRule = if ($SmallInput) {
        "5. 本次只允许创建下面列出的编号；输入不含 V2 延展需求，不要创建任何未列出的编号。"
    } else {
        "5. 不要创建 V2 延展·未实现需求（输入中 $V2Count 条，不属于本次交付，也不算缺陷）。"
    }
    $intro = if ($SmallInput) {
        "【小规模需求抽取批 $Index/$Total】文档《$DocumentName》已作为附件导入主澄清会话并完成解析；下面是该小输入文档的真实原文片段（不是完整 SUT 需求文档）。"
    } else {
        "【SUT 需求抽取批 $Index/$Total】文档《$DocumentName》已作为附件导入主澄清会话并完成解析；下面是同一文档的真实原文片段。"
    }
    return @"
$intro
请立即把下列编号逐条创建为 draft 需求，不要提问、不要确认、不要调用 attachment.*/sut.* 工具，也不要先读取需求列表。
规则：
1. 每个编号调用一次 requirement.draft_create，不得遗漏、不得重复、不得创建未列出的编号。
2. title 必须以编号开头，格式「<编号> <原文标题>」。
3. description 用 2-5 句概括该条「需求描述」；acceptance_criteria 保留 Given/When/Then 验收场景（至少一个场景）。
4. priority 必须与原文一致（P0/P1/P2/P3）。
$scopeRule
本次编号（共 $($ids.Count) 条）：$($ids -join '、')
--- 文档原文开始 ---
$raw
--- 文档原文结束 ---
"@
}

function Get-BatchRequirements {
    param($Context, [string] $VersionId)
    return @(Get-VersionRequirements -Context $Context -VersionId $VersionId -Limit 200)
}

function Get-SutIdMap {
    param($Requirements)
    $map = @{}
    $duplicates = @()
    foreach ($requirement in @($Requirements)) {
        $id = Get-RequirementSutId -Requirement $requirement
        if ($id -eq "") { continue }
        if ($map.ContainsKey($id)) { $duplicates += $id } else { $map[$id] = $requirement }
    }
    return [pscustomobject]@{ Map = $map; Duplicates = @($duplicates) }
}

$context = Initialize-AcceptanceContext -ApiBaseUrl $ApiBaseUrl -WebBaseUrl $WebBaseUrl `
    -ExecutorUrl $ExecutorUrl -SutWebBaseUrl $SutWebBaseUrl -SutApiBaseUrl $SutApiBaseUrl `
    -AdminUsername $AdminUsername -AdminPassword $AdminPassword -SutPassword $SutPassword `
    -EvidenceRoot $EvidenceRoot
$scenario = New-AcceptanceScenario -Context $context -Name "s2" -Title "SUT 需求文档导入与需求门 (S2)"

$runSuffix = (Get-Date).ToString("yyyyMMdd-HHmmss")
$exitCode = 2
$versionId = ""
$primaryConversationId = ""
$convergenceConversationId = ""
$baselineEntries = @()
$batchResults = @()
$primaryRecords = @()
$convergenceRecords = @()
$batchQuestionRecords = @()

# Task 88: resolve exactly one requirement source before any network call. A
# missing source or a count mismatch blocks S2 instead of importing the full
# SUT document by default.
$requirementProfile = $null
$inputProfileError = ""
try {
    $requirementProfile = Resolve-AcceptanceRequirementProfile -RequirementInput $RequirementInput `
        -ExpectedRequirements $ExpectedRequirements -ExpectedFrIds $ExpectedFrIds `
        -FullSutDoc:$FullSutDoc -FullSutDocPath $FullSutDocPath `
        -LegacyDocumentPath $RequirementDocumentPath -RepoRoot $context.RepoRoot
} catch {
    $inputProfileError = $_.Exception.Message
}
$isSmallInput = ($null -ne $requirementProfile -and $requirementProfile.Mode -eq "small-input")

function Write-S2Evidence {
    param([string] $Name, $Value, [string] $Kind = "api", [string] $Note = "")
    $path = Join-Path $scenario.Directory $Name
    Write-EvidenceJson -Path $path -Value $Value
    Add-AcceptanceArtifact -Scenario $scenario -Path $path -Kind $Kind -Note $Note
    return $path
}

try {
    $inputModeLabel = if ($null -ne $requirementProfile) { $requirementProfile.Mode } else { "unresolved" }
    $inputPathLabel = if ($null -ne $requirementProfile) { $requirementProfile.Path } else { $RequirementInput }
    Write-AcceptanceLog -Scenario $scenario -Message "S2 requirement import; mode=$inputModeLabel input=$inputPathLabel expected=$ExpectedRequirements batchEntries=$ImportMaxEntriesPerBatch batchChars=$ImportMaxCharsPerBatch batchTimeout=${ImportBatchTimeoutSeconds}s"

    if ($inputProfileError -ne "") {
        Add-AcceptanceCheck -Scenario $scenario -Name "requirement input profile resolved" -Passed $false -Severity blocker -Detail $inputProfileError
        $scenario.ErrorMessage = "S2 blocked: $inputProfileError"
    } else {
        Add-AcceptanceCheck -Scenario $scenario -Name "requirement input profile resolved" -Passed $true -Severity blocker `
            -Detail ("mode=$($requirementProfile.Mode) path=$($requirementProfile.Path) sha256=$($requirementProfile.Sha256) expected=$($requirementProfile.ExpectedRequirements) ids=$(@($requirementProfile.ExpectedFrIds) -join ',')")

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
                $title = "墨栈博客 0.2.1 验收 · S2 SUT 需求导入 · $runSuffix"
                $version = New-AcceptanceVersion -Context $context -Title $title -Binding $bindingResult.Binding
                $versionId = [string](Get-JsonProp -Object $version -Name "id" -Default "")
                Write-S2Evidence -Name "version.json" -Value $version -Note "created version"
                Add-AcceptanceCheck -Scenario $scenario -Name "version created" -Passed ($versionId -ne "") -Severity blocker -Detail "id=$versionId"
            } catch {
                Add-AcceptanceCheck -Scenario $scenario -Name "version created" -Passed $false -Severity blocker -Detail $_.Exception.Message
                $scenario.ErrorMessage = "S2 blocked: version creation failed: $($_.Exception.Message)"
            }

            if ($versionId -ne "") {
                # ------------------------------------------------------------------
                # Requirement baseline: the resolved profile is the only source.
                # Small input -> only its 3 requirements; -FullSutDoc -> the
                # explicitly requested legacy full document. No implicit 67.
                # ------------------------------------------------------------------
                $baselineError = ""
                $baselineEntries = @($requirementProfile.Entries)
                $v1Entries = @($baselineEntries | Where-Object { [string]$_.version -eq "V1" })
                $v2Entries = @($baselineEntries | Where-Object { [string]$_.version -eq "V2" })
                $v1Fr = @($v1Entries | Where-Object { [string]$_.id -like "FR-0*" })
                $nfr = @($v1Entries | Where-Object { [string]$_.id -like "NFR-*" })
                $p0Entries = @($v1Entries | Where-Object { [string]$_.priority -eq "P0" })
                $documentPathFull = $requirementProfile.Path
                Write-S2Evidence -Name "document-baseline.json" -Value ([pscustomobject]@{
                    path                   = $documentPathFull
                    mode                   = $requirementProfile.Mode
                    format                 = $requirementProfile.Format
                    sha256                 = $requirementProfile.Sha256
                    expected_requirements  = $requirementProfile.ExpectedRequirements
                    expected_fr_ids        = @($requirementProfile.ExpectedFrIds)
                    parsed_at              = (Get-Date).ToUniversalTime().ToString("o")
                    parse_error            = $baselineError
                    total                  = $baselineEntries.Count
                    v1_total               = $v1Entries.Count
                    v1_fr                  = $v1Fr.Count
                    nfr                    = $nfr.Count
                    v2                     = $v2Entries.Count
                    p0                     = $p0Entries.Count
                    entries                = @($baselineEntries | Select-Object id, title, priority, version, version_label, line_start, line_end, char_count)
                    v2_ids                 = @($v2Entries | ForEach-Object { $_.id })
                    p0_ids                 = @($p0Entries | ForEach-Object { $_.id })
                }) -Kind "baseline" -Note "resolved requirement input baseline (small input or explicit full document)"
                Write-S2Evidence -Name "requirement-input.json" -Value ([pscustomobject]@{
                    mode                   = $requirementProfile.Mode
                    input_path             = $documentPathFull
                    input_name             = $requirementProfile.Name
                    format                 = $requirementProfile.Format
                    size                   = $requirementProfile.Size
                    sha256                 = $requirementProfile.Sha256
                    expected_requirements  = $requirementProfile.ExpectedRequirements
                    expected_fr_ids        = @($requirementProfile.ExpectedFrIds)
                    baseline_requirements  = $baselineEntries.Count
                    baseline_fr_ids        = @($v1Entries | ForEach-Object { [string]$_.id })
                    v1_fr                  = $v1Fr.Count
                    nfr                    = $nfr.Count
                    v2                     = $v2Entries.Count
                    p0                     = $p0Entries.Count
                    full_sut_doc_opt_in    = [bool]$FullSutDoc
                }) -Kind "input" -Note "requirement input profile used by S2 (path/sha/count/FR IDs)"
                if ($isSmallInput) {
                    Add-AcceptanceCheck -Scenario $scenario -Name "small requirement input parsed (expected $($requirementProfile.ExpectedRequirements) requirements)" `
                        -Passed ($baselineEntries.Count -eq $requirementProfile.ExpectedRequirements -and $v1Entries.Count -eq $requirementProfile.ExpectedRequirements) -Severity assertion `
                        -Detail ("input=$($requirementProfile.Name) expected=$($requirementProfile.ExpectedRequirements) parsed=$($baselineEntries.Count) ids=$(@($requirementProfile.ExpectedFrIds) -join ',')")
                } else {
                    Add-AcceptanceCheck -Scenario $scenario -Name "full SUT requirement document parsed (V1 $($v1Entries.Count) requirements: $($v1Fr.Count) FR + $($nfr.Count) NFR; V2 $($v2Entries.Count))" `
                        -Passed ($baselineEntries.Count -gt 0 -and $v1Entries.Count -gt 0 -and $v1Fr.Count -gt 0) -Severity assertion `
                        -Detail ("mode=$($requirementProfile.Mode) v1=$($v1Entries.Count) fr=$($v1Fr.Count) nfr=$($nfr.Count) v2=$($v2Entries.Count) p0=$($p0Entries.Count) total=$($baselineEntries.Count)")
                }
                if ($baselineEntries.Count -eq 0) {
                    $scenario.ErrorMessage = "S2 blocked: requirement input produced no baseline entries: $documentPathFull"
                }

                if ($baselineEntries.Count -gt 0) {
                    $primaryConversation = $null
                    try {
                        $primaryTitle = if ($isSmallInput) { "S2 小输入需求澄清（附件导入）" } else { "S2 SUT 需求澄清（附件导入）" }
                        $primaryConversation = New-AcceptanceConversation -Context $context -VersionId $versionId -Kind "clarification" -Title $primaryTitle
                        $primaryConversationId = [string](Get-JsonProp -Object $primaryConversation -Name "id" -Default "")
                        Add-AcceptanceCheck -Scenario $scenario -Name "clarification conversation created" -Passed ($primaryConversationId -ne "") -Severity blocker -Detail "conversation=$primaryConversationId"
                    } catch {
                        Add-AcceptanceCheck -Scenario $scenario -Name "clarification conversation created" -Passed $false -Severity blocker -Detail $_.Exception.Message
                        $scenario.ErrorMessage = "S2 blocked: conversation creation failed: $($_.Exception.Message)"
                    }

                    # ------------------------------------------------------------------
                    # Upload and attach the real document (not a path reference).
                    # ------------------------------------------------------------------
                    $upload = $null
                    $attachmentObject = $null
                    $documentSha = ""
                    try {
                        $documentSha = Get-AcceptanceFileSha256 -Path $documentPathFull
                        $upload = New-AcceptanceContentUpload -Context $context -Path $documentPathFull
                        $attachmentObject = $upload.Data
                        $uploadSha = [string](Get-JsonProp -Object $attachmentObject -Name "sha256" -Default "")
                        $uploadSize = [long](Get-JsonProp -Object $attachmentObject -Name "size" -Default 0)
                        $uploadMime = [string](Get-JsonProp -Object $attachmentObject -Name "mime" -Default "")
                        $attachmentMime = if ($requirementProfile.Extension -eq ".feature") { "text/plain" } else { $uploadMime }
                        Write-S2Evidence -Name "attachment-upload.json" -Value ([pscustomobject]@{
                            source_path            = $documentPathFull
                            mode                   = $requirementProfile.Mode
                            name                   = $upload.Name
                            http_status            = $upload.Status
                            ok                     = $upload.Ok
                            sha256                 = $uploadSha
                            local_sha256           = $documentSha
                            size                   = $uploadSize
                            server_mime            = $uploadMime
                            message_attachment_mime = $attachmentMime
                            expected_requirements  = $requirementProfile.ExpectedRequirements
                            expected_fr_ids        = @($requirementProfile.ExpectedFrIds)
                        }) -Kind "api" -Note "POST /api/content upload of the resolved requirement input"
                        Add-AcceptanceCheck -Scenario $scenario -Name "requirement input uploaded to content ledger" -Passed ($upload.Ok -and $uploadSha -ne "") -Severity blocker `
                            -Detail ("HTTP $($upload.Status) sha=$uploadSha size=$uploadSize mime=$uploadMime")
                        Add-AcceptanceCheck -Scenario $scenario -Name "uploaded content sha256 matches the local requirement input" -Passed ($uploadSha -ne "" -and $uploadSha -eq $documentSha) -Severity assertion `
                            -Detail "upload=$uploadSha local=$documentSha"
                    } catch {
                        Add-AcceptanceCheck -Scenario $scenario -Name "requirement input uploaded to content ledger" -Passed $false -Severity blocker -Detail $_.Exception.Message
                        $scenario.ErrorMessage = "S2 blocked: requirement input upload failed: $($_.Exception.Message)"
                    }

                    if ($null -ne $attachmentObject -and $primaryConversationId -ne "") {
                        $baselineText = if ($isSmallInput) {
                            "【验收基线】附件《$($upload.Name)》是本次验收的小规模需求输入（$($requirementProfile.Format)），恰好包含 $($requirementProfile.ExpectedRequirements) 条需求：$(@($requirementProfile.ExpectedFrIds) -join '、')。它不是完整需求文档，不包含 V2 延展需求。"
                        } else {
                            "【验收基线】附件《$($upload.Name)》是本次显式选择的全量需求文档：V1 共 $($v1Entries.Count) 条（$($v1Fr.Count) 条 FR + $($nfr.Count) 条 NFR），V2 $($v2Entries.Count) 条为延展·未实现（不创建需求、不判定为缺陷）。"
                        }
                        $initialText = @"
【版本描述】墨栈博客：Vue 前端 + Go 后端博客系统，SUT 前端 $($context.SutWebBaseUrl)，后端 API $($context.SutApiBaseUrl)/api/v1。
$baselineText
【本轮要求】请确认附件已解析（只读取前 500 字符即可，不要通读全文），用一句话给出摘要，并最多提出 3 个关于抽取口径的澄清问题（可给默认假设）。本轮不要创建需求，等待用户确认口径后分批提交原文抽取。
"@
                        $attachment = [pscustomobject]@{
                            sha256 = [string](Get-JsonProp -Object $attachmentObject -Name "sha256" -Default "")
                            name   = [string]$upload.Name
                            mime   = $attachmentMime
                            size   = [long](Get-JsonProp -Object $attachmentObject -Name "size" -Default 0)
                        }
                        $initialMessage = $null
                        try {
                            $initialMessage = New-AcceptanceUserMessage -Context $context -VersionId $versionId -ConversationId $primaryConversationId -Text $initialText -Attachment $attachment -PendingRetrySeconds 180
                            $initialMessageId = [string](Get-JsonProp -Object $initialMessage -Name "id" -Default "")
                            Write-S2Evidence -Name "initial-message.json" -Value $initialMessage -Note "clarification message with the document attachment"
                            $parts = @(Get-JsonProp -Object $initialMessage -Name "parts" -Default @())
                            $attachmentParts = @($parts | Where-Object { [string](Get-JsonProp -Object $_ -Name "type" -Default "") -eq "attachment" })
                            Add-AcceptanceCheck -Scenario $scenario -Name "clarification message accepted with the document attachment" `
                                -Passed ($initialMessageId -ne "" -and $attachmentParts.Count -ge 1) -Severity blocker `
                                -Detail ("message=$initialMessageId attachmentParts=$($attachmentParts.Count)")
                        } catch {
                            Add-AcceptanceCheck -Scenario $scenario -Name "clarification message accepted with the document attachment" -Passed $false -Severity blocker -Detail $_.Exception.Message
                            $scenario.ErrorMessage = "S2 blocked: attachment message could not be posted: $($_.Exception.Message)"
                        }

                        # Attachment parse state: the server must have extracted
                        # the text; the script never substitutes a path reference.
                        $attachmentProjection = $null
                        if ($null -ne $initialMessage) {
                            $attachmentProjection = Get-SutAttachmentProjection -Context $context -VersionId $versionId
                            Write-S2Evidence -Name "attachment-parse.json" -Value $attachmentProjection -Kind "db" -Note "requirement_attachment parse state and extracted content object"
                            $attachmentRow = $null
                            if (@($attachmentProjection.Rows).Count -gt 0) { $attachmentRow = @($attachmentProjection.Rows)[0] }
                            $parseDone = ($null -ne $attachmentRow -and $attachmentRow.extraction_status -eq "done" -and $attachmentRow.extraction_error -eq "")
                            $extractedSize = 0
                            $extractedSha = ""
                            $extractedPath = ""
                            if ($null -ne $attachmentProjection.Content) {
                                $extractedSize = [long]$attachmentProjection.Content.size
                                $extractedSha = [string]$attachmentProjection.Content.sha256
                                $extractedPath = [string]$attachmentProjection.Content.relative_path
                            }
                            $markerIds = @($requirementProfile.ExpectedFrIds)
                            $v2Required = @($v2Entries | ForEach-Object { [string]$_.id })
                            $extractedText = ""
                            $extractedIds = @()
                            $fileLength = 0
                            if ($extractedPath -ne "") {
                                # content_object.relative_path is relative to the
                                # API data root (paths.data_root, default ./.data
                                # under the backend working directory). Try the
                                # real data root first, then two path fallbacks.
                                $dataRoot = Join-Path (Join-Path $context.RepoRoot "backend") ".data"
                                $pathCandidates = @(
                                    (Join-Path $dataRoot $extractedPath),
                                    (Join-Path (Join-Path $context.RepoRoot "backend") $extractedPath),
                                    (Join-Path $context.RepoRoot $extractedPath)
                                )
                                $candidate = $pathCandidates | Where-Object { Test-Path -LiteralPath $_ } | Select-Object -First 1
                                if ($null -ne $candidate) {
                                    $extractedText = [System.IO.File]::ReadAllText($candidate, [System.Text.Encoding]::UTF8)
                                    $fileLength = $extractedText.Length
                                    $extractedIds = @(Get-AcceptanceFrIdsFromText -Text $extractedText -Format "text")
                                }
                            }
                            $missingMarkers = @($markerIds | Where-Object { $extractedIds -notcontains $_ })
                            $unexpectedIds = if ($isSmallInput) {
                                @($extractedIds | Where-Object { $markerIds -notcontains $_ })
                            } else { @() }
                            $v2Missing = @($v2Required | Where-Object { $extractedIds -notcontains $_ })
                            $markers = [pscustomobject]@{
                                mode                 = $requirementProfile.Mode
                                expected_count       = $markerIds.Count
                                expected_ids         = @($markerIds)
                                extracted_count      = $extractedIds.Count
                                extracted_ids        = @($extractedIds)
                                missing_ids          = @($missingMarkers)
                                unexpected_ids       = @($unexpectedIds)
                                v2_required          = @($v2Required)
                                v2_missing           = @($v2Missing)
                            }
                            $extractedMinBytes = if ($isSmallInput) { 1 } else { 100000 }
                            Write-S2Evidence -Name "attachment-extracted-check.json" -Value ([pscustomobject]@{
                                attachment            = $attachmentRow
                                content               = $attachmentProjection.Content
                                extracted_file_length = $fileLength
                                markers               = $markers
                            }) -Kind "db" -Note "extracted attachment text content check"
                            Add-AcceptanceCheck -Scenario $scenario -Name "attachment extraction completed (status=done)" `
                                -Passed ($parseDone -and $extractedSize -ge $extractedMinBytes) -Severity assertion `
                                -Detail ("status=$(if ($null -ne $attachmentRow) { $attachmentRow.extraction_status } else { 'missing' }) extracted_bytes=$extractedSize message_link=$(if ($null -ne $attachmentRow) { $attachmentRow.conversation_message_id } else { 0 }) mode=$($requirementProfile.Mode)")
                            $markerPassed = ($markerIds.Count -gt 0 -and $missingMarkers.Count -eq 0)
                            if ($isSmallInput) {
                                $markerPassed = $markerPassed -and $unexpectedIds.Count -eq 0
                            } else {
                                $markerPassed = $markerPassed -and ($v2Required.Count -eq 0 -or $v2Missing.Count -eq 0)
                            }
                            Add-AcceptanceCheck -Scenario $scenario -Name "extracted requirement input contains exactly the expected FR/NFR IDs" `
                                -Passed $markerPassed -Severity assertion `
                                -Detail ("expected=$($markerIds.Count) extracted=$($extractedIds.Count) missing=$(@($missingMarkers) -join ',') unexpected=$(@($unexpectedIds) -join ',') v2_missing=$(@($v2Missing) -join ',') file_length=$fileLength")
                        }

                        # ------------------------------------------------------------------
                        # One real clarification answer turn (the host's question is
                        # answered with the extraction defaults), then batch extraction.
                        # ------------------------------------------------------------------
                        if ($null -ne $initialMessage) {
                            $initialMessageId = [string](Get-JsonProp -Object $initialMessage -Name "id" -Default "")
                            $firstReply = Wait-NewAssistantText -Context $context -VersionId $versionId -ConversationId $primaryConversationId `
                                -AfterMessageId $initialMessageId -TimeoutSeconds $ReplyTimeoutSeconds
                            if ($firstReply.Ok -and $null -ne $firstReply.Value) {
                                $record = @(Get-ConversationAssistantRecords -Context $context -VersionId $versionId -ConversationId $primaryConversationId | Where-Object { $_.message_id -eq [string](Get-JsonProp -Object $firstReply.Value -Name "id" -Default "") })
                                $primaryRecords += $record
                            }
                            $answerText = if ($isSmallInput) {
                                "抽取口径默认假设：本次只处理小规模需求输入中的 $($requirementProfile.ExpectedRequirements) 条需求（$(@($requirementProfile.ExpectedFrIds) -join '、')），没有其它编号，也没有 V2 延展需求。每条需求一个 requirement，title 以编号开头，description 概括需求描述，acceptance_criteria 保留 Given/When/Then，priority 按原文。无需再提问；我现在分批提交原文，由需求 Curator 直接创建 draft，全部保持 draft 等待确认。"
                            } else {
                                "抽取口径默认假设：范围=V1 $($v1Entries.Count) 条需求（$($v1Fr.Count) 条 FR + $($nfr.Count) 条 NFR）；V2 $($v2Entries.Count) 条为延展·未实现，只记录为未交付，不创建需求、不算缺陷。每条需求一个 requirement，title 以编号开头，description 概括需求描述，acceptance_criteria 保留 Given/When/Then，priority 按原文。无需再提问；我现在分批提交原文，由需求 Curator 直接创建 draft，全部保持 draft 等待确认。"
                            }
                            $answerMessage = New-AcceptanceUserMessage -Context $context -VersionId $versionId -ConversationId $primaryConversationId -Text $answerText
                            $answerMessageId = [string](Get-JsonProp -Object $answerMessage -Name "id" -Default "")
                            $secondReply = Wait-NewAssistantText -Context $context -VersionId $versionId -ConversationId $primaryConversationId `
                                -AfterMessageId $answerMessageId -TimeoutSeconds $ReplyTimeoutSeconds
                            if ($secondReply.Ok -and $null -ne $secondReply.Value) {
                                $record = @(Get-ConversationAssistantRecords -Context $context -VersionId $versionId -ConversationId $primaryConversationId | Where-Object { $_.message_id -eq [string](Get-JsonProp -Object $secondReply.Value -Name "id" -Default "") })
                                $primaryRecords += $record
                            }
                            Write-AcceptanceLog -Scenario $scenario -Message "primary clarification turns posted; starting document batches"
                        }

                        # ------------------------------------------------------------------
                        # Batch extraction: each batch is a fresh short clarification
                        # conversation carrying a bounded, verbatim document slice.
                        # Keeping batch conversations separate keeps the curator's
                        # transcript small enough for its real per-run token budget.
                        # Group directly by entry count AND character budget so no
                        # tiny remainder batch is produced.
                        # ------------------------------------------------------------------
                        $extractionEntries = @($requirementProfile.ExtractionEntries)
                        $batches = @()
                        $currentBatch = @()
                        $currentChars = 0
                        foreach ($entry in $extractionEntries) {
                            $entryChars = [int]$entry.char_count
                            if ($currentBatch.Count -gt 0 -and
                                (($currentBatch.Count -ge $ImportMaxEntriesPerBatch) -or
                                 (($currentChars + $entryChars) -gt $ImportMaxCharsPerBatch))) {
                                $batches += , @($currentBatch)
                                $currentBatch = @()
                                $currentChars = 0
                            }
                            $currentBatch += $entry
                            $currentChars += $entryChars
                        }
                        if ($currentBatch.Count -gt 0) { $batches += , @($currentBatch) }
                        if ($ImportMaxBatches -gt 0 -and $batches.Count -gt $ImportMaxBatches) {
                            $batches = @($batches | Select-Object -First $ImportMaxBatches)
                        }
                        Write-AcceptanceLog -Scenario $scenario -Message "document batches=$($batches.Count) entries=$(($batches | ForEach-Object { @($_).Count }) -join ',')"

                        $consecutiveEmpty = 0
                        for ($batchIndex = 0; $batchIndex -lt $batches.Count; $batchIndex++) {
                            $entries = @($batches[$batchIndex])
                            $expectedIds = @()
                            foreach ($entry in $entries) {
                                $property = $entry.PSObject.Properties["expected_ids"]
                                if ($null -ne $property -and @($property.Value).Count -gt 0) {
                                    $expectedIds += @($property.Value)
                                } elseif ([string]$entry.id -ne "") {
                                    $expectedIds += [string]$entry.id
                                }
                            }
                            $expectedIds = @($expectedIds | Where-Object { $_ -ne "" } | Select-Object -Unique)
                            if ($expectedIds.Count -eq 0) { $expectedIds = @($requirementProfile.ExpectedFrIds) }
                            $batchNumber = $batchIndex + 1
                            $batchStarted = Get-Date
                            $batchConversation = $null
                            $batchConversationId = ""
                            $batchMessageId = ""
                            $batchQuestions = @()
                            $batchError = ""
                            try {
                                $batchTitle = if ($isSmallInput) { "S2 小输入需求抽取批 $batchNumber/$($batches.Count)" } else { "S2 SUT 需求抽取批 $batchNumber/$($batches.Count)" }
                                $batchConversation = New-AcceptanceConversation -Context $context -VersionId $versionId -Kind "clarification" -Title $batchTitle
                                $batchConversationId = [string](Get-JsonProp -Object $batchConversation -Name "id" -Default "")
                            } catch {
                                $batchError = $_.Exception.Message
                                Write-AcceptanceLog -Scenario $scenario -Level warn -Message "batch $batchNumber conversation failed: $batchError"
                            }

                            $missing = @($expectedIds)
                            $foundIds = @()
                            if ($batchConversationId -ne "") {
                                $prompt = New-SutBatchPrompt -DocumentName $upload.Name -Index $batchNumber -Total $batches.Count -Entries $entries `
                                    -ExpectedIds $expectedIds -SmallInput:$isSmallInput -V2Count $v2Entries.Count
                                try {
                                    $batchMessage = New-AcceptanceUserMessage -Context $context -VersionId $versionId -ConversationId $batchConversationId -Text $prompt
                                    $batchMessageId = [string](Get-JsonProp -Object $batchMessage -Name "id" -Default "")
                                } catch {
                                    $batchError = $_.Exception.Message
                                    Write-AcceptanceLog -Scenario $scenario -Level warn -Message "batch $batchNumber message failed: $batchError"
                                }
                                if ($batchMessageId -ne "") {
                                    $deadline = (Get-Date).AddSeconds([Math]::Max(30, $ImportBatchTimeoutSeconds))
                                    while ((Get-Date) -lt $deadline) {
                                        Start-Sleep -Seconds 5
                                        $currentRequirements = @(Get-BatchRequirements -Context $context -VersionId $versionId)
                                        $idMap = Get-SutIdMap -Requirements $currentRequirements
                                        $missing = @($expectedIds | Where-Object { -not $idMap.Map.ContainsKey($_) })
                                        if ($missing.Count -eq 0) { break }
                                    }
                                    if ($missing.Count -gt 0) {
                                        # One repair turn in the same batch conversation:
                                        # the raw source is still in the thread; create
                                        # only the missing IDs, never duplicates.
                                        try {
                                            $repair = "上一批缺少以下编号：$($missing -join '、')。请只创建这些编号，每个一次，不要重复已存在的编号，不要提问、不要确认；完成后一句话回复。"
                                            [void](New-AcceptanceUserMessage -Context $context -VersionId $versionId -ConversationId $batchConversationId -Text $repair)
                                            $repairDeadline = (Get-Date).AddSeconds([Math]::Max(30, [int]($ImportBatchTimeoutSeconds / 2)))
                                            while ((Get-Date) -lt $repairDeadline) {
                                                Start-Sleep -Seconds 5
                                                $currentRequirements = @(Get-BatchRequirements -Context $context -VersionId $versionId)
                                                $idMap = Get-SutIdMap -Requirements $currentRequirements
                                                $missing = @($expectedIds | Where-Object { -not $idMap.Map.ContainsKey($_) })
                                                if ($missing.Count -eq 0) { break }
                                            }
                                        } catch {
                                            Write-AcceptanceLog -Scenario $scenario -Level warn -Message "batch $batchNumber repair failed: $($_.Exception.Message)"
                                        }
                                    }
                                }
                            }

                            $finalRequirements = @(Get-BatchRequirements -Context $context -VersionId $versionId)
                            $finalMap = Get-SutIdMap -Requirements $finalRequirements
                            $foundIds = @($expectedIds | Where-Object { $finalMap.Map.ContainsKey($_) })
                            $missing = @($expectedIds | Where-Object { -not $finalMap.Map.ContainsKey($_) })
                            $batchRequirementIds = @($foundIds | ForEach-Object { [string](Get-JsonProp -Object $finalMap.Map[$_] -Name "id" -Default "") })
                            $batchQuestions = if ($batchConversationId -ne "") { @(Get-ConversationAssistantRecords -Context $context -VersionId $versionId -ConversationId $batchConversationId | ForEach-Object { $_.questions } | ForEach-Object { $_ }) } else { @() }
                            $batchQuestionRecords += [pscustomobject]@{ batch = $batchNumber; conversation_id = $batchConversationId; questions = @($batchQuestions) }
                            $runStats = @()
                            if ($batchConversationId -ne "") {
                                $statsQuery = Invoke-AcceptanceDbQuery -Context $context -VersionId ([long]$versionId) `
                                    -Sql "SELECT profile_key, status, turns, coalesce(extract(epoch from (finished_at-started_at)),0), coalesce(tokens::text,'') FROM agent_run WHERE conversation_id = $batchConversationId ORDER BY id"
                                $runStats = @($statsQuery.Rows)
                            }
                            $batchResults += [pscustomobject]@{
                                batch                  = $batchNumber
                                conversation_id        = $batchConversationId
                                message_id             = $batchMessageId
                                expected               = @($expectedIds)
                                found                  = @($foundIds)
                                missing                = @($missing)
                                requirement_ids        = $batchRequirementIds
                                duration_seconds       = [Math]::Round(((Get-Date) - $batchStarted).TotalSeconds, 1)
                                error                  = $batchError
                                agent_runs             = @($runStats)
                            }
                            Write-AcceptanceLog -Scenario $scenario -Message ("batch {0}/{1} entries={2} found={3} missing={4} duration={5}s" -f $batchNumber, $batches.Count, $expectedIds.Count, $foundIds.Count, $missing.Count, [Math]::Round(((Get-Date) - $batchStarted).TotalSeconds, 1))
                            if ($foundIds.Count -eq 0) { $consecutiveEmpty++ } else { $consecutiveEmpty = 0 }
                            if ($consecutiveEmpty -ge [Math]::Max(1, $ImportConsecutiveEmptyBatchStop)) {
                                Write-AcceptanceLog -Scenario $scenario -Level warn -Message "stopping extraction after $consecutiveEmpty consecutive empty batches"
                                break
                            }
                        }
                        Write-S2Evidence -Name "import-batches.json" -Value ([pscustomobject]@{
                            document               = $documentPathFull
                            mode                   = $requirementProfile.Mode
                            expected_requirements  = $requirementProfile.ExpectedRequirements
                            expected_fr_ids        = @($requirementProfile.ExpectedFrIds)
                            batches                = @($batchResults)
                            batch_questions        = @($batchQuestionRecords)
                        }) -Kind "api" -Note "requirement input extraction batches (real input slices; small input or explicit full document)"
                        Write-S2Evidence -Name "batch-questions.json" -Value @($batchQuestionRecords) -Kind "api" -Note "questions asked by the batch extraction conversations (not the user clarification scope)"
                        Add-AcceptanceCheck -Scenario $scenario -Name "document extraction batches executed" -Passed ($batchResults.Count -ge 1) -Severity assertion `
                            -Detail "batches=$($batchResults.Count) entries=$((($batchResults | ForEach-Object { $_.expected.Count }) | Measure-Object -Sum).Sum) found=$((($batchResults | ForEach-Object { $_.found.Count }) | Measure-Object -Sum).Sum)"

                        # Let in-flight curator runs finish before computing the
                        # final coverage. This is a DB projection only.
                        $idleDeadline = (Get-Date).AddSeconds(120)
                        while ((Get-Date) -lt $idleDeadline) {
                            $idleQuery = Invoke-AcceptanceDbQuery -Context $context -VersionId ([long]$versionId) `
                                -Sql "SELECT count(*) FROM work_item WHERE version_id = $versionId AND type IN ('requirement.curate','chat.turn') AND status NOT IN ('done','failed','canceled','cancelled','dead')"
                            $active = 0
                            if ($idleQuery.Ok -and @($idleQuery.Rows).Count -gt 0) { $active = [int]([string]@($idleQuery.Rows)[0]) }
                            if ($active -eq 0) { break }
                            Start-Sleep -Seconds 5
                        }

                        # ------------------------------------------------------------------
                        # Requirement confirmation gate: one short conversation asks the
                        # Host for the explicit requirement confirmation request.
                        # ------------------------------------------------------------------
                        $convergenceConversation = $null
                        try {
                            $convergenceConversation = New-AcceptanceConversation -Context $context -VersionId $versionId -Kind "clarification" -Title "S2 需求确认门"
                            $convergenceConversationId = [string](Get-JsonProp -Object $convergenceConversation -Name "id" -Default "")
                        } catch {
                            Write-AcceptanceLog -Scenario $scenario -Level warn -Message "convergence conversation failed: $($_.Exception.Message)"
                        }
                        $stageValue = ""
                        if ($convergenceConversationId -ne "") {
                            $convergenceText = if ($isSmallInput) {
                                "小规模需求输入中的 $($requirementProfile.ExpectedRequirements) 条需求（$(@($requirementProfile.ExpectedFrIds) -join '、')）已全部抽取到需求列表并保持 draft；没有 V2 延展需求。请现在在回复末尾明确给出「需求确认请求」，停在需求确认门等待用户确认；不要开始设计、不要创建用例、不要再提问。"
                            } else {
                                "V1 $($v1Entries.Count) 条需求（$($v1Fr.Count) 条 FR + $($nfr.Count) 条 NFR）已全部抽取到需求列表并保持 draft；V2 $($v2Entries.Count) 条为延展·未实现，不创建需求、不算缺陷。请现在在回复末尾明确给出「需求确认请求」，停在需求确认门等待用户确认；不要开始设计、不要创建用例、不要再提问。"
                            }
                            try {
                                $convergenceMessage = New-AcceptanceUserMessage -Context $context -VersionId $versionId -ConversationId $convergenceConversationId -Text $convergenceText
                                $convergenceMessageId = [string](Get-JsonProp -Object $convergenceMessage -Name "id" -Default "")
                                [void](Wait-NewAssistantText -Context $context -VersionId $versionId -ConversationId $convergenceConversationId -AfterMessageId $convergenceMessageId -TimeoutSeconds $ReplyTimeoutSeconds)
                            } catch {
                                Write-AcceptanceLog -Scenario $scenario -Level warn -Message "convergence message failed: $($_.Exception.Message)"
                            }
                            $stageWait = Wait-Until -TimeoutSeconds ([Math]::Max(60, $ReplyTimeoutSeconds)) -IntervalSeconds 3 -Description "requirement_review stage" -Condition {
                                try { $current = Get-VersionStage -Context $context -VersionId $versionId } catch { return $null }
                                if ([string](Get-JsonProp -Object $current -Name "stage" -Default "") -eq "requirement_review") { return $current }
                                return $null
                            }
                            if (-not $stageWait.Ok) {
                                try {
                                    $nudge = New-AcceptanceUserMessage -Context $context -VersionId $versionId -ConversationId $convergenceConversationId -Text "请在回复末尾明确写出「需求确认请求」，并停在需求确认门等待用户确认；不要开始设计、不要创建用例。"
                                    [void](Wait-NewAssistantText -Context $context -VersionId $versionId -ConversationId $convergenceConversationId -AfterMessageId ([string](Get-JsonProp -Object $nudge -Name "id" -Default "")) -TimeoutSeconds $ReplyTimeoutSeconds)
                                } catch { }
                                $stageWait = Wait-Until -TimeoutSeconds 120 -IntervalSeconds 3 -Description "requirement_review stage after nudge" -Condition {
                                    try { $current = Get-VersionStage -Context $context -VersionId $versionId } catch { return $null }
                                    if ([string](Get-JsonProp -Object $current -Name "stage" -Default "") -eq "requirement_review") { return $current }
                                    return $null
                                }
                            }
                            $convergenceRecords = @(Get-ConversationAssistantRecords -Context $context -VersionId $versionId -ConversationId $convergenceConversationId)
                        }

                        # ------------------------------------------------------------------
                        # Final state and Task 72 traceability assertions.
                        # ------------------------------------------------------------------
                        $requirements = @(Get-BatchRequirements -Context $context -VersionId $versionId)
                        $stage = $null
                        try { $stage = Get-VersionStage -Context $context -VersionId $versionId } catch { }
                        $currentVersion = $null
                        try { $currentVersion = Get-Version -Context $context -VersionId $versionId } catch { }
                        $workItems = @()
                        try { $workItems = @(Get-VersionWorkItems -Context $context -VersionId $versionId) } catch { }
                        $conversations = @()
                        try { $conversations = @(Get-VersionConversations -Context $context -VersionId $versionId) } catch { }
                        $coverage = Get-SutRequirementCoverage -Entries $baselineEntries -Requirements $requirements -Version "V1"
                        $v2Coverage = Get-SutRequirementCoverage -Entries $baselineEntries -Requirements $requirements -Version "V2"
                        $frCovered = @($coverage.Rows | Where-Object { $_.sut_id -like "FR-0*" -and $_.covered }).Count
                        $frTotal = @($coverage.Rows | Where-Object { $_.sut_id -like "FR-0*" }).Count
                        $v2Created = @($v2Coverage.Rows | Where-Object { $_.covered })
                        $untraceableRequirements = @($requirements | Where-Object { (Get-RequirementSutId -Requirement $_) -eq "" })
                        $duplicates = @($coverage.DuplicateIds)
                        $nfrTotal = @($coverage.Rows | Where-Object { $_.sut_id -like "NFR-*" }).Count
                        $nfrCovered = @($coverage.Rows | Where-Object { $_.sut_id -like "NFR-*" -and $_.covered }).Count
                        $setCheck = $null
                        if ($isSmallInput) {
                            $setCheck = Compare-AcceptanceRequirementSet -ExpectedFrIds (@($requirementProfile.ExpectedFrIds) -join ",") `
                                -ExpectedRequirements $requirementProfile.ExpectedRequirements -Requirements $requirements
                        }
                        $stageValue = [string](Get-JsonProp -Object $stage -Name "stage" -Default "")
                        $requirementCount = $requirements.Count
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

                        $actualRequirementFrIds = if ($null -ne $setCheck) {
                            @($setCheck.ActualFrIds)
                        } else {
                            @($requirements | ForEach-Object { Get-RequirementSutId -Requirement $_ } | Where-Object { $_ -ne "" } | Sort-Object -Unique)
                        }
                        Write-S2Evidence -Name "requirements.json" -Value $requirements -Note "requirement drafts at the gate"
                        Write-S2Evidence -Name "requirements-coverage.json" -Value ([pscustomobject]@{
                            generated_at          = (Get-Date).ToUniversalTime().ToString("o")
                            mode                  = $requirementProfile.Mode
                            input_path            = $documentPathFull
                            input_sha256          = $requirementProfile.Sha256
                            expected_requirements = $requirementProfile.ExpectedRequirements
                            expected_fr_ids       = @($requirementProfile.ExpectedFrIds)
                            actual_requirements   = $requirementCount
                            actual_fr_ids         = @($actualRequirementFrIds)
                            set_check             = $setCheck
                            summary               = [pscustomobject]@{
                                v1_total        = $coverage.Total
                                v1_covered      = $coverage.Covered
                                v1_missing      = @($coverage.Missing)
                                p0_total        = $coverage.P0Total
                                p0_covered      = $coverage.P0Covered
                                p0_missing      = @($coverage.P0Missing)
                                duplicates      = @($duplicates)
                                untraceable     = $untraceableRequirements.Count
                                v2_created      = $v2Created.Count
                                v2_total        = $v2Coverage.Total
                            }
                            rows         = @($coverage.Rows)
                        }) -Kind "coverage" -Note "requirement input -> requirement traceability at the S2 gate"
                        Write-S2Evidence -Name "v2-handling.json" -Value ([pscustomobject]@{
                            v2_total = $v2Coverage.Total
                            v2_created_as_requirements = $v2Created.Count
                            v2_status = "延展·未实现（当前系统不存在，不要求交付，不算缺陷）"
                            rows = @($v2Coverage.Rows)
                        }) -Kind "coverage" -Note "V2 延展·未实现 handling"
                        Write-S2Evidence -Name "stage.json" -Value $stage -Note "final stage"
                        Write-S2Evidence -Name "work-items.json" -Value $workItems -Note "work items at the gate"
                        Write-S2Evidence -Name "conversations.json" -Value $conversations -Note "all version conversations (primary + batches + convergence)"

                        $messages = @()
                        try { $messages = @(Get-ConversationMessages -Context $context -VersionId $versionId -ConversationId $primaryConversationId -Limit 200) } catch { }
                        $messageSummary = @($messages | ForEach-Object {
                            [pscustomobject]@{
                                id = [string](Get-JsonProp -Object $_ -Name "id" -Default "")
                                role = [string](Get-JsonProp -Object $_ -Name "role" -Default "")
                                text = Get-AssistantText -Message $_
                                questions = @(Get-QuestionTexts -Message $_)
                            }
                        })
                        Write-S2Evidence -Name "messages.json" -Value $messageSummary -Kind "api" -Note "primary clarification messages (text only)"

                        $scopedRecords = @($primaryRecords) + @($convergenceRecords)
                        $allQuestions = @()
                        $turnQuestionCounts = @()
                        foreach ($record in $scopedRecords) {
                            $turnQuestions = @($record.questions)
                            $turnQuestionCounts += [pscustomobject]@{ message_id = $record.message_id; questions = $turnQuestions.Count }
                            $allQuestions += $turnQuestions
                        }
                        # The batch extraction conversations are harness sessions
                        # that feed the document slices; they are not product
                        # clarification turns. Measure them separately so the
                        # report states the full volume honestly without applying
                        # the user clarification budget to them.
                        $batchAllQuestions = @()
                        foreach ($record in $batchQuestionRecords) { $batchAllQuestions += @($record.questions) }
                        $questionSummary = Get-AcceptanceQuestionBudgetSummary -UserQuestions $allQuestions -BatchQuestions $batchAllQuestions
                        $uniqueNormalized = [int]$questionSummary.user_unique
                        $duplicatesQuestions = @($questionSummary.user_duplicates)
                        $batchUnique = [int]$questionSummary.batch_unique
                        $allConversationsUnique = [int]$questionSummary.all_unique
                        $budget = $QuestionBudgetPerRequirement * [Math]::Max(1, $requirementCount)
                        Write-S2Evidence -Name "questions.json" -Value ([pscustomobject]@{
                            scope               = "user clarification conversations (primary + convergence); S2 harness extraction batches measured separately"
                            total               = $questionSummary.user_total
                            unique              = $uniqueNormalized
                            duplicates          = $duplicatesQuestions
                            per_turn            = $turnQuestionCounts
                            requirement_count   = $requirementCount
                            budget              = $budget
                            user_clarification_budget = $UserClarificationQuestionBudget
                            batch_conversation_questions = @($batchQuestionRecords | ForEach-Object { [pscustomobject]@{ batch = $_.batch; count = @($_.questions).Count } })
                            batch_questions_total = $questionSummary.batch_total
                            batch_questions_unique = $batchUnique
                            all_conversations_total = $questionSummary.all_total
                            all_conversations_unique = $allConversationsUnique
                            requirement_input_mode = $requirementProfile.Mode
                            requirement_input_path = $documentPathFull
                            expected_requirements  = $requirementProfile.ExpectedRequirements
                            expected_fr_ids        = @($requirementProfile.ExpectedFrIds)
                            actual_requirements    = $requirementCount
                            actual_fr_ids          = @($actualRequirementFrIds)
                            extraction_session_exception = "S2 batch extraction conversations are acceptance-harness sessions that feed the resolved requirement input ($($requirementProfile.Mode), $($requirementProfile.ExpectedRequirements) expected requirements); they are not user clarification turns, so the user clarification budget is not claimed for them. The product version-wide budget remains 6/requirement + $UserClarificationQuestionBudget/version on the clarification conversations."
                        }) -Kind "api" -Note "question budget, duplicates and extraction-session exception"

                        $confirmationAsked = $false
                        foreach ($record in $scopedRecords) {
                            if (Test-RequirementConfirmationRequest -Text $record.text) { $confirmationAsked = $true; break }
                        }
                        $userTurnCount = @($messages | Where-Object { [string](Get-JsonProp -Object $_ -Name "role" -Default "") -eq "user" }).Count +
                            [int]$batchResults.Count + 1
                        Add-AcceptanceCheck -Scenario $scenario -Name "multi-turn clarification exercised" -Passed ($userTurnCount -ge 2) -Severity assertion -Detail "userTurns=$userTurnCount batches=$($batchResults.Count)"

                        Add-AcceptanceCheck -Scenario $scenario -Name "clarification questions asked" -Passed ($uniqueNormalized -gt 0) -Severity assertion `
                            -Detail "uniqueQuestions=$($uniqueNormalized) total=$($allQuestions.Count) turns=$($scopedRecords.Count)"
                        Add-AcceptanceCheck -Scenario $scenario -Name "user clarification question budget per requirement (<= $QuestionBudgetPerRequirement)" -Passed ($uniqueNormalized -le $budget) -Severity assertion `
                            -Detail "uniqueQuestions=$($uniqueNormalized) requirements=$requirementCount budget=$budget scope=primary+convergence"
                        Add-AcceptanceCheck -Scenario $scenario -Name "user clarification question budget (<= $UserClarificationQuestionBudget)" -Passed ($uniqueNormalized -le $UserClarificationQuestionBudget) -Severity assertion `
                            -Detail "uniqueQuestions=$($uniqueNormalized) userBudget=$UserClarificationQuestionBudget scope=primary+convergence (extraction batches excluded by design; see extraction-session observation)"
                        Add-AcceptanceCheck -Scenario $scenario -Name "extraction-session question volume recorded (observation only; not the user clarification budget)" -Passed $true -Severity warning `
                            -Detail "observation only: batchSessions=$($batchQuestionRecords.Count) batchQuestions=$($batchAllQuestions.Count) batchUnique=$($batchUnique) allConversationsUnique=$($allConversationsUnique); exception: the batch conversations are S2 harness extraction sessions, not product clarification turns, so the user clarification budget is not applied to them"
                        Add-AcceptanceCheck -Scenario $scenario -Name "no duplicate clarification questions" -Passed ($duplicatesQuestions.Count -eq 0) -Severity assertion `
                            -Detail ("duplicates=" + $(if ($duplicatesQuestions.Count -gt 0) { ($duplicatesQuestions -join " | ") } else { "none" }))
                        Add-AcceptanceCheck -Scenario $scenario -Name "at most 3 questions per assistant turn" -Passed (-not (@($turnQuestionCounts | Where-Object { $_.questions -gt 3 }).Count -gt 0)) -Severity warning `
                            -Detail (($turnQuestionCounts | ForEach-Object { "$($_.message_id)=$($_.questions)" }) -join ", ")

                        Add-AcceptanceCheck -Scenario $scenario -Name "requirement drafts produced" -Passed ($requirements.Count -gt 0) -Severity assertion `
                            -Detail "requirements=$($requirements.Count) drafts=$($draftRequirements.Count)"
                        if ($isSmallInput) {
                            Add-AcceptanceCheck -Scenario $scenario -Name "requirement count matches the small input ($($setCheck.ExpectedCount)/$($setCheck.ExpectedCount))" `
                                -Passed ([bool]$setCheck.CountPassed) -Severity assertion -Detail $setCheck.Detail
                            Add-AcceptanceCheck -Scenario $scenario -Name "imported FR IDs match the small input exactly" `
                                -Passed (@($setCheck.MissingFrIds).Count -eq 0 -and @($setCheck.ExtraFrIds).Count -eq 0 -and @($setCheck.DuplicateIds).Count -eq 0 -and @($setCheck.UntraceableRequirementIds).Count -eq 0) `
                                -Severity assertion -Detail $setCheck.Detail
                            Add-AcceptanceCheck -Scenario $scenario -Name "no full-document requirement set imported" `
                                -Passed ([bool]$setCheck.Passed -and -not [bool]$FullSutDoc) -Severity assertion `
                                -Detail ("mode=$($requirementProfile.Mode) input=$($requirementProfile.Name) expected=$($requirementProfile.ExpectedRequirements) actual=$($requirementCount) full_sut_doc_opt_in=$([bool]$FullSutDoc)")
                            Add-AcceptanceCheck -Scenario $scenario -Name "uploaded requirement source is the explicit small-input file (server sha256)" `
                                -Passed ($requirementProfile.Mode -eq "small-input" -and -not [bool]$FullSutDoc -and $null -ne $upload -and [string](Get-JsonProp -Object $upload.Data -Name "sha256" -Default "") -eq $requirementProfile.Sha256) -Severity assertion `
                                -Detail ("mode=$($requirementProfile.Mode) input=$($requirementProfile.Path) upload_sha256=$(if ($null -ne $upload) { [string](Get-JsonProp -Object $upload.Data -Name 'sha256' -Default '') } else { 'missing' }) profile_sha256=$($requirementProfile.Sha256)")
                        }
                        Add-AcceptanceCheck -Scenario $scenario -Name "V1 FR requirements ingested ($frCovered/$frTotal)" -Passed ($frTotal -gt 0 -and $frCovered -eq $frTotal) -Severity assertion `
                            -Detail "covered=$frCovered/$frTotal missing=$(@($coverage.Missing | Where-Object { $_ -like 'FR-0*' }) -join ',')"
                        if ($nfrTotal -gt 0) {
                            Add-AcceptanceCheck -Scenario $scenario -Name "NFR requirements ingested ($nfrCovered/$nfrTotal)" -Passed ($nfrCovered -eq $nfrTotal) -Severity assertion `
                                -Detail "nfr_covered=$nfrCovered/$nfrTotal missing=$(@($coverage.Rows | Where-Object { $_.sut_id -like 'NFR-*' -and -not $_.covered } | ForEach-Object { $_.sut_id }) -join ',')"
                        } else {
                            Add-AcceptanceCheck -Scenario $scenario -Name "NFR requirements are not part of this requirement input" -Passed $true -Severity warning `
                                -Detail "nfr_total=0 mode=$($requirementProfile.Mode); no NFR row exists in the resolved input"
                        }
                        if ($coverage.P0Total -gt 0) {
                            Add-AcceptanceCheck -Scenario $scenario -Name "P0 requirements covered ($($coverage.P0Covered)/$($coverage.P0Total))" -Passed ($coverage.P0Covered -eq $coverage.P0Total) -Severity assertion `
                                -Detail "p0_covered=$($coverage.P0Covered)/$($coverage.P0Total) missing=$(@($coverage.P0Missing) -join ',')"
                        } else {
                            Add-AcceptanceCheck -Scenario $scenario -Name "P0 priority is not declared in this requirement input" -Passed $true -Severity warning `
                                -Detail "p0_total=0 mode=$($requirementProfile.Mode); coverage is asserted by exact FR ID set instead"
                        }
                        Add-AcceptanceCheck -Scenario $scenario -Name "requirements traceable to FR/NFR IDs" -Passed ($untraceableRequirements.Count -eq 0) -Severity assertion `
                            -Detail "untraceable=$($untraceableRequirements.Count) titles=$(@($untraceableRequirements | ForEach-Object { [string](Get-JsonProp -Object $_ -Name 'title' -Default '') }) -join ' | ')"
                        Add-AcceptanceCheck -Scenario $scenario -Name "no duplicate FR/NFR requirements" -Passed ($duplicates.Count -eq 0) -Severity assertion `
                            -Detail ("duplicates=" + $(if ($duplicates.Count -gt 0) { ($duplicates -join ",") } else { "none" }))
                        Add-AcceptanceCheck -Scenario $scenario -Name "V2 延展·未实现 requirements not imported" -Passed ($v2Created.Count -eq 0) -Severity assertion `
                            -Detail "v2_created=$($v2Created.Count)/$($v2Coverage.Total) ids=$(@($v2Created | ForEach-Object { $_.sut_id }) -join ',')"
                        Add-AcceptanceCheck -Scenario $scenario -Name "all requirements remain draft at the gate" -Passed ($requirements.Count -gt 0 -and $nonDraftRequirements.Count -eq 0) -Severity assertion `
                            -Detail "draft=$($draftRequirements.Count) nonDraft=$($nonDraftRequirements.Count)"
                        Add-AcceptanceCheck -Scenario $scenario -Name "stopped at the requirement confirmation gate" -Passed ($stageValue -eq "requirement_review") -Severity assertion `
                            -Detail "stage=$stageValue (expected requirement_review)"
                        Add-AcceptanceCheck -Scenario $scenario -Name "no automatic design or execution" -Passed ($stageValue -notin @("designing", "case_review", "executing", "reporting", "completed")) -Severity assertion `
                            -Detail "stage=$stageValue"
                        Add-AcceptanceCheck -Scenario $scenario -Name "no cases generated before the case gate" -Passed ($caseTotal -eq 0) -Severity assertion -Detail "case_total=$caseTotal"
                        Add-AcceptanceCheck -Scenario $scenario -Name "no design/execute jobs before the case gate" -Passed ($designJobs.Count -eq 0) -Severity assertion `
                            -Detail "designJobs=$($designJobs.Count) totalWorkItems=$($workItems.Count)"
                        Add-AcceptanceCheck -Scenario $scenario -Name "no design conversation created" -Passed ($designConversations.Count -eq 0) -Severity assertion -Detail "designConversations=$($designConversations.Count)"
                        Add-AcceptanceCheck -Scenario $scenario -Name "assistant requested requirement confirmation" -Passed $confirmationAsked -Severity warning -Detail "confirmationRequested=$confirmationAsked"

                        # SSE snapshot after the import (persisted event ids).
                        $sseRecords = @()
                        try {
                            $cursor = Get-EventCursor -Context $context -Channel "version:$versionId"
                            $sse = Wait-SseEvent -Context $context -Channel "version:$versionId" -After $cursor -TimeoutSeconds 15 -MaxEvents 200
                            foreach ($eventObject in @($sse.Events)) {
                                $sseRecords += [pscustomobject]@{
                                    id    = [string]$eventObject.Id
                                    event = [string]$eventObject.Event
                                    kind  = [string](Get-JsonProp -Object (Get-JsonProp -Object $eventObject.Data -Name "payload" -Default $null) -Name "kind" -Default "")
                                }
                            }
                        } catch { }
                        Write-S2Evidence -Name "sse-events.json" -Value $sseRecords -Kind "sse" -Note "job.finished/version events after the requirement import"

                        try {
                            $shot = Save-AcceptanceScreenshot -Context $context -Scenario $scenario -Route "/versions/${versionId}?tab=requirements" -FileName "s2-requirements-gate.png" `
                                -WaitForSelector '[data-testid="version-header"]' -Note "requirements + clarification gate"
                            Add-AcceptanceCheck -Scenario $scenario -Name "requirements gate screenshot" -Passed $true -Severity warning -Detail (Resolve-RepoRelativePath -Context $context -Path $shot.Path)
                        } catch {
                            Add-AcceptanceCheck -Scenario $scenario -Name "requirements gate screenshot" -Passed $false -Severity warning -Detail $_.Exception.Message
                        }

                        $coverageSummary = [pscustomobject]@{
                            version_id              = $versionId
                            primary_conversation_id = $primaryConversationId
                            convergence_conversation_id = $convergenceConversationId
                            requirement_mode        = $requirementProfile.Mode
                            input_path              = $documentPathFull
                            input_sha256            = $requirementProfile.Sha256
                            expected_requirements   = $requirementProfile.ExpectedRequirements
                            expected_fr_ids         = @($requirementProfile.ExpectedFrIds)
                            actual_requirements     = $requirementCount
                            actual_fr_ids           = @($actualRequirementFrIds)
                            set_check               = $setCheck
                            requirements            = $requirementCount
                            fr_covered              = $frCovered
                            fr_total                = $frTotal
                            fr_missing              = @($coverage.Missing | Where-Object { $_ -like "FR-0*" })
                            v1_total                = $coverage.Total
                            v1_covered              = $coverage.Covered
                            nfr_covered             = $nfrCovered
                            nfr_total               = $nfrTotal
                            p0_covered              = $coverage.P0Covered
                            p0_total                = $coverage.P0Total
                            p0_missing              = @($coverage.P0Missing)
                            v2_total                = $v2Coverage.Total
                            v2_created              = $v2Created.Count
                            untraceable             = $untraceableRequirements.Count
                            duplicate_ids           = @($duplicates)
                            stage                   = $stageValue
                            cases                   = $caseTotal
                            batch_count             = $batchResults.Count
                        }
                        Write-S2Evidence -Name "sut-requirements-summary.json" -Value $coverageSummary -Kind "summary" -Note "S2 requirement input import summary"

                        $p0Passed = if ($coverage.P0Total -gt 0) { $coverage.P0Covered -eq $coverage.P0Total } else { $true }
                        $smallSetOk = if ($isSmallInput) { [bool]$setCheck.Passed } else { $true }
                        $s2Ok = ($stageValue -eq "requirement_review" -and
                            $requirements.Count -gt 0 -and
                            $frTotal -gt 0 -and $frCovered -eq $frTotal -and
                            $nfrCovered -eq $nfrTotal -and
                            $p0Passed -and
                            $v2Created.Count -eq 0 -and
                            $untraceableRequirements.Count -eq 0 -and
                            $duplicates.Count -eq 0 -and
                            $duplicatesQuestions.Count -eq 0 -and
                            $uniqueNormalized -le $budget -and
                            $uniqueNormalized -le $UserClarificationQuestionBudget -and
                            $smallSetOk)
                        if (-not $s2Ok) {
                            $setDetail = if ($null -ne $setCheck) { $setCheck.Detail } else { "n/a" }
                            $scenario.ErrorMessage = "S2 failed: mode=$($requirementProfile.Mode) stage=$stageValue requirements=$requirementCount expected=$($requirementProfile.ExpectedRequirements) fr=$frCovered/$frTotal nfr=$nfrCovered/$nfrTotal p0=$($coverage.P0Covered)/$($coverage.P0Total) v2_created=$($v2Created.Count) untraceable=$($untraceableRequirements.Count) duplicates=$($duplicates.Count) userClarificationQuestions=$($uniqueNormalized)/$budget (user budget $UserClarificationQuestionBudget) batchQuestions=$($batchAllQuestions.Count) set=$setDetail"
                        }
                        if ($null -ne $requirementProfile -and $versionId -ne "") {
                            $stateValues = @{
                                requirement_input_mode   = $requirementProfile.Mode
                                requirement_input_path   = $requirementProfile.Path
                                requirement_input_sha256 = $requirementProfile.Sha256
                                requirement_expected_requirements = $requirementProfile.ExpectedRequirements
                                requirement_expected_fr_ids       = @($requirementProfile.ExpectedFrIds)
                                requirement_actual_requirements   = $requirementCount
                                requirement_actual_fr_ids         = @($actualRequirementFrIds)
                                requirement_s2_passed             = [bool]$s2Ok
                                requirement_s2_evidence           = (Resolve-RepoRelativePath -Context $context -Path $scenario.Directory)
                            }
                            if ($isSmallInput) {
                                $stateValues["small_input_version_id"] = $versionId
                                $stateValues["small_input_s2_passed"] = [bool]$s2Ok
                            } else {
                                $stateValues["full_sut_doc_version_id"] = $versionId
                                $stateValues["full_sut_doc_s2_passed"] = [bool]$s2Ok
                            }
                            [void](Update-AcceptanceState -Context $context -Path (Get-AcceptanceStatePath -Context $context) -Values $stateValues)
                        }
                    }
                }
            }
        }
    }
    }
} catch {
    $scenario.ErrorMessage = $_.Exception.Message
    Add-AcceptanceCheck -Scenario $scenario -Name "S2 unexpected error" -Passed $false -Severity blocker -Detail ($_.Exception.Message + " | " + $_.ScriptStackTrace)
} finally {
    $exitCode = Complete-AcceptanceScenario -Scenario $scenario -Message "S2 requirement input import, count/FR mapping and requirement gate"
}

exit $exitCode
