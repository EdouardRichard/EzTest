#requires -Version 5.1
<#
.SYNOPSIS
    Assemble the public `main` release tree from the `dev` branch and (optionally) commit/push it.

.DESCRIPTION
    `main` is intentionally a curated public snapshot, not a history merge of `dev`. It contains the
    source code, the bilingual README, screenshots, deployment assets and the OpenAPI contract, and it
    excludes development-state artifacts (superpowers specs/plans, acceptance records, design/roadmap
    documents, the dev status log, internal gate scripts, local toolchain/runtime data).

    The script uses a git worktree under `.tmp/main-work` so the current checkout (usually `dev`) is
    never switched or modified. It:

      1. resets the main worktree to the tracked `main` state and removes tracked files;
      2. checks out the public allow-list from `dev`;
      3. applies the small, explicit sanitization needed for the public copy (deploy/README links that
         point at internal docs);
      4. verifies that no forbidden path/content leaked and that the expected files exist;
      5. (with -Commit) commits the tree and (with -Push) pushes `main` to origin.

    Default mode is a dry run: assemble + verify + print the diff/status, change nothing on the branch.

.PARAMETER Version
    Release version written into the commit message; the public release notes file
    `docs/release-notes/0.1-preview.md` must exist on `dev` before assembly.

.PARAMETER Commit
    Commit the assembled tree on `main` (implies a real commit, but still no push).

.PARAMETER Push
    Push `main` to `origin` after a successful commit; requires -Commit.

.NOTES
    Compatible with Windows PowerShell 5.1 and PowerShell 7+. Safe to re-run; the worktree is reset each
    time. Never contains or prints secrets.
#>
[CmdletBinding()]
param(
    [string]$Version = "0.1.0-preview.1",
    [string]$PublicRef = "main",
    [string]$SourceRef = "dev",
    [string]$WorkDir = ".tmp/main-work",
    [string]$Message = "",
    [switch]$Commit,
    [switch]$Push
)

$ErrorActionPreference = "Stop"

if ($Push -and -not $Commit) {
    throw "-Push requires -Commit."
}

$repoRoot = Split-Path -Parent $PSScriptRoot
$workPath = Join-Path $repoRoot $WorkDir

function Invoke-Git {
    param([string[]]$Arguments, [string]$WorkingDirectory = $repoRoot, [switch]$IgnoreExitCode)
    $previousPreference = $ErrorActionPreference
    # Windows PowerShell 5.1 raises a non-terminating NativeCommandError for
    # native stderr under Stop; capture the diagnostic and inspect the exit code.
    $ErrorActionPreference = "Continue"
    try {
        $output = & git -C $WorkingDirectory @Arguments 2>&1
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousPreference
    }
    if ($code -ne 0 -and -not $IgnoreExitCode) {
        $detail = ($output | ForEach-Object { [string]$_ }) -join " | "
        throw "git $($Arguments -join ' ') failed ($code): $detail"
    }
    return @($output | ForEach-Object { [string]$_ })
}

function Get-GitLines {
    param([string[]]$Arguments, [string]$WorkingDirectory = $repoRoot)
    $previousPreference = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        $output = & git -C $WorkingDirectory @Arguments 2>$null
    } finally {
        $ErrorActionPreference = $previousPreference
    }
    return @($output | ForEach-Object { [string]$_ })
}

function Test-GitPath {
    param([string]$Ref, [string]$Path)
    $previousPreference = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        & git -C $repoRoot cat-file -e ("{0}:{1}" -f $Ref, $Path) 2>$null
        return ($LASTEXITCODE -eq 0)
    } finally {
        $ErrorActionPreference = $previousPreference
    }
}

function Assert-PathMissing {
    param([string]$Relative)
    $path = Join-Path $workPath $Relative
    if (Test-Path -LiteralPath $path) {
        throw "public tree contains a forbidden path: $Relative"
    }
}

function Assert-PathPresent {
    param([string]$Relative)
    $path = Join-Path $workPath $Relative
    if (-not (Test-Path -LiteralPath $path)) {
        throw "public tree is missing a required path: $Relative"
    }
}

# ---------------------------------------------------------------------------
# Public allow-list. Everything not listed here is intentionally not published.
# ---------------------------------------------------------------------------
$includePaths = @(
    ".gitignore",
    ".gitattributes",
    ".dockerignore",
    ".env.example",
    "LICENSE",
    "README.md",
    "README.zh-CN.md",
    "backend",
    "deploy",
    "docs/assets/screenshots",
    "docs/openapi/api.yaml",
    "docs/release-notes/0.1-preview.md",
    "executor",
    "frontend"
)

$forbiddenPaths = @(
    ".env",
    ".tools",
    ".data",
    ".tmp",
    "DEVELOPMENT_STATUS.md",
    "Agent.md",
    "acceptance.md",
    "docs/acceptance",
    "docs/design",
    "docs/Pre-Research",
    "docs/superpowers",
    "docs/deployment.md",
    "docs/development.md",
    "docs/dev-setup.local.md",
    "docs/operations.md",
    "docs/user-guide.md",
    "docs/release-notes/p8.md",
    "eval",
    "scripts",
    "backend/.data",
    "frontend/.next",
    "frontend/node_modules",
    "executor/node_modules"
)

# ---------------------------------------------------------------------------
# 0. Preconditions.
# ---------------------------------------------------------------------------
Write-Host "==> preconditions" -ForegroundColor Cyan
if (-not (Get-Command git -ErrorAction SilentlyContinue)) {
    throw "git is required."
}
Invoke-Git -Arguments @("rev-parse", "--verify", "$SourceRef^{commit}") | Out-Null
Invoke-Git -Arguments @("rev-parse", "--verify", "$PublicRef^{commit}") | Out-Null
if (-not (Test-Path -LiteralPath (Join-Path $repoRoot "docs/release-notes/0.1-preview.md"))) {
    throw "docs/release-notes/0.1-preview.md is missing; write the public release notes before assembling main."
}

# ---------------------------------------------------------------------------
# 1. Prepare the main worktree at the tracked main state.
# ---------------------------------------------------------------------------
Write-Host "==> main worktree: $workPath" -ForegroundColor Cyan
$existing = Get-GitLines -Arguments @("worktree", "list", "--porcelain")
$normalizedWorkPath = $workPath.Replace("\", "/").TrimEnd("/")
$worktreeRegistered = $false
foreach ($line in $existing) {
    if ([string]$line -match '^worktree\s+(.+)$') {
        $registered = $matches[1].Trim().Replace("\", "/").TrimEnd("/")
        if ($registered -eq $normalizedWorkPath) { $worktreeRegistered = $true }
    }
}
if (-not (Test-Path -LiteralPath $workPath)) {
    if (Test-Path -LiteralPath (Join-Path $repoRoot $WorkDir)) {
        throw "$WorkDir exists but is not a git worktree; remove it manually and re-run."
    }
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $workPath) | Out-Null
    if ($worktreeRegistered) {
        Invoke-Git -Arguments @("worktree", "remove", "--force", $workPath) -IgnoreExitCode | Out-Null
    }
    Invoke-Git -Arguments @("worktree", "add", $workPath, $PublicRef) | Out-Null
} elseif (-not $worktreeRegistered) {
    throw "$WorkDir exists but is not registered as a git worktree; remove it manually and re-run."
}

Invoke-Git -Arguments @("reset", "--hard", $PublicRef) -WorkingDirectory $workPath | Out-Null
Invoke-Git -Arguments @("clean", "-fdx") -WorkingDirectory $workPath | Out-Null
Invoke-Git -Arguments @("rm", "-r", "-q", "--ignore-unmatch", "--", ".") -WorkingDirectory $workPath -IgnoreExitCode | Out-Null
# A tracked-file purge leaves the working tree empty; clean again so only .git remains.
Invoke-Git -Arguments @("clean", "-fdx") -WorkingDirectory $workPath | Out-Null

# ---------------------------------------------------------------------------
# 2. Check out the public allow-list from dev.
# ---------------------------------------------------------------------------
Write-Host "==> copy public files from $SourceRef" -ForegroundColor Cyan
foreach ($path in $includePaths) {
    if (-not (Test-GitPath -Ref $SourceRef -Path $path)) {
        Write-Warning "skipping missing path on ${SourceRef}: $path"
        continue
    }
    Invoke-Git -Arguments @("checkout", $SourceRef, "--", $path) -WorkingDirectory $workPath | Out-Null
}

# ---------------------------------------------------------------------------
# 3. Public-copy overrides: docs that must not expose internal links/process.
#    The sanitized sources live in docs/public/ on dev and are copied over the
#    published files in the main tree.
# ---------------------------------------------------------------------------
Write-Host "==> apply public document overrides" -ForegroundColor Cyan
$overrides = @(
    @{ Source = "docs/public/deploy-README.md"; Target = "deploy/README.md" },
    @{ Source = "docs/public/executor-README.md"; Target = "executor/README.md" }
)
foreach ($override in $overrides) {
    $overridePath = Join-Path $repoRoot $override.Source
    if (-not (Test-Path -LiteralPath $overridePath)) {
        throw "missing public document override: $($override.Source)"
    }
    Copy-Item -LiteralPath $overridePath -Destination (Join-Path $workPath $override.Target) -Force
}


# ---------------------------------------------------------------------------
# 4. Verify the assembled tree.
# ---------------------------------------------------------------------------
Write-Host "==> verify public tree" -ForegroundColor Cyan
foreach ($relative in $forbiddenPaths) {
    Assert-PathMissing $relative
}
foreach ($relative in @(
        "LICENSE",
        "README.md",
        "README.zh-CN.md",
        ".env.example",
        "backend/go.mod",
        "backend/internal/db/migrations/order.txt",
        "frontend/package.json",
        "frontend/src/app/(app)/versions/[id]/page.tsx",
        "executor/src/server.mjs",
        "deploy/docker-compose.yml",
        "deploy/Dockerfile.api",
        "deploy/Dockerfile.web",
        "docs/openapi/api.yaml",
        "docs/assets/screenshots/01-login.png",
        "docs/release-notes/0.1-preview.md"
    )) {
    Assert-PathPresent $relative
}

$publicReadme = Get-Content -LiteralPath (Join-Path $workPath "README.md") -Raw -Encoding UTF8
if ($publicReadme -notmatch "0\.1-preview") {
    throw "README.md does not mention the 0.1-preview release."
}
if ($publicReadme -match "docs/superpowers|docs/acceptance|Agent\.md|DEVELOPMENT_STATUS") {
    throw "README.md still references internal development artifacts."
}
$zhReadme = Get-Content -LiteralPath (Join-Path $workPath "README.zh-CN.md") -Raw -Encoding UTF8
if ($zhReadme -notmatch "README\.md") {
    throw "README.zh-CN.md has no language switch back to README.md."
}

$tracked = Get-GitLines -Arguments @("ls-files") -WorkingDirectory $workPath
if ($tracked.Count -lt 800) {
    throw "public tree looks too small ($($tracked.Count) files); expected the full source snapshot."
}

# Secret-shaped scan over the public tree (never print matching values).
$secretPatterns = @(
    '-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----',
    'sk-[A-Za-z0-9_-]{20,}',
    '(ghp|gho|ghs|ghu)_[A-Za-z0-9]{20,}',
    'AKIA[0-9A-Z]{16}'
)
$scanned = 0
Get-ChildItem -LiteralPath $workPath -Recurse -File -Force |
    Where-Object { $_.FullName -notmatch '[/\\]\.git[/\\]' -and $_.Length -lt 2MB } |
    ForEach-Object {
        $scanned++
        $text = Get-Content -LiteralPath $_.FullName -Raw -Encoding UTF8 -ErrorAction SilentlyContinue
        if ($null -eq $text) { return }
        foreach ($pattern in $secretPatterns) {
            if ($text -match $pattern) {
                throw "potential secret in public tree: $($_.FullName.Replace($workPath, ''))"
            }
        }
    }
Write-Host "scanned $scanned files for secrets"

$status = Get-GitLines -Arguments @("status", "--porcelain") -WorkingDirectory $workPath
Write-Host ("public tree: {0} tracked files, {1} working-tree entries" -f $tracked.Count, $status.Count)
Write-Host "==> diffstat vs $PublicRef" -ForegroundColor Cyan
Invoke-Git -Arguments @("--no-pager", "diff", "--stat", $PublicRef, "--") -WorkingDirectory $workPath |
    Select-Object -Last 12 |
    ForEach-Object { Write-Host $_ }
Write-Host ""
Write-Host "Top-level entries:" -ForegroundColor Cyan
Get-ChildItem -LiteralPath $workPath -Force |
    Where-Object { $_.Name -ne ".git" } |
    Sort-Object Name |
    ForEach-Object { Write-Host ("  {0}" -f $_.Name) }
Write-Host ""

# ---------------------------------------------------------------------------
# 5. Optional commit / push.
# ---------------------------------------------------------------------------
if (-not $Commit) {
    Write-Host "Dry run complete. Re-run with -Commit (and optionally -Push) to publish." -ForegroundColor Yellow
    exit 0
}

$commitMessage = $Message
if ([string]::IsNullOrWhiteSpace($commitMessage)) {
    $commitMessage = "release: EzTest $Version public source"
}
Invoke-Git -Arguments @("add", "-A") -WorkingDirectory $workPath | Out-Null
$staged = Get-GitLines -Arguments @("diff", "--cached", "--name-only") -WorkingDirectory $workPath
if ($staged.Count -eq 0) {
    Write-Host "Nothing to commit; main is already at the requested snapshot." -ForegroundColor Yellow
} else {
    Invoke-Git -Arguments @("commit", "-m", $commitMessage) -WorkingDirectory $workPath | Out-Null
    Write-Host ("committed {0} files" -f $staged.Count) -ForegroundColor Green
}

if ($Push) {
    Write-Host "==> push $PublicRef to origin" -ForegroundColor Cyan
    Invoke-Git -Arguments @("push", "origin", $PublicRef) -WorkingDirectory $workPath
    Write-Host "pushed $PublicRef" -ForegroundColor Green
}
