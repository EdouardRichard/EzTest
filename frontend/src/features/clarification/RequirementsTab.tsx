"use client"

import { useRouter } from "next/navigation"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { Button } from "@/components/ui/Button"
import { cn } from "@/components/ui/cn"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import type { ConversationModel } from "@/features/conversation/api"
import type { VersionStage } from "@/features/design/types"
import { getVersion } from "@/features/versions/api"
import type { Version } from "@/features/versions/types"
import { ApiError } from "@/lib/api-client"

import {
  confirmRequirements,
  confirmStageRequirements,
  createConversation,
  dedupeRequirements,
  deleteRequirement,
  getRequirement,
  getRequirementExtraction,
  listConversations,
  listRequirements,
  mergeRequirements,
  pauseRequirementExtraction,
  resumeRequirementExtraction,
  retryRequirementExtraction,
  startRequirementExtraction,
} from "./api"
import { BulkConfirmBar } from "./BulkConfirmBar"
import { ChatPanel } from "./ChatPanel"
import { DesignCTA } from "./DesignCTA"
import { RequirementCleanupBar } from "./RequirementCleanupBar"
import { RequirementEditor } from "./RequirementEditor"
import { RequirementSidebar } from "./RequirementSidebar"
import { TeamRoomPanel } from "./TeamRoomPanel"
import type {
  ConversationMessage,
  ConversationSummary,
  Requirement,
  RequirementDetail,
  RequirementExtractionProgress,
  RequirementFilter,
  TargetOptions,
  VersionClarificationStats,
} from "./types"
import { useVersionEvents } from "./useVersionEvents"

export type RequirementsTabProps = {
  versionId: string
  /** Deterministic version stage; drives the "开始设计" requirement gate. */
  stage?: VersionStage | null
  /** Refresh the page-level stage projection after a hard-gate transition. */
  onStageChanged?: () => void
  /** Deep link target from `?conversation=`; URL stays the source of truth. */
  initialConversationId?: string
  /** Deep link target from `?requirement=`; URL stays the source of truth. */
  initialRequirementId?: string
}

function buildWorkspaceUrl(
  versionId: string,
  conversationId: string | null,
  requirementId: string | null,
): string {
  const params = new URLSearchParams({ tab: "requirements" })
  if (conversationId !== null) params.set("conversation", conversationId)
  if (requirementId !== null) params.set("requirement", requirementId)
  return `/versions/${encodeURIComponent(versionId)}?${params.toString()}`
}

function activityText(payload: Record<string, unknown>): string | null {
  const status = typeof payload.status === "string" ? payload.status : ""
  if (status === "finished" || status === "failed" || status === "done") return null
  if (typeof payload.activity === "string" && payload.activity.length > 0) return payload.activity
  if (typeof payload.message === "string" && payload.message.length > 0) return payload.message
  return "AI 正在处理…"
}

/** The 409 start-design gate rejected because drafts are still unconfirmed. */
function isUnconfirmedRequirementsGate(error: unknown): boolean {
  return (
    error instanceof ApiError &&
    error.code === "stage_gate_failed" &&
    error.details.reason === "requirements_unconfirmed"
  )
}

/** Status labels of the requirement extraction card (API-5). */
const EXTRACTION_STATUS_LABELS: Record<string, string> = {
  pending: "待开始",
  running: "解析中",
  paused: "已暂停",
  done: "已完成",
  failed: "失败",
}

const EXTRACTION_SEGMENT_LABELS: Record<string, string> = {
  pending: "待处理",
  running: "处理中",
  done: "完成",
  failed: "失败",
  skipped: "跳过",
}

export type ExtractionAction = "start" | "pause" | "resume" | "retry" | "retry_segment"

export type ExtractionProgressCardProps = {
  /** Latest run of the version; null before the first extraction. */
  run: RequirementExtractionProgress | null
  loading: boolean
  /** Load failure of the progress read (never blocks the workspace itself). */
  error: unknown
  /** Typed action failure (400/404/409/503 from start/pause/resume/retry). */
  actionError: unknown
  notice: string | null
  busy: ExtractionAction | null
  onStart: () => void
  onPause: () => void
  onResume: () => void
  onRetry: () => void
  onRetrySegment: (seq: number) => void
}

/**
 * Requirement extraction progress card (v0.22 A §7.8/API-5/API-6/API-7).
 *
 * It is a pure projection of the latest `requirement_extraction_run`: status,
 * segment cursor, expected/extracted/missing counts, the persisted
 * reconciliation report and the start/pause/resume/retry actions. V2 planned ids
 * are rendered in their own partition and never listed as missing current ids.
 */
export function ExtractionProgressCard({
  run,
  loading,
  error,
  actionError,
  notice,
  busy,
  onStart,
  onPause,
  onResume,
  onRetry,
  onRetrySegment,
}: ExtractionProgressCardProps) {
  const status = run?.status ?? null
  const loadFailed = error !== null && run === null
  const canStart = !loadFailed && (run === null || status === "done" || status === "failed")
  const canPause = status === "pending" || status === "running"
  const canResume = status === "paused"
  const failedSegments = run?.segments.filter((segment) => segment.status === "failed") ?? []
  const canRetry = status === "failed" || (run?.failed_segments ?? 0) > 0
  const disabled = busy !== null
  const coveragePercent =
    run === null ? 0 : Math.min(100, Math.max(0, Math.round(run.coverage * 100)))
  const startLabel = run === null ? "开始抽取" : "重新抽取"

  return (
    <section
      data-testid="extraction-progress-card"
      aria-label="需求文档抽取"
      className="surface-card px-4 py-3"
    >
      <div className="flex flex-wrap items-start gap-2">
        <div className="min-w-0">
          <h2 className="text-sm font-semibold text-foreground">需求文档抽取</h2>
          <p className="mt-0.5 text-[11px] text-muted-foreground">
            逐段抽取需求，完成后自动对账；可暂停、继续与重试失败段。
          </p>
        </div>
        <div className="ml-auto flex flex-wrap items-center gap-1.5">
          {!loading && canStart ? (
            <Button data-testid="extraction-start" size="sm" disabled={disabled} onClick={onStart}>
              {startLabel}
            </Button>
          ) : null}
          {canPause ? (
            <Button
              data-testid="extraction-pause"
              size="sm"
              variant="secondary"
              disabled={disabled}
              onClick={onPause}
            >
              暂停
            </Button>
          ) : null}
          {canResume ? (
            <Button
              data-testid="extraction-resume"
              size="sm"
              disabled={disabled}
              onClick={onResume}
            >
              继续
            </Button>
          ) : null}
          {canRetry ? (
            <Button
              data-testid="extraction-retry"
              size="sm"
              variant="secondary"
              disabled={disabled}
              onClick={onRetry}
            >
              重试
            </Button>
          ) : null}
        </div>
      </div>

      {loading ? (
        <p data-testid="extraction-loading" className="mt-2 text-[11px] text-muted-foreground">
          正在加载抽取进度…
        </p>
      ) : null}

      {error !== null ? (
        <div className="mt-2">
          <InlineError error={error} />
        </div>
      ) : null}

      {!loading && run === null && error === null ? (
        <p
          data-testid="extraction-empty"
          className="mt-2 rounded-lg border border-dashed border-border px-3 py-2 text-[11px] text-muted-foreground"
        >
          尚未开始抽取需求文档。上传需求文档后点击「开始抽取」，系统会按 4–8k
          字符分段抽取并自动对账。
        </p>
      ) : null}

      {run !== null ? (
        <div className="mt-2.5 space-y-2">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px]">
            <span
              data-testid="extraction-status"
              className={cn(
                "rounded-full px-2 py-0.5 font-medium",
                run.status === "failed"
                  ? "bg-destructive/10 text-destructive"
                  : run.status === "done"
                    ? "bg-success/10 text-success"
                    : "bg-muted text-foreground",
              )}
            >
              {EXTRACTION_STATUS_LABELS[run.status] ?? run.status}
            </span>
            <span data-testid="extraction-segments" className="text-muted-foreground">
              分段 {run.done_segments}/{run.total_segments}
              {run.failed_segments > 0 ? `（失败 ${run.failed_segments}）` : ""}
            </span>
            <span data-testid="extraction-counts" className="text-foreground">
              已抽取 {run.extracted}
              {run.expected > 0 ? `/${run.expected}` : ""} 条
            </span>
            <span data-testid="extraction-coverage" className="text-muted-foreground">
              覆盖率 {coveragePercent}%
            </span>
            {run.error ? <span className="text-destructive">{run.error}</span> : null}
          </div>

          <div
            data-testid="extraction-coverage-bar"
            role="progressbar"
            aria-label="抽取覆盖率"
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={coveragePercent}
            className="h-1.5 overflow-hidden rounded-full bg-muted"
          >
            <div
              className={cn(
                "h-full rounded-full transition-[width]",
                run.status === "failed" ? "bg-destructive" : "bg-primary",
              )}
              style={{ width: `${coveragePercent}%` }}
            />
          </div>

          <div
            data-testid="extraction-metrics"
            className="flex flex-wrap gap-x-3 gap-y-1 text-[10.5px] text-muted-foreground"
          >
            <span>缺失 {run.missing}</span>
            <span>规划 {run.planned_count}</span>
            <span>低置信 {run.low_confidence}</span>
            <span>跳过图片 {run.skipped_images}</span>
          </div>

          {failedSegments.length > 0 ? (
            <div className="flex flex-wrap items-center gap-1.5 text-[10.5px] text-muted-foreground">
              <span>失败段：</span>
              {failedSegments.map((segment) => (
                <Button
                  key={segment.seq}
                  data-testid={`extraction-retry-segment-${segment.seq}`}
                  size="sm"
                  variant="ghost"
                  disabled={disabled}
                  onClick={() => onRetrySegment(segment.seq)}
                >
                  重试第 {segment.seq} 段
                </Button>
              ))}
            </div>
          ) : null}

          <fieldset
            data-testid="extraction-reconciliation"
            aria-label="抽取对账"
            className="grid gap-2 rounded-lg border border-border bg-muted/30 px-3 py-2 text-[11px] md:grid-cols-2"
          >
            <section data-testid="extraction-missing">
              <h3 className="font-medium text-foreground">缺失（当前版本）· {run.missing}</h3>
              {run.missing_ids.length === 0 ? (
                <p className="mt-0.5 text-muted-foreground">无缺失</p>
              ) : (
                <ul className="mt-0.5 flex flex-wrap gap-x-2 gap-y-0.5 text-muted-foreground">
                  {run.missing_ids.map((id) => (
                    <li key={id}>{id}</li>
                  ))}
                </ul>
              )}
            </section>
            <section data-testid="extraction-planned">
              <h3 className="font-medium text-foreground">
                V2 规划（不计入缺失）· {run.planned_count}
              </h3>
              {run.planned_ids.length === 0 ? (
                <p className="mt-0.5 text-muted-foreground">无规划需求</p>
              ) : (
                <ul className="mt-0.5 flex flex-wrap gap-x-2 gap-y-0.5 text-muted-foreground">
                  {run.planned_ids.map((id) => (
                    <li key={id}>{id}</li>
                  ))}
                </ul>
              )}
            </section>
          </fieldset>

          {run.segments.length > 0 ? (
            <div
              data-testid="extraction-segment-list"
              className="flex flex-wrap gap-1.5 text-[10.5px] text-muted-foreground"
            >
              {run.segments.map((segment) => (
                <span
                  key={segment.seq}
                  title={segment.error ?? segment.heading_path}
                  className={cn(
                    "rounded border border-border px-1.5 py-0.5",
                    segment.status === "failed" ? "text-destructive" : null,
                  )}
                >
                  #{segment.seq} {EXTRACTION_SEGMENT_LABELS[segment.status] ?? segment.status}
                </span>
              ))}
            </div>
          ) : null}
        </div>
      ) : null}

      {notice !== null ? (
        <p data-testid="extraction-notice" role="status" className="mt-2 text-[11px] text-success">
          {notice}
        </p>
      ) : null}
      {actionError !== null ? (
        <div className="mt-2">
          <InlineError error={actionError} />
        </div>
      ) : null}
    </section>
  )
}

/**
 * P1 "需求与澄清" workspace. Chat is the primary UI; the sidebar/editor handle
 * single and batch confirmation. The URL (`?tab=requirements&conversation=&requirement=`)
 * is the deep-link source of truth and the `version:{id}` SSE stream feeds live
 * messages, requirement refreshes and team-room updates.
 */
export function RequirementsTab({
  versionId,
  stage = null,
  onStageChanged,
  initialConversationId,
  initialRequirementId,
}: RequirementsTabProps) {
  const router = useRouter()

  const [conversations, setConversations] = useState<ConversationSummary[]>([])
  const [requirements, setRequirements] = useState<Requirement[]>([])
  const [version, setVersion] = useState<Version | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<unknown>(null)

  const [activeConversationId, setActiveConversationId] = useState<string | null>(
    initialConversationId ?? null,
  )
  const [selectedRequirementId, setSelectedRequirementId] = useState<string | null>(
    initialRequirementId ?? null,
  )
  const [detail, setDetail] = useState<RequirementDetail | null>(null)
  const [detailError, setDetailError] = useState<unknown>(null)
  const [detailToken, setDetailToken] = useState(0)

  const [filter, setFilter] = useState<RequirementFilter>("all")
  const [selectedIds, setSelectedIds] = useState<string[]>([])
  const [bulkConfirming, setBulkConfirming] = useState(false)
  const [bulkNotice, setBulkNotice] = useState<string | null>(null)
  const [bulkError, setBulkError] = useState<unknown>(null)
  // C-06: the editor is keyed by `current_version_id`, so a rollback/save
  // remounts it. The success notice must live here to survive that remount and
  // actually paint after `requirementUpdated` replaces the detail.
  const [editorNotice, setEditorNotice] = useState<string | null>(null)
  const [cleanupBusy, setCleanupBusy] = useState(false)
  const [cleanupNotice, setCleanupNotice] = useState<string | null>(null)
  const [cleanupError, setCleanupError] = useState<unknown>(null)
  const [startingDesign, setStartingDesign] = useState(false)
  const [designError, setDesignError] = useState<unknown>(null)

  const [creatingConversation, setCreatingConversation] = useState(false)
  const [createError, setCreateError] = useState<unknown>(null)
  const [liveMessages, setLiveMessages] = useState<ConversationMessage[]>([])
  const [activity, setActivity] = useState<string | null>(null)
  const [teamRoomToken, setTeamRoomToken] = useState(0)

  const [extraction, setExtraction] = useState<RequirementExtractionProgress | null>(null)
  const [extractionLoading, setExtractionLoading] = useState(true)
  const [extractionError, setExtractionError] = useState<unknown>(null)
  const [extractionActionError, setExtractionActionError] = useState<unknown>(null)
  const [extractionNotice, setExtractionNotice] = useState<string | null>(null)
  const [extractionBusy, setExtractionBusy] = useState<ExtractionAction | null>(null)
  // `extraction.*` SSE events can arrive once per segment; coalesce them into a
  // single progress read instead of one GET per event.
  const extractionEventTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  // The URL deep link seeds the initial conversation choice. Keep it in a ref so
  // `loadWorkspace` stays stable: depending on it directly made every in-page
  // URL sync (selecting a requirement/conversation) re-run the workspace load,
  // which unmounted the whole tab content for a loading card — dropping the chat
  // composer draft and clamping the document scroll to the top.
  const initialConversationIdRef = useRef(initialConversationId)
  initialConversationIdRef.current = initialConversationId

  const loadRequirements = useCallback(async (): Promise<void> => {
    setRequirements(await listRequirements(versionId))
  }, [versionId])

  const loadVersion = useCallback(async (): Promise<void> => {
    setVersion(await getVersion(versionId))
  }, [versionId])

  const loadWorkspace = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const [nextConversations, nextRequirements, nextVersion] = await Promise.all([
        listConversations(versionId),
        listRequirements(versionId),
        getVersion(versionId),
      ])
      setConversations(nextConversations)
      setRequirements(nextRequirements)
      setVersion(nextVersion)
      setActiveConversationId((current) => {
        if (current !== null && nextConversations.some((item) => item.id === current)) {
          return current
        }
        if (
          initialConversationIdRef.current !== undefined &&
          nextConversations.some((item) => item.id === initialConversationIdRef.current)
        ) {
          return initialConversationIdRef.current
        }
        return nextConversations[0]?.id ?? null
      })
    } catch (cause) {
      setError(cause)
    } finally {
      setLoading(false)
    }
  }, [versionId])

  useEffect(() => {
    void loadWorkspace()
  }, [loadWorkspace])

  /**
   * The extraction progress is an independent read: a typed 404 (no run yet) is
   * the card's empty state and any other failure only disables the card instead
   * of failing the whole clarification workspace.
   */
  const loadExtraction = useCallback(async (): Promise<void> => {
    setExtractionLoading(true)
    setExtractionError(null)
    try {
      setExtraction(await getRequirementExtraction(versionId))
    } catch (cause) {
      setExtractionError(cause)
    } finally {
      setExtractionLoading(false)
    }
  }, [versionId])

  useEffect(() => {
    void loadExtraction()
  }, [loadExtraction])

  useEffect(
    () => () => {
      if (extractionEventTimer.current !== null) clearTimeout(extractionEventTimer.current)
    },
    [],
  )

  // The extraction progress card is owned by this tab; the page-level SSE
  // subscription cannot reach it, so the tab consumes `extraction.*` itself.
  const scheduleExtractionRefresh = useCallback(() => {
    if (extractionEventTimer.current !== null) clearTimeout(extractionEventTimer.current)
    extractionEventTimer.current = setTimeout(() => {
      extractionEventTimer.current = null
      void loadExtraction()
    }, 350)
  }, [loadExtraction])

  /**
   * Runs one extraction action (start/pause/resume/retry) and reloads the
   * progress card. Server errors are typed ApiErrors and stay on the card as an
   * inline alert; they never unmount the workspace.
   */
  async function runExtractionAction(
    action: ExtractionAction,
    notice: string,
    request: () => Promise<unknown>,
  ): Promise<void> {
    if (extractionBusy !== null) return
    setExtractionBusy(action)
    setExtractionActionError(null)
    setExtractionNotice(null)
    try {
      await request()
      setExtractionNotice(notice)
      await loadExtraction()
    } catch (cause) {
      setExtractionActionError(cause)
    } finally {
      setExtractionBusy(null)
    }
  }

  // The route passes searchParams as props; keep internal state in sync when a
  // browser back/forward navigation replaces the deep links.
  useEffect(() => {
    if (initialConversationId !== undefined) setActiveConversationId(initialConversationId)
  }, [initialConversationId])

  useEffect(() => {
    setSelectedRequirementId(initialRequirementId ?? null)
  }, [initialRequirementId])

  // biome-ignore lint/correctness/useExhaustiveDependencies: detailToken re-fetches after confirm/rollback
  useEffect(() => {
    if (selectedRequirementId === null) {
      setDetail(null)
      setDetailError(null)
      return
    }
    let cancelled = false
    setDetailError(null)
    void getRequirement(versionId, selectedRequirementId).then(
      (next) => {
        if (!cancelled) setDetail(next)
      },
      (cause: unknown) => {
        if (!cancelled) setDetailError(cause)
      },
    )
    return () => {
      cancelled = true
    }
  }, [versionId, selectedRequirementId, detailToken])

  const sseStatus = useVersionEvents(versionId, {
    onConversationMessage: (message) => {
      setActivity(null)
      setLiveMessages((current) => {
        if (current.some((existing) => existing.id === message.id)) return current
        return [...current, message].slice(-200)
      })
    },
    onAgentActivity: (payload) => setActivity(activityText(payload)),
    onRequirementChanged: (event) => {
      void loadRequirements().catch(() => {})
      void loadVersion().catch(() => {})
      const changedId =
        typeof event.payload.requirement_id === "string" ? event.payload.requirement_id : undefined
      if (changedId !== undefined && changedId === selectedRequirementId) {
        setDetailToken((token) => token + 1)
      }
    },
    onTeamRoomChanged: () => setTeamRoomToken((token) => token + 1),
    onExtractionChanged: scheduleExtractionRefresh,
  })

  const targetOptions = useMemo<TargetOptions>(() => {
    if (version === null) return { suts: [], pages: [], apis: [] }
    const suts = new Map<string, string>()
    const pages = new Map<string, string>()
    const apis = new Map<string, string>()
    for (const binding of version.bindings) {
      suts.set(binding.sut.id, binding.sut.name)
      for (const page of binding.pages) pages.set(page.id, page.name)
      for (const api of binding.apis) apis.set(api.id, `${api.method} ${api.path_template}`)
    }
    const toOptions = (values: Map<string, string>) =>
      [...values.entries()].map(([id, label]) => ({ id, label }))
    return { suts: toOptions(suts), pages: toOptions(pages), apis: toOptions(apis) }
  }, [version])

  // Draft cleanup only ever sees draft selections; confirmed requirements keep
  // their own (confirm/rollback) paths and the server rejects them as 409 too.
  const draftRequirements = useMemo(
    () => requirements.filter((requirement) => requirement.status === "draft"),
    [requirements],
  )
  const selectedDrafts = useMemo(() => {
    const byId = new Map(draftRequirements.map((requirement) => [requirement.id, requirement]))
    return selectedIds
      .filter((id) => byId.has(id))
      .map((id) => ({ id, title: byId.get(id)?.title ?? id }))
  }, [draftRequirements, selectedIds])

  const clarifiedCount = requirements.filter(
    (requirement) => requirement.status === "clarified",
  ).length
  const stats = version as (Version & Partial<VersionClarificationStats>) | null
  const pendingDesignCount = stats?.pending_design_count ?? clarifiedCount

  function selectRequirement(requirementId: string): void {
    setSelectedRequirementId(requirementId)
    setBulkNotice(null)
    setEditorNotice(null)
    router.replace(buildWorkspaceUrl(versionId, activeConversationId, requirementId), {
      scroll: false,
    })
  }

  function selectConversation(conversationId: string): void {
    setActiveConversationId(conversationId)
    setActivity(null)
    router.replace(buildWorkspaceUrl(versionId, conversationId, selectedRequirementId), {
      scroll: false,
    })
  }

  async function createNewConversation(): Promise<void> {
    setCreatingConversation(true)
    setCreateError(null)
    try {
      const conversation = await createConversation(versionId, { kind: "clarification" })
      setConversations((current) => [
        conversation,
        ...current.filter((item) => item.id !== conversation.id),
      ])
      setActiveConversationId(conversation.id)
      setSelectedRequirementId(null)
      setDetail(null)
      setLiveMessages([])
      setActivity(null)
      router.replace(buildWorkspaceUrl(versionId, conversation.id, null), { scroll: false })
    } catch (cause) {
      setCreateError(cause)
    } finally {
      setCreatingConversation(false)
    }
  }

  function toggleSelection(requirementId: string): void {
    setSelectedIds((current) =>
      current.includes(requirementId)
        ? current.filter((id) => id !== requirementId)
        : [...current, requirementId],
    )
  }

  async function bulkConfirm(): Promise<void> {
    if (selectedIds.length === 0) return
    setBulkConfirming(true)
    setBulkError(null)
    setBulkNotice(null)
    try {
      const result = await confirmRequirements(versionId, selectedIds)
      setBulkNotice(
        `已确认 ${result.confirmed.length} 条，跳过 ${result.skipped.length} 条已确认需求`,
      )
      const selectedConfirmed = selectedIds.some((id) => result.confirmed.includes(id))
      setSelectedIds([])
      await Promise.all([loadRequirements(), loadVersion()])
      onStageChanged?.()
      if (selectedConfirmed && selectedRequirementId !== null) {
        setDetailToken((token) => token + 1)
      }
    } catch (cause) {
      setBulkError(cause)
    } finally {
      setBulkConfirming(false)
    }
  }

  /**
   * Draft cleanup (Task 83). Every action goes through the HTTP cleanup routes,
   * reports only what the server actually retired, and refreshes the list so a
   * rejected/partial request can never leave a stale "success" behind.
   */
  async function deleteSelectedDrafts(requirementIds: string[]): Promise<void> {
    if (requirementIds.length === 0) return
    setCleanupBusy(true)
    setCleanupError(null)
    setCleanupNotice(null)
    const deleted: string[] = []
    let failure: unknown = null
    try {
      const results = await Promise.allSettled(
        requirementIds.map((requirementId) => deleteRequirement(versionId, requirementId)),
      )
      results.forEach((result, index) => {
        if (result.status === "fulfilled") {
          deleted.push(requirementIds[index])
        } else if (failure === null) {
          failure = result.reason
        }
      })
      if (deleted.length > 0) {
        setSelectedIds((current) => current.filter((id) => !deleted.includes(id)))
      }
      await Promise.all([loadRequirements(), loadVersion()])
      if (deleted.length > 0) {
        setCleanupNotice(`已删除 ${deleted.length} 条草案（标记为 obsolete）`)
      }
      if (failure !== null) {
        setCleanupError(failure)
      }
      if (selectedRequirementId !== null && deleted.includes(selectedRequirementId)) {
        setSelectedRequirementId(null)
        setDetail(null)
        router.replace(buildWorkspaceUrl(versionId, activeConversationId, null), { scroll: false })
      }
    } catch (cause) {
      setCleanupError(failure ?? cause)
    } finally {
      setCleanupBusy(false)
    }
  }

  async function mergeSelectedDrafts(
    targetRequirementId: string,
    sourceRequirementIds: string[],
  ): Promise<void> {
    if (sourceRequirementIds.length === 0) return
    setCleanupBusy(true)
    setCleanupError(null)
    setCleanupNotice(null)
    try {
      const result = await mergeRequirements(versionId, targetRequirementId, sourceRequirementIds)
      const retired = new Set(result.merged_ids)
      setSelectedIds((current) => current.filter((id) => !retired.has(id)))
      await Promise.all([loadRequirements(), loadVersion()])
      if (selectedRequirementId !== null && retired.has(selectedRequirementId)) {
        setSelectedRequirementId(targetRequirementId)
        setDetailToken((token) => token + 1)
        router.replace(buildWorkspaceUrl(versionId, activeConversationId, targetRequirementId), {
          scroll: false,
        })
      } else if (selectedRequirementId === targetRequirementId) {
        setDetailToken((token) => token + 1)
      }
      setCleanupNotice(
        `已将 ${result.merged_ids.length} 条草案合并到「${result.requirement.title}」`,
      )
    } catch (cause) {
      setCleanupError(cause)
    } finally {
      setCleanupBusy(false)
    }
  }

  async function dedupeDrafts(): Promise<void> {
    setCleanupBusy(true)
    setCleanupError(null)
    setCleanupNotice(null)
    try {
      const result = await dedupeRequirements(versionId)
      const retired = new Set(result.obsolete_ids)
      if (retired.size > 0) {
        setSelectedIds((current) => current.filter((id) => !retired.has(id)))
        if (selectedRequirementId !== null && retired.has(selectedRequirementId)) {
          setSelectedRequirementId(null)
          setDetail(null)
          router.replace(buildWorkspaceUrl(versionId, activeConversationId, null), {
            scroll: false,
          })
        }
      }
      await Promise.all([loadRequirements(), loadVersion()])
      setCleanupNotice(
        result.obsolete_ids.length === 0
          ? "未发现重复草案"
          : `已去重 ${result.obsolete_ids.length} 条重复草案，保留 ${result.kept_ids.length} 条`,
      )
    } catch (cause) {
      setCleanupError(cause)
    } finally {
      setCleanupBusy(false)
    }
  }

  /**
   * Applies the requirement hard gate for the clarified requirements and moves
   * the viewer to the design workspace. This is what makes "开始设计" actually
   * advance `requirement_review -> designing` (Task 78); the previous deep link
   * left the version at the requirement gate while the design used to generate
   * cases at the wrong stage.
   */
  async function startDesign(): Promise<void> {
    const clarifiedIds = requirements
      .filter((requirement) => requirement.status === "clarified")
      .map((requirement) => requirement.id)
    if (clarifiedIds.length === 0) return
    setStartingDesign(true)
    setDesignError(null)
    try {
      // Recovery path: if a requirement confirmation did not converge the
      // version (older path, failed event), re-confirm the already-clarified
      // selections first so the hard gate below always has its source stage.
      if (stage === "clarifying" || stage === "created") {
        await confirmRequirements(versionId, clarifiedIds)
      }
      await confirmStageRequirements(versionId, clarifiedIds)
      await Promise.all([loadRequirements(), loadVersion()])
      onStageChanged?.()
      router.replace(`/versions/${encodeURIComponent(versionId)}?tab=design`)
    } catch (cause) {
      setDesignError(cause)
    } finally {
      setStartingDesign(false)
    }
  }

  function requirementUpdated(next: RequirementDetail): void {
    setEditorNotice(null)
    setDetail(next)
    void loadRequirements().catch(() => {})
    void loadVersion().catch(() => {})
    onStageChanged?.()
  }

  /**
   * Keep the workspace conversation summaries in sync after the header model
   * selector persists an override: the keyed selector is remounted when the
   * user switches conversations, so it must reseed from the updated summary.
   */
  function conversationModelChanged(updated: ConversationModel): void {
    setConversations((current) =>
      current.map((conversation) =>
        conversation.id === updated.id
          ? {
              ...conversation,
              model_override: updated.model_override,
              effective_model: updated.effective_model,
            }
          : conversation,
      ),
    )
  }

  if (loading) {
    return <LoadingCard label="正在加载澄清工作区…" />
  }

  if (error !== null) {
    return (
      <ErrorState
        error={error}
        title="澄清工作区加载失败"
        onRetry={() => {
          void loadWorkspace()
        }}
      />
    )
  }

  return (
    <div data-testid="requirements-tab" className="space-y-3">
      {createError !== null ? <InlineError error={createError} /> : null}

      <ExtractionProgressCard
        run={extraction}
        loading={extractionLoading}
        error={extractionError}
        actionError={extractionActionError}
        notice={extractionNotice}
        busy={extractionBusy}
        onStart={() => {
          void runExtractionAction("start", "已开始抽取需求文档", () =>
            startRequirementExtraction(versionId),
          )
        }}
        onPause={() => {
          if (extraction === null) return
          void runExtractionAction("pause", "已暂停抽取", () =>
            pauseRequirementExtraction(versionId, extraction.id),
          )
        }}
        onResume={() => {
          if (extraction === null) return
          void runExtractionAction("resume", "已继续抽取", () =>
            resumeRequirementExtraction(versionId, extraction.id),
          )
        }}
        onRetry={() => {
          if (extraction === null) return
          void runExtractionAction("retry", "已重试失败段", () =>
            retryRequirementExtraction(versionId, extraction.id),
          )
        }}
        onRetrySegment={(seq) => {
          if (extraction === null) return
          void runExtractionAction("retry_segment", `已重试第 ${seq} 段`, () =>
            retryRequirementExtraction(versionId, extraction.id, seq),
          )
        }}
      />

      <div
        data-testid="workspace-grid"
        className={cn("grid gap-3", "lg:grid-cols-[minmax(0,1fr)_400px]")}
      >
        <ChatPanel
          versionId={versionId}
          conversationId={activeConversationId}
          conversations={conversations}
          onSelectConversation={selectConversation}
          onCreateConversation={() => {
            void createNewConversation()
          }}
          creatingConversation={creatingConversation}
          onRequirementSelect={selectRequirement}
          onRequirementChanged={requirementUpdated}
          liveMessages={liveMessages}
          activity={activity}
          sseStatus={sseStatus}
          onConversationModelChanged={conversationModelChanged}
        />

        <div className="flex min-w-0 flex-col gap-3">
          <DesignCTA
            versionId={versionId}
            pendingCount={pendingDesignCount}
            stage={stage}
            starting={startingDesign}
            onStartDesign={() => {
              void startDesign()
            }}
          />

          {designError !== null ? (
            <InlineError error={designError}>
              {isUnconfirmedRequirementsGate(designError) ? (
                <button
                  type="button"
                  className="ml-2 underline underline-offset-2"
                  onClick={() => {
                    // The alert has done its job once the user jumps to the
                    // draft list; leaving it on top of the list hides the very
                    // requirements the user must confirm.
                    setDesignError(null)
                    setFilter("draft")
                  }}
                >
                  查看未确认需求
                </button>
              ) : null}
            </InlineError>
          ) : null}

          {bulkNotice !== null ? (
            <p
              data-testid="bulk-confirm-notice"
              role="status"
              className="rounded-lg border border-success/30 bg-success/10 px-3 py-2 text-[11px] text-success"
            >
              {bulkNotice}
            </p>
          ) : null}

          {detailError !== null ? <InlineError error={detailError} /> : null}

          <RequirementSidebar
            requirements={requirements}
            targetOptions={targetOptions}
            filter={filter}
            onFilterChange={setFilter}
            selectedIds={selectedIds}
            onToggleSelection={toggleSelection}
            activeRequirementId={selectedRequirementId}
            onSelectRequirement={selectRequirement}
            bulk={
              <>
                <BulkConfirmBar
                  selectedCount={selectedIds.length}
                  confirming={bulkConfirming}
                  error={bulkError}
                  onConfirm={() => {
                    void bulkConfirm()
                  }}
                />
                <RequirementCleanupBar
                  draftCount={draftRequirements.length}
                  selectedDrafts={selectedDrafts}
                  busy={cleanupBusy}
                  notice={cleanupNotice}
                  error={cleanupError}
                  onDelete={(requirementIds) => {
                    void deleteSelectedDrafts(requirementIds)
                  }}
                  onMerge={(targetRequirementId, sourceRequirementIds) => {
                    void mergeSelectedDrafts(targetRequirementId, sourceRequirementIds)
                  }}
                  onDedupe={() => {
                    void dedupeDrafts()
                  }}
                />
              </>
            }
            editor={
              detail !== null ? (
                <>
                  {editorNotice !== null ? (
                    <p
                      data-testid="requirement-notice"
                      role="status"
                      className="border-t border-border bg-card px-3 py-2 text-[11px] text-success"
                    >
                      {editorNotice}
                    </p>
                  ) : null}
                  <RequirementEditor
                    key={`${detail.id}-${detail.current_version_id}`}
                    versionId={versionId}
                    requirement={detail}
                    targetOptions={targetOptions}
                    onUpdated={requirementUpdated}
                    onNotice={setEditorNotice}
                  />
                </>
              ) : null
            }
          />

          <TeamRoomPanel versionId={versionId} refreshToken={teamRoomToken} />
        </div>
      </div>
    </div>
  )
}
