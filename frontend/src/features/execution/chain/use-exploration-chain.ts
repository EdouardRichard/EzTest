"use client"

import { useCallback, useEffect, useRef, useState } from "react"

import type { VersionStreamState } from "@/features/versions/useVersionStream"
import { useVersionStream } from "@/features/versions/useVersionStream"
import { apiGet } from "@/lib/api-client"
import type { SSEStatus } from "@/lib/sse-client"

import type { ChainSnapshot } from "./chain-types"

/** Default / maximum node budgets of the read model (spec §5.1). */
export const CHAIN_DEFAULT_LIMIT = 300
export const CHAIN_MIN_LIMIT = 50
export const CHAIN_MAX_LIMIT = 2000
export const CHAIN_DEFAULT_REQUIREMENT_LIMIT = 100
export const CHAIN_MIN_REQUIREMENT_LIMIT = 1
export const CHAIN_MAX_REQUIREMENT_LIMIT = 500

/** SSE bursts are coalesced into one read-model refresh per window (spec §5.5). */
export const CHAIN_REFRESH_DEBOUNCE_MS = 500

/** Search is capped at 100 runes by the backend (spec §5.1). */
const CHAIN_SEARCH_MAX_RUNES = 100

export type ExplorationChainParams = {
  /** Total node budget; clamped to 50–2000. Defaults to 300. */
  limit?: number
  /** Requirement-root budget; clamped to 1–500. Defaults to 100. */
  requirementLimit?: number
  /** Batch scope: a decimal id or `"all"`; omitted means the latest batch. */
  batchId?: string
  /** Narrows the graph to one requirement group. */
  requirementId?: string
  /** Terminal result status (`passed`/`failed`/…); pending/running are not filters. */
  resultStatus?: string
  /** Keeps only failed/blocked/inconclusive/interrupted results. */
  abnormalOnly?: boolean
  issueStatus?: string
  issueSeverity?: string
  /** Requirement/case title search; trimmed to 100 runes. */
  q?: string
  /** Whether AI-supplement cases are returned; the backend default is true. */
  includeSupplement?: boolean
  revalidationOnly?: boolean
}

export type ExplorationChainResult = {
  data: ChainSnapshot | null
  loading: boolean
  error: string
  streamStatus: SSEStatus
  refresh: () => Promise<void>
}

/** Event families that can invalidate the chain read model (spec §5.5). */
const CHAIN_REFRESH_EVENT_PREFIXES: readonly string[] = [
  "case.",
  "run.",
  "batch.",
  "issue.",
  "report.",
]

/** True when a version event may have changed the exploration chain. */
export function isChainRefreshEvent(type: string): boolean {
  if (type === "code.changed") return true
  return CHAIN_REFRESH_EVENT_PREFIXES.some((prefix) => type.startsWith(prefix))
}

function clampInteger(
  value: number | undefined,
  fallback: number,
  min: number,
  max: number,
): number {
  if (value === undefined || !Number.isFinite(value)) return fallback
  return Math.min(max, Math.max(min, Math.trunc(value)))
}

function trimToRunes(value: string | undefined, maxRunes: number): string {
  const trimmed = (value ?? "").trim()
  if (trimmed === "") return ""
  const runes = Array.from(trimmed)
  return runes.length <= maxRunes ? trimmed : runes.slice(0, maxRunes).join("")
}

function trimValue(value: string | undefined): string {
  return (value ?? "").trim()
}

type NormalizedChainParams = {
  limit: number
  requirementLimit: number
  batchId: string
  requirementId: string
  resultStatus: string
  abnormalOnly: boolean
  issueStatus: string
  issueSeverity: string
  q: string
  includeSupplement: boolean
  revalidationOnly: boolean
}

/** Applies the spec §5.1 bounds/defaults before a request leaves the client. */
export function normalizeChainParams(params: ExplorationChainParams): NormalizedChainParams {
  return {
    limit: clampInteger(params.limit, CHAIN_DEFAULT_LIMIT, CHAIN_MIN_LIMIT, CHAIN_MAX_LIMIT),
    requirementLimit: clampInteger(
      params.requirementLimit,
      CHAIN_DEFAULT_REQUIREMENT_LIMIT,
      CHAIN_MIN_REQUIREMENT_LIMIT,
      CHAIN_MAX_REQUIREMENT_LIMIT,
    ),
    batchId: trimValue(params.batchId),
    requirementId: trimValue(params.requirementId),
    resultStatus: trimValue(params.resultStatus),
    abnormalOnly: params.abnormalOnly === true,
    issueStatus: trimValue(params.issueStatus),
    issueSeverity: trimValue(params.issueSeverity),
    q: trimToRunes(params.q, CHAIN_SEARCH_MAX_RUNES),
    includeSupplement: params.includeSupplement !== false,
    revalidationOnly: params.revalidationOnly === true,
  }
}

/** Builds the read-model path; only non-default query parameters are appended. */
export function buildChainPath(versionId: string, params: NormalizedChainParams): string {
  const query = new URLSearchParams()
  query.set("limit", String(params.limit))
  query.set("requirementLimit", String(params.requirementLimit))
  if (params.batchId !== "") query.set("batchId", params.batchId)
  if (params.requirementId !== "") query.set("requirementId", params.requirementId)
  if (params.resultStatus !== "") query.set("resultStatus", params.resultStatus)
  if (params.abnormalOnly) query.set("abnormalOnly", "true")
  if (params.issueStatus !== "") query.set("issueStatus", params.issueStatus)
  if (params.issueSeverity !== "") query.set("issueSeverity", params.issueSeverity)
  if (params.q !== "") query.set("q", params.q)
  // The backend defaults includeSupplement to true; only send the deviation.
  if (!params.includeSupplement) query.set("includeSupplement", "false")
  if (params.revalidationOnly) query.set("revalidationOnly", "true")
  return `/api/versions/${encodeURIComponent(versionId)}/exploration-chain?${query.toString()}`
}

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === "AbortError"
}

function describeChainLoadError(error: unknown): string {
  if (error instanceof Error && error.message.trim() !== "") return error.message
  return "未知错误"
}

/**
 * Loads the P8 exploration-chain read model (spec §5.1) and keeps it fresh from
 * the version SSE channel (spec §5.5):
 *
 * - every read carries the `limit=300` / `requirementLimit=100` defaults and all
 *   server-side filters; the client never sends a limit above 2000;
 * - `case.*` / `run.*` / `batch.*` / `issue.*` / `report.*` / `code.changed`
 *   events are coalesced into one refresh per 500ms window;
 * - a failed refresh keeps the previously rendered snapshot and exposes the
 *   error instead of replacing it with an empty state;
 * - unmount/param change aborts the in-flight request, and an aborted request
 *   can never write into the hook state.
 *
 * A caller that already owns the version stream (VersionDetailPage does) passes
 * it in; standalone callers get their own connection. The hook calls
 * `useVersionStream("")` in the provided case so hook order stays stable.
 */
export function useExplorationChain(
  versionId: string,
  params: ExplorationChainParams = {},
  stream?: VersionStreamState,
): ExplorationChainResult {
  const ownStream = useVersionStream(stream === undefined ? versionId : "")
  const activeStream = stream ?? ownStream

  const [data, setData] = useState<ChainSnapshot | null>(null)
  const [loading, setLoading] = useState(versionId !== "")
  const [error, setError] = useState("")

  const abortRef = useRef<AbortController | null>(null)
  const requestRef = useRef(0)
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const lastSeqRef = useRef(0)
  const versionRef = useRef(versionId)

  const normalized = normalizeChainParams(params)
  // The normalized object is recreated on each render; its serialized path is
  // the stable request identity `load` (and therefore the effect) depends on.
  const requestPath = buildChainPath(versionId, normalized)

  const load = useCallback(async (): Promise<void> => {
    if (versionId === "") {
      setData(null)
      setLoading(false)
      setError("")
      return
    }

    const request = requestRef.current + 1
    requestRef.current = request
    abortRef.current?.abort()
    abortRef.current = null

    const controller = new AbortController()
    abortRef.current = controller
    // The loading flag only gates the skeleton when there is no data yet, so
    // a background refresh keeps the rendered canvas in place.
    setLoading(true)
    setError("")

    try {
      const next = await apiGet<ChainSnapshot>(requestPath, { signal: controller.signal })
      if (controller.signal.aborted || requestRef.current !== request) return
      setData(next)
      setError("")
    } catch (caught) {
      if (controller.signal.aborted || requestRef.current !== request) return
      if (isAbortError(caught)) return
      // Keep the previous snapshot: a transient read failure must not turn a
      // populated chain into a misleading empty state (spec §5.5).
      setError(describeChainLoadError(caught))
    } finally {
      if (requestRef.current === request) {
        if (abortRef.current === controller) abortRef.current = null
        setLoading(false)
      }
    }
  }, [versionId, requestPath])

  // A version switch starts a fresh view; the previous version's nodes must
  // never be attributed to the new one.
  useEffect(() => {
    if (versionRef.current === versionId) return
    versionRef.current = versionId
    setData(null)
    lastSeqRef.current = 0
  }, [versionId])

  useEffect(() => {
    requestRef.current += 1
    abortRef.current?.abort()
    abortRef.current = null
    if (timerRef.current !== null) {
      clearTimeout(timerRef.current)
      timerRef.current = null
    }
    if (versionId === "") {
      setData(null)
      setLoading(false)
      setError("")
      return
    }

    setLoading(true)
    setError("")
    void load()

    return () => {
      requestRef.current += 1
      abortRef.current?.abort()
      abortRef.current = null
    }
  }, [versionId, load])

  // Live chain events invalidate the projection. One trailing window absorbs a
  // burst; events that arrive after it schedule the next refresh.
  useEffect(() => {
    const events = activeStream.events
    if (events.length === 0) return

    let maxSeq = 0
    let hasRefreshEvent = false
    for (const event of events) {
      if (event.version_id !== null && event.version_id !== versionId) continue
      if (event.seq > maxSeq) maxSeq = event.seq
      if (event.seq > lastSeqRef.current && isChainRefreshEvent(event.type)) {
        hasRefreshEvent = true
      }
    }
    if (maxSeq <= lastSeqRef.current) return
    lastSeqRef.current = maxSeq
    if (!hasRefreshEvent || versionId === "") return
    if (timerRef.current !== null) return

    timerRef.current = setTimeout(() => {
      timerRef.current = null
      void load()
    }, CHAIN_REFRESH_DEBOUNCE_MS)
  }, [activeStream.events, versionId, load])

  useEffect(() => {
    return () => {
      if (timerRef.current !== null) {
        clearTimeout(timerRef.current)
        timerRef.current = null
      }
      abortRef.current?.abort()
      abortRef.current = null
    }
  }, [])

  const refresh = useCallback(async (): Promise<void> => {
    await load()
  }, [load])

  return {
    data,
    loading,
    error,
    streamStatus: activeStream.status,
    refresh,
  }
}
