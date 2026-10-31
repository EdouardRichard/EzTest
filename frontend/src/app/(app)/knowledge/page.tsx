import type { Metadata } from "next"

import { PlaceholderPage } from "@/components/placeholder-page"

export const metadata: Metadata = { title: "知识" }

export default function KnowledgePage() {
  return (
    <PlaceholderPage
      title="知识"
      phase="P1"
      description="需求文档、领域知识与可检索的测试上下文。"
    />
  )
}
