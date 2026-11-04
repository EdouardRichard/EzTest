#requires -Version 5.1
# Focused offline tests for Task 111 (P10-T16): small-input P0 design coverage
# and the S4/S5 case-selection coverage contract.
#
#   powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance-021/tests/p0-design-coverage.Tests.ps1
#
# These are pure helper tests: no HTTP, no database and no SUT is touched.
Set-StrictMode -Version 3.0
$ErrorActionPreference = "Stop"

$testsDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$scriptsDir = Split-Path -Parent $testsDir
$repoRoot = Split-Path -Parent (Split-Path -Parent $scriptsDir)
. (Join-Path $scriptsDir "lib.ps1")
. (Join-Path $scriptsDir "lib-s3-s6.ps1")

$expectedIds = @("FR-011", "FR-014", "FR-025")

$script:checks = 0
$script:failures = 0

function Assert-True {
    param([bool] $Condition, [string] $Message)
    $script:checks++
    if (-not $Condition) {
        $script:failures++
        Write-Host "[FAIL] $Message" -ForegroundColor Red
    } else {
        Write-Host "[PASS] $Message"
    }
}

function Assert-Equal {
    param($Expected, $Actual, [string] $Message)
    Assert-True -Condition ($Expected -eq $Actual) -Message ("{0} (expected={1} actual={2})" -f $Message, $Expected, $Actual)
}

# ---------------------------------------------------------------------------
# Fixtures: the three live small-input requirements and their draft cases. The
# card shape is the flattened design-graph card produced by
# Get-AcceptanceCaseCards (requirement_id + case_id).
# ---------------------------------------------------------------------------

function New-SmallRequirements {
    return @(
        [pscustomobject]@{ id = "req-011"; title = "FR-011 账号密码登录"; description = ""; status = "clarified" },
        [pscustomobject]@{ id = "req-014"; title = "FR-014 页面访问控制与权限跳转"; description = ""; status = "clarified" },
        [pscustomobject]@{ id = "req-025"; title = "FR-025 保存草稿与发布"; description = ""; status = "clarified" }
    )
}

function New-SmallCaseCards {
    param([string[]] $ExcludeSutIds = @())
    $cards = @()
    $groups = @(
        @{ requirement = "req-011"; sut = "FR-011"; count = 3; prefix = "AC-FR-011" },
        @{ requirement = "req-014"; sut = "FR-014"; count = 4; prefix = "AC-FR-014" },
        @{ requirement = "req-025"; sut = "FR-025"; count = 3; prefix = "AC-FR-025" }
    )
    foreach ($group in $groups) {
        if ($ExcludeSutIds -contains $group.sut) { continue }
        for ($index = 1; $index -le [int]$group.count; $index++) {
            $cards += [pscustomobject]@{
                requirement_id = $group.requirement
                case_id        = ("case-" + $group.sut + "-" + $index)
                title          = ("$($group.prefix)-0$index 用例")
                status         = "draft"
            }
        }
    }
    return $cards
}

function Get-FixtureSutIds {
    param($Cards, $Requirements)
    $byRequirement = @{}
    foreach ($requirement in @($Requirements)) {
        $byRequirement[[string]$requirement.id] = Get-RequirementSutId -Requirement $requirement
    }
    $sutIds = @()
    foreach ($card in @($Cards)) { $sutIds += [string]$byRequirement[[string]$card.requirement_id] }
    return $sutIds
}

# ---------------------------------------------------------------------------
# The design-completion coverage gate. S3 may only pass when every expected P0
# requirement has at least one case; a missing P0 is a hard failure, never a
# warning that gets hidden behind an "11/11 PASS".
# ---------------------------------------------------------------------------

function TestAllP0RequirementsGetCases {
    $requirements = New-SmallRequirements
    $cards = New-SmallCaseCards
    $coverage = Get-AcceptanceP0CaseCoverage -ExpectedFrIds $expectedIds -Requirements $requirements -CaseCards $cards
    Assert-Equal -Expected $true -Actual $coverage.Passed -Message "all three small-input P0 requirements with cases pass the design coverage gate"
    Assert-Equal -Expected 3 -Actual @($coverage.Covered).Count -Message "the gate reports all three covered P0 requirements"
    Assert-Equal -Expected 0 -Actual @($coverage.Missing).Count -Message "the gate reports no missing P0 requirement"
    Assert-Equal -Expected 3 -Actual ([int]$coverage.CaseCountBySutId["FR-011"]) -Message "the gate counts the FR-011 cases"
    Assert-Equal -Expected 3 -Actual ([int]$coverage.CaseCountBySutId["FR-025"]) -Message "the gate counts the FR-025 cases"
    Assert-True -Condition ($coverage.Detail -match "3/3") -Message "the gate detail records 3/3 P0 coverage (actual=$($coverage.Detail))"
}

function TestMissingP0CaseFailsDesignGate {
    $requirements = New-SmallRequirements
    $cards = New-SmallCaseCards -ExcludeSutIds @("FR-025")
    $coverage = Get-AcceptanceP0CaseCoverage -ExpectedFrIds $expectedIds -Requirements $requirements -CaseCards $cards
    Assert-Equal -Expected $false -Actual $coverage.Passed -Message "a P0 requirement with zero cases fails the design coverage gate"
    Assert-Equal -Expected "FR-025" -Actual (@($coverage.Missing) -join ",") -Message "the gate names the missing P0 requirement"
    Assert-Equal -Expected "FR-011,FR-014" -Actual (@($coverage.Covered) -join ",") -Message "the gate keeps the covered P0 requirements"
    Assert-True -Condition ($coverage.Detail -match "FR-025") -Message "the gate detail names the missing P0 requirement (actual=$($coverage.Detail))"

    $empty = Get-AcceptanceP0CaseCoverage -ExpectedFrIds @() -Requirements $requirements -CaseCards $cards
    Assert-Equal -Expected $false -Actual $empty.Passed -Message "an empty expected P0 set fails closed instead of claiming coverage"
}

# ---------------------------------------------------------------------------
# S4 selection. The candidate scan must be able to reach every requirement
# (retry-9 scanned only the first six draft cases, all FR-011/FR-014), and the
# selection must include one eligible case per expected P0 requirement so S5
# really executes FR-025.
# ---------------------------------------------------------------------------

function TestFR025Selectable {
    $requirements = New-SmallRequirements
    $cards = New-SmallCaseCards
    $sampled = @(Get-AcceptanceCaseCandidateSample -Cards $cards -PerRequirement 2 -MaxCandidates 40)
    $sampledSutIds = @(Get-FixtureSutIds -Cards $sampled -Requirements $requirements)
    Assert-True -Condition ($sampledSutIds -contains "FR-025") -Message "the candidate scan reaches the FR-025 draft cases"
    Assert-Equal -Expected 6 -Actual $sampled.Count -Message "the candidate scan samples two cases per requirement before the cap"
    Assert-Equal -Expected 2 -Actual @($sampled | Where-Object { [string]$_.requirement_id -eq "req-011" }).Count -Message "the candidate scan keeps exactly two FR-011 candidates"

    $sutIds = @(Get-FixtureSutIds -Cards $cards -Requirements $requirements)
    $selection = Select-AcceptanceCoverageCases -Candidates $cards -SutIds $sutIds -RequiredSutIds $expectedIds -MaxSelected 4
    Assert-Equal -Expected 3 -Actual @($selection.SelectedIndices).Count -Message "the selection picks one representative case per P0 requirement"
    Assert-Equal -Expected "FR-011,FR-014,FR-025" -Actual (@($selection.SelectedSutIds) -join ",") -Message "the selection covers all three P0 requirements in input order"
    Assert-Equal -Expected 0 -Actual @($selection.MissingRequiredSutIds).Count -Message "the selection reports no missing required requirement"
    $selectedCaseIds = @()
    foreach ($index in @($selection.SelectedIndices)) { $selectedCaseIds += [string]$cards[$index].case_id }
    Assert-True -Condition (@($selectedCaseIds | Where-Object { $_ -like "case-FR-025-*" }).Count -eq 1) -Message "the selection contains an FR-025 case ($($selectedCaseIds -join ','))"

    # A requirement without any candidate is reported, never silently dropped.
    $withoutFr025 = @(New-SmallCaseCards -ExcludeSutIds @("FR-025"))
    $withoutSutIds = @(Get-FixtureSutIds -Cards $withoutFr025 -Requirements $requirements)
    $missing = Select-AcceptanceCoverageCases -Candidates $withoutFr025 -SutIds $withoutSutIds -RequiredSutIds $expectedIds -MaxSelected 4
    Assert-Equal -Expected "FR-025" -Actual (@($missing.MissingRequiredSutIds) -join ",") -Message "the selection names FR-025 when no FR-025 case is selectable"
}

function TestExpectedP0SetFromRunState {
    # A targeted S4/S5 rerun may omit -ExpectedFrIds; S2 already stored the
    # small-input profile in state.json, so the scripts must still know FR-025.
    $state = @{
        requirement_input_mode       = "small-input"
        requirement_expected_fr_ids  = @("FR-011", "FR-014", "FR-025")
    }
    $fromState = @(Get-AcceptanceExpectedSutIds -ExpectedFrIds "" -State $state)
    Assert-Equal -Expected "FR-011,FR-014,FR-025" -Actual ($fromState -join ",") -Message "S4/S5 recover the expected P0 set from the small-input run state"
    $explicit = @(Get-AcceptanceExpectedSutIds -ExpectedFrIds "FR-025, FR-011" -State $state)
    Assert-Equal -Expected "FR-025,FR-011" -Actual ($explicit -join ",") -Message "an explicit -ExpectedFrIds list wins over the run state"
    $legacy = @(Get-AcceptanceExpectedSutIds -ExpectedFrIds "" -State @{ requirement_input_mode = "full-sut-doc" })
    Assert-Equal -Expected 0 -Actual $legacy.Count -Message "legacy full-document runs have no forced P0 set"
}

# ---------------------------------------------------------------------------
# Script wiring: S3 must wait for and hard-fail on the P0 coverage gate, S4
# must scan/select across requirements, and run-all must hand the expected FR
# IDs to S4/S5 so FR-025 is selected and executed.
# ---------------------------------------------------------------------------

function Get-AcceptanceScriptSource {
    param([Parameter(Mandatory = $true)] [string] $Name)
    $path = Join-Path $scriptsDir $Name
    if (-not (Test-Path -LiteralPath $path)) { throw "missing script: $path" }
    return (Get-Content -LiteralPath $path -Raw -Encoding UTF8)
}

function Get-AcceptanceScriptParameters {
    param([Parameter(Mandatory = $true)] [string] $Name)
    $path = Join-Path $scriptsDir $Name
    $tokens = $null
    $errors = $null
    $ast = [System.Management.Automation.Language.Parser]::ParseFile($path, [ref]$tokens, [ref]$errors)
    if (@($errors).Count -gt 0) { throw "cannot parse ${Name}: $(@($errors | ForEach-Object { $_.Message }) -join '; ')" }
    if ($null -eq $ast.ParamBlock) { return @() }
    return @($ast.ParamBlock.Parameters | ForEach-Object { $_.Name.VariablePath.UserPath })
}

function Test-S3CoverageGateWiring {
    $text = Get-AcceptanceScriptSource -Name "s3-design.ps1"
    Assert-True -Condition ($text -match 'Get-AcceptanceP0CaseCoverage') -Message "S3 uses the P0 case coverage gate"
    Assert-True -Condition (([regex]::Matches($text, 'Get-AcceptanceP0CaseCoverage')).Count -ge 2) -Message "S3 evaluates the coverage gate in the wait and again on the final graph"
    Assert-True -Condition ($text -match '\$caseWaitDescription = if \(\$isSmallInput\) \{ "all small-input P0 design cases" \}') -Message "S3 waits for all small-input P0 cases instead of the first case node"
    Assert-True -Condition ($text -match '-Description \$caseWaitDescription') -Message "S3 uses the P0 coverage wait description for the bounded case wait"
    Assert-True -Condition ($text -match 'all small-input requirements have design cases"[\s\S]{0,500}-Severity assertion') -Message "the small-input coverage check is a hard assertion, not a warning"
    Assert-True -Condition ($text -match 'P0 requirements have design cases"[\s\S]{0,500}\$p0Coverage\.Passed') -Message "the P0 design-case check is driven by the coverage gate result"
    Assert-True -Condition ($text -match '\$p0CoverageSeverity') -Message "the P0 design-case check is a hard assertion in small-input mode"
    Assert-True -Condition ($text -match 'each small-input P0 requirement has an executable case"[\s\S]{0,600}-Severity assertion') -Message "S3 hard-fails when a P0 requirement has no executable case"
    Assert-True -Condition ($text -match 'p0-case-executable\.json') -Message "S3 records the per-P0 executable-case evidence"
}

function Test-S4SelectionCoverageWiring {
    $names = @(Get-AcceptanceScriptParameters -Name "s4-confirm.ps1")
    Assert-True -Condition ($names -contains "ExpectedFrIds") -Message "S4 accepts -ExpectedFrIds from the runner"
    $text = Get-AcceptanceScriptSource -Name "s4-confirm.ps1"
    Assert-True -Condition ($text -match 'Get-AcceptanceCaseCandidateSample') -Message "S4 scans candidates across every requirement instead of the first six cards"
    Assert-True -Condition ($text -match 'Select-AcceptanceCoverageCases') -Message "S4 uses the per-requirement coverage selection helper"
    Assert-True -Condition ($text -match 'selected cases cover the expected small-input requirements"[\s\S]{0,600}-Severity assertion') -Message "S4 hard-fails when the selection misses an expected P0 requirement"
    Assert-True -Condition ($text -match 'missing_required_sut_ids') -Message "S4 records the missing required requirement IDs in its selection evidence"
    Assert-True -Condition ($text -match 'Get-AcceptanceExpectedSutIds') -Message "S4 recovers the expected P0 set from the small-input run state on a standalone rerun"
}

function Test-S5ExecutionCoverageWiring {
    $names = @(Get-AcceptanceScriptParameters -Name "s5-execute.ps1")
    Assert-True -Condition ($names -contains "ExpectedFrIds") -Message "S5 accepts -ExpectedFrIds from the runner"
    $text = Get-AcceptanceScriptSource -Name "s5-execute.ps1"
    Assert-True -Condition ($text -match 'Get-AcceptanceP0CaseCoverage') -Message "S5 rechecks the confirmed selection against the expected P0 set"
    Assert-True -Condition ($text -match 'confirmed selection covers the small-input requirements"[\s\S]{0,700}-Severity assertion') -Message "S5 hard-fails when the confirmed selection would not execute an expected P0 requirement"
    Assert-True -Condition ($text -match 'Get-AcceptanceExpectedSutIds') -Message "S5 recovers the expected P0 set from the small-input run state on a standalone rerun"
}

function Test-RunnerPassesExpectedFrIdsToS4AndS5 {
    $text = Get-AcceptanceScriptSource -Name "run-all.ps1"
    Assert-True -Condition ($text -match '@\("s4", "s5"\) -contains \$scenario\.Id') -Message "run-all targets S4 and S5 for the expected FR IDs"
    Assert-True -Condition ($text -match '@\("s4", "s5"\) -contains \$scenario\.Id[\s\S]{0,600}"-ExpectedFrIds"') -Message "run-all passes -ExpectedFrIds from the S4/S5 hand-off block"
    Assert-True -Condition ($text -match 'requirementProfile\.ExpectedFrIds') -Message "run-all uses the resolved small-input profile for the S4/S5 hand-off"
}

function Test-ModifiedAcceptanceScriptsParse {
    foreach ($name in @("lib-s3-s6.ps1", "s3-design.ps1", "s4-confirm.ps1", "s5-execute.ps1", "run-all.ps1")) {
        $path = Join-Path $scriptsDir $name
        $tokens = $null
        $errors = $null
        [void][System.Management.Automation.Language.Parser]::ParseFile($path, [ref]$tokens, [ref]$errors)
        $message = (@($errors | ForEach-Object { $_.Message }) -join "; ")
        Assert-Equal -Expected 0 -Actual @($errors).Count -Message "$name parses without errors $message"
    }
}

# ---------------------------------------------------------------------------
# Run
# ---------------------------------------------------------------------------

TestAllP0RequirementsGetCases
TestMissingP0CaseFailsDesignGate
TestFR025Selectable
TestExpectedP0SetFromRunState
Test-S3CoverageGateWiring
Test-S4SelectionCoverageWiring
Test-S5ExecutionCoverageWiring
Test-RunnerPassesExpectedFrIdsToS4AndS5
Test-ModifiedAcceptanceScriptsParse

Write-Host ""
Write-Host ("P0 design coverage tests: {0} checks, {1} failure(s)" -f $script:checks, $script:failures)
if ($script:failures -gt 0) { exit 1 }
exit 0
