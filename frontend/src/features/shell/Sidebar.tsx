"use client"

import { FlaskConical } from "lucide-react"
import Link from "next/link"
import { usePathname } from "next/navigation"
import type { ReactNode } from "react"

import { useAuth } from "@/features/auth/AuthProvider"
import { cn } from "@/lib/utils"
import { isActive, NAV_ITEMS, type NavItem } from "@/navigation/nav-items"

/** Admin-only destinations (e.g. the quality centre) are hidden from normal users. */
function isAdminOnly(item: NavItem): boolean {
  return (item as NavItem & { adminOnly?: boolean }).adminOnly === true
}

/** Brand row shared by the fixed desktop sidebar and the mobile drawer. */
export function SidebarHeader({ action }: { action?: ReactNode }) {
  return (
    <div
      className={cn(
        "flex items-center gap-2 border-b border-sidebar-border",
        action !== undefined && "pr-2",
      )}
    >
      <Link
        href="/"
        className="flex min-w-0 flex-1 items-center gap-3 px-5 py-4 transition-colors hover:bg-white/[0.03] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sky-400/60"
      >
        <span className="grid h-10 w-10 place-items-center rounded-xl bg-gradient-to-br from-blue-500 to-sky-400 text-white shadow-lg shadow-blue-900/40">
          <FlaskConical className="h-5 w-5" aria-hidden="true" />
        </span>
        <span className="min-w-0">
          <span className="block text-base font-semibold tracking-tight text-sidebar-foreground">
            EzTest
          </span>
          <span className="block truncate text-[11px] text-sidebar-muted">AI 端到端测试平台</span>
        </span>
      </Link>
      {action}
    </div>
  )
}

type SidebarNavigationProps = {
  /** The drawer enlarges every row to a 44px touch target (spec §8.3). */
  mobile?: boolean
  /** Called after a destination is followed so the drawer can close itself. */
  onNavigate?: () => void
}

/**
 * The blueprint destinations shared by the desktop sidebar and the mobile
 * drawer. Both instances filter the same way, so a normal user never sees an
 * admin-only destination on either layout (spec §8.3/§11.5).
 */
export function SidebarNavigation({ mobile = false, onNavigate }: SidebarNavigationProps) {
  const pathname = usePathname()
  const { user } = useAuth()
  const admin = user?.role === "admin"
  const items = NAV_ITEMS.filter((item) => !isAdminOnly(item) || admin)

  return (
    <>
      <p className="px-2.5 pb-1 text-[10px] font-semibold uppercase tracking-[0.18em] text-sidebar-muted">
        导航
      </p>
      <nav aria-label="主导航" className="flex flex-col gap-1">
        {items.map((item) => {
          const active = isActive(pathname, item.href)
          const Icon = item.icon
          return (
            <Link
              key={item.label}
              href={item.href}
              title={item.hint}
              aria-current={active ? "page" : undefined}
              onClick={onNavigate}
              className={cn(
                "group flex items-center gap-2.5 rounded-lg px-2.5 py-2 text-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sky-400/60",
                mobile && "min-h-11 py-2.5",
                active
                  ? "bg-sidebar-active font-medium text-sidebar-active-foreground"
                  : "text-sidebar-muted hover:bg-white/[0.05] hover:text-sidebar-foreground",
              )}
            >
              <Icon
                aria-hidden="true"
                className={cn("h-4 w-4 shrink-0", active && "text-sky-300")}
              />
              <span className="min-w-0 flex-1 truncate">{item.label}</span>
              <span
                className={cn(
                  "rounded-full border px-1.5 py-0.5 text-[10px] font-medium leading-none",
                  item.phase === "P8"
                    ? "border-emerald-400/25 bg-emerald-400/10 text-emerald-300"
                    : "border-white/10 bg-white/[0.04] text-sidebar-muted",
                )}
              >
                {item.phase}
              </span>
            </Link>
          )
        })}
      </nav>
    </>
  )
}

/** P8 status card pinned to the bottom of both the sidebar and the drawer. */
export function SidebarFooter() {
  return (
    <div className="border-t border-sidebar-border p-4">
      <div className="rounded-xl border border-sidebar-border bg-white/[0.03] p-3">
        <div className="flex items-center gap-2 text-xs font-medium text-sidebar-foreground">
          <span className="relative flex h-2 w-2" aria-hidden="true">
            <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-400 opacity-60" />
            <span className="relative inline-flex h-2 w-2 rounded-full bg-emerald-400" />
          </span>
          P8 · 体验硬化
        </div>
        <p className="mt-1.5 text-[11px] leading-relaxed text-sidebar-muted">
          登录、用户管理、LLM 配置、SSE 事件流与审计已接入。
        </p>
      </div>
    </div>
  )
}

/** Fixed desktop sidebar; under 1024px the menu lives in the `MobileNav` drawer. */
export function Sidebar() {
  return (
    <aside className="fixed inset-y-0 left-0 z-30 hidden w-64 flex-col border-r border-sidebar-border bg-sidebar lg:flex">
      <SidebarHeader />
      <div className="flex min-h-0 flex-1 flex-col gap-1 overflow-y-auto px-3 py-4">
        <SidebarNavigation />
      </div>
      <SidebarFooter />
    </aside>
  )
}
