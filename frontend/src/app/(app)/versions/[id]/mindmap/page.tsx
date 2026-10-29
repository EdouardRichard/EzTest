import { MindmapPage } from "@/features/mindmap/MindmapPage"

type MindmapRouteProps = {
  /** Next 16 passes dynamic segments as a promise. */
  params: Promise<{ id: string }>
  /** Next 16 passes the query string as a promise; `?case=` selects a node. */
  searchParams?: Promise<Record<string, string | string[] | undefined>>
}

/** Reads the single-string `?case=` deep link; repeats/arrays are ignored. */
function readCaseParam(searchParams: Record<string, string | string[] | undefined>): string {
  const value = searchParams.case
  return typeof value === "string" ? value : ""
}

export default async function MindmapRoutePage({ params, searchParams }: MindmapRouteProps) {
  const { id } = await params
  const query = (await searchParams) ?? {}
  return <MindmapPage versionId={id} initialCaseId={readCaseParam(query)} />
}
