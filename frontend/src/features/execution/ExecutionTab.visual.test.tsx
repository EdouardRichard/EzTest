import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const api = vi.hoisted(() => ({
  getOverview: vi.fn(),
  listWaves: vi.fn(),
  getWave: vi.fn(),
  listCaseRuns: vi.fn(),
  getCaseRun: vi.fn(),
  listHumanRequests: vi.fn(),
  pauseWave: vi.fn(),
  resumeWave: vi.fn(),
  cancelWave: vi.fn(),
  pauseCaseRun: vi.fn(),
  resumeCaseRun: vi.fn(),
  resolveHumanRequest: vi.fn(),
  cancelHumanRequest: vi.fn(),
}))
vi.mock("./api", () => api)

const navigation = vi.hoisted(() => ({ replace: vi.fn() }))
vi.mock("next/navigation", () => ({ useRouter: () => navigation }))

vi.mock("@/features/clarification/useVersionEvents", () => ({
  useVersionEvents: () => "live",
}))

vi.mock("@/features/visual/VisualView", () => ({
  VisualView: ({ versionId }: { versionId: string }) => (
    <div data-testid="visual-view-stub">视觉子视图 {versionId}</div>
  ),
}))

import { ExecutionTab } from "./ExecutionTab"
import {
  caseRunDetailFixture,
  caseRunsFixture,
  doneWaveFixture,
  executionOverviewFixture,
  runningWaveFixture,
} from "./test-fixtures"

function mockWorkspace() {
  api.getOverview.mockResolvedValue(executionOverviewFixture)
  api.listWaves.mockResolvedValue([runningWaveFixture, doneWaveFixture])
  api.listCaseRuns.mockResolvedValue(caseRunsFixture)
  api.getCaseRun.mockResolvedValue(caseRunDetailFixture)
  api.listHumanRequests.mockResolvedValue([])
}

describe("ExecutionTab visual sub-view", () => {
  const originalUrl = window.location.href

  beforeEach(() => {
    for (const fn of Object.values(api)) fn.mockReset()
    navigation.replace.mockReset()
    mockWorkspace()
    window.history.replaceState({}, "", "/versions/2001?tab=execution")
  })

  afterEach(() => {
    cleanup()
    window.history.replaceState({}, "", originalUrl)
  })

  it("renders the visual sub-view when the view prop selects it", async () => {
    render(<ExecutionTab versionId="2001" view="visual" />)

    expect(await screen.findByTestId("visual-view-stub")).toHaveTextContent("视觉子视图 2001")
    expect(screen.queryByTestId("wave-list")).not.toBeInTheDocument()
    expect(screen.queryByTestId("execution-toolbar")).not.toBeInTheDocument()
  })

  it("switches from waves to visual and writes ?view=visual, then restores waves", async () => {
    render(<ExecutionTab versionId="2001" />)

    expect(await screen.findByTestId("wave-list")).toBeInTheDocument()

    fireEvent.click(screen.getByRole("tab", { name: "视觉断言" }))
    await waitFor(() =>
      expect(navigation.replace).toHaveBeenCalledWith(
        expect.stringMatching(
          /^\/versions\/2001\?tab=execution&wave=9201(&case_run=9301)?&view=visual$/,
        ),
      ),
    )
    expect(screen.getByTestId("visual-view-stub")).toBeInTheDocument()

    fireEvent.click(screen.getByRole("tab", { name: "波次" }))
    await waitFor(() =>
      expect(navigation.replace).toHaveBeenLastCalledWith(
        expect.not.stringContaining("view=visual"),
      ),
    )
    expect(await screen.findByTestId("wave-list")).toBeInTheDocument()
  })

  it("honours ?view=visual from the URL when no prop is given", async () => {
    window.history.replaceState({}, "", "/versions/2001?tab=execution&view=visual")

    render(<ExecutionTab versionId="2001" />)

    expect(await screen.findByTestId("visual-view-stub")).toBeInTheDocument()
    expect(screen.queryByTestId("wave-list")).not.toBeInTheDocument()
    expect(navigation.replace).not.toHaveBeenCalled()
  })
})
