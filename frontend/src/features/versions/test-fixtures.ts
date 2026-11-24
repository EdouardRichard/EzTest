import type {
  AccountResource,
  ApiResource,
  BindingRepositoryRow,
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

// v0.2.4-B hierarchy inversion: assets carry only sut_id; a SUT owns its assets
// and belongs to one environment; the environment owns the SUT catalog.

const orderPages: PageResource[] = [
  {
    id: "1301",
    sut_id: "1001",
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
    username: "operator",
    roles: ["operator"],
    notes: "",
    password_set: true,
    status: "active",
    created_at: TIMESTAMP,
    updated_at: TIMESTAMP,
  },
]

const paymentPages: PageResource[] = [
  {
    id: "2301",
    sut_id: "1002",
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
]

const paymentApis: ApiResource[] = [
  {
    id: "2401",
    sut_id: "1002",
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
]

const orderSut: SutResource = {
  id: "1001",
  environment_id: "1101",
  name: "订单中心",
  intro: "订单创建、查询与退款",
  base_url: "https://order.test",
  pages: orderPages,
  apis: orderApis,
  databases: orderDatabases,
  created_at: TIMESTAMP,
  updated_at: TIMESTAMP,
}

const paymentSut: SutResource = {
  id: "1002",
  environment_id: "1101",
  name: "支付网关",
  intro: "支付与退款",
  base_url: "https://pay.test",
  pages: paymentPages,
  apis: paymentApis,
  databases: [],
  created_at: TIMESTAMP,
  updated_at: TIMESTAMP,
}

const orderEnvironment: EnvironmentResource = {
  id: "1101",
  name: "测试环境",
  description: "订单与支付联调环境",
  status: "active",
  suts: [orderSut, paymentSut],
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

const paymentRepository: CodeRepositoryResource = {
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
}

export const catalogFixture: Catalog = {
  environments: [orderEnvironment],
  repositories: [orderRepository, paymentRepository],
  accounts: orderAccounts,
}

export const versionSummaryFixture: VersionSummary = {
  id: "2001",
  title: "订单中心 · 2026 Q4 迭代",
  status: "running",
  sut_count: 2,
  created_at: TIMESTAMP,
  updated_at: TIMESTAMP,
}

const bindingRepositories: BindingRepositoryRow[] = [
  {
    repository: orderRepository,
    sut_id: "1001",
    role: "web",
    code_baseline_commit: "abc123",
  },
  {
    repository: paymentRepository,
    sut_id: "1002",
    role: "api",
    code_baseline_commit: null,
  },
]

export const versionFixture: Version = {
  id: "2001",
  title: "订单中心 · 2026 Q4 迭代",
  status: "running",
  auto_design_new_requirements: false,
  environment_id: "1101",
  binding: {
    environment: orderEnvironment,
    pages: [...orderPages, ...paymentPages],
    apis: [...orderApis, ...paymentApis],
    databases: orderDatabases,
    accounts: orderAccounts,
    repositories: bindingRepositories,
    code_baseline_commit: "abc123",
  },
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
    repository: paymentRepository,
    role: "api",
    enabled: true,
    created_at: TIMESTAMP,
    updated_at: TIMESTAMP,
  },
]

/**
 * One `GET /api/versions/{id}/binding` read model (v0.2.4-B). The bound
 * environment owns the SUT catalog; counts are approved/proposed only (the
 * version_sut exclusion chain is gone, so approved is also the included count).
 */
export const bindingOverviewFixture: VersionBindingOverview = {
  version_id: "2001",
  environment_id: "1101",
  stage: "designing",
  editable: true,
  environment: orderEnvironment,
  counts: {
    pages: { approved: 3, proposed: 0 },
    apis: { approved: 2, proposed: 0 },
    databases: { approved: 1, proposed: 0 },
  },
  pages: [...orderPages, ...paymentPages],
  apis: [...orderApis, ...paymentApis],
  databases: orderDatabases,
  accounts: orderAccounts,
  repositories: bindingRepositories,
  code_baseline_commit: "abc123",
  snapshot: {
    scope_version: "scope-v1",
    frozen_wave_id: null,
  },
}
