"use client"

import type { LucideIcon } from "lucide-react"
import {
  Bot,
  Boxes,
  Bug,
  FileBarChart2,
  FlaskConical,
  GitBranch,
  Layers,
  LayoutDashboard,
  Network,
  Settings,
} from "lucide-react"
import Link from "next/link"
import { usePathname } from "next/navigation"

import { cn } from "@/lib/utils"

type NavItem = {
  label: string
  href: string
  icon: LucideIcon
  phase: string
  hint?: string
}

// The nine blueprint navigation entries. P0 only exposes / and /settings; the
// remaining entries lead to placeholder pages until their phase lands.
const NAV_ITEMS: NavItem[] = [
  { label: "概览", href: "/", icon: LayoutDashboard, phase: "P0", hint: "平台概览与系统状态" },
  { label: "版本", href: "/versions", icon: Layers, phase: "P3", hint: "版本、多需求与澄清（P3）" },
  {
    label: "用例脑图",
    href: "/versions#mindmap",
    icon: Network,
    phase: "P5",
    hint: "脑图随版本详情提供（P5）",
  },
  { label: "问题", href: "/issues", icon: Bug, phase: "P7", hint: "问题中心（P7）" },
  { label: "报告", href: "/reports", icon: FileBarChart2, phase: "P6", hint: "版本报告（P6）" },
  {
    label: "代码仓",
    href: "/code-repos",
    icon: GitBranch,
    phase: "P2",
    hint: "本地 Git 代码仓（P2）",
  },
  { label: "公共资源", href: "/resources", icon: Boxes, phase: "P2", hint: "公共资源管理（P2）" },
  { label: "Agent 中心", href: "/agents", icon: Bot, phase: "P8", hint: "Agent 配置与观测（P8）" },
  { label: "设置", href: "/settings", icon: Settings, phase: "P0", hint: "账户与平台设置" },
]

function isActive(pathname: string, href: string): boolean {
  if (href.includes("#")) return false
  if (href === "/") return pathname === "/"
  return pathname === href || pathname.startsWith(`${href}/`)
}

export function Sidebar() {
  const pathname = usePathname()

  return (
    <aside className="fixed inset-y-0 left-0 z-30 hidden w-64 flex-col border-r border-sidebar-border bg-sidebar lg:flex">
      <Link
        href="/"
        className="flex items-center gap-3 border-b border-sidebar-border px-5 py-4 transition-colors hover:bg-white/[0.03] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sky-400/60"
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

      <div className="flex min-h-0 flex-1 flex-col gap-1 overflow-y-auto px-3 py-4">
        <p className="px-2.5 pb-1 text-[10px] font-semibold uppercase tracking-[0.18em] text-sidebar-muted">
          导航
        </p>
        <nav aria-label="主导航" className="flex flex-col gap-1">
          {NAV_ITEMS.map((item) => {
            const active = isActive(pathname, item.href)
            const Icon = item.icon
            return (
              <Link
                key={item.label}
                href={item.href}
                title={item.hint}
                aria-current={active ? "page" : undefined}
                className={cn(
                  "group flex items-center gap-2.5 rounded-lg px-2.5 py-2 text-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sky-400/60",
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
                    item.phase === "P0"
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
      </div>

      <div className="border-t border-sidebar-border p-4">
        <div className="rounded-xl border border-sidebar-border bg-white/[0.03] p-3">
          <div className="flex items-center gap-2 text-xs font-medium text-sidebar-foreground">
            <span className="relative flex h-2 w-2" aria-hidden="true">
              <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-400 opacity-60" />
              <span className="relative inline-flex h-2 w-2 rounded-full bg-emerald-400" />
            </span>
            P0 地基运行中
          </div>
          <p className="mt-1.5 text-[11px] leading-relaxed text-sidebar-muted">
            登录、请求封装与应用壳已接入；设置页当前为骨架。
          </p>
        </div>
      </div>
    </aside>
  )
}
