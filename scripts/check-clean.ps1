#requires -Version 5.1
<#
.SYNOPSIS
    Checks that the P8 workspace cleanup is complete.

.DESCRIPTION
    Asserts:
      - .gitignore lists .data/ and backend/.data/, and git actually ignores them.
      - The shell sources (frontend/src/features/shell) contain none of the stale
        P0 copy strings, and use the P8 copy instead.
      - The scratch spec docs/superpowers/specs/Untitled-1.md has been archived
        to the gitignored .tmp/p8-untitled-1.md and is gone from specs/.
    Prints PASS/FAIL per assertion and exits non-zero when any assertion fails.
    Compatible with Windows PowerShell 5.1 and PowerShell 7+.

.EXAMPLE
    powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/check-clean.ps1
#>
[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$failures = New-Object System.Collections.ArrayList

function Add-Pass {
    param([string]$Message)
    Write-Host "PASS  $Message" -ForegroundColor Green
}

function Add-Failure {
    param([string]$Message)
    [void]$failures.Add($Message)
    Write-Host "FAIL  $Message" -ForegroundColor Red
}

function Read-Utf8File {
    param([string]$Path)
    # Decode explicitly: the TypeScript sources are UTF-8 without a BOM and
    # Windows PowerShell 5.1 would otherwise fall back to the ANSI code page.
    return [System.IO.File]::ReadAllText($Path, [System.Text.Encoding]::UTF8)
}

function Test-GitIgnored {
    param([string]$RelativePath)
    try {
        & git -C $repoRoot check-ignore -q -- $RelativePath 2>$null
        return ($LASTEXITCODE -eq 0)
    } catch {
        return $false
    }
}

# 1. Local runtime data must never be committed.
$gitignorePath = Join-Path $repoRoot ".gitignore"
if (-not (Test-Path -LiteralPath $gitignorePath -PathType Leaf)) {
    Add-Failure ".gitignore is missing at $gitignorePath"
} else {
    $gitignoreLines = @(Get-Content -LiteralPath $gitignorePath | ForEach-Object { $_.Trim() })
    foreach ($entry in @(".data/", "backend/.data/")) {
        if ($gitignoreLines -contains $entry) {
            Add-Pass ".gitignore contains '$entry'"
        } else {
            Add-Failure ".gitignore is missing '$entry'"
        }

        if (Test-GitIgnored -RelativePath $entry) {
            Add-Pass "git ignores '$entry'"
        } else {
            Add-Failure "'$entry' is not ignored by git (check-ignore reported no match)"
        }
    }
}

# 2. Shell copy: no stale P0 labels, the P8 labels are present.
$shellRoot = Join-Path $repoRoot "frontend/src/features/shell"
$p0Copy = @(
    "P0 · 地基版本",
    "P0 地基运行中",
    "P0 平台地基"
)
$p8Copy = @(
    @{ File = "Topbar.tsx"; Text = "P8 · 体验硬化" },
    @{ File = "Sidebar.tsx"; Text = "P8 · 体验硬化" },
    @{ File = "AppShell.tsx"; Text = "EzTest · P8" }
)

$shellFiles = @()
if (Test-Path -LiteralPath $shellRoot -PathType Container) {
    $shellFiles = @(Get-ChildItem -LiteralPath $shellRoot -Recurse -File |
            Where-Object { $_.Extension -eq ".ts" -or $_.Extension -eq ".tsx" })
}

if ($shellFiles.Count -eq 0) {
    Add-Failure "no TypeScript sources found under $shellRoot"
} else {
    foreach ($needle in $p0Copy) {
        $hits = @()
        foreach ($file in $shellFiles) {
            $text = Read-Utf8File -Path $file.FullName
            if ($text.Contains($needle)) {
                $hits += $file.FullName
            }
        }
        if ($hits.Count -eq 0) {
            Add-Pass "shell sources contain no stale '$needle'"
        } else {
            Add-Failure "shell sources still contain '$needle': $($hits -join ', ')"
        }
    }

    foreach ($expectation in $p8Copy) {
        $path = Join-Path $shellRoot $expectation.File
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
            Add-Failure "missing shell source $($expectation.File)"
            continue
        }
        $text = Read-Utf8File -Path $path
        if ($text.Contains($expectation.Text)) {
            Add-Pass "$($expectation.File) contains '$($expectation.Text)'"
        } else {
            Add-Failure "$($expectation.File) is missing '$($expectation.Text)'"
        }
    }
}

# 3. The scratch draft is archived (moved, not deleted) under the gitignored .tmp/.
$specDraft = Join-Path $repoRoot "docs/superpowers/specs/Untitled-1.md"
$archivePath = Join-Path $repoRoot ".tmp/p8-untitled-1.md"

if (Test-Path -LiteralPath $specDraft) {
    Add-Failure "docs/superpowers/specs/Untitled-1.md still exists; archive it to .tmp/p8-untitled-1.md"
} else {
    Add-Pass "docs/superpowers/specs/Untitled-1.md is no longer in specs"
}

if (-not (Test-Path -LiteralPath $archivePath -PathType Leaf)) {
    Add-Failure ".tmp/p8-untitled-1.md is missing"
} elseif ((Get-Item -LiteralPath $archivePath).Length -eq 0) {
    Add-Failure ".tmp/p8-untitled-1.md is empty"
} else {
    Add-Pass ".tmp/p8-untitled-1.md exists and is not empty"
}

if (Test-GitIgnored -RelativePath ".tmp/p8-untitled-1.md") {
    Add-Pass ".tmp/p8-untitled-1.md is gitignored (not committed)"
} else {
    Add-Failure ".tmp/p8-untitled-1.md is not gitignored (check-ignore reported no match)"
}

Write-Host ""
if ($failures.Count -gt 0) {
    Write-Host "CLEAN CHECK FAILED: $($failures.Count) assertion(s) failed." -ForegroundColor Red
    exit 1
}

Write-Host "CLEAN CHECK PASSED" -ForegroundColor Green
exit 0
