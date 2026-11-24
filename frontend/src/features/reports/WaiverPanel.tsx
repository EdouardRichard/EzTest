"use client"

import { useCallback, useEffect, useState } from "react"

import { Alert } from "@/components/ui/Alert"
import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { Dialog } from "@/components/ui/Dialog"
import { InlineError } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Textarea } from "@/components/ui/Textarea"
import { useIsAdmin } from "@/features/auth/useIsAdmin"
import { listRequirements } from "@/features/clarification/api"
import type { Requirement } from "@/features/clarification/types"
import type { VersionStatus } from "@/features/versions/types"

import { waiveClose, waiveRequirement } from "./api"
import { CLOSURE_KIND_LABELS, type VersionClosureKind } from "./types"

export type WaiverPanelProps = {
  versionId: string
  versionStatus: VersionStatus
  closureKind?: VersionClosureKind | null
  unverified?: boolean | null
  /** Called after a successful waiver so the parent refreshes version/report. */
  onWaived?: () => void
}

type WaiveTarget = { kind: "version" } | { kind: "requirement"; id: string; title: string }

// Reason caps mirror the server contract per endpoint (final fix wave minor):
// version waive-close accepts 1..500 (VersionCloseReasonMax / WaiveCloseRequest),
// requirement waiver accepts 1..2000 (RequirementReasonMax / WaiveRequirementRequest).
// The dialog must not be stricter than the contract for either path.
const VERSION_REASON_MAX = 500
const REQUIREMENT_REASON_MAX = 2000

/**
 * Admin-only two-level waiver surface (SC-G11, Q7=A). A non-admin sees nothing
 * (the server still enforces 403 on both endpoints); an admin can waive-close
 * the version (overrides closure completeness, still requires execution settled)
 * and waive individual clarified requirements. Every waiver writes a reason +
 * operator audit server-side and surfaces the resulting unverified state.
 */
export function WaiverPanel({
  versionId,
  versionStatus,
  closureKind,
  unverified,
  onWaived,
}: WaiverPanelProps) {
  const isAdmin = useIsAdmin()
  const [requirements, setRequirements] = useState<Requirement[]>([])
  const [listError, setListError] = useState<unknown>(null)
  const [target, setTarget] = useState<WaiveTarget | null>(null)
  const [reason, setReason] = useState("")
  const [submitting, setSubmitting] = useState(false)
  const [actionError, setActionError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)

  const reloadRequirements = useCallback(() => {
    let cancelled = false
    listRequirements(versionId)
      .then((items) => {
        if (!cancelled) setRequirements(items)
      })
      .catch((cause: unknown) => {
        if (!cancelled) setListError(cause)
      })
    return () => {
      cancelled = true
    }
  }, [versionId])

  useEffect(() => {
    if (!isAdmin) return undefined
    return reloadRequirements()
  }, [isAdmin, reloadRequirements])

  if (!isAdmin) return null

  const openVersion = (): void => {
    setTarget({ kind: "version" })
    setReason("")
    setActionError(null)
    setNotice(null)
  }
  const openRequirement = (requirement: Requirement): void => {
    setTarget({ kind: "requirement", id: requirement.id, title: requirement.title })
    setReason("")
    setActionError(null)
    setNotice(null)
  }
  const closeDialog = (): void => {
    if (!submitting) setTarget(null)
  }

  const submit = async (): Promise<void> => {
    if (target === null || submitting) return
    const cap = target.kind === "requirement" ? REQUIREMENT_REASON_MAX : VERSION_REASON_MAX
    const trimmed = reason.trim()
    if (trimmed.length === 0) {
      setActionError(new Error(`豁免理由必填（1..${cap} 字）。`))
      return
    }
    setSubmitting(true)
    setActionError(null)
    try {
      if (target.kind === "version") {
        const result = await waiveClose(versionId, { reason: trimmed })
        setNotice(
          `已豁免关闭版本（closure_kind=${result.closure_kind}，unverified=${result.unverified ? "是" : "否"}），报告修订 r${result.report_revision} 已同步发布。`,
        )
      } else {
        await waiveRequirement(versionId, target.id, { reason: trimmed })
        setNotice(`已豁免需求 #${target.id}，确定性闭环将据此关闭该需求。`)
        reloadRequirements()
      }
      setTarget(null)
      setReason("")
      onWaived?.()
    } catch (cause) {
      setActionError(cause)
    } finally {
      setSubmitting(false)
    }
  }

  const confirmLabel = target?.kind === "requirement" ? "确认豁免需求" : "确认豁免关闭"
  const dialogTitle =
    target?.kind === "requirement" ? `豁免需求：${target.title}` : "豁免关闭版本（waive-close）"
  const reasonMax = target?.kind === "requirement" ? REQUIREMENT_REASON_MAX : VERSION_REASON_MAX

  return (
    <section data-testid="waiver-panel" aria-label="管理员豁免操作" className="space-y-3">
      <header className="flex flex-wrap items-center gap-2">
        <h3 className="text-xs font-semibold text-foreground">管理员豁免操作</h3>
        {closureKind != null ? (
          <Badge tone={closureKind === "waived" ? "warning" : "neutral"}>
            {CLOSURE_KIND_LABELS[closureKind]}
          </Badge>
        ) : null}
        {unverified === true ? <Badge tone="warning">未验证</Badge> : null}
      </header>
      <p className="text-[11px] text-muted-foreground">
        豁免会 override 闭环完整性检查（仍要求执行已结算），关闭后版本标记为未验证（unverified），
        并记录理由与操作者审计。仅管理员可操作；普通用户可查看豁免状态。
      </p>

      <div>
        <Button
          size="sm"
          variant="secondary"
          data-testid="waive-close-button"
          disabled={submitting}
          onClick={openVersion}
        >
          豁免关闭版本（waive-close）
        </Button>
        {versionStatus === "completed" && closureKind !== "waived" ? (
          <span className="ml-2 text-[11px] text-muted-foreground">
            版本已正常关闭；豁免关闭将被服务端拒绝（409）。
          </span>
        ) : null}
      </div>

      {notice !== null ? (
        <Alert tone="success" title="豁免已记录">
          {notice}
        </Alert>
      ) : null}
      {actionError !== null ? (
        <div data-testid="waiver-error">
          <InlineError error={actionError} />
        </div>
      ) : null}

      <div className="space-y-1.5 border-t border-border pt-2">
        <h4 className="text-[11px] font-semibold text-foreground">需求级豁免</h4>
        {listError !== null ? (
          <InlineError error={listError} />
        ) : requirements.length === 0 ? (
          <p className="text-[11px] text-muted-foreground">暂无需求。</p>
        ) : (
          <ul aria-label="需求豁免列表" className="space-y-1">
            {requirements.map((requirement) => (
              <li
                key={requirement.id}
                className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-card px-2 py-1.5 text-[11px]"
              >
                <span className="text-foreground">
                  #{requirement.id} {requirement.title}
                </span>
                <Badge tone="neutral">{requirement.status}</Badge>
                {requirement.waived === true ? (
                  <Badge tone="warning" data-testid={`requirement-waived-${requirement.id}`}>
                    已豁免
                  </Badge>
                ) : requirement.status === "clarified" ? (
                  <Button
                    size="sm"
                    variant="secondary"
                    className="ml-auto"
                    data-testid={`waive-requirement-${requirement.id}`}
                    disabled={submitting}
                    onClick={() => openRequirement(requirement)}
                  >
                    豁免该需求
                  </Button>
                ) : null}
              </li>
            ))}
          </ul>
        )}
      </div>

      <Dialog
        open={target !== null}
        onClose={closeDialog}
        title={dialogTitle}
        description={`理由会写入审计与报告豁免说明；最多 ${reasonMax} 字。`}
        footer={
          <>
            <Button variant="secondary" size="sm" disabled={submitting} onClick={closeDialog}>
              取消
            </Button>
            <Button
              size="sm"
              disabled={submitting}
              onClick={() => {
                void submit()
              }}
            >
              {submitting ? "提交中…" : confirmLabel}
            </Button>
          </>
        }
      >
        <Field id="waiver-reason" label="豁免理由（必填）" required>
          <Textarea
            id="waiver-reason"
            value={reason}
            maxLength={reasonMax}
            disabled={submitting}
            placeholder="例如：剩余风险已人工评估，未验证 AC 范围可接受"
            onChange={(event) => setReason(event.target.value)}
          />
        </Field>
      </Dialog>
    </section>
  )
}
