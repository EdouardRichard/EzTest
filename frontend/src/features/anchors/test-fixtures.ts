/** Shared code-anchor fixtures (frozen `CodeAnchorView`). */

import type { CodeAnchor } from "./types"

const TIMESTAMP = "2026-11-01T10:00:00Z"

function baseAnchor(id: string, status: CodeAnchor["status"], path: string): CodeAnchor {
  return {
    id,
    version_id: "2001",
    repo_id: "5001",
    subject_kind: "case_version",
    subject_id: "7002",
    file_path: path,
    start_line: 10,
    end_line: 24,
    entity_id: null,
    commit_sha: "a".repeat(40),
    excerpt_sha: `sha256:${"b".repeat(64)}`,
    status,
    confidence: "high",
    created_by_kind: "user",
    created_by: "9001",
    last_validated_at: TIMESTAMP,
    created_at: TIMESTAMP,
    updated_at: TIMESTAMP,
  }
}

export const anchorFixture = baseAnchor("8101", "valid", "src/pages/OrdersPage.tsx")
export const staleAnchorFixture = baseAnchor("8102", "stale", "src/api/refund.ts")
export const brokenAnchorFixture = baseAnchor("8103", "broken", "src/legacy/old.ts")
