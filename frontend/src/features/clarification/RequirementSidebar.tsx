"use client"

import type { ReactNode } from "react"

import { Badge } from "@/components/ui/Badge"
import { cn } from "@/components/ui/cn"
import { EmptyState } from "@/components/ui/EmptyState"

import {
  REQUIREMENT_PRIORITY_LABEL,
  REQUIREMENT_STATUS_LABEL,
  REQUIREMENT_STATUS_TONE,
} from "./labels"
import type { Requirement, RequirementFilter, TargetOptions } from "./types"

export type RequirementSidebarProps = {
  requirements: Requirement[]
  targetOptions?: TargetOptions
  filter: RequirementFilter
  onFilterChange: (filter: RequirementFilter) => void
  selectedIds: string[]
  onToggleSelection: (requirementId: string) => void
  activeRequirementId: string | null
  onSelectRequirement: (requirementId: string) => void
  /** Batch confirmation bar rendered under the filters. */
  bulk?: ReactNode
  /** Requirement editor rendered under the list. */
  editor?: ReactNode
}

type FilterItem = {
  value: RequirementFilter
  label: string
  count: number
}

function targetLabels(requirement: Requirement, targetOptions?: TargetOptions): string[] {
  if (targetOptions === undefined) return []
  const labels = new Map<string, string>()
  for (const option of [...targetOptions.suts, ...targetOptions.pages, ...targetOptions.apis]) {
    labels.set(`${option.id}`, option.label)
  }
  const ids = [
    ...requirement.targets.version_suts,
    ...requirement.targets.pages,
    ...requirement.targets.apis,
  ]
  return ids.map((id) => labels.get(id)).filter((label): label is string => label !== undefined)
}

/** Requirement list with counts, status filters and multi-select for batch confirm. */
export function RequirementSidebar({
  requirements,
  targetOptions,
  filter,
  onFilterChange,
  selectedIds,
  onToggleSelection,
  activeRequirementId,
  onSelectRequirement,
  bulk,
  editor,
}: RequirementSidebarProps) {
  const counts = {
    all: requirements.length,
    draft: requirements.filter((requirement) => requirement.status === "draft").length,
    clarified: requirements.filter((requirement) => requirement.status === "clarified").length,
    closed: requirements.filter(
      (requirement) => requirement.status === "closed" || requirement.status === "obsolete",
    ).length,
  }
  const filters: FilterItem[] = [
    { value: "all", label: "全部", count: counts.all },
    { value: "draft", label: "草案", count: counts.draft },
    { value: "clarified", label: "已澄清", count: counts.clarified },
    { value: "closed", label: "已闭环", count: counts.closed },
  ]
  const visible =
    filter === "all"
      ? requirements
      : filter === "closed"
        ? requirements.filter(
            (requirement) => requirement.status === "closed" || requirement.status === "obsolete",
          )
        : requirements.filter((requirement) => requirement.status === filter)

  return (
    <section
      data-testid="requirement-sidebar"
      aria-label="需求列表"
      className="surface-card flex flex-col overflow-hidden"
    >
      <header className="flex items-center gap-2 border-b border-border px-3.5 py-2.5">
        <div>
          <h2 className="text-sm font-semibold text-foreground">
            需求 <span className="font-medium text-muted-foreground">{counts.all}</span>
          </h2>
          <p className="text-[10px] text-muted-foreground">
            已澄清 {counts.clarified} · 草案 {counts.draft} · 已闭环 {counts.closed}
          </p>
        </div>
      </header>

      <div className="flex flex-wrap gap-1 px-3 pt-2.5">
        {filters.map((item) => (
          <button
            key={item.value}
            type="button"
            data-testid={`req-filter-${item.value}`}
            aria-pressed={filter === item.value}
            onClick={() => onFilterChange(item.value)}
            className={cn(
              "rounded-full border px-2 py-0.5 text-[10.5px] transition-colors",
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
              filter === item.value
                ? "border-primary/40 bg-primary/10 text-primary"
                : "border-border bg-card text-muted-foreground hover:text-foreground",
            )}
          >
            {item.label} {item.count}
          </button>
        ))}
      </div>

      {bulk}

      <div className="max-h-[340px] space-y-1 overflow-y-auto px-2.5 py-2.5">
        {visible.length === 0 ? (
          <EmptyState
            title="暂无需求"
            description="对话产出的需求草案会出现在这里，可编辑后单条或批量确认。"
            className="border-none bg-transparent py-6"
          />
        ) : (
          visible.map((requirement) => {
            const active = requirement.id === activeRequirementId
            const chips = targetLabels(requirement, targetOptions)
            return (
              <div
                key={requirement.id}
                data-testid={`requirement-item-${requirement.id}`}
                data-active={active ? "true" : "false"}
                className={cn(
                  "rounded-lg border px-2.5 py-2",
                  active
                    ? "border-primary/30 bg-primary/5"
                    : "border-transparent hover:bg-muted/60",
                )}
              >
                <div className="flex items-center gap-2">
                  <input
                    type="checkbox"
                    aria-label={`选择${requirement.title}`}
                    checked={selectedIds.includes(requirement.id)}
                    onChange={() => onToggleSelection(requirement.id)}
                    className="size-3.5 accent-primary"
                  />
                  <button
                    type="button"
                    onClick={() => onSelectRequirement(requirement.id)}
                    className="min-w-0 flex-1 truncate text-left text-xs font-semibold text-foreground hover:underline"
                  >
                    {requirement.title}
                  </button>
                  <Badge tone={REQUIREMENT_STATUS_TONE[requirement.status]}>
                    {REQUIREMENT_STATUS_LABEL[requirement.status]}
                  </Badge>
                </div>
                <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 pl-5 text-[10px] text-muted-foreground">
                  <span>v{requirement.revision}</span>
                  <span>{REQUIREMENT_PRIORITY_LABEL[requirement.priority]}</span>
                  {chips.slice(0, 3).map((label) => (
                    <span
                      key={`${requirement.id}-${label}`}
                      className="rounded-full bg-accent px-1.5 py-0.5 text-accent-foreground"
                    >
                      {label}
                    </span>
                  ))}
                  {chips.length > 3 ? <span>+{chips.length - 3}</span> : null}
                </div>
              </div>
            )
          })
        )}
      </div>

      {editor}
    </section>
  )
}
