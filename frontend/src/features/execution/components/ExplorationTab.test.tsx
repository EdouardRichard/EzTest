import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { CaseNodeView, MindmapGraph } from "@/features/mindmap/types"
import type { Requirement } from "@/features/versions/types"

import type { CaseRun, EvidenceView } from "../types"

const mocks = vi.hoisted(() => ({
  useCaseRuns: vi.fn(),
  listRequirements: vi.fn(),
  getGraph: vi.fn(),
}))

vi.mock("../hooks/useExecution", () => ({
  useCaseRuns: mocks.useCaseRuns,
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

const R1 = "1844674407370955101"
const R2 = "1844674407370955102"
const C1 = "1844674407370955201"
const C2 = "1844674407370955202"
const CR1 = "1844674407370955301"
const CR2 = "1844674407370955302"
const CR3 = "1844674407370955303"
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
  mocks.listRequirements.mockReset()
  mocks.getGraph.mockReset()

  mocks.useCaseRuns.mockReturnValue(caseRunList([PASSED_RUN, FAILED_RUN]))
  mocks.listRequirements.mockResolvedValue({
    items: REQUIREMENTS,
    nextCursor: "",
    hasMore: false,
  })
  mocks.getGraph.mockResolvedValue(GRAPH)
})

afterEach(cleanup)

describe("ExplorationTab", () => {
  it("renders the requirement → case → result layers with the P7 issue placeholder", async () => {
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

    const issuePlaceholder = screen.getByTestId("chain-issue-placeholder")
    expect(issuePlaceholder.textContent).toContain("P7")
    expect(issuePlaceholder.textContent).toContain("问题")
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
