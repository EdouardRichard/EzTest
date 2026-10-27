/** Human labels for the closed confidence set (spec §6.2). */
export const CONFIDENCE_LABELS: Record<string, string> = {
  high: "高置信",
  medium: "中置信",
  low: "低置信",
}

const CONFIDENCE_CLASSES: Record<string, string> = {
  high: "border-emerald-500/25 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300",
  medium: "border-sky-500/25 bg-sky-500/10 text-sky-700 dark:text-sky-300",
  low: "border-border bg-muted text-muted-foreground",
}

/**
 * Confidence pill shared by facts and impact candidates. The text always states
 * the confidence, so meaning never depends on colour alone (spec §9.4).
 */
export function ConfidenceBadge({ confidence }: { confidence: string }) {
  const label = CONFIDENCE_LABELS[confidence] ?? (confidence === "" ? "未知" : confidence)
  const classes = CONFIDENCE_CLASSES[confidence] ?? "border-border bg-muted text-muted-foreground"
  return (
    <span
      className={`inline-flex items-center rounded-full border px-2 py-0.5 text-[11px] font-medium ${classes}`}
    >
      {label}
    </span>
  )
}
