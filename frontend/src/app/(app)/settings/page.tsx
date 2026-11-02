import type { Metadata } from "next"

import { SettingsPage } from "@/features/settings/SettingsPage"

export const metadata: Metadata = { title: "设置" }

type SettingsRouteProps = {
  searchParams: Promise<{ tab?: string | string[] }>
}

export default async function SettingsRoute({ searchParams }: SettingsRouteProps) {
  const params = await searchParams
  const tab = Array.isArray(params.tab) ? params.tab[0] : params.tab
  return <SettingsPage initialTab={tab} />
}
