import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import type { ApiPage } from "@/lib/api-client"

import type { QuestionBatch } from "./api"
import { MessageList } from "./MessageList"
import type { ConversationMessage } from "./types"
import { mergeQuestionCards, questionBatchFromPart, useConversation } from "./useConversation"

const api = vi.hoisted(() => ({
  answerQuestion: vi.fn(),
  downloadContent: vi.fn(),
  listMessages: vi.fn(),
  listQuestions: vi.fn(),
  postMessage: vi.fn(),
  skipQuestion: vi.fn(),
}))
vi.mock("./api", () => api)

const TIMESTAMP = "2026-11-05T00:00:00Z"
const VERSION_ID = "2001"
const CONVERSATION_ID = "4001"

function batch(overrides: Partial<QuestionBatch> = {}): QuestionBatch {
  return {
    id: "101",
    version_id: VERSION_ID,
    conversation_id: CONVERSATION_ID,
    message_id: "9002",
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
    answered_by: null,
    answered_at: null,
    skipped: false,
    created_at: TIMESTAMP,
    ...overrides,
  }
}

function textMessage(id: string, conversationId = CONVERSATION_ID): ConversationMessage {
  return {
    id,
    conversation_id: conversationId,
    role: "assistant",
    parts: [{ type: "text", text: `消息 ${id}` }],
    refs: [],
    created_at: TIMESTAMP,
  }
}

function pageOf(
  items: ConversationMessage[],
  nextCursor = "",
  hasMore = false,
): ApiPage<ConversationMessage> {
  return { items, nextCursor, hasMore }
}

const onRequirementSelect = () => {}

/**
 * Mounts the real conversation hook and the real thread: a question card can
 * only appear after `listMessages`/`listQuestions` are fetched and overlaid by
 * the hook, exactly like a page reload. No local component state is seeded.
 */
function Thread() {
  const conversation = useConversation({
    versionId: VERSION_ID,
    conversationId: CONVERSATION_ID,
  })
  return (
    <MessageList
      versionId={VERSION_ID}
      messages={conversation.messages}
      activity={null}
      onRequirementSelect={onRequirementSelect}
    />
  )
}

describe("useConversation question overlay", () => {
  beforeEach(() => {
    api.answerQuestion.mockReset()
    api.downloadContent.mockReset()
    api.listMessages.mockReset()
    api.listQuestions.mockReset()
    api.postMessage.mockReset()
    api.skipQuestion.mockReset()
  })

  afterEach(cleanup)

  it("restores a persisted pending card after mount and after a remount", async () => {
    api.listMessages.mockResolvedValue(pageOf([textMessage("9001"), textMessage("9002")]))
    api.listQuestions.mockResolvedValue([batch()])

    const first = render(<Thread />)
    const card = await screen.findByTestId("question-card")
    expect(card).toHaveAttribute("data-question-id", "101")
    expect(screen.getByRole("button", { name: "提交" })).toBeEnabled()
    expect(screen.getByTestId("pending-question-todo")).toHaveTextContent("1")
    // Identity overlay by message_id: the card is attached to the loaded
    // message 9002 instead of a synthesized extra row.
    const assistantMessages = screen.getAllByTestId("message-assistant")
    expect(assistantMessages).toHaveLength(2)
    expect(assistantMessages[1]).toContainElement(card)

    // Reload = a fresh mount of the hook/thread; the card must come back from
    // the persisted question snapshot only.
    first.unmount()
    render(<Thread />)
    expect(await screen.findByTestId("question-card")).toHaveAttribute("data-question-id", "101")
    expect(screen.getByRole("button", { name: "提交" })).toBeEnabled()
    expect(screen.getByTestId("pending-question-todo")).toHaveTextContent("1")
    expect(screen.getAllByTestId("message-assistant")).toHaveLength(2)
  })

  it("does not synthesize an unloaded terminal batch above the loaded history", async () => {
    const terminal = batch({
      id: "301",
      message_id: "3001",
      status: "answered",
      answers: [{ question_id: "q1", selected: ["a"], text: "" }],
      answered_by: "42",
      answered_at: TIMESTAMP,
    })
    api.listMessages.mockResolvedValue(pageOf([textMessage("9001"), textMessage("9002")]))
    api.listQuestions.mockResolvedValue([terminal, batch()])

    const first = render(<Thread />)
    await screen.findByTestId("question-card")
    // Only the pending batch (bound to loaded message 9002) is rendered; the
    // answered batch of an unloaded older page must not float above history.
    expect(screen.getAllByTestId("question-card")).toHaveLength(1)
    expect(screen.getByTestId("question-card")).toHaveAttribute("data-question-id", "101")
    expect(screen.getAllByTestId("message-assistant")).toHaveLength(2)

    first.unmount()
    render(<Thread />)
    await screen.findByTestId("question-card")
    expect(screen.getAllByTestId("question-card")).toHaveLength(1)
    expect(screen.getAllByTestId("message-assistant")).toHaveLength(2)
  })

  it("replaces the stale question part on the bound message without duplicating it", () => {
    const stale = {
      ...textMessage("9002"),
      parts: [
        { type: "text", text: "请回答问题" },
        {
          type: "question",
          question_id: "101",
          batch_id: "qb-1",
          questions: batch().questions,
          expires_at: batch().expires_at,
        },
      ],
    } as unknown as ConversationMessage
    const answered = batch({
      status: "answered",
      answers: [{ question_id: "q1", selected: ["a"], text: "" }],
      answered_by: "42",
      answered_at: TIMESTAMP,
    })

    const merged = mergeQuestionCards([stale], [answered])

    expect(merged).toHaveLength(1)
    expect(merged[0].id).toBe("9002")
    expect(merged[0].parts).toHaveLength(2)
    const questionParts = merged[0].parts.filter((part) => questionBatchFromPart(part) !== null)
    expect(questionParts).toHaveLength(1)
    expect(questionBatchFromPart(questionParts[0])?.status).toBe("answered")
    expect(
      merged[0].parts.filter((part) => (part as { type?: string }).type === "text"),
    ).toHaveLength(1)
  })
})
