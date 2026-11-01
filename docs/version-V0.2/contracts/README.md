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
