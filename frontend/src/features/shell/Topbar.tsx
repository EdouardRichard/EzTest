"use client"

import { ChevronDown, KeyRound, LogOut } from "lucide-react"
import { usePathname, useRouter } from "next/navigation"
import { useEffect, useState } from "react"
import { cn } from "@/components/ui/cn"
import { useAuth } from "@/features/auth/AuthProvider"
import { ChangePasswordDialog } from "@/features/auth/ChangePasswordDialog"
import { CommandPalette } from "@/features/search/CommandPalette"
import { getApiBaseUrl } from "@/lib/api-client"
import { connectSSE, type SSEStatus } from "@/lib/sse-client"
import { findNavItem } from "@/navigation/nav-items"

import { TodoDrawerButton } from "./TodoDrawer"

const STATUS_META: Record<SSEStatus, { label: string; dotClassName: string }> = {
  connecting: { label: "连接中", dotClassName: "bg-warning animate-pulse" },
  live: { label: "实时", dotClassName: "bg-success" },
  reconnecting: { label: "重连中", dotClassName: "bg-warning animate-pulse" },
  closed: { label: "已断开", dotClassName: "bg-muted-foreground" },
}

const ROLE_LABELS: Record<string, string> = {
  admin: "管理员",
  user: "普通用户",
}

function useSystemStreamStatus(token: string | null): SSEStatus {
  const [status, setStatus] = useState<SSEStatus>("closed")

  useEffect(() => {
    if (token === null) {
      setStatus("closed")
      return undefined
    }
    return connectSSE({
      url: `${getApiBaseUrl()}/api/stream?channel=system`,
      token,
      onEvent: () => {},
      onStatus: setStatus,
    })
  }, [token])

  return status
}

/** Top bar: current page title, system-channel SSE dot and the user menu. */
export function Topbar() {
  const pathname = usePathname()
  const router = useRouter()
  const { user, token, logout } = useAuth()
  const [menuOpen, setMenuOpen] = useState(false)
  const [passwordOpen, setPasswordOpen] = useState(false)
  const [loggingOut, setLoggingOut] = useState(false)
  const status = useSystemStreamStatus(token)
  const statusMeta = STATUS_META[status]

  const title = findNavItem(pathname)?.label ?? "EzTest"
  const currentVersionId = /^\/versions\/([^/]+)(?:\/|$)/.exec(pathname)?.[1] ?? null
  const displayName = user?.display_name ?? "访客"
  const roleLabel = user ? (ROLE_LABELS[user.role] ?? user.role) : "未登录"
  const avatarText = displayName.slice(0, 1).toUpperCase()

  async function handleLogout(): Promise<void> {
    setMenuOpen(false)
    setLoggingOut(true)
    try {
      await logout()
    } finally {
      setLoggingOut(false)
      router.replace("/login")
    }
  }

  return (
    <header className="sticky top-0 z-30 flex h-14 items-center gap-3 border-b border-border bg-card/90 px-4 backdrop-blur sm:px-6">
      <div className="min-w-0">
        <span data-testid="topbar-title" className="block truncate text-sm font-semibold">
          {title}
        </span>
        <span className="block truncate text-[10px] text-muted-foreground">
          EzTest · AI 驱动端到端测试
        </span>
      </div>

      <div className="ml-auto flex items-center gap-2">
        <CommandPalette />
        <TodoDrawerButton versionId={currentVersionId} />

        <span
          data-testid="sse-status"
          data-state={status}
          title={`系统事件流：${statusMeta.label}`}
          className="inline-flex items-center gap-1.5 rounded-full border border-border bg-card px-2.5 py-1 text-[11px] text-muted-foreground"
        >
          <span
            className={cn("size-1.5 rounded-full", statusMeta.dotClassName)}
            aria-hidden="true"
          />
          {statusMeta.label}
        </span>

        <div className="relative">
          <button
            type="button"
            aria-label="打开用户菜单"
            aria-haspopup="menu"
            aria-expanded={menuOpen}
            onClick={() => setMenuOpen((open) => !open)}
            className="flex items-center gap-2 rounded-lg border border-border bg-card px-2 py-1 text-left transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <span className="flex size-7 items-center justify-center rounded-full bg-accent text-[11px] font-semibold text-accent-foreground">
              {avatarText}
            </span>
            <span className="hidden leading-tight sm:block">
              <span className="block max-w-32 truncate text-xs font-semibold">{displayName}</span>
              <span className="block text-[10px] text-muted-foreground">{roleLabel}</span>
            </span>
            <ChevronDown className="size-3.5 text-muted-foreground" aria-hidden="true" />
          </button>

          {menuOpen ? (
            <div
              role="menu"
              aria-label="用户菜单"
              className="absolute right-0 top-full z-40 mt-1 w-48 overflow-hidden rounded-lg border border-border bg-card py-1 shadow-xl"
            >
              <div className="border-b border-border px-3 py-2 text-[11px] text-muted-foreground">
                当前账号：{user?.username ?? "未登录"}
              </div>
              <button
                type="button"
                role="menuitem"
                onClick={() => {
                  setMenuOpen(false)
                  setPasswordOpen(true)
                }}
                className="flex w-full items-center gap-2 px-3 py-2 text-xs text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
              >
                <KeyRound className="size-3.5" aria-hidden="true" />
                修改密码
              </button>
              <button
                type="button"
                role="menuitem"
                disabled={loggingOut}
                onClick={() => void handleLogout()}
                className="flex w-full items-center gap-2 px-3 py-2 text-xs text-destructive transition-colors hover:bg-destructive/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring disabled:opacity-60"
              >
                <LogOut className="size-3.5" aria-hidden="true" />
                退出登录
              </button>
            </div>
          ) : null}
        </div>
      </div>

      <ChangePasswordDialog open={passwordOpen} onClose={() => setPasswordOpen(false)} />
    </header>
  )
}
