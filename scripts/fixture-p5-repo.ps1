#requires -Version 5.1
<#
.SYNOPSIS
    Recreates the deterministic P5 code-intelligence fixture repository.

.DESCRIPTION
    Builds `.tmp/p5-repo` (gitignored) from scratch with the two fixed commits
    required by design spec §12.2:

      commit A (base): src/api/refund.ts (client_call POST /api/refund),
        src/router.ts (route GET /orders), src/pages/OrdersPage.tsx (component
        OrdersPage), server/refund.go (api POST /api/refund),
        src/utils/format.ts, src/legacy/old.ts, assets/logo.png (binary).
      commit B (head): the refund client and the Go API both gain a `reason`
        parameter, OrdersPage gains the call-site change, the `/orders/refund`
        route is added, src/utils/format.ts changes (unmapped), src/legacy/old.ts
        is deleted and the binary logo is updated.

    The author, committer, timestamps, branch and messages are fixed, so the two
    commit SHAs are reproducible on every run. The environment variables
    GIT_AUTHOR_NAME/EMAIL, GIT_COMMITTER_NAME/EMAIL and the author/committer
    dates are pinned to fixture values and restored in `finally`; ambient or CI
    identities can therefore never leak into the fixture history. The script
    always deletes and rebuilds the repository (idempotent); acceptance callers
    may run it before every run.

    It prints three machine-readable lines consumed by `scripts/p5-acceptance.ps1`
    and `scripts/p5-e2e.ps1`:
      P5_FIXTURE_REPO=<absolute path with forward slashes>
      P5_FIXTURE_BASE_COMMIT=<40 hex sha>
      P5_FIXTURE_HEAD_COMMIT=<40 hex sha>

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
$fixtureDir = Join-Path $tmpRoot "p5-repo"

# Guard the recursive delete: only the known fixture directory may be removed.
if ((Split-Path -Leaf $fixtureDir) -ne "p5-repo") {
    throw "refusing to rebuild an unexpected fixture directory: $fixtureDir"
}
$tmpPrefix = $tmpRoot.TrimEnd([char[]]@('\', '/')) + [System.IO.Path]::DirectorySeparatorChar
if (-not $fixtureDir.StartsWith($tmpPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "refusing to rebuild a fixture directory outside .tmp: $fixtureDir"
}
if (-not (Get-Command git -ErrorAction SilentlyContinue)) {
    throw "git was not found on PATH; it is required by the P5 fixture"
}

$branch = "main"
$authorName = "EzTest P5 Fixture"
$authorEmail = "p5-fixture@eztest.invalid"
$commit1Date = "2026-10-20T09:00:00+08:00"
$commit2Date = "2026-10-21T09:00:00+08:00"
$commit1Message = "p5 fixture commit A: initial orders application"
$commit2Message = "p5 fixture commit B: add refund reason parameter"

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

function Get-GitLines {
    param([string[]]$Arguments)
    $output = & git -C $fixtureDir @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "git $($Arguments -join ' ') failed with exit code $LASTEXITCODE"
    }
    return @($output | ForEach-Object { "$_" })
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

# Pin every Git identity variable, not just the repository-local user.name and
# user.email: GIT_AUTHOR_NAME/EMAIL and GIT_COMMITTER_NAME/EMAIL override the
# local config, so an ambient/CI identity would otherwise change the commit
# SHAs. Capture and restore the previous values (including the date variables
# New-FixtureCommit sets) so the caller's environment is left untouched.
$identityEnv = [ordered]@{
    GIT_AUTHOR_NAME     = $authorName
    GIT_AUTHOR_EMAIL    = $authorEmail
    GIT_COMMITTER_NAME  = $authorName
    GIT_COMMITTER_EMAIL = $authorEmail
}
$identityEnvKeys = @($identityEnv.Keys) + @("GIT_AUTHOR_DATE", "GIT_COMMITTER_DATE")
$previousEnvironment = @{}
foreach ($key in $identityEnvKeys) {
    $previousEnvironment[$key] = [System.Environment]::GetEnvironmentVariable($key, [System.EnvironmentVariableTarget]::Process)
}
foreach ($key in @($identityEnv.Keys)) {
    Set-Item -Path ("Env:" + $key) -Value $identityEnv[$key]
}

try {
    if (Test-Path -LiteralPath $fixtureDir) {
        $existing = Get-Item -LiteralPath $fixtureDir -Force
        if (-not $existing.PSIsContainer) {
            throw "refusing to rebuild a non-directory fixture path: $fixtureDir"
        }
        if (($existing.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "refusing to recursively delete a reparse-point fixture path: $fixtureDir"
        }
        Remove-Item -LiteralPath $fixtureDir -Recurse -Force
    }
    Ensure-FixtureDirectory -Path $tmpRoot
    Ensure-FixtureDirectory -Path $fixtureDir

    Invoke-Git @("init", "--quiet")
    Invoke-Git @("symbolic-ref", "HEAD", "refs/heads/$branch")
    Invoke-Git @("config", "user.name", $authorName)
    Invoke-Git @("config", "user.email", $authorEmail)
    Invoke-Git @("config", "core.autocrlf", "false")
    Invoke-Git @("config", "commit.gpgsign", "false")

    # --- commit A: initial application -----------------------------------------

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

    # --- commit B: cross-stack `reason` parameter -------------------------------

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

    # New in commit B: a dedicated refund entry page (A status in the diff).
    Write-FixtureText "src/pages/RefundPage.tsx" @(
        'import { requestRefund } from "../api/refund"'
        ''
        'export function RefundPage({ orderId }: { orderId: string }) {'
        '  const submit = async () => {'
        '    await requestRefund({ orderId, reason: "customer request" })'
        '  }'
        ''
        '  return <RefundButton onClick={submit} />'
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
    $logoBytesV2 = [byte[]]($logoBytes + [System.Text.Encoding]::ASCII.GetBytes("P5 fixture logo v2"))
    Write-FixtureBytes "assets/logo.png" $logoBytesV2

    Invoke-Git @("rm", "--quiet", "src/legacy/old.ts")

    New-FixtureCommit -Message $commit2Message -Timestamp $commit2Date
    $headCommit = Get-GitText @("rev-parse", "HEAD")

    # --- verification: deterministic, expected shape ---------------------------

    $commitCount = Get-GitText @("rev-list", "--count", "HEAD")
    if ($commitCount -ne "2") {
        throw "the fixture must contain exactly two commits, found $commitCount"
    }
    if ($baseCommit -notmatch '^[0-9a-f]{40}$' -or $headCommit -notmatch '^[0-9a-f]{40}$') {
        throw "unexpected commit SHAs: base=$baseCommit head=$headCommit"
    }
    if ($baseCommit -eq $headCommit) {
        throw "the fixture base and head commits must differ"
    }
    $status = Get-GitText @("status", "--porcelain")
    if ($status -ne "") {
        throw "the fixture worktree is not clean: $status"
    }
    $currentBranch = Get-GitText @("rev-parse", "--abbrev-ref", "HEAD")
    if ($currentBranch -ne $branch) {
        throw "the fixture must be on branch $branch, found $currentBranch"
    }

    # The fixed identity/dates/messages must show up exactly as configured; this
    # catches an ambient Git identity or config leaking into the history.
    $expectedLog = @(
        "$authorName <$authorEmail>|$authorName <$authorEmail>|$commit2Date|$commit2Date|$commit2Message",
        "$authorName <$authorEmail>|$authorName <$authorEmail>|$commit1Date|$commit1Date|$commit1Message"
    )
    $logLines = Get-GitLines @("log", "-2", "--format=%an <%ae>|%cn <%ce>|%aI|%cI|%s")
    if (($logLines -join "`n") -ne ($expectedLog -join "`n")) {
        throw "unexpected fixture history: $($logLines -join '; ')"
    }

    $expectedBaseFiles = @(
        "assets/logo.png",
        "server/refund.go",
        "src/api/refund.ts",
        "src/legacy/old.ts",
        "src/pages/OrdersPage.tsx",
        "src/router.ts",
        "src/utils/format.ts"
    )
    $expectedHeadFiles = @(
        "assets/logo.png",
        "server/refund.go",
        "src/api/refund.ts",
        "src/pages/OrdersPage.tsx",
        "src/pages/RefundPage.tsx",
        "src/router.ts",
        "src/utils/format.ts"
    )
    $baseFiles = Get-GitLines @("ls-tree", "-r", "--name-only", $baseCommit)
    $headFiles = Get-GitLines @("ls-tree", "-r", "--name-only", $headCommit)
    if (($baseFiles -join "`n") -ne ($expectedBaseFiles -join "`n")) {
        throw "unexpected commit A tree: $($baseFiles -join ', ')"
    }
    if (($headFiles -join "`n") -ne ($expectedHeadFiles -join "`n")) {
        throw "unexpected commit B tree: $($headFiles -join ', ')"
    }

    $expectedStatus = @(
        "M`tassets/logo.png",
        "M`tserver/refund.go",
        "M`tsrc/api/refund.ts",
        "D`tsrc/legacy/old.ts",
        "M`tsrc/pages/OrdersPage.tsx",
        "A`tsrc/pages/RefundPage.tsx",
        "M`tsrc/router.ts",
        "M`tsrc/utils/format.ts"
    )
    $diffStatus = Get-GitLines @("diff", "--name-status", $baseCommit, $headCommit)
    if (($diffStatus -join "`n") -ne ($expectedStatus -join "`n")) {
        throw "unexpected A..B diff status: $($diffStatus -join '; ')"
    }
    $binaryLine = Get-GitLines @("diff", "--numstat", $baseCommit, $headCommit) | Where-Object { $_ -match '^-\s+-\s+assets/logo\.png$' }
    if ($null -eq $binaryLine) {
        throw "assets/logo.png must be reported as a binary change between A and B"
    }

    Write-Output ("P5_FIXTURE_REPO=" + ($fixtureDir -replace '\\', '/'))
    Write-Output ("P5_FIXTURE_BASE_COMMIT=" + $baseCommit)
    Write-Output ("P5_FIXTURE_HEAD_COMMIT=" + $headCommit)
} finally {
    # Restore every identity/date variable even when a git command fails, so the
    # caller's environment (and later fixture runs) stay deterministic.
    foreach ($key in $identityEnvKeys) {
        $previous = $previousEnvironment[$key]
        if ($null -eq $previous) {
            Remove-Item -Path ("Env:" + $key) -ErrorAction SilentlyContinue
        } else {
            Set-Item -Path ("Env:" + $key) -Value $previous
        }
    }
}
