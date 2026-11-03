#requires -Version 5.1
# Focused tests for the S5 real-execution evidence gate (Task 73 / QA-F1).
# The gate is a pure helper so it can be tested without a live stack:
#   powershell -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance-021/tests/s5-evidence-gate.Tests.ps1
Set-StrictMode -Version 3.0
$ErrorActionPreference = "Stop"

$testsDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$libDir = Split-Path -Parent $testsDir
. (Join-Path $libDir "lib-s5-evidence.ps1")

$script:failures = 0
$script:checks = 0

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

function New-RunSummary {
    param([string] $Id, [string] $Status, [string] $FailureClass, [string] $VerdictReason, [int] $EvidenceCount)
    return [pscustomobject]@{
        id             = $Id
        status         = $Status
        failure_class  = $FailureClass
        verdict_reason = $VerdictReason
        evidence_count = $EvidenceCount
    }
}

function New-Recorded {
    param([int] $Steps, [int] $Assertions, [int] $Evidence = 0)
    return @{ steps = $Steps; assertions = $Assertions; evidence = $Evidence }
}

# QA-F1: every selected case died as automation/agent_failure with zero
# steps/assertions/evidence. The old S5 treated that as PASS (control plane only).
function Test-S5RejectsZeroEvidence {
    $runs = @(
        New-RunSummary -Id "1" -Status "failed" -FailureClass "automation" -VerdictReason "agent_failure" -EvidenceCount 0
        New-RunSummary -Id "2" -Status "failed" -FailureClass "automation" -VerdictReason "agent_failure" -EvidenceCount 0
        New-RunSummary -Id "3" -Status "failed" -FailureClass "automation" -VerdictReason "agent_failure" -EvidenceCount 0
    )
    $recorded = @{
        "1" = New-Recorded -Steps 0 -Assertions 0
        "2" = New-Recorded -Steps 0 -Assertions 0
        "3" = New-Recorded -Steps 0 -Assertions 0
    }

    $gate = Get-S5ExecutionEvidenceGate -Runs $runs -Recorded $recorded
    Assert-Equal -Expected $false -Actual $gate.passed -Message "all agent_failure/zero-evidence case runs must FAIL S5"
    Assert-Equal -Expected $true -Actual $gate.all_agent_failure_zero_evidence -Message "all-agent-failure-zero-evidence is reported"
    Assert-Equal -Expected 3 -Actual $gate.agent_failure_zero_evidence_runs -Message "all three silent agent failures are counted"
    Assert-True -Condition (-not [string]::IsNullOrWhiteSpace([string]$gate.reason)) -Message "the gate explains why it failed"
}

function Test-S5AcceptsRecordedExecution {
    $runs = @(
        New-RunSummary -Id "1" -Status "passed" -FailureClass "" -VerdictReason "passed" -EvidenceCount 1
        New-RunSummary -Id "2" -Status "failed" -FailureClass "automation" -VerdictReason "agent_failure" -EvidenceCount 0
    )
    $recorded = @{
        "1" = New-Recorded -Steps 3 -Assertions 2 -Evidence 1
        "2" = New-Recorded -Steps 0 -Assertions 0
    }

    $gate = Get-S5ExecutionEvidenceGate -Runs $runs -Recorded $recorded
    Assert-Equal -Expected $true -Actual $gate.passed -Message "one real executed case with steps/assertions/evidence passes S5"
    Assert-Equal -Expected $false -Actual $gate.all_agent_failure_zero_evidence -Message "a mixed wave is not all-agent-failure"
    Assert-Equal -Expected 1 -Actual $gate.runs_with_steps_assertions_evidence -Message "the recorded run is counted once"
}

function Test-S5RequiresStepsAssertionsEvidence {
    $runs = @(New-RunSummary -Id "1" -Status "failed" -FailureClass "automation" -VerdictReason "step_failed" -EvidenceCount 0)
    $recorded = @{ "1" = New-Recorded -Steps 2 -Assertions 0 -Evidence 0 }
    $gate = Get-S5ExecutionEvidenceGate -Runs $runs -Recorded $recorded
    Assert-Equal -Expected $false -Actual $gate.passed -Message "steps without assertions/evidence do not prove a real execution"

    $runs = @(New-RunSummary -Id "1" -Status "passed" -FailureClass "" -VerdictReason "passed" -EvidenceCount 0)
    $recorded = @{ "1" = New-Recorded -Steps 2 -Assertions 2 -Evidence 0 }
    $gate = Get-S5ExecutionEvidenceGate -Runs $runs -Recorded $recorded
    Assert-Equal -Expected $false -Actual $gate.passed -Message "steps/assertions without evidence do not prove a real execution"

    $runs = @(New-RunSummary -Id "1" -Status "inconclusive" -FailureClass "" -VerdictReason "incomplete" -EvidenceCount 1)
    $recorded = @{ "1" = New-Recorded -Steps 1 -Assertions 1 -Evidence 1 }
    $gate = Get-S5ExecutionEvidenceGate -Runs $runs -Recorded $recorded
    Assert-Equal -Expected $true -Actual $gate.passed -Message "a truthful inconclusive run with recorded results is a real execution"
}

function Test-S5RejectsZeroEvidenceBlockedRun {
    $runs = @(New-RunSummary -Id "1" -Status "blocked" -FailureClass "blocked" -VerdictReason "blocked" -EvidenceCount 0)
    $recorded = @{ "1" = New-Recorded -Steps 0 -Assertions 0 }
    $gate = Get-S5ExecutionEvidenceGate -Runs $runs -Recorded $recorded
    Assert-Equal -Expected $false -Actual $gate.passed -Message "a blocked case without recorded results/evidence is not a functional PASS"
    Assert-Equal -Expected $false -Actual $gate.all_agent_failure_zero_evidence -Message "a blocked run is not counted as an agent failure"
}

function Test-S5RejectsNoTerminalRun {
    $gate = Get-S5ExecutionEvidenceGate -Runs @() -Recorded @{}
    Assert-Equal -Expected $false -Actual $gate.passed -Message "an execution without terminal case runs cannot PASS"
}

Test-S5RejectsZeroEvidence
Test-S5AcceptsRecordedExecution
Test-S5RequiresStepsAssertionsEvidence
Test-S5RejectsZeroEvidenceBlockedRun
Test-S5RejectsNoTerminalRun

Write-Host ""
Write-Host ("S5 evidence gate tests: {0} checks, {1} failure(s)" -f $script:checks, $script:failures)
if ($script:failures -gt 0) { exit 1 }
exit 0
