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
