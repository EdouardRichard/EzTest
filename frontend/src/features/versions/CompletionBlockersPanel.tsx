"use client"

import Link from "next/link"
import { useEffect, useState } from "react"

import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { InlineError } from "@/components/ui/ErrorState"
import { listRequirements } from "@/features/clarification/api"
import type { Requirement } from "@/features/clarification/types"
import { listReports } from "@/features/delivery/api"
import { REPORT_PDF_STATUS_LABELS, type ReportView } from "@/features/delivery/types"
import type { VersionStage } from "@/features/design/types"

import type { Version } from "./types"

/** How many unclosed requirements the panel lists per status bucket. */
const UNCLOSED_FETCH_LIMIT = 100

const UNCLOSED_STATUS_LABELS: Record<"draft" | "clarified", string> = {
  draft: "草案",
  clarified: "已澄清",
}

export type CompletionBlockersPanelProps = {
  /** Version projection; `requirement_counts` drives the closed/total row. */
  version: Version
  /** Effective stage (`stage.stage`); the panel only renders while reporting. */
  stage?: VersionStage | null
  /** Pending structured questions of the version (`pendingQuestions.length`). */
  pendingQuestions?: number
  /** Pending human decisions of the version (`pendingDecisions.length`). */
  pendingDecisions?: number
  /** SSE refresh nonce (`${version.updated_at}|${reportRevision ?? ""}`). */
  refreshKey?: string
}

type ReportState = {
  report: ReportView | null
  loading: boolean
  error: unknown
}

type RequirementsState = {
  items: Requirement[]
  loading: boolean
  error: unknown
}

function isUnclosedStatus(status: Requirement["status"]): status is "draft" | "clarified" {
  return status === "draft" || status === "clarified"
}

function badRequestReason(result: PromiseSettledResult<unknown>): unknown {
  return result.status === "rejected" ? result.reason : null
}

/**
 * R3-E-01: explicit completion conditions while a running version sits in the
 * `reporting` stage. The stage badge alone does not tell the operator which
 * gate is still open; this panel names the report state, the unclosed
 * requirements and the next automatic step.
 *
 * The requirement counts come from the version projection (`requirement_counts`),
 * so a failed list/report request degrades to an inline error and never hides
 * the panel or blocks the page.
 */
export function CompletionBlockersPanel({
  version,
  stage = null,
  pendingQuestions = 0,
  pendingDecisions = 0,
  refreshKey = "",
}: CompletionBlockersPanelProps) {
  const visible = stage === "reporting" && version.status === "running"
  const versionId = version.id

  const [requirements, setRequirements] = useState<RequirementsState>({
    items: [],
    loading: true,
    error: null,
  })
  const [reportState, setReportState] = useState<ReportState>({
    report: null,
    loading: true,
    error: null,
  })

  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshKey is the SSE refetch nonce.
  useEffect(() => {
    if (!visible) return
    let cancelled = false
    setRequirements({ items: [], loading: true, error: null })
    setReportState({ report: null, loading: true, error: null })

    void Promise.allSettled([
      listRequirements(versionId, { status: "draft", limit: UNCLOSED_FETCH_LIMIT }),
      listRequirements(versionId, { status: "clarified", limit: UNCLOSED_FETCH_LIMIT }),
      listReports(versionId, { kind: "version", limit: 20 }),
    ]).then(([draft, clarified, reports]) => {
      if (cancelled) return

      const items: Requirement[] = []
      if (draft.status === "fulfilled") items.push(...draft.value)
      if (clarified.status === "fulfilled") items.push(...clarified.value)
      setRequirements({
        items: items.filter((requirement) => isUnclosedStatus(requirement.status)),
        loading: false,
        error: badRequestReason(draft) ?? badRequestReason(clarified),
      })

      if (reports.status === "fulfilled") {
        const versionReport = reports.value.find((item) => item.kind === "version") ?? null
        setReportState({ report: versionReport, loading: false, error: null })
      } else {
        setReportState({ report: null, loading: false, error: reports.reason })
      }
    })

    return () => {
      cancelled = true
    }
  }, [versionId, visible, refreshKey])

  if (!visible) return null

  const counts = version.requirement_counts
  const closedCount = counts?.closed ?? 0
  const totalCount = counts?.total ?? 0
  // Counts are authoritative for both the readiness badge and the summary row;
  // the fetched list only supplies titles. Unknown counts fall back to the list.
  const unclosedCount = counts === undefined ? null : counts.draft + counts.clarified
  const fetchedUnclosed = requirements.items.length
  const missingUnclosed = unclosedCount === null ? 0 : Math.max(0, unclosedCount - fetchedUnclosed)
  const allClosed =
    counts === undefined
      ? !requirements.loading && requirements.error === null && fetchedUnclosed === 0
      : counts.draft === 0 && counts.clarified === 0

  const reportStatus = reportState.report?.status ?? null
  const reportPublished = reportStatus === "published"
  const reportRevision = reportState.report?.current_revision?.revision
  const reportPDFStatus = reportState.report?.current_revision?.pdf_status

  let reportLine: string
  if (reportState.loading) {
    reportLine = "正在读取报告状态…"
  } else if (reportState.error !== null) {
    reportLine = "状态未知（报告请求失败）"
  } else if (reportPublished) {
    const pdfLabel =
      reportPDFStatus === undefined ? "PDF 状态未知" : REPORT_PDF_STATUS_LABELS[reportPDFStatus]
    reportLine = `已发布（版本总结 v${reportRevision ?? "?"}，${pdfLabel}）`
  } else if (reportStatus === "stale") {
    reportLine = "已过期（需重新发布）"
  } else {
    reportLine = "未发布"
  }

  let readiness: { label: string; tone: BadgeTone }
  if (!reportPublished) {
    readiness = { label: "等待报告发布", tone: "warning" }
  } else if (allClosed) {
    readiness = { label: "即将完成", tone: "success" }
  } else {
    readiness = { label: "等待需求闭环", tone: "warning" }
  }

  let nextStep: string
  if (!reportPublished) {
    nextStep = "报告生成并发布后，版本会自动进入完成。"
  } else if (!allClosed) {
    nextStep =
      "版本完成要求全部需求闭环；请先处理上述需求（复测/重开/闭环），完成后系统会自动完成版本。"
  } else {
    nextStep = "全部需求已闭环、报告已发布；正在等待执行/计划项收尾，通常 1 分钟内会自动完成版本。"
  }

  const encodedVersionId = encodeURIComponent(versionId)
  const requirementsHref = `/versions/${encodedVersionId}?tab=requirements`
  const overviewHref = `/versions/${encodedVersionId}?tab=overview`
  const linkClassName = "text-[11px] text-primary hover:underline"

  return (
    <section
      data-testid="completion-readiness"
      aria-label="完成条件"
      className="surface-card space-y-2 p-3"
    >
      <header className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-wrap items-center gap-1.5">
          <h3 className="text-sm font-semibold text-foreground">完成条件</h3>
          <Badge tone={readiness.tone}>{readiness.label}</Badge>
        </div>
        <span className="text-[11px] text-muted-foreground">版本完成前必须满足</span>
      </header>

      <div className="space-y-1.5 text-xs">
        <div className="flex flex-wrap items-baseline gap-1.5">
          <span className="text-[11px] font-medium text-muted-foreground">版本总结报告</span>
          <span data-testid="completion-report" className="text-foreground">
            {reportLine}
          </span>
        </div>

        <div data-testid="completion-requirements" className="space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-medium text-foreground">
              {counts === undefined ? "需求闭环状态未知" : `已闭环 ${closedCount}/${totalCount}`}
            </span>
            {(unclosedCount ?? fetchedUnclosed) > 0 ? (
              <Link href={requirementsHref} className={linkClassName}>
                去需求与澄清 →
              </Link>
            ) : null}
          </div>

          {requirements.items.length > 0 ? (
            <ul aria-label="未闭环需求" className="space-y-1">
              {requirements.items.map((requirement) => (
                <li
                  key={requirement.id}
                  data-testid="completion-unclosed-requirement"
                  className="flex flex-wrap items-center gap-1.5"
                >
                  <Link
                    href={`${requirementsHref}&requirement=${encodeURIComponent(requirement.id)}`}
                    className="text-xs text-foreground hover:underline"
                  >
                    {requirement.title}
                  </Link>
                  {isUnclosedStatus(requirement.status) ? (
                    <Badge tone="warning">{UNCLOSED_STATUS_LABELS[requirement.status]}</Badge>
                  ) : null}
                </li>
              ))}
            </ul>
          ) : null}

          {requirements.loading ? (
            <p className="text-[11px] text-muted-foreground">正在加载未闭环需求…</p>
          ) : null}
          {!requirements.loading && missingUnclosed > 0 ? (
            <p className="text-[11px] text-muted-foreground">等 {missingUnclosed} 条</p>
          ) : null}
          {requirements.error !== null ? (
            <InlineError
              error={requirements.error}
              message="未闭环需求详情加载失败，已按版本计数显示。"
            />
          ) : null}
        </div>

        {pendingQuestions > 0 ? (
          <p data-testid="completion-pending-questions" className="text-muted-foreground">
            待回答问题 {pendingQuestions} 条 ·{" "}
            <Link href={overviewHref} className={linkClassName}>
              去总览处理 →
            </Link>
          </p>
        ) : null}

        {pendingDecisions > 0 ? (
          <p data-testid="completion-pending-decisions" className="text-muted-foreground">
            待处理人工决策 {pendingDecisions} 条 ·{" "}
            <Link href={overviewHref} className={linkClassName}>
              去总览处理 →
            </Link>
          </p>
        ) : null}
      </div>

      {reportState.error !== null ? (
        <InlineError error={reportState.error} message="报告状态读取失败，暂按未发布显示。" />
      ) : null}

      <p data-testid="completion-next-step" className="text-[11px] text-muted-foreground">
        {nextStep}
      </p>
    </section>
  )
}
