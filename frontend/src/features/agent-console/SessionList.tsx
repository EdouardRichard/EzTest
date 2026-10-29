"use client"

import { MessageSquare, Plus, Trash2 } from "lucide-react"
import { useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { ApiError } from "@/lib/api-client"

import type { AgentSession } from "./agent-api"
import { deleteSession } from "./agent-api"

export type SessionListProps = {
  sessions: AgentSession[]
  selectedId: number | null
  loading: boolean
  hasMore: boolean
  onSelect: (id: number) => void
  onCreate: () => void
  onLoadMore: () => void
  /** Called after the API confirms a deletion so the owner can refresh its list. */
  onDeleted?: (id: number) => void
}

function describeDeleteError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 403) return "当前账户没有管理员权限。"
    if (error.status === 404) return "会话已不存在。"
    return error.message
  }
  return error instanceof Error ? error.message : "未知错误"
}

/** Falls back to a readable label for a session the API left untitled. */
function sessionTitle(session: AgentSession): string {
  const title = session.title.trim()
  return title === "" ? "未命名会话" : title
}

/**
 * Left-hand session rail: create/select/delete sessions plus cursor pagination.
 * The title button's accessible name is exactly the session title (E2E depends
 * on it), so the running marker and delete control stay outside that button.
 */
export function SessionList({
  sessions,
  selectedId,
  loading,
  hasMore,
  onSelect,
  onCreate,
  onLoadMore,
  onDeleted,
}: SessionListProps) {
  const [deletingId, setDeletingId] = useState<number | null>(null)
  const [error, setError] = useState("")

  async function handleDelete(session: AgentSession) {
    setError("")
    setDeletingId(session.id)
    try {
      await deleteSession(session.id)
      onDeleted?.(session.id)
    } catch (caught) {
      setError(`删除会话失败：${describeDeleteError(caught)}`)
    } finally {
      setDeletingId(null)
    }
  }

  return (
    <section
      aria-label="会话列表"
      className="rounded-2xl border border-border bg-card p-3 shadow-sm"
    >
      <div className="flex items-center justify-between gap-2">
        <h2 className="px-1 text-sm font-semibold">会话列表</h2>
        <button
          type="button"
          onClick={onCreate}
          className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground transition-opacity hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <Plus className="h-3.5 w-3.5" aria-hidden="true" />
          新建会话
        </button>
      </div>

      {error ? (
        <p
          role="alert"
          className="mt-2 rounded-lg border border-destructive/30 bg-destructive/10 px-2.5 py-1.5 text-xs text-destructive"
        >
          {error}
        </p>
      ) : null}

      {loading && sessions.length === 0 ? (
        <p className="mt-3 rounded-lg border border-dashed border-border px-3 py-6 text-center text-sm text-muted-foreground">
          正在加载会话…
        </p>
      ) : sessions.length === 0 ? (
        <div className="mt-3">
          <EmptyState
            icon={MessageSquare}
            title="暂无会话"
            description="点击「新建会话」开始与 Agent 对话。"
          />
        </div>
      ) : (
        <ul className="mt-3 space-y-1.5">
          {sessions.map((session) => {
            const selected = session.id === selectedId
            const deleting = deletingId === session.id
            return (
              <li key={session.id}>
                <div
                  className={
                    selected
                      ? "flex items-center gap-1.5 rounded-lg border border-primary/40 bg-primary/10 px-2 py-1.5"
                      : "flex items-center gap-1.5 rounded-lg border border-transparent px-2 py-1.5 hover:bg-muted"
                  }
                >
                  <button
                    type="button"
                    onClick={() => onSelect(session.id)}
                    aria-current={selected ? "true" : undefined}
                    className="min-w-0 flex-1 truncate text-left text-sm font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    {sessionTitle(session)}
                  </button>
                  {session.running ? (
                    <span className="inline-flex shrink-0 items-center gap-1 text-[11px] font-medium text-emerald-700 dark:text-emerald-300">
                      <span
                        aria-hidden="true"
                        className="h-1.5 w-1.5 animate-pulse rounded-full bg-emerald-500"
                      />
                      运行中
                    </span>
                  ) : null}
                  <button
                    type="button"
                    onClick={() => void handleDelete(session)}
                    disabled={deleting}
                    aria-label="删除会话"
                    className="shrink-0 rounded-md p-1 text-muted-foreground transition-colors hover:bg-destructive/10 hover:text-destructive disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
                  </button>
                </div>
              </li>
            )
          })}
        </ul>
      )}

      {hasMore ? (
        <button
          type="button"
          onClick={onLoadMore}
          className="mt-2 w-full rounded-lg border border-border px-3 py-1.5 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          加载更早
        </button>
      ) : null}
    </section>
  )
}
