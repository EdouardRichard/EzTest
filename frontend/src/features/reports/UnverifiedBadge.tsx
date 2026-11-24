"use client"

import { Badge } from "@/components/ui/Badge"

import type { VersionClosureKind } from "./types"

export type UnverifiedBadgeProps = {
  closureKind?: VersionClosureKind | null
  unverified?: boolean | null
}

/**
 * The waived-version badge (SC-G11): a version closed via admin waiver carries
 * `closure_kind=waived`/`unverified=true` (T6). Both admin and user can SEE this
 * state (Q7=A read visibility); the badge warns that closure completeness was
 * overridden and acceptance must not be read as verified. A normal closure
 * renders nothing.
 */
export function UnverifiedBadge({ closureKind, unverified }: UnverifiedBadgeProps) {
  const isUnverified = unverified === true || closureKind === "waived"
  if (!isUnverified) return null
  return (
    <Badge tone="warning" data-testid="unverified-badge">
      未验证（unverified）
    </Badge>
  )
}
