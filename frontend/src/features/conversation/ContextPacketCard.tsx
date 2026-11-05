"use client"

import { useCallback, useEffect, useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { useIsAdmin } from "@/features/auth/useIsAdmin"
import { ApiError } from "@/lib/api-client"

import { type ContextPacketView, getLatestContextPacket } from "./api"

export type ContextPacketCardProps = {
  versionId: string
  /** SSE-driven refresh nonce (stage boundary / session reset invalidations). */
  refreshKey?: number
}

const INVALIDATION_LABEL: Record<string, string> = {
  "": "正常追加",
  stage_boundary: "阶段切换",
  session_reset: "会话重开",
  window_full: "窗口将满",
}

function shortHash(hash: string): string {
  return hash.length <= 12 ? hash : hash.slice(0, 12)
}

function isApiError(error: unknown, status: number): boolean {
  return error instanceof ApiError && error.status === status
}

/**
 * Latest persisted context packet card (admin-only, spec §6.3). The packet
 * contains prompt content, so a normal user gets an explicit read-only notice
 * instead of an error, a version with no recorded packet gets an empty state,
 * and an unwired reader stays a retryable error.
 */
export function ContextPacketCard({ versionId, refreshKey = 0 }: ContextPacketCardProps) {
  const isAdmin = useIsAdmin()
  const [packet, setPacket] = useState<ContextPacketView | null>(null)
  const [error, setError] = useState<unknown>(null)

  const load = useCallback(async () => {
    // Defence in depth for the admin-only endpoint: callers should not mount the
    // card for a normal user, and if they do it must not fire a guaranteed 403.
    if (!isAdmin) return
    setError(null)
    try {
      setPacket(await getLatestContextPacket(versionId))
    } catch (cause) {
      setPacket(null)
      setError(cause)
    }
  }, [versionId, isAdmin])

  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshKey is the SSE nonce.
  useEffect(() => {
    void load()
  }, [load, refreshKey])

  const forbidden = !isAdmin || isApiError(error, 403)
  const missing = isApiError(error, 404)

  return (
    <section
      data-testid="context-packet"
      aria-label="上下文包"
      className="space-y-2 rounded-lg border border-border bg-card px-3 py-2.5"
    >
      <header className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold text-foreground">上下文包</h2>
          <p className="text-[11px] text-muted-foreground">
            最近一次持久化的 cache-first 上下文 · 仅管理员可读
          </p>
        </div>
        <Badge tone="neutral">admin</Badge>
      </header>

      {forbidden ? (
        <p data-testid="context-packet-forbidden" className="text-[11px] text-muted-foreground">
          上下文包仅管理员可读（包含 system 前缀与回放 transcript），当前账号无权限查看。
        </p>
      ) : missing ? (
        <EmptyState
          title="该版本暂无上下文包"
          description="首次 agent run 记录上下文包后会在这里出现。"
        />
      ) : error !== null ? (
        <ErrorState error={error} title="上下文包加载失败" onRetry={() => void load()} />
      ) : packet === null ? (
        <LoadingCard label="正在加载上下文包…" />
      ) : (
        <div className="space-y-1.5 text-[11px]">
          <div className="flex flex-wrap items-center gap-1.5">
            <Badge tone={packet.cache_hit ? "success" : "neutral"}>
              {packet.cache_hit ? "命中" : "未命中"}
            </Badge>
            <span className="text-foreground">缓存命中 {packet.cached_tokens} tokens</span>
            <span className="text-muted-foreground">
              失效原因：
              {INVALIDATION_LABEL[packet.invalidation_reason] ?? packet.invalidation_reason}
              {packet.invalidation_reason !== "" ? `（${packet.invalidation_reason}）` : ""}
            </span>
          </div>
          <p className="text-muted-foreground">
            稳定前缀 <code>{shortHash(packet.prefix_hash)}</code> · 简报{" "}
            <code>{shortHash(packet.briefing_hash)}</code>
          </p>
          <details className="rounded-md border border-border px-2 py-1.5">
            <summary className="cursor-pointer text-foreground">
              {packet.segments.length} 段上下文（前缀 + 回放）
            </summary>
            <ol className="mt-1.5 space-y-1">
              {packet.segments.map((segment) => (
                <li
                  key={`${segment.prefix ? "prefix" : "dynamic"}:${segment.role}:${segment.name}:${segment.tokens}`}
                  className="rounded-md bg-muted/40 px-2 py-1"
                >
                  <div className="flex flex-wrap items-center gap-1.5">
                    <span className="font-medium text-foreground">{segment.name}</span>
                    <Badge tone="info">{segment.role}</Badge>
                    {segment.prefix ? <Badge tone="neutral">前缀</Badge> : null}
                    <span className="text-muted-foreground">{segment.tokens} tokens</span>
                  </div>
                  <p className="mt-0.5 max-h-24 overflow-auto whitespace-pre-wrap break-all text-muted-foreground">
                    {segment.content.length > 2000
                      ? `${segment.content.slice(0, 2000)}…`
                      : segment.content}
                  </p>
                </li>
              ))}
            </ol>
          </details>
        </div>
      )}
    </section>
  )
}
