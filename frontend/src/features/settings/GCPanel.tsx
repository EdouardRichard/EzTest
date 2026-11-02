"use client"

import { RefreshCw, ShieldAlert, Trash2 } from "lucide-react"
import { useEffect, useMemo, useState } from "react"

import { Alert } from "@/components/ui/Alert"
import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { Dialog } from "@/components/ui/Dialog"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Input } from "@/components/ui/Input"
import { LoadingCard } from "@/components/ui/Spinner"
import { Table, type TableColumn } from "@/components/ui/Table"
import { useAuth } from "@/features/auth/AuthProvider"
import { formatBytes, formatDateTime } from "@/lib/format"

import {
  createGCRun,
  type GCReport,
  type GCRun,
  type GCRunDetail,
  type GCRunMode,
  type GCRunStatus,
  getGCRun,
  listGCRuns,
} from "./api"

/** Candidate class labels (matches storage.gc candidate classes). */
export const GC_CLASS_LABELS: Record<string, string> = {
  unreferenced: "无引用对象",
  temp: "临时文件",
  superseded_export: "被替代导出",
  orphan_file: "孤儿文件",
}

const GC_CLASS_ORDER = ["unreferenced", "temp", "superseded_export", "orphan_file"]

const GC_MODE_LABELS: Record<GCRunMode, string> = {
  dry_run: "演练",
  apply: "执行",
}

const GC_STATUS_TONE: Record<GCRunStatus, BadgeTone> = {
  running: "info",
  completed: "success",
  failed: "danger",
}

const GC_STATUS_LABELS: Record<GCRunStatus, string> = {
  running: "运行中",
  completed: "已完成",
  failed: "失败",
}

const APPLY_CONFIRMATION = "APPLY"

type ClassRow = {
  className: string
  label: string
  count: number
  bytes: number
  deleted: number
}

/**
 * Builds the per-class report rows. Counts are authoritative; the byte column
 * is summed from the capped `candidate_samples` (the report only stores
 * aggregate planned/deleted bytes). In apply mode the deleted count mirrors the
 * selected candidates; failures are surfaced in the summary/errors instead.
 * Canonical classes are always listed so a zero class is visible.
 */
function buildClassRows(report: GCReport | undefined, mode: GCRunMode): ClassRow[] {
  if (report === undefined) return []
  const candidates = report.candidates ?? {}
  const bytesByClass = new Map<string, number>()
  for (const sample of report.candidate_samples ?? []) {
    bytesByClass.set(sample.class, (bytesByClass.get(sample.class) ?? 0) + sample.size)
  }
  const classes = Array.from(new Set([...GC_CLASS_ORDER, ...Object.keys(candidates)]))
  return classes.map((className) => {
    const count = candidates[className] ?? 0
    return {
      className,
      label: GC_CLASS_LABELS[className] ?? className,
      count,
      bytes: bytesByClass.get(className) ?? 0,
      deleted: mode === "apply" ? count : 0,
    }
  })
}

function RunStatusBadge({ status }: { status: GCRunStatus }) {
  return <Badge tone={GC_STATUS_TONE[status]}>{GC_STATUS_LABELS[status]}</Badge>
}

/** Admin-only data retention/GC panel (spec §10, contract p7-api §6.2). */
export function GCPanel() {
  const { user } = useAuth()
  const isAdmin = user?.role === "admin"

  const [runs, setRuns] = useState<GCRun[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<unknown>(null)
  const [reloadKey, setReloadKey] = useState(0)

  const [selectedRunId, setSelectedRunId] = useState<string | null>(null)
  const [detail, setDetail] = useState<GCRunDetail | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)
  const [actionError, setActionError] = useState<unknown>(null)

  const [dryRunning, setDryRunning] = useState(false)
  const [applyOpen, setApplyOpen] = useState(false)
  const [applyConfirm, setApplyConfirm] = useState("")
  const [applying, setApplying] = useState(false)
  const [applyError, setApplyError] = useState<unknown>(null)

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadKey forces a refetch
  useEffect(() => {
    if (!isAdmin) {
      setLoading(false)
      return undefined
    }
    let cancelled = false
    setLoading(true)
    setError(null)
    void listGCRuns()
      .then((page) => {
        if (cancelled) return
        setRuns(page.items)
        setSelectedRunId((current) => {
          if (current !== null && page.items.some((run) => run.id === current)) return current
          return page.items[0]?.id ?? null
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
  }, [reloadKey, isAdmin])

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadKey refreshes the selected detail
  useEffect(() => {
    if (selectedRunId === null) {
      setDetail(null)
      return undefined
    }
    let cancelled = false
    setDetailLoading(true)
    setDetail(null)
    setActionError(null)
    void getGCRun(selectedRunId)
      .then((next) => {
        if (!cancelled) setDetail(next)
      })
      .catch((cause: unknown) => {
        if (!cancelled) setActionError(cause)
      })
      .finally(() => {
        if (!cancelled) setDetailLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [selectedRunId, reloadKey])

  const selectedRun = useMemo(
    () => runs.find((run) => run.id === selectedRunId) ?? runs[0] ?? null,
    [runs, selectedRunId],
  )

  const report = detail?.report ?? selectedRun?.report
  const classRows = useMemo(
    () => buildClassRows(report, detail?.mode ?? selectedRun?.mode ?? "dry_run"),
    [detail?.mode, report, selectedRun?.mode],
  )

  const classColumns: TableColumn<ClassRow>[] = [
    {
      key: "class",
      header: "类别",
      render: (row) => (
        <div className="min-w-0">
          <span className="text-xs font-medium text-foreground">{row.label}</span>
          <span className="ml-1 font-mono text-[10px] text-muted-foreground">{row.className}</span>
        </div>
      ),
    },
    {
      key: "count",
      header: "候选数",
      render: (row) => <span className="text-xs text-foreground">{row.count}</span>,
    },
    {
      key: "bytes",
      header: "字节",
      render: (row) => (
        <span className="text-xs text-muted-foreground" title="按最多 200 条候选样本汇总">
          {formatBytes(row.bytes)}
        </span>
      ),
    },
    {
      key: "deleted",
      header: "删除数",
      render: (row) => (
        <span className="text-xs text-muted-foreground">
          {row.deleted === 0 ? "—" : row.deleted}
        </span>
      ),
    },
  ]

  const historyColumns: TableColumn<GCRun>[] = [
    {
      key: "run",
      header: "运行",
      render: (run) => <span className="font-mono text-[11px] text-foreground">#{run.id}</span>,
    },
    {
      key: "mode",
      header: "模式",
      render: (run) => (
        <Badge tone={run.mode === "apply" ? "warning" : "neutral"}>
          {GC_MODE_LABELS[run.mode]}
        </Badge>
      ),
    },
    {
      key: "status",
      header: "状态",
      render: (run) => <RunStatusBadge status={run.status} />,
    },
    {
      key: "started",
      header: "开始时间",
      render: (run) => (
        <span className="text-xs text-muted-foreground">{formatDateTime(run.started_at)}</span>
      ),
    },
    {
      key: "deleted",
      header: "删除",
      render: (run) => (
        <span className="text-xs text-muted-foreground">
          {run.deleted_objects} 对象 / {formatBytes(run.deleted_bytes)}
        </span>
      ),
    },
    {
      key: "actions",
      header: "操作",
      className: "text-right",
      render: (run) => (
        <Button variant="ghost" size="sm" onClick={() => setSelectedRunId(run.id)}>
          查看明细
        </Button>
      ),
    },
  ]

  if (user === null || user.role !== "admin") return null
  async function runDryRun(): Promise<void> {
    setDryRunning(true)
    setActionError(null)
    try {
      const created = await createGCRun("dry_run")
      setSelectedRunId(created.id)
      setReloadKey((key) => key + 1)
    } catch (cause) {
      setActionError(cause)
    } finally {
      setDryRunning(false)
    }
  }

  async function submitApply(): Promise<void> {
    if (applyConfirm.trim() !== APPLY_CONFIRMATION) return
    setApplying(true)
    setApplyError(null)
    try {
      const created = await createGCRun("apply", true)
      setApplyOpen(false)
      setApplyConfirm("")
      setSelectedRunId(created.id)
      setReloadKey((key) => key + 1)
    } catch (cause) {
      setApplyError(cause)
    } finally {
      setApplying(false)
    }
  }

  const tombstones = detail?.tombstones ?? []
  const busy = dryRunning || applying

  return (
    <section
      data-testid="gc-panel"
      aria-label="数据保留与 GC"
      className="surface-card overflow-hidden"
    >
      <header className="flex flex-wrap items-start justify-between gap-3 border-b border-border px-4 py-3">
        <div>
          <h2 className="flex items-center gap-1.5 text-sm font-semibold text-foreground">
            <ShieldAlert className="size-4" aria-hidden="true" />
            数据保留与 GC
          </h2>
          <p className="mt-1 text-xs text-muted-foreground">
            扫描内容账本中的孤儿对象、临时文件与被替代导出。演练只生成报告；执行会按 tombstone →
            引用 → 对象 → 文件 的顺序删除。
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button
            variant="secondary"
            size="sm"
            onClick={() => setReloadKey((key) => key + 1)}
            disabled={loading || busy}
          >
            <RefreshCw className="size-3.5" aria-hidden="true" />
            刷新
          </Button>
          <Button size="sm" onClick={() => void runDryRun()} disabled={loading || busy}>
            运行演练
          </Button>
          <Button
            size="sm"
            variant="secondary"
            onClick={() => {
              setApplyError(null)
              setApplyConfirm("")
              setApplyOpen(true)
            }}
            disabled={loading || busy}
          >
            <Trash2 className="size-3.5" aria-hidden="true" />
            执行回收
          </Button>
        </div>
      </header>

      <div className="space-y-4 px-4 py-3">
        {actionError !== null ? <InlineError error={actionError} /> : null}

        {loading && runs.length === 0 ? (
          <LoadingCard label="正在加载 GC 运行历史…" />
        ) : error !== null ? (
          <ErrorState
            error={error}
            title="GC 运行历史加载失败"
            onRetry={() => setReloadKey((key) => key + 1)}
          />
        ) : runs.length === 0 ? (
          <EmptyState
            title="还没有 GC 运行记录"
            description="先运行一次演练，确认候选分类后再执行回收。"
            className="border-none bg-transparent py-4"
          />
        ) : (
          <>
            <section aria-label="最新运行" className="space-y-2">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <h3 className="text-xs font-semibold text-foreground">
                  最新运行
                  {selectedRun !== null ? (
                    <span className="ml-1.5 font-mono text-[10px] font-normal text-muted-foreground">
                      #{selectedRun.id}
                    </span>
                  ) : null}
                </h3>
                {selectedRun !== null ? (
                  <div className="flex flex-wrap items-center gap-2 text-[11px] text-muted-foreground">
                    <Badge tone={selectedRun.mode === "apply" ? "warning" : "neutral"}>
                      {GC_MODE_LABELS[selectedRun.mode]}
                    </Badge>
                    <RunStatusBadge status={selectedRun.status} />
                    <span>{formatDateTime(selectedRun.started_at)}</span>
                    {detailLoading ? <span>正在加载明细…</span> : null}
                  </div>
                ) : null}
              </div>

              {selectedRun !== null ? (
                <div className="grid gap-2 text-[11px] text-muted-foreground sm:grid-cols-2 lg:grid-cols-4">
                  <p>
                    扫描：
                    <span className="text-foreground">{selectedRun.scanned_objects}</span> 对象 /{" "}
                    <span className="text-foreground">{selectedRun.scanned_refs}</span> 引用
                  </p>
                  <p>
                    计划删除：
                    <span className="text-foreground">{report?.planned?.objects ?? 0}</span> 对象 /{" "}
                    <span className="text-foreground">
                      {formatBytes(report?.planned?.bytes ?? 0)}
                    </span>
                  </p>
                  <p>
                    已删除：
                    <span className="text-foreground">{selectedRun.deleted_objects}</span> 对象 /{" "}
                    <span className="text-foreground">
                      {formatBytes(selectedRun.deleted_bytes)}
                    </span>
                  </p>
                  <p>
                    tombstone：
                    <span className="text-foreground">
                      {report?.tombstones ?? selectedRun.deleted_objects}
                    </span>
                  </p>
                </div>
              ) : null}

              <Table
                ariaLabel="GC 最新运行报告"
                columns={classColumns}
                rows={classRows}
                rowKey={(row) => row.className}
                empty={
                  <span className="text-xs text-muted-foreground">本次运行没有候选对象。</span>
                }
              />

              {report?.errors !== undefined && report.errors.length > 0 ? (
                <Alert tone="danger" title="GC 运行错误">
                  <ul className="list-disc pl-4">
                    {report.errors.map((message) => (
                      <li key={message}>{message}</li>
                    ))}
                  </ul>
                </Alert>
              ) : null}
            </section>

            <section aria-label="tombstone 明细" data-testid="gc-tombstones" className="space-y-2">
              <h3 className="text-xs font-semibold text-foreground">
                tombstone 明细
                <span className="ml-1.5 font-normal text-muted-foreground">
                  {detailLoading ? "加载中…" : `${tombstones.length} 条`}
                </span>
              </h3>
              {tombstones.length === 0 ? (
                <p className="text-[11px] text-muted-foreground">
                  该运行没有 tombstone 记录（演练或未删除对象）。
                </p>
              ) : (
                <ul className="space-y-1.5">
                  {tombstones.map((tombstone) => (
                    <li
                      key={tombstone.id}
                      className="rounded-lg border border-border bg-muted/30 px-3 py-2 text-[11px]"
                    >
                      <p className="flex flex-wrap items-center gap-2 text-foreground">
                        <span className="font-mono">{tombstone.relative_path}</span>
                        <Badge tone="neutral">
                          {GC_CLASS_LABELS[tombstone.reason] ?? tombstone.reason}
                        </Badge>
                        <span className="text-muted-foreground">{formatBytes(tombstone.size)}</span>
                      </p>
                      <p className="mt-0.5 font-mono text-[10px] text-muted-foreground">
                        {tombstone.object_sha} · {formatDateTime(tombstone.deleted_at)}
                      </p>
                    </li>
                  ))}
                </ul>
              )}
            </section>

            <section aria-label="运行历史" className="space-y-2 border-t border-border pt-3">
              <h3 className="text-xs font-semibold text-foreground">运行历史</h3>
              <Table
                ariaLabel="GC 运行历史"
                columns={historyColumns}
                rows={runs}
                rowKey={(run) => run.id}
              />
            </section>
          </>
        )}
      </div>

      <Dialog
        open={applyOpen}
        onClose={() => {
          if (!applying) setApplyOpen(false)
        }}
        title="确认执行 GC"
        description="执行会永久删除候选内容与 tombstone 记录，且不可撤销。"
        footer={
          <>
            <Button
              variant="secondary"
              size="sm"
              disabled={applying}
              onClick={() => setApplyOpen(false)}
            >
              取消
            </Button>
            <Button
              size="sm"
              disabled={applying || applyConfirm.trim() !== APPLY_CONFIRMATION}
              onClick={() => {
                void submitApply()
              }}
            >
              <Trash2 className="size-3.5" aria-hidden="true" />
              {applying ? "执行中…" : "确认执行"}
            </Button>
          </>
        }
      >
        <div className="space-y-3">
          <Field
            id="gc-apply-confirm"
            label={`输入 ${APPLY_CONFIRMATION} 以确认`}
            hint="apply 模式会真实删除数据；演练（dry_run）不会。"
          >
            <Input
              id="gc-apply-confirm"
              value={applyConfirm}
              autoComplete="off"
              disabled={applying}
              placeholder={APPLY_CONFIRMATION}
              onChange={(event) => setApplyConfirm(event.target.value)}
            />
          </Field>
          {applyError !== null ? <InlineError error={applyError} /> : null}
        </div>
      </Dialog>
    </section>
  )
}
