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

# ---------------------------------------------------------------------------
# Gate prep (FA-2): read-only validation of the fresh-stack evidence. The gate
# never starts/stops services and never writes evidence; it validates the
# producer's verdict fields plus the frozen input/issue SHAs.
# ---------------------------------------------------------------------------
if ($Gate -eq 'prep') {
    $prepDir = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence\final\prep'
    $prepVerdictPath = Join-Path $prepDir 'prep-verdict.json'
    $prepCheckDir = Join-Path $prepDir 'checks'
    $prepShotDir = Join-Path $prepDir 'screenshots'

    $red = New-Object System.Collections.Generic.List[string]
    $verdict = $null

    if (-not (Test-Path -LiteralPath $prepVerdictPath)) {
        $red.Add('missing prep/prep-verdict.json')
    }
    else {
        try {
            $verdict = Read-JsonUtf8 -Path $prepVerdictPath
        }
        catch {
            $red.Add("prep-verdict.json unreadable: $($_.Exception.Message)")
        }
    }

    if ($null -ne $verdict) {
        $prepStatus = [string]$verdict.status
        if ([string]::IsNullOrWhiteSpace($prepStatus)) {
            $red.Add('prep-verdict.status missing')
        }
        elseif ($prepStatus -eq 'EXTERNAL-BLOCKED') {
            $blockedReason = [string]$verdict.blockedReason
            if ([string]::IsNullOrWhiteSpace($blockedReason)) {
                $blockedReason = 'prep-verdict.status=EXTERNAL-BLOCKED without a reason'
            }
            Write-Host ("EXTERNAL-BLOCKED: " + $blockedReason)
            Write-Host 'BLOCKED: gate prep (external dependency unavailable; no PASS claimed)'
            exit 2
        }
        elseif ($prepStatus -ne 'PASS') {
            $red.Add("prep-verdict.status=$prepStatus (expected PASS or EXTERNAL-BLOCKED)")
        }

        if ([int]$verdict.schemaVersion -ne 1) {
            $red.Add("prep-verdict.schemaVersion=$([string]$verdict.schemaVersion) (expected 1)")
        }

        $gitHead = [string]$verdict.gitHead
        if ($gitHead -notmatch '^[0-9a-f]{40}$') {
            $red.Add("prep-verdict.gitHead is not a 40-char lowercase SHA: '$gitHead'")
        }
        else {
            $currentHead = Get-Head
            if ($gitHead -ne $currentHead) {
                $red.Add("prep-verdict.gitHead=$gitHead does not match git HEAD=$currentHead")
            }
        }

        # --- checks[]: at least 8, all ok, required probe names present ----
        $checks = @($verdict.checks)
        if ($checks.Count -lt 8) {
            $red.Add("prep-verdict.checks=$($checks.Count) (need >= 8)")
        }
        $checkNames = New-Object System.Collections.Generic.List[string]
        foreach ($check in $checks) {
            $name = [string]$check.name
            if ([string]::IsNullOrWhiteSpace($name)) {
                $red.Add('prep-verdict.checks[] entry without a name')
                continue
            }
            $checkNames.Add($name)
            if (-not [bool]$check.ok) {
                $detail = [string]$check.detail
                $red.Add("prep check not ok: $name ($detail)")
            }
        }
        $requiredPrepChecks = @(
            'db.eztest', 'db.sut', 'api.ready', 'executor.health', 'executor.auth',
            'sut.health', 'sut.posts', 'sut.uploads', 'sut.web',
            'vision.upstream', 'vision.catalog', 'screenshots'
        )
        foreach ($required in $requiredPrepChecks) {
            if (@($checkNames) -notcontains $required) {
                $red.Add("missing prep check: $required")
            }
        }

        # --- db / executor / sut / vision / stack fields -------------------
        if (-not [bool]$verdict.db.eztestSelect1) { $red.Add('db.eztestSelect1 is not true') }
        if (-not [bool]$verdict.db.sutSelect1) { $red.Add('db.sutSelect1 is not true') }
        if (-not [bool]$verdict.db.uploads) { $red.Add('db.uploads is not true') }

        $executorUrl = [string]$verdict.executor.url
        if ($executorUrl -ne 'http://127.0.0.1:13360') {
            $red.Add("executor.url='$executorUrl' (expected http://127.0.0.1:13360)")
        }
        if ([int]$verdict.executor.httpStatus -ne 200) {
            $red.Add("executor.httpStatus=$([string]$verdict.executor.httpStatus) (expected 200)")
        }
        if (-not [bool]$verdict.executor.auth.bearer200) {
            $red.Add('executor auth: Bearer GET /v1/contexts did not return 200')
        }
        if (-not [bool]$verdict.executor.auth.noToken401) {
            $red.Add('executor auth: GET /v1/contexts without a token did not return 401')
        }

        $visionModel = [string]$verdict.vision.model
        $visionBaseUrl = [string]$verdict.vision.baseUrl
        $visionCaption = [string]$verdict.vision.caption
        if ([string]::IsNullOrWhiteSpace($visionModel)) { $red.Add('vision.model missing') }
        if ([string]::IsNullOrWhiteSpace($visionBaseUrl)) {
            $red.Add('vision.baseUrl missing')
        }
        elseif ($visionBaseUrl -match '^(https?://)?(localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1\])(:\d+)?(/|$)') {
            $red.Add("vision.baseUrl points at a local proxy, not the real upstream: $visionBaseUrl")
        }
        if (-not [bool]$verdict.vision.visionExercised) {
            $red.Add('vision.visionExercised is not true')
        }
        if ([string]::IsNullOrWhiteSpace($visionCaption)) {
            $red.Add('vision.caption is empty (no real image caption)')
        }
        if (-not [bool]$verdict.vision.catalogSupportsVision) {
            $red.Add('vision.catalogSupportsVision is not true')
        }
        if (-not [bool]$verdict.vision.catalogDefault) {
            $red.Add('vision.catalogDefault is not true')
        }

        if ([string]$verdict.stack.db -ne 'eztest_v022_fa') {
            $red.Add("stack.db=$([string]$verdict.stack.db) (expected eztest_v022_fa)")
        }
        if ([int]$verdict.stack.apiPort -ne 13460) { $red.Add("stack.apiPort=$([string]$verdict.stack.apiPort) (expected 13460)") }
        if ([int]$verdict.stack.webPort -ne 6880) { $red.Add("stack.webPort=$([string]$verdict.stack.webPort) (expected 6880)") }
        if ([int]$verdict.stack.executorPort -ne 13360) { $red.Add("stack.executorPort=$([string]$verdict.stack.executorPort) (expected 13360)") }
        foreach ($processName in @('api', 'web', 'executor')) {
            if ([int]$verdict.stack.pids.$processName -le 0) {
                $red.Add("stack.pids.$processName missing or not positive")
            }
        }

        # --- frozen SHAs ---------------------------------------------------
        # The input set and the issue ledger must not drift. The gate script
        # itself evolves in FA-3..FA-15, so its frozen hash is recorded for the
        # audit trail but is not re-compared here.
        $inputsSection = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence\final\inputs\typical-section1.md'
        $issuesMd = Join-Path $repoRoot 'docs\version-V0.22\acceptance\issues.md'
        $frozenInputs = [string]$verdict.frozenShas.inputs
        $frozenGate = [string]$verdict.frozenShas.gate
        $frozenIssues = [string]$verdict.frozenShas.issuesMd
        if ($frozenInputs -notmatch '^[0-9a-f]{64}$') {
            $red.Add('frozenShas.inputs is not a 64-char lowercase SHA256')
        }
        elseif (-not (Test-Path -LiteralPath $inputsSection)) {
            $red.Add('missing inputs/typical-section1.md (cannot verify frozenShas.inputs)')
        }
        else {
            $currentInputs = Get-Sha -Path $inputsSection
            if ($frozenInputs -ne $currentInputs) {
                $red.Add("frozenShas.inputs=$frozenInputs does not match current typical-section1.md=$currentInputs")
            }
        }
        if ($frozenGate -notmatch '^[0-9a-f]{64}$') {
            $red.Add('frozenShas.gate is not a 64-char lowercase SHA256')
        }
        if ($frozenIssues -notmatch '^[0-9a-f]{64}$') {
            $red.Add('frozenShas.issuesMd is not a 64-char lowercase SHA256')
        }
        elseif (-not (Test-Path -LiteralPath $issuesMd)) {
            $red.Add('missing docs/version-V0.22/acceptance/issues.md (cannot verify frozenShas.issuesMd)')
        }
        else {
            $currentIssues = Get-Sha -Path $issuesMd
            if ($frozenIssues -ne $currentIssues) {
                $red.Add("frozenShas.issuesMd=$frozenIssues does not match current issues.md=$currentIssues")
            }
        }

        # --- raw check payloads and screenshots ----------------------------
        if (-not (Test-Path -LiteralPath $prepCheckDir)) {
            $red.Add('missing prep/checks/ (raw responses)')
        }
        else {
            $checkFiles = @(Get-ChildItem -LiteralPath $prepCheckDir -Filter '*.json' -File -ErrorAction SilentlyContinue)
            if ($checkFiles.Count -lt 8) {
                $red.Add("prep/checks/*.json=$($checkFiles.Count) (need >= 8 raw responses)")
            }
        }
        foreach ($shotName in @('versions.png', 'settings.png')) {
            $shotPath = Join-Path $prepShotDir $shotName
            foreach ($item in @(Assert-Png -Path $shotPath)) { $red.Add($item) }
        }
    }

    if ($red.Count -gt 0) {
        foreach ($item in $red) {
            Write-Host ("RED: " + $item)
        }
        Write-Host ("FAIL: gate prep ($($red.Count) red)")
        exit 1
    }

    Write-Host ("PASS: gate prep (fresh eztest_v022_fa stack, 12 checks, real vision caption, frozen inputs)")
    exit 0
}

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

    # Section 2 of typical-requirements.md is the internal implementation-status
    # table and must never appear in the isolated input set (spec §2.3 / FA-C6).
    if ([regex]::IsMatch($sectionText, '(?m)^##\s+2\.')) {
        $red.Add('typical-section1.md contains a "## 2." heading (section 2 must not be copied)')
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
    if ([string]::IsNullOrWhiteSpace([string]$manifest.source)) {
        $red.Add('manifest.source missing')
    }
    elseif ([string]$manifest.source -ne 'docs/version-V0.22/acceptance/typical-requirements.md') {
        $red.Add("manifest.source=$([string]$manifest.source) (expected docs/version-V0.22/acceptance/typical-requirements.md)")
    }
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
