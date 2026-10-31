# EzTest 0.2 P2 设计（用例脑图/确认/导出）— 实施规格

> 状态：**待用户审核**（2026-10-31）。用户批准后进入 writing-plans，再无人值守实施。
> 阶段：P2。阶段文件：`docs/version-V0.2/phases/phase-02-design.md`。
> 前置 handoff：`docs/version-V0.2/phases/phase-00-handoff.md`（P0 completed）与 `docs/version-V0.2/phases/phase-01-handoff.md`（P1 completed）。
> 计划：`docs/superpowers/plans/2026-10-31-eztest-v02-p2-case-design.md`（待编写）。
> 本规格只实现 P2；P3+ 内容只进入 handoff/backlog/契约保留位，不实现、不提前建表。
> 本规格不修改 `docs/version-V0.2/**` 设计文档；冲突按权威顺序裁决并记录在 §14。

## 0. 依据、阶段映射与前置检查

**权威顺序**：`architecture-design.md` → `high-level-design.md` → `research/decision-log.md` → `research/open-decisions.md` → `research/capability-matrix.md` → `phases/phase-02-design.md` → `contracts/*`（P0/P1 frozen）→ P0/P1 handoff。

**阅读依据（实际查阅）**
- `architecture-design.md` §5.2–5.5、§6.1–6.5、§7、§8.1–8.8、§9、§10、§11、§15、§18
- `high-level-design.md` §1–§8、§12
- `research/decision-log.md` D01–D53、§11a
- `research/open-decisions.md` OD-1…OD-11 与关闭记录
- `research/capability-matrix.md` D1/D2/D4/H3
- `phases/` 全部文件（用户 2026-10-31 更正后补读）：`phase-00-platform-foundation.md`、`phase-00-handoff.md`、`phase-01-runtime-clarification.md`、`phase-01-handoff.md`、`phase-02-design.md`、`phase-03-execution.md`、`phase-04-delivery-chain.md`、`phase-05-codeintel-visual.md`、`phase-06-parity-quality-search-agent-center.md`、`phase-07-p9.md`、`phase-08-hardening-acceptance.md`
- `contracts/README.md`、`openapi.yaml`、`events.md`、`db-schema.md`、`migrations-order.md`、`task-types.md`、`agent-profiles-tools.md`、`settings.md`
- `docs/version-V0.2/ui/mock.html` + `mock.css` + `ui/design.png`
- P1 实施证据：`scripts/test.ps1`、`scripts/p1-acceptance.ps1`、`scripts/p1-e2e.ps1`、P1 spec/plan

**阶段映射与提示词更正**：会话启动提示词的读取清单误用了 P0 模板（`phase-00-platform-foundation.md` + “无前置 handoff”）。用户已于 2026-10-31 明确更正：**本阶段前置 P0 与 P1，均有交接文档，必须读取 `docs/version-V0.2/phases/` 下全部文件**。`implementation-phase-plan.md` §0 阶段复制表同样规定 `P2 → phase-02-design.md`、`PREV_HANDOFF=phase-01-handoff.md`。本会话按 **P2 = phase-02** 实施，不重做 P0/P1；P0/P1 handoff、全部 phase 文件已补读；handoff 记录该提示词更正。
**后续阶段边界（P3–P8 只读，不在本阶段实现）**：P3 需要 P2 输出 `current+confirmed` 用例版本与目标 SUT/页面/API、`design.gap` 入口；P4 需要设计待办/收尾阻断信号；P5 扩展代码/视觉；P6/P7/P8 只消费确认/导出/事件契约。本规格不提前实现这些阶段。

**前置检查（已执行）**
1. `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1` → **EXIT=0**；Go 单元+集成全部 `ok`；前端 43 文件 / 341 tests passed；日志 `.tmp/p2/baseline-test.log`。
2. P1 contracts 已冻结（`frozen: P1` / `x-frozen: P1`）；本阶段只增量，不修改已应用迁移与 P0/P1 表结构（P2 仅新增表）。
3. P1 留下的占位可替换：`pending_design_count = clarified 数`（P1-D15）、`NoopCaseStaleHook`（`requirement_ports.go:23-35`）、`requirements` 页设计 CTA 禁用（`DesignCTA.tsx:13-29`）、design Tab 占位（`VersionDetailPage.tsx:34-65`）。
4. `.env` 有真实 DeepSeek 凭据（`llm_*` 小写键），真实 LLM 验收可跑；API 进程仍只读 4 项 `EZTEST_*` env。
5. 工作树存在与 P2 无关的 dirty 文件（`docs/development.md`、`docs/startup.md`、`scripts/dev-all.ps1` 等）；本阶段提交不纳入。

**关键设计事实（带来源）**
- 用例模型三级：需求→场景→用例；用例=逻辑对象+不可变版本；用例卡/步骤可跨 SUT（D20、architecture §6.3）。
- 确认粒度 = 单用例 / 场景下全部 / 需求下全部；确认冻结 case version；执行只认 `current+confirmed`（D21、architecture §6.3/§6.5）。
- `draft→confirmed`；`confirmed→stale`（需求版本变化/失效）；旧 `confirmed→superseded` 仅在新版本被确认后；新 draft 生成时不改旧状态（architecture §6.3 L266-269）。
- AI 只能提交候选/草案；不能确认用例/需求、不能删除、不能关闭版本；`case.confirm` 仅服务端/用户（D18/D40、architecture §7.3）。
- 设计页 = 脑图 + AI 对话；无“AI 生成”按钮；用户点“新建对话”开启一轮，设计团队扫描待设计需求并实时渲染（D23）。
- D25 A+：默认新需求需用户开设计对话激活；当前设计对话活跃时新澄清需求自动并入本轮；已激活范围内需求变化/stale/执行缺口自动补 draft；版本级 `auto_design_new_requirements` 默认关，开启后自动开轮并自动创建设计对话（D25、architecture §8.4）。
- 设计团队角色：Design Host（不确认）、Design Planner、Case Author、Design Critic（内部自审，不构成人工关卡）、Data Planner；无设计评审页签/整版冻结（D26）。
- 导出 Markdown/Mermaid/OPML/CSV；Excel 后置；导出物 = `export` 逻辑对象 + 内容账本 + 鉴权下载（OD-1、architecture §8.6）。
- 版本状态只是投影；`initial→running` 触发含“首次设计对话”（HLD §6.7 迁移表）。

### 0.1 请用户重点确认的裁决（其余为实现级细节，见 §14）

1. **工具面冲突**：architecture §7.3 指定单一 `case.graph_propose`，HLD §4.2 列了 `design.scenario_propose/case_propose/case_update`。本规格采用 `design.graph_read + case.graph_propose + design.review_submit + design.data_plan_submit`，HLD 的 3 个 granular 写工具由 `case.graph_propose` 统一承载，不注册空壳；`case.confirm` 永不暴露给模型。
2. **“活跃设计对话”不设关闭**：用户裁决“最新 design 对话永远算活跃”，P2 不新增关闭对话 API/“结束本轮”按钮。只要该版本存在设计对话，后续新澄清需求自动并入最新一轮；`auto_design_new_requirements` 实际只在“还没有任何设计对话”时决定是否自动建第一条。
3. **需求版本变化后的重设计**：旧场景/用例保留为历史并 stale；新需求版本生成新场景/新用例。同一需求版本内的用例修改才生成同 case 的新 revision。
4. **stale→superseded**：同一 case 的新版本被确认时，旧 confirmed 与旧 stale 都转 superseded（否则 stale 永远滞留）；draft 不直接 superseded。
5. **导出归属与范围**：`export_artifact` 表放在 storage 上下文（内容账本延伸），P2 只做整版本 case 域导出（`owner_kind=case, owner_id=version_id`）；CSV = 一行一步骤；字节入内容账本，`paths.export_dir` P2 不写。
6. **设计对话支持附件**（D48）：复用 P1 上传/`attachment.ingest` 管线，Host 可读附件文本；不新增消息 part 类型。
7. **待设计计数与 D25**：`pending_design_count` = clarified 且不在 active `design_scope` 的需求数；已有设计对话时新需求自动并入；自动开关仅在该版本**还没有设计对话**时自动建对话+轮次。
8. **真实 LLM 验收必跑（双轨）**：DeepSeek `deepseek-flash`；fake LLM 只做确定性回归/故障注入，真实 LLM 做 API 验收 + 浏览器主链路 + 契约冻结前早期产图探针；凭据缺失或真实验收失败 → handoff 标 blocked，不以 fake 结果替代。

## 1. 目标与退出标准

**目标**：打通“新建设计对话 → 设计团队扫描待设计需求 → 实时脑图 → 用户对话/确认 → 导出”的垂直闭环。

**退出标准（逐条可验证）**
1. 已澄清需求 → 新建设计对话 → Planner 激活 scope/建任务 → Case Author 产出场景/用例 draft → 脑图实时渲染；无“AI 生成”按钮。
2. 设计对话中继续修改 → 新增/修改 draft（不改旧 confirmed 状态）→ 用户可分别按用例/场景/需求确认；AI 无法确认。
3. 需求版本变化 → 关联 confirmed case version `stale`（事件带真实 `affected_case_version_ids`）→ 重设计产生新 draft；新版本确认后旧 confirmed `superseded`；执行集合只认 current+confirmed。
4. 导出 Markdown/Mermaid/OPML/CSV 可用、内容完整、sha256 校验一致、鉴权下载；重复导出生成新 revision。
5. D25 可验证：默认（该版本无设计对话）新需求只增加 `pending_design_count`（CTA）；已有设计对话时自动并入；`auto_design_new_requirements=true` 且无设计对话时自动开轮/创建设计对话。
6. 全量测试绿；API 验收（fake + 真实 LLM）、浏览器 E2E（fake 回归 + 真实 LLM 主链路）、早期真实产图探针通过；contracts 冻结为 `frozen: P2`；handoff 完成。

**不在 P2**
- 执行/波次/intent/证据/失败分类（P3）；问题/报告/链路（P4+）；代码智能/视觉/质量/搜索/待办/Agent 中心（P5/P6）；P9。
- 多用户实时协作/读写锁/站内通知；整版基线冻结/设计评审页签；AI 自动确认；用例删除。
- 0.1 数据迁移；多副本编排（P7）；全量 E2E/性能/SLO（P8）。
- 通用 `Idempotency-Key` 中间件（P1 已记录；P2 用自然幂等 + advisory lock）。

## 2. 实施策略与仓库边界

- 后端新增 `design` 限界上下文：`backend/internal/design/{domain,application,infrastructure,interfaces/httpapi}`；四层齐全。
- 扩展既有上下文（只做增量）：
  - `version`：允许创建设计对话、需求编辑/回退写 stale hook、版本详情增加设计/用例统计。
  - `agent`：新增 design 任务类型/profile/工具接入、chat.turn 按会话 kind 选择 Host、reconciler 增加 design 规则、worker 输出路由。
  - `storage`：新增 `export_artifact` 元数据表与 export 应用服务（内容对象/引用账本的自然延伸）。
  - `server`、`cmd/eztest`：路由与装配（唯一组装根）。
- 前端新增 `frontend/src/features/design/`，`VersionDetailPage` 的 design Tab 从占位改为真实组件；`DesignCTA` 可点击跳转。
- 共享文件（`contracts/*`、`migrations-order.md`、`openapi.yaml`）由 Lead 统一收口；并行子代理不并发写。
- 单实例假设沿用 P1；设计任务/验收不承诺多副本实时。

### 2.1 规范符合性（Agent.md）

| 规则 | P2 落地 |
|---|---|
| §6.1 目录/四层 | 新增 `backend/internal/design/{domain,application,infrastructure,interfaces/httpapi}`；不新建 `utils/common`；共享纯逻辑进 `platform` |
| §6.2 导入方向 | domain 只依赖 stdlib/platform；application 不导 infrastructure/httpapi；跨上下文只依赖 application 端口 |
| §6.3 迁移 | `<context>_<NNNN>_<desc>.sql`；只追加；一个文件一个上下文；`order.txt` 唯一顺序；启动 advisory lock |
| §6.4 前端 | 只走 `api-client.ts`/`sse-client.ts`；业务在 `features/design/`；`src/app` 只做路由 |
| §6.5 新上下文 | 四层 + `cmd/eztest` 装配 + 迁移；design reconciler 由既有 agent reconciler 调用（不新增常驻 goroutine）；`Agent.md §6.1` 目录树的更新记录在 handoff（用户限制只改 handoff/contracts/迁移/测试） |
| §6.6 ARTEX | 不参考/不复制 ARTEX；实现说明记录“无对应” |
| §6.7 ID | 雪花 `BIGINT`；JSON/URL/工具参数 ID 为十进制字符串；无 `BIGSERIAL` |
| §6.8 文件 | 导出字节在文件系统内容对象（不写 PG 二进制）；根内路径校验/原子写/0600；下载鉴权；§6.8.3 复用例外见 P2-D-29（已批准） |
| §6.9 DB | 方案 A 表/字段/索引命名；PK/UK/IDX 显式；跨上下文无 FK；审计列齐全 |



## 3. 数据模型与迁移（只追加）

新增 2 个迁移，追加到 `backend/internal/db/migrations/order.txt`；不改 P0/P1 已应用迁移。
所有新表雪花 `BIGINT` 主键（D29，与 P1 一致）；JSON 中 ID 为十进制字符串；新表审计列 `created_at/updated_at TIMESTAMPTZ NOT NULL DEFAULT now()`（不可变版本表也保留 `updated_at`，业务上从不更新）；跨上下文只做逻辑引用、不建 FK。

**命名规范（Agent.md §6.9/D34，方案 A）**：表名小写单数、`业务名称_作用`；主键约束显式 `pk_<表名>`；唯一约束 `uk_<表名>_<列...>`；普通索引 `idx_<表名>_<列...>`；字段小写下划线，布尔 `is_` 前缀（本轮无布尔列）；禁止保留字。设计文档字面名映射：`scenario→case_scenario`、`export→export_artifact`、`sort→sort_order`、`line→line_no`、`format→file_format`、`size→size_bytes`、`content_ref→content_object_id`；`case_entity` 保留（`case` 是保留字，`entity` 表逻辑用例，且 architecture 执行谓词使用该名）。
**通用 CHECK（迁移内落地）**：所有 JSONB 列 `NOT NULL DEFAULT` 并 `CHECK (jsonb_typeof(col) IN ('array'/'object'))`；`content_hash`/`content_object_id` `CHECK (col ~ '^[0-9a-f]{64}$')`；`revision > 0`、`seq > 0`、`sort_order >= 0`；`name` 1–200、`title` 1–200、`batch_id` 1–64；状态/kind/priority 全部 CHECK 枚举（见各表）。
**FK 规则**：同上下文 FK 显式 `REFERENCES`（`case_entity.scenario_id`、`case_version.case_id`、`case_step/assertion/source/confirmation.case_version_id`、`design_scope.activated_by_round_id`）；跨上下文（version/requirement/requirement_version/version_sut/page/api/database/conversation/round 归属）只逻辑引用，不建 FK；`case_entity.current_version_id` 因循环引用不建 FK，由服务端事务维护。

### 3.1 `design_0001_case_model.sql`（上下文：design，9 表）

**`case_scenario`**
`id BIGINT`、`version_id BIGINT NOT NULL`、`requirement_id BIGINT NOT NULL`、`requirement_version_id BIGINT NOT NULL`、`name TEXT NOT NULL`（1–200 字符）、`description TEXT NOT NULL DEFAULT ''`、`version_sut_ids JSONB NOT NULL DEFAULT '[]'`、`page_ids JSONB NOT NULL DEFAULT '[]'`、`api_ids JSONB NOT NULL DEFAULT '[]'`、`sort_order INT NOT NULL DEFAULT 0`、`created_by BIGINT NULL`、`created_by_run_id BIGINT NULL`、`created_at/updated_at`。
约束：`pk_case_scenario`；`uk_case_scenario_requirement_version_name UNIQUE(requirement_version_id,name)`；`idx_case_scenario_requirement(version_id,requirement_id,sort_order,id)`、`idx_case_scenario_requirement_version(requirement_version_id)`。

**`case_entity`**（逻辑用例）
`id BIGINT`、`scenario_id BIGINT NOT NULL REFERENCES case_scenario(id)`、`current_version_id BIGINT NULL`、`status TEXT NOT NULL DEFAULT 'draft'`（CHECK `draft/confirmed/stale`；当前版本投影，superseded 只属于历史版本）、`created_by BIGINT NULL`、`created_by_run_id BIGINT NULL`、`created_at/updated_at`。
约束：`pk_case_entity`；`idx_case_entity_scenario(scenario_id,id)`、`idx_case_entity_current(current_version_id)`、`idx_case_entity_status(status)`。

**`case_version`**（不可变）
`id BIGINT`、`case_id BIGINT NOT NULL REFERENCES case_entity(id)`、`revision INT NOT NULL`、`title TEXT NOT NULL`（1–200）、`description TEXT NOT NULL DEFAULT ''`、`preconditions TEXT NOT NULL DEFAULT ''`、`test_data JSONB NOT NULL DEFAULT '{}'`、`priority TEXT NOT NULL DEFAULT 'P2'`（CHECK `P0/P1/P2/P3`）、`default_version_sut_id BIGINT NULL`、`version_sut_ids JSONB NOT NULL DEFAULT '[]'`、`page_ids JSONB NOT NULL DEFAULT '[]'`、`api_ids JSONB NOT NULL DEFAULT '[]'`、`status TEXT NOT NULL DEFAULT 'draft'`（CHECK `draft/confirmed/stale/superseded`）、`content_hash TEXT NOT NULL`（64 hex）、`created_by BIGINT NULL`、`created_by_run_id BIGINT NULL`、`created_at/updated_at`。
约束：`pk_case_version`；`uk_case_version_case_revision UNIQUE(case_id,revision)`；`idx_case_version_case(case_id,id DESC)`、`idx_case_version_status(status)`。

**`case_step`**
`id BIGINT`、`case_version_id BIGINT NOT NULL REFERENCES case_version(id)`、`seq INT NOT NULL`、`action TEXT NOT NULL`（1–2000）、`expected TEXT NOT NULL DEFAULT ''`、`target_version_sut_id BIGINT NULL`（空=继承 `case_version.default_version_sut_id`）、`target_page_id BIGINT NULL`、`target_api_id BIGINT NULL`、`account_role TEXT NOT NULL DEFAULT ''`、`data JSONB NOT NULL DEFAULT '{}'`、`created_at/updated_at`。
约束：`pk_case_step`；`uk_case_step_version_seq UNIQUE(case_version_id,seq)`；`idx_case_step_version(case_version_id,seq)`。

**`case_assertion`**
`id BIGINT`、`case_version_id BIGINT NOT NULL REFERENCES case_version(id)`、`step_seq INT NOT NULL`、`kind TEXT NOT NULL`（CHECK `ui/api/db/text/visual`）、`target TEXT NOT NULL DEFAULT ''`、`expected TEXT NOT NULL DEFAULT ''`、`target_version_sut_id BIGINT NULL`、`target_api_id BIGINT NULL`、`database_id BIGINT NULL`、`created_at/updated_at`。
约束：`pk_case_assertion`；`idx_case_assertion_version(case_version_id,step_seq)`；`step_seq` 必须引用同版本 `case_step.seq`（应用层校验）。

**`case_source`**
`id BIGINT`、`case_version_id BIGINT NOT NULL REFERENCES case_version(id)`、`kind TEXT NOT NULL`（CHECK `requirement/code/page/api/knowledge`）、`ref_id TEXT NOT NULL`、`path TEXT NOT NULL DEFAULT ''`、`line_no INT NULL`、`sha TEXT NOT NULL DEFAULT ''`、`created_at/updated_at`。
约束：`pk_case_source`；`idx_case_source_version(case_version_id,kind)`；同一版本内应用层去重。

**`case_confirmation`**
`id BIGINT`、`case_version_id BIGINT NOT NULL REFERENCES case_version(id)`、`scope TEXT NOT NULL`（CHECK `case/scenario/requirement`）、`target_scope_id BIGINT NOT NULL`、`batch_id TEXT NOT NULL`（1–64）、`affected_case_version_ids JSONB NOT NULL DEFAULT '[]'`、`snapshot JSONB NOT NULL DEFAULT '{}'`、`confirmed_by BIGINT NOT NULL`、`confirmed_at TIMESTAMPTZ NOT NULL`、`created_at/updated_at`。
约束：`pk_case_confirmation`；`uk_case_confirmation_version UNIQUE(case_version_id)`；`idx_case_confirmation_batch(batch_id)`、`idx_case_confirmation_actor(confirmed_by,confirmed_at DESC)`。

**`design_round`**
`id BIGINT`、`version_id BIGINT NOT NULL`、`conversation_id BIGINT NOT NULL`、`activation TEXT NOT NULL`（CHECK `manual/auto_gap/auto_change/auto_new`）、`requirement_ids JSONB NOT NULL DEFAULT '[]'`、`requirement_version_ids JSONB NOT NULL DEFAULT '[]'`、`scope JSONB NOT NULL DEFAULT '{}'`、`status TEXT NOT NULL DEFAULT 'active'`（CHECK `active/completed`）、`created_by BIGINT NULL`、`created_at/updated_at`。
约束：`pk_design_round`；`uk_design_round_conversation UNIQUE(conversation_id)`；`idx_design_round_version(version_id,id DESC)`、`idx_design_round_status(status)`。

**`design_scope`**
`id BIGINT`、`version_id BIGINT NOT NULL`、`requirement_id BIGINT NOT NULL`、`activated_by_round_id BIGINT NULL REFERENCES design_round(id)`、`activated_at TIMESTAMPTZ NOT NULL`、`status TEXT NOT NULL DEFAULT 'active'`（CHECK `active/closed`）、`created_at/updated_at`。
约束：`pk_design_scope`；`uk_design_scope_version_requirement UNIQUE(version_id,requirement_id)`；`idx_design_scope_requirement(requirement_id)`。
P2 只写 `active`；`closed` 保留给后续阶段（需求关闭/放弃）。

### 3.2 `storage_0002_export_artifact.sql`（上下文：storage，1 表）

**`export_artifact`**
`id BIGINT`、`version_id BIGINT NOT NULL`、`owner_kind TEXT NOT NULL`（CHECK `case/report/chain`）、`owner_id BIGINT NOT NULL`、`revision INT NOT NULL DEFAULT 1`、`file_format TEXT NOT NULL`（CHECK `markdown/mermaid/opml/csv/html/pdf`）、`content_object_id TEXT NOT NULL`（内容对象 sha256，64 hex）、`size_bytes BIGINT NOT NULL`、`mime TEXT NOT NULL`、`filename TEXT NOT NULL`、`status TEXT NOT NULL DEFAULT 'ready'`（CHECK `ready/failed`）、`created_by BIGINT NULL`、`created_at/updated_at`。
约束：`pk_export_artifact`；`uk_export_artifact_version_owner_file_format_revision UNIQUE(version_id,owner_kind,owner_id,file_format,revision)`；`idx_export_artifact_version(version_id,id DESC)`、`idx_export_artifact_created_at(created_at)`。
导出字节写入 P0 内容账本（`content_object`），并写 `content_ref`（owner_kind=`export`，owner_id=export artifact id，role=`content`）。P2 不使用 `paths.export_dir` 存放字节（见 P2-D-20/P2-D-29）。

### 3.3 迁移顺序与幂等

`order.txt` 追加（顺序固定）：

```text
design_0001_case_model.sql
storage_0002_export_artifact.sql
```

- 迁移执行器按版本顺序事务执行；重复执行 no-op；集成测试断言新迁移数从 15 → 17、新表 10 张、失败回滚不留半成品。
- 不修改 `migrate_integration_test.go` 中已应用迁移的既有断言，只增量更新表清单/迁移数。

## 4. 领域状态机与不变量

### 4.1 CaseVersion 状态机

| 迁移 | 触发 | 说明 |
|---|---|---|
| `draft → confirmed` | 用户确认（case/scenario/requirement 任一 scope） | 冻结内容；写 `case_confirmation`；进入可执行集合 |
| `confirmed → stale` | 需求当前版本变化（编辑/AI 回退/需求失效） | 退出可执行集合；保留历史；等待重设计 |
| `confirmed → superseded` | 同一个 case 的新版本被确认 | 旧 confirmed 转历史；历史报告可查 |
| `stale → superseded` | 同一个 case 的新版本被确认 | P2 裁决：允许（否则 stale 永远滞留）；仅同一 case |
| `draft → superseded` | 不实现 | 设计文档明确“draft 不直接 superseded” |
| 其他迁移 | 非法 | `409 state_conflict` |

- `case_entity.current_version_id` 在**新 draft 创建时立即**指向新版本；`case_entity.status` = 当前版本状态投影（draft/confirmed/stale）。
- 执行集合唯一谓词（P3 消费）：`case_entity.current_version_id = case_version.id AND case_version.status='confirmed'`。因此新 draft 虽不改变旧 confirmed 的 status，但旧版本不再是 current，自然退出执行集合（与 D22 一致）。
- 旧版本（confirmed/stale/superseded）永久保留；不物理删除。P2 不提供删除 API，AI 也不能删除。
- 重新确认：只有当前 draft 可确认；当前 confirmed 的重复确认幂等返回 skipped；当前 stale 需先重设计（新 draft）。

### 4.2 需求版本与用例覆盖

- 场景绑定 `requirement_version_id`；同一需求版本下的场景/用例才可被确认。
- 需求版本变化后：
  1. stale hook 将**该需求下状态为 confirmed 的当前 case version** 置 `stale`，并返回 `affected_case_version_ids`（十进制字符串）。
  2. 设计团队为新的澄清需求版本创建**新场景**（旧场景保留为历史）；新用例/新 draft 属于新场景。
  3. 旧场景中的 stale 用例保留在图中（带 stale 标记）；不参与确认/执行。
- 用例内容修改（同一需求版本内）：由 `case.graph_propose` 对已有 `case_id` 提交新内容 → 新建 revision 的 draft 版本并成为 current；旧 confirmed 保持 confirmed，直到新版本确认后转 superseded。
- 当前版本为 stale 的 case 不能直接更新；必须走需求新版本的新场景。

### 4.3 目标（version_sut/page/api）规则

- `scenario.version_sut_ids/page_ids/api_ids` 是场景级候选范围；可为空。
- `case_version.version_sut_ids` 非空；每个必须是当前版本绑定的 `version_sut.id`。
- `case_version.default_version_sut_id` 必填（除非该 case 所有步骤都显式指定目标；P2 统一要求必填），且必须 ∈ `case_version.version_sut_ids`。
- 步骤 `target_version_sut_id` 为空时继承默认；非空时必须 ∈ case 的 version_sut 集合。
- 步骤/断言引用的 `target_page_id` / `target_api_id` / `database_id` 必须属于对应 `version_sut` 的 SUT+环境，并位于该版本的页面/API/数据库绑定范围内。
- 跨系统：同一 case 的不同步骤可指向不同 `version_sut_id`；卡片展示多个 SUT chips，步骤级展示目标 SUT。
- 版本绑定在 P2 之后才变更时，既有 case version 保留原目标快照（不做自动失效；页面/API 变更由后续设计评估，OD-7）。

### 4.4 断言与来源

- 断言 `kind ∈ ui/api/db/text/visual`（P2 全部允许设计；visual 的执行在 P5）；`target` 必填（1–2000），`expected` 允许空字符串但 UI 提示；`db` 断言必须给 `database_id` 且属于目标 version_sut。
- `step_seq` 必须等于该 case version 的某个 step.seq；同一 step 可有多个断言。
- 来源 `kind ∈ requirement/code/page/api/knowledge`；P2 AI 主要提交 `requirement`（ref_id=requirement_version_id）；page/api 来源校验绑定；code/knowledge 允许自由引用（P5/P7 再校验）。同一版本内按 `(kind,ref_id,path,line)` 去重。

### 4.5 确认语义

- 确认 = 用户/服务端 API 触发；AI 无确认工具。任何登录用户可确认（沿用 P1 单团队信任模型；审计记录 actor）。
- scope 选择集合：
  - `case`：目标 case 的当前版本，必须是 `draft`；当前 `confirmed` → 幂等 skipped；`stale/superseded` → 409。
  - `scenario`：场景下所有 case 的当前版本中 `draft` 的集合；至少 1 个，否则 409；`confirmed` skipped；`stale` 跳过（历史）。
  - `requirement`：需求下所有场景的当前版本中 `draft` 的集合；至少 1 个，否则 409；`confirmed` skipped；`stale` 跳过。
- 场景/需求级确认前校验：需求状态 `clarified` 且当前 `requirement_version_id` 与场景绑定一致；否则 409。
- 事务：行锁选中集合 → 校验 → 全部置 confirmed → 更新 `case_entity.status` → 插入 `case_confirmation`（同一 batch_id、每版本一行，`affected_case_version_ids`=本批全部）→ 旧 confirmed/stale 转 superseded → 提交后发事件/审计。
- 部分失败/并发：一旦校验失败整批回滚；并发确认由 `SELECT ... FOR UPDATE` 串行化；重复确认幂等。
- `snapshot` 记录 `{title,priority,step_count,assertion_count,source_count,content_hash}`，用于历史留痕。
- `batch_id` 缺省由服务端生成 `batch_`+16 hex；同一批所有 confirmation 行共享。

### 4.6 设计轮次与激活范围

- `design_round` 每个设计对话一行；P2 只写 `status=active`（`completed` 保留给后续阶段；**不新增关闭对话 API/“结束本轮”按钮**，用户 2026-10-31 裁决）。
- `design_scope(version_id,requirement_id,status=active)` 表示“已激活设计”。激活由设计轮次或自动规则写入，同一需求只一行。
- **当前设计轮次** = 该版本 `conversation.kind='design'` 中 id 最大的对话（P2 不关闭设计对话；多个历史对话允许存在，只有最新一条接收自动并入）。
- 手工“新建对话”：
  1. 创建 `Conversation(kind=design)`；
  2. 同步/异步（handler 触发 + reconciler 兜底）执行 Planner 扫描：
     - 计算本轮范围：所有 `clarified` 且未在 scope 的需求 + 已激活但“当前需求版本无 draft/confirmed 用例”或“存在 stale 当前用例”的需求；
     - 先创建 `design_round(activation=manual, requirement_ids/versions snapshot)`（`design_scope.activated_by_round_id` 需要引用它）；
     - 对本轮中未激活的需求写 `design_scope(active, activated_by_round_id=本轮)`；
     - 确保 `design.requirement` 任务（每个需求版本一个，dedupe `design:{requirement_id}:{requirement_version_id}`）；
     - 确保 `design.round` 任务（dedupe `round:{conversation_id}`）；
     - 版本 `initial → running`（幂等）。
  3. 若本轮无待设计需求，仍创建空 round，聊天显示“暂无待设计需求”。
- **自动并入**（D25）：新需求变成 clarified 后，只要该版本已存在设计对话（最新一条即当前轮）→ 自动写 scope、更新当前 round 的 requirement snapshot、确保 `design.requirement` 任务（activation 记 `auto_new`，事件 `design.scope_activated`）；不新建对话/轮次。
- **已激活范围内自动补**（D24/D25）：需求版本变化或 stale → 新需求版本被澄清后，自动确保 `design.requirement`（activation `auto_change`）；执行缺口通过 `RequestGap` 确保 `design.gap`（activation `auto_gap`）。
- **自动开关**：`version.auto_design_new_requirements=true` 且该版本**还没有任何设计对话**、存在 pending clarified 需求时，reconciler 自动创建一条设计对话（title `AI 设计 YYYY-MM-DD HH:mm`，created_by NULL）+ round（activation `auto_new`）+ 激活所有 pending + 任务；只创建一条，批量覆盖当前 pending。已有设计对话后，本开关不再额外动作（自动并入已覆盖）。
- **待设计（pending）**：`clarified` 且不在 `design_scope(active)` 的需求。`pending_design_count` = 该数量（替换 P1 的 clarified 计数）。
- 设计状态投影（图/需求行）：
  - `pending`=未激活；`in_design`=已激活但当前需求版本无 case；`draft`=存在当前 draft；`confirmed`=全部当前 case confirmed；`stale`=存在当前 stale（优先级：stale > draft > confirmed > in_design）。

## 5. 设计任务、Agent 与工具

### 5.1 任务类型（P2 增量）

| type | team | profile | dedupe_key | payload | 输出 |
|---|---|---|---|---|---|
| `design.round` | design | `design_planner` | `round:{conversation_id}` | `{conversation_id,round_id}` | 范围说明（conversation assistant message + team post） |
| `design.requirement` | design | `case_author` | `design:{requirement_id}:{requirement_version_id}` | `{requirement_id,requirement_version_id,reason}` | 场景/用例 draft（`case.graph_propose`）+ 团队帖子 |
| `design.gap` | design | `case_author` | `gap:{requirement_id}:{case_version_id|requirement_version_id}` | `{requirement_id,case_id?,case_version_id?,reason}` | 新 draft（`case.graph_propose`）+ 团队帖子 |
| `design.critic` | design | `design_critic` | `critic:{requirement_id}:{requirement_version_id}` | `{requirement_id,requirement_version_id}` | `design.review_submit` findings + 团队帖子 |
| `design.data_plan` | design | `data_planner` | `dataplan:{requirement_id}:{requirement_version_id}` | `{requirement_id,requirement_version_id}` | `design.data_plan_submit` 更新 draft + 团队帖子 |

- `chat.turn` 继续服务澄清与设计 Host：当 `conversation.kind='design'` 时 team=design、profile=`design_host`；澄清时保持 P1 语义（`team=clarification`、`clarification_host`）。这是 P1 冻结契约的**语义增量**，不新增 type。
- 任务创建失败不阻塞业务；reconciler 幂等兜底。任务失败按 P1 重试/backoff/dead 语义。
- `design.round` 执行时范围扫描已由 Planner 服务完成；LLM 只用 `requirement.read`/`design.graph_read` 生成范围说明，不写库。

### 5.2 Profiles（启动幂等 seed；key 冲突不覆盖管理员修改）

| key | 名称 | 工具 | budgets |
|---|---|---|---|
| `design_host` | 设计 Host | `context.version_pack`、`context.task_pack`、`context.team_room_read`、`context.summary_update`、`requirement.read`、`design.graph_read`、`case.graph_propose`、`attachment.list`、`attachment.read_text`、`sut.list`、`sut.page_list` | `{max_turns:12,soft_seconds:300,hard_seconds:600}` |
| `design_planner` | 设计 Planner | `context.version_pack`、`context.task_pack`、`context.team_room_read`、`requirement.read`、`design.graph_read` | `{max_turns:6,soft_seconds:180,hard_seconds:300}` |
| `case_author` | 用例 Author | `context.version_pack`、`context.task_pack`、`requirement.read`、`design.graph_read`、`case.graph_propose`、`sut.list`、`sut.page_list` | `{max_turns:8,soft_seconds:300,hard_seconds:600}` |
| `design_critic` | 设计 Critic | `context.task_pack`、`design.graph_read`、`design.review_submit` | `{max_turns:4,soft_seconds:120,hard_seconds:300}` |
| `data_planner` | 数据 Planner | `context.task_pack`、`design.graph_read`、`design.data_plan_submit` | `{max_turns:4,soft_seconds:120,hard_seconds:300}` |

system prompt 必须包含 `[profile:<key>]` 并声明：AI 只能提交 draft/候选，不能确认/删除/关闭；Host 只解释/修改脑图，不确认。

### 5.3 工具（P2 注册；scope 由 runtime 注入，schema 不出现 versionId）

运行 scope 新增/使用的注入键：`version_id/conversation_id/requirement_id/requirement_version_id/case_id/case_version_id/round_id/version_sut_ids`；工具只能使用注入 scope + 自身业务参数，不能跨需求/版本读写。

**读**
- `design.graph_read`：`{requirement_id?:string, case_id?:string}` → 版本/需求/场景/用例摘要 + `graph_hash`；带 case_id 时返回该用例当前版本完整详情（步骤/断言/来源）。
- 复用 P1 读工具：`requirement.read`、`attachment.list/read_text`、`sut.list/page_list`、`context.*`。

**写（候选提交 + 服务端校验）**
- `case.graph_propose`（architecture §7.3 指定的结构化图提交）：
  ```json
  {
    "requirement_id": "3001",
    "requirement_version_id": "3002",
    "base_graph_hash": "optional-64-hex",
    "scenarios": [{"ref":"s1","id":"5001","name":"正常登录","description":"...","version_sut_ids":["9001"],"page_ids":["..."],"api_ids":["..."],"sort_order":0}],
    "cases": [{"ref":"c1","id":"7001","scenario_ref":"s1","title":"账号密码登录成功","description":"...","preconditions":"...","test_data":{},
      "priority":"P1","default_version_sut_id":"9001","version_sut_ids":["9001"],"page_ids":["..."],"api_ids":["..."],
      "steps":[{"seq":1,"action":"打开登录页","expected":"显示表单","target_version_sut_id":null,"target_page_id":"...","target_api_id":null,"account_role":"admin","data":{}}],
      "assertions":[{"step_seq":1,"kind":"ui","target":"欢迎文案","expected":"欢迎回来","target_version_sut_id":null,"target_api_id":null,"database_id":null}],
      "sources":[{"kind":"requirement","ref_id":"3002","path":"","line":null,"sha":""}]}]
  }
  ```
`scenarios[].id` / `cases[].id` 新建时省略，更新时传既有 ID。
  校验：作用域/需求状态 clarified/需求版本=current；`base_graph_hash` 若提供则必须等于当前图 hash（否则 409，模型重新 `graph_read`）；`scenarios[].ref` 唯一、`scenarios[].id` 存在时必须是同需求版本下的场景；`scenarios[].name` 已存在且未给 id 时按自然键复用/更新；`cases[].ref` 唯一、`cases[].id` 存在时必须是该需求下用例，且其当前场景必须等于 `scenario_ref` 解析出的场景；`cases[].id` 未给但同场景同名用例已存在时按自然键复用/更新；至少 1 个场景/用例；至少 1 step；seq 连续 1..n；断言 step_seq 存在；目标属于版本绑定；长度/数量上限；全部事务内落库；生成新 revision 时内容 hash 与当前版本相同则幂等返回不新增；返回 `{scenario_ids,case_ids,case_version_ids,graph_hash}`。
- `design.review_submit`：`{summary:string(0..4000),findings:[{severity:"blocker|important|minor",message:string(1..2000),requirement_id?,case_id?}]}`；落 `team_room_post(kind=finding)` + 事件；不建人工评审表。返回 `{posted:true,count}`。
- `design.data_plan_submit`：`{case_version_id:string, preconditions?:string(0..20000), test_data?:object}`；仅当前 draft 版本；生成新 draft revision（复制 steps/assertions/sources，更新字段）；返回 `{case_id,case_version_id,revision}`。
- 不注册 `case.confirm`、`design.scenario_propose`、`design.case_propose`、`design.case_update` 为模型工具：前者的确认权限属于用户/服务端（architecture §7.3）；后三者由 architecture 指定的 `case.graph_propose` 统一承载（P2-D-5）。HLD 的 granular 名称在契约中登记为“由 case.graph_propose 覆盖”，不实现空壳。

**工具结果**：统一 JSON；超 32KiB 走 P1 落盘；结构化错误返回给模型（`invalid_argument/not_found/state_conflict/lease_lost/ai_unavailable`）。

### 5.4 Fake LLM（确定性；扩展 `scripts/fake-llm.cjs`）
- 保持 P0 echo、P1 Host/Curator/Scout 行为不变。
- `[profile:design_host]`：最近用户文本含“新增/补充/修改用例”时，先返回 `design.graph_read` tool_call；收到结果后返回 `case.graph_propose`（在首个需求/场景下新增或修改一个确定性 draft 用例）；否则返回中文设计回复。**不确认**。
- `[profile:design_planner]`：返回本轮范围说明（引用 requirement 数/场景/用例数），不调用工具。
- `[profile:case_author]`：首次返回 `design.graph_read`；随后为需求生成 1 场景 + 2 用例 draft（含步骤/断言/SUT 目标），之后最终文本。
- `[profile:design_critic]`：返回 `design.review_submit`（1 条 minor finding），再最终文本。
- `[profile:data_planner]`：返回 `design.graph_read` 后对首个 draft 用例调用 `design.data_plan_submit`，再最终文本。
- 所有响应确定性；只允许 loopback。

## 6. 对话、实时图与事件

### 6.1 对话扩展
- `POST /api/versions/{id}/conversations` 接受 `kind=design`；默认标题 `用例设计 YYYY-MM-DD HH:mm`；创建后：
  - 澄清：保持 P1 行为（opening chat.turn、team clarification）。
  - 设计：enqueue opening `chat.turn`（team=design）；handler 触发 `DesignPlanner.StartRound`（best-effort，失败由 reconciler 兜底）。
- 设计消息：复用 P1 `conversation_message`（user/assistant、text/attachment parts）；附件走 P1 上传+`attachment.ingest` 管线（D48：设计页支持文件上传）；Design Host 可读附件文本。设计消息不写 `requirement_card`（用例卡在脑图/详情中展示）。
- 消息 part 不新增结构化类型（避免破坏 P1 前端 union）；SSE 事件 + 图刷新承载设计变化。

### 6.2 事件（P2 增量；channel 固定 `version:{id}`，scope 为十进制字符串）

| type | payload | 触发 |
|---|---|---|
| `design.round_started` | `round_id,conversation_id,activation,requirement_ids` | 新设计轮次开始 |
| `design.scope_activated` | `round_id,activation,requirement_ids` | 手工/自动激活 |
| `design.graph_delta` | `requirement_id,requirement_version_id,scenario_ids,case_ids,case_version_ids,op` | `case.graph_propose`（op=propose）/`design.data_plan_submit`（op=data_plan）落库 |
| `design.case_version_created` | `requirement_id,case_id,case_version_id,revision,status:'draft'` | 新 draft 版本 |
| `design.case_version_stale` | `requirement_id,case_id,case_version_id,reason` | 需求变化 |
| `design.case_version_superseded` | `case_id,case_version_id` | 新版本确认 |
| `design.case_confirmed` | `scope,target_id,batch_id,case_ids,case_version_ids` | 用户确认 |
| `design.review_submitted` | `requirement_id,round_id,count` | Critic |
| `design.export_ready` | `export_id,format,size,sha256` | 导出完成 |

- 事件 payload 不含用例正文/步骤全文/LLM 输入输出/导出内容；只带 ID/计数/状态。
- SSE 沿用 P0/P1：`event` 表持久化、`(channel,seq)` 游标、`stream.reset`；前端断线后通过快照 + 事件游标补齐。
- `requirement.rolled_back` / `requirement.version_created` 增加 `affected_case_version_ids`（P1 已预留字段，P2 填真实 ID；事件字段不破坏）。

### 6.3 实时渲染协议
- 图真值在 DB；前端 `GET /design/graph` 拉快照，收到 `design.graph_delta`/确认/stale 事件后去抖重拉（≤300ms）。事件只携带受影响 ID，不传整图。
- 节点位置只在本地（`@xyflow/react` 状态），不写库；刷新按确定性列布局重算。
- 断线重连：`useVersionEvents` 按游标补事件；错过事件时重拉快照。

## 7. REST API（P2 增量；详见 `contracts/openapi.yaml`）

通用：Bearer 登录；ID/时间/分页/错误信封沿用 P0/P1；全部登录用户可读写（P1 单团队模型）。

### 7.1 版本与对话
| 方法/路径 | 说明 |
|---|---|
| `POST /api/versions/{id}/conversations` | `kind` 增加 `design`；201 |
| `GET /api/versions/{id}` | 增量字段：`pending_design_count`（新语义）、`case_counts{draft,confirmed,stale}`；`auto_design_new_requirements` 已有 |

### 7.2 设计图/详情/概览
| 方法/路径 | 说明 |
|---|---|
| `GET /api/versions/{id}/design/graph` | 全版本设计图快照（§7.2.1） |
| `GET /api/versions/{id}/design/overview` | `{pending_design_count,auto_design_new_requirements,round_count,active_round:{id,round_no,conversation_id,activation,requirement_ids,created_at}|null}`；`round_no`=该版本 design_round 按 id 升序的序号（从 1 开始） |
| `GET /api/versions/{id}/design/cases/{caseId}?version_id=` | 用例详情 + 版本历史（§7.2.2） |

**7.2.1 `DesignGraphView`**
```json
{
  "version_id": "1001",
  "generated_at": "2026-10-31T12:00:00Z",
  "graph_hash": "64-hex",
  "pending_design_count": 2,
  "auto_design_new_requirements": false,
  "stats": {"requirements": 3, "scenarios": 5, "cases": 9, "draft": 4, "confirmed": 3, "stale": 2},
  "requirements": [{
    "requirement_id": "3001", "requirement_version_id": "3002", "revision": 3,
    "title": "登录", "status": "clarified", "design_status": "draft",
    "target_version_sut_ids": ["9001"],
    "scenarios": [{
      "id": "5001", "requirement_version_id": "3002", "name": "正常登录", "description": "",
      "version_sut_ids": ["9001"], "page_ids": ["..."], "api_ids": [], "sort": 0,
      "cases": [{
        "case_id": "7001", "current_version_id": "7002", "revision": 2,
        "title": "账号密码登录成功", "priority": "P1", "status": "draft",
        "default_version_sut_id": "9001", "version_sut_ids": ["9001"],
        "page_ids": ["..."], "api_ids": [], "step_count": 3, "assertion_count": 2,
        "created_at": "..."
      }]
    }]
  }]
}
```
- 包含：所有 clarified 需求 + 任意已有场景/用例的需求（含 draft/closed 需求的历史 stale 图），按 `requirement_id`、`scenario.sort_order,id`、`case_id` 排序。

**7.2.2 `CaseDetailView`**
```json
{
  "case_id": "7001", "status": "draft", "current_version_id": "7002",
  "requirement_id": "3001", "requirement_version_id": "3002", "scenario_id": "5001", "scenario_name": "正常登录",
  "versions": [{"case_version_id":"7002","revision":2,"status":"draft","title":"...","created_at":"...","confirmed_at":null}],
  "selected": {
    "case_version_id": "7002", "revision": 2, "title": "...", "description": "",
    "preconditions": "", "test_data": {}, "priority": "P1",
    "default_version_sut_id": "9001", "version_sut_ids": ["9001"], "page_ids": [], "api_ids": [],
    "status": "draft", "content_hash": "...", "created_at": "...",
    "steps": [{"seq":1,"action":"...","expected":"...","target_version_sut_id":null,"target_page_id":null,"target_api_id":null,"account_role":"admin","data":{}}],
    "assertions": [{"step_seq":1,"kind":"ui","target":"...","expected":"...","target_version_sut_id":null,"target_api_id":null,"database_id":null}],
    "sources": [{"kind":"requirement","ref_id":"3002","path":"","line":null,"sha":""}]
  },
  "confirmable": true
}
```

### 7.3 确认
```http
POST /api/versions/{id}/design/confirmations
{"scope":"case|scenario|requirement","target_id":"7001","batch_id":"optional≤64"}
```
200：
```json
{"scope":"scenario","target_id":"5001","batch_id":"batch_ab12...",
 "confirmed":[{"case_id":"7001","case_version_id":"7002"}],
 "skipped":[{"case_id":"7003","case_version_id":"7004","status":"confirmed"}],
 "confirmed_at":"..."}
```
错误：`invalid_argument`（scope/target 非法、需求未澄清、版本不匹配）、`state_conflict`（无 draft、当前 stale）、`not_found`。

### 7.4 导出
| 方法/路径 | 说明 |
|---|---|
| `POST /api/versions/{id}/exports` | `{owner_kind:"case",owner_id:"<version_id>",format:"markdown|mermaid|opml|csv"}`；201 `ExportView` |
| `GET /api/versions/{id}/exports?format=&limit=&before=` | 列表（id desc，默认 20，最大 100，返回 `{data,next_cursor,has_more}`） |
| `GET /api/exports/{exportId}` | `ExportView` |
| `GET /api/exports/{exportId}/download` | 二进制；`Content-Type`/`Content-Disposition`；sha256/size 校验失败 500 |

`ExportView`：`{id,version_id,owner_kind,owner_id,revision,format,size,mime,filename,sha256,status,created_at,download_url}`。
- DB 列与 API 字段映射：`export_artifact.file_format→format`、`size_bytes→size`、`content_object_id→sha256`；API 仍用产品名，DB 用规范名。
- P2 只接受 `owner_kind=case` 且 `owner_id=version_id`（整版本设计导出）；其他 owner_kind/format → 400 `invalid_argument`。
- 每次 POST 生成新 `revision`（max+1）与新的内容对象；重复下载不重复生成。

### 7.5 内部端口（非 HTTP）
- `RequestGap(ctx, GapInput)`：P3 通过 application port 调用；写 scope（若缺）+ 确保 `design.gap` 任务；发 `design.scope_activated`/`design.graph_delta` 由后续任务产生。
- `CountPendingDesign(ctx, versionID)`：version 详情投影使用。
- `MarkRequirementChanged(...)`：实现 `versionapp.CaseStaleHook`。
- `DesignStarter.StartRound(...)`：version 对话 handler/reconciler 调用。

## 8. 导出内容规范

所有格式均包含：版本标题、生成时间（UTC RFC3339）、需求（含修订号/状态/目标 SUT）、场景、用例（含当前版本号/状态/优先级/目标 SUT/页面/API）、步骤、断言、来源。导出范围与 `GET /design/graph` 一致（clarified 需求 + 任意已有场景/用例的需求）；只导出**当前版本**（draft/confirmed/stale），不导出 superseded 历史版本。需求无场景/用例时输出“未设计”占位。

- **Markdown**（`text/markdown; charset=utf-8`，`.md`）：`# 用例设计：<version>` → `## 需求：<title>（vN，status）` → `### 场景：<name>` → `#### 用例：<title>（vN，status，P1）` → 元数据列表 → 步骤表（序号/动作/预期/目标 SUT/页面/API/账号角色）→ 断言列表（步骤/类型/目标/预期）→ 来源。
- **Mermaid**（`text/plain; charset=utf-8`，`.mmd`）：`mindmap` 根 `version root((<title>))`；需求 → 场景 → 用例；用例叶子包含状态/优先级；标签截断 60 runes，转义 `()[]{}` 与换行；不含多行详情（详情在 Markdown）。
- **OPML**（`text/x-opml; charset=utf-8`，`.opml`）：OPML 2.0；`<outline text>` 树=版本→需求→场景→用例；用例 `_note` 含状态/优先级/前置/测试数据/步骤/断言；`encoding/xml` 转义；用标准库解析测试。
- **CSV**（`text/csv; charset=utf-8`，`.csv`）：**一行一步骤**；列固定：
  `requirement_id,requirement_title,requirement_revision,requirement_status,scenario_id,scenario_name,case_id,case_title,case_revision,case_status,priority,sut_names,page_names,api_targets,step_seq,step_action,step_expected,step_target_sut,step_page,step_api,account_role,assertions`
  RFC4180 引号/换行转义；断言列以 ` | ` 连接（`kind:expected`）；无步骤的用例输出一行空步骤列；UTF-8 无 BOM。
- 文件字节写入内容账本（`content_object`，sha256 + size + mime），`export_artifact.content_object_id` 存 sha；`content_ref` 账本登记（owner_kind=`export`）；文件下载前校验 sha/size；不暴露内部路径。
- 文件名：`case-export-{exportId}.{ext}`；`Content-Disposition: attachment; filename="..."`。
- 单次导出内容上限 10 MiB（内容账本上限），超限 413/`payload_too_large`。

## 9. 规划器、协调器与 reconciler

### 9.1 Planner（确定性 + LLM 范围说明）
- `StartRound` 在每版本 advisory lock 内幂等执行（见 §4.6）；`design.round` 任务只做范围说明，不写业务表。
- `ScanVersion` 由 design reconciler 调用，规则：
  1. 候选版本：存在 clarified 需求 / design 对话 / design_scope / case 的 `initial|running` 版本，按 id 取 100 个。
  2. stale 安全网：需求当前版本变化但当前 confirmed case 仍 confirmed → 置 stale（幂等，发事件）。
  3. pending 处理：该版本**还没有任何设计对话**且 auto 开关关闭 → 不动作（CTA）；没有设计对话且开关开启 → 自动建对话+round+激活；已有设计对话 → 激活并入最新 round + 任务。
  4. 已激活需求：当前需求版本无场景 → `design.requirement`；有 stale → `design.requirement`（重设计）；已有 draft/confirmed 覆盖 → 不重复建任务。
  5. 每个需求版本有 draft/confirmed 用例且 critic/data_plan 任务不存在 → ensure `design.critic`（有 draft 或 confirmed 时）与 `design.data_plan`（仅存在当前 draft 时；每个需求版本一次）。
  6. 幂等：全部 ensure 去重键；重复扫描不新增。
- Reconciler 周期沿用 P1 3s；`agent` reconciler 增加一个 `DesignReconciler` 端口（design app 实现），失败聚合重试。
- P1 对话 reconciler：查询包含 `kind`；设计对话不创建 `requirement.curate`；opening/reply 的 `chat.turn` team 按 kind 区分。

### 9.2 待设计计数与版本投影
- `versionapp` 定义 `PendingDesignCounter` 端口；main 注入 design adapter；`Get` 详情调用；失败回退 clarified 计数并记录日志（不阻塞详情）。
- 设计对话创建/激活成功时调用 `VersionReader.MarkRunning`（`initial→running`）。
- 版本详情新增 `case_counts`（该版本**当前**用例版本按 `draft/confirmed/stale` 计数，不含 superseded）。

## 10. 前端（`frontend/src/features/design/`）

### 10.1 信息架构
- `VersionDetailPage` 的 design Tab 渲染 `DesignTab`；`?tab=design` 深链；`?case=` 选中用例；`?conversation=` 选中设计对话。
- `DesignTab` 布局（桌面，用户 2026-10-31 裁决）：
  ```text
  ┌───────────────────────────────┬──────────────┐
  │ 脑图主区（React Flow）          │ 设计聊天      │
  │ 需求 → 场景 → 用例              │ 历史/新建对话  │
  │ 缩放/平移/节点确认按钮          │ 消息/附件      │
  ├───────────────────────────────┴──────────────┤
  │ 用例详情（整行：步骤/断言/来源/版本/确认）      │
  └──────────────────────────────────────────────┘
  ```
  顶部工具栏：标题 `用例脑图 · 设计轮次 #N`、统计、`导出用例`、自动开关、`＋ 新建对话`。
- 移动端（<1024px）：脑图、聊天、详情纵向堆叠；脑图横向滚动。
- 空态：无 clarified 需求 → “暂无已澄清需求，请先在需求与澄清中确认需求”；无设计对话 → 显示新建对话 CTA + 待设计数量。
- 错误态：请求失败显示可重试 ErrorState；SSE 断线显示重连状态（复用 P1）。

### 10.2 交互
- 节点：需求节点（标题、`已澄清 vN`、SUT/页面数、场景/用例/覆盖统计、`确认全部`）；场景节点（名称、用例数、SUT、`确认场景`）；用例卡（标题、状态徽标 draft/confirmed/stale、优先级、SUT chips、页面 chip、跨系统标记、`确认用例`入口）。
- 用例详情：元数据、步骤与目标 SUT、断言、来源、版本历史；`确认用例`（仅 draft）；`请求修改`（把“请修改：<用例标题>”填入聊天输入，不自动发送）；不显示 `查看执行结果`（P3）。
- 确认：按用例/场景/需求三粒度；确认后按钮禁用/状态更新；409 显示内联错误；AI 无确认入口。
- 设计对话：新建对话（`POST conversations kind=design`）、历史对话切换、消息 Markdown 渲染、附件上传、Enter 发送、SSE 实时、stale 系统提示（`design.case_version_stale`）；聊天区含只读“设计团队动态”（复用 P1 `TeamRoomPanel`，team=design）展示 Planner/Critic/Data Planner 帖子。
- 导出：点击 `导出用例` 打开格式选择（Markdown/Mermaid/OPML/CSV）→ POST → 下载；显示进行中/成功/失败；导出不含执行结果。
- 自动开关：PATCH version `auto_design_new_requirements`；说明“默认关闭；开启后新澄清需求自动开设计轮次并创建设计对话”。
- 待设计 CTA：`pending_design_count>0` 且无设计对话时，在 requirements 页和 design 页显示 `N 条待设计`，点击新建/跳转设计对话；P1 `DesignCTA` 从禁用占位改为可用链接。

### 10.3 组件与测试
- 组件：`DesignTab`、`DesignMindmap`（React Flow custom nodes）、`RequirementNode`、`ScenarioNode`、`CaseCard`、`CaseDetailPanel`、`DesignChat`、`Composer` 复用/扩展、`ExportCasesDialog`、`AutoDesignSwitch`、`PendingDesignCTA`。
- 图布局纯函数 `layout.ts`（确定性、可单测）：输入 graph → nodes/edges；空图/200 用例性能上限。
- `data-testid`：`design-tab`、`design-mindmap`、`design-new-conversation`、`design-chat`、`design-composer`、`case-card-{id}`、`btn-confirm-case-{id}`、`btn-confirm-scenario-{id}`、`btn-confirm-requirement-{id}`、`export-cases`、`export-format-{format}`、`auto-design-switch`、`pending-design-cta`、`case-detail`。
- 无障碍：按钮 aria-label、图节点键盘可选中、状态不只靠颜色。

## 11. 契约变更（P2 增量，阶段末冻结）

| 文件 | 变更 |
|---|---|
| `openapi.yaml` | 新增 §7 的 design/exports paths 与 schemas；`ConversationKind` 增加 design 创建；Version 详情增量字段；错误码沿用；`x-frozen: P2` |
| `events.md` | §11 P2 事件目录 + `affected_case_version_ids` 真实语义；事件字段/频道规则 |
| `db-schema.md` | §8 P2 表结构（10 表）、索引、约束、迁移归属；P2 新表清单 |
| `migrations-order.md` | 追加 `design_0001_case_model.sql`、`storage_0002_export_artifact.sql` |
| `task-types.md` | P2 5 类任务（team/profile/dedupe/payload/输出/失败语义）+ chat.turn design 语义增量 |
| `agent-profiles-tools.md` | 5 profiles、P2 工具 schema、作用域注入、`case.confirm` 不注册说明 |
| `settings.md` | P2 不新增键/env；说明 `auto_design_new_requirements` 是版本列，`paths.export_dir` P2 不使用 |
| `contracts/README.md` | 冻结状态 `frozen: P2` 与实现证据指针 |

## 12. 测试与验收计划

| 层 | 覆盖 | 命令/证据 |
|---|---|---|
| Go 单元 | 图校验（层级/seq/断言/目标/长度/上限）；状态机（draft/confirmed/stale/superseded）；确认 scope/幂等；stale hook；scope/round/pending；计划器幂等；导出 4 格式（确定性内容、转义、CSV 行）；并发锁/重复 hash | `cd backend; ..\.tools\go\bin\go.exe test ./internal/design/... ./internal/version/... ./internal/agent/... ./internal/storage/...` |
| Go 集成 | 迁移 17 个幂等/10 表；仓储 CRUD；propose 事务/重复幂等/非法回滚；confirm 原子/并发/部分失败；scope/round 唯一；export sha/下载；SSE 事件；设计任务/扫描幂等；pending/case_counts | `powershell.exe -File scripts/test-integration.ps1 ./internal/design/... ./internal/version/... ./internal/storage/...` |
| 前端单元 | api 类型/DTO；布局纯函数；DesignTab 空/加载/错误；图节点选择；三粒度确认；导出版本选择/下载；自动开关；CTA；SSE 合并；stale 提示 | `cd frontend; pnpm exec vitest run src/features/design src/features/version` |
| API 验收 | 隔离 DB + fake LLM（确定性回归）：需求→设计对话→扫描→图；对话修改→draft；三粒度确认；需求变化→stale→重设计→superseded；4 格式导出+sha；D25 三种路径；SSE 断言 | `powershell.exe -File scripts/p2-acceptance.ps1 -FakeLLMPort 13334`；证据 `.tmp/p2-acceptance/api.log` |
| 浏览器 E2E | 登录→版本→设计 Tab→新建对话→图实时→详情→确认→导出下载；无 console error；截图（fake 确定性回归） | `powershell.exe -File scripts/p2-e2e.ps1`；证据 `.tmp/p2-acceptance/screenshots/` |
| 真实 LLM | `.env` `llm_*`（`deepseek-flash`）→ admin 配置→设计对话→真实工具调用产 draft→对话修改→确认→导出；语义断言（覆盖/无重复/状态/工具合法）；带超时/重试；凭据缺失则 handoff 标 blocked | `powershell.exe -File scripts/p2-acceptance-llm.ps1`；证据 `.tmp/p2-acceptance/llm.log` |
| 真实 LLM 浏览器 | 真实模型 + Playwright：设计页新建对话→图渲染→对话修改→确认→导出；允许语义等待/重试 | `powershell.exe -File scripts/p2-e2e-llm.ps1`；证据 `.tmp/p2-acceptance/e2e-llm/` |
| 早期真实探针 | 契约冻结前跑一次真实产图，验证 `case.graph_propose` schema/提示词可被模型稳定调用；失败则先修 schema/prompt | `powershell.exe -File scripts/p2-acceptance-llm.ps1 -ProbeOnly`；日志 `.tmp/p2-acceptance/llm-probe.log` |
| 全量门禁 | `scripts/test.ps1` 全绿（Go 单测+集成、前端 lint/typecheck/test）；契约解析；UTF-8 扫描 | 日志 `.tmp/p2/final-gate.log` |

**验收清单（映射 phase-02）**
- [ ] 已澄清需求 → 新建设计对话 → 团队扫描 → 初版脑图实时渲染。
- [ ] 对话修改 → 新增/修改 draft → 单用例/场景/需求确认；AI 不能确认。
- [ ] 需求版本变化 → stale（事件真实 affected IDs）→ 重设计；新 draft 不改旧 confirmed；新确认后旧 confirmed superseded。
- [ ] 导出 4 格式可用、内容完整、sha256 一致、鉴权下载；重复导出新 revision。
- `[ ]` D25：默认（无设计对话）CTA；已有设计对话自动并入；`auto_design_new_requirements` 开启且无设计对话时自动开轮/建对话。
- [ ] 跨 SUT 用例卡（步骤级目标）可表达并通过校验。
- [ ] 设计对话附件可上传并被 Host 读取。
- [ ] `scripts/test.ps1`、P2 API 验收（fake）、E2E（fake）、真实 LLM API 验收、真实 LLM 浏览器主链路、早期真实产图探针通过；契约冻结；handoff 完成。

## 13. 并行实施建议（供 plan 使用）

- 写作用域拆分（互不重叠）：
  - **Lead**：契约（openapi/events/db-schema/migrations-order/task-types/agent-profiles-tools/settings）、迁移文件与 order、server/main 装配、验收脚本、证据、handoff。
  - **backend-design**：`backend/internal/design/**`（domain/app/repo/http）+ design 迁移表仓储；`storage` export_artifact 表与 service；version 的 pending 计数/stale hook 调用点小改（如与 backend-agent 冲突则由 Lead 收口）。
  - **backend-agent**：`backend/internal/agent/**`（profiles/tools/runner/reconciler/ports）、design profiles/tools 适配、fake LLM；`backend/internal/version` 对话 kind（与 backend-design 的 version 小改按文件拆开：conversation* vs requirement/service 投影）。
  - **frontend**：`frontend/src/features/design/**`、`VersionDetailPage`、`DesignCTA`、lib 下载 helper；不碰后端。
  - **acceptance**：`scripts/p2-*`、`scripts/fake-llm.cjs` 扩展（与 backend-agent 共享 fake LLM 文件 → 归 backend-agent 或 Lead，避免并发）。
- 依赖顺序：契约骨架 → 迁移/domain/仓储 → 服务/API → Agent/工具 → 前端 → 验收/真实 LLM → 全量门禁/handoff。
- 并行安全：design 后端与 frontend 可在契约冻结后并行；Agent 接入依赖 design application 接口（先冻结接口签名）；验收脚本依赖 API 完成后编写。

## 14. 实施裁决（P2-D；冲突/缺口落地，不改设计文档）

| ID | 裁决 | 依据/理由 |
|---|---|---|
| P2-D-1 | 阶段按 P2=phase-02 实施，忽略提示词误用的 P0 读取清单 | `implementation-phase-plan.md` §0 阶段表；P1 先例 |
| P2-D-2 | 新增 `design` 上下文；扩展 version/agent/storage；不新增前端路由 | HLD §1.1；Agent.md §6.2 四层/导入规则 |
| P2-D-3 | 设计对话复用 `Conversation(kind=design)`；`chat.turn` 按会话 kind 选择 Host；P2 不关闭设计对话 | HLD §3.1/§6.4/§8.1；用户 2026-10-31 裁决 |
| P2-D-4 | 5 个 design 任务类型与 profile 映射（HLD §3.1 为规范名；architecture §5.3 的示例名 `case.author`/`clarify.turn` 不作为契约） | HLD §3.1/§4.1；contracts 预留 |
| P2-D-5 | 写工具使用 `case.graph_propose`（architecture §7.3）；HLD 的 `design.scenario_propose/case_propose/case_update` 由该工具统一承载；`case.confirm` 不注册为模型工具 | architecture 权威 > HLD 细节；D18/D40 |
| P2-D-6 | `current_version_id` 在新 draft 创建时立即移动；执行只认 current+confirmed；旧 confirmed 在**新版本确认后** superseded；stale→superseded 允许（同一 case） | architecture §6.3 L266-270；§6.5；D22 |
| P2-D-7 | 场景绑定 requirement_version；需求版本变化→新场景；case 内容更新仅限同一需求版本；旧场景/stale 用例保留历史 | HLD §2.2；architecture §6.2/§6.3；补足 C4 的覆盖链接 |
| P2-D-8 | 目标继承：step 空=继承 case 默认；case 默认必填∈version_sut 集合；page/api/db 必须属于对应 version_sut 与版本绑定 | architecture §6.3 L273、§11.1；OD-6/OD-7 |
| P2-D-9 | 断言 kind 五值全部允许；db 断言需 database_id；来源 kind 五值；P2 重点 requirement | HLD §2.2；P3/P5 消费；避免后续迁移 |
| P2-D-10 | 确认：任意登录用户；case/scenario/requirement 三 scope；整批事务；重复幂等；无 draft→409；batch_id 服务端生成 | D21/D40；P1 确认先例；补足 C10/C14 |
| P2-D-11 | `design_round` 每对话一行；P2 只写 active（completed 预留）；**当前轮=最新 design 对话，不关闭** | architecture §6.3 L275；D25；用户 2026-10-31 裁决 |
| P2-D-12 | `pending_design_count` = clarified 且不在 active design_scope 的需求数 | P1 handoff §5；P2 替换占位；D25 CTA |
| P2-D-13 | D25：手工轮次激活全部 pending；已有设计对话时新 clarified 自动并入；自动开关仅在无设计对话时自动建对话+轮次；不自动确认 | D23/D25；architecture §8.4 #7；用户 2026-10-31 裁决 |
| P2-D-14 | Planner 确定性扫描 + `design.round` LLM 只产范围说明 | architecture §7.1（planner 只规划）；可测试/确定性 |
| P2-D-15 | design reconciler 兜底：stale 安全网、pending/auto、任务幂等、critic/data_plan | architecture §5.3/§8.4；HLD §3.3 |
| P2-D-16 | 需求 `Update` 与 `Rollback` 均调用 stale hook；事件 `affected_case_version_ids` 填真实 ID | D15；P1 预留；补齐 P1 漏调用 |
| P2-D-17 | 图写并发：每需求 `pg_advisory_xact_lock` + 内容 hash 幂等 + 自然键去重 + 确认行锁；`case.graph_propose` 可选 `base_graph_hash`，过期 409 | 0.2 多用户协作非目标；补足 C5；用户 2026-10-31 工具裁决 |
| P2-D-18 | 图提交上限：每次 propose ≤3 场景/≤10 用例；每需求版本 200 用例/100 场景、每用例 50 步/50 断言/20 来源；字符串长度见 §3/§5 | 实现边界；防滥用/演示性能 |
| P2-D-19 | 事件命名用 `design.*` 点号风格；channel 仅 `version:{id}`；payload 只带 ID/计数 | P0/P1 事件风格；HLD 事件示例 |
| P2-D-20 | `export_artifact` 表归 storage 上下文（内容账本延伸）；P2 导出整版本 case 域；字节入 content ledger，`paths.export_dir` P2 不写 | HLD §1.1 storage 职责；architecture §8.6/§12.3；补足 C15 |
| P2-D-21 | 导出同步生成；格式/内容/CSV 一行一步骤；不新增设置/env | OD-1；architecture §18；P0 设置冻结 |
| P2-D-22 | 设计对话支持附件（D48）：复用 P1 上传/ingest；Host 可读文本；不新增消息 part 类型 | D48；P1 附件管线；兼容性 |
| P2-D-23 | 前端使用已安装 `@xyflow/react` 实现脑图平移/缩放；节点位置不落库；确定性列布局；布局=上脑图+聊天、下详情整行 | HLD §8.2/§8.3；依赖已存在；用户 2026-10-31 布局裁决 |
| P2-D-24 | 首次设计对话创建触发 `initial→running` | HLD §6.7 迁移表；P1 投影服务复用 |
| P2-D-25 | 不新增系统设置键；`auto_design_new_requirements` 是 `version` 列；P2 不使用 `paths.export_dir` | D49/D50；P0 settings 冻结 |
| P2-D-26 | 错误码沿用 P0/P1（invalid_argument/state_conflict/not_found/forbidden/ai_unavailable/lease_lost）；不新增导出专用码 | 稳定错误面；P1 先例 |
| P2-D-27 | 关闭设计对话（结束本轮）不在 P2 实现，保留给后续阶段；当前轮=最新设计对话 | 用户 2026-10-31 裁决 |
| P2-D-28 | 表/字段/索引按 Agent.md §6.9/D34 规范化：`case_scenario`、`export_artifact`、`sort_order`、`line_no`、`file_format`、`size_bytes`、`content_object_id`；PK/UK/IDX 全部显式 `pk_/uk_/idx_` 命名 | 用户 2026-10-31 裁决“选 A，其它规范也要遵守”；Agent.md §6.9 |
| P2-D-29 | 导出字节复用 P0 `content_object`/`content_ref` 账本（而不是设计自有 export_dir 文件存储）；作为 Agent.md §6.8.3 的**显式例外**记录理由 + 依据 D65/architecture §12.3；用户 2026-10-31 已批准此例外 | 用户裁决；Agent.md §6.8.3 允许“确需复用须在阶段规格记录理由与 D#”；architecture §8.6/§12.3、OD-4 |
| P2-D-30 | LLM 测试双轨：fake 负责确定性回归/故障注入；真实 LLM 负责 API 验收 + 浏览器主链路 + 早期产图探针；真实失败/缺 key → blocked，不以 fake 替代 | 用户 2026-10-31 裁决；0.2 质量目标；P1 教训 |

## 15. 风险与已知缺口（写入 handoff）

1. **图编辑并发**：P2 单实例 + 每需求 advisory lock；Host/Author 并发写同需求会串行，但跨需求可并行；多副本/实时协作 P7。
2. **真实 LLM 图质量/稳定性**：`case.graph_propose` 校验严格，真实模型可能多次校验失败或输出波动；prompt/schema 需迭代；真实 LLM 验收允许语义等待/重试，但失败必须阻断 handoff；fake 通过不证明模型质量。P8 做质量评测。
3. **实时渲染**：事件只带 ID，前端重拉快照；200 用例内性能目标；超大图截断/虚拟化留 P6/P8。
4. **导出/GC**：P2 字节入内容账本（§6.8.3 例外，P2-D-29）；P7 D65 统一 GC 直接消费 `content_ref`；`paths.export_dir` 未使用，需 P7 决策是否需要物理导出目录。
5. **附件 ingest 团队归属**：设计对话附件复用 clarification ingest 任务；Scout finding 落 clarification 频道（P3/P6 可调整）。
6. **设计轮次“活跃”语义**：按用户裁决，最新设计对话永远活跃、不关闭；因此该版本一旦建过设计对话，后续新需求会持续自动并入，`auto_design_new_requirements` 只在首条设计对话前生效。这是产品裁决，handoff 明确记录；若后续需要“结束本轮”，走增量契约。
7. **P3 消费契约**：P3 只读 current+confirmed；P2 提供真实数据与事件，但执行取消/重派在 P3。
8. **契约冻结**：P2 若需字段/状态机变更，先停止并更新本规格/contracts/handoff，不在实现中静默漂移。
