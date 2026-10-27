"use client"

import { useCallback, useId, useMemo, useState } from "react"

import { patchCandidate } from "./api"
import { ConfidenceBadge } from "./ConfidenceBadge"
import { EvidenceLink } from "./EvidenceLink"
import { describeCodeError } from "./empty"
import type { Analysis, Candidate } from "./types"

const GROUP_ORDER = ["requirement", "app_model"] as const

const GROUP_LABELS: Record<string, string> = {
  requirement: "需求",
  app_model: "应用模型",
}

const GROUP_HEADINGS: Record<string, string> = {
  requirement: "需求候选",
  app_model: "应用模型候选",
}

const SOURCE_LABELS: Record<string, string> = {
  direct: "直接",
  contract: "契约",
  relation: "关系",
  ai: "AI",
}

export type ImpactPanelProps = {
  analysis: Analysis
  /** Called after a candidate PATCH is persisted (the container may refresh). */
  onChanged?: () => void
}

/**
 * Impact candidates grouped by subject kind (spec §9.2): requirement first,
 * then app_model. Each checkbox PATCHes `selected` with an optimistic update;
 * a failure rolls the checkbox back and shows a toast-like alert. `low`
 * candidates arrive unselected from the backend but stay manually selectable
 * (spec §6.6 step 6), and a degraded analysis keeps its T0-only banner.
 */
export function ImpactPanel({ analysis, onChanged }: ImpactPanelProps) {
  const headingId = useId()
  const [selectedOverrides, setSelectedOverrides] = useState<Record<string, boolean>>({})
  const [savingIds, setSavingIds] = useState<Set<string>>(new Set())
  const [saveError, setSaveError] = useState("")

  // The container mounts this panel with `key={analysis.id}`, so optimistic
  // state never leaks from one analysis into the next.

  const terminal = analysis.status === "done"

  const groups = useMemo(
    () =>
      GROUP_ORDER.map((kind) => ({
        kind,
        heading: GROUP_HEADINGS[kind] ?? kind,
        candidates: analysis.candidates.filter((candidate) => candidate.subjectKind === kind),
      })).filter((group) => group.candidates.length > 0),
    [analysis.candidates],
  )

  const handleToggle = useCallback(
    async (candidate: Candidate, current: boolean): Promise<void> => {
      setSaveError("")
      setSelectedOverrides((previous) => ({ ...previous, [candidate.id]: !current }))
      setSavingIds((previous) => {
        const next = new Set(previous)
        next.add(candidate.id)
        return next
      })
      try {
        const updated = await patchCandidate(
          analysis.versionId,
          analysis.id,
          candidate.id,
          !current,
        )
        // Trust the server's authoritative selection after the PATCH.
        setSelectedOverrides((previous) => ({ ...previous, [candidate.id]: updated.selected }))
        onChanged?.()
      } catch (caught) {
        setSelectedOverrides((previous) => ({ ...previous, [candidate.id]: current }))
        setSaveError(`保存候选失败：${describeCodeError(caught)}`)
      } finally {
        setSavingIds((previous) => {
          const next = new Set(previous)
          next.delete(candidate.id)
          return next
        })
      }
    },
    [analysis.id, analysis.versionId, onChanged],
  )

  return (
    <section
      aria-labelledby={headingId}
      className="space-y-3 rounded-2xl border border-border bg-card p-4"
    >
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 id={headingId} className="text-base font-semibold">
          影响候选
        </h2>
        <p className="text-xs text-muted-foreground">{analysis.candidates.length} 个候选</p>
      </div>

      {analysis.degraded ? (
        <p
          role="status"
          className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300"
        >
          AI 不可用，仅文件级结果
        </p>
      ) : null}

      {saveError === "" ? null : (
        <p
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
        >
          {saveError}
        </p>
      )}

      {!terminal ? (
        <p role="status" className="text-sm text-muted-foreground">
          {analysis.status === "queued" || analysis.status === "running"
            ? "分析运行中，候选完成后可调整。"
            : "分析失败，暂无影响候选。"}
        </p>
      ) : groups.length === 0 ? (
        <p className="text-sm text-muted-foreground">暂无影响候选</p>
      ) : (
        <div className="space-y-4">
          {groups.map((group) => (
            <div key={group.kind} className="space-y-2">
              <div className="flex items-baseline justify-between gap-2">
                <h3 className="text-sm font-semibold">{group.heading}</h3>
                <span className="text-xs text-muted-foreground">{group.candidates.length} 个</span>
              </div>
              <ul className="divide-y divide-border">
                {group.candidates.map((candidate) => {
                  const saving = savingIds.has(candidate.id)
                  const selected = selectedOverrides[candidate.id] ?? candidate.selected
                  const subjectLabel = GROUP_LABELS[candidate.subjectKind] ?? candidate.subjectKind
                  const checkboxId = `${headingId}-candidate-${candidate.id}`
                  return (
                    <li
                      key={candidate.id}
                      data-confidence={candidate.confidence}
                      data-source={candidate.source}
                      className="flex gap-3 py-3"
                    >
                      <input
                        id={checkboxId}
                        type="checkbox"
                        checked={selected}
                        disabled={saving}
                        aria-label={`选择${subjectLabel} ${candidate.subjectId}`}
                        aria-busy={saving}
                        onChange={() => void handleToggle(candidate, selected)}
                        className="mt-1 h-4 w-4 shrink-0 rounded border-border text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-wait disabled:opacity-50"
                      />
                      <div className="min-w-0 flex-1 space-y-1.5">
                        <div className="flex flex-wrap items-center gap-1.5">
                          <label
                            htmlFor={checkboxId}
                            className="break-all font-mono text-sm text-foreground"
                          >
                            {subjectLabel} #{candidate.subjectId}
                          </label>
                          <span className="text-[11px] text-muted-foreground">
                            来源 {SOURCE_LABELS[candidate.source] ?? candidate.source}
                          </span>
                          <ConfidenceBadge confidence={candidate.confidence} />
                          {saving ? (
                            <span role="status" className="text-[11px] text-muted-foreground">
                              保存中…
                            </span>
                          ) : null}
                        </div>
                        <p className="break-all text-sm text-foreground">{candidate.reason}</p>
                        <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
                          <span>证据</span>
                          {candidate.evidence.length === 0 ? (
                            <span>无有效证据</span>
                          ) : (
                            candidate.evidence.map((evidence) => (
                              <EvidenceLink
                                key={`${evidence.file}:${evidence.startLine ?? ""}:${
                                  evidence.endLine ?? ""
                                }:${evidence.excerptHash ?? ""}`}
                                repoId={analysis.repoId}
                                commit={analysis.headCommit}
                                evidence={evidence}
                              />
                            ))
                          )}
                        </div>
                      </div>
                    </li>
                  )
                })}
              </ul>
            </div>
          ))}
        </div>
      )}
    </section>
  )
}
