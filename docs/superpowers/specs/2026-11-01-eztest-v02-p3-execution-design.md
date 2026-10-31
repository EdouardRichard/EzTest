# EzTest 0.2 P3 设计（执行/波次/证据/多 SUT）— 实施规格

> 状态：**待用户审核**（2026-11-01）。本规格通过 brainstorming 澄清（P3/P4 边界、demo 目标系统、LLM 验收策略已由用户裁决）。
> 阶段：P3。阶段文件：`docs/version-V0.2/phases/phase-03-execution.md`。
> 前置 handoff：`docs/version-V0.2/phases/phase-02-handoff.md`（P2 completed）。
> 本规格只实现 P3；P4+ 内容只进入 handoff/backlog/契约保留位，不实现、不提前建表。
> 本规格不修改 `docs/version-V0.2/**` 设计文档；冲突按权威顺序裁决并记录在 §17（P3-D）。
> 用户裁决（2026-11-01）：失败/覆盖分析 **仅 P3 自有信号**（不建 `analysis_run`/`issue`/`report`）；demo **扩展现有 fixture-target**；P3 验收 **仅真实 LLM**；同时把 `Agent.md` 中 0.1 的 P1–P7 编号标注为 0.1。
> 用户补充（2026-11-01）：**Agent 必须能处理时间窗口/异步等待**（如 120s 确认弹框超时消失）；P3 通过 API 侧 `execution.wait` 工具 + 页面定时器继续运行实现，不占用执行器 action 时限。

## 0. 依据、阶段映射与前置检查

**权威顺序**：`architecture-design.md` → `high-level-design.md` → `research/decision-log.md` → `research/open-decisions.md` → `research/capability-matrix.md` → `phases/phase-03-execution.md` → P0/P1/P2 frozen `contracts/*` → P0/P1/P2 handoff。

**阅读依据（实际查阅）**
- `architecture-design.md` §5.2–5.5、§6.1–6.5、§7、§8.5、§9、§10、§11、§12.4、§13–§16、§18
- `high-level-design.md` §1–§8、§11–§12
- `research/decision-log.md` D01–D53、§11a、§12
- `research/open-decisions.md` OD-1…OD-11（已关闭）与关闭记录
- `research/capability-matrix.md` A4/A6/B*/C*/D*/E*/I* 中与执行/证据/验收相关项
- `implementation-phase-plan.md` §0 阶段复制表、§1.2、§3 P3、§4 交接机制、§5 Review Focus
- `phases/phase-03-execution.md`、`phase-02-handoff.md`、`phase-04-delivery-chain.md`（边界只读）
- `contracts/README.md`、`openapi.yaml`（60 paths/100 schemas，`x-frozen: P2`）、`events.md`、`db-schema.md`、`migrations-order.md`、`task-types.md`、`agent-profiles-tools.md`、`settings.md`
- `ui/mock.html` + `ui/mock.css`（执行区 CSS 残留 L292–337；mock 无执行页）
- 现有实现：`backend/internal/{agent,resource,version,design,storage,eventing,audit,settings,platform,db,server}`、`executor/`、`frontend/src/features/*`、`scripts/test.ps1` 与 P1/P2 验收脚本
- P0/P1/P2 handoff 与 spec/plan（P0 无执行器集成；P2 current+confirmed 用例输入）

**阶段映射与编号**
- `implementation-phase-plan.md` §0 明确：**P3 → `phase-03-execution.md`，PREV_HANDOFF=`phase-02-handoff.md`，本阶段写 `phase-03-handoff.md`**。
- `Agent.md` §6.1 的 P1–P7 是 **EzTest 0.1 阶段编号**（目录树描述旧 `run_batch` 模型）；用户已要求标注为 0.1。P3 只按 `docs/version-V0.2` 的 P3=执行实施，不恢复 0.1 批次模型。
- P2 handoff 已记录 `Agent.md` §6.1 未更新；本阶段做“0.1 标注”文档修正，不改其设计权威地位。

**前置检查（开始实施前执行；不满足则停止报告）**
1. `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1` → **EXIT=0**（P2 全量门禁）。结果写入 `.tmp/p3/baseline-test.log`。
2. `node scripts/p2-contract-check.cjs` → 60 paths / 100 schemas / P2 increments（当前基线）。P3 阶段内改为兼容模式（见 §14）。
3. P1/P2 frozen 契约只增量：不改 17 个已应用迁移、不改 P2 用例/导出字段与语义。
4. `.env` 有真实 DeepSeek 凭据（`llm_api_key/llm_base_url/llm_model_name` 小写键）；API 进程仍只读 4 项 `EZTEST_*` env。
5. executor `127.0.0.1:13336` 可启动（Playwright 1.64.x）；P3 验收由脚本启动，不依赖当前手动进程。
6. 工作树 dirty 文件（`docs/development.md`、`docs/startup.md`、`scripts/dev-all.ps1`、用户专用文件）不属于 P3，提交不纳入。

**关键设计事实（带来源）**
- 执行是**意图驱动 + 自动波次**，无“开始执行”按钮；确认即入队（D28、D29）。
- 同版本单活跃波（`planned/running/paused` 都算活跃）；运行中新确认用例进下一波（D13/D29、HLD §2.3）。
- 跨系统用例步骤级指定 SUT；一个 case_run 多 SUT 上下文；整案调度、一次性预留配额（D12/D13、ARCH §11）。
- 执行层统一 `version_sut_id`；页面/API/账号/DB 必须属于同一 version_sut（ARCH §11.1）。
- 四态 = `passed/failed/blocked/inconclusive`；`skipped/interrupted` 是过程态（ARCH §6.4）。
- `case_run.failure_class` 仅 `failed(application)` 产问题候选；P4 建 issue/report（HLD §6.6、phase-04）。
- 执行器是独立 Node/Playwright 进程；**Case Executor（API 内 Agent）决定做什么，执行器只执行动作/回传证据，不接触 LLM 上下文**（HLD §1.3 L68）。这是 P3 执行主体模型的权威依据（`P3-D-10`）。
- 暂停/恢复是执行控制，不是版本状态；支持波次级/用例句级（D31）。
- 登录接管是异常例外：`human_request(login_takeover)`，波次等待，用户完成后恢复，超时按策略失败/跳过（ARCH §8.5-7）。
- 证据不可变；P3 证据字节复用 P0 内容账本（与 P2 导出例外同源，`P3-D-20`）。
- 版本状态只是投影；P3 创建 intent 时若版本为 `completed`，通过 version 端口清除完成标记回 `running`（HLD §6.7；`P3-D-31`）。

### 0.1 请用户重点确认的裁决（其余为实现级细节，见 §17）

1. **执行器注册采用 API 轮询（pull）而非执行器 push**：P3 单执行器下，API 按 `execution.executor_heartbeat_seconds` 轮询 `GET /healthz`，首次成功即登记 `executor_instance`，`last_heartbeat`/容量/上下文数/版本取自健康响应；`draining` 由管理员 API 摘流；`offline` 由心跳超时判定。HLD 的 `/api/internal/executors/*` push 端点留 P7（多副本）实现，P3 不新增内部鉴权 scheme。理由：单实例 P3、避免第 5 个 env 与执行器出站配置；行为等价（注册/心跳/容量/排空/离线可测）。
2. **执行器 token 放 DB 设置 secret 键**：新增 `execution.executor_token`（string，`is_secret=true`，默认 `eztest-dev-executor-token`，首次启用 secret 语义）。执行器进程用自身 env `EZTEST_EXECUTOR_TOKEN` 接收同一 token（**不是 EzTest API 启动 env**）；`scripts/dev-executor.ps1` 从 DB 读取当前值。**不新增第 5 个 EzTest 启动 env**。
3. **登录接管采用“暂停-恢复 + 用户响应”模型**，不做远程可视化/VNC：Agent 遇到 2FA/验证码时调用 `human.request`；case_run/wave 置 `paused(reason=waiting_human)`、work_item `waiting_human`；用户在执行 Tab 填一次性验证码/备注并 resolve；reconciler 以新 `dispatch_epoch` 续跑，Agent 从首个未通过步骤继续（登录重试并使用一次性响应）。`storage_state` 导入保留为后续增量，P3 验收用 2FA 数字码。
4. **P3/P4 边界（用户已选 A）**：P3 不建 `issue`/`report`/`analysis_run` 表；失败分类落 `case_run.failure_class`，问题候选以 `execution.failure_candidate` 事件 + `team_room_post`（refs 带候选字段）交付 P4；覆盖缺口只创建 P2 已冻结的 `design.gap` work item + 事件。P4 从 P3 表/事件重建 issue/report。
5. **失败分类枚举**（`P3-D-12`）：`application|environment|session|model|automation|timeout|executor_lost|blocked|unknown`；仅 `application` 产问题候选；`interrupted/skipped` 不是失败类，而是过程态。
6. **intent 幂等键**（`P3-D-08`）：partial unique `(case_version_id, environment_fingerprint, code_baseline, purpose) WHERE status IN ('open','claimed','running')`；`version_sut_ids` 为 JSON 数组；`environment_fingerprint` 与 `code_baseline` 由 §5.1 的确定性算法生成；`purpose` 三值 `initial/retest/manual`。
7. **证据存储**（`P3-D-20`）：`evidence` 存 `content_ref_id`（P0 内容账本）+ `sha256/size/mime`；字节不入 PG；`paths.evidence_dir` **P3 不使用**（与 P2 `paths.export_dir` 例外一致），P7 D65 GC 直接消费 `content_ref`。下载走 P3 鉴权端点并校验 sha/size。
8. **Analysis 持久化**（`P3-D-18`）：Failure/Coverage Analyst 的产物 = `case_run.failure_class`/`summary` + `team_room_post` + `execution.*` 事件；不建 `analysis_run`（P4 建）。Agent 失败/重试不影响确定性兜底：wave 终态时服务端先确定性分类/算缺口，Analyst 只做增强与解释。
9. **新建 5 个设置键**（`P3-D-19`；44→49）：`execution.executor_token`（secret）、`execution.executor_heartbeat_seconds`（10）、`execution.executor_offline_seconds`（30）、`execution.executor_context_ttl_seconds`（300）、`execution.human_request_timeout_seconds`（1800）。
10. **验收策略（用户已选 C：仅真实 LLM）**：P3 的 API/E2E 验收脚本全部使用 `.env` 的真实 DeepSeek；不新增/不使用 fake LLM 执行场景。单测/集成测试用 stub/fake port，不属“验收”。真实失败不用 fake 替代；失败即 blocked 并记录。验收用 SQL 预置 draft 用例 + P2 确认 API 生成 current+confirmed（P2 已验收设计流程，P3 不重复跑设计 LLM）。
11. **Agent.md 0.1 标注**：在 §6.1/§6.5/§6.7–§6.9 的 P 编号前加 `0.1-` 或加显著说明；不改目录树语义、不把 0.1 模型当 0.2 依据。
12. **P3 表集 10 张**（`P3-D-04`）：7 核心 + `executor_instance` + `evidence_ref` + `human_request`；`sut_account_session` 不建（P3 用 `case_run_sut_context.storage_state` 占位）；`analysis_run` 不建。
13. **Agent 等待/时间窗口（用户补充）**：新增 `execution.wait {seconds:int(1..300), reason?}` API 侧工具；等待期间租约/心跳继续、页面定时器继续运行、可被暂停/取消/lease 丢失打断；不占用执行器 action 60s 上限；单次 ≤300s、总时长受 `execution.case_timeout_seconds`（默认 900s，可 admin 调整）约束；120s 时间窗口场景纳入验收（`-LongWait`）。执行器仍不引入 sleep 语义（`P3-D-41`）。

## 1. 目标与退出标准

**目标**：打通“已确认用例 → `execution_intent` → 自动组波 → 多 SUT 执行 → 四态结果/证据 → 失败/覆盖分析 → 执行 UI/登录接管”的垂直闭环。

**退出标准（逐条可验证）**
1. current+confirmed 用例版本自动生成 intent；同组合重复确认/重复扫描不产生重复 intent；intent 自动组波，无“开始执行”按钮；单 SUT 用例可执行并落 step/assertion/evidence。
2. 跨系统用例在 ≥2 个 `version_sut` 上下文执行；步骤/断言/证据/失败分类均带 `version_sut_id`；波次 counts 按 SUT 分组。
3. 并发配额 3/2/1 生效；跨系统用例一次性预留多 SUT 配额，无 A 等 B 死锁；cleanup 后才释放配额。
4. 波次级/用例句级暂停/恢复可验证；`paused` 仍占单活跃波；恢复后从首个未通过步骤续跑。
5. `human_request(login_takeover)` 可创建、等待、resolve、超时失败并记录；波次暂停/恢复正确。
6. 执行器注册/心跳/容量/排空/offline 可验证；offline 的 case_run 置 `interrupted(executor_lost)`、`dispatch_epoch++`、按 intent 重派；旧 epoch 写入被拒绝。
7. stale/superseded/非 current 用例取消 active intent、终止在飞 case_run 并记录 `superseded`；不再生成新 intent。
8. 人工重跑/复测新建 intent（`retest/manual`），旧 intent/结果保留；重复确认不重复执行。
9. 失败分类正确；仅 `failed(application)` 产问题候选事件/帖；覆盖缺口创建 `design.gap`；P4 可消费 `wave/case_run/step/assertion/evidence/failure_class`。
10. **时间窗口/异步等待可验证**：`execution.wait` 支持 ≥120s 等待；等待期间页面定时器/弹框按预期变化；暂停/取消可中断等待；等待计入用例超时而非失败。
11. 证据不可变、sha256/size/version_sut/kind 完整；鉴权下载一致；无更新/删除 API。
12. 全量测试绿；P1/P2 回归通过；真实 LLM API 验收 + 浏览器 E2E 通过；contracts 冻结 `P3`；`phase-03-handoff.md` 完成。
13. `Agent.md` 的 0.1 编号已显著标注；P3 未修改 `docs/version-V0.2` 设计文档。

**不在 P3**
- `issue/issue_ref/issue_status_history/analysis_run` 表、问题确认/合并、报告 `report/report_revision`、探索链路（P4）。
- 代码智能/视觉断言实现（P5）；P3 中 `case_assertion.kind=visual` 只记 `skipped` 并在 case 结果说明，不判 passed。
- P9 多副本部署编排（P7）；P3 只做单实例注册/心跳/容量/排空/fencing/离线重派原语。
- 需求/设计/用例确认（P2）；P3 只消费 current+confirmed。
- 通用 `Idempotency-Key` 中间件（P0/P1 已记录不做）；P3 用自然唯一约束 + 幂等状态迁移。
- 远程浏览器/VNC/可视化接管；P3 用暂停-恢复 + 一次性响应。
- 非 PostgreSQL 目标库断言；外部工单/CI/IM；多租户/RBAC；生产级安全加固。

## 2. 实施策略与仓库边界

- 新建限界上下文 `backend/internal/execution/{domain,application,infrastructure,interfaces/httpapi}`，四层齐全。
- 扩展既有上下文（只做增量）：
  - `agent`：新增 3 个 P3 profile、3 个任务类型映射、执行工具注册与任务上下文端口、worker 输出路由；不改 P1/P2 profile。
  - `design`：新增只读方法 `GetCaseVersion`/`ListExecutableCaseVersions`（执行只读端口），不改 P2 写语义。
  - `version`：新增 `Get`（已有）、`Reopen`（completed→running）；不改 P1 字段。
  - `resource`：新增 secret 读取方法（供执行适配器，不经 HTTP 暴露）；不改资源表。
  - `storage`：复用 `Upload/AddRef/Open`，不新增表/方法（证据归属由 execution 维护）。
  - `agent`/`server`/`cmd/eztest`：装配与路由；共享契约由 Lead 收口。
- 前端新增 `frontend/src/features/execution/`；`VersionDetailPage` 的 execution Tab 从占位改为真实组件；不新增全局路由。
- executor 扩展：`executor/src` 增加 context 元数据与 `GET /v1/contexts`（用于容量/孤儿清理），其余协议保持；不引入 case/wave 语义（LLM 编排在 API 侧，`P3-D-10`）。
- 单实例假设沿用 P1/P2；P3 不承诺多副本实时/共享存储。
- 写作用域：后端模块 / 前端 feature / executor / 测试脚本 / 契约分线；`contracts/*`、`openapi.yaml`、`migrations-order.md` 由 Lead 统一收口。

### 2.1 规范符合性（Agent.md；0.1 编号除外）

| 规则 | P3 落地 |
|---|---|
| §6.1 目录/四层 | 新增 `execution` 四层；不新建 `utils/common/helpers`；共享纯逻辑进 `platform` |
| §6.2 导入方向 | domain 只依赖 stdlib/platform；application 不导 infra/httpapi；跨上下文只依赖 application 端口 |
| §6.3 迁移 | `execution_0001…0003`；只追加；`order.txt` 末尾；一个文件一个上下文；启动 advisory lock/checksum |
| §6.4 前端 | 只走 `api-client.ts`/`sse-client.ts`；业务在 `features/execution/`；`src/app` 只路由 |
| §6.5 新上下文 | 四层 + `cmd/eztest` 装配 + 迁移 + 前端 feature；后台调度由 `cmd/eztest` 注入并随进程启停 |
| §6.6 ARTEX | 不参考/不复制 ARTEX；0.1 同仓实现仅作结构参考，重写为 0.2 模型 |
| §6.7 ID | 雪花 `BIGINT`；JSON/URL/工具参数 ID 为十进制字符串；无 `BIGSERIAL` |
| §6.8 文件 | 证据字节入内容账本（不写 PG 二进制）；鉴权下载；不暴露路径；`paths.evidence_dir` 例外记录 |
| §6.9 DB | 表/字段/索引命名规范；PK/UK/IDX 显式；跨上下文无 FK；审计列齐全；迁移只追加 |

## 3. 数据模型与迁移（只追加）

新增 3 个迁移，追加到 `backend/internal/db/migrations/order.txt`（第 18–20 个）；不改 P0/P1/P2 已应用迁移。所有新表雪花 `BIGINT` 主键（应用层生成）；JSON 中 ID 为十进制字符串；审计列 `created_at/updated_at TIMESTAMPTZ NOT NULL DEFAULT now()`；跨上下文只逻辑引用、不建 FK；同上下文 FK 显式。

**命名规范**：表名小写单数；PK `pk_<表名>`；UK `uk_<表名>_<列...>`；IDX `idx_<表名>_<列...>`；字段小写下划线；枚举全 CHECK；JSONB 列 `NOT NULL DEFAULT` + `CHECK (jsonb_typeof(...))`。设计文档字面名映射：`execution_intent`/`run_wave`/`case_run`/`case_run_sut_context`/`step_result`/`assertion_result`/`evidence`/`evidence_ref`/`executor_instance`/`human_request` 原样保留。

### 3.1 `execution_0001_intent_wave.sql`（上下文：execution，4 表）

**`execution_intent`**
`id BIGINT`、`version_id BIGINT NOT NULL`、`case_version_id BIGINT NOT NULL`、`version_sut_ids JSONB NOT NULL DEFAULT '[]'`、`environment_fingerprint TEXT NOT NULL`（64 hex）、`code_baseline TEXT NOT NULL DEFAULT ''`（≤256）、`purpose TEXT NOT NULL DEFAULT 'initial'`（CHECK `initial/retest/manual`）、`status TEXT NOT NULL DEFAULT 'open'`（CHECK `open/claimed/running/done/failed/cancelled/superseded`）、`dedupe_key TEXT NOT NULL`（1–256）、`attempt_no INT NOT NULL DEFAULT 1`（CHECK ≥1）、`created_by BIGINT NULL`、`created_at/updated_at`。
约束：`pk_execution_intent`；**partial unique** `uk_execution_intent_active UNIQUE(case_version_id, environment_fingerprint, code_baseline, purpose) WHERE status IN ('open','claimed','running')`；`uk_execution_intent_dedupe UNIQUE(version_id, dedupe_key)`（同一 key 只允许历史多次？见下）。
说明：重跑/复测需要“同 key 新建 intent”，因此 `uk_execution_intent_dedupe` 不能全时唯一。改为 **非唯一索引** `idx_execution_intent_dedupe(version_id,dedupe_key)`；幂等由 partial unique + 状态机保证。索引：`idx_execution_intent_version_status(version_id,status,id DESC)`、`idx_execution_intent_case_version(case_version_id,status)`、`idx_execution_intent_dedupe(version_id,dedupe_key)`。

**`run_wave`**
`id BIGINT`、`version_id BIGINT NOT NULL`、`status TEXT NOT NULL DEFAULT 'planned'`（CHECK `planned/running/paused/done/failed/cancelled`）、`trigger TEXT NOT NULL DEFAULT 'auto'`（CHECK `auto/manual/retest`）、`counts JSONB NOT NULL DEFAULT '{}'`、`started_at TIMESTAMPTZ NULL`、`finished_at TIMESTAMPTZ NULL`、`paused_at TIMESTAMPTZ NULL`、`paused_by BIGINT NULL`、`pause_reason TEXT NOT NULL DEFAULT ''`（≤200）、`pause_mode TEXT NOT NULL DEFAULT ''`（CHECK `''/graceful/immediate`）、`report_id BIGINT NULL`（P4 逻辑引用；P3 恒 NULL）、`created_at/updated_at`。
约束：`pk_run_wave`；**partial unique** `uk_run_wave_active UNIQUE(version_id) WHERE status IN ('planned','running','paused')`；`idx_run_wave_version(version_id,id DESC)`、`idx_run_wave_status(status,id)`。
`counts` 结构（§5.6）：`{"total","pending","running","paused","passed","failed","blocked","inconclusive","skipped","interrupted","superseded","evidence", "by_version_sut":{...}}`。

**`case_run`**
`id BIGINT`、`wave_id BIGINT NOT NULL REFERENCES run_wave(id)`、`intent_id BIGINT NOT NULL REFERENCES execution_intent(id)`、`case_version_id BIGINT NOT NULL`、`status TEXT NOT NULL DEFAULT 'pending'`（CHECK `pending/running/paused/passed/failed/blocked/inconclusive/skipped/interrupted/superseded`）、`attempts INT NOT NULL DEFAULT 0`（CHECK ≥0）、`failure_class TEXT NOT NULL DEFAULT ''`（CHECK `''/application/environment/session/model/automation/timeout/executor_lost/blocked/unknown`）、`summary JSONB NOT NULL DEFAULT '{}'`、`started_at/finished_at TIMESTAMPTZ NULL`、`owner_instance TEXT NOT NULL DEFAULT ''`、`owner_executor_id BIGINT NULL`、`dispatch_epoch BIGINT NOT NULL DEFAULT 0`（CHECK ≥0）、`dispatch_token TEXT NOT NULL DEFAULT ''`（≤128）、`lease_until TIMESTAMPTZ NULL`、`paused_at TIMESTAMPTZ NULL`、`paused_by BIGINT NULL`、`pause_reason TEXT NOT NULL DEFAULT ''`、`created_at/updated_at`。
约束：`pk_case_run`；`uk_case_run_wave_intent UNIQUE(wave_id,intent_id)`；`idx_case_run_wave(wave_id,status,id)`、`idx_case_run_intent(intent_id)`、`idx_case_run_case_version(case_version_id)`、`idx_case_run_lease(status,lease_until)`。
`summary` 结构：`{"verdict_reason","assertion_total","assertion_failed","step_total","step_failed","evidence_count","human_request_id"?}`。

**`case_run_sut_context`**
`id BIGINT`、`case_run_id BIGINT NOT NULL REFERENCES case_run(id)`、`version_sut_id BIGINT NOT NULL`、`account_role TEXT NOT NULL DEFAULT ''`（≤100）、`account_id BIGINT NULL`、`browser_context_id TEXT NOT NULL DEFAULT ''`（≤128）、`storage_state JSONB NULL`（敏感；API 永不返回）、`lease_epoch BIGINT NOT NULL DEFAULT 0`、`status TEXT NOT NULL DEFAULT 'creating'`（CHECK `creating/active/closed/failed`）、`closed_at TIMESTAMPTZ NULL`、`created_at/updated_at`。
约束：`pk_case_run_sut_context`；`uk_case_run_sut_context UNIQUE(case_run_id,version_sut_id,account_role)`；`idx_case_run_sut_context_run(case_run_id)`、`idx_case_run_sut_context_context(browser_context_id)`。

### 3.2 `execution_0002_result_evidence.sql`（上下文：execution，4 表）

**`step_result`**
`id BIGINT`、`case_run_id BIGINT NOT NULL REFERENCES case_run(id)`、`seq INT NOT NULL`（CHECK >0）、`target_version_sut_id BIGINT NULL`、`status TEXT NOT NULL DEFAULT 'pending'`（CHECK `pending/running/passed/failed/blocked/skipped`）、`observed JSONB NOT NULL DEFAULT '{}'`、`error TEXT NOT NULL DEFAULT ''`（≤2000）、`started_at/finished_at TIMESTAMPTZ NULL`、`created_at/updated_at`。
约束：`pk_step_result`；`uk_step_result_run_seq UNIQUE(case_run_id,seq)`；`idx_step_result_run(case_run_id,seq)`。

**`assertion_result`**
`id BIGINT`、`case_run_id BIGINT NOT NULL REFERENCES case_run(id)`、`step_seq INT NOT NULL`（CHECK >0）、`assertion_seq INT NOT NULL`（CHECK >0；同 step 内 1-based 序号，按 `case_assertion.id` 升序派生）、`kind TEXT NOT NULL`（CHECK `ui/api/db/text/visual`）、`verdict TEXT NOT NULL`（CHECK `passed/failed/inconclusive/skipped`）、`reason TEXT NOT NULL DEFAULT ''`（≤2000）、`expected TEXT NOT NULL DEFAULT ''`（≤4000）、`actual TEXT NOT NULL DEFAULT ''`（≤4000）、`target_version_sut_id BIGINT NULL`、`created_at/updated_at`。
约束：`pk_assertion_result`；`uk_assertion_result_run_step_assertion UNIQUE(case_run_id,step_seq,assertion_seq)`；`idx_assertion_result_run(case_run_id,step_seq,assertion_seq)`。

**`evidence`**（不可变；无更新/删除 API）
`id BIGINT`、`case_run_id BIGINT NOT NULL REFERENCES case_run(id)`、`version_sut_id BIGINT NULL`、`step_seq INT NULL`（CHECK >0 OR NULL）、`assertion_seq INT NULL`、`kind TEXT NOT NULL`（CHECK `screenshot/snapshot/console/network/http/db/download/text`）、`content_ref_id BIGINT NOT NULL`（P0 `content_ref.id`，逻辑引用）、`sha256 TEXT NOT NULL`（64 hex）、`size_bytes BIGINT NOT NULL`（CHECK ≥0）、`mime TEXT NOT NULL DEFAULT 'application/octet-stream'`（≤128）、`filename TEXT NOT NULL DEFAULT ''`（≤255）、`created_by_run_id BIGINT NULL`、`created_at TIMESTAMPTZ NOT NULL DEFAULT now()`。
约束：`pk_evidence`；`idx_evidence_case_run(case_run_id,id DESC)`、`idx_evidence_version_sut(version_sut_id,id DESC)`、`idx_evidence_content_ref(content_ref_id)`、`idx_evidence_kind(kind)`。
`updated_at` 不设（不可变；审计列例外记录：只 `created_at`）。

**`evidence_ref`**
`id BIGINT`、`evidence_id BIGINT NOT NULL REFERENCES evidence(id)`、`owner_kind TEXT NOT NULL`（CHECK `case_run/step_result/assertion_result/human_request`）、`owner_id BIGINT NOT NULL`、`role TEXT NOT NULL DEFAULT 'attachment'`（≤64）、`created_at TIMESTAMPTZ NOT NULL DEFAULT now()`。
约束：`pk_evidence_ref`；`uk_evidence_ref_owner UNIQUE(evidence_id,owner_kind,owner_id,role)`；`idx_evidence_ref_owner(owner_kind,owner_id)`。

### 3.3 `execution_0003_executor_human.sql`（上下文：execution，2 表）

**`executor_instance`**
`id BIGINT`、`name TEXT NOT NULL`（≤200）、`endpoint TEXT NOT NULL`（≤500；唯一）、`token_hash TEXT NOT NULL DEFAULT ''`（64 hex）、`capacity INT NOT NULL DEFAULT 0`（CHECK ≥0；浏览器上下文上限）、`active_contexts INT NOT NULL DEFAULT 0`（CHECK ≥0）、`labels JSONB NOT NULL DEFAULT '{}'`、`status TEXT NOT NULL DEFAULT 'offline'`（CHECK `active/draining/offline`）、`last_heartbeat TIMESTAMPTZ NULL`、`version TEXT NOT NULL DEFAULT ''`（≤64）、`last_error TEXT NOT NULL DEFAULT ''`（≤2000）、`created_at/updated_at`。
约束：`pk_executor_instance`；`uk_executor_instance_endpoint UNIQUE(endpoint)`；`idx_executor_instance_status(status,last_heartbeat)`。

**`human_request`**（版本域；migration 归 execution，`P3-D-16`）
`id BIGINT`、`version_id BIGINT NOT NULL`、`case_run_id BIGINT NULL`、`kind TEXT NOT NULL`（CHECK `login_takeover/conflict/other`）、`payload JSONB NOT NULL DEFAULT '{}'`、`status TEXT NOT NULL DEFAULT 'open'`（CHECK `open/resolved/cancelled/expired`）、`response JSONB NOT NULL DEFAULT '{}'`、`expires_at TIMESTAMPTZ NOT NULL`、`resolved_by BIGINT NULL`、`resolved_at TIMESTAMPTZ NULL`、`created_by BIGINT NULL`、`created_at/updated_at`。
约束：`pk_human_request`；`idx_human_request_version_status(version_id,status,id DESC)`、`idx_human_request_case_run(case_run_id,status)`、`idx_human_request_expiry(status,expires_at)`。
`payload` 结构（P3 只创建 `login_takeover`）：`{"reason","version_sut_id","account_role","page_url","challenge_kind","evidence_id"?}`；`response` 结构：`{"code"?,"note"?}`（≤2000）。

### 3.4 迁移顺序与不变量

`order.txt` 追加：

```text
execution_0001_intent_wave.sql
execution_0002_result_evidence.sql
execution_0003_executor_human.sql
```

迁移测试计数 17→20（`backend/internal/db/migrate_test.go`、`migrate_integration_test.go`）。重复执行 no-op；checksum 不变；不改历史文件。**P3 不新增 FK 到跨上下文表**；`case_version_id`/`version_sut_id`/`owner_executor_id`/`content_ref_id` 均为逻辑引用。

**核心不变量**
1. 执行只认 `case_entity.current_version_id = case_version.id AND case_version.status='confirmed'`；stale/superseded/draft 不执行。
2. 同版本单活跃波（`planned/running/paused`）；`paused` 不释放活跃语义。
3. 同 `(case_version, environment_fingerprint, code_baseline, purpose)` 只允许一个 active intent；重跑/复测新建 intent，旧 intent 保留。
4. 一个 `(wave_id,intent_id)` 只有一个 case_run；重复调度不新建。
5. 暂停/终态后清理执行器上下文；未 cleanup 不释放配额。
6. 证据不可变；`sha256/size` 与内容账本一致；API 不返回 `storage_state`/路径/凭据。
7. 执行域一律 `version_sut_id`；页面/API/账号/DB 归属校验；`sut_id` 只展示。

## 4. 领域状态机与判定

### 4.1 ExecutionIntent

`open → claimed → running → done | failed | cancelled | superseded`；`open/claimed/running` 为 active（partial unique）。
- `claimed`：wave 创建事务内，为 intent 建 case_run 并置 claimed。
- `running`：首个 case_run 进入 running。
- `done`：case_run 到达四态终态（passed/failed/blocked/inconclusive）或 `interrupted` 后确定不再重派。
- `failed`：执行前基础设施失败（无法创建 context/账号解析失败且不重试）。
- `cancelled`：用户取消 intent/波次（未执行或主动停止）。
- `superseded`：用例不再 current+confirmed（stale/superseded/改版）。
- 禁止 `claim` 非 open；禁止终态回退；重复取消幂等返回当前状态。

### 4.2 RunWave

`planned → running ↔ paused → done | failed | cancelled`。
- `planned`：创建事务插入，尚未派发任何 case_run。
- `running`：首个 case_run 进入 running（或 planned 有空 case_run 立即置 running）。
- `paused`：`pause` API 或 human_request；`paused` 占单活跃波；调度停止新 case_run；`graceful` 允许在飞完成，`immediate` 取消在飞。
- `done`：所有 case_run 均非 `pending/running/paused`，且无待建 case_run；波次结果不影响 done（失败也 done）。
- `failed`：wave 级基础设施失败（无法创建任何 case_run/调度器内部错误）；P3 仅用于“所有 intent 都因基础设施失败且无法重试”。
- `cancelled`：用户取消波次；未终态 case_run → `superseded`（人工取消语义）。
- 终态不可回退；恢复只对 paused。

### 4.3 CaseRun 与四态聚合

`pending → running ↔ paused → passed | failed | blocked | inconclusive`；过程终态 `skipped/interrupted/superseded`。
- `pending`：已建 case_run，等待配额/执行器/暂停恢复。
- `running`：有活跃 agent run + executor lease；`lease_until` 由 runner 续租。
- `paused`：`pause`/human；保存已完成 step/assertion，恢复从首个未通过步骤继续。
- `interrupted`：执行器 offline/租约过期/agent 中止；可重派（`interrupted → pending`，attempts+1，epoch+1）或耗尽后 `failed`。
- `superseded`：用例失效/用户取消波次/用例级取消；终态。
- `skipped`：预留（P3 不主动产生；visual 断言只让断言 skipped）。
- **四态聚合**（服务端计算，模型建议需校验）：
  1. 任一 step `failed`（工具/环境/自动化错误）→ case_run `failed`，class 按错误映射。
  2. 任一 assertion `failed` → case_run `failed`，class=`application`（UI/API/text/db 实际不符）或 `automation`（target 不存在/歧义/步骤数据非法）。
  3. 全部必需 step 通过、全部 assertion `passed` → `passed`。
  4. 存在未执行的必需 step/assertion 且无失败 → `inconclusive`（`summary.verdict_reason=incomplete`）。
  5. 无法开始（缺账号/DB/页面/环境禁用、目标不属于 version_sut）→ `blocked`，class=`blocked`。
  6. 有 visual 断言且未执行 → 该断言 `skipped`；若其余全通过，case_run 仍可 `passed`，但 `summary.visual_skipped=1`（P5 补）。
  7. Agent run 因 model/budget/context 失败 → `failed`，class=`model`；`context_overflow` 不算失败类，按 P1 压缩/重试。
  8. 用例无 step 或 step 全无断言 → `inconclusive`。
- **禁止**模型直接改 case_run 终态；`execution.case_finish` 只提交候选，服务端按上述规则覆写不一致结果并返回结构化说明。

### 4.4 StepResult / AssertionResult

- `step_result.status`：`pending/running/passed/failed/blocked/skipped`；`observed` 为 `{summary?, output?}`；`error` 限量。
- `assertion_result.verdict`：`passed/failed/inconclusive/skipped`；`expected` 由服务端用 P2 `case_assertion.expected` 覆写（模型不得伪造）；`actual` 由模型提交；`kind` 取自 P2。
- 唯一键：step `(case_run_id,seq)`；assertion `(case_run_id,step_seq,assertion_seq)`；重复写入 upsert（幂等），迟到 epoch 拒绝。
- 断言序号派生：P2 `case_assertion` 无序号列，按 `(case_version_id, step_seq, id ASC)` 计算 1-based `assertion_seq`；P3 不改 P2 表（记录为 P3-D-06）。

### 4.5 failure_class 分类

| class | 判定 | 四态 | 问题候选 |
|---|---|---|---|
| `application` | assertion UI/API/text/db 实际值与期望不符（应用缺陷） | failed | **是** |
| `environment` | SUT 不可达/DNS/网络错误/浏览器崩溃/DB 连接失败 | failed | 否 |
| `session` | 登录失败/2FA/验证码/凭据缺失/授权失败 | failed | 否 |
| `model` | LLM 不可用/超时/预算耗尽/输出非法导致无法执行 | failed | 否 |
| `automation` | target_not_found/target_ambiguous/步骤/数据非法/脚本错误 | failed | 否 |
| `timeout` | 超过 `execution.case_timeout_seconds` 且无更具体归因 | failed | 否 |
| `executor_lost` | 执行器 offline/心跳丢失/租约过期 | interrupted（可重派） | 否 |
| `blocked` | 缺绑定/前置不满足，无法开始 | blocked | 否 |
| `unknown` | 兜底 | failed | 否 |

- 只有 `failed(application)` 生成问题候选（`execution.failure_candidate` + 帖）；P4 建 issue。
- `interrupted/skipped` 不是 failure_class；`interrupted` 时 `failure_class=executor_lost`。
- `failed(application)` 必须有证据（至少一条 assertion_result 或 step observed）；无证据不得判 failed(application)，降为 `inconclusive`。

### 4.6 Evidence

- 不可变；`kind` 八类；每 `case_run` 上限 100 条（超出丢弃并记 `evidence_skipped`，不失败）；单条 ≤ `execution.evidence_max_bytes`（5 MiB）；超过不落库，工具返回 `evidence_too_large`，case 继续。
- 内容账本：`storage.Upload` → `storage.AddRef(owner_kind='evidence', owner_id=evidenceID, role=kind)` → 插入 `evidence`（先分配雪花 ID、Upload、AddRef、再插行；失败留 orphan ref 由 P7 GC，与 P1 附件一致）。
- 自动取证：`browser.screenshot` 自动落证据；`browser.console/network`、`http.request`、`db.query` 自动落对应 kind；case_run 进入 failed/blocked/inconclusive 时，若 execution 仍有活跃 context，自动截图一次（best-effort）。
- 下载：`GET /api/versions/{id}/evidence/{evidenceId}/content`；校验 `case_run→wave→version` 归属、sha256/size 与账本一致；`Content-Disposition` 使用清洗后的 filename；不暴露路径。

### 4.7 HumanRequest

- 状态：`open → resolved | cancelled | expired`；不可回退。
- `open` 时对应 case_run/wave `paused`，work_item `waiting_human`；收尾谓词（P4）读 open human_request。
- resolve：任意登录用户；只允许 `open`；写 response/resolved_by/at；`login_takeover` 响应 `{code?,note?}`；触发恢复：case_run `pending`（attempts 不变、epoch+1）、wave `running`（若 pause_reason=waiting_human；若用户手动暂停则保持 paused）、新建 `execute.case` work item（epoch 后缀）。
- 超时：reconciler 将过期 open → `expired`；case_run `failed`、`failure_class=session`、`summary.verdict_reason=human_timeout`；wave 继续（若还有其他 case）。
- 取消：用户可取消 open human_request（视为放弃该用例）；case_run `failed(session)`。
- P3 不创建 `conflict/other`（保留 kind 枚举；P4/P6 可用）。

### 4.8 ExecutorInstance

- `active`：轮询健康且未摘流；可派发。
- `draining`：管理员摘流；不再派发新 case_run；在飞继续。
- `offline`：心跳超时/健康失败；其 running case_runs 由 reconciler 处理。
- 注册/心跳（pull）：每 `execution.executor_heartbeat_seconds` 调 `GET {execution.executor_url}/healthz`（Bearer token）；成功 upsert/更新（endpoint 唯一、name=host:port、capacity=maxContexts、active_contexts=contexts、version、last_heartbeat=now、last_error=''）；`status` 从 offline→active（draining 保持）；失败且 `now-last_heartbeat > execution.executor_offline_seconds` → offline，last_error 记录。
- P3 只有单 endpoint（settings 单 URL）；`executor_instance` 表为 P7 多执行器预留；调度只用当前 endpoint 的 active 行。

### 4.9 退出执行集（stale/superseded）

reconciler 每 tick：
1. 对 `open/claimed/running` intent，若其 case_version 不是 `current+confirmed` → intent `superseded`（open/claimed）或 `running→superseded`（终态前）；其非终态 case_run → `superseded`（取消 executor context，释放配额）；execute.case work item → `done`（last_error='superseded'，不发失败事件）。
2. 不再生成该 case_version 的新 intent。
3. 已生成的 `design.gap` 任务保留。
4. 需求回退/用例改版走 P2 stale 事件 + 本规则；不依赖事件顺序（DB 真值）。

## 5. 执行规划与调度

### 5.1 Intent 键与指纹算法

- `version_sut_ids`：用例所有 step 的 resolved target（`step.target_version_sut_id`，空继承 `case_version.default_version_sut_id`）+ `case_version.default_version_sut_id`，去重排序。
- `environment_fingerprint = sha256(canonical_json({version_suts:[{version_sut_id, sut_id, environment_id, base_url, page_ids, api_ids, database_ids, account_roles}]}))`（按 version_sut_id 升序；数组升序；UTF-8、无空格；只取 active 绑定）。环境/页面/API/DB/账号角色变化 → 新指纹 → 新 intent（旧 done intent 保留）。
- `code_baseline`：无代码仓绑定时 `""`；有则 `sha256(canonical_json([{repo_id, branch}]))` 的 64 hex（P3 无 commit 数据；P5 codeintel 接入后改为 commit SHA，届时走契约增量）。`branch` 空串表示默认分支。
- `purpose`：`initial`（reconciler 自动创建）/`retest`（对失败/未通过用例重跑）/`manual`（用户主动执行）。
- `dedupe_key = "exec:"+sha256(case_version_id|fingerprint|code_baseline|purpose)[:32]`；用于 work_item/日志关联，不承担唯一约束。
- `attempt_no`：同键历史 intent 数+1（仅展示；唯一由 partial unique 保证）。
- Reconciler 生成规则：对每个 version（`running` 或含 active 内容的 `initial`），列出 `current+confirmed` case_versions；若 **该 `(case_version, fingerprint, code_baseline, purpose=initial)` 组合不存在任何 intent（任意状态）** 且不存在 active intent → 创建 `open` intent。**重复扫描/重复确认/API 重启幂等**。`retest/manual` 只由 API 创建。
  - 说明：P3 用 `purpose` 区分；同一 case_version 的 `initial` 一旦创建（无论 done/failed/cancelled/superseded）不会自动再跑；失败后的再次执行必须显式 `retest/manual`。需求/用例更新产生新 case_version（新 ID）或环境变化产生新 fingerprint，自然生成新 initial intent。

### 5.2 Wave 创建与认领

- 调度 tick（1s，`ExecutionReconciler`，单实例）：
  1. 对每个 version 有 open intents 且无 active wave：事务内创建 `run_wave(status=planned)`（trigger 按 intent purpose：全 initial→auto；含 manual/retest→manual/retest（取优先级 retest>manual>auto）），为选中 intents 创建 `case_run(pending)`（唯一 `(wave,intent)`），intent→claimed。选中范围：该 version 当前所有 open intents（一次组波；新确认进下一波）。
  2. 若 wave 存在且有 pending case_run：尝试派发（§5.3）。
  3. 若 wave 所有 case_run 非 pending/running/paused 且无未建 case_run：计算 counts、wave→done（或 cancelled）、intent→done/failed/superseded；确保 `analyze.failure`/`analyze.coverage` 任务；ensure `design.gap`（确定性兜底）。
  4. 恢复：`planned` 波若已有 running case → running；无 case_run（异常）→ failed。
- 单活跃波由 partial unique 兜底；并发 tick 用 `pg_advisory_xact_lock(hash(version_id))` 或 `SELECT ... FOR UPDATE` 防双建。
- `run_wave.trigger` 不影响调度；`auto` 无人工。

### 5.3 配额与调度

- 全局并发 = 正在 `running` 的 case_run 数（不含 paused/pending）≤ `execution.global_concurrency`（默认 3）。
- 每 `version_sut` 并发 = 正在 running 且使用该 version_sut 的 case_run 数 ≤ `execution.per_sut_concurrency`（默认 2）；跨系统用例对每个使用的 version_sut 各计 1。
- 跨系统并发 = 正在 running 且 `version_sut_ids.len>1` 的 case_run 数 ≤ `execution.cross_system_concurrency`（默认 1，全局）。
- 执行器容量 = `active_contexts + 本 case 所需 context 数 ≤ capacity`；所需 context 数 = `(version_sut, account_role)` 组合数（role 空也计 1）。
- **一次性预留**：派发前在内存/DB 事务内检查并预留所有所需 version_sut slots + 全局 slot + executor contexts；不满足则跳过该 case_run（不阻塞同波其他可执行 case）。跨系统用例不得部分占用。
- 选择顺序：`case_run.id ASC`（先确认先执行）；跳过不满足配额的项继续尝试后续，避免队头阻塞。
- 释放：case_run 终态/中断且 executor context cleanup 完成后释放；失败/超时先 cleanup 再释放。
- 配额口径冻结进 handoff：全局 3（case_run 数）、每 version_sut 2（case_run 数）、跨系统 1（全局并发 case 数）。

### 5.4 派发、epoch/token 与迟到写

- 派发 case_run：选中执行器（P3 单 endpoint active 行）→ `attempts++`、`dispatch_epoch++`、生成 `dispatch_token`（32 hex）→ status `running`、lease_until=now+30s、owner_instance=API 实例、owner_executor_id → 建 `execute.case` work item，dedupe `execute:{intent_id}:{dispatch_epoch}`，payload `{intent_id,wave_id,case_run_id,case_version_id,dispatch_epoch,dispatch_token}`。
- Case Executor runner 在每次写 `step_result/assertion_result/evidence/case_run` 时，以 run scope 携带的 `dispatch_epoch+dispatch_token` 做 CAS；不匹配 → `409 lease_lost`，不覆盖。
- runner 心跳每 10s 续 `case_run.lease_until` 与 work item lease；lease 过期由 reconciler 回收。
- 旧 epoch 写入拒绝测试必须覆盖（延迟写入失败）。

### 5.5 执行器 offline/重派

- reconciler 发现 executor_instance.offline 或 case_run.lease_until < now（running/paused 除外）：
  - case_run → `interrupted`，`failure_class=executor_lost`，summary.reason=`executor_lost|lease_expired`；取消 API 内 runner（若有）；关闭 executor context（best-effort）。
  - 若 `attempts < execution max（3）` 且 intent 仍 active → 同 case_run 置 `pending`，`dispatch_epoch++`、清 token/lease；新 `execute.case` 在下次 tick 派发；保留已落 step/assertion（续跑）。
  - 否则 case_run `failed`，class=`environment`，intent `failed`；wave 继续。
- **不批量中断 running**：API 重启只恢复过期租约；不因新实例启动把所有 running 标 interrupted（P1 规则）。
- 旧 agent run 完成时若 epoch 不匹配，写入被拒并记审计。

### 5.6 counts

`run_wave.counts` 由 SQL 投影重算（不依赖内存）：
```json
{
  "total": 5,
  "pending": 1, "running": 1, "paused": 0,
  "passed": 2, "failed": 1, "blocked": 0, "inconclusive": 0,
  "skipped": 0, "interrupted": 0, "superseded": 0,
  "evidence": 7,
  "by_version_sut": {
    "5001": {"total": 4, "passed": 2, "failed": 1, "blocked": 0, "inconclusive": 0},
    "5002": {"total": 1, "passed": 0, "failed": 1, "blocked": 0, "inconclusive": 0}
  }
}
```
- 跨系统用例在其每个 version_sut 下计数；`total` 按 case_run 去重计数（非 SUT 求和）。
- 每次 case_run/evidence 变化后更新；wave 终态最终重算。

### 5.7 暂停/恢复

- **波次暂停** `POST /waves/{id}/pause {mode:"graceful"|"immediate", reason?}`：
  - graceful（默认）：wave→paused；不再派发 pending；在飞 case_run 允许跑完；finish 后 wave 保持 paused；恢复→running 再派发。
  - immediate：wave→paused；对 running case_run 调 executor `/cancel`、取消 agent run，case_run→paused（保留已完成结果）；pending 保持 pending；配额释放（cleanup 后）。
  - 幂等：已 paused 返回当前（200），不重复事件。
- **波次恢复** `POST /waves/{id}/resume`：paused→running；paused case_run→pending（attempts 不变、epoch+1）；继续派发；若 open human_request 仍存在 → 拒绝 `state_conflict`（先 resolve）。
- **用例级暂停** `POST /case-runs/{id}/pause {mode}`：pending→paused（不派发）；running→立即取消并 paused；完成结果保留。`resume`→pending（epoch+1）或由 wave resume 统一恢复。用例级暂停不改变 wave 状态，但该 case 占用配额直到 cleanup。
- 暂停不是版本状态；版本头仍显示 running（D31）。
- human_request：wave pause_reason=`waiting_human`；resolve 后仅当 pause_reason=waiting_human 才自动 running；用户手动暂停优先。

### 5.8 取消与重跑

- **intent 取消** `POST /execution/intents/{id}/cancel`：open→cancelled；claimed/running→cancelled + wave 内对应非终态 case_run→superseded；不删除历史。已终态返回幂等 200。
- **波次取消** `POST /waves/{id}/cancel`：planned/running/paused→cancelled；非终态 case_run→superseded；active intents→cancelled（除非已 superseded）；清理 context；不产生 analyze 任务（取消不算失败分析），但发 `wave.finished` status=cancelled。
- **重跑/复测** `POST /execution/intents {case_version_id, purpose:"retest"|"manual"}`：校验 current+confirmed 且属于该 version；创建 open intent（同键若无 active；若同键 active → 409 `state_conflict`）；retest 建议仅对存在失败结果时允许（否则 400）；manual 任意当前 confirmed 可用。新 intent 进入下一波。
- **用户重跑 UI**：结果行“重跑”调用该 API，purpose=retest。

### 5.9 版本重开

- 创建 intent（任何 purpose）时，若 `version.status=completed`：调用 `versionapp.Reopen`（清 `closed_at`、`close_reason`，发 `version.status_changed completed→running`，审计）。P4 负责收尾；P3 不实现 close。
- 若 version=initial：由已有 P1 投影在确认/需求时置 running；P3 不重复。执行不读 version.status 作为门禁（D05）。

## 6. 执行器集成与多 SUT 运行时

### 6.1 执行器客户端与协议

- 新 `execution/infrastructure/executor` HTTP 客户端：`GET /healthz`（无 auth）、`POST /v1/contexts`、`POST /v1/contexts/{id}/actions`、`POST /v1/contexts/{id}/cancel`、`DELETE /v1/contexts/{id}`、新增 `GET /v1/contexts`（P3 扩展，返回 `{contexts:[{id,key,createdAt,metadata}]}`）。
- 鉴权：`Authorization: Bearer <execution.executor_token>`；healthz 不需要；不得把 token 写入日志/事件/证据。
- 请求体 ≤1MB、action 15s（max 60s，goto 30s）、artifact ≤10MB、snapshot ≤12KB、maxContexts 由执行器配置（默认 8）。
- 上下文创建：`allowedHosts=[env.base_url host]`（demo 可加 `127.0.0.1`）；`baseUrl=env.base_url`；`storageState` 可选；`metadata={case_run_id, version_sut_id, account_role, dispatch_epoch, dispatch_token}`；`key="run:{case_run_id}:vs:{version_sut_id}:role:{role}"`。
- 多 SUT：每个 `(version_sut_id, account_role)` 一个 context；同一 case_run 串行按步骤执行；context 复用；终态全部 DELETE。
- 执行器扩展（`executor/src`）：
  - `POST /v1/contexts` 接受并保存 `metadata`（object，≤2KB）；`GET /v1/contexts` 返回 id/key/metadata/createdAt；health 增加 `activeContexts`（或沿用 contexts）。
  - 不改动作语义；新增 `GET /v1/contexts` 测试。
- `contracts/executor-protocol.md` 冻结现有协议 + P3 扩展 + token/注册（pull）/容量/排空/离线/多 SUT 映射/证据交接/错误码。

### 6.2 注册/心跳/容量/排空

- 轮询器（`cmd/eztest` 注入，`ExecutionReconciler` 内或独立 1s tick）：
  - 读 `execution.executor_url` + `execution.executor_token`；每 `heartbeat_seconds` 调 healthz；upsert endpoint 行（token_hash=sha256(token)、capacity=maxContexts、active_contexts=contexts、version、last_heartbeat、status 保持 draining 否则 active）。
  - 失败：若已有行且超过 `offline_seconds` → offline；无行 → 不建（未注册）；healthz 503 degraded 仍记录 last_heartbeat 但 status=active（degraded 只影响告警，不在 P3 阻塞派发；执行前再 healthz 失败则 case_run interrupted）。
- 管理员：`GET /api/admin/executors`、`POST /api/admin/executors/{id}/drain|activate`；drain 不改执行器进程，只停止新派发；在飞继续。
- 单实例：调度只选 endpoint 的 active 行；offline 时 pending 等待恢复，running 中断重派。

### 6.3 Case Executor 运行循环（API 进程内 Agent）

1. Worker 领取 `execute.case` → 校验 work item lease/epoch → 加载 case_run/intent/case_version（只读 current+confirmed）→ 若失效 → case_run superseded/work item done。
2. 解析目标：每个 step resolved `target_version_sut_id`（空继承默认）、page/api/database/account role；校验属于 version_sut；构建 SUT contexts（不实际创建 browser context，懒创建于首个 browser 工具）。
3. 构建任务上下文（§7.4）：用例标题/描述/前置/测试数据、步骤（seq/action/expected/target）、断言（kind/expected/target）、版本绑定、账号角色（不注入密码）、目标库 schema 摘要（可选）、已完成的 step/assertion 结果（续跑）、human 响应（resume 时）。
4. 运行 Agent（profile `case_executor`）；工具调用写结果/证据；每次写校验 epoch/token。
5. Agent 结束/超时/错误：服务端按 §4.3 聚合终态；若 `human.request` 触发 → 暂停/等待（不是失败）；否则写 case_run 终态、intent done/failed、清理 contexts、释放配额、更新 counts、发事件。
6. 失败自动取证：终态 failed/blocked/inconclusive 时对仍有活跃 context 的 SUT 截图一次。
7. 结果写回使用 case_run + epoch/token CAS；迟到写 409。

### 6.4 证据采集（API 侧）

- 执行器 artifact base64 → 解码后 API 侧重算 sha256（不信任执行器自报）→ 校验 ≤ `execution.evidence_max_bytes` → `storage.Upload` → `AddRef` → 插入 evidence + evidence_ref（case_run/step/assertion）→ 发 `evidence.created`。
- 证据 kind 映射：screenshot→`screenshot`；snapshot/console/network→对应；http.request→`http`；db.query→`db`；download→`download`；execution.evidence text→`text`。
- 每 case_run 上限 100；超出记 `summary.evidence_skipped`，不失败。
- 证据不写 `paths.evidence_dir`；handoff 记录例外。

### 6.5 DB 断言

- 目标库解析：case_assertion.database_id（P2 允许）或 version_sut 绑定的首个 active 数据库；必须属于该 version_sut 的 SUT+环境；kind 必须是 postgres。
- 凭据：新增 `resourceapp.SecretReader`（application 方法）`GetAccountSecret(accountID)`/`GetDatabaseSecret(databaseID)`；execution 适配器先经 version `Get(versionID)` 解析 version_sut→account/db ID，再取明文；仅内存使用，不写日志/事件/证据。
- `db.query` 工具：只允许单条 SELECT/WITH（大小写不敏感前缀校验 + 禁止分号多语句）；`BEGIN TRANSACTION READ ONLY`；`SET LOCAL statement_timeout='5s'`；行数 ≤200、结果 ≤64KB；失败映射 environment（连接）/automation（SQL 非法）；自动落 db 证据（SQL + 截断结果，密码不出现）。
- `database.schema_read`：返回 tables/columns 摘要（≤64KB），只读。

### 6.6 HTTP 请求

- `http.request`：base URL 由 version_sut 环境；如给 `api_id`，校验属于该 version_sut 且用 `method/path_template`；只允许该环境 host（防 SSRF）；超时 15s；响应 ≤64KB 落证据；请求/响应头中的 `authorization/cookie/set-cookie/x-api-key` 脱敏；状态码/耗时/摘要作为工具结果。
- 用途：API 断言、跨系统数据准备/校验；不替代浏览器导航。

### 6.7 账号与浏览器登录

- `account.credentials {version_sut_id?, role}`：经 version binding 解析 `(version_sut, role)` → test_account；返回 username/password（审计 `execution.credentials_read`）；不返回给 UI；role 精确匹配 `test_account.roles`（空 role 取该 version_sut 第一个 active 账号；多账号同 role → 409 配置错误）。
- Browser context 不带 storageState（P3 用 Agent 登录）；`browser.fill(secret=true)` 由执行器擦除 secret；密码不得进入证据（console/network/snapshot 已由执行器 scrub；API 侧再过滤）。
- 登录步骤由 Case Author 写明（P2）；Case Executor 按步骤执行；遇到 2FA/验证码 → `human.request`。

### 6.8 登录接管（human_request）

1. Agent 调 `human.request {kind:"login_takeover", reason, version_sut_id?, account_role?, challenge_kind?}`。
2. 服务端：校验 scope；创建 `human_request(open, expires_at=now+human_request_timeout_seconds)`；case_run→paused（reason=waiting_human）；wave→paused（pause_mode=graceful，pause_reason=waiting_human）；当前 agent run 标记 `waiting_human` 结束（work item→waiting_human）；发 `human_request.created`；工具返回 `{human_request_id,status:"open"}` 并提示停止。
3. UI 执行 Tab 顶部 HumanRequestCard 显示原因/系统/账号角色/操作指引/到期时间；用户输入验证码/备注 → resolve。
4. resolve（任意登录用户）：status→resolved、response、resolved_by/at；发 `human_request.resolved`；旧 `waiting_human` work item → `done`（last_error=`waiting_human_resumed`，不计失败）；case_run→pending、`dispatch_epoch+1`；wave→running（若 pause_reason=waiting_human 且无其他 open human）；调度器以新 epoch 创建新 `execute.case` work item；Agent 上下文带 `human_response`（一次性，敏感，不落事件）。
5. 恢复执行：从首个未通过 step 继续；登录步骤重试并把 code 填入验证码输入框；若仍失败可再次请求（同一 case 最多 3 次 human_request，超过→failed(session)）。
6. 超时：expired；case_run failed(session)，summary.reason=human_timeout；wave 继续。
7. 用户取消：cancelled；case_run failed(session)，reason=human_cancelled。

### 6.9 上下文生命周期- 创建 context 懒执行（首个 browser 工具）；记录 `browser_context_id` 到 `case_run_sut_context`（upsert active）。
- 清理：case_run 终态/取消/暂停 immediate → DELETE 每个 context（best-effort，404 忽略）；关闭后 status=closed。
- TTL：reconciler 对 `created_at < now - execution.executor_context_ttl_seconds` 且 case_run 非 running 的 context 执行 DELETE；对 case_run running 但 context 超 TTL 的不清理（避免打断）。
- 孤儿：`GET /v1/contexts` 与 DB 对账，删除 DB 无记录且 key 前缀 `run:` 的 context；每 60s 一次。
- executor 重启后 context 丢失：browser 工具返回 `context_not_found` → case_run interrupted(environment)→重派（§5.5）。

### 6.10 等待与时间窗口（`execution.wait`）

- **动机**：真实需求常含异步/时间窗口（如“确认弹框 120s 后自动消失，视为未确认”“退款 30s 后到账”）。执行器单 action 上限 60s（goto 30s），无法用 `browser.wait_for` 覆盖 120s；且长时间占用浏览器 action 会阻塞同一 context 的取消/清理。
- **实现**：Agent 工具 `execution.wait {seconds:int(1..300), reason?:string(0..200)}`，在 **API 进程**内等待（`select` on timer/ctx），不调用执行器、不占用 action 时限；页面 JS 定时器在 headless 下继续运行。等待期间 runner 心跳继续续 case_run lease；返回 `{waited_seconds, deadline_at}`，不产生证据（Agent 可在等待后 `browser.snapshot`/`browser.screenshot` 取证）。
- **边界**：
  - 单次 1–300s；累计等待**计入** `execution.case_timeout_seconds`；可在 case 超时前被
    `context` 取消（暂停/取消/lease 丢失/执行器 offline）打断，返回 `lease_lost` 或 `state_conflict`；case_run 按暂停/中断状态机处理，恢复后该 step 重试（等待重跑）。
  - 若 Agent 需要“等到某条件”而非固定时长，先 `execution.wait` 再 `browser.snapshot`/`db.query` 检查；P3 不新增条件轮询工具（避免隐藏循环；后续可增量）。
  - 执行器进程不实现 sleep；`browser.wait_for` 仍只用于 ≤60s 的可见性等待。
  - 120s 窗口是验收必测（`-LongWait`）；默认快验收用 10s 窗口，长验收用 120s 窗口 + `execution.wait(122)`。
  - 若需求需要 >900s 等待，管理员调整 `execution.case_timeout_seconds`；P3 不承诺无限等待。

## 7. Agent Profile、任务、工具与上下文

### 7.1 任务类型（`task-types.md` §5，frozen P3）

| type | team | profile | dedupe_key | payload | 输出 | 失败语义 |
|---|---|---|---|---|---|---|
| `execute.case` | execution | `case_executor` | `execute:{intent_id}:{dispatch_epoch}` | `{intent_id,wave_id,case_run_id,case_version_id,dispatch_epoch,dispatch_token}` | step/assertion/evidence/case_run 终态 | 模型/工具失败按 retry；scope 失效 dead；lease/epoch 冲突 409 |
| `analyze.failure` | execution | `failure_analyst` | `failure:{wave_id}` | `{wave_id,case_run_ids[]}` | 分类建议 + 问题候选帖/事件 | 可重试；最终失败不阻塞 wave 终态（确定性兜底已写） |
| `analyze.coverage` | execution | `coverage_analyst` | `coverage:{wave_id}` | `{wave_id}` | 缺口帖/事件 + `design.gap` ensure | 同上 |

- `analyze.wave` 不注册（ARCH §5.3 为简写）；`report.wave/version`、`issue.triage`、`version.close` 不在 P3。
- 执行 Planner 是确定性 runtime（ARCH §7.1），不注册 profile。
- create 者：调度器创建 `execute.case`；wave 终态创建 analyze 任务；P2 design reconciler 消费 `design.gap`（已有）。

### 7.2 Profiles（启动幂等 seed，不覆盖管理员修改）

| key | 名称 | budgets | 工具 |
|---|---|---|---|
| `case_executor` | 用例执行器 | `{max_turns:60, soft_seconds:780, hard_seconds:900}` | 见 §7.3 执行工具 + 只读上下文/资源/证据工具 |
| `failure_analyst` | 失败分析师 | `{max_turns:6, soft_seconds:180, hard_seconds:300}` | `context.task_pack`、`context.team_room_read`、`execution.result_read`、`execution.evidence_read`、`analysis.failure_submit` |
| `coverage_analyst` | 覆盖分析师 | `{max_turns:6, soft_seconds:180, hard_seconds:300}` | `context.task_pack`、`requirement.read`、`execution.coverage_read`、`analysis.coverage_submit` |

- system prompt 必须含 `[profile:<key>]`；case_executor 明确：严格按用例步骤/断言执行；每步完成后 `execution.step_record`；每条断言后 `execution.assertion_record`；**步骤要求等待/时间窗口时调用 `execution.wait`（不要用重复 snapshot 空转，也不要假设等待后结果）**；不得改需求/用例/确认；登录失败/2FA 调 `human.request`；无法继续时 `execution.case_finish(blocked)`；不得虚构 passed。
- `case_executor` 不注册 `case.confirm`/`requirement.*` 写工具；不能关闭版本/合并问题。

### 7.3 P3 工具（注册；scope 由 runtime 注入，schema 不出现 version_id）

**执行/浏览器**
| 工具 | 参数 | 说明 |
|---|---|---|
| `browser.goto` | `{version_sut_id?,account_role?,url:string}` | 打开页面；懒建 context；host 必须属于该 SUT 环境 |
| `browser.snapshot` | `{version_sut_id?,account_role?}` | 可访问性快照（≤12KB） |
| `browser.click` | `{version_sut_id?,account_role?,target:Target}` | `Target={role?,name?,exact?}|{text?}|{test_id?}|{selector?}` 恰一种 |
| `browser.fill` | `{version_sut_id?,account_role?,target:Target,value:string,secret?:bool}` | secret 由执行器擦除 |
| `browser.press` | `{version_sut_id?,account_role?,target?:Target,key:string}` | Enter/Tab 等 |
| `browser.wait_for` | `{version_sut_id?,account_role?,target?:Target,timeout_ms?:int(100..60000)}` | 等待可见 |
| `browser.screenshot` | `{version_sut_id?,account_role?,full_page?:bool}` | 自动落 screenshot 证据 |
| `browser.console` | `{version_sut_id?,account_role?,limit?:int(1..200)}` | 自动落 console 证据 |
| `browser.network` | `{version_sut_id?,account_role?,limit?:int(1..200)}` | 自动落 network 证据 |
| `browser.download` | `{version_sut_id?,account_role?,target:Target,filename?:string}` | 自动落 download 证据 |
| `browser.storage_state` | `{version_sut_id?,account_role?}` | 导出 storageState（P3 仅返回摘要/不落库） |
| `http.request` | `{version_sut_id?,api_id?,method?,path:string,headers?:object,body?:string}` | 只允许 SUT host；自动落 http 证据 |
| `db.query` | `{version_sut_id?,database_id?,sql:string,max_rows?:int(1..200)}` | 只读 PG；自动落 db 证据 |
| `database.schema_read` | `{version_sut_id?,database_id?,schema?:string}` | 表/列摘要 |
| `account.credentials` | `{version_sut_id?,role:string}` | 返回账号密码；审计；模型用于登录 |
| `execution.step_record` | `{seq:int,status,observed?:string,error?:string,version_sut_id?}` | upsert；发 `case_run.step_finished` |
| `execution.assertion_record` | `{step_seq:int,assertion_seq:int,verdict,expected?:string,actual?:string,reason?:string,version_sut_id?}` | expected 由服务端覆写；发 `case_run.assertion_finished` |
| `execution.evidence` | `{kind:"text",text:string,step_seq?,assertion_seq?}` | 文本证据；二进制只经截图/下载 |
| `execution.wait` | `{seconds:int(1..300),reason?:string}` | API 侧可中断等待；页面定时器继续；不占执行器 action 时限；累计计入 case 超时（§6.10） |
| `execution.case_finish` | `{verdict,summary?,failure_class?}` | 候选；服务端按 §4.3 校验/覆写 |
| `human.request` | `{kind,reason,version_sut_id?,account_role?,challenge_kind?}` | 暂停/等待；不能自我 resolve |

**只读/复用**：`context.task_pack`、`context.version_pack`、`context.team_room_read`、`requirement.read`、`sut.list`、`sut.page_list`、`design.graph_read`。

**Failure/Coverage 专用**
- `execution.result_read {case_run_id?}`：wave 内结果/断言/证据摘要（只读，scope 限 wave）。
- `execution.evidence_read {evidence_id,offset?,max_chars?}`：证据元数据/文本片段（不返回执行器路径）。
- `execution.coverage_read {}`：需求/用例/执行覆盖摘要。
- `analysis.failure_submit {case_run_id,failure_class,category,severity,summary,dedupe_hint?}`：校验后更新 failure_class/summary（仅 failed(application) 产候选事件/帖）。
- `analysis.coverage_submit {requirement_id,reason,evidence_case_run_ids?}`：ensure `design.gap` + 帖/事件。

**工具通用规则**：scope 注入 `version_id/wave_id/case_run_id/intent_id/case_version_id/version_sut_ids/agent_run_id/dispatch_epoch/dispatch_token`；schema 不含 `version_id`；结果 JSON；超 `agent.tool_result_max_bytes` 落盘 text content_object 返回 head+ref；所有调用写 `agent_message`；写操作审计带 `agent_run_id`。

### 7.4 上下文与续跑

任务上下文（`context.task_pack` + 专用执行段）包含：
- 用例：title/description/preconditions/test_data/priority；steps（seq/action/expected/target_version_sut/page/api/account_role/data）；assertions（step_seq/assertion_seq/kind/expected/target）；来源引用（requirement id/version）。
- 版本绑定：SUT 名称/环境 base_url/页面/API/数据库（不含密码）；账号角色列表（不含密码）。
- 执行状态：wave/case_run 状态、attempts、已完成 step/assertion 结果（续跑）、当前待执行 step、human_response（一次性）。
- 约束：不得修改用例/需求/确认；不得访问未绑定资源；密码只经 `account.credentials` 读取；证据自动落库。
- 上下文预算：沿用 P1 `context.*`；case_executor 超长时压缩保留用例步骤/断言/已完成结果（钉住事实）；工具结果落盘；`context_overflow` 不判失败。

### 7.5 真实 LLM 验收（用户裁决 C）

- P3 验收不使用 fake LLM；`scripts/p3-acceptance.ps1` 与 `p3-e2e.ps1` 读取 `.env` 小写 `llm_*` 经 admin API 写 `llm_config`（DeepSeek `deepseek-flash`），真实驱动 Case Executor/Failure Analyst/Coverage Analyst。
- 真实失败（模型/网络/工具）→ 记录并 blocked；不用 fake 替代。
- 单元/集成测试可使用 fake `Runner`/`LLM` port（非验收）。
- 真实模型提示词需给出“逐步执行 + 每步记录 + 不虚构”的强约束；验收用例步骤保持短小确定（§13）。
- `p3-acceptance-llm.ps1` 可保留命名或合并到 `p3-acceptance.ps1`；计划采用 `p3-acceptance.ps1`（真实 LLM 主验收）+ `p3-e2e.ps1`（浏览器 UI 真实 LLM）。

## 8. 失败与覆盖分析（P3 范围，不建 P4 表）

### 8.1 确定性兜底（必须）

Wave 进入 `done`/`failed` 时（`cancelled` 不做失败/覆盖分析），服务端在事务/紧邻事务内：
1. 对每个 failed/blocked/inconclusive case_run，按 §4.5 持久化 `failure_class`/`summary`（若 Agent 尚未写入）。
2. 对每个 `failed(application)` 且无候选的 case_run，生成**问题候选**：`dedupe_hint = sha256(version_id|case_version_id|step_seq|assertion_seq|failure_class)[:32]`；`category` 取 `application|data|ui|api|db`（P3 只给建议，P4 定 enum 映射）；`severity` 启发式（failed assertion priority P0→major、P1→major、P2→minor、P3→trivial）。
3. 对每个 current clarified/closed requirement，计算执行覆盖：
   - 有 current+confirmed case_version 且其中至少一个最近 case_run `passed` → covered；
   - 有 current+confirmed case_version 且有最近 case_run，但全部最近结果 `failed/blocked/inconclusive`（无 passed）→ gap(reason=`execution_failed_coverage`)；`superseded` 不计为已执行（按 `execution_not_run` 处理）；
   - 有 current+confirmed case_version 但从未执行（无 case_run）→ gap(reason=`execution_not_run`)；
   - 无 current+confirmed case_version → 不归 P3（P2 设计覆盖负责）。
4. 对每个 gap：ensure `design.gap` work_item（P2 已冻结类型），dedupe `gap:{requirement_id}:{case_version_id|requirement_version_id}`，payload `{requirement_id,case_id?,case_version_id?,reason}`；若已存在则跳过。`feature.auto_gap_design=false` 时不自动 ensure，只发事件/帖（P3 验收默认 true）。
5. 发 `execution.failure_candidate` / `execution.coverage_gap` 事件；写 `team_room_post(team=execution, kind=finding/blocker, refs=[...])`（不含正文/证据字节/密码）。
6. 标记 wave 已分析（内存/DB 由任务 dedupe 保证；重复扫描不重复建任务）。

### 8.2 Failure Analyst（Agent，增强）

- 任务 `analyze.failure`（dedupe `failure:{wave_id}`）；profile `failure_analyst`。
- 输入：wave 内 failed case_runs、assertion/step 结果、证据元数据、用例优先级。
- 输出：对每个候选校验/细化 class/category/severity/summary；`analysis.failure_submit` 逐条提交；服务端校验：
  - 只能把 `application` 细化为 `application`，不得把非 application 升级为 application 候选（避免误报）；可把 application 降级为 automation/environment（有证据时）。
  - 只允许 wave scope 内 case_run。
  - 写 `case_run.failure_class`/`summary` + 事件/帖（失败候选再次发布）。
- 失败/超时不阻塞 wave：确定性兜底已写；任务失败按 P1 重试/dead，handoff 记录。

### 8.3 Coverage Analyst（Agent）

- 任务 `analyze.coverage`（dedupe `coverage:{wave_id}`）；profile `coverage_analyst`。
- 输入：requirements（current version）、confirmed cases、最近执行结果、design_scope。
- 输出：`analysis.coverage_submit`（requirement_id/reason/evidence_case_run_ids）；服务端校验 requirement 属于 version、reason 合法、证据 case_run 属于 wave；ensure `design.gap` + 帖/事件。
- 确定性 §8.1 已先 ensure；Agent 只补充解释/优先级，不重复创建（dedupe 保证）。

### 8.4 P4 交接

P3 向 P4 提供（handoff 明确）：
- 表/查询：`run_wave`（status/counts/trigger/时间）、`case_run`（wave/status/failure_class/summary/version_sut 上下文）、`step_result`/`assertion_result`、`evidence`/`evidence_ref`、`execution_intent`。
- 事件：`wave.*`、`case_run.*`、`step/assertion`、`evidence.created`、`execution.failure_candidate`、`execution.coverage_gap`、`design.gap`（P2）。
- 建议接口：P4 按 `(version_id, wave_id)` 聚合；issue dedupe 用 `execution.failure_candidate.dedupe_hint` + 自己的 dedupe_key；报告按 `version_sut_id` 分组 counts。
- P3 **不建** `issue`/`report`/`analysis_run`；P4 不得依赖 P3 不存在的表。

## 9. REST API（P3 增量）

### 9.1 通用约定

- 全部登录用户可读写执行操作（与版本/需求一致）；管理员额外管理执行器。
- 统一 `{data}`/`ListEnvelope{data,next_cursor,has_more}`；ID 十进制字符串；时间 RFC3339 UTC；错误信封沿用 P0；错误码只新增 `evidence_too_large`（409/413 视体积）与 `executor_unavailable`（503），其余复用 `invalid_argument/state_conflict/not_found/lease_lost/unavailable`。
- 分页：waves/case-runs/evidence/intents/human-requests 用 `before`（上一页最后 ID，倒序 id）；`limit` 默认 20 上限 100。case-run detail 一次返回 steps+assertions（≤50 step/断言上限由 P2）+ evidence 前 50 条（更多走 evidence list）。
- 幂等：pause/resume/cancel/resolve 对已处于目标状态返回 200 当前值（自然幂等）；创建 manual/retest intent 有 active 同键 → 409 `state_conflict`。
- 权限：human request resolve 任意登录用户；执行器管理 admin。

### 9.2 Paths / operationId

| Method | Path | operationId | 说明 |
|---|---|---|---|
| GET | `/api/versions/{id}/execution/overview` | `getExecutionOverview` | 活跃波摘要、counts、open human 数、executor 状态/配额；空态可用 |
| GET | `/api/versions/{id}/execution/intents` | `listExecutionIntents` | opts `status`,before,limit |
| POST | `/api/versions/{id}/execution/intents` | `createExecutionIntent` | `{case_version_id,purpose:"retest"\|"manual"}` → 201；active 冲突 409 |
| POST | `/api/versions/{id}/execution/intents/{intentId}/cancel` | `cancelExecutionIntent` | → 200 IntentView |
| GET | `/api/versions/{id}/waves` | `listRunWaves` | before,limit |
| GET | `/api/versions/{id}/waves/{waveId}` | `getRunWave` | 仅 wave/counts（case_runs 走独立分页端点） |
| POST | `/api/versions/{id}/waves/{waveId}/pause` | `pauseRunWave` | `{mode:"graceful"\|"immediate",reason?}` |
| POST | `/api/versions/{id}/waves/{waveId}/resume` | `resumeRunWave` | open human → 409 |
| POST | `/api/versions/{id}/waves/{waveId}/cancel` | `cancelRunWave` | → cancelled |
| GET | `/api/versions/{id}/waves/{waveId}/case-runs` | `listWaveCaseRuns` | before,limit,status |
| GET | `/api/versions/{id}/case-runs/{caseRunId}` | `getCaseRun` | steps/assertions/evidence 摘要 |
| POST | `/api/versions/{id}/case-runs/{caseRunId}/pause` | `pauseCaseRun` | `{mode:"immediate"}`（pending 可直接） |
| POST | `/api/versions/{id}/case-runs/{caseRunId}/resume` | `resumeCaseRun` | |
| GET | `/api/versions/{id}/evidence` | `listEvidence` | case_run_id/version_sut_id/kind,before,limit |
| GET | `/api/versions/{id}/evidence/{evidenceId}` | `getEvidence` | 元数据（sha/size/mime/kind/created_at） |
| GET | `/api/versions/{id}/evidence/{evidenceId}/content` | `downloadEvidence` | 鉴权字节；校验 sha/size；`Content-Disposition` |
| GET | `/api/versions/{id}/human-requests` | `listHumanRequests` | status=open 等 |
| POST | `/api/human-requests/{humanRequestId}/resolve` | `resolveHumanRequest` | `{response:{code?,note?}}` |
| POST | `/api/human-requests/{humanRequestId}/cancel` | `cancelHumanRequest` | |
| GET | `/api/admin/executors` | `listExecutors` | admin |
| POST | `/api/admin/executors/{executorId}/drain` | `drainExecutor` | admin；切 draining |
| POST | `/api/admin/executors/{executorId}/activate` | `activateExecutor` | admin；draining/offline→active |

P3 不实现 `/api/internal/executors/*`（pull 注册，P3-D-01）；P7 再加。

### 9.3 Views（关键字段）

- `ExecutionOverview`：`version_id,active_wave?,counts,case_run_counts,open_human_requests,executor:{id?,status,endpoint,capacity,active_contexts,last_heartbeat,version},quotas:{global,per_sut,cross_system},human_request_timeout_seconds`。
- `IntentView`：`id,version_id,case_version_id,case_id?,case_title,version_sut_ids[],purpose,status,attempt_no,created_at`。
- `RunWaveView`：`id,version_id,status,trigger,counts,started_at,finished_at,paused_at,pause_mode,pause_reason,created_at`。
- `CaseRunView`：`id,wave_id,intent_id,case_id,case_version_id,title,status,failure_class,attempts,summary,started_at,finished_at,version_sut_ids[],evidence_count`。
- `CaseRunDetail`：`case_run` + `steps[]{seq,target_version_sut_id,status,observed,error,started_at,finished_at}` + `assertions[]{step_seq,assertion_seq,kind,verdict,expected,actual,reason,target_version_sut_id}` + `evidence[]{id,kind,sha256,size_bytes,mime,filename,version_sut_id,step_seq,assertion_seq}`。
- `EvidenceView`：`id,case_run_id,version_sut_id,kind,sha256,size_bytes,mime,filename,created_at`（不含 content_ref/路径）。
- `HumanRequestView`：`id,version_id,case_run_id,kind,status,payload{reason,version_sut_id,account_role,page_url,challenge_kind,evidence_id?},expires_at,response?,resolved_by?,resolved_at?,created_at`（response 不回显明文密码；code 可回显? P3 不回显 code，只显示 resolved）。
- `ExecutorView`：`id,name,endpoint,status,capacity,active_contexts,labels,last_heartbeat,version,last_error`（admin only；不含 token_hash）。

## 10. 事件与 SSE（P3 增量）

### 10.1 事件类型（`events.md` §12，channel 一律 `version:{id}`）

| type | payload（scope IDs 为十进制字符串） |
|---|---|
| `execution.intent_created` | `intent_id,case_version_id,purpose,version_sut_ids[]` |
| `execution.intent_cancelled` | `intent_id,reason` |
| `execution.intent_superseded` | `intent_id,case_version_id,reason` |
| `wave.created` | `wave_id,trigger,intent_count` |
| `wave.started` | `wave_id` |
| `wave.paused` | `wave_id,mode,reason` |
| `wave.resumed` | `wave_id` |
| `wave.finished` | `wave_id,status,counts` |
| `case_run.started` | `wave_id,case_run_id,case_version_id,version_sut_ids[]` |
| `case_run.paused` | `case_run_id,reason` |
| `case_run.resumed` | `case_run_id` |
| `case_run.finished` | `wave_id,case_run_id,status,failure_class,duration_ms` |
| `case_run.step_finished` | `case_run_id,step_seq,status,version_sut_id`（命名对齐 case_run 前缀；HLD 示例 `step.finished` 以本契约为准） |
| `case_run.assertion_finished` | `case_run_id,step_seq,assertion_seq,kind,verdict,version_sut_id` |
| `evidence.created` | `case_run_id,evidence_id,kind,version_sut_id,size_bytes` |
| `human_request.created` | `human_request_id,case_run_id,kind,expires_at` |
| `human_request.resolved` | `human_request_id,case_run_id,status` |
| `human_request.expired` | `human_request_id,case_run_id` |
| `execution.failure_candidate` | `wave_id,case_run_id,failure_class,category,severity,dedupe_hint` |
| `execution.coverage_gap` | `wave_id,requirement_id,reason,case_version_id?` |

- 不新增 `executor.*` 业务事件（执行器状态属运维，不污染版本频道；管理页直接读表）。
- scope 带 `version_id/wave_id/case_run_id/case_version_id/version_sut_id`；payload 只带 ID/计数/状态；不含证据字节/路径/凭据/模型输出。
- 事件持久化与 SSE 复用 P0：`GET /api/events`、`GET /api/stream?channel=version:{id}`；不新增端点/频道。

### 10.2 前端消费

`useVersionEvents` 增加 `wave.*`/`case_run.*`/`evidence.created`/`human_request.*`/`execution.*` 分支：去抖 300ms 重拉 overview/wave/case-run/evidence/human-requests；`stream.reset` 重新拉快照；不本地合并步骤结果。SSE 状态徽章复用 `SSE_STATUS_LABEL`。

## 11. 设置与 env（P3 增量）

### 11.1 不新增 EzTest 启动 env

4 项 env 不变；执行器 token 走 DB settings secret；执行器进程自身 env 不属于 EzTest 启动 env（`contracts/executor-protocol.md` 说明）。

### 11.2 新增 5 个设置键（44→49；execution 类 6→11）

| key | type | default | secret | restart | 说明/范围 |
|---|---|---|---|---|---|
| `execution.executor_token` | string | `eztest-dev-executor-token` | **是** | 否 | API→executor Bearer；执行器进程用同名 env 接收；UI 掩码；首次启用 secret 语义 |
| `execution.executor_heartbeat_seconds` | number | `10` | 否 | 否 | 1–60；轮询 healthz 间隔 |
| `execution.executor_offline_seconds` | number | `30` | 否 | 否 | 5–300；心跳超时判 offline（≥2×heartbeat 建议校验） |
| `execution.executor_context_ttl_seconds` | number | `300` | 否 | 否 | 30–3600；非 running case 的 context 清理 TTL |
| `execution.human_request_timeout_seconds` | number | `1800` | 否 | 否 | 60–86400；login_takeover 过期 |

- 已冻结的执行键继续使用：`execution.global_concurrency=3`、`per_sut_concurrency=2`、`cross_system_concurrency=1`、`executor_url`、`evidence_max_bytes=5242880`、`case_timeout_seconds=900`。
- settings seed/目录/测试/handoff 同步；`GET /api/admin/settings` 对 token 返回 `value=null`、`is_secret=true`；PATCH `""` 清空、省略/null 不改（P0 语义）。
- `paths.evidence_dir` P3 不使用（P3-D-20 例外，settings §4c 记录）；`paths.data_root` 不变。
- `scripts/dev-executor.ps1` 从 DB 读取 token（psql 查询 `system_setting.value_json`），设置 `EZTEST_EXECUTOR_TOKEN` 后启动；缺失时提示管理员设置。

## 12. 前端（`frontend/src/features/execution/`）

### 12.1 信息架构

`/versions/[id]?tab=execution`（已有 Tab）；深链 `?wave=`、`?case_run=`（新增，写入/读取 URL）。布局沿用 mock.css `.exec-layout` 三栏语义（响应式：窄屏上下堆叠）：
- **顶部工具栏**：活跃波标题/状态/触发/时间、counts 徽章（通过/失败/阻塞/暂停/待执行）、暂停/恢复/取消按钮、SUT 过滤、SSE 状态、执行器状态徽章（offline/degraded 时告警）。
- **左栏 `WaveList`**：波次历史（倒序分页）+ 活跃波高亮；每项显示 `#id · status · 时间 · passed/total · failed`；点击切换 `?wave=`。
- **中栏 `LiveTrace`**：当前波/选中 case_run 的步骤-断言-证据轨迹（按 step seq 升序；每行显示 target SUT chip、状态、耗时）；SSE 更新时保持滚动位置；空态“确认用例后自动入队组波”。
- **右栏 `ResultsPanel`**：case_run 结果表（title/SUT chips/status/failure_class/断言 x/y/证据数/耗时），失败/阻塞置顶；行展开 `CaseRunDetail`（步骤清单 + 断言 expected/actual + 证据缩略图）；终态行“重跑”（purpose=retest）。
- **HumanRequestBanner**（open 时置顶）：原因/系统/账号角色/操作指引/到期倒计时/验证码或备注输入/完成/取消。
- **PausedBanner**：波次/用例暂停模式、原因、已完成计数、恢复按钮。
- **EvidenceViewer**：Dialog；截图 objectURL（revoke）、文本/JSON 展示；sha256/size/“不可变证据”；下载按钮走 `apiDownload`。
- 空态/错误/加载：EmptyState/ErrorState/LoadingCard；执行器 offline 显示“等待执行器恢复”；无权限/404 沿用。
- 不显示问题/报告/视觉子视图；不新增全局路由；不改 VersionHeader 语义。

### 12.2 组件/文件

`ExecutionTab.tsx`、`WaveList.tsx`、`LiveTrace.tsx`、`ResultsPanel.tsx`、`CaseRunDetail.tsx`、`EvidenceViewer.tsx`、`HumanRequestBanner.tsx`、`PausedBanner.tsx`、`PauseDialog.tsx`、`ExecutionToolbar.tsx`、`labels.ts`、`types.ts`、`api.ts`、`useExecutionEvents.ts`（或扩展 `useVersionEvents`）、`test-fixtures.ts`。

- `api.ts`：`getOverview/listWaves/getWave/pauseWave/resumeWave/cancelWave/listCaseRuns/getCaseRun/pauseCaseRun/resumeCaseRun/listEvidence/getEvidence/downloadEvidence/listHumanRequests/resolveHumanRequest/cancelHumanRequest/listIntents/createIntent/cancelIntent`；写操作传 `idempotencyKey`（自然幂等仍服务端保证）。
- `labels.ts`：四态/过程态中文 + BadgeTone；failure_class 中文；wave/case 状态；pause mode；human request kind/status；SSE 状态复用。
- 交互：暂停默认 graceful + 说明；immediate 二次确认；恢复时如有 open human → 提示先处理；重跑确认；证据下载失败显示 ApiError。
- 可访问性：状态文字+颜色双编码；横幅 `role="alert"`；Dialog Esc/焦点；表格 aria-label。

### 12.3 SSE/刷新

复用 `useVersionEvents`（增加执行事件分支）；事件去抖 300ms 重拉快照；`stream.reset` 重拉；页面隐藏时暂停自动滚动；执行中不整页刷新。断线徽章“实时/重连中/已断开”。

### 12.4 前端测试

- `api.test.ts`（URL/method/headers/Idempotency-Key/错误信封）。
- `ExecutionTab.test.tsx`（加载/空/错误/深链/SSE 去抖/wave 切换）。
- `PauseDialog.test.tsx`（graceful 默认、immediate 二次确认、已暂停幂等）。
- `ResultsPanel.test.tsx`（四态、失败置顶、展开、重跑）。
- `EvidenceViewer.test.tsx`（objectURL revoke/下载/失败）。
- `HumanRequestBanner.test.tsx`（渲染/resolve/取消/到期）。
- `labels.test.ts`；`VersionDetailPage.test.tsx` 更新 execution 分支与 `?wave=` 深链。
- 浏览器 E2E `frontend/e2e/p3.spec.ts`（真实 LLM，见 §13）。

## 13. Demo 目标系统与验收夹具

### 13.1 扩展 `scripts/fixture-target.cjs`（用户裁决 A）

保留现有 `/login`、`/account`、`/api/profile`、`/p5/*`、`/p6/*` 路由行为不变，新增 P3 双 SUT 场景（同一脚本、按 `FIXTURE_VARIANT=orders|payments` 与端口启动两份）：
- 通用：`/p3/login`（用户名/密码；payments 变体支持 `?mfa=1` 两阶段：密码通过后显示 `#mfa` 验证码输入，校验码 `246810`；错误显示 `.error`）；`/p3/home`（登录后）；`/p3/orders`（列表，含订单 `ORD-1001` 金额 `5200.00` 状态 `paid`）；`/p3/orders/1`（详情，金额文本 `¥5,200.00`；`#refund-amount` 显示 `¥5,000.00`（与实付不一致，用于 application 失败）；`/p3/refund` 表单可创建退款并跳转；`/p3/slow?ms=8000`；`/p3/timeout-window?ms=10000`（弹出带倒计时确认弹框，`ms` 后自动移除；`#confirm-dialog` 在期间可见，`#confirm-dialog` 消失后出现 `#window-expired` 文本；支持 `ms` 到 120000）；`/p3/2fa` 页面）。
- payments 变体：`/p3/payments`（显示退款单号/金额/状态）、`/api/p3/payments`（JSON）、`/p3/login?mfa=1`。
- 健康：`GET /health`；所有页面 loopback。
- DB 夹具：`scripts/fixture-db.ps1` 扩展（或新 `scripts/p3-fixture-db.ps1`）在目标 PG 建/重置 `p3_orders`/`p3_refunds` 表并 seed：`orders(id=1, amount=5200.00, status='paid')`、`refunds(order_id=1, amount=5000.00, status='created')`；DB 断言用例可 `SELECT status FROM refunds WHERE order_id=1`。
- 启动：`scripts/p3-acceptance.ps1` 同时启动 `FIXTURE_PORT=13335?`（orders）与 `13337`（payments），或使用 `FIXTURE_VARIANT` + 端口；不占用 executor 13336。记录到 `.tmp/p3/fixture-*.log`。
- 兼容：现有 P2/P5/P6 路由不动；P2 E2E 不依赖 fixture 时无影响。

### 13.2 验收场景（真实 LLM，一次性脚本）

`scripts/p3-acceptance.ps1`（真实 LLM；启动 API/executor/fixture/DB；隔离库 `eztest_v02_e2e`；API 14333、Web 16868、LLM 用 .env 真实配置）：
1. **准备**：清理/重建 e2e 库；启动 API（迁移 20）；admin 登录；写真实 LLM 配置；建 2 个 SUT（Order Center/Payment Gateway）+ 环境 + 页面 + API + 目标库（fixture DB）+ 账号角色（admin/user）；建版本绑定 2 SUT；建 2 条已澄清需求。
2. **用例/确认**：SQL 预置 4 条 draft case（单 SUT 通过、跨系统通过、application 失败、2FA 接管）及其 step/assertion；调用 P2 `POST /design/confirmations`（case/requirement）确认 → current+confirmed；断言 `design.case_confirmed` 事件。
3. **自动入队/组波**：等待 intent/wave；断言无“开始执行”API/按钮；重复确认不新增 intent/case_run；全局 3/每 SUT 2/跨系统 1 配置生效（用 3 条并发用例验证至少不超限）。
4. **单 SUT 执行**：等待 passed；断言 step/assertion/evidence 完整、`version_sut_id` 正确、证据 sha/size/下载一致；真实 LLM 日志无密码。
5. **跨系统执行**：等待 passed；断言 2 个 version_sut contexts、步骤目标分别正确、证据/结果/失败分类带两个 version_sut_id；counts.by_version_sut 正确。
6. **失败分类/问题候选**：等待 failed(application)；断言 failure_class=application、`execution.failure_candidate` 事件与团队帖存在；无 issue 表/API。
7. **覆盖缺口**：断言 `design.gap` work item 存在（requirement + case_version），且无 `analysis_run`/report 表。
8. **登录接管**：2FA 用例 → human_request(open) → wave/case paused → resolve `246810` → 恢复 → passed；断言事件/状态/耗时。
9. **暂停/恢复**：slow 用例执行中 pause immediate → wave/case paused、executor context 取消；resume → 从首个未通过步骤续跑 → 终态；断言 paused 期间单活跃波仍占用。
10. **执行器 offline/重派**：slow 用例执行中停止 executor → poller offline → case_run interrupted(executor_lost)、epoch++；重启 executor → 重派 → 终态；断言旧 epoch 写入被拒（集成测试覆盖）。
11. **stale/superseded**：对某 confirmed 用例的需求创建新版本（P1 API）→ case stale → intent superseded、case_run superseded；无新 intent。
12. **重跑/复测**：失败用例调用 `POST intents {purpose:"retest"}` → 新 intent/wave → 再执行（真实 LLM）；旧结果保留；重复创建 active → 409。
13. **SSE/证据**：使用 `GET /api/stream?channel=version:{id}` 断言收到 wave/case/evidence/human 事件；`Last-Event-ID` 回放无重复。
14. **时间窗口（含 120s）**：`/p3/timeout-window?ms=10000` 用例：打开 → 断言 `#confirm-dialog` 可见 → `execution.wait {seconds:12}` → 断言弹框消失且 `#window-expired` 出现 → 截图证据；默认验收跑 10s。`-LongWait` 模式改用 `ms=120000` + `execution.wait {seconds:122}`，证明 ≥120s 真实时间窗口；断言等待期间 lease 心跳持续、暂停可中断（单测/集成覆盖）、case 不因等待被误判失败。
15. **回归**：`scripts/test.ps1`、P1/P2 验收（fake 回归）、`node scripts/p3-contract-check.cjs`、迁移幂等。
16. **证据落盘**：日志 `.tmp/p3-acceptance/`、截图 `frontend/e2e` 的 `.tmp/p3-acceptance/screenshots/`。

`scripts/p3-e2e.ps1`（真实 LLM，浏览器）：
- 复用 P2 e2e 的服务启动模式；准备 1 条简单通过用例（SQL+确认）；登录 → execution Tab → 观察波次/结果 → 打开证据查看器 → 暂停/恢复 → 重跑；断言 `pageErrors=[]`；截图 `.tmp/p3-acceptance/screenshots/`。

**真实 LLM 策略**：所有执行 Agent 走真实 DeepSeek；允许等待（每 case 软 780s/硬 900s）；失败不重试超过 3 次、不用 fake 替代；若模型无法稳定完成验收用例 → handoff blocked 并记录（P2-D-30 同规则）。为降低波动，用例步骤 ≤4、断言 ≤2，页面文本稳定。

## 14. 契约变更与冻结

| 文件 | P3 动作 | 冻结 |
|---|---|---|
| `contracts/openapi.yaml` | `info.version: 0.2.0-p3`；新增 22 paths / schemas（§9）；tags Execution/Evidence/HumanRequests；根 `x-frozen: P3`，删除 `x-draft`；P2 paths/schemas 保持 | `x-frozen: P3` |
| `contracts/events.md` | §12 P3 20 个事件 + 约束；`step.finished` 以 `case_run.step_finished` 冻结（说明 HLD 示例名映射） | `frozen: P3` |
| `contracts/db-schema.md` | §9：10 表逐列/PK/UK/IDX/枚举/不变量；说明 `executor_instance`/`evidence_ref`/`human_request` 归属与 `paths.evidence_dir` 例外 | `frozen: P3` |
| `contracts/migrations-order.md` | §5c：3 个 execution 迁移；`order.txt` 第 18–20 | `frozen: P3` |
| `contracts/task-types.md` | §4 移出 `execute.case/analyze.failure/analyze.coverage`；新增 §5（P3 类型） | `frozen: P3` |
| `contracts/agent-profiles-tools.md` | §4b 移出已启用工具；新增 §6（3 profiles、工具 schema/scope/校验/预算） | `frozen: P3` |
| `contracts/settings.md` | §3 44→49 键 + execution 类计数；§4c P3 使用说明（含 evidence_dir 例外、token secret 首次启用） | `frozen: P3` |
| `contracts/executor-protocol.md` | **新建**：协议版本、认证、contexts/actions、多 SUT 映射、metadata/GET contexts、pull 注册/心跳/容量/排空/offline、token env、artifact→content ledger、错误码/限额、P7 保留 | `frozen: P3` |
| `contracts/README.md` | 目录表加 executor-protocol；追加 P3 冻结状态段（paths/ops/schemas、事件、表、迁移、profiles/tools、settings） | — |
| `scripts/p3-contract-check.cjs` | 新建：断言 openapi `x-frozen P3`/无 `x-draft`/必需 paths/schemas；字符串断言 events/db/migrations/task-types/profiles/settings/executor-protocol | — |
| `frontend`/`backend` 调用方 | 同步 schema/错误码/版本号 `0.2.0-p3`；`/api/health.version` 同步 | — |

- P2 `p2-contract-check.cjs` 改为“P2 增量仍存在”兼容模式（不再断言根 `x-frozen: P2`），避免 P3 冻结后误红；P2 契约内容不变。
- P1/P2 验收脚本中的 `0.2.0-p2` 断言更新为 `0.2.0-p3`（测试证据调整，不改契约语义）。
- 破坏性变更：无（只追加表/路径/事件/任务/工具/设置；P0/P1/P2 字段与迁移不变）。若实现中发现必须改 P2 字段 → 停止并报告。
- `executor-protocol.md` 的 token/注册方式（pull）与 HLD 的 `/api/internal/executors/*` push 差异记录为 P3-D-01/P3-D-02，P7 多副本时补 push。

## 15. 测试与验收计划

### 15.1 门禁（每任务）

- Go：`go test ./...`；集成 `go test -tags=integration ./...`（使用 `EZTEST_TEST_DATABASE_URL`）。
- 前端：`pnpm lint`、`pnpm typecheck`、`pnpm test`。
- executor：`cd executor && pnpm test`（node:test，真实 Chromium；本地可跑）。
- 契约：`node scripts/p3-contract-check.cjs`（阶段内 draft 模式）。
- 每任务小步提交（`type(p3): ...`），不纳入 dirty 文件。

### 15.2 测试矩阵

| 层 | 覆盖 |
|---|---|
| 领域单测 | intent 唯一/幂等/取消/重跑/superseded；wave 单活跃/状态机/pause/resume/counts；case_run 四态聚合矩阵；failure_class 映射；配额 3/2/1 与跨系统预留；fingerprint/code_baseline/dedupe；evidence 上限/sha；dispatch epoch/token；human timeout；`execution.wait` 边界/累计超时/暂停取消中断 |
| 仓储集成 | 3 迁移幂等（20）；partial unique 生效；case_run 唯一 `(wave,intent)`；step/assertion upsert；executor_instance upsert；human_request resolve CAS |
| 应用/集成 | intent 生成/重复确认；wave 组波/派发；offline→interrupted→重派（旧 epoch 拒绝）；stale 取消；租约过期恢复；版本 completed→running；cleanup 后释放配额；SSE 事件 |
| HTTP 集成 | 22 paths 路由/鉴权/分页/错误码/404/409；证据下载 sha/归属；admin executor drain；human resolve |
| Agent 集成 | profiles seed；ProfileKeyForWorkType；工具 schema scope；step/assertion 写校验；human.request 暂停；runner 结束聚合 |
| 前端单测 | §12.4 |
| 真实 LLM 验收 | §13.2（API）+ §13.2 E2E |
| 回归 | P1 19 steps、P2 21 steps、P2 E2E、P1/P2 真实 LLM 脚本不回归（可只跑 fake P1/P2 验收 + P3 真实） |
| 契约/迁移 | p3-contract-check；20 迁移重复执行 no-op；P2 契约不变 |

### 15.3 证据路径

- `.tmp/p3/final-gate.log`、`.tmp/p3/baseline-test.log`
- `.tmp/p3-acceptance/api.log`、`llm.log`、`fixture-orders.log`、`fixture-payments.log`、`executor.log`
- `.tmp/p3-acceptance/screenshots/`（UI E2E）
- `executor` 测试输出、Go 集成输出、`p3-contract-check` 输出

## 16. 并行实施建议（供 plan 使用）

**依赖**：T1（契约骨架/领域裁决）→ T2（迁移+domain/repo）→ {T3 intent/reconciler, T4 wave/planner, T5 executor registry/fencing} 可并行 → T6 case executor/多 SUT 核心串行 → {T7 结果/证据, T8 DB 断言, T11 human/暂停} 并行 → {T9 failure, T10 coverage, T12 API, T13 events/SSE, T14 UI, T15 profiles/tools, T16 reconciler 集成} 并行 → T17 测试/验收 → T18 契约冻结/handoff。

**写作用域建议**（Team Lead 分派，避免同文件并发）：
- 子代理 A：execution domain/repo/迁移 + 调度（T2–T4）。
- 子代理 B：executor 客户端/协议 + registry/fencing（T5，含 executor/src）。
- 子代理 C：case executor/工具/多 SUT/DB 断言（T6–T8）。
- 子代理 D：failure/coverage/human/API/事件（T9–T13）。
- 子代理 E：前端 execution feature（T14）。
- 子代理 F：fixture/验收脚本（T15/§13）。
- Lead：contracts、openapi、migrations-order、共享文件、最终评审与门禁；子代理不并发写契约。
- 每任务 TDD、独立提交；共享文件由 Lead 串行合并。

## 17. 实施裁决（P3-D；冲突/缺口落地，不改设计文档）

| ID | 裁决 | 依据/理由 |
|---|---|---|
| P3-D-01 | 执行器注册/心跳采用 **API pull healthz**；`/api/internal/executors/*` push 留 P7 | 单实例 P3；HLD §1.3 action-level；避免第 5 env/出站配置；行为可测 |
| P3-D-02 | 执行器 token 用新增 secret setting `execution.executor_token`；执行器进程用自身 env 接收 | 严格 4 env（D49）；settings §2.1 secret 语义；P3 首次启用 secret |
| P3-D-03 | 执行主体 = **API 内 Case Executor Agent 逐动作驱动 executor**；case_run 的 epoch/token 由 API 侧校验迟到写 | HLD §1.3 L68；0.1 A′ 结构参考；phase-03 的 dispatch 字段解释为派发实例 fencing |
| P3-D-04 | P3 建 10 表（7 核心 + executor_instance + evidence_ref + human_request） | phase-03 范围；HLD §2.2；不建 `analysis_run`/`sut_account_session` |
| P3-D-05 | 四态=`passed/failed/blocked/inconclusive`；`skipped/interrupted/superseded` 过程态；case_run 状态列合并两者并写迁移矩阵 | ARCH §6.4；HLD §2.4；phase-03 |
| P3-D-06 | `assertion_result.assertion_seq` 由 `case_assertion.id` 升序派生；不改 P2 表 | P2 无断言序号列；HLD 未定义；P3 最小侵入 |
| P3-D-07 | `step_result.status`/`assertion_result.verdict` 枚举按 §4.4；`observed/summary` JSONB | HLD 字段存在但无枚举；补契约 |
| P3-D-08 | intent 唯一键 `(case_version_id,environment_fingerprint,code_baseline,purpose) WHERE active`；`version_sut_ids` JSON；`dedupe_key` 非唯一；`attempt_no` 展示 | HLD §2.3；ARCH §8.5-1；解决单数/复数冲突 |
| P3-D-09 | `environment_fingerprint`/`code_baseline` 算法按 §5.1；P3 code_baseline 用 repo branch，P5 升级 commit | 文档未定义；必须可复现幂等；P3 无 commit 数据 |
| P3-D-10 | 单活跃波 partial unique 含 `planned/running/paused`；wave `done` 不代表全通过 | HLD §2.3；D13/D29；四态口径 |
| P3-D-11 | 配额口径：全局= running case_run 数 3；每 version_sut= running case_run 数 2；跨系统= running 跨系统 case_run 数 1；context 数另按 executor capacity 预留 | ARCH §11.2；settings 默认；必须冻结 handoff |
| P3-D-12 | failure_class 9 值（§4.5）；仅 application 产问题候选；无证据不判 application | HLD §6.6 `failed(application)`；phase-03 失败分类；0.1 参考枚举 |
| P3-D-13 | P3 不建 issue/report/analysis_run；问题候选=事件+团队帖；覆盖缺口=design.gap+事件（用户裁决 A） | phase-03 Out；phase-04 In；用户 2026-11-01 裁决 |
| P3-D-14 | `analyze.failure`/`analyze.coverage` 任务；`analyze.wave` 视为简写不注册 | HLD §3.1；task-types §4；ARCH §5.3 简写 |
| P3-D-15 | Execution Planner 确定性 runtime，不注册 profile | ARCH §7.1；D35/D37 |
| P3-D-16 | human_request 表归 execution 迁移；status `open/resolved/cancelled/expired`；kind 保留三值 | phase-03 In；HLD §2.2 字段；P4 待办入口只读 |
| P3-D-17 | 登录接管=暂停-恢复+一次性 response（2FA code），不做远程浏览器；最多 3 次/用例 | HLD §6.5-6；安全/实现边界；P3-D-17a 后续可加 storageState |
| P3-D-18 | 失败/覆盖分析先确定性兜底，再由 Agent 增强；Agent 失败不阻塞 wave | D40/AI 候选；P3 验收必须可复现 |
| P3-D-19 | 新增 5 设置键（44→49）；`executor_token` secret；heartbeat/offline/TTL/human timeout | settings §5 增量；phase-03 注册/心跳/容量/排空/接管缺值 |
| P3-D-20 | 证据字节入 P0 内容账本；`paths.evidence_dir` 不用；evidence 存 content_ref_id+sha/size/mime；下载鉴权 | HLD §2.2/§12.3；P2-D-29 先例；D65 GC |
| P3-D-21 | 证据 kind 8 类；每 case_run ≤100；单条 ≤5MiB；超限不失败 | HLD/设置；防滥用；报告体积 |
| P3-D-22 | DB 断言仅 PG 只读；单条 SELECT/WITH、5s、≤200 行、≤64KB；自动 db 证据 | ARCH §11.3；D11 凭据可读；防注入 |
| P3-D-23 | HTTP 工具只允许 version_sut 环境 host；api_id 解析 method/path；脱敏证据 | OD-7；防 SSRF；信任模型 |
| P3-D-24 | `account.credentials` 返回明文（审计、UI 不回显、证据擦除）；按 role 解析 | D11/OD-6；信任模型 |
| P3-D-25 | 暂停：wave graceful/immediate；case-level immediate；paused 占活跃波；human=paused(waiting_human) | D31；HLD §2.4；avoid waiting 新状态 |
| P3-D-26 | 人工取消波次/用例用 `superseded` 记 case_run；intent cancelled；不发失败分析 | phase-03 取消/退出执行集；ARCH §8.5-8 |
| P3-D-27 | offline/lease 过期：case_run interrupted→pending（同 ID、attempts+1、epoch+1）最多 3 次；否则 failed(environment) | ARCH §12.4；phase-03 重派；避免无限重试 |
| P3-D-28 | 版本 completed 时新 intent 触发 reopen（清 closed_at）；P4 收尾 | HLD §6.7；P3/P4 交接 |
| P3-D-29 | 22 条 API path；证据单独鉴权下载；intents 暴露；admin executors drain/activate | HLD 前缀/phase-03 能力；UI 需要；契约冻结 |
| P3-D-30 | 事件 20 个；`step.finished` 冻结为 `case_run.step_finished`；不发行 executor 业务事件 | events.md 风格；HLD 示例映射；避免频道污染 |
| P3-D-31 | 前端三栏（波次/实时/结果+证据）+顶部工具栏/横幅；暂停/接管在 Tab 内；不新增全局路由 | mock.css exec-layout；HLD §8；P2 模式 |
| P3-D-32 | 实时 = SSE 去抖重拉快照（不本地合并）；复用 useVersionEvents | P2 先例；DB 真值；实现稳定 |
| P3-D-33 | 验收仅真实 LLM（用户裁决 C）；SQL 预置 draft 用例 + P2 确认 API；不跑设计 LLM | 用户 2026-11-01；P2 已验收设计；聚焦 P3 |
| P3-D-34 | demo 扩展现有 fixture-target 双变体（orders/payments）+ fixture DB；不新增产品目录（用户裁决 A） | 用户 2026-11-01；复用测试资产 |
| P3-D-35 | executor 扩展仅 metadata + `GET /v1/contexts`；不引入 case/wave 语义 | HLD §1.3；最小协议扩展 |
| P3-D-36 | `Agent.md` §6.1/§6.5/§6.7–6.9 的 P 编号标注为 0.1；不改其权威/目录树语义 | 用户 2026-11-01 要求；P2 handoff 已记录 |
| P3-D-37 | P2 `p2-contract-check.cjs` 改兼容模式；P1/P2 验收脚本版本号更新为 `0.2.0-p3` | P3 冻结后回归可跑；不改契约语义 |
| P3-D-38 | 前端 `?wave=`/`?case_run=` 深链；执行 Tab 计数不加后端字段（用 overview） | HLD §8.1；避免 Version 契约破坏性变更 |
| P3-D-39 | `case_assertion.kind=visual` 在 P3 记 `skipped`，不判 passed；P5 实现 | phase-03 Out/P5 In；不得假通过 |
| P3-D-40 | 提交/分支：`feat/v0.2-p3` 从 P2 HEAD 拉出；`type(p3):`；验收证据 `.tmp/p3*` | P2 先例；PLAN 频繁提交；无正式规范 |
| P3-D-41 | **Agent 等待**：新增 API 侧 `execution.wait {seconds:1..300}`；等待不占执行器 action 时限、可被暂停/取消/lease 丢失中断、累计计入 case 超时；执行器不实现 sleep；120s 时间窗口纳入 `-LongWait` 验收 | 用户 2026-11-01 补充；执行器单 action ≤60s；页面定时器继续；租约心跳保持 |

## 18. 风险与已知缺口（写入 handoff）

1. **真实 LLM 驱动浏览器不稳定**：DeepSeek 可能不按步骤调用工具/幻觉；验收用例必须短小确定；失败如实记录，不用 fake 替代；P8 做质量评测。缓解：强 system prompt、步骤/断言显式、工具一次一动作、真实探针先跑。
2. **跨系统执行复杂**：多 context/配额预留/登录/证据归属；用 2 SUT 最小闭环 + 配额矩阵单测；死锁测试覆盖。
3. **执行器离线/租约竞态**：offline 检测与旧 epoch 写入并发；用 CAS + 集成测试（延迟写入/双派发）。
4. **证据体积**：截图/网络/DB 证据可能超 5MiB/100 条；强制上限/截断/丢弃计数；P7 GC 依赖 content_ref。
5. **登录接管体验**：无远程可视化；验证码/复杂挑战可能无法完成；P3 验收用 2FA 数字码，复杂场景记 blocked；后续增量 storageState/可视化。
6. **计划任务可靠性**：1s 调度器单实例；API 崩溃后 intent/case_run 恢复依赖 DB 状态；不批量中断 running（P1 规则）。
7. **P2 遗留**：断言 target 必须等于 step target（跨 SUT 断言受限）；stale hook 非同一事务；P3 用 DB 真值 + reconciler 兜底，不依赖事件顺序。
8. **契约冻结**：P3 冻结后 P4 只能增量；`executor-protocol.md` push 注册留 P7；`analysis_run` 归 P4。
9. **SQL 预置验收用例**：绕过 P2 图校验；P2 已验收设计/确认流程，P3 聚焦执行；SQL 夹具只用于验收，不进入产品数据。
10. **设置 secret 首次启用**：token 默认已知（dev）；对外部署必须修改；UI 掩码/审计。
11. **Agent.md 0.1 标注**：文档编辑不影响设计权威；若用户希望进一步重写目录树，列入 P8/文档维护。
12. **长时间等待**：单次 ≤300s、用例 ≤900s（可配）；更长的真实等待需要调整超时或拆用例；等待期间页面若发生导航/上下文丢失按环境失败处理；`execution.wait` 不产生证据，验收必须等待后显式 snapshot/assert。

