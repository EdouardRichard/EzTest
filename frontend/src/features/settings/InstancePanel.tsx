"use client"

import { Server } from "lucide-react"
import { useCallback, useEffect, useState } from "react"

import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { Table, type TableColumn } from "@/components/ui/Table"
import { useAuth } from "@/features/auth/AuthProvider"
import { formatDateTime } from "@/lib/format"

import {
  activateInstance,
  drainInstance,
  type Instance,
  type InstanceStatus,
  listInstances,
} from "./api"

const INSTANCE_STATUS_TONE: Record<InstanceStatus, BadgeTone> = {
  active: "success",
  draining: "warning",
  offline: "danger",
  stopped: "neutral",
}

const INSTANCE_STATUS_LABELS: Record<InstanceStatus, string> = {
  active: "运行中",
  draining: "排空中",
  offline: "离线",
  stopped: "已停止",
}

/** Admin-only API instance roster with drain/activate (contract p7-api §6.3). */
export function InstancePanel() {
  const { user } = useAuth()
  const isAdmin = user?.role === "admin"

  const [instances, setInstances] = useState<Instance[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<unknown>(null)
  const [actionError, setActionError] = useState<unknown>(null)
  const [pendingId, setPendingId] = useState<string | null>(null)
  const [reloadKey, setReloadKey] = useState(0)

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadKey forces a refetch
  useEffect(() => {
    if (!isAdmin) {
      setLoading(false)
      return undefined
    }
    let cancelled = false
    setLoading(true)
    setError(null)
    void listInstances()
      .then((page) => {
        if (!cancelled) setInstances(page.items)
      })
      .catch((cause: unknown) => {
        if (!cancelled) setError(cause)
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [reloadKey, isAdmin])

  const reload = useCallback(() => setReloadKey((key) => key + 1), [])

  async function transition(instance: Instance, action: "drain" | "activate"): Promise<void> {
    setPendingId(instance.id)
    setActionError(null)
    try {
      if (action === "drain") {
        await drainInstance(instance.id)
      } else {
        await activateInstance(instance.id)
      }
      reload()
    } catch (cause) {
      setActionError(cause)
    } finally {
      setPendingId(null)
    }
  }

  const columns: TableColumn<Instance>[] = [
    {
      key: "instance",
      header: "实例",
      render: (instance) => (
        <div className="min-w-0">
          <span
            data-testid={`instance-${instance.id}`}
            className="block max-w-48 truncate text-xs font-medium text-foreground"
            title={instance.instance_key}
          >
            {instance.hostname.length > 0 ? instance.hostname : instance.instance_key}
          </span>
          <p className="font-mono text-[10px] text-muted-foreground">#{instance.id}</p>
        </div>
      ),
    },
    {
      key: "node",
      header: "Node",
      render: (instance) => (
        <span
          data-testid={`instance-node-${instance.id}`}
          className="font-mono text-xs text-foreground"
        >
          {instance.node_id}
        </span>
      ),
    },
    {
      key: "status",
      header: "状态",
      render: (instance) => (
        <div className="flex flex-col items-start gap-1">
          <Badge tone={INSTANCE_STATUS_TONE[instance.status]}>
            {INSTANCE_STATUS_LABELS[instance.status]}
          </Badge>
          {instance.last_error !== null && instance.last_error !== undefined ? (
            <span className="max-w-48 truncate text-[10px] text-muted-foreground">
              {instance.last_error}
            </span>
          ) : null}
        </div>
      ),
    },
    {
      key: "inflight",
      header: "在飞",
      render: (instance) => (
        <span
          data-testid={`instance-inflight-${instance.id}`}
          className="font-mono text-xs text-muted-foreground"
          title="active_requests / active_work"
        >
          {(instance.active_requests ?? 0)}/{(instance.active_work ?? 0)}
        </span>
      ),
    },
    {
      key: "heartbeat",
      header: "心跳",
      render: (instance) => (
        <span className="text-xs text-muted-foreground">
          {formatDateTime(instance.heartbeat_at)}
        </span>
      ),
    },
    {
      key: "process",
      header: "PID / 端口",
      render: (instance) => (
        <span className="flex flex-col font-mono text-[11px] text-foreground">
          <span>{instance.pid}</span>
          <span>{instance.api_port}</span>
        </span>
      ),
    },
    {
      key: "version",
      header: "版本",
      render: (instance) => (
        <span className="font-mono text-[11px] text-muted-foreground">{instance.version}</span>
      ),
    },
    {
      key: "actions",
      header: "操作",
      className: "text-right",
      render: (instance) => {
        const pending = pendingId === instance.id
        if (instance.status === "active") {
          return (
            <Button
              variant="secondary"
              size="sm"
              disabled={pending}
              onClick={() => void transition(instance, "drain")}
            >
              {pending ? "排空中…" : "排空"}
            </Button>
          )
        }
        if (instance.status === "draining" || instance.status === "offline") {
          return (
            <Button
              size="sm"
              disabled={pending}
              onClick={() => void transition(instance, "activate")}
            >
              {pending ? "激活中…" : "激活"}
            </Button>
          )
        }
        return <span className="text-[11px] text-muted-foreground">—</span>
      },
    },
  ]

  if (user === null || user.role !== "admin") return null

  return (
    <section
      data-testid="instance-panel"
      aria-label="实例"
      className="surface-card overflow-hidden"
    >
      <header className="flex flex-wrap items-start justify-between gap-3 border-b border-border px-4 py-3">
        <div>
          <h2 className="flex items-center gap-1.5 text-sm font-semibold text-foreground">
            <Server className="size-4" aria-hidden="true" />
            实例
          </h2>
          <p className="mt-1 text-xs text-muted-foreground">
            API 实例注册表：node 分配、心跳与排空状态。排空后 readiness 返回 503
            并停止认领新任务，in-flight 任务会完成。
          </p>
        </div>
        <Button variant="secondary" size="sm" onClick={reload} disabled={loading}>
          刷新
        </Button>
      </header>

      <div className="space-y-3 px-4 py-3">
        {actionError !== null ? <InlineError error={actionError} /> : null}
        {loading && instances.length === 0 ? (
          <LoadingCard label="正在加载实例列表…" />
        ) : error !== null ? (
          <ErrorState error={error} title="实例列表加载失败" onRetry={reload} />
        ) : instances.length === 0 ? (
          <EmptyState
            title="暂无注册实例"
            description="API 启动注册后会出现在这里。"
            className="border-none bg-transparent py-4"
          />
        ) : (
          <Table
            ariaLabel="API 实例列表"
            columns={columns}
            rows={instances}
            rowKey={(instance) => instance.id}
          />
        )}
      </div>
    </section>
  )
}
