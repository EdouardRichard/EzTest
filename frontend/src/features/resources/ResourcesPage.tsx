"use client"

import * as Tabs from "@radix-ui/react-tabs"
import { Boxes, Globe, KeyRound, Layers, Server } from "lucide-react"
import { useEffect, useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { StatusBadge } from "@/components/ui/StatusBadge"
import { useAuth } from "@/features/auth/AuthProvider"

import { EnvironmentsPanel } from "./EnvironmentsPanel"
import { SystemsPanel } from "./SystemsPanel"

const TABS = [
  { value: "systems", label: "系统与环境", icon: Server },
  { value: "app-models", label: "应用模型", icon: Boxes },
  { value: "accounts", label: "账号与访问", icon: KeyRound },
  { value: "sessions", label: "会话", icon: Layers },
] as const

type ResourceTab = (typeof TABS)[number]["value"]

const TAB_VALUES = new Set<string>(TABS.map((tab) => tab.value))

function isResourceTab(value: string | null): value is ResourceTab {
  return value !== null && TAB_VALUES.has(value)
}

/** Reads `?tab=` from the location so the route needs no Suspense boundary. */
function readTabParam(): ResourceTab {
  if (typeof window === "undefined") return "systems"
  const raw = new URLSearchParams(window.location.search).get("tab")
  return isResourceTab(raw) ? raw : "systems"
}

function readPositiveIntParam(name: string): number | null {
  if (typeof window === "undefined") return null
  const raw = new URLSearchParams(window.location.search).get(name)
  if (raw === null) return null
  const parsed = Number(raw)
  return Number.isInteger(parsed) && parsed > 0 ? parsed : null
}

/** Writes one query parameter back with replaceState so deep links survive refresh. */
function writeQueryParam(name: string, value: string | null): void {
  if (typeof window === "undefined") return
  const url = new URL(window.location.href)
  if (value === null) url.searchParams.delete(name)
  else url.searchParams.set(name, value)
  window.history.replaceState(null, "", `${url.pathname}${url.search}${url.hash}`)
}

const tabTriggerClass =
  "flex items-center justify-center gap-2 rounded-lg px-3 py-2 text-sm font-medium text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring data-[state=active]:bg-card data-[state=active]:text-foreground data-[state=active]:shadow-sm"

/**
 * `/resources` shell: four tabs for the public-resource areas. The tab and the
 * selected system live in the URL (`?tab=`, `?system=`) so later panels
 * (app models, accounts, sessions) can extend the same deep-link scheme.
 * Normal users get a read-only badge and never see write controls.
 */
export function ResourcesPage() {
  const { user } = useAuth()
  const canWrite = user?.role === "admin"
  const readonly = user !== null && !canWrite

  const [tab, setTab] = useState<ResourceTab>("systems")
  const [selectedSystemId, setSelectedSystemId] = useState<number | null>(null)

  useEffect(() => {
    setTab(readTabParam())
    setSelectedSystemId(readPositiveIntParam("system"))
  }, [])

  function handleTabChange(value: string) {
    if (!isResourceTab(value)) return
    setTab(value)
    writeQueryParam("tab", value)
  }

  function handleSelectSystem(id: number) {
    setSelectedSystemId(id)
    writeQueryParam("system", String(id))
  }

  return (
    <div className="space-y-6">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <h1 className="text-2xl font-semibold tracking-tight">公共资源</h1>
            <StatusBadge variant="info">P2</StatusBadge>
            {readonly ? <StatusBadge variant="warning">只读</StatusBadge> : null}
          </div>
          <p className="mt-1 text-sm text-muted-foreground">
            系统、环境、应用模型、账号与 Session 等公共资源；普通用户只读，管理员可维护。
          </p>
        </div>
      </header>

      <Tabs.Root value={tab} onValueChange={handleTabChange}>
        <Tabs.List
          aria-label="资源分类"
          className="grid grid-cols-2 gap-1 rounded-xl bg-muted p-1 sm:grid-cols-4"
        >
          {TABS.map((item) => {
            const Icon = item.icon
            return (
              <Tabs.Trigger key={item.value} value={item.value} className={tabTriggerClass}>
                <Icon className="h-4 w-4" aria-hidden="true" />
                {item.label}
              </Tabs.Trigger>
            )
          })}
        </Tabs.List>

        <Tabs.Content value="systems" className="pt-4">
          <div className="grid items-start gap-4 lg:grid-cols-[300px_minmax(0,1fr)]">
            <SystemsPanel
              canWrite={canWrite}
              selectedId={selectedSystemId}
              onSelect={handleSelectSystem}
            />
            {selectedSystemId === null ? (
              <EmptyState
                icon={Globe}
                title="选择一个系统"
                description="从左侧选择一个系统后查看和维护它的环境。"
              />
            ) : (
              <EnvironmentsPanel systemId={selectedSystemId} canWrite={canWrite} />
            )}
          </div>
        </Tabs.Content>

        <Tabs.Content value="app-models" className="pt-4">
          <EmptyState
            icon={Boxes}
            title="应用模型面板即将上线"
            description="应用模型树、kind 筛选与 JSON 导入将在后续任务中提供。"
          />
        </Tabs.Content>

        <Tabs.Content value="accounts" className="pt-4">
          <EmptyState
            icon={KeyRound}
            title="账号与访问面板即将上线"
            description="账号列表与 Access 凭据配置将在后续任务中提供。"
          />
        </Tabs.Content>

        <Tabs.Content value="sessions" className="pt-4">
          <EmptyState
            icon={Layers}
            title="会话面板即将上线"
            description="Session 状态、快照与人工接管将在后续任务中提供。"
          />
        </Tabs.Content>
      </Tabs.Root>
    </div>
  )
}
