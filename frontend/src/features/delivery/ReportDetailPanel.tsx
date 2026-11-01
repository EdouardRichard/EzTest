"use client"

import { Download, ExternalLink, FileText, RefreshCw } from "lucide-react"
import Link from "next/link"

import { Alert } from "@/components/ui/Alert"
import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Select } from "@/components/ui/Select"
import { LoadingCard } from "@/components/ui/Spinner"
import { formatDateTime } from "@/lib/format"

import {
  REPORT_KIND_LABELS,
  REPORT_REF_LABELS,
  REPORT_RENDER_STATUS_LABELS,
  REPORT_STATUS_LABELS,
  type ReportDetailView,
  type ReportRefKind,
  type ReportRefView,
  type ReportRevisionView,
  type ReportStatus,
} from "./types"

export type ReportDetailPanelProps = {
  versionId: string
  report: ReportDetailView | null
  /** The revision selected in the dropdown (defaults to the current revision). */
  revision: ReportRevisionView | null
  revisionNumber: number | null
  loading: boolean
  error: unknown
  onRetry: () => void
  onRevisionChange: (revision: number) => void
  onOpenHTML: () => void
  onDownloadHTML?: () => void
  onRegenerate?: () => void
  /** Global reports reuse this panel without download/regenerate/export. */
  readOnly?: boolean
  /** True while the HTML bytes are being fetched. */
  opening: boolean
  /** True while a regenerate request is being submitted. */
  regenerating: boolean
  actionError: unknown
  notice: string | null
}

const STATUS_TONE: Record<ReportStatus, BadgeTone> = {
  draft: "neutral",
  published: "success",
  stale: "warning",
}

function reportRefHref(versionId: string, ref: ReportRefView): string {
  const base = `/versions/${encodeURIComponent(versionId)}`
  switch (ref.kind) {
    case "issue": {
      const params = new URLSearchParams({ tab: "issues", issue: ref.ref_id })
      return `${base}?${params.toString()}`
    }
    case "requirement": {
      const params = new URLSearchParams({ tab: "requirements", requirement: ref.ref_id })
      return `${base}?${params.toString()}`
    }
    case "wave": {
      const params = new URLSearchParams({ tab: "execution", wave: ref.ref_id })
      return `${base}?${params.toString()}`
    }
    case "case_run": {
      const params = new URLSearchParams({ tab: "execution", case_run: ref.ref_id })
      return `${base}?${params.toString()}`
    }
    case "scenario":
    case "case_version": {
      const params = new URLSearchParams({ tab: "design" })
      return `${base}?${params.toString()}`
    }
    default:
      return `${base}?tab=chain`
  }
}

function refLabel(ref: ReportRefView): string {
  return `${REPORT_REF_LABELS[ref.kind as ReportRefKind]} #${ref.ref_id}`
}

function RefList({ versionId, refs }: { versionId: string; refs: ReportRefView[] }) {
  if (refs.length === 0) {
    return <p className="text-[11px] text-muted-foreground">该报告没有关联引用。</p>
  }
  return (
    <ul className="flex flex-wrap gap-x-3 gap-y-1.5">
      {refs.map((ref) => (
        <li key={`${ref.kind}-${ref.ref_id}`}>
          <Link
            href={reportRefHref(versionId, ref)}
            className="inline-flex items-center gap-1 text-xs text-primary underline-offset-2 hover:underline"
          >
            {refLabel(ref)}
            <ExternalLink className="size-3" aria-hidden="true" />
          </Link>
        </li>
      ))}
    </ul>
  )
}

/**
 * Right-hand report detail (spec §9.3): revision metadata, as-of, stale hint,
 * revision history dropdown and the HTML/regenerate/PDF actions. All requests
 * belong to `ReportsTab`; this panel is presentational and read-only apart
 * from the explicit actions.
 */
export function ReportDetailPanel({
  versionId,
  report,
  revision,
  revisionNumber,
  loading,
  error,
  onRetry,
  onRevisionChange,
  onOpenHTML,
  onDownloadHTML,
  onRegenerate,
  readOnly = false,
  opening,
  regenerating,
  actionError,
  notice,
}: ReportDetailPanelProps) {
  if (loading) {
    return (
      <section data-testid="report-detail" aria-label="报告详情" className="surface-card p-4">
        <LoadingCard label="正在加载报告详情…" />
      </section>
    )
  }

  if (error !== null) {
    return (
      <section data-testid="report-detail" aria-label="报告详情" className="surface-card p-4">
        <ErrorState error={error} title="报告详情加载失败" onRetry={onRetry} />
      </section>
    )
  }

  if (report === null) {
    return (
      <section data-testid="report-detail" aria-label="报告详情" className="surface-card p-4">
        <EmptyState
          title="请选择报告"
          description="从左侧选择版本总结或波次报告查看修订详情。"
          className="border-none bg-transparent"
        />
      </section>
    )
  }

  const currentRevisionNumber = report.current_revision?.revision ?? null
  const showingCurrent = revisionNumber !== null && revisionNumber === currentRevisionNumber
  const revisions = [...report.revisions].sort((left, right) => right.revision - left.revision)
  const htmlAvailable = revision?.html_available === true
  const generateState = revision?.render_status ?? null
  const generated = generateState === "done" && htmlAvailable

  return (
    <section data-testid="report-detail" aria-label="报告详情" className="surface-card p-4">
      <header className="flex flex-wrap items-start justify-between gap-2 border-b border-border pb-3">
        <div className="min-w-0">
          <p className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
            <FileText className="size-3.5" aria-hidden="true" />
            报告详情
          </p>
          <h2 className="mt-0.5 flex flex-wrap items-center gap-1.5 text-sm font-semibold text-foreground">
            {REPORT_KIND_LABELS[report.kind]}
            {report.kind === "wave" && report.wave_id !== null && report.wave_id !== undefined ? (
              <span className="text-muted-foreground">· 波次 #{report.wave_id}</span>
            ) : null}
            <Badge tone={STATUS_TONE[report.status]}>{REPORT_STATUS_LABELS[report.status]}</Badge>
            {showingCurrent ? <Badge tone="info">当前修订</Badge> : null}
          </h2>
        </div>
      </header>

      {report.status === "stale" ? (
        <Alert tone="warning" title="输入已变化" className="mt-3">
          输入已变化，建议重新生成。
        </Alert>
      ) : null}

      {notice !== null ? (
        <p
          role="status"
          data-testid="report-notice"
          className="mt-3 rounded-lg border border-success/30 bg-success/10 px-3 py-2 text-[11px] text-success"
        >
          {notice}
        </p>
      ) : null}

      <div className="mt-3 space-y-3">
        <Field id="report-revision" label="修订版本">
          <Select
            id="report-revision"
            value={revisionNumber === null ? "" : String(revisionNumber)}
            disabled={revisions.length === 0}
            onChange={(event) => onRevisionChange(Number(event.target.value))}
          >
            {revisions.length === 0 ? <option value="">暂无修订</option> : null}
            {revisions.map((item) => (
              <option key={item.id} value={item.revision}>
                v{item.revision} · {REPORT_STATUS_LABELS[item.status]} ·{" "}
                {REPORT_RENDER_STATUS_LABELS[item.render_status]}
              </option>
            ))}
          </Select>
        </Field>

        {revision === null ? (
          <p className="text-xs text-muted-foreground">该报告还没有可读取的修订。</p>
        ) : (
          <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-[11px]">
            <dt className="text-muted-foreground">修订</dt>
            <dd className="font-medium text-foreground">v{revision.revision}</dd>
            <dt className="text-muted-foreground">as-of</dt>
            <dd className="text-foreground">{formatDateTime(revision.as_of)}</dd>
            <dt className="text-muted-foreground">模板版本</dt>
            <dd className="text-foreground">{revision.template_version}</dd>
            <dt className="text-muted-foreground">输入指纹</dt>
            <dd className="font-mono text-foreground" title={revision.input_fingerprint}>
              {revision.input_fingerprint.slice(0, 12)}
            </dd>
            <dt className="text-muted-foreground">渲染状态</dt>
            <dd className="text-foreground">
              {REPORT_RENDER_STATUS_LABELS[revision.render_status]}
              {revision.render_status === "failed" ? (
                <span className="ml-1 text-destructive">，请重新生成</span>
              ) : null}
            </dd>
          </dl>
        )}

        <div className="flex flex-wrap items-center gap-2 border-t border-border pt-3">
          <Button size="sm" disabled={!generated || opening || regenerating} onClick={onOpenHTML}>
            打开报告 HTML
          </Button>
          {readOnly ? (
            <Link
              href={`/versions/${encodeURIComponent(versionId)}?tab=reports&report=${encodeURIComponent(report.id)}`}
              className="inline-flex items-center gap-1 text-xs text-primary underline-offset-2 hover:underline"
            >
              在版本中处理
              <ExternalLink className="size-3" aria-hidden="true" />
            </Link>
          ) : (
            <>
              <Button
                size="sm"
                variant="secondary"
                disabled={!generated || opening || regenerating}
                onClick={() => onDownloadHTML?.()}
              >
                <Download className="size-3.5" aria-hidden="true" />
                下载 HTML
              </Button>
              <Button
                size="sm"
                variant="secondary"
                disabled={
                  regenerating || generateState === "pending" || generateState === "running"
                }
                onClick={() => onRegenerate?.()}
              >
                <RefreshCw className="size-3.5" aria-hidden="true" />
                重新生成
              </Button>
              <Button size="sm" variant="secondary" disabled title="PDF 导出属于 P7 阶段">
                PDF（P7）
              </Button>
            </>
          )}
        </div>

        {opening ? <p className="text-[11px] text-muted-foreground">正在获取 HTML…</p> : null}
        {actionError !== null ? <InlineError error={actionError} /> : null}
      </div>

      <section aria-label="报告关联引用" className="mt-4 space-y-2 border-t border-border pt-3">
        <h3 className="text-xs font-semibold text-foreground">关联引用</h3>
        <RefList versionId={versionId} refs={report.refs} />
      </section>
    </section>
  )
}
