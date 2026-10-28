import { type APIRequestContext, type APIResponse, expect, test } from "@playwright/test"

import {
  API_URL,
  adminApiToken,
  createCodeRepo,
  createRequirement,
  createSystem,
  FAKE_LLM_URL,
  p3HarnessSkipReason,
  prepareGitRepo,
  type RequirementRecord,
  startVersionClarification,
  uniqueSuffix,
  type VersionRecord,
} from "./p3-helpers"
import { authHeaders, createP4AppModel, type P4AppModelNode } from "./p4-helpers"

/**
 * Shared P5 mindmap E2E helpers (design spec §10.2/§10.5). Every helper talks
 * to the real P3/P2/P5 API and the deterministic fake LLM upstream of
 * `scripts/fake-llm.cjs`; nothing here mocks the backend. IDs stay decimal
 * strings throughout (D29).
 */

/** Fixture target base URL used by the exploration seed (spec §10.2). */
export function fixtureTargetUrl(): string {
  const configured = process.env.EZTEST_E2E_FIXTURE_URL?.trim()
  if (configured !== undefined && configured !== "") return configured.replace(/\/+$/, "")
  return "http://127.0.0.1:13335"
}

/**
 * Skips the calling spec when the deterministic harness (admin credentials,
 * fake LLM) is unavailable, so a locally started browser run never silently
 * talks to a real provider.
 */
export async function p5HarnessSkipReason(): Promise<string> {
  return p3HarnessSkipReason()
}

/** Marks the whole spec file skipped with a reason when the harness is absent. */
export async function skipMindmapSpecWhenHarnessMissing(): Promise<boolean> {
  const reason = await p5HarnessSkipReason()
  if (reason === "") return false
  test.skip(true, reason)
  return true
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

// --- mindmap DTOs (hand-written mirrors of the OpenAPI contract, spec §7.3) --

export type MindmapStep = {
  id: string
  seq: number
  action: string
  expected: string
  data: string
  source: string
}

export type MindmapAssertion = {
  id: string
  stepId: string | null
  kind: string
  target: string
  expression: string
  expected: string
  source: string
}

export type MindmapSource = {
  id: string
  refKind: string
  refId: string
  refKey: string
  source: string
  confidence?: string
}

export type MindmapAnchor = {
  id: string
  filePath: string
  startLine: number | null
  endLine: number | null
  confidence: string
  commitSha?: string
}

export type MindmapNode = {
  id: string
  parentId: string | null
  kind: "scenario" | "case"
  title: string
  description?: string
  priority: string
  preconditions?: string
  testData?: unknown
  source: string
  status: string
  sortOrder: number
  revision: number
  steps: MindmapStep[]
  assertions: MindmapAssertion[]
  sources: MindmapSource[]
  anchors: MindmapAnchor[]
  openBlockerCount: number
}

export type MindmapCoverage = {
  requirementIds: string[]
  uncoveredRequirementIds: string[]
  staleRequirementIds: string[]
  matrix: Array<{ requirementId: string; caseIds: string[] }>
}

export type MindmapBaseline = {
  id: string
  graphHash: string
  caseCount: number
  nodeCount: number
  frozenAt: string
  codeBaselineCommit?: string
}

export type MindmapGraph = {
  versionId: string
  versionStatus: string
  editable: boolean
  nodes: MindmapNode[]
  coverage: MindmapCoverage
  baseline: MindmapBaseline | null
}

export type MindmapRunStatus = "queued" | "running" | "done" | "failed" | "degraded"

export type MindmapRun = {
  id?: string
  runId?: string
  kind?: string
  status: MindmapRunStatus
  reason?: string
  stats?: Record<string, unknown>
  errors?: Array<{ code?: string; message?: string }>
  startedAt?: string | null
  finishedAt?: string | null
}

export type MindmapRunKind = "design" | "review" | "data_plan" | "explore"

export type StartMindmapRunInput = {
  kind: MindmapRunKind
  mode?: string
  confirmReplace?: boolean
  nodeIds?: string[]
  systemId?: number
  environmentId?: number
  sessionId?: string
  startPaths?: string[]
  headCommit?: string
}

export type StartedMindmapRun = { runId: string; status: string }

export type MindmapExportFormat = "markdown" | "mermaid" | "opml"

export type MindmapExport = {
  id: string
  versionId?: string
  format?: string
  sizeBytes?: number
  sha256?: string
  baselineId?: string | null
  createdAt?: string
}

export type MindmapExportDownload = {
  /** The persisted export metadata returned by POST /export. */
  export: MindmapExport
  /** The raw downloaded bytes (spec §7.2 `GET /exports/{exportId}/download`). */
  body: Buffer
  filename: string
  contentType: string
}

export type MindmapCaseEvent = {
  seq: number
  type: string
  summary: string
  detail?: Record<string, unknown> | null
  refs?: { case?: string[] }
}

// --- seed ------------------------------------------------------------------

export type MindmapSeedOptions = {
  /** Unique suffix for the seeded resource keys; defaults to `uniqueSuffix()`. */
  suffix?: string
  /** Fixture target origin; defaults to `EZTEST_E2E_FIXTURE_URL` or :13335. */
  fixtureUrl?: string
  /** Reuse an existing code repository instead of creating one. */
  codeRepositoryId?: number
  /** Requirement titles, in creation order (two confirmed requirements). */
  requirementTitles?: [string, string]
}

export type MindmapSeed = {
  token: string
  suffix: string
  fixtureUrl: string
  systemId: number
  environmentId: number
  codeRepositoryId: number
  /** The version after entering `designing` (design runs are allowed). */
  version: VersionRecord
  /** The two clarified requirements created for the version. */
  requirements: RequirementRecord[]
  /** The seeded P2 application model nodes (page/route/api). */
  appModels: P4AppModelNode[]
}

type P2Environment = { id: number; name: string; baseUrl: string; allowedHosts: string[] }

export type CodeRepoFixture = { localPath: string; branch: string; commit: string }

/**
 * Resolves the code repository fixture. `scripts/e2e.ps1` exports the P4
 * codeintel fixture repository (design spec §10.2); a direct Playwright run
 * falls back to the throwaway P3 repository helper.
 */
export function p5CodeRepoFixture(): CodeRepoFixture {
  const repo = process.env.EZTEST_E2E_P4_REPO?.trim() ?? ""
  const commit = process.env.EZTEST_E2E_P4_HEAD_COMMIT?.trim() ?? ""
  if (repo !== "" && commit !== "") {
    expect(commit).toMatch(/^[0-9a-f]{40}$/)
    return {
      localPath: repo,
      branch: process.env.EZTEST_E2E_P4_BRANCH?.trim() || "main",
      commit,
    }
  }
  const fallback = prepareGitRepo()
  return { localPath: fallback.path, branch: fallback.branch, commit: fallback.commit }
}

/** Creates one P2 environment with the fixture host allow-listed (spec §6.9). */
async function createP5Environment(
  request: APIRequestContext,
  token: string,
  systemId: number,
  input: { key: string; name: string; baseUrl: string; allowedHosts: string[] },
): Promise<P2Environment> {
  const response = await request.post(`${API_URL}/api/resources/systems/${systemId}/environments`, {
    headers: authHeaders(token),
    data: { kind: "dev", ...input },
  })
  return readData<P2Environment>(response, 201, "create P5 environment")
}

/** Creates one version bound to the seeded resources with an explicit baseline. */
async function createP5Version(
  request: APIRequestContext,
  token: string,
  input: {
    name: string
    label: string
    description: string
    systemId: number
    environmentId: number
    codeRepositoryId: number
    codeBranch?: string
    codeBaselineCommit?: string
  },
): Promise<VersionRecord> {
  const response = await request.post(`${API_URL}/api/versions`, {
    headers: authHeaders(token),
    data: input,
  })
  return readData<VersionRecord>(response, 201, "create P5 version")
}

/** POST /api/versions/{id}/requirements/{rid}/confirm (P3 gate, spec §6.2). */
async function confirmP5Requirement(
  request: APIRequestContext,
  token: string,
  versionId: string,
  requirementId: string,
): Promise<RequirementRecord> {
  const response = await request.post(
    `${API_URL}/api/versions/${versionId}/requirements/${requirementId}/confirm`,
    { headers: authHeaders(token) },
  )
  return readData<RequirementRecord>(response, 200, "confirm P5 requirement")
}

/** POST /api/versions/{id}/enter-design, returning the `designing` version. */
async function enterP5Design(
  request: APIRequestContext,
  token: string,
  versionId: string,
): Promise<VersionRecord> {
  const response = await request.post(`${API_URL}/api/versions/${versionId}/enter-design`, {
    headers: authHeaders(token),
    data: { acknowledgeUnclarified: false },
  })
  const data = await readData<{ version: VersionRecord; warnings: string[] }>(
    response,
    200,
    "enter P5 design",
  )
  return data.version
}

/**
 * Resolves (or creates) the P5 code repository binding. The version stores the
 * fixture branch/commit as its baseline, so the repository is resolved exactly
 * once per seed: re-preparing the throwaway repo would change its HEAD.
 */
async function ensureP5CodeRepository(
  request: APIRequestContext,
  token: string,
  suffix: string,
  existingId?: number,
): Promise<{ id: number; fixture: CodeRepoFixture | null }> {
  if (existingId !== undefined) return { id: existingId, fixture: null }
  const fixture = p5CodeRepoFixture()
  const repo = await createCodeRepo(request, token, {
    key: `p5-repo-${suffix}`,
    name: `P5 Repo ${suffix}`,
    localPath: fixture.localPath,
    fixedBranch: fixture.branch,
  })
  return { id: repo.id, fixture }
}

/**
 * Seeds the full mindmap fixture through the real APIs (spec §10.2/§10.5 step
 * 1): system, environment, application model (page/route/api), code repository,
 * a version bound to all of them, and two clarified requirements. The version
 * is moved `draft -> clarifying -> designing` so design runs and manual editing
 * are allowed but no baseline exists yet.
 */
export async function seedMindmapVersion(
  request: APIRequestContext,
  options: MindmapSeedOptions = {},
): Promise<MindmapSeed> {
  const token = await adminApiToken(request)
  const suffix = options.suffix ?? uniqueSuffix()
  const fixtureUrl = options.fixtureUrl ?? fixtureTargetUrl()
  const [firstTitle, secondTitle] = options.requirementTitles ?? ["P5 下单主流程", "P5 退款流程"]

  const system = await createSystem(request, token, {
    key: `p5-sys-${suffix}`,
    name: `P5 System ${suffix}`,
    baseUrl: fixtureUrl,
  })

  const environment = await createP5Environment(request, token, system.id, {
    key: `p5-env-${suffix}`,
    name: `P5 Environment ${suffix}`,
    baseUrl: fixtureUrl,
    allowedHosts: [new URL(fixtureUrl).host],
  })

  // The seeded keys deliberately do not collide with the `/p5/*` fixture that
  // the fake exploration scenario discovers, so an explore run can still add
  // nodes instead of skipping every duplicate (spec §6.9 rule 6).
  const appModels = [
    await createP4AppModel(request, token, system.id, {
      key: "page:/home",
      kind: "page",
      name: "Home",
      path: "/home",
    }),
    await createP4AppModel(request, token, system.id, {
      key: "route:/orders",
      kind: "route",
      name: "Orders",
      path: "/orders",
    }),
    await createP4AppModel(request, token, system.id, {
      key: "api:GET /api/profile",
      kind: "api",
      name: "GET /api/profile",
      path: "/api/profile",
      method: "GET",
    }),
  ]

  const codeRepo = await ensureP5CodeRepository(request, token, suffix, options.codeRepositoryId)

  const version = await createP5Version(request, token, {
    name: `P5 Version ${suffix}`,
    label: `p5-version-${suffix}`,
    description: "P5 E2E mindmap version",
    systemId: system.id,
    environmentId: environment.id,
    codeRepositoryId: codeRepo.id,
    codeBranch: codeRepo.fixture?.branch,
    codeBaselineCommit: codeRepo.fixture?.commit,
  })

  const requirements = [
    await createRequirement(request, token, version.id, {
      title: firstTitle,
      description: "用户提交订单时创建订单并扣减库存。",
      acceptanceCriteria: "订单创建成功后库存数量减少。",
      priority: "must",
    }),
    await createRequirement(request, token, version.id, {
      title: secondTitle,
      description: "用户申请退款后原路退回支付金额。",
      acceptanceCriteria: "退款成功后支付金额原路退回。",
      priority: "should",
    }),
  ]

  // draft -> clarifying (needs at least one requirement) -> confirm -> designing.
  await startVersionClarification(request, token, version.id)
  for (const requirement of requirements) {
    await confirmP5Requirement(request, token, version.id, requirement.id)
  }
  const designing = await enterP5Design(request, token, version.id)

  return {
    token,
    suffix,
    fixtureUrl,
    systemId: system.id,
    environmentId: environment.id,
    codeRepositoryId: codeRepo.id,
    version: designing,
    requirements,
    appModels,
  }
}

// --- runs ------------------------------------------------------------------

/** POST /api/versions/{id}/mindmap/runs (202, spec §7.2 row 17). */
export async function startMindmapRun(
  request: APIRequestContext,
  token: string,
  versionId: string,
  input: StartMindmapRunInput,
): Promise<StartedMindmapRun> {
  const response = await request.post(`${API_URL}/api/versions/${versionId}/mindmap/runs`, {
    headers: authHeaders(token),
    data: input,
  })
  const data = await readData<{ runId?: string; id?: string; status?: string }>(
    response,
    [200, 202],
    "start mindmap run",
  )
  const runId = data.runId ?? data.id ?? ""
  expect(runId).toMatch(/^\d+$/)
  return { runId, status: data.status ?? "queued" }
}

/** Starts the `design` run generated by the fake LLM P5 design scenario. */
export async function startDesignRun(
  request: APIRequestContext,
  token: string,
  versionId: string,
  options: { mode?: string; confirmReplace?: boolean } = {},
): Promise<StartedMindmapRun> {
  return startMindmapRun(request, token, versionId, { kind: "design", ...options })
}

/** Starts the `review` run generated by the fake LLM P5 review scenario. */
export async function startReviewRun(
  request: APIRequestContext,
  token: string,
  versionId: string,
): Promise<StartedMindmapRun> {
  return startMindmapRun(request, token, versionId, { kind: "review" })
}

/** GET /api/versions/{id}/mindmap/runs/{runId} (run detail). */
export async function getMindmapRun(
  request: APIRequestContext,
  token: string,
  versionId: string,
  runId: string,
): Promise<MindmapRun> {
  const response = await request.get(
    `${API_URL}/api/versions/${versionId}/mindmap/runs/${encodeURIComponent(runId)}`,
    { headers: authHeaders(token) },
  )
  return readData<MindmapRun>(response, 200, "get mindmap run")
}

/**
 * Polls one run until `done`/`failed`/`degraded`. A `failed` run throws with its
 * stable reason unless the caller explicitly allows it (degraded specs).
 */
export async function waitMindmapRun(
  request: APIRequestContext,
  token: string,
  versionId: string,
  runId: string,
  options: { timeoutMs?: number; allowFailed?: boolean } = {},
): Promise<MindmapRun> {
  const timeout = options.timeoutMs ?? 180_000
  await expect
    .poll(async () => (await getMindmapRun(request, token, versionId, runId)).status, {
      timeout,
      intervals: [250, 500, 1000, 2000],
    })
    .toMatch(/^(done|failed|degraded)$/)

  const run = await getMindmapRun(request, token, versionId, runId)
  if (run.status === "failed" && options.allowFailed !== true) {
    const codes = (run.errors ?? []).map((error) => error.code ?? "").filter(Boolean)
    throw new Error(
      `mindmap run ${runId} failed: ${run.reason ?? "unknown reason"}${
        codes.length > 0 ? ` (${codes.join(", ")})` : ""
      }`,
    )
  }
  return run
}

/** Polls a design run to its terminal state (see `waitMindmapRun`). */
export async function waitDesignRun(
  request: APIRequestContext,
  token: string,
  versionId: string,
  runId: string,
  options: { timeoutMs?: number; allowFailed?: boolean } = {},
): Promise<MindmapRun> {
  return waitMindmapRun(request, token, versionId, runId, options)
}

// --- graph / review / export ----------------------------------------------

/** GET /api/versions/{id}/mindmap (`include` is a comma-separated subset). */
export async function getMindmap(
  request: APIRequestContext,
  token: string,
  versionId: string,
  include?: string,
): Promise<MindmapGraph> {
  const query = include === undefined ? "" : `?include=${encodeURIComponent(include)}`
  const response = await request.get(`${API_URL}/api/versions/${versionId}/mindmap${query}`, {
    headers: authHeaders(token),
  })
  return readData<MindmapGraph>(response, 200, "get mindmap")
}

/** POST /api/versions/{id}/mindmap/submit-review (designing -> reviewing). */
export async function submitMindmapReview(
  request: APIRequestContext,
  token: string,
  versionId: string,
): Promise<VersionRecord> {
  const response = await request.post(
    `${API_URL}/api/versions/${versionId}/mindmap/submit-review`,
    {
      headers: authHeaders(token),
      data: {},
    },
  )
  const data = await readData<{ version: VersionRecord }>(response, 200, "submit mindmap review")
  return data.version
}

export type MindmapApproveResult = {
  version: VersionRecord
  baseline: MindmapBaseline | null
  review: { id?: string; status?: string } | null
}

/**
 * POST /api/versions/{id}/mindmap/review with `decision=approve`. Blockers are
 * acknowledged by default: the fake LLM review always reports one
 * `code_uncovered` blocker (spec §10.6), so the E2E passes
 * `acknowledgeFindings: false` to assert the 409 gate first.
 */
export async function approveMindmap(
  request: APIRequestContext,
  token: string,
  versionId: string,
  options: { comment?: string; acknowledgeFindings?: boolean } = {},
): Promise<MindmapApproveResult> {
  const response = await request.post(`${API_URL}/api/versions/${versionId}/mindmap/review`, {
    headers: authHeaders(token),
    data: {
      decision: "approve",
      acknowledgeFindings: options.acknowledgeFindings ?? true,
      ...(options.comment === undefined ? {} : { comment: options.comment }),
    },
  })
  const data = await readData<Partial<MindmapApproveResult>>(response, 200, "approve mindmap")
  if (data.version === undefined) throw new Error("approve response is missing the version")
  return {
    version: data.version,
    baseline: data.baseline ?? null,
    review: data.review ?? null,
  }
}

/** Extracts the filename of one Content-Disposition header. */
function filenameFromDisposition(header: string | undefined): string {
  if (header === undefined) return ""
  const quoted = /filename="([^"]+)"/i.exec(header)
  if (quoted !== null) return quoted[1]
  const plain = /filename=([^;]+)/i.exec(header)
  return plain === null ? "" : plain[1].trim()
}

/**
 * POST /api/versions/{id}/mindmap/export then downloads the resulting file, so
 * the caller can assert the persisted bytes (spec §10.5 step 6).
 */
export async function exportMindmap(
  request: APIRequestContext,
  token: string,
  versionId: string,
  format: MindmapExportFormat = "markdown",
): Promise<MindmapExportDownload> {
  const response = await request.post(`${API_URL}/api/versions/${versionId}/mindmap/export`, {
    headers: authHeaders(token),
    data: { format },
  })
  const data = await readData<MindmapExport>(response, [200, 201], "export mindmap")
  expect(data.id).toMatch(/^\d+$/)

  const download = await request.get(
    `${API_URL}/api/versions/${versionId}/mindmap/exports/${data.id}/download`,
    { headers: authHeaders(token) },
  )
  expect(download.status(), "download mindmap export").toBe(200)
  const headers = download.headers()
  const filename = filenameFromDisposition(headers["content-disposition"])

  return {
    export: data,
    body: await download.body(),
    filename: filename === "" ? `${format}-${data.id}` : filename,
    contentType: headers["content-type"] ?? "",
  }
}

// --- events ----------------------------------------------------------------

/** GET /api/versions/{id}/events, optionally filtered to mindmap event types. */
export async function readCaseEvents(
  request: APIRequestContext,
  token: string,
  versionId: string,
  options: { limit?: number; types?: string[] } = {},
): Promise<MindmapCaseEvent[]> {
  const limit = options.limit ?? 200
  const response = await request.get(`${API_URL}/api/versions/${versionId}/events?limit=${limit}`, {
    headers: authHeaders(token),
  })
  expect(response.status()).toBe(200)
  const body = (await response.json()) as { data?: MindmapCaseEvent[] }
  const events = Array.isArray(body.data) ? body.data : []
  if (options.types === undefined) return events
  return events.filter((event) => options.types?.includes(event.type) === true)
}

/** Polls the event snapshot until one event matches, then returns the snapshot. */
export async function waitForCaseEvents(
  request: APIRequestContext,
  token: string,
  versionId: string,
  predicate: (event: MindmapCaseEvent) => boolean,
  options: { timeoutMs?: number; limit?: number } = {},
): Promise<MindmapCaseEvent[]> {
  await expect
    .poll(
      async () =>
        (await readCaseEvents(request, token, versionId, { limit: options.limit })).some(predicate),
      { timeout: options.timeoutMs ?? 60_000, intervals: [250, 500, 1000, 2000] },
    )
    .toBe(true)
  return readCaseEvents(request, token, versionId, { limit: options.limit })
}

// --- fake LLM P5 mode ------------------------------------------------------

/** Reads the active fake LLM P5 mode through the test-only `GET /__p5-mode`. */
export async function getP5Mode(request: APIRequestContext): Promise<string> {
  const response = await request.get(`${FAKE_LLM_URL}/__p5-mode`)
  expect(response.status(), "GET fake P5 mode").toBe(200)
  const body = (await response.json()) as { mode?: string }
  return body.mode ?? ""
}

/**
 * Switches the fake LLM P5 scenario through `POST /__p5-mode` and verifies the
 * applied mode. Degraded/invalid specs must reset it to `normal` afterwards.
 */
export async function setP5Mode(
  request: APIRequestContext,
  mode: "normal" | "invalid" | "degraded",
): Promise<void> {
  const response = await request.post(`${FAKE_LLM_URL}/__p5-mode`, { data: { mode } })
  expect(response.status(), `set fake P5 mode ${mode}`).toBe(200)
  const body = (await response.json()) as { mode?: string }
  expect(body.mode).toBe(mode)
}
