// W0 requirement-curation regression test (W1-15 diagnostic B).
//
// Run: node --test scripts/v022-w0-flow-curate.test.cjs
//
// The fresh W0 regression run (version 376783338439966720) failed at the real
// requirement_review -> designing gate. DB evidence:
//   * workflow: requirement_confirmation rejected at 2026-11-06T01:23:52
//   * requirements: 1 clarified (kept 376783357154951169, confirmed 01:23:52)
//     + 6 draft (created 01:23:28 by clarification_host run 376783484095561728)
//     + 89 obsolete (deleted from the stale pre-host list)
//   * the 6 drafts are source:FR-001..FR-006, produced by
//     requirement.draft_create while the flow was still exercising the UI
//     question card; the flow's requirementList snapshot was taken before that
//     host run, so the drafts were never curated.
// The gate (backend/cmd/eztest/v022_stage_wiring.go:304-312) correctly
// requires RequirementCounts.Draft == 0, so this was harness fragility, not a
// product regression.
//
// This test extracts the drain/curate helpers from the embedded W0 flow in
// scripts/v022-w0-regression.ps1, proves the old stale-snapshot algorithm
// leaves the gate closed on that exact state, and proves the fixed algorithm
// re-reads the live list, drains late batches and opens the gate. It does not
// start any stack.

const test = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const { spawnSync } = require('node:child_process')

const SCRIPT = path.join(__dirname, 'v022-w0-regression.ps1')
const source = fs.readFileSync(SCRIPT, 'utf8')

const KEEP = '376783357154951169'
const HOST_DRAFTS = [
  '376783590412779520', // source:FR-001
  '376783590496665600', // source:FR-002
  '376783590534414336', // source:FR-003
  '376783590584745984', // source:FR-004
  '376783590647660544', // source:FR-005
  '376783590714769408', // source:FR-006
]

// The embedded W0 flow lives inside a single-quoted here-string. Return only
// that region: the W1 section above it defines helpers with the same names.
function extractW0Flow() {
  const marker = '// W0 acceptance flow (Task 38)'
  const startedAt = source.indexOf(marker)
  assert.ok(startedAt >= 0, 'W0 acceptance flow marker not found')
  const opener = source.lastIndexOf("@'", startedAt)
  assert.ok(opener >= 0, 'W0 here-string opener not found')
  const bodyStart = source.indexOf('\n', opener) + 1
  const closer = source.indexOf('\n\'@ | Set-Content -Encoding UTF8 $flowPath', bodyStart)
  assert.ok(closer >= 0, 'W0 here-string closer not found')
  return source.slice(bodyStart, closer)
}

// Brace-match one async function out of the flow source.
function extractFunction(flow, name) {
  const marker = `async function ${name}(`
  const start = flow.indexOf(marker)
  assert.ok(start >= 0, `missing ${name} in the embedded W0 flow`)
  const bodyStart = flow.indexOf('{', start)
  let depth = 0
  for (let i = bodyStart; i < flow.length; i++) {
    if (flow[i] === '{') depth++
    else if (flow[i] === '}') {
      depth--
      if (depth === 0) return flow.slice(start, i + 1)
    }
  }
  throw new Error(`unterminated ${name}`)
}

function loadHelpers(flow, env) {
  const names = ['skipPendingQuestions', 'drainPendingQuestions', 'curateRequirementsToKeep']
  const fnSource = names.map((name) => extractFunction(flow, name)).join('\n')
  const factory = new Function(
    'api', 'must', 'listData', 'log', 'sleep', 'state',
    `${fnSource}\nreturn { skipPendingQuestions, drainPendingQuestions, curateRequirementsToKeep }`,
  )
  return factory(env.api, env.must, env.listData, env.log, env.sleep, env.state)
}

// The real gate predicate, copied from the production wiring: every current
// requirement is non-draft and at least one is clarified/closed.
function gateOpen(requirements) {
  const live = requirements.filter((entry) => entry.status !== 'obsolete')
  const drafts = live.filter((entry) => entry.status === 'draft').length
  const verified = live.filter((entry) => entry.status === 'clarified' || entry.status === 'closed').length
  return drafts === 0 && verified > 0
}

function staleSnapshotFailure() {
  // The flow listed requirements before the host run: 90 rows in total.
  const requirements = [
    { id: KEEP, status: 'draft', title: '首页内容浏览' },
    ...Array.from({ length: 89 }, (_, i) => ({ id: String(500000 + i), status: 'draft', title: `doc requirement ${i}` })),
  ]
  // The clarification host then drafted 6 requirements while the flow was in
  // the UI question card. They are not in the snapshot.
  for (const id of HOST_DRAFTS) requirements.push({ id, status: 'draft', title: 'late host draft' })
  // Old algorithm: delete every snapshot row except the kept one, then confirm.
  let deleted = 0
  for (const entry of requirements.slice(0, 90)) {
    if (entry.id === KEEP) continue
    entry.status = 'obsolete'
    deleted++
  }
  const kept = requirements.find((entry) => entry.id === KEEP)
  kept.status = 'clarified'
  const live = requirements.filter((entry) => entry.status !== 'obsolete')
  return { requirements, deleted, live, drafts: live.filter((entry) => entry.status === 'draft') }
}

function createBackend() {
  const requirements = [
    { id: KEEP, status: 'draft', title: '首页内容浏览' },
    ...Array.from({ length: 89 }, (_, i) => ({ id: String(500000 + i), status: 'draft', title: `doc requirement ${i}` })),
  ]
  for (const id of HOST_DRAFTS) requirements.push({ id, status: 'draft', title: 'late host draft' })
  const batches = [{ id: 'qb-376783624780906496', status: 'pending' }]
  const calls = { deleted: [], skipped: [], confirmed: [] }

  async function api(method, p, body) {
    if (method === 'GET' && p.endsWith('/questions')) {
      return { status: 200, json: { data: batches.map((b) => ({ id: b.id, status: b.status })) }, text: '' }
    }
    const skipMatch = p.match(/\/questions\/([^/]+)\/skip$/)
    if (method === 'POST' && skipMatch) {
      const batch = batches.find((b) => b.id === skipMatch[1])
      if (batch) batch.status = 'cancelled'
      calls.skipped.push(skipMatch[1])
      return { status: 200, json: { data: { id: skipMatch[1], status: 'cancelled' } }, text: '' }
    }
    if (method === 'GET' && p.includes('/requirements')) {
      return {
        status: 200,
        json: { data: requirements.map((r) => ({ id: r.id, status: r.status, title: r.title })) },
        text: '',
      }
    }
    const deleteMatch = p.match(/\/requirements\/([^/]+)$/)
    if (method === 'DELETE' && deleteMatch) {
      const row = requirements.find((r) => r.id === deleteMatch[1])
      if (row) row.status = 'obsolete'
      calls.deleted.push(deleteMatch[1])
      return { status: 204, json: null, text: '' }
    }
    if (method === 'POST' && p.endsWith('/requirements/confirmations')) {
      for (const id of body.requirement_ids) {
        const row = requirements.find((r) => r.id === id)
        if (row) row.status = 'clarified'
      }
      calls.confirmed.push(...body.requirement_ids)
      return { status: 201, json: { data: {} }, text: '' }
    }
    throw new Error(`unexpected request ${method} ${p}`)
  }

  async function must(method, p, body) {
    const r = await api(method, p, body)
    if (![200, 201].includes(r.status)) throw new Error(`${method} ${p} -> ${r.status}`)
    return r.json
  }

  return { requirements, batches, calls, api, must }
}

test('the embedded W0 flow is valid JavaScript', () => {
  const flow = extractW0Flow()
  const file = path.join(os.tmpdir(), `eztest-w0-flow-${process.pid}.mjs`)
  fs.writeFileSync(file, flow, 'utf8')
  try {
    const checked = spawnSync(process.execPath, ['--check', file], { encoding: 'utf8' })
    assert.equal(checked.status, 0, checked.stderr || checked.stdout)
  } finally {
    fs.rmSync(file, { force: true })
  }
})

test('the pre-fix stale snapshot leaves 6 drafts and keeps the gate closed', () => {
  const failure = staleSnapshotFailure()
  assert.equal(failure.deleted, 89)
  assert.equal(failure.drafts.length, HOST_DRAFTS.length)
  assert.equal(gateOpen(failure.requirements), false)
})

test('the fixed curation drains late drafts and opens the real gate', async () => {
  const backend = createBackend()
  const flow = extractW0Flow()
  const state = { versionId: '376783338439966720' }
  const logs = []
  const helpers = loadHelpers(flow, {
    api: backend.api,
    must: backend.must,
    listData: (json) => (json && Array.isArray(json.data) ? json.data : []),
    log: (...args) => logs.push(args.join(' ')),
    sleep: async () => {},
    state,
  })

  await helpers.drainPendingQuestions()
  await helpers.curateRequirementsToKeep(KEEP)

  assert.equal(backend.batches.filter((b) => b.status === 'pending').length, 0)
  for (const id of HOST_DRAFTS) assert.ok(backend.calls.deleted.includes(id), `late draft ${id} was not deleted`)
  assert.deepEqual(backend.calls.confirmed, [KEEP])
  const live = backend.requirements.filter((entry) => entry.status !== 'obsolete')
  assert.deepEqual(live.map((entry) => `${entry.id}:${entry.status}`), [`${KEEP}:clarified`])
  assert.equal(gateOpen(backend.requirements), true)
  assert.ok(logs.some((line) => line.includes(`kept requirement ${KEEP}`)), 'curation did not log the kept requirement')
})

test('the advance retry handles requirement_confirmation without bypassing the gate', () => {
  const flow = extractW0Flow()
  const attempt = flow.indexOf("if (gate === 'requirement_gap' || gate === 'requirement_confirmation')")
  assert.ok(attempt >= 0, 'advance loop must re-curate on requirement_confirmation')
  const block = flow.slice(attempt, attempt + 700)
  assert.ok(block.includes('curateRequirementsToKeep('), 'retry must re-curate from a fresh list')
  assert.ok(block.includes('continue'), 'retry must go back through the real stage advance call')
  assert.ok(!/stageNow\s*=\s*\{[^}]*designing/.test(flow), 'the stage must never be forced to designing')
})
