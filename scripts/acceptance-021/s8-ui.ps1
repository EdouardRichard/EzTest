#requires -Version 5.1
<#
.SYNOPSIS
    S8 UI acceptance for EzTest 0.2.1.

.DESCRIPTION
    Creates a real clarification conversation backed by the real LLM, then
    drives the real Next.js UI with Playwright (the executor's installed
    module) and asserts the spec 13.2 S8 criteria:
      * thinking and tool-call parts are visible in the chat;
      * job cards/progress are visible;
      * the model selector shows the persisted actual model and a user switch
        is persisted (then restored);
      * the floating chat does not lose the composer draft on expand/collapse;
      * the design workspace renders the three layers (chat/mindmap/detail);
      * the mindmap zoom control changes the viewport and the focus dialog
        opens with the same workspace.

    The chat data is produced by posting a real user message to a new version
    and waiting for the assistant's thinking/tool parts. If that turn happens
    not to emit thinking/tools, the script falls back to a discovered
    conversation that already has thinking+tool parts (read-only evidence).
    A missing product surface is a failed assertion, never a skip.

    Screenshots and the DOM check JSON are written under
    docs/version-V0.2.1/acceptance/evidence/s7-s10/s8/.

.PARAMETER ReplyTimeoutSeconds
    How long to wait for the assistant thinking/tool parts. Default 180.

.PARAMETER DesignDiscoveryLimit
    How many recent versions to inspect for a design graph with cases. Default 20.

.NOTES
    Exit codes: 0 = passed, 1 = failed acceptance assertion, 2 = blocked
    environment. No git writes, no product code changes.
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
    [int] $ReplyTimeoutSeconds = 180,
    [int] $DesignDiscoveryLimit = 50
)

Set-StrictMode -Version 3.0
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "lib.ps1")

$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
if ([string]::IsNullOrWhiteSpace($EvidenceRoot)) {
    $EvidenceRoot = Join-Path $repoRoot "docs/version-V0.2.1/acceptance/evidence/s7-s10"
}
$context = Initialize-AcceptanceContext -RepoRoot $repoRoot -EvidenceRoot $EvidenceRoot `
    -ApiBaseUrl $ApiBaseUrl -WebBaseUrl $WebBaseUrl -ExecutorUrl $ExecutorUrl `
    -SutWebBaseUrl $SutWebBaseUrl -SutApiBaseUrl $SutApiBaseUrl `
    -AdminUsername $AdminUsername -AdminPassword $AdminPassword -SutPassword $SutPassword
$scenario = New-AcceptanceScenario -Context $context -Name "s8" -Title "UI: conversation, floating panel, three layers, mindmap (S8)"

$exitCode = 2
$runSuffix = (Get-Date).ToString("yyyyMMdd-HHmmss")
$versionId = ""
$conversationId = ""
$originalOverride = $null
$originalEffective = ""

function Get-PartTypes {
    param($Message)
    $parts = Get-JsonProp -Object $Message -Name "parts" -Default @()
    return @($parts | ForEach-Object { [string](Get-JsonProp -Object $_ -Name "type" -Default "") })
}

function Get-MessageText {
    param($Message)
    $parts = Get-JsonProp -Object $Message -Name "parts" -Default @()
    $chunks = @()
    foreach ($part in @($parts)) {
        if ([string](Get-JsonProp -Object $part -Name "type" -Default "") -eq "text") {
            $chunks += [string](Get-JsonProp -Object $part -Name "text" -Default "")
        }
    }
    return ($chunks -join "`n")
}

function Find-ChatWithThinkingTool {
    param($Context, [int] $VersionLimit = 8, [int] $ConversationLimit = 4)
    $versionsResponse = Invoke-ApiOrFail -Context $Context -Method "GET" -Path "/api/versions?limit=$VersionLimit" -Token $Context.Token -Label "discover versions"
    foreach ($version in @($versionsResponse.Data)) {
        $candidateVersionId = [string](Get-JsonProp -Object $version -Name "id" -Default "")
        if ([string]::IsNullOrWhiteSpace($candidateVersionId)) { continue }
        try {
            $conversationsResponse = Invoke-ApiOrFail -Context $Context -Method "GET" -Path "/api/versions/$candidateVersionId/conversations?limit=$ConversationLimit" -Token $Context.Token -Label "discover conversations"
        } catch { continue }
        foreach ($conversation in @($conversationsResponse.Data)) {
            $candidateConversationId = [string](Get-JsonProp -Object $conversation -Name "id" -Default "")
            if ([string]::IsNullOrWhiteSpace($candidateConversationId)) { continue }
            try {
                $messagesResponse = Invoke-ApiOrFail -Context $Context -Method "GET" -Path "/api/versions/$candidateVersionId/conversations/$candidateConversationId/messages?limit=100" -Token $Context.Token -Label "discover messages"
            } catch { continue }
            foreach ($message in @($messagesResponse.Data)) {
                if ([string](Get-JsonProp -Object $message -Name "role" -Default "") -ne "assistant") { continue }
                $types = Get-PartTypes -Message $message
                if ($types -contains "thinking" -and $types -contains "tool_call" -and $types -contains "text") {
                    return [pscustomobject]@{
                        VersionId = $candidateVersionId
                        ConversationId = $candidateConversationId
                        MessageId = [string](Get-JsonProp -Object $message -Name "id" -Default "")
                        Types = $types
                        VersionTitle = [string](Get-JsonProp -Object $version -Name "title" -Default "")
                        ConversationTitle = [string](Get-JsonProp -Object $conversation -Name "title" -Default "")
                    }
                }
            }
        }
    }
    return $null
}

function Find-DesignVersion {
    param($Context, [int] $Limit = 20)
    $versionsResponse = Invoke-ApiOrFail -Context $Context -Method "GET" -Path "/api/versions?limit=$Limit" -Token $Context.Token -Label "discover design versions"
    $best = $null
    $bestCases = 0
    foreach ($version in @($versionsResponse.Data)) {
        $candidateId = [string](Get-JsonProp -Object $version -Name "id" -Default "")
        if ([string]::IsNullOrWhiteSpace($candidateId)) { continue }
        try {
            $graph = Invoke-ApiOrFail -Context $Context -Method "GET" -Path "/api/versions/$candidateId/design/graph" -Token $Context.Token -Label "design graph"
        } catch { continue }
        $stats = Get-JsonProp -Object $graph.Data -Name "stats" -Default $null
        $cases = 0
        if ($null -ne $stats) { $cases = [int](Get-JsonProp -Object $stats -Name "cases" -Default 0) }
        if ($cases -gt $bestCases) {
            $bestCases = $cases
            $best = [pscustomobject]@{
                VersionId = $candidateId
                Cases = $cases
                Requirements = [int](Get-JsonProp -Object $stats -Name "requirements" -Default 0)
                Scenarios = [int](Get-JsonProp -Object $stats -Name "scenarios" -Default 0)
                Title = [string](Get-JsonProp -Object $version -Name "title" -Default "")
                Graph = $graph.Data
            }
        }
    }
    return $best
}

try {
    Write-AcceptanceLog -Scenario $scenario -Message "S8 UI acceptance; replyTimeout=${ReplyTimeoutSeconds}s designLimit=$DesignDiscoveryLimit"

    $login = Connect-EzTest -Context $context
    Add-AcceptanceCheck -Scenario $scenario -Name "EzTest admin login" -Passed $login.Ok -Severity blocker -Detail ($(if ($login.Ok) { "admin session established" } else { "admin credentials rejected" }))
    $web = Test-EzTestWebReachable -Context $context -TimeoutSeconds 20
    Add-AcceptanceCheck -Scenario $scenario -Name "EzTest web reachable" -Passed $web.Ok -Severity blocker -Detail ("HTTP $($web.Status) $($web.Error)")

    if (-not $login.Ok -or -not $web.Ok) {
        $scenario.ErrorMessage = "S8 blocked: EzTest login or web app unavailable"
    } else {
        # ------------------------------------------------------------------
        # Build/aquire a chat with real thinking + tool parts.
        # ------------------------------------------------------------------
        $thinkingToolChat = $null
        $usedFallback = $false
        try {
            $binding = Resolve-AcceptanceSutBinding -Context $context
            $version = New-AcceptanceVersion -Context $context -Title ("S8 UI acceptance " + $runSuffix) -Binding $binding.Binding
            $versionId = [string](Get-JsonProp -Object $version -Name "id" -Default "")
            $conversation = New-AcceptanceConversation -Context $context -VersionId $versionId -Kind "clarification" -Title "S8 UI conversation"
            $conversationId = [string](Get-JsonProp -Object $conversation -Name "id" -Default "")
            Add-AcceptanceCheck -Scenario $scenario -Name "S8 version and conversation created" -Passed ($versionId -ne "" -and $conversationId -ne "") -Severity blocker -Detail "version=$versionId conversation=$conversationId"

            $overrideResponse = Invoke-Api -Context $context -Method "GET" -Path "/api/versions/$versionId/conversations/$conversationId" -Token $context.Token
            if ($overrideResponse.Status -eq 200) {
                $originalOverride = Get-JsonProp -Object $overrideResponse.Data -Name "model_override" -Default $null
                $originalEffective = [string](Get-JsonProp -Object $overrideResponse.Data -Name "effective_model" -Default "")
            }

            New-AcceptanceUserMessage -Context $context -VersionId $versionId -ConversationId $conversationId `
                -Text "Please ask one clarifying question, and use your available tools to inspect the version context before answering. Keep it short." | Out-Null

            $deadline = (Get-Date).AddSeconds($ReplyTimeoutSeconds)
            $messageSummary = @()
            while ((Get-Date) -lt $deadline) {
                try {
                    $messagesResponse = Invoke-ApiOrFail -Context $context -Method "GET" -Path "/api/versions/$versionId/conversations/$conversationId/messages?limit=100" -Token $context.Token -Label "S8 messages"
                } catch { Start-Sleep -Seconds 3; continue }
                $messageSummary = @()
                foreach ($message in @($messagesResponse.Data)) {
                    $types = Get-PartTypes -Message $message
                    $messageSummary += [pscustomobject]@{
                        id = [string](Get-JsonProp -Object $message -Name "id" -Default "")
                        role = [string](Get-JsonProp -Object $message -Name "role" -Default "")
                        types = @($types)
                        text = (Get-MessageText -Message $message)
                    }
                    if ([string](Get-JsonProp -Object $message -Name "role" -Default "") -eq "assistant" -and $types -contains "thinking" -and $types -contains "tool_call") {
                        $thinkingToolChat = [pscustomobject]@{
                            VersionId = $versionId
                            ConversationId = $conversationId
                            MessageId = [string](Get-JsonProp -Object $message -Name "id" -Default "")
                            Types = $types
                        }
                        break
                    }
                }
                if ($null -ne $thinkingToolChat) { break }
                Start-Sleep -Seconds 4
            }
            Write-EvidenceJson -Path (Join-Path $scenario.Directory "s8-chat-messages.json") -Value $messageSummary
            Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "s8-chat-messages.json") -Kind "api" -Note "S8 chat message part types"
            Add-AcceptanceCheck -Scenario $scenario -Name "real assistant turn emitted thinking and tool parts" -Passed ($null -ne $thinkingToolChat) -Severity assertion `
                -Detail ($(if ($null -ne $thinkingToolChat) { "message=$($thinkingToolChat.MessageId) types=$($thinkingToolChat.Types -join ',')" } else { "no thinking+tool_call assistant message within ${ReplyTimeoutSeconds}s" }))

            if ($null -eq $thinkingToolChat) {
                $usedFallback = $true
                Write-AcceptanceLog -Scenario $scenario -Level warn -Message "falling back to a discovered conversation with thinking+tool parts for the UI assertion"
                $fallback = Find-ChatWithThinkingTool -Context $context
                if ($null -ne $fallback) {
                    $thinkingToolChat = $fallback
                    Write-EvidenceJson -Path (Join-Path $scenario.Directory "s8-fallback-chat.json") -Value ([pscustomobject]@{
                        version_id = $fallback.VersionId
                        conversation_id = $fallback.ConversationId
                        message_id = $fallback.MessageId
                        part_types = @($fallback.Types)
                        version_title = $fallback.VersionTitle
                        conversation_title = $fallback.ConversationTitle
                    })
                    Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "s8-fallback-chat.json") -Kind "api" -Note "discovered chat with thinking/tool parts (read-only)"
                }
                Add-AcceptanceCheck -Scenario $scenario -Name "discovered chat provides thinking and tool parts" -Passed ($null -ne $fallback) -Severity assertion `
                    -Detail ($(if ($null -ne $fallback) { "version=$($fallback.VersionId) conversation=$($fallback.ConversationId) types=$($fallback.Types -join ',')" } else { "no existing conversation has thinking+tool_call parts" }))
            }
        } catch {
            Add-AcceptanceCheck -Scenario $scenario -Name "S8 chat data preparation" -Passed $false -Severity blocker -Detail $_.Exception.Message
        }

        # ------------------------------------------------------------------
        # Discover a version with design cases for the three-layer/mindmap checks.
        # ------------------------------------------------------------------
        $designVersion = $null
        try {
            $designVersion = Find-DesignVersion -Context $context -Limit $DesignDiscoveryLimit
            if ($null -ne $designVersion -and $null -ne $designVersion.Graph) {
                Write-EvidenceJson -Path (Join-Path $scenario.Directory "s8-design-graph.json") -Value $designVersion.Graph
                Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "s8-design-graph.json") -Kind "api" -Note "design graph used for the UI evidence"
            }
            Add-AcceptanceCheck -Scenario $scenario -Name "version with design cases discovered" -Passed ($null -ne $designVersion) -Severity assertion `
                -Detail ($(if ($null -ne $designVersion) { "version=$($designVersion.VersionId) cases=$($designVersion.Cases) requirements=$($designVersion.Requirements)" } else { "no design graph with cases in the newest $DesignDiscoveryLimit versions" }))
        } catch {
            Add-AcceptanceCheck -Scenario $scenario -Name "version with design cases discovered" -Passed $false -Severity assertion -Detail $_.Exception.Message
        }

        # ------------------------------------------------------------------
        # Drive the real UI with Playwright.
        # ------------------------------------------------------------------
        if ($null -ne $thinkingToolChat) {
            $node = Get-Command node -ErrorAction SilentlyContinue
            $playwrightRoot = Join-Path $context.RepoRoot "executor/node_modules"
            if ($null -eq $node) {
                Add-AcceptanceCheck -Scenario $scenario -Name "Playwright UI driver available" -Passed $false -Severity blocker -Detail "node is not on PATH"
            } elseif (-not (Test-Path -LiteralPath (Join-Path $playwrightRoot "playwright"))) {
                Add-AcceptanceCheck -Scenario $scenario -Name "Playwright UI driver available" -Passed $false -Severity blocker -Detail "playwright is not installed under executor/node_modules"
            } else {
                $tempRoot = Join-Path $context.RepoRoot ".tmp/acceptance-021"
                New-Item -ItemType Directory -Force -Path $tempRoot | Out-Null
                $optionsPath = Join-Path $tempRoot ("s8-ui-options-" + [Guid]::NewGuid().ToString("N") + ".json")
                $options = [pscustomobject]@{
                    webBaseUrl = $context.WebBaseUrl
                    versionId = $thinkingToolChat.VersionId
                    conversationId = $thinkingToolChat.ConversationId
                    designVersionId = $(if ($null -ne $designVersion) { $designVersion.VersionId } else { "" })
                    token = $context.Token
                    user = $context.User
                    outDir = $scenario.Directory
                    timeoutMs = 60000
                }
                Write-EvidenceJson -Path $optionsPath -Value $options -NoRedaction
                $previousNodePath = $env:NODE_PATH
                try {
                    $env:NODE_PATH = if ($previousNodePath) { "$playwrightRoot;$previousNodePath" } else { $playwrightRoot }
                    $driver = Join-Path $PSScriptRoot "s8-ui.cjs"
                    $output = & $node.Source $driver $optionsPath 2>&1
                    $driverExit = $LASTEXITCODE
                } finally {
                    $env:NODE_PATH = $previousNodePath
                    Remove-Item -LiteralPath $optionsPath -Force -ErrorAction SilentlyContinue
                }
                Write-EvidenceText -Path (Join-Path $scenario.Directory "s8-ui-driver.log") -Text (($output | Out-String))
                Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "s8-ui-driver.log") -Kind "log" -Note "Playwright UI driver output"

                $domPath = Join-Path $scenario.Directory "s8-ui-dom.json"
                $domResult = $null
                if (Test-Path -LiteralPath $domPath) {
                    try { $domResult = [System.IO.File]::ReadAllText($domPath, [System.Text.Encoding]::UTF8) | ConvertFrom-Json } catch { $domResult = $null }
                }
                Add-AcceptanceCheck -Scenario $scenario -Name "Playwright UI driver produced DOM results" -Passed ($null -ne $domResult) -Severity assertion -Detail ("exit=$driverExit")
                if ($null -ne $domResult) {
                    if ($null -ne $domResult.checks) {
                        foreach ($item in @($domResult.checks)) {
                            Add-AcceptanceCheck -Scenario $scenario -Name ([string]$item.name) -Passed ([bool]$item.passed) -Severity assertion -Detail ([string]$item.detail)
                        }
                    }
                    if ($null -ne $domResult.consoleErrors) {
                        $consoleErrors = @($domResult.consoleErrors)
                        Add-AcceptanceCheck -Scenario $scenario -Name "no browser console errors during the UI checks" -Passed ($consoleErrors.Count -eq 0) -Severity warning -Detail ("console_errors=$($consoleErrors.Count) " + (($consoleErrors | Select-Object -First 3) -join " | "))
                    }
                    if ($null -ne $domResult.screenshots) {
                        foreach ($screenshotPath in @($domResult.screenshots)) {
                            if (Test-Path -LiteralPath ([string]$screenshotPath)) {
                                Add-AcceptanceArtifact -Scenario $scenario -Path ([string]$screenshotPath) -Kind "screenshot" -Note "S8 UI evidence"
                                Add-AcceptanceCheck -Scenario $scenario -Name ("screenshot: " + (Split-Path -Leaf ([string]$screenshotPath))) -Passed $true -Severity warning -Detail (Resolve-RepoRelativePath -Context $context -Path ([string]$screenshotPath))
                            }
                        }
                    }
                    Add-AcceptanceCheck -Scenario $scenario -Name "Playwright UI driver exit code" -Passed ($driverExit -eq 0) -Severity assertion -Detail ("exit=$driverExit")
                }
            }
        } else {
            Add-AcceptanceCheck -Scenario $scenario -Name "Playwright UI checks" -Passed $false -Severity assertion -Detail "no chat with thinking/tool parts available for the UI"
        }

        # ------------------------------------------------------------------
        # API-side verification of the model switch state after the UI run.
        # ------------------------------------------------------------------
        if ($versionId -ne "" -and $conversationId -ne "") {
            try {
                $conversationAfter = Invoke-ApiOrFail -Context $context -Method "GET" -Path "/api/versions/$versionId/conversations/$conversationId" -Token $context.Token -Label "conversation after UI"
                $overrideAfter = Get-JsonProp -Object $conversationAfter.Data -Name "model_override" -Default $null
                $effectiveAfter = [string](Get-JsonProp -Object $conversationAfter.Data -Name "effective_model" -Default "")
                $restoredOverride = (($null -eq $originalOverride) -and ($null -eq $overrideAfter)) -or ([string]$originalOverride -eq [string]$overrideAfter)
                $restoredEffective = ($originalEffective -eq "" -or $effectiveAfter -eq $originalEffective) -or (
                    ($null -eq $originalOverride) -and ($null -ne $overrideAfter) -and ([string]$overrideAfter -eq $originalEffective)
                )
                Add-AcceptanceCheck -Scenario $scenario -Name "model selector restored the conversation model" -Passed ($restoredOverride -or $restoredEffective) -Severity assertion `
                    -Detail ("override_before=$(if ($null -eq $originalOverride) { 'null' } else { [string]$originalOverride }) override_after=$(if ($null -eq $overrideAfter) { 'null' } else { [string]$overrideAfter }) effective_before=$originalEffective effective_after=$effectiveAfter")
                if ($usedFallback) {
                    Add-AcceptanceCheck -Scenario $scenario -Name "S8 UI evidence used a discovered conversation" -Passed $true -Severity warning -Detail "the new real-LLM turn emitted no thinking/tool parts; UI was checked against existing evidence data"
                }
            } catch {
                Add-AcceptanceCheck -Scenario $scenario -Name "model selector restored the conversation model" -Passed $false -Severity warning -Detail $_.Exception.Message
            }
        }
    }
} catch {
    $scenario.ErrorMessage = $_.Exception.Message
    Add-AcceptanceCheck -Scenario $scenario -Name "S8 unexpected error" -Passed $false -Severity blocker -Detail ($_.Exception.Message + " | " + $_.ScriptStackTrace)
} finally {
    if ($versionId -ne "" -and $conversationId -ne "") {
        try { Invoke-Api -Context $context -Method "PATCH" -Path "/api/versions/$versionId/conversations/$conversationId" -Token $context.Token -Body @{ model_override = $originalOverride } | Out-Null } catch { }
    }
    $exitCode = Complete-AcceptanceScenario -Scenario $scenario -Message "S8 UI surfaces"
}

exit $exitCode
