import { describe, expect, it } from "vitest"

import {
  isPlaceholder,
  normalizePlaceholderTerm,
  precheckConfirmation,
  type ValidationRules,
} from "./requirement-precheck"

const rules: ValidationRules = {
  placeholderTerms: ["TBD", "TODO", "待定", "占位"],
  minDescriptionChars: 20,
}

const validValues = {
  title: "退款到账时效",
  asA: "买家",
  iWant: "退款审核通过后 T+1 个工作日原路退回",
  soThat: "我能及时收到退款",
  sutDescription: "支付网关退款链路，覆盖审核与到账两个环节",
  criteria: [{ given: "退款审核通过", when: "T+1 个工作日", then: "金额原路到账" }],
}

describe("placeholder whole-value match (mirrors server)", () => {
  it("normalizes with trim + lower-case", () => {
    expect(normalizePlaceholderTerm("  TBD  ")).toBe("tbd")
    expect(normalizePlaceholderTerm("TODO")).toBe("todo")
  })

  it("matches a whole value regardless of case/whitespace", () => {
    expect(isPlaceholder("TBD", rules.placeholderTerms)).toBe(true)
    expect(isPlaceholder("  tbd  ", rules.placeholderTerms)).toBe(true)
    expect(isPlaceholder("待定", rules.placeholderTerms)).toBe(true)
  })

  it("does NOT match a substring (whole-value semantics)", () => {
    expect(isPlaceholder("TBD 待补充细节", rules.placeholderTerms)).toBe(false)
    expect(isPlaceholder("这是 TODO 项", rules.placeholderTerms)).toBe(false)
    expect(isPlaceholder("退款到账时效", rules.placeholderTerms)).toBe(false)
  })
})

describe("precheckConfirmation", () => {
  it("passes a fully valid requirement", () => {
    expect(precheckConfirmation(validValues, rules)).toEqual({})
  })

  it("flags a placeholder title", () => {
    const errors = precheckConfirmation({ ...validValues, title: "TBD" }, rules)
    expect(errors.title).toBeDefined()
  })

  it("flags placeholder user-story parts", () => {
    expect(precheckConfirmation({ ...validValues, asA: "待定" }, rules).as_a).toBeDefined()
    expect(precheckConfirmation({ ...validValues, iWant: "TODO" }, rules).i_want).toBeDefined()
    expect(precheckConfirmation({ ...validValues, soThat: "占位" }, rules).so_that).toBeDefined()
  })

  it("flags a placeholder acceptance-criterion part", () => {
    const errors = precheckConfirmation(
      { ...validValues, criteria: [{ given: "退款审核通过", when: "TBD", then: "到账" }] },
      rules,
    )
    expect(errors.acceptance_criteria).toMatch(/第 1 条验收标准的 when/)
  })

  it("flags a too-short sut_description by rune length", () => {
    const errors = precheckConfirmation({ ...validValues, sutDescription: "支付网关" }, rules)
    expect(errors.sut_description).toMatch(/至少 20 字/)
  })

  it("counts runes, not UTF-16 units, for the length floor", () => {
    // 20 astral characters = 20 runes but 40 UTF-16 units; must pass a 20 floor.
    const astral = "𠮷".repeat(20)
    expect(precheckConfirmation({ ...validValues, sutDescription: astral }, rules)).toEqual({})
  })

  it("applies no increment when terms are empty and floor is zero", () => {
    const lax: ValidationRules = { placeholderTerms: [], minDescriptionChars: 0 }
    expect(
      precheckConfirmation({ ...validValues, title: "TBD", sutDescription: "x" }, lax),
    ).toEqual({})
  })
})
