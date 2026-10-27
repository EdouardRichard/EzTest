import type { LucideIcon } from "lucide-react"
import {
  Bot,
  Boxes,
  Bug,
  FileBarChart2,
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
}

// The nine blueprint navigation entries. P0 only exposes / and /settings; the
// remaining entries lead to placeholder pages until their phase lands. This
// cross-feature navigation data lives outside features/shell (Agent.md §6.4).
export const NAV_ITEMS: NavItem[] = [
  { label: "概览", href: "/", icon: LayoutDashboard, phase: "P0", hint: "平台概览与系统状态" },
  { label: "版本", href: "/versions", icon: Layers, phase: "P3", hint: "版本、多需求与澄清（P3）" },
  {
    label: "用例脑图",
    href: "/versions#mindmap",
    icon: Network,
    phase: "P5",
    hint: "脑图随版本详情提供（P5）",
  },
  { label: "问题", href: "/issues", icon: Bug, phase: "P7", hint: "问题中心（P7）" },
  { label: "报告", href: "/reports", icon: FileBarChart2, phase: "P6", hint: "版本报告（P6）" },
  {
    label: "代码仓",
    href: "/code-repos",
    icon: GitBranch,
    phase: "P2",
    hint: "本地 Git 代码仓（P2）",
  },
  { label: "公共资源", href: "/resources", icon: Boxes, phase: "P2", hint: "公共资源管理（P2）" },
  { label: "Agent 中心", href: "/agents", icon: Bot, phase: "P8", hint: "Agent 配置与观测（P8）" },
  { label: "设置", href: "/settings", icon: Settings, phase: "P0", hint: "账户与平台设置" },
]

/** Reports whether href is the active route for pathname; hash anchors never match. */
export function isActive(pathname: string, href: string): boolean {
  if (href.includes("#")) return false
  if (href === "/") return pathname === "/"
  return pathname === href || pathname.startsWith(`${href}/`)
}
