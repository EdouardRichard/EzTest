import type { Metadata } from "next"

import { PlaceholderPage } from "@/components/placeholder-page"

export const metadata: Metadata = { title: "Agent 中心" }

export default function AgentsPage() {
  return (
    <PlaceholderPage
      title="Agent 中心"
      phase="P1"
      description="AI Agent 编排、运行记录与权限管理。"
    />
  )
}
