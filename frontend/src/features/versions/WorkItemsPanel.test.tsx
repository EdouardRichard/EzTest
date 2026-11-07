import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

const api = vi.hoisted(() => ({
  listWorkItems: vi.fn(),
  getWorkItem: vi.fn(),
}))
vi.mock("./api", () => api)

import type { WorkItem } from "./api"
import { WorkItemsPanel } from "./WorkItemsPanel"

function workItem(overrides: Partial<WorkItem>): WorkItem {
  return {
    id: "5001",
    version_id: "2001",
    kind: "execute.batch",
    goal: "执行订单回归批次",
    state: "running",
    created_by: "state_machine",
    attempt: 1,
    created_at: "2026-11-01T08:00:00Z",
    updated_at: "2026-11-01T08:05:00Z",
    ...overrides,
  }
}

const runningItem = workItem({})
const failedItem = workItem({
  id: "5002",
  kind: "report.version",
  goal: "生成版本报告",
  state: "failed",
  attempt: 2,
})

describe("WorkItemsPanel", () => {
  beforeEach(() => {
    api.listWorkItems.mockReset()
    api.getWorkItem.mockReset()
  })

  afterEach(cleanup)

  it("renders the work-item list with state, kind and a deep link", async () => {
    api.listWorkItems.mockResolvedValue([runningItem, failedItem])

    render(<WorkItemsPanel versionId="2001" />)

    expect(await screen.findByText("执行订单回归批次")).toBeInTheDocument()
    expect(screen.getByText("生成版本报告")).toBeInTheDocument()
    expect(screen.getByText("running")).toBeInTheDocument()
    expect(screen.getByText("execute.batch")).toBeInTheDocument()
    expect(api.listWorkItems).toHaveBeenCalledWith("2001")

    const link = screen.getByRole("link", { name: /work-item #5001/ })
    expect(link).toHaveAttribute("href", expect.stringContaining("work_item=5001"))
  })

  it("shows an explicit empty state when there are no work items", async () => {
    api.listWorkItems.mockResolvedValue([])

    render(<WorkItemsPanel versionId="2001" />)

    expect(await screen.findByText("该版本暂无作业")).toBeInTheDocument()
  })

  it("shows the list error and retries the read", async () => {
    api.listWorkItems
      .mockRejectedValueOnce(
        new ApiError({
          status: 503,
          code: "unavailable",
          message: "作业列表读取失败",
          requestId: "req_work_items",
        }),
      )
      .mockResolvedValueOnce([runningItem])

    render(<WorkItemsPanel versionId="2001" />)

    expect(await screen.findByText("作业列表读取失败")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(await screen.findByText("执行订单回归批次")).toBeInTheDocument()
  })

  it("selects a work item and mounts its detail panel", async () => {
    api.listWorkItems.mockResolvedValue([runningItem])
    api.getWorkItem.mockResolvedValue(runningItem)

    render(<WorkItemsPanel versionId="2001" />)

    fireEvent.click(await screen.findByRole("button", { name: "查看 work-item #5001" }))

    expect(await screen.findByTestId("work-item-detail")).toBeInTheDocument()
    expect(api.getWorkItem).toHaveBeenCalledWith("2001", "5001")
  })

  it("honours the ?work_item= deep link on mount", async () => {
    api.listWorkItems.mockResolvedValue([failedItem])
    api.getWorkItem.mockResolvedValue(failedItem)

    render(<WorkItemsPanel versionId="2001" initialWorkItemId="5002" />)

    expect(await screen.findByTestId("work-item-detail")).toBeInTheDocument()
    expect(api.getWorkItem).toHaveBeenCalledWith("2001", "5002")
  })
})
