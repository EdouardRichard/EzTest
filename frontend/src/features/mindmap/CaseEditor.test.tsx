import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

const mocks = vi.hoisted(() => ({
  updateNode: vi.fn(),
  deleteNode: vi.fn(),
  createStep: vi.fn(),
  updateStep: vi.fn(),
  deleteStep: vi.fn(),
  reorderSteps: vi.fn(),
  createAssertion: vi.fn(),
  updateAssertion: vi.fn(),
  deleteAssertion: vi.fn(),
}))

vi.mock("./api", () => ({
  mindmapApi: {
    updateNode: mocks.updateNode,
    deleteNode: mocks.deleteNode,
    createStep: mocks.createStep,
    updateStep: mocks.updateStep,
    deleteStep: mocks.deleteStep,
    reorderSteps: mocks.reorderSteps,
    createAssertion: mocks.createAssertion,
    updateAssertion: mocks.updateAssertion,
    deleteAssertion: mocks.deleteAssertion,
  },
}))

import { CaseEditor } from "./CaseEditor"
import type { CaseAssertionView, CaseNodeView, CaseStepView } from "./types"

const VERSION_ID = "1844674407370955161"
const NODE_ID = "1844674407370955162"
const S1 = "1844674407370955163"
const S2 = "1844674407370955164"
const S3 = "1844674407370955165"
const A1 = "1844674407370955166"
const A2 = "1844674407370955167"

function step(overrides: Partial<CaseStepView> & Pick<CaseStepView, "id" | "seq">): CaseStepView {
  return {
    action: "打开订单",
    expected: "展示订单",
    data: "",
    source: "user_input",
    ...overrides,
  }
}

function assertion(
  overrides: Partial<CaseAssertionView> & Pick<CaseAssertionView, "id">,
): CaseAssertionView {
  return {
    stepId: null,
    kind: "ui",
    target: "/orders",
    expression: "状态为已取消",
    expected: "显示已取消",
    source: "user_input",
    ...overrides,
  }
}

function caseNode(overrides: Partial<CaseNodeView> = {}): CaseNodeView {
  return {
    id: NODE_ID,
    parentId: null,
    kind: "case",
    title: "取消订单",
    description: "用户取消已创建的订单。",
    priority: "P1",
    preconditions: "用户已登录",
    testData: { orderNo: "A001" },
    source: "user_input",
    status: "draft",
    sortOrder: 0,
    revision: 7,
    steps: [
      step({ id: S1, seq: 1, action: "打开订单" }),
      step({ id: S2, seq: 2, action: "点击取消" }),
      step({ id: S3, seq: 3, action: "确认取消" }),
    ],
    assertions: [
      assertion({ id: A1, stepId: S3, kind: "ui" }),
      assertion({ id: A2, kind: "api", target: "/api/orders/{id}", expression: "返回 200" }),
    ],
    sources: [],
    anchors: [],
    openBlockerCount: 0,
    ...overrides,
  }
}

function renderEditor(overrides: Partial<Parameters<typeof CaseEditor>[0]> = {}) {
  const onSaved = overrides.onSaved ?? vi.fn()
  const onDeleted = overrides.onDeleted ?? vi.fn()
  const result = render(
    <CaseEditor
      versionId={VERSION_ID}
      node={caseNode()}
      editable
      onSaved={onSaved}
      onDeleted={onDeleted}
      {...overrides}
    />,
  )
  return { ...result, onSaved, onDeleted }
}

function conflictError(): ApiError {
  return new ApiError({
    status: 409,
    code: "conflict",
    message: "the case node was modified",
    details: { reason: "case_revision_conflict" },
  })
}

describe("CaseEditor", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
  })

  afterEach(cleanup)

  it("keeps the local draft and shows the reload banner on a 409 revision conflict", async () => {
    mocks.updateNode.mockRejectedValue(conflictError())
    renderEditor()

    fireEvent.change(screen.getByLabelText("用例标题"), {
      target: { value: "取消订单（本地草稿）" },
    })
    fireEvent.click(screen.getByRole("button", { name: "保存用例" }))

    const banner = await screen.findByTestId("case-revision-conflict")
    expect(banner.textContent).toContain("内容已变化，请刷新")
    expect(banner.getAttribute("role")).toBe("alert")
    expect((screen.getByLabelText("用例标题") as HTMLInputElement).value).toBe(
      "取消订单（本地草稿）",
    )
    expect(mocks.updateNode).toHaveBeenCalledTimes(1)
    expect(mocks.updateNode).toHaveBeenCalledWith(
      VERSION_ID,
      NODE_ID,
      expect.objectContaining({ title: "取消订单（本地草稿）", revision: 7 }),
    )
  })

  it("does not submit invalid testData JSON", async () => {
    renderEditor()

    fireEvent.change(screen.getByLabelText("测试数据（JSON）"), {
      target: { value: '{ "orderNo": ' },
    })
    fireEvent.click(screen.getByRole("button", { name: "保存用例" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("测试数据必须是合法的 JSON")
    expect(mocks.updateNode).not.toHaveBeenCalled()
  })

  it("submits parsed testData together with the node revision", async () => {
    const updated = caseNode({ title: "取消订单（修订）", revision: 8 })
    mocks.updateNode.mockResolvedValue(updated)
    const onSaved = vi.fn()
    renderEditor({ onSaved })

    fireEvent.change(screen.getByLabelText("用例标题"), {
      target: { value: "取消订单（修订）" },
    })
    fireEvent.change(screen.getByLabelText("测试数据（JSON）"), {
      target: { value: '{ "orderNo": "B002", "force": true }' },
    })
    fireEvent.click(screen.getByRole("button", { name: "保存用例" }))

    await waitFor(() =>
      expect(mocks.updateNode).toHaveBeenCalledWith(
        VERSION_ID,
        NODE_ID,
        expect.objectContaining({
          title: "取消订单（修订）",
          testData: { orderNo: "B002", force: true },
          revision: 7,
        }),
      ),
    )
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith(updated))
  })

  it("calls the step order endpoint when moving a step up", async () => {
    mocks.reorderSteps.mockResolvedValue(undefined)
    const onSaved = vi.fn()
    renderEditor({ onSaved })

    fireEvent.click(screen.getByRole("button", { name: "上移步骤 2" }))

    await waitFor(() =>
      expect(mocks.reorderSteps).toHaveBeenCalledWith(VERSION_ID, NODE_ID, {
        stepIds: [S2, S1, S3],
        nodeRevision: 7,
      }),
    )
    await waitFor(() =>
      expect((screen.getByLabelText("步骤 1 动作") as HTMLInputElement).value).toBe("点击取消"),
    )
    expect(onSaved).toHaveBeenCalled()
  })

  it("disables deleting the last assertion and explains why", () => {
    renderEditor({ node: caseNode({ assertions: [assertion({ id: A1 })] }) })

    const remove = screen.getByRole("button", { name: "删除断言 1" }) as HTMLButtonElement
    expect(remove.disabled).toBe(true)
    expect(screen.getByText("每个用例至少保留一条断言，最后一条不可删除。")).toBeTruthy()

    fireEvent.click(remove)
    expect(mocks.deleteAssertion).not.toHaveBeenCalled()
  })

  it("deletes an assertion when another one remains", async () => {
    mocks.deleteAssertion.mockResolvedValue(undefined)
    const onSaved = vi.fn()
    renderEditor({ onSaved })

    fireEvent.click(screen.getByRole("button", { name: "删除断言 1" }))

    await waitFor(() => expect(mocks.deleteAssertion).toHaveBeenCalledWith(VERSION_ID, A1, 7))
    expect(onSaved).toHaveBeenCalled()
  })

  it("disables every input and editing action in read-only mode", () => {
    renderEditor({ editable: false })

    for (const element of screen.getAllByRole("textbox")) {
      expect((element as HTMLInputElement | HTMLTextAreaElement).disabled).toBe(true)
    }
    for (const element of screen.getAllByRole("combobox")) {
      expect((element as HTMLSelectElement).disabled).toBe(true)
    }

    expect((screen.getByRole("button", { name: "上移步骤 2" }) as HTMLButtonElement).disabled).toBe(
      true,
    )
    expect((screen.getByRole("button", { name: "删除断言 1" }) as HTMLButtonElement).disabled).toBe(
      true,
    )
    expect(screen.queryByRole("button", { name: "保存用例" })).toBeNull()
    expect(screen.getByTestId("case-editor-readonly")).toBeTruthy()
  })

  it("guides the user when a scenario node is passed instead of a case", () => {
    renderEditor({ node: caseNode({ kind: "scenario", steps: [], assertions: [] }) })

    expect(screen.getByTestId("case-editor").textContent).toContain("请选择用例节点")
  })
})
