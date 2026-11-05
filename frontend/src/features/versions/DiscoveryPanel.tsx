"use client"

import Link from "next/link"
import { useCallback, useEffect, useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { Table, type TableColumn } from "@/components/ui/Table"
import { useIsAdmin } from "@/features/auth/useIsAdmin"
import {
  confirmApiProposal,
  confirmPageProposal,
  rejectApiProposal,
  rejectPageProposal,
} from "@/features/resources/api"

import { getDiscoveryObservations } from "./api"
import type { DiscoveryDecision, DiscoveryKind, DiscoveryObservation } from "./types"

export type DiscoveryPanelProps = {
  versionId: string
  /** SSE-driven refresh nonce (`resource.delta`). */
  refreshKey?: number
  /** Notifies the page after a decision so the coverage matrix refreshes. */
  onChanged?: () => void
}

type KindFilter = "all" | DiscoveryKind
type DecisionFilter = "proposed" | "all"

const KIND_LABEL: Record<KindFilter, string> = { all: "全部类型", page: "页面", api: "API" }

function executionHref(versionId: string, observation: DiscoveryObservation): string | null {
  if (observation.wave_id === null && observation.case_run_id === null) return null
  const params = new URLSearchParams({ tab: "execution" })
  if (observation.wave_id !== null) params.set("wave", observation.wave_id)
  if (observation.case_run_id !== null) params.set("case_run", observation.case_run_id)
  return `/versions/${encodeURIComponent(versionId)}?${params.toString()}`
}

function decisionTone(decision: DiscoveryDecision): "info" | "success" | "warning" | "neutral" {
  switch (decision) {
    case "proposed":
      return "warning"
    case "approved":
      return "success"
    case "new":
      return "info"
    default:
      return "neutral"
  }
}

/**
 * W1-14B discovery panel (API-36): the execution-time observations of a
 * version, defaulting to the `proposed` backlog that still needs a decision.
 * Only an admin may approve/reject a discovered page/API; the decision reuses
 * the resource catalog routes (`POST /api/resources/pages|apis/{id}/confirm|reject`)
 * so the catalog stays the system of record. The panel is read-only for a
 * normal user.
 */
export function DiscoveryPanel({ versionId, refreshKey = 0, onChanged }: DiscoveryPanelProps) {
  const isAdmin = useIsAdmin()
  const [kind, setKind] = useState<KindFilter>("all")
  const [decision, setDecision] = useState<DecisionFilter>("proposed")
  const [observations, setObservations] = useState<DiscoveryObservation[] | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [actionError, setActionError] = useState<unknown>(null)
  const [busyKey, setBusyKey] = useState<string | null>(null)

  const load = useCallback(async () => {
    setError(null)
    // A reload (filter switch, SSE refresh) supersedes any stale decision error.
    setActionError(null)
    try {
      setObservations(
        await getDiscoveryObservations(versionId, {
          ...(kind === "all" ? {} : { kind }),
          ...(decision === "proposed" ? { decision: "proposed" as const } : {}),
          limit: 50,
        }),
      )
    } catch (cause) {
      setObservations(null)
      setError(cause)
    }
  }, [versionId, kind, decision])

  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshKey is the SSE nonce.
  useEffect(() => {
    void load()
  }, [load, refreshKey])

  async function decide(
    observation: DiscoveryObservation,
    action: "confirm" | "reject",
  ): Promise<void> {
    const resourceId = observation.resource_id
    if (resourceId === null) return
    const key = `${observation.observation_id}-${action}`
    setBusyKey(key)
    setActionError(null)
    try {
      if (observation.kind === "page") {
        await (action === "confirm"
          ? confirmPageProposal(resourceId)
          : rejectPageProposal(resourceId))
      } else {
        await (action === "confirm"
          ? confirmApiProposal(resourceId)
          : rejectApiProposal(resourceId))
      }
      await load()
      onChanged?.()
    } catch (cause) {
      // Keep the row: the resource state may have changed under another tab.
      setActionError(cause)
    } finally {
      setBusyKey(null)
    }
  }

  const columns: TableColumn<DiscoveryObservation>[] = [
    {
      key: "kind",
      header: "类型",
      render: (row) => <Badge tone={row.kind === "page" ? "info" : "neutral"}>{row.kind}</Badge>,
    },
    {
      key: "method",
      header: "方法",
      render: (row) => (
        <span className="font-mono text-[11px] text-muted-foreground">
          {row.method === "" ? "—" : row.method}
        </span>
      ),
    },
    {
      key: "host",
      header: "主机",
      render: (row) => (
        <span className="block max-w-40 truncate font-mono text-[11px] text-muted-foreground">
          {row.host}
        </span>
      ),
    },
    {
      key: "template",
      header: "资源模板",
      render: (row) => (
        <span className="block max-w-md truncate font-mono text-[11px] text-foreground">
          {row.resource_template}
        </span>
      ),
    },
    {
      key: "occurrences",
      header: "次数",
      render: (row) => <span className="text-xs text-foreground">{row.occurrences}</span>,
    },
    {
      key: "decision",
      header: "决策",
      render: (row) => <Badge tone={decisionTone(row.decision)}>{row.decision}</Badge>,
    },
    {
      key: "provenance",
      header: "来源",
      render: (row) => {
        const href = executionHref(versionId, row)
        if (href === null) return <span className="text-[11px] text-muted-foreground">—</span>
        return (
          <span className="flex flex-wrap items-center gap-1 text-[11px]">
            {row.wave_id !== null ? (
              <Link
                href={href}
                className="underline underline-offset-2 hover:text-foreground"
                aria-label={`wave ${row.wave_id}`}
              >
                wave {row.wave_id}
              </Link>
            ) : null}
            {row.case_run_id !== null ? (
              <Link
                href={href}
                className="underline underline-offset-2 hover:text-foreground"
                aria-label={`case_run ${row.case_run_id}`}
              >
                case_run {row.case_run_id}
              </Link>
            ) : null}
          </span>
        )
      },
    },
  ]

  if (isAdmin) {
    columns.push({
      key: "actions",
      header: "操作",
      className: "text-right",
      render: (row) => {
        if (row.decision !== "proposed" || row.resource_id === null) {
          return <span className="text-[11px] text-muted-foreground">—</span>
        }
        return (
          <span className="flex items-center justify-end gap-1">
            <Button
              size="sm"
              variant="secondary"
              disabled={busyKey !== null}
              onClick={() => void decide(row, "confirm")}
            >
              批准
            </Button>
            <Button
              size="sm"
              variant="ghost"
              disabled={busyKey !== null}
              onClick={() => void decide(row, "reject")}
            >
              拒绝
            </Button>
          </span>
        )
      },
    })
  }

  return (
    <section
      data-testid="discovery-panel"
      aria-label="探索发现"
      className="space-y-2 rounded-lg border border-border bg-card px-3 py-2.5"
    >
      <header className="flex flex-wrap items-end justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold text-foreground">探索发现</h2>
          <p className="text-[11px] text-muted-foreground">
            共 {observations?.length ?? 0} 条观测 · 批准/拒绝复用资源目录的确认路由
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-1">
          {(["all", "page", "api"] as KindFilter[]).map((value) => (
            <Button
              key={value}
              size="sm"
              variant={kind === value ? "primary" : "ghost"}
              aria-pressed={kind === value}
              onClick={() => setKind(value)}
            >
              {KIND_LABEL[value]}
            </Button>
          ))}
          <Button
            size="sm"
            variant={decision === "proposed" ? "primary" : "ghost"}
            aria-pressed={decision === "proposed"}
            onClick={() => setDecision("proposed")}
          >
            仅待处理
          </Button>
          <Button
            size="sm"
            variant={decision === "all" ? "primary" : "ghost"}
            aria-pressed={decision === "all"}
            onClick={() => setDecision("all")}
          >
            全部状态
          </Button>
        </div>
      </header>

      {error !== null ? (
        <ErrorState error={error} title="探索发现加载失败" onRetry={() => void load()} />
      ) : observations === null ? (
        <LoadingCard label="正在加载探索发现…" />
      ) : observations.length === 0 ? (
        <EmptyState
          title="该版本暂无探索发现"
          description="执行 wave 结束后，命中允许主机的页面/API 观测会出现在这里，等待批准或拒绝。"
        />
      ) : (
        <Table
          ariaLabel="探索发现观测"
          columns={columns}
          rows={observations}
          rowKey={(row) => row.observation_id}
        />
      )}

      {actionError !== null ? <InlineError error={actionError} /> : null}
    </section>
  )
}
