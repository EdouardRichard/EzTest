"use client"

import { useRouter } from "next/navigation"
import { useCallback, useEffect, useRef, useState } from "react"

import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { useVersionEvents } from "@/features/clarification/useVersionEvents"

import {
  cancelHumanRequest,
  cancelWave,
  getCaseRun,
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
import { ExecutionToolbar } from "./ExecutionToolbar"
import { HumanRequestBanner } from "./HumanRequestBanner"
import { LiveTrace } from "./LiveTrace"
import { PauseDialog, type PauseInput, type PauseTarget } from "./PauseDialog"
import { PausedBanner } from "./PausedBanner"
import { ResultsPanel } from "./ResultsPanel"
import type {
  CaseRun,
  CaseRunDetail,
  ExecutionOverview,
  HumanRequest,
  HumanRequestResponseInput,
  RunWave,
} from "./types"
import { WaveList } from "./WaveList"

export type ExecutionTabProps = {
  versionId: string
  /** Deep link `?wave=` forwarded by the version page. */
  initialWaveId?: string
  /** Deep link `?case_run=` forwarded by the version page. */
  initialCaseRunId?: string
}

const REFRESH_DEBOUNCE_MS = 300

/**
 * P3 execution workspace (spec §12.1): a three-column layout — wave history,
 * live case-run trace and results — under a toolbar, with the human-takeover
 * and paused banners pinned on top. Execution has no "start" button: confirmed
 * cases generate intents and waves on their own. The `version:{id}` SSE channel
 * only triggers a debounced snapshot refetch (spec §10.2); the UI never merges
 * step results locally.
 */
export function ExecutionTab({ versionId, initialWaveId, initialCaseRunId }: ExecutionTabProps) {
  const router = useRouter()

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

  const refreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const selectedWaveIdRef = useRef<string | null>(null)
  const selectedCaseRunIdRef = useRef<string | null>(null)
  selectedWaveIdRef.current = selectedWaveId
  selectedCaseRunIdRef.current = selectedCaseRunId

  const loadWorkspace = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const [nextOverview, nextWaves, nextHumanRequests] = await Promise.all([
        getOverview(versionId),
        listWaves(versionId),
        listHumanRequests(versionId, { status: "open" }),
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
      setSelectedCaseRunId(null)
      setDetail(null)
      return undefined
    }
    let cancelled = false
    void listCaseRuns(versionId, selectedWaveId)
      .then((nextCaseRuns) => {
        if (cancelled) return
        setCaseRuns(nextCaseRuns)
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
        listHumanRequests(versionId, { status: "open" }),
      ])
      setOverview(nextOverview)
      setWaves(nextWaves)
      setHumanRequests(nextHumanRequests)

      const waveId = selectedWaveIdRef.current
      if (waveId !== null) {
        try {
          setCaseRuns(await listCaseRuns(versionId, waveId))
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

  const sseStatus = useVersionEvents(versionId, {
    onWaveChanged: scheduleRefresh,
    onCaseRunChanged: scheduleRefresh,
    onEvidenceCreated: scheduleRefresh,
    onHumanRequestChanged: scheduleRefresh,
    onExecutionChanged: scheduleRefresh,
  })

  const updateUrl = useCallback(
    (waveId: string | null, caseRunId: string | null) => {
      const params = new URLSearchParams({ tab: "execution" })
      if (waveId !== null) params.set("wave", waveId)
      if (caseRunId !== null) params.set("case_run", caseRunId)
      router.replace(`/versions/${encodeURIComponent(versionId)}?${params.toString()}`)
    },
    [router, versionId],
  )

  const selectWave = useCallback(
    (waveId: string) => {
      setSelectedWaveId(waveId)
      setSelectedCaseRunId(null)
      setDetail(null)
      setNotice(null)
      updateUrl(waveId, null)
    },
    [updateUrl],
  )

  const selectCaseRun = useCallback(
    (caseRunId: string) => {
      setSelectedCaseRunId(caseRunId)
      updateUrl(selectedWaveId, caseRunId)
    },
    [updateUrl, selectedWaveId],
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
  const pausedWave = selectedWave !== null && selectedWave.status === "paused" ? selectedWave : null
  const pausedCaseRun =
    caseRuns.find((caseRun) => caseRun.id === selectedCaseRunId && caseRun.status === "paused") ??
    null
  const openHumanRequests = humanRequests.filter((request) => request.status === "open")

  const sutOptions = Array.from(
    new Set([
      ...caseRuns.flatMap((caseRun) => caseRun.version_sut_ids),
      ...Object.keys(selectedWave?.counts.by_version_sut ?? {}),
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
      <ExecutionToolbar
        wave={selectedWave}
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

      {openHumanRequests.length > 0 ? (
        <HumanRequestBanner
          requests={openHumanRequests}
          onResolve={handleResolveHumanRequest}
          onCancel={handleCancelHumanRequest}
        />
      ) : null}

      <PausedBanner
        wave={pausedWave}
        caseRun={pausedCaseRun}
        onResumeWave={handleResumeWave}
        onResumeCaseRun={() => handleResumeCaseRun()}
        busy={busy}
      />

      <div className="grid gap-3 lg:grid-cols-[300px_minmax(0,1fr)_390px]">
        <WaveList
          waves={waves}
          selectedWaveId={selectedWaveId}
          activeWaveId={overview?.active_wave?.id ?? null}
          onSelect={selectWave}
        />
        <LiveTrace
          wave={selectedWave}
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
          detail={detail}
          detailLoading={detailLoading}
          detailError={detailError}
          onRetryDetail={() => setDetailToken((token) => token + 1)}
        />
      </div>

      <PauseDialog
        open={pauseTarget !== null}
        target={pauseTarget}
        onClose={() => setPauseTarget(null)}
        onSubmit={handlePauseSubmit}
      />
    </div>
  )
}
