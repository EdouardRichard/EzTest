"use client"

import { type FormEvent, useCallback, useEffect, useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Input } from "@/components/ui/Input"
import { Select } from "@/components/ui/Select"
import { LoadingCard } from "@/components/ui/Spinner"
import { Table, type TableColumn } from "@/components/ui/Table"
import { useIsAdmin } from "@/features/auth/useIsAdmin"
import { getStoredUser } from "@/lib/auth-storage"
import { formatDateTime } from "@/lib/format"

import { listKnowledge } from "./api"
import { KnowledgeDetail } from "./KnowledgeDetail"
import { KnowledgeEditor } from "./KnowledgeEditor"
import {
  KNOWLEDGE_SCOPE_LABELS,
  KNOWLEDGE_STATUS_LABELS,
  KNOWLEDGE_STATUS_TONES,
  type KnowledgeItemView,
  type KnowledgeListOptions,
  type KnowledgeScope,
  type KnowledgeStatus,
  knowledgeScopeLabel,
} from "./types"

const PAGE_SIZE = 20

const STATUSES = Object.keys(KNOWLEDGE_STATUS_LABELS) as KnowledgeStatus[]
const SCOPES = Object.keys(KNOWLEDGE_SCOPE_LABELS) as KnowledgeScope[]

type KnowledgeFilters = {
  scope: KnowledgeScope | "all"
  status: KnowledgeStatus | "all"
  systemKey: string
  tag: string
  q: string
}

const EMPTY_FILTERS: KnowledgeFilters = {
  scope: "all",
  status: "all",
  systemKey: "",
  tag: "",
  q: "",
}

function listOptions(filters: KnowledgeFilters): KnowledgeListOptions {
  const options: KnowledgeListOptions = { limit: PAGE_SIZE }
  if (filters.scope !== "all") options.scope = filters.scope
  if (filters.status !== "all") options.status = filters.status
  if (filters.systemKey.length > 0) options.systemKey = filters.systemKey
  if (filters.tag.length > 0) options.tag = filters.tag
  if (filters.q.length > 0) options.q = filters.q
  return options
}

function hasActiveFilters(filters: KnowledgeFilters): boolean {
  return (
    filters.scope !== "all" ||
    filters.status !== "all" ||
    filters.systemKey.length > 0 ||
    filters.tag.length > 0 ||
    filters.q.length > 0
  )
}

type EditorState = { mode: "create" } | { mode: "edit"; item: KnowledgeItemView } | null

/**
 * P7 cross-version knowledge workspace (`/knowledge`, spec §10).
 *
 * Every logged-in user can browse/search knowledge, create drafts and edit
 * their own draft/rejected items. The review transitions are admin-only and
 * live in the detail dialog, whose refresh also refreshes the list.
 */
export function KnowledgePage() {
  const isAdmin = useIsAdmin()
  const currentUserId = getStoredUser()?.id ?? ""

  const [filters, setFilters] = useState<KnowledgeFilters>(EMPTY_FILTERS)
  const [queryDraft, setQueryDraft] = useState("")
  const [tagDraft, setTagDraft] = useState("")
  const [systemKeyDraft, setSystemKeyDraft] = useState("")

  const [items, setItems] = useState<KnowledgeItemView[]>([])
  const [nextCursor, setNextCursor] = useState("")
  const [hasMore, setHasMore] = useState(false)
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [reloadToken, setReloadToken] = useState(0)

  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [detailToken, setDetailToken] = useState(0)
  const [editor, setEditor] = useState<EditorState>(null)

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken forces a retry
  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    void listKnowledge(listOptions(filters))
      .then((page) => {
        if (cancelled) return
        setItems(page.items)
        setNextCursor(page.nextCursor)
        setHasMore(page.hasMore)
      })
      .catch((cause: unknown) => {
        if (!cancelled) setError(cause)
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [filters, reloadToken])

  const refreshList = useCallback(() => {
    setReloadToken((token) => token + 1)
  }, [])

  function updateFilter<Key extends keyof KnowledgeFilters>(
    key: Key,
    value: KnowledgeFilters[Key],
  ): void {
    setFilters((current) => ({ ...current, [key]: value }))
  }

  function changeScope(next: KnowledgeScope | "all"): void {
    setFilters((current) => ({
      ...current,
      scope: next,
      // system_key only makes sense with the system scope (spec §6.1).
      systemKey: next === "system" ? current.systemKey : "",
    }))
    if (next !== "system") setSystemKeyDraft("")
  }

  function submitFilters(event: FormEvent<HTMLFormElement>): void {
    event.preventDefault()
    setFilters((current) => ({
      ...current,
      systemKey: current.scope === "system" ? systemKeyDraft.trim() : "",
      tag: tagDraft.trim(),
      q: queryDraft.trim(),
    }))
  }

  async function loadMore(): Promise<void> {
    if (loadingMore || !hasMore || nextCursor.length === 0) return
    setLoadingMore(true)
    try {
      const page = await listKnowledge(listOptions(filters), nextCursor)
      setItems((current) => [...current, ...page.items])
      setNextCursor(page.nextCursor)
      setHasMore(page.hasMore)
    } catch (cause) {
      setError(cause)
    } finally {
      setLoadingMore(false)
    }
  }

  const columns: TableColumn<KnowledgeItemView>[] = [
    {
      key: "title",
      header: "标题",
      render: (item) => (
        <div className="min-w-0">
          <button
            type="button"
            data-testid={`knowledge-item-${item.id}`}
            onClick={() => setSelectedId(item.id)}
            className="max-w-md text-left text-xs font-medium text-foreground underline-offset-2 hover:underline"
          >
            {item.title}
          </button>
          <div className="mt-0.5 flex flex-wrap items-center gap-1.5 text-[11px] text-muted-foreground">
            {item.source_stale ? (
              <span data-testid={`knowledge-stale-${item.id}`}>
                <Badge tone="warning">来源版本已失效</Badge>
              </span>
            ) : null}
            <span data-testid={`knowledge-tags-${item.id}`}>
              {item.tags.length > 0 ? item.tags.join("、") : "无标签"}
            </span>
          </div>
        </div>
      ),
    },
    {
      key: "scope",
      header: "范围",
      render: (item) => (
        <span data-testid={`knowledge-scope-${item.id}`} className="text-xs text-foreground">
          {knowledgeScopeLabel(item)}
        </span>
      ),
    },
    {
      key: "status",
      header: "状态",
      render: (item) => (
        <span data-testid={`knowledge-status-${item.id}`}>
          <Badge tone={KNOWLEDGE_STATUS_TONES[item.status]}>
            {KNOWLEDGE_STATUS_LABELS[item.status]}
          </Badge>
        </span>
      ),
    },
    {
      key: "revision",
      header: "修订",
      render: (item) => (
        <span data-testid={`knowledge-revision-${item.id}`} className="text-xs text-foreground">
          r{item.revision}
        </span>
      ),
    },
    {
      key: "updated",
      header: "更新时间",
      render: (item) => (
        <span className="text-xs text-muted-foreground">{formatDateTime(item.updated_at)}</span>
      ),
    },
  ]

  const filtered = hasActiveFilters(filters)

  return (
    <div data-testid="knowledge-page" className="space-y-4">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <p className="text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">
            跨版本知识
          </p>
          <div className="mt-1 flex items-center gap-2">
            <h1 className="text-xl font-semibold text-foreground">知识</h1>
            {isAdmin ? <Badge tone="info">管理员</Badge> : null}
          </div>
          <p className="mt-1 max-w-2xl text-xs leading-relaxed text-muted-foreground">
            全局/系统范围的可复用结论。创建后为草稿，管理员审核通过后才会注入版本上下文；来源版本失效的条目不会注入。
          </p>
        </div>
        <Button
          size="sm"
          data-testid="knowledge-create"
          onClick={() => setEditor({ mode: "create" })}
        >
          新建知识
        </Button>
      </header>

      <form
        aria-label="知识筛选"
        data-testid="knowledge-filters"
        className="flex flex-wrap items-end gap-3"
        onSubmit={submitFilters}
      >
        <Field id="knowledge-filter-scope" label="范围" className="w-40">
          <Select
            id="knowledge-filter-scope"
            value={filters.scope}
            onChange={(event) => changeScope(event.target.value as KnowledgeScope | "all")}
          >
            <option value="all">全部范围</option>
            {SCOPES.map((scope) => (
              <option key={scope} value={scope}>
                {KNOWLEDGE_SCOPE_LABELS[scope]}
              </option>
            ))}
          </Select>
        </Field>
        <Field id="knowledge-filter-status" label="状态" className="w-36">
          <Select
            id="knowledge-filter-status"
            value={filters.status}
            onChange={(event) =>
              updateFilter("status", event.target.value as KnowledgeFilters["status"])
            }
          >
            <option value="all">全部状态</option>
            {STATUSES.map((status) => (
              <option key={status} value={status}>
                {KNOWLEDGE_STATUS_LABELS[status]}
              </option>
            ))}
          </Select>
        </Field>
        {filters.scope === "system" ? (
          <Field id="knowledge-filter-system-key" label="系统标识" className="w-44">
            <Input
              id="knowledge-filter-system-key"
              value={systemKeyDraft}
              maxLength={64}
              placeholder="order-center"
              onChange={(event) => setSystemKeyDraft(event.target.value)}
            />
          </Field>
        ) : null}
        <Field id="knowledge-filter-tag" label="标签" className="w-40">
          <Input
            id="knowledge-filter-tag"
            value={tagDraft}
            maxLength={40}
            placeholder="结算"
            onChange={(event) => setTagDraft(event.target.value)}
          />
        </Field>
        <Field id="knowledge-filter-q" label="搜索" className="w-64">
          <Input
            id="knowledge-filter-q"
            type="search"
            value={queryDraft}
            maxLength={200}
            placeholder="标题/内容关键词"
            onChange={(event) => setQueryDraft(event.target.value)}
          />
        </Field>
        <Button type="submit" size="sm" variant="secondary" data-testid="knowledge-search">
          搜索
        </Button>
        {filtered ? (
          <Button
            type="button"
            size="sm"
            variant="ghost"
            data-testid="knowledge-reset-filters"
            onClick={() => {
              setFilters(EMPTY_FILTERS)
              setQueryDraft("")
              setTagDraft("")
              setSystemKeyDraft("")
            }}
          >
            重置
          </Button>
        ) : null}
      </form>

      {loading ? (
        <LoadingCard label="正在加载知识…" />
      ) : error !== null ? (
        <div data-testid="knowledge-error">
          <ErrorState
            error={error}
            title="知识列表加载失败"
            onRetry={() => setReloadToken((token) => token + 1)}
          />
        </div>
      ) : items.length === 0 ? (
        <div data-testid="knowledge-empty">
          <EmptyState
            title={filtered ? "没有符合筛选条件的知识" : "暂无知识"}
            description={
              filtered
                ? "调整范围、状态、标签或搜索词后重试。"
                : "从版本沉淀或直接创建草稿；管理员审核通过后注入版本上下文。"
            }
          />
        </div>
      ) : (
        <div data-testid="knowledge-list">
          <Table ariaLabel="知识列表" columns={columns} rows={items} rowKey={(item) => item.id} />
        </div>
      )}

      {hasMore && !loading && error === null ? (
        <div className="flex justify-center">
          <Button
            variant="secondary"
            size="sm"
            data-testid="knowledge-load-more"
            disabled={loadingMore}
            onClick={() => {
              void loadMore()
            }}
          >
            {loadingMore ? "加载中…" : "加载更多"}
          </Button>
        </div>
      ) : null}

      {selectedId !== null ? (
        <KnowledgeDetail
          knowledgeId={selectedId}
          isAdmin={isAdmin}
          currentUserId={currentUserId}
          refreshToken={detailToken}
          onClose={() => setSelectedId(null)}
          onEdit={(item) => setEditor({ mode: "edit", item })}
          onChanged={refreshList}
        />
      ) : null}

      {editor !== null ? (
        <KnowledgeEditor
          mode={editor.mode}
          item={editor.mode === "edit" ? editor.item : null}
          onClose={() => setEditor(null)}
          onSaved={(item) => {
            const wasCreate = editor.mode === "create"
            setEditor(null)
            refreshList()
            if (wasCreate) {
              setSelectedId(item.id)
            } else {
              setDetailToken((token) => token + 1)
            }
          }}
        />
      ) : null}
    </div>
  )
}
