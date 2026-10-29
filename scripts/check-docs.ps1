#requires -Version 5.1
<#
.SYNOPSIS
    Static P8 documentation / OpenAPI / Compose contract for EzTest.

.DESCRIPTION
    Repository-local checks only (no database, API, Docker or Playwright):

      1. the four P8 documentation categories (deployment, operations,
         user-guide, development) plus the P8 release notes exist and are
         non-trivial;
      2. DEVELOPMENT_STATUS.md and .env.example document the P8 environment keys;
      3. docs/openapi/api.yaml parses with the frontend `yaml` dependency and
         contains the 18 P8 paths (plus the additive batchTimeline contract and
         the case.visual_baseline_approved event);
      4. deploy/docker-compose.yml has the four services, six named volumes,
         api volume mounts, health checks, startup ordering and the Dockerfiles
         it builds;
      5. the tracked documentation contains no secret-shaped values and .env is
         gitignored/untracked;
      6. local Markdown links in the P8 documents resolve to existing files.

    The script needs Node plus frontend/node_modules/yaml (the same dependency
    already used by scripts/acceptance.ps1) for steps 3 and 4; it fails with an
    actionable error instead of silently skipping when they are missing. It is
    wired into `scripts/acceptance.ps1 -StaticOnly`; a non-zero exit fails that
    gate. This is a static contract check, not proof that Docker was run.

.NOTES
    Compatible with Windows PowerShell 5.1 and PowerShell 7+.
#>
[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$frontendDir = Join-Path $repoRoot "frontend"
$failures = New-Object System.Collections.ArrayList

function Add-Failure {
    param([string]$Message)
    [void]$failures.Add($Message)
    Write-Host "FAIL $Message" -ForegroundColor Red
}

function Assert-True {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) {
        Add-Failure $Message
    }
}

function Assert-File {
    param([string]$Relative)
    $path = Join-Path $repoRoot $Relative
    Assert-True (Test-Path -LiteralPath $path) "missing required file $Relative"
    return (Test-Path -LiteralPath $path)
}

function Read-Text {
    param([string]$Relative)
    $path = Join-Path $repoRoot $Relative
    if (-not (Test-Path -LiteralPath $path)) {
        throw "missing required file $Relative"
    }
    return Get-Content -LiteralPath $path -Raw -Encoding UTF8
}

function Assert-Contains {
    param([string]$Relative, [string]$Text, [string]$Needle, [string]$Description)
    if (-not $Text.Contains($Needle)) {
        Add-Failure "$Relative is missing $Description ($Needle)"
    }
}

function Invoke-NodeCheck {
    param([string]$Script, [string[]]$Arguments, [string]$Label)
    $nodeCommand = Get-Command node -ErrorAction SilentlyContinue
    if ($null -eq $nodeCommand) {
        throw "node is required for the $Label check; install Node.js 22+ and retry"
    }
    $nodeExe = $nodeCommand.Source
    if ([string]::IsNullOrWhiteSpace($nodeExe)) {
        $nodeExe = $nodeCommand.Path
    }
    $yamlModule = Join-Path $frontendDir "node_modules\yaml"
    if (-not (Test-Path -LiteralPath $yamlModule)) {
        throw "frontend/node_modules/yaml is missing; run 'pnpm install' in frontend (the $Label check parses YAML with that dependency)"
    }
    Push-Location $frontendDir
    $previousPreference = $ErrorActionPreference
    # Windows PowerShell 5.1 raises a non-terminating NativeCommandError for
    # native stderr under Stop. Continue + 2>&1 captures the diagnostic so the
    # exit code and the Node message can both be reported.
    $ErrorActionPreference = "Continue"
    try {
        $output = & $nodeExe -e $Script @Arguments 2>&1
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousPreference
        Pop-Location
    }
    if ($code -ne 0) {
        $detail = ($output | ForEach-Object { [string]$_ }) -join " | "
        throw "${Label} check failed: $detail"
    }
    foreach ($line in @($output)) {
        if (-not [string]::IsNullOrWhiteSpace([string]$line)) {
            Write-Host ([string]$line)
        }
    }
}

# ---------------------------------------------------------------------------
# 1. The four P8 documentation categories + release notes.
# ---------------------------------------------------------------------------
Write-Host "==> P8 documentation categories"
$documentation = @(
    [pscustomobject]@{ Path = "docs/deployment.md"; Needle = "Compose"; Label = "deployment guide" },
    [pscustomobject]@{ Path = "docs/operations.md"; Needle = "SLO"; Label = "operations runbook" },
    [pscustomobject]@{ Path = "docs/user-guide.md"; Needle = "EZTEST_KNOWLEDGE_MAX_INJECT"; Label = "user guide" },
    [pscustomobject]@{ Path = "docs/development.md"; Needle = "check-docs.ps1"; Label = "development guide" },
    [pscustomobject]@{ Path = "docs/release-notes/p8.md"; Needle = "P8"; Label = "P8 release notes" }
)
foreach ($doc in $documentation) {
    $exists = Assert-File -Relative $doc.Path
    if (-not $exists) { continue }
    $text = Read-Text -Relative $doc.Path
    Assert-True ($text.Length -ge 1500) "$($doc.Path) is too short to be the $($doc.Label)"
    Assert-Contains -Relative $doc.Path -Text $text -Needle $doc.Needle -Description "the $($doc.Label) marker"
}
$deployReadme = Read-Text -Relative "deploy/README.md"
foreach ($needle in @("validate-compose.mjs", "executor", "evidence", "reports", "exports")) {
    Assert-Contains -Relative "deploy/README.md" -Text $deployReadme -Needle $needle -Description "the P8 deployment contract"
}

# ---------------------------------------------------------------------------
# 2. README / .env.example environment keys.
# ---------------------------------------------------------------------------
Write-Host "==> P8 environment keys"
$readme = Read-Text -Relative "DEVELOPMENT_STATUS.md"
foreach ($key in @(
        "EZTEST_KNOWLEDGE_MAX_INJECT",
        "EZTEST_KNOWLEDGE_MAX_BYTES",
        "EZTEST_VISUAL_DIFF_THRESHOLD",
        "EZTEST_VISUAL_BASELINE_MAX_BYTES",
        "EZTEST_RATE_LIMIT_RPS",
        "EZTEST_RATE_LIMIT_BURST",
        "EZTEST_RATE_LIMIT_SSE_MAX",
        "EZTEST_GC_ENABLED",
        "EZTEST_GC_RETENTION_DAYS",
        "EZTEST_GC_BATCH_KEEP",
        "EZTEST_INSTANCE_ID",
        "EZTEST_LEADER_LOCK_KEY",
        "EZTEST_AUTONOMY_BATCHES_PER_TICK"
    )) {
    Assert-Contains -Relative "DEVELOPMENT_STATUS.md" -Text $readme -Needle $key -Description "the P8 environment table entry"
}
$envTemplate = Read-Text -Relative ".env.example"
foreach ($key in @(
        "EZTEST_KNOWLEDGE_MAX_INJECT",
        "EZTEST_KNOWLEDGE_MAX_BYTES",
        "EZTEST_VISUAL_DIFF_THRESHOLD",
        "EZTEST_VISUAL_BASELINE_MAX_BYTES",
        "EZTEST_AUTONOMY_BATCHES_PER_TICK",
        "EZTEST_RATE_LIMIT_RPS",
        "EZTEST_GC_ENABLED",
        "EZTEST_EVIDENCE_DIR",
        "EZTEST_REPORT_DIR",
        "EZTEST_EXPORT_DIR"
    )) {
    Assert-Contains -Relative ".env.example" -Text $envTemplate -Needle $key -Description "the P8 template key"
}
Assert-True ($envTemplate.Contains("EZTEST_KNOWLEDGE_MAX_INJECT=5")) ".env.example must default EZTEST_KNOWLEDGE_MAX_INJECT=5"
Assert-True ($envTemplate.Contains("EZTEST_KNOWLEDGE_MAX_BYTES=4096")) ".env.example must default EZTEST_KNOWLEDGE_MAX_BYTES=4096"
Assert-True ($envTemplate.Contains("EZTEST_VISUAL_DIFF_THRESHOLD=0.005")) ".env.example must reserve EZTEST_VISUAL_DIFF_THRESHOLD=0.005"
Assert-True ($envTemplate.Contains("EZTEST_VISUAL_BASELINE_MAX_BYTES=5242880")) ".env.example must reserve EZTEST_VISUAL_BASELINE_MAX_BYTES=5242880"

# ---------------------------------------------------------------------------
# 3. OpenAPI YAML parses and carries the P8 paths.
# ---------------------------------------------------------------------------
Write-Host "==> OpenAPI P8 paths"
$openapiPath = Join-Path $repoRoot "docs/openapi/api.yaml"
Assert-True (Test-Path -LiteralPath $openapiPath) "missing required file docs/openapi/api.yaml"
if (Test-Path -LiteralPath $openapiPath) {
    $openapiScript = @'
const fs = require('fs');
const YAML = require('yaml');
const file = process.argv[1];
const doc = YAML.parse(fs.readFileSync(file, 'utf8'));
if (!doc || typeof doc !== 'object' || !doc.paths || !doc.components || !doc.components.schemas) {
  throw new Error('invalid OpenAPI document: paths/components.schemas are required');
}
const required = [
  '/api/versions/{versionId}/exploration-chain',
  '/api/search',
  '/api/todos',
  '/api/agent-center/catalog',
  '/api/agent-center/sessions',
  '/api/agent-center/quality',
  '/api/versions/{id}/knowledge',
  '/api/versions/{id}/knowledge/{kid}',
  '/api/versions/{id}/knowledge/{kid}/review',
  '/api/knowledge',
  '/api/code/repos/{repoId}/quality',
  '/api/code/repos/{repoId}/facts/{factId}/reviews',
  '/api/quality/eval-runs',
  '/api/quality/eval-runs/{id}',
  '/api/admin/gc',
  '/api/versions/{versionId}/code/changes',
  '/api/versions/{versionId}/mindmap/assertions/{assertionId}/visual',
  '/api/versions/{versionId}/mindmap/assertions/{assertionId}/visual/baseline',
];
const missing = required.filter((p) => !doc.paths[p]);
if (missing.length > 0) {
  throw new Error('missing P8 OpenAPI path(s): ' + missing.join(', '));
}
const schemaText = JSON.stringify(doc.components.schemas);
if (schemaText.indexOf('batchTimeline') < 0) {
  throw new Error('components.schemas does not describe batchTimeline');
}
const changesText = JSON.stringify(doc.paths['/api/versions/{versionId}/code/changes']);
if (changesText.indexOf('batchTimeline') < 0) {
  throw new Error('/api/versions/{versionId}/code/changes does not document batchTimeline');
}
if (schemaText.indexOf('case.visual_baseline_approved') < 0) {
  throw new Error('components.schemas does not publish the case.visual_baseline_approved event');
}
console.log('OpenAPI parsed: ' + Object.keys(doc.paths).length + ' paths, ' +
  Object.keys(doc.components.schemas).length + ' schemas, ' + required.length + ' P8 paths');
'@
    Invoke-NodeCheck -Script $openapiScript -Arguments @($openapiPath) -Label "OpenAPI YAML"
}

# ---------------------------------------------------------------------------
# 4. Compose services, volumes, health checks and Dockerfiles.
# ---------------------------------------------------------------------------
Write-Host "==> Compose P8 contract"
$composeScript = @'
const fs = require('fs');
const path = require('path');
const YAML = require('yaml');
const repoRoot = process.argv[1];
const composePath = path.join(repoRoot, 'deploy', 'docker-compose.yml');
const doc = YAML.parse(fs.readFileSync(composePath, 'utf8'));
const problems = [];
const check = (condition, message) => { if (!condition) problems.push(message); };
const services = (doc && doc.services) || {};
for (const name of ['api', 'web', 'postgres', 'executor']) {
  check(Boolean(services[name]), 'services.' + name + ' is missing');
}
for (const name of ['api', 'web', 'postgres', 'executor']) {
  const healthcheck = (services[name] || {}).healthcheck || {};
  check(Boolean(healthcheck.test), name + ' healthcheck.test is missing');
  check(Boolean(healthcheck.interval), name + ' healthcheck.interval is missing');
  check(Boolean(healthcheck.retries), name + ' healthcheck.retries is missing');
}
const volumes = (doc && doc.volumes) || {};
for (const name of ['pgdata', 'objects', 'attachments', 'evidence', 'reports', 'exports']) {
  check(Object.prototype.hasOwnProperty.call(volumes, name), 'top-level volume ' + name + ' is missing');
}
const mounts = (service) => ((service || {}).volumes || []).map((entry) => String(entry).split(':')).map((parts) => ({
  source: parts[0],
  target: parts[1] || '',
}));
const apiMounts = mounts(services.api);
for (const pair of [
  ['objects', '/data/objects'],
  ['attachments', '/data/version-attachments'],
  ['evidence', '/data/evidence'],
  ['reports', '/data/reports'],
  ['exports', '/data/exports'],
]) {
  const volume = pair[0];
  const target = pair[1];
  check(apiMounts.some((mount) => mount.source === volume && mount.target === target),
    'api must mount ' + volume + ' at ' + target);
}
const dependsHealthy = (service, target) => {
  const dependsOn = (service || {}).depends_on;
  return Boolean(dependsOn && dependsOn[target] && dependsOn[target].condition === 'service_healthy');
};
check(dependsHealthy(services.api, 'postgres'), 'api must depend_on postgres (service_healthy)');
check(dependsHealthy(services.web, 'api'), 'web must depend_on api (service_healthy)');
check(!(services.executor || {}).ports, 'executor must not publish host ports');
check(((services.executor || {}).expose || []).map(String).indexOf('13336') >= 0,
  'executor must expose 13336');
for (const dockerfile of ['deploy/Dockerfile.api', 'deploy/Dockerfile.web', 'executor/Dockerfile']) {
  check(fs.existsSync(path.join(repoRoot, dockerfile)), 'missing ' + dockerfile);
}
if (problems.length > 0) {
  throw new Error(problems.join('; '));
}
console.log('Compose contract OK: 4 services, 6 volumes, 4 health checks, executor internal-only, Dockerfiles present');
'@
Invoke-NodeCheck -Script $composeScript -Arguments @($repoRoot) -Label "Compose contract"

# ---------------------------------------------------------------------------
# 5. No committed secrets; .env stays gitignored.
# ---------------------------------------------------------------------------
Write-Host "==> secret scan"
$secretFiles = @(
    "README.md",
    "README.zh-CN.md",
    "DEVELOPMENT_STATUS.md",
    ".env.example",
    "deploy/README.md",
    "docs/deployment.md",
    "docs/operations.md",
    "docs/user-guide.md",
    "docs/development.md",
    "docs/release-notes/p8.md",
    "docs/openapi/api.yaml"
)
$secretPatterns = @(
    [pscustomobject]@{ Name = "private key"; Pattern = "-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----" },
    [pscustomobject]@{ Name = "OpenAI-style API key"; Pattern = "sk-[A-Za-z0-9_-]{20,}" },
    [pscustomobject]@{ Name = "GitHub token"; Pattern = "(ghp|gho|ghs|ghu)_[A-Za-z0-9]{20,}" },
    [pscustomobject]@{ Name = "AWS access key"; Pattern = "AKIA[0-9A-Z]{16}" },
    [pscustomobject]@{ Name = "Slack token"; Pattern = "xox[baprs]-[A-Za-z0-9-]{10,}" },
    [pscustomobject]@{ Name = "literal bearer token"; Pattern = "Bearer [A-Za-z0-9._-]{30,}" }
)
foreach ($relative in $secretFiles) {
    if (-not (Test-Path -LiteralPath (Join-Path $repoRoot $relative))) { continue }
    $text = Read-Text -Relative $relative
    foreach ($secretPattern in $secretPatterns) {
        if ($text -match $secretPattern.Pattern) {
            Add-Failure "$relative contains a $($secretPattern.Name) pattern (never commit credentials)"
        }
    }
}
Assert-True ($envTemplate.Contains('EZTEST_SECRET_KEYS={"1":"<base64-encoded-32-byte-key>"}')) ".env.example must keep the EZTEST_SECRET_KEYS placeholder instead of a real key"
Assert-True ($envTemplate.Contains("EZTEST_EXECUTOR_TOKEN=<executor-token>")) ".env.example must keep the EZTEST_EXECUTOR_TOKEN placeholder"
Assert-True ($envTemplate.Contains("POSTGRES_PASSWORD=<db-password>")) ".env.example must keep the POSTGRES_PASSWORD placeholder"
$gitCommand = Get-Command git -ErrorAction SilentlyContinue
if ($null -ne $gitCommand) {
    $gitExe = $gitCommand.Source
    if ([string]::IsNullOrWhiteSpace($gitExe)) { $gitExe = $gitCommand.Path }
    $trackedEnv = @(& $gitExe -C $repoRoot ls-files -- ".env" 2>$null)
    if ($trackedEnv.Count -gt 0) {
        Add-Failure ".env must never be tracked by git"
    }
    & $gitExe -C $repoRoot check-ignore -q ".env" 2>$null
    if ($LASTEXITCODE -ne 0) {
        Add-Failure ".env is not gitignored"
    }
} else {
    Write-Host "git is not available; skipped the .env tracking check"
}

# ---------------------------------------------------------------------------
# 6. Local Markdown links in the P8 documentation.
# ---------------------------------------------------------------------------
Write-Host "==> local Markdown links"
$linkFiles = @(
    "README.md",
    "README.zh-CN.md",
    "DEVELOPMENT_STATUS.md",
    "deploy/README.md",
    "docs/deployment.md",
    "docs/operations.md",
    "docs/user-guide.md",
    "docs/development.md",
    "docs/release-notes/p8.md"
)
$linkPattern = [regex]"\[[^\]]*\]\(([^)]+)\)"
$linkCount = 0
foreach ($relative in $linkFiles) {
    if (-not (Test-Path -LiteralPath (Join-Path $repoRoot $relative))) { continue }
    $text = Read-Text -Relative $relative
    foreach ($match in $linkPattern.Matches($text)) {
        $target = $match.Groups[1].Value.Trim()
        if ($target -match "^(https?:|mailto:|//|#)") { continue }
        $target = $target.Split("#")[0]
        if ([string]::IsNullOrWhiteSpace($target)) { continue }
        $linkCount++
        $baseDirectory = Split-Path -Parent (Join-Path $repoRoot $relative)
        $resolved = [System.IO.Path]::GetFullPath((Join-Path $baseDirectory $target))
        if (-not (Test-Path -LiteralPath $resolved)) {
            Add-Failure "$relative links to a missing local file: $target"
        }
    }
}
Write-Host "checked $linkCount local Markdown links"

# ---------------------------------------------------------------------------
# Summary.
# ---------------------------------------------------------------------------
if ($failures.Count -gt 0) {
    Write-Host ""
    Write-Host ("check-docs FAILED: {0} problem(s)" -f $failures.Count) -ForegroundColor Red
    exit 1
}
Write-Host ""
Write-Host "check-docs PASSED: 4 P8 documentation categories + release notes, DEVELOPMENT_STATUS/.env.example P8 keys, OpenAPI paths, Compose contract, no secrets, local Markdown links."
exit 0
