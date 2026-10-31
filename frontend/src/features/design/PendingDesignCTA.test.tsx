import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { PendingDesignCTA } from "./PendingDesignCTA"

describe("PendingDesignCTA", () => {
  afterEach(cleanup)

  it("renders nothing when nothing is waiting for design", () => {
    const { container } = render(
      <PendingDesignCTA versionId="2001" pendingCount={0} hasConversation={false} />,
    )
    expect(container).toBeEmptyDOMElement()
  })

  it("shows the pending count and creates the first design conversation", () => {
    const onCreateConversation = vi.fn()
    render(
      <PendingDesignCTA
        versionId="2001"
        pendingCount={2}
        hasConversation={false}
        onCreateConversation={onCreateConversation}
      />,
    )

    expect(screen.getByTestId("pending-design-cta")).toHaveTextContent("2 条待设计")
    fireEvent.click(screen.getByRole("button", { name: "新建设计对话" }))
    expect(onCreateConversation).toHaveBeenCalledTimes(1)
  })

  it("explains automatic joining when a design conversation already exists", () => {
    render(<PendingDesignCTA versionId="2001" pendingCount={1} hasConversation />)

    expect(screen.getByTestId("pending-design-cta")).toHaveTextContent(
      "新需求会自动并入当前设计对话",
    )
    expect(screen.queryByRole("button", { name: "新建设计对话" })).not.toBeInTheDocument()
  })

  it("links to the design tab from the requirements page", () => {
    render(
      <PendingDesignCTA
        versionId="2001"
        pendingCount={3}
        hasConversation={false}
        href="/versions/2001?tab=design"
      />,
    )

    const link = screen.getByRole("link", { name: "前往设计" })
    expect(link).toHaveAttribute("href", "/versions/2001?tab=design")
    expect(screen.queryByRole("button", { name: "新建设计对话" })).not.toBeInTheDocument()
  })
})
