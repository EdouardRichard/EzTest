import { cleanup, render, screen } from "@testing-library/react"
import { FlaskConical } from "lucide-react"
import { afterEach, describe, expect, it } from "vitest"

import { EmptyState } from "@/components/ui/EmptyState"
import { StatusBadge } from "@/components/ui/StatusBadge"

describe("shared UI primitives", () => {
  afterEach(() => {
    cleanup()
  })

  it("renders the status label so meaning does not depend on colour", () => {
    render(<StatusBadge variant="success">正常</StatusBadge>)

    expect(screen.getByText("正常")).toBeTruthy()
  })

  it("renders an empty state with its action", () => {
    render(
      <EmptyState
        icon={FlaskConical}
        title="P3 开放"
        description="稍后提供"
        action={<a href="/settings">前往设置</a>}
      />,
    )

    expect(screen.getByRole("heading", { name: "P3 开放" })).toBeTruthy()
    expect(screen.getByText("稍后提供")).toBeTruthy()
    expect(screen.getByRole("link", { name: "前往设置" })).toBeTruthy()
  })
})
