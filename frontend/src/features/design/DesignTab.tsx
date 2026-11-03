"use client"

import { useRouter } from "next/navigation"
import {
  type KeyboardEvent,
  type PointerEvent,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react"

import { Button } from "@/components/ui/Button"
import { cn } from "@/components/ui/cn"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import type { ConversationMessage, ConversationSummary } from "@/features/clarification/types"
import { useVersionEvents } from "@/features/clarification/useVersionEvents"
import type { ConversationModel } from "@/features/conversation/api"
import { AutoDesignSwitch } from "./AutoDesignSwitch"
import {
  confirmCases,
  createDesignConversation,
  getDesignGraph,
  getDesignOverview,
  listDesignConversations,
} from "./api"
import { CaseDetailPanel } from "./CaseDetailPanel"
import { DesignChat } from "./DesignChat"
import { DesignMindmap } from "./DesignMindmap"
import { ExportCasesDialog } from "./ExportCasesDialog"
import type { DesignExecutionResults } from "./layout"
import { PendingDesignCTA } from "./PendingDesignCTA"
import type { CaseConfirmationScope, DesignGraph, DesignOverview } from "./types"

export type DesignTabProps = {
  versionId: string
  /** Deep link `?case=` forwarded by the version page. */
  initialCaseId?: string
  /** Deep link `?conversation=` forwarded by the version page. */
  initialConversationId?: string
}

const EMPTY_REQUIREMENTS_COPY = "暂无已澄清需求，请先在需求与澄清中确认需求"

function activityText(payload: Record<string, unknown>): string | null {
  const status = typeof payload.status === "string" ? payload.status : ""
  if (status === "finished" || status === "failed" || status === "done") return null
  if (typeof payload.activity === "string" && payload.activity.length > 0) return payload.activity
  if (typeof payload.message === "string" && payload.message.length > 0) return payload.message
  return "AI 正在处理…"
}

/** The two draggable boundaries of the vertical three-layer workspace. */
type DividerKind = "chat" | "mindmap"

/** Chat / mindmap / detail share of the workspace height, in percent. */
type LayerSplits = { chat: number; mindmap: number; detail: number }

const DEFAULT_LAYER_SPLITS: LayerSplits = { chat: 35, mindmap: 40, detail: 25 }
const MIN_LAYER_PERCENT = 10
const KEYBOARD_RESIZE_STEP = 5
/** `.app-content` bottom padding, reserved so the page itself never scrolls. */
const PAGE_BOTTOM_INSET = 48
const MIN_WORKSPACE_HEIGHT = 320

function clampPercent(value: number, min: number, max: number): number {
  return Math.min(Math.max(value, min), max)
}

function roundPercent(value: number): number {
  return Math.round(value * 100) / 100
}

/**
 * Move one divider by `delta` percent. A boundary always keeps the combined
 * height of its two neighbouring layers constant (chat+mindmap / mindmap+detail),
 * so dragging never changes the third layer.
 */
function resizeLayerSplits(current: LayerSplits, divider: DividerKind, delta: number): LayerSplits {
  if (divider === "chat") {
    const total = current.chat + current.mindmap
    const chat = roundPercent(
      clampPercent(current.chat + delta, MIN_LAYER_PERCENT, total - MIN_LAYER_PERCENT),
    )
    return { chat, mindmap: roundPercent(total - chat), detail: current.detail }
  }
  const total = current.mindmap + current.detail
  const mindmap = roundPercent(
    clampPercent(current.mindmap + delta, MIN_LAYER_PERCENT, total - MIN_LAYER_PERCENT),
  )
  return { chat: current.chat, mindmap, detail: roundPercent(total - mindmap) }
}

/**
 * P2 design workspace: a full-height, vertically stacked three-layer layout
 * (design chat / React Flow mindmap / case detail) with draggable separators.
 * The URL is the deep-link source of truth and the `version:{id}` SSE channel
 * drives debounced snapshot refreshes; node positions stay local (`layout.ts`)
 * and are never persisted.
 */
export function DesignTab({ versionId, initialCaseId, initialConversationId }: DesignTabProps) {
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
  const [exportOpen, setExportOpen] = useState(false)
  const [activity, setActivity] = useState<string | null>(null)
  const [liveMessages, setLiveMessages] = useState<ConversationMessage[]>([])
  const [teamRoomToken, setTeamRoomToken] = useState(0)

  const [layerSplits, setLayerSplits] = useState<LayerSplits>(DEFAULT_LAYER_SPLITS)
  const [chatCollapsed, setChatCollapsed] = useState(false)
  const [activeDivider, setActiveDivider] = useState<DividerKind | null>(null)
  const [workspaceHeight, setWorkspaceHeight] = useState("calc(100dvh - 19rem)")

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

  const refreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const workspaceRef = useRef<HTMLDivElement>(null)
  const layersRef = useRef<HTMLDivElement>(null)
  const dividerDrag = useRef<{
    divider: DividerKind
    startY: number
    startSplits: LayerSplits
  } | null>(null)

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
      setActivity(null)
      setLiveMessages((current) => {
        if (current.some((existing) => existing.id === message.id)) return current
        return [...current, message].slice(-200)
      })
    },
    onAgentActivity: (payload) => setActivity(activityText(payload)),
    onTeamRoomChanged: () => setTeamRoomToken((token) => token + 1),
    onDesignGraphChanged: () => scheduleGraphRefresh(),
    onDesignCaseChanged: (event) => {
      scheduleGraphRefresh()
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
      setActivity(null)
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
    void handleNewConversation()
  }, [handleNewConversation])

  const handleConfirm = useCallback(
    async (scope: CaseConfirmationScope, targetId: string): Promise<void> => {
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
    [versionId, refreshGraph],
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

  // The version page is not a flex column, so the workspace measures the space
  // left below its own top edge and caps itself to the viewport. That keeps the
  // document from growing: only the three layers (and their panels) scroll.
  useEffect(() => {
    if (loading || graph === null) return undefined
    const updateHeight = () => {
      const workspace = workspaceRef.current
      if (workspace === null) return
      const top = workspace.getBoundingClientRect().top
      const available = Math.max(MIN_WORKSPACE_HEIGHT, window.innerHeight - top - PAGE_BOTTOM_INSET)
      setWorkspaceHeight(`${Math.round(available)}px`)
    }
    updateHeight()
    window.addEventListener("resize", updateHeight)
    return () => window.removeEventListener("resize", updateHeight)
  }, [loading, graph])

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
  const autoDesign = overview?.auto_design_new_requirements ?? graph.auto_design_new_requirements
  const roundTitle = overview?.active_round
    ? `设计轮次 #${overview.active_round.round_no}`
    : "暂无设计轮次"

  const chatPairTotal = layerSplits.chat + layerSplits.mindmap
  const mindmapPairTotal = layerSplits.mindmap + layerSplits.detail

  function dividerDisabled(divider: DividerKind): boolean {
    return divider === "chat" && chatCollapsed
  }

  function handleDividerPointerDown(
    divider: DividerKind,
    event: PointerEvent<HTMLDivElement>,
  ): void {
    if (dividerDisabled(divider)) return
    dividerDrag.current = { divider, startY: event.clientY, startSplits: layerSplits }
    setActiveDivider(divider)
    if (typeof event.currentTarget.setPointerCapture === "function") {
      event.currentTarget.setPointerCapture(event.pointerId)
    }
    event.preventDefault()
  }

  function handleDividerPointerMove(
    divider: DividerKind,
    event: PointerEvent<HTMLDivElement>,
  ): void {
    const drag = dividerDrag.current
    if (drag === null || drag.divider !== divider) return
    const height = layersRef.current?.getBoundingClientRect().height ?? 0
    if (height <= 0) return
    const delta = ((event.clientY - drag.startY) / height) * 100
    setLayerSplits(resizeLayerSplits(drag.startSplits, divider, delta))
  }

  function handleDividerPointerUp(divider: DividerKind): void {
    if (dividerDrag.current?.divider !== divider) return
    dividerDrag.current = null
    setActiveDivider(null)
  }

  function handleDividerKeyDown(divider: DividerKind, event: KeyboardEvent<HTMLDivElement>): void {
    if (dividerDisabled(divider)) return
    if (event.key !== "ArrowUp" && event.key !== "ArrowDown") return
    event.preventDefault()
    const delta = event.key === "ArrowDown" ? KEYBOARD_RESIZE_STEP : -KEYBOARD_RESIZE_STEP
    setLayerSplits((current) => resizeLayerSplits(current, divider, delta))
  }

  const chatLayerStyle = chatCollapsed
    ? { flexGrow: 0, flexShrink: 0, flexBasis: "auto" }
    : { flexGrow: 0, flexShrink: 1, flexBasis: `${layerSplits.chat}%` }
  // Collapsed chat takes only its content height; the mindmap grows into the rest.
  const mindmapLayerStyle = chatCollapsed
    ? { flexGrow: 1, flexShrink: 1, flexBasis: "0%" }
    : { flexGrow: 0, flexShrink: 1, flexBasis: `${layerSplits.mindmap}%` }
  const detailLayerStyle = { flexGrow: 0, flexShrink: 1, flexBasis: `${layerSplits.detail}%` }

  return (
    <div
      ref={workspaceRef}
      data-testid="design-tab"
      className="flex min-h-0 flex-col gap-3 overflow-hidden"
      style={{ height: workspaceHeight }}
    >
      <header className="flex shrink-0 flex-wrap items-center gap-2">
        <h1 className="text-lg font-semibold text-foreground">用例脑图 · {roundTitle}</h1>
        <span data-testid="design-stats" className="text-[11px] text-muted-foreground">
          需求 {stats.requirements} · 场景 {stats.scenarios} · 用例 {stats.cases} · draft{" "}
          {stats.draft} · confirmed {stats.confirmed} · stale {stats.stale}
        </span>
        <div className="ml-auto flex flex-wrap items-center gap-1.5">
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
            disabled={creatingConversation}
            onClick={createConversation}
          >
            {creatingConversation ? "创建中…" : "＋ 新建对话"}
          </Button>
        </div>
      </header>

      {createError !== null ? <InlineError error={createError} /> : null}
      {confirmError !== null ? <InlineError error={confirmError} /> : null}
      {confirmNotice !== null ? (
        <p
          data-testid="design-confirm-notice"
          role="status"
          className="rounded-lg border border-success/30 bg-success/10 px-3 py-2 text-[11px] text-success"
        >
          {confirmNotice}
        </p>
      ) : null}

      {conversations.length === 0 ? (
        <PendingDesignCTA
          versionId={versionId}
          pendingCount={graph.pending_design_count}
          hasConversation={false}
          onCreateConversation={createConversation}
        />
      ) : null}

      <div
        ref={layersRef}
        data-testid="design-layers"
        className="flex min-h-0 flex-1 flex-col overflow-hidden"
      >
        <div
          data-testid="design-layer-chat"
          data-collapsed={chatCollapsed ? "true" : "false"}
          className="min-h-0 overflow-auto"
          style={chatLayerStyle}
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
            collapsed={chatCollapsed}
            onToggleCollapsed={toggleChatCollapsed}
            onConversationModelChanged={applyConversationModel}
          />
        </div>

        <hr
          data-testid="design-divider-chat"
          // biome-ignore lint/a11y/noRedundantRoles: explicit role documents the splitter contract
          role="separator"
          aria-orientation="horizontal"
          aria-label="调整对话与脑图高度"
          aria-valuemin={MIN_LAYER_PERCENT}
          aria-valuemax={Math.round(chatPairTotal - MIN_LAYER_PERCENT)}
          aria-valuenow={Math.round(layerSplits.chat)}
          aria-disabled={chatCollapsed ? true : undefined}
          tabIndex={chatCollapsed ? -1 : 0}
          onPointerDown={(event) => handleDividerPointerDown("chat", event)}
          onPointerMove={(event) => handleDividerPointerMove("chat", event)}
          onPointerUp={() => handleDividerPointerUp("chat")}
          onPointerCancel={() => handleDividerPointerUp("chat")}
          onKeyDown={(event) => handleDividerKeyDown("chat", event)}
          className={cn(
            "group relative m-0 h-2 w-full shrink-0 touch-none select-none border-0 bg-transparent",
            "after:absolute after:left-1/2 after:top-1/2 after:h-1 after:w-12 after:-translate-x-1/2 after:-translate-y-1/2",
            "after:rounded-full after:bg-border after:transition-colors after:content-['']",
            "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
            chatCollapsed
              ? "cursor-not-allowed opacity-60"
              : "cursor-row-resize hover:after:bg-primary/60",
            activeDivider === "chat" && "after:bg-primary",
          )}
        />

        <div
          data-testid="design-layer-mindmap"
          className="flex min-h-0 flex-col overflow-auto [&>section]:min-h-0 [&>section]:flex-1"
          style={mindmapLayerStyle}
        >
          {graph.requirements.length === 0 ? (
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
              selectedCaseId={selectedCaseId}
              confirmingKey={confirmingKey}
              executionResults={executionResults}
              onSelectCase={selectCase}
              onConfirmCase={confirmCase}
              onConfirmScenario={confirmScenario}
              onConfirmRequirement={confirmRequirement}
            />
          )}
        </div>

        <hr
          data-testid="design-divider-mindmap"
          // biome-ignore lint/a11y/noRedundantRoles: explicit role documents the splitter contract
          role="separator"
          aria-orientation="horizontal"
          aria-label="调整脑图与详情高度"
          aria-valuemin={MIN_LAYER_PERCENT}
          aria-valuemax={Math.round(mindmapPairTotal - MIN_LAYER_PERCENT)}
          aria-valuenow={Math.round(layerSplits.mindmap)}
          tabIndex={0}
          onPointerDown={(event) => handleDividerPointerDown("mindmap", event)}
          onPointerMove={(event) => handleDividerPointerMove("mindmap", event)}
          onPointerUp={() => handleDividerPointerUp("mindmap")}
          onPointerCancel={() => handleDividerPointerUp("mindmap")}
          onKeyDown={(event) => handleDividerKeyDown("mindmap", event)}
          className={cn(
            "group relative m-0 h-2 w-full shrink-0 cursor-row-resize touch-none select-none border-0 bg-transparent",
            "after:absolute after:left-1/2 after:top-1/2 after:h-1 after:w-12 after:-translate-x-1/2 after:-translate-y-1/2",
            "after:rounded-full after:bg-border after:transition-colors after:content-[''] hover:after:bg-primary/60",
            "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
            activeDivider === "mindmap" && "after:bg-primary",
          )}
        />

        <div
          data-testid="design-layer-detail"
          className="min-h-0 overflow-auto"
          style={detailLayerStyle}
        >
          <CaseDetailPanel
            versionId={versionId}
            caseId={selectedCaseId}
            refreshToken={detailToken}
            confirming={confirmingKey === `case:${selectedCaseId ?? ""}`}
            onConfirm={confirmCase}
            onRequestChange={setChatDraft}
          />
        </div>
      </div>

      <ExportCasesDialog
        versionId={versionId}
        open={exportOpen}
        onClose={() => setExportOpen(false)}
      />
    </div>
  )
}

/** Re-exported for callers that need the empty-state copy in tests/tooltips. */
export const DESIGN_EMPTY_COPY = EMPTY_REQUIREMENTS_COPY
