"use client"

import { Loader2 } from "lucide-react"
import { useRouter } from "next/navigation"
import { type ReactNode, useEffect } from "react"

import { useAuth } from "@/features/auth/AuthProvider"
import { AppShell } from "@/features/shell/AppShell"

export default function ProtectedLayout({ children }: { children: ReactNode }) {
  const { user, loading } = useAuth()
  const router = useRouter()

  useEffect(() => {
    if (!loading && !user) router.replace("/login")
  }, [loading, router, user])

  if (loading || !user) {
    return (
      <div className="grid min-h-screen place-items-center bg-background">
        <div className="flex flex-col items-center gap-3 text-muted-foreground">
          <Loader2 className="h-6 w-6 animate-spin text-primary" aria-hidden="true" />
          <p className="text-sm">正在验证会话…</p>
        </div>
      </div>
    )
  }

  return <AppShell>{children}</AppShell>
}
