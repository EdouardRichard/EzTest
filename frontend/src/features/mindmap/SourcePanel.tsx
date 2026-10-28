"use client"

import { Loader2, Plus, Trash2 } from "lucide-react"
import { useCallback, useEffect, useId, useState } from "react"

import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"
import { listRequirements } from "@/features/versions/api"

import { mindmapApi } from "./api"
import { describeMindmapError } from "./error-messages"
import type {
  CaseNodeView,
  CaseRefKind,
  CaseSourceView,
  CodeAnchorView,
  Confidence,
  CreateAnchorBody,
} from "./types"

/** Projected requirement option accepted from a version loader or fetched here. */
export type SourceRequirementOption = {
  id: string
  title: string
  /** When present, only `clarified` requirements are selectable (spec §6.2). */
  status?: string
}

const MAX_REQUIREMENTS = 100

const REF_KIND_LABEL: Record<CaseRefKind, string> = {
  requirement: "需求",
  app_model: "应用模型",
  ai_run: "AI 运行",
  manual: "手工",
}

const CONTENT_SOURCE_LABEL: Record<CaseSourceView["source"], string> = {
  user_input: "人工",
  ai_generated: "AI",
  ai_supplement: "AI 补充",
}

const CONFIDENCE_LABEL: Record<Confidence, string> = {
  high: "高",
  medium: "中",
  low: "低",
}

const CONFIDENCE_VARIANT: Record<Confidence, StatusBadgeVariant> = {
  high: "success",
  medium: "warning",
  low: "neutral",
}

const FIELD_CLASS =
  "mt-1 w-full rounded-lg border border-border bg-input px-2.5 py-1.5 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-70 read-only:cursor-not-allowed read-only:opacity-70"

const SMALL_BUTTON_CLASS =
  "inline-flex items-center gap-1 rounded-lg border border-border bg-card px-2 py-1 text-[11px] font-medium text-foreground transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

const PRIMARY_BUTTON_CLASS =
  "inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

/** `file:start-end` for one anchor; line numbers are optional (spec §7.3). */
function formatAnchorLocation(
  anchor: Pick<CodeAnchorView, "filePath" | "startLine" | "endLine">,
): string {
  if (typeof anchor.startLine !== "number") return anchor.filePath
  if (typeof anchor.endLine !== "number" || anchor.endLine === anchor.startLine) {
    return `${anchor.filePath}:${anchor.startLine}`
  }
  return `${anchor.filePath}:${anchor.startLine}-${anchor.endLine}`
}

export type SourcePanelProps = {
  versionId: string
  /** The selected case node; scenarios never carry sources (spec §6.2). */
  node: CaseNodeView
  /** Server-owned editability flag; the panel never infers it. */
  editable: boolean
  /** Optional preloaded confirmed requirements; fetched from the version API otherwise. */
  requirements?: readonly SourceRequirementOption[]
  /** Called after a successful source/anchor write so the graph can refetch. */
  onChanged: () => void
}

/**
 * Sources and code anchors of one case (spec §9.2): confirmed requirement
 * coverage, app-model/manual references and `file:line` anchors. Every case
 * keeps at least one requirement source, so deleting the last one is disabled
 * with an explicit reason.
 */
export function SourcePanel(props: SourcePanelProps) {
  if (props.node.kind !== "case") {
    return (
      <section
        data-testid="source-panel"
        aria-label="来源与锚点"
        className="min-w-0 rounded-2xl border border-border bg-card p-4"
      >
        <h2 className="text-base font-semibold">来源与锚点</h2>
        <p className="mt-2 text-sm text-muted-foreground">
          场景节点不记录需求来源与代码锚点，请选择用例节点查看。
        </p>
      </section>
    )
  }

  return <SourcePanelContent key={`${props.node.id}:${props.node.revision}`} {...props} />
}

function SourcePanelContent({
  versionId,
  node,
  editable,
  requirements,
  onChanged,
}: SourcePanelProps) {
  const uid = useId()
  const [sources, setSources] = useState<CaseSourceView[]>(() => node.sources)
  const [anchors, setAnchors] = useState<CodeAnchorView[]>(() => node.anchors ?? [])
  const [fetchedRequirements, setFetchedRequirements] = useState<SourceRequirementOption[]>([])
  const [requirementsLoading, setRequirementsLoading] = useState(requirements === undefined)
  const [requirementsError, setRequirementsError] = useState("")

  const [selectedRequirementId, setSelectedRequirementId] = useState("")
  const [refKind, setRefKind] = useState<"app_model" | "manual">("manual")
  const [refKey, setRefKey] = useState("")

  const [filePath, setFilePath] = useState("")
  const [startLine, setStartLine] = useState("")
  const [endLine, setEndLine] = useState("")

  const [busy, setBusy] = useState<string | null>(null)
  const [actionError, setActionError] = useState("")

  const loadRequirements = useCallback(async (): Promise<void> => {
    if (requirements !== undefined) {
      setRequirementsLoading(false)
      return
    }
    setRequirementsLoading(true)
    setRequirementsError("")
    try {
      const page = await listRequirements(versionId, {
        status: "clarified",
        limit: MAX_REQUIREMENTS,
      })
      setFetchedRequirements(
        page.items
          .filter((requirement) => requirement.status === "clarified")
          .map((requirement) => ({
            id: requirement.id,
            title: requirement.title,
            status: requirement.status,
          })),
      )
    } catch (caught) {
      setRequirementsError(describeMindmapError(caught))
    } finally {
      setRequirementsLoading(false)
    }
  }, [versionId, requirements])

  useEffect(() => {
    void loadRequirements()
  }, [loadRequirements])

  const requirementOptions = (requirements ?? fetchedRequirements).filter(
    (requirement) => requirement.status === undefined || requirement.status === "clarified",
  )
  const requirementSourceCount = sources.filter((source) => source.refKind === "requirement").length
  const linkedRequirementIds = new Set(
    sources
      .filter((source) => source.refKind === "requirement" && source.refId !== null)
      .map((source) => source.refId as string),
  )
  const selectableRequirements = requirementOptions.filter(
    (requirement) => !linkedRequirementIds.has(requirement.id),
  )

  function requirementTitle(refId: string | null): string {
    if (refId === null) return "未知需求"
    return requirementOptions.find((requirement) => requirement.id === refId)?.title ?? `#${refId}`
  }

  async function addRequirementSource(): Promise<void> {
    if (selectedRequirementId === "") {
      setActionError("请选择需求。")
      return
    }
    setBusy("requirement")
    setActionError("")
    try {
      const created = await mindmapApi.createSource(versionId, node.id, {
        refKind: "requirement",
        refId: selectedRequirementId,
      })
      setSources((current) => [...current, created])
      setSelectedRequirementId("")
      onChanged()
    } catch (caught) {
      setActionError(describeMindmapError(caught))
    } finally {
      setBusy(null)
    }
  }

  async function addRefSource(): Promise<void> {
    const key = refKey.trim()
    if (key === "") {
      setActionError("请填写来源引用。")
      return
    }
    setBusy("ref")
    setActionError("")
    try {
      const created = await mindmapApi.createSource(versionId, node.id, { refKind, refKey: key })
      setSources((current) => [...current, created])
      setRefKey("")
      onChanged()
    } catch (caught) {
      setActionError(describeMindmapError(caught))
    } finally {
      setBusy(null)
    }
  }

  async function deleteSource(source: CaseSourceView): Promise<void> {
    if (source.refKind === "requirement" && requirementSourceCount <= 1) return
    setBusy(`source-${source.id}`)
    setActionError("")
    try {
      await mindmapApi.deleteSource(versionId, source.id)
      setSources((current) => current.filter((item) => item.id !== source.id))
      onChanged()
    } catch (caught) {
      setActionError(describeMindmapError(caught))
    } finally {
      setBusy(null)
    }
  }

  /** Parses the optional range; returns an error message or the line numbers. */
  function parseLineRange(): { startLine?: number; endLine?: number } | string {
    const startText = startLine.trim()
    const endText = endLine.trim()
    if (startText === "" && endText === "") return {}
    if (startText === "" || endText === "") return "起始行与结束行需同时填写。"
    const start = Number(startText)
    const end = Number(endText)
    if (!Number.isInteger(start) || !Number.isInteger(end) || start < 1 || end < 1) {
      return "行号必须为正整数。"
    }
    if (start > end) return "起始行不能大于结束行。"
    return { startLine: start, endLine: end }
  }

  async function addAnchor(): Promise<void> {
    const trimmed = filePath.trim()
    if (trimmed === "") {
      setActionError("请填写文件路径。")
      return
    }
    const range = parseLineRange()
    if (typeof range === "string") {
      setActionError(range)
      return
    }

    const body: CreateAnchorBody = { filePath: trimmed }
    if (range.startLine !== undefined && range.endLine !== undefined) {
      body.startLine = range.startLine
      body.endLine = range.endLine
    }

    setBusy("anchor")
    setActionError("")
    try {
      const created = await mindmapApi.createAnchor(versionId, node.id, body)
      setAnchors((current) => [...current, created])
      setFilePath("")
      setStartLine("")
      setEndLine("")
      onChanged()
    } catch (caught) {
      setActionError(describeMindmapError(caught))
    } finally {
      setBusy(null)
    }
  }

  async function deleteAnchor(anchor: CodeAnchorView): Promise<void> {
    setBusy(`anchor-${anchor.id}`)
    setActionError("")
    try {
      await mindmapApi.deleteAnchor(versionId, anchor.id)
      setAnchors((current) => current.filter((item) => item.id !== anchor.id))
      onChanged()
    } catch (caught) {
      setActionError(describeMindmapError(caught))
    } finally {
      setBusy(null)
    }
  }

  const controlsDisabled = busy !== null

  return (
    <section
      data-testid="source-panel"
      aria-label="来源与锚点"
      className="min-w-0 space-y-4 rounded-2xl border border-border bg-card p-4"
    >
      <div>
        <h2 className="text-base font-semibold">来源与锚点</h2>
        <p className="mt-1 text-[11px] text-muted-foreground">
          需求来源决定覆盖矩阵；代码锚点标记相关文件位置。
        </p>
      </div>

      {editable ? null : (
        <p className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300">
          用例已冻结，来源与锚点只读。
        </p>
      )}

      <section aria-label="需求来源" className="space-y-2">
        <div className="flex items-baseline justify-between gap-2">
          <h3 className="text-sm font-semibold">需求来源</h3>
          <span className="text-xs text-muted-foreground">{`${requirementSourceCount} 条`}</span>
        </div>

        {sources.length === 0 ? (
          <p className="text-xs text-muted-foreground">
            该用例还没有来源，请先关联至少一条已确认需求。
          </p>
        ) : (
          <ul className="divide-y divide-border">
            {sources.map((source) => {
              const deleteBlocked = source.refKind === "requirement" && requirementSourceCount <= 1
              return (
                <li
                  key={source.id}
                  data-testid={`source-row-${source.id}`}
                  className="flex flex-wrap items-center justify-between gap-2 py-2"
                >
                  <div className="min-w-0 space-y-1">
                    <p className="break-all text-xs text-foreground">
                      {source.refKind === "requirement"
                        ? `${REF_KIND_LABEL.requirement}：${requirementTitle(source.refId)}`
                        : `${REF_KIND_LABEL[source.refKind]}：${source.refKey || source.refId || "—"}`}
                    </p>
                    <div className="flex flex-wrap items-center gap-1.5">
                      <StatusBadge variant="neutral">
                        {CONTENT_SOURCE_LABEL[source.source]}
                      </StatusBadge>
                      <StatusBadge variant={CONFIDENCE_VARIANT[source.confidence]}>
                        {`置信度 ${CONFIDENCE_LABEL[source.confidence]}`}
                      </StatusBadge>
                    </div>
                  </div>
                  <button
                    type="button"
                    aria-label={`删除来源 ${source.id}`}
                    disabled={!editable || controlsDisabled || deleteBlocked}
                    aria-describedby={deleteBlocked ? `${uid}-last-requirement` : undefined}
                    onClick={() => void deleteSource(source)}
                    className={SMALL_BUTTON_CLASS}
                  >
                    {busy === `source-${source.id}` ? (
                      <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
                    ) : (
                      <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
                    )}
                    删除
                  </button>
                </li>
              )
            })}
          </ul>
        )}

        {requirementSourceCount === 1 ? (
          <p id={`${uid}-last-requirement`} className="text-[11px] text-muted-foreground">
            每个用例至少保留一条需求来源，最后一条不可删除。
          </p>
        ) : null}

        {requirementsError === "" ? null : (
          <div
            role="alert"
            className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
          >
            <span>{requirementsError}</span>
            <button
              type="button"
              onClick={() => void loadRequirements()}
              className={SMALL_BUTTON_CLASS}
            >
              重试
            </button>
          </div>
        )}

        {editable ? (
          <div className="space-y-2 rounded-xl border border-dashed border-border p-2">
            <div className="flex flex-wrap items-end gap-2">
              <label className="min-w-0 flex-1 text-[11px] text-muted-foreground">
                已确认需求
                <select
                  aria-label="选择需求"
                  value={selectedRequirementId}
                  disabled={
                    controlsDisabled || requirementsLoading || selectableRequirements.length === 0
                  }
                  onChange={(event) => setSelectedRequirementId(event.target.value)}
                  className={FIELD_CLASS}
                >
                  <option value="">{requirementsLoading ? "正在加载需求…" : "请选择需求"}</option>
                  {selectableRequirements.map((requirement) => (
                    <option key={requirement.id} value={requirement.id}>
                      {`${requirement.title}（#${requirement.id}）`}
                    </option>
                  ))}
                </select>
              </label>
              <button
                type="button"
                disabled={controlsDisabled || selectedRequirementId === ""}
                onClick={() => void addRequirementSource()}
                className={PRIMARY_BUTTON_CLASS}
              >
                <Plus className="h-3.5 w-3.5" aria-hidden="true" />
                添加需求来源
              </button>
            </div>
            {!requirementsLoading && selectableRequirements.length === 0 ? (
              <p className="text-[11px] text-muted-foreground">
                没有可关联的已确认需求（需求需先澄清）。
              </p>
            ) : null}

            <div className="flex flex-wrap items-end gap-2 border-t border-border pt-2">
              <label className="w-28 text-[11px] text-muted-foreground">
                来源类型
                <select
                  aria-label="来源类型"
                  value={refKind}
                  disabled={controlsDisabled}
                  onChange={(event) => setRefKind(event.target.value as "app_model" | "manual")}
                  className={FIELD_CLASS}
                >
                  <option value="app_model">应用模型</option>
                  <option value="manual">手工</option>
                </select>
              </label>
              <label className="min-w-0 flex-1 text-[11px] text-muted-foreground">
                来源引用
                <input
                  aria-label="来源引用"
                  value={refKey}
                  disabled={controlsDisabled}
                  placeholder="例如 app_model:page:/orders"
                  onChange={(event) => setRefKey(event.target.value)}
                  className={FIELD_CLASS}
                />
              </label>
              <button
                type="button"
                disabled={controlsDisabled || refKey.trim() === ""}
                onClick={() => void addRefSource()}
                className={PRIMARY_BUTTON_CLASS}
              >
                <Plus className="h-3.5 w-3.5" aria-hidden="true" />
                添加来源
              </button>
            </div>
          </div>
        ) : null}
      </section>

      <section aria-label="代码锚点" className="space-y-2 border-t border-border pt-3">
        <div className="flex items-baseline justify-between gap-2">
          <h3 className="text-sm font-semibold">代码锚点</h3>
          <span className="text-xs text-muted-foreground">{`${anchors.length} 个`}</span>
        </div>

        {anchors.length === 0 ? (
          <p className="text-xs text-muted-foreground">暂无代码锚点，可添加 file:line 引用。</p>
        ) : (
          <ul className="divide-y divide-border">
            {anchors.map((anchor) => {
              const location = formatAnchorLocation(anchor)
              return (
                <li
                  key={anchor.id}
                  data-testid={`anchor-row-${anchor.id}`}
                  className="flex flex-wrap items-center justify-between gap-2 py-2"
                >
                  <div className="min-w-0 space-y-1">
                    <p className="break-all font-mono text-xs text-foreground">{location}</p>
                    <div className="flex flex-wrap items-center gap-1.5">
                      <StatusBadge variant={CONFIDENCE_VARIANT[anchor.confidence]}>
                        {`置信度 ${CONFIDENCE_LABEL[anchor.confidence]}`}
                      </StatusBadge>
                      {anchor.commitSha === null ? null : (
                        <span className="font-mono text-[11px] text-muted-foreground">
                          {anchor.commitSha.slice(0, 12)}
                        </span>
                      )}
                    </div>
                  </div>
                  <button
                    type="button"
                    aria-label={`删除锚点 ${location}`}
                    disabled={!editable || controlsDisabled}
                    onClick={() => void deleteAnchor(anchor)}
                    className={SMALL_BUTTON_CLASS}
                  >
                    {busy === `anchor-${anchor.id}` ? (
                      <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
                    ) : (
                      <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
                    )}
                    删除
                  </button>
                </li>
              )
            })}
          </ul>
        )}

        {editable ? (
          <div className="rounded-xl border border-dashed border-border p-2">
            <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_5rem_5rem]">
              <label className="block text-[11px] text-muted-foreground">
                文件路径
                <input
                  id={`${uid}-anchor-file`}
                  value={filePath}
                  disabled={controlsDisabled}
                  placeholder="src/order.ts"
                  onChange={(event) => setFilePath(event.target.value)}
                  className={`${FIELD_CLASS} font-mono`}
                />
              </label>
              <label className="block text-[11px] text-muted-foreground">
                起始行（可选）
                <input
                  id={`${uid}-anchor-start`}
                  type="number"
                  min={1}
                  value={startLine}
                  disabled={controlsDisabled}
                  onChange={(event) => setStartLine(event.target.value)}
                  className={`${FIELD_CLASS} font-mono`}
                />
              </label>
              <label className="block text-[11px] text-muted-foreground">
                结束行（可选）
                <input
                  id={`${uid}-anchor-end`}
                  type="number"
                  min={1}
                  value={endLine}
                  disabled={controlsDisabled}
                  onChange={(event) => setEndLine(event.target.value)}
                  className={`${FIELD_CLASS} font-mono`}
                />
              </label>
            </div>
            <div className="mt-2 flex justify-end">
              <button
                type="button"
                disabled={controlsDisabled || filePath.trim() === ""}
                onClick={() => void addAnchor()}
                className={PRIMARY_BUTTON_CLASS}
              >
                <Plus className="h-3.5 w-3.5" aria-hidden="true" />
                添加锚点
              </button>
            </div>
          </div>
        ) : null}
      </section>

      {actionError === "" ? null : (
        <p
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
        >
          {actionError}
        </p>
      )}
    </section>
  )
}
