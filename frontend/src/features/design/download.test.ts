import { afterEach, describe, expect, it, vi } from "vitest"

import { saveDownload } from "./download"

describe("saveDownload", () => {
  afterEach(() => {
    vi.restoreAllMocks()
    Reflect.deleteProperty(URL, "createObjectURL")
    Reflect.deleteProperty(URL, "revokeObjectURL")
  })

  it("downloads the blob with the filename from Content-Disposition", () => {
    const createObjectURL = vi.fn(() => "blob:mock")
    const revokeObjectURL = vi.fn()
    Object.assign(URL, { createObjectURL, revokeObjectURL })
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {})
    const blob = new Blob(["# 用例设计"])

    saveDownload({ blob, filename: "case-export-9001.md", contentType: "text/markdown" }, "case.md")

    expect(createObjectURL).toHaveBeenCalledWith(blob)
    expect(click).toHaveBeenCalledTimes(1)
    const anchor = click.mock.instances[0] as HTMLAnchorElement
    expect(anchor.download).toBe("case-export-9001.md")
    expect(anchor.href).toContain("blob:mock")
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:mock")
  })

  it("falls back to the provided name when the header is missing", () => {
    Object.assign(URL, {
      createObjectURL: vi.fn(() => "blob:mock"),
      revokeObjectURL: vi.fn(),
    })
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {})

    saveDownload({ blob: new Blob(["x"]), filename: "", contentType: "" }, "case-export-9002.csv")

    const anchor = click.mock.instances[0] as HTMLAnchorElement
    expect(anchor.download).toBe("case-export-9002.csv")
  })
})
