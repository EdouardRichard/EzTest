"use client"

import { useCallback, useEffect, useState } from "react"

import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { AnchorPanel } from "@/features/anchors/AnchorPanel"
import { listVisualAssertions } from "@/features/visual/api"
import { VisualConfigEditor } from "@/features/visual/components/VisualConfigEditor"
import type { VisualAssertion } from "@/features/visual/types"
import { formatDateTime } from "@/lib/format"

import { getDesignCase } from "./api"
import { CASE_VERSION_STATUS_LABEL } from "./nodeActions"
import type {
  DesignAssertion,
  DesignAssertionKind,
  DesignCaseDetail,
  DesignCaseVersionDetail,
} from "./types"

function statusTone(status: DesignCaseVersionDetail["status"]): BadgeTone {
  if (status === "confirmed") return "success"
  if (status === "stale") return "warning"
  if (status === "superseded") return "neutral"
  return "info"
}

const ASSERTION_KIND_LABEL: Record<DesignAssertionKind, string> = {
  ui: "UI",
  api: "API",
  db: "DB",
  text: "文本",
  visual: "视觉",
}

/**
 * Visual assertion config section (spec §7.1/§9.4). It maps the design
 * assertion to its P5 config by `(step_seq, assertion_seq)`, because the frozen
 * design DTO does not expose the P2 `case_assertion` id; when a config exists,
 * its `case_assertion_id` is reused for the upsert path. No P2 confirmation or
 * edit semantics are touched here.
 */
function CaseVisualAssertions({
  versionId,
  selected,
  previousCaseVersionId,
}: {
  versionId: string
  selected: DesignCaseVersionDetail
  previousCaseVersionId?: string
}) {
  const visualAssertions = selected.assertions
    .map((assertion, index) => {
      let assertionSeq = 0
      for (let cursor = 0; cursor <= index; cursor += 1) {
        if (selected.assertions[cursor].step_seq === assertion.step_seq) assertionSeq += 1
      }
      return { assertion, assertionSeq }
    })
    .filter((item) => item.assertion.kind === "visual")

  const [configs, setConfigs] = useState<VisualAssertion[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [editingKey, setEditingKey] = useState<string | null>(null)
  const [reloadToken, setReloadToken] = useState(0)

  const hasVisual = visualAssertions.length > 0

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken/revision refetch the configs
  useEffect(() => {
    if (!hasVisual) return undefined
    let cancelled = false
    setLoading(true)
    setError(null)
    void listVisualAssertions(versionId, { caseVersionId: selected.case_version_id })
      .then((items) => {
        if (!cancelled) setConfigs(items)
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
  }, [versionId, selected.case_version_id, hasVisual, reloadToken])

  if (!hasVisual) return null

  function keyOf(assertion: DesignAssertion, assertionSeq: number): string {
    return `${assertion.step_seq}-${assertionSeq}`
  }

  function configOf(assertion: DesignAssertion, assertionSeq: number): VisualAssertion | undefined {
    return configs.find(
      (config) => config.step_seq === assertion.step_seq && config.assertion_seq === assertionSeq,
    )
  }

  return (
    <section aria-label="视觉断言配置" className="space-y-2">
      <header className="flex flex-wrap items-center gap-2">
        <h3 className="text-xs font-semibold text-foreground">视觉断言配置</h3>
        <Badge tone="info">{visualAssertions.length} 条</Badge>
        <span className="text-[10px] text-muted-foreground">
          配置走 P5 visual API；保存不改变 P2 断言确认语义。
        </span>
      </header>

      {loading ? <p className="text-[11px] text-muted-foreground">正在加载视觉配置…</p> : null}
      {error !== null ? <InlineError error={error} /> : null}

      {visualAssertions.map(({ assertion, assertionSeq }) => {
        const key = keyOf(assertion, assertionSeq)
        const config = configOf(assertion, assertionSeq)
        const caseAssertionId = assertion.case_assertion_id ?? config?.case_assertion_id
        return (
          <div key={key} className="space-y-1.5">
            <div className="flex flex-wrap items-center gap-1.5 rounded-md border border-border bg-muted/20 px-2.5 py-2 text-[11px]">
              <span className="font-medium text-foreground">
                视觉断言 {assertion.step_seq}.{assertionSeq}
              </span>
              <span className="text-muted-foreground">
                {assertion.expected || assertion.target}
              </span>
              {config !== undefined ? (
                <Badge tone={config.baseline ? "success" : "warning"}>
                  {config.baseline ? `基线已批准 r${config.config_revision}` : "缺失基线"}
                </Badge>
              ) : (
                <Badge tone="neutral">未配置</Badge>
              )}
              <Button
                variant="secondary"
                size="sm"
                className="ml-auto"
                aria-expanded={editingKey === key}
                onClick={() => setEditingKey((current) => (current === key ? null : key))}
              >
                配置视觉断言
              </Button>
            </div>
            {editingKey === key ? (
              <VisualConfigEditor
                versionId={versionId}
                caseVersionId={selected.case_version_id}
                caseAssertionId={caseAssertionId}
                versionSutId={config?.version_sut_id ?? selected.default_version_sut_id}
                assertion={{
                  step_seq: assertion.step_seq,
                  assertion_seq: assertionSeq,
                  expected: assertion.expected,
                  case_assertion_id: assertion.case_assertion_id,
                }}
                initial={config ?? null}
                previousCaseVersionId={previousCaseVersionId}
                onSaved={() => setReloadToken((token) => token + 1)}
              />
            ) : null}
          </div>
        )
      })}
    </section>
  )
}

export type CaseDetailPanelProps = {
  versionId: string
  caseId: string | null
  /** Bumped by the page after a confirmation/SSE change to refetch the detail. */
  refreshToken?: number
  onConfirm: (caseId: string) => void
  /** Fills the design chat input with a modification request (never sends). */
  onRequestChange: (title: string) => void
  confirming?: boolean
}

function CaseDetailBody({
  versionId,
  detail,
  selectedVersionId,
  onSelectVersion,
  onConfirm,
  onRequestChange,
  confirming,
}: {
  versionId: string
  detail: DesignCaseDetail
  selectedVersionId: string | undefined
  onSelectVersion: (caseVersionId: string) => void
  onConfirm: (caseId: string) => void
  onRequestChange: (title: string) => void
  confirming: boolean
}) {
  const selected = detail.selected
  const canConfirm = detail.confirmable && selected.status === "draft"
  const previousVersion = [...detail.versions]
    .filter((version) => version.revision < selected.revision)
    .sort((left, right) => right.revision - left.revision)[0]
  const visualConfigTarget = {
    versionId,
    selected,
    previousCaseVersionId: previousVersion?.case_version_id,
  }

  return (
    <div className="space-y-4">
      <header className="flex flex-wrap items-start gap-2">
        <div className="min-w-0 flex-1">
          <h2 className="text-sm font-semibold text-foreground">{selected.title}</h2>
          <p className="mt-0.5 text-[11px] text-muted-foreground">
            场景 {detail.scenario_name} · 需求 {detail.requirement_id} · v{selected.revision}
          </p>
        </div>
        <Badge tone={statusTone(selected.status)}>
          {CASE_VERSION_STATUS_LABEL[selected.status]}
        </Badge>
        <Button
          size="sm"
          variant="secondary"
          onClick={() => onRequestChange(`请修改：${selected.title}`)}
        >
          请求修改
        </Button>
        {canConfirm ? (
          <Button
            size="sm"
            data-testid={`btn-confirm-detail-${detail.case_id}`}
            disabled={confirming}
            onClick={() => onConfirm(detail.case_id)}
          >
            {confirming ? "确认中…" : "确认用例"}
          </Button>
        ) : null}
      </header>

      <dl className="grid grid-cols-2 gap-2 text-[11px] sm:grid-cols-4">
        <div>
          <dt className="text-muted-foreground">优先级</dt>
          <dd className="font-medium text-foreground">{selected.priority}</dd>
        </div>
        <div>
          <dt className="text-muted-foreground">默认 SUT</dt>
          <dd className="font-medium text-foreground">{selected.default_version_sut_id}</dd>
        </div>
        <div>
          <dt className="text-muted-foreground">页面</dt>
          <dd className="font-medium text-foreground">
            {selected.page_ids.length > 0 ? selected.page_ids.join("、") : "—"}
          </dd>
        </div>
        <div>
          <dt className="text-muted-foreground">创建时间</dt>
          <dd className="font-medium text-foreground">{formatDateTime(selected.created_at)}</dd>
        </div>
      </dl>

      {selected.description.length > 0 ? (
        <p className="text-xs leading-relaxed text-foreground">{selected.description}</p>
      ) : null}

      <section aria-label="前置条件" className="space-y-1">
        <h3 className="text-xs font-semibold text-foreground">前置条件</h3>
        <p className="text-xs text-muted-foreground">{selected.preconditions || "—"}</p>
      </section>

      <section aria-label="步骤" className="space-y-1">
        <h3 className="text-xs font-semibold text-foreground">步骤</h3>
        <div className="overflow-x-auto">
          <table className="w-full border-collapse text-[11px]">
            <thead>
              <tr className="border-b border-border text-left text-muted-foreground">
                <th className="px-1.5 py-1 font-medium">序号</th>
                <th className="px-1.5 py-1 font-medium">动作</th>
                <th className="px-1.5 py-1 font-medium">预期</th>
                <th className="px-1.5 py-1 font-medium">目标 SUT</th>
                <th className="px-1.5 py-1 font-medium">页面</th>
                <th className="px-1.5 py-1 font-medium">API</th>
                <th className="px-1.5 py-1 font-medium">账号角色</th>
              </tr>
            </thead>
            <tbody>
              {selected.steps.map((step) => (
                <tr key={step.seq} className="border-b border-border/60 align-top">
                  <td className="px-1.5 py-1">{step.seq}</td>
                  <td className="px-1.5 py-1 text-foreground">{step.action}</td>
                  <td className="px-1.5 py-1 text-muted-foreground">{step.expected || "—"}</td>
                  <td className="px-1.5 py-1">
                    {step.target_version_sut_id ? `SUT ${step.target_version_sut_id}` : "继承默认"}
                  </td>
                  <td className="px-1.5 py-1">
                    {step.target_page_id ? `页面 ${step.target_page_id}` : "—"}
                  </td>
                  <td className="px-1.5 py-1">
                    {step.target_api_id ? `API ${step.target_api_id}` : "—"}
                  </td>
                  <td className="px-1.5 py-1">{step.account_role || "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>

      <section aria-label="断言" className="space-y-1">
        <h3 className="text-xs font-semibold text-foreground">断言</h3>
        {selected.assertions.length === 0 ? (
          <p className="text-xs text-muted-foreground">—</p>
        ) : (
          <ul className="space-y-1">
            {selected.assertions.map((assertion) => (
              <li
                key={`${assertion.step_seq}-${assertion.kind}-${assertion.target}-${assertion.expected}`}
                className="flex flex-wrap items-center gap-1.5 text-[11px]"
              >
                <span className="text-foreground">
                  第 {assertion.step_seq} 步 · {ASSERTION_KIND_LABEL[assertion.kind]} ·{" "}
                  {assertion.target}
                </span>
                <span className="text-muted-foreground">{assertion.expected}</span>
              </li>
            ))}
          </ul>
        )}
      </section>

      <CaseVisualAssertions
        versionId={visualConfigTarget.versionId}
        selected={visualConfigTarget.selected}
        previousCaseVersionId={visualConfigTarget.previousCaseVersionId}
      />

      <section aria-label="来源" className="space-y-1">
        <h3 className="text-xs font-semibold text-foreground">来源</h3>
        {selected.sources.length === 0 ? (
          <p className="text-xs text-muted-foreground">—</p>
        ) : (
          <ul className="flex flex-wrap gap-1.5">
            {selected.sources.map((source) => (
              <li
                key={`${source.kind}-${source.ref_id}-${source.path}-${source.line ?? ""}`}
                className="rounded-full bg-muted px-2 py-0.5 text-[10px] text-muted-foreground"
              >
                {source.kind} · {source.ref_id}
              </li>
            ))}
          </ul>
        )}
      </section>

      <section aria-label="版本历史" className="space-y-1">
        <h3 className="text-xs font-semibold text-foreground">版本历史</h3>
        <ul className="space-y-1">
          {detail.versions.map((version) => {
            const active =
              selectedVersionId !== undefined
                ? version.case_version_id === selectedVersionId
                : version.case_version_id === detail.current_version_id
            return (
              <li key={version.case_version_id}>
                <button
                  type="button"
                  aria-label={`查看版本 v${version.revision}`}
                  aria-pressed={active}
                  onClick={() => onSelectVersion(version.case_version_id)}
                  className={
                    active
                      ? "flex w-full items-center gap-2 rounded-md bg-accent/60 px-2 py-1 text-left text-[11px]"
                      : "flex w-full items-center gap-2 rounded-md px-2 py-1 text-left text-[11px] hover:bg-muted"
                  }
                >
                  <span className="text-foreground">
                    v{version.revision} · {CASE_VERSION_STATUS_LABEL[version.status]}
                  </span>
                  <span className="ml-auto text-[10px] text-muted-foreground">
                    {formatDateTime(version.created_at)}
                  </span>
                </button>
              </li>
            )
          })}
        </ul>
      </section>

      <section aria-label="代码锚点" className="soft-divider space-y-1 border-t border-border pt-3">
        <AnchorPanel
          versionId={versionId}
          subjectKind="case_version"
          subjectId={selected.case_version_id}
          title="用例代码锚点"
        />
      </section>
    </div>
  )
}

/**
 * Bottom full-width case detail: metadata, steps with per-step targets,
 * assertions, sources and version history. Confirmation is only offered for the
 * current draft version; execution results belong to P3 and are not shown.
 */
export function CaseDetailPanel({
  versionId,
  caseId,
  refreshToken = 0,
  onConfirm,
  onRequestChange,
  confirming = false,
}: CaseDetailPanelProps) {
  const [detail, setDetail] = useState<DesignCaseDetail | null>(null)
  const [selectedVersionId, setSelectedVersionId] = useState<string | undefined>(undefined)
  const [loading, setLoading] = useState(caseId !== null)
  const [error, setError] = useState<unknown>(null)
  const [reloadToken, setReloadToken] = useState(0)

  // Switching to another case resets the historical version selection.
  // biome-ignore lint/correctness/useExhaustiveDependencies: caseId is the reset trigger
  useEffect(() => {
    setSelectedVersionId(undefined)
  }, [caseId])

  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshToken/reloadToken refetch the detail after confirm/SSE
  useEffect(() => {
    let cancelled = false
    if (caseId === null) {
      setDetail(null)
      setError(null)
      setLoading(false)
      return () => {
        cancelled = true
      }
    }
    setLoading(true)
    setError(null)
    void getDesignCase(versionId, caseId, selectedVersionId).then(
      (next) => {
        if (cancelled) return
        setDetail(next)
        setLoading(false)
      },
      (cause: unknown) => {
        if (cancelled) return
        setError(cause)
        setLoading(false)
      },
    )
    return () => {
      cancelled = true
    }
  }, [versionId, caseId, selectedVersionId, refreshToken, reloadToken])

  const retry = useCallback(() => setReloadToken((token) => token + 1), [])

  return (
    <section data-testid="case-detail" aria-label="用例详情" className="surface-card p-4">
      {caseId === null ? (
        <p className="text-xs text-muted-foreground">选择用例查看详情</p>
      ) : loading && detail === null ? (
        <LoadingCard label="正在加载用例详情…" />
      ) : error !== null ? (
        <ErrorState error={error} title="用例详情加载失败" onRetry={retry} />
      ) : detail === null ? null : (
        <CaseDetailBody
          versionId={versionId}
          detail={detail}
          selectedVersionId={selectedVersionId}
          onSelectVersion={setSelectedVersionId}
          onConfirm={onConfirm}
          onRequestChange={onRequestChange}
          confirming={confirming}
        />
      )}
    </section>
  )
}
