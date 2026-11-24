"use client"

import { useCallback, useEffect, useRef, useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Dialog } from "@/components/ui/Dialog"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { getApiBaseUrl } from "@/lib/api-client"
import { getToken } from "@/lib/auth-storage"
import { formatDateTime } from "@/lib/format"
import { connectSSE, type SSEStatus } from "@/lib/sse-client"

import { getRunTranscript } from "./api"
import {
  type AgentRunView,
  formatUsage,
  TRANSCRIPT_ROLE_LABEL,
  TRANSCRIPT_STREAM_STATE_LABEL,
  type TranscriptDelta,
  type TranscriptMessage,
  type TranscriptStreamState,
} from "./types"

export type RunTranscriptDrawerProps = {
  run: AgentRunView
  onClose: () => void
}

/* ----------------------------- delta accumulation ---------------------------- */

const STREAM_STATES: ReadonlySet<string> = new Set<TranscriptStreamState>([
  "streaming",
  "complete",
  "interrupted",
])

/**
 * Cap on the pre-snapshot delta buffer. The drawer buffers `agent.transcript.delta`
 * frames that arrive before the initial HTTP snapshot resolves; contiguous
 * same-(message_id, seq) appends coalesce to one entry, so a healthy burst stays
 * tiny. This bound guards a pathological flood, and overflowing it is surfaced
 * (a lag notice with retry), never silent (constitution 七.6).
 */
export const TRANSCRIPT_PENDING_LIMIT = 512

/**
 * How many times a buffered non-terminal ASSISTANT head may force an
 * authoritative re-read before the drawer stops re-reading and falls back to
 * applying the snapshot plus appends/terminal frames (H-E22 livelock bound). Two
 * short turns — or frequent non-prefix rewrites — can otherwise keep a
 * non-terminal full row arriving faster than the HTTP round-trip, driving an
 * unbounded fetch→re-read loop that never loads `rows`.
 */
export const MAX_STALE_REREADS = 2

/**
 * Debounce before a forward-offset-gap snapshot re-read. It must be at least one
 * bounded-flush period (the server persists coalesced transcript text every
 * 250ms), so the re-read observes the text the live increment's offset refers
 * to instead of the same lagging row.
 */
export const GAP_REREAD_DELAY_MS = 300

const TRANSCRIPT_ROLES: ReadonlySet<string> = new Set(["system", "user", "assistant", "tool"])

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

function compareTranscriptIds(left: string, right: string): number {
  if (left === right) return 0
  try {
    const leftId = BigInt(left)
    const rightId = BigInt(right)
    return leftId < rightId ? -1 : 1
  } catch {
    return left.localeCompare(right)
  }
}

function sortTranscript(rows: TranscriptMessage[]): TranscriptMessage[] {
  return [...rows].sort((left, right) => compareTranscriptIds(left.id, right.id))
}

function isFiniteNumber(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value)
}

/**
 * Validates the ONE full transcript row the drawer actually renders (H-E21).
 * `TranscriptRow` reads `parts.map`, `tool_calls.map`, `created_at.length`,
 * `formatUsage(usage)` and indexes `TRANSCRIPT_ROLE_LABEL[role]`; a row missing
 * any of those collections crashed the whole drawer. This checks exactly that
 * render surface (arrays, element shapes, string/number fields, an enum role and
 * stream state, and `message.id === message_id`) and returns null on any breach,
 * so a malformed frame changes nothing instead of throwing.
 */
function normalizeSnapshotRow(value: unknown, messageId: string): TranscriptMessage | null {
  if (!isRecord(value)) return null
  if (typeof value.id !== "string" || value.id !== messageId) return null
  if (typeof value.role !== "string" || !TRANSCRIPT_ROLES.has(value.role)) return null
  if (typeof value.created_at !== "string") return null
  if (typeof value.stream_state !== "string" || !STREAM_STATES.has(value.stream_state)) {
    return null
  }
  if (!Array.isArray(value.parts)) return null
  for (const part of value.parts) {
    if (!isRecord(part) || typeof part.type !== "string") return null
    if (part.text !== undefined && typeof part.text !== "string") return null
  }
  if (!Array.isArray(value.tool_calls)) return null
  for (const call of value.tool_calls) {
    if (
      !isRecord(call) ||
      typeof call.id !== "string" ||
      typeof call.name !== "string" ||
      typeof call.arguments !== "string"
    ) {
      return null
    }
  }
  if (!isRecord(value.usage)) return null
  for (const amount of Object.values(value.usage)) {
    if (!isFiniteNumber(amount)) return null
  }
  return value as unknown as TranscriptMessage
}

/**
 * Validates one raw `agent.transcript.delta` payload against the server's
 * authoritative schema (agentapp.TranscriptDelta). The two modes are mutually
 * exclusive: a full `message` object is snapshot mode; otherwise a non-empty
 * `increment` with a positive integer `seq`, a `part_type` and a non-negative
 * integer `offset` (0 omitted) is append mode. Anything else changes nothing.
 */
export function normalizeTranscriptDelta(payload: unknown): TranscriptDelta | null {
  if (!isRecord(payload)) return null
  const runId = payload.run_id
  const messageId = payload.message_id
  const streamState = payload.stream_state
  if (typeof runId !== "string" || !/^[0-9]+$/.test(runId)) return null
  if (typeof messageId !== "string" || !/^[0-9]+$/.test(messageId)) return null
  if (typeof streamState !== "string" || !STREAM_STATES.has(streamState)) return null

  if (isRecord(payload.message)) {
    // Snapshot mode. A malformed full row is rejected wholesale (never treated
    // as append mode, never double-asserted into TranscriptMessage): returning
    // null means the drawer changes nothing rather than crashing on render.
    const message = normalizeSnapshotRow(payload.message, messageId)
    if (message === null) return null
    return {
      run_id: runId,
      message_id: messageId,
      stream_state: streamState as TranscriptStreamState,
      message,
    }
  }
  const seq = payload.seq
  const partType = payload.part_type
  const increment = payload.increment
  const offset = payload.offset ?? 0
  if (
    typeof increment === "string" &&
    increment.length > 0 &&
    typeof seq === "number" &&
    Number.isInteger(seq) &&
    seq > 0 &&
    typeof partType === "string" &&
    partType.length > 0 &&
    typeof offset === "number" &&
    Number.isInteger(offset) &&
    offset >= 0
  ) {
    return {
      run_id: runId,
      message_id: messageId,
      stream_state: streamState as TranscriptStreamState,
      seq,
      part_type: partType,
      offset,
      increment,
    }
  }
  return null
}

function isFrozen(row: TranscriptMessage): boolean {
  return row.stream_state === "complete" || row.stream_state === "interrupted"
}

/**
 * True when an APPEND delta targets a part whose locally accumulated text is
 * SHORTER than the delta's offset — the server's published text is ahead of what
 * this client holds. That happens when the drawer opens mid-turn: the HTTP
 * snapshot lags the published text (bounded DB flush), so the next increments
 * carry offsets past the local length and the strict-equality append drops them
 * until the turn seals. Only a STREAMING assistant head is eligible; a replay
 * (offset <= local length), a terminal/frozen row, a snapshot-mode frame and a
 * non-creatable seq jump are not.
 */
export function hasForwardOffsetGap(
  rows: readonly TranscriptMessage[],
  delta: TranscriptDelta,
): boolean {
  if (delta.message !== undefined) return false
  if (delta.stream_state !== "streaming") return false
  if (typeof delta.increment !== "string" || delta.increment.length === 0) return false
  const seq = delta.seq
  if (seq === undefined) return false
  const row = rows.find((candidate) => candidate.id === delta.message_id)
  if (row !== undefined && (row.role !== "assistant" || isFrozen(row))) return false
  const parts = row?.parts ?? []
  const partIndex = seq - 1
  const part = parts[partIndex]
  let localLength = 0
  if (part === undefined) {
    // Only the next slot is creatable; a jump past it is not an offset gap.
    if (partIndex !== parts.length) return false
  } else {
    if (part.type !== delta.part_type) return false
    if (part.text !== undefined && typeof part.text !== "string") return false
    localLength = typeof part.text === "string" ? part.text.length : 0
  }
  return (delta.offset ?? 0) > localLength
}

/**
 * Applies one validated delta to the accumulated transcript rows:
 * - snapshot mode (`message`): the full row replaces that message_id wholesale
 *   (parts/tool_calls/usage/stream_state); unseen rows insert in id order.
 * - append mode (`increment`): locate the part by (message_id, seq). A part at
 *   the next slot that does not exist yet is created as empty text BEFORE the
 *   offset check — the streaming placeholder has parts:[] while the first
 *   append targets seq 1. cur.length === offset appends; any other offset is a
 *   replayed/out-of-order frame and is discarded (idempotent, the next full
 *   snapshot heals). An append against a different-type part is likewise a gap
 *   (the server sends snapshot mode on type changes).
 * - a frozen row (complete/interrupted) accepts no further frames.
 */
export function applyTranscriptDelta(
  rows: TranscriptMessage[],
  delta: TranscriptDelta,
): TranscriptMessage[] {
  const index = rows.findIndex((row) => row.id === delta.message_id)
  const existing = index >= 0 ? rows[index] : undefined
  if (existing !== undefined && isFrozen(existing)) return rows

  if (delta.message !== undefined) {
    // A full row whose id disagrees with message_id is never silently re-keyed:
    // addressing a row by message_id while rendering another id would misroute
    // parts/tool_calls. Refuse it (the next authoritative frame heals).
    if (delta.message.id !== delta.message_id) return rows
    const next: TranscriptMessage = { ...delta.message, id: delta.message_id }
    if (existing === undefined) return sortTranscript([...rows, next])
    const copy = [...rows]
    copy[index] = next
    return copy
  }

  const seq = delta.seq as number
  const partType = delta.part_type as string
  const increment = delta.increment as string
  const offset = delta.offset ?? 0
  const base: TranscriptMessage = existing ?? {
    id: delta.message_id,
    run_id: delta.run_id,
    role: "assistant",
    parts: [],
    tool_calls: [],
    usage: {},
    stream_state: delta.stream_state,
    created_at: "",
  }
  const partIndex = seq - 1
  const parts = [...base.parts]
  let current = parts[partIndex]
  if (current === undefined) {
    // Only the next slot may be created; a jump past it is a gap we cannot
    // fabricate intermediate parts for.
    if (partIndex !== parts.length) return rows
    current = { type: partType, text: "" }
    parts[partIndex] = current
  }
  const currentText = current.text ?? ""
  if (current.type !== partType || typeof currentText !== "string") return rows
  if (currentText.length !== offset) return rows
  parts[partIndex] = { ...current, text: currentText + increment }
  const next: TranscriptMessage = { ...base, parts, stream_state: delta.stream_state }
  if (existing === undefined) return sortTranscript([...rows, next])
  const copy = [...rows]
  copy[index] = next
  return copy
}

function isTerminalStreamState(state: TranscriptStreamState): boolean {
  return state === "complete" || state === "interrupted"
}

/**
 * A buffered full row that is a non-terminal ASSISTANT streaming head — the one
 * shape that can be OLDER than a fresh snapshot of the same head and so must not
 * be replayed in arrival order (H-E22). Restricting the stale test to the
 * assistant streaming head avoids a needless re-read for unrelated full rows
 * (system/tool rows are always terminal and apply directly).
 */
function isStaleStreamingHead(delta: TranscriptDelta): boolean {
  return (
    delta.message !== undefined &&
    delta.message.role === "assistant" &&
    !isTerminalStreamState(delta.message.stream_state)
  )
}

/**
 * Losslessly compacts a delta into the pre-snapshot queue before the bound
 * applies (H-E23, Part E discipline): a full-row snapshot supersedes every
 * earlier queued frame of its message (the row replaces wholesale), and a
 * contiguous same-head, same-seq append (`next.offset === prev.offset +
 * prev.increment.length`, UTF-16 units) coalesces into one entry. Anything else
 * stays in arrival order for the existing offset/gap semantics.
 */
function compactTranscriptDelta(queue: TranscriptDelta[], delta: TranscriptDelta): void {
  if (delta.message !== undefined) {
    for (let index = queue.length - 1; index >= 0; index -= 1) {
      if (queue[index].message_id === delta.message_id) queue.splice(index, 1)
    }
    queue.push(delta)
    return
  }
  const offset = delta.offset ?? 0
  const index = queue.findIndex(
    (entry) =>
      entry.message === undefined &&
      entry.message_id === delta.message_id &&
      entry.seq === delta.seq &&
      entry.part_type === delta.part_type,
  )
  if (index < 0) {
    queue.push(delta)
    return
  }
  const prev = queue[index]
  if (offset === (prev.offset ?? 0) + (prev.increment as string).length) {
    queue[index] = { ...prev, increment: (prev.increment as string) + (delta.increment as string) }
    return
  }
  queue.push(delta)
}

/* --------------------------------- component --------------------------------- */

const SSE_STATUS_LABEL: Record<SSEStatus, string> = {
  connecting: "连接中…",
  live: "实时连接",
  reconnecting: "重连中…",
  closed: "连接已断开",
}

/**
 * Admin-only live transcript of one agent run: the initial HTTP snapshot plus
 * the run:{id} SSE deltas. This drawer is the SINGLE owner of its run channel
 * connection (one connectSSE per open drawer, disconnected on unmount); a
 * stream.reset refetches the snapshot over HTTP instead of reconnecting.
 */
export function RunTranscriptDrawer({ run, onClose }: RunTranscriptDrawerProps) {
  const [rows, setRows] = useState<TranscriptMessage[] | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [status, setStatus] = useState<SSEStatus>("connecting")
  const [reloadToken, setReloadToken] = useState(0)
  // True when the pre-snapshot buffer overflowed: rendered as a loud notice with
  // a retry instead of silently dropping frames (constitution 七.6).
  const [lagged, setLagged] = useState(false)
  // Deltas that arrive before the snapshot resolves are buffered (losslessly
  // compacted, then bounded) and replayed onto it, so a frame racing the initial
  // fetch is neither lost nor double-applied (offset dedup / full-row replace).
  const pendingRef = useRef<TranscriptDelta[]>([])
  const snapshotLoadedRef = useRef(false)
  const laggedRef = useRef(false)
  // Counts authoritative re-reads triggered by buffered stale heads within one
  // load generation. It must SURVIVE the internal re-read reload (which bumps
  // reloadToken), so it is only reset on an external (re)load — see the
  // rereadInFlightRef handshake below, which distinguishes the two.
  const rereadCountRef = useRef(0)
  const rereadInFlightRef = useRef(false)
  // Mirror of the committed rows, so the SSE handler can detect a forward offset
  // gap synchronously (React state is not readable from an event callback).
  const rowsRef = useRef<TranscriptMessage[] | null>(null)
  // Pending debounce for a forward-gap snapshot re-read (null when none queued).
  const gapRereadTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  /**
   * Schedules ONE debounced authoritative snapshot re-read for a forward offset
   * gap. It reuses the stale-head re-read budget (MAX_STALE_REREADS) and the
   * rereadInFlightRef handshake, so a burst of gap frames collapses to a single
   * fetch and the per-load budget can never be exceeded (no refetch storm). The
   * delay is at least one bounded-flush period, so the DB has caught up. An
   * external reload (retry / stream.reset) or a run switch resets the budget and
   * cancels the timer in the load effect.
   */
  const scheduleForwardGapReread = useCallback(() => {
    if (gapRereadTimerRef.current !== null) return
    if (rereadInFlightRef.current) return
    if (rereadCountRef.current >= MAX_STALE_REREADS) return
    gapRereadTimerRef.current = setTimeout(() => {
      gapRereadTimerRef.current = null
      if (rereadCountRef.current >= MAX_STALE_REREADS) return
      rereadCountRef.current += 1
      rereadInFlightRef.current = true
      setReloadToken((token) => token + 1)
    }, GAP_REREAD_DELAY_MS)
  }, [])

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken re-runs the explicit snapshot refetch
  useEffect(() => {
    let cancelled = false
    snapshotLoadedRef.current = false
    pendingRef.current = []
    laggedRef.current = false
    // An internal stale-head re-read keeps its count across the reload; any other
    // (re)load — initial mount, run switch, stream.reset, retry/re-sync — starts a
    // fresh re-read budget.
    if (rereadInFlightRef.current) {
      rereadInFlightRef.current = false
    } else {
      rereadCountRef.current = 0
    }
    setLagged(false)
    setError(null)
    void getRunTranscript(run.id).then(
      (snapshot) => {
        if (cancelled) return
        const pending = pendingRef.current
        // H-E22: a queued STREAMING full row can be OLDER than the snapshot we
        // just read (it was produced before this fetch resolved). The wire
        // carries no field to order two full rows of one message, so we never
        // replay a possibly-stale full row in arrival order; we re-read the
        // authoritative snapshot instead. A TERMINAL full row is the row's last
        // frame and applies directly; appends replay onto whichever snapshot
        // lands.
        //
        // CORRECTNESS ASSUMPTION (ops-visible): this re-read relies on the
        // primary database serving read-after-write — the snapshot fetched AFTER
        // a frame was buffered is at least as new as that frame, so dropping the
        // frame is safe. It does NOT hold under multi-replica or eventually
        // consistent reads; if that topology is introduced, extend the wire
        // payload with an orderable field (none exists today) instead of relying
        // on this re-read.
        const staleStreaming = pending.some(isStaleStreamingHead)
        if (staleStreaming && rereadCountRef.current < MAX_STALE_REREADS) {
          rereadCountRef.current += 1
          rereadInFlightRef.current = true
          pendingRef.current = []
          setReloadToken((token) => token + 1)
          return
        }
        let next = sortTranscript(snapshot)
        for (const delta of pending) {
          // Re-read cap exhausted: a still-arriving non-terminal streaming head
          // cannot be ordered against this snapshot, so drop it (the fresh
          // snapshot at request time supersedes it) instead of livelocking —
          // but keep appends and terminal frames so the UI keeps updating.
          if (staleStreaming && isStaleStreamingHead(delta)) continue
          next = applyTranscriptDelta(next, delta)
        }
        pendingRef.current = []
        snapshotLoadedRef.current = true
        rowsRef.current = next
        setRows(next)
      },
      (cause: unknown) => {
        if (cancelled) return
        // The error-window buffer is only kept BOUNDED (see the onEvent bound);
        // the retry rebuilds rows from a fresh snapshot and never replays it.
        // rows stay null so the ErrorState (with retry) is shown.
        setRows(null)
        rowsRef.current = null
        setError(cause)
      },
    )
    return () => {
      cancelled = true
      // An external reload (retry / stream.reset) or a run switch cancels any
      // queued forward-gap re-read.
      if (gapRereadTimerRef.current !== null) {
        clearTimeout(gapRereadTimerRef.current)
        gapRereadTimerRef.current = null
      }
    }
  }, [run.id, reloadToken])

  useEffect(() => {
    const channel = `run:${run.id}`
    const disconnect = connectSSE({
      url: `${getApiBaseUrl()}/api/stream?channel=${channel}`,
      token: getToken() ?? "",
      onEvent: (frame) => {
        if (frame.event !== "agent.transcript.delta") return
        let parsed: unknown
        try {
          parsed = JSON.parse(frame.data)
        } catch {
          return
        }
        if (!isRecord(parsed) || parsed.channel !== channel) return
        const delta = normalizeTranscriptDelta(parsed.payload)
        if (delta === null || delta.run_id !== run.id) return
        if (!snapshotLoadedRef.current) {
          compactTranscriptDelta(pendingRef.current, delta)
          if (pendingRef.current.length > TRANSCRIPT_PENDING_LIMIT) {
            pendingRef.current.length = TRANSCRIPT_PENDING_LIMIT
            if (!laggedRef.current) {
              laggedRef.current = true
              setLagged(true)
            }
          }
          return
        }
        const current = rowsRef.current ?? []
        if (hasForwardOffsetGap(current, delta)) scheduleForwardGapReread()
        const next = applyTranscriptDelta(current, delta)
        rowsRef.current = next
        setRows(next)
      },
      onStatus: setStatus,
      onReset: () => setReloadToken((token) => token + 1),
    })
    return disconnect
  }, [run.id, scheduleForwardGapReread])

  const runLive = run.status === "running"

  return (
    <Dialog
      open
      onClose={onClose}
      title={`Run #${run.id} 转录`}
      description={`${run.profile_key} · 实时转录仅管理员可见；终态消息已冻结。`}
      className="max-w-3xl"
    >
      <div data-testid="agent-center-run-transcript" className="space-y-3">
        <p
          data-testid="agent-center-transcript-status"
          className="text-[11px] text-muted-foreground"
        >
          {SSE_STATUS_LABEL[status]}
        </p>
        {error !== null ? (
          <ErrorState
            error={error}
            title="转录加载失败"
            onRetry={() => setReloadToken((token) => token + 1)}
          />
        ) : rows === null ? (
          <LoadingCard label="正在加载转录…" />
        ) : rows.length === 0 ? (
          <EmptyState title="暂无转录" description="该 run 尚未产生消息；开始后会自动出现。" />
        ) : (
          <div className="max-h-[60vh] space-y-3 overflow-y-auto pr-1">
            {lagged ? (
              <div
                data-testid="agent-center-transcript-lag"
                className="rounded-lg border border-border bg-muted/40 px-3 py-2 text-xs text-foreground"
              >
                实时增量过多，部分较新的帧未进入缓冲；终态消息或重新同步会补全。
                <button
                  type="button"
                  className="ml-2 underline"
                  onClick={() => setReloadToken((token) => token + 1)}
                >
                  重新同步
                </button>
              </div>
            ) : null}
            {rows.map((message) => (
              <TranscriptRow key={message.id} message={message} runLive={runLive} />
            ))}
          </div>
        )}
      </div>
    </Dialog>
  )
}

function TranscriptRow({ message, runLive }: { message: TranscriptMessage; runLive: boolean }) {
  // The part seq is its 1-based array index (the delta contract's addressing).
  const sequencedParts = message.parts.map((part, index) => ({ seq: index + 1, part }))
  const usageText = formatUsage(message.usage)
  return (
    <article
      data-testid={`agent-center-transcript-message-${message.id}`}
      className="rounded-lg border border-border bg-card px-3 py-2"
    >
      <header className="flex flex-wrap items-center gap-2 text-[11px] text-muted-foreground">
        <span className="font-medium text-foreground">
          {TRANSCRIPT_ROLE_LABEL[message.role] ?? message.role}
        </span>
        <TranscriptStateIndicator message={message} runLive={runLive} />
        {message.created_at.length > 0 ? <span>{formatDateTime(message.created_at)}</span> : null}
      </header>
      {sequencedParts.map(({ seq, part }) => (
        <p key={seq} className="mt-1 whitespace-pre-wrap break-words text-xs text-foreground">
          {part.type !== "text" ? (
            <span className="mr-1 text-[10px] text-muted-foreground">[{part.type}]</span>
          ) : null}
          {part.text ?? ""}
        </p>
      ))}
      {message.tool_calls.map((call) => (
        <div
          key={call.id}
          className="mt-1 rounded border border-border bg-muted/40 px-2 py-1 font-mono text-[11px] text-muted-foreground"
        >
          <span className="font-medium text-foreground">{call.name}</span>
          <span className="ml-1 break-all">{call.arguments}</span>
        </div>
      ))}
      {usageText !== "—" ? (
        <p className="mt-1 text-[10px] text-muted-foreground">{usageText}</p>
      ) : null}
    </article>
  )
}

function TranscriptStateIndicator({
  message,
  runLive,
}: {
  message: TranscriptMessage
  runLive: boolean
}) {
  if (message.stream_state === "complete") {
    return <Badge tone="success">{TRANSCRIPT_STREAM_STATE_LABEL.complete}</Badge>
  }
  if (message.stream_state === "interrupted") {
    return <Badge tone="warning">{TRANSCRIPT_STREAM_STATE_LABEL.interrupted}</Badge>
  }
  if (!runLive) {
    // The run is terminal but this row never sealed (e.g. a process crash left
    // it streaming). Show a stale indication instead of an endless spinner.
    return (
      <span data-testid={`agent-center-transcript-stale-${message.id}`}>
        <Badge tone="warning">已停止更新（未封口）</Badge>
      </span>
    )
  }
  return (
    <span
      data-testid={`agent-center-transcript-streaming-${message.id}`}
      className="inline-flex items-center gap-1 text-[11px] text-muted-foreground"
    >
      <span aria-hidden="true" className="size-1.5 animate-pulse rounded-full bg-current" />
      {TRANSCRIPT_STREAM_STATE_LABEL.streaming}…
    </span>
  )
}
