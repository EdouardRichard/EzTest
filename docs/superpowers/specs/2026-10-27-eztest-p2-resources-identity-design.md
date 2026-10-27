# EzTest P2 公共资源与身份 — 设计规格

> **状态**：待用户评审（2026-10-27）
> **范围**：实施阶段 P2（`docs/design/implementation-phases.md` §2-P2、§2.9 #16）
> **设计权威**：`docs/design/system-blueprint.md` v2.2（D1–D27）；本规格新增 D28（见 §12）
> **配套**：`auth-session-model.md` v0.1（按 D28 裁剪）、`code-intelligence.md` §3.2（代码仓配置）、`frontend-design.md` PG7/PG8、P0/P1 规格
> **性质**：P2 的实现依据（接口/DDL/契约/验收），不含任务拆解；任务拆解由 `writing-plans` 产出。

---

## 1. 目标与非目标

### 1.1 目标

交付「公共资源与身份」域，为 P3（需求）、P4（代码理解）、P6（执行/Session 注入）提供前置，并具备可演示的管理 UI：

1. 系统 / 环境 / 应用模型：系统与环境 CRUD；应用模型支持页面/路由/接口/组件节点的人工 CRUD 与 JSON 批量导入/展示。
2. 账号与访问：公司级账号、账号×系统 Access（登录方式、明文凭据、登录 URL、并发上限、TTL）。
3. Session：人工导入 storageState / token、结构校验、只读 HTTP login_check、最小权限快照、人工确认、失效、撤销、人工接管（占位流程）。
4. 本地 Git 代码仓配置：路径、固定分支、include/exclude、只读 git 校验、当前 commit、索引状态占位。
5. 管理 UI：`/resources` 与 `/code-repos`；管理员可写，普通用户只读。
6. 本地确定性 fixture 目标（Web 登录页/cookie 会话 + token API），支撑 E2E 与人工验收。

### 1.2 非目标（P2 明确不做）

- Session 自动登录全流程、AuthFlow 执行、TOTP/验证码自动处理（P6）。
- 完整 ProbeRecipe、能力探测、漂移检测、生产审批拦截（P6/P7/P8）。
- Session 租约 HTTP API 与后台调度/回收器（仅表 + 应用层 acquire/release，P6 接线）。
- Session 事件流、`session.*`/`resource.*` SSE 事件（P2 页面重取）。
- 凭据/Session 加密、脱敏、审计日志（D28：P2 明文存储，不写 `audit_logs`）。
- `secrets`/`session_blobs`/`session_events`/`auth_flows`/`probe_recipes` 表（P6 需要时追加）。
- 代码索引、diff、影响分析、文件树/符号（P4）。
- 应用自动探索、脑图、用例设计（P5）。
- 浏览器/HTTP 业务执行、Session 注入执行（P6）。
- 多公司/多租户、公司 CRUD、多用户协作、RBAC（P8 可选）。
- 代码仓 monorepo 子项目配置、glob 实际匹配（P4）。
- Session blob GC、保留期策略（P8）。

---

## 2. 已确认约束与决策

| 项 | 决策 | 来源 |
|---|---|---|
| 交付切分 | 单个 spec + 单个 plan 全量 P2；实施按 Wave/并发标记 | 用户 2026-10-27 |
| 限界上下文 | 单个 `resource` 上下文，四层齐全，无跨上下文外键 | 用户确认 |
| 单公司 | 种子一行 `companies(key='default')`，不做公司 CRUD；资源均带 `company_id`（System/Account） | 蓝图 §5.1「单公司/单租户，可简化」 |
| 凭据/Session 存储 | **明文**：`account_system_accesses.credential TEXT`、`sessions.storage_state JSONB`；不使用 P0 Keyring | 用户决策，D28 |
| 加密/脱敏/审计 | P2 不做；不写 `audit_logs`，不建事件表 | 用户决策，D28 |
| Session 状态 | 完整枚举 `pending/acquiring/needs_manual/valid/refreshing/expired/invalid/revoked/failed`；`expiring` 为派生值 | auth-session §4.1；D28 修蓝图 §5.5 |
| Session 校验 | JSON 结构校验 + 只读 HTTP login_check + 管理员人工确认；人工确认由 Agent 在验收时执行并留证 | 用户确认 |
| 权限快照 | 仅 login_check 最小快照（`session_snapshots`），不做 ProbeRecipe/漂移 | 用户确认 |
| 导入类型 | `storage_state`（Playwright JSON）与 `token`（headers bundle） | 本规格 |
| Git 校验 | 路径存在 + git 仓库/固定分支/commit 可达；不限根目录；只用只读 git 命令 | 用户确认 |
| 应用模型 | 统一节点表（page/route/api/component）+ 人工 CRUD + JSON 导入 | 用户确认 |
| 验收目标 | 本地确定性 fixture：登录页 + cookie 会话 + 受保护页 + token API | 用户确认 |
| API 前缀 | `/api/resources/*`（蓝图 §11.1）；动作子路径 | 蓝图（详细设计 `/api/systems` 等旧口径作废） |
| 权限 | GET=任意登录用户；POST/PATCH/DELETE/动作=admin | implementation-phases §2-P2 |
| System.kind | 仅 `web|api`（D13；详细设计的 `app` 不作数） | 蓝图 §3.2 D13 |
| 实时性 | 不新增 SSE；写后重取 | 本规格 |
| 加密字段 | 无（D28） | 用户决策 |
| 分支/提交 | `feat/p2-resources-identity`，从 `dev` 切出；小步提交，不 push；验收后 ff 合回 `dev` | 复用 P0/P1 约定 |
| Plan 并发 | 每任务标注 Wave/并发/依赖/写作用域；同 Wave 可并行，独立子代理执行 | 用户要求 + P1 模板 |

---

## 3. 架构与仓库结构

### 3.1 形态

- 新增 `backend/internal/resource` 限界上下文（DDD 四层）。
- 跨进程边界只有：PostgreSQL、本地文件系统（只读 git 与 storageState JSON 不落盘）、HTTP login_check、本地 fixture。
- 装配：`cmd/eztest/main.go` 构造 repo/service/handler → 填入 `server.Dependencies`；`server/server.go` 路由挂载，依赖为 nil 时整块不挂载。
- 中间件复用：请求 ID、日志、CORS、Bearer 认证、幂等、recover。读路由仅认证；写路由 `RequireAdmin`。

### 3.2 目录

```text
backend/internal/resource/
├─ domain/
│  ├─ company.go system.go environment.go appmodel.go
│  ├─ account.go access.go session.go snapshot.go lease.go takeover.go
│  ├─ coderepo.go errors.go repository.go
├─ application/
│  ├─ catalog_service.go access_service.go session_service.go coderepo_service.go
│  ├─ ports.go errors.go
├─ infrastructure/
│  ├─ postgres/            # 每个聚合一个 repository + helpers
│  ├─ httpcheck/           # login_check 客户端
│  └─ gitexec/             # 只读 git 校验器
└─ interfaces/httpapi/
   ├─ routes.go
   ├─ handlers_catalog.go handlers_access.go handlers_session.go handlers_coderepo.go
   ├─ dto.go errors.go

frontend/src/features/resources/   # 页面、面板、表单、api、测试
frontend/src/features/code-repos/
frontend/src/app/(app)/resources/page.tsx
frontend/src/app/(app)/code-repos/page.tsx
scripts/fixture-target.cjs
backend/internal/db/migrations/resource_0001_catalog.sql
backend/internal/db/migrations/resource_0002_accounts_sessions.sql
backend/internal/db/migrations/resource_0003_code_repositories.sql
```

### 3.3 请求流

```text
HTTP handler (auth / admin guard / DTO)
  → application service (校验、状态机 CAS、事务编排)
    → domain entity / domain errors
    → repository port → infrastructure/postgres
    → LoginChecker port → infrastructure/httpcheck
    → GitValidator port → infrastructure/gitexec
```

### 3.4 领域服务

| 服务 | 职责 |
|---|---|
| `CatalogService` | 系统/环境 CRUD、应用模型 CRUD 与 JSON 导入 |
| `AccessService` | 账号 CRUD、Access CRUD、凭据覆盖语义、级联保护 |
| `SessionService` | 导入、校验、人工确认、失效、撤销、快照、租约、接管 |
| `CodeRepoService` | 代码仓 CRUD、只读 git 校验、commit 记录 |

---

## 4. 运行、配置与本地开发

### 4.1 环境变量

P2 **不新增环境变量**。沿用 P0/P1：

| 变量 | 用途 |
|---|---|
| `EZTEST_DATABASE_URL` | 开发库 |
| `EZTEST_TEST_DATABASE_URL` | 集成测试库（必须以 `_test`/`_e2e` 结尾） |
| `EZTEST_SECRET_KEYS` / `EZTEST_SECRET_KEY_ACTIVE` | P0 Keyring（P2 不使用，但启动仍校验） |
| `EZTEST_WEB_ORIGIN` | CORS |
| `EZTEST_ADMIN_USERNAME` / `EZTEST_ADMIN_PASSWORD` | 种子管理员 |

fixture 端口固定 `13335`，仅测试脚本使用，不进入生产配置。

### 4.2 本地运行

```powershell
powershell -File scripts/db.ps1 init
powershell -File scripts/db.ps1 start
powershell -File scripts/dev-api.ps1      # :13333，自动迁移+种子
powershell -File scripts/dev-web.ps1      # :6868
```

### 4.3 测试与验收命令

```powershell
# 快速门禁（Go 单元 + 集成 + 前端 lint/typecheck/test）
powershell -File scripts/test.ps1
# 后端细粒度
cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/resource/...
powershell -File scripts/test-integration.ps1 ./internal/resource/...
# 前端
cd frontend; pnpm lint; pnpm typecheck; pnpm test
# E2E（含 fixture target）
powershell -File scripts/e2e.ps1
# 完整验收
powershell -File scripts/acceptance.ps1
```

### 4.4 迁移与种子

- 三个迁移按 `order.txt` 顺序执行；启动时 advisory lock；重复执行无副作用。
- 种子仅：`companies(key='default', name='Default')`。管理员种子沿用 P0 `EnsureAdmin`。
- 不写业务样例数据。

---

## 5. 数据模型与迁移

### 5.1 DDL（`resource_0001_catalog.sql`）

```sql
CREATE TABLE IF NOT EXISTS companies (
  id BIGSERIAL PRIMARY KEY,
  key TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO companies (key, name) VALUES ('default', 'Default')
ON CONFLICT (key) DO NOTHING;

CREATE TABLE IF NOT EXISTS systems (
  id BIGSERIAL PRIMARY KEY,
  company_id BIGINT NOT NULL REFERENCES companies(id) ON DELETE RESTRICT,
  key TEXT NOT NULL,
  name TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('web','api')),
  base_url TEXT NOT NULL,
  default_session_ttl_seconds INTEGER NOT NULL DEFAULT 3600
    CHECK (default_session_ttl_seconds BETWEEN 60 AND 604800),
  notes TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (company_id, key)
);

CREATE TABLE IF NOT EXISTS environments (
  id BIGSERIAL PRIMARY KEY,
  system_id BIGINT NOT NULL REFERENCES systems(id) ON DELETE CASCADE,
  key TEXT NOT NULL,
  name TEXT NOT NULL,
  base_url TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('dev','staging','prod','other')),
  is_prod BOOLEAN NOT NULL DEFAULT false,
  allowed_hosts TEXT[] NOT NULL DEFAULT '{}',
  login_check JSONB,
  allow_auto_login BOOLEAN NOT NULL DEFAULT false,
  require_approval BOOLEAN NOT NULL DEFAULT false,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (system_id, key)
);

CREATE TABLE IF NOT EXISTS app_model_nodes (
  id BIGSERIAL PRIMARY KEY,
  system_id BIGINT NOT NULL REFERENCES systems(id) ON DELETE CASCADE,
  parent_id BIGINT REFERENCES app_model_nodes(id) ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK (kind IN ('page','route','api','component')),
  key TEXT NOT NULL,
  name TEXT NOT NULL,
  path TEXT NOT NULL DEFAULT '',
  method TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual','import')),
  meta JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (system_id, key),
  CHECK (kind <> 'api' OR method <> '')
);

CREATE INDEX IF NOT EXISTS app_model_nodes_system_kind_idx
  ON app_model_nodes(system_id, kind);
```

### 5.2 DDL（`resource_0002_accounts_sessions.sql`）

```sql
CREATE TABLE IF NOT EXISTS accounts (
  id BIGSERIAL PRIMARY KEY,
  company_id BIGINT NOT NULL REFERENCES companies(id) ON DELETE RESTRICT,
  key TEXT NOT NULL,
  label TEXT NOT NULL,
  username TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL CHECK (kind IN ('human','service')),
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
  notes TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (company_id, key)
);

CREATE TABLE IF NOT EXISTS account_system_accesses (
  id BIGSERIAL PRIMARY KEY,
  account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  system_id BIGINT NOT NULL REFERENCES systems(id) ON DELETE CASCADE,
  auth_type TEXT NOT NULL
    CHECK (auth_type IN ('password','sso','token','apikey','basic','manual')),
  credential TEXT NOT NULL DEFAULT '',          -- 明文（D28）
  username_override TEXT NOT NULL DEFAULT '',
  login_url TEXT NOT NULL DEFAULT '',
  enabled BOOLEAN NOT NULL DEFAULT true,
  concurrency_limit INTEGER NOT NULL DEFAULT 1
    CHECK (concurrency_limit BETWEEN 1 AND 100),
  session_ttl_seconds INTEGER
    CHECK (session_ttl_seconds IS NULL OR session_ttl_seconds BETWEEN 60 AND 604800),
  permission_note TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (account_id, system_id)
);

CREATE INDEX IF NOT EXISTS accesses_system_idx
  ON account_system_accesses(system_id);

CREATE TABLE IF NOT EXISTS sessions (
  id BIGSERIAL PRIMARY KEY,
  account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  system_id BIGINT NOT NULL REFERENCES systems(id) ON DELETE CASCADE,
  environment_id BIGINT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  access_id BIGINT NOT NULL REFERENCES account_system_accesses(id) ON DELETE CASCADE,
  state TEXT NOT NULL
    CHECK (state IN ('pending','acquiring','needs_manual','valid','refreshing',
                     'expired','invalid','revoked','failed')),
  acquisition TEXT NOT NULL DEFAULT 'imported'
    CHECK (acquisition IN ('auto','manual','imported')),
  storage_state JSONB,                          -- 明文（D28）
  storage_state_hash TEXT NOT NULL DEFAULT '',
  storage_state_kind TEXT NOT NULL DEFAULT ''
    CHECK (storage_state_kind IN ('','storage_state','token')),
  cookie_count INTEGER NOT NULL DEFAULT 0,
  origin_count INTEGER NOT NULL DEFAULT 0,
  expires_at TIMESTAMPTZ,
  last_validated_at TIMESTAMPTZ,
  last_used_at TIMESTAMPTZ,
  failure_reason TEXT NOT NULL DEFAULT '',
  invalidated_reason TEXT NOT NULL DEFAULT '',
  created_by BIGINT,                            -- 逻辑引用 users.id，不建跨上下文外键
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS sessions_lookup_idx
  ON sessions(account_id, system_id, environment_id, state);
CREATE INDEX IF NOT EXISTS sessions_access_idx ON sessions(access_id);

CREATE TABLE IF NOT EXISTS session_snapshots (
  id BIGSERIAL PRIMARY KEY,
  session_id BIGINT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  kind TEXT NOT NULL DEFAULT 'login' CHECK (kind IN ('login')),
  items JSONB NOT NULL DEFAULT '{}'::jsonb,
  summary JSONB NOT NULL DEFAULT '{}'::jsonb,
  permission_hash TEXT NOT NULL DEFAULT '',
  degraded BOOLEAN NOT NULL DEFAULT false,
  captured_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS session_snapshots_session_idx
  ON session_snapshots(session_id, captured_at DESC);

CREATE TABLE IF NOT EXISTS session_leases (
  id BIGSERIAL PRIMARY KEY,
  session_id BIGINT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  holder TEXT NOT NULL DEFAULT '',
  acquired_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL,
  released_at TIMESTAMPTZ,
  released_reason TEXT NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_session_active_lease
  ON session_leases(session_id) WHERE released_at IS NULL;

CREATE TABLE IF NOT EXISTS manual_takeovers (
  id BIGSERIAL PRIMARY KEY,
  session_id BIGINT REFERENCES sessions(id) ON DELETE CASCADE,
  account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  system_id BIGINT NOT NULL REFERENCES systems(id) ON DELETE CASCADE,
  environment_id BIGINT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  reason TEXT NOT NULL
    CHECK (reason IN ('mfa','captcha','approval','login_unknown','other')),
  status TEXT NOT NULL DEFAULT 'waiting'
    CHECK (status IN ('waiting','completed','cancelled','expired')),
  instructions JSONB NOT NULL DEFAULT '{}'::jsonb,
  requested_by BIGINT,                          -- 逻辑引用 users.id
  requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ,
  completed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS manual_takeovers_status_idx
  ON manual_takeovers(status);
```

### 5.3 DDL（`resource_0003_code_repositories.sql`）

```sql
CREATE TABLE IF NOT EXISTS code_repositories (
  id BIGSERIAL PRIMARY KEY,
  key TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  local_path TEXT NOT NULL,
  fixed_branch TEXT NOT NULL DEFAULT 'main',
  include_globs TEXT[] NOT NULL DEFAULT '{}',
  exclude_globs TEXT[] NOT NULL DEFAULT
    ARRAY['.git/**','node_modules/**','dist/**','build/**','vendor/**'],
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
  last_commit TEXT NOT NULL DEFAULT '',
  last_validated_at TIMESTAMPTZ,
  last_validation_error TEXT NOT NULL DEFAULT '',
  last_indexed_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

### 5.4 迁移规则

- 三个文件追加到 `backend/internal/db/migrations/order.txt` 末尾，顺序 1/2/3。
- 只做加法式 DDL；已应用文件禁改；checksum 校验沿用 P0。
- 一个迁移文件只属于 `resource` 上下文。
- 所有时间戳为 `TIMESTAMPTZ`；API 输出 RFC3339 UTC。
- `users` 与 `accounts` 是不同概念：前者平台登录用户，后者被测系统身份；跨上下文仅逻辑引用，不建外键。

---

## 6. 核心规则

### 6.1 Session 状态机

| 当前 | 事件 | 目标 | P2 |
|---|---|---|---|
| — | 导入成功（结构合法） | `pending` | 是 |
| `pending` | login_check 通过 | `valid` | 是 |
| `pending` | 未配置/失败/不可达 | `needs_manual` | 是 |
| `needs_manual` | 重新校验通过 | `valid` | 是 |
| `needs_manual` | 重新校验失败 | `needs_manual` | 是 |
| `pending`/`needs_manual` | 人工确认 | `valid`（degraded 快照） | 是 |
| `valid` | 已过期（惰性判定） | `expired` | 是 |
| `valid`/`needs_manual`/`expired` | 管理员失效 | `invalid` | 是 |
| 任意非 `revoked` | 管理员撤销 | `revoked` | 是 |
| `valid`/`needs_manual`/`expired` | 重新校验未通过 | `needs_manual` | 是 |
| `valid` | 接管请求 | `needs_manual` | 是 |
| `acquiring`/`refreshing` | — | — | 预留（P6） |

- `failed` 预留给 P6 自动获取失败；P2 导入/校验失败一律进入 `needs_manual`，便于人工确认或撤销。

- 所有状态变更使用单条 `UPDATE ... WHERE id=$1 AND state=$2`（CAS）；0 行受影响→409 `session_state_conflict`。
- `expiring` 为派生值：`state='valid' AND 剩余时间 < max(10% * (expires_at-created_at), 5min)`；响应字段 `expiring: bool`，不落库。
- 过期判定惰性执行于列表/详情/校验/导入查询；不启动后台任务。
- 撤销/失效同时释放该 Session 所有活跃 lease（`released_at=now(), released_reason='revoked'|'invalidated'`）。
- `last_used_at` 仅在 P6 执行注入时更新；P2 不写。

### 6.2 导入格式与结构校验

**storage_state**（≤5 MiB 请求体）：

```json
{
  "kind": "storage_state",
  "cookies": [
    {"name":"sid","value":"...","domain":"fixture.local","path":"/",
     "expires":1760000000,"httpOnly":true,"secure":false,"sameSite":"Lax"}
  ],
  "origins": [
    {"origin":"http://localhost:13335","localStorage":[{"name":"k","value":"v"}]}
  ]
}
```

- `cookies` 数组 ≤2000；每项 `name` 非空 ≤256、`value` ≤4096、`domain` ≤253、`path` ≤1024；未知字段忽略。
- `origins` 数组 ≤500；每个 `origin` 为 http(s) URL ≤2048；`localStorage` ≤5000 项，`name`/`value` 各 ≤8192。
- `expires` 为秒（-1 或 0 表示 session cookie）。

**token**（API 系统）：

```json
{"kind":"token","headers":{"Authorization":"Bearer fixture-token"}}
```

- `headers` ≤50 项；名匹配 `^[A-Za-z0-9-]{1,64}$`；值 ≤8192 且不含 `\r`/`\n`；禁止 `Host`、`Content-Length`。

**通用**：

- 结构不合法 → 422 `invalid_argument`，`details.field` 指向具体字段，`details.reason='invalid_storage_state'`；不创建 Session。
- `storage_state_hash = sha256(规范化 JSON)`（Go `encoding/json` 对 map 排序键；对象按结构体字段顺序）；用于去重/展示，不用于加密。
- 生效 TTL：`access.session_ttl_seconds ?? system.default_session_ttl_seconds ?? 3600`。
- `expires_at = now + TTL`；若 storage_state 存在最早正数 cookie `expires`，取更早者；若结果 ≤ now → 422 `storage_state_expired`。
- `cookie_count`/`origin_count` 落库便于列表展示。

### 6.3 login_check

- 配置位于 `environments.login_check`：

```json
{"url":"/account","expect_status":[200],"body_contains":"Fixture Account"}
```

- `url` 以 `http://`/`https://` 开头则原样使用；否则与 `environments.base_url` 拼接。为空=未配置。
- 方法仅 GET；超时 10s；跟随重定向（默认最多 10 跳）；读取响应体上限 1 MiB（仅用于 `body_contains`）。
- 请求头：storage_state → Cookie 头由 cookies 组装（仅 name=value）；token → 原样 headers。
- 判定：状态码 ∈ `expect_status`（默认 `[200]`）且 `body_contains` 为空或命中 → `allowed`；否则 `denied`；网络/超时 → `error`。
- 结果快照：

```json
{
  "items": {"login": {"kind":"page","observed":"allowed","status":200}},
  "summary": {"total":1,"allowed":1,"denied":0,"error":0,"degraded":false},
  "permission_hash": "<sha256(sorted items[key,observed])>",
  "degraded": false
}
```

- 未配置 → 不请求；Session `needs_manual`，`failure_reason='login_check_not_configured'`，快照 `items={}`、`summary.degraded=true`。
- 失败 → Session `needs_manual`，`failure_reason` 为 `login_failed`/`env_unreachable`；快照记录 `observed=denied|error`。
- 成功 → Session `valid`，`last_validated_at=now()`。
- 每次校验写一条新快照（不可变）；Session 详情取最新一条。
- 人工确认 → `valid`，新快照 `items={}`、`summary={degraded:true, reason:"manual_confirmation"}`、`permission_hash=sha256("manual")`。
- P2 不因 login_check 失败阻止保存 Session；管理员可选择确认或撤销。

### 6.4 人工接管

- 发起：管理员对非 `revoked` Session 创建 takeover（`reason` + 可选 `instructions` + 可选 `expiresInSeconds` 默认 1800）；Session → `needs_manual`。
- 完成：管理员上传新的 storage_state/token（同 §6.2 结构），系统替换 `sessions.storage_state`、`storage_state_hash`、`cookie_count`/`origin_count`，重跑 login_check；takeover → `completed`，`completed_at=now()`；Session 按校验结果置 `valid`/`needs_manual`。
- 取消：takeover → `cancelled`；Session 保持 `needs_manual`。
- 同一 Session 最多一条 `waiting` takeover；重复发起 → 409 `takeover_not_waiting`（或返回已有）。
- 过期 takeover 在列表/详情读取时惰性置 `expired`。

### 6.5 租约（应用层，无 HTTP）

- `Acquire(ctx, sessionID, holder, ttl)`：要求 Session `valid` 且未过期；无活跃 lease；`expires_at = min(now+ttl(默认30min), session.expires_at)`；唯一索引冲突 → `lease_conflict`。
- `Release(ctx, sessionID, holder, reason)`：CAS 更新 `released_at`。
- 撤销/失效时批量释放。
- P6 执行器直接调用应用服务；P2 仅集成测试覆盖。

### 6.6 应用模型

- `key` 约定：`page:<path>`、`route:<path>`、`api:<METHOD> <path>`、`component:<Name>`；kind 必须与前缀一致；长度 ≤512。
- `parent_key` 可引用同 payload 或库内节点；缺失/环 → 422。
- JSON 导入：`{"nodes":[{...}]}` ≤5000；按 `(system_id,key)` upsert；`created/updated` 计数；不删除未出现的旧节点；`source='import'`。
- 单条 CRUD：key/kind 创建后不可改；PATCH 仅 name/path/method/meta/parent。
- 删除节点级联删除子节点（DDL `ON DELETE CASCADE`）；API 返回 204。

### 6.7 本地 Git 校验

- 输入：`local_path`、`fixed_branch`。
- 步骤（`exec.CommandContext`，固定参数，`GIT_OPTIONAL_LOCKS=0`，超时 5s，不写工作树）：
  1. `filepath.Abs` + `filepath.Clean`；`os.Stat` 必须为目录，否则 `path_not_found`。
  2. `git -C <path> rev-parse --is-inside-work-tree` 输出 `true`，否则 `path_not_git_repo`。
  3. `git -C <path> rev-parse --verify --quiet <branch>^{commit}`，否则 `branch_not_found`。
  4. `git -C <path> rev-parse <branch>^{commit}` 取 40 位 commit。
- 保存/重新校验成功：写 `last_commit`、`last_validated_at=now()`、`last_validation_error=''`；失败：422，不落库（更新时保留旧值）。
- 分支名校验：`^[A-Za-z0-9._/-]{1,255}$`，不以 `-` 开头。
- glob 校验：每项非空 ≤256，总数 ≤200；P2 只存储，不匹配。
- `last_indexed_at` 为 NULL，UI 显示「未索引（P4）」。
- 删除无依赖（P4 引入索引后需 409）。

### 6.8 删除与依赖保护

| 对象 | 规则 |
|---|---|
| System | 有 Environment/AccountSystemAccess 时 409 `has_dependencies`；否则 204 |
| Environment | 有 Session 时 409；否则 204 |
| Account | 有 Access/Session 时 409；否则 204 |
| Access | 有 Session 时 409；否则 204 |
| AppModelNode | 级联删除子树，204 |
| CodeRepository | 无依赖，204 |

---

## 7. API

### 7.1 通用约定

- 前缀 `/api/resources`；请求/响应 `application/json; charset=utf-8`。
- 成功：`{"data": ...}`；列表：`{"data":[...],"next_cursor":"...","has_more":false}`；创建 201，删除 204，动作 200。
- 分页：`limit` 1–100（默认 20）+ 不透明 cursor；筛选参数见各端点。
- 幂等：写请求可带 `Idempotency-Key`（复用 P0 中间件）；非必填。
- 错误：复用 `platform/errors` 信封与闭集 code：400/422=`invalid_argument`，404=`not_found`，409=`conflict`，403=`forbidden`；`details.reason` 细分如下：

| reason | HTTP | 场景 |
|---|---|---|
| `duplicate_key` | 409 | 唯一键冲突 |
| `has_dependencies` | 409 | 删除受保护对象 |
| `session_state_conflict` | 409 | CAS 失败/状态不允许 |
| `invalid_storage_state` | 422 | 导入 JSON 结构非法 |
| `storage_state_expired` | 422 | 计算过期时间已过 |
| `login_check_not_configured` | —（非错误） | Session 进入 needs_manual |
| `login_failed` / `env_unreachable` | —（非错误） | login_check 失败，Session needs_manual |
| `path_not_found` / `path_not_git_repo` / `branch_not_found` | 422 | 代码仓校验失败 |
| `credential_required` | 422 | （P6 预留；P2 不强制） |
| `takeover_not_waiting` | 409 | 接管状态不允许 |
| `lease_conflict` | 409 | 活跃租约冲突（仅内部） |

- 权限：所有 GET = 任意已登录用户；所有写/动作 = admin；普通用户 403 `forbidden`。

### 7.2 系统与环境

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/systems?status=&kind=&limit=&cursor=` | 列表 |
| POST | `/systems` | 创建（admin） |
| GET | `/systems/{id}` | 详情 |
| PATCH | `/systems/{id}` | 更新（key 不可改） |
| DELETE | `/systems/{id}` | 删除/依赖保护 |
| GET | `/systems/{id}/environments` | 环境列表 |
| POST | `/systems/{id}/environments` | 创建环境 |
| GET | `/environments/{id}` | 环境详情 |
| PATCH | `/environments/{id}` | 更新 |
| DELETE | `/environments/{id}` | 删除 |

字段：System `key,name,kind,baseUrl,defaultSessionTtlSeconds,notes,status`；Environment `key,name,baseUrl,kind,isProd,allowedHosts[],loginCheck{url,expectStatus[],bodyContains},allowAutoLogin,requireApproval`。

校验：
- `key` 匹配 `^[a-z0-9][a-z0-9_-]{1,63}$`。
- `base_url` 必须 `http(s)://` 且可解析。
- `login_check.url` 为空或 ≤2048；`expect_status` 每项 100–599，最多 10 个；`body_contains` ≤512。
- `allowed_hosts` 每项 ≤253，最多 50 个（仅存储，P2 不强制）。
- 系统/环境/账号/代码仓的 `key` 创建后不可修改；应用模型的 `key`/`kind` 创建后不可修改。

### 7.3 应用模型

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/systems/{id}/app-models?kind=&parentKey=&limit=&cursor=` | 列表 |
| POST | `/systems/{id}/app-models` | 创建 |
| POST | `/systems/{id}/app-models/import` | JSON 批量 upsert |
| GET | `/app-models/{id}` | 详情 |
| PATCH | `/app-models/{id}` | 更新（key/kind 不可改） |
| DELETE | `/app-models/{id}` | 删除子树 |

导入请求：

```json
{"nodes":[
  {"key":"route:/orders","kind":"route","name":"订单列表","path":"/orders"},
  {"key":"api:GET /api/orders","kind":"api","name":"订单查询","path":"/api/orders","method":"GET","parentKey":"route:/orders"}
]}
```

响应：`{"data":{"created":2,"updated":0}}`。

### 7.4 账号与 Access

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/accounts?status=&kind=&limit=&cursor=` | 列表 |
| POST | `/accounts` | 创建 |
| GET | `/accounts/{id}` | 详情 |
| PATCH | `/accounts/{id}` | 更新（key 不可改） |
| DELETE | `/accounts/{id}` | 删除/依赖保护 |
| GET | `/accounts/{id}/accesses` | Access 列表 |
| POST | `/accounts/{id}/accesses` | 创建 Access |
| GET | `/accesses/{id}` | Access 详情 |
| PATCH | `/accesses/{id}` | 更新 |
| DELETE | `/accesses/{id}` | 删除/依赖保护 |

Access 字段：`systemId,authType,credential,credentialSet,usernameOverride,loginUrl,enabled,concurrencyLimit,sessionTtlSeconds,permissionNote`。
- 请求 `credential`：省略=不变；空串=清除；非空=替换。响应永不返回 `credential`，只回 `credentialSet: bool`。
- `authType` 枚举见 DDL；`concurrencyLimit` 1–100；`sessionTtlSeconds` 60–604800 或 null。
- `basic` 使用 `usernameOverride` + `credential`（密码）；`password/token/apikey` 使用 `credential`；`sso/manual` 的 `credential` 可留空（P2 不执行自动登录）。

### 7.5 Session

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/sessions?accountId=&systemId=&environmentId=&state=&limit=&cursor=` | 列表 |
| GET | `/sessions/{id}` | 详情（含最新快照、活跃 lease、最新 takeover） |
| POST | `/accesses/{id}/sessions` | 导入创建（admin） |
| POST | `/sessions/{id}/validate` | 重新 login_check（admin） |
| POST | `/sessions/{id}/confirm` | 人工确认 valid（admin） |
| POST | `/sessions/{id}/invalidate` | 失效（admin） |
| POST | `/sessions/{id}/revoke` | 撤销（admin） |
| GET | `/sessions/{id}/snapshots?limit=&cursor=` | 快照列表 |

导入请求：

```json
{
  "environmentId": 12,
  "kind": "storage_state",
  "storageState": {"cookies":[...],"origins":[...]},
  "expiresAt": "2026-10-28T12:00:00Z",
  "note": "手工导入"
}
```

或：

```json
{"environmentId":12,"kind":"token","headers":{"Authorization":"Bearer fixture-token"}}
```

- `environmentId` 必填，且必须属于 Access 的 `system_id`；否则 422 `invalid_argument`。
- `POST /code-repos`、`/systems`、`/accounts` 不接受 `companyId`；服务固定使用 `companies(key='default')`。

响应 `201`：SessionDTO（`state` 为 `valid` 或 `needs_manual`；含 `latestSnapshot`）。`expiresAt` 可省略，按 §6.2 计算。

动作允许状态：

| 动作 | 允许来源状态 |
|---|---|
| `validate` | `pending`/`needs_manual`/`valid`/`expired`/`invalid` |
| `confirm` | `pending`/`needs_manual` |
| `invalidate` | `valid`/`needs_manual`/`expired` |
| `revoke` | 任意非 `revoked` |
| 发起 takeover | 任意非 `revoked` |

动作请求/响应：
- `validate`：无 body；`200` SessionDTO + 最新快照。
- `confirm`：`{"note":"人工确认（login_check 未配置）"}`；`200` SessionDTO。
- `invalidate` / `revoke`：`{"reason":"..."}`；`200` SessionDTO。
- 状态不允许时返回 409 `session_state_conflict`。

SessionDTO 字段：`id,accountId,systemId,environmentId,accessId,state,acquisition,storageStateKind,cookieCount,originCount,storageStateHash,expiresAt,lastValidatedAt,lastUsedAt,failureReason,invalidatedReason,expiring,createdAt,updatedAt`；详情额外 `latestSnapshot,activeLease,latestTakeover`。不返回 `storage_state` 或 `credential`。

### 7.6 人工接管

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/sessions/{id}/takeovers` | 发起（admin） |
| GET | `/takeovers?status=waiting&limit=&cursor=` | 列表（登录用户可读） |
| POST | `/takeovers/{id}/complete` | 上传新状态并完成（admin） |
| POST | `/takeovers/{id}/cancel` | 取消（admin） |

发起请求：`{"reason":"mfa","instructions":{"text":"请在 5 分钟内完成验证码"},"expiresInSeconds":1800}`。
完成请求：同导入结构（`kind` + `storageState|headers`）。
响应：TakeoverDTO + `session`（最新 SessionDTO）。

### 7.7 代码仓

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/code-repos?status=&limit=&cursor=` | 列表 |
| POST | `/code-repos` | 创建（保存前校验） |
| GET | `/code-repos/{id}` | 详情 |
| PATCH | `/code-repos/{id}` | 更新（key 不可改） |
| DELETE | `/code-repos/{id}` | 删除 |
| POST | `/code-repos/{id}/validate` | 重新校验并更新 commit |

字段：`key,name,localPath,fixedBranch,includeGlobs[],excludeGlobs[],status,lastCommit,lastValidatedAt,lastValidationError,lastIndexedAt`。

### 7.8 OpenAPI

- 手工追加到 `docs/openapi/api.yaml`：上述路径、DTO、错误响应。
- `GET /api/openapi.yaml` 自动可读。
- `scripts/acceptance.ps1` 增加关键词检查：`/api/resources/systems`、`/api/resources/sessions`、`/api/resources/code-repos`、`storageState`。

---

## 8. 前端

### 8.1 路由与结构

| 路由 | 组成 |
|---|---|
| `/resources` | `ResourcesPage` + Tabs：系统与环境 / 应用模型 / 账号与访问 / 会话 |
| `/code-repos` | `CodeReposPage`：列表 + 创建/编辑 + 重新校验 + 索引占位 |

feature 目录：`frontend/src/features/resources/{ResourcesPage.tsx,api.ts,types.ts,SystemsPanel.tsx,AppModelsPanel.tsx,AccountsPanel.tsx,SessionsPanel.tsx,SessionDetail.tsx,ImportSessionDialog.tsx,TakeoverDialog.tsx,*.test.tsx}`；`frontend/src/features/code-repos/{CodeReposPage.tsx,api.ts,*.test.tsx}`。

### 8.2 交互

- `useAuth()` 取 `role`；`canWrite = role === 'admin'`；普通用户隐藏写/动作按钮，页面顶部显示「只读」徽章。
- 深链：`/resources?tab=systems&system=<id>`、`?tab=sessions&session=<id>`；切换时 `router.replace`。
- 数据获取：`apiGet/apiPost/apiPatch/apiDelete`；每次写操作后重取当前 Tab；无 SSE。
- 系统与环境：左侧系统列表 + 右侧详情/编辑；环境下表格，编辑用内联表单或原生 `<dialog>`。
- 应用模型：系统选择器 + kind 过滤 + 表格；JSON 导入对话框显示 `created/updated` 或错误行。
- 账号与访问：账号列表 + Access 表格；credential 输入框留空=保留、勾选“清除”=清空；编辑时不回显。
- 会话：状态徽章（valid/needs_manual/expired/invalid/revoked/failed/pending）；详情显示有效期、最后校验、快照摘要、接管状态与操作按钮；导入对话框支持文件选择（读文本）或 token JSON 文本框。
- 代码仓：表格 + 表单；保存失败显示 422 reason（路径/分支）；成功后显示 lastCommit 与「未索引（P4）」。
- 错误 `role="alert"`，成功 `role="status"`；加载/空态复用 `EmptyState`；状态徽章扩展 `StatusBadge` 的 tone 映射。
- 可访问性：所有输入有 `<label>`；对话框焦点管理；按钮 disabled 态；键盘可操作。

### 8.3 只读验收

- 普通用户访问 `/resources` 与 `/code-repos` 显示数据与只读徽章，无新建/编辑/删除/导入/校验/确认/撤销按钮。
- 直接调用写 API 返回 403；E2E 使用真实浏览器 `request.post` 断言。

---

## 9. 测试与验收

### 9.1 分层

| 层 | 覆盖 |
|---|---|
| 领域单测 | key/kind 校验、状态机 CAS 表、导入结构校验、TTL/过期计算、快照 hash、glob/分支校验 |
| 应用单测 | 服务编排（fake repo/LoginChecker/GitValidator）、权限由 handler 测、删除依赖、接管状态 |
| HTTP 单测 | DTO 映射、credential 不回显、422/409 reason、普通用户 403 |
| 集成（Postgres） | 迁移幂等、CRUD、Session 导入→校验→confirm→invalidate/revoke、租约、应用模型导入、代码仓（临时 git 仓库）、响应不含 `storage_state`/`credential` |
| 前端单测 | 只读态、表单校验、状态徽章、导入错误展示、credential 留空语义 |
| E2E | 管理员全链路、普通用户只读、代码仓配置、fixture 目标 |

### 9.2 fixture target

`scripts/fixture-target.cjs`（Node，端口 13335）：

| 端点 | 行为 |
|---|---|
| `GET /login` | HTML 登录页（username/password） |
| `POST /login` | 校验 `fixture/fixture-pass`；成功 `Set-Cookie: fixture_session=ok; HttpOnly; Path=/`，302 `/account`；失败 401 |
| `GET /account` | 有有效 cookie → 200，HTML 含 `Fixture Account`；否则 302 `/login` |
| `GET /api/profile` | `Authorization: Bearer fixture-token` → 200 JSON；否则 401 |
| `GET /health` | 200 `ok` |

E2E 流程：
1. Playwright 打开 `http://127.0.0.1:13335/login`，登录后 `context.storageState()` 导出。
2. 管理员在 EzTest 创建 System（web）+ Environment（login_check `/account`）+ Account + Access。
3. 导入 storageState → 断言 Session `valid` + 快照 `allowed`；截图。
4. 撤销 → 断言 `revoked`；再导入 → `validate`；人工 `confirm`（degraded）。
5. 普通用户登录 → 只读徽章 + 写 API 403。
6. token 导入 + API 系统 login_check（`/api/profile`，expect_status 200）→ valid。
7. 代码仓：脚本创建 `.tmp/e2e-repo`（git init + commit），UI 保存并校验，断言 commit 显示。
8. 截图保存到 `.tmp/p2-acceptance/`（gitignored）；报告写 `.superpowers/sdd/<plan>/`（gitignored）。

### 9.3 E2E 文件

- `frontend/e2e/resources.spec.ts`：管理员全链路 + 普通用户只读。
- `frontend/e2e/code-repos.spec.ts`：代码仓创建/校验/编辑。
- `frontend/e2e/resources-session-actions.spec.ts`：validate/confirm/invalidate/revoke/takeover。
- `scripts/e2e.ps1`：启动/停止 fixture target；不产生 LLM 费用。

### 9.4 DoD 映射

| P2 DoD（implementation-phases §2-P2） | 验证 |
|---|---|
| 管理员可配置系统/环境/账号；导入 Session 后 valid，可被后续用例引用 | E2E + 集成；Session 持久化 + `access_id` 供 P6 使用 |
| Session 失效/撤销/人工接管路径可用（至少手工） | 集成 + E2E 动作；接管完成上传新状态 |
| 普通用户只能查看/使用，不能改配置 | API 403 集成 + E2E 只读 |
| 本地代码仓配置保存成功，能看到路径/分支/commit（索引占位） | 集成临时仓库 + E2E `/code-repos` |
| §2.9 #16 公共资源管理 UI | `/resources` 四 Tab + `/code-repos` |
| 权限快照最小版（login_check） | 快照行 + UI 摘要 + degraded 标记 |

### 9.5 人工验收（Agent 执行）

- 启动 `dev-api`/`dev-web` 与 fixture；用 Playwright 以管理员登录 EzTest。
- 完成：建系统/环境/账号/Access → 登录 fixture 导出 storageState → 导入 → 校验 valid → 截图 → 撤销 → 重新导入 → 人工确认 → 普通用户只读。
- 保存截图与步骤日志；输出验收报告到 `.superpowers/sdd/`。

---

## 10. 风险与降级

| 风险 | 处理 |
|---|---|
| login_check 未配置/不可达 | Session `needs_manual` + degraded 快照；管理员确认可继续（验收路径覆盖） |
| fixture 登录页/端口不稳定 | 固定端口 13335 + 健康检查重试；E2E 启动前探活 |
| Git 路径非法/分支不存在 | 422 不落库；UI 显示 reason；不写工作树 |
| 明文凭据（D28） | 接受；不写日志/不回显；未来需要时再加加密 |
| 无后台任务 | 过期惰性判定；无 GC；P8 硬化 |
| 无 Docker | Compose 仅静态校验；代码仓只读挂载写入部署文档 |
| 无 SSE | 页面重取；P6 再引入执行事件 |
| P1 诊断 Agent | 保留到 P3；D27「P2 起替换」在本阶段不执行（D28 说明） |

---

## 11. 交付物

### 11.1 后端

- `backend/internal/resource/**` 四层实现与测试。
- 三个迁移文件 + `order.txt` 追加。
- `cmd/eztest/main.go`、`server/server.go` 装配。
- `docs/openapi/api.yaml` 追加。

### 11.2 前端

- `frontend/src/features/resources/**`、`frontend/src/features/code-repos/**`。
- 两个路由页面替换占位。
- `nav-items.ts` 保持 P2 条目，必要时更新描述。
- E2E 三个 spec + fixture 脚本 + `e2e.ps1` 接线。

### 11.3 文档

- 本 spec。
- D28 写入 `system-blueprint.md` §3.2，并同步 §5.1/§9/§10.2/§11.1。
- `auth-session-model.md` 顶部加 P2 实施口径说明（D28），§10/§11 标注 P2 裁剪。
- `implementation-phases.md` §2-P2 风险行更新（去除加密/审计要求，注明 D28）。
- `frontend-design.md` PG7/PG8 对齐 `/resources` 与 `/code-repos`。
- `Agent.md` §6.1 目录树新增 `resource` 上下文。
- `README.md` 增加 P2 功能与 fixture 说明；`.env.example` 无新增变量。

---

## 12. 设计变更与文档同步（D28）

### 12.1 D28 原文（写入 `system-blueprint.md` §3.2 决策记录）

> **D28 P2 公共资源与身份：单 `resource` 上下文、明文存储、无审计/加密**
> P2 新增 `resource` 限界上下文，承载 System/Environment/AppModel/Account/Access/Session/Snapshot/Lease/Takeover/LocalGitRepo；单公司种子，不做公司 CRUD。被测系统凭据（`account_system_accesses.credential`）与 Session 状态（`sessions.storage_state`）**明文存储**；P2 不实现加密、脱敏、审计（不写 `audit_logs`，不建 `session_events`/`secrets`/`session_blobs`）。Session 状态采用 auth-session-model 完整枚举 + 派生 `expiring`；快照表名 `session_snapshots`；API 统一 `/api/resources/*`；`System.kind` 仅 `web|api`。P2 不做自动登录、ProbeRecipe、代码索引、Session 执行注入。理由：内部工具场景，被测系统凭据非生产秘密；先交付资源管理闭环，安全能力在需要时再引入。

### 12.2 同步修改

| 文档 | 修改 |
|---|---|
| `system-blueprint.md` | §3.2 追加 D28；§5.1 资源域去掉 secrets/session_blobs/session_events 表述；§5.5 Session 状态补全；§9 标注明文与 P2 范围；§10.2 表清单改为 P2 11 表+占位说明；§11.1 确认 `/api/resources/*` |
| `auth-session-model.md` | 顶部加「P2 实施口径（D28）」：明文、无审计/加密、凭证并入 Access、storageState 并入 Session、不建 secrets/session_blobs/session_events；§3/§10/§11 标注 P2 裁剪 |
| `implementation-phases.md` | §2-P2 风险行改为「凭据/Session 明文（D28）；人工接管流程尽早打通」；§2-P2 范围注明 Session 事件/审计不做 |
| `frontend-design.md` | PG7/PG8 对齐本规格路由与 Tab |
| `Agent.md` | §6.1 目录树新增 `internal/resource/` 四层；§6.5 接入清单不变 |

---

## 13. 明确不做（再次确认）

- 加密/脱敏/审计日志/事件流（D28）。
- 自动登录、AuthFlow、TOTP、验证码自动处理（P6）。
- ProbeRecipe、能力探测、漂移、审批拦截（P6+）。
- Session 租约 HTTP API、后台调度/回收（P6）。
- 代码索引、diff、影响分析、monorepo 子项目（P4）。
- 应用自动探索、脑图、用例设计（P5）。
- 浏览器/HTTP 业务执行与 Session 注入（P6）。
- 多公司/多租户/公司 CRUD、RBAC、多用户协作（P8 可选）。
- SSE `resource.*`/`session.*` 事件。
- Session blob GC/保留期。
- 真实外部被测系统接入（验收用 fixture；真实系统可选）。

---

## 14. 已知假设

1. P0/P1 已完成并合入 `dev`，端口/脚本/测试库可正常运行。
2. P2 在 `feat/p2-resources-identity` 分支实施，验收后 ff 合回 `dev`，不 push。
3. 管理员与普通用户由 P0 提供；P2 不新增角色。
4. fixture target 仅用于测试/验收，不进生产部署。
5. 预计任务数 18–22，按 Wave 并发执行；详细拆解由 `writing-plans` 产出。
6. 若实现中发现 D28 与新需求冲突，先更新本规格与 D28，再继续实现（禁止代码先行）。
