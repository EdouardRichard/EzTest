import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  updateVisualAssertion: vi.fn(),
  approveVisualBaseline: vi.fn(),
}))

vi.mock("./api", () => ({
  mindmapApi: {
    updateVisualAssertion: mocks.updateVisualAssertion,
    approveVisualBaseline: mocks.approveVisualBaseline,
  },
}))

import { CaseEditor } from "./CaseEditor"
import type { CaseAssertionView, CaseNodeView, CaseVisualAssertionView } from "./types"

const VERSION_ID = "1844674407370955161"
const NODE_ID = "1844674407370955162"
const ASSERTION_ID = "1844674407370955166"

function visualView(overrides: Partial<CaseVisualAssertionView> = {}): CaseVisualAssertionView {
  return {
    selector: "#chart",
    region: null,
    threshold: 0.005,
    baselineEvidenceId: null,
    viewportWidth: 1280,
    viewportHeight: 720,
    deviceScaleFactor: 1,
    approvedBy: null,
    approvedAt: null,
    revision: 1,
    ...overrides,
  }
}

function visualAssertion(overrides: Partial<CaseAssertionView> = {}): CaseAssertionView {
  return {
    id: ASSERTION_ID,
    stepId: null,
    kind: "visual",
    target: "",
    expression: "页面与基线一致",
    expected: "",
    source: "user_input",
    visual: visualView(),
    ...overrides,
  }
}

function caseNode(assertion: CaseAssertionView): CaseNodeView {
  return {
    id: NODE_ID,
    parentId: null,
    kind: "case",
    title: "商品详情视觉回归",
    description: "",
    priority: "P1",
    preconditions: "",
    testData: null,
    source: "user_input",
    status: "draft",
    sortOrder: 0,
    revision: 7,
    steps: [{ id: "step-1", seq: 1, action: "打开详情页", expected: "", data: "", source: "user_input" }],
    assertions: [assertion],
    sources: [],
    anchors: [],
    openBlockerCount: 0,
  }
}

function renderEditor(overrides: Partial<Parameters<typeof CaseEditor>[0]> = {}) {
  const onSaved = vi.fn()
  render(
    <CaseEditor
      versionId={VERSION_ID}
      node={caseNode(visualAssertion())}
      editable
      onSaved={onSaved}
      onDeleted={vi.fn()}
      {...overrides}
    />,
  )
  return onSaved
}

describe("CaseEditor visual assertion", () => {
  beforeEach(() => {
    mocks.updateVisualAssertion.mockReset()
    mocks.approveVisualBaseline.mockReset()
    mocks.updateVisualAssertion.mockResolvedValue(
      visualView({ revision: 2, selector: "#total", threshold: 0.02 }),
    )
    mocks.approveVisualBaseline.mockResolvedValue(
      visualView({ baselineEvidenceId: "501", approvedBy: 1, approvedAt: "2026-10-29T09:00:00Z" }),
    )
  })

  afterEach(cleanup)

  it("saves selector, threshold, viewport and normalized region under the node revision", async () => {
    const onSaved = renderEditor()

    fireEvent.change(screen.getByLabelText("断言 1 视觉选择器"), { target: { value: "#total" } })
    fireEvent.change(screen.getByLabelText("断言 1 视觉阈值"), { target: { value: "0.02" } })
    fireEvent.click(screen.getByLabelText("断言 1 启用区域裁剪"))
    fireEvent.change(screen.getByLabelText("断言 1 区域 x"), { target: { value: "0.1" } })
    fireEvent.change(screen.getByLabelText("断言 1 区域 y"), { target: { value: "0.2" } })
    fireEvent.change(screen.getByLabelText("断言 1 区域 width"), { target: { value: "0.5" } })
    fireEvent.change(screen.getByLabelText("断言 1 区域 height"), { target: { value: "0.3" } })
    fireEvent.click(screen.getByLabelText("保存视觉参数 1"))

    await waitFor(() => expect(mocks.updateVisualAssertion).toHaveBeenCalledTimes(1))
    expect(mocks.updateVisualAssertion).toHaveBeenCalledWith(VERSION_ID, ASSERTION_ID, {
      nodeRevision: 7,
      selector: "#total",
      region: { x: 0.1, y: 0.2, width: 0.5, height: 0.3 },
      threshold: 0.02,
      viewport: { width: 1280, height: 720 },
      deviceScaleFactor: 1,
    })
    expect(onSaved).toHaveBeenCalled()
  })

  it("approves a baseline as an administrator and shows the approved id", async () => {
    const onSaved = renderEditor({ isAdmin: true })

    fireEvent.change(screen.getByLabelText("断言 1 基线证据 ID"), { target: { value: "501" } })
    fireEvent.click(screen.getByLabelText("审批基线 1"))

    await waitFor(() => expect(mocks.approveVisualBaseline).toHaveBeenCalledTimes(1))
    expect(mocks.approveVisualBaseline).toHaveBeenCalledWith(VERSION_ID, ASSERTION_ID, {
      evidenceId: "501",
    })
    expect(onSaved).toHaveBeenCalled()
    expect(await screen.findByText("基线 #501")).toBeTruthy()
  })

  it("keeps baseline approval admin-only", () => {
    renderEditor({ isAdmin: false })
    expect(screen.queryByLabelText("审批基线 1")).toBeNull()
    expect(screen.getByText("基线审批需要管理员权限。")).toBeTruthy()
  })
})
