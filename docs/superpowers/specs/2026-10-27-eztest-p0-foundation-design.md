# EzTest P0 工程与运行基础 — 设计规格

> **状态**：待用户评审（2026-10-27）
> **范围**：实施阶段 P0（`docs/design/implementation-phases.md` §2-P0）
> **设计权威**：`docs/design/system-blueprint.md` v2.2（D1–D26）；详细设计从属于主蓝图
> **配套**：`frontend-design.md`、`auth-session-model.md`（AS-M0）、`code-intelligence.md`（CI-M0）、`Agent.md` §6
> **性质**：P0 的实现依据（接口/DDL/契约/验收），不含任务拆解；任务拆解由 `writing-plans` 产出。

---

## 1. 目标与非目标

### 1.1 目标

在任何业务功能之前，交付“能跑、能观测、能鉴权、能实时、能调模型”的工程地基：

1. Go 后端 + PostgreSQL 17 + Next.js 前端的可构建、可测试、可启动骨架。
2. 幂等、可重入、启动即执行的数据库迁移框架。
3. 单管理员 + 普通用户的认证、授权、token 撤销与审计骨架。
4. 对象/证据存储抽象（本地文件 + sha256 内容寻址）。
5. 统一 JSON API：错误信封、request_id、分页、幂等、健康检查、OpenAPI 骨架。
6. SSE 通道 V1：事件持久化、通道内单调 seq、快照游标、Last-Event-ID 回放、心跳、重连语义、测试通道。
7. 极简 LLM 网关：DB 配置（开发模式明文）、Chat Completions + Responses 双协议、多模态图片、流式/非流式、重试。
8. 前端壳：登录/登出、侧边栏、路由、主题、状态徽章、空页面、请求封装、fetch SSE hook、最小设置页（用户/LLM/SSE 测试）。
9. 三个风险 spike：SSE 断线回放、LLM 多模态调用、浏览器执行环境可用性（只验证不集成）。

### 1.2 非目标（P0 明确不做）

- 版本、需求、用例、执行、证据、问题、报告等领域实体与页面。
- Agent 会话/工具/上下文/预算（P1）。
- 目标系统/环境/账号/Session 接入（P2/P6）。
- 代码仓库/代码理解/影响分析（P4）。
- 视觉断言（P8）。
- 多实例 SSE 广播、事件保留期 GC、消息队列。
- 多租户、复杂 RBAC、CI/工单/IM 集成。
- Docker 容器运行验证（本机无 Docker；Compose 只做静态校验）。

---

## 2. 已确认约束与决策

| 项 | 决策 |
|---|---|
| 实施范围 | 严格按 `implementation-phases.md` §2-P0；不建业务实体 |
| 后端 | Go（标准库 `net/http`，Go 1.22+ 方法路由），模块化单体 |
| 前端 | Next.js App Router + React 19 + Tailwind + TypeScript；无业务页面 |
| 数据库 | PostgreSQL 17.6 便携版，端口 `5433`；数据库 `eztest` / 测试库 `eztest_test` |
| 端口 | Web `6868`、API `2333`、PostgreSQL `5433` |
| 前后端 | CORS 直连；`EZTEST_WEB_ORIGIN=http://localhost:6868` |
| 认证 | 不透明 Bearer Token（32B 随机、SHA-256 哈希入库、不过期、可撤销） |
| 安全模式 | 开发模式：用户密码、LLM Key、custom headers 明文存储；管理员可查看；关闭登录限流；P8 回收 |
| SSE 鉴权 | fetch 流 + `Authorization` + `Last-Event-ID`；不使用 EventSource/Cookie |
| LLM | `https://api.deepseek.com`，`deepseek-flash`；Chat Completions + Responses 双协议 |
| 模型上下文 | 运行时从 `GET /v1/models` 的 `context_window` 读取，不手工配置 |
| LLM 配置 | 全量存 DB；API Key/custom headers 明文（开发模式），仅管理员可查看 |
| 视觉断言 | P8（D7） |
| 仓库结构 | ARTEX 式按能力分包 + 每上下文 DDD 四层（D26 / `Agent.md` §6） |
| Git | `feat/p0-foundation` 分支，小步提交，不 push；验收后本地合并回 `dev` |
| 风险 spike | 真实 LLM 调用 gated；浏览器可用性用本机 Chrome/Edge |

---

## 3. 架构与仓库结构

### 3.1 形态

- **模块化单体**：Go 单二进制提供 REST + SSE；Next.js 独立进程提供 UI；PostgreSQL 存业务/事件/审计/配置。
- **限界上下文**（P0）：`identity`、`eventing`、`llm`、`storage`、`audit`；技术共享 `platform`；基础设施 `config`/`db`/`server`。
- **导入规则**：以 `Agent.md` §6 为准（domain 不依赖 application/infra；application 不依赖 infra；infra 实现 domain 接口；httpapi 只调 application；跨上下文只走 application/事件）。
- **组装根**：`backend/cmd/eztest/main.go` 唯一负责依赖装配；`server` 只做路由/中间件装配。

### 3.2 目录

```text
backend/
  cmd/eztest/main.go
  internal/
    config/
    db/{db.go,migrate.go,migrations/}
    server/{router.go,middleware.go}
    platform/{clock.go,id.go,errors.go,pagination.go,validation.go,crypto/,idempotency/}
    identity/{domain,application,infrastructure,interfaces/httpapi}
    eventing/{domain,application,infrastructure,interfaces/httpapi}
    llm/{domain,application,infrastructure/{deepseek,catalog},interfaces/httpapi}
    storage/{domain,application,infrastructure/localfs,interfaces/httpapi}
    audit/{domain,application,infrastructure,interfaces/httpapi}
frontend/
  src/app/
  src/features/{auth,users,llm-settings,sse-test,shell}/
  src/components/ui/
  src/lib/{api-client.ts,sse-client.ts,auth-storage.ts,utils.ts}
  src/hooks/ src/navigation/ src/styles/
deploy/
docs/openapi/p0.yaml
scripts/
```

### 3.3 请求流

```text
HTTP -> server middleware（request_id -> recover -> logging -> CORS -> auth -> idempotency）
     -> interfaces/httpapi handler -> application use case -> domain
     -> infrastructure（postgres / llm http / localfs / crypto）
```

- 写操作在 application 层开事务；事件发布与业务写入同事务（P1 起复用）。
- SSE 不经过 idempotency 中间件；认证后直接进入 eventing 流处理器。

---

## 4. 运行、配置与本地开发

### 4.1 环境变量

| 变量 | 必填 | 默认 | 说明 |
|---|---|---|---|
| `EZTEST_ENV` | 否 | `dev` | P0 仅支持 `dev/test`；`prod` 启动失败（尚未实现生产安全模式） |
| `EZTEST_HTTP_ADDR` | 否 | `:2333` | API 监听地址 |
| `EZTEST_WEB_ORIGIN` | 否 | `http://localhost:6868` | CORS 允许的单一来源 |
| `EZTEST_DATABASE_URL` | 是 | — | `postgres://eztest:***@localhost:5433/eztest?sslmode=disable` |
| `EZTEST_TEST_DATABASE_URL` | 测试 | — | 集成测试库，禁止指向开发库 |
| `EZTEST_ADMIN_USERNAME` | 首次启动 | — | 无管理员时创建 |
| `EZTEST_ADMIN_PASSWORD` | 首次启动 | — | 明文种子；不写日志；创建后忽略 |
| `EZTEST_STORAGE_DIR` | 否 | `.data/objects` | 对象存储根 |
| `EZTEST_ENABLE_TEST_ENDPOINTS` | 否 | `dev` 下 `true` | 控制 SSE 测试发布端点 |
| `EZTEST_LOG_LEVEL` | 否 | `info` | `debug/info/warn/error` |
| `EZTEST_RUN_REAL_LLM` | 测试 | `false` | `1` 时运行真实 LLM 验收；常规测试默认跳过 |
| `llm_base_url` | 种子 | — | 首次启动无默认 LLM 配置时使用 |
| `llm_key` | 种子 | — | 首次启动时明文入库；不写日志 |
| `llm_model_name` | 种子 | — | 首次启动使用 |
| `NEXT_PUBLIC_API_BASE_URL` | 前端 | `http://localhost:2333` | 浏览器访问 API 的地址 |

- **失败快**：缺少 `EZTEST_DATABASE_URL` / 管理员种子（无管理员时）→ 启动失败并给出明确错误；`EZTEST_ENV=prod` 直接拒绝启动。
- 不在日志、错误、审计中输出 token、密码、LLM Key、Authorization、custom headers 值（排障卫生；明文存储见 6.3）。

### 4.2 本地开发

- `.tools/go`：便携 Go（Windows zip）；`.tools/pgsql`：PostgreSQL 17.6 便携版；`.tools/pgdata`：数据目录。
- `scripts/db.ps1`：`init/start/stop/status`，端口 5433；创建 `eztest` 与 `eztest_test`。
- `scripts/dev-api.ps1`：加载 `.env`，执行迁移，`go run ./cmd/eztest`。
- `scripts/dev-web.ps1`：在 `frontend/` 执行 `pnpm dev -- -p 6868`（Next.js `next dev -p 6868`）。
- `scripts/test.ps1`：`go test ./...` + 集成测试；在 `frontend/` 执行 `pnpm lint && pnpm typecheck && pnpm test`。
- `scripts/e2e.ps1`：启动 API/Web/PG，运行 Playwright。
- `.env.example` 只含占位，不含真实秘密；`.env` 已 gitignore。

### 4.3 部署形态（静态校验）

- `deploy/Dockerfile.api`：多阶段构建 Go 二进制，非 root 运行。
- `deploy/Dockerfile.web`：Next.js standalone 构建。
- `deploy/docker-compose.yml`：`api`、`web`、`postgres:17`；PG 数据卷、证据卷、healthcheck、依赖顺序。
- 本机无 Docker：只做 YAML 解析 + 端口/卷/依赖静态检查；在 P0 验收记录中标注“容器未运行验证”。

---

## 5. 数据模型与迁移

### 5.1 表与归属

| 上下文 | 表 | 迁移文件 |
|---|---|---|
| `identity` | `users`、`auth_tokens` | `identity_0001_users.sql`、`identity_0002_auth_tokens.sql` |
| `llm` | `llm_configs` | `llm_0001_configs.sql` |
| `eventing` | `event_channels`、`events` | `eventing_0001_events.sql` |
| `storage` | `storage_objects` | `storage_0001_objects.sql` |
| `audit` | `audit_logs` | `audit_0001_logs.sql` |
| `platform` | `idempotency_keys` | `platform_0001_idempotency.sql` |
| `db` | `schema_migrations` | 迁移执行器自管理 |

### 5.2 DDL 轮廓

```sql
-- 迁移执行器创建
schema_migrations (
  version BIGINT PRIMARY KEY,
  checksum TEXT NOT NULL,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)

users (
  id BIGSERIAL PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,
  password TEXT NOT NULL,
  role TEXT NOT NULL CHECK (role IN ('admin','user')),
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_login_at TIMESTAMPTZ
)
-- 部分唯一索引：CREATE UNIQUE INDEX users_one_admin ON users(role) WHERE role = 'admin';

auth_tokens (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash TEXT NOT NULL UNIQUE,
  expires_at TIMESTAMPTZ, -- null = 不过期（开发模式）
  revoked_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at TIMESTAMPTZ
)

audit_logs (
  id BIGSERIAL PRIMARY KEY,
  actor_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  action TEXT NOT NULL,
  target_type TEXT,
  target_id TEXT,
  request_id TEXT,
  ip TEXT,
  user_agent TEXT,
  metadata JSONB NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
)

llm_configs (
  id BIGSERIAL PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  protocol TEXT NOT NULL CHECK (protocol IN ('chat_completions','responses')),
  base_url TEXT NOT NULL,
  model TEXT NOT NULL,
  api_key TEXT,
  headers JSONB NOT NULL DEFAULT '{}',
  retry_count INT NOT NULL DEFAULT 2 CHECK (retry_count BETWEEN 0 AND 5),
  retry_interval_ms INT NOT NULL DEFAULT 500 CHECK (retry_interval_ms BETWEEN 100 AND 30000),
  timeout_ms INT NOT NULL DEFAULT 60000 CHECK (timeout_ms BETWEEN 1000 AND 300000),
  stream_enabled BOOLEAN NOT NULL DEFAULT true,
  multimodal_enabled BOOLEAN NOT NULL DEFAULT true,
  is_default BOOLEAN NOT NULL DEFAULT false,
  model_catalog JSONB NOT NULL DEFAULT '[]',
  catalog_fetched_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
)
-- 部分唯一索引：CREATE UNIQUE INDEX llm_configs_one_default ON llm_configs(is_default) WHERE is_default;

event_channels (
  channel TEXT PRIMARY KEY,
  last_seq BIGINT NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
)

events (
  id BIGSERIAL PRIMARY KEY,
  channel TEXT NOT NULL REFERENCES event_channels(channel) ON DELETE RESTRICT,
  seq BIGINT NOT NULL,
  type TEXT NOT NULL,
  version_id TEXT,
  batch_id TEXT,
  case_id TEXT,
  step_id TEXT,
  agent_role TEXT,
  session_id TEXT,
  ts TIMESTAMPTZ NOT NULL,
  summary TEXT NOT NULL DEFAULT '',
  detail JSONB NOT NULL DEFAULT '{}',
  refs JSONB NOT NULL DEFAULT '{"evidence":[],"case":[],"requirement":[],"code":[]}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(channel, seq)
)
-- 索引：(channel, seq)、(channel, ts)、(channel, type)

idempotency_keys (
  id BIGSERIAL PRIMARY KEY,
  key TEXT NOT NULL,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  method TEXT NOT NULL,
  path TEXT NOT NULL,
  request_hash TEXT NOT NULL,
  response_status INT NOT NULL,
  response_body JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL,
  UNIQUE(user_id, key, method, path)
)

storage_objects (
  sha256 TEXT PRIMARY KEY,
  size BIGINT NOT NULL,
  content_type TEXT NOT NULL,
  relative_path TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
)
```

### 5.3 迁移规则

- `go:embed` 加载 `backend/internal/db/migrations/*.sql`；启动时 `pg_advisory_lock` + 事务逐版本执行。
- 迁移只增不改；已应用迁移禁止修改；文件名 `<context>_<version>_<desc>.sql`。
- 重复启动/重复执行无副作用；失败回滚并终止启动。
- 种子只在首次启动执行：
  - 无管理员 → 创建管理员（角色 admin，状态 active）。
  - 无默认 LLM 配置且 `.env` 三值齐全 → 创建 `name=default` 配置，Key 明文写入（开发模式）。
  - 种子不写业务样例数据。

---

## 6. 认证、授权与安全

### 6.1 用户与密码

- 角色：`admin`、`user`。P0 只允许一个管理员：由种子创建；不提供创建/提升/降级管理员的 API；`users` 部分唯一索引保证 `role='admin'` 最多一行。
- 用户名：`^[a-zA-Z0-9_.-]{3,64}$`；密码：1–128 字符（开发模式不设复杂度/长度安全限制）。
- 密码明文存 `users.password`；管理员可在用户页查看/复制（开发模式）。
- 管理员不能禁用自己；P0 不提供删除用户的接口，`PATCH /api/users/{id}` 只允许改 `status`。
- 普通用户：只能读自己的信息；不能访问 `/api/users` 与 `/api/admin/*`。
- 关闭登录失败限流（开发模式）；失败仍返回 401。

### 6.2 Token

- 登录成功生成 32 随机字节，前缀 `ezt_`，base64url 编码；返回明文一次。
- DB 只存 `SHA-256(token)`；token 不过期（`expires_at` 为空）；`last_seen_at` 最多每 5 分钟更新一次。
- `POST /api/auth/logout` 撤销当前 token；禁用用户/重置密码撤销该用户全部 token。
- 认证头：`Authorization: Bearer <token>`；失败 401，带 `WWW-Authenticate: Bearer`。
- 前端把 token 放 `sessionStorage`（键 `eztest.token`）；关闭标签页失效；文档标注 XSS 边界。
- 不做 refresh token；token 不过期，登出/撤销后重新登录。

### 6.3 开发模式机密处理（安全债务）

- 开发模式（`EZTEST_ENV=dev/test`）：`users.password`、`llm_configs.api_key`、`llm_configs.headers` 明文存储；管理员可在设置页查看/复制。
- 不引入 AES 主密钥、`key_version` 或轮换；P0 不做秘密加密。
- Token 仍以 `SHA-256(token)` 哈希存储（不影响调试），但不过期。
- 日志/审计/错误仍不输出密码、token、LLM Key、headers 值（排障卫生，不是安全边界）。
- 仅限本地开发；不得用于共享/生产。P8 硬化必须恢复密码哈希、秘密加密、token 过期、登录限流，并轮换已暴露的凭据。

### 6.4 审计

- 写 `audit_logs` 的动作至少：登录成功/失败、登出、创建/更新/禁用用户、重置密码、LLM 配置增删改、LLM 测试成功/失败、存储对象创建、SSE 测试发布。
- `metadata` 脱敏：不写密码、token、Key、headers 值；可写用户名、配置 id/name、模型、耗时、状态码。
- 审计查询：`GET /api/admin/audit?limit=&cursor=`，管理员只读。

---

## 7. 事件与 SSE V1 契约

### 7.1 通道与序号

- 通道名：`system`（P0）；后续 `version:{id}`。
- `seq` 为**通道内**单调递增；由 `event_channels.last_seq` 原子自增分配。
- 发布流程（单事务）：
  1. `INSERT INTO event_channels(channel,last_seq,updated_at) VALUES($1,1,now()) ON CONFLICT(channel) DO UPDATE SET last_seq=event_channels.last_seq+1, updated_at=now() RETURNING last_seq;`
  2. 在事务内插入 `events(channel, seq, type, ts, ...)`；
  3. 提交后向进程内 hub 广播。
- 单实例 hub；慢消费者缓冲 256；溢出断开，由客户端 Last-Event-ID 回放。
- P0 不做多实例广播；后续用 Postgres LISTEN/NOTIFY。

### 7.2 事件信封（V1 冻结）

```json
{
  "seq": 42,
  "channel": "system",
  "type": "system.test",
  "version_id": null,
  "batch_id": null,
  "case_id": null,
  "step_id": null,
  "agent_role": null,
  "session_id": null,
  "ts": "2026-10-27T02:30:00Z",
  "summary": "admin test event",
  "detail": {"index": 1},
  "refs": {"evidence": [], "case": [], "requirement": [], "code": []}
}
```

- `type` 命名 `<aggregate>.<past_tense>`；未知类型前端可按通用事件渲染。
- `summary` ≤500 字符；`detail` JSON ≤64KB；`channel` ≤64 字符；`type` ≤100 字符。
- `ts` RFC3339 UTC；`refs` 四个数组始终存在。
- SSE 帧：`id: <seq>`、`event: <type>`、`data: <JSON>`；每条后 flush。
- 唯一例外：`stream.reset` 是合成控制帧，不进入上述信封、不持久化、不带 `id`。

### 7.3 端点

```text
GET /api/stream/events?channel=system&limit=50&before_seq=
  -> {"data":{"items":[...],"snapshot_cursor":N,"has_more":true}}

GET /api/stream?channel=system&since=N
  Headers: Authorization: Bearer <token>
  Optional: Last-Event-ID: <seq>
  Response headers: Content-Type: text/event-stream, Cache-Control: no-cache,
                    X-Accel-Buffering: no, X-Snapshot-Cursor: <latest_seq>

POST /api/admin/stream/test-events
  Body: {"count":1,"summary":"...","detail":{}}
  -> {"data":{"seqs":[N]}}
  Only when EZTEST_ENABLE_TEST_ENDPOINTS=true; admin only.
```

- `Last-Event-ID` 优先于 `since`；两者都无 → 只推实时，`X-Snapshot-Cursor` 给当前末尾。
- `GET /api/stream/events`：`items` 按 seq 升序；`before_seq` 返回 seq < before_seq 的最近 N 条；`snapshot_cursor` 为请求时刻通道最新 seq；未知 channel 404。
- 回放：handler 先订阅 hub，再查 `seq > cursor` 的历史；先发历史，再发订阅期间缓冲的实时事件；按 `seq` 去重。
- 游标早于当前通道最早事件、或通道被重建 → 发送合成控制帧 `event: stream.reset`，`data: {"channel":"system","snapshot_cursor":N,"reason":"cursor_too_old"}`；该帧**不带 `id`、不写入 `events`**，前端收到后重取首屏。
- 心跳：每 20s 发送 SSE 注释 `: ping`；客户端 45s 无任何字节视为断线并重连。
- 重连：指数退避 1s→30s + jitter；UI 显示“实时/重连中/已断开”，不得把断线显示为“无消息”。
- 认证在连接建立时校验；token 被撤销后新连接 401，既有连接不强制中断（客户端重连时重新鉴权）。
- 测试发布：`count` 取值 1–100，`channel` 固定 `system`，`type` 固定 `system.test`。

### 7.4 前端 SSE 客户端

- `lib/sse-client.ts` 使用 `fetch` + `ReadableStream`，不使用 `EventSource`。
- 解析 `id/event/data` 多行、忽略注释；按 `seq` 去重；记录 `lastSeq`。
- 断线自动重连，带 `Last-Event-ID`；`stream.reset` 时清空并通知上层重取快照。
- 暴露状态：`connecting | live | reconnecting | closed`；设置页 SSE 测试卡显示状态与最后 seq。

---

## 8. LLM 网关

### 8.1 配置模型

- 一条 `llm_configs` 记录即一个网关配置；`is_default` 部分唯一索引保证最多一个默认。
- 设置 `is_default=true` 时在同一事务先清除其他默认；禁止并行产生两个默认。
- 字段见 5.2；`protocol` 决定适配器。
- `base_url` 归一化：
  - 以 `/v1` 结尾：`{base}/chat/completions`、`{base}/responses`、`{base}/models`；
  - 否则：`{base}/v1/chat/completions`、`{base}/v1/responses`、`{base}/v1/models`。
- 自定义 headers：JSON 对象（明文，开发模式）；应用到所有上游请求；禁止覆盖 `Authorization`、`Content-Type`（保存时校验拒绝）。
- 校验：base_url 必须 http/https；protocol 枚举；retry 0–5；interval 100–30000ms；timeout 1000–300000ms。

### 8.2 种子

- 首次启动无默认配置且 `llm_base_url`、`llm_key`、`llm_model_name` 齐全：
  - `name=default`、`protocol=chat_completions`、`base_url=llm_base_url`、`model=llm_model_name`；
  - Key 明文写入；retry=2、interval=500ms、timeout=60000ms、stream=true、multimodal=true；
  - 写入后不再读取 `.env` 的 LLM 值。
- 缺任一值 → 不种子；设置页提示“未配置 LLM”，应用仍可启动。

### 8.3 模型目录

- `GET {base}/v1/models`（带配置 headers/Key）。
- 解析 `data[]`，保留：`id`、`context_window`、`max_output_tokens`、`input_modalities`。
- 写入 `llm_configs.model_catalog` 与 `catalog_fetched_at`；刷新失败保留旧缓存并返回错误。
- 前端设置页展示当前 `model` 的上下文大小；缺失字段显示“未知”，不猜测。

### 8.4 双协议适配

**Chat Completions**（`/v1/chat/completions`）：

```json
{
  "model": "deepseek-flash",
  "messages": [
    {"role": "user", "content": [
      {"type": "text", "text": "describe"},
      {"type": "image_url", "image_url": {"url": "data:image/png;base64,..."}}
    ]}
  ],
  "stream": false
}
```

**Responses**（`/v1/responses`）：

```json
{
  "model": "deepseek-flash",
  "input": [
    {"role": "user", "content": [
      {"type": "input_text", "text": "describe"},
      {"type": "input_image", "image_url": "data:image/png;base64,..."}
    ]}
  ],
  "stream": false
}
```

- 非流式：解析文本（Chat：`choices[0].message.content`；Responses：`output[]` 中的文本项），并返回 usage。
- 流式：Chat 解析 `data:` 分片；Responses 解析其事件流；对外统一归一化为 `llm.delta` / `llm.done` / `llm.error` SSE。
- 图片：P0 测试端点使用内置 1×1 PNG；多模态开关关闭时拒绝带图请求。

### 8.5 重试、超时与错误

- 重试条件：429、5xx、网络超时；非流式整请求重试；流式在首字节前重试，首字节后只报错。
- 重试次数/间隔来自配置；固定间隔 + 0–100ms jitter；总耗时受 `timeout_ms` 约束。
- 上游错误映射：
  - 缺少默认配置/Key/模型 → `llm_not_configured`（不可重试）
  - 401/403 → `llm_auth_failed`（不可重试）
  - 429 → `llm_rate_limited`（可重试）
  - 400/422 → `llm_bad_request`
  - 5xx → `llm_upstream_error`（可重试）
  - 超时 → `llm_timeout`（可重试）
  - 响应解析失败 → `llm_invalid_response`
- 错误统一走 API 错误信封；含 `request_id`、`retryable`、`details.upstream_status`。

### 8.6 管理端点

| 端点 | 行为 |
|---|---|
| `GET /api/admin/llm/configs` | 列表；返回 `api_key`/`headers` 明文（仅管理员，开发模式）；前端默认掩码可显示 |
| `POST /api/admin/llm/configs` | 创建；Key/headers 可选；写审计 |
| `PATCH /api/admin/llm/configs/{id}` | 更新；`api_key`/`headers` 缺省=保留，传入=替换；写审计 |
| `DELETE /api/admin/llm/configs/{id}` | 删除；删除默认后 `llm_configured=false`，仅重启时按 `.env` 重新种子 |
| `POST /api/admin/llm/configs/{id}/catalog/refresh` | 刷新模型目录 |
| `POST /api/admin/llm/configs/{id}/test` | Body `{stream?:bool,prompt?:string,image?:bool}`；非流式返回文本/usage/延迟；流式返回归一化 SSE |

- `test` 的 `image=true` 使用内置 PNG；`prompt` 默认“请用一句话描述这张图片”。
- `stream=true` 但配置 `stream_enabled=false` → 400 `invalid_argument`；`image=true` 但 `multimodal_enabled=false` → 400 `invalid_argument`。
- 测试成功/失败均写审计（不写 Key/图片内容）。
- 只有默认配置参与后续 P1 Agent 调用；P0 不提供公开 chat 端点。

---

## 9. API 骨架

### 9.1 统一约定

- 前缀 `/api`；JSON；UTF-8；时间 RFC3339 UTC。
- 成功：`{"data": ...}`；通用列表：`{"data":[...],"next_cursor":"...","has_more":true}`。
- SSE 快照端点例外：`{"data":{"items":[...],"snapshot_cursor":N,"has_more":true}}`（事件流专用结构）。
- 分页：`limit` 1–100（默认 20），`cursor` 为不透明 id 游标，按 id 倒序。
- 错误信封：

```json
{
  "error": {
    "code": "invalid_argument",
    "message": "username is required",
    "request_id": "req_...",
    "retryable": false,
    "details": {}
  }
}
```

- 错误码：`invalid_argument`、`unauthenticated`、`forbidden`、`not_found`、`conflict`、`idempotency_conflict`、`rate_limited`、`internal`、`unavailable`、`storage_corrupted`，以及 8.5 的 `llm_*`。
- `X-Request-ID`：客户端可传 `^[A-Za-z0-9_-]{8,128}$`，否则 UUID；响应回显；日志与错误信封一致。
- 幂等：写接口接受 `Idempotency-Key`（`^[A-Za-z0-9_-]{8,128}$`）；24h TTL；`request_hash = SHA-256(method + "\n" + path + "\n" + body)`；过期记录在查询/插入时滚动清理；同 key 同 body 重放已存响应并带 `Idempotent-Replayed: true`；同 key 不同 body 409。仅缓存 <1MB 的 JSON 响应；SSE 与 `stream=true` 的 LLM 测试不做幂等缓存。
- CORS：单一 `EZTEST_WEB_ORIGIN`；允许 `Authorization, Content-Type, X-Request-ID, Idempotency-Key, Last-Event-ID`；暴露 `X-Request-ID, X-Snapshot-Cursor, Idempotent-Replayed`；`credentials=false`。
- 健康：`GET /healthz` 返回 200；`GET /api/health` 返回 `{status,version,commit,db,llm_configured}`，DB 失败 503；`llm_configured=true` 仅当存在默认配置且 Key 可解密。
- OpenAPI：`docs/openapi/p0.yaml` 手工维护；`GET /api/openapi.yaml` 公开只读。

### 9.2 端点清单

```text
POST   /api/auth/login
POST   /api/auth/logout
GET    /api/auth/me

GET    /api/users
POST   /api/users                       # 只创建普通用户
GET    /api/users/{id}
PATCH  /api/users/{id}                  # body {status}
POST   /api/users/{id}/reset-password   # body {new_password}；撤销该用户全部 token

GET    /api/admin/llm/configs
POST   /api/admin/llm/configs
PATCH  /api/admin/llm/configs/{id}
DELETE /api/admin/llm/configs/{id}
POST   /api/admin/llm/configs/{id}/catalog/refresh
POST   /api/admin/llm/configs/{id}/test

GET    /api/stream/events
GET    /api/stream
POST   /api/admin/stream/test-events

POST   /api/admin/storage/objects
GET    /api/admin/storage/objects/{sha256}

GET    /api/admin/audit

GET    /healthz
GET    /api/health
GET    /api/openapi.yaml
```

### 9.3 关键请求/响应

**登录**：

```json
POST /api/auth/login
{"username":"admin","password":"..."}
-> 200 {"data":{"token":"ezt_...","expires_at":null,"user":{"id":1,"username":"admin","role":"admin","status":"active"}}}
```

**创建用户**：

```json
POST /api/users
{"username":"qa01","password":"..."}
-> 201 {"data":{"id":2,"username":"qa01","role":"user","status":"active"}}
```

> P0 的 `POST /api/users` 只创建 `role=user`；管理员由种子创建，且全库仅一个。开发模式下，管理员 `GET /api/users` 与 `GET /api/users/{id}` 返回 `password` 明文；普通用户访问 403。

**SSE 快照**：

```json
GET /api/stream/events?channel=system&limit=50
-> 200 {"data":{"items":[{"seq":1,"type":"system.test",...}],"snapshot_cursor":1,"has_more":false}}
```

---

## 10. 前端壳

### 10.1 技术栈

- Next.js 16 App Router、React 19、TypeScript、Tailwind CSS；`next-themes` 主题；
- Radix 原语 + 自建 `components/ui`（Button/Input/Table/Dialog/Tabs/Badge/EmptyState/Skeleton/ErrorState）；
- lucide-react 图标；Biome（lint/format）；Vitest + Testing Library；Playwright（Chrome/Edge channel）；
- 版本在 `package.json` 锁定；不引入业务状态库，认证/主题用 React Context。

### 10.2 路由与布局

| 路由 | 内容 |
|---|---|
| `/login` | 登录表单；错误提示；已登录跳 `/` |
| `/` | 概览：API/DB/LLM/SSE 状态卡；版本功能 P3 空状态 |
| `/settings` | 管理员：用户、LLM、SSE 测试三 Tab；普通用户：账户信息 + 主题 |
| `/versions`、`/issues`、`/reports`、`/code-repos`、`/resources`、`/agents` | P0 占位页（空状态 + 阶段说明） |

- 顶栏：Logo、环境徽章、用户菜单（角色/登出）、主题切换。
- 侧边栏：蓝图 9 项；P0 仅概览/设置可用，其余标“P1/P2/P3 规划中”。
- 受保护布局：无 token → `/login`；401 → 清理 token 并跳转。

### 10.3 前端基础设施

- `lib/api-client.ts`：`NEXT_PUBLIC_API_BASE_URL`、Bearer、错误信封、401 处理、`X-Request-ID`、`Idempotency-Key`（写请求可选）。
- `lib/sse-client.ts`：fetch 流解析、`Last-Event-ID`、20s 心跳超时、退避重连、`stream.reset`、状态回调。
- `lib/auth-storage.ts`：`sessionStorage` 读写 token；不在 URL/日志中出现。
- `features/auth`：登录/登出/当前用户。
- `features/users`：用户列表/创建/禁用/重置密码（管理员）；查看/复制明文密码（开发模式）。
- `features/llm-settings`：配置表单（Key 明文可查看/替换）、模型目录、上下文大小、测试（含图片/流式）。
- `features/sse-test`：连接状态、最后 seq、发送测试事件、事件列表。
- `features/shell`：布局、侧边栏、顶栏、状态徽章、主题。
- 空状态/错误重试/骨架屏统一组件；键盘可达、焦点管理、对比度、状态不只靠颜色。

### 10.4 设置页验收动线

1. 管理员登录 → 创建普通用户（可查看/复制明文密码）→ 普通用户登录，确认 403/只读。
2. 管理员配置 LLM（Key 明文可查看）→ 刷新模型目录 → 显示 `context_window`。
3. 发送多模态测试（含图片）→ 非流式成功；切换流式 → 看到增量输出。
4. SSE 测试卡连接 → 发送 3 条测试事件 → 普通用户侧实时可见；断网/恢复 → 状态“重连中”→“实时”，seq 连续。

---

## 11. 对象存储抽象

- 接口（`storage/domain`）：

```go
type Object struct {
    SHA256      string
    Size        int64
    ContentType string
    RelativePath string
}

type Store interface {
    Put(ctx context.Context, r io.Reader, contentType string) (Object, error)
    Open(ctx context.Context, sha256 string) (io.ReadCloser, Object, error)
    Stat(ctx context.Context, sha256 string) (Object, error)
}
```

- 本地实现（`storage/infrastructure/localfs`）：
  - 根目录 `EZTEST_STORAGE_DIR`，路径 `ab/cd/<sha256>`（sha256 前 4 位分两级）。
  - 写入临时文件 → 计算 SHA-256 → `fsync` → 原子 rename；已存在同哈希直接复用。
  - 读取时重新计算哈希校验；不一致返回 `storage_corrupted`。
  - 不覆盖已存在对象；`storage_objects` 只增。
- 管理端点：`POST /api/admin/storage/objects`（原始 body + Content-Type，≤10MB）→ `{sha256,size,content_type}`；`GET /api/admin/storage/objects/{sha256}` 流式返回。
- P0 不实现公开 URL、GC、保留期；证据域在 P6 接入。

---

## 12. 测试与验收

### 12.1 测试分层

| 层 | 命令/工具 | 覆盖 |
|---|---|---|
| Go 单元 | `go test ./...` | 领域校验、密码明文可见、token 不过期/撤销、错误映射、重试策略、SSE 编解码 |
| Go 集成 | `go test -tags=integration ./...`（`EZTEST_TEST_DATABASE_URL`） | 迁移重复执行、仓储、登录/登出、权限、幂等重放、事件 seq/回放、LLM mock |
| 前端单元 | `pnpm test`（Vitest） | api-client 错误/401；sse-client 解析/去重/重连/reset |
| E2E | `pnpm e2e`（Playwright，Chrome/Edge） | 登录、创建用户、只读权限、LLM 配置/目录/测试、SSE 连接/断线恢复 |
| Spike | 脚本/gated | 真实 LLM 多模态；Playwright 打开页面/截图 |

### 12.2 DoD 验收映射

| DoD | 验收 |
|---|---|
| 管理员登录并创建普通用户；权限差异 | E2E + 集成测试（403/只读）；管理员可查看明文密码 |
| 开发模式明文与不过期 | 管理员 GET 用户/LLM 返回明文；普通用户 403；日志无秘密；token 不过期 |
| 健康/日志/审计可用；迁移重复执行无副作用 | `/api/health`、审计查询、migrate 跑两次断言 |
| 前端收到测试 SSE；断线重连不丢事件 | 集成测试 Last-Event-ID 回放；E2E 断网/恢复后 seq 连续 |
| LLM 多模态 + 重试 | 真实 Chat Completions 含图（gated）；Responses 文本真实 + 多模态 mock；mock 429/5xx 重试验证 |
| 目录/构建/运行文档齐备 | `go build/test`、`pnpm lint/typecheck/build`、`README`、`.env.example`、OpenAPI |

### 12.3 测试基础设施

- 集成测试使用独立库 `eztest_test`；每个用例事务回滚或 truncate；不碰开发库。
- LLM mock：`httptest` 模拟 `/v1/chat/completions`、`/v1/responses`、`/v1/models`；可注入 429/500/超时/流式分片。
- 真实 LLM：`EZTEST_RUN_REAL_LLM=1` 才运行；图片用 1×1 PNG fixture；不在常规测试中产生费用。
- SSE 测试端点仅在 `EZTEST_ENABLE_TEST_ENDPOINTS=true` 注册。
- Playwright 不下载 Chromium；使用本机 Chrome/Edge channel；E2E 脚本负责启动/停止服务。

### 12.4 验收前必须补齐

- `.env` 增加 `EZTEST_ADMIN_USERNAME` / `EZTEST_ADMIN_PASSWORD`、`EZTEST_DATABASE_URL`；均不提交；确认仅本地开发使用。
- 初始化便携 PostgreSQL，创建 `eztest` / `eztest_test`。
- 确认 `.env` 的 `llm_base_url` / `llm_key` / `llm_model_name` 有效（真实调用验收前）。

---

## 13. 风险与降级

| 风险 | 处理 |
|---|---|
| 无 Docker，Compose 未运行验证 | 只做 YAML/字段/端口/卷静态校验；验收记录标注“未运行容器”，不声称已验证 |
| 真实 LLM 不可用/限流/费用 | 设置页明确错误；常规测试用 mock；真实调用 gated |
| SSE 重连窗口/慢消费者 | 先订阅后回放、seq 去重、缓冲溢出断开回放；集成 + E2E 覆盖 |
| Next/React 版本兼容 | package.json 锁版本；先壳后组件；`pnpm build` 作为门禁 |
| Windows 便携 PG 生命周期 | 脚本 initdb/start/stop/健康检查；失败明确提示 |
| token 存 sessionStorage 的 XSS 边界 | 仅本地 dev；token 不过期意味着一旦被窃取长期有效；输入转义、CSP 预留；P8 硬化恢复过期 |
| 明文密码/LLM Key 泄露 | 仅本地 `dev/test`；DB/备份不共享；P8 硬化前不接真实凭据；日志仍脱敏 |
| 范围膨胀 | 严格按本规格与 §2.9 前端映射；不建业务表/页面 |

---

## 14. 交付物

- `backend/` Go 服务、迁移、测试、OpenAPI。
- `frontend/` Next.js 壳、设置页、API/SSE 客户端、测试。
- `deploy/` Dockerfile ×2、`docker-compose.yml`。
- `scripts/` 本地开发/测试/E2E 脚本。
- `.env.example`、`README.md`（构建/运行/验收）、`docs/openapi/p0.yaml`。
- 测试证据（命令输出、E2E 截图/录屏可选）。

---

## 15. 已知假设

1. 本机 Windows 可下载 Go/PostgreSQL 便携包（已实测可达），无需管理员权限。
2. 本机 Chrome/Edge 可用于 Playwright。
3. DeepSeek 网关在 P0 期间保持 `chat_completions` / `responses` / `models` 端点可用；模型名 `deepseek-flash` 支持图片输入。
4. 单实例部署；SSE 不做多实例广播。
5. 管理员信任其配置的 `base_url`（P0 不针对 LLM 网关做 SSRF 白名单）。
6. P0 仅用于本地开发/测试；明文密码与 LLM Key 是已知安全债务，P8 硬化回收。
7. 真实 LLM Key 只存在于本地 `.env` 与开发 DB，不分享、不用于生产。

---

## 16. 明确不做（再次确认）

- 任何版本/需求/用例/执行/证据/问题/报告实体与页面。
- Agent 运行时、工具、会话、预算。
- 目标系统/环境/账号/Session、代码仓库、代码理解。
- 多实例、消息队列、Redis/NATS、多租户、复杂 RBAC。
- Docker 容器运行验证。
- 生产安全模式（密码哈希、秘密加密、token 过期、登录限流；P8）。
- 视觉断言（P8）。
