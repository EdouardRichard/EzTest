import { Alert } from "@/components/ui/Alert"
import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"

import type { CodeConfidence, CodeFactKind, CodeFactView, CodeFileWindowView } from "../types"
import { CODE_CONFIDENCE_LABELS, CODE_FACT_KIND_LABELS, CODE_FACT_KIND_ORDER } from "../types"

export type FactsPanelProps = {
  facts: CodeFactView[]
  hasMore: boolean
  loading: boolean
  error: unknown
  focusedFactId?: string | null
  fileWindow: CodeFileWindowView | null
  fileWindowFact: CodeFactView | null
  fileWindowLoading: boolean
  fileWindowError: unknown
  onOpenFile: (fact: CodeFactView) => void
  onReview: (fact: CodeFactView) => void
  onRetry: () => void
  onRetryFileWindow?: () => void
}

const CONFIDENCE_TONE: Record<CodeConfidence, BadgeTone> = {
  high: "success",
  medium: "info",
  low: "neutral",
}

type FactGroup = {
  kind: CodeFactKind
  facts: CodeFactView[]
}

function groupFacts(facts: CodeFactView[]): FactGroup[] {
  return CODE_FACT_KIND_ORDER.map((kind) => ({
    kind,
    facts: facts.filter((fact) => fact.kind === kind),
  })).filter((group) => group.facts.length > 0)
}

function windowLines(window: CodeFileWindowView): Array<{ number: number; text: string }> {
  return window.content.split("\n").map((text, index) => ({
    number: window.start_line + index,
    text,
  }))
}

function isHighlighted(lineNumber: number, fact: CodeFactView | null): boolean {
  if (fact === null) return false
  if (typeof fact.start_line !== "number") return false
  const end = typeof fact.end_line === "number" ? fact.end_line : fact.start_line
  return lineNumber >= fact.start_line && lineNumber <= end
}

/** Middle column: T1 facts grouped by kind plus the audited file window. */
export function FactsPanel({
  facts,
  hasMore,
  loading,
  error,
  focusedFactId = null,
  fileWindow,
  fileWindowFact,
  fileWindowLoading,
  fileWindowError,
  onOpenFile,
  onReview,
  onRetry,
  onRetryFileWindow,
}: FactsPanelProps) {
  const groups = groupFacts(facts)

  return (
    <section aria-label="代码事实" className="surface-card space-y-3 p-4">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold text-foreground">代码事实</h2>
          <p className="mt-0.5 text-[11px] text-muted-foreground">
            低置信事实以“线索”展示，不自动选中影响。
          </p>
        </div>
        <Badge tone="neutral">{facts.length} 条</Badge>
      </div>

      {hasMore ? <p className="text-[11px] text-warning">仅显示前 100 条（还有更多）</p> : null}

      {loading && facts.length === 0 ? (
        <LoadingCard label="正在加载代码事实…" />
      ) : error !== null && facts.length === 0 ? (
        <ErrorState error={error} title="代码事实加载失败" onRetry={onRetry} />
      ) : facts.length === 0 ? (
        <EmptyState
          title="暂无代码事实"
          description="触发变更分析后生成 T1 事实；AI 不可用时只保留确定性索引。"
        />
      ) : (
        <div className="space-y-4">
          {groups.map((group) => (
            <div key={group.kind}>
              <h3 className="text-xs font-semibold text-muted-foreground">
                {CODE_FACT_KIND_LABELS[group.kind]}
                <span className="ml-1 font-normal">({group.facts.length})</span>
              </h3>
              <ul className="mt-1 divide-y divide-border">
                {group.facts.map((fact) => {
                  const focused = fact.id === focusedFactId
                  const clue = fact.confidence === "low"
                  return (
                    <li
                      key={fact.id}
                      aria-current={focused ? "true" : undefined}
                      className={clue ? "py-2 opacity-60" : "py-2"}
                    >
                      <div className="flex items-start gap-2">
                        <div className="min-w-0 flex-1">
                          <button
                            type="button"
                            onClick={() => onOpenFile(fact)}
                            aria-label={`查看事实 ${fact.name ?? fact.fact_key}`}
                            className="block max-w-full truncate text-left font-mono text-xs text-foreground hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                          >
                            {fact.name ?? fact.fact_key}
                          </button>
                          <p
                            className="truncate text-[11px] text-muted-foreground"
                            title={fact.path}
                          >
                            {fact.path}
                            {typeof fact.start_line === "number"
                              ? `:${fact.start_line}${typeof fact.end_line === "number" ? `-${fact.end_line}` : ""}`
                              : ""}
                          </p>
                          <div className="mt-1 flex flex-wrap items-center gap-1.5">
                            <Badge tone={CONFIDENCE_TONE[fact.confidence]}>
                              {CODE_CONFIDENCE_LABELS[fact.confidence]}
                            </Badge>
                            {fact.evidence === undefined || fact.evidence.length === 0 ? (
                              <Badge tone="warning">无证据</Badge>
                            ) : (
                              <span className="text-[11px] text-muted-foreground">
                                证据 {fact.evidence.length} 条
                              </span>
                            )}
                          </div>
                        </div>
                        <Button variant="ghost" size="sm" onClick={() => onReview(fact)}>
                          复核
                        </Button>
                      </div>
                    </li>
                  )
                })}
              </ul>
            </div>
          ))}
        </div>
      )}

      {fileWindowFact !== null ? (
        <section
          aria-label="文件窗口"
          className="space-y-2 rounded-lg border border-border bg-muted/20 p-3"
        >
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className="min-w-0 truncate font-mono text-[11px] text-foreground">
              {fileWindowFact.path}
            </p>
            {fileWindow !== null ? (
              <p className="text-[11px] text-muted-foreground">
                行 {fileWindow.start_line}-{fileWindow.end_line} / {fileWindow.total_lines}
              </p>
            ) : null}
          </div>
          {fileWindowLoading ? (
            <LoadingCard label="正在加载文件窗口…" />
          ) : fileWindowError !== null ? (
            <ErrorState
              error={fileWindowError}
              title="文件窗口加载失败"
              onRetry={onRetryFileWindow}
            />
          ) : fileWindow === null ? null : fileWindow.binary ? (
            <Alert tone="info">二进制文件不显示内容</Alert>
          ) : (
            <>
              {fileWindow.truncated ? <Alert tone="warning">文件窗口已截断</Alert> : null}
              <pre className="max-h-72 overflow-auto rounded-md border border-border bg-card p-2 font-mono text-[11px] leading-relaxed">
                {windowLines(fileWindow).map((line) => {
                  const highlighted = isHighlighted(line.number, fileWindowFact)
                  return (
                    <div
                      key={line.number}
                      data-line={line.number}
                      aria-current={highlighted ? "true" : undefined}
                      className={
                        highlighted
                          ? "flex gap-2 bg-warning/15 text-foreground"
                          : "flex gap-2 text-muted-foreground"
                      }
                    >
                      <span className="w-8 shrink-0 select-none text-right">{line.number}</span>
                      <span className="whitespace-pre-wrap">
                        {line.text.length > 0 ? line.text : " "}
                      </span>
                    </div>
                  )
                })}
              </pre>
            </>
          )}
        </section>
      ) : null}
    </section>
  )
}
