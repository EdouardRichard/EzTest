"use client"

import { Loader2 } from "lucide-react"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"

import { CodeChangesTab } from "@/features/code-intel/CodeChangesTab"
import { useReport } from "@/features/delivery/hooks/useReports"
import { BroadcastTab } from "@/features/execution/components/BroadcastTab"
import { EvidenceViewer } from "@/features/execution/components/EvidenceViewer"
import {
  type ExplorationDeepLink,
  type ExplorationFilters,
  ExplorationTab,
} from "@/features/execution/components/ExplorationTab"
import { ResultsTab } from "@/features/execution/components/ResultsTab"
import { SessionsTab } from "@/features/execution/components/SessionsTab"
import { useBatches, useExecutionActions } from "@/features/execution/hooks/useExecution"
import type { Batch, PendingTakeover } from "@/features/execution/types"
import { TakeoverDialog } from "@/features/resources/TakeoverDialog"
import { ApiError } from "@/lib/api-client"
import { getStoredUser } from "@/lib/auth-storage"

import { getVersion } from "./api"
import { OverviewTab } from "./OverviewTab"
import type { Version } from "./types"
import { useVersionStream } from "./useVersionStream"
import { VersionHeader } from "./VersionHeader"
import { isVersionTab, VersionTabs, type VersionTabValue } from "./VersionTabs"
import { Workbench } from "./Workbench"

const DEFAULT_TAB: VersionTabValue = "requirements"

/** The batch statuses that occupy the single active-batch slot (spec §5.1). */
const ACTIVE_BATCH_STATUSES = new Set(["planned", "running"])

/** Reads `?tab=` from the location; an explicit node deep link implies exploration. */
function readTabParam(): VersionTabValue {
  if (typeof window === "undefined") return DEFAULT_TAB
  const params = new URLSearchParams(window.location.search)
  const raw = params.get("tab")
  if (isVersionTab(raw)) return raw
  if ((params.get("node") ?? "") !== "") return "exploration"
  return DEFAULT_TAB
}

/** Reads the exploration filter deep links (`req/status/abnormal/q`). */
function readExplorationFilters(): ExplorationFilters {
  if (typeof window === "undefined") {
    return { requirementId: "", resultStatus: "", abnormalOnly: false, q: "" }
  }
  const params = new URLSearchParams(window.location.search)
  const abnormal = params.get("abnormal") ?? ""
  return {
    requirementId: params.get("req") ?? "",
    resultStatus: params.get("status") ?? "",
    abnormalOnly: abnormal === "1" || abnormal === "true",
    q: params.get("q") ?? "",
  }
}

/** Reads the `?node=<kind>:<id>` exploration Sheet deep link. */
function readExplorationNodeParam(): string {
  if (typeof window === "undefined") return ""
  return new URLSearchParams(window.location.search).get("node") ?? ""
}

/** Reads the `?batch=` deep link of the selected execution batch. */
function readBatchParam(): string {
  if (typeof window === "undefined") return ""
  return new URLSearchParams(window.location.search).get("batch") ?? ""
}

/** Reads the `?evidence=` deep link of the broadcast/results links. */
function readEvidenceParam(): string {
  if (typeof window === "undefined") return ""
  return new URLSearchParams(window.location.search).get("evidence") ?? ""
}

/** Reads the `?issue=` / `?report=` delivery deep links (spec §10.3/§10.4). */
function readDeepLinkParam(name: "issue" | "report"): string {
  if (typeof window === "undefined") return ""
  return new URLSearchParams(window.location.search).get(name) ?? ""
}

/** Writes one query parameter with replaceState, removing it for an empty value. */
function writeQueryParam(name: string, value: string): void {
  if (typeof window === "undefined") return
  const url = new URL(window.location.href)
  if (value === "") url.searchParams.delete(name)
  else url.searchParams.set(name, value)
  window.history.replaceState(null, "", `${url.pathname}${url.search}${url.hash}`)
}

/** Writes the active tab back with replaceState so the deep link survives refresh. */
function writeTabParam(value: VersionTabValue): void {
  writeQueryParam("tab", value)
}

function describeLoadError(error: unknown): string {
  if (!(error instanceof ApiError)) {
    return error instanceof Error ? error.message : "未知错误"
  }
  if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
  if (error.status === 404) return "版本不存在或已被删除。"
  if (error.status === 401) return "登录状态已失效，请重新登录。"
  return error.message
}

type RequirementsWorkbenchSlotProps = {
  version: Version
  onChanged: () => void
  /** Header 新增需求 intent: non-zero opens the create editor in the workbench. */
  openCreateToken: number
  onOpenCreateIntentConsumed: () => void
}

/**
 * Requirements tab content. T17 owns the workbench; T18 extends it with the
 * clarification column. `onChanged` refetches the version so the header
 * counters stay in sync after requirement writes.
 */
function RequirementsWorkbenchSlot({
  version,
  onChanged,
  openCreateToken,
  onOpenCreateIntentConsumed,
}: RequirementsWorkbenchSlotProps) {
  return (
    <Workbench
      version={version}
      onChanged={onChanged}
      openCreateToken={openCreateToken}
      onOpenCreateIntentConsumed={onOpenCreateIntentConsumed}
    />
  )
}

export type VersionDetailPageProps = {
  /** P3-owned ID; a decimal string from the dynamic route segment. */
  versionId: string
}

/**
 * Version detail container (spec §9.1/§9.2): loads the version, renders the
 * header with the P6 batch/pause controls, and hosts the nine-tab shell. The
 * five execution tabs share this container's batch selection and version
 * stream, evidence opens in one viewer owned here, and completing a human
 * takeover reuses the existing admin-only `TakeoverDialog`.
 */
export function VersionDetailPage({ versionId }: VersionDetailPageProps) {
  const [version, setVersion] = useState<Version | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState("")
  const [tab, setTab] = useState<VersionTabValue>(DEFAULT_TAB)
  const [evidenceId, setEvidenceId] = useState("")
  const [issueId, setIssueId] = useState("")
  const [reportId, setReportId] = useState("")
  const [createToken, setCreateToken] = useState(0)
  const [takeover, setTakeover] = useState<PendingTakeover | null>(null)
  const [selectedBatchId, setSelectedBatchId] = useState("")
  const [explorationFilters, setExplorationFilters] = useState<ExplorationFilters>({
    requirementId: "",
    resultStatus: "",
    abnormalOnly: false,
    q: "",
  })
  const [explorationNodeId, setExplorationNodeId] = useState("")
  const [role, setRole] = useState<string | undefined>(undefined)
  const requestRef = useRef(0)
  /** Highest stream seq already inspected for a version-level refetch. */
  const versionEventSeqRef = useRef<number | null>(null)

  const stream = useVersionStream(versionId)
  const batchPage = useBatches(versionId, {}, stream)
  const actions = useExecutionActions(versionId, { onChanged: batchPage.refresh })
  // Resolve a `?report=` deep link so the shared batch selection can follow the
  // report's own batch even when the newest batch is a different one.
  const deepLinkedReport = useReport(versionId, reportId, stream)

  const activeBatch = useMemo(
    () => batchPage.batches.find((batch) => ACTIVE_BATCH_STATUSES.has(batch.status)) ?? null,
    [batchPage.batches],
  )
  const selectedBatch = useMemo<Batch | null>(() => {
    if (selectedBatchId !== "") {
      const chosen = batchPage.batches.find((batch) => batch.id === selectedBatchId)
      if (chosen !== undefined) return chosen
    }
    return activeBatch ?? batchPage.batches[0] ?? null
  }, [batchPage.batches, selectedBatchId, activeBatch])

  const load = useCallback(async (): Promise<void> => {
    const requestId = ++requestRef.current
    setLoading(true)
    setError("")
    try {
      const data = await getVersion(versionId)
      if (requestRef.current === requestId) setVersion(data)
    } catch (caught) {
      if (requestRef.current === requestId) setError(describeLoadError(caught))
    } finally {
      if (requestRef.current === requestId) setLoading(false)
    }
  }, [versionId])

  useEffect(() => {
    setTab(readTabParam())
    setEvidenceId(readEvidenceParam())
    setIssueId(readDeepLinkParam("issue"))
    setReportId(readDeepLinkParam("report"))
    setExplorationFilters(readExplorationFilters())
    setExplorationNodeId(readExplorationNodeParam())
    setSelectedBatchId(readBatchParam())
    setRole(getStoredUser()?.role)
  }, [])

  // The report detail carries its batch id. Once it matches the selected deep
  // link, adopt that batch so the header selector, the execution tabs and the
  // report tab agree on the report's batch instead of the newest one.
  const deepLinkedReportBatchId =
    reportId !== "" && deepLinkedReport.report?.id === reportId
      ? deepLinkedReport.report.batchId
      : ""

  useEffect(() => {
    if (deepLinkedReportBatchId !== "") setSelectedBatchId(deepLinkedReportBatchId)
  }, [deepLinkedReportBatchId])

  useEffect(() => {
    void load()
    return () => {
      // Invalidate any request still in flight for this mount/version.
      requestRef.current += 1
    }
  }, [load])

  // Continuing transitions happen on the backend (creating a requirement on a
  // completed version, phase changes, another window's writes). The persisted
  // SSE event is the authoritative trigger for a header refetch; the first
  // snapshot is already covered by the initial load.
  useEffect(() => {
    const events = stream.events.filter(
      (event) => event.version_id === null || event.version_id === versionId,
    )
    if (events.length === 0) {
      versionEventSeqRef.current = null
      return
    }
    const maxSeq = events.reduce((latest, event) => Math.max(latest, event.seq), 0)
    const previousSeq = versionEventSeqRef.current
    versionEventSeqRef.current = maxSeq
    if (previousSeq === null && stream.status !== "live") return
    const changed = events.some(
      (event) =>
        event.seq > (previousSeq ?? -1) &&
        (event.type.startsWith("version.") || event.type === "requirement.added"),
    )
    if (changed) void load()
  }, [stream.events, stream.status, versionId, load])

  const refresh = useCallback(() => {
    void load()
  }, [load])

  function handleTabChange(value: VersionTabValue): void {
    setTab(value)
    writeTabParam(value)
  }

  function handleIssueChange(nextIssueId: string): void {
    setIssueId(nextIssueId)
    writeQueryParam("issue", nextIssueId)
  }

  function handleReportChange(nextReportId: string): void {
    setReportId(nextReportId)
    writeQueryParam("report", nextReportId)
  }

  function handleBatchChange(nextBatchId: string): void {
    setSelectedBatchId(nextBatchId)
    writeQueryParam("batch", nextBatchId)
    // A different batch cannot keep the previous batch's report deep link.
    setReportId("")
    writeQueryParam("report", "")
  }

  function handleExplorationFiltersChange(nextFilters: ExplorationFilters): void {
    setExplorationFilters(nextFilters)
    writeQueryParam("req", nextFilters.requirementId)
    writeQueryParam("status", nextFilters.resultStatus)
    writeQueryParam("abnormal", nextFilters.abnormalOnly ? "1" : "")
    writeQueryParam("q", nextFilters.q)
  }

  function handleExplorationNodeChange(nextNodeId: string): void {
    setExplorationNodeId(nextNodeId)
    writeQueryParam("node", nextNodeId)
  }

  function handleExplorationNavigate(target: ExplorationDeepLink): void {
    if (target.kind === "result") {
      handleTabChange("results")
      return
    }
    if (target.kind === "issue") {
      handleIssueChange(target.id)
      handleTabChange("issues")
      return
    }
    if (target.kind === "case") {
      if (typeof window !== "undefined") {
        window.location.assign(
          `/versions/${encodeURIComponent(versionId)}/mindmap?case=${encodeURIComponent(target.id)}`,
        )
      }
      return
    }
    handleTabChange("requirements")
  }

  function handleCompleteTakeover(next: PendingTakeover): void {
    setTakeover(next)
  }

  if (version === null) {
    return (
      <div className="space-y-4">
        {error === "" ? (
          <p className="flex items-center gap-2 rounded-xl border border-dashed border-border px-4 py-10 text-sm text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
            正在加载版本…
          </p>
        ) : (
          <div
            role="alert"
            className="space-y-3 rounded-xl border border-destructive/30 bg-destructive/10 px-4 py-6 text-sm text-destructive"
          >
            <p>加载版本失败：{error}</p>
            <button
              type="button"
              onClick={refresh}
              className="rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              重试
            </button>
          </div>
        )}
      </div>
    )
  }

  const batchId = selectedBatch?.id ?? ""

  return (
    <div className="space-y-4">
      <VersionHeader
        version={version}
        refreshing={loading}
        onRefresh={refresh}
        onUpdated={setVersion}
        onAddRequirement={() => {
          setCreateToken((previous) => previous + 1)
          handleTabChange("requirements")
        }}
        onViewChanges={() => handleTabChange("code")}
        onOpenClarification={() => handleTabChange("requirements")}
        execution={{
          batches: batchPage.batches,
          activeBatch,
          selectedBatchId: batchId,
          stream,
          actions,
          onBatchChange: handleBatchChange,
          onOpenReport: () => handleTabChange("report"),
          onOpenSessions: () => handleTabChange("sessions"),
          onOpenResults: () => handleTabChange("results"),
        }}
      />

      {error === "" ? null : (
        <p
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          刷新版本失败：{error}（正在展示上次数据）
        </p>
      )}

      <VersionTabs
        versionId={versionId}
        active={tab}
        onTabChange={handleTabChange}
        stream={stream}
        requirements={
          <RequirementsWorkbenchSlot
            version={version}
            onChanged={refresh}
            openCreateToken={createToken}
            onOpenCreateIntentConsumed={() => setCreateToken(0)}
          />
        }
        issueId={issueId}
        onIssueChange={handleIssueChange}
        reportId={reportId}
        onReportChange={handleReportChange}
        batchId={batchId}
        onBatchChange={handleBatchChange}
        overview={
          <OverviewTab
            version={version}
            batches={batchPage.batches}
            stream={stream}
            onOpenIssues={() => handleTabChange("issues")}
            onOpenCode={() => handleTabChange("code")}
            onOpenReports={(batchIdValue) => {
              // A batch entry supersedes any deep-linked report revision;
              // handleBatchChange also clears the stale `?report=`.
              handleBatchChange(batchIdValue)
              handleTabChange("report")
            }}
          />
        }
        sessions={
          <SessionsTab
            versionId={versionId}
            role={role}
            onOpenEvidence={setEvidenceId}
            onCompleteTakeover={handleCompleteTakeover}
          />
        }
        exploration={
          <ExplorationTab
            versionId={versionId}
            batchId={batchId}
            stream={stream}
            onOpenEvidence={setEvidenceId}
            onNavigate={handleExplorationNavigate}
            initialFilters={explorationFilters}
            onFiltersChange={handleExplorationFiltersChange}
            selectedNodeId={explorationNodeId}
            onSelectedNodeChange={handleExplorationNodeChange}
          />
        }
        broadcast={<BroadcastTab versionId={versionId} />}
        results={
          <ResultsTab
            versionId={versionId}
            batchId={batchId}
            batchNo={selectedBatch?.batch_no}
            stream={stream}
          />
        }
        code={<CodeChangesTab versionId={versionId} repoId={version.codeRepositoryId} />}
      />

      {evidenceId === "" ? null : (
        <EvidenceViewer
          versionId={versionId}
          evidenceId={evidenceId}
          onClose={() => setEvidenceId("")}
        />
      )}

      {takeover === null ? null : (
        <TakeoverDialog
          mode="complete"
          // P2's dialog still types the id as number; P6 serializes it as a
          // decimal string, so it is passed through verbatim (never Number()).
          takeoverId={takeover.id as unknown as number}
          onClose={() => setTakeover(null)}
          onDone={() => {
            setTakeover(null)
            void batchPage.refresh()
            void stream.reconnect()
          }}
        />
      )}
    </div>
  )
}
