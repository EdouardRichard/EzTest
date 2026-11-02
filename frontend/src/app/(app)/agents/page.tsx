import type { Metadata } from "next"

import { AgentCenterPage } from "@/features/agent-center/AgentCenterPage"

export const metadata: Metadata = { title: "Agent 中心" }

type AgentsRouteProps = {
  searchParams: Promise<{ tab?: string | string[]; profile?: string | string[] }>
}

function firstParam(value: string | string[] | undefined): string | undefined {
  const candidate = Array.isArray(value) ? value[0] : value
  return candidate !== undefined && candidate.length > 0 ? candidate : undefined
}

export default async function AgentsPage({ searchParams }: AgentsRouteProps) {
  const params = await searchParams
  return (
    <AgentCenterPage
      initialTab={firstParam(params.tab)}
      initialProfile={firstParam(params.profile)}
    />
  )
}
