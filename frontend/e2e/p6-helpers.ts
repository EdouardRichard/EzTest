import { type APIRequestContext, type APIResponse, expect } from "@playwright/test"

import { API_URL, type RequirementRecord, uniqueSuffix, type VersionRecord } from "./p3-helpers"
import { seedMindmapVersion } from "./p5-helpers"

/**
 * Shared P6 execution E2E helpers (design spec §10.2/§10.5). Every helper talks
 * to the real API and the deterministic P6 fixture/fake LLM; nothing is mocked.
 * Execution-owned identifiers stay decimal strings throughout (D29).
 */

/** Fixture target base URL (the P6 pages live under `/p6/*`). */
export function fixtureTargetUrl(): string {
  const configured = process.env.EZTEST_E2E_FIXTURE_URL?.trim()
  if (configured !== undefined && configured !== "") return configured.replace(/\/+$/, "")
  return "http://127.0.0.1:13335"
}

/** Fake LLM upstream base URL used to switch the P6 scenario mode. */
export function fakeLlmUrl(): string {
  const configured = process.env.EZTEST_E2E_FAKE_LLM_URL?.trim()
  if (configured !== undefined && configured !== "") return configured.replace(/\/+$/, "")
  return "http://127.0.0.1:13334"
}

/** Asserts the HTTP status and unwraps the `{data: ...}` envelope. */
async function readData<T>(
  response: APIResponse,
  expected: number | number[],
  label: string,
): Promise<T> {
  const allowed = Array.isArray(expected) ? expected : [expected]
  expect(allowed, `${label}: unexpected HTTP ${response.status()}`).toContain(response.status())
  const body = (await response.json()) as { data: T }
  return body.data
}

function authHeaders(token: string): Record<string, string> {
  return { Authorization: `Bearer ${token}` }
}

// --- execution DTOs (hand-written mirrors of the OpenAPI contract, spec §6.3) --

export type BatchStatus = "planned" | "running" | "done" | "failed" | "cancelled"
export type PauseMode = "graceful" | "immediate"
export type CaseRunStatus =
  | "pending"
  | "running"
  | "passed"
  | "failed"
  | "blocked"
  | "inconclusive"
  | "skipped"
  | "interrupted"
export type FailureClass = "application" | "environment" | "session" | "model" | "case"

export type BatchCounts = {
  total: number
  passed: number
  failed: number
  blocked: number
  inconclusive: number
  skipped: number
  interrupted: number
}

export type Batch = {
  id: string
  version_id: string
  batch_no: number
  status: BatchStatus
  paused: boolean
  pause_mode: PauseMode | ""
  pause_reason: string
  baseline_hash: string
  code_baseline_commit: string | null
  head_commit: string | null
  counts: BatchCounts
  created_by: string
  started_at: string | null
  finished_at: string | null
  failure_reason: string
}

export type StepResultView = {
  id?: string
  seq: number
  action: string
  expected: string
  actual: string
  status: string
  evidence_ids: string[]
}

export type AssertionView = {
  id: string
  kind: string
  target: string
  expected: string
  actual: string
  verdict: string
  evidence_ids: string[]
  step_seq?: number
}

export type EvidenceView = {
  id: string
  kind: string
  mime: string
  size_bytes: number
  sha256: string
}

export type PendingTakeover = {
  id: string
  reason: string
  instructions: Record<string, unknown>
  expires_at: string
}

export type CaseRun = {
  id: string
  batch_id: string
  case_node_id: string
  case_revision: number
  attempt: number
  status: CaseRunStatus
  failure_class: FailureClass | null
  failure_reason: string
  summary: string
  duration_ms: number
  title: string
  session: { id: string; account: string; state: string } | null
  checkpoint: {
    completed_seqs: number[]
    cleanup_required: boolean
    interrupted_step_seq: number | null
  }
  steps: StepResultView[]
  assertions: AssertionView[]
  evidence: EvidenceView[]
  pending_takeover: PendingTakeover | null
}

export type ReportView = {
  batch: {
    id: string
    batch_no: number
    status: BatchStatus
    baseline_hash: string
    code_baseline_commit: string | null
  }
  summary: {
    case_count: number
    by_status: Partial<Record<CaseRunStatus, number>>
    by_failure_class: Partial<Record<FailureClass, number>>
    flaky_case_ids: string[]
    duration_ms: number
    evidence_count: number
  }
  cases: Array<{
    case_run_id: string
    case_node_id: string
    title: string
    status: CaseRunStatus
    failure_class: FailureClass | null
    steps: number
    assertions: number
    evidence: number
    duration_ms: number
  }>
  environment: { system: string; environment: string; base_url: string }
  generated_at: string
}

export type VersionEvent = {
  seq: number
  type: string
  summary: string
  detail?: Record<string, unknown> | null
  refs?: Record<string, string[]>
  ts?: string
  batch_id?: string
  case_id?: string
  step_id?: string
  agent_role?: string
  session_id?: string
}

/** One persisted evidence artifact (bytes + the hash header the server sent). */
export type EvidenceDownload = {
  evidenceId: string
  body: Buffer
  contentType: string
  sha256: string
}

// --- mindmap seed primitives (P5 API shapes, spec p5 §7.2) -----------------

type MindmapNodeRecord = {
  id: string
  kind: "scenario" | "case"
  title: string
  revision: number
}

type MindmapStepRecord = { id: string; seq: number; action: string; expected: string }

type MindmapAssertionRecord = { id: string; stepId: string | null; kind: string }

export type ExecutionSeedOptions = {
  /** Unique suffix for seeded keys; defaults to `uniqueSuffix()`. */
  suffix?: string
  /** Fixture target origin; defaults to `EZTEST_E2E_FIXTURE_URL` or :13335. */
  fixtureUrl?: string
  /** Title of the seeded scenario/case. */
  caseTitle?: string
  /** Import one valid fixture session; defaults to true (spec §10.5 step 1). */
  seedSession?: boolean
}

/** The resources the execution E2E needs to run one batch. */
export type ExecutionSeed = {
  token: string
  suffix: string
  fixtureUrl: string
  systemId: number
  environmentId: number
  version: VersionRecord
  requirements: RequirementRecord[]
  scenarioNodeId: string
  caseNodeId: string
  stepIds: string[]
  assertionIds: string[]
  /** Fixture credentials and login recipe (auto-login / takeover specs). */
  authFlowId: string
  accountId: number
  accessId: number
  /** Imported valid session; 0 when `seedSession` is false. */
  sessionId: number
  /** Raw `p6_session` cookie value used by the imported storage state. */
  sessionCookie: string
}

async function postMindmapNode(
  request: APIRequestContext,
  token: string,
  versionId: string,
  body: Record<string, unknown>,
): Promise<MindmapNodeRecord> {
  const response = await request.post(`${API_URL}/api/versions/${versionId}/mindmap/nodes`, {
    headers: authHeaders(token),
    data: body,
  })
  return readData<MindmapNodeRecord>(response, 201, "create mindmap node")
}

async function postMindmapStep(
  request: APIRequestContext,
  token: string,
  versionId: string,
  nodeId: string,
  body: { action: string; expected: string },
): Promise<MindmapStepRecord> {
  const response = await request.post(
    `${API_URL}/api/versions/${versionId}/mindmap/nodes/${nodeId}/steps`,
    { headers: authHeaders(token), data: body },
  )
  return readData<MindmapStepRecord>(response, 201, "create mindmap step")
}

async function postMindmapAssertion(
  request: APIRequestContext,
  token: string,
  versionId: string,
  nodeId: string,
  body: {
    stepId?: string
    kind: string
    target?: string
    expression: string
    expected: string
  },
): Promise<MindmapAssertionRecord> {
  const response = await request.post(
    `${API_URL}/api/versions/${versionId}/mindmap/nodes/${nodeId}/assertions`,
    { headers: authHeaders(token), data: body },
  )
  return readData<MindmapAssertionRecord>(response, 201, "create mindmap assertion")
}

/** Extracts one Set-Cookie value from a fetch Response (Node 24). */
function setCookieValue(response: Response, name: string): string {
  const values =
    typeof response.headers.getSetCookie === "function" ? response.headers.getSetCookie() : []
  const fallback = response.headers.get("set-cookie")
  const candidates = values.length > 0 ? values : fallback === null ? [] : [fallback]
  for (const value of candidates) {
    for (const part of value.split(",")) {
      const pair = part.split(";")[0]?.trim() ?? ""
      const separator = pair.indexOf("=")
      if (separator > 0 && pair.slice(0, separator) === name) {
        return pair.slice(separator + 1)
      }
    }
  }
  return ""
}

/** Creates one valid fixture session by logging in to /p6/login with fetch. */
async function importFixtureSession(
  request: APIRequestContext,
  token: string,
  input: {
    fixtureUrl: string
    environmentId: number
    accessId: number
  },
): Promise<{ sessionId: number; cookie: string }> {
  const response = await fetch(`${input.fixtureUrl}/p6/login`, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ username: "fixture", password: "fixture-pass" }).toString(),
    redirect: "manual",
  })
  expect(response.status, "fixture P6 login").toBe(302)
  const cookie = setCookieValue(response, "p6_session")
  expect(cookie, "fixture P6 login must set p6_session").not.toBe("")

  const fixture = new URL(input.fixtureUrl)
  const importResponse = await request.post(
    `${API_URL}/api/resources/accesses/${input.accessId}/sessions`,
    {
      headers: authHeaders(token),
      data: {
        environmentId: input.environmentId,
        kind: "storage_state",
        storageState: {
          cookies: [
            {
              name: "p6_session",
              value: cookie,
              domain: fixture.hostname,
              path: "/",
              expires: -1,
              httpOnly: true,
              secure: false,
              sameSite: "Lax",
            },
          ],
          origins: [{ origin: fixture.origin, localStorage: [], sessionStorage: [] }],
        },
        note: "P6 E2E imported fixture session",
      },
    },
  )
  const session = await readData<{ id: number; state: string }>(
    importResponse,
    201,
    "import fixture session",
  )
  expect(session.state).toBe("valid")
  return { sessionId: session.id, cookie }
}

/**
 * Seeds everything one execution batch needs (spec §10.5 step 1): a system,
 * an environment bound to the fixture target, a version with two confirmed
 * requirements, a scenario+case with two steps and one UI assertion, an
 * approved baseline (the version moves to `executing`), a form login recipe,
 * an account/access and one imported valid fixture session.
 */
export async function seedExecutionVersion(
  request: APIRequestContext,
  options: ExecutionSeedOptions = {},
): Promise<ExecutionSeed> {
  const suffix = options.suffix ?? uniqueSuffix()
  const fixtureUrl = options.fixtureUrl ?? fixtureTargetUrl()
  const seedSession = options.seedSession !== false

  // Reuse the proven P3/P5 seed: system + environment (fixture host allow-list)
  // + code repository + version bound to two confirmed requirements + designing.
  const base = await seedMindmapVersion(request, { suffix, fixtureUrl })
  const token = base.token

  // Keep the fixture host and allow auto-login for the session/takeover specs.
  const envResponse = await request.patch(
    `${API_URL}/api/resources/environments/${base.environmentId}`,
    { headers: authHeaders(token), data: { allowAutoLogin: true } },
  )
  await readData(envResponse, 200, "patch execution environment")

  const loginUrl = `${fixtureUrl}/p6/login`
  const authFlowResponse = await request.post(`${API_URL}/api/resources/auth-flows`, {
    headers: authHeaders(token),
    data: {
      system_id: String(base.systemId),
      name: `P6 login ${suffix}`,
      kind: "form",
      definition: {
        login_url: loginUrl,
        username_selector: "#username",
        password_selector: "#password",
        submit_selector: "button[type=submit]",
        success: { url_contains: "/p6/home" },
        failure: { invalid_selector: ".login-error", mfa_selector: "#mfa" },
        timeout_ms: 10_000,
      },
      enabled: true,
    },
  })
  const authFlow = await readData<{ id: string }>(authFlowResponse, 201, "create auth flow")
  expect(authFlow.id).toMatch(/^\d+$/)

  const accountResponse = await request.post(`${API_URL}/api/resources/accounts`, {
    headers: authHeaders(token),
    data: {
      key: `p6-account-${suffix}`,
      label: `P6 Account ${suffix}`,
      username: "fixture",
      kind: "human",
    },
  })
  const account = await readData<{ id: number }>(accountResponse, 201, "create account")

  const accessResponse = await request.post(
    `${API_URL}/api/resources/accounts/${account.id}/accesses`,
    {
      headers: authHeaders(token),
      data: {
        systemId: base.systemId,
        authType: "password",
        credential: "fixture-pass",
        usernameOverride: "fixture",
        loginUrl,
        login_flow_id: authFlow.id,
        enabled: true,
      },
    },
  )
  const access = await readData<{ id: number }>(accessResponse, 201, "create access")

  const scenario = await postMindmapNode(request, token, base.version.id, {
    kind: "scenario",
    title: `${options.caseTitle ?? "P6 下单流程"}场景`,
    description: "P6 E2E 执行场景",
  })
  const caseNode = await postMindmapNode(request, token, base.version.id, {
    kind: "case",
    parentId: scenario.id,
    title: options.caseTitle ?? "P6 登录后创建订单",
    priority: "P0",
    preconditions: "已通过 fixture 会话登录",
    requirementIds: [base.requirements[0]?.id].filter((id): id is string => id !== undefined),
  })
  const firstStep = await postMindmapStep(request, token, base.version.id, caseNode.id, {
    action: "打开 /p6/orders/new 并填写订单号",
    expected: "新建订单表单可见",
  })
  const secondStep = await postMindmapStep(request, token, base.version.id, caseNode.id, {
    action: "提交新建订单并确认创建成功",
    expected: "订单详情显示新订单号",
  })
  const assertion = await postMindmapAssertion(request, token, base.version.id, caseNode.id, {
    stepId: secondStep.id,
    kind: "ui",
    target: "#result",
    expression: "text contains 订单创建成功",
    expected: "订单创建成功",
  })

  const submitResponse = await request.post(
    `${API_URL}/api/versions/${base.version.id}/mindmap/submit-review`,
    { headers: authHeaders(token), data: {} },
  )
  await readData<{ version: VersionRecord }>(submitResponse, 200, "submit mindmap review")

  const approveResponse = await request.post(
    `${API_URL}/api/versions/${base.version.id}/mindmap/review`,
    {
      headers: authHeaders(token),
      data: { decision: "approve", acknowledgeFindings: true },
    },
  )
  const approved = await readData<{
    version: VersionRecord
    baseline: { id: string; graphHash: string } | null
  }>(approveResponse, 200, "approve mindmap")
  expect(approved.version.status, "approval must move the version to executing").toBe("executing")
  expect(approved.baseline, "approval must freeze a baseline").not.toBeNull()

  let sessionId = 0
  let sessionCookie = ""
  if (seedSession) {
    const imported = await importFixtureSession(request, token, {
      fixtureUrl,
      environmentId: base.environmentId,
      accessId: access.id,
    })
    sessionId = imported.sessionId
    sessionCookie = imported.cookie
  }

  return {
    token,
    suffix,
    fixtureUrl,
    systemId: base.systemId,
    environmentId: base.environmentId,
    version: approved.version,
    requirements: base.requirements,
    scenarioNodeId: scenario.id,
    caseNodeId: caseNode.id,
    stepIds: [firstStep.id, secondStep.id],
    assertionIds: [assertion.id],
    authFlowId: authFlow.id,
    accountId: account.id,
    accessId: access.id,
    sessionId,
    sessionCookie,
  }
}

// --- batches ---------------------------------------------------------------

/** POST /api/versions/{id}/batches — omitted `caseIds` executes every baseline case. */
export async function createBatch(
  request: APIRequestContext,
  token: string,
  versionId: string,
  caseIds?: string[],
): Promise<Batch> {
  const response = await request.post(`${API_URL}/api/versions/${versionId}/batches`, {
    headers: authHeaders(token),
    data: caseIds === undefined ? {} : { caseIds },
  })
  return readData<Batch>(response, 201, "create batch")
}

/** GET /api/versions/{id}/batches/{batchId}. */
export async function getBatch(
  request: APIRequestContext,
  token: string,
  versionId: string,
  batchId: string,
): Promise<Batch> {
  const response = await request.get(`${API_URL}/api/versions/${versionId}/batches/${batchId}`, {
    headers: authHeaders(token),
  })
  return readData<Batch>(response, 200, "get batch")
}

export type WaitBatchOptions = {
  timeoutMs?: number
  /** Additional statuses that end the wait (e.g. `paused` is not a status). */
  until?: BatchStatus[]
}

/**
 * Polls one batch until a terminal status (or an explicit `until` status).
 * `finished_at`/`counts` are read from the same detail response.
 */
export async function waitBatch(
  request: APIRequestContext,
  token: string,
  versionId: string,
  batchId: string,
  options: WaitBatchOptions = {},
): Promise<Batch> {
  const timeout = options.timeoutMs ?? 180_000
  const terminal = new Set<BatchStatus>(options.until ?? ["done", "failed", "cancelled"])
  const deadline = Date.now() + timeout
  let last: Batch | null = null
  while (Date.now() < deadline) {
    last = await getBatch(request, token, versionId, batchId)
    if (terminal.has(last.status)) return last
    await sleep(250)
  }
  throw new Error(
    `batch ${batchId} did not reach ${[...terminal].join("/")} within ${timeout}ms ` +
      `(last status: ${last?.status ?? "none"})`,
  )
}

/** POST .../pause — graceful by default; immediate interrupts the in-flight step. */
export async function pauseBatch(
  request: APIRequestContext,
  token: string,
  versionId: string,
  batchId: string,
  mode: PauseMode = "graceful",
): Promise<Batch> {
  const response = await request.post(
    `${API_URL}/api/versions/${versionId}/batches/${batchId}/pause`,
    { headers: authHeaders(token), data: { mode } },
  )
  return readData<Batch>(response, 200, "pause batch")
}

/** POST .../resume — only a paused batch can resume. */
export async function resumeBatch(
  request: APIRequestContext,
  token: string,
  versionId: string,
  batchId: string,
): Promise<Batch> {
  const response = await request.post(
    `${API_URL}/api/versions/${versionId}/batches/${batchId}/resume`,
    { headers: authHeaders(token), data: {} },
  )
  return readData<Batch>(response, 200, "resume batch")
}

// --- case runs / evidence / report / events --------------------------------

/** GET .../case-runs — one page of runs (useful to discover case-run ids). */
export async function listCaseRuns(
  request: APIRequestContext,
  token: string,
  versionId: string,
  batchId: string,
  options: { status?: CaseRunStatus; limit?: number } = {},
): Promise<CaseRun[]> {
  const params = new URLSearchParams()
  if (options.status !== undefined) params.set("status", options.status)
  params.set("limit", String(options.limit ?? 100))
  const response = await request.get(
    `${API_URL}/api/versions/${versionId}/batches/${batchId}/case-runs?${params.toString()}`,
    { headers: authHeaders(token) },
  )
  expect(response.status(), "list case runs").toBe(200)
  const body = (await response.json()) as { data: CaseRun[] }
  return Array.isArray(body.data) ? body.data : []
}

/** GET .../case-runs/{caseRunId} — steps, assertions, evidence and takeover. */
export async function getCaseRun(
  request: APIRequestContext,
  token: string,
  versionId: string,
  batchId: string,
  caseRunId: string,
): Promise<CaseRun> {
  const response = await request.get(
    `${API_URL}/api/versions/${versionId}/batches/${batchId}/case-runs/${caseRunId}`,
    { headers: authHeaders(token) },
  )
  return readData<CaseRun>(response, 200, "get case run")
}

/** Polls one case run until it reaches a requested (default: terminal) status. */
export async function waitCaseRun(
  request: APIRequestContext,
  token: string,
  versionId: string,
  batchId: string,
  caseRunId: string,
  options: { timeoutMs?: number; until?: CaseRunStatus[] } = {},
): Promise<CaseRun> {
  const timeout = options.timeoutMs ?? 180_000
  const terminal = new Set<CaseRunStatus>(
    options.until ?? ["passed", "failed", "blocked", "inconclusive", "skipped", "interrupted"],
  )
  const deadline = Date.now() + timeout
  let last: CaseRun | null = null
  while (Date.now() < deadline) {
    last = await getCaseRun(request, token, versionId, batchId, caseRunId)
    if (terminal.has(last.status)) return last
    await sleep(250)
  }
  throw new Error(
    `case run ${caseRunId} did not reach ${[...terminal].join("/")} within ${timeout}ms ` +
      `(last status: ${last?.status ?? "none"})`,
  )
}

/** GET /api/versions/{id}/evidence/{evidenceId} — the raw artifact bytes. */
export async function getEvidence(
  request: APIRequestContext,
  token: string,
  versionId: string,
  evidenceId: string,
): Promise<EvidenceDownload> {
  const response = await request.get(
    `${API_URL}/api/versions/${versionId}/evidence/${evidenceId}`,
    {
      headers: authHeaders(token),
    },
  )
  expect(response.status(), `get evidence ${evidenceId}`).toBe(200)
  const headers = response.headers()
  return {
    evidenceId,
    body: await response.body(),
    contentType: headers["content-type"] ?? "",
    sha256: headers["x-content-sha256"] ?? "",
  }
}

/** GET .../report — the Batch 1 report JSON. */
export async function getReport(
  request: APIRequestContext,
  token: string,
  versionId: string,
  batchId: string,
): Promise<ReportView> {
  const response = await request.get(
    `${API_URL}/api/versions/${versionId}/batches/${batchId}/report`,
    { headers: authHeaders(token) },
  )
  return readData<ReportView>(response, 200, "get batch report")
}

/**
 * GET /api/versions/{id}/events — the persisted event snapshot, newest page.
 * The platform caps `limit` at 100, so larger values are narrowed here.
 */
export async function readVersionEvents(
  request: APIRequestContext,
  token: string,
  versionId: string,
  options: { limit?: number; types?: string[] } = {},
): Promise<VersionEvent[]> {
  const limit = Math.min(options.limit ?? 100, 100)
  const response = await request.get(`${API_URL}/api/versions/${versionId}/events?limit=${limit}`, {
    headers: authHeaders(token),
  })
  expect(response.status(), "read version events").toBe(200)
  const body = (await response.json()) as { data?: VersionEvent[] }
  const events = Array.isArray(body.data) ? body.data : []
  if (options.types === undefined) return events
  return events.filter((event) => options.types?.includes(event.type) === true)
}

// --- fake LLM P6 mode ------------------------------------------------------

/** Reads the active fake LLM P6 mode through `GET /__p6-mode`. */
export async function getP6Mode(request: APIRequestContext): Promise<string> {
  const response = await request.get(`${fakeLlmUrl()}/__p6-mode`)
  expect(response.status(), "GET fake P6 mode").toBe(200)
  const body = (await response.json()) as { mode?: string }
  return body.mode ?? ""
}

/**
 * Switches the fake LLM P6 scenario through `POST /__p6-mode`. Specs that
 * change it must reset it to `normal` afterwards.
 */
export async function setP6Mode(
  request: APIRequestContext,
  mode: "normal" | "fail" | "blocked" | "takeover" | "budget",
): Promise<void> {
  const response = await request.post(`${fakeLlmUrl()}/__p6-mode`, { data: { mode } })
  expect(response.status(), `set fake P6 mode ${mode}`).toBe(200)
  const body = (await response.json()) as { mode?: string }
  expect(body.mode).toBe(mode)
}

/** Sleep helper for the polling helpers above. */
export function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}
