import type { TabItem } from "@/components/ui/Tabs"

import type { VersionTab } from "./types"

/** The eight version workspace tabs, in product order (spec §11.1). */
export const VERSION_TABS: TabItem[] = [
  { value: "overview", label: "总览" },
  { value: "requirements", label: "需求与澄清" },
  { value: "design", label: "用例设计" },
  { value: "execution", label: "执行" },
  { value: "chain", label: "探索链路" },
  { value: "issues", label: "问题" },
  { value: "reports", label: "报告" },
  { value: "code", label: "代码与变更" },
]

/** Normalizes an arbitrary `?tab=` value to one of the eight P0 tabs. */
export function parseVersionTab(value: string | string[] | undefined): VersionTab {
  const candidate = Array.isArray(value) ? value[0] : value
  const match = VERSION_TABS.find((tab) => tab.value === candidate)
  return (match?.value as VersionTab | undefined) ?? "overview"
}
