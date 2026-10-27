"use client"

import { Check, Loader2, Plus, RefreshCw, X } from "lucide-react"
import { type FormEvent, useCallback, useEffect, useId, useRef, useState } from "react"

import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"
import { ApiError } from "@/lib/api-client"

import { createDecision, listDecisions, updateDecision } from "./api"
import type {
  ClarificationDecision,
  ClarificationDecisionStatus,
  CreateDecisionBody,
  Requirement,
} from "./types"

/** Decision bounds from spec §6.6. */
const TOPIC_MAX = 300
const DECISION_MAX = 5000
const RATIONALE_MAX = 5000
/** The workbench loads every decision so the status counters are complete. */
const PAGE_SIZE = 100

export const DECISION_STATUS_META: Record<
  ClarificationDecisionStatus,
  { label: string; variant: StatusBadgeVariant }
> = {
  proposed: { label: "待确认", variant: "warning" },
  accepted: { label: "已接受", variant: "success" },
  rejected: { label: "已驳回", variant: "danger" },
}

/** Human labels for the closed decision error reasons (spec §7.1). */
const REASON_LABELS: Record<string, string> = {
  decision_state_conflict: "决策状态已变化，请刷新后重试。",
}

function describeDecisionError(error: unknown): string {
  if (!(error instanceof ApiError)) {
    return error instanceof Error ? error.message : "未知错误"
  }
  if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
  const reason = typeof error.details.reason === "string" ? error.details.reason : ""
  const label = REASON_LABELS[reason]
  if (label !== undefined) return label
  if (reason !== "") return `${error.message}（${reason}）`
  return error.message
}

type StatusFilter = "all" | ClarificationDecisionStatus

const FILTERS: readonly { value: StatusFilter; label: string }[] = [
  { value: "all", label: "全部" },
  { value: "proposed", label: "待确认" },
  { value: "accepted", label: "已接受" },
  { value: "rejected", label: "已驳回" },
]

type DecisionCounts = Record<ClarificationDecisionStatus, number>

function countDecisions(items: ClarificationDecision[]): DecisionCounts {
  const counts: DecisionCounts = { proposed: 0, accepted: 0, rejected: 0 }
  for (const item of items) counts[item.status] += 1
  return counts
}

type DecisionDraft = {
  topic: string
  decision: string
  rationale: string
  requirementId: string
}

const EMPTY_DRAFT: DecisionDraft = {
  topic: "",
  decision: "",
  rationale: "",
  requirementId: "",
}

export type DecisionPanelProps = {
  /** P3-owned version ID; never re-parsed. */
  versionId: string
  /** Requirements used to resolve the linked requirement and the selector. */
  requirements: Requirement[]
  /** Bumped by the workbench when another clarification panel writes. */
  refreshToken?: number
  /** Notifies the workbench after a successful write. */
  onChanged?: () => void
}

/**
 * Decision panel (spec §9.2/§7.6): proposed/accepted/rejected filters with
 * counts, accept/reject for proposed decisions, the linked requirement/question,
 * and manual recording (the backend stores it as accepted/user). A failed write
 * keeps the list on screen and surfaces the reason; AI unavailability does not
 * block manual decisions.
 */
export function DecisionPanel({
  versionId,
  requirements,
  refreshToken = 0,
  onChanged,
}: DecisionPanelProps) {
  const uid = useId()
  const [decisions, setDecisions] = useState<ClarificationDecision[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState("")
  const [filter, setFilter] = useState<StatusFilter>("all")
  const [busyId, setBusyId] = useState<string | null>(null)
  const [actionError, setActionError] = useState("")
  const [creating, setCreating] = useState(false)
  const [saving, setSaving] = useState(false)
  const [draft, setDraft] = useState<DecisionDraft>(EMPTY_DRAFT)
  const [createError, setCreateError] = useState("")
  const requestRef = useRef(0)

  const load = useCallback(async (): Promise<void> => {
    const requestId = ++requestRef.current
    setLoading(true)
    setLoadError("")
    try {
      const page = await listDecisions(versionId, { limit: PAGE_SIZE })
      if (requestRef.current === requestId) setDecisions(page.items)
    } catch (caught) {
      if (requestRef.current === requestId) setLoadError(describeDecisionError(caught))
    } finally {
      if (requestRef.current === requestId) setLoading(false)
    }
  }, [versionId])

  // `refreshToken` is a workbench-provided reload trigger for writes made in
  // another clarification panel; it is intentionally not read inside the effect.
  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshToken restarts the load
  useEffect(() => {
    void load()
    return () => {
      requestRef.current += 1
    }
  }, [load, refreshToken])

  const counts = countDecisions(decisions)
  const visible = filter === "all" ? decisions : decisions.filter((item) => item.status === filter)

  function requirementTitle(requirementId: string): string {
    return (
      requirements.find((requirement) => requirement.id === requirementId)?.title ?? requirementId
    )
  }

  async function decide(decisionId: string, status: "accepted" | "rejected"): Promise<void> {
    setBusyId(decisionId)
    setActionError("")
    try {
      await updateDecision(versionId, decisionId, { status })
      await load()
      onChanged?.()
    } catch (caught) {
      setActionError(describeDecisionError(caught))
    } finally {
      setBusyId(null)
    }
  }

  async function handleCreate(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    const topic = draft.topic.trim()
    const decision = draft.decision.trim()
    if (topic === "") {
      setCreateError("请输入决策主题。")
      return
    }
    if (decision === "") {
      setCreateError("请输入决策内容。")
      return
    }
    const body: CreateDecisionBody = { topic, decision }
    const rationale = draft.rationale.trim()
    if (rationale !== "") body.rationale = rationale
    if (draft.requirementId !== "") body.requirementId = draft.requirementId

    setSaving(true)
    setCreateError("")
    try {
      await createDecision(versionId, body)
      setCreating(false)
      setDraft(EMPTY_DRAFT)
      await load()
      onChanged?.()
    } catch (caught) {
      setCreateError(describeDecisionError(caught))
    } finally {
      setSaving(false)
    }
  }

  return (
    <section aria-label="澄清决策" className="min-w-0 rounded-2xl border border-border bg-card p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h2 className="text-base font-semibold">澄清决策</h2>
          <p className="mt-0.5 text-xs text-muted-foreground">
            接受或驳回 AI 建议，也可人工记录已确认的决策。
          </p>
        </div>
        <div className="flex items-center gap-1.5">
          {loading ? (
            <Loader2
              className="h-3.5 w-3.5 animate-spin text-muted-foreground"
              aria-hidden="true"
            />
          ) : null}
          <button
            type="button"
            onClick={() => {
              setCreating((previous) => !previous)
              setCreateError("")
            }}
            className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-2.5 py-1.5 text-xs font-medium transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <Plus className="h-3.5 w-3.5" aria-hidden="true" />
            新增决策
          </button>
        </div>
      </div>

      <fieldset className="mt-2 flex flex-wrap gap-1 border-0 p-0">
        <legend className="sr-only">决策状态筛选</legend>
        {FILTERS.map((option) => {
          const count = option.value === "all" ? decisions.length : counts[option.value]
          return (
            <button
              key={option.value}
              type="button"
              aria-pressed={filter === option.value}
              onClick={() => setFilter(option.value)}
              className="rounded-lg border border-border px-2.5 py-1 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted aria-pressed:border-primary/40 aria-pressed:bg-primary/10 aria-pressed:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {`${option.label} ${count}`}
            </button>
          )
        })}
      </fieldset>

      {loadError === "" ? null : (
        <div
          role="alert"
          className="mt-2 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
        >
          <p>加载决策失败：{loadError}</p>
          <button
            type="button"
            onClick={() => void load()}
            className="mt-1.5 inline-flex items-center gap-1 rounded-lg border border-border bg-card px-2.5 py-1 text-[11px] font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <RefreshCw className="h-3 w-3" aria-hidden="true" />
            重试
          </button>
        </div>
      )}

      {actionError === "" ? null : (
        <p
          role="alert"
          className="mt-2 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
        >
          {actionError}
        </p>
      )}

      {creating ? (
        <form
          onSubmit={(event) => void handleCreate(event)}
          className="mt-3 space-y-2 rounded-xl border border-border p-3"
        >
          <div>
            <label htmlFor={`${uid}-topic`} className="text-xs font-medium text-muted-foreground">
              决策主题
            </label>
            <input
              id={`${uid}-topic`}
              value={draft.topic}
              maxLength={TOPIC_MAX}
              autoComplete="off"
              placeholder="例如：退款政策"
              onChange={(event) =>
                setDraft((previous) => ({ ...previous, topic: event.target.value }))
              }
              className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
            />
          </div>
          <div>
            <label
              htmlFor={`${uid}-decision`}
              className="text-xs font-medium text-muted-foreground"
            >
              决策内容
            </label>
            <textarea
              id={`${uid}-decision`}
              value={draft.decision}
              rows={2}
              maxLength={DECISION_MAX}
              placeholder="输入已确认的决策…"
              onChange={(event) =>
                setDraft((previous) => ({ ...previous, decision: event.target.value }))
              }
              className="mt-1 w-full resize-y rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
            />
          </div>
          <div>
            <label
              htmlFor={`${uid}-rationale`}
              className="text-xs font-medium text-muted-foreground"
            >
              决策理由
            </label>
            <textarea
              id={`${uid}-rationale`}
              value={draft.rationale}
              rows={2}
              maxLength={RATIONALE_MAX}
              placeholder="可选"
              onChange={(event) =>
                setDraft((previous) => ({ ...previous, rationale: event.target.value }))
              }
              className="mt-1 w-full resize-y rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
            />
          </div>
          <div>
            <label
              htmlFor={`${uid}-requirement`}
              className="text-xs font-medium text-muted-foreground"
            >
              关联需求
            </label>
            <select
              id={`${uid}-requirement`}
              value={draft.requirementId}
              onChange={(event) =>
                setDraft((previous) => ({ ...previous, requirementId: event.target.value }))
              }
              className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <option value="">版本级决策（不绑定需求）</option>
              {requirements.map((requirement) => (
                <option key={requirement.id} value={requirement.id}>
                  {requirement.title}
                </option>
              ))}
            </select>
          </div>
          {createError === "" ? null : (
            <p
              role="alert"
              className="rounded-lg border border-destructive/30 bg-destructive/10 px-2.5 py-1.5 text-xs text-destructive"
            >
              {createError}
            </p>
          )}
          <div className="flex flex-wrap justify-end gap-1.5">
            <button
              type="button"
              onClick={() => {
                setCreating(false)
                setCreateError("")
              }}
              disabled={saving}
              className="rounded-lg border border-border bg-card px-2.5 py-1.5 text-xs font-medium transition-colors hover:bg-muted disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              取消
            </button>
            <button
              type="submit"
              disabled={saving}
              className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {saving ? <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" /> : null}
              保存决策
            </button>
          </div>
        </form>
      ) : null}

      {loading && decisions.length === 0 ? (
        <p className="mt-3 flex items-center gap-1.5 text-xs text-muted-foreground">
          <Loader2 className="h-3 w-3 animate-spin" aria-hidden="true" />
          正在加载决策…
        </p>
      ) : null}

      {!loading && loadError === "" && decisions.length === 0 ? (
        <div className="mt-3 rounded-xl border border-dashed border-border px-3 py-4 text-center">
          <p className="text-sm font-medium">暂无决策</p>
          <p className="mt-1 text-xs text-muted-foreground">
            AI 建议与人工记录的决策会显示在这里。
          </p>
        </div>
      ) : null}

      {!loading && decisions.length > 0 && visible.length === 0 ? (
        <p className="mt-3 text-xs text-muted-foreground">当前筛选下没有决策。</p>
      ) : null}

      {visible.length === 0 ? null : (
        <ul aria-label="决策列表" className="mt-3 space-y-2">
          {visible.map((item) => {
            const meta = DECISION_STATUS_META[item.status]
            const busy = busyId === item.id
            return (
              <li
                key={item.id}
                data-testid={`decision-row-${item.id}`}
                className="rounded-xl border border-border px-3 py-2"
              >
                <div className="flex flex-wrap items-center gap-1.5">
                  <StatusBadge variant={meta.variant}>{meta.label}</StatusBadge>
                  <span className="text-[11px] text-muted-foreground">
                    {item.source === "ai" ? "AI 建议" : "人工记录"}
                  </span>
                </div>
                <p className="mt-1 break-words text-sm font-medium">{item.topic}</p>
                <p className="mt-0.5 break-words text-xs text-muted-foreground">{item.decision}</p>
                {item.rationale === "" ? null : (
                  <p className="mt-0.5 break-words text-xs text-muted-foreground">
                    {`理由：${item.rationale}`}
                  </p>
                )}
                <p className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-[11px] text-muted-foreground">
                  {item.requirementId === null ? (
                    <span>版本级决策</span>
                  ) : (
                    <span>{`关联需求：${requirementTitle(item.requirementId)}`}</span>
                  )}
                  {item.questionId === null ? null : <span>{`关联问题 #${item.questionId}`}</span>}
                </p>

                {item.status === "proposed" ? (
                  <div className="mt-2 flex flex-wrap gap-1.5">
                    <button
                      type="button"
                      onClick={() => void decide(item.id, "accepted")}
                      disabled={busy}
                      className="inline-flex items-center gap-1 rounded-lg bg-primary px-2.5 py-1 text-xs font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    >
                      {busy ? (
                        <Loader2 className="h-3 w-3 animate-spin" aria-hidden="true" />
                      ) : (
                        <Check className="h-3 w-3" aria-hidden="true" />
                      )}
                      接受
                    </button>
                    <button
                      type="button"
                      onClick={() => void decide(item.id, "rejected")}
                      disabled={busy}
                      className="inline-flex items-center gap-1 rounded-lg border border-border bg-card px-2.5 py-1 text-xs font-medium text-destructive transition-colors hover:bg-destructive/10 disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    >
                      <X className="h-3 w-3" aria-hidden="true" />
                      驳回
                    </button>
                  </div>
                ) : null}
              </li>
            )
          })}
        </ul>
      )}
    </section>
  )
}
