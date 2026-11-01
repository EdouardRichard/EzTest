"use client"

import { type FormEvent, useMemo, useState } from "react"

import { Button } from "@/components/ui/Button"
import { Dialog } from "@/components/ui/Dialog"
import { InlineError } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Input } from "@/components/ui/Input"
import { Select } from "@/components/ui/Select"
import { Textarea } from "@/components/ui/Textarea"

import { createKnowledge, updateKnowledge } from "./api"
import type {
  CreateKnowledgeInput,
  KnowledgeItemView,
  KnowledgeScope,
  UpdateKnowledgeInput,
} from "./types"

export type KnowledgeEditorProps = {
  mode: "create" | "edit"
  /** Required in edit mode: the item being edited. */
  item?: KnowledgeItemView | null
  onClose: () => void
  onSaved: (item: KnowledgeItemView) => void
}

type FieldErrors = {
  title?: string
  content?: string
  systemKey?: string
  tags?: string
  sourceVersion?: string
}

const MAX_TITLE = 200
const MAX_CONTENT = 20000
const MAX_TAGS = 20
const MAX_TAG = 40
const MAX_SYSTEM_KEY = 64

/** Splits comma-separated tag input (ASCII or full-width commas), trimming and deduping. */
function parseTags(raw: string): string[] {
  const tags: string[] = []
  const seen = new Set<string>()
  for (const part of raw.split(/[,，]/)) {
    const tag = part.trim()
    if (tag.length === 0 || seen.has(tag)) continue
    seen.add(tag)
    tags.push(tag)
  }
  return tags
}

function validate(
  mode: "create" | "edit",
  values: {
    title: string
    content: string
    scope: KnowledgeScope
    systemKey: string
    tagsText: string
    sourceVersionId: string
  },
): FieldErrors {
  const errors: FieldErrors = {}
  const title = values.title.trim()
  if (title.length < 1 || title.length > MAX_TITLE) {
    errors.title = `标题长度需在 1–${MAX_TITLE} 字之间`
  }
  const content = values.content.trim()
  if (content.length < 1 || content.length > MAX_CONTENT) {
    errors.content = `内容长度需在 1–${MAX_CONTENT} 字之间`
  }
  if (values.scope === "system") {
    const systemKey = values.systemKey.trim()
    if (systemKey.length === 0) {
      errors.systemKey = "系统范围必须填写系统标识（system_key）"
    } else if (systemKey.length > MAX_SYSTEM_KEY) {
      errors.systemKey = `系统标识最多 ${MAX_SYSTEM_KEY} 个字符`
    } else if (systemKey !== systemKey.toLowerCase()) {
      errors.systemKey = "系统标识必须为小写"
    } else if (/\s/.test(systemKey)) {
      errors.systemKey = "系统标识不能包含空白字符"
    }
  }
  const tags = parseTags(values.tagsText)
  if (tags.length > MAX_TAGS) {
    errors.tags = `标签最多 ${MAX_TAGS} 个`
  } else {
    const oversized = tags.find((tag) => tag.length > MAX_TAG)
    if (oversized !== undefined) errors.tags = `单个标签最多 ${MAX_TAG} 个字符`
  }
  if (mode === "create") {
    const sourceVersionId = values.sourceVersionId.trim()
    if (sourceVersionId.length > 0 && !/^[1-9]\d*$/.test(sourceVersionId)) {
      errors.sourceVersion = "来源版本 ID 必须为正整数"
    }
  }
  return errors
}

/**
 * Create/edit dialog for knowledge drafts (spec §10).
 *
 * A normal user can create a draft; editing is limited to draft/rejected items
 * and offered only to the author or an admin (the server enforces the same
 * rule). The form validates the frozen contract bounds before calling the API.
 */
export function KnowledgeEditor({ mode, item, onClose, onSaved }: KnowledgeEditorProps) {
  const [title, setTitle] = useState(item?.title ?? "")
  const [content, setContent] = useState(item?.content ?? "")
  const [scope, setScope] = useState<KnowledgeScope>(item?.scope ?? "global")
  const [systemKey, setSystemKey] = useState(item?.system_key ?? "")
  const [tagsText, setTagsText] = useState((item?.tags ?? []).join(", "))
  const [sourceVersionId, setSourceVersionId] = useState(item?.source_version_id ?? "")
  const [errors, setErrors] = useState<FieldErrors>({})
  const [submitting, setSubmitting] = useState(false)
  const [apiError, setApiError] = useState<unknown>(null)

  const editing = mode === "edit" && item !== null && item !== undefined
  const dialogTitle = editing ? "编辑知识" : "新建知识"
  const description = editing
    ? "仅草稿或已驳回条目可编辑；保存后追加一条新修订。"
    : "新知识以草稿创建，管理员审核通过后才会注入版本上下文。"

  const values = useMemo(
    () => ({ title, content, scope, systemKey, tagsText, sourceVersionId }),
    [title, content, scope, systemKey, tagsText, sourceVersionId],
  )

  async function submit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    if (submitting) return
    const nextErrors = validate(mode, values)
    setErrors(nextErrors)
    if (Object.keys(nextErrors).length > 0) return

    const tags = parseTags(tagsText)
    setSubmitting(true)
    setApiError(null)
    try {
      if (editing && item !== null && item !== undefined) {
        const body: UpdateKnowledgeInput = {
          title: title.trim(),
          content: content.trim(),
          tags,
          scope,
          system_key: scope === "system" ? systemKey.trim() : null,
        }
        onSaved(await updateKnowledge(item.id, body))
      } else {
        const body: CreateKnowledgeInput = {
          title: title.trim(),
          content: content.trim(),
          scope,
          system_key: scope === "system" ? systemKey.trim() : null,
          tags,
        }
        const sourceVersion = sourceVersionId.trim()
        if (sourceVersion.length > 0) body.source_version_id = sourceVersion
        onSaved(await createKnowledge(body))
      }
    } catch (cause) {
      setApiError(cause)
      setSubmitting(false)
    }
  }

  return (
    <Dialog
      open
      onClose={onClose}
      title={dialogTitle}
      description={description}
      className="max-w-2xl"
      footer={
        <>
          <Button
            variant="secondary"
            size="sm"
            data-testid="knowledge-editor-cancel"
            disabled={submitting}
            onClick={onClose}
          >
            取消
          </Button>
          <Button
            size="sm"
            type="submit"
            form="knowledge-editor-form"
            data-testid="knowledge-editor-submit"
            disabled={submitting}
          >
            {submitting ? "保存中…" : editing ? "保存修改" : "创建草稿"}
          </Button>
        </>
      }
    >
      <form
        id="knowledge-editor-form"
        data-testid="knowledge-editor"
        aria-label={dialogTitle}
        className="space-y-3"
        onSubmit={(event) => {
          void submit(event)
        }}
      >
        <Field id="knowledge-editor-title" label="标题" required error={errors.title}>
          <Input
            id="knowledge-editor-title"
            data-testid="knowledge-editor-title"
            value={title}
            maxLength={MAX_TITLE}
            disabled={submitting}
            placeholder="一句话描述这条知识"
            onChange={(event) => setTitle(event.target.value)}
          />
        </Field>

        <Field id="knowledge-editor-content" label="内容" required error={errors.content}>
          <Textarea
            id="knowledge-editor-content"
            data-testid="knowledge-editor-content"
            value={content}
            maxLength={MAX_CONTENT}
            disabled={submitting}
            placeholder="可复用的结论、约束或设计决策（最多 20000 字）"
            onChange={(event) => setContent(event.target.value)}
          />
        </Field>

        <div className="grid gap-3 sm:grid-cols-2">
          <Field id="knowledge-editor-scope" label="范围" required>
            <Select
              id="knowledge-editor-scope"
              data-testid="knowledge-editor-scope"
              value={scope}
              disabled={submitting}
              onChange={(event) => setScope(event.target.value as KnowledgeScope)}
            >
              <option value="global">全局（所有版本）</option>
              <option value="system">系统（按 SUT 族匹配）</option>
            </Select>
          </Field>
          <Field
            id="knowledge-editor-system-key"
            label="系统标识（system_key）"
            required={scope === "system"}
            error={errors.systemKey}
            hint={scope === "system" ? "小写、无空白，与 sut.family 一致。" : "全局范围无需填写。"}
          >
            <Input
              id="knowledge-editor-system-key"
              data-testid="knowledge-editor-system-key"
              value={systemKey}
              maxLength={MAX_SYSTEM_KEY}
              disabled={submitting || scope !== "system"}
              placeholder="order-center"
              onChange={(event) => setSystemKey(event.target.value)}
            />
          </Field>
        </div>

        <Field
          id="knowledge-editor-tags"
          label="标签"
          error={errors.tags}
          hint="用逗号分隔，最多 20 个、每个最多 40 字。"
        >
          <Input
            id="knowledge-editor-tags"
            data-testid="knowledge-editor-tags"
            value={tagsText}
            maxLength={MAX_TAGS * (MAX_TAG + 1)}
            disabled={submitting}
            placeholder="结算, 支付"
            onChange={(event) => setTagsText(event.target.value)}
          />
        </Field>

        {!editing ? (
          <Field
            id="knowledge-editor-source-version"
            label="来源版本 ID（可选）"
            error={errors.sourceVersion}
            hint="从某个版本晋升时填写；来源版本失效后详情会标注 source_stale。"
          >
            <Input
              id="knowledge-editor-source-version"
              data-testid="knowledge-editor-source-version"
              value={sourceVersionId}
              inputMode="numeric"
              disabled={submitting}
              placeholder="2001"
              onChange={(event) => setSourceVersionId(event.target.value)}
            />
          </Field>
        ) : null}

        {apiError !== null ? <InlineError error={apiError} /> : null}
        <p className="text-[11px] text-muted-foreground">
          内容修订会追加到 revisions，历史不可修改。
        </p>
      </form>
    </Dialog>
  )
}
