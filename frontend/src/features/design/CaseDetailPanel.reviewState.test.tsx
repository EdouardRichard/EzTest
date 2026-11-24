import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const api = vi.hoisted(() => ({
  getDesignCase: vi.fn(),
  rollbackCaseVersion: vi.fn(),
  overrideJudgment: vi.fn(),
  listJudgments: vi.fn(),
}))
vi.mock("./api", () => api)

import { CaseDetailPanel } from "./CaseDetailPanel"
import { designCaseDetailFixture } from "./test-fixtures"

function renderPanel() {
  render(
    <CaseDetailPanel
      versionId="2001"
      caseId="7001"
      onConfirm={vi.fn()}
      onRequestChange={vi.fn()}
    />,
  )
}

describe("CaseDetailPanel review_state badge", () => {
  beforeEach(() => {
    api.getDesignCase.mockReset()
    api.rollbackCaseVersion.mockReset()
  })
  afterEach(cleanup)

  it("renders the current case version review_state badge", async () => {
    api.getDesignCase.mockResolvedValue({
      ...designCaseDetailFixture,
      review_state: "escalated_fail",
    })
    renderPanel()
    const badge = await screen.findByTestId("case-detail-review-state")
    expect(badge).toHaveTextContent("升级失败")
    expect(badge).toHaveAttribute("data-tone", "danger")
  })

  it("shows pass distinctly from a second-round failure", async () => {
    api.getDesignCase.mockResolvedValue({ ...designCaseDetailFixture, review_state: "pass" })
    renderPanel()
    const badge = await screen.findByTestId("case-detail-review-state")
    expect(badge).toHaveTextContent("审查通过")
    expect(badge).toHaveAttribute("data-tone", "success")
  })

  it("tolerates the degraded empty review_state", async () => {
    api.getDesignCase.mockResolvedValue({ ...designCaseDetailFixture, review_state: "" })
    renderPanel()
    const badge = await screen.findByTestId("case-detail-review-state")
    expect(badge).toHaveTextContent("审查未知")
  })

  it("omits the badge when review_state is absent (older fixture)", async () => {
    api.getDesignCase.mockResolvedValue(designCaseDetailFixture)
    renderPanel()
    await screen.findByText("退款 T+1 到账")
    expect(screen.queryByTestId("case-detail-review-state")).toBeNull()
  })
})

// Whole-branch review minor: JudgmentReport's single-case mode had no production
// caller (DoD "no unwired code"). The case detail is its natural home — the
// review_state badge plus the case-level human override, gated exactly as the
// report mode is (admin AND escalated_fail / judge_unavailable).
describe("CaseDetailPanel case-level judgment override", () => {
  beforeEach(() => {
    api.getDesignCase.mockReset()
    api.rollbackCaseVersion.mockReset()
    api.overrideJudgment.mockReset()
    api.listJudgments.mockReset()
  })
  afterEach(cleanup)

  function renderDetail(reviewState: string, readOnly: boolean, onJudgmentOverridden = vi.fn()) {
    api.getDesignCase.mockResolvedValue({ ...designCaseDetailFixture, review_state: reviewState })
    render(
      <CaseDetailPanel
        versionId="2001"
        caseId="7001"
        readOnly={readOnly}
        onConfirm={vi.fn()}
        onRequestChange={vi.fn()}
        onJudgmentOverridden={onJudgmentOverridden}
      />,
    )
    return onJudgmentOverridden
  }

  it("offers the human override on an escalated_fail case for an admin", async () => {
    const onOverridden = renderDetail("escalated_fail", false)
    await screen.findByTestId("case-detail-review-state")
    fireEvent.click(screen.getByTestId("override-btn"))
    fireEvent.change(screen.getByLabelText(/决断原因/), { target: { value: "人工复核覆盖后放行" } })
    fireEvent.click(screen.getByTestId("override-submit"))
    await waitFor(() =>
      expect(api.overrideJudgment).toHaveBeenCalledWith("2001", "7001", "人工复核覆盖后放行"),
    )
    await waitFor(() => expect(onOverridden).toHaveBeenCalled())
  })

  it("offers the override on a judge_unavailable case", async () => {
    renderDetail("judge_unavailable", false)
    await screen.findByTestId("case-detail-review-state")
    expect(screen.getByTestId("override-btn")).toBeInTheDocument()
  })

  it("never offers the override to a non-admin", async () => {
    renderDetail("escalated_fail", true)
    await screen.findByTestId("case-detail-review-state")
    expect(screen.queryByTestId("override-btn")).toBeNull()
  })

  it("never offers the override on a pass / pending case", async () => {
    renderDetail("pass", false)
    await screen.findByTestId("case-detail-review-state")
    expect(screen.queryByTestId("override-btn")).toBeNull()
  })
})
