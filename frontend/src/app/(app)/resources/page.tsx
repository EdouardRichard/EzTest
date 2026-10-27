import { Boxes } from "lucide-react"

import { EmptyState } from "@/components/ui/EmptyState"
import { StatusBadge } from "@/components/ui/StatusBadge"

export default function ResourcesPage() {
  return (
    <div className="space-y-6">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <h1 className="text-2xl font-semibold tracking-tight">公共资源</h1>
            <StatusBadge variant="warning">P2 规划中</StatusBadge>
          </div>
          <p className="mt-1 text-sm text-muted-foreground">
            系统、环境、应用模型、账号与 Session 等公共资源管理。
          </p>
        </div>
      </header>

      <EmptyState
        icon={Boxes}
        title="公共资源管理规划中"
        description="管理员将能在 P2 阶段配置共享资源；普通用户只读。当前阶段尚未开放资源编辑。"
      />
    </div>
  )
}
