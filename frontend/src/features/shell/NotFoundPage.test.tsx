import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it } from "vitest"
import "@testing-library/jest-dom/vitest"

import { NotFoundPage } from "./NotFoundPage"

describe("NotFoundPage", () => {
  afterEach(cleanup)

  it("renders a Chinese product 404 with a way back to the versions list", () => {
    render(<NotFoundPage />)

    expect(screen.getByTestId("not-found-page")).toBeInTheDocument()
    expect(screen.getByText("404")).toBeInTheDocument()
    expect(screen.getByRole("heading", { name: "页面不存在" })).toBeInTheDocument()
    expect(screen.getByText(/页面不存在或已被移动/)).toBeInTheDocument()
    expect(screen.getByRole("link", { name: /返回版本列表/ })).toHaveAttribute("href", "/versions")
  })
})
