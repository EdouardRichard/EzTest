import { describe, expect, it } from "vitest"

import {
  parseApisImport,
  parsePagesImport,
  splitList,
  validateAccountForm,
  validateApiForm,
  validateDatabaseForm,
  validateEnvironmentForm,
  validatePageForm,
  validateRepositoryForm,
  validateSutForm,
} from "./validation"

const validSut = {
  name: "订单中心",
  intro: "订单主链路",
  environmentName: "测试环境",
  baseUrl: "https://order.test",
}

describe("validateSutForm", () => {
  it("accepts a complete SUT with its default environment", () => {
    expect(validateSutForm(validSut)).toEqual({})
  })

  it("requires the SUT name, environment name and base URL", () => {
    const errors = validateSutForm({
      name: "  ",
      intro: "",
      environmentName: "",
      baseUrl: "",
    })

    expect(errors.name).toBe("请输入 SUT 名称")
    expect(errors.environmentName).toBe("请输入默认环境名称")
    expect(errors.baseUrl).toBe("请输入环境地址")
  })

  it("rejects a base URL without an http(s) scheme", () => {
    expect(validateSutForm({ ...validSut, baseUrl: "order.test" }).baseUrl).toBe(
      "环境地址必须是完整的 http(s) 地址",
    )
    expect(validateSutForm({ ...validSut, baseUrl: "ftp://order.test" }).baseUrl).toBe(
      "环境地址必须是完整的 http(s) 地址",
    )
  })

  it("enforces the SUT name and intro limits", () => {
    expect(validateSutForm({ ...validSut, name: "x".repeat(101) }).name).toBe(
      "SUT 名称不能超过 100 个字符",
    )
    expect(validateSutForm({ ...validSut, intro: "x".repeat(2001) }).intro).toBe(
      "简介不能超过 2000 个字符",
    )
  })
})

describe("validateEnvironmentForm", () => {
  const validEnvironment = {
    name: "预发环境",
    baseUrl: "https://pre.order.test",
    isDefault: false,
    status: "active" as const,
  }

  it("accepts a complete environment", () => {
    expect(validateEnvironmentForm(validEnvironment)).toEqual({})
  })

  it("requires a name and a valid base URL", () => {
    const errors = validateEnvironmentForm({ ...validEnvironment, name: "", baseUrl: "//bad" })
    expect(errors.name).toBe("请输入环境名称")
    expect(errors.baseUrl).toBe("环境地址必须是完整的 http(s) 地址")
  })
})

describe("validatePageForm", () => {
  const validPage = {
    name: "订单列表",
    url: "https://order.test/orders",
    routeTemplate: "",
    pageType: "page",
    authRole: "",
    module: "订单",
  }

  it("accepts a page without a route template (derived from the URL path)", () => {
    expect(validatePageForm(validPage)).toEqual({})
  })

  it("requires the page name and a complete http(s) URL", () => {
    const errors = validatePageForm({ ...validPage, name: " ", url: "/orders" })
    expect(errors.name).toBe("请输入页面名称")
    expect(errors.url).toBe("页面 URL 必须是完整的 http(s) 地址")
  })

  it("requires a provided route template to start with /", () => {
    expect(validatePageForm({ ...validPage, routeTemplate: "orders" })).toEqual({
      routeTemplate: "路由模板必须以 / 开头",
    })
    expect(validatePageForm({ ...validPage, routeTemplate: "/orders/{id}" })).toEqual({})
  })
})

describe("validateApiForm", () => {
  const validApi = {
    name: "查询订单",
    method: "GET" as const,
    pathTemplate: "/api/orders",
    authRole: "",
    schemaRef: "",
  }

  it("accepts a complete API definition", () => {
    expect(validateApiForm(validApi)).toEqual({})
  })

  it("rejects an unknown HTTP method", () => {
    expect(validateApiForm({ ...validApi, method: "FETCH" as never }).method).toBe(
      "请选择有效的 HTTP 方法",
    )
  })

  it("requires the path template to start with /", () => {
    expect(validateApiForm({ ...validApi, pathTemplate: "api/orders" }).pathTemplate).toBe(
      "API 路径模板必须以 / 开头",
    )
    expect(validateApiForm({ ...validApi, pathTemplate: " " }).pathTemplate).toBe(
      "请输入 API 路径模板",
    )
  })
})

describe("validateDatabaseForm", () => {
  const validDatabase = {
    environmentId: "1101",
    name: "order_db",
    dsn: "postgres://order-db:5432/order",
    username: "order",
    password: "",
    readOnly: true,
  }

  it("accepts postgres and postgresql DSNs", () => {
    expect(validateDatabaseForm(validDatabase)).toEqual({})
    expect(validateDatabaseForm({ ...validDatabase, dsn: "postgresql://order-db/order" })).toEqual(
      {},
    )
  })

  it("rejects other DSN schemes and missing fields", () => {
    const errors = validateDatabaseForm({
      ...validDatabase,
      environmentId: "",
      dsn: "mysql://order-db/order",
      username: "",
    })
    expect(errors.environmentId).toBe("请选择环境")
    expect(errors.dsn).toBe("DSN 必须以 postgres:// 或 postgresql:// 开头")
    expect(errors.username).toBe("请输入数据库用户名")
  })

  it("caps the password length", () => {
    expect(validateDatabaseForm({ ...validDatabase, password: "x".repeat(1001) }).password).toBe(
      "密码不能超过 1000 个字符",
    )
  })
})

describe("validateAccountForm", () => {
  const validAccount = {
    environmentId: "1101",
    username: "buyer",
    password: "secret",
    roles: "buyer, order_admin",
    notes: "",
  }

  it("requires an initial password when creating", () => {
    expect(validateAccountForm(validAccount, { requirePassword: true })).toEqual({})
    expect(
      validateAccountForm({ ...validAccount, password: "" }, { requirePassword: true }).password,
    ).toBe("请输入初始密码")
  })

  it("allows an empty password when only updating", () => {
    expect(validateAccountForm({ ...validAccount, password: "" })).toEqual({})
  })

  it("caps roles at 20 entries and notes at 2000 characters", () => {
    const roles = Array.from({ length: 21 }, (_, index) => `role${index}`).join(",")
    expect(validateAccountForm({ ...validAccount, roles }).roles).toBe("角色不能超过 20 个")
    expect(validateAccountForm({ ...validAccount, notes: "x".repeat(2001) }).notes).toBe(
      "备注不能超过 2000 个字符",
    )
  })
})

describe("validateRepositoryForm", () => {
  const validRepository = {
    name: "order-center-web",
    path: "D:/code/order-center-web",
    branch: "main",
    include: "src/**",
    exclude: "node_modules/**, dist/**",
  }

  it("accepts a complete repository", () => {
    expect(validateRepositoryForm(validRepository)).toEqual({})
  })

  it("requires a name and a path", () => {
    expect(validateRepositoryForm({ ...validRepository, name: "", path: " " })).toEqual({
      name: "请输入仓库名称",
      path: "请输入仓库路径",
    })
  })

  it("caps include and exclude rule counts", () => {
    const tooMany = Array.from({ length: 101 }, (_, index) => `src/${index}`).join(",")
    expect(validateRepositoryForm({ ...validRepository, include: tooMany }).include).toBe(
      "包含规则不能超过 100 条",
    )
    expect(validateRepositoryForm({ ...validRepository, exclude: tooMany }).exclude).toBe(
      "排除规则不能超过 100 条",
    )
  })
})

describe("splitList", () => {
  it("splits on commas and newlines and drops blank entries", () => {
    expect(splitList(" buyer, order_admin\nqa \n\n")).toEqual(["buyer", "order_admin", "qa"])
    expect(splitList("  ")).toEqual([])
  })
})

describe("parsePagesImport", () => {
  it("accepts a bare array and normalizes page defaults", () => {
    const result = parsePagesImport(
      JSON.stringify([
        { name: " 订单列表 ", url: " https://order.test/orders " },
        { name: "订单详情", url: "https://order.test/orders/{id}", route_template: "/orders/{id}" },
      ]),
    )

    expect(result).toEqual({
      ok: true,
      items: [
        {
          name: "订单列表",
          url: "https://order.test/orders",
          routeTemplate: "",
          pageType: "page",
          authRole: "",
          module: "",
        },
        {
          name: "订单详情",
          url: "https://order.test/orders/{id}",
          routeTemplate: "/orders/{id}",
          pageType: "page",
          authRole: "",
          module: "",
        },
      ],
    })
  })

  it("accepts the {items: [...]} form used by the API", () => {
    const result = parsePagesImport(
      JSON.stringify({ items: [{ name: "订单列表", url: "https://order.test/orders" }] }),
    )

    expect(result.ok).toBe(true)
    if (result.ok) expect(result.items).toHaveLength(1)
  })

  it("reports invalid JSON, an empty list and a non-array payload", () => {
    expect(parsePagesImport("   ")).toEqual({
      ok: false,
      error: '请粘贴要导入的 JSON（支持数组或 {"items":[]}）',
    })
    expect(parsePagesImport("{").ok).toBe(false)
    expect(parsePagesImport("{}").ok).toBe(false)
    expect(parsePagesImport("[]").ok).toBe(false)
    expect(parsePagesImport(JSON.stringify({ items: [] })).ok).toBe(false)
  })

  it("reports the one-based index of an invalid item", () => {
    const result = parsePagesImport(
      JSON.stringify([
        { name: "订单列表", url: "https://order.test/orders" },
        { name: "订单详情" },
      ]),
    )

    expect(result).toEqual({ ok: false, error: "第 2 项：请输入页面 URL" })
  })

  it("reports a non-object item", () => {
    expect(parsePagesImport(JSON.stringify(["nope"]))).toEqual({
      ok: false,
      error: "第 1 项：必须是 JSON 对象",
    })
  })
})

describe("parseApisImport", () => {
  it("accepts API items and normalizes the method to upper case", () => {
    const result = parseApisImport(
      JSON.stringify([
        { name: "查询订单", method: "get", path_template: "/api/orders" },
        { name: "创建订单", method: "POST", path_template: "/api/orders" },
      ]),
    )

    expect(result).toEqual({
      ok: true,
      items: [
        {
          name: "查询订单",
          method: "GET",
          pathTemplate: "/api/orders",
          authRole: "",
          schemaRef: "",
        },
        {
          name: "创建订单",
          method: "POST",
          pathTemplate: "/api/orders",
          authRole: "",
          schemaRef: "",
        },
      ],
    })
  })

  it("reports the index of an invalid method and path", () => {
    expect(
      parseApisImport(
        JSON.stringify([
          { name: "查询订单", method: "GET", path_template: "/api/orders" },
          { name: "删除订单", method: "FETCH", path_template: "/api/orders" },
        ]),
      ),
    ).toEqual({ ok: false, error: "第 2 项：请选择有效的 HTTP 方法" })

    expect(
      parseApisImport(JSON.stringify([{ name: "查询订单", method: "GET", path_template: "x" }])),
    ).toEqual({ ok: false, error: "第 1 项：API 路径模板必须以 / 开头" })
  })

  it("uses GET when the method is omitted and reports a missing name", () => {
    const result = parseApisImport(
      JSON.stringify([{ name: "查询订单", path_template: "/api/orders" }]),
    )
    expect(result.ok).toBe(true)
    if (result.ok) expect(result.items[0]?.method).toBe("GET")

    expect(parseApisImport(JSON.stringify([{ path_template: "/api/orders" }]))).toEqual({
      ok: false,
      error: "第 1 项：请输入 API 名称",
    })
  })
})
