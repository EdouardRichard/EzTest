import { describe, expect, it } from "vitest"

import { catalogFixture } from "./test-fixtures"
import type { BindingDraft, VersionDraft } from "./types"
import {
  buildCreateVersionPayload,
  createBindingDraft,
  createVersionDraft,
  MAX_VERSION_BINDINGS,
  toggleSelection,
  updateBindingEnvironment,
  updateBindingSut,
  validateVersionDraft,
} from "./wizard"

function binding(key: string, overrides: Partial<BindingDraft> = {}): BindingDraft {
  return {
    ...createBindingDraft(key),
    sutId: "1001",
    environmentId: "1101",
    ...overrides,
  }
}

function draft(overrides: Partial<VersionDraft> = {}): VersionDraft {
  return { title: "订单中心 · 2026 Q4 迭代", bindings: [binding("b1")], ...overrides }
}

describe("validateVersionDraft", () => {
  it("requires a title and at least one binding", () => {
    const errors = validateVersionDraft({ title: "   ", bindings: [] }, catalogFixture)

    expect(errors.valid).toBe(false)
    expect(errors.title).toBe("请输入版本标题")
    expect(errors.bindings).toBe("请至少添加一个 SUT 绑定")
  })

  it("requires a SUT and then an environment for every binding", () => {
    const noSut = validateVersionDraft(
      draft({ bindings: [binding("b1", { sutId: "", environmentId: "" })] }),
      catalogFixture,
    )
    expect(noSut.byBinding.b1?.sutId).toBe("请选择 SUT")
    expect(noSut.valid).toBe(false)

    const noEnvironment = validateVersionDraft(
      draft({ bindings: [binding("b1", { environmentId: "" })] }),
      catalogFixture,
    )
    expect(noEnvironment.byBinding.b1?.environmentId).toBe("请选择环境")
    expect(noEnvironment.valid).toBe(false)
  })

  it("allows empty page, API, database and account selections", () => {
    const errors = validateVersionDraft(
      draft({
        bindings: [
          binding("b1", {
            pageIds: [],
            apiIds: [],
            databaseIds: [],
            accountIds: [],
          }),
        ],
      }),
      catalogFixture,
    )

    expect(errors.valid).toBe(true)
    expect(errors.byBinding).toEqual({})
  })

  it("rejects the same SUT bound twice", () => {
    const errors = validateVersionDraft(
      draft({
        bindings: [binding("b1"), binding("b2", { environmentId: "1102" })],
      }),
      catalogFixture,
    )

    expect(errors.valid).toBe(false)
    expect(errors.byBinding.b1).toBeUndefined()
    expect(errors.byBinding.b2?.sutId).toBe("同一 SUT 只能绑定一次")
  })

  it("rejects an environment that does not belong to the selected SUT", () => {
    const errors = validateVersionDraft(
      draft({ bindings: [binding("b1", { environmentId: "2101" })] }),
      catalogFixture,
    )

    expect(errors.valid).toBe(false)
    expect(errors.byBinding.b1?.environmentId).toBe("环境不属于所选 SUT")
  })

  it("rejects scoped resources from another environment", () => {
    const errors = validateVersionDraft(
      draft({
        bindings: [
          binding("b1", {
            pageIds: ["2301"],
            apiIds: ["2401"],
            databaseIds: ["9999"],
            accountIds: ["9998"],
          }),
        ],
      }),
      catalogFixture,
    )

    expect(errors.byBinding.b1?.pages).toBe("页面不属于所选环境")
    expect(errors.byBinding.b1?.apis).toBe("API 不属于所选环境")
    expect(errors.byBinding.b1?.databases).toBe("目标库不属于所选环境")
    expect(errors.byBinding.b1?.accounts).toBe("测试账号不属于所选环境")
  })

  it("rejects an unknown repository and enforces the binding limit", () => {
    const unknownRepository = validateVersionDraft(
      draft({ bindings: [binding("b1", { repositoryId: "9999" })] }),
      catalogFixture,
    )
    expect(unknownRepository.byBinding.b1?.repositoryId).toBe("代码仓不存在")

    const tooMany: BindingDraft[] = Array.from({ length: MAX_VERSION_BINDINGS + 1 }, (_, index) =>
      binding(`b${index}`, { sutId: index === 0 ? "1001" : "1002" }),
    )
    const limitErrors = validateVersionDraft(draft({ bindings: tooMany }), catalogFixture)
    expect(limitErrors.bindings).toBe(`最多支持 ${MAX_VERSION_BINDINGS} 个 SUT 绑定`)
    expect(limitErrors.valid).toBe(false)
  })
})

describe("draft transitions", () => {
  it("toggles ids while preserving selection order", () => {
    expect(toggleSelection([], "1301")).toEqual(["1301"])
    expect(toggleSelection(["1302", "1301"], "1301")).toEqual(["1302"])
    expect(toggleSelection(["1302"], "1301")).toEqual(["1302", "1301"])
  })

  it("clears the environment and scoped selections when the SUT changes", () => {
    const next = updateBindingSut(
      binding("b1", {
        pageIds: ["1301"],
        apiIds: ["1401"],
        databaseIds: ["1501"],
        accountIds: ["1601"],
      }),
      "1002",
    )

    expect(next.sutId).toBe("1002")
    expect(next.environmentId).toBe("")
    expect(next.pageIds).toEqual([])
    expect(next.apiIds).toEqual([])
    expect(next.databaseIds).toEqual([])
    expect(next.accountIds).toEqual([])
  })

  it("clears scoped selections when the environment changes", () => {
    const next = updateBindingEnvironment(
      binding("b1", {
        pageIds: ["1301"],
        apiIds: ["1401"],
        databaseIds: ["1501"],
        accountIds: ["1601"],
      }),
      "1102",
    )

    expect(next.environmentId).toBe("1102")
    expect(next.pageIds).toEqual([])
    expect(next.apiIds).toEqual([])
    expect(next.databaseIds).toEqual([])
    expect(next.accountIds).toEqual([])
  })

  it("starts a new version draft with one empty binding", () => {
    const empty = createVersionDraft()

    expect(empty.title).toBe("")
    expect(empty.bindings).toHaveLength(1)
    expect(empty.bindings[0]?.sutId).toBe("")
    expect(empty.bindings[0]?.key).not.toBe("")
  })
})

describe("buildCreateVersionPayload", () => {
  it("maps the draft to the exact contract payload", () => {
    const versionDraft: VersionDraft = {
      title: "  订单中心 · 2026 Q4 迭代  ",
      bindings: [
        binding("b1", {
          repositoryId: "1201",
          pageIds: ["1302", "1301"],
          apiIds: ["1401"],
          databaseIds: ["1501"],
          accountIds: ["1601", "1602"],
        }),
        binding("b2", {
          sutId: "1002",
          environmentId: "2101",
          repositoryId: "",
          pageIds: [],
          apiIds: [],
          databaseIds: [],
          accountIds: [],
        }),
      ],
    }

    const payload = buildCreateVersionPayload(versionDraft)

    expect(payload).toEqual({
      title: "订单中心 · 2026 Q4 迭代",
      bindings: [
        {
          sut_id: "1001",
          environment_id: "1101",
          code_repository_id: "1201",
          page_ids: ["1302", "1301"],
          api_ids: ["1401"],
          database_ids: ["1501"],
          accounts: [{ account_id: "1601" }, { account_id: "1602" }],
        },
        {
          sut_id: "1002",
          environment_id: "2101",
          page_ids: [],
          api_ids: [],
          database_ids: [],
          accounts: [],
        },
      ],
    })
    expect(payload.bindings[1]).not.toHaveProperty("code_repository_id")
    expect(payload.bindings[0]?.accounts[0]).not.toHaveProperty("role")
  })

  it("deduplicates ids and drops empty ones", () => {
    const payload = buildCreateVersionPayload(
      draft({
        bindings: [
          binding("b1", {
            pageIds: ["1301", "1302", "1301"],
            apiIds: ["1401", "1401"],
            accountIds: ["1601", "1601", "1602"],
          }),
        ],
      }),
    )

    expect(payload.bindings[0]?.page_ids).toEqual(["1301", "1302"])
    expect(payload.bindings[0]?.api_ids).toEqual(["1401"])
    expect(payload.bindings[0]?.accounts).toEqual([{ account_id: "1601" }, { account_id: "1602" }])
  })
})
