import { clsx } from "clsx"
import { AlertTriangle, BrainCircuit, Bug, FileText, KeyRound, type LucideIcon } from "lucide-react"

import type { FailureClass } from "../types"

/** Chinese labels of the v1 failure classes (spec §5.8). */
export const FAILURE_CLASS_LABELS: Record<FailureClass, string> = {
  application: "应用缺陷",
  environment: "环境问题",
  session: "会话问题",
  model: "模型/工具问题",
  case: "用例问题",
}

type FailureClassConfig = {
  icon: LucideIcon
  label: string
  className: string
}

/**
 * Colour + icon + text for every failure class. The text label is always
 * rendered, so classification never depends on colour alone (spec §9.5).
 */
const FAILURE_CLASS_CONFIG: Record<FailureClass, FailureClassConfig> = {
  application: {
    icon: Bug,
    label: FAILURE_CLASS_LABELS.application,
    className: "border-red-500/25 bg-red-500/10 text-red-700 dark:text-red-300",
  },
  environment: {
    icon: AlertTriangle,
    label: FAILURE_CLASS_LABELS.environment,
    className: "border-amber-500/25 bg-amber-500/10 text-amber-700 dark:text-amber-300",
  },
  session: {
    icon: KeyRound,
    label: FAILURE_CLASS_LABELS.session,
    className: "border-violet-500/25 bg-violet-500/10 text-violet-700 dark:text-violet-300",
  },
  model: {
    icon: BrainCircuit,
    label: FAILURE_CLASS_LABELS.model,
    className: "border-sky-500/25 bg-sky-500/10 text-sky-700 dark:text-sky-300",
  },
  case: {
    icon: FileText,
    label: FAILURE_CLASS_LABELS.case,
    className: "border-slate-500/25 bg-slate-500/10 text-slate-700 dark:text-slate-300",
  },
}

export type FailureClassBadgeProps = {
  /** `null` for terminal runs without a classification (passed/skipped/...). */
  failureClass: FailureClass | null
  className?: string
}

/**
 * Classification pill of the results table (spec §9.3). It renders an icon and
 * the Chinese label; only when there is no classification does it fall back to
 * an em dash instead of an empty coloured dot.
 */
export function FailureClassBadge({ failureClass, className }: FailureClassBadgeProps) {
  if (failureClass === null) {
    return (
      <span data-failure-class="none" className={clsx("text-xs text-muted-foreground", className)}>
        —
      </span>
    )
  }

  const config = FAILURE_CLASS_CONFIG[failureClass]
  const Icon = config.icon

  return (
    <span
      data-failure-class={failureClass}
      className={clsx(
        "inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-[11px] font-medium leading-none",
        config.className,
        className,
      )}
    >
      <Icon className="h-3 w-3" aria-hidden="true" />
      {config.label}
    </span>
  )
}
