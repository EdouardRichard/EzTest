"use client"

import Link from "next/link"
import { useEffect, useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { Dialog } from "@/components/ui/Dialog"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { formatDateTime } from "@/lib/format"

import { getKnowledge } from "./api"
import { KnowledgeReviewActions } from "./KnowledgeReviewActions"
import {
  canEditKnowledge,
  KNOWLEDGE_SCOPE_LABELS,
  KNOWLEDGE_STATUS_LABELS,
  KNOWLEDGE_STATUS_TONES,
  type KnowledgeDetailView,
  type KnowledgeItemView,
  type KnowledgeRevisionView,
  knowledgeScopeLabel,
} from "./types"

export type KnowledgeDetailProps = {
  knowledgeId: string
  isAdmin: boolean
  currentUserId: string
  /** Bump to refetch the detail after an external edit. */
  refreshToken: number
  onClose: () => void
  onEdit: (item: KnowledgeItemView) => void
  /** Called after a review transition so the list can refresh. */
  onChanged: () => void
}

const REVISION_EXCERPT = 120

function excerpt(content: string): string {
  const text = content.trim()
  if (text.length <= REVISION_EXCERPT) return text
  return `${text.slice(0, REVISION_EXCERPT)}…`
}

/**
 * Read-only knowledge detail dialog (spec §10): every frozen field, the
 * read-time `source_stale` badge, review metadata, the append-only revision
 * list and the admin review toolbar. Loading/error states are local so the
 * list behind the dialog stays usable.
 */
export function KnowledgeDetail({
  knowledgeId,
  isAdmin,
  currentUserId,
  refreshToken,
  onClose,
  onEdit,
  onChanged,
}: KnowledgeDetailProps) {
  const [detail, setDetail] = useState<KnowledgeDetailView | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<unknown>(null)
  const [reloadToken, setReloadToken] = useState(0)

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken forces a retry
  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    void getKnowledge(knowledgeId)
      .then((next) => {
        if (!cancelled) setDetail(next)
      })
      .catch((cause: unknown) => {
        if (!cancelled) {
          setDetail(null)
          setError(cause)
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [knowledgeId, refreshToken, reloadToken])

  function handleReviewUpdated(updated: KnowledgeItemView): void {
    setDetail((current) => (current === null ? current : { ...current, ...updated }))
    onChanged()
  }

  const editable = detail !== null && canEditKnowledge(detail, currentUserId, isAdmin)

  return (
    <Dialog
      open
      onClose={onClose}
      title="知识详情"
      description={`知识 #${knowledgeId}`}
      className="max-w-2xl"
      footer={
        <Button variant="secondary" size="sm" onClick={onClose}>
          关闭
        </Button>
      }
    >
      <div data-testid="knowledge-detail" className="max-h-[70vh] space-y-4 overflow-y-auto pr-1">
        {loading && detail === null ? (
          <LoadingCard label="正在加载知识详情…" />
        ) : error !== null ? (
          <div data-testid="knowledge-detail-error">
            <ErrorState
              error={error}
              title="知识详情加载失败"
              onRetry={() => setReloadToken((token) => token + 1)}
            />
          </div>
        ) : detail !== null ? (
          <>
            <header className="space-y-2">
              <div className="flex flex-wrap items-start justify-between gap-2">
                <h3 className="text-sm font-semibold text-foreground">{detail.title}</h3>
                <div className="flex flex-wrap items-center gap-1.5">
                  <span data-testid="knowledge-status">
                    <Badge tone={KNOWLEDGE_STATUS_TONES[detail.status]}>
                      {KNOWLEDGE_STATUS_LABELS[detail.status]}
                    </Badge>
                  </span>
                  {detail.source_stale ? (
                    <span data-testid="knowledge-source-stale">
                      <Badge tone="warning">来源版本已失效</Badge>
                    </span>
                  ) : null}
                </div>
              </div>
              <div className="flex flex-wrap items-center gap-2 text-[11px] text-muted-foreground">
                <span data-testid="knowledge-scope">{knowledgeScopeLabel(detail)}</span>
                <span aria-hidden="true">·</span>
                <span data-testid="knowledge-revision">r{detail.revision}</span>
                {editable ? (
                  <Button
                    variant="secondary"
                    size="sm"
                    data-testid="knowledge-edit"
                    className="ml-auto"
                    onClick={() => onEdit(detail)}
                  >
                    编辑
                  </Button>
                ) : null}
              </div>
            </header>

            <section aria-label="知识属性" className="rounded-lg border border-border bg-muted/20">
              <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 px-3 py-2.5 text-[11px]">
                <dt className="text-muted-foreground">范围</dt>
                <dd className="text-foreground">
                  {KNOWLEDGE_SCOPE_LABELS[detail.scope]}
                  {detail.system_key !== null ? ` · ${detail.system_key}` : ""}
                </dd>
                <dt className="text-muted-foreground">修订</dt>
                <dd className="text-foreground">r{detail.revision}</dd>
                <dt className="text-muted-foreground">来源版本</dt>
                <dd data-testid="knowledge-source-version" className="text-foreground">
                  {detail.source_version_id !== null ? (
                    <Link
                      href={`/versions/${detail.source_version_id}`}
                      className="text-primary underline-offset-2 hover:underline"
                    >
                      #{detail.source_version_id}
                    </Link>
                  ) : (
                    "—"
                  )}
                </dd>
                <dt className="text-muted-foreground">晋升来源</dt>
                <dd className="text-foreground">
                  {detail.promoted_from_version_id !== null
                    ? `#${detail.promoted_from_version_id}`
                    : "—"}
                </dd>
                <dt className="text-muted-foreground">创建者</dt>
                <dd className="text-foreground">{detail.created_by ?? "—"}</dd>
                <dt className="text-muted-foreground">创建时间</dt>
                <dd className="text-foreground">{formatDateTime(detail.created_at)}</dd>
                <dt className="text-muted-foreground">更新时间</dt>
                <dd className="text-foreground">{formatDateTime(detail.updated_at)}</dd>
              </dl>
            </section>

            <section aria-label="知识内容" className="space-y-2">
              <h3 className="text-xs font-semibold text-foreground">内容</h3>
              <p
                data-testid="knowledge-content"
                className="whitespace-pre-wrap rounded-lg border border-border bg-card px-3 py-2.5 text-xs leading-relaxed text-foreground"
              >
                {detail.content}
              </p>
              <div data-testid="knowledge-tags" className="flex flex-wrap gap-1.5">
                {detail.tags.length === 0 ? (
                  <span className="text-[11px] text-muted-foreground">无标签</span>
                ) : (
                  detail.tags.map((tag) => (
                    <Badge key={tag} tone="neutral">
                      {tag}
                    </Badge>
                  ))
                )}
              </div>
            </section>

            <section
              data-testid="knowledge-review-info"
              aria-label="审核信息"
              className="space-y-1 rounded-lg border border-border bg-muted/20 px-3 py-2.5 text-[11px]"
            >
              <h3 className="text-xs font-semibold text-foreground">审核信息</h3>
              <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1">
                <dt className="text-muted-foreground">审核人</dt>
                <dd className="text-foreground">{detail.reviewed_by ?? "—"}</dd>
                <dt className="text-muted-foreground">审核时间</dt>
                <dd className="text-foreground">
                  {detail.reviewed_at !== null ? formatDateTime(detail.reviewed_at) : "—"}
                </dd>
                <dt className="text-muted-foreground">撤回时间</dt>
                <dd className="text-foreground">
                  {detail.revoked_at !== null ? formatDateTime(detail.revoked_at) : "—"}
                </dd>
                <dt className="text-muted-foreground">审核/撤回理由</dt>
                <dd className="text-foreground">{detail.review_reason ?? "—"}</dd>
              </dl>
            </section>

            <KnowledgeReviewActions
              item={detail}
              isAdmin={isAdmin}
              onUpdated={handleReviewUpdated}
            />

            <section
              data-testid="knowledge-revisions"
              aria-label="内容修订历史"
              className="space-y-2"
            >
              <h3 className="text-xs font-semibold text-foreground">
                内容修订历史（{detail.revisions.length}）
              </h3>
              {detail.revisions.length === 0 ? (
                <p className="text-[11px] text-muted-foreground">暂无修订历史。</p>
              ) : (
                <ul className="space-y-1.5">
                  {detail.revisions.map((revision) => (
                    <RevisionRow key={revision.id} revision={revision} />
                  ))}
                </ul>
              )}
            </section>
          </>
        ) : null}
      </div>
    </Dialog>
  )
}

function RevisionRow({ revision }: { revision: KnowledgeRevisionView }) {
  return (
    <li
      data-testid={`knowledge-revision-${revision.revision}`}
      className="rounded-lg border border-border bg-card px-3 py-2"
    >
      <div className="flex flex-wrap items-center justify-between gap-2 text-[11px]">
        <span className="font-medium text-foreground">
          r{revision.revision} · {revision.title}
        </span>
        <span className="text-muted-foreground">
          {revision.changed_by ?? "—"} · {formatDateTime(revision.created_at)}
        </span>
      </div>
      <p className="mt-1 text-[11px] text-muted-foreground">{excerpt(revision.content)}</p>
    </li>
  )
}
