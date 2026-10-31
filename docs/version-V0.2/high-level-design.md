# EzTest 0.2 概要设计（讨论稿）

> 状态：讨论稿，基于 [架构设计](architecture-design.md) 与 [设计决策日志](research/decision-log.md)。
> 范围：模块分解、数据设计、任务板、Agent/工具、上下文、核心流程、接口/事件、前端、P9、测试。
> 本文不是 spec/plan/task；它是进入 Superpowers 工程流前的"设计蓝图"。

---

## 1. 模块分解

### 1.1 后端（Go 模块化单体）

| 模块/上下文 | 职责 | 0.1 关系 |
|---|---|---|
| `platform` | clock/id/errors/validation/crypto/pagination | 移植 |
| `identity` | 用户/登录/token | 移植 |
| `settings` | 系统设置中心（DB + 管理 UI + 热加载） | 新增 |
| `llm` | 供应商/模型目录/网关/流式/重试 | 移植 + 扩展 |
| `eventing` | 持久事件/SSE/跨实例 fan-out | 移植 + 多副本改造 |
| `storage` | 内容对象/引用账本（附件/证据/报告/导出） | 改造 |
| `audit` | 追加式审计 | 移植 |
| `resource` | SUT/环境/页面/目标库/测试账号/代码仓/版本绑定 | 重写 |
| `version` | 版本投影/需求版本/关系/澄清对话 | 重写 |
| `design` | 场景/用例版本/确认/设计轮次 | 重写（0.1 mindmap） |
| `execution` | 波次/用例 run/多 SUT 上下文/证据/暂停恢复 | 重写 |
| `delivery` | 问题/报告/分析 | 重写/扩展 |
| `knowledge` | 全局知识/晋升/审核/注入 | 重写（0.1 版本内） |
| `agent` | profile/run/session/message/context/tools | 重写运行时 |
| `orchestration` | 任务板/reconciler/团队频道/Version Steward | 新增 |
| `chain` | 探索链路只读读模型 | 重写 |
| `codeintel` | 代码索引/diff/影响/锚点（全功能对齐） | 移植 + 适配 |
| `visual` | 视觉基线/断言/差异（全功能对齐） | 移植 + 适配 |
| `quality` | 评测/质量中心 | 移植 + 适配 |
| `search` / `workspace` | 全局搜索/待办 | 移植 + 适配 |
| `renderer`（或 delivery 子模块） | 报告 HTML/PDF 渲染 worker | 新增 |

**依赖规则**：沿用 0.1 的四层 + 端口/适配器；跨上下文只依赖 application 端口；装配根唯一。

### 1.2 前端（Next.js/React）

| Feature | 页面/职责 |
|---|---|
| `shell` | 全局导航、版本切换、搜索、待办、用户 |
| `versions` | 版本列表/创建/绑定/总览 |
| `clarification` | 需求与澄清：对话主区 + 需求列表/编辑 |
| `design` | 用例设计：脑图 + 设计对话 + 用例详情/确认 |
| `execution` | 波次/实时/结果/证据/暂停恢复 |
| `chain` | 探索链路动态图 |
| `delivery` | 问题/报告（版本内 + 全局） |
| `knowledge` | 全局知识 |
| `resources` | SUT/环境/页面/库/账号/代码仓 |
| `visual` | 视觉基线/审批/actual/diff |
| `quality` | 质量中心/评测 |
| `agent-center` | Agent 目录/运行观察/配置 |
| `settings` | 系统设置中心 |
| `code-intel` | 代码与变更 |
| `search` | 全局搜索（跨实体） |
| `todos` | 全局待办（评审/接管/人工请求） |
| `shared/chat` | Markdown/Transcript/Composer/附件/会话切换/确认卡/HumanRequestCard |
| `shared/graph` | 脑图/链路通用图组件 |

### 1.3 执行器（Node/Playwright）

- 保持独立进程；API 不装 Chromium。
- 扩展：一个 case_run 可创建多个 SUT 的 browser context；动作带 `context_id`；证据带 SUT 作用域。
- 不接触 LLM 上下文；只执行动作、回传结果/证据。
- 多副本：执行器注册/心跳/容量/排空；API 按健康+容量选择执行器；case_run 带 `owner_executor_id` + `dispatch_epoch` + `lease_until`，结果写入校验 epoch/token，拒绝迟到写回。
- 执行器与 Agent 的边界：Case Executor（Agent，跑在 API 进程）决定做什么；Playwright 执行器只执行动作/回传证据，不接触 LLM 上下文。

### 1.4 P0–P8 → 0.2 全功能对齐映射

| 0.1 能力 | 0.2 模块/入口 | API/UI | 验收锚点 |
|---|---|---|---|
| P0 身份/LLM/事件/存储/审计/SSE | identity/llm/eventing/storage/audit/settings | `/api/auth/*`、`/api/admin/*`、`/api/stream*`；全局壳/设置 | 登录/流式/回放/审计/设置生效 |
| P1 Agent 运行时 | agent/orchestration | `/api/agents/*`、`/api/agent-center/*`；Agent 中心 | run/会话/工具/预算/租约/恢复 |
| P2 公共资源 | resource | `/api/resources/*`；资源页 | SUT/环境/页面/API/库/账号/代码仓 CRUD |
| P3 版本/需求/澄清 | version | `/api/versions/*`、`/conversations/*`、`/requirements/*`；需求与澄清 Tab | 对话/草案/确认/回退/批量/关系 |
| P4 代码智能 | codeintel | `/api/code/*`、`/versions/{id}/code/*`；代码与变更 Tab | 索引/diff/影响/锚点/事实复核 |
| P5 用例脑图 | design | `/versions/{id}/design/*`；用例设计 Tab | 需求→场景→用例/版本/确认/导出 |
| P6 执行与证据 | execution | `/versions/{id}/waves/*`、`/evidence/*`；执行 Tab | 波次/多 SUT/四态/证据/暂停恢复 |
| P7 交付 | delivery | `/versions/{id}/issues/*`、`/versions/{id}/reports/*`；问题/报告 Tab | 问题/报告/revision/stale/分析 |
| P8 硬化：链路/搜索/待办/知识/质量/视觉/多实例 | chain/search/workspace/knowledge/quality/visual + runtime | `/exploration-chain`、`/search`、`/todos`、`/knowledge`、`/quality`、`/visual`；全局壳 | 链路实时/搜索/待办/知识审核/评测/视觉基线/多副本安全 |
| P9 五项 | delivery/knowledge/storage/orchestration | 报告 HTML/PDF、知识、GC、执行器/多副本、问题重开合并 | 见 §9 与架构 §12 |

> 全功能对齐的详细表见 [capability-matrix.md](research/capability-matrix.md)；该表已按“选项 C 全对齐”改判。

---

## 2. 数据设计

### 2.1 实体关系总览

```text
SUT ─< SUTEnvironment ─< SUTPage
SUT ─< TargetDatabase
SUT ─< TestAccount
SUT ─< CodeRepository

Version ─< VersionSUT ─< VersionResource
Version ─< Requirement ─< RequirementVersion
Version ─< Conversation ─< Message
RequirementVersion ─< Scenario ─< Case ─< CaseVersion
CaseVersion ─< CaseStep / CaseAssertion / CaseSource / CaseConfirmation

Version ─< RunWave ─< CaseRun ─< CaseRunSUTContext
CaseRun ─< StepResult / AssertionResult / EvidenceRef
RunWave ─< Report
CaseRun / StepResult / Evidence ─< IssueRef
Version ─< Issue
KnowledgeItem（global/system）─< KnowledgeRevision

Version ─< WorkItem ─< WorkItemAttempt
Version × Team ─< TeamRoomPost
Conversation ─< ConversationSummary
AgentRun ─< ContextPacket / AgentMessage
```

### 2.2 表清单（按域）

#### 平台/设置

| 表 | 关键字段/约束 |
|---|---|
| `system_setting` | key 唯一、value_json、category、type、is_secret、restart_required、version、updated_by/at |
| `system_setting_revision` | setting_key、old/new、actor、at（审计/回滚） |
| `llm_config` | provider/protocol/base_url/key_cipher/headers/timeouts/default |
| `llm_model` | config_id、model、context_window、max_output、supports_tools/vision |
| `event` / `event_channel` | seq、channel、type、envelope、created_at |
| `content_object` | sha256 PK、size、mime、relative_path、created_at |
| `content_ref` | object_id、owner_kind、owner_id、role、created_at、deleted_at |
| `audit_log` | actor/action/target/request_id/ip/ua/metadata/at |
| `idempotency_key` | user+method+path+key、request_hash、response、expires_at |

#### 资源

| 表 | 关键字段/约束 |
|---|---|
| `sut` | name、intro、created_by、timestamps |
| `sut_environment` | sut_id、name、base_url、is_default、status |
| `sut_page` | sut_id、environment_id、name、url、route_template、page_type、auth_role、module、status(`approved\|proposed\|rejected`)、source(`admin\|ai\|import`) |
| `sut_api` | sut_id、environment_id、method、path、name、auth_role、source(`code\|openapi\|ai\|manual`)、status、schema_ref |
| `target_database` | sut_id、environment_id、name、kind(`postgres`)、dsn、username/password、read_only、status |
| `test_account` | sut_id、environment_id、username、password、roles JSON、notes、status |
| `code_repository` | name、path/url、branch、include/exclude、created_by、status |
| `version_sut` | version_id、sut_id、environment_id、code_repo_id、scope JSON（pages/apis）、status |
| `version_sut_resource` | version_sut_id、kind(`database\|account`)、resource_id、role、selected |
| `sut_account_session` | sut_id、account_id、storage_state、status、expires_at（可选，执行器会话复用） |

#### 版本/需求/澄清

| 表 | 关键字段/约束 |
|---|---|
| `version` | title、status(`initial\|running\|completed`)、auto_design_new_requirements BOOL、closed_at、close_reason、close_run_id、created_by、timestamps |
| `requirement` | version_id、current_version_id、status(`draft\|clarified\|closed\|obsolete`)、target_version_sut_ids JSON、closed_at、closed_by_run_id、created_by、source_conversation_id |
| `requirement_closure` | requirement_id、requirement_version_id、checks JSON、evidence_refs JSON、closed_by_run_id、closed_at、reopen_count |
| `requirement_version` | requirement_id、revision、title、description、acceptance_criteria、priority、constraints JSON、target_version_suts JSON、target_pages JSON、target_apis JSON、content_hash、created_by、created_at |
| `requirement_relation` | version_id、from_requirement_id、to_requirement_id、kind、description、source_revision_id |
| `conversation` | version_id、kind(`clarification\|design`)、title、status、created_by、created_at |
| `conversation_message` | conversation_id、role、parts JSON、refs JSON、token_usage、created_at |
| `conversation_summary` | conversation_id、summary、pinned_facts JSON、token_estimate、version、updated_at |
| `requirement_attachment` | requirement_id/version_id、content_ref_id、name、mime、size |
| `requirement_confirmation` | requirement_version_id、scope(`requirement\|batch`)、batch_id、actor_id、confirmed_at、affected_requirement_ids JSON |

**需求字段规则（已定）**：必填 `title/description/acceptance_criteria/priority`；推荐 `target_version_suts/target_pages/target_apis` 与 `constraints JSON` 下的 `business_rules/preconditions/exceptions/data_constraints/dependencies`。AI 草案允许空，但确认前必须提示/校验。

#### 设计/用例

| 表 | 关键字段/约束 |
|---|---|
| `scenario` | requirement_id、requirement_version_id、name、description、version_sut_ids JSON、page_ids JSON、api_ids JSON、sort |
| `case_entity` | scenario_id、current_version_id、status(`draft\|confirmed\|stale`，当前版本投影；superseded 只属于历史版本)、created_by |
| `case_version` | case_id、revision、title、description、preconditions、test_data JSON、priority、default_version_sut_id、version_sut_ids JSON、page_ids JSON、api_ids JSON、status、content_hash、created_by/at |
| `case_step` | case_version_id、seq、action、expected、target_version_sut_id（nullable=继承默认）、target_page_id、target_api_id、account_role、data JSON |
| `case_assertion` | case_version_id、step_seq、kind(`ui\|api\|db\|text\|visual`)、target、expected、target_version_sut_id、target_api_id、database_id |
| `case_source` | case_version_id、kind(`requirement\|code\|page\|api\|knowledge`)、ref_id、path、line、sha |
| `case_confirmation` | case_version_id、scope(`case\|scenario\|requirement`)、target_scope_id、batch_id、affected_case_version_ids JSON、snapshot JSON、confirmed_by/at |
| `design_round` | version_id、conversation_id、activation(`manual\|auto_gap\|auto_change\|auto_new`)、requirement_ids JSON、requirement_version_ids JSON、scope JSON、status、created_at |
| `design_scope` | version_id、requirement_id、activated_by_round_id、activated_at、status(`active\|closed`)；UNIQUE(version_id, requirement_id) |

#### 执行/证据

| 表 | 关键字段/约束 |
|---|---|
| `execution_intent` | version_id、case_version_id、version_sut_ids JSON、environment_fingerprint、code_baseline、purpose(`initial\|retest\|manual`)、status(`open\|claimed\|running\|done\|failed\|cancelled\|superseded`)、dedupe_key、attempt_no、created_by/at |
| `run_wave` | version_id、status、started_at/finished_at、trigger、counts JSON、report_id、paused_at/paused_by/pause_reason |
| `executor_instance` | id、name、endpoint、token_hash、capacity、labels、status(`active\|draining\|offline`)、last_heartbeat、version |
| `case_run` | wave_id、intent_id、case_version_id、status、attempts、failure_class、started/finished、summary、owner_instance、owner_executor_id、dispatch_epoch、dispatch_token、lease_until、paused_at |
| `case_run_sut_context` | case_run_id、version_sut_id、account_role、account_id、browser_context_id、storage_state、lease_epoch、status |
| `step_result` | case_run_id、seq、target_version_sut_id、status、observed、started/finished |
| `assertion_result` | case_run_id、step_seq、kind、verdict、reason、expected/actual、target_version_sut_id |
| `evidence` | content_ref_id、kind、sha256、size、version_sut_id、case_run_id、created_at |
| `evidence_ref` | evidence_id、owner_kind、owner_id、role、created_at |

#### 交付/知识

| 表 | 关键字段/约束 |
|---|---|
| `issue` | version_id、status(`open\|confirmed\|dismissed\|merged`)、category、severity、dedupe_key、merged_into_id、merged_at/by、reopen_count、fingerprint、source、expected_revision |
| `issue_ref` | issue_id、kind、ref_id、version_sut_id、created_at |
| `issue_status_history` | issue_id、from/to、reason、actor、at |
| `analysis_run` | kind(`failure\|coverage\|triage`)、status、input_ref、output JSON |
| `report` | version_id、wave_id nullable、kind(`wave\|version`)、current_revision_id、status(`draft\|published\|stale`) |
| `report_revision` | report_id、revision、status(`draft\|published\|stale`)、html_ref、pdf_ref、input_fingerprint、as_of、template_version、render_status(`pending\|running\|done\|failed`)、created_by/at；append-only |
| `report_ref` | report_id、report_revision_id、kind、ref_id |
| `export` | version_id、owner_kind(`case\|report\|chain`)、owner_id、revision、format(`markdown\|mermaid\|opml\|csv\|html\|pdf`)、content_ref、status、created_by/at |
| `knowledge_item` | scope(`global\|system`)、system_id nullable、kind、title、content、status(`draft\|approved\|rejected\|revoked`)、confidence、source_version_id、promoted_from_version_id、reviewed_by/at、revoked_at、tags、version |
| `knowledge_revision` | knowledge_id、revision、content、changed_by/at |
| `knowledge_usage` | knowledge_id、agent_run_id、injected_at、token_estimate |

#### Agent/运行时

| 表 | 关键字段/约束 |
|---|---|
| `agent_profile` | key、name、system_prompt、allowed_tools、model、budgets、enabled |
| `agent_run` | profile_key、work_item_id、conversation_id、status、terminal_reason、turns、usage、owner_instance、lease_epoch、context_packet_id |
| `agent_message` | run_id/conversation_id、role、parts、tool_calls、usage |
| `context_packet` | run_id、version_id、sections JSON、token_counts JSON、hash、created_at |
| `team_room_post` | version_id、team、kind、content、refs JSON、agent_run_id、created_at |
| `team_room_digest` | version_id、team、summary、pinned_post_ids JSON、token_estimate、updated_at |
| `work_item` | version_id、team、type、status、priority、dedupe_key、depends_on、payload、claimed_by、lease_until、lease_epoch、attempts、max_attempts、last_error |
| `work_item_attempt` | work_item_id、attempt、status、error、started/finished |
| `human_request` | version_id、kind(`login_takeover\|conflict\|other`)、payload、status、response、expires_at |

#### 代码智能/质量/搜索

- `code_index_run`、`code_entity`、`code_edge`、`code_fact`、`code_analysis`、`code_impact_candidate`、`code_anchor`、`code_fact_review`。
- `visual_baseline`、`visual_assertion`、`visual_diff`（全功能对齐）。
- `eval_run`、`eval_metric`。
- `search`/`workspace`/`chain` 无表（只读读模型）。

### 2.3 关键约束

- 版本内单活跃波：`UNIQUE (version_id) WHERE status IN ('planned','running','paused')`；暂停不释放“活跃”语义。
- 执行意图：`UNIQUE (case_version_id, environment_fingerprint, code_baseline, purpose) WHERE status IN ('open','claimed','running')`；新重跑/复测生成新 intent，不复用旧 intent。
- 用例版本确认：允许历史 confirmed 版本保留；执行唯一性由 `case_entity.current_version_id = case_version.id AND case_version.status='confirmed'` 保证（加唯一/索引校验）。
- CaseVersion 迁移：`draft→confirmed`；`confirmed→stale`（需求变化/失效）；新 draft 生成时**不改**旧 confirmed 状态；新版本被确认后，旧 confirmed 才 `→superseded`。历史报告仍引用旧版本。
- 需求版本：`UNIQUE (requirement_id, revision)`；当前版本由 `requirement.current_version_id`。
- 任务去重：`UNIQUE (version_id, type, dedupe_key)`；`dedupe_key` 由作用域 + 版本号/用例版本 + 环境生成。
- 问题去重：`UNIQUE (version_id, dedupe_key)`；合并只改状态/指针。
- 内容对象：sha256 主键；引用账本决定 GC。
- 所有 Agent 写入必须带 `agent_run_id` + 服务端校验；不允许模型直接写表。
- **SUT 标识约定**：资源层用 `sut_id`；执行层（场景/用例/步骤/断言/上下文/证据/问题引用）一律用 `version_sut_id`；页面/API/账号/DB 必须校验属于同一 `version_sut` 的 SUT+环境；`case_version.default_version_sut_id` 为步骤默认目标。

### 2.4 状态机（与架构一致）

- Version：`initial → running → completed → running`；initial→running 由首个需求/对话/设计/确认触发；completed 由 Agent 收尾；新增需求/确认/重开/新 intent 自动重开。详见 §6.7 迁移表。
- Requirement：`draft → clarified → closed`；`clarified → draft`（回退）；任意 → `obsolete`；`closed → draft`（重开）。
- CaseVersion：`draft → confirmed`；`confirmed → stale`（需求失效/退出执行）；新版本确认后旧 confirmed `→superseded`；draft 不直接 superseded。执行只取 current+confirmed。
- WorkItem：`open → claimed → running → done/failed/waiting_human/dead`。
- RunWave：`planned → running ↔ paused → done/failed/cancelled`；paused 持久化，恢复回 running。
- CaseRun：`pending → running ↔ paused → passed/failed/blocked/inconclusive/skipped/interrupted`；paused 持久化，恢复回 running。
- Issue：`open ↔ confirmed/dismissed`；`open/confirmed/dismissed → merged`。
- Report：`draft → published → stale`；revision 不可变。
- Knowledge：`draft → approved/rejected`；`approved → revoked`。

---

## 3. 任务板与 reconciler

### 3.1 任务类型

| 类型 | 团队 | 输入 | 输出 |
|---|---|---|---|
| `chat.turn` | 澄清/设计 Host | conversation_id、user message | assistant message、可能的需求/图草案 |
| `attachment.ingest` | 澄清 | content_ref、requirement/version | 文本/图片引用、摘要 |
| `requirement.curate` | 澄清 | 对话/附件/上下文 | draft 需求版本、关系建议 |
| `design.round` | 设计 | conversation_id、scope | 本轮设计任务/范围说明 |
| `design.requirement` | 设计 | requirement_version_id | 场景/用例草案 |
| `design.gap` | 设计 | 覆盖缺口/执行反馈 | 新 draft 用例 |
| `design.critic` | 设计 | 本轮图/需求 | findings/建议 |
| `design.data_plan` | 设计 | 用例版本 | 前置/测试数据 |
| `requirement.close` | 交付/Version Steward | 需求版本、覆盖、执行结果 | `requirement.goal_met` 提议 + 服务端校验 |
| `execute.case` | 执行 | intent_id、case_version_id、环境、绑定 | case_run/结果/证据 |
| `analyze.failure` | 执行/交付 | wave/case_run | 失败分类/问题候选 |
| `analyze.coverage` | 执行 | wave/需求 | 覆盖缺口/设计任务 |
| `issue.triage` | 交付 | 问题候选 | 合并/分诊建议 |
| `report.wave` | 交付 | wave | 波次报告 |
| `report.version` | 交付 | version | 版本总结 |
| `report.render_pdf` | 交付 | report_revision/html_ref | PDF 产物/重试 |
| `knowledge.review` | 交付 | 知识草案 | 审核/晋升 |
| `version.close` | Version Steward | version | close 提议（工具 `version.close_propose`，事件 `version.closed`） |
| `gc.run` / `index.repo` / `visual.*` / `search.*` | 运维/代码/视觉 | 各类 | 工程结果 |

### 3.2 认领与租约

- 领取：`SELECT ... FOR UPDATE SKIP LOCKED` 或单行 CAS；写 `claimed_by/lease_until/lease_epoch`。
- 心跳：长任务每 10–15s 续租；超过 `lease_until` 可被回收，`lease_epoch` 递增，旧实例写回被拒绝。
- 重试：失败按类型决定；max_attempts 后 `dead`；`dead` 在待办/团队频道暴露，人工处理。
- 幂等：任务创建用 dedupe_key；执行结果写入用 case_version + wave + SUT 幂等键；重复事件不重复执行。

### 3.3 reconciler 规则（示例）

```text
每 2–5s / 事件唤醒：
  对每个 running 版本：
    - 已澄清且无当前设计覆盖的 requirement_version → 若 requirement_id 在 `design_scope(active)` 中（已激活范围）、gap/change 自动范围、或版本开启 auto_design_new_requirements，ensure design.requirement；否则展示“待设计”待办，等待用户新建设计对话（D25 A+）
    - current+confirmed case_version + 环境/代码基线 + purpose 无 open/claimed/running intent → ensure execution_intent
    - case_version 非 current/confirmed（stale/superseded）→ cancel 其 active intent，终止在飞 case_run（superseded）
    - executor_instance 心跳超时/offline → 其 case_run 置 interrupted(executor_lost)、dispatch_epoch++、按 intent 重新派发；旧 epoch 写入拒绝
    - 可执行 intent 且无活跃 wave → ensure wave + execute.case
    - wave 终态且无 report.wave → ensure report.wave
    - 需求/波次全闭环 + 无 open intent + 无 report.version → ensure report.version
    - report_revision HTML published 且 PDF render_status ∈ pending/failed → ensure report.render_pdf
    - 用例 failed(application) 且无 issue → ensure analyze.failure
    - 覆盖缺口 → ensure design.gap
    - 收尾谓词（无 open/claimed/running/waiting_human work_item、无 active intent、无 open human_request、无活跃/暂停 wave/在飞 case_run、需求全 closed/obsolete、report.version published）→ ensure version.close
  对所有版本（含 completed）：
    - 新需求/需求重开/新用例确认/新 intent → 清 closed_at → running
```

- reconciler 只创建/关闭任务，不直接执行业务动作。
- 事件用于低延迟；扫描兜底漏事件与重启恢复。
- 所有规则必须幂等，重复扫描不产生重复任务。

### 3.4 团队频道

- 帖子写入与任务/作品同事务或紧邻事务；planner 读摘要，worker 读相关帖子。
- 摘要由 `team_room_digest` 维护：每 20 条或 10 分钟生成，保留最近 N 版；帖子全量审计；不全文回放。
- 人类消息进入频道 → 生成 `chat.turn`（若属于对话）或 `human_request`；不直接触发 Agent；不支持自由群聊控制面。

---

## 4. Agent 与工具设计

### 4.1 Profile 与运行

- `agent_profile`：key、system_prompt、allowed_tools、model、max_turns、run_seconds、hard_timeout、max_output、thinking。
- 每个团队 Host/Planner/Worker 都有 profile；planner/worker 可用不同模型。
- `agent_run` 绑定 work_item/conversation；`run scope` 运行时注入。
- Agent 不能用 `versionId` 作为自由参数；工具 schema 只接受业务参数，运行时补作用域。

### 4.2 工具清单（按域）

| 域 | 工具 |
|---|---|
| 上下文 | `context.version_pack`、`context.task_pack`、`context.summary_update`、`context.team_room_read` |
| 需求 | `requirement.draft_create`、`requirement.draft_update`、`requirement.relation_propose`、`requirement.reopen_propose`、`requirement.goal_met`、`requirement.read` |
| 澄清 | `conversation.reply`、`attachment.read_text`、`attachment.list`、`attachment.image_ref` |
| 资源 | `sut.list`、`sut.page_list`、`sut.page_propose`、`account.credentials`、`database.schema_read` |
| 设计 | `design.graph_read`、`design.scenario_propose`、`design.case_propose`、`design.case_update`、`design.review_submit`、`design.data_plan_submit` |
| 代码 | `code.tree`、`code.read`、`code.search`、`code.facts`、`code.routes`、`code.apis`、`code.diff`、`code.impact` |
| 执行 | `browser.*`、`http.request`、`db.query`、`execution.step_record`、`execution.assertion_record`、`execution.evidence` |
| 交付 | `issue.propose`、`issue.merge_propose`、`issue.reopen_propose`、`report.data`、`report.narrative_propose`、`knowledge.propose`、`version.close_propose` |
| 安全/治理 | `human.request`（登录接管/冲突）、`budget.check` |

**写工具原则**：只提交候选；服务端校验作用域/版本/引用/状态/证据/去重/权限；高风险动作需用户/服务端确认。

### 4.3 结构化提交

- 每种 AI 产物对应 schema + 校验器：
  - 需求草案：字段完整、目标 SUT/页面存在、优先级合法。
  - 场景/用例：层级、步骤连续、断言合法、SUT/页面存在、覆盖需求版本。
  - 失败分析：证据引用存在、分类合法、问题去重键。
  - 报告：只允许叙述候选，正文/统计确定性生成。
  - 收尾：无开放任务/需求全闭环/报告已发布。
- 校验失败返回结构化错误给 Agent，允许修正；不直接落库。

### 4.4 预算与恢复

- max_turns / soft timeout / hard timeout / tool budget / token budget。
- 失败分类：model/tool/budget/context_compacted/aborted/interrupted。
- 任务失败重试有上限；死信暴露；人工可重跑/取消。
- 实例崩溃：只恢复过期租约；不批量中断所有 running（修正 0.1 D67）。

---

## 5. 上下文设计

### 5.1 上下文包 Schema

```json
{
  "version_id": "...",
  "task": {
    "type": "design.requirement",
    "goal": "...",
    "scope": {"requirement_version_id": "...", "version_sut_ids": ["..."]},
    "constraints": ["..."],
    "acceptance": ["..."]
  },
  "version_pack": {
    "suts": [{"id": "...", "name": "...", "pages": ["..."]}],
    "requirements": [{"id": "...", "version": 3, "summary": "...", "status": "clarified"}],
    "coverage": {"cases": 9, "confirmed": 5, "closed_requirements": 2},
    "issues": [{"id": "...", "severity": "major", "status": "open"}],
    "knowledge_refs": ["..."]
  },
  "conversation": {
    "recent_messages": ["..."],
    "summary": "...",
    "pinned_facts": ["..."]
  },
  "team_room": {"digest": "...", "relevant_posts": ["..."]},
  "artifacts": [{"kind": "requirement_version", "id": "...", "ref": "..."}],
  "token_budget": {"total": 64000, "used": 12000, "reserved_output": 8000}
}
```

- `context_packet` 持久化 hash/各段 token/引用；run 可复现。
- 版本包可缓存，按需求/用例/问题变更增量刷新。
- 任务包每次重建；会话记忆只用于 Host；worker 不继承聊天全文。

### 5.2 压缩策略

1. 估算 token（模型 tokenizer/近似）；预算 = window − output reserve − tool/evidence reserve。
2. 达到 65–75% → 生成滚动摘要：
   - 保留：钉住事实、决定、未决问题、需求/用例引用、错误。
   - 压缩：旧对话、已完成工具调用、重复上下文。
3. 摘要失败 → 确定性裁剪（保留 system + 最新任务 + 钉住事实）。
4. 仍超 → 工具结果落盘、减少可选段、拆子任务/子会话、人工请求。
5. 记录 `context_compacted` 事件；不把 run 判失败。

### 5.3 工具结果与证据

- 单条工具结果上限（如 32 KiB），超出落盘为 content_object，只回 head + 引用。
- 证据/附件以引用传递；Agent 按需 `read_ref` 分页读取。
- 执行用例上下文只含当前用例/步骤/前置/环境/账号角色/历史尝试，不含整波次。

---

## 6. 核心流程

### 6.1 管理员配置

1. 登录 → 用户管理。
2. SUT：名称/简介/环境入口；应用模型维护（页面/API：新增/导入/编辑/停用）；AI 页面/API 提议 → 管理员确认。
3. 目标库（PG 只读）、测试账号（账号/密码/角色）、代码仓（可选）。
4. 系统设置：LLM/模型、Agent profile、执行限额、路径、GC、报告模板、知识 scope。
5. 所有配置写审计；secret 掩码。

### 6.2 版本创建与绑定

1. `POST /api/versions`：标题、SUT 列表。
2. 每个 SUT：选环境入口、页面/API 范围、目标库、测试账号、代码仓。
3. 写入 `version` + `version_sut` + `version_sut_resource`；生成版本上下文包。
4. 版本 `initial`；用户进入需求与澄清。

### 6.3 澄清

1. `POST /versions/{id}/conversations {kind: clarification}` → conversation + `chat.turn`。
2. Host 读版本包，开场；用户消息/附件。
3. `attachment.ingest` 解析；Curator 产出 draft 需求版本；Host 对话解释；关系建议。
4. 用户编辑/单条/批量确认；写 `requirement_confirmation`（scope/batch/actor/time/affected）；AI 可提议回退；回退生成新 draft + 用例 stale + 设计任务。
5. 新会话 = 新线程；上下文包 + 历史线程摘要；不全文回放。

### 6.4 设计

1. `POST /versions/{id}/conversations {kind: design}` → `design.round` 任务。
2. Planner 扫描待设计需求（未设计、需求版本变化、stale、执行缺口），按 `design_scope(active)` 判断已激活范围，发布本轮范围说明。
3. Case Author 产出场景/用例草案（页面/API 目标）；图增量事件实时渲染；Critic 自审；Data Planner 补前置。
4. 用户对话修改；AI 只能 draft；用户确认（case/scenario/requirement），写 `case_confirmation`（scope/target/batch/affected/snapshot）。
5. 确认 → case_version confirmed → execution intent；执行团队自动领取。
6. 首轮后设计团队保持覆盖职责：缺口/变化自动补 draft；默认新增需求批次需新设计对话激活。
7. D25（A+）：当前设计对话活跃时，新澄清需求自动并入本轮并写入 `design_scope`；版本级 `auto_design_new_requirements`（默认关）开启后，新需求自动开设计轮次并自动创建设计对话。

### 6.5 执行

1. Reconciler 为 current+confirmed case_version + 环境/代码基线创建 `execution_intent`；同组合 active intent 唯一。
2. Execution Planner 无活跃 wave → 用 intent 组波；跨系统用例整体调度；每 SUT 配额。
3. Case Executor 领取 case_run（intent_id）；为每个目标 SUT 建上下文/账号；按步骤执行；写 step/assertion/evidence。
4. Wave 终态 → failure/coverage analysis → report → issue triage；intent 置 done/failed。
5. 新确认用例产生新 intent 进下一波；人工重跑/复测新建 intent；缺口 → design.gap。
6. 遇到 2FA/验证码/登录无法完成 → 创建 `human_request`，波次进入等待；用户完成后恢复，超时按策略失败/跳过。

### 6.6 交付

1. 问题候选：仅 `failed(application)`；服务端 dedupe；人工确认/驳回；重开/合并。
2. 报告：波次 + 版本；HTML/PDF；revision/as-of/stale；旧 revision 不覆盖。
3. 知识：版本来源晋升全局；审核；注入；撤回。
4. 需求闭环：`requirement.close` 任务调用 `requirement.goal_met`；服务端校验覆盖、终态结果、失败应用用例的 issue 引用、无开放任务；通过则写 `requirement_closure` 并置 closed。失败转问题不阻止闭环，但必须有 issue 引用或 waived 记录。

### 6.7 收尾/重开

1. Version Steward 检查条件；`version.close_propose`。
2. 服务端校验 → completed；记录 close 原因/run。
3. 新需求/确认/重开/新 intent → 清 completed → running；报告后续 revision。

**版本状态迁移表（唯一口径）**

| 当前 | 触发 | 目标 |
|---|---|---|
| `initial` | 首个需求创建 / 首个对话消息 / 首次设计对话 / 首次确认 | `running` |
| `running` | 收尾谓词满足并通过 `version.close_propose` | `completed` |
| `completed` | 新需求创建 / 需求重开 / 新用例确认 / 新 execution intent / 人工重开 | `running`（清 closed_at） |
| `running` | 新增需求/确认用例/复测 | `running`（不改变状态） |

**收尾谓词（一份）**：无 open/claimed/running/waiting_human work_item；无 open/claimed/running execution_intent；无 open human_request；无活跃/暂停 wave 或在飞 case_run；所有需求 `closed/obsolete`；当前**版本总结报告** `published`。只有 `running` 版本做收尾评估；`completed` 版本只做重开扫描。

### 6.8 探索链路

- 读模型聚合各域 provider；节点/边带 SUT/页面/波次/状态；过滤；深链；实时 SSE；历史回放。
- 链路节点预算/截断/重连；问题/报告深链。

### 6.9 配置中心

- env 4 项 → DB settings → 默认值；管理 UI 分类；热加载；restart_required 标记。
- 设置变更审计；secret 掩码；启动时校验。

---

## 7. 接口与事件

### 7.1 REST 分组（概要）

| 前缀 | 内容 |
|---|---|
| `/api/auth/*` | 登录/登出/me |
| `/api/admin/users/*` | 用户管理 |
| `/api/admin/settings/*` | 设置中心 |
| `/api/admin/llm/*` | LLM 配置/模型/测试 |
| `/api/resources/*` | SUT/环境/页面/库/账号/代码仓 |
| `/api/versions/*` | 版本/绑定/需求/关系/对话/消息 |
| `/api/versions/{id}/requirements/*` | 需求版本/确认/批量/重开 |
| `/api/versions/{id}/conversations/*` | 澄清/设计对话 |
| `/api/versions/{id}/design/*` | 场景/用例/确认/图 |
| `/api/versions/{id}/waves/*` | 波次/用例 run/证据/暂停 |
| `/api/versions/{id}/human-requests` / `/api/human-requests/{id}/resolve` | 登录接管/人工例外请求与响应 |
| `/api/versions/{id}/issues/*` | 问题/重开/合并 |
| `/api/versions/{id}/reports/*` | 波次/版本报告/HTML/PDF/diff |
| `/api/versions/{id}/exports` / `/api/exports/{id}` | 用例/报告/链路导出与下载 |
| `/api/versions/{id}/exploration-chain` | 链路读模型 |
| `/api/versions/{id}/code/*` | 代码智能 |
| `/api/versions/{id}/visual/*` | 视觉基线/断言/差异 |
| `/api/issues` / `/api/reports` | 全局问题/报告中心（只读/深链） |
| `/api/knowledge/*` | 全局知识 |
| `/api/internal/executors/*` | 执行器注册/心跳/排空（token 鉴权） |
| `/api/agents/*` / `/api/agent-center/*` | Agent 配置/观察 |
| `/api/quality/*` | 评测/质量 |
| `/api/search` / `/api/todos` | 搜索/待办 |
| `/api/stream*` | SSE |

### 7.2 事件（示例）

- `version.created/started/closed/reopened/status_changed`
- `requirement.created/version_created/clarified/reopened/closed`
- `conversation.created/message/agent.activity/summary_updated`
- `design.round_started/graph_delta/case_version_created/case_confirmed`
- `wave.created/started/finished/paused`
- `case_run.started/finished/step.finished/evidence.created`
- `issue.created/updated/reopened/merged`
- `report.published/stale`
- `work_item.created/claimed/failed/dead`
- `team_room.post`
- `settings.updated`
- `knowledge.proposed/approved/revoked`

envelope 带 `version_id/requirement_id/case_id/wave_id/case_run_id/agent_run_id/version_sut_id/page_id/api_id` 等作用域；执行域用 `version_sut_id`，不用裸 `sut_id`。

### 7.3 错误/幂等

- 统一错误信封；稳定 reason 码（state_conflict/invalid_argument/forbidden/not_found/ai_unavailable/context_compacted/lease_lost）。
- Idempotency-Key 用于创建波次/确认/导出等；作用域 user+method+path。
- 租约丢失返回 `lease_lost`；UI 提示重试/刷新。

---

## 8. 前端设计

### 8.1 路由

- `/versions`、`/versions/[id]?tab=overview|requirements|design|execution|chain|issues|report|code`
- `/issues`、`/reports`、`/knowledge`、`/resources`、`/quality`、`/agents`、`/settings`
- 深链：`?requirement=`, `?case=`, `?wave=`, `?issue=`, `?report=`, `?node=`, `?view=visual`。
- **全局搜索** = Topbar CommandPalette（⌘K），不占导航项；**待办** = Topbar TodoDrawer，不占导航项；**视觉断言** = 执行 Tab 的 `view=visual` 子视图 + 质量中心指标，不新增一级 Tab。

### 8.2 组件树（关键）

```text
AppShell
 ├─ Sidebar / Topbar / VersionSwitcher / CommandPalette / TodoDrawer
 └─ VersionWorkspace
     ├─ VersionHeader（SUT chips/status/AI brief）
     ├─ VersionTabs（8）
     ├─ RequirementsTab
     │   ├─ ChatPanel（SessionSwitcher/MessageList/Composer/AttachmentUploader/ConfirmCard）
     │   └─ RequirementSidebar（Filter/List/Editor/BulkConfirm）
     ├─ DesignTab
     │   ├─ DesignMindmap（RequirementRoot/Scenario/CaseCard/SUTBadge/PageBadge）
     │   ├─ DesignChat
     │   └─ CaseDetail（Steps/Assertions/Confirm/RequestChange）
     ├─ ExecutionTab（WaveList/LiveTranscript/Evidence/Results/VisualAssertions）
     ├─ ChainTab（ChainGraph/Filter/Legend/DetailSheet/Replay）
     ├─ IssuesTab / ReportTab / CodeTab
     └─ OverviewTab
```

### 8.3 状态与数据流

- 以 URL 为深链真值；服务端快照 + SSE 合并；避免全局 store 依赖。
- Chat：先 POST 消息（服务端落库）→ SSE 流式回放；无乐观消息（或仅临时 UI，成功后替换）。
- 图：图增量事件 + 快照；断线重连按游标补齐；节点位置本地保留。
- 确认：POST 确认 → 服务端校验 → 事件 → 列表/图状态更新。
- 错误：统一 toast/内联错误；租约丢失/版本过期提示刷新。

### 8.4 关键交互

- 需求：新增对话、历史会话、附件、Markdown、单条/批量确认、AI 回退卡。
- 设计：新建对话、AI 实时生成、按用例/场景/需求确认、请求修改、导出（Markdown/Mermaid/OPML/CSV，可扩展）。
- 设计待办：存在“已澄清但未设计”的需求且未开启自动设计时，版本详情/待办中心展示“N 条待设计”CTA；不做设计会阻止需求闭环/版本收尾。
- 版本设置：`auto_design_new_requirements` 开关（默认关）；开启后新需求自动开设计轮次并创建设计对话。
- 执行：波次切换、实时/结果/证据、暂停/恢复、SUT 过滤、登录接管/人工请求卡。
- 链路：SUT/页面/波次/状态过滤、跟随执行、历史回放、问题侧栏。
- 设置：分类表单、默认值、重启标记、密钥掩码。

---

## 9. P9 详细设计

### 9.1 D59 报告 HTML/PDF

- `report` 是逻辑报告（version/wave/kind/current_revision_id）；`report_revision` **内容字段 append-only**（revision、HTML ref、input_fingerprint、as_of、template_version）；`render_status/pdf_ref` 是可更新的渲染元数据，不改变内容/as-of。
- 模板：内置 1 套 + 章节开关；模板版本化；0.2 不支持自定义模板编辑器。
- 生成流程：wave/version 数据 → 确定性内容模型 → 新 revision + HTML → 发布 → `report.render_pdf` 异步生成 PDF → 更新 revision 的 render_status/pdf_ref。
- stale/as-of/revision：新输入生成新 revision；旧 revision 内容不可变；PDF 可按 revision 重生成；render_pdf 失败可重试。

### 9.2 D63 知识

- `knowledge_item` scope=global（跨版本）/system（同一 SUT 族）；source_version_id + promoted_from_version_id；版本知识晋升 → 审核 → global/system。
- 注入：按 scope/系统/标签/关键词检索，预算限制，带引用；澄清/设计工具。
- 撤回/复审：approved → revoked；注入时过滤 stale/revoked；来源删除不删知识，标 stale。

### 9.3 D65 GC

- `content_ref` 是唯一账本；`export`/`report_revision`/`evidence`/`attachment` 只做语义关联；GC 扫描无引用对象、临时文件、被替代导出/报告 PDF。
- 默认 dry-run；宽限期（如 24h）；保护：版本/需求/用例/问题/报告/知识/视觉基线。
- 旧报告 revision 与有引用附件不删；删除留审计/墓碑。

### 9.4 D67 多副本

- 任务 lease/fencing；启动恢复只处理过期租约；不批量中断 running。
- SSE：PG LISTEN/NOTIFY（或轮询）fan-out；快照/回放不变。
- 共享存储要求；readiness/drain；滚动升级；限流按实例/DB。
- 执行器注册/心跳/容量/排空；case_run 带 owner_executor_id/dispatch_epoch/lease_until/dispatch_token；结果写入校验 epoch/token。执行器心跳超时→offline；其 case_run 置 interrupted(executor_lost)、epoch++、按 intent 重派；旧 epoch 写入拒绝。

### 9.5 问题重开/合并/去重

- reset：confirmed/dismissed → open；记录 reason/count/history；旧报告 as-of。
- merge：源 merged + merged_into_id/merged_at/merged_by；refs 迁移/别名；保留历史；0.2 不做撤销。
- 复发重定向：新失败按 dedupe_key 先查源 issue；若源已 merged，沿 `merged_into_id` 链重定向到目标 issue，追加 refs/recurrence；不新建源问题。
- dedupe：确定性 key + AI 语义建议；人工确认；跨波次稳定；不做跨版本合并。

---

## 10. 多实例与运维

- 单机默认；多副本安全目标。
- 迁移：只追加、启动锁、checksum、备份。
- 健康：API/DB/LLM/executor 分别探测；readiness 反映依赖。
- 观察：任务板/团队频道/事件/审计/链路；SLO 后置。
- 存储：共享文件系统；对象存储后置。
- 安全：受控开发测试网络；凭据 AI 可读；UI 掩码；不做生产加固。

---

## 11. 测试与验收

| 层 | 内容 |
|---|---|
| 单测 | 领域状态机/校验器/上下文预算/任务板租约/去重/GC 引用 |
| 集成 | DB 迁移/任务板/reconciler/多 SUT 绑定/报告渲染/SSE |
| E2E | 管理员配置 → 版本 → 澄清 → 设计 → 确认 → 执行 → 问题/报告 → 闭环 → 重开 |
| Fake LLM | 确定性演示/回归；真实 LLM 验收单独 gated |
| 多 SUT | 跨系统用例执行/证据/报告；账号/DB/页面解析 |
| 上下文 | 长会话/长执行/工具大结果/摘要恢复/无 context_overflow 失败 |
| P9 | HTML/PDF、知识晋升、GC dry-run、多副本安全、问题重开/合并 |
| 全功能对齐 | 0.1 P0–P8 能力逐项验收 |

---

## 12. 依赖顺序与参数默认

### 12.1 建议依赖顺序（非任务计划）

1. 平台底座：identity/settings/llm/eventing/storage/audit/resource（多 SUT/页面）。
2. 运行时：work_item/lease/reconciler/team room/agent run/context。
3. 版本/需求/澄清对话。
4. 设计/用例/确认/脑图。
5. 执行/波次/多 SUT/证据。
6. 交付/问题/报告/链路。
7. 全功能对齐：codeintel/visual/quality/search/todos/agent center。
8. P9 五项。
9. 硬化/验收。

多副本安全原语在第 1–2 波就位；完整部署编排在 P9。

### 12.2 参数默认（已定）

11 项参数已于 2026-10-31 全部按推荐关闭，详见 [open-decisions.md](research/open-decisions.md)。主文档已在对应章节采用这些默认值（导出/P9/知识/GC/迁移/账号/URL/并发/频道/需求字段/报告种类）。没有遗留的产品级开放决策；后续如需调整，按变更流程回写。

---

*文档版本：v0.3（讨论稿，2026-10-31，已并入独立评审 P0/P1 修订与全部参数默认）。下一步：基于本文进入 Superpowers spec/plan。*
