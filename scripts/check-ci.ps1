#requires -Version 5.1
<#
.SYNOPSIS
    Statically checks that the CI workflow keeps the EzTest baseline test gate.

.DESCRIPTION
    Reads .github/workflows/ci.yml and asserts that the workflow still wires up
    the gates the repository expects:

      - push / pull_request triggers;
      - an Ubuntu Go + Node job with a PostgreSQL 17 service on port 5433;
      - Go unit tests (go test ./...) and the integration suite
        (go test -tags=integration ./...) using EZTEST_TEST_DATABASE_URL;
      - Node 22 + pnpm, frontend lint/typecheck/test and the executor test;
      - the PowerShell static acceptance gate
        (scripts/acceptance.ps1 -StaticOnly).

    The local gate is scripts/test.ps1; the workflow is expected to stay in
    parity with its commands. Real-LLM and Playwright E2E gates are deliberately
    out of scope for CI, so their scripts must not appear in the workflow.

    This is a structural (text) check: it does not run the workflow. It exits 1
    with one FAIL line per missing/forbidden fragment and exits 0 on success.

.NOTES
    Compatible with Windows PowerShell 5.1 and PowerShell 7+.
#>
[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$workflowPath = Join-Path $repoRoot ".github\workflows\ci.yml"
$relativeWorkflowPath = ".github/workflows/ci.yml"

if (-not (Test-Path -LiteralPath $workflowPath)) {
    Write-Host ("FAIL: {0} does not exist; create the baseline workflow first." -f $relativeWorkflowPath) -ForegroundColor Red
    exit 1
}

$workflow = Get-Content -LiteralPath $workflowPath -Raw
if ([string]::IsNullOrWhiteSpace($workflow)) {
    Write-Host ("FAIL: {0} is empty." -f $relativeWorkflowPath) -ForegroundColor Red
    exit 1
}

$script:failures = @()

function Test-WorkflowFragment {
    param(
        [string]$Needle,
        [string]$Description
    )
    if ($script:workflow.IndexOf($Needle, [System.StringComparison]::OrdinalIgnoreCase) -lt 0) {
        $script:failures += ("{0} (missing fragment: {1})" -f $Description, $Needle)
        Write-Host ("FAIL: {0}" -f $Description) -ForegroundColor Red
        return
    }
    Write-Host ("PASS: {0}" -f $Description) -ForegroundColor Green
}

function Test-WorkflowRegex {
    param(
        [string]$Pattern,
        [string]$Description
    )
    if (-not [regex]::IsMatch($script:workflow, $Pattern)) {
        $script:failures += ("{0} (no match for: {1})" -f $Description, $Pattern)
        Write-Host ("FAIL: {0}" -f $Description) -ForegroundColor Red
        return
    }
    Write-Host ("PASS: {0}" -f $Description) -ForegroundColor Green
}

function Test-WorkflowExcludes {
    param(
        [string]$Needle,
        [string]$Description
    )
    if ($script:workflow.IndexOf($Needle, [System.StringComparison]::OrdinalIgnoreCase) -ge 0) {
        $script:failures += ("{0} (forbidden fragment: {1})" -f $Description, $Needle)
        Write-Host ("FAIL: {0}" -f $Description) -ForegroundColor Red
        return
    }
    Write-Host ("PASS: {0}" -f $Description) -ForegroundColor Green
}

# Trigger and shared job structure.
Test-WorkflowFragment -Needle 'on: [push, pull_request]' -Description 'workflow runs on push and pull_request'
Test-WorkflowFragment -Needle 'runs-on: ubuntu-latest' -Description 'test job runs on Ubuntu'
Test-WorkflowFragment -Needle 'postgres:17' -Description 'PostgreSQL 17 service image'
Test-WorkflowFragment -Needle '5433:5432' -Description 'PostgreSQL service is published on port 5433'

# Toolchain setup.
Test-WorkflowFragment -Needle 'actions/setup-go' -Description 'Go toolchain is set up'
Test-WorkflowFragment -Needle 'go-version-file: backend/go.mod' -Description 'Go version follows backend/go.mod (Go 1.24)'
Test-WorkflowFragment -Needle 'node-version: 22' -Description 'Node 22 is set up'
Test-WorkflowFragment -Needle 'pnpm/action-setup' -Description 'pnpm is installed'
Test-WorkflowFragment -Needle 'pnpm install --frozen-lockfile' -Description 'dependencies install from the frozen lockfile'

# Go gates.
Test-WorkflowFragment -Needle 'go test -count=1 ./...' -Description 'Go unit tests run'
Test-WorkflowFragment -Needle 'go test -tags=integration -count=1 ./...' -Description 'Go integration tests run'
Test-WorkflowFragment -Needle 'EZTEST_TEST_DATABASE_URL' -Description 'integration tests receive EZTEST_TEST_DATABASE_URL'
Test-WorkflowFragment -Needle '5433/eztest_test' -Description 'integration tests target the dedicated eztest_test database'

# Frontend gates (same commands as scripts/test.ps1).
Test-WorkflowFragment -Needle 'working-directory: frontend' -Description 'frontend steps run in frontend/'
Test-WorkflowFragment -Needle 'run: pnpm lint' -Description 'frontend lint runs'
Test-WorkflowFragment -Needle 'run: pnpm typecheck' -Description 'frontend typecheck runs'
Test-WorkflowFragment -Needle 'run: pnpm test' -Description 'frontend unit tests run'

# Executor gate: `cd executor && pnpm test` expressed as working-directory.
Test-WorkflowRegex -Pattern 'working-directory:[ \t]*executor[ \t]*\r?\n[ \t]*run:[ \t]*pnpm test' -Description 'executor test runs in executor/'

# Static acceptance gate, invoked through pwsh (PowerShell 7) on Windows.
Test-WorkflowFragment -Needle 'runs-on: windows-latest' -Description 'static acceptance job runs on Windows'
Test-WorkflowFragment -Needle 'scripts/acceptance.ps1 -StaticOnly' -Description 'static acceptance gate is wired up'
Test-WorkflowFragment -Needle 'pwsh -NoProfile -File scripts/acceptance.ps1 -StaticOnly' -Description 'static acceptance gate runs through pwsh -File'

# Local gate parity: the workflow must not silently drift from scripts/test.ps1.
Test-WorkflowFragment -Needle 'scripts/test.ps1' -Description 'workflow documents parity with the local scripts/test.ps1 gate'

# E2E and real-LLM acceptance stay out of CI (no browsers, no paid calls).
Test-WorkflowExcludes -Needle 'scripts/e2e.ps1' -Description 'Playwright E2E gate is not run in CI'
Test-WorkflowExcludes -Needle 'playwright test' -Description 'Playwright test runner is not invoked in CI'
Test-WorkflowExcludes -Needle 'acceptance-llm' -Description 'real-LLM acceptance scripts are not run in CI'
Test-WorkflowExcludes -Needle 'llm_key' -Description 'no real LLM key is configured in CI'

if ($script:failures.Count -gt 0) {
    Write-Host ("FAIL: {0} structural CI check(s) failed for {1}." -f $script:failures.Count, $relativeWorkflowPath) -ForegroundColor Red
    exit 1
}

Write-Host ("PASS: {0} keeps the baseline CI gates." -f $relativeWorkflowPath) -ForegroundColor Green
exit 0
