"use client"

import type { FormEvent } from "react"
import { useEffect, useState } from "react"

import { Button } from "@/components/ui/Button"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Input } from "@/components/ui/Input"
import { LoadingCard } from "@/components/ui/Spinner"
import { Table, type TableColumn } from "@/components/ui/Table"
import type { ApiPage } from "@/lib/api-client"
import { formatDateTime } from "@/lib/format"

import { type AuditLogEntry, listAuditLogs } from "./api"

const PAGE_SIZE = 20

type Filters = { action: string; actor: string }

function hasMetadata(entry: AuditLogEntry): boolean {
  return Object.keys(entry.metadata).length > 0
}

/** Admin audit log with exact-match filters and cursor pagination. */
export function AuditPanel() {
  const [draft, setDraft] = useState<Filters>({ action: "", actor: "" })
  const [filters, setFilters] = useState<Filters>({ action: "", actor: "" })
  const [cursor, setCursor] = useState("")
  const [history, setHistory] = useState<string[]>([])
  const [page, setPage] = useState<ApiPage<AuditLogEntry> | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<unknown>(null)
  const [filterError, setFilterError] = useState<string | null>(null)
  const [reloadKey, setReloadKey] = useState(0)

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadKey retriggers reload
  useEffect(() => {
    let active = true
    setLoading(true)
    setError(null)
    listAuditLogs({
      action: filters.action,
      actor: filters.actor,
      cursor,
      limit: PAGE_SIZE,
    })
      .then((result) => {
        if (active) setPage(result)
      })
      .catch((caught: unknown) => {
        if (active) setError(caught)
      })
      .finally(() => {
        if (active) setLoading(false)
      })
    return () => {
      active = false
    }
  }, [filters, cursor, reloadKey])

  function applyFilters(event: FormEvent<HTMLFormElement>): void {
    event.preventDefault()
    const actor = draft.actor.trim()
    if (actor.length > 0 && !/^\d+$/.test(actor)) {
      setFilterError("操作者 ID 必须是数字")
      return
    }
    setFilterError(null)
    setHistory([])
    setCursor("")
    setPage(null)
    setFilters({ action: draft.action.trim(), actor })
    setReloadKey((n) => n + 1)
  }

  function resetFilters(): void {
    setDraft({ action: "", actor: "" })
    setFilterError(null)
    setHistory([])
    setCursor("")
    setPage(null)
    setFilters({ action: "", actor: "" })
    setReloadKey((n) => n + 1)
  }

  function nextPage(): void {
    if (!page || page.nextCursor.length === 0 || loading) return
    setHistory((current) => [...current, cursor])
    setCursor(page.nextCursor)
  }

  function previousPage(): void {
    if (history.length === 0 || loading) return
    const prior = history[history.length - 1] ?? ""
    setHistory(history.slice(0, -1))
    setCursor(prior)
  }

  const columns: TableColumn<AuditLogEntry>[] = [
    {
      key: "created_at",
      header: "时间",
      render: (entry) => (
        <span className="whitespace-nowrap text-xs text-muted-foreground">
          {formatDateTime(entry.created_at)}
        </span>
      ),
    },
    {
      key: "action",
      header: "操作",
      render: (entry) => (
        <code className="font-mono text-[11px] text-foreground">{entry.action}</code>
      ),
    },
    {
      key: "actor",
      header: "操作者",
      render: (entry) => (
        <span className="font-mono text-[11px] text-foreground">
          {entry.actor_id ? `#${entry.actor_id}` : "系统"}
        </span>
      ),
    },
    {
      key: "target",
      header: "对象",
      render: (entry) => (
        <div className="max-w-56">
          <span className="text-xs text-muted-foreground">{entry.target_kind}</span>
          <span className="ml-1 break-all font-mono text-[11px] text-foreground">
            {entry.target_id}
          </span>
        </div>
      ),
    },
    {
      key: "request_id",
      header: "请求 ID",
      render: (entry) => (
        <code className="font-mono text-[10px] text-muted-foreground">{entry.request_id}</code>
      ),
    },
    {
      key: "ip",
      header: "IP",
      render: (entry) => <span className="font-mono text-[11px]">{entry.ip}</span>,
    },
    {
      key: "metadata",
      header: "详情",
      render: (entry) =>
        hasMetadata(entry) ? (
          <details className="text-[11px]">
            <summary className="cursor-pointer text-muted-foreground">详情</summary>
            <pre className="mt-1 max-w-72 overflow-x-auto whitespace-pre-wrap break-all rounded bg-muted p-2 font-mono text-[10px]">
              {JSON.stringify(entry.metadata, null, 2)}
            </pre>
          </details>
        ) : (
          <span className="text-xs text-muted-foreground">—</span>
        ),
    },
  ]

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="text-sm font-semibold text-foreground">审计日志</h2>
          <p className="mt-1 text-xs text-muted-foreground">
            只读查询登录、用户、设置与 LLM 配置等管理动作；按记录 ID 倒序展示。
          </p>
        </div>
        <Button variant="secondary" size="sm" onClick={() => setReloadKey((n) => n + 1)}>
          刷新
        </Button>
      </div>

      <form onSubmit={applyFilters} className="surface-card flex flex-wrap items-end gap-3 p-4">
        <Field id="audit-action" label="操作" className="min-w-48 flex-1">
          <Input
            id="audit-action"
            placeholder="如 auth.login"
            value={draft.action}
            onChange={(event) =>
              setDraft((current) => ({ ...current, action: event.target.value }))
            }
          />
        </Field>
        <Field id="audit-actor" label="操作者 ID" className="w-36">
          <Input
            id="audit-actor"
            inputMode="numeric"
            placeholder="如 1"
            value={draft.actor}
            onChange={(event) => setDraft((current) => ({ ...current, actor: event.target.value }))}
          />
        </Field>
        <div className="flex items-center gap-2 pb-0.5">
          <Button type="submit" size="sm">
            查询
          </Button>
          <Button type="button" variant="ghost" size="sm" onClick={resetFilters}>
            重置
          </Button>
        </div>
        {filterError ? (
          <p role="alert" className="basis-full text-xs text-destructive">
            {filterError}
          </p>
        ) : null}
      </form>

      {loading && page === null ? <LoadingCard label="正在加载审计日志…" /> : null}

      {error && page === null ? (
        <ErrorState
          error={error}
          title="审计日志加载失败"
          onRetry={() => setReloadKey((n) => n + 1)}
        />
      ) : null}

      {page !== null ? (
        <>
          {error ? <InlineError error={error} /> : null}
          <Table
            ariaLabel="审计日志"
            columns={columns}
            rows={page.items}
            rowKey={(entry) => entry.id}
            empty="暂无审计日志"
          />
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className="text-[11px] text-muted-foreground">
              本页 {page.items.length} 条 {loading ? "· 加载中…" : ""}
            </p>
            <div className="flex items-center gap-2">
              <Button
                variant="secondary"
                size="sm"
                onClick={previousPage}
                disabled={history.length === 0 || loading}
              >
                上一页
              </Button>
              <Button
                variant="secondary"
                size="sm"
                onClick={nextPage}
                disabled={!page.hasMore || page.nextCursor.length === 0 || loading}
              >
                下一页
              </Button>
            </div>
          </div>
        </>
      ) : null}
    </div>
  )
}
