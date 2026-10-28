/**
 * Client-side PNG export for the mindmap canvas (spec §6.10, D38).
 *
 * The server owns Markdown/Mermaid/OPML; PNG is a pure browser snapshot. The
 * canvas SVG is serialized with an explicit XML namespace, rasterized through
 * an `Image` and a canvas, and exported at 2x the viewBox size on a white
 * background so the download looks identical regardless of the page theme.
 * No third-party dependency is involved.
 */

/** Rendered pixels per SVG user unit. Two keeps text legible when zoomed out. */
export const PNG_SCALE = 2

const PNG_MIME = "image/png"
const SVG_NAMESPACE = "http://www.w3.org/2000/svg"
const WHITE = "#ffffff"

/** Deterministic size used only when the SVG exposes neither viewBox nor size. */
const FALLBACK_WIDTH = 1200
const FALLBACK_HEIGHT = 800

/** A user-unit size, before the 2x device scale is applied. */
export type PngSize = {
  width: number
  height: number
}

function isPositiveFinite(value: number): boolean {
  return Number.isFinite(value) && value > 0
}

/** Reads `min-x min-y width height`; invalid or missing viewBoxes yield null. */
function parseViewBox(value: string | null): PngSize | null {
  if (value === null) return null
  const parts = value.trim().split(/[\s,]+/)
  if (parts.length !== 4) return null
  const width = Number.parseFloat(parts[2] ?? "")
  const height = Number.parseFloat(parts[3] ?? "")
  if (!isPositiveFinite(width) || !isPositiveFinite(height)) return null
  return { width, height }
}

/** Parses `320` / `320px`; anything non-positive or unparsable yields null. */
function parseLength(value: string | null): number | null {
  if (value === null) return null
  const parsed = Number.parseFloat(value)
  return isPositiveFinite(parsed) ? parsed : null
}

/**
 * Deterministic export size: an explicit override wins, then the viewBox
 * (the canvas always sets one), then the width/height attributes, and finally
 * a fixed fallback.
 */
export function resolvePngSize(svg: SVGSVGElement, size?: PngSize): PngSize {
  if (size !== undefined && isPositiveFinite(size.width) && isPositiveFinite(size.height)) {
    return { width: Math.round(size.width), height: Math.round(size.height) }
  }

  const viewBox = parseViewBox(svg.getAttribute("viewBox"))
  if (viewBox !== null) {
    return { width: Math.round(viewBox.width), height: Math.round(viewBox.height) }
  }

  const width = parseLength(svg.getAttribute("width"))
  const height = parseLength(svg.getAttribute("height"))
  if (width !== null && height !== null) {
    return { width: Math.round(width), height: Math.round(height) }
  }

  return { width: FALLBACK_WIDTH, height: FALLBACK_HEIGHT }
}

/**
 * The `{versionName}-mindmap.png` filename of spec §6.10. Path separators and
 * characters that browsers reject in a `download` name are replaced with "-".
 */
export function mindmapPngFilename(versionName: string): string {
  const cleaned = versionName
    .trim()
    .replace(/[\\/:*?"<>|]+/g, "-")
    .replace(/\s+/g, " ")
    .trim()
  const base = cleaned === "" ? "version" : cleaned
  return `${base}-mindmap.png`
}

/**
 * Clones the SVG so the live canvas is untouched, pins the namespace, size and
 * (when absent) a viewBox. The rendered image therefore keeps the current
 * full-graph framing.
 */
function serializeSvg(svg: SVGSVGElement, size: PngSize): string {
  const clone = svg.cloneNode(true) as SVGSVGElement
  clone.setAttribute("xmlns", SVG_NAMESPACE)
  clone.setAttribute("width", String(size.width))
  clone.setAttribute("height", String(size.height))
  if (clone.getAttribute("viewBox") === null) {
    clone.setAttribute("viewBox", `0 0 ${size.width} ${size.height}`)
  }
  return new XMLSerializer().serializeToString(clone)
}

function svgDataUrl(markup: string): string {
  return `data:image/svg+xml;charset=utf-8,${encodeURIComponent(markup)}`
}

function loadImage(url: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const image = new Image()
    image.onload = () => resolve(image)
    image.onerror = () => reject(new Error("PNG 导出失败：无法渲染画布。"))
    image.src = url
  })
}

function canvasToBlob(canvas: HTMLCanvasElement): Promise<Blob> {
  return new Promise((resolve, reject) => {
    canvas.toBlob((blob) => {
      if (blob === null) {
        reject(new Error("PNG 导出失败：浏览器未返回图片。"))
        return
      }
      resolve(blob)
    }, PNG_MIME)
  })
}

/**
 * Serializes `svg`, rasterizes it at `PNG_SCALE` on a white canvas and resolves
 * the encoded PNG blob. `size` overrides the viewBox-derived dimensions.
 */
export async function svgToPngBlob(svg: SVGSVGElement, size?: PngSize): Promise<Blob> {
  const rendered = resolvePngSize(svg, size)
  const markup = serializeSvg(svg, rendered)
  const image = await loadImage(svgDataUrl(markup))

  const canvas = document.createElement("canvas")
  canvas.width = rendered.width * PNG_SCALE
  canvas.height = rendered.height * PNG_SCALE

  const context = canvas.getContext("2d")
  if (context === null) {
    throw new Error("PNG 导出失败：浏览器不支持 canvas。")
  }

  context.fillStyle = WHITE
  context.fillRect(0, 0, canvas.width, canvas.height)
  context.drawImage(image, 0, 0, canvas.width, canvas.height)

  return canvasToBlob(canvas)
}

/**
 * Triggers a browser download of `blob` under `filename` using a temporary
 * anchor and a short-lived object URL; the URL is always revoked.
 */
export function downloadBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement("a")
  anchor.href = url
  anchor.download = filename
  anchor.rel = "noopener"
  anchor.style.display = "none"
  document.body.appendChild(anchor)

  try {
    anchor.click()
  } finally {
    anchor.remove()
    URL.revokeObjectURL(url)
  }
}
