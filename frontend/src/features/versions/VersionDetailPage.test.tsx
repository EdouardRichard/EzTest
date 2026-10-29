import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

const codeIntelMocks = vi.hoisted(() => ({
  getVersionChanges: vi.fn(),
  getLatestIndexRun: vi.fn(),
  getLatestAnalysis: vi.fn(),
  startAnalysis: vi.fn(),
  getFileDiff: vi.fn(),
}))

vi.mock("@/features/code-intel/api", () => ({
  getVersionChanges: codeIntelMocks.getVersionChanges,
  getLatestIndexRun: codeIntelMocks.getLatestIndexRun,
  getLatestAnalysis: codeIntelMocks.getLatestAnalysis,
  startAnalysis: codeIntelMocks.startAnalysis,
  getFileDiff: codeIntelMocks.getFileDiff,
}))

const mocks = vi.hoisted(() => ({
  getVersion: vi.fn(),
  startClarification: vi.fn(),
  enterDesign: vi.fn(),
  listRequirements: vi.fn(),
  listRelations: vi.fn(),
  getClarification: vi.fn(),
  listClarificationMessages: vi.fn(),
  sendClarificationMessage: vi.fn(),
  startClarificationSession: vi.fn(),
  analyzeClarification: vi.fn(),
  reconClarification: vi.fn(),
  listClarificationQuestions: vi.fn(),
  createClarificationQuestion: vi.fn(),
  updateClarificationQuestion: vi.fn(),
  listDecisions: vi.fn(),
  createDecision: vi.fn(),
  updateDecision: vi.fn(),
  fetchVersionEvents: vi.fn(),
  useVersionStream: vi.fn(),
  useBatches: vi.fn(),
  useBatch: vi.fn(),
  useCaseRuns: vi.fn(),
  useCaseRun: vi.fn(),
  useReport: vi.fn(),
  useExecution: vi.fn(),
  useExecutionActions: vi.fn(),
  listExecutionSessions: vi.fn(),
  createBatch: vi.fn(),
  pauseBatch: vi.fn(),
  resumeBatch: vi.fn(),
  createRequirement: vi.fn(),
  getIncrementalPlan: vi.fn(),
  useIssues: vi.fn(),
  useReports: vi.fn(),
  issuesProps: vi.fn(),
  reportsProps: vi.fn(),
  explorationProps: vi.fn(),
  getEvidenceMeta: vi.fn(),
  getEvidenceUrl: vi.fn(),
}))

vi.mock("./api", () => ({
  getVersion: mocks.getVersion,
  startClarification: mocks.startClarification,
  enterDesign: mocks.enterDesign,
  createRequirement: mocks.createRequirement,
  getIncrementalPlan: mocks.getIncrementalPlan,
  listRequirements: mocks.listRequirements,
  listRelations: mocks.listRelations,
  getClarification: mocks.getClarification,
  listClarificationMessages: mocks.listClarificationMessages,
  sendClarificationMessage: mocks.sendClarificationMessage,
  startClarificationSession: mocks.startClarificationSession,
  analyzeClarification: mocks.analyzeClarification,
  reconClarification: mocks.reconClarification,
  listClarificationQuestions: mocks.listClarificationQuestions,
  createClarificationQuestion: mocks.createClarificationQuestion,
  updateClarificationQuestion: mocks.updateClarificationQuestion,
  listDecisions: mocks.listDecisions,
  createDecision: mocks.createDecision,
  updateDecision: mocks.updateDecision,
  fetchVersionEvents: mocks.fetchVersionEvents,
}))

// The execution tabs are covered by their own suites; this container test only
// needs their empty projections to prove the slots and header controls are wired.
vi.mock("@/features/execution/hooks/useExecution", () => ({
  useBatches: mocks.useBatches,
  useBatch: mocks.useBatch,
  useCaseRuns: mocks.useCaseRuns,
  useCaseRun: mocks.useCaseRun,
  useReport: mocks.useReport,
  useExecution: mocks.useExecution,
  useExecutionActions: mocks.useExecutionActions,
}))

vi.mock("@/features/execution/api", () => ({
  listExecutionSessions: mocks.listExecutionSessions,
  getEvidenceMeta: mocks.getEvidenceMeta,
  getEvidenceUrl: mocks.getEvidenceUrl,
}))

// The delivery tabs are covered by their own suites; the overview data is
// projected through the delivery hooks, which this container test stubs with
// empty projections unless a case sets them.
vi.mock("@/features/delivery/hooks/useIssues", () => ({ useIssues: mocks.useIssues }))
vi.mock("@/features/delivery/hooks/useReports", () => ({
  useReports: mocks.useReports,
  useReport: mocks.useReport,
}))
vi.mock("@/features/delivery/components/IssuesTab", () => ({
  IssuesTab: (props: {
    versionId: string
    issueId?: string
    degraded?: boolean
    onIssueChange?: (issueId: string) => void
  }) => {
    mocks.issuesProps(props)
    return <div data-testid="issues-tab">issues for {props.versionId}</div>
  },
}))
vi.mock("@/features/delivery/components/ReportsTab", () => ({
  ReportsTab: (props: {
    versionId: string
    batchId?: string
    reportId?: string
    onBatchChange?: (batchId: string) => void
    onReportChange?: (reportId: string) => void
  }) => {
    mocks.reportsProps(props)
    return <div data-testid="report-tab">reports for {props.versionId}</div>
  },
}))

// The real exploration tab is covered by its own suite; the container test
// pins the deep-link wiring (params in, URL writes out) and the issue callback.
vi.mock("@/features/execution/components/ExplorationTab", () => ({
  ExplorationTab: (props: {
    versionId: string
    batchId?: string
    initialFilters?: {
      requirementId?: string
      resultStatus?: string
      abnormalOnly?: boolean
      q?: string
    }
    selectedNodeId?: string
    onFiltersChange?: (filters: {
      requirementId: string
      resultStatus: string
      abnormalOnly: boolean
      q: string
    }) => void
    onSelectedNodeChange?: (nodeId: string) => void
    onNavigate?: (target: { kind: "issue"; id: string }) => void
  }) => {
    mocks.explorationProps(props)
    return (
      <div data-testid="exploration-tab">
        exploration for {props.versionId}
        <button
          type="button"
          data-testid="exploration-issue-link"
          onClick={() => props.onNavigate?.({ kind: "issue", id: "1844674407370955410" })}
        >
          探索问题
        </button>
        <button
          type="button"
          data-testid="exploration-node-link"
          onClick={() => props.onSelectedNodeChange?.("case:1844674407370955210")}
        >
          打开节点
        </button>
        <button
          type="button"
          data-testid="exploration-node-close"
          onClick={() => props.onSelectedNodeChange?.("")}
        >
          关闭节点
        </button>
        <button
          type="button"
          data-testid="exploration-filter-link"
          onClick={() =>
            props.onFiltersChange?.({
              requirementId: "1844674407370955101",
              resultStatus: "failed",
              abnormalOnly: true,
              q: "登录",
            })
          }
        >
          筛选
        </button>
      </div>
    )
  },
}))

vi.mock("@/features/mindmap/api", () => ({
  getGraph: vi.fn(async () => ({
    versionId: "1844674407370955161",
    versionStatus: "clarifying",
    editable: false,
    nodes: [],
    coverage: {
      requirementIds: [],
      uncoveredRequirementIds: [],
      staleRequirementIds: [],
      matrix: [],
    },
    baseline: null,
    latestRuns: {},
  })),
}))

// The workbench owns the version stream; this suite only covers the tab shell.
vi.mock("./useVersionStream", () => ({ useVersionStream: mocks.useVersionStream }))

// The bindings form reads the P2 resource APIs; the container test stays focused
// on the tab shell and header actions, while T17's own suite covers bindings.
vi.mock("./VersionBindingsForm", () => ({
  VersionBindingsForm: () => null,
}))

import VersionDetailRoutePage from "@/app/(app)/versions/[id]/page"

import type { Analysis, ChangeList, IndexRun } from "@/features/code-intel/types"
import type { Batch } from "@/features/execution/types"

import type { Requirement, Version, VersionEvent } from "./types"
import { VersionDetailPage } from "./VersionDetailPage"

/** A snowflake ID that a JS number cannot hold; it must stay a string verbatim. */
const VERSION_ID = "1844674407370955161"

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
  requirementCount: 3,
  clarifiedCount: 1,
  openQuestionCount: 2,
  revision: 2,
  createdBy: 1,
  updatedBy: 1,
  createdAt: "2026-10-27T10:00:00Z",
  updatedAt: "2026-10-27T10:00:00Z",
}

const TAB_LABELS = [
  "需求与澄清",
  "总览",
  "执行会话",
  "探索链路",
  "实时播报",
  "结果与证据",
  "问题",
  "报告",
  "代码与变更",
]

/** The four P6 execution tab slots and the root test id each real tab renders. */
const EXECUTION_TAB_SLOTS = [
  ["执行会话", "sessions-tab"],
  ["探索链路", "exploration-tab"],
  ["实时播报", "broadcast-tab"],
  ["结果与证据", "results-tab"],
] as const

/** The two P7 delivery tabs mounted in place of the P3 placeholders. */
const DELIVERY_TAB_SLOTS = [
  ["问题", "issues-tab"],
  ["报告", "report-tab"],
] as const

/** Result sent by POST requirements; the backend DTO keys stay camelCase. */
const CREATED_REQUIREMENT: Requirement = {
  id: "1844674407370956009",
  versionId: VERSION_ID,
  title: "退款审核",
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
  createdAt: "2026-10-28T10:00:00Z",
  updatedAt: "2026-10-28T10:00:00Z",
}

function versionEvent(seq: number, type: string): VersionEvent {
  return {
    seq,
    channel: `version:${VERSION_ID}`,
    type,
    version_id: VERSION_ID,
    batch_id: null,
    case_id: null,
    step_id: null,
    agent_role: null,
    session_id: null,
    ts: "2026-10-28T10:00:00Z",
    summary: "版本状态变化",
    detail: { phase: "clarifying", reason: "new_requirement" },
    refs: { evidence: [], case: [], requirement: [], code: [] },
  }
}

const ACTIVE_BATCH_ID = "1844674407370955165"
/** Batches for the header selector / overview report entry. */
const HEADER_BATCH_1 = "1844674407370955171"
const HEADER_BATCH_2 = "1844674407370955172"
const ISSUE_ID = "1844674407370955410"
const REPORT_ID = "1844674407370955501"
const SECOND_REPORT_ID = "1844674407370955502"

function batchFixture(overrides: Partial<Batch> = {}): Batch {
  return {
    id: ACTIVE_BATCH_ID,
    version_id: VERSION_ID,
    batch_no: 1,
    status: "running",
    paused: false,
    pause_mode: "",
    pause_reason: "",
    baseline_hash: "a".repeat(64),
    code_baseline_commit: null,
    head_commit: null,
    counts: {
      total: 2,
      passed: 1,
      failed: 0,
      blocked: 0,
      inconclusive: 0,
      skipped: 0,
      interrupted: 0,
    },
    created_by: "1",
    started_at: "2026-10-28T00:00:00Z",
    finished_at: null,
    failure_reason: "",
    ...overrides,
  }
}

function batchesResult(batches: Batch[] = []) {
  return {
    batches,
    nextCursor: "",
    hasMore: false,
    loading: false,
    error: "",
    streamStatus: "live" as const,
    refresh: vi.fn().mockResolvedValue(undefined),
  }
}

function deliveryPage<T>(items: T[] = []) {
  return {
    items,
    reports: items,
    issues: items,
    nextCursor: "",
    hasMore: false,
    loading: false,
    loadingMore: false,
    error: "",
    streamStatus: "live" as const,
    refresh: vi.fn().mockResolvedValue(undefined),
  }
}

/** A resolved (or empty) report resource for the page-level `?report=` sync. */
function reportResult(report: object | null) {
  return {
    data: report,
    report,
    loading: false,
    error: "",
    streamStatus: "live" as const,
    refresh: vi.fn(),
  }
}

function actionsResult() {
  return {
    createBatch: mocks.createBatch,
    pause: mocks.pauseBatch,
    resume: mocks.resumeBatch,
    cancel: vi.fn(),
    rerun: vi.fn(),
    exportReport: vi.fn(),
    pending: "" as const,
    error: "",
    clearError: vi.fn(),
  }
}

const CODE_BASE = "0123456789abcdef0123456789abcdef01234567"
const CODE_HEAD = "89abcdef0123456789abcdef0123456789abcdef"

const CODE_CHANGES: ChangeList = {
  changeSetId: "1844674407370955163",
  baseCommit: CODE_BASE,
  headCommit: CODE_HEAD,
  stats: { files: 1, additions: 12, deletions: 1, truncated: false },
  files: [
    {
      path: "src/api/refund.ts",
      oldPath: null,
      status: "M",
      additions: 12,
      deletions: 1,
      binary: false,
      headBlobHash: "git-sha1:1111111111111111111111111111111111111111",
    },
  ],
  nextCursor: null,
  hasMore: false,
}

const CODE_INDEX: IndexRun = {
  runId: "1844674407370955164",
  repoId: 3,
  commit: CODE_HEAD,
  status: "done",
  stats: { files: 20, truncated: false },
  startedAt: "2026-10-28T00:50:00Z",
  finishedAt: "2026-10-28T00:52:03Z",
}

const CODE_ANALYSIS: Analysis = {
  id: "1844674407370955162",
  versionId: VERSION_ID,
  repoId: 3,
  baseCommit: CODE_BASE,
  headCommit: CODE_HEAD,
  status: "done",
  degraded: false,
  analysisVersion: 1,
  model: "deepseek-chat",
  promptVersion: "p4.1",
  changeSetId: "1844674407370955163",
  changeSummary: "退款接口新增 reason 参数",
  stats: null,
  factVersions: [],
  candidates: [],
  unmapped: [],
  errors: [],
  createdAt: "2026-10-28T01:00:00Z",
  startedAt: "2026-10-28T01:00:00Z",
  finishedAt: "2026-10-28T01:02:03Z",
}

/** Minimal report list item for the page-level degraded flag / deep-link tests. */
const REPORT_SUMMARY = {
  id: REPORT_ID,
  versionId: VERSION_ID,
  batchId: HEADER_BATCH_1,
  revision: 1,
  status: "published",
  title: "Batch 1 报告",
  inputFingerprint: "f".repeat(64),
  markdown: { sha256: "e".repeat(64), sizeBytes: 1024, downloadPath: "" },
  staleReason: "",
  publishedAt: "2026-10-28T11:00:00Z",
  createdAt: "2026-10-28T11:00:00Z",
  updatedAt: "2026-10-28T11:00:00Z",
}

const DEGRADED_REPORT = {
  ...REPORT_SUMMARY,
  batchNo: 1,
  batchKind: "baseline",
  content: { degraded: true, requirements: [] },
}

describe("VersionDetailPage", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    mocks.getVersion.mockResolvedValue(VERSION)
    // The mounted workbench loads these on mount; empty data keeps the shell
    // assertions deterministic.
    mocks.listRequirements.mockResolvedValue({ items: [], nextCursor: "", hasMore: false })
    mocks.listRelations.mockResolvedValue([])
    mocks.getClarification.mockResolvedValue({
      session: null,
      questions: { open: 0, answered: 0, dismissed: 0 },
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
    mocks.useVersionStream.mockReturnValue({ events: [], status: "live", reconnect: vi.fn() })
    mocks.fetchVersionEvents.mockResolvedValue({ events: [], cursor: 0, hasMore: false })
    mocks.useBatches.mockReturnValue(batchesResult())
    mocks.useBatch.mockReturnValue({
      batch: null,
      loading: false,
      error: "",
      streamStatus: "live",
      refresh: vi.fn(),
    })
    mocks.useCaseRuns.mockReturnValue({
      caseRuns: [],
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
    mocks.useReport.mockReturnValue({
      report: null,
      loading: false,
      error: "",
      streamStatus: "live",
      refresh: vi.fn(),
    })
    mocks.useExecution.mockReturnValue({
      batches: [],
      activeBatch: null,
      loading: false,
      error: "",
      streamStatus: "live",
      refresh: vi.fn(),
      actions: actionsResult(),
    })
    mocks.useExecutionActions.mockReturnValue(actionsResult())
    mocks.listExecutionSessions.mockResolvedValue({ items: [], nextCursor: "", hasMore: false })
    // Keep the evidence viewer of a `?evidence=` deep link pending: the test
    // only pins that the query parameter survives the tab normalization.
    mocks.getEvidenceMeta.mockReturnValue(new Promise(() => {}))
    mocks.getEvidenceUrl.mockReturnValue(
      `/api/versions/${VERSION_ID}/evidence/1844674407370955402/bytes`,
    )
    mocks.createBatch.mockResolvedValue(batchFixture())
    mocks.pauseBatch.mockResolvedValue(true)
    mocks.resumeBatch.mockResolvedValue(true)
    for (const mock of Object.values(codeIntelMocks)) mock.mockReset()
    codeIntelMocks.getVersionChanges.mockResolvedValue(CODE_CHANGES)
    codeIntelMocks.getLatestIndexRun.mockResolvedValue(CODE_INDEX)
    codeIntelMocks.getLatestAnalysis.mockResolvedValue(CODE_ANALYSIS)
    mocks.useIssues.mockImplementation(() => deliveryPage())
    mocks.useReports.mockReturnValue(deliveryPage())
    mocks.useReport.mockReturnValue({
      data: null,
      report: null,
      loading: false,
      error: "",
      streamStatus: "live",
      refresh: vi.fn(),
    })
    mocks.createRequirement.mockResolvedValue(CREATED_REQUIREMENT)
    mocks.getIncrementalPlan.mockResolvedValue({
      versionId: VERSION_ID,
      phase: "clarifying",
      parentBatchId: null,
      run: null,
      newCases: [],
      affectedCases: [],
      unmappedChanges: [],
      defaultCaseIds: [],
      warnings: [],
      degraded: false,
    })
  })

  afterEach(() => {
    cleanup()
    window.history.replaceState(null, "", "/")
  })

  it("renders the nine tabs with requirements active by default", async () => {
    render(<VersionDetailPage versionId={VERSION_ID} />)

    expect(await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })).toBeTruthy()
    expect(mocks.getVersion).toHaveBeenCalledWith(VERSION_ID)

    const tabs = screen.getAllByRole("tab")
    expect(tabs.map((tab) => tab.textContent)).toEqual(TAB_LABELS)
    expect(screen.getByRole("tab", { name: "需求与澄清" }).getAttribute("aria-selected")).toBe(
      "true",
    )
    expect(screen.getByTestId("requirements-workbench-slot")).toBeTruthy()
  })

  it("opens the overview tab from the ?tab=overview deep link", async () => {
    window.history.replaceState(null, "", `/versions/${VERSION_ID}?tab=overview`)

    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })

    await waitFor(() =>
      expect(screen.getByRole("tab", { name: "总览" }).getAttribute("aria-selected")).toBe("true"),
    )

    const metrics = screen.getByTestId("overview-metrics")
    expect(within(metrics).getByRole("heading", { name: "需求覆盖" })).toBeTruthy()
    expect(within(metrics).getByText(/已确认 1 \/ 已覆盖 — \/ 缺口 —/)).toBeTruthy()
    expect(within(metrics).getByRole("heading", { name: "用例结果" })).toBeTruthy()
    expect(within(metrics).getByText("暂无执行结果")).toBeTruthy()
    expect(within(metrics).getByRole("heading", { name: "问题" })).toBeTruthy()
    expect(within(metrics).getByText("未解决 0 · 已确认 0")).toBeTruthy()
    expect(within(metrics).getByRole("heading", { name: "当前批次" })).toBeTruthy()

    expect(screen.getByTestId("overview-coverage-matrix")).toBeTruthy()
    expect(screen.getByText("暂无报告，生成批次报告后展示覆盖矩阵。")).toBeTruthy()
    expect(screen.getByTestId("overview-batch-timeline")).toBeTruthy()
    expect(screen.getAllByText("暂无批次").length).toBeGreaterThan(0)
    expect(screen.getByTestId("overview-issue-top")).toBeTruthy()
    expect(screen.getByText("暂无未解决问题。")).toBeTruthy()
    expect(screen.getByTestId("overview-code-summary")).toBeTruthy()
    expect(screen.getByRole("button", { name: "查看代码与变更" })).toBeTruthy()
  })

  it("switches tabs, syncs ?tab= and returns from the issues tab", async () => {
    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })

    fireEvent.mouseDown(screen.getByRole("tab", { name: "问题" }))

    await waitFor(() => expect(window.location.search).toContain("tab=issues"))
    expect(await screen.findByTestId("issues-tab")).toBeTruthy()
    expect(screen.queryByText("阶段 P7")).toBeNull()
    expect(screen.queryByText("该 Tab 尚未接入数据，不展示占位假数据。")).toBeNull()

    fireEvent.mouseDown(screen.getByRole("tab", { name: "需求与澄清" }))

    await waitFor(() =>
      expect(screen.getByRole("tab", { name: "需求与澄清" }).getAttribute("aria-selected")).toBe(
        "true",
      ),
    )
    expect(screen.getByTestId("requirements-workbench-slot")).toBeTruthy()
    await waitFor(() => expect(window.location.search).toContain("tab=requirements"))
  })

  it("renders the four P6 execution tab slots instead of placeholders", async () => {
    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })

    for (const [label, testId] of EXECUTION_TAB_SLOTS) {
      fireEvent.mouseDown(screen.getByRole("tab", { name: label }))

      expect(await screen.findByTestId(testId)).toBeTruthy()
      expect(screen.queryByText("阶段 P6")).toBeNull()
      expect(screen.queryByText("该 Tab 尚未接入数据，不展示占位假数据。")).toBeNull()
    }
  })

  it("mounts the real delivery issues and report tabs", async () => {
    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })

    for (const [label, testId] of DELIVERY_TAB_SLOTS) {
      fireEvent.mouseDown(screen.getByRole("tab", { name: label }))

      expect(await screen.findByTestId(testId)).toBeTruthy()
      expect(screen.queryByText("阶段 P7")).toBeNull()
      expect(screen.queryByText("该 Tab 尚未接入数据，不展示占位假数据。")).toBeNull()
    }
  })

  it("shows 开始执行 only when an executing version has no active batch", async () => {
    mocks.getVersion.mockResolvedValue({ ...VERSION, status: "executing" })

    const { unmount } = render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })

    fireEvent.click(await screen.findByTestId("exec-start-btn"))
    await waitFor(() => expect(mocks.createBatch).toHaveBeenCalled())

    unmount()
    mocks.useBatches.mockReturnValue(batchesResult([batchFixture()]))
    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })

    expect(screen.queryByTestId("exec-start-btn")).toBeNull()
    expect(screen.getByTestId("exec-batch-selector")).toBeTruthy()
    expect(screen.getByTestId("exec-pause-btn")).toBeTruthy()
  })

  it("shows the paused banner and resume action for a paused batch", async () => {
    mocks.getVersion.mockResolvedValue({ ...VERSION, status: "executing" })
    mocks.useBatches.mockReturnValue(
      batchesResult([batchFixture({ paused: true, pause_mode: "graceful" })]),
    )

    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })

    expect(await screen.findByTestId("paused-banner")).toBeTruthy()
    expect(screen.getAllByText(/优雅暂停/).length).toBeGreaterThan(0)
    expect(screen.getByText(/已完成 1\/2 个用例/)).toBeTruthy()

    fireEvent.click(screen.getByTestId("exec-resume-btn"))
    await waitFor(() => expect(mocks.resumeBatch).toHaveBeenCalledWith(ACTIVE_BATCH_ID))
  })

  it("mounts the real code changes tab instead of the P4 placeholder", async () => {
    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })

    fireEvent.mouseDown(screen.getByRole("tab", { name: "代码与变更" }))

    expect(await screen.findByRole("heading", { name: "变更文件" })).toBeTruthy()
    expect(screen.getByText("src/api/refund.ts")).toBeTruthy()
    expect(screen.queryByText("阶段 P4")).toBeNull()
    expect(screen.queryByText(/代码与变更在 P4 阶段接入/)).toBeNull()
    expect(screen.queryByText("该 Tab 尚未接入数据，不展示占位假数据。")).toBeNull()

    expect(codeIntelMocks.getVersionChanges).toHaveBeenCalledWith(VERSION_ID)
    expect(codeIntelMocks.getLatestIndexRun).toHaveBeenCalledWith(3)
    expect(codeIntelMocks.getLatestAnalysis).toHaveBeenCalledWith(VERSION_ID)
  })

  it("shows the header status, binding summary and action for the current state", async () => {
    mocks.getVersion.mockResolvedValue({
      ...VERSION,
      bindings: {
        ...VERSION.bindings,
        codeRepository: { id: 3, name: "order-web", available: false },
      },
    })

    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })

    expect(screen.getByText("澄清中")).toBeTruthy()
    expect(screen.getByText("系统：订单系统")).toBeTruthy()
    expect(screen.getByText("环境：staging")).toBeTruthy()
    expect(screen.getByText("代码仓：order-web（已删除）")).toBeTruthy()
    expect(screen.getByText("未澄清需求 2")).toBeTruthy()
    expect(screen.getByText("待澄清问题 2")).toBeTruthy()
    expect(screen.getByRole("button", { name: "进入设计" })).toBeTruthy()
    expect(screen.queryByRole("button", { name: "开始澄清" })).toBeNull()
    expect(screen.getByRole("link", { name: "返回版本列表" }).getAttribute("href")).toBe(
      "/versions",
    )
  })

  it("starts clarification from the draft header and refreshes on demand", async () => {
    mocks.getVersion.mockResolvedValue({ ...VERSION, status: "draft" })
    mocks.startClarification.mockResolvedValue({
      ...VERSION,
      status: "clarifying",
      revision: 3,
    })

    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })
    const versionHeader = screen.getAllByRole("banner")[0]
    expect(within(versionHeader).getByText("草稿")).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "开始澄清" }))

    await waitFor(() => expect(mocks.startClarification).toHaveBeenCalledWith(VERSION_ID))
    expect(await screen.findByText("澄清中")).toBeTruthy()
    expect(screen.getByRole("button", { name: "进入设计" })).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "刷新" }))
    await waitFor(() => expect(mocks.getVersion).toHaveBeenCalledTimes(2))
  })

  it("asks for confirmation when entering design with unclarified requirements", async () => {
    mocks.enterDesign.mockRejectedValueOnce(
      new ApiError({
        status: 409,
        code: "conflict",
        message: "unclarified requirements",
        details: {
          reason: "unclarified_requirements",
          unclarifiedRequirementIds: ["1001", "1002"],
          openQuestionCount: 1,
        },
      }),
    )
    mocks.enterDesign.mockResolvedValueOnce({
      version: { ...VERSION, status: "designing", revision: 6 },
      warnings: ["2 个需求未澄清"],
    })

    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })

    fireEvent.click(screen.getByRole("button", { name: "进入设计" }))

    const confirm = await screen.findByRole("alertdialog")
    expect(confirm.textContent).toContain("2 条需求")
    expect(confirm.textContent).toContain("1 个待澄清问题")
    expect(mocks.enterDesign).toHaveBeenCalledWith(VERSION_ID, false)

    fireEvent.click(within(confirm).getByRole("button", { name: "确认进入设计" }))

    await waitFor(() => expect(mocks.enterDesign).toHaveBeenLastCalledWith(VERSION_ID, true))
    expect(await screen.findByText("设计中")).toBeTruthy()
    expect(await screen.findByText("2 个需求未澄清")).toBeTruthy()
  })

  it("shows 继续中（第 2 批） after creating a requirement on a completed version", async () => {
    const completed: Version = { ...VERSION, status: "completed", batch_no: 1, continuePhase: null }
    const continuing: Version = {
      ...VERSION,
      status: "continuing",
      batch_no: 1,
      continuePhase: "clarifying",
      revision: 4,
    }
    mocks.getVersion.mockReset()
    mocks.getVersion.mockResolvedValueOnce(completed).mockResolvedValue(continuing)
    // Start outside the workbench so the header entry is what navigates.
    window.history.replaceState(null, "", `/versions/${VERSION_ID}?tab=overview`)

    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })
    await waitFor(() =>
      expect(screen.getByRole("tab", { name: "总览" }).getAttribute("aria-selected")).toBe("true"),
    )
    expect(screen.queryByTestId("continuing-badge")).toBeNull()

    // The completed-version entry opens the requirements workbench, where the
    // requirement is created; the backend turns the version into continuing.
    fireEvent.click(screen.getByTestId("add-requirement-btn"))
    await waitFor(() =>
      expect(screen.getByRole("tab", { name: "需求与澄清" }).getAttribute("aria-selected")).toBe(
        "true",
      ),
    )
    expect(await screen.findByRole("heading", { name: "新增需求" })).toBeTruthy()

    fireEvent.change(screen.getByLabelText("标题"), { target: { value: "退款审核" } })
    fireEvent.click(screen.getByRole("button", { name: "创建需求" }))

    await waitFor(() => expect(mocks.createRequirement).toHaveBeenCalled())
    await waitFor(() => expect(mocks.getVersion).toHaveBeenCalledTimes(2))

    expect((await screen.findByTestId("continuing-badge")).textContent).toContain(
      "继续中（第 2 批）",
    )
    expect(screen.getByTestId("continuing-banner").textContent).toContain("增量澄清")
    expect(screen.getByTestId("add-requirement-btn")).toBeTruthy()
    expect(screen.getByTestId("view-changes-btn")).toBeTruthy()
  })

  it("refetches the version when a version.status_changed event arrives", async () => {
    const completed: Version = { ...VERSION, status: "completed", batch_no: 1, continuePhase: null }
    const continuing: Version = {
      ...VERSION,
      status: "continuing",
      batch_no: 1,
      continuePhase: "designing",
      revision: 4,
    }
    mocks.getVersion.mockReset()
    mocks.getVersion.mockResolvedValueOnce(completed).mockResolvedValue(continuing)

    const { rerender } = render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })
    expect(screen.queryByTestId("continuing-banner")).toBeNull()

    mocks.useVersionStream.mockReturnValue({
      events: [versionEvent(7, "version.status_changed")],
      status: "live",
      reconnect: vi.fn(),
    })
    rerender(<VersionDetailPage versionId={VERSION_ID} />)

    await waitFor(() => expect(mocks.getVersion).toHaveBeenCalledTimes(2))
    expect((await screen.findByTestId("continuing-badge")).textContent).toContain(
      "继续中（第 2 批）",
    )
    expect(screen.getByTestId("continue-phase-label").textContent).toContain("增量设计")
  })

  it("keeps the last data, reports a load failure and retries", async () => {
    mocks.getVersion
      .mockRejectedValueOnce(
        new ApiError({ status: 0, code: "network_error", message: "connection refused" }),
      )
      .mockResolvedValueOnce(VERSION)

    render(<VersionDetailPage versionId={VERSION_ID} />)

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("无法连接 API 服务")

    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    expect(await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })).toBeTruthy()
    expect(mocks.getVersion).toHaveBeenCalledTimes(2)
  })

  it("honours ?tab=issues&issue= and syncs the URL when the issue opens or closes", async () => {
    window.history.replaceState(null, "", `/versions/${VERSION_ID}?tab=issues&issue=${ISSUE_ID}`)

    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })

    await waitFor(() =>
      expect(mocks.issuesProps).toHaveBeenLastCalledWith(
        expect.objectContaining({ issueId: ISSUE_ID }),
      ),
    )
    expect(window.location.search).toContain("tab=issues")
    expect(window.location.search).toContain(`issue=${ISSUE_ID}`)

    act(() => {
      mocks.issuesProps.mock.calls.at(-1)?.[0].onIssueChange?.("")
    })
    await waitFor(() => expect(window.location.search).not.toContain("issue="))
    expect(window.location.search).toContain("tab=issues")
    await waitFor(() =>
      expect(mocks.issuesProps).toHaveBeenLastCalledWith(expect.objectContaining({ issueId: "" })),
    )

    act(() => {
      mocks.issuesProps.mock.calls.at(-1)?.[0].onIssueChange?.(ISSUE_ID)
    })
    await waitFor(() => expect(window.location.search).toContain(`issue=${ISSUE_ID}`))
    expect(window.location.search).toContain("tab=issues")
  })

  it("honours ?tab=report&report= and syncs the URL when the report changes", async () => {
    window.history.replaceState(null, "", `/versions/${VERSION_ID}?tab=report&report=${REPORT_ID}`)

    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })

    await waitFor(() =>
      expect(mocks.reportsProps).toHaveBeenLastCalledWith(
        expect.objectContaining({ reportId: REPORT_ID }),
      ),
    )
    expect(window.location.search).toContain("tab=report")
    expect(window.location.search).toContain(`report=${REPORT_ID}`)

    act(() => {
      mocks.reportsProps.mock.calls.at(-1)?.[0].onReportChange?.(SECOND_REPORT_ID)
    })
    await waitFor(() => expect(window.location.search).toContain(`report=${SECOND_REPORT_ID}`))
    expect(window.location.search).toContain("tab=report")

    act(() => {
      mocks.reportsProps.mock.calls.at(-1)?.[0].onReportChange?.("")
    })
    await waitFor(() => expect(window.location.search).not.toContain("report="))
    expect(window.location.search).toContain("tab=report")
    await waitFor(() =>
      expect(mocks.reportsProps).toHaveBeenLastCalledWith(
        expect.objectContaining({ reportId: "" }),
      ),
    )
  })

  it("adopts the deep-linked report's batch for the header selector", async () => {
    mocks.getVersion.mockResolvedValue({ ...VERSION, status: "completed" })
    const earlier = batchFixture({
      id: HEADER_BATCH_1,
      batch_no: 1,
      status: "done",
      finished_at: "2026-10-28T01:00:00Z",
    })
    const later = batchFixture({
      id: HEADER_BATCH_2,
      batch_no: 2,
      status: "done",
      finished_at: "2026-10-28T02:00:00Z",
    })
    mocks.useBatches.mockReturnValue(batchesResult([later, earlier]))
    // The deep-linked report belongs to the older batch; the newest batch is
    // what the container would otherwise select.
    const deepLinkedReport = { ...REPORT_SUMMARY, batchId: HEADER_BATCH_1 }
    mocks.useReport.mockImplementation((_versionId: string, nextReportId: string) =>
      reportResult(nextReportId === REPORT_ID ? deepLinkedReport : null),
    )
    window.history.replaceState(null, "", `/versions/${VERSION_ID}?tab=report&report=${REPORT_ID}`)

    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })

    await waitFor(() =>
      expect(mocks.reportsProps).toHaveBeenLastCalledWith(
        expect.objectContaining({ reportId: REPORT_ID, batchId: HEADER_BATCH_1 }),
      ),
    )
    expect((screen.getByTestId("exec-batch-selector") as HTMLSelectElement).value).toBe(
      HEADER_BATCH_1,
    )
  })

  it("removes the ?report= deep link when the header batch changes", async () => {
    mocks.getVersion.mockResolvedValue({ ...VERSION, status: "completed" })
    const earlier = batchFixture({
      id: HEADER_BATCH_1,
      batch_no: 1,
      status: "done",
      finished_at: "2026-10-28T01:00:00Z",
    })
    const later = batchFixture({
      id: HEADER_BATCH_2,
      batch_no: 2,
      status: "done",
      finished_at: "2026-10-28T02:00:00Z",
    })
    mocks.useBatches.mockReturnValue(batchesResult([later, earlier]))
    window.history.replaceState(null, "", `/versions/${VERSION_ID}?tab=report&report=${REPORT_ID}`)

    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })
    await waitFor(() =>
      expect(mocks.reportsProps).toHaveBeenLastCalledWith(
        expect.objectContaining({ reportId: REPORT_ID }),
      ),
    )

    fireEvent.change(screen.getByTestId("exec-batch-selector"), {
      target: { value: HEADER_BATCH_1 },
    })

    await waitFor(() => expect(window.location.search).not.toContain("report="))
    await waitFor(() =>
      expect(mocks.reportsProps).toHaveBeenLastCalledWith(
        expect.objectContaining({ reportId: "" }),
      ),
    )
    expect(window.location.search).toContain("tab=report")
  })

  it("selects the overview report batch and the header batch selector for the report tab", async () => {
    mocks.getVersion.mockResolvedValue({ ...VERSION, status: "completed" })
    const earlier = batchFixture({
      id: HEADER_BATCH_1,
      batch_no: 1,
      status: "done",
      finished_at: "2026-10-28T01:00:00Z",
    })
    const later = batchFixture({
      id: HEADER_BATCH_2,
      batch_no: 2,
      status: "done",
      finished_at: "2026-10-28T02:00:00Z",
    })
    mocks.useBatches.mockReturnValue(batchesResult([later, earlier]))
    window.history.replaceState(null, "", `/versions/${VERSION_ID}?tab=overview`)

    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })
    await waitFor(() =>
      expect(screen.getByRole("tab", { name: "总览" }).getAttribute("aria-selected")).toBe("true"),
    )

    fireEvent.click(screen.getByRole("button", { name: "查看 Batch 1 报告" }))

    await waitFor(() => expect(window.location.search).toContain("tab=report"))
    await waitFor(() =>
      expect(mocks.reportsProps).toHaveBeenLastCalledWith(
        expect.objectContaining({ batchId: HEADER_BATCH_1 }),
      ),
    )

    fireEvent.change(screen.getByTestId("exec-batch-selector"), {
      target: { value: HEADER_BATCH_2 },
    })
    await waitFor(() =>
      expect(mocks.reportsProps).toHaveBeenLastCalledWith(
        expect.objectContaining({ batchId: HEADER_BATCH_2 }),
      ),
    )
    expect(window.location.search).toContain("tab=report")
  })

  it("replaces a stale ?report= deep link when opening a batch report from the overview", async () => {
    mocks.getVersion.mockResolvedValue({ ...VERSION, status: "completed" })
    const earlier = batchFixture({
      id: HEADER_BATCH_1,
      batch_no: 1,
      status: "done",
      finished_at: "2026-10-28T01:00:00Z",
    })
    const later = batchFixture({
      id: HEADER_BATCH_2,
      batch_no: 2,
      status: "done",
      finished_at: "2026-10-28T02:00:00Z",
    })
    mocks.useBatches.mockReturnValue(batchesResult([later, earlier]))
    // The old deep link points at the newest batch, so only the overview entry
    // can move the shared selection to the clicked (older) batch.
    const deepLinkedReport = { ...REPORT_SUMMARY, batchId: HEADER_BATCH_2 }
    mocks.useReport.mockImplementation((_versionId: string, nextReportId: string) =>
      reportResult(nextReportId === REPORT_ID ? deepLinkedReport : null),
    )
    window.history.replaceState(
      null,
      "",
      `/versions/${VERSION_ID}?tab=overview&report=${REPORT_ID}`,
    )

    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })
    await waitFor(() =>
      expect(screen.getByRole("tab", { name: "总览" }).getAttribute("aria-selected")).toBe("true"),
    )

    fireEvent.click(await screen.findByRole("button", { name: "查看 Batch 1 报告" }))

    await waitFor(() => expect(window.location.search).toContain("tab=report"))
    await waitFor(() =>
      expect(mocks.reportsProps).toHaveBeenLastCalledWith(
        expect.objectContaining({ batchId: HEADER_BATCH_1, reportId: "" }),
      ),
    )
    expect(window.location.search).not.toContain(`report=${REPORT_ID}`)
  })

  it("opens the requirement create editor from 新增需求 with a non-empty requirement list", async () => {
    mocks.getVersion.mockResolvedValue({ ...VERSION, status: "completed" })
    mocks.listRequirements.mockResolvedValue({
      items: [CREATED_REQUIREMENT],
      nextCursor: "",
      hasMore: false,
    })
    window.history.replaceState(null, "", `/versions/${VERSION_ID}?tab=overview`)

    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })

    fireEvent.click(await screen.findByTestId("add-requirement-btn"))

    await waitFor(() =>
      expect(screen.getByRole("tab", { name: "需求与澄清" }).getAttribute("aria-selected")).toBe(
        "true",
      ),
    )
    expect(await screen.findByRole("heading", { name: "新增需求" })).toBeTruthy()
    expect(screen.getByRole("button", { name: "创建需求" })).toBeTruthy()
    expect(screen.queryByRole("heading", { name: "编辑需求" })).toBeNull()
  })

  it("opens the issues deep link when exploration navigates to an issue", async () => {
    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })

    fireEvent.mouseDown(screen.getByRole("tab", { name: "探索链路" }))
    fireEvent.click(await screen.findByTestId("exploration-issue-link"))

    await waitFor(() =>
      expect(screen.getByRole("tab", { name: "问题" }).getAttribute("aria-selected")).toBe("true"),
    )
    expect(window.location.search).toContain("tab=issues")
    expect(window.location.search).toContain(`issue=1844674407370955410`)
    expect(mocks.issuesProps).toHaveBeenLastCalledWith(
      expect.objectContaining({ issueId: "1844674407370955410" }),
    )
  })

  it("passes ?node/?batch/?req/?status/?abnormal/?q to the exploration tab", async () => {
    mocks.getVersion.mockResolvedValue({ ...VERSION, status: "executing" })
    const earlier = batchFixture({
      id: HEADER_BATCH_1,
      batch_no: 1,
      status: "done",
      finished_at: "2026-10-28T01:00:00Z",
    })
    const later = batchFixture({
      id: HEADER_BATCH_2,
      batch_no: 2,
      status: "done",
      finished_at: "2026-10-28T02:00:00Z",
    })
    mocks.useBatches.mockReturnValue(batchesResult([later, earlier]))
    const caseNodeId = "1844674407370955210"
    const requirementId = "1844674407370955101"
    window.history.replaceState(
      null,
      "",
      `/versions/${VERSION_ID}?tab=exploration&node=case:${caseNodeId}&batch=${HEADER_BATCH_1}&req=${requirementId}&status=failed&abnormal=1&q=${encodeURIComponent("登录")}`,
    )

    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })

    await waitFor(() =>
      expect(mocks.explorationProps).toHaveBeenLastCalledWith(
        expect.objectContaining({
          batchId: HEADER_BATCH_1,
          selectedNodeId: `case:${caseNodeId}`,
          initialFilters: expect.objectContaining({
            requirementId,
            resultStatus: "failed",
            abnormalOnly: true,
            q: "登录",
          }),
        }),
      ),
    )
    expect((screen.getByTestId("exec-batch-selector") as HTMLSelectElement).value).toBe(
      HEADER_BATCH_1,
    )
  })

  it("opens the exploration tab and the Sheet from ?node= without ?tab=", async () => {
    const caseNodeId = "1844674407370955210"
    const evidenceId = "1844674407370955402"
    window.history.replaceState(
      null,
      "",
      `/versions/${VERSION_ID}?node=case:${caseNodeId}&evidence=${evidenceId}`,
    )

    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })

    await waitFor(() =>
      expect(screen.getByRole("tab", { name: "探索链路" }).getAttribute("aria-selected")).toBe(
        "true",
      ),
    )
    // The inferred tab is persisted so closing the Sheet (which deletes `node`)
    // cannot fall back to the requirements tab on refresh.
    await waitFor(() =>
      expect(new URLSearchParams(window.location.search).get("tab")).toBe("exploration"),
    )
    const params = new URLSearchParams(window.location.search)
    expect(params.get("node")).toBe(`case:${caseNodeId}`)
    expect(params.get("evidence")).toBe(evidenceId)
    await waitFor(() =>
      expect(mocks.explorationProps).toHaveBeenLastCalledWith(
        expect.objectContaining({ selectedNodeId: `case:${caseNodeId}` }),
      ),
    )
  })

  it("syncs exploration Sheet open/close to ?node=", async () => {
    const caseNodeId = "1844674407370955210"
    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })

    fireEvent.mouseDown(screen.getByRole("tab", { name: "探索链路" }))
    await screen.findByTestId("exploration-tab")
    await waitFor(() =>
      expect(mocks.explorationProps).toHaveBeenLastCalledWith(
        expect.objectContaining({ selectedNodeId: "" }),
      ),
    )

    act(() => {
      mocks.explorationProps.mock.calls.at(-1)?.[0].onSelectedNodeChange?.(`case:${caseNodeId}`)
    })
    await waitFor(() =>
      expect(new URLSearchParams(window.location.search).get("node")).toBe(`case:${caseNodeId}`),
    )
    await waitFor(() =>
      expect(mocks.explorationProps).toHaveBeenLastCalledWith(
        expect.objectContaining({ selectedNodeId: `case:${caseNodeId}` }),
      ),
    )

    act(() => {
      mocks.explorationProps.mock.calls.at(-1)?.[0].onSelectedNodeChange?.("")
    })
    await waitFor(() => expect(new URLSearchParams(window.location.search).get("node")).toBeNull())
    await waitFor(() =>
      expect(mocks.explorationProps).toHaveBeenLastCalledWith(
        expect.objectContaining({ selectedNodeId: "" }),
      ),
    )
  })

  it("syncs exploration filters to ?req/?status/?abnormal/?q=", async () => {
    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })

    fireEvent.mouseDown(screen.getByRole("tab", { name: "探索链路" }))
    await screen.findByTestId("exploration-tab")

    act(() => {
      mocks.explorationProps.mock.calls.at(-1)?.[0].onFiltersChange?.({
        requirementId: "1844674407370955101",
        resultStatus: "failed",
        abnormalOnly: true,
        q: "登录",
      })
    })

    await waitFor(() => {
      const params = new URLSearchParams(window.location.search)
      expect(params.get("req")).toBe("1844674407370955101")
      expect(params.get("status")).toBe("failed")
      expect(params.get("abnormal")).toBe("1")
      expect(params.get("q")).toBe("登录")
      expect(params.get("tab")).toBe("exploration")
    })
    await waitFor(() =>
      expect(mocks.explorationProps).toHaveBeenLastCalledWith(
        expect.objectContaining({
          initialFilters: expect.objectContaining({
            requirementId: "1844674407370955101",
            resultStatus: "failed",
            abnormalOnly: true,
            q: "登录",
          }),
        }),
      ),
    )
  })

  it("updates ?batch= when the header batch selector changes", async () => {
    mocks.getVersion.mockResolvedValue({ ...VERSION, status: "executing" })
    const earlier = batchFixture({
      id: HEADER_BATCH_1,
      batch_no: 1,
      status: "done",
      finished_at: "2026-10-28T01:00:00Z",
    })
    const later = batchFixture({
      id: HEADER_BATCH_2,
      batch_no: 2,
      status: "done",
      finished_at: "2026-10-28T02:00:00Z",
    })
    mocks.useBatches.mockReturnValue(batchesResult([later, earlier]))

    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })

    fireEvent.mouseDown(screen.getByRole("tab", { name: "探索链路" }))
    await screen.findByTestId("exploration-tab")

    fireEvent.change(screen.getByTestId("exec-batch-selector"), {
      target: { value: HEADER_BATCH_1 },
    })

    await waitFor(() =>
      expect(new URLSearchParams(window.location.search).get("batch")).toBe(HEADER_BATCH_1),
    )
    await waitFor(() =>
      expect(mocks.explorationProps).toHaveBeenLastCalledWith(
        expect.objectContaining({ batchId: HEADER_BATCH_1 }),
      ),
    )
  })

  it("passes the latest report degraded flag to the issues tab", async () => {
    mocks.useReports.mockReturnValue(deliveryPage([REPORT_SUMMARY]))
    mocks.useReport.mockReturnValue({
      data: DEGRADED_REPORT,
      report: DEGRADED_REPORT,
      loading: false,
      error: "",
      streamStatus: "live" as const,
      refresh: vi.fn(),
    })

    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })

    fireEvent.mouseDown(screen.getByRole("tab", { name: "问题" }))

    await waitFor(() =>
      expect(mocks.issuesProps).toHaveBeenLastCalledWith(
        expect.objectContaining({ degraded: true }),
      ),
    )
  })

  it("renders the detail route with the string id from the dynamic segment", async () => {
    const route = await VersionDetailRoutePage({ params: Promise.resolve({ id: VERSION_ID }) })

    render(route)

    expect(await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })).toBeTruthy()
    expect(mocks.getVersion).toHaveBeenCalledWith(VERSION_ID)
  })
})
