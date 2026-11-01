import type { Metadata } from "next"

import { QualityPage } from "@/features/quality/QualityPage"

export const metadata: Metadata = { title: "质量" }

type QualityRouteProps = {
  searchParams: Promise<{ version?: string | string[] }>
}

function firstParam(value: string | string[] | undefined): string | undefined {
  const candidate = Array.isArray(value) ? value[0] : value
  return candidate !== undefined && candidate.length > 0 ? candidate : undefined
}

export default async function QualityRoute({ searchParams }: QualityRouteProps) {
  const params = await searchParams
  return <QualityPage initialVersionId={firstParam(params.version)} />
}
