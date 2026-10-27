import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

const mocks = vi.hoisted(() => ({
  listRequirements: vi.fn(),
  createRequirement: vi.fn(),
  updateRequirement: vi.fn(),
  deleteRequirement: vi.fn(),
  confirmRequirement: vi.fn(),
  reopenRequirement: vi.fn(),
  listRelations: vi.fn(),
  createRelation: vi.fn(),
  updateRelation: vi.fn(),
  deleteRelation: vi.fn(),
  listAttachments: vi.fn(),
  uploadAttachment: vi.fn(),
  downloadAttachment: vi.fn(),
  deleteAttachment: vi.fn(),
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
  useVersionStream: vi.fn(),
  listSystems: vi.fn(),
  listEnvironments: vi.fn(),
  listCodeRepos: vi.fn(),
}))

vi.mock("./api", () => ({
  listRequirements: mocks.listRequirements,
  createRequirement: mocks.createRequirement,
  updateRequirement: mocks.updateRequirement,
  deleteRequirement: mocks.deleteRequirement,
  confirmRequirement: mocks.confirmRequirement,
  reopenRequirement: mocks.reopenRequirement,
  listRelations: mocks.listRelations,
  createRelation: mocks.createRelation,
  updateRelation: mocks.updateRelation,
  deleteRelation: mocks.deleteRelation,
  listAttachments: mocks.listAttachments,
  uploadAttachment: mocks.uploadAttachment,
  downloadAttachment: mocks.downloadAttachment,
  deleteAttachment: mocks.deleteAttachment,
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
}))

vi.mock("./useVersionStream", () => ({ useVersionStream: mocks.useVersionStream }))

vi.mock("@/features/resources/api", () => ({
  listSystems: mocks.listSystems,
  listEnvironments: mocks.listEnvironments,
}))

vi.mock("@/features/code-repos/api", () => ({
  listCodeRepos: mocks.listCodeRepos,
}))

import type {
  ClarificationDecision,
  ClarificationQuestion,
  ClarificationSession,
  Requirement,
  RequirementRelation,
  Version,
  VersionEvent,
} from "./types"
import { Workbench } from "./Workbench"

const VERSION_ID = "1844674407370955161"
const REQ_A = "1844674407370956001"
const REQ_B = "1844674407370956002"
const REQ_C = "1844674407370956003"
const REL_ID = "1844674407370956101"

const VERSION: Version = {
  id: VERSION_ID,
  name: "订单系统 2.0",
  label: "v2.0",
  description: "",
  status: "clarifying",
  systemId: null,
  environmentId: null,
  codeRepositoryId: null,
  codeBranch: null,
  codeBaselineCommit: null,
  bindings: { system: null, environment: null, codeRepository: null },
  requirementCount: 2,
  clarifiedCount: 1,
  openQuestionCount: 2,
  revision: 2,
  createdBy: 1,
  updatedBy: 1,
  createdAt: "2026-10-27T10:00:00Z",
  updatedAt: "2026-10-27T10:00:00Z",
}

const CLARIFICATION_SESSION: ClarificationSession = {
  id: "1844674407370957001",
  status: "active",
  agentSessionId: 77,
  startedAt: "2026-10-27T10:00:00Z",
  endedAt: null,
  contextSummary: "",
  summaryUpdatedAt: null,
}

function requirement(
  overrides: Partial<Requirement> & Pick<Requirement, "id" | "title">,
): Requirement {
  return {
    versionId: VERSION_ID,
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
    createdAt: "2026-10-27T10:00:00Z",
    updatedAt: "2026-10-27T10:00:00Z",
    ...overrides,
  }
}

const DRAFT = requirement({
  id: REQ_A,
  title: "订单创建",
  description: "用户提交订单。",
  openQuestionCount: 2,
  relationCount: 1,
  revision: 3,
})
const CLARIFIED = requirement({
  id: REQ_B,
  title: "库存扣减",
  status: "clarified",
  revision: 4,
  confirmedAt: "2026-10-27T11:00:00Z",
  confirmedBy: 1,
})

const RELATION: RequirementRelation = {
  id: REL_ID,
  versionId: VERSION_ID,
  fromRequirementId: REQ_A,
  toRequirementId: REQ_B,
  relationType: "depends_on",
  description: "创建订单后扣减库存",
  source: "user",
  createdBy: 1,
  createdAt: "2026-10-27T10:00:00Z",
  updatedAt: "2026-10-27T10:00:00Z",
}

const CLARIFICATION_QUESTION: ClarificationQuestion = {
  id: "1844674407370957101",
  versionId: VERSION_ID,
  requirementId: REQ_A,
  sessionId: CLARIFICATION_SESSION.id,
  question: "下单后如何扣减库存？",
  status: "open",
  answer: "",
  source: "ai",
  answeredVia: "ui",
  answeredBy: null,
  answeredAt: null,
  createdBy: 1,
  createdAt: "2026-10-27T10:00:00Z",
  updatedAt: "2026-10-27T10:00:00Z",
}

function clarificationDecision(
  overrides: Partial<ClarificationDecision> &
    Pick<ClarificationDecision, "id" | "topic" | "status">,
): ClarificationDecision {
  return {
    versionId: VERSION_ID,
    requirementId: REQ_A,
    questionId: null,
    decision: "支持 7 天无理由退款",
    rationale: "",
    source: "ai",
    proposedBy: 1,
    decidedBy: null,
    decidedAt: null,
    createdAt: "2026-10-27T10:00:00Z",
    updatedAt: "2026-10-27T10:00:00Z",
    ...overrides,
  }
}

const PROPOSED_DECISION = clarificationDecision({
  id: "1844674407370957201",
  topic: "退款政策",
  status: "proposed",
})

const ACCEPTED_DECISION = clarificationDecision({
  id: "1844674407370957201",
  topic: "退款政策",
  status: "accepted",
  decidedBy: 1,
  decidedAt: "2026-10-27T12:00:00Z",
})

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
    ts: new Date(Date.UTC(2026, 9, 27, 10, 0, seq)).toISOString(),
    summary: "",
    detail: {},
    refs: { evidence: [], case: [], requirement: [], code: [] },
  }
}

function page(items: Requirement[], nextCursor = "", hasMore = false) {
  return { items, nextCursor, hasMore }
}

function renderWorkbench(onChanged: () => void = vi.fn()) {
  return render(<Workbench version={VERSION} onChanged={onChanged} />)
}

describe("Workbench", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    mocks.listRequirements.mockResolvedValue(page([DRAFT, CLARIFIED]))
    mocks.listRelations.mockResolvedValue([RELATION])
    mocks.listAttachments.mockResolvedValue([])
    mocks.listSystems.mockResolvedValue({ items: [], nextCursor: null, hasMore: false })
    mocks.listEnvironments.mockResolvedValue([])
    mocks.listCodeRepos.mockResolvedValue({ items: [], nextCursor: null, hasMore: false })
    mocks.useVersionStream.mockReturnValue({ events: [], status: "live", reconnect: vi.fn() })
    mocks.getClarification.mockResolvedValue({
      session: CLARIFICATION_SESSION,
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
  })

  afterEach(cleanup)

  it("loads requirements and relations, auto-selects the first requirement and shows real counts", async () => {
    renderWorkbench()

    expect(await screen.findByRole("heading", { name: "需求与澄清工作台" })).toBeTruthy()
    await waitFor(() =>
      expect((screen.getByLabelText("标题") as HTMLInputElement).value).toBe("订单创建"),
    )

    expect(mocks.listRequirements).toHaveBeenCalledWith(VERSION_ID, { limit: 100 })
    expect(mocks.listRelations).toHaveBeenCalledWith(VERSION_ID)
    expect(mocks.listAttachments).toHaveBeenCalledWith(VERSION_ID, REQ_A)

    const list = screen.getByRole("region", { name: "需求列表" })
    expect(within(list).getByText("库存扣减")).toBeTruthy()
    expect(screen.getByText("待澄清 2")).toBeTruthy()
    expect(screen.getByText("待澄清问题 2")).toBeTruthy()
    expect(screen.getByText("共 1 条关系")).toBeTruthy()
    expect(screen.getByText("依赖（有向，箭头指向被依赖需求）")).toBeTruthy()
    expect(screen.getByRole("button", { name: "保存绑定" })).toBeTruthy()
  })

  it("creates, edits and confirms a requirement while refreshing version counts", async () => {
    const created = requirement({ id: REQ_C, title: "退款审核", revision: 1 })
    const updated = requirement({
      id: REQ_C,
      title: "退款审核",
      description: "客服审核退款申请。",
      revision: 2,
    })
    const confirmed = requirement({
      id: REQ_C,
      title: "退款审核",
      description: "客服审核退款申请。",
      status: "clarified",
      revision: 3,
      confirmedAt: "2026-10-27T12:00:00Z",
      confirmedBy: 1,
    })
    mocks.listRequirements.mockResolvedValue(page([]))
    mocks.listRelations.mockResolvedValue([])
    mocks.createRequirement.mockResolvedValue(created)
    mocks.updateRequirement.mockResolvedValue(updated)
    mocks.confirmRequirement.mockResolvedValue(confirmed)
    const onChanged = vi.fn()

    renderWorkbench(onChanged)
    expect(await screen.findByText("暂无需求")).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "新增需求" }))
    fireEvent.change(screen.getByLabelText("标题"), { target: { value: "退款审核" } })
    fireEvent.click(screen.getByRole("button", { name: "创建需求" }))

    await waitFor(() =>
      expect(mocks.createRequirement).toHaveBeenCalledWith(VERSION_ID, {
        title: "退款审核",
        description: "",
        acceptanceCriteria: "",
        priority: "should",
      }),
    )
    await waitFor(() =>
      expect((screen.getByLabelText("标题") as HTMLInputElement).value).toBe("退款审核"),
    )
    expect(onChanged).toHaveBeenCalledTimes(1)

    fireEvent.change(screen.getByLabelText("描述"), {
      target: { value: "客服审核退款申请。" },
    })
    fireEvent.click(screen.getByRole("button", { name: "保存需求" }))

    await waitFor(() =>
      expect(mocks.updateRequirement).toHaveBeenCalledWith(VERSION_ID, REQ_C, {
        revision: 1,
        title: "退款审核",
        description: "客服审核退款申请。",
        acceptanceCriteria: "",
        priority: "should",
      }),
    )
    expect(onChanged).toHaveBeenCalledTimes(2)

    fireEvent.click(screen.getByRole("button", { name: "确认需求" }))
    await waitFor(() => expect(mocks.confirmRequirement).toHaveBeenCalledWith(VERSION_ID, REQ_C))
    expect(onChanged).toHaveBeenCalledTimes(3)
    await waitFor(() =>
      expect(screen.getByText("已确认需求为只读，可重开后继续编辑。")).toBeTruthy(),
    )
  })

  it("reopens a clarified requirement", async () => {
    mocks.listRequirements.mockResolvedValue(page([CLARIFIED]))
    mocks.listRelations.mockResolvedValue([])
    const reopened = { ...CLARIFIED, status: "draft" as const, revision: 5, confirmedAt: null }
    mocks.reopenRequirement.mockResolvedValue(reopened)
    const onChanged = vi.fn()

    renderWorkbench(onChanged)
    fireEvent.click(await screen.findByRole("button", { name: "重开需求" }))

    await waitFor(() => expect(mocks.reopenRequirement).toHaveBeenCalledWith(VERSION_ID, REQ_B))
    expect(onChanged).toHaveBeenCalledTimes(1)
    await waitFor(() => expect(screen.getByRole("button", { name: "保存需求" })).toBeTruthy())
  })

  it("creates, edits and deletes a relation, refreshing the graph", async () => {
    mocks.listRelations.mockResolvedValueOnce([]).mockResolvedValue([RELATION])
    mocks.createRelation.mockResolvedValue(RELATION)
    const updatedRelation = { ...RELATION, relationType: "overlaps" as const }
    mocks.updateRelation.mockResolvedValue(updatedRelation)
    mocks.deleteRelation.mockResolvedValue(undefined)

    renderWorkbench()
    await screen.findByText("暂无关系，添加关系后将在此展示关系图。")

    fireEvent.change(screen.getByLabelText("起点需求"), { target: { value: REQ_A } })
    fireEvent.change(screen.getByLabelText("终点需求"), { target: { value: REQ_B } })
    fireEvent.change(screen.getByLabelText("关系说明"), {
      target: { value: "创建订单后扣减库存" },
    })
    fireEvent.click(screen.getByRole("button", { name: "新增关系" }))

    await waitFor(() =>
      expect(mocks.createRelation).toHaveBeenCalledWith(VERSION_ID, {
        fromRequirementId: REQ_A,
        toRequirementId: REQ_B,
        relationType: "depends_on",
        description: "创建订单后扣减库存",
      }),
    )
    expect(await screen.findByTestId(`relation-edge-${REL_ID}`)).toBeTruthy()

    mocks.listRelations.mockResolvedValue([updatedRelation])
    const row = screen.getByTestId(`relation-item-${REL_ID}`)
    fireEvent.click(within(row).getByRole("button", { name: "编辑关系" }))
    fireEvent.change(screen.getByLabelText("编辑关系类型"), { target: { value: "overlaps" } })
    fireEvent.click(screen.getByRole("button", { name: "保存关系" }))

    await waitFor(() =>
      expect(mocks.updateRelation).toHaveBeenCalledWith(VERSION_ID, REL_ID, {
        relationType: "overlaps",
        description: "创建订单后扣减库存",
      }),
    )
    await waitFor(() =>
      expect(screen.getByTestId(`relation-edge-${REL_ID}`).getAttribute("data-relation-type")).toBe(
        "overlaps",
      ),
    )

    const updatedRow = screen.getByTestId(`relation-item-${REL_ID}`)
    fireEvent.click(within(updatedRow).getByRole("button", { name: "删除关系" }))
    mocks.listRelations.mockResolvedValue([])
    fireEvent.click(within(updatedRow).getByRole("button", { name: "确认删除关系" }))

    await waitFor(() => expect(mocks.deleteRelation).toHaveBeenCalledWith(VERSION_ID, REL_ID))
    await waitFor(() =>
      expect(screen.getByText("暂无关系，添加关系后将在此展示关系图。")).toBeTruthy(),
    )
  })

  it("shows empty states for requirements and relations without inventing data", async () => {
    mocks.listRequirements.mockResolvedValue(page([]))
    mocks.listRelations.mockResolvedValue([])

    renderWorkbench()

    expect(await screen.findByText("暂无需求")).toBeTruthy()
    expect(screen.getByText("暂无可绘制关系的需求。")).toBeTruthy()
    expect(screen.getByText("共 0 条关系")).toBeTruthy()
  })

  it("keeps the last data and retries when the initial load fails", async () => {
    mocks.listRequirements
      .mockRejectedValueOnce(new ApiError({ status: 0, code: "network_error", message: "offline" }))
      .mockResolvedValueOnce(page([DRAFT]))

    renderWorkbench()

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("加载需求失败")

    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect((await screen.findAllByText("订单创建")).length).toBeGreaterThan(0)
  })

  it("preserves stale requirements when a manual refresh fails", async () => {
    mocks.listRequirements
      .mockResolvedValueOnce(page([DRAFT]))
      .mockRejectedValueOnce(new ApiError({ status: 500, code: "internal", message: "boom" }))

    renderWorkbench()
    await screen.findAllByText("订单创建")

    fireEvent.click(screen.getByRole("button", { name: "刷新工作台" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("加载需求失败")
    expect(screen.getAllByText("订单创建").length).toBeGreaterThan(0)
  })

  it("reloads requirements and relations after deleting a requirement so the graph stays in sync", async () => {
    mocks.listRequirements
      .mockResolvedValueOnce(page([DRAFT, CLARIFIED]))
      .mockResolvedValue(page([CLARIFIED]))
    mocks.listRelations.mockResolvedValueOnce([RELATION]).mockResolvedValue([])
    mocks.deleteRequirement.mockResolvedValue(undefined)
    const onChanged = vi.fn()

    renderWorkbench(onChanged)
    expect(await screen.findByTestId(`relation-edge-${REL_ID}`)).toBeTruthy()
    expect(screen.getByText("共 1 条关系")).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "删除需求" }))
    fireEvent.click(screen.getByRole("button", { name: "确认删除" }))

    await waitFor(() => expect(mocks.deleteRequirement).toHaveBeenCalledWith(VERSION_ID, REQ_A))
    await waitFor(() => expect(mocks.listRelations).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(mocks.listRequirements).toHaveBeenCalledTimes(2))

    await waitFor(() => {
      expect(screen.queryByTestId(`relation-item-${REL_ID}`)).toBeNull()
      expect(screen.queryByTestId(`relation-edge-${REL_ID}`)).toBeNull()
      expect(screen.getByText("共 0 条关系")).toBeTruthy()
    })
    expect(
      within(screen.getByRole("region", { name: "需求列表" })).queryByText("订单创建"),
    ).toBeNull()
    expect(onChanged).toHaveBeenCalledTimes(1)
  })

  it("asks for both relation endpoints with the requirement wording", async () => {
    renderWorkbench()
    fireEvent.click(await screen.findByRole("button", { name: "新增关系" }))

    expect(screen.getByRole("alert").textContent).toContain("请选择关系两端的需求。")
    expect(mocks.createRelation).not.toHaveBeenCalled()
  })

  it("mounts the clarification chat, question panel and decision panel", async () => {
    renderWorkbench()

    const chat = await screen.findByRole("region", { name: "澄清对话" })
    expect(screen.getByRole("region", { name: "澄清问题" })).toBeTruthy()
    expect(screen.getByRole("region", { name: "澄清决策" })).toBeTruthy()
    expect(within(chat).getByLabelText("消息内容")).toBeTruthy()
    expect(mocks.useVersionStream).toHaveBeenCalledWith(VERSION_ID)
    expect(mocks.getClarification).toHaveBeenCalledWith(VERSION_ID)
    expect(mocks.listClarificationMessages).toHaveBeenCalledWith(VERSION_ID, { limit: 50 })
    expect(mocks.listClarificationQuestions).toHaveBeenCalledWith(VERSION_ID, { limit: 100 })
    expect(mocks.listDecisions).toHaveBeenCalledWith(VERSION_ID, { limit: 100 })
  })

  it("reloads the clarification overview, panels and header when AI events arrive", async () => {
    let streamEvents: VersionEvent[] = [versionEvent(1, "requirement.added")]
    mocks.useVersionStream.mockImplementation(() => ({
      events: streamEvents,
      status: "live",
      reconnect: vi.fn(),
    }))
    mocks.getClarification.mockResolvedValue({
      session: CLARIFICATION_SESSION,
      questions: { open: 2, answered: 1, dismissed: 0 },
      decisions: { proposed: 1, accepted: 0, rejected: 0 },
    })
    const onChanged = vi.fn()
    const view = renderWorkbench(onChanged)

    await waitFor(() => expect(mocks.getClarification).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(mocks.listClarificationQuestions).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(mocks.listDecisions).toHaveBeenCalledTimes(1))
    // The mounted panels display the overview's server counters.
    expect(screen.getByRole("button", { name: "待回答 2" })).toBeTruthy()
    expect(screen.getByRole("button", { name: "待确认 1" })).toBeTruthy()

    // clarification.question_updated: question panel + overview + header reload.
    streamEvents = [...streamEvents, versionEvent(2, "clarification.question_updated")]
    mocks.listClarificationQuestions.mockResolvedValue({
      items: [CLARIFICATION_QUESTION],
      nextCursor: "",
      hasMore: false,
    })
    mocks.getClarification.mockResolvedValue({
      session: CLARIFICATION_SESSION,
      questions: { open: 1, answered: 1, dismissed: 0 },
      decisions: { proposed: 1, accepted: 0, rejected: 0 },
    })
    view.rerender(<Workbench version={VERSION} onChanged={onChanged} />)
    expect(await screen.findByText("下单后如何扣减库存？")).toBeTruthy()
    await waitFor(() => expect(mocks.getClarification).toHaveBeenCalledTimes(2))
    expect(screen.getByRole("button", { name: "待回答 1" })).toBeTruthy()

    // decision.proposed: decision panel + overview + header reload.
    streamEvents = [...streamEvents, versionEvent(3, "decision.proposed")]
    mocks.listDecisions.mockResolvedValue({
      items: [PROPOSED_DECISION],
      nextCursor: "",
      hasMore: false,
    })
    mocks.getClarification.mockResolvedValue({
      session: CLARIFICATION_SESSION,
      questions: { open: 1, answered: 1, dismissed: 0 },
      decisions: { proposed: 2, accepted: 0, rejected: 0 },
    })
    view.rerender(<Workbench version={VERSION} onChanged={onChanged} />)
    expect(await screen.findByText("支持 7 天无理由退款")).toBeTruthy()
    await waitFor(() => expect(mocks.getClarification).toHaveBeenCalledTimes(3))
    expect(screen.getByRole("button", { name: "待确认 2" })).toBeTruthy()

    // clarification.context_updated: the overview reloads as well.
    streamEvents = [...streamEvents, versionEvent(4, "clarification.context_updated")]
    view.rerender(<Workbench version={VERSION} onChanged={onChanged} />)
    await waitFor(() => expect(mocks.getClarification).toHaveBeenCalledTimes(4))

    // decision.decided: decision panel + overview reload.
    streamEvents = [...streamEvents, versionEvent(5, "decision.decided")]
    mocks.listDecisions.mockResolvedValue({
      items: [ACCEPTED_DECISION],
      nextCursor: "",
      hasMore: false,
    })
    mocks.getClarification.mockResolvedValue({
      session: CLARIFICATION_SESSION,
      questions: { open: 1, answered: 1, dismissed: 0 },
      decisions: { proposed: 1, accepted: 1, rejected: 0 },
    })
    view.rerender(<Workbench version={VERSION} onChanged={onChanged} />)
    expect(await screen.findByText("已接受 1")).toBeTruthy()
    await waitFor(() => expect(mocks.getClarification).toHaveBeenCalledTimes(5))

    await waitFor(() => expect(mocks.listClarificationQuestions).toHaveBeenCalledTimes(5))
    expect(mocks.listDecisions).toHaveBeenCalledTimes(5)
    expect(onChanged).toHaveBeenCalledTimes(4)
  })

  it("reloads the clarification overview when the session changes externally", async () => {
    let streamEvents: VersionEvent[] = [versionEvent(1, "requirement.added")]
    mocks.useVersionStream.mockImplementation(() => ({
      events: streamEvents,
      status: "live",
      reconnect: vi.fn(),
    }))
    const onChanged = vi.fn()
    const view = renderWorkbench(onChanged)

    await waitFor(() => expect(mocks.getClarification).toHaveBeenCalledTimes(1))
    expect(screen.queryByText("澄清会话已结束。")).toBeNull()

    // Another window/user started the session: the overview must refresh.
    streamEvents = [...streamEvents, versionEvent(2, "clarification.session_started")]
    view.rerender(<Workbench version={VERSION} onChanged={onChanged} />)
    await waitFor(() => expect(mocks.getClarification).toHaveBeenCalledTimes(2))

    // The session completed elsewhere: the chat reflects the new status.
    mocks.getClarification.mockResolvedValue({
      session: {
        ...CLARIFICATION_SESSION,
        status: "completed",
        endedAt: "2026-10-27T12:00:00Z",
      },
      questions: { open: 0, answered: 0, dismissed: 0 },
      decisions: { proposed: 0, accepted: 0, rejected: 0 },
    })
    streamEvents = [...streamEvents, versionEvent(3, "clarification.session_completed")]
    view.rerender(<Workbench version={VERSION} onChanged={onChanged} />)

    expect(await screen.findByText("澄清会话已结束。")).toBeTruthy()
    await waitFor(() => expect(mocks.getClarification).toHaveBeenCalledTimes(3))
    expect(onChanged).toHaveBeenCalledTimes(2)
  })

  it("refreshes the clarification panels when the workbench is reloaded manually", async () => {
    renderWorkbench()

    await waitFor(() => expect(mocks.listClarificationQuestions).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(mocks.listDecisions).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(mocks.getClarification).toHaveBeenCalledTimes(1))

    const refresh = screen.getByRole("button", { name: "刷新工作台" })
    await waitFor(() => expect((refresh as HTMLButtonElement).disabled).toBe(false))
    fireEvent.click(refresh)

    await waitFor(() => expect(mocks.listClarificationQuestions).toHaveBeenCalledTimes(2))
    expect(mocks.listDecisions).toHaveBeenCalledTimes(2)
    expect(mocks.getClarification).toHaveBeenCalledTimes(2)
  })

  it("does not offer to start a session when the clarification overview fails to load", async () => {
    mocks.getClarification.mockRejectedValue(
      new ApiError({ status: 500, code: "internal", message: "boom" }),
    )

    renderWorkbench()

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("加载澄清会话失败")
    await waitFor(() => expect(screen.queryByText("正在加载澄清会话…")).toBeNull())
    expect(screen.getByText("无法加载澄清会话状态。")).toBeTruthy()
    expect(screen.queryByRole("button", { name: "开始澄清会话" })).toBeNull()
  })
})
