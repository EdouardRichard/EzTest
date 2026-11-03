#requires -Version 5.1
<#
.SYNOPSIS
    Final unattended acceptance entrypoint for EzTest 0.2.1 (S0-S10).

.DESCRIPTION
    Runs the numbered acceptance scenarios in order, stopping at the first
    non-passing scenario by default (-KeepGoing aggregates instead), and writes
    a machine-readable summary plus a human-readable report.

    The runner does not implement any acceptance criterion itself. It delegates
    to scripts/acceptance-021/sN-*.ps1 and judges each scenario by two things:

      1. the child process exit code (0 = passed, 1 = failed assertion,
         2 = blocked environment); and
      2. the scenario's structured result.json status.

    A scenario is recorded as PASS only when BOTH agree on "passed". An exit
    code of 0 without a readable result.json is recorded as a harness failure,
    never as a pass. Missing or ambiguous scenario scripts are recorded as
    MISSING / AMBIGUOUS and make the run non-zero. Scenarios that were not
    reached because the run stopped are recorded as NOT RUN.

    Evidence layout (default):

        docs/version-V0.2.1/acceptance/evidence/final/
          summary.json              machine-readable result of this run
          run-all.log               orchestration log (includes child output)
          s0/ s1/ ...               per-scenario evidence written by the child
        docs/version-V0.2.1/acceptance/final-report.md

    Exit codes:

        0 = all S0-S10 scenarios passed
        1 = at least one scenario failed an acceptance assertion
        2 = blocked (environment prerequisite, missing/ambiguous harness
            script, or the runner itself failed)

.PARAMETER RequirementInput
    Task 88 small requirement input (small-a.md / small-b.feature /
    small-b.txt / small-c.docx). Exactly one requirement source is required:
    either this parameter or -FullSutDoc. Supplying neither is a preflight
    error (exit 2); the runner never falls back to the full SUT document.

.PARAMETER ExpectedRequirements
    Expected requirement count for the supplied input (e.g. 3). When omitted it
    is derived from the input's FR/NFR ID set; when supplied it must match it,
    otherwise the preflight fails instead of importing a different document.

.PARAMETER ExpectedFrIds
    Optional comma-separated expected FR/NFR ID set. When omitted it is derived
    from the input; when supplied it must match the input exactly.

.PARAMETER FullSutDoc
    Explicit legacy mode: import the full SUT requirement document. Requires
    -FullSutDocPath: the preflight resolves the document itself and fails
    instead of falling back to a scenario default. Cannot be combined with
    -RequirementInput.

.PARAMETER FullSutDocPath
    Path of the explicit legacy full SUT requirement document. Required when
    -FullSutDoc is set; validated by the runner preflight.

.PARAMETER KeepGoing
    Run every available scenario instead of stopping at the first non-passing
    one. The exit code is still non-zero if anything did not pass.

.PARAMETER ScenarioDirectory
    Directory containing the sN-*.ps1 scenario scripts. Defaults to this
    script's directory. Mostly used by the runner self-test.

.PARAMETER EvidenceRoot
    Evidence root passed to every scenario. Defaults to
    docs/version-V0.2.1/acceptance/evidence/final.

.PARAMETER SummaryPath
    Machine-readable summary path. Defaults to <EvidenceRoot>/summary.json.

.PARAMETER ReportPath
    Human-readable report path. Defaults to
    docs/version-V0.2.1/acceptance/final-report.md.

.NOTES
    Credentials supplied here (-AdminPassword / -SutPassword) are handed to the
    child scenarios through process environment variables, not command-line
    arguments, and are never written to the summary or the report.
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
    [string] $SummaryPath = "",
    [string] $ReportPath = "",
    [string] $ScenarioDirectory = "",
    # Task 88 (P10-T13): the final E2E must import one explicit small requirement
    # input, not the full 67-requirement SUT document. Supplying neither the
    # small input nor -FullSutDoc is a preflight error (no silent 67 fallback).
    [string] $RequirementInput = "",
    [int] $ExpectedRequirements = 0,
    [string] $ExpectedFrIds = "",
    [switch] $FullSutDoc,
    [string] $FullSutDocPath = "",
    [switch] $KeepGoing
)

Set-StrictMode -Version 3.0
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "lib.ps1")

$script:RunAllSchemaVersion = "1"

$script:ScenarioCatalog = @(
    [pscustomobject]@{
        Id        = "s0"
        Title     = "S0 Environment self-check"
        Criterion = "SUT / login / LLM / executor reachable; failures visible, retryable, never silently pending"
        Pattern   = "s0-*.ps1"
    },
    [pscustomobject]@{
        Id        = "s1"
        Title     = "S1 Create version and discovery sub-loop"
        Criterion = "Version + SUT binding; discovery produces login_session/page/API facts without manual setup"
        Pattern   = "s1-*.ps1"
    },
    [pscustomobject]@{
        Id        = "s2"
        Title     = "S2 Clarification budget and requirement gate"
        Criterion = "Multi-turn clarification; <=6 questions/requirement; no duplicates; stops at requirement_review"
        Pattern   = "s2-*.ps1"
    },
    [pscustomobject]@{
        Id        = "s3"
        Title     = "S3 Design generation"
        Criterion = "Confirmed requirements produce cases/jobs automatically; progress visible; cases in mindmap"
        Pattern   = "s3-*.ps1"
    },
    [pscustomobject]@{
        Id        = "s4"
        Title     = "S4 Modify and confirm"
        Criterion = "Conversation/selection edits are versioned; confirmation enqueues execution without manual prompting"
        Pattern   = "s4-*.ps1"
    },
    [pscustomobject]@{
        Id        = "s5"
        Title     = "S5 Execution"
        Criterion = "Real Chrome execution; executor_lost=0; degraded not dispatched; pause/resume works"
        Pattern   = "s5-*.ps1"
    },
    [pscustomobject]@{
        Id        = "s6"
        Title     = "S6 Report"
        Criterion = "Reports only from execution data; not empty; include cases/evidence/issues"
        Pattern   = "s6-*.ps1"
    },
    [pscustomobject]@{
        Id        = "s7"
        Title     = "S7 Fault injection"
        Criterion = "Kill executor/network/model 4xx/30+ gateways: accurate errors, retryable, not mislabelled"
        Pattern   = "s7-*.ps1"
    },
    [pscustomobject]@{
        Id        = "s8"
        Title     = "S8 UI"
        Criterion = "Thinking/tools/jobs visible; model switch shows actual; overlays preserve state; 3-layer; mindmap zoom"
        Pattern   = "s8-*.ps1"
    },
    [pscustomobject]@{
        Id        = "s9"
        Title     = "S9 Knowledge loop"
        Criterion = "Login success -> knowledge -> new-version injection; revocable; no write-without-effect"
        Pattern   = "s9-*.ps1"
    },
    [pscustomobject]@{
        Id        = "s10"
        Title     = "S10 Regression"
        Criterion = "fake-LLM E2E, contracts and static gates all green"
        Pattern   = "s10-*.ps1"
    }
)

# ---------------------------------------------------------------------------
# Small helpers
# ---------------------------------------------------------------------------

function Write-RunLog {
    param(
        [Parameter(Mandatory = $true)] [string] $Path,
        [Parameter(Mandatory = $true)] [AllowEmptyString()] [string] $Text
    )
    $directory = Split-Path -Parent $Path
    if (-not [string]::IsNullOrEmpty($directory)) {
        New-Item -ItemType Directory -Force -Path $directory | Out-Null
    }
    $line = "[{0}] {1}" -f (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ"), $Text
    [System.IO.File]::AppendAllText($Path, $line + [Environment]::NewLine, [System.Text.UTF8Encoding]::new($false))
}

function Format-Duration {
    param([Parameter(Mandatory = $false)] $Seconds)
    if ($null -eq $Seconds) { return "-" }
    $value = [double]$Seconds
    if ($value -lt 60) { return ("{0:N1}s" -f $value) }
    $minutes = [Math]::Floor($value / 60)
    $rest = $value - ($minutes * 60)
    return ("{0}m {1:N0}s" -f $minutes, $rest)
}

function Get-RelativePathFromDirectory {
    param(
        [Parameter(Mandatory = $true)] [string] $BaseDirectory,
        [Parameter(Mandatory = $true)] [string] $TargetPath
    )
    $base = [System.IO.Path]::GetFullPath($BaseDirectory).TrimEnd('\', '/')
    $target = [System.IO.Path]::GetFullPath($TargetPath)
    if ($target.StartsWith($base, [System.StringComparison]::OrdinalIgnoreCase)) {
        return $target.Substring($base.Length).TrimStart('\', '/')
    }
    return $target
}

function Format-MarkdownCell {
    param([Parameter(Mandatory = $false)] [AllowNull()] [string] $Text)
    if ([string]::IsNullOrEmpty($Text)) { return "" }
    $value = $Text -replace "`r?`n", " "
    return $value.Replace("|", "\|")
}

function Get-StatusLabel {
    param([Parameter(Mandatory = $false)] [string] $Status)
    switch ($Status) {
        "passed" { return "PASS" }
        "failed" { return "FAIL" }
        "blocked" { return "BLOCKED" }
        "missing" { return "MISSING" }
        "ambiguous" { return "AMBIGUOUS" }
        "not_run" { return "NOT RUN" }
        default { return ([string]$Status).ToUpperInvariant() }
    }
}

function Get-OutputTail {
    param(
        [Parameter(Mandatory = $false)] $Lines,
        [Parameter(Mandatory = $false)] [int] $Count = 3
    )
    $text = @()
    foreach ($line in @($Lines)) {
        $value = [string]$line
        if (-not [string]::IsNullOrWhiteSpace($value)) {
            $trimmed = $value.Trim()
            if ($trimmed.Length -gt 500) { $trimmed = $trimmed.Substring(0, 500) + "..." }
            $text += $trimmed
        }
    }
    if ($text.Count -eq 0) { return "" }
    $start = [Math]::Max(0, $text.Count - $Count)
    return (($text[$start..($text.Count - 1)]) -join " | ")
}

function Get-ScenarioInventory {
    param([Parameter(Mandatory = $true)] [string] $Directory)
    $items = [ordered]@{}
    foreach ($scenario in $script:ScenarioCatalog) {
        $files = @()
        if (Test-Path -LiteralPath $Directory) {
            $files = @(Get-ChildItem -LiteralPath $Directory -Filter $scenario.Pattern -File -ErrorAction SilentlyContinue | Sort-Object Name)
        }
        $status = "present"
        $scriptPath = ""
        $matches = @()
        if ($files.Count -eq 0) {
            $status = "missing"
        } elseif ($files.Count -gt 1) {
            $status = "ambiguous"
            $matches = @($files | ForEach-Object { $_.Name })
        } else {
            $scriptPath = $files[0].FullName
            $matches = @($files[0].Name)
        }
        $items[$scenario.Id] = [pscustomobject]@{
            Id         = $scenario.Id
            Pattern    = $scenario.Pattern
            Status     = $status
            ScriptPath = $scriptPath
            Matches    = $matches
        }
    }
    return $items
}

function Read-ScenarioResult {
    param([Parameter(Mandatory = $true)] [string] $Path)
    if (-not (Test-Path -LiteralPath $Path)) {
        return [pscustomobject]@{ Ok = $false; Reason = "scenario did not write result.json"; Data = $null }
    }
    try {
        $raw = Get-Content -LiteralPath $Path -Raw -Encoding UTF8
        if ([string]::IsNullOrWhiteSpace($raw)) {
            return [pscustomobject]@{ Ok = $false; Reason = "result.json is empty"; Data = $null }
        }
        $data = $raw | ConvertFrom-Json -ErrorAction Stop
        return [pscustomobject]@{ Ok = $true; Reason = ""; Data = $data }
    } catch {
        return [pscustomobject]@{ Ok = $false; Reason = "result.json is not valid JSON: $($_.Exception.Message)"; Data = $null }
    }
}

function Get-CheckList {
    param($Result)
    $checks = @(Get-JsonProp -Object $Result -Name "checks" -Default @())
    $list = @()
    foreach ($check in $checks) {
        if ($null -eq $check) { continue }
        $list += [pscustomobject]@{
            name     = [string](Get-JsonProp -Object $check -Name "name" -Default "")
            status   = [string](Get-JsonProp -Object $check -Name "status" -Default "")
            severity = [string](Get-JsonProp -Object $check -Name "severity" -Default "")
            detail   = [string](Get-JsonProp -Object $check -Name "detail" -Default "")
        }
    }
    return $list
}

function Get-ScenarioBlocker {
    param(
        [Parameter(Mandatory = $false)] $Result,
        [Parameter(Mandatory = $false)] [int] $ExitCode = 0,
        [Parameter(Mandatory = $false)] [string] $OutputTail = "",
        [Parameter(Mandatory = $false)] [string] $Fallback = ""
    )
    if ($null -ne $Result) {
        $errorText = [string](Get-JsonProp -Object $Result -Name "error" -Default "")
        if (-not [string]::IsNullOrWhiteSpace($errorText)) { return $errorText.Trim() }
        $failed = @(Get-CheckList -Result $Result | Where-Object { $_.status -eq "fail" })
        if ($failed.Count -gt 0) {
            $parts = @()
            foreach ($check in ($failed | Select-Object -First 3)) {
                $part = $check.name
                if (-not [string]::IsNullOrWhiteSpace($check.detail)) { $part += ": " + $check.detail }
                $parts += $part.Trim()
            }
            return (($parts -join " | ").Trim())
        }
    }
    if (-not [string]::IsNullOrWhiteSpace($OutputTail)) {
        return ("exit $ExitCode without a failed check; output tail: $OutputTail")
    }
    if (-not [string]::IsNullOrWhiteSpace($Fallback)) { return $Fallback }
    return "scenario exited $ExitCode without a specific failure message"
}

# ---------------------------------------------------------------------------
# Paths / context
# ---------------------------------------------------------------------------

$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$repoRoot = [System.IO.Path]::GetFullPath($repoRoot)

if ([string]::IsNullOrWhiteSpace($ScenarioDirectory)) {
    $ScenarioDirectory = $PSScriptRoot
} elseif (-not [System.IO.Path]::IsPathRooted($ScenarioDirectory)) {
    $ScenarioDirectory = Join-Path $repoRoot $ScenarioDirectory
}
$ScenarioDirectory = [System.IO.Path]::GetFullPath($ScenarioDirectory)

# Credentials are handed to child scenarios through the process environment,
# never through command-line arguments (which would end up in process lists and
# in the recorded command string).
if (-not [string]::IsNullOrWhiteSpace($AdminUsername)) { $env:EZTEST_ADMIN_USERNAME = $AdminUsername }
if (-not [string]::IsNullOrWhiteSpace($AdminPassword)) { $env:EZTEST_ADMIN_PASSWORD = $AdminPassword }
if (-not [string]::IsNullOrWhiteSpace($SutPassword)) { $env:SUT_PASSWORD = $SutPassword }

if ([string]::IsNullOrWhiteSpace($EvidenceRoot)) {
    $EvidenceRoot = "docs/version-V0.2.1/acceptance/evidence/final"
}

$context = Initialize-AcceptanceContext -ApiBaseUrl $ApiBaseUrl -WebBaseUrl $WebBaseUrl `
    -ExecutorUrl $ExecutorUrl -SutWebBaseUrl $SutWebBaseUrl -SutApiBaseUrl $SutApiBaseUrl `
    -AdminUsername $AdminUsername -AdminPassword $AdminPassword -SutPassword $SutPassword `
    -EvidenceRoot $EvidenceRoot

if ([string]::IsNullOrWhiteSpace($SummaryPath)) {
    $SummaryPath = Join-Path $context.EvidenceRoot "summary.json"
} elseif (-not [System.IO.Path]::IsPathRooted($SummaryPath)) {
    $SummaryPath = Join-Path $context.RepoRoot $SummaryPath
}
$SummaryPath = [System.IO.Path]::GetFullPath($SummaryPath)

if ([string]::IsNullOrWhiteSpace($ReportPath)) {
    $ReportPath = Join-Path $context.RepoRoot "docs/version-V0.2.1/acceptance/final-report.md"
} elseif (-not [System.IO.Path]::IsPathRooted($ReportPath)) {
    $ReportPath = Join-Path $context.RepoRoot $ReportPath
}
$ReportPath = [System.IO.Path]::GetFullPath($ReportPath)

$logPath = Join-Path $context.EvidenceRoot "run-all.log"
$runId = (Get-Date).ToUniversalTime().ToString("yyyyMMdd'T'HHmmss'Z'")
$branch = "unknown"
try {
    $branchValue = & git -C $context.RepoRoot rev-parse --abbrev-ref HEAD 2>$null
    if ($LASTEXITCODE -eq 0 -and $branchValue) { $branch = ([string]$branchValue).Trim() }
} catch { }

# Start a fresh orchestration log for this run. Scenario evidence files are
# overwritten by the scenarios themselves; the summary is authoritative.
New-Item -ItemType Directory -Force -Path $context.EvidenceRoot | Out-Null
[System.IO.File]::WriteAllText($logPath, "", [System.Text.UTF8Encoding]::new($false))

$commandParts = @("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File")
$runAllDisplay = Resolve-RepoRelativePath -Context $context -Path $PSCommandPath
$commandParts += $runAllDisplay
foreach ($pair in @(
        @{ Name = "-ApiBaseUrl"; Value = $context.ApiBaseUrl },
        @{ Name = "-WebBaseUrl"; Value = $context.WebBaseUrl },
        @{ Name = "-ExecutorUrl"; Value = $context.ExecutorUrl },
        @{ Name = "-SutWebBaseUrl"; Value = $context.SutWebBaseUrl },
        @{ Name = "-SutApiBaseUrl"; Value = $context.SutApiBaseUrl },
        @{ Name = "-EvidenceRoot"; Value = $context.EvidenceRoot },
        @{ Name = "-ScenarioDirectory"; Value = $ScenarioDirectory },
        @{ Name = "-SummaryPath"; Value = $SummaryPath },
        @{ Name = "-ReportPath"; Value = $ReportPath },
        @{ Name = "-RequirementInput"; Value = $RequirementInput },
        @{ Name = "-ExpectedRequirements"; Value = $(if ($ExpectedRequirements -gt 0) { [string]$ExpectedRequirements } else { "" }) },
        @{ Name = "-ExpectedFrIds"; Value = $ExpectedFrIds },
        @{ Name = "-FullSutDocPath"; Value = $FullSutDocPath }
    )) {
    if (-not [string]::IsNullOrWhiteSpace([string]$pair.Value)) {
        $commandParts += $pair.Name
        $commandParts += ([string]$pair.Value)
    }
}
if ($FullSutDoc) { $commandParts += "-FullSutDoc" }
if ($KeepGoing) { $commandParts += "-KeepGoing" }
$commandText = $commandParts -join " "
if (-not [string]::IsNullOrWhiteSpace($AdminPassword) -or -not [string]::IsNullOrWhiteSpace($SutPassword)) {
    $commandText += "  # credentials provided via process environment (not recorded)"
}

Write-RunLog -Path $logPath -Text "run-all started: run_id=$runId"
Write-RunLog -Path $logPath -Text "command: $commandText"
Write-RunLog -Path $logPath -Text "repo=$($context.RepoRoot) branch=$branch head=$($context.RepoHead) evidence=$($context.EvidenceRoot)"
Write-Host "=== EzTest 0.2.1 acceptance run $runId ===" -ForegroundColor Cyan
Write-Host "repo=$($context.RepoRoot) branch=$branch head=$($context.RepoHead)"
Write-Host "api=$($context.ApiBaseUrl) web=$($context.WebBaseUrl) executor=$($context.ExecutorUrl)"
Write-Host "sut=$($context.SutWebBaseUrl) / $($context.SutApiBaseUrl) database=$($context.DatabaseName)"
Write-Host "mode=$(if ($KeepGoing) { 'keep-going' } else { 'stop-at-first-failure' })"

# ---------------------------------------------------------------------------
# Preflight inventory + result table
# ---------------------------------------------------------------------------

$inventory = Get-ScenarioInventory -Directory $ScenarioDirectory
$results = [System.Collections.Generic.List[object]]::new()
foreach ($scenario in $script:ScenarioCatalog) {
    $inv = $inventory[$scenario.Id]
    $scriptDisplay = ""
    if (-not [string]::IsNullOrWhiteSpace([string]$inv.ScriptPath)) {
        $scriptDisplay = Resolve-RepoRelativePath -Context $context -Path ([string]$inv.ScriptPath)
    }
    $evidenceDir = Join-Path $context.EvidenceRoot $scenario.Id
    $evidenceDisplay = Resolve-RepoRelativePath -Context $context -Path $evidenceDir
    $resultPath = Join-Path $evidenceDir "result.json"
    [void]$results.Add([pscustomobject]@{
        Id              = $scenario.Id
        Title           = $scenario.Title
        Criterion       = $scenario.Criterion
        Pattern         = $scenario.Pattern
        InventoryStatus = [string]$inv.Status
        InventoryMatches = @($inv.Matches)
        ScriptPath      = [string]$inv.ScriptPath
        Script          = $scriptDisplay
        Status          = "not_run"
        ExitCode        = $null
        StartedAt       = ""
        EndedAt         = ""
        DurationSeconds = $null
        Message         = ""
        Error           = ""
        Blocker         = ""
        Inconsistent    = $false
        ResultFound     = $false
        ResultStatus    = ""
        ResultExitCode  = $null
        ResultPath      = $resultPath
        ResultDisplay   = (Resolve-RepoRelativePath -Context $context -Path $resultPath)
        ResultExists    = (Test-Path -LiteralPath $resultPath)
        LogPath         = (Join-Path $evidenceDir "run.log")
        LogDisplay      = (Resolve-RepoRelativePath -Context $context -Path (Join-Path $evidenceDir "run.log"))
        EvidenceDir     = $evidenceDir
        EvidenceDisplay = $evidenceDisplay
        Checks          = [pscustomobject]@{ total = 0; passed = 0; failed = 0; warnings = 0 }
        FailedChecks    = @()
        Warnings        = @()
        Artifacts       = @()
        OutputTail      = ""
    })
}
$resultById = @{}
foreach ($item in $results) { $resultById[$item.Id] = $item }

# ---------------------------------------------------------------------------
# Run scenarios
# ---------------------------------------------------------------------------

$stopReason = "completed"
$stopScenario = ""
$stopMessage = ""
$runError = ""
$exitCode = 0

# ---------------------------------------------------------------------------
# Requirement-source preflight (Task 88)
#
# The final E2E imports one explicit small requirement input. Supplying neither
# -RequirementInput nor -FullSutDoc is an explicit preflight failure; it must
# never fall back to the full 67-requirement SUT document. The resolved profile
# is handed to S1/S2/S3 and recorded in the summary/report.
# ---------------------------------------------------------------------------

$requirementProfile = $null
$preflightError = ""
try {
    $requirementProfile = Resolve-AcceptanceRequirementProfile -RequirementInput $RequirementInput `
        -ExpectedRequirements $ExpectedRequirements -ExpectedFrIds $ExpectedFrIds `
        -FullSutDoc:$FullSutDoc -FullSutDocPath $FullSutDocPath -RepoRoot $context.RepoRoot
} catch {
    $preflightError = $_.Exception.Message
}
$requirementProfileSummary = $null
if ($null -ne $requirementProfile) {
    $requirementProfileSummary = [pscustomobject]@{
        mode                  = $requirementProfile.Mode
        input                 = (Resolve-RepoRelativePath -Context $context -Path $requirementProfile.Path)
        input_name            = $requirementProfile.Name
        format                = $requirementProfile.Format
        sha256                = $requirementProfile.Sha256
        expected_requirements = $requirementProfile.ExpectedRequirements
        expected_fr_ids       = @($requirementProfile.ExpectedFrIds)
        full_sut_doc_opt_in   = [bool]$FullSutDoc
    }
}
if ($preflightError -ne "") {
    $runError = $preflightError
    $stopReason = "preflight"
    $stopScenario = "preflight"
    $stopMessage = $preflightError
    Write-RunLog -Path $logPath -Text "preflight failed: $preflightError"
    Write-Host ("[BLOCKED] preflight: {0}" -f $preflightError) -ForegroundColor Yellow
}

if ($preflightError -eq "") {
# A new run must not inherit an S2/S3 version hand-off from an earlier run.
# Clearing state.json keeps the small-input S2 -> S3 chain honest (S3 refuses to
# design a version that this run's S2 did not import).
$runStatePath = Join-Path $context.EvidenceRoot "state.json"
if (Test-Path -LiteralPath $runStatePath) {
    Remove-Item -LiteralPath $runStatePath -Force
    Write-RunLog -Path $logPath -Text "cleared stale scenario state: $runStatePath"
}
foreach ($scenario in $script:ScenarioCatalog) {
    $r = $resultById[$scenario.Id]
    $inv = $inventory[$scenario.Id]

    if ([string]$inv.Status -ne "present") {
        $r.StartedAt = (Get-Date).ToUniversalTime().ToString("o")
        $r.EndedAt = $r.StartedAt
        $r.Status = if ([string]$inv.Status -eq "missing") { "missing" } else { "ambiguous" }
        $r.ExitCode = 2
        if ($r.Status -eq "missing") {
            $r.Blocker = "scenario script not found: expected $($scenario.Pattern) in $ScenarioDirectory"
        } else {
            $r.Blocker = "multiple scenario scripts matched $($scenario.Pattern): $((@($inv.Matches)) -join ', ')"
        }
        Write-RunLog -Path $logPath -Text "$($scenario.Id) $($r.Status): $($r.Blocker)"
        Write-Host ("[{0}] {1}: {2}" -f (Get-StatusLabel $r.Status), $scenario.Id, $r.Blocker) -ForegroundColor Yellow
        if (-not $KeepGoing) {
            $stopReason = if ($r.Status -eq "missing") { "missing_script" } else { "ambiguous_script" }
            $stopScenario = $scenario.Id
            $stopMessage = $r.Blocker
            break
        }
        continue
    }

    $r.StartedAt = (Get-Date).ToUniversalTime().ToString("o")
    $startWatch = Get-Date
    $childArgs = @("-NoProfile", "-ExecutionPolicy", "Bypass", "-File", $r.ScriptPath)
    foreach ($pair in @(
            @{ Name = "-ApiBaseUrl"; Value = $ApiBaseUrl },
            @{ Name = "-WebBaseUrl"; Value = $WebBaseUrl },
            @{ Name = "-ExecutorUrl"; Value = $ExecutorUrl },
            @{ Name = "-SutWebBaseUrl"; Value = $SutWebBaseUrl },
            @{ Name = "-SutApiBaseUrl"; Value = $SutApiBaseUrl }
        )) {
        if (-not [string]::IsNullOrWhiteSpace([string]$pair.Value)) {
            $childArgs += $pair.Name
            $childArgs += [string]$pair.Value
        }
    }
    $childArgs += "-EvidenceRoot"
    $childArgs += $context.EvidenceRoot
    if (@("s1", "s2", "s3") -contains $scenario.Id -and $null -ne $requirementProfile) {
        if ($requirementProfile.Mode -eq "small-input") {
            $childArgs += @("-RequirementInput", $requirementProfile.Path, "-ExpectedRequirements", [string]$requirementProfile.ExpectedRequirements)
            if (@($requirementProfile.ExpectedFrIds).Count -gt 0) {
                $childArgs += @("-ExpectedFrIds", (@($requirementProfile.ExpectedFrIds) -join ","))
            }
        } else {
            $childArgs += @("-FullSutDoc", "-FullSutDocPath", $requirementProfile.Path)
        }
    }

    Write-Host ""
    Write-Host ("=== [{0}] {1} ({2}) ===" -f $scenario.Id, $scenario.Title, $r.Script) -ForegroundColor Cyan
    Write-RunLog -Path $logPath -Text "$($scenario.Id) start: $($scenario.Title) script=$($r.Script)"
    Write-RunLog -Path $logPath -Text "child command: powershell.exe $($childArgs -join ' ')"

    $childOutput = @()
    $childExitCode = 2
    try {
        $childOutput = @(& powershell.exe @childArgs 2>&1)
        $childExitCode = [int]$LASTEXITCODE
    } catch {
        $runError = "failed to launch scenario $($scenario.Id): $($_.Exception.Message)"
        $childOutput += $runError
        $childExitCode = 2
    }

    $outputText = (@($childOutput) | ForEach-Object { [string]$_ }) -join [Environment]::NewLine
    if (-not [string]::IsNullOrWhiteSpace($outputText)) {
        Write-Host $outputText
        [System.IO.File]::AppendAllText($logPath, $outputText + [Environment]::NewLine, [System.Text.UTF8Encoding]::new($false))
    }

    $r.ExitCode = $childExitCode
    $r.EndedAt = (Get-Date).ToUniversalTime().ToString("o")
    $r.DurationSeconds = [Math]::Round(((Get-Date) - $startWatch).TotalSeconds, 1)
    $r.OutputTail = Get-OutputTail -Lines $childOutput -Count 3
    $r.ResultPath = Join-Path (Join-Path $context.EvidenceRoot $scenario.Id) "result.json"
    $r.ResultDisplay = Resolve-RepoRelativePath -Context $context -Path $r.ResultPath
    $r.ResultExists = Test-Path -LiteralPath $r.ResultPath
    $read = Read-ScenarioResult -Path $r.ResultPath
    $childData = $read.Data
    $declaredStatus = ""
    if ($read.Ok) {
        $declaredStatus = [string](Get-JsonProp -Object $childData -Name "status" -Default "")
        $r.ResultFound = $true
        $r.ResultStatus = $declaredStatus
        $declaredExit = Get-JsonProp -Object $childData -Name "exit_code" -Default $null
        if ($null -ne $declaredExit) {
            try { $r.ResultExitCode = [int]$declaredExit } catch { $r.ResultExitCode = $null }
        }
        $r.Message = [string](Get-JsonProp -Object $childData -Name "message" -Default "")
        $r.Error = [string](Get-JsonProp -Object $childData -Name "error" -Default "")
        $checks = @(Get-CheckList -Result $childData)
        $r.Checks = [pscustomobject]@{
            total    = $checks.Count
            passed   = @($checks | Where-Object { $_.status -eq "pass" }).Count
            failed   = @($checks | Where-Object { $_.status -eq "fail" }).Count
            warnings = @($checks | Where-Object { $_.status -eq "warn" }).Count
        }
        $r.FailedChecks = @($checks | Where-Object { $_.status -eq "fail" })
        $r.Warnings = @($checks | Where-Object { $_.status -eq "warn" })
        $r.Artifacts = @(Get-JsonProp -Object $childData -Name "artifacts" -Default @())
    }

    # Status is never inferred from the result file alone: the process exit
    # code must agree. An exit 0 without a readable result is NOT a pass.
    $mapped = switch ($childExitCode) {
        0 { "passed" }
        1 { "failed" }
        2 { "blocked" }
        default { "failed" }
    }
    $effectiveStatus = $mapped
    if ($childExitCode -eq 0) {
        if ($read.Ok -and @("passed", "failed", "blocked") -contains $declaredStatus) {
            $effectiveStatus = $declaredStatus
            if ($declaredStatus -ne "passed") { $r.Inconsistent = $true }
        } else {
            $effectiveStatus = "failed"
            $r.Inconsistent = $true
        }
        if ($null -ne $r.ResultExitCode -and $r.ResultExitCode -ne 0) {
            $effectiveStatus = "failed"
            $r.Inconsistent = $true
        }
    } elseif ($read.Ok -and -not [string]::IsNullOrWhiteSpace($declaredStatus) -and $declaredStatus -ne $mapped) {
        $r.Inconsistent = $true
    }

    $r.Status = $effectiveStatus
    if ($r.Status -ne "passed") {
        $fallback = if (-not $read.Ok) { $read.Reason } else { "" }
        $r.Blocker = Get-ScenarioBlocker -Result $childData -ExitCode $childExitCode -OutputTail $r.OutputTail -Fallback $fallback
        if ($r.Inconsistent) {
            $r.Blocker = "INCONSISTENT RESULT (exit $childExitCode, result.json status '$declaredStatus'): $($r.Blocker)"
        }
    }

    $statusText = "[{0}] {1}: {2} (exit {3}, {4})" -f (Get-StatusLabel $r.Status), $scenario.Id, $r.Blocker, $childExitCode, (Format-Duration $r.DurationSeconds)
    if ($null -eq $r.Blocker -or $r.Blocker -eq "") {
        $statusText = "[{0}] {1} (exit {2}, {3})" -f (Get-StatusLabel $r.Status), $scenario.Id, $childExitCode, (Format-Duration $r.DurationSeconds)
    }
    Write-RunLog -Path $logPath -Text "$($scenario.Id) result: $($r.Status) exit=$childExitCode duration=$($r.DurationSeconds)s checks=$($r.Checks.passed)/$($r.Checks.total) warnings=$($r.Checks.warnings) blocker=$($r.Blocker)"
    switch ($r.Status) {
        "passed" { Write-Host $statusText -ForegroundColor Green }
        "failed" { Write-Host $statusText -ForegroundColor Red }
        "blocked" { Write-Host $statusText -ForegroundColor Yellow }
        default { Write-Host $statusText -ForegroundColor Red }
    }

    if ($r.Status -ne "passed" -and -not $KeepGoing) {
        $stopReason = "scenario_$($r.Status)"
        $stopScenario = $scenario.Id
        $stopMessage = $r.Blocker
        break
    }
}
}

# Mark everything that was not reached. It is never counted as a pass.
foreach ($r in $results) {
    if ($r.Status -eq "not_run") {
        if ($stopReason -eq "preflight") {
            $r.Blocker = "not run: requirement-source preflight failed: $preflightError"
        } elseif (-not [string]::IsNullOrWhiteSpace($stopScenario)) {
            $r.Blocker = "not run: acceptance run stopped at $stopScenario ($stopReason)"
        } else {
            $r.Blocker = "not run"
        }
    }
}

$statuses = @($results | ForEach-Object { $_.Status })
$totals = [pscustomobject]@{
    scenarios = @($results).Count
    passed    = @($statuses | Where-Object { $_ -eq "passed" }).Count
    failed    = @($statuses | Where-Object { $_ -eq "failed" }).Count
    blocked   = @($statuses | Where-Object { $_ -eq "blocked" }).Count
    missing   = @($statuses | Where-Object { $_ -eq "missing" }).Count
    ambiguous = @($statuses | Where-Object { $_ -eq "ambiguous" }).Count
    not_run   = @($statuses | Where-Object { $_ -eq "not_run" }).Count
}

$overallStatus = "passed"
$overallExit = 0
if ($totals.failed -gt 0) {
    $overallStatus = "failed"
    $overallExit = 1
} elseif (-not [string]::IsNullOrWhiteSpace($runError)) {
    $overallStatus = "error"
    $overallExit = 2
} elseif ($totals.blocked -gt 0) {
    $overallStatus = "blocked"
    $overallExit = 2
} elseif (($totals.missing + $totals.ambiguous) -gt 0) {
    $overallStatus = "incomplete"
    $overallExit = 2
} elseif ($totals.not_run -gt 0) {
    $overallStatus = "incomplete"
    $overallExit = 2
}

if ($stopScenario -eq "" -and $KeepGoing -and ($totals.failed + $totals.blocked + $totals.missing + $totals.ambiguous) -gt 0) {
    $stopReason = "aggregated"
    $stopMessage = "KeepGoing aggregated every available scenario; not all passed."
}

$allPassed = ($overallStatus -eq "passed" -and $totals.passed -eq @($script:ScenarioCatalog).Count -and $totals.scenarios -eq @($script:ScenarioCatalog).Count)
$assertionReason = if ($allPassed) {
    "All $($totals.scenarios) scenarios exited 0 with result.json status=passed."
} else {
    "Not claimed: run status is $overallStatus ($($totals.failed) failed, $($totals.blocked) blocked, $($totals.missing) missing, $($totals.ambiguous) ambiguous, $($totals.not_run) not run)."
}

$blockers = @($results | Where-Object { @("failed", "blocked", "missing", "ambiguous") -contains $_.Status } | ForEach-Object {
    [pscustomobject]@{
        scenario    = $_.Id
        title       = $_.Title
        status      = $_.Status
        exit_code   = $_.ExitCode
        blocker     = $_.Blocker
        result_path = $_.ResultDisplay
        log_path    = $_.LogDisplay
    }
})

$warnings = @()
foreach ($r in $results) {
    foreach ($warning in @($r.Warnings)) {
        if ($null -eq $warning) { continue }
        $warnings += [pscustomobject]@{
            scenario = $r.Id
            name     = [string](Get-JsonProp -Object $warning -Name "name" -Default "")
            detail   = [string](Get-JsonProp -Object $warning -Name "detail" -Default "")
        }
    }
}

$inventoryList = @()
foreach ($scenario in $script:ScenarioCatalog) {
    $inv = $inventory[$scenario.Id]
    $scriptDisplay = ""
    if (-not [string]::IsNullOrWhiteSpace([string]$inv.ScriptPath)) {
        $scriptDisplay = Resolve-RepoRelativePath -Context $context -Path ([string]$inv.ScriptPath)
    }
    $inventoryList += [pscustomobject]@{
        id      = $scenario.Id
        pattern = $scenario.Pattern
        status  = [string]$inv.Status
        matches = @($inv.Matches)
        script  = $scriptDisplay
    }
}
$missingPatterns = @($inventoryList | Where-Object { $_.status -eq "missing" } | ForEach-Object { $_.pattern })

$notes = @(
    "A scenario is PASS only when its exit code is 0 AND result.json status is 'passed'; NOT RUN/MISSING are never passes.",
    "Scenario evidence directories are overwritten by each run; summary.json is authoritative for this run.",
    "Credentials are passed to children through the process environment and are not recorded.",
    "Requirement source is explicit: -RequirementInput (small input) or -FullSutDoc (legacy); no silent full-document fallback."
)
if ($null -ne $requirementProfile) {
    $notes += "Requirement profile: mode=$($requirementProfile.Mode) input=$($requirementProfile.Name) sha256=$($requirementProfile.Sha256) expected=$($requirementProfile.ExpectedRequirements) ids=$(@($requirementProfile.ExpectedFrIds) -join ',')."
}
if ($missingPatterns.Count -gt 0) {
    $notes += "Scenario scripts missing at run time: $($missingPatterns -join ', ')."
}
if (-not [string]::IsNullOrWhiteSpace($runError)) {
    $notes += "Runner error: $runError"
}

$summary = [pscustomobject][ordered]@{
    schema_version = $script:RunAllSchemaVersion
    kind           = "eztest-021-final-acceptance-summary"
    run_id         = $runId
    generated_at   = (Get-Date).ToUniversalTime().ToString("o")
    status         = $overallStatus
    exit_code      = $overallExit
    mode           = [pscustomobject]@{
        keep_going            = [bool]$KeepGoing
        stop_at_first_failure = (-not $KeepGoing)
    }
    stop           = [pscustomobject]@{
        reason   = $stopReason
        scenario = $stopScenario
        message  = $stopMessage
    }
    preflight      = [pscustomobject]@{
        ok                    = [string]::IsNullOrWhiteSpace($preflightError)
        error                 = $preflightError
        requirement_input     = $RequirementInput
        expected_requirements = $ExpectedRequirements
        full_sut_doc          = [bool]$FullSutDoc
    }
    requirement_profile = $requirementProfileSummary
    command        = $commandText
    repo           = [pscustomobject]@{
        root   = $context.RepoRoot
        branch = $branch
        head   = $context.RepoHead
        dirty  = $null
    }
    environment    = [pscustomobject]@{
        api_base_url     = $context.ApiBaseUrl
        web_base_url     = $context.WebBaseUrl
        executor_url     = $context.ExecutorUrl
        sut_web_base_url = $context.SutWebBaseUrl
        sut_api_base_url = $context.SutApiBaseUrl
        database_name    = $context.DatabaseName
        evidence_root    = (Resolve-RepoRelativePath -Context $context -Path $context.EvidenceRoot)
    }
    paths          = [pscustomobject]@{
        summary = (Resolve-RepoRelativePath -Context $context -Path $SummaryPath)
        report  = (Resolve-RepoRelativePath -Context $context -Path $ReportPath)
        log     = (Resolve-RepoRelativePath -Context $context -Path $logPath)
    }
    options        = [pscustomobject]@{
        scenario_directory = $ScenarioDirectory
        keep_going         = [bool]$KeepGoing
        endpoint_overrides = [pscustomobject]@{
            api_base_url     = $ApiBaseUrl
            web_base_url     = $WebBaseUrl
            executor_url     = $ExecutorUrl
            sut_web_base_url = $SutWebBaseUrl
            sut_api_base_url = $SutApiBaseUrl
        }
    }
    assertions     = [pscustomobject]@{
        all_scenarios_passed         = $allPassed
        zero_visible_blockers_claimed = $allPassed
        zero_manual_guidance_claimed  = $allPassed
        reason                       = $assertionReason
    }
    inventory      = $inventoryList
    scenario_order = @($script:ScenarioCatalog | ForEach-Object { $_.Id })
    scenarios      = @($results | ForEach-Object {
        [pscustomobject]@{
            id                = $_.Id
            title             = $_.Title
            criterion         = $_.Criterion
            script            = $_.Script
            status            = $_.Status
            exit_code         = $_.ExitCode
            inventory_status  = $_.InventoryStatus
            inconsistent      = $_.Inconsistent
            started_at        = $_.StartedAt
            ended_at          = $_.EndedAt
            duration_seconds  = $_.DurationSeconds
            message           = $_.Message
            error             = $_.Error
            blocker           = $_.Blocker
            result_path       = $_.ResultDisplay
            result_exists     = $_.ResultExists
            result_status     = $_.ResultStatus
            log_path          = $_.LogDisplay
            evidence_dir      = $_.EvidenceDisplay
            checks            = $_.Checks
            failed_checks     = @($_.FailedChecks)
            warnings          = @($_.Warnings)
            artifacts         = @($_.Artifacts)
        }
    })
    totals         = $totals
    blockers       = $blockers
    warnings       = $warnings
    notes          = $notes
}
$summary.repo.dirty = $null

# ---------------------------------------------------------------------------
# Human-readable report
# ---------------------------------------------------------------------------

function Write-AcceptanceReport {
    param(
        [Parameter(Mandatory = $true)] $Summary,
        [Parameter(Mandatory = $true)] [string] $Path,
        [Parameter(Mandatory = $true)] [string] $RepoRoot
    )

    $reportDir = Split-Path -Parent $Path
    $lines = [System.Collections.Generic.List[string]]::new()

    $lines.Add("# EzTest 0.2.1 Final Acceptance Report")
    $lines.Add("")
    $lines.Add(("**Status: {0}** (exit {1}) - generated {2}" -f (Get-StatusLabel $Summary.status).ToUpperInvariant(), $Summary.exit_code, $Summary.generated_at))
    $lines.Add("")
    $lines.Add("This report is generated by ``scripts/acceptance-021/run-all.ps1`` from actual scenario exit codes and ``result.json`` files.")
    $lines.Add("A scenario is marked PASS only when the process exit code is 0 **and** ``result.json`` status is ``passed``.")
    $lines.Add("NOT RUN / MISSING scenarios are never counted as passes. No criterion is inferred from logs or prior runs.")
    $lines.Add("")
    $lines.Add("## Run")
    $lines.Add("")
    $lines.Add("| Field | Value |")
    $lines.Add("|---|---|")
    $lines.Add(("| Run ID | {0} |" -f (Format-MarkdownCell $Summary.run_id)))
    $lines.Add(("| Started | {0} |" -f (Format-MarkdownCell $Summary.scenarios[0].started_at)))
    $lines.Add(("| Finished | {0} |" -f (Format-MarkdownCell $Summary.generated_at)))
    $lines.Add(("| Mode | {0} |" -f $(if ($Summary.mode.keep_going) { "KeepGoing (aggregate)" } else { "stop at first non-passing scenario" })))
    $stopDetail = ("{0} {1}" -f $Summary.stop.reason, $(if ($Summary.stop.scenario) { "at " + $Summary.stop.scenario } else { "" })).Trim()
    $lines.Add(("| Stop | {0} |" -f (Format-MarkdownCell $stopDetail)))
    $lines.Add(("| Repo | {0} @ {1} ({2}) |" -f (Format-MarkdownCell $Summary.repo.root), (Format-MarkdownCell $Summary.repo.branch), (Format-MarkdownCell $Summary.repo.head)))
    $lines.Add(("| API / Web | {0} / {1} |" -f (Format-MarkdownCell $Summary.environment.api_base_url), (Format-MarkdownCell $Summary.environment.web_base_url)))
    $lines.Add(("| Executor | {0} |" -f (Format-MarkdownCell $Summary.environment.executor_url)))
    $lines.Add(("| SUT | {0} (API {1}) |" -f (Format-MarkdownCell $Summary.environment.sut_web_base_url), (Format-MarkdownCell $Summary.environment.sut_api_base_url)))
    $lines.Add(("| Database | {0} |" -f (Format-MarkdownCell $Summary.environment.database_name)))
    $lines.Add(("| Evidence root | ``{0}`` |" -f (Format-MarkdownCell $Summary.environment.evidence_root)))
    $lines.Add(("| Summary JSON | ``{0}`` |" -f (Format-MarkdownCell $Summary.paths.summary)))
    $lines.Add("")
    $lines.Add("Command (credentials, when supplied, are passed through the process environment and are not shown):")
    $lines.Add("")
    $lines.Add('```powershell')
    $lines.Add($Summary.command)
    $lines.Add('```')
    $lines.Add("")
    $lines.Add("## Requirement input (Task 88)")
    $lines.Add("")
    if ($null -eq $Summary.requirement_profile) {
        $lines.Add(("**Preflight failed:** {0}" -f (Format-MarkdownCell $Summary.preflight.error)))
        $lines.Add("")
        $lines.Add("The run stopped before any scenario because no explicit requirement input was resolved. Pass ``-RequirementInput <small-a.md|small-b.feature|small-b.txt|small-c.docx> -ExpectedRequirements 3`` or the legacy ``-FullSutDoc -FullSutDocPath <doc>``.")
    } else {
        $profile = $Summary.requirement_profile
        $lines.Add("| Field | Value |")
        $lines.Add("|---|---|")
        $lines.Add(("| Mode | {0} |" -f (Format-MarkdownCell $profile.mode)))
        $lines.Add(("| Input | ``{0}`` |" -f (Format-MarkdownCell $profile.input)))
        $lines.Add(("| Format | {0} |" -f (Format-MarkdownCell $profile.format)))
        $lines.Add(("| SHA-256 | ``{0}`` |" -f (Format-MarkdownCell $profile.sha256)))
        $lines.Add(("| Expected requirements | {0} |" -f $profile.expected_requirements))
        $lines.Add(("| Expected FR/NFR IDs | {0} |" -f (Format-MarkdownCell (@($profile.expected_fr_ids) -join ", "))))
        $lines.Add(("| Full SUT doc opt-in | {0} |" -f $(if ($profile.full_sut_doc_opt_in) { "yes" } else { "no" })))
        $lines.Add(("| Preflight | {0} |" -f $(if ($Summary.preflight.ok) { "PASS" } else { "FAIL: " + (Format-MarkdownCell $Summary.preflight.error) })))
    }
    $lines.Add("")
    $lines.Add("## Scenario results")
    $lines.Add("")
    $lines.Add("| ID | Scenario | Spec criterion | Status | Exit | Duration | Evidence | Blocker |")
    $lines.Add("|---|---|---|---|---|---|---|---|")
    foreach ($scenario in @($Summary.scenarios)) {
        $evidenceLink = "-"
        if ($scenario.result_exists) {
            $resultAbsolute = $scenario.result_path
            if (-not [System.IO.Path]::IsPathRooted($resultAbsolute)) {
                $resultAbsolute = Join-Path $RepoRoot $resultAbsolute
            }
            $relative = Get-RelativePathFromDirectory -BaseDirectory $reportDir -TargetPath $resultAbsolute
            $evidenceLink = ("[result.json]({0})" -f ($relative -replace '\\', '/'))
        }
        $blockerCell = Format-MarkdownCell $scenario.blocker
        if ($blockerCell.Length -gt 220) { $blockerCell = $blockerCell.Substring(0, 217) + "..." }
        $lines.Add(("| {0} | {1} | {2} | {3} | {4} | {5} | {6} | {7} |" -f
                $scenario.id,
                (Format-MarkdownCell $scenario.title),
                (Format-MarkdownCell $scenario.criterion),
                (Get-StatusLabel $scenario.status),
                $(if ($null -eq $scenario.exit_code) { "-" } else { [string]$scenario.exit_code }),
                (Format-Duration $scenario.duration_seconds),
                $evidenceLink,
                $blockerCell))
    }
    $lines.Add("")
    $lines.Add(("Totals: {0} passed, {1} failed, {2} blocked, {3} missing, {4} ambiguous, {5} not run (of {6})." -f
            $Summary.totals.passed, $Summary.totals.failed, $Summary.totals.blocked, $Summary.totals.missing,
            $Summary.totals.ambiguous, $Summary.totals.not_run, $Summary.totals.scenarios))
    $lines.Add("")

    $lines.Add("## Blockers")
    $lines.Add("")
    if (@($Summary.blockers).Count -eq 0) {
        $lines.Add("None recorded in this run.")
    } else {
        foreach ($blocker in @($Summary.blockers)) {
            $lines.Add(("### {0} - {1} (exit {2})" -f $blocker.scenario, (Get-StatusLabel $blocker.status), $blocker.exit_code))
            $lines.Add("")
            $lines.Add(("**Blocker:** {0}" -f $blocker.blocker))
            $lines.Add("")
            $scenario = @($Summary.scenarios | Where-Object { $_.id -eq $blocker.scenario } | Select-Object -First 1)
            if (@($scenario).Count -gt 0 -and @($scenario[0].failed_checks).Count -gt 0) {
                $lines.Add("**Failed checks:**")
                $lines.Add("")
                foreach ($check in @($scenario[0].failed_checks)) {
                    $lines.Add(("- ``[{0}] {1}`` - {2}" -f $check.severity, $check.name, $check.detail))
                }
                $lines.Add("")
            }
            $linkParts = @()
            if ($scenario.Count -gt 0 -and $scenario[0].result_exists) {
                $resultAbsolute = $scenario[0].result_path
                if (-not [System.IO.Path]::IsPathRooted($resultAbsolute)) { $resultAbsolute = Join-Path $RepoRoot $resultAbsolute }
                $linkParts += ("[result.json]({0})" -f ((Get-RelativePathFromDirectory -BaseDirectory $reportDir -TargetPath $resultAbsolute) -replace '\\', '/'))
            }
            if ($scenario.Count -gt 0) {
                $logAbsolute = $scenario[0].log_path
                if (-not [string]::IsNullOrWhiteSpace($logAbsolute)) {
                    if (-not [System.IO.Path]::IsPathRooted($logAbsolute)) { $logAbsolute = Join-Path $RepoRoot $logAbsolute }
                    if (Test-Path -LiteralPath $logAbsolute) {
                        $linkParts += ("[run.log]({0})" -f ((Get-RelativePathFromDirectory -BaseDirectory $reportDir -TargetPath $logAbsolute) -replace '\\', '/'))
                    }
                }
            }
            if ($linkParts.Count -gt 0) {
                $lines.Add(("**Evidence:** {0}" -f ($linkParts -join ", ")))
            } else {
                $lines.Add("**Evidence:** no result.json was produced.")
            }
            $lines.Add("")
        }
    }

    $notRun = @($Summary.scenarios | Where-Object { $_.status -eq "not_run" })
    $lines.Add("## Not run")
    $lines.Add("")
    if ($notRun.Count -eq 0) {
        $lines.Add("None.")
    } else {
        foreach ($scenario in $notRun) {
            $lines.Add(("- **{0}** ({1}) - {2}" -f $scenario.id, $scenario.title, $scenario.blocker))
        }
    }
    $lines.Add("")

    $lines.Add("## Warnings (non-blocking)")
    $lines.Add("")
    if (@($Summary.warnings).Count -eq 0) {
        $lines.Add("None recorded.")
    } else {
        foreach ($warning in @($Summary.warnings)) {
            $lines.Add(("- **{0}** ``{1}`` - {2}" -f $warning.scenario, $warning.name, $warning.detail))
        }
    }
    $lines.Add("")

    $lines.Add("## Unattended acceptance assertions (spec 13.3)")
    $lines.Add("")
    $blockerClaim = if ($Summary.assertions.zero_visible_blockers_claimed) { "CLAIMED" } else { "NOT CLAIMED" }
    $guidanceClaim = if ($Summary.assertions.zero_manual_guidance_claimed) { "CLAIMED" } else { "NOT CLAIMED" }
    $lines.Add(("- Zero visible blocking errors: **{0}**" -f $blockerClaim))
    $lines.Add(("- Zero manual guidance: **{0}**" -f $guidanceClaim))
    $lines.Add(("- Reason: {0}" -f $Summary.assertions.reason))
    $lines.Add("")

    $lines.Add("## Harness inventory at run time")
    $lines.Add("")
    $lines.Add("| ID | Expected script pattern | Presence | Matched script(s) |")
    $lines.Add("|---|---|---|---|")
    foreach ($item in @($Summary.inventory)) {
        $lines.Add(("| {0} | ``{1}`` | {2} | {3} |" -f $item.id, $item.pattern, $item.status, (Format-MarkdownCell (($item.matches) -join ", "))))
    }
    $lines.Add("")

    $missing = @($Summary.inventory | Where-Object { $_.status -eq "missing" })
    $lines.Add("## What a full S0-S10 acceptance run needs")
    $lines.Add("")
    $lines.Add(("- Every scenario script present (``s0-*.ps1`` ... ``s10-*.ps1``). Missing at this run: {0}." -f $(if ($missing.Count -eq 0) { "none" } else { ($missing | ForEach-Object { "``" + $_.pattern + "``" }) -join ", " })))
    $lines.Add("- The EzTest API, web, executor, real LLM and SUT reachable as asserted by S0.")
    $lines.Add("- One explicit requirement source: ``-RequirementInput`` (small input, expected count validated) or ``-FullSutDoc -FullSutDocPath`` (legacy full document). There is no silent full-document fallback.")
    $lines.Add("- Any product/runtime blockers reported above fixed and verified by rerunning the affected scenario.")
    $lines.Add("- Rerun the entrypoint; acceptance is complete only when this report shows **status PASSED**, every scenario PASS, and the summary ``assertions.all_scenarios_passed`` is true.")
    $lines.Add("")
    $lines.Add('```powershell')
    $lines.Add("powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts\acceptance-021\run-all.ps1 ``")
    $lines.Add("  -RequirementInput docs\version-V0.2.1\acceptance\inputs\small-a.md -ExpectedRequirements 3")
    $lines.Add('```')
    $lines.Add("")
    $lines.Add("_Generated by run-all.ps1. Rerun to refresh; do not edit by hand._")

    $text = ($lines -join [Environment]::NewLine) + [Environment]::NewLine
    Write-EvidenceText -Path $Path -Text $text
}

# ---------------------------------------------------------------------------
# Write summary + report + exit
# ---------------------------------------------------------------------------

$writeError = ""
try {
    Write-EvidenceJson -Path $SummaryPath -Value $summary
} catch {
    $writeError = "failed to write summary ${SummaryPath}: $($_.Exception.Message)"
}
try {
    Write-AcceptanceReport -Summary $summary -Path $ReportPath -RepoRoot $context.RepoRoot
} catch {
    if ($writeError) { $writeError += "; " }
    $writeError += "failed to write report ${ReportPath}: $($_.Exception.Message)"
}

if (-not [string]::IsNullOrWhiteSpace($writeError)) {
    Write-Host "ERROR: $writeError" -ForegroundColor Red
    Write-RunLog -Path $logPath -Text "ERROR: $writeError"
    $overallStatus = "error"
    $overallExit = 2
}

$summaryDisplay = Resolve-RepoRelativePath -Context $context -Path $SummaryPath
$reportDisplay = Resolve-RepoRelativePath -Context $context -Path $ReportPath
$finalLine = "run-all finished: status=$overallStatus exit=$overallExit passed=$($totals.passed)/$($totals.scenarios) summary=$summaryDisplay report=$reportDisplay"
Write-RunLog -Path $logPath -Text $finalLine
Write-Host ""
switch ($overallStatus) {
    "passed" { Write-Host $finalLine -ForegroundColor Green }
    "failed" { Write-Host $finalLine -ForegroundColor Red }
    default { Write-Host $finalLine -ForegroundColor Yellow }
}

exit $overallExit
