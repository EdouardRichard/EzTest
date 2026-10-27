import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError, apiFetch, apiGet, apiGetPage, apiStreamFetch } from "./api-client"
import { getToken, setToken } from "./auth-storage"

function jsonResponse(body: unknown, init: ResponseInit = {}) {
  return new Response(JSON.stringify(body), {
    ...init,
    headers: {
      "content-type": "application/json",
      ...(init.headers as Record<string, string> | undefined),
    },
  })
}

function stubFetch(response: Response) {
  const fetchMock = vi.fn().mockResolvedValue(response)
  vi.stubGlobal("fetch", fetchMock)
  return fetchMock
}

describe("apiFetch", () => {
  beforeEach(() => {
    sessionStorage.clear()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it("wraps error envelope and attaches bearer", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          error: {
            code: "invalid_argument",
            message: "bad",
            request_id: "req_1",
            retryable: false,
          },
        }),
        { status: 400, headers: { "content-type": "application/json" } },
      ),
    )
    vi.stubGlobal("fetch", fetchMock)
    await expect(apiFetch("/api/users", { token: "ezt_x" })).rejects.toMatchObject({
      status: 400,
      code: "invalid_argument",
    })
    expect(fetchMock.mock.calls[0][1].headers.Authorization).toBe("Bearer ezt_x")
  })

  it("uses the session token and unwraps the data envelope", async () => {
    setToken("ezt_session")
    const fetchMock = stubFetch(jsonResponse({ data: { status: "ok" } }))

    await expect(apiFetch<{ status: string }>("/api/health")).resolves.toEqual({ status: "ok" })

    const init = fetchMock.mock.calls[0][1]
    expect(init.headers.Authorization).toBe("Bearer ezt_session")
    expect(init.headers["Content-Type"]).toBeUndefined()
  })

  it("clears the stored token on 401", async () => {
    setToken("ezt_expired")
    stubFetch(
      jsonResponse(
        {
          error: {
            code: "unauthenticated",
            message: "authentication required",
            request_id: "req_401",
            retryable: false,
          },
        },
        { status: 401 },
      ),
    )

    await expect(apiFetch("/api/users")).rejects.toMatchObject({
      status: 401,
      code: "unauthenticated",
    })
    expect(getToken()).toBeNull()
  })

  it("forwards a client request id and falls back to the response request id", async () => {
    const fetchMock = stubFetch(
      jsonResponse(
        {
          error: {
            code: "invalid_argument",
            message: "bad",
            request_id: "",
            retryable: false,
          },
        },
        { status: 400, headers: { "x-request-id": "req_header" } },
      ),
    )

    await expect(apiFetch("/api/users", { requestId: "req_client" })).rejects.toMatchObject({
      requestId: "req_header",
    })
    expect(fetchMock.mock.calls[0][1].headers["X-Request-ID"]).toBe("req_client")
  })

  it("wraps network failures in an ApiError", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("Failed to fetch")))

    const error = await apiFetch("/api/health").catch((caught: unknown) => caught)
    expect(error).toBeInstanceOf(ApiError)
    expect(error).toMatchObject({
      status: 0,
      code: "network_error",
      retryable: true,
    })
  })

  it("returns undefined for 204 responses", async () => {
    stubFetch(new Response(null, { status: 204 }))

    await expect(apiFetch<void>("/api/auth/logout", { method: "POST" })).resolves.toBeUndefined()
  })

  it("keeps the session when a 401 request carried no bearer", async () => {
    setToken("ezt_current")
    stubFetch(
      jsonResponse(
        {
          error: {
            code: "unauthenticated",
            message: "invalid username or password",
            request_id: "req_login",
            retryable: false,
          },
        },
        { status: 401 },
      ),
    )

    await expect(
      apiFetch("/api/auth/login", { method: "POST", token: null, body: "{}" }),
    ).rejects.toMatchObject({ status: 401 })
    expect(getToken()).toBe("ezt_current")
  })
})

describe("apiGetPage", () => {
  beforeEach(() => {
    sessionStorage.clear()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it("preserves the list envelope cursor and has_more fields", async () => {
    const fetchMock = stubFetch(
      jsonResponse({ data: [{ id: "1844674407370955161" }], next_cursor: "c2", has_more: true }),
    )

    await expect(apiGetPage("/api/versions?limit=20")).resolves.toEqual({
      items: [{ id: "1844674407370955161" }],
      nextCursor: "c2",
      hasMore: true,
    })
    expect(fetchMock.mock.calls[0][1].method).toBe("GET")
  })

  it("reads the numeric snapshot_cursor used by the events snapshot", async () => {
    stubFetch(jsonResponse({ data: [{ seq: 7 }], snapshot_cursor: 7, has_more: false }))

    await expect(apiGetPage("/api/versions/1/events?limit=50")).resolves.toEqual({
      items: [{ seq: 7 }],
      nextCursor: "7",
      hasMore: false,
    })
  })

  it("keeps apiGet unwrapping only data for the same envelope", async () => {
    stubFetch(
      jsonResponse({ data: [{ id: "1844674407370955161" }], next_cursor: "c2", has_more: true }),
    )

    await expect(apiGet("/api/versions")).resolves.toEqual([{ id: "1844674407370955161" }])
  })

  it("surfaces list error envelopes as ApiError", async () => {
    stubFetch(
      jsonResponse(
        {
          error: {
            code: "invalid_argument",
            message: "invalid cursor",
            request_id: "req_page",
            retryable: false,
          },
        },
        { status: 400 },
      ),
    )

    await expect(apiGetPage("/api/versions?cursor=bad")).rejects.toMatchObject({
      status: 400,
      code: "invalid_argument",
    })
  })
})

describe("apiStreamFetch", () => {
  beforeEach(() => {
    sessionStorage.clear()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it("returns the raw streaming response with the bearer attached", async () => {
    const fetchMock = stubFetch(
      new Response('event: llm.delta\ndata: {"delta":"hi"}\n\n', {
        status: 200,
        headers: { "content-type": "text/event-stream" },
      }),
    )

    const response = await apiStreamFetch("/api/admin/llm/configs/1/test", {
      method: "POST",
      body: "{}",
      token: "ezt_stream",
    })

    await expect(response.text()).resolves.toContain("llm.delta")
    expect(fetchMock.mock.calls[0][1].headers.Authorization).toBe("Bearer ezt_stream")
    expect(fetchMock.mock.calls[0][1].headers.Accept).toBe("application/json")
  })

  it("wraps stream error envelopes in ApiError", async () => {
    stubFetch(
      jsonResponse(
        {
          error: {
            code: "invalid_argument",
            message: "streaming is disabled for this LLM configuration",
            request_id: "req_stream",
            retryable: false,
          },
        },
        { status: 400 },
      ),
    )

    await expect(
      apiStreamFetch("/api/admin/llm/configs/1/test", { method: "POST", body: "{}" }),
    ).rejects.toMatchObject({
      status: 400,
      code: "invalid_argument",
      message: "streaming is disabled for this LLM configuration",
    })
  })
})
