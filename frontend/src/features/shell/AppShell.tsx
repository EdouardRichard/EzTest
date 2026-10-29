"use client"

import { usePathname } from "next/navigation"
import type { ReactNode } from "react"

import { cn } from "@/lib/utils"

import { Sidebar } from "./Sidebar"
import { Topbar } from "./Topbar"

/** The full-screen mindmap route keeps an edge-to-edge canvas (spec §9.1). */
const FULL_BLEED_PATTERN = /^\/versions\/[^/]+\/mindmap\/?$/

/** Reports whether the route wants the full-width content area, not `max-w-6xl`. */
export function isFullBleedPath(pathname: string): boolean {
  return FULL_BLEED_PATTERN.test(pathname)
}

export function AppShell({ children }: { children: ReactNode }) {
  const pathname = usePathname() ?? ""
  const fullBleed = isFullBleedPath(pathname)

  return (
    <div className="min-h-screen bg-background">
      <a
        href="#main-content"
        className="sr-only focus:not-sr-only focus:absolute focus:left-4 focus:top-4 focus:z-50 focus:rounded-lg focus:border focus:border-border focus:bg-card focus:px-4 focus:py-2 focus:text-sm focus:shadow-lg"
      >
        跳到主要内容
      </a>
      <Sidebar />
      <div className="flex min-h-screen flex-col lg:pl-64">
        <Topbar />
        <main id="main-content" className="flex-1 px-4 py-6 sm:px-6 lg:px-10 lg:py-8">
          <div
            data-testid="app-content"
            data-full-bleed={fullBleed ? "true" : "false"}
            className={cn("mx-auto w-full", fullBleed ? "max-w-none" : "max-w-6xl")}
          >
            {children}
          </div>
        </main>
        <footer className="px-4 pb-6 sm:px-6 lg:px-10">
          <div className="mx-auto flex w-full max-w-6xl flex-wrap items-center justify-between gap-2 text-[11px] text-muted-foreground">
            <span>EzTest · P8</span>
            <span className="font-mono">Next.js 16 · React 19</span>
          </div>
        </footer>
      </div>
    </div>
  )
}
