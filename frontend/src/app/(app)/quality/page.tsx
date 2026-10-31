import type { Metadata } from "next"

import { PlaceholderPage } from "@/components/placeholder-page"

export const metadata: Metadata = { title: "质量" }

export default function QualityPage() {
  return (
    <PlaceholderPage title="质量" phase="P1" description="质量度量、门禁策略与测试覆盖趋势。" />
  )
}
