"use client"

import { type ChangeEvent, type FormEvent, useEffect, useRef, useState } from "react"

import { ApiError } from "@/lib/api-client"

import { importAppModels } from "./api"
import type { AppModelNodeDTO } from "./types"

/** Import payload cap: 5 MiB of JSON and 5000 nodes (spec §6.6). */
const MAX_IMPORT_BYTES = 5 * 1024 * 1024
const MAX_IMPORT_NODES = 5000

type ImportNode = Partial<AppModelNodeDTO> & { parentKey?: string }

type ParsedNodes = { nodes: ImportNode[]; error?: never } | { error: string; nodes?: never }

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

function byteLength(text: string): number {
  return new TextEncoder().encode(text).byteLength
}

/** Reads and validates the `{"nodes":[...]}` import document. */
function parseNodes(raw: string): ParsedNodes {
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    return { error: "JSON 解析失败，请检查语法。" }
  }
  if (!isRecord(parsed) || !Array.isArray(parsed.nodes)) {
    return { error: 'JSON 必须形如 {"nodes":[...]}。' }
  }
  if (parsed.nodes.length === 0) {
    return { error: "nodes 不能为空。" }
  }
  if (parsed.nodes.length > MAX_IMPORT_NODES) {
    return { error: `一次最多导入 ${MAX_IMPORT_NODES} 个节点。` }
  }
  const nodes: ImportNode[] = []
  for (const [index, item] of parsed.nodes.entries()) {
    if (!isRecord(item)) {
      return { error: `第 ${index + 1} 个节点必须是 JSON 对象。` }
    }
    if (typeof item.key !== "string" || item.key.trim() === "") {
      return { error: `第 ${index + 1} 个节点缺少 key。` }
    }
    if (typeof item.kind !== "string" || item.kind.trim() === "") {
      return { error: `第 ${index + 1} 个节点缺少 kind。` }
    }
    nodes.push(item as ImportNode)
  }
  return { nodes }
}

function describeImportError(error: unknown): string {
  if (!(error instanceof ApiError)) {
    return error instanceof Error ? error.message : "未知错误"
  }
  if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
  if (error.status === 403) return "当前账户没有管理员权限。"
  const reason = typeof error.details.reason === "string" ? error.details.reason : ""
  const field = typeof error.details.field === "string" ? error.details.field : ""
  if (error.status === 422) {
    const detail = reason === "" ? "" : `（${reason}${field === "" ? "" : ` / ${field}`}）`
    return `导入失败${detail}：${error.message}`
  }
  if (reason === "duplicate_key") return "节点标识已存在，请更换 key。"
  if (reason !== "") return `${error.message}（${reason}）`
  return error.message
}

export type AppModelImportDialogProps = {
  systemId: number
  onClose: () => void
  onImported: () => void | Promise<void>
}

/**
 * JSON import dialog for app model nodes. Accepts a pasted document or a file
 * (both capped at 5 MiB), validates the envelope locally, then shows the
 * backend's created/updated counts or its 422 `details.reason`.
 */
export function AppModelImportDialog({ systemId, onClose, onImported }: AppModelImportDialogProps) {
  const [json, setJson] = useState("")
  const [error, setError] = useState("")
  const [result, setResult] = useState("")
  const [importing, setImporting] = useState(false)
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  useEffect(() => {
    textareaRef.current?.focus()
  }, [])

  async function handleFileChange(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0]
    if (!file) return
    setError("")
    setResult("")
    if (file.size > MAX_IMPORT_BYTES) {
      setError("JSON 文件不能超过 5 MiB。")
      return
    }
    try {
      setJson(await file.text())
    } catch {
      setError("读取文件失败，请重试或直接粘贴 JSON。")
    }
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setError("")
    setResult("")

    if (json.trim() === "") {
      setError("请粘贴 JSON 或选择 JSON 文件。")
      return
    }
    if (byteLength(json) > MAX_IMPORT_BYTES) {
      setError("JSON 内容不能超过 5 MiB。")
      return
    }
    const parsed = parseNodes(json)
    if (parsed.error !== undefined) {
      setError(parsed.error)
      return
    }

    setImporting(true)
    try {
      const counts = await importAppModels(systemId, parsed.nodes)
      setResult(`已导入：新建 ${counts.created} 个，更新 ${counts.updated} 个。`)
      await onImported()
    } catch (caught) {
      setError(describeImportError(caught))
    } finally {
      setImporting(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/40 p-4 sm:items-center">
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="app-model-import-title"
        onKeyDown={(event) => {
          if (event.key === "Escape") onClose()
        }}
        className="w-full max-w-2xl space-y-3 rounded-2xl border border-border bg-card p-4 shadow-xl"
      >
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div>
            <h2 id="app-model-import-title" className="text-base font-semibold">
              JSON 导入
            </h2>
            <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
              为系统 #{systemId} 批量 upsert 应用模型节点；结构为 {"{"}&quot;nodes&quot;:[...]
              {"}"}，单次最多 {MAX_IMPORT_NODES} 个节点、5 MiB。
            </p>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="rounded-lg border border-border px-2.5 py-1.5 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            关闭
          </button>
        </div>

        {error ? (
          <p
            role="alert"
            className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
          >
            {error}
          </p>
        ) : null}
        {result ? (
          <p
            role="status"
            className="rounded-xl border border-emerald-500/25 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-700 dark:text-emerald-300"
          >
            {result}
          </p>
        ) : null}

        <form onSubmit={(event) => void handleSubmit(event)} className="space-y-3">
          <div>
            <label
              htmlFor="app-model-import-json"
              className="text-xs font-medium text-muted-foreground"
            >
              JSON
            </label>
            <textarea
              id="app-model-import-json"
              ref={textareaRef}
              value={json}
              onChange={(event) => setJson(event.target.value)}
              rows={10}
              spellCheck={false}
              placeholder={'{"nodes":[{"key":"route:/orders","kind":"route","name":"订单列表"}]}'}
              className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 font-mono text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring"
            />
          </div>
          <div>
            <label
              htmlFor="app-model-import-file"
              className="text-xs font-medium text-muted-foreground"
            >
              JSON 文件
            </label>
            <input
              id="app-model-import-file"
              type="file"
              accept=".json,application/json"
              onChange={(event) => void handleFileChange(event)}
              className="mt-1 block w-full text-xs text-muted-foreground file:mr-2 file:rounded-lg file:border file:border-border file:bg-card file:px-2.5 file:py-1.5 file:text-xs file:font-medium file:text-foreground"
            />
          </div>
          <div className="flex justify-end gap-1.5">
            <button
              type="button"
              onClick={onClose}
              disabled={importing}
              className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              取消
            </button>
            <button
              type="submit"
              disabled={importing}
              className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {importing ? "导入中…" : "导入"}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
