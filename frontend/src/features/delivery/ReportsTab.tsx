"use client"

import { FileText } from "lucide-react"
import { useRouter, useSearchParams } from "next/navigation"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"

import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { Dialog } from "@/components/ui/Dialog"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { LoadingCard } from "@/components/ui/Spinner"
import { Textarea } from "@/components/ui/Textarea"
import { useVersionEvents } from "@/features/clarification/useVersionEvents"
import { formatDateTime } from "@/lib/format"

import { downloadHTML, getReport, getRevision, listReports, regenerate } from "./api"
import { ReportDetailPanel } from "./ReportDetailPanel"
import {
  REPORT_KIND_LABELS,
  REPORT_STATUS_LABELS,
  type ReportDetailView,
  type ReportKind,
  type ReportRevisionView,
  type ReportStatus,
  type ReportView,
} from "./types"

export type ReportsTabProps = {
  versionId: string
}

const STATUS_TONE: Record<ReportStatus, BadgeTone> = {
  draft: "neutral",
  published: "success",
  stale: "warning",
}

/** SSE bursts (report.stale + report.published) collapse into one snapshot refetch. */
const SSE_REFRESH_DEBOUNCE_MS = 350

function parseRevision(value: string | null): number | null {
  if (value === null) return null
  const parsed = Number(value)
  return Number.isInteger(parsed) && parsed >= 1 ? parsed : null
}

function saveBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement("a")
  anchor.href = url
  anchor.download = filename
  anchor.rel = "noopener"
  anchor.style.display = "none"
  document.body.appendChild(anchor)
  anchor.click()
  anchor.remove()
  URL.revokeObjectURL(url)
}

/**
 * P4 reports tab (spec §9.3): version summary first, a switch to wave reports,
 * revision history with as-of/stale/render status, authenticated HTML opening
 * and the append-only regenerate action. `?report=`/`?revision=` are the deep
 * link source of truth; every request goes through `./api` (api-client).
 */
export function ReportsTab({ versionId }: ReportsTabProps) {
  const router = useRouter()
  const searchParams = useSearchParams()

  const [kind, setKind] = useState<ReportKind>("version")
  const [reports, setReports] = useState<ReportView[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<unknown>(null)
  const [reloadToken, setReloadToken] = useState(0)

  const [selectedReportId, setSelectedReportId] = useState<string | null>(() =>
    searchParams.get("report"),
  )
  const [selectedRevision, setSelectedRevision] = useState<number | null>(() =>
    parseRevision(searchParams.get("revision")),
  )
  const [detail, setDetail] = useState<ReportDetailView | null>(null)
  const [revisionView, setRevisionView] = useState<ReportRevisionView | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)
  const [detailError, setDetailError] = useState<unknown>(null)
  const [detailToken, setDetailToken] = useState(0)

  const [opening, setOpening] = useState(false)
  const [actionError, setActionError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)

  const [regenerateOpen, setRegenerateOpen] = useState(false)
  const [regenerateReason, setRegenerateReason] = useState("")
  const [regenerating, setRegenerating] = useState(false)
  const [regenerateError, setRegenerateError] = useState<unknown>(null)

  // Follow URL deep links (back/forward) without rewriting them on mount.
  useEffect(() => {
    const urlReport = searchParams.get("report")
    if (urlReport !== null) setSelectedReportId(urlReport)
    setSelectedRevision(parseRevision(searchParams.get("revision")))
  }, [searchParams])

  const fetchReports = useCallback(() => listReports(versionId, { limit: 100 }), [versionId])

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken forces a refetch
  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    void fetchReports()
      .then((items) => {
        if (cancelled) return
        setReports(items)
        setSelectedReportId((current) => {
          if (current !== null) return current
          const urlReport = searchParams.get("report")
          if (urlReport !== null) return urlReport
          const versionReport = items.find((report) => report.kind === "version")
          return versionReport?.id ?? items[0]?.id ?? null
        })
      })
      .catch((cause: unknown) => {
        if (!cancelled) setError(cause)
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [fetchReports, reloadToken])

  // biome-ignore lint/correctness/useExhaustiveDependencies: detailToken forces a retry
  useEffect(() => {
    if (selectedReportId === null) {
      setDetail(null)
      setDetailLoading(false)
      setDetailError(null)
      return undefined
    }
    let cancelled = false
    setDetail(null)
    setRevisionView(null)
    setDetailLoading(true)
    setDetailError(null)
    void getReport(versionId, selectedReportId)
      .then((next) => {
        if (!cancelled) setDetail(next)
      })
      .catch((cause: unknown) => {
        if (!cancelled) setDetailError(cause)
      })
      .finally(() => {
        if (!cancelled) setDetailLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [versionId, selectedReportId, detailToken])

  const effectiveRevisionNumber =
    selectedRevision ?? detail?.current_revision?.revision ?? detail?.revisions[0]?.revision ?? null

  // Fetch the selected revision metadata when the dropdown/deep link differs
  // from the implicit current revision.
  // biome-ignore lint/correctness/useExhaustiveDependencies: detail.id is the request key
  useEffect(() => {
    if (selectedRevision === null || detail === null) {
      setRevisionView(null)
      return undefined
    }
    let cancelled = false
    void getRevision(versionId, detail.id, selectedRevision)
      .then((next) => {
        if (!cancelled) setRevisionView(next)
      })
      .catch((cause: unknown) => {
        if (!cancelled) setActionError(cause)
      })
    return () => {
      cancelled = true
    }
  }, [versionId, detail?.id, selectedRevision])

  const selectedReportIdRef = useRef(selectedReportId)
  selectedReportIdRef.current = selectedReportId
  const selectedRevisionRef = useRef(selectedRevision)
  selectedRevisionRef.current = selectedRevision

  /**
   * Silent live refetch: the list, the selected report detail and the explicit
   * revision metadata. `selectedRevision` is never reset here, so a revision the
   * user picked stays selected while the report gains new revisions upstream.
   */
  const refetchFromEvents = useCallback(async () => {
    try {
      setReports(await fetchReports())
    } catch {
      // Keep the last list snapshot; the next event or manual retry refreshes.
    }
    const reportId = selectedReportIdRef.current
    if (reportId === null) return
    try {
      setDetail(await getReport(versionId, reportId))
    } catch {
      // Keep the last detail snapshot.
    }
    const revisionNumber = selectedRevisionRef.current
    if (revisionNumber === null) return
    try {
      setRevisionView(await getRevision(versionId, reportId, revisionNumber))
    } catch {
      // Keep the last revision snapshot.
    }
  }, [fetchReports, versionId])

  const eventRefreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const eventRefreshRef = useRef(refetchFromEvents)
  eventRefreshRef.current = refetchFromEvents

  const scheduleEventRefresh = useCallback(() => {
    if (eventRefreshTimer.current !== null) clearTimeout(eventRefreshTimer.current)
    eventRefreshTimer.current = setTimeout(() => {
      eventRefreshTimer.current = null
      void eventRefreshRef.current()
    }, SSE_REFRESH_DEBOUNCE_MS)
  }, [])

  const refreshNow = useCallback(() => {
    if (eventRefreshTimer.current !== null) {
      clearTimeout(eventRefreshTimer.current)
      eventRefreshTimer.current = null
    }
    void eventRefreshRef.current()
  }, [])

  useEffect(
    () => () => {
      if (eventRefreshTimer.current !== null) clearTimeout(eventRefreshTimer.current)
    },
    [],
  )

  // One subscription per rendered tab; requirement/version events also stale or
  // publish reports, so they share the same debounced refresh.
  useVersionEvents(versionId, {
    onReportChanged: scheduleEventRefresh,
    onIssueChanged: scheduleEventRefresh,
    onRequirementChanged: scheduleEventRefresh,
    onVersionChanged: scheduleEventRefresh,
    onStreamReset: refreshNow,
  })

  const revision = useMemo(() => {
    if (detail === null || effectiveRevisionNumber === null) return null
    if (revisionView !== null && revisionView.revision === effectiveRevisionNumber) {
      return revisionView
    }
    return (
      detail.revisions.find((item) => item.revision === effectiveRevisionNumber) ??
      (detail.current_revision?.revision === effectiveRevisionNumber
        ? detail.current_revision
        : null)
    )
  }, [detail, effectiveRevisionNumber, revisionView])

  const visibleReports = useMemo(
    () => reports.filter((report) => report.kind === kind),
    [reports, kind],
  )

  const writeUrl = useCallback(
    (reportId: string | null, revisionNumber: number | null) => {
      const params = new URLSearchParams({ tab: "reports" })
      if (reportId !== null) params.set("report", reportId)
      if (revisionNumber !== null) params.set("revision", String(revisionNumber))
      router.replace(`/versions/${encodeURIComponent(versionId)}?${params.toString()}`)
    },
    [router, versionId],
  )

  const selectReport = useCallback(
    (reportId: string) => {
      setSelectedReportId(reportId)
      setSelectedRevision(null)
      setRevisionView(null)
      setActionError(null)
      setNotice(null)
      writeUrl(reportId, null)
    },
    [writeUrl],
  )

  const selectKind = useCallback(
    (nextKind: ReportKind) => {
      setKind(nextKind)
      const candidate = reports.find((report) => report.kind === nextKind)
      if (candidate !== undefined) {
        selectReport(candidate.id)
      } else {
        setSelectedReportId(null)
        setSelectedRevision(null)
        setDetail(null)
        setRevisionView(null)
        writeUrl(null, null)
      }
    },
    [reports, selectReport, writeUrl],
  )

  const selectRevision = useCallback(
    (nextRevision: number) => {
      if (!Number.isInteger(nextRevision) || nextRevision < 1) return
      setSelectedRevision(nextRevision)
      if (selectedReportId !== null) writeUrl(selectedReportId, nextRevision)
    },
    [selectedReportId, writeUrl],
  )

  async function openReportHTML(): Promise<void> {
    if (detail === null || revision === null || !revision.html_available) return
    setOpening(true)
    setActionError(null)
    try {
      const download = await downloadHTML(versionId, detail.id, revision.revision)
      const url = URL.createObjectURL(download.blob)
      window.open(url, "_blank", "noopener,noreferrer")
      URL.revokeObjectURL(url)
    } catch (cause) {
      setActionError(cause)
    } finally {
      setOpening(false)
    }
  }

  async function downloadReportHTML(): Promise<void> {
    if (detail === null || revision === null || !revision.html_available) return
    setOpening(true)
    setActionError(null)
    try {
      const download = await downloadHTML(versionId, detail.id, revision.revision)
      saveBlob(
        download.blob,
        download.filename.length > 0
          ? download.filename
          : `report-${detail.id}-r${revision.revision}.html`,
      )
    } catch (cause) {
      setActionError(cause)
    } finally {
      setOpening(false)
    }
  }

  async function submitRegenerate(): Promise<void> {
    if (detail === null || regenerating) return
    setRegenerating(true)
    setRegenerateError(null)
    try {
      const trimmed = regenerateReason.trim()
      const result = await regenerate(
        versionId,
        detail.id,
        trimmed.length > 0 ? { reason: trimmed } : {},
      )
      setRegenerateOpen(false)
      setRegenerateReason("")
      setNotice(`已提交重新生成（revision ${result.revision}），生成完成后会自动刷新。`)
      setReloadToken((token) => token + 1)
      setDetailToken((token) => token + 1)
    } catch (cause) {
      setRegenerateError(cause)
    } finally {
      setRegenerating(false)
    }
  }

  return (
    <div data-testid="reports-tab" className="space-y-3">
      <fieldset className="flex flex-wrap items-center gap-2 border-0 p-0" aria-label="报告类型">
        <Button
          size="sm"
          variant={kind === "version" ? "primary" : "secondary"}
          data-testid="reports-kind-version"
          aria-pressed={kind === "version"}
          onClick={() => selectKind("version")}
        >
          版本总结
        </Button>
        <Button
          size="sm"
          variant={kind === "wave" ? "primary" : "secondary"}
          data-testid="reports-kind-wave"
          aria-pressed={kind === "wave"}
          onClick={() => selectKind("wave")}
        >
          波次报告
        </Button>
      </fieldset>

      <div className="grid gap-3 lg:grid-cols-[320px_minmax(0,1fr)]">
        <section
          data-testid="report-list"
          aria-label="报告列表"
          className="surface-card flex min-h-40 flex-col overflow-hidden"
        >
          <header className="flex items-center justify-between border-b border-border px-3 py-2">
            <h2 className="flex items-center gap-1.5 text-sm font-semibold text-foreground">
              <FileText className="size-4" aria-hidden="true" />
              报告
            </h2>
            <span className="text-[11px] text-muted-foreground">{visibleReports.length} 份</span>
          </header>

          {loading ? (
            <LoadingCard label="正在加载报告…" />
          ) : error !== null ? (
            <div className="p-3">
              <ErrorState
                error={error}
                title="报告列表加载失败"
                onRetry={() => setReloadToken((token) => token + 1)}
              />
            </div>
          ) : visibleReports.length === 0 ? (
            <div className="p-3">
              <EmptyState
                title="暂无报告"
                description="执行波次或版本收尾后会自动生成报告。"
                className="border-none bg-transparent py-6"
              />
            </div>
          ) : (
            <ol aria-label="报告列表" className="min-h-0 flex-1 space-y-1.5 overflow-y-auto p-2">
              {visibleReports.map((report) => {
                const current = report.current_revision ?? null
                return (
                  <li key={report.id}>
                    <button
                      type="button"
                      data-testid={`report-${report.id}`}
                      aria-current={report.id === selectedReportId ? "true" : undefined}
                      onClick={() => selectReport(report.id)}
                      className={
                        "w-full rounded-lg border px-2.5 py-2 text-left transition-colors " +
                        "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring " +
                        (report.id === selectedReportId
                          ? "border-primary/40 bg-accent/60"
                          : "border-border bg-card hover:bg-muted/60")
                      }
                    >
                      <span className="flex flex-wrap items-center gap-1.5">
                        <span className="text-xs font-semibold text-foreground">
                          {REPORT_KIND_LABELS[report.kind]}
                          {report.kind === "wave" && report.wave_id != null
                            ? ` #${report.wave_id}`
                            : ""}
                        </span>
                        <Badge tone={STATUS_TONE[report.status]}>
                          {REPORT_STATUS_LABELS[report.status]}
                        </Badge>
                      </span>
                      <span className="mt-1 block text-[10px] text-muted-foreground">
                        {current === null
                          ? "暂无修订"
                          : `as-of ${formatDateTime(current.as_of)} · v${current.revision}`}
                      </span>
                    </button>
                  </li>
                )
              })}
            </ol>
          )}
        </section>

        <ReportDetailPanel
          versionId={versionId}
          report={detail}
          revision={revision}
          revisionNumber={effectiveRevisionNumber}
          loading={detailLoading}
          error={detailError}
          onRetry={() => setDetailToken((token) => token + 1)}
          onRevisionChange={selectRevision}
          onOpenHTML={() => {
            void openReportHTML()
          }}
          onDownloadHTML={() => {
            void downloadReportHTML()
          }}
          onRegenerate={() => {
            setRegenerateError(null)
            setRegenerateOpen(true)
          }}
          opening={opening}
          regenerating={regenerating}
          actionError={actionError}
          notice={notice}
        />
      </div>

      <Dialog
        open={regenerateOpen}
        onClose={() => {
          if (!regenerating) setRegenerateOpen(false)
        }}
        title="重新生成报告"
        description="服务端会创建新的 append-only revision；旧 revision 保持可读。"
        footer={
          <>
            <Button
              variant="secondary"
              size="sm"
              disabled={regenerating}
              onClick={() => setRegenerateOpen(false)}
            >
              取消
            </Button>
            <Button
              size="sm"
              disabled={regenerating}
              onClick={() => {
                void submitRegenerate()
              }}
            >
              {regenerating ? "提交中…" : "重新生成"}
            </Button>
          </>
        }
      >
        <div className="space-y-3">
          <Field
            id="report-regenerate-reason"
            label="重新生成原因（可选）"
            hint="会写入审计；最多 2000 字。"
          >
            <Textarea
              id="report-regenerate-reason"
              value={regenerateReason}
              maxLength={2000}
              disabled={regenerating}
              placeholder="例如：问题状态变化后需要刷新报告"
              onChange={(event) => setRegenerateReason(event.target.value)}
            />
          </Field>
          {regenerateError !== null ? <InlineError error={regenerateError} /> : null}
        </div>
      </Dialog>
    </div>
  )
}
