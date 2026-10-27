import { Construction } from "lucide-react"

import { EmptyState } from "@/components/ui/EmptyState"
import { StatusBadge } from "@/components/ui/StatusBadge"

export type PlaceholderTabProps = {
  title: string
  /** Owning delivery phase, for example "P6". */
  phase: string
  description: string
  onBackToRequirements: () => void
}

/**
 * Placeholder for a version tab whose owning phase has not landed yet (spec
 * §9.1): it names the tab, its phase and a short explanation, offers a way back
 * to the requirements workbench, and deliberately renders no fabricated data.
 */
export function PlaceholderTab({
  title,
  phase,
  description,
  onBackToRequirements,
}: PlaceholderTabProps) {
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <StatusBadge variant="neutral">阶段 {phase}</StatusBadge>
        <span className="text-xs text-muted-foreground">占位 Tab</span>
      </div>
      <EmptyState
        icon={Construction}
        title={title}
        description={description}
        action={
          <button
            type="button"
            onClick={onBackToRequirements}
            className="rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            返回需求与澄清工作台
          </button>
        }
      />
      <p className="text-xs text-muted-foreground">该 Tab 尚未接入数据，不展示占位假数据。</p>
    </div>
  )
}
