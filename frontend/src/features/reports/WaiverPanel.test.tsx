import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import type { Requirement } from "@/features/clarification/types"

const reportsApi = vi.hoisted(() => ({
  waiveClose: vi.fn(),
  waiveRequirement: vi.fn(),
}))
vi.mock("./api", () => reportsApi)

const clarificationApi = vi.hoisted(() => ({
  listRequirements: vi.fn(),
}))
vi.mock("@/features/clarification/api", () => clarificationApi)

const admin = vi.hoisted(() => ({ value: true }))
vi.mock("@/features/auth/useIsAdmin", () => ({ useIsAdmin: () => admin.value }))

import { WaiverPanel } from "./WaiverPanel"

function requirementFixture(overrides: Partial<Requirement> = {}): Requirement {
  return {
    id: "7001",
    current_version_id: "7101",
    revision: 1,
    title: "用户登录",
    status: "clarified",
    priority: "P2",
    updated_at: "2026-11-01T00:00:00Z",
    confirmed_at: null,
    closed_at: null,
    closure: null,
    waived: false,
    ...overrides,
  }
}

function renderPanel(overrides: Partial<Parameters<typeof WaiverPanel>[0]> = {}): void {
  render(
    <WaiverPanel
      versionId="2001"
      versionStatus="running"
      closureKind="normal"
      unverified={false}
      onWaived={vi.fn()}
      {...overrides}
    />,
  )
}

describe("WaiverAdminOnly", () => {
  beforeEach(() => {
    admin.value = true
    reportsApi.waiveClose.mockResolvedValue({
      version_id: "2001",
      status: "completed",
      closure_kind: "waived",
      unverified: true,
      close_reason: "剩余风险已人工评估",
      report_id: "8101",
      report_revision: 3,
    })
    reportsApi.waiveRequirement.mockResolvedValue({ requirement_id: "7001", waived: true })
    clarificationApi.listRequirements.mockResolvedValue([requirementFixture()])
  })
  afterEach(cleanup)

  it("offers no waiver operation to a non-admin user", async () => {
    admin.value = false
    renderPanel()
    expect(screen.queryByTestId("waiver-panel")).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /豁免/ })).not.toBeInTheDocument()
    // A non-admin must not even fetch the requirement waiver list.
    expect(clarificationApi.listRequirements).not.toHaveBeenCalled()
    expect(reportsApi.waiveClose).not.toHaveBeenCalled()
  })

  it("lets an admin waive-close the version with a reason and refreshes", async () => {
    const onWaived = vi.fn()
    renderPanel({ onWaived })
    fireEvent.click(screen.getByTestId("waive-close-button"))
    const reason = screen.getByLabelText(/豁免理由/)
    fireEvent.change(reason, { target: { value: "剩余风险已人工评估" } })
    fireEvent.click(screen.getByRole("button", { name: "确认豁免关闭" }))
    await waitFor(() =>
      expect(reportsApi.waiveClose).toHaveBeenCalledWith("2001", { reason: "剩余风险已人工评估" }),
    )
    await waitFor(() => expect(onWaived).toHaveBeenCalled())
  })

  it("surfaces a waive-close rejection (e.g. execution not settled)", async () => {
    reportsApi.waiveClose.mockRejectedValue(new Error("执行尚未结算"))
    renderPanel()
    fireEvent.click(screen.getByTestId("waive-close-button"))
    fireEvent.change(screen.getByLabelText(/豁免理由/), { target: { value: "尝试豁免" } })
    fireEvent.click(screen.getByRole("button", { name: "确认豁免关闭" }))
    await waitFor(() =>
      expect(screen.getByTestId("waiver-error")).toHaveTextContent("执行尚未结算"),
    )
  })

  it("lists clarified requirements and lets an admin waive one", async () => {
    renderPanel()
    await waitFor(() => expect(clarificationApi.listRequirements).toHaveBeenCalledWith("2001"))
    const waiveButton = await screen.findByTestId("waive-requirement-7001")
    fireEvent.click(waiveButton)
    fireEvent.change(screen.getByLabelText(/豁免理由/), { target: { value: "该需求豁免" } })
    fireEvent.click(screen.getByRole("button", { name: "确认豁免需求" }))
    await waitFor(() =>
      expect(reportsApi.waiveRequirement).toHaveBeenCalledWith("2001", "7001", {
        reason: "该需求豁免",
      }),
    )
  })

  it("marks an already waived requirement and does not offer to waive it again", async () => {
    clarificationApi.listRequirements.mockResolvedValue([
      requirementFixture({ waived: true, status: "closed" }),
    ])
    renderPanel()
    await waitFor(() => expect(screen.getByTestId("requirement-waived-7001")).toBeInTheDocument())
    expect(screen.queryByTestId("waive-requirement-7001")).not.toBeInTheDocument()
  })

  it("caps the requirement waiver reason at the contract limit 2000 (not the version 500)", async () => {
    // Final fix wave minor: the service + OpenAPI allow 2000 for the requirement
    // waiver (RequirementReasonMax) but only 500 for the version waive-close. The
    // dialog must not be stricter than the contract for the requirement path.
    renderPanel()
    fireEvent.click(await screen.findByTestId("waive-requirement-7001"))
    const reason = screen.getByLabelText(/豁免理由/)
    expect(reason).toHaveAttribute("maxlength", "2000")
    expect(screen.getByText(/最多 2000 字/)).toBeInTheDocument()
    // A 600-character reason (over the version cap, within the requirement cap)
    // is accepted and submitted.
    const longReason = "需".repeat(600)
    fireEvent.change(reason, { target: { value: longReason } })
    fireEvent.click(screen.getByRole("button", { name: "确认豁免需求" }))
    await waitFor(() =>
      expect(reportsApi.waiveRequirement).toHaveBeenCalledWith("2001", "7001", {
        reason: longReason,
      }),
    )
  })

  it("caps the version waive-close reason at 500 with matching dialog text", async () => {
    renderPanel()
    fireEvent.click(screen.getByTestId("waive-close-button"))
    const reason = screen.getByLabelText(/豁免理由/)
    expect(reason).toHaveAttribute("maxlength", "500")
    expect(screen.getByText(/最多 500 字/)).toBeInTheDocument()
  })
})
