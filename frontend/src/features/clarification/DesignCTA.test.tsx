import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { DesignCTA } from "./DesignCTA"

describe("DesignCTA", () => {
  afterEach(cleanup)

  it("renders nothing when no clarified requirement is waiting for design", () => {
    const { container } = render(<DesignCTA versionId="2001" pendingCount={0} />)
    expect(container).toBeEmptyDOMElement()
  })

  it("links to the version design tab with the pending count", () => {
    render(<DesignCTA versionId="2001" pendingCount={3} />)

    expect(screen.getByTestId("design-cta")).toHaveTextContent("3 条需求待设计")
    const link = screen.getByRole("link", { name: "开始设计" })
    expect(link).toHaveAttribute("href", "/versions/2001?tab=design")
  })

  it("does not call any design API from the CTA", () => {
    const fetchSpy = vi.fn()
    vi.stubGlobal("fetch", fetchSpy)

    render(<DesignCTA versionId="2001" pendingCount={1} />)

    expect(fetchSpy).not.toHaveBeenCalled()
    vi.unstubAllGlobals()
  })

  it("applies the requirement gate from the CTA at requirement_review", () => {
    const onStartDesign = vi.fn()
    render(
      <DesignCTA
        versionId="2001"
        pendingCount={2}
        stage="requirement_review"
        onStartDesign={onStartDesign}
      />,
    )

    expect(screen.queryByRole("link", { name: "开始设计" })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "开始设计" }))

    expect(onStartDesign).toHaveBeenCalledTimes(1)
  })

  it("disables the gate action while it is submitting", () => {
    render(
      <DesignCTA
        versionId="2001"
        pendingCount={2}
        stage="requirement_review"
        starting
        onStartDesign={vi.fn()}
      />,
    )

    expect(screen.getByRole("button", { name: "提交需求门…" })).toBeDisabled()
  })
})
