"use client"

import { useCallback, useEffect, useState } from "react"

import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { Table, type TableColumn } from "@/components/ui/Table"
import { formatDateTime } from "@/lib/format"

import { type AdminExecutor, type AdminExecutorStatus, listAdminExecutors } from "./api"

const STATUS_LABEL: Record<AdminExecutorStatus, string> = {
  active: "运行中",
  draining: "排空中",
  offline: "离线",
}

const STATUS_TONE: Record<AdminExecutorStatus, BadgeTone> = {
  active: "success",
  draining: "warning",
  offline: "danger",
}

/**
 * Admin-only read-only executor registry (`GET /api/admin/executors`). It shows
 * registration, health (active contexts vs capacity) and the last heartbeat or
 * error. Drain/activate are separate lifecycle write routes and stay out of this
 * read view, matching the API-only disposal of the executor list.
 */
export function ExecutorsPanel() {
  const [executors, setExecutors] = useState<AdminExecutor[] | null>(null)
  const [error, setError] = useState<unknown>(null)

  const load = useCallback(async () => {
    setError(null)
    setExecutors(null)
    try {
      setExecutors(await listAdminExecutors())
    } catch (cause) {
      setError(cause)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const columns: TableColumn<AdminExecutor>[] = [
    {
      key: "name",
      header: "执行器",
      render: (executor) => (
        <div className="min-w-0">
          <p className="truncate text-xs font-medium text-foreground">{executor.name}</p>
          <p className="text-[10px] text-muted-foreground">#{executor.id}</p>
        </div>
      ),
    },
    {
      key: "endpoint",
      header: "地址",
      render: (executor) => (
        <code className="break-all font-mono text-[10px] text-foreground">{executor.endpoint}</code>
      ),
    },
    {
      key: "status",
      header: "状态",
      render: (executor) => (
        <span data-testid={`executor-status-${executor.id}`}>
          <Badge tone={STATUS_TONE[executor.status]}>{STATUS_LABEL[executor.status]}</Badge>
        </span>
      ),
    },
    {
      key: "health",
      header: "健康 / 负载",
      render: (executor) => (
        <span data-testid={`executor-health-${executor.id}`} className="text-xs text-foreground">
          {executor.active_contexts}/{executor.capacity}
        </span>
      ),
    },
    {
      key: "version",
      header: "版本",
      render: (executor) => (
        <span className="text-[11px] text-muted-foreground">{executor.version || "—"}</span>
      ),
    },
    {
      key: "heartbeat",
      header: "最后心跳",
      render: (executor) => (
        <span className="text-[11px] text-muted-foreground">
          {formatDateTime(executor.last_heartbeat)}
        </span>
      ),
    },
    {
      key: "error",
      header: "最近错误",
      render: (executor) => (
        <span className="text-[10px] text-muted-foreground">{executor.last_error || "—"}</span>
      ),
    },
  ]

  return (
    <section
      data-testid="executors-panel"
      aria-label="执行器只读列表"
      className="space-y-2 rounded-lg border border-border bg-card px-3 py-2.5"
    >
      <header className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold text-foreground">执行器</h2>
          <p className="text-[11px] text-muted-foreground">
            管理员只读视图 · 注册状态、健康负载与最后心跳
          </p>
        </div>
        <Badge tone="neutral">admin · 只读</Badge>
      </header>

      {error !== null ? (
        <ErrorState error={error} title="执行器列表加载失败" onRetry={() => void load()} />
      ) : executors === null ? (
        <LoadingCard label="正在加载执行器…" />
      ) : (
        <Table
          ariaLabel="执行器列表"
          columns={columns}
          rows={executors}
          rowKey={(executor) => executor.id}
          empty="暂无执行器"
        />
      )}

      <div className="flex justify-end">
        <Button variant="ghost" size="sm" disabled={executors === null} onClick={() => void load()}>
          刷新
        </Button>
      </div>
    </section>
  )
}
