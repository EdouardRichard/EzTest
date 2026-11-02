import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { versionFixture } from "@/features/versions/test-fixtures"
import { ApiError } from "@/lib/api-client"

const versionsApi = vi.hoisted(() => ({ updateVersion: vi.fn() }))
vi.mock("@/features/versions/api", () => versionsApi)

import { AutoDesignSwitch } from "./AutoDesignSwitch"

describe("AutoDesignSwitch", () => {
  beforeEach(() => {
    versionsApi.updateVersion.mockReset()
    versionsApi.updateVersion.mockImplementation(
      (_id: string, payload: { auto_design_new_requirements?: boolean }) =>
        Promise.resolve({
          ...versionFixture,
          auto_design_new_requirements: payload.auto_design_new_requirements ?? false,
        }),
    )
  })

  afterEach(cleanup)

  it("renders the current auto-design value", () => {
    render(<AutoDesignSwitch versionId="2001" enabled={false} />)

    const toggle = screen.getByTestId("auto-design-switch")
    expect(toggle).toHaveAttribute("role", "switch")
    expect(toggle).toHaveAttribute("aria-checked", "false")
    expect(toggle).toHaveTextContent("新需求自动设计")
  })

  it("patches the version flag and reports the new value", async () => {
    const onChanged = vi.fn()
    render(<AutoDesignSwitch versionId="2001" enabled={false} onChanged={onChanged} />)

    fireEvent.click(screen.getByTestId("auto-design-switch"))

    await waitFor(() =>
      expect(versionsApi.updateVersion).toHaveBeenCalledWith("2001", {
        auto_design_new_requirements: true,
      }),
    )
    await waitFor(() =>
      expect(screen.getByTestId("auto-design-switch")).toHaveAttribute("aria-checked", "true"),
    )
    expect(onChanged).toHaveBeenCalledWith(true)
  })

  it("rolls back the optimistic toggle when the PATCH fails", async () => {
    versionsApi.updateVersion.mockRejectedValue(
      new ApiError({ status: 500, code: "internal", message: "更新失败，请重试" }),
    )
    const onChanged = vi.fn()
    render(<AutoDesignSwitch versionId="2001" enabled={false} onChanged={onChanged} />)

    fireEvent.click(screen.getByTestId("auto-design-switch"))

    expect(await screen.findByText("更新失败，请重试")).toBeInTheDocument()
    await waitFor(() =>
      expect(screen.getByTestId("auto-design-switch")).toHaveAttribute("aria-checked", "false"),
    )
    expect(onChanged).not.toHaveBeenCalled()
  })
})
