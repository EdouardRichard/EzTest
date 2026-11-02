import type { ReactNode } from "react"

import { RequireAuth } from "@/features/auth/AuthProvider"
import { AppShell } from "@/features/shell/AppShell"

export default function AppLayout({ children }: { children: ReactNode }) {
  return (
    <RequireAuth>
      <AppShell>{children}</AppShell>
    </RequireAuth>
  )
}
