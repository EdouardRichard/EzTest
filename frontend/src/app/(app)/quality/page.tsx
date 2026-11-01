import type { Metadata } from "next"

import { VisualQualityPage } from "@/features/visual/VisualQualityPage"

export const metadata: Metadata = { title: "质量" }

type QualityRouteProps = {
  searchParams: Promise<{ version?: string | string[] }>
}

function firstParam(value: string | string[] | undefined): string | undefined {
  const candidate = Array.isArray(value) ? value[0] : value
  return candidate !== undefined && candidate.length > 0 ? candidate : undefined
}

export default async function QualityPage({ searchParams }: QualityRouteProps) {
  const params = await searchParams
  return <VisualQualityPage initialVersionId={firstParam(params.version)} />
}
