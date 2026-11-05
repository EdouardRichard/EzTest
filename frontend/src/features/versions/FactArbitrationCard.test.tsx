import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

const api = vi.hoisted(() => ({ arbitrateFact: vi.fn() }))
vi.mock("./api", () => api)

import type { FactArbitrationResult, VersionFact } from "./api"
import { FactArbitrationCard } from "./FactArbitrationCard"

const fact: VersionFact = {
  id: "21",
  version_id: "2001",
  kind: "api",
  key: "api:env:1:GET /orders",
  payload: { method: "GET", path_template: "/orders" },
  source: "explore",
  source_ref: "run:7",
  evidence_refs: [],
  content_hash: "b".repeat(64),
  confidence: 0.4,
  superseded_by: null,
  created_at: "2026-11-01T08:00:00Z",
}

const result: FactArbitrationResult = {
  version_id: "2001",
  fact_id: "21",
  kind: "api",
  key: fact.key,
  decision: "keep",
  reason: "以探索结果为准",
  keep_id: "21",
  dropped_ids: ["22"],
  changed: true,
  current: [],
}

describe("FactArbitrationCard", () => {
  beforeEach(() => {
    api.arbitrateFact.mockReset()
  })

  afterEach(cleanup)

  it("requires a reason and keeps the selected fact", async () => {
    api.arbitrateFact.mockResolvedValue(result)
    const onArbitrated = vi.fn()

    render(
      <FactArbitrationCard
        versionId="2001"
        fact={fact}
        conflictCount={2}
        onArbitrated={onArbitrated}
      />,
    )

    const keep = screen.getByRole("button", { name: "保留此条" })
    expect(keep).toBeDisabled()

    fireEvent.change(screen.getByLabelText("仲裁原因"), {
      target: { value: "以探索结果为准" },
    })
    fireEvent.click(keep)

    await waitFor(() =>
      expect(api.arbitrateFact).toHaveBeenCalledWith("2001", "21", {
        decision: "keep",
        reason: "以探索结果为准",
      }),
    )
    await waitFor(() => expect(onArbitrated).toHaveBeenCalledWith(result))
    expect(await screen.findByTestId("fact-arbitration-notice")).toHaveTextContent("已保留")
  })

  it("drops the selected fact with the drop decision", async () => {
    api.arbitrateFact.mockResolvedValue({
      ...result,
      decision: "drop",
      keep_id: "22",
      dropped_ids: ["21"],
    })

    render(<FactArbitrationCard versionId="2001" fact={fact} conflictCount={2} />)

    fireEvent.change(screen.getByLabelText("仲裁原因"), { target: { value: "旧值已过期" } })
    fireEvent.click(screen.getByRole("button", { name: "丢弃此条" }))

    await waitFor(() =>
      expect(api.arbitrateFact).toHaveBeenCalledWith("2001", "21", {
        decision: "drop",
        reason: "旧值已过期",
      }),
    )
  })

  it("shows the API error when the fact is no longer a live conflict", async () => {
    api.arbitrateFact.mockRejectedValue(
      new ApiError({
        status: 409,
        code: "state_conflict",
        message: "该事实已解决",
        requestId: "req_arbitrate",
      }),
    )

    render(<FactArbitrationCard versionId="2001" fact={fact} conflictCount={2} />)

    fireEvent.change(screen.getByLabelText("仲裁原因"), { target: { value: "再试一次" } })
    fireEvent.click(screen.getByRole("button", { name: "保留此条" }))

    expect(await screen.findByText("该事实已解决")).toBeInTheDocument()
  })

  it("renders an already superseded fact as read-only", () => {
    render(
      <FactArbitrationCard
        versionId="2001"
        fact={{ ...fact, superseded_by: "22" }}
        conflictCount={1}
      />,
    )

    expect(screen.getByText(/已被 #22 替代/)).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "保留此条" })).not.toBeInTheDocument()
  })
})
