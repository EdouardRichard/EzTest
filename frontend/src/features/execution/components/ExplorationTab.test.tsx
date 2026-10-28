import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { IssueSummary } from "@/features/delivery/types"
import type { CaseNodeView, MindmapGraph } from "@/features/mindmap/types"
import type { Requirement } from "@/features/versions/types"

import type { CaseRun, EvidenceView } from "../types"

const mocks = vi.hoisted(() => ({
  useCaseRuns: vi.fn(),
  useIssues: vi.fn(),
  listRequirements: vi.fn(),
  getGraph: vi.fn(),
}))

vi.mock("../hooks/useExecution", () => ({
  useCaseRuns: mocks.useCaseRuns,
}))

vi.mock("@/features/delivery/hooks/useIssues", () => ({
  useIssues: mocks.useIssues,
}))

vi.mock("@/features/versions/api", () => ({
  listRequirements: mocks.listRequirements,
}))

vi.mock("@/features/mindmap/api", () => ({
  getGraph: mocks.getGraph,
}))

import { ExplorationTab } from "./ExplorationTab"

const VERSION_ID = "1844674407370955161"
const BATCH_ID = "1844674407370955162"
const OTHER_BATCH_ID = "1844674407370955163"

const R1 = "1844674407370955101"
const R2 = "1844674407370955102"
const C1 = "1844674407370955201"
const C2 = "1844674407370955202"
const C3 = "1844674407370955203"
const CR1 = "1844674407370955301"
const CR2 = "1844674407370955302"
const CR3 = "1844674407370955303"
const ISSUE_1 = "1844674407370955501"
const ISSUE_2 = "1844674407370955502"
const ISSUE_3 = "1844674407370955503"
const EVIDENCE = "1844674407370955401"

function requirement(id: string, title: string, overrides: Partial<Requirement> = {}): Requirement {
  return {
    id,
    versionId: VERSION_ID,
    title,
    description: "",
    acceptanceCriteria: "",
    priority: "must",
    status: "clarified",
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
    createdAt: "2026-10-28T00:00:00Z",
    updatedAt: "2026-10-28T00:00:00Z",
    ...overrides,
  }
}

function caseNode(
  id: string,
  title: string,
  requirementIds: string[],
  overrides: Partial<CaseNodeView> = {},
): CaseNodeView {
  return {
    id,
    parentId: null,
    kind: "case",
    title,
    description: "",
    priority: "P2",
    preconditions: "",
    testData: null,
    source: "user_input",
    status: "executed",
    sortOrder: 0,
    revision: 1,
    steps: [],
    assertions: [],
    sources: requirementIds.map((requirementId, index) => ({
      id: `source-${id}-${index}`,
      refKind: "requirement",
      refId: requirementId,
      refKey: "",
      source: "user_input",
      confidence: "high",
    })),
    anchors: [],
    openBlockerCount: 0,
    ...overrides,
  }
}

function graph(nodes: CaseNodeView[]): MindmapGraph {
  return {
    versionId: VERSION_ID,
    versionStatus: "executing",
    editable: false,
    nodes,
    coverage: {
      requirementIds: [R1, R2],
      uncoveredRequirementIds: [],
      staleRequirementIds: [],
      matrix: [],
    },
    baseline: null,
    latestRuns: {},
    impactCases: [],
  }
}

function evidenceView(id: string): EvidenceView {
  return {
    id,
    kind: "screenshot",
    mime: "image/png",
    size_bytes: 2048,
    sha256: "a".repeat(64),
  }
}

function caseRun(
  overrides: Partial<CaseRun> & Pick<CaseRun, "id" | "case_node_id" | "status">,
): CaseRun {
  const { id, case_node_id, status, ...rest } = overrides
  return {
    id,
    case_node_id,
    status,
    batch_id: BATCH_ID,
    case_revision: 1,
    attempt: 1,
    failure_class: null,
    failure_reason: "",
    summary: "",
    duration_ms: 0,
    title: "",
    session: null,
    checkpoint: { completed_seqs: [], cleanup_required: false, interrupted_step_seq: null },
    steps: [],
    assertions: [],
    evidence: [],
    pending_takeover: null,
    ...rest,
  }
}

function caseRunList(caseRuns: CaseRun[]) {
  return {
    caseRuns,
    nextCursor: "",
    hasMore: false,
    loading: false,
    error: "",
    streamStatus: "live" as const,
    refresh: vi.fn(),
  }
}

function issue(overrides: Partial<IssueSummary> & Pick<IssueSummary, "id">): IssueSummary {
  return {
    versionId: VERSION_ID,
    batchId: BATCH_ID,
    caseRunId: CR2,
    caseNodeId: C2,
    stepResultId: null,
    source: "ai",
    category: "defect",
    severity: "major",
    status: "open",
    confidence: "high",
    failureClass: "application",
    title: "提交订单失败",
    rerunSuggestion: "重跑下单用例",
    recurrenceCount: 1,
    revision: 1,
    refs: [
      { kind: "case_run", id: CR2 },
      { kind: "evidence", id: EVIDENCE },
    ],
    codeRefs: [],
    dismissedReason: "",
    createdBy: "1",
    confirmedBy: null,
    confirmedAt: null,
    dismissedBy: null,
    dismissedAt: null,
    createdAt: "2026-10-28T00:00:00Z",
    updatedAt: "2026-10-28T00:00:00Z",
    ...overrides,
  }
}

function issuesResult(
  items: IssueSummary[],
  overrides: { loading?: boolean; error?: string } = {},
) {
  return {
    items,
    hasMore: false,
    loading: overrides.loading ?? false,
    loadingMore: false,
    error: overrides.error ?? "",
    streamStatus: "live" as const,
    refresh: vi.fn(),
    loadMore: vi.fn(),
  }
}

const REQUIREMENTS = [requirement(R1, "登录需求"), requirement(R2, "下单需求")]

const GRAPH = graph([caseNode(C1, "登录成功", [R1]), caseNode(C2, "提交订单", [R2])])

const PASSED_RUN = caseRun({
  id: CR1,
  case_node_id: C1,
  status: "passed",
  title: "登录成功",
  duration_ms: 800,
})
const FAILED_RUN = caseRun({
  id: CR2,
  case_node_id: C2,
  status: "failed",
  failure_class: "application",
  title: "提交订单",
  summary: "提交订单返回 500",
  duration_ms: 1200,
  evidence: [evidenceView(EVIDENCE)],
})

function renderTab(
  overrides: {
    onOpenEvidence?: (evidenceId: string) => void
    onNavigate?: (target: unknown) => void
  } = {},
) {
  const onOpenEvidence = overrides.onOpenEvidence ?? vi.fn()
  const onNavigate = overrides.onNavigate ?? vi.fn()
  const result = render(
    <ExplorationTab
      versionId={VERSION_ID}
      batchId={BATCH_ID}
      onOpenEvidence={onOpenEvidence}
      onNavigate={onNavigate}
    />,
  )
  return { ...result, onOpenEvidence, onNavigate }
}

beforeEach(() => {
  mocks.useCaseRuns.mockReset()
  mocks.useIssues.mockReset()
  mocks.listRequirements.mockReset()
  mocks.getGraph.mockReset()

  mocks.useCaseRuns.mockReturnValue(caseRunList([PASSED_RUN, FAILED_RUN]))
  mocks.useIssues.mockReturnValue(issuesResult([]))
  mocks.listRequirements.mockResolvedValue({
    items: REQUIREMENTS,
    nextCursor: "",
    hasMore: false,
  })
  mocks.getGraph.mockResolvedValue(GRAPH)
})

afterEach(cleanup)

describe("ExplorationTab", () => {
  it("renders the requirement → case → result layers with the live issue panel", async () => {
    renderTab()

    const requirementNode = await screen.findByTestId(`chain-node-${R1}`)
    expect(requirementNode.getAttribute("role")).toBe("treeitem")
    expect(screen.getByRole("tree")).toBeTruthy()

    const caseNodeElement = screen.getByTestId(`chain-node-${C1}`)
    expect(caseNodeElement.textContent).toContain("登录成功")
    expect(caseNodeElement.textContent).toContain("用例")

    const passed = screen.getByTestId(`chain-node-${CR1}`)
    expect(passed.textContent).toContain("通过")

    const failed = screen.getByTestId(`chain-node-${CR2}`)
    expect(failed.textContent).toContain("失败")

    const issuePanel = screen.getByTestId("chain-issue-panel")
    expect(issuePanel.textContent).toContain("问题")
    expect(screen.getByTestId("chain-issues-empty").textContent).toContain("本批次未发现问题")
    expect(screen.queryByTestId("chain-issue-placeholder")).toBeNull()
  })

  it("keeps the status colour, glyph and text alongside each other", async () => {
    renderTab()

    const passed = await screen.findByTestId(`chain-node-${CR1}`)
    const failed = screen.getByTestId(`chain-node-${CR2}`)

    expect(passed.getAttribute("data-status")).toBe("passed")
    expect(failed.getAttribute("data-status")).toBe("failed")
    expect(passed.getAttribute("aria-label")).toContain("通过")
    expect(failed.getAttribute("aria-label")).toContain("失败")
    expect(passed.textContent).toContain("✓")
    expect(failed.textContent).toContain("✕")
  })

  it("opens a node drawer with summary, evidence and deep-link callbacks", async () => {
    const { onOpenEvidence, onNavigate } = renderTab()

    fireEvent.click(await screen.findByTestId(`chain-node-${CR2}`))

    const drawer = screen.getByTestId("chain-drawer")
    expect(drawer.textContent).toContain("提交订单")
    expect(drawer.textContent).toContain("提交订单返回 500")
    expect(drawer.textContent).toContain("失败")

    fireEvent.click(screen.getByTestId(`chain-drawer-evidence-${EVIDENCE}`))
    expect(onOpenEvidence).toHaveBeenCalledWith(EVIDENCE)

    fireEvent.click(screen.getByTestId("chain-drawer-open-case"))
    expect(onNavigate).toHaveBeenCalledWith({ kind: "case", id: C2 })

    fireEvent.click(screen.getByTestId("chain-drawer-open-result"))
    expect(onNavigate).toHaveBeenCalledWith({ kind: "result", id: CR2 })

    fireEvent.click(screen.getByTestId("chain-drawer-close"))
    expect(screen.queryByTestId("chain-drawer")).toBeNull()
  })

  it("opens a requirement drawer with its deep link", async () => {
    const { onNavigate } = renderTab()

    fireEvent.click(await screen.findByTestId(`chain-node-${R2}`))
    fireEvent.click(screen.getByTestId("chain-drawer-open-requirement"))

    expect(onNavigate).toHaveBeenCalledWith({ kind: "requirement", id: R2 })
  })

  it("renders red diamond issue nodes from the delivery issues API", async () => {
    mocks.useIssues.mockReturnValue(
      issuesResult([
        issue({ id: ISSUE_1, title: "提交订单失败", severity: "blocker", status: "open" }),
      ]),
    )

    renderTab()

    const node = await screen.findByTestId(`chain-issue-node-${ISSUE_1}`)
    expect(node.getAttribute("data-kind")).toBe("issue")
    expect(node.getAttribute("data-severity")).toBe("blocker")
    expect(node.textContent).toContain("提交订单失败")
    expect(node.textContent).toContain("阻断")
    expect(node.getAttribute("aria-label")).toContain("问题")
    expect(node.querySelector("polygon")?.getAttribute("fill")).toBe("#ef4444")
    // IDs stay decimal strings end-to-end (D29), never Number()'d.
    expect(node.getAttribute("data-testid")).toContain(ISSUE_1)
  })

  it("opens the issue drawer with severity, evidence and issue/case deep links", async () => {
    mocks.useIssues.mockReturnValue(
      issuesResult([
        issue({
          id: ISSUE_1,
          title: "提交订单失败",
          severity: "major",
          status: "confirmed",
          confidence: "high",
          category: "defect",
        }),
      ]),
    )
    const { onOpenEvidence, onNavigate } = renderTab()

    fireEvent.click(await screen.findByTestId(`chain-issue-node-${ISSUE_1}`))

    const drawer = screen.getByTestId("chain-drawer")
    expect(drawer.getAttribute("data-kind")).toBe("issue")
    expect(drawer.textContent).toContain("提交订单失败")
    expect(drawer.textContent).toContain("已确认")
    expect(drawer.textContent).toContain("严重")
    expect(drawer.textContent).toContain("高")

    fireEvent.click(screen.getByTestId(`chain-drawer-evidence-${EVIDENCE}`))
    expect(onOpenEvidence).toHaveBeenCalledWith(EVIDENCE)

    fireEvent.click(screen.getByTestId("chain-drawer-open-issue"))
    expect(onNavigate).toHaveBeenCalledWith({ kind: "issue", id: ISSUE_1 })

    fireEvent.click(screen.getByTestId("chain-drawer-open-case"))
    expect(onNavigate).toHaveBeenCalledWith({ kind: "case", id: C2 })
  })

  it("keeps every chain request within the backend's 100-item limit cap", async () => {
    renderTab()

    await screen.findByTestId(`chain-node-${R1}`)
    expect(mocks.listRequirements).toHaveBeenCalledWith(VERSION_ID, { limit: 100 })
    expect(mocks.useIssues).toHaveBeenCalledWith(
      VERSION_ID,
      { batchId: BATCH_ID, limit: 100 },
      undefined,
    )
  })

  it("filters issue nodes between the current batch and all batches", async () => {
    mocks.useIssues.mockImplementation((_versionId: string, filters: { batchId?: string }) =>
      filters.batchId === BATCH_ID
        ? issuesResult([issue({ id: ISSUE_1, caseNodeId: C2, caseRunId: CR2 })])
        : issuesResult([
            issue({ id: ISSUE_1, caseNodeId: C2, caseRunId: CR2 }),
            issue({
              id: ISSUE_2,
              batchId: OTHER_BATCH_ID,
              caseNodeId: C1,
              caseRunId: CR1,
              title: "登录按钮缺失",
            }),
          ]),
    )

    renderTab()

    expect(await screen.findByTestId(`chain-issue-node-${ISSUE_1}`)).toBeTruthy()
    expect(screen.queryByTestId(`chain-issue-node-${ISSUE_2}`)).toBeNull()
    expect(mocks.useIssues).toHaveBeenLastCalledWith(
      VERSION_ID,
      { batchId: BATCH_ID, limit: 100 },
      undefined,
    )

    fireEvent.change(screen.getByTestId("chain-batch-filter"), {
      target: { value: "all" },
    })

    expect(await screen.findByTestId(`chain-issue-node-${ISSUE_2}`)).toBeTruthy()
    expect(screen.getByTestId(`chain-issue-node-${ISSUE_1}`)).toBeTruthy()
    expect(mocks.useIssues).toHaveBeenLastCalledWith(VERSION_ID, { limit: 100 }, undefined)
  })

  it("shows AI-supplemented and needs_revalidation cases with text, not colour alone", async () => {
    const revalidation = caseNode(C2, "购物车金额", [R2], { status: "needs_revalidation" })
    const supplemented = caseNode(C3, "AI 补的边界用例", [R2], {
      source: "ai_supplement",
      status: "supplemented",
    })
    mocks.getGraph.mockResolvedValue(
      graph([caseNode(C1, "登录成功", [R1]), revalidation, supplemented]),
    )
    mocks.useCaseRuns.mockReturnValue(caseRunList([PASSED_RUN, FAILED_RUN]))

    renderTab()

    const aiNode = await screen.findByTestId(`chain-node-${C3}`)
    const aiBadge = screen.getByTestId("chain-supplement-badge")
    expect(aiBadge.textContent).toContain("AI")
    expect(aiNode.querySelector("rect")?.getAttribute("stroke-dasharray")).toBe("6 4")

    const revalidationNode = screen.getByTestId(`chain-node-${C2}`)
    const revalidationBadge = screen.getByTestId("chain-revalidation-badge")
    expect(revalidationBadge.textContent).toContain("需重验")
    expect(revalidationNode.querySelector("rect")?.getAttribute("stroke")).toBe("#f59e0b")
    expect(revalidationNode.getAttribute("data-revalidation")).toBe("true")
  })

  it("lists issues without a case and links them to the issue page", async () => {
    mocks.useIssues.mockReturnValue(
      issuesResult([
        issue({
          id: ISSUE_3,
          title: "缺少导出需求",
          caseNodeId: null,
          caseRunId: null,
          category: "requirement_gap",
        }),
      ]),
    )
    const { onNavigate } = renderTab()

    const list = await screen.findByTestId("chain-unlinked-issues")
    expect(list.textContent).toContain("缺少导出需求")
    fireEvent.click(screen.getByTestId(`chain-unlinked-issue-${ISSUE_3}`))
    expect(onNavigate).toHaveBeenCalledWith({ kind: "issue", id: ISSUE_3 })
    expect(screen.queryByTestId(`chain-issue-node-${ISSUE_3}`)).toBeNull()
  })

  it("degrades gracefully when the issue read fails", async () => {
    mocks.useIssues.mockReturnValue(issuesResult([], { error: "无权限读取问题" }))

    renderTab()

    const degraded = await screen.findByTestId("chain-issues-degraded")
    expect(degraded.textContent).toContain("问题数据加载失败")
    expect(degraded.textContent).toContain("AI 分析降级")
    expect(degraded.textContent).toContain("不完整")
    // The chain itself still renders.
    expect(screen.getByTestId("chain-graph")).toBeTruthy()
    expect(screen.queryByTestId("chain-issues-empty")).toBeNull()
  })

  it("does not report zero issues when the issue request is rejected with a 422", async () => {
    mocks.useIssues.mockReturnValue(
      issuesResult([], { error: "limit must be an integer between 1 and 100" }),
    )

    renderTab()

    const degraded = await screen.findByTestId("chain-issues-degraded")
    expect(degraded.textContent).toContain("limit must be an integer between 1 and 100")
    expect(screen.queryByTestId("chain-issues-empty")).toBeNull()

    const count = screen.getByTestId("chain-issue-count")
    expect(count.textContent).toContain("问题数据不可用")
    expect(count.textContent).not.toContain("0 个问题")
  })

  it("keeps the case/result layers and names the failed requirement layer", async () => {
    mocks.listRequirements.mockRejectedValue(
      new Error("limit must be an integer between 1 and 100"),
    )

    renderTab()

    // The graph request succeeded, so the cases and results still render.
    expect(await screen.findByTestId(`chain-node-${C1}`)).toBeTruthy()
    expect(screen.getByTestId(`chain-node-${CR1}`)).toBeTruthy()

    const degraded = await screen.findByTestId("exploration-degraded")
    expect(degraded.textContent).toContain("需求层")
    expect(degraded.textContent).toContain("limit must be an integer between 1 and 100")
    expect(screen.queryByTestId("exploration-error")).toBeNull()
  })

  it("shows the chain error and the issue layer hint when every request is rejected", async () => {
    const message = "limit must be an integer between 1 and 100"
    mocks.listRequirements.mockRejectedValue(new Error(message))
    mocks.getGraph.mockRejectedValue(new Error(message))
    mocks.useCaseRuns.mockReturnValue(caseRunList([]))
    mocks.useIssues.mockReturnValue(issuesResult([], { error: message }))

    renderTab()

    const error = await screen.findByTestId("exploration-error")
    expect(error.textContent).toContain(message)
    expect(error.textContent).toContain("需求层")
    expect(error.textContent).toContain("用例层")
    expect(screen.queryByTestId("exploration-empty")).toBeNull()
    expect((await screen.findByTestId("chain-issues-degraded")).textContent).toContain(message)
    expect(screen.queryByTestId("chain-issues-empty")).toBeNull()
    expect(screen.getByTestId("chain-issue-count").textContent).toContain("问题数据不可用")
  })

  it("shows the current-batch issue empty state", async () => {
    renderTab()

    expect((await screen.findByTestId("chain-issues-empty")).textContent).toContain(
      "本批次未发现问题",
    )
  })

  it("filters the graph by requirement", async () => {
    renderTab()
    await screen.findByTestId(`chain-node-${R1}`)

    fireEvent.change(screen.getByTestId("exploration-filter-requirement"), {
      target: { value: R1 },
    })

    expect(screen.getByTestId(`chain-node-${R1}`)).toBeTruthy()
    expect(screen.getByTestId(`chain-node-${C1}`)).toBeTruthy()
    expect(screen.queryByTestId(`chain-node-${R2}`)).toBeNull()
    expect(screen.queryByTestId(`chain-node-${C2}`)).toBeNull()
  })

  it("filters by result status and by abnormal-only", async () => {
    renderTab()
    await screen.findByTestId(`chain-node-${CR1}`)

    fireEvent.change(screen.getByTestId("exploration-filter-status"), {
      target: { value: "failed" },
    })
    expect(screen.queryByTestId(`chain-node-${CR1}`)).toBeNull()
    expect(screen.getByTestId(`chain-node-${CR2}`)).toBeTruthy()
    expect(screen.queryByTestId(`chain-node-${C1}`)).toBeNull()

    fireEvent.change(screen.getByTestId("exploration-filter-status"), {
      target: { value: "" },
    })
    fireEvent.click(screen.getByTestId("exploration-filter-abnormal"))

    expect(screen.queryByTestId(`chain-node-${CR1}`)).toBeNull()
    expect(screen.getByTestId(`chain-node-${CR2}`)).toBeTruthy()
  })

  it("collapses and expands a requirement group", async () => {
    renderTab()

    const toggle = await screen.findByTestId(`chain-toggle-${R1}`)
    fireEvent.click(toggle)

    expect(screen.queryByTestId(`chain-node-${C1}`)).toBeNull()
    expect(screen.queryByTestId(`chain-node-${CR1}`)).toBeNull()
    expect(screen.getByTestId(`chain-node-${R1}`).getAttribute("aria-expanded")).toBe("false")

    fireEvent.click(screen.getByTestId(`chain-toggle-${R1}`))
    expect(screen.getByTestId(`chain-node-${C1}`)).toBeTruthy()
    expect(screen.getByTestId(`chain-node-${CR1}`)).toBeTruthy()
  })

  it("zooms and fits the hand-written SVG", async () => {
    renderTab()
    await screen.findByTestId("chain-graph")

    expect(screen.getByTestId("chain-graph").getAttribute("data-zoom")).toBe("1")
    fireEvent.click(screen.getByTestId("chain-zoom-in"))
    expect(screen.getByTestId("chain-graph").getAttribute("data-zoom")).toBe("1.25")
    expect(screen.getByTestId("chain-zoom-level").textContent).toContain("125")

    fireEvent.click(screen.getByTestId("chain-fit"))
    expect(screen.getByTestId("chain-graph").getAttribute("data-zoom")).toBe("1")
  })

  it("shows only the latest attempt of a case in the result layer", async () => {
    const newest = caseRun({
      id: CR3,
      case_node_id: C1,
      status: "passed",
      attempt: 2,
      title: "登录成功",
    })
    mocks.useCaseRuns.mockReturnValue(caseRunList([newest, PASSED_RUN]))

    renderTab()
    await screen.findByTestId(`chain-node-${C1}`)

    expect(screen.getByTestId(`chain-node-${CR3}`).textContent).toContain("通过")
    expect(screen.queryByTestId(`chain-node-${CR1}`)).toBeNull()
  })

  it("shows a filtered empty state when the filters hide every node", async () => {
    renderTab()
    await screen.findByTestId(`chain-node-${CR1}`)

    fireEvent.change(screen.getByTestId("exploration-filter-status"), {
      target: { value: "interrupted" },
    })

    const filtered = await screen.findByTestId("exploration-filtered-empty")
    expect(filtered.textContent).toContain("没有符合筛选条件")
    expect(screen.queryByTestId(`chain-node-${CR1}`)).toBeNull()
  })

  it("shows an empty state when the version has no chain nodes", async () => {
    mocks.listRequirements.mockResolvedValue({ items: [], nextCursor: "", hasMore: false })
    mocks.getGraph.mockResolvedValue(graph([]))
    mocks.useCaseRuns.mockReturnValue(caseRunList([]))

    renderTab()

    const empty = await screen.findByTestId("exploration-empty")
    expect(empty.textContent).toContain("暂无探索链路")
    expect(screen.queryByTestId("chain-drawer")).toBeNull()
    expect(within(screen.getByTestId("exploration-tab")).queryByRole("tree")).toBeNull()
  })
})
