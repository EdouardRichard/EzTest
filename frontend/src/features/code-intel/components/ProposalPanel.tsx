import { Sparkles } from "lucide-react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard, Spinner } from "@/components/ui/Spinner"

import type { CodeProposalResultView, CodeProposalView } from "../types"
import { CODE_PROPOSAL_KIND_LABELS } from "../types"

export type ProposalPanelProps = {
  proposals: CodeProposalView[]
  loading: boolean
  error: unknown
  generating: boolean
  result: CodeProposalResultView | null
  onGenerate: () => void
  onRetry: () => void
}

function proposalLocation(proposal: CodeProposalView): string {
  if (proposal.kind === "api") {
    const method = proposal.method ?? ""
    const path = proposal.path_template ?? ""
    return `${method} ${path}`.trim()
  }
  return proposal.route_template ?? proposal.url ?? ""
}

/** Proposed page/API resources generated from code facts (spec §8.1, §9.2). */
export function ProposalPanel({
  proposals,
  loading,
  error,
  generating,
  result,
  onGenerate,
  onRetry,
}: ProposalPanelProps) {
  return (
    <section aria-label="页面/API 提议" className="surface-card space-y-3 p-4">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold text-foreground">页面/API 提议</h2>
          <p className="mt-0.5 text-[11px] text-muted-foreground">
            只生成 proposed 资源，确认后才会进入资源库。
          </p>
        </div>
        <Button size="sm" variant="secondary" onClick={onGenerate} disabled={generating}>
          {generating ? (
            <span aria-hidden="true">
              <Spinner size="sm" label="生成中" />
            </span>
          ) : (
            <Sparkles className="size-3.5" aria-hidden="true" />
          )}
          从代码生成提议
        </Button>
      </div>

      {result !== null ? (
        <p role="status" className="text-[11px] text-foreground">
          本次生成：页面 新增 {result.pages.created} · 跳过 {result.pages.skipped}；API 新增{" "}
          {result.apis.created} · 跳过 {result.apis.skipped}
        </p>
      ) : null}

      {loading && proposals.length === 0 ? (
        <LoadingCard label="正在加载页面/API 提议…" />
      ) : error !== null && proposals.length === 0 ? (
        <ErrorState error={error} title="页面/API 提议加载失败" onRetry={onRetry} />
      ) : proposals.length === 0 ? (
        <EmptyState
          title="暂无页面/API 提议"
          description="触发分析后，可从代码事实生成 proposed 页面与 API。"
        />
      ) : (
        <ul className="divide-y divide-border">
          {proposals.map((proposal) => (
            <li key={`${proposal.kind}:${proposal.id}`} className="py-2">
              <div className="flex flex-wrap items-center gap-1.5">
                <Badge tone={proposal.kind === "page" ? "info" : "warning"}>
                  {CODE_PROPOSAL_KIND_LABELS[proposal.kind]}
                </Badge>
                <span className="text-xs font-medium text-foreground">{proposal.name}</span>
                <Badge tone="neutral">{proposal.source === "code" ? "代码" : "AI"}</Badge>
                <Badge tone="neutral">待确认</Badge>
              </div>
              <p className="mt-1 truncate font-mono text-[11px] text-muted-foreground">
                {proposalLocation(proposal)}
              </p>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
