"use client"

import { useRouter } from "next/navigation"
import { useState } from "react"

import { Alert } from "@/components/ui/Alert"
import { type TabItem, Tabs } from "@/components/ui/Tabs"
import { useAuth } from "@/features/auth/AuthProvider"

import { AuditPanel } from "./AuditPanel"
import { LLMSettings } from "./LLMSettings"
import { SystemSettings } from "./SystemSettings"
import { UsersPanel } from "./UsersPanel"

export type SettingsTab = "general" | "llm" | "users" | "audit"

const TAB_ITEMS: TabItem[] = [
  { value: "general", label: "常规设置" },
  { value: "llm", label: "LLM 配置" },
  { value: "users", label: "用户管理" },
  { value: "audit", label: "审计日志" },
]

const TAB_VALUES: SettingsTab[] = ["general", "llm", "users", "audit"]

/** Normalizes a `?tab=` value; unknown/absent values fall back to general. */
export function normalizeSettingsTab(value: string | null | undefined): SettingsTab {
  if (value === "llm" || value === "users" || value === "audit" || value === "general") {
    return value
  }
  return "general"
}

function activeLabel(tab: SettingsTab): string {
  return TAB_ITEMS.find((item) => item.value === tab)?.label ?? "常规设置"
}

/**
 * Admin settings console. The active tab is deep-linkable (`/settings?tab=`),
 * and each panel owns its own data loading so a tab switch is independent.
 */
export function SettingsPage({ initialTab }: { initialTab?: string | null }) {
  const router = useRouter()
  const { user } = useAuth()
  const [tab, setTab] = useState<SettingsTab>(() => normalizeSettingsTab(initialTab))

  function changeTab(value: string): void {
    const next = TAB_VALUES.includes(value as SettingsTab) ? (value as SettingsTab) : "general"
    setTab(next)
    router.replace(`/settings?tab=${next}`)
  }

  const header = (
    <header className="flex flex-wrap items-start justify-between gap-3">
      <div>
        <p className="text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">
          平台设置
        </p>
        <h1 className="mt-1 text-xl font-semibold text-foreground">设置中心</h1>
        <p className="mt-1 max-w-2xl text-xs leading-relaxed text-muted-foreground">
          管理运行参数、LLM 接入、用户账号与审计日志。设置保存后立即热加载，标注需重启的键在 API
          重启后生效。
        </p>
      </div>
    </header>
  )

  if (user !== null && user.role !== "admin") {
    return (
      <div className="space-y-5">
        {header}
        <Alert tone="warning" title="设置中心仅管理员可用">
          当前账号没有管理员权限。如需调整系统参数、LLM 配置或用户，请联系管理员。
        </Alert>
      </div>
    )
  }

  return (
    <div className="space-y-5">
      {header}

      <Tabs items={TAB_ITEMS} value={tab} onValueChange={changeTab} ariaLabel="设置分类" />

      <section role="tabpanel" aria-label={activeLabel(tab)}>
        {tab === "general" ? <SystemSettings /> : null}
        {tab === "llm" ? <LLMSettings /> : null}
        {tab === "users" ? <UsersPanel /> : null}
        {tab === "audit" ? <AuditPanel /> : null}
      </section>
    </div>
  )
}
