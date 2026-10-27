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

function describeChatError(error: unknown): string {
  if (!(error instanceof ApiError)) {
    return error instanceof Error ? error.message : "未知错误"
  }
  if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
  if (error.status === 401) return "登录状态已失效，请重新登录。"
  const reason = typeof error.details.reason === "string" ? error.details.reason : ""
  if (reason === "clarification_session_not_active") {
    return "澄清会话未处于进行中状态，请刷新后重试。"
  }
  if (reason === "agent_busy") return "已有澄清任务在运行，请等待其结束。"
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
      status: string
      durationMs: number | null
      at: string
    }
  | { kind: "notice"; key: string; text: string; tone: "info" | "error"; at: string }

type OrderedEntry = { entry: TranscriptEntry; order: number }

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
 * - persisted text/thinking/tool content is rendered once (the API is the
 *   authority after a reload);
 * - live text/thinking chunks of the same `(message_id, part)` are folded;
 * - tool_use/tool_result pairs fold by `call_id`;
 * - a live activity whose message is already in the snapshot is skipped, so a
 *   refresh never duplicates a restored message.
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

  for (const message of messages) {
    knownMessageIds.add(message.id)
    const text = messageText(message.parts)
    if (message.role === "user") {
      if (text !== "")
        add({ kind: "user", key: `message-${message.id}`, text, at: message.createdAt })
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
        persistedToolCalls.push(
          add({
            kind: "tool",
            key: `message-${message.id}-tool-${call.id}`,
            callId: call.id,
            name: call.name,
            args: call.arguments,
            result: "",
            status: "",
            durationMs: null,
            at: message.createdAt,
          }),
        )
      }
      continue
    }
    // Tool-role messages only carry the result; attach them to the most recent
    // persisted call still waiting for one, otherwise render a standalone row.
    const pending = [...persistedToolCalls]
      .reverse()
      .find((item) => item.entry.kind === "tool" && item.entry.result === "")
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
        status: "ok",
        durationMs: null,
        at: message.createdAt,
      })
    }
  }

  for (const local of optimistic) {
    add({ kind: "user", key: local.key, text: local.text, at: local.at })
  }

  const liveText = new Map<string, OrderedEntry>()
  const liveTools = new Map<string, OrderedEntry>()

  for (const event of events) {
    if (event.type !== "agent.activity") continue
    const kind = activityKind(event)
    if (kind === "" || kind === "user_message") continue
    const messageId = readDetailNumber(event, "message_id")
    if (messageId !== null && knownMessageIds.has(messageId)) continue

    if (kind === "text" || kind === "thinking") {
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
      const name = readDetailText(event, "tool") || callId
      const existing = liveTools.get(callId)
      if (existing !== undefined && existing.entry.kind === "tool") {
        if (kind === "tool_use") {
          existing.entry.name = name
          const args = readDetailText(event, "args")
          if (args !== "") existing.entry.args = args
        } else {
          existing.entry.result = readDetailText(event, "result")
          existing.entry.status = readDetailText(event, "status")
          existing.entry.durationMs = readDetailNumber(event, "duration_ms")
        }
        continue
      }
      liveTools.set(
        callId,
        add({
          kind: "tool",
          key: `live-tool-${callId}`,
          callId,
          name,
          args: kind === "tool_use" ? readDetailText(event, "args") : "",
          result: kind === "tool_result" ? readDetailText(event, "result") : "",
          status: kind === "tool_result" ? readDetailText(event, "status") : "",
          durationMs: kind === "tool_result" ? readDetailNumber(event, "duration_ms") : null,
          at: event.ts,
        }),
      )
    }
  }

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
          </div>
          <div>
            <p className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
              结果
            </p>
            <pre className="mt-1 overflow-x-auto whitespace-pre-wrap break-all rounded-lg bg-muted/50 p-2 font-mono text-xs">
              {entry.result || "（无结果）"}
            </pre>
          </div>
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
    void loadMessages()
    return () => {
      messagesRequestRef.current += 1
    }
  }, [loadMessages])

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
            {starting ? <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" /> : null}
            开始澄清会话
          </button>
        </div>
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
