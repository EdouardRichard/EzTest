import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { MessageList } from "@/features/clarification/MessageList"
import type { ConversationMessage } from "@/features/clarification/types"

import { CaseCard } from "./CaseCard"
import type { CaseCardPart, MessagePart } from "./types"

afterEach(cleanup)

const CASE_CARD: CaseCardPart = {
  type: "case_card",
  case_id: "7001",
  case_version_id: "7002",
  title: "退款 T+1 到账",
  status: "draft",
}

function threadMessage(part: MessagePart): ConversationMessage {
  return {
    id: "5001",
    conversation_id: "4009",
    role: "assistant",
    parts: [part],
    refs: [],
    created_at: "2026-11-03T00:00:00Z",
  }
}

describe("CaseCard", () => {
  it("renders the referenced case title, id and status", () => {
    render(<CaseCard part={CASE_CARD} onOpen={vi.fn()} />)

    const card = screen.getByTestId("message-case-card-7001")
    expect(card).toHaveTextContent("退款 T+1 到账")
    expect(card).toHaveTextContent("#7001")
    expect(card).toHaveTextContent("draft")
  })

  it("opens the referenced case through the supplied handler", () => {
    const onOpen = vi.fn()
    render(<CaseCard part={CASE_CARD} onOpen={onOpen} />)

    fireEvent.click(screen.getByTestId("message-case-card-open-7001"))

    expect(onOpen).toHaveBeenCalledWith("7001")
  })

  it("falls back to a real design deep link when no handler is supplied", () => {
    render(<CaseCard part={CASE_CARD} versionId="2001" />)

    const link = screen.getByRole("link", { name: /打开用例/ })
    expect(link).toHaveAttribute("href", "/versions/2001?tab=design&case=7001")
  })

  it("renders and opens a case_card part inside the conversation thread", () => {
    const onCaseSelect = vi.fn()
    render(
      <MessageList
        versionId="2001"
        messages={[threadMessage(CASE_CARD)]}
        activity={null}
        onRequirementSelect={vi.fn()}
        onCaseSelect={onCaseSelect}
      />,
    )

    expect(screen.getByTestId("message-case-card-7001")).toBeInTheDocument()
    fireEvent.click(screen.getByTestId("message-case-card-open-7001"))
    expect(onCaseSelect).toHaveBeenCalledWith("7001")
  })
})
