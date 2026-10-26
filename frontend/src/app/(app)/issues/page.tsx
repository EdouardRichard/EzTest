import { Bug } from "lucide-react"

import { EmptyState } from "@/features/shell/EmptyState"
import { StatusBadge } from "@/features/shell/StatusBadge"

export default function IssuesPage() {
  return (
    <div className="space-y-6">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <h1 className="text-2xl font-semibold tracking-tight">问题</h1>
            <StatusBadge variant="warning">P7 规划中</StatusBadge>
          </div>
          <p className="mt-1 text-sm text-muted-foreground">
            问题列表、详情、确认/驳回与证据、代码锚点深链。
          </p>
        </div>
      </header>

      <EmptyState
        icon={Bug}
        title="问题中心规划中"
        description="问题中心将在 P7 阶段接入；届时可按严重级、状态与关联用例查看 AI 发现的缺陷，并深链到执行证据。"
      />
    </div>
  )
}
