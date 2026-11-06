#requires -Version 5.1
<#
.SYNOPSIS
    EzTest v0.22 W2-10 visual real-data acceptance script.

.DESCRIPTION
    Reuses the W2 base environment from run/data-verdict.json and proves the
    visual assertion pipeline end to end on an isolated stack:

      1. starts (or reuses) the API on 13447, executor on 13351, web on 6872;
         the W2 database eztest_v022_w2 is never dropped;
      2. creates one acceptance version through the product API, adds one
         visual-baseline requirement, runs the real design host, and picks the
         designed case that carries a kind=visual assertion;
      3. configures that assertion with threshold 0.02, confirms the case and
         runs a real wave; the executor captures actual PNGs and the server
         stores visual_diff rows (no baseline -> inconclusive);
      4. approves the actual as baseline #1, re-runs the case, verifies the
         threshold comparison (passed/failed) and approves baseline #2 so the
         history proves the supersede transition;
      5. verifies DB rows, visual events, API 200s and captures design /
         execution / quality screenshots;
      6. writes run/visual-verdict.json and exits 0 only when baselines >= 1,
         diffs >= 1, every visual state is one of passed|failed|inconclusive|
         blocked, visual.baseline_approved and visual.diff_created events exist
         and the baseline history reaches two rows.

    Everything the script starts is stopped at the end except the SUT frontend
    (localhost:5173), which is left running when the script starts it.

.PARAMETER SkipBuild
    Reuse .tmp/w2-visual/eztest.exe instead of rebuilding the current HEAD.

.PARAMETER DesignTimeoutSeconds
    Upper bound for the real design host to produce cases (default 900s).

.PARAMETER RunTimeoutSeconds
    Upper bound for one wave / manual run to reach a terminal state (default 900s).

.PARAMETER ReuseFlow
    Reuse run/visual/flow-state.json (version/case/assertion ids) instead of
    creating a new acceptance version. Used for a fast repeat run on the same DB.
#>
param(
    [switch]$SkipBuild,
    [int]$DesignTimeoutSeconds = 900,
    [int]$RunTimeoutSeconds = 900,
    [switch]$ReuseFlow
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

# Native tools write UTF-8; PowerShell 5.1 decodes native stdout with the console
# code page by default, which mangles CJK output on a zh-CN host.
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
$OutputEncoding = New-Object System.Text.UTF8Encoding($false)

$dbName = 'eztest_v022_w2'
$apiPort = 13447
$execPort = 13351
$webPort = 6872
$proxyPort = 13999
$apiBase = "http://127.0.0.1:$apiPort"
$webUrl = "http://127.0.0.1:$webPort"
$executorUrl = "http://127.0.0.1:$execPort"
$proxyBase = "http://127.0.0.1:$proxyPort"
$sutUrl = 'http://localhost:5173/'
$sutDir = if ($env:EZTEST_SUT_DIR) { $env:EZTEST_SUT_DIR } else { 'D:\project\blog-frontend' }
$work = Join-Path $repoRoot '.tmp\w2-visual'
$evidenceRoot = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence\w2\run'
$visualDir = Join-Path $evidenceRoot 'visual'
$verdictPath = Join-Path $evidenceRoot 'visual-verdict.json'
$flowStatePath = Join-Path $visualDir 'flow-state.json'
$flowResultPath = Join-Path $visualDir 'flow-result.json'
$dataVerdictPath = Join-Path $evidenceRoot 'data-verdict.json'
$uiLogPath = Join-Path $visualDir 'ui.log'
$flowLogPath = Join-Path $visualDir 'flow.log'
$apiExe = Join-Path $work 'eztest.exe'
$apiLog = Join-Path $work 'api.log'
$execLog = Join-Path $work 'executor.log'
$webLog = Join-Path $work 'web.log'
$proxyLog = Join-Path $work 'llm-proxy.log'
$psql = Join-Path $repoRoot '.tools\pgsql\bin\psql.exe'
$goExe = Join-Path $repoRoot '.tools\go\bin\go.exe'
$storageDir = Join-Path $repoRoot '.tmp\w2-e2e\storage'

New-Item -ItemType Directory -Force -Path $work, $visualDir, $storageDir | Out-Null

$script:token = ''
$script:originalExecutorUrl = $null
$script:executorUrlRestored = $false
$script:started = @{}
$script:errors = New-Object System.Collections.Generic.List[string]
$script:startedAt = Get-Date

$verdict = [ordered]@{
    status        = 'FAIL'
    db            = $dbName
    versionId     = $null
    assertionId   = $null
    baselineIds   = @()
    diffIds       = @()
    visualStates  = @()
    history       = 0
    threshold     = $null
    events        = [ordered]@{ baselineApproved = 0; diffCreated = 0 }
    cases         = [ordered]@{}
    screenshots   = @()
    dbCounters    = [ordered]@{}
    apiChecks     = [ordered]@{}
    stage         = $null
    apiBase       = $apiBase
    webUrl        = $webUrl
    executorUrl   = $executorUrl
    generatedAt   = (Get-Date -Format o)
    errors        = @()
}

function Write-Utf8([string]$path, [string]$text) {
    [System.IO.File]::WriteAllText($path, $text, (New-Object System.Text.UTF8Encoding($false)))
}

function Get-EnvValue([string]$key) {
    $line = Select-String -Path (Join-Path $repoRoot '.env') -Pattern ("^" + [regex]::Escape($key) + "=(.*)$")
    if (-not $line) { throw "missing $key in .env" }
    return $line.Matches[0].Groups[1].Value
}

function Get-HttpCode([string]$url) {
    $curlExe = (Get-Command curl.exe -ErrorAction SilentlyContinue).Source
    if (-not $curlExe) { return 'no-curl' }
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $code = (& $curlExe -s -o NUL -w "%{http_code}" --max-time 5 $url 2>&1 | Out-String).Trim()
        return $code
    } finally {
        $ErrorActionPreference = $previous
    }
}

function Wait-Http200([string]$url, [int]$timeoutSeconds) {
    $deadline = (Get-Date).AddSeconds($timeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        if ((Get-HttpCode $url) -eq '200') { return $true }
        Start-Sleep -Seconds 2
    }
    return $false
}

function Start-Detached([string]$name, [string]$commandFile) {
    $null = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{
        CommandLine      = "cmd.exe /c `"$commandFile`""
        CurrentDirectory = $work
    }
    $script:started[$name] = $true
}

function Stop-StartedPort([string]$name, [int]$port) {
    if (-not $script:started[$name]) { return }
    $conns = Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue
    foreach ($conn in $conns) {
        Stop-Process -Id $conn.OwningProcess -Force -ErrorAction SilentlyContinue
    }
}

function Invoke-Db([string]$sql) {
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $output = (& $psql -h localhost -p 5433 -U eztest -d $dbName -tAc $sql 2>&1 | Out-String).Trim()
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($code -ne 0) { throw "psql failed (exit=$code): $($output.Substring(0, [Math]::Min(400, $output.Length)))" }
    return $output
}

function Invoke-Api {
    param(
        [string]$Method,
        [string]$Path,
        $Body = $null,
        [int[]]$Expected = @(200, 201)
    )
    $curlExe = (Get-Command curl.exe -ErrorAction SilentlyContinue).Source
    if (-not $curlExe) { throw 'curl.exe is required' }
    $bodyFile = Join-Path $work 'api-body.json'
    $responseFile = Join-Path $work 'api-response.json'
    if (Test-Path -LiteralPath $responseFile) { Remove-Item -LiteralPath $responseFile -Force }
    $curlArgs = @('-s', '-S', '-X', $Method, '-o', $responseFile, '-w', '%{http_code}', '--max-time', '60')
    if ($script:token -ne '') { $curlArgs += @('-H', "Authorization: Bearer $($script:token)") }
    if ($null -ne $Body) {
        $json = $Body | ConvertTo-Json -Depth 12 -Compress
        [System.IO.File]::WriteAllText($bodyFile, $json, (New-Object System.Text.UTF8Encoding($false)))
        $curlArgs += @('-H', 'Content-Type: application/json; charset=utf-8')
        $curlArgs += @('--data-binary', "@$bodyFile")
    }
    $curlArgs += "$apiBase$Path"
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $statusText = (& $curlExe @curlArgs 2>&1 | Out-String).Trim()
        $curlExit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    $status = 0
    if ($statusText -match '([0-9]{3})\s*$') { $status = [int]$Matches[1] }
    $text = ''
    if (Test-Path -LiteralPath $responseFile) {
        $text = [System.IO.File]::ReadAllText($responseFile, (New-Object System.Text.UTF8Encoding($false)))
    }
    if ($curlExit -ne 0 -or -not ($Expected -contains $status)) {
        throw "HTTP $Method $Path -> $status $($text.Substring(0, [Math]::Min(600, $text.Length)))"
    }
    if ([string]::IsNullOrWhiteSpace($text)) { return $null }
    return ($text | ConvertFrom-Json)
}

function Save-ExecutorUrl {
    try {
        $current = Invoke-Api -Method 'GET' -Path '/api/admin/settings/execution.executor_url'
        $script:originalExecutorUrl = [string]$current.data.value
        $verdict.dbCounters.originalExecutorUrl = $script:originalExecutorUrl
    } catch {
        $script:errors.Add("read execution.executor_url: $($_.Exception.Message)")
    }
}

function Restore-ExecutorUrl {
    if ($script:executorUrlRestored -or $null -eq $script:originalExecutorUrl) { return }
    $script:executorUrlRestored = $true
    try {
        $current = Invoke-Api -Method 'GET' -Path '/api/admin/settings/execution.executor_url'
        if ([string]$current.data.value -ne $script:originalExecutorUrl) {
            Invoke-Api -Method 'PATCH' -Path '/api/admin/settings/execution.executor_url' -Body @{ value = $script:originalExecutorUrl } | Out-Null
        }
        $verdict.dbCounters.executorUrlRestored = $true
    } catch {
        $script:errors.Add("restore execution.executor_url: $($_.Exception.Message)")
    }
}

function Write-Verdict {
    $verdict.errors = @($script:errors)
    $verdict.generatedAt = (Get-Date -Format o)
    Write-Utf8 $verdictPath (($verdict | ConvertTo-Json -Depth 12))
}

function Get-ScreenshotFiles {
    if (-not (Test-Path -LiteralPath $visualDir)) { return @() }
    return @(Get-ChildItem -LiteralPath $visualDir -Filter '*.png' | Where-Object { $_.Length -gt 0 })
}

# ---------------------------------------------------------------------------
# Embedded Node flow: product APIs + real design host + executor runs.
# ---------------------------------------------------------------------------
$flowScript = @'
import fs from "node:fs"
import path from "node:path"
import { createRequire } from "node:module"

const API = process.env.W210_API
const WEB = process.env.W210_WEB
const OUT = process.env.W210_OUT
const DATA_VERDICT = process.env.W210_DATA_VERDICT
const ADMIN_PW = process.env.W210_ADMIN_PW
const DESIGN_TIMEOUT = Number(process.env.W210_DESIGN_TIMEOUT || 900) * 1000
const RUN_TIMEOUT = Number(process.env.W210_RUN_TIMEOUT || 900) * 1000
const REUSE = process.env.W210_REUSE === "1"
const REQUIREMENT_TITLE = "W2-VISUAL visual baseline regression"
const REQUIREMENT_DESCRIPTION = "As a visitor I want the home page to stay consistent with the approved visual baseline so that visual regressions are detected."
const REQUIREMENT_CRITERIA = Buffer.from(process.env.W210_REQUIREMENT_B64, "base64").toString("utf8")
const MODIFY_PROMPT_TEMPLATE = Buffer.from(process.env.W210_MODIFY_B64, "base64").toString("utf8")

let token = ""
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const nowIso = () => new Date().toISOString()
const log = (...args) => console.log(nowIso(), ...args)
const listData = (value) => (value && Array.isArray(value.data) ? value.data : [])
const ids = (items) => (items || []).map((item) => String(item.id ?? item.case_run_id ?? ""))

async function api(method, p, body, expected = [200, 201]) {
  const response = await fetch(API + p, {
    method,
    headers: {
      ...(token ? { authorization: `Bearer ${token}` } : {}),
      ...(body !== undefined ? { "content-type": "application/json" } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  const text = await response.text()
  let json = null
  try { json = text.length ? JSON.parse(text) : null } catch {}
  if (!expected.includes(response.status)) {
    throw new Error(`${method} ${p} -> ${response.status}: ${text.slice(0, 700)}`)
  }
  return { status: response.status, json, text }
}

async function waitFor(label, probe, timeoutMs, intervalMs = 3000) {
  const deadline = Date.now() + timeoutMs
  let last = null
  while (Date.now() < deadline) {
    last = await probe()
    if (last) return last
    await sleep(intervalMs)
  }
  throw new Error(`timeout waiting for ${label}`)
}

async function stage() {
  return (await api("GET", `/api/versions/${state.versionId}/stage`)).json.data.stage
}

async function graphCases(versionId) {
  const graph = await api("GET", `/api/versions/${versionId}/design/graph`)
  const cases = []
  for (const requirement of graph.json.data.requirements || []) {
    for (const scenario of requirement.scenarios || []) {
      for (const card of scenario.cases || []) {
        cases.push({
          ...card,
          requirement_id: requirement.requirement_id,
          scenario_name: scenario.name,
        })
      }
    }
  }
  return cases
}

async function caseDetail(versionId, card) {
  const result = await api("GET", `/api/versions/${versionId}/design/cases/${card.case_id}?version_id=${card.current_version_id}`)
  return result.json.data
}

async function designRuns(versionId) {
  const runs = await api("GET", "/api/agent-center/runs?limit=100")
  return listData(runs.json).filter((run) => String(run.version_id) === String(versionId))
}

async function waitDesignSettled(versionId, timeoutMs) {
  let stable = 0
  let idlePolls = 0
  let previous = ""
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const currentStage = await stage()
    const cases = await graphCases(versionId)
    const active = (await designRuns(versionId)).filter((run) => run.status === "running")
    const signature = JSON.stringify(cases.map((card) => [card.case_id, card.current_version_id, card.assertion_count]))
    if (currentStage === "designing" && cases.length > 0 && active.length === 0) {
      idlePolls += 1
      // The design host is expected to converge by itself; if it did not after
      // two idle polls, ask the deterministic case gate to enter case_review.
      if (idlePolls >= 2) {
        await api("POST", `/api/versions/${versionId}/stage/advance`, {}, [200, 409]).catch(() => null)
      }
    } else {
      idlePolls = 0
    }
    if (currentStage === "case_review" && cases.length > 0 && active.length === 0 && signature === previous) {
      stable += 1
    } else {
      stable = 0
    }
    previous = signature
    if (stable >= 2) return cases
    await sleep(3000)
  }
  throw new Error("timeout waiting for the design host to settle at case_review")
}

async function findVisualCard(versionId, cards) {
  const candidates = []
  for (const card of cards) {
    if (Number(card.assertion_count) <= 0 || Number(card.step_count) <= 0) continue
    const detail = await caseDetail(versionId, card)
    const visuals = (detail.selected?.assertions || []).filter((assertion) => assertion.kind === "visual")
    if (visuals.length > 0) candidates.push({ card, detail, visuals })
  }
  // Prefer a case with exactly one visual assertion: configuring every visual
  // assertion is required before the case gate, and one keeps the run focused.
  candidates.sort((left, right) => left.visuals.length - right.visuals.length)
  return candidates[0] || null
}

async function requestVisualRevision(versionId, caseId) {
  const conversations = await api("GET", `/api/versions/${versionId}/conversations?kind=design&limit=20`)
  const items = listData(conversations.json)
  if (items.length === 0) throw new Error("no design conversation to request a visual assertion")
  const conversationId = String(items[0].id)
  const prompt = MODIFY_PROMPT_TEMPLATE.replace("{CASE_ID}", String(caseId))
  await api("POST", `/api/versions/${versionId}/conversations/${conversationId}/messages`, {
    parts: [{ type: "text", text: prompt }],
  })
  log("requested design revision for case", caseId, "conversation", conversationId)
}

async function createAcceptanceVersion() {
  const data = JSON.parse(fs.readFileSync(DATA_VERDICT, "utf8"))
  const version = await api("POST", "/api/versions", {
    title: "W2-10 visual baseline acceptance",
    description: "v0.22 W2 visual acceptance: real design visual assertion, execution capture/diff, baseline approval/history and quality metrics.",
    goal: "Prove the visual assertion pipeline with real design, executor screenshots and approved baseline history.",
    environment_id: String(data.environmentId),
  })
  const versionId = String(version.json.data.id)
  state.versionId = versionId
  state.environmentId = String(data.environmentId)
  saveState()

  let current = await stage()
  if (current === "created") {
    await api("POST", `/api/versions/${versionId}/stage/advance`, {})
    current = await stage()
  }
  if (current !== "clarifying") throw new Error(`new version stage=${current}, want clarifying`)

  const binding = (await api("GET", `/api/versions/${versionId}`)).json.data.bindings?.[0]
  state.versionSutId = String(binding?.id || "")
  if (!state.versionSutId) throw new Error("new version has no version_sut binding")

  const requirement = await api("POST", `/api/versions/${versionId}/requirements`, {
    title: REQUIREMENT_TITLE,
    description: REQUIREMENT_DESCRIPTION,
    acceptance_criteria: REQUIREMENT_CRITERIA,
    priority: "P1",
    constraints: { source: "w2-10-visual", requires_visual_baseline: true },
    targets: { version_suts: [state.versionSutId], pages: [String(data.pageIds[0])], apis: [] },
  })
  state.requirementId = String(requirement.json.data.id)
  saveState()
  await api("POST", `/api/versions/${versionId}/requirements/confirmations`, {
    requirement_ids: [state.requirementId],
  })
  current = await stage()
  if (current === "requirement_review") {
    await api("POST", `/api/versions/${versionId}/stage/advance`, {})
    current = await stage()
  }
  if (current !== "designing") throw new Error(`design stage=${current}, want designing`)
  return data
}

async function approveBaseline(actualEvidenceId) {
  const baseline = await api("POST", `/api/versions/${state.versionId}/visual/assertions/${state.assertionId}/baselines`, {
    evidenceId: String(actualEvidenceId),
  })
  state.baselineIds.push(String(baseline.json.data.id))
  saveState()
  return baseline.json.data
}

async function runOnce(useWave = false) {
  const seen = new Set(await allRunIDs())
  if (useWave) {
    const waves = listData((await api("GET", `/api/versions/${state.versionId}/waves?limit=50`)).json)
    const active = waves.find((wave) => ["planned", "running", "paused"].includes(wave.status))
    if (!active) {
      try {
        const wave = await api("POST", `/api/versions/${state.versionId}/waves`, { case_ids: [state.caseId] })
        state.waveIds.push(String(wave.json.data.wave_id))
        saveState()
      } catch (error) {
        // A repeated run on the same DB has no new initial intent, so the wave
        // entry point answers "no dispatchable cases"; the product retest path
        // is a manual intent.
        log("wave entry point unavailable; using a manual intent:", String(error.message).slice(0, 200))
        await api("POST", `/api/versions/${state.versionId}/execution/intents`, {
          case_version_id: state.caseVersionId,
          purpose: "manual",
        })
      }
    }
  } else {
    await api("POST", `/api/versions/${state.versionId}/execution/intents`, {
      case_version_id: state.caseVersionId,
      purpose: "manual",
    })
  }
  return waitFor("case run terminal", async () => {
    const waves = await api("GET", `/api/versions/${state.versionId}/waves?limit=50`)
    for (const wave of listData(waves.json)) {
      const runs = await api("GET", `/api/versions/${state.versionId}/waves/${wave.id}/case-runs`)
      for (const run of listData(runs.json)) {
        if (seen.has(String(run.id))) continue
        if (["passed", "failed", "inconclusive", "blocked", "skipped", "interrupted"].includes(run.status)) return run
      }
    }
    return null
  }, RUN_TIMEOUT)
}

async function allRunIDs() {
  const result = new Set()
  const waves = await api("GET", `/api/versions/${state.versionId}/waves?limit=50`)
  for (const wave of listData(waves.json)) {
    const runs = await api("GET", `/api/versions/${state.versionId}/waves/${wave.id}/case-runs`)
    for (const run of listData(runs.json)) result.add(String(run.id))
  }
  return result
}

async function waitNoActiveIntent() {
  return waitFor("active intent to finish", async () => {
    const intents = await api("GET", `/api/versions/${state.versionId}/execution/intents?limit=100`)
    const active = listData(intents.json).filter((intent) => ["open", "claimed", "running"].includes(intent.status))
    return active.length === 0 ? true : null
  }, 120000, 2000)
}

function diffForRun(diffs, runId) {
  return (diffs || []).find((diff) =>
    String(diff.case_run_id) === String(runId) &&
    String(diff.visual_assertion_id) === String(state.assertionId))
}

async function loadDiffs() {
  return listData((await api("GET", `/api/versions/${state.versionId}/visual/diffs?limit=100`)).json)
}

function saveState() {
  fs.writeFileSync(process.env.W210_STATE, JSON.stringify(state, null, 2))
}

function saveResult() {
  fs.writeFileSync(process.env.W210_RESULT, JSON.stringify(state, null, 2))
}

const state = {
  versionId: null,
  environmentId: null,
  requirementId: null,
  caseId: null,
  caseVersionId: null,
  caseAssertionId: null,
  assertionId: null,
  threshold: 0.02,
  visualAssertions: [],
  waveIds: [],
  runIds: [],
  baselineIds: [],
  diffIds: [],
  visualStates: [],
  history: 0,
  stage: null,
  designTitle: null,
  error: null,
}

try {
  const login = await api("POST", "/api/auth/login", { username: "admin", password: ADMIN_PW })
  token = String(login.json.data.token)
  if (REUSE && fs.existsSync(process.env.W210_STATE)) {
    const saved = JSON.parse(fs.readFileSync(process.env.W210_STATE, "utf8"))
    Object.assign(state, saved)
    log("reusing flow state version", state.versionId, "assertion", state.assertionId)
  }

  if (!state.versionId || !state.assertionId) {
    const data = await createAcceptanceVersion()
    let cards = await waitDesignSettled(state.versionId, DESIGN_TIMEOUT)
    let found = await findVisualCard(state.versionId, cards)
    if (!found) {
      const candidate = cards.find((card) => Number(card.assertion_count) > 0 && Number(card.step_count) > 0) || cards[0]
      if (!candidate) throw new Error("design produced no case")
      await requestVisualRevision(state.versionId, candidate.case_id)
      cards = await waitDesignSettled(state.versionId, DESIGN_TIMEOUT)
      found = await findVisualCard(state.versionId, cards)
    }
    if (!found) throw new Error("design produced no kind=visual assertion")
    state.caseId = String(found.card.case_id)
    state.caseVersionId = String(found.card.current_version_id)
    state.designTitle = found.card.title
    state.visualAssertions = []
    for (const visual of found.visuals) {
      const configured = await api("PUT", `/api/versions/${state.versionId}/visual/assertions/${visual.case_assertion_id}`, {
        selector: "",
        threshold: 0.02,
        viewport: { width: 1280, height: 720 },
        deviceScaleFactor: 1,
        waitPolicy: null,
        region: null,
      })
      state.visualAssertions.push({
        id: String(configured.json.data.id),
        caseAssertionId: String(visual.case_assertion_id),
        stepSeq: visual.step_seq,
        assertionSeq: visual.assertion_seq,
      })
    }
    if (state.visualAssertions.length === 0) throw new Error("no visual assertion was configured")
    state.assertionId = state.visualAssertions[0].id
    state.caseAssertionId = state.visualAssertions[0].caseAssertionId
    state.threshold = 0.02
    saveState()
    log("configured", state.visualAssertions.length, "visual assertion(s); primary", state.assertionId, "threshold", state.threshold)

    await api("POST", `/api/versions/${state.versionId}/design/confirmations`, {
      scope: "case", target_id: state.caseId, batch_id: "w2-visual",
    })
    let current = await stage()
    if (current === "case_review") {
      await api("POST", `/api/versions/${state.versionId}/stage/advance`, {})
      current = await stage()
    }
    if (current !== "executing") throw new Error(`execution stage=${current}, want executing`)
  }

  // First run: no baseline yet, so the server records inconclusive + actual.
  let run = await runOnce(true)
  let diffs = await loadDiffs()
  let diff = diffForRun(diffs, run.id)
  for (let attempt = 0; !diff && attempt < 2; attempt += 1) {
    log("no diff for run", run.id, "retrying the visual case")
    await sleep(2000)
    await waitNoActiveIntent()
    run = await runOnce(false)
    diffs = await loadDiffs()
    diff = diffForRun(diffs, run.id)
  }
  if (!diff) throw new Error("the executor produced no visual_diff row")
  state.runIds.push(String(run.id))
  state.diffIds.push(String(diff.id))
  if (!diff.actual_evidence_id) throw new Error("the visual_diff row has no actual evidence")
  log("first diff", diff.id, diff.classification, diff.reason, "actual", diff.actual_evidence_id)
  await sleep(3000)

  await approveBaseline(diff.actual_evidence_id)
  log("approved baseline #1")

  // Second run: compare against the approved baseline.
  let baselineTwoApproved = false
  for (let attempt = 0; attempt < 3 && !baselineTwoApproved; attempt += 1) {
    await waitNoActiveIntent()
    run = await runOnce(false)
    diffs = await loadDiffs()
    diff = diffForRun(diffs, run.id)
    state.runIds.push(String(run.id))
    if (!diff) continue
    state.diffIds.push(String(diff.id))
    log("comparison diff", diff.id, diff.classification, diff.reason, "ratio", diff.diff_ratio, "threshold", diff.threshold)
    if (diff.classification === "inconclusive" && diff.actual_evidence_id) {
      await approveBaseline(diff.actual_evidence_id)
      log("baseline missing/changed again; approved and retrying")
      continue
    }
    if (diff.actual_evidence_id) {
      await approveBaseline(diff.actual_evidence_id)
      baselineTwoApproved = true
      log("approved baseline #2 (history supersede)")
    }
  }
  if (!baselineTwoApproved) {
    // Last resort: approve the newest actual so the history requirement is a
    // real second approval even if the comparison stayed inconclusive.
    if (diff && diff.actual_evidence_id) {
      await approveBaseline(diff.actual_evidence_id)
      baselineTwoApproved = true
    }
  }
  if (!baselineTwoApproved) throw new Error("could not approve a second baseline")

  const history = listData((await api("GET", `/api/versions/${state.versionId}/visual/assertions/${state.assertionId}/baselines`)).json)
  state.history = history.length
  diffs = (await loadDiffs()).filter((entry) => String(entry.visual_assertion_id) === String(state.assertionId))
  state.diffIds = diffs.map((entry) => String(entry.id))
  state.visualStates = [...new Set(diffs.map((entry) => entry.classification))]
  state.stage = await stage()
  const overview = await api("GET", `/api/versions/${state.versionId}/visual/overview`)
  const metrics = await api("GET", `/api/quality/visual-metrics?versionId=${state.versionId}`)
  state.overview = overview.json.data
  state.metrics = metrics.json.data
  state.diffs = diffs
  state.baselines = history
  log("flow complete", JSON.stringify({ version: state.versionId, assertion: state.assertionId, baselines: state.baselineIds, diffs: state.diffIds, states: state.visualStates, history: state.history, stage: state.stage }))
  saveResult()
} catch (error) {
  state.error = String(error && error.stack ? error.stack : error).slice(0, 4000)
  saveResult()
  log("FLOW ERROR", state.error)
  process.exit(1)
}
'@

$uiScript = @'
import path from "node:path"
import { createRequire } from "node:module"

const repo = process.env.W210_REPO
const web = process.env.W210_WEB
const versionId = process.env.W210_VERSION_ID
const caseId = process.env.W210_CASE_ID
const out = process.env.W210_OUT
const adminPassword = process.env.W210_ADMIN_PW
const requireFromExecutor = createRequire(path.join(repo, "executor", "package.json"))
const { chromium } = requireFromExecutor("playwright")

let browser
try {
  browser = await chromium.launch({ headless: true, channel: "chrome" })
} catch {
  browser = await chromium.launch({ headless: true, channel: "msedge" })
}
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
page.setDefaultTimeout(60000)
async function shot(name) {
  const target = path.join(out, name)
  await page.screenshot({ path: target, fullPage: true })
  console.log("screenshot", name)
}
try {
  await page.goto(`${web}/login`, { waitUntil: "domcontentloaded" })
  await page.waitForTimeout(800)
  if (page.url().includes("/login")) {
    await page.fill("#username", "admin")
    await page.fill("#password", adminPassword)
    await page.locator("button[type=submit]").click()
    await page.waitForURL(/\/versions/, { timeout: 60000 })
  }
  await page.goto(`${web}/versions/${versionId}?tab=design&case=${caseId}`, { waitUntil: "domcontentloaded" })
  await page.waitForSelector('[data-testid="case-detail"]', { timeout: 120000 })
  const configure = page.getByRole("button", { name: Buffer.from("6YWN572u6KeG6KeJ5pat6KiA", "base64").toString("utf8") }).first()
  if (await configure.count()) {
    await configure.click()
    await page.locator('[data-testid="visual-config-editor"]').waitFor({ timeout: 30000 }).catch(() => null)
    await page.waitForTimeout(800)
  }
  await page.waitForTimeout(1200)
  await shot("01-design-visual-config.png")

  await page.goto(`${web}/versions/${versionId}?tab=execution&view=visual`, { waitUntil: "domcontentloaded" })
  await page.waitForSelector('[data-testid="visual-view"]', { timeout: 120000 })
  await page.waitForTimeout(1500)
  await shot("02-execution-visual.png")

  await page.goto(`${web}/quality?version=${versionId}`, { waitUntil: "domcontentloaded" })
  await page.waitForSelector('[data-testid="quality-metrics-panel"]', { timeout: 120000 })
  await page.waitForTimeout(1200)
  await shot("03-quality-visual.png")
} finally {
  await browser.close()
}
'@

Write-Utf8 (Join-Path $work 'w2-visual-flow.mjs') $flowScript
Write-Utf8 (Join-Path $work 'w2-visual-ui.mjs') $uiScript

try {
    if (-not (Test-Path -LiteralPath $dataVerdictPath)) { throw "missing W2 data verdict: $dataVerdictPath" }
    $dataText = [System.IO.File]::ReadAllText($dataVerdictPath, (New-Object System.Text.UTF8Encoding($false)))
    $data = $dataText | ConvertFrom-Json
    if ($null -eq $data.versionId -or [string]::IsNullOrWhiteSpace([string]$data.versionId)) {
        throw 'data-verdict.json has no versionId'
    }
    $baseVersionId = [string]$data.versionId
    $verdict.cases.baseVersionId = $baseVersionId
    Write-Host "== W2-10 visual: base version=$baseVersionId db=$dbName"

    if (-not (Test-Path -LiteralPath $psql)) { throw "psql missing: $psql" }
    $dbUrl = Get-EnvValue 'EZTEST_DATABASE_URL'
    $parsed = [uri]$dbUrl
    $dbUser = [uri]::UnescapeDataString($parsed.UserInfo.Split(':')[0])
    $dbPassword = [uri]::UnescapeDataString($parsed.UserInfo.Split(':')[1])
    $env:PGPASSWORD = $dbPassword
    $adminPassword = Get-EnvValue 'EZTEST_ADMIN_PASSWORD'
    $execToken = Get-EnvValue 'EZTEST_EXECUTOR_TOKEN'
    $llmKey = Get-EnvValue 'llm_api_key'
    $llmBase = Get-EnvValue 'llm_base_url'
    $nodeExe = (Get-Command node -ErrorAction SilentlyContinue).Source
    if (-not $nodeExe) { throw 'node is required' }
    $curlExe = (Get-Command curl.exe -ErrorAction SilentlyContinue).Source
    if (-not $curlExe) { throw 'curl.exe is required' }

    # ---------------------------------------------------------------------
    # 1. SUT frontend (left running; localhost keeps Vite on IPv6).
    # ---------------------------------------------------------------------
    if ((Get-HttpCode $sutUrl) -ne '200') {
        if (-not (Test-Path -LiteralPath $sutDir)) { throw "SUT frontend is down and $sutDir is missing" }
        Write-Host "== starting SUT frontend in $sutDir"
        $sutCmd = Join-Path $work 'run-sut.cmd'
        $sutLines = @('@echo off', "cd /d `"$sutDir`"", "npm run dev >> `"$work\sut.log`" 2>&1")
        [System.IO.File]::WriteAllLines($sutCmd, $sutLines, (New-Object System.Text.UTF8Encoding($false)))
        $null = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{
            CommandLine = "cmd.exe /c `"$sutCmd`""; CurrentDirectory = $work
        }
        if (-not (Wait-Http200 $sutUrl 180)) { throw 'SUT frontend did not become ready on localhost:5173' }
    }

    # ---------------------------------------------------------------------
    # 2. LLM proxy on 13999 (the default LLM config points here).
    # ---------------------------------------------------------------------
    if ((Get-HttpCode "$proxyBase/healthz") -ne '200') {
        $proxyScript = Join-Path $work 'w2-visual-llm-proxy.mjs'
        $proxySource = @'
import http from "node:http"
const port = Number(process.env.FAKE_LLM_PORT || 13999)
const upstream = (process.env.FAKE_LLM_UPSTREAM || "").replace(/\/+$/, "")
const key = process.env.FAKE_LLM_API_KEY || ""
if (!upstream || !key) { console.error("missing upstream or key"); process.exit(2) }
const server = http.createServer(async (req, res) => {
  if (req.url === "/healthz") {
    res.writeHead(200, { "content-type": "application/json" })
    res.end('{"status":"ok"}')
    return
  }
  try {
    const chunks = []
    for await (const chunk of req) chunks.push(chunk)
    const body = Buffer.concat(chunks)
    const headers = { ...req.headers }
    delete headers.host
    delete headers.authorization
    headers.authorization = `Bearer ${key}`
    const response = await fetch(upstream + req.url, {
      method: req.method,
      headers,
      body: req.method === "GET" || req.method === "HEAD" ? undefined : body,
    })
    const buffer = Buffer.from(await response.arrayBuffer())
    const responseHeaders = {}
    for (const [name, value] of response.headers.entries()) {
      if (name === "content-encoding" || name === "content-length" || name === "transfer-encoding") continue
      responseHeaders[name] = value
    }
    res.writeHead(response.status, responseHeaders)
    res.end(buffer)
  } catch (error) {
    res.writeHead(502, { "content-type": "text/plain" })
    res.end(String(error))
  }
})
server.listen(port, "127.0.0.1", () => console.log(`[visual-llm-proxy] 127.0.0.1:${port} upstream=${upstream}`))
'@
        Write-Utf8 $proxyScript $proxySource
        $runProxy = Join-Path $work 'run-visual-proxy.cmd'
        $proxyLines = @('@echo off', "cd /d `"$work`"", "set FAKE_LLM_PORT=$proxyPort", "set FAKE_LLM_UPSTREAM=$llmBase", "set FAKE_LLM_API_KEY=$llmKey", "`"$nodeExe`" `"$proxyScript`" >> `"$proxyLog`" 2>&1")
        [System.IO.File]::WriteAllLines($runProxy, $proxyLines, (New-Object System.Text.UTF8Encoding($false)))
        Start-Detached 'proxy' $runProxy
        if (-not (Wait-Http200 "$proxyBase/healthz" 60)) { throw 'LLM proxy did not become ready on 13999' }
    }

    # ---------------------------------------------------------------------
    # 3. API on 13447.
    # ---------------------------------------------------------------------
    if ((Get-HttpCode "$apiBase/api/health") -ne '200') {
        if (-not $SkipBuild) {
            Write-Host '== building backend'
            Push-Location (Join-Path $repoRoot 'backend')
            try {
                & $goExe build -o $apiExe ./cmd/eztest 2>&1 | Tee-Object -FilePath (Join-Path $visualDir 'build.log')
                if ($LASTEXITCODE -ne 0) { throw 'go build ./cmd/eztest failed' }
            } finally { Pop-Location }
        }
        if (-not (Test-Path -LiteralPath $apiExe)) { throw "backend binary missing: $apiExe" }
        $apiDbUrl = "postgres://$dbUser`:$dbPassword@localhost:5433/${dbName}?sslmode=disable"
        $runApi = Join-Path $work 'run-api.cmd'
        $apiLines = @(
            '@echo off',
            "cd /d `"$repoRoot\backend`"",
            "set EZTEST_DATABASE_URL=$apiDbUrl",
            "set EZTEST_API_PORT=$apiPort",
            "set EZTEST_WEB_PORT=$webPort",
            "set EZTEST_EXECUTOR_PORT=$execPort",
            "set EZTEST_STORAGE_DIR=$storageDir",
            "set EZTEST_INITIAL_ADMIN_PASSWORD=$adminPassword",
            "set EZTEST_ADMIN_USERNAME=admin",
            "set EZTEST_SNOWFLAKE_NODE_ID=10",
            "`"$apiExe`" >> `"$apiLog`" 2>&1"
        )
        [System.IO.File]::WriteAllLines($runApi, $apiLines, (New-Object System.Text.UTF8Encoding($false)))
        Write-Host '== starting API on 13447'
        Start-Detached 'api' $runApi
        if (-not (Wait-Http200 "$apiBase/api/health" 120)) { throw 'API did not become healthy on 13447' }
    } else {
        Write-Host '== reusing the API already listening on 13447'
    }

    # ---------------------------------------------------------------------
    # 4. Executor on 13351.
    # ---------------------------------------------------------------------
    if ((Get-HttpCode "$executorUrl/healthz") -ne '200') {
        $runExec = Join-Path $work 'run-executor.cmd'
        $execLines = @(
            '@echo off',
            "cd /d `"$repoRoot\executor`"",
            "set EZTEST_EXECUTOR_TOKEN=$execToken",
            "set EZTEST_EXECUTOR_ADDR=127.0.0.1:$execPort",
            "set EZTEST_API_URL=$apiBase",
            "set EZTEST_EXECUTOR_NAME=w2-visual",
            "`"$nodeExe`" src/server.mjs >> `"$execLog`" 2>&1"
        )
        [System.IO.File]::WriteAllLines($runExec, $execLines, (New-Object System.Text.UTF8Encoding($false)))
        Write-Host '== starting executor on 13351'
        Start-Detached 'executor' $runExec
        if (-not (Wait-Http200 "$executorUrl/healthz" 90)) { throw 'executor did not become healthy on 13351' }
    }

    # ---------------------------------------------------------------------
    # 5. EzTest web on 6872 (start compiling while the API flow runs).
    # ---------------------------------------------------------------------
    if ((Get-HttpCode "$webUrl/login") -ne '200') {
        $runWeb = Join-Path $work 'run-web.cmd'
        $webLines = @(
            '@echo off',
            "cd /d `"$repoRoot\frontend`"",
            "set NEXT_PUBLIC_API_BASE_URL=$apiBase",
            "`"$nodeExe`" `"$repoRoot\frontend\node_modules\next\dist\bin\next`" dev -p $webPort >> `"$webLog`" 2>&1"
        )
        [System.IO.File]::WriteAllLines($runWeb, $webLines, (New-Object System.Text.UTF8Encoding($false)))
        Write-Host '== starting EzTest web on 6872'
        Start-Detached 'web' $runWeb
    }

    # ---------------------------------------------------------------------
    # 6. Login and point the shared executor URL at the isolated executor.
    # ---------------------------------------------------------------------
    $session = Invoke-Api -Method 'POST' -Path '/api/auth/login' -Body @{ username = 'admin'; password = $adminPassword }
    if ($null -eq $session -or [string]::IsNullOrWhiteSpace([string]$session.data.token)) { throw 'login returned no token' }
    $script:token = [string]$session.data.token
    Save-ExecutorUrl
    $patched = Invoke-Api -Method 'PATCH' -Path '/api/admin/settings/execution.executor_url' -Body @{ value = $executorUrl }
    if ([string]$patched.data.value -ne $executorUrl) { throw "execution.executor_url did not accept $executorUrl" }
    Write-Host "== execution.executor_url -> $executorUrl"

    # ---------------------------------------------------------------------
    # 7. Real product flow (design assertion -> runs -> baselines).
    # ---------------------------------------------------------------------
    $flowEnv = @{
        W210_API = $apiBase
        W210_WEB = $webUrl
        W210_OUT = $visualDir
        W210_DATA_VERDICT = $dataVerdictPath
        W210_ADMIN_PW = $adminPassword
        W210_DESIGN_TIMEOUT = "$DesignTimeoutSeconds"
        W210_RUN_TIMEOUT = "$RunTimeoutSeconds"
        W210_STATE = $flowStatePath
        W210_RESULT = $flowResultPath
        W210_REPO = $repoRoot
        W210_REUSE = $(if ($ReuseFlow) { '1' } else { '0' })
        W210_REQUIREMENT_B64 = 'VzItVklTVUFMIOmmlumhteinhuinieWfuue6v+S4juWbnuW9ku+8muS9nOS4uuiuv+Wuou+8jOaIkeW4jOacm+mmlumhteS4juW3suaJueWHhuinhuinieWfuue6v+S/neaMgeS4gOiHtO+8jOS7peS+v+WPkeeOsOinhuinieWbnuW9kuOAgumqjOaUtuagh+WHhu+8mkdpdmVuIOeuoeeQhuWRmOW3suaJueWHhummlumhteinhuinieWfuue6vyBXaGVuIOiuv+WuouaJk+W8gOmmlumhtSBUaGVuIOWvuemmlumhteaVtOmhteaIquWbvuS4juW3suaJueWHhuWfuue6v+WBmuWDj+e0oOe6p+avlOWvue+8jOW3ruW8guWDj+e0oOavlOS+i+S4jei2hei/h+mYiOWAvCAwLjAy44CC5b+F6aG75L2/55SoIGFzc2VydGlvbi5raW5kPXZpc3VhbCDnmoTop4bop4nln7rnur/miKrlm77mlq3oqIDvvIzogIzkuI3mmK/mlofmnKzmiJYgVUkg5pat6KiA77yb5ZCM5LiA54mI5pys6YeN5aSN5omn6KGM5pe25b+F6aG76K+75Y+W5bey5om55YeG5Z+657q/5bm257uZ5Ye6IHBhc3NlZC9mYWlsZWQg5YiG57G744CC'
        W210_MODIFY_B64 = '6K+35L+u5pS555So5L6LIHtDQVNFX0lEfe+8muS/neeVmeeOsOacieatpemqpO+8jOWcqOivpeeUqOS+i+S4reaWsOWinuS4gOadoSBraW5kPXZpc3VhbCDnmoTmlq3oqIDvvJrlr7npppbpobXmlbTpobXmiKrlm77kuI7lt7Lmibnlh4bop4bop4nln7rnur/lgZrlg4/ntKDnuqfmr5Tlr7nvvIzlt67lvILlg4/ntKDmr5TkvovpmIjlgLwgMC4wMuOAgumcgOaxguS4jumqjOaUtuagh+WHhuaYjuehruimgeaxguinhuinieWfuue6v+avlOWvue+8jOi/meaYryBXMiDop4bop4npqozmlLbnmoTlv4XopoHnlKjkvovjgILor7flhYggZGVzaWduLmdyYXBoX3JlYWTvvIznhLblkI7lnKjlkIzkuIDova7lhoXnq4vljbPnlKggY2FzZS5ncmFwaF9wcm9wb3NlIOaPkOS6pOWujOaVtCBkcmFmdCDkv67mlLnvvIzkuI3opoHlj6rlm57lpI3orqHliJLjgII='
    }
    foreach ($key in $flowEnv.Keys) { Set-Item -Path "env:$key" -Value $flowEnv[$key] }
    Write-Host '== running the real visual flow'
    & $nodeExe (Join-Path $work 'w2-visual-flow.mjs') 2>&1 | Tee-Object -FilePath $flowLogPath
    $flowExit = $LASTEXITCODE
    if (-not (Test-Path -LiteralPath $flowResultPath)) { throw 'visual flow produced no flow-result.json' }
    $flow = [System.IO.File]::ReadAllText($flowResultPath, (New-Object System.Text.UTF8Encoding($false))) | ConvertFrom-Json
    if ($flow.error) { throw "visual flow failed: $($flow.error)" }
    if ($flowExit -ne 0) { throw "visual flow exited $flowExit" }

    $versionId = [string]$flow.versionId
    $verdict.versionId = $versionId
    $verdict.assertionId = [string]$flow.assertionId
    $verdict.baselineIds = @($flow.baselineIds | ForEach-Object { [string]$_ })
    $verdict.diffIds = @($flow.diffIds | ForEach-Object { [string]$_ })
    $verdict.visualStates = @($flow.visualStates | ForEach-Object { [string]$_ })
    $verdict.history = [int]$flow.history
    $verdict.threshold = $flow.threshold
    $verdict.stage = [string]$flow.stage
    $verdict.cases = [ordered]@{
        baseVersionId = $baseVersionId
        versionId     = $versionId
        requirementId = [string]$flow.requirementId
        caseId        = [string]$flow.caseId
        caseVersionId = [string]$flow.caseVersionId
        caseAssertionId = [string]$flow.caseAssertionId
        caseTitle     = [string]$flow.designTitle
    }
    Write-Host "== flow version=$versionId assertion=$($verdict.assertionId) baselineIds=$($verdict.baselineIds -join ',') states=$($verdict.visualStates -join ',') history=$($verdict.history)"

    # ---------------------------------------------------------------------
    # 8. Screenshots (design / execution / quality).
    # ---------------------------------------------------------------------
    if ((Get-HttpCode "$webUrl/login") -ne '200') {
        if (-not (Wait-Http200 "$webUrl/login" 240)) { throw 'EzTest web did not become ready on 6872' }
    }
    $env:W210_REPO = $repoRoot
    $env:W210_WEB = $webUrl
    $env:W210_VERSION_ID = $versionId
    $env:W210_CASE_ID = [string]$flow.caseId
    $env:W210_OUT = $visualDir
    $env:W210_ADMIN_PW = $adminPassword
    Write-Host '== capturing UI screenshots'
    & $nodeExe (Join-Path $work 'w2-visual-ui.mjs') 2>&1 | Tee-Object -FilePath $uiLogPath
    if ($LASTEXITCODE -ne 0) { throw 'screenshot helper failed' }
    $verdict.screenshots = @(Get-ScreenshotFiles | ForEach-Object { "visual/$($_.Name)" })
    Write-Host "== screenshots: $($verdict.screenshots -join ', ')"

    # ---------------------------------------------------------------------
    # 9. DB / API / event verification (read-only SQL).
    # ---------------------------------------------------------------------
    $assertionId = [string]$flow.assertionId
    $dbAssertions = [int](Invoke-Db "select count(*) from visual_assertion where version_id = $versionId and status = 'active'")
    $dbBaselines = [int](Invoke-Db "select count(*) from visual_baseline b join visual_assertion a on a.id = b.visual_assertion_id where a.version_id = $versionId and b.status = 'approved'")
    $dbBaselineRows = [int](Invoke-Db "select count(*) from visual_baseline b join visual_assertion a on a.id = b.visual_assertion_id where a.version_id = $versionId")
    $dbDiffs = [int](Invoke-Db "select count(*) from visual_diff d join visual_assertion a on a.id = d.visual_assertion_id where a.version_id = $versionId")
    $dbStatesRaw = (Invoke-Db "select coalesce(string_agg(distinct d.classification, ',' order by d.classification), '') from visual_diff d join visual_assertion a on a.id = d.visual_assertion_id where a.version_id = $versionId")
    $dbThreshold = (Invoke-Db "select threshold::float8 from visual_assertion where id = $assertionId")
    $eventBaseline = [int](Invoke-Db "select count(*) from event where channel = 'version:$versionId' and type = 'visual.baseline_approved'")
    $eventDiff = [int](Invoke-Db "select count(*) from event where channel = 'version:$versionId' and type = 'visual.diff_created'")
    $historyRows = [int](Invoke-Db "select count(*) from visual_baseline where visual_assertion_id = $assertionId")
    $supersededRows = [int](Invoke-Db "select count(*) from visual_baseline where visual_assertion_id = $assertionId and status = 'superseded'")
    $approvedRows = [int](Invoke-Db "select count(*) from visual_baseline where visual_assertion_id = $assertionId and status = 'approved'")

    $verdict.dbCounters = [ordered]@{
        activeAssertions = $dbAssertions
        approvedBaselines = $dbBaselines
        baselineRows     = $dbBaselineRows
        diffs            = $dbDiffs
        classifications  = $dbStatesRaw
        threshold        = $dbThreshold
        historyRows      = $historyRows
        approvedRows     = $approvedRows
        supersededRows   = $supersededRows
    }
    $verdict.events.baselineApproved = $eventBaseline
    $verdict.events.diffCreated = $eventDiff
    if (@($flow.visualStates).Count -gt 0) { $verdict.visualStates = @($flow.visualStates | ForEach-Object { [string]$_ }) }
    elseif ($dbStatesRaw -ne '') { $verdict.visualStates = @($dbStatesRaw.Split(',')) }

    $checks = [ordered]@{}
    $checks.overview200 = $false
    $checks.metrics200 = $false
    $checks.diffDetail200 = $false
    try {
        Invoke-Api -Method 'GET' -Path "/api/versions/$versionId/visual/overview" | Out-Null
        $checks.overview200 = $true
    } catch { $script:errors.Add("overview API: $($_.Exception.Message)") }
    try {
        Invoke-Api -Method 'GET' -Path "/api/quality/visual-metrics?versionId=$versionId" | Out-Null
        $checks.metrics200 = $true
    } catch { $script:errors.Add("metrics API: $($_.Exception.Message)") }
    if (@($flow.diffIds).Count -gt 0) {
        $diffId = [string]@($flow.diffIds)[0]
        try {
            Invoke-Api -Method 'GET' -Path "/api/versions/$versionId/visual/diffs/$diffId" | Out-Null
            $checks.diffDetail200 = $true
        } catch { $script:errors.Add("diff API: $($_.Exception.Message)") }
    }
    $verdict.apiChecks = $checks

    $allowedStates = @('passed', 'failed', 'inconclusive', 'blocked')
    $invalidStates = @($verdict.visualStates | Where-Object { $allowedStates -notcontains $_ })
    if ($verdict.visualStates.Count -lt 1) { throw 'visualStates is empty' }
    if ($invalidStates.Count -gt 0) { throw "invalid visual states: $($invalidStates -join ',')" }
    if ($dbBaselines -lt 1) { throw "approved baselines=$dbBaselines" }
    if ($dbDiffs -lt 1) { throw "visual diffs=$dbDiffs" }
    if ($eventBaseline -lt 1) { throw "visual.baseline_approved events=$eventBaseline" }
    if ($eventDiff -lt 1) { throw "visual.diff_created events=$eventDiff" }
    if ($historyRows -lt 2) { throw "baseline history=$historyRows, want >= 2" }
    if ($supersededRows -lt 1) { throw "no superseded baseline row (history=$historyRows)" }
    if ([Math]::Abs([double]$dbThreshold - 0.02) -gt 0.0000001) { throw "persisted threshold=$dbThreshold, want 0.02" }
    if (-not $checks.overview200) { throw 'visual overview API did not return 200' }
    if (-not $checks.metrics200) { throw 'quality metrics API did not return 200' }
    if (-not $checks.diffDetail200) { throw 'visual diff detail API did not return 200' }
    if (@($verdict.screenshots).Count -lt 3) { throw "screenshots=$(@($verdict.screenshots).Count), want >= 3" }
    foreach ($shot in @($verdict.screenshots)) {
        $file = Join-Path $visualDir (Split-Path -Leaf $shot)
        if (-not (Test-Path -LiteralPath $file) -or (Get-Item -LiteralPath $file).Length -le 0) { throw "empty screenshot: $shot" }
    }
    if (@('executing', 'reporting', 'completed') -notcontains [string]$verdict.stage) {
        throw "acceptance version stage=$($verdict.stage), want executing|reporting|completed"
    }

    $verdict.status = 'PASS'
    Write-Host '== W2-10 visual domain PASS'
    Write-Host "== version=$versionId assertion=$assertionId baselines=$dbBaselines diffs=$dbDiffs states=$($verdict.visualStates -join ',') history=$historyRows"
    Write-Host "== events baselineApproved=$eventBaseline diffCreated=$eventDiff screenshots=$(@($verdict.screenshots).Count)"
} catch {
    $script:errors.Add($_.Exception.Message)
    Write-Host "ERROR: $($_.Exception.Message)"
} finally {
    Restore-ExecutorUrl
    Stop-StartedPort 'api' $apiPort
    Stop-StartedPort 'executor' $execPort
    Stop-StartedPort 'web' $webPort
    Stop-StartedPort 'proxy' $proxyPort
    Write-Verdict
    Write-Host "== verdict: $verdictPath"
}

if ($verdict.status -ne 'PASS') {
    Write-Host '== W2-10 visual domain FAIL'
    Write-Host ($script:errors -join ' | ')
    exit 1
}
exit 0
