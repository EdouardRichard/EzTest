import { MindmapPage } from "@/features/mindmap/MindmapPage"

type MindmapRouteProps = {
  /** Next 16 passes dynamic segments as a promise. */
  params: Promise<{ id: string }>
}

export default async function MindmapRoutePage({ params }: MindmapRouteProps) {
  const { id } = await params
  return <MindmapPage versionId={id} />
}
