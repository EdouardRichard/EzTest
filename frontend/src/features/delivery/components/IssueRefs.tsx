"use client"

import type { IssueCodeRef, IssueDetail } from "../types"

/** First ref id of one kind, used when the detail summary object is absent. */
function refId(issue: IssueDetail, kind: string): string {
  return issue.refs.find((ref) => ref.kind === kind)?.id ?? ""
}

export type IssueRefsProps = {
  /** Version id used by the case/code deep links; never re-parsed. */
  versionId: string
  issue: IssueDetail
  onOpenEvidence?: (evidenceId: string) => void
  onOpenCase?: (caseNodeId: string) => void
  onOpenCodeRef?: (ref: IssueCodeRef) => void
}

const LINK_CLASS = "text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"

/**
 * Reference column of the issue detail (spec §10.3): evidence links (opened by
 * the caller), case/requirement deep links and code anchors. It renders only
 * the refs the API returned; absent kinds say so instead of fabricating data.
 */
export function IssueRefs({
  versionId,
  issue,
  onOpenEvidence,
  onOpenCase,
  onOpenCodeRef,
}: IssueRefsProps) {
  const evidenceRefs = issue.refs.filter((ref) => ref.kind === "evidence")
  const caseRunId = issue.case?.caseRunId ?? refId(issue, "case_run")
  const caseNodeId = issue.case?.caseNodeId ?? refId(issue, "case_node")
  const requirementId = issue.requirement?.id ?? refId(issue, "requirement")
  const versionBase = `/versions/${encodeURIComponent(versionId)}`

  return (
    <section className="space-y-3">
      <div>
        <h4 className="text-xs font-semibold text-muted-foreground">
          证据（{evidenceRefs.length}）
        </h4>
        {evidenceRefs.length === 0 ? (
          <p className="mt-1 text-xs text-muted-foreground">无关联证据</p>
        ) : (
          <ul className="mt-1 flex flex-wrap gap-2">
            {evidenceRefs.map((ref) => (
              <li key={`evidence:${ref.id}`}>
                <button
                  type="button"
                  data-testid="issue-evidence-link"
                  onClick={() => onOpenEvidence?.(ref.id)}
                  className="rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  查看证据 {ref.id}
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>

      <div>
        <h4 className="text-xs font-semibold text-muted-foreground">关联用例与需求</h4>
        <ul className="mt-1 space-y-1 text-xs">
          {caseRunId === "" ? null : (
            <li>
              <a
                data-testid="issue-case-link"
                href={`${versionBase}?tab=results&caseRun=${encodeURIComponent(caseRunId)}`}
                className={LINK_CLASS}
              >
                用例运行 {caseRunId}
              </a>
            </li>
          )}
          {caseNodeId === "" ? null : (
            <li>
              <a
                data-testid="issue-case-node-link"
                href={`${versionBase}/mindmap?case=${encodeURIComponent(caseNodeId)}`}
                onClick={(event) => {
                  if (onOpenCase === undefined) return
                  event.preventDefault()
                  onOpenCase(caseNodeId)
                }}
                className={LINK_CLASS}
              >
                用例脑图 {caseNodeId}
              </a>
            </li>
          )}
          {requirementId === "" ? null : (
            <li>
              <a
                data-testid="issue-requirement-link"
                href={`${versionBase}?tab=requirements&requirement=${encodeURIComponent(requirementId)}`}
                className={LINK_CLASS}
              >
                关联需求 {requirementId}
              </a>
            </li>
          )}
          {caseRunId === "" && caseNodeId === "" && requirementId === "" ? (
            <li className="text-muted-foreground">无关联用例或需求</li>
          ) : null}
        </ul>
      </div>

      <div>
        <h4 className="text-xs font-semibold text-muted-foreground">代码位置</h4>
        {issue.codeRefs.length === 0 ? (
          <p className="mt-1 text-xs text-muted-foreground">无代码锚点</p>
        ) : (
          <ul className="mt-1 space-y-1 text-xs">
            {issue.codeRefs.map((ref) => (
              <li key={`${ref.anchorId}:${ref.path}:${ref.line}`}>
                <a
                  data-testid="issue-code-ref"
                  href={`${versionBase}?tab=code&path=${encodeURIComponent(ref.path)}&line=${ref.line}`}
                  onClick={(event) => {
                    if (onOpenCodeRef === undefined) return
                    event.preventDefault()
                    onOpenCodeRef(ref)
                  }}
                  className={`break-all font-mono ${LINK_CLASS}`}
                >
                  {ref.path}:{ref.line}
                </a>
                {ref.label === "" ? null : (
                  <span className="ml-1 text-muted-foreground">{ref.label}</span>
                )}
              </li>
            ))}
          </ul>
        )}
      </div>
    </section>
  )
}
