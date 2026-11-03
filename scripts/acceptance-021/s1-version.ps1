#requires -Version 5.1
<#
.SYNOPSIS
    S1 create-version acceptance for EzTest 0.2.1.

.DESCRIPTION
    Creates a version with the full 墨栈博客 binding (SUT + local environment +
    the three SUT accounts + the SUT code repository), posts the version
    description/goal as the first clarification message, and then asserts that
    the discovery sub-loop produced the three required version facts:
    `login_session`, `page` and `api`.

    The version is intentionally *not* pre-populated with pages or APIs: the
    acceptance expects the product to discover them. A missing fact is a real
    failure, not a skip.

    All observations and the full facts snapshot are written under
    docs/version-V0.2.1/acceptance/evidence/s0-s2/s1/.

.PARAMETER FactTimeoutSeconds
    How long to wait for the discovery facts. Defaults to 120 seconds.

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
    [int] $FactTimeoutSeconds = 120
)

Set-StrictMode -Version 3.0
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "lib.ps1")

$context = Initialize-AcceptanceContext -ApiBaseUrl $ApiBaseUrl -WebBaseUrl $WebBaseUrl `
    -ExecutorUrl $ExecutorUrl -SutWebBaseUrl $SutWebBaseUrl -SutApiBaseUrl $SutApiBaseUrl `
    -AdminUsername $AdminUsername -AdminPassword $AdminPassword -SutPassword $SutPassword `
    -EvidenceRoot $EvidenceRoot
$scenario = New-AcceptanceScenario -Context $context -Name "s1" -Title "建版本与发现子循环 (S1)"

$runSuffix = (Get-Date).ToString("yyyyMMdd-HHmmss")
$version = $null
$conversation = $null
$bindingResult = $null
$exitCode = 2

try {
    Write-AcceptanceLog -Scenario $scenario -Message "S1 create-version acceptance; fact timeout=${FactTimeoutSeconds}s"

    # ------------------------------------------------------------------
    # Login (blocked when the stack is not ready)
    # ------------------------------------------------------------------
    $login = Connect-EzTest -Context $context
    Add-AcceptanceCheck -Scenario $scenario -Name "EzTest admin login" -Passed $login.Ok -Severity blocker `
        -Detail ($(if ($login.Ok) { "user=$([string](Get-JsonProp -Object $login.User -Name "username" -Default "?"))" } else { $login.Error }))
    if (-not $login.Ok) {
        $scenario.ErrorMessage = "S1 blocked: EzTest admin login failed"
    } else {
        # ------------------------------------------------------------------
        # Bind the SUT (idempotent; EzTest data only)
        # ------------------------------------------------------------------
        $bindingResult = $null
        try {
            $bindingResult = Resolve-AcceptanceSutBinding -Context $context
            Write-EvidenceJson -Path (Join-Path $scenario.Directory "sut-binding.json") -Value ([pscustomobject]@{
                sut = $bindingResult.Sut
                environment = $bindingResult.Environment
                accounts = @($bindingResult.Accounts | ForEach-Object {
                    [pscustomobject]@{
                        id = [string](Get-JsonProp -Object $_ -Name "id" -Default "")
                        username = [string](Get-JsonProp -Object $_ -Name "username" -Default "")
                        roles = @(Get-JsonProp -Object $_ -Name "roles" -Default @())
                        status = [string](Get-JsonProp -Object $_ -Name "status" -Default "")
                        password_set = [bool](Get-JsonProp -Object $_ -Name "password_set" -Default $false)
                    }
                })
                repository = $bindingResult.Repository
                binding = $bindingResult.Binding
                created = @($bindingResult.Created)
            })
            Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "sut-binding.json") -Kind "api" -Note "SUT binding (no secrets)"
            Add-AcceptanceCheck -Scenario $scenario -Name "SUT bound (SUT/environment/accounts/repository)" -Passed $true -Severity blocker `
                -Detail ("sut=$([string](Get-JsonProp -Object $bindingResult.Sut -Name "name" -Default "?")) env=$([string](Get-JsonProp -Object $bindingResult.Environment -Name "base_url" -Default "?")) accounts=$(@($bindingResult.Accounts).Count) repo=$([string](Get-JsonProp -Object $bindingResult.Repository -Name "path" -Default "?")) created=$(@($bindingResult.Created) -join ",")")
        } catch {
            Add-AcceptanceCheck -Scenario $scenario -Name "SUT bound (SUT/environment/accounts/repository)" -Passed $false -Severity blocker -Detail $_.Exception.Message
            $scenario.ErrorMessage = "S1 blocked: could not prepare the SUT binding: $($_.Exception.Message)"
        }

        if ($null -ne $bindingResult) {
            # ------------------------------------------------------------------
            # Create the version and verify the binding round-trips
            # ------------------------------------------------------------------
            $title = "墨栈博客 0.2.1 验收 · S1 建版本 · $runSuffix"
            try {
                $version = New-AcceptanceVersion -Context $context -Title $title -Binding $bindingResult.Binding
                Write-EvidenceJson -Path (Join-Path $scenario.Directory "version.json") -Value $version
                Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "version.json") -Kind "api" -Note "created version"
                Add-AcceptanceCheck -Scenario $scenario -Name "version created" -Passed ($null -ne $version -and [string](Get-JsonProp -Object $version -Name "id" -Default "") -ne "") -Severity assertion `
                    -Detail ("id=" + [string](Get-JsonProp -Object $version -Name "id" -Default "") + " title=" + [string](Get-JsonProp -Object $version -Name "title" -Default ""))
            } catch {
                Add-AcceptanceCheck -Scenario $scenario -Name "version created" -Passed $false -Severity blocker -Detail $_.Exception.Message
                $scenario.ErrorMessage = "S1 blocked: version creation failed: $($_.Exception.Message)"
            }

            if ($null -ne $version) {
                $versionId = [string](Get-JsonProp -Object $version -Name "id" -Default "")
                $bindings = @(Get-JsonProp -Object $version -Name "bindings" -Default @())
                $boundSutName = ""
                $boundBaseUrl = ""
                $boundRepoPath = ""
                $boundAccountCount = 0
                if ($bindings.Count -gt 0) {
                    $boundSutName = [string](Get-JsonProp -Object (Get-JsonProp -Object $bindings[0] -Name "sut" -Default $null) -Name "name" -Default "")
                    $boundBaseUrl = [string](Get-JsonProp -Object (Get-JsonProp -Object $bindings[0] -Name "environment" -Default $null) -Name "base_url" -Default "")
                    $boundRepoPath = [string](Get-JsonProp -Object (Get-JsonProp -Object $bindings[0] -Name "repository" -Default $null) -Name "path" -Default "")
                    $boundAccountCount = @(Get-JsonProp -Object $bindings[0] -Name "accounts" -Default @()).Count
                }
                Add-AcceptanceCheck -Scenario $scenario -Name "version binding includes the SUT and environment" -Passed ($boundSutName -ne "" -and $boundBaseUrl.TrimEnd('/') -eq $context.SutWebBaseUrl) -Severity assertion `
                    -Detail "sut=$boundSutName base_url=$boundBaseUrl"
                Add-AcceptanceCheck -Scenario $scenario -Name "version binding includes the code repository" -Passed ($boundRepoPath -ne "") -Severity warning -Detail "repository=$boundRepoPath"
                Add-AcceptanceCheck -Scenario $scenario -Name "version binding includes the three SUT accounts" -Passed ($boundAccountCount -ge 3) -Severity assertion -Detail "accounts=$boundAccountCount"

                # ------------------------------------------------------------------
                # Post the version description + goal (the only natural-language
                # version-goal surface in the 0.2.1 API) and verify it is stored.
                # ------------------------------------------------------------------
                try {
                    $conversation = New-AcceptanceConversation -Context $context -VersionId $versionId -Kind "clarification" -Title "S1 版本描述与目标"
                    $goal = @"
【版本描述】墨栈博客是一个 Vue 前端 + Go 后端的博客系统，SUT 前端 $($context.SutWebBaseUrl)，后端 API $($context.SutApiBaseUrl)/api/v1，测试账号 admin/openClaw/Hermes。
【版本目标】验收登录与会话、文章列表与文章详情、文章评论、后台管理四项主流程；包含未登录/越权与文章状态边界。
请先启动发现子循环（登录会话、页面、API），再进入需求澄清。
"@
                    $message = New-AcceptanceUserMessage -Context $context -VersionId $versionId -ConversationId ([string](Get-JsonProp -Object $conversation -Name "id" -Default "")) -Text $goal
                    Write-EvidenceJson -Path (Join-Path $scenario.Directory "initial-message.json") -Value ([pscustomobject]@{
                        conversation_id = [string](Get-JsonProp -Object $conversation -Name "id" -Default "")
                        message_id = [string](Get-JsonProp -Object $message -Name "id" -Default "")
                        role = [string](Get-JsonProp -Object $message -Name "role" -Default "")
                        text = $goal
                    })
                    Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "initial-message.json") -Kind "api" -Note "version description + goal"
                    Add-AcceptanceCheck -Scenario $scenario -Name "version description and goal accepted" -Passed ($null -ne $message -and [string](Get-JsonProp -Object $message -Name "id" -Default "") -ne "") -Severity assertion `
                        -Detail ("conversation=" + [string](Get-JsonProp -Object $conversation -Name "id" -Default "") + " message=" + [string](Get-JsonProp -Object $message -Name "id" -Default ""))
                } catch {
                    Add-AcceptanceCheck -Scenario $scenario -Name "version description and goal accepted" -Passed $false -Severity assertion -Detail $_.Exception.Message
                }

                # ------------------------------------------------------------------
                # The acceptance criterion: the discovery sub-loop produces
                # login_session + page + api facts without manual setup.
                # ------------------------------------------------------------------
                $watcher = Wait-Until -TimeoutSeconds $FactTimeoutSeconds -IntervalSeconds 3 -Description "discovery facts" -Condition {
                    $facts = @(Get-VersionFacts -Context $context -VersionId $versionId -Limit 500)
                    $counts = @{}
                    foreach ($fact in $facts) {
                        $kind = [string](Get-JsonProp -Object $fact -Name "kind" -Default "")
                        if ($counts.ContainsKey($kind)) { $counts[$kind] = $counts[$kind] + 1 } else { $counts[$kind] = 1 }
                    }
                    $loginCount = if ($counts.ContainsKey("login_session")) { $counts["login_session"] } else { 0 }
                    $pageCount = if ($counts.ContainsKey("page")) { $counts["page"] } else { 0 }
                    $apiCount = if ($counts.ContainsKey("api")) { $counts["api"] } else { 0 }
                    if ($loginCount -gt 0 -and $pageCount -gt 0 -and $apiCount -gt 0) {
                        return [pscustomobject]@{ Facts = $facts; Counts = $counts; Login = $loginCount; Page = $pageCount; API = $apiCount }
                    }
                    return $null
                }
                $facts = @()
                $counts = @{}
                $loginFacts = 0
                $pageFacts = 0
                $apiFacts = 0
                if ($watcher.Ok -and $null -ne $watcher.Value) {
                    $facts = @($watcher.Value.Facts)
                    $counts = $watcher.Value.Counts
                    $loginFacts = [int]$watcher.Value.Login
                    $pageFacts = [int]$watcher.Value.Page
                    $apiFacts = [int]$watcher.Value.API
                } else {
                    $facts = @(Get-VersionFacts -Context $context -VersionId $versionId -Limit 500)
                    foreach ($fact in $facts) {
                        $kind = [string](Get-JsonProp -Object $fact -Name "kind" -Default "")
                        if ($counts.ContainsKey($kind)) { $counts[$kind] = $counts[$kind] + 1 } else { $counts[$kind] = 1 }
                    }
                    $loginFacts = if ($counts.ContainsKey("login_session")) { [int]$counts["login_session"] } else { 0 }
                    $pageFacts = if ($counts.ContainsKey("page")) { [int]$counts["page"] } else { 0 }
                    $apiFacts = if ($counts.ContainsKey("api")) { [int]$counts["api"] } else { 0 }
                }
                Write-EvidenceJson -Path (Join-Path $scenario.Directory "facts.json") -Value $facts
                Write-EvidenceJson -Path (Join-Path $scenario.Directory "fact-kind-counts.json") -Value $counts
                Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "facts.json") -Kind "api" -Note "version facts snapshot"
                Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "fact-kind-counts.json") -Kind "api" -Note "fact kinds"

                $kindDetail = ($counts.GetEnumerator() | Sort-Object Key | ForEach-Object { "$($_.Key)=$($_.Value)" }) -join ", "
                if ($kindDetail -eq "") { $kindDetail = "(no facts)" }
                Add-AcceptanceCheck -Scenario $scenario -Name "login_session fact discovered" -Passed ($loginFacts -gt 0) -Severity assertion `
                    -Detail ("login_session=$loginFacts; all kinds: $kindDetail")
                Add-AcceptanceCheck -Scenario $scenario -Name "page facts discovered" -Passed ($pageFacts -gt 0) -Severity assertion `
                    -Detail ("page=$pageFacts; all kinds: $kindDetail")
                Add-AcceptanceCheck -Scenario $scenario -Name "API facts discovered" -Passed ($apiFacts -gt 0) -Severity assertion `
                    -Detail ("api=$apiFacts; all kinds: $kindDetail")

                # ------------------------------------------------------------------
                # Supporting evidence: stage, work items, messages, screenshots
                # ------------------------------------------------------------------
                try {
                    $stage = Get-VersionStage -Context $context -VersionId $versionId
                    Write-EvidenceJson -Path (Join-Path $scenario.Directory "stage.json") -Value $stage
                    Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "stage.json") -Kind "api" -Note "version stage"
                    Add-AcceptanceCheck -Scenario $scenario -Name "version left the created stage" -Passed ([string](Get-JsonProp -Object $stage -Name "stage" -Default "") -ne "created") -Severity assertion `
                        -Detail ("stage=" + [string](Get-JsonProp -Object $stage -Name "stage" -Default ""))
                } catch {
                    Add-AcceptanceCheck -Scenario $scenario -Name "version left the created stage" -Passed $false -Severity warning -Detail $_.Exception.Message
                }
                try {
                    $workItems = @(Get-VersionWorkItems -Context $context -VersionId $versionId)
                    Write-EvidenceJson -Path (Join-Path $scenario.Directory "work-items.json") -Value $workItems
                    Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "work-items.json") -Kind "api" -Note "work items"
                } catch { }
                try {
                    $conversationId = [string](Get-JsonProp -Object $conversation -Name "id" -Default "")
                    if ($conversationId -ne "") {
                        $messages = @(Get-ConversationMessages -Context $context -VersionId $versionId -ConversationId $conversationId)
                        $messageSummary = @($messages | ForEach-Object {
                            [pscustomobject]@{
                                id = [string](Get-JsonProp -Object $_ -Name "id" -Default "")
                                role = [string](Get-JsonProp -Object $_ -Name "role" -Default "")
                                text = (Get-AssistantText -Message $_)
                            }
                        })
                        Write-EvidenceJson -Path (Join-Path $scenario.Directory "messages.json") -Value $messageSummary
                        Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "messages.json") -Kind "api" -Note "conversation messages (text only)"
                    }
                } catch { }

                try {
                    $shot = Save-AcceptanceScreenshot -Context $context -Scenario $scenario -Route "/versions/$versionId" -FileName "s1-version-overview.png" `
                        -WaitForSelector '[data-testid="version-header"]' -Note "version overview with SUT binding"
                    Add-AcceptanceCheck -Scenario $scenario -Name "version overview screenshot" -Passed $true -Severity warning -Detail (Resolve-RepoRelativePath -Context $context -Path $shot.Path)
                } catch {
                    Add-AcceptanceCheck -Scenario $scenario -Name "version overview screenshot" -Passed $false -Severity warning -Detail $_.Exception.Message
                }
                try {
                    $chainShot = Save-AcceptanceScreenshot -Context $context -Scenario $scenario -Route "/versions/${versionId}?tab=chain" -FileName "s1-exploration-chain.png" `
                        -WaitForSelector '[data-testid="version-header"]' -Note "exploration chain tab (facts view)"
                    Add-AcceptanceCheck -Scenario $scenario -Name "exploration chain screenshot" -Passed $true -Severity warning -Detail (Resolve-RepoRelativePath -Context $context -Path $chainShot.Path)
                } catch {
                    Add-AcceptanceCheck -Scenario $scenario -Name "exploration chain screenshot" -Passed $false -Severity warning -Detail $_.Exception.Message
                }

                if ($loginFacts -eq 0 -or $pageFacts -eq 0 -or $apiFacts -eq 0) {
                    $scenario.ErrorMessage = "S1 failed: discovery facts missing after ${FactTimeoutSeconds}s (login_session=$loginFacts page=$pageFacts api=$apiFacts)"
                }
            }
        }
    }
} catch {
    $scenario.ErrorMessage = $_.Exception.Message
    Add-AcceptanceCheck -Scenario $scenario -Name "S1 unexpected error" -Passed $false -Severity blocker -Detail ($_.Exception.Message + " | " + $_.ScriptStackTrace)
} finally {
    $exitCode = Complete-AcceptanceScenario -Scenario $scenario -Message "S1 create version and discovery facts"
}

exit $exitCode
