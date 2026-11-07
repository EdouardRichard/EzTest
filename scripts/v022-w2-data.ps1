#requires -Version 5.1
<#
.SYNOPSIS
    EzTest v0.22 W2 base-data e2e (Task W2-1).

.DESCRIPTION
    Boots a disposable composition root (fresh PostgreSQL database eztest_v022_w2,
    the real cmd/eztest binary, the real executor on a W2-only port, the real
    Next.js frontend, the real DeepSeek upstream behind the deterministic
    vision-caption proxy and the real SUT at http://localhost:5173), creates the
    reusable W2 base data through the product API, runs the real design
    Host/LLM, executes one wave with at least one passed and one failed
    case_run, waits for the P4 reconciler to auto-create a delivery_issue from
    the failed run, and writes run/data-verdict.json with every ID the later W2
    domain scripts need.

    The version deliberately stays in the executing stage: the impossible
    requirement can never satisfy requirement closure, so the executing ->
    reporting report gate keeps rejecting and the version remains reusable for
    more W2 waves. A published wave report is recorded when the P4 report
    writer produces one; the version report is left to W2-7/W2-8 and reportId
    stays null if no report exists yet.

    Ports: API 13445, executor 13349 (W2-only: sharing another root's executor
    lets that root's orphan reconciliation delete live contexts), vision proxy
    13999, web 6871. Database: eztest_v022_w2 on PostgreSQL 5433 (reset on
    every fresh run). The SUT (localhost:8080 / localhost:5173) is reused and
    left running; every process this script starts is stopped at the end.

    -VerdictOnly is the RED-phase gate: it only evaluates the verdict contract
    and exits non-zero when run/data-verdict.json is missing or incomplete.

    Evidence is written under docs/version-V0.22/acceptance/evidence/w2/run/
    (untracked).
#>
param(
    [switch]$SkipBuild,
    [switch]$VerdictOnly,
    [int]$DesignTimeoutSeconds = 900,
    [int]$ExecutionTimeoutSeconds = 1200,
    [int]$IssueTimeoutSeconds = 300,
    [int]$ReportTimeoutSeconds = 240
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

$label = 'W2 base data e2e'
$work = Join-Path $repoRoot '.tmp\w2-e2e'
$evidenceRoot = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence\w2'
$runEvidence = Join-Path $evidenceRoot 'run'
$fixtures = Join-Path $work 'fixtures'
$dbName = 'eztest_v022_w2'
$apiPort = 13445
$webPort = 6871
$execPort = 13349
$proxyPort = 13999
$proxyBase = "http://127.0.0.1:$proxyPort"
$apiBase = "http://127.0.0.1:$apiPort"
$webUrl = "http://127.0.0.1:$webPort"
$executorUrl = "http://127.0.0.1:$execPort"
$sutWeb = 'http://localhost:5173'
$sutApi = 'http://localhost:8080/api/v1/posts'
New-Item -ItemType Directory -Force -Path $work, $runEvidence, $fixtures | Out-Null
$verdictPath = Join-Path $runEvidence 'data-verdict.json'
$baseDataPath = Join-Path $runEvidence 'base-data.json'
$flowResultPath = Join-Path $runEvidence 'result.json'
$flowExit = 0
$exitCode = 0
$stackStarted = $false
$externalBlocked = $false
$blockedReason = ''
$sutFrontendStarted = $false

# UTF-8 without BOM: evidence carries CJK titles, and the Windows PowerShell
# 5.1 text cmdlets would rewrite them in the active ANSI code page.
function Write-Utf8([string]$path, [string]$text) {
    [System.IO.File]::WriteAllText($path, $text, (New-Object System.Text.UTF8Encoding($false)))
}

function Get-EnvValue([string]$key) {
    $line = Select-String -Path (Join-Path $repoRoot '.env') -Pattern ("^" + [regex]::Escape($key) + "=(.*)$")
    if (-not $line) { throw "missing $key in .env" }
    return $line.Matches[0].Groups[1].Value
}

# Secrets that must never live in the script or its logs. A missing value is an
# external blocker, not a reason to fall back to a default.
function Get-RequiredEnvValue([string]$key) {
    $value = $null
    try { $value = Get-EnvValue $key } catch { }
    if ([string]::IsNullOrWhiteSpace($value)) {
        Exit-ExternalBlocked "$key is missing from .env (required; never defaulted)"
    }
    return $value
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

# Every listener this script owns is stopped at the end; the SUT (8080/5173)
# and PostgreSQL (5433) are never touched.
function Stop-E2EPorts {
    foreach ($port in @($apiPort, $execPort, $webPort, $proxyPort)) {
        $conns = Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue
        foreach ($conn in $conns) {
            Stop-Process -Id $conn.OwningProcess -Force -ErrorAction SilentlyContinue
        }
    }
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
    Write-Utf8 (Join-Path $runEvidence "blocked-$stamp.json") ($blocked | ConvertTo-Json -Depth 4)
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

# The SUT is shared with the other acceptance runs and can be restarting when
# this run starts; a short bounded retry distinguishes a transient restart from
# a genuinely missing dependency.
function Wait-Http200([string]$url, [int]$timeoutSeconds) {
    $deadline = (Get-Date).AddSeconds($timeoutSeconds)
    do {
        if (Test-Http200 $url) { return $true }
        Start-Sleep -Seconds 2
    } while ((Get-Date) -lt $deadline)
    return (Test-Http200 $url)
}

# /healthz of the executor answers 200; the SUT web must answer 200 on '/'.
function Test-ExecutorHealth([string]$url) {
    try { $null = Invoke-RestMethod -Uri $url -TimeoutSec 5; return $true } catch { return $false }
}

# ---------------------------------------------------------------------------
# RED-phase gate: the verdict contract from the W2-1 brief. This block is the
# acceptance test of the base-data run; it must fail before the flow exists.
# ---------------------------------------------------------------------------
function Assert-DataVerdict {
    if (-not (Test-Path $verdictPath)) { throw 'missing data verdict' }
    $v = Get-Content $verdictPath -Raw -Encoding UTF8 | ConvertFrom-Json
    if ($v.versionId -eq $null -or @($v.caseRunIds.passed).Count -lt 1 -or @($v.caseRunIds.failed).Count -lt 1) { throw 'base data incomplete' }
    if ($v.status -ne 'PASS') { throw "data verdict status=$($v.status)" }
    if ($v.stage -ne 'executing') { throw "version stage=$($v.stage), want executing" }
}

try {
    if ($VerdictOnly) {
        Assert-DataVerdict
        Write-Host '== W2 base-data verdict contract: PASS'
        $exitCode = 0
    } else {
        # -------------------------------------------------------------------
        # 0. Credentials / hard dependencies / real upstream preflight.
        # -------------------------------------------------------------------
        $dbUrl = Get-EnvValue 'EZTEST_DATABASE_URL'
        $dbPassword = [regex]::Match($dbUrl, 'postgres://[^:]+:([^@]+)@').Groups[1].Value
        $execToken = Get-EnvValue 'EZTEST_EXECUTOR_TOKEN'
        $adminPassword = Get-EnvValue 'EZTEST_ADMIN_PASSWORD'
        $sutPassword = Get-EnvValue 'SUT_PASSWORD'
        # The SUT database credentials are local-only secrets (repo .env is
        # gitignored); the script must never carry the DSN or password literal.
        $sutDbDsn = Get-RequiredEnvValue 'SUT_DB_DSN'
        $sutDbPassword = Get-RequiredEnvValue 'SUT_DB_PASSWORD'
        $llmKey = Get-EnvValue 'llm_api_key'
        $llmBase = Get-EnvValue 'llm_base_url'
        $llmModel = Get-EnvValue 'llm_model_name'
        $llmBase = $llmBase.TrimEnd('/')
        $env:PGPASSWORD = $dbPassword
        $psql = Join-Path $repoRoot '.tools\pgsql\bin\psql.exe'
        $goExe = Join-Path $repoRoot '.tools\go\bin\go.exe'
        try {
            $nodeExe = (Get-Command node).Source
        } catch {
            Exit-ExternalBlocked 'node is not on PATH'
        }
        if (-not (Test-Path -LiteralPath $psql)) { Exit-ExternalBlocked "PostgreSQL client is missing: $psql" }
        if (-not (Test-Path -LiteralPath $goExe)) { Exit-ExternalBlocked "Go toolchain is missing: $goExe" }
        $previous = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        & $psql -h localhost -p 5433 -U eztest -d postgres -tAc 'select 1' 2>&1 | Out-Null
        $psqlOk = ($LASTEXITCODE -eq 0)
        $ErrorActionPreference = $previous
        if (-not $psqlOk) { Exit-ExternalBlocked 'PostgreSQL 5433 is unreachable (psql select 1 failed)' }

        if (-not (Test-Path -LiteralPath 'D:\project\blog-frontend')) { Exit-ExternalBlocked 'SUT frontend checkout D:\project\blog-frontend is missing' }
        if (-not (Test-Path -LiteralPath 'D:\project\blog-backend')) { Exit-ExternalBlocked 'SUT backend checkout D:\project\blog-backend is missing' }
        $sutApiCode = Get-HttpCode $sutApi
        if ($sutApiCode -ne '200' -and -not (Wait-Http200 $sutApi 30)) {
            Exit-ExternalBlocked "SUT backend $sutApi is not reachable (last HTTP '$sutApiCode', HTTP 200 required)"
        }
        if (-not (Test-Http200 $sutWeb)) {
            Write-Host "== SUT frontend $sutWeb is down; starting 'npm run dev' in D:\project\blog-frontend"
            $null = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{
                CommandLine      = 'cmd.exe /c start "w2-sut-frontend" /min npm run dev'
                CurrentDirectory = 'D:\project\blog-frontend'
            }
            $frontendReady = $false
            for ($i = 0; $i -lt 180; $i++) {
                if (Test-Http200 $sutWeb) { $frontendReady = $true; break }
                Start-Sleep -Seconds 1
            }
            if (-not $frontendReady) { Exit-ExternalBlocked "SUT frontend $sutWeb did not become ready after 'npm run dev'" }
            $script:sutFrontendStarted = $true
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

        # The run directory is the evidence of the latest execution only; the
        # preflight above does not write a verdict on success, so wiping here
        # cannot destroy a blocked marker from a failed preflight.
        Get-ChildItem -Path $runEvidence -Force -ErrorAction SilentlyContinue | Remove-Item -Recurse -Force -ErrorAction SilentlyContinue
        Get-ChildItem -Path $work -Force -ErrorAction SilentlyContinue | Remove-Item -Recurse -Force -ErrorAction SilentlyContinue
        New-Item -ItemType Directory -Force -Path $work, $runEvidence, $fixtures | Out-Null
        Write-Host "== ${label}: repo=$repoRoot"
        Write-Host "== evidence=$runEvidence"

        # -------------------------------------------------------------------
        # 1. Fresh database, dedicated ports, clean storage.
        # -------------------------------------------------------------------
        Stop-E2EPorts
        Start-Sleep -Milliseconds 600
        $previous = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        & $psql -h localhost -p 5433 -U eztest -d postgres -c "DROP DATABASE IF EXISTS $dbName" 2>&1 | Out-Null
        & $psql -h localhost -p 5433 -U eztest -d postgres -c "CREATE DATABASE $dbName" 2>&1 | Out-Null
        $createExit = $LASTEXITCODE
        $ErrorActionPreference = $previous
        if ($createExit -ne 0) { Exit-ExternalBlocked "could not create database $dbName on PostgreSQL 5433" }
        $storage = Join-Path $work 'storage'
        Remove-Item $storage -Recurse -Force -ErrorAction SilentlyContinue
        New-Item -ItemType Directory -Force -Path $storage | Out-Null
        Write-Host "== db $dbName created"

        # -------------------------------------------------------------------
        # 2. Build the real composition root.
        # -------------------------------------------------------------------
        $backendExe = Join-Path $work 'eztest.exe'
        if (-not $SkipBuild) {
            Push-Location (Join-Path $repoRoot 'backend')
            try {
                & $goExe build -o $backendExe ./cmd/eztest 2>&1 | Tee-Object -FilePath (Join-Path $runEvidence 'build.log')
                if ($LASTEXITCODE -ne 0) { throw 'go build ./cmd/eztest failed' }
            } finally { Pop-Location }
            Write-Host '== backend built'
        }
        if (-not (Test-Path -LiteralPath $backendExe)) { throw "backend binary missing: $backendExe (use the full run, not -SkipBuild)" }

        # -------------------------------------------------------------------
        # 3. Service wrappers (W2 namespace: DB/ports/log dir disjoint from W0/W1).
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
set EZTEST_SNOWFLAKE_NODE_ID=7
"$backendExe" >> "$work\api.log" 2>&1
"@ | Set-Content -Encoding ASCII $runApi

        $runExecutor = Join-Path $work 'run-executor.cmd'
        @"
@echo off
cd /d "$repoRoot\executor"
set EZTEST_EXECUTOR_TOKEN=$execToken
set EZTEST_EXECUTOR_ADDR=127.0.0.1:$execPort
set EZTEST_API_URL=http://127.0.0.1:$apiPort
set EZTEST_EXECUTOR_NAME=w2-e2e
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
"$nodeExe" "$work\fake-vision-llm.mjs" >> "$work\vision-proxy.log" 2>&1
"@ | Set-Content -Encoding ASCII $runProxy

        $runWeb = Join-Path $work 'run-web.cmd'
        @"
@echo off
cd /d "$repoRoot\frontend"
set NEXT_PUBLIC_API_BASE_URL=http://127.0.0.1:$apiPort
"$nodeExe" "$repoRoot\frontend\node_modules\next\dist\bin\next" dev -p $webPort >> "$work\web.log" 2>&1
"@ | Set-Content -Encoding ASCII $runWeb

        $restartScript = Join-Path $work 'restart-api.ps1'
        @"
`$ErrorActionPreference = 'Stop'
`$conns = Get-NetTCPConnection -State Listen -LocalPort $apiPort -ErrorAction SilentlyContinue
foreach (`$conn in `$conns) { Stop-Process -Id `$conn.OwningProcess -Force -ErrorAction SilentlyContinue }
Start-Sleep -Milliseconds 500
`$p = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{
  CommandLine = 'cmd.exe /c ""$runApi""'
  CurrentDirectory = '$work'
}
for (`$i = 0; `$i -lt 90; `$i++) {
  try { `$r = Invoke-RestMethod 'http://127.0.0.1:$apiPort/api/health' -TimeoutSec 2; Write-Host 'api restarted'; exit 0 } catch { Start-Sleep -Seconds 1 }
}
Write-Error 'api did not restart'
exit 1
"@ | Set-Content -Encoding UTF8 $restartScript

        # Vision caption helper (C5): text requests are forwarded verbatim to the
        # real DeepSeek endpoint; image_url requests get a deterministic caption.
        @'
import fs from "node:fs"
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
function caption() {
  return "A web application screenshot showing a navigation bar, a content list and action controls."
}
const server = http.createServer(async (req, res) => {
  const url = new URL(req.url || "/", "http://127.0.0.1")
  if (req.method === "GET" && (url.pathname === "/healthz" || url.pathname === "/")) {
    return sendJson(res, 200, { status: "ok", service: "vision-caption-proxy" })
  }
  if (req.method === "GET" && url.pathname.endsWith("/models")) {
    return sendJson(res, 200, { object: "list", data: [{ id: model, object: "model", owned_by: "deepseek" }] })
  }
  if (req.method === "POST" && url.pathname.endsWith("/chat/completions")) {
    const chunks = []
    let size = 0
    for await (const chunk of req) {
      size += chunk.length
      if (size > MAX) { res.writeHead(413); return res.end("too large") }
      chunks.push(chunk)
    }
    const raw = Buffer.concat(chunks)
    let body = null
    try { body = JSON.parse(raw.toString("utf8")) } catch { body = null }
    if (hasImage(body)) {
      return sendJson(res, 200, {
        id: "chatcmpl-caption",
        object: "chat.completion",
        created: Math.floor(Date.now() / 1000),
        model,
        choices: [{ index: 0, message: { role: "assistant", content: caption() }, finish_reason: "stop" }],
        usage: { prompt_tokens: 1200, completion_tokens: 30, total_tokens: 1230 },
      })
    }
    const headers = { "content-type": "application/json" }
    if (apiKey) headers.authorization = `Bearer ${apiKey}`
    try {
      const upstreamRes = await fetch(`${upstream}${url.pathname}${url.search}`, { method: "POST", headers, body: raw })
      const buf = Buffer.from(await upstreamRes.arrayBuffer())
      res.writeHead(upstreamRes.status, { "content-type": upstreamRes.headers.get("content-type") || "application/json", "content-length": buf.length })
      res.end(buf)
    } catch (err) {
      sendJson(res, 502, { error: { message: `upstream failed: ${err.message}` } })
    }
    return
  }
  sendJson(res, 404, { error: { message: `unknown path ${url.pathname}` } })
})
server.listen(port, "127.0.0.1", () => console.log(`[vision-caption-proxy] 127.0.0.1:${port} upstream=${upstream}`))
'@ | Set-Content -Encoding UTF8 (Join-Path $work 'fake-vision-llm.mjs')

        function Start-DetachedConsole([string]$name, [string]$commandFile, [string]$workDir) {
            $null = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{
                CommandLine      = "cmd.exe /c start `"$name`" /min `"$commandFile`""
                CurrentDirectory = $workDir
            }
        }

        Remove-Item (Join-Path $work 'api.log') -Force -ErrorAction SilentlyContinue
        Remove-Item (Join-Path $work 'executor.log') -Force -ErrorAction SilentlyContinue
        Remove-Item (Join-Path $work 'web.log') -Force -ErrorAction SilentlyContinue
        Remove-Item (Join-Path $work 'vision-proxy.log') -Force -ErrorAction SilentlyContinue
        Start-DetachedConsole 'w2-vision-proxy' $runProxy $work
        Start-DetachedConsole 'w2-executor' $runExecutor (Join-Path $repoRoot 'executor')
        Start-DetachedConsole 'w2-api' $runApi (Join-Path $repoRoot 'backend')
        Start-DetachedConsole 'w2-web' $runWeb (Join-Path $repoRoot 'frontend')
        $script:stackStarted = $true

        $apiReady = $false
        for ($attempt = 0; $attempt -lt 2 -and -not $apiReady; $attempt++) {
            if ($attempt -gt 0) {
                Write-Host '== api did not become ready; restarting it once'
                & powershell.exe -NoProfile -ExecutionPolicy Bypass -File $restartScript | Out-Null
            }
            for ($i = 0; $i -lt 120; $i++) {
                try {
                    $health = Invoke-RestMethod "http://127.0.0.1:$apiPort/api/health" -TimeoutSec 2
                    $apiReady = $true
                    Write-Host "== api ready version=$($health.data.version) db=$($health.data.db)"
                    break
                } catch { Start-Sleep -Seconds 1 }
            }
        }
        if (-not $apiReady) {
            Get-Content (Join-Path $work 'api.log') -Tail 40 -ErrorAction SilentlyContinue
            throw 'eztest API did not become ready'
        }
        $execReady = $false
        for ($i = 0; $i -lt 60; $i++) {
            if (Test-ExecutorHealth "http://127.0.0.1:$execPort/healthz") { $execReady = $true; break }
            Start-Sleep -Seconds 1
        }
        if (-not $execReady) { Exit-ExternalBlocked "the real executor did not become ready on 127.0.0.1:$execPort" }
        $webReady = $false
        for ($i = 0; $i -lt 180; $i++) {
            try { $status = (Invoke-WebRequest -Uri "http://127.0.0.1:$webPort/login" -UseBasicParsing -TimeoutSec 30).StatusCode; if ($status -eq 200) { $webReady = $true; break } } catch { Start-Sleep -Seconds 1 }
        }
        if (-not $webReady) { throw 'frontend did not become ready' }
        $proxyReady = $false
        for ($i = 0; $i -lt 60; $i++) {
            try {
                $proxyHealth = Invoke-RestMethod "http://127.0.0.1:$proxyPort/healthz" -TimeoutSec 2
                if ($proxyHealth.status -eq 'ok') { $proxyReady = $true; break }
            } catch { }
            Start-Sleep -Milliseconds 500
        }
        if (-not $proxyReady) { Exit-ExternalBlocked 'the vision-caption proxy did not become ready on 127.0.0.1:13999' }
        Write-Host "== stack ready (api=$apiPort executor=$execPort web=$webPort proxy=$proxyPort)"

        # -------------------------------------------------------------------
        # 4. Embedded Node base-data flow.
        # -------------------------------------------------------------------
        $flowPath = Join-Path $work 'w2-base-flow.mjs'
        @'
// EzTest v0.22 W2 base-data e2e flow (W2-1).
//
// Composition root: fresh PostgreSQL database, real cmd/eztest, real executor,
// real Next.js frontend, the real DeepSeek upstream behind the deterministic
// vision-caption proxy and the real SUT at http://localhost:5173. The flow
// creates two requirements through the real API: one satisfiable home-list
// requirement (its case passes) and one impossible title requirement (its case
// deterministically fails). The version stays executing so later W2 tasks can
// start more waves.
import fs from "node:fs"
import path from "node:path"
import { spawnSync } from "node:child_process"
import { createRequire } from "node:module"

const BASE = process.env.W2_BASE
const WEB = process.env.W2_WEB
const EVIDENCE = process.env.W2_EVIDENCE
const REPO_ROOT = process.env.W2_REPO_ROOT
const PSQL = process.env.W2_PSQL
const DB_NAME = process.env.W2_DB
const DB_PASSWORD = process.env.W2_DB_PASSWORD
const ADMIN_PASSWORD = process.env.W2_ADMIN_PW
const SUT_PASSWORD = process.env.W2_SUT_PW
const SUT_DB_DSN = process.env.W2_SUT_DB_DSN
const SUT_DB_PASSWORD = process.env.W2_SUT_DB_PASSWORD
const LLM_MODEL = process.env.W2_LLM_MODEL
const LLM_BASE = (process.env.W2_LLM_BASE || "").replace(/\/+$/, "")
const LLM_UPSTREAM = (process.env.W2_LLM_UPSTREAM || "").replace(/\/+$/, "")
const LLM_PROXY_KEY = "w2-vision-proxy"
const DESIGN_TIMEOUT = Number(process.env.W2_DESIGN_TIMEOUT || 900) * 1000
const WAVE_TIMEOUT = Number(process.env.W2_EXEC_TIMEOUT || 1200) * 1000
const ISSUE_TIMEOUT = Number(process.env.W2_ISSUE_TIMEOUT || 300) * 1000
const REPORT_TIMEOUT = Number(process.env.W2_REPORT_TIMEOUT || 240) * 1000
const CLOSE_TIMEOUT = Number(process.env.W2_CLOSE_TIMEOUT || 300) * 1000
const IMPOSSIBLE = "__W2_MUST_FAIL__"

const requireFromExecutor = createRequire(path.join(REPO_ROOT, "executor", "package.json"))
const outDir = path.join(EVIDENCE, "run")
const baseDir = path.join(outDir, "base")
fs.mkdirSync(baseDir, { recursive: true })

const steps = []
const state = { checks: {}, startedAt: new Date().toISOString() }
let token = ""
let lastHttp = null

function log(...args) { console.log(new Date().toISOString(), ...args) }
function saveJson(name, value) {
  fs.writeFileSync(path.join(outDir, name), JSON.stringify(value, null, 2), "utf8")
}
function saveBaseData(value) {
  fs.writeFileSync(path.join(outDir, "base-data.json"), JSON.stringify(value, null, 2), "utf8")
}
// API responses for resource bindings carry the stored database DSN/password.
// Evidence must never echo a secret, so only the saved copy is masked; the
// in-memory binding is still used for the flow assertions.
function redactSecrets(value) {
  if (Array.isArray(value)) return value.map(redactSecrets)
  if (!value || typeof value !== "object") return value
  const copy = {}
  for (const [key, entry] of Object.entries(value)) {
    copy[key] = /^(dsn|password|api_key)$/i.test(key) ? "<redacted>" : redactSecrets(entry)
  }
  return copy
}
function sleep(ms) { return new Promise((resolve) => setTimeout(resolve, ms)) }
function nowIso() { return new Date().toISOString() }
function listData(json) { return json && Array.isArray(json.data) ? json.data : [] }

function dbQuery(sql) {
  const result = spawnSync(PSQL, ["-h", "localhost", "-p", "5433", "-U", "eztest", "-d", DB_NAME, "-tAc", sql], {
    env: { ...process.env, PGPASSWORD: DB_PASSWORD }, encoding: "utf8",
  })
  if (result.status !== 0) {
    throw new Error(`psql failed (status=${result.status}): ${String(result.stderr || result.error?.message || "").trim().slice(0, 300)}`)
  }
  return String(result.stdout || "").trim()
}

const COUNTERS_SQL = `select json_build_object(
  'requirements', (select count(*) from requirement),
  'requirementsConfirmed', (select count(*) from requirement where status in ('clarified','closed')),
  'cases', (select count(*) from case_entity),
  'caseVersions', (select count(*) from case_version),
  'caseVersionsConfirmed', (select count(*) from case_version where status = 'confirmed'),
  'planItems', (select count(*) from version_plan_item),
  'waves', (select count(*) from run_wave),
  'caseRuns', (select count(*) from case_run),
  'caseRunsPassed', (select count(*) from case_run where status = 'passed'),
  'caseRunsFailed', (select count(*) from case_run where status = 'failed'),
  'evidenceRows', (select count(*) from evidence),
  'events', (select count(*) from event),
  'issues', (select count(*) from delivery_issue),
  'reports', (select count(*) from delivery_report),
  'reportsPublished', (select count(*) from delivery_report where status = 'published')
)::text`
function dbCounters() {
  try { return JSON.parse(dbQuery(COUNTERS_SQL)) } catch (err) { return { error: String(err.message).slice(0, 300) } }
}
function versionEvents(versionId) {
  if (!versionId) return 0
  try { return Number(dbQuery(`select count(*) from event where channel = 'version:${versionId}'`)) } catch { return 0 }
}

function recordStep(name, detail = {}) {
  const step = {
    index: steps.length + 1, step: name, at: nowIso(),
    db: dbCounters(), versionEvents: versionEvents(state.versionId), ...detail,
  }
  steps.push(step)
  saveJson("w2-steps.json", steps)
  saveJson("state.json", state)
  log(`[STEP ${step.index}] ${name} http=${JSON.stringify(step.http)} db=${JSON.stringify(step.db)} versionEvents=${step.versionEvents}`)
  return step
}
function check(name, ok, detail) {
  state.checks[name] = { ok: !!ok, detail: detail === undefined ? "" : String(detail) }
  saveJson("state.json", state)
  log(`[${ok ? "PASS" : "RED"}] ${name}: ${detail}`)
  return !!ok
}

async function api(method, p, body, opts = {}) {
  const headers = { ...(opts.headers || {}) }
  if (token) headers.authorization = `Bearer ${token}`
  let payload
  if (body !== undefined && body !== null) {
    headers["content-type"] = "application/json"
    payload = JSON.stringify(body)
  }
  const res = await fetch(`${BASE}${p}`, { method, headers, body: payload })
  const text = await res.text()
  let json = null
  try { json = text === "" ? null : JSON.parse(text) } catch { json = null }
  lastHttp = { method, path: p, status: res.status }
  return { status: res.status, json, text }
}
async function must(method, p, body, expected = [200, 201]) {
  const r = await api(method, p, body)
  if (!expected.includes(r.status)) {
    throw new Error(`${method} ${p} -> ${r.status}: ${r.text.slice(0, 500)}`)
  }
  return r.json
}
async function waitFor(label, fn, timeoutMs = 120000, intervalMs = 2000) {
  const deadline = Date.now() + timeoutMs
  let last = null
  while (Date.now() < deadline) {
    try {
      const value = await fn()
      if (value) return value
      last = value
    } catch (err) { last = String(err) }
    await sleep(intervalMs)
  }
  throw new Error(`timeout waiting for ${label} (last=${JSON.stringify(last)?.slice(0, 300)})`)
}

// ---------------------------------------------------------------------------
// Browser helpers (Playwright from executor/node_modules).
// ---------------------------------------------------------------------------
let browser = null
let page = null
async function ensureBrowser() {
  if (page) return page
  const { chromium } = requireFromExecutor("playwright")
  try {
    browser = await chromium.launch({ headless: true, channel: "chrome" })
  } catch (err) {
    log("chrome channel unavailable, falling back to msedge:", String(err.message).split("\n")[0])
    browser = await chromium.launch({ headless: true, channel: "msedge" })
  }
  page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  page.setDefaultTimeout(45000)
  return page
}
async function loginUi() {
  const p = await ensureBrowser()
  await p.goto(`${WEB}/login`, { waitUntil: "domcontentloaded" })
  await sleep(800)
  if (!p.url().includes("/login")) return p
  if (await p.locator("#username").count()) {
    await p.fill("#username", "admin")
    await p.fill("#password", ADMIN_PASSWORD)
    await p.locator("button[type=submit]").click()
  }
  await p.waitForURL(/\/versions/, { timeout: 60000 })
  return p
}
async function screenshot(name) {
  const p = await ensureBrowser()
  await p.screenshot({ path: path.join(baseDir, name), fullPage: true })
  return `base/${name}`
}
async function openVersionTab(tabValue) {
  const p = await ensureBrowser()
  await p.goto(`${WEB}/versions/${state.versionId}?tab=${tabValue}`, { waitUntil: "domcontentloaded" })
  await p.waitForSelector('[data-testid="stage-bar"]', { timeout: 60000 })
  await sleep(1500)
  return p
}

// ---------------------------------------------------------------------------
// Requirement fixtures. Requirement A is satisfiable by the real SUT (the
// W1-proven home list assertion). Requirement B is impossible by construction:
// no real page title can equal the marker, so its designed assertion fails.
// ---------------------------------------------------------------------------
const PASS_REQUIREMENT = {
  title: "W2-PASS 首页文章列表可浏览",
  description: "作为访客，我希望在首页看到已发布文章列表，以便发现内容。",
  acceptance_criteria: "Given 系统中存在已发布文章 When 访客打开首页 Then 首页文章列表区域可见且至少展示 1 篇已发布文章的标题；接口 GET /api/v1/posts 返回 HTTP 200。首页是最新文章预览（最多展示 5 条），不要求页面条目数与接口返回总数相等。",
  priority: "P1",
}
const FAIL_REQUIREMENT = {
  title: "W2-FAIL 页面标题必须等于 __W2_MUST_FAIL__",
  description: "验收占位需求：页面标题必须严格等于一个不可能出现的目标值，用于产生确定性的失败用例。",
  acceptance_criteria: "Given 访客打开首页 When 页面加载完成 Then 浏览器页面标题（document.title）必须严格等于字符串 __W2_MUST_FAIL__；任何其他标题都视为失败，并记录实际标题。",
  priority: "P2",
}

function collectDesignCases(graph) {
  const cases = []
  for (const requirement of graph.requirements || []) {
    for (const scenario of requirement.scenarios || []) {
      for (const card of scenario.cases || []) {
        cases.push({
          ...card,
          requirement_id: requirement.requirement_id,
          requirement_title: requirement.title,
          requirement_status: requirement.status,
          scenario_name: scenario.name,
        })
      }
    }
  }
  return cases
}

async function fetchCaseDetail(card) {
  const r = await api("GET", `/api/versions/${state.versionId}/design/cases/${card.case_id}?version_id=${card.current_version_id}`)
  if (r.status !== 200) return null
  return r.json?.data || null
}

async function stage() {
  const r = await api("GET", `/api/versions/${state.versionId}/stage`)
  return r.status === 200 ? r.json?.data?.stage || "" : ""
}

// ---------------------------------------------------------------------------
// Main flow.
// ---------------------------------------------------------------------------
async function main() {
  saveJson("env.json", { base: BASE, web: WEB, llmBase: LLM_BASE, llmUpstream: LLM_UPSTREAM, llmModel: LLM_MODEL, startedAt: state.startedAt })
  if (!SUT_DB_DSN || !SUT_DB_PASSWORD) {
    throw new Error("SUT_DB_DSN/SUT_DB_PASSWORD are missing from the environment (required; never defaulted)")
  }
  recordStep("preflight.stack", { http: null, requirements: [PASS_REQUIREMENT.title, FAIL_REQUIREMENT.title], impossible: IMPOSSIBLE })

  // -- login ----------------------------------------------------------------
  const login = await must("POST", "/api/auth/login", { username: "admin", password: ADMIN_PASSWORD })
  token = login.data.token
  state.adminId = login.data.user.id
  recordStep("auth.login", { http: lastHttp, user: login.data.user.username })
  check("login", !!token, `admin ${login.data.user.username}`)

  // -- real LLM configuration (through the vision-caption proxy) ------------
  const cfg = await must("POST", "/api/admin/llm/configs", {
    name: "w2-deepseek-vision-proxy", base_url: LLM_BASE, api_key: LLM_PROXY_KEY,
    protocol: "openai_chat_completions", is_default: true, timeout_seconds: 180, max_retries: 2,
  })
  state.llmConfigId = cfg.data.id
  const model = await must("POST", `/api/admin/llm/configs/${state.llmConfigId}/models`, {
    model: LLM_MODEL, context_window: 128000, max_output: 8192,
    supports_tools: true, supports_vision: true, supports_reasoning: false, supports_prompt_cache: false,
  })
  state.llmModelId = model.data.id
  await must("PATCH", `/api/admin/llm/configs/${state.llmConfigId}`, { base_url: LLM_BASE, default_model_id: state.llmModelId })
  const models = await must("GET", "/api/admin/llm/configs")
  const cfgAfter = listData(models).find((entry) => entry.id === state.llmConfigId)
  const cfgModel = cfgAfter && (cfgAfter.models || []).find((entry) => entry.id === state.llmModelId)
  const cfgBase = (cfgAfter?.base_url || "").replace(/\/+$/, "")
  saveJson("llm-config.json", cfgAfter)
  state.llm = {
    model: LLM_MODEL, base: LLM_BASE, upstream: LLM_UPSTREAM, configBaseUrl: cfgAfter?.base_url,
    supportsVision: cfgModel?.supports_vision, visionBacked: cfgBase === LLM_BASE && LLM_BASE !== "" ? "proxy" : "none",
    visionExercised: false,
  }
  recordStep("llm.config", { http: lastHttp, configBaseUrl: cfgAfter?.base_url, model: cfgModel?.model, declaredVision: cfgModel?.supports_vision })
  check("llmRealEndpoint",
    !!cfgAfter && cfgBase === LLM_BASE && !!cfgModel && cfgModel.model === LLM_MODEL && cfgModel.supports_tools === true && cfgModel.supports_vision === true && state.llm.visionBacked === "proxy",
    `base_url=${cfgAfter?.base_url} model=${cfgModel?.model} tools=${cfgModel?.supports_tools} supports_vision=${cfgModel?.supports_vision} visionBacked=${state.llm.visionBacked} upstream=${LLM_UPSTREAM}`)

  // -- SUT / repositories / environment -------------------------------------
  const sut = await must("POST", "/api/resources/suts", { name: "墨栈博客 W2", intro: "v0.22 W2 base-data e2e SUT (real composition root)" })
  state.sutId = sut.data.id
  const repoBackend = await must("POST", "/api/resources/code-repos", {
    name: "blog-backend", path: "D:\\project\\blog-backend", branch: "master", include: ["**/*.go"], exclude: ["node_modules"],
  })
  const repoFrontend = await must("POST", "/api/resources/code-repos", {
    name: "blog-frontend", path: "D:\\project\\blog-frontend", branch: "master", include: ["src/**"], exclude: ["node_modules"],
  })
  state.repoBackendId = repoBackend.data.id
  state.repoFrontendId = repoFrontend.data.id
  for (const repo of [repoBackend.data.id, repoFrontend.data.id]) {
    await must("POST", `/api/suts/${state.sutId}/repositories`, { repository_id: repo, role: "code" })
  }
  const environment = await must("POST", `/api/resources/suts/${state.sutId}/environments`, {
    name: "本地环境", base_url: "http://localhost:5173", is_default: true,
  })
  state.environmentId = environment.data.id
  const pages = [
    { name: "首页", url: "http://localhost:5173/", route_template: "/" },
    { name: "登录", url: "http://localhost:5173/login", route_template: "/login" },
    { name: "文章详情", url: "http://localhost:5173/article/1", route_template: "/article/:id" },
    { name: "后台管理", url: "http://localhost:5173/admin", route_template: "/admin" },
  ]
  state.pageIds = []
  state.pageStatuses = []
  for (const item of pages) {
    const created = await must("POST", `/api/resources/environments/${state.environmentId}/pages`, item)
    state.pageIds.push(created.data.id)
    state.pageStatuses.push(created.data.status)
  }
  const apis = [
    { method: "GET", path_template: "/api/v1/posts", name: "文章列表" },
    { method: "GET", path_template: "/api/v1/posts/:id", name: "文章详情" },
    { method: "POST", path_template: "/api/v1/auth/login", name: "登录" },
    { method: "GET", path_template: "/api/v1/categories", name: "分类列表" },
  ]
  state.apiIds = []
  state.apiStatuses = []
  for (const item of apis) {
    const created = await must("POST", `/api/resources/environments/${state.environmentId}/apis`, item)
    state.apiIds.push(created.data.id)
    state.apiStatuses.push(created.data.status)
  }
  const database = await must("POST", `/api/resources/suts/${state.sutId}/databases`, {
    environment_id: state.environmentId, name: "blog-db", kind: "postgres",
    dsn: SUT_DB_DSN,
    username: "blog", password: SUT_DB_PASSWORD, read_only: true,
  })
  state.databaseId = database.data.id
  const account = await must("POST", `/api/resources/suts/${state.sutId}/accounts`, {
    environment_id: state.environmentId, username: "admin", password: SUT_PASSWORD, roles: ["admin"],
  })
  state.accountId = account.data.id
  recordStep("resources", { http: lastHttp, sutId: state.sutId, environmentId: state.environmentId, pages: state.pageIds.length, apis: state.apiIds.length })
  const allApproved = state.pageStatuses.every((s) => s === "approved") && state.apiStatuses.every((s) => s === "approved")
  check("sutEnvironmentResources",
    !!state.sutId && !!state.environmentId && !!state.databaseId && !!state.accountId && state.pageIds.length === 4 && state.apiIds.length === 4 && allApproved,
    `sut=${state.sutId} environment=${state.environmentId} pages=${state.pageIds.length} apis=${state.apiIds.length} pagesApproved=${state.pageStatuses.join(",")} apisApproved=${state.apiStatuses.join(",")}`)

  // -- version (environment only) ------------------------------------------
  const version = await must("POST", "/api/versions", {
    title: "W2 墨栈博客基线数据版本", description: "v0.22 W2 base-data e2e (real LLM + real executor + real SUT)", environment_id: state.environmentId,
  })
  state.versionId = version.data.id
  const versionRead = await must("GET", `/api/versions/${state.versionId}`)
  const binding = (versionRead.data.bindings || [])[0] || {}
  state.versionSutId = binding.id
  saveJson("version-binding.json", redactSecrets(binding))
  recordStep("version.created", { http: lastHttp, versionId: state.versionId, bindingId: state.versionSutId })
  check("versionEnvironmentOnly",
    !!binding.environment && binding.environment.id === state.environmentId && Array.isArray(binding.repositories) && binding.repositories.length === 2 &&
      (binding.pages || []).length === 4 && (binding.apis || []).length === 4 && (binding.accounts || []).length === 1 && (binding.databases || []).length === 1,
    `environment=${binding.environment?.id} pages=${(binding.pages || []).length} apis=${(binding.apis || []).length} accounts=${(binding.accounts || []).length} databases=${(binding.databases || []).length} repositories=${(binding.repositories || []).length}`)

  // -- created -> clarifying ------------------------------------------------
  let stageNow = await stage()
  if (stageNow === "created") {
    const advanced = await api("POST", `/api/versions/${state.versionId}/stage/advance`, {})
    if (advanced.status !== 200) throw new Error(`stage created->clarifying -> ${advanced.status}: ${advanced.text.slice(0, 300)}`)
    stageNow = await stage()
  }
  recordStep("stage.clarifying", { http: lastHttp, stage: stageNow })
  check("stageClarifying", stageNow === "clarifying", `stage=${stageNow}`)

  // -- two requirements through the real API --------------------------------
  const passReq = await must("POST", `/api/versions/${state.versionId}/requirements`, {
    ...PASS_REQUIREMENT,
    constraints: { source: "w2-base-e2e", role: "pass" },
    targets: { version_suts: [state.versionSutId], pages: [state.pageIds[0]], apis: [state.apiIds[0]] },
  })
  const failReq = await must("POST", `/api/versions/${state.versionId}/requirements`, {
    ...FAIL_REQUIREMENT,
    constraints: { source: "w2-base-e2e", role: "fail", impossible_marker: IMPOSSIBLE },
    targets: { version_suts: [state.versionSutId], pages: [state.pageIds[0]], apis: [] },
  })
  state.passRequirementId = passReq.data.id
  state.failRequirementId = failReq.data.id
  state.passRequirementVersionId = passReq.data.current_version?.id
  state.failRequirementVersionId = failReq.data.current_version?.id
  saveJson("requirements.json", { pass: passReq.data, fail: failReq.data })
  recordStep("requirements.created", { http: lastHttp, pass: state.passRequirementId, fail: state.failRequirementId })
  check("requirementsCreated", !!state.passRequirementId && !!state.failRequirementId && state.passRequirementId !== state.failRequirementId,
    `pass=${state.passRequirementId} fail=${state.failRequirementId}`)

  const confirm = await must("POST", `/api/versions/${state.versionId}/requirements/confirmations`, {
    requirement_ids: [state.passRequirementId, state.failRequirementId],
  })
  const reqList = await must("GET", `/api/versions/${state.versionId}/requirements?limit=200`)
  const liveReqs = listData(reqList).filter((entry) => entry.status !== "obsolete")
  const allClarified = liveReqs.length === 2 && liveReqs.every((entry) => entry.status === "clarified")
  recordStep("requirements.confirmed", { http: lastHttp, confirmed: (confirm.data?.confirmed || []).length, skipped: (confirm.data?.skipped || []).length, statuses: liveReqs.map((entry) => `${entry.id}:${entry.status}`) })
  check("requirementsConfirmed", allClarified, `confirmed=${(confirm.data?.confirmed || []).length} statuses=${liveReqs.map((entry) => `${entry.id}:${entry.status}`).join(",")}`)

  // -- requirement_review -> designing --------------------------------------
  stageNow = await stage()
  if (stageNow === "requirement_review") {
    const advanced = await api("POST", `/api/versions/${state.versionId}/stage/advance`, {})
    if (advanced.status !== 200) throw new Error(`stage requirement_review->designing -> ${advanced.status}: ${advanced.text.slice(0, 300)}`)
    stageNow = await stage()
  }
  recordStep("stage.designing", { http: lastHttp, stage: stageNow })
  check("stageDesigning", stageNow === "designing", `stage=${stageNow}`)

  // -- real design Host/LLM ------------------------------------------------
  const design = await waitFor("design cases at case_review", async () => {
    const current = await stage()
    const graph = await api("GET", `/api/versions/${state.versionId}/design/graph`)
    if (graph.status !== 200) return null
    const cases = collectDesignCases(graph.json?.data || {})
    if (current === "case_review" && cases.length > 0) {
      return { stage: current, graph: graph.json.data, cases }
    }
    return null
  }, DESIGN_TIMEOUT, 3000)
  saveJson("design-graph.json", design.graph)
  const passCandidates = design.cases.filter((entry) => String(entry.requirement_id) === String(state.passRequirementId))
  const failCandidates = design.cases.filter((entry) => String(entry.requirement_id) === String(state.failRequirementId))
  recordStep("design.cases", { http: lastHttp, stage: design.stage, cases: design.cases.length, passCases: passCandidates.length, failCases: failCandidates.length })
  check("designCases", design.stage === "case_review" && passCandidates.length >= 1 && failCandidates.length >= 1,
    `cases=${design.cases.length} pass=${passCandidates.length} fail=${failCandidates.length}`)

  // The design host may still be finishing its round; let it settle before we
  // confirm case versions so a confirmation never races a case write.
  try {
    await waitFor("design host runs settle", async () => {
      const runs = await api("GET", "/api/agent-center/runs?limit=100")
      return listData(runs.json).filter((run) => run.profile_key === "design_host" && run.status === "running").length === 0
    }, 180000, 3000)
  } catch (err) { log("design host did not settle:", String(err.message).slice(0, 160)) }

  // Re-read the graph once the host settled, then inspect full case details so
  // the selected cases are exactly: one satisfiable home-list case and one case
  // whose assertion carries the impossible marker.
  const finalGraph = await must("GET", `/api/versions/${state.versionId}/design/graph`)
  const finalCases = collectDesignCases(finalGraph.data || {})
  saveJson("design-graph-final.json", finalGraph.data)
  const passPool = finalCases.filter((entry) => String(entry.requirement_id) === String(state.passRequirementId) && Number(entry.step_count) > 0 && Number(entry.assertion_count) > 0)
  const failPool = finalCases.filter((entry) => String(entry.requirement_id) === String(state.failRequirementId) && Number(entry.step_count) > 0 && Number(entry.assertion_count) > 0)
  for (const card of [...passPool, ...failPool]) {
    card.detail = await fetchCaseDetail(card)
  }
  saveJson("case-details.json", [...passPool, ...failPool].map((entry) => ({ case_id: entry.case_id, title: entry.title, requirement_id: entry.requirement_id, detail: entry.detail })))

  const passCase = passPool.find((entry) => /首页|列表|home|FR-001/i.test(`${entry.title || ""} ${entry.scenario_name || ""}`) && !/空|错误|登录|5xx|500|分页|鉴权|null/i.test(entry.title || "")) || passPool[0]
  const failCase = failPool.find((entry) => (entry.detail?.selected?.assertions || []).some((a) => String(a.expected || "").includes(IMPOSSIBLE))) || failPool[0]
  const failAssertions = failCase?.detail?.selected?.assertions || []
  const impossibleAssertion = failAssertions.find((a) => String(a.expected || "").includes(IMPOSSIBLE))
  state.cases = {
    pass: passCase ? { case_id: passCase.case_id, case_version_id: passCase.current_version_id, title: passCase.title, detail: passCase.detail } : null,
    fail: failCase ? { case_id: failCase.case_id, case_version_id: failCase.current_version_id, title: failCase.title, detail: failCase.detail, assertions: failAssertions } : null,
  }
  saveJson("selected-cases.json", state.cases)
  recordStep("design.cases.selected", { http: lastHttp, pass: passCase?.case_id, fail: failCase?.case_id, impossibleAssertion: impossibleAssertion?.expected })
  check("passCaseSelected", !!passCase, passCase ? `${passCase.case_id} (${passCase.title})` : "no executable case for the satisfiable requirement")
  check("failCaseSelected", !!failCase, failCase ? `${failCase.case_id} (${failCase.title})` : "no executable case for the impossible requirement")
  check("failCaseImpossibleAssertion", !!impossibleAssertion,
    impossibleAssertion ? `assertion expected=${impossibleAssertion.expected}` : `no designed assertion carries ${IMPOSSIBLE}; assertions=${JSON.stringify(failAssertions.map((a) => a.expected)).slice(0, 300)}`)
  if (!passCase || !failCase) throw new Error("design did not produce an executable case for both requirements")
  if (!impossibleAssertion) throw new Error(`design produced no assertion carrying ${IMPOSSIBLE}; the failing run is not deterministic`)

  // -- confirm exactly one case per requirement -----------------------------
  const confirmed = []
  const confirmations = [
    { role: "pass", card: passCase, batch: "w2-pass" },
    { role: "fail", card: failCase, batch: "w2-fail" },
  ]
  for (const item of confirmations) {
    const result = await must("POST", `/api/versions/${state.versionId}/design/confirmations`, {
      scope: "case", target_id: item.card.case_id, batch_id: item.batch,
    })
    const entry = (result.data?.confirmed || [])[0]
    confirmed.push({ role: item.role, case_id: entry?.case_id || item.card.case_id, case_version_id: entry?.case_version_id || item.card.current_version_id })
  }
  state.confirmedCases = confirmed
  saveJson("confirmed-cases.json", confirmed)
  recordStep("design.cases.confirmed", { http: lastHttp, confirmed: confirmed.length })
  check("designCasesConfirmed", confirmed.length === 2 && confirmed.every((entry) => !!entry.case_version_id), `confirmed=${confirmed.map((entry) => `${entry.role}:${entry.case_id}/${entry.case_version_id}`).join(",")}`)

  // -- case_review -> executing, then start the wave ------------------------
  stageNow = await stage()
  if (stageNow === "case_review") {
    const advanced = await api("POST", `/api/versions/${state.versionId}/stage/advance`, {})
    if (advanced.status !== 200) throw new Error(`stage case_review->executing -> ${advanced.status}: ${advanced.text.slice(0, 300)}`)
  }
  const waveStart = await api("POST", `/api/versions/${state.versionId}/waves`)
  state.waveId = waveStart.json?.data?.wave_id || null
  state.waveStart = { status: waveStart.status, body: waveStart.json?.data || waveStart.text?.slice(0, 400) }
  recordStep("stage.executing", { http: lastHttp, stage: await stage() })
  recordStep("wave.start", { http: lastHttp, waveId: state.waveId, status: waveStart.status, caseRunCount: waveStart.json?.data?.case_run_count, body: waveStart.status === 201 ? undefined : waveStart.text?.slice(0, 300) })
  check("stageExecuting", (await stage()) === "executing", `stage=${await stage()}`)
  check("waveStart201", waveStart.status === 201 && !!state.waveId, `POST /waves -> ${waveStart.status} wave_id=${state.waveId} case_run_count=${waveStart.json?.data?.case_run_count}`)
  if (!state.waveId) throw new Error(`POST /waves did not return a wave_id: ${waveStart.status} ${waveStart.text?.slice(0, 400)}`)

  // -- poll the wave to a terminal status ----------------------------------
  const wave = await waitFor("wave terminal", async () => {
    const r = await api("GET", `/api/versions/${state.versionId}/waves/${state.waveId}`)
    if (r.status !== 200) return null
    const current = r.json?.data
    return current && ["done", "failed", "cancelled"].includes(current.status) ? current : null
  }, WAVE_TIMEOUT, 3000)
  state.wave = wave
  saveJson("wave.json", wave)
  const caseRuns = await must("GET", `/api/versions/${state.versionId}/waves/${state.waveId}/case-runs`)
  const runs = listData(caseRuns)
  saveJson("wave-case-runs.json", runs)
  state.caseRuns = runs.map((run) => ({ id: run.id, case_id: run.case_id, case_version_id: run.case_version_id, status: run.status, failure_class: run.failure_class, title: run.title }))
  const passedRuns = runs.filter((run) => run.status === "passed")
  const failedRuns = runs.filter((run) => run.status === "failed")
  state.caseRunIds = { passed: passedRuns.map((run) => run.id), failed: failedRuns.map((run) => run.id) }
  const waveCounts = wave.counts || {}
  recordStep("wave.terminal", { http: lastHttp, waveId: state.waveId, status: wave.status, counts: wave.counts, caseRuns: runs.length, runStatuses: state.caseRuns.map((run) => run.status) })
  check("waveTerminal", ["done", "failed", "cancelled"].includes(wave.status), `wave=${state.waveId} status=${wave.status} counts=${JSON.stringify(wave.counts)}`)
  check("waveCountsPassedAndFailed", Number(waveCounts.passed) >= 1 && Number(waveCounts.failed) >= 1 && passedRuns.length >= 1 && failedRuns.length >= 1,
    `counts.total=${waveCounts.total} passed=${waveCounts.passed} failed=${waveCounts.failed} runs=${runs.length} statuses=${runs.map((run) => run.status).join(",")}`)

  // Read the failed run detail: the assertion failure is the deterministic
  // proof that the impossible requirement produced a real application failure.
  for (const run of failedRuns) {
    const detail = await api("GET", `/api/versions/${state.versionId}/case-runs/${run.id}`)
    if (detail.status === 200) {
      run.detail = detail.json?.data
      saveJson(`case-run-${run.id}.json`, detail.json?.data)
    }
  }
  const failingRun = failedRuns[0]
  const failedAssertions = (failingRun?.detail?.assertions || []).filter((a) => a.verdict === "failed")
  const impossibleFailure = failedAssertions.find((a) => String(a.expected || "").includes(IMPOSSIBLE))
  state.failingRun = failingRun ? {
    id: failingRun.id, status: failingRun.status, failure_class: failingRun.failure_class,
    failedAssertions: failedAssertions.map((a) => ({ step_seq: a.step_seq, assertion_seq: a.assertion_seq, kind: a.kind, expected: a.expected, actual: a.actual, reason: a.reason })),
    impossibleMarkerObserved: !!impossibleFailure,
  } : null
  saveJson("failing-run.json", state.failingRun)
  recordStep("wave.failure", { http: lastHttp, failedRun: failingRun?.id, failureClass: failingRun?.failure_class, failedAssertions: failedAssertions.length, impossibleMarkerObserved: !!impossibleFailure })
  check("failingRunIsFailed", !!failingRun && failingRun.status === "failed", failingRun ? `case_run=${failingRun.id} status=${failingRun.status} failure_class=${failingRun.failure_class}` : "no failed case_run")
  check("failingAssertionCarriesMarker", !!impossibleFailure, impossibleFailure ? `expected=${impossibleFailure.expected} actual=${impossibleFailure.actual}` : `failed assertions=${JSON.stringify(failedAssertions.map((a) => a.expected)).slice(0, 300)}`)

  // -- P4 reconciler: delivery_issue from the failed run --------------------
  let issueIds = []
  try {
    issueIds = await waitFor("delivery issue for the failed case_run", async () => {
      const raw = dbQuery(`select coalesce(json_agg(id order by id), '[]')::text from delivery_issue where version_id = ${state.versionId}`)
      const ids = JSON.parse(raw)
      return ids.length > 0 ? ids : null
    }, ISSUE_TIMEOUT, 3000)
  } catch (err) {
    issueIds = []
    log("no delivery_issue observed:", String(err.message).slice(0, 200))
  }
  state.issueIds = issueIds
  const issueDetail = issueIds.length > 0 ? dbQuery(`select json_agg(json_build_object('id', id, 'status', status, 'severity', severity, 'category', category, 'source', source, 'title', title))::text from delivery_issue where version_id = ${state.versionId}`) : "[]"
  state.issueRows = safeParse(issueDetail)
  saveJson("issues.json", state.issueRows)
  recordStep("issues", { http: lastHttp, issueIds: state.issueIds })
  check("deliveryIssueCreated", state.issueIds.length >= 1, `delivery_issue ids=${state.issueIds.join(",")}`)

  // -- report (best effort: a published wave report is recorded when the P4
  //    writer produces one; version report is W2-7/W2-8 territory) ----------
  let reports = []
  let report = null
  try {
    report = await waitFor("published report", async () => {
      const r = await api("GET", `/api/versions/${state.versionId}/reports`)
      if (r.status !== 200) return null
      reports = listData(r.json)
      return reports.find((entry) => entry.status === "published" || entry.current_revision?.status === "published") || null
    }, REPORT_TIMEOUT, 4000)
  } catch (err) {
    const r = await api("GET", `/api/versions/${state.versionId}/reports`)
    if (r.status === 200) reports = listData(r.json)
    log("no published report observed (best effort):", String(err.message).slice(0, 160))
  }
  const anyReport = report || reports[0] || null
  state.report = anyReport ? {
    id: anyReport.id, kind: anyReport.kind, status: anyReport.status, wave_id: anyReport.wave_id,
    revision: anyReport.current_revision?.revision ?? null,
    revisionStatus: anyReport.current_revision?.status || null,
    current_revision_status: anyReport.current_revision?.status || null,
  } : null
  saveJson("reports.json", reports)
  recordStep("report", { http: lastHttp, reports: reports.length, report: state.report })

  // -- the version must still be reusable (executing), not reporting -------
  await sleep(5000)
  const stageAfterWave = await stage()
  state.stageAfterWave = stageAfterWave
  recordStep("stage.afterWave", { http: null, stage: stageAfterWave })
  check("stageStillExecuting", stageAfterWave === "executing", `stage=${stageAfterWave} (must stay executing for later W2 waves; reporting would make the version read-only)`)

  // -- requirement closure (P4 `requirement.close` task) -------------------
  // The reconciler only enqueues the close task once the predicate passes; an
  // agent worker then closes the requirement. That is asynchronous and can
  // take ~1 min, so wait for the pass requirement to reach `closed` before the
  // final snapshot (the impossible requirement must stay `clarified`).
  try {
    await waitFor("pass requirement closure", async () => {
      const raw = dbQuery(`select status from requirement where id = ${state.passRequirementId}`)
      return raw === "closed" ? raw : null
    }, CLOSE_TIMEOUT, 3000)
    const passStatus = dbQuery(`select status from requirement where id = ${state.passRequirementId}`)
    state.passRequirementStatus = passStatus
    check("passRequirementClosed", passStatus === "closed", `requirement=${state.passRequirementId} status=${passStatus}`)
  } catch (err) {
    let passStatus = "unknown"
    try { passStatus = dbQuery(`select status from requirement where id = ${state.passRequirementId}`) } catch { }
    state.passRequirementStatus = passStatus
    check("passRequirementClosed", false, `requirement=${state.passRequirementId} status=${passStatus} after ${Math.round(CLOSE_TIMEOUT / 1000)}s: ${String(err.message).slice(0, 160)}`)
  }

  // -- evidence ids ---------------------------------------------------------
  let evidenceIds = []
  try {
    const evidence = await must("GET", `/api/versions/${state.versionId}/evidence`)
    evidenceIds = listData(evidence).map((entry) => entry.id)
    saveJson("evidence.json", evidence)
  } catch (err) { log("evidence read failed:", String(err.message).slice(0, 160)) }
  state.evidenceIds = evidenceIds

  // -- screenshots ----------------------------------------------------------
  try {
    await loginUi()
    await openVersionTab("overview")
    await screenshot("01-version-overview.png")
    await openVersionTab("requirements")
    await screenshot("02-requirements.png")
    await openVersionTab("execution")
    await screenshot("03-execution.png")
    state.screenshots = fs.readdirSync(baseDir).filter((name) => name.endsWith(".png")).sort().map((name) => `base/${name}`)
    const nonEmpty = state.screenshots.filter((name) => {
      try { return fs.statSync(path.join(outDir, name)).size > 0 } catch { return false }
    })
    check("screenshots", nonEmpty.length >= 3, `${nonEmpty.length} non-empty: ${nonEmpty.join(", ")}`)
  } catch (err) {
    state.screenshots = fs.readdirSync(baseDir).filter((name) => name.endsWith(".png")).sort().map((name) => `base/${name}`)
    check("screenshots", false, `screenshot flow failed: ${String(err.message).slice(0, 300)} (captured=${state.screenshots.length})`)
  }

  // -- final authoritative snapshot ----------------------------------------
  // P4 triage can replace the candidate issue row the flow first observed, and
  // the design host keeps proposing resources after the flow's own creation.
  // Re-read the final DB here so base-data.json matches the surviving rows; the
  // PowerShell verdict phase re-reads again after the flow and rewrites the
  // issue/evidence fields from that later snapshot.
  const issueIdsPreTriage = (state.issueIds || []).map(String)
  let finalIssueRows = []
  try {
    finalIssueRows = JSON.parse(dbQuery(`select coalesce(json_agg(json_build_object('id', id, 'status', status, 'severity', severity, 'category', category, 'source', source, 'title', title, 'merged_into_id', merged_into_id) order by id), '[]')::text from delivery_issue where version_id = ${state.versionId}`))
    state.issueIds = finalIssueRows.map((entry) => entry.id)
    state.issueIdsPreTriage = issueIdsPreTriage
    state.issueRows = finalIssueRows
    saveJson("issues.json", finalIssueRows)
    recordStep("issues.final", { http: null, issueIds: state.issueIds, preTriageIssueIds: issueIdsPreTriage })
  } catch (err) {
    log("final issue snapshot failed (keeping the flow snapshot):", String(err.message).slice(0, 200))
    finalIssueRows = state.issueRows || []
  }

  function statusCounts(table) {
    try {
      const raw = dbQuery(`select coalesce(json_object_agg(status, n), '{}')::text from (select status, count(*) as n from ${table} where environment_id = ${state.environmentId} group by status) t`)
      return JSON.parse(raw || "{}")
    } catch { return {} }
  }
  const sumCounts = (counts) => Object.values(counts).reduce((total, value) => total + Number(value || 0), 0)
  const pageStatusCounts = statusCounts("sut_page")
  const apiStatusCounts = statusCounts("sut_api")
  const pageCounts = { approved: Number(pageStatusCounts.approved || 0), proposed: Number(pageStatusCounts.proposed || 0), rejected: Number(pageStatusCounts.rejected || 0), total: sumCounts(pageStatusCounts) }
  const apiCounts = { approved: Number(apiStatusCounts.approved || 0), proposed: Number(apiStatusCounts.proposed || 0), rejected: Number(apiStatusCounts.rejected || 0), total: sumCounts(apiStatusCounts) }
  const environmentResourceNote = `${state.pageIds.length}+${state.apiIds.length} flow-created approved; design host later proposed ${pageCounts.proposed} pages/${apiCounts.proposed} APIs`
  state.environmentResources = { pageIds: state.pageIds, apiIds: state.apiIds, pageCounts, apiCounts, note: environmentResourceNote }
  saveJson("environment-resources.json", state.environmentResources)

  let requirementRows = []
  try {
    requirementRows = JSON.parse(dbQuery(`select coalesce(json_agg(json_build_object('id', id, 'status', status) order by id), '[]')::text from requirement where version_id = ${state.versionId}`))
  } catch (err) { log("final requirement snapshot failed:", String(err.message).slice(0, 160)) }
  const requirementStates = {
    pass: requirementRows.find((entry) => String(entry.id) === String(state.passRequirementId)) || null,
    fail: requirementRows.find((entry) => String(entry.id) === String(state.failRequirementId)) || null,
  }
  state.requirementStates = requirementStates
  saveJson("requirement-states.json", requirementStates)

  // -- final base-data payload (the PowerShell phase re-verifies it) --------
  const baseData = {
    versionId: state.versionId,
    environmentId: state.environmentId,
    sutId: state.sutId,
    versionSutId: state.versionSutId,
    repoIds: [state.repoBackendId, state.repoFrontendId],
    requirementIds: [state.passRequirementId, state.failRequirementId],
    passRequirementId: state.passRequirementId,
    failRequirementId: state.failRequirementId,
    caseIds: confirmed.map((entry) => entry.case_id),
    caseVersionIds: confirmed.map((entry) => entry.case_version_id),
    passCaseId: passCase.case_id,
    passCaseVersionId: confirmed.find((entry) => entry.role === "pass")?.case_version_id,
    failCaseId: failCase.case_id,
    failCaseVersionId: confirmed.find((entry) => entry.role === "fail")?.case_version_id,
    waveId: state.waveId,
    caseRunIds: state.caseRunIds,
    issueIds: state.issueIds,
    issueIdsPreTriage,
    issueRows: finalIssueRows,
    requirementStates,
    reportId: state.report?.id || null,
    reportKind: state.report?.kind || null,
    reportStatus: state.report?.status || null,
    reportRevision: state.report?.revision ?? null,
    reportRevisionStatus: state.report?.revisionStatus || null,
    evidenceIds,
    stage: state.stageAfterWave,
    apiBase: BASE,
    webUrl: WEB,
    executorUrl: process.env.W2_EXECUTOR_URL,
    pageIds: state.pageIds,
    apiIds: state.apiIds,
    environmentPages: pageCounts,
    environmentApis: apiCounts,
    environmentResourceNote,
    generatedAt: nowIso(),
  }
  saveBaseData(baseData)
  saveJson("state.json", state)
  saveJson("result.json", { checks: state.checks, steps, state, baseData, finishedAt: nowIso() })
  const failed = Object.entries(state.checks).filter(([, value]) => !value.ok)
  log(`W2 BASE FLOW COMPLETE: ${Object.keys(state.checks).length - failed.length} PASS / ${failed.length} RED`)
  return failed.length === 0
}

function safeParse(text) {
  try { return JSON.parse(text) } catch { return [] }
}

async function shutdown(code) {
  try { if (browser) await browser.close() } catch { }
  await sleep(500)
  process.exit(code)
}
main().then((ok) => {
  shutdown(ok ? 0 : 1)
}).catch((err) => {
  log("W2 BASE FLOW FAILED:", err.stack || err.message)
  state.error = String(err.stack || err.message)
  saveJson("state.json", state)
  saveJson("result.json", { checks: state.checks, steps, state, error: state.error, finishedAt: nowIso() })
  shutdown(1)
})
'@ | Set-Content -Encoding UTF8 $flowPath

        $env:W2_BASE = $apiBase
        $env:W2_WEB = $webUrl
        $env:W2_EXECUTOR_URL = $executorUrl
        $env:W2_EVIDENCE = $evidenceRoot
        $env:W2_REPO_ROOT = $repoRoot
        $env:W2_PSQL = $psql
        $env:W2_DB = $dbName
        $env:W2_DB_PASSWORD = $dbPassword
        $env:W2_ADMIN_PW = $adminPassword
        $env:W2_SUT_PW = $sutPassword
        $env:W2_SUT_DB_DSN = $sutDbDsn
        $env:W2_SUT_DB_PASSWORD = $sutDbPassword
        $env:W2_LLM_MODEL = $llmModel
        $env:W2_LLM_BASE = $proxyBase
        $env:W2_LLM_UPSTREAM = $llmBase
        $env:W2_DESIGN_TIMEOUT = "$DesignTimeoutSeconds"
        $env:W2_EXEC_TIMEOUT = "$ExecutionTimeoutSeconds"
        $env:W2_ISSUE_TIMEOUT = "$IssueTimeoutSeconds"
        $env:W2_REPORT_TIMEOUT = "$ReportTimeoutSeconds"

        Push-Location $work
        try {
            # PS 5.1 turns native stderr into terminating errors under
            # $ErrorActionPreference='Stop'; capture all streams to a file instead.
            $flowLog = Join-Path $runEvidence 'flow.log'
            $previous = $ErrorActionPreference
            $ErrorActionPreference = 'Continue'
            & $nodeExe $flowPath *> $flowLog
            $flowExit = $LASTEXITCODE
            $ErrorActionPreference = $previous
        } finally { Pop-Location }
        Write-Host "== flow exit=$flowExit"
        if (Test-Path (Join-Path $runEvidence 'flow.log')) {
            Get-Content (Join-Path $runEvidence 'flow.log') -Tail 40 | Write-Host
        }

        # -------------------------------------------------------------------
        # 5. Independent DB/API verdict phase.
        # -------------------------------------------------------------------
        Write-Host ''
        Write-Host '== W2 verdict phase (fresh DB + API assertions)'
        $flowState = $null
        $flowResult = $null
        try { if (Test-Path (Join-Path $runEvidence 'state.json')) { $flowState = Get-Content (Join-Path $runEvidence 'state.json') -Raw -Encoding UTF8 | ConvertFrom-Json } } catch { Write-Warning "state.json unreadable: $($_.Exception.Message)" }
        try { if (Test-Path $flowResultPath) { $flowResult = Get-Content $flowResultPath -Raw -Encoding UTF8 | ConvertFrom-Json } } catch { Write-Warning "result.json unreadable: $($_.Exception.Message)" }
        $base = $null
        try { if (Test-Path $baseDataPath) { $base = Get-Content $baseDataPath -Raw -Encoding UTF8 | ConvertFrom-Json } } catch { Write-Warning "base-data.json unreadable: $($_.Exception.Message)" }

        $red = New-Object System.Collections.Generic.List[string]
        $assertions = [ordered]@{}
        $details = [ordered]@{}
        function Add-Assertion([string]$name, [bool]$ok, [string]$detail) {
            $script:assertions[$name] = $ok
            $script:details[$name] = $detail
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

        if ($flowResult -and $flowResult.error) { $red.Add("flow: $($flowResult.error)") }
        if ($null -eq $flowResult) { $red.Add('flow produced no result.json') }
        if ($null -eq $base) { $red.Add('flow produced no base-data.json') }

        # -- version / wave identity ---------------------------------------
        $versionId = if ($base) { [string]$base.versionId } else { '' }
        if (-not $versionId) { try { $versionId = Db 'select id from version order by id desc limit 1' } catch { $versionId = '' } }
        $waveId = if ($base) { [string]$base.waveId } else { '' }
        if (-not $waveId -and $versionId) {
            try { $waveId = Db "select id from run_wave where version_id = $versionId order by id desc limit 1" } catch { $waveId = '' }
        }
        $versionParts = @()
        if ($versionId) {
            try { $versionParts = (Db "select stage||'|'||status||'|'||coalesce(closed_at::text,'null') from version where id = $versionId").Split('|') } catch { $red.Add("db.version query failed: $($_.Exception.Message)") }
        }
        $versionStage = if ($versionParts.Count -ge 1) { $versionParts[0] } else { '' }
        $versionStatus = if ($versionParts.Count -ge 2) { $versionParts[1] } else { '' }
        $versionClosedAt = if ($versionParts.Count -ge 3) { $versionParts[2] } else { '' }

        # -- DB counters ----------------------------------------------------
        $rawCounters = $null
        try {
            $rawCounters = (Db "select json_build_object(
              'requirements',(select count(*) from requirement),
              'requirementsClarifiedOrClosed',(select count(*) from requirement where status in ('clarified','closed')),
              'cases',(select count(*) from case_entity),
              'caseVersions',(select count(*) from case_version),
              'caseVersionsConfirmed',(select count(*) from case_version where status = 'confirmed'),
              'planItems',(select count(*) from version_plan_item),
              'waves',(select count(*) from run_wave),
              'caseRuns',(select count(*) from case_run),
              'caseRunsPassed',(select count(*) from case_run where status = 'passed'),
              'caseRunsFailed',(select count(*) from case_run where status = 'failed'),
              'evidenceRows',(select count(*) from evidence),
              'events',(select count(*) from event),
              'issues',(select count(*) from delivery_issue),
              'reports',(select count(*) from delivery_report),
              'reportsPublished',(select count(*) from delivery_report where status = 'published'))::text") | ConvertFrom-Json
        } catch { $red.Add("db counters query failed: $($_.Exception.Message)") }
        $dbCounters = [ordered]@{}
        foreach ($key in 'requirements', 'requirementsClarifiedOrClosed', 'cases', 'caseVersions', 'caseVersionsConfirmed', 'planItems', 'waves', 'caseRuns', 'caseRunsPassed', 'caseRunsFailed', 'evidenceRows', 'events', 'issues', 'reports', 'reportsPublished') {
            $dbCounters[$key] = if ($rawCounters) { [int64]$rawCounters.$key } else { 0 }
        }

        # -- DB ids ---------------------------------------------------------
        $passedRunIds = @()
        $failedRunIds = @()
        if ($waveId) {
            try {
                $passedRunIds = @(Parse-JsonArray (Db "select coalesce(json_agg(id order by id), '[]') from case_run where wave_id = $waveId and status = 'passed'"))
                $failedRunIds = @(Parse-JsonArray (Db "select coalesce(json_agg(id order by id), '[]') from case_run where wave_id = $waveId and status = 'failed'"))
            } catch { $red.Add("db.caseRunIds query failed: $($_.Exception.Message)") }
        }
        $issueIds = @()
        $reportRows = @()
        $evidenceIds = @()
        if ($versionId) {
            try { $issueIds = @(Parse-JsonArray (Db "select coalesce(json_agg(id order by id), '[]') from delivery_issue where version_id = $versionId")) } catch { $red.Add("db.issueIds query failed: $($_.Exception.Message)") }
            try { $reportRows = @(Parse-JsonArray (Db "select coalesce(json_agg(json_build_object('id',id,'kind',kind,'status',status,'wave_id',wave_id) order by id), '[]') from delivery_report where version_id = $versionId")) } catch { $red.Add("db.reportRows query failed: $($_.Exception.Message)") }
            try { $evidenceIds = @(Parse-JsonArray (Db "select coalesce(json_agg(e.id order by e.id), '[]') from evidence e join case_run r on r.id = e.case_run_id join run_wave w on w.id = r.wave_id where w.version_id = $versionId")) } catch { $red.Add("db.evidenceIds query failed: $($_.Exception.Message)") }
        }
        $publishedReport = $reportRows | Where-Object { $_.status -eq 'published' } | Select-Object -First 1
        $anyReport = $publishedReport
        if (-not $anyReport) { $anyReport = $reportRows | Select-Object -First 1 }
        $reportId = if ($anyReport) { [string]$anyReport.id } else { $null }
        $reportKind = if ($anyReport) { [string]$anyReport.kind } else { $null }
        $reportStatus = if ($anyReport) { [string]$anyReport.status } else { $null }
        # A wave report is published before later auto-augmentation can mark the
        # row stale; the published revision and the report.published event are
        # the acceptance evidence the brief asks for.
        $reportPublishedRevisions = 0
        $reportPublishedEvents = 0
        if ($versionId) {
            try { $reportPublishedRevisions = [int](Db "select count(*) from delivery_report_revision rev join delivery_report rp on rp.id = rev.report_id where rp.version_id = $versionId and rev.status = 'published'") } catch { $red.Add("db.reportPublishedRevisions query failed: $($_.Exception.Message)") }
            try { $reportPublishedEvents = [int](Db "select count(*) from event where channel = 'version:$versionId' and type = 'report.published'") } catch { $red.Add("db.reportPublishedEvents query failed: $($_.Exception.Message)") }
        }
        # The delivery_report row can go stale after later draft revisions; the
        # acceptance evidence is the published revision itself. Downstream
        # W2-7/W2-8 must use this revision, never the row status.
        $reportRevision = $null
        $reportRevisionStatus = $null
        if ($versionId) {
            try {
                $revisionParts = (Db "select rev.revision||'|'||rev.status from delivery_report_revision rev join delivery_report rp on rp.id = rev.report_id where rp.version_id = $versionId and rev.status = 'published' order by rev.revision desc limit 1").Split('|')
                if ($revisionParts.Count -ge 2) {
                    $reportRevision = [int]$revisionParts[0]
                    $reportRevisionStatus = [string]$revisionParts[1]
                }
            } catch { $red.Add("db.reportRevision query failed: $($_.Exception.Message)") }
        }

        # -- base data fields ----------------------------------------------
        $environmentId = if ($base) { [string]$base.environmentId } else { '' }
        $sutId = if ($base) { [string]$base.sutId } else { '' }
        $repoIds = if ($base) { @($base.repoIds) } else { @() }
        $requirementIds = if ($base) { @($base.requirementIds) } else { @() }
        $passRequirementId = if ($base) { [string]$base.passRequirementId } else { '' }
        $failRequirementId = if ($base) { [string]$base.failRequirementId } else { '' }
        $caseIds = if ($base) { @($base.caseIds) } else { @() }
        $caseVersionIds = if ($base) { @($base.caseVersionIds) } else { @() }
        $stageAfterWave = if ($flowState -and $flowState.stageAfterWave) { [string]$flowState.stageAfterWave } else { $versionStage }
        if (-not $passRequirementId) { try { $passRequirementId = Db "select id from requirement where version_id = $versionId and constraints->>'role' = 'pass' order by id limit 1" } catch { } }
        if (-not $failRequirementId) { try { $failRequirementId = Db "select id from requirement where version_id = $versionId and constraints->>'role' = 'fail' order by id limit 1" } catch { } }

        # -- environment resource evidence (flow-created 4+4 plus later design
        #    proposals). The flow-created ids come from the flow; the counts are
        #    re-read from the final DB so the evidence reflects the surviving rows.
        $pageIdsForEvidence = @()
        $apiIdsForEvidence = @()
        if ($base -and $base.pageIds) { $pageIdsForEvidence = @($base.pageIds) }
        if ($pageIdsForEvidence.Count -eq 0 -and $flowState -and $flowState.pageIds) { $pageIdsForEvidence = @($flowState.pageIds) }
        if ($base -and $base.apiIds) { $apiIdsForEvidence = @($base.apiIds) }
        if ($apiIdsForEvidence.Count -eq 0 -and $flowState -and $flowState.apiIds) { $apiIdsForEvidence = @($flowState.apiIds) }
        if ($pageIdsForEvidence.Count -eq 0 -and $environmentId) {
            try { $pageIdsForEvidence = @(Parse-JsonArray (Db "select coalesce(json_agg(id order by id), '[]') from (select id from sut_page where environment_id = $environmentId and status = 'approved' order by id limit 4) t")) } catch { }
        }
        if ($apiIdsForEvidence.Count -eq 0 -and $environmentId) {
            try { $apiIdsForEvidence = @(Parse-JsonArray (Db "select coalesce(json_agg(id order by id), '[]') from (select id from sut_api where environment_id = $environmentId and status = 'approved' order by id limit 4) t")) } catch { }
        }
        $pageCounts = [ordered]@{ approved = 0; proposed = 0; rejected = 0; total = 0 }
        $apiCounts = [ordered]@{ approved = 0; proposed = 0; rejected = 0; total = 0 }
        if ($environmentId) {
            try {
                $pageCountObject = (Db "select json_build_object('approved', count(*) filter (where status = 'approved'), 'proposed', count(*) filter (where status = 'proposed'), 'rejected', count(*) filter (where status = 'rejected'), 'total', count(*))::text from sut_page where environment_id = $environmentId") | ConvertFrom-Json
                foreach ($key in 'approved', 'proposed', 'rejected', 'total') { $pageCounts[$key] = [int]$pageCountObject.$key }
            } catch { $red.Add("db.pageCounts query failed: $($_.Exception.Message)") }
            try {
                $apiCountObject = (Db "select json_build_object('approved', count(*) filter (where status = 'approved'), 'proposed', count(*) filter (where status = 'proposed'), 'rejected', count(*) filter (where status = 'rejected'), 'total', count(*))::text from sut_api where environment_id = $environmentId") | ConvertFrom-Json
                foreach ($key in 'approved', 'proposed', 'rejected', 'total') { $apiCounts[$key] = [int]$apiCountObject.$key }
            } catch { $red.Add("db.apiCounts query failed: $($_.Exception.Message)") }
        }
        $environmentResourceNote = "$($pageIdsForEvidence.Count)+$($apiIdsForEvidence.Count) flow-created approved; design host later proposed $($pageCounts['proposed']) pages/$($apiCounts['proposed']) APIs"

        # -- requirement / issue states (the W2-4/W2-5/W2-6 contract) --------
        $requirementRows = @()
        if ($versionId) {
            try { $requirementRows = @(Parse-JsonArray (Db "select coalesce(json_agg(json_build_object('id', id, 'status', status) order by id), '[]') from requirement where version_id = $versionId")) } catch { $red.Add("db.requirementStates query failed: $($_.Exception.Message)") }
        }
        $passRequirementStatus = [string]((($requirementRows | Where-Object { [string]$_.id -eq [string]$passRequirementId } | Select-Object -First 1).status))
        $failRequirementStatus = [string]((($requirementRows | Where-Object { [string]$_.id -eq [string]$failRequirementId } | Select-Object -First 1).status))
        $issueRows = @()
        if ($versionId) {
            try { $issueRows = @(Parse-JsonArray (Db "select coalesce(json_agg(json_build_object('id', id, 'status', status, 'severity', severity, 'category', category, 'source', source, 'merged_into_id', merged_into_id) order by id), '[]') from delivery_issue where version_id = $versionId")) } catch { $red.Add("db.issueStates query failed: $($_.Exception.Message)") }
        }
        $openIssueIds = @($issueRows | Where-Object { $_.status -eq 'open' } | ForEach-Object { [string]$_.id })

        # -- flow checks + DB assertions -----------------------------------
        if ($flowState -and $flowState.checks) {
            foreach ($property in $flowState.checks.PSObject.Properties) {
                Add-Assertion ("flow." + $property.Name) ([bool]$property.Value.ok) ([string]$property.Value.detail)
            }
        } else {
            Add-Assertion 'flow.checks' $false 'the flow wrote no state.checks'
        }
        Add-Assertion 'flow.exit' ($flowExit -eq 0) "flowExit=$flowExit"
        Add-Assertion 'db.requirements' ($dbCounters['requirements'] -ge 2) "requirements=$($dbCounters['requirements'])"
        Add-Assertion 'db.cases' ($dbCounters['cases'] -ge 2) "cases=$($dbCounters['cases'])"
        Add-Assertion 'db.caseVersionsConfirmed' ($dbCounters['caseVersionsConfirmed'] -ge 2) "confirmed case_versions=$($dbCounters['caseVersionsConfirmed'])"
        Add-Assertion 'db.planItems' ($dbCounters['planItems'] -ge 1) "planItems=$($dbCounters['planItems'])"
        Add-Assertion 'db.waves' ($dbCounters['waves'] -ge 1) "waves=$($dbCounters['waves'])"
        Add-Assertion 'db.caseRunsPassed' ($dbCounters['caseRunsPassed'] -ge 1) "case_run passed=$($dbCounters['caseRunsPassed'])"
        Add-Assertion 'db.caseRunsFailed' ($dbCounters['caseRunsFailed'] -ge 1) "case_run failed=$($dbCounters['caseRunsFailed'])"
        Add-Assertion 'db.evidence' ($dbCounters['evidenceRows'] -ge 1) "evidence=$($dbCounters['evidenceRows']) evidenceIds=$($evidenceIds.Count)"
        Add-Assertion 'db.issues' ($dbCounters['issues'] -ge 1 -and $issueIds.Count -ge 1) "delivery_issue=$($dbCounters['issues']) issueIds=$($issueIds -join ',')"
        Add-Assertion 'db.versionExecuting' ($versionStage -eq 'executing') "stage=$versionStage status=$versionStatus closed_at=$versionClosedAt (must stay executing)"
        Add-Assertion 'db.reportRevision' ($reportRevision -ge 1 -and $reportRevisionStatus -eq 'published') "report=$reportId kind=$reportKind reportRevision=$reportRevision reportRevisionStatus=$reportRevisionStatus delivery_report.status=$reportStatus (gate is the latest published revision; P4 may publish more than once on a slow/retried wave; downstream W2-7/8 use the data-verdict reportRevision)"
        Add-Assertion 'db.environmentResources' ($pageIdsForEvidence.Count -eq 4 -and $apiIdsForEvidence.Count -eq 4 -and $pageCounts['approved'] -ge 4 -and $apiCounts['approved'] -ge 4) "flowCreated pages=$($pageIdsForEvidence.Count) apis=$($apiIdsForEvidence.Count) ($($pageIdsForEvidence -join ',') / $($apiIdsForEvidence -join ',')); counts pages=approved:$($pageCounts['approved'])/proposed:$($pageCounts['proposed'])/total:$($pageCounts['total']) apis=approved:$($apiCounts['approved'])/proposed:$($apiCounts['proposed'])/total:$($apiCounts['total']) note=$environmentResourceNote"
        Add-Assertion 'db.requirementStates' ($passRequirementStatus -eq 'closed' -and $failRequirementStatus -eq 'clarified') "pass=$passRequirementId/$passRequirementStatus fail=$failRequirementId/$failRequirementStatus"
        Add-Assertion 'db.issueOpen' ($openIssueIds.Count -ge 1) "issues=$($issueRows.Count) open=$($openIssueIds -join ',')"
        # The brief lists a report, the W2-1 acceptance message makes it best
        # effort: a report is recorded when the P4 writer produced one, but its
        # absence does not fail the base data because W2-7/W2-8 own reports.
        # The published revision (not delivery_report.status) is the evidence.
        if ($reportRows.Count -ge 1 -and $reportPublishedRevisions -ge 1) {
            Add-Assertion 'db.reportPublished' $true "reports=$($dbCounters['reports']) reportId=$reportId kind=$reportKind reportRevision=$reportRevision reportRevisionStatus=$reportRevisionStatus delivery_report.status=$reportStatus publishedRevisions=$reportPublishedRevisions report.published events=$reportPublishedEvents (row status is not a gate)"
        } elseif ($reportRows.Count -ge 1) {
            Write-Host "   [INFO] db.reportPublished: report $reportId exists without a published revision yet (best effort)"
        } else {
            Write-Host '   [INFO] db.reportPublished: no report yet (best effort; W2-7/W2-8 create reports)'
        }

        # Re-read the event counter at the very end: the design host keeps
        # writing events during the verdict phase, so the earlier snapshot can
        # lag by one (observed 993 vs 994).
        try { $dbCounters['events'] = [int64](Db 'select count(*) from event') } catch { }

        # -- refresh base evidence from the final DB snapshot ----------------
        if ($base) {
            $baseOrdered = [ordered]@{}
            foreach ($property in $base.PSObject.Properties) {
                if ($property.Name -eq 'paperIds') { continue }
                $baseOrdered[$property.Name] = $property.Value
            }
            $baseOrdered['pageIds'] = @($pageIdsForEvidence | ForEach-Object { [string]$_ })
            $baseOrdered['apiIds'] = @($apiIdsForEvidence | ForEach-Object { [string]$_ })
            $baseOrdered['issueIds'] = @($issueIds | ForEach-Object { [string]$_ })
            $baseOrdered['issueRows'] = @($issueRows)
            $baseOrdered['issueIdsSource'] = 'final DB snapshot at verdict phase (post-triage; replaces any pre-triage candidate)'
            $baseOrdered['environmentPages'] = $pageCounts
            $baseOrdered['environmentApis'] = $apiCounts
            $baseOrdered['environmentResourceNote'] = $environmentResourceNote
            $baseOrdered['requirementStates'] = [ordered]@{
                pass = [ordered]@{ id = "$passRequirementId"; status = "$passRequirementStatus" }
                fail = [ordered]@{ id = "$failRequirementId"; status = "$failRequirementStatus" }
            }
            $baseOrdered['reportRevision'] = $reportRevision
            $baseOrdered['reportRevisionStatus'] = $reportRevisionStatus
            Write-Utf8 $baseDataPath ($baseOrdered | ConvertTo-Json -Depth 10)
            Write-Utf8 (Join-Path $runEvidence 'issues.json') (ConvertTo-Json -InputObject @($issueRows) -Depth 6)
            Write-Utf8 (Join-Path $runEvidence 'requirement-states.json') (([ordered]@{
                pass = [ordered]@{ id = "$passRequirementId"; status = "$passRequirementStatus" }
                fail = [ordered]@{ id = "$failRequirementId"; status = "$failRequirementStatus" }
            }) | ConvertTo-Json -Depth 5)
            Write-Utf8 (Join-Path $runEvidence 'environment-resources.json') (([ordered]@{
                pageIds = @($pageIdsForEvidence | ForEach-Object { [string]$_ })
                apiIds = @($apiIdsForEvidence | ForEach-Object { [string]$_ })
                pageCounts = $pageCounts
                apiCounts = $apiCounts
                note = $environmentResourceNote
            }) | ConvertTo-Json -Depth 5)
        }

        $status = if ($red.Count -eq 0) { 'PASS' } else { 'FAIL' }
        $verdict = [ordered]@{
            status = $status
            db = $dbName
            versionId = "$versionId"
            environmentId = "$environmentId"
            sutId = "$sutId"
            repoIds = @($repoIds | ForEach-Object { [string]$_ })
            requirementIds = @($requirementIds | ForEach-Object { [string]$_ })
            passRequirementId = "$passRequirementId"
            failRequirementId = "$failRequirementId"
            caseIds = @($caseIds | ForEach-Object { [string]$_ })
            caseVersionIds = @($caseVersionIds | ForEach-Object { [string]$_ })
            waveId = "$waveId"
            caseRunIds = [ordered]@{ passed = @($passedRunIds | ForEach-Object { [string]$_ }); failed = @($failedRunIds | ForEach-Object { [string]$_ }) }
            issueIds = @($issueIds | ForEach-Object { [string]$_ })
            issues = @($issueRows)
            reportId = $reportId
            reportKind = $reportKind
            reportStatus = $reportStatus
            reportRevision = $reportRevision
            reportRevisionStatus = $reportRevisionStatus
            reportRevisionNote = "W2-7/W2-8 must use revision $reportRevision ($reportRevisionStatus); delivery_report.status=$reportStatus is the later stale row and is not a gate."
            reportPublishedRevision = ($reportPublishedRevisions -ge 1)
            reportPublishedRevisions = $reportPublishedRevisions
            reportPublishedEvents = $reportPublishedEvents
            evidenceIds = @($evidenceIds | ForEach-Object { [string]$_ })
            pageIds = @($pageIdsForEvidence | ForEach-Object { [string]$_ })
            apiIds = @($apiIdsForEvidence | ForEach-Object { [string]$_ })
            environmentPages = [ordered]@{
                flowCreated = $pageIdsForEvidence.Count
                flowCreatedStatus = 'approved'
                approved = $pageCounts['approved']
                proposed = $pageCounts['proposed']
                rejected = $pageCounts['rejected']
                total = $pageCounts['total']
                note = $environmentResourceNote
            }
            environmentApis = [ordered]@{
                flowCreated = $apiIdsForEvidence.Count
                flowCreatedStatus = 'approved'
                approved = $apiCounts['approved']
                proposed = $apiCounts['proposed']
                rejected = $apiCounts['rejected']
                total = $apiCounts['total']
                note = $environmentResourceNote
            }
            requirementStates = [ordered]@{
                pass = [ordered]@{ id = "$passRequirementId"; status = "$passRequirementStatus" }
                fail = [ordered]@{ id = "$failRequirementId"; status = "$failRequirementStatus" }
            }
            downstreamNotes = @(
                "W2-7/W2-8: use report $reportId revision $reportRevision ($reportRevisionStatus), not delivery_report.status=$reportStatus",
                "W2-4/W2-5/W2-6: pass requirement $passRequirementId is $passRequirementStatus; fail requirement $failRequirementId is $failRequirementStatus; confirm the issue or create a manual issue (open issue ids: $($openIssueIds -join ','))",
                "environment: $environmentResourceNote (counts captured from the final DB at verdict time)"
            )
            stage = $versionStage
            versionStatus = $versionStatus
            apiBase = $apiBase
            webUrl = $webUrl
            executorUrl = $executorUrl
            llm = [ordered]@{ base = $proxyBase; upstream = $llmBase; model = $llmModel; visionBacked = 'proxy'; visionExercised = $false }
            dbCounters = $dbCounters
            assertions = $assertions
            assertionDetails = $details
            red = @($red)
            reportBestEffort = $true
            generatedAt = (Get-Date -Format o)
        }
        Write-Utf8 $verdictPath ($verdict | ConvertTo-Json -Depth 10)
        Write-Utf8 (Join-Path $runEvidence 'db-counters.json') ($dbCounters | ConvertTo-Json -Depth 4)
        Write-Host ''
        Write-Host "== W2 RED items ($($red.Count)):"
        foreach ($entry in $red) { Write-Host "   - $entry" }
        Write-Host ''
        Write-Host "== verdict: $status (evidence: $runEvidence)"
        Write-Host "== version=$versionId stage=$versionStage wave=$waveId passed=$($passedRunIds -join ',') failed=$($failedRunIds -join ',') issues=$($issueIds -join ',') report=$reportId"
        Write-Host "== db: requirements=$($dbCounters['requirements']) cases=$($dbCounters['cases']) planItems=$($dbCounters['planItems']) waves=$($dbCounters['waves']) caseRuns=$($dbCounters['caseRuns']) evidence=$($dbCounters['evidenceRows']) issues=$($dbCounters['issues'])"

        if ($status -ne 'PASS') {
            $exitCode = 1
        } else {
            Assert-DataVerdict
            Write-Host 'W2 BASE DATA: PASS'
            $exitCode = 0
        }
    }
} catch {
    if ($externalBlocked) {
        Write-Host 'EXTERNAL-BLOCKED'
        Write-Host "== EXTERNAL-BLOCKED: $blockedReason"
        $exitCode = 2
    } else {
        Write-Host "== W2 BASE DATA RED: $($_.Exception.Message)"
        $exitCode = 1
    }
} finally {
    if ($stackStarted) {
        Stop-E2EPorts
        Write-Host '== stopped W2 API/executor/web/vision-proxy (SUT left running)'
    }
}
exit $exitCode
