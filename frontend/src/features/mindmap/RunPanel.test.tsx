import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

import type { CaseRunView, MindmapGraph } from "./types"

const mocks = vi.hoisted(() => ({
  startRun: vi.fn(),
  getRun: vi.fn(),
  role: "admin" as string | null,
}))

vi.mock("@/features/auth/AuthProvider", () => ({
  useAuth: () => ({
    user:
      mocks.role === null
        ? null
        : { id: 1, username: "tester", role: mocks.role, status: "active" },
    loading: false,
    logout: vi.fn(),
  }),
}))

vi.mock("./api", () => ({
  mindmapApi: { startRun: mocks.startRun, getRun: mocks.getRun },
}))

import { RunPanel } from "./RunPanel"

const VERSION_ID = "1844674407370955161"
const RUN_ID = "1844674407370955401"
const TIMESTAMP = "2026-10-28T10:00:00Z"

function graph(): MindmapGraph {
  return {
    versionId: VERSION_ID,
    versionStatus: "designing",
    editable: true,
    nodes: [],
    coverage: {
      requirementIds: [],
      uncoveredRequirementIds: [],
      staleRequirementIds: [],
      matrix: [],
    },
    baseline: null,
    latestRuns: {},
    impactCases: [],
  }
}

function run(overrides: Partial<CaseRunView> = {}): CaseRunView {
  return {
    id: RUN_ID,
    versionId: VERSION_ID,
    kind: "explore",
    status: "done",
    reason: "",
    stats: null,
    errors: [],
    agentSessionId: null,
    agentRunId: null,
    model: "",
    promptVersion: "",
    createdBy: 1,
    createdAt: TIMESTAMP,
    startedAt: TIMESTAMP,
    finishedAt: TIMESTAMP,
    ...overrides,
  }
}

function renderPanel(overrides: Partial<Parameters<typeof RunPanel>[0]> = {}) {
  return render(
    <RunPanel
      versionId={VERSION_ID}
      graph={graph()}
      systemId={1}
      environmentId={2}
      {...overrides}
    />,
  )
}

describe("RunPanel explore entry", () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.role = "admin"
    mocks.startRun.mockResolvedValue({ runId: RUN_ID })
    mocks.getRun.mockResolvedValue(run())
  })

  afterEach(cleanup)

  it("hides the explore trigger from non-admins", () => {
    mocks.role = "designer"

    renderPanel()

    expect(screen.queryByTestId("btn-explore")).toBeNull()
  })

  it("starts an explore run with the version bindings for an admin", async () => {
    renderPanel()

    fireEvent.click(screen.getByTestId("btn-explore"))

    await waitFor(() =>
      expect(mocks.startRun).toHaveBeenCalledWith(VERSION_ID, {
        kind: "explore",
        systemId: 1,
        environmentId: 2,
      }),
    )
    await waitFor(() => expect(mocks.getRun).toHaveBeenCalledWith(VERSION_ID, RUN_ID))
  })

  it("disables the explore trigger and explains when the version lacks bindings", () => {
    renderPanel({ systemId: null, environmentId: null })

    expect((screen.getByTestId("btn-explore") as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getByTestId("explore-unavailable").textContent).toContain("绑定")
  })

  it("surfaces the mapped 403 reason", async () => {
    mocks.startRun.mockRejectedValue(
      new ApiError({
        status: 403,
        code: "forbidden",
        message: "forbidden",
        details: { reason: "forbidden" },
      }),
    )
    renderPanel()

    fireEvent.click(screen.getByTestId("btn-explore"))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("没有权限执行此操作。")
  })

  it("sends optional start paths when provided", async () => {
    renderPanel()

    fireEvent.change(screen.getByLabelText(/起始路径/), {
      target: { value: "/p5/home, /orders" },
    })
    fireEvent.click(screen.getByTestId("btn-explore"))

    await waitFor(() =>
      expect(mocks.startRun).toHaveBeenCalledWith(VERSION_ID, {
        kind: "explore",
        systemId: 1,
        environmentId: 2,
        startPaths: ["/p5/home", "/orders"],
      }),
    )
  })
})
