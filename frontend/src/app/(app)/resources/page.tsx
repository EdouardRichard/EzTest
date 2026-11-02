import type { Metadata } from "next"

import { ResourcesPage } from "@/features/resources/ResourcesPage"

export const metadata: Metadata = { title: "资源" }

type ResourcesRouteProps = {
  searchParams: Promise<{ tab?: string | string[] }>
}

export default async function ResourcesRoute({ searchParams }: ResourcesRouteProps) {
  const params = await searchParams
  return <ResourcesPage initialTab={params.tab} />
}
