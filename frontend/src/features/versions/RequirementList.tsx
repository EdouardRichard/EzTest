"use client"

import { FileText, Loader2, Plus } from "lucide-react"
import { useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"

import type { Requirement, RequirementStatus } from "./types"

/** Display metadata for every requirement status the API may return (spec §6.2). */
export const REQUIREMENT_STATUS_META: Record<
  RequirementStatus,
  { label: string; variant: StatusBadgeVariant }
> = {
  draft: { label: "草稿", variant: "neutral" },
  clarified: { label: "已澄清", variant: "success" },
  designed: { label: "已设计", variant: "info" },
  verified: { label: "已验证", variant: "success" },
  obsolete: { label: "已废弃", variant: "neutral" },
}

export function requirementStatusLabel(status: RequirementStatus): string {
  return REQUIREMENT_STATUS_META[status].label
}

/** The workbench list can narrow the fetched requirements to one pipeline state. */
type ListFilter = "all" | "draft" | "clarified"

const FILTERS: readonly { value: ListFilter; label: string }[] = [
  { value: "all", label: "全部" },
  { value: "draft", label: "草稿" },
  { value: "clarified", label: "已澄清" },
]

export type RequirementListProps = {
  requirements: Requirement[]
  selectedId: string | null
  onSelect: (requirementId: string) => void
  onCreate: () => void
  /** Disable the create entry while the create form is already open. */
  creating?: boolean
  loading?: boolean
  /** Presents a failure banner without discarding the last loaded requirements. */
  error?: string
  onRetry?: () => void
  hasMore?: boolean
  loadingMore?: boolean
  onLoadMore?: () => void
}

/**
 * Left column of the requirement workbench (spec §9.2): the requirement list
 * with a draft/clarified filter, selection, open-question/relation/attachment
 * badges and the create entry. Counts come from the requirement DTO; nothing is
 * inferred or fabricated here.
 */
export function RequirementList({
  requirements,
  selectedId,
  onSelect,
  onCreate,
  creating = false,
  loading = false,
  error = "",
  onRetry,
  hasMore = false,
  loadingMore = false,
  onLoadMore,
}: RequirementListProps) {
  const [filter, setFilter] = useState<ListFilter>("all")

  const visible = requirements.filter((requirement) => {
    if (filter === "all") return true
    return requirement.status === filter
  })

  return (
    <section
      aria-label="需求列表"
      className="flex min-w-0 flex-col rounded-2xl border border-border bg-card"
    >
      <header className="space-y-3 border-b border-border p-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-base font-semibold">需求列表</h2>
          <button
            type="button"
            onClick={onCreate}
            disabled={creating}
            className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-1.5 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <Plus className="h-4 w-4" aria-hidden="true" />
            新增需求
          </button>
        </div>

        <fieldset className="flex flex-wrap gap-1">
          <legend className="sr-only">需求状态筛选</legend>
          {FILTERS.map((item) => (
            <button
              key={item.value}
              type="button"
              aria-pressed={filter === item.value}
              onClick={() => setFilter(item.value)}
              className="rounded-lg border border-border px-2.5 py-1 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted aria-pressed:border-primary/40 aria-pressed:bg-primary/10 aria-pressed:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {item.label}
            </button>
          ))}
        </fieldset>
      </header>

      {error === "" ? null : (
        <div
          role="alert"
          className="m-3 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
        >
          <p>加载需求失败：{error}</p>
          {onRetry === undefined ? null : (
            <button
              type="button"
              onClick={onRetry}
              className="mt-1.5 rounded-lg border border-border bg-card px-2.5 py-1 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              重试
            </button>
          )}
        </div>
      )}

      {loading && requirements.length === 0 ? (
        <p className="flex items-center gap-2 px-4 py-8 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
          正在加载需求…
        </p>
      ) : null}

      {!loading && requirements.length === 0 && error === "" ? (
        <div className="p-3">
          <EmptyState
            icon={FileText}
            title="暂无需求"
            description="先新增需求并填写标题，再补充描述、验收标准与优先级。"
            action={
              <button
                type="button"
                onClick={onCreate}
                disabled={creating}
                className="rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                新增第一条需求
              </button>
            }
          />
        </div>
      ) : null}

      {requirements.length > 0 && visible.length === 0 ? (
        <p className="px-4 py-6 text-sm text-muted-foreground">当前筛选下暂无需求。</p>
      ) : null}

      {visible.length === 0 ? null : (
        <ul aria-label="需求条目" className="space-y-1 p-2">
          {visible.map((requirement) => {
            const status = REQUIREMENT_STATUS_META[requirement.status]
            return (
              <li key={requirement.id}>
                <button
                  type="button"
                  aria-pressed={requirement.id === selectedId}
                  onClick={() => onSelect(requirement.id)}
                  className="w-full rounded-xl border border-transparent px-3 py-2 text-left transition-colors hover:bg-muted aria-pressed:border-primary/40 aria-pressed:bg-primary/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  <span className="flex items-start justify-between gap-2">
                    <span className="break-all text-sm font-medium">{requirement.title}</span>
                    <StatusBadge variant={status.variant}>{status.label}</StatusBadge>
                  </span>
                  <span className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
                    {requirement.openQuestionCount > 0 ? (
                      <span>待澄清 {requirement.openQuestionCount}</span>
                    ) : null}
                    {requirement.relationCount > 0 ? (
                      <span>关系 {requirement.relationCount}</span>
                    ) : null}
                    {requirement.attachmentCount > 0 ? (
                      <span>附件 {requirement.attachmentCount}</span>
                    ) : null}
                  </span>
                </button>
              </li>
            )
          })}
        </ul>
      )}

      {hasMore && onLoadMore !== undefined ? (
        <div className="border-t border-border p-2">
          <button
            type="button"
            onClick={onLoadMore}
            disabled={loadingMore}
            className="w-full rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {loadingMore ? "加载中…" : "加载更多需求"}
          </button>
        </div>
      ) : null}
    </section>
  )
}
