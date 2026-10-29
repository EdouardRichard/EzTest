import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it } from "vitest"

import { ReadonlyBanner } from "./ReadonlyBanner"

describe("ReadonlyBanner", () => {
  afterEach(() => {
    cleanup()
  })

  it("states that the Agent centre is read-only for the current account", () => {
    render(<ReadonlyBanner />)

    const banner = screen.getByTestId("readonly-banner")
    expect(banner).toBeTruthy()
    expect(banner.textContent).toContain("只读")
    // The banner must say what is still available, not only what is blocked.
    expect(banner.textContent).toContain("角色目录")
    expect(banner.textContent).toContain("运行观察")
  })

  it("exposes the notice to assistive technology as a status", () => {
    render(<ReadonlyBanner />)

    const banner = screen.getByRole("status")
    expect(banner.getAttribute("data-testid")).toBe("readonly-banner")
  })
})
