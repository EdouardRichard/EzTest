import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import {
  ApiError,
  apiDelete,
  apiFetch,
  apiGetPage,
  apiPatch,
  apiPost,
  getApiBaseUrl,
  resolveApiUrl,
} from "./api-client"
import { AUTH_CLEARED_EVENT, getToken, setToken } from "./auth-storage"

function jsonResponse(body: unknown, status = 200, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json", ...headers },
  })
}

function readHeaders(init: RequestInit | undefined): Headers {
  return new Headers(init?.headers)
}

const fetchMock = vi.fn<typeof fetch>()

describe("resolveApiUrl", () => {
  afterEach(() => {
    vi.unstubAllEnvs()
  })

  it("defaults to the local API origin", () => {
    vi.stubEnv("NEXT_PUBLIC_API_BASE_URL", "")
    expect(getApiBaseUrl()).toBe("http://localhost:13333")
    expect(resolveApiUrl("/api/auth/me")).toBe("http://localhost:13333/api/auth/me")
  })

  it("uses the configured origin and trims trailing slashes", () => {
    vi.stubEnv("NEXT_PUBLIC_API_BASE_URL", "http://192.168.1.9:13333/")
    expect(resolveApiUrl("api/health")).toBe("http://192.168.1.9:13333/api/health")
  })

  it("keeps absolute URLs untouched", () => {
    expect(resolveApiUrl("https://example.test/x")).toBe("https://example.test/x")
  })
})

describe("apiFetch", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it("attaches the bearer token, Accept and a request id", async () => {
    setToken("ezt_token")
    fetchMock.mockResolvedValue(jsonResponse({ data: { ok: true } }))

    await expect(apiFetch<{ ok: boolean }>("/api/auth/me")).resolves.toEqual({ ok: true })

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/auth/me")
    const headers = readHeaders(init)
    expect(headers.get("Authorization")).toBe("Bearer ezt_token")
    expect(headers.get("Accept")).toBe("application/json")
    expect(headers.get("X-Request-ID")).toMatch(/^req_[0-9a-f]{12}$/)
  })

  it("generates a distinct request id per call and honours an explicit one", async () => {
    fetchMock.mockImplementation(async () => jsonResponse({ data: null }))

    await apiFetch("/a")
    await apiFetch("/b")
    await apiFetch("/c", { requestId: "req_fixed000000" })

    const first = readHeaders(fetchMock.mock.calls[0][1]).get("X-Request-ID")
    const second = readHeaders(fetchMock.mock.calls[1][1]).get("X-Request-ID")
    const third = readHeaders(fetchMock.mock.calls[2][1]).get("X-Request-ID")
    expect(first).not.toBe(second)
    expect(third).toBe("req_fixed000000")
  })

  it("unwraps the data envelope", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: [1, 2, 3] }))
    await expect(apiFetch<number[]>("/api/numbers")).resolves.toEqual([1, 2, 3])
  })

  it("returns undefined for a 204 response", async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }))
    await expect(apiFetch("/api/x")).resolves.toBeUndefined()
  })

  it("maps the shared error envelope to ApiError", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(
        {
          error: {
            code: "invalid_argument",
            message: "title is required",
            request_id: "req_body",
            retryable: false,
            details: { field: "title" },
          },
        },
        400,
      ),
    )

    const error = await apiFetch("/api/versions").catch((cause: unknown) => cause)
    expect(error).toBeInstanceOf(ApiError)
    const apiError = error as ApiError
    expect(apiError.status).toBe(400)
    expect(apiError.code).toBe("invalid_argument")
    expect(apiError.message).toBe("title is required")
    expect(apiError.requestId).toBe("req_body")
    expect(apiError.retryable).toBe(false)
    expect(apiError.details).toEqual({ field: "title" })
  })

  it("falls back to HTTP semantics when the body is not an error envelope", async () => {
    fetchMock.mockResolvedValue(
      new Response("oops", { status: 503, headers: { "X-Request-ID": "req_header" } }),
    )

    const error = (await apiFetch("/api/x").catch((cause: unknown) => cause)) as ApiError
    expect(error.code).toBe("unavailable")
    expect(error.requestId).toBe("req_header")
    expect(error.retryable).toBe(true)
    expect(error.details).toEqual({})
  })

  it("wraps network failures as a retryable ApiError", async () => {
    fetchMock.mockRejectedValue(new Error("connection refused"))

    const error = (await apiFetch("/api/x").catch((cause: unknown) => cause)) as ApiError
    expect(error).toBeInstanceOf(ApiError)
    expect(error.status).toBe(0)
    expect(error.code).toBe("network_error")
    expect(error.retryable).toBe(true)
    expect(error.message).toContain("connection refused")
  })

  it("clears the local session on an authenticated 401", async () => {
    setToken("ezt_expired")
    const cleared = vi.fn()
    window.addEventListener(AUTH_CLEARED_EVENT, cleared)
    fetchMock.mockResolvedValue(
      jsonResponse({ error: { code: "unauthenticated", message: "login required" } }, 401),
    )

    await expect(apiFetch("/api/auth/me")).rejects.toBeInstanceOf(ApiError)

    expect(getToken()).toBeNull()
    expect(cleared).toHaveBeenCalledTimes(1)
    window.removeEventListener(AUTH_CLEARED_EVENT, cleared)
  })

  it("does not dispatch a clear event for a 401 without a bearer", async () => {
    const cleared = vi.fn()
    window.addEventListener(AUTH_CLEARED_EVENT, cleared)
    fetchMock.mockResolvedValue(
      jsonResponse({ error: { code: "unauthenticated", message: "login required" } }, 401),
    )

    await expect(apiFetch("/api/auth/login")).rejects.toBeInstanceOf(ApiError)

    expect(cleared).not.toHaveBeenCalled()
    window.removeEventListener(AUTH_CLEARED_EVENT, cleared)
  })

  it("rejects a success response that is not JSON", async () => {
    fetchMock.mockResolvedValue(new Response("not json", { status: 200 }))
    const error = (await apiFetch("/api/x").catch((cause: unknown) => cause)) as ApiError
    expect(error.code).toBe("invalid_response")
  })

  it("sends JSON bodies and the idempotency key through the helpers", async () => {
    fetchMock.mockImplementation(async () => jsonResponse({ data: { id: "1" } }))

    await apiPost("/api/versions", { title: "v" }, { idempotencyKey: "key-1" })
    await apiPatch("/api/versions/1", { title: "t" })
    await apiDelete("/api/versions/1")

    const [postUrl, postInit] = fetchMock.mock.calls[0]
    expect(postUrl).toBe("http://localhost:13333/api/versions")
    expect(postInit?.method).toBe("POST")
    const postHeaders = readHeaders(postInit)
    expect(postHeaders.get("Content-Type")).toBe("application/json")
    expect(postHeaders.get("Idempotency-Key")).toBe("key-1")
    expect(postInit?.body).toBe(JSON.stringify({ title: "v" }))

    const patchInit = fetchMock.mock.calls[1][1]
    expect(patchInit?.method).toBe("PATCH")
    expect(patchInit?.body).toBe(JSON.stringify({ title: "t" }))

    const deleteInit = fetchMock.mock.calls[2][1]
    expect(deleteInit?.method).toBe("DELETE")
    expect(deleteInit?.body).toBeUndefined()
  })
})

describe("apiGetPage", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it("keeps the pagination envelope alongside the data", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ data: [{ id: "1" }], next_cursor: "1", has_more: true }),
    )

    await expect(apiGetPage<{ id: string }>("/api/versions")).resolves.toEqual({
      items: [{ id: "1" }],
      nextCursor: "1",
      hasMore: true,
    })
  })

  it("reads snapshot_cursor for the events endpoint", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: [], snapshot_cursor: 7, has_more: false }))

    await expect(apiGetPage("/api/events?channel=system")).resolves.toEqual({
      items: [],
      nextCursor: "7",
      hasMore: false,
    })
  })
})
