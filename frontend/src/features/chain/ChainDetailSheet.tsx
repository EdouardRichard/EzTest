"use client"

import { Copy, ExternalLink, X } from "lucide-react"
import Link from "next/link"
import { useEffect, useState } from "react"

import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { formatDateTime } from "@/lib/format"

import { CHAIN_NODE_KIND_LABELS, type ChainNodeView } from "./types"

export type ChainDetailSheetProps = {
  versionId: string
  node: ChainNodeView
  onClose: () => void
}

const STATUS_TONE: Record<string, BadgeTone> = {
  open: "warning",
  draft: "neutral",
  clarified: "info",
  confirmed: "success",
  active: "info",
  running: "info",
  paused: "warning",
  done: "success",
  passed: "success",
  failed: "danger",
  blocked: "warning",
  merged: "info",
  stale: "warning",
  published: "success",
  closed: "neutral",
}

const META_KEYS = [
  "result",
  "failure_class",
  "severity",
  "issue_status",
  "category",
  "revision",
  "as_of",
  "total",
  "passed",
  "failed",
  "blocked",
  "evidence_count",
  "step_count",
  "assertion_count",
  "attempt",
  "duration_ms",
  "case_count",
  "executed_count",
  "failed_count",
  "uncovered",
  "stale",
  "wave_status",
  "trigger",
  "report_kind",
  "report_status",
  "code_ref_count",
  "close_round",
  "closure_status",
] as const

function readMetaString(meta: Record<string, unknown> | undefined, key: string): string {
  const value = meta?.[key]
  if (typeof value === "string") return value
  if (typeof value === "number") return String(value)
  if (typeof value === "boolean") return value ? "true" : "false"
  return ""
}

type LinkSpec = { label: string; href: string }

function nodeLinks(versionId: string, node: ChainNodeView): LinkSpec[] {
  const base = `/versions/${encodeURIComponent(versionId)}`
  const links: LinkSpec[] = []
  const add = (label: string, params: Record<string, string>) => {
    const search = new URLSearchParams(params)
    links.push({ label, href: `${base}?${search.toString()}` })
  }

  switch (node.kind) {
    case "issue":
      add(`打开问题 #${node.ref_id}`, { tab: "issues", issue: node.ref_id })
      if (node.case_run_id !== null && node.case_run_id !== undefined) {
        add(`打开执行 #${node.case_run_id}`, { tab: "execution", case_run: node.case_run_id })
      }
      break
    case "report":
      add(`打开报告 #${node.ref_id}`, { tab: "reports", report: node.ref_id })
      if (node.wave_id !== null && node.wave_id !== undefined) {
        add(`打开波次 #${node.wave_id}`, { tab: "execution", wave: node.wave_id })
      }
      break
    case "wave":
      add(`打开波次 #${node.ref_id}`, { tab: "execution", wave: node.ref_id })
      break
    case "case_run":
      add(`打开执行 #${node.ref_id}`, {
        tab: "execution",
        ...(node.wave_id !== null && node.wave_id !== undefined ? { wave: node.wave_id } : {}),
        case_run: node.ref_id,
      })
      break
    case "requirement":
    case "closure":
      add(`打开需求 #${node.ref_id}`, { tab: "requirements", requirement: node.ref_id })
      break
    case "scenario":
    case "case_version":
      add("打开用例设计", { tab: "design" })
      break
    default:
      break
  }
  return links
}

/**
 * Right-hand chain node sheet (spec §9.4): node metadata plus cross-tab deep
 * links. It is strictly read-only; counts and evidence enter through the
 * existing execution views instead of being embedded in the chain response.
 */
export function ChainDetailSheet({ versionId, node, onClose }: ChainDetailSheetProps) {
  const [copied, setCopied] = useState<"idle" | "done" | "error">("idle")

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose()
    }
    window.addEventListener("keydown", onKeyDown)
    return () => window.removeEventListener("keydown", onKeyDown)
  }, [onClose])

  const links = nodeLinks(versionId, node)
  const mergedIntoId = readMetaString(node.meta, "merged_into_id")
  const metaEntries = META_KEYS.map((key) => ({
    key,
    value: readMetaString(node.meta, key),
  })).filter((entry) => entry.value.length > 0)

  async function copyDeepLink(): Promise<void> {
    try {
      if (navigator.clipboard === undefined) throw new Error("clipboard is unavailable")
      await navigator.clipboard.writeText(window.location.href)
      setCopied("done")
    } catch {
      setCopied("error")
    }
  }

  return (
    <div className="fixed inset-0 z-40">
      <button
        type="button"
        aria-label="关闭链路详情遮罩"
        onClick={onClose}
        className="absolute inset-0 h-full w-full bg-slate-950/40 backdrop-blur-[1px]"
      />
      <aside
        role="dialog"
        aria-modal="true"
        aria-label="链路节点详情"
        data-testid="chain-detail-sheet"
        className="absolute inset-y-0 right-0 flex w-full max-w-md flex-col border-l border-border bg-card text-card-foreground shadow-2xl"
      >
        <header className="flex items-start justify-between gap-3 border-b border-border px-5 py-4">
          <div className="min-w-0">
            <p className="text-[11px] text-muted-foreground">
              {CHAIN_NODE_KIND_LABELS[node.kind]} · #{node.ref_id}
            </p>
            <h2 className="mt-0.5 truncate text-sm font-semibold text-foreground">{node.label}</h2>
            <div className="mt-2 flex flex-wrap items-center gap-1.5">
              {node.status !== null && node.status !== undefined && node.status.length > 0 ? (
                <Badge tone={STATUS_TONE[node.status] ?? "neutral"}>{node.status}</Badge>
              ) : null}
              {node.version_sut_id !== null && node.version_sut_id !== undefined ? (
                <Badge tone="info">SUT {node.version_sut_id}</Badge>
              ) : null}
            </div>
          </div>
          <Button variant="ghost" size="sm" aria-label="关闭链路详情" onClick={onClose}>
            <X className="size-4" aria-hidden="true" />
          </Button>
        </header>

        <div className="flex-1 space-y-4 overflow-y-auto px-5 py-4">
          <dl
            data-testid="chain-detail-meta"
            className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-[11px]"
          >
            <dt className="text-muted-foreground">节点</dt>
            <dd className="font-mono text-foreground">{node.id}</dd>
            <dt className="text-muted-foreground">创建于</dt>
            <dd className="text-foreground">{formatDateTime(node.created_at)}</dd>
            {metaEntries.map((entry) => (
              <div key={entry.key} className="contents">
                <dt className="text-muted-foreground">{entry.key}</dt>
                <dd className="text-foreground">{entry.value}</dd>
              </div>
            ))}
          </dl>

          {mergedIntoId.length > 0 ? (
            <p className="rounded-lg border border-border bg-muted/40 px-3 py-2 text-xs text-foreground">
              已合并至{" "}
              <Link
                href={`/versions/${encodeURIComponent(versionId)}?tab=issues&issue=${encodeURIComponent(mergedIntoId)}`}
                className="text-primary underline-offset-2 hover:underline"
              >
                主问题 #{mergedIntoId}
              </Link>
            </p>
          ) : null}

          <section aria-label="关联深链" className="space-y-2">
            <h3 className="text-xs font-semibold text-foreground">关联入口</h3>
            {links.length === 0 ? (
              <p className="text-[11px] text-muted-foreground">该节点没有可跳转的关联实体。</p>
            ) : (
              <ul className="space-y-1.5">
                {links.map((link) => (
                  <li key={`${link.label}-${link.href}`}>
                    <Link
                      href={link.href}
                      className="inline-flex items-center gap-1 text-xs text-primary underline-offset-2 hover:underline"
                    >
                      {link.label}
                      <ExternalLink className="size-3" aria-hidden="true" />
                    </Link>
                  </li>
                ))}
              </ul>
            )}
          </section>

          <div className="flex flex-wrap items-center gap-2 border-t border-border pt-3">
            <Button
              size="sm"
              variant="secondary"
              onClick={() => {
                void copyDeepLink()
              }}
            >
              <Copy className="size-3.5" aria-hidden="true" />
              复制链路深链
            </Button>
            {copied === "done" ? <span className="text-[11px] text-success">已复制</span> : null}
            {copied === "error" ? (
              <span className="text-[11px] text-destructive">复制失败</span>
            ) : null}
          </div>
        </div>
      </aside>
    </div>
  )
}
