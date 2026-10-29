import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

import {
  type AgentProfile,
  type AgentSession,
  createSession,
  deleteSession,
  fetchActivities,
  getMessage,
  getSession,
  listProfiles,
  listSessions,
  listTools,
  sendMessage,
  stopSession,
  updateProfile,
  updateSessionTitle,
} from "./agent-api"

const fetchMock = vi.fn()

const PROFILE: AgentProfile = {
  id: 2,
  key: "runtime-diagnostic",
  name: "运行时验证 Agent",
  description: "",
  builtin: true,
  enabled: true,
  system_prompt: "you are a runtime diagnostic agent",
  model: "",
  allowed_tools: ["file.list", "file.read"],
  max_turns: 12,
  run_seconds: 600,
  hard_timeout_seconds: 60,
  thinking: "enabled",
  reasoning_effort: "max",
  max_output_tokens: 32768,
  created_at: "2026-10-27T00:00:00Z",
  updated_at: "2026-10-27T00:00:00Z",
}

const SESSION: AgentSession = {
  id: 1,
  profile_id: 2,
  profile_key: "runtime-diagnostic",
  title: "检查运行时健康",
  created_by: 1,
  running: false,
  active_run: null,
  last_run: null,
  created_at: "2026-10-27T00:00:00Z",
  updated_at: "2026-10-27T00:00:00Z",
  last_activity_at: "2026-10-27T00:00:00Z",
}

/** Builds a JSON Response the way the real API envelope is shaped. */
function jsonResponse(payload: unknown, status = 200): Response {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "content-type": "application/json" },
  })
}

/** Returns the URL and init of the most recent fetch call. */
function lastRequest(): { url: string; init: RequestInit } {
  const [url, init] = fetchMock.mock.calls.at(-1) as [string, RequestInit]
  return { url: String(url), init }
}

describe("agent-api", () => {
  beforeEach(() => {
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it("lists profiles and tools through the shared API client", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: [PROFILE] }))
    await expect(listProfiles()).resolves.toEqual([PROFILE])
    expect(lastRequest().url.endsWith("/api/agents/profiles")).toBe(true)
    expect(lastRequest().init.method).toBe("GET")

    fetchMock.mockResolvedValue(
      jsonResponse({ data: [{ name: "file.read", description: "read a file", schema: null }] }),
    )
    await expect(listTools()).resolves.toEqual([
      { name: "file.read", description: "read a file", schema: null },
    ])
    expect(lastRequest().url.endsWith("/api/agents/tools")).toBe(true)
  })

  it("patches a profile with only the editable fields", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: { ...PROFILE, max_turns: 5 } }))

    const updated = await updateProfile(2, { max_turns: 5, enabled: false })

    expect(updated.max_turns).toBe(5)
    const { url, init } = lastRequest()
    expect(url.endsWith("/api/agents/profiles/2")).toBe(true)
    expect(init.method).toBe("PATCH")
    expect(JSON.parse(String(init.body))).toEqual({ max_turns: 5, enabled: false })
  })

  it("lists sessions with limit/cursor and unwraps the page envelope", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ data: { items: [SESSION], has_more: true, next_cursor: "next-page" } }),
    )

    const page = await listSessions({ limit: 20, cursor: "abc" })

    expect(page).toEqual({ items: [SESSION], has_more: true, next_cursor: "next-page" })
    const { url, init } = lastRequest()
    expect(url.endsWith("/api/agents/sessions?limit=20&cursor=abc")).toBe(true)
    expect(init.method).toBe("GET")
  })

  it("lists sessions without a query string when no params are given", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ data: { items: [], has_more: false, next_cursor: null } }),
    )

    await expect(listSessions()).resolves.toEqual({
      items: [],
      has_more: false,
      next_cursor: null,
    })
    expect(lastRequest().url.endsWith("/api/agents/sessions")).toBe(true)
  })

  it("creates a session and sends a message", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: SESSION }, 201))
    await expect(createSession({})).resolves.toEqual(SESSION)
    const created = lastRequest()
    expect(created.url.endsWith("/api/agents/sessions")).toBe(true)
    expect(created.init.method).toBe("POST")
    expect(created.init.body).toBe("{}")

    fetchMock.mockResolvedValue(jsonResponse({ data: { run_id: 9, status: "accepted" } }, 202))
    await expect(sendMessage(1, "检查运行时健康")).resolves.toEqual({
      run_id: 9,
      status: "accepted",
    })
    const sent = lastRequest()
    expect(sent.url.endsWith("/api/agents/sessions/1/messages")).toBe(true)
    expect(sent.init.method).toBe("POST")
    expect(JSON.parse(String(sent.init.body))).toEqual({ message: "检查运行时健康" })
  })

  it("stops a session", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: { status: "stopping" } }))
    await expect(stopSession(1)).resolves.toEqual({ status: "stopping" })
    const { url, init } = lastRequest()
    expect(url.endsWith("/api/agents/sessions/1/stop")).toBe(true)
    expect(init.method).toBe("POST")
  })

  it("fetches, renames and deletes a session", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: SESSION }))
    await expect(getSession(1)).resolves.toEqual(SESSION)
    expect(lastRequest().url.endsWith("/api/agents/sessions/1")).toBe(true)

    fetchMock.mockResolvedValue(jsonResponse({ data: { ...SESSION, title: "新标题" } }))
    await expect(updateSessionTitle(1, "新标题")).resolves.toMatchObject({ title: "新标题" })
    const patched = lastRequest()
    expect(patched.url.endsWith("/api/agents/sessions/1")).toBe(true)
    expect(patched.init.method).toBe("PATCH")
    expect(JSON.parse(String(patched.init.body))).toEqual({ title: "新标题" })

    fetchMock.mockResolvedValue(new Response(null, { status: 204 }))
    await expect(deleteSession(1)).resolves.toBeUndefined()
    const deleted = lastRequest()
    expect(deleted.url.endsWith("/api/agents/sessions/1")).toBe(true)
    expect(deleted.init.method).toBe("DELETE")
  })

  it("fetches one message detail", async () => {
    const message = {
      id: 42,
      session_id: 1,
      run_id: 9,
      role: "assistant",
      content: [{ type: "text", text: "done" }],
      reasoning: "",
      tool_calls: null,
      tool_call_id: null,
      tool_name: null,
      usage: null,
      created_at: "2026-10-27T00:00:00Z",
    }
    fetchMock.mockResolvedValue(jsonResponse({ data: message }))

    await expect(getMessage(1, 42)).resolves.toEqual(message)
    expect(lastRequest().url.endsWith("/api/agents/sessions/1/messages/42")).toBe(true)
  })

  it("fetches activities from the encoded agent channel", async () => {
    const event = {
      seq: 12,
      channel: "agent:1",
      type: "agent.activity",
      ts: "2026-10-27T00:00:00Z",
      summary: "assistant text",
      detail: { kind: "text" },
    }
    fetchMock.mockResolvedValue(
      jsonResponse({ data: { items: [event], snapshot_cursor: 12, has_more: true } }),
    )

    const page = await fetchActivities(1, { before_seq: 5, limit: 20 })

    expect(page).toEqual({ items: [event], snapshot_cursor: 12, has_more: true })
    const { url, init } = lastRequest()
    expect(url.endsWith("/api/stream/events?channel=agent%3A1&before_seq=5&limit=20")).toBe(true)
    expect(init.method).toBe("GET")
  })

  it("surfaces a 403 as ApiError", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ error: { code: "forbidden", message: "admin only" } }, 403),
    )

    await expect(listProfiles()).rejects.toBeInstanceOf(ApiError)
    await expect(listProfiles()).rejects.toMatchObject({ status: 403 })
  })
})
