import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  useCaseRuns: vi.fn(),
}))

vi.mock("@/features/execution/hooks/useExecution", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/features/execution/hooks/useExecution")>()
  return { ...actual, useCaseRuns: mocks.useCaseRuns }
})

// The plan panel owns its own data loading; the header test only asserts that
// the banner action opens it.
vi.mock("./IncrementalPlanPanel", () => ({
  IncrementalPlanPanel: ({ versionId }: { versionId: string }) => (
    <div data-testid="incremental-plan">plan for {versionId}</div>
  ),
}))

import type { UseExecutionActionsResult } from "@/features/execution/hooks/useExecution"
import type { Batch } from "@/features/execution/types"
import type { Version } from "./types"
import { VersionHeader, type VersionHeaderExecution } from "./VersionHeader"

const VERSION_ID = "1844674407370955161"
const ACTIVE_BATCH_ID = "1844674407370955165"

function version(overrides: Partial<Version> = {}): Version {
  return {
    id: VERSION_ID,
    name: "订单系统 2.0",
    label: "v2.0",
    description: "",
    status: "continuing",
    batch_no: 1,
    continuePhase: "designing",
    systemId: 1,
    environmentId: 2,
    codeRepositoryId: null,
    codeBranch: "main",
    codeBaselineCommit: null,
    bindings: {
      system: { id: 1, name: "订单系统", available: true },
      environment: { id: 2, name: "staging", available: true },
      codeRepository: null,
    },
    requirementCount: 4,
    clarifiedCount: 4,
    openQuestionCount: 0,
    revision: 4,
    createdBy: 1,
    updatedBy: 1,
    createdAt: "2026-10-27T10:00:00Z",
    updatedAt: "2026-10-27T10:00:00Z",
    ...overrides,
  }
}

function batchFixture(overrides: Partial<Batch> = {}): Batch {
  return {
    id: ACTIVE_BATCH_ID,
    version_id: VERSION_ID,
    batch_no: 2,
    status: "running",
    paused: false,
    pause_mode: "",
    pause_reason: "",
    baseline_hash: "a".repeat(64),
    code_baseline_commit: null,
    head_commit: null,
    counts: {
      total: 3,
      passed: 1,
      failed: 0,
      blocked: 0,
      inconclusive: 0,
      skipped: 0,
      interrupted: 0,
    },
    created_by: "1",
    started_at: "2026-10-28T00:00:00Z",
    finished_at: null,
    failure_reason: "",
    ...overrides,
  }
}

function actions(): UseExecutionActionsResult {
  return {
    createBatch: vi.fn(),
    pause: vi.fn(),
    resume: vi.fn(),
    cancel: vi.fn(),
    rerun: vi.fn(),
    exportReport: vi.fn(),
    pending: "",
    error: "",
    clearError: vi.fn(),
  }
}

function execution(activeBatch: Batch | null = null): VersionHeaderExecution {
  return {
    batches: activeBatch === null ? [] : [activeBatch],
    activeBatch,
    selectedBatchId: activeBatch?.id ?? "",
    stream: { events: [], status: "live", reconnect: vi.fn() },
    actions: actions(),
    onBatchChange: vi.fn(),
    onOpenReport: vi.fn(),
    onOpenSessions: vi.fn(),
    onOpenResults: vi.fn(),
  }
}

describe("VersionHeader continuing state", () => {
  beforeEach(() => {
    mocks.useCaseRuns.mockReturnValue({ caseRuns: [] })
  })

  afterEach(cleanup)

  it("shows the continuing badge, banner, change entry and plan action", () => {
    const onAddRequirement = vi.fn()
    const onViewChanges = vi.fn()
    render(
      <VersionHeader
        version={version()}
        refreshing={false}
        onRefresh={vi.fn()}
        onUpdated={vi.fn()}
        onAddRequirement={onAddRequirement}
        onViewChanges={onViewChanges}
        execution={execution()}
      />,
    )

    const badge = screen.getByTestId("continuing-badge")
    expect(badge.textContent).toContain("继续中（第 2 批）")

    const banner = screen.getByTestId("continuing-banner")
    expect(banner.textContent).toContain("继续中（第 2 批）")
    expect(screen.getByTestId("continue-phase-label").textContent).toContain("增量设计")

    fireEvent.click(screen.getByRole("button", { name: "新增需求" }))
    expect(onAddRequirement).toHaveBeenCalledTimes(1)

    fireEvent.click(screen.getByTestId("view-changes-btn"))
    expect(onViewChanges).toHaveBeenCalledTimes(1)

    fireEvent.click(screen.getByRole("button", { name: "查看增量计划" }))
    expect(screen.getByTestId("incremental-plan")).toBeTruthy()
  })

  it("uses the active batch number instead of batch_no + 1 while a batch is running", () => {
    render(
      <VersionHeader
        version={version({ continuePhase: "executing", batch_no: 2 })}
        refreshing={false}
        onRefresh={vi.fn()}
        onUpdated={vi.fn()}
        onAddRequirement={vi.fn()}
        execution={execution(batchFixture({ batch_no: 2 }))}
      />,
    )

    expect(screen.getByTestId("continuing-badge").textContent).toContain("继续中（第 2 批）")
    expect(screen.getByTestId("continuing-banner").textContent).toContain("继续中（第 2 批）")
  })

  it("maps the server-shaped VersionDTO keys batch_no/pause_mode without renaming", () => {
    // getVersion returns the wire DTO verbatim: the P6 fields are snake_case.
    const serverVersion: Version = {
      id: VERSION_ID,
      name: "订单系统 2.0",
      label: "v2.0",
      description: "",
      status: "continuing",
      paused: true,
      pause_mode: "graceful",
      batch_no: 3,
      continuePhase: "clarifying",
      continuedAt: null,
      systemId: null,
      environmentId: null,
      codeRepositoryId: null,
      codeBranch: null,
      codeBaselineCommit: null,
      bindings: { system: null, environment: null, codeRepository: null },
      requirementCount: 4,
      clarifiedCount: 4,
      openQuestionCount: 0,
      revision: 5,
      createdBy: 1,
      updatedBy: 1,
      createdAt: "2026-10-27T10:00:00Z",
      updatedAt: "2026-10-27T10:00:00Z",
    }

    render(
      <VersionHeader
        version={serverVersion}
        refreshing={false}
        onRefresh={vi.fn()}
        onUpdated={vi.fn()}
        onAddRequirement={vi.fn()}
      />,
    )

    expect(screen.getByTestId("continuing-badge").textContent).toContain("继续中（第 4 批）")
  })

  it("offers the add-requirement entry but no continuing badge on a completed version", () => {
    const onAddRequirement = vi.fn()
    render(
      <VersionHeader
        version={version({ status: "completed", continuePhase: null, batch_no: 2 })}
        refreshing={false}
        onRefresh={vi.fn()}
        onUpdated={vi.fn()}
        onAddRequirement={onAddRequirement}
        execution={execution()}
      />,
    )

    expect(screen.queryByTestId("continuing-badge")).toBeNull()
    expect(screen.queryByTestId("continuing-banner")).toBeNull()
    expect(screen.queryByTestId("view-changes-btn")).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "新增需求" }))
    expect(onAddRequirement).toHaveBeenCalledTimes(1)
  })

  it("disables pause when the continuing version has no active batch", () => {
    render(
      <VersionHeader
        version={version()}
        refreshing={false}
        onRefresh={vi.fn()}
        onUpdated={vi.fn()}
        onAddRequirement={vi.fn()}
        execution={execution()}
      />,
    )

    const pause = screen.getByTestId("exec-pause-btn") as HTMLButtonElement
    expect(pause.disabled).toBe(true)
  })

  it("enables pause while the continuing version has a running batch", () => {
    render(
      <VersionHeader
        version={version({ continuePhase: "executing" })}
        refreshing={false}
        onRefresh={vi.fn()}
        onUpdated={vi.fn()}
        onAddRequirement={vi.fn()}
        execution={execution(batchFixture())}
      />,
    )

    const pause = screen.getByTestId("exec-pause-btn") as HTMLButtonElement
    expect(pause.disabled).toBe(false)
    expect(screen.getByTestId("continuing-banner").textContent).toContain("增量执行")
  })

  it("stacks the paused overlay and only offers the add-requirement entry", () => {
    render(
      <VersionHeader
        version={version({ paused: true, continuePhase: "executing" })}
        refreshing={false}
        onRefresh={vi.fn()}
        onUpdated={vi.fn()}
        onAddRequirement={vi.fn()}
        execution={execution(batchFixture({ paused: true, pause_mode: "graceful" }))}
      />,
    )

    expect(screen.getByTestId("continuing-banner").textContent).toContain("已暂停")
    expect(screen.getByRole("button", { name: "新增需求" })).toBeTruthy()
    // The paused banner owns resume; pause is not offered a second time.
    expect(screen.queryByTestId("exec-pause-btn")).toBeNull()
    expect(screen.getByTestId("exec-resume-btn")).toBeTruthy()
  })

  it("hides the continuing and add-requirement entries before completion", () => {
    render(
      <VersionHeader
        version={version({ status: "designing", continuePhase: null })}
        refreshing={false}
        onRefresh={vi.fn()}
        onUpdated={vi.fn()}
        onAddRequirement={vi.fn()}
        execution={execution()}
      />,
    )

    expect(screen.queryByTestId("continuing-badge")).toBeNull()
    expect(screen.queryByTestId("add-requirement-btn")).toBeNull()
    expect(screen.queryByTestId("view-changes-btn")).toBeNull()
  })
})
