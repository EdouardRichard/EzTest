"use client"

import { useEffect, useMemo, useRef, useState } from "react"

import type { SSEStatus } from "@/lib/sse-client"
import {
  ActivityItem,
  activityKind,
  readDetailId,
  readDetailNumber,
  readDetailText,
  type TranscriptEntry,
} from "./ActivityItem"
import type { AgentEvent, AgentMessageDetail } from "./agent-api"

export type TranscriptProps = {
  activities: AgentEvent[]
  status: SSEStatus
  loading?: boolean
  hasMore?: boolean
  onLoadEarlier?: () => void
  getDetail?: (messageId: number) => Promise<AgentMessageDetail>
}

type RunGroup = {
  key: string
  runId: string | null
  user: AgentEvent | null
  started: AgentEvent | null
  finished: AgentEvent | null
  entries: TranscriptEntry[]
  usage: AgentEvent[]
}

type EntryLocation = { group: RunGroup; index: number }

/**
 * Folds the seq-ordered activity list into per-run groups:
 * - thinking/text chunks merge by `(message_id, part)`;
 * - tool_use/tool_result pair by `call_id`;
 * - usage events collect at the run tail;
 * - user messages stay with the run they started.
 */
function buildRunGroups(activities: AgentEvent[]): RunGroup[] {
  const groups: RunGroup[] = []
  const byRun = new Map<string, RunGroup>()
  const chunkIndex = new Map<string, EntryLocation>()
  const toolIndex = new Map<string, EntryLocation>()
  let current: RunGroup | null = null

  function createGroup(runId: string | null, key: string): RunGroup {
    const group: RunGroup = {
      key,
      runId,
      user: null,
      started: null,
      finished: null,
      entries: [],
      usage: [],
    }
    groups.push(group)
    if (runId !== null) byRun.set(runId, group)
    current = group
    return group
  }

  function groupFor(event: AgentEvent, runId: string | null): RunGroup {
    if (runId !== null) {
      const existing = byRun.get(runId)
      if (existing) {
        current = existing
        return existing
      }
      return createGroup(runId, `run-${runId}`)
    }
    if (current) return current
    return createGroup(null, `runless-${event.seq}`)
  }

  function appendEntry(group: RunGroup, entry: TranscriptEntry): EntryLocation {
    group.entries.push(entry)
    return { group, index: group.entries.length - 1 }
  }

  for (const event of activities) {
    const kind = activityKind(event)
    const runId = readDetailId(event, "run_id")

    switch (kind) {
      case "user_message": {
        const group = createGroup(null, `user-${event.seq}`)
        group.user = event
        break
      }
      case "run_started": {
        let group: RunGroup | undefined = runId === null ? undefined : byRun.get(runId)
        if (
          !group &&
          current !== null &&
          current.runId === null &&
          current.started === null &&
          current.entries.length === 0
        ) {
          // Attach the run to the group opened by its user message.
          group = current
        }
        if (!group) group = createGroup(runId, runId === null ? `run-${event.seq}` : `run-${runId}`)
        if (runId !== null) {
          group.runId = runId
          byRun.set(runId, group)
        }
        group.started = event
        current = group
        break
      }
      case "run_finished": {
        const group: RunGroup | null = (runId === null ? undefined : byRun.get(runId)) ?? current
        if (!group) break
        group.finished = event
        if (runId !== null) {
          group.runId = runId
          byRun.set(runId, group)
        }
        current = group
        break
      }
      case "thinking":
      case "text": {
        const messageId = readDetailNumber(event, "message_id")
        if (messageId === null) break
        const part = readDetailNumber(event, "part") ?? 0
        const group = groupFor(event, runId)
        const chunkKey = `${group.key}|${kind}|${messageId}|${part}`
        const text = readDetailText(event, "text")
        const existing = chunkIndex.get(chunkKey)
        if (existing) {
          const entry = existing.group.entries[existing.index]
          if (entry.kind === "thinking" || entry.kind === "text") entry.text += text
        } else {
          const entry: TranscriptEntry =
            kind === "text"
              ? {
                  kind: "text",
                  key: `text-${messageId}-${part}-${event.seq}`,
                  messageId,
                  part,
                  text,
                }
              : {
                  kind: "thinking",
                  key: `thinking-${messageId}-${part}-${event.seq}`,
                  messageId,
                  part,
                  text,
                }
          chunkIndex.set(chunkKey, appendEntry(group, entry))
        }
        break
      }
      case "tool_use":
      case "tool_result": {
        const callId = readDetailId(event, "call_id")
        if (callId === null) break
        const group = groupFor(event, runId)
        const toolKey = `${group.key}|${callId}`
        const name = readDetailText(event, "tool")
        const existing = toolIndex.get(toolKey)
        if (existing) {
          const entry = existing.group.entries[existing.index]
          if (entry.kind === "tool") {
            if (kind === "tool_use") entry.use = event
            else entry.result = event
            if (name) entry.name = name
          }
        } else {
          toolIndex.set(
            toolKey,
            appendEntry(group, {
              kind: "tool",
              key: `tool-${callId}-${event.seq}`,
              callId,
              name: name || callId,
              use: kind === "tool_use" ? event : null,
              result: kind === "tool_result" ? event : null,
            }),
          )
        }
        break
      }
      case "usage": {
        groupFor(event, runId).usage.push(event)
        break
      }
      case "decision": {
        appendEntry(groupFor(event, runId), {
          kind: "decision",
          key: `decision-${event.seq}`,
          event,
        })
        break
      }
      default: {
        appendEntry(groupFor(event, runId), {
          kind: "notice",
          key: `notice-${event.seq}`,
          event,
        })
        break
      }
    }
  }

  return groups
}

function formatTime(ts: string): string {
  if (!ts) return ""
  const date = new Date(ts)
  if (Number.isNaN(date.getTime())) return ""
  return date.toLocaleTimeString("zh-CN", { hour12: false })
}

function runOutcome(event: AgentEvent): string {
  switch (readDetailText(event, "status")) {
    case "aborted":
      return "运行终止"
    case "failed":
      return "运行出错"
    case "interrupted":
      return "运行中断"
    default:
      return "运行结束"
  }
}

function UserMessage({ event }: { event: AgentEvent }) {
  const text = readDetailText(event, "text")
  const truncated = event.detail.truncated === true
  return (
    <div className="flex justify-end">
      <div className="max-w-[85%] rounded-2xl rounded-br-sm bg-primary/10 px-3 py-2">
        <p className="whitespace-pre-wrap break-words text-sm leading-relaxed">{text}</p>
        {truncated ? <p className="mt-1 text-[11px] text-muted-foreground">消息已截断</p> : null}
      </div>
    </div>
  )
}

function RunHeading({ event, finished }: { event: AgentEvent; finished: AgentEvent | null }) {
  const runId = readDetailId(event, "run_id")
  const profileKey = readDetailText(event, "profile_key")
  const time = formatTime(event.ts)
  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1 border-b border-border/70 pb-1 text-[11px] text-muted-foreground">
      <span className="font-medium text-foreground">运行 #{runId ?? "?"}</span>
      {profileKey ? <span>{profileKey}</span> : null}
      {time ? <span>{time}</span> : null}
      {finished ? <span className="ml-auto">{runOutcome(finished)}</span> : null}
    </div>
  )
}

function UsageSummary({ events }: { events: AgentEvent[] }) {
  const [expanded, setExpanded] = useState(false)
  const totals = events.reduce(
    (accumulator, event) => ({
      prompt: accumulator.prompt + (readDetailNumber(event, "prompt_tokens") ?? 0),
      completion: accumulator.completion + (readDetailNumber(event, "completion_tokens") ?? 0),
      reasoning: accumulator.reasoning + (readDetailNumber(event, "reasoning_tokens") ?? 0),
      total: accumulator.total + (readDetailNumber(event, "total_tokens") ?? 0),
    }),
    { prompt: 0, completion: 0, reasoning: 0, total: 0 },
  )

  return (
    <div className="rounded-xl border border-border bg-card/60">
      <button
        type="button"
        aria-label="用量"
        aria-expanded={expanded}
        onClick={() => setExpanded((previous) => !previous)}
        className="flex w-full items-center justify-between gap-2 px-3 py-1.5 text-left text-[11px] text-muted-foreground transition-colors hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <span>用量 · {events.length} 回合</span>
        <span className="font-mono">token</span>
      </button>
      {expanded ? (
        <div className="space-y-1 border-t border-border px-3 py-2 text-[11px] text-muted-foreground">
          <p className="font-medium text-foreground">共 {totals.total} tokens</p>
          <p>
            输入 {totals.prompt} · 输出 {totals.completion} · 推理 {totals.reasoning}
          </p>
          <ul className="space-y-1">
            {events.map((event) => (
              <li key={event.seq} className="flex flex-wrap gap-x-3">
                <span>第 {readDetailNumber(event, "turn") ?? "?"} 回合</span>
                <span>输入 {readDetailNumber(event, "prompt_tokens") ?? 0}</span>
                <span>输出 {readDetailNumber(event, "completion_tokens") ?? 0}</span>
                <span>推理 {readDetailNumber(event, "reasoning_tokens") ?? 0}</span>
                <span>合计 {readDetailNumber(event, "total_tokens") ?? 0}</span>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
    </div>
  )
}

/**
 * The transcript pane: run-grouped activities, seq/merge-friendly rendering,
 * "load earlier" pagination and auto-scroll that pauses once the user scrolls
 * up. While the stream is connecting/reconnecting it never claims the session
 * is empty.
 */
export function Transcript({
  activities,
  status,
  loading = false,
  hasMore = false,
  onLoadEarlier,
  getDetail,
}: TranscriptProps) {
  const groups = useMemo(() => buildRunGroups(activities), [activities])
  const scrollRef = useRef<HTMLDivElement | null>(null)
  const stickToBottomRef = useRef(true)

  useEffect(() => {
    if (activities.length === 0) return
    const container = scrollRef.current
    if (!container || !stickToBottomRef.current) return
    container.scrollTop = container.scrollHeight
  }, [activities])

  function handleScroll() {
    const container = scrollRef.current
    if (!container) return
    const distance = container.scrollHeight - container.scrollTop - container.clientHeight
    stickToBottomRef.current = distance < 48
  }

  const connecting = status === "connecting" || status === "reconnecting"
  const empty = !loading && activities.length === 0

  return (
    <div
      ref={scrollRef}
      onScroll={handleScroll}
      role="log"
      aria-label="转录内容"
      className="max-h-[60vh] min-h-[10rem] space-y-4 overflow-y-auto rounded-xl border border-border bg-muted/20 p-3"
    >
      {hasMore ? (
        <button
          type="button"
          onClick={onLoadEarlier}
          className="w-full rounded-lg border border-border bg-card px-3 py-1.5 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          加载更早
        </button>
      ) : null}

      {loading && activities.length === 0 ? (
        <p className="rounded-xl border border-dashed border-border px-3 py-6 text-center text-sm text-muted-foreground">
          正在加载转录…
        </p>
      ) : null}

      {empty ? (
        <div className="rounded-xl border border-dashed border-border bg-card/60 px-4 py-8 text-center">
          {connecting ? (
            <p className="text-sm text-muted-foreground">正在连接会话…</p>
          ) : status === "closed" ? (
            <p className="text-sm text-muted-foreground">连接已断开</p>
          ) : (
            <>
              <p className="text-sm font-medium">暂无消息</p>
              <p className="mt-1 text-xs text-muted-foreground">发送第一条消息开始对话。</p>
            </>
          )}
        </div>
      ) : null}

      {groups.map((group) => (
        <section
          key={group.key}
          aria-label={group.runId === null ? undefined : `运行 ${group.runId}`}
          className="space-y-2"
        >
          {group.user ? <UserMessage event={group.user} /> : null}
          {group.started ? <RunHeading event={group.started} finished={group.finished} /> : null}
          {group.entries.map((entry) => (
            <ActivityItem key={entry.key} entry={entry} getDetail={getDetail} />
          ))}
          {group.usage.length > 0 ? <UsageSummary events={group.usage} /> : null}
          {!group.started && group.finished ? (
            <p className="text-[11px] text-muted-foreground">{runOutcome(group.finished)}</p>
          ) : null}
        </section>
      ))}
    </div>
  )
}
