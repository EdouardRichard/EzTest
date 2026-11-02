"use client"

import { Badge } from "@/components/ui/Badge"
import { formatDateTime } from "@/lib/format"

import { WAVE_STATUS_LABEL, WAVE_STATUS_TONE } from "./labels"
import type { RunWave } from "./types"

export type WaveListProps = {
  waves: RunWave[]
  selectedWaveId: string | null
  /** The one non-terminal wave for this version (spec §3.4 invariant 2). */
  activeWaveId: string | null
  onSelect: (waveId: string) => void
}

/**
 * Left column of the execution workspace (spec §12.1): wave history newest
 * first, active wave highlighted. Selecting an item switches `?wave=`.
 */
export function WaveList({ waves, selectedWaveId, activeWaveId, onSelect }: WaveListProps) {
  return (
    <section
      aria-label="波次历史"
      data-testid="wave-list"
      className="surface-card flex min-h-0 flex-col"
    >
      <header className="flex items-center justify-between border-b border-border px-3 py-2">
        <h2 className="text-sm font-semibold text-foreground">波次</h2>
        <span className="text-[11px] text-muted-foreground">{waves.length} 个波次</span>
      </header>

      {waves.length === 0 ? (
        <div className="space-y-1 px-3 py-6 text-center">
          <p className="text-xs font-semibold text-foreground">暂无波次</p>
          <p className="text-[11px] text-muted-foreground">确认用例后会自动组波，无需手动开始。</p>
        </div>
      ) : (
        <ol aria-label="波次列表" className="min-h-0 flex-1 space-y-1.5 overflow-y-auto p-2">
          {waves.map((wave) => {
            const selected = wave.id === selectedWaveId
            const active = wave.id === activeWaveId
            return (
              <li key={wave.id}>
                <button
                  type="button"
                  aria-current={selected ? "true" : undefined}
                  data-active={active ? "true" : "false"}
                  onClick={() => onSelect(wave.id)}
                  className={
                    "w-full rounded-lg border px-2.5 py-2 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring " +
                    (selected
                      ? "border-primary/40 bg-accent/60"
                      : "border-border bg-card hover:bg-muted/60")
                  }
                >
                  <span className="flex flex-wrap items-center gap-1.5">
                    <span className="text-xs font-semibold text-foreground">#{wave.id}</span>
                    <Badge tone={WAVE_STATUS_TONE[wave.status]}>
                      {WAVE_STATUS_LABEL[wave.status]}
                    </Badge>
                    {active ? <Badge tone="info">活跃</Badge> : null}
                  </span>
                  <span className="mt-1 flex flex-wrap items-center gap-2 text-[10px] text-muted-foreground">
                    <span>{formatDateTime(wave.started_at ?? wave.created_at)}</span>
                    <span>
                      {wave.counts.passed}/{wave.counts.total}
                    </span>
                    <span>失败 {wave.counts.failed}</span>
                  </span>
                </button>
              </li>
            )
          })}
        </ol>
      )}
    </section>
  )
}
