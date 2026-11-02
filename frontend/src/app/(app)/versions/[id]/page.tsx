import type { Metadata } from "next"

import { parseVersionTab } from "@/features/versions/tabs"
import { VersionDetailPage } from "@/features/versions/VersionDetailPage"

export const metadata: Metadata = { title: "版本详情" }

type VersionDetailRouteProps = {
  params: Promise<{ id: string }>
  searchParams: Promise<Record<string, string | string[] | undefined>>
}

function firstParam(value: string | string[] | undefined): string | undefined {
  const candidate = Array.isArray(value) ? value[0] : value
  return candidate !== undefined && candidate.length > 0 ? candidate : undefined
}

export default async function VersionDetailRoute({
  params,
  searchParams,
}: VersionDetailRouteProps) {
  const { id } = await params
  const query = await searchParams

  return (
    <VersionDetailPage
      versionId={id}
      initialTab={parseVersionTab(query.tab)}
      initialConversationId={firstParam(query.conversation)}
      initialRequirementId={firstParam(query.requirement)}
    />
  )
}
