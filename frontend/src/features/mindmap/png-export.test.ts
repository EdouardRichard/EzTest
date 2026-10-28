import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import {
  downloadBlob,
  mindmapPngFilename,
  PNG_SCALE,
  resolvePngSize,
  svgToPngBlob,
} from "./png-export"

const SVG_NS = "http://www.w3.org/2000/svg"
const VIEW_WIDTH = 640
const VIEW_HEIGHT = 360

type FakeContext = {
  fillStyle: unknown
  fillRect: ReturnType<typeof vi.fn>
  drawImage: ReturnType<typeof vi.fn>
}

let imageSources: string[] = []
let context: FakeContext
let createObjectURL: ReturnType<typeof vi.fn>
let revokeObjectURL: ReturnType<typeof vi.fn>
let originalCreateObjectURL: typeof URL.createObjectURL
let originalRevokeObjectURL: typeof URL.revokeObjectURL

/** jsdom never loads images; resolve `onload` on the next microtask instead. */
class MockImage {
  onload: (() => void) | null = null
  onerror: (() => void) | null = null
  #src = ""

  get src(): string {
    return this.#src
  }

  set src(value: string) {
    this.#src = value
    imageSources.push(value)
    queueMicrotask(() => this.onload?.())
  }
}

function makeSvg(viewBox: string | null = `0 0 ${VIEW_WIDTH} ${VIEW_HEIGHT}`): SVGSVGElement {
  const svg = document.createElementNS(SVG_NS, "svg")
  if (viewBox !== null) svg.setAttribute("viewBox", viewBox)
  const rect = document.createElementNS(SVG_NS, "rect")
  rect.setAttribute("width", "10")
  rect.setAttribute("height", "10")
  svg.appendChild(rect)
  return svg
}

beforeEach(() => {
  imageSources = []
  context = { fillStyle: "", fillRect: vi.fn(), drawImage: vi.fn() }
  createObjectURL = vi.fn(() => "blob:mock-url")
  revokeObjectURL = vi.fn()

  vi.stubGlobal("Image", MockImage)
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(
    () => context as unknown as CanvasRenderingContext2D,
  )
  vi.spyOn(HTMLCanvasElement.prototype, "toBlob").mockImplementation((callback) => {
    callback(new Blob(["png"], { type: "image/png" }))
  })

  originalCreateObjectURL = URL.createObjectURL
  originalRevokeObjectURL = URL.revokeObjectURL
  URL.createObjectURL = createObjectURL as typeof URL.createObjectURL
  URL.revokeObjectURL = revokeObjectURL as typeof URL.revokeObjectURL
})

afterEach(() => {
  vi.unstubAllGlobals()
  URL.createObjectURL = originalCreateObjectURL
  URL.revokeObjectURL = originalRevokeObjectURL
})

describe("svgToPngBlob", () => {
  it("renders the SVG at twice its viewBox size on a white background", async () => {
    const blob = await svgToPngBlob(makeSvg())

    expect(blob).toBeInstanceOf(Blob)
    expect(blob.type).toBe("image/png")
    expect(PNG_SCALE).toBe(2)
    expect(context.fillStyle).toBe("#ffffff")
    expect(context.fillRect).toHaveBeenCalledWith(0, 0, VIEW_WIDTH * 2, VIEW_HEIGHT * 2)
    expect(context.drawImage).toHaveBeenCalledWith(
      expect.anything(),
      0,
      0,
      VIEW_WIDTH * 2,
      VIEW_HEIGHT * 2,
    )
  })

  it("serializes the SVG with an XML namespace and keeps its viewBox", async () => {
    await svgToPngBlob(makeSvg())

    expect(imageSources).toHaveLength(1)
    const source = imageSources[0] ?? ""
    expect(source.startsWith("data:image/svg+xml")).toBe(true)

    const payload = source.slice(source.indexOf(",") + 1)
    const markup = decodeURIComponent(payload)
    expect(markup).toContain('xmlns="http://www.w3.org/2000/svg"')
    expect(markup).toContain(`viewBox="0 0 ${VIEW_WIDTH} ${VIEW_HEIGHT}"`)
    expect(markup).toContain("<rect")
  })

  it("honours an explicit export size", async () => {
    await svgToPngBlob(makeSvg(), { width: 100, height: 50 })

    expect(context.fillRect).toHaveBeenCalledWith(0, 0, 200, 100)
    expect(context.drawImage).toHaveBeenCalledWith(expect.anything(), 0, 0, 200, 100)
  })
})

describe("resolvePngSize", () => {
  it("falls back to the width/height attributes and then to a default size", () => {
    const sized = document.createElementNS(SVG_NS, "svg")
    sized.setAttribute("width", "320px")
    sized.setAttribute("height", "200")
    expect(resolvePngSize(sized)).toEqual({ width: 320, height: 200 })

    expect(resolvePngSize(document.createElementNS(SVG_NS, "svg"))).toEqual({
      width: 1200,
      height: 800,
    })
  })

  it("prefers the viewBox over the width/height attributes", () => {
    const svg = document.createElementNS(SVG_NS, "svg")
    svg.setAttribute("width", "10")
    svg.setAttribute("height", "10")
    svg.setAttribute("viewBox", "0 0 640 360")
    expect(resolvePngSize(svg)).toEqual({ width: 640, height: 360 })
  })
})

describe("mindmapPngFilename", () => {
  it("builds the spec §6.10 version-based filename", () => {
    expect(mindmapPngFilename("订单系统 2.0")).toBe("订单系统 2.0-mindmap.png")
    expect(mindmapPngFilename("  ")).toBe("version-mindmap.png")
    expect(mindmapPngFilename("v1/订单:系统")).toBe("v1-订单-系统-mindmap.png")
  })
})

describe("downloadBlob", () => {
  it("clicks a temporary anchor with the filename and revokes the object URL", () => {
    const captured: { anchor: HTMLAnchorElement | null } = { anchor: null }
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {
      captured.anchor = document.querySelector<HTMLAnchorElement>("a[download]")
    })

    const blob = new Blob(["png"], { type: "image/png" })
    downloadBlob(blob, "订单系统-mindmap.png")

    expect(createObjectURL).toHaveBeenCalledWith(blob)
    expect(click).toHaveBeenCalledTimes(1)
    const anchor = captured.anchor
    expect(anchor).not.toBeNull()
    if (anchor === null) throw new Error("expected a temporary download anchor")
    expect(anchor.download).toBe("订单系统-mindmap.png")
    expect(anchor.getAttribute("href")).toBe("blob:mock-url")
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:mock-url")
    expect(document.querySelector("a[download]")).toBeNull()
  })
})
