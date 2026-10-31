import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { DesignCTA } from "./DesignCTA"

describe("DesignCTA", () => {
  afterEach(cleanup)

  it("renders nothing when no clarified requirement is waiting for design", () => {
    const { container } = render(<DesignCTA pendingCount={0} />)
    expect(container).toBeEmptyDOMElement()
  })

  it("shows the P2 design placeholder with a disabled action", () => {
    render(<DesignCTA pendingCount={3} />)

    expect(screen.getByTestId("design-cta")).toHaveTextContent("3 条需求待设计 · P2 提供设计对话")
    const action = screen.getByRole("button", { name: /开始设计/ })
    expect(action).toBeDisabled()
    expect(action).toHaveAttribute("title", "P2 提供设计对话")
  })

  it("does not call any design API (P1 renders the placeholder only)", () => {
    const fetchSpy = vi.fn()
    vi.stubGlobal("fetch", fetchSpy)

    render(<DesignCTA pendingCount={1} />)

    expect(fetchSpy).not.toHaveBeenCalled()
    vi.unstubAllGlobals()
  })
})
