import type { Metadata } from "next"
import { Suspense } from "react"

import { LoadingCard } from "@/components/ui/Spinner"
import { GlobalReportsPage } from "@/features/delivery/GlobalReportsPage"

export const metadata: Metadata = { title: "报告" }

export default function ReportsPage() {
  return (
    <Suspense fallback={<LoadingCard label="正在加载全局报告…" />}>
      <GlobalReportsPage />
    </Suspense>
  )
}
