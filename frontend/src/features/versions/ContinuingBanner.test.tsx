import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { ContinuingBanner } from "./ContinuingBanner"
import type { Version } from "./types"

const VERSION_ID = "1844674407370955161"

function version(overrides: Partial<Version> = {}): Version {
  return {
    id: VERSION_ID,
    name: "订单系统 2.0",
    label: "v2.0",
    description: "",
    status: "continuing",
    batch_no: 1,
    continuePhase: "clarifying",
    systemId: null,
    environmentId: null,
    codeRepositoryId: null,
    codeBranch: null,
    codeBaselineCommit: null,
    bindings: { system: null, environment: null, codeRepository: null },
    requirementCount: 4,
    clarifiedCount: 3,
    openQuestionCount: 0,
    revision: 3,
    createdBy: 1,
    updatedBy: 1,
    createdAt: "2026-10-27T10:00:00Z",
    updatedAt: "2026-10-27T10:00:00Z",
    ...overrides,
  }
}

const BATCH = {
  batch_no: 2,
  counts: {
    total: 5,
    passed: 2,
    failed: 1,
    blocked: 0,
    inconclusive: 0,
    skipped: 0,
    interrupted: 0,
  },
}

describe("ContinuingBanner", () => {
  afterEach(cleanup)

  it("shows the next batch number and the incremental clarification action", () => {
    const onClarify = vi.fn()
    render(<ContinuingBanner version={version()} onClarify={onClarify} />)

    const banner = screen.getByTestId("continuing-banner")
    expect(banner.textContent).toContain("继续中（第 2 批）")
    expect(screen.getByTestId("continue-phase-label").textContent).toContain("增量澄清")

    fireEvent.click(screen.getByRole("button", { name: "去澄清" }))
    expect(onClarify).toHaveBeenCalledTimes(1)
  })

  it("opens the incremental plan during the designing phase", () => {
    const onOpenPlan = vi.fn()
    render(
      <ContinuingBanner
        version={version({ continuePhase: "designing", batch_no: 2 })}
        onOpenPlan={onOpenPlan}
      />,
    )

    expect(screen.getByTestId("continuing-banner").textContent).toContain("继续中（第 3 批）")
    expect(screen.getByTestId("continue-phase-label").textContent).toContain("增量设计")

    fireEvent.click(screen.getByRole("button", { name: "查看增量计划" }))
    expect(onOpenPlan).toHaveBeenCalledTimes(1)
  })

  it("shows the incremental execution progress of the active batch", () => {
    render(
      <ContinuingBanner
        version={version({ continuePhase: "executing", batch_no: 1 })}
        activeBatch={BATCH}
      />,
    )

    const banner = screen.getByTestId("continuing-banner")
    expect(banner.textContent).toContain("增量执行")
    expect(banner.textContent).toContain("已执行 3/5 个用例")
  })

  it("stacks 已暂停 on top of the continuing badge", () => {
    render(<ContinuingBanner version={version({ paused: true, continuePhase: "executing" })} />)

    const banner = screen.getByTestId("continuing-banner")
    expect(banner.textContent).toContain("继续中（第 2 批）")
    expect(banner.textContent).toContain("已暂停")
    expect(screen.getByTestId("continue-phase-label").textContent).toBe("已暂停")
  })

  it("does not render for a version that is not continuing", () => {
    render(<ContinuingBanner version={version({ status: "completed", continuePhase: null })} />)
    expect(screen.queryByTestId("continuing-banner")).toBeNull()
  })

  it("reads the server-shaped VersionDTO batch_no contract and derives batch_no + 1", () => {
    // Exactly the wire shape getVersion returns: P6 fields are snake_case
    // (batch_no/pause_mode) while the P7 phase keys stay camelCase.
    const serverVersion: Version = {
      id: VERSION_ID,
      name: "订单系统 2.0",
      label: "v2.0",
      description: "",
      status: "continuing",
      paused: false,
      pause_mode: "",
      batch_no: 2,
      continuePhase: "clarifying",
      continuedAt: "2026-10-28T10:00:00Z",
      systemId: null,
      environmentId: null,
      codeRepositoryId: null,
      codeBranch: null,
      codeBaselineCommit: null,
      bindings: { system: null, environment: null, codeRepository: null },
      requirementCount: 4,
      clarifiedCount: 3,
      openQuestionCount: 0,
      revision: 3,
      createdBy: 1,
      updatedBy: 1,
      createdAt: "2026-10-27T10:00:00Z",
      updatedAt: "2026-10-27T10:00:00Z",
    }

    render(<ContinuingBanner version={serverVersion} />)

    expect(screen.getByTestId("continuing-banner").textContent).toContain("继续中（第 3 批）")
  })

  it("uses the active batch number when an incremental batch is running", () => {
    render(
      <ContinuingBanner
        version={version({ continuePhase: "executing", batch_no: 2 })}
        activeBatch={{ ...BATCH, batch_no: 2 }}
      />,
    )

    expect(screen.getByTestId("continuing-banner").textContent).toContain("继续中（第 2 批）")
  })

  it("falls back to 第 1 批 for the first continuing episode", () => {
    render(<ContinuingBanner version={version({ batch_no: undefined })} />)

    expect(screen.getByTestId("continuing-banner").textContent).toContain("继续中（第 1 批）")
  })
})
