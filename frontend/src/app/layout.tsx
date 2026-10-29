import type { Metadata } from "next"
import { ThemeProvider } from "next-themes"
import type { ReactNode } from "react"

import { AuthProvider } from "@/features/auth/AuthProvider"
import "@/styles/globals.css"

export const metadata: Metadata = {
  title: {
    default: "EzTest · AI 端到端测试平台",
    template: "%s · EzTest",
  },
  description: "AI 自主端到端测试平台：版本、需求、用例、执行与证据的可观察闭环。",
}

export default function RootLayout({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <html lang="zh-CN" suppressHydrationWarning>
      <body>
        <ThemeProvider
          attribute="class"
          defaultTheme="system"
          enableSystem
          disableTransitionOnChange
        >
          <AuthProvider>{children}</AuthProvider>
        </ThemeProvider>
      </body>
    </html>
  )
}
