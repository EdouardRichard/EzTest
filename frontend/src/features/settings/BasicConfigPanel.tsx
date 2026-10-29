"use client"

import {
  AlertTriangle,
  Gauge,
  HardDrive,
  RefreshCw,
  Server,
  ShieldCheck,
  Trash2,
} from "lucide-react"
import { useEffect, useRef, useState } from "react"
import { ErrorState } from "@/components/ui/ErrorState"
import { StatusBadge } from "@/components/ui/StatusBadge"
import { apiGet, apiPost, getApiBaseUrl } from "@/lib/api-client"

/** The subset of `GET /api/health` the read-only config panel displays. */
type Health = {
  status: string
  version: string
  commit: string
  db: string
  llm_configured: boolean
  rate_limit?: {
    rps: number
    burst: number
    sse_max: number
  }
  gc?: {
    enabled: boolean
    retention_days: number
    batch_keep: number
  }
}

type ConfigItem = {
  label: string
  value: string
  hint?: string
  /** True when the panel can only document the default, not read the effective value. */
  defaultValue?: boolean
}

type ConfigSection = {
  title: string
  icon: typeof Server
  items: ConfigItem[]
}

/** The `POST /api/admin/gc` report (spec §10.2). */
type GCReport = {
  dryRun: boolean
  candidates: number
  protected: number
  bytes: number
  deleted: number
  failed: number
  skipped: number
  retentionDays: number
  batchKeep: number
  /** Per-run candidate cap (GCCandidateLimit); hitting it means "run again". */
  candidateLimit: number
  runAt?: string
}

/** Formats a byte count with binary units for the GC report. */
function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 B"
  const units = ["B", "KiB", "MiB", "GiB"]
  const exponent = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  const value = bytes / 1024 ** exponent
  return `${value.toFixed(exponent === 0 ? 0 : 1)} ${units[exponent]}`
}

/** Reads the HTTP status of an API error without exposing the raw response. */
function errorStatus(error: unknown): number {
  if (typeof error !== "object" || error === null || !("status" in error)) return 0
  const status = (error as { status?: unknown }).status
  return typeof status === "number" ? status : 0
}

/**
 * The GC failure copy: 403 means the caller is not an admin (the endpoint is
 * admin-only) and 409 means execution is disabled; a generic message must not
 * mislabel either as a hash/file failure.
 */
function gcErrorMessage(error: unknown): string {
  const message = error instanceof Error ? error.message : "未知错误"
  const status = errorStatus(error)
  if (status === 403) return `仅管理员可执行 GC（403）：${message}`
  if (status === 409) return `${message}（409：非 dry-run 需要 EZTEST_GC_ENABLED=true）`
  return message
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
  // The effective thresholds come from /api/health; the documented defaults are
  // shown until the probe answers and remain visible when it is unreachable.
  const rateLimit = health?.rate_limit
  const rateLimitValue =
    rateLimit === undefined
      ? "50 次/秒 · 突发 100"
      : rateLimit.rps === 0
        ? "已关闭"
        : `${rateLimit.rps} 次/秒 · 突发 ${rateLimit.burst}`
  const sseValue = rateLimit === undefined ? "10 / 用户" : `${rateLimit.sse_max} / 用户`
  const gc = health?.gc
  const retentionValue = gc === undefined ? "30 天" : `${gc.retention_days} 天`
  const batchKeepValue = gc === undefined ? "3 批" : `${gc.batch_keep} 批`
  const gcState = gc === undefined ? "默认关闭" : gc.enabled ? "允许非 dry-run" : "仅 dry-run"
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
        { label: "限流阈值", value: rateLimitValue, hint: "EZTEST_RATE_LIMIT_RPS / _BURST" },
        { label: "SSE 并发", value: sseValue, hint: "EZTEST_RATE_LIMIT_SSE_MAX" },
        { label: "GC 保留期", value: retentionValue, hint: "EZTEST_GC_RETENTION_DAYS" },
        { label: "最近批保护", value: batchKeepValue, hint: "EZTEST_GC_BATCH_KEEP" },
        { label: "GC 执行模式", value: gcState, hint: "EZTEST_GC_ENABLED；只读投影" },
      ],
    },
    {
      title: "SLO 阈值",
      icon: Gauge,
      items: [
        {
          label: "首屏 / 链路 Tab P95",
          value: "≤ 2.5s",
          hint: "200 节点 / 400 边；默认值，以服务端配置为准",
          defaultValue: true,
        },
        {
          label: "代码变更集 API P95",
          value: "≤ 2s",
          hint: "默认值，以服务端配置为准",
          defaultValue: true,
        },
        {
          label: "8 并发批次",
          value: "0 错误",
          hint: "默认值，以服务端配置为准",
          defaultValue: true,
        },
      ],
    },
    {
      title: "存储与配置",
      icon: HardDrive,
      items: [
        {
          label: "证据目录",
          value: ".data/evidence",
          hint: "EZTEST_EVIDENCE_DIR；默认值，以服务端配置为准，不展示主机绝对路径",
          defaultValue: true,
        },
        {
          label: "报告目录",
          value: ".data/reports",
          hint: "EZTEST_REPORT_DIR；默认值，以服务端配置为准",
          defaultValue: true,
        },
        {
          label: "导出目录",
          value: ".data/exports",
          hint: "EZTEST_EXPORT_DIR；默认值，以服务端配置为准",
          defaultValue: true,
        },
        {
          label: "版本附件目录",
          value: ".data/version-attachments",
          hint: "EZTEST_VERSION_ATTACHMENT_DIR；默认值，以服务端配置为准",
          defaultValue: true,
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
  const [gcReport, setGcReport] = useState<GCReport | null>(null)
  const [gcError, setGcError] = useState("")
  const [gcLoading, setGcLoading] = useState(false)
  const [gcConfirming, setGcConfirming] = useState(false)
  const executeButtonRef = useRef<HTMLButtonElement>(null)
  const confirmCancelRef = useRef<HTMLButtonElement>(null)
  const wasConfirmingRef = useRef(false)

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

  // Alertdialog focus management: move focus to the least destructive action
  // (取消) when the confirmation opens, and back to 执行 GC when it closes.
  useEffect(() => {
    if (gcConfirming) {
      wasConfirmingRef.current = true
      confirmCancelRef.current?.focus()
      return
    }
    if (wasConfirmingRef.current) {
      wasConfirmingRef.current = false
      executeButtonRef.current?.focus()
    }
  }, [gcConfirming])

  // A purge may only be started from a fresh dry-run preview with candidates;
  // after a real run the button stays disabled until the next dry-run so the
  // destructive action is never repeated blindly.
  const gcCanExecute = gcReport !== null && gcReport.dryRun && gcReport.candidates > 0

  /** Runs the admin GC endpoint. dryRun=true previews; false purges. */
  async function runGC(dryRun: boolean) {
    setGcLoading(true)
    setGcError("")
    try {
      const report = await apiPost<GCReport>("/api/admin/gc", {
        dryRun,
        reason: "settings panel",
      })
      setGcReport(report)
      setGcConfirming(false)
    } catch (error: unknown) {
      setGcError(gcErrorMessage(error))
      setGcConfirming(false)
    } finally {
      setGcLoading(false)
    }
  }

  const sections = configSections(health)

  return (
    <section aria-labelledby="basic-config-title" className="space-y-4">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <h2 id="basic-config-title" className="text-base font-semibold">
              基础配置
            </h2>
            <StatusBadge variant="info">配置只读</StatusBadge>
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
                      {item.defaultValue ? (
                        <span className="ml-1 text-[11px] text-muted-foreground">（默认值）</span>
                      ) : null}
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

      <section
        aria-labelledby="gc-maintenance-title"
        className="rounded-xl border border-border bg-card p-4"
      >
        <header className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <div className="flex items-center gap-2">
              <h3
                id="gc-maintenance-title"
                className="flex items-center gap-2 text-sm font-semibold"
              >
                <Trash2 className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
                证据 GC
              </h3>
              <StatusBadge variant="info">管理员</StatusBadge>
            </div>
            <p className="mt-1 text-sm leading-relaxed text-muted-foreground">
              先 dry-run 预览超期且未被引用的候选；执行会二次确认，且仅当后端
              <span className="font-mono"> EZTEST_GC_ENABLED=true </span>
              时才会删除文件。当前自动清理仅覆盖执行证据文件（不含导出、附件、报告文件与
              孤儿文件）；未关闭问题、最近批次、基线证据与运行中批次始终受保护。
            </p>
          </div>
          <div className="flex flex-wrap gap-2">
            <button
              type="button"
              onClick={() => runGC(true)}
              disabled={gcLoading}
              className="rounded-lg border border-border px-3 py-2 text-sm font-medium hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
            >
              GC dry-run 预览
            </button>
            <button
              ref={executeButtonRef}
              type="button"
              onClick={() => setGcConfirming(true)}
              disabled={gcLoading || !gcCanExecute}
              className="rounded-lg bg-destructive px-3 py-2 text-sm font-medium text-destructive-foreground hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
            >
              执行 GC
            </button>
          </div>
        </header>

        {gcConfirming ? (
          <div
            role="alertdialog"
            aria-labelledby="gc-confirm-title"
            aria-describedby="gc-confirm-desc"
            onKeyDown={(event) => {
              if (event.key === "Escape" && !gcLoading) {
                event.preventDefault()
                setGcConfirming(false)
              }
            }}
            className="mt-3 rounded-lg border border-warning/40 bg-warning/10 p-3 text-sm"
          >
            <p id="gc-confirm-title" className="flex items-start gap-2 font-medium">
              <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
              确认执行 GC？将删除候选证据文件并保留数据库元数据，此操作不可撤销。
            </p>
            <p id="gc-confirm-desc" className="mt-1 text-muted-foreground">
              只会删除本次 dry-run 预览到的执行证据文件；取消不会发起任何请求。
            </p>
            <div className="mt-2 flex gap-2">
              <button
                type="button"
                onClick={() => runGC(false)}
                disabled={gcLoading}
                className="rounded-lg bg-destructive px-3 py-1.5 text-sm font-medium text-destructive-foreground hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
              >
                确认执行
              </button>
              <button
                ref={confirmCancelRef}
                type="button"
                onClick={() => setGcConfirming(false)}
                disabled={gcLoading}
                className="rounded-lg border border-border px-3 py-1.5 text-sm font-medium hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                取消
              </button>
            </div>
          </div>
        ) : null}

        {gcError === "" ? null : (
          <ErrorState
            title="GC 请求失败"
            message={gcError}
            className="mt-3 border-warning/40 bg-warning/10 text-warning"
          />
        )}

        {gcReport === null ? null : (
          <dl className="mt-3 grid gap-2 text-sm sm:grid-cols-2 lg:grid-cols-4">
            <div className="rounded-lg bg-muted/40 px-3 py-2">
              <dt className="text-muted-foreground">候选</dt>
              <dd className="font-medium">候选 {gcReport.candidates} 个</dd>
            </div>
            <div className="rounded-lg bg-muted/40 px-3 py-2">
              <dt className="text-muted-foreground">体积</dt>
              <dd className="font-medium">{formatBytes(gcReport.bytes)}</dd>
            </div>
            <div className="rounded-lg bg-muted/40 px-3 py-2">
              <dt className="text-muted-foreground">受保护</dt>
              <dd className="font-medium">受保护 {gcReport.protected} 个</dd>
            </div>
            <div className="rounded-lg bg-muted/40 px-3 py-2">
              <dt className="text-muted-foreground">{gcReport.dryRun ? "模式" : "结果"}</dt>
              <dd className="font-medium">
                {gcReport.dryRun ? "dry-run（未删除）" : `已删除 ${gcReport.deleted} 个`}
                {gcReport.dryRun || (gcReport.failed === 0 && gcReport.skipped === 0)
                  ? null
                  : ` · 失败 ${gcReport.failed} · 跳过 ${gcReport.skipped}`}
              </dd>
            </div>
          </dl>
        )}
        {gcReport === null ? null : (
          <p className="mt-2 text-xs text-muted-foreground">
            单次最多处理 {gcReport.candidateLimit} 个候选；达到上限时请再次运行。
            {gcReport.dryRun ? null : " 本次已执行，再次执行前请重新 dry-run 预览。"}
          </p>
        )}
      </section>

      <div className="flex items-start gap-2 rounded-xl border border-border bg-muted/40 px-4 py-3 text-sm text-muted-foreground">
        <RefreshCw className="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
        <p>
          配置在进程启动时加载；修改环境变量后需重启后端服务生效。此处不提供编辑或重新加载按钮。
        </p>
      </div>
    </section>
  )
}
