"use client"

import { AlertTriangle, ArrowRight, Loader2, Network } from "lucide-react"
import Link from "next/link"
import { useEffect, useMemo, useRef, useState } from "react"

import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"
import { getLatestAnalysis } from "@/features/code-intel/api"
import type { Analysis } from "@/features/code-intel/types"
import { useIssues } from "@/features/delivery/hooks/useIssues"
import { useReport, useReports } from "@/features/delivery/hooks/useReports"
import type {
  DeliveryStream,
  IssueSummary,
  ReportContentRequirement,
} from "@/features/delivery/types"
import { ApiError } from "@/lib/api-client"

import { getIncrementalPlan } from "./api"
import type { Version } from "./types"

/** Subset of the execution Batch the overview timeline renders. */
export type OverviewBatch = {
  id: string
  batch_no: number
  status: string
  /** P7 additive field (spec §7.7); P6 historical batches read as baseline. */
  kind?: string
  parentBatchId?: string | null
  code_baseline_commit?: string | null
  head_commit?: string | null
  counts: {
    total: number
    passed: number
    failed: number
    blocked: number
    inconclusive: number
    skipped: number
    interrupted: number
  }
  finished_at?: string | null
}

export type OverviewTabProps = {
  version: Version
  /** Batches of the version, newest first (as `useBatches` returns them). */
  batches?: OverviewBatch[]
  stream?: DeliveryStream
  onOpenIssues?: () => void
  onOpenCode?: () => void
  onOpenReports?: (batchId: string) => void
}

const BATCH_KIND_LABELS: Record<string, string> = {
  baseline: "基线",
  incremental: "增量",
  supplement: "补全",
}

const BATCH_KIND_VARIANTS: Record<string, StatusBadgeVariant> = {
  baseline: "neutral",
  incremental: "warning",
  supplement: "info",
}

const SEVERITY_ORDER = ["blocker", "major", "minor", "info"] as const

const SEVERITY_LABELS: Record<string, string> = {
  blocker: "阻塞",
  major: "严重",
  minor: "一般",
  info: "提示",
}

const SEVERITY_VARIANTS: Record<string, StatusBadgeVariant> = {
  blocker: "danger",
  major: "warning",
  minor: "info",
  info: "neutral",
}

/** P6 rows without `kind` are baseline batches (spec §7.7). */
function batchKind(batch: OverviewBatch): string {
  return batch.kind ?? "baseline"
}

function kindLabel(batch: OverviewBatch): string {
  return BATCH_KIND_LABELS[batchKind(batch)] ?? batchKind(batch)
}

function shortCommit(commit: string | null | undefined): string {
  if (commit === null || commit === undefined || commit === "") return "—"
  return commit.slice(0, 8)
}

function latestBatch(batches: OverviewBatch[]): OverviewBatch | null {
  let latest: OverviewBatch | null = null
  for (const batch of batches) {
    if (latest === null || batch.batch_no > latest.batch_no) latest = batch
  }
  return latest
}

function severityCounts(issues: IssueSummary[]): string {
  const counts = new Map<string, number>()
  for (const item of issues) counts.set(item.severity, (counts.get(item.severity) ?? 0) + 1)
  const parts: string[] = []
  for (const severity of SEVERITY_ORDER) {
    const count = counts.get(severity)
    if (count !== undefined) parts.push(`${severity} ${count}`)
  }
  return parts.length === 0 ? "无" : parts.join(" · ")
}

/** Cases that already reached a terminal state; the rest is truly not executed. */
function terminalCaseCount(counts: OverviewBatch["counts"]): number {
  return (
    counts.passed +
    counts.failed +
    counts.blocked +
    counts.inconclusive +
    counts.skipped +
    counts.interrupted
  )
}

function notExecutedCount(counts: OverviewBatch["counts"]): number {
  return Math.max(counts.total - terminalCaseCount(counts), 0)
}

function issueSeverityBadge(issue: IssueSummary): { label: string; variant: StatusBadgeVariant } {
  return {
    label: SEVERITY_LABELS[issue.severity] ?? issue.severity,
    variant: SEVERITY_VARIANTS[issue.severity] ?? "neutral",
  }
}

function describeAnalysisError(error: unknown): string {
  if (error instanceof ApiError && error.status === 404) return "暂无代码变更分析。"
  return "暂无代码变更分析。"
}

function coverageText(rows: ReportContentRequirement[]): {
  covered: number
  gap: number
  total: number
} {
  const covered = rows.filter((row) => row.caseCount > 0).length
  return { covered, gap: rows.length - covered, total: rows.length }
}

type MetricCardProps = {
  title: string
  value: string
  detail: string
}

function MetricCard({ title, value, detail }: MetricCardProps) {
  return (
    <article className="rounded-2xl border border-border bg-card p-4">
      <h2 className="text-sm font-medium">{title}</h2>
      <p className="mt-2 text-sm font-semibold tracking-tight">{value}</p>
      <p className="mt-1 text-xs text-muted-foreground">{detail}</p>
    </article>
  )
}

/** Inline read failure with the retry control every projection surface needs. */
function SectionError({
  message,
  onRetry,
  testId,
}: {
  message: string
  onRetry: () => void
  testId: string
}) {
  return (
    <div
      role="alert"
      data-testid={testId}
      className="flex flex-wrap items-center gap-2 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
    >
      <span>{message}</span>
      <button
        type="button"
        onClick={onRetry}
        className="rounded-lg border border-border bg-card px-2.5 py-1 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        重试
      </button>
    </div>
  )
}

function CoverageMatrix({
  rows,
  hasReport,
}: {
  rows: ReportContentRequirement[]
  hasReport: boolean
}) {
  const [expanded, setExpanded] = useState(false)
  if (rows.length === 0) {
    return (
      <p className="text-sm text-muted-foreground">
        {hasReport ? "报告中暂无需求覆盖数据。" : "暂无报告，生成批次报告后展示覆盖矩阵。"}
      </p>
    )
  }
  const visible = expanded ? rows : rows.slice(0, 6)
  return (
    <div className="space-y-2">
      <ul className="space-y-1">
        {visible.map((row) => (
          <li
            key={row.requirementId}
            className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-border px-2.5 py-1.5 text-xs"
          >
            <span className="break-all font-medium">{row.title}</span>
            <span className={row.caseCount === 0 ? "text-amber-700 dark:text-amber-300" : ""}>
              {row.caseCount === 0
                ? "0 用例 · 覆盖缺口"
                : `${row.caseCount} 用例 · ${row.passed} 通过 · ${row.failed} 失败`}
            </span>
          </li>
        ))}
      </ul>
      {rows.length > 6 ? (
        <button
          type="button"
          onClick={() => setExpanded((previous) => !previous)}
          className="text-xs font-medium text-primary"
        >
          {expanded ? "收起覆盖矩阵" : `展开全部（${rows.length} 条需求）`}
        </button>
      ) : null}
    </div>
  )
}

/**
 * Version overview (spec §10.2): the four metric cards, the report-derived
 * requirement coverage matrix, the batch timeline with kind labels, the open
 * issue top 5 and the code-change summary. Every section keeps its explicit
 * empty state; no metric is invented when the source data is absent.
 */
export function OverviewTab({
  version,
  batches = [],
  stream,
  onOpenIssues,
  onOpenCode,
  onOpenReports,
}: OverviewTabProps) {
  const openIssues = useIssues(version.id, { status: "open", limit: 100 }, stream)
  const confirmedIssues = useIssues(version.id, { status: "confirmed", limit: 100 }, stream)
  const reports = useReports(version.id, { limit: 1 }, stream)
  const latestReportId = reports.reports[0]?.id ?? ""
  const reportResource = useReport(version.id, latestReportId, stream)
  const report = reportResource.report

  const [analysis, setAnalysis] = useState<Analysis | null>(null)
  const [analysisError, setAnalysisError] = useState("")
  const [analysisLoading, setAnalysisLoading] = useState(false)
  const [affectedCaseCount, setAffectedCaseCount] = useState<number | null>(null)
  const analysisRequestRef = useRef(0)
  const planRequestRef = useRef(0)

  const repoId = version.codeRepositoryId
  useEffect(() => {
    const requestId = ++analysisRequestRef.current
    if (repoId === null) {
      setAnalysis(null)
      setAnalysisError("")
      setAnalysisLoading(false)
      return
    }
    setAnalysisLoading(true)
    setAnalysisError("")
    void getLatestAnalysis(version.id)
      .then((data) => {
        if (analysisRequestRef.current === requestId) setAnalysis(data)
      })
      .catch((caught: unknown) => {
        if (analysisRequestRef.current === requestId) {
          setAnalysis(null)
          setAnalysisError(describeAnalysisError(caught))
        }
      })
      .finally(() => {
        if (analysisRequestRef.current === requestId) setAnalysisLoading(false)
      })
    return () => {
      analysisRequestRef.current += 1
    }
  }, [version.id, repoId])

  useEffect(() => {
    const requestId = ++planRequestRef.current
    if (version.status !== "continuing") {
      setAffectedCaseCount(null)
      return
    }
    void getIncrementalPlan(version.id)
      .then((plan) => {
        if (planRequestRef.current === requestId) setAffectedCaseCount(plan.affectedCases.length)
      })
      .catch(() => {
        if (planRequestRef.current === requestId) setAffectedCaseCount(null)
      })
    return () => {
      planRequestRef.current += 1
    }
  }, [version.id, version.status])

  const latest = useMemo(() => latestBatch(batches), [batches])
  const timeline = useMemo(
    () => [...batches].sort((left, right) => left.batch_no - right.batch_no),
    [batches],
  )
  const requirements = report?.content.requirements ?? []
  const coverage = coverageText(requirements)
  const open = openIssues.issues
  const confirmed = confirmedIssues.issues
  const topIssues = open.slice(0, 5)

  const issuesLoading = openIssues.loading || confirmedIssues.loading
  const issuesError = openIssues.error !== "" ? openIssues.error : confirmedIssues.error
  const reportsLoading = reports.loading || reportResource.loading
  const reportsError = reports.error !== "" ? reports.error : reportResource.error
  const reconnecting =
    stream?.status === "reconnecting" ||
    openIssues.streamStatus === "reconnecting" ||
    confirmedIssues.streamStatus === "reconnecting" ||
    reports.streamStatus === "reconnecting" ||
    reportResource.streamStatus === "reconnecting"

  function retryIssues(): void {
    void openIssues.refresh()
    void confirmedIssues.refresh()
  }

  function retryReports(): void {
    void reports.refresh()
    if (latestReportId !== "") void reportResource.refresh()
  }

  const resultsText =
    latest === null
      ? "暂无执行结果"
      : `通过 ${latest.counts.passed} · 失败 ${latest.counts.failed} · 阻塞 ${
          latest.counts.blocked
        } · 未执行 ${notExecutedCount(latest.counts)}`

  const coverageValue =
    requirements.length === 0
      ? `已确认 ${version.clarifiedCount} / 已覆盖 — / 缺口 —`
      : `已确认 ${version.clarifiedCount} / 已覆盖 ${coverage.covered} / 缺口 ${coverage.gap}`

  const issuesValue =
    issuesError !== ""
      ? "—"
      : issuesLoading
        ? "加载中…"
        : `未解决 ${open.length} · 已确认 ${confirmed.length}`
  const issuesDetail =
    issuesError !== ""
      ? "问题加载失败，可重试。"
      : issuesLoading
        ? "正在加载问题…"
        : severityCounts([...open, ...confirmed])
  const coverageDetail =
    reportsError !== ""
      ? "报告加载失败，覆盖数据暂不可用。"
      : reportsLoading && report === null
        ? "正在加载报告覆盖数据…"
        : requirements.length === 0
          ? "覆盖数据来自最新批次报告。"
          : `报告覆盖 ${coverage.covered}/${coverage.total} 条需求`

  const currentBatch =
    latest === null
      ? "暂无批次"
      : `Batch ${latest.batch_no} · ${kindLabel(latest)} · ${shortCommit(
          latest.head_commit ?? latest.code_baseline_commit,
        )}`

  return (
    <div className="space-y-4">
      {reconnecting ? (
        <p
          role="status"
          data-testid="overview-stream-status"
          className="flex items-center gap-2 rounded-xl border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-amber-800 dark:text-amber-200"
        >
          <AlertTriangle className="h-4 w-4 shrink-0" aria-hidden="true" />
          实时连接重连中，问题与报告可能不是最新。
        </p>
      ) : null}

      <section
        aria-label="版本指标"
        data-testid="overview-metrics"
        className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4"
      >
        <MetricCard title="需求覆盖" value={coverageValue} detail={coverageDetail} />
        <MetricCard
          title="用例结果"
          value={resultsText}
          detail={latest === null ? "暂无批次" : `Batch ${latest.batch_no} 执行结果`}
        />
        <MetricCard title="问题" value={issuesValue} detail={issuesDetail} />
        <MetricCard
          title="当前批次"
          value={currentBatch}
          detail={latest === null ? "创建批次后展示执行摘要。" : `状态 ${latest.status}`}
        />
      </section>

      <section
        aria-label="需求覆盖矩阵"
        data-testid="overview-coverage-matrix"
        className="rounded-2xl border border-border bg-card p-4"
      >
        <h2 className="text-sm font-medium">需求 × 用例覆盖矩阵</h2>
        <div className="mt-2">
          {reportsError !== "" ? (
            <SectionError
              message={`加载报告失败：${reportsError}`}
              onRetry={retryReports}
              testId="overview-report-error"
            />
          ) : reportsLoading && report === null ? (
            <p className="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
              正在加载报告覆盖数据…
            </p>
          ) : (
            <CoverageMatrix rows={requirements} hasReport={report !== null} />
          )}
        </div>
      </section>

      <section
        aria-label="批次时间线"
        data-testid="overview-batch-timeline"
        className="rounded-2xl border border-border bg-card p-4"
      >
        <h2 className="text-sm font-medium">批次时间线</h2>
        {timeline.length === 0 ? (
          <p className="mt-2 text-sm text-muted-foreground">暂无批次</p>
        ) : (
          <ol className="mt-2 space-y-1.5">
            {timeline.map((batch) => (
              <li
                key={batch.id}
                className="flex flex-wrap items-center gap-2 rounded-lg border border-border px-2.5 py-1.5 text-xs"
              >
                <span className="font-medium">{`Batch ${batch.batch_no}`}</span>
                <StatusBadge variant={BATCH_KIND_VARIANTS[batchKind(batch)] ?? "neutral"}>
                  {kindLabel(batch)}
                </StatusBadge>
                <span className="text-muted-foreground">{batch.status}</span>
                <span className="font-mono text-muted-foreground">
                  {shortCommit(batch.head_commit ?? batch.code_baseline_commit)}
                </span>
                <span className="text-muted-foreground">
                  {`${batch.counts.passed} 通过 / ${batch.counts.failed} 失败 / ${batch.counts.blocked} 阻塞`}
                </span>
                <button
                  type="button"
                  onClick={() => onOpenReports?.(batch.id)}
                  className="ml-auto font-medium text-primary"
                >
                  {`查看 Batch ${batch.batch_no} 报告`}
                </button>
              </li>
            ))}
          </ol>
        )}
      </section>

      <div className="grid gap-3 lg:grid-cols-2">
        <section
          aria-label="问题 Top 5"
          data-testid="overview-issue-top"
          className="rounded-2xl border border-border bg-card p-4"
        >
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h2 className="text-sm font-medium">问题 Top 5</h2>
            <button
              type="button"
              onClick={() => onOpenIssues?.()}
              className="inline-flex items-center gap-1 text-xs font-medium text-primary"
            >
              查看全部问题
              <ArrowRight className="h-3 w-3" aria-hidden="true" />
            </button>
          </div>
          {issuesError !== "" ? (
            <div className="mt-2">
              <SectionError
                message={`加载问题失败：${issuesError}`}
                onRetry={retryIssues}
                testId="overview-issue-error"
              />
            </div>
          ) : issuesLoading && topIssues.length === 0 && confirmed.length === 0 ? (
            <p className="mt-2 flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
              正在加载问题…
            </p>
          ) : topIssues.length === 0 ? (
            <p className="mt-2 text-sm text-muted-foreground">暂无未解决问题。</p>
          ) : (
            <ol className="mt-2 space-y-1">
              {topIssues.map((item) => {
                const badge = issueSeverityBadge(item)
                return (
                  <li
                    key={item.id}
                    data-testid={`overview-issue-${item.id}`}
                    className="flex flex-wrap items-center gap-2 rounded-lg border border-border px-2.5 py-1.5 text-xs"
                  >
                    <StatusBadge variant={badge.variant}>{badge.label}</StatusBadge>
                    <span className="break-all font-medium">{item.title}</span>
                    <span className="ml-auto text-muted-foreground">{item.confidence}</span>
                  </li>
                )
              })}
            </ol>
          )}
          {report?.content.degraded === true ? (
            <p className="mt-2 flex items-center gap-1 text-xs text-amber-700 dark:text-amber-300">
              <AlertTriangle className="h-3.5 w-3.5" aria-hidden="true" />
              AI 分析不可用，问题可能不完整。
            </p>
          ) : null}
        </section>

        <section
          aria-label="代码变更摘要"
          data-testid="overview-code-summary"
          className="rounded-2xl border border-border bg-card p-4"
        >
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h2 className="text-sm font-medium">代码变更摘要</h2>
            <button
              type="button"
              onClick={() => onOpenCode?.()}
              className="inline-flex items-center gap-1 text-xs font-medium text-primary"
            >
              查看代码与变更
              <ArrowRight className="h-3 w-3" aria-hidden="true" />
            </button>
          </div>
          {repoId === null ? (
            <p className="mt-2 text-sm text-muted-foreground">未绑定代码仓库，暂无代码变更摘要。</p>
          ) : analysisLoading ? (
            <p className="mt-2 flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
              正在加载代码变更…
            </p>
          ) : analysis === null ? (
            <p className="mt-2 text-sm text-muted-foreground">{analysisError}</p>
          ) : (
            <p className="mt-2 text-sm">
              {`变更文件 ${analysis.stats?.changedFiles ?? 0} · 未映射 ${
                analysis.stats?.unmapped ?? analysis.unmapped.length
              } · 受影响用例 ${affectedCaseCount === null ? "—" : affectedCaseCount}`}
            </p>
          )}
          {analysis !== null && analysis.stats?.truncated === true ? (
            <p className="mt-1 text-xs text-muted-foreground">变更文件列表已截断。</p>
          ) : null}
        </section>
      </div>

      <Link
        href={`/versions/${version.id}/mindmap`}
        data-testid="overview-open-mindmap"
        className="inline-flex items-center gap-1.5 rounded-xl border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <Network className="h-4 w-4" aria-hidden="true" />
        用例脑图
      </Link>
    </div>
  )
}
