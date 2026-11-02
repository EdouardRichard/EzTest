import type { Metadata } from "next"

import { VersionsPage } from "@/features/versions/VersionsPage"

export const metadata: Metadata = { title: "版本" }

export default function VersionsRoute() {
  return <VersionsPage />
}
