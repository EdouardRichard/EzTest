import { clearSession, getToken } from "./auth-storage"

/** Fallback API origin used when NEXT_PUBLIC_API_BASE_URL is not configured. */
export const DEFAULT_API_BASE_URL = "http://localhost:13333"

export type ApiErrorDetails = Record<string, unknown>

export type ApiErrorInit = {
  status: number
  code: string
  message: string
  requestId?: string
  retryable?: boolean
  details?: ApiErrorDetails
}

/**
 * ApiError carries the API's error envelope (`code`, `message`, `request_id`,
 * `retryable`, `details`) instead of exposing the raw Response to feature code.
 */
export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly requestId: string
  readonly retryable: boolean
  readonly details: ApiErrorDetails

  constructor(init: ApiErrorInit) {
    super(init.message)
    this.name = "ApiError"
    this.status = init.status
    this.code = init.code
    this.requestId = init.requestId ?? ""
    this.retryable = init.retryable ?? false
    this.details = init.details ?? {}
  }
}

export type ApiFetchOptions = RequestInit & {
  /** Optional X-Request-ID sent with the request for end-to-end correlation. */
  requestId?: string
  /** Optional Idempotency-Key for write requests. */
  idempotencyKey?: string
}

export function getApiBaseUrl(): string {
  const configured = process.env.NEXT_PUBLIC_API_BASE_URL?.trim()
  return (configured ? configured : DEFAULT_API_BASE_URL).replace(/\/+$/, "")
}

/** Resolves an API path against NEXT_PUBLIC_API_BASE_URL. */
export function resolveApiUrl(path: string): string {
  if (/^https?:\/\//i.test(path)) return path
  return `${getApiBaseUrl()}${path.startsWith("/") ? path : `/${path}`}`
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

function randomHex(byteLength: number): string {
  const bytes = new Uint8Array(byteLength)
  if (typeof crypto !== "undefined" && typeof crypto.getRandomValues === "function") {
    crypto.getRandomValues(bytes)
  } else {
    for (let i = 0; i < bytes.length; i += 1) bytes[i] = Math.floor(Math.random() * 256)
  }
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("")
}

/** Creates a `req_` + 12-hex correlation id matching the backend format. */
export function createRequestId(): string {
  return `req_${randomHex(6)}`
}

function fallbackCode(status: number): string {
  if (status === 401) return "unauthenticated"
  if (status === 403) return "forbidden"
  if (status === 404) return "not_found"
  if (status === 409) return "conflict"
  if (status === 413) return "too_large"
  if (status === 429) return "rate_limited"
  if (status === 503) return "unavailable"
  if (status >= 500) return "internal"
  return "invalid_argument"
}

function fallbackMessage(status: number): string {
  if (status === 0) return "network request failed"
  if (status === 401) return "authentication required"
  if (status === 403) return "permission denied"
  if (status === 404) return "resource not found"
  return status >= 500 ? "the API is unavailable" : "the request was rejected"
}

async function toApiError(response: Response): Promise<ApiError> {
  const headerRequestId = response.headers.get("x-request-id") ?? ""
  let body: unknown = null
  try {
    body = await response.json()
  } catch {
    body = null
  }

  const envelope = isRecord(body) && isRecord(body.error) ? body.error : null
  const code =
    typeof envelope?.code === "string" && envelope.code.length > 0
      ? envelope.code
      : fallbackCode(response.status)
  const message =
    typeof envelope?.message === "string" && envelope.message.length > 0
      ? envelope.message
      : fallbackMessage(response.status)
  const requestId =
    typeof envelope?.request_id === "string" && envelope.request_id.length > 0
      ? envelope.request_id
      : headerRequestId
  const retryable =
    typeof envelope?.retryable === "boolean"
      ? envelope.retryable
      : response.status === 429 || response.status >= 500
  const details = isRecord(envelope?.details) ? envelope.details : {}

  return new ApiError({
    status: response.status,
    code,
    message,
    requestId,
    retryable,
    details,
  })
}

function unwrapData<T>(payload: unknown): T {
  if (isRecord(payload) && "data" in payload) return payload.data as T
  return payload as T
}

type PreparedRequest = {
  url: string
  init: RequestInit
  bearer: string | null
  requestId: string
}

function prepareRequest(path: string, init: ApiFetchOptions): PreparedRequest {
  const { requestId, idempotencyKey, ...requestInit } = init
  const headers = new Headers(requestInit.headers)

  const bearer = getToken()
  if (bearer) headers.set("Authorization", `Bearer ${bearer}`)
  if (!headers.has("Accept")) headers.set("Accept", "application/json")
  if (
    requestInit.body !== undefined &&
    requestInit.body !== null &&
    typeof requestInit.body === "string" &&
    !headers.has("Content-Type")
  ) {
    headers.set("Content-Type", "application/json")
  }
  const resolvedRequestId = requestId ?? createRequestId()
  if (!headers.has("X-Request-ID")) headers.set("X-Request-ID", resolvedRequestId)
  if (idempotencyKey) headers.set("Idempotency-Key", idempotencyKey)

  return {
    url: resolveApiUrl(path),
    init: { ...requestInit, headers },
    bearer: bearer ?? null,
    requestId: resolvedRequestId,
  }
}

async function performRequest(path: string, init: ApiFetchOptions): Promise<Response> {
  const prepared = prepareRequest(path, init)
  let response: Response
  try {
    response = await fetch(prepared.url, prepared.init)
  } catch (cause) {
    throw new ApiError({
      status: 0,
      code: "network_error",
      message: cause instanceof Error ? cause.message : fallbackMessage(0),
      requestId: prepared.requestId,
      retryable: true,
    })
  }
  if (response.status === 401 && prepared.bearer) clearSession()
  return response
}

async function requestPayload(path: string, init: ApiFetchOptions): Promise<unknown> {
  const response = await performRequest(path, init)
  if (!response.ok) throw await toApiError(response)
  if (response.status === 204) return undefined

  const text = await response.text()
  if (text.length === 0) return undefined
  try {
    return JSON.parse(text)
  } catch {
    throw new ApiError({
      status: response.status,
      code: "invalid_response",
      message: "the API returned a non-JSON response",
      requestId: response.headers.get("x-request-id") ?? "",
      retryable: false,
    })
  }
}

/**
 * The single HTTP entry point for the web app. It prefixes the configured API
 * origin, attaches the bearer token, sends a correlation id, unwraps
 * `{"data": ...}`, turns error envelopes into ApiError, and clears the local
 * session on an authenticated 401.
 */
export async function apiFetch<T>(path: string, init: ApiFetchOptions = {}): Promise<T> {
  return unwrapData<T>(await requestPayload(path, init))
}

/** One cursor page from a list endpoint (spec §7.1: `{data, next_cursor, has_more}`). */
export type ApiPage<T> = {
  items: T[]
  nextCursor: string
  hasMore: boolean
}

function readPageCursor(payload: Record<string, unknown>): string {
  const raw = payload.next_cursor ?? payload.snapshot_cursor
  if (typeof raw === "string") return raw
  if (typeof raw === "number" && Number.isFinite(raw)) return String(raw)
  return ""
}

/**
 * Fetches one page of a list endpoint while preserving the pagination envelope
 * that apiFetch deliberately unwraps.
 */
export async function apiGetPage<T>(path: string, init: ApiFetchOptions = {}): Promise<ApiPage<T>> {
  const payload = await requestPayload(path, { ...init, method: "GET" })
  if (!isRecord(payload)) return { items: [], nextCursor: "", hasMore: false }
  return {
    items: Array.isArray(payload.data) ? (payload.data as T[]) : [],
    nextCursor: readPageCursor(payload),
    hasMore: payload.has_more === true,
  }
}

type RequestOptions = Omit<ApiFetchOptions, "method" | "body">

function decodeFilename(value: string): string {
  const encoded = /filename\*=UTF-8''([^;]+)/i.exec(value)
  if (encoded !== null) {
    const candidate = encoded[1].trim().replace(/^"|"$/g, "")
    try {
      return decodeURIComponent(candidate)
    } catch {
      return candidate
    }
  }
  const quoted = /filename="([^"]*)"/i.exec(value)
  if (quoted !== null) return quoted[1]
  const bare = /filename=([^;]+)/i.exec(value)
  return bare === null ? "" : bare[1].trim()
}

/** One authenticated binary download (export bytes). */
export type ApiDownload = {
  blob: Blob
  /** Resolved from `Content-Disposition`; empty when the header is absent. */
  filename: string
  contentType: string
}

/**
 * Downloads a binary endpoint through the same bearer/401/error-envelope path
 * as `apiFetch`. Feature code never calls `fetch` directly and never has to
 * parse `Content-Disposition` itself (spec §7.4).
 */
export async function apiDownload(path: string, init: RequestOptions = {}): Promise<ApiDownload> {
  const response = await performRequest(path, { ...init, method: "GET" })
  if (!response.ok) throw await toApiError(response)
  return {
    blob: await response.blob(),
    filename: decodeFilename(response.headers.get("content-disposition") ?? ""),
    contentType: response.headers.get("content-type") ?? "",
  }
}

export function apiGet<T>(path: string, init: RequestOptions = {}): Promise<T> {
  return apiFetch<T>(path, { ...init, method: "GET" })
}

export function apiPost<T>(path: string, body?: unknown, init: RequestOptions = {}): Promise<T> {
  return apiFetch<T>(path, {
    ...init,
    method: "POST",
    body: body === undefined ? undefined : JSON.stringify(body),
  })
}

export function apiPatch<T>(path: string, body?: unknown, init: RequestOptions = {}): Promise<T> {
  return apiFetch<T>(path, {
    ...init,
    method: "PATCH",
    body: body === undefined ? undefined : JSON.stringify(body),
  })
}

export function apiPut<T>(path: string, body?: unknown, init: RequestOptions = {}): Promise<T> {
  return apiFetch<T>(path, {
    ...init,
    method: "PUT",
    body: body === undefined ? undefined : JSON.stringify(body),
  })
}

export function apiDelete<T>(path: string, init: RequestOptions = {}): Promise<T> {
  return apiFetch<T>(path, { ...init, method: "DELETE" })
}

/**
 * Uploads a multipart body (typically `FormData`) through the same entry point
 * as JSON calls. The browser owns the multipart `Content-Type` boundary, so no
 * JSON header is attached.
 */
export function apiUpload<T>(
  path: string,
  formData: FormData,
  init: RequestOptions = {},
): Promise<T> {
  return apiFetch<T>(path, { ...init, method: "POST", body: formData })
}
