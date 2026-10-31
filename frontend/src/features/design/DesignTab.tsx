"use client"

import { useRouter } from "next/navigation"
import { useCallback, useEffect, useRef, useState } from "react"

import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import type { ConversationSummary } from "@/features/clarification/types"
import { useVersionEvents } from "@/features/clarification/useVersionEvents"

import {
  confirmCases,
  createDesignConversation,
  getDesignGraph,
  getDesignOverview,
  listDesignConversations,
} from "./api"
import { CaseDetailPanel } from "./CaseDetailPanel"
import { DesignMindmap } from "./DesignMindmap"
import type { CaseConfirmationScope, DesignGraph, DesignOverview } from "./types"

export type DesignTabProps = {
  versionId: string
  /** Deep link `?case=` forwarded by the version page. */
  initialCaseId?: string
  /** Deep link `?conversation=` forwarded by the version page. */
  initialConversationId?: string
}

const EMPTY_REQUIREMENTS_COPY = "暂无已澄清需求，请先在需求与澄清中确认需求"

/**
 * P2 design workspace shell: React Flow mindmap + design chat (top row) and the
 * full-width case detail (bottom row). The URL is the deep-link source of truth
 * and the `version:{id}` SSE channel drives debounced snapshot refreshes; node
 * positions stay local (`layout.ts`) and are never persisted.
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

  const refreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

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

  function selectCase(caseId: string): void {
    setSelectedCaseId(caseId)
    updateUrl(caseId, activeConversationId)
  }

  async function handleNewConversation(): Promise<void> {
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
  }

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
  const activeConversation = conversations.find(
    (conversation) => conversation.id === activeConversationId,
  )
  const roundTitle = overview?.active_round
    ? `设计轮次 #${overview.active_round.round_no}`
    : "暂无设计轮次"

  return (
    <div data-testid="design-tab" className="space-y-3">
      <header className="flex flex-wrap items-center gap-2">
        <h1 className="text-lg font-semibold text-foreground">用例脑图 · {roundTitle}</h1>
        <span data-testid="design-stats" className="text-[11px] text-muted-foreground">
          需求 {stats.requirements} · 场景 {stats.scenarios} · 用例 {stats.cases} · draft{" "}
          {stats.draft} · confirmed {stats.confirmed} · stale {stats.stale}
        </span>
        <div className="ml-auto flex flex-wrap items-center gap-1.5">
          <Button size="sm" variant="secondary" data-testid="export-cases">
            导出用例
          </Button>
          <button
            type="button"
            data-testid="auto-design-switch"
            aria-pressed={autoDesign}
            disabled
            title="自动设计开关将在下一步接入"
            className="inline-flex h-8 items-center rounded-md border border-border px-2.5 text-xs text-muted-foreground disabled:opacity-60"
          >
            新需求自动设计：{autoDesign ? "开" : "关"}
          </button>
          <Button
            size="sm"
            data-testid="design-new-conversation"
            disabled={creatingConversation}
            onClick={() => {
              void handleNewConversation()
            }}
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
      {staleNotice !== null ? (
        <p
          data-testid="design-stale-notice"
          role="status"
          className="rounded-lg border border-warning/30 bg-warning/10 px-3 py-2 text-[11px] text-warning"
        >
          {staleNotice}
        </p>
      ) : null}

      <div className="grid gap-3 lg:grid-cols-[minmax(0,1fr)_380px]">
        <div className="min-w-0">
          {graph.requirements.length === 0 ? (
            <div className="surface-card flex min-h-[560px] items-center justify-center p-6">
              <EmptyState
                title={EMPTY_REQUIREMENTS_COPY}
                description="需求确认后，新建设计对话即可让设计团队扫描并生成用例。"
                action={
                  <Button
                    size="sm"
                    disabled={creatingConversation}
                    onClick={() => {
                      void handleNewConversation()
                    }}
                  >
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
              onSelectCase={selectCase}
              onConfirmCase={(caseId) => {
                void handleConfirm("case", caseId)
              }}
              onConfirmScenario={(scenarioId) => {
                void handleConfirm("scenario", scenarioId)
              }}
              onConfirmRequirement={(requirementId) => {
                void handleConfirm("requirement", requirementId)
              }}
            />
          )}
        </div>

        <section
          data-testid="design-chat"
          aria-label="设计对话"
          className="surface-card flex min-h-[560px] flex-col p-3"
        >
          <header className="flex items-center gap-2 border-b border-border pb-2">
            <h2 className="text-sm font-semibold text-foreground">设计对话</h2>
            <span
              data-testid="design-sse-status"
              className="ml-auto text-[10px] text-muted-foreground"
            >
              {sseStatus}
            </span>
          </header>
          <div className="flex flex-1 flex-col gap-2 py-3">
            <p className="text-[11px] text-muted-foreground">
              {activeConversation?.title ?? "暂无设计对话"}
            </p>
            {chatDraft.length > 0 ? (
              <p
                data-testid="design-chat-draft"
                className="rounded-lg border border-border bg-muted px-2 py-1 text-[11px] text-foreground"
              >
                {chatDraft}
              </p>
            ) : null}
          </div>
        </section>

        <div className="lg:col-span-2">
          <CaseDetailPanel
            versionId={versionId}
            caseId={selectedCaseId}
            refreshToken={detailToken}
            confirming={confirmingKey === `case:${selectedCaseId ?? ""}`}
            onConfirm={(caseId) => {
              void handleConfirm("case", caseId)
            }}
            onRequestChange={(title) => setChatDraft(title)}
          />
        </div>
      </div>
    </div>
  )
}

/** Re-exported for callers that need the empty-state copy in tests/tooltips. */
export const DESIGN_EMPTY_COPY = EMPTY_REQUIREMENTS_COPY
