#requires -Version 5.1
<#
.SYNOPSIS
    EzTest v0.22 W0 exit gate (Task 39).

.DESCRIPTION
    Certifies the W0 exit before W1 starts:
      1. required evidence files exist (REPORT.md, EXIT.md, PROGRESS.md,
         run/verdict.json);
      2. the Task 38 verdict is green (flowExit=0, redCount=0, dbChecks.red
         empty) and every zero-data counter is non-zero;
      3. EXIT.md contains no item marked with the red status token;
      4. the full W0 verification commands are run fresh.

    Commands (each writes a log under .../evidence/w0/run/exit/):
      go test ./... -count=1
      powershell.exe -File scripts/test-integration.ps1 ./... -p 1 -count=1
      pnpm --dir frontend test
      pnpm --dir frontend typecheck
      node scripts/v021-contract-check.cjs
      node scripts/v022-contract-check.cjs
      powershell.exe -File scripts/v022-w0-regression.ps1 -ReuseFlow

    Exit code 0 only when the pre-check, every command, and the replayed
    regression verdict are green. A missing EXIT.md is a hard pre-check failure
    by design: the evidence this gate certifies must exist before it can pass.

.NOTES
    Windows PowerShell 5.1 compatible. Run from the repository root:
        powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts\v022-w0-exit.ps1
#>

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

$w0Evidence = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence\w0'
$runEvidence = Join-Path $w0Evidence 'run'
$exitEvidence = Join-Path $runEvidence 'exit'
$verdictPath = Join-Path $runEvidence 'verdict.json'
$reportPath = Join-Path $w0Evidence 'REPORT.md'
$exitPath = Join-Path $w0Evidence 'EXIT.md'
$progressPath = Join-Path $repoRoot 'docs\version-V0.22\PROGRESS.md'

$goExe = Join-Path $repoRoot '.tools\go\bin\go.exe'
if (-not (Test-Path -LiteralPath $goExe)) {
    $goCommand = Get-Command go.exe -ErrorAction SilentlyContinue
    if (-not $goCommand) { throw 'Go is not available (.tools/go/bin/go.exe or go.exe on PATH).' }
    $goExe = $goCommand.Source
}
$nodeExe = (Get-Command node.exe -ErrorAction Stop).Source
$pnpmExe = (Get-Command pnpm.cmd -ErrorAction SilentlyContinue).Source
if (-not $pnpmExe) { $pnpmExe = (Get-Command pnpm -ErrorAction Stop).Source }
$powershellExe = (Get-Command powershell.exe -ErrorAction Stop).Source

$precheckFailures = New-Object System.Collections.Generic.List[string]

# ---------------------------------------------------------------------------
# 1. Required evidence files.
# ---------------------------------------------------------------------------
$required = @(
    @{ Path = $reportPath;   Label = 'REPORT.md' },
    @{ Path = $exitPath;     Label = 'EXIT.md' },
    @{ Path = $progressPath; Label = 'PROGRESS.md' },
    @{ Path = $verdictPath;  Label = 'run/verdict.json' }
)
foreach ($entry in $required) {
    if (-not (Test-Path -LiteralPath $entry.Path)) {
        $precheckFailures.Add("missing required file: $($entry.Label) ($($entry.Path))")
    }
}

# ---------------------------------------------------------------------------
# 2. Task 38 verdict: green and zero-data counters non-zero.
# ---------------------------------------------------------------------------
$verdict = $null
$verdictState = [ordered]@{
    flowExit = $null
    redCount = $null
    dbRedCount = $null
}
if (Test-Path -LiteralPath $verdictPath) {
    try {
        $verdict = Get-Content -LiteralPath $verdictPath -Raw -Encoding UTF8 | ConvertFrom-Json
        $verdictState.flowExit = [int]$verdict.flowExit
        $verdictState.redCount = [int]$verdict.redCount
        $verdictState.dbRedCount = @($verdict.dbChecks.red).Count
        if ($verdictState.flowExit -ne 0) {
            $precheckFailures.Add("verdict.flowExit=$($verdictState.flowExit) (expected 0)")
        }
        if ($verdictState.redCount -ne 0) {
            $redList = @($verdict.red) -join '; '
            $precheckFailures.Add("verdict.redCount=$($verdictState.redCount) (expected 0): $redList")
        }
        if ($verdictState.dbRedCount -ne 0) {
            $dbRedList = @($verdict.dbChecks.red) -join '; '
            $precheckFailures.Add("verdict.dbChecks.red has $($verdictState.dbRedCount) entr(ies): $dbRedList")
        }
        $zeroDataKeys = @(
            'completedRuns', 'planItems', 'planEvents', 'facts', 'waves',
            'doneExtractions', 'doneImages', 'versionCompleted',
            'stageEventKinds', 'evidenceRows'
        )
        foreach ($key in $zeroDataKeys) {
            $value = [int]$verdict.dbChecks.$key
            if ($value -le 0) {
                $precheckFailures.Add("verdict.dbChecks.$key=$value (zero-data/zero-consumer must not PASS)")
            }
        }
        if ([int]$verdict.dbChecks.emptyScopes -ne 0) {
            $precheckFailures.Add("verdict.dbChecks.emptyScopes=$($verdict.dbChecks.emptyScopes) (expected 0)")
        }
    } catch {
        $precheckFailures.Add("cannot parse run/verdict.json: $($_.Exception.Message)")
    }
}

# ---------------------------------------------------------------------------
# 3. EXIT.md may not contain an item marked with the red token.
# ---------------------------------------------------------------------------
if (Test-Path -LiteralPath $exitPath) {
    $exitText = Get-Content -LiteralPath $exitPath -Raw -Encoding UTF8
    if ($exitText -cmatch '\bRED\b') {
        $precheckFailures.Add('EXIT.md contains an item marked RED; a W0 exit with a failing item must not be signed off')
    }
}

if ($precheckFailures.Count -gt 0) {
    Write-Host ''
    Write-Host "== W0 exit pre-check failures ($($precheckFailures.Count)):"
    foreach ($failure in $precheckFailures) { Write-Host "   - $failure" }
    Write-Host ''
    Write-Host 'W0 EXIT: FAIL (pre-check; verification commands not run)'
    exit 1
}

# ---------------------------------------------------------------------------
# 4. Full W0 verification commands.
# ---------------------------------------------------------------------------
New-Item -ItemType Directory -Force -Path $exitEvidence | Out-Null
$checks = New-Object System.Collections.Generic.List[object]
$failures = New-Object System.Collections.Generic.List[string]

function Get-RepoRelativePath([string]$path) {
    return ($path.Substring($repoRoot.Length).TrimStart('\') -replace '\\', '/')
}

function Invoke-ExitGate {
    param(
        [string]$Id,
        [string]$Command,
        [string]$File,
        [string[]]$Arguments,
        [string]$WorkDir
    )
    Write-Host ''
    Write-Host "== [$Id] $Command"
    $logPath = Join-Path $exitEvidence ("$Id.log")
    $started = Get-Date
    $exitCode = -1
    $output = $null
    Push-Location -LiteralPath $WorkDir
    try {
        $global:LASTEXITCODE = 0
        $previousEAP = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        try {
            $output = & $File @Arguments 2>&1
            $exitCode = $LASTEXITCODE
        } catch {
            $exitCode = 1
            $output = @("gate invocation error: $($_.Exception.Message)")
        } finally {
            $ErrorActionPreference = $previousEAP
        }
    } finally {
        Pop-Location
    }
    if ($null -ne $output) {
        $output | Out-File -LiteralPath $logPath -Encoding utf8
        $output | Out-Host
    } else {
        Set-Content -LiteralPath $logPath -Value '' -Encoding UTF8
    }
    $duration = [math]::Round(((Get-Date) - $started).TotalSeconds, 1)
    $check = [ordered]@{
        id              = $Id
        command         = $Command
        workingDir      = Get-RepoRelativePath $WorkDir
        exitCode        = $exitCode
        durationSeconds = $duration
        log             = Get-RepoRelativePath $logPath
    }
    $checks.Add($check)
    if ($exitCode -ne 0) {
        $failures.Add("$Id exited $exitCode (log: $(Get-RepoRelativePath $logPath))")
    } else {
        Write-Host "   -> exit 0 ($duration s)"
    }
}

Invoke-ExitGate -Id 'backend-go-test' `
    -Command 'go test ./... -count=1' `
    -File $goExe -Arguments @('test', './...', '-count=1') `
    -WorkDir (Join-Path $repoRoot 'backend')

Invoke-ExitGate -Id 'backend-integration' `
    -Command 'powershell.exe -File scripts/test-integration.ps1 ./... -p 1 -count=1' `
    -File $powershellExe -Arguments @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', 'scripts\test-integration.ps1', './...', '-p', '1', '-count=1') `
    -WorkDir $repoRoot

Invoke-ExitGate -Id 'frontend-test' `
    -Command 'pnpm --dir frontend test' `
    -File $pnpmExe -Arguments @('--dir', 'frontend', 'test') `
    -WorkDir $repoRoot

Invoke-ExitGate -Id 'frontend-typecheck' `
    -Command 'pnpm --dir frontend typecheck' `
    -File $pnpmExe -Arguments @('--dir', 'frontend', 'typecheck') `
    -WorkDir $repoRoot

Invoke-ExitGate -Id 'contract-v021' `
    -Command 'node scripts/v021-contract-check.cjs' `
    -File $nodeExe -Arguments @('scripts\v021-contract-check.cjs') `
    -WorkDir $repoRoot

Invoke-ExitGate -Id 'contract-v022' `
    -Command 'node scripts/v022-contract-check.cjs' `
    -File $nodeExe -Arguments @('scripts\v022-contract-check.cjs') `
    -WorkDir $repoRoot

Invoke-ExitGate -Id 'w0-regression-reuse' `
    -Command 'powershell.exe -File scripts/v022-w0-regression.ps1 -ReuseFlow' `
    -File $powershellExe -Arguments @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', 'scripts\v022-w0-regression.ps1', '-ReuseFlow') `
    -WorkDir $repoRoot

# ---------------------------------------------------------------------------
# 5. Post-run verdict re-read: the regression gate rewrites verdict.json.
# ---------------------------------------------------------------------------
$postVerdictState = [ordered]@{
    flowExit   = $null
    redCount   = $null
    dbRedCount = $null
}
try {
    $postVerdict = Get-Content -LiteralPath $verdictPath -Raw -Encoding UTF8 | ConvertFrom-Json
    $postVerdictState.flowExit = [int]$postVerdict.flowExit
    $postVerdictState.redCount = [int]$postVerdict.redCount
    $postVerdictState.dbRedCount = @($postVerdict.dbChecks.red).Count
    if ($postVerdictState.flowExit -ne 0) {
        $failures.Add("post-run verdict.flowExit=$($postVerdictState.flowExit) (expected 0)")
    }
    if ($postVerdictState.redCount -ne 0) {
        $failures.Add("post-run verdict.redCount=$($postVerdictState.redCount): $(@($postVerdict.red) -join '; ')")
    }
    if ($postVerdictState.dbRedCount -ne 0) {
        $failures.Add("post-run verdict.dbChecks.red has $($postVerdictState.dbRedCount) entr(ies): $(@($postVerdict.dbChecks.red) -join '; ')")
    }
} catch {
    $failures.Add("post-run verdict re-read failed: $($_.Exception.Message)")
}

# ---------------------------------------------------------------------------
# 6. Machine summary.
# ---------------------------------------------------------------------------
$gitHead = ''
try { $gitHead = (& git rev-parse HEAD 2>$null | Out-String).Trim() } catch { }

$summary = @{
    generatedAt        = (Get-Date -Format o)
    gitHead            = $gitHead
    requirements       = 'spec section 11.1 W0 exit items 1-10'
    verdict            = $verdictState
    postRunVerdict     = $postVerdictState
    checks             = @($checks)
    failures           = @($failures)
}
$summary | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath (Join-Path $exitEvidence 'checks.json') -Encoding UTF8

Write-Host ''
Write-Host "== W0 exit checks ($($checks.Count)):"
foreach ($check in $checks) {
    Write-Host ("   {0,-24} exit={1} {2}s" -f $check.id, $check.exitCode, $check.durationSeconds)
}
Write-Host ''
Write-Host "== Failures ($($failures.Count)):"
foreach ($failure in $failures) { Write-Host "   - $failure" }
Write-Host ''
if ($failures.Count -gt 0) {
    Write-Host "W0 EXIT: FAIL (evidence: $(Get-RepoRelativePath $exitEvidence))"
    exit 1
}
Write-Host "W0 EXIT: PASS (evidence: $(Get-RepoRelativePath $exitEvidence))"
exit 0
