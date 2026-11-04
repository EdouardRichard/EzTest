import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

import type { QuestionBatch } from "./api"

const api = vi.hoisted(() => ({
  answerQuestion: vi.fn(),
  skipQuestion: vi.fn(),
}))
vi.mock("./api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./api")>()),
  answerQuestion: api.answerQuestion,
  skipQuestion: api.skipQuestion,
}))

import { QuestionCard } from "./QuestionCard"

function pendingBatch(overrides: Partial<QuestionBatch> = {}): QuestionBatch {
  return {
    id: "101",
    version_id: "7",
    conversation_id: "4001",
    message_id: "5001",
    kind: "clarification_question",
    status: "pending",
    batch_id: "qb-1",
    questions: [
      {
        id: "q1",
        text: "退款到账时间按 T+1 还是 T+3 验收？",
        options: [
          { id: "a", label: "T+1" },
          { id: "b", label: "T+3" },
        ],
        multiple: false,
        allow_free_text: true,
        required: true,
        source_refs: ["requirement:FR-012"],
      },
    ],
    expires_at: "2099-01-01T00:00:00Z",
    answers: [],
    answered_at: null,
    answered_by: null,
    skipped: false,
    created_at: "2026-11-05T06:00:00Z",
    ...overrides,
  }
}

const answeredBatch = (selected: string[], text = ""): QuestionBatch =>
  pendingBatch({
    status: "answered",
    answers: [{ question_id: "q1", selected, text }],
    answered_by: "42",
    answered_at: "2026-11-05T06:30:00Z",
  })

describe("QuestionCard", () => {
  beforeEach(() => {
    api.answerQuestion.mockReset()
    api.skipQuestion.mockReset()
  })

  afterEach(cleanup)

  it("renders the batch title, required marker and source refs", () => {
    render(<QuestionCard versionId="7" question={pendingBatch()} />)

    expect(screen.getByTestId("question-card")).toBeInTheDocument()
    expect(screen.getByText("澄清提问")).toBeInTheDocument()
    expect(screen.getByText("共 1 题")).toBeInTheDocument()
    expect(screen.getByText("必填")).toBeInTheDocument()
    expect(screen.getByText("退款到账时间按 T+1 还是 T+3 验收？")).toBeInTheDocument()
    expect(screen.getByText(/requirement:FR-012/)).toBeInTheDocument()
  })

  it("submits selected option and shows the answer", async () => {
    api.answerQuestion.mockResolvedValue(answeredBatch(["a"]))
    const onAnswer = vi.fn()

    render(<QuestionCard versionId="7" question={pendingBatch()} onAnswer={onAnswer} />)
    fireEvent.click(screen.getByLabelText("T+1"))
    fireEvent.click(screen.getByRole("button", { name: "提交" }))

    await waitFor(() =>
      expect(api.answerQuestion).toHaveBeenCalledWith("7", "101", [
        { question_id: "q1", selected: ["a"], text: "" },
      ]),
    )
    expect(onAnswer).toHaveBeenCalledWith({ questionId: "q1", selected: ["a"], text: "" })
    expect(await screen.findByText("已回答")).toBeInTheDocument()
    expect(screen.getByTestId("question-answer-q1")).toHaveTextContent("T+1")
    expect(screen.getByRole("button", { name: "提交" })).toBeDisabled()
    expect(screen.getByRole("button", { name: "跳过" })).toBeDisabled()
  })

  it("requires a selection when required", async () => {
    render(<QuestionCard versionId="7" question={pendingBatch()} />)

    fireEvent.click(screen.getByRole("button", { name: "提交" }))

    expect(await screen.findByText("请选择")).toBeInTheDocument()
    expect(api.answerQuestion).not.toHaveBeenCalled()
  })

  it("submits every checked option of a multiple-choice question", async () => {
    const batch = pendingBatch({
      questions: [
        {
          id: "q1",
          text: "需要覆盖哪些退款场景？",
          options: [
            { id: "a", label: "整单退款" },
            { id: "b", label: "部分退款" },
          ],
          multiple: true,
          allow_free_text: false,
          required: true,
          source_refs: [],
        },
      ],
    })
    api.answerQuestion.mockResolvedValue({
      ...batch,
      status: "answered",
      answers: [{ question_id: "q1", selected: ["a", "b"], text: "" }],
    })

    render(<QuestionCard versionId="7" question={batch} />)
    fireEvent.click(screen.getByRole("button", { name: "提交" }))
    expect(await screen.findByText("请选择")).toBeInTheDocument()
    expect(api.answerQuestion).not.toHaveBeenCalled()

    fireEvent.click(screen.getByLabelText("整单退款"))
    fireEvent.click(screen.getByLabelText("部分退款"))
    fireEvent.click(screen.getByRole("button", { name: "提交" }))

    await waitFor(() =>
      expect(api.answerQuestion).toHaveBeenCalledWith("7", "101", [
        { question_id: "q1", selected: ["a", "b"], text: "" },
      ]),
    )
  })

  it("submits free text when the question allows it", async () => {
    const batch = pendingBatch({
      questions: [
        {
          id: "q1",
          text: "补充退款失败场景的验收标准",
          options: [],
          multiple: false,
          allow_free_text: true,
          required: true,
          source_refs: [],
        },
      ],
    })
    api.answerQuestion.mockResolvedValue({
      ...batch,
      status: "answered",
      answers: [{ question_id: "q1", selected: [], text: "接口超时后需要重试并记录" }],
    })

    render(<QuestionCard versionId="7" question={batch} />)
    fireEvent.click(screen.getByRole("button", { name: "提交" }))
    expect(await screen.findByText("请填写")).toBeInTheDocument()
    expect(api.answerQuestion).not.toHaveBeenCalled()

    fireEvent.change(screen.getByLabelText(/补充说明/), {
      target: { value: "接口超时后需要重试并记录" },
    })
    fireEvent.click(screen.getByRole("button", { name: "提交" }))

    await waitFor(() =>
      expect(api.answerQuestion).toHaveBeenCalledWith("7", "101", [
        { question_id: "q1", selected: [], text: "接口超时后需要重试并记录" },
      ]),
    )
  })

  it("skips the batch and renders the cancelled state", async () => {
    api.skipQuestion.mockResolvedValue(
      pendingBatch({ status: "cancelled", skipped: true, answers: [] }),
    )

    render(<QuestionCard versionId="7" question={pendingBatch()} />)
    fireEvent.click(screen.getByRole("button", { name: "跳过" }))

    await waitFor(() => expect(api.skipQuestion).toHaveBeenCalledWith("7", "101"))
    expect(await screen.findByText("已跳过")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "提交" })).toBeDisabled()
    expect(api.answerQuestion).not.toHaveBeenCalled()
  })

  it("shows an expired question without a submit action", () => {
    render(<QuestionCard versionId="7" question={pendingBatch({ status: "expired" })} />)

    expect(screen.getByText("已过期")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "提交" })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "跳过" })).not.toBeInTheDocument()
  })

  it("treats a pending batch past its deadline as expired", () => {
    render(
      <QuestionCard
        versionId="7"
        question={pendingBatch({ expires_at: "2000-01-01T00:00:00Z" })}
      />,
    )

    expect(screen.getByText("已过期")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "提交" })).not.toBeInTheDocument()
  })

  it("renders a persisted answered batch as a read-only echo", () => {
    render(<QuestionCard versionId="7" question={answeredBatch(["b"], "T+3 更稳妥")} />)

    expect(screen.getByText("已回答")).toBeInTheDocument()
    expect(screen.getByTestId("question-answer-q1")).toHaveTextContent("T+3")
    expect(screen.getByTestId("question-answer-q1")).toHaveTextContent("T+3 更稳妥")
    expect(screen.getByRole("button", { name: "提交" })).toBeDisabled()
  })

  it("shows a submit error inline and allows retrying", async () => {
    api.answerQuestion
      .mockRejectedValueOnce(
        new ApiError({ status: 409, code: "conflict", message: "问题已关闭", retryable: true }),
      )
      .mockResolvedValueOnce(answeredBatch(["a"]))

    render(<QuestionCard versionId="7" question={pendingBatch()} />)
    fireEvent.click(screen.getByLabelText("T+1"))
    fireEvent.click(screen.getByRole("button", { name: "提交" }))

    expect(await screen.findByText("问题已关闭")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "提交" })).toBeEnabled()

    fireEvent.click(screen.getByRole("button", { name: "提交" }))
    await waitFor(() => expect(api.answerQuestion).toHaveBeenCalledTimes(2))
    expect(await screen.findByText("已回答")).toBeInTheDocument()
  })

  it("notifies the thread with the updated batch", async () => {
    const updated = answeredBatch(["a"])
    api.answerQuestion.mockResolvedValue(updated)
    const onUpdated = vi.fn()

    render(<QuestionCard versionId="7" question={pendingBatch()} onUpdated={onUpdated} />)
    fireEvent.click(screen.getByLabelText("T+1"))
    fireEvent.click(screen.getByRole("button", { name: "提交" }))

    await waitFor(() => expect(onUpdated).toHaveBeenCalledWith(updated))
  })
})
