# EzTest

EzTest 的 P0 基础阶段（foundation）实现：Go 后端 API、Next.js 管理前端、PostgreSQL 持久化，以及 SSE 事件流、对象存储、审计、LLM 配置管理等基础能力。

- 分支：`feat/p0-foundation`
- 阶段规格：[`docs/superpowers/specs/2026-10-27-eztest-p0-foundation-design.md`](docs/superpowers/specs/2026-10-27-eztest-p0-foundation-design.md)
- OpenAPI：[`docs/openapi/p0.yaml`](docs/openapi/p0.yaml)（公开端点 `GET /api/openapi.yaml`）
- 设计权威：[`docs/design/system-blueprint.md`](docs/design/system-blueprint.md)；实现约定见 [`Agent.md`](Agent.md)

## P0 范围

已实现：

- 账号与认证：单管理员种子、登录/登出/当前用户、Bearer token（只存 SHA-256 哈希）、登录失败限流。
- 用户管理：管理员创建普通用户、列表/分页、启用/禁用、重置密码（撤销该用户全部 token）。
- LLM 配置管理：CRUD、AES-256-GCM 加密存储 Key/headers、模型目录刷新、非流式/流式连通性测试（含 1×1 PNG 多模态）。
- 事件流：`system` 频道事件快照、SSE 实时流、断线按 `Last-Event-ID` 回放、`stream.reset`、20s 心跳、管理端测试事件发布。
- 对象存储：内容寻址（SHA-256）、本地文件系统、10 MiB 上限、下载时完整性校验。
- 审计：管理员查询、登录/用户/LLM/存储/测试事件动作记录。
- 统一约定：`{"data": ...}` 成功信封、统一错误信封与错误码、`X-Request-ID`、幂等键、CORS、健康检查、OpenAPI。
- 本地开发脚本、E2E、P0 验收入口；Docker/Compose 交付文件与静态校验。

P0 **不包含**版本/需求/用例/执行/证据/问题/报告等业务实体与页面，也不包含 Agent 运行时、代码仓库管理、多实例/多租户等能力（详见阶段规格 §16）。

## 技术架构

### 后端（`backend/`）

- Go 1.24，标准库 `net/http`（Go 1.22+ 方法路由），`pgx/v5` 连接 PostgreSQL 17。
- DDD 限界上下文：`identity`、`eventing`、`llm`、`storage`、`audit`；每个上下文四层（`domain` / `application` / `infrastructure` / `interfaces/httpapi`），导入方向约束见 `Agent.md` §6.2。
- `cmd/eztest` 是唯一组装根：读取配置、连接数据库、执行迁移、种子管理员与默认 LLM 配置、装配 HTTP 服务、优雅退出。
- `internal/server` 只装配路由与中间件：recover → request ID → 日志 → CORS → 认证 → 幂等 → 路由。
- `internal/platform` 放共享的纯基础设施：错误信封、分页游标、JSON 解码、请求 ID、AES keyring、幂等存储。
- 迁移嵌入二进制，启动时在 advisory lock 下按版本顺序事务执行；重复启动无副作用。
- 启动时自动执行迁移，无需单独 migrate 命令；`schema_migrations` 记录版本与校验和。

### 前端（`frontend/`）

- Next.js 16 App Router、React 19、TypeScript、Tailwind CSS 4、Radix 原语、`next-themes`、lucide-react。
- `src/app/` 只做路由/布局/页面组装；业务在 `src/features/{auth,users,llm-settings,sse-test,shell}`。
- 所有 HTTP 请求经 `src/lib/api-client.ts`（Bearer、错误信封、`X-Request-ID`、幂等键）；SSE 经 `src/lib/sse-client.ts`（fetch 流、`Last-Event-ID`、退避重连、心跳超时）。
- 质量工具：Biome（lint/format）、Vitest + Testing Library、Playwright（使用本机 Chrome/Edge channel，不下载 Chromium）。

### 部署（`deploy/`）

- `Dockerfile.api`：Go 多阶段构建，Alpine 运行时，非 root 用户，镜像内带 `docs/openapi/p0.yaml`。
- `Dockerfile.web`：Next.js `standalone` 多阶段构建，非 root 用户，监听 6868。
- `docker-compose.yml`：`api`（13333）、`web`（6868）、`postgres:17`（5433），健康检查、`pgdata`/`objects` 卷、启动顺序。
- 本机没有 Docker，Compose 只做静态校验，**未运行过容器**；详见 [`deploy/README.md`](deploy/README.md)。

## 目录结构

```text
EzTest/
├─ backend/
│  ├─ go.mod
│  ├─ cmd/eztest/main.go              # 唯一组装根
│  └─ internal/
│     ├─ config/                      # env 解析/校验
│     ├─ db/                          # pgx pool、事务、嵌入式迁移
│     │  └─ migrations/
│     ├─ server/                      # 路由装配、中间件链
│     ├─ platform/                    # 错误/分页/请求 ID/crypto/幂等
│     ├─ identity/                    # 用户/认证（四层）
│     ├─ eventing/                    # 事件/SSE（四层）
│     ├─ llm/                         # LLM 配置/网关（四层）
│     ├─ storage/                     # 对象存储（四层）
│     └─ audit/                       # 审计（四层）
├─ frontend/
│  ├─ src/app/                        # Next.js App Router 页面组装
│  ├─ src/features/                   # auth/users/llm-settings/sse-test/shell
│  ├─ src/lib/                        # api-client / sse-client / auth-storage
│  ├─ scripts/validate-compose.mjs    # Compose 静态校验
│  └─ e2e/                            # Playwright
├─ deploy/                            # Dockerfile.api / Dockerfile.web / docker-compose.yml
├─ scripts/                           # db/dev-api/dev-web/test/e2e/acceptance PowerShell 脚本
├─ docs/openapi/p0.yaml               # 手工维护的 OpenAPI 3.0
├─ .env.example                       # 环境变量模板（无真实秘密）
├─ .tools/                            # 便携 Go/PostgreSQL（gitignored）
└─ README.md
```

## 本地工具链（Windows）

要求：Windows 10/11、PowerShell 5.1+（`powershell.exe`；本机没有 `pwsh` 时所有脚本仍可运行）、Node.js 22+、pnpm 10/11。

仓库自带便携工具链脚本（`gitignored` 的 `.tools/`）：

```powershell
# 1) 复制环境变量模板并填写占位符（生成 EZTEST_SECRET_KEYS 的片段见下节）
Copy-Item .env.example .env

# 2) 首次准备便携 Go 1.24 + PostgreSQL 17，初始化/启动 PG 并创建 eztest、eztest_test
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .tools/setup-toolchain.ps1

# 或：工具链已存在时用仓库脚本管理 PG
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/db.ps1 init
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/db.ps1 status
```

已有本机工具链时，日常只需：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/db.ps1 start
```

数据库端口固定为 `5433`；`eztest` 为开发库，`eztest_test` 为集成测试库。

## 环境变量

完整模板见 [`.env.example`](.env.example)。`.env`、`.tools/`、`docs/*.local.md` 均被 gitignore，**禁止提交真实密钥**。

| 变量 | 必填 | 说明 |
|---|---|---|
| `EZTEST_ENV` | 否 | `dev`（默认）/ `prod`；`prod` 默认关闭测试端点。 |
| `EZTEST_HTTP_ADDR` | 否 | API 监听地址，默认 `:13333`。 |
| `EZTEST_WEB_ORIGIN` | 否 | CORS 允许的前端 origin，默认 `http://localhost:6868`。 |
| `EZTEST_DATABASE_URL` | 是 | PostgreSQL URL；本地为 `postgres://eztest:<password>@localhost:5433/eztest?sslmode=disable`。 |
| `EZTEST_TEST_DATABASE_URL` | 集成测试 | 独立测试库 URL（`eztest_test`），不会碰开发库。 |
| `EZTEST_ADMIN_USERNAME` / `EZTEST_ADMIN_PASSWORD` | 首次启动 | 种子管理员；密码至少 10 字符，仅存 bcrypt 哈希。 |
| `EZTEST_SECRET_KEYS` | 是 | JSON `{"版本":"base64 32 字节密钥"}`，用于加密 LLM Key/headers。 |
| `EZTEST_SECRET_KEY_ACTIVE` | 是 | 当前加密密钥版本，必须在 `EZTEST_SECRET_KEYS` 中存在。 |
| `EZTEST_STORAGE_DIR` | 否 | 对象根目录，默认 `.data/objects`（容器内 `/data/objects`）。 |
| `EZTEST_ENABLE_TEST_ENDPOINTS` | 否 | 是否注册 `POST /api/admin/stream/test-events`，dev 默认 true。 |
| `EZTEST_LOG_LEVEL` | 否 | `debug` / `info` / `warn` / `error`。 |
| `EZTEST_OPENAPI_PATH` | 否 | OpenAPI 文件路径，默认 `docs/openapi/p0.yaml`；相对路径以进程工作目录为准，`dev-api.ps1` 会设为绝对路径。 |
| `llm_base_url` / `llm_key` / `llm_model_name` | 否 | 首次启动的可选默认 LLM 种子；小写变量名是设计约定，Key 留空则不发起真实调用。 |
| `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB` | Compose | 仅 `deploy/docker-compose.yml` 使用；`POSTGRES_PASSWORD` 必填。 |
| `NEXT_PUBLIC_API_BASE_URL` | 否 | 浏览器访问的 API origin，默认 `http://localhost:13333`；Compose 构建时内联。 |

生成 `EZTEST_SECRET_KEYS`（PowerShell，32 字节随机密钥）：

```powershell
$bytes = New-Object byte[] 32
[System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
'{"1":"' + [Convert]::ToBase64String($bytes) + '"}'
```

把输出整行写入 `.env`，并保持 `EZTEST_SECRET_KEY_ACTIVE=1`。轮换时新增版本（如 `"2"`）再提升 active，旧版本需保留以便解密历史数据。

## 本地启动

```powershell
# 终端 1：API（自动执行迁移、种子管理员；13333）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/dev-api.ps1

# 终端 2：前端（6868；首次先 cd frontend; pnpm install）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/dev-web.ps1
```

打开：

- Web：<http://localhost:6868>
- API 健康：<http://localhost:13333/api/health>
- OpenAPI：<http://localhost:13333/api/openapi.yaml>

等价的底层命令：

```powershell
# API（必须从 backend/ 运行；OpenAPI 路径需按需设置）
cd backend
..\.tools\go\bin\go.exe run ./cmd/eztest

# 前端
cd frontend
pnpm dev
```

首次登录使用 `EZTEST_ADMIN_USERNAME` / `EZTEST_ADMIN_PASSWORD`（默认用户 `admin`）。

### 开发管理员密码

本地管理员密码只存在于两处，二者都被 gitignore：

- 仓库根 `.env` 的 `EZTEST_ADMIN_PASSWORD`（Compose/脚本读取）；
- 本机生成的 `docs/dev-setup.local.md`（`.tools/gen-env.ps1` 首次生成时写入，仅本机可见）。

数据库中只存 bcrypt 哈希；API 响应、日志、审计与错误信封都不会回显密码。不要把这两个文件提交或粘贴到 issue/PR。

## API 概览

基础约定（详见 OpenAPI 与阶段规格 §9）：

- 成功：`{"data": ...}`；列表：`{"data":[...],"next_cursor":"...","has_more":true}`。
- 错误：`{"error":{"code","message","request_id","retryable","details"}}`；错误码为闭集（`invalid_argument`、`unauthenticated`、`forbidden`、`not_found`、`conflict`、`idempotency_conflict`、`rate_limited`、`internal`、`unavailable`、`storage_corrupted`、`llm_*`）。
- 认证：`Authorization: Bearer <token>`；登录返回的 token 只出现一次。
- `X-Request-ID` 可传入 `^[A-Za-z0-9_-]{8,128}$`，否则生成 UUID；响应回显。
- 认证写接口支持 `Idempotency-Key`（24h 重放，同 key 不同 body 为 409）。
- CORS 只允许 `EZTEST_WEB_ORIGIN`；SSE 支持 `Last-Event-ID`。

端点分组：

| 分组 | 端点 |
|---|---|
| 认证 | `POST /api/auth/login`、`POST /api/auth/logout`、`GET /api/auth/me` |
| 用户（管理员） | `GET/POST /api/users`、`GET/PATCH /api/users/{id}`、`POST /api/users/{id}/reset-password` |
| LLM（管理员） | `GET/POST /api/admin/llm/configs`、`PATCH/DELETE /api/admin/llm/configs/{id}`、`POST /api/admin/llm/configs/{id}/catalog/refresh`、`POST /api/admin/llm/configs/{id}/test` |
| 事件流 | `GET /api/stream/events`、`GET /api/stream`（SSE）、`POST /api/admin/stream/test-events`（需开启测试端点） |
| 存储（管理员） | `POST /api/admin/storage/objects`、`GET /api/admin/storage/objects/{sha256}` |
| 审计（管理员） | `GET /api/admin/audit` |
| 健康/文档 | `GET /healthz`、`GET /api/health`、`GET /api/openapi.yaml` |

## 数据库与迁移

- 迁移文件在 `backend/internal/db/migrations/`，命名 `<context>_<version>_<desc>.sql`，`order.txt` 固定执行顺序。
- 迁移只做加法式 DDL；已应用迁移不修改，只追加。
- API 启动时自动执行：连接后获取 advisory lock、按版本顺序在事务中执行、写入 `schema_migrations`；重复启动幂等（验收脚本会对比两次启动的迁移指纹）。
- 开发库 `eztest`、集成库 `eztest_test` 由 `scripts/db.ps1 init`（或 `.tools/setup-toolchain.ps1`）创建。
- 查看状态：`scripts/db.ps1 status`；停止：`scripts/db.ps1 stop`。

## 测试与验收

在仓库根执行（PowerShell 5.1 兼容）：

```powershell
# 一键本地检查（Go 单元 + Go 集成 + 前端 lint/typecheck/test）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1

# 完整 P0 验收（Go 单元/集成、前端 lint/typecheck/test/build、Playwright E2E、
# 迁移重复执行、健康/审计断言）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1

# 仅 E2E（会自行启动/停止 API 与 Web，使用 eztest_e2e 库）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1

# Compose 静态校验（不需要 Docker 守护进程）
cd frontend
node scripts/validate-compose.mjs
```

分层命令：

| 层 | 命令 |
|---|---|
| Go 单元 | `cd backend; ..\.tools\go\bin\go.exe test ./...` |
| Go 集成 | `cd backend; $env:EZTEST_TEST_DATABASE_URL=...; ..\.tools\go\bin\go.exe test -tags=integration ./...` |
| 前端单元 | `cd frontend; pnpm test` |
| 前端静态/构建 | `cd frontend; pnpm lint; pnpm typecheck; pnpm build` |
| E2E | `cd frontend; pnpm e2e`（推荐用 `scripts/e2e.ps1` 管理服务与数据库） |

验收说明：

- 本机没有 Docker，`scripts/acceptance.ps1` 会把“Compose 容器运行”标为 **SKIPPED**；Compose 仅通过 `validate-compose.mjs` 做静态字段校验，不代表已运行验证。
- 真实 LLM 调用默认不执行；常规测试全部使用 `httptest` mock（`/v1/chat/completions`、`/v1/responses`、`/v1/models`，可注入 429/5xx/超时/分片）。
- 通过条件：所有非 SKIPPED 检查 FAIL 数为 0，脚本退出码为 0。

## 真实 LLM（gated）

只有同时满足以下条件才会产生真实上游调用与费用：

1. `.env` 中 `llm_base_url` / `llm_key` / `llm_model_name` 有效；
2. 显式设置 `EZTEST_RUN_REAL_LLM=1` 并运行 `scripts/acceptance-llm.ps1`。

普通 `go test`、`pnpm test`、E2E 与 `scripts/acceptance.ps1` 都不会访问真实 LLM。配置也可在管理员登录后于设置页创建/更新，Key 留空表示保留原值。

## 容器部署

见 [`deploy/README.md`](deploy/README.md)。要点：

- `api` 13333、`web` 6868、`postgres:17` 5433（宿主端口）。
- 需要仓库根 `.env`（gitignored）提供 `POSTGRES_PASSWORD`、`EZTEST_SECRET_KEYS`、管理员凭据等。
- 两个运行时镜像都以非 root 用户运行；API 镜像已内置 `docs/openapi/p0.yaml`。
- **本机未安装 Docker**：镜像未构建、Compose 未启动；唯一验证是
  `cd frontend && node scripts/validate-compose.mjs`（解析 YAML，断言服务、端口、健康检查、卷、依赖顺序）。

## 已知 P0 限制

- 单实例部署：SSE 的订阅与广播在进程内完成，不做多实例广播；重启/多副本会丢失实时连接（客户端可重连回放）。
- 登录 token 存 `sessionStorage`：属于单管理员内网工具的 XSS 边界，P0 未做 CSP/HttpOnly cookie 会话。
- 无 TLS、反向代理、限流网关或多租户/RBAC；管理员角色是唯一权限分层。
- 对象存储只有本地内容寻址实现：无公开 URL、GC、保留期、去重清理。
- LLM `base_url` 由管理员配置且被信任（P0 不做 SSRF 白名单）；真实调用受上游可用性/限流/费用影响。
- Compose/镜像仅静态交付，未在本机运行验证；Dockerfile 使用 `golang:1.24.6-alpine`、`node:22-alpine`、`alpine:3.21`，首次构建需要拉取镜像与依赖。
- 迁移与数据备份需自行负责；丢失 `EZTEST_SECRET_KEYS` 会导致已加密的 LLM Key/headers 不可恢复。
- P0 页面只覆盖登录、概览、设置（用户/LLM/SSE 测试），其余侧边栏入口为占位页。

## 许可证

Apache-2.0，见 [`LICENSE`](LICENSE)。
