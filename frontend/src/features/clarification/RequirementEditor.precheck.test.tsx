import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const api = vi.hoisted(() => ({
  updateRequirement: vi.fn(),
  confirmRequirement: vi.fn(),
  reopenRequirement: vi.fn(),
  rollbackRequirement: vi.fn(),
  getAttachmentPendingDetails: vi.fn(),
}))
vi.mock("./api", () => api)

const settingsApi = vi.hoisted(() => ({ getSetting: vi.fn() }))
vi.mock("@/features/settings/api", () => settingsApi)

import { RequirementEditor } from "./RequirementEditor"
import { confirmedRequirementDetailFixture, requirementDetailFixture } from "./test-fixtures"

function mockRules(terms: string[], minChars: number) {
  settingsApi.getSetting.mockImplementation((key: string) => {
    if (key === "validation.placeholder_terms") return Promise.resolve({ value: terms })
    if (key === "validation.min_description_chars") return Promise.resolve({ value: minChars })
    return Promise.reject(new Error("unknown key"))
  })
}

function renderEditor() {
  render(
    <RequirementEditor
      versionId="2001"
      requirement={requirementDetailFixture}
      onUpdated={vi.fn()}
    />,
  )
}

describe("RequirementEditor confirmation precheck (settings-driven)", () => {
  beforeEach(() => {
    for (const fn of Object.values(api)) fn.mockReset()
    settingsApi.getSetting.mockReset()
    api.confirmRequirement.mockResolvedValue({
      confirmed: true,
      requirement: confirmedRequirementDetailFixture,
    })
  })
  afterEach(cleanup)

  it("displays the server rules read from settings (no hard-coded list)", async () => {
    mockRules(["TBD", "待定"], 20)
    renderEditor()
    const hint = await screen.findByTestId("editor-confirm-rules")
    expect(hint).toHaveTextContent("TBD")
    expect(hint).toHaveTextContent("待定")
    expect(hint).toHaveTextContent("20")
  })

  it("blocks confirm on a placeholder title and does not call the API", async () => {
    mockRules(["TBD", "TODO"], 1)
    renderEditor()
    await screen.findByTestId("editor-confirm-rules")

    fireEvent.change(screen.getByTestId("editor-title"), { target: { value: "TBD" } })
    fireEvent.click(screen.getByTestId("editor-confirm"))

    await waitFor(() => expect(screen.getByText(/不能是占位词/)).toBeInTheDocument())
    expect(api.confirmRequirement).not.toHaveBeenCalled()
  })

  it("blocks confirm on a too-short sut_description per the settings floor", async () => {
    mockRules([], 20)
    renderEditor()
    await screen.findByTestId("editor-confirm-rules")

    // fixture sut_description "支付网关退款链路" is 8 runes < 20
    fireEvent.click(screen.getByTestId("editor-confirm"))

    await waitFor(() => expect(screen.getByText(/至少 20 字/)).toBeInTheDocument())
    expect(api.confirmRequirement).not.toHaveBeenCalled()
  })

  it("confirms a valid requirement once the precheck passes", async () => {
    mockRules(["TBD"], 1)
    renderEditor()
    await screen.findByTestId("editor-confirm-rules")

    fireEvent.click(screen.getByTestId("editor-confirm"))
    await waitFor(() => expect(api.confirmRequirement).toHaveBeenCalledWith("2001", "6001"))
  })

  it("skips the precheck when settings are unreadable (non-admin); server stays authoritative", async () => {
    settingsApi.getSetting.mockRejectedValue(new Error("forbidden"))
    renderEditor()
    await screen.findByTestId("editor-confirm")

    fireEvent.change(screen.getByTestId("editor-title"), { target: { value: "TBD" } })
    fireEvent.click(screen.getByTestId("editor-confirm"))

    // No client gate without rules: the request goes to the authoritative server.
    await waitFor(() => expect(api.confirmRequirement).toHaveBeenCalled())
    expect(screen.queryByTestId("editor-confirm-rules")).toBeNull()
  })

  it("surfaces a notice instead of silently coercing a malformed placeholder-terms setting", async () => {
    // H-F10 #44: a successful read whose value is not a string array (settings
    // TypeJSON accepts any JSON) must NOT be silently coerced to [] — the skipped
    // precheck is reported to the user (宪法七.6 不静默降级).
    settingsApi.getSetting.mockImplementation((key: string) => {
      if (key === "validation.placeholder_terms") return Promise.resolve({ value: "TBD" })
      if (key === "validation.min_description_chars") return Promise.resolve({ value: 20 })
      return Promise.reject(new Error("unknown key"))
    })
    renderEditor()

    const notice = await screen.findByTestId("editor-confirm-rules-invalid")
    expect(notice).toHaveTextContent("已跳过增量判断")
    // P31: the operator-facing notice must not leak internal setting keys.
    expect(notice.textContent).not.toContain("validation.")
    // No rules are applied, so the confirm-rule hint is not shown …
    expect(screen.queryByTestId("editor-confirm-rules")).toBeNull()
    // … and the precheck is skipped: the authoritative server still gates.
    fireEvent.change(screen.getByTestId("editor-title"), { target: { value: "TBD" } })
    fireEvent.click(screen.getByTestId("editor-confirm"))
    await waitFor(() => expect(api.confirmRequirement).toHaveBeenCalled())
  })

  it("surfaces a notice instead of silently zeroing a malformed min-length setting", async () => {
    // JSON cannot carry NaN; emulate the malformed shape with a string value.
    settingsApi.getSetting.mockImplementation((key: string) => {
      if (key === "validation.placeholder_terms") return Promise.resolve({ value: ["TBD"] })
      if (key === "validation.min_description_chars") return Promise.resolve({ value: "20" })
      return Promise.reject(new Error("unknown key"))
    })
    renderEditor()

    const notice = await screen.findByTestId("editor-confirm-rules-invalid")
    expect(notice).toHaveTextContent("已跳过增量判断")
    expect(notice.textContent).not.toContain("validation.")
    expect(screen.queryByTestId("editor-confirm-rules")).toBeNull()
  })

  it("does not flag a valid rules shape", async () => {
    mockRules(["TBD"], 20)
    renderEditor()
    await screen.findByTestId("editor-confirm-rules")
    expect(screen.queryByTestId("editor-confirm-rules-invalid")).toBeNull()
  })
})
