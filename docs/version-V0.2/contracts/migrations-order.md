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

## 5c. P3 增量迁移（`frozen: P3`，2026-11-01）

P0/P1/P2 的 17 个迁移保持原样；P3 在 `order.txt` 末尾追加 3 个文件（第 18–20 个）：

| 序号 | 迁移文件 | 上下文（ownership） | 创建的表 |
|---|---|---|---|
| 18 | `execution_0001_intent_wave.sql` | execution | `execution_intent`、`run_wave`、`case_run`、`case_run_sut_context` |
| 19 | `execution_0002_result_evidence.sql` | execution | `step_result`、`assertion_result`、`evidence`、`evidence_ref` |
| 20 | `execution_0003_executor_human.sql` | execution | `executor_instance`、`human_request` |

`order.txt` 末尾追加：

```text
execution_0001_intent_wave.sql
execution_0002_result_evidence.sql
execution_0003_executor_human.sql
```

- 3 个迁移只追加、不改历史文件；`execution_0001` 内部 FK 依赖同上下文新表（`case_run → run_wave/execution_intent`），`execution_0002` 依赖 `execution_0001`（`step_result/assertion_result/evidence → case_run`，`evidence_ref → evidence`），`execution_0003` 不依赖前两者；固定顺序 18→19→20。
- **partial unique 用唯一索引实现**（PostgreSQL 的 UNIQUE 约束不支持 `WHERE`）：`uk_execution_intent_active`、`uk_run_wave_active` 位于 `pg_indexes`；其余 `uk_*` 为表约束（`pg_constraint`）。
- P3 不新增跨上下文 FK；`version_id`/`case_version_id`/`version_sut_id`/`content_ref_id`/`owner_executor_id` 均为逻辑引用。
- 集成测试断言 20 个迁移、10 张新表、`pk_/uk_/idx_` 名称、两个 partial unique 索引的 `WHERE` 谓词；重复执行迁移为 no-op，checksum 不变。
- `human_request` 属版本域对象，但迁移归 execution 上下文（P3-D-16）；P3 不建 `analysis_run`/`issue`/`report` 表。
- 开发库/演示库照常启动前滚；回滚仍为恢复备份，不提供 down SQL。

## 6. P4 增量迁移（`frozen: P4`，2026-11-01）

P0/P1/P2/P3 的 20 个迁移保持原样；P4 在 `order.txt` 末尾追加 4 个文件（第 21–24 个）：

| 序号 | 迁移文件 | 上下文（ownership） | 创建的表 |
|---|---|---|---|
| 21 | `delivery_0001_issue_lifecycle.sql` | delivery | `delivery_issue`、`delivery_issue_ref`、`delivery_issue_status_history` |
| 22 | `delivery_0002_report_revision.sql` | delivery | `delivery_report`、`delivery_report_revision`、`delivery_report_ref` |
| 23 | `delivery_0003_analysis_run.sql` | delivery | `delivery_analysis_run` |
| 24 | `version_0005_requirement_closure.sql` | version | `requirement_closure` |

`order.txt` 末尾追加：

```text
delivery_0001_issue_lifecycle.sql
delivery_0002_report_revision.sql
delivery_0003_analysis_run.sql
version_0005_requirement_closure.sql
```

- 4 个迁移只追加、不改历史文件；固定顺序 21→24。本契约冻结文件名、顺序与 ownership；迁移 SQL 与 `order.txt` 由 P4 迁移任务落盘。
- `delivery_0002` 依赖 `delivery_0001`（`delivery_report_ref → delivery_report/delivery_report_revision`；`delivery_report_revision → delivery_report` 均为同上下文 FK），`delivery_0003` 独立，`version_0005` 依赖 P1 的 `requirement`/`requirement_version`（同上下文 FK）。
- **partial unique 用唯一索引实现**（PostgreSQL 的 UNIQUE 约束不支持 `WHERE`）：`uk_delivery_report_version_id_wave_id`、`uk_delivery_report_version_id_kind`、`uk_delivery_analysis_run_version_id_kind_dedupe_key`、`uk_delivery_analysis_run_agent_run_id` 位于 `pg_indexes`；其余 `uk_*` 为表约束（`pg_constraint`）。
- P4 不新增跨上下文 FK；`version_id`/`wave_id`/`ref_id`/`version_sut_id`/`merged_by`/`created_by_run_id`/`closed_by_run_id`/`content_ref` 均为逻辑引用。
- 8 张新表全部 `delivery_*`（7 张）+ `requirement_closure`（version 家族）；P4 不建 chain 表（chain 为只读读模型，P4-D-15）。
- 集成测试断言 24 个迁移、8 张 P4 表、`pk_/uk_/idx_` 名称与 partial unique 索引谓词；重复执行迁移为 no-op，checksum 不变。
- 开发库/演示库照常启动前滚；回滚仍为恢复备份，不提供 down SQL。

## 7. P5 增量迁移（`frozen: P5`，2026-11-01）

P0/P1/P2/P3/P4 的 24 个迁移保持原样；P5 在 `order.txt` 末尾追加 7 个文件（第 25–31 个）：

| 序号 | 迁移文件 | 上下文（ownership） | 创建/变更 |
|---|---|---|---|
| 25 | `codeintel_0001_code_index.sql` | codeintel | 创建 `code_index_run`、`code_entity`、`code_edge` |
| 26 | `codeintel_0002_code_analysis.sql` | codeintel | 创建 `code_fact`、`code_analysis`、`code_impact_candidate` |
| 27 | `codeintel_0003_anchor_review.sql` | codeintel | 创建 `code_anchor`、`code_fact_review` |
| 28 | `visual_0001_visual_assertion.sql` | visual | 创建 `visual_assertion`、`visual_baseline`、`visual_diff` |
| 29 | `execution_0004_visual_evidence.sql` | execution | `evidence.kind` CHECK 8→10 值（`visual_actual`/`visual_diff`） |
| 30 | `version_0006_version_sut_code_baseline.sql` | version | `version_sut ADD COLUMN code_baseline_commit TEXT NULL` |
| 31 | `agent_0004_code_team.sql` | agent | `work_item.team` CHECK 5→6 值（新增 `code`） |

`order.txt` 末尾追加：

```text
codeintel_0001_code_index.sql
codeintel_0002_code_analysis.sql
codeintel_0003_anchor_review.sql
visual_0001_visual_assertion.sql
execution_0004_visual_evidence.sql
version_0006_version_sut_code_baseline.sql
agent_0004_code_team.sql
```

- 7 个迁移只追加、不改历史文件；固定顺序 25→31。`codeintel_0001` 内部 FK 依赖同上下文新表（`code_entity → code_index_run`、`code_edge → code_entity`）；`codeintel_0002` 的 `code_impact_candidate → code_analysis` 为同上下文 FK；`codeintel_0003` 依赖 `#26` 的 `code_fact`（同上下文 FK）；`visual_0001` 内部 FK（`visual_baseline`/`visual_diff → visual_assertion`、`visual_diff → visual_baseline`）；`execution_0004`/`version_0006`/`agent_0004` 只 ALTER 既有表，依赖 P0/P1/P3 的表已存在。
- **partial unique 与表达式唯一键用唯一索引实现**（PostgreSQL 的 UNIQUE 约束不支持 `WHERE`/`COALESCE` 表达式）：`uk_code_index_run_active`、`uk_code_entity_file`、`uk_code_entity_fact`、`uk_code_analysis_active`、`uk_visual_baseline_approved`、`uk_code_anchor_subject_point` 位于 `pg_indexes`；其余 `uk_*` 为表约束（`pg_constraint`）。
- `execution_0004` 先 `DROP CONSTRAINT IF EXISTS evidence_kind_check`（原内联 CHECK 的自动名）再 `ADD CONSTRAINT chk_evidence_kind`；`agent_0004` 先 `DROP CONSTRAINT IF EXISTS work_item_team_check` 再 `ADD CONSTRAINT chk_work_item_team`。两者只扩宽枚举，不改列/数据。
- P5 不新增跨上下文 FK；`version_id`/`repo_id`/`analysis_id`/`entity_id`/`subject_id`/`evidence_id`/`case_run_id`/`assertion_result_id`/`approved_by`/`created_by`/`reviewer_id` 均为逻辑引用。
- 11 张新表 = codeintel 8（`code_index_run`、`code_entity`、`code_edge`、`code_fact`、`code_analysis`、`code_impact_candidate`、`code_anchor`、`code_fact_review`）+ visual 3（`visual_assertion`、`visual_baseline`、`visual_diff`）；`codeintel`/`visual` 为 P5 新 bounded context。
- 集成测试断言 31 个迁移、11 张 P5 表、二次执行 no-op（`schema_migrations` 不变）；`migrate_test.go` 的 `wantTail` 固定为上述 7 个文件。
- 开发库/演示库照常启动前滚；回滚仍为恢复备份，不提供 down SQL。

## 8. P6 增量迁移（`frozen: P6`，2026-11-01）

P0/P1/P2/P3/P4/P5 的 31 个迁移保持原样；P6 在 `order.txt` 末尾追加 2 个文件（第 32–33 个）：

| 序号 | 迁移文件 | 上下文（ownership） | 创建/变更 |
|---|---|---|---|
| 32 | `quality_0001_eval.sql` | quality | 创建 `eval_run`、`eval_metric` |
| 33 | `agent_0005_profile_prompt_revision.sql` | agent | 创建 `agent_profile_prompt_revision`（append-only 提示词版本） |

`order.txt` 末尾追加：

```text
quality_0001_eval.sql
agent_0005_profile_prompt_revision.sql
```

- 2 个迁移只追加、不改历史文件；固定顺序 32→33（`#32` → `#33`）。`quality_0001` 只建同上下文两张表（`eval_metric.run_id → eval_run.id` 同上下文 FK，`ON DELETE CASCADE`）；`agent_0005` 依赖 P1 的 `agent_profile(key)`（同上下文 FK，`ON DELETE CASCADE`）。
- **P6 新表共 3 张**：quality 2（`eval_run`、`eval_metric`）+ agent 1（`agent_profile_prompt_revision`）；**不新增其他表**，P6 不对既有表做 ALTER/列变更，也不改 P0–P5 迁移的 checksum。
- 主键/约束/索引命名：`pk_eval_run`、`pk_eval_metric`、`fk_eval_metric_run`、`idx_eval_run_created_at`、`idx_eval_metric_run`、`pk_agent_profile_prompt_revision`、`uk_agent_profile_prompt_revision`、`idx_agent_profile_prompt_revision_key`。
- 3 张新表均雪花 `BIGINT` 主键（应用生成）、`TIMESTAMPTZ`（API 为 RFC3339 UTC）；`eval_metric` 主键 `(run_id, name)`；`agent_profile_prompt_revision` 唯一键 `(profile_key, version)`，历史只追加。
- P6 不新增跨上下文 FK；`actor_id` 为 identity 逻辑引用；quality 不直读他域表；agent prompt 版本不新增 `agent_profile.current_prompt_id`/`prompt_version` 列。
- 集成测试断言 **33** 个迁移、3 张 P6 表、**二次执行 no-op**（`schema_migrations` 不变）；`migrate_test.go` 的 `wantTail` 固定为上述 2 个文件、迁移计数 31→33。
- 开发库/演示库照常启动前滚；回滚仍为恢复备份，不提供 down SQL。

## 9. P7 增量迁移（draft: P7，2026-11-01）

P0–P6 的 33 个迁移保持原样；P7 在 `order.txt` 末尾追加 6 个文件（第 34–39 个）：

| 序号 | 迁移文件 | 上下文（ownership） | 创建/变更 |
|---|---|---|---|
| 34 | `knowledge_0001_knowledge.sql` | knowledge（P7 新上下文） | 创建 `knowledge_item`、`knowledge_revision`、`knowledge_usage`；IMMUTABLE 函数 `eztest_text_array_to_string(TEXT[])`；6 个索引（含 GIN 全文） |
| 35 | `storage_0003_gc.sql` | storage | 创建 `gc_run`、`gc_tombstone`；`uk_gc_run_running`；`idx_gc_run_started_at`、`idx_gc_tombstone_run_id`；`idx_content_ref_live_object`、`idx_content_object_created_at` |
| 36 | `delivery_0004_report_pdf.sql` | delivery | `delivery_report_revision` 新增 `pdf_status/pdf_error/pdf_attempts/pdf_rendered_at`；`idx_delivery_report_revision_pdf_pending` |
| 37 | `orchestration_0001_instance.sql` | orchestration（P7 新上下文） | 创建 `api_instance`；`idx_api_instance_status_heartbeat` |
| 38 | `resource_0002_sut_family.sql` | resource | `sut` 新增 `family`；`idx_sut_family` |
| 39 | `delivery_0005_issue_recurrence.sql` | delivery | `delivery_issue` 新增 `recurrence_count/last_recurred_at` |

`order.txt` 末尾追加：

```text
knowledge_0001_knowledge.sql
storage_0003_gc.sql
delivery_0004_report_pdf.sql
orchestration_0001_instance.sql
resource_0002_sut_family.sql
delivery_0005_issue_recurrence.sql
```

- 固定顺序 34→39（knowledge → storage → delivery_pdf → orchestration → resource → delivery_recurrence）；只追加、不改历史文件，`#1–#33` 的 checksum 不变。
- **P7 新表共 6 张**：knowledge 3（`knowledge_item`、`knowledge_revision`、`knowledge_usage`）+ storage 2（`gc_run`、`gc_tombstone`）+ orchestration 1（`api_instance`）；**既有表扩展 3 张**（`delivery_report_revision`、`sut`、`delivery_issue`），不新增其他表。
- 主键/约束/索引命名：`pk_knowledge_item`、`ck_knowledge_item_scope`、`idx_knowledge_item_scope_status_id`、`idx_knowledge_item_status_id`、`idx_knowledge_item_source_version`、`idx_knowledge_item_search`、`pk_knowledge_revision`、`uk_knowledge_revision_knowledge_id_revision`、`idx_knowledge_revision_knowledge_id_id`、`pk_knowledge_usage`、`idx_knowledge_usage_knowledge_id_id`、`pk_gc_run`、`uk_gc_run_running`、`idx_gc_run_started_at`、`pk_gc_tombstone`、`idx_gc_tombstone_run_id`、`idx_content_ref_live_object`、`idx_content_object_created_at`、`idx_delivery_report_revision_pdf_pending`、`pk_api_instance`、`uk_api_instance_instance_key`、`uk_api_instance_node_id`、`idx_api_instance_status_heartbeat`、`idx_sut_family`。
- 全部新表雪花 `BIGINT` 主键（应用生成）、`TIMESTAMPTZ`（API 为 RFC3339 UTC）；`knowledge_revision`/`knowledge_usage` 的同上下文 FK、`gc_tombstone.run_id → gc_run.id` 同上下文 FK 显式；跨上下文只做逻辑引用（版本/identity/agent_run/work_item）。
- 集成测试断言 **39** 个迁移、6 张 P7 新表、`#34–#39` 的尾部顺序、**二次执行 no-op**（`schema_migrations` 不变）；`migrate_test.go` 的 `wantTail` 固定为上述 6 个文件、迁移计数 33→39。
- 开发库/演示库照常启动前滚；回滚仍为恢复备份，不提供 down SQL（见 §10）。

## 10. 回滚 / 备份 / 数据库边界

- P0 **只支持前滚**：没有 down migration，也不提供自动回滚。
- 部署/验收前必须备份目标库（`pg_dump` / 环境快照）；恢复方式是恢复备份，而不是执行 down SQL。
- 开发库：`eztest_v02`；集成测试库：`eztest_v02_test`；二者与 0.1 的 `eztest`/`eztest_test` 隔离。
- P0 不迁移 0.1 数据；空库首启会 seed 默认管理员与设置目录。
- 多实例同时启动时，advisory lock 使只有一个实例执行迁移，其余等待后看到已完成状态并跳过。
- 迁移失败时进程必须**拒绝启动 HTTP**（不得带着半迁移 schema 提供服务）。

## 11. 冻结与变更控制

- 本文件在 P0 冻结；P1+ 只能**追加**文件与 `order.txt` 行，并在本文件新增说明。
- 破坏性变更（改列、改约束、改语义）必须先停止、说明影响面、更新 contracts + 决策记录 + 调用方/测试，再实施。
- 迁移只追加规则同样适用于 `schema_migrations` 的 checksum：历史行不得修改。
