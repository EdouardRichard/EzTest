"use client"

import { AlertTriangle, ClipboardCheck, Loader2, RefreshCw, ShieldAlert, X } from "lucide-react"
import Link from "next/link"

import { cn } from "@/lib/utils"

import type { TodoItem, TodoKind, TodoSnapshot } from "./useTodos"

type GroupMeta = {
  kind: TodoKind
  label: string
  description: string
  icon: typeof ClipboardCheck
  accent: string
}

const GROUPS: readonly GroupMeta[] = [
  {
    kind: "review",
    label: "待评审",
    description: "等待人工评审的版本与知识草稿",
    icon: ClipboardCheck,
    accent: "text-sky-500",
  },
  {
    kind: "takeover",
    label: "待接管",
    description: "等待人工完成登录的用例",
    icon: ShieldAlert,
    accent: "text-amber-500",
  },
  {
    kind: "gate",
    label: "待升级",
    description: "需要管理员处置的执行 gate",
    icon: AlertTriangle,
    accent: "text-rose-500",
  },
]

const SEVERITY_CLASS: Record<string, string> = {
  info: "border-sky-500/30 bg-sky-500/10 text-sky-700 dark:text-sky-300",
  warning: "border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-300",
  major: "border-rose-500/30 bg-rose-500/10 text-rose-700 dark:text-rose-300",
}

const SEVERITY_LABEL: Record<string, string> = {
  info: "提示",
  warning: "待处理",
  major: "需升级",
}

export type TodoPanelProps = {
  todos: TodoSnapshot
  loading?: boolean
  error?: string
  onRetry?: () => void
  onClose?: () => void
  /** Called when an item link is followed so the owner can close the panel. */
  onNavigate?: () => void
}

function groupCount(todos: TodoSnapshot, kind: TodoKind): number {
  const counted = todos.counts[kind]
  const rendered = todos.items.filter((item) => item.kind === kind).length
  return Math.max(counted, rendered)
}

function TodoRow({ item, onNavigate }: { item: TodoItem; onNavigate?: () => void }) {
  const severityClass = SEVERITY_CLASS[item.severity] ?? SEVERITY_CLASS.info
  const severityLabel = SEVERITY_LABEL[item.severity] ?? item.severity
  return (
    <li>
      <Link
        href={item.url}
        onClick={() => onNavigate?.()}
        className="block rounded-lg border border-transparent px-2.5 py-2 transition-colors hover:border-border hover:bg-muted/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <span className="flex items-start gap-2">
          <span className="min-w-0 flex-1">
            <span className="block truncate text-sm font-medium">{item.title}</span>
            <span className="mt-0.5 block truncate text-[11px] text-muted-foreground">
              {item.versionName}
            </span>
          </span>
          {item.severity === "" ? null : (
            <span
              className={cn(
                "shrink-0 rounded-full border px-1.5 py-0.5 text-[10px] font-medium leading-none",
                severityClass,
              )}
            >
              {severityLabel}
            </span>
          )}
        </span>
      </Link>
    </li>
  )
}

/**
 * Topbar todo centre panel (spec §6.3): grouped review/takeover/gate items,
 * per-group counts, deep links, loading/error/empty states. Groups with no
 * entries are not rendered, so a regular user (whose server response carries no
 * takeover/gate items) never sees those headings.
 */
export function TodoPanel({
  todos,
  loading = false,
  error = "",
  onRetry,
  onClose,
  onNavigate,
}: TodoPanelProps) {
  const visibleGroups = GROUPS.filter((group) => groupCount(todos, group.kind) > 0)
  const hasItems = visibleGroups.length > 0
  const showLoading = loading && !hasItems && error === ""

  return (
    <div
      role="dialog"
      aria-label="待办中心"
      aria-busy={loading || undefined}
      data-testid="todo-panel"
      className="w-[22rem] max-w-[calc(100vw-2rem)] overflow-hidden rounded-xl border border-border bg-card shadow-xl shadow-black/10"
    >
      <div className="flex items-center justify-between border-b border-border px-3 py-2.5">
        <p className="text-sm font-semibold">待办中心</p>
        <div className="flex items-center gap-1">
          <button
            type="button"
            onClick={onRetry}
            disabled={onRetry === undefined}
            aria-label="刷新待办"
            className="grid h-7 w-7 place-items-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground disabled:hidden focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <RefreshCw
              className={cn("h-3.5 w-3.5", loading && "animate-spin")}
              aria-hidden="true"
            />
          </button>
          {onClose === undefined ? null : (
            <button
              type="button"
              onClick={onClose}
              aria-label="关闭待办中心"
              className="grid h-7 w-7 place-items-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <X className="h-3.5 w-3.5" aria-hidden="true" />
            </button>
          )}
        </div>
      </div>

      <div className="max-h-[26rem] overflow-y-auto p-2">
        {error !== "" ? (
          <div
            role="alert"
            className="rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2.5 text-sm"
          >
            <p className="font-medium text-destructive">待办加载失败</p>
            <p className="mt-0.5 text-xs text-muted-foreground">{error}</p>
            {onRetry === undefined ? null : (
              <button
                type="button"
                onClick={onRetry}
                className="mt-2 rounded-md border border-border bg-card px-2.5 py-1 text-xs font-medium transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                重试
              </button>
            )}
          </div>
        ) : null}

        {showLoading ? (
          <div className="flex items-center justify-center gap-2 px-3 py-8 text-sm text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
            正在加载待办…
          </div>
        ) : null}

        {!loading && error === "" && !hasItems ? (
          <p className="px-3 py-8 text-center text-sm text-muted-foreground">暂无待办</p>
        ) : null}

        {visibleGroups.map((group) => {
          const Icon = group.icon
          const count = groupCount(todos, group.kind)
          const items = todos.items.filter((item) => item.kind === group.kind)
          return (
            <section
              key={group.kind}
              data-testid="todo-group"
              data-kind={group.kind}
              className="py-1"
            >
              <div className="flex items-center gap-2 px-2.5 py-1.5">
                <Icon className={cn("h-3.5 w-3.5", group.accent)} aria-hidden="true" />
                <span className="text-xs font-semibold">{group.label}</span>
                <span
                  data-testid={`todo-count-${group.kind}`}
                  className="rounded-full border border-border bg-muted px-1.5 py-0.5 text-[10px] font-medium leading-none text-muted-foreground"
                >
                  {count}
                </span>
                <span className="ml-auto truncate text-[10px] text-muted-foreground">
                  {group.description}
                </span>
              </div>
              <ul className="space-y-0.5">
                {items.map((item) => (
                  <TodoRow
                    key={`${item.kind}-${item.versionId}-${item.title}-${item.url}`}
                    item={item}
                    onNavigate={onNavigate}
                  />
                ))}
              </ul>
              {items.length === 0 ? (
                <p className="px-3 py-1.5 text-[11px] text-muted-foreground">
                  更多条目已超出本次加载范围
                </p>
              ) : null}
            </section>
          )
        })}
      </div>
    </div>
  )
}
