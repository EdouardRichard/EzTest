#requires -Version 5.1
<#
.SYNOPSIS
P8 (EzTest 0.2) repository-local static gate.

.DESCRIPTION
Database-free gate for acceptance scenario S13. It verifies the 0.2 delivery
surface that can be checked without PostgreSQL, Docker, Chrome or an LLM:

  - .env.example defines exactly the four documented EZTEST_* startup variables
    (EZTEST_DATABASE_URL, EZTEST_API_PORT, EZTEST_WEB_PORT,
    EZTEST_EXECUTOR_PORT) and .env is gitignored and untracked;
  - docs/version-V0.2/contracts/openapi.yaml is frozen as 0.2.0-p8 / P8 and the
    frozen P8 contract gate (scripts/p8-contract-check.cjs) passes;
  - the migration order (order.txt, 42 entries) and settings catalogue (85 keys)
    match the frozen P8 totals;
  - deploy/docker-compose.yml declares the six P8 services and
    frontend/scripts/validate-compose.mjs accepts it;
  - the root READMEs and DEVELOPMENT_STATUS.md are upgraded to 0.2;
  - .github/workflows/ci.yml keeps the Ubuntu test job, runs
    scripts/p8-static-check.ps1 from the static-acceptance job, and no longer
    references the legacy 0.1 gates or 0.1-only environment keys;
  - the 0.1 gate scripts are retained as legacy but no P8 script invokes them;
  - docs/version-V0.2/acceptance/p8/ contains the required P8 evidence files;
  - no secret-shaped values are present in the tracked docs/config scan scope;
  - all relative Markdown links in the root/P8 documents resolve.

Each check prints one PASS or FAIL line. The script exits 1 when any check
fails and 0 when all checks pass.

.NOTES
- PowerShell 5.1 compatible (Windows PowerShell) and PowerShell 7 compatible.
- Read-only: it never modifies the repository and never prints secret values.
- Prerequisite: frontend dependencies installed for the OpenAPI/compose
  parsers (cd frontend; pnpm install --frozen-lockfile). CI does this first.
- The acceptance evidence documents are produced by the P8 acceptance tasks;
  until they exist this gate reports them as missing and fails on purpose.
#>
[CmdletBinding()]
param()

$ErrorActionPreference = 'Continue'
$ProgressPreference = 'SilentlyContinue'
try {
    # Native tools (node/git) emit UTF-8; keep non-ASCII diagnostics readable
    # where the host allows changing the console encoding.
    [Console]::OutputEncoding = [System.Text.Encoding]::UTF8
} catch {
    # Hosts without a console can reject this; it is diagnostic only.
}

$repoRoot = Split-Path -Parent $PSScriptRoot
if (-not (Test-Path -LiteralPath $repoRoot -PathType Container)) {
    Write-Host 'FAIL  repo-root              cannot resolve the repository root from the script location'
    Write-Host 'RESULT: FAIL - 0 passed, 1 failed'
    exit 1
}

$script:Passed = 0
$script:Failed = 0
$script:NodeReady = $false

# The 0.1-only startup environment keys replaced by the 0.2 settings centre.
# Keep in sync with frontend/scripts/validate-compose.mjs.
$script:LegacyEnvKeys = @(
    'EZTEST_HTTP_ADDR',
    'EZTEST_WEB_ORIGIN',
    'EZTEST_STORAGE_DIR',
    'EZTEST_VERSION_ATTACHMENT_DIR',
    'EZTEST_EVIDENCE_DIR',
    'EZTEST_REPORT_DIR',
    'EZTEST_EXPORT_DIR',
    'EZTEST_SNOWFLAKE_NODE_ID',
    'EZTEST_OPENAPI_PATH',
    'EZTEST_ENABLE_TEST_ENDPOINTS'
)

$script:RequiredStartupKeys = @(
    'EZTEST_DATABASE_URL',
    'EZTEST_API_PORT',
    'EZTEST_WEB_PORT',
    'EZTEST_EXECUTOR_PORT'
)

$script:RequiredAcceptanceFiles = @(
    'acceptance.md',
    'resilience.md',
    'multi.md',
    'e2e.md',
    'capability-signoff.md',
    'static.md',
    'known-limitations.md',
    'runbook.md'
)

function Write-Pass {
    param([string]$Name, [string]$Detail)
    $script:Passed++
    Write-Host ('PASS  {0,-20} {1}' -f $Name, $Detail) -ForegroundColor Green
}

function Write-Fail {
    param([string]$Name, [string]$Detail)
    $script:Failed++
    Write-Host ('FAIL  {0,-20} {1}' -f $Name, $Detail) -ForegroundColor Red
}

function Get-RepoPath {
    param([string]$RelativePath)
    return (Join-Path $repoRoot $RelativePath)
}

function Test-RepoLeaf {
    param([string]$RelativePath)
    return (Test-Path -LiteralPath (Get-RepoPath $RelativePath) -PathType Leaf)
}

function Read-RepoText {
    param([string]$Path)
    return [System.IO.File]::ReadAllText($Path, [System.Text.Encoding]::UTF8)
}

function Get-TextLines {
    param([string]$Text)
    return ($Text -split "`r?`n")
}

function Get-RelativeDisplayPath {
    param([string]$Path)
    return ($Path.Substring($repoRoot.Length) -replace '^[\\/]+', '' -replace '\\', '/')
}

function Invoke-NodeScript {
    param([string]$ScriptPath, [string]$WorkingDirectory)
    $lines = @()
    $exitCode = -1
    Push-Location -LiteralPath $WorkingDirectory
    try {
        $lines = @(& node $ScriptPath 2>&1 | ForEach-Object { [string]$_ })
        $exitCode = $LASTEXITCODE
    } finally {
        Pop-Location
    }
    return [pscustomobject]@{ ExitCode = $exitCode; Lines = $lines }
}

function Initialize-NodeToolchain {
    if (-not (Get-Command node -ErrorAction SilentlyContinue)) {
        Write-Fail 'node-tooling' 'node is not installed or not on PATH; install Node.js 22+ before running this gate'
        return
    }
    $yamlModule = Join-Path (Join-Path $repoRoot 'frontend') 'node_modules\yaml'
    if (-not (Test-Path -LiteralPath $yamlModule -PathType Container)) {
        Write-Fail 'frontend-deps' 'frontend/node_modules/yaml is missing; run: cd frontend; pnpm install --frozen-lockfile'
        return
    }
    $script:NodeReady = $true
}

# ---------------------------------------------------------------------------
# Startup environment surface.
# ---------------------------------------------------------------------------
function Check-EnvExample {
    $name = 'env-example'
    $path = Get-RepoPath '.env.example'
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        Write-Fail $name '.env.example is missing'
        return
    }
    $text = Read-RepoText $path
    $present = New-Object System.Collections.Generic.List[string]
    $empty = New-Object System.Collections.Generic.List[string]
    foreach ($line in (Get-TextLines $text)) {
        $matchResult = [regex]::Match($line, '^\s*(EZTEST_[A-Z0-9_]+)[ \t]*=[ \t]*(.*)$')
        if (-not $matchResult.Success) { continue }
        $key = $matchResult.Groups[1].Value
        if (-not $present.Contains($key)) { $present.Add($key) }
        if ($matchResult.Groups[2].Value.Trim().Length -eq 0 -and -not $empty.Contains($key)) {
            $empty.Add($key)
        }
    }
    $missing = New-Object System.Collections.Generic.List[string]
    foreach ($key in $script:RequiredStartupKeys) {
        if (-not $present.Contains($key)) { $missing.Add($key) }
    }
    $unexpected = New-Object System.Collections.Generic.List[string]
    foreach ($key in $present) {
        if ($script:RequiredStartupKeys -notcontains $key) { $unexpected.Add($key) }
    }
    $legacy = New-Object System.Collections.Generic.List[string]
    foreach ($key in $present) {
        if ($script:LegacyEnvKeys -contains $key) { $legacy.Add($key) }
    }
    $problems = New-Object System.Collections.Generic.List[string]
    if ($missing.Count -gt 0) { $problems.Add('missing ' + ($missing -join ', ')) }
    if ($unexpected.Count -gt 0) { $problems.Add('unexpected ' + ($unexpected -join ', ')) }
    if ($legacy.Count -gt 0) { $problems.Add('0.1-only ' + ($legacy -join ', ')) }
    if ($empty.Count -gt 0) { $problems.Add('empty value for ' + ($empty -join ', ')) }
    if ($problems.Count -gt 0) {
        Write-Fail $name ('.env.example must define exactly the four documented EZTEST_* startup variables; ' + ($problems -join '; '))
        return
    }
    Write-Pass $name 'exactly the four documented EZTEST_* startup variables, no fifth'
}

function Check-DotEnv {
    if (-not (Get-Command git -ErrorAction SilentlyContinue)) {
        Write-Fail 'env-gitignore' 'git is required to verify the .env ignore rule, but it is not installed'
        Write-Fail 'env-untracked' 'git is required to verify .env tracking, but it is not installed'
        return
    }
    Push-Location -LiteralPath $repoRoot
    try {
        & git check-ignore -q -- .env 2>$null
        $ignoreExit = $LASTEXITCODE
        $tracked = @(& git ls-files -- .env 2>$null)
        $trackExit = $LASTEXITCODE
    } finally {
        Pop-Location
    }
    if ($ignoreExit -eq 0) {
        Write-Pass 'env-gitignore' '.env is gitignored'
    } else {
        Write-Fail 'env-gitignore' '.env is not gitignored; add .env to .gitignore'
    }
    if ($trackExit -ne 0) {
        Write-Fail 'env-untracked' 'git ls-files failed; run this gate inside the repository checkout'
    } elseif ($tracked.Count -eq 0) {
        Write-Pass 'env-untracked' '.env is not tracked by git'
    } else {
        Write-Fail 'env-untracked' '.env is tracked by git; run: git rm --cached .env'
    }
}

# ---------------------------------------------------------------------------
# Frozen P8 contract + counts.
# ---------------------------------------------------------------------------
function Check-OpenApiFrozen {
    $name = 'openapi'
    $relative = 'docs/version-V0.2/contracts/openapi.yaml'
    if (-not (Test-RepoLeaf $relative)) {
        Write-Fail $name ($relative + ' is missing')
        return
    }
    $text = Read-RepoText (Get-RepoPath $relative)
    $versionMatch = [regex]::Match($text, '(?m)^  version:[ \t]*([^\r\n]+)')
    $version = ''
    if ($versionMatch.Success) { $version = $versionMatch.Groups[1].Value.Trim() }
    $frozenMatch = [regex]::Match($text, '(?m)^x-frozen:[ \t]*([^\r\n]+)')
    $frozen = ''
    if ($frozenMatch.Success) { $frozen = $frozenMatch.Groups[1].Value.Trim() }
    $problems = New-Object System.Collections.Generic.List[string]
    if ($version -ne '0.2.0-p8') { $problems.Add("info.version is '$version' (expected 0.2.0-p8)") }
    if ($frozen -ne 'P8') { $problems.Add("x-frozen is '$frozen' (expected P8)") }
    if ($problems.Count -gt 0) {
        Write-Fail $name ($relative + ': ' + ($problems -join '; '))
        return
    }
    Write-Pass $name 'openapi info.version = 0.2.0-p8 and x-frozen: P8'
}

function Check-ContractGate {
    if (-not $script:NodeReady) {
        Write-Fail 'p8-contract' 'not run: node/frontend dependencies are unavailable (see node-tooling/frontend-deps)'
        return
    }
    if (-not (Test-RepoLeaf 'scripts/p8-contract-check.cjs')) {
        Write-Fail 'p8-contract' 'scripts/p8-contract-check.cjs is missing'
        return
    }
    $result = Invoke-NodeScript -ScriptPath 'scripts/p8-contract-check.cjs' -WorkingDirectory $repoRoot
    if ($result.ExitCode -eq 0) {
        Write-Pass 'p8-contract' 'scripts/p8-contract-check.cjs passed (frozen P8 contract and P2-P7 forward compatibility)'
        return
    }
    Write-Fail 'p8-contract' ('scripts/p8-contract-check.cjs exited ' + $result.ExitCode)
    Write-Host '      last contract-gate lines:' -ForegroundColor DarkGray
    foreach ($line in (@($result.Lines) | Select-Object -Last 20)) {
        Write-Host ('      ' + $line) -ForegroundColor DarkGray
    }
}

function Check-Migrations {
    $name = 'migrations'
    $relative = 'backend/internal/db/migrations/order.txt'
    if (-not (Test-RepoLeaf $relative)) {
        Write-Fail $name ($relative + ' is missing')
        return
    }
    $count = 0
    foreach ($line in (Get-TextLines (Read-RepoText (Get-RepoPath $relative)))) {
        $trimmed = $line.Trim()
        if ($trimmed.Length -eq 0) { continue }
        if ($trimmed.StartsWith('#')) { continue }
        $count++
    }
    if ($count -ne 42) {
        Write-Fail $name ('order.txt lists ' + $count + ' migrations (expected 42 for frozen P8)')
        return
    }
    Write-Pass $name 'order.txt lists the 42 frozen P8 migrations'
}

function Check-Settings {
    $name = 'settings'
    $relative = 'backend/internal/settings/application/catalog.go'
    if (-not (Test-RepoLeaf $relative)) {
        Write-Fail $name ($relative + ' is missing')
        return
    }
    $text = Read-RepoText (Get-RepoPath $relative)
    $count = ([regex]::Matches($text, '\{Key: "')).Count
    if ($count -ne 85) {
        Write-Fail $name ('catalog.go declares ' + $count + ' settings (expected 85 for frozen P8)')
        return
    }
    Write-Pass $name 'settings catalog declares the 85 frozen P8 settings'
}

# ---------------------------------------------------------------------------
# Deploy shape.
# ---------------------------------------------------------------------------
function Get-ComposeServiceNames {
    param([string]$Text)
    $names = New-Object System.Collections.Generic.List[string]
    $inServices = $false
    foreach ($line in (Get-TextLines $Text)) {
        if (-not $inServices) {
            if ($line -match '^services:[ \t]*$') { $inServices = $true }
            continue
        }
        if ($line -match '^[^\s#]') { break }
        $matchResult = [regex]::Match($line, '^  ([A-Za-z0-9_.-]+):[ \t]*$')
        if ($matchResult.Success) { $names.Add($matchResult.Groups[1].Value) }
    }
    return $names
}

function Check-ComposeFile {
    $name = 'compose-services'
    $relative = 'deploy/docker-compose.yml'
    if (-not (Test-RepoLeaf $relative)) {
        Write-Fail $name ($relative + ' is missing')
        return
    }
    $expected = @('api', 'api2', 'executor', 'postgres', 'renderer', 'web')
    $names = @(Get-ComposeServiceNames -Text (Read-RepoText (Get-RepoPath $relative)))
    $missing = @($expected | Where-Object { $names -notcontains $_ })
    $unexpected = @($names | Where-Object { $expected -notcontains $_ })
    $problems = New-Object System.Collections.Generic.List[string]
    if ($missing.Count -gt 0) { $problems.Add('missing ' + ($missing -join ', ')) }
    if ($unexpected.Count -gt 0) { $problems.Add('unexpected ' + ($unexpected -join ', ')) }
    if ($names.Count -ne 6) { $problems.Add('found ' + $names.Count + ' services (expected 6)') }
    if ($problems.Count -gt 0) {
        Write-Fail $name ($relative + ': ' + ($problems -join '; '))
        return
    }
    Write-Pass $name 'deploy/docker-compose.yml declares the six P8 services'
}

function Check-ComposeValidator {
    if (-not $script:NodeReady) {
        Write-Fail 'compose-validator' 'not run: node/frontend dependencies are unavailable (see node-tooling/frontend-deps)'
        return
    }
    if (-not (Test-RepoLeaf 'frontend/scripts/validate-compose.mjs')) {
        Write-Fail 'compose-validator' 'frontend/scripts/validate-compose.mjs is missing'
        return
    }
    $result = Invoke-NodeScript -ScriptPath 'scripts/validate-compose.mjs' -WorkingDirectory (Get-RepoPath 'frontend')
    if ($result.ExitCode -eq 0) {
        Write-Pass 'compose-validator' 'frontend/scripts/validate-compose.mjs passed (static validation only, Docker not required)'
        return
    }
    Write-Fail 'compose-validator' ('frontend/scripts/validate-compose.mjs exited ' + $result.ExitCode)
    Write-Host '      last compose-validation lines:' -ForegroundColor DarkGray
    foreach ($line in (@($result.Lines) | Select-Object -Last 20)) {
        Write-Host ('      ' + $line) -ForegroundColor DarkGray
    }
}

# ---------------------------------------------------------------------------
# Root documentation status.
# ---------------------------------------------------------------------------
function Check-RootDocStatus {
    param(
        [string]$Name,
        [string]$RelativePath,
        [string]$StatusPattern,
        [string]$StatusLabel
    )
    if (-not (Test-RepoLeaf $RelativePath)) {
        Write-Fail $Name ($RelativePath + ' is missing')
        return
    }
    $text = Read-RepoText (Get-RepoPath $RelativePath)
    $problems = New-Object System.Collections.Generic.List[string]
    if ($text -match '(?i)v0\.1-preview') { $problems.Add('still advertises v0.1-preview') }
    if ($text -notmatch $StatusPattern) { $problems.Add('does not record the ' + $StatusLabel + ' status') }
    if ($problems.Count -gt 0) {
        Write-Fail $Name ($RelativePath + ': ' + ($problems -join '; '))
        return
    }
    Write-Pass $Name ($RelativePath + ' records the ' + $StatusLabel + ' status')
}

# ---------------------------------------------------------------------------
# CI de-0.1-ification.
# ---------------------------------------------------------------------------
function Get-CiJobBlock {
    param([string[]]$Lines, [string]$JobName)
    $start = -1
    for ($index = 0; $index -lt $Lines.Count; $index++) {
        if ($Lines[$index] -match ('^  ' + [regex]::Escape($JobName) + ':[ \t]*$')) {
            $start = $index
            break
        }
    }
    if ($start -lt 0) { return $null }
    $block = New-Object System.Collections.Generic.List[string]
    for ($index = $start + 1; $index -lt $Lines.Count; $index++) {
        if ($Lines[$index] -match '^  [A-Za-z0-9_-]+:[ \t]*$') { break }
        $block.Add($Lines[$index])
    }
    return ($block -join "`n")
}

function Check-CiWorkflow {
    $name = 'ci-workflow'
    $relative = '.github/workflows/ci.yml'
    if (-not (Test-RepoLeaf $relative)) {
        Write-Fail $name ($relative + ' is missing')
        return
    }
    $text = Read-RepoText (Get-RepoPath $relative)
    $lines = Get-TextLines $text
    $problems = New-Object System.Collections.Generic.List[string]

    $testBlock = Get-CiJobBlock -Lines $lines -JobName 'test'
    if ($null -eq $testBlock) {
        $problems.Add('the Ubuntu test job is missing')
    } elseif ($testBlock -notmatch 'runs-on:[ \t]*ubuntu-latest') {
        $problems.Add('the test job no longer runs on ubuntu-latest')
    }

    $staticBlock = Get-CiJobBlock -Lines $lines -JobName 'static-acceptance'
    if ($null -eq $staticBlock) {
        $problems.Add('the static-acceptance job is missing')
    } else {
        if ($staticBlock -notmatch 'scripts/p8-static-check\.ps1') {
            $problems.Add('static-acceptance does not run scripts/p8-static-check.ps1')
        }
        if ($staticBlock -notmatch 'pnpm install --frozen-lockfile') {
            $problems.Add('static-acceptance does not install the frontend dependencies')
        }
        if ($staticBlock -notmatch 'working-directory:[ \t]*frontend') {
            $problems.Add('static-acceptance does not install the frontend dependencies from frontend/')
        }
    }

    $forbiddenNames = New-Object System.Collections.Generic.List[string]
    foreach ($matchResult in [regex]::Matches($text, '(?i)(?<![\w-])(?:check-ci|check-docs|acceptance)\.ps1')) {
        if (-not $forbiddenNames.Contains($matchResult.Value)) {
            $forbiddenNames.Add($matchResult.Value)
        }
    }
    if ($forbiddenNames.Count -gt 0) {
        $problems.Add('still references 0.1 gate script(s): ' + ($forbiddenNames -join ', '))
    }
    if ($text -match '(?i)-StaticOnly\b') {
        $problems.Add('still references the 0.1 -StaticOnly mode')
    }
    foreach ($key in $script:LegacyEnvKeys) {
        if ($text -match ('(?i)(?<![A-Z0-9_])' + [regex]::Escape($key) + '(?![A-Z0-9_])')) {
            $problems.Add('still references the 0.1-only environment key ' + $key)
        }
    }

    if ($problems.Count -gt 0) {
        Write-Fail $name ($relative + ': ' + ($problems -join '; '))
        return
    }
    Write-Pass $name 'Ubuntu test job kept; static-acceptance runs scripts/p8-static-check.ps1; no 0.1 gate/env references'
}

function Check-P8Scripts {
    $name = 'p8-scripts'
    $scriptsDir = Get-RepoPath 'scripts'
    if (-not (Test-Path -LiteralPath $scriptsDir -PathType Container)) {
        Write-Fail $name 'scripts/ directory is missing'
        return
    }
    $offenses = New-Object System.Collections.Generic.List[string]
    $checked = 0
    foreach ($file in @(Get-ChildItem -LiteralPath $scriptsDir -File -Filter 'p8-*.ps1')) {
        if ($file.Name -eq 'p8-static-check.ps1') { continue }
        $checked++
        $lineNumber = 0
        foreach ($line in (Get-TextLines (Read-RepoText $file.FullName))) {
            $lineNumber++
            if ($line -match '(?i)-StaticOnly\b') {
                $offenses.Add((Get-RelativeDisplayPath $file.FullName) + ':' + $lineNumber + ' references -StaticOnly')
                continue
            }
            if ($line -match '(?i)(?:-File|&)\s+["'']?scripts[\\/](?:check-ci|check-docs|acceptance)\.ps1') {
                $offenses.Add((Get-RelativeDisplayPath $file.FullName) + ':' + $lineNumber + ' invokes a legacy 0.1 gate script')
            }
        }
    }
    if ($offenses.Count -gt 0) {
        Write-Fail $name ('P8 scripts invoke 0.1 gates: ' + (($offenses | Select-Object -First 10) -join '; '))
        return
    }
    Write-Pass $name ('no 0.1 gate invocation in ' + $checked + ' other P8 script(s)')
}

function Check-LegacyScriptsRetained {
    $name = 'legacy-scripts'
    $missing = @()
    foreach ($relative in @('scripts/check-ci.ps1', 'scripts/check-docs.ps1', 'scripts/acceptance.ps1')) {
        if (-not (Test-RepoLeaf $relative)) { $missing += $relative }
    }
    if ($missing.Count -gt 0) {
        Write-Fail $name ('legacy 0.1 scripts must be retained, not deleted; missing: ' + ($missing -join ', '))
        return
    }
    Write-Pass $name '0.1 gate scripts retained as legacy and not referenced by CI'
}

# ---------------------------------------------------------------------------
# P8 acceptance evidence documents.
# ---------------------------------------------------------------------------
function Check-AcceptanceFiles {
    $name = 'acceptance-p8'
    $directory = Get-RepoPath 'docs/version-V0.2/acceptance/p8'
    if (-not (Test-Path -LiteralPath $directory -PathType Container)) {
        Write-Fail $name ('docs/version-V0.2/acceptance/p8/ is missing; required: ' + ($script:RequiredAcceptanceFiles -join ', '))
        return
    }
    $missing = New-Object System.Collections.Generic.List[string]
    $empty = New-Object System.Collections.Generic.List[string]
    foreach ($fileName in $script:RequiredAcceptanceFiles) {
        $path = Join-Path $directory $fileName
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
            $missing.Add($fileName)
        } elseif ((Get-Item -LiteralPath $path).Length -eq 0) {
            $empty.Add($fileName)
        }
    }
    $problems = New-Object System.Collections.Generic.List[string]
    if ($missing.Count -gt 0) { $problems.Add('missing ' + ($missing -join ', ')) }
    if ($empty.Count -gt 0) { $problems.Add('empty ' + ($empty -join ', ')) }
    if ($problems.Count -gt 0) {
        Write-Fail $name ('docs/version-V0.2/acceptance/p8/: ' + ($problems -join '; '))
        return
    }
    Write-Pass $name ('docs/version-V0.2/acceptance/p8/ contains all ' + $script:RequiredAcceptanceFiles.Count + ' required documents')
}

# ---------------------------------------------------------------------------
# Secret-shaped values in tracked docs/config.
# ---------------------------------------------------------------------------
function Test-PlaceholderValue {
    param([string]$Value)
    $trimmed = $Value.Trim().Trim('"').Trim("'")
    if ($trimmed.Length -eq 0) { return $true }
    if ($trimmed.IndexOfAny([char[]]@('$', '<', '>', '{', '}', '%')) -ge 0) { return $true }
    # Dotted setting keys (for example security.audit_retention_days).
    if ($trimmed -match '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$') { return $true }
    if ($trimmed -match '^(?i)(?:x{4,}|\*{3,}|\.{3,}|_+)$') { return $true }
    $prefixes = @(
        'dev-', 'test-', 'example', 'sample', 'your_', 'your-', 'placeholder',
        'changeme', 'change-me', 'dummy', 'fake', 'redacted', 'eztest',
        'postgres', 'password', 'passwd', 'secret', 'token', 'todo', 'null',
        'none', 'local-', '0123456789abcdef', 'abcdef0123456789', 'deadbeef',
        'feedface'
    )
    foreach ($prefix in $prefixes) {
        if ($trimmed.StartsWith($prefix, [System.StringComparison]::OrdinalIgnoreCase)) { return $true }
    }
    return $false
}

function Get-SecretScanFiles {
    $files = New-Object System.Collections.Generic.List[string]
    $explicit = @(
        '.env.example',
        '.github/workflows/ci.yml',
        'deploy/docker-compose.yml',
        'deploy/README.md',
        'README.md',
        'README.zh-CN.md',
        'DEVELOPMENT_STATUS.md',
        'docs/version-V0.1/README.md'
    )
    foreach ($relative in $explicit) {
        $path = Get-RepoPath $relative
        if (Test-Path -LiteralPath $path -PathType Leaf) { $files.Add($path) }
    }
    foreach ($relative in @('docs/version-V0.2', 'docs/version-V0.1')) {
        $directory = Get-RepoPath $relative
        if (-not (Test-Path -LiteralPath $directory -PathType Container)) { continue }
        foreach ($file in @(Get-ChildItem -LiteralPath $directory -Recurse -File)) {
            if (@('.md', '.yaml', '.yml') -contains $file.Extension.ToLowerInvariant()) {
                $files.Add($file.FullName)
            }
        }
    }
    return $files
}

function Check-Secrets {
    $name = 'secrets'
    $files = @(Get-SecretScanFiles)
    if ($files.Count -eq 0) {
        Write-Fail $name 'no docs/config files found to scan'
        return
    }
    $patterns = @(
        [pscustomobject]@{ Name = 'private key block'; Regex = '-----BEGIN (?:RSA |EC |DSA |OPENSSH )?PRIVATE KEY-----'; CapturedValue = $false },
        [pscustomobject]@{ Name = 'AWS access key'; Regex = '\bAKIA[0-9A-Z]{16}\b'; CapturedValue = $false },
        [pscustomobject]@{ Name = 'GitHub token'; Regex = '\bgh[pousr]_[A-Za-z0-9]{36,}\b'; CapturedValue = $false },
        [pscustomobject]@{ Name = 'Slack token'; Regex = '\bxox[baprs]-[A-Za-z0-9-]{10,}\b'; CapturedValue = $false },
        [pscustomobject]@{ Name = 'provider API key'; Regex = '\bsk-[A-Za-z0-9_-]{20,}\b'; CapturedValue = $false },
        [pscustomobject]@{ Name = 'JWT'; Regex = '\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b'; CapturedValue = $false },
        [pscustomobject]@{ Name = 'bearer credential'; Regex = '(?i)\bBearer\s+([A-Za-z0-9_\-\.=+/]{20,})'; CapturedValue = $true },
        [pscustomobject]@{ Name = 'credential assignment'; Regex = '(?i)(?:^|[^A-Za-z0-9-])(?:api[_-]?key|secret|password|passwd|access[_-]?token|auth[_-]?token|private[_-]?key|token)["'']?[ \t]*[:=][ \t]*["'']?([A-Za-z0-9_][A-Za-z0-9_\-\./+=]{15,})'; CapturedValue = $true },
        [pscustomobject]@{ Name = 'database URL password'; Regex = '(?i)\b(?:postgres(?:ql)?|mysql|mongodb(?:\+srv)?|redis)://[^\s:/@]+:([^\s@/]{8,})@'; CapturedValue = $true }
    )
    $findings = New-Object System.Collections.Generic.List[string]
    foreach ($file in $files) {
        $lineNumber = 0
        foreach ($line in (Get-TextLines (Read-RepoText $file))) {
            $lineNumber++
            foreach ($pattern in $patterns) {
                foreach ($matchResult in [regex]::Matches($line, $pattern.Regex)) {
                    if ($pattern.CapturedValue) {
                        # Skip shell/Compose interpolation and placeholder markers
                        # (for example ${EZTEST_RENDER_TOKEN:-...}) before looking
                        # at the captured value.
                        if ($matchResult.Value.IndexOfAny([char[]]@('$', '{', '}', '<', '>', '%')) -ge 0) { continue }
                        $value = $matchResult.Groups[1].Value
                        if (Test-PlaceholderValue $value) { continue }
                    }
                    $findings.Add((Get-RelativeDisplayPath $file) + ':' + $lineNumber + ' (' + $pattern.Name + ')')
                }
            }
        }
    }
    if ($findings.Count -gt 0) {
        $shown = @($findings | Select-Object -First 25)
        $suffix = ''
        if ($findings.Count -gt $shown.Count) { $suffix = ' (+' + ($findings.Count - $shown.Count) + ' more)' }
        Write-Fail $name ('secret-shaped value(s) in tracked docs/config (values not printed): ' + ($shown -join '; ') + $suffix)
        return
    }
    Write-Pass $name ('no secret-shaped values in ' + $files.Count + ' scanned docs/config files')
}

# ---------------------------------------------------------------------------
# Relative Markdown links in the new P8/root documentation.
# ---------------------------------------------------------------------------
function Get-MarkdownLinkTargets {
    param([string]$Text)
    $targets = New-Object System.Collections.Generic.List[string]
    $inFence = $false
    foreach ($line in (Get-TextLines $Text)) {
        if ($line -match '^\s*(?:```|~~~)') {
            $inFence = -not $inFence
            continue
        }
        if ($inFence) { continue }
        $withoutCode = [regex]::Replace($line, '`[^`]*`', '')
        foreach ($matchResult in [regex]::Matches($withoutCode, '!?\[[^\]]*\]\(([^)\r\n]+)\)')) {
            $targets.Add($matchResult.Groups[1].Value.Trim())
        }
    }
    return $targets
}

function Get-LinkCheckFiles {
    $files = New-Object System.Collections.Generic.List[string]
    foreach ($relative in @(
        'README.md',
        'README.zh-CN.md',
        'DEVELOPMENT_STATUS.md',
        'deploy/README.md',
        'docs/version-V0.1/README.md'
    )) {
        $path = Get-RepoPath $relative
        if (Test-Path -LiteralPath $path -PathType Leaf) { $files.Add($path) }
    }
    $p8Directory = Get-RepoPath 'docs/version-V0.2/acceptance/p8'
    if (Test-Path -LiteralPath $p8Directory -PathType Container) {
        foreach ($file in @(Get-ChildItem -LiteralPath $p8Directory -Recurse -File -Filter '*.md')) {
            $files.Add($file.FullName)
        }
    }
    return $files
}

function Check-MarkdownLinks {
    $name = 'doc-links'
    $files = @(Get-LinkCheckFiles)
    if ($files.Count -eq 0) {
        Write-Fail $name 'no root/P8 Markdown documents found to check'
        return
    }
    $broken = New-Object System.Collections.Generic.List[string]
    $checked = 0
    foreach ($file in $files) {
        $fileDirectory = Split-Path -Parent $file
        foreach ($rawTarget in @(Get-MarkdownLinkTargets -Text (Read-RepoText $file))) {
            $target = $rawTarget.Trim()
            if ($target.Length -eq 0) { continue }
            if ($target.StartsWith('<')) {
                $end = $target.IndexOf('>')
                if ($end -lt 1) { continue }
                $target = $target.Substring(1, $end - 1).Trim()
            } else {
                $parts = @($target -split '\s+')
                if ($parts.Count -gt 0) { $target = $parts[0] }
            }
            if ($target.Length -eq 0) { continue }
            if ($target -match '^[A-Za-z][A-Za-z0-9+.-]*:') { continue }
            if ($target.StartsWith('#')) { continue }
            if ($target.StartsWith('//')) { continue }
            if ($target -match '\{[^}]*\}') { continue }
            $fragmentIndex = $target.IndexOf('#')
            if ($fragmentIndex -ge 0) { $target = $target.Substring(0, $fragmentIndex) }
            $target = $target.Trim()
            if ($target.Length -eq 0) { continue }
            try {
                $target = [System.Uri]::UnescapeDataString($target)
            } catch {
                # Keep the raw target when it is not valid percent-encoding.
            }
            if ($target -match '^[A-Za-z]:[\\/]') { continue }
            $baseDirectory = $fileDirectory
            if ($target.StartsWith('/')) {
                $baseDirectory = $repoRoot
                $target = $target.TrimStart('/')
            }
            try {
                $resolved = [System.IO.Path]::GetFullPath([System.IO.Path]::Combine($baseDirectory, $target))
            } catch {
                $broken.Add((Get-RelativeDisplayPath $file) + ' -> ' + $target)
                continue
            }
            $checked++
            if (-not (Test-Path -LiteralPath $resolved)) {
                $broken.Add((Get-RelativeDisplayPath $file) + ' -> ' + $target)
            }
        }
    }
    if ($broken.Count -gt 0) {
        $shown = @($broken | Select-Object -First 25)
        $suffix = ''
        if ($broken.Count -gt $shown.Count) { $suffix = ' (+' + ($broken.Count - $shown.Count) + ' more)' }
        Write-Fail $name ('broken relative link(s): ' + ($shown -join '; ') + $suffix)
        return
    }
    Write-Pass $name ('{0} relative link(s) in {1} root/P8 document(s) resolve' -f $checked, $files.Count)
}

# ---------------------------------------------------------------------------
# Run every check; continue after failures so the report lists them all.
# ---------------------------------------------------------------------------
try {
    Check-EnvExample
    Check-DotEnv
    Check-OpenApiFrozen
    Initialize-NodeToolchain
    Check-ContractGate
    Check-Migrations
    Check-Settings
    Check-ComposeFile
    Check-ComposeValidator
    Check-RootDocStatus -Name 'readme-en' -RelativePath 'README.md' -StatusPattern '(?i)v0\.2' -StatusLabel 'v0.2 (P8)'
    Check-RootDocStatus -Name 'readme-zh' -RelativePath 'README.zh-CN.md' -StatusPattern '(?i)v0\.2' -StatusLabel 'v0.2 (P8)'
    Check-RootDocStatus -Name 'dev-status' -RelativePath 'DEVELOPMENT_STATUS.md' -StatusPattern '(?i)v?0\.2' -StatusLabel 'v0.2 (P8)'
    Check-CiWorkflow
    Check-P8Scripts
    Check-LegacyScriptsRetained
    Check-AcceptanceFiles
    Check-Secrets
    Check-MarkdownLinks
} catch {
    Write-Fail 'internal' ('unexpected error: ' + $_.Exception.Message)
}

Write-Host ''
if ($script:Failed -gt 0) {
    Write-Host ('RESULT: FAIL - {0} passed, {1} failed' -f $script:Passed, $script:Failed) -ForegroundColor Red
    exit 1
}
Write-Host ('RESULT: PASS - {0} checks passed' -f $script:Passed) -ForegroundColor Green
exit 0
