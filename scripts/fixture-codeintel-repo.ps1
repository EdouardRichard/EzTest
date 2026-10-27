#requires -Version 5.1
<#
.SYNOPSIS
    Recreates the deterministic P4 code-intelligence E2E fixture repository.

.DESCRIPTION
    Builds `.tmp/p4-e2e-repo` (gitignored) from scratch with the two fixed
    commits of design spec §10.2:

      commit 1 (base): src/api/refund.ts (client_call POST /api/refund),
        src/router.ts (route /orders), src/pages/OrdersPage.tsx (component
        OrdersPage), server/refund.go (api POST /api/refund),
        src/utils/format.ts, src/legacy/old.ts, assets/logo.png (binary).
      commit 2 (head): the refund call and the Go API both gain a `reason`
        parameter, OrdersPage gains the call-site change, `/orders/refund` is
        added, src/utils/format.ts changes (unmapped), src/legacy/old.ts is
        deleted and the binary logo is updated (unsupported content).

    The author, committer, timestamps, branch and messages are fixed, so the
    two commit SHAs are reproducible. The script always deletes and rebuilds the
    repository (idempotent); E2E callers may therefore run it before every run.

    It prints three machine-readable lines consumed by `scripts/e2e.ps1`:
      P4_FIXTURE_REPO=<absolute path with forward slashes>
      P4_FIXTURE_BASE_COMMIT=<40 hex sha>
      P4_FIXTURE_HEAD_COMMIT=<40 hex sha>

.NOTES
    Compatible with Windows PowerShell 5.1 and PowerShell 7+. Requires git on
    PATH. The fixture working tree is written with LF line endings, independent
    of the checkout's core.autocrlf setting.
#>
[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$tmpRoot = Join-Path $repoRoot ".tmp"
$fixtureDir = Join-Path $tmpRoot "p4-e2e-repo"

# Guard the recursive delete: only the known fixture directory may be removed.
if ((Split-Path -Leaf $fixtureDir) -ne "p4-e2e-repo") {
    throw "refusing to rebuild an unexpected fixture directory: $fixtureDir"
}
if (-not $fixtureDir.StartsWith($tmpRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "refusing to rebuild a fixture directory outside .tmp: $fixtureDir"
}
if (-not (Get-Command git -ErrorAction SilentlyContinue)) {
    throw "git was not found on PATH; it is required by the P4 fixture"
}

$branch = "main"
$authorName = "EzTest P4 Fixture"
$authorEmail = "p4-fixture@eztest.invalid"
$commit1Date = "2026-10-01T09:00:00+08:00"
$commit2Date = "2026-10-02T09:00:00+08:00"
$commit1Message = "p4 fixture commit 1: initial refund app"
$commit2Message = "p4 fixture commit 2: add refund reason parameter"

function Invoke-Git {
    param([string[]]$Arguments)
    & git -C $fixtureDir @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "git $($Arguments -join ' ') failed with exit code $LASTEXITCODE"
    }
}

function Get-GitText {
    param([string[]]$Arguments)
    $output = & git -C $fixtureDir @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "git $($Arguments -join ' ') failed with exit code $LASTEXITCODE"
    }
    return (($output | Out-String).Trim())
}

function Ensure-FixtureDirectory {
    param([string]$Path)
    if (-not (Test-Path -LiteralPath $Path)) {
        New-Item -ItemType Directory -Force -Path $Path | Out-Null
    }
}

function Write-FixtureText {
    param(
        [string]$RelativePath,
        [string[]]$Lines
    )
    $fullPath = Join-Path $fixtureDir $RelativePath
    Ensure-FixtureDirectory -Path (Split-Path -Parent $fullPath)
    # Join explicitly with LF so the fixture is byte-identical on every host.
    $text = ($Lines -join "`n") + "`n"
    $encoding = New-Object System.Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllText($fullPath, $text, $encoding)
}

function Write-FixtureBytes {
    param(
        [string]$RelativePath,
        [byte[]]$Bytes
    )
    $fullPath = Join-Path $fixtureDir $RelativePath
    Ensure-FixtureDirectory -Path (Split-Path -Parent $fullPath)
    [System.IO.File]::WriteAllBytes($fullPath, $Bytes)
}

function New-FixtureCommit {
    param(
        [string]$Message,
        [string]$Timestamp
    )
    # Fixed author/committer date makes the commit SHA reproducible.
    $env:GIT_AUTHOR_DATE = $Timestamp
    $env:GIT_COMMITTER_DATE = $Timestamp
    Invoke-Git @("add", "--all")
    Invoke-Git @("commit", "--quiet", "--no-gpg-sign", "-m", $Message)
}

if (Test-Path -LiteralPath $fixtureDir) {
    Remove-Item -LiteralPath $fixtureDir -Recurse -Force
}
Ensure-FixtureDirectory -Path $fixtureDir

Invoke-Git @("init", "--quiet")
Invoke-Git @("symbolic-ref", "HEAD", "refs/heads/$branch")
Invoke-Git @("config", "user.name", $authorName)
Invoke-Git @("config", "user.email", $authorEmail)
Invoke-Git @("config", "core.autocrlf", "false")
Invoke-Git @("config", "commit.gpgsign", "false")

# --- commit 1: initial application -----------------------------------------

Write-FixtureText "src/api/refund.ts" @(
    '// Refund API client used by the orders flow.'
    'export async function requestRefund(orderId: string): Promise<void> {'
    '  const response = await fetch("/api/refund", {'
    '    method: "POST",'
    '    headers: { "Content-Type": "application/json" },'
    '    body: JSON.stringify({ orderId }),'
    '  })'
    '  if (!response.ok) {'
    '    throw new Error("refund request failed: " + response.status)'
    '  }'
    '}'
)

Write-FixtureText "src/router.ts" @(
    'import { createRouter } from "./framework"'
    ''
    'export const router = createRouter()'
    ''
    'router.get("/orders", async (context) => {'
    '  return context.render("OrdersPage")'
    '})'
)

Write-FixtureText "src/pages/OrdersPage.tsx" @(
    'import { requestRefund } from "../api/refund"'
    ''
    'export function OrdersPage({ orders }: { orders: Order[] }) {'
    '  const refund = async (orderId: string) => {'
    '    await requestRefund(orderId)'
    '  }'
    ''
    '  return <OrderList orders={orders} onRefund={refund} />'
    '}'
)

Write-FixtureText "server/refund.go" @(
    'package server'
    ''
    'import ('
    '	"encoding/json"'
    '	"net/http"'
    ')'
    ''
    'type refundRequest struct {'
    '	OrderID string `json:"orderId"`'
    '}'
    ''
    'func registerRefundRoutes(mux *http.ServeMux) {'
    '	mux.HandleFunc("POST /api/refund", handleRefund)'
    '}'
    ''
    'func handleRefund(w http.ResponseWriter, r *http.Request) {'
    '	var request refundRequest'
    '	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {'
    '		http.Error(w, "invalid request", http.StatusBadRequest)'
    '		return'
    '	}'
    '	w.WriteHeader(http.StatusNoContent)'
    '}'
)

Write-FixtureText "src/utils/format.ts" @(
    'export function formatAmount(value: number): string {'
    '  return value.toFixed(2)'
    '}'
)

Write-FixtureText "src/legacy/old.ts" @(
    'export const legacyRefundNote = "superseded by src/api/refund.ts"'
)

# 1x1 transparent PNG; a real binary blob so git reports it as binary.
$logoBytes = [System.Convert]::FromBase64String(
    'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=='
)
Write-FixtureBytes "assets/logo.png" $logoBytes

New-FixtureCommit -Message $commit1Message -Timestamp $commit1Date
$baseCommit = Get-GitText @("rev-parse", "HEAD")

# --- commit 2: cross-stack `reason` parameter -------------------------------

Write-FixtureText "src/api/refund.ts" @(
    '// Refund API client used by the orders flow.'
    'export type RefundRequest = { orderId: string; reason: string }'
    ''
    'export async function requestRefund(input: RefundRequest): Promise<void> {'
    '  const response = await fetch("/api/refund", {'
    '    method: "POST",'
    '    headers: { "Content-Type": "application/json" },'
    '    body: JSON.stringify(input),'
    '  })'
    '  if (!response.ok) {'
    '    throw new Error("refund request failed: " + response.status)'
    '  }'
    '}'
)

Write-FixtureText "src/router.ts" @(
    'import { createRouter } from "./framework"'
    ''
    'export const router = createRouter()'
    ''
    'router.get("/orders", async (context) => {'
    '  return context.render("OrdersPage")'
    '})'
    ''
    'router.get("/orders/refund", async (context) => {'
    '  return context.render("OrdersPage", { focus: "refund" })'
    '})'
)

Write-FixtureText "src/pages/OrdersPage.tsx" @(
    'import { requestRefund } from "../api/refund"'
    ''
    'export function OrdersPage({ orders }: { orders: Order[] }) {'
    '  const refund = async (orderId: string) => {'
    '    await requestRefund({ orderId, reason: "customer request" })'
    '  }'
    ''
    '  return <OrderList orders={orders} onRefund={refund} />'
    '}'
)

Write-FixtureText "server/refund.go" @(
    'package server'
    ''
    'import ('
    '	"encoding/json"'
    '	"net/http"'
    ')'
    ''
    'type refundRequest struct {'
    '	OrderID string `json:"orderId"`'
    '	Reason  string `json:"reason"`'
    '}'
    ''
    'func registerRefundRoutes(mux *http.ServeMux) {'
    '	mux.HandleFunc("POST /api/refund", handleRefund)'
    '}'
    ''
    'func handleRefund(w http.ResponseWriter, r *http.Request) {'
    '	var request refundRequest'
    '	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {'
    '		http.Error(w, "invalid request", http.StatusBadRequest)'
    '		return'
    '	}'
    '	w.WriteHeader(http.StatusNoContent)'
    '}'
)

Write-FixtureText "src/utils/format.ts" @(
    'export function formatAmount(value: number, currency: string): string {'
    '  const scale = currency === "JPY" ? 0 : 2'
    '  return `${currency} ${value.toFixed(scale)}`'
    '}'
)

# The binary update is deterministic: the v1 bytes plus a fixed ASCII trailer.
$logoBytesV2 = [byte[]]($logoBytes + [System.Text.Encoding]::ASCII.GetBytes("P4 fixture logo v2"))
Write-FixtureBytes "assets/logo.png" $logoBytesV2

Invoke-Git @("rm", "--quiet", "src/legacy/old.ts")

New-FixtureCommit -Message $commit2Message -Timestamp $commit2Date
$headCommit = Get-GitText @("rev-parse", "HEAD")

Remove-Item Env:GIT_AUTHOR_DATE -ErrorAction SilentlyContinue
Remove-Item Env:GIT_COMMITTER_DATE -ErrorAction SilentlyContinue

# --- verification: deterministic shape -------------------------------------

$commitCount = Get-GitText @("rev-list", "--count", "HEAD")
if ($commitCount -ne "2") {
    throw "the fixture must contain exactly two commits, found $commitCount"
}
if ($baseCommit -notmatch '^[0-9a-f]{40}$' -or $headCommit -notmatch '^[0-9a-f]{40}$') {
    throw "unexpected commit SHAs: base=$baseCommit head=$headCommit"
}
$status = Get-GitText @("status", "--porcelain")
if ($status -ne "") {
    throw "the fixture worktree is not clean: $status"
}
$currentBranch = Get-GitText @("rev-parse", "--abbrev-ref", "HEAD")
if ($currentBranch -ne $branch) {
    throw "the fixture must be on branch $branch, found $currentBranch"
}

Write-Output ("P4_FIXTURE_REPO=" + ($fixtureDir -replace '\\', '/'))
Write-Output ("P4_FIXTURE_BASE_COMMIT=" + $baseCommit)
Write-Output ("P4_FIXTURE_HEAD_COMMIT=" + $headCommit)
