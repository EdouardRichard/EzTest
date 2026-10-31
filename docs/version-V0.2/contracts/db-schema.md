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

## 7. P1 增量表结构（`draft: P1`，阶段末冻结）

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
