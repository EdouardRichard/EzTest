"use client"

import { Clock3, UserRoundCheck } from "lucide-react"

import { StatusBadge } from "@/components/ui/StatusBadge"

import type { PendingTakeover } from "../types"

/** Stable `pending_takeover.reason` labels (spec §5.9, §6.4). */
const REASON_LABELS: Record<string, string> = {
  mfa: "多因素认证（MFA）",
  captcha: "验证码",
  approval: "人工审批",
  login_unknown: "未知登录问题",
  other: "其他原因",
}

export type HumanGateCardProps = {
  /** `caseRun.pending_takeover`; the card renders nothing when it is absent. */
  takeover: PendingTakeover
  /** Human-readable case title used to identify the blocked run. */
  caseTitle?: string
  /** Current user role; only an admin gets the completion entry (spec §5.11). */
  role?: string
  /** Opens the existing takeover dialog through the parent container. */
  onComplete?: (takeover: PendingTakeover) => void
}

/** Human-readable instruction text from the free-form `instructions` object. */
function readInstructions(instructions: Record<string, unknown>): string {
  const text = instructions.text
  if (typeof text === "string" && text.trim() !== "") return text
  const entries = Object.entries(instructions)
  if (entries.length === 0) return ""
  return entries
    .map(([key, value]) => {
      const rendered =
        typeof value === "string" ? value : JSON.stringify(value, undefined, 2) || String(value)
      return `${key}: ${rendered}`
    })
    .join("\n")
}

/** Localized expiry; an unparsable value stays visible rather than becoming a dash. */
function formatExpiry(value: string): string {
  if (value.trim() === "") return "未设置"
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString("zh-CN", { hour12: false })
}

/**
 * Human-takeover card of the execution Sessions tab (spec §9.3). It shows why
 * the run is blocked and who owns the account, and only an admin can open the
 * completion flow. The takeover ID stays a decimal string: the existing
 * `TakeoverDialog` is opened through `onComplete`, never by narrowing the ID.
 */
export function HumanGateCard({ takeover, caseTitle, role, onComplete }: HumanGateCardProps) {
  const reasonLabel = REASON_LABELS[takeover.reason] ?? takeover.reason
  const instructions = readInstructions(takeover.instructions)
  const isAdmin = role === "admin"
  const account = takeover.account?.trim() || "未提供"
  const system = takeover.system?.trim() || "未提供"

  return (
    <article
      data-testid="human-gate-card"
      aria-label="需要人工接管"
      className="space-y-3 rounded-2xl border border-amber-500/40 bg-amber-500/10 p-4 text-sm"
    >
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="flex min-w-0 items-start gap-2">
          <span className="mt-0.5 grid h-8 w-8 shrink-0 place-items-center rounded-xl bg-amber-500/20 text-amber-700 dark:text-amber-200">
            <UserRoundCheck className="h-4 w-4" aria-hidden="true" />
          </span>
          <div className="min-w-0">
            <h3 className="text-sm font-semibold">需要人工接管</h3>
            {caseTitle ? (
              <p className="mt-0.5 truncate text-xs text-muted-foreground">用例：{caseTitle}</p>
            ) : null}
          </div>
        </div>
        <StatusBadge variant="warning">{reasonLabel}</StatusBadge>
      </div>

      <dl className="grid gap-2 sm:grid-cols-3">
        <div className="min-w-0">
          <dt className="text-[11px] font-medium text-muted-foreground">账号</dt>
          <dd className="mt-0.5 break-all font-mono text-xs">{account}</dd>
        </div>
        <div className="min-w-0">
          <dt className="text-[11px] font-medium text-muted-foreground">系统</dt>
          <dd className="mt-0.5 break-all text-xs">{system}</dd>
        </div>
        <div className="min-w-0">
          <dt className="text-[11px] font-medium text-muted-foreground">到期时间</dt>
          <dd className="mt-0.5 flex items-center gap-1 text-xs">
            <Clock3 className="h-3 w-3 shrink-0" aria-hidden="true" />
            {formatExpiry(takeover.expires_at)}
          </dd>
        </div>
      </dl>

      {instructions !== "" ? (
        <div>
          <h4 className="text-[11px] font-medium text-muted-foreground">操作指引</h4>
          <p className="mt-1 whitespace-pre-wrap break-words text-xs leading-relaxed">
            {instructions}
          </p>
        </div>
      ) : null}

      {isAdmin ? (
        <div className="flex justify-end">
          <button
            type="button"
            data-testid="human-gate-complete"
            onClick={() => onComplete?.(takeover)}
            className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            完成接管
          </button>
        </div>
      ) : (
        <p className="text-xs text-muted-foreground">仅管理员可完成接管</p>
      )}
    </article>
  )
}
