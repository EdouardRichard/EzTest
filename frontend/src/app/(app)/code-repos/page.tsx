import { GitBranch } from "lucide-react"

import { EmptyState } from "@/components/ui/EmptyState"
import { StatusBadge } from "@/components/ui/StatusBadge"

export default function CodeReposPage() {
  return (
    <div className="space-y-6">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <h1 className="text-2xl font-semibold tracking-tight">代码仓</h1>
            <StatusBadge variant="warning">P2 规划中</StatusBadge>
          </div>
          <p className="mt-1 text-sm text-muted-foreground">
            本地 Git 代码仓、分支、索引状态与变更影响分析。
          </p>
        </div>
      </header>

      <EmptyState
        icon={GitBranch}
        title="本地代码仓管理规划中"
        description="代码仓配置与索引将在 P2 阶段开放，代码与变更影响分析在 P4 阶段接入。"
      />
    </div>
  )
}
