import type { Metadata } from "next"

import { LoginPage } from "@/features/auth/LoginPage"

export const metadata: Metadata = { title: "登录" }

type LoginRouteProps = {
  searchParams: Promise<{ next?: string | string[] }>
}

function firstParam(value: string | string[] | undefined): string | undefined {
  const candidate = Array.isArray(value) ? value[0] : value
  return candidate !== undefined && candidate.length > 0 ? candidate : undefined
}

export default async function LoginRoute({ searchParams }: LoginRouteProps) {
  const params = await searchParams
  return <LoginPage nextPath={firstParam(params.next)} />
}
