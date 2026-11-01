"use client"

import { useCallback, useEffect, useRef, useState } from "react"

import { ErrorState } from "@/components/ui/ErrorState"
import { Select } from "@/components/ui/Select"
import { LoadingCard } from "@/components/ui/Spinner"
import { useIsAdmin } from "@/features/auth/useIsAdmin"
import { useVersionEvents } from "@/features/clarification/useVersionEvents"
import { createIntent, listCaseRuns, listWaves } from "@/features/execution/api"
import type { CaseRun, RunWave } from "@/features/execution/types"

import { getVisualOverview, listVisualAssertions, listVisualDiffs } from "./api"
import { VisualAssertionList } from "./components/VisualAssertionList"
import type { VisualAssertion, VisualDiff, VisualOverview } from "./types"
import { formatRatio } from "./types"

export type VisualViewProps = {
  versionId: string
  /** Deep link `?wave=` forwarded by the execution tab. */
  initialWaveId?: string
}

const REFRESH_DEBOUNCE_MS = 300

/**
 * Execution-tab visual sub-view (spec §9.3): grouped by wave/case run, showing
 * the four-state badges, baseline state, config summary and actual/diff entry
 * points. It consumes the P5 visual API plus the P3 wave/case-run projections
 * for grouping; admin-only baseline approval is hidden for normal users and the
 * server still enforces it.
 */
export function VisualView({ versionId, initialWaveId }: VisualViewProps) {
  const isAdmin = useIsAdmin()

  const [waves, setWaves] = useState<RunWave[]>([])
  const [caseRuns, setCaseRuns] = useState<CaseRun[]>([])
  const [assertions, setAssertions] = useState<VisualAssertion[]>([])
  const [diffs, setDiffs] = useState<VisualDiff[]>([])
  const [overview, setOverview] = useState<VisualOverview | null>(null)
  const [selectedWaveId, setSelectedWaveId] = useState<string | null>(initialWaveId ?? null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const selectedWaveIdRef = useRef<string | null>(initialWaveId ?? null)
  selectedWaveIdRef.current = selectedWaveId
  const refreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  const loadWorkspace = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const [nextWaves, nextAssertions, nextDiffs, nextOverview] = await Promise.all([
        listWaves(versionId),
        listVisualAssertions(versionId, {}),
        listVisualDiffs(versionId, { limit: 100 }),
        getVisualOverview(versionId),
      ])
      setWaves(nextWaves)
      setAssertions(nextAssertions)
      setDiffs(nextDiffs)
      setOverview(nextOverview)
      setSelectedWaveId((current) => {
        const preferred = initialWaveId ?? current
        if (preferred !== null && nextWaves.some((wave) => wave.id === preferred)) return preferred
        return nextWaves[0]?.id ?? null
      })
    } catch (cause) {
      setError(cause)
    } finally {
      setLoading(false)
    }
  }, [initialWaveId, versionId])

  useEffect(() => {
    void loadWorkspace()
  }, [loadWorkspace])

  useEffect(() => {
    if (selectedWaveId === null) {
      setCaseRuns([])
      return undefined
    }
    let cancelled = false
    void listCaseRuns(versionId, selectedWaveId)
      .then((items) => {
        if (!cancelled) setCaseRuns(items)
      })
      .catch(() => {
        // The workspace-level error already covers a failed wave projection.
      })
    return () => {
      cancelled = true
    }
  }, [selectedWaveId, versionId])

  const refresh = useCallback(async () => {
    try {
      const [nextAssertions, nextDiffs, nextOverview] = await Promise.all([
        listVisualAssertions(versionId, {}),
        listVisualDiffs(versionId, { limit: 100 }),
        getVisualOverview(versionId),
      ])
      setAssertions(nextAssertions)
      setDiffs(nextDiffs)
      setOverview(nextOverview)
      const waveId = selectedWaveIdRef.current
      if (waveId !== null) {
        try {
          setCaseRuns(await listCaseRuns(versionId, waveId))
        } catch {
          // Keep the last case-run snapshot; the next visual event retries.
        }
      }
    } catch {
      // A failed projection refresh must not blank the visual view.
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
    onVisualChanged: scheduleRefresh,
    onEvidenceCreated: scheduleRefresh,
    onCaseRunChanged: scheduleRefresh,
    onStreamReset: () => {
      void loadWorkspace()
    },
  })

  async function retest(caseRun: CaseRun): Promise<void> {
    setBusy(true)
    setNotice(null)
    try {
      await createIntent(versionId, {
        case_version_id: caseRun.case_version_id,
        purpose: "retest",
      })
      setNotice("已创建复测意图")
      await loadWorkspace()
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(false)
    }
  }

  if (loading) {
    return (
      <div data-testid="visual-view">
        <LoadingCard label="正在加载视觉工作区…" />
      </div>
    )
  }

  if (error !== null) {
    return (
      <div data-testid="visual-view">
        <ErrorState
          error={error}
          title="视觉工作区加载失败"
          onRetry={() => {
            void loadWorkspace()
          }}
        />
      </div>
    )
  }

  return (
    <div data-testid="visual-view" className="space-y-3">
      <header className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold text-foreground">视觉断言</h2>
          <p className="mt-0.5 text-[11px] text-muted-foreground">
            {sseStatus === "live" ? "版本通道已连接" : "版本通道重连中"} · 四态结果来自
            visual_diff；缺失基线不会计为失败。
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {waves.length > 0 ? (
            <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
              <span>波次</span>
              <Select
                aria-label="波次"
                value={selectedWaveId ?? ""}
                onChange={(event) => {
                  setSelectedWaveId(event.target.value === "" ? null : event.target.value)
                  setNotice(null)
                }}
                className="h-8 w-36 text-xs"
              >
                {waves.map((wave) => (
                  <option key={wave.id} value={wave.id}>
                    #{wave.id} · {wave.status}
                  </option>
                ))}
              </Select>
            </div>
          ) : null}
          <a
            href={`/quality?version=${encodeURIComponent(versionId)}`}
            className="text-[11px] text-primary underline-offset-2 hover:underline"
          >
            质量指标 →
          </a>
        </div>
      </header>

      {overview !== null ? (
        <dl
          data-testid="visual-overview-strip"
          className="flex flex-wrap items-center gap-x-4 gap-y-1 rounded-lg border border-border bg-card px-3 py-2 text-[11px] text-muted-foreground"
        >
          <span>基线覆盖 {`${overview.baseline_covered}/${overview.assertion_total}`}</span>
          <span>通过 {overview.passed}</span>
          <span>失败 {overview.failed}</span>
          <span>未定 {overview.inconclusive}</span>
          <span>阻塞 {overview.blocked}</span>
          <span>通过率 {formatRatio(overview.pass_ratio)}</span>
        </dl>
      ) : null}

      {notice !== null ? (
        <p role="status" className="text-[11px] text-success">
          {notice}
        </p>
      ) : null}

      {busy ? <LoadingCard label="正在创建复测…" /> : null}

      <VisualAssertionList
        versionId={versionId}
        caseRuns={caseRuns}
        assertions={assertions}
        diffs={diffs}
        isAdmin={isAdmin}
        onRetest={(caseRun) => {
          void retest(caseRun)
        }}
        onChanged={() => {
          void refresh()
        }}
      />
    </div>
  )
}
