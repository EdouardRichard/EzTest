/** Short display helpers shared by the code-intelligence panels. */

/** First 7 characters of a commit sha, or an em dash when absent. */
export function shortCommit(sha: string | null | undefined): string {
  if (sha === null || sha === undefined || sha.length === 0) return "—"
  return sha.slice(0, 7)
}

/** `path:start-end` (or just the path when the anchor has no line range). */
export function anchorLocation(
  path: string,
  startLine: number | null | undefined,
  endLine: number | null | undefined,
): string {
  if (typeof startLine !== "number") return path
  if (typeof endLine === "number" && endLine !== startLine) {
    return `${path}:${startLine}-${endLine}`
  }
  return `${path}:${startLine}`
}
