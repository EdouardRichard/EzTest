import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it } from "vitest"

import { Skeleton } from "@/components/ui/Skeleton"

describe("Skeleton", () => {
  afterEach(cleanup)

  it("announces loading and renders the requested number of bars", () => {
    render(<Skeleton lines={4} label="正在加载版本…" />)

    const status = screen.getByRole("status")
    expect(status.getAttribute("aria-busy")).toBe("true")
    expect(screen.getByText("正在加载版本…")).toBeTruthy()
    expect(status.querySelectorAll("[data-skeleton-bar]").length).toBe(4)
  })

  it("defaults to three bars and a neutral loading label", () => {
    render(<Skeleton />)

    expect(screen.getByText("加载中")).toBeTruthy()
    expect(screen.getByRole("status").querySelectorAll("[data-skeleton-bar]").length).toBe(3)
  })
})
