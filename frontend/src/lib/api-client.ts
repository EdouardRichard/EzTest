import { clearSession, getToken } from "./auth-storage"

/** Fallback API origin used when NEXT_PUBLIC_API_BASE_URL is not configured. */
export const DEFAULT_API_BASE_URL = "http://localhost:13333"

/** Structured, non-sensitive error returned by the shared API error envelope. */
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
 * ApiError carries the API's error envelope (`code`, `request_id`, `retryable`,
 * `details`) instead of exposing the raw Response to feature code.
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
  /** Explicit token override. `null` means "do not attach a token". */
  token?: string | null
  /** Optional X-Request-ID sent with the request for end-to-end correlation. */
  requestId?: string
  /** Optional Idempotency-Key for write requests. */
  idempotencyKey?: string
}

type HeaderRecord = Record<string, string>

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

function toHeaderRecord(headers?: HeadersInit): HeaderRecord {
  const record: HeaderRecord = {}
  if (!headers) return record
  new Headers(headers).forEach((value, key) => {
    record[key] = value
  })
  return record
}

function findHeaderName(headers: HeaderRecord, name: string): string | undefined {
  const lower = name.toLowerCase()
  return Object.keys(headers).find((key) => key.toLowerCase() === lower)
}

function getHeader(headers: HeaderRecord, name: string): string | undefined {
  const key = findHeaderName(headers, name)
  return key === undefined ? undefined : headers[key]
}

function setHeader(headers: HeaderRecord, name: string, value: string): void {
  const existing = findHeaderName(headers, name)
  if (existing !== undefined && existing !== name) delete headers[existing]
  headers[name] = value
}

function fallbackCode(status: number): string {
  if (status === 401) return "unauthenticated"
  if (status === 403) return "forbidden"
  if (status === 404) return "not_found"
  if (status === 409) return "conflict"
  if (status === 429) return "rate_limited"
  if (status === 503) return "unavailable"
  if (status >= 500) return "internal"
  return "invalid_argument"
}

function fallbackMessage(status: number): string {
  if (status === 0) return "network request failed"
  if (status === 401) return "authentication required"
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

/**
 * The single HTTP entry point for the web app. It prefixes the configured API
 * origin, attaches the bearer token, unwraps `{"data": ...}`, turns error
 * envelopes into ApiError, and clears the local session on 401.
 */
export async function apiFetch<T>(path: string, init: ApiFetchOptions = {}): Promise<T> {
  const { token, requestId, idempotencyKey, ...requestInit } = init
  const headers = toHeaderRecord(requestInit.headers)

  const bearer = token === undefined ? getToken() : token
  if (bearer) setHeader(headers, "Authorization", `Bearer ${bearer}`)
  if (!getHeader(headers, "Accept")) setHeader(headers, "Accept", "application/json")
  if (
    requestInit.body !== undefined &&
    requestInit.body !== null &&
    typeof requestInit.body === "string" &&
    !getHeader(headers, "Content-Type")
  ) {
    setHeader(headers, "Content-Type", "application/json")
  }
  if (requestId && !getHeader(headers, "X-Request-ID")) {
    setHeader(headers, "X-Request-ID", requestId)
  }
  if (idempotencyKey && !getHeader(headers, "Idempotency-Key")) {
    setHeader(headers, "Idempotency-Key", idempotencyKey)
  }

  let response: Response
  try {
    response = await fetch(resolveApiUrl(path), { ...requestInit, headers })
  } catch (cause) {
    throw new ApiError({
      status: 0,
      code: "network_error",
      message: cause instanceof Error ? cause.message : fallbackMessage(0),
      requestId: requestId ?? "",
      retryable: true,
    })
  }

  if (response.status === 401) clearSession()
  if (!response.ok) throw await toApiError(response)
  if (response.status === 204) return undefined as T

  const text = await response.text()
  if (text.length === 0) return undefined as T

  let payload: unknown
  try {
    payload = JSON.parse(text)
  } catch {
    throw new ApiError({
      status: response.status,
      code: "invalid_response",
      message: "the API returned a non-JSON response",
      requestId: response.headers.get("x-request-id") ?? "",
      retryable: false,
    })
  }

  return unwrapData<T>(payload)
}

type RequestOptions = Omit<ApiFetchOptions, "method" | "body">

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

export function apiPut<T>(path: string, body?: unknown, init: RequestOptions = {}): Promise<T> {
  return apiFetch<T>(path, {
    ...init,
    method: "PUT",
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

export function apiDelete<T>(path: string, init: RequestOptions = {}): Promise<T> {
  return apiFetch<T>(path, { ...init, method: "DELETE" })
}
