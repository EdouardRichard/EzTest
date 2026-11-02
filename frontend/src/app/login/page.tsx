import type { Metadata } from "next"

import { LoginPage } from "@/features/auth/LoginPage"

export const metadata: Metadata = { title: "登录" }

export default function LoginRoute() {
  return <LoginPage />
}
