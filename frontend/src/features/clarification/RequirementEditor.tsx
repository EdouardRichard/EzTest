"use client"

import { useEffect, useState } from "react"

import { Alert } from "@/components/ui/Alert"
import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { Dialog } from "@/components/ui/Dialog"
import { InlineError } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Input } from "@/components/ui/Input"
import { Select } from "@/components/ui/Select"
import { Textarea } from "@/components/ui/Textarea"
import { AnchorPanel } from "@/features/anchors/AnchorPanel"

import {
  confirmRequirement,
  reopenRequirement,
  rollbackRequirement,
  updateRequirement,
} from "./api"
import { REQUIREMENT_STATUS_LABEL, REQUIREMENT_STATUS_TONE } from "./labels"
import type { RequirementDetail, RequirementPriority, TargetOptions } from "./types"

const PRIORITIES: RequirementPriority[] = ["P0", "P1", "P2", "P3"]
const TITLE_LIMIT = 200
const TEXT_LIMIT = 20000

export type RequirementEditorProps = {
  versionId: string
  requirement: RequirementDetail
  /** Version-bound SUT/page/API ids the requirement may target. */
  targetOptions: TargetOptions
  /** Called after save/confirm with the new requirement detail. */
  onUpdated: (requirement: RequirementDetail) => void
  /**
   * Lifts success notices to the parent (C-06). `RequirementsTab` keys this
   * editor by `current_version_id`, so a local notice is lost when a rollback /
   * save remounts it; the parent then renders the notice above the keyed editor.
   * Without the callback (unit use) the notice stays local.
   */
  onNotice?: (notice: string | null) => void
}

type EditorErrors = {
  title?: string
  description?: string
  acceptance?: string
}

function validate(values: {
  title: string
  description: string
  acceptance: string
}): EditorErrors {
  const errors: EditorErrors = {}
  if (values.title.trim().length === 0) errors.title = "标题不能为空"
  else if (values.title.length > TITLE_LIMIT) errors.title = `标题不能超过 ${TITLE_LIMIT} 字`
  if (values.description.trim().length === 0) errors.description = "描述不能为空"
  else if (values.description.length > TEXT_LIMIT)
    errors.description = `描述不能超过 ${TEXT_LIMIT} 字`
  if (values.acceptance.trim().length === 0) errors.acceptance = "验收标准不能为空"
  else if (values.acceptance.length > TEXT_LIMIT)
    errors.acceptance = `验收标准不能超过 ${TEXT_LIMIT} 字`
  return errors
}

function toggleValue(values: string[], id: string): string[] {
  return values.includes(id) ? values.filter((value) => value !== id) : [...values, id]
}

/**
 * Requirement editor: title/description/acceptance/priority + target multi-select.
 * Save creates a new draft revision (PATCH); confirm freezes the current version.
 * A clarified requirement shows the rollback hint instead of the confirm action.
 */
export function RequirementEditor({
  versionId,
  requirement,
  targetOptions,
  onUpdated,
  onNotice,
}: RequirementEditorProps) {
  const current = requirement.current_version
  const [title, setTitle] = useState(current.title)
  const [description, setDescription] = useState(current.description)
  const [acceptance, setAcceptance] = useState(current.acceptance_criteria)
  const [priority, setPriority] = useState<RequirementPriority>(current.priority)
  const [suts, setSuts] = useState<string[]>(current.target_version_suts)
  const [pages, setPages] = useState<string[]>(current.target_pages)
  const [apis, setApis] = useState<string[]>(current.target_apis)
  const [errors, setErrors] = useState<EditorErrors>({})
  const [error, setError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [reopenOpen, setReopenOpen] = useState(false)
  const [reopenReason, setReopenReason] = useState("")
  const [reopening, setReopening] = useState(false)
  const [reopenError, setReopenError] = useState<unknown>(null)
  const [rollbackOpen, setRollbackOpen] = useState(false)
  const [rollbackReason, setRollbackReason] = useState("")
  const [rollingBack, setRollingBack] = useState(false)
  const [rollbackError, setRollbackError] = useState<unknown>(null)

  useEffect(() => {
    setTitle(current.title)
    setDescription(current.description)
    setAcceptance(current.acceptance_criteria)
    setPriority(current.priority)
    setSuts(current.target_version_suts)
    setPages(current.target_pages)
    setApis(current.target_apis)
    setErrors({})
    setError(null)
    setNotice(null)
  }, [current])

  const clarified = requirement.status === "clarified"
  const closed = requirement.status === "closed"
  const obsolete = requirement.status === "obsolete"
  // C-06: a clarified requirement is read-only here. Changing it must go
  // through the audited rollback route (new draft revision + event + audit),
  // never a plain PATCH that silently turns it back into a draft.
  const readOnly = closed || obsolete || clarified
  const reopenSubmitDisabled = reopening || reopenReason.trim().length === 0
  const rollbackSubmitDisabled = rollingBack || rollbackReason.trim().length === 0

  /** Reports a success notice locally or to the lifting parent when provided. */
  function notify(message: string | null): void {
    if (onNotice !== undefined) onNotice(message)
    else setNotice(message)
  }

  async function rollback(): Promise<void> {
    const reason = rollbackReason.trim()
    if (reason.length === 0) return
    setRollbackError(null)
    setRollingBack(true)
    try {
      const updated = await rollbackRequirement(versionId, requirement.id, { reason })
      onUpdated(updated)
      notify(`已回退为草案 v${updated.revision}`)
      setRollbackOpen(false)
      setRollbackReason("")
    } catch (cause) {
      setRollbackError(cause)
    } finally {
      setRollingBack(false)
    }
  }

  async function reopen(): Promise<void> {
    const reason = reopenReason.trim()
    if (reason.length === 0) return
    setReopenError(null)
    setReopening(true)
    try {
      const updated = await reopenRequirement(versionId, requirement.id, { reason })
      onUpdated(updated)
      notify("已重新打开为草案")
      setReopenOpen(false)
      setReopenReason("")
    } catch (cause) {
      setReopenError(cause)
    } finally {
      setReopening(false)
    }
  }

  async function save(): Promise<void> {
    const nextErrors = validate({ title, description, acceptance })
    setErrors(nextErrors)
    setError(null)
    notify(null)
    if (Object.keys(nextErrors).length > 0) return

    setSaving(true)
    try {
      const updated = await updateRequirement(versionId, requirement.id, {
        title: title.trim(),
        description: description.trim(),
        acceptance_criteria: acceptance.trim(),
        priority,
        constraints: current.constraints,
        targets: { version_suts: suts, pages, apis },
      })
      onUpdated(updated)
      notify(`已保存为草案 v${updated.revision}`)
    } catch (cause) {
      setError(cause)
    } finally {
      setSaving(false)
    }
  }

  async function confirm(): Promise<void> {
    setError(null)
    notify(null)
    setSaving(true)
    try {
      const result = await confirmRequirement(versionId, requirement.id)
      onUpdated(result.requirement)
      notify("已确认")
    } catch (cause) {
      setError(cause)
    } finally {
      setSaving(false)
    }
  }

  return (
    <section
      data-testid="requirement-editor"
      aria-label="需求编辑器"
      className="border-t border-border bg-card px-3 py-3"
    >
      <header className="flex flex-wrap items-center gap-1.5">
        <h3 className="text-xs font-semibold text-foreground">{requirement.title}</h3>
        <Badge tone={REQUIREMENT_STATUS_TONE[requirement.status]}>
          {REQUIREMENT_STATUS_LABEL[requirement.status]}
        </Badge>
        <span className="text-[10px] text-muted-foreground">v{requirement.revision}</span>
      </header>

      {clarified ? (
        <div data-testid="editor-rollback-hint" className="mt-2">
          <Alert tone="warning">
            该需求已澄清；编辑前需先回退为草案（会生成新 revision 并重新确认）。
          </Alert>
        </div>
      ) : null}

      {closed ? (
        <div data-testid="editor-closed-hint" className="mt-2">
          <Alert tone="warning">
            该需求已闭环；重新打开后会回到草案，需重新确认后才会再次进入设计与执行。
          </Alert>
        </div>
      ) : null}

      <div className="mt-2.5 space-y-2.5">
        <Field id="clarification-editor-title" label="标题" required error={errors.title}>
          <Input
            id="clarification-editor-title"
            data-testid="editor-title"
            value={title}
            maxLength={TITLE_LIMIT}
            aria-invalid={errors.title !== undefined}
            disabled={readOnly || saving}
            onChange={(event) => setTitle(event.target.value)}
          />
        </Field>

        <Field
          id="clarification-editor-description"
          label="描述"
          required
          error={errors.description}
        >
          <Textarea
            id="clarification-editor-description"
            data-testid="editor-description"
            rows={3}
            value={description}
            aria-invalid={errors.description !== undefined}
            disabled={readOnly || saving}
            onChange={(event) => setDescription(event.target.value)}
            className="min-h-20 text-xs"
          />
        </Field>

        <Field
          id="clarification-editor-acceptance"
          label="验收标准"
          required
          error={errors.acceptance}
        >
          <Textarea
            id="clarification-editor-acceptance"
            data-testid="editor-acceptance"
            rows={3}
            value={acceptance}
            aria-invalid={errors.acceptance !== undefined}
            disabled={readOnly || saving}
            onChange={(event) => setAcceptance(event.target.value)}
            className="min-h-20 text-xs"
          />
        </Field>

        <Field id="clarification-editor-priority" label="优先级">
          <Select
            id="clarification-editor-priority"
            data-testid="editor-priority"
            value={priority}
            disabled={readOnly || saving}
            onChange={(event) => setPriority(event.target.value as RequirementPriority)}
          >
            {PRIORITIES.map((value) => (
              <option key={value} value={value}>
                {value}
              </option>
            ))}
          </Select>
        </Field>

        <fieldset className="space-y-1.5">
          <legend className="text-xs font-medium text-foreground">目标范围</legend>
          <TargetGroup
            title="目标 SUT"
            idPrefix="sut"
            options={targetOptions.suts}
            values={suts}
            disabled={readOnly || saving}
            onToggle={(id) => setSuts((currentValues) => toggleValue(currentValues, id))}
          />
          <TargetGroup
            title="目标页面"
            idPrefix="page"
            options={targetOptions.pages}
            values={pages}
            disabled={readOnly || saving}
            onToggle={(id) => setPages((currentValues) => toggleValue(currentValues, id))}
          />
          <TargetGroup
            title="目标 API"
            idPrefix="api"
            options={targetOptions.apis}
            values={apis}
            disabled={readOnly || saving}
            onToggle={(id) => setApis((currentValues) => toggleValue(currentValues, id))}
          />
        </fieldset>
      </div>

      <div className="mt-3 flex items-center gap-2">
        {clarified ? (
          <Button
            size="sm"
            data-testid="editor-rollback"
            onClick={() => {
              setRollbackError(null)
              setRollbackReason("")
              setRollbackOpen(true)
            }}
          >
            回退为草案
          </Button>
        ) : readOnly ? (
          closed ? (
            <Button
              size="sm"
              data-testid="editor-reopen"
              onClick={() => {
                setReopenError(null)
                setReopenOpen(true)
              }}
            >
              重新打开需求
            </Button>
          ) : null
        ) : (
          <>
            <Button
              size="sm"
              variant="secondary"
              data-testid="editor-save"
              disabled={saving}
              onClick={() => {
                void save()
              }}
            >
              {saving ? "保存中…" : "保存"}
            </Button>
            <Button
              size="sm"
              data-testid="editor-confirm"
              disabled={saving}
              onClick={() => {
                void confirm()
              }}
            >
              确认已澄清
            </Button>
          </>
        )}
        {notice !== null ? (
          <p role="status" className="ml-auto text-[11px] text-success">
            {notice}
          </p>
        ) : null}
      </div>

      {error !== null ? (
        <div className="mt-1.5">
          <InlineError error={error} />
        </div>
      ) : null}

      <Dialog
        open={reopenOpen}
        onClose={() => {
          if (!reopening) setReopenOpen(false)
        }}
        title="重新打开需求"
        description="需求从已闭环回到草案；当前版本与确认历史保留，重开原因会写入审计。重新打开后需再次确认。"
        footer={
          <>
            <Button
              variant="secondary"
              size="sm"
              disabled={reopening}
              onClick={() => setReopenOpen(false)}
            >
              取消
            </Button>
            <Button
              size="sm"
              disabled={reopenSubmitDisabled}
              onClick={() => {
                void reopen()
              }}
            >
              {reopening ? "提交中…" : "重新打开需求"}
            </Button>
          </>
        }
      >
        <div className="space-y-3">
          <Field
            id="requirement-reopen-reason"
            label="重开原因"
            required
            hint="必填（1–2000 字）；会写入审计与 requirement.reopened 事件。"
          >
            <Textarea
              id="requirement-reopen-reason"
              value={reopenReason}
              maxLength={2000}
              disabled={reopening}
              placeholder="例如：缺陷修复后需要重新执行验收"
              onChange={(event) => setReopenReason(event.target.value)}
            />
          </Field>
          {reopenError !== null ? <InlineError error={reopenError} /> : null}
        </div>
      </Dialog>

      <Dialog
        open={rollbackOpen}
        onClose={() => {
          if (!rollingBack) setRollbackOpen(false)
        }}
        title="回退为草案"
        description="需求从已澄清回到新的草案 revision；当前版本与确认历史保留，回退原因会写入审计与 requirement.rolled_back 事件。回退后需重新确认。"
        footer={
          <>
            <Button
              variant="secondary"
              size="sm"
              disabled={rollingBack}
              onClick={() => setRollbackOpen(false)}
            >
              取消
            </Button>
            <Button
              size="sm"
              disabled={rollbackSubmitDisabled}
              onClick={() => {
                void rollback()
              }}
            >
              {rollingBack ? "提交中…" : "回退为草案"}
            </Button>
          </>
        }
      >
        <div className="space-y-3">
          <Field
            id="requirement-rollback-reason"
            label="回退原因"
            required
            hint="必填（1–2000 字）；会生成新的草案 revision 并写入审计。"
          >
            <Textarea
              id="requirement-rollback-reason"
              value={rollbackReason}
              maxLength={2000}
              disabled={rollingBack}
              placeholder="例如：验收口径变更，需要重新澄清"
              onChange={(event) => setRollbackReason(event.target.value)}
            />
          </Field>
          {rollbackError !== null ? <InlineError error={rollbackError} /> : null}
        </div>
      </Dialog>

      <div className="mt-3 border-t border-border pt-3">
        <AnchorPanel
          versionId={versionId}
          subjectKind="requirement"
          subjectId={requirement.id}
          title="需求代码锚点"
        />
      </div>
    </section>
  )
}

function TargetGroup({
  title,
  idPrefix,
  options,
  values,
  disabled = false,
  onToggle,
}: {
  title: string
  idPrefix: string
  options: TargetOptions["suts"]
  values: string[]
  disabled?: boolean
  onToggle: (id: string) => void
}) {
  if (options.length === 0) return null

  return (
    <div>
      <p className="text-[10px] text-muted-foreground">{title}</p>
      <div className="mt-1 flex flex-wrap gap-x-3 gap-y-1">
        {options.map((option) => (
          <label
            key={option.id}
            htmlFor={`clarification-${idPrefix}-${option.id}`}
            className="flex items-center gap-1 text-[11px] text-foreground"
          >
            <input
              id={`clarification-${idPrefix}-${option.id}`}
              data-testid={`editor-target-${idPrefix}-${option.id}`}
              type="checkbox"
              checked={values.includes(option.id)}
              disabled={disabled}
              onChange={() => onToggle(option.id)}
              className="size-3.5 accent-primary"
            />
            {option.label}
          </label>
        ))}
      </div>
    </div>
  )
}
