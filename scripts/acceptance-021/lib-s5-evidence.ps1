#requires -Version 5.1
<#
.SYNOPSIS
    Pure real-execution evidence gate for the S5 acceptance scenario (Task 73).

.DESCRIPTION
    The S5 script drives the real executor through the public API. Before Task 73
    it could PASS on a control-plane-only execution where every selected case
    ended as automation/agent_failure with zero recorded steps, assertions and
    evidence (QA-F1, final review Important #10). This helper makes the verdict
    explicit and testable without a live stack:

      * at least one terminal case run must have recorded real step results,
        assertion results and captured evidence;
      * a wave where every terminal case run is an automation/agent_failure with
        zero steps/assertions/evidence must FAIL (never PASS).

    A failing case is a valid execution result; a missing execution is not.

.NOTES
    The helper takes plain objects/hashtables so it can be dot-sourced by both
    s5-execute.ps1 and tests/s5-evidence-gate.Tests.ps1.
#>
Set-StrictMode -Version 3.0

function Get-S5PropertyValue {
    param($Object, [string] $Name, $Default = $null)
    if ($null -eq $Object) { return $Default }
    if ($Object -is [System.Collections.IDictionary]) {
        if ($Object.Contains($Name)) { return $Object[$Name] }
        return $Default
    }
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) { return $Default }
    return $property.Value
}

function Get-S5ExecutionEvidenceGate {
    [CmdletBinding()]
    param(
        [object[]] $Runs = @(),
        [hashtable] $Recorded = @{}
    )

    $terminalStatuses = @("passed", "failed", "blocked", "inconclusive", "skipped", "superseded", "interrupted")
    $terminal = @($Runs | Where-Object {
            $_ -and ($terminalStatuses -contains [string](Get-S5PropertyValue -Object $_ -Name "status" -Default ""))
        })

    $runsWithSteps = 0
    $runsWithAssertions = 0
    $runsWithEvidence = 0
    $runsWithAll = 0
    $agentFailureZeroEvidence = 0

    foreach ($run in $terminal) {
        $id = [string](Get-S5PropertyValue -Object $run -Name "id" -Default "")
        $steps = 0
        $assertions = 0
        $recordedEvidence = $null
        if ($null -ne $Recorded -and $Recorded.ContainsKey($id)) {
            $entry = $Recorded[$id]
            $steps = [int](Get-S5PropertyValue -Object $entry -Name "steps" -Default 0)
            $assertions = [int](Get-S5PropertyValue -Object $entry -Name "assertions" -Default 0)
            $recordedEvidence = [int](Get-S5PropertyValue -Object $entry -Name "evidence" -Default 0)
        }

        $evidence = Get-S5PropertyValue -Object $run -Name "evidence_count" -Default $null
        if ($null -eq $evidence) {
            $evidence = Get-S5PropertyValue -Object (Get-S5PropertyValue -Object $run -Name "summary" -Default $null) -Name "evidence_count" -Default 0
        }
        if ($null -ne $recordedEvidence) { $evidence = $recordedEvidence }
        $evidence = [int]$evidence

        $verdictReason = [string](Get-S5PropertyValue -Object $run -Name "verdict_reason" -Default "")
        if ([string]::IsNullOrWhiteSpace($verdictReason)) {
            $verdictReason = [string](Get-S5PropertyValue -Object (Get-S5PropertyValue -Object $run -Name "summary" -Default $null) -Name "verdict_reason" -Default "")
        }
        $failureClass = [string](Get-S5PropertyValue -Object $run -Name "failure_class" -Default "")
        $isAgentFailure = (($failureClass -eq "automation" -and $verdictReason -eq "agent_failure") -or $failureClass -eq "executor_lost")

        if ($steps -ge 1) { $runsWithSteps++ }
        if ($assertions -ge 1) { $runsWithAssertions++ }
        if ($evidence -ge 1) { $runsWithEvidence++ }
        if ($steps -ge 1 -and $assertions -ge 1 -and $evidence -ge 1) { $runsWithAll++ }
        if ($isAgentFailure -and $steps -eq 0 -and $assertions -eq 0 -and $evidence -eq 0) {
            $agentFailureZeroEvidence++
        }
    }

    $allAgentFailureZeroEvidence = ($terminal.Count -gt 0 -and $agentFailureZeroEvidence -eq $terminal.Count)
    $reason = ""
    if ($terminal.Count -eq 0) {
        $reason = "no terminal case run was observed"
    } elseif ($runsWithAll -eq 0) {
        if ($allAgentFailureZeroEvidence) {
            $reason = "every selected case failed as automation/agent_failure with zero steps, assertions and evidence"
        } elseif ($runsWithSteps -eq 0) {
            $reason = "no terminal case run recorded step results"
        } elseif ($runsWithAssertions -eq 0) {
            $reason = "no terminal case run recorded assertion results"
        } elseif ($runsWithEvidence -eq 0) {
            $reason = "no terminal case run captured evidence"
        } else {
            $reason = "no single case run recorded steps, assertions and evidence together"
        }
    }

    return [pscustomobject]@{
        passed                             = ($reason -eq "")
        terminal_runs                      = $terminal.Count
        runs_with_steps                    = $runsWithSteps
        runs_with_assertions               = $runsWithAssertions
        runs_with_evidence                 = $runsWithEvidence
        runs_with_steps_assertions_evidence = $runsWithAll
        agent_failure_zero_evidence_runs   = $agentFailureZeroEvidence
        all_agent_failure_zero_evidence    = $allAgentFailureZeroEvidence
        reason                             = $reason
    }
}
