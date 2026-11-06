#requires -Version 5.1
<#
.SYNOPSIS
    EzTest v0.22 final-acceptance (FA) read-only gate.

.DESCRIPTION
    Validates FA evidence without running the product and without writing any
    evidence. Every check appends a human-readable RED item; the script prints
    all RED items and exits 1 when at least one exists, otherwise it exits 0.

    -Gate inputs (FA-1) validates the isolated input package:
      docs/version-V0.22/acceptance/evidence/final/inputs/
        typical-section1.md  exactly R01-R12 and exactly 41 AC IDs,
                             neutral preface present, no leak markers
        manifest.json        sha256 matches typical-section1.md, counts 12/41,
                             requirementIds/acIds exact sets
        ops-manual.md        exists and documents 13460/6880/13360 + 禁读清单

    Other gates are delivered by FA-2..FA-15 and currently throw
    "gate not implemented yet" (never a false PASS).

    Exit codes: 0 = PASS, 1 = FAIL. FA-3 extends the exit-code contract with
    2 = EXTERNAL-BLOCKED.

.NOTES
    Run from the repository root:
        powershell.exe -NoProfile -ExecutionPolicy Bypass `
            -File scripts\v022-fa-gate.ps1 -Gate inputs
#>

param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('inputs', 'prep', 'flow', 'typical', 'acs', 'issues14',
        'domains', 'exploration', 'visual', 'run1', 'fix', 'verdict', 'report')]
    [string]$Gate,

    [string]$RunDir
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
$OutputEncoding = New-Object System.Text.UTF8Encoding($false)

. (Join-Path $PSScriptRoot 'v022-fa-common.ps1')

if ($Gate -ne 'inputs') {
    throw "gate not implemented yet: $Gate"
}

$inputsDir = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence\final\inputs'
$sectionPath = Join-Path $inputsDir 'typical-section1.md'
$manifestPath = Join-Path $inputsDir 'manifest.json'
$manualPath = Join-Path $inputsDir 'ops-manual.md'

# Frozen expectation for typical-requirements.md section 1: 12 requirements and
# 41 ACs. The per-requirement AC counts are structural facts of the input set,
# not an implementation-status hint; keeping them here means this gate never
# reads typical-requirements.md (and cannot see section 2).
$acCounts = [ordered]@{
    R01 = 3
    R02 = 4
    R03 = 4
    R04 = 4
    R05 = 4
    R06 = 3
    R07 = 3
    R08 = 4
    R09 = 3
    R10 = 3
    R11 = 3
    R12 = 3
}
$expectedRequirementIds = @($acCounts.Keys)
$expectedAcIds = New-Object System.Collections.Generic.List[string]
foreach ($rid in $acCounts.Keys) {
    for ($n = 1; $n -le [int]$acCounts[$rid]; $n++) {
        $expectedAcIds.Add(('AC-{0}-{1:D2}' -f $rid, $n))
    }
}

$red = New-Object System.Collections.Generic.List[string]

$sectionText = $null
$manifestText = $null
$manifest = $null
$manualText = $null

if (-not (Test-Path -LiteralPath $sectionPath)) {
    $red.Add('missing inputs/typical-section1.md')
}
else {
    try {
        $sectionText = [System.IO.File]::ReadAllText(
            (Resolve-Path -LiteralPath $sectionPath).Path,
            [System.Text.Encoding]::UTF8)
    }
    catch {
        $red.Add("typical-section1.md unreadable: $($_.Exception.Message)")
    }
}

if (-not (Test-Path -LiteralPath $manifestPath)) {
    $red.Add('missing inputs/manifest.json')
}
else {
    try {
        $manifestText = [System.IO.File]::ReadAllText(
            (Resolve-Path -LiteralPath $manifestPath).Path,
            [System.Text.Encoding]::UTF8)
        $manifest = $manifestText | ConvertFrom-Json
    }
    catch {
        $red.Add("manifest.json unreadable: $($_.Exception.Message)")
    }
}

if (-not (Test-Path -LiteralPath $manualPath)) {
    $red.Add('missing inputs/ops-manual.md')
}
else {
    try {
        $manualText = [System.IO.File]::ReadAllText(
            (Resolve-Path -LiteralPath $manualPath).Path,
            [System.Text.Encoding]::UTF8)
    }
    catch {
        $red.Add("ops-manual.md unreadable: $($_.Exception.Message)")
    }
}

# --- typical-section1.md content checks -----------------------------------
if ($null -ne $sectionText) {
    if (-not [regex]::IsMatch($sectionText, '(?m)^## 1\. 验收输入集\s*$')) {
        $red.Add('typical-section1.md missing the "## 1. 验收输入集" heading')
    }

    $preface = '本次需求输入共 12 条（R01–R12），共 41 个验收标准（AC）；任一 AC 的 Then/And 未满足即判该 AC 失败并记录 observed/expected。'
    if (-not $sectionText.Contains($preface)) {
        $red.Add('typical-section1.md missing the neutral preface sentence')
    }

    $actualRequirementIds = @(
        [regex]::Matches($sectionText, '(?m)^###\s+(R\d{2})\b') |
            ForEach-Object { $_.Groups[1].Value }
    )
    foreach ($item in @(Assert-ExactSetStrings -Actual $actualRequirementIds -Expected $expectedRequirementIds -Label 'requirementIds')) {
        $red.Add($item)
    }

    $actualAcIds = @(
        [regex]::Matches($sectionText, '(?m)^-\s+\*\*(AC-R\d{2}-\d{2})\b') |
            ForEach-Object { $_.Groups[1].Value }
    )
    foreach ($item in @(Assert-ExactSetStrings -Actual $actualAcIds -Expected @($expectedAcIds) -Label 'acIds')) {
        $red.Add($item)
    }
}

# --- manifest.json checks --------------------------------------------------
if ($null -ne $manifest) {
    if ([string]$manifest.requirements -ne '12') {
        $red.Add("manifest.requirements=$([string]$manifest.requirements) (expected 12)")
    }
    if ([string]$manifest.acs -ne '41') {
        $red.Add("manifest.acs=$([string]$manifest.acs) (expected 41)")
    }

    $manifestRequirementIds = @($manifest.requirementIds)
    foreach ($item in @(Assert-ExactSetStrings -Actual $manifestRequirementIds -Expected $expectedRequirementIds -Label 'manifest.requirementIds')) {
        $red.Add($item)
    }

    $manifestAcIds = @($manifest.acIds)
    foreach ($item in @(Assert-ExactSetStrings -Actual $manifestAcIds -Expected @($expectedAcIds) -Label 'manifest.acIds')) {
        $red.Add($item)
    }

    $manifestSha = [string]$manifest.sha256
    if ([string]::IsNullOrWhiteSpace($manifestSha)) {
        $red.Add('manifest.sha256 missing')
    }
    elseif (Test-Path -LiteralPath $sectionPath) {
        $fileSha = Get-Sha -Path $sectionPath
        if ($manifestSha.ToLowerInvariant() -ne $fileSha) {
            $red.Add("manifest.sha256 does not match typical-section1.md (manifest=$manifestSha file=$fileSha)")
        }
    }
}

# --- ops-manual.md checks --------------------------------------------------
if ($null -ne $manualText) {
    foreach ($needle in @('13460', '6880', '13360', '禁读清单')) {
        if (-not $manualText.Contains($needle)) {
            $red.Add("ops-manual.md missing required text: $needle")
        }
    }
}

# --- leak scan over every input file --------------------------------------
$forbiddenPattern = '第\s*2\s*节|第2节|9\s*已实现|3\s*未实现|内部对照|预期未实现|trap'
$scanTargets = @(
    @{ Name = 'typical-section1.md'; Text = $sectionText },
    @{ Name = 'manifest.json'; Text = $manifestText },
    @{ Name = 'ops-manual.md'; Text = $manualText }
)
foreach ($target in $scanTargets) {
    $text = [string]$target.Text
    if ([string]::IsNullOrEmpty($text)) { continue }
    $hits = @(
        [regex]::Matches($text, $forbiddenPattern,
            [System.Text.RegularExpressions.RegexOptions]::IgnoreCase) |
            ForEach-Object { $_.Value } |
            Sort-Object -Unique
    )
    foreach ($hit in $hits) {
        $red.Add("forbidden term in $($target.Name): $hit")
    }
}

# --- verdict ---------------------------------------------------------------
if ($red.Count -gt 0) {
    foreach ($item in $red) {
        Write-Host ("RED: " + $item)
    }
    Write-Host ("FAIL: gate inputs ($($red.Count) red)")
    exit 1
}

Write-Host 'PASS: gate inputs (12 requirements / 41 ACs, sha256 bound, no leak markers)'
exit 0
