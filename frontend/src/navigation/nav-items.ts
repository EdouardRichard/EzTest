import type { LucideIcon } from "lucide-react"
import { BookOpen, Bot, Bug, Database, FileText, Gauge, Layers, Settings } from "lucide-react"

export type NavSectionId = "workspace" | "platform"

export type NavSection = {
  id: NavSectionId
  label: string
}

export type NavItem = {
  key: string
  label: string
  href: string
  icon: LucideIcon
  section: NavSectionId
  /** True for global pages that stay placeholders until a later phase. */
  placeholder?: boolean
  /** Phase that will provide the real page, shown on placeholder entries. */
  phase?: string
}

export const NAV_SECTIONS: NavSection[] = [
  { id: "workspace", label: "工作区" },
  { id: "platform", label: "平台" },
]

/** The eight P0 navigation entries, in product order. */
export const NAV_ITEMS: NavItem[] = [
  { key: "versions", label: "版本", href: "/versions", icon: Layers, section: "workspace" },
  {
    key: "issues",
    label: "问题",
    href: "/issues",
    icon: Bug,
    section: "workspace",
  },
  {
    key: "reports",
    label: "报告",
    href: "/reports",
    icon: FileText,
    section: "workspace",
  },
  {
    key: "knowledge",
    label: "知识",
    href: "/knowledge",
    icon: BookOpen,
    section: "workspace",
  },
  { key: "resources", label: "资源", href: "/resources", icon: Database, section: "workspace" },
  {
    key: "quality",
    label: "质量",
    href: "/quality",
    icon: Gauge,
    section: "platform",
  },
  {
    key: "agents",
    label: "Agent 中心",
    href: "/agents",
    icon: Bot,
    section: "platform",
  },
  { key: "settings", label: "设置", href: "/settings", icon: Settings, section: "platform" },
]

/** A navigation entry owns its exact route and every nested route below it. */
export function isNavItemActive(pathname: string, item: NavItem): boolean {
  return pathname === item.href || pathname.startsWith(`${item.href}/`)
}

/** Resolves the navigation entry that owns a pathname (used by the topbar title). */
export function findNavItem(pathname: string): NavItem | undefined {
  return NAV_ITEMS.find((item) => isNavItemActive(pathname, item))
}
