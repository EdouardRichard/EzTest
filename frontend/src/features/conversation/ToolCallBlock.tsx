"use client"

import { ChevronRight, Wrench } from "lucide-react"
import { useState } from "react"

import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { cn } from "@/components/ui/cn"
import { formatDuration } from "@/lib/format"

import type { ToolCallPart, ToolCallStatus } from "./types"

export type ToolCallBlockProps = {
  part: ToolCallPart
}

const STATUS_LABEL: Record<ToolCallStatus, string> = {
  running: "运行中",
  done: "完成",
  failed: "失败",
}

const STATUS_TONE: Record<ToolCallStatus, BadgeTone> = {
  running: "info",
  done: "success",
  failed: "danger",
}

function DetailSection({ label, value }: { label: string; value: unknown }) {
  return (
    <div className="space-y-0.5">
      <span className="text-[10px] font-medium text-muted-foreground">{label}</span>
      <pre className="max-h-40 overflow-auto rounded bg-background/80 p-1.5 text-[10px] leading-relaxed text-foreground">
        {typeof value === "string" ? value : JSON.stringify(value, null, 2)}
      </pre>
    </div>
  )
}

/**
 * One tool-call row: tool name, status and elapsed time stay visible; arguments,
 * result and error are folded behind the details toggle (spec §9.1).
 */
export function ToolCallBlock({ part }: ToolCallBlockProps) {
  const [open, setOpen] = useState(false)
  const hasDetails =
    part.arguments !== undefined || part.result !== undefined || part.error !== undefined
  const duration = typeof part.duration_ms === "number" ? formatDuration(part.duration_ms) : null
  const argumentsSummary =
    part.arguments === undefined ? null : JSON.stringify(part.arguments).slice(0, 120)

  return (
    <div
      data-testid={`tool-call-${part.call_id}`}
      className="rounded-lg border border-border/70 bg-muted/20 px-2 py-1.5"
    >
      <div className="flex flex-wrap items-center gap-1.5">
        <Wrench className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span className="min-w-0 flex-1 truncate font-mono text-[11px] font-medium text-foreground">
          {part.name}
        </span>
        <Badge tone={STATUS_TONE[part.status]}>{STATUS_LABEL[part.status]}</Badge>
        {duration !== null ? (
          <span className="text-[10px] text-muted-foreground">{duration}</span>
        ) : null}
      </div>

      {argumentsSummary !== null ? (
        <p className="mt-0.5 truncate pl-5 font-mono text-[10px] text-muted-foreground">
          {argumentsSummary}
        </p>
      ) : null}

      {part.error !== undefined ? (
        <p role="alert" className="mt-1 pl-5 text-[11px] text-destructive">
          {part.error}
        </p>
      ) : null}

      {hasDetails ? (
        <button
          type="button"
          aria-expanded={open}
          onClick={() => setOpen((current) => !current)}
          className="mt-1 flex items-center gap-1 text-[10px] text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <ChevronRight
            className={cn("size-3 transition-transform", open && "rotate-90")}
            aria-hidden="true"
          />
          {open ? "收起结果" : "查看结果"}
        </button>
      ) : null}

      {open ? (
        <div className="mt-1 space-y-1.5 pl-4">
          {part.arguments !== undefined ? (
            <DetailSection label="参数" value={part.arguments} />
          ) : null}
          {part.result !== undefined && part.result !== null ? (
            <DetailSection label="结果" value={part.result} />
          ) : null}
          {part.error !== undefined ? <DetailSection label="错误" value={part.error} /> : null}
        </div>
      ) : null}
    </div>
  )
}
