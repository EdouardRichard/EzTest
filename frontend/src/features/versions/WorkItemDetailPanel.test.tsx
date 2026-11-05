import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

const api = vi.hoisted(() => ({ getWorkItem: vi.fn() }))
vi.mock("./api", () => api)

import type { WorkItem } from "./api"
import { WorkItemDetailPanel } from "./WorkItemDetailPanel"

const detail: WorkItem = {
  id: "5001",
  version_id: "2001",
  kind: "execute.batch",
  goal: "执行订单回归批次",
  state: "done",
  created_by: "state_machine",
  attempt: 1,
  depends_on: ["5000"],
  result: { wave_id: "9201", passed: 12, failed: 0 },
  created_at: "2026-11-01T08:00:00Z",
  updated_at: "2026-11-01T08:30:00Z",
}

describe("WorkItemDetailPanel", () => {
  beforeEach(() => {
    api.getWorkItem.mockReset()
  })

  afterEach(cleanup)

  it("renders the work-item payload summary and result reference", async () => {
    api.getWorkItem.mockResolvedValue(detail)

    render(<WorkItemDetailPanel versionId="2001" workItemId="5001" />)

    expect(await screen.findByTestId("work-item-detail")).toBeInTheDocument()
    expect(screen.getByText("执行订单回归批次")).toBeInTheDocument()
    expect(screen.getByText("execute.batch")).toBeInTheDocument()
    expect(screen.getByText("done", { selector: "[data-tone]" })).toBeInTheDocument()
    expect(screen.getByText(/"passed": 12/)).toBeInTheDocument()
    expect(api.getWorkItem).toHaveBeenCalledWith("2001", "5001")
  })

  it("shows a loading state while the detail read is pending", () => {
    api.getWorkItem.mockImplementation(() => new Promise<WorkItem>(() => undefined))

    render(<WorkItemDetailPanel versionId="2001" workItemId="5001" />)

    expect(screen.getByText("正在加载 work-item…")).toBeInTheDocument()
  })

  it("shows a not-found error when the work item does not exist", async () => {
    api.getWorkItem.mockRejectedValue(
      new ApiError({
        status: 404,
        code: "not_found",
        message: "work-item 不存在",
        requestId: "req_work_item",
      }),
    )

    render(<WorkItemDetailPanel versionId="2001" workItemId="9999" />)

    expect(await screen.findByText("work-item 不存在")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "重试" })).toBeInTheDocument()
  })
})
