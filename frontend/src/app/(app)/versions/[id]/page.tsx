import { VersionDetailPage } from "@/features/versions/VersionDetailPage"

type VersionDetailRouteProps = {
  /** Next 16 passes dynamic segments as a promise. */
  params: Promise<{ id: string }>
}

export default async function VersionDetailRoutePage({ params }: VersionDetailRouteProps) {
  const { id } = await params
  return <VersionDetailPage versionId={id} />
}
