# EzTest 0.2 P0 启动配置与设置中心契约

> 状态：`frozen: P0`（P0 阶段末冻结；后续阶段只能增量）。
> 来源：`docs/superpowers/specs/2026-10-31-eztest-v02-p0-platform-foundation-design.md` §3、§5。
> 表结构：`contracts/db-schema.md` 的 `system_setting` / `system_setting_revision`；
> API：`contracts/openapi.yaml` 的 `/api/admin/settings*`。

## 1. 启动 env（严格 4 项）

EzTest 运行时**只有以下 4 个**启动环境变量，**没有第 5 个 EzTest env**：

| 变量 | 必填 | 默认 | 用途 |
|---|---|---|---|
| `EZTEST_DATABASE_URL` | 是 | — | PostgreSQL URL（pgx）；scheme 必须是 `postgres` 或 `postgresql` |
| `EZTEST_API_PORT` | 否 | `13333` | API 监听端口；服务绑定 `:port` |
| `EZTEST_WEB_PORT` | 否 | `6868` | Web 端口；用于派生 CORS origin 与部分设置默认值 |
| `EZTEST_EXECUTOR_PORT` | 否 | `13336` | 派生默认设置 `execution.executor_url=http://127.0.0.1:{port}` |

校验规则：

1. 端口必须是整数且在 `1–65535`。
2. `EZTEST_DATABASE_URL` 必填，URL 解析失败或 scheme 非 `postgres/postgresql` → 启动失败。
3. 缺失/非法时输出明确错误并退出；**不猜测、不回退到隐藏默认值**。
4. 不存在 `EZTEST_WEB_ORIGIN`；CORS 允许的来源由 `EZTEST_WEB_PORT` 派生：
   `http://localhost:{EZTEST_WEB_PORT}` 与 `http://127.0.0.1:{EZTEST_WEB_PORT}`（不带 credentials）。

### 1.1 构建期前端变量（不属于 EzTest 运行时 env）

`NEXT_PUBLIC_API_BASE_URL`（默认 `http://localhost:13333`）仅用于浏览器定位 API origin，
是前端构建期变量，**不算 EzTest 启动配置**，不参与后端 4 项校验；部署换端口时需重建 Web。

### 1.2 启动顺序

```text
读 env（4 项校验）
  → 连 DB
  → 迁移（advisory lock）
  → seed 设置默认值
  → 读 runtime.snowflake_node_id 构造雪花
  → 构造依赖/服务
  → seed 默认管理员（空 user 表时）
  → 启动 HTTP
```

## 2. 设置中心存储

```sql
system_setting(
  key TEXT PRIMARY KEY,
  value_json JSONB NOT NULL,
  category TEXT NOT NULL,
  type TEXT NOT NULL CHECK (type IN ('string','number','boolean','json')),
  is_secret BOOLEAN NOT NULL DEFAULT false,
  restart_required BOOLEAN NOT NULL DEFAULT false,
  description TEXT NOT NULL DEFAULT '',
  version INT NOT NULL DEFAULT 1,
  updated_by BIGINT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
)

system_setting_revision(
  id BIGINT PRIMARY KEY,            -- 雪花
  setting_key TEXT NOT NULL REFERENCES system_setting(key) ON DELETE CASCADE,
  old_value JSONB NULL,
  new_value JSONB NOT NULL,
  actor_id BIGINT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
)
```

- `type ∈ string|number|boolean|json`；secret 用 `is_secret` 标记，不引入独立类型。
- seed 幂等：`INSERT ... ON CONFLICT (key) DO NOTHING`；**不覆盖已存在值**。
- 更新顺序：校验 key 存在与类型/区间 → 写 revision → 更新 setting（`version+1`）→ 失效/重载读缓存
  → 发 `settings.updated` 事件（`system` 频道）→ 写审计。
- `restart_required` 只做 UI 提示，不阻断更新。
- 未知 key → 404 `not_found`；类型/区间错误 → 400 `invalid_argument`（`details.field` 指向 key）。
- `GET /api/admin/settings` 返回完整目录（P0 为 44 项）；PATCH 单项返回更新后的设置项。

### 2.1 secret 语义

- GET 返回 secret 项时：`value=null`、`is_secret=true`。
- revision 查询中 secret 的 `old_value`/`new_value` 均为 `null`。
- PATCH secret 项：`value` 省略或 `null` 表示**不修改**；`""` 表示**清空**。
- P0 的 44 个键**没有 secret 项**；LLM API key 存在 `llm_config.api_key`（独立表），
  响应只返回 `api_key_set`，不进入设置中心。secret 机制为后续阶段保留并已冻结语义。

### 2.2 热加载与重启

- 设置 Service 持有读缓存；每次更新成功后立即 reload，后续读取立即看到新值。
- 业务代码必须通过设置 Service 读取（不得直接读 env/文件），因此绝大多数键**即时生效**。
- `restart_required=true` 的键：值会立即写入并热加载，但其**实际运行时效果**需要重启进程
  （如雪花 node、数据根目录）。UI 只显示提示，不阻止保存。
- 相对路径按进程 CWD 解析；`paths.data_root` 启动后不可热改（重启生效）。

## 3. P0 设置键目录（44 项，完整冻结）

`secret` 列全部为 `false`；重启列中“是”对应 `restart_required=true`。
未给出区间的 `number` 为整数类型，具体上限由后续阶段按合同增量（P0 不额外发明约束）。

| # | key | type | category | default | 取值/说明 | restart |
|---|---|---|---|---|---|---|
| 1 | `runtime.snowflake_node_id` | number | runtime | `1` | 整数 0–1023；雪花 node | 是 |
| 2 | `paths.data_root` | string | paths | `./.data` | 数据根目录（CWD 相对） | 是 |
| 3 | `paths.attachment_dir` | string | paths | `./.data/attachments` | 附件目录 | 否 |
| 4 | `paths.evidence_dir` | string | paths | `./.data/evidence` | 证据目录 | 否 |
| 5 | `paths.report_dir` | string | paths | `./.data/reports` | 报告目录 | 否 |
| 6 | `paths.export_dir` | string | paths | `./.data/exports` | 导出目录 | 否 |
| 7 | `execution.global_concurrency` | number | execution | `3` | 整数 1–32 | 否 |
| 8 | `execution.per_sut_concurrency` | number | execution | `2` | 整数 1–16 | 否 |
| 9 | `execution.cross_system_concurrency` | number | execution | `1` | 整数 1–8 | 否 |
| 10 | `execution.executor_url` | string | execution | `http://127.0.0.1:{EZTEST_EXECUTOR_PORT}` | 默认由端口派生（默认端口 13336） | 否 |
| 11 | `execution.evidence_max_bytes` | number | execution | `5242880` | 整数；5 MiB | 否 |
| 12 | `execution.case_timeout_seconds` | number | execution | `900` | 整数；15 分钟 | 否 |
| 13 | `agent.max_turns` | number | agent | `12` | 整数 | 否 |
| 14 | `agent.run_soft_timeout_seconds` | number | agent | `300` | 整数 | 否 |
| 15 | `agent.run_hard_timeout_seconds` | number | agent | `600` | 整数 | 否 |
| 16 | `agent.max_concurrent_runs` | number | agent | `4` | 整数 | 否 |
| 17 | `agent.tool_result_max_bytes` | number | agent | `32768` | 整数；32 KiB | 否 |
| 18 | `llm.request_timeout_seconds` | number | llm | `60` | 整数 | 否 |
| 19 | `llm.max_retries` | number | llm | `2` | 整数 | 否 |
| 20 | `context.window_tokens` | number | context | `64000` | 整数 | 否 |
| 21 | `context.output_reserve_tokens` | number | context | `8000` | 整数 | 否 |
| 22 | `context.compact_threshold` | number | context | `0.7` | 小数 0.5–0.9 | 否 |
| 23 | `context.keep_recent_messages` | number | context | `12` | 整数 | 否 |
| 24 | `team_room.digest_every_n_posts` | number | team_room | `20` | 整数 | 否 |
| 25 | `team_room.digest_interval_minutes` | number | team_room | `10` | 整数 | 否 |
| 26 | `team_room.digest_keep_versions` | number | team_room | `5` | 整数 | 否 |
| 27 | `retention.temp_hours` | number | retention | `24` | 整数 | 否 |
| 28 | `retention.replaced_export_days` | number | retention | `7` | 整数 | 否 |
| 29 | `retention.unreferenced_content_days` | number | retention | `30` | 整数 | 否 |
| 30 | `retention.gc_grace_hours` | number | retention | `24` | 整数 | 否 |
| 31 | `retention.gc_dry_run` | boolean | retention | `true` | — | 否 |
| 32 | `report.template_version` | string | report | `"1"` | 字符串版本号 | 否 |
| 33 | `report.render_pdf` | boolean | report | `true` | — | 否 |
| 34 | `report.section_summary` | boolean | report | `true` | — | 否 |
| 35 | `report.section_results` | boolean | report | `true` | — | 否 |
| 36 | `report.section_issues` | boolean | report | `true` | — | 否 |
| 37 | `report.section_coverage` | boolean | report | `true` | — | 否 |
| 38 | `report.section_evidence` | boolean | report | `true` | — | 否 |
| 39 | `knowledge.injection_budget_tokens` | number | knowledge | `4000` | 整数 | 否 |
| 40 | `knowledge.review_required` | boolean | knowledge | `true` | — | 否 |
| 41 | `security.audit_retention_days` | number | security | `90` | 整数 | 否 |
| 42 | `security.mask_secrets` | boolean | security | `true` | — | 否 |
| 43 | `feature.auto_gap_design` | boolean | feature | `true` | — | 否 |
| 44 | `feature.experimental` | boolean | feature | `false` | — | 否 |

类别数量：runtime 1、paths 5、execution 6、agent 5、llm 2、context 4、team_room 3、
retention 5、report 7、knowledge 2、security 2、feature 2 = **44**。

默认值来源：规格 §5.2（OD-4/OD-8/OD-9/架构 §9–§12）；未在设计中逐项给出的键由 P0 冻结，
后续阶段只能通过契约变更修改默认值或新增键。

## 4. 派生默认值的细节

- `execution.executor_url` 的默认值只**在首次 seed 时**由 `EZTEST_EXECUTOR_PORT` 派生：
  `http://127.0.0.1:{EZTEST_EXECUTOR_PORT}`。
- 由于 seed 使用 `ON CONFLICT DO NOTHING`，数据库已有该键时不会因 env 改变而覆盖；
  需要修改时通过设置中心 PATCH。
- 其他键默认值固定，不读取 env。

## 4a. P1 使用说明（`draft: P1`）

- P1 **不新增** env、设置键或类别；44 键目录、类型/默认/热加载/secret 语义保持 P0。
- P1 消费的键：
  - `agent.max_turns`、`agent.run_soft_timeout_seconds`、`agent.run_hard_timeout_seconds`、`agent.max_concurrent_runs`、`agent.tool_result_max_bytes`；
  - `context.output_reserve_tokens`、`context.compact_threshold`、`context.keep_recent_messages`；`context.window_tokens` **仅作模型元数据缺失时的回退**；
  - `team_room.digest_every_n_posts`、`team_room.digest_interval_minutes`、`team_room.digest_keep_versions`；
  - `llm.request_timeout_seconds`、`llm.max_retries`、`paths.data_root`、`paths.attachment_dir`。
- 上下文窗口/最大输出优先来自 LLM 网关注册的 `llm_model.context_window/max_output`（`/models` refresh）；仅缺失时回退 `context.window_tokens/output_reserve_tokens`。
- 运行时读取器新增 `Float(ctx,key)`（只读；number 整数拓宽，字符串报错），用于 `context.compact_threshold`；不改变设置 API/存储语义。
- P1 的 LLM 验收凭据（`.env` 小写 `llm_*`）**不属于**系统设置/env 契约：仅验收脚本读取，经 admin API 写入 `llm_config`。

## 5. 冻结与变更控制

1. P0 冻结：4 项 env、44 键目录、类型/默认值/类别/restart 标记、secret 语义、更新流水线。
2. 后续阶段只能**增量**：新增键必须同时更新本文件、seed 目录、测试与 handoff；不得改已冻结键的语义。
3. 新增 env 属于破坏性变更：本阶段明确规定没有第 5 个 EzTest 启动 env，任何新配置必须进设置中心。
4. YAML/JSON 类型严格：`number` 不接受字符串数字；`boolean` 不接受 `"true"` 字符串；`report.template_version` 是字符串 `"1"`。
5. 所有设置更新都必须写 revision（secret 值置 null）并留下审计；不允许绕过服务直接改表。
