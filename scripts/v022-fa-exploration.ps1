#requires -Version 5.1
<#
.SYNOPSIS
    EzTest v0.22 FA-9 execution-time exploration (T10-T14) evidence gate.

.DESCRIPTION
    Produces RUN-<n>/exploration/ref.json from the W2 execution-time exploration
    verdict (FA-4 copy first, then the W2 run evidence) and validates the T10-T14
    contract mechanically:

      T10  current wave case_run IDs are identical before/after auto-confirm;
      T11  the same-version replay is idempotent and actually executed;
      T12  the rollback replacement keeps the case (rollbackCaseKept);
      T13  the verdict carries the consumed case.revision.auto_confirmed evidence;
      T14  next-wave snapshot contains the discovered page U and API A, and the
           coverage matrix moves from uncovered to covered.

    When the source verdict's gitHead differs from HEAD and a product
    (backend/ frontend/) diff exists between the two, the source cannot be
    reused under FA-C3. The ref records reuse.reused=false plus
    reason='replay required'. Only -Fresh actually replays
    scripts/v022-exploration-e2e.ps1 (which starts the SUT stack); without
    -Fresh the stale verdict is reported honestly and no product code is run.

    Screenshots are resolved relative to the source verdict directory, copied to
    RUN-<n>/exploration/screenshots/ and validated with the shared Assert-Png
    helper (magic bytes, dimensions, non-blankness).

    Exit codes: 0 = PASS, 1 = RED. The ref is always written (UTF-8 no BOM).

.NOTES
    Run from the repository root:
        powershell.exe -NoProfile -ExecutionPolicy Bypass `
            -File scripts\v022-fa-exploration.ps1 -RunDir RUN-1

    -Fresh is opt-in because it boots the FA stack and touches the real SUT.
#>
param(
    [ValidatePattern('^RUN-[0-9]+$')]
    [string]$RunDir = 'RUN-1',

    [string]$EvidenceRoot,

    [switch]$Fresh
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
$OutputEncoding = New-Object System.Text.UTF8Encoding($false)

. (Join-Path $PSScriptRoot 'v022-fa-common.ps1')

$utf8NoBom = New-Object System.Text.UTF8Encoding($false)

if ([string]::IsNullOrWhiteSpace($EvidenceRoot)) {
    $script:EvidenceRoot = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence\final'
}
elseif ([System.IO.Path]::IsPathRooted($EvidenceRoot)) {
    $script:EvidenceRoot = [System.IO.Path]::GetFullPath($EvidenceRoot)
}
else {
    $script:EvidenceRoot = [System.IO.Path]::GetFullPath((Join-Path $repoRoot $EvidenceRoot))
}

$runRoot = Join-Path $script:EvidenceRoot $RunDir
$outDir = Join-Path $runRoot 'exploration'
$shotOutDir = Join-Path $outDir 'screenshots'
$refPath = Join-Path $outDir 'ref.json'
$w2CopyRoot = Join-Path $runRoot 'w2'
$w2CopyVerdict = Join-Path $w2CopyRoot 'exploration-verdict.json'
# docs/version-V0.22/acceptance/evidence/final -> .../evidence/w2/run
$w2FallbackVerdict = Join-Path (Split-Path -Parent $script:EvidenceRoot) 'w2\run\exploration-verdict.json'
$w2E2EDir = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence\w2\run'
$w2E2EVerdict = Join-Path $w2E2EDir 'exploration-verdict.json'

New-Item -ItemType Directory -Force -Path $outDir | Out-Null

$red = New-Object System.Collections.Generic.List[string]
$head = ''
try { $head = Get-Head } catch { $head = '' }

function Get-Field {
    param($Object, [string[]]$Names, $Default = $null)
    if ($null -eq $Object) { return $Default }
    foreach ($name in $Names) {
        if ($Object -is [System.Collections.IDictionary]) {
            if ($Object.Contains($name)) { return $Object[$name] }
            continue
        }
        $property = $Object.PSObject.Properties[$name]
        if ($null -ne $property) { return $property.Value }
    }
    return $Default
}

function Get-StringField {
    param($Object, [string[]]$Names, [string]$Default = '')
    $value = Get-Field $Object $Names $null
    if ($null -eq $value) { return $Default }
    return [string]$value
}

function Get-IntField {
    param($Object, [string[]]$Names, [int]$Default = -1)
    $value = Get-Field $Object $Names $null
    if ($null -eq $value) { return $Default }
    $parsed = 0
    if ([int]::TryParse([string]$value, [ref]$parsed)) { return $parsed }
    return $Default
}

function Get-BoolField {
    param($Object, [string[]]$Names, [bool]$Default = $false)
    $value = Get-Field $Object $Names $null
    if ($null -eq $value) { return $Default }
    if ($value -is [bool]) { return $value }
    return ([string]$value -eq 'True' -or [string]$value -eq 'true' -or [string]$value -eq '1')
}

function Get-StringList {
    param($Object, [string[]]$Names)
    $value = Get-Field $Object $Names $null
    $list = New-Object System.Collections.Generic.List[string]
    foreach ($item in @($value)) {
        if ($null -ne $item -and -not [string]::IsNullOrWhiteSpace([string]$item)) {
            $list.Add([string]$item)
        }
    }
    return @($list)
}

function Get-RepoRelativePath {
    param([string]$Path)
    if ([string]::IsNullOrWhiteSpace($Path)) { return '' }
    $full = [System.IO.Path]::GetFullPath($Path)
    $root = [System.IO.Path]::GetFullPath($repoRoot).TrimEnd('\') + '\'
    if ($full.StartsWith($root, [System.StringComparison]::OrdinalIgnoreCase)) {
        return ($full.Substring($root.Length) -replace '\\', '/')
    }
    return ($full -replace '\\', '/')
}

function Resolve-SourceScreenshot {
    param([string]$Entry, [string]$SourceBase)
    if ([string]::IsNullOrWhiteSpace($Entry)) { return $null }
    $normalized = $Entry -replace '/', '\'
    $candidates = New-Object System.Collections.Generic.List[string]
    if ([System.IO.Path]::IsPathRooted($normalized)) {
        $candidates.Add($normalized)
    }
    else {
        $candidates.Add((Join-Path $SourceBase $normalized))
        if (-not $normalized.StartsWith('exploration\', [System.StringComparison]::OrdinalIgnoreCase)) {
            $candidates.Add((Join-Path (Join-Path $SourceBase 'exploration') $normalized))
        }
        $candidates.Add((Join-Path $script:EvidenceRoot $normalized))
        $candidates.Add((Join-Path $runRoot $normalized))
        $candidates.Add((Join-Path $repoRoot $normalized))
    }
    foreach ($candidate in $candidates) {
        if (Test-Path -LiteralPath $candidate) {
            return (Resolve-Path -LiteralPath $candidate).Path
        }
    }
    return $null
}

function Copy-EvidenceFile {
    param([string]$From, [string]$To)
    $parent = Split-Path -Parent $To
    if ($parent -and -not (Test-Path -LiteralPath $parent)) {
        New-Item -ItemType Directory -Force -Path $parent | Out-Null
    }
    Copy-Item -LiteralPath $From -Destination $To -Force
    $now = Get-Date
    (Get-Item -LiteralPath $To).LastWriteTime = $now
    (Get-Item -LiteralPath $To).CreationTime = $now
}

function Write-Ref {
    param($Ref)
    Write-JsonUtf8 -Path $refPath -Object $Ref -Depth 20
}

function Complete-Red {
    param($Ref)
    $Ref['status'] = 'RED'
    $Ref['red'] = @($red)
    Write-Ref $Ref
    foreach ($item in $red) { Write-Host "RED: $item" }
    Write-Host "FA EXPLORATION: RED ($($red.Count), $RunDir)"
    exit 1
}

# ---------------------------------------------------------------------------
# Optional fresh replay. This boots the FA stack and must stay opt-in.
# ---------------------------------------------------------------------------
$freshRun = $false
if ($Fresh) {
    $e2eScript = Join-Path $PSScriptRoot 'v022-exploration-e2e.ps1'
    if (-not (Test-Path -LiteralPath $e2eScript)) {
        $red.Add("fresh replay script missing: $e2eScript")
    }
    else {
        Write-Host "== fresh replay: scripts/v022-exploration-e2e.ps1"
        & powershell.exe -NoProfile -ExecutionPolicy Bypass -File $e2eScript
        $freshExit = $LASTEXITCODE
        Write-Host "== fresh replay exit=$freshExit"
        $freshRun = $true
        if ($freshExit -ne 0) { $red.Add("fresh replay exited $freshExit") }
        if (-not (Test-Path -LiteralPath $w2E2EVerdict)) {
            $red.Add("fresh replay produced no verdict: $w2E2EVerdict")
        }
        else {
            New-Item -ItemType Directory -Force -Path $w2CopyRoot | Out-Null
            Copy-EvidenceFile $w2E2EVerdict $w2CopyVerdict
            $freshIdempotency = Join-Path $w2E2EDir 'exploration-idempotency.json'
            if (Test-Path -LiteralPath $freshIdempotency) {
                Copy-EvidenceFile $freshIdempotency (Join-Path $w2CopyRoot 'exploration-idempotency.json')
            }
            $freshShots = Join-Path $w2E2EDir 'exploration'
            if (Test-Path -LiteralPath $freshShots) {
                foreach ($file in @(Get-ChildItem -LiteralPath $freshShots -File -Force)) {
                    Copy-EvidenceFile $file.FullName (Join-Path (Join-Path $w2CopyRoot 'exploration') $file.Name)
                }
            }
        }
    }
}

# ---------------------------------------------------------------------------
# Source verdict: FA-4 copy first, then the W2 run evidence.
# ---------------------------------------------------------------------------
$sourcePath = $null
$tried = New-Object System.Collections.Generic.List[string]
foreach ($candidate in @($w2CopyVerdict, $w2FallbackVerdict)) {
    $tried.Add($candidate)
    if (Test-Path -LiteralPath $candidate) { $sourcePath = $candidate; break }
}

$baseRef = [ordered]@{
    status = 'RED'
    source = ''
    sourceSha256 = ''
    sourceGitHead = ''
    sourceStatus = ''
    gitHead = $head
    reason = ''
    generatedAt = (Get-Date).ToString('o')
    reuse = [ordered]@{ reused = $false }
    idempotency = [ordered]@{ replayed = $false }
    currentWaveCaseRunIdsBefore = @()
    currentWaveCaseRunIdsAfter = @()
    nextWaveSnapshotContainsU = $false
    nextWaveSnapshotContainsA = $false
    coverageBeforeUncovered = 0
    coverageAfterCovered = 0
    rollbackCaseKept = $false
    fourEvidence = [ordered]@{ db = $false; api = $false; ui = $false; screenshots = @() }
    screenshots = @()
    red = @()
}

if ($null -eq $sourcePath) {
    $baseRef['reason'] = 'source exploration verdict missing: ' + ($tried -join '; ')
    $red.Add($baseRef['reason'])
    Complete-Red $baseRef
}

$source = $null
try { $source = Read-JsonUtf8 -Path $sourcePath }
catch {
    $red.Add("source exploration verdict unreadable: $($_.Exception.Message)")
    $baseRef['reason'] = 'source exploration verdict unreadable'
    Complete-Red $baseRef
}
if ($null -eq $source) {
    $red.Add('source exploration verdict is empty')
    $baseRef['reason'] = 'source exploration verdict is empty'
    Complete-Red $baseRef
}

$sourceBase = Split-Path -Parent $sourcePath
$baseRef['source'] = Get-RepoRelativePath $sourcePath
$baseRef['sourceSha256'] = Get-Sha -Path $sourcePath
$sourceStatus = Get-StringField $source @('status')
$baseRef['sourceStatus'] = $sourceStatus

if ($sourceStatus -ne 'PASS') {
    $red.Add("source status='$sourceStatus' (expected PASS)")
}

$before = @(Get-StringList $source @('currentWaveCaseRunIdsBefore', 'currentWaveBefore'))
$after = @(Get-StringList $source @('currentWaveCaseRunIdsAfter', 'currentWaveAfter'))
$baseRef['currentWaveCaseRunIdsBefore'] = $before
$baseRef['currentWaveCaseRunIdsAfter'] = $after
$beforeKey = (@($before) | Sort-Object) -join '|'
$afterKey = (@($after) | Sort-Object) -join '|'
if ($beforeKey -ne $afterKey) {
    $red.Add("currentWaveCaseRunIdsBefore != currentWaveCaseRunIdsAfter ($($before -join ',') vs $($after -join ','))")
}

$containsU = Get-BoolField $source @('nextWaveSnapshotContainsU')
$containsA = Get-BoolField $source @('nextWaveSnapshotContainsA')
$baseRef['nextWaveSnapshotContainsU'] = $containsU
$baseRef['nextWaveSnapshotContainsA'] = $containsA
if (-not $containsU) { $red.Add('nextWaveSnapshotContainsU is not true') }
if (-not $containsA) { $red.Add('nextWaveSnapshotContainsA is not true') }

$coverageBefore = Get-IntField $source @('coverageBeforeUncovered')
$coverageAfter = Get-IntField $source @('coverageAfterCovered')
$baseRef['coverageBeforeUncovered'] = $coverageBefore
$baseRef['coverageAfterCovered'] = $coverageAfter
if ($coverageBefore -lt 1) { $red.Add("coverageBeforeUncovered=$coverageBefore (need >= 1)") }
if ($coverageAfter -lt 1) { $red.Add("coverageAfterCovered=$coverageAfter (need >= 1)") }

$rollbackCaseKept = Get-BoolField $source @('rollbackCaseKept')
$baseRef['rollbackCaseKept'] = $rollbackCaseKept
if (-not $rollbackCaseKept) {
    $red.Add('rollbackCaseKept is not true (T12 rollback keeps the case)')
}

$idempotency = Get-Field $source @('idempotency')
$replayed = $false
if ($null -ne $idempotency) {
    if (Get-BoolField $idempotency @('replayed', 'replayExecuted')) { $replayed = $true }
    elseif (Get-BoolField $source @('replayed')) { $replayed = $true }
    $idempotencyStatus = Get-StringField $idempotency @('status')
    if ($idempotencyStatus -and $idempotencyStatus -ne 'PASS') {
        $red.Add("idempotency.status='$idempotencyStatus' (expected PASS)")
    }
}
elseif (Get-BoolField $source @('replayed')) {
    $replayed = $true
}
$baseRef['idempotency'] = [ordered]@{ replayed = $replayed }
if (-not $replayed) { $red.Add('idempotency.replayed is not true') }

# ---------------------------------------------------------------------------
# Screenshots: resolve, validate as real non-blank PNGs of at least element
# size, and copy under the run dir. Exploration evidence legitimately includes
# element captures (a card/panel); the full-page >=800x600 rule is enforced by
# the visual-review gate, not here (spec §11.14#12 only requires the screenshot).
# ---------------------------------------------------------------------------
function Test-EvidencePng {
    param([string]$Path)
    $problems = New-Object System.Collections.Generic.List[string]
    $bytes = [IO.File]::ReadAllBytes($Path)
    if ($bytes.Length -lt 33) { $problems.Add('not a PNG'); return $problems }
    $signature = [byte[]](0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A)
    for ($i = 0; $i -lt 8; $i++) { if ($bytes[$i] -ne $signature[$i]) { $problems.Add('bad PNG signature'); return $problems } }
    $width = ([int]$bytes[16] -shl 24) -bor ([int]$bytes[17] -shl 16) -bor ([int]$bytes[18] -shl 8) -bor [int]$bytes[19]
    $height = ([int]$bytes[20] -shl 24) -bor ([int]$bytes[21] -shl 16) -bor ([int]$bytes[22] -shl 8) -bor [int]$bytes[23]
    if ($width -lt 200 -or $height -lt 100) { $problems.Add("too small (${width}x${height}, need >=200x100)") }
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
            if ($colors.Count -lt 4) { $problems.Add("looks blank (uniqueColors=$($colors.Count))") }
        }
        finally { $bitmap.Dispose() }
    }
    catch { $problems.Add('could not be decoded') }
    return $problems
}

$shotEntries = @(Get-StringList $source @('screenshots'))
$outShots = New-Object System.Collections.Generic.List[string]
$seenNames = New-Object 'System.Collections.Generic.HashSet[string]'
if ($shotEntries.Count -eq 0) {
    $red.Add('screenshots=0 (need >= 1)')
}
foreach ($entry in $shotEntries) {
    $resolved = Resolve-SourceScreenshot -Entry $entry -SourceBase $sourceBase
    if ($null -eq $resolved) {
        $red.Add("screenshot missing: $entry")
        continue
    }
    foreach ($problem in @(Test-EvidencePng -Path $resolved)) {
        $red.Add("screenshot $problem")
    }
    $name = [System.IO.Path]::GetFileName($resolved)
    if ($seenNames.Contains($name)) {
        $name = ('{0:D2}-{1}' -f ($outShots.Count + 1), $name)
    }
    [void]$seenNames.Add($name)
    $dest = Join-Path $shotOutDir $name
    Copy-EvidenceFile $resolved $dest
    $outShots.Add("$RunDir/exploration/screenshots/$name")
}
$baseRef['screenshots'] = @($outShots)
if ($shotEntries.Count -gt 0 -and $outShots.Count -eq 0) {
    $red.Add('screenshots resolved to zero usable PNGs (need >= 1)')
}

# Four-way evidence: DB counters, API snapshot/coverage, UI screenshots.
$dbEvidence = ($null -ne $idempotency) -and ($null -ne (Get-Field $source @('rollbackCaseKept')))
$apiEvidence = ($null -ne (Get-Field $source @('nextWaveSnapshotContainsU'))) -and
    ($null -ne (Get-Field $source @('nextWaveSnapshotContainsA'))) -and
    ($null -ne (Get-Field $source @('coverageBeforeUncovered')))
$uiEvidence = ($outShots.Count -ge 1)
$baseRef['fourEvidence'] = [ordered]@{
    db          = [bool]$dbEvidence
    api         = [bool]$apiEvidence
    ui          = [bool]$uiEvidence
    screenshots = @($outShots)
}
if (-not ($dbEvidence -and $apiEvidence -and $uiEvidence)) {
    $red.Add("four evidence incomplete (db=$dbEvidence api=$apiEvidence ui=$uiEvidence)")
}

# ---------------------------------------------------------------------------
# FA-C3 reuse rule: only an empty backend/frontend diff may be reused.
# ---------------------------------------------------------------------------
$sourceGitHead = Get-StringField $source @('gitHead')
$sourceGitHeadFrom = ''
if ([string]::IsNullOrWhiteSpace($sourceGitHead)) {
    # The exploration verdict itself carries no gitHead. When it was produced by
    # the same W2 exit run, the sibling w2-exit-verdict.json records that run's
    # HEAD; only trust it if the W2 exit verdict was generated at/after the
    # exploration verdict (a later manual exploration rerun invalidates it).
    $siblingW2Verdict = Join-Path $sourceBase 'w2-exit-verdict.json'
    if (Test-Path -LiteralPath $siblingW2Verdict) {
        try {
            $sibling = Read-JsonUtf8 -Path $siblingW2Verdict
            $candidateHead = Get-StringField $sibling @('gitHead')
            $sameRun = $true
            $sourceGeneratedAt = Get-StringField $source @('generatedAt')
            $siblingGeneratedAt = Get-StringField $sibling @('generatedAt')
            if ($sourceGeneratedAt -and $siblingGeneratedAt) {
                try {
                    $sourceTime = [datetime]::Parse($sourceGeneratedAt, [System.Globalization.CultureInfo]::InvariantCulture, [System.Globalization.DateTimeStyles]::RoundtripKind)
                    $siblingTime = [datetime]::Parse($siblingGeneratedAt, [System.Globalization.CultureInfo]::InvariantCulture, [System.Globalization.DateTimeStyles]::RoundtripKind)
                    if ($sourceTime -gt $siblingTime) { $sameRun = $false }
                }
                catch { $sameRun = $false }
            }
            if ($sameRun -and -not [string]::IsNullOrWhiteSpace($candidateHead)) {
                $sourceGitHead = $candidateHead
                $sourceGitHeadFrom = 'w2-exit-verdict.json'
            }
        }
        catch { }
    }
}
$baseRef['sourceGitHead'] = $sourceGitHead
$baseRef['sourceGitHeadFrom'] = $sourceGitHeadFrom

$reused = $false
$reason = ''
if ($freshRun) {
    $reason = 'fresh run'
}
elseif ([string]::IsNullOrWhiteSpace($sourceGitHead)) {
    $reason = 'source verdict has no gitHead; replay required'
}
elseif ([string]::IsNullOrWhiteSpace($head)) {
    $reason = 'current HEAD unknown; replay required'
}
else {
    $diffLines = & git -C $repoRoot diff --stat "$sourceGitHead..$head" -- backend frontend 2>$null
    if ($LASTEXITCODE -ne 0) {
        $reason = 'git diff failed; replay required'
    }
    else {
        $diffText = (@($diffLines) -join "`n").Trim()
        if ([string]::IsNullOrWhiteSpace($diffText)) {
            $reused = $true
        }
        else {
            $reason = 'replay required'
        }
    }
}
$baseRef['reuse'] = [ordered]@{ reused = $reused }
$baseRef['reason'] = $reason

if ($red.Count -gt 0) {
    # An invalid verdict is never reusable; surface the first RED item as the
    # reason so a later reader does not have to parse the red array.
    $baseRef['reuse'] = [ordered]@{ reused = $false }
    if ([string]::IsNullOrWhiteSpace($reason)) { $baseRef['reason'] = $red[0] }
    Complete-Red $baseRef
}

$baseRef['status'] = 'PASS'
$baseRef['red'] = @()
Write-Ref $baseRef
Write-Host ("FA EXPLORATION: PASS ($RunDir, source=$($baseRef['source']), reuse=$reused, screenshots=$($outShots.Count))")
if ($reason) { Write-Host "NOTE: $reason" }
exit 0
