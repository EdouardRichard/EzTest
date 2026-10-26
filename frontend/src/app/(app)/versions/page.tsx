import { Layers } from "lucide-react"

import { EmptyState } from "@/features/shell/EmptyState"
import { StatusBadge } from "@/features/shell/StatusBadge"

export default function VersionsPage() {
  return (
    <div className="space-y-6">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <h1 className="text-2xl font-semibold tracking-tight">版本</h1>
            <StatusBadge variant="warning">P3 规划中</StatusBadge>
          </div>
          <p className="mt-1 text-sm text-muted-foreground">
            版本列表、多需求录入、澄清工作台与版本详情。
          </p>
        </div>
      </header>

      <EmptyState
        icon={Layers}
        title="版本管理规划中"
        description="版本、需求澄清、用例脑图与执行中心将按 P3–P6 阶段逐步开放。当前 P0 仅提供应用壳、登录与请求基础设施。"
      />
    </div>
  )
}
