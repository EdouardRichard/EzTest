import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import axe from "axe-core"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { VersionStreamState } from "@/features/versions/useVersionStream"

import { ExplorationTab } from "../components/ExplorationTab"
import type { ChainSnapshot, ChainSnapshotNode } from "./chain-types"

/**
 * P8 Task 13 accessibility gate of the exploration chain (spec §5.7/§5.9):
 *
 * - axe must report no serious/critical violation on the real tab with a
 *   200-node read model;
 * - the real React Flow canvas + hidden WAI-ARIA tree render all 200 nodes;
 * - the complete keyboard flow works: arrow navigation, Home/End, collapse and
 *   expand with real count changes, Enter opens the node Sheet and Escape
 *   closes it (focus is trapped and returned by the real Sheet).
 *
 * Only the network boundaries are mocked (`useExplorationChain`, `useCaseRun`);
 * the tab, canvas, layout, keyboard tree, Sheet, toolbar and legend are real.
 */

const mocks = vi.hoisted(() => ({
  useExplorationChain: vi.fn(),
  useCaseRun: vi.fn(),
}))

vi.mock("./use-exploration-chain", () => ({
  useExplorationChain: mocks.useExplorationChain,
}))

vi.mock("../hooks/useExecution", () => ({
  useCaseRun: mocks.useCaseRun,
}))

const VERSION_ID = "7000000000000"
const BATCH_ID = "7010000000000"
const REQUIREMENT_COUNT = 40
const NODE_COUNT = REQUIREMENT_COUNT * 5

const STREAM = {
  events: [],
  status: "live",
  reconnect: vi.fn(),
} as unknown as VersionStreamState

function requirementId(index: number): string {
  return String(7_000_000_000_000 + index)
}

function caseId(index: number): string {
  return String(7_100_000_000_000 + index)
}

function passedResultId(index: number): string {
  return String(7_200_000_000_000 + index * 2 - 1)
}

function failedResultId(index: number): string {
  return String(7_200_000_000_000 + index * 2)
}

function issueId(index: number): string {
  return String(7_300_000_000_000 + index)
}

/** A complete 200-node snapshot: 40 requirements × (1 case + 2 results + 1 issue). */
function chainSnapshot200(): ChainSnapshot {
  const nodes: ChainSnapshotNode[] = []
  for (let index = 1; index <= REQUIREMENT_COUNT; index += 1) {
    const requirementNodeId = `requirement:${requirementId(index)}`
    const caseNodeId = `case:${caseId(index)}`
    const passedNodeId = `result:${passedResultId(index)}`
    const failedNodeId = `result:${failedResultId(index)}`
    nodes.push({
      id: requirementNodeId,
      kind: "requirement",
      title: `需求 ${index}`,
      status: "clarified",
      priority: "must",
      uncovered: false,
      caseCount: 1,
      executedCount: 1,
      failedCount: 1,
      openQuestionCount: 0,
    })
    nodes.push({
      id: caseNodeId,
      kind: "case",
      title: `用例 ${index}`,
      status: "executed",
      source: "user_input",
      revalidation: false,
      priority: "P1",
      requirementIds: [requirementId(index)],
      stepCount: 2,
      assertionCount: 1,
      impacted: false,
      impactReason: "",
    })
    nodes.push({
      id: passedNodeId,
      kind: "result",
      title: `用例 ${index}`,
      status: "passed",
      caseRunId: passedResultId(index),
      caseNodeId: caseId(index),
      attempt: 1,
      durationMs: 800,
      failureClass: "",
      evidenceCount: 0,
      batchId: BATCH_ID,
      summary: "",
    })
    nodes.push({
      id: failedNodeId,
      kind: "result",
      title: `用例 ${index}`,
      status: "failed",
      caseRunId: failedResultId(index),
      caseNodeId: caseId(index),
      attempt: 1,
      durationMs: 1200,
      failureClass: "application",
      evidenceCount: 1,
      batchId: BATCH_ID,
      summary: "提交订单返回 500",
    })
    nodes.push({
      id: `issue:${issueId(index)}`,
      kind: "issue",
      title: `问题 ${index}`,
      status: "open",
      severity: "major",
      category: "defect",
      confidence: "high",
      caseRunId: failedResultId(index),
      caseNodeId: caseId(index),
      evidenceCount: 1,
      codeRefCount: 1,
    })
  }

  const edges: ChainSnapshot["edges"] = []
  for (let index = 1; index <= REQUIREMENT_COUNT; index += 1) {
    edges.push({
      id: `requirement_case:${requirementId(index)}:${caseId(index)}`,
      source: `requirement:${requirementId(index)}`,
      target: `case:${caseId(index)}`,
      kind: "requirement_case",
    })
    edges.push({
      id: `case_result:${caseId(index)}:${passedResultId(index)}`,
      source: `case:${caseId(index)}`,
      target: `result:${passedResultId(index)}`,
      kind: "case_result",
    })
    edges.push({
      id: `case_result:${caseId(index)}:${failedResultId(index)}`,
      source: `case:${caseId(index)}`,
      target: `result:${failedResultId(index)}`,
      kind: "case_result",
    })
    edges.push({
      id: `result_issue:${failedResultId(index)}:${issueId(index)}`,
      source: `result:${failedResultId(index)}`,
      target: `issue:${issueId(index)}`,
      kind: "result_issue",
    })
  }

  return {
    versionId: VERSION_ID,
    batch: { id: BATCH_ID, batchNo: 1, kind: "baseline", status: "done" },
    nodes,
    edges,
    counts: {
      requirements: REQUIREMENT_COUNT,
      cases: REQUIREMENT_COUNT,
      results: REQUIREMENT_COUNT * 2,
      issues: REQUIREMENT_COUNT,
      byResultStatus: {
        pending: 0,
        running: 0,
        passed: REQUIREMENT_COUNT,
        failed: REQUIREMENT_COUNT,
        blocked: 0,
        inconclusive: 0,
        skipped: 0,
        interrupted: 0,
      },
      byIssueSeverity: {
        blocker: 0,
        major: REQUIREMENT_COUNT,
        minor: 0,
        info: 0,
      },
      uncoveredRequirements: 0,
      revalidationCases: 0,
      supplementCases: 0,
    },
    truncated: { requirements: false, cases: false, results: false, issues: false },
    generatedAt: "2026-10-29T12:00:00Z",
  }
}

const SNAPSHOT = chainSnapshot200()

/**
 * Runs axe on the rendered container and fails on serious/critical impact only.
 *
 * `color-contrast` is disabled because jsdom has no layout/paint engine, so this
 * gate makes **no contrast claim**. Contrast of the chain is covered separately
 * by Task 22 and its manual/visual review; this test must not be reported as an
 * "axe clean with no exclusions" result.
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

function treeItem(nodeId: string): HTMLElement {
  return screen.getByTestId(`chain-treeitem-${nodeId}`)
}

function activeTreeItem(): HTMLElement | null {
  return document.querySelector<HTMLElement>('[data-testid^="chain-treeitem-"][data-active="true"]')
}

function keyOn(element: HTMLElement, key: string): void {
  element.focus()
  fireEvent.keyDown(element, { key })
}

beforeEach(() => {
  mocks.useExplorationChain.mockReturnValue({
    data: SNAPSHOT,
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
  // jsdom logs "Not implemented: getContext" whenever axe probes a canvas.
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(null)
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe("探索链路 a11y（200 节点 + 键盘全流程）", () => {
  it("renders 200 nodes with no serious/critical axe violation and completes the keyboard flow", async () => {
    const { container } = render(
      <ExplorationTab versionId={VERSION_ID} batchId={BATCH_ID} stream={STREAM} />,
    )

    expect(await screen.findByTestId("exploration-tab")).not.toBeNull()
    await waitFor(
      () => {
        expect(screen.getAllByRole("treeitem")).toHaveLength(NODE_COUNT)
      },
      { timeout: 20_000 },
    )
    // The hidden tree renders every node; the canvas itself also mounted.
    expect(screen.getByTestId("chain-canvas")).not.toBeNull()
    // The filter toolbar must sit above the React Flow viewport, not on top of
    // the first node row: at 1280x720 a pointer click on a baseline/issue card
    // would otherwise be swallowed by the overlay. jsdom has no layout engine,
    // so this pins the structure that the E2E pointer hit-test proves.
    const toolbar = screen.getByTestId("chain-toolbar")
    const canvasViewport = screen.getByTestId("chain-canvas-viewport")
    expect(canvasViewport.contains(toolbar)).toBe(false)
    expect(toolbar.contains(canvasViewport)).toBe(false)
    expect(
      toolbar.compareDocumentPosition(canvasViewport) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).not.toBe(0)
    expect(canvasViewport.contains(screen.getByTestId("chain-canvas"))).toBe(true)
    expect(screen.getByTestId("exploration-visible-count").textContent).toContain(
      `可见 ${NODE_COUNT} 个节点`,
    )
    await expectNoSeriousA11yViolations(container)

    // --- Keyboard flow: the tree order is requirement → case → results → issue.
    const firstRequirement = treeItem(`requirement:${requirementId(1)}`)
    expect(firstRequirement.getAttribute("tabindex")).toBe("0")
    expect(firstRequirement.getAttribute("aria-expanded")).toBe("true")
    firstRequirement.focus()

    fireEvent.keyDown(firstRequirement, { key: "ArrowDown" })
    const firstCase = treeItem(`case:${caseId(1)}`)
    await waitFor(() => {
      expect(document.activeElement).toBe(firstCase)
    })

    fireEvent.keyDown(firstCase, { key: "ArrowRight" })
    const passedResult = treeItem(`result:${passedResultId(1)}`)
    await waitFor(() => {
      expect(document.activeElement).toBe(passedResult)
    })

    fireEvent.keyDown(passedResult, { key: "ArrowDown" })
    const failedResult = treeItem(`result:${failedResultId(1)}`)
    await waitFor(() => {
      expect(document.activeElement).toBe(failedResult)
    })

    fireEvent.keyDown(failedResult, { key: "ArrowDown" })
    const firstIssue = treeItem(`issue:${issueId(1)}`)
    await waitFor(() => {
      expect(document.activeElement).toBe(firstIssue)
    })

    // Enter opens the real node Sheet (a modal dialog with a focus trap).
    fireEvent.keyDown(firstIssue, { key: "Enter" })
    const sheet = await screen.findByTestId("chain-sheet")
    expect(sheet.getAttribute("role")).toBe("dialog")
    expect(sheet.getAttribute("data-node-id")).toBe(`issue:${issueId(1)}`)
    await expectNoSeriousA11yViolations(container)

    // Escape closes it and the selected node clears from the tab.
    fireEvent.keyDown(document.activeElement ?? sheet, { key: "Escape" })
    await waitFor(() => {
      expect(screen.queryByTestId("chain-sheet")).toBeNull()
    })

    // Collapse/expand through the keyboard: ← collapses an expanded requirement
    // (its case, two results and issue = 4 hidden nodes), → expands it back.
    const requirement = treeItem(`requirement:${requirementId(1)}`)
    keyOn(requirement, "ArrowLeft")
    await waitFor(() => {
      expect(screen.getByTestId("exploration-visible-count").textContent).toContain(
        `可见 ${NODE_COUNT - 4} 个节点，折叠 4 个`,
      )
    })
    expect(screen.queryByTestId(`chain-treeitem-case:${caseId(1)}`)).toBeNull()
    expect(treeItem(`requirement:${requirementId(1)}`).getAttribute("aria-expanded")).toBe("false")

    keyOn(treeItem(`requirement:${requirementId(1)}`), "ArrowRight")
    await waitFor(() => {
      expect(screen.getByTestId("exploration-visible-count").textContent).toContain(
        `可见 ${NODE_COUNT} 个节点`,
      )
    })
    expect(treeItem(`case:${caseId(1)}`)).not.toBeNull()

    // Home/End move to the first/last visible node (standard tree contract).
    fireEvent.keyDown(activeTreeItem() ?? treeItem(`requirement:${requirementId(1)}`), {
      key: "End",
    })
    const lastIssue = treeItem(`issue:${issueId(REQUIREMENT_COUNT)}`)
    await waitFor(() => {
      expect(document.activeElement).toBe(lastIssue)
    })
    fireEvent.keyDown(lastIssue, { key: "Home" })
    await waitFor(() => {
      expect(document.activeElement).toBe(treeItem(`requirement:${requirementId(1)}`))
    })

    // Space opens a node Sheet too, and Escape closes it (full keyboard path).
    fireEvent.keyDown(treeItem(`requirement:${requirementId(1)}`), { key: " " })
    expect(await screen.findByTestId("chain-sheet")).not.toBeNull()
    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" })
    await waitFor(() => {
      expect(screen.queryByTestId("chain-sheet")).toBeNull()
    })
  }, 60_000)
})
