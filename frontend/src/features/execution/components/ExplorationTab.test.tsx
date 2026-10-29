import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { VersionEvent } from "@/features/versions/types"
import type { VersionStreamState } from "@/features/versions/useVersionStream"
import type { SSEStatus } from "@/lib/sse-client"

import type { ChainSnapshot, ChainSnapshotEdge, ChainSnapshotNode } from "../chain/chain-types"
import type { CaseRun, EvidenceView } from "../types"

const mocks = vi.hoisted(() => ({
  useExplorationChain: vi.fn(),
  useCaseRun: vi.fn(),
}))

vi.mock("../chain/use-exploration-chain", () => ({
  useExplorationChain: mocks.useExplorationChain,
}))

vi.mock("../hooks/useExecution", () => ({
  useCaseRun: mocks.useCaseRun,
}))

import { type ExplorationFilters, ExplorationTab } from "./ExplorationTab"

const VERSION_ID = "1844674407370955161"
const BATCH_ID = "1844674407370955162"

const R1 = "1844674407370955101"
const R2 = "1844674407370955102"
const C1 = "1844674407370955201"
const C2 = "1844674407370955202"
const C3 = "1844674407370955203"
const CR1 = "1844674407370955301"
const CR2 = "1844674407370955302"
const ISSUE_1 = "1844674407370955501"
const ISSUE_3 = "1844674407370955503"
const EVIDENCE = "1844674407370955401"

function requirementNode(
  id: string,
  title: string,
  overrides: Partial<ChainSnapshotNode> = {},
): ChainSnapshotNode {
  return {
    id: `requirement:${id}`,
    kind: "requirement",
    title,
    status: "clarified",
    priority: "must",
    uncovered: false,
    caseCount: 1,
    executedCount: 1,
    failedCount: 0,
    openQuestionCount: 0,
    ...overrides,
  }
}

function caseNode(
  id: string,
  title: string,
  requirementIds: string[],
  overrides: Partial<ChainSnapshotNode> = {},
): ChainSnapshotNode {
  return {
    id: `case:${id}`,
    kind: "case",
    title,
    status: "executed",
    source: "user_input",
    revalidation: false,
    priority: "P2",
    requirementIds,
    stepCount: 3,
    assertionCount: 2,
    impacted: false,
    impactReason: "",
    ...overrides,
  }
}

function resultNode(
  id: string,
  caseNodeId: string,
  status: string,
  overrides: Partial<ChainSnapshotNode> = {},
): ChainSnapshotNode {
  return {
    id: `result:${id}`,
    kind: "result",
    title: "",
    status,
    caseRunId: id,
    caseNodeId,
    attempt: 1,
    durationMs: 0,
    failureClass: "",
    evidenceCount: 0,
    batchId: BATCH_ID,
    summary: "",
    ...overrides,
  }
}

function issueNode(
  id: string,
  title: string,
  overrides: Partial<ChainSnapshotNode> = {},
): ChainSnapshotNode {
  return {
    id: `issue:${id}`,
    kind: "issue",
    title,
    status: "open",
    severity: "major",
    category: "defect",
    confidence: "high",
    caseRunId: CR2,
    caseNodeId: C2,
    evidenceCount: 1,
    codeRefCount: 0,
    ...overrides,
  }
}

function requirementCaseEdge(requirementId: string, caseId: string): ChainSnapshotEdge {
  return {
    id: `requirement_case:${requirementId}:${caseId}`,
    source: `requirement:${requirementId}`,
    target: `case:${caseId}`,
    kind: "requirement_case",
  }
}

function caseResultEdge(caseId: string, resultId: string): ChainSnapshotEdge {
  return {
    id: `case_result:${caseId}:${resultId}`,
    source: `case:${caseId}`,
    target: `result:${resultId}`,
    kind: "case_result",
  }
}

function resultIssueEdge(resultId: string, issueId: string): ChainSnapshotEdge {
  return {
    id: `result_issue:${resultId}:${issueId}`,
    source: `result:${resultId}`,
    target: `issue:${issueId}`,
    kind: "result_issue",
  }
}

function countsOf(nodes: ChainSnapshotNode[]): ChainSnapshot["counts"] {
  const byResultStatus: Record<string, number> = {
    pending: 0,
    running: 0,
    passed: 0,
    failed: 0,
    blocked: 0,
    inconclusive: 0,
    skipped: 0,
    interrupted: 0,
  }
  const byIssueSeverity: Record<string, number> = {
    blocker: 0,
    major: 0,
    minor: 0,
    info: 0,
  }
  for (const node of nodes) {
    if (node.kind === "result" && node.status in byResultStatus) {
      byResultStatus[node.status] = (byResultStatus[node.status] ?? 0) + 1
    }
    if (node.kind === "issue" && node.severity !== undefined) {
      byIssueSeverity[node.severity] = (byIssueSeverity[node.severity] ?? 0) + 1
    }
  }
  return {
    requirements: nodes.filter((node) => node.kind === "requirement").length,
    cases: nodes.filter((node) => node.kind === "case").length,
    results: nodes.filter((node) => node.kind === "result").length,
    issues: nodes.filter((node) => node.kind === "issue").length,
    byResultStatus,
    byIssueSeverity,
    uncoveredRequirements: 0,
    revalidationCases: 0,
    supplementCases: 0,
  }
}

function snapshotOf(
  nodes: ChainSnapshotNode[],
  edges: ChainSnapshotEdge[] = [],
  overrides: Partial<ChainSnapshot> = {},
): ChainSnapshot {
  return {
    versionId: VERSION_ID,
    batch: { id: BATCH_ID, batchNo: 2, kind: "incremental", status: "done" },
    nodes,
    edges,
    counts: countsOf(nodes),
    truncated: { requirements: false, cases: false, results: false, issues: false },
    generatedAt: "2026-10-29T10:00:00Z",
    ...overrides,
  }
}

const REQUIREMENT_NODES = [requirementNode(R1, "登录需求"), requirementNode(R2, "下单需求")]
const CASE_NODES = [caseNode(C1, "登录成功", [R1]), caseNode(C2, "提交订单", [R2])]
const RESULT_NODES = [
  resultNode(CR1, C1, "passed", { title: "登录成功", durationMs: 800 }),
  resultNode(CR2, C2, "failed", {
    title: "提交订单",
    attempt: 2,
    durationMs: 1200,
    failureClass: "application",
    evidenceCount: 1,
    summary: "提交订单返回 500",
  }),
]
const ISSUE_NODES = [issueNode(ISSUE_1, "提交订单失败", { severity: "major" })]

const SNAPSHOT = snapshotOf(
  [...REQUIREMENT_NODES, ...CASE_NODES, ...RESULT_NODES, ...ISSUE_NODES],
  [
    requirementCaseEdge(R1, C1),
    requirementCaseEdge(R2, C2),
    caseResultEdge(C1, CR1),
    caseResultEdge(C2, CR2),
    resultIssueEdge(CR2, ISSUE_1),
  ],
)

const EMPTY_SNAPSHOT = snapshotOf([])

const LOGIN_ONLY_SNAPSHOT = snapshotOf(
  [
    requirementNode(R1, "登录需求"),
    caseNode(C1, "登录成功", [R1]),
    resultNode(CR1, C1, "passed", { title: "登录成功" }),
  ],
  [requirementCaseEdge(R1, C1), caseResultEdge(C1, CR1)],
)

const FAILED_ONLY_SNAPSHOT = snapshotOf(
  [
    requirementNode(R2, "下单需求"),
    caseNode(C2, "提交订单", [R2]),
    resultNode(CR2, C2, "failed", {
      title: "提交订单",
      attempt: 2,
      failureClass: "application",
      evidenceCount: 1,
      summary: "提交订单返回 500",
    }),
    issueNode(ISSUE_1, "提交订单失败"),
  ],
  [requirementCaseEdge(R2, C2), caseResultEdge(C2, CR2), resultIssueEdge(CR2, ISSUE_1)],
)

function stream(events: VersionEvent[] = [], status: SSEStatus = "live"): VersionStreamState {
  return { events, status, reconnect: vi.fn() }
}

const STREAM = stream()

const EVIDENCE_VIEW: EvidenceView = {
  id: EVIDENCE,
  kind: "screenshot",
  mime: "image/png",
  size_bytes: 2048,
  sha256: "a".repeat(64),
}

function caseRun(overrides: Partial<CaseRun> = {}): CaseRun {
  return {
    id: CR2,
    case_node_id: C2,
    status: "failed",
    batch_id: BATCH_ID,
    case_revision: 1,
    attempt: 2,
    failure_class: "application",
    failure_reason: "",
    summary: "提交订单返回 500",
    duration_ms: 1200,
    title: "提交订单",
    session: null,
    checkpoint: { completed_seqs: [], cleanup_required: false, interrupted_step_seq: null },
    steps: [],
    assertions: [],
    evidence: [EVIDENCE_VIEW],
    pending_takeover: null,
    ...overrides,
  }
}

function chainResult(
  data: ChainSnapshot | null,
  overrides: { loading?: boolean; error?: string; streamStatus?: SSEStatus } = {},
) {
  return {
    data,
    loading: overrides.loading ?? false,
    error: overrides.error ?? "",
    streamStatus: overrides.streamStatus ?? ("live" as SSEStatus),
    refresh: vi.fn().mockResolvedValue(undefined),
  }
}

type CaseRunResource = {
  caseRun: CaseRun | null
  loading: boolean
  error: string
  streamStatus: SSEStatus
  refresh: () => Promise<void>
}

function caseRunResult(
  detail: CaseRun | null,
  overrides: { loading?: boolean; error?: string } = {},
): CaseRunResource {
  return {
    caseRun: detail,
    loading: overrides.loading ?? false,
    error: overrides.error ?? "",
    streamStatus: "live",
    refresh: vi.fn(),
  }
}

type RenderOptions = {
  stream?: VersionStreamState
  initialFilters?: Partial<ExplorationFilters>
  selectedNodeId?: string
  onSelectedNodeChange?: (nodeId: string) => void
  onFiltersChange?: (filters: ExplorationFilters) => void
}

function renderTab(overrides: RenderOptions = {}) {
  const onOpenEvidence = vi.fn()
  const onNavigate = vi.fn()
  const onSelectedNodeChange = overrides.onSelectedNodeChange ?? vi.fn()
  const onFiltersChange = overrides.onFiltersChange ?? vi.fn()
  const result = render(
    <ExplorationTab
      versionId={VERSION_ID}
      batchId={BATCH_ID}
      stream={overrides.stream ?? STREAM}
      onOpenEvidence={onOpenEvidence}
      onNavigate={onNavigate}
      initialFilters={overrides.initialFilters}
      selectedNodeId={overrides.selectedNodeId}
      onSelectedNodeChange={onSelectedNodeChange}
      onFiltersChange={onFiltersChange}
    />,
  )
  return { ...result, onOpenEvidence, onNavigate, onSelectedNodeChange, onFiltersChange }
}

beforeEach(() => {
  mocks.useExplorationChain.mockReset()
  mocks.useCaseRun.mockReset()
  mocks.useExplorationChain.mockReturnValue(chainResult(SNAPSHOT))
  mocks.useCaseRun.mockReturnValue(caseRunResult(null))
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

describe("ExplorationTab", () => {
  it("renders the requirement → case → result layers from the read model", async () => {
    renderTab()

    const requirementNode = await screen.findByTestId(`chain-node-${R1}`)
    expect(requirementNode.getAttribute("role")).toBe("treeitem")
    expect(screen.getByRole("tree")).toBeTruthy()

    const caseNodeElement = screen.getByTestId(`chain-node-${C1}`)
    expect(caseNodeElement.textContent).toContain("登录成功")
    expect(caseNodeElement.textContent).toContain("用例")

    expect(screen.getByTestId(`chain-node-${CR1}`).textContent).toContain("通过")
    expect(screen.getByTestId(`chain-node-${CR2}`).textContent).toContain("失败")

    const issuePanel = screen.getByTestId("chain-issue-panel")
    expect(issuePanel.textContent).toContain("问题")
    expect(screen.getByTestId(`chain-issue-node-${ISSUE_1}`).textContent).toContain("提交订单失败")
    expect(screen.getByTestId("chain-issue-count").textContent).toContain("1 个问题")
    expect(screen.queryByTestId("chain-issue-placeholder")).toBeNull()
  })

  it("builds requirement edges from the read-model edges, not case requirementIds", async () => {
    // The case DTO lists R1 and R2, but only the R1 edge exists. R2 is a real
    // node of the response, so the negative assertion proves the missing edge
    // was not invented from `case.requirementIds`.
    mocks.useExplorationChain.mockReturnValue(
      chainResult(
        snapshotOf(
          [
            requirementNode(R1, "登录需求"),
            requirementNode(R2, "下单需求"),
            caseNode(C1, "登录成功", [R1, R2]),
            resultNode(CR1, C1, "passed", { title: "登录成功" }),
          ],
          [requirementCaseEdge(R1, C1), caseResultEdge(C1, CR1)],
        ),
      ),
    )

    renderTab()
    await screen.findByTestId(`chain-node-${C1}`)

    expect(screen.getByTestId(`chain-node-${R2}`)).toBeTruthy()
    expect(screen.queryByTestId(`chain-edge-${R1}->${C1}`)).toBeTruthy()
    expect(screen.queryByTestId(`chain-edge-${R2}->${C1}`)).toBeNull()
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

  it("opens a result drawer with attempt, summary and caseRunId-lazy evidence", async () => {
    mocks.useCaseRun.mockImplementation(
      (_versionId: string, _batchId: string, caseRunId: string) =>
        caseRunId === CR2 ? caseRunResult(caseRun()) : caseRunResult(null),
    )
    const { onOpenEvidence, onNavigate } = renderTab()

    fireEvent.click(await screen.findByTestId(`chain-node-${CR2}`))

    const drawer = screen.getByTestId("chain-drawer")
    expect(drawer.textContent).toContain("提交订单")
    expect(drawer.textContent).toContain("提交订单返回 500")
    expect(drawer.textContent).toContain("失败")
    expect(drawer.textContent).toContain("第 2 次尝试")

    // Evidence is not loaded with the chain: the hook is called disabled first.
    expect(mocks.useCaseRun).toHaveBeenLastCalledWith(VERSION_ID, BATCH_ID, "", STREAM)
    expect(screen.getByTestId("chain-drawer-load-evidence").textContent).toContain("证据")

    fireEvent.click(screen.getByTestId("chain-drawer-load-evidence"))
    expect(mocks.useCaseRun).toHaveBeenLastCalledWith(VERSION_ID, BATCH_ID, CR2, STREAM)

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

  it("renders red diamond issue nodes from the read model", async () => {
    mocks.useExplorationChain.mockReturnValue(
      chainResult(
        snapshotOf(
          [
            requirementNode(R2, "下单需求"),
            caseNode(C2, "提交订单", [R2]),
            resultNode(CR2, C2, "failed", { title: "提交订单" }),
            issueNode(ISSUE_1, "提交订单失败", { severity: "blocker" }),
          ],
          [requirementCaseEdge(R2, C2), caseResultEdge(C2, CR2), resultIssueEdge(CR2, ISSUE_1)],
        ),
      ),
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

  it("opens the issue drawer with severity, lazy evidence and deep links", async () => {
    mocks.useCaseRun.mockImplementation(
      (_versionId: string, _batchId: string, caseRunId: string) =>
        caseRunId === CR2 ? caseRunResult(caseRun()) : caseRunResult(null),
    )
    const { onOpenEvidence, onNavigate } = renderTab()

    fireEvent.click(await screen.findByTestId(`chain-issue-node-${ISSUE_1}`))

    const drawer = screen.getByTestId("chain-drawer")
    expect(drawer.getAttribute("data-kind")).toBe("issue")
    expect(drawer.textContent).toContain("提交订单失败")
    expect(drawer.textContent).toContain("待确认")
    expect(drawer.textContent).toContain("严重")
    expect(drawer.textContent).toContain("高")

    fireEvent.click(screen.getByTestId("chain-drawer-load-evidence"))
    fireEvent.click(screen.getByTestId(`chain-drawer-evidence-${EVIDENCE}`))
    expect(onOpenEvidence).toHaveBeenCalledWith(EVIDENCE)

    fireEvent.click(screen.getByTestId("chain-drawer-open-issue"))
    expect(onNavigate).toHaveBeenCalledWith({ kind: "issue", id: ISSUE_1 })

    fireEvent.click(screen.getByTestId("chain-drawer-open-case"))
    expect(onNavigate).toHaveBeenCalledWith({ kind: "case", id: C2 })
  })

  it("does not offer a dead-end evidence button for an issue without a caseRunId", async () => {
    mocks.useExplorationChain.mockReturnValue(
      chainResult(
        snapshotOf(
          [
            requirementNode(R2, "下单需求"),
            caseNode(C2, "提交订单", [R2]),
            resultNode(CR2, C2, "failed", { title: "提交订单" }),
            issueNode(ISSUE_1, "提交订单失败", { caseRunId: "", evidenceCount: 1 }),
          ],
          [requirementCaseEdge(R2, C2), caseResultEdge(C2, CR2), resultIssueEdge(CR2, ISSUE_1)],
        ),
      ),
    )
    const { onNavigate } = renderTab()

    fireEvent.click(await screen.findByTestId(`chain-issue-node-${ISSUE_1}`))

    // The evidence read is keyed by caseRunId; without one the old button could
    // only ever resolve to "暂无可查看证据". Point at the issue page instead.
    expect(screen.queryByTestId("chain-drawer-load-evidence")).toBeNull()
    const hint = screen.getByTestId("chain-drawer-evidence-unavailable")
    expect(hint.textContent).toContain("无法加载证据")
    expect(hint.textContent).toContain("查看问题")
    expect(mocks.useCaseRun).toHaveBeenLastCalledWith(VERSION_ID, expect.anything(), "", STREAM)

    fireEvent.click(screen.getByTestId("chain-drawer-open-issue"))
    expect(onNavigate).toHaveBeenCalledWith({ kind: "issue", id: ISSUE_1 })
  })

  it("requests the chain with the batch scope and the read-model defaults", async () => {
    renderTab()

    await screen.findByTestId(`chain-node-${R1}`)
    expect(mocks.useExplorationChain).toHaveBeenCalledWith(
      VERSION_ID,
      expect.objectContaining({ batchId: BATCH_ID }),
      STREAM,
    )
  })

  it("maps the batch filter to the server-side batchId query", async () => {
    mocks.useExplorationChain.mockImplementation(
      (_versionId: string, params: { batchId?: string }) =>
        chainResult(params.batchId === "all" ? SNAPSHOT : SNAPSHOT),
    )
    renderTab()

    expect(await screen.findByTestId(`chain-issue-node-${ISSUE_1}`)).toBeTruthy()
    fireEvent.change(screen.getByTestId("chain-batch-filter"), { target: { value: "all" } })

    expect(mocks.useExplorationChain).toHaveBeenLastCalledWith(
      VERSION_ID,
      expect.objectContaining({ batchId: "all" }),
      STREAM,
    )
  })

  it("maps every filter control to the read-model query", async () => {
    renderTab()
    await screen.findByTestId(`chain-node-${R1}`)

    fireEvent.change(screen.getByTestId("exploration-filter-requirement"), {
      target: { value: R1 },
    })
    expect(mocks.useExplorationChain).toHaveBeenLastCalledWith(
      VERSION_ID,
      expect.objectContaining({ requirementId: R1 }),
      STREAM,
    )

    fireEvent.change(screen.getByTestId("exploration-filter-status"), {
      target: { value: "failed" },
    })
    expect(mocks.useExplorationChain).toHaveBeenLastCalledWith(
      VERSION_ID,
      expect.objectContaining({ resultStatus: "failed" }),
      STREAM,
    )

    fireEvent.click(screen.getByTestId("exploration-filter-abnormal"))
    expect(mocks.useExplorationChain).toHaveBeenLastCalledWith(
      VERSION_ID,
      expect.objectContaining({ abnormalOnly: true }),
      STREAM,
    )

    fireEvent.change(screen.getByTestId("exploration-filter-issue-severity"), {
      target: { value: "major" },
    })
    expect(mocks.useExplorationChain).toHaveBeenLastCalledWith(
      VERSION_ID,
      expect.objectContaining({ issueSeverity: "major" }),
      STREAM,
    )

    fireEvent.change(screen.getByTestId("exploration-filter-issue-status"), {
      target: { value: "open" },
    })
    expect(mocks.useExplorationChain).toHaveBeenLastCalledWith(
      VERSION_ID,
      expect.objectContaining({ issueStatus: "open" }),
      STREAM,
    )

    fireEvent.click(screen.getByTestId("exploration-filter-supplement"))
    expect(mocks.useExplorationChain).toHaveBeenLastCalledWith(
      VERSION_ID,
      expect.objectContaining({ includeSupplement: false }),
      STREAM,
    )

    fireEvent.click(screen.getByTestId("exploration-filter-revalidation"))
    expect(mocks.useExplorationChain).toHaveBeenLastCalledWith(
      VERSION_ID,
      expect.objectContaining({ revalidationOnly: true }),
      STREAM,
    )
  })

  it("debounces the title search by 300ms before querying the server", async () => {
    vi.useFakeTimers()
    renderTab()

    fireEvent.change(screen.getByTestId("exploration-filter-search"), {
      target: { value: "登录" },
    })
    act(() => {
      vi.advanceTimersByTime(299)
    })
    expect(mocks.useExplorationChain).not.toHaveBeenLastCalledWith(
      VERSION_ID,
      expect.objectContaining({ q: "登录" }),
      STREAM,
    )

    act(() => {
      vi.advanceTimersByTime(1)
    })
    expect(mocks.useExplorationChain).toHaveBeenLastCalledWith(
      VERSION_ID,
      expect.objectContaining({ q: "登录" }),
      STREAM,
    )
  })

  it("forwards the initial deep-link filters to the first request", async () => {
    renderTab({
      initialFilters: {
        requirementId: R1,
        resultStatus: "failed",
        abnormalOnly: true,
        q: "登录",
      },
    })

    expect(mocks.useExplorationChain).toHaveBeenCalledWith(
      VERSION_ID,
      expect.objectContaining({
        requirementId: R1,
        resultStatus: "failed",
        abnormalOnly: true,
        q: "登录",
      }),
      STREAM,
    )
  })

  it("shows a truncation banner with the real pre-truncation counts", async () => {
    mocks.useExplorationChain.mockReturnValue(
      chainResult(
        snapshotOf(
          [
            requirementNode(R1, "登录需求"),
            caseNode(C1, "登录成功", [R1]),
            resultNode(CR1, C1, "passed", { title: "登录成功" }),
          ],
          [requirementCaseEdge(R1, C1), caseResultEdge(C1, CR1)],
          {
            counts: {
              requirements: 4,
              cases: 42,
              results: 43,
              issues: 2,
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
            truncated: { requirements: false, cases: true, results: true, issues: false },
          },
        ),
      ),
    )

    renderTab()

    const banner = await screen.findByTestId("chain-truncated")
    expect(banner.textContent).toContain("已显示前 3 条")
    expect(banner.textContent).toContain("共 91 条")
    expect(banner.textContent).toContain("请收窄筛选")
  })

  it("shows the stream status as 重连中 while the version stream reconnects", async () => {
    mocks.useExplorationChain.mockReturnValue(
      chainResult(SNAPSHOT, { streamStatus: "reconnecting" }),
    )

    renderTab()

    expect((await screen.findByTestId("chain-stream-status")).textContent).toContain("重连中")
  })

  it("shows AI-supplemented and needs_revalidation cases with text, not colour alone", async () => {
    mocks.useExplorationChain.mockReturnValue(
      chainResult(
        snapshotOf(
          [
            requirementNode(R2, "下单需求"),
            caseNode(C1, "登录成功", [R2]),
            caseNode(C2, "购物车金额", [R2], { status: "needs_revalidation", revalidation: true }),
            caseNode(C3, "AI 补的边界用例", [R2], { source: "ai_supplement" }),
          ],
          [requirementCaseEdge(R2, C1), requirementCaseEdge(R2, C2), requirementCaseEdge(R2, C3)],
        ),
      ),
    )

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

  it("lists issues without a case edge and links them to the issue page", async () => {
    mocks.useExplorationChain.mockReturnValue(
      chainResult(
        snapshotOf(
          [
            requirementNode(R1, "登录需求"),
            caseNode(C1, "登录成功", [R1]),
            issueNode(ISSUE_3, "缺少导出需求", {
              caseNodeId: C1,
              caseRunId: undefined,
              category: "requirement_gap",
            }),
          ],
          [requirementCaseEdge(R1, C1)],
        ),
      ),
    )
    const { onNavigate } = renderTab()

    const list = await screen.findByTestId("chain-unlinked-issues")
    expect(list.textContent).toContain("缺少导出需求")
    fireEvent.click(screen.getByTestId(`chain-unlinked-issue-${ISSUE_3}`))
    expect(onNavigate).toHaveBeenCalledWith({ kind: "issue", id: ISSUE_3 })
    expect(screen.queryByTestId(`chain-issue-node-${ISSUE_3}`)).toBeNull()
  })

  it("keeps the old data and marks the issue layer unavailable when a refresh fails", async () => {
    mocks.useExplorationChain.mockReturnValue(chainResult(SNAPSHOT, { error: "无权限读取问题" }))

    renderTab()

    const degraded = await screen.findByTestId("exploration-degraded")
    expect(degraded.textContent).toContain("无权限读取问题")
    // The chain itself still renders.
    expect(screen.getByTestId("chain-graph")).toBeTruthy()
    const issueDegraded = screen.getByTestId("chain-issues-degraded")
    expect(issueDegraded.textContent).toContain("无权限读取问题")
    expect(screen.getByTestId("chain-issue-count").textContent).toContain("问题数据不可用")
    expect(screen.queryByTestId("chain-issues-empty")).toBeNull()
  })

  it("shows the chain error and the issue layer hint when the chain read fails", async () => {
    const message = "limit must be an integer between 1 and 2000"
    mocks.useExplorationChain.mockReturnValue(chainResult(null, { error: message }))

    renderTab()

    const error = await screen.findByTestId("exploration-error")
    expect(error.textContent).toContain(message)
    expect(screen.queryByTestId("exploration-empty")).toBeNull()
    expect((await screen.findByTestId("chain-issues-degraded")).textContent).toContain(message)
    expect(screen.queryByTestId("chain-issues-empty")).toBeNull()
    expect(screen.getByTestId("chain-issue-count").textContent).toContain("问题数据不可用")
  })

  it("shows the current-batch issue empty state only when the read model has no issues", async () => {
    mocks.useExplorationChain.mockReturnValue(
      chainResult(
        snapshotOf(
          [requirementNode(R1, "登录需求"), caseNode(C1, "登录成功", [R1])],
          [requirementCaseEdge(R1, C1)],
        ),
      ),
    )

    renderTab()

    expect((await screen.findByTestId("chain-issues-empty")).textContent).toContain(
      "本批次未发现问题",
    )
  })

  it("narrows the graph through the server when the requirement filter changes", async () => {
    mocks.useExplorationChain.mockImplementation(
      (_versionId: string, params: { requirementId?: string }) =>
        chainResult(params.requirementId === R1 ? LOGIN_ONLY_SNAPSHOT : SNAPSHOT),
    )
    renderTab()
    await screen.findByTestId(`chain-node-${R1}`)

    fireEvent.change(screen.getByTestId("exploration-filter-requirement"), {
      target: { value: R1 },
    })

    expect(screen.getByTestId(`chain-node-${R1}`)).toBeTruthy()
    expect(screen.queryByTestId(`chain-node-${R2}`)).toBeNull()
    expect(screen.queryByTestId(`chain-node-${C2}`)).toBeNull()
  })

  it("filters by result status and by abnormal-only through the server", async () => {
    mocks.useExplorationChain.mockImplementation(
      (_versionId: string, params: { resultStatus?: string; abnormalOnly?: boolean }) =>
        chainResult(
          params.resultStatus === "failed" || params.abnormalOnly ? FAILED_ONLY_SNAPSHOT : SNAPSHOT,
        ),
    )
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

  it("hides the result node when the server returns no result for the case", async () => {
    mocks.useExplorationChain.mockReturnValue(
      chainResult(
        snapshotOf(
          [requirementNode(R1, "登录需求"), caseNode(C1, "登录成功", [R1])],
          [requirementCaseEdge(R1, C1)],
        ),
      ),
    )

    renderTab()
    await screen.findByTestId(`chain-node-${C1}`)

    expect(screen.queryByTestId(`chain-node-${CR1}`)).toBeNull()
  })

  it("shows a filtered empty state when the server returns no match", async () => {
    mocks.useExplorationChain.mockImplementation(
      (_versionId: string, params: { resultStatus?: string }) =>
        chainResult(params.resultStatus === "interrupted" ? EMPTY_SNAPSHOT : SNAPSHOT),
    )
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
    mocks.useExplorationChain.mockReturnValue(chainResult(EMPTY_SNAPSHOT))

    renderTab()

    const empty = await screen.findByTestId("exploration-empty")
    expect(empty.textContent).toContain("暂无探索链路")
    expect(screen.queryByTestId("chain-drawer")).toBeNull()
    expect(within(screen.getByTestId("exploration-tab")).queryByRole("tree")).toBeNull()
  })

  it("opens the Sheet from the deep-linked node and reports selection changes", async () => {
    const { onSelectedNodeChange } = renderTab({ selectedNodeId: `case:${C2}` })

    const drawer = await screen.findByTestId("chain-drawer")
    expect(drawer.getAttribute("data-kind")).toBe("case")
    expect(drawer.textContent).toContain("提交订单")

    fireEvent.click(screen.getByTestId("chain-drawer-close"))
    expect(onSelectedNodeChange).toHaveBeenLastCalledWith("")

    fireEvent.click(screen.getByTestId(`chain-node-${C1}`))
    expect(onSelectedNodeChange).toHaveBeenLastCalledWith(`case:${C1}`)
  })

  it("shows 节点不存在 for a stale deep-linked node instead of crashing", async () => {
    renderTab({ selectedNodeId: `case:${"9".repeat(18)}` })

    await screen.findByTestId("chain-graph")
    const missing = screen.findByTestId("chain-node-missing")
    expect((await missing).textContent).toContain("节点不存在")
    expect(screen.queryByTestId("chain-drawer")).toBeNull()
  })

  it("reports filter changes so the page can sync the URL", async () => {
    const onFiltersChange = vi.fn()
    renderTab({ onFiltersChange })
    await screen.findByTestId(`chain-node-${R1}`)

    fireEvent.change(screen.getByTestId("exploration-filter-status"), {
      target: { value: "failed" },
    })
    expect(onFiltersChange).toHaveBeenLastCalledWith({
      requirementId: "",
      resultStatus: "failed",
      abnormalOnly: false,
      q: "",
    })

    fireEvent.change(screen.getByTestId("exploration-filter-requirement"), {
      target: { value: R2 },
    })
    fireEvent.click(screen.getByTestId("exploration-filter-abnormal"))
    expect(onFiltersChange).toHaveBeenLastCalledWith({
      requirementId: R2,
      resultStatus: "failed",
      abnormalOnly: true,
      q: "",
    })
  })

  it("does not report zero issues when the issue layer is unavailable", async () => {
    mocks.useExplorationChain.mockReturnValue(
      chainResult(SNAPSHOT, { error: "limit must be an integer between 1 and 2000" }),
    )

    renderTab()

    await screen.findByTestId("chain-issues-degraded")
    const count = screen.getByTestId("chain-issue-count")
    expect(count.textContent).toContain("问题数据不可用")
    expect(count.textContent).not.toContain("0 个问题")
  })
})
