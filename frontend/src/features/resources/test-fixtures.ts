import type {
  CodeRepository,
  Sut,
  SutApi,
  SutEnvironment,
  SutPage,
  TargetDatabase,
  TestAccount,
} from "./types"

export const FIXTURE_TIMESTAMP = "2026-10-31T10:00:00Z"

export const pageFixture: SutPage = {
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
  created_at: FIXTURE_TIMESTAMP,
  updated_at: FIXTURE_TIMESTAMP,
}

export const apiFixture: SutApi = {
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
  created_at: FIXTURE_TIMESTAMP,
  updated_at: FIXTURE_TIMESTAMP,
}

export const databaseFixture: TargetDatabase = {
  id: "1501",
  sut_id: "1001",
  environment_id: "1101",
  name: "order_db",
  dsn: "postgres://order-db:5432/order",
  username: "order",
  password_set: true,
  read_only: true,
  status: "active",
  created_at: FIXTURE_TIMESTAMP,
  updated_at: FIXTURE_TIMESTAMP,
}

export const accountFixture: TestAccount = {
  id: "1601",
  sut_id: "1001",
  environment_id: "1101",
  username: "buyer",
  roles: ["buyer"],
  notes: "",
  password_set: true,
  status: "active",
  created_at: FIXTURE_TIMESTAMP,
  updated_at: FIXTURE_TIMESTAMP,
}

export const environmentFixture: SutEnvironment = {
  id: "1101",
  sut_id: "1001",
  name: "测试环境",
  base_url: "https://order.test",
  is_default: true,
  status: "active",
  pages: [pageFixture],
  apis: [apiFixture],
  databases: [databaseFixture],
  accounts: [accountFixture],
  created_at: FIXTURE_TIMESTAMP,
  updated_at: FIXTURE_TIMESTAMP,
}

export const secondEnvironmentFixture: SutEnvironment = {
  ...environmentFixture,
  id: "1102",
  name: "预发环境",
  base_url: "https://pre.order.test",
  is_default: false,
  pages: [],
  apis: [],
  databases: [],
  accounts: [],
}

export const sutFixture: Sut = {
  id: "1001",
  name: "订单中心",
  intro: "订单主链路",
  environments: [environmentFixture, secondEnvironmentFixture],
  created_at: FIXTURE_TIMESTAMP,
  updated_at: FIXTURE_TIMESTAMP,
}

export const sutListItemFixture: Sut = {
  ...sutFixture,
  environments: [],
}

export const secondSutFixture: Sut = {
  id: "1002",
  name: "支付网关",
  intro: "支付主链路",
  environments: [],
  created_at: FIXTURE_TIMESTAMP,
  updated_at: FIXTURE_TIMESTAMP,
}

export const repositoryFixture: CodeRepository = {
  id: "1201",
  name: "order-center-web",
  path: "D:/code/order-center-web",
  branch: "main",
  include: ["src/**"],
  exclude: ["node_modules/**"],
  created_by: "9001",
  status: "active",
  created_at: FIXTURE_TIMESTAMP,
  updated_at: FIXTURE_TIMESTAMP,
}
