import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  useIssues: vi.fn(),
  useReports: vi.fn(),
  useReport: vi.fn(),
  getLatestAnalysis: vi.fn(),
  getIncrementalPlan: vi.fn(),
}))

vi.mock("@/features/delivery/hooks/useIssues", () => ({ useIssues: mocks.useIssues }))
vi.mock("@/features/delivery/hooks/useReports", () => ({
  useReports: mocks.useReports,
  useReport: mocks.useReport,
}))
vi.mock("@/features/code-intel/api", () => ({ getLatestAnalysis: mocks.getLatestAnalysis }))
vi.mock("./api", () => ({ getIncrementalPlan: mocks.getIncrementalPlan }))

import type { IssueSummary, ReportDetail } from "@/features/delivery/types"

import { type OverviewBatch, OverviewTab } from "./OverviewTab"
import type { Version } from "./types"

const VERSION_ID = "1844674407370955161"
const REPORT_ID = "1844674407370955501"
const BATCH_2 = "1844674407370955302"
const BATCH_1 = "1844674407370955301"
const ISSUE_BLOCKER = "1844674407370955401"
const ISSUE_MAJOR = "1844674407370955402"
const ISSUE_MINOR = "1844674407370955403"

function version(overrides: Partial<Version> = {}): Version {
  return {
    id: VERSION_ID,
    name: "订单系统 2.0",
    label: "v2.0",
    description: "",
    status: "executing",
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
    requirementCount: 4,
    clarifiedCount: 3,
    openQuestionCount: 0,
    revision: 5,
    createdBy: 1,
    updatedBy: 1,
    createdAt: "2026-10-27T10:00:00Z",
    updatedAt: "2026-10-28T10:00:00Z",
    ...overrides,
  }
}

function batch(overrides: Partial<OverviewBatch> = {}): OverviewBatch {
  return {
    id: BATCH_1,
    batch_no: 1,
    status: "done",
    kind: "baseline",
    code_baseline_commit: "0123456789abcdef0123456789abcdef01234567",
    head_commit: null,
    counts: {
      total: 3,
      passed: 2,
      failed: 1,
      blocked: 0,
      inconclusive: 0,
      skipped: 0,
      interrupted: 0,
    },
    finished_at: "2026-10-27T11:00:00Z",
    ...overrides,
  }
}

const LATEST_BATCH = batch({
  id: BATCH_2,
  batch_no: 2,
  status: "running",
  kind: "incremental",
  code_baseline_commit: "0123456789abcdef0123456789abcdef01234567",
  head_commit: "89abcdef0123456789abcdef0123456789abcdef",
  counts: {
    total: 5,
    passed: 3,
    failed: 1,
    blocked: 1,
    inconclusive: 0,
    skipped: 0,
    interrupted: 0,
  },
  finished_at: null,
})

const EARLIER_BATCH = batch({ id: BATCH_1 })

function issue(
  overrides: Partial<IssueSummary> & Pick<IssueSummary, "id" | "title" | "severity" | "status">,
): IssueSummary {
  return {
    versionId: VERSION_ID,
    batchId: BATCH_2,
    caseRunId: null,
    caseNodeId: null,
    stepResultId: null,
    source: "ai",
    category: "defect",
    confidence: "high",
    failureClass: "application",
    rerunSuggestion: "",
    recurrenceCount: 0,
    revision: 1,
    refs: [],
    codeRefs: [],
    dismissedReason: "",
    createdBy: "1",
    confirmedBy: null,
    confirmedAt: null,
    dismissedBy: null,
    dismissedAt: null,
    createdAt: "2026-10-28T10:00:00Z",
    updatedAt: "2026-10-28T10:00:00Z",
    ...overrides,
  }
}

const OPEN_ISSUES: IssueSummary[] = [
  issue({ id: ISSUE_BLOCKER, title: "支付回调丢失", severity: "blocker", status: "open" }),
  issue({ id: ISSUE_MAJOR, title: "退款金额错误", severity: "major", status: "open" }),
  issue({ id: ISSUE_MINOR, title: "按钮文案错别字", severity: "minor", status: "open" }),
]

const CONFIRMED_ISSUES: IssueSummary[] = [
  issue({
    id: "1844674407370955404",
    title: "已确认问题",
    severity: "major",
    status: "confirmed",
  }),
]

const REPORT: ReportDetail = {
  id: REPORT_ID,
  versionId: VERSION_ID,
  batchId: BATCH_2,
  revision: 1,
  status: "published",
  title: "Batch 2 报告",
  inputFingerprint: "f".repeat(64),
  markdown: { sha256: "e".repeat(64), sizeBytes: 1024, downloadPath: "" },
  staleReason: "",
  publishedAt: "2026-10-28T11:00:00Z",
  createdAt: "2026-10-28T11:00:00Z",
  updatedAt: "2026-10-28T11:00:00Z",
  batchNo: 2,
  batchKind: "incremental",
  content: {
    schema: "eztest.report.v1",
    version: {
      id: VERSION_ID,
      name: "订单系统 2.0",
      label: "v2.0",
      status: "executing",
      codeBaselineCommit: "0123456789abcdef0123456789abcdef01234567",
    },
    batch: {
      id: BATCH_2,
      batchNo: 2,
      kind: "incremental",
      status: "running",
      baselineHash: "a".repeat(64),
      graphHash: "b".repeat(64),
      codeBaseCommit: "0123456789abcdef0123456789abcdef01234567",
      headCommit: "89abcdef0123456789abcdef0123456789abcdef",
      startedAt: "2026-10-28T10:00:00Z",
      finishedAt: "",
    },
    summary: {
      caseCount: 5,
      passed: 3,
      failed: 1,
      blocked: 1,
      inconclusive: 0,
      skipped: 0,
      interrupted: 0,
      flakyCount: 0,
    },
    cases: [],
    requirements: [
      {
        requirementId: "1844674407370956001",
        title: "订单创建",
        status: "clarified",
        caseCount: 2,
        passed: 2,
        failed: 0,
      },
      {
        requirementId: "1844674407370956002",
        title: "库存扣减",
        status: "clarified",
        caseCount: 0,
        passed: 0,
        failed: 0,
      },
    ],
    issues: [],
    risks: [],
    degraded: false,
    appendix: {
      environment: "staging",
      system: "订单系统",
      commit: "89abcdef",
      generatedAt: "2026-10-28T11:00:00Z",
    },
  },
}

function page<T>(items: T[]) {
  return {
    items,
    reports: items,
    issues: items,
    hasMore: false,
    loading: false,
    loadingMore: false,
    error: "",
    streamStatus: "live" as const,
    refresh: vi.fn().mockResolvedValue(undefined),
  }
}

describe("OverviewTab", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    mocks.useIssues.mockImplementation((_versionId: string, filters: { status?: string }) =>
      page(filters.status === "confirmed" ? CONFIRMED_ISSUES : OPEN_ISSUES),
    )
    mocks.useReports.mockReturnValue(page([REPORT]))
    mocks.useReport.mockReturnValue({
      data: REPORT,
      report: REPORT,
      loading: false,
      error: "",
      streamStatus: "live" as const,
      refresh: vi.fn(),
    })
    mocks.getLatestAnalysis.mockRejectedValue(new Error("no analysis"))
    mocks.getIncrementalPlan.mockResolvedValue({
      versionId: VERSION_ID,
      phase: "executing",
      parentBatchId: BATCH_1,
      run: null,
      newCases: [],
      affectedCases: [{ nodeId: "case-1", title: "受影响用例", confidence: "high" }],
      unmappedChanges: [],
      defaultCaseIds: [],
      warnings: [],
      degraded: false,
    })
  })

  afterEach(cleanup)

  it("renders the four metric cards from the version, latest batch, report and issues", () => {
    render(
      <OverviewTab
        version={version()}
        batches={[LATEST_BATCH, EARLIER_BATCH]}
        stream={{ events: [], status: "live" }}
      />,
    )

    expect(screen.getByTestId("overview-metrics")).toBeTruthy()
    expect(screen.getByRole("heading", { name: "需求覆盖" })).toBeTruthy()
    expect(screen.getByText("已确认 3 / 已覆盖 1 / 缺口 1")).toBeTruthy()

    expect(screen.getByRole("heading", { name: "用例结果" })).toBeTruthy()
    expect(screen.getByText("通过 3 · 失败 1 · 阻塞 1 · 未执行 0")).toBeTruthy()

    expect(screen.getByRole("heading", { name: "问题" })).toBeTruthy()
    expect(screen.getByText("未解决 3 · 已确认 1")).toBeTruthy()
    expect(screen.getByText("blocker 1 · major 2 · minor 1")).toBeTruthy()

    expect(screen.getByRole("heading", { name: "当前批次" })).toBeTruthy()
    expect(screen.getByText(/Batch 2 · 增量/)).toBeTruthy()
    expect(within(screen.getByTestId("overview-metrics")).getByText(/89abcdef/)).toBeTruthy()

    const mindmapLink = screen.getByTestId("overview-open-mindmap")
    expect(mindmapLink.getAttribute("href")).toBe(`/versions/${VERSION_ID}/mindmap`)
    expect(mindmapLink.textContent).toContain("用例脑图")
  })

  it("renders the coverage matrix summary and batch timeline with kind labels", () => {
    const onOpenReports = vi.fn()
    render(
      <OverviewTab
        version={version()}
        batches={[LATEST_BATCH, EARLIER_BATCH]}
        stream={{ events: [], status: "live" }}
        onOpenReports={onOpenReports}
      />,
    )

    expect(screen.getByTestId("overview-coverage-matrix")).toBeTruthy()
    expect(screen.getByText("订单创建")).toBeTruthy()
    expect(screen.getByText("2 用例 · 2 通过 · 0 失败")).toBeTruthy()
    expect(screen.getByText("库存扣减")).toBeTruthy()
    expect(screen.getByText("0 用例 · 覆盖缺口")).toBeTruthy()

    const timeline = screen.getByTestId("overview-batch-timeline")
    expect(timeline.textContent).toContain("Batch 2")
    expect(timeline.textContent).toContain("增量")
    expect(timeline.textContent).toContain("Batch 1")
    expect(timeline.textContent).toContain("基线")

    fireEvent.click(screen.getByRole("button", { name: /查看 Batch 2 报告/ }))
    expect(onOpenReports).toHaveBeenCalledWith(BATCH_2)
  })

  it("lists the top open issues by severity and jumps to the issues tab", () => {
    const onOpenIssues = vi.fn()
    render(
      <OverviewTab
        version={version()}
        batches={[LATEST_BATCH]}
        stream={{ events: [], status: "live" }}
        onOpenIssues={onOpenIssues}
      />,
    )

    const top = screen.getByTestId("overview-issue-top")
    expect(top.textContent).toContain("支付回调丢失")
    expect(top.textContent).toContain("退款金额错误")
    expect(top.textContent).toContain("按钮文案错别字")

    fireEvent.click(screen.getByRole("button", { name: "查看全部问题" }))
    expect(onOpenIssues).toHaveBeenCalledTimes(1)
  })

  it("shows the code change summary when a repository is bound", async () => {
    mocks.getLatestAnalysis.mockResolvedValue({
      id: "1844674407370955601",
      versionId: VERSION_ID,
      repoId: 3,
      baseCommit: "0123456789abcdef",
      headCommit: "89abcdef0123456789abcdef0123456789abcdef",
      status: "done",
      degraded: false,
      analysisVersion: 1,
      model: "",
      promptVersion: "",
      changeSetId: null,
      changeSummary: "退款接口新增 reason 参数",
      stats: {
        changedFiles: 3,
        analyzedFiles: 3,
        cacheHits: 0,
        facts: 10,
        edges: 4,
        contracts: 2,
        evidenceChecks: 0,
        evidenceFailures: 0,
        unmapped: 2,
        invalidFacts: 0,
        duplicateFacts: 0,
        unmatchedContracts: 0,
        truncated: false,
      },
      factVersions: [],
      candidates: [],
      unmapped: [
        { path: "src/x.ts", status: "M", reason: "", suggestion: "" },
        { path: "src/y.ts", status: "M", reason: "", suggestion: "" },
      ],
      errors: [],
      createdAt: "2026-10-28T10:00:00Z",
      startedAt: "2026-10-28T10:00:00Z",
      finishedAt: "2026-10-28T10:01:00Z",
    })
    const onOpenCode = vi.fn()

    render(
      <OverviewTab
        version={version({ status: "continuing", continuePhase: "executing" })}
        batches={[LATEST_BATCH]}
        stream={{ events: [], status: "live" }}
        onOpenCode={onOpenCode}
      />,
    )

    expect(await screen.findByTestId("overview-code-summary")).toBeTruthy()
    expect(screen.getByText(/变更文件 3/)).toBeTruthy()
    expect(screen.getByText(/未映射 2/)).toBeTruthy()
    expect(screen.getByText(/受影响用例 1/)).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "查看代码与变更" }))
    expect(onOpenCode).toHaveBeenCalledTimes(1)
  })

  it("renders the empty states without inventing metrics", () => {
    mocks.useIssues.mockImplementation(() => page([]))
    mocks.useReports.mockReturnValue(page([]))
    mocks.useReport.mockReturnValue({
      data: null,
      report: null,
      loading: false,
      error: "",
      streamStatus: "live" as const,
      refresh: vi.fn(),
    })

    render(
      <OverviewTab
        version={version({
          status: "clarifying",
          codeRepositoryId: null,
          bindings: { ...version().bindings, codeRepository: null },
        })}
        batches={[]}
        stream={{ events: [], status: "live" }}
      />,
    )

    expect(screen.getAllByText("暂无批次").length).toBeGreaterThan(0)
    expect(screen.getByText("暂无报告，生成批次报告后展示覆盖矩阵。")).toBeTruthy()
    expect(screen.getByText("暂无未解决问题。")).toBeTruthy()
    expect(screen.getByText("未绑定代码仓库，暂无代码变更摘要。")).toBeTruthy()
    expect(screen.getByText("暂无执行结果")).toBeTruthy()
  })

  it("counts every terminal case state in the 未执行 formula", () => {
    const terminalBatch = batch({
      id: BATCH_2,
      batch_no: 3,
      status: "done",
      counts: {
        total: 7,
        passed: 2,
        failed: 1,
        blocked: 1,
        inconclusive: 1,
        skipped: 1,
        interrupted: 0,
      },
    })

    render(
      <OverviewTab
        version={version()}
        batches={[terminalBatch]}
        stream={{ events: [], status: "live" }}
      />,
    )

    // 7 total - (2+1+1+1+1+0) terminal cases = 1 not executed.
    expect(screen.getByText("通过 2 · 失败 1 · 阻塞 1 · 未执行 1")).toBeTruthy()
  })

  it("shows loading placeholders instead of zero metrics and misleading empties", () => {
    mocks.useIssues.mockImplementation(() => ({ ...page([]), loading: true }))
    mocks.useReports.mockReturnValue({ ...page([]), loading: true })
    mocks.useReport.mockReturnValue({
      data: null,
      report: null,
      loading: false,
      error: "",
      streamStatus: "live" as const,
      refresh: vi.fn(),
    })

    render(
      <OverviewTab
        version={version()}
        batches={[LATEST_BATCH]}
        stream={{ events: [], status: "live" }}
      />,
    )

    expect(screen.getAllByText(/加载中/).length).toBeGreaterThan(0)
    expect(screen.queryByText(/未解决 0/)).toBeNull()
    expect(screen.queryByText("暂无未解决问题。")).toBeNull()
    expect(screen.queryByText("暂无报告，生成批次报告后展示覆盖矩阵。")).toBeNull()
  })

  it("shows retryable alerts for issue and report read failures instead of empties", () => {
    const issuesRefresh = vi.fn().mockResolvedValue(undefined)
    const reportsRefresh = vi.fn().mockResolvedValue(undefined)
    mocks.useIssues.mockImplementation(() => ({
      ...page([]),
      error: "boom",
      refresh: issuesRefresh,
    }))
    mocks.useReports.mockReturnValue({
      ...page([]),
      error: "boom",
      refresh: reportsRefresh,
    })

    render(
      <OverviewTab
        version={version()}
        batches={[LATEST_BATCH]}
        stream={{ events: [], status: "live" }}
      />,
    )

    const alerts = screen.getAllByRole("alert")
    const issueAlert = alerts.find((alert) => alert.textContent?.includes("加载问题失败：boom"))
    const reportAlert = alerts.find((alert) => alert.textContent?.includes("加载报告失败：boom"))
    expect(issueAlert).toBeTruthy()
    expect(reportAlert).toBeTruthy()
    expect(screen.queryByText("暂无未解决问题。")).toBeNull()
    expect(screen.queryByText("暂无报告，生成批次报告后展示覆盖矩阵。")).toBeNull()

    // Both issue pages (open + confirmed) share the same refresh callback.
    fireEvent.click(within(issueAlert as HTMLElement).getByRole("button", { name: "重试" }))
    expect(issuesRefresh).toHaveBeenCalledTimes(2)

    fireEvent.click(within(reportAlert as HTMLElement).getByRole("button", { name: "重试" }))
    expect(reportsRefresh).toHaveBeenCalledTimes(1)
  })

  it("surfaces the version stream reconnect status", () => {
    render(
      <OverviewTab
        version={version()}
        batches={[LATEST_BATCH]}
        stream={{ events: [], status: "reconnecting" }}
      />,
    )

    expect(screen.getByTestId("overview-stream-status").textContent).toContain("重连中")
  })
})
