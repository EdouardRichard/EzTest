"use client"

import { Check, Loader2, RefreshCw, ShieldCheck, X } from "lucide-react"
import { useCallback, useEffect, useState } from "react"

import { ApiError } from "@/lib/api-client"

import { listGlobalKnowledge, reviewKnowledge } from "./api"
import { type KnowledgeEntry, knowledgeConfidenceLabel, knowledgeKindLabel } from "./types"

/** Number of draft entries loaded into the review queue. */
const REVIEW_QUEUE_LIMIT = 50

export type ReviewDrawerProps = {
  open: boolean
  /** Draft entry to preselect (`/knowledge?review=<id>` deep link). */
  focusId?: string
  onClose: () => void
  /** Called after one approve/reject so the owner can refresh its list. */
  onReviewed?: () => void
}

/** Renders a load/submit failure without leaking anything but the message. */
function describeError(error: unknown): string {
  if (error instanceof ApiError) {
    const requestId = error.requestId === "" ? "" : `（request id: ${error.requestId}）`
    if (error.status === 0) return `无法连接 API 服务，请确认后端已启动。${requestId}`
    return `${error.message}${requestId}`
  }
  return error instanceof Error ? error.message : "未知错误"
}

/**
 * Admin review drawer for the version-knowledge draft queue (design spec §9.4):
 * lists the pending drafts, shows the selected entry, approves or rejects it at
 * the entry revision, and requires a non-empty reason for a rejection. The
 * backend enforces the admin role; this drawer is only mounted for the admin UI.
 */
export function ReviewDrawer({ open, focusId = "", onClose, onReviewed }: ReviewDrawerProps) {
  const [items, setItems] = useState<KnowledgeEntry[]>([])
  const [selectedId, setSelectedId] = useState("")
  const [loading, setLoading] = useState(false)
  const [loadError, setLoadError] = useState("")
  const [reason, setReason] = useState("")
  const [reasonError, setReasonError] = useState("")
  const [submitError, setSubmitError] = useState("")
  const [success, setSuccess] = useState("")
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    setLoadError("")
    try {
      const page = await listGlobalKnowledge({ status: "draft", limit: REVIEW_QUEUE_LIMIT })
      setItems(page.items)
      setSelectedId((current) => {
        if (focusId !== "" && page.items.some((item) => item.id === focusId)) return focusId
        if (current !== "" && page.items.some((item) => item.id === current)) return current
        return page.items[0]?.id ?? ""
      })
    } catch (caught) {
      setLoadError(describeError(caught))
    } finally {
      setLoading(false)
    }
  }, [focusId])

  useEffect(() => {
    if (!open) return
    setReason("")
    setReasonError("")
    setSubmitError("")
    setSuccess("")
    void load()
  }, [open, load])

  if (!open) return null

  const selected = items.find((item) => item.id === selectedId) ?? null

  function selectEntry(id: string): void {
    setSelectedId(id)
    setReason("")
    setReasonError("")
    setSubmitError("")
    setSuccess("")
  }

  async function submit(approve: boolean): Promise<void> {
    if (selected === null || busy) return
    if (!approve && reason.trim() === "") {
      setReasonError("驳回必须填写原因")
      return
    }
    setBusy(true)
    setSubmitError("")
    setSuccess("")
    try {
      const body = approve
        ? { revision: selected.revision, status: "approved" as const }
        : {
            revision: selected.revision,
            status: "rejected" as const,
            reason: reason.trim(),
          }
      const reviewed = await reviewKnowledge(selected.versionId, selected.id, body)
      const remaining = items.filter((item) => item.id !== reviewed.id)
      setItems(remaining)
      setSelectedId(remaining[0]?.id ?? "")
      setReason("")
      setReasonError("")
      setSuccess(approve ? "已批准，条目已进入全局知识库。" : "已驳回并记录原因。")
      onReviewed?.()
    } catch (caught) {
      setSubmitError(describeError(caught))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div
      role="dialog"
      aria-label="知识审核"
      aria-busy={loading || busy || undefined}
      data-testid="knowledge-review-drawer"
      className="fixed inset-0 z-50 flex justify-end bg-black/30"
    >
      <div className="flex h-full w-full max-w-2xl flex-col overflow-hidden border-l border-border bg-card shadow-2xl">
        <div className="flex items-center justify-between border-b border-border px-4 py-3">
          <div className="flex items-center gap-2">
            <ShieldCheck className="h-4 w-4 text-sky-500" aria-hidden="true" />
            <p className="text-sm font-semibold">知识审核</p>
            <span className="rounded-full border border-border bg-muted px-2 py-0.5 text-[10px] text-muted-foreground">
              {items.length} 条待审核
            </span>
          </div>
          <div className="flex items-center gap-1">
            <button
              type="button"
              onClick={() => void load()}
              aria-label="刷新审核队列"
              disabled={loading}
              className="grid h-8 w-8 place-items-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground disabled:opacity-50"
            >
              <RefreshCw
                className={loading ? "h-3.5 w-3.5 animate-spin" : "h-3.5 w-3.5"}
                aria-hidden="true"
              />
            </button>
            <button
              type="button"
              onClick={onClose}
              aria-label="关闭审核抽屉"
              className="grid h-8 w-8 place-items-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground"
            >
              <X className="h-3.5 w-3.5" aria-hidden="true" />
            </button>
          </div>
        </div>

        <div className="grid min-h-0 flex-1 grid-rows-[minmax(0,1fr)_minmax(0,1.4fr)] divide-y divide-border md:grid-cols-2 md:grid-rows-1 md:divide-x md:divide-y-0">
          <section aria-label="待审核队列" className="min-h-0 overflow-y-auto p-3">
            {loading && items.length === 0 ? (
              <p
                role="status"
                className="flex items-center gap-2 px-2 py-4 text-sm text-muted-foreground"
              >
                <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
                正在加载待审核知识…
              </p>
            ) : loadError !== "" ? (
              <div
                role="alert"
                data-testid="knowledge-review-load-error"
                className="rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
              >
                <p>加载审核队列失败：{loadError}</p>
                <button
                  type="button"
                  onClick={() => void load()}
                  className="mt-2 rounded-md border border-border bg-card px-2.5 py-1 text-xs font-medium text-foreground"
                >
                  重试
                </button>
              </div>
            ) : items.length === 0 ? (
              <p className="px-2 py-6 text-center text-sm text-muted-foreground">暂无待审核知识</p>
            ) : (
              <ul className="space-y-1">
                {items.map((item) => (
                  <li key={item.id}>
                    <button
                      type="button"
                      data-testid={`knowledge-review-item-${item.id}`}
                      onClick={() => selectEntry(item.id)}
                      aria-current={item.id === selectedId ? "true" : undefined}
                      className="w-full rounded-lg border border-transparent px-2.5 py-2 text-left hover:border-border hover:bg-muted/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring aria-[current=true]:border-border aria-[current=true]:bg-muted/60"
                    >
                      <span className="block truncate text-sm font-medium">{item.title}</span>
                      <span className="mt-0.5 block truncate text-[11px] text-muted-foreground">
                        {item.versionName || `版本 ${item.versionId}`} ·{" "}
                        {knowledgeKindLabel(item.kind)}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </section>

          <section aria-label="审核详情" className="min-h-0 overflow-y-auto p-4">
            {selected === null ? (
              <p className="text-sm text-muted-foreground">选择左侧草稿查看详情并审核。</p>
            ) : (
              <div className="space-y-3">
                <header>
                  <p className="text-base font-semibold">{selected.title}</p>
                  <p className="mt-1 text-xs text-muted-foreground">
                    {selected.versionName || `版本 ${selected.versionId}`} ·{" "}
                    {knowledgeKindLabel(selected.kind)} · 置信度{" "}
                    {knowledgeConfidenceLabel(selected.confidence)} · revision {selected.revision}
                  </p>
                </header>
                <p className="whitespace-pre-wrap break-words rounded-xl border border-border bg-muted/30 p-3 text-sm">
                  {selected.body}
                </p>
                {selected.sourceRefs.length === 0 ? null : (
                  <p className="text-[11px] text-muted-foreground">
                    来源：
                    {selected.sourceRefs.map((ref) => `${ref.kind} ${ref.id}`).join(" · ")}
                  </p>
                )}

                <label className="block text-xs font-medium" htmlFor="knowledge-review-reason">
                  驳回原因（驳回必填）
                </label>
                <textarea
                  id="knowledge-review-reason"
                  data-testid="knowledge-review-reason"
                  value={reason}
                  onChange={(event) => {
                    setReason(event.target.value)
                    if (reasonError !== "" && event.target.value.trim() !== "") setReasonError("")
                  }}
                  rows={3}
                  className="w-full rounded-lg border border-border bg-background px-2.5 py-2 text-sm"
                  placeholder="说明为什么不采纳这条知识"
                />
                {reasonError === "" ? null : (
                  <p
                    data-testid="knowledge-review-reason-error"
                    className="text-xs text-destructive"
                  >
                    {reasonError}
                  </p>
                )}

                {success === "" ? null : (
                  <p
                    data-testid="knowledge-review-success"
                    className="rounded-lg border border-emerald-500/30 bg-emerald-500/10 px-2.5 py-1.5 text-sm text-emerald-700 dark:text-emerald-300"
                  >
                    {success}
                  </p>
                )}
                {submitError === "" ? null : (
                  <p
                    data-testid="knowledge-review-submit-error"
                    role="alert"
                    className="rounded-lg border border-destructive/30 bg-destructive/10 px-2.5 py-1.5 text-sm text-destructive"
                  >
                    {submitError}
                  </p>
                )}

                <div className="flex flex-wrap items-center gap-2">
                  <button
                    type="button"
                    data-testid="knowledge-review-approve"
                    onClick={() => void submit(true)}
                    disabled={busy}
                    className="inline-flex items-center gap-1 rounded-lg bg-emerald-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-emerald-700 disabled:opacity-50"
                  >
                    <Check className="h-4 w-4" aria-hidden="true" />
                    批准
                  </button>
                  <button
                    type="button"
                    data-testid="knowledge-review-reject"
                    onClick={() => void submit(false)}
                    disabled={busy}
                    className="inline-flex items-center gap-1 rounded-lg border border-destructive/40 px-3 py-1.5 text-sm font-medium text-destructive hover:bg-destructive/10 disabled:opacity-50"
                  >
                    <X className="h-4 w-4" aria-hidden="true" />
                    驳回
                  </button>
                </div>
              </div>
            )}
          </section>
        </div>
      </div>
    </div>
  )
}
