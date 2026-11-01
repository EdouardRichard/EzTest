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
- `GET /api/admin/settings` 返回完整目录（P0 为 44 项；P3 起 49 项）。PATCH 单项返回更新后的设置项。

### 2.1 secret 语义

- GET 返回 secret 项时：`value=null`、`is_secret=true`。
- revision 查询中 secret 的 `old_value`/`new_value` 均为 `null`。
- PATCH secret 项：`value` 省略或 `null` 表示**不修改**；`""` 表示**清空**。
- P0 的 44 个键**没有 secret 项**；P3 新增的 `execution.executor_token` 是**首个 secret 键**（`is_secret=true`）；LLM API key 仍存在 `llm_config.api_key`（独立表），响应只返回 `api_key_set`，不进入设置中心。secret 机制为后续阶段保留并已冻结语义。

### 2.2 热加载与重启

- 设置 Service 持有读缓存；每次更新成功后立即 reload，后续读取立即看到新值。
- 业务代码必须通过设置 Service 读取（不得直接读 env/文件），因此绝大多数键**即时生效**。
- `restart_required=true` 的键：值会立即写入并热加载，但其**实际运行时效果**需要重启进程
  （如雪花 node、数据根目录）。UI 只显示提示，不阻止保存。
- 相对路径按进程 CWD 解析；`paths.data_root` 启动后不可热改（重启生效）。

## 3. 设置键目录（P0 44 项 + P3 5 项 + P4 4 项 + P5 16 项 + P6 3 项 = 72 项）

`secret` 列：P0 44 项全部为 `false`；P3 的 `execution.executor_token` 为 `true`（表中以 **secret** 标注），其余 4 项为 `false`；P4 4 个 `chain.*` 键全部为 `false`；P5 16 个 `codeintel.*`/`visual.*` 键全部为 `false`；P6 3 个 `search.*`/`todos.*`/`quality.*` 键全部为 `false`。
重启列中“是”对应 `restart_required=true`。
未给出区间的 `number` 为整数类型，具体上限由后续阶段按合同增量（P0 不额外发明约束；P3 键的区间见本表）。

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
| 45 | `execution.executor_token` | string | execution | `eztest-dev-executor-token` | **secret=true**；API→executor Bearer；执行器进程用同名 env 接收；UI 掩码；default 已知（dev） | 否 |
| 46 | `execution.executor_heartbeat_seconds` | number | execution | `10` | 整数 1–60；API 轮询 healthz 间隔 | 否 |
| 47 | `execution.executor_offline_seconds` | number | execution | `30` | 整数 5–300；心跳超时判 offline（建议 ≥2×heartbeat） | 否 |
| 48 | `execution.executor_context_ttl_seconds` | number | execution | `300` | 整数 30–3600；非 running case 的 context 清理 TTL | 否 |
| 49 | `execution.human_request_timeout_seconds` | number | execution | `1800` | 整数 60–86400；login_takeover 过期 | 否 |
| 50 | `chain.max_nodes` | number | chain | `300` | 整数 1–1000；链路节点上限；越界回退默认并审计 | 否 |
| 51 | `chain.max_edges` | number | chain | `600` | 整数 1–2000；链路边上限；越界回退默认并审计 | 否 |
| 52 | `chain.max_requirements` | number | chain | `100` | 整数 1–500；参与链路的版本需求上限；越界回退默认并审计 | 否 |
| 53 | `chain.replay_event_limit` | number | chain | `500` | 整数 1–2000；历史回看事件上限；越界回退默认并审计 | 否 |
| 54 | `codeintel.max_index_files` | number | codeintel | `5000` | 整数 1–50000；T0 索引文件上限，超出 `truncated` | 否 |
| 55 | `codeintel.max_changed_files` | number | codeintel | `20` | 整数 1–200；T1 分析文件上限 | 否 |
| 56 | `codeintel.max_file_bytes` | number | codeintel | `65536` | 整数 1024–1048576；T1 可读文件上限 | 否 |
| 57 | `codeintel.max_diff_bytes` | number | codeintel | `262144` | 整数 4096–1048576；按需 patch 上限 | 否 |
| 58 | `codeintel.max_excerpt_lines` | number | codeintel | `40` | 整数 1–200；事实证据摘录上限 | 否 |
| 59 | `codeintel.analysis_timeout_seconds` | number | codeintel | `180` | 整数 10–1800；分析硬超时 | 否 |
| 60 | `codeintel.max_concurrent_analyses` | number | codeintel | `1` | 整数 1–4；worker 并发 | 否 |
| 61 | `codeintel.prompt_version` | string | codeintel | `"p5.1"` | 字符串；变化会产生新事实版本 | 否 |
| 62 | `codeintel.exclude_globs` | json | codeintel | `["**/.git/**","**/node_modules/**","**/dist/**","**/build/**","**/vendor/**"]` | 路径排除，按 slash 归一化 | 否 |
| 63 | `visual.default_threshold` | number | visual | `0.005` | 小数 0–0.1；新配置默认 | 否 |
| 64 | `visual.baseline_max_bytes` | number | visual | `5242880` | 整数 65536–20971520；基线/actual 上限（= executor `visual.compare` body 上限） | 否 |
| 65 | `visual.default_viewport_width` | number | visual | `1280` | 整数 1–10000 | 否 |
| 66 | `visual.default_viewport_height` | number | visual | `720` | 整数 1–10000 | 否 |
| 67 | `visual.default_device_scale_factor` | number | visual | `1.0` | 小数 0.5–4 | 否 |
| 68 | `visual.compare_timeout_seconds` | number | visual | `60` | 整数 5–300；executor 动作超时 | 否 |
| 69 | `visual.wait_timeout_seconds` | number | visual | `15` | 整数 1–120；network/font/稳定等待上限 | 否 |
| 70 | `search.max_results_per_type` | number | search | `5` | 整数 1–20；搜索每类型上限默认值（查询参数可覆盖） | 否 |
| 71 | `todos.max_items` | number | workspace | `50` | 整数 1–200；待办默认上限（查询参数可覆盖） | 否 |
| 72 | `quality.eval_baseline_regression` | number | quality | `0.05` | 小数 0–1；基线回归标记容差（报告轨，不阻断） | 否 |

类别数量：runtime 1、paths 5、execution 11、chain 4、agent 5、llm 2、context 4、team_room 3、
retention 5、report 7、knowledge 2、security 2、feature 2、codeintel 9、visual 7、search 1、
workspace 1、quality 1 = **72**（P0 44 + P3 5 + P4 4 + P5 16 + P6 3）。

默认值来源：规格 §5.2（OD-4/OD-8/OD-9/架构 §9–§12）；未在设计中逐项给出的键由 P0 冻结，
后续阶段只能通过契约变更修改默认值或新增键；P3 新增 5 键按 spec §11.2 / P3-D-19 冻结。

## 4. 派生默认值的细节

- `execution.executor_url` 的默认值只**在首次 seed 时**由 `EZTEST_EXECUTOR_PORT` 派生：
  `http://127.0.0.1:{EZTEST_EXECUTOR_PORT}`。
- 由于 seed 使用 `ON CONFLICT DO NOTHING`，数据库已有该键时不会因 env 改变而覆盖；
  需要修改时通过设置中心 PATCH。
- 其他键默认值固定，不读取 env。

## 4a. P1 使用说明（`frozen: P1`，2026-10-31）

- P1 **不新增** env、设置键或类别；44 键目录、类型/默认/热加载/secret 语义保持 P0。
- P1 消费的键：
  - `agent.max_turns`、`agent.run_soft_timeout_seconds`、`agent.run_hard_timeout_seconds`、`agent.max_concurrent_runs`、`agent.tool_result_max_bytes`；
  - `context.output_reserve_tokens`、`context.compact_threshold`、`context.keep_recent_messages`；`context.window_tokens` **仅作模型元数据缺失时的回退**；
  - `team_room.digest_every_n_posts`、`team_room.digest_interval_minutes`、`team_room.digest_keep_versions`；
  - `llm.request_timeout_seconds`、`llm.max_retries`、`paths.data_root`、`paths.attachment_dir`。
- 上下文窗口/最大输出优先来自 LLM 网关注册的 `llm_model.context_window/max_output`（`/models` refresh）；仅缺失时回退 `context.window_tokens/output_reserve_tokens`。
- 运行时读取器新增 `Float(ctx,key)`（只读；number 整数拓宽，字符串报错），用于 `context.compact_threshold`；不改变设置 API/存储语义。
- P1 的 LLM 验收凭据（`.env` 小写 `llm_*`）**不属于**系统设置/env 契约：仅验收脚本读取，经 admin API 写入 `llm_config`。

## 4b. P2 使用说明（`frozen: P2`，2026-10-31）

- P2 **不新增** env、设置键或类别；4 项 env、44 键目录保持 P0，P1 语义不变。
- P2 消费的既有键：`agent.max_turns`、`agent.run_soft_timeout_seconds`、`agent.run_hard_timeout_seconds`、`agent.max_concurrent_runs`、`agent.tool_result_max_bytes`、`context.*`、`team_room.*`、`llm.request_timeout_seconds`、`llm.max_retries`、`paths.data_root`。
- `auto_design_new_requirements` 是 `version` 列，不是设置键；P2 不把它加入设置中心。
- 设计 profile budgets 存在 `agent_profile.budgets` JSON（启动 seed），不新增设置键。
- 导出字节写入 P0 内容账本（`content_object`/`content_ref`）；P2 不写 `paths.export_dir`（Agent.md §6.8.3 例外见 spec P2-D-29）。
- P2 的 LLM 验收凭据仍只来自 `.env` 小写 `llm_*`（验收脚本读取，经 admin API 写入 `llm_config`），不属于 EzTest 启动 env。

## 4c. P3 使用说明（`frozen: P3`，2026-11-01）

- P3 **不新增** EzTest 启动 env：仍是严格 4 项。执行器 token 放 DB 设置 `execution.executor_token`（**首个 secret 键**，`is_secret=true`）：`GET /api/admin/settings` 返回 `value=null`；PATCH `""` 清空、省略/null 不改。执行器**进程自身**用环境变量 `EZTEST_EXECUTOR_TOKEN` 接收同一 token（不是 EzTest API 启动 env）；`scripts/dev-executor.ps1` 从 DB 读取当前值后注入。
- P3 新增 5 键（44→49；execution 类 6→11）：
  - `execution.executor_token`：API→executor `Authorization: Bearer`；不得写入日志/事件/证据；对外部署必须修改默认值。
  - `execution.executor_heartbeat_seconds`：API 轮询执行器 `GET /healthz` 间隔（pull 注册，P3-D-01）。
  - `execution.executor_offline_seconds`：`now-last_heartbeat` 超过该值判 `offline`（reconciler 处理 running case_run）。
  - `execution.executor_context_ttl_seconds`：非 running case 的执行器 context 清理 TTL。
  - `execution.human_request_timeout_seconds`：`login_takeover` 人机请求过期时间（默认 1800s）。
- P3 继续消费已冻结的执行键：`execution.global_concurrency=3`、`execution.per_sut_concurrency=2`、`execution.cross_system_concurrency=1`、`execution.executor_url`、`execution.evidence_max_bytes=5242880`、`execution.case_timeout_seconds=900`。`execution.wait` 单次 1–300s，累计计入 `case_timeout_seconds`。
- `paths.evidence_dir` **P3 不使用**（P3-D-20 例外，与 P2 `paths.export_dir` 同源）：证据字节写入 P0 内容账本（`content_object`/`content_ref`），`evidence` 行只存 `content_ref_id/sha256/size/mime/kind`；下载走 P3 鉴权端点。`paths.data_root` 不变。
- `auto_design_new_requirements` 仍是 `version` 列，不是设置键；P3 不改。
- P3 的 LLM 验收凭据仍只来自 `.env` 小写 `llm_*`（验收脚本读取，经 admin API 写入 `llm_config`），不属于 EzTest 启动 env。

## 4d. P4 使用说明（`frozen: P4`，2026-11-01）

- P4 **不新增** EzTest 启动 env：仍是严格 4 项（`.env` 只有 `EZTEST_DATABASE_URL`/`EZTEST_API_PORT`/`EZTEST_WEB_PORT`/`EZTEST_EXECUTOR_PORT`）。新增配置一律进设置中心。
- P4 新增 4 键（49→53；新增 `chain` 类别）：
  - `chain.max_nodes=300`（1..1000）：链路节点上限；超限置 `truncated=true`、`truncation.reason="node_budget"`，按 spec §8.3 顺序丢弃（最后才丢 requirement，`focus_node` 及其祖先永不丢）。
  - `chain.max_edges=600`（1..2000）：链路边上限；超限 `truncation.reason="edge_budget"`。
  - `chain.max_requirements=100`（1..500）：参与链路的根需求上限（按 `updated_at DESC, id DESC`）；超限 `truncation.reason="requirement_budget"`。
  - `chain.replay_event_limit=500`（1..2000）：历史回看（按波次/事件区间）读取 `GET /api/events` 的事件上限。
- 4 键均非 secret、热加载；越界/非法值回退默认并写审计（不阻断请求）；P4 不承诺任意时点 as-of（两档回放见 spec §8.4）。
- 既有 `report.*` 键在 P4 的消费方式：
  - `report.template_version` 固定 `"1"`（P4 内置 HTML 模板，不可用户编辑）；
  - `report.section_summary/results/issues/coverage/evidence` 在 P4 HTML 渲染中生效（false 时不渲染对应章节）；
  - `report.render_pdf=true` **保留但 P4 不消费**（PDF 留 P7；`pdf_ref` 恒 NULL、`pdf_available=false`，不调度 `report.render_pdf`）；
  - `paths.report_dir` **P4 不使用**（与 `paths.export_dir`/`paths.evidence_dir` 例外一致）：报告 HTML 只写 P0 内容账本（`content_ref` owner_kind=`report_revision`、role=`report_html`），下载走鉴权端点，不落 `report_dir`。
- `paths.data_root` 不变；P4 新增 `delivery`/`chain` 不引入独立 env 或路径设置。
- P4 的 LLM 验收凭据仍只来自 `.env` 小写 `llm_*`（验收脚本读取，经 admin API 写入 `llm_config`），不属于 EzTest 启动 env。

## 4e. P5 使用说明（`frozen: P5`，2026-11-01）

- P5 **不新增** EzTest 启动 env：仍是严格 4 项（`.env` 只有 `EZTEST_DATABASE_URL`/`EZTEST_API_PORT`/`EZTEST_WEB_PORT`/`EZTEST_EXECUTOR_PORT`）。代码智能/视觉配置一律进设置中心；**不引入第 5 个 EzTest env**。
- P5 新增 16 键（53→69；新增 `codeintel` 9 键 + `visual` 7 键两个类别；全部非 secret、`restart_required=false`、热加载）：
  - `codeintel.max_index_files=5000`（1..50000）：T0 索引文件上限；超出 `code_index_run.truncated=true` 并记录 reason，不失败。
  - `codeintel.max_changed_files=20`（1..200）：T1 分析文件上限。
  - `codeintel.max_file_bytes=65536`（1024..1048576）：T1 可读文件上限。
  - `codeintel.max_diff_bytes=262144`（4096..1048576）：按需 patch 上限。
  - `codeintel.max_excerpt_lines=40`（1..200）：事实证据摘录上限。
  - `codeintel.analysis_timeout_seconds=180`（10..1800）：分析硬超时。
  - `codeintel.max_concurrent_analyses=1`（1..4）：codeintel worker 并发。
  - `codeintel.prompt_version="p5.1"`：变化会产生新事实版本（`code_fact.version+1`）。
  - `codeintel.exclude_globs`（json，默认 `["**/.git/**","**/node_modules/**","**/dist/**","**/build/**","**/vendor/**"]`）：路径排除，按 slash 归一化；非法 glob 回退默认并写审计（不阻断请求）。
  - `visual.default_threshold=0.005`（0..0.1）：新视觉配置默认阈值。
  - `visual.baseline_max_bytes=5242880`（65536..20971520）：基线/actual 字节上限；同时作为 executor `visual.compare` 的 action body 上限（其他 action 仍 1 MiB）。
  - `visual.default_viewport_width=1280`/`visual.default_viewport_height=720`（1..10000）：新配置默认视口。
  - `visual.default_device_scale_factor=1.0`（0.5..4）：新配置默认 DPR。
  - `visual.compare_timeout_seconds=60`（5..300）：executor `visual.compare` 动作超时。
  - `visual.wait_timeout_seconds=15`（1..120）：network idle/font/稳定等待上限。
- **代码常量硬上限**（来自 0.1 `limits.go`，设置只能降低不能突破）：`MaxEvidencePerFact=8`、`MaxFactsPerFile=200`、`MaxEdgesPerFile=200`、`MaxFactKeyChars=512`、`MaxReasonChars=2000`、`MaxPathChars=4096`、`MaxChangedFilesStored=2000`、`MaxTreeEntriesPerPage=200`、`MaxReadLines=400`、`MaxRequirementsInPrompt=100`、`MaxAnalysisErrors=20`、`MaxFactVersionsSnapshot=500`、`MaxUnmappedEntries=200`。
- **视觉常量**：threshold 0..0.1；viewport ≤10000；DPR 0.5..4；selector ≤500 runes；mismatch boxes ≤50；region 归一化。`visual.baseline_max_bytes` 同时限制基线存储与 executor body；`visual.compare_timeout_seconds`/`visual.wait_timeout_seconds` 分别约束 action 超时与等待策略。
- 消费方：codeintel worker 读 `codeintel.*`；visual 服务与 executor 调用读 `visual.*`；均通过设置 Service 读取（不直接读 env/文件）。所有设置更新必须写 revision（secret 值置 null）与审计。

## 4f. P6 使用说明（`frozen: P6`，2026-11-01）

- P6 **不新增** EzTest 启动 env：仍是严格 4 项（`.env` 只有 `EZTEST_DATABASE_URL`/`EZTEST_API_PORT`/`EZTEST_WEB_PORT`/`EZTEST_EXECUTOR_PORT`）。新增配置一律进设置中心，不引入第 5 个 EzTest env。
- P6 新增 3 键（69→72；新增 `search`/`workspace`/`quality` 三个类别；全部非 secret、`restart_required=false`、热加载）：
  - `search.max_results_per_type=5`（1..20）：搜索每类型上限默认值；请求 `limit` 可覆盖，总上限仍 5×20=100。
  - `todos.max_items=50`（1..200）：待办 items 默认上限；请求 `limit` 可覆盖；`counts` 始终为 limit 前计数。
  - `quality.eval_baseline_regression=0.05`（0..1）：真实 LLM 评测的基线回归标记容差；`regressed=true` 只作为质量观察，**不阻断**阶段验收。
- 3 键均非 secret，`GET /api/admin/settings` 返回明文值；热加载（读缓存失效），PATCH 后下一次请求生效；所有更新写 revision + 审计，不绕过服务直接改表。
- 消费方：search/workspace/quality application 通过设置 Service 读取（不直接读 env/文件）；`quality.eval_baseline_regression` 只用于 `detail.regressed` 标记，阈值本身仍来自评测语料/基线文件。
- P6 不新增事件、task type/profile/tool/executor 协议；不修改 P0–P5 已冻结键的语义/默认/类别/secret 标记。

## 5. 冻结与变更控制

1. P0 冻结：4 项 env、44 键目录、类型/默认值/类别/restart 标记、secret 语义、更新流水线。
2. 后续阶段只能**增量**：新增键必须同时更新本文件、seed 目录、测试与 handoff；不得改已冻结键的语义。
3. 新增 env 属于破坏性变更：本阶段明确规定没有第 5 个 EzTest 启动 env，任何新配置必须进设置中心。
4. YAML/JSON 类型严格：`number` 不接受字符串数字；`boolean` 不接受 `"true"` 字符串；`report.template_version` 是字符串 `"1"`。
5. 所有设置更新都必须写 revision（secret 值置 null）并留下审计；不允许绕过服务直接改表。
