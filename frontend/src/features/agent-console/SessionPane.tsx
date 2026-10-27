"use client"

import { useMemo, useState } from "react"

import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"
import { ApiError } from "@/lib/api-client"
import type { SSEStatus } from "@/lib/sse-client"

import { activityKind, readDetailText } from "./ActivityItem"
import type { AgentEvent, AgentSession } from "./agent-api"
import { sendMessage, stopSession } from "./agent-api"
import { Composer } from "./Composer"
import { Transcript } from "./Transcript"
import { useAgentSession } from "./useAgentSession"

export type SessionPaneProps = {
  sessionId: number | null
  session: AgentSession | null
}

type RunStatus = "running" | "done" | "aborted" | "failed" | "interrupted"

const RUN_STATUS_META: Record<RunStatus, { label: string; variant: StatusBadgeVariant }> = {
  running: { label: "运行中", variant: "info" },
  done: { label: "已完成", variant: "success" },
  aborted: { label: "已中止", variant: "warning" },
  failed: { label: "失败", variant: "danger" },
  interrupted: { label: "已中断", variant: "warning" },
}

const CONNECTION_META: Record<SSEStatus, { label: string; variant: StatusBadgeVariant }> = {
  connecting: { label: "连接中", variant: "info" },
  live: { label: "实时", variant: "success" },
  reconnecting: { label: "重连中", variant: "warning" },
  closed: { label: "已断开", variant: "neutral" },
}

function terminalStatus(value: string | null | undefined): RunStatus | null {
  if (value === "done" || value === "aborted" || value === "failed" || value === "interrupted") {
    return value
  }
  return null
}

/**
 * Derives the visible run status. Live `running` wins; otherwise the newest
 * persisted run_finished in the transcript; otherwise the session's last run.
 */
function deriveRunStatus(
  running: boolean,
  activities: AgentEvent[],
  session: AgentSession | null,
): RunStatus | null {
  if (running) return "running"
  for (let index = activities.length - 1; index >= 0; index -= 1) {
    const event = activities[index]
    const kind = activityKind(event)
    if (kind === "run_finished") return terminalStatus(readDetailText(event, "status")) ?? "done"
    if (kind === "run_started") return "running"
  }
  return terminalStatus(session?.last_run?.status)
}

function describeSendError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 403) return "仅管理员可用"
    if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
    if (error.status === 409) return `上一条仍在执行：${error.message}`
    return error.message
  }
  return error instanceof Error ? error.message : "未知错误"
}

function describeActionError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 403) return "仅管理员可用"
    if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
    return error.message
  }
  return error instanceof Error ? error.message : "未知错误"
}

/**
 * The selected session's transcript + composer. Sends and stops through
 * `agent-api`, then refreshes the snapshot; the live SSE merge is owned by
 * `useAgentSession`.
 */
export function SessionPane({ sessionId, session }: SessionPaneProps) {
  const { activities, status, loading, error, running, hasMore, loadEarlier, refresh, getDetail } =
    useAgentSession(sessionId, session)
  const [sending, setSending] = useState(false)
  const [stopping, setStopping] = useState(false)
  const [actionError, setActionError] = useState("")

  const runStatus = useMemo(
    () => deriveRunStatus(running, activities, session),
    [running, activities, session],
  )

  async function handleSend(message: string): Promise<void> {
    if (sessionId === null) return
    setActionError("")
    setSending(true)
    try {
      await sendMessage(sessionId, message)
      await refresh()
    } catch (caught) {
      setActionError(describeSendError(caught))
    } finally {
      setSending(false)
    }
  }

  async function handleStop(): Promise<void> {
    if (sessionId === null) return
    setActionError("")
    setStopping(true)
    try {
      await stopSession(sessionId)
      await refresh()
    } catch (caught) {
      setActionError(describeActionError(caught))
    } finally {
      setStopping(false)
    }
  }

  if (sessionId === null) {
    return (
      <section
        aria-label="会话内容"
        className="rounded-2xl border border-border bg-card p-5 shadow-sm"
      >
        <p className="text-sm text-muted-foreground">选择或新建会话</p>
      </section>
    )
  }

  const title = session?.title?.trim() || `会话 #${sessionId}`
  const statusMeta = runStatus === null ? null : RUN_STATUS_META[runStatus]
  const connectionMeta = CONNECTION_META[status]

  return (
    <section
      aria-label="会话内容"
      className="space-y-3 rounded-2xl border border-border bg-card p-4 shadow-sm"
    >
      <header className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <h2 className="truncate text-sm font-semibold">{title}</h2>
          <p className="mt-0.5 text-xs text-muted-foreground">
            {session?.profile_key ?? "Agent 会话"}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          {statusMeta ? (
            <StatusBadge variant={statusMeta.variant}>{statusMeta.label}</StatusBadge>
          ) : (
            <StatusBadge variant="neutral" dot={false}>
              空闲
            </StatusBadge>
          )}
          <StatusBadge variant={connectionMeta.variant} dot={false}>
            {connectionMeta.label}
          </StatusBadge>
        </div>
      </header>

      {error ? (
        <p
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          {error}
        </p>
      ) : null}

      <Transcript
        activities={activities}
        status={status}
        loading={loading}
        hasMore={hasMore}
        onLoadEarlier={() => void loadEarlier()}
        getDetail={getDetail}
      />

      <Composer
        running={running}
        sending={sending}
        stopping={stopping}
        error={actionError}
        onSend={(message) => void handleSend(message)}
        onStop={() => void handleStop()}
      />
    </section>
  )
}
