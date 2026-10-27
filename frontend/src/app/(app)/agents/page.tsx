import { Bot } from "lucide-react"

import { EmptyState } from "@/components/ui/EmptyState"
import { StatusBadge } from "@/components/ui/StatusBadge"

export default function AgentsPage() {
  return (
    <div className="space-y-6">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <h1 className="text-2xl font-semibold tracking-tight">Agent 中心</h1>
            <StatusBadge variant="warning">P8 规划中</StatusBadge>
          </div>
          <p className="mt-1 text-sm text-muted-foreground">
            Agent 角色、提示词、工具、模型与运行观测。
          </p>
        </div>
      </header>

      <EmptyState
        icon={Bot}
        title="Agent 中心规划中"
        description="13 个 Agent 角色的配置与观测将在 P8 阶段接入。当前阶段不提供 Agent 管理能力。"
      />
    </div>
  )
}
