"use client"

import { Settings } from "lucide-react"
import { useEffect, useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { StatusBadge } from "@/components/ui/StatusBadge"
import { ApiError } from "@/lib/api-client"

import type { AgentProfile, AgentTool } from "./agent-api"
import { listTools } from "./agent-api"
import { ProfileEditor } from "./ProfileEditor"

export type ConfigPaneProps = {
  profiles: AgentProfile[]
  onSaved: (profile: AgentProfile) => void
}

function describeToolError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
    return error.message
  }
  return error instanceof Error ? error.message : "未知错误"
}

/**
 * Configuration tab: loads the registered tool catalog once, shows the profile
 * rail (P1 ships a single built-in profile) and mounts the editor for the
 * selected profile. The editor is keyed by profile id so switching profiles
 * resets its local form state instead of carrying edits across.
 */
export function ConfigPane({ profiles, onSaved }: ConfigPaneProps) {
  const [tools, setTools] = useState<AgentTool[]>([])
  const [toolsLoading, setToolsLoading] = useState(true)
  const [toolsError, setToolsError] = useState("")
  const [selectedId, setSelectedId] = useState<number | null>(profiles[0]?.id ?? null)

  useEffect(() => {
    let cancelled = false

    async function loadTools() {
      setToolsLoading(true)
      setToolsError("")
      try {
        const catalog = await listTools()
        if (!cancelled) setTools(Array.isArray(catalog) ? catalog : [])
      } catch (caught) {
        if (!cancelled) setToolsError(`工具列表加载失败：${describeToolError(caught)}`)
      } finally {
        if (!cancelled) setToolsLoading(false)
      }
    }

    void loadTools()
    return () => {
      cancelled = true
    }
  }, [])

  const selected = profiles.find((profile) => profile.id === selectedId) ?? profiles[0] ?? null

  if (selected === null) {
    return (
      <EmptyState
        icon={Settings}
        title="暂无 Agent 配置"
        description="服务端没有返回可编辑的 Agent profile；请先完成内置 profile 初始化。"
      />
    )
  }

  return (
    <section aria-label="Agent 配置" className="space-y-3">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="text-base font-semibold">Agent 配置</h2>
          <p className="mt-1 text-sm text-muted-foreground">
            调整内置 Agent 的提示词、预算、思考模式与工具白名单。
          </p>
        </div>
        {toolsLoading ? <span className="text-xs text-muted-foreground">正在加载工具…</span> : null}
      </div>

      {toolsError ? (
        <p
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          {toolsError}
        </p>
      ) : null}

      <div className="grid items-start gap-4 lg:grid-cols-[220px_minmax(0,1fr)]">
        <nav
          aria-label="Agent 配置列表"
          className="rounded-2xl border border-border bg-card p-2 shadow-sm"
        >
          <ul className="space-y-1">
            {profiles.map((profile) => {
              const active = profile.id === selected.id
              return (
                <li key={profile.id}>
                  <button
                    type="button"
                    onClick={() => setSelectedId(profile.id)}
                    aria-current={active ? "true" : undefined}
                    className={
                      active
                        ? "w-full rounded-lg border border-primary/40 bg-primary/10 px-2.5 py-2 text-left text-sm font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                        : "w-full rounded-lg border border-transparent px-2.5 py-2 text-left text-sm font-medium transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    }
                  >
                    {profile.name}
                  </button>
                  <div className="flex flex-wrap items-center gap-1.5 px-2.5 pb-1.5">
                    <span className="break-all font-mono text-[11px] text-muted-foreground">
                      {profile.key}
                    </span>
                    {profile.builtin ? (
                      <StatusBadge variant="info" dot={false}>
                        内置
                      </StatusBadge>
                    ) : null}
                  </div>
                </li>
              )
            })}
          </ul>
        </nav>

        <ProfileEditor key={selected.id} profile={selected} tools={tools} onSaved={onSaved} />
      </div>
    </section>
  )
}
