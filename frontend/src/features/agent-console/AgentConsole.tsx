"use client"

import * as Tabs from "@radix-ui/react-tabs"
import { ShieldAlert } from "lucide-react"
import { useEffect, useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { StatusBadge } from "@/components/ui/StatusBadge"
import { ApiError } from "@/lib/api-client"

import type { AgentProfile, AgentSession } from "./agent-api"
import { createSession, getSession, listProfiles, listSessions } from "./agent-api"
import { ConfigPane } from "./ConfigPane"
import { SessionList } from "./SessionList"
import { SessionPane } from "./SessionPane"

const SESSION_PAGE_SIZE = 20

/** Reads `?session=` from the location without requiring a Suspense boundary. */
function readSessionParam(): number | null {
  const raw = new URLSearchParams(window.location.search).get("session")
  if (raw === null) return null
  const parsed = Number(raw)
  return Number.isInteger(parsed) && parsed > 0 ? parsed : null
}

/** Writes `?session=` back with replaceState so selection survives a refresh. */
function writeSessionParam(id: number | null): void {
  const url = new URL(window.location.href)
  if (id === null) url.searchParams.delete("session")
  else url.searchParams.set("session", String(id))
  window.history.replaceState(null, "", `${url.pathname}${url.search}${url.hash}`)
}

function describeLoadError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
    return error.message
  }
  return error instanceof Error ? error.message : "未知错误"
}

function ConsoleHeader({ subtitle }: { subtitle?: string }) {
  return (
    <header className="flex flex-wrap items-start justify-between gap-3">
      <div>
        <div className="flex items-center gap-2">
          <h1 className="text-2xl font-semibold tracking-tight">Agent 运行时控制台</h1>
          <StatusBadge variant="info">P1</StatusBadge>
        </div>
        <p className="mt-1 text-sm text-muted-foreground">
          {subtitle ?? "与内置 Agent 会话、查看实时活动并调整运行时配置。"}
        </p>
      </div>
    </header>
  )
}

/**
 * The /agents console shell: loads the agent profiles and the first session
 * page in parallel, keeps the selected session in `?session=` (read in an
 * effect, never with useSearchParams so the route needs no Suspense boundary),
 * and switches between the session and configuration tabs.
 */
export function AgentConsole() {
  const [profiles, setProfiles] = useState<AgentProfile[]>([])
  const [sessions, setSessions] = useState<AgentSession[]>([])
  const [selectedId, setSelectedId] = useState<number | null>(null)
  const [selectedSession, setSelectedSession] = useState<AgentSession | null>(null)
  const [hasMore, setHasMore] = useState(false)
  const [nextCursor, setNextCursor] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [creating, setCreating] = useState(false)
  const [forbidden, setForbidden] = useState(false)
  const [error, setError] = useState("")

  useEffect(() => {
    let cancelled = false

    async function load() {
      setLoading(true)
      setError("")
      try {
        const [profileList, page] = await Promise.all([
          listProfiles(),
          listSessions({ limit: SESSION_PAGE_SIZE }),
        ])
        if (cancelled) return
        setProfiles(Array.isArray(profileList) ? profileList : [])
        setSessions(Array.isArray(page.items) ? page.items : [])
        setHasMore(page.has_more)
        setNextCursor(page.next_cursor)

        const requested = readSessionParam()
        if (requested !== null) {
          setSelectedId(requested)
          const found = page.items.find((session) => session.id === requested) ?? null
          if (found) {
            setSelectedSession(found)
          } else {
            try {
              const fetched = await getSession(requested)
              // Do not clobber a selection the user made while this was loading.
              if (!cancelled && readSessionParam() === requested) setSelectedSession(fetched)
            } catch {
              // A stale deep link (deleted session) keeps the empty pane.
              if (!cancelled) setSelectedSession(null)
            }
          }
        }
      } catch (caught) {
        if (cancelled) return
        if (caught instanceof ApiError && caught.status === 403) {
          setForbidden(true)
        } else {
          setError(describeLoadError(caught))
        }
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    void load()
    return () => {
      cancelled = true
    }
  }, [])

  function selectSession(id: number) {
    setSelectedId(id)
    setSelectedSession(sessions.find((session) => session.id === id) ?? null)
    writeSessionParam(id)
  }

  async function handleCreate() {
    if (creating) return
    setCreating(true)
    setError("")
    try {
      const created = await createSession({})
      setSessions((previous) => [created, ...previous.filter((item) => item.id !== created.id)])
      setSelectedId(created.id)
      setSelectedSession(created)
      writeSessionParam(created.id)
    } catch (caught) {
      if (caught instanceof ApiError && caught.status === 403) setForbidden(true)
      else setError(describeLoadError(caught))
    } finally {
      setCreating(false)
    }
  }

  async function handleLoadMore() {
    if (nextCursor === null || loadingMore) return
    setLoadingMore(true)
    setError("")
    try {
      const page = await listSessions({ limit: SESSION_PAGE_SIZE, cursor: nextCursor })
      setSessions((previous) => {
        const known = new Set(previous.map((session) => session.id))
        return [...previous, ...page.items.filter((session) => !known.has(session.id))]
      })
      setHasMore(page.has_more)
      setNextCursor(page.next_cursor)
    } catch (caught) {
      if (caught instanceof ApiError && caught.status === 403) setForbidden(true)
      else setError(describeLoadError(caught))
    } finally {
      setLoadingMore(false)
    }
  }

  function handleDeleted(id: number) {
    setSessions((previous) => previous.filter((session) => session.id !== id))
    if (selectedId === id) {
      setSelectedId(null)
      setSelectedSession(null)
      writeSessionParam(null)
    }
  }

  function handleProfileSaved(updated: AgentProfile) {
    setProfiles((previous) =>
      previous.map((profile) => (profile.id === updated.id ? updated : profile)),
    )
  }

  if (forbidden) {
    return (
      <div className="space-y-6">
        <ConsoleHeader subtitle="Agent 运行时控制台仅对管理员开放。" />
        <EmptyState
          icon={ShieldAlert}
          title="仅管理员可用"
          description="当前账户没有管理员权限，无法查看或操作 Agent 会话。"
        />
      </div>
    )
  }

  return (
    <div className="space-y-6">
      <ConsoleHeader />

      {error ? (
        <p
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          {error}
        </p>
      ) : null}

      <Tabs.Root defaultValue="sessions">
        <Tabs.List
          aria-label="Agent 控制台视图"
          className="inline-flex gap-1 rounded-xl bg-muted p-1"
        >
          <Tabs.Trigger
            value="sessions"
            className="rounded-lg px-3 py-1.5 text-sm font-medium text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring data-[state=active]:bg-card data-[state=active]:text-foreground data-[state=active]:shadow-sm"
          >
            会话
          </Tabs.Trigger>
          <Tabs.Trigger
            value="config"
            className="rounded-lg px-3 py-1.5 text-sm font-medium text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring data-[state=active]:bg-card data-[state=active]:text-foreground data-[state=active]:shadow-sm"
          >
            配置
          </Tabs.Trigger>
        </Tabs.List>

        <Tabs.Content value="sessions" className="pt-3">
          <div className="grid items-start gap-4 lg:grid-cols-[280px_minmax(0,1fr)]">
            <SessionList
              sessions={sessions}
              selectedId={selectedId}
              loading={loading}
              hasMore={hasMore}
              onSelect={selectSession}
              onCreate={() => void handleCreate()}
              onLoadMore={() => void handleLoadMore()}
              onDeleted={handleDeleted}
            />
            <SessionPane sessionId={selectedId} session={selectedSession} />
          </div>
        </Tabs.Content>

        <Tabs.Content value="config" className="pt-3">
          <ConfigPane profiles={profiles} onSaved={handleProfileSaved} />
        </Tabs.Content>
      </Tabs.Root>
    </div>
  )
}
