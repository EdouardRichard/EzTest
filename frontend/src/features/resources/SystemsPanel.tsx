"use client"

import { Pencil, Plus, RefreshCw, Server } from "lucide-react"
import { type FormEvent, useCallback, useEffect, useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { StatusBadge } from "@/components/ui/StatusBadge"
import { ApiError } from "@/lib/api-client"

import { createSystem, listSystems, updateSystem } from "./api"
import type { SystemDTO } from "./types"

const SYSTEM_PAGE_LIMIT = 100
const KEY_PATTERN = /^[a-z0-9][a-z0-9_-]{1,63}$/
const DEFAULT_TTL_SECONDS = 3600
const MIN_TTL_SECONDS = 60
const MAX_TTL_SECONDS = 604800

type SystemKind = SystemDTO["kind"]

const KIND_OPTIONS: { value: SystemKind; label: string }[] = [
  { value: "web", label: "Web 站点" },
  { value: "api", label: "API 服务" },
]

function describeError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
    if (error.status === 403) return "当前账户没有管理员权限。"
    if (error.details.reason === "duplicate_key") return "系统标识已存在，请更换 key。"
    return error.message
  }
  return error instanceof Error ? error.message : "未知错误"
}

type SystemFormState = {
  key: string
  name: string
  kind: SystemKind
  baseUrl: string
  defaultSessionTtlSeconds: string
  notes: string
}

const EMPTY_FORM: SystemFormState = {
  key: "",
  name: "",
  kind: "web",
  baseUrl: "",
  defaultSessionTtlSeconds: String(DEFAULT_TTL_SECONDS),
  notes: "",
}

function toFormState(system: SystemDTO): SystemFormState {
  return {
    key: system.key,
    name: system.name,
    kind: system.kind,
    baseUrl: system.baseUrl,
    defaultSessionTtlSeconds: String(system.defaultSessionTtlSeconds),
    notes: system.notes,
  }
}

function validateForm(form: SystemFormState, editing: boolean): string {
  if (!editing && !KEY_PATTERN.test(form.key.trim())) {
    return "系统标识需匹配 ^[a-z0-9][a-z0-9_-]{1,63}$。"
  }
  if (form.name.trim() === "") return "请输入系统名称。"
  if (!/^https?:\/\//i.test(form.baseUrl.trim())) {
    return "Base URL 必须以 http:// 或 https:// 开头。"
  }
  const ttl = Number(form.defaultSessionTtlSeconds)
  if (!Number.isInteger(ttl) || ttl < MIN_TTL_SECONDS || ttl > MAX_TTL_SECONDS) {
    return `默认会话 TTL 需为 ${MIN_TTL_SECONDS}–${MAX_TTL_SECONDS} 秒之间的整数。`
  }
  return ""
}

const inputClass =
  "mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
const labelClass = "text-xs font-medium text-muted-foreground"
const primaryButtonClass =
  "inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
const secondaryButtonClass =
  "inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

export type SystemsPanelProps = {
  canWrite: boolean
  selectedId?: number | null
  onSelect: (id: number) => void
}

/**
 * System rail of the resources page: loads the system list and, for admins,
 * renders the create/edit form. Every write reloads the list, and the caller
 * owns the selection so the environments panel can follow it.
 */
export function SystemsPanel({ canWrite, selectedId = null, onSelect }: SystemsPanelProps) {
  const [systems, setSystems] = useState<SystemDTO[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState("")
  const [notice, setNotice] = useState("")
  const [formOpen, setFormOpen] = useState(false)
  const [editingId, setEditingId] = useState<number | null>(null)
  const [form, setForm] = useState<SystemFormState>(EMPTY_FORM)
  const [saving, setSaving] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    setError("")
    try {
      const page = await listSystems({ limit: SYSTEM_PAGE_LIMIT })
      setSystems(page.items)
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  function updateForm<K extends keyof SystemFormState>(field: K, value: SystemFormState[K]) {
    setForm((previous) => ({ ...previous, [field]: value }))
  }

  function startCreate() {
    setEditingId(null)
    setForm(EMPTY_FORM)
    setFormOpen(true)
    setError("")
    setNotice("")
  }

  function startEdit(system: SystemDTO) {
    setEditingId(system.id)
    setForm(toFormState(system))
    setFormOpen(true)
    setError("")
    setNotice("")
  }

  function cancelForm() {
    setFormOpen(false)
    setEditingId(null)
    setError("")
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const editing = editingId !== null
    const validationError = validateForm(form, editing)
    if (validationError !== "") {
      setError(validationError)
      return
    }

    setSaving(true)
    setError("")
    setNotice("")
    try {
      const payload = {
        name: form.name.trim(),
        kind: form.kind,
        baseUrl: form.baseUrl.trim(),
        defaultSessionTtlSeconds: Number(form.defaultSessionTtlSeconds),
        notes: form.notes.trim(),
      }
      if (editingId !== null) {
        const updated = await updateSystem(editingId, payload)
        setNotice(`已更新系统 ${updated.name}。`)
      } else {
        const created = await createSystem({ key: form.key.trim(), ...payload })
        setNotice(`已创建系统 ${created.name}。`)
        onSelect(created.id)
      }
      setFormOpen(false)
      setEditingId(null)
      await load()
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setSaving(false)
    }
  }

  return (
    <section
      aria-labelledby="systems-panel-title"
      className="space-y-3 rounded-2xl border border-border bg-card p-3 shadow-sm"
    >
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h2 id="systems-panel-title" className="flex items-center gap-2 text-base font-semibold">
            <Server className="h-4 w-4 text-primary" aria-hidden="true" />
            系统
          </h2>
          <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
            选择左侧系统查看其环境；管理员可新建与编辑。
          </p>
        </div>
        <div className="flex items-center gap-1.5">
          <button
            type="button"
            onClick={() => void load()}
            disabled={loading}
            aria-label="刷新系统"
            className={secondaryButtonClass}
          >
            <RefreshCw className={`h-4 w-4 ${loading ? "animate-spin" : ""}`} aria-hidden="true" />
          </button>
          {canWrite ? (
            <button type="button" onClick={startCreate} className={primaryButtonClass}>
              <Plus className="h-4 w-4" aria-hidden="true" />
              新建系统
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

      {formOpen && canWrite ? (
        <form
          onSubmit={(event) => void handleSubmit(event)}
          className="space-y-2 rounded-xl border border-border bg-muted/30 p-3"
        >
          <h3 className="text-sm font-semibold">{editingId === null ? "新建系统" : "编辑系统"}</h3>
          <div>
            <label htmlFor="system-key" className={labelClass}>
              系统标识 key
            </label>
            <input
              id="system-key"
              value={form.key}
              onChange={(event) => updateForm("key", event.target.value)}
              disabled={editingId !== null}
              autoComplete="off"
              placeholder="例如 shop"
              className={inputClass}
            />
          </div>
          <div>
            <label htmlFor="system-name" className={labelClass}>
              名称
            </label>
            <input
              id="system-name"
              value={form.name}
              onChange={(event) => updateForm("name", event.target.value)}
              autoComplete="off"
              placeholder="例如 示例商城"
              className={inputClass}
            />
          </div>
          <div>
            <label htmlFor="system-kind" className={labelClass}>
              类型
            </label>
            <select
              id="system-kind"
              value={form.kind}
              onChange={(event) => updateForm("kind", event.target.value as SystemKind)}
              className={inputClass}
            >
              {KIND_OPTIONS.map((option) => (
                <option key={option.value} value={option.value}>
                  {option.label}
                </option>
              ))}
            </select>
          </div>
          <div>
            <label htmlFor="system-base-url" className={labelClass}>
              Base URL
            </label>
            <input
              id="system-base-url"
              value={form.baseUrl}
              onChange={(event) => updateForm("baseUrl", event.target.value)}
              autoComplete="off"
              placeholder="http://localhost:13335"
              className={inputClass}
            />
          </div>
          <div>
            <label htmlFor="system-ttl" className={labelClass}>
              默认会话 TTL（秒）
            </label>
            <input
              id="system-ttl"
              type="number"
              min={MIN_TTL_SECONDS}
              max={MAX_TTL_SECONDS}
              value={form.defaultSessionTtlSeconds}
              onChange={(event) => updateForm("defaultSessionTtlSeconds", event.target.value)}
              className={inputClass}
            />
          </div>
          <div>
            <label htmlFor="system-notes" className={labelClass}>
              备注
            </label>
            <textarea
              id="system-notes"
              value={form.notes}
              onChange={(event) => updateForm("notes", event.target.value)}
              rows={2}
              className={inputClass}
            />
          </div>
          <div className="flex justify-end gap-1.5 pt-1">
            <button
              type="button"
              onClick={cancelForm}
              disabled={saving}
              className={secondaryButtonClass}
            >
              取消
            </button>
            <button type="submit" disabled={saving} className={primaryButtonClass}>
              {saving ? "保存中…" : "保存系统"}
            </button>
          </div>
        </form>
      ) : null}

      {loading && systems.length === 0 ? (
        <p className="rounded-xl border border-dashed border-border px-4 py-6 text-center text-sm text-muted-foreground">
          正在加载系统…
        </p>
      ) : systems.length === 0 ? (
        <EmptyState
          icon={Server}
          title="暂无系统"
          description={canWrite ? "使用“新建系统”创建第一个系统。" : "管理员尚未创建任何系统。"}
        />
      ) : (
        <ul aria-label="系统列表" className="space-y-1">
          {systems.map((system) => {
            const active = system.id === selectedId
            return (
              <li
                key={system.id}
                className={
                  active
                    ? "rounded-xl border border-primary/40 bg-primary/10 px-2.5 py-2"
                    : "rounded-xl border border-transparent px-2.5 py-2 transition-colors hover:bg-muted"
                }
              >
                <div className="flex items-start justify-between gap-1.5">
                  <div className="min-w-0 flex-1">
                    <button
                      type="button"
                      onClick={() => onSelect(system.id)}
                      aria-current={active ? "true" : undefined}
                      className="w-full truncate text-left text-sm font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    >
                      {system.name}
                    </button>
                    <span className="block truncate font-mono text-[11px] text-muted-foreground">
                      {system.key}
                    </span>
                  </div>
                  {canWrite ? (
                    <button
                      type="button"
                      onClick={() => startEdit(system)}
                      aria-label={`编辑 ${system.name}`}
                      className="rounded-lg border border-border p-1.5 text-muted-foreground transition-colors hover:bg-card focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    >
                      <Pencil className="h-3.5 w-3.5" aria-hidden="true" />
                    </button>
                  ) : null}
                </div>
                <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
                  <StatusBadge variant={system.status === "active" ? "success" : "neutral"}>
                    {system.status === "active" ? "活跃" : "已停用"}
                  </StatusBadge>
                  <span className="font-mono text-[11px] text-muted-foreground">{system.kind}</span>
                  <span className="break-all text-[11px] text-muted-foreground">
                    {system.baseUrl}
                  </span>
                </div>
              </li>
            )
          })}
        </ul>
      )}
    </section>
  )
}
