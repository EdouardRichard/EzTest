"use client"

import { Layers, Plus } from "lucide-react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { useCallback, useEffect, useState } from "react"

import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { Table, type TableColumn } from "@/components/ui/Table"
import { formatDateTime } from "@/lib/format"
import { listVersions } from "./api"
import { CreateVersionDialog } from "./CreateVersionDialog"
import type { VersionStatus, VersionSummary } from "./types"

const STATUS_META: Record<VersionStatus, { label: string; tone: BadgeTone }> = {
  initial: { label: "初始", tone: "info" },
  running: { label: "运行中", tone: "success" },
  completed: { label: "已完成", tone: "neutral" },
}

const COLUMNS: TableColumn<VersionSummary>[] = [
  {
    key: "title",
    header: "版本",
    render: (version) => (
      <div className="min-w-0">
        <Link
          href={`/versions/${encodeURIComponent(version.id)}?tab=overview`}
          aria-label={version.title}
          className="block max-w-md truncate text-sm font-medium text-primary hover:underline"
        >
          {version.title}
        </Link>
        <span className="text-[11px] text-muted-foreground">#{version.id}</span>
      </div>
    ),
  },
  {
    key: "status",
    header: "状态",
    render: (version) => {
      const meta = STATUS_META[version.status]
      return <Badge tone={meta.tone}>{meta.label}</Badge>
    },
  },
  {
    key: "sut_count",
    header: "SUT",
    render: (version) => (
      <span className="text-xs text-muted-foreground">{version.sut_count} 个 SUT</span>
    ),
  },
  {
    key: "created_at",
    header: "创建时间",
    render: (version) => (
      <span className="text-xs text-muted-foreground">{formatDateTime(version.created_at)}</span>
    ),
  },
  {
    key: "updated_at",
    header: "更新时间",
    render: (version) => (
      <span className="text-xs text-muted-foreground">{formatDateTime(version.updated_at)}</span>
    ),
  },
]

/** `/versions` list with the multi-SUT create dialog. */
export function VersionsPage() {
  const router = useRouter()
  const [versions, setVersions] = useState<VersionSummary[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<unknown>(null)
  const [createOpen, setCreateOpen] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setVersions(await listVersions())
    } catch (cause) {
      setError(cause)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  return (
    <div className="space-y-5">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <p className="text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">
            工作区
          </p>
          <h1 className="mt-1 text-xl font-semibold text-foreground">版本</h1>
          <p className="mt-1 max-w-2xl text-xs leading-relaxed text-muted-foreground">
            以版本为单位组织多 SUT 的测试范围；创建后进入版本工作区。
          </p>
        </div>
        <Button onClick={() => setCreateOpen(true)}>
          <Plus className="size-4" aria-hidden="true" />
          新建版本
        </Button>
      </header>

      {loading ? (
        <LoadingCard label="正在加载版本列表…" />
      ) : error ? (
        <ErrorState error={error} title="版本列表加载失败" onRetry={() => void load()} />
      ) : versions.length === 0 ? (
        <EmptyState
          icon={<Layers className="size-5" aria-hidden="true" />}
          title="还没有版本"
          description="创建第一个版本，绑定 SUT、环境、页面/API、目标库、测试账号与代码仓。"
          action={
            <Button onClick={() => setCreateOpen(true)}>
              <Plus className="size-4" aria-hidden="true" />
              新建版本
            </Button>
          }
        />
      ) : (
        <Table
          ariaLabel="版本列表"
          columns={COLUMNS}
          rows={versions}
          rowKey={(version) => version.id}
        />
      )}

      <CreateVersionDialog
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={(version) => {
          setCreateOpen(false)
          router.push(`/versions/${encodeURIComponent(version.id)}?tab=overview`)
        }}
      />
    </div>
  )
}
