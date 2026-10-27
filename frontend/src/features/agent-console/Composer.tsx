"use client"

import { Send, Square } from "lucide-react"
import { type KeyboardEvent, useState } from "react"

export type ComposerProps = {
  running: boolean
  disabled?: boolean
  sending?: boolean
  stopping?: boolean
  error?: string
  onSend: (message: string) => void
  onStop: () => void
}

/**
 * Message composer: Enter sends, Shift+Enter inserts a newline, the composer is
 * disabled while a run is active/sending, and the stop control only appears for
 * an active run. API failures (409/503) arrive as the `error` prop.
 */
export function Composer({
  running,
  disabled = false,
  sending = false,
  stopping = false,
  error = "",
  onSend,
  onStop,
}: ComposerProps) {
  const [message, setMessage] = useState("")
  const blocked = disabled || running || sending
  const canSend = !blocked && message.trim() !== ""

  function submit() {
    const value = message.trim()
    if (!canSend || value === "") return
    setMessage("")
    onSend(value)
  }

  function handleKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (event.key !== "Enter" || event.shiftKey) return
    event.preventDefault()
    submit()
  }

  return (
    <div className="space-y-2">
      <div className="flex items-end gap-2">
        <textarea
          aria-label="发送消息"
          value={message}
          onChange={(event) => setMessage(event.target.value)}
          onKeyDown={handleKeyDown}
          disabled={blocked}
          rows={3}
          placeholder="输入消息，Enter 发送，Shift+Enter 换行"
          className="min-h-[4.5rem] flex-1 resize-y rounded-xl border border-border bg-card px-3 py-2 text-sm text-foreground placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-60"
        />
        <div className="flex shrink-0 flex-col gap-1.5">
          <button
            type="button"
            onClick={submit}
            disabled={!canSend}
            className="inline-flex items-center justify-center gap-1.5 rounded-lg bg-primary px-3.5 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <Send className="h-3.5 w-3.5" aria-hidden="true" />
            发送
          </button>
          {running || stopping ? (
            <button
              type="button"
              aria-label="停止"
              onClick={onStop}
              disabled={stopping}
              className="inline-flex items-center justify-center gap-1.5 rounded-lg border border-destructive/40 bg-destructive/10 px-3.5 py-2 text-sm font-medium text-destructive transition-colors hover:bg-destructive/20 disabled:cursor-not-allowed disabled:opacity-60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <Square className="h-3.5 w-3.5" aria-hidden="true" />
              {stopping ? "停止中…" : "停止"}
            </button>
          ) : null}
        </div>
      </div>
      {error ? (
        <p
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          {error}
        </p>
      ) : null}
    </div>
  )
}
