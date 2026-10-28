import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

const mocks = vi.hoisted(() => ({
  updateNode: vi.fn(),
  deleteNode: vi.fn(),
}))

vi.mock("./api", () => ({
  mindmapApi: { updateNode: mocks.updateNode, deleteNode: mocks.deleteNode },
}))

import { ScenarioEditor } from "./ScenarioEditor"
import type { CaseNodeView } from "./types"

const VERSION_ID = "1844674407370955161"
const SCENARIO_ID = "1844674407370955162"

function scenarioNode(overrides: Partial<CaseNodeView> = {}): CaseNodeView {
  return {
    id: SCENARIO_ID,
    parentId: null,
    kind: "scenario",
    title: "订单场景",
    description: "订单主流程",
    priority: "P2",
    preconditions: "",
    testData: null,
    source: "user_input",
    status: "draft",
    sortOrder: 0,
    revision: 1,
    steps: [],
    assertions: [],
    sources: [],
    anchors: [],
    openBlockerCount: 0,
    ...overrides,
  }
}

function conflictError(): ApiError {
  return new ApiError({
    status: 409,
    code: "conflict",
    message: "the scenario was modified",
    details: { reason: "case_revision_conflict" },
  })
}

function renderEditor(overrides: Partial<Parameters<typeof ScenarioEditor>[0]> = {}) {
  const onSaved = overrides.onSaved ?? vi.fn()
  const onDeleted = overrides.onDeleted ?? vi.fn()
  const result = render(
    <ScenarioEditor
      versionId={VERSION_ID}
      node={scenarioNode()}
      editable
      childCount={0}
      onSaved={onSaved}
      onDeleted={onDeleted}
      {...overrides}
    />,
  )
  return { ...result, onSaved, onDeleted }
}

describe("ScenarioEditor", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
  })

  afterEach(cleanup)

  it("saves the edited scenario and reports the updated node", async () => {
    const updated = scenarioNode({ title: "订单场景（修订）", revision: 2 })
    mocks.updateNode.mockResolvedValue(updated)
    const onSaved = vi.fn()
    renderEditor({ onSaved })

    fireEvent.change(screen.getByLabelText("场景标题"), {
      target: { value: "订单场景（修订）" },
    })
    fireEvent.change(screen.getByLabelText("描述"), {
      target: { value: "更新后的描述" },
    })
    fireEvent.click(screen.getByRole("button", { name: "保存场景" }))

    await waitFor(() =>
      expect(mocks.updateNode).toHaveBeenCalledWith(VERSION_ID, SCENARIO_ID, {
        title: "订单场景（修订）",
        description: "更新后的描述",
        priority: "P2",
        revision: 1,
      }),
    )
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith(updated))
  })

  it("keeps the local draft and shows the reload banner on a 409 revision conflict", async () => {
    mocks.updateNode.mockRejectedValue(conflictError())
    renderEditor()

    fireEvent.change(screen.getByLabelText("场景标题"), {
      target: { value: "订单场景（本地草稿）" },
    })
    fireEvent.click(screen.getByRole("button", { name: "保存场景" }))

    const banner = await screen.findByTestId("scenario-revision-conflict")
    expect(banner.textContent).toContain("内容已变化，请刷新")
    expect(banner.getAttribute("role")).toBe("alert")
    expect((screen.getByLabelText("场景标题") as HTMLInputElement).value).toBe(
      "订单场景（本地草稿）",
    )
  })

  it("states the descendant count in the delete confirmation", async () => {
    mocks.deleteNode.mockResolvedValue(undefined)
    const onDeleted = vi.fn()
    renderEditor({ childCount: 3, onDeleted })

    fireEvent.click(screen.getByRole("button", { name: "删除场景" }))

    const dialog = screen.getByRole("alertdialog", { name: "确认删除场景" })
    expect(dialog.textContent).toContain("其下 3 个子节点将被一并删除")

    fireEvent.click(screen.getByRole("button", { name: "确认删除" }))

    await waitFor(() => expect(mocks.deleteNode).toHaveBeenCalledWith(VERSION_ID, SCENARIO_ID, 1))
    expect(onDeleted).toHaveBeenCalledWith(SCENARIO_ID)
  })
})
