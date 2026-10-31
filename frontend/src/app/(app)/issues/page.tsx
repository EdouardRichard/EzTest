import type { Metadata } from "next"

import { PlaceholderPage } from "@/components/placeholder-page"

export const metadata: Metadata = { title: "问题" }

export default function IssuesPage() {
  return (
    <PlaceholderPage title="问题" phase="P1" description="跨版本缺陷跟踪、证据关联与闭环处置。" />
  )
}
