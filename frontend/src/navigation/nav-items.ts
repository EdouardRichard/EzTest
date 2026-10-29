import type { LucideIcon } from "lucide-react"
import {
  Bot,
  Boxes,
  Bug,
  FileBarChart2,
  Gauge,
  GitBranch,
  Layers,
  LayoutDashboard,
  Network,
  Settings,
} from "lucide-react"

export type NavItem = {
  label: string
  href: string
  icon: LucideIcon
  phase: string
  hint?: string
  /** Admin-only destinations are filtered out for normal users. */
  adminOnly?: boolean
}

// The nine blueprint navigation entries. All entries are live as of P8; the
// phase field records the release in which each destination first landed. This
// cross-feature navigation data lives outside features/shell (Agent.md §6.4).
export const NAV_ITEMS: NavItem[] = [
  { label: "概览", href: "/", icon: LayoutDashboard, phase: "P8", hint: "平台概览与系统状态" },
  { label: "版本", href: "/versions", icon: Layers, phase: "P3", hint: "版本、多需求与澄清（P3）" },
  {
    label: "用例脑图",
    href: "/versions",
    icon: Network,
    phase: "P5",
    hint: "在版本详情中打开用例脑图（P5）",
  },
  { label: "问题", href: "/issues", icon: Bug, phase: "P7", hint: "全局问题中心（P7）" },
  {
    label: "报告",
    href: "/reports",
    icon: FileBarChart2,
    phase: "P7",
    hint: "版本与全局交付报告（P7）",
  },
  {
    label: "代码仓",
    href: "/code-repos",
    icon: GitBranch,
    phase: "P2",
    hint: "本地 Git 代码仓（P2）",
  },
  { label: "公共资源", href: "/resources", icon: Boxes, phase: "P2", hint: "公共资源管理（P2）" },
  {
    label: "Agent 中心",
    href: "/agents",
    icon: Bot,
    phase: "P8",
    hint: "Agent 运行时控制台（P8 扩展为 Agent 中心）",
  },
  {
    label: "质量中心",
    href: "/quality",
    icon: Gauge,
    phase: "P8",
    hint: "评测指标、阈值与基线（仅管理员）",
    adminOnly: true,
  },
  { label: "设置", href: "/settings", icon: Settings, phase: "P8", hint: "账户与平台设置" },
]

/** Filters the navigation for one viewer: admin-only entries are hidden. */
export function visibleNavItems(isAdmin: boolean): NavItem[] {
  return NAV_ITEMS.filter((item) => item.adminOnly !== true || isAdmin)
}

/** Reports whether href is the active route for pathname; hash anchors never match. */
export function isActive(pathname: string, href: string): boolean {
  if (href.includes("#")) return false
  if (href === "/") return pathname === "/"
  return pathname === href || pathname.startsWith(`${href}/`)
}
