#requires -Version 5.1
<#
.SYNOPSIS
    EzTest v0.22 final-acceptance (FA) read-only gate.

.DESCRIPTION
    Validates FA evidence without running the product and without writing any
    evidence or touching git history. Every check appends a human-readable RED
    item; the script prints all RED items and exits 1 when at least one exists,
    otherwise it exits 0. A missing external dependency may be recorded as
    EXTERNAL-BLOCKED, which exits 2.

    Gates:
      inputs      FA-1 isolated input package (12 requirements / 41 ACs)
      prep        FA-2 fresh stack + real vision evidence
      harness     RUN-x/run-meta.json (runId/phase/gitHead/startedAt/reuse)
      flow        RUN-x/w0flow/result.json (full-flow stages, DB, model)
      typical     RUN-x/typical/eztest.json (12 requirements, cases, runs)
      acs         RUN-x/acs-*.json + RUN-x/probes/*.json (41 or 22/19 ACs)
      issues14    RUN-x/issues14.json (exact 14 rows, fresh W0 PASS)
      domains     RUN-x/w2/w2-exit-verdict.json + 7 domain verdicts
      exploration RUN-x/exploration/ref.json (current-wave invariance)
      visual      RUN-x/visual/review.json (8 stages, real vision, PNGs)
      run1        run/p0.json + issues.md (P0/P1 ledger)
      fix         RUN-x/fixes/<ISSUE-ID>/** (red/green/reprobe/commit)
      verdict     run/fa-verdict.json (schema v1, sets, sha256, stage reuse)
      report      FINAL-REPORT.md + INDEX.md

    Exit codes: 0 = PASS, 1 = FAIL, 2 = EXTERNAL-BLOCKED.

.NOTES
    Run from the repository root:
        powershell.exe -NoProfile -ExecutionPolicy Bypass `
            -File scripts\v022-fa-gate.ps1 -Gate flow -RunDir RUN-2
#>

param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('inputs', 'prep', 'harness', 'flow', 'typical', 'acs', 'issues14',
        'domains', 'exploration', 'visual', 'run1', 'fix', 'verdict', 'report')]
    [string]$Gate,

    [ValidateSet('RUN-1', 'RUN-2')]
    [string]$RunDir = 'RUN-2',

    [ValidateSet('R01-R06', 'R07-R12', 'all')]
    [string]$Range = 'all',

    [string]$IssueId,

    [string]$EvidenceRoot,

    [string]$IssuesPath,

    [string]$ReportPath
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
$OutputEncoding = New-Object System.Text.UTF8Encoding($false)

. (Join-Path $PSScriptRoot 'v022-fa-common.ps1')

# ---------------------------------------------------------------------------
# Evidence root / repository state. All gates are read-only.
# ---------------------------------------------------------------------------
if ([string]::IsNullOrWhiteSpace($EvidenceRoot)) {
    $script:EvidenceRoot = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence\final'
}
elseif ([System.IO.Path]::IsPathRooted($EvidenceRoot)) {
    $script:EvidenceRoot = [System.IO.Path]::GetFullPath($EvidenceRoot)
}
else {
    $script:EvidenceRoot = [System.IO.Path]::GetFullPath((Join-Path $repoRoot $EvidenceRoot))
}
$script:RunDir = $RunDir
$script:Range = $Range
$script:Head = ''
try { $script:Head = Get-Head } catch { $script:Head = '' }

$script:Red = New-Object System.Collections.Generic.List[string]
$script:Warnings = New-Object System.Collections.Generic.List[string]
$script:Blocked = New-Object System.Collections.Generic.List[string]
$script:PassSuffix = ''

function Add-Red([string]$Message) { $script:Red.Add($Message) }
function Add-Warn([string]$Message) { $script:Warnings.Add($Message) }
function Add-Blocked([string]$Message) { $script:Blocked.Add($Message) }

function Complete-Gate {
    param([string]$Name, [string]$PassMessage)
    foreach ($warning in $script:Warnings) { Write-Host ("WARN: " + $warning) }
    if ($script:Blocked.Count -gt 0) {
        foreach ($item in $script:Blocked) { Write-Host ("EXTERNAL-BLOCKED: " + $item) }
        Write-Host ("BLOCKED: gate $Name ($($script:Blocked.Count) external)")
        exit 2
    }
    if ($script:Red.Count -gt 0) {
        foreach ($item in $script:Red) { Write-Host ("RED: " + $item) }
        Write-Host ("FAIL: gate $Name ($($script:Red.Count) red)")
        exit 1
    }
    $suffix = if (-not [string]::IsNullOrWhiteSpace($script:PassSuffix)) { $script:PassSuffix } else { $PassMessage }
    if ([string]::IsNullOrWhiteSpace($suffix)) {
        Write-Host ("PASS: gate " + $Name)
    }
    else {
        Write-Host ("PASS: gate $Name ($suffix)")
    }
    exit 0
}

# ---------------------------------------------------------------------------
# Small structural helpers.
# ---------------------------------------------------------------------------
function Get-Value {
    param($Object, [string[]]$Names, $Default = $null)
    if ($null -eq $Object) { return $Default }
    foreach ($name in $Names) {
        if ($Object -is [System.Collections.IDictionary]) {
            if ($Object.Contains($name)) { return $Object[$name] }
            continue
        }
        $property = $Object.PSObject.Properties[$name]
        if ($null -ne $property -and $null -ne $property.Value) { return $property.Value }
    }
    return $Default
}

function Get-Iarray($Value) {
    if ($null -eq $Value) { return @() }
    if ($Value -is [System.Array]) { return $Value }
    return @($Value)
}

function Get-StringValue($Object, [string[]]$Names, [string]$Default = '') {
    $value = Get-Value $Object $Names $null
    if ($null -eq $value) { return $Default }
    return [string]$value
}

function Get-IntValue($Object, [string[]]$Names, [int]$Default = -1) {
    $value = Get-Value $Object $Names $null
    if ($null -eq $value) { return $Default }
    $parsed = 0
    if ([int]::TryParse([string]$value, [ref]$parsed)) { return $parsed }
    return $Default
}

function Resolve-FaPath {
    param([string]$Path, [string]$BaseDir = '')
    if ([string]::IsNullOrWhiteSpace($Path)) { return $null }
    $normalized = $Path -replace '/', '\'
    $candidates = New-Object System.Collections.Generic.List[string]
    if ([System.IO.Path]::IsPathRooted($normalized)) {
        $candidates.Add($normalized)
    }
    else {
        $candidates.Add((Join-Path $script:EvidenceRoot $normalized))
        $candidates.Add((Join-Path (Join-Path $script:EvidenceRoot $script:RunDir) $normalized))
        if (-not [string]::IsNullOrWhiteSpace($BaseDir)) { $candidates.Add((Join-Path $BaseDir $normalized)) }
        $candidates.Add((Join-Path $repoRoot $normalized))
    }
    foreach ($candidate in $candidates) {
        if (Test-Path -LiteralPath $candidate) {
            try { return (Resolve-Path -LiteralPath $candidate).Path } catch { return [System.IO.Path]::GetFullPath($candidate) }
        }
    }
    return $null
}

function Read-JsonOrRed {
    param([string]$Path, [string]$Label)
    if ([string]::IsNullOrWhiteSpace($Path) -or -not (Test-Path -LiteralPath $Path)) {
        Add-Red "${Label} missing: $Path"
        return $null
    }
    try { return (Read-JsonUtf8 -Path $Path) }
    catch {
        Add-Red "${Label} unreadable: $($_.Exception.Message)"
        return $null
    }
}

function Get-EvidenceJson {
    param([string]$RelativePath, [string]$Label)
    $resolved = Resolve-FaPath $RelativePath
    if ($null -eq $resolved) {
        Add-Red "${Label} missing: $RelativePath"
        return $null
    }
    return Read-JsonOrRed $resolved $Label
}

function Assert-FaFile {
    param([string]$Path, [string]$Label, [string]$BaseDir = '', [switch]$AllowEmpty)
    if ([string]::IsNullOrWhiteSpace($Path)) {
        Add-Red "${Label} path missing"
        return $null
    }
    $resolved = Resolve-FaPath $Path $BaseDir
    if ($null -eq $resolved) {
        Add-Red "${Label} missing: $Path"
        return $null
    }
    $length = (Get-Item -LiteralPath $resolved).Length
    if (-not $AllowEmpty -and $length -le 0) {
        Add-Red "${Label} empty: $Path"
        return $null
    }
    return $resolved
}

function Assert-Screenshots {
    param($Paths, [string]$Label, [string]$BaseDir = '')
    $items = @(Get-Iarray $Paths)
    if ($items.Count -eq 0) {
        Add-Red "${Label} has no screenshots"
        return
    }
    foreach ($item in $items) {
        $resolved = Assert-FaFile ([string]$item) "${Label} screenshot" $BaseDir
        if ($null -ne $resolved) {
            foreach ($problem in @(Assert-Png -Path $resolved)) { Add-Red "${Label} $problem" }
        }
    }
}

function Assert-EvidenceFileList {
    param($Paths, [string]$Label, [string]$BaseDir = '')
    $items = @(Get-Iarray $Paths)
    if ($items.Count -eq 0) {
        Add-Red "${Label} has no evidence files"
        return
    }
    foreach ($item in $items) {
        [void](Assert-FaFile ([string]$item) "${Label} evidence" $BaseDir)
    }
}

function Assert-FreshRun {
    param([string]$Directory, [datetime]$StartedAt)
    if (-not (Test-Path -LiteralPath $Directory)) {
        Add-Red "run directory missing: $Directory"
        return
    }
    $stale = New-Object System.Collections.Generic.List[string]
    foreach ($file in @(Get-ChildItem -LiteralPath $Directory -Recurse -File -ErrorAction SilentlyContinue)) {
        if ($file.LastWriteTime.ToUniversalTime() -lt $StartedAt.ToUniversalTime()) {
            $stale.Add($file.FullName.Substring($repoRoot.Length).TrimStart('\'))
        }
    }
    if ($stale.Count -gt 0) {
        $shown = @($stale | Select-Object -First 5)
        $suffix = if ($stale.Count -gt 5) { " (+$($stale.Count - 5) more)" } else { '' }
        Add-Red ("evidence older than startedAt: " + ($shown -join ', ') + $suffix)
    }
}

function Invoke-GitCapture {
    param([string[]]$GitArgs)
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $raw = & git -C $repoRoot @GitArgs 2>&1
        $code = $LASTEXITCODE
    }
    finally { $ErrorActionPreference = $previous }
    return [pscustomobject]@{
        Exit = $code
        Output = @($raw | ForEach-Object { [string]$_ })
    }
}

function Assert-StageReuse {
    param([string]$VerdictHead, [string]$Label)
    if ($VerdictHead -notmatch '^[0-9a-f]{40}$') {
        Add-Red "${Label} gitHead is not a 40-char lowercase SHA: '$VerdictHead'"
        return $false
    }
    if ($VerdictHead -eq $script:Head) { return $true }
    if ([string]::IsNullOrWhiteSpace($script:Head)) {
        Add-Red "${Label} cannot compare gitHead because git HEAD is unavailable"
        return $false
    }
    $diff = Invoke-GitCapture @('diff', '--stat', "$VerdictHead..$($script:Head)", '--', 'backend', 'frontend')
    if ($diff.Exit -ne 0) {
        Add-Red "${Label} git diff failed for $VerdictHead..$($script:Head)"
        return $false
    }
    $text = ($diff.Output -join "`n").Trim()
    if ($text.Length -gt 0) {
        Add-Red "${Label} gitHead=$VerdictHead is stale: backend/frontend differ from HEAD=$($script:Head); fresh replay required"
        return $false
    }
    return $true
}

function Assert-StageEvidence {
    param($Entries, [string]$Label)
    $items = @(Get-Iarray $Entries)
    if ($items.Count -eq 0) {
        Add-Red "${Label} stageEvidence is empty"
        return
    }
    $stageIds = New-Object System.Collections.Generic.List[string]
    foreach ($entry in $items) {
        $stage = Get-StringValue $entry @('stage')
        if ([string]::IsNullOrWhiteSpace($stage)) {
            Add-Red "${Label} stageEvidence entry without a stage"
            continue
        }
        $stageIds.Add($stage)
        $status = Get-StringValue $entry @('status')
        if ($status -ne 'PASS') { Add-Red "${Label} stage $stage status='$status' (expected PASS)" }
        if (@(Get-Iarray (Get-Value $entry @('red'))).Count -gt 0) { Add-Red "${Label} stage $stage red is not empty" }
        $verdictPath = Get-StringValue $entry @('verdictPath')
        if ([string]::IsNullOrWhiteSpace($verdictPath)) {
            Add-Red "${Label} stage $stage verdictPath missing"
        }
        else {
            [void](Assert-FaFile $verdictPath "${Label} stage $stage verdictPath")
        }
        $gitHead = Get-StringValue $entry @('gitHead')
        if ($gitHead -notmatch '^[0-9a-f]{40}$') { Add-Red "${Label} stage $stage gitHead invalid: '$gitHead'" }
        $reuse = [bool](Get-Value $entry @('reuse') $false)
        $fresh = [bool](Get-Value $entry @('freshRun') $false)
        if ($reuse) {
            if ($fresh) { Add-Red "${Label} stage $stage reuse=true but freshRun=true" }
            [void](Assert-StageReuse $gitHead "${Label} stage $stage")
        }
        else {
            if (-not $fresh) { Add-Red "${Label} stage $stage reuse=false but freshRun=false" }
            if ($gitHead -and $gitHead -ne $script:Head) {
                Add-Red "${Label} stage $stage freshRun=true but gitHead=$gitHead != HEAD=$($script:Head)"
            }
        }
    }
    foreach ($problem in @(Assert-ExactSetStrings -Actual @($stageIds) -Expected @('w0', 'w1', 'w2') -Label "${Label} stageEvidence.stage")) {
        Add-Red $problem
    }
}

# ---------------------------------------------------------------------------
# Frozen input-set structure: 12 requirements / 41 ACs.
# ---------------------------------------------------------------------------
$script:AcCounts = [ordered]@{
    R01 = 3; R02 = 4; R03 = 4; R04 = 4; R05 = 4; R06 = 3
    R07 = 3; R08 = 4; R09 = 3; R10 = 3; R11 = 3; R12 = 3
}
$script:ExpectedRequirementIds = @($script:AcCounts.Keys)
$script:ExpectedAcIds = New-Object System.Collections.Generic.List[string]
foreach ($rid in $script:AcCounts.Keys) {
    for ($n = 1; $n -le [int]$script:AcCounts[$rid]; $n++) {
        $script:ExpectedAcIds.Add(('AC-{0}-{1:D2}' -f $rid, $n))
    }
}

function Get-ExpectedRequirementIdsForRange {
    param([string]$RangeName)
    if ($RangeName -eq 'R01-R06') { return @('R01', 'R02', 'R03', 'R04', 'R05', 'R06') }
    if ($RangeName -eq 'R07-R12') { return @('R07', 'R08', 'R09', 'R10', 'R11', 'R12') }
    return @($script:ExpectedRequirementIds)
}

function Get-ExpectedAcIdsForRange {
    param([string]$RangeName)
    $requirementIds = @(Get-ExpectedRequirementIdsForRange $RangeName)
    $result = New-Object System.Collections.Generic.List[string]
    foreach ($rid in $script:AcCounts.Keys) {
        if ($requirementIds -notcontains $rid) { continue }
        for ($n = 1; $n -le [int]$script:AcCounts[$rid]; $n++) {
            $result.Add(('AC-{0}-{1:D2}' -f $rid, $n))
        }
    }
    return @($result)
}

# ===========================================================================
# Gate: inputs (FA-1)
# ===========================================================================
function Invoke-GateInputs {
    $inputsDir = Join-Path $script:EvidenceRoot 'inputs'
    $sectionPath = Join-Path $inputsDir 'typical-section1.md'
    $manifestPath = Join-Path $inputsDir 'manifest.json'
    $manualPath = Join-Path $inputsDir 'ops-manual.md'

    $sectionText = $null
    $manifestText = $null
    $manifest = $null
    $manualText = $null

    if (-not (Test-Path -LiteralPath $sectionPath)) { Add-Red 'missing inputs/typical-section1.md' }
    else {
        try {
            $sectionText = [System.IO.File]::ReadAllText((Resolve-Path -LiteralPath $sectionPath).Path, [System.Text.Encoding]::UTF8)
        }
        catch { Add-Red "typical-section1.md unreadable: $($_.Exception.Message)" }
    }

    if (-not (Test-Path -LiteralPath $manifestPath)) { Add-Red 'missing inputs/manifest.json' }
    else {
        try {
            $manifestText = [System.IO.File]::ReadAllText((Resolve-Path -LiteralPath $manifestPath).Path, [System.Text.Encoding]::UTF8)
            $manifest = $manifestText | ConvertFrom-Json
        }
        catch { Add-Red "manifest.json unreadable: $($_.Exception.Message)" }
    }

    if (-not (Test-Path -LiteralPath $manualPath)) { Add-Red 'missing inputs/ops-manual.md' }
    else {
        try {
            $manualText = [System.IO.File]::ReadAllText((Resolve-Path -LiteralPath $manualPath).Path, [System.Text.Encoding]::UTF8)
        }
        catch { Add-Red "ops-manual.md unreadable: $($_.Exception.Message)" }
    }

    if ($null -ne $sectionText) {
        if (-not [regex]::IsMatch($sectionText, '(?m)^## 1\. 验收输入集\s*$')) {
            Add-Red 'typical-section1.md missing the "## 1. 验收输入集" heading'
        }
        if ([regex]::IsMatch($sectionText, '(?m)^##\s+2\.')) {
            Add-Red 'typical-section1.md contains a "## 2." heading (section 2 must not be copied)'
        }
        $preface = '本次需求输入共 12 条（R01–R12），共 41 个验收标准（AC）；任一 AC 的 Then/And 未满足即判该 AC 失败并记录 observed/expected。'
        if (-not $sectionText.Contains($preface)) { Add-Red 'typical-section1.md missing the neutral preface sentence' }

        $actualRequirementIds = @([regex]::Matches($sectionText, '(?m)^###\s+(R\d{2})\b') | ForEach-Object { $_.Groups[1].Value })
        foreach ($problem in @(Assert-ExactSetStrings -Actual $actualRequirementIds -Expected $script:ExpectedRequirementIds -Label 'requirementIds')) { Add-Red $problem }

        $actualAcIds = @([regex]::Matches($sectionText, '(?m)^-\s+\*\*(AC-R\d{2}-\d{2})\b') | ForEach-Object { $_.Groups[1].Value })
        foreach ($problem in @(Assert-ExactSetStrings -Actual $actualAcIds -Expected @($script:ExpectedAcIds) -Label 'acIds')) { Add-Red $problem }
    }

    if ($null -ne $manifest) {
        if ([string]$manifest.requirements -ne '12') { Add-Red "manifest.requirements=$([string]$manifest.requirements) (expected 12)" }
        if ([string]$manifest.acs -ne '41') { Add-Red "manifest.acs=$([string]$manifest.acs) (expected 41)" }
        foreach ($problem in @(Assert-ExactSetStrings -Actual @($manifest.requirementIds) -Expected $script:ExpectedRequirementIds -Label 'manifest.requirementIds')) { Add-Red $problem }
        foreach ($problem in @(Assert-ExactSetStrings -Actual @($manifest.acIds) -Expected @($script:ExpectedAcIds) -Label 'manifest.acIds')) { Add-Red $problem }
        $manifestSha = [string]$manifest.sha256
        if ([string]::IsNullOrWhiteSpace([string]$manifest.source)) { Add-Red 'manifest.source missing' }
        elseif ([string]$manifest.source -ne 'docs/version-V0.22/acceptance/typical-requirements.md') {
            Add-Red "manifest.source=$([string]$manifest.source) (expected docs/version-V0.22/acceptance/typical-requirements.md)"
        }
        if ([string]::IsNullOrWhiteSpace($manifestSha)) { Add-Red 'manifest.sha256 missing' }
        elseif (Test-Path -LiteralPath $sectionPath) {
            $fileSha = Get-Sha -Path $sectionPath
            if ($manifestSha.ToLowerInvariant() -ne $fileSha) {
                Add-Red "manifest.sha256 does not match typical-section1.md (manifest=$manifestSha file=$fileSha)"
            }
        }
    }

    if ($null -ne $manualText) {
        foreach ($needle in @('13460', '6880', '13360', '禁读清单')) {
            if (-not $manualText.Contains($needle)) { Add-Red "ops-manual.md missing required text: $needle" }
        }
    }

    $forbiddenPattern = '第\s*2\s*节|第2节|9\s*已实现|3\s*未实现|内部对照|预期未实现|trap'
    $scanTargets = @(
        @{ Name = 'typical-section1.md'; Text = $sectionText },
        @{ Name = 'manifest.json'; Text = $manifestText },
        @{ Name = 'ops-manual.md'; Text = $manualText }
    )
    foreach ($target in $scanTargets) {
        $text = [string]$target.Text
        if ([string]::IsNullOrEmpty($text)) { continue }
        $hits = @([regex]::Matches($text, $forbiddenPattern, [System.Text.RegularExpressions.RegexOptions]::IgnoreCase) |
                ForEach-Object { $_.Value } | Sort-Object -Unique)
        foreach ($hit in $hits) { Add-Red "forbidden term in $($target.Name): $hit" }
    }
}

# ===========================================================================
# Gate: prep (FA-2)
# ===========================================================================
function Invoke-GatePrep {
    $prepDir = Join-Path $script:EvidenceRoot 'prep'
    $prepVerdictPath = Join-Path $prepDir 'prep-verdict.json'
    $prepCheckDir = Join-Path $prepDir 'checks'
    $prepShotDir = Join-Path $prepDir 'screenshots'

    $verdict = $null
    if (-not (Test-Path -LiteralPath $prepVerdictPath)) { Add-Red 'missing prep/prep-verdict.json' }
    else {
        try { $verdict = Read-JsonUtf8 -Path $prepVerdictPath }
        catch { Add-Red "prep-verdict.json unreadable: $($_.Exception.Message)" }
    }

    if ($null -ne $verdict) {
        $prepStatus = [string]$verdict.status
        if ([string]::IsNullOrWhiteSpace($prepStatus)) {
            Add-Red 'prep-verdict.status missing'
        }
        elseif ($prepStatus -eq 'EXTERNAL-BLOCKED') {
            $blockedReason = [string]$verdict.blockedReason
            if ([string]::IsNullOrWhiteSpace($blockedReason)) { $blockedReason = 'prep-verdict.status=EXTERNAL-BLOCKED without a reason' }
            Add-Blocked $blockedReason
            return
        }
        elseif ($prepStatus -ne 'PASS') {
            Add-Red "prep-verdict.status=$prepStatus (expected PASS or EXTERNAL-BLOCKED)"
        }

        if ([int]$verdict.schemaVersion -ne 1) { Add-Red "prep-verdict.schemaVersion=$([string]$verdict.schemaVersion) (expected 1)" }

        $gitHead = [string]$verdict.gitHead
        if ($gitHead -notmatch '^[0-9a-f]{40}$') { Add-Red "prep-verdict.gitHead is not a 40-char lowercase SHA: '$gitHead'" }
        else {
            $currentHead = $script:Head
            if ([string]::IsNullOrWhiteSpace($currentHead)) { Add-Red 'cannot resolve git HEAD for prep verdict' }
            elseif ($gitHead -ne $currentHead) { Add-Red "prep-verdict.gitHead=$gitHead does not match git HEAD=$currentHead" }
        }

        $checks = @($verdict.checks)
        if ($checks.Count -lt 8) { Add-Red "prep-verdict.checks=$($checks.Count) (need >= 8)" }
        $checkNames = New-Object System.Collections.Generic.List[string]
        foreach ($check in $checks) {
            $name = [string]$check.name
            if ([string]::IsNullOrWhiteSpace($name)) { Add-Red 'prep-verdict.checks[] entry without a name'; continue }
            $checkNames.Add($name)
            if (-not [bool]$check.ok) { Add-Red "prep check not ok: $name ($([string]$check.detail))" }
        }
        $requiredPrepChecks = @('sut.health', 'sut.posts', 'sut.uploads', 'sut.web',
            'api.ready', 'executor.health', 'web.login', 'vision.catalog', 'vision.upstream',
            'vision.caption', 'db.eztest', 'db.sut', 'executor.auth', 'screenshots')
        if ($checks.Count -ne $requiredPrepChecks.Count) { Add-Red "prep-verdict.checks=$($checks.Count) (expected exactly $($requiredPrepChecks.Count))" }
        foreach ($required in $requiredPrepChecks) {
            if (@($checkNames) -notcontains $required) { Add-Red "missing prep check: $required" }
        }
        foreach ($name in @($checkNames)) {
            if ($requiredPrepChecks -notcontains $name) { Add-Red "unexpected prep check: $name" }
        }
        $duplicateChecks = @($checkNames | Group-Object | Where-Object { $_.Count -gt 1 } | ForEach-Object { $_.Name })
        if ($duplicateChecks.Count -gt 0) { Add-Red "duplicate prep checks: $($duplicateChecks -join ',')" }

        if (-not [bool]$verdict.db.eztestSelect1) { Add-Red 'db.eztestSelect1 is not true' }
        if (-not [bool]$verdict.db.sutSelect1) { Add-Red 'db.sutSelect1 is not true' }
        if (-not [bool]$verdict.db.uploads) { Add-Red 'db.uploads is not true' }
        $redisField = $verdict.db.PSObject.Properties['redis']
        if ($null -ne $redisField -and ([string]$redisField.Value -ne 'not_applicable') -and (-not [bool]$redisField.Value)) {
            Add-Red "db.redis=$([string]$redisField.Value) (expected true or not_applicable)"
        }

        $executorUrl = [string]$verdict.executor.url
        if ($executorUrl -ne 'http://127.0.0.1:13360') { Add-Red "executor.url='$executorUrl' (expected http://127.0.0.1:13360)" }
        if ([int]$verdict.executor.httpStatus -ne 200) { Add-Red "executor.httpStatus=$([string]$verdict.executor.httpStatus) (expected 200)" }
        if (-not [bool]$verdict.executor.auth.bearer200) { Add-Red 'executor auth: Bearer GET /v1/contexts did not return 200' }
        if (-not [bool]$verdict.executor.auth.noToken401) { Add-Red 'executor auth: GET /v1/contexts without a token did not return 401' }

        $visionModel = [string]$verdict.vision.model
        $visionBaseUrl = [string]$verdict.vision.baseUrl
        $visionCaption = [string]$verdict.vision.caption
        if ([string]::IsNullOrWhiteSpace($visionModel)) { Add-Red 'vision.model missing' }
        if ([string]::IsNullOrWhiteSpace($visionBaseUrl)) { Add-Red 'vision.baseUrl missing' }
        elseif ($visionBaseUrl -match '^(https?://)?(localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1\])(:\d+)?(/|$)') {
            Add-Red "vision.baseUrl points at a local proxy, not the real upstream: $visionBaseUrl"
        }
        if (-not [bool]$verdict.vision.visionExercised) { Add-Red 'vision.visionExercised is not true' }
        if ([string]::IsNullOrWhiteSpace($visionCaption)) { Add-Red 'vision.caption is empty (no real image caption)' }
        if (-not [bool]$verdict.vision.catalogSupportsVision) { Add-Red 'vision.catalogSupportsVision is not true' }
        if (-not [bool]$verdict.vision.catalogDefault) { Add-Red 'vision.catalogDefault is not true' }
        if ([int]$verdict.vision.httpStatus -ne 200) { Add-Red "vision.httpStatus=$([string]$verdict.vision.httpStatus) (expected 200)" }
        if ($visionModel -ne 'deepseek-flash') { Add-Red "vision.model='$visionModel' (expected deepseek-flash)" }

        # Raw evidence cross-checks: a forged verdict must not pass while the
        # recorded upstream response or catalog JSON says otherwise.
        $visionRaw = $null
        try { $visionRaw = Read-JsonUtf8 -Path (Join-Path $script:EvidenceRoot 'prep\checks\vision-upstream.json') } catch { $visionRaw = $null }
        if ($null -eq $visionRaw) { Add-Red 'missing prep/checks/vision-upstream.json' }
        else {
            if ([int]$visionRaw.status -ne 200) { Add-Red "vision-upstream.json status=$([string]$visionRaw.status) (expected 200)" }
            if ([string]$visionRaw.model -ne 'deepseek-flash') { Add-Red "vision-upstream.json model='$([string]$visionRaw.model)' (expected deepseek-flash)" }
            if (-not ([string]$visionRaw.baseUrl).StartsWith('https://')) { Add-Red "vision-upstream.json baseUrl='$([string]$visionRaw.baseUrl)' (expected https upstream)" }
            $rawCaption = [string]$visionRaw.caption
            if ([string]::IsNullOrWhiteSpace($rawCaption)) { Add-Red 'vision-upstream.json caption is empty' }
            elseif ($rawCaption -ne $visionCaption) { Add-Red "vision.caption='$visionCaption' does not match raw caption='$rawCaption'" }
            $rawContent = ''
            try { $rawContent = [string]$visionRaw.response.choices[0].message.content } catch { $rawContent = '' }
            if ([string]::IsNullOrWhiteSpace($rawContent)) { Add-Red 'vision-upstream.json response has no assistant content' }
            elseif ([string]::IsNullOrWhiteSpace($rawCaption) -or -not $rawContent.Contains($rawCaption)) { Add-Red 'vision-upstream.json content does not contain the recorded caption' }
        }
        $modelsRaw = $null
        try { $modelsRaw = Read-JsonUtf8 -Path (Join-Path $script:EvidenceRoot 'prep\checks\llm-models.json') } catch { $modelsRaw = $null }
        if ($null -eq $modelsRaw) { Add-Red 'missing prep/checks/llm-models.json' }
        else {
            if ([int]$modelsRaw.status -ne 200) { Add-Red "llm-models.json status=$([string]$modelsRaw.status) (expected 200)" }
            $catalogModels = @($modelsRaw.body.data)
            $catalogModel = @($catalogModels | Where-Object { [string]$_.model -eq 'deepseek-flash' })
            if ($catalogModel.Count -ne 1) { Add-Red "llm-models.json deepseek-flash entries=$($catalogModel.Count) (expected 1)" }
            else {
                if (-not [bool]$catalogModel[0].capabilities.supports_vision) { Add-Red 'llm-models.json deepseek-flash supports_vision is not true' }
                if (-not [bool]$catalogModel[0].default) { Add-Red 'llm-models.json deepseek-flash is not the default model' }
            }
        }
        $prepChecksDir = Join-Path $script:EvidenceRoot 'prep\checks'
        function Read-PrepRaw([string]$Name) {
            $path = Join-Path $prepChecksDir $Name
            if (-not (Test-Path -LiteralPath $path)) { return $null }
            try { return (Read-JsonUtf8 -Path $path) } catch { return $null }
        }
        $webLoginRaw = Read-PrepRaw 'web-login.json'
        if ($null -eq $webLoginRaw) { Add-Red 'missing prep/checks/web-login.json' }
        elseif ([int]$webLoginRaw.status -ne 200) { Add-Red "web-login.json status=$([string]$webLoginRaw.status) (expected 200)" }
        $execRaw = Read-PrepRaw 'executor-contexts.json'
        $execNoAuthRaw = Read-PrepRaw 'executor-unauthorized.json'
        $execHealthRaw = Read-PrepRaw 'executor-health.json'
        if ($null -eq $execRaw) { Add-Red 'missing prep/checks/executor-contexts.json' }
        elseif ([int]$execRaw.status -ne 200) { Add-Red "executor-contexts.json status=$([string]$execRaw.status) (expected 200)" }
        if ($null -eq $execNoAuthRaw) { Add-Red 'missing prep/checks/executor-unauthorized.json' }
        elseif ([int]$execNoAuthRaw.status -ne 401) { Add-Red "executor-unauthorized.json status=$([string]$execNoAuthRaw.status) (expected 401)" }
        if ($null -eq $execHealthRaw) { Add-Red 'missing prep/checks/executor-health.json' }
        elseif ([int]$execHealthRaw.status -ne 200) { Add-Red "executor-health.json status=$([string]$execHealthRaw.status) (expected 200)" }

        if ([string]$verdict.stack.db -ne 'eztest_v022_fa') { Add-Red "stack.db=$([string]$verdict.stack.db) (expected eztest_v022_fa)" }
        if ([int]$verdict.stack.apiPort -ne 13460) { Add-Red "stack.apiPort=$([string]$verdict.stack.apiPort) (expected 13460)" }
        if ([int]$verdict.stack.webPort -ne 6880) { Add-Red "stack.webPort=$([string]$verdict.stack.webPort) (expected 6880)" }
        if ([int]$verdict.stack.executorPort -ne 13360) { Add-Red "stack.executorPort=$([string]$verdict.stack.executorPort) (expected 13360)" }
        foreach ($processName in @('api', 'web', 'executor')) {
            if ([int]$verdict.stack.pids.$processName -le 0) { Add-Red "stack.pids.$processName missing or not positive" }
        }

        $inputsSection = Join-Path $script:EvidenceRoot 'inputs\typical-section1.md'
        $issuesMd = Join-Path $repoRoot 'docs\version-V0.22\acceptance\issues.md'
        $frozenInputs = [string]$verdict.frozenShas.inputs
        $frozenGate = [string]$verdict.frozenShas.gate
        $frozenIssues = [string]$verdict.frozenShas.issuesMd
        if ($frozenInputs -notmatch '^[0-9a-f]{64}$') { Add-Red 'frozenShas.inputs is not a 64-char lowercase SHA256' }
        elseif (-not (Test-Path -LiteralPath $inputsSection)) { Add-Red 'missing inputs/typical-section1.md (cannot verify frozenShas.inputs)' }
        else {
            $currentInputs = Get-Sha -Path $inputsSection
            if ($frozenInputs -ne $currentInputs) { Add-Red "frozenShas.inputs=$frozenInputs does not match current typical-section1.md=$currentInputs" }
        }
        if ($frozenGate -notmatch '^[0-9a-f]{64}$') { Add-Red 'frozenShas.gate is not a 64-char lowercase SHA256' }
        else {
            $currentGateSha = Get-Sha -Path (Join-Path $PSScriptRoot 'v022-fa-gate.ps1')
            if ($frozenGate -ne $currentGateSha) { Add-Red "frozenShas.gate=$frozenGate does not match the current gate script=$currentGateSha (re-run prep after any gate change)" }
        }
        if ($frozenIssues -notmatch '^[0-9a-f]{64}$') { Add-Red 'frozenShas.issuesMd is not a 64-char lowercase SHA256' }
        elseif (-not (Test-Path -LiteralPath $issuesMd)) { Add-Red 'missing docs/version-V0.22/acceptance/issues.md (cannot verify frozenShas.issuesMd)' }
        else {
            $currentIssues = Get-Sha -Path $issuesMd
            if ($frozenIssues -ne $currentIssues) { Add-Red "frozenShas.issuesMd=$frozenIssues does not match current issues.md=$currentIssues" }
        }

        if (-not (Test-Path -LiteralPath $prepCheckDir)) { Add-Red 'missing prep/checks/ (raw responses)' }
        else {
            $checkFiles = @(Get-ChildItem -LiteralPath $prepCheckDir -Filter '*.json' -File -ErrorAction SilentlyContinue)
            if ($checkFiles.Count -lt 8) { Add-Red "prep/checks/*.json=$($checkFiles.Count) (need >= 8 raw responses)" }
        }
        foreach ($shotName in @('versions.png', 'settings.png')) {
            $shotPath = Join-Path $prepShotDir $shotName
            foreach ($problem in @(Assert-Png -Path $shotPath)) { Add-Red $problem }
        }
    }
}

# ===========================================================================
# Gate: harness (run-meta)
# ===========================================================================
function Invoke-GateHarness {
    $runRoot = Join-Path $script:EvidenceRoot $script:RunDir
    $metaPath = Join-Path $runRoot 'run-meta.json'
    if (-not (Test-Path -LiteralPath $metaPath)) {
        Add-Red "missing $($script:RunDir)/run-meta.json"
        return
    }
    $meta = Read-JsonOrRed $metaPath 'run-meta.json'
    if ($null -eq $meta) { return }

    $runId = Get-StringValue $meta @('runId')
    if ([string]::IsNullOrWhiteSpace($runId)) { Add-Red 'run-meta.runId missing' }

    $phase = Get-StringValue $meta @('phase')
    $expectedPhase = if ($script:RunDir -eq 'RUN-1') { 'run1' } else { 'run2' }
    if ([string]::IsNullOrWhiteSpace($phase)) { Add-Red 'run-meta.phase missing' }
    elseif (($phase.ToLowerInvariant() -ne $expectedPhase) -and ($phase.ToUpperInvariant() -ne $script:RunDir)) {
        Add-Red "run-meta.phase='$phase' does not match RunDir=$($script:RunDir)"
    }

    $gitHead = Get-StringValue $meta @('gitHead')
    if ($gitHead -notmatch '^[0-9a-f]{40}$') { Add-Red "run-meta.gitHead is not a 40-char lowercase SHA: '$gitHead'" }
    elseif ([string]::IsNullOrWhiteSpace($script:Head)) { Add-Red 'cannot resolve git HEAD for run-meta.gitHead comparison' }
    elseif ($gitHead -ne $script:Head) { Add-Red "run-meta.gitHead=$gitHead does not match git HEAD=$($script:Head)" }

    $reuse = Get-Value $meta @('reuse')
    if ($null -eq $reuse) { Add-Red 'run-meta.reuse missing' }
    elseif ([bool](Get-Value $reuse @('reused') $false)) { Add-Red 'run-meta.reuse.reused is true (Run must be fresh)' }

    $startedText = Get-StringValue $meta @('startedAt')
    if ([string]::IsNullOrWhiteSpace($startedText)) {
        Add-Red 'run-meta.startedAt missing'
    }
    else {
        $startedAt = [datetime]::MinValue
        try {
            $startedAt = [datetime]::Parse($startedText, [System.Globalization.CultureInfo]::InvariantCulture, [System.Globalization.DateTimeStyles]::RoundtripKind)
        }
        catch { Add-Red "run-meta.startedAt is not a valid ISO8601 timestamp: '$startedText'" }
        if ($startedAt -ne [datetime]::MinValue) { Assert-FreshRun $runRoot $startedAt }
    }
}

# ===========================================================================
# Gate: flow
# ===========================================================================
function Invoke-GateFlow {
    $flow = Get-EvidenceJson "$($script:RunDir)/w0flow/result.json" 'w0flow/result.json'
    if ($null -eq $flow) {
        $fallback = Get-EvidenceJson "$($script:RunDir)/w0flow/flow.json" 'w0flow/flow.json'
        if ($null -eq $fallback) { return }
        $flow = $fallback
    }

    $flowExit = Get-IntValue $flow @('flowExit') -1
    if ($flowExit -ne 0) { Add-Red "flowExit=$flowExit (expected 0)" }

    $redItems = @(Get-Iarray (Get-Value $flow @('red')))
    if ($redItems.Count -gt 0) { Add-Red ("flow red is not empty: " + (($redItems | ForEach-Object { [string]$_ }) -join ', ')) }

    $requiredStages = @('created', 'clarifying', 'requirement_review', 'designing', 'case_review', 'executing', 'reporting', 'completed')
    $stageItems = @(Get-Iarray (Get-Value $flow @('stages', 'stageTrace')))
    if ($stageItems.Count -eq 0) { Add-Red 'flow stages missing' }

    $stageShots = @{}
    $stageIds = New-Object System.Collections.Generic.List[string]
    foreach ($item in $stageItems) {
        $name = if ($item -is [string]) { [string]$item } else { Get-StringValue $item @('name', 'stage', 'to') }
        if ([string]::IsNullOrWhiteSpace($name)) { Add-Red 'flow stages[] entry without a name'; continue }
        $stageIds.Add($name)
        $shots = @()
        if ($item -isnot [string]) {
            $shots = @(Get-Iarray (Get-Value $item @('screenshots', 'shots', 'pngs')))
        }
        if ($shots.Count -eq 0) {
            $byStage = Get-Value $flow @('stageScreenshots', 'screenshotsByStage')
            if ($null -ne $byStage) {
                $fromMap = Get-Value $byStage @($name)
                if ($null -ne $fromMap) { $shots = @(Get-Iarray $fromMap) }
            }
        }
        if ($shots.Count -eq 0) {
            $topShots = @(Get-Iarray (Get-Value $flow @('screenshots')))
            foreach ($top in $topShots) {
                if ($top -is [string]) {
                    if ($top -match [regex]::Escape($name)) { $shots += [string]$top }
                }
                else {
                    $topStage = Get-StringValue $top @('stage', 'name')
                    if ($topStage -eq $name) {
                        $path = Get-StringValue $top @('path', 'file')
                        if ($path) { $shots += $path }
                    }
                }
            }
        }
        if (-not $stageShots.ContainsKey($name)) { $stageShots[$name] = @() }
        $stageShots[$name] = @($stageShots[$name]) + @($shots)
    }

    foreach ($problem in @(Assert-ExactSetStrings -Actual @($stageIds) -Expected $requiredStages -Label 'flow.stages')) { Add-Red $problem }
    foreach ($stage in $requiredStages) {
        $shots = @()
        if ($stageShots.ContainsKey($stage)) { $shots = @($stageShots[$stage]) }
        if ($shots.Count -eq 0) {
            Add-Red "stage $stage has no screenshot"
        }
        else {
            foreach ($shot in $shots) {
                $resolved = Assert-FaFile ([string]$shot) "stage $stage screenshot"
                if ($null -ne $resolved) {
                    foreach ($problem in @(Assert-Png -Path $resolved)) { Add-Red "stage $stage $problem" }
                }
            }
        }
    }

    $db = Get-Value $flow @('dbCounters', 'db')
    $thresholds = [ordered]@{ requirements = 12; cases = 1; waves = 1; caseRuns = 1; evidence = 1 }
    foreach ($key in $thresholds.Keys) {
        $value = 0
        if ($null -ne $db) {
            $names = if ($key -eq 'evidence') { @('evidence', 'evidenceRows') } else { @($key) }
            $value = Get-IntValue $db $names 0
        }
        if ($value -lt [int]$thresholds[$key]) { Add-Red "db.$key=$value (need >= $($thresholds[$key]))" }
    }

    $agentRun = Get-Value $flow @('agentRun')
    $agentRuns = @(Get-Iarray (Get-Value $flow @('agentRuns')))
    $model = ''
    if ($null -ne $agentRun) { $model = Get-StringValue $agentRun @('model') }
    if ([string]::IsNullOrWhiteSpace($model) -and $agentRuns.Count -gt 0) {
        foreach ($run in $agentRuns) {
            $candidate = Get-StringValue $run @('model')
            if (-not [string]::IsNullOrWhiteSpace($candidate)) { $model = $candidate; break }
        }
    }
    if ([string]::IsNullOrWhiteSpace($model)) { $model = Get-StringValue $flow @('model') }
    if ($agentRuns.Count -eq 0 -and $null -eq $agentRun) { Add-Red 'agentRun/agentRuns missing (need at least one run)' }
    if ($model -ne 'deepseek-flash') { Add-Red "agentRun.model='$model' (expected deepseek-flash)" }
}

# ===========================================================================
# Gate: typical
# ===========================================================================
function Invoke-GateTypical {
    $typical = Get-EvidenceJson "$($script:RunDir)/typical/eztest.json" 'typical/eztest.json'
    if ($null -eq $typical) { return }

    $stage = Get-StringValue $typical @('stage')
    if ([string]::IsNullOrWhiteSpace($stage)) {
        $version = Get-Value $typical @('version')
        if ($null -ne $version) { $stage = Get-StringValue $version @('stage') }
    }
    if ($stage -ne 'completed') { Add-Red "typical.stage='$stage' (expected completed)" }

    $requirements = @(Get-Iarray (Get-Value $typical @('requirements')))
    if ($requirements.Count -eq 0) {
        $requirementIds = @(Get-Iarray (Get-Value $typical @('requirementIds')))
        $requirements = @($requirementIds | ForEach-Object { [pscustomobject]@{ id = [string]$_ } })
    }
    if ($requirements.Count -ne 12) { Add-Red "typical.requirements=$($requirements.Count) (expected 12)" }

    $requirementIds = New-Object System.Collections.Generic.List[string]
    $cases = @(Get-Iarray (Get-Value $typical @('cases')))
    foreach ($requirement in $requirements) {
        $rid = if ($requirement -is [string]) { [string]$requirement } else { Get-StringValue $requirement @('id', 'requirementId', 'external_id') }
        if ([string]::IsNullOrWhiteSpace($rid)) { Add-Red 'typical.requirements[] entry without an id'; continue }
        $requirementIds.Add($rid)
        $caseIds = @()
        if ($requirement -isnot [string]) { $caseIds = @(Get-Iarray (Get-Value $requirement @('caseIds', 'cases'))) }
        if ($caseIds.Count -eq 0 -and $cases.Count -gt 0) {
            foreach ($case in $cases) {
                $caseReq = Get-StringValue $case @('requirementId', 'requirement_id', 'requirement')
                if ($caseReq -eq $rid) { $caseIds += [string](Get-StringValue $case @('id', 'caseId')) }
            }
        }
        if ($caseIds.Count -eq 0) { Add-Red "requirement $rid has no case" }
    }
    if (@($requirementIds | Where-Object { $_ -match '^R\d{2}$' }).Count -eq $requirementIds.Count -and $requirementIds.Count -gt 0) {
        foreach ($problem in @(Assert-ExactSetStrings -Actual @($requirementIds) -Expected $script:ExpectedRequirementIds -Label 'typical.requirements')) { Add-Red $problem }
    }
    else {
        if ($requirementIds.Count -ne 12) { Add-Red "typical requirement ids are not the exact R01-R12 set" }
        if (@($requirementIds | Sort-Object -Unique).Count -ne $requirementIds.Count) { Add-Red 'typical.requirements has duplicate ids' }
    }

    $caseRuns = Get-Value $typical @('caseRuns', 'case_runs', 'caseRunIds')
    $caseRunCount = 0
    if ($null -eq $caseRuns) { Add-Red 'typical.caseRuns missing' }
    elseif ($caseRuns -is [string] -or $caseRuns -is [System.ValueType]) { $caseRunCount = 1 }
    elseif ($caseRuns -is [System.Array]) { $caseRunCount = @($caseRuns).Count }
    else {
        foreach ($property in $caseRuns.PSObject.Properties) {
            $number = 0
            if ([int]::TryParse([string]$property.Value, [ref]$number)) { $caseRunCount += $number }
            else { $caseRunCount++ }
        }
    }
    if ($caseRunCount -le 0) { Add-Red 'typical.caseRuns is empty' }

    $shots = @(Get-Iarray (Get-Value $typical @('screenshots', 'screenshot')))
    if ($shots.Count -eq 0) { Add-Red 'typical.screenshots is empty' }
    else { Assert-Screenshots $shots 'typical' }

    $validVerdicts = @('pass', 'fail', 'gap', 'blocked', 'missing')
    $sawVerdict = $false
    $topVerdict = Get-StringValue $typical @('eztestVerdict')
    if (-not [string]::IsNullOrWhiteSpace($topVerdict)) {
        $sawVerdict = $true
        if ($validVerdicts -notcontains $topVerdict.ToLowerInvariant()) { Add-Red "typical.eztestVerdict='$topVerdict' is not a legal verdict" }
    }
    foreach ($requirement in $requirements) {
        if ($requirement -is [string]) { continue }
        $verdict = Get-StringValue $requirement @('eztestVerdict')
        if ([string]::IsNullOrWhiteSpace($verdict)) { continue }
        $sawVerdict = $true
        if ($validVerdicts -notcontains $verdict.ToLowerInvariant()) { Add-Red "requirement $([string](Get-StringValue $requirement @('id'))) eztestVerdict='$verdict' is not legal" }
    }
    if (-not $sawVerdict) { Add-Red 'typical.eztestVerdict missing' }
}

# ===========================================================================
# Gate: acs
# ===========================================================================
function Invoke-GateAcs {
    $sourceFiles = @()
    if ($script:Range -eq 'R01-R06') { $sourceFiles = @('acs-r01-r06.json') }
    elseif ($script:Range -eq 'R07-R12') { $sourceFiles = @('acs-r07-r12.json') }
    else { $sourceFiles = @('acs-r01-r06.json', 'acs-r07-r12.json') }

    $allAcs = @()
    $allRequirements = @()
    foreach ($file in $sourceFiles) {
        $json = Get-EvidenceJson "$($script:RunDir)/$file" $file
        if ($null -eq $json) { continue }
        if ($json -is [System.Array]) { $allAcs += @($json); continue }
        $allAcs += @(Get-Iarray (Get-Value $json @('acs', 'acsList')))
        $allRequirements += @(Get-Iarray (Get-Value $json @('requirements')))
    }

    $expectedAcIds = @(Get-ExpectedAcIdsForRange $script:Range)
    $actualAcIds = New-Object System.Collections.Generic.List[string]
    foreach ($ac in $allAcs) {
        $acId = Get-StringValue $ac @('id', 'acId')
        if ([string]::IsNullOrWhiteSpace($acId)) { Add-Red 'acs[] entry without an id'; continue }
        $actualAcIds.Add($acId)
    }
    foreach ($problem in @(Assert-ExactSetStrings -Actual @($actualAcIds) -Expected $expectedAcIds -Label 'acIds')) { Add-Red $problem }
    if ($allAcs.Count -ne $expectedAcIds.Count) { Add-Red "acs=$($allAcs.Count) (expected $($expectedAcIds.Count) for range $($script:Range))" }

    $validStatus = @('pass', 'fail', 'gap', 'blocked', 'partial')
    $validGroundTruth = @('present', 'absent', 'partial')
    $validVerdict = @('pass', 'fail', 'gap', 'blocked', 'missing')
    $statusByRequirement = @{}

    foreach ($ac in $allAcs) {
        $acId = Get-StringValue $ac @('id', 'acId')
        if ([string]::IsNullOrWhiteSpace($acId)) { continue }
        $rid = Get-StringValue $ac @('requirementId', 'requirement_id')
        if ([string]::IsNullOrWhiteSpace($rid) -and $acId -match '^AC-(R\d{2})-') { $rid = $Matches[1] }

        $status = (Get-StringValue $ac @('status')).ToLowerInvariant()
        if ($validStatus -notcontains $status) { Add-Red "$acId status='$status' (expected pass|fail|gap|blocked|partial)" }
        $groundTruth = (Get-StringValue $ac @('groundTruth', 'ground_truth')).ToLowerInvariant()
        if ($validGroundTruth -notcontains $groundTruth) { Add-Red "$acId groundTruth='$groundTruth' (expected present|absent|partial)" }

        $probe = Get-Value $ac @('probe')
        if ($null -eq $probe) {
            Add-Red "$acId missing probe"
        }
        else {
            $recomputed = (Get-StringValue $probe @('recomputed')).ToLowerInvariant()
            if ([string]::IsNullOrWhiteSpace($recomputed)) { Add-Red "$acId probe.recomputed missing" }
            elseif ($recomputed -ne $status) { Add-Red "$acId probe.recomputed='$recomputed' != status='$status'" }
            $probeFile = Get-StringValue $probe @('file', 'path')
            if ([string]::IsNullOrWhiteSpace($probeFile)) { Add-Red "$acId missing probe file" }
            else {
                $probeResolved = Resolve-FaPath $probeFile (Join-Path $script:EvidenceRoot "$($script:RunDir)\probes")
                if ($null -eq $probeResolved) { Add-Red "$acId missing probe: $probeFile" }
                else {
                    $probeJson = Read-JsonOrRed $probeResolved "$acId probe JSON"
                    if ($null -ne $probeJson) {
                        $probeRecomputed = (Get-StringValue $probeJson @('recomputed')).ToLowerInvariant()
                        if ($probeRecomputed -and $probeRecomputed -ne $status) {
                            Add-Red "$acId probe file recomputed='$probeRecomputed' != status='$status'"
                        }
                    }
                }
            }
        }

        $observed = [string](Get-Value $ac @('observed') '')
        $expected = [string](Get-Value $ac @('expected') '')
        if ([string]::IsNullOrWhiteSpace($observed)) { Add-Red "$acId observed is empty" }
        if ([string]::IsNullOrWhiteSpace($expected)) { Add-Red "$acId expected is empty" }

        Assert-EvidenceFileList (Get-Value $ac @('evidence')) "$acId"
        $shots = @(Get-Iarray (Get-Value $ac @('screenshots')))
        if ($shots.Count -eq 0) { Add-Red "$acId has no screenshots" } else { Assert-Screenshots $shots "$acId" }

        $verdict = (Get-StringValue $ac @('eztestVerdict')).ToLowerInvariant()
        if ($validVerdict -notcontains $verdict) { Add-Red "$acId eztestVerdict='$verdict' is not legal" }
        elseif ($groundTruth -eq 'absent' -and $verdict -eq 'pass') { Add-Red "$acId groundTruth=absent but eztestVerdict=pass" }
        elseif ($status -eq 'pass' -and $verdict -ne 'pass') { Add-Red "$acId SUT truth pass but eztestVerdict='$verdict'" }
        elseif ($status -in @('fail', 'gap', 'blocked') -and $verdict -eq 'pass') { Add-Red "$acId SUT truth=$status but eztestVerdict=pass" }

        if ([string]::IsNullOrWhiteSpace($rid)) { Add-Red "$acId requirementId missing" }
        else {
            if (-not $statusByRequirement.ContainsKey($rid)) { $statusByRequirement[$rid] = New-Object System.Collections.Generic.List[string] }
            $statusByRequirement[$rid].Add($status)
        }
    }

    $expectedRequirementIds = @(Get-ExpectedRequirementIdsForRange $script:Range)
    $actualRequirementIds = New-Object System.Collections.Generic.List[string]
    foreach ($requirement in $allRequirements) {
        $rid = if ($requirement -is [string]) { [string]$requirement } else { Get-StringValue $requirement @('id', 'requirementId') }
        if ([string]::IsNullOrWhiteSpace($rid)) { Add-Red 'acs requirements[] entry without an id'; continue }
        $actualRequirementIds.Add($rid)
        if ($requirement -is [string]) { continue }
        $declared = (Get-StringValue $requirement @('status')).ToLowerInvariant()
        $derived = 'pass'
        $statuses = @()
        if ($statusByRequirement.ContainsKey($rid)) { $statuses = @($statusByRequirement[$rid]) }
        if ($statuses -contains 'fail') { $derived = 'fail' }
        elseif ($statuses -contains 'blocked') { $derived = 'blocked' }
        elseif ($statuses -contains 'gap') { $derived = 'gap' }
        elseif ($statuses -contains 'partial') { $derived = 'partial' }
        if ($declared -ne $derived) { Add-Red "requirement $rid status='$declared' but AC-derived status='$derived'" }
        $eztestVerdict = (Get-StringValue $requirement @('eztestVerdict')).ToLowerInvariant()
        if ($validVerdict -notcontains $eztestVerdict) { Add-Red "requirement $rid eztestVerdict='$eztestVerdict' is not legal" }
        elseif ($derived -eq 'pass' -and $eztestVerdict -ne 'pass') { Add-Red "requirement $rid SUT truth pass but eztestVerdict='$eztestVerdict'" }
        elseif ($derived -in @('fail', 'gap', 'blocked', 'partial') -and $eztestVerdict -eq 'pass') { Add-Red "requirement $rid SUT truth=$derived but eztestVerdict=pass" }
    }
    if ($allRequirements.Count -eq 0) { Add-Red 'acs requirements[] missing (requirement status must be AC-derived)' }
    else {
        foreach ($problem in @(Assert-ExactSetStrings -Actual @($actualRequirementIds) -Expected $expectedRequirementIds -Label 'acs.requirements')) { Add-Red $problem }
    }
}

# ===========================================================================
# Gate: issues14
# ===========================================================================
function Invoke-GateIssues14 {
    $issuesJson = Get-EvidenceJson "$($script:RunDir)/issues14.json" 'issues14.json'
    if ($null -eq $issuesJson) { return }

    if ($issuesJson -is [System.Array]) { $problems = @($issuesJson) }
    else { $problems = @(Get-Iarray (Get-Value $issuesJson @('problems', 'issues'))) }
    $actualIds = New-Object System.Collections.Generic.List[string]
    foreach ($problem in $problems) {
        $id = Get-StringValue $problem @('id')
        if ($id) { $actualIds.Add($id) }
    }
    $expectedIds = @(1..14 | ForEach-Object { [string]$_ })
    foreach ($msg in @(Assert-ExactSetStrings -Actual @($actualIds) -Expected $expectedIds -Label 'problems')) { Add-Red $msg }

    $w0Path = Resolve-FaPath "$($script:RunDir)/w0flow/result.json"
    if ($null -eq $w0Path) { $w0Path = Resolve-FaPath 'docs/version-V0.22/acceptance/evidence/w0/run/result.json' }
    $passingChecks = @{}
    if ($null -eq $w0Path) {
        Add-Red 'fresh W0 result.json missing (cannot verify w0CheckIds)'
    }
    else {
        $w0 = Read-JsonOrRed $w0Path 'W0 result.json'
        if ($null -ne $w0) {
            foreach ($container in @((Get-Value $w0 @('checks')), (Get-Value $w0 @('state')))) {
                if ($null -eq $container) { continue }
                $checkBag = if ($container -is [System.Collections.IDictionary]) { $container } else { Get-Value $container @('checks') }
                if ($null -eq $checkBag) { continue }
                foreach ($property in $checkBag.PSObject.Properties) {
                    $ok = Get-Value $property.Value @('ok', 'pass', 'passed')
                    if ($null -ne $ok -and [bool]$ok) { $passingChecks[$property.Name] = $true }
                }
            }
            foreach ($collectionName in @('items', 'checks')) {
                foreach ($item in @(Get-Iarray (Get-Value $w0 @($collectionName)))) {
                    $id = Get-StringValue $item @('id', 'name')
                    $ok = Get-Value $item @('ok', 'pass', 'passed')
                    if ($id -and $null -ne $ok -and [bool]$ok) { $passingChecks[$id] = $true }
                }
            }
            $assertions = Get-Value $w0 @('assertions')
            if ($null -ne $assertions) {
                foreach ($property in $assertions.PSObject.Properties) {
                    if ([bool]$property.Value) { $passingChecks[$property.Name] = $true }
                }
            }
        }
    }

    $independentIds = @('1', '2', '4', '5', '11', '12')
    foreach ($problem in $problems) {
        $id = Get-StringValue $problem @('id')
        $status = (Get-StringValue $problem @('status')).ToLowerInvariant()
        if ($status -ne 'pass') { Add-Red "problem $id status='$status' (expected pass)" }
        $checkIds = @(Get-Iarray (Get-Value $problem @('w0CheckIds', 'w0CheckId')))
        if ($checkIds.Count -eq 0) { Add-Red "problem $id has no w0CheckIds" }
        foreach ($checkId in $checkIds) {
            if (-not $passingChecks.ContainsKey([string]$checkId)) { Add-Red "problem $id w0 check not PASS: $checkId" }
        }
        $assertions = @(Get-Iarray (Get-Value $problem @('assertions')))
        if ($assertions.Count -eq 0) { Add-Red "problem $id assertions is empty" }
        foreach ($assertion in $assertions) {
            $ok = Get-Value $assertion @('ok', 'pass')
            if ($null -ne $ok -and -not [bool]$ok) { Add-Red "problem $id assertion not ok: $([string](Get-Value $assertion @('detail') ''))" }
        }
        if ($independentIds -contains $id) {
            $hasIndependent = $false
            foreach ($assertion in $assertions) {
                if ([bool](Get-Value $assertion @('independent') $false)) { $hasIndependent = $true }
            }
            if (-not $hasIndependent) { Add-Red "problem $id (legacy hard-coded item) has no independent assertion" }
        }
        Assert-EvidenceFileList (Get-Value $problem @('evidence')) "problem $id"
    }
}

# ===========================================================================
# Gate: domains
# ===========================================================================
function Get-LastRegexNumber {
    param([string]$Text, [string]$Pattern)
    $matches = [regex]::Matches($Text, $Pattern)
    if ($matches.Count -eq 0) { return -1 }
    return [int]$matches[$matches.Count - 1].Groups[1].Value
}

function Get-ContractCounts {
    param($W2)
    $contract = Get-Value $W2 @('contract')
    if ($null -ne $contract) {
        return [pscustomobject]@{ Paths = Get-IntValue $contract @('paths') -1; Operations = Get-IntValue $contract @('operations', 'ops') -1; Schemas = Get-IntValue $contract @('schemas') -1; Source = 'w2.contract' }
    }
    $thresholds = Get-Value $W2 @('domainThresholds')
    if ($null -ne $thresholds) {
        $nested = Get-Value $thresholds @('contract')
        if ($null -ne $nested) {
            return [pscustomobject]@{ Paths = Get-IntValue $nested @('paths') -1; Operations = Get-IntValue $nested @('operations', 'ops') -1; Schemas = Get-IntValue $nested @('schemas') -1; Source = 'w2.domainThresholds.contract' }
        }
    }
    foreach ($relative in @("$($script:RunDir)/w2/contract.json", "$($script:RunDir)/w2/contract-verdict.json", "$($script:RunDir)/contract.json", 'contract.json')) {
        $resolved = Resolve-FaPath $relative
        if ($null -eq $resolved) { continue }
        $json = Read-JsonOrRed $resolved 'contract verdict'
        if ($null -ne $json) {
            return [pscustomobject]@{ Paths = Get-IntValue $json @('paths') -1; Operations = Get-IntValue $json @('operations', 'ops') -1; Schemas = Get-IntValue $json @('schemas') -1; Source = $relative }
        }
    }
    foreach ($gate in @(Get-Iarray (Get-Value $W2 @('gates')))) {
        if ((Get-StringValue $gate @('name')) -ne 'contract.v022') { continue }
        $log = Get-StringValue $gate @('log')
        if ([string]::IsNullOrWhiteSpace($log)) { continue }
        $resolved = Resolve-FaPath $log
        if ($null -eq $resolved) { continue }
        $text = [System.IO.File]::ReadAllText($resolved, [System.Text.Encoding]::UTF8)
        $summaryLines = @($text -split "`r?`n" | Where-Object { $_ -match 'contract check PASSED|contract check passed' })
        $parseText = if ($summaryLines.Count -gt 0) { $summaryLines[$summaryLines.Count - 1] } else { $text }
        $paths = Get-LastRegexNumber $parseText 'paths\s*[=:]\s*(\d+)'
        if ($paths -lt 0) { $paths = Get-LastRegexNumber $parseText '(\d+)\s+paths' }
        $operations = Get-LastRegexNumber $parseText 'operations?\s*[=:]\s*(\d+)'
        if ($operations -lt 0) { $operations = Get-LastRegexNumber $parseText '(\d+)\s+(?:operations?|ops)' }
        $schemas = Get-LastRegexNumber $parseText 'schemas?\s*[=:]\s*(\d+)'
        if ($schemas -lt 0) { $schemas = Get-LastRegexNumber $parseText '(\d+)\s+schemas' }
        return [pscustomobject]@{ Paths = $paths; Operations = $operations; Schemas = $schemas; Source = $log }
    }
    return $null
}

function Invoke-GateDomains {
    $w2 = Get-EvidenceJson "$($script:RunDir)/w2/w2-exit-verdict.json" 'w2/w2-exit-verdict.json'
    if ($null -eq $w2) { return }

    $status = Get-StringValue $w2 @('status')
    if ($status -ne 'PASS') { Add-Red "w2-exit-verdict.status='$status' (expected PASS)" }
    if (@(Get-Iarray (Get-Value $w2 @('red'))).Count -gt 0) { Add-Red 'w2-exit-verdict.red is not empty' }

    $requiredGateNames = @('data.base', 'domain.human', 'domain.issue', 'domain.export', 'domain.code',
        'domain.visual', 'domain.exploration', 'domain.eval', 'domain.gc',
        'static.constructor-refs', 'static.api-only', 'static.event-consumers', 'static.explore-residue',
        'contract.v021', 'contract.v022', 'backend.unit', 'backend.integration',
        'frontend.test', 'frontend.typecheck', 'restart.persistence')
    $gates = @(Get-Iarray (Get-Value $w2 @('gates')))
    if ($gates.Count -ne 20) { Add-Red "w2 gates=$($gates.Count) (expected 20)" }
    $gateNames = New-Object System.Collections.Generic.List[string]
    foreach ($gate in $gates) {
        $name = Get-StringValue $gate @('name')
        if ($name) { $gateNames.Add($name) }
        $exit = Get-IntValue $gate @('exit') -1
        if ($exit -ne 0) { Add-Red "w2 gate $name exit=$exit (expected 0)" }
    }
    foreach ($problem in @(Assert-ExactSetStrings -Actual @($gateNames) -Expected $requiredGateNames -Label 'w2.gates')) { Add-Red $problem }

    $restart = Get-Value $w2 @('restart')
    $restartEqual = if ($null -ne $restart) { [bool](Get-Value $restart @('equal') $false) } else { [bool](Get-Value $w2 @('restartEqual') $false) }
    if (-not $restartEqual) { Add-Red 'w2.restart.equal is not true' }

    $eventCounters = Get-Value $w2 @('eventCounters')
    $unregistered = if ($null -ne $eventCounters) { Get-IntValue $eventCounters @('unregistered') -1 } else { Get-IntValue $w2 @('unregistered') -1 }
    $unknown = if ($null -ne $eventCounters) { Get-IntValue $eventCounters @('unknown') -1 } else { Get-IntValue $w2 @('unknown') -1 }
    if ($unregistered -ne 0) { Add-Red "w2 unregistered=$unregistered (expected 0)" }
    if ($unknown -ne 0) { Add-Red "w2 unknown=$unknown (expected 0)" }

    $domainFiles = [ordered]@{
        human = 'w2/human-verdict.json'
        issue = 'w2/issue/issue-verdict.json'
        export = 'w2/export/export-verdict.json'
        code = 'w2/code-verdict.json'
        visual = 'w2/visual-verdict.json'
        eval = 'w2/eval-verdict.json'
        gc = 'w2/gc-verdict.json'
    }
    foreach ($domain in $domainFiles.Keys) {
        $relative = $domainFiles[$domain]
        $resolved = Resolve-FaPath "$($script:RunDir)/$relative"
        if ($null -eq $resolved -and ($domain -eq 'issue' -or $domain -eq 'export')) {
            $flat = if ($domain -eq 'issue') { "$($script:RunDir)/w2/issue-verdict.json" } else { "$($script:RunDir)/w2/export-verdict.json" }
            $resolved = Resolve-FaPath $flat
        }
        if ($null -eq $resolved) { Add-Red "missing domain verdict: $domain ($relative)"; continue }
        $verdict = Read-JsonOrRed $resolved "$domain verdict"
        if ($null -eq $verdict) { continue }
        $domainStatus = Get-StringValue $verdict @('status')
        if ($domainStatus -ne 'PASS') { Add-Red "domain $domain status='$domainStatus' (expected PASS)" }
        if (@(Get-Iarray (Get-Value $verdict @('red'))).Count -gt 0) { Add-Red "domain $domain red is not empty" }
        $domainHead = Get-StringValue $verdict @('gitHead')
        if ($domainHead) { [void](Assert-StageReuse $domainHead "domain $domain") }
    }

    $contract = Get-ContractCounts $w2
    if ($null -eq $contract) {
        Add-Red 'contract counts missing (need 214 paths / 264 operations / 322 schemas)'
    }
    else {
        if ([int]$contract.Paths -ne 214) { Add-Red "contract.paths=$($contract.Paths) (expected 214; source=$($contract.Source))" }
        if ([int]$contract.Operations -ne 264) { Add-Red "contract.operations=$($contract.Operations) (expected 264; source=$($contract.Source))" }
        if ([int]$contract.Schemas -ne 322) { Add-Red "contract.schemas=$($contract.Schemas) (expected 322; source=$($contract.Source))" }
    }

    $thresholds = Get-Value $w2 @('domainThresholds')
    $migrations = if ($null -ne $thresholds) { Get-IntValue $thresholds @('migrations') -1 } else { Get-IntValue $w2 @('migrations') -1 }
    if ($migrations -ne 64) { Add-Red "migrations=$migrations (expected 64)" }

    $w2Head = Get-StringValue $w2 @('gitHead')
    if ($w2Head) { [void](Assert-StageReuse $w2Head 'w2-exit-verdict') }

    $stageEvidenceEntries = $null
    $stageEvidenceLabel = 'stageEvidence'
    foreach ($relative in @("$($script:RunDir)/stageEvidence.json", "$($script:RunDir)/stage-evidence.json",
            "$($script:RunDir)/w2/stageEvidence.json", "$($script:RunDir)/w2/stage-evidence.json")) {
        $candidate = Resolve-FaPath $relative
        if ($null -eq $candidate) { continue }
        $stageEvidence = Read-JsonOrRed $candidate $relative
        if ($null -ne $stageEvidence) {
            $stageEvidenceEntries = Get-Value $stageEvidence @('stages', 'stageEvidence')
            $stageEvidenceLabel = $relative
        }
        break
    }
    if ($null -eq $stageEvidenceEntries) {
        $runMetaPath = Resolve-FaPath "$($script:RunDir)/run-meta.json"
        if ($null -ne $runMetaPath) {
            $runMeta = Read-JsonOrRed $runMetaPath 'run-meta.json'
            if ($null -ne $runMeta) {
                $stageEvidenceEntries = Get-Value $runMeta @('stageEvidence')
                if ($null -ne $stageEvidenceEntries) { $stageEvidenceLabel = 'run-meta.stageEvidence' }
            }
        }
    }
    if ($null -eq $stageEvidenceEntries) {
        Add-Red 'stageEvidence missing (cannot verify stage verdict HEAD reuse rules)'
    }
    else {
        Assert-StageEvidence $stageEvidenceEntries $stageEvidenceLabel
    }
}

# ===========================================================================
# Gate: exploration
# ===========================================================================
# Exploration evidence may include element screenshots (a card/panel), not only
# full-page captures. The spec requires a non-empty screenshot; the full-page
# >=800x600 rule belongs to the visual-review gate. Evidence screenshots only
# need a real, non-blank PNG of at least element size.
function Assert-EvidencePng {
    param([string]$Path, [string]$Label = 'evidence')
    if ([string]::IsNullOrWhiteSpace($Path) -or -not (Test-Path -LiteralPath $Path)) {
        Add-Red "${Label} screenshot missing: $Path"
        return
    }
    $bytes = [IO.File]::ReadAllBytes($Path)
    if ($bytes.Length -lt 33) { Add-Red "${Label} is not a PNG: $Path"; return }
    $signature = [byte[]](0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A)
    for ($i = 0; $i -lt 8; $i++) {
        if ($bytes[$i] -ne $signature[$i]) { Add-Red "${Label} has a bad PNG signature: $Path"; return }
    }
    $width = ([int]$bytes[16] -shl 24) -bor ([int]$bytes[17] -shl 16) -bor ([int]$bytes[18] -shl 8) -bor [int]$bytes[19]
    $height = ([int]$bytes[20] -shl 24) -bor ([int]$bytes[21] -shl 16) -bor ([int]$bytes[22] -shl 8) -bor [int]$bytes[23]
    if ($width -lt 200 -or $height -lt 100) { Add-Red "${Label} screenshot too small: $Path ${width}x${height} (need >=200x100)" }
    try {
        Add-Type -AssemblyName System.Drawing -ErrorAction SilentlyContinue
        $bitmap = [System.Drawing.Bitmap]::FromFile($Path)
        try {
            $colors = New-Object 'System.Collections.Generic.HashSet[int]'
            $stepX = [Math]::Max(1, [int]($bitmap.Width / 32))
            $stepY = [Math]::Max(1, [int]($bitmap.Height / 32))
            for ($x = 0; $x -lt $bitmap.Width; $x += $stepX) {
                for ($y = 0; $y -lt $bitmap.Height; $y += $stepY) { [void]$colors.Add($bitmap.GetPixel($x, $y).ToArgb()) }
            }
            if ($colors.Count -lt 4) { Add-Red "${Label} screenshot looks blank: $Path (uniqueColors=$($colors.Count))" }
        }
        finally { $bitmap.Dispose() }
    }
    catch { Add-Red "${Label} screenshot could not be decoded: $Path" }
}

function Invoke-GateExploration {
    $ref = Get-EvidenceJson "$($script:RunDir)/exploration/ref.json" 'exploration/ref.json'
    if ($null -eq $ref) {
        $ref = Get-EvidenceJson "$($script:RunDir)/w2/exploration-verdict.json" 'w2/exploration-verdict.json'
    }
    if ($null -eq $ref) { return }

    $status = Get-StringValue $ref @('status')
    if ($status -ne 'PASS') { Add-Red "exploration.status='$status' (expected PASS)" }
    if (@(Get-Iarray (Get-Value $ref @('red'))).Count -gt 0) { Add-Red 'exploration.red is not empty' }

    $before = Get-Value $ref @('currentWaveBefore', 'currentWaveCaseRunIdsBefore')
    $after = Get-Value $ref @('currentWaveAfter', 'currentWaveCaseRunIdsAfter')
    if ($null -eq $before -or $null -eq $after) {
        Add-Red 'currentWaveBefore/After missing'
    }
    else {
        $beforeList = @(Get-Iarray $before | ForEach-Object { [string]$_ } | Sort-Object)
        $afterList = @(Get-Iarray $after | ForEach-Object { [string]$_ } | Sort-Object)
        if (($beforeList -join '|') -ne ($afterList -join '|')) {
            Add-Red ('currentWaveBefore != currentWaveAfter (' + ($beforeList -join ',') + ' vs ' + ($afterList -join ',') + ')')
        }
    }

    if (-not [bool](Get-Value $ref @('nextWaveSnapshotContainsU') $false)) { Add-Red 'nextWaveSnapshotContainsU is not true' }
    if (-not [bool](Get-Value $ref @('nextWaveSnapshotContainsA') $false)) { Add-Red 'nextWaveSnapshotContainsA is not true' }

    $coverageBefore = Get-IntValue $ref @('coverageBeforeUncovered') -1
    $coverageAfter = Get-IntValue $ref @('coverageAfterCovered') -1
    if ($coverageBefore -lt 1) { Add-Red "coverageBeforeUncovered=$coverageBefore (need >= 1)" }
    if ($coverageAfter -lt 1) { Add-Red "coverageAfterCovered=$coverageAfter (need >= 1)" }

    $idempotency = Get-Value $ref @('idempotency')
    $replayed = if ($null -ne $idempotency) { [bool](Get-Value $idempotency @('replayed') $false) } else { [bool](Get-Value $ref @('replayed') $false) }
    if (-not $replayed) { Add-Red 'idempotency.replayed is not true' }

    $shots = @(Get-Iarray (Get-Value $ref @('screenshots')))
    if ($shots.Count -lt 5) { Add-Red "exploration.screenshots=$($shots.Count) (need >= 5)" }
    foreach ($shot in $shots) {
        $shotPath = Resolve-FaPath ([string]$shot)
        if ($null -eq $shotPath) { $shotPath = [string]$shot }
        Assert-EvidencePng -Path $shotPath -Label 'exploration'
    }
}

# ===========================================================================
# Gate: visual
# ===========================================================================
function Invoke-GateVisual {
    $review = Get-EvidenceJson "$($script:RunDir)/visual/review.json" 'visual/review.json'
    if ($null -eq $review) { return }

    $reviewer = Get-StringValue $review @('reviewer')
    if ([string]::IsNullOrWhiteSpace($reviewer)) { Add-Red 'visual.reviewer missing' }

    $requiredStages = @('prep', 'clarifying', 'requirement_review', 'designing', 'executing', 'reporting', 'completed', 'issues')
    $stages = @(Get-Iarray (Get-Value $review @('stages')))
    $stageIds = New-Object System.Collections.Generic.List[string]
    $stageShots = @{}
    foreach ($stage in $stages) {
        $name = if ($stage -is [string]) { [string]$stage } else { Get-StringValue $stage @('name', 'stage') }
        if ([string]::IsNullOrWhiteSpace($name)) { Add-Red 'visual.stages[] entry without a name'; continue }
        $stageIds.Add($name)
        $shots = @()
        if ($stage -isnot [string]) { $shots = @(Get-Iarray (Get-Value $stage @('screenshots'))) }
        if ($shots.Count -eq 0) {
            $topShots = @(Get-Iarray (Get-Value $review @('screenshots')))
            foreach ($top in $topShots) {
                if ($top -is [string]) { if ($top -match [regex]::Escape($name)) { $shots += $top } }
                else {
                    $topStage = Get-StringValue $top @('stage', 'name')
                    if ($topStage -eq $name) { $shots += (Get-StringValue $top @('path', 'file')) }
                }
            }
        }
        $stageShots[$name] = @($shots)
    }
    foreach ($problem in @(Assert-ExactSetStrings -Actual @($stageIds) -Expected $requiredStages -Label 'visual.stages')) { Add-Red $problem }
    foreach ($stage in $requiredStages) {
        $shots = if ($stageShots.ContainsKey($stage)) { @($stageShots[$stage]) } else { @() }
        if ($shots.Count -eq 0) { Add-Red "visual stage $stage has no screenshot" }
        else { Assert-Screenshots $shots "visual stage $stage" }
    }

    $allShots = @(Get-Iarray (Get-Value $review @('screenshots')))
    if ($allShots.Count -eq 0) {
        foreach ($stage in $requiredStages) {
            if ($stageShots.ContainsKey($stage)) { $allShots += @($stageShots[$stage]) }
        }
    }
    $uniqueShots = @($allShots | ForEach-Object { [string]$_ } | Sort-Object -Unique)
    if ($uniqueShots.Count -eq 0) { Add-Red 'visual.screenshots is empty' }
    else { Assert-Screenshots $uniqueShots 'visual' }

    $reviewed = Get-IntValue $review @('reviewedScreenshots') -1
    if ($reviewed -ne $uniqueShots.Count) { Add-Red "visual.reviewedScreenshots=$reviewed != screenshots=$($uniqueShots.Count)" }

    $vision = Get-Value $review @('vision')
    if ($null -eq $vision) { Add-Red 'visual.vision missing' }
    else {
        $visionModel = Get-StringValue $vision @('model')
        if ([string]::IsNullOrWhiteSpace($visionModel)) { Add-Red 'visual.vision.model missing' }
        if (-not [bool](Get-Value $vision @('visionExercised') $false)) { Add-Red 'visual.vision.visionExercised is not true' }
        $baseUrl = Get-StringValue $vision @('baseUrl')
        if ([string]::IsNullOrWhiteSpace($baseUrl)) { Add-Red 'visual.vision.baseUrl missing' }
        elseif ($baseUrl -match '^(https?://)?(localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1\])(:\d+)?(/|$)') {
            Add-Red "visual.vision.baseUrl points at a local proxy: $baseUrl"
        }
    }

    $defects = @(Get-Iarray (Get-Value $review @('defects')))
    foreach ($defect in $defects) {
        $severity = (Get-StringValue $defect @('severity')).ToUpperInvariant()
        if (@('P0', 'P1', 'P2') -notcontains $severity) { Add-Red "visual defect $([string](Get-Value $defect @('id') '')) severity='$severity' is not P0/P1/P2" }
        elseif ($severity -eq 'P0') { Add-Red "visual defect $([string](Get-Value $defect @('id') '')) is P0 (must be zero)" }
    }
}

# ===========================================================================
# Gate: run1
# ===========================================================================
function Invoke-GateRun1 {
    $p0Path = Resolve-FaPath 'run/p0.json'
    if ($null -eq $p0Path) { Add-Red 'missing run/p0.json' }
    else {
        $p0 = Read-JsonOrRed $p0Path 'run/p0.json'
        if ($null -ne $p0) {
            $open = Get-IntValue $p0 @('open') -1
            if ($open -ne 0) { Add-Red "p0.open=$open (expected 0)" }
            foreach ($item in @(Get-Iarray (Get-Value $p0 @('items')))) {
                $itemStatus = (Get-StringValue $item @('status')).ToLowerInvariant()
                if ($itemStatus -eq 'open') { Add-Red "p0 item $([string](Get-Value $item @('id') '')) is still open" }
            }
        }
    }

    $issuesFile = $IssuesPath
    if ([string]::IsNullOrWhiteSpace($issuesFile)) {
        $issuesFile = Join-Path $repoRoot 'docs\version-V0.22\acceptance\issues.md'
    }
    elseif (-not [System.IO.Path]::IsPathRooted($issuesFile)) {
        $issuesFile = Join-Path $repoRoot $issuesFile
    }
    if (-not (Test-Path -LiteralPath $issuesFile)) {
        Add-Red "issues ledger missing: $issuesFile"
        return
    }
    $text = [System.IO.File]::ReadAllText($issuesFile, [System.Text.Encoding]::UTF8)
    if ($text -match 'ISSUE-001') { Add-Red 'issues.md still contains the ISSUE-001 placeholder' }

    $rows = New-Object System.Collections.Generic.List[object]
    foreach ($line in ($text -split "`r?`n")) {
        $trimmed = $line.Trim()
        if (-not $trimmed.StartsWith('|')) { continue }
        if ($trimmed -match '^\|[\s\-:|]+\|$') { continue }
        $cells = @($trimmed.Trim('|') -split '\|' | ForEach-Object { $_.Trim() })
        if ($cells.Count -lt 9) { continue }
        if ($cells[0] -match '^(?i)ID$') { continue }
        $rows.Add([pscustomobject]@{ Id = $cells[0]; Severity = $cells[1]; Status = $cells[7]; Raw = $trimmed })
    }
    foreach ($row in $rows) {
        $severity = $row.Severity.ToUpperInvariant()
        $status = $row.Status.ToLowerInvariant()
        if ($severity -eq 'P0') { Add-Red "issues.md contains P0 row $($row.Id) (P0 must not be in issues.md)" }
        if ($severity -eq 'P1') {
            if (@('fixed', 'deferred') -notcontains $status) { Add-Red "$($row.Id) P1 status='$status' (expected fixed|deferred)" }
            if ($status -eq 'deferred' -and $row.Raw -notmatch 'approved_by\s*[=:]\s*user') {
                Add-Warn "$($row.Id) P1 deferred without approved_by=user (pending user signoff)"
            }
        }
    }
}

# ===========================================================================
# Gate: fix
# ===========================================================================
function Invoke-GateFix {
    if ([string]::IsNullOrWhiteSpace($IssueId)) {
        Add-Red 'fix gate requires -IssueId'
        return
    }
    $record = $null
    $recordBase = Join-Path $script:EvidenceRoot "$($script:RunDir)\fixes\$IssueId"
    foreach ($candidate in @((Join-Path $recordBase 'fix.json'), (Join-Path $recordBase 'record.json'),
            (Join-Path $script:EvidenceRoot "$($script:RunDir)\fixes\$IssueId.json"))) {
        if (Test-Path -LiteralPath $candidate) { $record = Read-JsonOrRed $candidate "fix record $IssueId"; break }
    }
    if ($null -eq $record) {
        $fvPath = Resolve-FaPath 'run/fa-verdict.json'
        if ($null -ne $fvPath) {
            $fv = Read-JsonOrRed $fvPath 'fa-verdict.json'
            if ($null -ne $fv) {
                foreach ($candidate in @(Get-Iarray (Get-Value $fv @('fixes')))) {
                    if ((Get-StringValue $candidate @('issueId', 'id')) -eq $IssueId) { $record = $candidate; break }
                }
            }
        }
    }
    if ($null -eq $record) {
        Add-Red "no fix record for $IssueId (looked in $recordBase and run/fa-verdict.json fixes[])"
        return
    }
    if ((Get-StringValue $record @('issueId', 'id')) -and (Get-StringValue $record @('issueId', 'id')) -ne $IssueId) {
        Add-Red "fix record issueId=$([string](Get-Value $record @('issueId', 'id'))) != $IssueId"
    }

    $commit = Get-StringValue $record @('commit')
    if ([string]::IsNullOrWhiteSpace($commit)) { Add-Red 'fix.commit missing' }
    elseif ($commit -notmatch '^[0-9a-fA-F]{7,40}$') { Add-Red "fix.commit is not a git SHA: '$commit'" }
    else {
        $exists = Invoke-GitCapture @('cat-file', '-e', "$commit^{commit}")
        if ($exists.Exit -ne 0) { Add-Red "fix.commit does not exist: $commit" }
        else {
            $ancestor = Invoke-GitCapture @('merge-base', '--is-ancestor', $commit, $script:Head)
            if ($ancestor.Exit -ne 0) { Add-Red "fix.commit $commit is not an ancestor of HEAD=$($script:Head)" }
            $changedResult = Invoke-GitCapture @('show', '--name-only', '--pretty=format:', $commit)
            foreach ($path in @($changedResult.Output | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })) {
                $normalized = $path -replace '\\', '/'
                if ($normalized -match '^docs/') { Add-Red "fix.commit $commit contains docs/ file: $normalized" }
            }
        }
    }

    $redLog = Get-StringValue $record @('redLog')
    if ([string]::IsNullOrWhiteSpace($redLog)) { $redLog = "$($script:RunDir)/fixes/$IssueId/red.log" }
    $redPath = Assert-FaFile $redLog 'red log'
    $greenLog = Get-StringValue $record @('greenLog')
    if ([string]::IsNullOrWhiteSpace($greenLog)) { $greenLog = "$($script:RunDir)/fixes/$IssueId/green.log" }
    [void](Assert-FaFile $greenLog 'green log')

    if ($null -ne $redPath) {
        $redText = [System.IO.File]::ReadAllText($redPath, [System.Text.Encoding]::UTF8)
        $compilePattern = '(?im)(^|\n)\s*(#.*command-line-arguments|.*\bbuild failed\b|.*\bcompilation failed\b|.*\bcompile error\b|.*\bCompileError\b|.*\bsyntax error\b|.*\berror TS\d+|.*\bcannot find package\b)'
        $match = [regex]::Match($redText, $compilePattern)
        if ($match.Success) { Add-Red "red log looks like a compile error: $($match.Value.Trim())" }
    }

    $reprobe = Get-Value $record @('reprobe')
    if ($reprobe -is [System.Management.Automation.PSCustomObject]) { $reprobe = Get-Value $reprobe @('status') }
    if ([string]$reprobe -ne 'pass') { Add-Red "fix.reprobe='$([string]$reprobe)' (expected pass)" }
}

# ===========================================================================
# Gate: verdict
# ===========================================================================
function Invoke-GateVerdict {
    $fvPath = Resolve-FaPath 'run/fa-verdict.json'
    if ($null -eq $fvPath) { Add-Red 'missing run/fa-verdict.json'; return }
    $fv = Read-JsonOrRed $fvPath 'run/fa-verdict.json'
    if ($null -eq $fv) { return }

    $schemaVersion = Get-IntValue $fv @('schemaVersion') -1
    if ($schemaVersion -ne 1) { Add-Red "fa-verdict.schemaVersion=$schemaVersion (expected 1)" }

    $status = (Get-StringValue $fv @('status')).ToUpperInvariant()
    if (@('PASS', 'FAIL', 'EXTERNAL-BLOCKED', 'PENDING-SIGNOFF') -notcontains $status) {
        Add-Red "fa-verdict.status='$status' is not legal"
    }
    $runId = Get-StringValue $fv @('runId')
    if ([string]::IsNullOrWhiteSpace($runId)) { Add-Red 'fa-verdict.runId missing' }
    $generatedAt = Get-StringValue $fv @('generatedAt')
    if ([string]::IsNullOrWhiteSpace($generatedAt)) { Add-Red 'fa-verdict.generatedAt missing' }

    $gitHead = Get-StringValue $fv @('gitHead')
    if ($gitHead -notmatch '^[0-9a-f]{40}$') { Add-Red "fa-verdict.gitHead is not a 40-char lowercase SHA: '$gitHead'" }
    elseif ([string]::IsNullOrWhiteSpace($script:Head)) { Add-Red 'fa-verdict cannot compare gitHead because git HEAD is unavailable' }
    elseif ($gitHead -ne $script:Head) { Add-Red "fa-verdict.gitHead=$gitHead != HEAD=$($script:Head)" }

    $redItems = @(Get-Iarray (Get-Value $fv @('red')))
    if ($status -eq 'PASS' -and $redItems.Count -gt 0) { Add-Red 'fa-verdict.status=PASS but red is not empty' }
    if ($status -in @('FAIL', 'EXTERNAL-BLOCKED') -and $redItems.Count -eq 0) { Add-Red "fa-verdict.status=$status but red is empty" }

    # Mechanical gate ledger: never trust the aggregate status alone. Every
    # required gate must be recorded, green, and backed by an intact log.
    $gates = @(Get-Iarray (Get-Value $fv @('gates')))
    $requiredGateNames = @('inputs', 'prep', 'harness', 'flow', 'typical', 'acs', 'issues14', 'domains', 'exploration', 'visual', 'run1')
    if ($gates.Count -eq 0) { Add-Red 'fa-verdict.gates is empty' }
    $gateNames = New-Object System.Collections.Generic.List[string]
    foreach ($gateEntry in $gates) {
        $gateName = Get-StringValue $gateEntry @('name')
        if ([string]::IsNullOrWhiteSpace($gateName)) { Add-Red 'fa-verdict.gates[] entry without name'; continue }
        $gateNames.Add($gateName)
        $gateExit = Get-IntValue $gateEntry @('exit') -1
        if ($gateExit -ne 0) { Add-Red "fa-verdict gate '$gateName' exit=$gateExit (expected 0)" }
        $gateLog = Get-StringValue $gateEntry @('log')
        if ([string]::IsNullOrWhiteSpace($gateLog)) { Add-Red "fa-verdict gate '$gateName' has no log" }
        else {
            $gateLogPath = Resolve-FaPath $gateLog
            if ($null -eq $gateLogPath) { Add-Red "fa-verdict gate '$gateName' log missing: $gateLog" }
            else {
                $gateLogSha = Get-StringValue $gateEntry @('sha256')
                if ($gateLogSha -notmatch '^[0-9a-f]{64}$') { Add-Red "fa-verdict gate '$gateName' log sha256 invalid" }
                elseif ($gateLogSha -ne (Get-Sha -Path $gateLogPath)) { Add-Red "fa-verdict gate '$gateName' log sha256 mismatch" }
            }
        }
    }
    foreach ($requiredGate in $requiredGateNames) {
        if ($gateNames -notcontains $requiredGate) { Add-Red "fa-verdict.gates missing '$requiredGate'" }
    }
    $fixRecordsForGates = @(Get-Iarray (Get-Value $fv @('fixes')))
    $fixGateCount = @($gateNames | Where-Object { $_ -eq 'fix' }).Count
    if ($fixRecordsForGates.Count -ne $fixGateCount) { Add-Red "fa-verdict gates fix=$fixGateCount but fixes=$($fixRecordsForGates.Count)" }

    $reuse = Get-Value $fv @('reuse')
    if ($null -eq $reuse) { Add-Red 'fa-verdict.reuse missing' }
    elseif ([bool](Get-Value $reuse @('reused') $false)) { Add-Red 'fa-verdict.reuse.reused is true (final verdict must be fresh)' }

    $inputSet = Get-Value $fv @('inputSet')
    if ($null -eq $inputSet) { Add-Red 'fa-verdict.inputSet missing' }
    else {
        $inputFile = Get-StringValue $inputSet @('file')
        if ([string]::IsNullOrWhiteSpace($inputFile)) { Add-Red 'fa-verdict.inputSet.file missing' }
        else {
            $resolvedInput = Assert-FaFile $inputFile 'inputSet.file'
            $inputSha = Get-StringValue $inputSet @('sha256')
            if ($resolvedInput -and $inputSha) {
                if ($inputSha -ne (Get-Sha -Path $resolvedInput)) { Add-Red 'fa-verdict.inputSet.sha256 does not match the input file' }
            }
            elseif (-not $inputSha) { Add-Red 'fa-verdict.inputSet.sha256 missing' }
        }
        if ((Get-IntValue $inputSet @('requirements') -1) -ne 12) { Add-Red 'fa-verdict.inputSet.requirements != 12' }
        if ((Get-IntValue $inputSet @('acs') -1) -ne 41) { Add-Red 'fa-verdict.inputSet.acs != 41' }
    }

    $requirements = @(Get-Iarray (Get-Value $fv @('requirements')))
    $requirementIds = New-Object System.Collections.Generic.List[string]
    foreach ($requirement in $requirements) {
        $rid = Get-StringValue $requirement @('id')
        if ($rid) { $requirementIds.Add($rid) }
    }
    foreach ($problem in @(Assert-ExactSetStrings -Actual @($requirementIds) -Expected $script:ExpectedRequirementIds -Label 'fa-verdict.requirements')) { Add-Red $problem }

    $acs = @(Get-Iarray (Get-Value $fv @('acs')))
    $acIds = New-Object System.Collections.Generic.List[string]
    foreach ($ac in $acs) {
        $acId = Get-StringValue $ac @('id')
        if ($acId) { $acIds.Add($acId) }
    }
    foreach ($problem in @(Assert-ExactSetStrings -Actual @($acIds) -Expected @($script:ExpectedAcIds) -Label 'fa-verdict.acs')) { Add-Red $problem }

    foreach ($ac in $acs) {
        $acId = Get-StringValue $ac @('id')
        $acStatus = (Get-StringValue $ac @('status')).ToLowerInvariant()
        $groundTruth = (Get-StringValue $ac @('groundTruth')).ToLowerInvariant()
        $eztestVerdict = (Get-StringValue $ac @('eztestVerdict')).ToLowerInvariant()
        if ($acStatus -notin @('pass', 'fail', 'gap', 'blocked')) { Add-Red "$acId status='$acStatus'" }
        if ($groundTruth -notin @('present', 'absent', 'partial')) { Add-Red "$acId groundTruth='$groundTruth'" }
        if ($eztestVerdict -notin @('pass', 'fail', 'gap', 'blocked', 'missing')) { Add-Red "$acId eztestVerdict='$eztestVerdict'" }
        if ($groundTruth -eq 'absent' -and $eztestVerdict -eq 'pass') { Add-Red "FORGED PASS ${acId}: SUT absent but EzTest=pass" }
        if ($groundTruth -eq 'absent' -and $acStatus -eq 'pass') { Add-Red "FORGED PASS ${acId}: SUT absent but status=pass" }
        $probe = Get-Value $ac @('probe')
        if ($null -eq $probe) { Add-Red "$acId probe missing" }
        else {
            $recomputed = (Get-StringValue $probe @('recomputed')).ToLowerInvariant()
            if ($recomputed -ne $acStatus) { Add-Red "$acId status=$acStatus != probe.recomputed=$recomputed" }
        }
        if ([string]::IsNullOrWhiteSpace((Get-StringValue $ac @('observed')))) { Add-Red "$acId observed empty" }
        if ([string]::IsNullOrWhiteSpace((Get-StringValue $ac @('expected')))) { Add-Red "$acId expected empty" }
        if (@(Get-Iarray (Get-Value $ac @('evidence'))).Count -lt 1) { Add-Red "$acId evidence empty" }
    }

    $problems = @(Get-Iarray (Get-Value $fv @('fourteenProblems')))
    $problemIds = New-Object System.Collections.Generic.List[string]
    foreach ($problem in $problems) {
        $id = Get-StringValue $problem @('id')
        if ($id) { $problemIds.Add($id) }
    }
    foreach ($msg in @(Assert-ExactSetStrings -Actual @($problemIds) -Expected @(1..14 | ForEach-Object { [string]$_ }) -Label 'fa-verdict.fourteenProblems')) { Add-Red $msg }
    foreach ($problem in $problems) {
        if (@(Get-Iarray (Get-Value $problem @('assertions'))).Count -lt 1) { Add-Red "fourteenProblems $((Get-StringValue $problem @('id'))) assertions empty" }
    }

    $domains = @(Get-Iarray (Get-Value $fv @('sevenDomains')))
    $domainIds = New-Object System.Collections.Generic.List[string]
    foreach ($domain in $domains) {
        $id = Get-StringValue $domain @('domain')
        if ($id) { $domainIds.Add($id) }
        $verdict = Get-StringValue $domain @('verdict')
        if ($verdict -ne 'PASS') { Add-Red "sevenDomains $id verdict='$verdict' (expected PASS)" }
    }
    foreach ($problem in @(Assert-ExactSetStrings -Actual @($domainIds) -Expected @('human', 'issue', 'export', 'code', 'visual', 'eval', 'gc') -Label 'fa-verdict.sevenDomains')) { Add-Red $problem }

    $visualPayload = Get-Value $fv @('visual')
    if ($null -eq $visualPayload) { Add-Red 'fa-verdict.visual missing' }
    else {
        if ([string]::IsNullOrWhiteSpace((Get-StringValue $visualPayload @('reviewer')))) { Add-Red 'fa-verdict.visual.reviewer is empty' }
        $visualShots = @(Get-Iarray (Get-Value $visualPayload @('screenshots')))
        $reviewedCount = Get-IntValue $visualPayload @('reviewedScreenshots') -1
        if ($reviewedCount -ne $visualShots.Count) { Add-Red "visual.reviewedScreenshots=$reviewedCount != screenshots=$($visualShots.Count)" }
        $visualVision = Get-Value $visualPayload @('vision')
        if ($null -eq $visualVision -or -not [bool](Get-Value $visualVision @('visionExercised') $false)) { Add-Red 'fa-verdict.visual.vision.visionExercised is not true' }
    }

    $evidence = Get-Value $fv @('evidence')
    if ($null -eq $evidence) { Add-Red 'fa-verdict.evidence missing' }
    else {
        $files = @(Get-Iarray (Get-Value $evidence @('files')))
        if ($files.Count -eq 0) { Add-Red 'fa-verdict.evidence.files is empty' }
        foreach ($file in $files) {
            $path = Get-StringValue $file @('path')
            if ([string]::IsNullOrWhiteSpace($path)) { Add-Red 'evidence.files[] entry without path'; continue }
            $resolved = Resolve-FaPath $path
            if ($null -eq $resolved) { Add-Red "evidence file missing: $path"; continue }
            $bytes = Get-IntValue $file @('bytes') -1
            if ($bytes -ne (Get-Item -LiteralPath $resolved).Length) { Add-Red "evidence bytes mismatch: $path ($bytes vs $((Get-Item -LiteralPath $resolved).Length))" }
            $sha = Get-StringValue $file @('sha256')
            if ($sha -notmatch '^[0-9a-f]{64}$') { Add-Red "evidence sha256 invalid: $path" }
            elseif ($sha -ne (Get-Sha -Path $resolved)) { Add-Red "evidence sha256 mismatch: $path" }
        }
        $claims = @(Get-Iarray (Get-Value $evidence @('claims')))
        if ($claims.Count -lt 1) { Add-Red 'fa-verdict.evidence.claims is empty (no traceable claim)' }
    }

    Assert-StageEvidence (Get-Value $fv @('stageEvidence')) 'fa-verdict'

    $signoff = Get-Value $fv @('signoff')
    if ($null -eq $signoff) { Add-Red 'fa-verdict.signoff missing' }
    else {
        $userConfirmed = [bool](Get-Value $signoff @('userConfirmed') $false)
        if ($status -eq 'PASS' -and -not $userConfirmed) { Add-Red 'fa-verdict.status=PASS but signoff.userConfirmed is not true' }
    }

    if ($status -eq 'FAIL') { Add-Red 'fa-verdict.status=FAIL (acceptance not passed)' }
    elseif ($status -eq 'EXTERNAL-BLOCKED') {
        Add-Blocked ('fa-verdict.status=EXTERNAL-BLOCKED: ' + (($redItems | ForEach-Object { [string]$_ }) -join '; '))
    }
    elseif ($status -eq 'PENDING-SIGNOFF') {
        Add-Warn 'fa-verdict.status=PENDING-SIGNOFF (awaiting user signoff or P1 approval; not PASS)'
        $script:PassSuffix = 'schema valid; status=PENDING-SIGNOFF (not PASS)'
    }
}

# ===========================================================================
# Gate: report
# ===========================================================================
function Invoke-GateReport {
    $fvPath = Resolve-FaPath 'run/fa-verdict.json'
    $fv = $null
    if ($null -eq $fvPath) { Add-Red 'missing run/fa-verdict.json (report cannot be validated)' }
    else { $fv = Read-JsonOrRed $fvPath 'run/fa-verdict.json' }

    $reportFile = $ReportPath
    if ([string]::IsNullOrWhiteSpace($reportFile)) {
        $candidate = Join-Path $repoRoot 'docs\version-V0.22\FINAL-REPORT.md'
        if (Test-Path -LiteralPath $candidate) { $reportFile = $candidate }
        else { $reportFile = Join-Path $script:EvidenceRoot 'FINAL-REPORT.md' }
    }
    elseif (-not [System.IO.Path]::IsPathRooted($reportFile)) {
        $resolvedReport = Resolve-FaPath $reportFile
        if ($null -ne $resolvedReport) { $reportFile = $resolvedReport }
        else { $reportFile = Join-Path $repoRoot $reportFile }
    }
    if (-not (Test-Path -LiteralPath $reportFile)) {
        Add-Red "FINAL-REPORT.md missing: $reportFile"
        return
    }
    $reportBytes = (Get-Item -LiteralPath $reportFile).Length
    if ($reportBytes -lt 4000) { Add-Red "FINAL-REPORT.md too short: $reportBytes bytes (need >= 4000)" }
    $text = [System.IO.File]::ReadAllText($reportFile, [System.Text.Encoding]::UTF8)

    $sections = [ordered]@{
        '12 requirements' = '(?i)12\s*(条)?\s*(requirements?|需求)'
        '14 problems' = '(?i)14\s*(个)?\s*(problems?|问题)'
        'seven domains' = '(?i)(seven\s+domains|7\s*(个)?\s*(域|domains?)|七\s*个?\s*域)'
        'exploration' = '(?i)(exploration|探索)'
        'visual' = '(?i)(visual|视觉)'
        'residual risk' = '(?i)(residual\s*risk|残余风险)'
        'evidence index' = '(?i)(evidence\s*index|证据索引)'
        'signoff' = '(?i)(signoff|sign-off|签字)'
    }
    foreach ($section in $sections.Keys) {
        if (-not [regex]::IsMatch($text, $sections[$section])) { Add-Red "FINAL-REPORT.md missing required sections: $section" }
    }
    $placeholder = [regex]::Match($text, '(?i)\bTBD\b|\bTODO\b|待填|\bFIXME\b')
    if ($placeholder.Success) { Add-Red "FINAL-REPORT.md contains TBD|TODO placeholder: $($placeholder.Value)" }
    if ($text -notmatch '(?i)fa-verdict\.json') { Add-Red 'FINAL-REPORT.md does not reference run/fa-verdict.json' }

    $indexPath = Join-Path $script:EvidenceRoot 'INDEX.md'
    if (-not (Test-Path -LiteralPath $indexPath)) {
        Add-Red 'missing INDEX.md'
    }
    elseif ($null -ne $fv) {
        $indexText = [System.IO.File]::ReadAllText($indexPath, [System.Text.Encoding]::UTF8)
        $missing = New-Object System.Collections.Generic.List[string]
        foreach ($file in @(Get-Iarray (Get-Value (Get-Value $fv @('evidence')) @('files')))) {
            $path = Get-StringValue $file @('path')
            if ([string]::IsNullOrWhiteSpace($path)) { continue }
            $sha = Get-StringValue $file @('sha256')
            if (-not $indexText.Contains($path) -and -not $indexText.Contains(($path -replace '/', '\'))) {
                $missing.Add("path $path")
            }
            if ($sha -and -not $indexText.Contains($sha)) { $missing.Add("sha256 $sha") }
        }
        if ($missing.Count -gt 0) {
            $shown = @($missing | Select-Object -First 10)
            Add-Red ('INDEX.md does not cover evidence.files: ' + ($shown -join ', ') + $(if ($missing.Count -gt 10) { " (+$($missing.Count - 10) more)" } else { '' }))
        }
    }

    if ($null -ne $fv) {
        $status = (Get-StringValue $fv @('status')).ToUpperInvariant()
        $signoff = Get-Value $fv @('signoff')
        $confirmed = if ($null -ne $signoff) { [bool](Get-Value $signoff @('userConfirmed') $false) } else { $false }
        if ($status -eq 'PASS' -and -not $confirmed) { Add-Red 'FINAL-REPORT.md says PASS but signoff.userConfirmed is not true' }
        if ($status -eq 'PENDING-SIGNOFF') { Add-Warn 'fa-verdict.status=PENDING-SIGNOFF (report may be signed later)' }
    }
}

# ===========================================================================
# Dispatch
# ===========================================================================
switch ($Gate) {
    'inputs' { Invoke-GateInputs; Complete-Gate 'inputs' '12 requirements / 41 ACs, sha256 bound, no leak markers' }
    'prep' { Invoke-GatePrep; Complete-Gate 'prep' 'fresh eztest_v022_fa stack, real vision caption, frozen inputs' }
    'harness' { Invoke-GateHarness; Complete-Gate 'harness' "$($script:RunDir) run-meta bound to HEAD and fresh" }
    'flow' { Invoke-GateFlow; Complete-Gate 'flow' '7 workflow stages + completed, DB counters, deepseek-flash' }
    'typical' { Invoke-GateTypical; Complete-Gate 'typical' '12 requirements, cases, runs, screenshots, verdict enum' }
    'acs' { Invoke-GateAcs; Complete-Gate 'acs' "range $($script:Range) exact AC set, probe truth, derived requirements" }
    'issues14' { Invoke-GateIssues14; Complete-Gate 'issues14' '14 rows, fresh W0 PASS, independent assertions' }
    'domains' { Invoke-GateDomains; Complete-Gate 'domains' '20 gates, 7 domains, contract 214/264/322, migrations 64' }
    'exploration' { Invoke-GateExploration; Complete-Gate 'exploration' 'current wave unchanged, next wave U/A, coverage, replay' }
    'visual' { Invoke-GateVisual; Complete-Gate 'visual' '8 stages, real vision, non-blank PNGs, no P0' }
    'run1' { Invoke-GateRun1; Complete-Gate 'run1' 'P0 open=0, no placeholder, P1 terminal states' }
    'fix' { Invoke-GateFix; Complete-Gate 'fix' "fix record for $IssueId (red/green/reprobe/commit)" }
    'verdict' { Invoke-GateVerdict; Complete-Gate 'verdict' 'schema v1, sets 12/41/14/7, sha256, stage reuse' }
    'report' { Invoke-GateReport; Complete-Gate 'report' 'FINAL-REPORT + INDEX coverage + signoff' }
    default { Add-Red "unknown gate: $Gate"; Complete-Gate $Gate '' }
}
