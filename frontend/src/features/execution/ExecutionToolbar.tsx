"use client"

import { useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { InlineError } from "@/components/ui/ErrorState"
import { Select } from "@/components/ui/Select"
import { SSE_STATUS_LABEL } from "@/features/clarification/labels"
import { formatDateTime } from "@/lib/format"
import type { SSEStatus } from "@/lib/sse-client"

import {
  EXECUTOR_STATUS_LABEL,
  EXECUTOR_STATUS_TONE,
  WAVE_STATUS_LABEL,
  WAVE_STATUS_TONE,
  WAVE_TRIGGER_LABEL,
} from "./labels"
import type { ExecutorStatusView, RunWave } from "./types"

export type ExecutionToolbarProps = {
  wave: RunWave | null
  sseStatus: SSEStatus
  executor: ExecutorStatusView | null
  sutOptions: string[]
  sutFilter: string
  onSutFilterChange: (value: string) => void
  onPause: () => void
  onResume: () => void
  onCancel: () => void
  busy?: boolean
  actionError?: unknown
  notice?: string | null
}

const COUNT_FIELDS: { key: keyof RunWave["counts"]; label: string }[] = [
  { key: "passed", label: "通过" },
  { key: "failed", label: "失败" },
  { key: "blocked", label: "阻塞" },
  { key: "paused", label: "暂停" },
  { key: "pending", label: "待执行" },
]

/**
 * Top toolbar of the execution workspace (spec §12.1): active-wave header,
 * counts badges, pause/resume/cancel controls, SUT filter, the reused SSE
 * badge and the executor status warning. Pause defaults to graceful and lives
 * in `PauseDialog`; cancel asks for an inline second confirmation.
 */
export function ExecutionToolbar({
  wave,
  sseStatus,
  executor,
  sutOptions,
  sutFilter,
  onSutFilterChange,
  onPause,
  onResume,
  onCancel,
  busy = false,
  actionError,
  notice,
}: ExecutionToolbarProps) {
  const [confirmingCancel, setConfirmingCancel] = useState(false)
  const cancellable =
    wave !== null &&
    (wave.status === "planned" || wave.status === "running" || wave.status === "paused")

  return (
    <section
      aria-label="执行工具栏"
      data-testid="execution-toolbar"
      className="surface-card space-y-2 px-3 py-2.5"
    >
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="text-sm font-semibold text-foreground">
          {wave === null ? "暂无活跃波次" : `波次 #${wave.id}`}
        </h2>
        {wave !== null ? (
          <>
            <Badge tone={WAVE_STATUS_TONE[wave.status]}>{WAVE_STATUS_LABEL[wave.status]}</Badge>
            <span className="text-[11px] text-muted-foreground">
              {WAVE_TRIGGER_LABEL[wave.trigger]}触发 ·{" "}
              {formatDateTime(wave.started_at ?? wave.created_at)}
            </span>
          </>
        ) : null}

        <span className="ml-auto flex flex-wrap items-center gap-1.5">
          {wave !== null && (wave.status === "planned" || wave.status === "running") ? (
            <Button size="sm" variant="secondary" disabled={busy} onClick={onPause}>
              暂停
            </Button>
          ) : null}
          {wave !== null && wave.status === "paused" ? (
            <Button size="sm" disabled={busy} onClick={onResume}>
              恢复
            </Button>
          ) : null}
          {cancellable ? (
            confirmingCancel ? (
              <>
                <span className="text-[11px] text-muted-foreground">确认取消？</span>
                <Button
                  size="sm"
                  variant="danger"
                  disabled={busy}
                  onClick={() => {
                    setConfirmingCancel(false)
                    onCancel()
                  }}
                >
                  确认取消
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  disabled={busy}
                  onClick={() => setConfirmingCancel(false)}
                >
                  返回
                </Button>
              </>
            ) : (
              <Button
                size="sm"
                variant="secondary"
                disabled={busy}
                onClick={() => setConfirmingCancel(true)}
              >
                取消波次
              </Button>
            )
          ) : null}
        </span>
      </div>

      {wave !== null ? (
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="text-[11px] text-muted-foreground">共 {wave.counts.total} 条</span>
          {COUNT_FIELDS.map((field) => (
            <Badge
              key={field.key}
              tone={
                field.key === "passed"
                  ? "success"
                  : field.key === "failed"
                    ? "danger"
                    : field.key === "blocked" || field.key === "paused"
                      ? "warning"
                      : "neutral"
              }
            >
              {field.label} {String(wave.counts[field.key])}
            </Badge>
          ))}
        </div>
      ) : null}

      <div className="flex flex-wrap items-center gap-2">
        <label htmlFor="execution-sut-filter" className="text-[11px] text-muted-foreground">
          SUT 过滤
        </label>
        <Select
          id="execution-sut-filter"
          aria-label="SUT 过滤"
          value={sutFilter}
          className="h-7 w-auto text-[11px]"
          onChange={(event) => onSutFilterChange(event.target.value)}
        >
          <option value="all">全部 SUT</option>
          {sutOptions.map((versionSutId) => (
            <option key={versionSutId} value={versionSutId}>
              SUT {versionSutId}
            </option>
          ))}
        </Select>

        <Badge
          tone={sseStatus === "live" ? "success" : sseStatus === "closed" ? "neutral" : "warning"}
        >
          {SSE_STATUS_LABEL[sseStatus]}
        </Badge>

        {executor === null ? (
          <Badge tone="neutral">执行器未注册</Badge>
        ) : (
          <>
            <Badge tone={EXECUTOR_STATUS_TONE[executor.status]}>
              执行器{EXECUTOR_STATUS_LABEL[executor.status]}
            </Badge>
            {executor.status === "offline" ? (
              <span className="text-[11px] text-destructive">等待执行器恢复</span>
            ) : null}
            <span className="text-[10px] text-muted-foreground">
              上下文 {executor.active_contexts}/{executor.capacity}
            </span>
          </>
        )}
      </div>

      {notice !== null && notice !== undefined ? (
        <p role="status" className="text-[11px] text-warning">
          {notice}
        </p>
      ) : null}
      {actionError !== null && actionError !== undefined ? (
        <InlineError error={actionError} />
      ) : null}
    </section>
  )
}
