"use client"

import { useEffect, useState } from "react"

import { Alert } from "@/components/ui/Alert"
import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { InlineError } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Input } from "@/components/ui/Input"
import { Select } from "@/components/ui/Select"
import { Textarea } from "@/components/ui/Textarea"

import { confirmRequirement, updateRequirement } from "./api"
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

  async function save(): Promise<void> {
    const nextErrors = validate({ title, description, acceptance })
    setErrors(nextErrors)
    setError(null)
    setNotice(null)
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
      setNotice(`已保存为草案 v${updated.revision}`)
    } catch (cause) {
      setError(cause)
    } finally {
      setSaving(false)
    }
  }

  async function confirm(): Promise<void> {
    setError(null)
    setNotice(null)
    setSaving(true)
    try {
      const result = await confirmRequirement(versionId, requirement.id)
      onUpdated(result.requirement)
      setNotice("已确认")
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
          <Alert tone="warning">该需求已澄清；如需修改，请先确认回退为草案。</Alert>
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
            onChange={(event) => setAcceptance(event.target.value)}
            className="min-h-20 text-xs"
          />
        </Field>

        <Field id="clarification-editor-priority" label="优先级">
          <Select
            id="clarification-editor-priority"
            data-testid="editor-priority"
            value={priority}
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
            onToggle={(id) => setSuts((currentValues) => toggleValue(currentValues, id))}
          />
          <TargetGroup
            title="目标页面"
            idPrefix="page"
            options={targetOptions.pages}
            values={pages}
            onToggle={(id) => setPages((currentValues) => toggleValue(currentValues, id))}
          />
          <TargetGroup
            title="目标 API"
            idPrefix="api"
            options={targetOptions.apis}
            values={apis}
            onToggle={(id) => setApis((currentValues) => toggleValue(currentValues, id))}
          />
        </fieldset>
      </div>

      <div className="mt-3 flex items-center gap-2">
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
        {clarified ? null : (
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
    </section>
  )
}

function TargetGroup({
  title,
  idPrefix,
  options,
  values,
  onToggle,
}: {
  title: string
  idPrefix: string
  options: TargetOptions["suts"]
  values: string[]
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
