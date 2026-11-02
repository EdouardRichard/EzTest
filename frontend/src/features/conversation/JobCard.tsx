"use client"

import { Ban, RotateCcw } from "lucide-react"
import { useEffect, useState } from "react"

import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { apiPost } from "@/lib/api-client"

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
}

const CANCELLABLE: ReadonlySet<JobStatus> = new Set<JobStatus>([
  "open",
  "claimed",
  "running",
  "waiting_human",
])

type JobViewResponse = {
  state?: JobStatus
  current?: number
  total?: number
}

function jobActionPath(versionId: string, jobId: string, action: "cancel" | "retry"): string {
  return `/api/versions/${encodeURIComponent(versionId)}/jobs/${encodeURIComponent(jobId)}/${action}`
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
 * percentage when `total = 0`), keeps the cancel action while the job is
 * active, and the retry action once it failed. The actions call the 0.2.1
 * `/jobs/{id}/cancel|retry` endpoints unless a parent supplies an override.
 */
export function JobCard({ versionId, part, onCancel, onRetry }: JobCardProps) {
  const [job, setJob] = useState<JobCardPart>(part)
  const [pending, setPending] = useState<"cancel" | "retry" | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)

  // The streamed snapshot (or a streamed job.progress merge) always wins.
  useEffect(() => {
    setJob(part)
  }, [part])

  const total = typeof job.total === "number" && job.total > 0 ? job.total : null
  const current = typeof job.current === "number" && job.current >= 0 ? job.current : 0
  const percent = total !== null ? Math.min(100, Math.round((current / total) * 100)) : null

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
        setJob((currentJob) => mergeJobView(currentJob, view))
        return
      }
      if (onRetry !== undefined) {
        await onRetry(job.job_id)
        return
      }
      const view = await apiPost<JobViewResponse>(jobActionPath(versionId, job.job_id, "retry"))
      setJob((currentJob) => mergeJobView(currentJob, view))
    } catch (cause) {
      setActionError(cause instanceof Error ? cause.message : "作业操作失败，请重试。")
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

      {CANCELLABLE.has(job.status) || job.status === "failed" ? (
        <div className="mt-1.5 flex items-center gap-1.5">
          {CANCELLABLE.has(job.status) ? (
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
          {job.status === "failed" ? (
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
