"use client"

import { useRouter } from "next/navigation"
import { useCallback, useEffect, useMemo, useState } from "react"

import { cn } from "@/components/ui/cn"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { getVersion } from "@/features/versions/api"
import type { Version } from "@/features/versions/types"

import {
  confirmRequirements,
  createConversation,
  getRequirement,
  listConversations,
  listRequirements,
} from "./api"
import { BulkConfirmBar } from "./BulkConfirmBar"
import { ChatPanel } from "./ChatPanel"
import { DesignCTA } from "./DesignCTA"
import { RequirementEditor } from "./RequirementEditor"
import { RequirementSidebar } from "./RequirementSidebar"
import { TeamRoomPanel } from "./TeamRoomPanel"
import type {
  ConversationMessage,
  ConversationSummary,
  Requirement,
  RequirementDetail,
  RequirementFilter,
  TargetOptions,
  VersionClarificationStats,
} from "./types"
import { useVersionEvents } from "./useVersionEvents"

export type RequirementsTabProps = {
  versionId: string
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

/**
 * P1 "需求与澄清" workspace. Chat is the primary UI; the sidebar/editor handle
 * single and batch confirmation. The URL (`?tab=requirements&conversation=&requirement=`)
 * is the deep-link source of truth and the `version:{id}` SSE stream feeds live
 * messages, requirement refreshes and team-room updates.
 */
export function RequirementsTab({
  versionId,
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

  const [creatingConversation, setCreatingConversation] = useState(false)
  const [createError, setCreateError] = useState<unknown>(null)
  const [liveMessages, setLiveMessages] = useState<ConversationMessage[]>([])
  const [activity, setActivity] = useState<string | null>(null)
  const [teamRoomToken, setTeamRoomToken] = useState(0)

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

  const clarifiedCount = requirements.filter(
    (requirement) => requirement.status === "clarified",
  ).length
  const stats = version as (Version & Partial<VersionClarificationStats>) | null
  const pendingDesignCount = stats?.pending_design_count ?? clarifiedCount

  function selectRequirement(requirementId: string): void {
    setSelectedRequirementId(requirementId)
    setBulkNotice(null)
    router.replace(buildWorkspaceUrl(versionId, activeConversationId, requirementId))
  }

  function selectConversation(conversationId: string): void {
    setActiveConversationId(conversationId)
    setActivity(null)
    router.replace(buildWorkspaceUrl(versionId, conversationId, selectedRequirementId))
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
      router.replace(buildWorkspaceUrl(versionId, conversation.id, null))
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
      if (selectedConfirmed && selectedRequirementId !== null) {
        setDetailToken((token) => token + 1)
      }
    } catch (cause) {
      setBulkError(cause)
    } finally {
      setBulkConfirming(false)
    }
  }

  function requirementUpdated(next: RequirementDetail): void {
    setDetail(next)
    void loadRequirements().catch(() => {})
    void loadVersion().catch(() => {})
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
        />

        <div className="flex min-w-0 flex-col gap-3">
          <DesignCTA pendingCount={pendingDesignCount} />

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
              <BulkConfirmBar
                selectedCount={selectedIds.length}
                confirming={bulkConfirming}
                error={bulkError}
                onConfirm={() => {
                  void bulkConfirm()
                }}
              />
            }
            editor={
              detail !== null ? (
                <RequirementEditor
                  key={`${detail.id}-${detail.current_version_id}`}
                  versionId={versionId}
                  requirement={detail}
                  targetOptions={targetOptions}
                  onUpdated={requirementUpdated}
                />
              ) : null
            }
          />

          <TeamRoomPanel versionId={versionId} refreshToken={teamRoomToken} />
        </div>
      </div>
    </div>
  )
}
