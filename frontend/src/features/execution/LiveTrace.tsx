"use client"

import { useRef } from "react"

import { Badge } from "@/components/ui/Badge"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { formatDuration } from "@/lib/format"

import { ASSERTION_VERDICT_LABEL, CASE_RUN_STATUS_LABEL, STEP_STATUS_LABEL } from "./labels"
import type { CaseRun, CaseRunDetail, RunWave } from "./types"

export type LiveTraceProps = {
  wave: RunWave | null
  caseRun: CaseRun | null
  detail: CaseRunDetail | null
  loading: boolean
  error: unknown
  onRetry: () => void
}

function stepDuration(start: string | null, end: string | null): string {
  if (start === null || end === null) return "—"
  const startMs = Date.parse(start)
  const endMs = Date.parse(end)
  if (Number.isNaN(startMs) || Number.isNaN(endMs)) return "—"
  return formatDuration(endMs - startMs)
}

/**
 * Middle column of the execution workspace (spec §12.1): the selected
 * case-run trace in `step seq` order with target SUT chips, status, duration,
 * observed summary and nested assertion verdicts. SSE events only trigger a
 * snapshot refetch, so the scroll container keeps its position.
 */
export function LiveTrace({ wave, caseRun, detail, loading, error, onRetry }: LiveTraceProps) {
  const scrollRef = useRef<HTMLDivElement | null>(null)

  return (
    <section
      aria-label="实时轨迹"
      data-testid="live-trace"
      className="surface-card flex min-h-0 flex-col"
    >
      <header className="flex flex-wrap items-center gap-2 border-b border-border px-3 py-2">
        <h2 className="text-sm font-semibold text-foreground">实时轨迹</h2>
        {caseRun !== null ? (
          <>
            <span className="truncate text-xs text-foreground">{caseRun.title}</span>
            <Badge tone="neutral">{CASE_RUN_STATUS_LABEL[caseRun.status]}</Badge>
          </>
        ) : null}
      </header>

      <div ref={scrollRef} className="min-h-0 flex-1 overflow-y-auto p-3">
        {wave === null ? (
          <EmptyState
            title="确认用例后自动入队组波"
            description="执行是意图驱动的：确认 current+confirmed 用例后会自动生成执行意图并组波，无需点击“开始执行”。"
            className="border-none bg-transparent px-3 py-8"
          />
        ) : loading ? (
          <LoadingCard label="正在加载用例轨迹…" />
        ) : error !== null ? (
          <ErrorState error={error} title="用例轨迹加载失败" onRetry={onRetry} />
        ) : caseRun === null ? (
          <EmptyState
            title="选择用例查看轨迹"
            description="在右侧结果列表中选择一条用例，查看步骤、断言与证据的实时轨迹。"
            className="border-none bg-transparent px-3 py-8"
          />
        ) : detail === null ? (
          <EmptyState
            title="轨迹尚未生成"
            description="该用例还没有步骤结果。"
            className="border-none bg-transparent px-3 py-8"
          />
        ) : (
          <ol aria-label="步骤轨迹" className="space-y-2">
            {[...detail.steps]
              .sort((left, right) => left.seq - right.seq)
              .map((step) => {
                const assertions = detail.assertions.filter(
                  (assertion) => assertion.step_seq === step.seq,
                )
                const evidenceCount = detail.evidence.filter(
                  (evidence) => evidence.step_seq === step.seq,
                ).length
                return (
                  <li
                    key={step.seq}
                    data-testid={`trace-step-${step.seq}`}
                    className="rounded-lg border border-border bg-card px-2.5 py-2"
                  >
                    <div className="flex flex-wrap items-center gap-1.5 text-[11px]">
                      <span className="font-medium text-foreground">步骤 {step.seq}</span>
                      <Badge
                        tone={
                          step.status === "passed"
                            ? "success"
                            : step.status === "failed"
                              ? "danger"
                              : "neutral"
                        }
                      >
                        {STEP_STATUS_LABEL[step.status]}
                      </Badge>
                      {step.target_version_sut_id !== null ? (
                        <Badge tone="info">SUT {step.target_version_sut_id}</Badge>
                      ) : null}
                      {evidenceCount > 0 ? (
                        <span className="text-muted-foreground">证据 {evidenceCount}</span>
                      ) : null}
                      <span className="ml-auto text-muted-foreground">
                        {stepDuration(step.started_at, step.finished_at)}
                      </span>
                    </div>

                    {typeof step.observed.summary === "string" &&
                    step.observed.summary.length > 0 ? (
                      <p className="mt-1 text-[11px] text-muted-foreground">
                        {step.observed.summary}
                      </p>
                    ) : null}
                    {step.error.length > 0 ? (
                      <p role="alert" className="mt-1 text-[11px] text-destructive">
                        {step.error}
                      </p>
                    ) : null}

                    {assertions.length > 0 ? (
                      <ul className="mt-1.5 space-y-1">
                        {assertions.map((assertion) => (
                          <li
                            key={`${assertion.step_seq}-${assertion.assertion_seq}`}
                            className="flex flex-wrap items-center gap-1.5 text-[10px] text-muted-foreground"
                          >
                            <Badge tone={assertion.verdict === "passed" ? "success" : "danger"}>
                              {ASSERTION_VERDICT_LABEL[assertion.verdict]}
                            </Badge>
                            <span>
                              断言 {assertion.step_seq}.{assertion.assertion_seq}
                            </span>
                          </li>
                        ))}
                      </ul>
                    ) : null}
                  </li>
                )
              })}
          </ol>
        )}
      </div>
    </section>
  )
}
