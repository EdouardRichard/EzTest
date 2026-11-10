"use client"

import { Ban, RotateCcw } from "lucide-react"
import { useEffect, useState } from "react"

import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { ApiError, apiPost } from "@/lib/api-client"

import type { JobCardPart, JobKind, JobStatus } from "./types"

export type JobCardProps = {
  versionId: string
  part: JobCardPart
  /** Overrides the default cancel endpoint call (used by parents/tests). */
  onCancel?: (jobId: string) => void | Promise<void>
  /** Overrides the default retry endpoint call (used by parents/tests). */
  onRetry?: (jobId: string) => void | Promise<void>
}

const STATUS_LABEL: Record<JobStatus, string> = {
  open: "排队中",
  claimed: "已领取",
  running: "运行中",
  waiting_human: "等待人工",
  done: "完成",
  failed: "失败",
  cancelled: "已取消",
}

const STATUS_TONE: Record<JobStatus, BadgeTone> = {
  open: "neutral",
  claimed: "info",
  running: "info",
  waiting_human: "warning",
  done: "success",
  failed: "danger",
  cancelled: "neutral",
}

const KIND_LABEL: Partial<Record<JobKind, string>> = {
  "explore.login": "探索登录",
  "explore.page": "探索页面",
  "explore.api": "探索接口",
  "explore.coverage": "覆盖分析",
  "explore.gap": "探索缺口",
  "design.cases": "设计用例",
  "review.cases": "评审用例",
  "execute.batch": "批量执行",
  "analyze.failure": "失败分析",
  "report.version": "生成报告",
  "knowledge.propose": "沉淀知识",
  "chat.turn": "对话回复",
  "requirement.curate": "需求整理",
  "design.round": "设计轮次",
}

// The job control API cancels only open/claimed/running jobs. `waiting_human`
// is intentionally excluded: the API rejects it with a 409, so the card must
// not offer a button that is guaranteed to fail.
const CANCELLABLE: ReadonlySet<JobStatus> = new Set<JobStatus>(["open", "claimed", "running"])

/**
 * Chinese copy for a typed job failure code. The backend keeps the stable code
 * and (for older cards) an English `reason`; the UI explains every known code
 * in the user's language and keeps the raw cause in the tooltip (D-04).
 */
const FAILURE_REASON: Record<string, string> = {
  invalid_work_type:
    "旧版设计轮次作业已停用，该作业不会影响已生成用例，也无需重试；请让 AI 重新执行设计以生成用例。",
  llm_capability_mismatch:
    "所选模型不支持图片输入，请在「设置 → LLM 配置」中改选支持视觉的模型后重试。",
}

/**
 * Terminal failures whose retry is guaranteed to fail again. The retry control
 * is hidden instead of offering a button that always ends in another red card.
 */
const NOT_RETRYABLE_CODES: ReadonlySet<string> = new Set(["invalid_work_type"])

type FailurePresentation = {
  text: string
  /** Raw reason/code for support, never the primary copy. */
  title?: string
  /** Unknown codes keep showing the code inline next to the original reason. */
  showCode: boolean
}

function failurePresentation(job: JobCardPart): FailurePresentation | null {
  const reason = (job.reason ?? "").trim()
  if (job.status !== "failed" || reason === "") return null
  const code = (job.code ?? "").trim()
  const localized = code === "" ? undefined : FAILURE_REASON[code]
  if (localized !== undefined) {
    return { text: localized, title: `${reason}（${code}）`, showCode: false }
  }
  return { text: reason, title: code === "" ? undefined : code, showCode: code !== "" }
}

type JobViewResponse = {
  state?: JobStatus
  current?: number
  total?: number
}

function jobActionPath(versionId: string, jobId: string, action: "cancel" | "retry"): string {
  return `/api/versions/${encodeURIComponent(versionId)}/jobs/${encodeURIComponent(jobId)}/${action}`
}

/**
 * User-facing text for a rejected cancel/retry. The card cannot know the job's
 * attempt budget (`job_card` carries no attempts/max_attempts), so a retry on a
 * failed-but-exhausted job is only guaranteed to be discovered from the typed
 * 409; translating that status keeps the error actionable instead of showing
 * the raw API sentence.
 */
function jobActionError(cause: unknown, action: "cancel" | "retry"): string {
  if (cause instanceof ApiError) {
    if (cause.status === 409) {
      return action === "cancel"
        ? "作业状态已变化，无法取消；请刷新后重试。"
        : "该作业当前不可重试（可能已取消或尝试次数已用尽）；请刷新后重试。"
    }
    if (cause.status === 404) return "作业不存在或已失效；请刷新后重试。"
    return cause.message
  }
  return "作业操作失败，请重试。"
}

function mergeJobView(part: JobCardPart, view: JobViewResponse): JobCardPart {
  return {
    ...part,
    status: view.state ?? part.status,
    current: typeof view.current === "number" ? view.current : part.current,
    total: typeof view.total === "number" ? view.total : part.total,
  }
}

/**
 * One job progress card (spec §9.1). It shows current/total (never a fabricated
 * percentage when `total = 0`), offers cancel only for the states the job
 * control API accepts (open/claimed/running — `waiting_human` is rejected), and
 * retry only once the job failed (a cancelled job is never retryable). The
 * actions call the 0.2.1 `/jobs/{id}/cancel|retry` endpoints unless a parent
 * supplies an override; a typed 409 is surfaced as an actionable message
 * because the card itself carries no attempt budget.
 */
export function JobCard({ versionId, part, onCancel, onRetry }: JobCardProps) {
  // Only the local result of cancel/retry lives here; the streamed snapshot is
  // always the base so job.progress updates keep flowing.
  const [actionResult, setActionResult] = useState<JobCardPart | null>(null)
  const [pending, setPending] = useState<"cancel" | "retry" | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)

  // A streamed job id/status change owns the card again. A parent re-render (or
  // a progress-only update) with the same id/status must not restore the stale
  // running snapshot over a just-completed cancel/retry.
  // biome-ignore lint/correctness/useExhaustiveDependencies: job id/status are the reset triggers; progress-only updates must not reset
  useEffect(() => {
    setActionResult(null)
    setActionError(null)
  }, [part.job_id, part.status])

  const job = actionResult === null ? part : { ...part, ...actionResult }

  const total = typeof job.total === "number" && job.total > 0 ? job.total : null
  const current = typeof job.current === "number" && job.current >= 0 ? job.current : 0
  const percent = total !== null ? Math.min(100, Math.round((current / total) * 100)) : null
  const failure = failurePresentation(job)
  const showCancel = CANCELLABLE.has(job.status)
  const showRetry =
    job.status === "failed" && !(job.code !== undefined && NOT_RETRYABLE_CODES.has(job.code))

  async function runAction(action: "cancel" | "retry"): Promise<void> {
    setPending(action)
    setActionError(null)
    try {
      if (action === "cancel") {
        if (onCancel !== undefined) {
          await onCancel(job.job_id)
          return
        }
        const view = await apiPost<JobViewResponse>(
          jobActionPath(versionId, job.job_id, "cancel"),
          {},
        )
        setActionResult((current) => mergeJobView(current ?? part, view))
        return
      }
      if (onRetry !== undefined) {
        await onRetry(job.job_id)
        return
      }
      const view = await apiPost<JobViewResponse>(jobActionPath(versionId, job.job_id, "retry"))
      setActionResult((current) => mergeJobView(current ?? part, view))
    } catch (cause) {
      setActionError(jobActionError(cause, action))
    } finally {
      setPending(null)
    }
  }

  return (
    <div
      data-testid={`job-card-${job.job_id}`}
      className="rounded-lg border border-border/70 bg-background/70 px-3 py-2"
    >
      <div className="flex flex-wrap items-center gap-1.5">
        <span className="text-[11px] font-semibold text-foreground">
          {KIND_LABEL[job.kind] ?? job.kind}
        </span>
        <Badge tone={STATUS_TONE[job.status]}>{STATUS_LABEL[job.status]}</Badge>
        <span className="ml-auto text-[10px] text-muted-foreground">作业 #{job.job_id}</span>
      </div>

      {total !== null && percent !== null ? (
        <div className="mt-1.5 space-y-1">
          <div className="flex items-center justify-between text-[10px] text-muted-foreground">
            <span>
              {current}/{total}
            </span>
            <span>{percent}%</span>
          </div>
          <div
            data-testid="job-progress"
            role="progressbar"
            aria-label="作业进度"
            aria-valuemin={0}
            aria-valuemax={total}
            aria-valuenow={current}
            className="h-1.5 overflow-hidden rounded-full bg-muted"
          >
            <div className="h-full rounded-full bg-primary" style={{ width: `${percent}%` }} />
          </div>
        </div>
      ) : (
        <p className="mt-1 text-[10px] text-muted-foreground">已处理 {current} 步</p>
      )}

      {failure !== null ? (
        <p
          data-testid="job-card-reason"
          role="status"
          title={failure.title}
          className="mt-1 text-[10px] text-destructive"
        >
          <span className="font-medium">失败原因：</span>
          {failure.text}
          {failure.showCode ? (
            <span className="ml-1 text-muted-foreground">（{job.code}）</span>
          ) : null}
        </p>
      ) : null}

      {showCancel || showRetry ? (
        <div className="mt-1.5 flex items-center gap-1.5">
          {showCancel ? (
            <Button
              variant="secondary"
              size="sm"
              disabled={pending !== null}
              onClick={() => void runAction("cancel")}
            >
              <Ban className="size-3" aria-hidden="true" />
              取消作业
            </Button>
          ) : null}
          {showRetry ? (
            <Button
              variant="secondary"
              size="sm"
              disabled={pending !== null}
              onClick={() => void runAction("retry")}
            >
              <RotateCcw className="size-3" aria-hidden="true" />
              重试作业
            </Button>
          ) : null}
        </div>
      ) : null}

      {actionError !== null ? (
        <p role="alert" className="mt-1 text-[10px] text-destructive">
          {actionError}
        </p>
      ) : null}
    </div>
  )
}
