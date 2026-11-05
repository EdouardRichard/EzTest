import { describe, expect, it } from "vitest"

import { catalogFixture } from "./test-fixtures"
import type { AccountResource, DatabaseResource, PageResource } from "./types"
import {
  activeAccounts,
  approvedAPIs,
  approvedPages,
  buildBindingUpdate,
  buildCreateVersionPayload,
  environmentOptions,
  findEnvironmentOption,
  resourceCounts,
  scopeDatabases,
  stepForBindingField,
  toggleExclude,
  toggleSelection,
} from "./wizard"

const TIMESTAMP = "2026-10-31T10:00:00Z"

function proposedPage(): PageResource {
  return {
    id: "1309",
    sut_id: "1001",
    environment_id: "1101",
    name: "探索发现页",
    url: "https://order.test/discovered",
    route_template: "/discovered",
    page_type: "page",
    auth_role: "",
    module: "",
    status: "proposed",
    source: "ai",
    created_at: TIMESTAMP,
    updated_at: TIMESTAMP,
  }
}

describe("environment options and derived SUT", () => {
  it("flattens every environment of every SUT and derives the SUT from the environment", () => {
    const options = environmentOptions(catalogFixture)
    expect(options.map((option) => option.environment.id)).toEqual(["1101", "1102", "2101"])

    const option = findEnvironmentOption(catalogFixture, "2101")
    expect(option?.sut.id).toBe("1002")
    expect(option?.sut.name).toBe("支付网关")
    expect(findEnvironmentOption(catalogFixture, "missing")).toBeUndefined()
  })
})

describe("effective resources and counts", () => {
  it("keeps only approved pages/APIs and active databases", () => {
    const environment = catalogFixture.suts[0]?.environments[0]
    expect(environment).toBeDefined()
    if (environment === undefined) return

    expect(approvedPages(environment).map((page) => page.id)).toEqual(["1301", "1302"])
    expect(approvedAPIs(environment).map((item) => item.id)).toEqual(["1401"])
    expect(scopeDatabases(environment).map((item) => item.id)).toEqual(["1501"])

    const withProposed = { ...environment, pages: [...environment.pages, proposedPage()] }
    expect(approvedPages(withProposed).map((page) => page.id)).toEqual(["1301", "1302"])
    expect(resourceCounts(withProposed.pages, []).proposed).toBe(1)
  })

  it("counts approved/proposed/excluded/included like the binding API", () => {
    const resources = [proposedPage(), ...(catalogFixture.suts[0]?.environments[0]?.pages ?? [])]
    expect(resourceCounts(resources, ["1302"])).toEqual({
      approved: 2,
      proposed: 1,
      excluded: 1,
      included: 1,
    })
  })

  it("filters the global account pool to active accounts compatible with the environment", () => {
    const environment = catalogFixture.suts[0]?.environments[0]
    expect(environment).toBeDefined()
    if (environment === undefined) return

    const disabled: AccountResource = {
      ...(environment.accounts[0] as AccountResource),
      id: "1609",
      username: "disabled",
      status: "disabled",
    }
    const foreign: AccountResource = {
      ...(environment.accounts[0] as AccountResource),
      id: "1610",
      username: "foreign",
      environment_id: "1102",
    }
    const custom = { ...environment, accounts: [...environment.accounts, disabled, foreign] }
    expect(activeAccounts(custom).map((item) => item.id)).toEqual(["1601", "1602"])
  })

  it("deduplicates global accounts shared by the catalog environments", () => {
    const environment = catalogFixture.suts[0]?.environments[0]
    expect(environment).toBeDefined()
    if (environment === undefined) return
    const duplicated = {
      ...environment,
      accounts: [
        environment.accounts[0] as AccountResource,
        environment.accounts[0] as AccountResource,
      ],
    }
    expect(activeAccounts(duplicated)).toHaveLength(1)
  })

  it("filters disabled databases out of the version scope", () => {
    const database = catalogFixture.suts[0]?.environments[0]?.databases[0] as DatabaseResource
    expect(
      scopeDatabases({ databases: [{ ...database, status: "disabled" }] } as never),
    ).toHaveLength(0)
  })
})

describe("wizard payload builders", () => {
  it("builds the environment-only create payload (API-15)", () => {
    expect(
      buildCreateVersionPayload({
        title: "  订单中心  ",
        description: "目标描述",
        goal: "目标",
        environmentId: "1101",
      }),
    ).toEqual({
      title: "订单中心",
      description: "目标描述",
      goal: "目标",
      environment_id: "1101",
    })
  })

  it("omits unknown repository/account selections but keeps explicit empty lists", () => {
    expect(
      buildBindingUpdate({
        includedRepositoryIds: null,
        accountIds: null,
        scopeExclude: { pages: [], apis: [], databases: [] },
      }),
    ).toEqual({ scope_exclude: { pages: [], apis: [], databases: [] } })

    expect(
      buildBindingUpdate({
        includedRepositoryIds: [],
        accountIds: ["1601"],
        scopeExclude: { pages: ["1302"], apis: [], databases: [] },
      }),
    ).toEqual({
      repository_ids: [],
      account_ids: ["1601"],
      scope_exclude: { pages: ["1302"], apis: [], databases: [] },
    })
  })

  it("toggles selections and scope exclusions without reordering", () => {
    expect(toggleSelection(["1", "2"], "3")).toEqual(["1", "2", "3"])
    expect(toggleSelection(["1", "2"], "1")).toEqual(["2"])

    const excluded = toggleExclude({ pages: ["1"], apis: [], databases: [] }, "pages", "2")
    expect(excluded).toEqual({ pages: ["1", "2"], apis: [], databases: [] })
    expect(toggleExclude(excluded, "pages", "1")).toEqual({
      pages: ["2"],
      apis: [],
      databases: [],
    })
  })

  it("maps typed binding fields back to the wizard steps", () => {
    expect(stepForBindingField("environment_id")).toBe(1)
    expect(stepForBindingField("repository_ids[0]")).toBe(2)
    expect(stepForBindingField("account_ids")).toBe(2)
    expect(stepForBindingField("scope_exclude.pages")).toBe(3)
    expect(stepForBindingField("title")).toBe(1)
    expect(stepForBindingField("")).toBe(4)
  })
})
