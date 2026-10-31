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
