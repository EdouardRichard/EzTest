"use client"

import { Boxes, Pencil, Plus, RefreshCw, Upload } from "lucide-react"
import { type FormEvent, useCallback, useEffect, useRef, useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { ApiError } from "@/lib/api-client"

import { AppModelImportDialog } from "./AppModelImportDialog"
import { createAppModel, listAppModels, updateAppModel } from "./api"
import type { AppModelNodeDTO } from "./types"

const APP_MODEL_PAGE_LIMIT = 100
const MAX_KEY_LENGTH = 512

const KIND_OPTIONS = [
  { value: "page", label: "page（页面）" },
  { value: "route", label: "route（路由）" },
  { value: "api", label: "api（接口）" },
  { value: "component", label: "component（组件）" },
]

/** Human labels for the closed `details.reason` set of spec §7.1/§6.6. */
const ERROR_REASON_LABELS: Record<string, string> = {
  duplicate_key: "节点标识已存在",
  invalid_app_model_key: "key 与 kind 不匹配",
  has_dependencies: "存在依赖，无法删除",
}

function describeError(error: unknown): string {
  if (!(error instanceof ApiError)) {
    return error instanceof Error ? error.message : "未知错误"
  }
  if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
  if (error.status === 403) return "当前账户没有管理员权限。"
  const reason = typeof error.details.reason === "string" ? error.details.reason : ""
  const label = reason === "" ? "" : ERROR_REASON_LABELS[reason]
  if (label) return `${label}（${reason}）`
  if (error.status === 422 && reason !== "") return `保存失败（${reason}）：${error.message}`
  if (reason !== "") return `${error.message}（${reason}）`
  return error.message
}

/** Mirrors the backend's key/kind convention from spec §6.6. */
function keyMatchesKind(kind: string, key: string): boolean {
  if (kind === "api") return /^api:[A-Z]+ \S.*$/.test(key)
  const prefix = `${kind}:`
  return key.length > prefix.length && key.startsWith(prefix)
}

type NodeFormState = {
  kind: string
  key: string
  name: string
  path: string
  method: string
}

const EMPTY_FORM: NodeFormState = {
  kind: "page",
  key: "",
  name: "",
  path: "",
  method: "",
}

function toFormState(node: AppModelNodeDTO): NodeFormState {
  return {
    kind: node.kind,
    key: node.key,
    name: node.name,
    path: node.path,
    method: node.method,
  }
}

function validateForm(form: NodeFormState, editing: boolean): string {
  if (!editing) {
    const key = form.key.trim()
    if (key === "") return "请输入节点 key。"
    if (key.length > MAX_KEY_LENGTH) return `节点 key 不能超过 ${MAX_KEY_LENGTH} 个字符。`
    if (!keyMatchesKind(form.kind, key)) {
      return "节点 key 必须匹配 kind 前缀：page:/route:/component:/api:<METHOD> <path>。"
    }
  }
  if (form.name.trim() === "") return "请输入节点名称。"
  return ""
}

const inputClass =
  "mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-60"
const labelClass = "text-xs font-medium text-muted-foreground"
const primaryButtonClass =
  "inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
const secondaryButtonClass =
  "inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

export type AppModelsPanelProps = {
  systemId: number
  canWrite: boolean
}

/**
 * App model tab for one system: a kind filter, the node table and admin-only
 * inline create/edit plus the JSON import dialog. Nodes are read at the
 * backend's maximum page size because the shared API client hides cursor
 * metadata and P2 has no paging UI.
 */
export function AppModelsPanel({ systemId, canWrite }: AppModelsPanelProps) {
  const [nodes, setNodes] = useState<AppModelNodeDTO[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState("")
  const [notice, setNotice] = useState("")
  const [kindFilter, setKindFilter] = useState("")
  const [formOpen, setFormOpen] = useState(false)
  const [editingId, setEditingId] = useState<number | null>(null)
  const [form, setForm] = useState<NodeFormState>(EMPTY_FORM)
  const [saving, setSaving] = useState(false)
  const [importOpen, setImportOpen] = useState(false)
  const importButtonRef = useRef<HTMLButtonElement>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError("")
    try {
      const params: { kind?: string; limit: number } = { limit: APP_MODEL_PAGE_LIMIT }
      if (kindFilter !== "") params.kind = kindFilter
      const page = await listAppModels(systemId, params)
      setNodes(page.items)
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setLoading(false)
    }
  }, [systemId, kindFilter])

  useEffect(() => {
    setNodes([])
    void load()
  }, [load])

  function updateForm<K extends keyof NodeFormState>(field: K, value: NodeFormState[K]) {
    setForm((previous) => ({ ...previous, [field]: value }))
  }

  function startCreate() {
    setEditingId(null)
    setForm(EMPTY_FORM)
    setFormOpen(true)
    setError("")
    setNotice("")
  }

  function startEdit(node: AppModelNodeDTO) {
    setEditingId(node.id)
    setForm(toFormState(node))
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

    const mutable = {
      name: form.name.trim(),
      path: form.path.trim(),
      method: form.method.trim(),
    }

    setSaving(true)
    setError("")
    setNotice("")
    try {
      if (editingId !== null) {
        const updated = await updateAppModel(editingId, mutable)
        setNotice(`已更新节点 ${updated.name}。`)
      } else {
        const created = await createAppModel(systemId, {
          key: form.key.trim(),
          kind: form.kind,
          ...mutable,
        })
        setNotice(`已创建节点 ${created.name}。`)
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

  function closeImport() {
    setImportOpen(false)
    importButtonRef.current?.focus()
  }

  return (
    <section
      aria-labelledby="app-models-panel-title"
      className="space-y-3 rounded-2xl border border-border bg-card p-3 shadow-sm"
    >
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h2
            id="app-models-panel-title"
            className="flex items-center gap-2 text-base font-semibold"
          >
            <Boxes className="h-4 w-4 text-primary" aria-hidden="true" />
            应用模型
          </h2>
          <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
            系统 #{systemId} 的应用模型节点；按 kind 过滤，管理员可维护或 JSON 批量导入。
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          <label htmlFor="app-model-kind-filter" className="sr-only">
            按类型筛选
          </label>
          <select
            id="app-model-kind-filter"
            value={kindFilter}
            onChange={(event) => setKindFilter(event.target.value)}
            className="rounded-lg border border-border bg-input px-2.5 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <option value="">全部类型</option>
            {KIND_OPTIONS.map((option) => (
              <option key={option.value} value={option.value}>
                {option.value}
              </option>
            ))}
          </select>
          <button
            type="button"
            onClick={() => void load()}
            disabled={loading}
            aria-label="刷新应用模型"
            className={secondaryButtonClass}
          >
            <RefreshCw className={`h-4 w-4 ${loading ? "animate-spin" : ""}`} aria-hidden="true" />
          </button>
          {canWrite ? (
            <>
              <button
                type="button"
                ref={importButtonRef}
                onClick={() => {
                  setError("")
                  setNotice("")
                  setImportOpen(true)
                }}
                className={secondaryButtonClass}
              >
                <Upload className="h-4 w-4" aria-hidden="true" />
                JSON 导入
              </button>
              <button type="button" onClick={startCreate} className={primaryButtonClass}>
                <Plus className="h-4 w-4" aria-hidden="true" />
                新建节点
              </button>
            </>
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
          <h3 className="text-sm font-semibold">{editingId === null ? "新建节点" : "编辑节点"}</h3>
          <div className="grid gap-2 sm:grid-cols-2">
            <div>
              <label htmlFor="app-model-kind" className={labelClass}>
                节点类型
              </label>
              <select
                id="app-model-kind"
                value={form.kind}
                onChange={(event) => updateForm("kind", event.target.value)}
                disabled={editingId !== null}
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
              <label htmlFor="app-model-key" className={labelClass}>
                节点 key
              </label>
              <input
                id="app-model-key"
                value={form.key}
                onChange={(event) => updateForm("key", event.target.value)}
                disabled={editingId !== null}
                autoComplete="off"
                placeholder="例如 route:/orders"
                className={inputClass}
              />
            </div>
            <div>
              <label htmlFor="app-model-name" className={labelClass}>
                名称
              </label>
              <input
                id="app-model-name"
                value={form.name}
                onChange={(event) => updateForm("name", event.target.value)}
                autoComplete="off"
                placeholder="例如 订单列表"
                className={inputClass}
              />
            </div>
            <div>
              <label htmlFor="app-model-path" className={labelClass}>
                路径
              </label>
              <input
                id="app-model-path"
                value={form.path}
                onChange={(event) => updateForm("path", event.target.value)}
                autoComplete="off"
                placeholder="例如 /orders"
                className={inputClass}
              />
            </div>
            <div>
              <label htmlFor="app-model-method" className={labelClass}>
                方法
              </label>
              <input
                id="app-model-method"
                value={form.method}
                onChange={(event) => updateForm("method", event.target.value)}
                autoComplete="off"
                placeholder="例如 GET"
                className={inputClass}
              />
            </div>
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
              {saving ? "保存中…" : "保存节点"}
            </button>
          </div>
        </form>
      ) : null}

      {loading && nodes.length === 0 ? (
        <p className="rounded-xl border border-dashed border-border px-4 py-6 text-center text-sm text-muted-foreground">
          正在加载应用模型…
        </p>
      ) : nodes.length === 0 ? (
        <EmptyState
          icon={Boxes}
          title="暂无应用模型节点"
          description={
            canWrite ? "使用“新建节点”或“JSON 导入”为该系统添加节点。" : "该系统尚未导入应用模型。"
          }
        />
      ) : (
        <div className="overflow-x-auto rounded-xl border border-border">
          <table className="min-w-full divide-y divide-border text-sm">
            <caption className="sr-only">应用模型列表</caption>
            <thead className="bg-muted/50 text-left text-[11px] uppercase tracking-wide text-muted-foreground">
              <tr>
                <th scope="col" className="px-2.5 py-2 font-medium">
                  kind
                </th>
                <th scope="col" className="px-2.5 py-2 font-medium">
                  key
                </th>
                <th scope="col" className="px-2.5 py-2 font-medium">
                  名称
                </th>
                <th scope="col" className="px-2.5 py-2 font-medium">
                  路径
                </th>
                <th scope="col" className="px-2.5 py-2 font-medium">
                  方法
                </th>
                <th scope="col" className="px-2.5 py-2 font-medium">
                  来源
                </th>
                {canWrite ? (
                  <th scope="col" className="px-2.5 py-2 text-right font-medium">
                    操作
                  </th>
                ) : null}
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {nodes.map((node) => (
                <tr key={node.id}>
                  <td className="px-2.5 py-2 font-mono text-[11px] text-muted-foreground">
                    {node.kind}
                  </td>
                  <td className="break-all px-2.5 py-2 font-mono text-[11px]">{node.key}</td>
                  <td className="px-2.5 py-2 font-medium">{node.name}</td>
                  <td className="break-all px-2.5 py-2 font-mono text-xs text-muted-foreground">
                    {node.path === "" ? "—" : node.path}
                  </td>
                  <td className="px-2.5 py-2 font-mono text-xs text-muted-foreground">
                    {node.method === "" ? "—" : node.method}
                  </td>
                  <td className="px-2.5 py-2 text-xs text-muted-foreground">{node.source}</td>
                  {canWrite ? (
                    <td className="px-2.5 py-2 text-right">
                      <button
                        type="button"
                        onClick={() => startEdit(node)}
                        aria-label={`编辑 ${node.name}`}
                        className="inline-flex items-center gap-1 rounded-lg border border-border px-2 py-1 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                      >
                        <Pencil className="h-3 w-3" aria-hidden="true" />
                        编辑
                      </button>
                    </td>
                  ) : null}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {importOpen && canWrite ? (
        <AppModelImportDialog systemId={systemId} onClose={closeImport} onImported={load} />
      ) : null}
    </section>
  )
}
