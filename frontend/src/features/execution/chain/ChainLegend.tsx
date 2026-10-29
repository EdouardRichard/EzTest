"use client"

import { cn } from "@/lib/utils"

import { EDGE_KIND_TOKENS, NODE_KIND_TOKENS } from "./chain-tokens"
import type { ChainEdgeKindDTO, ChainNodeKindDTO } from "./chain-types"

const NODE_KINDS: readonly ChainNodeKindDTO[] = ["requirement", "case", "result", "issue"]
const EDGE_KINDS: readonly ChainEdgeKindDTO[] = [
  "requirement_case",
  "case_result",
  "result_issue",
  "case_issue",
  "impact_case",
]

/** Spec §5.4: only the impact edge is dashed; the others are solid. */
const EDGE_LINE_STYLE: Record<ChainEdgeKindDTO, "solid" | "dashed"> = {
  requirement_case: "solid",
  case_result: "solid",
  result_issue: "solid",
  case_issue: "solid",
  impact_case: "dashed",
}

function EdgeSample({ kind }: { kind: ChainEdgeKindDTO }) {
  const lineStyle = EDGE_LINE_STYLE[kind]
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 28 10"
      className="h-2.5 w-7 shrink-0"
      data-line-style={lineStyle}
    >
      <line
        x1="1"
        y1="5"
        x2="27"
        y2="5"
        stroke={EDGE_KIND_TOKENS[kind].stroke}
        strokeWidth="2"
        strokeDasharray={lineStyle === "dashed" ? "4 3" : undefined}
      />
    </svg>
  )
}

export type ChainLegendProps = {
  /** Shows the empty-chain hint instead of only the palette (spec §5.4). */
  empty?: boolean
  className?: string
}

/**
 * Overlay legend of the exploration chain (spec §5.4): the four node kinds and
 * the five semantic edge kinds, all reading `chain-tokens` so the legend can
 * never drift from the canvas. Every item carries an icon/sample and a text
 * label, so meaning never depends on colour alone.
 */
export function ChainLegend({ empty = false, className }: ChainLegendProps) {
  return (
    <aside
      data-testid="chain-legend"
      role="note"
      aria-label="链路图例"
      className={cn(
        "absolute left-2 top-2 z-20 hidden w-60 rounded-2xl border border-border bg-card/95 p-3 text-xs shadow-lg backdrop-blur lg:block",
        className,
      )}
    >
      <h3 className="text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">
        图例
      </h3>

      <ul className="mt-2 space-y-1" aria-label="节点类型">
        {NODE_KINDS.map((kind) => {
          const token = NODE_KIND_TOKENS[kind]
          const Icon = token.icon
          return (
            <li
              key={kind}
              data-testid={`chain-legend-node-${kind}`}
              data-color={token.color}
              data-stroke={token.stroke}
              className="flex items-center gap-2"
            >
              <span
                aria-hidden="true"
                className={cn(
                  "inline-flex h-4 w-4 items-center justify-center rounded border",
                  token.badgeClass,
                )}
              >
                <Icon className="h-2.5 w-2.5" />
              </span>
              <span className="text-foreground">{token.label}</span>
            </li>
          )
        })}
      </ul>

      <h4 className="mt-3 text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">
        关系
      </h4>
      <ul className="mt-1.5 space-y-1" aria-label="边关系">
        {EDGE_KINDS.map((kind) => {
          const token = EDGE_KIND_TOKENS[kind]
          return (
            <li
              key={kind}
              data-testid={`chain-legend-edge-${kind}`}
              data-color={token.color}
              data-stroke={token.stroke}
              data-line-style={EDGE_LINE_STYLE[kind]}
              className="flex items-center gap-2"
            >
              <EdgeSample kind={kind} />
              <span className="text-foreground">{token.label}</span>
              <span className="ml-auto text-[10px] text-muted-foreground">
                {EDGE_LINE_STYLE[kind] === "dashed" ? "虚线" : "实线"}
              </span>
            </li>
          )
        })}
      </ul>

      {empty ? (
        <p
          data-testid="chain-legend-empty"
          className="mt-3 rounded-lg border border-dashed border-border px-2 py-1.5 text-[10px] leading-relaxed text-muted-foreground"
        >
          暂无链路节点：创建需求、用例并执行后，节点与关系会显示在这里。
        </p>
      ) : null}

      <p
        data-testid="chain-legend-note"
        className="mt-3 text-[10px] leading-relaxed text-muted-foreground"
      >
        仅异常包含失败、阻塞、不确定、中断；截断时以服务端返回的计数为准。
      </p>
    </aside>
  )
}
