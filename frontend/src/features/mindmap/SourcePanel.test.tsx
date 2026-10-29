import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  createSource: vi.fn(),
  deleteSource: vi.fn(),
  createAnchor: vi.fn(),
  deleteAnchor: vi.fn(),
  listRequirements: vi.fn(),
  listAppModels: vi.fn(),
}))

vi.mock("./api", () => ({
  mindmapApi: {
    createSource: mocks.createSource,
    deleteSource: mocks.deleteSource,
    createAnchor: mocks.createAnchor,
    deleteAnchor: mocks.deleteAnchor,
  },
}))

vi.mock("@/features/versions/api", () => ({
  listRequirements: mocks.listRequirements,
}))

vi.mock("@/features/resources/api", () => ({
  listAppModels: mocks.listAppModels,
}))

import { SourcePanel, type SourceRequirementOption } from "./SourcePanel"
import type { CaseNodeView, CaseSourceView, CodeAnchorView } from "./types"

const VERSION_ID = "1844674407370955161"
const NODE_ID = "1844674407370955162"
const SRC1 = "1844674407370955163"
const SRC2 = "1844674407370955164"
const SRC3 = "1844674407370955165"
const ANCHOR1 = "1844674407370955166"
const REQ1 = "1844674407370955170"
const REQ2 = "1844674407370955171"
const REQ3 = "1844674407370955172"
const APP_MODEL_ID = 501
const APP_MODEL_KEY = "page:/orders"

const REQUIREMENTS: SourceRequirementOption[] = [
  { id: REQ1, title: "订单创建", status: "clarified" },
  { id: REQ2, title: "取消订单", status: "clarified" },
  { id: REQ3, title: "未澄清需求", status: "draft" },
]

function source(overrides: Partial<CaseSourceView> & Pick<CaseSourceView, "id">): CaseSourceView {
  return {
    refKind: "requirement",
    refId: REQ1,
    refKey: "",
    source: "user_input",
    confidence: "high",
    ...overrides,
  }
}

function anchor(overrides: Partial<CodeAnchorView> & Pick<CodeAnchorView, "id">): CodeAnchorView {
  return {
    filePath: "src/order.ts",
    startLine: 10,
    endLine: 20,
    confidence: "medium",
    commitSha: null,
    ...overrides,
  }
}

function node(overrides: Partial<CaseNodeView> = {}): CaseNodeView {
  return {
    id: NODE_ID,
    parentId: null,
    kind: "case",
    title: "取消订单",
    description: "",
    priority: "P1",
    preconditions: "",
    testData: null,
    source: "user_input",
    status: "draft",
    sortOrder: 0,
    revision: 4,
    steps: [],
    assertions: [],
    sources: [],
    anchors: [],
    openBlockerCount: 0,
    ...overrides,
  }
}

function renderPanel(overrides: Partial<Parameters<typeof SourcePanel>[0]> = {}) {
  const onChanged = overrides.onChanged ?? vi.fn()
  const result = render(
    <SourcePanel
      versionId={VERSION_ID}
      node={node()}
      editable
      requirements={REQUIREMENTS}
      onChanged={onChanged}
      {...overrides}
    />,
  )
  return { ...result, onChanged }
}

describe("SourcePanel", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    mocks.listRequirements.mockResolvedValue({ items: [], nextCursor: "", hasMore: false })
    mocks.listAppModels.mockResolvedValue({ items: [], nextCursor: null, hasMore: false })
  })

  afterEach(cleanup)

  it("disables deleting the last requirement source and explains why", () => {
    renderPanel({ node: node({ sources: [source({ id: SRC1 })] }) })

    const remove = screen.getByRole("button", { name: `删除来源 ${SRC1}` }) as HTMLButtonElement
    expect(remove.disabled).toBe(true)
    expect(screen.getByText("每个用例至少保留一条需求来源，最后一条不可删除。")).toBeTruthy()

    fireEvent.click(remove)
    expect(mocks.deleteSource).not.toHaveBeenCalled()
  })

  it("deletes a requirement source when another one remains", async () => {
    mocks.deleteSource.mockResolvedValue(undefined)
    const onChanged = vi.fn()
    renderPanel({
      node: node({
        sources: [source({ id: SRC1, refId: REQ1 }), source({ id: SRC2, refId: REQ2 })],
      }),
      onChanged,
    })

    fireEvent.click(screen.getByRole("button", { name: `删除来源 ${SRC1}` }))

    await waitFor(() => expect(mocks.deleteSource).toHaveBeenCalledWith(VERSION_ID, SRC1))
    await waitFor(() => expect(onChanged).toHaveBeenCalled())
  })

  it("adds a clarified requirement as a source", async () => {
    mocks.createSource.mockResolvedValue(source({ id: SRC3 }))
    const onChanged = vi.fn()
    renderPanel({ node: node({ sources: [] }), onChanged })

    const select = screen.getByLabelText("选择需求") as HTMLSelectElement
    expect(select.textContent).toContain("订单创建")
    expect(select.textContent).not.toContain("未澄清需求")

    fireEvent.change(select, { target: { value: REQ1 } })
    fireEvent.click(screen.getByRole("button", { name: "添加需求来源" }))

    await waitFor(() =>
      expect(mocks.createSource).toHaveBeenCalledWith(VERSION_ID, NODE_ID, {
        refKind: "requirement",
        refId: REQ1,
      }),
    )
    await waitFor(() => expect(onChanged).toHaveBeenCalled())
  })

  it("posts the selected app model id and key for an app_model source", async () => {
    mocks.listAppModels.mockResolvedValue({
      items: [
        {
          id: APP_MODEL_ID,
          systemId: 9,
          parentId: null,
          kind: "page",
          key: APP_MODEL_KEY,
          name: "订单列表",
          path: "/orders",
          method: "",
          source: "manual",
          meta: null,
        },
      ],
      nextCursor: null,
      hasMore: false,
    })
    mocks.createSource.mockResolvedValue(
      source({
        id: SRC3,
        refKind: "app_model",
        refId: String(APP_MODEL_ID),
        refKey: APP_MODEL_KEY,
      }),
    )
    const onChanged = vi.fn()
    renderPanel({ node: node({ sources: [] }), systemId: 9, onChanged })

    await waitFor(() => expect(mocks.listAppModels).toHaveBeenCalledWith(9, { limit: 100 }))

    fireEvent.change(screen.getByLabelText("来源类型"), { target: { value: "app_model" } })
    const picker = await screen.findByLabelText("选择应用模型")
    fireEvent.change(picker, { target: { value: String(APP_MODEL_ID) } })
    fireEvent.click(screen.getByRole("button", { name: "添加来源" }))

    await waitFor(() =>
      expect(mocks.createSource).toHaveBeenCalledWith(VERSION_ID, NODE_ID, {
        refKind: "app_model",
        refId: String(APP_MODEL_ID),
        refKey: APP_MODEL_KEY,
      }),
    )
    await waitFor(() => expect(onChanged).toHaveBeenCalled())
  })

  it("adds a manual source with only a refKey", async () => {
    mocks.createSource.mockResolvedValue(
      source({ id: SRC3, refKind: "manual", refId: null, refKey: "会议纪要" }),
    )
    const onChanged = vi.fn()
    renderPanel({ node: node({ sources: [] }), onChanged })

    fireEvent.change(screen.getByLabelText("来源类型"), { target: { value: "manual" } })
    fireEvent.change(screen.getByLabelText("来源引用"), { target: { value: "会议纪要" } })
    fireEvent.click(screen.getByRole("button", { name: "添加来源" }))

    await waitFor(() =>
      expect(mocks.createSource).toHaveBeenCalledWith(VERSION_ID, NODE_ID, {
        refKind: "manual",
        refKey: "会议纪要",
      }),
    )
  })

  it("explains that an app model source needs a bound system", () => {
    renderPanel({ node: node({ sources: [] }), systemId: null })

    fireEvent.change(screen.getByLabelText("来源类型"), { target: { value: "app_model" } })

    expect((screen.getByLabelText("选择应用模型") as HTMLSelectElement).disabled).toBe(true)
    expect(screen.getByText(/未绑定系统/)).toBeTruthy()
    expect(mocks.listAppModels).not.toHaveBeenCalled()
  })

  it("adds and deletes code anchors through the API", async () => {
    mocks.createAnchor.mockResolvedValue(anchor({ id: ANCHOR1 }))
    mocks.deleteAnchor.mockResolvedValue(undefined)
    const onChanged = vi.fn()
    renderPanel({ node: node({ anchors: [] }), onChanged })

    fireEvent.change(screen.getByLabelText("文件路径"), {
      target: { value: "src/order.ts" },
    })
    fireEvent.change(screen.getByLabelText("起始行（可选）"), { target: { value: "10" } })
    fireEvent.change(screen.getByLabelText("结束行（可选）"), { target: { value: "20" } })
    fireEvent.click(screen.getByRole("button", { name: "添加锚点" }))

    await waitFor(() =>
      expect(mocks.createAnchor).toHaveBeenCalledWith(VERSION_ID, NODE_ID, {
        filePath: "src/order.ts",
        startLine: 10,
        endLine: 20,
      }),
    )

    const remove = await screen.findByRole("button", { name: "删除锚点 src/order.ts:10-20" })
    fireEvent.click(remove)

    await waitFor(() => expect(mocks.deleteAnchor).toHaveBeenCalledWith(VERSION_ID, ANCHOR1))
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(2))
  })

  it("shows the existing sources with their confidence badges", () => {
    renderPanel({ node: node({ sources: [source({ id: SRC1 })] }) })

    const row = screen.getByTestId(`source-row-${SRC1}`)
    expect(row.textContent).toContain("需求")
    expect(row.textContent).toContain("订单创建")
    expect(row.textContent).toContain("置信度 高")
  })
})
