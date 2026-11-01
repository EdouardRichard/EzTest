import type { Metadata } from "next"
import { Suspense } from "react"

import { LoadingCard } from "@/components/ui/Spinner"
import { GlobalIssuesPage } from "@/features/delivery/GlobalIssuesPage"

export const metadata: Metadata = { title: "问题" }

export default function IssuesPage() {
  return (
    <Suspense fallback={<LoadingCard label="正在加载全局问题…" />}>
      <GlobalIssuesPage />
    </Suspense>
  )
}
