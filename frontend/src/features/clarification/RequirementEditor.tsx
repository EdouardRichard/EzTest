"use client"

import { useEffect, useRef, useState } from "react"

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
import { precheckConfirmation, useValidationRules } from "./requirement-precheck"
import type { RequirementDetail, RequirementPriority } from "./types"

const PRIORITIES: RequirementPriority[] = ["P0", "P1", "P2", "P3"]
// Length caps mirror the backend draft contract (version/domain): title 200,
// each user-story part 500, each given/when/then 2000. The server stays the
// authority; these only drive input maxLength and the client-side required check.
const TITLE_LIMIT = 200
const STORY_PART_LIMIT = 500
const AC_PART_LIMIT = 2000

/**
 * One editable acceptance criterion. `id` is the server-assigned "AC-<n>" shown
 * read-only (empty for a row the user just added); `key` is a local React key
 * that is never sent. The save body drops both and sends only {given,when,then}
 * (requirement_dto.go acceptanceCriterionInput), so a client can neither pick an
 * id nor resurrect a deleted one.
 */
type EditableCriterion = {
  key: number
  id: string
  given: string
  when: string
  then: string
}

type CriterionField = "given" | "when" | "then"

export type RequirementEditorProps = {
  versionId: string
  requirement: RequirementDetail
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
  as_a?: string
  i_want?: string
  so_that?: string
  sut_description?: string
  acceptance_criteria?: string
}

/** Splits a comma/newline-separated role hint into a list; the server dedups. */
function parseRoles(value: string): string[] {
  return value
    .split(/[,\n]/)
    .map((role) => role.trim())
    .filter((role) => role.length > 0)
}

/**
 * Required-field check mirroring the backend draft contract (ValidateDraft):
 * title, the three user-story parts, sut_description and at least one complete
 * acceptance criterion. Completeness/clarity (placeholder terms, min chars) is
 * the server-side confirmation gate owned by subproject E, not checked here.
 */
function validate(values: {
  title: string
  asA: string
  iWant: string
  soThat: string
  sutDescription: string
  criteria: EditableCriterion[]
}): EditorErrors {
  const errors: EditorErrors = {}
  if (values.title.trim().length === 0) errors.title = "标题不能为空"
  else if (values.title.length > TITLE_LIMIT) errors.title = `标题不能超过 ${TITLE_LIMIT} 字`
  if (values.asA.trim().length === 0) errors.as_a = "角色（as_a）不能为空"
  if (values.iWant.trim().length === 0) errors.i_want = "目标（i_want）不能为空"
  if (values.soThat.trim().length === 0) errors.so_that = "价值（so_that）不能为空"
  if (values.sutDescription.trim().length === 0) errors.sut_description = "被测系统描述不能为空"
  if (values.criteria.length === 0) {
    errors.acceptance_criteria = "至少需要一条验收标准"
  } else if (
    values.criteria.some(
      (criterion) =>
        criterion.given.trim().length === 0 ||
        criterion.when.trim().length === 0 ||
        criterion.then.trim().length === 0,
    )
  ) {
    errors.acceptance_criteria = "每条验收标准的 Given / When / Then 都不能为空"
  }
  return errors
}

/**
 * V1 requirement editor (natural-language contract, v0.2.4-B): title/priority,
 * the three-part user story, rules, a structured given/when/then acceptance
 * editor (server-assigned ids shown read-only), sut_description, scope_out and
 * account_roles_hint. Save creates a new draft revision (PATCH) whose acceptance
 * criteria carry no id; confirm freezes the current version. A clarified
 * requirement is read-only here and changes only through the audited rollback.
 */
export function RequirementEditor({
  versionId,
  requirement,
  onUpdated,
  onNotice,
}: RequirementEditorProps) {
  const current = requirement.current_version
  const [title, setTitle] = useState(current.title)
  const [asA, setAsA] = useState(current.user_story.as_a)
  const [iWant, setIWant] = useState(current.user_story.i_want)
  const [soThat, setSoThat] = useState(current.user_story.so_that)
  const [rules, setRules] = useState(current.rules)
  const [criteria, setCriteria] = useState<EditableCriterion[]>(() =>
    current.acceptance_criteria.map((criterion, index) => ({
      key: index,
      id: criterion.id,
      given: criterion.given,
      when: criterion.when,
      then: criterion.then,
    })),
  )
  const [sutDescription, setSutDescription] = useState(current.sut_description)
  const [scopeOut, setScopeOut] = useState(current.scope_out)
  const [accountRoles, setAccountRoles] = useState(current.account_roles_hint.join(", "))
  const [priority, setPriority] = useState<RequirementPriority>(current.priority)
  // Local React keys for added rows start above the initial indices so a key is
  // never reused after a middle row is removed.
  const nextKeyRef = useRef(current.acceptance_criteria.length)
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
  // Settings-driven confirmation rules (null when unreadable, e.g. non-admin):
  // the client pre-check is a convenience; the server stays authoritative.
  // `invalidShape` is non-null only when a successful read yielded a malformed
  // setting value — that skipped pre-check is surfaced (H-F10 #44).
  const validation = useValidationRules()
  const validationRules = validation.rules

  useEffect(() => {
    setTitle(current.title)
    setAsA(current.user_story.as_a)
    setIWant(current.user_story.i_want)
    setSoThat(current.user_story.so_that)
    setRules(current.rules)
    setCriteria(
      current.acceptance_criteria.map((criterion, index) => ({
        key: index,
        id: criterion.id,
        given: criterion.given,
        when: criterion.when,
        then: criterion.then,
      })),
    )
    nextKeyRef.current = current.acceptance_criteria.length
    setSutDescription(current.sut_description)
    setScopeOut(current.scope_out)
    setAccountRoles(current.account_roles_hint.join(", "))
    setPriority(current.priority)
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
  const fieldsDisabled = readOnly || saving
  const reopenSubmitDisabled = reopening || reopenReason.trim().length === 0
  const rollbackSubmitDisabled = rollingBack || rollbackReason.trim().length === 0

  /** Reports a success notice locally or to the lifting parent when provided. */
  function notify(message: string | null): void {
    if (onNotice !== undefined) onNotice(message)
    else setNotice(message)
  }

  function updateCriterion(key: number, field: CriterionField, value: string): void {
    setCriteria((rows) => rows.map((row) => (row.key === key ? { ...row, [field]: value } : row)))
  }

  function addCriterion(): void {
    setCriteria((rows) => [
      ...rows,
      { key: nextKeyRef.current++, id: "", given: "", when: "", then: "" },
    ])
  }

  function removeCriterion(key: number): void {
    setCriteria((rows) => rows.filter((row) => row.key !== key))
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
    const nextErrors = validate({ title, asA, iWant, soThat, sutDescription, criteria })
    setErrors(nextErrors)
    setError(null)
    notify(null)
    if (Object.keys(nextErrors).length > 0) return

    setSaving(true)
    try {
      const updated = await updateRequirement(versionId, requirement.id, {
        title: title.trim(),
        user_story: { as_a: asA.trim(), i_want: iWant.trim(), so_that: soThat.trim() },
        rules: rules.trim(),
        // Write DTO is {given,when,then} only: the server-assigned id and the
        // local React key are dropped, never submitted (requirement_dto.go:148).
        // All prose is trimmed to match validate()'s trimmed checks; the server
        // stays authoritative for canonicalization (AC inner-whitespace collapse,
        // account-role dedup).
        acceptance_criteria: criteria.map(({ given, when, then }) => ({
          given: given.trim(),
          when: when.trim(),
          then: then.trim(),
        })),
        priority,
        constraints: current.constraints,
        sut_description: sutDescription.trim(),
        scope_out: scopeOut.trim(),
        account_roles_hint: parseRoles(accountRoles),
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
    // Settings-driven client pre-check mirrors the server confirmation gate;
    // when the rules are unreadable (non-admin) it is skipped and the server
    // remains the authoritative gate.
    if (validationRules !== null) {
      const precheckErrors = precheckConfirmation(
        { title, asA, iWant, soThat, sutDescription, criteria },
        validationRules,
      )
      if (Object.keys(precheckErrors).length > 0) {
        setErrors(precheckErrors)
        return
      }
    }
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
            disabled={fieldsDisabled}
            onChange={(event) => setTitle(event.target.value)}
          />
        </Field>

        <fieldset className="space-y-2 rounded-lg border border-border px-2.5 py-2">
          <legend className="px-1 text-[11px] font-semibold text-foreground">用户故事</legend>
          <Field id="clarification-editor-as-a" label="作为（as_a）" required error={errors.as_a}>
            <Input
              id="clarification-editor-as-a"
              data-testid="editor-story-as-a"
              value={asA}
              maxLength={STORY_PART_LIMIT}
              aria-invalid={errors.as_a !== undefined}
              disabled={fieldsDisabled}
              placeholder="例如：买家"
              onChange={(event) => setAsA(event.target.value)}
            />
          </Field>
          <Field
            id="clarification-editor-i-want"
            label="我希望（i_want）"
            required
            error={errors.i_want}
          >
            <Textarea
              id="clarification-editor-i-want"
              data-testid="editor-story-i-want"
              rows={2}
              value={iWant}
              maxLength={STORY_PART_LIMIT}
              aria-invalid={errors.i_want !== undefined}
              disabled={fieldsDisabled}
              placeholder="例如：退款审核通过后 T+1 个工作日原路退回"
              onChange={(event) => setIWant(event.target.value)}
              className="min-h-14 text-xs"
            />
          </Field>
          <Field
            id="clarification-editor-so-that"
            label="以便（so_that）"
            required
            error={errors.so_that}
          >
            <Textarea
              id="clarification-editor-so-that"
              data-testid="editor-story-so-that"
              rows={2}
              value={soThat}
              maxLength={STORY_PART_LIMIT}
              aria-invalid={errors.so_that !== undefined}
              disabled={fieldsDisabled}
              placeholder="例如：我能及时收到退款"
              onChange={(event) => setSoThat(event.target.value)}
              className="min-h-14 text-xs"
            />
          </Field>
        </fieldset>

        <Field id="clarification-editor-rules" label="业务规则" hint="补充约束、边界与例外；可选。">
          <Textarea
            id="clarification-editor-rules"
            data-testid="editor-rules"
            rows={3}
            value={rules}
            disabled={fieldsDisabled}
            onChange={(event) => setRules(event.target.value)}
            className="min-h-20 text-xs"
          />
        </Field>

        <section aria-label="验收标准" className="space-y-2">
          <div className="flex items-center justify-between">
            <h4 className="text-xs font-medium text-foreground">
              验收标准 <span className="text-destructive">*</span>
            </h4>
            <Button
              size="sm"
              variant="secondary"
              data-testid="editor-ac-add"
              disabled={fieldsDisabled}
              onClick={addCriterion}
            >
              添加验收标准
            </Button>
          </div>
          {errors.acceptance_criteria !== undefined ? (
            <p role="alert" className="text-xs text-destructive">
              {errors.acceptance_criteria}
            </p>
          ) : null}
          {criteria.length === 0 ? (
            <p className="text-[11px] text-muted-foreground">
              暂无验收标准；点击「添加验收标准」新增一条 Given / When / Then。
            </p>
          ) : (
            <ul className="space-y-2">
              {criteria.map((criterion, index) => (
                <li
                  key={criterion.key}
                  data-testid={`editor-ac-row-${index}`}
                  className="space-y-1.5 rounded-lg border border-border px-2.5 py-2"
                >
                  <div className="flex items-center justify-between">
                    <span
                      data-testid={`editor-ac-${index}-id`}
                      className="text-[10px] font-medium text-muted-foreground"
                    >
                      {criterion.id === "" ? "新条目（保存后分配 id）" : criterion.id}
                    </span>
                    <Button
                      size="sm"
                      variant="ghost"
                      data-testid={`editor-ac-remove-${index}`}
                      disabled={fieldsDisabled}
                      onClick={() => removeCriterion(criterion.key)}
                    >
                      删除
                    </Button>
                  </div>
                  <Field id={`clarification-ac-${index}-given`} label="Given（前提）" required>
                    <Textarea
                      id={`clarification-ac-${index}-given`}
                      data-testid={`editor-ac-${index}-given`}
                      rows={2}
                      value={criterion.given}
                      maxLength={AC_PART_LIMIT}
                      disabled={fieldsDisabled}
                      onChange={(event) =>
                        updateCriterion(criterion.key, "given", event.target.value)
                      }
                      className="min-h-12 text-xs"
                    />
                  </Field>
                  <Field id={`clarification-ac-${index}-when`} label="When（动作）" required>
                    <Textarea
                      id={`clarification-ac-${index}-when`}
                      data-testid={`editor-ac-${index}-when`}
                      rows={2}
                      value={criterion.when}
                      maxLength={AC_PART_LIMIT}
                      disabled={fieldsDisabled}
                      onChange={(event) =>
                        updateCriterion(criterion.key, "when", event.target.value)
                      }
                      className="min-h-12 text-xs"
                    />
                  </Field>
                  <Field id={`clarification-ac-${index}-then`} label="Then（结果）" required>
                    <Textarea
                      id={`clarification-ac-${index}-then`}
                      data-testid={`editor-ac-${index}-then`}
                      rows={2}
                      value={criterion.then}
                      maxLength={AC_PART_LIMIT}
                      disabled={fieldsDisabled}
                      onChange={(event) =>
                        updateCriterion(criterion.key, "then", event.target.value)
                      }
                      className="min-h-12 text-xs"
                    />
                  </Field>
                </li>
              ))}
            </ul>
          )}
        </section>

        <Field
          id="clarification-editor-sut-description"
          label="被测系统描述"
          required
          error={errors.sut_description}
          hint="用自然语言描述本需求涉及的系统与链路。"
        >
          <Textarea
            id="clarification-editor-sut-description"
            data-testid="editor-sut-description"
            rows={3}
            value={sutDescription}
            aria-invalid={errors.sut_description !== undefined}
            disabled={fieldsDisabled}
            onChange={(event) => setSutDescription(event.target.value)}
            className="min-h-20 text-xs"
          />
        </Field>

        <Field
          id="clarification-editor-scope-out"
          label="范围外（scope_out）"
          hint="明确不在本需求范围内的内容；可选。"
        >
          <Textarea
            id="clarification-editor-scope-out"
            data-testid="editor-scope-out"
            rows={2}
            value={scopeOut}
            disabled={fieldsDisabled}
            onChange={(event) => setScopeOut(event.target.value)}
            className="min-h-14 text-xs"
          />
        </Field>

        <Field
          id="clarification-editor-account-roles"
          label="账号角色提示"
          hint="多个角色用逗号分隔；服务端按首次出现顺序去重。可选。"
        >
          <Input
            id="clarification-editor-account-roles"
            data-testid="editor-account-roles"
            value={accountRoles}
            disabled={fieldsDisabled}
            placeholder="例如：buyer, support"
            onChange={(event) => setAccountRoles(event.target.value)}
          />
        </Field>

        <Field id="clarification-editor-priority" label="优先级">
          <Select
            id="clarification-editor-priority"
            data-testid="editor-priority"
            value={priority}
            disabled={fieldsDisabled}
            onChange={(event) => setPriority(event.target.value as RequirementPriority)}
          >
            {PRIORITIES.map((value) => (
              <option key={value} value={value}>
                {value}
              </option>
            ))}
          </Select>
        </Field>
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
        {validationRules !== null && !readOnly ? (
          <p
            data-testid="editor-confirm-rules"
            className="w-full text-[10px] text-muted-foreground"
          >
            确认门槛（服务端权威）：占位词整值匹配（
            {validationRules.placeholderTerms.join("、") || "无"}
            ）；被测系统描述 ≥ {validationRules.minDescriptionChars} 字。
          </p>
        ) : null}
        {validation.invalidShape !== null && !readOnly ? (
          <p
            data-testid="editor-confirm-rules-invalid"
            role="status"
            className="w-full text-[10px] text-warning"
          >
            本地预检配置异常，已跳过增量判断；确认时仍以服务端校验为准。
          </p>
        ) : null}
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
