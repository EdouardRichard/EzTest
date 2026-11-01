"use client"

import { useRouter, useSearchParams } from "next/navigation"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"

import { Alert } from "@/components/ui/Alert"
import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
import { LoadingCard, Spinner } from "@/components/ui/Spinner"
import { SSE_STATUS_LABEL } from "@/features/clarification/labels"
import { ApiError } from "@/lib/api-client"
import { formatDateTime } from "@/lib/format"

import {
  createReview,
  deleteAnchor,
  generateProposals,
  getAnalysis,
  getCodeChanges,
  latestAnalysis,
  listAnchors,
  listCodeRepos,
  listFacts,
  listProposals,
  listReviews,
  patchCandidate,
  readDiffFile,
  readFile,
  startAnalysis,
  triggerIndex,
  validateAnchor,
} from "./api"
import { AnchorsPanel } from "./components/AnchorsPanel"
import { ChangedFilesPanel } from "./components/ChangedFilesPanel"
import { DiffDrawer } from "./components/DiffDrawer"
import { FactReviewDialog } from "./components/FactReviewDialog"
import { FactsPanel } from "./components/FactsPanel"
import { ImpactPanel } from "./components/ImpactPanel"
import { IndexRunStatus } from "./components/IndexRunStatus"
import { ProposalPanel } from "./components/ProposalPanel"
import { RepositoryStatus } from "./components/RepositoryStatus"
import { UnmappedPanel } from "./components/UnmappedPanel"
import { shortCommit } from "./format"
import type {
  CodeAnalysisDetailView,
  CodeAnalysisStatus,
  CodeAnalysisView,
  CodeAnchorView,
  CodeChangedFileView,
  CodeChangeSetView,
  CodeFactReviewView,
  CodeFactView,
  CodeFileDiffView,
  CodeFileWindowView,
  CodeImpactCandidateView,
  CodeProposalResultView,
  CodeProposalView,
  CodeRepoStatusView,
  CreateCodeFactReviewInput,
} from "./types"
import { CODE_ANALYSIS_STATUS_LABELS } from "./types"
import { useCodeEvents } from "./useCodeEvents"

export type CodeTabProps = {
  versionId: string
}

const SSE_REFRESH_DEBOUNCE_MS = 350
const ANALYSIS_STATUS_TONE: Record<CodeAnalysisStatus, BadgeTone> = {
  queued: "warning",
  running: "info",
  done: "success",
  failed: "danger",
}

function analysisResultNotice(analysis: CodeAnalysisView): string {
  return analysis.status === "done" ? "分析已完成（复用）" : "分析运行已入队"
}

function isBaselineMissing(cause: unknown): boolean {
  return cause instanceof ApiError && cause.code === "baseline_commit_missing"
}

/**
 * P5 code & changes tab (spec §9.2): repository/index status, trigger actions,
 * changed files + on-demand patch drawer, T1 facts grouped by kind with fact
 * review, impact candidates with optimistic PATCH, unmapped changes, version
 * anchors and page/API proposals. All reads go through `./api` and the P0 SSE
 * client; the URL (`?tab=code&repo=&analysis=&candidate=&anchor=&fact=`) is the
 * deep-link source of truth and no global store is introduced.
 */
export function CodeTab({ versionId }: CodeTabProps) {
  const router = useRouter()
  const searchParams = useSearchParams()
  const urlRepo = searchParams.get("repo")
  const urlAnalysis = searchParams.get("analysis")
  const urlCandidate = searchParams.get("candidate")
  const urlAnchor = searchParams.get("anchor")
  const urlFact = searchParams.get("fact")

  const [repos, setRepos] = useState<CodeRepoStatusView[]>([])
  const [reposLoading, setReposLoading] = useState(true)
  const [reposError, setReposError] = useState<unknown>(null)
  const [selectedRepoId, setSelectedRepoId] = useState<string | null>(null)
  const [reloadToken, setReloadToken] = useState(0)

  const [analysisDetail, setAnalysisDetail] = useState<CodeAnalysisDetailView | null>(null)
  const [changes, setChanges] = useState<CodeChangeSetView | null>(null)
  const [workspaceLoading, setWorkspaceLoading] = useState(false)
  const [workspaceError, setWorkspaceError] = useState<unknown>(null)
  const [workspaceResolvedFor, setWorkspaceResolvedFor] = useState<string | null>(null)

  const [facts, setFacts] = useState<CodeFactView[]>([])
  const [factsHasMore, setFactsHasMore] = useState(false)
  const [factsLoading, setFactsLoading] = useState(false)
  const [factsError, setFactsError] = useState<unknown>(null)

  const [anchors, setAnchors] = useState<CodeAnchorView[]>([])
  const [anchorsLoading, setAnchorsLoading] = useState(false)
  const [anchorsError, setAnchorsError] = useState<unknown>(null)
  const [anchorsBusyId, setAnchorsBusyId] = useState<string | null>(null)

  const [proposals, setProposals] = useState<CodeProposalView[]>([])
  const [proposalsLoading, setProposalsLoading] = useState(false)
  const [proposalsError, setProposalsError] = useState<unknown>(null)
  const [proposalResult, setProposalResult] = useState<CodeProposalResultView | null>(null)
  const [generating, setGenerating] = useState(false)

  const [focusedFactId, setFocusedFactId] = useState<string | null>(null)
  const [focusedCandidateId, setFocusedCandidateId] = useState<string | null>(null)
  const [focusedAnchorId, setFocusedAnchorId] = useState<string | null>(null)
  const [pendingCandidates, setPendingCandidates] = useState<Record<string, boolean>>({})

  const [diffOpen, setDiffOpen] = useState(false)
  const [diffFile, setDiffFile] = useState<CodeChangedFileView | null>(null)
  const [diff, setDiff] = useState<CodeFileDiffView | null>(null)
  const [diffLoading, setDiffLoading] = useState(false)
  const [diffError, setDiffError] = useState<unknown>(null)

  const [fileWindow, setFileWindow] = useState<CodeFileWindowView | null>(null)
  const [fileWindowFact, setFileWindowFact] = useState<CodeFactView | null>(null)
  const [fileWindowLoading, setFileWindowLoading] = useState(false)
  const [fileWindowError, setFileWindowError] = useState<unknown>(null)

  const [reviewFact, setReviewFact] = useState<CodeFactView | null>(null)
  const [reviews, setReviews] = useState<CodeFactReviewView[]>([])
  const [reviewsLoading, setReviewsLoading] = useState(false)
  const [reviewsError, setReviewsError] = useState<unknown>(null)
  const [reviewSubmitting, setReviewSubmitting] = useState(false)
  const [reviewError, setReviewError] = useState<unknown>(null)

  const [indexBusy, setIndexBusy] = useState(false)
  const [analysisBusy, setAnalysisBusy] = useState(false)
  const [actionError, setActionError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)

  const selectedRepo = useMemo(
    () => repos.find((repo) => repo.repo_id === selectedRepoId) ?? null,
    [repos, selectedRepoId],
  )
  const baselineCommit = selectedRepo?.baseline_commit ?? null
  const analysis = analysisDetail?.analysis ?? null

  const changedFiles = useMemo(() => {
    if (analysisDetail !== null && analysisDetail.changed_files.length > 0) {
      return analysisDetail.changed_files
    }
    return changes?.files ?? []
  }, [analysisDetail, changes])

  const baseCommit = analysis?.base_commit_sha ?? changes?.base_commit_sha ?? baselineCommit ?? null
  const headCommit =
    analysis?.head_commit_sha ??
    changes?.head_commit_sha ??
    selectedRepo?.latest_index?.commit_sha ??
    null
  // Facts belong to a commit: wait for the analysis/change-set probe so the
  // first page is never loaded against a stale index commit, and only fall back
  // to the indexed commit once the workspace probe has settled.
  const factsCommit =
    analysis?.head_commit_sha ??
    changes?.head_commit_sha ??
    (workspaceResolvedFor === selectedRepoId
      ? (selectedRepo?.latest_index?.commit_sha ?? null)
      : null)
  const truncated =
    selectedRepo?.latest_index?.truncated === true ||
    changes?.truncated === true ||
    analysis?.stats?.truncated === true
  const analysisActive = analysis?.status === "queued" || analysis?.status === "running"
  const canTriggerAnalysis =
    selectedRepoId !== null && baselineCommit !== null && !analysisActive && !analysisBusy

  const refresh = useCallback(() => {
    setReloadToken((token) => token + 1)
  }, [])

  const updateUrl = useCallback(
    (updates: Record<string, string | null>) => {
      const params = new URLSearchParams(searchParams.toString())
      params.set("tab", "code")
      for (const [key, value] of Object.entries(updates)) {
        if (value === null || value.length === 0) params.delete(key)
        else params.set(key, value)
      }
      router.replace(`/versions/${encodeURIComponent(versionId)}?${params.toString()}`)
    },
    [router, searchParams, versionId],
  )

  /* ------------------------------ repositories ----------------------------- */
  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken forces a snapshot refetch
  useEffect(() => {
    let cancelled = false
    setReposLoading(true)
    void listCodeRepos(versionId)
      .then((next) => {
        if (cancelled) return
        setRepos(next)
        setReposError(null)
      })
      .catch((cause: unknown) => {
        if (!cancelled) setReposError(cause)
      })
      .finally(() => {
        if (!cancelled) setReposLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [versionId, reloadToken])

  // URL is the source of truth for the repository selection (spec §9.2).
  useEffect(() => {
    if (repos.length === 0) return
    setSelectedRepoId((current) => {
      if (urlRepo !== null && repos.some((repo) => repo.repo_id === urlRepo)) return urlRepo
      if (current !== null && repos.some((repo) => repo.repo_id === current)) return current
      return repos[0].repo_id
    })
  }, [repos, urlRepo])

  /* ------------------------- analysis / change set ------------------------- */
  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken forces a snapshot refetch
  useEffect(() => {
    if (selectedRepoId === null) {
      setAnalysisDetail(null)
      setChanges(null)
      setWorkspaceError(null)
      setWorkspaceResolvedFor(null)
      return undefined
    }
    let cancelled = false
    setWorkspaceLoading(true)
    setWorkspaceError(null)
    void (async () => {
      try {
        const latest = await latestAnalysis(versionId, selectedRepoId)
        const requestedId =
          urlAnalysis !== null && urlAnalysis.length > 0 ? urlAnalysis : (latest?.id ?? null)
        const detail = requestedId === null ? null : await getAnalysis(versionId, requestedId)
        if (cancelled) return
        setAnalysisDetail(detail)
        let preview: CodeChangeSetView | null = null
        if ((detail === null || detail.analysis.status !== "done") && baselineCommit !== null) {
          try {
            preview = await getCodeChanges(versionId, { repoId: selectedRepoId })
          } catch (cause) {
            // A missing baseline is already rendered as a warning; only surface
            // unexpected preview failures.
            if (!isBaselineMissing(cause)) throw cause
          }
        }
        if (!cancelled) setChanges(preview)
      } catch (cause) {
        if (!cancelled) {
          setAnalysisDetail(null)
          setChanges(null)
          setWorkspaceError(cause)
        }
      } finally {
        if (!cancelled) {
          setWorkspaceLoading(false)
          setWorkspaceResolvedFor(selectedRepoId)
        }
      }
    })()
    return () => {
      cancelled = true
    }
  }, [versionId, selectedRepoId, urlAnalysis, baselineCommit, reloadToken])

  /* ---------------------------------- facts -------------------------------- */
  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken forces a snapshot refetch
  useEffect(() => {
    if (selectedRepoId === null) {
      setFacts([])
      setFactsHasMore(false)
      setFactsError(null)
      return undefined
    }
    if (factsCommit === null) {
      // Keep the previous commit's facts while the workspace probe settles.
      return undefined
    }
    let cancelled = false
    setFactsLoading(true)
    setFactsError(null)
    void listFacts(selectedRepoId, { commit: factsCommit, limit: 100 })
      .then((page) => {
        if (cancelled) return
        setFacts(page.items)
        setFactsHasMore(page.hasMore)
      })
      .catch((cause: unknown) => {
        if (!cancelled) setFactsError(cause)
      })
      .finally(() => {
        if (!cancelled) setFactsLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [selectedRepoId, factsCommit, reloadToken])

  /* ------------------------------ anchors/proposals ------------------------ */
  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken forces a snapshot refetch
  useEffect(() => {
    let cancelled = false
    setAnchorsLoading(true)
    void listAnchors(versionId)
      .then((next) => {
        if (cancelled) return
        setAnchors(next)
        setAnchorsError(null)
      })
      .catch((cause: unknown) => {
        if (!cancelled) setAnchorsError(cause)
      })
      .finally(() => {
        if (!cancelled) setAnchorsLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [versionId, reloadToken])

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken forces a snapshot refetch
  useEffect(() => {
    let cancelled = false
    setProposalsLoading(true)
    void listProposals(versionId)
      .then((next) => {
        if (cancelled) return
        setProposals(next)
        setProposalsError(null)
      })
      .catch((cause: unknown) => {
        if (!cancelled) setProposalsError(cause)
      })
      .finally(() => {
        if (!cancelled) setProposalsLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [versionId, reloadToken])

  /* ------------------------------- deep links ------------------------------ */
  useEffect(() => setFocusedFactId(urlFact), [urlFact])
  useEffect(() => setFocusedCandidateId(urlCandidate), [urlCandidate])
  useEffect(() => setFocusedAnchorId(urlAnchor), [urlAnchor])

  /* ----------------------------------- SSE --------------------------------- */
  const refreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  const clearRefreshTimer = useCallback(() => {
    if (refreshTimer.current !== null) {
      clearTimeout(refreshTimer.current)
      refreshTimer.current = null
    }
  }, [])

  const refreshNow = useCallback(() => {
    clearRefreshTimer()
    setReloadToken((token) => token + 1)
  }, [clearRefreshTimer])

  const scheduleRefresh = useCallback(() => {
    clearRefreshTimer()
    refreshTimer.current = setTimeout(() => {
      refreshTimer.current = null
      setReloadToken((token) => token + 1)
    }, SSE_REFRESH_DEBOUNCE_MS)
  }, [clearRefreshTimer])

  useEffect(() => clearRefreshTimer, [clearRefreshTimer])

  const sseStatus = useCodeEvents(versionId, {
    onCodeChanged: scheduleRefresh,
    onStreamReset: refreshNow,
  })

  /* -------------------------------- actions -------------------------------- */
  function handleSelectRepo(repoId: string): void {
    if (repoId === selectedRepoId) return
    setSelectedRepoId(repoId)
    setAnalysisDetail(null)
    setChanges(null)
    setWorkspaceResolvedFor(null)
    setFileWindow(null)
    setFileWindowFact(null)
    setDiffOpen(false)
    setDiffFile(null)
    setDiff(null)
    updateUrl({ repo: repoId, analysis: null, candidate: null, fact: null })
  }

  async function handleTriggerIndex(): Promise<void> {
    if (selectedRepoId === null || indexBusy) return
    setIndexBusy(true)
    setActionError(null)
    setNotice(null)
    try {
      const run = await triggerIndex(versionId, { repoId: selectedRepoId })
      setNotice(run.status === "done" ? "索引已完成（复用）" : "索引运行已入队")
      setReloadToken((token) => token + 1)
    } catch (cause) {
      setActionError(cause)
    } finally {
      setIndexBusy(false)
    }
  }

  async function handleTriggerAnalysis(): Promise<void> {
    if (selectedRepoId === null || analysisBusy) return
    setAnalysisBusy(true)
    setActionError(null)
    setNotice(null)
    try {
      // A manual trigger on an existing analysis is a deliberate re-run
      // (degraded/failed/stale results); the API otherwise reuses base+head.
      const run = await startAnalysis(versionId, {
        repoId: selectedRepoId,
        force: analysis !== null,
      })
      setNotice(analysisResultNotice(run))
      updateUrl({ analysis: run.id })
      setReloadToken((token) => token + 1)
    } catch (cause) {
      setActionError(cause)
    } finally {
      setAnalysisBusy(false)
    }
  }

  async function handleSelectFile(file: CodeChangedFileView): Promise<void> {
    if (selectedRepoId === null || baseCommit === null || headCommit === null) return
    setDiffFile(file)
    setDiffOpen(true)
    setDiff(null)
    setDiffError(null)
    setDiffLoading(true)
    try {
      setDiff(
        await readDiffFile(selectedRepoId, {
          base: baseCommit,
          head: headCommit,
          path: file.path,
        }),
      )
    } catch (cause) {
      setDiffError(cause)
    } finally {
      setDiffLoading(false)
    }
  }

  function closeDiff(): void {
    setDiffOpen(false)
    setDiffFile(null)
    setDiff(null)
    setDiffError(null)
  }

  async function handleOpenFact(fact: CodeFactView): Promise<void> {
    setFocusedFactId(fact.id)
    updateUrl({ fact: fact.id })
    setFileWindowFact(fact)
    setFileWindow(null)
    setFileWindowError(null)
    setFileWindowLoading(true)
    try {
      setFileWindow(
        await readFile(fact.repo_id, {
          commit: fact.commit_sha,
          path: fact.path,
          startLine: fact.start_line ?? undefined,
          endLine: fact.end_line ?? undefined,
        }),
      )
    } catch (cause) {
      setFileWindowError(cause)
    } finally {
      setFileWindowLoading(false)
    }
  }

  async function loadReviews(fact: CodeFactView): Promise<void> {
    setReviews([])
    setReviewsError(null)
    setReviewsLoading(true)
    try {
      setReviews(await listReviews(versionId, fact.id))
    } catch (cause) {
      setReviewsError(cause)
    } finally {
      setReviewsLoading(false)
    }
  }

  function handleOpenReview(fact: CodeFactView): void {
    setReviewFact(fact)
    setReviewError(null)
    void loadReviews(fact)
  }

  async function handleSubmitReview(input: CreateCodeFactReviewInput): Promise<void> {
    if (reviewFact === null) return
    setReviewSubmitting(true)
    setReviewError(null)
    try {
      await createReview(versionId, reviewFact.id, input)
      setNotice("事实复核已记录")
      setReviewFact(null)
    } catch (cause) {
      setReviewError(cause)
    } finally {
      setReviewSubmitting(false)
    }
  }

  async function handleToggleCandidate(
    candidate: CodeImpactCandidateView,
    selected: boolean,
  ): Promise<void> {
    if (analysis === null) return
    const previous = candidate.selected
    setAnalysisDetail((current) =>
      current === null
        ? current
        : {
            ...current,
            candidates: current.candidates.map((entry) =>
              entry.id === candidate.id ? { ...entry, selected } : entry,
            ),
          },
    )
    setPendingCandidates((current) => ({ ...current, [candidate.id]: true }))
    setActionError(null)
    try {
      const updated = await patchCandidate(versionId, analysis.id, candidate.id, selected)
      setAnalysisDetail((current) =>
        current === null
          ? current
          : {
              ...current,
              candidates: current.candidates.map((entry) =>
                entry.id === candidate.id ? updated : entry,
              ),
            },
      )
    } catch (cause) {
      setAnalysisDetail((current) =>
        current === null
          ? current
          : {
              ...current,
              candidates: current.candidates.map((entry) =>
                entry.id === candidate.id ? { ...entry, selected: previous } : entry,
              ),
            },
      )
      setActionError(cause)
    } finally {
      setPendingCandidates((current) => {
        const next = { ...current }
        delete next[candidate.id]
        return next
      })
    }
  }

  async function handleValidateAnchor(anchorId: string): Promise<void> {
    setAnchorsBusyId(anchorId)
    setActionError(null)
    try {
      const updated = await validateAnchor(versionId, anchorId)
      setAnchors((current) => current.map((anchor) => (anchor.id === anchorId ? updated : anchor)))
      setNotice("锚点状态已更新")
    } catch (cause) {
      setActionError(cause)
    } finally {
      setAnchorsBusyId(null)
    }
  }

  async function handleDeleteAnchor(anchorId: string): Promise<void> {
    setAnchorsBusyId(anchorId)
    setActionError(null)
    try {
      await deleteAnchor(versionId, anchorId)
      setAnchors((current) => current.filter((anchor) => anchor.id !== anchorId))
      setNotice("锚点已删除")
    } catch (cause) {
      setActionError(cause)
    } finally {
      setAnchorsBusyId(null)
    }
  }

  async function handleGenerateProposals(): Promise<void> {
    if (generating) return
    setGenerating(true)
    setActionError(null)
    try {
      const result = await generateProposals(
        versionId,
        selectedRepoId !== null ? { repoId: selectedRepoId } : {},
      )
      setProposalResult(result)
      setProposals(await listProposals(versionId))
      setNotice(`已生成页面 ${result.pages.created} 个、API ${result.apis.created} 个`)
    } catch (cause) {
      setActionError(cause)
    } finally {
      setGenerating(false)
    }
  }

  /* --------------------------------- render -------------------------------- */
  if (reposLoading && repos.length === 0) {
    return <LoadingCard label="正在加载代码仓库…" />
  }

  if (repos.length === 0 && reposError !== null) {
    return <ErrorState error={reposError} title="代码仓库加载失败" onRetry={refresh} />
  }

  if (repos.length === 0) {
    return (
      <EmptyState
        title="未绑定代码仓库"
        description="该版本尚未绑定代码仓库，无法索引代码或分析变更。请在创建版本时为 SUT 绑定代码仓。"
        action={
          <Button variant="secondary" size="sm" onClick={refresh}>
            刷新状态
          </Button>
        }
      />
    )
  }

  const reviewHistory = reviews

  return (
    <div className="space-y-4">
      <RepositoryStatus
        repos={repos}
        selectedRepoId={selectedRepoId}
        baselineCommit={baselineCommit}
        busy={reposLoading}
        onSelectRepo={handleSelectRepo}
        onRefresh={refresh}
      />

      <div className="grid gap-3 md:grid-cols-2">
        <IndexRunStatus
          index={selectedRepo?.latest_index ?? null}
          baselineCommit={baselineCommit}
          indexInProgress={selectedRepo?.index_in_progress === true}
          busy={indexBusy}
          onTriggerIndex={() => void handleTriggerIndex()}
        />

        <section aria-label="变更分析" className="surface-card space-y-3 p-4">
          <div className="flex flex-wrap items-start justify-between gap-2">
            <div>
              <h2 className="text-sm font-semibold text-foreground">变更分析</h2>
              <p className="mt-0.5 font-mono text-[11px] text-muted-foreground">
                {baselineCommit !== null ? shortCommit(baseCommit) : "无基线"} →{" "}
                {headCommit !== null ? shortCommit(headCommit) : "未确定 head"}
              </p>
              {analysis !== null ? (
                <p className="mt-0.5 text-[11px] text-muted-foreground">
                  分析 #{analysis.id} · 更新于 {formatDateTime(analysis.created_at)}
                </p>
              ) : null}
            </div>
            <div className="flex flex-wrap items-center gap-2">
              {analysis !== null ? (
                <Badge tone={ANALYSIS_STATUS_TONE[analysis.status]}>
                  {CODE_ANALYSIS_STATUS_LABELS[analysis.status]}
                </Badge>
              ) : (
                <Badge tone="neutral">未分析</Badge>
              )}
              {analysis?.degraded === true ? <Badge tone="warning">AI 降级</Badge> : null}
              {truncated ? <Badge tone="warning">已截断</Badge> : null}
              <Button
                size="sm"
                onClick={() => void handleTriggerAnalysis()}
                disabled={!canTriggerAnalysis}
              >
                {analysisBusy ? (
                  <span aria-hidden="true">
                    <Spinner size="sm" label="分析中" />
                  </span>
                ) : null}
                触发分析
              </Button>
            </div>
          </div>

          {baselineCommit === null ? (
            <Alert tone="warning" title="无代码基线">
              先触发一次代码索引，成功后会自动回填基线 commit。
            </Alert>
          ) : null}

          {analysis?.degraded === true ? (
            <Alert tone="warning" title="AI 事实层已降级">
              确定性变更、锚点与影响仍可用；可稍后重新触发分析以补齐 AI 事实。
            </Alert>
          ) : null}

          {truncated ? (
            <Alert tone="warning" title="索引或变更集已截断">
              列表只展示上限内的数据，完整结果请缩小仓库 include 范围后重试。
            </Alert>
          ) : null}

          {analysis?.status === "failed" ? (
            <Alert tone="danger" title="分析失败">
              {analysis.error !== null && analysis.error !== undefined && analysis.error.length > 0
                ? analysis.error
                : "分析运行失败，请重试。"}
            </Alert>
          ) : null}

          {actionError !== null ? <InlineError error={actionError} /> : null}

          {notice !== null ? (
            <p role="status" className="text-[11px] text-success">
              {notice}
            </p>
          ) : null}

          <p className="text-[11px] text-muted-foreground">{SSE_STATUS_LABEL[sseStatus]}</p>
        </section>
      </div>

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2 xl:grid-cols-3">
        <div className="min-w-0">
          <ChangedFilesPanel
            files={changedFiles}
            truncated={truncated}
            loading={workspaceLoading}
            error={workspaceError}
            baseCommit={baseCommit}
            headCommit={headCommit}
            selectedPath={diffFile?.path ?? null}
            onSelectFile={(file) => void handleSelectFile(file)}
            onRetry={refresh}
          />
        </div>

        <div className="min-w-0">
          <FactsPanel
            facts={facts}
            hasMore={factsHasMore}
            loading={factsLoading || (workspaceLoading && facts.length === 0)}
            error={factsError}
            focusedFactId={focusedFactId}
            fileWindow={fileWindow}
            fileWindowFact={fileWindowFact}
            fileWindowLoading={fileWindowLoading}
            fileWindowError={fileWindowError}
            onOpenFile={(fact) => void handleOpenFact(fact)}
            onReview={handleOpenReview}
            onRetry={refresh}
            onRetryFileWindow={() => {
              if (fileWindowFact !== null) void handleOpenFact(fileWindowFact)
            }}
          />
        </div>

        <div className="min-w-0 space-y-4">
          <ImpactPanel
            candidates={analysisDetail?.candidates ?? []}
            pendingIds={pendingCandidates}
            focusedCandidateId={focusedCandidateId}
            degraded={analysis?.degraded === true}
            onToggleSelected={(candidate, selected) =>
              void handleToggleCandidate(candidate, selected)
            }
            onFocusCandidate={(candidateId) => {
              setFocusedCandidateId(candidateId)
              updateUrl({ candidate: candidateId })
            }}
          />
          <UnmappedPanel unmapped={analysisDetail?.unmapped ?? []} />
          <AnchorsPanel
            anchors={anchors}
            loading={anchorsLoading}
            error={anchorsError}
            busyAnchorId={anchorsBusyId}
            focusedAnchorId={focusedAnchorId}
            onValidate={(anchorId) => void handleValidateAnchor(anchorId)}
            onDelete={(anchorId) => void handleDeleteAnchor(anchorId)}
            onFocusAnchor={(anchorId) => {
              setFocusedAnchorId(anchorId)
              updateUrl({ anchor: anchorId })
            }}
            onRetry={refresh}
          />
          <ProposalPanel
            proposals={proposals}
            loading={proposalsLoading}
            error={proposalsError}
            generating={generating}
            result={proposalResult}
            onGenerate={() => void handleGenerateProposals()}
            onRetry={refresh}
          />
        </div>
      </div>

      <DiffDrawer
        open={diffOpen}
        file={diffFile}
        diff={diff}
        loading={diffLoading}
        error={diffError}
        onClose={closeDiff}
        onRetry={() => {
          if (diffFile !== null) void handleSelectFile(diffFile)
        }}
      />

      <FactReviewDialog
        fact={reviewFact}
        reviews={reviewHistory}
        reviewsLoading={reviewsLoading}
        reviewsError={reviewsError}
        submitting={reviewSubmitting}
        error={reviewError}
        onClose={() => setReviewFact(null)}
        onSubmit={(input) => void handleSubmitReview(input)}
        onRetryReviews={() => {
          if (reviewFact !== null) void loadReviews(reviewFact)
        }}
      />
    </div>
  )
}
