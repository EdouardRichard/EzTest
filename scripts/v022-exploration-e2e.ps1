#requires -Version 5.1
<#
.SYNOPSIS
    EzTest v0.22 W2-13 WP-7 real-SUT execution-time exploration acceptance.

.DESCRIPTION
    Proves the P -> U -> A execution-time discovery loop against the real SUT
    (http://localhost:5173, backend http://localhost:8080) without changing SUT
    code and without writing business tables directly.

    The script boots an isolated composition root on a W2-only namespace
    (API 13450, executor 13353, web 6874, vision-caption proxy 13999) against
    the existing database eztest_v022_w2. It creates a dedicated environment
    with only the home page P approved, binds a version to it, creates one
    requirement and lets the real design Host/Author produce C1 (open P, click
    the first article title, reload the article detail page U with a full
    document load). Wave 1 executes C1 and the terminal capture records the
    unregistered page U = /posts/{id} and unregistered API A =
    GET /api/v1/posts/{id}; the delta records proposed resources, facts and one
    resource.delta event.

    The admin then approves U/A through the existing resource confirm routes.
    W2-11 auto-confirms one additive case revision per resource
    (trigger=discovery_delta, auto_confirmed_by=system) while the current wave
    case_run IDs stay unchanged. Wave 2 (explicit selection of the new cases)
    shows the new resources in the frozen scope snapshot and executes the new
    cases. Coverage moves from uncovered to covered. The script then rolls back
    one auto-confirmed revision through the W2-12 UI (in-session notice) and
    verifies the W2-C11 replacement semantics (case kept, trigger=rollback,
    rollback_of set, repeated rollback idempotent).

    The SUT and PostgreSQL are never stopped. The started API/executor/web/
    proxy are stopped at the end and execution.executor_url is restored to its
    original value.

    Evidence (untracked):
        docs/version-V0.22/acceptance/evidence/w2/run/exploration-verdict.json
        docs/version-V0.22/acceptance/evidence/w2/run/exploration/*.png
        docs/version-V0.22/acceptance/evidence/w2/run/exploration/*.json

    Exit codes: 0 PASS, 1 acceptance failure, 2 EXTERNAL-BLOCKED.
#>
param(
    [switch]$SkipBuild,
    [switch]$VerdictOnly,
    [switch]$KeepStack,
    [int]$DesignTimeoutSeconds = 1200,
    [int]$WaveTimeoutSeconds = 1200,
    [int]$Wave2TimeoutSeconds = 1200,
    [int]$AutoConfirmTimeoutSeconds = 180,
    [int]$UiTimeoutSeconds = 240
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

$label = 'W2-13 execution-time exploration e2e'
$work = Join-Path $repoRoot '.tmp\w2-13'
$evidenceRoot = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence\w2'
$runEvidence = Join-Path $evidenceRoot 'run'
$shotDir = Join-Path $runEvidence 'exploration'
$dbName = 'eztest_v022_w2'
$apiPort = 13450
$webPort = 6874
$execPort = 13353
$proxyPort = 13999
$apiBase = "http://127.0.0.1:$apiPort"
$webUrl = "http://127.0.0.1:$webPort"
$executorUrl = "http://127.0.0.1:$execPort"
$proxyBase = "http://127.0.0.1:$proxyPort"
$sutWeb = 'http://localhost:5173'
$sutApi = 'http://localhost:8080/api/v1/posts?limit=1'
$verdictPath = Join-Path $runEvidence 'exploration-verdict.json'
$statePath = Join-Path $runEvidence 'exploration-state.json'
$flowLog = Join-Path $runEvidence 'exploration-flow.log'
$originalExecutorUrlPath = Join-Path $work 'original-executor-url.txt'
$flowExit = 0
$exitCode = 0
$externalBlocked = $false
$blockedReason = ''
$stackStarted = $false
$token = ''

New-Item -ItemType Directory -Force -Path $work, $runEvidence, $shotDir | Out-Null

# UTF-8 without BOM: evidence carries CJK case titles and the Windows
# PowerShell 5.1 text cmdlets would rewrite them in the active ANSI code page.
function Write-Utf8([string]$path, [string]$text) {
    [System.IO.File]::WriteAllText($path, $text, (New-Object System.Text.UTF8Encoding($false)))
}

function Get-EnvValue([string]$key) {
    $line = Select-String -Path (Join-Path $repoRoot '.env') -Pattern ("^" + [regex]::Escape($key) + "=(.*)$")
    if (-not $line) { throw "missing $key in .env" }
    return $line.Matches[0].Groups[1].Value
}

# Windows PowerShell 5.1 emits a JSON array as ONE pipeline object; @(<pipeline>)
# would wrap it into a single element. Assign first and emit each element so
# arrays parsed from psql JSON are flat.
function Parse-JsonArray([string]$raw) {
    if (-not $raw) { return }
    $parsed = $raw | ConvertFrom-Json
    if ($null -eq $parsed) { return }
    foreach ($item in @($parsed)) { $item }
}

function Exit-ExternalBlocked([string]$reason) {
    $script:externalBlocked = $true
    $script:blockedReason = $reason
    $stamp = Get-Date -Format 'yyyyMMdd-HHmmssfff'
    $blocked = [ordered]@{
        generatedAt     = (Get-Date -Format o)
        externalBlocked = $true
        reason          = $reason
    }
    Write-Utf8 (Join-Path $runEvidence "exploration-blocked-$stamp.json") ($blocked | ConvertTo-Json -Depth 4)
    throw "EXTERNAL-BLOCKED: $reason"
}

function Get-HttpCode([string]$url) {
    $curlExe = (Get-Command curl.exe -ErrorAction SilentlyContinue).Source
    if (-not $curlExe) { return 'no-curl' }
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $code = (& $curlExe -s -o NUL -w "%{http_code}" --max-time 10 $url 2>&1 | Out-String).Trim()
        $curlExit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($curlExit -ne 0) { return "curl-exit-$curlExit" }
    return $code
}

function Test-Http200([string]$url) {
    return ((Get-HttpCode $url) -eq '200')
}

function Wait-Http200([string]$url, [int]$timeoutSeconds) {
    $deadline = (Get-Date).AddSeconds($timeoutSeconds)
    do {
        if (Test-Http200 $url) { return $true }
        Start-Sleep -Seconds 2
    } while ((Get-Date) -lt $deadline)
    return (Test-Http200 $url)
}

function Stop-E2EPorts {
    foreach ($port in @($apiPort, $execPort, $webPort, $proxyPort)) {
        $conns = Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue
        foreach ($conn in $conns) {
            Stop-Process -Id $conn.OwningProcess -Force -ErrorAction SilentlyContinue
        }
    }
}

# ---------------------------------------------------------------------------
# Verdict contract gate. This is the RED-phase acceptance test: it fails
# before the evidence run exists and is re-evaluated after every run.
# ---------------------------------------------------------------------------
function Assert-ExplorationVerdict {
    if (-not (Test-Path $verdictPath)) { throw 'missing exploration verdict' }
    $v = Get-Content $verdictPath -Raw -Encoding UTF8 | ConvertFrom-Json
    if ($null -eq $v) { throw 'unreadable exploration verdict' }
    if ($v.status -ne 'PASS') { throw "exploration verdict status=$($v.status)" }
    if ($v.replayed -ne $false) { throw "exploration verdict replayed=$($v.replayed), want false" }

    $before = @($v.currentWaveCaseRunIdsBefore | ForEach-Object { [string]$_ })
    $after = @($v.currentWaveCaseRunIdsAfter | ForEach-Object { [string]$_ })
    if (($before -join ',') -ne ($after -join ',')) { throw 'current wave case_run IDs changed' }
    if ($before.Count -lt 1) { throw 'current wave has no case_run IDs' }

    if (-not $v.nextWaveSnapshotContainsU) { throw 'next wave snapshot missing U' }
    if (-not $v.nextWaveSnapshotContainsA) { throw 'next wave snapshot missing A' }

    if ([int]$v.coverageBeforeUncovered -lt 1) { throw "coverageBeforeUncovered=$($v.coverageBeforeUncovered), want >= 1" }
    if ([int]$v.coverageAfterCovered -lt 1) { throw "coverageAfterCovered=$($v.coverageAfterCovered), want >= 1" }

    if ([int]$v.observationPages -lt 1) { throw 'no page observations' }
    if ([int]$v.observationApis -lt 1) { throw 'no API observations' }
    if (@($v.proposedPageIds | Where-Object { $_ }).Count -lt 1) { throw 'no proposed page ids' }
    if (@($v.proposedApiIds | Where-Object { $_ }).Count -lt 1) { throw 'no proposed API ids' }
    if ([int]$v.factPageCount -lt 1) { throw 'no page facts' }
    if ([int]$v.factApiCount -lt 1) { throw 'no API facts' }
    if ([int]$v.resourceDeltaEvents -lt 1) { throw 'no resource.delta events' }
    if ([int]$v.caseRevisionEvents -lt 1) { throw 'no case.revision.auto_confirmed events' }
    if (@($v.newCaseVersionIds | Where-Object { $_ }).Count -lt 1) { throw 'no auto-confirmed case revisions' }
    if ([string]::IsNullOrWhiteSpace([string]$v.rollbackRevisionId)) { throw 'no rollback replacement revision' }

    $shots = @($v.screenshots | Where-Object { $_ })
    if ($shots.Count -lt 3) { throw "screenshots=$($shots.Count), want at least 3" }
    foreach ($shot in $shots) {
        $shotPath = Join-Path $runEvidence ([string]$shot)
        if (-not (Test-Path $shotPath)) { throw "missing screenshot $shot" }
        if ((Get-Item $shotPath).Length -le 0) { throw "empty screenshot $shot" }
    }
}

try {
    if ($VerdictOnly) {
        Assert-ExplorationVerdict
        Write-Host '== W2-13 exploration verdict contract: PASS'
        $exitCode = 0
    } else {
        # -------------------------------------------------------------------
        # 0. Credentials, hard dependencies and real upstream preflight.
        # -------------------------------------------------------------------
        $dbUrl = Get-EnvValue 'EZTEST_DATABASE_URL'
        $dbPassword = [regex]::Match($dbUrl, 'postgres://[^:]+:([^@]+)@').Groups[1].Value
        $execToken = Get-EnvValue 'EZTEST_EXECUTOR_TOKEN'
        $adminPassword = Get-EnvValue 'EZTEST_ADMIN_PASSWORD'
        $llmKey = Get-EnvValue 'llm_api_key'
        $llmBase = (Get-EnvValue 'llm_base_url').TrimEnd('/')
        $llmModel = Get-EnvValue 'llm_model_name'
        $env:PGPASSWORD = $dbPassword
        $psql = Join-Path $repoRoot '.tools\pgsql\bin\psql.exe'
        $goExe = Join-Path $repoRoot '.tools\go\bin\go.exe'
        try { $nodeExe = (Get-Command node).Source } catch { Exit-ExternalBlocked 'node is not on PATH' }
        if (-not (Test-Path -LiteralPath $psql)) { Exit-ExternalBlocked "PostgreSQL client is missing: $psql" }
        if (-not (Test-Path -LiteralPath $goExe)) { Exit-ExternalBlocked "Go toolchain is missing: $goExe" }

        $previous = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        & $psql -h localhost -p 5433 -U eztest -d postgres -tAc 'select 1' 2>&1 | Out-Null
        $psqlOk = ($LASTEXITCODE -eq 0)
        $ErrorActionPreference = $previous
        if (-not $psqlOk) { Exit-ExternalBlocked 'PostgreSQL 5433 is unreachable (psql select 1 failed)' }

        $previous = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        $dbCheck = (& $psql -h localhost -p 5433 -U eztest -d postgres -tAc "select 1 from pg_database where datname='$dbName'" 2>&1 | Out-String).Trim()
        $dbCheckExit = $LASTEXITCODE
        $ErrorActionPreference = $previous
        if ($dbCheckExit -ne 0 -or $dbCheck -ne '1') { Exit-ExternalBlocked "database $dbName does not exist (W2 base data is required; this script never drops it)" }

        if (-not (Test-Path -LiteralPath 'D:\project\blog-frontend')) { Exit-ExternalBlocked 'SUT frontend checkout D:\project\blog-frontend is missing' }
        if (-not (Test-Path -LiteralPath 'D:\project\blog-backend')) { Exit-ExternalBlocked 'SUT backend checkout D:\project\blog-backend is missing' }
        $sutApiCode = Get-HttpCode $sutApi
        if ($sutApiCode -ne '200' -and -not (Wait-Http200 $sutApi 30)) {
            Exit-ExternalBlocked "SUT backend $sutApi is not reachable (last HTTP '$sutApiCode', HTTP 200 required)"
        }
        if (-not (Test-Http200 $sutWeb)) {
            Write-Host "== SUT frontend $sutWeb is down; starting 'npm run dev' in D:\project\blog-frontend"
            $null = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{
                CommandLine      = 'cmd.exe /c start "w2-13-sut-frontend" /min npm run dev'
                CurrentDirectory = 'D:\project\blog-frontend'
            }
            $frontendReady = $false
            for ($i = 0; $i -lt 180; $i++) {
                if (Test-Http200 $sutWeb) { $frontendReady = $true; break }
                Start-Sleep -Seconds 1
            }
            if (-not $frontendReady) { Exit-ExternalBlocked "SUT frontend $sutWeb did not become ready after 'npm run dev'" }
        }
        Write-Host "== preflight SUT: $sutWeb ok, $sutApi ok"

        [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
        try {
            $probe = @{ model = $llmModel; messages = @(@{ role = 'user'; content = 'ping' }); max_tokens = 1; stream = $false } | ConvertTo-Json -Depth 5
            $answer = Invoke-RestMethod -Uri ($llmBase + '/chat/completions') -Method Post -Headers @{ authorization = "Bearer $llmKey" } -ContentType 'application/json' -Body $probe -TimeoutSec 90
            if (-not $answer.choices) { Exit-ExternalBlocked "LLM probe returned no choices (base=$llmBase model=$llmModel)" }
            Write-Host "== preflight LLM upstream: $llmBase model=$llmModel ok"
        } catch {
            $status = ''
            try { $status = [int]$_.Exception.Response.StatusCode } catch { }
            Exit-ExternalBlocked "LLM $llmBase (model=$llmModel) unreachable or rejected the key (status=$status): $($_.Exception.Message)"
        }

        Write-Host "== ${label}: repo=$repoRoot"
        Write-Host "== evidence=$runEvidence"

        # -------------------------------------------------------------------
        # 1. Dedicated ports, storage and build (DB is reused, never dropped).
        # -------------------------------------------------------------------
        Stop-E2EPorts
        Start-Sleep -Milliseconds 600
        $storage = Join-Path $work 'storage'
        New-Item -ItemType Directory -Force -Path $storage | Out-Null
        $backendExe = Join-Path $work 'eztest.exe'
        if (-not $SkipBuild) {
            Push-Location (Join-Path $repoRoot 'backend')
            try {
                & $goExe build -o $backendExe ./cmd/eztest 2>&1 | Tee-Object -FilePath (Join-Path $runEvidence 'exploration-build.log')
                if ($LASTEXITCODE -ne 0) { throw 'go build ./cmd/eztest failed' }
            } finally { Pop-Location }
            Write-Host '== backend built'
        }
        if (-not (Test-Path -LiteralPath $backendExe)) { throw "backend binary missing: $backendExe (use the full run, not -SkipBuild)" }

        # -------------------------------------------------------------------
        # 2. Service wrappers (isolated W2-13 namespace).
        # -------------------------------------------------------------------
        $runApi = Join-Path $work 'run-api.cmd'
        @"
@echo off
cd /d "$repoRoot\backend"
set EZTEST_DATABASE_URL=postgres://eztest:$dbPassword@localhost:5433/${dbName}?sslmode=disable
set EZTEST_API_PORT=$apiPort
set EZTEST_WEB_PORT=$webPort
set EZTEST_EXECUTOR_PORT=$execPort
set EZTEST_STORAGE_DIR=$storage
set EZTEST_INITIAL_ADMIN_PASSWORD=$adminPassword
set EZTEST_ADMIN_USERNAME=admin
set EZTEST_SNOWFLAKE_NODE_ID=8
"$backendExe" >> "$work\api.log" 2>&1
"@ | Set-Content -Encoding ASCII $runApi

        $runExecutor = Join-Path $work 'run-executor.cmd'
        @"
@echo off
cd /d "$repoRoot\executor"
set EZTEST_EXECUTOR_TOKEN=$execToken
set EZTEST_EXECUTOR_ADDR=127.0.0.1:$execPort
set EZTEST_API_URL=http://127.0.0.1:$apiPort
set EZTEST_EXECUTOR_NAME=w2-13-exploration
"$nodeExe" src/server.mjs >> "$work\executor.log" 2>&1
"@ | Set-Content -Encoding ASCII $runExecutor

        $runProxy = Join-Path $work 'run-vision-proxy.cmd'
        @"
@echo off
cd /d "$work"
set FAKE_LLM_PORT=$proxyPort
set FAKE_LLM_UPSTREAM=$llmBase
set FAKE_LLM_API_KEY=$llmKey
set FAKE_LLM_MODEL=$llmModel
"$nodeExe" "$work\vision-proxy.mjs" >> "$work\vision-proxy.log" 2>&1
"@ | Set-Content -Encoding ASCII $runProxy

        $runWeb = Join-Path $work 'run-web.cmd'
        @"
@echo off
cd /d "$repoRoot\frontend"
set NEXT_PUBLIC_API_BASE_URL=http://127.0.0.1:$apiPort
"$nodeExe" "$repoRoot\frontend\node_modules\next\dist\bin\next" dev -p $webPort >> "$work\web.log" 2>&1
"@ | Set-Content -Encoding ASCII $runWeb

        # Vision-caption proxy: text requests are forwarded verbatim to the real
        # upstream; image requests get a deterministic caption. The existing W2
        # LLM config points at 127.0.0.1:13999, so reusing the proxy keeps the
        # shared DB's LLM configuration untouched.
        @'
import http from "node:http"
const port = Number(process.env.FAKE_LLM_PORT || 13999)
const upstream = (process.env.FAKE_LLM_UPSTREAM || "https://api.deepseek.com").replace(/\/+$/, "")
const apiKey = process.env.FAKE_LLM_API_KEY || ""
const model = process.env.FAKE_LLM_MODEL || "deepseek-flash"
const MAX = 50 * 1024 * 1024
function sendJson(res, status, obj) {
  const body = Buffer.from(JSON.stringify(obj))
  res.writeHead(status, { "content-type": "application/json", "content-length": body.length })
  res.end(body)
}
function hasImage(value) {
  if (!value || typeof value !== "object") return false
  if (Array.isArray(value)) return value.some(hasImage)
  for (const [k, v] of Object.entries(value)) {
    if (k === "image_url" || k === "input_image") return true
    if (hasImage(v)) return true
  }
  return false
}
function caption() { return "A web application screenshot showing a navigation bar, a content list and action controls." }
const server = http.createServer(async (req, res) => {
  const url = new URL(req.url || "/", "http://127.0.0.1")
  if (req.method === "GET" && (url.pathname === "/healthz" || url.pathname === "/")) { return sendJson(res, 200, { status: "ok", service: "vision-caption-proxy" }) }
  if (req.method === "GET" && url.pathname.endsWith("/models")) { return sendJson(res, 200, { object: "list", data: [{ id: model, object: "model", owned_by: "deepseek" }] }) }
  if (req.method === "POST" && url.pathname.endsWith("/chat/completions")) {
    const chunks = []
    let size = 0
    for await (const chunk of req) { size += chunk.length; if (size > MAX) { res.writeHead(413); return res.end("too large") } chunks.push(chunk) }
    const raw = Buffer.concat(chunks)
    let body = null
    try { body = JSON.parse(raw.toString("utf8")) } catch { body = null }
    if (hasImage(body)) {
      return sendJson(res, 200, { id: "chatcmpl-caption", object: "chat.completion", created: Math.floor(Date.now() / 1000), model, choices: [{ index: 0, message: { role: "assistant", content: caption() }, finish_reason: "stop" }], usage: { prompt_tokens: 1200, completion_tokens: 30, total_tokens: 1230 } })
    }
    const headers = { "content-type": "application/json" }
    if (apiKey) headers.authorization = `Bearer ${apiKey}`
    try {
      const upstreamRes = await fetch(`${upstream}${url.pathname}${url.search}`, { method: "POST", headers, body: raw })
      const buf = Buffer.from(await upstreamRes.arrayBuffer())
      res.writeHead(upstreamRes.status, { "content-type": upstreamRes.headers.get("content-type") || "application/json", "content-length": buf.length })
      res.end(buf)
    } catch (err) { sendJson(res, 502, { error: { message: `upstream failed: ${err.message}` } }) }
    return
  }
  sendJson(res, 404, { error: { message: `unknown path ${url.pathname}` } })
})
server.listen(port, "127.0.0.1", () => console.log(`[vision-caption-proxy] 127.0.0.1:${port} upstream=${upstream}`))
'@ | Set-Content -Encoding ASCII (Join-Path $work 'vision-proxy.mjs')

        function Start-DetachedConsole([string]$name, [string]$commandFile) {
            $null = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{
                CommandLine      = "cmd.exe /c start `"$name`" /min `"$commandFile`""
                CurrentDirectory = $work
            }
        }

        Remove-Item (Join-Path $work 'api.log') -Force -ErrorAction SilentlyContinue
        Remove-Item (Join-Path $work 'executor.log') -Force -ErrorAction SilentlyContinue
        Remove-Item (Join-Path $work 'vision-proxy.log') -Force -ErrorAction SilentlyContinue
        Remove-Item (Join-Path $work 'web.log') -Force -ErrorAction SilentlyContinue
        Start-DetachedConsole 'w2-13-vision-proxy' $runProxy
        Start-DetachedConsole 'w2-13-executor' $runExecutor
        Start-DetachedConsole 'w2-13-api' $runApi
        Start-DetachedConsole 'w2-13-web' $runWeb
        $script:stackStarted = $true

        $apiReady = $false
        for ($i = 0; $i -lt 120; $i++) {
            try {
                $health = Invoke-RestMethod "http://127.0.0.1:$apiPort/api/health" -TimeoutSec 2
                $apiReady = $true
                Write-Host "== api ready version=$($health.data.version) db=$($health.data.db)"
                break
            } catch { Start-Sleep -Seconds 1 }
        }
        if (-not $apiReady) {
            Get-Content (Join-Path $work 'api.log') -Tail 40 -ErrorAction SilentlyContinue
            throw 'eztest API did not become ready'
        }
        $execReady = $false
        for ($i = 0; $i -lt 60; $i++) {
            try { $null = Invoke-RestMethod "http://127.0.0.1:$execPort/healthz" -TimeoutSec 2; $execReady = $true; break } catch { Start-Sleep -Seconds 1 }
        }
        if (-not $execReady) { Exit-ExternalBlocked "the real executor did not become ready on 127.0.0.1:$execPort" }
        if (-not (Wait-Http200 "http://127.0.0.1:$webPort/login" 180)) { throw 'frontend did not become ready' }
        if (-not (Wait-Http200 "http://127.0.0.1:$proxyPort/healthz" 60)) { Exit-ExternalBlocked 'the vision-caption proxy did not become ready on 127.0.0.1:13999' }
        Write-Host "== stack ready (api=$apiPort executor=$execPort web=$webPort proxy=$proxyPort)"

        # -------------------------------------------------------------------
        # 3. Point execution.executor_url at the isolated executor. The
        #    original value is restored in the finally block.
        # -------------------------------------------------------------------
        $login = Invoke-RestMethod -Uri "$apiBase/api/auth/login" -Method Post -ContentType 'application/json' -Body (@{ username = 'admin'; password = $adminPassword } | ConvertTo-Json)
        $script:token = $login.data.token
        $headers = @{ authorization = "Bearer $($script:token)" }
        $current = Invoke-RestMethod -Uri "$apiBase/api/admin/settings/execution.executor_url" -Headers $headers
        $originalExecutorUrl = [string]$current.data.value
        Set-Content -Encoding ASCII $originalExecutorUrlPath $originalExecutorUrl
        $null = Invoke-RestMethod -Uri "$apiBase/api/admin/settings/execution.executor_url" -Method Patch -Headers $headers -ContentType 'application/json' -Body (@{ value = $executorUrl } | ConvertTo-Json)
        Write-Host "== execution.executor_url: $originalExecutorUrl -> $executorUrl"

        # -------------------------------------------------------------------
        # 4. Embedded Node production flow.
        # -------------------------------------------------------------------
        $flowPath = Join-Path $work 'exploration-flow.mjs'
        @'
// EzTest v0.22 W2-13 execution-time exploration e2e flow.
//
// Real product APIs only: the script creates the environment/version/
// requirement, lets the real design Host/Author produce C1, executes wave 1
// through the real executor, approves the discovered resources through the
// existing admin confirm routes, starts wave 2, rolls back an auto-confirmed
// revision through the W2-12 UI and writes the verdict contract.
import fs from "node:fs"
import path from "node:path"
import { spawnSync } from "node:child_process"
import { createRequire } from "node:module"

const BASE = process.env.W213_API
const WEB = process.env.W213_WEB
const EXECUTOR_URL = process.env.W213_EXECUTOR
const EVIDENCE = process.env.W213_EVIDENCE
const RUN = process.env.W213_RUN
const SHOTS = process.env.W213_SHOTS
const REPO_ROOT = process.env.W213_REPO_ROOT
const PSQL = process.env.W213_PSQL
const DB_NAME = process.env.W213_DB
const DB_PASSWORD = process.env.W213_DB_PASSWORD
const ADMIN_PASSWORD = process.env.W213_ADMIN_PW
const SENTINEL = "W2_13_DISCOVERY_SENTINEL"
const DESIGN_TIMEOUT = Number(process.env.W213_DESIGN_TIMEOUT || 1200) * 1000
const WAVE_TIMEOUT = Number(process.env.W213_WAVE_TIMEOUT || 1200) * 1000
const WAVE2_TIMEOUT = Number(process.env.W213_WAVE2_TIMEOUT || 1200) * 1000
const AUTO_TIMEOUT = Number(process.env.W213_AUTO_TIMEOUT || 180) * 1000
const UI_TIMEOUT = Number(process.env.W213_UI_TIMEOUT || 240) * 1000
const SUT_WEB = "http://localhost:5173"

const requireFromExecutor = createRequire(path.join(REPO_ROOT, "executor", "package.json"))
fs.mkdirSync(SHOTS, { recursive: true })

const designDir = path.join(RUN, "exploration")
fs.mkdirSync(designDir, { recursive: true })

const state = { checks: {}, startedAt: new Date().toISOString(), red: [] }
const steps = []
let token = ""
let lastHttp = null
let browser = null
let pageOverview = null
let pageDesign = null

function log(...args) { console.log(new Date().toISOString(), ...args) }
function saveJson(name, value) { fs.writeFileSync(path.join(RUN, name), JSON.stringify(value, null, 2), "utf8") }
function saveDesign(name, value) { fs.writeFileSync(path.join(designDir, name), JSON.stringify(value, null, 2), "utf8") }
function sleep(ms) { return new Promise((resolve) => setTimeout(resolve, ms)) }
function nowIso() { return new Date().toISOString() }

function listItems(json) {
  if (!json) return []
  if (Array.isArray(json.data)) return json.data
  if (json.data && Array.isArray(json.data.items)) return json.data.items
  return []
}

function dbQuery(sql) {
  const result = spawnSync(PSQL, ["-h", "localhost", "-p", "5433", "-U", "eztest", "-d", DB_NAME, "-tAc", sql], {
    env: { ...process.env, PGPASSWORD: DB_PASSWORD }, encoding: "utf8",
  })
  if (result.status !== 0) {
    throw new Error(`psql failed (status=${result.status}): ${String(result.stderr || result.error?.message || "").trim().slice(0, 400)}`)
  }
  return String(result.stdout || "").trim()
}
function dbJson(sql) {
  const raw = dbQuery(sql)
  try { return raw === "" ? null : JSON.parse(raw) } catch { return null }
}

async function api(method, p, body) {
  const headers = {}
  if (token) headers.authorization = `Bearer ${token}`
  let payload
  if (body !== undefined && body !== null) { headers["content-type"] = "application/json"; payload = JSON.stringify(body) }
  const res = await fetch(`${BASE}${p}`, { method, headers, body: payload })
  const text = await res.text()
  let json = null
  try { json = text === "" ? null : JSON.parse(text) } catch { json = null }
  lastHttp = { method, path: p, status: res.status }
  return { status: res.status, json, text }
}
async function must(method, p, body, expected = [200, 201]) {
  const r = await api(method, p, body)
  if (!expected.includes(r.status)) throw new Error(`${method} ${p} -> ${r.status}: ${r.text.slice(0, 600)}`)
  return r.json
}
async function waitFor(label, fn, timeoutMs = 120000, intervalMs = 2000) {
  const deadline = Date.now() + timeoutMs
  let last = null
  while (Date.now() < deadline) {
    try { const value = await fn(); if (value) return value; last = value } catch (err) { last = String(err.message || err) }
    await sleep(intervalMs)
  }
  throw new Error(`timeout waiting for ${label} (last=${String(last).slice(0, 400)})`)
}

function check(name, ok, detail) {
  state.checks[name] = { ok: !!ok, detail: detail === undefined ? "" : String(detail) }
  if (!ok) state.red.push(`${name}: ${detail === undefined ? "" : String(detail)}`)
  log(`[${ok ? "PASS" : "RED"}] ${name}: ${detail === undefined ? "" : String(detail)}`)
  return !!ok
}
function recordStep(name, detail = {}) {
  const step = { index: steps.length + 1, step: name, at: nowIso(), ...detail }
  steps.push(step)
  saveJson("exploration-steps.json", steps)
  saveJson("exploration-state.json", state)
  log(`[STEP ${step.index}] ${name}`, JSON.stringify(detail).slice(0, 600))
}

// ---------------------------------------------------------------------------
// Browser helpers.
// ---------------------------------------------------------------------------
async function ensureBrowser() {
  if (browser) return browser
  try {
    const { chromium } = requireFromExecutor("playwright")
    try {
      browser = await chromium.launch({ headless: true, channel: "chrome" })
    } catch (err) {
      log("chrome channel unavailable, falling back to msedge:", String(err.message).split("\n")[0])
      browser = await chromium.launch({ headless: true, channel: "msedge" })
    }
  } catch (err) {
    saveDesign("exploration-browser-blocked.json", { blocked: true, reason: String(err.message || err) })
    log("EXTERNAL-BLOCKED: cannot launch Playwright browser:", String(err.message || err).slice(0, 400))
    process.exit(2)
  }
  return browser
}
async function loginUi() {
  const p = await ensureBrowser().then((b) => b.newPage({ viewport: { width: 1440, height: 900 } }))
  p.setDefaultTimeout(45000)
  await p.goto(`${WEB}/login`, { waitUntil: "domcontentloaded" })
  await sleep(900)
  if (p.url().includes("/login")) {
    if (await p.locator("#username").count()) {
      await p.fill("#username", "admin")
      await p.fill("#password", ADMIN_PASSWORD)
      await p.locator("button[type=submit]").click()
    }
    await p.waitForURL(/\/versions/, { timeout: 60000 })
  }
  return p
}
async function openVersionTab(p, tab) {
  await p.goto(`${WEB}/versions/${state.versionId}?tab=${tab}`, { waitUntil: "domcontentloaded" })
  await p.waitForSelector('[data-testid="stage-bar"]', { timeout: 60000 })
  await sleep(1200)
}
async function screenshotElement(p, selector, name) {
  const locator = p.locator(selector).first()
  await locator.waitFor({ state: "visible", timeout: 60000 })
  await locator.scrollIntoViewIfNeeded()
  await sleep(500)
  const target = path.join(SHOTS, name)
  await locator.screenshot({ path: target })
  return { path: `exploration/${name}`, bytes: fs.statSync(target).size }
}
async function screenshotPage(p, name) {
  const target = path.join(SHOTS, name)
  await p.screenshot({ path: target, fullPage: true })
  return { path: `exploration/${name}`, bytes: fs.statSync(target).size }
}

// ---------------------------------------------------------------------------
// Product helpers.
// ---------------------------------------------------------------------------
async function stage() {
  const r = await must("GET", `/api/versions/${state.versionId}/stage`)
  return r.data.stage
}
async function listWaves() {
  return listItems(await must("GET", `/api/versions/${state.versionId}/waves?limit=50`))
}
async function waitWaveTerminal(waveId, timeoutMs) {
  return waitFor(`wave ${waveId} terminal`, async () => {
    const r = await api("GET", `/api/versions/${state.versionId}/waves/${waveId}`)
    if (r.status !== 200) return null
    const wave = r.json?.data
    return wave && ["done", "failed", "cancelled"].includes(wave.status) ? wave : null
  }, timeoutMs, 3000)
}
async function caseRuns(waveId) {
  return listItems(await must("GET", `/api/versions/${state.versionId}/waves/${waveId}/case-runs`))
}
async function coverage() {
  return (await must("GET", `/api/versions/${state.versionId}/coverage`)).data
}
async function observations(waveId) {
  const q = waveId ? `?wave_id=${waveId}&limit=200` : "?limit=200"
  return listItems(await must("GET", `/api/versions/${state.versionId}/discovery/observations${q}`))
}
async function deltas(waveId) {
  return (await must("GET", `/api/versions/${state.versionId}/discovery/deltas?wave_id=${waveId}`)).data
}
async function requirementView(requirementId) {
  return (await must("GET", `/api/versions/${state.versionId}/requirements/${requirementId}`)).data
}
// DB JSON projections must render snowflake IDs as decimal strings; a JSON
// number round-trips through Node's float64 and silently changes the last
// digits.
function dbRevisionRows(triggerCondition) {
  return dbJson(`select coalesce(json_agg(json_build_object('case_version_id', cv.id::text, 'case_id', cv.case_id::text, 'revision', cv.revision, 'status', cv.status, 'trigger', cv.trigger, 'auto_confirmed_by', cv.auto_confirmed_by, 'source_observation_id', cv.source_observation_id::text, 'rollback_of', cv.rollback_of::text) order by cv.id), '[]')::text from case_version cv join case_entity ce on ce.id = cv.case_id join case_scenario s on s.id = ce.scenario_id where s.version_id = ${state.versionId} and ${triggerCondition}`) || []
}
async function ensureRequirementClarified(requirementId) {
  const current = await requirementView(requirementId)
  if (current.status === "clarified") return { status: current.status, reopened: false }
  if (current.status === "closed") {
    await must("POST", `/api/versions/${state.versionId}/requirements/${requirementId}/reopen`, { reason: "W2-13 keep the requirement open for the execution-time discovery loop" })
    await must("POST", `/api/versions/${state.versionId}/requirements/${requirementId}/confirmations`, {})
    const next = await requirementView(requirementId)
    return { status: next.status, reopened: true }
  }
  if (current.status === "draft") {
    await must("POST", `/api/versions/${state.versionId}/requirements/${requirementId}/confirmations`, {})
    const next = await requirementView(requirementId)
    return { status: next.status, reopened: false }
  }
  return { status: current.status, reopened: false }
}
async function graphCases() {
  const graph = await must("GET", `/api/versions/${state.versionId}/design/graph`)
  const cases = []
  for (const requirement of graph.data.requirements || []) {
    for (const scenario of requirement.scenarios || []) {
      for (const card of scenario.cases || []) {
        cases.push({ ...card, requirement_id: requirement.requirement_id, scenario_name: scenario.name })
      }
    }
  }
  return { graph: graph.data, cases }
}
async function caseDetail(caseId) {
  return (await must("GET", `/api/versions/${state.versionId}/design/cases/${caseId}`)).data
}
function caseScore(detail) {
  const selected = detail.selected || {}
  const text = JSON.stringify({ steps: selected.steps || [], assertions: selected.assertions || [], title: selected.title || "" })
  let score = 0
  if (/browser\.goto|goto|reload|refresh|\u5237\u65b0|\u91cd\u65b0\u52a0\u8f7d/i.test(text)) score += 4
  if (/click|\u70b9\u51fb/i.test(text)) score += 2
  if (text.includes(SENTINEL)) score += 4
  if (/\/posts\//.test(text)) score += 2
  score += Math.min(3, (selected.steps || []).length)
  return score
}
function caseText(detail) {
  const selected = detail.selected || {}
  return JSON.stringify({ steps: selected.steps || [], assertions: selected.assertions || [] })
}

async function main() {
  // -- login ----------------------------------------------------------------
  const login = await must("POST", "/api/auth/login", { username: "admin", password: ADMIN_PASSWORD })
  token = login.data.token
  state.adminId = login.data.user.id
  recordStep("auth.login", { http: lastHttp, user: login.data.user.username })

  // -- real SUT article used by C1 -----------------------------------------
  const sutPosts = await fetch(`${SUT_WEB.replace(":5173", ":8080")}/api/v1/posts?limit=3`).then((r) => r.json())
  if (!sutPosts || !Array.isArray(sutPosts.data) || sutPosts.data.length === 0) {
    throw new Error("EXTERNAL-BLOCKED: the SUT has no published posts to link to")
  }
  const article = sutPosts.data[0]
  const articleUrl = `${SUT_WEB}/posts/${article.id}`
  state.article = { id: String(article.id), title: article.title, url: articleUrl }
  recordStep("sut.article", state.article)

  // -- SUT + dedicated environment (only P = home page approved) ------------
  const suts = listItems(await must("GET", "/api/resources/suts"))
  const sut = suts.find((entry) => String(entry.name).includes("W2") || String(entry.name).includes("blog")) || suts[0]
  if (!sut) throw new Error("no SUT resource exists in the database")
  state.sutId = sut.id
  const environment = await must("POST", `/api/resources/suts/${sut.id}/environments`, {
    name: `W2-13 exploration env ${Date.now()}`,
    base_url: SUT_WEB,
    is_default: false,
  })
  state.environmentId = environment.data.id
  const home = await must("POST", `/api/resources/environments/${state.environmentId}/pages`, {
    name: "Home (P)",
    url: `${SUT_WEB}/`,
    route_template: "/",
  })
  state.homePageId = home.data.id
  check("environmentHomePageApproved", home.data.status === "approved", `page=${home.data.id} status=${home.data.status}`)
  recordStep("environment.created", { http: lastHttp, environmentId: state.environmentId, homePageId: state.homePageId, sutId: state.sutId })

  // -- version bound to the dedicated environment ---------------------------
  const version = await must("POST", "/api/versions", {
    title: `W2-13 exploration version ${Date.now()}`,
    description: "W2-13 real-SUT execution-time discovery (P -> U -> A) acceptance",
    environment_id: state.environmentId,
  })
  state.versionId = version.data.id
  const versionRead = await must("GET", `/api/versions/${state.versionId}`)
  const binding = (versionRead.data.bindings || [])[0] || {}
  state.bindingId = binding.id
  state.bindingPages = binding.pages || []
  state.bindingApis = binding.apis || []
  check("versionBoundToDedicatedEnvironment",
    !!binding.id && (binding.environment || {}).id === state.environmentId && state.bindingPages.length === 1 && state.bindingApis.length === 0,
    `environment=${(binding.environment || {}).id} pages=${state.bindingPages.length} apis=${state.bindingApis.length}`)
  recordStep("version.created", { http: lastHttp, versionId: state.versionId, bindingId: state.bindingId })

  if ((await stage()) === "created") { await must("POST", `/api/versions/${state.versionId}/stage/advance`, {}); await sleep(800) }
  check("stageClarifying", (await stage()) === "clarifying", `stage=${await stage()}`)

  // -- one requirement: P -> click -> U; reload U; impossible sentinel ------
  const requirement = await must("POST", `/api/versions/${state.versionId}/requirements`, {
    title: "W2-13 exploration: home article link into the unregistered detail page",
    description: `A visitor opens the registered home page P, clicks the first published article title link to enter the unregistered article detail page U (${articleUrl}), reloads U with a full document load and verifies the article content and API.`,
    acceptance_criteria: [
      "Given a visitor opens the registered home page http://localhost:5173/ (page P) and waits for it to load.",
      "When the visitor clicks the first published article title link in the latest-articles area, the browser enters the article detail page /posts/{id} (unregistered page U).",
      `Then use browser.goto to open the article detail URL ${articleUrl} again (same article page, forcing a full document load), wait until the document is loaded, and verify the article title and body are visible and the article detail API GET /api/v1/posts/{id} returns HTTP 200.`,
      `Finally assert the browser document.title strictly equals the string ${SENTINEL}; any other title is a failure and the actual title must be recorded. This assertion is expected to fail and keeps the requirement open while the execution-time discovery loop runs.`,
    ].join(" "),
    priority: "P2",
    constraints: { source: "w2-13-exploration-e2e", role: "navigation" },
    targets: { version_suts: [state.bindingId], pages: [state.homePageId], apis: [] },
  })
  state.requirementId = requirement.data.id
  await must("POST", `/api/versions/${state.versionId}/requirements/confirmations`, { requirement_ids: [state.requirementId] })
  const confirmedRequirement = await requirementView(state.requirementId)
  check("requirementClarified", confirmedRequirement.status === "clarified", `requirement=${state.requirementId} status=${confirmedRequirement.status}`)
  recordStep("requirement.created", { http: lastHttp, requirementId: state.requirementId, status: confirmedRequirement.status })

  // -- requirement_review -> designing --------------------------------------
  for (let i = 0; i < 4; i++) {
    const current = await stage()
    if (current === "clarifying" || current === "requirement_review") {
      const advanced = await api("POST", `/api/versions/${state.versionId}/stage/advance`, {})
      if (advanced.status !== 200) throw new Error(`stage advance ${current} -> ${advanced.status}: ${advanced.text.slice(0, 300)}`)
      await sleep(700)
    } else break
  }
  check("stageDesigning", (await stage()) === "designing", `stage=${await stage()}`)

  // -- real design Host/Author ---------------------------------------------
  const design = await waitFor("design cases at case_review", async () => {
    const current = await stage()
    const result = await graphCases()
    return current === "case_review" && result.cases.length > 0 ? { stage: current, ...result } : null
  }, DESIGN_TIMEOUT, 3000)
  recordStep("design.cases", { http: lastHttp, stage: design.stage, cases: design.cases.length })
  // Let the design round settle so a confirmation never races a case write.
  try {
    await waitFor("design hosts settle", async () => {
      const runs = listItems(await must("GET", "/api/agent-center/runs?limit=100"))
      const running = runs.filter((run) => ["design_host", "design_author", "design_critic"].includes(run.profile_key) && run.status === "running")
      return running.length === 0
    }, 180000, 3000)
  } catch (err) { log("design host did not settle:", String(err.message).slice(0, 200)) }

  // -- pick C1: a case that opens P, clicks the article link, reloads U with a
  //    full document load and carries the unusable sentinel assertion.
  const gotoRegex = /browser\.goto|reload|refresh|\u5237\u65b0|\u91cd\u65b0\u52a0\u8f7d/i
  const clickRegex = /click|\u70b9\u51fb/i
  async function pickCase() {
    const result = await graphCases()
    const requirementCases = result.cases.filter((entry) => String(entry.requirement_id) === String(state.requirementId) && Number(entry.step_count) > 0 && Number(entry.assertion_count) > 0)
    const scored = []
    for (const card of requirementCases) {
      const detail = await caseDetail(card.case_id)
      const text = caseText(detail)
      const score = caseScore(detail) + (clickRegex.test(text) ? 2 : 0)
      scored.push({ card, detail, text, score })
    }
    scored.sort((a, b) => b.score - a.score)
    return { result, scored }
  }
  async function findAdequateCase() {
    const result = await graphCases()
    for (const card of result.cases) {
      if (String(card.requirement_id) !== String(state.requirementId)) continue
      if (Number(card.step_count) < 1 || Number(card.assertion_count) < 1) continue
      const detail = await caseDetail(card.case_id)
      const text = caseText(detail)
      if (text.includes(SENTINEL) && gotoRegex.test(text) && clickRegex.test(text)) {
        return { card, detail, text, score: caseScore(detail) + 2 }
      }
    }
    return null
  }
  let picked = await pickCase()
  if (picked.scored.length === 0) throw new Error("the design Host produced no executable case for the exploration requirement")
  let combined = picked.scored[0]
  saveDesign("exploration-c1-initial.json", { caseId: combined.card.case_id, caseVersionId: combined.detail.selected.case_version_id, title: combined.detail.selected.title, steps: combined.detail.selected.steps, assertions: combined.detail.selected.assertions, score: combined.score })
  recordStep("design.c1.initial", { http: lastHttp, caseId: combined.card.case_id, caseVersionId: combined.detail.selected.case_version_id, score: combined.score })

  // The C1 must carry an unusable document.title assertion, a full navigation
  // and the P -> click -> U link. If the real Host split those requirements
  // across sibling cases, ask it once to submit one combined revision.
  let c1Text = combined.text
  const needsRevision = !c1Text.includes(SENTINEL) || !gotoRegex.test(c1Text) || !clickRegex.test(c1Text)
  if (needsRevision) {
    try {
      const conversations = listItems(await must("GET", `/api/versions/${state.versionId}/conversations?limit=20`))
      const designConversation = conversations.find((entry) => entry.kind === "design")
      if (designConversation) {
        const instruction = `Please revise case ${combined.card.case_id}. The case must: (1) open http://localhost:5173/ (home page P); (2) click the first published article title link in the latest-articles area; (3) call browser.goto on ${articleUrl} to reload the article detail page U with a full document load; (4) verify the article title/body and GET /api/v1/posts/{id} HTTP 200; (5) assert document.title strictly equals ${SENTINEL} and record the actual title. Keep the existing case ID and submit the full revision with case.graph_propose.`
        await must("POST", `/api/versions/${state.versionId}/conversations/${designConversation.id}/messages`, {
          client_message_id: `w2-13-revise-${Date.now()}`,
          parts: [{ type: "text", text: instruction }],
        })
        const adequate = await waitFor("design C1 combined revision", async () => findAdequateCase(), 600000, 5000)
        picked = await pickCase()
        const revisedEntry = picked.scored.find((entry) => String(entry.card.case_id) === String(adequate.card.case_id))
        combined = revisedEntry || adequate
        c1Text = combined.text
        log("design C1 revised", combined.detail.selected.case_version_id)
      }
    } catch (err) { log("design C1 revision failed (continuing with the current case):", String(err.message).slice(0, 300)) }
  }
  state.c1CaseId = combined.card.case_id
  state.c1CaseVersionId = combined.detail.selected.case_version_id
  // If the combined case still lacks the click, confirm the best click case as
  // a supporting case so the P -> click -> U path is exercised in wave 1 too.
  const clickSupport = picked.scored.find((entry) =>
    String(entry.card.case_id) !== String(state.c1CaseId) &&
    clickRegex.test(entry.text) && /\/posts\//.test(entry.text))
  state.supportingClickCaseId = (!clickRegex.test(c1Text) && clickSupport) ? String(clickSupport.card.case_id) : null
  check("designCaseSelected", combined.score >= 8 && c1Text.includes(SENTINEL) && gotoRegex.test(c1Text),
    `case=${state.c1CaseId} score=${combined.score} title=${combined.detail.selected.title}`)
  check("designC1ClicksIntoU", clickRegex.test(c1Text) || !!state.supportingClickCaseId,
    `c1Clicks=${clickRegex.test(c1Text)} supportingClickCase=${state.supportingClickCaseId || "none"}`)
  saveDesign("exploration-c1.json", { caseId: state.c1CaseId, caseVersionId: state.c1CaseVersionId, title: combined.detail.selected.title, steps: combined.detail.selected.steps, assertions: combined.detail.selected.assertions, score: combined.score, supportingClickCaseId: state.supportingClickCaseId })

  // -- keep the version report gate closed between waves --------------------
  const humanRequest = await must("POST", `/api/versions/${state.versionId}/human-requests`, {
    kind: "approval",
    reason: "W2-13 acceptance: hold the version report gate open between exploration waves",
  })
  state.humanRequestId = humanRequest.data.id
  recordStep("humanRequest.created", { humanRequestId: state.humanRequestId, kind: humanRequest.data.kind })

  // -- confirm C1 (and the supporting click case when the Host split the
  //    click path into a sibling case), enter executing, start wave 1 --------
  const c1Confirmation = await must("POST", `/api/versions/${state.versionId}/design/confirmations`, {
    scope: "case", target_id: state.c1CaseId, batch_id: "w2-13-c1",
  })
  const c1Confirmed = (c1Confirmation.data.confirmed || [])[0]
  if (c1Confirmed) state.c1CaseVersionId = c1Confirmed.case_version_id
  check("c1Confirmed", !!c1Confirmed && !!state.c1CaseVersionId, `case=${state.c1CaseId} case_version=${state.c1CaseVersionId}`)
  if (state.supportingClickCaseId) {
    const supportConfirmation = await must("POST", `/api/versions/${state.versionId}/design/confirmations`, {
      scope: "case", target_id: state.supportingClickCaseId, batch_id: "w2-13-clicksupport",
    })
    const supportConfirmed = (supportConfirmation.data.confirmed || [])[0]
    state.supportingClickCaseVersionId = supportConfirmed ? supportConfirmed.case_version_id : null
    check("supportingClickCaseConfirmed", !!state.supportingClickCaseVersionId, `case=${state.supportingClickCaseId} case_version=${state.supportingClickCaseVersionId}`)
  }

  for (let i = 0; i < 4; i++) {
    if ((await stage()) === "case_review") { await must("POST", `/api/versions/${state.versionId}/stage/advance`, {}); await sleep(900) } else break
  }
  check("stageExecuting", (await stage()) === "executing", `stage=${await stage()}`)

  // The scheduler may auto-plan the wave on the executing stage entry; attach
  // to it instead of racing a second POST /waves.
  let wave1 = null
  try {
    wave1 = await waitFor("wave 1 start", async () => {
      const waves = await listWaves()
      const active = waves.find((wave) => !["done", "failed", "cancelled"].includes(wave.status))
      return active || null
    }, 20000, 1500)
  } catch (err) {
    const started = await api("POST", `/api/versions/${state.versionId}/waves`, {})
    if (started.status !== 201 && started.status !== 200) throw new Error(`POST /waves -> ${started.status}: ${started.text.slice(0, 400)}`)
    wave1 = { id: started.json?.data?.wave_id }
  }
  state.wave1Id = String(wave1.id)
  const wave1Done = await waitWaveTerminal(state.wave1Id, WAVE_TIMEOUT)
  const wave1Runs = await caseRuns(state.wave1Id)
  state.wave1CaseRunIdsBefore = wave1Runs.map((run) => String(run.id)).sort()
  state.wave1RunStatuses = wave1Runs.map((run) => `${run.id}:${run.status}`)
  check("wave1Terminal", ["done", "failed", "cancelled"].includes(wave1Done.status), `wave=${state.wave1Id} status=${wave1Done.status} counts=${JSON.stringify(wave1Done.counts)}`)
  check("wave1ExecutedC1", wave1Runs.some((run) => String(run.case_id) === String(state.c1CaseId)), `runs=${state.wave1RunStatuses.join(",")}`)
  recordStep("wave1.terminal", { waveId: state.wave1Id, status: wave1Done.status, runs: state.wave1RunStatuses })

  // -- discovery observations / deltas / coverage before approval -----------
  // The terminal capture writes the observation rows; the W1-12 delta then
  // classifies them (decision=proposed, resource_id/fact_id) and publishes the
  // wave-level resource.delta. Wait for the classification, not just the row.
  const wave1Observations = await waitFor("wave 1 discovery delta classification", async () => {
    const rows = await observations(state.wave1Id)
    const page = rows.find((row) => row.kind === "page" && row.resource_template === "/posts/{id}" && row.decision === "proposed" && row.resource_id)
    const api = rows.find((row) => row.kind === "api" && String(row.method).toUpperCase() === "GET" && row.resource_template === "/api/v1/posts/{id}" && row.decision === "proposed" && row.resource_id)
    return page && api ? rows : null
  }, 180000, 3000)
  await waitFor("resource.delta event", async () => Number(dbQuery(`select count(*) from event where channel = 'version:${state.versionId}' and type = 'resource.delta'`)) >= 1, 60000, 2000)
  state.observations = wave1Observations
  const wave1Deltas = await deltas(state.wave1Id)
  state.deltas = wave1Deltas
  const coverageBefore = await coverage()
  state.coverageBefore = coverageBefore
  const pageObservations = wave1Observations.filter((row) => row.kind === "page")
  const apiObservations = wave1Observations.filter((row) => row.kind === "api")
  const uObservation = pageObservations.find((row) => String(row.resource_template) === "/posts/{id}")
  const aObservation = apiObservations.find((row) => String(row.method).toUpperCase() === "GET" && String(row.resource_template) === "/api/v1/posts/{id}")
  state.uPageId = uObservation ? String(uObservation.resource_id) : null
  state.aApiId = aObservation ? String(aObservation.resource_id) : null
  state.uPageObservationId = uObservation ? String(uObservation.observation_id) : null
  state.aApiObservationId = aObservation ? String(aObservation.observation_id) : null
  check("observationSourceExecution", wave1Observations.every((row) => row.source_kind === "execution") && wave1Observations.every((row) => row.wave_id === state.wave1Id),
    `observations=${wave1Observations.length} sources=${[...new Set(wave1Observations.map((row) => row.source_kind))].join(",")}`)
  check("observationU", !!uObservation && uObservation.kind === "page" && !!uObservation.resource_id && !!uObservation.fact_id,
    uObservation ? `observation=${uObservation.observation_id} template=${uObservation.resource_template} resource=${uObservation.resource_id} fact=${uObservation.fact_id}` : `page templates=${pageObservations.map((row) => row.resource_template).join(",")}`)
  check("observationA", !!aObservation && String(aObservation.method).toUpperCase() === "GET" && !!aObservation.resource_id && !!aObservation.fact_id,
    aObservation ? `observation=${aObservation.observation_id} method=${aObservation.method} template=${aObservation.resource_template} resource=${aObservation.resource_id} fact=${aObservation.fact_id}` : `api templates=${apiObservations.map((row) => `${row.method} ${row.resource_template}`).join(",")}`)
  const proposedPages = wave1Observations.filter((row) => row.kind === "page" && row.decision === "proposed" && row.resource_id)
  const proposedApis = wave1Observations.filter((row) => row.kind === "api" && row.decision === "proposed" && row.resource_id)
  state.proposedPageIds = [...new Set(proposedPages.map((row) => String(row.resource_id)))]
  state.proposedApiIds = [...new Set(proposedApis.map((row) => String(row.resource_id)))]
  state.resourceDeltaEvents = Number(dbQuery(`select count(*) from event where channel = 'version:${state.versionId}' and type = 'resource.delta'`))
  // R-EX-3 dedupe: a discovery observation may reuse an existing P9 fact for
  // the same resource instead of writing a second execution-sourced fact. The
  // acceptance fact counts are therefore the distinct facts carried by the
  // wave-1 observations (the page/API knowledge the discovery recorded), and
  // the execution-sourced counts are kept as a secondary detail.
  const factPageIds = new Set(wave1Observations.filter((row) => row.kind === "page" && row.fact_id).map((row) => String(row.fact_id)))
  const factApiIds = new Set(wave1Observations.filter((row) => row.kind === "api" && row.fact_id).map((row) => String(row.fact_id)))
  state.factPageCount = factPageIds.size
  state.factApiCount = factApiIds.size
  state.executionFactPageCount = Number(dbQuery(`select count(*) from version_fact where version_id = ${state.versionId} and kind = 'page' and source = 'execution'`))
  state.executionFactApiCount = Number(dbQuery(`select count(*) from version_fact where version_id = ${state.versionId} and kind = 'api' and source = 'execution'`))
  state.factPageIds = [...factPageIds]
  state.factApiIds = [...factApiIds]
  state.uPageFactId = uObservation ? String(uObservation.fact_id) : null
  state.aApiFactId = aObservation ? String(aObservation.fact_id) : null
  const coverageBeforeUncovered = (coverageBefore.pages?.uncovered || []).length + (coverageBefore.apis?.uncovered || []).length
  state.coverageBeforeUncovered = coverageBeforeUncovered
  check("discoveryDeltaRecorded", !!wave1Deltas && (wave1Deltas.counts?.proposed || 0) > 0, `counts=${JSON.stringify(wave1Deltas?.counts || {})}`)
  check("resourceDeltaEvent", state.resourceDeltaEvents >= 1, `resource.delta events=${state.resourceDeltaEvents}`)
  check("proposedResources", state.proposedPageIds.length >= 1 && state.proposedApiIds.length >= 1, `pages=${state.proposedPageIds.join(",")} apis=${state.proposedApiIds.join(",")}`)
  check("executionFacts", state.factPageCount >= 1 && state.factApiCount >= 1, `pageFacts=${state.factPageCount} (${state.factPageIds.join(",")}) apiFacts=${state.factApiCount} (${state.factApiIds.slice(0, 8).join(",")}) executionSourcedPage=${state.executionFactPageCount} executionSourcedApi=${state.executionFactApiCount}`)
  check("coverageBeforeUncovered", coverageBeforeUncovered >= 1, `uncovered=${coverageBeforeUncovered} pages=${(coverageBefore.pages?.uncovered || []).join(",")} apis=${(coverageBefore.apis?.uncovered || []).join(",")}`)
  if (!state.uPageId || !state.aApiId) throw new Error("wave 1 did not discover both U and A")
  recordStep("discovery.wave1", { waveId: state.wave1Id, observations: wave1Observations.length, proposedPages: state.proposedPageIds.length, proposedApis: state.proposedApiIds.length, uPageId: state.uPageId, aApiId: state.aApiId, coverageBeforeUncovered })

  // -- open the UI pages before the approve so the W2-12 in-session notice
  //    receives the case.revision.auto_confirmed SSE event -----------------
  pageOverview = await loginUi()
  await openVersionTab(pageOverview, "overview")
  await pageOverview.waitForSelector('[data-testid="discovery-panel"]', { timeout: 60000 })
  await pageOverview.locator('[data-testid="discovery-panel"]').getByText("/posts/{id}").first().waitFor({ timeout: 60000 })
  const shotBefore = await screenshotElement(pageOverview, '[data-testid="discovery-overview"]', "01-discovery-before.png")
  state.screenshots = [shotBefore.path]
  recordStep("ui.discovery.before", shotBefore)

  pageDesign = await loginUi()
  await openVersionTab(pageDesign, "design")
  await pageDesign.waitForSelector('[data-testid="design-workbench"]', { timeout: 60000 })
  await sleep(2500)

  // -- keep the requirement clarified at approval time ------------------------
  // A passed C1 can close the requirement before the W2-11 Critic runs, so the
  // script re-opens and re-confirms it immediately before the resource confirm
  // calls (the reopen keeps the same current requirement version, so the case
  // provenance stays valid). The pending human request keeps the version in the
  // executing stage even after the requirement closes, so wave 2 can start.
  const clarified = await ensureRequirementClarified(state.requirementId)
  check("requirementStillClarified", clarified.status === "clarified", `status=${clarified.status} reopened=${clarified.reopened}`)

  // -- admin approves U and A through the existing resource confirm routes --
  const pageConfirm = await must("POST", `/api/resources/pages/${state.uPageId}/confirm`, {})
  const apiConfirm = await must("POST", `/api/resources/apis/${state.aApiId}/confirm`, {})
  check("resourceApprovedU", pageConfirm.data.status === "approved", `page=${state.uPageId} status=${pageConfirm.data.status}`)
  check("resourceApprovedA", apiConfirm.data.status === "approved", `api=${state.aApiId} status=${apiConfirm.data.status}`)

  // -- W2-11 auto-confirm ----------------------------------------------------
  const autoConfirmed = await waitFor("discovery_delta auto-confirmed cases", async () => {
    const rows = dbRevisionRows("cv.trigger = 'discovery_delta'")
    const confirmed = rows.filter((row) => row.status === "confirmed" && row.auto_confirmed_by === "system")
    return confirmed.length >= 1 ? confirmed : null
  }, AUTO_TIMEOUT, 1000)
  // Wait for both resources to settle (one revision per discovered resource).
  const autoConfirmedBoth = await waitFor("two auto-confirmed discovery revisions", async () => {
    const rows = dbRevisionRows("cv.trigger = 'discovery_delta'")
    return rows.filter((row) => row.status === "confirmed" && row.auto_confirmed_by === "system").length >= 2 ? rows : null
  }, AUTO_TIMEOUT, 1500).catch(() => autoConfirmed)
  state.autoConfirmedRevisions = autoConfirmedBoth
  state.newCaseVersionIds = autoConfirmedBoth.filter((row) => row.status === "confirmed").map((row) => String(row.case_version_id))
  state.newCaseIds = [...new Set(autoConfirmedBoth.filter((row) => row.status === "confirmed").map((row) => String(row.case_id)))]
  state.lowConfidenceDraftCount = Number(dbQuery(`select count(*) from case_version cv join case_entity ce on ce.id = cv.case_id join case_scenario s on s.id = ce.scenario_id where s.version_id = ${state.versionId} and cv.trigger = 'discovery_delta' and cv.status = 'draft'`))
  state.caseRevisionEvents = Number(dbQuery(`select count(*) from event where channel = 'version:${state.versionId}' and type = 'case.revision.auto_confirmed'`))
  check("autoConfirmRevision", state.newCaseVersionIds.length >= 2 && state.autoConfirmedRevisions.every((row) => row.trigger === "discovery_delta" && row.auto_confirmed_by === "system"),
    `case_versions=${state.newCaseVersionIds.join(",")} triggers=${[...new Set(state.autoConfirmedRevisions.map((row) => row.trigger))].join(",")} actors=${[...new Set(state.autoConfirmedRevisions.map((row) => row.auto_confirmed_by))].join(",")}`)
  check("caseRevisionEvent", state.caseRevisionEvents >= 1, `case.revision.auto_confirmed events=${state.caseRevisionEvents}`)

  // -- current wave must be untouched by the auto-confirmation -------------
  const wave1RunsAfterAuto = await caseRuns(state.wave1Id)
  state.wave1CaseRunIdsAfter = wave1RunsAfterAuto.map((run) => String(run.id)).sort()
  check("currentWaveUnchangedByAutoConfirm",
    (state.wave1CaseRunIdsBefore.join(",") === state.wave1CaseRunIdsAfter.join(",")),
    `before=${state.wave1CaseRunIdsBefore.join(",")} after=${state.wave1CaseRunIdsAfter.join(",")}`)

  // -- coverage restored: uncovered -> covered ------------------------------
  const coverageAfter = await waitFor("coverage decisions settle", async () => {
    const current = await coverage()
    const covered = (current.pages?.approved || 0) + (current.apis?.approved || 0)
    return covered >= 2 ? current : null
  }, 60000, 1500).catch(() => coverage())
  state.coverageAfter = coverageAfter
  state.coverageAfterCovered = (coverageAfter.pages?.approved || 0) + (coverageAfter.apis?.approved || 0)
  const uncoveredAfter = (coverageAfter.pages?.uncovered || []).length + (coverageAfter.apis?.uncovered || []).length
  check("coverageUncoveredToCovered", state.coverageBeforeUncovered >= 1 && state.coverageAfterCovered >= 1 && uncoveredAfter < state.coverageBeforeUncovered,
    `beforeUncovered=${state.coverageBeforeUncovered} afterCovered=${state.coverageAfterCovered} afterUncovered=${uncoveredAfter}`)

  // -- W2-12 in-session auto-confirm notice --------------------------------
  try {
    const notice = pageDesign.locator('[data-testid="discovery-revision-notice"]').first()
    await notice.waitFor({ state: "visible", timeout: 60000 })
    const shotConfirm = await screenshotElement(pageDesign, '[data-testid="discovery-revision-notice"]', "03-design-auto-confirm.png")
    state.screenshots.push(shotConfirm.path)
    recordStep("ui.design.autoConfirm", shotConfirm)
  } catch (err) {
    check("uiAutoConfirmNotice", false, `in-session notice not visible: ${String(err.message).slice(0, 200)}`)
    throw err
  }

  // -- after screenshot: reload the API-backed overview --------------------
  await openVersionTab(pageOverview, "overview")
  await pageOverview.waitForSelector('[data-testid="discovery-panel"]', { timeout: 60000 })
  const allButton = pageOverview.getByRole("button", { name: "\u5168\u90e8\u72b6\u6001" })
  if (await allButton.count()) { await allButton.first().click(); await sleep(800) }
  await pageOverview.locator('[data-testid="discovery-panel"]').getByText("approved").first().waitFor({ timeout: 60000 })
  await pageOverview.locator('[data-testid="coverage-panel"]').getByText("\u5df2\u6279\u51c6").first().waitFor({ timeout: 60000 }).catch(() => {})
  const shotAfter = await screenshotElement(pageOverview, '[data-testid="discovery-overview"]', "02-discovery-after.png")
  state.screenshots.push(shotAfter.path)
  recordStep("ui.discovery.after", shotAfter)

  // -- wave 2: explicit selection of the new cases only --------------------
  let started2 = await api("POST", `/api/versions/${state.versionId}/waves`, { case_ids: state.newCaseIds })
  if (started2.status !== 201 && started2.status !== 200 && started2.status === 409) {
    // The scheduler may have planned a wave on its own; attach to the active
    // wave (it contains every current+confirmed case, including the new ones).
    const waves = await listWaves()
    const active = waves.find((wave) => !["done", "failed", "cancelled"].includes(wave.status))
    if (active) started2 = { status: 200, json: { data: { wave_id: active.id } }, text: "" }
  }
  if (started2.status !== 201 && started2.status !== 200) throw new Error(`POST /waves -> ${started2.status}: ${started2.text.slice(0, 400)}`)
  state.wave2Id = String(started2.json.data.wave_id)
  const wave2Done = await waitWaveTerminal(state.wave2Id, WAVE2_TIMEOUT)
  const wave2Runs = await caseRuns(state.wave2Id)
  state.wave2CaseRuns = wave2Runs.map((run) => ({ id: String(run.id), case_id: String(run.case_id), case_version_id: String(run.case_version_id), status: run.status, title: run.title }))
  const newCaseRuns = wave2Runs.filter((run) => state.newCaseIds.includes(String(run.case_id)))
  check("nextWaveExecutedNewCase", newCaseRuns.length >= 1,
    `wave2=${state.wave2Id} runs=${state.wave2CaseRuns.map((run) => `${run.case_id}:${run.status}`).join(",")}`)
  check("nextWaveNewCasesTerminal", newCaseRuns.length >= 1 && newCaseRuns.every((run) => ["passed", "failed", "blocked", "inconclusive", "skipped", "interrupted", "superseded"].includes(run.status)) && newCaseRuns.some((run) => run.status === "passed"),
    `newRuns=${newCaseRuns.map((run) => `${run.case_id}/${run.case_version_id}:${run.status}`).join(",")}`)
  recordStep("wave2.terminal", { waveId: state.wave2Id, status: wave2Done.status, runs: state.wave2CaseRuns })

  // -- next wave frozen scope contains U and A -----------------------------
  const snapshotResp = await api("GET", `/api/versions/${state.versionId}/scope-snapshot?wave_id=${state.wave2Id}`)
  const snapshotRaw = snapshotResp.text
  saveDesign("exploration-scope-snapshot.json", snapshotResp.json)
  const containsId = (raw, id) => new RegExp("(^|[^0-9])" + String(id) + "([^0-9]|$)").test(raw)
  state.nextWaveSnapshotContainsU = containsId(snapshotRaw, state.uPageId)
  state.nextWaveSnapshotContainsA = containsId(snapshotRaw, state.aApiId)
  state.nextWaveSnapshotRaw = snapshotRaw
  check("nextWaveSnapshotContainsU", state.nextWaveSnapshotContainsU, `page=${state.uPageId} raw=${snapshotRaw.slice(0, 700)}`)
  check("nextWaveSnapshotContainsA", state.nextWaveSnapshotContainsA, `api=${state.aApiId}`)

  // -- current wave case_run IDs must still be unchanged -------------------
  const wave1RunsAfterWave2 = await caseRuns(state.wave1Id)
  state.currentWaveCaseRunIdsBefore = state.wave1CaseRunIdsBefore
  state.currentWaveCaseRunIdsAfter = wave1RunsAfterWave2.map((run) => String(run.id)).sort()
  check("currentWaveUnchangedAfterNextWave",
    state.currentWaveCaseRunIdsBefore.join(",") === state.currentWaveCaseRunIdsAfter.join(","),
    `before=${state.currentWaveCaseRunIdsBefore.join(",")} after=${state.currentWaveCaseRunIdsAfter.join(",")}`)

  // -- W2-12 rollback through the UI + W2-C11 replacement semantics --------
  const notice = pageDesign.locator('[data-testid="discovery-revision-notice"]').first()
  await notice.waitFor({ state: "visible", timeout: 30000 })
  const rollbackButton = notice.getByRole("button", { name: "\u56de\u6eda" }).first()
  await rollbackButton.waitFor({ state: "visible", timeout: 30000 })
  await rollbackButton.click()
  await pageDesign.locator('[data-testid="discovery-rollback-result"]').first().waitFor({ state: "visible", timeout: 60000 })
  const shotRollback = await screenshotElement(pageDesign, '[data-testid="discovery-revision-notice"]', "04-design-rollback.png")
  state.screenshots.push(shotRollback.path)
  recordStep("ui.design.rollback", shotRollback)

  const rollbackRows = dbRevisionRows("cv.trigger = 'rollback'")
  if (rollbackRows.length === 0) throw new Error("the UI rollback produced no replacement revision")
  const replacement = rollbackRows.find((row) => state.newCaseVersionIds.includes(String(row.rollback_of))) || rollbackRows[0]
  state.rollbackRevisionId = String(replacement.case_version_id)
  state.rollbackOfRevisionId = String(replacement.rollback_of)
  state.rollbackTrigger = replacement.trigger
  state.rollbackCaseId = String(replacement.case_id)
  const rollbackCase = dbJson(`select coalesce(json_agg(json_build_object('id', ce.id::text) order by ce.id), '[]')::text from case_entity ce where ce.id = ${replacement.case_id}`) || []
  // The API is idempotent: repeating the rollback of the original returns the
  // same replacement revision.
  const repeat = await must("POST", `/api/versions/${state.versionId}/case-versions/${state.rollbackOfRevisionId}/rollback`, { reason: "W2-13 repeated rollback (idempotency)" })
  const repeatId = String(repeat.data.case_version_id)
  check("rollbackReplacementConfirmed",
    replacement.status === "confirmed" && replacement.trigger === "rollback" && String(replacement.rollback_of) === state.rollbackOfRevisionId && rollbackCase.length === 1,
    `replacement=${state.rollbackRevisionId} original=${state.rollbackOfRevisionId} status=${replacement.status} trigger=${replacement.trigger} caseKept=${rollbackCase.length === 1}`)
  check("rollbackIdempotent", repeatId === state.rollbackRevisionId, `repeat=${repeatId} first=${state.rollbackRevisionId}`)
  recordStep("rollback.verified", { replacement: state.rollbackRevisionId, original: state.rollbackOfRevisionId, repeated: repeatId })
  // The rollback publishes one more case.revision.auto_confirmed (rollback_of
  // set); record the final event count for the verdict.
  state.caseRevisionEvents = Number(dbQuery(`select count(*) from event where channel = 'version:${state.versionId}' and type = 'case.revision.auto_confirmed'`))
  state.rollbackEventCount = Number(dbQuery(`select count(*) from event where channel = 'version:${state.versionId}' and type = 'case.revision.auto_confirmed' and envelope::text like '%"rollback_of": "${state.rollbackOfRevisionId}"%'`))

  // -- execution screenshot: next wave with the new cases ------------------
  await openVersionTab(pageOverview, "execution")
  await sleep(2000)
  const shotExecution = await screenshotPage(pageOverview, "05-execution-wave2.png")
  state.screenshots.push(shotExecution.path)
  recordStep("ui.execution.wave2", shotExecution)

  // -- cleanup: resolve the report-gate hold request -----------------------
  try {
    await must("POST", `/api/human-requests/${state.humanRequestId}/resolve`, { response: { decision: "approved", reason: "W2-13 acceptance evidence captured" } })
    state.humanRequestResolved = true
  } catch (err) { state.humanRequestResolved = false; log("human request resolve failed:", String(err.message).slice(0, 200)) }

  // -- final evidence snapshot ---------------------------------------------
  state.finalStage = await stage()
  state.wave1RunStatusesFinal = (await caseRuns(state.wave1Id)).map((run) => `${run.id}:${run.status}`)
  state.dbFacts = dbJson(`select coalesce(json_agg(json_build_object('id', id::text, 'kind', kind, 'key', key) order by id), '[]')::text from version_fact where version_id = ${state.versionId} and source = 'execution'`) || []
  state.dbCaseRuns = dbJson(`select coalesce(json_agg(json_build_object('id', r.id::text, 'wave_id', r.wave_id::text, 'case_id', cv.case_id::text, 'case_version_id', r.case_version_id::text, 'status', r.status) order by r.id), '[]')::text from case_run r join case_version cv on cv.id = r.case_version_id where r.wave_id in (${state.wave1Id}, ${state.wave2Id})`) || []
  saveJson("exploration-state.json", state)

  const verdict = {
    status: "PASS",
    replayed: false,
    currentWaveId: state.wave1Id,
    nextWaveId: state.wave2Id,
    currentWaveCaseRunIdsBefore: state.currentWaveCaseRunIdsBefore,
    currentWaveCaseRunIdsAfter: state.currentWaveCaseRunIdsAfter,
    observationPages: state.observations.filter((row) => row.kind === "page").length,
    observationApis: state.observations.filter((row) => row.kind === "api").length,
    proposedPageIds: state.proposedPageIds,
    proposedApiIds: state.proposedApiIds,
    factPageCount: state.factPageCount,
    factApiCount: state.factApiCount,
    factPageIds: state.factPageIds,
    factApiIds: state.factApiIds,
    executionFactPageCount: state.executionFactPageCount,
    executionFactApiCount: state.executionFactApiCount,
    resourceDeltaEvents: state.resourceDeltaEvents,
    nextWaveSnapshotContainsU: state.nextWaveSnapshotContainsU,
    nextWaveSnapshotContainsA: state.nextWaveSnapshotContainsA,
    newCaseVersionIds: state.newCaseVersionIds,
    caseRevisionEvents: state.caseRevisionEvents,
    coverageBeforeUncovered: state.coverageBeforeUncovered,
    coverageAfterCovered: state.coverageAfterCovered,
    lowConfidenceDraftCount: state.lowConfidenceDraftCount,
    rollbackRevisionId: state.rollbackRevisionId,
    screenshots: state.screenshots,
    apiBase: BASE,
    webUrl: WEB,
    executorUrl: EXECUTOR_URL,
    // Extended evidence (explicitly allowed; the contract fields above are the
    // frozen required subset).
    versionId: state.versionId,
    environmentId: state.environmentId,
    sutId: state.sutId,
    requirementId: state.requirementId,
    homePageId: state.homePageId,
    c1CaseId: state.c1CaseId,
    c1CaseVersionId: state.c1CaseVersionId,
    article: state.article,
    uPageId: state.uPageId,
    uPageObservationId: state.uPageObservationId,
    uPageFactId: state.uPageFactId,
    aApiId: state.aApiId,
    aApiObservationId: state.aApiObservationId,
    aApiFactId: state.aApiFactId,
    observationPageRows: state.observations.filter((row) => row.kind === "page").map((row) => ({ observation_id: row.observation_id, resource_template: row.resource_template, decision: row.decision, resource_id: row.resource_id, fact_id: row.fact_id, source_kind: row.source_kind })),
    observationApiRows: state.observations.filter((row) => row.kind === "api").map((row) => ({ observation_id: row.observation_id, method: row.method, resource_template: row.resource_template, decision: row.decision, resource_id: row.resource_id, fact_id: row.fact_id, source_kind: row.source_kind })),
    discoveryDeltaCounts: state.deltas?.counts || null,
    autoConfirmedRevisions: state.autoConfirmedRevisions,
    newCaseIds: state.newCaseIds,
    wave2CaseRuns: state.wave2CaseRuns,
    coverageBefore: state.coverageBefore,
    coverageAfter: state.coverageAfter,
    rollbackOfRevisionId: state.rollbackOfRevisionId,
    rollbackTrigger: state.rollbackTrigger,
    rollbackCaseId: state.rollbackCaseId,
    rollbackCaseKept: true,
    humanRequestId: state.humanRequestId,
    checks: state.checks,
    red: state.red,
    steps,
    generatedAt: nowIso(),
  }
  if (state.red.length > 0) verdict.status = "FAIL"
  saveJson("exploration-verdict.json", verdict)
  log(`W2-13 EXPLORATION E2E COMPLETE: status=${verdict.status} checks=${Object.keys(state.checks).length} red=${state.red.length}`)
  return state.red.length === 0
}

async function shutdown(code) {
  try { if (browser) await browser.close() } catch { }
  await sleep(300)
  process.exit(code)
}
main().then((ok) => {
  shutdown(ok ? 0 : 1)
}).catch((err) => {
  log("W2-13 EXPLORATION FLOW FAILED:", err.stack || err.message)
  state.error = String(err.stack || err.message)
  // Always emit a FAIL verdict so the independent PS gate has a machine
  // readable result even when the flow throws mid-way.
  const failed = {
    status: "FAIL",
    replayed: false,
    currentWaveId: state.wave1Id || null,
    nextWaveId: state.wave2Id || null,
    currentWaveCaseRunIdsBefore: state.currentWaveCaseRunIdsBefore || [],
    currentWaveCaseRunIdsAfter: state.currentWaveCaseRunIdsAfter || [],
    observationPages: (state.observations || []).filter((row) => row.kind === "page").length,
    observationApis: (state.observations || []).filter((row) => row.kind === "api").length,
    proposedPageIds: state.proposedPageIds || [],
    proposedApiIds: state.proposedApiIds || [],
    factPageCount: state.factPageCount || 0,
    factApiCount: state.factApiCount || 0,
    resourceDeltaEvents: state.resourceDeltaEvents || 0,
    nextWaveSnapshotContainsU: !!state.nextWaveSnapshotContainsU,
    nextWaveSnapshotContainsA: !!state.nextWaveSnapshotContainsA,
    newCaseVersionIds: state.newCaseVersionIds || [],
    caseRevisionEvents: state.caseRevisionEvents || 0,
    coverageBeforeUncovered: state.coverageBeforeUncovered || 0,
    coverageAfterCovered: state.coverageAfterCovered || 0,
    lowConfidenceDraftCount: state.lowConfidenceDraftCount || 0,
    rollbackRevisionId: state.rollbackRevisionId || null,
    screenshots: state.screenshots || [],
    apiBase: BASE,
    webUrl: WEB,
    executorUrl: EXECUTOR_URL,
    error: state.error,
    checks: state.checks,
    red: state.red.concat([state.error]),
    generatedAt: nowIso(),
  }
  saveJson("exploration-verdict.json", failed)
  shutdown(1)
})
'@ | Set-Content -Encoding ASCII $flowPath

        $env:W213_API = $apiBase
        $env:W213_WEB = $webUrl
        $env:W213_EXECUTOR = $executorUrl
        $env:W213_EVIDENCE = $evidenceRoot
        $env:W213_RUN = $runEvidence
        $env:W213_SHOTS = $shotDir
        $env:W213_REPO_ROOT = $repoRoot
        $env:W213_PSQL = $psql
        $env:W213_DB = $dbName
        $env:W213_DB_PASSWORD = $dbPassword
        $env:W213_ADMIN_PW = $adminPassword
        $env:W213_DESIGN_TIMEOUT = "$DesignTimeoutSeconds"
        $env:W213_WAVE_TIMEOUT = "$WaveTimeoutSeconds"
        $env:W213_WAVE2_TIMEOUT = "$Wave2TimeoutSeconds"
        $env:W213_AUTO_TIMEOUT = "$AutoConfirmTimeoutSeconds"
        $env:W213_UI_TIMEOUT = "$UiTimeoutSeconds"

        Push-Location $work
        try {
            $previous = $ErrorActionPreference
            $ErrorActionPreference = 'Continue'
            & $nodeExe $flowPath *> $flowLog
            $flowExit = $LASTEXITCODE
            $ErrorActionPreference = $previous
        } finally { Pop-Location }
        Write-Host "== exploration flow exit=$flowExit"
        if (Test-Path $flowLog) { Get-Content $flowLog -Tail 50 | Write-Host }

        if ($flowExit -eq 2) { Exit-ExternalBlocked 'the real Playwright browser is unavailable' }

        # -------------------------------------------------------------------
        # 5. Independent verdict phase.
        # -------------------------------------------------------------------
        Write-Host ''
        Write-Host '== W2-13 verdict phase (verdict contract + independent DB/API assertions)'
        Assert-ExplorationVerdict

        $verdict = Get-Content $verdictPath -Raw -Encoding UTF8 | ConvertFrom-Json
        $red = New-Object System.Collections.Generic.List[string]
        $assertions = [ordered]@{}
        function Add-Assertion([string]$name, [bool]$ok, [string]$detail) {
            $script:assertions[$name] = $ok
            if (-not $ok) { $script:red.Add("${name}: $detail") }
            $status = if ($ok) { 'PASS' } else { 'RED ' }
            Write-Host ("   [{0}] {1}: {2}" -f $status, $name, $detail)
        }
        function Db([string]$sql) {
            $output = ''
            $code = 1
            $previous = $ErrorActionPreference
            $ErrorActionPreference = 'Continue'
            try {
                $output = (& $psql -h localhost -p 5433 -U eztest -d $dbName -tAc $sql 2>&1 | Out-String).Trim()
                $code = $LASTEXITCODE
            } finally {
                $ErrorActionPreference = $previous
            }
            if ($code -ne 0) { throw "psql failed (exit=$code): $($output.Substring(0, [Math]::Min(300, $output.Length)))" }
            return $output
        }

        if ($verdict.status -ne 'PASS') { $red.Add("verdict.status=$($verdict.status)") }
        if ($verdict.replayed -ne $false) { $red.Add("verdict.replayed=$($verdict.replayed)") }
        $before = @($verdict.currentWaveCaseRunIdsBefore | ForEach-Object { [string]$_ })
        $after = @($verdict.currentWaveCaseRunIdsAfter | ForEach-Object { [string]$_ })
        Add-Assertion 'currentWaveCaseRunsUnchanged' (($before -join ',') -eq ($after -join ',')) "before=$($before -join ',') after=$($after -join ',')"
        Add-Assertion 'nextWaveSnapshotContainsU' ([bool]$verdict.nextWaveSnapshotContainsU) "uPageId=$($verdict.uPageId)"
        Add-Assertion 'nextWaveSnapshotContainsA' ([bool]$verdict.nextWaveSnapshotContainsA) "aApiId=$($verdict.aApiId)"
        Add-Assertion 'coverageBeforeUncovered' ([int]$verdict.coverageBeforeUncovered -ge 1) "coverageBeforeUncovered=$($verdict.coverageBeforeUncovered)"
        Add-Assertion 'coverageAfterCovered' ([int]$verdict.coverageAfterCovered -ge 1) "coverageAfterCovered=$($verdict.coverageAfterCovered)"
        Add-Assertion 'observationPages' ([int]$verdict.observationPages -ge 1) "observationPages=$($verdict.observationPages)"
        Add-Assertion 'observationApis' ([int]$verdict.observationApis -ge 1) "observationApis=$($verdict.observationApis)"
        Add-Assertion 'proposedPageIds' (@($verdict.proposedPageIds | Where-Object { $_ }).Count -ge 1) "proposedPageIds=$(@($verdict.proposedPageIds) -join ',')"
        Add-Assertion 'proposedApiIds' (@($verdict.proposedApiIds | Where-Object { $_ }).Count -ge 1) "proposedApiIds=$(@($verdict.proposedApiIds) -join ',')"
        Add-Assertion 'factPageCount' ([int]$verdict.factPageCount -ge 1) "factPageCount=$($verdict.factPageCount)"
        Add-Assertion 'factApiCount' ([int]$verdict.factApiCount -ge 1) "factApiCount=$($verdict.factApiCount)"
        Add-Assertion 'resourceDeltaEvents' ([int]$verdict.resourceDeltaEvents -ge 1) "resourceDeltaEvents=$($verdict.resourceDeltaEvents)"
        Add-Assertion 'caseRevisionEvents' ([int]$verdict.caseRevisionEvents -ge 1) "caseRevisionEvents=$($verdict.caseRevisionEvents)"
        Add-Assertion 'newCaseVersionIds' (@($verdict.newCaseVersionIds | Where-Object { $_ }).Count -ge 1) "newCaseVersionIds=$(@($verdict.newCaseVersionIds) -join ',')"
        Add-Assertion 'rollbackRevisionId' (-not [string]::IsNullOrWhiteSpace([string]$verdict.rollbackRevisionId)) "rollbackRevisionId=$($verdict.rollbackRevisionId)"
        $shots = @($verdict.screenshots | Where-Object { $_ })
        $shotsOk = $shots.Count -ge 3
        foreach ($shot in $shots) {
            $shotPath = Join-Path $runEvidence ([string]$shot)
            if (-not (Test-Path $shotPath) -or (Get-Item $shotPath).Length -le 0) { $shotsOk = $false }
        }
        Add-Assertion 'screenshots' $shotsOk "screenshots=$($shots -join ',')"

        # Independent DB evidence: the two discovery_delta revisions, the
        # resource.delta and case.revision.auto_confirmed events, and the
        # rollback replacement row.
        try {
            $autoRows = @(Parse-JsonArray (Db "select coalesce(json_agg(json_build_object('case_version_id', cv.id::text, 'case_id', cv.case_id::text, 'status', cv.status, 'trigger', cv.trigger, 'auto_confirmed_by', cv.auto_confirmed_by, 'source_observation_id', cv.source_observation_id::text) order by cv.id), '[]')::text from case_version cv join case_entity ce on ce.id = cv.case_id join case_scenario s on s.id = ce.scenario_id where s.version_id = $($verdict.versionId) and cv.trigger = 'discovery_delta'"))
            $autoSystem = @($autoRows | Where-Object { $_.auto_confirmed_by -eq 'system' })
            $autoConfirmedCount = @($autoSystem | Where-Object { $_.status -eq 'confirmed' }).Count
            $autoSupersededCount = @($autoSystem | Where-Object { $_.status -eq 'superseded' }).Count
            Add-Assertion 'dbAutoConfirmedRevisions' ($autoSystem.Count -ge 2 -and $autoConfirmedCount -ge 1 -and $autoSupersededCount -ge 1) "rows=$($autoRows.Count) system=$($autoSystem.Count) confirmed=$autoConfirmedCount superseded=$autoSupersededCount (the rolled-back original stays as superseded audit history)"
            $rollbackRows = @(Parse-JsonArray (Db "select coalesce(json_agg(json_build_object('case_version_id', cv.id, 'case_id', cv.case_id, 'revision', cv.revision, 'status', cv.status, 'trigger', cv.trigger, 'rollback_of', cv.rollback_of) order by cv.id), '[]')::text from case_version cv join case_entity ce on ce.id = cv.case_id join case_scenario s on s.id = ce.scenario_id where s.version_id = $($verdict.versionId) and cv.trigger = 'rollback'"))
            Add-Assertion 'dbRollbackReplacement' ($rollbackRows.Count -ge 1 -and $rollbackRows[0].status -eq 'confirmed' -and $rollbackRows[0].rollback_of -ne $null) "rollbackRows=$($rollbackRows.Count) first=$($rollbackRows[0].case_version_id)/$($rollbackRows[0].status)/rollback_of=$($rollbackRows[0].rollback_of)"
            $rollbackCaseKept = [int](Db "select count(*) from case_entity where id = $($verdict.rollbackCaseId)")
            Add-Assertion 'dbRollbackCaseKept' ($rollbackCaseKept -eq 1) "case=$($verdict.rollbackCaseId) rows=$rollbackCaseKept"
            $deltaEvents = [int](Db "select count(*) from event where channel = 'version:$($verdict.versionId)' and type = 'resource.delta'")
            $revisionEvents = [int](Db "select count(*) from event where channel = 'version:$($verdict.versionId)' and type = 'case.revision.auto_confirmed'")
            Add-Assertion 'dbEvents' ($deltaEvents -ge 1 -and $revisionEvents -ge 1) "resource.delta=$deltaEvents case.revision.auto_confirmed=$revisionEvents"
        } catch {
            $red.Add("db evidence query failed: $($_.Exception.Message)")
        }

        if ($red.Count -gt 0) {
            Write-Host ''
            Write-Host '== W2-13 RED:'
            foreach ($entry in $red) { Write-Host "   - $entry" }
            throw "W2-13 exploration acceptance failed with $($red.Count) red item(s)"
        }
        Write-Host '== W2-13 exploration acceptance: PASS'
        $exitCode = 0
    }
} catch {
    $message = $_.Exception.Message
    if ($script:externalBlocked) {
        Write-Host "== EXTERNAL-BLOCKED: $($script:blockedReason)"
        $exitCode = 2
    } else {
        Write-Host "== W2-13 EXPLORATION FAILED: $message"
        $exitCode = 1
    }
} finally {
    if ($script:stackStarted) {
        # Restore execution.executor_url through the product API before the API
        # is stopped. A failed restore is reported but never masks the result.
        if (Test-Path $originalExecutorUrlPath) {
            try {
                $originalExecutorUrl = (Get-Content $originalExecutorUrlPath -Raw -Encoding ASCII).Trim()
                if ($originalExecutorUrl) {
                    $login = Invoke-RestMethod -Uri "$apiBase/api/auth/login" -Method Post -ContentType 'application/json' -Body (@{ username = 'admin'; password = (Get-EnvValue 'EZTEST_ADMIN_PASSWORD') } | ConvertTo-Json) -TimeoutSec 10
                    $headers = @{ authorization = "Bearer $($login.data.token)" }
                    $null = Invoke-RestMethod -Uri "$apiBase/api/admin/settings/execution.executor_url" -Method Patch -Headers $headers -ContentType 'application/json' -Body (@{ value = $originalExecutorUrl } | ConvertTo-Json) -TimeoutSec 10
                    Write-Host "== restored execution.executor_url=$originalExecutorUrl"
                }
            } catch {
                Write-Warning "could not restore execution.executor_url: $($_.Exception.Message)"
            }
        }
        if (-not $KeepStack) {
            Stop-E2EPorts
            Write-Host '== stopped W2-13 API/executor/web/vision-proxy (SUT left running)'
        } else {
            Write-Host '== -KeepStack: left W2-13 services running'
        }
    }
}
exit $exitCode
