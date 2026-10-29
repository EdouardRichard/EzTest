"use client"

import { Gauge, HardDrive, RefreshCw, Server, ShieldCheck } from "lucide-react"
import { useEffect, useState } from "react"
import { ErrorState } from "@/components/ui/ErrorState"
import { StatusBadge } from "@/components/ui/StatusBadge"
import { apiGet, getApiBaseUrl } from "@/lib/api-client"

/** The subset of `GET /api/health` the read-only config panel displays. */
type Health = {
  status: string
  version: string
  commit: string
  db: string
  llm_configured: boolean
}

type ConfigItem = {
  label: string
  value: string
  hint?: string
}

type ConfigSection = {
  title: string
  icon: typeof Server
  items: ConfigItem[]
}

/** The port of the configured API origin, falling back to the URL defaults. */
function apiPort(): string {
  try {
    const url = new URL(getApiBaseUrl())
    if (url.port !== "") return url.port
    return url.protocol === "https:" ? "443" : "80"
  } catch {
    return "—"
  }
}

/** The environment label of the running web build. */
function environmentLabel(): string {
  return process.env.NEXT_PUBLIC_APP_ENV ?? process.env.NODE_ENV ?? "unknown"
}

/**
 * The read-only configuration projection of spec §8.2. Every value is either a
 * public build/client fact or a documented backend default; secrets, database
 * URLs, tokens and host-absolute store paths are deliberately absent. Values
 * are fixed at process start, so the panel documents that a restart is needed
 * to reload them.
 */
function configSections(health: Health | null): ConfigSection[] {
  const version = health === null ? "读取中…" : health.version
  const commit = health === null ? "—" : health.commit === "" ? "—" : health.commit.slice(0, 7)
  const llm = health === null ? "读取中…" : health.llm_configured ? "已配置" : "未配置"
  const database = health === null ? "读取中…" : health.db === "ok" ? "正常" : "异常"
  return [
    {
      title: "运行环境",
      icon: Server,
      items: [
        { label: "环境", value: environmentLabel(), hint: "构建时确定" },
        { label: "API 地址", value: getApiBaseUrl() },
        { label: "API 端口", value: apiPort() },
        { label: "服务版本", value: version, hint: `提交 ${commit}` },
        { label: "数据库连接", value: database, hint: "仅显示连通状态，不展示连接串" },
        { label: "LLM 配置", value: llm, hint: "仅显示是否已配置，不展示密钥" },
      ],
    },
    {
      title: "限流与保留",
      icon: ShieldCheck,
      items: [
        { label: "限流阈值", value: "50 次/秒 · 突发 100", hint: "EZTEST_RATE_LIMIT_*" },
        { label: "SSE 并发", value: "10 / 用户", hint: "EZTEST_RATE_LIMIT_SSE_MAX" },
        { label: "GC 保留期", value: "30 天", hint: "EZTEST_GC_RETENTION_DAYS" },
        { label: "最近批保护", value: "3 批", hint: "EZTEST_GC_BATCH_KEEP" },
        { label: "定时 GC", value: "默认关闭", hint: "EZTEST_GC_ENABLED；只读投影" },
      ],
    },
    {
      title: "SLO 阈值",
      icon: Gauge,
      items: [
        { label: "首屏 / 链路 Tab P95", value: "≤ 2.5s", hint: "200 节点 / 400 边" },
        { label: "代码变更集 API P95", value: "≤ 2s" },
        { label: "8 并发批次", value: "0 错误" },
      ],
    },
    {
      title: "存储与配置",
      icon: HardDrive,
      items: [
        {
          label: "证据目录",
          value: ".data/evidence",
          hint: "EZTEST_EVIDENCE_DIR（只读投影，不展示主机绝对路径）",
        },
        { label: "报告目录", value: ".data/reports", hint: "EZTEST_REPORT_DIR" },
        { label: "导出目录", value: ".data/exports", hint: "EZTEST_EXPORT_DIR" },
        {
          label: "版本附件目录",
          value: ".data/version-attachments",
          hint: "EZTEST_VERSION_ATTACHMENT_DIR",
        },
      ],
    },
  ]
}

/**
 * Admin basic-configuration tab (spec §8.2): a read-only projection of the
 * environment, build, rate-limit, GC, SLO and storage configuration. There is
 * no editing control and no secret material; changing a value requires
 * restarting the backend process.
 */
export function BasicConfigPanel() {
  const [health, setHealth] = useState<Health | null>(null)
  const [healthError, setHealthError] = useState("")

  useEffect(() => {
    let cancelled = false
    apiGet<Health>("/api/health")
      .then((data) => {
        if (!cancelled) setHealth(data)
      })
      .catch((error: unknown) => {
        if (!cancelled) {
          setHealthError(error instanceof Error ? error.message : "未知错误")
        }
      })
    return () => {
      cancelled = true
    }
  }, [])

  const sections = configSections(health)

  return (
    <section aria-labelledby="basic-config-title" className="space-y-4">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <h2 id="basic-config-title" className="text-base font-semibold">
              基础配置
            </h2>
            <StatusBadge variant="info">只读</StatusBadge>
          </div>
          <p className="mt-1 text-sm leading-relaxed text-muted-foreground">
            当前进程生效的公开配置投影。不展示密钥、凭据或存储主机路径。
          </p>
        </div>
      </header>

      {healthError === "" ? null : (
        <ErrorState
          title="无法读取运行信息"
          message={healthError}
          className="border-warning/40 bg-warning/10 text-warning"
        />
      )}

      <div className="grid gap-3 md:grid-cols-2">
        {sections.map((section) => {
          const Icon = section.icon
          return (
            <article
              key={section.title}
              aria-labelledby={`basic-config-${section.title}`}
              className="rounded-xl border border-border bg-card p-4"
            >
              <h3
                id={`basic-config-${section.title}`}
                className="flex items-center gap-2 text-sm font-semibold"
              >
                <Icon className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
                {section.title}
              </h3>
              <dl className="mt-3 space-y-2.5 text-sm">
                {section.items.map((item) => (
                  <div key={item.label} className="flex items-start justify-between gap-3">
                    <dt className="text-muted-foreground">{item.label}</dt>
                    <dd className="text-right">
                      <span className="font-medium">{item.value}</span>
                      {item.hint === undefined ? null : (
                        <span className="mt-0.5 block font-mono text-[11px] text-muted-foreground">
                          {item.hint}
                        </span>
                      )}
                    </dd>
                  </div>
                ))}
              </dl>
            </article>
          )
        })}
      </div>

      <div className="flex items-start gap-2 rounded-xl border border-border bg-muted/40 px-4 py-3 text-sm text-muted-foreground">
        <RefreshCw className="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
        <p>
          配置在进程启动时加载；修改环境变量后需重启后端服务生效。此处不提供编辑或重新加载按钮。
        </p>
      </div>
    </section>
  )
}
