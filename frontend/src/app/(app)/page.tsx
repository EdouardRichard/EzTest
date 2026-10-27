"use client"

import type { LucideIcon } from "lucide-react"
import {
  ArrowRight,
  Bot,
  Database,
  Layers,
  Loader2,
  RadioTower,
  RefreshCw,
  Server,
} from "lucide-react"
import Link from "next/link"
import { useCallback, useEffect, useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"
import { ApiError, apiGet, getApiBaseUrl } from "@/lib/api-client"

type HealthData = {
  status: string
  version: string
  commit: string
  db: string
  llm_configured: boolean
}

type ServiceCard = {
  title: string
  icon: LucideIcon
  label: string
  detail: string
  variant: StatusBadgeVariant
  /** Optional call to action; the SSE card points at its settings panel. */
  href?: string
}

export default function OverviewPage() {
  const [health, setHealth] = useState<HealthData | null>(null)
  const [error, setError] = useState<ApiError | null>(null)
  const [loading, setLoading] = useState(true)

  const loadHealth = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const data = await apiGet<HealthData>("/api/health", { token: null })
      setHealth(data)
    } catch (cause) {
      setHealth(null)
      setError(
        cause instanceof ApiError
          ? cause
          : new ApiError({
              status: 0,
              code: "network_error",
              message: "无法连接 API 服务",
              retryable: true,
            }),
      )
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void loadHealth()
  }, [loadHealth])

  const apiCard: ServiceCard = error
    ? error.status === 503
      ? {
          title: "API 服务",
          icon: Server,
          label: "降级",
          detail: "API 可达，但数据库健康检查失败。",
          variant: "warning",
        }
      : {
          title: "API 服务",
          icon: Server,
          label: "不可达",
          detail:
            error.code === "network_error"
              ? `无法连接后端，请确认 API 已在 ${getApiBaseUrl()} 启动。`
              : `后端返回 HTTP ${error.status}。`,
          variant: "danger",
        }
    : {
        title: "API 服务",
        icon: Server,
        label: loading ? "检查中" : "正常",
        detail: health?.version ? `版本 ${health.version}` : "已连接本地 API。",
        variant: loading ? "neutral" : "success",
      }

  const databaseCard: ServiceCard = health
    ? {
        title: "数据库",
        icon: Database,
        label: health.db === "ok" ? "正常" : "异常",
        detail: health.db === "ok" ? "PostgreSQL 连接正常。" : "PostgreSQL 健康检查失败。",
        variant: health.db === "ok" ? "success" : "danger",
      }
    : {
        title: "数据库",
        icon: Database,
        label: error?.status === 503 ? "异常" : "未知",
        detail:
          error?.status === 503 ? "API 健康检查报告数据库不可用。" : "等待 API 健康检查返回。",
        variant: error?.status === 503 ? "danger" : "neutral",
      }

  const llmCard: ServiceCard = health
    ? {
        title: "LLM",
        icon: Bot,
        label: health.llm_configured ? "已配置" : "未配置",
        detail: health.llm_configured
          ? "已存在可解密的默认 LLM 配置。"
          : "管理员可在设置页配置 LLM 连接、模型与 API Key。",
        variant: health.llm_configured ? "success" : "warning",
      }
    : {
        title: "LLM",
        icon: Bot,
        label: "未知",
        detail: "等待 API 健康检查返回。",
        variant: "neutral",
      }

  const cards: ServiceCard[] = [
    apiCard,
    databaseCard,
    llmCard,
    {
      title: "SSE 事件流",
      icon: RadioTower,
      label: "已接入",
      detail: "管理员可在设置页的 SSE 测试面板连接 system 频道并发布测试事件。",
      variant: "success",
      href: "/settings",
    },
  ]

  return (
    <div className="space-y-6">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <h1 className="text-2xl font-semibold tracking-tight">概览</h1>
            <StatusBadge variant="info">P0 地基</StatusBadge>
          </div>
          <p className="mt-1 text-sm text-muted-foreground">
            平台与依赖的实时健康状态，以及当前阶段的交付边界。
          </p>
        </div>
        <button
          type="button"
          onClick={() => void loadHealth()}
          disabled={loading}
          className="inline-flex h-9 items-center gap-2 rounded-lg border border-border bg-card px-3 text-sm font-medium transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-60"
        >
          {loading ? (
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
          ) : (
            <RefreshCw className="h-4 w-4" aria-hidden="true" />
          )}
          刷新状态
        </button>
      </header>

      <section aria-label="系统状态" className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        {cards.map((card) => {
          const Icon = card.icon
          return (
            <article
              key={card.title}
              className="rounded-2xl border border-border bg-card p-4 shadow-sm"
            >
              <div className="flex items-center justify-between gap-2">
                <span className="grid h-9 w-9 place-items-center rounded-xl bg-muted text-muted-foreground">
                  <Icon className="h-4 w-4" aria-hidden="true" />
                </span>
                <StatusBadge variant={card.variant}>{card.label}</StatusBadge>
              </div>
              <h2 className="mt-3 text-sm font-medium">{card.title}</h2>
              <p className="mt-1 text-xs leading-relaxed text-muted-foreground">{card.detail}</p>
              {card.href ? (
                <Link
                  href={card.href}
                  className="mt-3 inline-flex items-center gap-1 text-xs font-medium text-sky-600 transition-colors hover:text-sky-500 dark:text-sky-400"
                >
                  前往设置
                  <ArrowRight className="h-3 w-3" aria-hidden="true" />
                </Link>
              ) : null}
            </article>
          )
        })}
      </section>

      <EmptyState
        icon={Layers}
        title="版本与澄清工作台已开放"
        description="创建版本、绑定应用与代码仓库、编写需求与附件、发起 AI 澄清并确认需求。用例脑图、执行中心与报告会按后续阶段逐步接入。"
        action={
          <Link
            href="/versions"
            className="inline-flex h-9 items-center gap-2 rounded-lg bg-primary px-3 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90"
          >
            前往版本工作台
            <ArrowRight className="h-3 w-3" aria-hidden="true" />
          </Link>
        }
      />
    </div>
  )
}
