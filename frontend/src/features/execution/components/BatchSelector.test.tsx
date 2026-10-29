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
    kind: "baseline",
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
    expect(screen.getByText("批次 #2 · 基线（执行中）")).toBeTruthy()

    fireEvent.change(select, { target: { value: second.id } })
    expect(onChange).toHaveBeenCalledWith(second.id)
  })

  it("shows the kind badge, parent and the supplement origin of the selected batch", () => {
    const parent = batchFixture({ id: "1844674407370955161", batch_no: 1 })
    const supplement = batchFixture({
      id: "1844674407370955163",
      batch_no: 2,
      status: "done",
      kind: "supplement",
      parentBatchId: parent.id,
    })

    render(
      <BatchSelector batches={[supplement, parent]} value={supplement.id} onChange={vi.fn()} />,
    )

    const badge = screen.getByTestId("batch-kind-badge")
    expect(badge.getAttribute("data-kind")).toBe("supplement")
    expect(badge.textContent).toContain("补全")
    expect(screen.getByTestId("batch-parent").textContent).toContain("父批次 #1")
    expect(screen.getByTestId("batch-origin").textContent).toContain("由补全 Agent 创建")

    // The option label also carries the kind so the list is scannable.
    const select = screen.getByTestId("exec-batch-selector") as HTMLSelectElement
    expect(select.options[0]?.textContent).toContain("补全")
    expect(select.options[0]?.textContent).toContain("父批次 #1")
  })

  it("reads a historical batch without a kind as the baseline", () => {
    render(<BatchSelector batches={[batchFixture({ kind: undefined })]} value="" />)

    expect(screen.getByTestId("batch-kind-badge").getAttribute("data-kind")).toBe("baseline")
    expect(screen.getByTestId("batch-kind-badge").textContent).toContain("基线")
    expect(screen.queryByTestId("batch-parent")).toBeNull()
    expect(screen.queryByTestId("batch-origin")).toBeNull()
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
