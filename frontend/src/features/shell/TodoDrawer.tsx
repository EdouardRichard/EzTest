"use client"

import { Bell, ChevronDown, ChevronRight, RefreshCw, X } from "lucide-react"
import Link from "next/link"
import { usePathname } from "next/navigation"
import { useEffect, useState } from "react"

import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { useIsAdmin } from "@/features/auth/useIsAdmin"
import { apiGet } from "@/lib/api-client"
import { formatDateTime } from "@/lib/format"

/** P6 todo kinds of the frozen workspace contract (spec §4.3.2). */
export type TodoKind =
  | "requirement_confirmation"
  | "case_confirmation"
  | "issue_confirmation"
  | "design_pending"
  | "human_request"
  | "dead_work_item"

export type TodoSeverity = "major" | "warning" | "info"

export type TodoItem = {
  kind: TodoKind
  title: string
  version_id: string
  version_title: string
  severity: TodoSeverity
  created_at: string
  expires_at?: string | null
  url: string
}

export type TodoCounts = Record<TodoKind, number>
export type TodoSnapshot = { items: TodoItem[]; counts: TodoCounts }

/** Contract limit of GET /api/todos: 1–200 with a default of 50. */
export const TODO_LIMIT = 50
/** The drawer polls the read-only snapshot every 30 seconds while open. */
export const TODO_POLL_MS = 30_000

const TODO_KINDS: TodoKind[] = [
  "human_request",
  "design_pending",
  "issue_confirmation",
  "requirement_confirmation",
  "case_confirmation",
  "dead_work_item",
]

const GROUP_META: Record<
  TodoKind,
  { label: string; description: string; adminOnly: boolean; tone: BadgeTone }
> = {
  human_request: {
    label: "人工请求",
    description: "等待人工处理的接管或冲突请求",
    adminOnly: false,
    tone: "warning",
  },
  design_pending: {
    label: "待设计",
    description: "已澄清但尚未生成设计的需求",
    adminOnly: false,
    tone: "warning",
  },
  issue_confirmation: {
    label: "问题确认",
    description: "等待确认或关闭的全局问题",
    adminOnly: false,
    tone: "danger",
  },
  requirement_confirmation: {
    label: "需求确认",
    description: "等待用户确认的草稿需求",
    adminOnly: false,
    tone: "warning",
  },
  case_confirmation: {
    label: "用例确认",
    description: "等待用户确认的草稿用例",
    adminOnly: false,
    tone: "warning",
  },
  dead_work_item: {
    label: "失效工作项",
    description: "已耗尽重试预算的执行任务（仅管理员）",
    adminOnly: true,
    tone: "danger",
  },
}

const SEVERITY_META: Record<TodoSeverity, { label: string; tone: BadgeTone }> = {
  major: { label: "严重", tone: "danger" },
  warning: { label: "警告", tone: "warning" },
  info: { label: "提示", tone: "info" },
}

export type TodoState = {
  snapshot: TodoSnapshot | null
  loading: boolean
  error: unknown
  reload: () => void
}

function emptyCounts(): TodoCounts {
  return {
    requirement_confirmation: 0,
    case_confirmation: 0,
    issue_confirmation: 0,
    design_pending: 0,
    human_request: 0,
    dead_work_item: 0,
  }
}

function toCount(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) && value >= 0 ? value : 0
}

/**
 * Normalizes the API snapshot so a partial/unknown payload can never produce a
 * NaN badge or a missing kind group.
 */
function normalizeSnapshot(value: TodoSnapshot | null | undefined): TodoSnapshot {
  const items = Array.isArray(value?.items) ? value.items : []
  const rawCounts = value?.counts as Partial<TodoCounts> | undefined
  const counts = emptyCounts()
  for (const kind of TODO_KINDS) counts[kind] = toCount(rawCounts?.[kind])
  return { items, counts }
}

function fetchTodoSnapshot(): Promise<TodoSnapshot> {
  return apiGet<TodoSnapshot>(`/api/todos?limit=${TODO_LIMIT}`).then(normalizeSnapshot)
}

/**
 * Shared read-only snapshot source used by the topbar entry and the drawer.
 * Polls every 30s while active and reloads on route changes, so opening a
 * different version/location never shows another route's stale feed.
 */
function useTodoSnapshot(active: boolean): TodoState {
  const pathname = usePathname()
  const [snapshot, setSnapshot] = useState<TodoSnapshot | null>(null)
  const [loading, setLoading] = useState(active)
  const [error, setError] = useState<unknown>(null)
  const [reloadToken, setReloadToken] = useState(0)

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken and pathname force a refresh
  useEffect(() => {
    if (!active) return undefined
    let cancelled = false

    const load = () => {
      setLoading(true)
      void fetchTodoSnapshot()
        .then((next) => {
          if (cancelled) return
          setSnapshot(next)
          setError(null)
        })
        .catch((cause: unknown) => {
          if (!cancelled) setError(cause)
        })
        .finally(() => {
          if (!cancelled) setLoading(false)
        })
    }

    load()
    const timer = setInterval(load, TODO_POLL_MS)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [active, pathname, reloadToken])

  return {
    snapshot,
    loading,
    error,
    reload: () => {
      setError(null)
      setReloadToken((token) => token + 1)
    },
  }
}

export type TodoDrawerProps = {
  /** Optional route version. The drawer is global and does not require it. */
  versionId?: string | null
  open: boolean
  onClose: () => void
  /** Shared snapshot from the topbar entry; omitted drawers fetch their own. */
  state?: TodoState
}

/**
 * Global read-only todo drawer (spec §4.3.2/§7): six-kind snapshot from
 * `GET /api/todos`, pre-limit count badges, collapsible groups, deep links and
 * empty/error/loading states. It never resolves or cancels anything, and the
 * admin-only dead work item group is hidden for regular users.
 */
export function TodoDrawer({ open, onClose, state }: TodoDrawerProps) {
  const isAdmin = useIsAdmin()
  const internal = useTodoSnapshot(open && state === undefined)
  const todo = state ?? internal
  const [collapsed, setCollapsed] = useState<ReadonlySet<TodoKind>>(new Set())

  if (!open) return null

  const snapshot = todo.snapshot
  const counts = snapshot?.counts ?? emptyCounts()
  const items = snapshot?.items ?? []
  const total = visibleTotal(counts, isAdmin)
  const groups = TODO_KINDS.map((kind) => ({
    kind,
    count: counts[kind],
    items: items.filter((item) => item.kind === kind),
    meta: GROUP_META[kind],
  })).filter((group) => !group.meta.adminOnly || isAdmin)

  const toggleGroup = (kind: TodoKind) => {
    setCollapsed((previous) => {
      const next = new Set(previous)
      if (next.has(kind)) next.delete(kind)
      else next.add(kind)
      return next
    })
  }

  return (
    <div className="fixed inset-0 z-50">
      <button
        type="button"
        aria-label="关闭待办遮罩"
        onClick={onClose}
        className="absolute inset-0 h-full w-full bg-slate-950/40 backdrop-blur-[1px]"
      />
      <aside
        role="dialog"
        aria-modal="true"
        aria-label="待办"
        data-testid="todo-drawer"
        className="absolute inset-y-0 right-0 flex w-full max-w-md flex-col border-l border-border bg-card text-card-foreground shadow-2xl"
      >
        <header className="flex items-start justify-between gap-3 border-b border-border px-5 py-4">
          <div className="min-w-0">
            <p className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
              <Bell className="size-3.5" aria-hidden="true" />
              全局待办
            </p>
            <h2 className="mt-0.5 truncate text-sm font-semibold text-foreground">
              待办中心{snapshot === null ? "" : ` · ${total} 项`}
            </h2>
          </div>
          <div className="flex items-center gap-1">
            <Button
              variant="ghost"
              size="sm"
              aria-label="刷新待办"
              disabled={todo.loading}
              onClick={todo.reload}
            >
              <RefreshCw className="size-3.5" aria-hidden="true" />
            </Button>
            <Button variant="ghost" size="sm" aria-label="关闭待办" onClick={onClose}>
              <X className="size-4" aria-hidden="true" />
            </Button>
          </div>
        </header>

        <div className="flex-1 space-y-4 overflow-y-auto px-5 py-4">
          {snapshot === null && todo.loading ? (
            <LoadingCard label="正在加载待办…" />
          ) : todo.error !== null ? (
            <ErrorState error={todo.error} title="待办加载失败" onRetry={todo.reload} />
          ) : groups.every((group) => group.count === 0 && group.items.length === 0) ? (
            <EmptyState
              title="暂无待办"
              description="当前没有待确认、待设计或待人工处理的全局事项。"
              className="border-none bg-transparent py-6"
            />
          ) : (
            groups.map(({ kind, count, items: groupItems, meta }) => {
              const isCollapsed = collapsed.has(kind)
              return (
                <section key={kind} aria-label={`${meta.label}待办`} className="space-y-2">
                  <button
                    type="button"
                    data-testid={`todo-group-${kind}`}
                    aria-expanded={!isCollapsed}
                    onClick={() => toggleGroup(kind)}
                    className="flex w-full items-center gap-2 rounded-lg border border-border bg-muted/30 px-3 py-2 text-left transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    {isCollapsed ? (
                      <ChevronRight className="size-3.5 text-muted-foreground" aria-hidden="true" />
                    ) : (
                      <ChevronDown className="size-3.5 text-muted-foreground" aria-hidden="true" />
                    )}
                    <span className="text-xs font-semibold text-foreground">{meta.label}</span>
                    <Badge tone={meta.tone} className="ml-auto">
                      <span data-testid={`todo-count-${kind}`}>{count}</span>
                    </Badge>
                  </button>

                  {isCollapsed ? null : groupItems.length === 0 ? (
                    <p className="px-1 text-[11px] text-muted-foreground">{meta.description}</p>
                  ) : (
                    <ul data-testid={`todo-items-${kind}`} className="space-y-2">
                      {groupItems.map((item, index) => (
                        <li
                          key={`${item.kind}-${item.version_id}-${item.created_at}-${item.url}`}
                          data-testid={`todo-item-${kind}-${index}`}
                          className="rounded-lg border border-border bg-card px-3 py-2"
                        >
                          <div className="flex flex-wrap items-center gap-1.5">
                            <Badge tone={SEVERITY_META[item.severity]?.tone ?? "neutral"}>
                              {SEVERITY_META[item.severity]?.label ?? item.severity}
                            </Badge>
                            <span className="text-[11px] text-muted-foreground">
                              {item.version_title}
                            </span>
                            <Link
                              href={item.url}
                              className="ml-auto text-xs text-primary underline-offset-2 hover:underline"
                            >
                              去处理
                            </Link>
                          </div>
                          <p className="mt-1 text-xs text-foreground">{item.title}</p>
                          {item.expires_at ? (
                            <p className="mt-0.5 text-[10px] text-muted-foreground">
                              过期时间 {formatDateTime(item.expires_at)}
                            </p>
                          ) : null}
                        </li>
                      ))}
                    </ul>
                  )}
                </section>
              )
            })
          )}
        </div>
      </aside>
    </div>
  )
}

function visibleTotal(counts: TodoCounts, isAdmin: boolean): number {
  let total = 0
  for (const kind of TODO_KINDS) {
    if (GROUP_META[kind].adminOnly && !isAdmin) continue
    total += counts[kind]
  }
  return total
}

export type TodoDrawerButtonProps = {
  /** Kept for the Topbar call site; the global entry no longer requires it. */
  versionId?: string | null
}

/**
 * Topbar todo entry. It is global (available without a version), keeps the
 * pre-limit count visible and opens the shared read-only drawer.
 */
export function TodoDrawerButton(_props: TodoDrawerButtonProps) {
  const [open, setOpen] = useState(false)
  const isAdmin = useIsAdmin()
  const todo = useTodoSnapshot(true)
  const total = todo.snapshot === null ? null : visibleTotal(todo.snapshot.counts, isAdmin)

  return (
    <>
      <Button
        size="sm"
        variant="secondary"
        data-testid="todo-drawer-trigger"
        aria-label="打开待办"
        onClick={() => setOpen(true)}
      >
        待办{total === null ? "" : ` ${total}`}
      </Button>
      <TodoDrawer open={open} onClose={() => setOpen(false)} state={todo} />
    </>
  )
}
