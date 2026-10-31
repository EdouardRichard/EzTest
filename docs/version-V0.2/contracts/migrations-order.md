# EzTest 0.2 P0 迁移顺序与 Checksum 契约

> 状态：`frozen: P0`（P0 阶段末冻结；后续阶段只能追加）。
> 权威来源：`backend/internal/db/migrations/order.txt` 与 `backend/internal/db/migrate.go`。
> 表结构见 `contracts/db-schema.md`。

## 1. 当前迁移清单（P0，共 8 个）

`order.txt` 显式列出并固定以下顺序（settings → identity → llm → eventing → storage → audit → resource → version）：

| 序号 | 迁移文件 | 上下文（ownership） | 创建的表 |
|---|---|---|---|
| 1 | `settings_0001_settings.sql` | settings | `system_setting`、`system_setting_revision` |
| 2 | `identity_0001_identity.sql` | identity | `app_user`、`auth_token` |
| 3 | `llm_0001_llm.sql` | llm | `llm_config`、`llm_model` |
| 4 | `eventing_0001_events.sql` | eventing | `event_channel`、`event` |
| 5 | `storage_0001_content_ledger.sql` | storage | `content_object`、`content_ref` |
| 6 | `audit_0001_audit_log.sql` | audit | `audit_log` |
| 7 | `resource_0001_catalog.sql` | resource | `sut`、`sut_environment`、`sut_page`、`sut_api`、`target_database`、`test_account`、`code_repository` |
| 8 | `version_0001_versions.sql` | version | `version`、`version_sut`、`version_sut_resource` |

`order.txt` 原文（注释行以 `#` 开头，空白行忽略）：

```text
settings_0001_settings.sql
identity_0001_identity.sql
llm_0001_llm.sql
eventing_0001_events.sql
storage_0001_content_ledger.sql
audit_0001_audit_log.sql
resource_0001_catalog.sql
version_0001_versions.sql
```

## 2. 文件命名

```text
<context>_<NNNN>_<description>.sql
```

- `<context>`：bounded context（如 `version`、`resource`；不是 Go 包名）。
- `<NNNN>`：四位序号，从 `0001` 开始；同一上下文内递增，跨上下文不要求全局序号。
- `<description>`：小写蛇形，描述本次变更。
- 每个文件必须被 `order.txt` **恰好列出一次**；执行器在启动时校验：
  - `order.txt` 中无重复行；
  - 所有内嵌 `.sql` 都出现；
  - 列出的数量与 `.sql` 文件数量一致。
  不一致 → 迁移失败、进程不启动。

## 3. 执行顺序与执行器行为

1. 启动流程：读 env → 连 DB → **迁移（advisory lock）** → seed settings → 构造依赖 → 启动 HTTP。
2. 执行器获取 PostgreSQL advisory lock `0x455A5430`（ASCII `"EZT0"`），保证同一数据库同时只有一个迁移者。
3. `CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, checksum TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`。
4. 按 `order.txt` 逐项处理：
   - 已存在 `schema_migrations.version`：
     - checksum 一致 → 跳过（幂等，不重复执行）；
     - checksum 不一致 → 返回 `ErrMigrationChecksum`，中止启动；
   - 不存在 → 在一个**独立事务**中执行该文件的 SQL，然后插入 `schema_migrations(version, checksum)` 并提交。
5. 每个迁移独立事务：失败只回滚当前迁移，不留下半应用状态；已提交迁移不会回滚。
6. 释放 advisory lock。
7. 迁移完成后才 seed 设置/管理员、装配服务。

## 4. Checksum 规则（冻结）

- 算法：**SHA-256**。
- 输入：迁移文件的**原始字节**（`embed.FS` 读取的 `.sql` 内容，未做换行/编码归一化）。
- 输出：**小写 hex** 字符串（64 字符），存入 `schema_migrations.checksum`。
- 已应用迁移的 checksum 必须稳定；任何字节变化（包括结尾换行、注释、空白）都会被视为变更并报错。
- 迁移文件只允许**追加**；禁止修改已应用文件、禁止重排 `order.txt`、禁止删除条目。
- 修改迁移后若需要开发库继续使用：不要改文件，追加新迁移；必要时删除并重建开发库（P0 无 0.1 数据迁移，允许重建）。
- 集成测试使用独立 schema/数据库（`eztest_v02_test`），重复迁移必须保持 `schema_migrations` 不变。

## 5. 追加新迁移的流程

1. 在 `backend/internal/db/migrations/` 新建 `<context>_<NNNN+1>_<desc>.sql`；
2. 在 `order.txt` **末尾**追加文件名（绝不插入中间或重排已有行）；
3. 迁移内部只允许增量变更（`CREATE TABLE`、`ALTER TABLE ... ADD COLUMN` 等）；不得依赖已删除旧列/旧表；
4. 如果要改已有约束/索引，使用新的 `ALTER` 语句或新增索引，不修改历史文件；
5. 更新 `contracts/db-schema.md` 与（如涉及对外行为）`events.md`/`openapi.yaml`；
6. 本地/集成库跑两次迁移，确认第二次为 no-op；必要时用 `EZTEST_TEST_DATABASE_URL` 跑集成测试；
7. 在 handoff 中记录新迁移文件、顺序、备份/回滚影响。

示例（P1 新增迁移，仅示意）：

```text
# order.txt 末尾追加
version_0002_clarification.sql
```

```sql
-- backend/internal/db/migrations/version_0002_clarification.sql
CREATE TABLE clarification_session ( ... );
```

## 5a. P1 迁移清单（`frozen: P1`，2026-10-31）

P0 的 8 个迁移保持原样；P1 在 `order.txt` 末尾追加以下 7 个文件（`backend/internal/db/migrations/`）：

| 序号 | 迁移文件 | 上下文 | 创建/变更 |
|---|---|---|---|
| 9 | `agent_0001_work_item.sql` | agent | `work_item`、`work_item_attempt` |
| 10 | `agent_0002_team_room.sql` | agent | `team_room_post`、`team_room_digest` |
| 11 | `agent_0003_runtime.sql` | agent | `agent_profile`、`agent_run`、`agent_message`、`context_packet` |
| 12 | `version_0002_requirement.sql` | version | `requirement`、`requirement_version`、`requirement_confirmation`、`requirement_relation` |
| 13 | `version_0003_conversation.sql` | version | `conversation`、`conversation_message`、`conversation_summary` |
| 14 | `version_0004_requirement_attachment.sql` | version | `requirement_attachment` |
| 15 | `eventing_0002_event_index.sql` | eventing | `idx_event_channel_created_at`（只加索引） |

`order.txt` 末尾原文：

```text
agent_0001_work_item.sql
agent_0002_team_room.sql
agent_0003_runtime.sql
version_0002_requirement.sql
version_0003_conversation.sql
version_0004_requirement_attachment.sql
eventing_0002_event_index.sql
```

- 上文的 `version_0002_clarification.sql`/`clarification_session` 只是 P0 的历史示例，**P1 不采用**；P1 使用设计表 `conversation`（见 db-schema §7）。
- P1 迁移已在集成测试随机 schema 中重复执行验证；开发库/演示库通过启动迁移前滚。
- 迁移顺序影响：P1 表无跨上下文 FK；`version_0002` 依赖 P0 `version`，`agent_0003` 内部 FK 依赖 `work_item`（同上下文）。回滚方式仍是恢复备份，不提供 down SQL。

## 5b. P2 迁移清单（`frozen: P2`，2026-10-31）

P0/P1 的 15 个迁移保持原样；P2 在 `order.txt` 末尾追加 2 个文件：

| 序号 | 迁移文件 | 上下文 | 创建 |
|---|---|---|---|
| 16 | `design_0001_case_model.sql` | design | `case_scenario`、`case_entity`、`case_version`、`case_step`、`case_assertion`、`case_source`、`case_confirmation`、`design_round`、`design_scope` |
| 17 | `storage_0002_export_artifact.sql` | storage | `export_artifact` |

`order.txt` 末尾追加：

```text
design_0001_case_model.sql
storage_0002_export_artifact.sql
```

- P2 新表命名按 Agent.md §6.9 方案 A；同上下文 FK 显式，跨上下文只逻辑引用。
- 迁移 16 先于 17；两者互不依赖，保持固定顺序便于校验。
- 集成测试断言 17 个迁移、10 张新表、`pk_/uk_/idx_` 名称；重复执行 no-op。
- 开发库/演示库照常启动前滚；回滚仍为恢复备份，不提供 down SQL。

## 6. 回滚 / 备份 / 数据库边界

- P0 **只支持前滚**：没有 down migration，也不提供自动回滚。
- 部署/验收前必须备份目标库（`pg_dump` / 环境快照）；恢复方式是恢复备份，而不是执行 down SQL。
- 开发库：`eztest_v02`；集成测试库：`eztest_v02_test`；二者与 0.1 的 `eztest`/`eztest_test` 隔离。
- P0 不迁移 0.1 数据；空库首启会 seed 默认管理员与设置目录。
- 多实例同时启动时，advisory lock 使只有一个实例执行迁移，其余等待后看到已完成状态并跳过。
- 迁移失败时进程必须**拒绝启动 HTTP**（不得带着半迁移 schema 提供服务）。

## 7. 冻结与变更控制

- 本文件在 P0 冻结；P1+ 只能**追加**文件与 `order.txt` 行，并在本文件新增说明。
- 破坏性变更（改列、改约束、改语义）必须先停止、说明影响面、更新 contracts + 决策记录 + 调用方/测试，再实施。
- 迁移只追加规则同样适用于 `schema_migrations` 的 checksum：历史行不得修改。
