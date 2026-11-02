"use client"

import Link from "next/link"
import { usePathname } from "next/navigation"

import { cn } from "@/components/ui/cn"
import { isNavItemActive, NAV_ITEMS, NAV_SECTIONS } from "@/navigation/nav-items"

/** Dark product sidebar: workspace/platform sections plus the environment note. */
export function Sidebar() {
  const pathname = usePathname()

  return (
    <aside className="hidden w-60 shrink-0 flex-col border-r border-sidebar-border bg-sidebar text-sidebar-foreground md:flex">
      <div className="flex items-center gap-3 px-4 py-5">
        <span className="flex size-9 items-center justify-center rounded-lg bg-white/10 text-xs font-bold tracking-wide text-white">
          EZ
        </span>
        <span className="min-w-0">
          <span className="block text-sm font-semibold text-white">EzTest</span>
          <span className="block text-[11px] text-sidebar-muted">AI 驱动端到端测试</span>
        </span>
      </div>

      <nav aria-label="主导航" className="flex-1 space-y-5 overflow-y-auto px-3 pb-4">
        {NAV_SECTIONS.map((section) => (
          <div key={section.id} className="space-y-1">
            <p className="px-3 pb-1 text-[10px] font-semibold uppercase tracking-[0.14em] text-sidebar-muted">
              {section.label}
            </p>
            {NAV_ITEMS.filter((item) => item.section === section.id).map((item) => {
              const active = isNavItemActive(pathname, item)
              const Icon = item.icon
              return (
                <Link
                  key={item.key}
                  href={item.href}
                  aria-current={active ? "page" : undefined}
                  data-active={active}
                  className={cn(
                    "flex items-center gap-2.5 rounded-lg px-3 py-2 text-xs font-medium transition-colors",
                    "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                    active
                      ? "bg-sidebar-active text-sidebar-active-foreground"
                      : "text-sidebar-foreground hover:bg-white/5 hover:text-white",
                  )}
                >
                  <Icon className="size-4 shrink-0" aria-hidden="true" />
                  <span className="flex-1 truncate">{item.label}</span>
                  {item.placeholder && item.phase ? (
                    <span className="rounded bg-white/10 px-1.5 py-0.5 text-[9px] font-semibold text-sidebar-muted">
                      {item.phase}
                    </span>
                  ) : null}
                </Link>
              )
            })}
          </div>
        ))}
      </nav>

      <div className="border-t border-sidebar-border px-4 py-4 text-[10px] leading-relaxed text-sidebar-muted">
        <p className="font-medium text-sidebar-foreground">开发测试环境 · 受控网络</p>
        <p>AI 可读取测试账号凭据</p>
      </div>
    </aside>
  )
}
