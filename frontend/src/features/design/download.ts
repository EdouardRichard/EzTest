import type { ApiDownload } from "@/lib/api-client"

/**
 * Triggers a browser download for already-authenticated bytes. The API returns
 * the filename through `Content-Disposition`; `fallbackName` covers a missing
 * header. Exports only ever contain design content, never execution output.
 */
export function saveDownload(download: ApiDownload, fallbackName: string): void {
  const filename = download.filename.length > 0 ? download.filename : fallbackName
  const url = URL.createObjectURL(download.blob)
  const anchor = document.createElement("a")
  anchor.href = url
  anchor.download = filename
  anchor.rel = "noopener"
  anchor.style.display = "none"
  document.body.appendChild(anchor)
  anchor.click()
  anchor.remove()
  URL.revokeObjectURL(url)
}
