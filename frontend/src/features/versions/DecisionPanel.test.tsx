import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

const mocks = vi.hoisted(() => ({
  listDecisions: vi.fn(),
  createDecision: vi.fn(),
  updateDecision: vi.fn(),
}))

vi.mock("./api", () => ({
  listDecisions: mocks.listDecisions,
  createDecision: mocks.createDecision,
  updateDecision: mocks.updateDecision,
}))

import { DecisionPanel } from "./DecisionPanel"
import type {
  ClarificationDecision,
  ClarificationDecisionCounts,
  ClarificationDecisionsPage,
  Requirement,
} from "./types"

const VERSION_ID = "1844674407370955161"
const REQ_A = "1844674407370956001"
const REQ_B = "1844674407370956002"
const QUESTION_ID = "1844674407370957101"
const DECISION_PROPOSED = "1844674407370957201"
const DECISION_ACCEPTED = "1844674407370957202"
const DECISION_REJECTED = "1844674407370957203"
const DECISION_PROPOSED_2 = "1844674407370957204"

function requirement(id: string, title: string): Requirement {
  return {
    id,
    versionId: VERSION_ID,
    title,
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
  }
}

const REQUIREMENTS = [requirement(REQ_A, "订单创建"), requirement(REQ_B, "库存扣减")]

function decision(
  overrides: Partial<ClarificationDecision> & Pick<ClarificationDecision, "id" | "topic">,
): ClarificationDecision {
  return {
    versionId: VERSION_ID,
    requirementId: null,
    questionId: null,
    decision: "",
    rationale: "",
    status: "proposed",
    source: "ai",
    proposedBy: 1,
    decidedBy: null,
    decidedAt: null,
    createdAt: "2026-10-27T10:00:00Z",
    updatedAt: "2026-10-27T10:00:00Z",
    ...overrides,
  }
}

const PROPOSED = decision({
  id: DECISION_PROPOSED,
  topic: "退款政策",
  decision: "支持 7 天无理由退款",
  rationale: "与售后 SLA 一致",
  requirementId: REQ_A,
  questionId: QUESTION_ID,
})

const PROPOSED_SECOND = decision({
  id: DECISION_PROPOSED_2,
  topic: "退款方式",
  decision: "原路退回",
  requirementId: REQ_A,
})

const ACCEPTED = decision({
  id: DECISION_ACCEPTED,
  topic: "库存扣减时机",
  decision: "下单即扣减",
  requirementId: REQ_B,
  status: "accepted",
  decidedBy: 1,
  decidedAt: "2026-10-27T11:00:00Z",
})

const REJECTED = decision({
  id: DECISION_REJECTED,
  topic: "多语言",
  decision: "支持中英双语",
  status: "rejected",
  decidedBy: 1,
  decidedAt: "2026-10-27T11:30:00Z",
})

function decisionsPage(items: ClarificationDecision[]): ClarificationDecisionsPage {
  return { items, nextCursor: "", hasMore: false }
}

function renderPanel(
  onChanged: () => void = vi.fn(),
  refreshToken = 0,
  counts?: ClarificationDecisionCounts,
) {
  return render(
    <DecisionPanel
      versionId={VERSION_ID}
      requirements={REQUIREMENTS}
      refreshToken={refreshToken}
      counts={counts}
      onChanged={onChanged}
    />,
  )
}

describe("DecisionPanel", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    mocks.listDecisions.mockResolvedValue(
      decisionsPage([PROPOSED, PROPOSED_SECOND, ACCEPTED, REJECTED]),
    )
    mocks.createDecision.mockImplementation(
      async (_versionId: string, body: { topic: string; decision: string }) =>
        decision({
          id: "1844674407370957205",
          topic: body.topic,
          decision: body.decision,
          status: "accepted",
          source: "user",
          decidedBy: 1,
          decidedAt: "2026-10-27T12:00:00Z",
        }),
    )
    mocks.updateDecision.mockImplementation(
      async (_versionId: string, decisionId: string, body: { status: string }) => ({
        ...(decisionId === DECISION_PROPOSED ? PROPOSED : PROPOSED_SECOND),
        ...body,
      }),
    )
  })

  afterEach(cleanup)

  it("lists decisions with status counts and their linked requirement and question", async () => {
    renderPanel()

    expect(await screen.findByText("支持 7 天无理由退款")).toBeTruthy()

    const proposedRow = screen.getByTestId(`decision-row-${DECISION_PROPOSED}`)
    expect(within(proposedRow).getByText("关联需求：订单创建")).toBeTruthy()
    expect(within(proposedRow).getByText(`关联问题 #${QUESTION_ID}`)).toBeTruthy()
    expect(within(proposedRow).getByText("理由：与售后 SLA 一致")).toBeTruthy()

    const filters = screen.getByRole("group", { name: "决策状态筛选" })
    expect(within(filters).getByRole("button", { name: "待确认 2" })).toBeTruthy()
    expect(within(filters).getByRole("button", { name: "已接受 1" })).toBeTruthy()
    expect(within(filters).getByRole("button", { name: "已驳回 1" })).toBeTruthy()
    expect(mocks.listDecisions).toHaveBeenCalledWith(VERSION_ID, { limit: 100 })
  })

  it("filters decisions by status", async () => {
    renderPanel()
    await screen.findByText("支持 7 天无理由退款")

    fireEvent.click(screen.getByRole("button", { name: "已驳回 1" }))

    expect(screen.getByText("支持中英双语")).toBeTruthy()
    expect(screen.queryByText("支持 7 天无理由退款")).toBeNull()
  })

  it("shows the clarification overview counters when they are provided", async () => {
    renderPanel(vi.fn(), 0, { proposed: 4, accepted: 2, rejected: 1 })

    expect(await screen.findByText("支持 7 天无理由退款")).toBeTruthy()

    const filters = screen.getByRole("group", { name: "决策状态筛选" })
    expect(within(filters).getByRole("button", { name: "全部 7" })).toBeTruthy()
    expect(within(filters).getByRole("button", { name: "待确认 4" })).toBeTruthy()
    expect(within(filters).getByRole("button", { name: "已接受 2" })).toBeTruthy()
    expect(within(filters).getByRole("button", { name: "已驳回 1" })).toBeTruthy()
  })

  it("accepts one proposed decision and rejects another", async () => {
    mocks.listDecisions
      .mockResolvedValueOnce(decisionsPage([PROPOSED, PROPOSED_SECOND]))
      .mockResolvedValueOnce(
        decisionsPage([
          { ...PROPOSED, status: "accepted", decidedBy: 1, decidedAt: "2026-10-27T12:00:00Z" },
          PROPOSED_SECOND,
        ]),
      )
      .mockResolvedValueOnce(
        decisionsPage([
          { ...PROPOSED, status: "accepted", decidedBy: 1, decidedAt: "2026-10-27T12:00:00Z" },
          {
            ...PROPOSED_SECOND,
            status: "rejected",
            decidedBy: 1,
            decidedAt: "2026-10-27T12:05:00Z",
          },
        ]),
      )
    const onChanged = vi.fn()

    renderPanel(onChanged)
    const first = await screen.findByTestId(`decision-row-${DECISION_PROPOSED}`)
    fireEvent.click(within(first).getByRole("button", { name: "接受" }))

    await waitFor(() =>
      expect(mocks.updateDecision).toHaveBeenCalledWith(VERSION_ID, DECISION_PROPOSED, {
        status: "accepted",
      }),
    )
    await waitFor(() =>
      expect(
        within(screen.getByTestId(`decision-row-${DECISION_PROPOSED}`)).getByText("已接受"),
      ).toBeTruthy(),
    )

    const second = screen.getByTestId(`decision-row-${DECISION_PROPOSED_2}`)
    fireEvent.click(within(second).getByRole("button", { name: "驳回" }))

    await waitFor(() =>
      expect(mocks.updateDecision).toHaveBeenLastCalledWith(VERSION_ID, DECISION_PROPOSED_2, {
        status: "rejected",
      }),
    )
    await waitFor(() =>
      expect(
        within(screen.getByTestId(`decision-row-${DECISION_PROPOSED_2}`)).getByText("已驳回"),
      ).toBeTruthy(),
    )
    expect(mocks.listDecisions).toHaveBeenCalledTimes(3)
    expect(onChanged).toHaveBeenCalledTimes(2)
  })

  it("records a manual decision as accepted for the selected requirement", async () => {
    const created = decision({
      id: "1844674407370957205",
      topic: "退款时效",
      decision: "T+1 到账",
      rationale: "财务确认",
      requirementId: REQ_A,
      status: "accepted",
      source: "user",
      decidedBy: 1,
      decidedAt: "2026-10-27T12:00:00Z",
    })
    mocks.listDecisions
      .mockResolvedValueOnce(decisionsPage([]))
      .mockResolvedValueOnce(decisionsPage([created]))
    const onChanged = vi.fn()

    renderPanel(onChanged)
    expect(await screen.findByText("暂无决策")).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "新增决策" }))
    fireEvent.change(screen.getByLabelText("决策主题"), { target: { value: "退款时效" } })
    fireEvent.change(screen.getByLabelText("决策内容"), { target: { value: "T+1 到账" } })
    fireEvent.change(screen.getByLabelText("决策理由"), { target: { value: "财务确认" } })
    fireEvent.change(screen.getByLabelText("关联需求"), { target: { value: REQ_A } })
    fireEvent.click(screen.getByRole("button", { name: "保存决策" }))

    await waitFor(() =>
      expect(mocks.createDecision).toHaveBeenCalledWith(VERSION_ID, {
        topic: "退款时效",
        decision: "T+1 到账",
        rationale: "财务确认",
        requirementId: REQ_A,
      }),
    )
    expect(await screen.findByText("T+1 到账")).toBeTruthy()
    expect(onChanged).toHaveBeenCalledTimes(1)
    const filters = screen.getByRole("group", { name: "决策状态筛选" })
    expect(within(filters).getByRole("button", { name: "已接受 1" })).toBeTruthy()
  })

  it("validates the manual decision fields before submitting", async () => {
    mocks.listDecisions.mockResolvedValue(decisionsPage([]))

    renderPanel()
    fireEvent.click(await screen.findByRole("button", { name: "新增决策" }))
    fireEvent.click(screen.getByRole("button", { name: "保存决策" }))

    expect((await screen.findByRole("alert")).textContent).toContain("请输入决策主题。")
    expect(mocks.createDecision).not.toHaveBeenCalled()
  })

  it("keeps the panel usable and retries when loading fails", async () => {
    mocks.listDecisions
      .mockRejectedValueOnce(new ApiError({ status: 500, code: "internal", message: "boom" }))
      .mockResolvedValueOnce(decisionsPage([PROPOSED]))

    renderPanel()

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("加载决策失败")
    fireEvent.click(within(alert).getByRole("button", { name: "重试" }))
    expect(await screen.findByText("支持 7 天无理由退款")).toBeTruthy()
  })
})
