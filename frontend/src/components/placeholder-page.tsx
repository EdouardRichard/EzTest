import type { ReactNode } from "react"

import { Badge } from "@/components/ui/Badge"
import { EmptyState } from "@/components/ui/EmptyState"

export type PlaceholderPageProps = {
  title: string
  description: string
  phase?: string
  hint?: ReactNode
}

/**
 * Shared global-page placeholder for routes whose real workspace ships after
 * P0. It keeps the navigation honest instead of faking a half-built page.
 */
export function PlaceholderPage({ title, description, phase = "P1", hint }: PlaceholderPageProps) {
  return (
    <div className="space-y-5">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <p className="text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">
            全局工作区
          </p>
          <h1 className="mt-1 text-xl font-semibold text-foreground">{title}</h1>
          <p className="mt-1 max-w-2xl text-xs leading-relaxed text-muted-foreground">
            {description}
          </p>
        </div>
        <Badge tone="warning">{phase} 阶段提供</Badge>
      </header>

      <section className="surface-card p-6">
        <EmptyState
          title={`${title}页面将在 ${phase} 阶段提供`}
          description={
            hint ?? "P0 只交付平台底座：登录、全局壳、资源与版本基础能力；本页先标注后续阶段。"
          }
        />
      </section>
    </div>
  )
}
