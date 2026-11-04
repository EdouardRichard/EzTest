#requires -Version 5.1
<#
.SYNOPSIS
    S9 knowledge acceptance for EzTest 0.2.1.

.DESCRIPTION
    Exercises the knowledge loop required by spec 13.2 S9:
      * a successful login should sediment a login_recipe knowledge item
        (observed through the knowledge API);
      * an approved knowledge item is injected into a new version's agent
        context packet (pinned facts + knowledge section) and is searchable;
      * revoking the item removes it from search and from the next agent run's
        context packet (the write must actually take effect);
      * the clarification team room remains readable.

    The login-sediment half depends on the discovery sub-loop producing a
    login_session fact. When the product does not produce it (the known S1 gap)
    the check fails with the observed fact counts instead of being skipped.
    The create -> approve -> inject -> revoke half is driven through the public
    API and a real LLM chat turn.

    Evidence is written under
    docs/version-V0.2.1/acceptance/evidence/s7-s10/s9/.

.PARAMETER InjectionTimeoutSeconds
    How long to wait for the context packet with the injected item. Default 180.

.PARAMETER RevokeTimeoutSeconds
    How long to wait for the post-revoke context packet. Default 180.

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
    [int] $InjectionTimeoutSeconds = 180,
    [int] $RevokeTimeoutSeconds = 180
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
$scenario = New-AcceptanceScenario -Context $context -Name "s9" -Title "Knowledge: sediment, inject, revoke (S9)"

$exitCode = 2
$runSuffix = (Get-Date).ToString("yyyyMMdd-HHmmss")
$token = "S9KNOW" + (Get-Date).ToString("HHmmss")
$knowledgeId = ""
$versionId = ""
$injectedPacketHash = ""

function Get-PacketText {
    param($Packet)
    if ($null -eq $Packet) { return "" }
    $segments = Get-JsonProp -Object $Packet -Name "segments" -Default @()
    $chunks = @()
    foreach ($segment in @($segments)) {
        $chunks += [string](Get-JsonProp -Object $segment -Name "content" -Default "")
    }
    return ($chunks -join "`n")
}

function Get-LatestPacket {
    param($Context, [string] $VersionId)
    $response = Invoke-Api -Context $Context -Method "GET" -Path "/api/versions/$VersionId/context-packets/latest" -Token $Context.Token
    if ($response.Status -ne 200) { return [pscustomobject]@{ HttpStatus = $response.Status; Packet = $null; Text = ""; PrefixHash = "" } }
    $packet = $response.Data
    return [pscustomobject]@{
        HttpStatus = $response.Status
        Packet = $packet
        Text = (Get-PacketText -Packet $packet)
        PrefixHash = [string](Get-JsonProp -Object $packet -Name "prefix_hash" -Default "")
        BriefingHash = [string](Get-JsonProp -Object $packet -Name "briefing_hash" -Default "")

    }
}

try {
    $injectedPacketId = ""
    $injectedBriefingHash = ""
    $injectedText = ""
    Write-AcceptanceLog -Scenario $scenario -Message "S9 knowledge acceptance; injectionTimeout=${InjectionTimeoutSeconds}s revokeTimeout=${RevokeTimeoutSeconds}s token=$token"

    $login = Connect-EzTest -Context $context
    Add-AcceptanceCheck -Scenario $scenario -Name "EzTest admin login" -Passed $login.Ok -Severity blocker -Detail ($(if ($login.Ok) { "admin session established" } else { "admin credentials rejected" }))
    if (-not $login.Ok) {
        $scenario.ErrorMessage = "S9 blocked: EzTest admin login failed"
    } else {
        # ------------------------------------------------------------------
        # 1. Login-success sediment. The product must have written a
        #    login_recipe knowledge item from a discovered login_session fact.
        # ------------------------------------------------------------------
        $itemsResponse = Invoke-ApiOrFail -Context $context -Method "GET" -Path "/api/knowledge/items?limit=100" -Token $context.Token -Label "list knowledge items"
        $allItems = @($itemsResponse.Data)
        $loginRecipes = @($allItems | Where-Object {
            $tags = @(Get-JsonProp -Object $_ -Name "tags" -Default @())
            $sourceVersionId = Get-JsonProp -Object $_ -Name "source_version_id" -Default $null
            ($tags -contains "kind:login_recipe") -and ($null -ne $sourceVersionId)
        })
        $loginSessionFacts = 0
        $versionsResponse = Invoke-ApiOrFail -Context $context -Method "GET" -Path "/api/versions?limit=20" -Token $context.Token -Label "versions for sediment check"
        foreach ($version in @($versionsResponse.Data)) {
            $candidateId = [string](Get-JsonProp -Object $version -Name "id" -Default "")
            if ([string]::IsNullOrWhiteSpace($candidateId)) { continue }
            try {
                $facts = @(Get-VersionFacts -Context $context -VersionId $candidateId -Limit 500)
                $loginSessionFacts += @($facts | Where-Object { [string](Get-JsonProp -Object $_ -Name "kind" -Default "") -eq "login_session" }).Count
            } catch { }
        }
        Write-EvidenceJson -Path (Join-Path $scenario.Directory "s9-sediment-baseline.json") -Value ([pscustomobject]@{
            knowledge_items = $allItems.Count
            login_recipe_items = $loginRecipes.Count
            login_session_facts = $loginSessionFacts
            login_recipe_ids = @($loginRecipes | ForEach-Object { [string](Get-JsonProp -Object $_ -Name "id" -Default "") })
        })
        Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "s9-sediment-baseline.json") -Kind "api" -Note "knowledge sediment baseline"
        Add-AcceptanceCheck -Scenario $scenario -Name "successful login sediments login_recipe knowledge" -Passed ($loginRecipes.Count -gt 0 -and $loginSessionFacts -gt 0) -Severity assertion `
            -Detail ("login_recipe_items=$($loginRecipes.Count) login_session_facts=$loginSessionFacts in the newest 20 versions")

        # ------------------------------------------------------------------
        # 2. Create and approve a distinctive global knowledge item.
        # ------------------------------------------------------------------
        $titleText = "S9 acceptance login recipe " + $token
        $contentText = "$token admin login uses the acceptance account; open /login and submit the admin credentials. This knowledge must be injected into new versions."
        $createResponse = Invoke-Api -Context $context -Method "POST" -Path "/api/knowledge/items" -Token $context.Token -Body @{
            title   = $titleText
            content = $contentText
            scope   = "global"
            tags    = @("acceptance", "s9", $token)
        }
        if ($createResponse.Status -notin @(200, 201)) {
            Add-AcceptanceCheck -Scenario $scenario -Name "knowledge item created as draft" -Passed $false -Severity assertion -Detail ("HTTP $($createResponse.Status) $($createResponse.Text)")
        } else {
            $knowledgeId = [string](Get-JsonProp -Object $createResponse.Data -Name "id" -Default "")
            $createdStatus = [string](Get-JsonProp -Object $createResponse.Data -Name "status" -Default "")
            Add-AcceptanceCheck -Scenario $scenario -Name "knowledge item created as draft" -Passed ($knowledgeId -ne "" -and $createdStatus -eq "draft") -Severity assertion `
                -Detail "id=$knowledgeId status=$createdStatus"
        }

        if ($knowledgeId -ne "") {
            $approveResponse = Invoke-Api -Context $context -Method "POST" -Path "/api/knowledge/items/$knowledgeId/approve" -Token $context.Token -Body @{ reason = "S9 acceptance approval" }
            $approvedStatus = [string](Get-JsonProp -Object $approveResponse.Data -Name "status" -Default "")
            Add-AcceptanceCheck -Scenario $scenario -Name "knowledge item approved" -Passed ($approveResponse.Status -eq 200 -and $approvedStatus -eq "approved") -Severity assertion `
                -Detail "HTTP $($approveResponse.Status) status=$approvedStatus"

            $searchApproved = Invoke-Api -Context $context -Method "GET" -Path ("/api/knowledge/search?q=" + [Uri]::EscapeDataString($token) + "&limit=20") -Token $context.Token
            $searchApprovedIds = @(@($searchApproved.Data) | ForEach-Object { [string](Get-JsonProp -Object $_ -Name "id" -Default "") })
            Add-AcceptanceCheck -Scenario $scenario -Name "approved knowledge is searchable" -Passed ($searchApproved.Status -eq 200 -and $searchApprovedIds -contains $knowledgeId) -Severity assertion `
                -Detail ("HTTP $($searchApproved.Status) ids=$($searchApprovedIds -join ',')")

            # --------------------------------------------------------------
            # 3. New version: approved knowledge is injected into the run's
            #    context packet (pinned facts + knowledge section).
            # --------------------------------------------------------------
            $binding = Resolve-AcceptanceSutBinding -Context $context
            $version = New-AcceptanceVersion -Context $context -Title ("$token acceptance version") -Binding $binding.Binding
            $versionId = [string](Get-JsonProp -Object $version -Name "id" -Default "")
            $conversation = New-AcceptanceConversation -Context $context -VersionId $versionId -Kind "clarification" -Title "S9 injection conversation"
            $conversationId = [string](Get-JsonProp -Object $conversation -Name "id" -Default "")
            Add-AcceptanceCheck -Scenario $scenario -Name "S9 version and conversation created" -Passed ($versionId -ne "" -and $conversationId -ne "") -Severity blocker `
                -Detail "version=$versionId conversation=$conversationId"

            New-AcceptanceUserMessage -Context $context -VersionId $versionId -ConversationId $conversationId -Text "Reply with the single word ok." | Out-Null
            $marker = "[K-$knowledgeId]"
            $deadline = (Get-Date).AddSeconds($InjectionTimeoutSeconds)
            $injected = $null
            while ((Get-Date) -lt $deadline) {
                $latest = Get-LatestPacket -Context $context -VersionId $versionId
                if ($latest.HttpStatus -eq 200 -and $latest.Text -match [regex]::Escape($marker)) {
                    $injected = $latest
                    break
                }
                Start-Sleep -Seconds 3
            }
            if ($null -ne $injected) {
                $injectedPacketHash = $injected.PrefixHash
                $injectedBriefingHash = $injected.BriefingHash
                $injectedText = $injected.Text
                Write-EvidenceJson -Path (Join-Path $scenario.Directory "s9-context-packet-injected.json") -Value $injected.Packet
                Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "s9-context-packet-injected.json") -Kind "api" -Note "context packet with injected knowledge"
            }
            $hasKnowledgeSection = $false
            $hasPinnedMarker = $false
            if ($null -ne $injected) {
                $segments = @(Get-JsonProp -Object $injected.Packet -Name "segments" -Default @())
                foreach ($segment in $segments) {
                    $segmentText = [string](Get-JsonProp -Object $segment -Name "content" -Default "")
                    if ($segmentText -match [regex]::Escape($marker)) {
                        $hasPinnedMarker = $true
                        if ($segmentText -match "## 知识" -or $segmentText -match "\[K-") { $hasKnowledgeSection = $true }
                    }
                }
            }
            Add-AcceptanceCheck -Scenario $scenario -Name "approved knowledge injected into the new version context" -Passed ($null -ne $injected) -Severity assertion `
                -Detail ($(if ($null -ne $injected) { "marker=$marker prefix_hash=$injectedPacketHash" } else { "no context packet contained $marker within ${InjectionTimeoutSeconds}s" }))
            Add-AcceptanceCheck -Scenario $scenario -Name "injected knowledge is carried by the packet segments" -Passed ($hasPinnedMarker -and $hasKnowledgeSection) -Severity assertion `
                -Detail "marker_in_segment=$hasPinnedMarker knowledge_section=$hasKnowledgeSection"

            # --------------------------------------------------------------
            # 4. Revoke: search and the next run exclude the item.
            # --------------------------------------------------------------
            $revokeResponse = Invoke-Api -Context $context -Method "POST" -Path "/api/knowledge/items/$knowledgeId/revoke" -Token $context.Token -Body @{ reason = "S9 acceptance revoke" }
            $revokedStatus = [string](Get-JsonProp -Object $revokeResponse.Data -Name "status" -Default "")
            $revokedAt = [string](Get-JsonProp -Object $revokeResponse.Data -Name "revoked_at" -Default "")
            Add-AcceptanceCheck -Scenario $scenario -Name "knowledge item revoked" -Passed ($revokeResponse.Status -eq 200 -and $revokedStatus -eq "revoked" -and $revokedAt -ne "") -Severity assertion `
                -Detail "HTTP $($revokeResponse.Status) status=$revokedStatus revoked_at=$revokedAt"

            $searchRevoked = Invoke-Api -Context $context -Method "GET" -Path ("/api/knowledge/search?q=" + [Uri]::EscapeDataString($token) + "&limit=20") -Token $context.Token
            $searchRevokedIds = @(@($searchRevoked.Data) | ForEach-Object { [string](Get-JsonProp -Object $_ -Name "id" -Default "") })
            Add-AcceptanceCheck -Scenario $scenario -Name "revoked knowledge is excluded from search" -Passed ($searchRevoked.Status -eq 200 -and -not ($searchRevokedIds -contains $knowledgeId)) -Severity assertion `
                -Detail ("HTTP $($searchRevoked.Status) ids=$($searchRevokedIds -join ',')")

            $conversation2 = New-AcceptanceConversation -Context $context -VersionId $versionId -Kind "clarification" -Title "S9 post-revoke conversation"
            $conversation2Id = [string](Get-JsonProp -Object $conversation2 -Name "id" -Default "")
            New-AcceptanceUserMessage -Context $context -VersionId $versionId -ConversationId $conversation2Id -Text "Reply with the single word ok." | Out-Null
            $deadline = (Get-Date).AddSeconds($RevokeTimeoutSeconds)
            $postRevokePacket = $null
            while ((Get-Date) -lt $deadline) {
                $latest = Get-LatestPacket -Context $context -VersionId $versionId
                if ($latest.HttpStatus -eq 200 -and -not ($latest.Text -match [regex]::Escape($marker)) -and (($latest.BriefingHash -ne "" -and $latest.BriefingHash -ne $injectedBriefingHash) -or ($latest.Text -ne "" -and $latest.Text -ne $injectedText))) {
                    $postRevokePacket = $latest
                    break
                }
                Start-Sleep -Seconds 3
            }
            if ($null -ne $postRevokePacket) {
                Write-EvidenceJson -Path (Join-Path $scenario.Directory "s9-context-packet-after-revoke.json") -Value $postRevokePacket.Packet
                Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "s9-context-packet-after-revoke.json") -Kind "api" -Note "context packet after knowledge revoke"
            }
            Add-AcceptanceCheck -Scenario $scenario -Name "revoked knowledge is absent from the next context packet" -Passed ($null -ne $postRevokePacket) -Severity assertion `
                -Detail ($(if ($null -ne $postRevokePacket) { "new packet prefix_hash=$($postRevokePacket.PrefixHash)" } else { "no new context packet without $marker within ${RevokeTimeoutSeconds}s" }))

            $detailResponse = Invoke-Api -Context $context -Method "GET" -Path "/api/knowledge/items/$knowledgeId" -Token $context.Token
            $detailStatus = [string](Get-JsonProp -Object $detailResponse.Data -Name "status" -Default "")
            $detailRevokedAt = [string](Get-JsonProp -Object $detailResponse.Data -Name "revoked_at" -Default "")
            Add-AcceptanceCheck -Scenario $scenario -Name "revoked knowledge detail reports the persisted state" -Passed ($detailResponse.Status -eq 200 -and $detailStatus -eq "revoked" -and $detailRevokedAt -ne "") -Severity assertion `
                -Detail "HTTP $($detailResponse.Status) status=$detailStatus revoked_at=$detailRevokedAt"
        }

        # ------------------------------------------------------------------
        # 5. Team room remains readable (supporting evidence; the spec S9 flow
        #    does not require a new post).
        # ------------------------------------------------------------------
        if ($versionId -ne "") {
            try {
                $teamRoom = Invoke-Api -Context $context -Method "GET" -Path "/api/versions/$versionId/team-rooms/clarification?limit=10" -Token $context.Token
                Write-EvidenceJson -Path (Join-Path $scenario.Directory "s9-team-room.json") -Value $teamRoom.Json
                Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "s9-team-room.json") -Kind "api" -Note "clarification team room"
                Add-AcceptanceCheck -Scenario $scenario -Name "clarification team room readable" -Passed ($teamRoom.Status -eq 200) -Severity warning -Detail "HTTP $($teamRoom.Status)"
            } catch {
                Add-AcceptanceCheck -Scenario $scenario -Name "clarification team room readable" -Passed $false -Severity warning -Detail $_.Exception.Message
            }
        }

        Add-AcceptanceCheck -Scenario $scenario -Name "source-stale (suspect) marking observed" -Passed $false -Severity warning `
            -Detail "no source-version close/stale lifecycle was driven; background version source handling is not exercised by this S9 run"
    }
} catch {
    $scenario.ErrorMessage = $_.Exception.Message
    Add-AcceptanceCheck -Scenario $scenario -Name "S9 unexpected error" -Passed $false -Severity blocker -Detail ($_.Exception.Message + " | " + $_.ScriptStackTrace)
} finally {
    $exitCode = Complete-AcceptanceScenario -Scenario $scenario -Message "S9 knowledge sediment/inject/revoke"
}

exit $exitCode
