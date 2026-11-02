"use client"

import { ChevronDown, ChevronRight } from "lucide-react"
import { useCallback, useEffect, useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { cn } from "@/components/ui/cn"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { formatDateTime } from "@/lib/format"

import { getTeamRoom } from "./api"
import { TEAM_ROOM_POST_LABEL } from "./labels"
import type { TeamRoom, TeamRoomKind } from "./types"

export type TeamRoomPanelProps = {
  versionId: string
  /** Bumped by `team_room.*` SSE events to refetch while expanded. */
  refreshToken?: number
  /** Team channel to read; defaults to the P1 clarification room. */
  team?: TeamRoomKind
}

const TEAM_ROOM_TITLE: Partial<Record<TeamRoomKind, string>> = {
  clarification: "澄清团队频道",
  design: "设计团队频道",
}

/**
 * Collapsed, read-only view of a team channel (spec §4.4): latest digest +
 * recent posts. P1/P2 expose no posting/chat controls.
 */
export function TeamRoomPanel({
  versionId,
  refreshToken = 0,
  team = "clarification",
}: TeamRoomPanelProps) {
  const [open, setOpen] = useState(false)
  const [room, setRoom] = useState<TeamRoom | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const title = TEAM_ROOM_TITLE[team] ?? `${team} 团队频道`

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setRoom(await getTeamRoom(versionId, team))
    } catch (cause) {
      setError(cause)
    } finally {
      setLoading(false)
    }
  }, [versionId, team])

  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshToken retriggers the fetch on SSE events
  useEffect(() => {
    if (!open) return
    void load()
  }, [open, load, refreshToken])

  return (
    <section data-testid="team-room-panel" aria-label={title} className="surface-card">
      <button
        type="button"
        data-testid="team-room-toggle"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
        className={cn(
          "flex w-full items-center gap-2 px-3.5 py-2.5 text-left",
          "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        )}
      >
        {open ? (
          <ChevronDown className="size-3.5 text-muted-foreground" aria-hidden="true" />
        ) : (
          <ChevronRight className="size-3.5 text-muted-foreground" aria-hidden="true" />
        )}
        <span className="text-xs font-semibold text-foreground">{title}</span>
        <span className="ml-auto text-[10px] text-muted-foreground">只读</span>
      </button>

      {open ? (
        <div className="border-t border-border px-3.5 py-2.5">
          {loading ? (
            <LoadingCard label="正在加载团队动态…" />
          ) : error !== null ? (
            <ErrorState error={error} title="团队频道加载失败" onRetry={() => void load()} />
          ) : room === null || (room.digest === null && room.posts.length === 0) ? (
            <EmptyState
              title="暂无团队动态"
              description="澄清团队生成摘要或发布帖子后会显示在这里。"
              className="border-none bg-transparent py-4"
            />
          ) : (
            <div className="space-y-2.5">
              {room.digest !== null ? (
                <div data-testid="team-room-digest" className="rounded-lg bg-muted/60 px-2.5 py-2">
                  <div className="flex items-center gap-1.5">
                    <span className="text-[10px] font-medium text-foreground">团队摘要</span>
                    <Badge tone="info">v{room.digest.version}</Badge>
                    <span className="ml-auto text-[10px] text-muted-foreground">
                      {formatDateTime(room.digest.updated_at)}
                    </span>
                  </div>
                  <p className="mt-1 text-[11px] leading-relaxed text-muted-foreground">
                    {room.digest.summary}
                  </p>
                </div>
              ) : null}

              {room.posts.length > 0 ? (
                <ul data-testid="team-room-posts" className="space-y-2">
                  {room.posts.map((post) => (
                    <li key={post.id} className="rounded-lg border border-border px-2.5 py-2">
                      <div className="flex flex-wrap items-center gap-1.5">
                        <Badge tone={post.kind === "blocker" ? "danger" : "neutral"}>
                          {TEAM_ROOM_POST_LABEL[post.kind]}
                        </Badge>
                        <span className="ml-auto text-[10px] text-muted-foreground">
                          {formatDateTime(post.created_at)}
                        </span>
                      </div>
                      <p className="mt-1 text-[11px] leading-relaxed text-foreground">
                        {post.content}
                      </p>
                      {post.refs.length > 0 ? (
                        <div className="mt-1 flex flex-wrap gap-1">
                          {post.refs.map((ref) => (
                            <span
                              key={`${post.id}-${ref.kind}-${ref.id}`}
                              className="rounded-full bg-accent px-1.5 py-0.5 text-[10px] text-accent-foreground"
                            >
                              {ref.kind}:{ref.id}
                            </span>
                          ))}
                        </div>
                      ) : null}
                    </li>
                  ))}
                </ul>
              ) : null}
            </div>
          )}
        </div>
      ) : null}
    </section>
  )
}
