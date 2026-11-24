// v0.2.4-B: the wizard binds an environment directly; accounts are a global pool.
// Scope-exclusion / version-level repository-override assertions were deleted with
// the version_sut chain (see task-20-report.md). The account-pool fixtures below
// exercise real filtering/dedup against the re-signed activeAccounts(accounts[]).
import { describe, expect, it } from "vitest"

import { catalogFixture } from "./test-fixtures"
import type {
  AccountResource,
  DatabaseResource,
  EnvironmentResource,
  PageResource,
  SutResource,
} from "./types"
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
  toggleSelection,
} from "./wizard"

const TIMESTAMP = "2026-10-31T10:00:00Z"

// v0.2.4-B: assets are per-SUT; the catalog exposes top-level environments.
const environment = catalogFixture.environments[0] as EnvironmentResource
const firstSut = environment.suts[0] as SutResource
// Item 4 (T19b review): the shared environment owns two SUTs; the status-filtering
// behaviour under test is per-SUT, so pin a single-SUT fixture and keep the
// assertions byte-identical.
const singleSutEnvironment: EnvironmentResource = { ...environment, suts: [firstSut] }

function proposedPage(): PageResource {
  return {
    id: "1309",
    sut_id: "1001",
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
  it("yields one option per top-level environment with a representative SUT", () => {
    const options = environmentOptions(catalogFixture)
    expect(options.map((option) => option.environment.id)).toEqual(["1101"])

    const option = findEnvironmentOption(catalogFixture, "1101")
    expect(option?.sut.id).toBe("1001")
    expect(option?.sut.name).toBe("订单中心")
    expect(option?.environment.suts.map((sut) => sut.id)).toEqual(["1001", "1002"])
    expect(findEnvironmentOption(catalogFixture, "missing")).toBeUndefined()
  })
})

describe("effective resources and counts", () => {
  it("keeps only approved pages/APIs and active databases", () => {
    expect(approvedPages(singleSutEnvironment).map((page) => page.id)).toEqual(["1301", "1302"])
    expect(approvedAPIs(singleSutEnvironment).map((item) => item.id)).toEqual(["1401"])
    expect(scopeDatabases(singleSutEnvironment).map((item) => item.id)).toEqual(["1501"])

    const withProposed: EnvironmentResource = {
      ...singleSutEnvironment,
      suts: [{ ...firstSut, pages: [...firstSut.pages, proposedPage()] }],
    }
    expect(approvedPages(withProposed).map((page) => page.id)).toEqual(["1301", "1302"])
    expect(resourceCounts(withProposed.suts.flatMap((sut) => sut.pages)).proposed).toBe(1)
  })

  it("aggregates approved assets across every SUT of the environment", () => {
    // The full two-SUT environment flattens both SUTs' approved assets.
    expect(approvedPages(environment).map((page) => page.id)).toEqual(["1301", "1302", "2301"])
    expect(approvedAPIs(environment).map((item) => item.id)).toEqual(["1401", "2401"])
  })

  it("counts approved/proposed like the binding API (no exclusion buckets)", () => {
    const resources = [proposedPage(), ...firstSut.pages]
    expect(resourceCounts(resources)).toEqual({ approved: 2, proposed: 1 })
  })

  it("filters the global account pool to active accounts", () => {
    const baseAccount = catalogFixture.accounts[0] as AccountResource
    const disabled: AccountResource = {
      ...baseAccount,
      id: "1609",
      username: "disabled",
      status: "disabled",
    }
    // A real pool: two active accounts plus one disabled one that must be dropped.
    const pool = [...catalogFixture.accounts, disabled]
    expect(activeAccounts(pool).map((item) => item.id)).toEqual(["1601", "1602"])
  })

  it("deduplicates global accounts shared across the pool", () => {
    const baseAccount = catalogFixture.accounts[0] as AccountResource
    const duplicated = [
      baseAccount,
      { ...baseAccount },
      catalogFixture.accounts[1] as AccountResource,
    ]
    expect(activeAccounts(duplicated)).toHaveLength(2)
    expect(activeAccounts(duplicated).map((item) => item.id)).toEqual(["1601", "1602"])
  })

  it("filters disabled databases out of the version scope", () => {
    const database = firstSut.databases[0] as DatabaseResource
    const disabledSut: SutResource = {
      ...firstSut,
      databases: [{ ...database, status: "disabled" }],
    }
    expect(scopeDatabases({ ...environment, suts: [disabledSut] })).toHaveLength(0)
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

  it("builds the account_ids-only binding update and keeps explicit empty lists", () => {
    // null means the pool could not be read: omit account_ids entirely.
    expect(buildBindingUpdate({ accountIds: null })).toEqual({})
    expect(buildBindingUpdate({ accountIds: ["1601"] })).toEqual({ account_ids: ["1601"] })
    // An explicit empty list clears the selection.
    expect(buildBindingUpdate({ accountIds: [] })).toEqual({ account_ids: [] })
  })

  it("toggles selections without reordering", () => {
    expect(toggleSelection(["1", "2"], "3")).toEqual(["1", "2", "3"])
    expect(toggleSelection(["1", "2"], "1")).toEqual(["2"])
  })

  it("maps typed binding fields back to the wizard steps", () => {
    expect(stepForBindingField("environment_id")).toBe(1)
    expect(stepForBindingField("account_ids")).toBe(2)
    expect(stepForBindingField("account_ids[0]")).toBe(2)
    expect(stepForBindingField("title")).toBe(1)
    expect(stepForBindingField("")).toBe(4)
  })
})
