"use client"

import { RadioTower, Send, Unplug } from "lucide-react"
import { useCallback, useEffect, useRef, useState } from "react"

import { StatusBadge, type StatusBadgeVariant } from "@/features/shell/StatusBadge"
import { ApiError, apiGet, apiPost, resolveApiUrl } from "@/lib/api-client"
import { getToken } from "@/lib/auth-storage"
import { connectSSE, type SSEEvent, type SSEStatus } from "@/lib/sse-client"

const CHANNEL = "system"
const MAX_EVENTS = 50

const STATUS_META: Record<SSEStatus, { label: string; variant: StatusBadgeVariant }> = {
  connecting: { label: "连接中", variant: "info" },
  live: { label: "实时", variant: "success" },
  reconnecting: { label: "重连中", variant: "warning" },
  closed: { label: "未连接", variant: "neutral" },
}

type StreamEvent = {
  seq: number
  channel?: string
  type: string
  ts?: string
  summary?: string
}

type Snapshot = {
  items?: StreamEvent[]
  snapshot_cursor?: number
  has_more?: boolean
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

/** Parses the frozen V1 event envelope carried on the data line. */
function parseStreamEvent(event: SSEEvent): StreamEvent | null {
  try {
    const data: unknown = JSON.parse(event.data)
    if (!isRecord(data) || typeof data.seq !== "number") return null
    return {
      seq: data.seq,
      channel: typeof data.channel === "string" ? data.channel : undefined,
      type: typeof data.type === "string" ? data.type : event.event,
      ts: typeof data.ts === "string" ? data.ts : undefined,
      summary: typeof data.summary === "string" ? data.summary : "",
    }
  } catch {
    return null
  }
}

function formatTime(ts?: string): string {
  if (!ts) return "—"
  const date = new Date(ts)
  if (Number.isNaN(date.getTime())) return ts
  return date.toLocaleTimeString("zh-CN", { hour12: false })
}

function describeError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
    if (error.status === 403) return "当前账户没有管理员权限。"
    if (error.status === 404) {
      return "测试事件端点未启用（需要 EZTEST_ENABLE_TEST_ENDPOINTS=true）。"
    }
    return error.message
  }
  return error instanceof Error ? error.message : "未知错误"
}

/**
 * Admin SSE diagnostics card: connects to the system channel with the shared
 * fetch-based client, shows the connection state and last seq, publishes three
 * synthetic events through the admin test endpoint, and lists live events.
 * The token is only sent as an Authorization header; it never appears in a URL.
 */
export function SSETestPanel() {
  const [status, setStatus] = useState<SSEStatus>("closed")
  const [events, setEvents] = useState<StreamEvent[]>([])
  const [lastSeq, setLastSeq] = useState<number | null>(null)
  const [notice, setNotice] = useState("")
  const [error, setError] = useState("")
  const [sending, setSending] = useState(false)
  const stopRef = useRef<(() => void) | null>(null)
  const disposedRef = useRef(false)

  useEffect(() => {
    disposedRef.current = false
    return () => {
      disposedRef.current = true
      stopRef.current?.()
      stopRef.current = null
    }
  }, [])

  const loadSnapshot = useCallback(async (): Promise<number | undefined> => {
    try {
      const snapshot = await apiGet<Snapshot>(
        `/api/stream/events?channel=${CHANNEL}&limit=${MAX_EVENTS}`,
      )
      const items = Array.isArray(snapshot?.items) ? [...snapshot.items] : []
      items.sort((left, right) => left.seq - right.seq)
      if (disposedRef.current) return undefined
      setEvents(items.slice(-MAX_EVENTS))
      const cursor =
        typeof snapshot?.snapshot_cursor === "number" ? snapshot.snapshot_cursor : undefined
      setLastSeq(cursor !== undefined && cursor > 0 ? cursor : null)
      return cursor !== undefined && cursor > 0 ? cursor : undefined
    } catch (caught) {
      // An unknown channel has not stored any event yet; connecting live-only is
      // still useful and avoids showing a spurious error on first use.
      if (caught instanceof ApiError && caught.status === 404) {
        if (!disposedRef.current) {
          setEvents([])
          setLastSeq(null)
        }
        return undefined
      }
      throw caught
    }
  }, [])

  const handleConnect = useCallback(async () => {
    if (stopRef.current) return
    setError("")
    setNotice("")
    setStatus("connecting")

    const token = getToken()
    if (!token) {
      setStatus("closed")
      setError("当前会话没有访问令牌，请重新登录。")
      return
    }

    try {
      const cursor = await loadSnapshot()
      if (disposedRef.current || stopRef.current) return

      stopRef.current = connectSSE({
        url: resolveApiUrl(`/api/stream?channel=${CHANNEL}`),
        token,
        lastEventId: cursor,
        onStatus: (next) => {
          if (!disposedRef.current) setStatus(next)
        },
        onEvent: (event) => {
          const parsed = parseStreamEvent(event)
          if (!parsed || disposedRef.current) return
          setEvents((previous) => {
            if (previous.some((item) => item.seq === parsed.seq)) return previous
            return [...previous, parsed].sort((left, right) => left.seq - right.seq)
          })
          setLastSeq((previous) =>
            previous === null || parsed.seq > previous ? parsed.seq : previous,
          )
        },
        onReset: () => {
          if (disposedRef.current) return
          setNotice("接收游标已过期，已重新获取事件快照。")
          void loadSnapshot().catch((caught: unknown) => {
            if (!disposedRef.current) setError(describeError(caught))
          })
        },
      })
    } catch (caught) {
      setStatus("closed")
      setError(describeError(caught))
    }
  }, [loadSnapshot])

  function handleDisconnect() {
    stopRef.current?.()
    stopRef.current = null
  }

  async function handleSendTestEvents() {
    if (status !== "live") {
      setError("请先建立 SSE 连接，再发送测试事件。")
      return
    }
    setError("")
    setNotice("")
    setSending(true)
    try {
      const result = await apiPost<{ seqs?: number[] }>("/api/admin/stream/test-events", {
        count: 3,
        summary: "设置页 SSE 测试事件",
        detail: { source: "settings-sse-test" },
      })
      const seqs = Array.isArray(result?.seqs) ? result.seqs : []
      setNotice(
        seqs.length > 0 ? `已发送 3 条测试事件：seq ${seqs.join("、")}。` : "已发送 3 条测试事件。",
      )
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setSending(false)
    }
  }

  const meta = STATUS_META[status]
  const connected = status !== "closed"

  return (
    <section className="space-y-5" aria-labelledby="sse-panel-title">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 id="sse-panel-title" className="flex items-center gap-2 text-base font-semibold">
            <RadioTower className="h-4 w-4 text-primary" aria-hidden="true" />
            SSE 联通性测试
          </h2>
          <p className="mt-1 max-w-2xl text-sm leading-relaxed text-muted-foreground">
            通过 fetch 流连接 <code className="font-mono text-xs">/api/stream?channel=system</code>
            ，验证状态、最后 seq 与断线重连。断线后客户端按 1s→30s 退避并附带 Last-Event-ID。
          </p>
        </div>
        <StatusBadge variant={meta.variant}>{meta.label}</StatusBadge>
      </div>

      <dl className="grid gap-3 sm:grid-cols-3">
        <div className="rounded-xl border border-border bg-muted/40 p-3">
          <dt className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
            连接状态
          </dt>
          <dd className="mt-1 text-sm font-semibold">{meta.label}</dd>
        </div>
        <div className="rounded-xl border border-border bg-muted/40 p-3">
          <dt className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
            最后 seq
          </dt>
          <dd className="mt-1 font-mono text-sm font-semibold">{lastSeq ?? "—"}</dd>
        </div>
        <div className="rounded-xl border border-border bg-muted/40 p-3">
          <dt className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
            已接收事件
          </dt>
          <dd className="mt-1 font-mono text-sm font-semibold">{events.length}</dd>
        </div>
      </dl>

      <div className="flex flex-wrap gap-2">
        <button
          type="button"
          onClick={() => void handleConnect()}
          disabled={connected}
          className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3.5 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <RadioTower className="h-4 w-4" aria-hidden="true" />
          连接
        </button>
        <button
          type="button"
          onClick={handleDisconnect}
          disabled={!connected}
          className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3.5 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <Unplug className="h-4 w-4" aria-hidden="true" />
          断开
        </button>
        <button
          type="button"
          onClick={() => void handleSendTestEvents()}
          disabled={status !== "live" || sending}
          className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3.5 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <Send className="h-4 w-4" aria-hidden="true" />
          {sending ? "发送中…" : "发送 3 条测试事件"}
        </button>
      </div>

      {error ? (
        <p
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          {error}
        </p>
      ) : null}
      {notice ? (
        <p
          role="status"
          className="rounded-xl border border-emerald-500/25 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-700 dark:text-emerald-300"
        >
          {notice}
        </p>
      ) : null}

      <div className="overflow-x-auto rounded-xl border border-border">
        <table className="min-w-full divide-y divide-border text-sm">
          <caption className="sr-only">SSE 实时事件列表</caption>
          <thead className="bg-muted/50 text-left text-[11px] uppercase tracking-wide text-muted-foreground">
            <tr>
              <th scope="col" className="px-3 py-2 font-medium">
                seq
              </th>
              <th scope="col" className="px-3 py-2 font-medium">
                事件
              </th>
              <th scope="col" className="px-3 py-2 font-medium">
                摘要
              </th>
              <th scope="col" className="px-3 py-2 font-medium">
                时间
              </th>
            </tr>
          </thead>
          <tbody className="divide-y divide-border">
            {events.length === 0 ? (
              <tr>
                <td colSpan={4} className="px-3 py-6 text-center text-sm text-muted-foreground">
                  暂无事件。连接后发送测试事件即可在这里看到实时数据。
                </td>
              </tr>
            ) : (
              events.map((event) => (
                <tr key={event.seq}>
                  <td className="px-3 py-2 font-mono text-xs">{event.seq}</td>
                  <td className="px-3 py-2 font-mono text-xs">{event.type}</td>
                  <td className="px-3 py-2">{event.summary || "—"}</td>
                  <td className="px-3 py-2 text-xs text-muted-foreground">
                    {formatTime(event.ts)}
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>
    </section>
  )
}
