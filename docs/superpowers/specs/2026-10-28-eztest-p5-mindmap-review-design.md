# EzTest P5 用例脑图与评审 — 设计规格

> **状态**：待用户评审（2026-10-28，rev2：范围=主线全量+探索降级；Agent 执行模型=运行时 Agent+单一结构化提交；批准后 `reviewing→executing`；导出=OPML+Markdown+Mermaid+PNG；数据库表名按阿里规约规范化并回改旧表）
> **范围**：实施阶段 P5（`docs/design/implementation-phases.md` §2-P5、§2.9 #15/#21）与 CI-M1 中「设计评审用"代码有、脑图没覆盖"」项（`docs/design/code-intelligence.md` §11.2）
> **设计权威**：`docs/design/system-blueprint.md` v2.2（D1–D33；重点 D2/D5/D9/D17/D19/D29/D30/D31/D32）；本规格拟新增 D34–D38 并同步详细设计（见 §13）
> **配套**：P0/P1/P2/P3/P4 规格；`docs/design/frontend-design.md` §3（PG4）/§4.2/§5.3；`docs/design/code-intelligence.md` §14；`Agent.md` §6（含拟新增 §6.9）
> **性质**：P5 实现依据（范围/架构/DDL/接口/契约/验收），不含任务拆解；任务拆解由 `writing-plans` 产出。

---

## 1. 目标与非目标

### 1.1 目标

把「已确认需求 + 需求关系 + 应用模型 + P4 代码事实」变成**可评审、可编辑、可冻结、可导出**的用例脑图，覆盖后端、4 个设计 Agent、前端全屏脑图页与真实验收：

1. **脑图领域**：`版本 → 需求分组 → 场景 → 用例 → 步骤/断言` 树；用例带优先级、前置、测试数据、来源（人工/AI）、状态；需求↔用例覆盖关系显式可查。
2. **测试设计 Agent**：运行时 Agent（P1 底座）读取需求/关系/应用模型/代码事实，一次运行产出整版脑图提案；服务端校验后**单事务物化**，失败零物化。
3. **设计评审 Agent**：只读脑图与输入，产出五类发现项（覆盖/断言/可执行性/重复/代码有而脑图没覆盖），不改脑图；评审通过/驳回由人决定。
4. **数据/前置规划 Agent（最小）**：为既有用例补前置条件、测试数据与清理建议，并引用 P2 账号/Session；不新建节点。
5. **应用探索 Agent（基础）**：按环境白名单对被测系统做**只读 HTTP 探索**（GET/HEAD），补全应用模型（页面/路由/接口）；无 LLM 时降级为确定性 BFS 抓取；**不引入浏览器**（浏览器探索归 P6）。
6. **评审与基线**：人工编辑与评审；批准后基线冻结（节点只读、`graph_hash` 固化、Version 状态 `reviewing→executing`，见 D35）；驳回回 `designing` 可继续编辑；基线只评审一次（D5）。
7. **锚点**：需求↔用例↔代码/应用模型接口可追溯；代码锚点扩展 `subject_kind='case'`（D37）；P4 影响分析结果只读映射为「受影响用例提示」。
8. **导出**：服务端生成 Markdown、Mermaid、OPML 三类文件（文件系统 + sha256 + 元数据），前端生成 PNG 视图快照（D38）；同一 `(version, format, baseline)` 复用。
9. **实时**：设计/评审/数据规划/探索 run 状态与 `case.*` 事件经 `version:{id}` SSE 实时可见；断线按 seq/Last-Event-ID 补齐。
10. **降级**：AI 不可用时人工建/改/评审/导出全链路可用，主流程不阻塞（P5 退出标准）。
11. **规范落地**：数据库表名按阿里规约单数化（旧表一并改名，字段不变），规约写入 `Agent.md` 全局约定（D34，§3）。

### 1.2 非目标（P5 明确不做）

- 执行引擎、Batch/CaseRun/StepResult、证据、失败分类（P6）；`CaseNode.status='executed'` 仅保留枚举值，P5 不写入。
- AI 自主补用例（`ai_supplement`）与回填脑图、继续中（P7）；P5 仅保留枚举/字段。
- 问题（Issue）与报告（Report）实体（P7）；P5 的评审发现项不等于问题单。
- 浏览器自动化/Playwright/视觉断言/Session 注入执行（P6/P8）；应用探索仅 HTTP 只读。
- 符号级/字段级代码分析、parser（D19）；P5 只消费 P4 事实与锚点。
- PNG 服务端渲染与 XMind 格式（D38 关闭 §17.4：OPML 替代 XMind 作为通用导入格式）。
- 增量脑图自动免评审的开关策略（P7）；P5 一次基线评审。
- 多用户协作编辑/实时协作文档、权限细分 RBAC（P8）。
- 新增 Go 依赖；新增前端图库/表单库/状态库/导出库；服务端图片渲染依赖。

---

## 2. 已确认约束与决策

### 2.1 用户已确认（本轮 brainstorming）

| # | 决策 | 说明 |
|---|---|---|
| U1 | P5 范围 = **主线全量 + 探索降级** | 测试设计/设计评审/数据规划用真实 LLM；应用探索仅 HTTP 只读 + AI 归类，浏览器探索留 P6 |
| U2 | Agent 执行模型 = **运行时 Agent + 单一结构化提交工具** | 4 个 seed profile 走 P1 运行时；只读工具 + 一个 `*_submit` 工具；服务端校验后物化 |
| U3 | 批准后 `reviewing → executing` | 严格按蓝图 §5.6；P5 无批次，UI 标注"基线已批准，等待执行（P6）" |
| U4 | 导出 = **OPML + Markdown + Mermaid + PNG** | 前三种服务端生成落文件系统；PNG 由前端对 SVG 画布光栅化下载 |
| U5 | 数据库表名按阿里规约**规范化并回改旧表** | 单数、小写+下划线、禁保留字、`业务_作用`；旧表字段/约束/既有索引名不变；规约写入全局约定 |

### 2.2 继承约束（不可违反）

- **D2**：管理员可配 Agent 提示词/工具/模型；普通用户只用。P5 的 4 个 profile 走同一配置面。
- **D5**：基线脑图评审一次；批准后不再评审；AI 补充自动并入（P7）。
- **D9**：应用探索可主动爬取/枚举，**只读**、环境白名单。
- **D17/D19/D24**：本地 Git 为唯一代码真源；不做 parser；代码只读事实 + 按需读片段。
- **D29**：新增实体主键雪花 ID，应用层生成；JSON/URL/工具参数为十进制字符串；禁止 DB 自增主键。
- **D30**：业务文件（含导出物）走文件系统，禁止二进制/base64 入 PostgreSQL；DB 只存元数据 + sha256；路径根内校验、原子 rename、0600/0700。
- **D31**：Version 粗粒度容器；需求操作永不改变/回退 `Version.status`；P5 只在设计评审流程内改变状态。
- **D32/D33**：P4 代码事实/证据/缓存/影响分析契约不改；P5 只读消费 `code_*` 工具与 `QueryService`/`ImpactService`。
- **P3 契约**：`version` 上下文仍是 Version/Requirement 唯一写者；脑图经 `VersionLifecycle` 端口请求状态流转。
- **P1 契约**：Agent 会话/run/活动事件/`activity_channel` 语义不变；P5 只新增 profile、工具与 run 协调。
- **错误/分页/幂等**：统一 JSON 错误信封 + `details.reason`（非 problem+json）；游标分页；控制类接口支持 `Idempotency-Key`。
- **事件上限**：`summary ≤500 rune`、`detail ≤64 KiB`、`refs ≤20`（P4 已固化，P5 沿用）。
- **迁移只追加**：已应用迁移与 checksum 不改；新迁移追加到 `order.txt` 末尾；一个迁移文件只属于一个上下文。

### 2.3 本轮新增决策（拟入蓝图，见 §13）

| # | 决策摘要 |
|---|---|
| **D34** | 数据库命名规约（阿里规约 + PostgreSQL 适配）：表名单数/小写/下划线/禁保留字；索引 `pk_/uk_/idx_` 前缀按 PG 全库唯一适配为 `pk_<表>/uk_<表>_<列>/idx_<表>_<列>`；迁移命名 `<context>_<NNNN>_<desc>.sql` 不变；P5 起新表沿用 `created_at/updated_at`（对阿里"三字段"的显式例外）；旧表以新增 rename 迁移改名，字段/约束/既有索引名不变；`schema_migrations` 为框架表例外；规约同步 `Agent.md` §6.9 与主蓝图 §10.2 |
| **D35** | P5 用例脑图与评审：新增 `mindmap` 限界上下文与 9 张 `case_*` 表；树/步骤/断言/来源/评审/基线/run/导出模型；状态流转 `designing→reviewing→executing`（驳回回 `designing`）；基线冻结后只读；评审发现项 blocker 批准需显式确认；4 个 Agent 采用"运行时 Agent + 单一结构化提交 + 服务端校验物化"；代码锚点扩展 `case` 见 D37 |
| **D36** | 应用探索边界：仅 GET/HEAD、同源、环境 `allowed_hosts` 白名单、请求/深度/超时上限、跳过状态变更与登出类链接；管理员触发；产物 `app_model_nodes.source='explored'`；无 LLM 降级为确定性 BFS；不引入浏览器（P6 交付） |
| **D37** | 代码锚点主体扩展 `case`：`code_anchors.subject_kind` CHECK 扩宽，P4 规格中"`case` 预留"正式接入；不改锚点既有字段与唯一索引语义；案例锚点由 mindmap 经 codeintel `AnchorService` 创建/删除 |
| **D38** | 脑图导出：服务端 Markdown / Mermaid（`mindmap` 语法）/ OPML 2.0 三格式落文件系统 + sha256 + 元数据；PNG 由前端对 SVG 画布光栅化下载（视图快照，不入库）；关闭蓝图 §17.4（XMind 非必需） |

---

## 3. 数据库命名规约（D34）与改名映射

### 3.1 规约（写入 `Agent.md` §6.9，此处为规格正文）

1. **表名**：小写字母/数字/下划线；**单数**；禁止保留字；推荐 `业务名称_表的作用`（如 `case_review_finding`）；禁止 `tbl_`/`tb_` 前缀。
2. **字段名**：小写+下划线；布尔用 `is_` 前缀；禁止保留字；P5 **不改**任何既有字段名。
3. **主键**：`id BIGINT`（雪花，D29），不做自增、不做 unsigned 语义（PostgreSQL 无 unsigned）；每表必须有主键。
4. **审计字段**：P5 起新表沿用 `created_at TIMESTAMPTZ NOT NULL DEFAULT now()`、`updated_at TIMESTAMPTZ NOT NULL DEFAULT now()`（对阿里"id/gmt_create/gmt_modified 三字段"的**显式例外**，理由：与本仓库既有字段/Go 映射一致，避免两套时间字段语义）。
5. **索引命名**：`pk_<表名>`（主键约束）、`uk_<表名>_<列...>`（唯一）、`idx_<表名>_<列...>`（普通）。阿里原文的 `pk_字段名`/`uk_字段名` 在 PostgreSQL 下要求 schema 内全库唯一，按此适配落地。
6. **迁移**：`<context>_<NNNN>_<desc>.sql` 只追加；改名/扩宽 CHECK 用新迁移；一个文件只属一个上下文。
7. **外键**：跨上下文不建 FK，只做 BIGINT 逻辑引用；同上下文内 FK 可选。
8. **例外**：`schema_migrations`（迁移框架表，类比 `flyway_schema_history`）不改名；历史阶段规格（P2/P3/P4 spec）作为交付记录保留旧名，主蓝图 §10.2 更新为规范名。

### 3.2 改名映射（旧表 → 新表，字段不变）

| 上下文 | 改名 |
|---|---|
| identity | `users` → **`user_account`**（`user` 为 PG 保留字）、`auth_tokens` → `auth_token` |
| platform | `idempotency_keys` → `idempotency_key` |
| eventing | `events` → `event`、`event_channels` → `event_channel` |
| audit / llm / storage | `audit_logs` → `audit_log`、`llm_configs` → `llm_config`、`storage_objects` → `storage_object` |
| agent | `agent_profiles`→`agent_profile`、`agent_sessions`→`agent_session`、`agent_runs`→`agent_run`、`agent_messages`→`agent_message` |
| resource | `companies`→`company`、`systems`→`system`、`environments`→`environment`、`app_model_nodes`→`app_model_node`、`accounts`→`account`、`account_system_accesses`→`account_system_access`、`sessions`→`session`、`session_snapshots`→`session_snapshot`、`session_leases`→`session_lease`、`manual_takeovers`→`manual_takeover`、`code_repositories`→`code_repository` |
| version | `versions`→`version`、`requirements`→`requirement`、`requirement_relations`→`requirement_relation`、`requirement_attachments`→`requirement_attachment`、`clarification_sessions`→`clarification_session`、`clarification_questions`→`clarification_question`、`decisions`→`decision` |
| codeintel | `code_change_sets`→`code_change_set`、`code_index_runs`→`code_index_run`、`code_entities`→`code_entity`、`code_edges`→`code_edge`、`code_fact_versions`→`code_fact_version`、`code_analyses`→`code_analysis`、`code_impact_candidates`→`code_impact_candidate`、`code_anchors`→`code_anchor`、`code_analysis_cache` 不变 |
| P5 新增 | `case_node`、`case_step`、`case_assertion`、`case_source`、`case_review`、`case_review_finding`、`case_baseline`、`case_design_run`、`case_export` |

### 3.3 改名迁移（只追加，10 条）

`identity_0003_rename_tables.sql`、`platform_0002_rename_tables.sql`、`eventing_0002_rename_tables.sql`、`audit_0002_rename_tables.sql`、`llm_0002_rename_tables.sql`、`storage_0002_rename_tables.sql`、`agent_0003_rename_tables.sql`、`resource_0004_rename_tables.sql`、`version_0003_rename_tables.sql`、`codeintel_0003_rename_tables.sql`。

- 每条只含本上下文 `ALTER TABLE IF EXISTS <old> RENAME TO <new>;` 及对应 `ALTER SEQUENCE IF EXISTS <old>_id_seq RENAME TO <new>_id_seq;`。
- 不重命名既有索引/约束；不 `DROP`/重建；不触碰数据。
- 全新库：先按历史迁移建旧名，再由 rename 迁移改为新名，最终 schema 与既有库一致。
- **不做**：把旧名保留为视图/兼容别名（内部项目无外部消费者；保留别名会让两套名字长期并存）。

### 3.4 迁移清单（P5 全部新增，共 13 条）

| 文件 | 内容 |
|---|---|
| 10 条 `*_rename_tables.sql` | §3.3 |
| `resource_0005_app_model_explored_source.sql` | `app_model_nodes.source` CHECK 扩宽至 `('manual','import','explored')` |
| `codeintel_0004_case_anchor_subject.sql` | `code_anchors.subject_kind` CHECK 扩宽至 `('version','requirement','app_model','case')` |
| `mindmap_0001_cases.sql` | 9 张 `case_*` 表（§5） |

---

## 4. 架构与仓库结构

### 4.1 形态

```text
cmd/eztest/main.go                      ← 唯一组装根：构造 mindmap 服务、注册路由、seed 4 个 profile、注册 case_* 工具、启动 run 协调 worker
internal/mindmap/                        ← 新限界上下文（四层）
  domain/        node/step/assertion/source/review/finding/baseline/run/export 实体与规则；错误；profile 提示词常量
  application/   graph_service（人工 CRUD/树校验）、materialize（提交校验+物化）、review_service、baseline_service、
                 export_service（md/mermaid/opml）、explore_service（HTTP 只读 BFS + AI 归类）、run_service（4 类 run 协调）、ports
  infrastructure/postgres/（9 表仓储）、filestore/（导出文件）、http/（探索 HTTP 客户端）、eventing/（发布）、version/（状态端口适配）、
                 resource/（应用模型/环境/Session 端口适配）、codeintel/（事实/锚点/影响端口适配）、agent/（run 启动/查询端口适配）、audit/
  interfaces/httpapi/  /api/versions/{id}/mindmap/* handler、DTO、路由
```

### 4.2 依赖方向（`Agent.md` §6.2 强制）

- `domain` 仅依赖标准库 + `platform` 纯类型；**禁止**导入 pgx/HTTP/LLM SDK。
- `application` 只依赖本上下文 `domain` + `platform` + 自身 `ports`；**禁止**导入 `infrastructure`/`interfaces`。
- `infrastructure` 实现 `domain`/`ports` 定义的接口；可 import 本上下文 domain/application。
- `interfaces/httpapi` 只调 `application`；DTO 与领域对象分离。
- 跨上下文只经对方 **application 接口**（端口适配放在 `infrastructure/<ctx>`），禁止 import 对方 infrastructure/interfaces，禁止跨上下文共享可变状态。

### 4.3 跨上下文端口（P5 需要新增/复用的 application 接口）

| 端口（mindmap/application/ports.go） | 由谁实现 | 用途 |
|---|---|---|
| `VersionLifecycle` | version application | `GetVersion`、`EnterReview(versionID, actor)`、`RejectReview(versionID, actor)`、`ApproveBaseline(versionID, actor)`；内部校验状态机并发 `version.status_changed` |
| `RequirementReader` | version application | 读已确认需求、关系、优先级；用于上下文组装与覆盖校验 |
| `CatalogGateway` | resource application | `GetEnvironment`（`allowed_hosts`/base URL）、`ListAppModels`、`ImportAppModels`（P5 扩展 `AppModelInput.Source`：默认 `import`，探索传 `explored`） |
| `SessionGateway` | resource application | 读人工导入 Session 的 `storage_state`（只取 cookies 用于 HTTP 探索）、状态校验 |
| `CodeFactsGateway` | codeintel application | `Facts/Search/File/VersionChanges` 只读事实、`Impact.Latest`/`ListCandidates` |
| `CodeAnchorGateway` | codeintel application | `Create/List/Delete`（subject_kind=`case`） |
| `AgentRuntime` | agent application | `EnsureProfile`（幂等 seed 校验）、`CreateSession(profileKey,title,actor,channel,versionID)`、`StartText(sessionID,text,actor)`、`GetSession(sessionID)`（复用 `SessionService.Get` 的 `Running`/`ActiveRun`/`LastRun`，P1 无单独 RunStatus getter）、`Abort(sessionID,actor)`、`HasRunningByChannel` |
| `EventPublisher` | eventing application | 发布 `case.*`/`gate.*`/`version.status_changed` 到 `version:{id}` |
| `IDGenerator`/`Clock`/`Audit` | platform/audit | 雪花 ID、时间、审计 |

> version 侧接口由 P5 追加（`VersionService.EnterReview/RejectReview/ApproveBaseline`），不改既有方法；`version` 仍是 Version 状态唯一写者。

### 4.4 请求流

```text
人工编辑：UI → mindmap httpapi → graph_service（领域校验 + revision OCC）→ postgres → 事件 case.created/updated/deleted
AI 生成：UI POST /mindmap/runs{kind=design} → run_service（创建 case_design_run=queued）→ agent.StartRun(profile=case-design, channel=version:{id})
        → 工具读取上下文 → LLM 调 case_graph_propose（payload 暂存 case_design_run.result）
        → run 结束 → materialize（单事务：领域校验 → 写 case_node/step/assertion/source → run=done → 发 case.* + 状态流转）
评审：POST /mindmap/runs{kind=review} → 同上 → findings 落库 → gate.pending
批准：POST /mindmap/review{approve} → baseline_service（校验 blocker 确认）→ 写 case_baseline + 节点 reviewed + VersionLifecycle.ApproveBaseline
导出：POST /mindmap/export{format} → export_service（读基线/当前图 → 生成文本 → 原子写文件 → 元数据入表）
探索：POST /mindmap/runs{kind=explore, admin} → explore_service（HTTP BFS 或 agent 归类）→ CatalogGateway.ImportAppModels(source=explored)
```

## 5. 数据模型与迁移

### 5.1 设计原则

- 主键全部雪花 ID（应用层生成，DDL 无序列/默认值，D29）；JSON/URL/事件/工具参数中为十进制字符串。
- 表名/列名遵循 D34；审计列 `created_at/updated_at`（`DEFAULT now()`，更新时应用层显式赋值）。
- 跨上下文只做 BIGINT 逻辑引用，不建 FK；同表自引用（`parent_id`）与 `step_id` 用 FK 可选（P5 用 FK + `ON DELETE CASCADE` 保证一致性）。
- `version_id`、`requirement_id`、`app_model_node_id`、`repo_id`、`agent_*_id` 全部逻辑引用。
- 文件（导出物）只存路径 + sha256 + 大小（D30），二进制绝不入库。

### 5.2 表 DDL（`mindmap_0001_cases.sql`）

```sql
-- P5 ids are Snowflake values generated by platform/id in the application; no sequence/default.

CREATE TABLE IF NOT EXISTS case_node (
  id             BIGINT NOT NULL,
  version_id     BIGINT NOT NULL,
  parent_id      BIGINT REFERENCES case_node(id) ON DELETE CASCADE,
  kind           TEXT NOT NULL CHECK (kind IN ('scenario','case')),
  title          TEXT NOT NULL CHECK (title <> '' AND length(title) <= 200),
  description    TEXT NOT NULL DEFAULT '',
  priority       TEXT NOT NULL DEFAULT 'P2' CHECK (priority IN ('P0','P1','P2','P3')),
  preconditions  TEXT NOT NULL DEFAULT '',
  test_data      JSONB,
  source         TEXT NOT NULL CHECK (source IN ('user_input','ai_generated','ai_supplement')),
  status         TEXT NOT NULL DEFAULT 'draft' CHECK (status IN (
                   'draft','reviewed','executed','supplemented','merged','rejected','needs_revalidation')),
  sort_order     INT NOT NULL DEFAULT 0,
  revision       INT NOT NULL DEFAULT 1 CHECK (revision >= 1),
  created_by     BIGINT NOT NULL,
  updated_by     BIGINT NOT NULL,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT pk_case_node PRIMARY KEY (id),
  CONSTRAINT ck_case_node_title_trim CHECK (title = btrim(title))
);
CREATE INDEX IF NOT EXISTS idx_case_node_version ON case_node(version_id, parent_id, sort_order, id);
CREATE INDEX IF NOT EXISTS idx_case_node_version_status ON case_node(version_id, status);
CREATE INDEX IF NOT EXISTS idx_case_node_version_source ON case_node(version_id, source);

CREATE TABLE IF NOT EXISTS case_step (
  id          BIGINT NOT NULL,
  node_id     BIGINT NOT NULL REFERENCES case_node(id) ON DELETE CASCADE,
  seq         INT NOT NULL CHECK (seq BETWEEN 1 AND 30),
  action      TEXT NOT NULL CHECK (action <> ''),
  expected    TEXT NOT NULL DEFAULT '',
  data        TEXT NOT NULL DEFAULT '',
  source      TEXT NOT NULL CHECK (source IN ('user_input','ai_generated')),
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT pk_case_step PRIMARY KEY (id),
  CONSTRAINT uk_case_step_node_seq UNIQUE (node_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_case_step_node ON case_step(node_id, seq);

CREATE TABLE IF NOT EXISTS case_assertion (
  id          BIGINT NOT NULL,
  node_id     BIGINT NOT NULL REFERENCES case_node(id) ON DELETE CASCADE,
  step_id     BIGINT REFERENCES case_step(id) ON DELETE CASCADE,
  kind        TEXT NOT NULL CHECK (kind IN ('ui','api','db','text')),
  target      TEXT NOT NULL DEFAULT '',
  expression  TEXT NOT NULL CHECK (expression <> ''),
  expected    TEXT NOT NULL DEFAULT '',
  source      TEXT NOT NULL CHECK (source IN ('user_input','ai_generated')),
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT pk_case_assertion PRIMARY KEY (id)
);
CREATE INDEX IF NOT EXISTS idx_case_assertion_node ON case_assertion(node_id);
CREATE INDEX IF NOT EXISTS idx_case_assertion_step ON case_assertion(step_id);

CREATE TABLE IF NOT EXISTS case_source (
  id          BIGINT NOT NULL,
  version_id  BIGINT NOT NULL,
  node_id     BIGINT NOT NULL REFERENCES case_node(id) ON DELETE CASCADE,
  ref_kind    TEXT NOT NULL CHECK (ref_kind IN ('requirement','app_model','ai_run','manual')),
  ref_id      BIGINT,
  ref_key     TEXT NOT NULL DEFAULT '',
  source      TEXT NOT NULL CHECK (source IN ('user_input','ai_generated','ai_supplement')),
  confidence  TEXT NOT NULL DEFAULT 'high' CHECK (confidence IN ('high','medium','low')),
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT pk_case_source PRIMARY KEY (id),
  CONSTRAINT ck_case_source_ref CHECK (ref_kind = 'manual' OR ref_id IS NOT NULL)
);
CREATE UNIQUE INDEX IF NOT EXISTS uk_case_source_ref
  ON case_source(node_id, ref_kind, COALESCE(ref_id, 0), ref_key);
CREATE INDEX IF NOT EXISTS idx_case_source_version_kind
  ON case_source(version_id, ref_kind, ref_id);

CREATE TABLE IF NOT EXISTS case_review (
  id          BIGINT NOT NULL,
  version_id  BIGINT NOT NULL,
  run_id      BIGINT,
  status      TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','approved','rejected')),
  comment     TEXT NOT NULL DEFAULT '',
  reviewer_id BIGINT NOT NULL,
  decided_by  BIGINT,
  decided_at  TIMESTAMPTZ,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT pk_case_review PRIMARY KEY (id)
);
CREATE UNIQUE INDEX IF NOT EXISTS uk_case_review_approved
  ON case_review(version_id) WHERE status = 'approved';
CREATE INDEX IF NOT EXISTS idx_case_review_version ON case_review(version_id, created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS case_review_finding (
  id          BIGINT NOT NULL,
  review_id   BIGINT NOT NULL REFERENCES case_review(id) ON DELETE CASCADE,
  node_id     BIGINT REFERENCES case_node(id) ON DELETE SET NULL,
  category    TEXT NOT NULL CHECK (category IN (
                'coverage','assertion','executability','duplicate','code_uncovered','other')),
  severity    TEXT NOT NULL CHECK (severity IN ('blocker','major','minor','info')),
  detail      TEXT NOT NULL CHECK (detail <> ''),
  status      TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','fixed','dismissed')),
  source      TEXT NOT NULL DEFAULT 'ai' CHECK (source IN ('ai','human')),
  created_by  BIGINT NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT pk_case_review_finding PRIMARY KEY (id)
);
CREATE INDEX IF NOT EXISTS idx_case_review_finding_review
  ON case_review_finding(review_id, severity, id);
CREATE INDEX IF NOT EXISTS idx_case_review_finding_node ON case_review_finding(node_id);

CREATE TABLE IF NOT EXISTS case_baseline (
  id                   BIGINT NOT NULL,
  version_id           BIGINT NOT NULL,
  review_id            BIGINT NOT NULL,
  node_count           INT NOT NULL CHECK (node_count >= 0),
  case_count           INT NOT NULL CHECK (case_count >= 0),
  graph_hash           TEXT NOT NULL CHECK (graph_hash ~ '^[0-9a-f]{64}$'),
  code_baseline_commit TEXT CHECK (code_baseline_commit IS NULL
                         OR code_baseline_commit ~ '^[0-9a-f]{40}$'),
  frozen_by            BIGINT NOT NULL,
  frozen_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT pk_case_baseline PRIMARY KEY (id),
  CONSTRAINT uk_case_baseline_version UNIQUE (version_id),
  CONSTRAINT uk_case_baseline_review UNIQUE (review_id)
);

CREATE TABLE IF NOT EXISTS case_design_run (
  id               BIGINT NOT NULL,
  version_id       BIGINT NOT NULL,
  kind             TEXT NOT NULL CHECK (kind IN ('explore','design','review','data_plan')),
  status           TEXT NOT NULL DEFAULT 'queued'
                     CHECK (status IN ('queued','running','done','failed','degraded')),
  agent_session_id BIGINT,
  agent_run_id     BIGINT,
  model            TEXT NOT NULL DEFAULT '',
  prompt_version   TEXT NOT NULL DEFAULT '',
  input            JSONB,
  result           JSONB,
  stats            JSONB,
  errors           JSONB,
  created_by       BIGINT NOT NULL,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  started_at       TIMESTAMPTZ,
  finished_at      TIMESTAMPTZ,
  CONSTRAINT pk_case_design_run PRIMARY KEY (id)
);
CREATE UNIQUE INDEX IF NOT EXISTS uk_case_design_run_active
  ON case_design_run(version_id, kind) WHERE status IN ('queued','running');
CREATE INDEX IF NOT EXISTS idx_case_design_run_version
  ON case_design_run(version_id, created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS case_export (
  id           BIGINT NOT NULL,
  version_id   BIGINT NOT NULL,
  format       TEXT NOT NULL CHECK (format IN ('markdown','mermaid','opml')),
  file_path    TEXT NOT NULL CHECK (file_path <> ''),
  file_sha256  TEXT NOT NULL CHECK (file_sha256 ~ '^[0-9a-f]{64}$'),
  size_bytes   BIGINT NOT NULL CHECK (size_bytes > 0),
  baseline_id  BIGINT,
  node_count   INT NOT NULL CHECK (node_count >= 0),
  created_by   BIGINT NOT NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT pk_case_export PRIMARY KEY (id)
);
CREATE UNIQUE INDEX IF NOT EXISTS uk_case_export_baseline
  ON case_export(version_id, format, baseline_id) WHERE baseline_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_case_export_version ON case_export(version_id, created_at DESC, id DESC);
```

> 说明：`PRIMARY KEY` 在 PostgreSQL 中已隐含唯一索引，`CONSTRAINT pk_<表> PRIMARY KEY` 同时满足 D34 的索引命名规约；`uk_case_source_ref` 用函数索引处理 `ref_id NULL` 的去重（`manual` 来源可重复与否由 `ref_key` 区分）。

### 5.3 迁移执行顺序

`order.txt` 追加（末尾，顺序即版本号）：`identity_0003`、`platform_0002`、`eventing_0002`、`audit_0002`、`llm_0002`、`storage_0002`、`agent_0003`、`resource_0004`（rename）、`resource_0005`（source）、`version_0003`（rename）、`codeintel_0003`（rename）、`codeintel_0004`（anchor case）、`mindmap_0001`（9 表）。

- 全部只追加；全新库与既有库最终 schema 一致（见 §3.3）；启动幂等、`schema_migrations` checksum 不变。
- `mindmap` 表不建跨上下文 FK；`parent_id`/`step_id` 自上下文 FK + `ON DELETE CASCADE`。
- 迁移由 `db.MigrateEmbedded` 在启动时执行（advisory lock 4242），P5 不引入独立迁移工具。

---

## 6. 核心规则

### 6.1 脑图树规则

1. 根节点：`version_id` 下 `parent_id IS NULL`；允许 `scenario` 嵌套，最多 **2 层 scenario**（version→scenario→scenario→case 共 3 层边）。
2. `kind='case'` 必须是叶子（不允许有子节点）；`kind='scenario'` 不能直接挂步骤/断言。
3. `case` 至少 1 条 `case_step`；`seq` 从 1 连续到 N（≤30），空缺口 → 409 `invalid_case_tree`。
4. `case` 至少 1 条 `case_assertion`；`step_id` 非空时必须属于同一 `node_id`。
5. 同版本内 `sort_order` 由服务端维护（创建追加到末尾；重排接口一次提交 `nodeIds` 全量有序列表，事务内更新）。
6. 删除 `scenario` 级联删除子树（步骤/断言/来源/锚点由 FK 级联或应用层清理）；删除 `case` 同时删除其 anchors（经 `CodeAnchorGateway` best-effort）与 sources。
7. 所有写操作在事务内校验树规则；失败零写入。

### 6.2 覆盖规则（需求↔用例）

1. 每个 `case` 至少一条 `case_source(ref_kind='requirement')`，`ref_id` 必须属于本版本且 `Requirement.status='clarified'`。
2. `case_source` 只允许挂在 `kind='case'` 节点（`scenario` 不写来源）；`scenario` 只作为结构性分组，不构成覆盖。
3. 覆盖矩阵 = 对每个已确认需求，列出覆盖它的 case 集合；未被任何 case 覆盖的需求在 `GET /mindmap` 的 `coverage.uncoveredRequirementIds` 显式返回。
4. 用例可覆盖多需求（多来源行）；删除来源不允许使 case 变为零需求（409 `invalid_case_tree`）。
5. 需求被重开（`clarified→draft`）时，其关联用例不自动删除，但覆盖矩阵标记 `staleRequirementIds`；P5 不自动改节点状态（P7 增量处理）。

### 6.3 评审与状态机门禁

```text
版本状态（P5 相关）：
designing ──生成成功（auto）或 POST submit-review（人工）──▶ reviewing
reviewing ──POST review{reject}──▶ designing
reviewing ──POST review{approve}──▶ executing（写 case_baseline，节点 draft→reviewed）
```

| 动作 | 允许的版本状态 | 失败 |
|---|---|---|
| 人工 CRUD 脑图/步骤/断言/来源 | `designing`、`reviewing`，且无 baseline | 409 `version_state_conflict` / 409 `baseline_frozen` |
| 启动 `design`/`review`/`data_plan` run | `designing`、`reviewing` | 409 `version_state_conflict`、`run_in_progress` |
| 启动 `explore` run（admin） | `designing` 且版本绑定了 system+environment | 409 `exploration_requires_environment` |
| `submit-review` | `designing`，且存在 ≥1 case、全部满足 §6.1 | 409 `invalid_case_tree` / `no_cases` |
| 生成成功自动转 reviewing | `designing` | 若已是 `reviewing` 保持不变；物化失败不动状态 |
| `review{approve}` | `reviewing`，无 baseline | 409 `version_state_conflict`、`baseline_frozen` |
| `review{reject}` | `reviewing` | 409 `version_state_conflict` |
| 导出 | `designing`/`reviewing`/`executing` | 409 `version_state_conflict`（超出范围） |

- **批准门禁**：存在 `open` 的 `severity='blocker'` findings 时必须带 `acknowledgeFindings=true`，否则 409 `blocker_findings_open`（details 含 finding ids）；确认后批准成功，findings 保留原状态（不自动 fixed）。
- **`EnterReview` 幂等**：目标状态已是 `reviewing` 时返回成功且不发重复事件（CAS `designing→reviewing` 命中 0 行但当前状态为 reviewing 视为幂等成功）。
- **基线唯一**：`case_baseline.version_id` 唯一；批准后任何再次批准 409 `baseline_frozen`（D5 基线只评审一次）。
- **驳回**：写 `case_review.status='rejected'` + 评论；不建 baseline；版本回 `designing`；脑图保持可编辑。
- 状态流转全部经 `VersionLifecycle`；`mindmap` 不直接 UPDATE `version` 表。

### 6.4 冻结与基线

1. 批准时计算 `graph_hash`：对规范化 JSON（节点按 `sort_order,id` 排序；字段固定顺序；步骤/断言按 `seq`/`id`）做 sha256。
2. **跨上下文写入顺序（补偿式）**：先在 mindmap 单事务写 `case_baseline` + 全图节点 `draft→reviewed` → 再调 `VersionLifecycle.ApproveBaseline`（CAS `reviewing→executing`）→ 若返回冲突/失败，在补偿事务中删除该 baseline 并把节点状态的 `revision`/`status` 回滚到批准前 → 抛 409 `version_state_conflict`。补偿只处理"状态被并发抢占"这一种失败；补偿本身失败记 error 日志并返回 500，需人工核对（测试覆盖）。
3. 基线冻结后：`case_node/step/assertion/source` 全部写操作 409 `baseline_frozen`；评审 run 仍可启动（只读评审）但不可再次批准；导出可继续。
4. `case_baseline` 记录 `node_count`、`case_count`、`code_baseline_commit`（取版本当前值）、`frozen_by/at`。
5. 冻结不可回退（P5 无"解冻"）；需求重开或代码变更后的增量处理归 P7。
6. 冻结后 `case_export` 强制绑定 `baseline_id`；未冻结时导出的 `baseline_id=NULL` 且不参与复用。

### 6.5 并发编辑（OCC）

- `case_node` 有 `revision`；`PATCH` 必须带 `revision`，服务端 `UPDATE ... WHERE id=? AND revision=?`，不匹配 409 `case_revision_conflict`（details 含当前 revision）。
- 步骤/断言写入请求带 `nodeRevision`，服务端在同一事务校验节点 revision 后再增删改并 `revision+1`，避免"节点内容与子集合"交叉更新丢失。
- 排序/删除同样走 revision；批量重排只校验一次版本级 `revision`（版本级 `graph_revision` 不新增，直接用节点 revision 集合并校验，冲突整单 409）。
- **节点级重排例外**：`PUT /mindmap/order`（spec §7.2 端点 5）没有 revision 参数，按"最后一次写入胜出 + 必须提交完整子集合"处理（不传或漏传子节点 → 409 `invalid_case_tree`）；步骤重排（端点 9）仍要求 `nodeRevision`。

### 6.6 Agent run 生命周期（4 类）

```text
POST runs → 校验状态/单飞/输入 → INSERT case_design_run(status=queued, input) 
          → agent.StartRun(profile, channel=version:{id}, instruction) 失败 → run=failed + 502/409 语义
worker（2s 轮询 + 启动恢复）→ agent.GetRun → running/done/failed
done → materialize（按 kind 分支，单事务）→ run=done + case.* / gate.* 事件 + 状态流转
failed/degraded → run 终态 + 错误摘要事件（不物化、不改版本状态）
```

- **单飞**：`uk_case_design_run_active(version_id, kind) WHERE status IN ('queued','running')`；并发启动 409 `run_in_progress`。
- **恢复**：启动时把 `status IN ('queued','running')` 且 `started_at < now()-10min` 的 run 置 `failed`（`reason=worker_restart`），并标记对应 agent run 不可用（P4 `RecoverStale` 先例）。
- **预算**：每次 run 的 agent 步数/时长上限由 profile 预算字段控制（P1 既有）；P5 额外限制：设计每次最多 60 个 case、每 case 最多 30 步、每次提交 payload ≤1 MiB（服务端校验）。
- **幂等**：物化以 `run_id` 为幂等键；同一 run 重复处理直接返回（`stats.materialized=true`）；`Idempotency-Key` 作用于 `POST runs` 与 `POST export`。
- **事件**：`case.run_started` / `case.run_finished`（含 kind/status/reason 摘要）；`agent.activity` 照常由 P1 推送（同 `version:{id}` 通道）。

### 6.7 提交 payload 与物化

`case-design` 的 `case_graph_propose` 入参（工具 schema 与 OpenAPI 同步）：

```json
{
  "mode": "append",
  "scenarios": [
    {"ref":"s1","title":"下单主流程","parentRef":null,"description":""}
  ],
  "cases": [
    {
      "ref":"c1","scenarioRef":"s1","title":"正常下单","priority":"P1",
      "requirementIds":["1838293000123"],"preconditions":"已登录且购物车非空",
      "testData":{"sku":"A-1","qty":1},
      "steps":[{"action":"打开商品页","expected":"展示价格与库存","data":""}],
      "assertions":[{"stepIndex":0,"kind":"ui","target":"/order/confirm","expression":"订单号非空","expected":"生成订单号"}],
      "anchors":[{"file":"src/order/submit.ts","startLine":120,"endLine":140}]
    }
  ]
}
```

**服务端校验（全部通过才物化，否则 run=failed，`stats.validationErrors` 明细）**：

1. `scenarios[].ref` / `cases[].ref` 唯一；`scenarioRef` 必须存在或为 `null`（case 直接挂根）。
2. `requirementIds` 非空、去重、全部属于本版本且 `clarified`；否则 `unconfirmed_requirement`。
3. 标题非空 ≤200；`priority ∈ P0..P3`；steps 1..30 且 action 非空；assertions ≥1 且 `stepIndex ∈ [0,len(steps))` 或 `null`。
4. `anchors[]`：`file` 必须是版本代码仓基线 commit 下存在的路径（经 `CodeFactsGateway` 校验）；不存在 → 丢弃并记 `stats.anchorsDropped`（不失败）；通过 → 经 `CodeAnchorGateway` 建 `subject_kind='case'` 锚点。
5. `cases` 总数 ≤60；payload ≤1 MiB；文本字段长度上限（title 200 / action 2000 / expected 2000 / description 4000）。
6. `mode='replace'` 必须带显式确认（API 层 `confirmReplace=true`）：删除本版本全部 `status='draft'` 节点后再物化；`status='reviewed'` 节点存在（理论上仅冻结后）时拒绝 replace（409 `baseline_frozen`）。
7. 物化后在**同一事务**写入 nodes/steps/assertions/sources，并更新 run 状态；事务外才发事件（避免事件先于数据）。
8. 来源：`case_node.source='ai_generated'`；`case_source.source='ai_generated'`；来源 `ai_run` 行记录 run id，便于追溯与 P7 补全标记。

### 6.8 评审、数据规划与探索的物化差异

| kind | 输入 | 物化 | 失败/降级 |
|---|---|---|---|
| `review` | 当前图（只读）+ 需求/代码事实 | 新建 `case_review(status=draft)` + findings；把本版本此前 `source='ai' AND status='open'` 的 findings 置 `dismissed`（superseded） | 降级 → run=degraded，不写 review |
| `data_plan` | 既有节点 id 列表（≤60）+ 资源引用 | 只更新这些节点的 `preconditions/test_data`；带 `nodeRevision`，人工已改则跳过并记 `stats.skipped`；不新建节点 | 降级 → run=degraded，部分更新走单事务（全成功或全跳过） |
| `explore` | system/environment id、可选 session id | 经 `CatalogGateway.ImportAppModels(source='explored')` 写应用模型；`stats.discovered/imported/skipped` | LLM 不可用 → 确定性 BFS 直接入库，run=degraded（reason=ai_unavailable） |

### 6.9 应用探索规则（D36）

1. 仅 `GET`/`HEAD`；不发送 `POST/PUT/PATCH/DELETE`；跳过 `href` 含 `logout|signout|delete|remove|unsubscribe` 的链接（大小写不敏感）。
2. 起点 = 环境 base URL + `allowed_hosts` 白名单；解析同源链接、`<form action>`、`<title>`、`<script src>`；不跟踪跨域重定向（3xx 跨 host 直接丢弃）。
3. 限额：最多 40 个请求、深度 ≤2、单请求 5s 超时、总时长 ≤60s、响应体解析上限 512 KiB、并发 ≤4；超限停止并记 `stats.truncated`。
4. 鉴权：若传入 P2 Session（status ∈ `valid|expiring`），仅使用其 `storage_state.cookies` 生成 `Cookie` 头；不使用 localStorage/token 注入；不写回 Session。
5. 用户可手动提供额外起始路径（≤10 条），仍受白名单限制。
6. 产物去重键 = `api:<METHOD> <path>` / `page:<path>` / `route:<path>`（与 P2 app model key 规则一致）；与既有节点同 key 跳过（`stats.skipped`）。
7. `explore` run 仅 admin 可发起（写资源域），普通用户可选版本已有应用模型做设计。
8. SSRF 防护：拒绝非 http/https、拒绝 URL 内嵌凭据、拒绝白名单外 host、解析 IP 属于私网/回环且未在白名单显式放行时拒绝（测试环境可白名单放行 `127.0.0.1`）。

### 6.10 导出规则（D38）

| 格式 | 生成方 | 内容 | 约束 |
|---|---|---|---|
| `markdown` | 服务端 | 标题、版本/基线信息、覆盖矩阵、每条用例（来源/优先级/前置/测试数据/步骤/断言/锚点）、评审发现项、生成时间 | UTF-8；无 YAML front matter；用例按场景与 `sort_order` 排序 |
| `mermaid` | 服务端 | `mindmap` 语法：根=版本名；需求分组→场景→用例（叶子标签截断 60 字符，重复加序号） | 转义 `()[]{}` 与换行；Mermaid 不支持多行详情，详情只在 Markdown |
| `opml` | 服务端 | OPML 2.0：`<outline text>` 树=版本→需求→场景→用例；用例叶子 `_note` 含优先级/前置/测试数据/步骤/断言/锚点；未覆盖需求 `_note="未覆盖"` | `encoding/xml` 生成；转义 XML；`_` 前缀属性不影响导入器 |
| `png` | 前端 | 当前画布 SVG → canvas → `toBlob('image/png')` 下载 | 视图快照，不入库；文件名 `{versionName}-mindmap.png`；导出前 `fit` 全图 |

- 文件路径：`$EZTEST_EXPORT_DIR/versions/{versionId}/{baselineId|draft}/{format}-{exportId}.{ext}`，根内校验 + 临时文件 + 原子 rename + 0600/0700（D30）。
- `sha256` 为文件内容哈希；下载接口按 `case_export.file_path` 读取并校验哈希，不暴露真实路径。
- 冻结后导出必须引用 `baseline_id`；`(version, format, baseline)` 已存在则复用（返回既有元数据，不重复写文件）。
- 导出事件：`case.export_ready`（summary 含 format/exportId，不含内容）。

### 6.11 事件与实时

新增事件类型（追加到 OpenAPI `VersionEventType`，`channel=version:{id}`，`version_id` 必填）：

| 事件 | 触发 | detail 摘要 |
|---|---|---|
| `case.created` / `case.updated` / `case.deleted` | 人工或物化写入 | nodeId/kind/title(≤120)/source |
| `case.reviewed` | 评审 run 物化 | reviewId/findings 计数/blocker 计数 |
| `case.baseline_frozen` | 批准 | baselineId/nodeCount/caseCount/graphHash 前缀 |
| `case.run_started` / `case.run_finished` | run 状态变化 | runId/kind/status/reason |
| `case.export_ready` | 导出完成 | exportId/format/size |
| `gate.pending` / `gate.resolved` | 进入 reviewing / 批准或驳回 | gate=`mindmap_review` |

- 事件字段沿用 `eventing.envelope`；`refs.case` 填节点 id（≤20）；`detail ≤64 KiB`、`summary ≤500 rune`。
- SSE 通道、seq、快照游标、Last-Event-ID、20s 心跳全部复用 P0/P1；P5 不新增通道。

### 6.12 权限与审计

- 脑图/评审/导出的读与写：**任意登录用户**（与 P3 版本域一致）。
- `explore` run、应用模型写入：**admin**（`role=admin`）；普通用户 403 `forbidden`。
- 代码锚点删除：admin 或创建者（沿用 codeintel 锚点既有规则）。
- 审计：`mindmap` 写操作记 `audit_logs`（`case.node.created/updated/deleted`、`case.review.decided`、`case.baseline.frozen`、`case.export.created`、`case.run.started`）；导出内容/LLM 输入输出正文**不入审计、不入事件**。
- 附件/导出下载必须鉴权；不对外暴露内部路径。

### 6.13 核心不变量（测试锚点，逐条必须有测试）

1. 树：`case` 无子、`scenario` 无步骤/断言、steps seq 连续 1..N、每 case ≥1 断言且 step 归属正确。
2. 覆盖：每 case ≥1 已确认需求来源；删除来源不得使覆盖归零；未覆盖需求显式返回。
3. 状态：非 `designing|reviewing` 一律 409；批准后 `executing` 且 baseline 唯一；驳回回 `designing`。
4. 冻结：冻结后所有写 409 `baseline_frozen`；`graph_hash` 可复算一致。
5. 门禁：blocker 未确认批准 409；确认后成功且 findings 不被静默关闭。
6. 物化：不合法 payload → run=failed、库零写入；同一 run 重复物化零副作用。
7. 单飞/恢复：同 kind 并发 run 409；重启后 stale run 转 failed。
8. OCC：`revision` 不匹配 409；并发编辑不丢更新。
9. 导出：三格式文件存在、sha256 匹配、内容包含全部用例与覆盖矩阵；冻结后绑定 baseline 并复用。
10. 探索：非白名单/非 GET/超限/跨域重定向被拒；产物 source='explored' 且去重。
11. 降级：LLM 关闭时设计/评审/数据规划 run 失败有 reason，人工全链路（建/改/评审/导出）可用。
12. ID：所有接口返回的 mindmap ID 均为十进制字符串；前端不 `Number()`。

## 7. API

### 7.1 约定

- 基址 `/api/versions/{versionId}/mindmap`（沿用 P4 版本级子资源命名风格 `{versionId}`；P3 的 `/api/versions/{id}` 与 `stream` 不变）。
- 成功 `{"data": ...}`；列表 `{"items":[...], "next_cursor":"", "has_more":false}`；错误统一信封 `{error:{code,message,request_id,retryable,details}}`，`details.reason` 为稳定机读原因。
- 所有 mindmap ID（node/step/assertion/source/review/finding/run/export）在 JSON/URL 中为**十进制字符串**；`versionId/requirementId` 同样为字符串；`repoId` 等 P2 既有 ID 保持既有类型语义（沿用 P4 规则，前端不 `Number()`）。
- 写操作鉴权：任意登录用户（除 `explore` run 与锚点删除的 admin 规则）；未登录 401，无权限 403。
- `Idempotency-Key` 支持：`POST runs`、`POST review`、`POST export`、`POST submit-review`。
- 时间 `RFC3339`；布尔 `editable` 由服务端给出，前端不得自行推断。

### 7.2 端点清单

| # | 方法/路径 | 请求 | 响应/语义 |
|---|---|---|---|
| 1 | `GET /mindmap` | `?include=anchors,findings,impact`（逗号分隔，默认全含） | `data: MindmapGraph`（§7.3） |
| 2 | `POST /nodes` | `{kind,parentId?,title,description?,priority?,preconditions?,testData?,requirementIds?,source?}` | 201 `data: CaseNodeView`；人工默认 `source=user_input` |
| 3 | `PATCH /nodes/{nodeId}` | `{title?,description?,priority?,preconditions?,testData?,requirementIds?,revision}` | 200；OCC 失败 409 `case_revision_conflict` |
| 4 | `DELETE /nodes/{nodeId}` | `?revision=` | 204；级联删子树 + sources + 锚点 |
| 5 | `PUT /order` | `{parentId?,nodeIds:[...]}`（必须含该父下全部子节点且无重复） | 204；事务内更新 `sort_order` |
| 6 | `POST /nodes/{nodeId}/steps` | `{action,expected?,data?,afterSeq?}` | 201 `data: CaseStepView`；`afterSeq` 省略=末尾；重排 seq |
| 7 | `PATCH /steps/{stepId}` | `{action?,expected?,data?,nodeRevision}` | 200 |
| 8 | `DELETE /steps/{stepId}` | `?nodeRevision=` | 204；重排 seq；删除该步骤断言 |
| 9 | `PUT /nodes/{nodeId}/steps/order` | `{stepIds:[...]}` | 204；必须含全部步骤 |
| 10 | `POST /nodes/{nodeId}/assertions` | `{stepId?,kind,target?,expression,expected?}` | 201 |
| 11 | `PATCH /assertions/{assertionId}` | `{kind?,target?,expression?,expected?,nodeRevision}` | 200 |
| 12 | `DELETE /assertions/{assertionId}` | `?nodeRevision=` | 204；每 case 最后一条断言不可删（409 `invalid_case_tree`） |
| 13 | `POST /nodes/{nodeId}/sources` | `{refKind,refId?,refKey?}` | 201；`requirement` 来源必须属于本版本且 clarified |
| 14 | `DELETE /sources/{sourceId}` | — | 204；不得使 case 的 requirement 来源归零 |
| 15 | `POST /nodes/{nodeId}/anchors` | `{filePath,startLine?,endLine?,confidence?}` | 201 `data: CodeAnchorView`；经 codeintel 建 `subject_kind='case'` |
| 16 | `DELETE /anchors/{anchorId}` | — | 204；admin 或创建者 |
| 17 | `POST /runs` | `{kind:'design'\|'review'\|'data_plan'\|'explore', mode?,confirmReplace?,nodeIds?,systemId?,environmentId?,sessionId?,startPaths?(≤10),headCommit?}` | 202 `{runId}`；单飞冲突 409 `run_in_progress`；explore 需 admin |
| 18 | `GET /runs` | `?kind=&status=&limit=&cursor=` | 分页 run 列表 |
| 19 | `GET /runs/{runId}` | — | run 详情（含 `status/reason/stats/errors`，不回传 LLM 原文） |
| 20 | `POST /submit-review` | `{}` | 200 `{version}`；`designing→reviewing`；不满足树规则 409 |
| 21 | `POST /review` | `{decision:'approve'\|'reject',comment?,acknowledgeFindings?}` | 200 `{version,baseline?,review}`；approve→`executing`+冻结 |
| 22 | `GET /reviews` | `?limit=&cursor=` | 评审历史 |
| 23 | `GET /findings` | `?status=&severity=&nodeId=` | 发现项列表 |
| 24 | `PATCH /findings/{findingId}` | `{status:'fixed'\|'dismissed'}` | 200；人工处置评审意见 |
| 25 | `POST /export` | `{format:'markdown'\|'mermaid'\|'opml'}` | 201 `data: CaseExportView`；同 baseline+format 复用返回 200 |
| 26 | `GET /exports` | `?format=&limit=&cursor=` | 导出历史 |
| 27 | `GET /exports/{exportId}/download` | — | 200 二进制附件；`Content-Disposition: attachment`；哈希校验失败 500 `export_corrupted` |

> 说明：步骤/断言的写操作全部带 `nodeRevision`（或从路径推导 node）以保证"节点内容 + 子集合"的一致性；服务端在事务内校验后整体 `revision+1`。

### 7.3 `MindmapGraph` 响应结构（契约）

```json
{
  "versionId": "1838...",
  "versionStatus": "reviewing",
  "editable": true,
  "nodes": [
    {
      "id": "1838...", "parentId": null, "kind": "scenario",
      "title": "下单主流程", "description": "", "priority": "P2",
      "preconditions": "", "testData": null,
      "source": "ai_generated", "status": "draft", "sortOrder": 0, "revision": 3,
      "steps": [
        {"id":"1838...","seq":1,"action":"打开商品页","expected":"展示价格","data":"","source":"ai_generated"}
      ],
      "assertions": [
        {"id":"1838...","stepId":"1838...","kind":"ui","target":"/order","expression":"订单号非空","expected":"生成订单号","source":"ai_generated"}
      ],
      "sources": [
        {"id":"1838...","refKind":"requirement","refId":"1838...","refKey":"","source":"ai_generated","confidence":"high"}
      ],
      "anchors": [
        {"id":"1838...","filePath":"src/order/submit.ts","startLine":120,"endLine":140,"confidence":"high","commitSha":"aaaa..."}
      ],
      "openBlockerCount": 0
    }
  ],
  "coverage": {
    "requirementIds": ["1838..."],
    "uncoveredRequirementIds": ["1838..."],
    "staleRequirementIds": [],
    "matrix": [{"requirementId":"1838...","caseIds":["1838..."]}]
  },
  "baseline": {"id":"1838...","graphHash":"....","caseCount":12,"nodeCount":20,"frozenAt":"2026-10-28T10:00:00Z","codeBaselineCommit":"aaaa..."} ,
  "latestRuns": {"design":{"id":"1838...","status":"done","kind":"design","finishedAt":"..."},
                 "review":{"id":"1838...","status":"done","findingCount":4,"blockerCount":1}},
  "impactCases": [{"caseNodeId":"1838...","reason":"requirement changed","confidence":"medium","source":"relation"}]
}
```

- `editable = versionStatus ∈ {designing, reviewing} 且无 baseline`。
- **`latestRuns` 字段约定**：每个 run 摘要至少含 `id/status/kind`；`finishedAt`、`findingCount`、`blockerCount` 可缺省（无则 null/省略）；`GET /findings` 返回 `{"data": [FindingView...]}` 扁平数组（不分页，前端按 severity/status 过滤）。`include` 省略 `anchors`/`impact` 时，对应字段返回空数组（`anchors: []`、`impactCases: []`），不省略键。
- `impactCases` 由 P4 最新 `code_analysis` 的候选（subject=requirement/app_model）经 `case_source` 映射得到，只读提示；`affected.cases` 的正式接入与 `needs_revalidation` 标记归 P6/P7。
- 大图性能：`include` 可省略 `anchors/findings/impact`；节点 >200 时返回 `X-Graph-Truncated: false` 仅当全量；P5 上限 300 节点（超出 409 `graph_too_large`）。

### 7.4 错误原因（`details.reason`，OpenAPI 同步枚举）

`version_state_conflict`、`baseline_frozen`、`case_revision_conflict`、`invalid_case_tree`、`no_cases`、`unconfirmed_requirements`、`requirement_not_found`、`requirement_not_clarified`、`node_not_found`、`step_not_found`、`assertion_not_found`、`source_not_found`、`anchor_not_found`、`review_not_found`、`baseline_not_found`、`finding_not_found`、`run_in_progress`、`run_not_found`、`run_state_conflict`、`invalid_run_kind`、`invalid_case_proposal`、`blocker_findings_open`、`exploration_requires_environment`、`environment_not_allowed`、`environment_unreachable`、`code_repo_unavailable`、`ai_unavailable`、`export_format_unsupported`、`export_not_found`、`export_conflict`、`export_corrupted`、`duplicate_case_source`、`duplicate_key`、`graph_too_large`、`forbidden`。

> 补充约定（实现期固化）：`*_not_found` 统一为 404；`invalid_case_tree`/`no_cases`/`graph_too_large`/`unconfirmed_requirements`/`requirement_not_clarified` 在 HTTP 层映射 409（领域层仍可用 `KindInvalid`）；`duplicate_key` 沿用平台既有 reason；`export_conflict` 表示冻结态导出的并发唯一冲突。

### 7.5 SSE 与事件

- 复用 `GET /api/versions/{versionId}/stream?since=` 与 `GET /api/versions/{versionId}/events`，通道固定 `version:{id}`，不新增通道。
- 新增事件类型（追加 OpenAPI `VersionEventType`）：`case.created`、`case.updated`、`case.deleted`、`case.reviewed`、`case.baseline_frozen`、`case.run_started`、`case.run_finished`、`case.export_ready`，并复用 `gate.pending`/`gate.resolved`。
- `refs.case` 携带节点 ID（≤20）；事件 `detail` 只放 id/计数/状态摘要（≤64 KiB），不放脑图正文、LLM 输入输出、导出内容。

---

## 8. Agent 与提示词

### 8.1 4 个 profile（seed，`Builtin=false`，幂等；启动时 `AllowedTools` 对活注册表校验）

| key | Name | AllowedTools | 预算 |
|---|---|---|---|
| `case-design` | 测试设计 Agent | `version.context_get`、`case.graph_read`、`code.search`、`code.read`、`code.facts`、`code.routes`、`code.apis`、`code.diff`、`code.impact`、`case.graph_propose` | MaxTurns 24、RunSeconds 1200、HardTimeout 90、MaxOutputTokens 32768、thinking enabled / effort max |
| `case-review` | 设计评审 Agent | `version.context_get`、`case.graph_read`、`code.facts`、`code.routes`、`code.apis`、`code.read`、`case.review_submit` | MaxTurns 16、RunSeconds 900、HardTimeout 90、MaxOutputTokens 32768 |
| `case-data-plan` | 数据/前置规划 Agent | `version.context_get`、`case.graph_read`、`case.data_plan_submit` | MaxTurns 10、RunSeconds 600、HardTimeout 60、MaxOutputTokens 16384 |
| `app-exploration` | 应用探索 Agent | `explore.http`、`app_model_propose`、`code.routes`、`code.apis` | MaxTurns 16、RunSeconds 600、HardTimeout 60、MaxOutputTokens 16384 |

- 系统提示词常量与 `prompt_version`（`p5-design-v1`/`p5-review-v1`/`p5-data-plan-v1`/`p5-explore-v1`）放在 `mindmap/domain/prompts.go`；提示词正文在实现规格落地时逐字写入代码，本规格约束其**必须包含**：角色边界、输入范围、输出必须调用提交工具、禁止编造需求 ID/文件路径、优先级定义、覆盖要求、失败时明确说明缺什么。
- 管理员可通过既有 `PATCH /api/agent-profiles/{key}`（或 P1 既有 admin 接口）改提示词/模型/工具；P5 不新增配置面。
- `Builtin=false` 保证不会被 `profileForSession(0)` 当作默认 profile。

### 8.2 只读与提交工具（`mindmap/infrastructure/agenttools` 侧薄壳，注册进 P1 registry）

| 工具 | 入参 schema（要点） | 行为 |
|---|---|---|
| `case.graph_read` | `{versionId}` | 返回图摘要（节点 id/kind/title/优先级/需求来源/步骤数/断言数/锚点数/评审发现），**不含**全文；上限 300 节点、返回 ≤64 KiB，超出截断并标注 `truncated` |
| `case.graph_propose` | §6.7 payload + `{versionId, mode?}` | 仅把 payload 写入当前 run 的 `result`（通过工具调用上下文定位 `case_design_run`）；不直接写业务表；重复调用以最后一次为准（`stats.proposals` 计数） |
| `case.review_submit` | `{versionId, findings:[{nodeId?,category,severity,detail}]}` | 写当前 review run 的 `result`；findings ≤200、detail ≤2000；不写业务表 |
| `case.data_plan_submit` | `{versionId, items:[{nodeId,nodeRevision,preconditions?,testData?}]}` | 写当前 data_plan run 的 result；items ≤60；不改节点 |
| `app_model_propose` | `{systemId, nodes:[{key,kind,name,path?,method?,parentKey?}]}` | 写当前 explore run 的 result；节点 ≤200；由物化阶段校验 key 规则后经 resource 导入 |
| `explore.http` | `{url, method?:'GET'\|'HEAD'}` | 只读探测（§6.9 限额/白名单）；返回 status/title/links/forms 摘要 ≤32 KiB；非白名单/私网/非 GET 返回 ToolError `explore_not_allowed` |

- 工具实现只依赖 `mindmap/application` 端口，不直接访问 DB/LLM SDK；调用记审计 `mindmap.tool.invoked`（tool/runId/actor/结果计数）。
- 工具错误使用 P1 `ToolError{code,message}`：`invalid_arguments`、`run_not_found`、`proposal_too_large`、`explore_not_allowed`、`explore_limit_exceeded`。

### 8.3 上下文组装（run 启动时）

`instruction` 文本包含（≤64 KiB，超出按优先级截断并记 `stats.contextTruncated`）：

1. `versionId`、`runId`、`kind`、`mode`；版本名/状态/绑定资源（system/environment/代码仓基线 commit，若有）。
2. 已确认需求列表：id、标题、优先级、验收要点、附件摘要（文本类，≤8 KiB/条，最多 20 条；图片不注入，P5 评估不需要）。
3. 需求关系（依赖/冲突/重叠/共享范围）。
4. 应用模型摘要（page/route/api/component key，最多 200 条）。
5. P4 代码事实摘要：变更文件/路由/接口/影响候选（只读；最多 100 条）。
6. 现有脑图摘要（`case.graph_read` 同构）。
7. 任务指令与"必须调用 `*_submit` 工具"的硬要求；失败时说明缺什么，不要编造。

### 8.4 run 协调 worker（`mindmap/application/run_service.go`）

- 轮询间隔 2s（`EZTEST_MINDMAP_WORKER_INTERVAL_MS`，默认 2000，范围 200–30000）；每轮最多处理 5 个 run；单实例进程内 worker（P4 先例）。
- 通过 `AgentRuntime.GetSession(ctx, agentSessionID)` 读 `Running/LastRun.Status`：`done→物化`；`failed/aborted/interrupted→run=failed`（reason=`agent_<status>`）；超时（`started_at + HardTimeout + 60s`）→ 调 `Abort` 并 `run=failed`（reason=`agent_timeout`）。
- `queued` 超过 60s 未变成 `running` → `run=failed`（reason=`agent_start_timeout`）。
- 启动恢复：`status IN ('queued','running')` 且 `updated_at < now()-10min` → `failed`（reason=`worker_restart`）。
- 物化在单事务内完成；事务提交后才发事件；失败写 `errors`（≤20 条，每条 ≤500 rune）。

### 8.5 降级语义

| 场景 | 行为 |
|---|---|
| LLM 未配置（`llm_configured=false`） | `POST runs` 仍创建 run → `run=failed`（reason=`ai_unavailable`）+ `case.run_finished`；HTTP 仍 202（异步语义），run 详情给可读原因；**人工路径不受影响** |
| LLM 超时/失败 | 重试 1 次（同 run 内）；仍失败 → `degraded`（若已拿到部分 proposal 则丢弃，零物化） |
| 设计 payload 校验失败 | `run=failed`（reason=`invalid_case_proposal`，`stats.validationErrors` 最多 20 条）；不物化、不改版本状态 |
| 评审 run 失败 | `run=failed`；不生成 review，不阻塞人工评审/批准（无 blocker 时） |
| 数据规划失败 | `run=failed`；已有前置/数据不变 |
| 探索 LLM 不可用 | 确定性 BFS 结果直接导入，`run=degraded`（reason=`ai_unavailable`） |
| 探索环境不可达 | `run=failed`（reason=`environment_unreachable`）；返回明确指引 |

---

## 9. 前端设计

### 9.1 路由与入口

- 新增 `/versions/[versionId]/mindmap`（`frontend/src/app/(app)/versions/[id]/mindmap/page.tsx` → `features/mindmap/MindmapPage.tsx`）；全屏页面，进入即拉 `GET /mindmap` 并订阅 `useVersionStream(versionId)`。
- 入口：`VersionHeader` 增"用例脑图"按钮（`data-testid="open-mindmap"`）；`OverviewTab` 的 FUTURE_SECTIONS 中 P5 项替换为可点卡片（覆盖数/用例数/基线状态）；`nav-items.ts` 的"用例脑图"改为指向 `/versions`（版本列表选择后进入），移除无效 hash。
- `AppShell`：当 pathname 匹配 `/versions/[^/]+/mindmap$` 时内容区不加 `max-w-6xl`（全幅画布）；其他页面不变。

### 9.2 组件与文件

```text
frontend/src/features/mindmap/
  api.ts                 # 28 个端点的薄封装（复用 api-client；ID 保持字符串）
  types.ts               # MindmapGraph/CaseNodeView/CaseRunView/...（手写 DTO，镜像 OpenAPI）
  useMindmap.ts          # REST 快照 + SSE 合并；事件→局部刷新；run 轮询兜底（SSE 断开时）
  MindmapPage.tsx        # 布局：工具栏 + 画布 + 右栏（编辑/评审/运行/导出）
  MindmapCanvas.tsx      # 手写 SVG 确定性 tidy-tree（布局函数在 mindmap-layout.ts）
  mindmap-layout.ts      # 纯函数：树→坐标/连线/折叠；单测覆盖
  CaseEditor.tsx         # 节点属性 + 步骤/断言（revision OCC、上下移、删除保护）
  ScenarioEditor.tsx
  ReviewPanel.tsx        # 发起评审、findings 处置、批准/驳回、blocker 确认
  RunPanel.tsx           # 生成/数据规划/探索入口、run 状态、degraded 横幅
  SourcePanel.tsx        # 需求/应用模型来源 + 代码锚点（新增/删除）
  ExportMenu.tsx         # Markdown/Mermaid/OPML 下载 + PNG（SVG→canvas）
  png-export.ts          # serializeSvgToPng(svg, filename)（纯函数 + DOM 边界）
  error-messages.ts      # reason → 中文文案（含全部 §7.4 原因）
```

- 状态：`useMindmap` 维护 `graph/loading/error/streamStatus/selectedNodeId/dirty`；写操作成功后用响应替换局部状态（服务端权威，不做乐观更新，沿用 P3 风格）；409 冲突显示"内容已变化，请刷新"并保留本地草稿。
- SSE 合并：`case.*`/`version.status_changed`/`gate.*` 触发节流刷新（≤1 次/500ms，结构签名防抖，沿用 frontend-design §6）；`streamStatus` 在工具栏显示"实时/重连中/断开"。
- 画布交互：折叠展开、点击选中、键盘 ↑↓←→/Enter、缩放（0.5–2）、fit、拖拽平移（SVG transform，无图库）；用例节点显示来源角标（人工/AI）、优先级、状态、blocker 数、受影响提示；冻结后画布只读徽章。
- 编辑：右侧面板；步骤/断言上下移；前置/测试数据文本域（`testData` 为 JSON 文本，保存前校验 JSON）；删除节点二次确认（含子节点数）。
- 评审：`ReviewPanel` 展示 findings 按严重级分组；`fixed/dismissed` 操作；批准按钮在存在 open blocker 时要求勾选"已知悉 blocker 并继续"；驳回必填评论（≥1 字符）。
- 导出：`ExportMenu` 三个服务端格式走 `apiStreamFetch` → blob 下载（按 `Content-Disposition` 文件名）；PNG 走 `png-export.ts`；导出中/失败有状态；冻结后提示"已绑定基线 vN"。
- 降级/空态：无需求→引导去 P3 澄清；无脑图→"AI 生成"与"手工新建"双入口；AI 不可用→`RunPanel` 显示 reason + 手工路径提示；无代码仓→锚点面板提示不可用；无权限探索→隐藏/禁用并说明 admin 限制。
- 可访问性：画布 `role="tree"`/`treeitem`、`aria-expanded`、焦点环；面板表单 label 关联；错误 `role="alert"`。

### 9.3 测试钩子（稳定、供 E2E）

`data-testid`：`mindmap-canvas`、`case-node-{id}`、`scenario-node-{id}`、`mindmap-toolbar`、`btn-generate`、`btn-run-review`、`btn-approve`、`btn-reject`、`btn-export-markdown`、`btn-export-mermaid`、`btn-export-opml`、`btn-export-png`、`case-editor`、`step-row-{seq}`、`finding-row-{id}`、`stream-status`、`baseline-badge`、`readonly-banner`。

## 10. 测试与验收

### 10.1 分层矩阵

| 层 | 覆盖 | 位置 | 门禁 |
|---|---|---|---|
| Go 单元 | 领域规则/树校验/覆盖/状态门禁/导出器/探索器/哈希 | `internal/mindmap/...`（colocated `*_test.go`） | `go test ./internal/mindmap/...` |
| Go 集成 | 9 表迁移与仓储、改名迁移链、物化事务、冻结补偿、run 恢复、探索 HTTP（httptest） | `internal/mindmap/integration/`（`//go:build integration`） | `scripts/test-integration.ps1 ./internal/mindmap/integration/...` |
| 前端单测 | 布局/PNG/错误文案/OCC/画布渲染 | `frontend/src/features/mindmap/*.test.ts(x)` | `pnpm vitest run src/features/mindmap` |
| E2E | 全链路（fake LLM） | `frontend/e2e/mindmap.spec.ts` | `scripts/e2e.ps1 mindmap` |
| 总验收 | 静态+迁移+API+人工路径+导出+事件 | `scripts/acceptance.ps1`（P5 区块） | `scripts/acceptance.ps1` |
| 真实 LLM | 生成/评审质量冒烟+降级 | `scripts/acceptance-mindmap-llm.ps1` | `$env:EZTEST_RUN_REAL_LLM=1` |
| 真实人工验收 | 我以用户身份走完整闭环 | 浏览器 + 真实 LLM + 真实 DB | 人工判定 + 证据留档 |

### 10.2 Fixtures 与数据

- `scripts/fixture-target.cjs` 扩展 P5 专用页面（仅测试夹具，生产无影响）：新增 `/p5/home`（含 3 个同源链接与 1 个表单）、`/p5/orders`（页面 + 链接到 `/p5/orders/{id}`）、`/p5/admin`（需 cookie 登录，未登录 302）、`/api/orders`（GET JSON）、`/api/orders/{id}`；保留既有路由。用于探索验收（页面/路由/接口发现、鉴权跳过、去重）。
- 代码仓夹具复用 `scripts/fixture-codeintel-repo.ps1`（已有 2 个 commit + 变更文件），P5 只需在其上创建版本并写锚点。
- 版本/需求夹具：通过 P3 API 创建需求并 `confirm`（必要时用 fake LLM 走澄清），保证 `Requirement.status='clarified'`；应用模型通过 P2 `POST /api/resources/app-models` 或 P5 探索生成。
- E2E 新 helper：`frontend/e2e/p5-helpers.ts`（`seedMindmapVersion`、`startDesignRun`、`waitDesignRun`、`getMindmap`、`approveMindmap`、`exportMindmap`、`readCaseEvents`、`p5HarnessSkipReason`），复用 `p3-helpers` 的登录/建版本/需求函数。

### 10.3 后端测试（必须覆盖 §6.13 不变量）

- `domain`：树规则（深/seq/断言/step 归属）、来源规则、状态门禁、`graph_hash` 规范化、导出 Markdown/Mermaid/OPML（XML 解析 + 层级/`_note` 断言）、探索 URL 校验与链接过滤。
- `application`：`graph_service` CRUD + OCC；`materialize` 合法/非法 payload（零物化）、mode=replace、锚点丢弃、run 幂等；`review` blocker 门禁/重复批准；`baseline` 冻结只读/补偿；`export` 三格式复用；`run_service` 状态机（queued→running→done/failed/degraded、超时、stale 恢复、单飞）——用 fake `AgentRuntime` 端口。
- `infrastructure/postgres`：9 表仓储 + 部分唯一索引（活动 run、批准 review）+ 级联删除。
- `integration`：完整迁移链（旧名→新名→P5 表）后表存在性断言；物化事务回滚；冻结并发（版本状态被抢占 → baseline 清理、节点回滚、409）。

### 10.4 前端单测

- `mindmap-layout.test.ts`：确定性布局（节点坐标、边、折叠、空图、200 节点）；`png-export.test.ts`：SVG 序列化 + canvas 调用（mock）；`error-messages.test.ts`：§7.4 全覆盖；组件测试：画布 aria/键盘、编辑器 OCC 409 横幅、评审批准 blocker 勾选、只读态禁用。

### 10.5 E2E（`mindmap.spec.ts`，fake LLM P5 模式）

一个 `test()`、`test.setTimeout(300_000)`、serial；步骤：

1. 登录 → API seed：system/environment/app model/code repo/version/2 已确认需求（复用 P3 helper）。
2. 打开 `/versions/{id}/mindmap` → 空态双入口可见；点"AI 生成" → fake LLM 返回 proposal → 画布出现需求分组→场景→用例（断言节点数/来源角标）。
3. 编辑用例标题/优先级、增删步骤与断言、上下移；刷新页面内容保持（REST 权威）。
4. 触发评审 run → findings 出现；批准（有 blocker → 勾选确认）→ 只读横幅 + baseline badge。
5. 冻结后尝试编辑 → 按钮禁用/409 提示。
6. 导出 Markdown/OPML/Mermaid（Playwright `download` 事件 + 读取内容断言包含用例标题/步骤/覆盖矩阵）+ PNG（`download` 事件，文件头 PNG 魔数）。
7. `expect.poll` 事件快照包含 `case.created`、`case.reviewed`、`case.baseline_frozen`、`case.export_ready`；`stream-status` 显示实时/重连。
8. 负例：删除最后一个 requirement source 返回 409 且 UI 提示；非 admin 触发探索入口不可见。

### 10.6 fake LLM（`scripts/fake-llm.cjs`）P5 模式

- 在 `detectP4Scenario` 之后、`recordRequest` 之前增加 `if (await handleP5Scenario(messages, res, stream)) return`；`detectP5Scenario` 匹配 **system prompt 标记**：`EzTest 的测试设计 Agent`、`EzTest 的设计评审 Agent`、`EzTest 的数据与前置规划 Agent`、`EzTest 的应用探索 Agent`。
- 场景状态用 `run.step` 计数（同 P3）：design = `case_graph_read` → `case_graph_propose`（payload 从指令里的 versionId/需求 id 构造）→ 最终文本；review = `case_graph_read` → `case_review_submit`（覆盖/断言/重复 findings 各 1 + 1 blocker）；data_plan = `case_graph_read` → `case_data_plan_submit`；explore = `explore_http`（2 次）→ `app_model_propose`。
- 工具函数名使用**下划线形式**（P1 网关 `strings.ReplaceAll(name,".","_")`，`internal/agent/infrastructure/llm/gateway.go:193-195`）。
- 增加 `EZTEST_FAKE_LLM_P5_MODE`（`normal|invalid|degraded`）与 `/__p5-mode` GET/POST（镜像 P4 模式），用于 E2E 断言"非法 proposal → run failed 零物化"与降级 UI。

### 10.7 `scripts/acceptance.ps1` 追加（P4 区块之后、SKIPPED 行之前）

至少 12 个 check（命名与 DoD 对齐）：

1. `P5 spec/deliverables`：13 个迁移存在、`order.txt` 追加、目录/文件齐全、README/env 更新。
2. `P5 OpenAPI`：27 个 path、`VersionEventType` 8 个新事件、`MindmapErrorReason` 全部 reason。
3. `P5 migration rename`：启动两次，旧表名不存在、新表名存在、`schema_migrations` 指纹不变。
4. `P5 API smoke`：未登录 401；不存在版本 404；`POST /nodes` 成功；非法树 409；ID 字符串断言。
5. `P5 manual path`：无 LLM 时人工建场景/用例/步骤/断言/需求来源 → submit-review → approve → 只读 409。
6. `P5 freeze/export`：批准后导出三格式 → 文件存在 + sha256 匹配 + 内容含标题/步骤/覆盖矩阵；重复导出复用同一 export id。
7. `P5 validation`：非法 proposal（构造 fake LLM invalid 模式）→ run failed + 库中零新增节点。
8. `P5 explore`：admin 触发探索 fixture → app model 出现 page/route/api 节点（source=explored）；普通用户 403；非白名单 URL 拒绝。
9. `P5 events`：事件快照含 `case.created/case.reviewed/case.baseline_frozen/case.export_ready`，`refs.case` 为字符串。
10. `P5 permissions`：普通用户可编辑脑图/评审、不可 explore；锚点删除 admin/创建者。
11. `P5 degraded`：LLM 配置清空后 design run=failed(ai_unavailable)，人工路径仍可完成。
12. `P5 DoD 映射`：§10.9 表格逐条 → 证据（检查项/测试/人工验收）。

### 10.8 真实 LLM 验收脚本（`scripts/acceptance-mindmap-llm.ps1`，gated）

沿用 P4 骨架（`Invoke-Api`/`Add-CheckResult`/健康检查/端口与新库），至少 16 个 check：

1. 门禁：`EZTEST_RUN_REAL_LLM=1` 且 `llm_base_url/key/model_name` 齐全（缺失 = 失败，不 skip）。
2. 新库 `eztest_e2e` + 迁移 + 健康 + admin 登录。
3. Seed：system/environment/app model（含 page/route/api）+ code repo（fixture 仓）+ 版本 + 3 个已确认需求 + 1 个需求关系。
4. 真实 LLM 设计 run：202→轮询→done；断言 case 数 ≥3、每 case steps ≥2、assertions ≥1、每条 case 至少 1 个需求来源、`source=ai_generated`。
5. 需求覆盖：3 个需求中 ≥2 个被覆盖；未覆盖需求显式出现在 `coverage.uncoveredRequirementIds`。
6. 代码锚点：至少 1 条锚点落在 fixture 仓真实文件路径（或零锚点但 `anchorsDropped` 有原因；二者必须可解释）。
7. 评审 run：done；findings ≥1，含 `code_uncovered` 或 `coverage` 类别；blocker 存在时批准需确认。
8. 人工编辑：改标题/加步骤/加断言 → 刷新保持；OCC 旧 revision 409。
9. 驳回：新建评审 run 后 reject → 版本 `designing`、无 baseline、仍可编辑。
10. 再提交评审 → approve（ack）→ `executing` + baseline + 节点 `reviewed` + 只读 409。
11. 导出 Markdown/Mermaid/OPML：文件存在、sha256、内容含真实用例标题/步骤/覆盖矩阵/基线 commit；重复导出复用。
12. 事件：`case.created/case.reviewed/case.baseline_frozen/case.export_ready` 均可见；SSE 快照 seq 单调。
13. 数据规划 run：done 且至少 1 个用例的前置/测试数据被补充。
14. 探索（fixture）：`explore` run done/degraded；应用模型新增 page/route/api 且 source=explored。
15. 降级：LLM 关闭（临时清空配置或 `EZTEST_LLM_DISABLED=1` 测试开关）→ design run failed(ai_unavailable)，人工路径仍可完成建/审/导出。
16. 幂等/并发：同 kind 并发 run 409；重复导出复用；重复 approve 409。

### 10.9 P5 DoD → 证据映射

| DoD（implementation-phases §2-P5） | 证据 |
|---|---|
| 从确认需求生成脑图 | 真实 LLM check 4/5；E2E 步骤 2 |
| 人可编辑/评审 | E2E 步骤 3/4；check 8/9/10 |
| 批准后成为基线并导出 | E2E 步骤 4/6；check 10/11 |
| 节点带来源与代码锚点 | check 4/6；E2E 断言来源角标 |
| 关系正确（需求↔用例覆盖） | check 5；`GET /mindmap` 覆盖矩阵断言 |
| AI 不可用时人工建/改流程不阻断 | check 15；check 5（acceptance.ps1 manual path） |
| 脑图评审 SSE 实时可见 | E2E 步骤 7；check 12 |
| 降级路径定义 | §8.5 + check 15 |
| 设计文档同步 | §13 + acceptance check 2 |
| 自动化测试关键路径 | §10.3–10.7 全绿 |

### 10.10 真实人工验收（我代你执行，纳入交付）

**环境**：本机 PG `:5433`（独立 `eztest_p5_acceptance` 库）、API `:13333`、Web `:6868`、真实 `llm_*`（`.env`，`deepseek-flash`）、fixture `:13335`、fixture 代码仓。**全程不依赖 fake LLM**。

**路径（以人类操作顺序）**：
1. 浏览器登录（管理员与普通用户各一次），建版本/绑定系统环境代码仓，录入 3 个需求并确认（可用真实澄清 Agent 或人工确认）。
2. 进入脑图页 → 应用探索（先 fixture 环境）→ 查看补全的应用模型；再触发测试设计（真实 LLM）→ 观察 SSE 活动流/run 状态 → 检查生成用例质量（场景结构、步骤/断言、优先级、需求覆盖、锚点）。
3. 以评审者身份：改 1 条用例、删 1 条断言、加 1 条锚点；触发评审 → 阅读 findings → 处置 1 条 → 以 blocker 勾选确认 → 批准。
4. 批准后：验证只读、baseline 徽章、再次批准 409；导出 Markdown/OPML/Mermaid 并打开文件核对；导出 PNG 并查看。
5. 普通用户：编辑/评审可用；探索入口不可用（403/隐藏）。
6. 降级演练：临时清空 LLM 配置，刷新后手工新建/编辑/评审/导出一条完整链路；恢复配置。
7. 断线演练：脑图页刷新/断网重连，事件按 seq 补齐，无重复/丢失。
8. 证据留档：截图（≥10 张，`.tmp/p5-acceptance/`）、三个导出文件、DB 行数/关键行、事件快照 JSON、run 统计；写入 `.superpowers/sdd/2026-10-28-eztest-p5-mindmap/acceptance.md`（gitignored），最终在会话里向你汇报"通过/不通过 + 证据索引"。

**判定**：§10.9 全表有证据且无未解释的失败 = P5 验收通过；任何一条不通过 → 记缺陷、修复后重跑该条及关联回归，不跳过。

---

## 11. 风险与降级

| # | 风险 | 影响 | 对策/降级 |
|---|---|---|---|
| R1 | LLM 生成结构不合规/质量差 | 物化失败或脑图不可评审 | 严格 schema + 服务端校验 + 上限（60 case/30 step）+ 重试 1 次 + 失败零物化；人工全链路替代；质量迭代归 P8 评测 |
| R2 | 运行时 Agent 非确定性 | 验收脆弱 | 单一提交 + fake LLM 脚本化；真实 LLM 验收只断言结构与关键语义，不断言具体文案 |
| R3 | 改名迁移跨 39 表 | 遗漏 SQL 引用/线上兼容 | 10 条上下文内迁移 + 全仓引用扫描 + 集成测试断言旧表不存在/新表存在 + 迁移指纹幂等；一次性完成、不做兼容视图 |
| R4 | 冻结与版本状态跨上下文不一致 | executing 无 baseline 或 baseline 无状态 | 先写 baseline 再调 `ApproveBaseline`，失败补偿删除 baseline 并回滚节点状态；并发测试覆盖 |
| R5 | 探索 SSRF/误操作 | 打到内网/改数据 | 仅 GET/HEAD、白名单、私网拦截、跨域重定向拒绝、请求/深度/超时上限、跳过状态变更链接、admin 触发 |
| R6 | OPML/Mermaid 兼容性 | 第三方导入器展示差异 | 只用标准 OPML 2.0 + `_note`；Mermaid 仅结构；文件内容用 XML/Mermaid 语法解析测试；详情以 Markdown 为准 |
| R7 | PNG 前端光栅化差异 | 产物不一致 | 固定 viewBox/字体回退/缩放；PNG 定位为视图快照不入库；E2E 只断言下载与 PNG 魔数 |
| R8 | 大图性能 | 画布卡顿 | 300 节点上限 + 折叠默认 + 结构签名防抖 + 分层渲染；超限 409 `graph_too_large` |
| R9 | 事件/SSE 丢事件 | UI 状态陈旧 | 复用 seq/Last-Event-ID/快照游标；`useMindmap` 在重连/`stream.reset` 时全量刷新；run 轮询兜底 |
| R10 | 需求重开/代码变更导致脑图过期 | 评审依据过时 | 覆盖矩阵 `staleRequirementIds` + `impactCases` 只读提示；P5 不自动改状态，P7 处理 |
| R11 | 迁移与 P4 锚点契约冲突 | 锚点 422 | 单独 `codeintel_0004` 扩宽 CHECK + `ValidateSubjectKind` 增 `case` + 既有 3 类主体行为不变；契约测试 |

---

## 12. 交付物

**代码**：`backend/internal/mindmap/**`（四层）、`version` 三个状态方法、`resource` 的 `AppModelInput.Source` 扩展、`codeintel` 锚点主体扩展、`cmd/eztest` 组装、`frontend/src/features/mindmap/**`、`frontend/src/app/(app)/versions/[id]/mindmap/page.tsx`、`frontend/src/features/versions/VersionHeader.tsx`/`OverviewTab.tsx`/`nav-items.ts`/`shell/AppShell.tsx` 小改。

**迁移**：13 条（§3.4）+ `order.txt`。

**测试与脚本**：Go 单测/集成；前端单测；`frontend/e2e/mindmap.spec.ts` + `p5-helpers.ts`；`scripts/fake-llm.cjs` P5 模式；`scripts/acceptance.ps1` P5 区块；`scripts/acceptance-mindmap-llm.ps1`；`scripts/fixture-target.cjs` P5 页面。

**配置/文档**：`.env.example` 新增 `EZTEST_EXPORT_DIR`（默认 `.data/exports`）、`EZTEST_MINDMAP_MAX_CASES`（60）、`EZTEST_MINDMAP_WORKER_INTERVAL_MS`（2000）、`EZTEST_EXPLORE_MAX_REQUESTS`（40）、`EZTEST_EXPLORE_TIMEOUT_MS`（5000）；`docs/openapi/api.yaml`；`README.md` P5 runbook；本规格与实现计划；`.superpowers/sdd/2026-10-28-eztest-p5-mindmap/acceptance.md`。

**文档同步**：见 §13。

---

## 13. 设计变更与文档同步

### 13.1 拟新增决策原文（写入 `system-blueprint.md` §3.2）

- **D34（数据库命名规约）**：全项目表名/索引名遵循阿里《Java 开发手册》数据库规约并做 PostgreSQL 适配——表名单数、小写+下划线、禁保留字、推荐 `业务_作用`；索引 `pk_<表>/uk_<表>_<列>/idx_<表>_<列>`；P5 起新表沿用 `created_at/updated_at`（对"id/gmt_create/gmt_modified 三字段"的显式例外）；历史表以新增 rename 迁移改名，字段/约束/既有索引名不变，`schema_migrations` 为框架表例外；规约同步 `Agent.md` §6.9 与蓝图 §10.2。
- **D35（P5 用例脑图与评审）**：新增 `mindmap` 限界上下文（`Agent.md` §6.1 登记）与 9 张 `case_*` 表；树/步骤/断言/来源/评审/基线/run/导出模型按本规格 §5–§7；状态流转 `designing→reviewing→executing`，驳回回 `designing`，基线冻结后只读且只评审一次（D5）；4 个 Agent 采用"P1 运行时 + 单一结构化提交工具 + 服务端校验单事务物化"，不新增配置面；评审 blocker 批准需显式确认；导出见 D38；`affected.cases` 在 P5 只读映射，正式接入归 P6/P7。
- **D36（应用探索边界）**：应用探索仅 HTTP 只读（GET/HEAD）、环境白名单、请求/深度/超时上限、跳过状态变更链接；管理员触发；产物 `app_model_nodes.source='explored'`（扩宽 CHECK + `AppModelInput.Source`）；LLM 不可用时确定性 BFS 降级；浏览器探索与 Session 注入归 P6。
- **D37（锚点主体扩展 case）**：`code_anchors.subject_kind` 扩宽至含 `case`；P4 规格中"case 预留"正式接入；不改既有字段/唯一索引/其他主体行为；锚点经 codeintel `AnchorService` 创建/删除，由 mindmap 调用。
- **D38（脑图导出）**：服务端 Markdown/Mermaid/OPML 三格式落文件系统（D30）+ sha256 + 元数据；PNG 由前端对 SVG 画布光栅化下载（不入库）；关闭蓝图 §17.4（XMind 非必需，OPML 为通用导入格式）。

### 13.2 同步修改清单

| 文件 | 修改 |
|---|---|
| `docs/design/system-blueprint.md` | §3.2 增 D34–D38；§5.1/§5.2/§5.4/§5.5 增补脑图/锚点/状态；§10.2 表名更新为规范名并附映射；§11.1 增 mindmap 端点；§11.2 增 8 个事件；§12.2 PG4 交付说明；§17.4 关闭 |
| `Agent.md` | §6.1 目录树增 `mindmap`、表名映射说明；新增 **§6.9 数据库命名规约（D34）**；§6.3 迁移规则引用 §6.9；§4 文档地图无需新增文档 |
| `docs/design/frontend-design.md` | §3 PG4 交付说明、§4.2 入口、§5.3 评审流（approve→executing 已一致）、组件表补 MindmapCanvas/ReviewPanel |
| `docs/design/implementation-phases.md` | 不改范围；如需可在 §2.9 #15/#21 备注"P5 已交付"（非设计变更） |
| `docs/openapi/api.yaml` | 27 端点 + 8 事件 + 错误 reason + 新 schema |
| `README.md` | P5 runbook：导出目录/环境变量、真实 LLM 验收、E2E、探索白名单、迁移改名说明 |
| `.env.example` | §12 新增键 |

> 历史阶段规格（P2/P3/P4）不改写；P4 规格 §6.7 "case 预留" 的变更以 D37 追认，P4 规格文末如需可加一行"由 D37 更新"，不改原文。

---

## 14. 明确不做（再确认）

- 执行引擎/批次/证据/失败分类（P6）；P5 只到"批准基线 + 可选执行态"。
- AI 补全（`ai_supplement`）与回填、needs_revalidation 自动标记、继续中（P7）。
- 问题/报告实体与页面（P7）。
- 浏览器探索/Playwright/Session 注入/自动登录（P6）。
- 符号级代码分析、parser（D19）；P5 只消费 P4 事实。
- PNG 服务端渲染、XMind 文件、PDF 导出。
- 多用户协作/读写锁/评审分配、权限细分（P8）。
- 新图库/新前端依赖/新 Go 依赖。
- 版本归档/删除/克隆（P8）。
- 探索写入"人工确认后入库"的审批流（P5 直接入库并标注 source=explored，可后续人工修正）。

---

## 15. 已知假设与待验证

1. **XMind/第三方兼容**：不再交付 XMind；OPML 2.0 是通用导入格式，具体导入器对 `_note` 的展示差异不影响数据完整性（验收只保证结构/文本正确）。
2. **PNG**：以当前 Chrome/Edge 对 SVG→canvas 的支持为准；字体回退可能造成与屏幕渲染的细微差异，属可接受。
3. **Agents 生成质量**：真实 LLM 生成质量取决于提示词与 P3/P4 输入质量；P5 只保证"可评审/可编辑/可导出"，质量迭代归 P8 评测（沿用 implementation-phases §2-P5 风险口径）。
4. **改名迁移**：假定当前无外部系统直接读旧表名；改名一次性完成、不保留兼容视图（内部项目，已确认）。
5. **`case` 锚点与 P4 数据**：假定 `code_anchors` 既有数据只含 version/requirement/app_model，扩宽 CHECK 不影响历史行。
6. **探索目标**：假定环境 `allowed_hosts` 与 BaseURL 已由管理员正确配置；P5 不自动发现/猜环境。
7. **P1 运行时**：假定 P1 的 agent run 在同一进程内可观测（`SessionService.Get` 的 `Running/LastRun`）；跨实例部署不在 P5 范围。
8. **P6 衔接**：`executing` 状态在 P5 结束时可存在且无批次；P6 首次建批次即消费该状态（需 P6 规格确认）。

---

*规格版本：rev2（2026-10-28）；与 system-blueprint v2.2 配套；实现计划由 `writing-plans` 产出。*
