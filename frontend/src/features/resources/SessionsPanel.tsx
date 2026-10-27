"use client"

import { Layers, Plus, RefreshCw } from "lucide-react"
import { useCallback, useEffect, useRef, useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"
import { ApiError } from "@/lib/api-client"
import { getSession, listAccounts, listEnvironments, listSessions, listSystems } from "./api"
import { ImportSessionDialog } from "./ImportSessionDialog"
import { SessionDetail } from "./SessionDetail"
import type { AccountDTO, EnvironmentDTO, SessionDTO, SystemDTO } from "./types"

/** Lists run at the backend's maximum page size; the API client hides cursors. */
const PAGE_LIMIT = 100

const STATE_OPTIONS = [
  "pending",
  "valid",
  "needs_manual",
  "expired",
  "invalid",
  "revoked",
  "failed",
] as const

const STATE_VARIANTS: Record<string, StatusBadgeVariant> = {
  valid: "success",
  needs_manual: "warning",
  pending: "info",
  expired: "danger",
  invalid: "danger",
  revoked: "neutral",
  failed: "danger",
}

const ERROR_REASON_LABELS: Record<string, string> = {
  session_state_conflict: "当前状态不允许该操作",
  takeover_not_waiting: "没有等待中的接管",
}

function describeError(error: unknown): string {
  if (!(error instanceof ApiError)) {
    return error instanceof Error ? error.message : "未知错误"
  }
  if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
  if (error.status === 403) return "当前账户没有管理员权限。"
  const reason = typeof error.details.reason === "string" ? error.details.reason : ""
  const field = typeof error.details.field === "string" ? error.details.field : ""
  const location = reason === "" ? "" : field === "" ? `（${reason}）` : `（${reason} / ${field}）`
  const label = reason === "" ? "" : ERROR_REASON_LABELS[reason]
  if (label) return `${label}${location}：${error.message}`
  if (reason !== "") return `${error.message}${location}`
  return error.message
}

function formatDateTime(value: string | null): string {
  if (value === null || value === "") return "—"
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString("zh-CN", { hour12: false })
}

type SessionFilters = {
  accountId: string
  systemId: string
  state: string
}

const EMPTY_FILTERS: SessionFilters = { accountId: "", systemId: "", state: "" }

const primaryButtonClass =
  "inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
const secondaryButtonClass =
  "inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
const filterClass =
  "rounded-lg border border-border bg-input px-2.5 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"

export type SessionsPanelProps = {
  canWrite: boolean
  initialSessionId?: number
  /** Keeps the page's `?session=` deep link in sync when a row is selected. */
  onSelectSession?: (id: number) => void
}

/**
 * Session tab: filtered list plus the selected session's detail. Sessions are
 * read at the backend's maximum page size; selecting a row loads the detail
 * (latest snapshot, lease, takeover) while the list data stays visible, and
 * every admin action re-reads both list and detail.
 */
export function SessionsPanel({ canWrite, initialSessionId, onSelectSession }: SessionsPanelProps) {
  const [sessions, setSessions] = useState<SessionDTO[]>([])
  const [accounts, setAccounts] = useState<AccountDTO[]>([])
  const [systems, setSystems] = useState<SystemDTO[]>([])
  const [environments, setEnvironments] = useState<EnvironmentDTO[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState("")
  const [notice, setNotice] = useState("")
  const [filters, setFilters] = useState<SessionFilters>(EMPTY_FILTERS)
  const [selectedId, setSelectedId] = useState<number | null>(initialSessionId ?? null)
  const [detail, setDetail] = useState<SessionDTO | null>(null)
  const [detailError, setDetailError] = useState("")
  const [importOpen, setImportOpen] = useState(false)
  const detailRequestRef = useRef(0)

  const loadOptions = useCallback(async () => {
    try {
      const [accountPage, systemPage] = await Promise.all([
        listAccounts({ limit: PAGE_LIMIT }),
        listSystems({ limit: PAGE_LIMIT }),
      ])
      setAccounts(accountPage.items)
      setSystems(systemPage.items)
    } catch (caught) {
      setError(describeError(caught))
    }
  }, [])

  const load = useCallback(async () => {
    setLoading(true)
    setError("")
    try {
      const page = await listSessions({
        limit: PAGE_LIMIT,
        ...(filters.accountId === "" ? {} : { accountId: Number(filters.accountId) }),
        ...(filters.systemId === "" ? {} : { systemId: Number(filters.systemId) }),
        ...(filters.state === "" ? {} : { state: filters.state }),
      })
      setSessions(page.items)
      // With no deep link the first row becomes the selection, so the detail
      // (and the admin actions) are reachable without an extra click.
      setSelectedId((previous) => previous ?? page.items[0]?.id ?? null)

      const systemIds = [...new Set(page.items.map((item) => item.systemId))]
      if (systemIds.length === 0) {
        setEnvironments([])
        return
      }
      try {
        const lists = await Promise.all(systemIds.map((id) => listEnvironments(id)))
        setEnvironments(lists.flat())
      } catch {
        // Environment names are decorative; keep the session list usable.
      }
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setLoading(false)
    }
  }, [filters])

  useEffect(() => {
    void loadOptions()
  }, [loadOptions])

  useEffect(() => {
    void load()
  }, [load])

  const loadDetail = useCallback(async (id: number) => {
    const requestId = detailRequestRef.current + 1
    detailRequestRef.current = requestId
    setDetailError("")
    try {
      const loaded = await getSession(id)
      if (detailRequestRef.current !== requestId) return
      setDetail(loaded)
    } catch (caught) {
      if (detailRequestRef.current !== requestId) return
      setDetailError(describeError(caught))
    }
  }, [])

  useEffect(() => {
    if (selectedId === null) {
      setDetail(null)
      setDetailError("")
      return
    }
    void loadDetail(selectedId)
  }, [selectedId, loadDetail])

  const refresh = useCallback(async () => {
    await load()
    if (selectedId !== null) await loadDetail(selectedId)
  }, [load, loadDetail, selectedId])

  const selectedSession =
    selectedId === null
      ? null
      : detail !== null && detail.id === selectedId
        ? detail
        : (sessions.find((item) => item.id === selectedId) ?? null)

  function accountName(id: number): string {
    return accounts.find((account) => account.id === id)?.label ?? `账号 #${id}`
  }

  function systemName(id: number): string {
    return systems.find((system) => system.id === id)?.name ?? `系统 #${id}`
  }

  function environmentName(id: number): string {
    return environments.find((environment) => environment.id === id)?.name ?? `环境 #${id}`
  }

  function selectSession(id: number) {
    if (id === selectedId) return
    setSelectedId(id)
    setNotice("")
    onSelectSession?.(id)
  }

  async function handleImported(session: SessionDTO) {
    // The dialog stays open so its state/snapshot/degraded result stays visible
    // (mirrors the app-model import dialog); only the list behind it refreshes.
    const degraded = session.latestSnapshot?.degraded === true
    setNotice(`已导入会话 #${session.id}（${session.state}${degraded ? "，能力未验证" : ""}）。`)
    setSelectedId(session.id)
    onSelectSession?.(session.id)
    await load()
    await loadDetail(session.id)
  }

  function updateFilter(field: keyof SessionFilters, value: string) {
    setFilters((previous) => ({ ...previous, [field]: value }))
  }

  return (
    <section
      aria-labelledby="sessions-panel-title"
      className="space-y-3 rounded-2xl border border-border bg-card p-3 shadow-sm"
    >
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h2 id="sessions-panel-title" className="flex items-center gap-2 text-base font-semibold">
            <Layers className="h-4 w-4 text-primary" aria-hidden="true" />
            会话
          </h2>
          <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
            导入并校验目标系统的 Session；管理员可校验、确认、失效、撤销与发起人工接管。
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          <button
            type="button"
            onClick={() => void refresh()}
            disabled={loading}
            aria-label="刷新会话"
            className={secondaryButtonClass}
          >
            <RefreshCw className={`h-4 w-4 ${loading ? "animate-spin" : ""}`} aria-hidden="true" />
          </button>
          {canWrite ? (
            <button
              type="button"
              onClick={() => {
                setError("")
                setNotice("")
                setImportOpen(true)
              }}
              className={primaryButtonClass}
            >
              <Plus className="h-4 w-4" aria-hidden="true" />
              导入会话
            </button>
          ) : null}
        </div>
      </div>

      {error ? (
        <p
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          {error}
        </p>
      ) : null}
      {notice ? (
        <p
          role="status"
          className="rounded-xl border border-emerald-500/25 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-700 dark:text-emerald-300"
        >
          {notice}
        </p>
      ) : null}

      <div className="flex flex-wrap items-center gap-1.5">
        <select
          aria-label="按账号筛选"
          value={filters.accountId}
          onChange={(event) => updateFilter("accountId", event.target.value)}
          className={filterClass}
        >
          <option value="">全部账号</option>
          {accounts.map((account) => (
            <option key={account.id} value={account.id}>
              {account.label}
            </option>
          ))}
        </select>
        <select
          aria-label="按系统筛选"
          value={filters.systemId}
          onChange={(event) => updateFilter("systemId", event.target.value)}
          className={filterClass}
        >
          <option value="">全部系统</option>
          {systems.map((system) => (
            <option key={system.id} value={system.id}>
              {system.name}
            </option>
          ))}
        </select>
        <select
          aria-label="按状态筛选"
          value={filters.state}
          onChange={(event) => updateFilter("state", event.target.value)}
          className={filterClass}
        >
          <option value="">全部状态</option>
          {STATE_OPTIONS.map((state) => (
            <option key={state} value={state}>
              {state}
            </option>
          ))}
        </select>
      </div>

      {loading && sessions.length === 0 ? (
        <p className="rounded-xl border border-dashed border-border px-4 py-6 text-center text-sm text-muted-foreground">
          正在加载会话…
        </p>
      ) : sessions.length === 0 ? (
        <EmptyState
          icon={Layers}
          title="暂无会话"
          description={
            canWrite
              ? "使用「导入会话」导入 storage_state 或 token 请求头。"
              : "管理员尚未导入任何会话。"
          }
        />
      ) : (
        <div className="overflow-x-auto rounded-xl border border-border">
          <table className="min-w-full divide-y divide-border text-sm">
            <caption className="sr-only">会话列表</caption>
            <thead className="bg-muted/50 text-left text-[11px] uppercase tracking-wide text-muted-foreground">
              <tr>
                <th scope="col" className="px-2.5 py-2 font-medium">
                  账号
                </th>
                <th scope="col" className="px-2.5 py-2 font-medium">
                  系统
                </th>
                <th scope="col" className="px-2.5 py-2 font-medium">
                  环境
                </th>
                <th scope="col" className="px-2.5 py-2 font-medium">
                  状态
                </th>
                <th scope="col" className="px-2.5 py-2 font-medium">
                  有效期
                </th>
                <th scope="col" className="px-2.5 py-2 font-medium">
                  最后校验
                </th>
                <th scope="col" className="px-2.5 py-2 text-right font-medium">
                  操作
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {sessions.map((session) => {
                const active = session.id === selectedId
                return (
                  <tr key={session.id} className={active ? "bg-primary/5" : undefined}>
                    <td className="px-2.5 py-2">
                      {accountName(session.accountId)}
                      <span className="block font-mono text-[11px] text-muted-foreground">
                        #{session.id}
                      </span>
                    </td>
                    <td className="px-2.5 py-2 text-muted-foreground">
                      {systemName(session.systemId)}
                    </td>
                    <td className="px-2.5 py-2 text-muted-foreground">
                      {environmentName(session.environmentId)}
                    </td>
                    <td className="px-2.5 py-2">
                      <div className="flex flex-wrap items-center gap-1">
                        <StatusBadge variant={STATE_VARIANTS[session.state] ?? "neutral"}>
                          {session.state}
                        </StatusBadge>
                        {session.expiring ? (
                          <StatusBadge variant="warning">即将过期</StatusBadge>
                        ) : null}
                      </div>
                    </td>
                    <td className="px-2.5 py-2 text-muted-foreground">
                      {formatDateTime(session.expiresAt)}
                    </td>
                    <td className="px-2.5 py-2 text-muted-foreground">
                      {formatDateTime(session.lastValidatedAt)}
                    </td>
                    <td className="px-2.5 py-2 text-right">
                      <button
                        type="button"
                        onClick={() => selectSession(session.id)}
                        aria-current={active ? "true" : undefined}
                        aria-label={`查看会话 #${session.id}`}
                        className="inline-flex items-center gap-1 rounded-lg border border-border px-2 py-1 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                      >
                        查看
                      </button>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}

      {selectedSession === null ? (
        sessions.length === 0 ? null : (
          <p className="rounded-xl border border-dashed border-border px-4 py-4 text-center text-sm text-muted-foreground">
            选择一个会话查看详情与操作。
          </p>
        )
      ) : (
        <div className="space-y-2">
          {detailError ? (
            <p
              role="alert"
              className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
            >
              加载会话详情失败：{detailError}
            </p>
          ) : null}
          <SessionDetail
            session={selectedSession}
            accountName={accountName(selectedSession.accountId)}
            systemName={systemName(selectedSession.systemId)}
            environmentName={environmentName(selectedSession.environmentId)}
            canWrite={canWrite}
            onChanged={refresh}
          />
        </div>
      )}

      {importOpen && canWrite ? (
        <ImportSessionDialog onClose={() => setImportOpen(false)} onImported={handleImported} />
      ) : null}
    </section>
  )
}
