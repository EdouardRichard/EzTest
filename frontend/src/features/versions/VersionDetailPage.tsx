"use client"

import { Loader2 } from "lucide-react"
import { useCallback, useEffect, useRef, useState } from "react"

import { StatusBadge } from "@/components/ui/StatusBadge"
import { ApiError } from "@/lib/api-client"

import { getVersion } from "./api"
import { OverviewTab } from "./OverviewTab"
import type { Version } from "./types"
import { VersionHeader } from "./VersionHeader"
import { isVersionTab, VersionTabs, type VersionTabValue } from "./VersionTabs"

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
}

/**
 * T17/T18 mount point. T8 deliberately renders a labelled slot instead of a
 * workbench so the container, tabs and routing can ship first (spec §9.1).
 */
function RequirementsWorkbenchSlot({ version }: RequirementsWorkbenchSlotProps) {
  return (
    <section
      aria-label="需求与澄清工作台"
      data-testid="requirements-workbench-slot"
      className="rounded-2xl border border-dashed border-border bg-card/60 p-6"
    >
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="text-base font-semibold">需求与澄清工作台</h2>
        <StatusBadge variant="info">T17 挂载点</StatusBadge>
      </div>
      <p className="mt-2 max-w-2xl text-sm leading-relaxed text-muted-foreground">
        工作台尚未接入：T17 将在此挂载需求列表、需求编辑器、附件与关系图，T18
        将挂载澄清对话、问题与决策。
      </p>
      <p className="mt-1 text-xs text-muted-foreground">
        当前版本需求计数：{version.requirementCount} 条，已确认 {version.clarifiedCount} 条。
      </p>
    </section>
  )
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
        requirements={<RequirementsWorkbenchSlot version={version} />}
        overview={<OverviewTab version={version} />}
      />
    </div>
  )
}
