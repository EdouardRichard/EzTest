import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import type { QuestionBatch } from "./api"
import { MessageList } from "./MessageList"
import type { ConversationMessage, MessagePart } from "./types"
import { mergeQuestionCards } from "./useConversation"

const api = vi.hoisted(() => ({
  downloadContent: vi.fn(),
  answerQuestion: vi.fn(),
  skipQuestion: vi.fn(),
}))
vi.mock("./api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./api")>()),
  downloadContent: api.downloadContent,
  answerQuestion: api.answerQuestion,
  skipQuestion: api.skipQuestion,
}))

function questionBatch(overrides: Partial<QuestionBatch> = {}): QuestionBatch {
  return {
    id: "101",
    version_id: "2001",
    conversation_id: "2",
    message_id: "6001",
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
        allow_free_text: false,
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

/** Wire shape written by the Task 23 question message payload. */
function questionMessage(batch: QuestionBatch): ConversationMessage {
  return {
    id: batch.message_id ?? "6001",
    conversation_id: "2",
    role: "assistant",
    parts: [
      {
        type: "question",
        question_id: batch.id,
        batch_id: batch.batch_id,
        questions: batch.questions,
        expires_at: batch.expires_at,
      },
    ],
    refs: [],
    created_at: "2026-11-05T06:00:00Z",
  } as unknown as ConversationMessage
}

const SHA = "e".repeat(64)

function userMessage(parts: MessagePart[]): ConversationMessage {
  return {
    id: "1",
    conversation_id: "2",
    role: "user",
    parts,
    refs: [],
    created_at: "2026-11-01T00:00:00Z",
  }
}

describe("MessageList", () => {
  beforeEach(() => {
    api.downloadContent.mockReset()
    api.answerQuestion.mockReset()
    api.skipQuestion.mockReset()
    vi.stubGlobal(
      "URL",
      Object.assign(URL, {
        createObjectURL: vi.fn(() => "blob:image"),
        revokeObjectURL: vi.fn(),
      }),
    )
  })

  afterEach(cleanup)

  it("renders an image part through the authenticated content download", async () => {
    api.downloadContent.mockResolvedValue({
      blob: new Blob(["png"], { type: "image/png" }),
      filename: "",
      contentType: "image/png",
    })

    render(
      <MessageList
        versionId="2001"
        messages={[
          userMessage([
            { type: "image", mime: "image/png", data_ref: SHA, width: 800, height: 600 },
          ]),
        ]}
        activity={null}
        onRequirementSelect={vi.fn()}
      />,
    )

    const image = await screen.findByTestId("message-image")
    expect(api.downloadContent).toHaveBeenCalledWith(SHA)
    expect(image).toHaveAttribute("src", "blob:image")
    expect(image).toHaveAttribute("width", "800")
    expect(image).toHaveAttribute("height", "600")
  })

  it("shows a readable error when the image content cannot be loaded", async () => {
    api.downloadContent.mockRejectedValue(new Error("content unavailable"))

    render(
      <MessageList
        versionId="2001"
        messages={[
          userMessage([
            { type: "image", mime: "image/png", data_ref: SHA, width: 800, height: 600 },
          ]),
        ]}
        activity={null}
        onRequirementSelect={vi.fn()}
      />,
    )

    expect(await screen.findByText("图片加载失败")).toBeInTheDocument()
  })

  it("renders a question part as an interactive card in the thread", async () => {
    const batch = questionBatch()
    api.answerQuestion.mockResolvedValue({
      ...batch,
      status: "answered",
      answers: [{ question_id: "q1", selected: ["a"], text: "" }],
    })

    render(
      <MessageList
        versionId="2001"
        messages={[questionMessage(batch)]}
        activity={null}
        onRequirementSelect={vi.fn()}
      />,
    )

    expect(screen.getByTestId("question-card")).toBeInTheDocument()
    expect(screen.getByText("退款到账时间按 T+1 还是 T+3 验收？")).toBeInTheDocument()
    expect(screen.getByTestId("pending-question-todo")).toHaveTextContent("1")

    fireEvent.click(screen.getByLabelText("T+1"))
    fireEvent.click(screen.getByRole("button", { name: "提交" }))

    expect(await screen.findByText("已回答")).toBeInTheDocument()
    await waitFor(() =>
      expect(screen.queryByTestId("pending-question-todo")).not.toBeInTheDocument(),
    )
    expect(api.answerQuestion).toHaveBeenCalledWith("2001", "101", [
      { question_id: "q1", selected: ["a"], text: "" },
    ])
  })

  it("renders a persisted pending question merged from the question state overlay", async () => {
    const batch = questionBatch()
    // Overlay branch for a pending batch whose bound message is not in this page
    // (the hook-driven reload path is covered in useConversation.questions.test).
    const messages = mergeQuestionCards([], [batch])

    render(
      <MessageList
        versionId="2001"
        messages={messages}
        activity={null}
        onRequirementSelect={vi.fn()}
      />,
    )

    expect(screen.getByTestId("question-card")).toBeInTheDocument()
    expect(screen.getByText("退款到账时间按 T+1 还是 T+3 验收？")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "提交" })).toBeEnabled()
    expect(screen.getByTestId("pending-question-todo")).toHaveTextContent("1")
  })

  it("lets a locally answered card become answerable again when a reopened batch arrives", async () => {
    const batch = questionBatch()
    api.answerQuestion.mockResolvedValue({
      ...batch,
      status: "answered",
      answers: [{ question_id: "q1", selected: ["a"], text: "" }],
      answered_by: "42",
      answered_at: "2026-11-05T06:30:00Z",
    })

    const { rerender } = render(
      <MessageList
        versionId="2001"
        messages={[questionMessage(batch)]}
        activity={null}
        onRequirementSelect={vi.fn()}
      />,
    )
    fireEvent.click(screen.getByLabelText("T+1"))
    fireEvent.click(screen.getByRole("button", { name: "提交" }))
    expect(await screen.findByText("已回答")).toBeInTheDocument()
    await waitFor(() =>
      expect(screen.queryByTestId("pending-question-todo")).not.toBeInTheDocument(),
    )

    // The Host reopened the question: a newer authoritative pending batch (new
    // deadline, cleared answers) replaces the local answer echo. The stale
    // local override must be discarded, so the card is answerable and counted.
    const reopened = questionBatch({ expires_at: "2099-06-01T00:00:00Z", answers: [] })
    rerender(
      <MessageList
        versionId="2001"
        messages={[questionMessage(reopened)]}
        activity={null}
        onRequirementSelect={vi.fn()}
      />,
    )

    expect(await screen.findByText("待回答")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "提交" })).toBeEnabled()
    expect(screen.getByTestId("pending-question-todo")).toHaveTextContent("1")
  })

  it("reads a question card from a nested part payload", () => {
    const batch = questionBatch({ id: "102", message_id: "6100", batch_id: "qb-2" })
    const message = {
      id: "6100",
      conversation_id: "2",
      role: "assistant",
      parts: [
        {
          type: "question",
          payload: {
            question_id: "102",
            batch_id: "qb-2",
            questions: batch.questions,
            expires_at: batch.expires_at,
          },
        },
      ],
      refs: [],
      created_at: "2026-11-05T06:00:00Z",
    } as unknown as ConversationMessage

    render(
      <MessageList
        versionId="2001"
        messages={[message]}
        activity={null}
        onRequirementSelect={vi.fn()}
      />,
    )

    expect(screen.getByTestId("question-card")).toHaveAttribute("data-question-id", "102")
    expect(screen.getByTestId("pending-question-todo")).toHaveTextContent("1")
  })
})
