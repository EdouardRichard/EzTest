"use client"

import {
  AlertTriangle,
  BrainCircuit,
  ChevronRight,
  Loader2,
  RefreshCw,
  Send,
  Wrench,
} from "lucide-react"
import { type FormEvent, useCallback, useEffect, useId, useMemo, useRef, useState } from "react"

import { ApiError } from "@/lib/api-client"
import type { SSEStatus } from "@/lib/sse-client"

import {
  analyzeClarification,
  listClarificationMessages,
  reconClarification,
  sendClarificationMessage,
  startClarificationSession,
} from "./api"
import type {
  ClarificationMessage,
  ClarificationSession,
  ClarificationToolCall,
  VersionEvent,
} from "./types"

/** Bounds from spec §7.5. */
const MESSAGE_MAX = 5000
/** The most recent persisted messages the snapshot restores. */
const MESSAGE_LIMIT = 50

const STATUS_LABELS: Record<SSEStatus, string> = {
  connecting: "正在连接…",
  live: "实时连接",
  reconnecting: "重连中…",
  closed: "连接已断开",
}

const TOOL_STATUS_LABELS: Record<string, string> = {
  ok: "成功",
  error: "失败",
  timeout: "超时",
  cancelled: "已取消",
  denied: "被拒绝",
  running: "执行中",
}

type FailedAction =
  | { kind: "send"; message: string }
  | { kind: "start" }
  | { kind: "analyze" }
  | { kind: "recon" }

type OptimisticMessage = {
  key: string
  text: string
  at: string
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

/** Reads `detail.kind`, the frozen V1 activity discriminator. */
function activityKind(event: VersionEvent): string {
  const kind = event.detail.kind
  return typeof kind === "string" ? kind : ""
}

function readDetailText(event: VersionEvent, key: string): string {
  const value = event.detail[key]
  return typeof value === "string" ? value : ""
}

function readDetailNumber(event: VersionEvent, key: string): number | null {
  const value = event.detail[key]
  return typeof value === "number" && Number.isFinite(value) ? value : null
}

/** Reads a boolean activity flag such as `args_truncated`. */
function readDetailFlag(event: VersionEvent, key: string): boolean {
  return event.detail[key] === true
}

/** Reads an ID that may arrive as a JSON number or a decimal string. */
function readDetailId(event: VersionEvent, key: string): string | null {
  const value = event.detail[key]
  if (typeof value === "string" && value !== "") return value
  if (typeof value === "number" && Number.isFinite(value)) return String(value)
  return null
}

function messageText(parts: ClarificationMessage["parts"]): string {
  if (!Array.isArray(parts)) return ""
  return parts
    .filter((part) => isRecord(part) && part.type === "text")
    .map((part) => (typeof part.text === "string" ? part.text : ""))
    .join("")
}

/**
 * Whether a persisted message has content the snapshot can render on its own.
 * P1 inserts an empty assistant placeholder before a run streams its answer, so
 * an empty placeholder must not be treated as authoritative: the live chunks
 * below are what make the answer visible.
 */
function hasRenderableContent(message: ClarificationMessage): boolean {
  if (messageText(message.parts) !== "") return true
  if (message.role !== "assistant") return false
  if (message.reasoning !== "") return true
  return Array.isArray(message.toolCalls) && message.toolCalls.length > 0
}

/** Whether an agent run is currently active according to the activity stream. */
function streamBusy(events: VersionEvent[]): boolean {
  let running = false
  for (const event of events) {
    if (event.type !== "agent.activity") continue
    const kind = activityKind(event)
    if (kind === "run_started") running = true
    else if (kind === "run_finished") running = false
  }
  return running
}

/**
 * Whether the error is the spec §7.1 `ai_unavailable` 503. The banner keeps the
 * human paths usable and offers a retry of the exact action that failed.
 */
function isAiUnavailable(error: unknown): boolean {
  if (!(error instanceof ApiError)) return false
  if (error.status === 503) return true
  if (error.code === "unavailable") return true
  return error.details.reason === "ai_unavailable"
}

/** Human labels for the closed clarification error reasons (spec §7.1). */
const CHAT_REASON_LABELS: Record<string, string> = {
  clarification_session_not_active: "澄清会话未处于进行中状态，请刷新后重试。",
  clarification_session_active: "已有进行中的澄清会话，请刷新后重新查看。",
  requirements_required: "开始澄清前请至少录入一条需求。",
  agent_busy: "已有澄清任务在运行，请等待其结束。",
}

function describeChatError(error: unknown): string {
  if (!(error instanceof ApiError)) {
    return error instanceof Error ? error.message : "未知错误"
  }
  if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
  if (error.status === 401) return "登录状态已失效，请重新登录。"
  const reason = typeof error.details.reason === "string" ? error.details.reason : ""
  const label = CHAT_REASON_LABELS[reason]
  if (label !== undefined) return label
  if (reason !== "") return `${error.message}（${reason}）`
  return error.message
}

type TranscriptEntry =
  | { kind: "user"; key: string; text: string; at: string }
  | { kind: "assistant"; key: string; text: string; at: string }
  | { kind: "thinking"; key: string; text: string; at: string }
  | {
      kind: "tool"
      key: string
      callId: string
      name: string
      args: string
      result: string
      /** The visible args/result is the runner's bounded preview, not the full value. */
      argsTruncated: boolean
      resultTruncated: boolean
      status: string
      durationMs: number | null
      at: string
    }
  | {
      kind: "usage"
      key: string
      prompt: number
      completion: number
      reasoning: number
      total: number
      turns: number
      at: string
    }
  | { kind: "notice"; key: string; text: string; tone: "info" | "error"; at: string }

type ToolEntry = Extract<TranscriptEntry, { kind: "tool" }>
type UsageEntry = Extract<TranscriptEntry, { kind: "usage" }>

/** Terminal run states that must be surfaced instead of finishing silently. */
const RUN_STATUS_LABELS: Record<string, string> = {
  failed: "运行出错",
  aborted: "运行已中止",
  interrupted: "运行被中断",
}

/** Describes a non-successful `run_finished`; "" means the run completed. */
function runOutcomeNotice(event: VersionEvent): string {
  const status = readDetailText(event, "status")
  const label = RUN_STATUS_LABELS[status]
  if (label === undefined) return ""
  const reason = readDetailText(event, "terminal_reason")
  const code = readDetailText(event, "abort_code") || readDetailText(event, "error_code")
  if (reason !== "" && code !== "") return `${label}：${reason}（${code}）`
  if (reason !== "" || code !== "") return `${label}：${reason !== "" ? reason : code}`
  return label
}

/**
 * Applies one live tool activity to an existing tool row. The persisted snapshot
 * is authoritative: the runner bounds event previews to 2048 bytes, so a preview
 * only fills a value the snapshot does not have. A preview that does fill a gap
 * keeps its `args_truncated`/`result_truncated` flag so the row is never shown
 * as if the shortened preview were the complete value.
 */
function applyToolActivity(entry: ToolEntry, event: VersionEvent, kind: string): void {
  const name = readDetailText(event, "tool")
  if (name !== "") entry.name = name
  if (kind === "tool_use") {
    if (entry.args !== "") return
    const args = readDetailText(event, "args")
    if (args === "") return
    entry.args = args
    entry.argsTruncated = readDetailFlag(event, "args_truncated")
    return
  }
  if (entry.result === "") {
    const result = readDetailText(event, "result")
    if (result !== "") {
      entry.result = result
      entry.resultTruncated = readDetailFlag(event, "result_truncated")
    }
  }
  const status = readDetailText(event, "status")
  if (status !== "") entry.status = status
  const durationMs = readDetailNumber(event, "duration_ms")
  if (durationMs !== null) entry.durationMs = durationMs
}

/**
 * Finds the live tool row still waiting for a result. Real activity detail
 * always carries `run_id`; this fallback keeps a result folded with its tool use
 * even when a frame omits the run id.
 */
function findPendingLiveTool(
  entries: Map<string, OrderedEntry>,
  callId: string,
  kind: string,
): OrderedEntry | undefined {
  for (const item of entries.values()) {
    if (item.entry.kind !== "tool" || item.entry.callId !== callId) continue
    if (kind === "tool_result" && item.entry.status !== "") continue
    return item
  }
  return undefined
}

type OrderedEntry = { entry: TranscriptEntry; order: number }

/**
 * Key for a persisted tool call. Call ids are only unique per run, so the run id
 * scopes the lookup when the snapshot has one; a bare call id is used only when
 * the snapshot has no run id at all.
 */
function persistedToolKey(runId: string | number | null, callId: string): string {
  return runId === null ? callId : `${runId}|${callId}`
}

function parseTime(value: string): number {
  const time = Date.parse(value)
  return Number.isNaN(time) ? Number.POSITIVE_INFINITY : time
}

/** Stable chronological rendering: timestamp first, arrival order second. */
function sortEntries(ordered: OrderedEntry[]): TranscriptEntry[] {
  return [...ordered]
    .sort((left, right) => {
      const leftTime = parseTime(left.entry.at)
      const rightTime = parseTime(right.entry.at)
      if (leftTime !== rightTime) return leftTime - rightTime
      return left.order - right.order
    })
    .map((item) => item.entry)
}

/**
 * Merges the persisted message snapshot with local optimistic user messages and
 * the live `agent.activity` stream:
 * - a persisted message is authoritative only when it has renderable content, so
 *   an empty P1 assistant placeholder does not hide its live streamed answer;
 * - persisted text/thinking/tool content is rendered once (the API is the
 *   authority after a reload);
 * - live text/thinking chunks of the same `(message_id, part)` are folded;
 * - persisted tool args/results are authoritative; live activity only fills a
 *   missing value and keeps the runner's truncation flags;
 * - tool_use/tool_result pairs fold by `run_id` + `call_id`, falling back to the
 *   persisted call with the same call id so a restored call is never duplicated;
 * - usage activities fold into one summary and a non-successful `run_finished`
 *   is surfaced as a notice;
 * - optimistic user messages are dropped once the snapshot contains them.
 */
function buildTranscript(
  messages: ClarificationMessage[],
  optimistic: OptimisticMessage[],
  events: VersionEvent[],
): TranscriptEntry[] {
  const ordered: OrderedEntry[] = []
  let order = 0
  function add(entry: TranscriptEntry): OrderedEntry {
    const item: OrderedEntry = { entry, order }
    order += 1
    ordered.push(item)
    return item
  }

  const knownMessageIds = new Set<number>()
  const persistedToolCalls: OrderedEntry[] = []
  const persistedToolByKey = new Map<string, OrderedEntry>()
  const persistedUserTexts = new Map<string, number>()

  for (const message of messages) {
    if (hasRenderableContent(message)) knownMessageIds.add(message.id)
    const text = messageText(message.parts)
    if (message.role === "user") {
      if (text !== "") {
        persistedUserTexts.set(text, (persistedUserTexts.get(text) ?? 0) + 1)
        add({ kind: "user", key: `message-${message.id}`, text, at: message.createdAt })
      }
      continue
    }
    if (message.role === "assistant") {
      if (message.reasoning !== "") {
        add({
          kind: "thinking",
          key: `message-${message.id}-thinking`,
          text: message.reasoning,
          at: message.createdAt,
        })
      }
      if (text !== "") {
        add({ kind: "assistant", key: `message-${message.id}`, text, at: message.createdAt })
      }
      const calls: ClarificationToolCall[] = Array.isArray(message.toolCalls)
        ? message.toolCalls
        : []
      for (const call of calls) {
        const entry = add({
          kind: "tool",
          key: `message-${message.id}-tool-${call.id}`,
          callId: call.id,
          name: call.name,
          args: call.arguments,
          result: "",
          argsTruncated: false,
          resultTruncated: false,
          status: "",
          durationMs: null,
          at: message.createdAt,
        })
        persistedToolCalls.push(entry)
        const key = persistedToolKey(message.runId, call.id)
        if (!persistedToolByKey.has(key)) persistedToolByKey.set(key, entry)
      }
      continue
    }
    // Tool-role messages only carry the result; pair them with the first
    // persisted call still waiting for one (call order), otherwise render a
    // standalone row. Tool calls and their results are persisted in order. An
    // empty persisted result still consumes its call so the queue stays aligned.
    const pending = persistedToolCalls.find(
      (item) => item.entry.kind === "tool" && item.entry.status === "",
    )
    if (pending !== undefined && pending.entry.kind === "tool") {
      pending.entry.result = text
      pending.entry.status = "ok"
    } else if (text !== "") {
      add({
        kind: "tool",
        key: `message-${message.id}`,
        callId: `message-${message.id}`,
        name: message.toolName ?? "工具",
        args: "",
        result: text,
        argsTruncated: false,
        resultTruncated: false,
        status: "ok",
        durationMs: null,
        at: message.createdAt,
      })
    }
  }

  for (const local of optimistic) {
    const persisted = persistedUserTexts.get(local.text) ?? 0
    if (persisted > 0) {
      persistedUserTexts.set(local.text, persisted - 1)
      continue
    }
    add({ kind: "user", key: local.key, text: local.text, at: local.at })
  }

  const liveText = new Map<string, OrderedEntry>()
  const liveTools = new Map<string, OrderedEntry>()
  let usage: UsageEntry | null = null

  for (const event of events) {
    if (event.type !== "agent.activity") continue
    const kind = activityKind(event)
    if (kind === "" || kind === "user_message") continue
    const messageId = readDetailNumber(event, "message_id")

    if (kind === "text" || kind === "thinking") {
      // A message with renderable content is restored by the snapshot; only an
      // empty placeholder leaves its live chunks to render here.
      if (messageId !== null && knownMessageIds.has(messageId)) continue
      const part = readDetailNumber(event, "part") ?? 0
      // Chunks of one persisted message part fold together (spec §9.2); the
      // message ID plus part index is stable across a run's activity frames.
      const chunkKey = `${kind}|${messageId ?? "?"}|${part}`
      const text = readDetailText(event, "text")
      const existing = liveText.get(chunkKey)
      if (
        existing !== undefined &&
        (existing.entry.kind === "assistant" || existing.entry.kind === "thinking")
      ) {
        existing.entry.text += text
        continue
      }
      liveText.set(
        chunkKey,
        add(
          kind === "text"
            ? { kind: "assistant", key: `live-${chunkKey}`, text, at: event.ts }
            : { kind: "thinking", key: `live-${chunkKey}`, text, at: event.ts },
        ),
      )
      continue
    }

    if (kind === "tool_use" || kind === "tool_result") {
      const callId = readDetailId(event, "call_id")
      if (callId === null) continue
      // Call ids are only unique per run; the run id keeps two runs apart.
      const runId = readDetailId(event, "run_id")
      const toolKey = `${runId ?? "?"}|${callId}`
      let existing = liveTools.get(toolKey)
      if (existing === undefined) {
        let persisted = persistedToolByKey.get(persistedToolKey(runId, callId))
        if (persisted === undefined) {
          // A bare key only ever holds a call whose snapshot message had no run
          // id, so this fallback cannot attach to a call scoped to another run.
          persisted = persistedToolByKey.get(callId)
        }
        if (persisted !== undefined && persisted.entry.kind === "tool") {
          applyToolActivity(persisted.entry, event, kind)
          liveTools.set(toolKey, persisted)
          continue
        }
        // Only an event without a run id may reuse a row from another run; a
        // missing exact key otherwise means this is a different run.
        existing =
          runId === null
            ? findPendingLiveTool(liveTools, callId, kind)
            : liveTools.get(`?|${callId}`)
      }
      if (existing !== undefined && existing.entry.kind === "tool") {
        applyToolActivity(existing.entry, event, kind)
        liveTools.set(toolKey, existing)
        continue
      }
      liveTools.set(
        toolKey,
        add({
          kind: "tool",
          key: `live-tool-${toolKey}`,
          callId,
          name: readDetailText(event, "tool") || callId,
          args: kind === "tool_use" ? readDetailText(event, "args") : "",
          result: kind === "tool_result" ? readDetailText(event, "result") : "",
          argsTruncated: kind === "tool_use" && readDetailFlag(event, "args_truncated"),
          resultTruncated: kind === "tool_result" && readDetailFlag(event, "result_truncated"),
          status: kind === "tool_result" ? readDetailText(event, "status") : "",
          durationMs: kind === "tool_result" ? readDetailNumber(event, "duration_ms") : null,
          at: event.ts,
        }),
      )
      continue
    }

    if (kind === "usage") {
      if (usage === null) {
        usage = {
          kind: "usage",
          key: "usage",
          prompt: 0,
          completion: 0,
          reasoning: 0,
          total: 0,
          turns: 0,
          at: event.ts,
        }
      }
      usage.turns += 1
      usage.prompt += readDetailNumber(event, "prompt_tokens") ?? 0
      usage.completion += readDetailNumber(event, "completion_tokens") ?? 0
      usage.reasoning += readDetailNumber(event, "reasoning_tokens") ?? 0
      usage.total += readDetailNumber(event, "total_tokens") ?? 0
      continue
    }

    if (kind === "run_finished") {
      const notice = runOutcomeNotice(event)
      if (notice !== "") {
        add({ kind: "notice", key: `run-${event.seq}`, text: notice, tone: "error", at: event.ts })
      }
    }
  }

  if (usage !== null) add(usage)

  return sortEntries(ordered)
}

function ThinkingRow({ entry }: { entry: Extract<TranscriptEntry, { kind: "thinking" }> }) {
  const [expanded, setExpanded] = useState(false)
  return (
    <div className="rounded-xl border border-dashed border-border bg-muted/30">
      <button
        type="button"
        aria-label="思考"
        aria-expanded={expanded}
        onClick={() => setExpanded((previous) => !previous)}
        className="flex w-full items-center gap-2 px-3 py-2 text-left text-xs font-medium text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <BrainCircuit className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
        <span>思考</span>
        <ChevronRight
          className={
            expanded
              ? "ml-auto h-3.5 w-3.5 rotate-90 transition-transform"
              : "ml-auto h-3.5 w-3.5 transition-transform"
          }
          aria-hidden="true"
        />
      </button>
      {expanded ? (
        <p className="whitespace-pre-wrap break-words border-t border-border px-3 py-2 text-sm leading-relaxed text-muted-foreground">
          {entry.text}
        </p>
      ) : null}
    </div>
  )
}

function ToolRow({ entry }: { entry: Extract<TranscriptEntry, { kind: "tool" }> }) {
  const [expanded, setExpanded] = useState(false)
  const status = TOOL_STATUS_LABELS[entry.status] ?? entry.status
  const duration = entry.durationMs === null ? "" : `${entry.durationMs}ms`
  return (
    <div className="overflow-hidden rounded-xl border border-border bg-card/70">
      <button
        type="button"
        aria-label={`工具 ${entry.name}`}
        aria-expanded={expanded}
        onClick={() => setExpanded((previous) => !previous)}
        className="flex w-full items-center gap-2 px-3 py-2 text-left transition-colors hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <Wrench className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span className="max-w-[60%] truncate font-mono text-xs font-medium">
          工具 {entry.name}
        </span>
        {status === "" ? null : (
          <span className="rounded-full border border-border px-1.5 py-0.5 text-[11px] text-muted-foreground">
            {status}
          </span>
        )}
        {duration === "" ? null : (
          <span className="text-[11px] text-muted-foreground">{duration}</span>
        )}
        <ChevronRight
          className={
            expanded
              ? "ml-auto h-3.5 w-3.5 shrink-0 rotate-90 transition-transform"
              : "ml-auto h-3.5 w-3.5 shrink-0 transition-transform"
          }
          aria-hidden="true"
        />
      </button>
      {expanded ? (
        <div className="space-y-2 border-t border-border px-3 py-2">
          <div>
            <p className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
              参数
            </p>
            <pre className="mt-1 overflow-x-auto whitespace-pre-wrap break-all rounded-lg bg-muted/50 p-2 font-mono text-xs">
              {entry.args || "（无）"}
            </pre>
            {entry.argsTruncated ? (
              <p className="mt-1 text-[11px] text-amber-700 dark:text-amber-300">
                参数已截断，仅显示预览。
              </p>
            ) : null}
          </div>
          <div>
            <p className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
              结果
            </p>
            <pre className="mt-1 overflow-x-auto whitespace-pre-wrap break-all rounded-lg bg-muted/50 p-2 font-mono text-xs">
              {entry.result || "（无结果）"}
            </pre>
            {entry.resultTruncated ? (
              <p className="mt-1 text-[11px] text-amber-700 dark:text-amber-300">
                结果已截断，仅显示预览。
              </p>
            ) : null}
          </div>
        </div>
      ) : null}
    </div>
  )
}

function UsageRow({ entry }: { entry: UsageEntry }) {
  const [expanded, setExpanded] = useState(false)
  return (
    <div className="rounded-xl border border-border bg-card/60">
      <button
        type="button"
        aria-label="用量"
        aria-expanded={expanded}
        onClick={() => setExpanded((previous) => !previous)}
        className="flex w-full items-center gap-2 px-3 py-2 text-left text-[11px] text-muted-foreground transition-colors hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <span>{`用量 · ${entry.turns} 回合`}</span>
        <span className="ml-auto font-mono">token</span>
        <ChevronRight
          className={
            expanded
              ? "h-3.5 w-3.5 shrink-0 rotate-90 transition-transform"
              : "h-3.5 w-3.5 shrink-0 transition-transform"
          }
          aria-hidden="true"
        />
      </button>
      {expanded ? (
        <div className="space-y-1 border-t border-border px-3 py-2 text-[11px] text-muted-foreground">
          <p className="font-medium text-foreground">{`共 ${entry.total} tokens`}</p>
          <p>{`输入 ${entry.prompt} · 输出 ${entry.completion} · 推理 ${entry.reasoning}`}</p>
        </div>
      ) : null}
    </div>
  )
}

export type ClarificationChatProps = {
  /** P3-owned version ID; never re-parsed. */
  versionId: string
  /** The active clarification session, or null before one is started. */
  session: ClarificationSession | null
  /** Live/persisted events from `useVersionStream(versionId)`. */
  events: VersionEvent[]
  status: SSEStatus
  /** True while the owning workbench is still loading the overview. */
  sessionLoading?: boolean
  /**
   * The overview load error from the owning workbench. A failed session lookup
   * must not present a start-session CTA next to the retry banner.
   */
  sessionError?: string
  /** Refreshes the workbench overview after starting a session. */
  onChanged?: () => void
  /** Reopens the version stream after a disconnect. */
  onReconnect?: () => void
}

/**
 * Clarification conversation (spec §9.2/§9.4): the persisted P1 message snapshot
 * restores the history on mount/reload, while `agent.activity` events stream
 * text/tool/thinking rows into the transcript. A reconnecting stream never
 * claims the conversation is empty; an AI 503 keeps the input and offers an
 * explicit retry of the failed action (spec §8.6).
 */
export function ClarificationChat({
  versionId,
  session,
  events,
  status,
  sessionLoading = false,
  sessionError = "",
  onChanged,
  onReconnect,
}: ClarificationChatProps) {
  const uid = useId()
  const [messages, setMessages] = useState<ClarificationMessage[]>([])
  const [messagesLoading, setMessagesLoading] = useState(true)
  const [messagesError, setMessagesError] = useState("")
  const [optimistic, setOptimistic] = useState<OptimisticMessage[]>([])
  const [input, setInput] = useState("")
  const [sending, setSending] = useState(false)
  const [actionError, setActionError] = useState("")
  const [aiUnavailable, setAiUnavailable] = useState(false)
  const [failedAction, setFailedAction] = useState<FailedAction | null>(null)
  const [startedSession, setStartedSession] = useState<ClarificationSession | null>(null)
  const [starting, setStarting] = useState(false)
  const [runningAction, setRunningAction] = useState<"analyze" | "recon" | null>(null)

  const messagesRequestRef = useRef(0)
  const optimisticCounterRef = useRef(0)
  /** Highest stream seq already inspected for a finished-run refetch. */
  const lastEventSeqRef = useRef<number | null>(null)
  const reconnectPendingRef = useRef(status === "reconnecting")
  const wasLiveRef = useRef(status === "live")

  const loadMessages = useCallback(async (): Promise<void> => {
    const requestId = ++messagesRequestRef.current
    setMessagesLoading(true)
    setMessagesError("")
    try {
      const page = await listClarificationMessages(versionId, { limit: MESSAGE_LIMIT })
      if (messagesRequestRef.current !== requestId) return
      setMessages(page.items)
    } catch (caught) {
      if (messagesRequestRef.current === requestId) {
        setMessagesError(describeChatError(caught))
      }
    } finally {
      if (messagesRequestRef.current === requestId) setMessagesLoading(false)
    }
  }, [versionId])

  useEffect(() => {
    setMessages([])
    setOptimistic([])
    setInput("")
    setActionError("")
    setAiUnavailable(false)
    setFailedAction(null)
    setStartedSession(null)
    lastEventSeqRef.current = null
    void loadMessages()
    return () => {
      messagesRequestRef.current += 1
    }
  }, [loadMessages])

  // A finished run finalizes the persisted assistant answer: the snapshot must
  // replace the streamed chunks, which otherwise stay the only visible answer
  // for the empty P1 placeholder. The persisted event snapshot is loaded while
  // the stream is still connecting and cannot be newer than the messages
  // already fetched, so only live-run finishes trigger the refetch.
  useEffect(() => {
    const versionEvents = events.filter(
      (event) => event.version_id === null || event.version_id === versionId,
    )
    if (versionEvents.length === 0) return
    let maxSeq = -1
    for (const event of versionEvents) {
      if (event.seq > maxSeq) maxSeq = event.seq
    }
    const previousSeq = lastEventSeqRef.current
    lastEventSeqRef.current = maxSeq
    if (previousSeq === null && status !== "live") return
    const since = previousSeq ?? -1
    const finished = versionEvents.some(
      (event) =>
        event.seq > since &&
        event.type === "agent.activity" &&
        activityKind(event) === "run_finished",
    )
    if (finished) void loadMessages()
  }, [events, loadMessages, versionId, status])

  // Messages completed while the stream was disconnected fall outside the
  // 50-event snapshot window, so a reconnect after a live/closed episode
  // refetches the persisted transcript. A mount that starts already
  // reconnecting refetches on its first `live` transition as well.
  useEffect(() => {
    if (status === "live") {
      wasLiveRef.current = true
      if (reconnectPendingRef.current) {
        reconnectPendingRef.current = false
        void loadMessages()
      }
      return
    }
    if (status === "reconnecting") {
      reconnectPendingRef.current = true
      return
    }
    if ((status === "closed" || status === "connecting") && wasLiveRef.current) {
      reconnectPendingRef.current = true
    }
  }, [status, loadMessages])

  const resolvedSession =
    session !== null && session.status === "active" ? session : (startedSession ?? session)
  const sessionActive = resolvedSession !== null && resolvedSession.status === "active"
  const busy = useMemo(() => streamBusy(events), [events])
  const entries = useMemo(
    () => buildTranscript(messages, optimistic, events),
    [messages, optimistic, events],
  )

  async function runStart(): Promise<void> {
    setStarting(true)
    setActionError("")
    try {
      const created = await startClarificationSession(versionId)
      setStartedSession(created)
      setAiUnavailable(false)
      setFailedAction(null)
      onChanged?.()
    } catch (caught) {
      if (isAiUnavailable(caught)) {
        setAiUnavailable(true)
        setFailedAction({ kind: "start" })
      } else {
        setActionError(describeChatError(caught))
      }
    } finally {
      setStarting(false)
    }
  }

  async function runSend(text: string): Promise<void> {
    setSending(true)
    setActionError("")
    try {
      await sendClarificationMessage(versionId, text)
      optimisticCounterRef.current += 1
      setOptimistic((previous) => [
        ...previous,
        {
          key: `local-${optimisticCounterRef.current}`,
          text,
          at: new Date().toISOString(),
        },
      ])
      if (input.trim() === text) setInput("")
      setAiUnavailable(false)
      setFailedAction(null)
    } catch (caught) {
      if (isAiUnavailable(caught)) {
        setAiUnavailable(true)
        setFailedAction({ kind: "send", message: text })
      } else {
        setActionError(describeChatError(caught))
      }
    } finally {
      setSending(false)
    }
  }

  async function runAi(kind: "analyze" | "recon"): Promise<void> {
    setRunningAction(kind)
    setActionError("")
    try {
      if (kind === "analyze") await analyzeClarification(versionId)
      else await reconClarification(versionId)
      setAiUnavailable(false)
      setFailedAction(null)
    } catch (caught) {
      if (isAiUnavailable(caught)) {
        setAiUnavailable(true)
        setFailedAction({ kind })
      } else {
        setActionError(describeChatError(caught))
      }
    } finally {
      setRunningAction(null)
    }
  }

  async function retryFailedAction(): Promise<void> {
    const action = failedAction
    if (action === null) return
    if (action.kind === "send") {
      await runSend(action.message)
      return
    }
    if (action.kind === "start") {
      await runStart()
      return
    }
    await runAi(action.kind)
  }

  function handleSubmit(event: FormEvent<HTMLFormElement>): void {
    event.preventDefault()
    const text = input.trim()
    if (text === "") {
      setActionError("请输入消息内容。")
      return
    }
    if (text.length > MESSAGE_MAX) {
      setActionError(`消息不能超过 ${MESSAGE_MAX} 个字符。`)
      return
    }
    void runSend(text)
  }

  const aiActionsDisabled =
    !sessionActive || busy || aiUnavailable || runningAction !== null || sending
  const canSend = sessionActive && !sending && !busy
  const reconnectVisible =
    onReconnect !== undefined && (status === "reconnecting" || status === "closed")

  return (
    <section
      aria-label="澄清对话"
      className="flex min-w-0 flex-col rounded-2xl border border-border bg-card p-4"
    >
      <header className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h2 className="text-base font-semibold">澄清对话</h2>
          <p className="mt-0.5 text-xs text-muted-foreground">
            与澄清 Agent 对话；问题与决策会同步到右侧面板。
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          <span role="status" className="text-xs text-muted-foreground">
            {STATUS_LABELS[status]}
          </span>
          {reconnectVisible ? (
            <button
              type="button"
              onClick={onReconnect}
              className="inline-flex items-center gap-1 rounded-lg border border-border bg-card px-2.5 py-1 text-xs font-medium transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <RefreshCw className="h-3 w-3" aria-hidden="true" />
              重新连接
            </button>
          ) : null}
        </div>
      </header>

      {aiUnavailable ? (
        <div
          role="alert"
          className="mt-2 rounded-xl border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-800 dark:text-amber-200"
        >
          <div className="flex items-start gap-2">
            <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
            <div className="min-w-0 flex-1">
              <p>AI 服务暂不可用，请稍后重试。人工新增问题与决策不受影响。</p>
              {failedAction === null ? null : (
                <button
                  type="button"
                  onClick={() => void retryFailedAction()}
                  disabled={sending || starting || runningAction !== null}
                  className="mt-1.5 rounded-lg border border-border bg-card px-2.5 py-1 text-[11px] font-medium text-foreground transition-colors hover:bg-muted disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  重试 AI
                </button>
              )}
            </div>
          </div>
        </div>
      ) : null}

      {actionError === "" ? null : (
        <p
          role="alert"
          className="mt-2 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
        >
          {actionError}
        </p>
      )}

      {messagesError === "" ? null : (
        <div
          role="alert"
          className="mt-2 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
        >
          <p>加载对话失败：{messagesError}</p>
          <button
            type="button"
            onClick={() => void loadMessages()}
            className="mt-1.5 rounded-lg border border-border bg-card px-2.5 py-1 text-[11px] font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            重试
          </button>
        </div>
      )}

      {sessionLoading ? (
        <p className="mt-2 flex items-center gap-1.5 rounded-xl border border-dashed border-border px-3 py-2 text-xs text-muted-foreground">
          <Loader2 className="h-3 w-3 animate-spin" aria-hidden="true" />
          正在加载澄清会话…
        </p>
      ) : sessionActive ? null : resolvedSession === null ? (
        sessionError !== "" ? (
          <div className="mt-2 rounded-xl border border-dashed border-border px-3 py-3">
            <p className="text-sm font-medium">无法加载澄清会话状态。</p>
            <p className="mt-0.5 text-xs text-muted-foreground">
              请先重试上方概览加载，避免重复启动会话。
            </p>
          </div>
        ) : (
          <div className="mt-2 rounded-xl border border-dashed border-border px-3 py-3">
            <p className="text-sm font-medium">尚未开始澄清会话。</p>
            <p className="mt-0.5 text-xs text-muted-foreground">
              开始会话后，Agent 会结合需求进行追问；AI 不可用时人工问题与决策仍可录入。
            </p>
            <button
              type="button"
              onClick={() => void runStart()}
              disabled={starting}
              className="mt-2 inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {starting ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
              ) : null}
              开始澄清会话
            </button>
          </div>
        )
      ) : (
        <div className="mt-2 rounded-xl border border-dashed border-border px-3 py-3">
          <p className="text-sm font-medium">澄清会话已结束。</p>
          <p className="mt-0.5 text-xs text-muted-foreground">可开始新的会话继续澄清。</p>
          <button
            type="button"
            onClick={() => void runStart()}
            disabled={starting}
            className="mt-2 inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-1.5 text-xs font-medium transition-colors hover:bg-muted disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            开始新会话
          </button>
        </div>
      )}

      <div
        role="log"
        aria-label="对话消息"
        className="mt-3 max-h-[28rem] min-h-[10rem] flex-1 space-y-3 overflow-y-auto rounded-xl border border-border bg-muted/20 p-3"
      >
        {messagesLoading && entries.length === 0 ? (
          <p className="flex items-center justify-center gap-1.5 px-3 py-6 text-xs text-muted-foreground">
            <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
            正在加载对话…
          </p>
        ) : null}

        {!messagesLoading && messagesError === "" && entries.length === 0 ? (
          <div className="px-3 py-6 text-center">
            {status === "reconnecting" ? (
              <p className="text-xs text-muted-foreground">
                连接中断，正在尝试恢复；对话内容不会丢失。
              </p>
            ) : status === "connecting" ? (
              <p className="text-xs text-muted-foreground">正在连接对话…</p>
            ) : status === "closed" ? (
              <p className="text-xs text-muted-foreground">连接已断开，请重新连接后继续对话。</p>
            ) : (
              <>
                <p className="text-sm font-medium">暂无消息</p>
                <p className="mt-1 text-xs text-muted-foreground">发送第一条澄清消息开始对话。</p>
              </>
            )}
          </div>
        ) : null}

        {entries.map((entry) => {
          if (entry.kind === "user") {
            return (
              <div key={entry.key} className="flex justify-end">
                <div className="max-w-[85%] rounded-2xl rounded-br-sm bg-primary/10 px-3 py-2">
                  <p className="whitespace-pre-wrap break-words text-sm leading-relaxed">
                    {entry.text}
                  </p>
                </div>
              </div>
            )
          }
          if (entry.kind === "assistant") {
            return (
              <div key={entry.key} className="flex justify-start">
                <div className="max-w-[85%] rounded-2xl rounded-bl-sm bg-card px-3 py-2 shadow-sm">
                  <p className="whitespace-pre-wrap break-words text-sm leading-relaxed">
                    {entry.text}
                  </p>
                </div>
              </div>
            )
          }
          if (entry.kind === "thinking") return <ThinkingRow key={entry.key} entry={entry} />
          if (entry.kind === "tool") return <ToolRow key={entry.key} entry={entry} />
          if (entry.kind === "usage") return <UsageRow key={entry.key} entry={entry} />
          return (
            <p
              key={entry.key}
              className={
                entry.tone === "error"
                  ? "text-xs text-destructive"
                  : "text-xs text-muted-foreground"
              }
            >
              {entry.text}
            </p>
          )
        })}
      </div>

      <form onSubmit={handleSubmit} className="mt-3 space-y-2">
        <div>
          <label htmlFor={`${uid}-message`} className="text-xs font-medium text-muted-foreground">
            消息内容
          </label>
          <textarea
            id={`${uid}-message`}
            value={input}
            rows={3}
            maxLength={MESSAGE_MAX}
            disabled={!sessionActive || sending || busy}
            placeholder={sessionActive ? "输入澄清消息…" : "请先开始澄清会话"}
            onChange={(event) => setInput(event.target.value)}
            className="mt-1 w-full resize-y rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-60"
          />
        </div>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex flex-wrap gap-1.5">
            <button
              type="button"
              onClick={() => void runAi("analyze")}
              disabled={aiActionsDisabled}
              className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-1.5 text-xs font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {runningAction === "analyze" ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
              ) : null}
              需求分析
            </button>
            <button
              type="button"
              onClick={() => void runAi("recon")}
              disabled={aiActionsDisabled}
              className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-1.5 text-xs font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {runningAction === "recon" ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
              ) : null}
              上下文侦察
            </button>
          </div>
          <div className="flex items-center gap-2">
            <span className="text-[11px] text-muted-foreground">
              {input.length}/{MESSAGE_MAX}
            </span>
            <button
              type="submit"
              disabled={!canSend}
              className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3.5 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {sending ? (
                <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
              ) : (
                <Send className="h-4 w-4" aria-hidden="true" />
              )}
              发送
            </button>
          </div>
        </div>
      </form>
    </section>
  )
}
