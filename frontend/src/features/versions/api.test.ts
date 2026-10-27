import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { setToken } from "@/lib/auth-storage"

import {
  createVersion,
  enterDesign,
  fetchVersionEvents,
  getVersion,
  listVersions,
  putBindings,
  startClarification,
  updateVersion,
} from "./api"
import type { Version, VersionEvent } from "./types"

/** A snowflake ID that a JS number cannot hold; it must stay a string verbatim. */
const VERSION_ID = "1844674407370955161"
const OTHER_VERSION_ID = "1844674407370955162"

const VERSION: Version = {
  id: VERSION_ID,
  name: "订单系统 2.0",
  label: "v2.0",
  description: "",
  status: "clarifying",
  systemId: 1,
  environmentId: 2,
  codeRepositoryId: 3,
  codeBranch: "main",
  codeBaselineCommit: "0123456789abcdef0123456789abcdef01234567",
  bindings: {
    system: { id: 1, name: "订单系统", available: true },
    environment: { id: 2, name: "staging", available: true },
    codeRepository: { id: 3, name: "order-web", available: true },
  },
  requirementCount: 3,
  clarifiedCount: 1,
  openQuestionCount: 2,
  revision: 2,
  createdBy: 1,
  updatedBy: 1,
  createdAt: "2026-10-27T10:00:00Z",
  updatedAt: "2026-10-27T10:00:00Z",
}

const EVENT: VersionEvent = {
  seq: 7,
  channel: `version:${VERSION_ID}`,
  type: "requirement.added",
  version_id: VERSION_ID,
  batch_id: null,
  case_id: null,
  step_id: null,
  agent_role: null,
  session_id: null,
  ts: "2026-10-27T10:00:00Z",
  summary: "新增需求",
  detail: { requirementId: OTHER_VERSION_ID },
  refs: { evidence: [], case: [], requirement: [OTHER_VERSION_ID], code: [] },
}

const fetchMock = vi.fn()

function jsonResponse(payload: unknown, status = 200): Response {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "content-type": "application/json" },
  })
}

type CapturedRequest = {
  url: string
  method: string
  headers: Record<string, string>
  body: string | undefined
}

/** Returns the URL, method, headers and body of the most recent fetch call. */
function lastRequest(): CapturedRequest {
  const [url, init] = fetchMock.mock.calls.at(-1) as [string, RequestInit]
  return {
    url: String(url),
    method: String(init.method),
    headers: (init.headers ?? {}) as Record<string, string>,
    body: typeof init.body === "string" ? init.body : undefined,
  }
}

describe("versions api", () => {
  beforeEach(() => {
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
    setToken("test-token")
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it("lists versions with filters and keeps the page envelope", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ data: [VERSION], next_cursor: "cursor-2", has_more: true }),
    )

    const page = await listVersions({ status: "clarifying", limit: 20, cursor: "cursor-1" })

    expect(page.items).toEqual([VERSION])
    expect(page.nextCursor).toBe("cursor-2")
    expect(page.hasMore).toBe(true)
    expect(typeof page.items[0].id).toBe("string")
    expect(page.items[0].id).toBe(VERSION_ID)

    const request = lastRequest()
    expect(request.url.endsWith("/api/versions?status=clarifying&limit=20&cursor=cursor-1")).toBe(
      true,
    )
    expect(request.method).toBe("GET")
    expect(request.headers.Authorization).toBe("Bearer test-token")
  })

  it("creates a version with P2 resource IDs as numbers", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: VERSION }, 201))

    const created = await createVersion({
      name: "订单系统 2.0",
      label: "v2.0",
      systemId: 1,
      environmentId: 2,
      codeRepositoryId: 3,
      codeBranch: "main",
    })

    expect(created).toEqual(VERSION)
    const request = lastRequest()
    expect(request.url.endsWith("/api/versions")).toBe(true)
    expect(request.method).toBe("POST")
    expect(request.body === undefined ? undefined : JSON.parse(request.body)).toEqual({
      name: "订单系统 2.0",
      label: "v2.0",
      systemId: 1,
      environmentId: 2,
      codeRepositoryId: 3,
      codeBranch: "main",
    })
  })

  it("reads and patches a version by its raw string id", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: VERSION }))
    await expect(getVersion(VERSION_ID)).resolves.toEqual(VERSION)
    expect(lastRequest().url.endsWith(`/api/versions/${VERSION_ID}`)).toBe(true)
    expect(lastRequest().method).toBe("GET")

    fetchMock.mockResolvedValue(
      jsonResponse({ data: { ...VERSION, name: "订单系统 3.0", revision: 3 } }),
    )
    const updated = await updateVersion(VERSION_ID, { revision: 2, name: "订单系统 3.0" })

    expect(updated.revision).toBe(3)
    const request = lastRequest()
    expect(request.url.endsWith(`/api/versions/${VERSION_ID}`)).toBe(true)
    expect(request.method).toBe("PATCH")
    expect(request.body === undefined ? undefined : JSON.parse(request.body)).toEqual({
      revision: 2,
      name: "订单系统 3.0",
    })
  })

  it("replaces bindings with a revision and explicit nulls", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        data: {
          ...VERSION,
          systemId: null,
          environmentId: null,
          codeRepositoryId: null,
          bindings: { system: null, environment: null, codeRepository: null },
          revision: 4,
        },
      }),
    )

    const bound = await putBindings(VERSION_ID, {
      revision: 3,
      systemId: null,
      codeBranch: "release",
      codeBaselineCommit: null,
    })

    expect(bound.bindings.system).toBeNull()
    const request = lastRequest()
    expect(request.url.endsWith(`/api/versions/${VERSION_ID}/bindings`)).toBe(true)
    expect(request.method).toBe("PUT")
    expect(request.body === undefined ? undefined : JSON.parse(request.body)).toEqual({
      revision: 3,
      systemId: null,
      codeBranch: "release",
      codeBaselineCommit: null,
    })
  })

  it("starts clarification without a request body", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: VERSION }))

    await expect(startClarification(VERSION_ID)).resolves.toEqual(VERSION)

    const request = lastRequest()
    expect(request.url.endsWith(`/api/versions/${VERSION_ID}/start-clarification`)).toBe(true)
    expect(request.method).toBe("POST")
    expect(request.body).toBeUndefined()
  })

  it("enters design with the acknowledgement flag and returns warnings", async () => {
    const designing: Version = { ...VERSION, status: "designing", revision: 5 }
    fetchMock.mockResolvedValue(
      jsonResponse({ data: { version: designing, warnings: ["2 个需求未澄清"] } }),
    )

    const result = await enterDesign(VERSION_ID, true)

    expect(result.version.status).toBe("designing")
    expect(result.warnings).toEqual(["2 个需求未澄清"])
    const request = lastRequest()
    expect(request.url.endsWith(`/api/versions/${VERSION_ID}/enter-design`)).toBe(true)
    expect(request.method).toBe("POST")
    expect(request.body === undefined ? undefined : JSON.parse(request.body)).toEqual({
      acknowledgeUnclarified: true,
    })

    fetchMock.mockResolvedValue(jsonResponse({ data: { version: designing } }))
    await expect(enterDesign(VERSION_ID, false)).resolves.toEqual({
      version: designing,
      warnings: [],
    })
  })

  it("fetches the event snapshot with the snapshot cursor preserved", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: [EVENT], snapshot_cursor: 7, has_more: true }))

    const page = await fetchVersionEvents(VERSION_ID, { limit: 50 })

    expect(page).toEqual({ events: [EVENT], cursor: 7, hasMore: true })
    expect(page.events[0].version_id).toBe(VERSION_ID)
    const request = lastRequest()
    expect(request.url.endsWith(`/api/versions/${VERSION_ID}/events?limit=50`)).toBe(true)
    expect(request.method).toBe("GET")
  })
})
