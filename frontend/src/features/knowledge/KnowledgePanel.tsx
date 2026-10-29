"use client"

import { BookOpen, Loader2, Plus, RefreshCw } from "lucide-react"
import Link from "next/link"
import { useCallback, useEffect, useState } from "react"

import { ApiError } from "@/lib/api-client"

import { createKnowledge, listVersionKnowledge } from "./api"
import {
  KNOWLEDGE_CONFIDENCES,
  KNOWLEDGE_KINDS,
  type KnowledgeConfidence,
  type KnowledgeEntry,
  type KnowledgeKind,
  knowledgeConfidenceLabel,
  knowledgeKindLabel,
  knowledgeStatusLabel,
} from "./types"

/** Number of approved entries rendered in the version overview panel. */
const PANEL_LIMIT = 50

export type KnowledgePanelProps = {
  /** P3 version id as a decimal string. */
  versionId: string
}

function describeError(error: unknown): string {
  if (error instanceof ApiError) {
    const requestId = error.requestId === "" ? "" : `（request id: ${error.requestId}）`
    if (error.status === 0) return `无法连接 API 服务，请确认后端已启动。${requestId}`
    return `${error.message}${requestId}`
  }
  return error instanceof Error ? error.message : "未知错误"
}

/**
 * Version-overview knowledge entry (design spec §9.4): the version's approved
 * knowledge list plus the "记录知识" draft form (any logged-in user may create a
 * draft, which waits for an administrator review). The panel links to the
 * global `/knowledge` read. Data is never cached across reads.
 */
export function KnowledgePanel({ versionId }: KnowledgePanelProps) {
  const [entries, setEntries] = useState<KnowledgeEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState("")

  const [formOpen, setFormOpen] = useState(false)
  const [kind, setKind] = useState<KnowledgeKind>("pattern")
  const [title, setTitle] = useState("")
  const [body, setBody] = useState("")
  const [confidence, setConfidence] = useState<KnowledgeConfidence>("medium")
  const [formError, setFormError] = useState("")
  const [submitError, setSubmitError] = useState("")
  const [success, setSuccess] = useState("")
  const [submitting, setSubmitting] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    setError("")
    try {
      // The overview entry shows the approved knowledge only; drafts created
      // here are appended locally until an administrator approves them.
      const page = await listVersionKnowledge(versionId, { status: "approved", limit: PANEL_LIMIT })
      setEntries(page.items)
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setLoading(false)
    }
  }, [versionId])

  useEffect(() => {
    void load()
  }, [load])

  async function submit(): Promise<void> {
    if (submitting) return
    if (title.trim() === "" || body.trim() === "") {
      setFormError("标题和正文必填")
      return
    }
    setFormError("")
    setSubmitError("")
    setSuccess("")
    setSubmitting(true)
    try {
      const created = await createKnowledge(versionId, {
        kind,
        title: title.trim(),
        body: body.trim(),
        confidence,
      })
      setEntries((current) => [created, ...current])
      setTitle("")
      setBody("")
      setFormOpen(false)
      setSuccess("草稿已提交，等待管理员审核后进入全局知识库。")
    } catch (caught) {
      setSubmitError(describeError(caught))
    } finally {
      setSubmitting(false)
    }
  }

  const approved = entries.filter((entry) => entry.status === "approved")
  const pending = entries.filter((entry) => entry.status !== "approved")

  return (
    <section
      aria-label="版本知识"
      data-testid="knowledge-panel"
      className="rounded-2xl border border-border bg-card p-4"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <BookOpen className="h-4 w-4 text-sky-500" aria-hidden="true" />
          <h2 className="text-sm font-medium">版本知识</h2>
          <span className="rounded-full border border-border bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
            {approved.length} 条已批准
          </span>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <button
            type="button"
            data-testid="knowledge-create-toggle"
            onClick={() => setFormOpen((open) => !open)}
            className="inline-flex items-center gap-1 rounded-lg border border-border bg-card px-2.5 py-1.5 text-xs font-medium hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <Plus className="h-3.5 w-3.5" aria-hidden="true" />
            记录知识
          </button>
          <button
            type="button"
            onClick={() => void load()}
            aria-label="刷新版本知识"
            disabled={loading}
            className="grid h-7 w-7 place-items-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground disabled:opacity-50"
          >
            <RefreshCw
              className={loading ? "h-3.5 w-3.5 animate-spin" : "h-3.5 w-3.5"}
              aria-hidden="true"
            />
          </button>
          <Link
            href={`/knowledge?version=${versionId}`}
            className="text-xs font-medium text-primary"
          >
            查看知识库
          </Link>
        </div>
      </div>

      {success === "" ? null : (
        <p
          data-testid="knowledge-submit-success"
          className="mt-2 rounded-lg border border-emerald-500/30 bg-emerald-500/10 px-2.5 py-1.5 text-xs text-emerald-700 dark:text-emerald-300"
        >
          {success}
        </p>
      )}
      {submitError === "" ? null : (
        <p
          data-testid="knowledge-submit-error"
          role="alert"
          className="mt-2 rounded-lg border border-destructive/30 bg-destructive/10 px-2.5 py-1.5 text-xs text-destructive"
        >
          提交失败：{submitError}
        </p>
      )}

      {formOpen ? (
        <form
          data-testid="knowledge-create-form"
          onSubmit={(event) => {
            event.preventDefault()
            void submit()
          }}
          className="mt-3 space-y-2 rounded-xl border border-border p-3"
        >
          <div className="grid gap-2 sm:grid-cols-2">
            <label className="text-xs font-medium">
              类型
              <select
                data-testid="knowledge-kind"
                value={kind}
                onChange={(event) => setKind(event.target.value as KnowledgeKind)}
                className="mt-1 w-full rounded-lg border border-border bg-background px-2 py-1.5 text-sm"
              >
                {KNOWLEDGE_KINDS.map((item) => (
                  <option key={item.value} value={item.value}>
                    {item.label}
                  </option>
                ))}
              </select>
            </label>
            <label className="text-xs font-medium">
              置信度
              <select
                data-testid="knowledge-confidence"
                value={confidence}
                onChange={(event) => setConfidence(event.target.value as KnowledgeConfidence)}
                className="mt-1 w-full rounded-lg border border-border bg-background px-2 py-1.5 text-sm"
              >
                {KNOWLEDGE_CONFIDENCES.map((item) => (
                  <option key={item.value} value={item.value}>
                    {item.label}
                  </option>
                ))}
              </select>
            </label>
          </div>
          <label className="block text-xs font-medium">
            标题
            <input
              data-testid="knowledge-title"
              value={title}
              onChange={(event) => setTitle(event.target.value)}
              maxLength={300}
              className="mt-1 w-full rounded-lg border border-border bg-background px-2 py-1.5 text-sm"
              placeholder="一句话说明这条经验"
            />
          </label>
          <label className="block text-xs font-medium">
            正文
            <textarea
              data-testid="knowledge-body"
              value={body}
              onChange={(event) => setBody(event.target.value)}
              rows={4}
              className="mt-1 w-full rounded-lg border border-border bg-background px-2 py-1.5 text-sm"
              placeholder="记录模式、陷阱、决策或用例灵感"
            />
          </label>
          {formError === "" ? null : (
            <p data-testid="knowledge-form-error" className="text-xs text-destructive">
              {formError}
            </p>
          )}
          <div className="flex items-center gap-2">
            <button
              type="submit"
              data-testid="knowledge-submit"
              disabled={submitting}
              className="inline-flex items-center gap-1 rounded-lg bg-primary px-3 py-1.5 text-sm font-medium text-primary-foreground disabled:opacity-50"
            >
              {submitting ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
              ) : null}
              提交草稿
            </button>
            <button
              type="button"
              onClick={() => {
                setFormOpen(false)
                setFormError("")
              }}
              className="rounded-lg border border-border px-3 py-1.5 text-sm"
            >
              取消
            </button>
          </div>
        </form>
      ) : null}

      <div className="mt-3">
        {loading && entries.length === 0 ? (
          <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
            正在加载版本知识…
          </p>
        ) : error !== "" ? (
          <div
            role="alert"
            data-testid="knowledge-panel-error"
            className="flex flex-wrap items-center gap-2 rounded-lg border border-destructive/30 bg-destructive/10 px-2.5 py-1.5 text-sm text-destructive"
          >
            <span>加载版本知识失败：{error}</span>
            <button
              type="button"
              onClick={() => void load()}
              className="rounded-md border border-border bg-card px-2 py-0.5 text-xs font-medium text-foreground"
            >
              重试
            </button>
          </div>
        ) : entries.length === 0 ? (
          <p className="text-sm text-muted-foreground">本版本暂无已批准知识。</p>
        ) : (
          <ul className="space-y-1.5">
            {[...approved, ...pending].map((entry) => (
              <li
                key={entry.id}
                data-testid={`knowledge-entry-${entry.id}`}
                className="rounded-lg border border-border px-2.5 py-2"
              >
                <div className="flex flex-wrap items-center gap-1.5">
                  <span className="rounded-full border border-border bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
                    {knowledgeKindLabel(entry.kind)}
                  </span>
                  <span className="text-sm font-medium">{entry.title}</span>
                  <span className="ml-auto text-[10px] text-muted-foreground">
                    {entry.status === "approved"
                      ? `置信度 ${knowledgeConfidenceLabel(entry.confidence)}`
                      : entry.status === "draft"
                        ? "待审核"
                        : knowledgeStatusLabel(entry.status)}
                  </span>
                </div>
                <p className="mt-1 whitespace-pre-wrap break-words text-xs text-muted-foreground">
                  {entry.body}
                </p>
              </li>
            ))}
          </ul>
        )}
      </div>
    </section>
  )
}
