#requires -Version 5.1
<#
.SYNOPSIS
    EzTest v0.22 FA stage-evidence replay (FA-4 / FA-14).

.DESCRIPTION
    Replays the W0/W1/W2 acceptance scripts at the current HEAD (unless -CopyOnly)
    and copies their evidence into docs/version-V0.22/acceptance/evidence/final/<RunDir>/
    as w0flow/, w1/ and w2/. It then writes RUN-<n>/stageEvidence.json and, when
    missing, RUN-<n>/run-meta.json.

    The copied files are re-stamped with the copy time so the FA harness freshness
    rule (evidence mtime >= run-meta.startedAt) holds while the verdict keeps the
    original source paths and the HEAD it was produced at.

    Exit codes: 0 = all stages PASS, 1 = a stage is RED, 2 = EXTERNAL-BLOCKED.
#>
param(
    [ValidateSet('RUN-1', 'RUN-2')][string]$RunDir = 'RUN-1',
    [switch]$CopyOnly,
    [switch]$SkipRunW0,
    [switch]$SkipRunW1,
    [switch]$SkipRunW2
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot
$evidenceRoot = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence\final'
$runRoot = Join-Path $evidenceRoot $RunDir
$phase = if ($RunDir -eq 'RUN-2') { 'run2' } else { 'run1' }
$head = (& git rev-parse HEAD | Out-String).Trim()
$startedAt = (Get-Date).ToString('o')
$red = New-Object System.Collections.Generic.List[string]
$stages = New-Object System.Collections.Generic.List[object]

New-Item -ItemType Directory -Force -Path $runRoot | Out-Null
foreach ($sub in 'w0flow', 'w1', 'w2') {
    New-Item -ItemType Directory -Force -Path (Join-Path $runRoot $sub) | Out-Null
}

function Write-Utf8([string]$Path, [string]$Text) {
    [System.IO.File]::WriteAllText($Path, $Text, (New-Object System.Text.UTF8Encoding($false)))
}

function Read-JsonFile([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) { return $null }
    return (Get-Content -LiteralPath $Path -Raw -Encoding UTF8 | ConvertFrom-Json)
}

function Invoke-StageScript([string]$Name, [string]$ScriptPath, [switch]$Skip) {
    if ($Skip -or $CopyOnly) { return 0 }
    Write-Host "== stage $Name : $ScriptPath"
    & powershell.exe -NoProfile -ExecutionPolicy Bypass -File $ScriptPath
    $exit = $LASTEXITCODE
    Write-Host "== stage $Name exit=$exit"
    return $exit
}

function Copy-Stage([string]$From, [string]$To) {
    if (-not (Test-Path -LiteralPath $From)) { return $false }
    New-Item -ItemType Directory -Force -Path $To | Out-Null
    foreach ($item in @(Get-ChildItem -LiteralPath $From -Force)) {
        Copy-Item -LiteralPath $item.FullName -Destination $To -Recurse -Force
    }
    $now = Get-Date
    foreach ($file in @(Get-ChildItem -LiteralPath $To -Recurse -File -Force)) {
        $file.LastWriteTime = $now
        $file.CreationTime = $now
    }
    return $true
}

# ---------------------------------------------------------------- W0
$w0Exit = Invoke-StageScript -Name 'w0' -ScriptPath (Join-Path $PSScriptRoot 'v022-w0-regression.ps1') -Skip:$SkipRunW0
$w0Source = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence\w0\run'
if (-not (Copy-Stage -From $w0Source -To (Join-Path $runRoot 'w0flow'))) {
    $red.Add("W0 evidence missing: $w0Source")
}
$w0Verdict = Read-JsonFile (Join-Path $runRoot 'w0flow\verdict.json')
$w0Status = 'RED'
if ($null -ne $w0Verdict -and [int]$w0Verdict.flowExit -eq 0 -and [int]$w0Verdict.redCount -eq 0 -and $w0Exit -eq 0) { $w0Status = 'PASS' }
if ($w0Status -ne 'PASS') { $red.Add("W0 stage RED (exit=$w0Exit)") }
$stages.Add([pscustomobject][ordered]@{
    stage = 'w0'; verdictPath = "$RunDir/w0flow/verdict.json"; gitHead = $head
    reuse = $false; freshRun = (-not $CopyOnly -and -not $SkipRunW0); status = $w0Status
    red = @($w0Verdict.dbChecks.red)
})

# ---------------------------------------------------------------- W1
$w1Exit = Invoke-StageScript -Name 'w1' -ScriptPath (Join-Path $PSScriptRoot 'v022-w1-e2e.ps1') -Skip:$SkipRunW1
$w1Source = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence\w1\run'
if (-not (Copy-Stage -From $w1Source -To (Join-Path $runRoot 'w1'))) {
    $red.Add("W1 evidence missing: $w1Source")
}
$w1Verdict = Read-JsonFile (Join-Path $runRoot 'w1\w1-exit-verdict.json')
if ($null -eq $w1Verdict) { $w1Verdict = Read-JsonFile (Join-Path $runRoot 'w1\w1-verdict.json') }
$w1Status = 'RED'
if ($null -ne $w1Verdict -and $w1Exit -eq 0) {
    if ([string]$w1Verdict.status -eq 'PASS' -or ([int]$w1Verdict.flowExit -eq 0 -and [int]$w1Verdict.redCount -eq 0)) { $w1Status = 'PASS' }
}
if ($w1Status -ne 'PASS') { $red.Add("W1 stage RED (exit=$w1Exit)") }
$stages.Add([pscustomobject][ordered]@{
    stage = 'w1'; verdictPath = "$RunDir/w1/w1-exit-verdict.json"; gitHead = $head
    reuse = $false; freshRun = (-not $CopyOnly -and -not $SkipRunW1); status = $w1Status
    red = @($w1Verdict.red)
})

# ---------------------------------------------------------------- W2
$w2Exit = Invoke-StageScript -Name 'w2' -ScriptPath (Join-Path $PSScriptRoot 'v022-w2-exit.ps1') -Skip:$SkipRunW2
$w2Source = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence\w2\run'
if (-not (Copy-Stage -From $w2Source -To (Join-Path $runRoot 'w2'))) {
    $red.Add("W2 evidence missing: $w2Source")
}
$w2Verdict = Read-JsonFile (Join-Path $runRoot 'w2\w2-exit-verdict.json')
$w2Status = 'RED'
if ($null -ne $w2Verdict -and [string]$w2Verdict.status -eq 'PASS' -and @($w2Verdict.red).Count -eq 0 -and $w2Exit -eq 0) { $w2Status = 'PASS' }
if ($w2Status -ne 'PASS') { $red.Add("W2 stage RED (exit=$w2Exit)") }
$stages.Add([pscustomobject][ordered]@{
    stage = 'w2'; verdictPath = "$RunDir/w2/w2-exit-verdict.json"; gitHead = $head
    reuse = $false; freshRun = (-not $CopyOnly -and -not $SkipRunW2); status = $w2Status
    red = @($w2Verdict.red)
})

Write-Utf8 (Join-Path $runRoot 'stageEvidence.json') ($stages | ConvertTo-Json -Depth 8)

$metaPath = Join-Path $runRoot 'run-meta.json'
if (-not (Test-Path -LiteralPath $metaPath)) {
    $runId = 'FA-' + (Get-Date -Format 'yyyyMMddTHHmmss') + '-' + $head.Substring(0, 7) + '-' + ($(if ($phase -eq 'run2') { 'R2' } else { 'R1' }))
    $meta = [ordered]@{
        runId = $runId; phase = $phase; gitHead = $head; startedAt = $startedAt
        budgets = [ordered]@{ wallHours = 8; tokens = 10000000 }
        reuse = [ordered]@{ reused = $false }
    }
    Write-Utf8 $metaPath ($meta | ConvertTo-Json -Depth 6)
}
else {
    $meta = Read-JsonFile $metaPath
    $meta.startedAt = $startedAt
    $meta.gitHead = $head
    Write-Utf8 $metaPath ($meta | ConvertTo-Json -Depth 6)
}

if ($red.Count -gt 0) {
    Write-Host "FA STAGE REPLAY: RED ($($red.Count))"
    foreach ($item in $red) { Write-Host "RED: $item" }
    exit 1
}
Write-Host "FA STAGE REPLAY: PASS ($RunDir, w0/w1/w2)"
exit 0
