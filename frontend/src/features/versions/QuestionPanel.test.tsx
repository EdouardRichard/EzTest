import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

const mocks = vi.hoisted(() => ({
  listClarificationQuestions: vi.fn(),
  createClarificationQuestion: vi.fn(),
  updateClarificationQuestion: vi.fn(),
}))

vi.mock("./api", () => ({
  listClarificationQuestions: mocks.listClarificationQuestions,
  createClarificationQuestion: mocks.createClarificationQuestion,
  updateClarificationQuestion: mocks.updateClarificationQuestion,
}))

import { QuestionPanel } from "./QuestionPanel"
import type { ClarificationQuestion, ClarificationQuestionsPage, Requirement } from "./types"

const VERSION_ID = "1844674407370955161"
const REQ_A = "1844674407370956001"
const REQ_B = "1844674407370956002"
const Q_OPEN = "1844674407370957101"
const Q_ANSWERED = "1844674407370957102"
const Q_DISMISSED = "1844674407370957103"

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

function question(
  overrides: Partial<ClarificationQuestion> & Pick<ClarificationQuestion, "id" | "question">,
): ClarificationQuestion {
  return {
    versionId: VERSION_ID,
    requirementId: REQ_A,
    sessionId: null,
    status: "open",
    answer: "",
    source: "ai",
    answeredVia: "ui",
    answeredBy: null,
    answeredAt: null,
    createdBy: 1,
    createdAt: "2026-10-27T10:00:00Z",
    updatedAt: "2026-10-27T10:00:00Z",
    ...overrides,
  }
}

const OPEN_QUESTION = question({ id: Q_OPEN, question: "下单后如何扣减库存？" })
const ANSWERED_QUESTION = question({
  id: Q_ANSWERED,
  question: "是否支持部分退款？",
  status: "answered",
  answer: "支持。",
  answeredBy: 1,
  answeredAt: "2026-10-27T11:00:00Z",
})
const DISMISSED_QUESTION = question({
  id: Q_DISMISSED,
  question: "是否需要多语言？",
  requirementId: null,
  status: "dismissed",
})

function questionsPage(items: ClarificationQuestion[]): ClarificationQuestionsPage {
  return { items, nextCursor: "", hasMore: false }
}

function renderPanel(onChanged: () => void = vi.fn(), refreshToken = 0) {
  return render(
    <QuestionPanel
      versionId={VERSION_ID}
      requirements={REQUIREMENTS}
      refreshToken={refreshToken}
      onChanged={onChanged}
    />,
  )
}

describe("QuestionPanel", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    mocks.listClarificationQuestions.mockResolvedValue(
      questionsPage([OPEN_QUESTION, ANSWERED_QUESTION, DISMISSED_QUESTION]),
    )
    mocks.createClarificationQuestion.mockImplementation(
      async (_versionId: string, body: { question: string }) =>
        question({
          id: "1844674407370957104",
          question: body.question,
          source: "user",
        }),
    )
    mocks.updateClarificationQuestion.mockImplementation(
      async (
        _versionId: string,
        questionId: string,
        body: Partial<ClarificationQuestion> & { status?: ClarificationQuestion["status"] },
      ) => ({ ...(questionId === Q_OPEN ? OPEN_QUESTION : ANSWERED_QUESTION), ...body }),
    )
  })

  afterEach(cleanup)

  it("groups questions by requirement and shows counts for each status", async () => {
    renderPanel()

    expect(await screen.findByText("下单后如何扣减库存？")).toBeTruthy()
    expect(screen.getByRole("heading", { name: "订单创建" })).toBeTruthy()
    expect(screen.getByRole("heading", { name: "版本级问题" })).toBeTruthy()

    const filters = screen.getByRole("group", { name: "问题状态筛选" })
    expect(within(filters).getByRole("button", { name: "待回答 1" })).toBeTruthy()
    expect(within(filters).getByRole("button", { name: "已回答 1" })).toBeTruthy()
    expect(within(filters).getByRole("button", { name: "已驳回 1" })).toBeTruthy()
    expect(mocks.listClarificationQuestions).toHaveBeenCalledWith(VERSION_ID, { limit: 100 })
  })

  it("filters the list by status", async () => {
    renderPanel()
    await screen.findByText("下单后如何扣减库存？")

    fireEvent.click(screen.getByRole("button", { name: "已回答 1" }))

    expect(screen.getByText("是否支持部分退款？")).toBeTruthy()
    expect(screen.queryByText("下单后如何扣减库存？")).toBeNull()
    expect(screen.getByText("回答：支持。")).toBeTruthy()
  })

  it("answers an open question inline and refreshes the list and counts", async () => {
    const answered = {
      ...OPEN_QUESTION,
      status: "answered" as const,
      answer: "按下单数量扣减。",
      answeredBy: 1,
      answeredAt: "2026-10-27T12:00:00Z",
    }
    mocks.listClarificationQuestions
      .mockResolvedValueOnce(questionsPage([OPEN_QUESTION]))
      .mockResolvedValueOnce(questionsPage([answered]))
    const onChanged = vi.fn()

    renderPanel(onChanged)
    const row = await screen.findByTestId(`question-row-${Q_OPEN}`)

    fireEvent.change(within(row).getByLabelText("回答内容"), {
      target: { value: "按下单数量扣减。" },
    })
    fireEvent.click(within(row).getByRole("button", { name: "保存回答" }))

    await waitFor(() =>
      expect(mocks.updateClarificationQuestion).toHaveBeenCalledWith(VERSION_ID, Q_OPEN, {
        status: "answered",
        answer: "按下单数量扣减。",
      }),
    )
    expect(await screen.findByText("回答：按下单数量扣减。")).toBeTruthy()
    expect(mocks.listClarificationQuestions).toHaveBeenCalledTimes(2)
    expect(onChanged).toHaveBeenCalledTimes(1)

    const filters = screen.getByRole("group", { name: "问题状态筛选" })
    expect(within(filters).getByRole("button", { name: "待回答 0" })).toBeTruthy()
    expect(within(filters).getByRole("button", { name: "已回答 1" })).toBeTruthy()
  })

  it("requires an answer before saving it", async () => {
    mocks.listClarificationQuestions.mockResolvedValue(questionsPage([OPEN_QUESTION]))

    renderPanel()
    const row = await screen.findByTestId(`question-row-${Q_OPEN}`)
    fireEvent.click(within(row).getByRole("button", { name: "保存回答" }))

    expect((await screen.findByRole("alert")).textContent).toContain("请输入回答内容。")
    expect(mocks.updateClarificationQuestion).not.toHaveBeenCalled()
  })

  it("dismisses and reopens a question", async () => {
    mocks.listClarificationQuestions
      .mockResolvedValueOnce(questionsPage([OPEN_QUESTION]))
      .mockResolvedValueOnce(questionsPage([{ ...OPEN_QUESTION, status: "dismissed" }]))
      .mockResolvedValueOnce(questionsPage([OPEN_QUESTION]))

    renderPanel()
    const row = await screen.findByTestId(`question-row-${Q_OPEN}`)
    fireEvent.click(within(row).getByRole("button", { name: "驳回" }))

    await waitFor(() =>
      expect(mocks.updateClarificationQuestion).toHaveBeenCalledWith(VERSION_ID, Q_OPEN, {
        status: "dismissed",
      }),
    )

    const dismissed = await screen.findByTestId(`question-row-${Q_OPEN}`)
    fireEvent.click(within(dismissed).getByRole("button", { name: "重开" }))
    await waitFor(() =>
      expect(mocks.updateClarificationQuestion).toHaveBeenLastCalledWith(VERSION_ID, Q_OPEN, {
        status: "open",
      }),
    )
  })

  it("creates a manual question bound to a requirement", async () => {
    const created = question({
      id: "1844674407370957104",
      question: "是否需要发票？",
      source: "user",
    })
    mocks.listClarificationQuestions
      .mockResolvedValueOnce(questionsPage([]))
      .mockResolvedValueOnce(questionsPage([created]))
    const onChanged = vi.fn()

    renderPanel(onChanged)
    expect(await screen.findByText("暂无澄清问题")).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "新增问题" }))
    fireEvent.change(screen.getByLabelText("问题内容"), { target: { value: "是否需要发票？" } })
    fireEvent.change(screen.getByLabelText("关联需求"), { target: { value: REQ_A } })
    fireEvent.click(screen.getByRole("button", { name: "创建问题" }))

    await waitFor(() =>
      expect(mocks.createClarificationQuestion).toHaveBeenCalledWith(VERSION_ID, {
        question: "是否需要发票？",
        requirementId: REQ_A,
      }),
    )
    expect(await screen.findByText("是否需要发票？")).toBeTruthy()
    expect(onChanged).toHaveBeenCalledTimes(1)
  })

  it("shows an empty state after loading questions", async () => {
    mocks.listClarificationQuestions.mockResolvedValue(questionsPage([]))

    renderPanel()

    expect(await screen.findByText("暂无澄清问题")).toBeTruthy()
  })

  it("keeps the panel usable and retries when loading fails", async () => {
    mocks.listClarificationQuestions
      .mockRejectedValueOnce(new ApiError({ status: 500, code: "internal", message: "boom" }))
      .mockResolvedValueOnce(questionsPage([OPEN_QUESTION]))

    renderPanel()

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("加载问题失败")
    fireEvent.click(within(alert).getByRole("button", { name: "重试" }))
    expect(await screen.findByText("下单后如何扣减库存？")).toBeTruthy()
  })
})
