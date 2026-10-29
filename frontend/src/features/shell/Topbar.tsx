"use client"

import * as DropdownMenu from "@radix-ui/react-dropdown-menu"
import { ChevronDown, FlaskConical, LogOut, Moon, ShieldCheck, Sun } from "lucide-react"
import Link from "next/link"
import { useTheme } from "next-themes"
import { useEffect, useState } from "react"
import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"
import { useAuth } from "@/features/auth/AuthProvider"

import { MobileMenuButton } from "./MobileNav"
import { SearchCommand } from "./search/SearchCommand"
import { TodoButton } from "./todos/TodoButton"
import { VersionSwitcher } from "./VersionSwitcher"

type EnvironmentMeta = {
  label: string
  variant: StatusBadgeVariant
}

function resolveEnvironment(): EnvironmentMeta {
  const environment = process.env.NEXT_PUBLIC_APP_ENV ?? "development"
  if (environment === "production") return { label: "生产环境", variant: "warning" }
  if (environment === "test") return { label: "测试环境", variant: "info" }
  return { label: "开发环境", variant: "info" }
}

const ENVIRONMENT = resolveEnvironment()

function ThemeToggle() {
  const { resolvedTheme, setTheme } = useTheme()
  const [mounted, setMounted] = useState(false)

  useEffect(() => setMounted(true), [])

  const isDark = mounted && resolvedTheme === "dark"
  return (
    <button
      type="button"
      onClick={() => setTheme(isDark ? "light" : "dark")}
      aria-label={isDark ? "切换到浅色主题" : "切换到深色主题"}
      className="grid h-9 w-9 place-items-center rounded-lg border border-border bg-card text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      {mounted ? (
        isDark ? (
          <Sun className="h-4 w-4" aria-hidden="true" />
        ) : (
          <Moon className="h-4 w-4" aria-hidden="true" />
        )
      ) : (
        <Moon className="h-4 w-4 opacity-0" aria-hidden="true" />
      )}
    </button>
  )
}

function UserMenu() {
  const { user, logout } = useAuth()

  if (!user) {
    return <div className="h-9 w-9 animate-pulse rounded-full bg-muted" aria-hidden="true" />
  }

  const roleLabel = user.role === "admin" ? "管理员" : "普通用户"
  const statusLabel = user.status === "active" ? "活跃" : "已禁用"
  const initial = user.username.slice(0, 1).toUpperCase()

  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger asChild>
        <button
          type="button"
          className="flex items-center gap-2 rounded-full border border-transparent p-1 pr-2 transition-colors hover:border-border hover:bg-card focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          aria-label="打开用户菜单"
        >
          <span className="grid h-8 w-8 place-items-center rounded-full bg-primary/15 text-sm font-semibold text-primary">
            {initial}
          </span>
          <span className="hidden text-left sm:block">
            <span className="block text-sm font-medium leading-tight">{user.username}</span>
            <span className="block text-[11px] leading-tight text-muted-foreground">
              {roleLabel}
            </span>
          </span>
          <ChevronDown className="h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
        </button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content
          align="end"
          sideOffset={10}
          className="z-50 w-60 rounded-xl border border-border bg-card p-1.5 shadow-xl shadow-black/10"
        >
          <div className="px-2.5 py-2">
            <p className="truncate text-sm font-medium">{user.username}</p>
            <p className="mt-0.5 text-xs text-muted-foreground">
              {roleLabel} · {statusLabel}
            </p>
          </div>
          <DropdownMenu.Separator className="my-1 h-px bg-border" />
          <DropdownMenu.Item asChild>
            <Link
              href="/settings"
              className="flex cursor-pointer items-center gap-2 rounded-lg px-2.5 py-2 text-sm outline-none transition-colors data-[highlighted]:bg-muted"
            >
              账户设置
            </Link>
          </DropdownMenu.Item>
          <DropdownMenu.Item
            onSelect={() => void logout()}
            className="flex cursor-pointer items-center gap-2 rounded-lg px-2.5 py-2 text-sm text-destructive outline-none transition-colors data-[highlighted]:bg-destructive/10"
          >
            <LogOut className="h-4 w-4" aria-hidden="true" />
            退出登录
          </DropdownMenu.Item>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  )
}

export type TopbarProps = {
  /** Whether the mobile navigation drawer is open (drives `aria-expanded`). */
  navOpen: boolean
  onToggleNav: () => void
}

export function Topbar({ navOpen, onToggleNav }: TopbarProps) {
  return (
    <header className="sticky top-0 z-20 border-b border-border bg-background/80 backdrop-blur-xl">
      {/*
        Phones wrap the version switcher and search onto a second row; from `sm`
        up the header returns to its original single-row layout whenever the
        content fits, and wraps instead of overflowing when it does not. Only the
        <sm order changes, so the desktop tab order stays hamburger → version →
        search → actions.
      */}
      <div className="flex min-h-16 flex-wrap items-center gap-x-3 gap-y-2 px-4 py-2 sm:px-6 sm:py-0 lg:px-10">
        <MobileMenuButton open={navOpen} onToggle={onToggleNav} />

        <Link href="/" className="flex items-center gap-2 max-sm:order-2 sm:order-none lg:hidden">
          <span className="grid h-8 w-8 place-items-center rounded-lg bg-gradient-to-br from-blue-500 to-sky-400 text-white">
            <FlaskConical className="h-4 w-4" aria-hidden="true" />
          </span>
          <span className="text-sm font-semibold tracking-tight">EzTest</span>
        </Link>

        {/* P8 global shell slot (spec §6.1): current version + status badge. */}
        <div className="min-w-0 max-sm:order-4 sm:order-none">
          <VersionSwitcher />
        </div>

        {/* P8 global search (spec §6.2): trigger + Ctrl/Cmd+K command palette. */}
        <div className="flex items-center max-sm:order-5 sm:order-none">
          <SearchCommand />
        </div>

        <div className="hidden items-center gap-2 max-sm:order-6 sm:order-none sm:flex">
          <StatusBadge variant={ENVIRONMENT.variant}>{ENVIRONMENT.label}</StatusBadge>
          <span className="hidden items-center gap-1.5 rounded-full border border-border bg-card px-2.5 py-1 text-[11px] font-medium text-muted-foreground md:inline-flex">
            <ShieldCheck className="h-3.5 w-3.5 text-emerald-500" aria-hidden="true" />
            P8 · 体验硬化
          </span>
        </div>

        <div className="ml-auto flex items-center gap-1.5 max-sm:order-3 sm:order-none sm:gap-2">
          <TodoButton />
          <ThemeToggle />
          <span className="h-6 w-px bg-border" aria-hidden="true" />
          <UserMenu />
        </div>
      </div>
    </header>
  )
}
