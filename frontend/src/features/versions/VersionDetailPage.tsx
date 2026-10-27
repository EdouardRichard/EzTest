"use client"

import { Loader2 } from "lucide-react"
import { useCallback, useEffect, useRef, useState } from "react"

import { CodeChangesTab } from "@/features/code-intel/CodeChangesTab"
import { ApiError } from "@/lib/api-client"

import { getVersion } from "./api"
import { OverviewTab } from "./OverviewTab"
import type { Version } from "./types"
import { VersionHeader } from "./VersionHeader"
import { isVersionTab, VersionTabs, type VersionTabValue } from "./VersionTabs"
import { Workbench } from "./Workbench"

const DEFAULT_TAB: VersionTabValue = "requirements"

/** Reads `?tab=` from the location; unknown values fall back to requirements. */
function readTabParam(): VersionTabValue {
  if (typeof window === "undefined") return DEFAULT_TAB
  const raw = new URLSearchParams(window.location.search).get("tab")
  return isVersionTab(raw) ? raw : DEFAULT_TAB
}

/** Writes the active tab back with replaceState so the deep link survives refresh. */
function writeTabParam(value: VersionTabValue): void {
  if (typeof window === "undefined") return
  const url = new URL(window.location.href)
  url.searchParams.set("tab", value)
  window.history.replaceState(null, "", `${url.pathname}${url.search}${url.hash}`)
}

function describeLoadError(error: unknown): string {
  if (!(error instanceof ApiError)) {
    return error instanceof Error ? error.message : "未知错误"
  }
  if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
  if (error.status === 404) return "版本不存在或已被删除。"
  if (error.status === 401) return "登录状态已失效，请重新登录。"
  return error.message
}

type RequirementsWorkbenchSlotProps = {
  version: Version
  onChanged: () => void
}

/**
 * Requirements tab content. T17 owns the workbench; T18 extends it with the
 * clarification column. `onChanged` refetches the version so the header
 * counters stay in sync after requirement writes.
 */
function RequirementsWorkbenchSlot({ version, onChanged }: RequirementsWorkbenchSlotProps) {
  return <Workbench version={version} onChanged={onChanged} />
}

export type VersionDetailPageProps = {
  /** P3-owned ID; a decimal string from the dynamic route segment. */
  versionId: string
}

/**
 * Version detail container (spec §9.1/§9.2): loads the version, renders the
 * header and the nine-tab shell, keeps the active tab in `?tab=` (default
 * `requirements`), and exposes a refresh path that T17/T18 can reuse after
 * they mutate version-scoped data.
 */
export function VersionDetailPage({ versionId }: VersionDetailPageProps) {
  const [version, setVersion] = useState<Version | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState("")
  const [tab, setTab] = useState<VersionTabValue>(DEFAULT_TAB)
  const requestRef = useRef(0)

  const load = useCallback(async (): Promise<void> => {
    const requestId = ++requestRef.current
    setLoading(true)
    setError("")
    try {
      const data = await getVersion(versionId)
      if (requestRef.current === requestId) setVersion(data)
    } catch (caught) {
      if (requestRef.current === requestId) setError(describeLoadError(caught))
    } finally {
      if (requestRef.current === requestId) setLoading(false)
    }
  }, [versionId])

  useEffect(() => {
    setTab(readTabParam())
  }, [])

  useEffect(() => {
    void load()
    return () => {
      // Invalidate any request still in flight for this mount/version.
      requestRef.current += 1
    }
  }, [load])

  const refresh = useCallback(() => {
    void load()
  }, [load])

  function handleTabChange(value: VersionTabValue): void {
    setTab(value)
    writeTabParam(value)
  }

  if (version === null) {
    return (
      <div className="space-y-4">
        {error === "" ? (
          <p className="flex items-center gap-2 rounded-xl border border-dashed border-border px-4 py-10 text-sm text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
            正在加载版本…
          </p>
        ) : (
          <div
            role="alert"
            className="space-y-3 rounded-xl border border-destructive/30 bg-destructive/10 px-4 py-6 text-sm text-destructive"
          >
            <p>加载版本失败：{error}</p>
            <button
              type="button"
              onClick={refresh}
              className="rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              重试
            </button>
          </div>
        )}
      </div>
    )
  }

  return (
    <div className="space-y-4">
      <VersionHeader
        version={version}
        refreshing={loading}
        onRefresh={refresh}
        onUpdated={setVersion}
      />

      {error === "" ? null : (
        <p
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          刷新版本失败：{error}（正在展示上次数据）
        </p>
      )}

      <VersionTabs
        active={tab}
        onTabChange={handleTabChange}
        requirements={<RequirementsWorkbenchSlot version={version} onChanged={refresh} />}
        overview={<OverviewTab version={version} />}
        code={<CodeChangesTab versionId={versionId} repoId={version.codeRepositoryId} />}
      />
    </div>
  )
}
