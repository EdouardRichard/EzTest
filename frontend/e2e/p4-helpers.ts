import { readFileSync } from "node:fs"

import { type APIRequestContext, type APIResponse, expect, test } from "@playwright/test"

import {
  API_URL,
  FAKE_LLM_URL,
  FAKE_REQUEST_LOG,
  p3HarnessSkipReason,
  type VersionRecord,
} from "./p3-helpers"

/**
 * Shared P4 code-intelligence E2E helpers (design spec §10.2/§10.5). The
 * fixture repository and its pinned commits are prepared by
 * `scripts/e2e.ps1`/`scripts/fixture-codeintel-repo.ps1` and passed through the
 * `EZTEST_E2E_P4_*` environment variables; every helper talks to the real API
 * and the deterministic fake LLM upstream, never to a mock.
 */

export type P4Fixture = {
  /** Absolute fixture checkout path with forward slashes. */
  repo: string
  baseCommit: string
  headCommit: string
  branch: string
}

function readP4Env(): { repo: string; base: string; head: string; branch: string } {
  return {
    repo: process.env.EZTEST_E2E_P4_REPO?.trim() ?? "",
    base: process.env.EZTEST_E2E_P4_BASE_COMMIT?.trim() ?? "",
    head: process.env.EZTEST_E2E_P4_HEAD_COMMIT?.trim() ?? "",
    branch: process.env.EZTEST_E2E_P4_BRANCH?.trim() ?? "main",
  }
}

/** Reads and validates the fixture coordinates exported by `scripts/e2e.ps1`. */
export function p4Fixture(): P4Fixture {
  const values = readP4Env()
  if (values.repo === "" || values.base === "" || values.head === "") {
    throw new Error(
      "EZTEST_E2E_P4_REPO/BASE_COMMIT/HEAD_COMMIT must be set; run via scripts/e2e.ps1",
    )
  }
  expect(values.base).toMatch(/^[0-9a-f]{40}$/)
  expect(values.head).toMatch(/^[0-9a-f]{40}$/)
  return {
    repo: values.repo,
    baseCommit: values.base,
    headCommit: values.head,
    branch: values.branch,
  }
}

/**
 * Skips the calling spec when the deterministic harness (admin credentials,
 * fake LLM) or the P4 fixture repository is unavailable, so a locally started
 * browser run never silently talks to a real provider.
 */
export async function skipP4SpecWhenHarnessMissing(): Promise<boolean> {
  const reason = await p3HarnessSkipReason()
  if (reason !== "") {
    test.skip(true, reason)
    return true
  }
  const values = readP4Env()
  if (values.repo === "" || values.base === "" || values.head === "") {
    test.skip(true, "EZTEST_E2E_P4_* is not set; run via scripts/e2e.ps1")
    return true
  }
  return false
}

export function authHeaders(token: string): Record<string, string> {
  return { Authorization: `Bearer ${token}` }
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

/** One P4 app model node created through the real resource API. */
export type P4AppModelNode = {
  id: number
  systemId: number
  kind: string
  key: string
  name: string
  path: string
  method: string
}

/** Creates one app model node through the real admin resource API. */
export async function createP4AppModel(
  request: APIRequestContext,
  token: string,
  systemId: number,
  node: { key: string; kind: string; name: string; path?: string; method?: string },
): Promise<P4AppModelNode> {
  const response = await request.post(`${API_URL}/api/resources/systems/${systemId}/app-models`, {
    headers: authHeaders(token),
    data: node,
  })
  return readData<P4AppModelNode>(response, 201, "create app model")
}

/** Input for one P4-bound version; an explicit baseline is required by §10.2. */
export type P4VersionInput = {
  name: string
  label: string
  description?: string
  systemId?: number
  codeRepositoryId: number
  codeBranch: string
  codeBaselineCommit: string
}

/** Creates a draft version bound to a repository with an explicit baseline. */
export async function createP4Version(
  request: APIRequestContext,
  token: string,
  input: P4VersionInput,
): Promise<VersionRecord> {
  const response = await request.post(`${API_URL}/api/versions`, {
    headers: authHeaders(token),
    data: input,
  })
  return readData<VersionRecord>(response, 201, "create P4 version")
}

/** One persisted T0 index run (`IndexRunDTO`). */
export type P4IndexRun = {
  runId: string
  repoId: number
  commit: string
  source: string
  status: string
  stats: { files: number; truncated: boolean } | null
  errors: Array<{ code: string; path?: string; message: string }>
  startedAt: string | null
  finishedAt: string | null
}

export type P4IndexAccepted = {
  runId: string
  status: string
  commit: string
}

/** POST /api/code/repos/{repoId}/index — triggers one T0 index (admin). */
export async function startRepoIndex(
  request: APIRequestContext,
  token: string,
  repoId: number,
): Promise<P4IndexAccepted> {
  const response = await request.post(`${API_URL}/api/code/repos/${repoId}/index`, {
    headers: authHeaders(token),
  })
  return readData<P4IndexAccepted>(response, 202, "start repo index")
}

/** GET /api/code/repos/{repoId}/index-runs/latest. */
export async function getLatestIndexRun(
  request: APIRequestContext,
  token: string,
  repoId: number,
): Promise<P4IndexRun> {
  const response = await request.get(`${API_URL}/api/code/repos/${repoId}/index-runs/latest`, {
    headers: authHeaders(token),
  })
  return readData<P4IndexRun>(response, 200, "latest index run")
}

export type P4IndexObservation = {
  accepted: P4IndexAccepted
  run: P4IndexRun
  /** Every distinct status observed from the trigger until the terminal state. */
  statuses: string[]
}

/**
 * Triggers an index run and polls it to a terminal state. The accepted status
 * (queued) and every later status (running/done) are collected so a caller can
 * assert the state machine without racing the fast fixture index.
 */
export async function triggerAndWaitForIndex(
  request: APIRequestContext,
  token: string,
  repoId: number,
  timeoutMs = 60_000,
): Promise<P4IndexObservation> {
  const accepted = await startRepoIndex(request, token, repoId)
  const statuses = [accepted.status]
  const deadline = Date.now() + timeoutMs
  let run: P4IndexRun | null = null
  while (Date.now() < deadline) {
    run = await getLatestIndexRun(request, token, repoId)
    if (statuses[statuses.length - 1] !== run.status) statuses.push(run.status)
    if (run.status === "done") return { accepted, run, statuses }
    if (run.status === "failed") {
      throw new Error(
        `index run ${run.runId} failed: ${run.errors.map((error) => error.code).join(", ")}`,
      )
    }
    await sleep(50)
  }
  throw new Error(`index run for repo ${repoId} did not finish within ${timeoutMs}ms`)
}

/** One persisted impact candidate (`CandidateDTO`). */
export type P4Candidate = {
  id: string
  subjectKind: string
  subjectId: string
  reason: string
  confidence: string
  source: string
  evidence: Array<{ file: string; startLine?: number; endLine?: number; excerptHash?: string }>
  selected: boolean
}

/** One unmapped change entry (`UnmappedChangeDTO`). */
export type P4UnmappedChange = {
  path: string
  status: string
  reason: string
  suggestion: string
}

/** One completed/failed analysis (`AnalysisDTO`). */
export type P4Analysis = {
  id: string
  versionId: string
  repoId: number
  baseCommit: string
  headCommit: string
  status: string
  degraded: boolean
  analysisVersion: number
  model: string
  promptVersion: string
  changeSetId: string | null
  changeSummary: string
  stats: {
    changedFiles: number
    analyzedFiles: number
    cacheHits: number
    facts: number
    edges: number
    contracts: number
    evidenceChecks: number
    evidenceFailures: number
    unmapped: number
    invalidFacts: number
    duplicateFacts: number
    unmatchedContracts: number
    truncated: boolean
    skipped: number
  }
  factVersions: Array<{ factKey: string; version: number }>
  candidates: P4Candidate[]
  unmapped: P4UnmappedChange[]
  errors: Array<{ code: string; path?: string; message: string }>
  createdAt: string
  finishedAt: string | null
}

export type P4StartAnalysisResult = {
  id: string
  status: string
  analysisVersion: number
  reused: boolean
}

export type P4StartAnalysisResponse = {
  /** 202 for a new run, 200 for a reused one (spec §7.3). */
  status: number
  data: P4StartAnalysisResult
}

/** POST /api/versions/{id}/code/analyses — 202 new / 200 reused. */
export async function startP4Analysis(
  request: APIRequestContext,
  token: string,
  versionId: string,
  body: { headCommit?: string; force?: boolean } = {},
): Promise<P4StartAnalysisResponse> {
  const response = await request.post(`${API_URL}/api/versions/${versionId}/code/analyses`, {
    headers: authHeaders(token),
    data: body,
  })
  const data = await readData<P4StartAnalysisResult>(response, [200, 202], "start code analysis")
  return { status: response.status(), data }
}

/** GET /api/versions/{id}/code/analyses/latest; null on the documented 404. */
export async function getLatestAnalysis(
  request: APIRequestContext,
  token: string,
  versionId: string,
): Promise<P4Analysis | null> {
  const response = await request.get(`${API_URL}/api/versions/${versionId}/code/analyses/latest`, {
    headers: authHeaders(token),
  })
  if (response.status() === 404) return null
  return readData<P4Analysis>(response, 200, "latest code analysis")
}

/** GET /api/versions/{id}/code/analyses/{analysisId}. */
export async function getAnalysisById(
  request: APIRequestContext,
  token: string,
  versionId: string,
  analysisId: string,
): Promise<P4Analysis> {
  const response = await request.get(
    `${API_URL}/api/versions/${versionId}/code/analyses/${encodeURIComponent(analysisId)}`,
    { headers: authHeaders(token) },
  )
  return readData<P4Analysis>(response, 200, "code analysis detail")
}

/** Polls the latest (or a specific) analysis until it reaches a terminal state. */
export async function waitForAnalysis(
  request: APIRequestContext,
  token: string,
  versionId: string,
  analysisId?: string,
  timeoutMs = 120_000,
): Promise<P4Analysis> {
  const deadline = Date.now() + timeoutMs
  let last: P4Analysis | null = null
  while (Date.now() < deadline) {
    last =
      analysisId === undefined
        ? await getLatestAnalysis(request, token, versionId)
        : await getAnalysisById(request, token, versionId, analysisId)
    if (last !== null && (last.status === "done" || last.status === "failed")) {
      if (last.status === "failed") {
        throw new Error(
          `analysis ${last.id} failed: ${last.errors.map((error) => error.code).join(", ")}`,
        )
      }
      return last
    }
    await sleep(150)
  }
  throw new Error(
    `analysis for version ${versionId} did not finish within ${timeoutMs}ms (last status: ${
      last?.status ?? "none"
    })`,
  )
}

/** One persisted version event (`VersionEvent`). */
export type P4VersionEvent = {
  seq: number
  type: string
  summary: string
  detail: Record<string, unknown>
  refs: { code: string[] }
}

/** GET /api/versions/{id}/events — the persisted event snapshot. */
export async function listVersionEvents(
  request: APIRequestContext,
  token: string,
  versionId: string,
  limit = 50,
): Promise<P4VersionEvent[]> {
  const response = await request.get(`${API_URL}/api/versions/${versionId}/events?limit=${limit}`, {
    headers: authHeaders(token),
  })
  expect(response.status()).toBe(200)
  const body = (await response.json()) as { data: P4VersionEvent[] }
  return Array.isArray(body.data) ? body.data : []
}

/** One code anchor (`AnchorDTO`). */
export type P4Anchor = {
  id: string
  versionId: string
  subjectKind: string
  subjectId: string
  filePath: string
  startLine: number | null
  endLine: number | null
  confidence: string
}

/** POST /api/versions/{id}/code/anchors. */
export async function createP4Anchor(
  request: APIRequestContext,
  token: string,
  versionId: string,
  body: {
    subjectKind: string
    subjectId: string
    filePath: string
    startLine?: number
    endLine?: number
  },
): Promise<P4Anchor> {
  const response = await request.post(`${API_URL}/api/versions/${versionId}/code/anchors`, {
    headers: authHeaders(token),
    data: body,
  })
  return readData<P4Anchor>(response, 201, "create anchor")
}

/** GET /api/versions/{id}/code/anchors. */
export async function listP4Anchors(
  request: APIRequestContext,
  token: string,
  versionId: string,
): Promise<P4Anchor[]> {
  const response = await request.get(`${API_URL}/api/versions/${versionId}/code/anchors`, {
    headers: authHeaders(token),
  })
  return readData<P4Anchor[]>(response, 200, "list anchors")
}

/** One compact P4 request log line written by scripts/fake-llm.cjs. */
export type P4LoggedRequest = {
  p4: true
  kind: "file_analysis" | "impact_analysis"
  path: string | null
  commit: string | null
  mode: string
}

/** Reads the compact P4 request log lines (never prompt content). */
export function readP4Requests(): P4LoggedRequest[] {
  let raw = ""
  try {
    raw = readFileSync(FAKE_REQUEST_LOG, "utf8")
  } catch {
    return []
  }
  const requests: P4LoggedRequest[] = []
  for (const line of raw.split("\n")) {
    if (line.trim() === "") continue
    try {
      const parsed = JSON.parse(line) as Partial<P4LoggedRequest>
      if (parsed.p4 === true) requests.push(parsed as P4LoggedRequest)
    } catch {
      // A torn last line while the fake is appending cannot be parsed; skip it.
    }
  }
  return requests
}

/** Counts the logged P4 requests matching one optional filter. */
export function countP4Requests(match?: (request: P4LoggedRequest) => boolean): number {
  return readP4Requests().filter((request) => (match === undefined ? true : match(request))).length
}

/** Counts the file-analysis LLM requests for one commit (cache assertions). */
export function countP4FileAnalysis(commit?: string): number {
  return countP4Requests(
    (request) =>
      request.kind === "file_analysis" && (commit === undefined || request.commit === commit),
  )
}

/**
 * Switches the fake LLM's P4 scenario at runtime through the test-only
 * `POST /__p4-mode` handle and verifies the applied mode. The degraded spec must
 * always reset it to `ok` afterwards (it is restored in a `finally`).
 */
export async function setP4Mode(request: APIRequestContext, mode: string): Promise<void> {
  const response = await request.post(`${FAKE_LLM_URL}/__p4-mode`, { data: { mode } })
  expect(response.status(), `set fake P4 mode ${mode}`).toBe(200)
  const body = (await response.json()) as { mode?: string }
  expect(body.mode).toBe(mode)
}

/** Returns one P4 request log line for the given file analysis, if present. */
export function findP4FileRequest(path: string, commit?: string): P4LoggedRequest | undefined {
  return readP4Requests().find(
    (request) =>
      request.kind === "file_analysis" &&
      request.path === path &&
      (commit === undefined || request.commit === commit),
  )
}

/**
 * Normalises the analysis' candidates into a comparable projection so a force
 * re-analysis can be asserted to keep the same result (IDs and row order are
 * intentionally excluded: a new analysis version persists new candidate rows).
 */
export function candidateProjection(analysis: P4Analysis): Array<Record<string, unknown>> {
  return analysis.candidates.map((candidate) => ({
    subjectKind: candidate.subjectKind,
    subjectId: candidate.subjectId,
    reason: candidate.reason,
    confidence: candidate.confidence,
    source: candidate.source,
    selected: candidate.selected,
    evidence: candidate.evidence.map(
      (item) => `${item.file}:${item.startLine ?? ""}-${item.endLine ?? ""}`,
    ),
  }))
}

/** Sleep helper for the polling helpers above. */
export function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}
