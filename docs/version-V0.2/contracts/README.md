# EzTest 0.2 阶段间契约

> 状态：模板/机制说明；具体内容由各阶段实施时写入并冻结。
> 规则：**代码先行、契约后补**不允许；阶段末必须冻结本阶段对外接口。
>
> **P0 冻结状态（2026-10-31）**：`openapi.yaml`（43 paths / 66 operations，`x-frozen: P0`）、`events.md`、`db-schema.md`、`migrations-order.md`、`settings.md` 已冻结。`task-types.md` 与 `agent-profiles-tools.md` 按机制说明从 P1 开始写入；P0 未创建。
>
> **P1 冻结状态（2026-10-31）**：`db-schema.md` §7（16 表/索引）、`migrations-order.md` §5a（7 迁移）、`events.md` §10（23 事件）、`settings.md` §4a（不新增键）、`task-types.md`、`agent-profiles-tools.md`、`openapi.yaml` P1 paths/schemas（10 个 path item / 23 schemas）已冻结为 **`frozen: P1`**。P1 的实现证据：`scripts/test.ps1` 全绿、`scripts/p1-acceptance.ps1` 19 步 PASS、`scripts/p1-e2e.ps1` 浏览器 PASS、`scripts/p1-acceptance-llm.ps1` 真实 DeepSeek `deepseek-flash` PASS。P2+ 只能增量，破坏性变更需先停止并更新契约/决策/handoff。
>
> **P2 冻结状态（2026-10-31）**：P2 已完成 design/exports paths/schemas（7 paths / 8 schemas）、§11 事件（9）、10 张表/2 迁移、5 类任务、5 个 profiles、4 个设计工具与 settings §4b。P2 增量已从 `x-draft: P2` / `draft: P2` 翻为 **`x-frozen: P2` / `frozen: P2`**。实现证据：`scripts/test.ps1` 全绿、`scripts/p2-acceptance.ps1` 21 步 PASS、`scripts/p2-e2e.ps1` 浏览器 PASS、`scripts/p2-acceptance-llm.ps1` 真实 DeepSeek `deepseek-flash` PASS、`scripts/p2-e2e-llm.ps1` 真实模型浏览器 PASS。冻结后 P3+ 只能增量，破坏性变更需更新契约/决策/handoff。
>
> **P3 冻结状态（2026-11-01）**：P3 执行/证据/人机请求契约已完成并冻结。`openapi.yaml`：`info.version: 0.2.0-p3`，新增 22 个执行/证据/人机请求 operation（21 个 path item；tags `Execution`/`Evidence`/`HumanRequests`）与 22 个 P3 schema，P0/P1/P2 paths/schemas 语义不变；根已翻为 **`x-frozen: P3`** 并删除 `x-draft`。`events.md` §12（20 事件，`case_run.step_finished` 冻结名）、`db-schema.md` §9（10 表逐列/约束/索引/不变量）、`migrations-order.md` §5c（3 个 execution 迁移，`order.txt` 第 18–20）、`task-types.md` §5（3 任务，已从 §4 移出）、`agent-profiles-tools.md` §6（3 profiles + 执行/等待/接管工具，含 `execution.wait`；§4b 只留 P4+ 预留）、`settings.md` §3/§4c（44→49 键，`execution.executor_token` 首个 secret）均为 **`frozen: P3`**；`executor-protocol.md` 新建并 `frozen: P3`。实现证据：`node scripts/p3-contract-check.cjs`（frozen 模式，81 paths / 110 operations / 122 schemas）、`scripts/p3-acceptance.ps1` 真实 DeepSeek 16 场景 PASS（默认 316.2s；`-LongWait` 384.3s，含 120s 时间窗口）、`scripts/p3-e2e.ps1` 浏览器 E2E 1 passed（2.7m）、`go test ./...` 与集成迁移 20 幂等。冻结后 P4+ 只能增量，破坏性变更需更新契约/决策/handoff。
>
> **P4 冻结状态（2026-11-01）**：P4 交付 + 链路契约已完成并冻结。`openapi.yaml`：`info.version: 0.2.0-p4`，新增 14 个交付/链路/收尾 operation（6 issue、5 report、1 chain、1 version reopen、1 requirement reopen；tags `Delivery`/`Chain`）与 P4 schemas，P0/P1/P2/P3 paths/schemas 语义不变（Version/Requirement 仅加可选字段）；根已翻为 **`x-frozen: P4`** 并删除 `x-draft`。`events.md` §13（10 事件）、`db-schema.md` §5/§10（delivery 上下文 7 表 + `requirement_closure` 逐列/约束/索引/不变量；chain 无表）、`migrations-order.md` §6（4 个迁移：`delivery_0001_issue_lifecycle` #21、`delivery_0002_report_revision` #22、`delivery_0003_analysis_run` #23、`version_0005_requirement_closure` #24）、`task-types.md` §6（5 任务，已从 §4 移出）、`agent-profiles-tools.md` §7（3 profiles/8 工具/scope 注入）、`settings.md` §4d（49→53 键，新增 4 个 `chain.*`；`paths.report_dir`/`report.render_pdf` P4 不使用/不消费）均为 **`frozen: P4`**。`node scripts/p4-contract-check.cjs` 为 P4 冻结门禁；`scripts/p2-contract-check.cjs`/`scripts/p3-contract-check.cjs` 改为“增量仍在、不硬编码阶段总数”的前向兼容模式。冻结后 P5+ 只能消费；破坏性变更需先停止并更新 contracts + 决策记录 + handoff。实现证据：`node scripts/p4-contract-check.cjs` OK（14 ops/8 表/4 迁移/10 事件/5 任务/3 profiles/8 工具/4 设置）；`scripts/p4-acceptance.ps1` 真实 DeepSeek `deepseek-flash` 13/13 PASS（226.1s）；`scripts/p4-e2e.ps1` 浏览器两阶段 PASS（17 截图 + 2 HTML）；P1/P2/P2-E2E/P3 回归全 PASS。
>
> **P5 冻结状态（2026-11-01）**：P5 代码智能 + 视觉契约已完成并冻结。`openapi.yaml`：`info.version: 0.2.0-p5`，新增 **38 个 operation**（34 个 path item：代码索引/树/文件/diff/事实/搜索/commit 11、版本变更/分析/影响 6、锚点 4、事实复核 2、页面/API 提议 6、视觉/质量 9）与 P5 schemas，新增 **`Code`/`Visual`/`Quality`** 三个 tag；P0/P1/P2/P3/P4 paths/schemas 语义不变（`Binding` 仅加可选 `code_baseline_commit`、`CaseRunSummaryView` 仅加视觉计数、`EvidenceView.kind` 枚举扩宽）；根已翻为 **`x-frozen: P5`**。`events.md` §14（**12 事件**）、`db-schema.md` §11–§13（**11 新表**：codeintel 8 + visual 3；`version_sut.code_baseline_commit`；`evidence.kind` **10 值**；`work_item.team` **6 值**）、`migrations-order.md` §7（7 个迁移：`codeintel_0001_code_index` #25、`codeintel_0002_code_analysis` #26、`codeintel_0003_anchor_review` #27、`visual_0001_visual_assertion` #28、`execution_0004_visual_evidence` #29、`version_0006_version_sut_code_baseline` #30、`agent_0004_code_team` #31；`order.txt` 共 **31**）、`task-types.md` §7（**2 任务** `index.repo`/`code.analyze` + **`code` 团队**）、`agent-profiles-tools.md` §8（**11 工具** + 5 个现有 profile 白名单增量）、`settings.md` §4e（**53→69**，新增 `codeintel` 9 + `visual` 7）、`executor-protocol.md` §11（协议 **v1.2**：`visual.compare`/body 限额/证据映射/环境指纹）均为 **`frozen: P5`**。P5 冻结计数：**31 迁移、76 表、38 operations、12 事件、2 任务 + `code` 团队、11 工具、16 设置、10 证据 kind、6 work item team**。`scripts/p5-contract-check.cjs` 为 P5 契约门禁（P5 装配任务落盘）；P2/P3/P4 检查保持前向兼容（只按增量存在、不硬编码阶段总数）。冻结时点：真实 LLM + 真实 Chrome 验收通过、`scripts/test.ps1` 全绿、`p5-contract-check.cjs` 通过后；冻结后 P6+ 只能消费，破坏性变更需先停止并更新 contracts + 决策记录 + handoff。
>
> **P6 契约状态（2026-11-01，`frozen: P6`）**：P6 全功能补齐契约增量已写入。`openapi.yaml`：`info.version: 0.2.0-p6`、`x-frozen: P6`，新增 **20 个 operation / 18 个 path item**（搜索 1、待办 1、质量/评测 4、全局问题/报告 2、Agent 中心 3、profile/提示词/工具 9）与 **46 个 P6 schema**，新增 **`Search`/`Workspace`/`AgentCenter`** 三个 tag；P0–P5 paths/schemas 语义不变（只加新 path 与可选字段），既有 `GET /api/versions/{versionId}/exploration-chain` 增加 chain v2 的 q/abnormal_only/issue_status/issue_severity/requirement_limit 参数与 counts/meta/degraded_layers/truncated_layers/replay 字段；P5 `GET /api/quality/visual-metrics` 语义不变。`db-schema.md` §14–§15（**3 新表**：quality `eval_run`/`eval_metric` + agent `agent_profile_prompt_revision`；无其他新表）、`migrations-order.md` §8（2 个迁移：`quality_0001_eval.sql` #32、`agent_0005_profile_prompt_revision.sql` #33；`order.txt` 共 **33**）、`settings.md` §4f（**69→72**，新增 `search.max_results_per_type`/`todos.max_items`/`quality.eval_baseline_regression`）、`agent-profiles-tools.md` §9（prompt 版本/变量/渲染/回退/marker/长度/权限/审计/下一 run 语义）均为 **P6**。P6 不新增事件、task type/team、profile、tool、executor 协议；不新增 env；不改 P0–P5 已冻结语义。契约门禁：`node scripts/p6-contract-check.cjs`。实现/真实 LLM/Chrome 验收证据**预留**：`scripts/p6-acceptance.ps1`、`scripts/p6-e2e.ps1`、`docs/version-V0.2/acceptance/p6/**` 与 `phase-06-handoff.md` 由后续任务落盘；验收通过后维持 `frozen: P6`，P7 只能消费，破坏性变更需先停止并更新 contracts + 决策记录 + handoff。

## 目录内容

| 文件 | 内容 | 首次写入 | 后续维护 |
|---|---|---|---|
| `openapi.yaml` | REST/SSE 对外契约 | P0 骨架 | 每阶段增量 |
| `events.md` | 事件类型/envelope/作用域字段 | P0 | 每阶段增量 |
| `db-schema.md` | 表、关键字段、约束、索引 | P0 | 每阶段增量 |
| `migrations-order.md` | 迁移文件顺序与 ownership | P0 | 每阶段追加 |
| `task-types.md` | work_item 类型/状态/去重键 | P1 | 每阶段增量 |
| `agent-profiles-tools.md` | profile、工具白名单、作用域注入、提交 schema | P1 | 每阶段增量 |
| `settings.md` | env 4 项、DB 设置键/默认/热加载/重启标记 | P0 | 每阶段增量 |
| `executor-protocol.md` | 执行器 HTTP 协议、token/注册/心跳/多 SUT 映射/证据交接 | P3 | 每阶段增量 |
| `handoff-template.md` | 阶段 handoff 模板 | 已有 | 按需 |

## 冻结规则

1. 阶段内可以改契约；阶段末把本阶段对外接口写入并标记 `frozen: <phase-id>`。
2. 下一阶段只消费 frozen 契约；需要破坏性变更时，先停止、说明影响面、更新本目录 + 决策记录，再继续。
3. 契约变更必须同步：调用方/测试/迁移/handoff；不允许只改代码。
4. 共享契约文件由阶段 Lead 统一收口；子代理不并发写同一个契约文件。
5. 数据库迁移只追加；已冻结迁移不得修改；顺序以 `migrations-order.md` 为准。

## 最小字段约定

- **API**：method/path/request/response/error codes/idempotency。
- **事件**：type/channel/scope ids/payload version/是否持久化。
- **表**：表名/主键/关键字段/唯一约束/索引/所属上下文/迁移文件。
- **任务**：type/team/input scope/dedupe key/输出/失败语义。
- **工具**：name/scope 注入字段/参数 schema/提交校验/权限。
- **设置**：key/type/default/secret/restart_required/热加载方式。

## 交接检查

阶段末 handoff 必须回答：

1. 本阶段改了哪些契约？是否 frozen？
2. 下一阶段可以依赖哪些接口/表/事件/任务/设置？
3. 哪些是临时/占位，下一阶段必须替换？
4. 迁移顺序、回滚/备份影响？
5. 测试命令与验收证据路径？
