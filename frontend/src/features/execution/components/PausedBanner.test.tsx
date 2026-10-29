import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import type { Batch } from "../types"

import { PausedBanner } from "./PausedBanner"

const BATCH_ID = "1844674407370955162"

function batchFixture(overrides: Partial<Batch> = {}): Batch {
  return {
    id: BATCH_ID,
    version_id: "1844674407370955161",
    batch_no: 2,
    status: "running",
    paused: true,
    pause_mode: "graceful",
    pause_reason: "",
    baseline_hash: "a".repeat(64),
    code_baseline_commit: null,
    head_commit: null,
    counts: {
      total: 5,
      passed: 2,
      failed: 1,
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

afterEach(() => {
  cleanup()
})

describe("PausedBanner", () => {
  it("shows the pause mode and case progress", () => {
    render(<PausedBanner batch={batchFixture()} onResume={vi.fn()} />)

    expect(screen.getByTestId("paused-banner")).toBeTruthy()
    expect(screen.getAllByText(/优雅暂停/).length).toBeGreaterThan(0)
    expect(screen.getByText(/已完成 3\/5 个用例/)).toBeTruthy()
    expect(screen.getByTestId("exec-resume-btn")).toBeTruthy()
  })

  it("reports an immediate pause and the interrupted count", () => {
    render(
      <PausedBanner
        batch={batchFixture({
          pause_mode: "immediate",
          counts: {
            total: 4,
            passed: 1,
            failed: 0,
            blocked: 0,
            inconclusive: 0,
            skipped: 0,
            interrupted: 2,
          },
        })}
        onResume={vi.fn()}
      />,
    )

    expect(screen.getAllByText(/立即暂停/).length).toBeGreaterThan(0)
    expect(screen.getByText(/已中断 2 个用例/)).toBeTruthy()
  })

  it("resumes through the banner action", () => {
    const onResume = vi.fn()
    render(<PausedBanner batch={batchFixture()} onResume={onResume} />)

    fireEvent.click(screen.getByTestId("exec-resume-btn"))
    expect(onResume).toHaveBeenCalledTimes(1)
  })

  it("disables resume while the request is in flight", () => {
    render(<PausedBanner batch={batchFixture()} busy onResume={vi.fn()} />)

    expect((screen.getByTestId("exec-resume-btn") as HTMLButtonElement).disabled).toBe(true)
  })

  it("renders the pause reason when the server supplied one", () => {
    render(<PausedBanner batch={batchFixture({ pause_reason: "restart" })} onResume={vi.fn()} />)

    expect(screen.getByText(/restart/)).toBeTruthy()
  })
})
