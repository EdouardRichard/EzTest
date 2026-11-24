#requires -Version 5.1
<#
.SYNOPSIS
    Static documentation / OpenAPI / Compose contract for EzTest.

.DESCRIPTION
    Repository-local checks only (no database, API, Docker or Playwright):

      1. the four documentation categories (deployment, operations,
         user-guide, development) plus the current release notes exist and are
         non-trivial;
      2. the root .env.example documents the four API runtime variables plus the
         first-start administrator credential;
      3. docs/openapi/api.yaml parses with the frontend `yaml` dependency and
         contains a representative set of current API paths;
      4. deploy/docker-compose.yml has the six services, the two named volumes
         (pgdata, data), the shared data mount on both API replicas, health
         checks, startup ordering and the Dockerfiles it builds;
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
    # Windows PowerShell 5.1 corrupts multi-line native arguments passed through
    # `node -e`: the script is truncated/mangled, so node runs an empty program
    # and exits 0 — a silent no-op that looks green. Write the script to a
    # temporary .cjs file inside frontend/ (so `require('yaml')` resolves) and run
    # it as a file instead. The target argument is read from process.argv[2]
    # (argv[1] is the script file path when running `node <file> <arg>`).
    $tempScript = Join-Path $frontendDir (".check-docs-" + [guid]::NewGuid().ToString("N") + ".cjs")
    [System.IO.File]::WriteAllText($tempScript, $Script, (New-Object System.Text.UTF8Encoding($false)))
    Push-Location $frontendDir
    $previousPreference = $ErrorActionPreference
    # Windows PowerShell 5.1 raises a non-terminating NativeCommandError for
    # native stderr under Stop. Continue + 2>&1 captures the diagnostic so the
    # exit code and the Node message can both be reported.
    $ErrorActionPreference = "Continue"
    try {
        $output = & $nodeExe $tempScript @Arguments 2>&1
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousPreference
        Pop-Location
        Remove-Item -LiteralPath $tempScript -Force -ErrorAction SilentlyContinue
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
Write-Host "==> documentation categories"
$documentation = @(
    [pscustomobject]@{ Path = "docs/deployment.md"; Needle = "Compose"; Label = "deployment guide" },
    [pscustomobject]@{ Path = "docs/operations.md"; Needle = "SLO"; Label = "operations runbook" },
    [pscustomobject]@{ Path = "docs/user-guide.md"; Needle = "knowledge.injection_max_items"; Label = "user guide" },
    [pscustomobject]@{ Path = "docs/development.md"; Needle = "check-docs.ps1"; Label = "development guide" },
    [pscustomobject]@{ Path = "docs/v0.2.4/20-v0.2.4-release-notes.md"; Needle = "P8"; Label = "release notes" }
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
# 2. Environment template: the four API runtime variables + first-start seed.
#    Runtime knobs moved to the DB settings center, so the application no longer
#    reads the old P8 EZTEST_* environment keys (knowledge/visual/rate-limit/GC/
#    instance/autonomy) — asserting them here would contradict the template's own
#    contract ("Do not add any other EZTEST_* variable for runtime configuration").
#    What remains real is the four runtime variables plus the administrator seed.
# ---------------------------------------------------------------------------
Write-Host "==> API runtime environment template"
$envTemplate = Read-Text -Relative ".env.example"
foreach ($key in @(
        "EZTEST_DATABASE_URL",
        "EZTEST_API_PORT",
        "EZTEST_WEB_PORT",
        "EZTEST_EXECUTOR_PORT",
        "EZTEST_INITIAL_ADMIN_PASSWORD"
    )) {
    Assert-Contains -Relative ".env.example" -Text $envTemplate -Needle $key -Description "the API runtime variable"
}

# ---------------------------------------------------------------------------
# 3. OpenAPI YAML parses and carries the P8 paths.
# ---------------------------------------------------------------------------
Write-Host "==> OpenAPI contract paths"
$openapiPath = Join-Path $repoRoot "docs/openapi/api.yaml"
Assert-True (Test-Path -LiteralPath $openapiPath) "missing required file docs/openapi/api.yaml"
if (Test-Path -LiteralPath $openapiPath) {
    $openapiScript = @'
const fs = require('fs');
const YAML = require('yaml');
const file = process.argv[2];
const doc = YAML.parse(fs.readFileSync(file, 'utf8'));
if (!doc || typeof doc !== 'object' || !doc.paths || !doc.components || !doc.components.schemas) {
  throw new Error('invalid OpenAPI document: paths/components.schemas are required');
}
const required = [
  '/api/versions/{versionId}/exploration-chain',
  '/api/search',
  '/api/todos',
  '/api/agent-center/catalog',
  '/api/agent-center/runs',
  '/api/agent-center/quality',
  '/api/knowledge/items',
  '/api/knowledge/items/{knowledgeId}',
  '/api/knowledge/items/{knowledgeId}/approve',
  '/api/knowledge/search',
  '/api/quality/trends',
  '/api/versions/{versionId}/code/facts/{factId}/reviews',
  '/api/quality/eval-runs',
  '/api/quality/eval-runs/{id}',
  '/api/admin/gc/runs',
  '/api/versions/{versionId}/code/changes',
];
const missing = required.filter((p) => !doc.paths[p]);
if (missing.length > 0) {
  throw new Error('missing OpenAPI path(s): ' + missing.join(', '));
}
console.log('OpenAPI parsed: ' + Object.keys(doc.paths).length + ' paths, ' +
  Object.keys(doc.components.schemas).length + ' schemas, ' + required.length + ' required paths present');
'@
    Invoke-NodeCheck -Script $openapiScript -Arguments @($openapiPath) -Label "OpenAPI YAML"
}

# ---------------------------------------------------------------------------
# 3b. v0.2.4 OpenAPI contract guards (T19): code-derived spec consistency.
#     These derive required/properties from Go json tags, bind each shared DTO to
#     every Go source that serves it (fail on projection disagreement), and check
#     query params per-operation against the handler's actual reads (both directions).
#     The mutant gate proves the deep guard catches the regression classes. The guards
#     are TRACKED and run in HOSTED CI via the node-only `openapi-contract` job in
#     .github/workflows/ci.yml (after the frontend pnpm install) — that is where they
#     block a regression. This check-docs section is a LOCAL convenience invocation only:
#     check-docs itself runs via acceptance.ps1 -StaticOnly in the development workflow,
#     not as a hosted CI job, and exits 1 on pre-existing failures, so it is not the gate
#     of record. Each guard resolves the repo root from its own location (cwd-independent)
#     and uses frontend/node_modules/yaml (the same dependency as step 3).
# ---------------------------------------------------------------------------
Write-Host "==> v0.2.4 OpenAPI contract guards (T19)"
$v024Node = Get-Command node -ErrorAction SilentlyContinue
if ($null -eq $v024Node) {
    Add-Failure "node is required for the v0.2.4 OpenAPI contract guards"
} else {
    $v024Guards = @(
        "v024-openapi-contract-check.cjs",
        "v024-openapi-deep-check.cjs",
        "v024-openapi-validate.cjs",
        "v024-openapi-oneof-test.cjs",
        "v024-openapi-mutants.cjs",
        "generate-capability-list.cjs",
        "generate-contract-docs.cjs",
        "workflow-c-numbers.cjs",
        "check-acceptance-paths.cjs"
    )
    foreach ($guard in $v024Guards) {
        $guardPath = Join-Path $repoRoot "scripts\$guard"
        if (-not (Test-Path -LiteralPath $guardPath)) {
            Add-Failure "missing tracked guard scripts/$guard"
            continue
        }
        $previousPreference = $ErrorActionPreference
        $ErrorActionPreference = "Continue"
        try {
            $guardOutput = & $v024Node.Source $guardPath 2>&1
            $guardCode = $LASTEXITCODE
        } finally {
            $ErrorActionPreference = $previousPreference
        }
        if ($guardCode -ne 0) {
            $detail = ($guardOutput | ForEach-Object { [string]$_ }) -join " | "
            Add-Failure "scripts/$guard exited $guardCode :: $detail"
        } else {
            Write-Host "OK scripts/$guard"
        }
    }
}

# ---------------------------------------------------------------------------
# 4. Compose services, volumes, health checks and Dockerfiles.
# ---------------------------------------------------------------------------
Write-Host "==> Compose contract"
$composeScript = @'
const fs = require('fs');
const path = require('path');
const YAML = require('yaml');
const repoRoot = process.argv[2];
const composePath = path.join(repoRoot, 'deploy', 'docker-compose.yml');
const doc = YAML.parse(fs.readFileSync(composePath, 'utf8'));
const problems = [];
const check = (condition, message) => { if (!condition) problems.push(message); };
const services = (doc && doc.services) || {};
for (const name of ['api', 'api2', 'web', 'postgres', 'executor', 'renderer']) {
  check(Boolean(services[name]), 'services.' + name + ' is missing');
}
for (const name of ['api', 'api2', 'web', 'postgres', 'executor', 'renderer']) {
  const healthcheck = (services[name] || {}).healthcheck || {};
  check(Boolean(healthcheck.test), name + ' healthcheck.test is missing');
  check(Boolean(healthcheck.interval), name + ' healthcheck.interval is missing');
  check(Boolean(healthcheck.retries), name + ' healthcheck.retries is missing');
}
const volumes = (doc && doc.volumes) || {};
for (const name of ['pgdata', 'data']) {
  check(Object.prototype.hasOwnProperty.call(volumes, name), 'top-level volume ' + name + ' is missing');
}
const mounts = (service) => ((service || {}).volumes || []).map((entry) => String(entry).split(':')).map((parts) => ({
  source: parts[0],
  target: parts[1] || '',
}));
for (const replica of ['api', 'api2']) {
  const replicaMounts = mounts(services[replica]);
  check(replicaMounts.some((mount) => mount.source === 'data' && mount.target === '/data'),
    replica + ' must mount the shared data volume at /data');
}
const dependsHealthy = (service, target) => {
  const dependsOn = (service || {}).depends_on;
  return Boolean(dependsOn && dependsOn[target] && dependsOn[target].condition === 'service_healthy');
};
check(dependsHealthy(services.api, 'postgres'), 'api must depend_on postgres (service_healthy)');
check(dependsHealthy(services.web, 'api'), 'web must depend_on api (service_healthy)');
check(!(services.executor || {}).ports, 'executor must not publish host ports');
check(!(services.renderer || {}).ports, 'renderer must not publish host ports');
check(((services.executor || {}).expose || []).map(String).indexOf('13336') >= 0,
  'executor must expose 13336');
check(((services.renderer || {}).expose || []).map(String).indexOf('13337') >= 0,
  'renderer must expose 13337');
for (const dockerfile of ['deploy/Dockerfile.api', 'deploy/Dockerfile.web', 'deploy/Dockerfile.renderer', 'executor/Dockerfile']) {
  check(fs.existsSync(path.join(repoRoot, dockerfile)), 'missing ' + dockerfile);
}
if (problems.length > 0) {
  throw new Error(problems.join('; '));
}
console.log('Compose contract OK: 6 services, ' + Object.keys(volumes).length +
  ' volumes, shared data volume on both API replicas, executor/renderer internal-only, Dockerfiles present');
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
    "docs/v0.2.4/20-v0.2.4-release-notes.md",
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
# Deployment-face values live in the deployment templates, not the root
# .env.example (which documents only the four API runtime variables + seed):
#   * EZTEST_EXECUTOR_TOKEN is read by the executor process and carried by the
#     packaged runtime template scripts/package/.env.example;
#   * POSTGRES_PASSWORD is interpolated by the Compose file.
$packageEnvTemplate = Read-Text -Relative "scripts/package/.env.example"
Assert-Contains -Relative "scripts/package/.env.example" -Text $packageEnvTemplate -Needle "EZTEST_EXECUTOR_TOKEN" -Description "the executor token entry"
$composeTemplate = Read-Text -Relative "deploy/docker-compose.yml"
Assert-Contains -Relative "deploy/docker-compose.yml" -Text $composeTemplate -Needle "POSTGRES_PASSWORD" -Description "the PostgreSQL password interpolation"
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
    "docs/v0.2.4/20-v0.2.4-release-notes.md"
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
Write-Host "check-docs PASSED: 4 documentation categories + release notes, API runtime env template, OpenAPI paths, Compose contract, no secrets, local Markdown links."
exit 0
