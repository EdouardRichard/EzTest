"use client"

import { type FormEvent, useCallback, useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Input } from "@/components/ui/Input"
import { Select } from "@/components/ui/Select"
import { LoadingCard } from "@/components/ui/Spinner"
import { formatDateTime } from "@/lib/format"

import { type KnowledgeSearchOptions, type KnowledgeSearchResult, searchKnowledge } from "./api"
import {
  KNOWLEDGE_STATUS_LABELS,
  KNOWLEDGE_STATUS_TONES,
  type KnowledgeStatus,
  knowledgeScopeLabel,
} from "./types"

export type KnowledgeSearchPanelProps = {
  /** Opens the full knowledge detail for a hit (keeps the page dialog). */
  onSelect?: (knowledgeId: string) => void
}

const STATUSES = Object.keys(KNOWLEDGE_STATUS_LABELS) as KnowledgeStatus[]

/** Human binding summary; absent dimensions are omitted, credentials never exist. */
function bindingSummary(item: KnowledgeSearchResult): string {
  const parts: string[] = []
  if (item.bindings.system_id !== null) parts.push(`系统 ${item.bindings.system_id}`)
  if (item.bindings.environment_id !== null) parts.push(`环境 ${item.bindings.environment_id}`)
  if (item.bindings.page_key !== null) parts.push(`页面 ${item.bindings.page_key}`)
  if (item.bindings.api_key !== null) parts.push(item.bindings.api_key)
  if (item.bindings.account_role !== null) parts.push(`账号 ${item.bindings.account_role}`)
  return parts.length === 0 ? "无结构化绑定" : parts.join(" · ")
}

/**
 * Structured cross-version knowledge search (`GET /api/knowledge/search`).
 * The endpoint is approved-only by default, so this panel is the user-facing
 * retrieval entry separate from the paginated P7 item list. Results stay
 * read-only; selecting one opens the host page's existing detail dialog.
 */
export function KnowledgeSearchPanel({ onSelect }: KnowledgeSearchPanelProps) {
  const [query, setQuery] = useState("")
  const [status, setStatus] = useState<KnowledgeStatus>("approved")
  const [items, setItems] = useState<KnowledgeSearchResult[] | null>(null)
  const [nextCursor, setNextCursor] = useState("")
  const [hasMore, setHasMore] = useState(false)
  const [loading, setLoading] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [lastSearch, setLastSearch] = useState<KnowledgeSearchOptions | null>(null)

  const load = useCallback(async (options: KnowledgeSearchOptions, cursor: string) => {
    if (cursor === "") setLoading(true)
    else setLoadingMore(true)
    setError(null)
    try {
      const page =
        cursor === "" ? await searchKnowledge(options) : await searchKnowledge(options, cursor)
      setItems((current) => (cursor === "" ? page.items : [...(current ?? []), ...page.items]))
      setNextCursor(page.nextCursor)
      setHasMore(page.hasMore)
    } catch (cause) {
      if (cursor === "") setItems(null)
      setError(cause)
    } finally {
      setLoading(false)
      setLoadingMore(false)
    }
  }, [])

  function submit(event: FormEvent<HTMLFormElement>): void {
    event.preventDefault()
    const keyword = query.trim()
    const options: KnowledgeSearchOptions = {
      q: keyword === "" ? undefined : keyword,
      status,
      limit: 20,
    }
    setLastSearch(options)
    void load(options, "")
  }

  function retry(): void {
    if (lastSearch !== null) void load(lastSearch, "")
  }

  function loadMore(): void {
    if (lastSearch === null || loadingMore || !hasMore || nextCursor === "") return
    void load(lastSearch, nextCursor)
  }

  return (
    <section
      data-testid="knowledge-search-panel"
      aria-label="知识检索"
      className="space-y-2 rounded-lg border border-border bg-card px-3 py-2.5"
    >
      <header className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold text-foreground">知识检索</h2>
          <p className="text-[11px] text-muted-foreground">
            跨版本结构化检索（默认只返回已通过的知识）
          </p>
        </div>
        <Badge tone="info">search</Badge>
      </header>

      <form aria-label="知识检索条件" className="flex flex-wrap items-end gap-2" onSubmit={submit}>
        <Field id="knowledge-search-query" label="检索关键词" className="w-64">
          <Input
            id="knowledge-search-query"
            type="search"
            value={query}
            maxLength={512}
            placeholder="退款 / 结算 / 接口规范"
            onChange={(event) => setQuery(event.target.value)}
          />
        </Field>
        <Field id="knowledge-search-status" label="检索状态" className="w-36">
          <Select
            id="knowledge-search-status"
            value={status}
            onChange={(event) => setStatus(event.target.value as KnowledgeStatus)}
          >
            {STATUSES.map((value) => (
              <option key={value} value={value}>
                {KNOWLEDGE_STATUS_LABELS[value]}
              </option>
            ))}
          </Select>
        </Field>
        <Button type="submit" size="sm" variant="secondary" disabled={loading}>
          检索
        </Button>
      </form>

      {error !== null && items === null ? (
        <ErrorState error={error} title="知识检索失败" onRetry={retry} />
      ) : loading ? (
        <LoadingCard label="正在检索知识…" />
      ) : items === null ? (
        <p className="text-[11px] text-muted-foreground">
          输入关键词后点击「检索」；空关键词返回当前状态下的全部知识。
        </p>
      ) : items.length === 0 ? (
        <EmptyState
          title="没有匹配的知识"
          description="调整关键词或检索状态后重试；未审核的知识不会出现在默认检索中。"
        />
      ) : (
        <div className="space-y-2">
          <ul data-testid="knowledge-search-results" className="space-y-1.5">
            {items.map((item) => (
              <li
                key={item.id}
                data-testid={`knowledge-search-result-${item.id}`}
                className="rounded-md border border-border px-2 py-1.5"
              >
                <div className="flex flex-wrap items-center gap-1.5 text-[11px]">
                  <Badge tone={KNOWLEDGE_STATUS_TONES[item.status]}>
                    {KNOWLEDGE_STATUS_LABELS[item.status]}
                  </Badge>
                  {onSelect === undefined ? (
                    <span className="min-w-0 flex-1 truncate font-medium text-foreground">
                      {item.title}
                    </span>
                  ) : (
                    <button
                      type="button"
                      onClick={() => onSelect(item.id)}
                      className="min-w-0 flex-1 truncate text-left font-medium text-foreground underline-offset-2 hover:underline"
                    >
                      {item.title}
                    </button>
                  )}
                  <span
                    data-testid={`knowledge-search-scope-${item.id}`}
                    className="text-muted-foreground"
                  >
                    {knowledgeScopeLabel(item)}
                  </span>
                  <span className="text-muted-foreground">{formatDateTime(item.updated_at)}</span>
                </div>
                <div className="mt-0.5 flex flex-wrap items-center gap-1.5 text-[10px] text-muted-foreground">
                  <span data-testid={`knowledge-search-binding-${item.id}`}>
                    绑定：{bindingSummary(item)}
                  </span>
                  {item.tags.length > 0 ? (
                    <span className="flex flex-wrap gap-1">
                      {item.tags.map((tag) => (
                        <Badge key={tag} tone="neutral">
                          {tag}
                        </Badge>
                      ))}
                    </span>
                  ) : null}
                </div>
              </li>
            ))}
          </ul>
          {hasMore ? (
            <Button variant="secondary" size="sm" disabled={loadingMore} onClick={loadMore}>
              {loadingMore ? "加载中…" : "加载更多检索结果"}
            </Button>
          ) : null}
          {error !== null ? <InlineError error={error} /> : null}
        </div>
      )}
    </section>
  )
}
