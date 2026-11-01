# EzTest 0.2 P0 数据库 Schema 契约

> 状态：`frozen: P0`（P0 阶段末冻结；后续阶段只能增量）。
> 来源：`backend/internal/db/migrations/*.sql`（8 个迁移，P0 实际落地），
> 规格 `docs/superpowers/specs/2026-10-31-eztest-v02-p0-platform-foundation-design.md` §6。
> 迁移顺序/checksum 规则见 `contracts/migrations-order.md`；事件语义见 `contracts/events.md`。

## 1. 数据库与通用约定

- PostgreSQL（pgx v5）；开发库 `eztest_v02`，集成测试库 `eztest_v02_test`（与 0.1 的 `eztest`/`eztest_test` 隔离）。
- 表名/列名蛇形小写；数据库层列名用 `type`（PostgreSQL 非保留字）。
- 所有业务实体主键：`BIGINT` **雪花 ID，应用层生成**；不使用 `SERIAL`/`GENERATED AS IDENTITY`。
- JSON/URL 中的实体 ID 一律十进制字符串；Go 内部 `int64`。
- 时间列一律 `TIMESTAMPTZ`；API 中 RFC3339 UTC。
- 枚举列必须用 `CHECK` 限制取值。
- 唯一性：名称类唯一约束使用 `lower(...)` 表达式唯一索引（大小写不敏感），部分唯一索引用 `WHERE`。
- 约束/索引命名：显式创建的唯一索引/普通索引使用 `uk_<table>_...`、`idx_<table>_...`；
  `PRIMARY KEY`、`REFERENCES`、`CHECK` 均内联、由 PostgreSQL 自动命名（契约冻结谓词，不冻结自动名）。
- **跨上下文不做数据库外键**（见 §4）；同一上下文内可用 `REFERENCES ... ON DELETE CASCADE`。
- 迁移只追加、不回改；`schema_migrations` 由迁移执行器维护，不属于业务上下文。

## 2. ID 约定与例外

| 对象 | 主键/标识 | 说明 |
|---|---|---|
| 所有业务实体 | `BIGINT` 雪花 | 41bit ms + 10bit node + 12bit seq；node 来自 DB 设置 `runtime.snowflake_node_id`（默认 1）；应用层生成 |
| `event` | `PRIMARY KEY (channel, seq)` | `seq` 是**频道内**事件顺序号，由 `event_channel.last_seq` 在同一事务内分配；不是业务实体 ID；JSON 中为整数 |
| `event_channel` | `channel TEXT PRIMARY KEY` | 频道自然键 |
| `content_object` | `sha256 TEXT PRIMARY KEY` | 内容主键：64 位小写 hex SHA-256 |
| `system_setting` | `key TEXT PRIMARY KEY` | 设置键自然键 |
| `system_setting_revision` | `id BIGINT`（雪花） | 变更历史行 ID，兼作分页游标 |
| `auth_token` | `id BIGINT`（雪花）；`token_hash` 唯一 | 明文 token 不入库；DB 只存小写 hex SHA-256 |
| `schema_migrations` | `version TEXT PRIMARY KEY` | 迁移文件名自然键，执行器所有 |

## 3. 表清单（按迁移）

### 3.1 `settings_0001_settings.sql` — 设置中心

**`system_setting`**（上下文：settings）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `key` | TEXT | PRIMARY KEY |
| `value_json` | JSONB | NOT NULL |
| `category` | TEXT | NOT NULL |
| `type` | TEXT | NOT NULL，CHECK `type IN ('string','number','boolean','json')` |
| `is_secret` | BOOLEAN | NOT NULL DEFAULT false |
| `restart_required` | BOOLEAN | NOT NULL DEFAULT false |
| `description` | TEXT | NOT NULL DEFAULT '' |
| `version` | INT | NOT NULL DEFAULT 1（每次更新 +1） |
| `updated_by` | BIGINT | NULL（逻辑引用 identity，无 FK） |
| `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

**`system_setting_revision`**（上下文：settings）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PRIMARY KEY（雪花） |
| `setting_key` | TEXT | NOT NULL，`REFERENCES system_setting(key) ON DELETE CASCADE` |
| `old_value` | JSONB | NULL |
| `new_value` | JSONB | NOT NULL |
| `actor_id` | BIGINT | NULL（逻辑引用 identity，无 FK） |
| `created_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

索引：`idx_system_setting_revision_key_created (setting_key, created_at DESC)`。

### 3.2 `identity_0001_identity.sql` — 身份/令牌

**`app_user`**（上下文：identity）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PRIMARY KEY（雪花） |
| `username` | TEXT | NOT NULL |
| `display_name` | TEXT | NOT NULL |
| `password_hash` | TEXT | NOT NULL（bcrypt(sha256+base64 预哈希)，cost 12；不返回） |
| `role` | TEXT | NOT NULL，CHECK `role IN ('admin','user')` |
| `status` | TEXT | NOT NULL DEFAULT 'active'，CHECK `status IN ('active','disabled')` |
| `created_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |
| `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

索引/唯一性：

- `uk_app_user_username` UNIQUE `(lower(username))`；
- `uk_app_user_one_admin` UNIQUE `(role) WHERE role = 'admin'`（**至多一个 admin**）。

**`auth_token`**（上下文：identity）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PRIMARY KEY（雪花） |
| `user_id` | BIGINT | NOT NULL，`REFERENCES app_user(id) ON DELETE CASCADE` |
| `token_hash` | TEXT | NOT NULL UNIQUE（SHA-256 hex；明文 token 不落库） |
| `expires_at` | TIMESTAMPTZ | NOT NULL（固定 7 天 TTL） |
| `created_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

索引：`idx_auth_token_user (user_id)`、`idx_auth_token_expires (expires_at)`。

### 3.3 `llm_0001_llm.sql` — LLM 配置/模型目录

**`llm_config`**（上下文：llm）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PRIMARY KEY（雪花） |
| `name` | TEXT | NOT NULL |
| `provider` | TEXT | NOT NULL DEFAULT 'openai' |
| `protocol` | TEXT | NOT NULL DEFAULT 'openai'，CHECK `protocol IN ('openai','openai_compatible')` |
| `base_url` | TEXT | NOT NULL |
| `api_key` | TEXT | NOT NULL DEFAULT ''（明文入库；API 只返回 `api_key_set`） |
| `headers` | JSONB | NOT NULL DEFAULT '{}'::jsonb |
| `timeout_seconds` | INT | NOT NULL DEFAULT 60，CHECK `BETWEEN 1 AND 600` |
| `max_retries` | INT | NOT NULL DEFAULT 2，CHECK `BETWEEN 0 AND 10` |
| `is_default` | BOOLEAN | NOT NULL DEFAULT false |
| `status` | TEXT | NOT NULL DEFAULT 'active'，CHECK `IN ('active','disabled')` |
| `created_by` | BIGINT | NULL（逻辑引用 identity，无 FK） |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

索引/唯一性：

- `uk_llm_config_name` UNIQUE `(lower(name))`；
- `uk_llm_config_default` UNIQUE `(is_default) WHERE is_default`（**至多一个默认配置**）。

**`llm_model`**（上下文：llm）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PRIMARY KEY（雪花） |
| `config_id` | BIGINT | NOT NULL，`REFERENCES llm_config(id) ON DELETE CASCADE` |
| `model` | TEXT | NOT NULL |
| `display_name` | TEXT | NOT NULL DEFAULT '' |
| `context_window` | INT | NOT NULL DEFAULT 0，CHECK `>= 0` |
| `max_output` | INT | NOT NULL DEFAULT 0，CHECK `>= 0` |
| `supports_tools` | BOOLEAN | NOT NULL DEFAULT false |
| `supports_vision` | BOOLEAN | NOT NULL DEFAULT false |
| `source` | TEXT | NOT NULL DEFAULT 'manual'，CHECK `IN ('catalog','manual')` |
| `status` | TEXT | NOT NULL DEFAULT 'active'，CHECK `IN ('active','disabled')` |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

唯一性：`uk_llm_model_config_model` UNIQUE `(config_id, model)`。

### 3.4 `eventing_0001_events.sql` — 持久事件

**`event_channel`**（上下文：eventing）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `channel` | TEXT | PRIMARY KEY |
| `last_seq` | BIGINT | NOT NULL DEFAULT 0 |
| `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

**`event`**（上下文：eventing）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `channel` | TEXT | NOT NULL，`REFERENCES event_channel(channel)` |
| `seq` | BIGINT | NOT NULL |
| `type` | TEXT | NOT NULL |
| `envelope` | JSONB | NOT NULL DEFAULT '{}'::jsonb |
| `created_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |
| — | — | PRIMARY KEY `(channel, seq)` |

索引：`idx_event_created_at (created_at)`。

> P0 差异记录：规格 §8.1 文字写“索引 `(channel, created_at)`”，实际迁移为 `idx_event_created_at (created_at)`。
> 契约以实际迁移为准；handoff 需记录该实现差异（不影响游标/回放语义）。

### 3.5 `storage_0001_content_ledger.sql` — 内容账本

**`content_object`**（上下文：storage）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `sha256` | TEXT | PRIMARY KEY（64 位小写 hex） |
| `size` | BIGINT | NOT NULL，CHECK `size >= 0` |
| `mime` | TEXT | NOT NULL DEFAULT 'application/octet-stream' |
| `relative_path` | TEXT | NOT NULL |
| `created_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

**`content_ref`**（上下文：storage；供 P1+ 使用，P0 无公开 CRUD API）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PRIMARY KEY（雪花） |
| `object_id` | TEXT | NOT NULL，`REFERENCES content_object(sha256)` |
| `owner_kind` | TEXT | NOT NULL（逻辑引用其它上下文） |
| `owner_id` | BIGINT | NOT NULL（逻辑引用其它上下文，无 FK） |
| `role` | TEXT | NOT NULL DEFAULT 'attachment' |
| `created_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |
| `deleted_at` | TIMESTAMPTZ | NULL（软删除） |

索引：`idx_content_ref_object (object_id)`、`idx_content_ref_owner (owner_kind, owner_id)`。

### 3.6 `audit_0001_audit_log.sql` — 追加式审计

**`audit_log`**（上下文：audit）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PRIMARY KEY（雪花；分页游标） |
| `actor_id` | BIGINT | NULL（匿名事件为 NULL；逻辑引用 identity，无 FK） |
| `action` | TEXT | NOT NULL（如 `auth.login`、`settings.updated`） |
| `target_kind` | TEXT | NOT NULL DEFAULT '' |
| `target_id` | TEXT | NOT NULL DEFAULT ''（业务 ID 十进制字符串） |
| `request_id` | TEXT | NOT NULL DEFAULT '' |
| `ip` | TEXT | NOT NULL DEFAULT '' |
| `user_agent` | TEXT | NOT NULL DEFAULT '' |
| `metadata` | JSONB | NOT NULL DEFAULT '{}'::jsonb（禁止密码/token/key） |
| `created_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

索引：

- `idx_audit_log_created_at (created_at DESC)`；
- `idx_audit_log_action (action, created_at DESC)`；
- `idx_audit_log_actor (actor_id, created_at DESC)`。

### 3.7 `resource_0001_catalog.sql` — 资源目录

**`sut`**（上下文：resource）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PRIMARY KEY（雪花） |
| `name` | TEXT | NOT NULL |
| `intro` | TEXT | NOT NULL DEFAULT '' |
| `created_by` | BIGINT | NOT NULL（逻辑引用 identity，无 FK） |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

唯一性：`uk_sut_name` UNIQUE `(lower(name))`。

**`sut_environment`**（上下文：resource）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PRIMARY KEY（雪花） |
| `sut_id` | BIGINT | NOT NULL，`REFERENCES sut(id) ON DELETE CASCADE` |
| `name` | TEXT | NOT NULL |
| `base_url` | TEXT | NOT NULL（应用层校验 http/https） |
| `is_default` | BOOLEAN | NOT NULL DEFAULT false |
| `status` | TEXT | NOT NULL DEFAULT 'active'，CHECK `IN ('active','disabled')` |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

唯一性：

- `uk_sut_environment_name` UNIQUE `(sut_id, lower(name))`；
- `uk_sut_environment_default` UNIQUE `(sut_id) WHERE is_default`（每个 SUT 至多一个默认环境）。

**`sut_page`**（上下文：resource）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PRIMARY KEY（雪花） |
| `sut_id` | BIGINT | NOT NULL，`REFERENCES sut(id) ON DELETE CASCADE` |
| `environment_id` | BIGINT | NOT NULL，`REFERENCES sut_environment(id) ON DELETE CASCADE` |
| `name` | TEXT | NOT NULL |
| `url` | TEXT | NOT NULL |
| `route_template` | TEXT | NOT NULL |
| `page_type` | TEXT | NOT NULL DEFAULT 'page' |
| `auth_role` | TEXT | NOT NULL DEFAULT '' |
| `module` | TEXT | NOT NULL DEFAULT '' |
| `status` | TEXT | NOT NULL DEFAULT 'approved'，CHECK `IN ('approved','proposed','rejected')` |
| `source` | TEXT | NOT NULL DEFAULT 'admin'，CHECK `IN ('admin','ai','import')` |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

唯一性/索引：`uk_sut_page_template (environment_id, route_template)`、`idx_sut_page_sut (sut_id)`。

**`sut_api`**（上下文：resource）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PRIMARY KEY（雪花） |
| `sut_id` | BIGINT | NOT NULL，`REFERENCES sut(id) ON DELETE CASCADE` |
| `environment_id` | BIGINT | NOT NULL，`REFERENCES sut_environment(id) ON DELETE CASCADE` |
| `method` | TEXT | NOT NULL，CHECK `IN ('GET','POST','PUT','PATCH','DELETE','HEAD','OPTIONS')` |
| `path_template` | TEXT | NOT NULL |
| `name` | TEXT | NOT NULL |
| `auth_role` | TEXT | NOT NULL DEFAULT '' |
| `source` | TEXT | NOT NULL DEFAULT 'manual'，CHECK `IN ('code','openapi','ai','manual','import')` |
| `status` | TEXT | NOT NULL DEFAULT 'approved'，CHECK `IN ('approved','proposed','rejected')` |
| `schema_ref` | TEXT | NOT NULL DEFAULT '' |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

唯一性/索引：`uk_sut_api_route (environment_id, method, path_template)`、`idx_sut_api_sut (sut_id)`。

**`target_database`**（上下文：resource）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PRIMARY KEY（雪花） |
| `sut_id` | BIGINT | NOT NULL，`REFERENCES sut(id) ON DELETE CASCADE` |
| `environment_id` | BIGINT | NOT NULL，`REFERENCES sut_environment(id) ON DELETE CASCADE` |
| `name` | TEXT | NOT NULL |
| `kind` | TEXT | NOT NULL DEFAULT 'postgres'，CHECK `IN ('postgres')` |
| `dsn` | TEXT | NOT NULL |
| `username` | TEXT | NOT NULL |
| `password` | TEXT | NOT NULL DEFAULT ''（只入不出，API 返回 `password_set`） |
| `read_only` | BOOLEAN | NOT NULL DEFAULT true |
| `status` | TEXT | NOT NULL DEFAULT 'active'，CHECK `IN ('active','disabled')` |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

唯一性：`uk_target_database_name (environment_id, lower(name))`。

**`test_account`**（上下文：resource）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PRIMARY KEY（雪花） |
| `sut_id` | BIGINT | NOT NULL，`REFERENCES sut(id) ON DELETE CASCADE` |
| `environment_id` | BIGINT | NOT NULL，`REFERENCES sut_environment(id) ON DELETE CASCADE` |
| `username` | TEXT | NOT NULL |
| `password` | TEXT | NOT NULL（只入不出，API 返回 `password_set`） |
| `roles` | JSONB | NOT NULL DEFAULT '[]'::jsonb（字符串数组） |
| `notes` | TEXT | NOT NULL DEFAULT '' |
| `status` | TEXT | NOT NULL DEFAULT 'active'，CHECK `IN ('active','disabled')` |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

唯一性：`uk_test_account_username (environment_id, lower(username))`。

**`code_repository`**（上下文：resource）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PRIMARY KEY（雪花） |
| `name` | TEXT | NOT NULL |
| `path` | TEXT | NOT NULL |
| `branch` | TEXT | NOT NULL DEFAULT '' |
| `include` | JSONB | NOT NULL DEFAULT '[]'::jsonb（字符串数组） |
| `exclude` | JSONB | NOT NULL DEFAULT '[]'::jsonb（字符串数组） |
| `created_by` | BIGINT | NOT NULL（逻辑引用 identity，无 FK） |
| `status` | TEXT | NOT NULL DEFAULT 'active'，CHECK `IN ('active','disabled')` |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

唯一性：`uk_code_repository_name UNIQUE (lower(name))`。

### 3.8 `version_0001_versions.sql` — 版本与多 SUT 绑定

**`version`**（上下文：version）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PRIMARY KEY（雪花） |
| `title` | TEXT | NOT NULL |
| `status` | TEXT | NOT NULL DEFAULT 'initial'，CHECK `IN ('initial','running','completed')`；P0 只写 `initial` |
| `auto_design_new_requirements` | BOOLEAN | NOT NULL DEFAULT false |
| `closed_at` | TIMESTAMPTZ | NULL（P0 不写） |
| `close_reason` | TEXT | NOT NULL DEFAULT ''（P0 不写） |
| `close_run_id` | BIGINT | NULL（P0 不写） |
| `created_by` | BIGINT | NOT NULL（逻辑引用 identity，无 FK） |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

索引：`idx_version_created_at (created_at DESC)`。

**`version_sut`**（上下文：version）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PRIMARY KEY（雪花） |
| `version_id` | BIGINT | NOT NULL，`REFERENCES version(id) ON DELETE CASCADE` |
| `sut_id` | BIGINT | NOT NULL（**逻辑引用 resource，无 FK**） |
| `environment_id` | BIGINT | NOT NULL（**逻辑引用 resource，无 FK**） |
| `code_repo_id` | BIGINT | NULL（**逻辑引用 resource，无 FK**） |
| `scope` | JSONB | NOT NULL DEFAULT `'{"pages":[],"apis":[]}'::jsonb`；值为 ID 字符串数组 |
| `status` | TEXT | NOT NULL DEFAULT 'active'，CHECK `IN ('active','disabled')` |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

唯一性/索引：

- `uk_version_sut UNIQUE (version_id, sut_id)`；
- `idx_version_sut_sut (sut_id)`、`idx_version_sut_environment (environment_id)`、`idx_version_sut_repo (code_repo_id)`。

**`version_sut_resource`**（上下文：version）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PRIMARY KEY（雪花） |
| `version_sut_id` | BIGINT | NOT NULL，`REFERENCES version_sut(id) ON DELETE CASCADE` |
| `kind` | TEXT | NOT NULL，CHECK `IN ('database','account')` |
| `resource_id` | BIGINT | NOT NULL（**逻辑引用 resource，无 FK**） |
| `role` | TEXT | NOT NULL DEFAULT '' |
| `selected` | BOOLEAN | NOT NULL DEFAULT true |
| `created_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

唯一性：`uk_version_sut_resource (version_sut_id, kind, resource_id, role)`。

**`schema_migrations`**（迁移执行器维护，非业务表）

| 列 | 类型 | 约束 |
|---|---|---|
| `version` | TEXT | PRIMARY KEY（迁移文件名） |
| `checksum` | TEXT | NOT NULL（原始 SQL 字节的 SHA-256 小写 hex） |
| `applied_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

## 4. 跨上下文引用（无 FK）与同上下文 FK

### 4.1 只做逻辑引用、禁止建 FK 的列

| 列 | 引用的上下文/实体 |
|---|---|
| `system_setting.updated_by`、`system_setting_revision.actor_id` | identity（用户） |
| `audit_log.actor_id` | identity（用户） |
| `llm_config.created_by` | identity（用户） |
| `sut.created_by`、`code_repository.created_by` | identity（用户） |
| `version.created_by` | identity（用户） |
| `version_sut.sut_id`、`environment_id`、`code_repo_id` | resource（SUT/环境/代码仓） |
| `version_sut_resource.resource_id`（按 `kind`） | resource（目标库/测试账号） |
| `content_ref.owner_kind` + `owner_id` | 任意领域对象 |
| `event.envelope` 中的 `scope.*`、`actor_id` | 任意领域对象/用户 |
| `event.channel` 中的 `version:{id}`/`user:{id}` | 逻辑频道，不建 FK |
| `delivery_issue.version_id`、`delivery_issue_ref.version_sut_id`、`delivery_report.version_id`/`wave_id`、`delivery_report_ref.ref_id`、`delivery_analysis_run.version_id` | version/design/execution/delivery（逻辑引用） |
| `delivery_issue.created_by_run_id`、`delivery_issue.merged_by`、`delivery_issue_status_history.actor_id`、`delivery_report.created_by_run_id`、`delivery_report_revision.created_by_run_id`、`delivery_analysis_run.agent_run_id`、`requirement_closure.closed_by_run_id` | agent（agent_run）/identity（用户） |

原因：0.2 四层架构要求跨上下文只依赖 application 接口，不允许数据库层跨上下文耦合；
删除守卫由 `resource/application.BindingUsageChecker` 在应用层保证（spec §6.4）。

### 4.2 同上下文 FK（允许，均在同一迁移/上下文内）

| 子表列 | 父表 | 删除行为 |
|---|---|---|
| `auth_token.user_id` | `app_user(id)` | CASCADE |
| `system_setting_revision.setting_key` | `system_setting(key)` | CASCADE |
| `llm_model.config_id` | `llm_config(id)` | CASCADE |
| `event.channel` | `event_channel(channel)` | NO ACTION |
| `content_ref.object_id` | `content_object(sha256)` | NO ACTION |
| `sut_environment.sut_id` | `sut(id)` | CASCADE |
| `sut_page.sut_id` / `sut_page.environment_id` | `sut(id)` / `sut_environment(id)` | CASCADE |
| `sut_api.sut_id` / `sut_api.environment_id` | `sut(id)` / `sut_environment(id)` | CASCADE |
| `target_database.sut_id` / `.environment_id` | `sut(id)` / `sut_environment(id)` | CASCADE |
| `test_account.sut_id` / `.environment_id` | `sut(id)` / `sut_environment(id)` | CASCADE |
| `version_sut.version_id` | `version(id)` | CASCADE |
| `version_sut_resource.version_sut_id` | `version_sut(id)` | CASCADE |
| `delivery_issue_ref.issue_id` | `delivery_issue(id)` | CASCADE |
| `delivery_issue_status_history.issue_id` | `delivery_issue(id)` | CASCADE |
| `delivery_issue.merged_into_id` | `delivery_issue(id)` | NO ACTION（合并目标；应用层防成环） |
| `delivery_report_revision.report_id` | `delivery_report(id)` | CASCADE |
| `delivery_report_ref.report_id` | `delivery_report(id)` | CASCADE |
| `delivery_report_ref.report_revision_id` | `delivery_report_revision(id)` | CASCADE |
| `requirement_closure.requirement_id` | `requirement(id)` | CASCADE |
| `requirement_closure.requirement_version_id` | `requirement_version(id)` | CASCADE |

## 5. 表归属（bounded context / 迁移文件）

| 上下文 | 迁移文件 | 表 |
|---|---|---|
| settings | `settings_0001_settings.sql` | `system_setting`、`system_setting_revision` |
| identity | `identity_0001_identity.sql` | `app_user`、`auth_token` |
| llm | `llm_0001_llm.sql` | `llm_config`、`llm_model` |
| eventing | `eventing_0001_events.sql` | `event_channel`、`event` |
| storage | `storage_0001_content_ledger.sql` | `content_object`、`content_ref` |
| audit | `audit_0001_audit_log.sql` | `audit_log` |
| resource | `resource_0001_catalog.sql` | `sut`、`sut_environment`、`sut_page`、`sut_api`、`target_database`、`test_account`、`code_repository` |
| version | `version_0001_versions.sql` | `version`、`version_sut`、`version_sut_resource` |
| version (P1) | `version_0002_requirement.sql`、`version_0003_conversation.sql`、`version_0004_requirement_attachment.sql` | `requirement`、`requirement_version`、`requirement_confirmation`、`requirement_relation`、`conversation`、`conversation_message`、`conversation_summary`、`requirement_attachment` |
| agent (P1) | `agent_0001_work_item.sql`、`agent_0002_team_room.sql`、`agent_0003_runtime.sql` | `work_item`、`work_item_attempt`、`team_room_post`、`team_room_digest`、`agent_profile`、`agent_run`、`agent_message`、`context_packet` |
| eventing (P1) | `eventing_0002_event_index.sql` | 仅索引 `idx_event_channel_created_at` |
| delivery (P4) | `delivery_0001_issue_lifecycle.sql`、`delivery_0002_report_revision.sql`、`delivery_0003_analysis_run.sql` | `delivery_issue`、`delivery_issue_ref`、`delivery_issue_status_history`、`delivery_report`、`delivery_report_revision`、`delivery_report_ref`、`delivery_analysis_run` |
| version (P4) | `version_0005_requirement_closure.sql` | `requirement_closure` |
| chain (P4) | 无迁移、无表 | 只读读模型：请求级聚合 version/design/execution/delivery/resource 的 application 端口，不建 chain 表 |
| db/migrate | 迁移执行器 | `schema_migrations` |

## 6. 迁移幂等与数据不变量（摘要）

- 重复执行迁移不产生变化（`schema_migrations` 已有记录且 checksum 一致 → 跳过）。
- 已应用迁移文件内容变化 → 执行器报错并中止启动；只能新增迁移。
- P0 不迁移 0.1 数据；新库从空 schema 开始。
- 种子：设置目录 `ON CONFLICT (key) DO NOTHING`；空 `app_user` 表时创建唯一 admin（`admin/admin123`，幂等）。
- 删除资源受版本绑定守卫：被引用返回 409 `conflict`，不依赖数据库 FK 级联跨上下文。

---

## 7. P1 增量表结构（`frozen: P1`，2026-10-31）

> 来源：`docs/superpowers/specs/2026-10-31-eztest-v02-p1-runtime-clarification-design.md` §3。P1 不修改 P0 表；新增表全部雪花 `BIGINT` 主键、`TIMESTAMPTZ`、CHECK 枚举与 `uk_/idx_` 命名。跨上下文只做逻辑引用，不建 FK。

### 7.1 `agent_0001_work_item.sql`

`work_item`：`id BIGINT PK`、`version_id BIGINT NOT NULL`、`team TEXT NOT NULL`、`type TEXT NOT NULL`、`status TEXT NOT NULL DEFAULT 'open'`（CHECK `open/claimed/running/waiting_human/done/failed/dead`）、`priority INT NOT NULL DEFAULT 0`、`dedupe_key TEXT NOT NULL`、`depends_on BIGINT NULL`、`payload JSONB NOT NULL DEFAULT '{}'`、`claimed_by TEXT NULL`、`lease_until TIMESTAMPTZ NULL`、`lease_epoch BIGINT NOT NULL DEFAULT 0`、`attempts INT NOT NULL DEFAULT 0`、`max_attempts INT NOT NULL DEFAULT 3`、`last_error TEXT NULL`、`available_at TIMESTAMPTZ NOT NULL DEFAULT now()`、`created_at/updated_at`。
约束/索引：`uk_work_item_dedupe UNIQUE(version_id,type,dedupe_key)`；`idx_work_item_claim(status,available_at,priority DESC,id)`；`idx_work_item_lease(status,lease_until)`；`idx_work_item_version(version_id,status,id)`。

`work_item_attempt`：`id BIGINT PK`、`work_item_id BIGINT NOT NULL REFERENCES work_item(id)`、`attempt INT NOT NULL`、`status TEXT NOT NULL`、`error TEXT NULL`、`started_at`、`finished_at`；`UNIQUE(work_item_id,attempt)`；`idx_work_item_attempt_item(work_item_id,id)`。

### 7.2 `agent_0002_team_room.sql`

`team_room_post`：`id BIGINT PK`、`version_id BIGINT NOT NULL`、`team TEXT NOT NULL`、`kind TEXT NOT NULL`（CHECK `finding/decision/blocker/handoff/status`）、`content TEXT NOT NULL`、`refs JSONB NOT NULL DEFAULT '[]'`、`agent_run_id BIGINT NULL`、`created_by BIGINT NULL`、`created_at`；`idx_team_room_post_team(version_id,team,id DESC)`、`idx_team_room_post_created(version_id,created_at)`。

`team_room_digest`：`id BIGINT PK`、`version_id BIGINT NOT NULL`、`team TEXT NOT NULL`、`summary TEXT NOT NULL`、`pinned_post_ids JSONB NOT NULL DEFAULT '[]'`、`token_estimate INT NOT NULL DEFAULT 0`、`version INT NOT NULL`、`updated_at`；`UNIQUE(version_id,team,version)`；`idx_team_room_digest_latest(version_id,team,version DESC)`。

### 7.3 `agent_0003_runtime.sql`

`agent_profile`：`id BIGINT PK`、`key TEXT NOT NULL UNIQUE`、`name TEXT NOT NULL`、`system_prompt TEXT NOT NULL`、`allowed_tools JSONB NOT NULL DEFAULT '[]'`、`model TEXT NULL`、`budgets JSONB NOT NULL DEFAULT '{}'`、`enabled BOOL NOT NULL DEFAULT true`、`created_at/updated_at`。

`agent_run`：`id BIGINT PK`、`profile_key TEXT NOT NULL`、`work_item_id BIGINT NULL`、`conversation_id BIGINT NULL`、`status TEXT NOT NULL DEFAULT 'running'`（CHECK `running/done/failed/aborted/interrupted`）、`terminal_reason TEXT NULL`、`turns INT NOT NULL DEFAULT 0`、`usage JSONB NOT NULL DEFAULT '{}'`、`owner_instance TEXT NOT NULL`、`lease_epoch BIGINT NOT NULL DEFAULT 0`、`context_packet_id BIGINT NULL`、`started_at`、`finished_at`；`idx_agent_run_work_item(work_item_id)`、`idx_agent_run_conversation(conversation_id,id DESC)`、`idx_agent_run_status(status)`。

`agent_message`：`id BIGINT PK`、`run_id BIGINT NULL`、`conversation_id BIGINT NULL`、`role TEXT NOT NULL`（CHECK `system/user/assistant/tool`）、`parts JSONB NOT NULL DEFAULT '[]'`、`tool_calls JSONB NOT NULL DEFAULT '[]'`、`tool_call_id TEXT NULL`、`usage JSONB NOT NULL DEFAULT '{}'`、`created_at`；`idx_agent_message_run(run_id,id)`、`idx_agent_message_conversation(conversation_id,id)`。

`context_packet`：`id BIGINT PK`、`run_id BIGINT NOT NULL`、`version_id BIGINT NOT NULL`、`sections JSONB NOT NULL`、`token_counts JSONB NOT NULL`、`hash TEXT NOT NULL`（64 hex）、`created_at`；`idx_context_packet_run(run_id)`。

### 7.4 `version_0002_requirement.sql`

`requirement`：`id BIGINT PK`、`version_id BIGINT NOT NULL REFERENCES version(id)`、`current_version_id BIGINT NULL`、`status TEXT NOT NULL DEFAULT 'draft'`（CHECK `draft/clarified/closed/obsolete`）、`target_version_sut_ids JSONB NOT NULL DEFAULT '[]'`、`closed_at NULL`、`closed_by_run_id BIGINT NULL`、`created_by BIGINT NULL`、`created_by_run_id BIGINT NULL`、`source_conversation_id BIGINT NULL`、`created_at/updated_at`；`idx_requirement_version_status(version_id,status,id DESC)`。

`requirement_version`：`id BIGINT PK`、`requirement_id BIGINT NOT NULL REFERENCES requirement(id)`、`revision INT NOT NULL`、`title/description/acceptance_criteria TEXT NOT NULL DEFAULT ''`、`priority TEXT NOT NULL DEFAULT 'P2'`（CHECK `P0/P1/P2/P3`）、`constraints JSONB`、`target_version_suts JSONB`、`target_pages JSONB`、`target_apis JSONB`、`content_hash TEXT NOT NULL`（64 hex）、`created_by BIGINT NULL`、`created_by_run_id BIGINT NULL`、`created_at`；`UNIQUE(requirement_id,revision)`；`idx_requirement_version_latest(requirement_id,id DESC)`。

`requirement_confirmation`：`id BIGINT PK`、`requirement_version_id BIGINT NOT NULL REFERENCES requirement_version(id)`、`scope TEXT NOT NULL`（CHECK `requirement/batch`）、`batch_id TEXT NULL`、`actor_id BIGINT NOT NULL`、`confirmed_at`、`affected_requirement_ids JSONB NOT NULL DEFAULT '[]'`；`UNIQUE(requirement_version_id)`；`idx_requirement_confirmation_batch(batch_id)`、`idx_requirement_confirmation_actor(actor_id,confirmed_at DESC)`。

`requirement_relation`：`id BIGINT PK`、`version_id BIGINT NOT NULL`、`from_requirement_id BIGINT NOT NULL`、`to_requirement_id BIGINT NOT NULL`、`kind TEXT NOT NULL`（CHECK `depends_on/conflicts_with/overlaps/shares_scope`）、`description TEXT NOT NULL DEFAULT ''`、`source_revision_id BIGINT NULL`、`created_by BIGINT NULL`、`created_by_run_id BIGINT NULL`、`created_at`；`CHECK(from_requirement_id<>to_requirement_id)`；`UNIQUE(version_id,from_requirement_id,to_requirement_id,kind)`；`idx_requirement_relation_from/to`。

### 7.5 `version_0003_conversation.sql`

`conversation`：`id BIGINT PK`、`version_id BIGINT NOT NULL REFERENCES version(id)`、`kind TEXT NOT NULL`（CHECK `clarification/design`）、`title TEXT NOT NULL`、`status TEXT NOT NULL DEFAULT 'active'`（CHECK `active/closed`）、`created_by BIGINT NULL`、`created_at/updated_at`；`idx_conversation_version_kind(version_id,kind,id DESC)`。

`conversation_message`：`id BIGINT PK`、`conversation_id BIGINT NOT NULL REFERENCES conversation(id)`、`role TEXT NOT NULL`（CHECK `user/assistant`）、`parts JSONB NOT NULL DEFAULT '[]'`、`refs JSONB NOT NULL DEFAULT '[]'`、`token_usage JSONB NOT NULL DEFAULT '{}'`、`actor_id BIGINT NULL`、`agent_run_id BIGINT NULL`、`client_message_id TEXT NULL`、`reply_to_message_id BIGINT NULL`、`created_at`；`UNIQUE(conversation_id,client_message_id)`；`idx_conversation_message_conversation(conversation_id,id)`、`idx_conversation_message_reply(conversation_id,reply_to_message_id)`。

`conversation_summary`：`id BIGINT PK`、`conversation_id BIGINT NOT NULL REFERENCES conversation(id)`、`summary TEXT NOT NULL`、`pinned_facts JSONB NOT NULL DEFAULT '[]'`、`token_estimate INT NOT NULL DEFAULT 0`、`version INT NOT NULL`、`updated_at`；`UNIQUE(conversation_id,version)`；`idx_conversation_summary_latest(conversation_id,version DESC)`。

### 7.6 `version_0004_requirement_attachment.sql`

`requirement_attachment`：`id BIGINT PK`、`version_id BIGINT NOT NULL`、`requirement_id BIGINT NULL`、`requirement_version_id BIGINT NULL`、`conversation_message_id BIGINT NULL`、`content_ref_id BIGINT NOT NULL`、`name TEXT NOT NULL`、`mime TEXT NOT NULL`、`size BIGINT NOT NULL`、`extraction_status TEXT NOT NULL DEFAULT 'pending'`（CHECK `pending/processing/done/failed/skipped`）、`extracted_content_ref_id BIGINT NULL`、`extraction_error TEXT NULL`、`created_by BIGINT NULL`、`created_at/updated_at`；`idx_requirement_attachment_version(version_id,requirement_id)`、`idx_requirement_attachment_message(conversation_message_id)`、`idx_requirement_attachment_pending(extraction_status,id)`。

- `extracted_content_ref_id/extraction_status/extraction_error/requirement_version_id` 是 HLD“关键字段”之外的运行态字段（P1-D5）；提取文本作为 text `content_object` + `content_ref`，不复制全文进需求版本。

### 7.7 `eventing_0002_event_index.sql`

`CREATE INDEX idx_event_channel_created_at ON event(channel, created_at);`（收口 P0 handoff §2 的索引漂移；不修改 P0 迁移/既有索引）。

### 7.8 P1 数据不变量

- `work_item` 全时唯一 `(version_id,type,dedupe_key)`；重试复用同一行，需再次执行用新 dedupe key。
- `requirement_version` 不可变；`(requirement_id,revision)` 唯一；`requirement.current_version_id` 由服务端维护。
- 确认按 `requirement_version_id` 唯一；批量确认每受影响版本一行、共享 `batch_id`、`scope='batch'`。
- `conversation_message.(conversation_id,client_message_id)` 唯一（NULL 不冲突）；assistant 通过 `reply_to_message_id` 关联用户消息。
- `team_room_digest` 多版本保留最近 N 版；`context_packet.hash` 为 64 hex。
- **`version_sut.scope` 修正**：DB 中页面/API ID 数组为数字；API/事件中按十进制字符串输出（P0 文档此处以本 P1 契约为准）。

## 8. P2 增量表结构（`frozen: P2`，2026-10-31）

> 命名按 Agent.md §6.9/D34 方案 A：小写单数、`业务名称_作用`；PK/UK/IDX 显式 `pk_/uk_/idx_`；同上下文 FK 显式，跨上下文只逻辑引用；审计列 `created_at/updated_at`。设计文档字面名映射：`scenario→case_scenario`、`export→export_artifact`、`sort→sort_order`、`line→line_no`、`format→file_format`、`size→size_bytes`、`content_ref→content_object_id`。

### 8.1 `design_0001_case_model.sql`（design 上下文，9 表）

- `case_scenario`：`id BIGINT PK`、`version_id/requirement_id/requirement_version_id BIGINT NOT NULL`、`name TEXT(1..200)`、`description`、`version_sut_ids/page_ids/api_ids JSONB`、`sort_order INT >=0`、`created_by/created_by_run_id`、`created_at/updated_at`；`pk_case_scenario`、`uk_case_scenario_requirement_version_name(requirement_version_id,name)`；索引 `idx_case_scenario_requirement`、`idx_case_scenario_requirement_version`。
- `case_entity`：`id BIGINT PK`、`scenario_id BIGINT NOT NULL REFERENCES case_scenario(id)`、`current_version_id BIGINT NULL`（循环引用，不建 FK）、`status TEXT CHECK draft/confirmed/stale`、审计列；索引 `idx_case_entity_scenario/current/status`。
- `case_version`（不可变）：`id BIGINT PK`、`case_id BIGINT NOT NULL REFERENCES case_entity(id)`、`revision INT >0`、`title`、`description/preconditions`、`test_data JSONB`、`priority P0..P3`、`default_version_sut_id NULL`、`version_sut_ids/page_ids/api_ids JSONB`、`status CHECK draft/confirmed/stale/superseded`、`content_hash 64hex`、`created_by/run_id`、`created_at/updated_at`；`uk_case_version_case_revision(case_id,revision)`；`idx_case_version_case/status`。
- `case_step`：`id PK`、`case_version_id FK`、`seq >0`、`action(1..2000)`、`expected`、`target_version_sut_id/target_page_id/target_api_id NULL`、`account_role`、`data JSONB`、审计列；`uk_case_step_version_seq(case_version_id,seq)`；`idx_case_step_version`。
- `case_assertion`：`id PK`、`case_version_id FK`、`step_seq>0`、`kind CHECK ui/api/db/text/visual`、`target/expected`、`target_version_sut_id/target_api_id/database_id NULL`、审计列；`idx_case_assertion_version(case_version_id,step_seq)`；`step_seq` 归属由应用校验。
- `case_source`：`id PK`、`case_version_id FK`、`kind CHECK requirement/code/page/api/knowledge`、`ref_id`、`path`、`line_no NULL`、`sha`、审计列；`idx_case_source_version`。
- `case_confirmation`：`id PK`、`case_version_id FK`、`scope CHECK case/scenario/requirement`、`target_scope_id`、`batch_id(1..64)`、`affected_case_version_ids JSONB`、`snapshot JSONB`、`confirmed_by`、`confirmed_at`、审计列；`uk_case_confirmation_version(case_version_id)`；`idx_case_confirmation_batch/actor`。
- `design_round`：`id PK`、`version_id`、`conversation_id`、`activation CHECK manual/auto_gap/auto_change/auto_new`、`requirement_ids/requirement_version_ids JSONB`、`scope JSONB`、`status CHECK active/completed`（P2 只写 active）、`created_by`、审计列；`uk_design_round_conversation(conversation_id)`；`idx_design_round_version/status`。
- `design_scope`：`id PK`、`version_id/requirement_id`、`activated_by_round_id NULL REFERENCES design_round(id)`、`activated_at`、`status CHECK active/closed`（P2 只写 active）、审计列；`uk_design_scope_version_requirement(version_id,requirement_id)`；`idx_design_scope_requirement`。

### 8.2 `storage_0002_export_artifact.sql`（storage 上下文，1 表）

- `export_artifact`：`id PK`、`version_id`、`owner_kind CHECK case/report/chain`、`owner_id`、`revision >0`、`file_format CHECK markdown/mermaid/opml/csv/html/pdf`、`content_object_id TEXT 64hex`、`size_bytes >=0`、`mime`、`filename(1..255)`、`status CHECK ready/failed`、`created_by`、审计列；`uk_export_artifact_version_owner_file_format_revision(version_id,owner_kind,owner_id,file_format,revision)`；`idx_export_artifact_version/created_at`。
- 导出字节写入 `content_object` 并登记 `content_ref`（owner_kind=`export`，owner_id=export id，role=`content`）；P2 不写 `paths.export_dir`（P2-D-29 例外）。

### 8.3 P2 数据不变量

- `case_entity.current_version_id` 指向最新版本；执行只认 `current_version_id = case_version.id AND status='confirmed'`。
- `case_version` 不可变；`(case_id,revision)` 唯一；需求改版重设计创建新场景/新用例，旧组 stale 保留。
- 同一需求版本内按“场景名 + 用例标题”自然键去重；内容 hash 幂等；`case.graph_propose` 可选 `base_graph_hash` 过期返回 409。
- `design_scope` 唯一 `(version_id,requirement_id)`；`design_round` 每对话唯一；P2 不关闭设计对话。
- `export_artifact` 每次新 revision；内容对象 sha256 与大小由服务端校验。

---

## 9. P3 增量表结构（`frozen: P3`，2026-11-01）

> 来源：`docs/superpowers/specs/2026-11-01-eztest-v02-p3-execution-design.md` §3（P3-D-04：10 表）。
> 迁移：`execution_0001_intent_wave.sql`、`execution_0002_result_evidence.sql`、`execution_0003_executor_human.sql`（`order.txt` 第 18–20 个）。
> P0/P1/P2 表不变。10 张新表全部雪花 `BIGINT` 主键（应用层生成）、JSON 中 ID 为十进制字符串。跨上下文只逻辑引用、不建 FK；同上下文 FK 显式。`created_at/updated_at` 为审计列；`evidence`/`evidence_ref` 不可变，只有 `created_at`（审计列例外，见 §9.2）。
> 执行域一律 `version_sut_id`；页面/API/账号/DB 归属由应用层校验，数据库不建跨上下文 FK。

### 9.1 `execution_0001_intent_wave.sql`（execution 上下文，4 表）

**`execution_intent`**（逻辑引用 `version`/`case_version`，无 FK）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_execution_intent` PRIMARY KEY |
| `version_id` | BIGINT | NOT NULL（逻辑引用 version） |
| `case_version_id` | BIGINT | NOT NULL（逻辑引用 design，无 FK） |
| `version_sut_ids` | JSONB | NOT NULL DEFAULT `'[]'::jsonb`，CHECK `jsonb_typeof = 'array'`；元素为 ID 十进制字符串 |
| `environment_fingerprint` | TEXT | NOT NULL，CHECK `~ '^[0-9a-f]{64}$'`（64 hex；算法见 spec §5.1） |
| `code_baseline` | TEXT | NOT NULL DEFAULT `''`，CHECK `char_length <= 256`；无代码仓绑定为 `''`，否则 64 hex |
| `purpose` | TEXT | NOT NULL DEFAULT `'initial'`，CHECK `initial/retest/manual` |
| `status` | TEXT | NOT NULL DEFAULT `'open'`，CHECK `open/claimed/running/done/failed/cancelled/superseded`；`open/claimed/running` 为 active |
| `dedupe_key` | TEXT | NOT NULL，CHECK `char_length BETWEEN 1 AND 256`（仅日志/work_item 关联，不承担唯一约束） |
| `attempt_no` | INTEGER | NOT NULL DEFAULT 1，CHECK `>= 1`（同键历史 intent 数 + 1，仅展示） |
| `created_by` | BIGINT | NULL（逻辑引用 identity） |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：

- `pk_execution_intent`；
- **partial unique** `uk_execution_intent_active` UNIQUE `(case_version_id, environment_fingerprint, code_baseline, purpose) WHERE status IN ('open','claimed','running')`（P3-D-08；重跑/复测在旧 intent 离开 active 后新建）；
- `idx_execution_intent_version_status (version_id, status, id DESC)`；
- `idx_execution_intent_case_version (case_version_id, status)`；
- `idx_execution_intent_dedupe (version_id, dedupe_key)`（**非唯一**；幂等由 partial unique + 状态机保证）。

**`run_wave`**（逻辑引用 `version`，无 FK）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_run_wave` PRIMARY KEY |
| `version_id` | BIGINT | NOT NULL（逻辑引用 version） |
| `status` | TEXT | NOT NULL DEFAULT `'planned'`，CHECK `planned/running/paused/done/failed/cancelled` |
| `trigger` | TEXT | NOT NULL DEFAULT `'auto'`，CHECK `auto/manual/retest` |
| `counts` | JSONB | NOT NULL DEFAULT `'{}'::jsonb`，CHECK `jsonb_typeof = 'object'`；结构见 spec §5.6（含 `by_version_sut`） |
| `started_at` / `finished_at` / `paused_at` | TIMESTAMPTZ | NULL |
| `paused_by` | BIGINT | NULL（逻辑引用 identity） |
| `pause_reason` | TEXT | NOT NULL DEFAULT `''`，CHECK `char_length <= 200` |
| `pause_mode` | TEXT | NOT NULL DEFAULT `''`，CHECK `''/graceful/immediate` |
| `report_id` | BIGINT | NULL（P4 逻辑引用；P3 恒 NULL，不建 report 表） |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：

- `pk_run_wave`；
- **partial unique** `uk_run_wave_active` UNIQUE `(version_id) WHERE status IN ('planned','running','paused')`（同版本单活跃波；`paused` 仍占活跃语义，P3-D-10）；
- `idx_run_wave_version (version_id, id DESC)`；
- `idx_run_wave_status (status, id)`。

**`case_run`**（同上下文 FK：`wave_id`/`intent_id`）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_case_run` PRIMARY KEY |
| `wave_id` | BIGINT | NOT NULL，`REFERENCES run_wave(id)` |
| `intent_id` | BIGINT | NOT NULL，`REFERENCES execution_intent(id)` |
| `case_version_id` | BIGINT | NOT NULL（逻辑引用 design，无 FK） |
| `status` | TEXT | NOT NULL DEFAULT `'pending'`，CHECK `pending/running/paused/passed/failed/blocked/inconclusive/skipped/interrupted/superseded`（四态 + 过程态合并列，P3-D-05） |
| `attempts` | INTEGER | NOT NULL DEFAULT 0，CHECK `>= 0` |
| `failure_class` | TEXT | NOT NULL DEFAULT `''`，CHECK `''/application/environment/session/model/automation/timeout/executor_lost/blocked/unknown`（9 值 + 空） |
| `summary` | JSONB | NOT NULL DEFAULT `'{}'::jsonb`，CHECK `jsonb_typeof = 'object'`；结构见 spec §3.1/§4.3 |
| `started_at` / `finished_at` | TIMESTAMPTZ | NULL |
| `owner_instance` | TEXT | NOT NULL DEFAULT `''`（API 实例标识） |
| `owner_executor_id` | BIGINT | NULL（逻辑引用 executor_instance；P3 单 endpoint） |
| `dispatch_epoch` | BIGINT | NOT NULL DEFAULT 0，CHECK `>= 0`（派发栅栏） |
| `dispatch_token` | TEXT | NOT NULL DEFAULT `''`，CHECK `char_length <= 128`（32 hex 生成；迟到写 CAS） |
| `lease_until` | TIMESTAMPTZ | NULL（runner 续租；过期由 reconciler 回收） |
| `paused_at` / `paused_by` | TIMESTAMPTZ / BIGINT | NULL |
| `pause_reason` | TEXT | NOT NULL DEFAULT `''` |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：

- `pk_case_run`；`uk_case_run_wave_intent` UNIQUE `(wave_id, intent_id)`（一个波次内同 intent 只有一个 case_run）；
- `idx_case_run_wave (wave_id, status, id)`；`idx_case_run_intent (intent_id)`；`idx_case_run_case_version (case_version_id)`；`idx_case_run_lease (status, lease_until)`。

**`case_run_sut_context`**（同上下文 FK：`case_run_id`）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_case_run_sut_context` PRIMARY KEY |
| `case_run_id` | BIGINT | NOT NULL，`REFERENCES case_run(id)` |
| `version_sut_id` | BIGINT | NOT NULL（逻辑引用 version） |
| `account_role` | TEXT | NOT NULL DEFAULT `''`，CHECK `char_length <= 100` |
| `account_id` | BIGINT | NULL（逻辑引用 resource） |
| `browser_context_id` | TEXT | NOT NULL DEFAULT `''`，CHECK `char_length <= 128`（懒创建后 upsert） |
| `storage_state` | JSONB | NULL（敏感；API 永不返回；P3 占位，P3-D-17a 后续增量） |
| `lease_epoch` | BIGINT | NOT NULL DEFAULT 0 |
| `status` | TEXT | NOT NULL DEFAULT `'creating'`，CHECK `creating/active/closed/failed` |
| `closed_at` | TIMESTAMPTZ | NULL |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：

- `pk_case_run_sut_context`；`uk_case_run_sut_context` UNIQUE `(case_run_id, version_sut_id, account_role)`；
- `idx_case_run_sut_context_run (case_run_id)`；`idx_case_run_sut_context_context (browser_context_id)`。

### 9.2 `execution_0002_result_evidence.sql`（execution 上下文，4 表）

**`step_result`**（同上下文 FK：`case_run_id`）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_step_result` PRIMARY KEY |
| `case_run_id` | BIGINT | NOT NULL，`REFERENCES case_run(id)` |
| `seq` | INTEGER | NOT NULL，CHECK `> 0` |
| `target_version_sut_id` | BIGINT | NULL（逻辑引用 version；空表示继承用例默认） |
| `status` | TEXT | NOT NULL DEFAULT `'pending'`，CHECK `pending/running/passed/failed/blocked/skipped` |
| `observed` | JSONB | NOT NULL DEFAULT `'{}'::jsonb`，CHECK `jsonb_typeof = 'object'`（`summary?/output?`） |
| `error` | TEXT | NOT NULL DEFAULT `''`，CHECK `char_length <= 2000` |
| `started_at` / `finished_at` | TIMESTAMPTZ | NULL |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：`pk_step_result`；`uk_step_result_run_seq` UNIQUE `(case_run_id, seq)`（重复写入 upsert）；`idx_step_result_run (case_run_id, seq)`。

**`assertion_result`**（同上下文 FK：`case_run_id`）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_assertion_result` PRIMARY KEY |
| `case_run_id` | BIGINT | NOT NULL，`REFERENCES case_run(id)` |
| `step_seq` | INTEGER | NOT NULL，CHECK `> 0` |
| `assertion_seq` | INTEGER | NOT NULL，CHECK `> 0`（按 `case_assertion.id` 升序派生的 1-based 序号，P3-D-06） |
| `kind` | TEXT | NOT NULL，CHECK `ui/api/db/text/visual`（取自 P2 `case_assertion.kind`） |
| `verdict` | TEXT | NOT NULL，CHECK `passed/failed/inconclusive/skipped` |
| `reason` | TEXT | NOT NULL DEFAULT `''`，CHECK `char_length <= 2000` |
| `expected` | TEXT | NOT NULL DEFAULT `''`，CHECK `char_length <= 4000`（服务端用 P2 expected 覆写） |
| `actual` | TEXT | NOT NULL DEFAULT `''`，CHECK `char_length <= 4000` |
| `target_version_sut_id` | BIGINT | NULL（逻辑引用 version） |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：`pk_assertion_result`；`uk_assertion_result_run_step_assertion` UNIQUE `(case_run_id, step_seq, assertion_seq)`（upsert 幂等）；`idx_assertion_result_run (case_run_id, step_seq, assertion_seq)`。

**`evidence`**（不可变；同上下文 FK：`case_run_id`）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_evidence` PRIMARY KEY |
| `case_run_id` | BIGINT | NOT NULL，`REFERENCES case_run(id)` |
| `version_sut_id` | BIGINT | NULL（逻辑引用 version） |
| `step_seq` | INTEGER | NULL，CHECK `IS NULL OR > 0` |
| `assertion_seq` | INTEGER | NULL，CHECK `IS NULL OR > 0` |
| `kind` | TEXT | NOT NULL，CHECK `screenshot/snapshot/console/network/http/db/download/text`（8 类） |
| `content_ref_id` | BIGINT | NOT NULL（逻辑引用 P0 `content_ref.id`；字节入内容账本，P3-D-20） |
| `sha256` | TEXT | NOT NULL，CHECK `~ '^[0-9a-f]{64}$'`（API 侧重算，不信任执行器自报） |
| `size_bytes` | BIGINT | NOT NULL，CHECK `>= 0` |
| `mime` | TEXT | NOT NULL DEFAULT `'application/octet-stream'`，CHECK `char_length <= 128` |
| `filename` | TEXT | NOT NULL DEFAULT `''`，CHECK `char_length <= 255` |
| `created_by_run_id` | BIGINT | NULL（逻辑引用 agent_run） |
| `created_at` | TIMESTAMPTZ | NOT NULL DEFAULT now()（**无 `updated_at`**：不可变） |

约束/索引：

- `pk_evidence`；
- `idx_evidence_case_run (case_run_id, id DESC)`；`idx_evidence_version_sut (version_sut_id, id DESC)`；`idx_evidence_content_ref (content_ref_id)`；`idx_evidence_kind (kind)`。

说明：证据无更新/删除 API；每 `case_run` ≤100 条、单条 ≤ `execution.evidence_max_bytes`（5 MiB）；`paths.evidence_dir` **P3 不使用**（与 P2 `paths.export_dir` 例外一致，P3-D-20），下载走鉴权端点 `GET /api/versions/{id}/evidence/{evidenceId}/content`。

**`evidence_ref`**（不可变；同上下文 FK：`evidence_id`）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_evidence_ref` PRIMARY KEY |
| `evidence_id` | BIGINT | NOT NULL，`REFERENCES evidence(id)` |
| `owner_kind` | TEXT | NOT NULL，CHECK `case_run/step_result/assertion_result/human_request` |
| `owner_id` | BIGINT | NOT NULL（逻辑引用对应实体） |
| `role` | TEXT | NOT NULL DEFAULT `'attachment'`，CHECK `char_length <= 64` |
| `created_at` | TIMESTAMPTZ | NOT NULL DEFAULT now()（**无 `updated_at`**：不可变） |

约束/索引：`pk_evidence_ref`；`uk_evidence_ref_owner` UNIQUE `(evidence_id, owner_kind, owner_id, role)`；`idx_evidence_ref_owner (owner_kind, owner_id)`。

### 9.3 `execution_0003_executor_human.sql`（execution 上下文，2 表）

**`executor_instance`**（执行器注册/心跳；P7 多执行器预留）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_executor_instance` PRIMARY KEY |
| `name` | TEXT | NOT NULL，CHECK `char_length BETWEEN 1 AND 200`（`host:port`） |
| `endpoint` | TEXT | NOT NULL，CHECK `char_length BETWEEN 1 AND 500`；UNIQUE |
| `token_hash` | TEXT | NOT NULL DEFAULT `''`，CHECK `= '' OR ~ '^[0-9a-f]{64}$'`（sha256(token)；不含明文） |
| `capacity` | INTEGER | NOT NULL DEFAULT 0，CHECK `>= 0`（= 执行器 maxContexts） |
| `active_contexts` | INTEGER | NOT NULL DEFAULT 0，CHECK `>= 0`（healthz contexts） |
| `labels` | JSONB | NOT NULL DEFAULT `'{}'::jsonb`，CHECK `jsonb_typeof = 'object'` |
| `status` | TEXT | NOT NULL DEFAULT `'offline'`，CHECK `active/draining/offline` |
| `last_heartbeat` | TIMESTAMPTZ | NULL |
| `version` | TEXT | NOT NULL DEFAULT `''`，CHECK `char_length <= 64`（healthz version） |
| `last_error` | TEXT | NOT NULL DEFAULT `''`，CHECK `char_length <= 2000` |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：`pk_executor_instance`；`uk_executor_instance_endpoint` UNIQUE `(endpoint)`；`idx_executor_instance_status (status, last_heartbeat)`。

说明：P3 单 endpoint，API 按 `execution.executor_heartbeat_seconds` 轮询 `GET {execution.executor_url}/healthz` 后 upsert（P3-D-01）；`draining` 由 admin API 摘流；`offline` 由 `execution.executor_offline_seconds` 判定。P3 不建 `/api/internal/executors/*` push 端点（P7）。

**`human_request`**（版本域对象；migration 归 execution，P3-D-16；同上下文无 FK，`case_run_id` 逻辑引用）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_human_request` PRIMARY KEY |
| `version_id` | BIGINT | NOT NULL（逻辑引用 version） |
| `case_run_id` | BIGINT | NULL（逻辑引用 case_run；可空） |
| `kind` | TEXT | NOT NULL，CHECK `login_takeover/conflict/other`（P3 只创建 `login_takeover`，其余保留） |
| `payload` | JSONB | NOT NULL DEFAULT `'{}'::jsonb`，CHECK `jsonb_typeof = 'object'`；结构 `{reason,version_sut_id,account_role,page_url,challenge_kind,evidence_id?}` |
| `status` | TEXT | NOT NULL DEFAULT `'open'`，CHECK `open/resolved/cancelled/expired`（不可回退） |
| `response` | JSONB | NOT NULL DEFAULT `'{}'::jsonb`，CHECK `jsonb_typeof = 'object'`；`{code?,note?}`，API 不回显 `code`/密码 |
| `expires_at` | TIMESTAMPTZ | NOT NULL（now + `execution.human_request_timeout_seconds`，默认 1800） |
| `resolved_by` / `resolved_at` | BIGINT / TIMESTAMPTZ | NULL（逻辑引用 identity） |
| `created_by` | BIGINT | NULL（逻辑引用 identity） |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：`pk_human_request`；`idx_human_request_version_status (version_id, status, id DESC)`；`idx_human_request_case_run (case_run_id, status)`；`idx_human_request_expiry (status, expires_at)`。

### 9.4 P3 数据不变量

1. 执行只认 `case_entity.current_version_id = case_version.id AND case_version.status='confirmed'`；stale/superseded/draft 不执行。
2. 同版本单活跃波（`planned/running/paused`，partial unique）；`paused` 仍占活跃波。
3. 同 `(case_version_id, environment_fingerprint, code_baseline, purpose)` 只允许一个 active intent（partial unique）；重跑/复测新建 intent，旧 intent 保留。
4. 一个 `(wave_id, intent_id)` 只有一个 case_run（`uk_case_run_wave_intent`）。
5. 暂停/终态后清理执行器上下文；未 cleanup 不释放配额。
6. 证据不可变：无更新/删除 API；`sha256`/`size_bytes` 与内容账本一致；API 不返回 `storage_state`/内部路径/凭据。
7. 执行域一律 `version_sut_id`；页面/API/账号/DB 必须属于同一 `version_sut`（应用层校验），`sut_id` 只展示。
8. `case_run` 迟到写必须匹配 `dispatch_epoch + dispatch_token`；不匹配返回 `lease_lost` 且不覆盖数据。
9. `human_request` 只能 `open → resolved | cancelled | expired`；`expired` 由 reconciler 扫描，不物理删除。
10. P3 不建 `analysis_run`/`issue`/`report`/`sut_account_session` 表；问题候选与覆盖缺口以事件 + 团队帖 + `design.gap` work item 表达（P3-D-13/P3-D-04）。

## 10. P4 增量表结构（`frozen: P4`，2026-11-01）

> 来源：`docs/superpowers/specs/2026-11-01-eztest-v02-p4-delivery-chain-design.md` §3（P4-D-02/P4-D-03/P4-D-05）。
> 迁移：`delivery_0001_issue_lifecycle.sql`（#21）、`delivery_0002_report_revision.sql`（#22）、`delivery_0003_analysis_run.sql`（#23）、`version_0005_requirement_closure.sql`（#24）。
> P0–P3 表不变（不 ALTER）；8 张新表全部雪花 `BIGINT` 主键（应用层生成），JSON/API 中 ID 为十进制字符串，时间 `TIMESTAMPTZ`（RFC3339 UTC）。
> 8 表均有 `created_at`/`updated_at`（D34 §6.9.4 字面口径）；**append-only** 表（`delivery_issue_ref`、`delivery_issue_status_history`、`delivery_report_ref`）的 `updated_at` 仅插入时初始化，仓储不提供更新方法，不可变语义由接口/测试保证。`delivery_report_revision` 内容 append-only（发布落定后不可变，见 §10.2）。
> 跨上下文只做逻辑引用、不建 FK；同上下文 FK 显式。约束/索引命名 `pk_<表名>`、`uk_<表名>_<全列名>`、`idx_<表名>_<全列名>`；partial unique 用唯一索引实现，谓词写在 `WHERE`。

### 10.1 `delivery_0001_issue_lifecycle.sql`（delivery 上下文，3 表）

**`delivery_issue`**（逻辑引用 `version`/`agent_run`/`identity`，无跨上下文 FK；同上下文自引用 `merged_into_id`）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_delivery_issue` PRIMARY KEY（雪花） |
| `version_id` | BIGINT | NOT NULL（逻辑引用 version） |
| `title` | TEXT | NOT NULL，CHECK `char_length BETWEEN 1 AND 500` |
| `summary` | TEXT | NOT NULL DEFAULT `''`，CHECK `char_length <= 4000` |
| `status` | TEXT | NOT NULL DEFAULT `'open'`，CHECK `open/confirmed/dismissed/merged` |
| `category` | TEXT | NOT NULL，CHECK `ui/api/db` |
| `severity` | TEXT | NOT NULL，CHECK `major/minor/trivial` |
| `dedupe_key` | TEXT | NOT NULL，CHECK `~ '^[0-9a-f]{32,64}$'` |
| `fingerprint` | TEXT | NOT NULL DEFAULT `''`，CHECK `= '' OR ~ '^[0-9a-f]{64}$'` |
| `source` | TEXT | NOT NULL DEFAULT `'failure_candidate'`，CHECK `failure_candidate` |
| `expected_revision` | INTEGER | NOT NULL DEFAULT 1，CHECK `> 0`（CAS 状态机） |
| `merged_into_id` | BIGINT | NULL，`REFERENCES delivery_issue(id)`（同上下文自引用；合并目标） |
| `merged_at` | TIMESTAMPTZ | NULL |
| `merged_by` | BIGINT | NULL（逻辑引用 identity） |
| `reopen_count` | INTEGER | NOT NULL DEFAULT 0，CHECK `>= 0` |
| `created_by_run_id` | BIGINT | NULL（逻辑引用 agent_run；确定性创建为 NULL） |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：

- `pk_delivery_issue`；
- `uk_delivery_issue_version_id_dedupe_key` UNIQUE `(version_id, dedupe_key)`（跨波次幂等）；
- CHECK `merged_into_id IS NULL OR merged_into_id <> id`（禁自指；成环由应用层最多 16 跳校验）；
- `idx_delivery_issue_version_id_status_id (version_id, status, id DESC)`；
- `idx_delivery_issue_merged_into_id (merged_into_id)`；
- `idx_delivery_issue_created_at (created_at)`。

**`delivery_issue_ref`**（append-only；`updated_at` 仅初始化；同上下文 FK `issue_id`）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_delivery_issue_ref` PRIMARY KEY |
| `issue_id` | BIGINT | NOT NULL，`REFERENCES delivery_issue(id) ON DELETE CASCADE` |
| `kind` | TEXT | NOT NULL，CHECK `case_run/case_version/wave/step_result/assertion_result/evidence/report/requirement` |
| `ref_id` | BIGINT | NOT NULL（逻辑引用对应实体） |
| `version_sut_id` | BIGINT | NULL（逻辑引用 version） |
| `role` | TEXT | NOT NULL DEFAULT `'source'`，CHECK `char_length <= 32` |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now()（`updated_at` 仅插入初始化） |

约束/索引：

- `pk_delivery_issue_ref`；
- `uk_delivery_issue_ref_issue_id_kind_ref_id_role` UNIQUE `(issue_id, kind, ref_id, role)`；
- `idx_delivery_issue_ref_kind_ref_id (kind, ref_id)`；
- `idx_delivery_issue_ref_issue_id_id (issue_id, id)`。
- 每个 issue 的 refs 写侧上限 20；重复扫描/事件 `ON CONFLICT DO NOTHING` 幂等。

**`delivery_issue_status_history`**（append-only；`updated_at` 仅初始化；同上下文 FK `issue_id`）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_delivery_issue_status_history` PRIMARY KEY |
| `issue_id` | BIGINT | NOT NULL，`REFERENCES delivery_issue(id) ON DELETE CASCADE` |
| `from_status` | TEXT | NULL（首次创建为 NULL），CHECK `NULL OR open/confirmed/dismissed/merged` |
| `to_status` | TEXT | NOT NULL，CHECK `open/confirmed/dismissed/merged` |
| `reason` | TEXT | NOT NULL DEFAULT `''`，CHECK `char_length <= 2000` |
| `actor_id` | BIGINT | NULL（逻辑引用 identity） |
| `actor_kind` | TEXT | NOT NULL，CHECK `user/agent/system` |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now()（`updated_at` 仅插入初始化） |

约束/索引：`pk_delivery_issue_status_history`；`idx_delivery_issue_status_history_issue_id_id (issue_id, id)`。

### 10.2 `delivery_0002_report_revision.sql`（delivery 上下文，3 表）

**`delivery_report`**（逻辑引用 `version`/`run_wave`/`delivery_report_revision`/`agent_run`，无跨上下文 FK）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_delivery_report` PRIMARY KEY |
| `version_id` | BIGINT | NOT NULL（逻辑引用 version） |
| `wave_id` | BIGINT | NULL（逻辑引用 run_wave；kind=wave 必填） |
| `kind` | TEXT | NOT NULL，CHECK `wave/version` |
| `current_revision_id` | BIGINT | NULL（逻辑引用 delivery_report_revision） |
| `status` | TEXT | NOT NULL DEFAULT `'draft'`，CHECK `draft/published/stale` |
| `input_fingerprint` | TEXT | NOT NULL DEFAULT `''`，CHECK `= '' OR ~ '^[0-9a-f]{64}$'` |
| `created_by_run_id` | BIGINT | NULL（逻辑引用 agent_run） |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：

- `pk_delivery_report`；
- CHECK `(kind = 'version' AND wave_id IS NULL) OR (kind = 'wave' AND wave_id IS NOT NULL)`；
- partial unique `uk_delivery_report_version_id_wave_id` UNIQUE `(version_id, wave_id) WHERE kind = 'wave'`；
- partial unique `uk_delivery_report_version_id_kind` UNIQUE `(version_id) WHERE kind = 'version'`（同版本至多一个版本总结报告）；
- `idx_delivery_report_version_id_kind_id (version_id, kind, id DESC)`；
- `idx_delivery_report_current_revision_id (current_revision_id)`。
- `wave` 报告发布成功后回填既有 `run_wave.report_id`（不 ALTER 表）。

**`delivery_report_revision`**（内容 append-only；同上下文 FK `report_id`）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_delivery_report_revision` PRIMARY KEY |
| `report_id` | BIGINT | NOT NULL，`REFERENCES delivery_report(id) ON DELETE CASCADE` |
| `revision` | INTEGER | NOT NULL，CHECK `> 0` |
| `status` | TEXT | NOT NULL DEFAULT `'draft'`，CHECK `draft/published/stale` |
| `html_ref` | BIGINT | NULL（逻辑引用 P0 `content_ref.id`；P4 发布落定后必填） |
| `pdf_ref` | BIGINT | NULL（逻辑引用 `content_ref.id`；P4 不写，P7 回填） |
| `input_fingerprint` | TEXT | NOT NULL，CHECK `~ '^[0-9a-f]{64}$'` |
| `as_of` | TIMESTAMPTZ | NOT NULL（生成开始时刻 UTC） |
| `template_version` | TEXT | NOT NULL DEFAULT `'1'`，CHECK `char_length <= 32` |
| `render_status` | TEXT | NOT NULL DEFAULT `'pending'`，CHECK `pending/running/done/failed` |
| `created_by_run_id` | BIGINT | NULL（逻辑引用 agent_run） |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：

- `pk_delivery_report_revision`；
- `uk_delivery_report_revision_report_id_revision` UNIQUE `(report_id, revision)`（`next = COALESCE(MAX(revision),0)+1`）；
- `idx_delivery_report_revision_report_id_revision (report_id, revision DESC)`；
- `idx_delivery_report_revision_html_ref_id (html_ref)`。
- **append-only 约束**：`revision/status/html_ref/input_fingerprint/as_of/template_version` 在发布落定后不可变；创建过程中仅允许一次 `draft→published` 落定（同时写 `html_ref/status/render_status`），发布后仅 `pdf_ref/render_status/updated_at` 可更新。仓储只暴露 `FinalizePublished` 与 `MarkRenderMetadata`，不提供发布后的内容更新方法；集成测试对已发布内容字段的 SQL 更新必须失败（应用层用例覆盖，DB 触发器可选）。
- `html_ref` 指向 `content_ref`：`owner_kind='report_revision'`、`owner_id=revision.id`、`role='report_html'`；`content_object` 存 sha256/size/mime；不写 `paths.report_dir`。

**`delivery_report_ref`**（append-only；`updated_at` 仅初始化；同上下文 FK `report_id`/`report_revision_id`）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_delivery_report_ref` PRIMARY KEY |
| `report_id` | BIGINT | NOT NULL，`REFERENCES delivery_report(id) ON DELETE CASCADE` |
| `report_revision_id` | BIGINT | NOT NULL，`REFERENCES delivery_report_revision(id) ON DELETE CASCADE` |
| `kind` | TEXT | NOT NULL，CHECK `requirement/scenario/case_version/wave/case_run/issue/version_sut` |
| `ref_id` | BIGINT | NOT NULL（逻辑引用对应实体） |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now()（`updated_at` 仅插入初始化） |

约束/索引：

- `pk_delivery_report_ref`；
- `uk_delivery_report_ref_report_revision_id_kind_ref_id` UNIQUE `(report_revision_id, kind, ref_id)`；
- `idx_delivery_report_ref_kind_ref_id (kind, ref_id)`；
- `idx_delivery_report_ref_report_id_id (report_id, id)`。

### 10.3 `delivery_0003_analysis_run.sql`（delivery 上下文，1 表）

**`delivery_analysis_run`**（逻辑引用 `version`/`agent_run`，无跨上下文 FK）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_delivery_analysis_run` PRIMARY KEY |
| `version_id` | BIGINT | NOT NULL（逻辑引用 version） |
| `kind` | TEXT | NOT NULL，CHECK `failure/coverage/triage`（P4 写入方仅 `triage`） |
| `status` | TEXT | NOT NULL DEFAULT `'queued'`，CHECK `queued/running/done/failed/degraded` |
| `dedupe_key` | TEXT | NOT NULL，CHECK `char_length BETWEEN 1 AND 200` |
| `input_ref` | JSONB | NOT NULL DEFAULT `'{}'::jsonb` |
| `output` | JSONB | NOT NULL DEFAULT `'{}'::jsonb` |
| `agent_run_id` | BIGINT | NULL（逻辑引用 agent_run；1:1） |
| `error` | TEXT | NOT NULL DEFAULT `''`，CHECK `char_length <= 2000` |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：

- `pk_delivery_analysis_run`；
- partial unique `uk_delivery_analysis_run_version_id_kind_dedupe_key` UNIQUE `(version_id, kind, dedupe_key) WHERE status IN ('queued','running')`；
- partial unique `uk_delivery_analysis_run_agent_run_id` UNIQUE `(agent_run_id) WHERE agent_run_id IS NOT NULL`（一个 agent_run 至多一条 analysis_run）；
- `idx_delivery_analysis_run_version_id_kind_id (version_id, kind, id DESC)`。
- P4 仅 `issue.triage` 写入；`failure/coverage` 仅保留枚举（P3 已把结果写 `case_run/step/assertion` 与事件，不重复写），P7 如需回填另立契约。

### 10.4 `version_0005_requirement_closure.sql`（version 上下文，1 表）

**`requirement_closure`**（同上下文 FK：`requirement_id`/`requirement_version_id`；逻辑引用 `agent_run`）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_requirement_closure` PRIMARY KEY |
| `requirement_id` | BIGINT | NOT NULL，`REFERENCES requirement(id) ON DELETE CASCADE` |
| `requirement_version_id` | BIGINT | NOT NULL，`REFERENCES requirement_version(id) ON DELETE CASCADE` |
| `close_round` | INTEGER | NOT NULL，CHECK `> 0` |
| `check_result` | JSONB | NOT NULL（服务端重算的闭环校验快照；HLD `checks` 的 DB 字段映射） |
| `evidence_refs` | JSONB | NOT NULL DEFAULT `'[]'::jsonb`（issue/case_run/report 引用） |
| `closed_by_run_id` | BIGINT | NULL（逻辑引用 agent_run） |
| `closed_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |
| `reopen_count` | INTEGER | NOT NULL DEFAULT 0，CHECK `>= 0` |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：

- `pk_requirement_closure`；
- `uk_requirement_closure_requirement_id_close_round` UNIQUE `(requirement_id, close_round)`；
- `idx_requirement_closure_requirement_id_id (requirement_id, id DESC)`；
- `idx_requirement_closure_requirement_version_id (requirement_version_id)`。
- 重开时更新最新 closure 的 `reopen_count + 1`（运营元数据，允许更新）；再次关闭插入 `close_round = 已有最大轮次 + 1`；closure 行不删除。

### 10.5 P4 数据不变量

1. `delivery_issue` 以 `(version_id, dedupe_key)` 幂等；同一失败用例跨波次只允许一条 issue；refs 重复写 `ON CONFLICT DO NOTHING`。
2. issue 状态机：`open → confirmed|dismissed`、`confirmed|dismissed → open`（reopen_count++）、`open|confirmed|dismissed → merged`；`merged` 为 P4 终态，不提供 unmerge/reopen；状态变更必须写 `delivery_issue_status_history`、`expected_revision++`、审计与事件。
3. 合并目标必须同版本、非 merged、非自身、非成环（最多 16 跳）；P4 不迁移源 issue 的 refs（P7 做 alias/迁移）。
4. `delivery_report` 同版本同 `wave_id` 至多一条 wave 报告、至多一条 version 报告（partial unique）；report `kind` 与 `wave_id` 组合由 CHECK 保证。
5. 报告内容 append-only：旧 published revision 的 `html_ref/input_fingerprint/as_of` 不变；新生成只追加 `revision`，`report.current_revision_id` 指向最新 published；发布后只允许更新 `pdf_ref/render_status/updated_at`。
6. `delivery_report_ref` 只能引用本 revision 的输入；`html_ref` 的 content ledger owner 必须为 `report_revision` + `report_html`，下载时校验 owner/role/sha256/size。
7. `delivery_analysis_run` 的 `(version_id, kind, dedupe_key)` 在 queued/running 期间唯一；`agent_run_id` 非空时全局唯一（1:1）。
8. `requirement_closure` 每次通过写一行，`(requirement_id, close_round)` 唯一且轮次递增；`check_result` 为服务端唯一真值快照；closure 行不物理删除，重开只递增最新行的 `reopen_count`。
9. 报告 HTML 只入 P0 内容账本（`content_ref`，`owner_kind='report_revision'`, `role='report_html'`）；不写 `paths.report_dir`、不 base64 入库；`pdf_ref` P4 恒 NULL，PDF 属 P7。
10. 既有表只使用不改结构：`version.closed_at/close_reason/close_run_id`（P4 写入/清空）、`run_wave.report_id`（P4 回填 wave 报告）、`content_ref`（报告 HTML）、`human_request`（待办只读）、`requirement.status`（新增 `closed` 写入与 `closed→draft` 重开路径，`obsolete` 只读）、`work_item`（新增 5 个任务类型，不改表结构）。

---

## 11. P5 codeintel 增量表结构（`frozen: P5`，2026-11-01）

> 来源：`docs/superpowers/specs/2026-11-01-eztest-v02-p5-codeintel-visual-design.md` §3.2–§3.4（P5-D-02/P5-D-06）。
> 迁移：`codeintel_0001_code_index.sql`（#25）、`codeintel_0002_code_analysis.sql`（#26）、`codeintel_0003_anchor_review.sql`（#27）。
> P0–P4 表不变（不 ALTER）；8 张新表全部雪花 `BIGINT` 主键（应用层生成），JSON/API 中 ID 为十进制字符串，时间 `TIMESTAMPTZ`（RFC3339 UTC）。
> 8 表均有 `created_at`/`updated_at`（D34 §6.9.4 字面口径）。**append-only**：`code_fact` 内容版本只追加（`superseded_at` 标记历史，不物理删除、不更新内容），`code_fact_review` 的 `updated_at` 仅插入时初始化（仓储不提供更新方法）。
> 跨上下文只做逻辑引用、不建 FK（`repo_id`/`version_id`/`analysis_id`/`entity_id`/`subject_*`）；同上下文 FK 显式。约束/索引命名 `pk_<表名>`、`uk_<表名>_<全列名>`、`idx_<表名>_<全列名>`；partial unique 与表达式唯一键用唯一索引实现，谓词/表达式写在 `WHERE`/列表达式中。布尔列沿用 P0–P4 既有口径（`truncated`/`degraded`/`environment_match`/`selected` 不加 `is_` 前缀，P5-D-02 的 D34 显式例外）。

### 11.1 `codeintel_0001_code_index.sql`（codeintel 上下文，3 表）

**`code_index_run`**（T0 索引运行；逻辑引用 `version`/`resource.code_repository`/`identity`，无跨上下文 FK；同上下文 FK 无）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_code_index_run` PRIMARY KEY（雪花） |
| `version_id` | BIGINT | NULL（触发版本；逻辑引用 version；全局后台索引 P6/P7 不建） |
| `repo_id` | BIGINT | NOT NULL（逻辑引用 resource.code_repository） |
| `commit_sha` | TEXT | NOT NULL，CHECK `~ '^[0-9a-f]{40}$'` |
| `branch` | TEXT | NOT NULL DEFAULT `''`，CHECK `char_length <= 255` |
| `source` | TEXT | NOT NULL DEFAULT `'git'`，CHECK `git` |
| `status` | TEXT | NOT NULL DEFAULT `'queued'`，CHECK `queued/running/done/failed` |
| `files_total` | INTEGER | NOT NULL DEFAULT 0，CHECK `>= 0` |
| `files_indexed` | INTEGER | NOT NULL DEFAULT 0，CHECK `>= 0` |
| `truncated` | BOOLEAN | NOT NULL DEFAULT false |
| `stats` | JSONB | NOT NULL DEFAULT `'{}'::jsonb`，CHECK `jsonb_typeof = 'object'` |
| `errors` | JSONB | NOT NULL DEFAULT `'[]'::jsonb`，CHECK `jsonb_typeof = 'array'`（应用限 ≤20 条） |
| `started_at` / `finished_at` | TIMESTAMPTZ | NULL |
| `created_by` | BIGINT | NULL（逻辑引用 identity） |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：

- `pk_code_index_run`；
- partial unique **索引** `uk_code_index_run_active` UNIQUE `(repo_id) WHERE status IN ('queued','running')`（同一仓库同时至多一个活跃索引）；
- `idx_code_index_run_repo (repo_id, created_at DESC)`；
- `idx_code_index_run_version (version_id, created_at DESC)`。

**`code_entity`**（当前可查询实体/事实投影；`index_run_id` 为同上下文 FK，`repo_id`/`analysis_id` 逻辑引用）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_code_entity` PRIMARY KEY |
| `repo_id` | BIGINT | NOT NULL（逻辑引用 resource.code_repository） |
| `commit_sha` | TEXT | NOT NULL，CHECK `~ '^[0-9a-f]{40}$'` |
| `index_run_id` | BIGINT | NULL，`REFERENCES code_index_run(id) ON DELETE SET NULL` |
| `kind` | TEXT | NOT NULL，CHECK `file/module/route/api/client_call/component/page/symbol` |
| `path` | TEXT | NOT NULL，CHECK `char_length BETWEEN 1 AND 4096` |
| `name` | TEXT | NOT NULL DEFAULT `''`，CHECK `char_length <= 512` |
| `signature` | TEXT | NOT NULL DEFAULT `''`，CHECK `char_length <= 2000` |
| `start_line` / `end_line` | INTEGER | NULL；CHECK `start_line IS NULL OR start_line >= 1`；CHECK `end_line IS NULL OR (start_line IS NOT NULL AND end_line >= start_line)` |
| `blob_hash` | TEXT | NOT NULL DEFAULT `''`，CHECK `'' OR ^git-sha1:<40hex>$ OR ^sha256:<64hex>$` |
| `source` | TEXT | NOT NULL，CHECK `git/ai` |
| `confidence` | TEXT | NOT NULL DEFAULT `'high'`，CHECK `low/medium/high` |
| `evidence` | JSONB | NOT NULL DEFAULT `'[]'::jsonb`，CHECK `jsonb_typeof = 'array'`（应用限 ≤8 条） |
| `meta` | JSONB | NOT NULL DEFAULT `'{}'::jsonb`，CHECK `jsonb_typeof = 'object'` |
| `fact_key` | TEXT | NOT NULL DEFAULT `''`，CHECK `char_length <= 512` |
| `fact_version` | INTEGER | NOT NULL DEFAULT 1，CHECK `>= 1` |
| `analysis_id` | BIGINT | NULL（逻辑引用 code_analysis） |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：

- `pk_code_entity`；
- partial unique **索引** `uk_code_entity_file` UNIQUE `(repo_id, commit_sha, path) WHERE kind='file' AND source='git'`；
- partial unique **索引** `uk_code_entity_fact` UNIQUE `(repo_id, commit_sha, fact_key) WHERE fact_key <> ''`；
- `idx_code_entity_kind (repo_id, commit_sha, kind, path)`；
- `idx_code_entity_path (repo_id, path)`；
- `idx_code_entity_analysis (analysis_id)`。

**`code_edge`**（实体关系；同上下文 FK 指向 `code_entity`，`analysis_id` 逻辑引用）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_code_edge` PRIMARY KEY |
| `repo_id` | BIGINT | NOT NULL（逻辑引用 resource.code_repository） |
| `commit_sha` | TEXT | NOT NULL，CHECK `~ '^[0-9a-f]{40}$'` |
| `analysis_id` | BIGINT | NULL（逻辑引用 code_analysis） |
| `src_entity_id` / `dst_entity_id` | BIGINT | NOT NULL，`REFERENCES code_entity(id) ON DELETE CASCADE` |
| `rel` | TEXT | NOT NULL，CHECK `contains/imports/defines/calls/renders/route_to_api` |
| `source` | TEXT | NOT NULL，CHECK `git/ai` |
| `confidence` | TEXT | NOT NULL DEFAULT `'high'`，CHECK `low/medium/high` |
| `evidence` | JSONB | NOT NULL DEFAULT `'[]'::jsonb`，CHECK `jsonb_typeof = 'array'` |
| `meta` | JSONB | NOT NULL DEFAULT `'{}'::jsonb`，CHECK `jsonb_typeof = 'object'` |
| `fact_key` | TEXT | NOT NULL DEFAULT `''`，CHECK `char_length <= 512` |
| `fact_version` | INTEGER | NOT NULL DEFAULT 1，CHECK `>= 1` |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：`pk_code_edge`；`uk_code_edge_src_dst_rel` UNIQUE `(src_entity_id, dst_entity_id, rel)`；`idx_code_edge_src (src_entity_id)`；`idx_code_edge_dst (dst_entity_id)`；`idx_code_edge_analysis (analysis_id)`。

### 11.2 `codeintel_0002_code_analysis.sql`（codeintel 上下文，3 表）

**`code_fact`**（不可变事实版本；`analysis_id` 逻辑引用 code_analysis）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_code_fact` PRIMARY KEY |
| `repo_id` | BIGINT | NOT NULL（逻辑引用 resource.code_repository） |
| `commit_sha` | TEXT | NOT NULL，CHECK `~ '^[0-9a-f]{40}$'` |
| `fact_key` | TEXT | NOT NULL，CHECK `char_length BETWEEN 1 AND 512` |
| `version` | INTEGER | NOT NULL，CHECK `>= 1` |
| `kind` | TEXT | NOT NULL，CHECK 同 `code_entity.kind`（8 值） |
| `path` | TEXT | NOT NULL，CHECK `char_length BETWEEN 1 AND 4096` |
| `name` / `signature` | TEXT | NOT NULL DEFAULT `''`，CHECK `<= 512` / `<= 2000` |
| `start_line` / `end_line` | INTEGER | NULL；同实体行号规则 |
| `blob_hash` | TEXT | NOT NULL DEFAULT `''`，CHECK 同实体 blob 规则 |
| `body` | JSONB | NOT NULL DEFAULT `'{}'::jsonb`，CHECK `jsonb_typeof = 'object'` |
| `evidence` | JSONB | NOT NULL DEFAULT `'[]'::jsonb`，CHECK `jsonb_typeof = 'array'` |
| `source` | TEXT | NOT NULL，CHECK `git/ai` |
| `confidence` | TEXT | NOT NULL DEFAULT `'high'`，CHECK `low/medium/high` |
| `model` | TEXT | NOT NULL DEFAULT `''`，CHECK `char_length <= 128` |
| `prompt_version` | TEXT | NOT NULL DEFAULT `''`，CHECK `char_length <= 64` |
| `analysis_id` | BIGINT | NULL（逻辑引用 code_analysis） |
| `superseded_at` | TIMESTAMPTZ | NULL |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：`pk_code_fact`；`uk_code_fact_key_version` UNIQUE `(repo_id, commit_sha, fact_key, version)`；`idx_code_fact_lookup (repo_id, commit_sha, fact_key, version DESC)`；`idx_code_fact_kind (repo_id, commit_sha, kind, path)`。

**`code_analysis`**（版本 × 仓库的变更分析运行；逻辑引用 `version`/`resource.code_repository`/`identity`）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_code_analysis` PRIMARY KEY |
| `version_id` | BIGINT | NOT NULL（逻辑引用 version） |
| `repo_id` | BIGINT | NOT NULL（逻辑引用 resource.code_repository） |
| `base_commit_sha` / `head_commit_sha` | TEXT | NOT NULL，CHECK `~ '^[0-9a-f]{40}$'` |
| `status` | TEXT | NOT NULL DEFAULT `'queued'`，CHECK `queued/running/done/failed` |
| `degraded` | BOOLEAN | NOT NULL DEFAULT false |
| `model` | TEXT | NOT NULL DEFAULT `''`，CHECK `char_length <= 128` |
| `prompt_version` | TEXT | NOT NULL DEFAULT `''`，CHECK `char_length <= 64` |
| `analysis_version` | INTEGER | NOT NULL DEFAULT 1，CHECK `>= 1` |
| `changed_files` | JSONB | NOT NULL DEFAULT `'[]'::jsonb`，CHECK `jsonb_typeof = 'array'`；应用限 ≤2000；元素 `{path,old_path,status,additions,deletions,binary,head_blob_hash}` |
| `stats` | JSONB | NOT NULL DEFAULT `'{}'::jsonb`，CHECK `jsonb_typeof = 'object'` |
| `unmapped` | JSONB | NOT NULL DEFAULT `'[]'::jsonb`，CHECK `jsonb_typeof = 'array'`；应用限 ≤200；`{path,reason}` |
| `errors` | JSONB | NOT NULL DEFAULT `'[]'::jsonb`，CHECK `jsonb_typeof = 'array'`（应用限 ≤20） |
| `fact_versions` | JSONB | NOT NULL DEFAULT `'{}'::jsonb`，CHECK `jsonb_typeof = 'object'`（fact_key→version 快照，应用限 ≤500） |
| `created_by` | BIGINT | NULL（逻辑引用 identity） |
| `started_at` / `finished_at` | TIMESTAMPTZ | NULL |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：

- `pk_code_analysis`；
- partial unique **索引** `uk_code_analysis_active` UNIQUE `(version_id, repo_id) WHERE status IN ('queued','running')`；
- `idx_code_analysis_version (version_id, repo_id, id DESC)`；
- `idx_code_analysis_status (status, id)`。

**`code_impact_candidate`**（影响候选；`analysis_id` 同上下文 FK，`subject_id` 逻辑引用）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_code_impact_candidate` PRIMARY KEY |
| `analysis_id` | BIGINT | NOT NULL，`REFERENCES code_analysis(id) ON DELETE CASCADE` |
| `subject_kind` | TEXT | NOT NULL，CHECK `requirement/requirement_version/case_version/sut_page/sut_api` |
| `subject_id` | BIGINT | NOT NULL（逻辑引用对应实体） |
| `reason` | TEXT | NOT NULL DEFAULT `''`，CHECK `char_length <= 2000` |
| `confidence` | TEXT | NOT NULL，CHECK `low/medium/high` |
| `source` | TEXT | NOT NULL，CHECK `direct/contract/relation/ai` |
| `evidence` | JSONB | NOT NULL DEFAULT `'[]'::jsonb`，CHECK `jsonb_typeof = 'array'`（应用限 ≤5） |
| `selected` | BOOLEAN | NOT NULL DEFAULT true |
| `selection_updated_by` | BIGINT | NULL（逻辑引用 identity） |
| `selection_updated_at` | TIMESTAMPTZ | NULL |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：`pk_code_impact_candidate`；`uk_code_impact_candidate_subject` UNIQUE `(analysis_id, subject_kind, subject_id)`；`idx_code_impact_candidate_subject (subject_kind, subject_id)`。

### 11.3 `codeintel_0003_anchor_review.sql`（codeintel 上下文，2 表）

**`code_anchor`**（统一代码锚点；逻辑引用 `version`/`resource.code_repository`/`code_entity`/主体实体，无跨上下文 FK）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_code_anchor` PRIMARY KEY |
| `version_id` | BIGINT | NOT NULL（逻辑引用 version） |
| `repo_id` | BIGINT | NOT NULL（逻辑引用 resource.code_repository） |
| `subject_kind` | TEXT | NOT NULL，CHECK `version/requirement/requirement_version/case_version/issue` |
| `subject_id` | BIGINT | NOT NULL（逻辑引用对应实体） |
| `file_path` | TEXT | NOT NULL，CHECK `char_length BETWEEN 1 AND 4096` |
| `start_line` / `end_line` | INTEGER | NULL；同实体行号规则 |
| `entity_id` | BIGINT | NULL（逻辑引用 code_entity） |
| `commit_sha` | TEXT | NULL，CHECK `commit_sha IS NULL OR ~ '^[0-9a-f]{40}$'` |
| `excerpt_sha` | TEXT | NOT NULL DEFAULT `''`，CHECK `'' OR ~ '^sha256:[0-9a-f]{64}$'` |
| `status` | TEXT | NOT NULL DEFAULT `'valid'`，CHECK `valid/stale/broken` |
| `confidence` | TEXT | NOT NULL DEFAULT `'high'`，CHECK `low/medium/high` |
| `created_by_kind` | TEXT | NOT NULL DEFAULT `'user'`，CHECK `user/ai/system` |
| `created_by` | BIGINT | NULL（逻辑引用 identity） |
| `last_validated_at` | TIMESTAMPTZ | NULL |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：

- `pk_code_anchor`；
- unique **表达式索引** `uk_code_anchor_subject_point` UNIQUE `(version_id, subject_kind, subject_id, file_path, COALESCE(start_line,0), COALESCE(end_line,0))`（空行号归一化为 0，避免 NULL 破坏唯一性）；
- `idx_code_anchor_subject (subject_kind, subject_id)`；
- `idx_code_anchor_repo_path (repo_id, file_path)`。

**`code_fact_review`**（append-only 事实复核；`fact_id` 同上下文 FK，`reviewer_id` 逻辑引用 identity）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_code_fact_review` PRIMARY KEY |
| `fact_id` | BIGINT | NOT NULL，`REFERENCES code_fact(id) ON DELETE CASCADE` |
| `fact_version` | INTEGER | NOT NULL，CHECK `>= 1` |
| `repo_id` | BIGINT | NOT NULL（逻辑引用 resource.code_repository） |
| `commit_sha` | TEXT | NOT NULL，CHECK `~ '^[0-9a-f]{40}$'` |
| `verdict` | TEXT | NOT NULL，CHECK `correct/incorrect` |
| `reason` | TEXT | NOT NULL DEFAULT `''`，CHECK `char_length <= 2000` |
| `reviewer_id` | BIGINT | NOT NULL（逻辑引用 identity） |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now()（`updated_at` 仅插入初始化） |

约束/索引：`pk_code_fact_review`；`idx_code_fact_review_fact (fact_id, id DESC)`；`idx_code_fact_review_created (created_at DESC)`。

### 11.4 P5 codeintel 数据不变量

1. `code_index_run` 同一 `repo_id` 至多一个 `queued/running`（partial unique）；终态之后可再次触发；失败/截断不删除已建实体。
2. `code_entity` 的 `(repo_id, commit_sha, path, kind='file', source='git')` 唯一；AI 事实投影按 `fact_key` 唯一（`fact_key <> ''`）；索引刷新可 upsert，不跨 commit 覆盖。
3. `code_edge` 的 `(src_entity_id, dst_entity_id, rel)` 唯一；实体删除级联删除边；跨仓库/跨 commit 不建边。
4. `code_fact` 内容版本只追加：新版本 `version = max+1`，旧版本只写 `superseded_at`，不更新 `body/evidence/kind/path/...`；`(repo_id, commit_sha, fact_key, version)` 唯一。
5. `code_analysis` 同一 `(version_id, repo_id)` 至多一个 `queued/running`；`changed_files`/`unmapped`/`errors`/`fact_versions` 为服务端写侧快照（应用限见 §11.2），不 base64 存正文。
6. `code_impact_candidate` 同一分析内 `(subject_kind, subject_id)` 唯一；`selected` 是用户选择覆盖，`selection_updated_by/at` 记录最近选择；`low` 置信度不自动选中由应用层保证（§4.5）。
7. `code_anchor` 同一版本、主体、文件、行区间只允许一个锚点（表达式唯一索引）；`status` 由校验重算（`valid/stale/broken`），不物理删除历史事实。
8. `code_fact_review` 只追加，不更新/删除；`verdict` 只允许 `correct/incorrect`；`fact_version` 必须与复核的事实版本一致（应用层校验）。
9. 代码正文不落契约表：表只存 path/行号/摘要/证据引用；正文经 `code.read`/`file` 鉴权读取（§4.1）。
10. 迁移只追加：`#25–#27` 不改 `#1–#24`；`codeintel` 上下文不建跨上下文 FK。

---

## 12. P5 visual 增量表结构（`frozen: P5`，2026-11-01）

> 来源：`docs/superpowers/specs/2026-11-01-eztest-v02-p5-codeintel-visual-design.md` §3.5/§7（P5-D-07/P5-D-08）。
> 迁移：`visual_0001_visual_assertion.sql`（#28，visual 上下文，3 表）。
> P0–P4 表不变（不 ALTER）；3 张新表雪花 `BIGINT` 主键，时间 `TIMESTAMPTZ`（RFC3339 UTC）。
> 3 表均有 `created_at`/`updated_at`；**视觉 diff 为 append-only**：`updated_at` 仅插入时初始化，不提供更新方法。`evidence_id`/`case_run_id`/`assertion_result_id`/`actual_evidence_id`/`diff_evidence_id` 均为执行/存储上下文的逻辑引用（不建 FK）；`visual_baseline.visual_assertion_id` 与 `visual_diff.visual_assertion_id` 为同上下文 FK。

### 12.1 `visual_0001_visual_assertion.sql`（visual 上下文，3 表）

**`visual_assertion`**（视觉断言配置；逻辑引用 `version`/`design.case_version`/`design.case_assertion`/`version.version_sut`）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_visual_assertion` PRIMARY KEY |
| `version_id` / `case_version_id` / `case_assertion_id` | BIGINT | NOT NULL（逻辑引用对应实体） |
| `step_seq` / `assertion_seq` | INTEGER | NOT NULL，CHECK `> 0` |
| `version_sut_id` | BIGINT | NOT NULL（逻辑引用 version_sut） |
| `selector` | TEXT | NOT NULL DEFAULT `''`，CHECK `char_length <= 500` |
| `region` | JSONB | NOT NULL DEFAULT `'{}'::jsonb`，CHECK `jsonb_typeof = 'object'`（`{x,y,width,height}` 或空） |
| `threshold` | NUMERIC(6,5) | NOT NULL DEFAULT 0.005，CHECK `0..0.1` |
| `viewport_width` / `viewport_height` | INTEGER | NOT NULL DEFAULT 1280 / 720，CHECK `1..10000` |
| `device_scale_factor` | NUMERIC(4,2) | NOT NULL DEFAULT 1.0，CHECK `0.5..4` |
| `wait_policy` | JSONB | NOT NULL DEFAULT `'{}'::jsonb`，CHECK `jsonb_typeof = 'object'` |
| `config_revision` | INTEGER | NOT NULL DEFAULT 1，CHECK `>= 1` |
| `status` | TEXT | NOT NULL DEFAULT `'active'`，CHECK `active/archived` |
| `created_by` | BIGINT | NULL（逻辑引用 identity） |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：`pk_visual_assertion`；`uk_visual_assertion_case_assertion` UNIQUE `(case_assertion_id)`；`idx_visual_assertion_version (version_id, status)`；`idx_visual_assertion_case_version (case_version_id)`；`idx_visual_assertion_version_sut (version_sut_id)`。

**`visual_baseline`**（已批准基线历史；`visual_assertion_id` 同上下文 FK，`evidence_id` 逻辑引用 execution.evidence）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_visual_baseline` PRIMARY KEY |
| `visual_assertion_id` | BIGINT | NOT NULL，`REFERENCES visual_assertion(id) ON DELETE CASCADE` |
| `case_version_id` / `version_sut_id` | BIGINT | NOT NULL（逻辑引用） |
| `evidence_id` | BIGINT | NOT NULL（逻辑引用 execution.evidence） |
| `sha256` | TEXT | NOT NULL，CHECK `~ '^[0-9a-f]{64}$'` |
| `size_bytes` | BIGINT | NOT NULL，CHECK `> 0` |
| `width` / `height` | INTEGER | NOT NULL，CHECK `> 0` |
| `viewport_width` / `viewport_height` | INTEGER | NOT NULL，CHECK `> 0` |
| `device_scale_factor` | NUMERIC(4,2) | NOT NULL，CHECK `0.5..4` |
| `config_revision` | INTEGER | NOT NULL，CHECK `>= 1` |
| `environment_fingerprint` | TEXT | NOT NULL，CHECK `~ '^[0-9a-f]{64}$'` |
| `status` | TEXT | NOT NULL DEFAULT `'approved'`，CHECK `approved/superseded` |
| `approved_by` | BIGINT | NOT NULL（逻辑引用 identity） |
| `approved_at` | TIMESTAMPTZ | NOT NULL |
| `superseded_at` | TIMESTAMPTZ | NULL |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：

- `pk_visual_baseline`；
- partial unique **索引** `uk_visual_baseline_approved` UNIQUE `(visual_assertion_id) WHERE status='approved'`（每个断言至多一个已批准基线）；
- `idx_visual_baseline_assertion (visual_assertion_id, id DESC)`；
- `idx_visual_baseline_evidence (evidence_id)`。

**`visual_diff`**（每次比对结果；append-only；`visual_assertion_id`/`baseline_id` 同上下文 FK）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_visual_diff` PRIMARY KEY |
| `visual_assertion_id` | BIGINT | NOT NULL，`REFERENCES visual_assertion(id) ON DELETE CASCADE` |
| `case_run_id` / `assertion_result_id` | BIGINT | NOT NULL（逻辑引用 execution） |
| `baseline_id` | BIGINT | NULL，`REFERENCES visual_baseline(id) ON DELETE SET NULL` |
| `actual_evidence_id` | BIGINT | NOT NULL（逻辑引用 execution.evidence） |
| `diff_evidence_id` | BIGINT | NULL（逻辑引用 execution.evidence） |
| `diff_ratio` | NUMERIC(8,6) | NOT NULL DEFAULT 0，CHECK `0..1` |
| `threshold` | NUMERIC(6,5) | NOT NULL，CHECK `0..0.1` |
| `classification` | TEXT | NOT NULL，CHECK `passed/failed/inconclusive/blocked` |
| `reason` | TEXT | NOT NULL DEFAULT `''`，CHECK `char_length <= 2000` |
| `environment_match` | BOOLEAN | NOT NULL DEFAULT true |
| `width` / `height` | INTEGER | NOT NULL DEFAULT 0，CHECK `>= 0` |
| `mismatch_boxes` | JSONB | NOT NULL DEFAULT `'[]'::jsonb`，CHECK `jsonb_typeof = 'array'`（应用限 ≤50） |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now()（append-only，`updated_at` 仅初始化） |

约束/索引：`pk_visual_diff`；`uk_visual_diff_assertion_result` UNIQUE `(assertion_result_id)`；`idx_visual_diff_run (case_run_id, id DESC)`；`idx_visual_diff_assertion (visual_assertion_id, id DESC)`；`idx_visual_diff_classification (classification, created_at DESC)`。

### 12.2 P5 visual 数据不变量

1. `visual_assertion` 以 `case_assertion_id` 唯一（一个设计断言至多一条视觉配置）；`PUT` upsert 时 `config_revision++`，并 supersede 旧 `approved` 基线（同事务）。
2. `visual_baseline` 每个断言至多一个 `approved`（partial unique）；审批新基线把旧 approved 置 `superseded` 并写 `superseded_at`，历史行不删除。
3. `visual_diff` 每个 `assertion_result_id` 至多一条（同一断言结果不重复记 diff）；append-only，不更新/删除。
4. 四态：`passed/failed/inconclusive/blocked`；`blocked` 只在通用执行错误出现（assertion verdict 仍为 `inconclusive` 的契约见 §7.3）；`environment_match=false` 不返回 diff 图且不把环境不一致当失败。
5. 视觉 actual/diff 字节只经内容账本（`content_ref`，execution 写，`evidence.kind=visual_actual/visual_diff`）；表只存 evidence id/sha256/size/尺寸/指纹。
6. 基线批准 admin-only；`evidence_id` 必须属于同 `case_version`/`version_sut` 且尺寸/指纹/配置一致，否则 422（§4.2 #33）。
7. 迁移只追加：`#28` 不改 `#1–#27`；visual 上下文不建跨上下文 FK。

---

## 13. P5 跨上下文增量（`frozen: P5`，2026-11-01）

> P5 只对既有表做两处 CHECK 扩宽与一个可空列；不改既有列语义、不重建表、不动 P0–P4 迁移。

### 13.1 `evidence.kind` 10 值（`execution_0004_visual_evidence.sql`，#29）

- 由 8 值扩为 10 值：`screenshot/snapshot/console/network/http/db/download/text/visual_actual/visual_diff`。
- 实现：`ALTER TABLE evidence DROP CONSTRAINT IF EXISTS evidence_kind_check;` 后 `ADD CONSTRAINT chk_evidence_kind CHECK (kind IN (...10...))`；原内联 CHECK 由 PostgreSQL 自动命名为 `evidence_kind_check`。
- 不新增列、不改既有证据语义；`assertion_result.kind` 已含 `visual`（5 值），P5 不扩。
- 应用侧证据映射/视图枚举同步（`EvidenceView.kind` 增加 `visual_actual`/`visual_diff`；README 计数 10）。
- 视觉证据字节仍写 P0 内容账本（`content_object`/`content_ref`，`owner_kind='evidence'`），不写 `paths.evidence_dir`。

### 13.2 `version_sut.code_baseline_commit`（`version_0006_version_sut_code_baseline.sql`，#30）

- `ALTER TABLE version_sut ADD COLUMN code_baseline_commit TEXT NULL CHECK (code_baseline_commit IS NULL OR code_baseline_commit ~ '^[0-9a-f]{40}$');`
- 版本绑定创建/更新时解析并回填；解析失败保留 NULL；分析 API 对 NULL 返回 `baseline_commit_missing`（422）。
- 不新增表；API `Binding`（VersionSUT 视图）增加可选 `code_baseline_commit`。
- P3 的 `execution_intent.code_baseline` 语义在应用层升级为 commit 列表哈希（P5-D-11），不改列。

### 13.3 `work_item.team` 6 值（`agent_0004_code_team.sql`，#31）

- 由 5 值扩为 6 值：`clarification/design/execution/delivery/steward/code`。
- P7 的 `agent_0006_ops_team.sql`（#40）在 6 值上追加 `ops`，见 §16.7；`ops` 只承载确定性 worker 任务（`report.render_pdf`），agent worker 仍只领取 5 个产品团队。
- 实现：`ALTER TABLE work_item DROP CONSTRAINT IF EXISTS work_item_team_check;` 后 `ADD CONSTRAINT chk_work_item_team CHECK (team IN (...6...))`；原内联 CHECK 由 PostgreSQL 自动命名为 `work_item_team_check`。
- `work_item.version_id` 保持 NOT NULL（codeintel 任务必须版本作用域，B1a）；`team_room_post`/`team_room_digest` 的 team CHECK 不变（P5 不为 `code` 团队建频道）。
- 任务类型 `index.repo`/`code.analyze` 由应用注册（`task-types.md` §7），不改表结构。

### 13.4 P5 既有表使用边界

1. `code_repository`（resource）：只读使用 `path/branch/include/exclude`；索引状态由 `code_index_run` 派生，不改列。
2. `version_sut`：除 `code_baseline_commit` 新列外不改；`code_repo_id` 决定可分析的仓库。
3. `case_assertion`/`case_source`：不改；P5 读取 visual kind/code source，锚点写 `code_anchor`。
4. `execution_intent`/`case_run`/`assertion_result`/`evidence`：除证据 kind 外不改；`code_baseline` 语义升级在应用层。
5. `work_item`：除 team CHECK 扩宽外不改；新增任务类型由应用注册。
6. `content_ref`/`content_object`：visual actual/diff 字节复用；`owner_kind` 保持 `evidence`（由 execution 写）。
7. `delivery_issue_ref`：不改枚举；代码锚点统一写 `code_anchor`（P5-D-06）。
8. `sut_page`/`sut_api`：不改列；P5 允许写 `status=proposed`、`source=ai|code`（P0 服务增量支持，§7.1/§8.1）。
9. 跨上下文一律逻辑引用，不建 FK；同上下文 FK 显式命名。
10. 迁移只追加：`#29–#31` 是 P5 对既有表的全部结构变更；P0–P4 表/字段/迁移不改。

---

## 14. P6 quality 增量表结构（`frozen: P6`，2026-11-01）

> 来源：`docs/superpowers/specs/2026-11-01-eztest-v02-p6-parity-quality-search-agent-center-design.md` §3.2/§5/§14。
> 迁移：`quality_0001_eval.sql`（#32，quality 上下文，2 表）。
> P0–P5 表不变（不 ALTER）；2 张新表雪花 `BIGINT` 主键（应用生成，不 BIGSERIAL），时间 `TIMESTAMPTZ`（API 为 RFC3339 UTC）。
> quality 上下文只写自己的两张表；运营指标通过 application 端口聚合，不直读他域表。

### 14.1 `eval_run`

**`eval_run`**（终态评测 run；真实 LLM 报告轨）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_eval_run` PRIMARY KEY（雪花） |
| `mode` | TEXT | NOT NULL，CHECK `mode = 'real_llm'`（P6 不实现 fake-LLM） |
| `corpus_version` | TEXT | NOT NULL，CHECK `corpus_version <> ''` |
| `model` | TEXT | NOT NULL DEFAULT `''`（真实模型名，如 `deepseek-flash`） |
| `status` | TEXT | NOT NULL，CHECK `running/passed/failed/error` |
| `summary` | JSONB | NOT NULL DEFAULT `'{}'::jsonb`，CHECK `jsonb_typeof(summary) = 'object'` |
| `started_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |
| `finished_at` | TIMESTAMPTZ | NULL |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：

- `pk_eval_run`；
- `idx_eval_run_created_at (created_at DESC, id DESC)`（列表/最新摘要按时间倒序）。

### 14.2 `eval_metric`

**`eval_metric`**（run 的 6+1 指标；`run_id` 为同上下文 FK）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `run_id` | BIGINT | NOT NULL，`REFERENCES eval_run(id) ON DELETE CASCADE` |
| `name` | TEXT | NOT NULL，CHECK `name <> ''`（服务端只接受 §5.3 指标名，DB 不硬编码枚举） |
| `value` | NUMERIC(8,5) | NOT NULL |
| `threshold` | NUMERIC(8,5) | NOT NULL（真实 LLM 报告阈值，不阻断阶段验收） |
| `passed` | BOOLEAN | NOT NULL |
| `detail` | JSONB | NOT NULL DEFAULT `'{}'::jsonb`，CHECK `jsonb_typeof(detail) = 'object'` |
| `created_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：

- `pk_eval_metric` PRIMARY KEY `(run_id, name)`；
- `fk_eval_metric_run` FOREIGN KEY `(run_id) REFERENCES eval_run(id) ON DELETE CASCADE`；
- `idx_eval_metric_run (run_id, name)`。

### 14.3 P6 quality 数据不变量

1. 不新增 `baseline` 列：基线值写 `detail.baseline`（与 0.1 DDL 一致；0.1 detailed-design 的 baseline 列与 DDL 不一致，以 DDL 为准）。
2. `eval_metric.name` 不在 DB 硬编码枚举；服务端只接受 §5.3 的 7 个指标名，未知名称拒绝。
3. 指标由服务端从样本计数派生；客户端传入的 passed/threshold/verdict 被忽略；`detail` 必含 `numerator/denominator/threshold_passed/regressed/reason`，有 baseline 时含 `baseline`。
4. `eval_run.status` 除请求声明 `error` 外由指标派生（全 passed→passed，否则 failed）；测试项写操作审计走 `audit_log`（不新增事件/触发器）。
5. 评测样本物理约束（非负、`covered<=confirmed`、`impact_hit<=impact_expected`、`impact_hit<=impact_selected`、`correct<=injected`、`true_positive<=ai_issues`、`supplement_hits<=supplement_cases`、`visual_correct<=visual_injected`）在服务端校验，违反 400 `invalid_argument`；表只持久化已校验结果。
6. 迁移只追加：`#32` 不改 `#1–#31`；quality 上下文不建跨上下文 FK。

---

## 15. P6 agent 提示词版本增量（`frozen: P6`，2026-11-01）

> 来源：spec §3.3/§8/§14。迁移：`agent_0005_profile_prompt_revision.sql`（#33，agent 上下文，1 表）。
> P6 **不新增** `agent_profile.current_prompt_id`/`prompt_version` 列：`agent_profile.system_prompt` 仍是运行时唯一“当前真源”，读取路径零改动；当前版本 = `max(version)`。
> `agent_profile` 表结构不变（只写 `updated_at` 数据）；历史 revision 只追加、不更新、不删除。

### 15.1 `agent_profile_prompt_revision`

**`agent_profile_prompt_revision`**（提示词历史；append-only；`profile_key` 同上下文 FK）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PRIMARY KEY（雪花） |
| `profile_key` | TEXT | NOT NULL，`REFERENCES agent_profile(key) ON DELETE CASCADE`（同上下文 FK） |
| `version` | INT | NOT NULL，CHECK `version > 0` |
| `system_prompt` | TEXT | NOT NULL，CHECK `system_prompt <> ''` |
| `note` | TEXT | NULL |
| `source` | TEXT | NOT NULL，CHECK `source IN ('seed','admin','reset')` |
| `actor_id` | BIGINT | NULL（逻辑引用 identity；seed 为 NULL） |
| `created_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：

- `pk_agent_profile_prompt_revision` PRIMARY KEY `(id)`；
- `uk_agent_profile_prompt_revision` UNIQUE `(profile_key, version)`；
- `idx_agent_profile_prompt_revision_key (profile_key, version DESC)`。

### 15.2 版本/回填/回退语义

1. **append-only**：只有 INSERT；PUT/reset/restore 均新增版本，不修改历史行。`version=max(version)+1` 在 `SELECT ... FOR UPDATE` 事务内计算。
2. **seed/回填**：启动 `EnsureProfiles` 后，对无 revision 的 profile 插入 `v1`，内容=当时 DB `system_prompt`，`source=seed`；幂等、不覆盖管理员编辑；不重复插入已有 revision 的 profile。
3. **PUT**：校验（非空/≤32768 runes/含 `[profile:<key>]`/模板语法/变量白名单）→ insert revision(`source=admin`) → 同一事务 `UPDATE agent_profile.system_prompt` + `updated_at`。内容与当前正文相同 no-op：不插版本、返回 `changed=false`；`expected_version` 不匹配 409 `state_conflict`。
4. **reset**：追加默认正文（与 `Profiles()` seed 同源，`DefaultPrompt(key)`），`source=reset`。
5. **restore**：追加指定旧版本正文，`source=admin`、`note=restore vN`；不修改历史行。
6. **当前正文**：始终以 `agent_profile.system_prompt` 为准（版本表是历史，不是读路径）；`prompt_version` API 字段=当前 `max(version)`。
7. 迁移只追加：`#33` 不改 `#1–#32`；同上下文 FK 显式，跨上下文只逻辑引用。

### 15.3 `agent_profile.updated_at` 写路径与 P6 新表边界

- `agent_profile` 无触发器：PATCH profile 配置与 PUT/reset/restore prompt 在**同一事务**内显式 `updated_at=now()`；其余读路径不改写。`name`/`enabled`/`key` 不写；`system_prompt` 只经版本端点写。
- **P6 唯一新表 = 3 张**：`eval_run`、`eval_metric`（#32 quality）+ `agent_profile_prompt_revision`（#33 agent）；**不新增其他表**，P6 不对既有表做任何 ALTER/列变更。
- `agent_run` 仅为 Agent 中心观察读模型；P6 默认**不加** `idx_agent_run_profile_key_id`（数据量小，仅当集成性能测试证明需要时由后续阶段追加）。
- `delivery_issue`/`delivery_report`/`delivery_report_revision`/`version` 被全局中心只读复用；不新增表、不加索引，全局列表按 id DESC 走 PK。
- 命名/结构自检：新表主键 `pk_`、唯一键 `uk_`、普通索引 `idx_`；字段 snake_case；时间 RFC3339 UTC；跨上下文不建 FK；迁移文件只属一个上下文（#32 quality、#33 agent）。

## 16. P7 增量表结构（frozen: P7，2026-11-01）

> 来源：`docs/superpowers/specs/2026-11-01-eztest-v02-p7-p9-design.md` §5/§13。
> 迁移：#34–#40（7 个）；新增 bounded context：knowledge（#34）、orchestration（#37）。
> P0–P6 表结构不变；P7 **新增 6 张表**：knowledge 3（`knowledge_item`、`knowledge_revision`、`knowledge_usage`）+ storage 2（`gc_run`、`gc_tombstone`）+ orchestration 1（`api_instance`）；**扩展 4 张既有表**：`delivery_report_revision`（新增 4 列）、`delivery_issue`（新增 2 列）、`sut`（新增 1 列）、`work_item`（team CHECK 6→7 值，#40，不改列）。
> 全部新表雪花 `BIGINT` 主键（应用生成）、`TIMESTAMPTZ`（API 为 RFC3339 UTC）；跨上下文只做逻辑引用、不建 FK；同上下文 FK 显式。

### 16.1 `knowledge_0001_knowledge.sql`（#34，knowledge 上下文，3 表）

**`knowledge_item`**（知识主记录；逻辑引用 `version`/`identity`，无跨上下文 FK）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_knowledge_item` PRIMARY KEY（雪花） |
| `scope` | TEXT | NOT NULL，CHECK `scope IN ('global','system')` |
| `system_key` | TEXT | NULL，CHECK `system_key IS NULL OR (system_key = lower(system_key) AND char_length(system_key) BETWEEN 1 AND 64)`（对应 `sut.family`） |
| `title` | TEXT | NOT NULL，CHECK `char_length(title) BETWEEN 1 AND 200` |
| `content` | TEXT | NOT NULL，CHECK `char_length(content) BETWEEN 1 AND 20000` |
| `tags` | TEXT[] | NOT NULL DEFAULT `'{}'::text[]`，CHECK `cardinality(tags) <= 20` |
| `status` | TEXT | NOT NULL DEFAULT `'draft'`，CHECK `draft/approved/rejected/revoked` |
| `confidence` | NUMERIC(4,3) | NULL，CHECK `confidence IS NULL OR (confidence >= 0 AND confidence <= 1)` |
| `source_version_id` | BIGINT | NULL（逻辑引用 version；晋升来源版本） |
| `promoted_from_version_id` | BIGINT | NULL（逻辑引用 version；缺省等同 `source_version_id`） |
| `revision` | INTEGER | NOT NULL DEFAULT 1，CHECK `revision > 0`（当前 revision；编辑时 +1） |
| `review_reason` | TEXT | NULL，CHECK `review_reason IS NULL OR char_length(review_reason) <= 2000`（reject/revoke 原因） |
| `reviewed_by` | BIGINT | NULL（逻辑引用 identity；最近一次审核人） |
| `reviewed_at` | TIMESTAMPTZ | NULL |
| `revoked_at` | TIMESTAMPTZ | NULL |
| `created_by` | BIGINT | NULL（逻辑引用 identity；AI 提议为触发 run 的 actor，未知时为 NULL） |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |
| `search_vector` | tsvector | GENERATED ALWAYS AS (`to_tsvector('simple', coalesce(title,'') \|\| ' ' \|\| coalesce(content,'') \|\| ' ' \|\| eztest_text_array_to_string(tags))`) STORED；`eztest_text_array_to_string(TEXT[])` 为 #34 创建的 IMMUTABLE SQL 函数（`array_to_string` 非 IMMUTABLE，不能直接用于生成列） |

约束/索引：

- `pk_knowledge_item`；`ck_knowledge_item_scope` CHECK `((scope = 'global' AND system_key IS NULL) OR (scope = 'system' AND system_key IS NOT NULL))`；
- `idx_knowledge_item_scope_status_id (scope, status, id DESC)`（按 scope/status 列表）；
- `idx_knowledge_item_status_id (status, id DESC)`（审核队列/注入扫描）；
- `idx_knowledge_item_source_version (source_version_id) WHERE source_version_id IS NOT NULL`；
- `idx_knowledge_item_search USING GIN (search_vector)`（`websearch_to_tsquery('simple', q)` + `ts_rank`，不做向量检索）；
- 状态机：`draft → approved | rejected`；`approved → revoked`；`revoked → approved`（复审）；`rejected → draft`（编辑后重新提交）。不提供物理删除；`source_stale` 不是列，来源版本不存在时由读取期标注并从注入排除。

**`knowledge_revision`**（追加式内容历史；同上下文 FK `knowledge_id`；spec §5 以字段清单给出，类型/约束在此补全）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_knowledge_revision` PRIMARY KEY（雪花） |
| `knowledge_id` | BIGINT | NOT NULL，`REFERENCES knowledge_item(id) ON DELETE CASCADE`（同上下文 FK） |
| `revision` | INTEGER | NOT NULL，CHECK `revision > 0` |
| `title` | TEXT | NOT NULL，CHECK `char_length(title) BETWEEN 1 AND 200` |
| `content` | TEXT | NOT NULL，CHECK `char_length(content) BETWEEN 1 AND 20000` |
| `tags` | TEXT[] | NOT NULL DEFAULT `'{}'::text[]`，CHECK `cardinality(tags) <= 20` |
| `changed_by` | BIGINT | NULL（逻辑引用 identity；AI 提议写入时为 run actor 或 NULL） |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now()（`updated_at` 仅插入初始化） |

约束/索引：

- `pk_knowledge_revision`；
- `uk_knowledge_revision_knowledge_id_revision` UNIQUE `(knowledge_id, revision)`；
- `idx_knowledge_revision_knowledge_id_id (knowledge_id, id DESC)`；
- **append-only**：仅 INSERT，不 UPDATE/DELETE 历史行；创建/编辑在同一事务内写新 revision，`revision = knowledge_item.revision` 新值。

**`knowledge_usage`**（知识注入记录；逻辑引用 `agent_run`/`work_item`，无跨上下文 FK）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_knowledge_usage` PRIMARY KEY（雪花） |
| `knowledge_id` | BIGINT | NOT NULL，`REFERENCES knowledge_item(id) ON DELETE CASCADE`（同上下文 FK） |
| `agent_run_id` | BIGINT | NULL（逻辑引用 agent_run） |
| `work_item_id` | BIGINT | NULL（逻辑引用 work_item） |
| `token_estimate` | INTEGER | NOT NULL DEFAULT 0，CHECK `token_estimate >= 0`（注入估算 token） |
| `injected_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：

- `pk_knowledge_usage`；
- `idx_knowledge_usage_knowledge_id_id (knowledge_id, id DESC)`；
- 每次注入一条知识记一行；记录失败只写日志，不影响 run（见 `agent-profiles-tools.md` §10.3）。

### 16.2 `storage_0003_gc.sql`（#35，storage 上下文，2 表 + 5 索引）

**`gc_run`**（GC 运行账本；逻辑引用 `identity`，无跨上下文 FK）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_gc_run` PRIMARY KEY（雪花） |
| `mode` | TEXT | NOT NULL，CHECK `mode IN ('dry_run','apply')` |
| `status` | TEXT | NOT NULL DEFAULT `'running'`，CHECK `status IN ('running','completed','failed')` |
| `actor_id` | BIGINT | NULL（逻辑引用 identity；周期 worker 为 NULL） |
| `actor_kind` | TEXT | NOT NULL DEFAULT `'system'`，CHECK `actor_kind IN ('user','system')` |
| `lease_owner` | TEXT | NULL |
| `lease_until` | TIMESTAMPTZ | NULL |
| `lease_epoch` | BIGINT | NOT NULL DEFAULT 0，CHECK `lease_epoch >= 0` |
| `started_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |
| `finished_at` | TIMESTAMPTZ | NULL |
| `scanned_objects` | INTEGER | NOT NULL DEFAULT 0，CHECK `scanned_objects >= 0` |
| `scanned_refs` | INTEGER | NOT NULL DEFAULT 0，CHECK `scanned_refs >= 0` |
| `deleted_objects` | INTEGER | NOT NULL DEFAULT 0，CHECK `deleted_objects >= 0` |
| `deleted_refs` | INTEGER | NOT NULL DEFAULT 0，CHECK `deleted_refs >= 0` |
| `deleted_bytes` | BIGINT | NOT NULL DEFAULT 0，CHECK `deleted_bytes >= 0` |
| `report` | JSONB | NOT NULL DEFAULT `'{}'::jsonb`，CHECK `jsonb_typeof(report) = 'object'`（候选/解码/缺失文件明细） |
| `error` | TEXT | NULL，CHECK `error IS NULL OR char_length(error) <= 4000` |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：

- `pk_gc_run`；
- `uk_gc_run_running` UNIQUE `((true)) WHERE status = 'running'`（同一时间只允许一个 running；第二实例冲突 → 409 `gc_running`）；
- `idx_gc_run_started_at (started_at DESC, id DESC)`（运行历史列表）；
- 运行租约（实现约束，无独立索引）：`CreateRun` 在同一事务内先回收过期 running 行——`lease_until` 已过，或旧行无租约且 `started_at` 早于固定 `GCLease`（15 分钟，900s，代码常量 `storageapp.GCLease`，不新增 settings 键）——将其置 `failed`、写 `finished_at`/`error` 并清空 `lease_owner`/`lease_until`，随后插入新行；只有真正存活的 running 行仍返回 `ErrGCRunning`。终态 `UpdateRun` 同样清空租约。这样一次崩溃/取消不会永久卡住后续 GC。

**`gc_tombstone`**（删除墓碑；append-only；同上下文 FK `run_id`）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_gc_tombstone` PRIMARY KEY（雪花） |
| `run_id` | BIGINT | NOT NULL，`REFERENCES gc_run(id) ON DELETE CASCADE`（同上下文 FK） |
| `object_sha` | TEXT | NOT NULL，CHECK `object_sha ~ '^[0-9a-f]{64}$'` |
| `size_bytes` | BIGINT | NOT NULL，CHECK `size_bytes >= 0` |
| `mime` | TEXT | NOT NULL DEFAULT `'application/octet-stream'` |
| `relative_path` | TEXT | NOT NULL（`paths.data_root` 相对路径；不对外暴露） |
| `reason` | TEXT | NOT NULL，CHECK `reason IN ('unreferenced','temp','superseded_export','orphan_file')` |
| `deleted_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now()（`updated_at` 仅插入初始化） |

约束/索引：`pk_gc_tombstone`；同上下文 FK `run_id → gc_run.id`（`ON DELETE CASCADE`）；`idx_gc_tombstone_run_id (run_id, id DESC)`（运行详情墓碑列表）。

- **内容账本扫描索引**（同属 #35）：`idx_content_ref_live_object ON content_ref (object_id) WHERE deleted_at IS NULL`；`idx_content_object_created_at ON content_object (created_at)`。
- 删除顺序固定：先 soft-delete/删 `content_ref` → 再删 `content_object` 行 → 最后删磁盘文件；删对象前先写 `gc_tombstone`。`dry_run` 不写墓碑、不删任何行/文件，只写 `gc_run.report`。
- 保护集（永不自动删除）：`report_revision` 的 `report_html`/`report_pdf`（旧 revision 保留）、attachment 行与 version 行均存在的 `requirement_attachment`、evidence 行存在的证据、当前（非被替代）`export_artifact`、任何有活 ref 的对象、落在 `retention.gc_grace_hours` 宽限期内的一切内容。

### 16.3 `delivery_0004_report_pdf.sql`（#36，delivery 上下文，ALTER 1 表 + 1 索引）

**`delivery_report_revision` 新增列**（PDF 生成元数据；既有列与 P4 语义不变）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `pdf_status` | TEXT | NOT NULL DEFAULT `'pending'`，CHECK `pdf_status IN ('pending','running','done','failed','disabled')` |
| `pdf_error` | TEXT | NULL，CHECK `pdf_error IS NULL OR char_length(pdf_error) <= 2000` |
| `pdf_attempts` | INTEGER | NOT NULL DEFAULT 0，CHECK `pdf_attempts >= 0`（每次 worker 尝试 +1，成功/失败均计） |
| `pdf_rendered_at` | TIMESTAMPTZ | NULL |

- `idx_delivery_report_revision_pdf_pending ON delivery_report_revision (pdf_status) WHERE pdf_status IN ('pending','failed')`（补齐队列/重试扫描）。
- **不可变量**：发布后仅 `pdf_ref`、`pdf_status`、`pdf_error`、`pdf_attempts`、`pdf_rendered_at`、`updated_at` 可变；`as_of`、`html_ref`、`input_fingerprint`、`template_version` 永远不变。
- `pdf_status` 语义：`pending` 待生成；`running` 已被 render worker 认领；`done` 已写入 PDF 且 `pdf_ref` 非空；`failed` 超过 `report.render_max_attempts` 或不可重试；`disabled` 因 `report.render_pdf=false` 不排队。
- 迁移把**既有已发布 revision** 默认为 `pending` 并纳入 PDF 补齐队列（历史 revision 补齐，不重写 HTML/内容）；`report.render_pdf=false` 时 reconciler 跳过补齐，新 revision 置 `disabled`。
- `pdf_ref` 指向 `content_ref`：`owner_kind='report_revision'`、`owner_id=revision.id`、`role='report_pdf'`；下载校验 owner/role/sha/size，不暴露路径。
- **P4 注记的 P7 生效范围**：P4 §10.2 所述“发布后仅 `pdf_ref/render_status/updated_at` 可更新”在 P7 扩为上述六列；P4 已冻结的其余内容不可变语义不变。

### 16.4 `orchestration_0001_instance.sql`（#37，orchestration 上下文，1 表）

**`api_instance`**（API 副本注册表；无跨上下文 FK）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | NOT NULL，`pk_api_instance` PRIMARY KEY（雪花） |
| `instance_key` | TEXT | NOT NULL，UNIQUE（`hostname:pid` 自然键） |
| `hostname` | TEXT | NOT NULL |
| `pid` | INTEGER | NOT NULL，CHECK `pid > 0` |
| `api_port` | INTEGER | NOT NULL，CHECK `api_port BETWEEN 1 AND 65535` |
| `node_id` | INTEGER | NOT NULL，UNIQUE，CHECK `node_id BETWEEN 0 AND 1023` |
| `status` | TEXT | NOT NULL DEFAULT `'active'`，CHECK `status IN ('active','draining','offline','stopped')` |
| `version` | TEXT | NOT NULL DEFAULT `''` |
| `last_error` | TEXT | NULL |
| `started_at` | TIMESTAMPTZ | NOT NULL |
| `heartbeat_at` | TIMESTAMPTZ | NOT NULL |
| `draining_at` | TIMESTAMPTZ | NULL |
| `stopped_at` | TIMESTAMPTZ | NULL |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：

- `pk_api_instance`；`uk_api_instance_instance_key` UNIQUE `(instance_key)`；`uk_api_instance_node_id` UNIQUE `(node_id)`；
- `idx_api_instance_status_heartbeat (status, heartbeat_at DESC)`；
- 语义：启动插入，`node_id` 优先取 `runtime.snowflake_node_id`，被占用取最小空闲 0–1023；每 `instance.heartbeat_seconds` 更新 `heartbeat_at`；超过 `instance.offline_seconds` 无心跳 → `offline`（node 可回收）；优雅退出置 `stopped` 并释放 node；`drain` 置 `draining`（readiness 503、停止认领新任务、in-flight 完成）；启动/恢复**不得**批量中断其它实例的 running。
- leader 选举使用 PostgreSQL advisory lock（`pg_try_advisory_lock(0x455A5431)`），不落本表；`IsLeader=false` 的副本只跑 workers，不跑 reconciler/scheduler/poller/GC。

### 16.5 `resource_0002_sut_family.sql`（#38，resource 上下文，ALTER 1 表 + 1 索引）

**`sut` 新增列**：

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `family` | TEXT | NULL，CHECK `family IS NULL OR (family = lower(family) AND char_length(family) BETWEEN 1 AND 64)`（系统族键；可多个 SUT 共用） |

- `idx_sut_family ON sut (family) WHERE family IS NOT NULL`。
- 语义：可选、无回填；用于 `knowledge_item.scope='system'` 的 `system_key` 匹配（`system_key = ANY(版本绑定 SUT 的 family)`）；SUT 读/写模型新增可选 `family`，旧值 NULL 不受影响。

### 16.6 `delivery_0005_issue_recurrence.sql`（#39，delivery 上下文，ALTER 1 表）

**`delivery_issue` 新增列**：

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `recurrence_count` | INTEGER | NOT NULL DEFAULT 0，CHECK `recurrence_count >= 0`（复发重定向累计次数） |
| `last_recurred_at` | TIMESTAMPTZ | NULL |

- 复发重定向（同 `version_id,dedupe_key` 命中且源 issue `status='merged'`）沿 `merged_into_id` 链（≤16）找到目标：向目标追加 `role='recurrence'` refs（重复跳过）、`recurrence_count+1`、`last_recurred_at=now`、写状态历史（`from=to=目标状态`、`reason='recurrence'`、`actor_kind='system'`）；源 issue 的 refs/历史不变；链断裂/成环 → 事件 `issue.recurred` 带 `redirect_failed=true` 并保持旧行为（追加到源）。
- `merged_chain` 是读取期沿链计算的 ID 数组（≤16），**不落库**；不新增索引（按既有 `uk_delivery_issue_version_id_dedupe_key` 命中）。

### 16.7 `agent_0006_ops_team.sql`（#40，agent 上下文，ALTER 1 表）

- `work_item.team` 由 6 值扩为 **7 值**：`clarification/design/execution/delivery/steward/code/ops`。
- 实现：`ALTER TABLE work_item DROP CONSTRAINT IF EXISTS chk_work_item_team;`（当前名，`agent_0004_code_team.sql` #31 添加）并兼容 `DROP CONSTRAINT IF EXISTS work_item_team_check;`（原内联 CHECK 自动名），然后 `ADD CONSTRAINT chk_work_item_team CHECK (team IN (...7...))`。
- 语义：`ops` 是确定性 worker 任务团队；P7 `report.render_pdf` 的 `work_item.team='ops'`，由 `RenderWorker` 按 `ClaimForTypes` 领取。P1 agent worker 只领取 5 个产品团队，因此不会领取并误杀该任务（验收 S1 的 P7-BUG-01）。
- 不建 `team_room_post`/`team_room_digest` 的 `ops` 频道；`work_item.version_id` 保持 NOT NULL；只扩宽 CHECK，不改列/数据。

### 16.8 P7 数据不变量

1. **只追加**：#34–#40 不改 #1–#33 的文件与 checksum；P7 新表 6 张、既有表扩展 4 张（含 #40 `work_item.team` CHECK），不新增其他表。
2. **归属**：knowledge（`knowledge_*`，#34）、storage（`gc_run`/`gc_tombstone`，#35）、delivery（`delivery_report_revision` 列，#36；`delivery_issue` 列，#39）、orchestration（`api_instance`，#37）、resource（`sut.family`，#38）、agent（`work_item.team` CHECK，#40）；迁移文件只属一个上下文。
3. **跨上下文只逻辑引用**：`source_version_id`、`promoted_from_version_id`、`created_by`、`reviewed_by`、`agent_run_id`、`work_item_id`、`actor_id` 等不建 FK；同上下文 FK（`knowledge_revision.knowledge_id`、`knowledge_usage.knowledge_id`、`gc_tombstone.run_id`）显式 `ON DELETE CASCADE`。
4. **知识**：`knowledge_item.status` 只允许 §16.1 状态机；reject/revoke 必须带 `review_reason`；`draft/rejected/revoked` 永不注入；`approved` 方可注入；`source_stale` 不建状态/列，读取期过滤；`knowledge_revision` append-only、不做物理删除。
5. **报告 PDF**：`pdf_status='done'` ⇔ `pdf_ref` 非空；`pdf_status='failed'` 后 reconciler 不再自动补；`report.render_pdf=false` 只影响新 revision（`disabled`）与补齐队列，不删已生成 PDF；PDF 生成失败不影响 report published 与版本收尾谓词。
6. **GC**：同一时刻至多一个 `running`；`apply` 必须 `confirm=true`；先写 tombstone 再删；删除顺序 ref → object 行 → 文件；保护集与宽限期优先；`dry_run` 绝不删数据。
7. **问题复发**：`recurrence_count` 单调递增，`last_recurred_at` 只前移；合并链 ≤16 且读取期计算；旧 revision 的 HTML/统计/as_of 不因合并/复发变化。
8. **实例**：`instance_key`、`node_id` 全表唯一；同一 node 至多一个未回收实例；`draining` 由 admin drain/activate 迁移；状态过期为 `offline`，`stopped` 终态；不批量中断。
9. **报告 revision 不可变**：§16.3 列出的内容字段在发布后不可变；P7 的 PDF 元数据更新必须使用 fencing（`pdf_ref` 为空且 `pdf_status IN ('pending','failed','running')`）。
10. 命名/结构自检：新表主键 `pk_`、唯一键 `uk_`、普通索引 `idx_`；字段 snake_case；时间 RFC3339 UTC；JSONB 对象列显式 `jsonb_typeof='object'`；跨上下文不建 FK。
11. **work_item team 7 值**：`clarification/design/execution/delivery/steward/code/ops`；`ops` 只承载确定性 worker 任务（P7 `report.render_pdf`），不建 team room；agent worker 只领取 5 个产品团队，`RenderWorker` 按 type 领取。

