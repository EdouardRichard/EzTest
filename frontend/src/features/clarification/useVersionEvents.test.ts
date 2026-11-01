import { act, cleanup, render, screen } from "@testing-library/react"
import { createElement } from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { getToken, setToken } from "@/lib/auth-storage"

import { conversationMessageFixture } from "./test-fixtures"

type ConnectOptions = {
  url: string
  token: string
  onEvent: (event: { id?: string; event: string; data: string }) => void
  onStatus: (status: string) => void
  onReset?: (data: unknown) => void
}

const sse = vi.hoisted(() => ({
  connectSSE: vi.fn<(options: ConnectOptions) => () => void>(),
}))

vi.mock("@/lib/sse-client", () => ({
  connectSSE: sse.connectSSE,
}))

import { useVersionEvents } from "./useVersionEvents"

function eventFrame(type: string, payload: Record<string, unknown>, scope = {}) {
  return JSON.stringify({
    seq: 7,
    channel: "version:2001",
    type,
    scope: { version_id: "2001", ...scope },
    payload,
    actor_id: null,
    request_id: "req_sse",
    created_at: "2026-10-31T10:00:00Z",
  })
}

function Harness(props: {
  handlers: Parameters<typeof useVersionEvents>[1]
}): ReturnType<typeof createElement> {
  const status = useVersionEvents("2001", props.handlers)
  return createElement("span", { "data-testid": "sse-status" }, status)
}

describe("useVersionEvents", () => {
  let connectOptions: ConnectOptions
  let cleanupSpy: ReturnType<typeof vi.fn<() => void>>

  beforeEach(() => {
    window.sessionStorage.clear()
    setToken("ezt_token")
    expect(getToken()).toBe("ezt_token")
    vi.stubEnv("NEXT_PUBLIC_API_BASE_URL", "")
    cleanupSpy = vi.fn<() => void>()
    sse.connectSSE.mockReset()
    sse.connectSSE.mockImplementation((options: ConnectOptions) => {
      connectOptions = options
      return cleanupSpy
    })
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllEnvs()
    vi.unstubAllGlobals()
    window.sessionStorage.clear()
  })

  it("connects to the version channel with the bearer token", () => {
    render(createElement(Harness, { handlers: {} }))

    expect(sse.connectSSE).toHaveBeenCalledTimes(1)
    expect(connectOptions.url).toBe("http://localhost:13333/api/stream?channel=version:2001")
    expect(connectOptions.token).toBe("ezt_token")
    expect(screen.getByTestId("sse-status")).toHaveTextContent("connecting")
  })

  it("routes conversation.message to the message handler and reports status", () => {
    const onConversationMessage = vi.fn()
    render(createElement(Harness, { handlers: { onConversationMessage } }))

    act(() => connectOptions.onStatus("live"))
    expect(screen.getByTestId("sse-status")).toHaveTextContent("live")

    connectOptions.onEvent({
      event: "message",
      data: eventFrame(
        "conversation.message",
        conversationMessageFixture as unknown as Record<string, unknown>,
        { conversation_id: "4001" },
      ),
    })

    expect(onConversationMessage).toHaveBeenCalledWith(conversationMessageFixture)
  })

  it("accepts a payload that wraps the message in a `message` field", () => {
    const onConversationMessage = vi.fn()
    render(createElement(Harness, { handlers: { onConversationMessage } }))

    connectOptions.onEvent({
      event: "message",
      data: eventFrame("conversation.message", { message: conversationMessageFixture }),
    })

    expect(onConversationMessage).toHaveBeenCalledWith(conversationMessageFixture)
  })

  it("routes P1 activity/requirement/team-room/version events", () => {
    const onAgentActivity = vi.fn()
    const onRequirementChanged = vi.fn()
    const onTeamRoomChanged = vi.fn()
    const onVersionChanged = vi.fn()
    render(
      createElement(Harness, {
        handlers: { onAgentActivity, onRequirementChanged, onTeamRoomChanged, onVersionChanged },
      }),
    )

    connectOptions.onEvent({
      event: "message",
      data: eventFrame("agent.activity", { activity: "正在分析需求关系", status: "running" }),
    })
    connectOptions.onEvent({
      event: "message",
      data: eventFrame("requirement.created", { requirement_id: "6001" }),
    })
    connectOptions.onEvent({
      event: "message",
      data: eventFrame("requirement.clarified", { requirement_id: "6001" }),
    })
    connectOptions.onEvent({
      event: "message",
      data: eventFrame("team_room.post", { post_id: "9101" }),
    })
    connectOptions.onEvent({
      event: "message",
      data: eventFrame("version.status_changed", { status: "running" }),
    })

    expect(onAgentActivity).toHaveBeenCalledWith({
      activity: "正在分析需求关系",
      status: "running",
    })
    expect(onRequirementChanged).toHaveBeenCalledTimes(2)
    expect(onTeamRoomChanged).toHaveBeenCalledTimes(1)
    expect(onVersionChanged).toHaveBeenCalledTimes(1)
  })

  it("routes P2 design events to the design handlers", () => {
    const onDesignGraphChanged = vi.fn()
    const onDesignCaseChanged = vi.fn()
    const onDesignRoundChanged = vi.fn()
    const onDesignExportReady = vi.fn()
    render(
      createElement(Harness, {
        handlers: {
          onDesignGraphChanged,
          onDesignCaseChanged,
          onDesignRoundChanged,
          onDesignExportReady,
        },
      }),
    )

    connectOptions.onEvent({
      event: "message",
      data: eventFrame("design.graph_delta", {
        requirement_id: "6001",
        case_ids: ["7001"],
        op: "propose",
      }),
    })
    for (const type of [
      "design.case_version_created",
      "design.case_version_stale",
      "design.case_version_superseded",
      "design.case_confirmed",
    ]) {
      connectOptions.onEvent({
        event: "message",
        data: eventFrame(type, { requirement_id: "6001" }),
      })
    }
    connectOptions.onEvent({
      event: "message",
      data: eventFrame("design.round_started", { round_id: "8101" }),
    })
    connectOptions.onEvent({
      event: "message",
      data: eventFrame("design.scope_activated", { round_id: "8101" }),
    })
    connectOptions.onEvent({
      event: "message",
      data: eventFrame("design.export_ready", { export_id: "9001", format: "markdown" }),
    })

    expect(onDesignGraphChanged).toHaveBeenCalledTimes(1)
    expect(onDesignCaseChanged).toHaveBeenCalledTimes(4)
    expect(onDesignRoundChanged).toHaveBeenCalledTimes(2)
    expect(onDesignExportReady).toHaveBeenCalledTimes(1)
    expect(onDesignExportReady.mock.calls[0][0].type).toBe("design.export_ready")
  })

  it("routes P3 execution events to the execution handlers", () => {
    const onWaveChanged = vi.fn()
    const onCaseRunChanged = vi.fn()
    const onEvidenceCreated = vi.fn()
    const onHumanRequestChanged = vi.fn()
    const onExecutionChanged = vi.fn()
    render(
      createElement(Harness, {
        handlers: {
          onWaveChanged,
          onCaseRunChanged,
          onEvidenceCreated,
          onHumanRequestChanged,
          onExecutionChanged,
        },
      }),
    )

    for (const type of [
      "wave.created",
      "wave.started",
      "wave.paused",
      "wave.resumed",
      "wave.finished",
    ]) {
      connectOptions.onEvent({ event: "message", data: eventFrame(type, { wave_id: "9201" }) })
    }
    for (const type of [
      "case_run.started",
      "case_run.paused",
      "case_run.resumed",
      "case_run.finished",
      "case_run.step_finished",
      "case_run.assertion_finished",
    ]) {
      connectOptions.onEvent({ event: "message", data: eventFrame(type, { case_run_id: "9301" }) })
    }
    connectOptions.onEvent({
      event: "message",
      data: eventFrame("evidence.created", { evidence_id: "9401" }),
    })
    for (const type of [
      "human_request.created",
      "human_request.resolved",
      "human_request.expired",
    ]) {
      connectOptions.onEvent({
        event: "message",
        data: eventFrame(type, { human_request_id: "9501" }),
      })
    }
    for (const type of [
      "execution.intent_created",
      "execution.intent_cancelled",
      "execution.intent_superseded",
      "execution.failure_candidate",
      "execution.coverage_gap",
    ]) {
      connectOptions.onEvent({ event: "message", data: eventFrame(type, { intent_id: "9101" }) })
    }

    expect(onWaveChanged).toHaveBeenCalledTimes(5)
    expect(onCaseRunChanged).toHaveBeenCalledTimes(6)
    expect(onEvidenceCreated).toHaveBeenCalledTimes(1)
    expect(onHumanRequestChanged).toHaveBeenCalledTimes(3)
    expect(onExecutionChanged).toHaveBeenCalledTimes(5)
    expect(onWaveChanged.mock.calls[0][0].type).toBe("wave.created")
    expect(onCaseRunChanged.mock.calls[0][0].type).toBe("case_run.started")
    expect(onHumanRequestChanged.mock.calls[2][0].type).toBe("human_request.expired")
    expect(onExecutionChanged.mock.calls[4][0].type).toBe("execution.coverage_gap")
  })

  it("routes P4 issue, report, requirement-closure and version-closure events", () => {
    const onIssueChanged = vi.fn()
    const onReportChanged = vi.fn()
    const onRequirementChanged = vi.fn()
    const onVersionChanged = vi.fn()
    render(
      createElement(Harness, {
        handlers: { onIssueChanged, onReportChanged, onRequirementChanged, onVersionChanged },
      }),
    )

    for (const type of ["issue.created", "issue.updated", "issue.reopened", "issue.merged"]) {
      connectOptions.onEvent({ event: "message", data: eventFrame(type, { issue_id: "9101" }) })
    }
    for (const type of ["report.published", "report.stale"]) {
      connectOptions.onEvent({ event: "message", data: eventFrame(type, { report_id: "8101" }) })
    }
    for (const type of ["requirement.closed", "requirement.reopened"]) {
      connectOptions.onEvent({
        event: "message",
        data: eventFrame(type, { requirement_id: "6001" }),
      })
    }
    for (const type of ["version.closed", "version.reopened"]) {
      connectOptions.onEvent({ event: "message", data: eventFrame(type, { version_id: "2001" }) })
    }

    expect(onIssueChanged).toHaveBeenCalledTimes(4)
    expect(onReportChanged).toHaveBeenCalledTimes(2)
    expect(onRequirementChanged).toHaveBeenCalledTimes(2)
    expect(onVersionChanged).toHaveBeenCalledTimes(2)
    expect(onIssueChanged.mock.calls[0][0].type).toBe("issue.created")
    expect(onReportChanged.mock.calls[1][0].type).toBe("report.stale")
    expect(onRequirementChanged.mock.calls[0][0].type).toBe("requirement.closed")
    expect(onVersionChanged.mock.calls[1][0].type).toBe("version.reopened")
    // One connection serves every P4 event family.
    expect(sse.connectSSE).toHaveBeenCalledTimes(1)
  })

  it("routes P7 PDF and issue-recurrence events to the P4 refresh handlers", () => {
    const onReportChanged = vi.fn()
    const onIssueChanged = vi.fn()
    render(createElement(Harness, { handlers: { onReportChanged, onIssueChanged } }))

    for (const type of ["report.pdf_ready", "report.pdf_failed"]) {
      connectOptions.onEvent({
        event: "message",
        data: eventFrame(type, {
          report_id: "8101",
          report_revision_id: "8201",
          revision: 2,
          pdf_ref: "content://pdf",
          size_bytes: 2048,
          attempts: 3,
          error: "renderer unavailable",
        }),
      })
    }
    connectOptions.onEvent({
      event: "message",
      data: eventFrame("issue.recurred", {
        issue_id: "9101",
        source_issue_id: "9100",
        dedupe_key: "login-retry",
        recurrence_count: 2,
      }),
    })

    expect(onReportChanged).toHaveBeenCalledTimes(2)
    expect(onIssueChanged).toHaveBeenCalledTimes(1)
    expect(onReportChanged.mock.calls[0][0].type).toBe("report.pdf_ready")
    expect(onReportChanged.mock.calls[1][0].type).toBe("report.pdf_failed")
    expect(onIssueChanged.mock.calls[0][0].type).toBe("issue.recurred")
    expect(sse.connectSSE).toHaveBeenCalledTimes(1)
  })

  it("notifies stream.reset through a callback without opening another EventSource", () => {
    const onStreamReset = vi.fn()
    render(createElement(Harness, { handlers: { onStreamReset } }))

    expect(sse.connectSSE).toHaveBeenCalledTimes(1)
    act(() => connectOptions.onReset?.({ reason: "cursor_too_old", snapshot_cursor: 99 }))

    expect(onStreamReset).toHaveBeenCalledWith({ reason: "cursor_too_old", snapshot_cursor: 99 })
    expect(sse.connectSSE).toHaveBeenCalledTimes(1)
  })

  it("ignores unknown event types and malformed payloads", () => {
    const onConversationMessage = vi.fn()
    const onRequirementChanged = vi.fn()
    render(createElement(Harness, { handlers: { onConversationMessage, onRequirementChanged } }))

    connectOptions.onEvent({ event: "message", data: "not-json" })
    connectOptions.onEvent({
      event: "message",
      data: eventFrame("unknown.event", { anything: true }),
    })

    expect(onConversationMessage).not.toHaveBeenCalled()
    expect(onRequirementChanged).not.toHaveBeenCalled()
  })

  it("uses the latest handlers without reconnecting and disconnects on unmount", () => {
    const first = vi.fn()
    const second = vi.fn()
    const { rerender, unmount } = render(
      createElement(Harness, { handlers: { onRequirementChanged: first } }),
    )

    rerender(createElement(Harness, { handlers: { onRequirementChanged: second } }))
    expect(sse.connectSSE).toHaveBeenCalledTimes(1)

    connectOptions.onEvent({
      event: "message",
      data: eventFrame("requirement.version_created", { requirement_id: "6001" }),
    })
    expect(first).not.toHaveBeenCalled()
    expect(second).toHaveBeenCalledTimes(1)

    unmount()
    expect(cleanupSpy).toHaveBeenCalledTimes(1)
  })
})
