import Link from "next/link"

import { StatusBadge } from "@/components/ui/StatusBadge"

import type { Version, VersionResource } from "./types"

function bindingLabel(binding: VersionResource | null): string {
  if (binding === null) return "未绑定"
  return binding.available ? binding.name : `${binding.name}（已删除）`
}

/** Skeleton content for later-phase areas that P3 deliberately leaves empty. */
const FUTURE_SECTIONS = [
  {
    title: "执行批次",
    phase: "P6",
    description: "暂无批次数据，执行引擎在 P6 阶段接入。",
  },
  {
    title: "问题",
    phase: "P7",
    description: "暂无问题，问题中心在 P7 阶段接入。",
  },
] as const

export type OverviewTabProps = {
  version: Version
}

/**
 * Version overview skeleton (spec §9.2): the four real version counters
 * (requirements, open questions, relations, bindings) plus clearly-labelled
 * empty areas for batches/cases/issues. It never invents values the version
 * DTO does not provide.
 */
export function OverviewTab({ version }: OverviewTabProps) {
  const unclarifiedCount = Math.max(version.requirementCount - version.clarifiedCount, 0)

  return (
    <div className="space-y-4">
      <section aria-label="版本指标" className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <article className="rounded-2xl border border-border bg-card p-4">
          <h2 className="text-sm font-medium">需求</h2>
          <p className="mt-2 text-2xl font-semibold tracking-tight">{version.requirementCount}</p>
          <p className="mt-1 text-xs text-muted-foreground">
            已确认 {version.clarifiedCount} · 未澄清 {unclarifiedCount}
          </p>
        </article>

        <article className="rounded-2xl border border-border bg-card p-4">
          <h2 className="text-sm font-medium">待澄清问题</h2>
          <p className="mt-2 text-2xl font-semibold tracking-tight">{version.openQuestionCount}</p>
          <p className="mt-1 text-xs text-muted-foreground">问题清单在「需求与澄清」工作台维护。</p>
        </article>

        <article className="rounded-2xl border border-border bg-card p-4">
          <h2 className="text-sm font-medium">关系</h2>
          <p className="mt-2 text-2xl font-semibold tracking-tight text-muted-foreground">—</p>
          <p className="mt-1 text-xs text-muted-foreground">
            关系数量由需求工作台加载，总览不做推测。
          </p>
        </article>

        <article className="rounded-2xl border border-border bg-card p-4">
          <h2 className="text-sm font-medium">资源绑定</h2>
          <dl className="mt-2 space-y-1 text-xs">
            <div className="flex gap-2">
              <dt className="shrink-0 text-muted-foreground">系统</dt>
              <dd className="break-all">{bindingLabel(version.bindings.system)}</dd>
            </div>
            <div className="flex gap-2">
              <dt className="shrink-0 text-muted-foreground">环境</dt>
              <dd className="break-all">{bindingLabel(version.bindings.environment)}</dd>
            </div>
            <div className="flex gap-2">
              <dt className="shrink-0 text-muted-foreground">代码仓</dt>
              <dd className="break-all">{bindingLabel(version.bindings.codeRepository)}</dd>
            </div>
          </dl>
        </article>
      </section>

      <section aria-label="后续阶段空态" className="grid gap-3 lg:grid-cols-3">
        <Link
          href={`/versions/${version.id}/mindmap`}
          data-testid="overview-open-mindmap"
          className="group rounded-2xl border border-border bg-card p-4 transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="text-sm font-medium">用例脑图</h2>
            <StatusBadge variant="info">P5</StatusBadge>
          </div>
          <p className="mt-2 text-xs leading-relaxed text-muted-foreground">
            打开用例脑图：按已确认需求生成、编辑并评审用例。
          </p>
          <span className="mt-2 inline-flex items-center gap-1 text-xs font-medium text-primary">
            打开脑图 →
          </span>
        </Link>

        {FUTURE_SECTIONS.map((section) => (
          <article
            key={section.title}
            className="rounded-2xl border border-dashed border-border bg-card/60 p-4"
          >
            <div className="flex flex-wrap items-center gap-2">
              <h2 className="text-sm font-medium">{section.title}</h2>
              <StatusBadge variant="neutral">{section.phase}</StatusBadge>
            </div>
            <p className="mt-2 text-xs leading-relaxed text-muted-foreground">
              {section.description}
            </p>
          </article>
        ))}
      </section>
    </div>
  )
}
