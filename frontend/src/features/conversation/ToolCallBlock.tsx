"use client"

import { ChevronRight, Wrench } from "lucide-react"
import { useState } from "react"

import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { cn } from "@/components/ui/cn"
import { formatDuration } from "@/lib/format"

import type { ToolCallPart, ToolCallStatus } from "./types"

export type ToolCallBlockProps = {
  part: ToolCallPart
  /** Retries a failed tool call; only structured retryable errors offer it. */
  onRetry?: (part: ToolCallPart) => void
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

function recordOf(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined
}

function stringField(record: Record<string, unknown> | undefined, key: string): string {
  const value = record?.[key]
  return typeof value === "string" ? value.trim() : ""
}

function stringList(record: Record<string, unknown> | undefined, key: string): string[] {
  const value = record?.[key]
  if (!Array.isArray(value)) return []
  return value.filter((item): item is string => typeof item === "string")
}

function countOf(record: Record<string, unknown>, key: string): number {
  const value = record[key]
  return Array.isArray(value) ? value.length : 0
}

type StructuredToolError = {
  code: string
  message: string
  retryable: boolean
  field: string
  allowed: string[]
  hint: string
}

/**
 * Reads the v0.22 E-1 structured tool error from a tool result
 * (`{"error":{code,message,retryable,field,allowed,hint,details}}`). Older
 * payloads put field/allowed/hint inside `details`; both shapes are honored.
 */
function structuredToolError(part: ToolCallPart): StructuredToolError | null {
  const error = recordOf(recordOf(part.result)?.error)
  if (!error) return null
  const details = recordOf(error.details)
  const field = stringField(error, "field") || stringField(details, "field")
  const hint = stringField(error, "hint") || stringField(details, "hint")
  const allowed =
    stringList(error, "allowed").length > 0
      ? stringList(error, "allowed")
      : stringList(details, "allowed")
  const code = stringField(error, "code")
  const message = stringField(error, "message")
  if (code === "" && message === "" && field === "") return null
  return { code, message, retryable: error.retryable === true, field, allowed, hint }
}

function ToolErrorDetails({
  part,
  error,
  onRetry,
}: {
  part: ToolCallPart
  error: StructuredToolError
  onRetry?: (part: ToolCallPart) => void
}) {
  return (
    <div
      data-testid="tool-error-details"
      className="space-y-1 rounded border border-destructive/30 bg-destructive/5 p-1.5"
    >
      {error.code !== "" ? (
        <p className="text-[10px] text-muted-foreground">错误码：{error.code}</p>
      ) : null}
      {error.message !== "" ? (
        <p className="text-[11px] text-destructive">{error.message}</p>
      ) : null}
      {error.field !== "" ? (
        <p className="text-[10px] text-muted-foreground">
          字段：<code className="break-all font-mono text-foreground">{error.field}</code>
        </p>
      ) : null}
      {error.allowed.length > 0 ? (
        <p className="text-[10px] text-muted-foreground">
          允许值：
          <code className="break-all font-mono text-foreground">{error.allowed.join(", ")}</code>
        </p>
      ) : null}
      {error.hint !== "" ? <p className="text-[10px] text-muted-foreground">{error.hint}</p> : null}
      {error.retryable && onRetry !== undefined ? (
        <Button variant="secondary" size="sm" onClick={() => onRetry(part)}>
          重试
        </Button>
      ) : null}
    </div>
  )
}

function DatabaseListResult({ result }: { result: Record<string, unknown> }) {
  const databases = Array.isArray(result.databases)
    ? result.databases
        .map(recordOf)
        .filter((item): item is Record<string, unknown> => item !== undefined)
    : []
  if (databases.length === 0) {
    return <p className="text-[10px] text-muted-foreground">当前版本没有绑定可用的目标数据库。</p>
  }
  return (
    <div data-testid="database-list" className="space-y-1">
      <span className="text-[10px] font-medium text-muted-foreground">
        可用数据库（database_id）
      </span>
      <ul className="space-y-0.5">
        {databases.map((database) => {
          const id = stringField(database, "id")
          const name = stringField(database, "name")
          const kind = stringField(database, "kind")
          const readOnly = database.read_only === true
          return (
            <li
              key={id}
              className="flex flex-wrap items-center gap-1 rounded bg-background/80 px-1.5 py-1 text-[10px]"
            >
              <code className="font-mono text-foreground">{id}</code>
              <span className="min-w-0 flex-1 truncate text-foreground">{name}</span>
              {kind !== "" ? <span className="text-muted-foreground">{kind}</span> : null}
              <Badge tone={readOnly ? "info" : "success"}>{readOnly ? "只读" : "可写"}</Badge>
            </li>
          )
        })}
      </ul>
    </div>
  )
}

function GraphProposeResult({
  part,
  onRetry,
}: {
  part: ToolCallPart
  onRetry?: (part: ToolCallPart) => void
}) {
  const error = structuredToolError(part)
  if (error) {
    return <ToolErrorDetails part={part} error={error} onRetry={onRetry} />
  }
  const result = recordOf(part.result) ?? {}
  const scenarios = countOf(result, "scenario_ids")
  const cases = countOf(result, "case_ids")
  const caseVersions = countOf(result, "case_version_ids")
  const graphHash = stringField(result, "graph_hash")
  return (
    <div data-testid="graph-propose-result" className="space-y-1">
      <span className="text-[10px] font-medium text-muted-foreground">已提交草案</span>
      <div className="flex flex-wrap items-center gap-1.5 text-[11px] text-foreground">
        <Badge tone="info">{`${scenarios} 个场景`}</Badge>
        <Badge tone="success">{`${cases} 个用例`}</Badge>
        <Badge tone="info">{`${caseVersions} 个用例版本`}</Badge>
      </div>
      {graphHash !== "" ? (
        <p className="break-all text-[10px] text-muted-foreground">graph_hash: {graphHash}</p>
      ) : null}
    </div>
  )
}

/**
 * One tool-call row: tool name, status and elapsed time stay visible; arguments,
 * result and error are folded behind the details toggle (spec §9.1). The v0.22
 * E-1 tools render structured results instead of raw JSON: the database
 * inventory as a readable list, graph proposals as draft counts, and field-level
 * tool errors with their path/allowed/hint plus a retry affordance.
 */
export function ToolCallBlock({ part, onRetry }: ToolCallBlockProps) {
  const [open, setOpen] = useState(false)
  const hasDetails =
    part.arguments !== undefined || part.result !== undefined || part.error !== undefined
  const duration = typeof part.duration_ms === "number" ? formatDuration(part.duration_ms) : null
  const argumentsSummary =
    part.arguments === undefined ? null : JSON.stringify(part.arguments).slice(0, 120)
  const toolError = structuredToolError(part)

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
          {toolError ? (
            <ToolErrorDetails part={part} error={toolError} onRetry={onRetry} />
          ) : part.result !== undefined && part.result !== null ? (
            part.name === "sut.database_list" ? (
              <DatabaseListResult result={recordOf(part.result) ?? {}} />
            ) : part.name === "case.graph_propose" ? (
              <GraphProposeResult part={part} onRetry={onRetry} />
            ) : (
              <DetailSection label="结果" value={part.result} />
            )
          ) : null}
          {part.error !== undefined ? <DetailSection label="错误" value={part.error} /> : null}
        </div>
      ) : null}
    </div>
  )
}
