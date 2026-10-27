"use client"

import { Loader2, X } from "lucide-react"
import { type FormEvent, useEffect, useId, useRef, useState } from "react"

import { listCodeRepos } from "@/features/code-repos/api"
import type { CodeRepoDTO } from "@/features/code-repos/types"
import { listEnvironments, listSystems } from "@/features/resources/api"
import type { EnvironmentDTO, SystemDTO } from "@/features/resources/types"
import { ApiError } from "@/lib/api-client"

import { createVersion } from "./api"
import type { CreateVersionBody, Version } from "./types"

const NAME_MAX = 200
const LABEL_MAX = 50
const DESCRIPTION_MAX = 20000
const MAX_PAGE_SIZE = 100

/** Human labels for the closed `details.reason` set a create call may return. */
const REASON_LABELS: Record<string, string> = {
  system_not_found: "所选系统不存在或已删除。",
  system_disabled: "所选系统已停用。",
  environment_not_found: "所选环境不存在或已删除。",
  environment_system_mismatch: "所选环境不属于所选系统。",
  code_repository_not_found: "所选代码仓不存在或已删除。",
  code_repository_disabled: "所选代码仓已停用。",
}

function describeError(error: unknown): string {
  if (!(error instanceof ApiError)) {
    return error instanceof Error ? error.message : "未知错误"
  }
  if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
  if (error.status === 403) return "当前账户没有创建版本的权限。"

  const reason = typeof error.details.reason === "string" ? error.details.reason : ""
  const label = REASON_LABELS[reason]
  if (label !== undefined) return label
  if (reason !== "") return `${error.message}（${reason}）`
  return error.message
}

type FormState = {
  name: string
  label: string
  description: string
  systemId: number | null
  environmentId: number | null
  codeRepositoryId: number | null
}

const EMPTY_FORM: FormState = {
  name: "",
  label: "",
  description: "",
  systemId: null,
  environmentId: null,
  codeRepositoryId: null,
}

export type CreateVersionDialogProps = {
  open: boolean
  onClose: () => void
  /** Called with the created draft; the container decides where to navigate. */
  onCreated: (version: Version) => void
}

/**
 * Create-version dialog (spec §9.2): name is the only required field; the
 * optional system → environment → code repository cascade reads the P2
 * resource APIs and only sends the chosen numeric resource IDs. A resource
 * list failure never blocks creating an unbound version.
 */
export function CreateVersionDialog({ open, onClose, onCreated }: CreateVersionDialogProps) {
  const uid = useId()
  const nameRef = useRef<HTMLInputElement>(null)

  const [form, setForm] = useState<FormState>(EMPTY_FORM)
  const [systems, setSystems] = useState<SystemDTO[]>([])
  const [environments, setEnvironments] = useState<EnvironmentDTO[]>([])
  const [repos, setRepos] = useState<CodeRepoDTO[]>([])
  const [resourcesFailed, setResourcesFailed] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState("")

  useEffect(() => {
    if (!open) return
    let cancelled = false
    setForm(EMPTY_FORM)
    setSystems([])
    setEnvironments([])
    setRepos([])
    setResourcesFailed(false)
    setError("")
    setSaving(false)
    nameRef.current?.focus()

    void Promise.allSettled([listSystems({ limit: MAX_PAGE_SIZE }), listCodeRepos()]).then(
      ([systemsResult, reposResult]) => {
        if (cancelled) return
        if (systemsResult.status === "fulfilled") setSystems(systemsResult.value.items)
        if (reposResult.status === "fulfilled") setRepos(reposResult.value.items)
        if (systemsResult.status === "rejected" || reposResult.status === "rejected") {
          setResourcesFailed(true)
        }
      },
    )

    return () => {
      cancelled = true
    }
  }, [open])

  useEffect(() => {
    if (!open || form.systemId === null) {
      setEnvironments([])
      return
    }
    let cancelled = false
    setEnvironments([])
    void listEnvironments(form.systemId)
      .then((items) => {
        if (!cancelled) setEnvironments(items)
      })
      .catch(() => {
        if (!cancelled) {
          setEnvironments([])
          setResourcesFailed(true)
        }
      })
    return () => {
      cancelled = true
    }
  }, [open, form.systemId])

  function updateForm(patch: Partial<FormState>): void {
    setForm((previous) => ({ ...previous, ...patch }))
  }

  function handleSystemChange(value: string): void {
    updateForm({
      systemId: value === "" ? null : Number(value),
      environmentId: null,
    })
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    setError("")

    const name = form.name.trim()
    const label = form.label.trim()
    const description = form.description.trim()

    if (name === "") {
      setError("请输入版本名称。")
      return
    }
    if (name.length > NAME_MAX) {
      setError(`版本名称不能超过 ${NAME_MAX} 个字符。`)
      return
    }
    if (label.length > LABEL_MAX) {
      setError(`标识不能超过 ${LABEL_MAX} 个字符。`)
      return
    }
    if (description.length > DESCRIPTION_MAX) {
      setError(`描述不能超过 ${DESCRIPTION_MAX} 个字符。`)
      return
    }

    const body: CreateVersionBody = { name }
    if (label !== "") body.label = label
    if (description !== "") body.description = description
    if (form.systemId !== null) body.systemId = form.systemId
    if (form.environmentId !== null) body.environmentId = form.environmentId
    if (form.codeRepositoryId !== null) body.codeRepositoryId = form.codeRepositoryId

    setSaving(true)
    try {
      const created = await createVersion(body)
      onCreated(created)
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setSaving(false)
    }
  }

  if (!open) return null

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center overflow-y-auto p-4">
      <button
        type="button"
        tabIndex={-1}
        aria-label="关闭新建版本对话框"
        onClick={() => {
          if (!saving) onClose()
        }}
        className="absolute inset-0 bg-black/50"
      />
      <form
        role="dialog"
        aria-modal="true"
        aria-labelledby={`${uid}-title`}
        onSubmit={(event) => void handleSubmit(event)}
        onKeyDown={(event) => {
          if (event.key === "Escape" && !saving) onClose()
        }}
        className="relative w-full max-w-2xl space-y-4 rounded-2xl border border-border bg-card p-5 shadow-xl"
      >
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div>
            <h2 id={`${uid}-title`} className="text-base font-semibold">
              新建版本
            </h2>
            <p className="mt-1 text-xs text-muted-foreground">
              版本创建后为草稿；资源绑定可选，之后也可在工作台调整。
            </p>
          </div>
          <button
            type="button"
            onClick={onClose}
            disabled={saving}
            aria-label="关闭"
            className="rounded-lg border border-border p-1.5 text-muted-foreground transition-colors hover:bg-muted disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <X className="h-4 w-4" aria-hidden="true" />
          </button>
        </div>

        {resourcesFailed ? (
          <p
            role="status"
            className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300"
          >
            系统、环境或代码仓列表加载失败，仍可先创建未绑定资源的版本。
          </p>
        ) : null}

        <div className="grid gap-3 sm:grid-cols-2">
          <div>
            <label htmlFor={`${uid}-name`} className="text-xs font-medium text-muted-foreground">
              名称
            </label>
            <input
              ref={nameRef}
              id={`${uid}-name`}
              value={form.name}
              onChange={(event) => updateForm({ name: event.target.value })}
              maxLength={NAME_MAX}
              autoComplete="off"
              placeholder="例如 订单系统 2.0"
              className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
            />
          </div>

          <div>
            <label htmlFor={`${uid}-label`} className="text-xs font-medium text-muted-foreground">
              标识
            </label>
            <input
              id={`${uid}-label`}
              value={form.label}
              onChange={(event) => updateForm({ label: event.target.value })}
              maxLength={LABEL_MAX}
              autoComplete="off"
              placeholder="例如 v2.0"
              className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 font-mono text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring"
            />
          </div>

          <div className="sm:col-span-2">
            <label
              htmlFor={`${uid}-description`}
              className="text-xs font-medium text-muted-foreground"
            >
              描述
            </label>
            <textarea
              id={`${uid}-description`}
              value={form.description}
              onChange={(event) => updateForm({ description: event.target.value })}
              maxLength={DESCRIPTION_MAX}
              rows={2}
              placeholder="可选：版本目标、范围或备注"
              className="mt-1 w-full resize-y rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
            />
          </div>

          <div>
            <label htmlFor={`${uid}-system`} className="text-xs font-medium text-muted-foreground">
              系统
            </label>
            <select
              id={`${uid}-system`}
              value={form.systemId === null ? "" : String(form.systemId)}
              onChange={(event) => handleSystemChange(event.target.value)}
              className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <option value="">不绑定</option>
              {systems.map((system) => (
                <option key={system.id} value={system.id}>
                  {system.name}
                </option>
              ))}
            </select>
          </div>

          <div>
            <label
              htmlFor={`${uid}-environment`}
              className="text-xs font-medium text-muted-foreground"
            >
              环境
            </label>
            <select
              id={`${uid}-environment`}
              value={form.environmentId === null ? "" : String(form.environmentId)}
              onChange={(event) =>
                updateForm({
                  environmentId: event.target.value === "" ? null : Number(event.target.value),
                })
              }
              disabled={form.systemId === null}
              className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-60"
            >
              <option value="">{form.systemId === null ? "先选择系统" : "不绑定"}</option>
              {environments.map((environment) => (
                <option key={environment.id} value={environment.id}>
                  {environment.name}
                </option>
              ))}
            </select>
          </div>

          <div className="sm:col-span-2">
            <label
              htmlFor={`${uid}-code-repo`}
              className="text-xs font-medium text-muted-foreground"
            >
              代码仓
            </label>
            <select
              id={`${uid}-code-repo`}
              value={form.codeRepositoryId === null ? "" : String(form.codeRepositoryId)}
              onChange={(event) =>
                updateForm({
                  codeRepositoryId: event.target.value === "" ? null : Number(event.target.value),
                })
              }
              className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <option value="">不绑定</option>
              {repos.map((repo) => (
                <option key={repo.id} value={repo.id}>
                  {repo.status === "active" ? repo.name : `${repo.name}（停用）`}
                </option>
              ))}
            </select>
          </div>
        </div>

        {error === "" ? null : (
          <p
            role="alert"
            className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
          >
            {error}
          </p>
        )}

        <div className="flex justify-end gap-1.5">
          <button
            type="button"
            onClick={onClose}
            disabled={saving}
            className="rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            取消
          </button>
          <button
            type="submit"
            disabled={saving}
            className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3.5 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {saving ? <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" /> : null}
            {saving ? "创建中…" : "创建版本"}
          </button>
        </div>
      </form>
    </div>
  )
}
