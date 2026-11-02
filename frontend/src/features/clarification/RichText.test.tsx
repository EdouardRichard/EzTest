import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { RichText } from "./RichText"

describe("RichText", () => {
  afterEach(cleanup)

  it("renders GFM markdown: bold, tables, code and lists", () => {
    const { container } = render(
      <RichText>{`**重点**\n\n| 字段 | 说明 |\n| --- | --- |\n| T+1 | 到账 |\n\n- 第一条\n- 第二条\n\n\`code\``}</RichText>,
    )

    expect(screen.getByText("重点").tagName).toBe("STRONG")
    expect(container.querySelector("table")).not.toBeNull()
    expect(container.querySelector("table")).toHaveTextContent("T+1")
    expect(container.querySelectorAll("li")).toHaveLength(2)
    expect(container.querySelector("code")).toHaveTextContent("code")
  })

  it("never renders raw HTML as executable nodes", () => {
    const alertSpy = vi.fn()
    vi.stubGlobal("alert", alertSpy)

    const { container } = render(
      <RichText>{`hello <script>alert("xss")</script> <img src=x onerror="alert(1)" />`}</RichText>,
    )

    expect(container.querySelector("script")).toBeNull()
    expect(container.querySelector("img")).toBeNull()
    expect(alertSpy).not.toHaveBeenCalled()
    expect(container).toHaveTextContent('hello <script>alert("xss")</script>')

    vi.unstubAllGlobals()
  })

  it("drops unsafe link protocols and hardens external links", () => {
    const { container } = render(
      <RichText>{`[恶意](javascript:alert(1)) [文档](https://example.test/doc)`}</RichText>,
    )

    const anchors = [...container.querySelectorAll("a")]
    expect(anchors.some((anchor) => anchor.getAttribute("href")?.includes("javascript"))).toBe(
      false,
    )

    const external = anchors.find(
      (anchor) => anchor.getAttribute("href") === "https://example.test/doc",
    )
    expect(external).toBeDefined()
    expect(external).toHaveAttribute("target", "_blank")
    expect(external?.getAttribute("rel")).toContain("noopener")
    expect(external?.getAttribute("rel")).toContain("noreferrer")
  })
})
