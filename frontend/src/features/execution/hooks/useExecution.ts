"use client"

import { useCallback, useEffect, useRef, useState } from "react"

import type { VersionEvent } from "@/features/versions/types"
import { useVersionStream, type VersionStreamState } from "@/features/versions/useVersionStream"
import type { SSEStatus } from "@/lib/sse-client"
import {
  cancelBatch,
  createBatch,
  type ExportReportOptions,
  exportReport,
  getBatch,
  getCaseRun,
  getReport,
  listBatches,
  listCaseRuns,
  pauseBatch,
  rerunCase,
  resumeBatch,
} from "../api"
import { describeExecutionError } from "../execution-errors"
import {
  type Batch,
  type BatchQuery,
  type BatchStatus,
  type CaseRun,
  type CaseRunQuery,
  type CaseRunStatus,
  type CreateBatchBody,
  P7_EVENT_TYPES,
  type PauseMode,
  type ReportDownload,
  type ReportView,
} from "../types"

/** At most one live-data refresh per window, whichever event triggers it. */
export const EXECUTION_REFRESH_THROTTLE_MS = 500

/** Batch statuses that count as "active" for the header/start-batch UI. */
const ACTIVE_BATCH_STATUSES: readonly BatchStatus[] = ["planned", "running"]

/** P7 event families that always invalidate the execution projection (spec §8.3). */
const P7_EVENT_FAMILIES: readonly string[] = ["issue.", "report.", "analysis."]

/** P7 non-family event types (the two case lifecycle events of spec §8.1). */
const P7_CASE_EVENT_TYPES = new Set<string>(
  P7_EVENT_TYPES.filter((type) => type.startsWith("case.")),
)

/**
 * SSE event types that can change batches, case runs, issues, analyses or the
 * report (spec §5.10, §8.3). Exported so the UI refresh contract is pinned by
 * a unit test instead of only through a hook render.
 */
export function isExecutionRefreshEvent(type: string): boolean {
  return (
    type.startsWith("batch.") ||
    type.startsWith("run.") ||
    type.startsWith("step.") ||
    type === "evidence.created" ||
    type === "case.status_changed" ||
    type === "human.requested" ||
    type.startsWith("gate.") ||
    P7_EVENT_FAMILIES.some((family) => type.startsWith(family)) ||
    P7_CASE_EVENT_TYPES.has(type)
  )
}

/** Events may omit `version_id`; a null id still belongs to the open channel. */
function isEventForVersion(event: VersionEvent, versionId: string): boolean {
  return event.version_id === null || event.version_id === versionId
}

function maxEventSeq(events: VersionEvent[]): number {
  let max = 0
  for (const event of events) {
    if (event.seq > max) max = event.seq
  }
  return max
}

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === "AbortError"
}

/** Empty detail sections of a list response become arrays, never `undefined`. */
function normalizeCaseRun(raw: CaseRun): CaseRun {
  return {
    ...raw,
    failure_class: raw.failure_class ?? null,
    failure_reason: raw.failure_reason ?? "",
    summary: raw.summary ?? "",
    duration_ms: raw.duration_ms ?? 0,
    title: raw.title ?? "",
    session: raw.session ?? null,
    checkpoint: raw.checkpoint ?? {
      completed_seqs: [],
      cleanup_required: false,
      interrupted_step_seq: null,
    },
    steps: raw.steps ?? [],
    assertions: raw.assertions ?? [],
    evidence: raw.evidence ?? [],
    pending_takeover: raw.pending_takeover ?? null,
  }
}

/** Builds a clean query object so absent filters never become `undefined` keys. */
function batchQuery(status?: BatchStatus, limit?: number, cursor?: string): BatchQuery {
  const query: BatchQuery = {}
  if (status !== undefined) query.status = status
  if (limit !== undefined) query.limit = limit
  if (cursor !== undefined) query.cursor = cursor
  return query
}

/** Same normalization for the case-run list filters. */
function caseRunQuery(status?: CaseRunStatus, limit?: number, cursor?: string): CaseRunQuery {
  const query: CaseRunQuery = {}
  if (status !== undefined) query.status = status
  if (limit !== undefined) query.limit = limit
  if (cursor !== undefined) query.cursor = cursor
  return query
}

/**
 * One stream per hook tree. A caller that already owns the version stream (for
 * example `useExecution`) passes it down; the hook still calls
 * `useVersionStream("")` to keep hook order stable, and an empty id never opens
 * a connection.
 */
function useExecutionStream(versionId: string, provided?: VersionStreamState): VersionStreamState {
  const own = useVersionStream(provided === undefined ? versionId : "")
  return provided ?? own
}

type Fetcher<T> = (signal: AbortSignal) => Promise<T>

type ExecutionResource<T> = {
  data: T | null
  loading: boolean
  error: string
  streamStatus: SSEStatus
  refresh: () => Promise<void>
}

/**
 * Shared abort-safe read: one request at a time, the latest request wins, and
 * an abort/unmount can never write into the rendered state. Live execution
 * events trigger a throttled refetch instead of an optimistic merge.
 */
function useExecutionResource<T>(
  versionId: string,
  key: string,
  enabled: boolean,
  fetcher: Fetcher<T>,
  providedStream?: VersionStreamState,
): ExecutionResource<T> {
  const { events, status } = useExecutionStream(versionId, providedStream)
  const [data, setData] = useState<T | null>(null)
  const [loading, setLoading] = useState(enabled)
  const [error, setError] = useState("")

  const fetcherRef = useRef(fetcher)
  const abortRef = useRef<AbortController | null>(null)
  const requestRef = useRef(0)
  const lastRefreshAtRef = useRef(0)
  const maxSeqRef = useRef(0)
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Keep the latest fetcher without making every render recreate `load`.
  useEffect(() => {
    fetcherRef.current = fetcher
  }, [fetcher])

  const load = useCallback(
    async (initial: boolean): Promise<void> => {
      const request = requestRef.current + 1
      requestRef.current = request
      abortRef.current?.abort()
      abortRef.current = null

      if (!enabled) {
        setData(null)
        setLoading(false)
        setError("")
        return
      }

      const controller = new AbortController()
      abortRef.current = controller
      if (initial) setLoading(true)

      try {
        const next = await fetcherRef.current(controller.signal)
        if (controller.signal.aborted || requestRef.current !== request) return
        setData(next)
        setError("")
      } catch (caught) {
        if (controller.signal.aborted || requestRef.current !== request) return
        if (isAbortError(caught)) return
        setError(describeExecutionError(caught))
      } finally {
        if (requestRef.current === request) {
          if (abortRef.current === controller) abortRef.current = null
          setLoading(false)
        }
      }
    },
    [enabled],
  )

  // Initial load and full reset when the version/batch/case-run key changes.
  // `key` is a reload trigger for parameter changes, not a fetch input.
  // biome-ignore lint/correctness/useExhaustiveDependencies: key restarts the read
  useEffect(() => {
    requestRef.current += 1
    abortRef.current?.abort()
    abortRef.current = null
    maxSeqRef.current = 0
    lastRefreshAtRef.current = 0
    if (timerRef.current !== null) {
      clearTimeout(timerRef.current)
      timerRef.current = null
    }
    setError("")

    if (!enabled) {
      setData(null)
      setLoading(false)
      return
    }

    setData(null)
    setLoading(true)
    void load(true)

    return () => {
      abortRef.current?.abort()
      abortRef.current = null
    }
    // `load` only changes with `enabled`; `key` expresses the parameter change.
  }, [enabled, key, load])

  const scheduleRefresh = useCallback((): void => {
    if (!enabled) return
    if (timerRef.current !== null) return
    const wait = EXECUTION_REFRESH_THROTTLE_MS - (Date.now() - lastRefreshAtRef.current)
    if (wait <= 0) {
      lastRefreshAtRef.current = Date.now()
      void load(false)
      return
    }
    timerRef.current = setTimeout(() => {
      timerRef.current = null
      lastRefreshAtRef.current = Date.now()
      void load(false)
    }, wait)
  }, [enabled, load])

  // A new execution event for this channel means the REST projection is stale.
  useEffect(() => {
    if (!enabled || events.length === 0) return
    const scoped = events.filter((event) => isEventForVersion(event, versionId))
    if (scoped.length === 0) return

    const previousMax = maxSeqRef.current
    const nextMax = maxEventSeq(scoped)
    if (nextMax <= previousMax) return
    maxSeqRef.current = nextMax
    if (!scoped.some((event) => event.seq > previousMax && isExecutionRefreshEvent(event.type))) {
      return
    }
    scheduleRefresh()
  }, [events, enabled, versionId, scheduleRefresh])

  // A reconnect may have missed events; reconcile with one full read.
  const previousStatusRef = useRef<SSEStatus>(status)
  useEffect(() => {
    const previous = previousStatusRef.current
    previousStatusRef.current = status
    if (!enabled || status !== "live") return
    if (previous !== "reconnecting" && previous !== "closed") return
    lastRefreshAtRef.current = Date.now()
    void load(false)
  }, [status, enabled, load])

  useEffect(() => {
    return () => {
      if (timerRef.current !== null) clearTimeout(timerRef.current)
    }
  }, [])

  const refresh = useCallback(async (): Promise<void> => {
    await load(false)
  }, [load])

  return { data, loading, error, streamStatus: status, refresh }
}

export type UseBatchesResult = {
  batches: Batch[]
  nextCursor: string
  hasMore: boolean
  loading: boolean
  error: string
  streamStatus: SSEStatus
  refresh: () => Promise<void>
}

/**
 * One cursor page of the version's batches. Pass `status: "running"` with
 * `limit: 1` to resolve the active batch (spec §6.3: versions do not embed it).
 */
export function useBatches(
  versionId: string,
  query: BatchQuery = {},
  stream?: VersionStreamState,
): UseBatchesResult {
  const status = query.status
  const limit = query.limit
  const cursor = query.cursor
  const key = `${versionId}|batches|${status ?? ""}|${limit ?? ""}|${cursor ?? ""}`
  const fetcher = useCallback(
    (signal: AbortSignal) => listBatches(versionId, batchQuery(status, limit, cursor), { signal }),
    [versionId, status, limit, cursor],
  )
  const resource = useExecutionResource(versionId, key, versionId !== "", fetcher, stream)

  return {
    batches: resource.data?.items ?? [],
    nextCursor: resource.data?.nextCursor ?? "",
    hasMore: resource.data?.hasMore ?? false,
    loading: resource.loading,
    error: resource.error,
    streamStatus: resource.streamStatus,
    refresh: resource.refresh,
  }
}

export type UseBatchResult = {
  batch: Batch | null
  loading: boolean
  error: string
  streamStatus: SSEStatus
  refresh: () => Promise<void>
}

/** One batch detail, kept fresh by batch-level events. */
export function useBatch(
  versionId: string,
  batchId: string,
  stream?: VersionStreamState,
): UseBatchResult {
  const key = `${versionId}|batch|${batchId}`
  const fetcher = useCallback(
    (requestOptions: AbortSignal) => getBatch(versionId, batchId, { signal: requestOptions }),
    [versionId, batchId],
  )
  const resource = useExecutionResource(
    versionId,
    key,
    versionId !== "" && batchId !== "",
    fetcher,
    stream,
  )

  return {
    batch: resource.data,
    loading: resource.loading,
    error: resource.error,
    streamStatus: resource.streamStatus,
    refresh: resource.refresh,
  }
}

export type UseCaseRunsResult = {
  caseRuns: CaseRun[]
  nextCursor: string
  hasMore: boolean
  loading: boolean
  error: string
  streamStatus: SSEStatus
  refresh: () => Promise<void>
}

/** One cursor page of a batch's case runs, with normalized detail sections. */
export function useCaseRuns(
  versionId: string,
  batchId: string,
  query: CaseRunQuery = {},
  stream?: VersionStreamState,
): UseCaseRunsResult {
  const status = query.status
  const limit = query.limit
  const cursor = query.cursor
  const key = `${versionId}|case-runs|${batchId}|${status ?? ""}|${limit ?? ""}|${cursor ?? ""}`
  const fetcher = useCallback(
    async (signal: AbortSignal) => {
      const page = await listCaseRuns(versionId, batchId, caseRunQuery(status, limit, cursor), {
        signal,
      })
      return { ...page, items: page.items.map(normalizeCaseRun) }
    },
    [versionId, batchId, status, limit, cursor],
  )
  const resource = useExecutionResource(
    versionId,
    key,
    versionId !== "" && batchId !== "",
    fetcher,
    stream,
  )

  return {
    caseRuns: resource.data?.items ?? [],
    nextCursor: resource.data?.nextCursor ?? "",
    hasMore: resource.data?.hasMore ?? false,
    loading: resource.loading,
    error: resource.error,
    streamStatus: resource.streamStatus,
    refresh: resource.refresh,
  }
}

export type UseCaseRunResult = {
  caseRun: CaseRun | null
  loading: boolean
  error: string
  streamStatus: SSEStatus
  refresh: () => Promise<void>
}

/** One case-run detail: steps, assertions, evidence, checkpoint and takeover. */
export function useCaseRun(
  versionId: string,
  batchId: string,
  caseRunId: string,
  stream?: VersionStreamState,
): UseCaseRunResult {
  const key = `${versionId}|case-run|${batchId}|${caseRunId}`
  const fetcher = useCallback(
    async (signal: AbortSignal) =>
      normalizeCaseRun(await getCaseRun(versionId, batchId, caseRunId, { signal })),
    [versionId, batchId, caseRunId],
  )
  const resource = useExecutionResource(
    versionId,
    key,
    versionId !== "" && batchId !== "" && caseRunId !== "",
    fetcher,
    stream,
  )

  return {
    caseRun: resource.data,
    loading: resource.loading,
    error: resource.error,
    streamStatus: resource.streamStatus,
    refresh: resource.refresh,
  }
}

export type UseReportResult = {
  report: ReportView | null
  loading: boolean
  error: string
  streamStatus: SSEStatus
  refresh: () => Promise<void>
}

/** The Batch 1 report JSON; a `stream.reset`/reconnect triggers a refetch. */
export function useReport(
  versionId: string,
  batchId: string,
  stream?: VersionStreamState,
): UseReportResult {
  const key = `${versionId}|report|${batchId}`
  const fetcher = useCallback(
    (signal: AbortSignal) => getReport(versionId, batchId, { signal }),
    [versionId, batchId],
  )
  const resource = useExecutionResource(
    versionId,
    key,
    versionId !== "" && batchId !== "",
    fetcher,
    stream,
  )

  return {
    report: resource.data,
    loading: resource.loading,
    error: resource.error,
    streamStatus: resource.streamStatus,
    refresh: resource.refresh,
  }
}

export type ExecutionActionKey =
  | "createBatch"
  | "pause"
  | "resume"
  | "cancel"
  | "rerun"
  | "exportReport"

export type UseExecutionActionsOptions = {
  /** Called after every successful write so callers refetch server state. */
  onChanged?: () => void | Promise<void>
}

export type UseExecutionActionsResult = {
  createBatch: (body?: CreateBatchBody) => Promise<Batch | null>
  pause: (batchId: string, mode?: PauseMode) => Promise<boolean>
  resume: (batchId: string) => Promise<boolean>
  cancel: (batchId: string) => Promise<boolean>
  rerun: (batchId: string, caseRunId: string) => Promise<CaseRun | null>
  exportReport: (batchId: string, options?: ExportReportOptions) => Promise<ReportDownload | null>
  /** Action currently in flight; empty when idle. */
  pending: ExecutionActionKey | ""
  error: string
  clearError: () => void
}

/**
 * Batch control writes. Nothing is updated optimistically: a successful call
 * awaits `onChanged` (normally a hook `refresh`) and the server projection wins.
 * Failures are mapped to Chinese copy and exposed on `error`.
 */
export function useExecutionActions(
  versionId: string,
  options: UseExecutionActionsOptions = {},
): UseExecutionActionsResult {
  const [pending, setPending] = useState<ExecutionActionKey | "">("")
  const [error, setError] = useState("")
  const onChangedRef = useRef(options.onChanged)

  useEffect(() => {
    onChangedRef.current = options.onChanged
  }, [options.onChanged])

  const execute = useCallback(
    async <T>(key: ExecutionActionKey, task: () => Promise<T>): Promise<T | null> => {
      setPending(key)
      setError("")
      try {
        const result = await task()
        await onChangedRef.current?.()
        return result
      } catch (caught) {
        setError(describeExecutionError(caught))
        return null
      } finally {
        setPending("")
      }
    },
    [],
  )

  const createBatchAction = useCallback(
    async (body: CreateBatchBody = {}): Promise<Batch | null> =>
      execute("createBatch", () => createBatch(versionId, body)),
    [execute, versionId],
  )

  const pause = useCallback(
    async (batchId: string, mode: PauseMode = "graceful"): Promise<boolean> => {
      const ok = await execute("pause", async () => {
        await pauseBatch(versionId, batchId, mode)
        return true
      })
      return ok === true
    },
    [execute, versionId],
  )

  const resume = useCallback(
    async (batchId: string): Promise<boolean> => {
      const ok = await execute("resume", async () => {
        await resumeBatch(versionId, batchId)
        return true
      })
      return ok === true
    },
    [execute, versionId],
  )

  const cancel = useCallback(
    async (batchId: string): Promise<boolean> => {
      const ok = await execute("cancel", async () => {
        await cancelBatch(versionId, batchId)
        return true
      })
      return ok === true
    },
    [execute, versionId],
  )

  const rerun = useCallback(
    async (batchId: string, caseRunId: string): Promise<CaseRun | null> =>
      execute("rerun", () => rerunCase(versionId, batchId, caseRunId)),
    [execute, versionId],
  )

  const exportReportAction = useCallback(
    async (
      batchId: string,
      exportOptions: ExportReportOptions = {},
    ): Promise<ReportDownload | null> =>
      execute("exportReport", () => exportReport(versionId, batchId, exportOptions)),
    [execute, versionId],
  )

  const clearError = useCallback((): void => setError(""), [])

  return {
    createBatch: createBatchAction,
    pause,
    resume,
    cancel,
    rerun,
    exportReport: exportReportAction,
    pending,
    error,
    clearError,
  }
}

export type UseExecutionResult = {
  batches: Batch[]
  activeBatch: Batch | null
  loading: boolean
  error: string
  streamStatus: SSEStatus
  refresh: () => Promise<void>
  actions: UseExecutionActionsResult
}

/**
 * Header-level composition (spec §9.1): one version stream, the batch page,
 * the active batch (planned/running) and the control actions. Tabs that need a
 * detail projection use `useCaseRuns`/`useCaseRun`/`useReport` directly.
 */
export function useExecution(versionId: string): UseExecutionResult {
  const stream = useVersionStream(versionId)
  const batches = useBatches(versionId, {}, stream)
  const activeBatch =
    batches.batches.find((batch) => ACTIVE_BATCH_STATUSES.includes(batch.status)) ?? null
  const actions = useExecutionActions(versionId, { onChanged: batches.refresh })

  return {
    batches: batches.batches,
    activeBatch,
    loading: batches.loading,
    error: batches.error,
    streamStatus: stream.status,
    refresh: batches.refresh,
    actions,
  }
}
