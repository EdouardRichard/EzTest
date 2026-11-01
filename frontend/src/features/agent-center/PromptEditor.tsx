"use client"

import { useEffect, useRef, useState } from "react"

import { Button } from "@/components/ui/Button"
import { cn } from "@/components/ui/cn"
import { Dialog } from "@/components/ui/Dialog"
import { InlineError } from "@/components/ui/ErrorState"
import { Input } from "@/components/ui/Input"
import { ApiError } from "@/lib/api-client"

import { previewAgentPrompt, resetAgentPrompt, saveAgentPrompt } from "./api"
import { PromptVersionList } from "./PromptVersionList"
import { PROMPT_MAX_RUNES, type ProfileDetail, type PromptRevision, promptRuneCount } from "./types"

export type PromptEditorProps = {
  detail: ProfileDetail
  /** Called after a successful save/reset/restore so the parent reloads. */
  onMutated: () => void
}

/**
 * Admin prompt editor: current body, variable chips, preview, save-as-new-
 * version and append-only version history. Saving affects the next run only;
 * the in-flight run keeps its snapshot.
 */
export function PromptEditor({ detail, onMutated }: PromptEditorProps) {
  const key = detail.profile.key
  const [text, setText] = useState(detail.profile.system_prompt)
  const [note, setNote] = useState("")
  const [saving, setSaving] = useState(false)
  const [resetting, setResetting] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [message, setMessage] = useState("")
  const [previewing, setPreviewing] = useState(false)
  const [previewOpen, setPreviewOpen] = useState(false)
  const [previewRendered, setPreviewRendered] = useState("")
  const [previewError, setPreviewError] = useState<unknown>(null)
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  useEffect(() => {
    setText(detail.profile.system_prompt)
    setNote("")
  }, [detail.profile.system_prompt])

  const marker = `[profile:${key}]`
  const hasMarker = text.includes(marker)
  const runeCount = promptRuneCount(text)

  function insertVariable(name: string): void {
    const token = `{{.${name}}}`
    const textarea = textareaRef.current
    const start = textarea?.selectionStart ?? text.length
    const end = textarea?.selectionEnd ?? start
    const next = `${text.slice(0, start)}${token}${text.slice(end)}`
    setText(next)
    requestAnimationFrame(() => {
      const element = textareaRef.current
      if (element === null) return
      const cursor = start + token.length
      element.focus()
      element.setSelectionRange(cursor, cursor)
    })
  }

  async function save(): Promise<void> {
    setSaving(true)
    setError(null)
    setMessage("")
    try {
      const mutation = await saveAgentPrompt(key, {
        system_prompt: text,
        note: note.trim().length > 0 ? note.trim() : undefined,
        expected_version: detail.prompt_version,
      })
      setMessage(
        mutation.changed
          ? `已保存版本 v${mutation.version}，下一次 run 生效。`
          : "内容未变化，未创建新版本。",
      )
      setNote("")
      onMutated()
    } catch (cause) {
      setError(cause)
    } finally {
      setSaving(false)
    }
  }

  async function reset(): Promise<void> {
    setResetting(true)
    setError(null)
    setMessage("")
    try {
      const mutation = await resetAgentPrompt(key)
      setMessage(`已恢复默认提示词（v${mutation.version}），下一次 run 生效。`)
      onMutated()
    } catch (cause) {
      setError(cause)
    } finally {
      setResetting(false)
    }
  }

  async function preview(): Promise<void> {
    setPreviewing(true)
    setPreviewError(null)
    try {
      const result = await previewAgentPrompt(key, { system_prompt: text })
      setPreviewRendered(result.rendered)
      setPreviewOpen(true)
    } catch (cause) {
      setPreviewError(cause)
    } finally {
      setPreviewing(false)
    }
  }

  function loadRevision(revision: PromptRevision): void {
    setText(revision.system_prompt)
    setMessage(`已载入版本 v${revision.version}（未保存）。`)
    textareaRef.current?.focus()
  }

  useEffect(() => {
    if (previewError === null) return
    setPreviewOpen(true)
  }, [previewError])

  return (
    <section data-testid="agent-prompt-editor" className="space-y-3">
      <header className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h3 className="text-sm font-semibold text-foreground">提示词</h3>
          <p className="mt-0.5 text-[11px] text-muted-foreground">
            必须是 Go text/template，且保留 <code>{marker}</code> 标记；变量由服务端运行期生成。
          </p>
        </div>
        <span className="text-[11px] text-muted-foreground">
          当前版本 <span className="font-mono">v{detail.prompt_version}</span>
        </span>
      </header>

      <p data-testid="agent-prompt-save-hint" className="text-[11px] text-muted-foreground">
        保存后<strong className="text-foreground">下一次 run 生效</strong>
        ；正在运行的 run 使用启动时的快照，不受影响。
      </p>

      <fieldset className="flex flex-wrap gap-1.5">
        <legend className="sr-only">提示词变量</legend>
        {detail.variables.map((variable) => (
          <button
            key={variable.name}
            type="button"
            data-testid={`agent-prompt-variable-${variable.name}`}
            title={`${variable.description}（示例：${variable.example}）`}
            onClick={() => insertVariable(variable.name)}
            className="inline-flex items-center rounded-full border border-border bg-muted px-2 py-0.5 font-mono text-[11px] text-foreground transition-colors hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {`{{.${variable.name}}}`}
          </button>
        ))}
      </fieldset>

      <textarea
        ref={textareaRef}
        data-testid="agent-prompt-textarea"
        aria-label="系统提示词"
        value={text}
        onChange={(event) => setText(event.target.value)}
        spellCheck={false}
        className={cn(PROMPT_TEXTAREA_CLASSES, "min-h-56 font-mono text-xs leading-relaxed")}
      />

      <div className="flex flex-wrap items-center justify-between gap-2 text-[11px] text-muted-foreground">
        <span data-testid="agent-prompt-counter">
          {runeCount} / {PROMPT_MAX_RUNES}
        </span>
        {!hasMarker ? (
          <span data-testid="agent-prompt-marker-warning" className="text-warning">
            缺少标记 <code>{marker}</code>，保存会被服务端拒绝。
          </span>
        ) : (
          <span className="text-success">已包含 profile 标记</span>
        )}
      </div>

      <div className="flex flex-wrap items-end gap-2">
        <label
          htmlFor="agent-prompt-note"
          className="flex min-w-56 flex-1 flex-col gap-1 text-[11px] text-muted-foreground"
        >
          版本备注（可选）
          <Input
            id="agent-prompt-note"
            data-testid="agent-prompt-note"
            value={note}
            maxLength={200}
            placeholder="例如：收紧输出约束"
            onChange={(event) => setNote(event.target.value)}
            className="h-8 text-xs"
          />
        </label>
        <Button
          data-testid="agent-prompt-preview-button"
          variant="secondary"
          size="sm"
          disabled={previewing}
          onClick={() => void preview()}
        >
          {previewing ? "预览中…" : "预览渲染"}
        </Button>
        <Button
          data-testid="agent-prompt-reset"
          variant="secondary"
          size="sm"
          disabled={resetting || saving}
          onClick={() => void reset()}
        >
          {resetting ? "恢复中…" : "恢复默认"}
        </Button>
        <Button
          data-testid="agent-prompt-save"
          size="sm"
          disabled={saving || !hasMarker || runeCount > PROMPT_MAX_RUNES}
          onClick={() => void save()}
        >
          {saving ? "保存中…" : "保存新版本"}
        </Button>
      </div>

      {error !== null ? (
        <p data-testid="agent-prompt-message" role="alert" className="text-xs text-destructive">
          {describePromptError(error)}
        </p>
      ) : message.length > 0 ? (
        <p data-testid="agent-prompt-message" className="text-xs text-success">
          {message}
        </p>
      ) : null}

      {previewError !== null ? <InlineError error={previewError} /> : null}

      <PromptVersionList
        profileKey={key}
        currentPrompt={detail.profile.system_prompt}
        onLoad={loadRevision}
        onRestored={onMutated}
      />

      <Dialog
        open={previewOpen}
        onClose={() => {
          setPreviewOpen(false)
          setPreviewError(null)
        }}
        title="渲染预览"
        description="使用变量示例值渲染；仅预览，不保存、不执行工具。"
        className="max-w-2xl"
      >
        {previewError !== null ? (
          <InlineError error={previewError} />
        ) : (
          <pre
            data-testid="agent-prompt-preview-output"
            className="max-h-[60vh] overflow-auto whitespace-pre-wrap break-words rounded-md border border-border bg-muted/20 p-3 font-mono text-xs"
          >
            {previewRendered}
          </pre>
        )}
      </Dialog>
    </section>
  )
}

const PROMPT_TEXTAREA_CLASSES =
  "min-h-24 w-full resize-y rounded-md border border-border bg-input px-3 py-2 text-sm " +
  "text-foreground placeholder:text-muted-foreground transition-colors " +
  "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1 " +
  "focus-visible:ring-offset-background disabled:cursor-not-allowed disabled:opacity-50 " +
  "aria-invalid:border-destructive aria-invalid:ring-destructive/30"

function describePromptError(error: unknown): string {
  if (error instanceof ApiError) {
    const field = typeof error.details.field === "string" ? error.details.field : ""
    const reason = typeof error.details.reason === "string" ? error.details.reason : ""
    const prefix = [field, reason].filter((part) => part.length > 0).join(" / ")
    return prefix.length > 0 ? `${prefix}: ${error.message}` : error.message
  }
  if (error instanceof Error) return error.message
  return "保存失败，请稍后重试"
}
