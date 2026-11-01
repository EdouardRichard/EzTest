import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import type { GCRun, GCRunDetail } from "./api"

const api = vi.hoisted(() => ({
  listGCRuns: vi.fn(),
  getGCRun: vi.fn(),
  createGCRun: vi.fn(),
  listInstances: vi.fn(),
  drainInstance: vi.fn(),
  activateInstance: vi.fn(),
}))
vi.mock("./api", () => api)

const auth = vi.hoisted(() => ({ role: "admin" as "admin" | "user" }))
vi.mock("@/features/auth/AuthProvider", () => ({
  useAuth: () => ({ user: { role: auth.role } }),
}))

import { GCPanel } from "./GCPanel"

/* --------------------------------- fixtures -------------------------------- */

const startedAt = "2026-11-01T06:00:00Z"

const dryRun: GCRun = {
  id: "6001",
  mode: "dry_run",
  status: "completed",
  actor_id: "9001",
  actor_kind: "user",
  started_at: startedAt,
  finished_at: "2026-11-01T06:00:01Z",
  scanned_objects: 12,
  scanned_refs: 30,
  deleted_objects: 0,
  deleted_refs: 0,
  deleted_bytes: 0,
  report: {
    mode: "dry_run",
    dry_run: true,
    status: "completed",
    candidates: { unreferenced: 2, temp: 1, superseded_export: 1, orphan_file: 0 },
    candidate_samples: [
      {
        class: "unreferenced",
        object_sha: "a".repeat(64),
        size: 1024,
        age_seconds: 7200,
        delete_object: true,
        delete_ref: false,
      },
      {
        class: "unreferenced",
        object_sha: "b".repeat(64),
        size: 1024,
        age_seconds: 7200,
        delete_object: true,
        delete_ref: false,
      },
      {
        class: "temp",
        object_sha: "c".repeat(64),
        size: 512,
        age_seconds: 7200,
        delete_object: false,
        delete_ref: false,
      },
    ],
    planned: { objects: 2, refs: 0, files: 2, bytes: 2048 },
    deleted: { objects: 0, refs: 0, files: 0, bytes: 0 },
    tombstones: 0,
  },
  error: null,
}

const oldRun: GCRun = {
  ...dryRun,
  id: "6002",
  mode: "apply",
  started_at: "2026-10-31T06:00:00Z",
  deleted_objects: 2,
  deleted_bytes: 2048,
  report: {
    ...dryRun.report,
    mode: "apply",
    dry_run: false,
    deleted: { objects: 2, refs: 0, files: 2, bytes: 2048 },
    tombstones: 2,
  },
}

const dryRunDetail: GCRunDetail = {
  ...dryRun,
  tombstones: [
    {
      id: "6101",
      run_id: "6001",
      object_sha: "a".repeat(64),
      size: 1024,
      mime: "application/pdf",
      relative_path: "objects/aa/aa.pdf",
      reason: "unreferenced",
      deleted_at: "2026-11-01T06:00:01Z",
    },
  ],
}

function mockApi(): void {
  api.listGCRuns.mockResolvedValue({ items: [dryRun, oldRun], nextCursor: "", hasMore: false })
  api.getGCRun.mockImplementation((runId: string) =>
    Promise.resolve(runId === "6001" ? dryRunDetail : { ...oldRun, tombstones: [] }),
  )
  api.createGCRun.mockResolvedValue(dryRun)
}

function renderPanel(): void {
  render(<GCPanel />)
}

describe("GCPanel", () => {
  beforeEach(() => {
    auth.role = "admin"
    for (const fn of Object.values(api)) fn.mockReset()
    mockApi()
  })

  afterEach(cleanup)

  it("is hidden for non-admin users and issues no request", () => {
    auth.role = "user"

    renderPanel()

    expect(screen.queryByTestId("gc-panel")).not.toBeInTheDocument()
    expect(api.listGCRuns).not.toHaveBeenCalled()
    expect(api.createGCRun).not.toHaveBeenCalled()
  })

  it("renders the latest run report by class, history and tombstone detail", async () => {
    renderPanel()

    expect(await screen.findByTestId("gc-panel")).toBeInTheDocument()
    expect(api.listGCRuns).toHaveBeenCalledTimes(1)
    await waitFor(() => expect(api.getGCRun).toHaveBeenCalledWith("6001"))

    const reportTable = await screen.findByRole("table", { name: "GC 最新运行报告" })
    expect(within(reportTable).getByText("无引用对象")).toBeInTheDocument()
    expect(within(reportTable).getByText("临时文件")).toBeInTheDocument()
    expect(within(reportTable).getAllByText("2").length).toBeGreaterThan(0)

    const history = screen.getByRole("table", { name: "GC 运行历史" })
    expect(within(history).getByText("演练")).toBeInTheDocument()
    expect(within(history).getByText("执行")).toBeInTheDocument()

    const tombstones = await screen.findByTestId("gc-tombstones")
    expect(within(tombstones).getByText("objects/aa/aa.pdf")).toBeInTheDocument()
    expect(within(tombstones).getByText("1.0 KB")).toBeInTheDocument()
  })

  it("runs a dry-run without confirmation and refreshes the history", async () => {
    renderPanel()
    await screen.findByTestId("gc-panel")

    fireEvent.click(screen.getByRole("button", { name: "运行演练" }))

    await waitFor(() => expect(api.createGCRun).toHaveBeenCalledWith("dry_run"))
    await waitFor(() => expect(api.listGCRuns).toHaveBeenCalledTimes(2))
  })

  it("requires the typed APPLY confirmation before an apply run", async () => {
    renderPanel()
    await screen.findByTestId("gc-panel")

    fireEvent.click(screen.getByRole("button", { name: "执行回收" }))
    const dialog = await screen.findByRole("dialog", { name: "确认执行 GC" })
    const submit = within(dialog).getByRole("button", { name: "确认执行" })
    expect(submit).toBeDisabled()

    const input = within(dialog).getByLabelText("输入 APPLY 以确认")
    fireEvent.change(input, { target: { value: "apply" } })
    expect(submit).toBeDisabled()

    fireEvent.change(input, { target: { value: "APPLY" } })
    expect(submit).toBeEnabled()
    fireEvent.click(submit)

    await waitFor(() => expect(api.createGCRun).toHaveBeenCalledWith("apply", true))
    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "确认执行 GC" })).not.toBeInTheDocument()
    })
  })

  it("shows the apply permission/state error inline and keeps the dialog open", async () => {
    api.createGCRun.mockRejectedValueOnce(new Error("another gc run is already running"))
    renderPanel()
    await screen.findByTestId("gc-panel")

    fireEvent.click(screen.getByRole("button", { name: "执行回收" }))
    const dialog = await screen.findByRole("dialog", { name: "确认执行 GC" })
    fireEvent.change(within(dialog).getByLabelText("输入 APPLY 以确认"), {
      target: { value: "APPLY" },
    })
    fireEvent.click(within(dialog).getByRole("button", { name: "确认执行" }))

    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "another gc run is already running",
    )
    expect(screen.getByRole("dialog", { name: "确认执行 GC" })).toBeInTheDocument()
  })

  it("loads a historical run detail from the history table", async () => {
    renderPanel()
    const history = await screen.findByRole("table", { name: "GC 运行历史" })
    await waitFor(() => expect(api.getGCRun).toHaveBeenCalledWith("6001"))

    fireEvent.click(within(history).getAllByRole("button", { name: "查看明细" })[1])

    await waitFor(() => expect(api.getGCRun).toHaveBeenCalledWith("6002"))
  })
})
