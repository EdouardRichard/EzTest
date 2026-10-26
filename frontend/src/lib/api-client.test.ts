import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError, apiFetch } from "./api-client"
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
})
