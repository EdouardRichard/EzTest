"use client"

import { useCallback, useEffect, useRef, useState } from "react"

import { useIsAdmin } from "@/features/auth/useIsAdmin"
import { ApiError } from "@/lib/api-client"

import { listSessionFixtures, type SessionFixture } from "./session-fixtures"

/** Page size for the admin fixture listing (restores the pre-pagination 100-row
 * first-page coverage; "load more" walks the rest so audit coverage is complete,
 * not silently capped — constitution art.7.6). */
const PAGE_SIZE = 100

/**
 * Admin visibility of the version's session fixture library (SC-F5): the saved
 * login-state fixtures the execution team reuses across case_runs, newest
 * first. Metadata only — the reuse key (sut/environment/role), the
 * content-addressed state reference and provenance; the state bytes are never
 * fetched here. Non-admins see nothing (the endpoint is admin-guarded).
 */
export function SessionFixturePanel({ versionId }: { versionId: string }) {
  const isAdmin = useIsAdmin()
  const [fixtures, setFixtures] = useState<SessionFixture[]>([])
  const [nextCursor, setNextCursor] = useState("")
  const [hasMore, setHasMore] = useState(false)
  const [loading, setLoading] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // requestId guards against out-of-order responses: a manual refresh (or a
  // version change / unmount) bumps the counter so any earlier in-flight
  // response — including a "load more" continuation of the superseded view — is
  // ignored and cannot overwrite the latest result.
  const requestIdRef = useRef(0)

  const load = useCallback(() => {
    if (!isAdmin) return
    const requestId = ++requestIdRef.current
    setLoading(true)
    setLoadingMore(false)
    setError(null)
    listSessionFixtures(versionId, { limit: PAGE_SIZE })
      .then((page) => {
        if (requestIdRef.current !== requestId) return
        setFixtures(page.items)
        setNextCursor(page.nextCursor)
        setHasMore(page.hasMore)
      })
      .catch((cause: unknown) => {
        if (requestIdRef.current !== requestId) return
        setError(cause instanceof ApiError ? cause.message : "会话夹具列表加载失败")
      })
      .finally(() => {
        if (requestIdRef.current === requestId) setLoading(false)
      })
  }, [isAdmin, versionId])

  const loadMore = useCallback(() => {
    if (!isAdmin || !hasMore || loadingMore || loading) return
    // A continuation of the CURRENT view: it does not bump requestId, but a
    // concurrent refresh/version-change does, so a late continuation of a
    // superseded view is dropped by the same guard.
    const requestId = requestIdRef.current
    const before = nextCursor
    setLoadingMore(true)
    listSessionFixtures(versionId, { limit: PAGE_SIZE, before })
      .then((page) => {
        if (requestIdRef.current !== requestId) return
        setFixtures((prev) => [...prev, ...page.items])
        setNextCursor(page.nextCursor)
        setHasMore(page.hasMore)
      })
      .catch((cause: unknown) => {
        if (requestIdRef.current !== requestId) return
        setError(cause instanceof ApiError ? cause.message : "会话夹具列表加载失败")
      })
      .finally(() => {
        if (requestIdRef.current === requestId) setLoadingMore(false)
      })
  }, [isAdmin, hasMore, loadingMore, loading, nextCursor, versionId])

  useEffect(() => {
    load()
    // Invalidate any in-flight request on unmount / dependency change so a late
    // response never lands after the panel is gone or the version switched.
    return () => {
      requestIdRef.current++
    }
  }, [load])

  if (!isAdmin) return null

  return (
    <section
      aria-label="会话夹具库"
      data-testid="session-fixture-panel"
      className="surface-card space-y-2 px-3 py-2"
    >
      <div className="flex items-center justify-between">
        <h3 className="text-xs font-semibold text-foreground">会话夹具库（登录态复用）</h3>
        <button
          type="button"
          data-testid="session-fixture-refresh"
          className="text-[11px] text-muted-foreground hover:text-foreground"
          onClick={load}
        >
          刷新
        </button>
      </div>
      {error ? (
        <p
          role="alert"
          data-testid="session-fixture-error"
          className="text-[11px] text-destructive"
        >
          {error}
        </p>
      ) : loading ? (
        <p role="status" className="text-[11px] text-muted-foreground">
          加载中…
        </p>
      ) : fixtures.length === 0 ? (
        <p data-testid="session-fixture-empty" className="text-[11px] text-muted-foreground">
          暂无会话夹具。执行团队登录成功后经 state-save + fixture.upload
          保存登录态，后续用例即可复用。
        </p>
      ) : (
        <>
          <table className="w-full text-[11px]" data-testid="session-fixture-table">
            <thead>
              <tr className="text-left text-muted-foreground">
                <th className="py-1 pr-2 font-medium">角色</th>
                <th className="py-1 pr-2 font-medium">SUT</th>
                <th className="py-1 pr-2 font-medium">环境</th>
                <th className="py-1 pr-2 font-medium">状态引用（sha256）</th>
                <th className="py-1 font-medium">保存时间</th>
              </tr>
            </thead>
            <tbody>
              {fixtures.map((fixture) => (
                <tr
                  key={fixture.id}
                  data-testid={`session-fixture-${fixture.id}`}
                  className="border-t border-border/40"
                >
                  <td className="py-1 pr-2 text-foreground">{fixture.role}</td>
                  <td className="py-1 pr-2 text-muted-foreground">{fixture.sut_id}</td>
                  <td className="py-1 pr-2 text-muted-foreground">{fixture.environment_id}</td>
                  <td className="py-1 pr-2 font-mono text-muted-foreground">
                    {fixture.storage_state_ref.slice(0, 12)}…
                  </td>
                  <td className="py-1 text-muted-foreground">{fixture.created_at}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {hasMore ? (
            <button
              type="button"
              data-testid="session-fixture-load-more"
              className="text-[11px] text-muted-foreground hover:text-foreground"
              onClick={loadMore}
              disabled={loadingMore}
            >
              {loadingMore ? "加载中…" : "加载更多"}
            </button>
          ) : null}
        </>
      )}
    </section>
  )
}
