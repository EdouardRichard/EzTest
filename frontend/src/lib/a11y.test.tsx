import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import axe from "axe-core"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type {
  AgentCenterCatalog,
  AgentCenterRole,
  AgentCenterSession,
} from "@/features/agent-console/agent-api"
import type { ChainSnapshot } from "@/features/execution/chain/chain-types"
import type { Batch, CaseRun } from "@/features/execution/types"
import type { Requirement, RequirementRelation, Version } from "@/features/versions/types"

const mocks = vi.hoisted(() => ({
  useAuth: vi.fn(),
  replace: vi.fn(),
  push: vi.fn(),
  setTheme: vi.fn(),
  // Versions API.
  getVersion: vi.fn(),
  listVersions: vi.fn(),
  createVersion: vi.fn(),
  updateVersion: vi.fn(),
  putBindings: vi.fn(),
  startClarification: vi.fn(),
  enterDesign: vi.fn(),
  fetchVersionEvents: vi.fn(),
  listRequirements: vi.fn(),
  createRequirement: vi.fn(),
  updateRequirement: vi.fn(),
  deleteRequirement: vi.fn(),
  confirmRequirement: vi.fn(),
  reopenRequirement: vi.fn(),
  listAttachments: vi.fn(),
  uploadAttachment: vi.fn(),
  downloadAttachment: vi.fn(),
  deleteAttachment: vi.fn(),
  listRelations: vi.fn(),
  createRelation: vi.fn(),
  updateRelation: vi.fn(),
  deleteRelation: vi.fn(),
  getClarification: vi.fn(),
  startClarificationSession: vi.fn(),
  completeClarificationSession: vi.fn(),
  listClarificationMessages: vi.fn(),
  sendClarificationMessage: vi.fn(),
  analyzeClarification: vi.fn(),
  reconClarification: vi.fn(),
  listClarificationQuestions: vi.fn(),
  createClarificationQuestion: vi.fn(),
  updateClarificationQuestion: vi.fn(),
  listDecisions: vi.fn(),
  createDecision: vi.fn(),
  updateDecision: vi.fn(),
  getIncrementalPlan: vi.fn(),
  createIncrementalBatch: vi.fn(),
  // Streams / execution hooks.
  useVersionStream: vi.fn(),
  useBatches: vi.fn(),
  useBatch: vi.fn(),
  useCaseRuns: vi.fn(),
  useCaseRun: vi.fn(),
  useExecutionReport: vi.fn(),
  useExecution: vi.fn(),
  useExecutionActions: vi.fn(),
  // Delivery hooks.
  useReports: vi.fn(),
  useDeliveryReport: vi.fn(),
  useReportBatches: vi.fn(),
  useGlobalReports: vi.fn(),
  useIssues: vi.fn(),
  useIssue: vi.fn(),
  useGlobalIssues: vi.fn(),
  // Exploration chain read model.
  useExplorationChain: vi.fn(),
  // Network + feature APIs of the settings and code surfaces.
  apiGet: vi.fn(),
  apiGetPage: vi.fn(),
  apiPost: vi.fn(),
  apiPatch: vi.fn(),
  apiPut: vi.fn(),
  apiDelete: vi.fn(),
  apiStreamFetch: vi.fn(),
  getLatestAnalysis: vi.fn(),
  listSystems: vi.fn(),
  listEnvironments: vi.fn(),
  listCodeRepos: vi.fn(),
  // Agent centre.
  getAgentCenterCatalog: vi.fn(),
  getAgentCenterQuality: vi.fn(),
  listAgentCenterSessions: vi.fn(),
}))

vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: mocks.replace, push: mocks.push }),
  usePathname: () => "/",
  useSearchParams: () => new URLSearchParams(),
}))

vi.mock("next-themes", () => ({
  useTheme: () => ({ theme: "system", setTheme: mocks.setTheme }),
}))

vi.mock("@/features/auth/AuthProvider", () => ({ useAuth: mocks.useAuth }))

// Only the network boundary is stubbed. The real version/settings/agent
// components render; their data comes from these API fixtures.
vi.mock("@/lib/api-client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api-client")>()
  return {
    ...actual,
    apiGet: mocks.apiGet,
    apiGetPage: mocks.apiGetPage,
    apiPost: mocks.apiPost,
    apiPatch: mocks.apiPatch,
    apiPut: mocks.apiPut,
    apiDelete: mocks.apiDelete,
    apiStreamFetch: mocks.apiStreamFetch,
  }
})

vi.mock("@/features/versions/api", () => ({
  getVersion: mocks.getVersion,
  listVersions: mocks.listVersions,
  createVersion: mocks.createVersion,
  updateVersion: mocks.updateVersion,
  putBindings: mocks.putBindings,
  startClarification: mocks.startClarification,
  enterDesign: mocks.enterDesign,
  fetchVersionEvents: mocks.fetchVersionEvents,
  listRequirements: mocks.listRequirements,
  createRequirement: mocks.createRequirement,
  updateRequirement: mocks.updateRequirement,
  deleteRequirement: mocks.deleteRequirement,
  confirmRequirement: mocks.confirmRequirement,
  reopenRequirement: mocks.reopenRequirement,
  listAttachments: mocks.listAttachments,
  uploadAttachment: mocks.uploadAttachment,
  downloadAttachment: mocks.downloadAttachment,
  deleteAttachment: mocks.deleteAttachment,
  listRelations: mocks.listRelations,
  createRelation: mocks.createRelation,
  updateRelation: mocks.updateRelation,
  deleteRelation: mocks.deleteRelation,
  getClarification: mocks.getClarification,
  startClarificationSession: mocks.startClarificationSession,
  completeClarificationSession: mocks.completeClarificationSession,
  listClarificationMessages: mocks.listClarificationMessages,
  sendClarificationMessage: mocks.sendClarificationMessage,
  analyzeClarification: mocks.analyzeClarification,
  reconClarification: mocks.reconClarification,
  listClarificationQuestions: mocks.listClarificationQuestions,
  createClarificationQuestion: mocks.createClarificationQuestion,
  updateClarificationQuestion: mocks.updateClarificationQuestion,
  listDecisions: mocks.listDecisions,
  createDecision: mocks.createDecision,
  updateDecision: mocks.updateDecision,
  getIncrementalPlan: mocks.getIncrementalPlan,
  createIncrementalBatch: mocks.createIncrementalBatch,
}))

vi.mock("@/features/versions/useVersionStream", () => ({
  useVersionStream: mocks.useVersionStream,
}))

vi.mock("@/features/execution/hooks/useExecution", () => ({
  useBatches: mocks.useBatches,
  useBatch: mocks.useBatch,
  useCaseRuns: mocks.useCaseRuns,
  useCaseRun: mocks.useCaseRun,
  useReport: mocks.useExecutionReport,
  useExecution: mocks.useExecution,
  useExecutionActions: mocks.useExecutionActions,
}))

vi.mock("@/features/delivery/hooks/useReports", () => ({
  useReports: mocks.useReports,
  useReport: mocks.useDeliveryReport,
  useReportBatches: mocks.useReportBatches,
  useGlobalReports: mocks.useGlobalReports,
}))

vi.mock("@/features/delivery/hooks/useIssues", () => ({
  useIssues: mocks.useIssues,
  useIssue: mocks.useIssue,
  useGlobalIssues: mocks.useGlobalIssues,
}))

vi.mock("@/features/execution/chain/use-exploration-chain", () => ({
  useExplorationChain: mocks.useExplorationChain,
}))

vi.mock("@/features/execution/api", () => ({
  createBatch: vi.fn(),
  listBatches: vi.fn(),
  getBatch: vi.fn(),
  pauseBatch: vi.fn(),
  resumeBatch: vi.fn(),
  cancelBatch: vi.fn(),
  listCaseRuns: vi.fn(),
  getCaseRun: vi.fn(),
  rerunCase: vi.fn(),
  getReport: vi.fn(),
  exportReport: vi.fn(),
  getEvidenceUrl: vi.fn(),
  getEvidenceMeta: vi.fn(),
  listExecutionSessions: vi.fn(),
}))

vi.mock("@/features/code-intel/api", () => ({
  getCodeRepos: vi.fn(),
  startIndex: vi.fn(),
  getLatestIndexRun: vi.fn(),
  getTree: vi.fn(),
  getFile: vi.fn(),
  getDiff: vi.fn(),
  getFileDiff: vi.fn(),
  getFacts: vi.fn(),
  searchFacts: vi.fn(),
  getVersionChanges: vi.fn(),
  startAnalysis: vi.fn(),
  getLatestAnalysis: mocks.getLatestAnalysis,
  getAnalysis: vi.fn(),
  patchCandidate: vi.fn(),
  listAnchors: vi.fn(),
  createAnchor: vi.fn(),
  deleteAnchor: vi.fn(),
}))

vi.mock("@/features/resources/api", () => ({
  listSystems: mocks.listSystems,
  listEnvironments: mocks.listEnvironments,
  requestTakeover: vi.fn(),
  completeTakeover: vi.fn(),
}))

vi.mock("@/features/code-repos/api", () => ({
  listCodeRepos: mocks.listCodeRepos,
}))

// The agent console itself is the P1 configuration surface, not part of this
// gate; the Agent centre page, catalogue, run observation and quality card are
// real. The API read model is mocked like every other page here.
vi.mock("@/features/agent-console/AgentConsole", () => ({
  AgentConsole: () => null,
}))

vi.mock("@/features/agent-console/agent-api", () => ({
  getAgentCenterCatalog: mocks.getAgentCenterCatalog,
  getAgentCenterQuality: mocks.getAgentCenterQuality,
  listAgentCenterSessions: mocks.listAgentCenterSessions,
}))

// The human-takeover modal is not opened by any scanned state.
vi.mock("@/features/resources/TakeoverDialog", () => ({
  TakeoverDialog: () => null,
}))

import AgentCenterPage from "@/app/(app)/agents/page"
import SettingsPage from "@/app/(app)/settings/page"
import LoginPage from "@/app/login/page"
import { VersionDetailPage } from "@/features/versions/VersionDetailPage"
import { VersionsPage } from "@/features/versions/VersionsPage"

/** A snowflake ID that a JS number cannot hold; it must stay a string verbatim. */
const VERSION_ID = "1844674407370955161"
const REQUIREMENT_ID = "1844674407370956001"
const SECOND_REQUIREMENT_ID = "1844674407370956002"
const RELATION_ID = "1844674407370956101"
const BATCH_ID = "1844674407370955165"
const CASE_RUN_PASSED = "1844674407370955170"
const CASE_RUN_FAILED = "1844674407370955171"

const VERSION: Version = {
  id: VERSION_ID,
  name: "订单系统 2.0",
  label: "v2.0",
  description: "订单系统第二阶段",
  status: "clarifying",
  systemId: 1,
  environmentId: 2,
  codeRepositoryId: 3,
  codeBranch: "main",
  codeBaselineCommit: "0123456789abcdef0123456789abcdef01234567",
  bindings: {
    system: { id: 1, name: "订单系统", available: true },
    environment: { id: 2, name: "staging", available: true },
    codeRepository: { id: 3, name: "order-web", available: true },
  },
  requirementCount: 2,
  clarifiedCount: 1,
  openQuestionCount: 2,
  revision: 2,
  createdBy: 1,
  updatedBy: 1,
  createdAt: "2026-10-27T10:00:00Z",
  updatedAt: "2026-10-27T10:00:00Z",
}

function requirement(
  overrides: Partial<Requirement> & Pick<Requirement, "id" | "title">,
): Requirement {
  return {
    versionId: VERSION_ID,
    description: "",
    acceptanceCriteria: "",
    priority: "should",
    status: "draft",
    source: "user",
    lastUpdateSource: "user",
    revision: 1,
    confirmedAt: null,
    confirmedBy: null,
    openQuestionCount: 0,
    relationCount: 0,
    attachmentCount: 0,
    createdBy: 1,
    updatedBy: 1,
    createdAt: "2026-10-27T10:00:00Z",
    updatedAt: "2026-10-27T10:00:00Z",
    ...overrides,
  }
}

const DRAFT_REQUIREMENT = requirement({
  id: REQUIREMENT_ID,
  title: "订单创建",
  description: "用户提交订单。",
  openQuestionCount: 2,
  relationCount: 1,
  revision: 3,
})

const CLARIFIED_REQUIREMENT = requirement({
  id: SECOND_REQUIREMENT_ID,
  title: "库存扣减",
  status: "clarified",
  revision: 4,
  confirmedAt: "2026-10-27T11:00:00Z",
  confirmedBy: 1,
})

const RELATION: RequirementRelation = {
  id: RELATION_ID,
  versionId: VERSION_ID,
  fromRequirementId: REQUIREMENT_ID,
  toRequirementId: SECOND_REQUIREMENT_ID,
  relationType: "depends_on",
  description: "创建订单后扣减库存",
  source: "user",
  createdBy: 1,
  createdAt: "2026-10-27T10:00:00Z",
  updatedAt: "2026-10-27T10:00:00Z",
}

const CLARIFICATION_SESSION = {
  id: "1844674407370957001",
  status: "active" as const,
  agentSessionId: 77,
  startedAt: "2026-10-27T10:00:00Z",
  endedAt: null,
  contextSummary: "",
  summaryUpdatedAt: null,
}

const BATCH: Batch = {
  id: BATCH_ID,
  version_id: VERSION_ID,
  batch_no: 1,
  status: "done",
  paused: false,
  pause_mode: "",
  pause_reason: "",
  baseline_hash: "a".repeat(64),
  code_baseline_commit: "0123456789abcdef0123456789abcdef01234567",
  head_commit: "89abcdef0123456789abcdef0123456789abcdef",
  counts: {
    total: 2,
    passed: 1,
    failed: 1,
    blocked: 0,
    inconclusive: 0,
    skipped: 0,
    interrupted: 0,
  },
  created_by: "1",
  started_at: "2026-10-28T00:00:00Z",
  finished_at: "2026-10-28T00:10:00Z",
  failure_reason: "",
}

function caseRun(overrides: Partial<CaseRun> & Pick<CaseRun, "id" | "status">): CaseRun {
  const { id, status, ...rest } = overrides
  return {
    id,
    status,
    batch_id: BATCH_ID,
    case_node_id: `node-${id}`,
    case_revision: 1,
    attempt: 1,
    failure_class: null,
    failure_reason: "",
    summary: "",
    duration_ms: 0,
    title: `用例 ${id}`,
    session: null,
    checkpoint: { completed_seqs: [], cleanup_required: false, interrupted_step_seq: null },
    steps: [],
    assertions: [],
    evidence: [],
    pending_takeover: null,
    ...rest,
  }
}

const PASSED_CASE_RUN = caseRun({
  id: CASE_RUN_PASSED,
  status: "passed",
  title: "登录成功",
  duration_ms: 800,
})
const FAILED_CASE_RUN = caseRun({
  id: CASE_RUN_FAILED,
  status: "failed",
  failure_class: "application",
  title: "提交订单",
  duration_ms: 1200,
  summary: "提交订单返回 500",
})

const EXPLORATION_CHAIN: ChainSnapshot = {
  versionId: VERSION_ID,
  batch: { id: BATCH_ID, batchNo: 1, kind: "baseline", status: "done" },
  nodes: [
    {
      id: `requirement:${REQUIREMENT_ID}`,
      kind: "requirement",
      title: "订单创建",
      status: "clarified",
      priority: "must",
      uncovered: false,
      caseCount: 1,
      executedCount: 1,
      failedCount: 0,
      openQuestionCount: 0,
    },
    {
      id: "case:1844674407370955201",
      kind: "case",
      title: "登录成功",
      status: "executed",
      source: "user_input",
      revalidation: false,
      priority: "P2",
      requirementIds: [REQUIREMENT_ID],
      stepCount: 3,
      assertionCount: 2,
      impacted: false,
      impactReason: "",
    },
    {
      id: `result:${CASE_RUN_PASSED}`,
      kind: "result",
      title: "登录成功",
      status: "passed",
      caseRunId: CASE_RUN_PASSED,
      caseNodeId: "1844674407370955201",
      attempt: 1,
      durationMs: 800,
      failureClass: "",
      evidenceCount: 0,
      batchId: BATCH_ID,
      summary: "",
    },
  ],
  edges: [
    {
      id: `requirement_case:${REQUIREMENT_ID}:1844674407370955201`,
      source: `requirement:${REQUIREMENT_ID}`,
      target: "case:1844674407370955201",
      kind: "requirement_case",
    },
    {
      id: `case_result:1844674407370955201:${CASE_RUN_PASSED}`,
      source: "case:1844674407370955201",
      target: `result:${CASE_RUN_PASSED}`,
      kind: "case_result",
    },
  ],
  counts: {
    requirements: 1,
    cases: 1,
    results: 1,
    issues: 0,
    byResultStatus: {
      pending: 0,
      running: 0,
      passed: 1,
      failed: 0,
      blocked: 0,
      inconclusive: 0,
      skipped: 0,
      interrupted: 0,
    },
    byIssueSeverity: { blocker: 0, major: 0, minor: 0, info: 0 },
    uncoveredRequirements: 0,
    revalidationCases: 0,
    supplementCases: 0,
  },
  truncated: { requirements: false, cases: false, results: false, issues: false },
  generatedAt: "2026-10-29T10:00:00Z",
}

const USERS = [
  { id: 1, username: "admin", role: "admin", status: "active" },
  { id: 2, username: "qa01", role: "user", status: "active" },
]

const LLM_CONFIG = {
  id: 1,
  name: "deepseek",
  protocol: "chat_completions",
  base_url: "https://api.deepseek.com",
  model: "deepseek-chat",
  has_api_key: true,
  key_version: 2,
  header_names: ["X-Tenant"],
  retry_count: 2,
  retry_interval_ms: 500,
  timeout_ms: 60000,
  stream_enabled: true,
  multimodal_enabled: true,
  is_default: true,
  model_catalog: [
    {
      id: "deepseek-chat",
      context_window: 1048576,
      max_output_tokens: 8192,
      input_modalities: ["text"],
    },
  ],
  catalog_fetched_at: "2026-10-27T00:00:00Z",
}

const HEALTH = {
  status: "ok",
  version: "1.2.3",
  commit: "abcdef1234567890",
  db: "ok",
  llm_configured: true,
}

const AUDIT_ENTRY = {
  id: 12,
  actor_user_id: 1,
  action: "version.requirement_created",
  target_type: "requirement",
  target_id: "77",
  request_id: "req_audit_1",
  ip: "203.0.113.7",
  user_agent: "eztest/1.0",
  metadata: { status: "draft" },
  created_at: "2026-10-27T04:00:00Z",
}

const AGENT_ROLES: AgentCenterRole[] = [
  {
    order: 1,
    role: "clarification-dialog",
    name: "澄清对话",
    phase: "P3",
    mode: "agent",
    note: "",
    profile_key: "clarification-dialog",
    profile_id: 1,
    enabled: true,
    model: "deepseek-chat",
    prompt_version: null,
    allowed_tools: ["tool.one"],
    stats: { run_count: 4, succeeded_count: 3, failed_count: 1, success_rate: 0.75 },
  },
  {
    order: 2,
    role: "report",
    name: "报告整理",
    phase: "P7",
    mode: "deterministic",
    note: "无独立 profile，由确定性服务生成",
    profile_key: null,
    profile_id: null,
    enabled: false,
    model: "",
    prompt_version: null,
    allowed_tools: [],
    stats: { run_count: 0, succeeded_count: 0, failed_count: 0, success_rate: 0 },
  },
]

const AGENT_SESSION: AgentCenterSession = {
  id: 9,
  role: "clarification-dialog",
  title: "设计会话",
  status: "done",
  running: false,
  created_at: "2026-10-29T00:00:00Z",
  updated_at: "2026-10-29T01:00:00Z",
  last_activity_at: "2026-10-29T01:00:00Z",
}

function batchesResult() {
  return {
    batches: [BATCH],
    nextCursor: "",
    hasMore: false,
    loading: false,
    error: "",
    streamStatus: "live" as const,
    refresh: vi.fn().mockResolvedValue(undefined),
  }
}

function actionsResult() {
  return {
    createBatch: vi.fn(),
    pause: vi.fn(),
    resume: vi.fn(),
    cancel: vi.fn(),
    rerun: vi.fn(),
    exportReport: vi.fn(),
    pending: "" as const,
    error: "",
    clearError: vi.fn(),
  }
}

/**
 * Runs axe on the rendered container and fails on serious/critical impact only.
 *
 * `color-contrast` is disabled because jsdom has no layout/paint engine: axe
 * cannot compute a trustworthy pass/fail result there. No automated Playwright
 * contrast test exists yet, so real contrast remains a manual/follow-up check
 * (spec §8.3) rather than something this gate covers.
 */
async function expectNoSeriousA11yViolations(container: HTMLElement): Promise<void> {
  const results = await axe.run(container, {
    rules: { "color-contrast": { enabled: false } },
  })
  const serious = results.violations.filter(
    (violation) => violation.impact === "serious" || violation.impact === "critical",
  )
  const failures = serious.map((violation) => {
    const targets = violation.nodes.map((node) => node.target.join(" ")).join(", ")
    return `${violation.id} (${violation.impact}): ${targets}`
  })
  expect(failures).toEqual([])
}

describe("accessibility (axe)", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) {
      if (typeof mock === "function" && "mockReset" in mock) mock.mockReset()
    }
    // jsdom logs "Not implemented: getContext" whenever axe probes a canvas;
    // these pages contain none, so keep the expected null result silent.
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(null)
    mocks.useAuth.mockReturnValue({ user: null, loading: false, login: vi.fn(), logout: vi.fn() })
    mocks.listVersions.mockResolvedValue({ items: [], nextCursor: "", hasMore: false })
    mocks.getVersion.mockResolvedValue(VERSION)
    mocks.fetchVersionEvents.mockResolvedValue({ events: [], cursor: 0, hasMore: false })
    mocks.useVersionStream.mockReturnValue({ events: [], status: "live", reconnect: vi.fn() })
    mocks.listRequirements.mockResolvedValue({
      items: [DRAFT_REQUIREMENT, CLARIFIED_REQUIREMENT],
      nextCursor: "",
      hasMore: false,
    })
    mocks.listRelations.mockResolvedValue([RELATION])
    mocks.listAttachments.mockResolvedValue([])
    mocks.getClarification.mockResolvedValue({
      session: CLARIFICATION_SESSION,
      questions: { open: 2, answered: 0, dismissed: 0 },
      decisions: { proposed: 0, accepted: 0, rejected: 0 },
    })
    mocks.listClarificationMessages.mockResolvedValue({
      items: [],
      nextCursor: "",
      hasMore: false,
    })
    mocks.listClarificationQuestions.mockResolvedValue({
      items: [],
      nextCursor: "",
      hasMore: false,
    })
    mocks.listDecisions.mockResolvedValue({ items: [], nextCursor: "", hasMore: false })
    mocks.listSystems.mockResolvedValue({
      items: [
        {
          id: 1,
          key: "order",
          name: "订单系统",
          kind: "web",
          baseUrl: "https://order.example.com",
          defaultSessionTtlSeconds: 3600,
          notes: "",
          status: "active",
          createdAt: "2026-10-27T10:00:00Z",
          updatedAt: "2026-10-27T10:00:00Z",
        },
      ],
      nextCursor: null,
      hasMore: false,
    })
    mocks.listEnvironments.mockResolvedValue([
      {
        id: 2,
        systemId: 1,
        key: "staging",
        name: "staging",
        baseUrl: "https://staging.example.com",
        kind: "web",
        isProd: false,
        allowedHosts: [],
        loginCheck: null,
        allowAutoLogin: false,
        requireApproval: false,
      },
    ])
    mocks.listCodeRepos.mockResolvedValue({
      items: [
        {
          id: 3,
          key: "order-web",
          name: "order-web",
          localPath: "/srv/repos/order-web",
          fixedBranch: "",
          includeGlobs: [],
          excludeGlobs: [],
          status: "active",
          lastCommit: "",
          lastValidatedAt: null,
          lastValidationError: "",
          lastIndexedAt: null,
        },
      ],
      nextCursor: null,
      hasMore: false,
    })
    mocks.useBatches.mockReturnValue(batchesResult())
    mocks.useBatch.mockReturnValue({
      batch: null,
      loading: false,
      error: "",
      streamStatus: "live",
      refresh: vi.fn(),
    })
    mocks.useCaseRuns.mockReturnValue({
      caseRuns: [PASSED_CASE_RUN, FAILED_CASE_RUN],
      nextCursor: "",
      hasMore: false,
      loading: false,
      error: "",
      streamStatus: "live",
      refresh: vi.fn(),
    })
    mocks.useCaseRun.mockReturnValue({
      caseRun: null,
      loading: false,
      error: "",
      streamStatus: "live",
      refresh: vi.fn(),
    })
    mocks.useExecutionReport.mockReturnValue({
      report: null,
      loading: false,
      error: "",
      streamStatus: "live",
      refresh: vi.fn(),
    })
    mocks.useExecution.mockReturnValue({
      batches: [BATCH],
      activeBatch: null,
      loading: false,
      error: "",
      streamStatus: "live",
      refresh: vi.fn(),
      actions: actionsResult(),
    })
    mocks.useExecutionActions.mockReturnValue(actionsResult())
    mocks.useReports.mockReturnValue({
      reports: [],
      items: [],
      nextCursor: "",
      hasMore: false,
      loading: false,
      loadingMore: false,
      error: "",
      streamStatus: "live",
      refresh: vi.fn(),
    })
    mocks.useDeliveryReport.mockReturnValue({
      data: null,
      report: null,
      loading: false,
      error: "",
      streamStatus: "live",
      refresh: vi.fn(),
    })
    mocks.useReportBatches.mockReturnValue({
      items: [],
      nextCursor: "",
      hasMore: false,
      loading: false,
      error: "",
      streamStatus: "live",
      refresh: vi.fn(),
    })
    mocks.useGlobalReports.mockReturnValue({
      reports: [],
      items: [],
      nextCursor: "",
      hasMore: false,
      loading: false,
      loadingMore: false,
      error: "",
      streamStatus: "live",
      refresh: vi.fn(),
    })
    mocks.useIssues.mockReturnValue({
      issues: [],
      items: [],
      nextCursor: "",
      hasMore: false,
      loading: false,
      loadingMore: false,
      error: "",
      streamStatus: "live",
      refresh: vi.fn(),
    })
    mocks.useIssue.mockReturnValue({
      issue: null,
      data: null,
      loading: false,
      error: "",
      streamStatus: "live",
      refresh: vi.fn(),
    })
    mocks.useGlobalIssues.mockReturnValue({
      issues: [],
      items: [],
      nextCursor: "",
      hasMore: false,
      loading: false,
      loadingMore: false,
      error: "",
      streamStatus: "live",
      refresh: vi.fn(),
    })
    mocks.useExplorationChain.mockReturnValue({
      data: EXPLORATION_CHAIN,
      loading: false,
      error: "",
      streamStatus: "live",
      refresh: vi.fn(),
    })
    mocks.getLatestAnalysis.mockResolvedValue({
      id: "analysis-1",
      versionId: VERSION_ID,
      status: "done",
      degraded: false,
      stats: { changedFiles: 2, unmapped: 0, truncated: false },
      unmapped: [],
      errors: [],
    })
    mocks.apiGet.mockImplementation((path: string) => {
      if (path.startsWith("/api/users")) return Promise.resolve(USERS)
      if (path === "/api/admin/llm/configs") return Promise.resolve([LLM_CONFIG])
      if (path === "/api/health") return Promise.resolve(HEALTH)
      return Promise.reject(new Error(`unexpected apiGet: ${path}`))
    })
    mocks.apiGetPage.mockResolvedValue({
      items: [AUDIT_ENTRY],
      nextCursor: "",
      hasMore: false,
    })
    mocks.getAgentCenterCatalog.mockResolvedValue({
      roles: AGENT_ROLES,
      latest_eval: null,
    } satisfies AgentCenterCatalog)
    mocks.getAgentCenterQuality.mockResolvedValue({
      latest: {
        run_id: 1,
        passed: true,
        metrics: { design_coverage: 0.9, impact_recall: 0.8 },
        thresholds: { design_coverage: 0.8 },
        created_at: "2026-10-29T00:00:00Z",
      },
    })
    mocks.listAgentCenterSessions.mockResolvedValue({ items: [AGENT_SESSION] })
  })

  afterEach(() => {
    cleanup()
    window.history.replaceState(null, "", "/")
  })

  it("detects a known serious violation so the gate cannot silently pass", async () => {
    const { container } = render(
      // biome-ignore lint/a11y/useAriaPropsSupportedByRole: deliberately malformed markup proves the axe gate detects violations.
      <div aria-checked="true">切换</div>,
    )
    const results = await axe.run(container, {
      rules: { "color-contrast": { enabled: false } },
    })
    const serious = results.violations.filter(
      (violation) => violation.impact === "serious" || violation.impact === "critical",
    )

    expect(serious.length).toBeGreaterThan(0)
  })

  it("has no serious or critical violations on the login page", async () => {
    const { container } = render(<LoginPage />)

    expect(await screen.findByRole("heading", { name: "登录 EzTest" })).toBeTruthy()
    await expectNoSeriousA11yViolations(container)
  })

  it("has no serious or critical violations on the versions list", async () => {
    mocks.listVersions.mockResolvedValue({
      items: [VERSION],
      nextCursor: "",
      hasMore: false,
    })

    const { container } = render(<VersionsPage />)

    expect(await screen.findByText("订单系统 2.0")).toBeTruthy()
    await expectNoSeriousA11yViolations(container)
  })

  it("has no serious or critical violations on the real key version-detail panels", async () => {
    window.history.replaceState(null, "", `/versions/${VERSION_ID}?tab=requirements`)

    const { container } = render(<VersionDetailPage versionId={VERSION_ID} />)

    expect(await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })).toBeTruthy()

    // 需求与澄清: the real Workbench (list, editor, relation, chat, bindings),
    // not a stand-in panel.
    expect(await screen.findByRole("heading", { name: "需求与澄清工作台" })).toBeTruthy()
    // The list, the relation form and the editor all project the loaded
    // fixture; at least one is enough to prove the real data path ran.
    expect((await screen.findAllByText("订单创建")).length).toBeGreaterThan(0)
    expect(await screen.findByText("创建订单后扣减库存")).toBeTruthy()
    expect(screen.getByRole("button", { name: "保存绑定" })).toBeTruthy()
    await expectNoSeriousA11yViolations(container)

    // 总览: the real OverviewTab with the shared batch fixture.
    fireEvent.mouseDown(screen.getByRole("tab", { name: "总览" }))
    expect(await screen.findByTestId("overview-metrics")).toBeTruthy()
    expect(screen.getByRole("heading", { name: "需求 × 用例覆盖矩阵" })).toBeTruthy()
    expect(screen.getByRole("heading", { name: "批次时间线" })).toBeTruthy()
    expect(screen.getByText(/Batch 1 · 基线/)).toBeTruthy()
    await expectNoSeriousA11yViolations(container)

    // 探索链路: the real tab and its React Flow canvas (jsdom is polyfilled
    // for React Flow in vitest.setup.ts, so this is not a stub).
    fireEvent.mouseDown(screen.getByRole("tab", { name: "探索链路" }))
    expect(await screen.findByTestId("exploration-tab")).toBeTruthy()
    expect(await screen.findByTestId("chain-canvas")).toBeTruthy()
    await expectNoSeriousA11yViolations(container)

    // 结果与证据: the real results table with both case runs.
    fireEvent.mouseDown(screen.getByRole("tab", { name: "结果与证据" }))
    expect(await screen.findByTestId("results-tab")).toBeTruthy()
    expect(await screen.findByText("登录成功")).toBeTruthy()
    expect(screen.getByText("提交订单返回 500")).toBeTruthy()
    expect(screen.getByRole("columnheader", { name: "失败分类" })).toBeTruthy()
    await expectNoSeriousA11yViolations(container)
  }, 30_000)

  it("has no serious or critical violations on the real admin settings panels", async () => {
    mocks.useAuth.mockReturnValue({
      user: { id: 1, username: "admin", role: "admin", status: "active" },
      loading: false,
      login: vi.fn(),
      logout: vi.fn(),
    })

    const { container } = render(<SettingsPage />)

    expect(await screen.findByRole("heading", { name: "设置" })).toBeTruthy()

    // 用户: the real UsersPanel table.
    fireEvent.mouseDown(screen.getByRole("tab", { name: "用户" }))
    expect(await screen.findByRole("heading", { name: "用户管理" })).toBeTruthy()
    expect(await screen.findByText("qa01")).toBeTruthy()
    expect(screen.getByRole("table", { name: "用户账户列表" })).toBeTruthy()
    await expectNoSeriousA11yViolations(container)

    // LLM: the real LLMPanel with a masked configuration card.
    fireEvent.mouseDown(screen.getByRole("tab", { name: "LLM" }))
    expect(await screen.findByRole("heading", { name: "LLM 配置" })).toBeTruthy()
    expect(await screen.findByText("deepseek")).toBeTruthy()
    await expectNoSeriousA11yViolations(container)

    // SSE: the real SSETestPanel diagnostics card.
    fireEvent.mouseDown(screen.getByRole("tab", { name: "SSE 测试" }))
    expect(await screen.findByRole("heading", { name: "SSE 联通性测试" })).toBeTruthy()
    expect(screen.getByRole("button", { name: "连接" })).toBeTruthy()
    await expectNoSeriousA11yViolations(container)

    // 基础配置: the real read-only BasicConfigPanel.
    fireEvent.mouseDown(screen.getByRole("tab", { name: "基础配置" }))
    expect(await screen.findByRole("heading", { name: "基础配置" })).toBeTruthy()
    expect(await screen.findByText("1.2.3")).toBeTruthy()
    await expectNoSeriousA11yViolations(container)

    // 审计日志: the real AuditLogPage table.
    fireEvent.mouseDown(screen.getByRole("tab", { name: "审计日志" }))
    expect(await screen.findByRole("heading", { name: "审计日志" })).toBeTruthy()
    expect(await screen.findByText("version.requirement_created")).toBeTruthy()
    expect(screen.getByRole("table", { name: "审计日志列表" })).toBeTruthy()
    await expectNoSeriousA11yViolations(container)
  }, 30_000)

  it("has no serious or critical violations on the Agent Center", async () => {
    mocks.useAuth.mockReturnValue({
      user: { id: 1, username: "tester", role: "admin", status: "active" },
      loading: false,
      login: vi.fn(),
      logout: vi.fn(),
    })

    const { container } = render(<AgentCenterPage />)

    expect(await screen.findByRole("heading", { name: "Agent 中心" })).toBeTruthy()
    expect(await screen.findAllByTestId("agent-role-row")).toHaveLength(AGENT_ROLES.length)
    expect(await screen.findByTestId("agent-center-quality")).toBeTruthy()
    expect(screen.getByText("设计覆盖率")).toBeTruthy()
    await expectNoSeriousA11yViolations(container)

    // 运行观察: the real session table.
    fireEvent.mouseDown(screen.getByRole("tab", { name: "运行观察" }))
    expect(await screen.findByText("设计会话")).toBeTruthy()
    expect(screen.getByRole("table", { name: "Agent 运行观察" })).toBeTruthy()
    await expectNoSeriousA11yViolations(container)
  }, 30_000)
})
