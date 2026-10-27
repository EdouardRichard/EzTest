"use client"

import { Loader2, Plus, RefreshCw, Trash2 } from "lucide-react"
import { type FormEvent, useCallback, useEffect, useRef, useState } from "react"

import { ApiError } from "@/lib/api-client"

import {
  createRelation,
  deleteRelation,
  getClarification,
  listRelations,
  listRequirements,
  updateRelation,
} from "./api"
import { ClarificationChat } from "./ClarificationChat"
import { DecisionPanel } from "./DecisionPanel"
import { QuestionPanel } from "./QuestionPanel"
import { RELATION_TYPE_META, RelationGraph } from "./RelationGraph"
import { RequirementEditor } from "./RequirementEditor"
import { RequirementList } from "./RequirementList"
import type {
  ClarificationOverview,
  RelationType,
  Requirement,
  RequirementRelation,
  Version,
} from "./types"
import { useVersionStream } from "./useVersionStream"
import { VersionBindingsForm } from "./VersionBindingsForm"

/** The backend's maximum page size; the list exposes a load-more control. */
const PAGE_SIZE = 100

const RELATION_TYPES = Object.keys(RELATION_TYPE_META) as RelationType[]

/**
 * Clarification event types that change the question/decision panels, the
 * clarification overview counters or the version header counters. A live event
 * of one of these types refreshes every clarification surface. The session
 * lifecycle events are included so a session started or completed elsewhere
 * (another window/user) refreshes the chat's session state as well.
 */
const CLARIFICATION_PANEL_EVENTS: ReadonlySet<string> = new Set([
  "clarification.question_updated",
  "clarification.context_updated",
  "clarification.session_started",
  "clarification.session_completed",
  "decision.proposed",
  "decision.decided",
])

function describeLoadError(error: unknown): string {
  if (!(error instanceof ApiError)) {
    return error instanceof Error ? error.message : "未知错误"
  }
  if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
  return error.message
}

/** Human labels for the closed relation error reasons (spec §7.1). */
const RELATION_REASON_LABELS: Record<string, string> = {
  duplicate_relation: "相同关系已存在。",
  relation_requirement_mismatch: "关系两端必须属于同一版本，且不能是同一需求。",
}

function describeRelationError(error: unknown): string {
  if (!(error instanceof ApiError)) {
    return error instanceof Error ? error.message : "未知错误"
  }
  if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
  const reason = typeof error.details.reason === "string" ? error.details.reason : ""
  const label = RELATION_REASON_LABELS[reason]
  if (label !== undefined) return label
  if (reason !== "") return `${error.message}（${reason}）`
  return error.message
}

type RelationDraft = {
  fromRequirementId: string
  toRequirementId: string
  relationType: RelationType
  description: string
}

const EMPTY_RELATION_DRAFT: RelationDraft = {
  fromRequirementId: "",
  toRequirementId: "",
  relationType: "depends_on",
  description: "",
}

type RelationEditState = {
  relationType: RelationType
  description: string
}

export type WorkbenchProps = {
  version: Version
  /** Refreshes the owning container (header counters) after a write. */
  onChanged: () => void
}

/**
 * Requirement workbench (spec §9.2): requirement list + create, the editor with
 * attachments, the relation graph with its management form, and the binding
 * editor. T18 appends the clarification column beside these sections.
 *
 * Data is loaded with cancelled-request guards, successful writes refresh the
 * affected data and notify the container, and load failures keep the last data
 * on screen.
 */
export function Workbench({ version, onChanged }: WorkbenchProps) {
  const [requirements, setRequirements] = useState<Requirement[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState("")
  const [hasMore, setHasMore] = useState(false)
  const [nextCursor, setNextCursor] = useState("")
  const [loadingMore, setLoadingMore] = useState(false)
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)

  const [relations, setRelations] = useState<RequirementRelation[]>([])
  const [relationsLoading, setRelationsLoading] = useState(true)
  const [relationsError, setRelationsError] = useState("")

  const [clarification, setClarification] = useState<ClarificationOverview | null>(null)
  const [clarificationLoading, setClarificationLoading] = useState(true)
  const [clarificationError, setClarificationError] = useState("")
  const [clarificationRevision, setClarificationRevision] = useState(0)

  const [relationDraft, setRelationDraft] = useState<RelationDraft>(EMPTY_RELATION_DRAFT)
  const [editingRelationId, setEditingRelationId] = useState<string | null>(null)
  const [relationEdit, setRelationEdit] = useState<RelationEditState>({
    relationType: "depends_on",
    description: "",
  })
  const [deletingRelationId, setDeletingRelationId] = useState<string | null>(null)
  const [relationSaving, setRelationSaving] = useState(false)
  const [relationError, setRelationError] = useState("")

  const requirementsRequestRef = useRef(0)
  const relationsRequestRef = useRef(0)
  const clarificationRequestRef = useRef(0)
  /** Highest stream seq already inspected for a clarification panel refresh. */
  const streamEventSeqRef = useRef<number | null>(null)

  const stream = useVersionStream(version.id)

  const loadRequirements = useCallback(
    async (cursor?: string): Promise<void> => {
      const requestId = ++requirementsRequestRef.current
      const append = cursor !== undefined && cursor !== ""
      if (append) setLoadingMore(true)
      else setLoading(true)
      setLoadError("")
      try {
        const page = await listRequirements(
          version.id,
          append ? { limit: PAGE_SIZE, cursor } : { limit: PAGE_SIZE },
        )
        if (requirementsRequestRef.current !== requestId) return
        setRequirements((previous) => {
          if (!append) return page.items
          const known = new Set(previous.map((item) => item.id))
          return [...previous, ...page.items.filter((item) => !known.has(item.id))]
        })
        setNextCursor(page.nextCursor)
        setHasMore(page.hasMore && page.nextCursor !== "")
        if (!append) {
          setSelectedId((previous) => {
            if (previous !== null && page.items.some((item) => item.id === previous)) {
              return previous
            }
            return page.items[0]?.id ?? null
          })
        }
      } catch (caught) {
        if (requirementsRequestRef.current === requestId) {
          setLoadError(describeLoadError(caught))
        }
      } finally {
        if (requirementsRequestRef.current === requestId) {
          setLoading(false)
          setLoadingMore(false)
        }
      }
    },
    [version.id],
  )

  const loadRelations = useCallback(async (): Promise<void> => {
    const requestId = ++relationsRequestRef.current
    setRelationsLoading(true)
    setRelationsError("")
    try {
      const items = await listRelations(version.id)
      if (relationsRequestRef.current === requestId) setRelations(items)
    } catch (caught) {
      if (relationsRequestRef.current === requestId) {
        setRelationsError(describeLoadError(caught))
      }
    } finally {
      if (relationsRequestRef.current === requestId) setRelationsLoading(false)
    }
  }, [version.id])

  const loadClarification = useCallback(async (): Promise<void> => {
    const requestId = ++clarificationRequestRef.current
    setClarificationLoading(true)
    setClarificationError("")
    try {
      const overview = await getClarification(version.id)
      if (clarificationRequestRef.current === requestId) setClarification(overview)
    } catch (caught) {
      if (clarificationRequestRef.current === requestId) {
        setClarificationError(describeLoadError(caught))
      }
    } finally {
      if (clarificationRequestRef.current === requestId) setClarificationLoading(false)
    }
  }, [version.id])

  useEffect(() => {
    setRequirements([])
    setSelectedId(null)
    setCreating(false)
    setRelations([])
    setClarification(null)
    setClarificationError("")
    streamEventSeqRef.current = null
    void loadRequirements()
    void loadRelations()
    void loadClarification()
    return () => {
      // Invalidate every request started for this version.
      requirementsRequestRef.current += 1
      relationsRequestRef.current += 1
      clarificationRequestRef.current += 1
    }
  }, [loadRequirements, loadRelations, loadClarification])

  // AI (and human) writes arrive as version events. The workbench owns the
  // stream, so it must refresh the question/decision panels and the overview
  // counters itself; otherwise the chat updates while the panels stay stale
  // until a page refresh. The first batch is the persisted snapshot (loaded
  // while the stream is still connecting) and is already reflected in the
  // initial loads; a batch that arrives after `live` is new and refreshes.
  useEffect(() => {
    const versionEvents = stream.events.filter(
      (event) => event.version_id === null || event.version_id === version.id,
    )
    if (versionEvents.length === 0) {
      streamEventSeqRef.current = null
      return
    }
    const maxSeq = versionEvents.reduce((latest, event) => Math.max(latest, event.seq), 0)
    const previousSeq = streamEventSeqRef.current
    streamEventSeqRef.current = maxSeq
    const changed = versionEvents.some(
      (event) => event.seq > (previousSeq ?? -1) && CLARIFICATION_PANEL_EVENTS.has(event.type),
    )
    if (!changed) return
    if (previousSeq === null && stream.status !== "live") return
    setClarificationRevision((previous) => previous + 1)
    void loadClarification()
    onChanged()
  }, [stream.events, stream.status, version.id, loadClarification, onChanged])

  function reloadAll(): void {
    // A manual refresh must refresh the AI-driven panels as well; their only
    // other reload trigger is the stream event watcher above.
    setClarificationRevision((previous) => previous + 1)
    void loadRequirements()
    void loadRelations()
    void loadClarification()
  }

  /** Reload everything and refresh the container's version counters. */
  function reloadWithContainer(): void {
    reloadAll()
    onChanged()
  }

  /**
   * A clarification write changes the version counters and may update the
   * status of questions linked from decisions, so every clarification surface
   * (and the container header) is refreshed.
   */
  function handleClarificationChanged(): void {
    setClarificationRevision((previous) => previous + 1)
    void loadClarification()
    onChanged()
  }

  function handleCreated(created: Requirement): void {
    setCreating(false)
    setSelectedId(created.id)
    setRequirements((previous) => {
      if (previous.some((item) => item.id === created.id)) {
        return previous.map((item) => (item.id === created.id ? created : item))
      }
      return [...previous, created]
    })
    onChanged()
  }

  function handleUpdated(updated: Requirement): void {
    setRequirements((previous) => previous.map((item) => (item.id === updated.id ? updated : item)))
    onChanged()
  }

  function handleDeleted(requirementId: string): void {
    const remaining = requirements.filter((item) => item.id !== requirementId)
    setRequirements(remaining)
    setSelectedId((previous) =>
      previous === requirementId ? (remaining[0]?.id ?? null) : previous,
    )
    // Deleting a requirement cascades its relations and changes the relation
    // badges of the remaining requirements, so both lists need a fresh read.
    void loadRequirements()
    void loadRelations()
    onChanged()
  }

  function handleAttachmentsChanged(): void {
    void loadRequirements()
    onChanged()
  }

  async function handleCreateRelation(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    const from = relationDraft.fromRequirementId
    const to = relationDraft.toRequirementId
    if (from === "" || to === "") {
      setRelationError("请选择关系两端的需求。")
      return
    }
    if (from === to) {
      setRelationError("关系两端不能是同一需求。")
      return
    }

    setRelationSaving(true)
    setRelationError("")
    try {
      await createRelation(version.id, {
        fromRequirementId: from,
        toRequirementId: to,
        relationType: relationDraft.relationType,
        description: relationDraft.description.trim(),
      })
      setRelationDraft(EMPTY_RELATION_DRAFT)
      await loadRelations()
      void loadRequirements()
    } catch (caught) {
      setRelationError(describeRelationError(caught))
    } finally {
      setRelationSaving(false)
    }
  }

  function startEditRelation(relation: RequirementRelation): void {
    setEditingRelationId(relation.id)
    setDeletingRelationId(null)
    setRelationEdit({
      relationType: relation.relationType,
      description: relation.description,
    })
    setRelationError("")
  }

  async function handleUpdateRelation(relationId: string): Promise<void> {
    setRelationSaving(true)
    setRelationError("")
    try {
      await updateRelation(version.id, relationId, {
        relationType: relationEdit.relationType,
        description: relationEdit.description.trim(),
      })
      setEditingRelationId(null)
      await loadRelations()
      void loadRequirements()
    } catch (caught) {
      setRelationError(describeRelationError(caught))
    } finally {
      setRelationSaving(false)
    }
  }

  async function handleDeleteRelation(relationId: string): Promise<void> {
    setRelationSaving(true)
    setRelationError("")
    try {
      await deleteRelation(version.id, relationId)
      setDeletingRelationId(null)
      await loadRelations()
      void loadRequirements()
    } catch (caught) {
      setRelationError(describeRelationError(caught))
    } finally {
      setRelationSaving(false)
    }
  }

  const selectedRequirement =
    !creating && selectedId !== null
      ? (requirements.find((item) => item.id === selectedId) ?? null)
      : null

  function requirementTitle(requirementId: string): string {
    return requirements.find((item) => item.id === requirementId)?.title ?? requirementId
  }

  return (
    <section
      aria-label="需求与澄清工作台"
      data-testid="requirements-workbench-slot"
      className="space-y-4"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h2 className="text-lg font-semibold">需求与澄清工作台</h2>
          <p className="mt-0.5 text-xs text-muted-foreground">
            维护需求、关系、附件与资源绑定；右侧为澄清对话、问题与决策。
          </p>
        </div>
        <button
          type="button"
          onClick={reloadWithContainer}
          disabled={loading}
          className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          {loading ? (
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
          ) : (
            <RefreshCw className="h-4 w-4" aria-hidden="true" />
          )}
          刷新工作台
        </button>
      </div>

      <div className="grid gap-4 lg:grid-cols-[minmax(0,20rem)_minmax(0,1fr)]">
        <RequirementList
          requirements={requirements}
          selectedId={selectedId}
          onSelect={(requirementId) => {
            setSelectedId(requirementId)
            setCreating(false)
          }}
          onCreate={() => {
            setCreating(true)
            setRelationError("")
          }}
          creating={creating}
          loading={loading}
          error={loadError}
          onRetry={() => void loadRequirements()}
          hasMore={hasMore}
          loadingMore={loadingMore}
          onLoadMore={() => void loadRequirements(nextCursor)}
        />

        <RequirementEditor
          versionId={version.id}
          requirement={selectedRequirement}
          creating={creating}
          onCreated={handleCreated}
          onUpdated={handleUpdated}
          onDeleted={handleDeleted}
          onCancelCreate={() => setCreating(false)}
          onReload={reloadWithContainer}
          onAttachmentsChanged={handleAttachmentsChanged}
        />
      </div>

      <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_minmax(0,26rem)]">
        <RelationGraph
          requirements={requirements}
          relations={relations}
          loading={relationsLoading}
          error={relationsError}
          onSelect={(requirementId) => {
            setSelectedId(requirementId)
            setCreating(false)
          }}
        />

        <section
          aria-label="需求关系管理"
          className="min-w-0 rounded-2xl border border-border bg-card p-4"
        >
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h2 className="text-base font-semibold">需求关系管理</h2>
            {relationsLoading ? (
              <span className="flex items-center gap-1 text-xs text-muted-foreground">
                <Loader2 className="h-3 w-3 animate-spin" aria-hidden="true" />
                加载中…
              </span>
            ) : null}
          </div>

          {relationsError === "" ? null : (
            <div
              role="alert"
              className="mt-2 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
            >
              <p>加载关系失败：{relationsError}</p>
              <button
                type="button"
                onClick={() => void loadRelations()}
                className="mt-1.5 rounded-lg border border-border bg-card px-2.5 py-1 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                重试
              </button>
            </div>
          )}

          {requirements.length < 2 ? (
            <p className="mt-3 rounded-xl border border-dashed border-border px-3 py-4 text-xs text-muted-foreground">
              至少需要两条需求才能建立关系。
            </p>
          ) : (
            <form onSubmit={(event) => void handleCreateRelation(event)} className="mt-3 space-y-2">
              <div className="grid gap-2 sm:grid-cols-2">
                <div>
                  <label
                    htmlFor="relation-from"
                    className="text-[11px] font-medium text-muted-foreground"
                  >
                    起点需求
                  </label>
                  <select
                    id="relation-from"
                    value={relationDraft.fromRequirementId}
                    onChange={(event) =>
                      setRelationDraft((previous) => ({
                        ...previous,
                        fromRequirementId: event.target.value,
                      }))
                    }
                    className="mt-1 w-full rounded-lg border border-border bg-input px-2.5 py-1.5 text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    <option value="">选择需求</option>
                    {requirements.map((requirement) => (
                      <option key={requirement.id} value={requirement.id}>
                        {requirement.title}
                      </option>
                    ))}
                  </select>
                </div>

                <div>
                  <label
                    htmlFor="relation-to"
                    className="text-[11px] font-medium text-muted-foreground"
                  >
                    终点需求
                  </label>
                  <select
                    id="relation-to"
                    value={relationDraft.toRequirementId}
                    onChange={(event) =>
                      setRelationDraft((previous) => ({
                        ...previous,
                        toRequirementId: event.target.value,
                      }))
                    }
                    className="mt-1 w-full rounded-lg border border-border bg-input px-2.5 py-1.5 text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    <option value="">选择需求</option>
                    {requirements.map((requirement) => (
                      <option key={requirement.id} value={requirement.id}>
                        {requirement.title}
                      </option>
                    ))}
                  </select>
                </div>

                <div>
                  <label
                    htmlFor="relation-type"
                    className="text-[11px] font-medium text-muted-foreground"
                  >
                    关系类型
                  </label>
                  <select
                    id="relation-type"
                    value={relationDraft.relationType}
                    onChange={(event) =>
                      setRelationDraft((previous) => ({
                        ...previous,
                        relationType: event.target.value as RelationType,
                      }))
                    }
                    className="mt-1 w-full rounded-lg border border-border bg-input px-2.5 py-1.5 text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    {RELATION_TYPES.map((type) => (
                      <option key={type} value={type}>
                        {RELATION_TYPE_META[type].label}
                      </option>
                    ))}
                  </select>
                </div>

                <div>
                  <label
                    htmlFor="relation-description"
                    className="text-[11px] font-medium text-muted-foreground"
                  >
                    关系说明
                  </label>
                  <input
                    id="relation-description"
                    value={relationDraft.description}
                    autoComplete="off"
                    placeholder="可选"
                    onChange={(event) =>
                      setRelationDraft((previous) => ({
                        ...previous,
                        description: event.target.value,
                      }))
                    }
                    className="mt-1 w-full rounded-lg border border-border bg-input px-2.5 py-1.5 text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  />
                </div>
              </div>

              <button
                type="submit"
                disabled={relationSaving}
                className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                {relationSaving ? (
                  <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
                ) : (
                  <Plus className="h-3.5 w-3.5" aria-hidden="true" />
                )}
                新增关系
              </button>
            </form>
          )}

          {relationError === "" ? null : (
            <p
              role="alert"
              className="mt-2 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
            >
              {relationError}
            </p>
          )}

          {relations.length === 0 ? (
            <p className="mt-3 text-xs text-muted-foreground">
              暂无关系；可新增依赖、冲突、重叠或共享范围。
            </p>
          ) : (
            <ul aria-label="关系列表" className="mt-3 space-y-2">
              {relations.map((relation) => {
                const meta = RELATION_TYPE_META[relation.relationType]
                const fromTitle = requirementTitle(relation.fromRequirementId)
                const toTitle = requirementTitle(relation.toRequirementId)
                const editing = editingRelationId === relation.id
                return (
                  <li
                    key={relation.id}
                    data-testid={`relation-item-${relation.id}`}
                    className="rounded-xl border border-border px-2.5 py-2"
                  >
                    {editing ? (
                      <div className="space-y-2">
                        <div className="grid gap-2 sm:grid-cols-[minmax(0,10rem)_minmax(0,1fr)]">
                          <div>
                            <label
                              htmlFor={`relation-edit-type-${relation.id}`}
                              className="text-[11px] font-medium text-muted-foreground"
                            >
                              编辑关系类型
                            </label>
                            <select
                              id={`relation-edit-type-${relation.id}`}
                              value={relationEdit.relationType}
                              onChange={(event) =>
                                setRelationEdit((previous) => ({
                                  ...previous,
                                  relationType: event.target.value as RelationType,
                                }))
                              }
                              className="mt-1 w-full rounded-lg border border-border bg-input px-2.5 py-1.5 text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring"
                            >
                              {RELATION_TYPES.map((type) => (
                                <option key={type} value={type}>
                                  {RELATION_TYPE_META[type].label}
                                </option>
                              ))}
                            </select>
                          </div>
                          <div>
                            <label
                              htmlFor={`relation-edit-description-${relation.id}`}
                              className="text-[11px] font-medium text-muted-foreground"
                            >
                              编辑关系说明
                            </label>
                            <input
                              id={`relation-edit-description-${relation.id}`}
                              value={relationEdit.description}
                              autoComplete="off"
                              onChange={(event) =>
                                setRelationEdit((previous) => ({
                                  ...previous,
                                  description: event.target.value,
                                }))
                              }
                              className="mt-1 w-full rounded-lg border border-border bg-input px-2.5 py-1.5 text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring"
                            />
                          </div>
                        </div>
                        <div className="flex flex-wrap gap-1.5">
                          <button
                            type="button"
                            onClick={() => void handleUpdateRelation(relation.id)}
                            disabled={relationSaving}
                            className="rounded-lg bg-primary px-2.5 py-1 text-xs font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                          >
                            保存关系
                          </button>
                          <button
                            type="button"
                            onClick={() => setEditingRelationId(null)}
                            disabled={relationSaving}
                            className="rounded-lg border border-border bg-card px-2.5 py-1 text-xs font-medium transition-colors hover:bg-muted disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                          >
                            取消编辑关系
                          </button>
                        </div>
                      </div>
                    ) : (
                      <div className="flex flex-wrap items-start justify-between gap-2">
                        <div className="min-w-0">
                          <p className="break-all text-xs font-medium">
                            {`${fromTitle} ${meta.directed ? "→" : "—"} ${toTitle}`}
                          </p>
                          <p className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[11px] text-muted-foreground">
                            <span>{meta.label}</span>
                            {relation.source === "ai" ? <span>AI 建议</span> : null}
                            {relation.description === "" ? null : (
                              <span className="break-all">{relation.description}</span>
                            )}
                          </p>
                        </div>
                        <div className="flex shrink-0 flex-wrap gap-1">
                          <button
                            type="button"
                            onClick={() => startEditRelation(relation)}
                            disabled={relationSaving}
                            className="rounded-lg border border-border bg-card px-2.5 py-1 text-[11px] font-medium transition-colors hover:bg-muted disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                          >
                            编辑关系
                          </button>
                          <button
                            type="button"
                            onClick={() => setDeletingRelationId(relation.id)}
                            disabled={relationSaving}
                            className="inline-flex items-center gap-1 rounded-lg border border-border bg-card px-2.5 py-1 text-[11px] font-medium text-destructive transition-colors hover:bg-destructive/10 disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                          >
                            <Trash2 className="h-3 w-3" aria-hidden="true" />
                            删除关系
                          </button>
                        </div>
                      </div>
                    )}

                    {deletingRelationId === relation.id ? (
                      <div className="mt-2 flex flex-wrap items-center gap-1.5 rounded-lg border border-destructive/30 bg-destructive/10 px-2.5 py-1.5">
                        <span className="mr-auto text-[11px] text-destructive">
                          确认删除该关系？
                        </span>
                        <button
                          type="button"
                          onClick={() => void handleDeleteRelation(relation.id)}
                          disabled={relationSaving}
                          className="rounded-lg bg-destructive px-2.5 py-1 text-[11px] font-medium text-white transition-opacity hover:opacity-90 disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                        >
                          确认删除关系
                        </button>
                        <button
                          type="button"
                          onClick={() => setDeletingRelationId(null)}
                          disabled={relationSaving}
                          className="rounded-lg border border-border bg-card px-2.5 py-1 text-[11px] font-medium transition-colors hover:bg-muted disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                        >
                          取消删除关系
                        </button>
                      </div>
                    ) : null}
                  </li>
                )
              })}
            </ul>
          )}
        </section>
      </div>

      <section
        aria-label="澄清与决策"
        className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_minmax(0,28rem)]"
      >
        <div className="min-w-0 space-y-2">
          {clarificationError === "" ? null : (
            <div
              role="alert"
              className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
            >
              <p>加载澄清会话失败：{clarificationError}</p>
              <button
                type="button"
                onClick={() => void loadClarification()}
                className="mt-1.5 rounded-lg border border-border bg-card px-2.5 py-1 text-[11px] font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                重试
              </button>
            </div>
          )}
          <ClarificationChat
            versionId={version.id}
            session={clarification?.session ?? null}
            events={stream.events}
            status={stream.status}
            sessionLoading={clarificationLoading}
            sessionError={clarificationError}
            onChanged={handleClarificationChanged}
            onReconnect={stream.reconnect}
          />
        </div>
        <div className="min-w-0 space-y-4">
          <QuestionPanel
            versionId={version.id}
            requirements={requirements}
            refreshToken={clarificationRevision}
            counts={clarification?.questions ?? null}
            onChanged={handleClarificationChanged}
          />
          <DecisionPanel
            versionId={version.id}
            requirements={requirements}
            refreshToken={clarificationRevision}
            counts={clarification?.decisions ?? null}
            onChanged={handleClarificationChanged}
          />
        </div>
      </section>

      <VersionBindingsForm version={version} onChanged={onChanged} />
    </section>
  )
}
