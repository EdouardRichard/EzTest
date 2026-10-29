#requires -Version 5.1
<#
.SYNOPSIS
    Checks that the P8 workspace cleanup is complete.

.DESCRIPTION
    Asserts:
      - .gitignore lists .data/ and backend/.data/, and git actually ignores them.
      - All sources under frontend/src contain none of the stale P0 copy strings,
        and the shell sources use the P8 copy instead.
      - nav-items.ts no longer carries a phase: "P0" badge, and the login footer
        shows P8 rather than P0.
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

# 2. Copy: no stale P0 labels anywhere under frontend/src; shell uses P8 copy.
$srcRoot = Join-Path $repoRoot "frontend/src"
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

$srcFiles = @()
if (Test-Path -LiteralPath $srcRoot -PathType Container) {
    $srcFiles = @(Get-ChildItem -LiteralPath $srcRoot -Recurse -File)
}

if ($srcFiles.Count -eq 0) {
    Add-Failure "no sources found under $srcRoot"
} else {
    foreach ($needle in $p0Copy) {
        $hits = @()
        foreach ($file in $srcFiles) {
            $text = Read-Utf8File -Path $file.FullName
            if ($text.Contains($needle)) {
                $hits += $file.FullName
            }
        }
        if ($hits.Count -eq 0) {
            Add-Pass "frontend sources contain no stale '$needle'"
        } else {
            Add-Failure "frontend sources still contain '$needle': $($hits -join ', ')"
        }
    }
}

$shellRoot = Join-Path $repoRoot "frontend/src/features/shell"
$shellFiles = @()
if (Test-Path -LiteralPath $shellRoot -PathType Container) {
    $shellFiles = @(Get-ChildItem -LiteralPath $shellRoot -Recurse -File |
            Where-Object { $_.Extension -eq ".ts" -or $_.Extension -eq ".tsx" })
}

if ($shellFiles.Count -eq 0) {
    Add-Failure "no TypeScript sources found under $shellRoot"
} else {
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

# 3. Navigation phase badges and the login footer must no longer say P0.
$navItemsPath = Join-Path $repoRoot "frontend/src/navigation/nav-items.ts"
if (-not (Test-Path -LiteralPath $navItemsPath -PathType Leaf)) {
    Add-Failure "missing navigation source $navItemsPath"
} else {
    $navText = Read-Utf8File -Path $navItemsPath
    if ($navText -match 'phase:\s*"P0"') {
        Add-Failure 'nav-items.ts still contains a phase: "P0" badge'
    } else {
        Add-Pass 'nav-items.ts contains no phase: "P0" badge'
    }
}

$loginPagePath = Join-Path $repoRoot "frontend/src/app/login/page.tsx"
if (-not (Test-Path -LiteralPath $loginPagePath -PathType Leaf)) {
    Add-Failure "missing login page $loginPagePath"
} else {
    $loginText = Read-Utf8File -Path $loginPagePath
    $loginFooterLines = @($loginText -split '\r?\n' | Where-Object { $_ -match 'EzTest ·' })
    $p0FooterLines = @($loginFooterLines | Where-Object { $_ -match 'P0' })

    if ($loginFooterLines.Count -eq 0) {
        Add-Failure "login page has no EzTest footer line"
    } elseif ($p0FooterLines.Count -gt 0) {
        Add-Failure "login footer still contains P0 copy: $($p0FooterLines -join ' | ')"
    } else {
        Add-Pass "login footer contains no P0 copy"
    }

    if (@($loginFooterLines | Where-Object { $_.Contains("EzTest · P8") }).Count -gt 0) {
        Add-Pass "login footer contains 'EzTest · P8'"
    } else {
        Add-Failure "login footer is missing 'EzTest · P8'"
    }
}

# 4. The scratch draft is archived (moved, not deleted) under the gitignored .tmp/.
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
