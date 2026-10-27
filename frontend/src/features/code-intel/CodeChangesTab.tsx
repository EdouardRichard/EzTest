"use client"

import { Loader2 } from "lucide-react"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"

import { StatusBadge } from "@/components/ui/StatusBadge"
import { ApiError } from "@/lib/api-client"

import { getLatestIndexRun, getVersionChanges, startAnalysis } from "./api"
import { ChangedFiles } from "./ChangedFiles"
import { type CodeAnalysisView, CommitHeader } from "./CommitHeader"
import { DiffDrawer } from "./DiffDrawer"
import {
  CodeLoadErrorState,
  codeErrorReason,
  describeCodeError,
  NoBaselineCodeState,
  NoChangesCodeState,
  UnboundCodeState,
  UnindexedCodeState,
} from "./empty"
import type { Analysis, ChangedFile, ChangeList, IndexRun } from "./types"
import { useAnalysisPolling } from "./useAnalysisPolling"

/** Version-level reasons that mean "no usable code repository binding". */
const UNBOUND_REASONS = new Set([
  "code_repository_not_bound",
  "code_repository_not_found",
  "code_repository_disabled",
])

/** Index status polling cadence from spec §7.4 (max 60 attempts). */
const INDEX_POLL_INTERVAL_MS = 1500
const INDEX_POLL_MAX_ATTEMPTS = 60

type ChangesLoad =
  | { kind: "loading" }
  | { kind: "unbound" }
  | { kind: "noBaseline" }
  | { kind: "unindexed"; run: IndexRun | null }
  | { kind: "ready"; changes: ChangeList }
  | { kind: "error"; message: string }

export type CodeChangesTabProps = {
  /** Version id; a decimal string that is never re-parsed as a number (spec §7.1). */
  versionId: string
  /**
   * P2 code repository id from the version binding (`version.codeRepositoryId`).
   * `null` means unbound; omitted means the caller did not resolve the binding,
   * in which case the index gate is skipped and the analysis repo id is used for
   * diff reads.
   */
  repoId?: number | null
}

const VIEW_VARIANTS = {
  none: "neutral",
  running: "info",
  done: "success",
  degraded: "warning",
  failed: "danger",
} as const

const VIEW_BADGE_LABELS: Record<CodeAnalysisView, string> = {
  none: "未分析",
  running: "运行中",
  done: "完成",
  degraded: "降级",
  failed: "失败",
}

type AnalysisSummaryProps = {
  analysis: Analysis | null
  view: CodeAnalysisView
}

/**
 * Real analysis projection only: the change summary and backend counters. It
 * deliberately renders no affected-case/batch sections (owned by P6/P7) and no
 * placeholder facts; T11 mounts the facts/impact/unmapped panels in the slots
 * reserved by the container.
 */
function AnalysisSummary({ analysis, view }: AnalysisSummaryProps) {
  if (analysis === null) {
    return (
      <section
        aria-label="分析摘要"
        className="space-y-2 rounded-2xl border border-border bg-card p-4"
      >
        <h2 className="text-base font-semibold">分析摘要</h2>
        <p className="text-sm text-muted-foreground">尚未开始分析</p>
        <p className="text-xs text-muted-foreground">
          点击「开始分析」生成受影响需求、应用模型候选与未映射变更；P4 不展示批次/用例数据。
        </p>
      </section>
    )
  }

  const stats = analysis.stats
  return (
    <section
      aria-label="分析摘要"
      className="space-y-2 rounded-2xl border border-border bg-card p-4"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-base font-semibold">分析摘要</h2>
        <StatusBadge variant={VIEW_VARIANTS[view]}>{VIEW_BADGE_LABELS[view]}</StatusBadge>
      </div>

      {view === "running" ? (
        <>
          <p
            role="status"
            className="flex items-center gap-2 text-sm text-sky-700 dark:text-sky-300"
          >
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
            AI 影响分析运行中…
          </p>
          <p className="text-xs text-muted-foreground">每 1.5 秒自动刷新，分析完成后自动停止。</p>
        </>
      ) : null}

      {view === "done" ? (
        <p role="status" className="text-xs text-emerald-700 dark:text-emerald-300">
          分析完成
        </p>
      ) : null}

      {view === "degraded" ? (
        <div className="space-y-1">
          <p
            role="status"
            className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300"
          >
            AI 不可用，仅文件级结果
          </p>
          <p className="text-xs text-amber-700 dark:text-amber-300">分析降级</p>
        </div>
      ) : null}

      {view === "failed" ? (
        <div
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
        >
          <p>分析失败：{analysis.errors[0]?.message ?? "分析执行失败，请重试。"}</p>
        </div>
      ) : null}

      {analysis.changeSummary === "" ? null : (
        <p className="text-sm text-foreground">{analysis.changeSummary}</p>
      )}

      {stats === null ? null : (
        <p className="text-xs text-muted-foreground">
          变更文件 {stats.changedFiles} · 已分析 {stats.analyzedFiles} · 候选{" "}
          {analysis.candidates.length} · 未映射 {analysis.unmapped.length}
        </p>
      )}
    </section>
  )
}

/**
 * 「代码与变更」container (spec §9.1/§9.2): loads the version change preview,
 * gates on the repository index, derives the six UI states (unbound / unindexed
 * / no changes / running / done / degraded, plus a failed error state), polls a
 * running analysis, and opens the unified diff drawer on demand. It never
 * fabricates batches, cases or facts; T11 fills the reserved panel slots.
 */
export function CodeChangesTab({ versionId, repoId }: CodeChangesTabProps) {
  const [load, setLoad] = useState<ChangesLoad>(
    repoId === null ? { kind: "unbound" } : { kind: "loading" },
  )
  const [refreshing, setRefreshing] = useState(false)
  const [openFile, setOpenFile] = useState<ChangedFile | null>(null)
  const [starting, setStarting] = useState(false)
  const [actionError, setActionError] = useState("")
  const requestRef = useRef(0)

  const loadChanges = useCallback(
    async (options: { silent?: boolean } = {}): Promise<void> => {
      const requestId = ++requestRef.current
      setRefreshing(true)
      if (!options.silent) setLoad({ kind: "loading" })

      if (repoId === null) {
        setLoad({ kind: "unbound" })
        setRefreshing(false)
        return
      }

      try {
        const [changesResult, indexResult] = await Promise.allSettled([
          getVersionChanges(versionId),
          typeof repoId === "number" ? getLatestIndexRun(repoId) : Promise.resolve(null),
        ] as const)
        if (requestRef.current !== requestId) return

        if (changesResult.status === "rejected") {
          const reason = codeErrorReason(changesResult.reason)
          if (UNBOUND_REASONS.has(reason)) {
            setLoad({ kind: "unbound" })
            return
          }
          if (reason === "baseline_commit_missing") {
            setLoad({ kind: "noBaseline" })
            return
          }
          setLoad({ kind: "error", message: describeCodeError(changesResult.reason) })
          return
        }

        if (typeof repoId === "number") {
          if (indexResult.status === "rejected") {
            const error = indexResult.reason
            const missing = error instanceof ApiError && error.status === 404
            if (!missing) {
              setLoad({ kind: "error", message: describeCodeError(error) })
              return
            }
          }
          const run = indexResult.status === "fulfilled" ? indexResult.value : null
          if (run === null || run.status !== "done") {
            setLoad({ kind: "unindexed", run })
            return
          }
        }

        setLoad({ kind: "ready", changes: changesResult.value })
      } finally {
        if (requestRef.current === requestId) setRefreshing(false)
      }
    },
    [repoId, versionId],
  )

  useEffect(() => {
    void loadChanges()
    return () => {
      // Invalidate an in-flight change preview for this mount/version.
      requestRef.current += 1
    }
  }, [loadChanges])

  const handleCodeEvent = useCallback(() => {
    void loadChanges({ silent: true })
  }, [loadChanges])

  const analysisPolling = useAnalysisPolling(versionId, {
    enabled: repoId !== null,
    onCodeEvent: handleCodeEvent,
  })
  const analysis = analysisPolling.analysis

  const view = useMemo<CodeAnalysisView>(() => {
    if (analysis === null) return "none"
    if (analysis.status === "queued" || analysis.status === "running") return "running"
    if (analysis.status === "failed") return "failed"
    if (analysis.degraded) return "degraded"
    return "done"
  }, [analysis])

  // Index runs are polled only while the code tab stays open (spec §7.4).
  const indexRun = load.kind === "unindexed" ? load.run : null
  const indexActive = indexRun?.status === "queued" || indexRun?.status === "running"
  useEffect(() => {
    if (!indexActive || typeof repoId !== "number") return
    let stopped = false
    let attempts = 0
    const timer = setInterval(() => {
      if (stopped) return
      if (typeof document !== "undefined" && document.hidden) return
      if (attempts >= INDEX_POLL_MAX_ATTEMPTS) {
        clearInterval(timer)
        return
      }
      attempts += 1
      void getLatestIndexRun(repoId)
        .then((latest) => {
          if (stopped) return
          if (latest.status === "queued" || latest.status === "running") {
            setLoad((current) =>
              current.kind === "unindexed" ? { kind: "unindexed", run: latest } : current,
            )
          } else {
            void loadChanges({ silent: true })
          }
        })
        .catch(() => {
          // Keep the last known index state; the manual refresh stays available.
        })
    }, INDEX_POLL_INTERVAL_MS)
    return () => {
      stopped = true
      clearInterval(timer)
    }
  }, [indexActive, repoId, loadChanges])

  async function handleStartAnalysis(force: boolean): Promise<void> {
    setStarting(true)
    setActionError("")
    try {
      await startAnalysis(versionId, force ? { force: true } : {})
      await analysisPolling.refresh()
    } catch (caught) {
      if (codeErrorReason(caught) === "analysis_in_progress") {
        setActionError("该版本已有分析正在运行，已刷新最新状态。")
        await analysisPolling.refresh()
      } else {
        setActionError(describeCodeError(caught))
      }
    } finally {
      setStarting(false)
    }
  }

  function handleRefresh(): void {
    void loadChanges({ silent: true })
    void analysisPolling.refresh()
  }

  const effectiveRepoId = typeof repoId === "number" ? repoId : (analysis?.repoId ?? null)

  function handleOpenFile(file: ChangedFile): void {
    if (effectiveRepoId === null) {
      setActionError("无法确定代码仓库，暂不能查看文件 diff。")
      return
    }
    setActionError("")
    setOpenFile(file)
  }

  if (load.kind === "loading") {
    return (
      <div aria-busy="true" className="space-y-3">
        <p
          role="status"
          className="flex items-center gap-2 rounded-xl border border-dashed border-border px-4 py-10 text-sm text-muted-foreground"
        >
          <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
          正在加载代码变更…
        </p>
      </div>
    )
  }

  if (load.kind === "unbound") return <UnboundCodeState versionId={versionId} />
  if (load.kind === "noBaseline") return <NoBaselineCodeState versionId={versionId} />
  if (load.kind === "unindexed") {
    return (
      <UnindexedCodeState
        run={load.run}
        refreshing={refreshing}
        onRefresh={() => void loadChanges({ silent: true })}
      />
    )
  }
  if (load.kind === "error") {
    return <CodeLoadErrorState message={load.message} onRetry={() => void loadChanges()} />
  }

  const changes = load.changes

  return (
    <div className="space-y-4">
      <CommitHeader
        repoId={typeof repoId === "number" ? repoId : (analysis?.repoId ?? null)}
        baseCommit={changes.baseCommit}
        headCommit={changes.headCommit}
        analysis={analysis}
        view={view}
        exhausted={analysisPolling.exhausted}
        starting={starting}
        actionError={actionError}
        onStartAnalysis={(force) => {
          void handleStartAnalysis(force)
        }}
        onRefresh={handleRefresh}
      />

      {analysisPolling.error === null ? null : (
        <p
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
        >
          刷新分析状态失败：{describeCodeError(analysisPolling.error)}
        </p>
      )}

      {changes.files.length === 0 ? (
        <NoChangesCodeState
          baseCommit={changes.baseCommit}
          headCommit={changes.headCommit}
          refreshing={refreshing}
          onRefresh={() => void loadChanges({ silent: true })}
        />
      ) : (
        <div className="grid items-start gap-4 xl:grid-cols-2">
          <div className="space-y-4">
            <ChangedFiles changes={changes} onOpenFile={handleOpenFile} />
            {/* T11 mounts FactsPanel in this slot. */}
            <div data-testid="code-facts-slot" />
          </div>
          <div className="space-y-4">
            <AnalysisSummary analysis={analysis} view={view} />
            {/* T11 mounts ImpactPanel in this slot. */}
            <div data-testid="code-impact-slot" />
            {/* T11 mounts UnmappedChanges (and AnchorDialog) in this slot. */}
            <div data-testid="code-unmapped-slot" />
          </div>
        </div>
      )}

      {openFile !== null && effectiveRepoId !== null ? (
        <DiffDrawer
          repoId={effectiveRepoId}
          baseCommit={changes.baseCommit}
          headCommit={changes.headCommit}
          file={openFile}
          onClose={() => setOpenFile(null)}
        />
      ) : null}
    </div>
  )
}
