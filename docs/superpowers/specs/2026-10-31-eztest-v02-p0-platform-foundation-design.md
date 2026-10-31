# EzTest 0.2 P0 平台底座 — 实施规格

> 状态：实施规格（implementation spec）；2026-10-31 已按用户对 6 个阻塞缺口的裁决更新，待用户审阅后进入 writing-plans。
> 日期：2026-10-31
> 阶段：P0（[phase-00-platform-foundation.md](../../version-V0.2/phases/phase-00-platform-foundation.md)）
> 权威来源：`docs/version-V0.2/architecture-design.md`、`high-level-design.md`、`research/decision-log.md`、`research/open-decisions.md`、`implementation-phase-plan.md`、`ui/mock.html`。
> 冲突处理：本文件不替代设计文档；与设计文档冲突时以设计文档为准，并停止实现报告冲突。本文件只把设计落到 P0 可执行的边界、命名与验收。
> 用户裁决（2026-10-31）：固定默认管理员；不透明 Bearer 7d；雪花 ID + DB node；代码仓任意登录用户 CRUD；LLM 专用表 + 明文掩码 + 仅配置；P0 冻结完整设置目录。其余 P0 实现决策见 §13。

---

## 1. 目标与退出标准

### 1.1 目标

建立 EzTest 0.2 的新地基，使后续 P1–P8 可以只消费冻结契约：

1. 严格 4 项启动 env + DB 系统设置中心。
2. 身份/登录/用户管理；空库种子固定默认管理员 + 自助改密。
3. LLM 配置与模型目录、连通性测试（不含 Agent 运行时）。
4. 持久事件日志 + 快照 + SSE 实时流/回放。
5. 内容对象 + 引用账本（content_object/content_ref）。
6. 追加式审计。
7. 资源域：SUT、环境、页面、API、目标库、测试账号、代码仓。
8. 版本与多 SUT 绑定（环境、页面/API scope、库、账号、代码仓）。
9. 前端全局壳 8 项、版本详情 8 Tab 骨架、资源页、设置页、登录页。
10. contracts 骨架、fake LLM 测试上游、单测/集成/验收脚本。

### 1.2 退出标准（阶段验收）

1. 管理员登录 → 配置 LLM/设置 → 创建 SUT/环境/页面/API/库/账号/代码仓 → 页面/API 清单可见。
2. 任意登录用户创建版本 → 绑定多个 SUT（各选环境、页面/API scope、库、账号、代码仓）→ 版本详情壳可打开。
3. 设置中心改一项非重启配置 → 立即生效；事件流可快照 + 回放；迁移重复执行无副作用。
4. 测试全绿；contracts 骨架 frozen；handoff 完成。
5. 0.1 旧控制面假设未回流：无 8 状态机、无人工批次、无整版冻结、无 design review 页签。

### 1.3 不在 P0

- 任务板/租约/reconciler、Agent runtime、context packet、conversation/requirement、design/脑图、execution/wave、delivery/issue/report、chain、knowledge、codeintel、visual、quality、search、todos、GC、PDF、多副本编排。
- 任何 0.1 数据迁移；接口/数据兼容。
- LLM chat completion 网关（P1 使用；P0 只做配置/模型目录/连通性）。
- executor 改动（P3 使用；P0 只保留 `executor/` 目录不动）。

---

## 2. 实施策略与仓库边界

### 2.1 就地重写（P0-D2）

- 在分支 `feat/v0.2-p0` 上**就地重写** `backend/` 与 `frontend/`；0.1 完整实现保留在分支 `backup/0.1-dev` 与 git 历史中，仅作参考。
- `executor/`、`deploy/`、`docs/` 等目录不在 P0 删除；0.2 后续阶段继续复用 executor。
- 新数据库：`eztest_v02`（开发/演示）与 `eztest_v02_test`（集成测试），与 0.1 的 `eztest`/`eztest_test` 隔离；不迁移 0.1 数据（OD-5）。
- 0.1 的通用确定性底座（统一错误信封、请求 ID、api-client、sse-client、bcrypt/token 思路）可移植/重写；禁止复制 0.1 控制面状态机。

### 2.2 后端目录（四层强制）

```text
backend/
├─ go.mod                          # 沿用 module github.com/EdouardRichard/EzTest/backend
├─ cmd/eztest/main.go              # 唯一组装根：env → DB → migrate → seed → wire → HTTP
└─ internal/
   ├─ config/                      # 仅 4 项 env 解析/校验
   ├─ db/                          # pgx pool、事务 helper、迁移执行器
   │  └─ migrations/               # <context>_<NNNN>_<desc>.sql + order.txt
   ├─ platform/                    # errors/envelope、request id、pagination、validation、id
   ├─ server/                      # ServeMux 装配、中间件、CORS、health
   ├─ testutil/                    # 集成测试 DB（schema 隔离）/fixtures
   ├─ settings/{domain,application,infrastructure,interfaces/httpapi}/
   ├─ identity/{domain,application,infrastructure,interfaces/httpapi}/
   ├─ llm/{domain,application,infrastructure,interfaces/httpapi}/
   ├─ eventing/{domain,application,infrastructure,interfaces/httpapi}/
   ├─ storage/{domain,application,infrastructure,interfaces/httpapi}/
   ├─ audit/{domain,application,infrastructure,interfaces/httpapi}/
   ├─ resource/{domain,application,infrastructure,interfaces/httpapi}/
   └─ version/{domain,application,infrastructure,interfaces/httpapi}/
```

导入方向遵守 Agent.md §6.2：`domain` 只依赖标准库与 `platform`；`application` 可依赖同上下文 `domain` 与 `platform`；`infrastructure` 实现 domain/app 端口；`interfaces/httpapi` 只调 application；跨上下文只依赖对方 `application` 接口（或本地窄接口 + 组装根适配），禁止导入对方 infrastructure/interfaces。

### 2.3 前端目录

```text
frontend/
├─ src/app/                       # 只做路由/布局/页面组装
│  ├─ layout.tsx  globals 引用
│  ├─ login/page.tsx
│  └─ (app)/{versions,issues,reports,knowledge,resources,quality,agents,settings}/...
├─ src/features/{auth,shell,versions,resources,settings}/   # 业务状态与交互
├─ src/components/ui/             # 共享原语（Button/Input/Select/Dialog/Tabs/Badge/Table/Toast…）
├─ src/lib/{api-client,sse-client,auth-storage,format}.ts
├─ src/navigation/nav-items.ts
└─ src/styles/globals.css
```

规则：所有 HTTP 走 `src/lib/api-client.ts`；SSE 只走 `src/lib/sse-client.ts`；不新建 `utils/`；路由只做组装。

### 2.4 契约与证据

- 本阶段写 `docs/version-V0.2/contracts/{openapi.yaml,events.md,db-schema.md,migrations-order.md,settings.md}` 并在 handoff 标记 `frozen: P0`。
- 测试/验收证据写 `.tmp/p0-acceptance/`（日志、截图），handoff 引用路径；不得只写“已通过”。
- 设计文档只读；发现冲突停止并报告。

---

## 3. 启动配置（严格 4 项 env）

| 变量 | 必填 | 默认 | 用途 |
|---|---|---|---|
| `EZTEST_DATABASE_URL` | 是 | — | PostgreSQL URL（pgx） |
| `EZTEST_API_PORT` | 否 | `13333` | API 监听端口；绑定 `:port` |
| `EZTEST_WEB_PORT` | 否 | `6868` | Web 端口；用于派生 CORS origin 与设置默认值 |
| `EZTEST_EXECUTOR_PORT` | 否 | `13336` | 派生默认 `execution.executor_url=http://127.0.0.1:{port}` |

- **没有第 5 个 EzTest 启动 env**。数据目录、LLM、Agent、并发、路径、保留期、报告、知识、安全、功能开关全部进 DB 设置中心。
- `EZTEST_WEB_ORIGIN` 不存在；CORS 允许 `http://localhost:{EZTEST_WEB_PORT}` 与 `http://127.0.0.1:{EZTEST_WEB_PORT}`。
- 前端构建期变量 `NEXT_PUBLIC_API_BASE_URL`（默认 `http://localhost:13333`）仅用于浏览器定位 API，不属于 EzTest 运行时启动配置；部署换端口时需重建 Web。
- 校验：端口 1–65535；DB URL 必填且 scheme 为 postgres/postgresql；缺失/非法时启动失败并输出明确错误，不猜测。
- 启动顺序：读 env → 连 DB → 迁移（advisory lock）→ seed settings → 构造依赖 → 启动 HTTP。

---

## 4. 身份、认证与管理员种子

### 4.1 固定默认管理员（P0-D1，用户已裁决）

设计文档规定 `.env` 只有 4 项、种子只允许幂等初始化管理员，但未定义管理员凭据来源。用户裁决采用**固定默认凭据**，不新增 env：

1. 启动种子：迁移后、HTTP 启动前，若 `app_user` 为空，创建唯一 admin：`username=admin`、`display_name=管理员`、`password=admin123`、`role=admin`、`status=active`。重复启动幂等（用户表非空不写）。
2. 不做 `/api/auth/bootstrap`；不提供“初始化管理员”页面。
3. 登录页固定显示默认凭据提示：“首次启动默认 admin / admin123，登录后请在设置中修改密码”。
4. 密码策略因默认凭据定为 **8–128 字符**；用户登录后可通过 `POST /api/auth/password` 自助改密。
5. 安全风险：固定默认凭据只适用于开发/测试受控环境；生产/对外部署必须在首启改密。写入 handoff 风险；不实现强制改密。

### 4.2 用户与令牌

- 表 `app_user`：`id BIGINT PK(雪花)`、`username TEXT UNIQUE`、`display_name`、`password_hash`、`role`（`admin|user`）、`status`（`active|disabled`）、`created_at`、`updated_at`。
- 单管理员约束：种子创建唯一 admin；`POST /api/admin/users` 只能创建 `role=user`；PATCH 不允许提升为 admin、不允许禁用唯一 admin。
- 表 `auth_token`：`id BIGINT PK`、`user_id`、`token_hash`（SHA-256 hex）、`expires_at`、`created_at`。
- Token：`ezt_` + 32 字节 crypto/rand 的 base64url（无 padding），响应只返回一次明文；DB 只存小写 hex SHA-256；TTL **7 天固定**（用户裁决）；不刷新/不续期；登出删除当前 token；禁用用户删除全部 token。
- 密码：**8–128 字符**；先 SHA-256+base64 预哈希再 bcrypt（避免 72 字节截断），bcrypt cost **12**；不要求复杂度（开发测试信任模型）。
- 用户名：`^[a-zA-Z0-9_.-]{3,64}$`，创建后不可改。
- 传输：`Authorization: Bearer <token>`；401 `unauthenticated`；403 `forbidden`。
- 登录失败记录审计（`auth.login_failed`）；成功记录 `auth.login`；登出记录 `auth.logout`；改密记录 `auth.password_changed`。
- 不做登录限流/验证码（P0 明确后置；记录到 handoff 风险）。

### 4.3 权限矩阵（P0）

| 资源 | 读 | 写 |
|---|---|---|
| `/api/auth/login` | 公开 | — |
| `/api/auth/me`、`/api/auth/logout`、`/api/auth/password` | 登录用户 | 登录用户 |
| `/api/admin/users/*` | admin | admin |
| `/api/admin/settings/*` | admin | admin |
| `/api/admin/llm/*` | admin | admin |
| `/api/admin/audit-logs` | admin | — |
| `/api/resources/*` | 登录用户 | admin（**代码仓例外：任意登录用户完整 CRUD**，D09/architecture） |
| `/api/versions/*` | 登录用户（所有版本可见，单团队） | 登录用户 |
| `/api/content/*` | 登录用户 | 登录用户上传 |
| `/api/events`、`/api/stream` | 登录用户 | — |

---

## 5. 系统设置中心

### 5.1 表

`system_setting(key TEXT PK, value_json JSONB, category TEXT, type TEXT, is_secret BOOLEAN, restart_required BOOLEAN, description TEXT, version INT, updated_by BIGINT NULL, updated_at TIMESTAMPTZ)`。

`system_setting_revision(id BIGINT PK, setting_key, old_value JSONB NULL, new_value JSONB, actor_id BIGINT NULL, created_at TIMESTAMPTZ)`。

- `type ∈ string|number|boolean|json`；secret 用 `is_secret` 标记，不引入独立类型；列名用 HLD 的 `type`（PostgreSQL 非保留字）。
- seed 幂等：`INSERT ... ON CONFLICT (key) DO NOTHING`；不覆盖已存在值。
- 更新：校验 key 存在与类型/区间 → 写 revision → 更新 setting（version+1）→ 失效缓存 → 发 `settings.updated` 事件 → 审计。
- 热加载：Service 持有读缓存；更新后立即 reload；`restart_required` 只做 UI 提示，不阻断。
- GET 返回 secret 时 `value=null`、`is_secret=true`；revision 中 secret 的 old/new 也置 null。PATCH secret 时 `value` 省略/`null` 表示不修改；`""` 表示清空。
- 未知 key → 404；类型/区间错误 → 400 `invalid_argument`。

### 5.2 设置键目录与默认值（P0-D7）

| key | 类型 | 默认 | 类别 | restart |
|---|---|---|---|---|
| runtime.snowflake_node_id | number(0–1023) | 1 | runtime | 是 |
| paths.data_root | string | ./.data | paths | 是 |
| paths.attachment_dir | string | ./.data/attachments | paths | 否 |
| paths.evidence_dir | string | ./.data/evidence | paths | 否 |
| paths.report_dir | string | ./.data/reports | paths | 否 |
| paths.export_dir | string | ./.data/exports | paths | 否 |
| execution.global_concurrency | number(1–32) | 3 | execution | 否 |
| execution.per_sut_concurrency | number(1–16) | 2 | execution | 否 |
| execution.cross_system_concurrency | number(1–8) | 1 | execution | 否 |
| execution.executor_url | string | `http://127.0.0.1:{EXECUTOR_PORT}` | execution | 否 |
| execution.evidence_max_bytes | number | 5242880 | execution | 否 |
| execution.case_timeout_seconds | number | 900 | execution | 否 |
| agent.max_turns | number | 12 | agent | 否 |
| agent.run_soft_timeout_seconds | number | 300 | agent | 否 |
| agent.run_hard_timeout_seconds | number | 600 | agent | 否 |
| agent.max_concurrent_runs | number | 4 | agent | 否 |
| agent.tool_result_max_bytes | number | 32768 | agent | 否 |
| llm.request_timeout_seconds | number | 60 | llm | 否 |
| llm.max_retries | number | 2 | llm | 否 |
| context.window_tokens | number | 64000 | context | 否 |
| context.output_reserve_tokens | number | 8000 | context | 否 |
| context.compact_threshold | number(0.5–0.9) | 0.7 | context | 否 |
| context.keep_recent_messages | number | 12 | context | 否 |
| team_room.digest_every_n_posts | number | 20 | team_room | 否 |
| team_room.digest_interval_minutes | number | 10 | team_room | 否 |
| team_room.digest_keep_versions | number | 5 | team_room | 否 |
| retention.temp_hours | number | 24 | retention | 否 |
| retention.replaced_export_days | number | 7 | retention | 否 |
| retention.unreferenced_content_days | number | 30 | retention | 否 |
| retention.gc_grace_hours | number | 24 | retention | 否 |
| retention.gc_dry_run | boolean | true | retention | 否 |
| report.template_version | string | 1 | report | 否 |
| report.render_pdf | boolean | true | report | 否 |
| report.section_summary | boolean | true | report | 否 |
| report.section_results | boolean | true | report | 否 |
| report.section_issues | boolean | true | report | 否 |
| report.section_coverage | boolean | true | report | 否 |
| report.section_evidence | boolean | true | report | 否 |
| knowledge.injection_budget_tokens | number | 4000 | knowledge | 否 |
| knowledge.review_required | boolean | true | knowledge | 否 |
| security.audit_retention_days | number | 90 | security | 否 |
| security.mask_secrets | boolean | true | security | 否 |
| feature.auto_gap_design | boolean | true | feature | 否 |
| feature.experimental | boolean | false | feature | 否 |

默认值来自 OD-4/OD-8/OD-9/架构 §9–§12；未在设计中逐项给出的键由 P0 冻结，后续阶段只能通过合同变更。

---

## 6. 数据模型、ID 与迁移

### 6.1 ID 规则（P0-D4/P0-D6）

- 所有业务实体主键：雪花 `BIGINT`（41bit ms + 10bit node + 12bit seq），应用层生成；JSON/URL 中一律十进制字符串；Go 内部 int64。
- 雪花 node 来自 DB 设置 `runtime.snowflake_node_id`（默认 1）；因为严格 4 env 不能新增 `EZTEST_SNOWFLAKE_NODE_ID`，多实例唯一性由设置中心保证。
- 例外：`event.seq` 是**频道内**事件顺序号（由 `event_channel.last_seq` 在同一事务内分配），不是业务实体 ID；`content_object.sha256` 是内容主键（64 hex）；`system_setting.key` 是自然键。事件频道用 `event_channel` 维护每频道 `last_seq`，SSE 游标是 `(channel, seq)`。
- 时间：`TIMESTAMPTZ`，JSON RFC3339 UTC；审计列统一 `created_at/updated_at`（D34 例外）。

### 6.2 P0 表清单与迁移

| 迁移文件 | 表 | 说明 |
|---|---|---|
| `settings_0001_settings.sql` | `system_setting`、`system_setting_revision` | 设置中心 |
| `identity_0001_identity.sql` | `app_user`、`auth_token` | 身份/令牌 |
| `llm_0001_llm.sql` | `llm_config`、`llm_model` | LLM 配置/模型目录 |
| `eventing_0001_events.sql` | `event_channel`、`event` | 持久事件（每频道 seq） |
| `storage_0001_content_ledger.sql` | `content_object`、`content_ref` | 内容账本 |
| `audit_0001_audit_log.sql` | `audit_log` | 审计 |
| `resource_0001_catalog.sql` | `sut`、`sut_environment`、`sut_page`、`sut_api`、`target_database`、`test_account`、`code_repository` | 资源 |
| `version_0001_versions.sql` | `version`、`version_sut`、`version_sut_resource` | 版本/绑定 |

迁移顺序由 `backend/internal/db/migrations/order.txt` 显式列出（settings → identity → llm → eventing → storage → audit → resource → version），执行器校验每个内嵌 `.sql` 恰好出现一次；已应用迁移 checksum 不可变，只追加新文件。

### 6.3 关键约束

- `sut.name` 唯一；`sut_environment(sut_id,name)` 唯一；每个 SUT 至多一个 `is_default=true` 环境（部分唯一索引）。
- `sut_page(environment_id,route_template)` 唯一；`sut_api(environment_id,method,path_template)` 唯一；页面/API 必须属于同一 SUT 的环境。
- `test_account(environment_id,username)` 唯一；密码字段只入不出（响应 `password_set`）。
- `code_repository.name` 唯一；`version_sut(version_id,sut_id)` 唯一；`version_sut_resource(version_sut_id,kind,resource_id,role)` 唯一。
- `version.status ∈ initial|running|completed`；P0 只写 `initial`，迁移/收尾属于 P1+。
- `version_sut.scope JSONB = {"pages":[ids],"apis":[ids]}`；`version_sut_resource.kind ∈ database|account`。
- 跨上下文只做逻辑引用：`version_sut.sut_id/environment_id/code_repo_id` 与 `version_sut_resource.resource_id` 不建 FK；同一上下文 FK 可用。
- 所有枚举列加 CHECK；表名/索引名沿用 `Agent.md` §6.9 的数据库命名规约（`pk_<table>`、`uk_<table>_...`、`idx_<table>_...`；这是 0.1 的命名约定，不是 0.2 的 D34——0.2 的 D34 是跨版本知识，不能混用编号）。

### 6.4 删除守卫（P0-D10）

- 删除 SUT/环境/页面/API/库/账号/仓库前，通过 `resource/application.BindingUsageChecker` 检查是否被任何 `version_sut` 绑定/scope 引用；被引用返回 409 `conflict`。
- 实现：version application 提供 `Usage` 查询；组装根以适配器注入 resource handler；resource 不导入 version 包。

---

## 7. P0 API 契约（摘要，详见 contracts/openapi.yaml）

### 7.1 通用约定

- 成功：`{"data": ...}`；列表：`{"data":[...],"next_cursor":"<id|null>","has_more":bool}`。
- 错误：`{"error":{"code":"...","message":"...","request_id":"...","retryable":false,"details":{}}}`；HTTP 400/401/403/404/409/500/503；`X-Request-ID` 回显。
- 所有 ID 为十进制字符串；时间 RFC3339；未知字段默认忽略；写请求 JSON `Content-Type: application/json`。
- 鉴权：Bearer；公开端点仅 login/health；管理员由启动种子提供（admin/admin123）。
- 幂等：P0 不实现通用 `Idempotency-Key`（后续阶段对波次/确认/导出按设计接入）；P0 文档标明这一点，不假装支持。

### 7.2 端点

| 方法/路径 | 权限 | 说明 |
|---|---|---|
| `GET /healthz` | 公开 | 存活 |
| `GET /api/health` | 公开 | DB ping、版本、settings_loaded、executor_url |
| `POST /api/auth/login` | 公开 | 用户名/密码登录 |
| `POST /api/auth/logout` | 登录 | 撤销当前 token |
| `GET /api/auth/me` | 登录 | 当前用户 |
| `POST /api/auth/password` | 登录 | 自助改密（旧密码 + 新密码） |
| `GET/POST /api/admin/users` | admin | 列表/创建普通用户 |
| `PATCH /api/admin/users/{id}` | admin | 显示名/状态 |
| `POST /api/admin/users/{id}/reset-password` | admin | 重置密码 |
| `GET /api/admin/settings` | admin | 全部设置（secret 掩码） |
| `GET/PATCH /api/admin/settings/{key}` | admin | 读取/更新单项 |
| `GET /api/admin/settings/revisions` | admin | 变更历史 |
| `GET/POST /api/admin/llm/configs` | admin | LLM 配置列表/创建 |
| `GET/PATCH/DELETE /api/admin/llm/configs/{id}` | admin | 配置详情/更新/删除 |
| `POST /api/admin/llm/configs/{id}/test` | admin | 连通性测试 |
| `POST /api/admin/llm/configs/{id}/refresh-models` | admin | 拉取/覆盖模型目录 |
| `POST /api/admin/llm/configs/{id}/models` | admin | 手工新增模型 |
| `DELETE /api/admin/llm/configs/{id}/models/{modelId}` | admin | 删除模型 |
| `GET /api/admin/llm/models` | admin | 全部模型 |
| `GET /api/admin/audit-logs` | admin | 审计查询（action/actor/cursor） |
| `GET /api/events` | 登录 | 事件快照（channel/after/limit） |
| `GET /api/stream` | 登录 | SSE（channel/after 或 Last-Event-ID） |
| `POST /api/content` | 登录 | multipart 上传（≤10 MiB） |
| `GET /api/content/{sha256}` | 登录 | 下载并校验 sha |
| `GET /api/resources/catalog` | 登录 | 资源聚合（版本创建用） |
| `GET/POST /api/resources/suts` | 读登录/写 admin | SUT |
| `GET/PATCH/DELETE /api/resources/suts/{id}` | 读登录/写 admin | SUT |
| `GET/POST /api/resources/suts/{id}/environments` | 读登录/写 admin | 环境 |
| `PATCH/DELETE /api/resources/environments/{id}` | 读登录/写 admin | 环境 |
| `GET/POST /api/resources/environments/{id}/pages` | 读登录/写 admin | 页面 |
| `PATCH/DELETE /api/resources/pages/{id}` | 读登录/写 admin | 页面 |
| `POST /api/resources/environments/{id}/pages/import` | admin | 批量导入页面 |
| `GET/POST /api/resources/environments/{id}/apis` | 读登录/写 admin | API |
| `PATCH/DELETE /api/resources/apis/{id}` | 读登录/写 admin | API |
| `POST /api/resources/environments/{id}/apis/import` | admin | 批量导入 API |
| `GET/POST /api/resources/suts/{id}/databases` | 读登录/写 admin | 目标库 |
| `PATCH/DELETE /api/resources/databases/{id}` | 读登录/写 admin | 目标库 |
| `GET/POST /api/resources/suts/{id}/accounts` | 读登录/写 admin | 测试账号 |
| `PATCH/DELETE /api/resources/accounts/{id}` | 读登录/写 admin | 测试账号 |
| `GET/POST /api/resources/code-repos` | 登录 | 代码仓（任意登录用户可写） |
| `GET/PATCH/DELETE /api/resources/code-repos/{id}` | 登录 | 代码仓 |
| `GET/POST /api/versions` | 登录 | 版本列表/创建 |
| `GET/PATCH /api/versions/{id}` | 登录 | 版本详情/标题与开关 |

### 7.3 版本创建/详情

创建请求：

```json
{
  "title": "订单中心 2026 Q4 迭代",
  "bindings": [
    {
      "sut_id": "1001",
      "environment_id": "1101",
      "code_repository_id": "1201",
      "page_ids": ["1301", "1302"],
      "api_ids": ["1401"],
      "database_ids": ["1501"],
      "accounts": [{"account_id": "1601", "role": "order_admin"}]
    }
  ]
}
```

规则：1–20 个 binding；`sut_id` 不重复；环境/页面/API 属于同一 SUT+环境；页面/API id 去重且保留排序；库/账号属于该 SUT+环境；仓库存在；账号角色可空。校验失败 400 并返回 `details.field`。

详情返回嵌套 `bindings[]`（SUT/环境/仓库/页面/API/库/账号摘要；密码永不返回）；列表返回 `{id,title,status,sut_count,created_at,updated_at}`。

---

## 8. 事件与 SSE

### 8.1 表与信封

`event_channel(channel TEXT PK, last_seq BIGINT NOT NULL DEFAULT 0, updated_at TIMESTAMPTZ NOT NULL DEFAULT now())`；

`event(seq BIGINT NOT NULL, channel TEXT NOT NULL REFERENCES event_channel(channel), type TEXT NOT NULL, envelope JSONB NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY (channel, seq))`；索引 `(channel, created_at)`。

- `seq` 在 Publish 事务内通过 `INSERT INTO event_channel ... ON CONFLICT DO UPDATE SET last_seq=last_seq+1 RETURNING last_seq` 分配，同一频道内严格递增；`(channel, seq)` 是 SSE 游标，不是雪花业务实体 ID。
- 信封（`envelope` JSONB）：

```json
{
  "scope": {"version_id": "1001"},
  "payload": {"title": "...", "status": "initial", "sut_count": 2},
  "actor_id": "9001",
  "request_id": "req_..."
}
```

- `seq`、`channel`、`type`、`created_at` 是 `event` 列；其余进 `envelope`。频道：`system`、`version:{id}`、`user:{id}`；P0 只发布 `system` 与 `version:{id}`。
- P0 事件类型：`auth.login`、`auth.login_failed`、`auth.logout`、`user.created`、`user.updated`、`settings.updated`、`llm.config_created/updated/deleted`、`resource.sut.*`、`resource.environment.*`、`resource.page.*`、`resource.api.*`、`resource.database.*`、`resource.account.*`、`resource.code_repo.*`、`content.uploaded`、`version.created`。
- 事件写入与业务写入不同事务时，允许“业务成功、事件失败”在日志中告警；reconciler 属 P1+，P0 不假装可靠投递。

### 8.2 快照与 SSE

- `GET /api/events?channel=system&after=0&limit=50`：`after` 省略表示取该频道最新 `limit` 条（倒序后正序返回），返回 `{data:[...],"snapshot_cursor":latestSeq,"has_more":bool}`；`after>0` 表示取该游标之后的事件（正序），`has_more` 表示还有更多。
- `GET /api/stream?channel=system`：`Last-Event-ID` 头或 `after` 参数提供游标；有游标时先订阅 Hub、再回放 DB 中该游标之后的事件，最后接实时；无游标时从当前 `last_seq` 之后只订阅实时。心跳注释每 20s；SSE 帧 `id: <seq>`、`event: <type>`、`data: <event JSON>`。
- 游标过旧（`after > last_seq` 或 `after < min(seq)-1`，P0 无清理任务时仅后者理论存在）发送 synthetic `event: stream.reset`（无 id、不持久化、不进 onEvent），客户端应重拉快照；P0 仍定义该控制帧，避免 P7 清理后改协议。
- 401 返回 JSON 错误而非 SSE；SSE 先订阅 Hub 再查 DB，按 `(channel,seq)` 去重，避免丢事件。
- 跨实例 fan-out 属 P7；P0 Hub 为进程内实现，handoff 明确单实例假设。

---

## 9. 内容账本

- `content_object(sha256 TEXT PK, size BIGINT, mime TEXT, relative_path TEXT, created_at)`；`content_ref(id BIGINT PK, object_id, owner_kind, owner_id, role, created_at, deleted_at NULL)`。
- 上传 `POST /api/content`：multipart 字段 `file`；≤10 MiB；计算 SHA-256；MIME 以 `http.DetectContentType` 为准并保留原文件名仅作元数据（不入库）；写临时文件 → fsync/close → 原子 rename 到 `{data_root}/objects/{sha[0:2]}/{sha[2:4]}/{sha}`；文件 0600、目录 0700；`ON CONFLICT (sha256) DO NOTHING`；返回 `{sha256,size,mime,url}`。
- 下载 `GET /api/content/{sha256}`：校验 64 hex；读取时校验文件 sha 与 DB 一致；返回 `Content-Type`、`X-Content-SHA256`、`Cache-Control: private, max-age=31536000, immutable`；不暴露文件系统路径。
- `content_ref` 服务提供 Add/List/Delete（软删除）供 P1+ 使用；P0 不提供公开 ref CRUD API。
- 路径根来自 `paths.data_root`（相对路径按进程 CWD 解析），启动后不可热改；上传前确保目录存在。

---

## 10. 资源与版本绑定规则

### 10.1 资源验证

- SUT：名称 1–100 字符，唯一；简介 ≤2000。
- 环境：名称 1–100；`base_url` 必须 http/https；同一 SUT 下名称唯一；`is_default` 唯一（应用内事务 unset 其它）。
- 页面：名称/URL/route_template 必填；URL 完整 http(s)；route_template 以 `/` 开头，缺省从 URL path 派生；`source ∈ admin|import`（P0 不写 ai）；`status=approved`。
- API：method 枚举；path_template 以 `/` 开头；name 必填；`source ∈ manual|import|openapi|code`（P0 只写 manual/import）；`status=approved`。
- 目标库：name、dsn、username 必填；dsn 以 `postgres://`/`postgresql://` 开头；read_only 默认 true；密码可空。
- 测试账号：username、password 必填（创建）；roles 为字符串数组；同一环境 username 唯一。
- 代码仓：name 唯一；path 必填（本地路径/URL 字符串）；branch 可空；include/exclude 为字符串数组；任意登录用户可创建。
- 页面/API 导入：请求 `{items:[...]}`；逐项校验，重复键跳过；返回 `{created, skipped, items}`；任一非法项整体 400 并指出 index。

### 10.2 版本

- 创建即 `initial`；不触发任何 P1+ 任务/事件以外的副作用。
- 事件 `version.created` 发到 `version:{id}` 与 `system`。
- PATCH 仅允许 `title`、`auto_design_new_requirements`；状态不可由 P0 API 修改。
- 详情读取对已删除/失效资源做容错：被引用资源不可删除（§6.4），因此正常不会悬空。

---

## 11. 前端 IA

### 11.1 路由

| 路由 | 内容 |
|---|---|
| `/login` | 登录（固定默认凭据提示 admin/admin123；登录后设置页改密） |
| `/versions` | 版本列表 + 新建版本 |
| `/versions/{id}?tab=overview\|requirements\|design\|execution\|chain\|issues\|reports\|code` | 版本详情壳；P0 只有 overview 有内容，其余显示 P1+ 占位 |
| `/resources?tab=suts\|pages\|data\|repos` | 资源管理 |
| `/settings?tab=general\|llm\|users\|audit` | 设置中心（admin） |
| `/issues`、`/reports`、`/knowledge`、`/quality`、`/agents` | 全局壳占位，标注后续阶段 |
| `/` | 重定向 `/versions` |

### 11.2 壳与交互

- 左侧深色 Sidebar：工作区（版本/问题/报告/知识/资源）+ 平台（质量/Agent 中心/设置），当前路由高亮；底部显示“开发测试环境 · 受控网络”提示。
- 顶栏：当前页标题/面包屑、SSE 状态点、用户菜单（登出）。
- 登录：品牌区 + 表单 + 明确错误；固定默认凭据提示（admin/admin123）；登录成功进入 `/versions`。
- 版本列表：空状态 + “新建版本”对话框（标题 + 多 SUT binding 向导：选 SUT → 环境 → 页面/API 多选 → 库/账号多选 → 仓库）；提交后跳详情。
- 版本详情：标题/状态/SUT chips/8 Tab；overview 展示绑定卡片（环境、页面/API 数、库/账号、仓库）与后续阶段说明。
- 资源页：按 Tab 管理；SUT 详情内管理环境/页面/API/库/账号；代码仓独立 Tab；表单校验错误内联。
- 设置页：系统设置分组表单（保存单项）、LLM 配置（增删改/测试/刷新模型/手工模型）、用户管理、审计日志。
- 视觉：沿用 `globals.css` 的 CSS 变量与 Tailwind 4；参考 `ui/mock.html` 的深色侧栏/浅色卡片/状态色；不复制 mock 的静态内容。
- 不实现对话/脑图/执行等 P1+ 交互，只在对应 Tab/页面显示“P1 起提供”。

---

## 12. 测试与验收

### 12.1 测试分层

| 层 | 范围 | 命令 |
|---|---|---|
| Go 单测 | domain/application 纯逻辑；雪花/错误/分页/设置类型/校验器/事件帧 | `cd backend; go test ./...` |
| Go 集成 | 迁移幂等/检查、repo、认证、settings、events/SSE、storage、resource、version、审计 | `cd backend; go test -tags=integration ./...`（需 `EZTEST_TEST_DATABASE_URL`） |
| 前端单测 | api-client/sse-client/格式化/资源表单校验/版本向导校验 | `cd frontend; pnpm test` |
| 前端静态 | lint/typecheck | `pnpm lint`、`pnpm typecheck` |
| E2E（P0） | 默认 admin 登录 → 资源 → 版本 → 详情 → 设置热更新 → 自助改密 | `scripts/p0-e2e.ps1`（Playwright，fake LLM） |
| API 验收 | 不依赖浏览器的完整动线 + 事件回放 | `scripts/p0-acceptance.ps1` |

- 集成测试使用独立 schema：`testutil.NewDB(t)` 创建随机 schema、设置 `search_path`、跑迁移、结束 drop；避免包并行冲突。
- Fake LLM：`scripts/fake-llm.cjs`（P0 重写/保留），提供 `GET /models`、`POST /chat/completions`、`GET /health`；E2E/验收使用确定性响应，零真实费用。
- 真实 LLM 验收不在 P0 gated 范围（P1 起需要）。

### 12.2 验收场景

1. **种子/登录**：空库启动 → admin/admin123 登录 → me；错误密码 401；改密后旧密码 401、新密码 200；重复启动不重复建 admin。
2. **设置**：读取默认目录；改 `execution.global_concurrency=5` → 立即读回 5；revision 记录；secret 掩码；未知 key 404。
3. **资源**：创建 SUT/环境/页面/API/库/账号/仓库；重复页面/API 键被拒或导入跳过；任意登录用户可建仓库；普通用户写 SUT 403。
4. **版本**：创建 2 个 SUT 的版本（页面/API scope、库、账号、仓库）→ 详情嵌套正确；非法跨 SUT 页面 400；被版本绑定的 SUT 删除 409。
5. **事件**：创建版本后 `GET /api/events?channel=version:{id}` 有 `version.created`；`after` 游标只返回新事件；SSE 回放收到同 seq；重复执行迁移 schema_migrations 不变。
6. **内容**：上传 → 下载 sha 一致；重复上传去重；路径不存在/非法 sha 404/400。
7. **审计**：登录/设置/资源/版本操作均出现审计记录；普通用户访问 admin API 403。
8. **前端**：Playwright 完成 1–4 的关键动线并截图；设置热更新在 UI 可见；无控制台错误。

### 12.3 证据

- `.tmp/p0-acceptance/acceptance.log`（API 动线输出）
- `.tmp/p0-acceptance/e2e/`（Playwright 报告/截图）
- `.tmp/p0-acceptance/screenshots/*.png`（登录、资源、版本详情、设置）
- handoff 引用命令、结果摘要、截图路径。

---

## 13. 实施决策/偏差（需 handoff 确认）

| 编号 | 决策 | 理由 | 设计依据/偏差 |
|---|---|---|---|
| P0-D1 | 空库种子固定 admin/`admin123`；不新增 env；登录后自助改密；密码策略 8–128 | 严格 4 env 未定义管理员凭据；用户明确裁决 | 设计缺口；固定默认凭据风险记入 handoff |
| P0-D2 | 就地重写 backend/frontend，0.1 保留在 backup 分支 | 0.2 全量重写、完整替代 | D01；目录沿用 Agent.md |
| P0-D3 | LLM 配置用专用表 `llm_config`/`llm_model`；设置中心只承载 UI 分类与通用键；`api_key` 明文入库、API 掩码 | 4 env 无主密钥；设计明确不做密钥托管/信封加密 | HLD:126-127 优先于 architecture:472-476 的类别描述；D11/D51；字段名与 `key_cipher` 有差异 |
| P0-D4 | 雪花 node 存 DB 设置 | 不能新增 `EZTEST_SNOWFLAKE_NODE_ID` | D49 + 0.1 Agent.md §6.7 的适配 |
| P0-D5 | CORS origin 由 `EZTEST_WEB_PORT` 派生 | 无第 5 个 env | D49/D50 |
| P0-D6 | `event_channel` + 每频道 `seq` 作为游标 | 事件顺序号不是业务实体；HLD 列出 event_channel | HLD:128 的落地 |
| P0-D7 | P0 冻结设置键目录/默认值 | 设计只给类别与部分 OD 默认 | §10.2 + OD-4/8/9；需用户批准 |
| P0-D8 | 执行器 URL 由 `EZTEST_EXECUTOR_PORT` 派生并 seed 到设置 | 无第 5 个 env | §10.1 |
| P0-D9 | P0 版本只写 `initial`，不改状态 | 状态迁移属 P1/P4 | D05/D06 |
| P0-D10 | 资源删除做版本绑定守卫 | 防止悬空引用 | 架构 §6.1/§8.2 的工程约束 |
| P0-D11 | LLM P0 只做配置/模型目录/连通性 | phase-00 范围 | phase-00 §范围；gateway/streaming P1 |
| P0-D12 | `NEXT_PUBLIC_API_BASE_URL` 为构建期前端配置，不算 EzTest env | 浏览器需要 API origin | D49 的边界说明 |
| P0-D13 | 代码仓任意登录用户完整 CRUD，其他资源写操作仅 admin | D09 + architecture:230 均明确“任意登录用户可创建代码仓”；phase-00:36/HLD:427 是概要性冲突 | 用户已裁决按 D09/architecture；handoff 记录冲突 |
| P0-D14 | 身份表 `app_user`/`auth_token`，Bearer 不透明 token（SHA-256 存储，7 天固定 TTL） | 0.2 身份 schema 缺失；0.1 identity 可移植 | 用户已裁决 7d；HLD:16 “移植” |
| P0-D15 | 页面/API 导入为 JSON `{items:[...]}`；P0 不启用 AI 提议 | 设计只写“导入”，未定格式 | phase-00:22；P0 实现决策 |
| P0-D16 | 所有登录用户可读所有版本（单团队，无多用户协作） | 0.2 明确非目标多用户协作；版本写为登录用户 | architecture §2.2/D09；P0 实现决策 |
| P0-D17 | 版本状态/权限/命名以 version-V0.2 设计为准；Agent.md 只用于目录/导入/命名规约，不用于 0.2 控制面、ID、seed | 两份文档权威范围不同 | P0 实现决策 |
| P0-D18 | 新增 `POST /api/auth/password` 自助改密（旧密码 + 新密码），审计 `auth.password_changed` | 固定默认凭据必须允许用户自行修改 | 用户裁决的配套实现 |
| P0-D19 | 设置目录按 spec §5.2 全量冻结；未定值采用推荐（每 SUT 并发 2、跨系统 1、digest 保留 5、context 64000/8000/0.7/12、agent 12/300/600/4、evidence 5MiB、case 900s、audit 90d） | 设计只给类别/部分 OD 默认 | 用户已裁决“P0 冻结完整目录” |

以上决策不修改 `docs/version-V0.2` 设计文档；在 `phase-00-handoff.md` 列明。用户已对 6 个阻塞缺口作出裁决（2026-10-31）；其余为 P0 实现决策。

---

## 14. 契约冻结清单

P0 结束必须写并标记 frozen：

- `contracts/openapi.yaml`：P0 全部端点/请求/响应/错误码。
- `contracts/events.md`：事件类型、channel、envelope、游标/回放语义、单实例 Hub 限制。
- `contracts/db-schema.md`：P0 表/列/约束/索引/ID 约定。
- `contracts/migrations-order.md`：迁移顺序、ownership、checksum 规则。
- `contracts/settings.md`：4 项 env、设置键目录/默认/热加载/重启标记、secret 掩码。
- `contracts/README.md` 保持机制说明；`task-types.md`/`agent-profiles-tools.md` 不在 P0 创建（属 P1），handoff 说明。

---

## 15. 风险

1. 资源模型/字段是 P1–P8 的契约入口，P0 冻结后修改成本高；先按设计冻结，后续阶段只能增量。
2. 固定默认凭据/明文 API key/无登录限流属于开发测试信任模型，不能带入生产假设；首启必须改密。
3. 事件只做 UI 投影，P0 单实例 Hub；P7 多副本前不能承诺跨实例实时。
4. 前端从 0.1 全量重写，P0 只壳+资源+版本；P1 起需在稳定的 api-client/sse-client 上扩展。
5. 0.1 代码删除后仅存于 backup 分支；P1+ 需要参考时必须显式切分支阅读，禁止把旧控制面假设带回。
