import { Badge } from "@/components/ui/Badge"

import type { CodeAnalysisUnmapped } from "../types"
import { CODE_UNMAPPED_REASON_LABELS } from "../types"

export type UnmappedPanelProps = {
  unmapped: CodeAnalysisUnmapped[]
}

/** Files the analysis could not map to facts/tests (spec §5.4). */
export function UnmappedPanel({ unmapped }: UnmappedPanelProps) {
  return (
    <section aria-label="未映射变更" className="surface-card space-y-2 p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-sm font-semibold text-foreground">未映射变更</h2>
        <Badge tone={unmapped.length === 0 ? "neutral" : "warning"}>{unmapped.length} 条</Badge>
      </div>
      {unmapped.length === 0 ? (
        <p className="text-[11px] text-muted-foreground">无未映射项</p>
      ) : (
        <ul className="space-y-1">
          {unmapped.map((item) => (
            <li key={`${item.path}:${item.reason}`} className="text-[11px]">
              <span className="block truncate font-mono text-foreground" title={item.path}>
                {item.path}
              </span>
              <span className="text-muted-foreground">
                {CODE_UNMAPPED_REASON_LABELS[item.reason] ?? item.reason}
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
