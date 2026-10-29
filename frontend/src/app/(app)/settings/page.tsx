"use client"

import { Monitor, Moon, Settings2, Sun } from "lucide-react"
import { useTheme } from "next-themes"
import { useEffect, useState } from "react"
import { StatusBadge } from "@/components/ui/StatusBadge"
import { useAuth } from "@/features/auth/AuthProvider"
import { SettingsTabs } from "@/features/settings/SettingsTabs"

const THEME_OPTIONS = [
  { value: "light", label: "浅色", icon: Sun },
  { value: "dark", label: "深色", icon: Moon },
  { value: "system", label: "跟随系统", icon: Monitor },
] as const

/**
 * Local theme preference for normal users. It is intentionally rendered only
 * on the non-admin branch: admins already have the topbar theme toggle and the
 * admin settings page is reserved for the three management tabs.
 */
function ThemeSettings() {
  const { theme, setTheme } = useTheme()
  const [mounted, setMounted] = useState(false)

  useEffect(() => setMounted(true), [])

  const current = mounted ? (theme ?? "system") : "system"

  return (
    <section className="rounded-2xl border border-border bg-card p-5 shadow-sm">
      <div className="flex items-start gap-3">
        <Settings2 className="mt-0.5 h-5 w-5 shrink-0 text-muted-foreground" aria-hidden="true" />
        <div className="w-full">
          <h2 className="text-base font-semibold">外观</h2>
          <p className="mt-1 text-sm leading-relaxed text-muted-foreground">
            主题偏好仅保存在当前浏览器，不影响其他用户。
          </p>
          <fieldset className="mt-4 grid gap-2 border-0 p-0 sm:grid-cols-3">
            <legend className="sr-only">主题偏好</legend>
            {THEME_OPTIONS.map((option) => {
              const Icon = option.icon
              const active = current === option.value
              return (
                <button
                  key={option.value}
                  type="button"
                  onClick={() => setTheme(option.value)}
                  aria-pressed={active}
                  className={
                    active
                      ? "flex items-center justify-center gap-2 rounded-xl border border-primary/40 bg-primary/10 px-3 py-2 text-sm font-medium text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                      : "flex items-center justify-center gap-2 rounded-xl border border-border bg-card px-3 py-2 text-sm font-medium text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  }
                >
                  <Icon className="h-4 w-4" aria-hidden="true" />
                  {option.label}
                </button>
              )
            })}
          </fieldset>
        </div>
      </div>
    </section>
  )
}

export default function SettingsPage() {
  const { user } = useAuth()
  const isAdmin = user?.role === "admin"

  return (
    <div className="space-y-6">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <h1 className="text-2xl font-semibold tracking-tight">设置</h1>
            <StatusBadge variant="info">P8</StatusBadge>
          </div>
          <p className="mt-1 text-sm text-muted-foreground">
            账户信息、主题偏好，以及管理员可见的用户、LLM 与 SSE 测试面板。
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

      {isAdmin ? <SettingsTabs /> : <ThemeSettings />}
    </div>
  )
}
