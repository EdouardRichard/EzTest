import { describe, expect, it } from "vitest"

import { formatTokensK, formatTokensKWithAuto, parseKInput } from "./token-units"

describe("token K units", () => {
  it("renders raw tokens as K with at most 3 decimals", () => {
    expect(formatTokensK(128_000)).toBe("128")
    expect(formatTokensK(4096)).toBe("4.096")
    expect(formatTokensK(1_048_576)).toBe("1048.576")
  })

  it("calls out zero as automatic instead of pretending it is a real capacity", () => {
    expect(formatTokensKWithAuto(0)).toBe("0K（自动）")
    expect(formatTokensKWithAuto(128_000)).toBe("128K")
  })

  it("parses a K input with 1K = 1000 tokens", () => {
    expect(parseKInput("128", "上下文窗口")).toEqual({ ok: true, tokens: 128_000 })
    expect(parseKInput("0", "最大输出")).toEqual({ ok: true, tokens: 0 })
  })

  it("converts fractional K values to exact raw tokens", () => {
    expect(parseKInput("0.5", "最大输出")).toEqual({ ok: true, tokens: 500 })
    expect(parseKInput("1048.576", "上下文窗口")).toEqual({ ok: true, tokens: 1_048_576 })
  })

  it("rejects negative, non-numeric and out-of-range K values", () => {
    const negative = parseKInput("-1", "上下文窗口")
    expect(negative.ok).toBe(false)
    if (!negative.ok) expect(negative.error).toContain("上下文窗口必须是")

    const text = parseKInput("abc", "最大输出")
    expect(text.ok).toBe(false)

    const tooLarge = parseKInput("10001", "上下文窗口")
    expect(tooLarge.ok).toBe(false)
    if (!tooLarge.ok) expect(tooLarge.error).toContain("不能超过 10000K")
  })
})
