"use client"

import { ChevronDown, ChevronRight, Copy, Plus } from "lucide-react"
import { useCallback, useEffect, useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { InlineError } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Input } from "@/components/ui/Input"
import { Select } from "@/components/ui/Select"
import { LoadingCard } from "@/components/ui/Spinner"
import { formatDateTime } from "@/lib/format"

import {
  createCodeAnchor,
  deleteCodeAnchor,
  listCodeAnchors,
  listVersionCodeRepos,
  validateCodeAnchor,
} from "./api"
import {
  CODE_ANCHOR_STATUS_LABEL,
  CODE_ANCHOR_STATUS_TONE,
  type CodeAnchor,
  type CodeRepoOption,
  type CodeSubjectKind,
} from "./types"

export type AnchorPanelProps = {
  versionId: string
  subjectKind: CodeSubjectKind
  subjectId: string
  /** Section label; defaults to "代码锚点". */
  title?: string
  /** Open immediately (used by tests/deep panels); the default is collapsed. */
  defaultOpen?: boolean
}

type FormErrors = {
  filePath?: string
  startLine?: string
  endLine?: string
  repoId?: string
}

function anchorPath(anchor: CodeAnchor): string {
  const start = anchor.start_line ?? null
  const end = anchor.end_line ?? null
  if (start === null) return anchor.file_path
  if (end === null) return `${anchor.file_path}:${start}`
  return `${anchor.file_path}:${start}-${end}`
}

function parseLine(value: string): number | undefined {
  if (value.trim().length === 0) return undefined
  const parsed = Number(value)
  return Number.isInteger(parsed) && parsed > 0 ? parsed : Number.NaN
}

/**
 * Code-anchor panel for a design case version, requirement or issue
 * (spec §9.4): list/add/validate/delete with the valid/stale/broken state kept
 * visible instead of hiding invalid anchors. Collapsed by default so dense
 * panels do not fetch until the user opens the section.
 */
export function AnchorPanel({
  versionId,
  subjectKind,
  subjectId,
  title = "代码锚点",
  defaultOpen = false,
}: AnchorPanelProps) {
  const [open, setOpen] = useState(defaultOpen)
  const [anchors, setAnchors] = useState<CodeAnchor[]>([])
  const [repos, setRepos] = useState<CodeRepoOption[]>([])
  const [loading, setLoading] = useState(false)
  const [listError, setListError] = useState<unknown>(null)
  const [reloadToken, setReloadToken] = useState(0)
  const [showingAdd, setShowingAdd] = useState(false)
  const [repoId, setRepoId] = useState("")
  const [filePath, setFilePath] = useState("")
  const [startLine, setStartLine] = useState("")
  const [endLine, setEndLine] = useState("")
  const [formErrors, setFormErrors] = useState<FormErrors>({})
  const [actionError, setActionError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [busyId, setBusyId] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken re-runs the list refetch
  useEffect(() => {
    if (!open) return undefined
    let cancelled = false
    setLoading(true)
    setListError(null)

    void listCodeAnchors(versionId, { subjectKind, subjectId })
      .then((items) => {
        if (!cancelled) setAnchors(items)
      })
      .catch((cause: unknown) => {
        if (!cancelled) setListError(cause)
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })

    void listVersionCodeRepos(versionId)
      .then((items) => {
        if (!cancelled) setRepos(items)
      })
      .catch(() => {
        if (!cancelled) setRepos([])
      })

    return () => {
      cancelled = true
    }
  }, [open, versionId, subjectKind, subjectId, reloadToken])

  useEffect(() => {
    if (!showingAdd) return
    setRepoId((current) => {
      if (current.length > 0 && repos.some((repo) => repo.id === current)) return current
      return repos[0]?.id ?? ""
    })
  }, [repos, showingAdd])

  const reload = useCallback(() => setReloadToken((token) => token + 1), [])

  async function copyPath(anchor: CodeAnchor): Promise<void> {
    const text = anchorPath(anchor)
    try {
      if (typeof navigator !== "undefined" && navigator.clipboard !== undefined) {
        await navigator.clipboard.writeText(text)
      }
      setNotice("已复制路径")
    } catch (cause) {
      setActionError(cause)
    }
  }

  async function validate(anchor: CodeAnchor): Promise<void> {
    setBusyId(anchor.id)
    setActionError(null)
    setNotice(null)
    try {
      const updated = await validateCodeAnchor(versionId, anchor.id)
      setAnchors((current) => current.map((item) => (item.id === updated.id ? updated : item)))
      setNotice("锚点状态已更新")
    } catch (cause) {
      setActionError(cause)
    } finally {
      setBusyId(null)
    }
  }

  async function remove(anchor: CodeAnchor): Promise<void> {
    setBusyId(anchor.id)
    setActionError(null)
    setNotice(null)
    try {
      await deleteCodeAnchor(versionId, anchor.id)
      setAnchors((current) => current.filter((item) => item.id !== anchor.id))
      setNotice("锚点已删除")
    } catch (cause) {
      setActionError(cause)
    } finally {
      setBusyId(null)
    }
  }

  async function submit(): Promise<void> {
    const errors: FormErrors = {}
    const trimmedPath = filePath.trim()
    const start = parseLine(startLine)
    const end = parseLine(endLine)

    if (trimmedPath.length === 0) errors.filePath = "文件路径不能为空"
    if (start !== undefined && !Number.isInteger(start)) errors.startLine = "起始行必须是正整数"
    if (end !== undefined && !Number.isInteger(end)) errors.endLine = "结束行必须是正整数"
    if (
      start !== undefined &&
      end !== undefined &&
      Number.isInteger(start) &&
      Number.isInteger(end) &&
      end < start
    ) {
      errors.endLine = "结束行不能小于起始行"
    }
    if (repoId.length === 0) errors.repoId = "请选择代码仓库"

    setFormErrors(errors)
    setActionError(null)
    setNotice(null)
    if (Object.keys(errors).length > 0) return

    setSaving(true)
    try {
      await createCodeAnchor(versionId, {
        subjectKind,
        subjectId,
        repoId,
        filePath: trimmedPath,
        ...(start !== undefined ? { startLine: start } : {}),
        ...(end !== undefined ? { endLine: end } : {}),
      })
      setNotice("锚点已创建")
      setShowingAdd(false)
      setFilePath("")
      setStartLine("")
      setEndLine("")
      setFormErrors({})
      reload()
    } catch (cause) {
      setActionError(cause)
    } finally {
      setSaving(false)
    }
  }

  return (
    <section data-testid="anchor-panel" aria-label={title} className="space-y-2">
      <button
        type="button"
        data-testid="anchor-panel-toggle"
        aria-expanded={open}
        onClick={() => {
          setOpen((current) => !current)
          setNotice(null)
        }}
        className="flex w-full items-center gap-1 text-left text-xs font-semibold text-foreground"
      >
        {open ? (
          <ChevronDown className="size-3.5" aria-hidden="true" />
        ) : (
          <ChevronRight className="size-3.5" aria-hidden="true" />
        )}
        {title}
        {anchors.length > 0 ? (
          <span className="text-[10px] font-normal text-muted-foreground">({anchors.length})</span>
        ) : null}
      </button>

      {open ? (
        <div className="space-y-2">
          {notice !== null ? (
            <p role="status" className="text-[11px] text-success">
              {notice}
            </p>
          ) : null}
          {actionError !== null ? <InlineError error={actionError} /> : null}

          {loading ? <LoadingCard label="正在加载代码锚点…" /> : null}

          {!loading && listError !== null ? (
            <div className="space-y-1.5">
              <InlineError error={listError} />
              <Button variant="secondary" size="sm" onClick={reload}>
                重试
              </Button>
            </div>
          ) : null}

          {!loading && listError === null && anchors.length === 0 && !showingAdd ? (
            <p className="text-[11px] text-muted-foreground">暂无代码锚点。</p>
          ) : null}

          {!loading && listError === null && anchors.length > 0 ? (
            <ul className="space-y-1.5">
              {anchors.map((anchor) => (
                <li
                  key={anchor.id}
                  data-testid={`anchor-row-${anchor.id}`}
                  className="rounded-lg border border-border bg-card px-2.5 py-2 text-[11px]"
                >
                  <div className="flex flex-wrap items-center gap-1.5">
                    <Badge tone={CODE_ANCHOR_STATUS_TONE[anchor.status]}>
                      {CODE_ANCHOR_STATUS_LABEL[anchor.status]}
                    </Badge>
                    <code className="break-all font-mono text-foreground">
                      {anchorPath(anchor)}
                    </code>
                    <Button
                      variant="ghost"
                      size="sm"
                      aria-label="复制路径"
                      onClick={() => {
                        void copyPath(anchor)
                      }}
                    >
                      <Copy className="size-3" aria-hidden="true" />
                      复制路径
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      disabled={busyId === anchor.id}
                      onClick={() => {
                        void validate(anchor)
                      }}
                    >
                      校验
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      disabled={busyId === anchor.id}
                      onClick={() => {
                        void remove(anchor)
                      }}
                    >
                      删除
                    </Button>
                  </div>
                  <p className="mt-0.5 text-[10px] text-muted-foreground">
                    仓库 {anchor.repo_id} · 置信度 {anchor.confidence}
                    {anchor.commit_sha !== null ? ` · commit ${anchor.commit_sha.slice(0, 8)}` : ""}
                    {anchor.last_validated_at !== null
                      ? ` · 校验于 ${formatDateTime(anchor.last_validated_at)}`
                      : ""}
                  </p>
                </li>
              ))}
            </ul>
          ) : null}

          {!showingAdd ? (
            <Button
              variant="secondary"
              size="sm"
              onClick={() => {
                setShowingAdd(true)
                setFormErrors({})
                setNotice(null)
              }}
            >
              <Plus className="size-3.5" aria-hidden="true" />
              添加锚点
            </Button>
          ) : (
            <div className="space-y-2 rounded-lg border border-border bg-muted/20 p-3">
              <div className="grid gap-2 sm:grid-cols-2">
                <Field id="anchor-repo" label="代码仓库" error={formErrors.repoId}>
                  <Select
                    id="anchor-repo"
                    value={repoId}
                    onChange={(event) => setRepoId(event.target.value)}
                  >
                    {repos.length === 0 ? <option value="">（无绑定仓库）</option> : null}
                    {repos.map((repo) => (
                      <option key={repo.id} value={repo.id}>
                        {repo.name}
                      </option>
                    ))}
                  </Select>
                </Field>
                <Field id="anchor-file-path" label="文件路径" error={formErrors.filePath}>
                  <Input
                    id="anchor-file-path"
                    value={filePath}
                    placeholder="src/pages/OrdersPage.tsx"
                    onChange={(event) => setFilePath(event.target.value)}
                  />
                </Field>
                <Field id="anchor-start-line" label="起始行" error={formErrors.startLine}>
                  <Input
                    id="anchor-start-line"
                    type="number"
                    min={1}
                    value={startLine}
                    onChange={(event) => setStartLine(event.target.value)}
                  />
                </Field>
                <Field id="anchor-end-line" label="结束行" error={formErrors.endLine}>
                  <Input
                    id="anchor-end-line"
                    type="number"
                    min={1}
                    value={endLine}
                    onChange={(event) => setEndLine(event.target.value)}
                  />
                </Field>
              </div>
              <div className="flex items-center gap-2">
                <Button
                  size="sm"
                  disabled={saving}
                  onClick={() => {
                    void submit()
                  }}
                >
                  {saving ? "保存中…" : "保存锚点"}
                </Button>
                <Button
                  variant="secondary"
                  size="sm"
                  disabled={saving}
                  onClick={() => {
                    setShowingAdd(false)
                    setFormErrors({})
                    setActionError(null)
                  }}
                >
                  取消
                </Button>
              </div>
            </div>
          )}
        </div>
      ) : null}
    </section>
  )
}
