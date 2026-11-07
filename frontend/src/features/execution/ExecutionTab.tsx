"use client"

import { useRouter } from "next/navigation"
import { useCallback, useEffect, useRef, useState } from "react"

import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { Tabs } from "@/components/ui/Tabs"
import type { VersionEvent } from "@/features/clarification/types"
import { useVersionEvents } from "@/features/clarification/useVersionEvents"
import { VisualView } from "@/features/visual/VisualView"
import { ApiError } from "@/lib/api-client"

import {
  cancelHumanRequest,
  cancelWave,
  getCaseRun,
  getEvidence,
  getOverview,
  listCaseRuns,
  listHumanRequests,
  listWaves,
  pauseCaseRun,
  pauseWave,
  resolveHumanRequest,
  resumeCaseRun,
  resumeWave,
} from "./api"
import { EvidenceViewer } from "./EvidenceViewer"
import { ExecutionToolbar } from "./ExecutionToolbar"
import { HumanRequestBanner } from "./HumanRequestBanner"
import { LiveTrace } from "./LiveTrace"
import { nextActionLabel, terminalCauseLabel } from "./labels"
import { PauseDialog, type PauseInput, type PauseTarget } from "./PauseDialog"
import { PausedBanner } from "./PausedBanner"
import { ResultsPanel } from "./ResultsPanel"
import type {
  CaseRun,
  CaseRunDetail,
  Evidence,
  ExecutionOverview,
  ExecutionQuotas,
  HumanRequest,
  HumanRequestResponseInput,
  RunWave,
  WaveCounts,
} from "./types"
import { WaveList } from "./WaveList"

export type ExecutionSubView = "waves" | "visual"

export type ExecutionTabProps = {
  versionId: string
  /** Deep link `?wave=` forwarded by the version page. */
  initialWaveId?: string
  /** Deep link `?case_run=` forwarded by the version page. */
  initialCaseRunId?: string
  /**
   * Deep link `?evidence=` forwarded by the version page (Task 37 wiring).
   * When omitted the tab also reads the query string itself, so the link keeps
   * working while the page wiring is pending.
   */
  initialEvidenceId?: string
  /** Sub-view selector; `?view=visual` in the URL is read when omitted. */
  view?: ExecutionSubView
}

const REFRESH_DEBOUNCE_MS = 300

const SUB_VIEW_TABS = [
  { value: "waves", label: "波次" },
  { value: "visual", label: "视觉断言" },
]

function parseSubView(value: string | null | undefined): ExecutionSubView {
  return value === "visual" ? "visual" : "waves"
}

/** The version page owns the tab URL; this sub-view reads `?view=` itself. */
function readInitialSubView(view: ExecutionSubView | undefined): ExecutionSubView {
  if (view !== undefined) return view
  if (typeof window === "undefined") return "waves"
  return parseSubView(new URLSearchParams(window.location.search).get("view"))
}

/** `?evidence=` deep link (v0.22 FE-8); Task 37 forwards it as a prop. */
function readInitialEvidenceId(prop: string | undefined): string | null {
  if (typeof prop === "string" && prop.length > 0) return prop
  if (typeof window === "undefined") return null
  const value = new URLSearchParams(window.location.search).get("evidence")
  return value !== null && value.length > 0 ? value : null
}

const COUNT_STATUS_KEYS = [
  "pending",
  "running",
  "paused",
  "passed",
  "failed",
  "blocked",
  "inconclusive",
  "skipped",
  "interrupted",
  "superseded",
] as const

type CountStatusKey = (typeof COUNT_STATUS_KEYS)[number]

function emptyWaveCounts(): WaveCounts {
  return {
    total: 0,
    pending: 0,
    running: 0,
    paused: 0,
    passed: 0,
    failed: 0,
    blocked: 0,
    inconclusive: 0,
    skipped: 0,
    interrupted: 0,
    superseded: 0,
    evidence: 0,
    by_version_sut: {},
  }
}

/** Runtime-tolerant copy of a wave projection (`counts` may be absent/partial). */
function normalizeWaveCounts(counts: WaveCounts | null | undefined): WaveCounts {
  const normalized = emptyWaveCounts()
  if (counts === null || counts === undefined || typeof counts !== "object") return normalized
  for (const key of [...COUNT_STATUS_KEYS, "total", "evidence"] as const) {
    const value = counts[key]
    if (typeof value === "number" && Number.isFinite(value) && value >= 0) {
      normalized[key] = value
    }
  }
  if (counts.by_version_sut !== null && typeof counts.by_version_sut === "object") {
    normalized.by_version_sut = counts.by_version_sut
  }
  return normalized
}

function countsAreEmpty(counts: WaveCounts): boolean {
  return counts.total === 0 && COUNT_STATUS_KEYS.every((key) => counts[key] === 0)
}

/** Per-status counts from the loaded case-run rows (real persisted fields). */
function countsFromCaseRuns(caseRuns: CaseRun[]): WaveCounts {
  const counts = emptyWaveCounts()
  for (const caseRun of caseRuns) {
    counts.total += 1
    if ((COUNT_STATUS_KEYS as readonly string[]).includes(caseRun.status)) {
      counts[caseRun.status as CountStatusKey] += 1
    }
    if (typeof caseRun.evidence_count === "number" && caseRun.evidence_count > 0) {
      counts.evidence += caseRun.evidence_count
    }
  }
  return counts
}

/**
 * v0.22 audit §2.5-3: `run_wave.counts` is only recomputed on pause/terminal, so
 * a running wave can project all zeros. Replace exactly that all-zero
 * projection with the counts of the loaded case-run page; a partial non-zero
 * projection stays authoritative because the page may be paginated.
 */
function effectiveWaveCounts(wave: RunWave, caseRuns: CaseRun[]): WaveCounts {
  const server = normalizeWaveCounts(wave.counts)
  if (!countsAreEmpty(server) || caseRuns.length === 0) return server
  const derived = countsFromCaseRuns(caseRuns)
  const serverByVersionSut = server.by_version_sut ?? {}
  return {
    ...derived,
    // The loaded page can be paginated, so its aggregate must not erase a
    // server-provided evidence/by-SUT projection.
    evidence: server.evidence > 0 ? server.evidence : derived.evidence,
    by_version_sut:
      Object.keys(serverByVersionSut).length > 0 ? serverByVersionSut : derived.by_version_sut,
  }
}

function countsEqual(left: WaveCounts, right: WaveCounts): boolean {
  return left.total === right.total && COUNT_STATUS_KEYS.every((key) => left[key] === right[key])
}

function effectiveWave(wave: RunWave | null, caseRuns: CaseRun[]): RunWave | null {
  if (wave === null) return null
  return { ...wave, counts: effectiveWaveCounts(wave, caseRuns) }
}

/** Execution settings line (TTL/quota from the overview; Task 37 wires the rest). */
function formatSettingSeconds(value: number | undefined): string {
  if (typeof value !== "number" || !Number.isFinite(value) || value <= 0) return "不可用"
  return value % 60 === 0 ? `${value / 60} 分钟` : `${value} 秒`
}

function executionQuotaText(overview: ExecutionOverview | null): string {
  const quotas: ExecutionQuotas | null | undefined = overview?.quotas
  if (
    quotas === null ||
    quotas === undefined ||
    typeof quotas.global !== "number" ||
    typeof quotas.per_sut !== "number" ||
    typeof quotas.cross_system !== "number"
  ) {
    return "不可用"
  }
  return `全局 ${quotas.global} / 每 SUT ${quotas.per_sut} / 跨系统 ${quotas.cross_system}`
}

/** Stable `case_run.blocked` dispatch reasons (scheduler spec §5.6). */
const BLOCKED_EVENT_REASON_LABEL: Record<string, string> = {
  no_executor: "没有可用的执行器",
  executor_degraded: "执行器健康异常",
  executor_unavailable: "执行器不可用",
}

function blockedRunReason(caseRun: CaseRun): string {
  const lastError =
    typeof caseRun.summary.last_error === "string" ? caseRun.summary.last_error.trim() : ""
  if (lastError.length > 0) return lastError
  const verdictReason =
    typeof caseRun.summary.verdict_reason === "string" ? caseRun.summary.verdict_reason.trim() : ""
  if (verdictReason.length > 0) return terminalCauseLabel(verdictReason) ?? verdictReason
  return terminalCauseLabel(caseRun.summary.terminal_cause) ?? "阻塞原因未记录"
}

function blockedRunNextAction(caseRun: CaseRun): string {
  return (
    nextActionLabel(caseRun.summary.next_action) ?? "先解决阻塞条件（账号/环境/数据），再重跑用例"
  )
}

type BlockedEvent = {
  caseRunId: string
  reason: string
  action: string
}

/**
 * A dispatch-health `case_run.blocked` notice is only current while the run is
 * still waiting for an executor. Once a snapshot shows the run has started or
 * settled, the warning is stale (the blocked-case banner takes over a blocked
 * verdict); drop it instead of showing it forever.
 */
function reconcileBlockedEvent(
  event: BlockedEvent | null,
  nextCaseRuns: CaseRun[],
): BlockedEvent | null {
  if (event === null) return null
  const run = nextCaseRuns.find((caseRun) => caseRun.id === event.caseRunId)
  if (run === undefined) return event
  return run.status === "pending" ? event : null
}

/**
 * P3 execution workspace (spec §12.1): a three-column layout — wave history,
 * live case-run trace and results — under a toolbar, with the human-takeover
 * and paused banners pinned on top. Execution has no "start" button: confirmed
 * cases generate intents and waves on their own. The `version:{id}` SSE channel
 * only triggers a debounced snapshot refetch (spec §10.2); the UI never merges
 * step results locally.
 */
export function ExecutionTab({
  versionId,
  initialWaveId,
  initialCaseRunId,
  initialEvidenceId,
  view,
}: ExecutionTabProps) {
  const router = useRouter()
  const [subView, setSubView] = useState<ExecutionSubView>(() => readInitialSubView(view))

  const [overview, setOverview] = useState<ExecutionOverview | null>(null)
  const [waves, setWaves] = useState<RunWave[]>([])
  const [caseRuns, setCaseRuns] = useState<CaseRun[]>([])
  const [humanRequests, setHumanRequests] = useState<HumanRequest[]>([])
  const [selectedWaveId, setSelectedWaveId] = useState<string | null>(null)
  const [selectedCaseRunId, setSelectedCaseRunId] = useState<string | null>(null)
  const [detail, setDetail] = useState<CaseRunDetail | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)
  const [detailError, setDetailError] = useState<unknown>(null)
  const [detailToken, setDetailToken] = useState(0)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<unknown>(null)
  const [actionError, setActionError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [sutFilter, setSutFilter] = useState("all")
  const [pauseTarget, setPauseTarget] = useState<PauseTarget | null>(null)
  // The evidence deep link is read once on mount (prop wins over the URL) and
  // opens the immutable evidence dialog without touching the current selection.
  const [evidenceDeepLinkId] = useState<string | null>(() =>
    readInitialEvidenceId(initialEvidenceId),
  )
  const [deepLinkEvidence, setDeepLinkEvidence] = useState<Evidence | null>(null)
  const [evidenceNotice, setEvidenceNotice] = useState<string | null>(null)
  // `case_run.blocked` is a dispatch-health warning; snapshot-derived blocked
  // verdicts render through the blocked-case banner instead.
  const [blockedEvent, setBlockedEvent] = useState<BlockedEvent | null>(null)
  const [caseRunsLoading, setCaseRunsLoading] = useState(false)
  // The wave the loaded `caseRuns` belong to: a fallback aggregate must never
  // be derived from the previous wave's rows while a new wave is loading.
  const [caseRunsWaveId, setCaseRunsWaveId] = useState<string | null>(null)

  const refreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const selectedWaveIdRef = useRef<string | null>(null)
  const selectedCaseRunIdRef = useRef<string | null>(null)
  selectedWaveIdRef.current = selectedWaveId
  selectedCaseRunIdRef.current = selectedCaseRunId

  // Only login takeover pauses execution and gates the wave resume (server
  // `GetOpenByCaseRun` is login_takeover-only). The other human_request kinds —
  // clarification questions above all — are version todos and must never render
  // as a takeover or block a resume (R-B3-4). Approval/blocked_recovery/conflict
  // are decision cards, not pause owners: they are shown but do not gate resume.
  const pendingHumanRequests = humanRequests.filter((request) => request.status === "pending")
  const openHumanRequests = pendingHumanRequests.filter(
    (request) => request.kind === "login_takeover",
  )

  // Deep link `?evidence=<id>`: resolve the metadata by id (the evidence may
  // belong to any case run of the version, not just the selected one) and open
  // the viewer; a missing id becomes a non-fatal notice.
  useEffect(() => {
    if (evidenceDeepLinkId === null) return undefined
    let cancelled = false
    setEvidenceNotice(null)
    void getEvidence(versionId, evidenceDeepLinkId)
      .then((evidence) => {
        if (!cancelled) setDeepLinkEvidence(evidence)
      })
      .catch((cause: unknown) => {
        if (cancelled) return
        setDeepLinkEvidence(null)
        if (cause instanceof ApiError && cause.status === 404) {
          setEvidenceNotice(`未找到证据 #${evidenceDeepLinkId}，链接可能已失效。`)
        } else {
          setEvidenceNotice(`证据 #${evidenceDeepLinkId} 加载失败，请稍后重试。`)
        }
      })
    return () => {
      cancelled = true
    }
  }, [evidenceDeepLinkId, versionId])

  // Focus the case run that owns the deep-linked evidence when it is part of
  // the loaded wave; the URL is left untouched (no history rewrite on mount).
  useEffect(() => {
    if (deepLinkEvidence === null) return
    const ownerId = deepLinkEvidence.case_run_id
    if (ownerId.length === 0 || selectedCaseRunId === ownerId) return
    if (!caseRuns.some((caseRun) => caseRun.id === ownerId)) return
    setSelectedCaseRunId(ownerId)
  }, [deepLinkEvidence, caseRuns, selectedCaseRunId])

  const loadWorkspace = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const [nextOverview, nextWaves, nextHumanRequests] = await Promise.all([
        getOverview(versionId),
        listWaves(versionId),
        listHumanRequests(versionId, { status: "pending" }),
      ])
      setOverview(nextOverview)
      setWaves(nextWaves)
      setHumanRequests(nextHumanRequests)
      setSelectedWaveId(initialWaveId ?? nextOverview.active_wave?.id ?? nextWaves[0]?.id ?? null)
    } catch (cause) {
      setError(cause)
    } finally {
      setLoading(false)
    }
  }, [versionId, initialWaveId])

  useEffect(() => {
    void loadWorkspace()
  }, [loadWorkspace])

  // Load the case runs of the selected wave and keep a compatible selection.
  useEffect(() => {
    if (selectedWaveId === null) {
      setCaseRuns([])
      setCaseRunsWaveId(null)
      setSelectedCaseRunId(null)
      setDetail(null)
      setCaseRunsLoading(false)
      return undefined
    }
    let cancelled = false
    setCaseRunsLoading(true)
    void listCaseRuns(versionId, selectedWaveId)
      .then((nextCaseRuns) => {
        if (cancelled) return
        setCaseRuns(nextCaseRuns)
        setCaseRunsWaveId(selectedWaveId)
        setBlockedEvent((current) => reconcileBlockedEvent(current, nextCaseRuns))
        setSelectedCaseRunId((current) => {
          if (
            initialCaseRunId !== undefined &&
            nextCaseRuns.some((caseRun) => caseRun.id === initialCaseRunId)
          ) {
            return initialCaseRunId
          }
          if (current !== null && nextCaseRuns.some((caseRun) => caseRun.id === current)) {
            return current
          }
          return nextCaseRuns[0]?.id ?? null
        })
      })
      .catch((cause: unknown) => {
        if (!cancelled) setError(cause)
      })
      .finally(() => {
        if (!cancelled) setCaseRunsLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [versionId, selectedWaveId, initialCaseRunId])

  // Load the selected case-run snapshot (steps/assertions/evidence).
  // biome-ignore lint/correctness/useExhaustiveDependencies: detailToken refetches the same case run
  useEffect(() => {
    if (selectedCaseRunId === null) {
      setDetail(null)
      setDetailLoading(false)
      setDetailError(null)
      return undefined
    }
    let cancelled = false
    setDetailLoading(true)
    setDetailError(null)
    void getCaseRun(versionId, selectedCaseRunId)
      .then((nextDetail) => {
        if (!cancelled) setDetail(nextDetail)
      })
      .catch((cause: unknown) => {
        if (!cancelled) {
          setDetail(null)
          setDetailError(cause)
        }
      })
      .finally(() => {
        if (!cancelled) setDetailLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [versionId, selectedCaseRunId, detailToken])

  /** Debounced snapshot refetch shared by every execution SSE event. */
  const refresh = useCallback(async () => {
    try {
      const [nextOverview, nextWaves, nextHumanRequests] = await Promise.all([
        getOverview(versionId),
        listWaves(versionId),
        listHumanRequests(versionId, { status: "pending" }),
      ])
      setOverview(nextOverview)
      setWaves(nextWaves)
      setHumanRequests(nextHumanRequests)

      const waveId = selectedWaveIdRef.current
      if (waveId !== null) {
        try {
          const nextCaseRuns = await listCaseRuns(versionId, waveId)
          // The user may have switched waves while the fetch was in flight.
          if (selectedWaveIdRef.current === waveId) {
            setCaseRuns(nextCaseRuns)
            setCaseRunsWaveId(waveId)
            setBlockedEvent((current) => reconcileBlockedEvent(current, nextCaseRuns))
          }
        } catch {
          // Keep the last case-run snapshot; the next event retries.
        }
      }

      const caseRunId = selectedCaseRunIdRef.current
      if (caseRunId !== null) {
        try {
          setDetail(await getCaseRun(versionId, caseRunId))
        } catch {
          // Keep the last trace snapshot.
        }
      }
    } catch {
      // A failed projection refresh must not blank the workspace.
    }
  }, [versionId])

  const scheduleRefresh = useCallback(() => {
    if (refreshTimer.current !== null) clearTimeout(refreshTimer.current)
    refreshTimer.current = setTimeout(() => {
      refreshTimer.current = null
      void refresh()
    }, REFRESH_DEBOUNCE_MS)
  }, [refresh])

  useEffect(
    () => () => {
      if (refreshTimer.current !== null) clearTimeout(refreshTimer.current)
    },
    [],
  )

  /**
   * `case_run.blocked` (v0.22 FE-8, audit §2.5-6): the scheduler could not
   * dispatch a pending run because no healthy executor is available. The event
   * carries the stable reason/action; it is surfaced once and then the snapshot
   * is refetched. A pending login takeover only owns its own case run; the
   * per-run `showBlockedEvent` guard keeps the two surfaces from stacking
   * without swallowing unrelated runs' warnings.
   */
  const handleCaseRunChanged = useCallback(
    (event: VersionEvent) => {
      if (event.type === "case_run.blocked") {
        const caseRunId =
          typeof event.payload.case_run_id === "string" ? event.payload.case_run_id : ""
        const reason = typeof event.payload.reason === "string" ? event.payload.reason : ""
        const action = typeof event.payload.action === "string" ? event.payload.action : ""
        if (caseRunId.length > 0) setBlockedEvent({ caseRunId, reason, action })
      }
      scheduleRefresh()
    },
    [scheduleRefresh],
  )

  const handleExecutionChanged = useCallback(
    (event: VersionEvent) => {
      if (event.type === "execution.intent_superseded") {
        // Task 106: a selected confirmed case version left the executable set.
        // Re-planned replacements run automatically; without one the user must
        // reconfirm. Either way the drop is stated, never silent.
        if (event.payload.replanned === true) {
          setNotice("有已确认用例版本被更新的已确认版本取代，已自动重新排入执行队列。")
        } else if (event.payload.reason === "case_version_superseded") {
          setNotice("有已确认用例版本在执行前失效且尚未重新确认，未执行；请重新确认用例后再执行。")
        }
      }
      scheduleRefresh()
    },
    [scheduleRefresh],
  )

  const sseStatus = useVersionEvents(versionId, {
    // Agent-run lifecycle events change the job/wave projection even when no
    // wave event follows, so they join the same debounced snapshot refetch.
    onAgentRunChanged: scheduleRefresh,
    onWaveChanged: scheduleRefresh,
    onCaseRunChanged: handleCaseRunChanged,
    onEvidenceCreated: scheduleRefresh,
    onHumanRequestChanged: scheduleRefresh,
    onExecutionChanged: handleExecutionChanged,
  })

  const updateUrl = useCallback(
    (waveId: string | null, caseRunId: string | null, nextView: ExecutionSubView) => {
      const params = new URLSearchParams({ tab: "execution" })
      if (waveId !== null) params.set("wave", waveId)
      if (caseRunId !== null) params.set("case_run", caseRunId)
      if (nextView === "visual") params.set("view", "visual")
      router.replace(`/versions/${encodeURIComponent(versionId)}?${params.toString()}`)
    },
    [router, versionId],
  )

  const changeSubView = useCallback(
    (value: string) => {
      const next = parseSubView(value)
      setSubView(next)
      updateUrl(selectedWaveId, selectedCaseRunId, next)
    },
    [selectedCaseRunId, selectedWaveId, updateUrl],
  )

  const selectWave = useCallback(
    (waveId: string) => {
      setSelectedWaveId(waveId)
      setSelectedCaseRunId(null)
      setDetail(null)
      setNotice(null)
      updateUrl(waveId, null, subView)
    },
    [subView, updateUrl],
  )

  const selectCaseRun = useCallback(
    (caseRunId: string) => {
      setSelectedCaseRunId(caseRunId)
      updateUrl(selectedWaveId, caseRunId, subView)
    },
    [selectedWaveId, subView, updateUrl],
  )

  const runAction = useCallback(
    async (action: () => Promise<unknown>) => {
      setBusy(true)
      setActionError(null)
      setNotice(null)
      try {
        await action()
        await refresh()
      } catch (cause) {
        setActionError(cause)
      } finally {
        setBusy(false)
      }
    },
    [refresh],
  )

  const selectedWave =
    waves.find((wave) => wave.id === selectedWaveId) ??
    (overview?.active_wave?.id === selectedWaveId ? overview.active_wave : null)
  // v0.22 audit §2.5-3: a running wave may project all-zero counts; show the
  // counts derived from the loaded case runs instead. The merge is display-only
  // and never mutates the fetched snapshot. Only rows loaded for the selected
  // wave may feed the fallback: after a wave switch the previous wave's rows
  // must not keep its stale numbers (or fake an empty state) while the next
  // list is in flight.
  const loadedWaveCaseRuns = caseRunsWaveId === selectedWaveId ? caseRuns : []
  const displayWave = effectiveWave(selectedWave, loadedWaveCaseRuns)
  const displayWaves = waves.map((wave) =>
    wave.id === selectedWaveId ? (effectiveWave(wave, loadedWaveCaseRuns) ?? wave) : wave,
  )
  const countsFallbackActive =
    selectedWave !== null &&
    displayWave !== null &&
    !countsEqual(normalizeWaveCounts(selectedWave.counts), displayWave.counts)
  const showCountsEmpty =
    selectedWave !== null &&
    displayWave !== null &&
    caseRunsWaveId === selectedWaveId &&
    displayWave.counts.total === 0 &&
    loadedWaveCaseRuns.length === 0 &&
    !caseRunsLoading
  const pausedWave = selectedWave !== null && selectedWave.status === "paused" ? selectedWave : null
  const pausedCaseRun =
    caseRuns.find((caseRun) => caseRun.id === selectedCaseRunId && caseRun.status === "paused") ??
    null
  const takeoverCaseRunIds = new Set(
    openHumanRequests
      .map((request) => request.case_run_id)
      .filter((id): id is string => typeof id === "string" && id.length > 0),
  )
  // A blocked verdict (blocked_recovery/账号/环境) is a case-run state, not the
  // dispatch-health `case_run.blocked` event. Do not stack it on top of the
  // login-takeover banner that already owns the same case run.
  const blockedCaseRuns = caseRuns.filter(
    (caseRun) => caseRun.status === "blocked" && !takeoverCaseRunIds.has(caseRun.id),
  )
  const showBlockedEvent =
    blockedEvent !== null &&
    !takeoverCaseRunIds.has(blockedEvent.caseRunId) &&
    !caseRuns.some(
      (caseRun) => caseRun.id === blockedEvent.caseRunId && caseRun.status === "blocked",
    )

  const sutOptions = Array.from(
    new Set([
      ...caseRuns.flatMap((caseRun) => caseRun.version_sut_ids),
      ...Object.keys(displayWave?.counts.by_version_sut ?? {}),
    ]),
  ).sort()

  const visibleCaseRuns =
    sutFilter === "all"
      ? caseRuns
      : caseRuns.filter((caseRun) => caseRun.version_sut_ids.includes(sutFilter))

  async function handlePauseSubmit(input: PauseInput): Promise<void> {
    if (pauseTarget === null) return
    if (pauseTarget.kind === "wave") {
      await pauseWave(versionId, pauseTarget.id, input)
    } else {
      // The frozen contract only allows immediate for case-level pause (spec §5.7).
      await pauseCaseRun(versionId, pauseTarget.id, { mode: "immediate" })
    }
    await refresh()
  }

  function handleResumeWave(): void {
    if (selectedWave === null) return
    if (openHumanRequests.length > 0) {
      setNotice("请先处理待处理的人工请求，再恢复波次。")
      return
    }
    void runAction(() => resumeWave(versionId, selectedWave.id))
  }

  function handleResumeCaseRun(caseRun: CaseRun | null = pausedCaseRun): void {
    if (caseRun === null) return
    void runAction(() => resumeCaseRun(versionId, caseRun.id))
  }

  async function handleResolveHumanRequest(
    humanRequestId: string,
    response: HumanRequestResponseInput,
  ): Promise<void> {
    await resolveHumanRequest(humanRequestId, response)
    await refresh()
  }

  async function handleCancelHumanRequest(humanRequestId: string): Promise<void> {
    await cancelHumanRequest(humanRequestId)
    await refresh()
  }

  if (loading) {
    return (
      <div data-testid="execution-tab">
        <LoadingCard label="正在加载执行工作区…" />
      </div>
    )
  }

  if (error !== null) {
    return (
      <div data-testid="execution-tab">
        <ErrorState
          error={error}
          title="执行工作区加载失败"
          onRetry={() => {
            void loadWorkspace()
          }}
        />
      </div>
    )
  }

  return (
    <div data-testid="execution-tab" className="space-y-3">
      <Tabs
        items={SUB_VIEW_TABS}
        value={subView}
        onValueChange={changeSubView}
        ariaLabel="执行视图"
      />

      {pendingHumanRequests.length > 0 ? (
        <HumanRequestBanner
          requests={pendingHumanRequests}
          onResolve={handleResolveHumanRequest}
          onCancel={handleCancelHumanRequest}
        />
      ) : null}

      {evidenceNotice !== null ? (
        <p
          role="status"
          data-testid="evidence-deep-link-notice"
          className="rounded-md border border-warning/30 bg-warning/10 px-3 py-2 text-[11px] text-foreground"
        >
          {evidenceNotice}
        </p>
      ) : null}

      {showBlockedEvent && blockedEvent !== null ? (
        <p
          role="status"
          data-testid="case-run-blocked-event-notice"
          className="rounded-md border border-warning/30 bg-warning/10 px-3 py-2 text-[11px] text-foreground"
        >
          用例 #{blockedEvent.caseRunId} 无法调度：
          {BLOCKED_EVENT_REASON_LABEL[blockedEvent.reason] || blockedEvent.reason || "阻塞原因未知"}
          {blockedEvent.action.length > 0 ? `；${blockedEvent.action}` : ""}
        </p>
      ) : null}

      {subView === "visual" ? (
        <VisualView versionId={versionId} initialWaveId={selectedWaveId ?? undefined} />
      ) : (
        <>
          <div
            data-testid="execution-settings"
            className="surface-card flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2 text-[11px] text-muted-foreground"
          >
            <span className="font-medium text-foreground">执行设置</span>
            <span>
              人工请求 TTL {formatSettingSeconds(overview?.human_request_timeout_seconds)}
            </span>
            <span>并发配额 {executionQuotaText(overview)}</span>
            <span>用例超时：服务端设置（界面待接线）</span>
          </div>

          <ExecutionToolbar
            wave={displayWave}
            sseStatus={sseStatus}
            executor={overview?.executor ?? null}
            sutOptions={sutOptions}
            sutFilter={sutFilter}
            onSutFilterChange={setSutFilter}
            onPause={() => {
              if (selectedWave !== null) {
                setPauseTarget({
                  kind: "wave",
                  id: selectedWave.id,
                  title: `#${selectedWave.id}`,
                  alreadyPaused: selectedWave.status === "paused",
                })
              }
            }}
            onResume={handleResumeWave}
            onCancel={() => {
              if (selectedWave !== null) {
                void runAction(() => cancelWave(versionId, selectedWave.id))
              }
            }}
            busy={busy}
            actionError={actionError}
            notice={notice}
          />

          {displayWave !== null && countsFallbackActive ? (
            <div
              data-testid="execution-counts"
              className="flex flex-wrap items-center gap-x-3 gap-y-1 px-1 text-[11px] text-muted-foreground"
            >
              <span className="font-medium text-foreground">实时统计</span>
              <span>未定 {displayWave.counts.inconclusive}</span>
              {displayWave.counts.skipped > 0 ? (
                <span>已跳过 {displayWave.counts.skipped}</span>
              ) : null}
              {displayWave.counts.evidence > 0 ? (
                <span>证据 {displayWave.counts.evidence}</span>
              ) : null}
            </div>
          ) : null}

          {showCountsEmpty ? (
            <p
              data-testid="execution-counts-empty"
              className="px-1 text-[11px] text-muted-foreground"
            >
              暂无执行数据（尚无用例结果，不计为通过）。
            </p>
          ) : null}

          <PausedBanner
            wave={pausedWave}
            caseRun={pausedCaseRun}
            onResumeWave={handleResumeWave}
            onResumeCaseRun={() => handleResumeCaseRun()}
            busy={busy}
          />

          {blockedCaseRuns.length > 0 ? (
            <section
              aria-label="阻塞用例"
              data-testid="blocked-case-run-banner"
              className="surface-card space-y-1 px-3 py-2"
            >
              {blockedCaseRuns.map((caseRun) => (
                <p
                  key={caseRun.id}
                  role="status"
                  data-testid={`blocked-case-run-${caseRun.id}`}
                  className="text-[11px] leading-relaxed"
                >
                  <span className="font-medium text-warning">用例「{caseRun.title}」阻塞：</span>
                  <span className="text-foreground">{blockedRunReason(caseRun)}</span>
                  <span className="text-muted-foreground">
                    ；下一步：{blockedRunNextAction(caseRun)}
                  </span>
                </p>
              ))}
            </section>
          ) : null}

          <div className="grid gap-3 lg:grid-cols-[300px_minmax(0,1fr)_390px]">
            <WaveList
              waves={displayWaves}
              selectedWaveId={selectedWaveId}
              activeWaveId={overview?.active_wave?.id ?? null}
              onSelect={selectWave}
            />
            <LiveTrace
              wave={displayWave}
              caseRun={caseRuns.find((caseRun) => caseRun.id === selectedCaseRunId) ?? null}
              detail={detail}
              loading={detailLoading}
              error={detailError}
              onRetry={() => setDetailToken((token) => token + 1)}
            />
            <ResultsPanel
              versionId={versionId}
              caseRuns={visibleCaseRuns}
              selectedCaseRunId={selectedCaseRunId}
              onSelect={selectCaseRun}
              onPause={(caseRun) =>
                setPauseTarget({ kind: "case_run", id: caseRun.id, title: caseRun.title })
              }
              onResume={(caseRun) => handleResumeCaseRun(caseRun)}
              onRerunComplete={() => {
                void refresh()
              }}
              onOpenRequirement={(requirementId) => {
                router.push(
                  `/versions/${encodeURIComponent(versionId)}?tab=requirements&requirement=${encodeURIComponent(requirementId)}`,
                )
              }}
              detail={detail}
              detailLoading={detailLoading}
              detailError={detailError}
              onRetryDetail={() => setDetailToken((token) => token + 1)}
            />
          </div>
        </>
      )}

      <PauseDialog
        open={pauseTarget !== null}
        target={pauseTarget}
        onClose={() => setPauseTarget(null)}
        onSubmit={handlePauseSubmit}
      />

      <EvidenceViewer
        versionId={versionId}
        evidence={deepLinkEvidence}
        open={deepLinkEvidence !== null}
        onClose={() => setDeepLinkEvidence(null)}
      />
    </div>
  )
}
