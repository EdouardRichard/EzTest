#requires -Version 5.1
<#
.SYNOPSIS
    EzTest v0.22 W1 composition-root end-to-end wrapper (W1-9).

.DESCRIPTION
    Runs scripts/v022-w0-regression.ps1 -W1: a disposable composition root with
    a fresh PostgreSQL database, the real cmd/eztest binary, the real executor,
    the real Next.js frontend, the real DeepSeek LLM and the real SUT. The W1
    path drives the version to completion through POST /api/versions/{id}/waves
    (empty body = every current+confirmed case) instead of the W0 manual-intent
    path, then replays the DB/API assertions over docs/version-V0.22/acceptance/
    evidence/w1/run/w1-verdict.json.

    Exit codes: 0 = PASS, 1 = RED (flow or implementation failure),
    2 = EXTERNAL-BLOCKED (missing .env key, unreachable PostgreSQL/executor/LLM,
    missing SUT checkout).
#>
param(
    [switch]$SkipBuild,
    [switch]$ReuseFlow,
    [int]$DesignTimeoutSeconds = 720,
    [int]$ExecutionTimeoutSeconds = 900
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot
$regression = Join-Path $PSScriptRoot 'v022-w0-regression.ps1'
$runDir = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence\w1\run'
$verdictPath = Join-Path $runDir 'w1-verdict.json'

$argv = @(
    '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $regression, '-W1',
    '-DesignTimeoutSeconds', "$DesignTimeoutSeconds",
    '-ExecutionTimeoutSeconds', "$ExecutionTimeoutSeconds"
)
if ($SkipBuild) { $argv += '-SkipBuild' }
if ($ReuseFlow) { $argv += '-ReuseFlow' }

& powershell.exe @argv
$runExit = $LASTEXITCODE
if ($runExit -eq 2) {
    Write-Host 'W1 E2E: EXTERNAL-BLOCKED (preflight failed; see run/w1-verdict.json and run/REPORT.md)'
    exit 2
}

# The wrapper is the machine check over the regression's own verdict: the flow
# exit code, every RED entry, every zero-data counter and every named assertion
# must be green. Evidence files alone are not proof.
$red = New-Object System.Collections.Generic.List[string]
if (-not (Test-Path $verdictPath)) {
    $red.Add("missing verdict: $verdictPath")
} else {
    $verdict = Get-Content $verdictPath -Raw -Encoding UTF8 | ConvertFrom-Json
    if ([int]$verdict.flowExit -ne 0) { $red.Add("flowExit=$($verdict.flowExit)") }
    foreach ($entry in @($verdict.red)) { if ($entry) { $red.Add([string]$entry) } }
    foreach ($key in 'requirements', 'cases', 'planItems', 'waves', 'caseRuns', 'evidenceRows', 'events', 'planEvents', 'resourceDeltaEvents', 'scopeSnapshots') {
        if ($null -eq $verdict.dbCounters -or [int64]$verdict.dbCounters.$key -lt 1) {
            $red.Add("zero data: dbCounters.$key=$($verdict.dbCounters.$key)")
        }
    }
    if ($verdict.assertions) {
        foreach ($property in $verdict.assertions.PSObject.Properties) {
            if (-not $property.Value) { $red.Add("assertion failed: $($property.Name)") }
        }
    }
}
if ($runExit -ne 0) { $red.Add("regression exit=$runExit") }

if ($red.Count -gt 0) {
    Write-Host ''
    Write-Host "W1 E2E: RED ($($red.Count) item(s))"
    foreach ($entry in $red) { Write-Host "   - $entry" }
    Write-Host "evidence: $runDir"
    exit 1
}
Write-Host "W1 E2E: PASS (evidence: $runDir)"
exit 0
