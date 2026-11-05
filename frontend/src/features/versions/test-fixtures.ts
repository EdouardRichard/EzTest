import type {
  AccountResource,
  ApiResource,
  Catalog,
  CodeRepositoryResource,
  DatabaseResource,
  EnvironmentResource,
  PageResource,
  SutRepository,
  SutResource,
  Version,
  VersionBindingOverview,
  VersionSummary,
} from "./types"

const TIMESTAMP = "2026-10-31T10:00:00Z"

const orderPages: PageResource[] = [
  {
    id: "1301",
    sut_id: "1001",
    environment_id: "1101",
    name: "订单列表",
    url: "https://order.test/orders",
    route_template: "/orders",
    page_type: "page",
    auth_role: "",
    module: "订单",
    status: "approved",
    source: "admin",
    created_at: TIMESTAMP,
    updated_at: TIMESTAMP,
  },
  {
    id: "1302",
    sut_id: "1001",
    environment_id: "1101",
    name: "订单详情",
    url: "https://order.test/orders/{id}",
    route_template: "/orders/{id}",
    page_type: "page",
    auth_role: "",
    module: "订单",
    status: "approved",
    source: "admin",
    created_at: TIMESTAMP,
    updated_at: TIMESTAMP,
  },
]

const orderApis: ApiResource[] = [
  {
    id: "1401",
    sut_id: "1001",
    environment_id: "1101",
    method: "GET",
    path_template: "/api/orders",
    name: "查询订单",
    auth_role: "",
    source: "manual",
    status: "approved",
    schema_ref: "",
    created_at: TIMESTAMP,
    updated_at: TIMESTAMP,
  },
]

const orderDatabases: DatabaseResource[] = [
  {
    id: "1501",
    sut_id: "1001",
    environment_id: "1101",
    name: "order_db",
    dsn: "postgres://order-db:5432/order",
    username: "order",
    password_set: true,
    read_only: true,
    status: "active",
    created_at: TIMESTAMP,
    updated_at: TIMESTAMP,
  },
]

const orderAccounts: AccountResource[] = [
  {
    id: "1601",
    sut_id: "1001",
    environment_id: "1101",
    username: "buyer",
    roles: ["buyer"],
    notes: "",
    password_set: true,
    status: "active",
    created_at: TIMESTAMP,
    updated_at: TIMESTAMP,
  },
  {
    id: "1602",
    sut_id: "1001",
    environment_id: "1101",
    username: "operator",
    roles: ["operator"],
    notes: "",
    password_set: true,
    status: "active",
    created_at: TIMESTAMP,
    updated_at: TIMESTAMP,
  },
]

const orderEnvironment: EnvironmentResource = {
  id: "1101",
  sut_id: "1001",
  name: "测试环境",
  base_url: "https://order.test",
  is_default: true,
  status: "active",
  pages: orderPages,
  apis: orderApis,
  databases: orderDatabases,
  accounts: orderAccounts,
  created_at: TIMESTAMP,
  updated_at: TIMESTAMP,
}

const orderStagingEnvironment: EnvironmentResource = {
  id: "1102",
  sut_id: "1001",
  name: "预发环境",
  base_url: "https://order-staging.test",
  is_default: false,
  status: "active",
  pages: [],
  apis: [],
  databases: [],
  accounts: [],
  created_at: TIMESTAMP,
  updated_at: TIMESTAMP,
}

const orderSut: SutResource = {
  id: "1001",
  name: "订单中心",
  intro: "订单创建、查询与退款",
  environments: [orderEnvironment, orderStagingEnvironment],
  created_at: TIMESTAMP,
  updated_at: TIMESTAMP,
}

const paymentEnvironment: EnvironmentResource = {
  id: "2101",
  sut_id: "1002",
  name: "联调环境",
  base_url: "https://pay.test",
  is_default: true,
  status: "active",
  pages: [
    {
      id: "2301",
      sut_id: "1002",
      environment_id: "2101",
      name: "支付结果",
      url: "https://pay.test/payments",
      route_template: "/payments",
      page_type: "page",
      auth_role: "",
      module: "支付",
      status: "approved",
      source: "admin",
      created_at: TIMESTAMP,
      updated_at: TIMESTAMP,
    },
  ],
  apis: [
    {
      id: "2401",
      sut_id: "1002",
      environment_id: "2101",
      method: "POST",
      path_template: "/api/payments",
      name: "发起支付",
      auth_role: "",
      source: "manual",
      status: "approved",
      schema_ref: "",
      created_at: TIMESTAMP,
      updated_at: TIMESTAMP,
    },
  ],
  databases: [],
  accounts: [],
  created_at: TIMESTAMP,
  updated_at: TIMESTAMP,
}

const paymentSut: SutResource = {
  id: "1002",
  name: "支付网关",
  intro: "支付与退款",
  environments: [paymentEnvironment],
  created_at: TIMESTAMP,
  updated_at: TIMESTAMP,
}

const orderRepository: CodeRepositoryResource = {
  id: "1201",
  name: "order-center-web",
  path: "/repos/order-center-web",
  branch: "main",
  include: [],
  exclude: [],
  created_by: "9001",
  status: "active",
  created_at: TIMESTAMP,
  updated_at: TIMESTAMP,
}

export const catalogFixture: Catalog = {
  suts: [orderSut, paymentSut],
  repositories: [
    orderRepository,
    {
      id: "1202",
      name: "payment-gateway",
      path: "/repos/payment-gateway",
      branch: "main",
      include: [],
      exclude: [],
      created_by: "9001",
      status: "active",
      created_at: TIMESTAMP,
      updated_at: TIMESTAMP,
    },
  ],
}

export const versionSummaryFixture: VersionSummary = {
  id: "2001",
  title: "订单中心 · 2026 Q4 迭代",
  status: "running",
  sut_count: 2,
  created_at: TIMESTAMP,
  updated_at: TIMESTAMP,
}

export const versionFixture: Version = {
  id: "2001",
  title: "订单中心 · 2026 Q4 迭代",
  status: "running",
  auto_design_new_requirements: false,
  bindings: [
    {
      id: "3001",
      sut: orderSut,
      environment: orderEnvironment,
      repository: orderRepository,
      pages: orderPages,
      apis: orderApis,
      databases: orderDatabases,
      accounts: orderAccounts,
    },
    {
      id: "3002",
      sut: paymentSut,
      environment: paymentEnvironment,
      repository: null,
      pages: paymentEnvironment.pages,
      apis: paymentEnvironment.apis,
      databases: [],
      accounts: [],
    },
  ],
  created_by: "9001",
  created_at: TIMESTAMP,
  updated_at: TIMESTAMP,
}

/** SUT-level repository associations returned by API-17 (Task 29 service view). */
export const orderSutRepositories: SutRepository[] = [
  {
    id: "3101",
    sut_id: "1001",
    repository: orderRepository,
    role: "web",
    enabled: true,
    created_at: TIMESTAMP,
    updated_at: TIMESTAMP,
  },
  {
    id: "3102",
    sut_id: "1001",
    repository: catalogFixture.repositories[1] as CodeRepositoryResource,
    role: "api",
    enabled: true,
    created_at: TIMESTAMP,
    updated_at: TIMESTAMP,
  },
]

/**
 * One Task 29 `GET /api/versions/{id}/binding` read model: derived SUT,
 * environment, effective repos/accounts and an excluded page + database.
 */
export const bindingOverviewFixture: VersionBindingOverview = {
  version_id: "2001",
  stage: "designing",
  editable: true,
  sut: orderSut,
  environment: orderEnvironment,
  scope_exclude: {
    pages: ["1302"],
    apis: [],
    databases: ["1501"],
  },
  counts: {
    pages: { approved: 2, proposed: 0, excluded: 1, included: 1 },
    apis: { approved: 1, proposed: 0, excluded: 0, included: 1 },
    databases: { approved: 1, proposed: 0, excluded: 1, included: 0 },
  },
  pages: [orderPages[0] as PageResource],
  apis: [orderApis[0] as ApiResource],
  databases: [],
  accounts: [orderAccounts[0] as AccountResource],
  repositories: [
    {
      repository: orderRepository,
      role: "web",
      included: true,
      source: "sut",
    },
    {
      repository: catalogFixture.repositories[1] as CodeRepositoryResource,
      role: "api",
      included: true,
      source: "override",
    },
    {
      repository: {
        ...(catalogFixture.repositories[1] as CodeRepositoryResource),
        id: "1203",
        name: "payment-infra",
      },
      role: "infra",
      included: false,
      source: "sut",
    },
  ],
  code_baseline_commit: "abc123",
}
