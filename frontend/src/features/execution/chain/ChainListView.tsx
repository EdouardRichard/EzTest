"use client"

import { type KeyboardEvent, useMemo } from "react"

import { cn } from "@/lib/utils"

import { layoutChain, type PositionedNode } from "./chain-layout"
import {
  getCaseStatusToken,
  getIssueSeverityToken,
  getIssueStatusToken,
  getRequirementStatusToken,
  getResultStatusToken,
  NODE_KIND_TOKENS,
} from "./chain-tokens"
import type { ChainSnapshot, ChainSnapshotNode } from "./chain-types"

export type ChainListViewProps = {
  snapshot: ChainSnapshot
  collapsed?: readonly string[] | ReadonlySet<string>
  selectedId?: string | null
  onSelect: (nodeId: string) => void
}

type ListRow = {
  node: ChainSnapshotNode
  relation: string
  evidence: string
}

function domainIdOf(nodeId: string): string {
  const separator = nodeId.indexOf(":")
  return separator === -1 ? nodeId : nodeId.slice(separator + 1)
}

function statusLabelOf(node: ChainSnapshotNode): string {
  if (node.kind === "result") return getResultStatusToken(node.status).label
  if (node.kind === "case") return getCaseStatusToken(node.status).label
  if (node.kind === "issue") return getIssueStatusToken(node.status).label
  return getRequirementStatusToken(node.status).label
}

function relationOf(node: PositionedNode, titleById: ReadonlyMap<string, string>): string {
  if (node.kind === "requirement") return "根节点"
  if (node.kind === "case") {
    const titles = node.requirementIds
      .map((id) => titleById.get(id))
      .filter((title): title is string => title !== undefined && title !== "")
    return titles.length === 0 ? "未关联需求" : titles.join("、")
  }
  if (node.kind === "result") {
    return titleById.get(node.caseNodeId ?? "") ?? "—"
  }
  const parentId = node.resultNodeId ?? node.caseNodeId
  return titleById.get(parentId ?? "") ?? "未关联"
}

function evidenceOf(node: ChainSnapshotNode): string {
  if (node.kind !== "result" && node.kind !== "issue") return "—"
  return `证据 ${node.evidenceCount ?? 0}`
}

/**
 * List-view fallback of the exploration chain (spec §5.5).
 *
 * The canvas is a primarily visual representation; this table offers the same
 * visible nodes as type/title/status/relation/evidence rows, reachable with a
 * keyboard through a native button per row. It is never the default view.
 */
export function ChainListView({
  snapshot,
  collapsed,
  selectedId = null,
  onSelect,
}: ChainListViewProps) {
  const layout = useMemo(
    () => layoutChain(snapshot.nodes, { edges: snapshot.edges, collapsed }),
    [snapshot.nodes, snapshot.edges, collapsed],
  )

  const rows = useMemo<ListRow[]>(() => {
    const titleById = new Map<string, string>()
    for (const node of snapshot.nodes) titleById.set(node.id, node.title)
    const positionedRows = layout.nodes.map((positioned) => ({
      node: positioned.data,
      relation: relationOf(positioned, titleById),
      evidence: evidenceOf(positioned.data),
    }))
    // Unlinked issues are not part of the graph but must remain reachable.
    const unlinkedRows = layout.unlinkedIssues.map((issue) => ({
      node: issue,
      relation: "未关联用例",
      evidence: evidenceOf(issue),
    }))
    return [...positionedRows, ...unlinkedRows]
  }, [layout, snapshot.nodes])

  function handleRowKeyDown(event: KeyboardEvent<HTMLButtonElement>, nodeId: string): void {
    if (event.key !== "Enter" && event.key !== " ") return
    event.preventDefault()
    onSelect(nodeId)
  }

  return (
    <div className="overflow-auto rounded-2xl border border-border bg-card">
      <table
        data-testid="chain-list-view"
        aria-label="探索链路列表"
        className="w-full text-left text-xs"
      >
        <thead className="sticky top-0 bg-muted/80 text-[11px] uppercase tracking-wide text-muted-foreground">
          <tr>
            <th scope="col" className="px-3 py-2 font-semibold">
              类型
            </th>
            <th scope="col" className="px-3 py-2 font-semibold">
              标题
            </th>
            <th scope="col" className="px-3 py-2 font-semibold">
              状态
            </th>
            <th scope="col" className="px-3 py-2 font-semibold">
              关联
            </th>
            <th scope="col" className="px-3 py-2 font-semibold">
              证据
            </th>
          </tr>
        </thead>
        <tbody>
          {rows.map(({ node, relation, evidence }) => {
            const domainId = domainIdOf(node.id)
            const kindToken = NODE_KIND_TOKENS[node.kind]
            const Icon = kindToken.icon
            const selected = selectedId === node.id
            const issueSeverity =
              node.kind === "issue" ? getIssueSeverityToken(node.severity) : null
            return (
              <tr
                key={node.id}
                data-testid={`chain-list-row-${domainId}`}
                data-node-id={node.id}
                data-kind={node.kind}
                data-status={node.status}
                aria-selected={selected}
                className={cn("border-t border-border", selected && "bg-muted/60")}
              >
                <td className="whitespace-nowrap px-3 py-2">
                  <span className={cn("inline-flex items-center gap-1", kindToken.textClass)}>
                    <Icon className="h-3 w-3" aria-hidden="true" />
                    {kindToken.label}
                  </span>
                </td>
                <td className="px-3 py-2">
                  <button
                    type="button"
                    data-testid={`chain-list-open-${domainId}`}
                    aria-label={`打开${kindToken.label}：${node.title}`}
                    onClick={() => onSelect(node.id)}
                    onKeyDown={(event) => handleRowKeyDown(event, node.id)}
                    className="rounded text-left font-medium text-foreground underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    {node.title}
                  </button>
                  {issueSeverity === null ? null : (
                    <span className={cn("ml-1.5 text-[10px]", issueSeverity.textClass)}>
                      {issueSeverity.label}
                    </span>
                  )}
                </td>
                <td className="whitespace-nowrap px-3 py-2 text-muted-foreground">
                  {statusLabelOf(node)}
                </td>
                <td className="px-3 py-2 text-muted-foreground">{relation}</td>
                <td className="whitespace-nowrap px-3 py-2 text-muted-foreground">{evidence}</td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}
