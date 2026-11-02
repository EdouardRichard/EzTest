import type { Metadata } from "next"
import type { ReactNode } from "react"

import { AuthProvider } from "@/features/auth/AuthProvider"

import "@/styles/globals.css"

export const metadata: Metadata = {
  title: {
    default: "EzTest · AI 驱动端到端测试",
    template: "%s · EzTest",
  },
  description: "EzTest 0.2 平台底座：登录、全局壳、资源与版本工作区。",
}

export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    <html lang="zh-CN">
      <body>
        <AuthProvider>{children}</AuthProvider>
      </body>
    </html>
  )
}
