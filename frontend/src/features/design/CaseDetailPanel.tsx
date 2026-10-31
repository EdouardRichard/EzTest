"use client"

import { useCallback, useEffect, useState } from "react"

import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { formatDateTime } from "@/lib/format"

import { getDesignCase } from "./api"
import { CASE_VERSION_STATUS_LABEL } from "./nodeActions"
import type { DesignAssertionKind, DesignCaseDetail, DesignCaseVersionDetail } from "./types"

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
  detail,
  selectedVersionId,
  onSelectVersion,
  onConfirm,
  onRequestChange,
  confirming,
}: {
  detail: DesignCaseDetail
  selectedVersionId: string | undefined
  onSelectVersion: (caseVersionId: string) => void
  onConfirm: (caseId: string) => void
  onRequestChange: (title: string) => void
  confirming: boolean
}) {
  const selected = detail.selected
  const canConfirm = detail.confirmable && selected.status === "draft"

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
