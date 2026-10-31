import type { Metadata } from "next"

import { PlaceholderPage } from "@/components/placeholder-page"

export const metadata: Metadata = { title: "报告" }

export default function ReportsPage() {
  return (
    <PlaceholderPage title="报告" phase="P1" description="波次报告、发布快照与跨版本质量趋势。" />
  )
}
