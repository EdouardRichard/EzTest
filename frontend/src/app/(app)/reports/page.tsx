import { FileBarChart2 } from "lucide-react"

import { EmptyState } from "@/components/ui/EmptyState"
import { StatusBadge } from "@/components/ui/StatusBadge"

export default function ReportsPage() {
  return (
    <div className="space-y-6">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <h1 className="text-2xl font-semibold tracking-tight">报告</h1>
            <StatusBadge variant="warning">P6 规划中</StatusBadge>
          </div>
          <p className="mt-1 text-sm text-muted-foreground">
            批次报告、需求覆盖矩阵、问题汇总与批次差异。
          </p>
        </div>
      </header>

      <EmptyState
        icon={FileBarChart2}
        title="报告中心规划中"
        description="批次报告将随执行能力在 P6 阶段接入，按批次展示覆盖矩阵、问题汇总、环境与 commit 附录。"
      />
    </div>
  )
}
