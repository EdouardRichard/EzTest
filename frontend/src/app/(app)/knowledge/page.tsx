import type { Metadata } from "next"

import { KnowledgePage } from "@/features/knowledge/KnowledgePage"

export const metadata: Metadata = { title: "知识" }

export default function KnowledgeRoute() {
  return <KnowledgePage />
}
