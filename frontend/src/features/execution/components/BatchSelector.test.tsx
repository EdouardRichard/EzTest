import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import type { Batch } from "../types"

import { BatchSelector } from "./BatchSelector"

function batchFixture(overrides: Partial<Batch> = {}): Batch {
  return {
    id: "1844674407370955162",
    version_id: "1844674407370955161",
    batch_no: 1,
    status: "done",
    paused: false,
    pause_mode: "",
    pause_reason: "",
    baseline_hash: "a".repeat(64),
    code_baseline_commit: null,
    head_commit: null,
    counts: {
      total: 2,
      passed: 2,
      failed: 0,
      blocked: 0,
      inconclusive: 0,
      skipped: 0,
      interrupted: 0,
    },
    created_by: "1",
    started_at: "2026-10-28T00:00:00Z",
    finished_at: "2026-10-28T00:10:00Z",
    failure_reason: "",
    ...overrides,
  }
}

afterEach(() => {
  cleanup()
})

describe("BatchSelector", () => {
  it("lists batches and reports the selected id", () => {
    const onChange = vi.fn()
    const first = batchFixture()
    const second = batchFixture({
      id: "1844674407370955163",
      batch_no: 2,
      status: "running",
    })

    render(<BatchSelector batches={[second, first]} value={first.id} onChange={onChange} />)

    const select = screen.getByTestId("exec-batch-selector") as HTMLSelectElement
    expect(select.value).toBe(first.id)
    expect(screen.getByText("批次 #2（执行中）")).toBeTruthy()

    fireEvent.change(select, { target: { value: second.id } })
    expect(onChange).toHaveBeenCalledWith(second.id)
  })

  it("renders nothing when there is no batch to select", () => {
    render(<BatchSelector batches={[]} value="" onChange={vi.fn()} />)

    expect(screen.queryByTestId("exec-batch-selector")).toBeNull()
  })

  it("disables the selector while loading", () => {
    render(<BatchSelector batches={[batchFixture()]} value="" loading onChange={vi.fn()} />)

    expect((screen.getByTestId("exec-batch-selector") as HTMLSelectElement).disabled).toBe(true)
  })
})
