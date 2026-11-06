#requires -Version 5.1
<#
.SYNOPSIS
    EzTest v0.22 FA visual-review harness (FA-10).

.DESCRIPTION
    Collects the screenshots of the eight logical FA stages, validates each PNG
    with the shared Assert-Png helper (magic bytes, >= 800x600, >= 8 sampled
    unique colors), and writes two untracked evidence files:

      <EvidenceRoot>/<RunDir>/visual/manifest.json
      <EvidenceRoot>/<RunDir>/visual/review-template.json

    manifest.json (paths are relative to the evidence root, forward slashes):

      {
        "generatedAt": "<ISO8601>",
        "stages": [ { "name": "prep", "screenshots": ["prep/screenshots/x.png"] } ],
        "screenshots": [ { "path", "bytes", "sha256", "width", "height", "nonBlank" } ],
        "missingStages": []
      }

    review-template.json carries empty layout/truncation/overlap/stateColor/
    emptyState/errorState/reviewer fields for every stage and every screenshot.
    It is NOT review.json and never prefills a verdict: a fresh reviewer reads
    the screenshots and fills the template (writing review.json) separately.

    The harness also re-reads manifest.json and validates it: exactly the eight
    required stages, no missing stage, at least one screenshot, the flat
    screenshot list matching the stage lists, existing files with matching
    bytes/sha256, non-blank content and dimensions >= 800x600.

    Stage screenshot sources (default):
      prep              <EvidenceRoot>/prep/screenshots/*.png (and RUN-x/prep)
      clarifying        RUN-x/{w0flow,typical,w1,w2/{base,visual,...}}/*question*,
                        *clarif*, *chat*, plus RUN-x/clarifying/screenshots
      requirement_review *requirement*, *review*
      designing         *design*, *plan*
      executing         *execut*, *run*
      reporting         *report*
      completed         *complet*, *final*, *overview*
      issues            RUN-x/{issues14,w2/issue}/**/*.png, *issue*

    Use -StageMap to override any stage with an explicit JSON map:

      {
        "clarifying": ["RUN-1/w0flow/screenshots/question.png"],
        "issues": ["RUN-1/issues14/screenshots"]
      }

    Each value is a list of paths relative to the evidence root; an entry may be
    a file, a directory (all PNGs recursively) or a wildcard pattern.

.PARAMETER RunDir
    Run directory under the evidence root: RUN-1 (default) or RUN-2.

.PARAMETER EvidenceRoot
    Evidence root. Defaults to docs/version-V0.22/acceptance/evidence/final.

.PARAMETER StageMap
    Optional JSON file that overrides the screenshot sources of named stages.

.NOTES
    Exit codes: 0 = manifest valid, 1 = RED (missing stage / invalid PNG /
    inconsistent manifest). This script never writes review.json and never
    changes product code.
#>
param(
    [ValidateSet('RUN-1', 'RUN-2')]
    [string]$RunDir = 'RUN-1',

    [string]$EvidenceRoot,

    [string]$StageMap
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
$OutputEncoding = New-Object System.Text.UTF8Encoding($false)

. (Join-Path $PSScriptRoot 'v022-fa-common.ps1')

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
$script:RequiredStages = @('prep', 'clarifying', 'requirement_review', 'designing',
    'executing', 'reporting', 'completed', 'issues')
$script:Red = New-Object System.Collections.Generic.List[string]
$script:Assigned = @{}
$script:StageMapObject = $null

$script:StageAliasDirs = @{
    clarifying         = @('clarifying', 'clarification')
    requirement_review = @('requirement_review', 'requirements', 'requirement-review')
    designing          = @('designing', 'design')
    executing          = @('executing', 'execution')
    reporting          = @('reporting', 'reports', 'report')
    completed          = @('completed', 'complete', 'final')
    issues             = @('issues', 'issue', 'issues14')
}

$script:StagePatterns = @{
    clarifying         = @('*clarif*', '*question*', '*chat*')
    requirement_review = @('*requirement*', '*review*')
    designing          = @('*design*', '*plan*')
    executing          = @('*execut*', '*run*')
    reporting          = @('*report*')
    completed          = @('*complet*', '*final*', '*overview*')
    issues             = @('*issue*')
}

function Add-Red([string]$Message) {
    $script:Red.Add($Message)
}

function Resolve-UnderRoot([string]$Path) {
    if ([string]::IsNullOrWhiteSpace($Path)) { return $null }
    $normalized = $Path -replace '/', '\'
    if ([System.IO.Path]::IsPathRooted($normalized)) {
        return [System.IO.Path]::GetFullPath($normalized)
    }
    return [System.IO.Path]::GetFullPath((Join-Path $script:EvidenceRoot $normalized))
}

function Add-UniqueFile([System.Collections.Generic.List[string]]$List, [hashtable]$Seen, [string]$Path) {
    if ([string]::IsNullOrWhiteSpace($Path)) { return }
    $full = [System.IO.Path]::GetFullPath($Path)
    $key = $full.ToLowerInvariant()
    if ($Seen.ContainsKey($key)) { return }
    $Seen[$key] = $true
    $List.Add($full)
}

function Get-PngFilesFromSpec([string]$Spec) {
    $results = New-Object System.Collections.Generic.List[string]
    if ([string]::IsNullOrWhiteSpace($Spec)) { return $results.ToArray() }
    $resolved = Resolve-UnderRoot $Spec
    if (Test-Path -LiteralPath $resolved) {
        $item = Get-Item -LiteralPath $resolved
        if ($item.PSIsContainer) {
            foreach ($file in @(Get-ChildItem -LiteralPath $item.FullName -Recurse -File -Filter '*.png' -ErrorAction SilentlyContinue)) {
                $results.Add($file.FullName)
            }
        }
        elseif ($item.Extension -ieq '.png') {
            $results.Add($item.FullName)
        }
        return $results.ToArray()
    }
    if ($Spec -match '[\*\?\[]') {
        $pattern = if ([System.IO.Path]::IsPathRooted($Spec)) { $Spec } else { Join-Path $script:EvidenceRoot $Spec }
        foreach ($file in @(Get-ChildItem -Path $pattern -File -ErrorAction SilentlyContinue)) {
            if ($file.Extension -ieq '.png') { $results.Add($file.FullName) }
        }
    }
    return $results.ToArray()
}

function Get-StageScreenshots([string]$Stage) {
    $files = New-Object System.Collections.Generic.List[string]
    $seen = @{}

    if ($null -ne $script:StageMapObject) {
        $property = $script:StageMapObject.PSObject.Properties[$Stage]
        if ($null -ne $property) {
            foreach ($spec in @($property.Value)) {
                foreach ($file in @(Get-PngFilesFromSpec ([string]$spec))) {
                    Add-UniqueFile $files $seen $file
                }
            }
            return $files.ToArray()
        }
    }

    if ($Stage -eq 'prep') {
        foreach ($spec in @('prep/screenshots', "$script:RunDir/prep/screenshots")) {
            foreach ($file in @(Get-PngFilesFromSpec $spec)) {
                Add-UniqueFile $files $seen $file
            }
        }
        return $files.ToArray()
    }

    # Explicit per-stage directories take precedence over filename patterns.
    foreach ($alias in @($script:StageAliasDirs[$Stage])) {
        foreach ($sub in @('screenshots', '')) {
            $spec = if ([string]::IsNullOrWhiteSpace($sub)) { "$script:RunDir/$alias" } else { "$script:RunDir/$alias/$sub" }
            $resolved = Resolve-UnderRoot $spec
            if ($null -ne $resolved -and (Test-Path -LiteralPath $resolved)) {
                foreach ($file in @(Get-PngFilesFromSpec $spec)) {
                    Add-UniqueFile $files $seen $file
                }
            }
        }
    }
    if ($files.Count -gt 0) { return $files.ToArray() }

    if ($Stage -eq 'issues') {
        # Dedicated issue screenshot directories: every PNG belongs to the
        # issues stage, regardless of its filename (e.g. 03-retest-run.png).
        foreach ($dedicated in @(
                "$script:RunDir/issues14/screenshots", "$script:RunDir/issues14",
                "$script:RunDir/w2/issue", "$script:RunDir/w2/issues")) {
            $resolved = Resolve-UnderRoot $dedicated
            if ($null -eq $resolved -or -not (Test-Path -LiteralPath $resolved)) { continue }
            foreach ($file in @(Get-PngFilesFromSpec $dedicated)) {
                Add-UniqueFile $files $seen $file
            }
        }
        if ($files.Count -gt 0) { return $files.ToArray() }
        $candidateDirs = @(
            "$script:RunDir/w0flow/screenshots", "$script:RunDir/w0flow",
            "$script:RunDir/typical/screenshots", "$script:RunDir/typical"
        )
    }
    else {
        $candidateDirs = @(
            "$script:RunDir/w0flow/screenshots", "$script:RunDir/w0flow",
            "$script:RunDir/typical/screenshots", "$script:RunDir/typical",
            "$script:RunDir/w1/screenshots", "$script:RunDir/w1",
            "$script:RunDir/w2/base", "$script:RunDir/w2/visual",
            "$script:RunDir/w2/human", "$script:RunDir/w2/export", "$script:RunDir/w2/code"
        )
    }

    foreach ($candidate in $candidateDirs) {
        $resolved = Resolve-UnderRoot $candidate
        if ($null -eq $resolved -or -not (Test-Path -LiteralPath $resolved)) { continue }
        $base = $resolved.TrimEnd('\')
        foreach ($file in @(Get-ChildItem -LiteralPath $resolved -Recurse -File -Filter '*.png' -ErrorAction SilentlyContinue)) {
            $full = $file.FullName
            # Match on the path below the candidate dir so the evidence root and
            # the RUN-<n> directory name cannot accidentally satisfy a pattern
            # such as *run* or *issue*.
            $relativeToDir = $full.Substring($base.Length).TrimStart('\')
            foreach ($pattern in @($script:StagePatterns[$Stage])) {
                if ($relativeToDir -like $pattern) {
                    Add-UniqueFile $files $seen $full
                    break
                }
            }
        }
    }
    return $files.ToArray()
}

function Get-EvidenceRelativePath([string]$FullPath) {
    $root = [System.IO.Path]::GetFullPath($script:EvidenceRoot).TrimEnd('\') + '\'
    $full = [System.IO.Path]::GetFullPath($FullPath)
    if (-not $full.StartsWith($root, [System.StringComparison]::OrdinalIgnoreCase)) { return $null }
    return ($full.Substring($root.Length) -replace '\\', '/')
}

function Get-PngInfo([string]$Path) {
    $full = (Resolve-Path -LiteralPath $Path).Path
    $bytes = [System.IO.File]::ReadAllBytes($full)
    $width = 0
    $height = 0
    $magic = @(0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A)
    $magicOk = $bytes.Length -ge 24
    if ($magicOk) {
        for ($i = 0; $i -lt $magic.Count; $i++) {
            if ([int]$bytes[$i] -ne [int]$magic[$i]) { $magicOk = $false; break }
        }
    }
    if ($magicOk) {
        $width = ([int]$bytes[16] -shl 24) -bor ([int]$bytes[17] -shl 16) -bor ([int]$bytes[18] -shl 8) -bor [int]$bytes[19]
        $height = ([int]$bytes[20] -shl 24) -bor ([int]$bytes[21] -shl 16) -bor ([int]$bytes[22] -shl 8) -bor [int]$bytes[23]
    }

    $problems = @(Assert-Png -Path $full)
    $nonBlank = $true
    foreach ($problem in $problems) {
        if ($problem -match 'blank|decode failed|magic mismatch|too short|unreadable|missing') {
            $nonBlank = $false
        }
    }

    return [pscustomobject]@{
        FullPath = $full
        Bytes    = $bytes.Length
        Sha256   = (Get-Sha -Path $full)
        Width    = $width
        Height   = $height
        NonBlank = $nonBlank
        Problems = $problems
    }
}

# ---------------------------------------------------------------------------
# Optional stage map.
# ---------------------------------------------------------------------------
if (-not [string]::IsNullOrWhiteSpace($StageMap)) {
    $mapPath = $StageMap
    if (-not [System.IO.Path]::IsPathRooted($mapPath)) { $mapPath = Join-Path $repoRoot $mapPath }
    if (-not (Test-Path -LiteralPath $mapPath)) {
        Write-Host "RED: stage map not found: $StageMap"
        Write-Host 'VISUAL MANIFEST: FAIL (1 red)'
        exit 1
    }
    $script:StageMapObject = Read-JsonUtf8 -Path $mapPath
    foreach ($property in @($script:StageMapObject.PSObject.Properties)) {
        if ($script:RequiredStages -notcontains $property.Name) {
            Add-Red "stage map has unknown stage: $($property.Name)"
        }
    }
}

# ---------------------------------------------------------------------------
# Collect + validate screenshots, build manifest.
# ---------------------------------------------------------------------------
$generatedAt = (Get-Date).ToString('o')
$stageEntries = New-Object System.Collections.Generic.List[object]
$flatEntries = New-Object System.Collections.Generic.List[object]
$flatStages = New-Object System.Collections.Generic.List[string]
$missingStages = New-Object System.Collections.Generic.List[string]

foreach ($stage in $script:RequiredStages) {
    $stageShots = New-Object System.Collections.Generic.List[string]
    foreach ($file in @(Get-StageScreenshots $stage)) {
        $full = [System.IO.Path]::GetFullPath($file)
        $key = $full.ToLowerInvariant()
        if ($script:Assigned.ContainsKey($key)) { continue }
        $script:Assigned[$key] = $true

        $relative = Get-EvidenceRelativePath $full
        if ($null -eq $relative) {
            Add-Red "stage ${stage}: screenshot outside evidence root: $full"
            continue
        }

        $info = Get-PngInfo $full
        foreach ($problem in @($info.Problems)) {
            Add-Red "stage ${stage}: $problem"
        }

        $stageShots.Add($relative)
        $flatStages.Add($stage)
        $flatEntries.Add([ordered]@{
            path     = $relative
            bytes    = [int64]$info.Bytes
            sha256   = [string]$info.Sha256
            width    = [int]$info.Width
            height   = [int]$info.Height
            nonBlank = [bool]$info.NonBlank
        })
    }

    if ($stageShots.Count -eq 0) {
        $missingStages.Add($stage)
        Add-Red "missing stage: $stage (no PNG screenshot found)"
    }
    $stageEntries.Add([ordered]@{
        name        = $stage
        screenshots = $stageShots.ToArray()
    })
}

$manifest = [ordered]@{
    generatedAt   = $generatedAt
    stages        = $stageEntries.ToArray()
    screenshots   = $flatEntries.ToArray()
    missingStages = $missingStages.ToArray()
}

$runRoot = Join-Path $script:EvidenceRoot $script:RunDir
$visualDir = Join-Path $runRoot 'visual'
New-Item -ItemType Directory -Force -Path $visualDir | Out-Null
$manifestPath = Join-Path $visualDir 'manifest.json'
$templatePath = Join-Path $visualDir 'review-template.json'
Write-JsonUtf8 -Path $manifestPath -Object $manifest -Depth 20

# ---------------------------------------------------------------------------
# Review template: empty fields only, never a prefilled verdict.
# ---------------------------------------------------------------------------
$templateStages = New-Object System.Collections.Generic.List[object]
foreach ($entry in $stageEntries) {
    $templateStages.Add([ordered]@{
        name        = [string]$entry['name']
        screenshots = @($entry['screenshots'])
        reviewer               = ''
        layout                 = ''
        truncation             = ''
        overlap                = ''
        stateColor             = ''
        emptyState             = ''
        errorState             = ''
    })
}

$reviews = New-Object System.Collections.Generic.List[object]
for ($i = 0; $i -lt $flatEntries.Count; $i++) {
    $reviews.Add([ordered]@{
        path       = [string]$flatEntries[$i]['path']
        stage      = [string]$flatStages[$i]
        reviewer   = ''
        layout     = ''
        truncation = ''
        overlap    = ''
        stateColor = ''
        emptyState = ''
        errorState = ''
        notes      = ''
    })
}

$flatPaths = New-Object System.Collections.Generic.List[string]
foreach ($entry in $flatEntries) { $flatPaths.Add([string]$entry['path']) }

$template = [ordered]@{
    schemaVersion       = 1
    generatedAt         = $generatedAt
    instructions        = 'Fill reviewer, reviewedAt, reviewedScreenshots, vision and every reviews[] judgment field by inspecting the screenshots. Do not prefill pass; save the completed file as review.json.'
    reviewer            = ''
    reviewedAt          = ''
    vision              = [ordered]@{
        model           = ''
        baseUrl         = ''
        visionExercised = $false
    }
    reviewedScreenshots = 0
    stages              = $templateStages.ToArray()
    screenshots         = $flatPaths.ToArray()
    reviews             = $reviews.ToArray()
    defects             = @()
}
Write-JsonUtf8 -Path $templatePath -Object $template -Depth 20

# ---------------------------------------------------------------------------
# Validate the written manifest.
# ---------------------------------------------------------------------------
$written = Read-JsonUtf8 -Path $manifestPath
$writtenStages = @($written.stages)
foreach ($problem in @(Assert-ExactSetStrings -Actual @($writtenStages | ForEach-Object { [string]$_.name }) -Expected $script:RequiredStages -Label 'manifest.stages')) {
    Add-Red $problem
}

$writtenShots = @($written.screenshots)
if ($writtenShots.Count -eq 0) { Add-Red 'manifest.screenshots is empty' }

$stageShotCount = 0
foreach ($stage in $writtenStages) { $stageShotCount += @($stage.screenshots).Count }
if ($writtenShots.Count -ne $stageShotCount) {
    Add-Red "manifest.screenshots=$($writtenShots.Count) != stage screenshot count=$stageShotCount"
}

$uniquePaths = @($writtenShots | ForEach-Object { [string]$_.path } | Sort-Object -Unique)
if ($uniquePaths.Count -ne $writtenShots.Count) {
    Add-Red 'manifest.screenshots contains duplicate paths'
}

$writtenMissing = @($written.missingStages)
if ($writtenMissing.Count -gt 0) {
    Add-Red ("manifest.missingStages: " + ($writtenMissing -join ', '))
}

foreach ($shot in $writtenShots) {
    $relative = [string]$shot.path
    if ([string]::IsNullOrWhiteSpace($relative)) { Add-Red 'manifest screenshot without path'; continue }
    if ([System.IO.Path]::IsPathRooted($relative)) { Add-Red "manifest path is absolute: $relative"; continue }
    if ($relative -match '\\') { Add-Red "manifest path is not forward-slash relative: $relative" }
    $full = Join-Path $script:EvidenceRoot ($relative -replace '/', '\')
    if (-not (Test-Path -LiteralPath $full)) { Add-Red "manifest screenshot missing: $relative"; continue }

    $item = Get-Item -LiteralPath $full
    if ([int64]$shot.bytes -ne [int64]$item.Length) {
        Add-Red "manifest bytes mismatch: $relative"
    }
    if ([string]$shot.sha256 -ne (Get-Sha -Path $full)) {
        Add-Red "manifest sha256 mismatch: $relative"
    }
    if ([bool]$shot.nonBlank -ne $true) {
        Add-Red "manifest nonBlank=false: $relative"
    }
    if ([int]$shot.width -lt 800 -or [int]$shot.height -lt 600) {
        Add-Red "manifest dimensions below 800x600: $relative ($($shot.width)x$($shot.height))"
    }
}

# ---------------------------------------------------------------------------
# Result.
# ---------------------------------------------------------------------------
if ($script:Red.Count -gt 0) {
    foreach ($item in $script:Red) { Write-Host ("RED: " + $item) }
    Write-Host ("VISUAL MANIFEST: FAIL ($($script:Red.Count) red)")
    Write-Host "manifest: $manifestPath"
    Write-Host "review template: $templatePath"
    exit 1
}

Write-Host ("VISUAL MANIFEST: PASS ({0} stages, {1} screenshots)" -f $script:RequiredStages.Count, $flatEntries.Count)
Write-Host "manifest: $manifestPath"
Write-Host "review template: $templatePath"
exit 0
