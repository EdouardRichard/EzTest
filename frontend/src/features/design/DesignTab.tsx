"use client"

import { ChevronDown, Maximize2, Minimize2 } from "lucide-react"
import { useRouter } from "next/navigation"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"

import { Button } from "@/components/ui/Button"
import { cn } from "@/components/ui/cn"
import { Dialog } from "@/components/ui/Dialog"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { useIsAdmin } from "@/features/auth/useIsAdmin"
import { activityText } from "@/features/clarification/activity"
import { SSE_STATUS_LABEL } from "@/features/clarification/labels"
import type { ConversationMessage, ConversationSummary } from "@/features/clarification/types"
import { useVersionEvents } from "@/features/clarification/useVersionEvents"
import type { ConversationModel } from "@/features/conversation/api"
import type { CaseCardPart } from "@/features/conversation/types"
import {
  caseGateGuidance,
  caseReviewStage,
  designActionsAllowed,
  designStageGuidance,
} from "@/features/versions/stage"
import { useShortViewport } from "@/features/versions/useShortViewport"
import { AutoDesignSwitch } from "./AutoDesignSwitch"
import {
  confirmCases,
  confirmStageCases,
  createDesignConversation,
  getDesignGraph,
  getDesignOverview,
  listDesignConversations,
  rollbackCaseVersion,
} from "./api"
import { CaseDetailPanel } from "./CaseDetailPanel"
import { DesignChat } from "./DesignChat"
import { DesignFocusDialog } from "./DesignFocusDialog"
import { DesignMindmap } from "./DesignMindmap"
import { DesignSplitPane } from "./DesignSplitPane"
import {
  DiscoveryRevisionNotice,
  discoveryRevisionFromEventPayload,
} from "./DiscoveryRevisionNotice"
import { ExportCasesDialog } from "./ExportCasesDialog"
import { ExportDetailPanel } from "./ExportDetailPanel"
import { HandoffPanel } from "./HandoffPanel"
import {
  DEFAULT_CHAT_SPLIT,
  DEFAULT_WORKBENCH_SPLIT,
  DESIGN_SPLIT_STORAGE_KEYS,
  type DesignExecutionResults,
  MAX_CHAT_SPLIT,
  MAX_GRAPH_SPLIT,
  MIN_CHAT_SPLIT,
  MIN_GRAPH_SPLIT,
} from "./layout"
import { PendingDesignCTA } from "./PendingDesignCTA"
import type {
  CaseConfirmationScope,
  CaseRevisionRollbackView,
  DesignCaseSummary,
  DesignGraph,
  DesignOverview,
  DiscoveryRevisionData,
  VersionStage,
} from "./types"

export type DesignTabProps = {
  versionId: string
  /**
   * Deterministic version stage. `undefined`/`null` (tab rendered outside the
   * version page, or before the projection loads) keeps the legacy permissive
   * UI; the backend stage guards remain authoritative.
   */
  stage?: VersionStage | null
  /** Refresh the page-level stage projection after a gate transition. */
  onStageChanged?: () => void
  /** Deep link `?case=` forwarded by the version page. */
  initialCaseId?: string
  /** Deep link `?conversation=` forwarded by the version page. */
  initialConversationId?: string
  /** Deep link `?export=` forwarded by the version page (export detail). */
  initialExportId?: string
}

const EMPTY_REQUIREMENTS_COPY = "暂无已澄清需求，请先在需求与澄清中确认需求"

/**
 * One live activity of an agent run (D-06). Design can run several agents at
 * the same time (round author, per-requirement author, critic), so the text is
 * keyed by run instead of being a single string: a terminal event only removes
 * its own run and a late frame cannot resurrect a finished one.
 */
type AgentActivity = {
  runId: string
  /** Empty when the frame carried no conversation; such a frame is unscoped. */
  conversationId: string
  text: string
}

/** `.app-content` bottom padding, reserved so the page itself never scrolls. */
const PAGE_BOTTOM_INSET = 48
/**
 * Below this available height (viewport minus the version header/tabs) the
 * design chat folds into its title bar so the graph and detail stay usable
 * (R-D2-4). The user can expand it again from the bar.
 */
const SHORT_WORKSPACE_HEIGHT = 620
/** Narrow viewports get the same collapse: the workbench is prioritised. */
const NARROW_WORKSPACE_WIDTH = 960
/**
 * Pixel floors of the expanded split panes (D-01/D-02). The workspace never
 * forces a taller-than-viewport block any more; when these floors cannot fit,
 * the split scrolls internally and the page stays scroll-free.
 */
const CHAT_PANE_MIN_HEIGHT = 300
/**
 * DesignChat fixed chrome measured from the 0.2.2 UAT evidence: header 60 +
 * composer ~90-115 + team room 38 ≈ 222px (floating panel: message list 543 of a
 * 765px content box). A 300px inline floor would leave the message list ~78px,
 * below the 180px readability target, so short viewports (1440x900/1280x800)
 * use a floor that leaves ≥180px after that chrome.
 */
const CHAT_PANE_MIN_HEIGHT_COMPACT = 420
const WORKBENCH_PANE_MIN_HEIGHT = 200
const GRAPH_PANE_MIN_HEIGHT = 220
const DETAIL_PANE_MIN_HEIGHT = 180

/**
 * Collapsed chat title bar (R-D2-4). It replaces the full DesignChat on a
 * short/narrow viewport or after the user collapses it; one click restores the
 * inline thread and composer. Kept deliberately thin (header row only) so the
 * graph + detail workbench keeps the available height.
 */
function ChatTitleBar({
  conversationTitle,
  onExpand,
  compact = false,
}: {
  conversationTitle: string | null
  onExpand: () => void
  /** Short viewports spend the pixels on the graph instead of this bar. */
  compact?: boolean
}) {
  return (
    <div
      data-testid="design-chat-titlebar"
      className={cn(
        "surface-card flex min-w-0 shrink-0 items-center gap-2 overflow-hidden border border-border px-3",
        compact ? "py-1" : "py-2",
      )}
    >
      <h2 className="shrink-0 text-sm font-semibold text-foreground">设计对话</h2>
      <p className="min-w-0 flex-1 truncate text-[11px] text-muted-foreground">
        {conversationTitle ?? "创建对话后，设计团队会扫描待设计需求并生成用例"}
      </p>
      <Button
        variant="secondary"
        size="sm"
        data-testid="design-chat-collapse-toggle"
        aria-expanded={false}
        aria-label="展开对话"
        onClick={onExpand}
        className="shrink-0 gap-1"
      >
        <ChevronDown className="size-3.5" aria-hidden="true" />
        <span>展开</span>
      </Button>
    </div>
  )
}

/**
 * D-2 design workspace: page header + a vertical split with the design chat on
 * top (40%) and the case workbench below (60%). The workbench itself splits
 * into the graph (65%) over the case detail (35%); both dividers drag, reset on
 * double-click and persist their ratio. The page never scrolls — each panel
 * scrolls internally (R-D2-1..R-D2-6).
 *
 * The URL is the deep-link source of truth and the `version:{id}` SSE channel
 * drives debounced snapshot refreshes; node positions stay local (`layout.ts`)
 * and are never persisted.
 */
export function DesignTab({
  versionId,
  stage,
  onStageChanged,
  initialCaseId,
  initialConversationId,
  initialExportId,
}: DesignTabProps) {
  const router = useRouter()

  const [graph, setGraph] = useState<DesignGraph | null>(null)
  const [overview, setOverview] = useState<DesignOverview | null>(null)
  const [conversations, setConversations] = useState<ConversationSummary[]>([])
  const [activeConversationId, setActiveConversationId] = useState<string | null>(
    initialConversationId ?? null,
  )
  const [selectedCaseId, setSelectedCaseId] = useState<string | null>(initialCaseId ?? null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<unknown>(null)

  const [creatingConversation, setCreatingConversation] = useState(false)
  const [createError, setCreateError] = useState<unknown>(null)
  const [confirmingKey, setConfirmingKey] = useState<string | null>(null)
  const [confirmError, setConfirmError] = useState<unknown>(null)
  const [confirmNotice, setConfirmNotice] = useState<string | null>(null)
  const [staleNotice, setStaleNotice] = useState<string | null>(null)
  const [detailToken, setDetailToken] = useState(0)
  const [chatDraft, setChatDraft] = useState("")
  const [batchBusy, setBatchBusy] = useState(false)
  /** Cases checked in the mindmap and handed to the design chat ("交给 AI 修改"). */
  const [chatCaseReferences, setChatCaseReferences] = useState<CaseCardPart[]>([])
  const [exportOpen, setExportOpen] = useState(false)
  const [handoffOpen, setHandoffOpen] = useState(false)
  const [activities, setActivities] = useState<AgentActivity[]>([])
  const [liveMessages, setLiveMessages] = useState<ConversationMessage[]>([])
  const [teamRoomToken, setTeamRoomToken] = useState(0)
  /** Latest EV-12 discovery revision (auto-confirm or rollback replacement). */
  const [discoveryRevision, setDiscoveryRevision] = useState<DiscoveryRevisionData | null>(null)
  const isAdmin = useIsAdmin()

  const [chatCollapsed, setChatCollapsed] = useState(false)
  const [shortViewport, setShortViewport] = useState(false)
  // R2-D-02: the page chrome is compacted from the same media query, so the
  // workspace may reclaim the reserved .app-content bottom inset on short
  // viewports and contain the page instead of scrolling it.
  const shortMediaViewport = useShortViewport()
  const [workspaceHeight, setWorkspaceHeight] = useState("calc(100dvh - 19rem)")
  /** Graph + detail enlarged together in the 90vw×90vh floating dialog (R-D2-3). */
  const [focusOpen, setFocusOpen] = useState(false)
  /** Bumped on every focus toggle so the canvas re-fits after the size change. */
  const [fitViewToken, setFitViewToken] = useState(0)

  // Stage-gated affordances (Task 78). Unknown projections stay permissive so a
  // direct tab embed/test keeps working; the backend rejects wrong-stage calls.
  const stageKnown = stage !== undefined && stage !== null
  const designAllowed = designActionsAllowed(stage)
  const canReviewCases = caseReviewStage(stage)

  // Each case card of the graph now carries its latest execution verdict
  // (spec §9.3); DesignMindmap consumes a case-id keyed map.
  const executionResults = useMemo<DesignExecutionResults>(() => {
    const results: DesignExecutionResults = {}
    for (const requirement of graph?.requirements ?? []) {
      for (const scenario of requirement.scenarios) {
        for (const designCase of scenario.cases) {
          if (designCase.execution_verdict !== undefined) {
            results[designCase.case_id] = designCase.execution_verdict
          }
        }
      }
    }
    return results
  }, [graph])

  /**
   * The bubble shows the newest active run of the open conversation. Runs of
   * another conversation stay in the map (their own terminal event still clears
   * them) but never leak into this thread (D-06).
   */
  const activity = useMemo(() => {
    const conversationId = activeConversationId ?? ""
    for (let index = activities.length - 1; index >= 0; index -= 1) {
      const entry = activities[index]
      if (entry.conversationId === "" || entry.conversationId === conversationId) return entry.text
    }
    return null
  }, [activities, activeConversationId])

  const refreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const workspaceRef = useRef<HTMLDivElement>(null)
  /** Previous short/narrow state so crossing the threshold collapses the chat. */
  const wasShortViewport = useRef(false)

  const loadWorkspace = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const [nextGraph, nextOverview, nextConversations] = await Promise.all([
        getDesignGraph(versionId),
        getDesignOverview(versionId),
        listDesignConversations(versionId),
      ])
      setGraph(nextGraph)
      setOverview(nextOverview)
      setConversations(nextConversations)
      setActiveConversationId((current) => {
        if (current !== null && nextConversations.some((item) => item.id === current)) {
          return current
        }
        if (
          initialConversationId !== undefined &&
          nextConversations.some((item) => item.id === initialConversationId)
        ) {
          return initialConversationId
        }
        return nextConversations[0]?.id ?? null
      })
    } catch (cause) {
      setError(cause)
    } finally {
      setLoading(false)
    }
  }, [versionId, initialConversationId])

  useEffect(() => {
    void loadWorkspace()
  }, [loadWorkspace])

  useEffect(() => {
    if (initialCaseId !== undefined) setSelectedCaseId(initialCaseId)
  }, [initialCaseId])

  const refreshGraph = useCallback(async () => {
    try {
      setGraph(await getDesignGraph(versionId))
    } catch {
      // Keep the last snapshot; the next SSE event or manual retry refreshes again.
    }
  }, [versionId])

  const refreshOverview = useCallback(async () => {
    try {
      const [nextOverview, nextConversations] = await Promise.all([
        getDesignOverview(versionId),
        listDesignConversations(versionId),
      ])
      setOverview(nextOverview)
      setConversations(nextConversations)
    } catch {
      // A failed projection refresh must not blank the workspace.
    }
  }, [versionId])

  const scheduleGraphRefresh = useCallback(() => {
    if (refreshTimer.current !== null) clearTimeout(refreshTimer.current)
    refreshTimer.current = setTimeout(() => {
      refreshTimer.current = null
      void refreshGraph()
    }, 250)
  }, [refreshGraph])

  useEffect(
    () => () => {
      if (refreshTimer.current !== null) clearTimeout(refreshTimer.current)
    },
    [],
  )

  const sseStatus = useVersionEvents(versionId, {
    onConversationMessage: (message) => {
      // A completed message is the authoritative end of the turn: drop the
      // activities of that conversation instead of waiting for a terminal run
      // event that may be lost with a stream reconnect.
      setActivities((current) =>
        current.filter(
          (entry) =>
            entry.conversationId !== "" && entry.conversationId !== message.conversation_id,
        ),
      )
      setLiveMessages((current) => {
        if (current.some((existing) => existing.id === message.id)) return current
        return [...current, message].slice(-200)
      })
    },
    onAgentActivity: (payload) => {
      const text = activityText(payload)
      if (text === null) return
      const runId = typeof payload.run_id === "string" ? payload.run_id : ""
      const conversationId =
        typeof payload.conversation_id === "string" ? payload.conversation_id : ""
      // Keep the entries in arrival order so the last one is the newest active
      // run; an update of the same run moves to the end.
      setActivities((current) => [
        ...current.filter((entry) => entry.runId !== runId),
        { runId, conversationId, text },
      ])
    },
    onAgentRunChanged: (event) => {
      if (event.type !== "agent.run.finished" && event.type !== "agent.run.failed") return
      const runId =
        typeof event.payload.run_id === "string"
          ? event.payload.run_id
          : typeof event.scope.agent_run_id === "string"
            ? event.scope.agent_run_id
            : ""
      if (runId === "") return
      // Terminal event: the bubble must disappear even though the backend never
      // publishes a terminal `agent.activity` frame (D-06).
      setActivities((current) => current.filter((entry) => entry.runId !== runId))
    },
    onTeamRoomChanged: () => setTeamRoomToken((token) => token + 1),
    onDesignGraphChanged: () => scheduleGraphRefresh(),
    onDesignCaseChanged: (event) => {
      scheduleGraphRefresh()
      if (event.type === "case.revision.auto_confirmed") {
        // EV-12/W2-12: show the auto-confirmed or rollback replacement revision
        // and keep the case detail/graph in sync.
        const next = discoveryRevisionFromEventPayload(event.payload)
        if (next !== null) setDiscoveryRevision(next)
      }
      if (event.type === "design.case_version_stale") {
        const title = typeof event.payload.title === "string" ? event.payload.title : "关联用例"
        setStaleNotice(`${title} 因需求变更已过期，等待重新设计。`)
      }
      const changedCaseId = typeof event.payload.case_id === "string" ? event.payload.case_id : null
      if (changedCaseId !== null && changedCaseId === selectedCaseId) {
        setDetailToken((token) => token + 1)
      }
    },
    onDesignRoundChanged: () => {
      void refreshOverview()
    },
    onCaseRunChanged: (event) => {
      // A finished execution changes the case's latest verdict on the mindmap.
      if (event.type === "case_run.finished") scheduleGraphRefresh()
    },
  })

  const updateUrl = useCallback(
    (caseId: string | null, conversationId: string | null) => {
      const params = new URLSearchParams({ tab: "design" })
      if (conversationId !== null) params.set("conversation", conversationId)
      if (caseId !== null) params.set("case", caseId)
      router.replace(`/versions/${encodeURIComponent(versionId)}?${params.toString()}`)
    },
    [router, versionId],
  )

  const selectCase = useCallback(
    (caseId: string) => {
      setSelectedCaseId(caseId)
      updateUrl(caseId, activeConversationId)
    },
    [updateUrl, activeConversationId],
  )

  const selectConversation = useCallback(
    (conversationId: string) => {
      setActiveConversationId(conversationId)
      setStaleNotice(null)
      updateUrl(selectedCaseId, conversationId)
    },
    [updateUrl, selectedCaseId],
  )

  const handleNewConversation = useCallback(async (): Promise<void> => {
    setCreatingConversation(true)
    setCreateError(null)
    try {
      const conversation = await createDesignConversation(versionId)
      setConversations((current) => [
        conversation,
        ...current.filter((item) => item.id !== conversation.id),
      ])
      setActiveConversationId(conversation.id)
      setSelectedCaseId(null)
      setStaleNotice(null)
      updateUrl(null, conversation.id)
    } catch (cause) {
      setCreateError(cause)
    } finally {
      setCreatingConversation(false)
    }
  }, [versionId, updateUrl])

  const createConversation = useCallback(() => {
    if (!designAllowed) {
      setCreateError(new Error(designStageGuidance(stage)))
      return
    }
    void handleNewConversation()
  }, [designAllowed, handleNewConversation, stage])

  const batchCaseGate = useCallback(
    async (caseIds: string[], mode: "confirm" | "execute"): Promise<void> => {
      if (caseIds.length === 0) return
      if (!canReviewCases) {
        setConfirmError(new Error(caseGateGuidance(stage)))
        return
      }
      setBatchBusy(true)
      setConfirmError(null)
      setConfirmNotice(null)
      try {
        if (mode === "execute") {
          // Explicit batch execute: the case hard gate confirms the selected
          // cases and creates the execute.batch job for exactly this selection.
          await confirmStageCases(versionId, caseIds)
          setConfirmNotice(`已提交 ${caseIds.length} 条用例执行`)
          onStageChanged?.()
        } else {
          // Batch confirm only freezes the selected case versions. It must not
          // advance the case hard gate, so no wave is created until the user
          // explicitly executes the selection (Task 78 / QA-F2).
          let confirmed = 0
          let skipped = 0
          for (const caseId of caseIds) {
            const result = await confirmCases(versionId, {
              scope: "case",
              target_id: caseId,
            })
            confirmed += result.confirmed.length
            skipped += result.skipped.length
          }
          setConfirmNotice(
            skipped > 0
              ? `已确认 ${confirmed} 条用例（未执行），跳过 ${skipped} 条已确认用例`
              : `已确认 ${confirmed} 条用例（未执行）`,
          )
        }
        await refreshGraph()
        await refreshOverview()
        setDetailToken((token) => token + 1)
      } catch (cause) {
        setConfirmError(cause)
      } finally {
        setBatchBusy(false)
      }
    },
    [versionId, refreshGraph, refreshOverview, canReviewCases, stage, onStageChanged],
  )

  // The batch bar hands exactly the checked ids to the 0.2.1 stage gate. The
  // backend refreshes intents only for that selection (D23), so unselected
  // confirmed cases never enter a wave; confirm and execute stay scoped to the
  // checked cases.
  const batchConfirmCases = useCallback(
    (caseIds: string[]) => {
      void batchCaseGate(caseIds, "confirm")
    },
    [batchCaseGate],
  )

  const batchExecuteCases = useCallback(
    (caseIds: string[]) => {
      void batchCaseGate(caseIds, "execute")
    },
    [batchCaseGate],
  )

  /**
   * "交给 AI 修改": resolve the checked case ids against the graph and put
   * `case_card` reference parts into the design composer. Sending stays the
   * user's action so the modification instruction can be edited first.
   */
  const requestAiChange = useCallback(
    (caseIds: string[]) => {
      if (graph === null) return
      const byId = new Map<string, DesignCaseSummary>()
      for (const requirement of graph.requirements) {
        for (const scenario of requirement.scenarios) {
          for (const designCase of scenario.cases) byId.set(designCase.case_id, designCase)
        }
      }
      const references: CaseCardPart[] = []
      for (const caseId of caseIds) {
        const designCase = byId.get(caseId)
        if (designCase === undefined) continue
        references.push({
          type: "case_card",
          case_id: designCase.case_id,
          case_version_id: designCase.current_version_id,
          title: designCase.title,
          status: designCase.status,
        })
      }
      if (references.length === 0) return
      setChatCaseReferences(references)
      setChatDraft(
        `请修改以下 ${references.length} 条用例：\n${references
          .map((reference) => `- ${reference.title}（#${reference.case_id}）`)
          .join("\n")}`,
      )
      if (activeConversationId === null) void handleNewConversation()
    },
    [graph, activeConversationId, handleNewConversation],
  )

  const requestAiChangeForCase = useCallback(
    (caseId: string) => requestAiChange([caseId]),
    [requestAiChange],
  )

  const handleConfirm = useCallback(
    async (scope: CaseConfirmationScope, targetId: string): Promise<void> => {
      if (!canReviewCases) {
        setConfirmError(new Error(caseGateGuidance(stage)))
        return
      }
      setConfirmingKey(`${scope}:${targetId}`)
      setConfirmError(null)
      setConfirmNotice(null)
      try {
        const result = await confirmCases(versionId, { scope, target_id: targetId })
        setConfirmNotice(
          `已确认 ${result.confirmed.length} 个用例，跳过 ${result.skipped.length} 个已确认用例`,
        )
        await refreshGraph()
        setDetailToken((token) => token + 1)
      } catch (cause) {
        setConfirmError(cause)
      } finally {
        setConfirmingKey(null)
      }
    },
    [versionId, refreshGraph, canReviewCases, stage],
  )

  const rollbackDiscoveryRevision = useCallback(
    (caseVersionId: string, reason?: string) =>
      reason === undefined
        ? rollbackCaseVersion(versionId, caseVersionId)
        : rollbackCaseVersion(versionId, caseVersionId, reason),
    [versionId],
  )

  const handleDiscoveryRolledBack = useCallback(
    (result?: CaseRevisionRollbackView) => {
      setDiscoveryRevision((current) => {
        if (current === null || result === undefined) return current
        return {
          ...current,
          caseId: result.case_id,
          caseVersionId: result.case_version_id,
          revision: result.revision,
          status: "confirmed",
          trigger: "rollback",
          rollbackOf: result.rolled_back_from,
          inferred: false,
        }
      })
      scheduleGraphRefresh()
      void refreshOverview()
      setDetailToken((token) => token + 1)
    },
    [refreshOverview, scheduleGraphRefresh],
  )

  // Stable wrappers for DesignMindmap's memoized node data: React Flow must not
  // see new callback identities on every parent render, or it re-resets the
  // measured node dimensions and the canvas never becomes visible.
  const confirmCase = useCallback(
    (caseId: string) => {
      void handleConfirm("case", caseId)
    },
    [handleConfirm],
  )

  const confirmScenario = useCallback(
    (scenarioId: string) => {
      void handleConfirm("scenario", scenarioId)
    },
    [handleConfirm],
  )

  const confirmRequirement = useCallback(
    (requirementId: string) => {
      void handleConfirm("requirement", requirementId)
    },
    [handleConfirm],
  )

  const applyAutoDesign = useCallback((enabled: boolean) => {
    setOverview((current) =>
      current === null ? current : { ...current, auto_design_new_requirements: enabled },
    )
    setGraph((current) =>
      current === null ? current : { ...current, auto_design_new_requirements: enabled },
    )
  }, [])

  /**
   * Keep the workspace conversation summaries in sync after the chat model
   * selector persists an override: collapsing/expanding the chat remounts the
   * keyed selector, which must reseed from the updated summary.
   */
  const applyConversationModel = useCallback((updated: ConversationModel) => {
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
  }, [])

  const toggleChatCollapsed = useCallback(() => setChatCollapsed((value) => !value), [])

  // The zoom toggle never re-creates the canvas: it only switches the 90vw×90vh
  // frame and lets `FloatingPanel` re-parent the existing workbench host. The
  // fit token defers the re-fit until the new size is committed (DesignMindmap).
  const toggleFocus = useCallback(() => {
    setFocusOpen((open) => !open)
    setFitViewToken((token) => token + 1)
  }, [])

  const closeFocus = useCallback(() => {
    setFocusOpen(false)
    setFitViewToken((token) => token + 1)
  }, [])

  // The version page is not a flex column, so the workspace measures the space
  // left below its own top edge and caps itself to the viewport. That keeps the
  // document from growing: only the panes (and their panels) scroll.
  // D-03: the old hard 320px minimum pushed a 1280x800 page 75px tall, so the
  // workspace now takes exactly `innerHeight - top - bottom inset` (0 at worst).
  // biome-ignore lint/correctness/useExhaustiveDependencies: stage changes move the chrome above the workspace without a resize event
  useEffect(() => {
    if (loading || graph === null) return undefined
    const updateHeight = () => {
      const workspace = workspaceRef.current
      if (workspace === null) return
      const top = workspace.getBoundingClientRect().top
      // Short viewports get compact page chrome, so the reserved 48px
      // app-content inset is reclaimed below the workspace and compensated by
      // the negative margin on the root. The document still ends at the
      // viewport bottom (R2-D-02).
      const bottomInset = shortMediaViewport ? 0 : PAGE_BOTTOM_INSET
      const available = Math.max(0, window.innerHeight - top - bottomInset)
      setWorkspaceHeight(`${Math.floor(available)}px`)
      const isShort =
        available < SHORT_WORKSPACE_HEIGHT || window.innerWidth < NARROW_WORKSPACE_WIDTH
      setShortViewport(isShort)
      // Collapse when the workspace *enters* the short/narrow band. Resizes
      // while already short never override the user's explicit expand.
      if (isShort && !wasShortViewport.current) setChatCollapsed(true)
      wasShortViewport.current = isShort
    }
    updateHeight()
    window.addEventListener("resize", updateHeight)
    // Stage notices / blocker panels change the chrome above the workspace and
    // move its top edge without firing `resize`; observe the parent so the
    // height follows the new available space (D-03).
    const parent = workspaceRef.current?.parentElement ?? null
    const observer =
      parent !== null && typeof ResizeObserver === "function"
        ? new ResizeObserver(updateHeight)
        : null
    observer?.observe(parent as Element)
    return () => {
      window.removeEventListener("resize", updateHeight)
      observer?.disconnect()
    }
  }, [loading, graph, stage, shortMediaViewport])

  if (loading) {
    return (
      <div data-testid="design-tab">
        <LoadingCard label="正在加载设计图…" />
      </div>
    )
  }

  if (error !== null || graph === null) {
    return (
      <div data-testid="design-tab">
        <ErrorState
          error={error}
          title="设计图加载失败"
          onRetry={() => {
            void loadWorkspace()
          }}
        />
      </div>
    )
  }

  const stats = graph.stats
  /** Short/narrow viewports compress the chrome so the graph keeps its floor. */
  const compact = shortViewport
  const autoDesign = overview?.auto_design_new_requirements ?? graph.auto_design_new_requirements
  const roundTitle = overview?.active_round
    ? `设计轮次 #${overview.active_round.round_no}`
    : "暂无设计轮次"
  const activeConversation =
    conversations.find((conversation) => conversation.id === activeConversationId) ?? null
  const effectiveModel =
    activeConversation?.effective_model ?? activeConversation?.model_override ?? "—"
  const sseLabel = SSE_STATUS_LABEL[sseStatus]
  const chatTitle = activeConversation?.title ?? null
  const selectedDiscoveryRevision =
    discoveryRevision !== null && discoveryRevision.caseId === selectedCaseId
      ? discoveryRevision
      : null
  const showDiscoveryBar = discoveryRevision !== null && selectedDiscoveryRevision === null

  const workbench = (
    <div
      data-testid="design-workbench"
      className="flex h-full min-h-0 min-w-0 flex-col overflow-hidden"
    >
      <header
        data-testid="design-workbench-toolbar"
        className={cn(
          "flex shrink-0 flex-wrap items-center gap-2 px-1",
          compact ? "pb-0.5" : "pb-1",
        )}
      >
        <h2 className="text-sm font-semibold text-foreground">用例工作区</h2>
        {compact ? null : (
          <span className="text-[10px] text-muted-foreground">
            图在上 · 详情紧接在下 · 内部滚动
          </span>
        )}
        <div className="ml-auto flex flex-wrap items-center gap-1.5">
          <span data-testid="design-workbench-live" className="text-[10px] text-muted-foreground">
            {sseLabel}
          </span>
          <Button
            size="sm"
            variant="secondary"
            data-testid="design-workbench-zoom"
            aria-label={focusOpen ? "还原工作区" : "放大工作区"}
            aria-expanded={focusOpen}
            aria-haspopup="dialog"
            onClick={toggleFocus}
            className="gap-1"
          >
            {focusOpen ? (
              <Minimize2 className="size-3.5" aria-hidden="true" />
            ) : (
              <Maximize2 className="size-3.5" aria-hidden="true" />
            )}
            {focusOpen ? "还原工作区" : "放大工作区"}
          </Button>
        </div>
      </header>

      <DesignSplitPane
        testId="design-split-workbench"
        topTestId="design-graph"
        bottomTestId="design-detail"
        defaultPercent={DEFAULT_WORKBENCH_SPLIT}
        minPercent={MIN_GRAPH_SPLIT}
        maxPercent={MAX_GRAPH_SPLIT}
        storageKey={DESIGN_SPLIT_STORAGE_KEYS.workbench}
        ariaLabel="调整用例图与用例详情高度（↑↓ ±5%，Home 最小，End 最大，双击复位）"
        topOverflow="auto"
        bottomOverflow="auto"
        topMinHeightPx={GRAPH_PANE_MIN_HEIGHT}
        bottomMinHeightPx={DETAIL_PANE_MIN_HEIGHT}
        scrollOnOverflow
        className="min-h-0 flex-1"
        topClassName="[&>section]:min-h-0 [&>section]:flex-1"
        top={
          graph.requirements.length === 0 ? (
            <div className="surface-card flex h-full items-center justify-center p-6">
              <EmptyState
                title={EMPTY_REQUIREMENTS_COPY}
                description="需求确认后，新建设计对话即可让设计团队扫描并生成用例。"
                action={
                  <Button size="sm" disabled={creatingConversation} onClick={createConversation}>
                    ＋ 新建对话
                  </Button>
                }
                className="border-none bg-transparent"
              />
            </div>
          ) : (
            <DesignMindmap
              graph={graph}
              compact={compact}
              selectedCaseId={selectedCaseId}
              confirmingKey={confirmingKey}
              executionResults={executionResults}
              batchBusy={batchBusy}
              caseReviewEnabled={canReviewCases}
              onSelectCase={selectCase}
              onConfirmCase={confirmCase}
              onConfirmScenario={confirmScenario}
              onConfirmRequirement={confirmRequirement}
              onBatchConfirmCases={batchConfirmCases}
              onBatchExecuteCases={batchExecuteCases}
              onRequestAiChange={requestAiChange}
              fitViewToken={fitViewToken}
            />
          )
        }
        bottom={
          <CaseDetailPanel
            versionId={versionId}
            caseId={selectedCaseId}
            refreshToken={detailToken}
            confirming={confirmingKey === `case:${selectedCaseId ?? ""}`}
            canConfirm={canReviewCases}
            discoveryRevision={selectedDiscoveryRevision}
            readOnly={!isAdmin}
            onRolledBack={handleDiscoveryRolledBack}
            onConfirm={confirmCase}
            onRequestChange={setChatDraft}
            onRequestAiChange={requestAiChangeForCase}
          />
        }
      />
    </div>
  )

  return (
    <div
      ref={workspaceRef}
      data-testid="design-scroll"
      data-short-viewport={shortViewport ? "true" : "false"}
      data-compact={compact ? "true" : "false"}
      className="relative flex min-h-0 min-w-0 flex-col gap-3 overflow-hidden"
      style={{
        height: workspaceHeight,
        overflow: "hidden",
        // The workspace ends at the viewport bottom on short viewports; pull
        // the .app-content bottom padding back under it so the document does
        // not gain 48px of scroll height (R2-D-02).
        marginBottom: shortMediaViewport ? -PAGE_BOTTOM_INSET : undefined,
      }}
    >
      <header
        data-testid="design-header"
        className={cn(
          "flex shrink-0 flex-wrap",
          compact ? "items-center gap-1" : "items-start gap-2",
        )}
      >
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span
              data-testid="design-header-version"
              className="rounded-md border border-border px-1.5 py-0.5 text-[10px] text-muted-foreground"
            >
              版本 #{versionId}
            </span>
            <span className="text-[10px] text-muted-foreground">用例设计</span>
            <h1 className="text-lg font-semibold text-foreground">用例脑图 · {roundTitle}</h1>
            {compact ? null : (
              <span data-testid="design-header-host" className="text-[10px] text-muted-foreground">
                Design Host 单写者 · 事件通道：{sseLabel}
              </span>
            )}
            <span data-testid="design-stats" className="text-[11px] text-muted-foreground">
              需求 {stats.requirements} · 场景 {stats.scenarios} · 用例 {stats.cases} · draft{" "}
              {stats.draft} · confirmed {stats.confirmed} · stale {stats.stale}
            </span>
          </div>
          {compact ? null : (
            <div className="mt-0.5 flex flex-wrap items-center gap-3 text-[10px] text-muted-foreground">
              <span data-testid="design-header-context">上下文水位 —</span>
              <span data-testid="design-header-model">模型 {effectiveModel}</span>
            </div>
          )}
        </div>
        <div className="ml-auto flex flex-wrap items-center gap-1.5">
          <Button
            size="sm"
            variant="secondary"
            data-testid="design-handoff"
            onClick={() => setHandoffOpen(true)}
          >
            交接文档
          </Button>
          <Button
            size="sm"
            variant="secondary"
            data-testid="export-cases"
            onClick={() => setExportOpen(true)}
          >
            导出用例
          </Button>
          <AutoDesignSwitch
            versionId={versionId}
            enabled={autoDesign}
            onChanged={applyAutoDesign}
          />
          <Button
            size="sm"
            data-testid="design-new-conversation"
            disabled={creatingConversation || !designAllowed}
            title={designAllowed ? undefined : designStageGuidance(stage)}
            onClick={createConversation}
          >
            {creatingConversation ? "创建中…" : "＋ 新建对话"}
          </Button>
        </div>
      </header>

      {stageKnown && !designAllowed ? (
        <p
          data-testid="design-stage-notice"
          role="status"
          className="shrink-0 rounded-lg border border-warning/40 bg-warning/10 px-3 py-2 text-[11px] text-warning"
        >
          {designStageGuidance(stage)}
        </p>
      ) : null}
      {stageKnown && !canReviewCases ? (
        <p
          data-testid="case-gate-notice"
          role="status"
          className="shrink-0 rounded-lg border border-border bg-muted/40 px-3 py-2 text-[11px] text-muted-foreground"
        >
          {caseGateGuidance(stage)}
        </p>
      ) : null}

      {showDiscoveryBar && discoveryRevision !== null ? (
        <div data-testid="design-discovery-revision" className="shrink-0">
          <DiscoveryRevisionNotice
            revision={discoveryRevision}
            readOnly={!isAdmin}
            onRollback={rollbackDiscoveryRevision}
            onRolledBack={handleDiscoveryRolledBack}
          />
        </div>
      ) : null}

      {initialExportId !== undefined ? (
        <div data-testid="design-export-detail" className="shrink-0">
          <ExportDetailPanel exportId={initialExportId} />
        </div>
      ) : null}

      {createError !== null ? <InlineError error={createError} /> : null}
      {confirmError !== null ? <InlineError error={confirmError} /> : null}
      {confirmNotice !== null ? (
        <p
          data-testid="design-confirm-notice"
          role="status"
          className="shrink-0 rounded-lg border border-success/30 bg-success/10 px-3 py-2 text-[11px] text-success"
        >
          {confirmNotice}
        </p>
      ) : null}

      {conversations.length === 0 ? (
        <PendingDesignCTA
          versionId={versionId}
          pendingCount={graph.pending_design_count}
          hasConversation={false}
          onCreateConversation={designAllowed ? createConversation : undefined}
        />
      ) : null}

      {/* R-D2-1/2: chat above the workbench (40/60), each pane owns its scroll. */}
      <DesignSplitPane
        testId="design-split-main"
        defaultPercent={DEFAULT_CHAT_SPLIT}
        minPercent={MIN_CHAT_SPLIT}
        maxPercent={MAX_CHAT_SPLIT}
        storageKey={DESIGN_SPLIT_STORAGE_KEYS.chat}
        ariaLabel="调整设计对话与用例工作区高度（↑↓ ±5%，Home 最小，End 最大，双击复位）"
        topCollapsed={chatCollapsed}
        topOverflow="hidden"
        bottomOverflow="hidden"
        topMinHeightPx={
          chatCollapsed ? undefined : compact ? CHAT_PANE_MIN_HEIGHT_COMPACT : CHAT_PANE_MIN_HEIGHT
        }
        bottomMinHeightPx={chatCollapsed ? undefined : WORKBENCH_PANE_MIN_HEIGHT}
        scrollOnOverflow
        className="min-h-0 flex-1"
        top={
          chatCollapsed ? (
            <ChatTitleBar
              conversationTitle={chatTitle}
              onExpand={toggleChatCollapsed}
              compact={compact}
            />
          ) : (
            <div
              data-testid="design-chat-pane"
              className="flex h-full min-h-0 min-w-0 flex-col overflow-hidden"
              style={{ overflow: "hidden" }}
            >
              <DesignChat
                versionId={versionId}
                conversations={conversations}
                activeConversationId={activeConversationId}
                onSelectConversation={selectConversation}
                onCreateConversation={createConversation}
                creatingConversation={creatingConversation}
                liveMessages={liveMessages}
                activity={activity}
                sseStatus={sseStatus}
                staleNotice={staleNotice}
                draftText={chatDraft}
                onDraftTextChange={setChatDraft}
                refreshToken={teamRoomToken}
                collapsed={false}
                onToggleCollapsed={toggleChatCollapsed}
                onConversationModelChanged={applyConversationModel}
                caseReferences={chatCaseReferences}
                onCaseReferencesChange={setChatCaseReferences}
                onCaseSelect={selectCase}
              />
            </div>
          )
        }
        bottom={
          // R-D2-3: graph + detail move together into the 90vw×90vh dialog.
          <DesignFocusDialog open={focusOpen} onClose={closeFocus}>
            {workbench}
          </DesignFocusDialog>
        }
      />

      <ExportCasesDialog
        versionId={versionId}
        open={exportOpen}
        onClose={() => setExportOpen(false)}
      />

      <Dialog
        open={handoffOpen}
        onClose={() => setHandoffOpen(false)}
        title="澄清交接文档"
        description="需求 / 问答 / 决策 / 待决 / 计划 / 资源六块交接（只读）"
        className="max-w-3xl"
      >
        <div className="max-h-[70vh] overflow-y-auto pr-1">
          <HandoffPanel versionId={versionId} />
        </div>
      </Dialog>
    </div>
  )
}

/** Re-exported for callers that need the empty-state copy in tests/tooltips. */
export const DESIGN_EMPTY_COPY = EMPTY_REQUIREMENTS_COPY
