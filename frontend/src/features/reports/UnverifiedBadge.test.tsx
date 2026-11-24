import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it } from "vitest"
import "@testing-library/jest-dom/vitest"

import { UnverifiedBadge } from "./UnverifiedBadge"

describe("UnverifiedBadge", () => {
  afterEach(cleanup)

  it("shows the unverified badge for a waived version", () => {
    render(<UnverifiedBadge closureKind="waived" unverified />)
    const badge = screen.getByTestId("unverified-badge")
    expect(badge).toHaveTextContent("未验证")
    expect(badge).toHaveTextContent("unverified")
  })

  it("shows the badge whenever the version is marked unverified", () => {
    render(<UnverifiedBadge closureKind="normal" unverified />)
    expect(screen.getByTestId("unverified-badge")).toBeInTheDocument()
  })

  it("renders nothing for a normal verified closure", () => {
    const { container } = render(<UnverifiedBadge closureKind="normal" unverified={false} />)
    expect(screen.queryByTestId("unverified-badge")).not.toBeInTheDocument()
    expect(container).toBeEmptyDOMElement()
  })

  it("tolerates absent waiver fields (pre-waiver fixtures)", () => {
    render(<UnverifiedBadge />)
    expect(screen.queryByTestId("unverified-badge")).not.toBeInTheDocument()
  })
})
