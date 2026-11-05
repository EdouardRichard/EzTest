"use client"

import { useCallback, useEffect, useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { ErrorState } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Input } from "@/components/ui/Input"
import { LoadingCard } from "@/components/ui/Spinner"
import { formatDateTime } from "@/lib/format"

import { getVersionHandoff, type HandoffView } from "./api"

export type HandoffPanelProps = {
  versionId: string
  /** Deep link / previous selection; omitted means the newest revision. */
  initialRevision?: string
}

function EmptyLine({ children }: { children: string }) {
  return <p className="text-[11px] text-muted-foreground">{children}</p>
}

/**
 * Clarification → design handoff document (B §8.1). The six blocks always exist:
 * requirements, Q&A, decisions, open items, plan and resources. A missing
 * revision is a 404 and is rendered as a retryable error; an explicit revision
 * can be read without leaving the design workspace.
 */
export function HandoffPanel({ versionId, initialRevision }: HandoffPanelProps) {
  const [handoff, setHandoff] = useState<HandoffView | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [revisionInput, setRevisionInput] = useState(initialRevision ?? "")

  const load = useCallback(
    async (revision?: string) => {
      setError(null)
      try {
        setHandoff(await getVersionHandoff(versionId, revision))
      } catch (cause) {
        setHandoff(null)
        setError(cause)
      }
    },
    [versionId],
  )

  useEffect(() => {
    void load(undefined)
  }, [load])

  function readRevision(): void {
    const trimmed = revisionInput.trim()
    void load(trimmed === "" ? undefined : trimmed)
  }

  return (
    <section data-testid="handoff-panel" aria-label="澄清交接文档" className="space-y-3">
      <header className="flex flex-wrap items-end justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold text-foreground">澄清交接文档</h2>
          <p className="text-[11px] text-muted-foreground">
            {handoff === null
              ? "需求 / 问答 / 决策 / 待决 / 计划 / 资源 六块交接"
              : `修订 #${handoff.revision} · 生成于 ${formatDateTime(handoff.generated_at)}`}
          </p>
        </div>
        <form
          className="flex items-end gap-2"
          onSubmit={(event) => {
            event.preventDefault()
            readRevision()
          }}
        >
          <Field id="handoff-revision" label="修订号" className="w-24">
            <Input
              id="handoff-revision"
              inputMode="numeric"
              value={revisionInput}
              placeholder="最新"
              onChange={(event) => setRevisionInput(event.target.value)}
            />
          </Field>
          <Button size="sm" variant="secondary" type="submit">
            读取
          </Button>
        </form>
      </header>

      {error !== null ? (
        <ErrorState error={error} title="交接文档加载失败" onRetry={() => void load(undefined)} />
      ) : handoff === null ? (
        <LoadingCard label="正在加载交接文档…" />
      ) : (
        <div className="space-y-3">
          <section aria-label="交接需求" className="space-y-1.5">
            <h3 className="text-xs font-semibold text-foreground">需求</h3>
            {handoff.requirements.length === 0 ? (
              <EmptyLine>暂无已确认需求</EmptyLine>
            ) : (
              <ul className="space-y-1.5">
                {handoff.requirements.map((requirement) => (
                  <li
                    key={requirement.id}
                    className="rounded-md border border-border px-2 py-1.5 text-[11px]"
                  >
                    <div className="flex flex-wrap items-center gap-1.5">
                      <span className="font-medium text-foreground">{requirement.title}</span>
                      <Badge tone="info">{requirement.priority}</Badge>
                      <span className="text-muted-foreground">{requirement.status}</span>
                    </div>
                    <p className="mt-0.5 text-muted-foreground">
                      {requirement.acceptance_criteria}
                    </p>
                  </li>
                ))}
              </ul>
            )}
          </section>

          <section aria-label="交接问答" className="space-y-1.5">
            <h3 className="text-xs font-semibold text-foreground">问答</h3>
            {handoff.qa.length === 0 ? (
              <EmptyLine>暂无问答记录</EmptyLine>
            ) : (
              <ul className="space-y-1.5">
                {handoff.qa.map((qa) => (
                  <li
                    key={qa.question_id}
                    className="rounded-md border border-border px-2 py-1.5 text-[11px]"
                  >
                    <p className="font-medium text-foreground">{qa.question}</p>
                    <p className="mt-0.5 text-muted-foreground">
                      状态 {qa.status} · 回答：
                      {qa.answer_text !== ""
                        ? qa.answer_text
                        : qa.answer_selected.length === 0
                          ? "未回答"
                          : qa.answer_selected.join("、")}
                    </p>
                  </li>
                ))}
              </ul>
            )}
          </section>

          <section aria-label="交接决策" className="space-y-1.5">
            <h3 className="text-xs font-semibold text-foreground">决策</h3>
            {handoff.decisions.length === 0 ? (
              <EmptyLine>暂无设计决策</EmptyLine>
            ) : (
              <ul className="space-y-1">
                {handoff.decisions.map((decision) => (
                  <li key={decision.fact_id} className="text-[11px]">
                    <span className="font-medium text-foreground">{decision.summary}</span>
                    <span className="ml-1.5 text-muted-foreground">
                      （{decision.kind} · {decision.key}）
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </section>

          <section aria-label="交接待决" className="space-y-1.5">
            <h3 className="text-xs font-semibold text-foreground">待决</h3>
            {handoff.open.length === 0 ? (
              <EmptyLine>暂无待决事项</EmptyLine>
            ) : (
              <ul className="space-y-1">
                {handoff.open.map((item, index) => (
                  <li key={`${item.kind}-${item.reference_id ?? index}`} className="text-[11px]">
                    <span className="font-medium text-foreground">{item.summary}</span>
                    <span className="ml-1.5 text-muted-foreground">
                      （{item.kind} · {item.status}）
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </section>

          <section aria-label="交接计划" className="space-y-1.5">
            <h3 className="text-xs font-semibold text-foreground">计划</h3>
            <p className="text-[11px] text-muted-foreground">阶段 {handoff.plan.stage}</p>
            {handoff.plan.current === null || handoff.plan.current === undefined ? (
              <EmptyLine>暂无当前计划项</EmptyLine>
            ) : (
              <p className="text-[11px] text-foreground">
                当前：{handoff.plan.current.title}（{handoff.plan.current.phase} ·{" "}
                {handoff.plan.current.status}）
              </p>
            )}
            {handoff.plan.next !== null && handoff.plan.next !== undefined ? (
              <p className="text-[11px] text-muted-foreground">
                下一步：{handoff.plan.next.title}（{handoff.plan.next.phase}）
              </p>
            ) : null}
          </section>

          <section aria-label="交接资源" className="space-y-1.5">
            <h3 className="text-xs font-semibold text-foreground">资源</h3>
            <p className="text-[11px] text-muted-foreground">
              SUT {handoff.resources.suts.length} · 页面 {handoff.resources.pages.length} · API{" "}
              {handoff.resources.apis.length} · 账号 {handoff.resources.accounts.length} · 目标库{" "}
              {handoff.resources.databases.length} · 代码仓 {handoff.resources.repositories.length}
            </p>
          </section>
        </div>
      )}
    </section>
  )
}
