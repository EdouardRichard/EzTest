"use client"

import { useEffect, useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
import { Spinner } from "@/components/ui/Spinner"

import { listPlanItems, type PlanItem, planConflictDetails, updatePlanItem } from "./api"

const STATUS_LABEL: Record<string, string> = {
  pending: "待办",
  in_progress: "进行中",
  blocked: "阻塞",
  done: "已完成",
  completed: "已完成",
  cancelled: "已取消",
  skipped: "已跳过",
}

/** Display order of the status groups: open work first, closed work last. */
const STATUS_ORDER = [
  "pending",
  "in_progress",
  "blocked",
  "done",
  "completed",
  "cancelled",
  "skipped",
]

const TERMINAL_STATUSES = new Set(["done", "completed", "cancelled", "skipped"])
const COMPLETED_STATUSES = new Set(["done", "completed"])
const NON_TOGGLEABLE_STATUSES = new Set(["cancelled", "skipped"])

function isTerminal(status: string): boolean {
  return TERMINAL_STATUSES.has(status)
}

function isCompleted(status: string): boolean {
  return COMPLETED_STATUSES.has(status)
}

function statusLabel(status: string): string {
  return STATUS_LABEL[status] ?? status
}

function byPlanOrder(a: PlanItem, b: PlanItem): number {
  return a.order - b.order || a.id.localeCompare(b.id)
}

type PlanGroup = {
  status: string
  label: string
  terminal: boolean
  items: PlanItem[]
}

/** Groups by plan status with a deterministic open-first order. */
function groupPlanItems(items: PlanItem[]): PlanGroup[] {
  const byStatus = new Map<string, PlanItem[]>()
  for (const item of items) {
    const bucket = byStatus.get(item.status)
    if (bucket === undefined) byStatus.set(item.status, [item])
    else bucket.push(item)
  }
  const statuses = [...byStatus.keys()].sort((a, b) => {
    const aIndex = STATUS_ORDER.indexOf(a)
    const bIndex = STATUS_ORDER.indexOf(b)
    const aRank = aIndex < 0 ? STATUS_ORDER.length : aIndex
    const bRank = bIndex < 0 ? STATUS_ORDER.length : bIndex
    return aRank - bRank || a.localeCompare(b)
  })
  return statuses.map((status) => ({
    status,
    label: statusLabel(status),
    terminal: isTerminal(status),
    items: [...(byStatus.get(status) ?? [])].sort(byPlanOrder),
  }))
}

function groupTestId(status: string): string {
  return `plan-group-${status.replace(/[^a-zA-Z0-9_-]/g, "-")}`
}

export type PlanPanelProps = {
  versionId: string
  /** Echo of the full list after a successful write. */
  onChanged?: (items: PlanItem[]) => void
  /** Nonce bumped by the page when `plan.item_changed` arrives; re-reads the plan. */
  refreshKey?: number
}

/**
 * Version plan overview (spec API-4/API-34). Items are grouped by status, the
 * first two open items are marked current/next, and completing an item sends a
 * `PATCH` carrying the item's `revision`. The toggle is optimistic; a stale
 * revision (409) reverts it and offers an explicit plan refresh.
 */
export function PlanPanel({ versionId, onChanged, refreshKey = 0 }: PlanPanelProps) {
  const [items, setItems] = useState<PlanItem[] | null>(null)
  const [loadError, setLoadError] = useState<unknown>(null)
  const [reloadToken, setReloadToken] = useState(0)
  const [conflict, setConflict] = useState<{
    itemId: string
    actualRevision: number | null
  } | null>(null)
  const [toggleError, setToggleError] = useState<unknown>(null)
  const [toggling, setToggling] = useState<Record<string, boolean>>({})

  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshKey is the SSE-driven reload nonce and reloadToken the explicit refresh counter; both intentionally retrigger the plan read.
  useEffect(() => {
    let cancelled = false
    setLoadError(null)
    void listPlanItems(versionId)
      .then((list) => {
        if (!cancelled) setItems(list)
      })
      .catch((cause: unknown) => {
        if (!cancelled) setLoadError(cause)
      })
    return () => {
      cancelled = true
    }
  }, [versionId, refreshKey, reloadToken])

  function reload(): void {
    setConflict(null)
    setToggleError(null)
    setLoadError(null)
    setReloadToken((token) => token + 1)
  }

  async function toggle(item: PlanItem): Promise<void> {
    if (toggling[item.id] === true || NON_TOGGLEABLE_STATUSES.has(item.status)) return
    const nextStatus = isCompleted(item.status) ? "pending" : "done"
    const previousItems = items
    const baseItems = items ?? []
    const optimisticItems = baseItems.map((entry) =>
      entry.id === item.id ? { ...entry, status: nextStatus } : entry,
    )

    setToggling((previous) => ({ ...previous, [item.id]: true }))
    setConflict(null)
    setToggleError(null)
    setItems(optimisticItems)
    try {
      const updated = await updatePlanItem(versionId, item.id, {
        status: nextStatus,
        revision: item.revision,
      })
      const mergedItems = optimisticItems.map((entry) =>
        entry.id === updated.id ? { ...entry, ...updated } : entry,
      )
      setItems(mergedItems)
      onChanged?.(mergedItems)
    } catch (cause) {
      setItems(previousItems)
      const details = planConflictDetails(cause)
      if (details !== null) {
        setConflict({ itemId: details.itemId, actualRevision: details.actualRevision })
      } else {
        setToggleError(cause)
      }
    } finally {
      setToggling((previous) => {
        const next = { ...previous }
        delete next[item.id]
        return next
      })
    }
  }

  const groups = groupPlanItems(items ?? [])
  const openItems = groups
    .filter((group) => !group.terminal)
    .flatMap((group) => group.items)
    .sort(byPlanOrder)
  const currentId = openItems[0]?.id ?? null
  const nextId = openItems[1]?.id ?? null
  const loading = items === null && loadError === null

  return (
    <section
      data-testid="plan-panel"
      aria-label="版本计划"
      className="space-y-2 rounded-xl border border-border bg-card px-3 py-2"
    >
      <header className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-wrap items-center gap-1.5">
          <h3 className="text-sm font-semibold text-foreground">版本计划</h3>
          {items !== null ? <Badge tone="neutral">共 {items.length} 项</Badge> : null}
        </div>
        <Button size="sm" variant="ghost" disabled={loading} onClick={reload} aria-label="刷新计划">
          刷新
        </Button>
      </header>

      {loadError !== null ? (
        <ErrorState error={loadError} title="计划加载失败" onRetry={reload} />
      ) : null}

      {loading ? (
        <div className="flex items-center gap-2 py-2 text-xs text-muted-foreground">
          <Spinner size="sm" label="加载计划" />
          正在加载计划…
        </div>
      ) : null}

      {items !== null && items.length === 0 && loadError === null ? (
        <EmptyState
          title="暂无计划项"
          description="Host 产出计划或手工创建计划项后会显示在这里，可按状态查看当前与下一步。"
        />
      ) : null}

      {conflict !== null ? (
        <div
          data-testid="plan-conflict"
          role="alert"
          className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-warning/40 bg-warning/10 px-2 py-1.5 text-[11px] text-warning"
        >
          <div>
            <p className="font-medium">计划项已在别处更新，本次修改已撤销。</p>
            <p className="text-muted-foreground">
              服务端版本 {conflict.actualRevision ?? "未知"}；刷新后请基于最新 revision 重试。
            </p>
          </div>
          <Button size="sm" variant="secondary" data-testid="plan-refresh" onClick={reload}>
            刷新计划
          </Button>
        </div>
      ) : null}

      {toggleError !== null ? <InlineError error={toggleError} /> : null}

      {items !== null && items.length > 0 ? (
        <div className="space-y-2">
          {groups.map((group) => (
            <section
              key={group.status}
              data-testid={groupTestId(group.status)}
              aria-label={`${group.label}计划项`}
              className="space-y-1"
            >
              <header className="flex items-center gap-1.5">
                <h4 className="text-xs font-semibold text-foreground">{group.label}</h4>
                <Badge tone={group.terminal ? "neutral" : "info"}>{group.items.length}</Badge>
              </header>
              <ul className="space-y-1">
                {group.items.map((item) => {
                  const checked = isCompleted(item.status)
                  const toggleable = !NON_TOGGLEABLE_STATUSES.has(item.status)
                  return (
                    <li
                      key={item.id}
                      data-testid={`plan-item-${item.id}`}
                      data-status={item.status}
                      className="flex items-start gap-2 rounded-md border border-border bg-background/60 px-2 py-1.5"
                    >
                      <input
                        type="checkbox"
                        className="mt-0.5 accent-primary"
                        checked={checked}
                        disabled={!toggleable || toggling[item.id] === true}
                        aria-label={`完成 ${item.title}`}
                        onChange={() => {
                          void toggle(item)
                        }}
                      />
                      <div className="min-w-0 flex-1">
                        <div className="flex flex-wrap items-center gap-1.5">
                          <p className="text-xs font-medium text-foreground">{item.title}</p>
                          {item.id === currentId ? <Badge tone="info">当前</Badge> : null}
                          {item.id === nextId ? <Badge tone="neutral">下一步</Badge> : null}
                          {item.phase !== "" ? <Badge tone="neutral">{item.phase}</Badge> : null}
                          <span className="text-[10px] text-muted-foreground">
                            v{item.revision}
                          </span>
                        </div>
                        <p className="text-[11px] text-muted-foreground">
                          {item.decision !== "" ? `决策：${item.decision}` : "决策：未记录"}
                        </p>
                      </div>
                      {toggling[item.id] === true ? (
                        <span className="text-[10px] text-muted-foreground">保存中…</span>
                      ) : null}
                    </li>
                  )
                })}
              </ul>
            </section>
          ))}
        </div>
      ) : null}
    </section>
  )
}
