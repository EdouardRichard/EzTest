import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { VersionEvent, VersionEventPage } from "@/features/versions/types"
import type { SSEStatus } from "@/lib/sse-client"

const mocks = vi.hoisted(() => ({
  fetchVersionEvents: vi.fn(),
  useVersionStream: vi.fn(),
  reconnect: vi.fn(),
}))

vi.mock("@/features/versions/api", () => ({
  fetchVersionEvents: mocks.fetchVersionEvents,
}))

vi.mock("@/features/versions/useVersionStream", () => ({
  useVersionStream: mocks.useVersionStream,
}))

import { BroadcastTab } from "./BroadcastTab"

const VERSION_ID = "1844674407370955161"

let streamEvents: VersionEvent[] = []
let streamStatus: SSEStatus = "live"

/** Builds one version-channel event; execution events reuse the P3 envelope. */
function makeEvent(seq: number, overrides: Partial<VersionEvent> = {}): VersionEvent {
  return {
    seq,
    channel: `version:${VERSION_ID}`,
    type: "step.finished",
    version_id: VERSION_ID,
    batch_id: null,
    case_id: null,
    step_id: null,
    agent_role: null,
    session_id: null,
    ts: "2026-10-28T10:00:00Z",
    summary: `事件 ${seq}`,
    detail: {},
    refs: { evidence: [], case: [], requirement: [], code: [] },
    ...overrides,
  }
}

function page(events: VersionEvent[], hasMore = false): VersionEventPage {
  return { events, cursor: 100, hasMore }
}

function renderTab() {
  return render(<BroadcastTab versionId={VERSION_ID} />)
}

/** Rendered row order, taken from the stable `data-seq` attribute. */
function rowSeqs(): string[] {
  return screen.queryAllByTestId(/^broadcast-row-/).map((row) => row.getAttribute("data-seq") ?? "")
}

function setFilter(label: string, value: string): void {
  fireEvent.change(screen.getByLabelText(label), { target: { value } })
}

beforeEach(() => {
  mocks.fetchVersionEvents.mockReset()
  mocks.useVersionStream.mockReset()
  mocks.reconnect.mockReset()
  streamEvents = []
  streamStatus = "live"
  mocks.fetchVersionEvents.mockResolvedValue(page([]))
  mocks.useVersionStream.mockImplementation(() => ({
    events: streamEvents,
    status: streamStatus,
    reconnect: mocks.reconnect,
  }))
})

afterEach(() => {
  cleanup()
})

describe("BroadcastTab", () => {
  it("renders the full timeline newest first", async () => {
    mocks.fetchVersionEvents.mockResolvedValue(
      page([makeEvent(1), makeEvent(2), makeEvent(3)], false),
    )

    renderTab()

    await screen.findByTestId("broadcast-row-3")
    expect(mocks.fetchVersionEvents).toHaveBeenCalledWith(VERSION_ID, { limit: 50 })
    expect(rowSeqs()).toEqual(["3", "2", "1"])
  })

  it("pins anomalous events above the regular timeline", async () => {
    mocks.fetchVersionEvents.mockResolvedValue(
      page(
        [
          makeEvent(1, { summary: "步骤通过" }),
          makeEvent(2, {
            type: "run.finished",
            summary: "用例失败",
            detail: { status: "failed" },
          }),
          makeEvent(3, { summary: "步骤通过" }),
        ],
        false,
      ),
    )

    renderTab()

    await screen.findByTestId("broadcast-row-2")
    expect(rowSeqs()).toEqual(["2", "3", "1"])
    const failedRow = screen.getByTestId("broadcast-row-2")
    expect(failedRow.getAttribute("data-anomaly")).toBe("true")
    expect(screen.getByTestId("broadcast-row-3").getAttribute("data-anomaly")).toBe("false")
  })

  it("filters by event type", async () => {
    mocks.fetchVersionEvents.mockResolvedValue(
      page([
        makeEvent(1, { type: "step.finished", detail: { status: "passed" } }),
        makeEvent(2, { type: "run.finished", detail: { status: "passed" } }),
        makeEvent(3, { type: "batch.created", detail: { status: "planned" } }),
      ]),
    )

    renderTab()
    await screen.findByTestId("broadcast-row-3")

    setFilter("类型", "run.finished")

    expect(rowSeqs()).toEqual(["2"])
  })

  it("labels and filters the P7 autonomy events", async () => {
    mocks.fetchVersionEvents.mockResolvedValue(
      page([
        makeEvent(1, { type: "issue.created", summary: "新问题已落库" }),
        makeEvent(2, { type: "analysis.finished", summary: "失败分析完成" }),
        makeEvent(3, { type: "step.finished", summary: "步骤完成" }),
      ]),
    )

    renderTab()
    await screen.findByTestId("broadcast-row-3")

    expect(screen.getByTestId("broadcast-row-1").textContent).toContain("问题创建")
    expect(screen.getByTestId("broadcast-row-2").textContent).toContain("AI 分析完成")
    expect(screen.getByRole("option", { name: "问题创建" })).toBeTruthy()

    setFilter("类型", "issue.created")
    expect(rowSeqs()).toEqual(["1"])
  })

  it("filters by agent role", async () => {
    mocks.fetchVersionEvents.mockResolvedValue(
      page([
        makeEvent(1, { agent_role: "execution-agent" }),
        makeEvent(2, { agent_role: "review-agent" }),
        makeEvent(3, { agent_role: "execution-agent" }),
      ]),
    )

    renderTab()
    await screen.findByTestId("broadcast-row-3")

    setFilter("Agent", "execution-agent")

    expect(rowSeqs()).toEqual(["3", "1"])
  })

  it("filters by derived severity", async () => {
    mocks.fetchVersionEvents.mockResolvedValue(
      page([
        makeEvent(1, { detail: { status: "passed" } }),
        makeEvent(2, { type: "run.finished", detail: { status: "failed" } }),
        makeEvent(3, { type: "run.finished", detail: { status: "blocked" } }),
      ]),
    )

    renderTab()
    await screen.findByTestId("broadcast-row-3")

    setFilter("严重级", "warning")

    expect(rowSeqs()).toEqual(["3"])
  })

  it("searches summaries and event metadata", async () => {
    mocks.fetchVersionEvents.mockResolvedValue(
      page([
        makeEvent(1, { summary: "步骤 1 执行完成" }),
        makeEvent(2, { summary: "请求超时，用例失败" }),
        makeEvent(3, { summary: "批次创建" }),
      ]),
    )

    renderTab()
    await screen.findByTestId("broadcast-row-3")

    fireEvent.change(screen.getByLabelText("搜索事件"), { target: { value: "超时" } })

    expect(rowSeqs()).toEqual(["2"])
  })

  it("shows the filtered empty state when nothing matches", async () => {
    mocks.fetchVersionEvents.mockResolvedValue(
      page([makeEvent(1, { detail: { status: "passed" } })]),
    )

    renderTab()
    await screen.findByTestId("broadcast-row-1")

    setFilter("严重级", "error")

    expect(await screen.findByText("没有符合筛选条件的事件")).toBeTruthy()
    expect(rowSeqs()).toEqual([])
  })

  it("loads older events with the before_seq cursor and stops at has_more=false", async () => {
    mocks.fetchVersionEvents
      .mockResolvedValueOnce(page([makeEvent(10), makeEvent(11)], true))
      .mockResolvedValueOnce(page([makeEvent(7), makeEvent(8), makeEvent(9)], false))

    renderTab()
    await screen.findByTestId("broadcast-row-11")

    fireEvent.click(screen.getByTestId("broadcast-load-older"))

    await screen.findByTestId("broadcast-row-7")
    expect(mocks.fetchVersionEvents).toHaveBeenNthCalledWith(2, VERSION_ID, {
      beforeSeq: 10,
      limit: 50,
    })
    expect(rowSeqs()).toEqual(["11", "10", "9", "8", "7"])
    expect(screen.queryByTestId("broadcast-load-older")).toBeNull()
  })

  it("merges live stream events and dedupes by seq", async () => {
    mocks.fetchVersionEvents.mockResolvedValue(page([makeEvent(1), makeEvent(2), makeEvent(3)]))
    streamEvents = [makeEvent(4), makeEvent(2)]

    renderTab()

    await screen.findByTestId("broadcast-row-4")
    await waitFor(() => expect(rowSeqs()).toEqual(["4", "3", "2", "1"]))
  })

  it("shows the empty state when the channel has no events", async () => {
    renderTab()

    expect(await screen.findByText("暂无执行事件")).toBeTruthy()
  })

  it("shows an error with a retry and recovers", async () => {
    mocks.fetchVersionEvents
      .mockRejectedValueOnce(new Error("网络错误"))
      .mockResolvedValueOnce(page([makeEvent(1)]))

    renderTab()

    const alert = await screen.findByTestId("broadcast-error")
    expect(alert.textContent).toContain("加载执行事件失败")
    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    await screen.findByTestId("broadcast-row-1")
    expect(mocks.fetchVersionEvents).toHaveBeenCalledTimes(2)
  })

  it("deep-links an event to its case run and evidence", async () => {
    mocks.fetchVersionEvents.mockResolvedValue(
      page([
        makeEvent(1, {
          type: "step.finished",
          case_id: "9001",
          batch_id: "300",
          detail: { case_run_id: "5001", status: "failed" },
          refs: { evidence: ["7001", "7002"], case: ["9001"], requirement: [], code: [] },
        }),
      ]),
    )

    renderTab()
    await screen.findByTestId("broadcast-row-1")

    const caseLink = screen.getByRole("link", { name: "查看用例" })
    expect(caseLink.getAttribute("href")).toBe(`/versions/${VERSION_ID}?tab=results&caseRun=5001`)
    const evidenceLink = screen.getByRole("link", { name: "证据 7001" })
    expect(evidenceLink.getAttribute("href")).toBe(
      `/versions/${VERSION_ID}?tab=results&evidence=7001`,
    )
  })

  it("renders the stream status and offers a manual reconnect", async () => {
    streamStatus = "reconnecting"

    renderTab()

    const status = await screen.findByTestId("stream-status")
    expect(status.textContent).toContain("重连中")
    fireEvent.click(screen.getByTestId("broadcast-reconnect"))
    expect(mocks.reconnect).toHaveBeenCalledTimes(1)
  })
})
