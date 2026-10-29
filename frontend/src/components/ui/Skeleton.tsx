import { cn } from "@/lib/utils"

export type SkeletonProps = {
  /** Number of placeholder bars rendered under the visible label. */
  lines?: number
  /** Screen-reader announcement; the bars themselves stay decorative. */
  label?: string
  className?: string
}

/**
 * Shared loading skeleton (spec §8.3). The visual bars are decorative; the
 * `role="status"` wrapper announces the loading context so a screen reader does
 * not fall back to an empty page while data is pending.
 */
export function Skeleton({ lines = 3, label = "加载中", className }: SkeletonProps) {
  return (
    <div
      role="status"
      aria-busy="true"
      data-testid="skeleton"
      className={cn("space-y-2 rounded-xl border border-dashed border-border p-4", className)}
    >
      <span className="sr-only">{label}</span>
      {Array.from({ length: Math.max(lines, 1) }).map((_, index) => (
        <span
          // biome-ignore lint/suspicious/noArrayIndexKey: the bars have no domain identity and the count is the only input.
          key={index}
          data-skeleton-bar=""
          aria-hidden="true"
          className={cn(
            "block h-3 animate-pulse rounded bg-muted",
            index === 0 ? "w-2/3" : index === Math.max(lines, 1) - 1 ? "w-1/2" : "w-full",
          )}
        />
      ))}
    </div>
  )
}
