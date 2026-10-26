"use client"

import * as Tabs from "@radix-ui/react-tabs"
import type { LucideIcon } from "lucide-react"
import { KeyRound, RadioTower, Users } from "lucide-react"

import { LLMPanel } from "@/features/llm-settings/LLMPanel"
import { SSETestPanel } from "@/features/sse-test/SSETestPanel"
import { UsersPanel } from "@/features/users/UsersPanel"

type SettingsTab = {
  value: string
  label: string
  icon: LucideIcon
}

const TABS: SettingsTab[] = [
  { value: "users", label: "用户", icon: Users },
  { value: "llm", label: "LLM", icon: KeyRound },
  { value: "sse", label: "SSE 测试", icon: RadioTower },
]

/**
 * The admin-only settings tabs. Inactive Radix content is unmounted, which also
 * tears down the SSE test connection when the admin leaves that tab.
 */
export function SettingsTabs() {
  return (
    <section className="rounded-2xl border border-border bg-card p-2 shadow-sm sm:p-3">
      <Tabs.Root defaultValue="users">
        <Tabs.List aria-label="设置分类" className="grid grid-cols-3 gap-1 rounded-xl bg-muted p-1">
          {TABS.map((tab) => {
            const Icon = tab.icon
            return (
              <Tabs.Trigger
                key={tab.value}
                value={tab.value}
                className="flex items-center justify-center gap-2 rounded-lg px-3 py-2 text-sm font-medium text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring data-[state=active]:bg-card data-[state=active]:text-foreground data-[state=active]:shadow-sm"
              >
                <Icon className="h-4 w-4" aria-hidden="true" />
                {tab.label}
              </Tabs.Trigger>
            )
          })}
        </Tabs.List>

        <Tabs.Content value="users" className="p-3 sm:p-4">
          <UsersPanel />
        </Tabs.Content>
        <Tabs.Content value="llm" className="p-3 sm:p-4">
          <LLMPanel />
        </Tabs.Content>
        <Tabs.Content value="sse" className="p-3 sm:p-4">
          <SSETestPanel />
        </Tabs.Content>
      </Tabs.Root>
    </section>
  )
}
