import type { Metadata } from "next"

import { SkillsPage } from "@/features/skills/SkillsPage"

export const metadata: Metadata = { title: "技能库" }

export default function SkillsRoute() {
  return <SkillsPage />
}
