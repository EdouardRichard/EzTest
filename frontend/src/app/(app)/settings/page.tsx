"use client"

import * as Tabs from "@radix-ui/react-tabs"
import type { LucideIcon } from "lucide-react"
import { KeyRound, RadioTower, Settings2, Users } from "lucide-react"

import { useAuth } from "@/features/auth/AuthProvider"
import { EmptyState } from "@/features/shell/EmptyState"
import { StatusBadge } from "@/features/shell/StatusBadge"

type SettingsTab = {
  value: string
  label: string
  icon: LucideIcon
  title: string
  description: string
}

const TABS: SettingsTab[] = [
  {
    value: "users",
    label: "用户",
    icon: Users,
    title: "用户管理",
    description: "用户列表、创建、禁用与重置密码将在设置页后续任务中接入。",
  },
  {
    value: "llm",
    label: "LLM",
    icon: KeyRound,
    title: "LLM 配置",
    description: "模型配置、上下文大小、目录刷新与多模态测试将在设置页后续任务中接入。",
  },
  {
    value: "sse",
    label: "SSE 测试",
    icon: RadioTower,
    title: "SSE 联通性测试",
    description: "浏览器 SSE 客户端、连接状态、最后 seq 与测试事件发送将在后续任务中接入。",
  },
]

export default function SettingsPage() {
  const { user } = useAuth()
  const isAdmin = user?.role === "admin"

  return (
    <div className="space-y-6">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <h1 className="text-2xl font-semibold tracking-tight">设置</h1>
            <StatusBadge variant="info">P0 · 骨架</StatusBadge>
          </div>
          <p className="mt-1 text-sm text-muted-foreground">
            账户信息、用户管理、LLM 配置与 SSE 联通性测试。
          </p>
        </div>
      </header>

      <section className="rounded-2xl border border-border bg-card p-5 shadow-sm">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex items-center gap-3">
            <span className="grid h-10 w-10 place-items-center rounded-xl bg-primary/10 text-sm font-semibold text-primary">
              {(user?.username ?? "?").slice(0, 1).toUpperCase()}
            </span>
            <div>
              <p className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
                当前账户
              </p>
              <p className="text-lg font-semibold leading-tight">{user?.username ?? "—"}</p>
            </div>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <StatusBadge variant={isAdmin ? "success" : "neutral"}>
              {isAdmin ? "管理员" : "普通用户"}
            </StatusBadge>
            <StatusBadge variant={user?.status === "active" ? "success" : "danger"}>
              {user?.status === "active" ? "活跃" : "已禁用"}
            </StatusBadge>
          </div>
        </div>
      </section>

      {isAdmin ? (
        <section className="rounded-2xl border border-border bg-card p-2 shadow-sm sm:p-3">
          <Tabs.Root defaultValue="users">
            <Tabs.List
              aria-label="设置分类"
              className="grid grid-cols-3 gap-1 rounded-xl bg-muted p-1"
            >
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

            {TABS.map((tab) => (
              <Tabs.Content key={tab.value} value={tab.value} className="p-3 sm:p-4">
                <EmptyState icon={tab.icon} title={tab.title} description={tab.description} />
              </Tabs.Content>
            ))}
          </Tabs.Root>
        </section>
      ) : (
        <section className="rounded-2xl border border-border bg-card p-5 shadow-sm">
          <div className="flex items-start gap-3">
            <Settings2
              className="mt-0.5 h-5 w-5 shrink-0 text-muted-foreground"
              aria-hidden="true"
            />
            <div>
              <h2 className="text-base font-semibold">普通用户设置</h2>
              <p className="mt-1 text-sm leading-relaxed text-muted-foreground">
                普通用户仅可查看账户信息与主题设置；用户、LLM 与 SSE 测试面板仅管理员可见。
                完整的账户设置将在后续任务补齐。
              </p>
            </div>
          </div>
        </section>
      )}
    </div>
  )
}
