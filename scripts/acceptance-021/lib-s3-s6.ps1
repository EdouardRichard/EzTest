#requires -Version 5.1
<#
.SYNOPSIS
    Shared helpers for the EzTest 0.2.1 S3-S6 acceptance scenarios.

.DESCRIPTION
    Builds on scripts/acceptance-021/lib.ps1 and adds the read helpers and
    read-only database probes the design/confirm/execute/report scenarios need.
    The library never writes application data through SQL: the only DB use is a
    read-only SELECT for job/attempt evidence, because the public work-item API
    fences out the legacy execution rows. All state changes go through the real
    HTTP API.
#>

Set-StrictMode -Version 3.0
$ErrorActionPreference = "Stop"

# ---------------------------------------------------------------------------
# Design graph
# ---------------------------------------------------------------------------

function Get-AcceptanceDesignGraph {
    param([Parameter(Mandatory = $true)] $Context, [Parameter(Mandatory = $true)] [string] $VersionId)
    $response = Invoke-ApiOrFail -Context $Context -Method "GET" -Path "/api/versions/$VersionId/design/graph" `
        -Token $Context.Token -Label "GET design graph"
    return $response.Data
}

function Get-AcceptanceDesignOverview {
    param([Parameter(Mandatory = $true)] $Context, [Parameter(Mandatory = $true)] [string] $VersionId)
    $response = Invoke-ApiOrFail -Context $Context -Method "GET" -Path "/api/versions/$VersionId/design/overview" `
        -Token $Context.Token -Label "GET design overview"
    return $response.Data
}

function Get-AcceptanceCaseDetail {
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $VersionId,
        [Parameter(Mandatory = $true)] [string] $CaseId
    )
    $response = Invoke-ApiOrFail -Context $Context -Method "GET" -Path "/api/versions/$VersionId/design/cases/$CaseId" `
        -Token $Context.Token -Label "GET case detail"
    return $response.Data
}

function Get-AcceptanceCaseCards {
    <#
        Flattens the design graph into case cards. Empty arrays are returned as
        empty arrays (not $null) so callers can safely measure them.
    #>
    param([Parameter(Mandatory = $false)] $Graph)
    $cards = @()
    if ($null -eq $Graph) { return $cards }
    foreach ($requirement in @(Get-JsonProp -Object $Graph -Name "requirements" -Default @())) {
        $requirementId = [string](Get-JsonProp -Object $requirement -Name "requirement_id" -Default "")
        $requirementTitle = [string](Get-JsonProp -Object $requirement -Name "title" -Default "")
        $requirementStatus = [string](Get-JsonProp -Object $requirement -Name "status" -Default "")
        foreach ($scenario in @(Get-JsonProp -Object $requirement -Name "scenarios" -Default @())) {
            $scenarioId = [string](Get-JsonProp -Object $scenario -Name "id" -Default "")
            $scenarioName = [string](Get-JsonProp -Object $scenario -Name "name" -Default "")
            foreach ($card in @(Get-JsonProp -Object $scenario -Name "cases" -Default @())) {
                $cards += [pscustomobject]@{
                    requirement_id    = $requirementId
                    requirement_title = $requirementTitle
                    requirement_status = $requirementStatus
                    scenario_id       = $scenarioId
                    scenario_name     = $scenarioName
                    case_id           = [string](Get-JsonProp -Object $card -Name "case_id" -Default "")
                    case_version_id   = [string](Get-JsonProp -Object $card -Name "current_version_id" -Default "")
                    title             = [string](Get-JsonProp -Object $card -Name "title" -Default "")
                    status            = [string](Get-JsonProp -Object $card -Name "status" -Default "")
                    execution_verdict = [string](Get-JsonProp -Object $card -Name "execution_verdict" -Default "")
                    confirmable       = [bool](Get-JsonProp -Object $card -Name "confirmable" -Default $false)
                    page_ids          = @(Get-JsonProp -Object $card -Name "page_ids" -Default @())
                    api_ids           = @(Get-JsonProp -Object $card -Name "api_ids" -Default @())
                }
            }
        }
    }
    return $cards
}

function Get-AcceptanceDesignConversation {
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $VersionId
    )
    $conversations = @(Get-VersionConversations -Context $Context -VersionId $VersionId -Limit 100)
    $design = @($conversations | Where-Object { [string](Get-JsonProp -Object $_ -Name "kind" -Default "") -eq "design" })
    if ($design.Count -eq 0) { return $null }
    return ($design | Sort-Object { [long](Get-JsonProp -Object $_ -Name "id" -Default "0") } | Select-Object -Last 1)
}

# ---------------------------------------------------------------------------
# Execution reads
# ---------------------------------------------------------------------------

function Get-AcceptanceExecutionOverview {
    param([Parameter(Mandatory = $true)] $Context, [Parameter(Mandatory = $true)] [string] $VersionId)
    $response = Invoke-ApiOrFail -Context $Context -Method "GET" -Path "/api/versions/$VersionId/execution/overview" `
        -Token $Context.Token -Label "GET execution overview"
    return $response.Data
}

function Get-AcceptanceExecutionIntents {
    param([Parameter(Mandatory = $true)] $Context, [Parameter(Mandatory = $true)] [string] $VersionId, [int] $Limit = 100)
    $response = Invoke-ApiOrFail -Context $Context -Method "GET" -Path "/api/versions/$VersionId/execution/intents?limit=$Limit" `
        -Token $Context.Token -Label "GET execution intents"
    return @($response.Data)
}

function Get-AcceptanceWaves {
    param([Parameter(Mandatory = $true)] $Context, [Parameter(Mandatory = $true)] [string] $VersionId, [int] $Limit = 50)
    $response = Invoke-ApiOrFail -Context $Context -Method "GET" -Path "/api/versions/$VersionId/waves?limit=$Limit" `
        -Token $Context.Token -Label "GET execution waves"
    return @($response.Data)
}

function Get-AcceptanceWave {
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $VersionId,
        [Parameter(Mandatory = $true)] [string] $WaveId
    )
    $response = Invoke-ApiOrFail -Context $Context -Method "GET" -Path "/api/versions/$VersionId/waves/$WaveId" `
        -Token $Context.Token -Label "GET execution wave"
    return $response.Data
}

function Get-AcceptanceWaveCaseRuns {
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $VersionId,
        [Parameter(Mandatory = $true)] [string] $WaveId,
        [int] $Limit = 100
    )
    $response = Invoke-ApiOrFail -Context $Context -Method "GET" -Path "/api/versions/$VersionId/waves/$WaveId/case-runs?limit=$Limit" `
        -Token $Context.Token -Label "GET wave case runs"
    return @($response.Data)
}

function Get-AcceptanceCaseRun {
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $VersionId,
        [Parameter(Mandatory = $true)] [string] $CaseRunId
    )
    $response = Invoke-ApiOrFail -Context $Context -Method "GET" -Path "/api/versions/$VersionId/case-runs/$CaseRunId" `
        -Token $Context.Token -Label "GET case run"
    return $response.Data
}

function New-AcceptanceManualIntent {
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $VersionId,
        [Parameter(Mandatory = $true)] [string] $CaseVersionId,
        [ValidateSet("manual", "retest")] [string] $Purpose = "manual"
    )
    return Invoke-Api -Context $Context -Method "POST" -Path "/api/versions/$VersionId/execution/intents" `
        -Token $Context.Token -Body @{ case_version_id = $CaseVersionId; purpose = $Purpose }
}

function Invoke-AcceptanceWaveAction {
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $VersionId,
        [Parameter(Mandatory = $true)] [string] $WaveId,
        [Parameter(Mandatory = $true)] [ValidateSet("pause", "resume", "cancel")] [string] $Action,
        [Parameter(Mandatory = $false)] [string] $Reason = "acceptance"
    )
    $body = $null
    if ($Action -eq "pause") { $body = @{ mode = "graceful"; reason = $Reason } }
    return Invoke-Api -Context $Context -Method "POST" -Path "/api/versions/$VersionId/waves/$WaveId/$Action" `
        -Token $Context.Token -Body $body
}

function Invoke-AcceptanceIntentCancel {
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $VersionId,
        [Parameter(Mandatory = $true)] [string] $IntentId
    )
    return Invoke-Api -Context $Context -Method "POST" -Path "/api/versions/$VersionId/execution/intents/$IntentId/cancel" `
        -Token $Context.Token
}

# ---------------------------------------------------------------------------
# Reports / issues
# ---------------------------------------------------------------------------

function Get-AcceptanceVersionReports {
    param([Parameter(Mandatory = $true)] $Context, [Parameter(Mandatory = $true)] [string] $VersionId, [int] $Limit = 50)
    $response = Invoke-ApiOrFail -Context $Context -Method "GET" -Path "/api/versions/$VersionId/reports?limit=$Limit" `
        -Token $Context.Token -Label "GET version reports"
    return @($response.Data)
}

function Get-AcceptanceReportDetail {
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $VersionId,
        [Parameter(Mandatory = $true)] [string] $ReportId
    )
    $response = Invoke-ApiOrFail -Context $Context -Method "GET" -Path "/api/versions/$VersionId/reports/$ReportId" `
        -Token $Context.Token -Label "GET report detail"
    return $response.Data
}

function Get-AcceptanceVersionIssues {
    param([Parameter(Mandatory = $true)] $Context, [Parameter(Mandatory = $true)] [string] $VersionId, [int] $Limit = 100)
    $response = Invoke-Api -Context $Context -Method "GET" -Path "/api/versions/$VersionId/issues?limit=$Limit" -Token $Context.Token
    if (-not $response.Ok) {
        return [pscustomobject]@{ Ok = $false; Items = @(); Error = "HTTP $($response.Status)" }
    }
    return [pscustomobject]@{ Ok = $true; Items = @($response.Data); Error = "" }
}

function Invoke-AcceptanceRequirementReopen {
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $VersionId,
        [Parameter(Mandatory = $true)] [string] $RequirementId,
        [Parameter(Mandatory = $false)] [string] $Reason = "acceptance retest"
    )
    return Invoke-Api -Context $Context -Method "POST" `
        -Path "/api/versions/$VersionId/requirements/$RequirementId/reopen" `
        -Token $Context.Token -Body @{ reason = $Reason }
}

# ---------------------------------------------------------------------------
# Conversation parts (job cards / thinking / tool calls)
# ---------------------------------------------------------------------------

function Get-AcceptanceMessages {
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $VersionId,
        [Parameter(Mandatory = $true)] [string] $ConversationId,
        [int] $Limit = 200
    )
    return @(Get-ConversationMessages -Context $Context -VersionId $VersionId -ConversationId $ConversationId -Limit $Limit)
}

function Get-AcceptanceMessagePartTypes {
    param([Parameter(Mandatory = $false)] $Message)
    $types = @()
    foreach ($part in @(Get-JsonProp -Object $Message -Name "parts" -Default @())) {
        $type = [string](Get-JsonProp -Object $part -Name "type" -Default "")
        if ($type) { $types += $type }
    }
    return $types
}

function Get-AcceptanceJobCards {
    param([Parameter(Mandatory = $false)] [AllowEmptyCollection()] $Messages)
    $cards = @()
    foreach ($message in @($Messages)) {
        foreach ($part in @(Get-JsonProp -Object $message -Name "parts" -Default @())) {
            if ([string](Get-JsonProp -Object $part -Name "type" -Default "") -ne "job_card") { continue }
            $cards += [pscustomobject]@{
                message_id = [string](Get-JsonProp -Object $message -Name "id" -Default "")
                job_id     = [string](Get-JsonProp -Object $part -Name "job_id" -Default "")
                kind       = [string](Get-JsonProp -Object $part -Name "kind" -Default "")
                status     = [string](Get-JsonProp -Object $part -Name "status" -Default "")
                current    = [int](Get-JsonProp -Object $part -Name "current" -Default 0)
                total      = [int](Get-JsonProp -Object $part -Name "total" -Default 0)
                cancelable = [bool](Get-JsonProp -Object $part -Name "cancelable" -Default $false)
            }
        }
    }
    return $cards
}

function Get-AcceptanceToolCalls {
    param([Parameter(Mandatory = $false)] [AllowEmptyCollection()] $Messages)
    $calls = @()
    foreach ($message in @($Messages)) {
        foreach ($part in @(Get-JsonProp -Object $message -Name "parts" -Default @())) {
            if ([string](Get-JsonProp -Object $part -Name "type" -Default "") -ne "tool_call") { continue }
            $payload = Get-JsonProp -Object $part -Name "payload" -Default $part
            $calls += [pscustomobject]@{
                message_id = [string](Get-JsonProp -Object $message -Name "id" -Default "")
                tool       = [string](Get-JsonProp -Object $payload -Name "name" -Default (Get-JsonProp -Object $payload -Name "tool" -Default ""))
                call_id    = [string](Get-JsonProp -Object $payload -Name "call_id" -Default "")
            }
        }
    }
    return $calls
}

# ---------------------------------------------------------------------------
# State hand-off between S3 -> S4 -> S5 -> S6
# ---------------------------------------------------------------------------

function Get-AcceptanceStatePath {
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $false)] [string] $StatePath = ""
    )
    if (-not [string]::IsNullOrWhiteSpace($StatePath)) {
        if ([System.IO.Path]::IsPathRooted($StatePath)) { return $StatePath }
        return (Join-Path $Context.RepoRoot $StatePath)
    }
    return (Join-Path $Context.EvidenceRoot "state.json")
}

function Read-AcceptanceState {
    param([Parameter(Mandatory = $true)] [string] $Path)
    $state = @{}
    if (-not (Test-Path -LiteralPath $Path)) { return $state }
    try {
        $parsed = [System.IO.File]::ReadAllText($Path, [System.Text.Encoding]::UTF8) | ConvertFrom-Json -ErrorAction Stop
        foreach ($property in $parsed.PSObject.Properties) { $state[$property.Name] = $property.Value }
    } catch { }
    return $state
}

function Save-AcceptanceState {
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $Path,
        [Parameter(Mandatory = $true)] $State
    )
    $directory = Split-Path -Parent $Path
    if ($directory) { New-Item -ItemType Directory -Force -Path $directory | Out-Null }
    Write-EvidenceJson -Path $Path -Value ([pscustomobject]$State) -NoRedaction
}

function Update-AcceptanceState {
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $Path,
        [Parameter(Mandatory = $true)] [hashtable] $Values
    )
    $state = Read-AcceptanceState -Path $Path
    foreach ($key in $Values.Keys) { $state[$key] = $Values[$key] }
    Save-AcceptanceState -Context $Context -Path $Path -State $state
    return $state
}

# ---------------------------------------------------------------------------
# Read-only database probe (job/attempt visibility)
# ---------------------------------------------------------------------------

function Get-AcceptanceDbInfo {
    param([Parameter(Mandatory = $true)] $Context)
    $url = [string](Get-AcceptanceSetting -Context $Context -Name "EZTEST_ACCEPTANCE_DATABASE_URL" -Default "")
    if ([string]::IsNullOrWhiteSpace($url)) {
        if ($Context.DotEnv.ContainsKey("EZTEST_DATABASE_URL")) { $url = [string]$Context.DotEnv["EZTEST_DATABASE_URL"] }
    }
    if ([string]::IsNullOrWhiteSpace($url)) { return $null }
    try { $uri = [System.Uri]::new($url) } catch { return $null }
    $userInfo = [string]$uri.UserInfo
    $user = ""
    $password = ""
    if ($userInfo.Contains(":")) {
        $separator = $userInfo.IndexOf(":")
        $user = $userInfo.Substring(0, $separator)
        $password = [System.Uri]::UnescapeDataString($userInfo.Substring($separator + 1))
    } elseif ($userInfo) {
        $user = $userInfo
    }
    $psql = Join-Path $Context.RepoRoot ".tools\pgsql\bin\psql.exe"
    if (-not (Test-Path -LiteralPath $psql)) { $psql = "psql" }
    return [pscustomobject]@{
        Host             = $uri.Host
        Port             = if ($uri.Port -gt 0) { $uri.Port } else { 5432 }
        User             = $user
        Password         = $password
        OriginalDatabase = $uri.AbsolutePath.TrimStart('/')
        Psql             = $psql
    }
}

function Invoke-AcceptancePsql {
    param(
        [Parameter(Mandatory = $false)] $Info,
        [Parameter(Mandatory = $true)] [string] $Database,
        [Parameter(Mandatory = $true)] [string] $Sql
    )
    if ($null -eq $Info) {
        return [pscustomobject]@{ Ok = $false; Rows = @(); Error = "no database URL available" }
    }
    $previous = $env:PGPASSWORD
    $exit = 1
    $output = @()
    try {
        $env:PGPASSWORD = $Info.Password
        $output = @(& $Info.Psql --no-psqlrc -h $Info.Host -p $Info.Port -U $Info.User -d $Database `
            -A -t -F "`t" -v ON_ERROR_STOP=1 -c $Sql 2>&1)
        $exit = $LASTEXITCODE
    } catch {
        $output = @($_.Exception.Message)
        $exit = 1
    } finally {
        $env:PGPASSWORD = $previous
    }
    if ($exit -ne 0) {
        $message = (@($output) | ForEach-Object { [string]$_ }) -join " "
        if ($message.Length -gt 500) { $message = $message.Substring(0, 500) }
        return [pscustomobject]@{ Ok = $false; Rows = @(); Error = $message }
    }
    return [pscustomobject]@{ Ok = $true; Rows = @($output | ForEach-Object { [string]$_ }); Error = "" }
}

function Invoke-AcceptanceDbQuery {
    <#
        Runs one read-only SELECT against the acceptance database that actually
        contains the version. Candidate databases are tried in order; the first
        one that owns the version wins. When VersionId is 0 the first candidate
        is used (the empty-version negative check does not query the DB).
    #>
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $Sql,
        [Parameter(Mandatory = $false)] [long] $VersionId = 0
    )
    $info = Get-AcceptanceDbInfo -Context $Context
    if ($null -eq $info) {
        return [pscustomobject]@{ Ok = $false; Database = ""; Rows = @(); Error = "no database URL in environment/.env" }
    }
    $candidates = @()
    $configured = [Environment]::GetEnvironmentVariable("EZTEST_ACCEPTANCE_DB_NAME", "Process")
    if (-not [string]::IsNullOrWhiteSpace($configured)) { $candidates += $configured }
    $candidates += "eztest_v021"
    if (-not [string]::IsNullOrWhiteSpace($info.OriginalDatabase)) { $candidates += $info.OriginalDatabase }
    $unique = @()
    foreach ($candidate in $candidates) {
        if (-not [string]::IsNullOrWhiteSpace($candidate) -and $unique -notcontains $candidate) { $unique += $candidate }
    }
    $chosen = $unique[0]
    if ($VersionId -gt 0) {
        foreach ($candidate in $unique) {
            $probe = Invoke-AcceptancePsql -Info $info -Database $candidate -Sql "SELECT 1 FROM version WHERE id = $VersionId"
            if ($probe.Ok -and @($probe.Rows) -contains "1") { $chosen = $candidate; break }
        }
    }
    $result = Invoke-AcceptancePsql -Info $info -Database $chosen -Sql $Sql
    return [pscustomobject]@{ Ok = $result.Ok; Database = $chosen; Rows = @($result.Rows); Error = $result.Error }
}

function Get-AcceptanceSplitRow {
    param([Parameter(Mandatory = $false)] [AllowEmptyString()] [string] $Line)
    if ($null -eq $Line) { return @() }
    return @($Line.Split("`t"))
}
