#requires -Version 5.1
<#
.SYNOPSIS
    EzTest v0.22 FA-5: the 12 typical requirements end to end on the FA stack.

.DESCRIPTION
    Drives one real version through the EzTest pipeline against the real SUT
    (D:\project, frontend :5173 / backend :8080) and the real LLM:

      login -> SUT with two repositories (blog-frontend/blog-backend) ->
      environment with 4 pages + 4 APIs -> target database + admin account ->
      environment-only version -> clarification conversation ->
      upload inputs/typical-section1.md + one real SUT screenshot ->
      wait for extraction (exactly 12 requirements) -> confirm requirements ->
      design (real LLM) -> >=1 case per requirement -> confirm cases ->
      POST /waves (empty body) -> wave terminal -> version stage=completed ->
      requirements/cases/case-runs/report evidence + 4+ UI screenshots.

    Two modes:
      -VerifyOnly   read the evidence written by a previous run and assert it;
                    this is the TDD validator and never touches the stack.
      (default)     run the whole producer flow, then verify it.

    -Resume reuses the checkpoint in typical/_state.json so a transient LLM
    failure does not restart the SUT/version from scratch.

    Evidence (untracked; docs/ is git-ignored):
      docs/version-V0.22/acceptance/evidence/final/RUN-x/typical/
        eztest.json requirements.json cases.json case-runs.json
        screenshots/*.png _state.json _flow-state.json
      docs/version-V0.22/acceptance/evidence/final/RUN-x/logs/fa5-*.log
      docs/version-V0.22/acceptance/evidence/final/RUN-x/logs/fa5-sut-writes.log

    The producer never fabricates a PASS: every wait is bounded, every LLM/API
    retry is capped at 2 exponential-backoff retries, and a missing requirement
    or a non-terminal wave is reported as RED/blocked with the observed values.

.NOTES
    Windows PowerShell 5.1 only. Run from the repository root:
        powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts\v022-fa-typical.ps1 -VerifyOnly
        powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts\v022-fa-typical.ps1
#>
param(
    [string]$RunDir = 'RUN-1',
    [switch]$VerifyOnly,
    [switch]$Resume,
    [int]$ApiPort = 13460,
    [int]$WebPort = 6880,
    [string]$DatabaseName = ''
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot
. (Join-Path $PSScriptRoot 'v022-fa-common.ps1')

$evidenceRoot = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence\final'
$runRoot = Join-Path $evidenceRoot $RunDir
$typicalDir = Join-Path $runRoot 'typical'
$shotDir = Join-Path $typicalDir 'screenshots'
$logDir = Join-Path $runRoot 'logs'
New-Item -ItemType Directory -Force -Path $typicalDir, $shotDir, $logDir | Out-Null

# Run 2 uses its own database (spec §11.14 / FA plan); an explicit -DatabaseName
# always wins so an operator can point the read-only enrichment anywhere.
$script:effectiveDatabase = if (-not [string]::IsNullOrWhiteSpace($DatabaseName)) {
    $DatabaseName
}
elseif ($RunDir -eq 'RUN-2') { 'eztest_v022_fa_r2' }
else { 'eztest_v022_fa' }

$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
$script:logPath = Join-Path $logDir ("fa5-run-" + $stamp + ".log")
$script:utf8NoBom = New-Object System.Text.UTF8Encoding($false)

function Write-Log {
    param([string]$Message)
    $line = "[{0}] {1}" -f (Get-Date -Format 'HH:mm:ss'), $Message
    Write-Host $line
    [System.IO.File]::AppendAllText($script:logPath, $line + [Environment]::NewLine, $script:utf8NoBom)
}

function Write-Utf8NoBom {
    param([string]$Path, [string]$Text)
    $parent = Split-Path -Parent $Path
    if ($parent -and -not (Test-Path -LiteralPath $parent)) { New-Item -ItemType Directory -Force -Path $parent | Out-Null }
    [System.IO.File]::WriteAllText($Path, $Text, $script:utf8NoBom)
}

function Get-OptionalJson {
    param([string]$Path)
    if (-not (Test-Path -LiteralPath $Path)) { return $null }
    try { return (Read-JsonUtf8 -Path $Path) } catch { return $null }
}

function Get-ArrayValue {
    param($Object, [string]$Name)
    if ($null -eq $Object) { return @() }
    $value = $Object.$Name
    if ($null -eq $value) { return @() }
    return @($value)
}

# ---------------------------------------------------------------------------
# TDD validator. Returns the RED list; an empty list means PASS. It reads only
# the evidence files, so it can never be satisfied by the producer's in-memory
# state and can be re-run independently.
# ---------------------------------------------------------------------------
function Invoke-TypicalVerification {
    param([string]$Dir = $typicalDir)

    $red = New-Object System.Collections.Generic.List[string]

    $ezPath = Join-Path $Dir 'eztest.json'
    if (-not (Test-Path -LiteralPath $ezPath)) {
        $red.Add('missing typical/eztest.json')
        # Keep the RED actionable: a producer that stopped before eztest.json still
        # leaves the observed requirement count behind.
        $earlyReqPath = Join-Path $Dir 'requirements.json'
        if (Test-Path -LiteralPath $earlyReqPath) {
            try {
                $earlyReqs = @(Get-ArrayValue (Read-JsonUtf8 -Path $earlyReqPath) 'requirements')
                if ($earlyReqs.Count -ne 12) { $red.Add("requirements.json count=$($earlyReqs.Count) (expected 12)") }
            }
            catch { $red.Add("requirements.json unreadable: $($_.Exception.Message)") }
        }
        return @($red)
    }
    $ez = $null
    try { $ez = Read-JsonUtf8 -Path $ezPath }
    catch { $red.Add("eztest.json unreadable: $($_.Exception.Message)"); return @($red) }

    if ([string]::IsNullOrWhiteSpace([string]$ez.versionId)) { $red.Add('eztest.versionId missing') }
    if ([string]::IsNullOrWhiteSpace([string]$ez.waveId)) { $red.Add('eztest.waveId missing') }
    if ([string]::IsNullOrWhiteSpace([string]$ez.reportId)) { $red.Add('eztest.reportId missing') }
    if ([string]::IsNullOrWhiteSpace([string]$ez.generatedAt)) { $red.Add('eztest.generatedAt missing') }
    if ([string]$ez.stage -ne 'completed') { $red.Add("eztest.stage='$($ez.stage)' (expected completed)") }

    $requirementIds = @(Get-ArrayValue $ez 'requirementIds' | ForEach-Object { [string]$_ })
    if ($requirementIds.Count -ne 12) { $red.Add("eztest.requirementIds=$($requirementIds.Count) (expected 12)") }
    if (@($requirementIds | Sort-Object -Unique).Count -ne $requirementIds.Count) { $red.Add('eztest.requirementIds has duplicates') }

    # requirements.json: exactly 12 requirements, ids matching eztest.json.
    $reqPath = Join-Path $Dir 'requirements.json'
    if (-not (Test-Path -LiteralPath $reqPath)) { $red.Add('missing typical/requirements.json') }
    else {
        $reqDoc = Get-OptionalJson -Path $reqPath
        $reqs = @()
        if ($null -ne $reqDoc) {
            $reqs = @(Get-ArrayValue $reqDoc 'requirements')
            if ($reqs.Count -eq 0) { $reqs = @(Get-ArrayValue $reqDoc 'data') }
        }
        if ($reqs.Count -ne 12) { $red.Add("requirements.json count=$($reqs.Count) (expected 12)") }
        $reqIdsFromDoc = @($reqs | ForEach-Object { [string]$_.id })
        if ($requirementIds.Count -eq 12 -and $reqIdsFromDoc.Count -eq 12) {
            foreach ($problem in @(Assert-ExactSetStrings -Actual $reqIdsFromDoc -Expected $requirementIds -Label 'requirements.json ids')) { $red.Add($problem) }
        }
    }

    # cases.json: every requirement has at least one case.
    $casesPath = Join-Path $Dir 'cases.json'
    if (-not (Test-Path -LiteralPath $casesPath)) { $red.Add('missing typical/cases.json') }
    else {
        $caseDoc = Get-OptionalJson -Path $casesPath
        $caseList = @()
        if ($null -ne $caseDoc) { $caseList = @(Get-ArrayValue $caseDoc 'cases') }
        if ($caseList.Count -eq 0) { $red.Add('cases.json has no cases') }
        $casesPerRequirement = @{}
        foreach ($case in $caseList) {
            $rid = [string]$case.requirementId
            if ([string]::IsNullOrWhiteSpace($rid)) { $red.Add("case $($case.id) has no requirementId"); continue }
            if (-not $casesPerRequirement.ContainsKey($rid)) { $casesPerRequirement[$rid] = 0 }
            $casesPerRequirement[$rid] = $casesPerRequirement[$rid] + 1
        }
        foreach ($rid in $requirementIds) {
            $n = 0
            if ($casesPerRequirement.ContainsKey($rid)) { $n = [int]$casesPerRequirement[$rid] }
            if ($n -lt 1) { $red.Add("requirement $rid has no case") }
        }
        if ($caseList.Count -lt 12) { $red.Add("cases=$($caseList.Count) (expected >= 12)") }
    }

    # case-runs.json: non-empty, grouped by requirement, every requirement ran.
    $runsPath = Join-Path $Dir 'case-runs.json'
    if (-not (Test-Path -LiteralPath $runsPath)) { $red.Add('missing typical/case-runs.json') }
    else {
        $runDoc = Get-OptionalJson -Path $runsPath
        $runGroups = @()
        if ($null -ne $runDoc) { $runGroups = @(Get-ArrayValue $runDoc 'requirements') }
        if ($runGroups.Count -eq 0) { $red.Add('case-runs.json has no requirement groups') }
        $totalRuns = 0
        foreach ($group in $runGroups) {
            $groupRuns = @(Get-ArrayValue $group 'runs')
            $totalRuns += $groupRuns.Count
        }
        if ($totalRuns -le 0) { $red.Add('case-runs.json has zero runs') }
        foreach ($rid in $requirementIds) {
            $group = $runGroups | Where-Object { [string]$_.requirementId -eq $rid } | Select-Object -First 1
            if ($null -eq $group) { $red.Add("requirement $rid has no case-run group") }
            elseif (@(Get-ArrayValue $group 'runs').Count -lt 1) { $red.Add("requirement $rid has no case run") }
        }
    }

    # caseRunIds: the three terminal buckets must carry the observed runs.
    $buckets = $ez.caseRunIds
    $bucketTotal = 0
    foreach ($name in 'passed', 'failed', 'blocked', 'inconclusive') {
        if ($null -eq $buckets) { continue }
        $bucketTotal += @(Get-ArrayValue $buckets $name).Count
    }
    if ($null -eq $buckets) { $red.Add('eztest.caseRunIds missing') }
    elseif ($bucketTotal -le 0) { $red.Add('eztest.caseRunIds is empty') }

    # screenshots: >= 4 real non-blank PNGs.
    $shots = @(Get-ArrayValue $ez 'screenshots' | ForEach-Object { [string]$_ })
    if ($shots.Count -lt 4) { $red.Add("screenshots=$($shots.Count) (expected >= 4)") }
    foreach ($shot in $shots) {
        $resolved = $shot
        if (-not [System.IO.Path]::IsPathRooted($resolved)) {
            $candidate = Join-Path $runRoot $resolved
            if (Test-Path -LiteralPath $candidate) { $resolved = $candidate }
            else {
                $candidate = Join-Path $evidenceRoot $resolved
                if (Test-Path -LiteralPath $candidate) { $resolved = $candidate }
            }
        }
        foreach ($problem in @(Assert-Png -Path $resolved)) { $red.Add("screenshot $problem") }
    }

    # Requirements projection inside eztest.json must agree with the runs.
    $ezReqs = @(Get-ArrayValue $ez 'requirements')
    if ($ezReqs.Count -eq 0) { $red.Add('eztest.requirements missing') }
    foreach ($req in $ezReqs) {
        $rid = [string]$req.id
        if ([string]::IsNullOrWhiteSpace($rid)) { $red.Add('eztest.requirements[] without id'); continue }
        if (@(Get-ArrayValue $req 'caseIds').Count -lt 1) { $red.Add("eztest requirement $rid has no caseIds") }
        $verdict = [string]$req.eztestVerdict
        if ([string]::IsNullOrWhiteSpace($verdict)) { $red.Add("eztest requirement $rid has no eztestVerdict") }
        elseif (@('pass', 'fail', 'gap', 'blocked', 'missing') -notcontains $verdict.ToLowerInvariant()) {
            $red.Add("eztest requirement $rid eztestVerdict='$verdict' is not legal")
        }
    }

    return @($red)
}

# ---------------------------------------------------------------------------
# Entry point.
# ---------------------------------------------------------------------------
if ($VerifyOnly) {
    Write-Log "verify-only run dir=$RunDir"
    $red = @(Invoke-TypicalVerification -Dir $typicalDir)
    if ($red.Count -eq 0) {
        Write-Log 'VERIFY PASS: 12 requirements, stage=completed, cases/runs/screenshots present'
        exit 0
    }
    foreach ($problem in $red) { Write-Log "VERIFY RED: $problem" }
    Write-Log "VERIFY FAIL: $($red.Count) RED item(s)"
    exit 1
}

# ---------------------------------------------------------------------------
# Producer
# ---------------------------------------------------------------------------
function Get-PostgresParts {
    param([string]$Url)
    $match = [regex]::Match($Url, 'postgres://(?<u>[^:]+):(?<p>[^@]+)@(?<h>[^:]+):(?<port>\d+)/(?<db>[^?]+)')
    if (-not $match.Success) { throw 'cannot parse EZTEST_DATABASE_URL' }
    return [pscustomobject]@{
        User     = $match.Groups['u'].Value
        Password = $match.Groups['p'].Value
        Host     = $match.Groups['h'].Value
        Port     = $match.Groups['port'].Value
        Db       = $match.Groups['db'].Value
    }
}

function Set-Fa5Environment {
    param([hashtable]$Values)
    $previous = @{}
    foreach ($key in $Values.Keys) {
        $previous[$key] = [System.Environment]::GetEnvironmentVariable($key, 'Process')
        [System.Environment]::SetEnvironmentVariable($key, [string]$Values[$key], 'Process')
    }
    return $previous
}

function Restore-Fa5Environment {
    param([hashtable]$Previous)
    foreach ($key in $Previous.Keys) {
        [System.Environment]::SetEnvironmentVariable($key, $Previous[$key], 'Process')
    }
}

# Enrich the API projections with requirement.external_id / source_quote from the
# FA database (the public requirement DTO does not expose them). Best effort:
# a psql or decode failure is logged and never turns a real evidence file bogus.
function Add-RequirementSourceFields {
    param([string]$VersionId, [string]$Dir = $typicalDir)
    $reqPath = Join-Path $Dir 'requirements.json'
    if (-not (Test-Path -LiteralPath $reqPath)) { return }
    $psql = Join-Path $repoRoot '.tools\pgsql\bin\psql.exe'
    if (-not (Test-Path -LiteralPath $psql)) { Write-Log 'source-fields: psql missing, skipped'; return }
    $parts = Get-PostgresParts (Get-EnvValue 'EZTEST_DATABASE_URL')
    $previousPassword = $env:PGPASSWORD
    $previousEncoding = $env:PGCLIENTENCODING
    try {
        $env:PGPASSWORD = $parts.Password
        $env:PGCLIENTENCODING = 'UTF8'
        $sql = "select coalesce(json_agg(json_build_object('id', id::text, 'external_id', external_id, 'source_quote', source_quote) order by id), '[]'::json) from requirement where version_id = $VersionId and status <> 'obsolete'"
        $raw = (& $psql -h $parts.Host -p $parts.Port -U $parts.User -d $script:effectiveDatabase -t -A -c $sql 2>$null) -join ''
    }
    finally {
        $env:PGPASSWORD = $previousPassword
        $env:PGCLIENTENCODING = $previousEncoding
    }
    if ([string]::IsNullOrWhiteSpace($raw)) { Write-Log 'source-fields: psql returned nothing, skipped'; return }
    $rows = $null
    try { $rows = $raw | ConvertFrom-Json } catch { Write-Log "source-fields: decode failed ($($_.Exception.Message)), skipped"; return }
    if ($null -eq $rows) { return }
    $byId = @{}
    foreach ($row in @($rows)) { $byId[[string]$row.id] = $row }

    $doc = Read-JsonUtf8 -Path $reqPath
    $list = @(Get-ArrayValue $doc 'requirements')
    foreach ($item in $list) {
        $row = $byId[[string]$item.id]
        if ($null -ne $row) {
            $item | Add-Member -NotePropertyName external_id -NotePropertyValue ([string]$row.external_id) -Force
            $item | Add-Member -NotePropertyName source_quote -NotePropertyValue ([string]$row.source_quote) -Force
        }
    }
    $doc.count = $list.Count
    Write-JsonUtf8 -Path $reqPath -Object $doc -Depth 30

    $ezPath = Join-Path $Dir 'eztest.json'
    if (Test-Path -LiteralPath $ezPath) {
        $ez = Read-JsonUtf8 -Path $ezPath
        foreach ($item in @(Get-ArrayValue $ez 'requirements')) {
            $row = $byId[[string]$item.id]
            if ($null -ne $row) {
                $item | Add-Member -NotePropertyName externalId -NotePropertyValue ([string]$row.external_id) -Force
                $item | Add-Member -NotePropertyName sourceQuote -NotePropertyValue ([string]$row.source_quote) -Force
            }
        }
        Write-JsonUtf8 -Path $ezPath -Object $ez -Depth 30
    }
    Write-Log "source-fields: merged $(@($rows).Count) DB row(s) into requirements.json/eztest.json"
}

function Write-SutWritesLog {
    param([string]$Dir = $typicalDir)
    $path = Join-Path $logDir 'fa5-sut-writes.log'
    $lines = New-Object System.Collections.Generic.List[string]
    $lines.Add("[{0}] FA-5 SUT interaction log (SUT root D:\\project; frontend :5173 / backend :8080)" -f (Get-Date -Format o))
    $lines.Add('READ  GET http://localhost:5173/blog via Playwright 1440x900 fullPage -> typical/screenshots/00-sut-blog-list.png (image-bearing requirement attachment)')
    $ez = Get-OptionalJson -Path (Join-Path $Dir 'eztest.json')
    $runs = Get-OptionalJson -Path (Join-Path $Dir 'case-runs.json')
    if ($null -ne $ez) { $lines.Add("WAVE  waveId=$($ez.waveId) status=$($ez.wave.status) caseRuns=$(@(Get-ArrayValue $ez 'caseIds').Count) confirmed case(s)") }
    if ($null -ne $runs) {
        $stats = $runs.stats
        if ($null -ne $stats) { $lines.Add("EXEC  executor-driven SUT operations: total=$($stats.total) passed=$($stats.passed) failed=$($stats.failed) blocked=$($stats.blocked) inconclusive=$($stats.inconclusive)") }
        foreach ($group in @(Get-ArrayValue $runs 'requirements')) {
            foreach ($run in @(Get-ArrayValue $group 'runs')) {
                $lines.Add("RUN   caseRun=$($run.caseRunId) case=$($run.caseId) status=$($run.status) started=$($run.startedAt) finished=$($run.finishedAt)")
            }
        }
    }
    $lines.Add('NOTE  the script itself performs no direct SUT mutation; all writes are executor/browser operations recorded above and in case-runs.json')
    Write-Utf8NoBom -Path $path -Text (($lines -join [Environment]::NewLine) + [Environment]::NewLine)
}

$adminPassword = Get-EnvValue 'EZTEST_ADMIN_PASSWORD'
$sutPassword = Get-EnvValue 'SUT_PASSWORD'
$sutDbDsn = Get-EnvValue 'SUT_DB_DSN'
$sutDbPassword = Get-EnvValue 'SUT_DB_PASSWORD'
$llmModel = Get-EnvValue 'llm_model_name'
$inputMd = Join-Path $evidenceRoot 'inputs\typical-section1.md'
if (-not (Test-Path -LiteralPath $inputMd)) {
    Write-Log "EXTERNAL-BLOCKED: missing input package file $inputMd"
    exit 2
}

$flowPath = Join-Path $repoRoot '.tmp\fa\typical-flow.mjs'
$nodeFlow = @'
// EzTest v0.22 FA-5 producer: 12 typical requirements end to end on the FA stack.
// Invoked by scripts/v022-fa-typical.ps1. Evidence only; the TDD validator in the
// PowerShell wrapper decides PASS/RED.
import fs from 'node:fs'
import path from 'node:path'
import { createRequire } from 'node:module'

const BASE = process.env.FA5_BASE
const WEB = process.env.FA5_WEB
const REPO_ROOT = process.env.FA5_REPO_ROOT
const TYPICAL = process.env.FA5_TYPICAL
const SHOTS = process.env.FA5_SHOTS
const RUN_DIR = process.env.FA5_RUN_DIR
const INPUT_MD = process.env.FA5_INPUT_MD
const ADMIN_PASSWORD = process.env.FA5_ADMIN_PW
const SUT_PASSWORD = process.env.FA5_SUT_PW
const SUT_DB_DSN = process.env.FA5_SUT_DB_DSN
const SUT_DB_PASSWORD = process.env.FA5_SUT_DB_PASSWORD
const LLM_MODEL = process.env.FA5_LLM_MODEL
const RESUME = process.env.FA5_RESUME === '1'
const EXPECTED = 12
const OFFLINE_TIMEOUT = 120000
const EXTRACTION_TIMEOUT = Number(process.env.FA5_EXTRACTION_TIMEOUT || 1200) * 1000
const DESIGN_TIMEOUT = Number(process.env.FA5_DESIGN_TIMEOUT || 1200) * 1000
const WAVE_TIMEOUT = Number(process.env.FA5_WAVE_TIMEOUT || 1500) * 1000
const REPORT_TIMEOUT = Number(process.env.FA5_REPORT_TIMEOUT || 900) * 1000

const requireFromExecutor = createRequire(path.join(REPO_ROOT, 'executor', 'package.json'))
fs.mkdirSync(TYPICAL, { recursive: true })
fs.mkdirSync(SHOTS, { recursive: true })

let token = ''
let versionId = ''
const state = { startedAt: new Date().toISOString(), steps: [] }

function log(...args) { console.log(new Date().toISOString(), ...args) }
function nowIso() { return new Date().toISOString() }
function sleep(ms) { return new Promise((resolve) => setTimeout(resolve, ms)) }
function saveJson(name, value) { fs.writeFileSync(path.join(TYPICAL, name), JSON.stringify(value, null, 2)) }
function listData(json) { return json && Array.isArray(json.data) ? json.data : [] }
function record(step, detail) {
  state.lastStep = step
  state.steps.push({ at: nowIso(), step, detail })
  state.versionId = versionId || state.versionId
  saveJson('_flow-state.json', state)
  log('STEP', step, JSON.stringify(detail || {}))
}

async function api(method, p, body, opts = {}) {
  const retries = opts.retries === undefined ? 2 : opts.retries
  const timeoutMs = opts.timeoutMs || OFFLINE_TIMEOUT
  let attempt = 0
  for (;;) {
    const headers = {}
    if (token) headers.authorization = `Bearer ${token}`
    let payload
    if (body !== undefined && body !== null) {
      headers['content-type'] = 'application/json'
      payload = JSON.stringify(body)
    }
    const controller = new AbortController()
    const timer = setTimeout(() => controller.abort(), timeoutMs)
    let res = null
    let text = ''
    let json = null
    let transport = null
    try {
      res = await fetch(`${BASE}${p}`, { method, headers, body: payload, signal: controller.signal })
      text = await res.text()
      try { json = text === '' ? null : JSON.parse(text) } catch { json = null }
    } catch (err) {
      transport = String(err && err.message ? err.message : err)
    } finally {
      clearTimeout(timer)
    }
    const status = res ? res.status : 0
    if ((status === 429 || status >= 500 || status === 0) && attempt < retries) {
      const delay = 1000 * Math.pow(2, attempt)
      attempt++
      log(`retry ${attempt}/${retries} ${method} ${p} status=${status} transport=${transport || ''} in ${delay}ms`)
      await sleep(delay)
      continue
    }
    return { status, json, text, transport }
  }
}

async function must(method, p, body, expected = [200, 201], opts = {}) {
  const r = await api(method, p, body, opts)
  if (!expected.includes(r.status)) {
    throw new Error(`${method} ${p} -> ${r.status}: ${(r.text || r.transport || '').slice(0, 500)}`)
  }
  return r.json
}

async function waitFor(label, fn, timeoutMs, intervalMs = 3000) {
  const deadline = Date.now() + timeoutMs
  let last = null
  while (Date.now() < deadline) {
    try {
      const value = await fn()
      if (value) return value
      last = value
    } catch (err) {
      last = String(err && err.message ? err.message : err)
    }
    await sleep(intervalMs)
  }
  throw new Error(`timeout waiting for ${label} (last=${String(JSON.stringify(last)).slice(0, 400)})`)
}

async function uploadContent(filePath, mime) {
  const data = fs.readFileSync(filePath)
  const name = path.basename(filePath)
  for (let attempt = 0; ; attempt++) {
    const form = new FormData()
    form.append('file', new Blob([data], { type: mime }), name)
    const res = await fetch(`${BASE}/api/content`, {
      method: 'POST', headers: { authorization: `Bearer ${token}` }, body: form,
    })
    const text = await res.text()
    if (res.status === 201) return JSON.parse(text).data
    if ((res.status === 429 || res.status >= 500) && attempt < 2) { await sleep(1000 * Math.pow(2, attempt)); continue }
    throw new Error(`upload ${name} -> ${res.status}: ${text.slice(0, 400)}`)
  }
}

async function getStage() {
  const r = await api('GET', `/api/versions/${versionId}/stage`)
  return r.status === 200 ? (r.json && r.json.data ? r.json.data.stage : null) : null
}

async function listRequirements() {
  const r = await api('GET', `/api/versions/${versionId}/requirements?limit=200`)
  if (r.status !== 200) return []
  return listData(r.json)
}

async function listQuestions() {
  const r = await api('GET', `/api/versions/${versionId}/questions`)
  return r.status === 200 ? listData(r.json) : []
}

async function skipPendingQuestions() {
  let skipped = 0
  for (const batch of await listQuestions()) {
    if (batch.status !== 'pending') continue
    const res = await api('POST', `/api/versions/${versionId}/questions/${batch.id}/skip`, {})
    if (res.status === 200) skipped++
  }
  if (skipped > 0) log(`skipped ${skipped} pending question batch(es)`)
  return skipped
}

async function drainQuestions(rounds = 4) {
  for (let round = 0; round < rounds; round++) {
    const skipped = await skipPendingQuestions()
    await sleep(1500)
    const pending = (await listQuestions()).filter((batch) => batch.status === 'pending')
    if (pending.length === 0 && skipped === 0) return
  }
}

async function activeDesignRuns() {
  const r = await api('GET', '/api/agent-center/runs?limit=100')
  if (r.status !== 200) return []
  return listData(r.json).filter((run) => run.status === 'running' && /^design_/.test(String(run.profile_key || '')))
}

// ---------------------------------------------------------------------------
// Browser
// ---------------------------------------------------------------------------
let browser = null
let ezPage = null
async function ensureBrowser() {
  if (browser) return browser
  const { chromium } = requireFromExecutor('playwright')
  try {
    browser = await chromium.launch({ headless: true, channel: 'chrome' })
  } catch (err) {
    log('chrome channel unavailable, falling back to msedge:', String(err.message).split('\n')[0])
    browser = await chromium.launch({ headless: true, channel: 'msedge' })
  }
  return browser
}
async function screenshotSutBlogList() {
  const file = path.join(SHOTS, '00-sut-blog-list.png')
  const b = await ensureBrowser()
  const page = await b.newPage({ viewport: { width: 1440, height: 900 } })
  page.setDefaultTimeout(45000)
  try {
    await page.goto('http://localhost:5173/blog', { waitUntil: 'domcontentloaded' })
    await sleep(2500)
    await page.screenshot({ path: file, fullPage: true })
  } finally {
    await page.close()
  }
  const size = fs.statSync(file).size
  if (size <= 0) throw new Error(`SUT blog screenshot is empty: ${file}`)
  log(`SUT blog screenshot: ${file} (${size} bytes)`)
  return file
}
async function loginEztestUi() {
  const b = await ensureBrowser()
  const page = await b.newPage({ viewport: { width: 1440, height: 900 } })
  page.setDefaultTimeout(60000)
  await page.goto(`${WEB}/login`, { waitUntil: 'domcontentloaded' })
  await sleep(1000)
  if (!page.url().includes('/login')) { ezPage = page; return page }
  const hasLogin = await page.locator('#username').count()
  if (hasLogin > 0) {
    await page.fill('#username', 'admin')
    await page.fill('#password', ADMIN_PASSWORD)
    await page.locator('button[type=submit]').click()
  }
  await page.waitForURL(/\/versions/, { timeout: 90000 })
  ezPage = page
  return page
}
async function openVersionTab(tab) {
  const page = ezPage || await loginEztestUi()
  await page.goto(`${WEB}/versions/${versionId}?tab=${tab}`, { waitUntil: 'domcontentloaded' })
  await page.waitForSelector('[data-testid="stage-bar"]', { timeout: 60000 })
  await sleep(1500)
  return page
}
async function captureUiScreenshots() {
  const shots = []
  await loginEztestUi()
  const tabs = [
    ['requirements', '01-requirements.png'],
    ['design', '02-design.png'],
    ['execution', '03-execution.png'],
    ['reports', '04-reports.png'],
  ]
  for (const [tab, name] of tabs) {
    await openVersionTab(tab)
    await ezPage.screenshot({ path: path.join(SHOTS, name), fullPage: true })
    shots.push(`${RUN_DIR}/typical/screenshots/${name}`)
    log(`screenshot ${tab} -> ${name}`)
  }
  return shots
}

// ---------------------------------------------------------------------------
// Resources + version
// ---------------------------------------------------------------------------
async function ensureSut() {
  if (state.sutId) {
    const existing = await api('GET', `/api/resources/suts/${state.sutId}`)
    if (existing.status === 200) return state.sutId
  }
  const list = listData(await must('GET', '/api/resources/suts'))
  const found = list.find((sut) => sut.name === 'FA SUT')
  if (found) { state.sutId = found.id; return found.id }
  const created = await must('POST', '/api/resources/suts', {
    name: 'FA SUT',
    intro: 'v0.22 FA-5 typical-requirements end-to-end SUT (real blog)',
  })
  state.sutId = created.data.id
  return state.sutId
}

async function ensureRepository(name, repoPath, include) {
  const list = listData(await must('GET', '/api/resources/code-repos'))
  const found = list.find((repo) => repo.path === repoPath || repo.name === name)
  if (found) return found
  const created = await must('POST', '/api/resources/code-repos', {
    name, path: repoPath, branch: 'master', include, exclude: ['node_modules'],
  })
  return created.data
}

async function ensureRepositories(sutId) {
  const backend = await ensureRepository('blog-backend', 'D:\\project\\blog-backend', ['**/*.go'])
  const frontend = await ensureRepository('blog-frontend', 'D:\\project\\blog-frontend', ['src/**'])
  const linked = listData(await must('GET', `/api/suts/${sutId}/repositories`))
  const linkedIds = new Set(linked.map((entry) => String(entry.repository && entry.repository.id ? entry.repository.id : entry.repository_id)))
  for (const [repo, role] of [[frontend, 'frontend'], [backend, 'backend']]) {
    if (linkedIds.has(String(repo.id))) continue
    const r = await api('POST', `/api/suts/${sutId}/repositories`, { repository_id: repo.id, role })
    if (r.status !== 201 && r.status !== 409) throw new Error(`link repository ${repo.name} -> ${r.status}: ${(r.text || '').slice(0, 300)}`)
  }
  state.repos = { backend: backend.id, frontend: frontend.id }
  record('sut.repositories', { sutId, backend: backend.id, frontend: frontend.id })
}

async function ensureEnvironment(sutId) {
  const list = listData(await must('GET', `/api/resources/suts/${sutId}/environments`))
  const found = list.find((env) => env.name === 'Local environment') || list.find((env) => env.is_default) || list[0]
  if (found) { state.environmentId = found.id; return found.id }
  const created = await must('POST', `/api/resources/suts/${sutId}/environments`, {
    name: 'Local environment', base_url: 'http://localhost:5173', is_default: true,
  })
  state.environmentId = created.data.id
  return state.environmentId
}

const PAGE_DEFS = [
  { name: 'Home', url: 'http://localhost:5173/', route_template: '/' },
  { name: 'Blog list', url: 'http://localhost:5173/blog', route_template: '/blog' },
  { name: 'Article detail', url: 'http://localhost:5173/posts/1', route_template: '/posts/:id' },
  { name: 'Admin', url: 'http://localhost:5173/admin', route_template: '/admin' },
]
const API_DEFS = [
  { name: 'List posts', method: 'GET', path_template: '/api/v1/posts' },
  { name: 'Post detail', method: 'GET', path_template: '/api/v1/posts/:id' },
  { name: 'Login', method: 'POST', path_template: '/api/v1/auth/login' },
  { name: 'List categories', method: 'GET', path_template: '/api/v1/categories' },
]

async function ensureResources(environmentId) {
  const pages = listData(await must('GET', `/api/resources/environments/${environmentId}/pages`))
  const pageIds = []
  for (const def of PAGE_DEFS) {
    const found = pages.find((page) => page.route_template === def.route_template) ||
      pages.find((page) => page.name === def.name)
    if (found) {
      pageIds.push(found.id)
      if (found.status === 'proposed') await must('POST', `/api/resources/pages/${found.id}/confirm`, {})
      continue
    }
    const created = await must('POST', `/api/resources/environments/${environmentId}/pages`, def)
    pageIds.push(created.data.id)
  }
  const apis = listData(await must('GET', `/api/resources/environments/${environmentId}/apis`))
  const apiIds = []
  for (const def of API_DEFS) {
    const found = apis.find((api) => api.method === def.method && api.path_template === def.path_template)
    if (found) {
      apiIds.push(found.id)
      if (found.status === 'proposed') await must('POST', `/api/resources/apis/${found.id}/confirm`, {})
      continue
    }
    const created = await must('POST', `/api/resources/environments/${environmentId}/apis`, def)
    apiIds.push(created.data.id)
  }
  const pagesNow = listData(await must('GET', `/api/resources/environments/${environmentId}/pages`))
  const apisNow = listData(await must('GET', `/api/resources/environments/${environmentId}/apis`))
  const approvedPages = pagesNow.filter((page) => page.status === 'approved')
  const approvedApis = apisNow.filter((api) => api.status === 'approved')
  if (approvedPages.length < 4) throw new Error(`approved pages=${approvedPages.length} (expected 4)`)
  if (approvedApis.length < 4) throw new Error(`approved APIs=${approvedApis.length} (expected 4)`)
  state.pageIds = pageIds
  state.apiIds = apiIds
  record('environment.resources', {
    environmentId, pages: pageIds.length, apis: apiIds.length,
    approvedPages: approvedPages.length, approvedApis: approvedApis.length,
  })
}

async function ensureDatabaseAndAccount(sutId, environmentId) {
  const dbs = listData(await must('GET', `/api/resources/suts/${sutId}/databases`))
  let db = dbs.find((entry) => entry.name === 'blog-db')
  if (!db) {
    db = (await must('POST', `/api/resources/suts/${sutId}/databases`, {
      environment_id: environmentId, name: 'blog-db', kind: 'postgres',
      dsn: SUT_DB_DSN, username: 'blog', password: SUT_DB_PASSWORD, read_only: true,
    })).data
  }
  const accounts = listData(await must('GET', `/api/resources/suts/${sutId}/accounts`))
  let account = accounts.find((entry) => entry.username === 'admin')
  if (!account) {
    account = (await must('POST', `/api/resources/suts/${sutId}/accounts`, {
      environment_id: environmentId, username: 'admin', password: SUT_PASSWORD, roles: ['admin'],
    })).data
  }
  state.databaseId = db.id
  state.accountId = account.id
  record('target.database.account', { databaseId: db.id, accountId: account.id })
}

async function createVersion(environmentId) {
  const created = await must('POST', '/api/versions', {
    title: 'FA typical requirements (R01-R12)',
    description: 'v0.22 FA-5: 12 typical requirements end to end',
    goal: 'Validate the 12 typical requirements against the real blog SUT',
    environment_id: environmentId,
  })
  versionId = created.data.id
  state.versionId = versionId
  record('version.created', { versionId, environmentId })
  return versionId
}

async function assertVersionBinding() {
  const version = await must('GET', `/api/versions/${versionId}`)
  const binding = (version.data.bindings || [])[0]
  if (!binding) throw new Error('version has no binding')
  if (!binding.environment || binding.environment.id !== state.environmentId) {
    throw new Error(`binding environment=${binding.environment && binding.environment.id} (expected ${state.environmentId})`)
  }
  const pages = binding.pages || []
  const apis = binding.apis || []
  const accounts = binding.accounts || []
  const databases = binding.databases || []
  const repositories = binding.repositories || []
  const approvedPages = pages.filter((page) => page.status === 'approved').length
  const approvedApis = apis.filter((api) => api.status === 'approved').length
  state.binding = {
    pages: pages.length, apis: apis.length, accounts: accounts.length, databases: databases.length,
    repositories: repositories.length, approvedPages, approvedApis,
    repositoryRoles: repositories.map((repo) => repo.role || (repo.repository && repo.repository.role) || null),
  }
  // Evidence must never carry credentials: the binding DTO includes the target
  // database DSN, so redact the URL password before persisting it.
  const bindingForEvidence = JSON.parse(JSON.stringify(binding).replace(/(postgres(?:ql)?:\/\/[^:\/]+:)[^@\/]+@/g, '$1***@'))
  saveJson('_binding.json', bindingForEvidence)
  record('version.binding', state.binding)
  if (pages.length !== 4 || apis.length !== 4) throw new Error(`binding pages=${pages.length} apis=${apis.length} (expected 4/4)`)
  if (approvedPages !== 4 || approvedApis !== 4) throw new Error(`binding approved pages=${approvedPages} apis=${approvedApis} (expected 4/4)`)
  if (accounts.length !== 1 || databases.length !== 1) throw new Error(`binding accounts=${accounts.length} databases=${databases.length} (expected 1/1)`)
  if (repositories.length !== 2) throw new Error(`binding repositories=${repositories.length} (expected 2)`)
}

async function ensureConversation() {
  const list = listData(await must('GET', `/api/versions/${versionId}/conversations`))
  const found = list.find((entry) => entry.kind === 'clarification')
  if (found) { state.conversationId = found.id; return found.id }
  const created = await must('POST', `/api/versions/${versionId}/conversations`, { kind: 'clarification', title: 'Requirement clarification' })
  state.conversationId = created.data.id
  return state.conversationId
}

async function postAttachmentMessage(contents, clientId) {
  const parts = contents.map((content) => ({
    type: 'attachment', sha256: content.sha256, name: content.name, mime: content.mime, size: content.size,
  }))
  const deadline = Date.now() + 240000
  while (Date.now() < deadline) {
    const r = await api('POST', `/api/versions/${versionId}/conversations/${state.conversationId}/messages`, {
      client_message_id: clientId, parts,
    })
    if (r.status === 201) return r.json && r.json.data
    if (r.status === 409 && r.json && r.json.error && r.json.error.code === 'attachment_pending') {
      await sleep(2500)
      continue
    }
    throw new Error(`attachment message -> ${r.status}: ${(r.text || '').slice(0, 500)}`)
  }
  throw new Error('attachments stayed pending for 240s')
}

async function waitExtractionRun(timeoutMs = EXTRACTION_TIMEOUT) {
  return waitFor('requirement extraction run', async () => {
    const r = await api('GET', `/api/versions/${versionId}/requirements/extractions`)
    if (r.status === 404) return null
    if (r.status !== 200) return null
    const run = r.json && r.json.data
    if (!run) return null
    if (['done', 'failed', 'skipped'].includes(run.status)) return run
    return null
  }, timeoutMs, 3000)
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------
async function main() {
  if (RESUME) {
    const saved = path.join(TYPICAL, '_flow-state.json')
    if (fs.existsSync(saved)) {
      try {
        const previous = JSON.parse(fs.readFileSync(saved, 'utf8'))
        for (const key of ['sutId', 'environmentId', 'pageIds', 'apiIds', 'databaseId', 'accountId', 'conversationId', 'waveId']) {
          if (previous[key] !== undefined) state[key] = previous[key]
        }
        if (previous.versionId) { versionId = previous.versionId; state.versionId = versionId }
        log('resume: loaded checkpoint', JSON.stringify({ versionId, sutId: state.sutId, stage: previous.stage }))
      } catch (err) {
        log('resume: checkpoint unreadable, starting fresh:', String(err.message))
      }
    }
  }
  saveJson('_flow-state.json', state)

  // -- login + real LLM verification --------------------------------------
  const login = await must('POST', '/api/auth/login', { username: 'admin', password: ADMIN_PASSWORD })
  token = login.data.token
  record('auth.login', { user: login.data.user.username })

  const configs = listData(await must('GET', '/api/admin/llm/configs'))
  const defaultConfig = configs.find((config) => config.is_default)
  if (!defaultConfig) throw new Error('no default LLM config registered in the FA stack')
  const models = defaultConfig.models || []
  const defaultModel = models.find((model) => model.id === defaultConfig.default_model_id) || models[0]
  const llm = {
    configId: defaultConfig.id, name: defaultConfig.name, baseUrl: defaultConfig.base_url,
    model: defaultModel ? defaultModel.model : null,
    supportsVision: !!(defaultModel && defaultModel.supports_vision),
    defaultModelId: defaultConfig.default_model_id,
  }
  state.llm = llm
  record('llm.default', llm)
  if (!llm.supportsVision) throw new Error('default LLM model does not support vision')
  if (llm.model !== LLM_MODEL) throw new Error(`default model=${llm.model} (expected ${LLM_MODEL})`)
  if (/^(https?:\/\/)?(localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1\])(:\d+)?(\/|$)/.test(String(llm.baseUrl))) {
    throw new Error(`LLM base_url points at a local proxy: ${llm.baseUrl}`)
  }

  // -- screenshot of the real SUT blog list (image-bearing requirement) ----
  const blogShot = path.join(SHOTS, '00-sut-blog-list.png')
  if (!fs.existsSync(blogShot) || fs.statSync(blogShot).size <= 0) await screenshotSutBlogList()
  else log('reusing existing SUT blog screenshot', blogShot)

  // -- SUT + repositories + environment + resources + account --------------
  const sutId = await ensureSut()
  await ensureRepositories(sutId)
  const environmentId = await ensureEnvironment(sutId)
  await ensureResources(environmentId)
  await ensureDatabaseAndAccount(sutId, environmentId)

  // -- environment-only version --------------------------------------------
  if (versionId) {
    const existing = await api('GET', `/api/versions/${versionId}`)
    if (existing.status !== 200) { versionId = ''; state.versionId = '' }
  }
  if (!versionId) await createVersion(environmentId)
  await assertVersionBinding()
  const conversationId = await ensureConversation()
  record('conversation.clarification', { conversationId })

  // -- requirements: upload + extraction -----------------------------------
  let requirements = await listRequirements()
  let extractionRun = null
  if (requirements.length !== EXPECTED) {
    const mdContent = await uploadContent(INPUT_MD, 'text/markdown')
    const shotContent = await uploadContent(blogShot, 'image/png')
    const mdInfo = { sha256: mdContent.sha256, name: path.basename(INPUT_MD), mime: 'text/markdown', size: mdContent.size }
    const shotInfo = { sha256: shotContent.sha256, name: path.basename(blogShot), mime: 'image/png', size: shotContent.size }
    state.attachments = [mdInfo, shotInfo]
    record('attachments.uploaded', { markdown: mdInfo.name + ':' + mdInfo.size, image: shotInfo.name + ':' + shotInfo.size })
    const message = await postAttachmentMessage([mdInfo, shotInfo], `fa5-requirements-${Date.now()}`)
    record('message.attachments', { messageId: message && message.id })
    await drainQuestions()
    try {
      extractionRun = await waitExtractionRun()
    } catch (err) {
      log('extraction wait failed:', String(err.message))
    }
    if (extractionRun) {
      state.extraction = {
        runId: extractionRun.id, status: extractionRun.status, expected: extractionRun.expected,
        extracted: extractionRun.extracted, missing: extractionRun.missing,
        plannedCount: extractionRun.planned_count, totalSegments: extractionRun.total_segments,
        doneSegments: extractionRun.done_segments, failedSegments: extractionRun.failed_segments,
      }
      record('extraction.run', state.extraction)
    }
    requirements = await listRequirements()
    if (requirements.length === 0) {
      log('extraction produced 0 requirements; retrying once')
      if (extractionRun && extractionRun.id) {
        const retry = await api('POST', `/api/versions/${versionId}/requirements/extractions/${extractionRun.id}/retry`, {})
        if (retry.status !== 200) await api('POST', `/api/versions/${versionId}/requirements/extractions`, {})
      } else {
        await api('POST', `/api/versions/${versionId}/requirements/extractions`, {})
      }
      try { extractionRun = await waitExtractionRun() } catch (err) { log('extraction retry wait failed:', String(err.message)) }
      requirements = await listRequirements()
    }
  } else {
    log('resume: version already has', requirements.length, 'requirements; skipping upload/extraction')
  }
  state.requirementIds = requirements.map((req) => String(req.id))
  record('requirements.extracted', {
    count: requirements.length,
    ids: state.requirementIds,
    statuses: requirements.map((req) => req.status),
  })
  // Persist the observed extraction (run + per-segment counts) so a RED outcome
  // is diagnosable without guessing: the acceptance input is expected to yield
  // exactly 12 requirements from its 12 requirement sections.
  try {
    const runResponse = await api('GET', `/api/versions/${versionId}/requirements/extractions`)
    const extractionData = runResponse.status === 200 ? (runResponse.json && runResponse.json.data) : null
    saveJson('_extraction.json', {
      versionId, generatedAt: nowIso(), run: extractionData,
      segments: extractionData && extractionData.segments ? extractionData.segments : [],
      observedRequirementCount: requirements.length,
    })
  } catch (err) {
    log('extraction evidence capture failed:', String(err.message))
  }
  saveJson('requirements.json', {
    versionId, generatedAt: nowIso(), count: requirements.length,
    extraction: state.extraction || null,
    requirements: requirements.map((req) => ({
      id: String(req.id), current_version_id: req.current_version_id, revision: req.revision,
      title: req.title, status: req.status, priority: req.priority, targets: req.targets,
      confirmed_at: req.confirmed_at,
    })),
  })
  if (requirements.length !== EXPECTED) {
    state.error = `requirements=${requirements.length} (expected ${EXPECTED})`
    throw new Error(state.error)
  }

  // -- confirm all 12 requirements -----------------------------------------
  const confirmedStatuses = () => requirements.every((req) => req.status === 'clarified' || req.status === 'closed')
  if (!confirmedStatuses()) {
    await must('POST', `/api/versions/${versionId}/requirements/confirmations`, {
      requirement_ids: state.requirementIds,
    })
    requirements = await waitFor('requirements clarified', async () => {
      const current = await listRequirements()
      return current.length === EXPECTED && current.every((req) => req.status === 'clarified' || req.status === 'closed') ? current : null
    }, 120000, 3000)
    state.requirements = requirements.map((req) => ({ id: String(req.id), status: req.status }))
  }
  record('requirements.confirmed', { statuses: requirements.map((req) => req.status) })
  await drainQuestions()

  // -- advance to designing ------------------------------------------------
  let stage = await getStage()
  for (let attempt = 0; attempt < 8 && (stage === 'clarifying' || stage === 'requirement_review'); attempt++) {
    const advanced = await api('POST', `/api/versions/${versionId}/stage/advance`, {})
    if (advanced.status === 409) {
      const gate = advanced.json && advanced.json.error && advanced.json.error.details ? advanced.json.error.details.gate || advanced.json.error.details.failed : ''
      log('stage advance rejected:', String(gate || advanced.status))
      if (String(gate).includes('requirement_gap')) { await drainQuestions(); await sleep(3000); stage = await getStage(); continue }
      break
    }
    stage = await getStage()
  }
  record('stage.designing', { stage })
  if (stage !== 'designing' && stage !== 'case_review' && !(RESUME && stage === 'completed')) throw new Error(`stage=${stage} (expected designing/case_review)`)
  if (RESUME && stage === 'completed') log('resume: version already completed; collecting evidence without re-running design/execution')

  // -- wait for design coverage (>=1 case per requirement) -----------------
  async function readDesignGraph() {
    const r = await api('GET', `/api/versions/${versionId}/design/graph`)
    if (r.status !== 200) return null
    return r.json && r.json.data ? r.json.data : null
  }
  function flattenCases(graph) {
    const cases = []
    for (const requirement of graph.requirements || []) {
      for (const scenario of requirement.scenarios || []) {
        for (const candidate of scenario.cases || []) {
          cases.push({ requirement_id: String(requirement.requirement_id), ...candidate })
        }
      }
    }
    return cases
  }
  let designGraph = null
  let designCases = []
  if (stage === 'designing' || stage === 'case_review') {
    const designed = await waitFor('design coverage for 12 requirements', async () => {
      const currentStage = await getStage()
      const graph = await readDesignGraph()
      if (!graph) return null
      const cases = flattenCases(graph)
      const covered = new Set(cases.map((entry) => entry.requirement_id))
      const allCovered = state.requirementIds.every((id) => covered.has(String(id)))
      const running = await activeDesignRuns()
      if (allCovered && (currentStage === 'case_review' || currentStage === 'designing')) {
        if (currentStage === 'designing' && running.length > 0) return null
        return { graph, cases, stage: currentStage }
      }
      if (currentStage === 'case_review' && running.length === 0 && !allCovered) {
        log('design settled without full coverage; waiting/nudging')
      }
      return null
    }, DESIGN_TIMEOUT, 4000)
    designGraph = designed.graph
    designCases = designed.cases
    stage = designed.stage
    // The design agent keeps adding revisions while the stage is case_review; a
    // revision that lands after a case was confirmed supersedes it, so wait for
    // the running design runs to drain (bounded) before confirming anything.
    for (let attempt = 0; attempt < 36; attempt++) {
      const running = await activeDesignRuns()
      if (running.length === 0) break
      if (attempt === 0) log('design: waiting for', running.length, 'running design run(s) before confirming')
      await sleep(5000)
    }
    designGraph = await readDesignGraph()
    if (designGraph) designCases = flattenCases(designGraph)
    if (stage === 'designing') {
      try {
        stage = await waitFor('design convergence to case_review', async () => {
          const current = await getStage()
          return current === 'case_review' ? current : null
        }, 120000, 3000)
      } catch (err) {
        log('design convergence wait failed:', String(err.message))
      }
    }
  } else {
    designGraph = await readDesignGraph()
    designCases = designGraph ? flattenCases(designGraph) : []
  }
  state.design = {
    stage, caseCount: designCases.length,
    coveredRequirements: new Set(designCases.map((entry) => entry.requirement_id)).size,
  }
  record('design.cases', state.design)
  saveJson('_design-graph.json', designGraph || null)
  if (designCases.length < EXPECTED) throw new Error(`design cases=${designCases.length} (expected >= ${EXPECTED})`)
  for (const rid of state.requirementIds) {
    if (!designCases.some((entry) => entry.requirement_id === String(rid))) throw new Error(`requirement ${rid} has no design case`)
  }

  // -- confirm one executable case per requirement -------------------------
  function pickCase(list) {
    const executable = list.filter((entry) => Number(entry.step_count) > 0 && Number(entry.assertion_count) > 0)
    const pool = executable.length > 0 ? executable : list
    return pool.find((entry) => entry.status === 'draft') || pool[0]
  }
  const confirmed = []
  for (let index = 0; index < state.requirementIds.length; index++) {
    const rid = String(state.requirementIds[index])
    const list = designCases.filter((entry) => entry.requirement_id === rid)
    let candidate = (RESUME && stage === 'completed')
      ? (list.find((entry) => String(entry.status) === 'confirmed') || pickCase(list))
      : pickCase(list)
    if (String(candidate.status) === 'confirmed' || (RESUME && stage === 'completed')) {
      // Already confirmed (for example by the deterministic discovery path):
      // confirming again would be a 409, so adopt the current case version.
      confirmed.push({
        requirementId: rid,
        caseId: String(candidate.case_id),
        caseVersionId: String(candidate.current_version_id),
        title: candidate.title,
        stepCount: candidate.step_count,
        assertionCount: candidate.assertion_count,
        alreadyConfirmed: true,
      })
      continue
    }
    let confirm = await api('POST', `/api/versions/${versionId}/design/confirmations`, {
      scope: 'case', target_id: candidate.case_id, batch_id: `fa5-r${String(index + 1).padStart(2, '0')}`,
    })
    if (confirm.status !== 200) {
      log(`confirm case ${candidate.case_id} -> ${confirm.status}; refreshing graph once`)
      const graph = await readDesignGraph()
      designCases = graph ? flattenCases(graph) : designCases
      candidate = pickCase(designCases.filter((entry) => entry.requirement_id === rid))
      confirm = await api('POST', `/api/versions/${versionId}/design/confirmations`, {
        scope: 'case', target_id: candidate.case_id, batch_id: `fa5-r${String(index + 1).padStart(2, '0')}-b`,
      })
      if (confirm.status !== 200) throw new Error(`confirm case ${candidate.case_id} -> ${confirm.status}: ${(confirm.text || '').slice(0, 300)}`)
    }
    confirmed.push({
      requirementId: rid,
      caseId: String(candidate.case_id),
      caseVersionId: String(candidate.current_version_id),
      title: candidate.title,
      stepCount: candidate.step_count,
      assertionCount: candidate.assertion_count,
    })
  }
  state.confirmedCases = confirmed
  record('design.confirmed', { confirmed: confirmed.length, caseIds: confirmed.map((entry) => entry.caseId) })
  const savedCases = designCases.map((entry) => ({
    id: String(entry.case_id), caseVersionId: String(entry.current_version_id),
    requirementId: entry.requirement_id, title: entry.title, status: entry.status,
    stepCount: entry.step_count, assertionCount: entry.assertion_count,
    confirmed: confirmed.some((c) => c.caseId === String(entry.case_id)),
  }))
  saveJson('cases.json', {
    versionId, generatedAt: nowIso(), count: savedCases.length,
    confirmedCaseIds: confirmed.map((entry) => entry.caseId),
    cases: savedCases,
  })

  // -- advance to executing + start the wave -------------------------------
  stage = await getStage()
  if (stage === 'case_review' || stage === 'designing') {
    const advance = await api('POST', `/api/versions/${versionId}/stage/advance`, {})
    if (advance.status !== 200) {
      const gate = advance.json && advance.json.error ? JSON.stringify(advance.json.error).slice(0, 300) : advance.status
      throw new Error(`advance to executing failed: ${gate}`)
    }
    stage = await getStage()
  }
  record('stage.executing', { stage })
  if (stage !== 'executing' && stage !== 'reporting' && stage !== 'completed') throw new Error(`stage=${stage} (expected executing)`)

  let waveId = state.waveId || null
  let wave = null
  if (!waveId && (stage === 'executing' || stage === 'reporting')) {
    // Select exactly the cases that are confirmed *now*: an empty body picks
    // every current+confirmed case, and a late design revision could silently
    // shrink that set between the confirmation and the wave start.
    const graphNow = await readDesignGraph()
    const casesNow = graphNow ? flattenCases(graphNow) : designCases
    const waveCaseIds = []
    for (const rid of state.requirementIds) {
      const hit = casesNow.find((entry) => String(entry.requirement_id) === String(rid) && String(entry.status) === 'confirmed')
      if (hit) waveCaseIds.push(String(hit.case_id))
    }
    if (waveCaseIds.length < EXPECTED) throw new Error(`only ${waveCaseIds.length} confirmed cases before the wave (expected ${EXPECTED})`)
    const started = await api('POST', `/api/versions/${versionId}/waves`, { case_ids: waveCaseIds })
    if (started.status === 201) waveId = started.json && started.json.data ? started.json.data.wave_id : null
    else if (started.status === 409) {
      const waves = listData(await must('GET', `/api/versions/${versionId}/waves`))
      const active = waves.find((entry) => ['pending', 'running', 'paused'].includes(entry.status)) || waves[waves.length - 1]
      waveId = active ? active.id : null
    } else {
      throw new Error(`POST /waves -> ${started.status}: ${(started.text || '').slice(0, 400)}`)
    }
    state.waveId = waveId
    record('wave.started', { waveId, status: started.status, body: started.json ? started.json.data : null })
  } else if (waveId) {
    log('resume: reusing wave', waveId)
  }

  if (waveId) {
    wave = await waitFor('wave terminal', async () => {
      const r = await api('GET', `/api/versions/${versionId}/waves/${waveId}`)
      if (r.status !== 200) return null
      const current = r.json && r.json.data
      return current && ['done', 'failed', 'cancelled'].includes(current.status) ? current : null
    }, WAVE_TIMEOUT, 3000)
    state.wave = wave
    record('wave.terminal', { waveId, status: wave.status, counts: wave.counts })
  }

  // -- reporting -> completed ----------------------------------------------
  stage = await getStage()
  if (stage === 'executing') {
    try { stage = (await waitFor('stage reporting', async () => { const s = await getStage(); return s === 'reporting' || s === 'completed' ? s : null }, 300000, 4000)) || stage } catch (err) { log('stage reporting wait failed:', String(err.message)) }
  }
  if (stage === 'reporting') {
    try { stage = (await waitFor('version completed', async () => { const s = await getStage(); return s === 'completed' ? s : null }, REPORT_TIMEOUT, 5000)) || stage } catch (err) { log('completed wait failed:', String(err.message)) }
  }
  state.stage = stage
  record('version.stage', { stage })

  // -- report + case runs --------------------------------------------------
  const reports = listData(await must('GET', `/api/versions/${versionId}/reports`))
  const published = reports.find((entry) => entry.status === 'published' || entry.current_status === 'published')
  const reportId = published ? published.id : (reports.length > 0 ? reports[reports.length - 1].id : null)
  saveJson('_reports.json', reports)
  state.reportId = reportId
  record('reports.collected', { reports: reports.length, reportId })

  let caseRuns = []
  if (waveId) {
    const response = await api('GET', `/api/versions/${versionId}/waves/${waveId}/case-runs`)
    if (response.status === 200) caseRuns = listData(response.json)
  }
  const caseToRequirement = new Map(designCases.map((entry) => [String(entry.case_id), entry.requirement_id]))
  const groups = state.requirementIds.map((rid) => ({ requirementId: String(rid), externalId: '', caseIds: [], runs: [] }))
  const groupByRid = new Map(groups.map((group) => [group.requirementId, group]))
  const unmapped = []
  const runIds = { passed: [], failed: [], blocked: [], inconclusive: [] }
  const stats = { total: caseRuns.length, passed: 0, failed: 0, blocked: 0, inconclusive: 0 }
  for (const run of caseRuns) {
    const rid = caseToRequirement.get(String(run.case_id))
    const entry = {
      caseRunId: String(run.id), caseId: String(run.case_id), caseVersionId: String(run.case_version_id),
      title: run.title, status: run.status, failureClass: run.failure_class || '',
      startedAt: run.started_at, finishedAt: run.finished_at, summary: run.summary || null,
      evidenceCount: run.evidence_count,
    }
    if (rid && groupByRid.has(String(rid))) {
      const group = groupByRid.get(String(rid))
      group.runs.push(entry)
      if (!group.caseIds.includes(String(run.case_id))) group.caseIds.push(String(run.case_id))
    } else {
      unmapped.push(entry)
    }
    if (runIds[run.status]) runIds[run.status].push(String(run.id))
    if (stats[run.status] !== undefined) stats[run.status] += 1
  }
  const requirementVerdict = (group) => {
    if (group.runs.length === 0) return 'missing'
    if (group.runs.some((run) => run.status === 'failed')) return 'fail'
    if (group.runs.some((run) => run.status === 'blocked')) return 'blocked'
    if (group.runs.some((run) => run.status === 'inconclusive')) return 'gap'
    if (group.runs.every((run) => run.status === 'passed')) return 'pass'
    return 'pass'
  }
  for (const group of groups) {
    const req = requirements.find((entry) => String(entry.id) === group.requirementId)
    group.eztestVerdict = requirementVerdict(group)
    if (req) group.title = req.title
  }
  saveJson('case-runs.json', {
    versionId, waveId, generatedAt: nowIso(), stats,
    requirements: groups, unmapped,
  })
  record('case-runs', { stats, unmapped: unmapped.length })

  // -- UI screenshots ------------------------------------------------------
  let screenshots = [`${RUN_DIR}/typical/screenshots/00-sut-blog-list.png`]
  try {
    const uiShots = await captureUiScreenshots()
    screenshots = screenshots.concat(uiShots)
  } catch (err) {
    log('UI screenshot flow failed:', String(err.message).slice(0, 400))
  }
  if (browser) { try { await browser.close() } catch { } }
  state.screenshots = screenshots
  record('screenshots', { screenshots })

  // -- eztest.json ---------------------------------------------------------
  const topVerdictOrder = ['pass', 'gap', 'blocked', 'fail']
  let topVerdict = 'pass'
  for (const group of groups) {
    const verdict = group.eztestVerdict
    if (topVerdictOrder.indexOf(verdict) > topVerdictOrder.indexOf(topVerdict)) topVerdict = verdict
  }
  if (groups.length === 0 || groups.some((group) => group.runs.length === 0)) topVerdict = 'gap'
  const eztest = {
    versionId, stage, waveId, reportId, generatedAt: nowIso(),
    requirementIds: state.requirementIds,
    caseIds: designCases.map((entry) => String(entry.case_id)),
    caseVersionIds: confirmed.map((entry) => entry.caseVersionId),
    caseRunIds: runIds,
    screenshots,
    eztestVerdict: topVerdict,
    wave: wave ? { id: wave.id, status: wave.status, counts: wave.counts } : null,
    extraction: state.extraction || null,
    llm,
    multiRepo: {
      sutRepositories: state.binding ? state.binding.repositories : 0,
      repositoryRoles: state.binding ? state.binding.repositoryRoles : [],
    },
    binding: state.binding || null,
    stats,
    requirements: groups.map((group) => ({
      id: group.requirementId, title: group.title || '', eztestVerdict: group.eztestVerdict,
      caseIds: group.caseIds, runIds: group.runs.map((run) => run.caseRunId),
      runStatuses: group.runs.map((run) => run.status),
    })),
    cases: savedCases,
  }
  saveJson('eztest.json', eztest)
  state.ok = true
  state.finishedAt = nowIso()
  record('evidence.written', { eztest: true, requirements: true, cases: true, caseRuns: true })
  saveJson('_flow-state.json', state)
  log('FLOW COMPLETE stage=', stage, 'requirements=', state.requirementIds.length, 'cases=', savedCases.length, 'caseRuns=', caseRuns.length, 'stats=', JSON.stringify(stats))
  return true
}

main().then(() => {
  process.exit(0)
}).catch(async (err) => {
  log('FLOW FAILED:', err && err.stack ? err.stack : String(err))
  state.error = String(err && err.message ? err.message : err)
  state.finishedAt = nowIso()
  try { saveJson('_flow-state.json', state) } catch { }
  try { if (browser) await browser.close() } catch { }
  process.exit(1)
})
'@

Write-Utf8NoBom -Path $flowPath -Text $nodeFlow
Write-Log "producer flow written: $flowPath"

$nodeExe = (Get-Command node -ErrorAction Stop).Source
$flowLogPath = Join-Path $logDir ("fa5-flow-" + $stamp + ".log")
$flowErrPath = Join-Path $logDir ("fa5-flow-" + $stamp + ".err.log")
$envValues = @{
    FA5_BASE               = "http://127.0.0.1:$ApiPort"
    FA5_WEB                = "http://127.0.0.1:$WebPort"
    FA5_REPO_ROOT          = $repoRoot
    FA5_TYPICAL            = $typicalDir
    FA5_SHOTS              = $shotDir
    FA5_RUN_DIR            = $RunDir
    FA5_INPUT_MD           = $inputMd
    FA5_ADMIN_PW           = $adminPassword
    FA5_SUT_PW             = $sutPassword
    FA5_SUT_DB_DSN         = $sutDbDsn
    FA5_SUT_DB_PASSWORD    = $sutDbPassword
    FA5_LLM_MODEL          = $llmModel
    FA5_RESUME             = $(if ($Resume) { '1' } else { '0' })
    FA5_EXTRACTION_TIMEOUT = '1200'
    FA5_DESIGN_TIMEOUT     = '1200'
    FA5_WAVE_TIMEOUT       = '1500'
    FA5_REPORT_TIMEOUT     = '900'
}
$previousEnv = Set-Fa5Environment -Values $envValues
try {
    Write-Log 'node producer start'
    $process = Start-Process -FilePath $nodeExe -ArgumentList @($flowPath) -WorkingDirectory $repoRoot `
        -RedirectStandardOutput $flowLogPath -RedirectStandardError $flowErrPath -PassThru -NoNewWindow -Wait
    $nodeExit = $process.ExitCode
}
finally {
    Restore-Fa5Environment -Previous $previousEnv
}
Write-Log "node producer exit=$nodeExit"
if (Test-Path -LiteralPath $flowErrPath) {
    $errText = [System.IO.File]::ReadAllText($flowErrPath, [System.Text.Encoding]::UTF8)
    if (-not [string]::IsNullOrWhiteSpace($errText)) {
        foreach ($line in ($errText -split "`r?`n")) { if ($line.Trim()) { Write-Log "stderr: $line" } }
    }
}
if (Test-Path -LiteralPath $flowLogPath) {
    foreach ($line in [System.IO.File]::ReadAllLines($flowLogPath, [System.Text.Encoding]::UTF8)) {
        Write-Log "flow: $line"
    }
}

$flowState = Get-OptionalJson -Path (Join-Path $typicalDir '_flow-state.json')
if ($null -ne $flowState -and -not [string]::IsNullOrWhiteSpace([string]$flowState.versionId)) {
    try { Add-RequirementSourceFields -VersionId ([string]$flowState.versionId) -Dir $typicalDir }
    catch { Write-Log "source-fields enrichment failed: $($_.Exception.Message)" }
}
try { Write-SutWritesLog -Dir $typicalDir } catch { Write-Log "sut-writes log failed: $($_.Exception.Message)" }

$verifyRed = @(Invoke-TypicalVerification -Dir $typicalDir)
$redLogPath = Join-Path $logDir 'fa5-red.log'
$greenLogPath = Join-Path $logDir 'fa5-green.log'
if ($verifyRed.Count -eq 0) {
    $ez = Get-OptionalJson -Path (Join-Path $typicalDir 'eztest.json')
    $summary = @(
        "FA-5 GREEN at $(Get-Date -Format o)",
        "versionId=$($ez.versionId) stage=$($ez.stage) waveId=$($ez.waveId) reportId=$($ez.reportId)",
        "requirements=$(@(Get-ArrayValue $ez 'requirementIds').Count) cases=$(@(Get-ArrayValue $ez 'caseIds').Count) screenshots=$(@(Get-ArrayValue $ez 'screenshots').Count)",
        "stats=$($ez.stats | ConvertTo-Json -Compress)"
    ) -join [Environment]::NewLine
    Write-Utf8NoBom -Path $greenLogPath -Text ($summary + [Environment]::NewLine)
    Write-Log 'VERIFY PASS: 12 requirements, stage=completed, cases/runs/screenshots present'
    if ($nodeExit -ne 0) {
        Write-Log "WARN: producer exit=$nodeExit but evidence verifies; recording GREEN with producer warning"
    }
    exit 0
}
foreach ($problem in $verifyRed) { Write-Log "VERIFY RED: $problem" }
$redText = (($verifyRed | ForEach-Object { "[{0}] {1}" -f (Get-Date -Format 'HH:mm:ss'), $_ }) -join [Environment]::NewLine) + [Environment]::NewLine
[System.IO.File]::AppendAllText($redLogPath, $redText, $script:utf8NoBom)
Write-Log "VERIFY FAIL: $($verifyRed.Count) RED item(s)"
exit 1
