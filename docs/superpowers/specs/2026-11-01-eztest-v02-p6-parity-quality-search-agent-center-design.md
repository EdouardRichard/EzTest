# EzTest 0.2 P6 全功能补齐 — 阶段设计规格（Spec）

> 状态：**待用户审阅**（2026-11-01）
> 阶段：P6（`phase-06-parity-quality-search-agent-center.md`）
> 实施分支：`feat/v0.2-p6`（从 `feat/v0.2` 拉出；完成后合回 `feat/v0.2`）
> 前置：P5 已完成并合入（`phase-05-handoff.md`；merge `90ad0f8`）
> 本 spec 是阶段实现依据，从属于 `docs/version-V0.2/` 设计文档；冲突以 0.2 设计文档/决策日志为准。**不修改 `docs/version-V0.2/` 设计文档**；只更新 contracts、handoff、迁移与测试/验收证据。
> 日期：2026-11-01

---

## 0. 依据、前置与冲突处理

### 0.1 权威来源与阅读依据

已实际读取（行号以当前工作树为准）：

- `Agent.md` §1–§6（文档地图、权威顺序、目录/导入约束）。
- `docs/version-V0.2/README.md`（全量）。
- `docs/version-V0.2/architecture-design.md` §6.3–§6.5、§7.1–§7.4、§8.4–§8.8、§10、§12、§15–§16、§18。
- `docs/version-V0.2/high-level-design.md` §1.1–§1.4、§2.1–§2.4、§3、§4、§6.4–§6.9、§7.1–§7.3、§8.1–§8.4、§9、§12。
- `docs/version-V0.2/research/decision-log.md` D01–D53（**决策日志止于 D53**）。
- `docs/version-V0.2/research/capability-matrix.md` §3 A–I、§5。
- `docs/version-V0.2/research/open-decisions.md` 全量（OD-1–OD-11）。
- `docs/version-V0.2/implementation-phase-plan.md` §0–§6。
- `docs/version-V0.2/phases/phase-06-parity-quality-search-agent-center.md`（全量，2.3 KB）。
- `docs/version-V0.2/phases/phase-05-handoff.md`（全量）。
- `docs/version-V0.2/contracts/`：`README.md`、`openapi.yaml`（P6 相关路径/schema）、`db-schema.md`（§5/§9/§11–§13/§13.3）、`migrations-order.md`、`events.md`、`settings.md`、`task-types.md`、`agent-profiles-tools.md`、`executor-protocol.md`、`handoff-template.md`。
- `docs/version-V0.2/ui/mock.html`（全量；mock 仅覆盖需求/设计/链路三屏）。
- 0.1 参考：`docs/superpowers/specs/2026-10-29-eztest-p8-hardening-design.md` §5/§6/§8/§9/§11/§15；`docs/superpowers/plans/2026-10-29-eztest-p8-hardening.md` 相关任务；`docs/detailed-design.md` §4.8–§4.12/§6；`docs/design/system-blueprint.md` D53–D69；`docs/release-notes/p8.md`；`docs/acceptance/p8/acceptance.md`、`wave1/exploration-chain.md`、`e2e/README.md`；`eval/README.md`、`eval/baseline.json`；0.1 历史对象（`git show 45e8d15:...` / `git show dev:...`）。
- ARTEX 参考（只读、AGPL-3.0，仅提炼能力，不复制源码）：`D:\Project_new\ARTEX\db\schema.sql`、`db/config.go`、`agent/prompt.go`、`agent/promptcatalog.go`、`agent/wrapup.go`、`server/server_mgmt.go`、`server/server.go`、`web/src/components/agent-editor.tsx` 等；详见 `.tmp/p6-research/08-artex-prompts.md`、`10-artex-agent-platform.md`。

已产出研究（本会话，只读，唯一写入 `.tmp/p6-research/`）：

- `01-arch.md`、`02-decisions-handoff.md`、`03-contracts.md`、`04-ui.md`、`05-codebase.md`、`06-p8-design.md`、`07-code-ref.md`、`08-artex-prompts.md`、`09-prompt-gap.md`、`10-artex-agent-platform.md`。

### 0.2 阶段映射与范围

- 0.2 阶段定义见 `implementation-phase-plan.md` §3；P6 = 全功能补齐（质量/评测、搜索、待办、Agent 中心、全局中心、链路 v2、能力矩阵）。
- 0.1 编号与 0.2 编号不得混用；本 spec 的 `P6-D-xx` 是 **P6 实施裁决**，不写回 0.2 决策日志。

### 0.3 前置检查（实施开始前执行；不满足则停止并报告）

1. 分支为 `feat/v0.2-p6`（若尚不存在，从 `feat/v0.2` @`1c10850` 或当前最新提交拉出）；`feat/v0.2` 包含 P5 merge `90ad0f8`。
2. 工作区：用户脏文件（`docs/development.md`、`docs/startup.md`、`docs/用户专用文件，禁止AI阅读.md`、`scripts/dev-all.ps1`）保持不动、不提交；实施文件不得覆盖它们。
3. 基线：`go build ./...`、`go test -count=1 ./...`、frontend `pnpm test`/`typecheck`/`lint`、executor `pnpm test`、P2–P5 契约门禁全绿（P5 handoff 基线：1438 Go test funcs、668 frontend tests/87 files、104 executor tests、31 migrations、69 settings、162 ops）。
4. 契约：P0–P5 全部 `frozen`；`openapi.yaml` 为 `0.2.0-p5` / `x-frozen: P5`；P6 只能追加。
5. 迁移：当前 31 条，`order.txt` 尾部为 `agent_0004_code_team.sql`；P6 使用 #32/#33。
6. 无任何 P6 实现（无 `internal/{quality,search,workspace}`、无 `/api/{search,todos,agent-center,issues,reports}`、`/agents` 为占位、`/quality` 仅 P5 视觉指标）。

### 0.4 用户裁决（2026-11-01 P6 会话）

| ID | 裁决 |
|---|---|
| U1 | 质量/评测按 **0.1 P8 完整对齐**：`eval_run`/`eval_metric` + 指标/阈值/基线 + 质量中心 + 评测 runner 双轨。 |
| U2 | 待办 = **确认类待办 + 设计待办 + 接管/人工请求**；入口为全局 Topbar TodoDrawer + `/api/todos`，**不新增一级导航**。 |
| U3 | 链路按 **增量对齐剩余 v2**：筛选/counts/typed meta/重连/回放细化；**不承诺任意时点 as-of**。 |
| U4 | 验收按 **B：全量真实 LLM + Chrome**（真实 DeepSeek `deepseek-flash` + 真实 Chrome + 浏览器 E2E/截图）。 |
| U5 | Agent 提示词运行态定制选 **A1**：完整对齐 ARTEX 核心（编辑、版本、历史/diff、恢复默认、变量/模板、预览、审计、热生效），并接受内网注入风险。 |
| U6 | 确认 ARTEX 能力矩阵处置：include/defer/na 见 §8.9；不适用项不硬造。 |
| U7 | 分支 `feat/v0.2-p6`，完成后合回 `feat/v0.2`。 |

### 0.5 文档漂移/冲突记录（不自行改设计）

1. `phase-06` 引用的决策日志 `D54–D58` **在 0.2 decision-log 中不存在**（止于 D53）；它们是 0.1 P8 编号。P6 按 0.2 权威 + 0.1 参考重建（P5 handoff §7.1 已警告）。
2. HLD §1.4 声称 P1 已交付 `/api/agents/*`、`/api/agent-center/*`；**实际 0.2 无这些路由**。P6 按“缺失”新建。
3. `phase-06` 的“配置”与 `implementation-phase-plan.md` P6 的“目录/运行/质量”不一致；按 phase-06 + HLD + 0.1 D60 取“含配置”。
4. HLD 的 `/quality` 全局入口、0.1 质量中心 admin-only、P5 视觉指标登录可见三者权限冲突；本 spec 裁决见 §5.1/P6-D-08。
5. 0.1 P8 评测 `detailed-design §4.12` 与实际 DDL 不一致；以 0.1 migration/实现为准（`eval_metric` 无 baseline 列，baseline 放 `detail`）。
6. 0.1 契约 camelCase / `Idempotency-Key` / 拒绝未知字段与 0.2 snake_case / 无通用幂等头 / 未知字段忽略冲突；P6 一律按 0.2 约定。
7. 0.2 无 `reviewing`/`gate`/`agent_session`/`mindmap case_node`/版本内知识；P6 待办、Agent 中心、全局中心、评测按 0.2 模型重定义，不恢复 0.1 旧控制面。
8. ARTEX 的变量/模板/预览调研结论为“0.2 无模板层，建议 na/defer”；用户 U5 明确要求纳入，本 spec 按“受控变量层”设计，风险见 §17。

---

## 1. 目标、范围与退出标准

### 1.1 目标

补齐 0.1 P0–P8 在 0.2 中尚未落地的能力：质量/评测、全局搜索、待办中心、Agent 中心（含提示词运行态定制）、全局问题/报告中心、探索链路 v2 增强；让 0.1 能力矩阵在 0.2 逐项有入口、有验收证据。

### 1.2 In

1. **quality 上下文**：`eval_run`/`eval_metric`（#32）+ 6 评测指标 + 视觉指标 + 7 运营指标；`/quality` 质量中心；0.2 评测语料/基线/runner 双轨。
2. **search 上下文**：`GET /api/search`（5 类实体、Provider 聚合、降级）+ Topbar CommandPalette。
3. **workspace 上下文**：`GET /api/todos`（确认/设计/接管/人工请求/dead）+ 全局 Topbar TodoDrawer。
4. **Agent 中心**：`/api/agent-center/{catalog,runs,quality}`（登录只读）+ `/api/agents/profiles*`、`/api/agents/tools`（admin 配置）+ `/agents` 页面；提示词运行态定制（版本/变量/预览/diff/恢复/审计/热生效）。
5. **全局中心**：`GET /api/issues`、`GET /api/reports` + `/issues`、`/reports` 页面（跨版本只读）。
6. **探索链路 v2**：增量过滤/counts/typed meta/latest-attempt/降级/重连/回放细化/性能预算；修 `chain.*` 热加载与 `replay_event_limit` 偏差。
7. **能力对齐矩阵**：P0–P8 逐项 0.2 入口/验收证据（P9 项标 P7）；ARTEX 提示词能力矩阵（include/defer/na）。
8. **契约/迁移/测试/验收**：openapi/db-schema/migrations-order/settings/agent-profiles-tools 冻结；P6 契约门禁；真实 LLM + Chrome 验收；E2E/截图；handoff。

### 1.3 Out（记录到 handoff/backlog，不实现）

- P9 五项：报告 PDF/模板、跨版本知识、GC、多实例、问题重开/合并/跨波次去重增强。
- `/knowledge` 页面/API（保持占位；P7）。
- 链路任意时点 as-of、链路导出；0.1 10 Tab/评审页/旧控制面；自由聊天 Agent 控制面。
- ARTEX `defer/na` 项：自定义 agent CRUD、触发器/run-merge、MCP/Skill、工具描述/schema 覆盖、`enabled` 开关、wrapup/收尾提示词、任务级超时收尾、profile 变更事件、跨 LLM 配置绑定、effective system 预览、token 估算、多租户（详见 §8.9）。
- P4/P5 已移交的其他项：obsolete 写路径、live HTTP probe BFS、大图渲染；`code.analysis_finished.candidates=0` 的消费（改用 `code.impact_computed`/impact API）。

### 1.4 退出标准（逐条可验证）

1. 32→33 迁移可重放；`eval_run`/`eval_metric`/`agent_profile_prompt_revision` 存在，约束/索引正确。
2. `/api/search` 返回 5 类分组、每类上限、`degraded`；CommandPalette 可用。
3. `/api/todos` 覆盖 6 个 kind、counts pre-limit、权限过滤、30s 轮询。
4. `/quality` 登录可见运营+视觉指标；admin 可见评测看板/阈值/基线/历史；评测 API 服务端派生、拒绝客户端 verdict。
5. `/agents` 目录/运行/质量；普通用户只读且无 prompt；admin 可编辑 profile 配置与提示词（版本/变量/预览/diff/恢复），保存后下一次 run 生效。
6. `/issues`、`/reports` 跨版本只读列表/筛选/游标/深链；只读边界。
7. 链路 v2 新筛选/counts/meta/降级/重连/回放/性能可验证；`chain.max_*` 热加载、`replay_event_limit` 生效。
8. 能力矩阵逐项有 0.2 入口与证据；ARTEX 矩阵 include 项全部实现，defer/na 有理由。
9. `scripts/test.ps1`、Go/Frontend/executor、P2–P6 契约门禁全绿；真实 LLM + Chrome 验收与浏览器 E2E PASS；截图/日志证据落盘。
10. `phase-06-handoff.md` 完成；contracts `frozen: P6`；合回 `feat/v0.2`；不自动进入 P7。

### 1.5 演示路径

管理员登录 → 配置资源/版本 → 澄清/设计/确认/执行（P5 真实数据或 P6 验收流程）→ 质量中心（运营/视觉/评测）→ Agent 中心（目录/运行/质量/编辑提示词并验证下一 run）→ 全局问题/报告 → 版本链路 v2 → 搜索/待办/全局中心；普通用户只读路径对照。

---

## 2. 实施策略、模块边界与仓库约束

### 2.1 新上下文/模块（按 Agent.md 四层）

| 模块 | 目录 | 职责 |
|---|---|---|
| quality | `backend/internal/quality/{domain,application,infrastructure,interfaces/httpapi}` | eval_run/eval_metric；质量总览/评测指标/服务端派生/阈值/基线；质量中心 API |
| search | `backend/internal/search/{...}` | 跨实体只读搜索；Provider 端口 + 聚合；无表 |
| workspace | `backend/internal/workspace/{...}` | 待办只读聚合；无表 |
| agent | 既有 `backend/internal/agent/` 增量 | Agent 中心只读投影 + profile 配置/提示词版本/变量/渲染/预览/审计 |
| delivery | 既有 `backend/internal/delivery/` 增量 | 全局 issue/report 只读查询 |
| chain | 既有 `backend/internal/chain/` 增量 | v2 过滤/counts/meta/降级/性能 |
| visual | 既有 `backend/internal/visual/` 消费 | `/api/quality/visual-metrics` 保留，不改语义 |

前端新 feature：`frontend/src/features/quality/`、`search/`、`todos/`（或 `shell/` 内）、`agent-center/`；全局问题/报告复用 `features/delivery/`；链路复用 `features/chain/`。`src/app/(app)/{quality,agents,issues,reports}/page.tsx` 只做路由组装。

### 2.2 依赖方向（强制）

- domain 只依赖标准库 + platform 纯类型；application 不依赖 infrastructure/interfaces；infrastructure 实现 domain/application 端口；interfaces 只调 application。
- 跨上下文只走对方 application 接口或事件；**禁止直接 SQL join 他域表**。search/workspace/quality 的 Provider 以 application 端口实现；必要时给拥有方 application 增加只读查询方法。
- delivery 全局查询在本上下文 infrastructure 内 SQL；version_title 通过 version application 端口批量获取。
- 不新建 `utils/common/helpers`；共享纯逻辑放 `internal/platform/`。

### 2.3 共享文件与写作用域

- Lead 统一收口：`docs/version-V0.2/contracts/**`、`backend/internal/server/server.go`、`backend/cmd/eztest/**`、`backend/internal/db/migrations/**`、`frontend/src/navigation/**`、`frontend/src/features/shell/Topbar.tsx`、`frontend/src/app/(app)/**` 路由、`scripts/**` 契约/验收脚本。
- 并行流只改自己的目录；需要共享文件时提交“接线请求”给 Lead，由 Lead 串行合并。
- 禁止子代理并发改 contracts/migrations/openapi/settings。

### 2.4 契约先行顺序

1. 本 spec 批准 → writing-plans。
2. 更新 contracts（openapi/db-schema/migrations-order/settings/agent-profiles-tools/README）并冻结 P6 增量（实现前可先标 draft，最终翻 frozen）。
3. 迁移 #32/#33 + 设置键 + 最小上下文骨架 + 路由 wiring。
4. 并行实现 6 条能力流（§15）。
5. 验收/E2E/矩阵/证据 → 契约冻结 P6 → handoff → 合并。

### 2.5 真实 LLM 原则（全阶段强制）

- 真实 LLM 失败按系统缺陷定位：上下文包/工具 scope/schema/提示词歧义/服务端校验/超时预算；修复后重跑。
- 不得以 fake 替代真实 LLM 验收；fake 仅用于确定性测试/E2E 准备。
- 工具调用抖动：验收脚本要有重试/最终器/可观测（日志、run id、system message 证据）。
- 验收环境：隔离库 + 独立端口，不碰开发栈（13333/6868/13336/5433）。

### 2.6 0.1 → 0.2 语义适配（实现必须遵守）

| 0.1 假设 | 0.2 事实 |
|---|---|
| 8 状态版本 | `initial/running/completed`（三状态投影） |
| batch | `run_wave` + `execution_intent`；单版本单活跃波 |
| mindmap `case_node` | `scenario` → `case_entity` → `case_version`（current+confirmed） |
| report 单 revision | `report` + `report_revision`（HTML 权威，current revision） |
| issue 三态 | `open/confirmed/dismissed/merged` + reopen |
| agent_session | `agent_run` + `work_item` + `team_room`（无 session） |
| 13 角色 | 14 个固定 profile + 6 个 team（code 无 profile） |

---

## 3. 数据模型与迁移

### 3.1 通用规则

- 迁移只追加，集中在 `backend/internal/db/migrations/`，命名 `<context>_<NNNN>_<desc>.sql`；`order.txt` 追加；不修改已应用迁移。
- 新表主键 `id BIGINT`（雪花，应用生成，不 BIGSERIAL）；审计列 `created_at`/`updated_at` 遵循 D34（新表默认 `now()`）。
- 同上下文 FK 可用；跨上下文不建 FK，只逻辑引用 BIGINT。
- 表名/索引名遵循 D34：`pk_/uk_/idx_`；字段 snake_case；bool `is_` 前缀（本 spec 新增表无 bool 列）。

### 3.2 `quality_0001_eval.sql`（order #32，quality 上下文）

```sql
CREATE TABLE eval_run (
    id             BIGINT NOT NULL,
    mode           TEXT NOT NULL CHECK (mode IN ('deterministic','real_llm')),
    corpus_version TEXT NOT NULL CHECK (corpus_version <> ''),
    model          TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL CHECK (status IN ('running','passed','failed','error')),
    summary        JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(summary) = 'object'),
    started_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at    TIMESTAMPTZ NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_eval_run PRIMARY KEY (id)
);
CREATE INDEX idx_eval_run_created_at ON eval_run (created_at DESC, id DESC);

CREATE TABLE eval_metric (
    run_id     BIGINT NOT NULL,
    name       TEXT NOT NULL CHECK (name <> ''),
    value      NUMERIC(8,5) NOT NULL,
    threshold  NUMERIC(8,5) NOT NULL,
    passed     BOOLEAN NOT NULL,
    detail     JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(detail) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_eval_metric PRIMARY KEY (run_id, name),
    CONSTRAINT fk_eval_metric_run FOREIGN KEY (run_id) REFERENCES eval_run(id) ON DELETE CASCADE
);
CREATE INDEX idx_eval_metric_run ON eval_metric (run_id, name);
```

- 不新增 `baseline` 列：baseline 值写 `detail.baseline`（与 0.1 DDL 一致；0.1 detailed-design 的 baseline 列与 DDL 不一致，以 DDL 为准）。
- `eval_metric.name` 不在 DB 硬编码枚举；服务端只接受 §5.3 的指标名。
- 无事件/触发器；写操作审计走 `audit_log`（§8/§5）。

### 3.3 `agent_0005_profile_prompt_revision.sql`（order #33，agent 上下文）

```sql
CREATE TABLE agent_profile_prompt_revision (
    id           BIGINT PRIMARY KEY,
    profile_key  TEXT NOT NULL REFERENCES agent_profile(key) ON DELETE CASCADE,
    version      INT NOT NULL CHECK (version > 0),
    system_prompt TEXT NOT NULL CHECK (system_prompt <> ''),
    note         TEXT NULL,
    source       TEXT NOT NULL CHECK (source IN ('seed','admin','reset')),
    actor_id     BIGINT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uk_agent_profile_prompt_revision UNIQUE (profile_key, version)
);
CREATE INDEX idx_agent_profile_prompt_revision_key
    ON agent_profile_prompt_revision (profile_key, version DESC);
```

- **不新增** `agent_profile.current_prompt_id`/`prompt_version`：`agent_profile.system_prompt` 仍是运行时唯一“当前真源”，读取路径零改动；当前版本 = `max(version)`。
- 回填：启动 `EnsureProfiles` 后，对无 revision 的 profile 插入 `v1`，内容=当时 DB `system_prompt`，`source=seed`，幂等、不覆盖。
- reset 追加 `source=reset` 的默认正文版本；restore 追加 `source=admin`、note=`restore vN` 的旧正文版本；不修改历史行。

### 3.4 既有表使用/索引

- `agent_profile`：复用；PATCH 显式 `updated_at=now()`（无 trigger）。
- `agent_run`：Agent 中心运行观察；可选索引 `idx_agent_run_profile_key_id (profile_key, id DESC)` 仅在集成性能测试证明需要时追加——P6 默认**不加**（14 profile，数据量小）。
- `issue`/`report`/`report_revision`/`version`：全局中心只读，不新增表；全局列表按 id DESC 走 PK。
- `eval_run`/`eval_metric` 与 `agent_profile_prompt_revision` 是 P6 唯一新表。

### 3.5 命名/结构自检

- 迁移文件只属一个上下文；`quality_0001_eval.sql` 属 quality，`agent_0005_profile_prompt_revision.sql` 属 agent。
- `order.txt` 追加顺序 #32 quality、#33 agent；不得重排历史。
- 断言：`migrate_test.go` 的迁移计数 31→33；`scripts/p5-contract-check.cjs` 硬编码 31/162/12 改为前向兼容（只验增量/标记）。

---

## 4. 契约：API / 事件 / 设置 / 任务 / 工具

### 4.1 通用约定

- 成功 `{"data": ...}`；列表 `{"data": [...], "next_cursor": "...", "has_more": bool}`；错误 `{"error":{code,message,request_id,retryable,details}}`。
- 业务 ID 十进制字符串；时间 RFC3339 UTC；请求/响应 snake_case；未知请求字段默认忽略（0.2 约定）。
- 分页：列表用 `before`（上一页最后 ID）+ `limit`（默认 20，上限 100）；搜索/待办为快照，不用 cursor；eval-runs 用 `limit`（1–50 默认 20，无 cursor）。
- 权限：Bearer；admin 面统一 `RequireAdmin`（403 `forbidden`）；登录只读面 `RequireAuth`。
- 错误码：400 `invalid_argument`、403 `forbidden`、404 `not_found`、409 `state_conflict`、413 `evidence_too_large`（不新增）、503 `provider_unavailable`/`ai_unavailable`；本 spec 新增 `profile_disabled` 不实现（enabled 不暴露）。
- 不实现通用 `Idempotency-Key`；重复 POST eval-run 允许（以 run id 区分）；PATCH/续写用 `expected_version` 可选乐观锁。

### 4.2 API 总表（P6 增量，frozen: P6）

| # | Method + Path | 权限 | 说明 |
|---|---|---|---|
| 1 | `GET /api/search` | 登录 | 跨实体搜索 |
| 2 | `GET /api/todos` | 登录 | 待办快照 |
| 3 | `GET /api/quality/eval-runs` | admin | 评测 run 列表 |
| 4 | `POST /api/quality/eval-runs` | admin | 记录终态评测 run（服务端派生） |
| 5 | `GET /api/quality/eval-runs/{id}` | admin | 单 run |
| 6 | `GET /api/quality/overview` | 登录 | 运营指标 + 最新评测摘要 |
| 7 | `GET /api/issues` | 登录 | 全局问题只读 |
| 8 | `GET /api/reports` | 登录 | 全局报告只读 |
| 9 | `GET /api/agent-center/catalog` | 登录 | Agent 目录/统计（无 prompt） |
| 10 | `GET /api/agent-center/runs` | 登录 | 运行观察（无 transcript） |
| 11 | `GET /api/agent-center/quality` | 登录 | 最新评测摘要 + 运行统计 |
| 12 | `GET /api/agents/profiles` | admin | profile 全量（含 prompt） |
| 13 | `GET /api/agents/profiles/{key}` | admin | profile 详情 + default prompt + variables + current version |
| 14 | `PATCH /api/agents/profiles/{key}` | admin | model/budgets/allowed_tools 配置 |
| 15 | `PUT /api/agents/profiles/{key}/prompt` | admin | 追加提示词版本 |
| 16 | `POST /api/agents/profiles/{key}/prompt/reset` | admin | 追加默认版本 |
| 17 | `POST /api/agents/profiles/{key}/prompt/restore` | admin | 追加指定旧版本 |
| 18 | `GET /api/agents/profiles/{key}/prompt/revisions` | admin | 版本文本列表 |
| 19 | `POST /api/agents/profiles/{key}/prompt/preview` | admin | 变量示例渲染预览 |
| 20 | `GET /api/agents/tools` | admin | 工具注册表目录 |
| 21 | `GET /api/versions/{versionId}/exploration-chain` | 登录 | 既有端点增量参数/字段 |
| 22 | `GET /api/quality/visual-metrics` | 登录 | 既有 P5 端点，**不改语义** |

> 路径裁决：Agent 配置沿用 HLD §7.1/0.1 的 `/api/agents/*`（admin guard），不采用 `/api/admin/agent-profiles`；只保留一套路径。读模型走 `/api/agent-center/*`。

### 4.3 关键 DTO/语义

#### 4.3.1 搜索

```
GET /api/search?q=<1..100>&types=version,requirement,case,issue,report&limit=<1..20,default 5>
```
- `q` trim 后必填 1–100；空/超长 400 `invalid_argument`（details.field=q）。
- `types` 省略=全部 5 类；未知值 400（details.field=types）。
- `limit` 为**每类型**上限；总上限 5×20=100。
- 响应：
```json
{"data":{
  "groups":[{"type":"version","items":[{"id":"...","title":"...","subtitle":"...",
    "version_id":"...","version_title":"...","status":"running",
    "url":"/versions/...?tab=overview"}]}],
  "total":12,"truncated":true,"degraded":false
}}
```
- `SearchItem`：`id,title,subtitle,version_id,version_title,status,url`；`url` 由服务端按 0.2 8 Tab 生成。
- 类型映射：
  - `version`：`version.title`；url `?tab=overview`。
  - `requirement`：当前 `requirement_version.title`；`id`=requirement id；url `?tab=requirements&requirement=<id>`。
  - `case`：`case_entity.current_version_id` 的 `case_version.title`；`id`=case_entity id；url `?tab=design&case=<case id>`。
  - `issue`：`issue.title`；url `?tab=issues&issue=<id>`。
  - `report`：`report`（kind + status）；title=`波次报告`/`版本报告`；url `?tab=reports&report=<id>`（代码 tab 值为 `reports`）。
- 匹配：`ILIKE '%' || q || '%'` on title/name；按 `updated_at DESC, id DESC`；每类 1 条 SQL（总 ≤5）。
- 降级：任一 Provider 失败 → 该组空 + `degraded=true`，健康组返回；全失败 → 200 空 groups + degraded=true（不 5xx）。
- 不含知识/证据正文/凭据/资源；不返回 prompt/transcript。

#### 4.3.2 待办

```
GET /api/todos?limit=<1..200,default 50>
```
- 响应：`{"data":{"items":[TodoItem],"counts":{...}}}`；counts 是 **limit 前** 的按 kind 计数；items 受 limit 截断。
- `TodoItem`：`kind,title,version_id,version_title,severity,created_at,expires_at?,url`。
- `kind` 枚举与来源：
  1. `requirement_confirmation`：`requirement.status='draft'`（当前版本待用户确认）；全员；severity=warning；url `?tab=requirements&requirement=<id>`。
  2. `case_confirmation`：`case_entity.status='draft'`；全员；warning；url `?tab=design&case=<case id>`。
  3. `issue_confirmation`：`issue.status='open'`；全员；severity 映射 `major→major`、`minor→warning`、`trivial→info`；url `?tab=issues&issue=<id>`。
  4. `design_pending`：`version.pending_design_count>0`（每个版本一条，title=`N 条待设计`）；全员；warning；url `?tab=design`。
  5. `human_request`：`human_request.status='open'`；`kind=login_takeover` **仅 admin 返回**；其余 kind（conflict/other）全员；severity=warning；url `?tab=execution`。
  6. `dead_work_item`：`work_item.status='dead'`；**仅 admin**；severity=major；url `?tab=execution`。
- counts 含全部 6 个 key；非 admin 的 admin-only 组计数为 0。
- 排序：`human_request`（takeover 优先）→ `design_pending` → `issue_confirmation` → `requirement_confirmation` → `case_confirmation` → `dead_work_item`；组内 `created_at DESC, id DESC`。
- 只读；30s 轮询 + 路由切换刷新（前端）；不在 Drawer 内 resolve。
- 非 admin 不查询 execution 的 human_request/dead 组（服务端过滤，避免泄露）。

#### 4.3.3 质量/评测

```
GET /api/quality/eval-runs?limit=<1..50,default 20>
POST /api/quality/eval-runs
GET /api/quality/eval-runs/{id}
GET /api/quality/overview?version_id=<optional>
```
- `POST` body：
```json
{"mode":"deterministic|real_llm","corpus_version":"v02-2026-11-01.1",
 "model":"deepseek-flash","status":"error?","sample":{...},"baseline":{...},
 "summary":{},"started_at":"...","finished_at":"..."}
```
- `sample`（snake_case，11+2 可选计数）：`confirmed_requirements,covered_requirements,impact_expected,impact_hit,impact_selected,injected_failures,correct_classifications,ai_issues,true_positive_issues,supplement_cases,supplement_hits,visual_injected?,visual_correct?`。
- 服务端校验：全部非负；`covered<=confirmed`、`impact_hit<=impact_expected`、`impact_hit<=impact_selected`、`correct<=injected`、`true_positive<=ai_issues`、`supplement_hits<=supplement_cases`、`visual_correct<=visual_injected`；违反 400 `invalid_argument`（details.field）。
- 服务端派生 6+1 指标（§5.3）；`threshold` 由服务端按 `mode` 选择；`baseline` 只用于 deterministic 回归（>0.05 → regressed）；`status` 除 `error` 外由指标派生（全 passed→passed，否则 failed）；客户端传 passed/failed 被忽略。
- 响应 `EvalRunView`：`id,mode,corpus_version,model,status,summary,metrics[{name,value,threshold,passed,detail{numerator,denominator,threshold_passed,baseline?,regressed,reason}}],started_at,finished_at,created_at`。
- `GET /api/quality/overview`：`{data:{scope:{version_id|null},generated_at,operational:[OperationalMetric],latest_eval:EvalSummary|null}}`；`OperationalMetric{name,value|null,numerator,denominator,available,definition}`；7 项见 §5.2。
- 权限：eval-runs 全 admin；overview 登录；`visual-metrics` 登录（P5 不变）。普通用户不看评测 run 列表/样本。

#### 4.3.4 Agent 中心与 profile

- `GET /api/agent-center/catalog`（登录）：
```json
{"data":{"profiles":[{"profile_key":"clarification_host","name":"澄清 Host","team":"clarification",
 "order":1,"mode":"agent","model":null,"enabled":true,
 "budgets":{"max_turns":12,"soft_seconds":300,"hard_seconds":600},
 "allowed_tools_count":9,"prompt_version":1,"prompt_updated_at":"...","updated_at":"...",
 "stats":{"run_count":3,"done_count":2,"failed_count":1,"aborted_count":0,
 "interrupted_count":0,"success_rate":0.66667,"last_run_at":"..."}}],
 "teams":[{"team":"clarification","label":"澄清","profile_keys":[...]},
          {"team":"code","label":"代码","profile_keys":[],"note":"确定性 worker，无 agent profile"}]}}
```
- `CatalogProfile` **永不含** `system_prompt`/`transcript`/工具参数正文/凭据。
- stats：`run_count`=全部 agent_run；`success_rate=done/(done+failed)`（0 分母→0；aborted/interrupted 不进分母）；`last_run_at=max(started_at)`。
- `GET /api/agent-center/runs?profile_key=&status=&version_id=&before=&limit=`（登录）：列表 envelope；`AgentRunView{id,profile_key,work_item_id,work_type,version_id,version_title?,conversation_id,status,terminal_reason,turns,usage,started_at,finished_at}`；按 id DESC；status 枚举 running/done/failed/aborted/interrupted；非法 400；不返回 message/transcript。
- `GET /api/agent-center/quality`（登录）：`{data:{latest:EvalSummary|null,run_stats:{run_count,done_count,failed_count,aborted_count,interrupted_count,success_rate,last_run_at}}}`；latest 来自最近 eval_run（id/ mode/status/created_at/metrics 计数/passed）；无 run → null。
- `GET /api/agents/profiles`（admin）：`{data:[ProfileView]}`；`ProfileView{id,key,name,system_prompt,allowed_tools,model,budgets,enabled,prompt_version,created_at,updated_at}`。
- `GET /api/agents/profiles/{key}`（admin）：`{data:{profile:ProfileView,default_system_prompt,variables:[PromptVariable],prompt_version}}`；`PromptVariable{name,description,example,source}`；`source ∈ runtime`。
- `PATCH /api/agents/profiles/{key}`（admin）：body 可选 `model`、`budgets`、`allowed_tools`；不改 prompt；校验；审计；返回 `{data:ProfileView,changed}`；404/400/403。
- `PUT /api/agents/profiles/{key}/prompt`（admin）：body `{system_prompt,note?,expected_version?}`；校验（非空/≤32768 runes/含 `[profile:<key>]`/模板语法/变量白名单）；事务：`SELECT ... FOR UPDATE` → `max(version)+1` → insert revision(source=admin) → UPDATE agent_profile.system_prompt+updated_at；no-op（内容相同）不插版本返回 `changed:false`；`expected_version` 不匹配 409 `state_conflict`；返回 `{data:{profile,version,changed}}`。
- `POST /api/agents/profiles/{key}/prompt/reset`（admin）：追加默认正文（`Profiles()` seed 同源）source=reset；返回同上。
- `POST /api/agents/profiles/{key}/prompt/restore`（admin）：body `{version}`；校验该版本属于该 profile；追加该正文 source=admin、note=`restore vN`；不修改历史；返回同上。
- `GET /api/agents/profiles/{key}/prompt/revisions?limit=20&before_version=`（admin）：按 version DESC；`RevisionView{version,system_prompt,note,source,actor_id,created_at}`；`next_cursor`=上一页最后 version（字符串），`has_more`。
- `POST /api/agents/profiles/{key}/prompt/preview`（admin）：body `{system_prompt?,sample?}`；空 template=当前正文；用变量示例值 + sample 覆盖渲染；语法/未知变量 400 `invalid_argument`（details.field=system_prompt）；返回 `{data:{rendered}}`。
- `GET /api/agents/tools`（admin）：`{data:[{name,description,parameters}]}`（来自 P5 registry；不执行工具）。
- `enabled`、`name`、`system_prompt` 不在 PATCH 中可写（prompt 走版本端点；enabled/name defer）。
- 普通用户不返回 prompt/transcript；catalog 不含正文。

#### 4.3.5 全局问题/报告

```
GET /api/issues?version_id=&status=&severity=&category=&q=&before=&limit=
GET /api/reports?version_id=&status=&kind=&before=&limit=
```
- issues：`status ∈ open/confirmed/dismissed/merged`；`severity ∈ major/minor/trivial`；`category ∈ ui/api/db`；`q` 1–100（title ILIKE）；`before` 上一页最后 id；`limit` 1–100 默认 20；按 id DESC。
- 响应 `{data:[GlobalIssueView],next_cursor,has_more}`；`GlobalIssueView` = IssueView 字段 + `version_title` + `url`（`/versions/{vid}?tab=issues&issue={id}`）。
- reports：`status ∈ draft/published/stale`；`kind ∈ wave/version`；按 id DESC；`GlobalReportView` = ReportView + `version_title` + `url`（`/versions/{vid}?tab=reports&report={id}`）；含 current_revision；不返回 content/内部路径。
- 只读；详情复用版本内接口；无写/导出/批量。

#### 4.3.6 链路 v2（既有端点增量）

`GET /api/versions/{versionId}/exploration-chain` 新增 query：
- `q`（≤100，需求/场景/用例标题模糊）；
- `abnormal_only`（bool，默认 false；仅 failed/blocked/inconclusive/interrupted）；
- `issue_status`（open/confirmed/dismissed/merged）；
- `issue_severity`（major/minor/trivial）；
- `requirement_limit`（1–500；缺省用 settings `chain.max_requirements`）。
既有 `status/result` 枚举在 handler 补校验（非法 400 `invalid_argument`）。

响应增量（`ChainView`）：
- `counts.by_case_run_status{passed,failed,blocked,inconclusive,skipped,interrupted}`；`counts.by_issue_severity`；`counts.by_issue_status`；`counts.coverage{requirements,covered,uncovered}`。
- `nodes[].meta` 类型化可选字段：requirement `uncovered,case_count,executed_count,failed_count`；scenario `requirement_id,case_count`；case_version `scenario_id,step_count,assertion_count,stale`；wave `wave_status,trigger`；case_run `attempt,failure_class,evidence_count,duration_ms`；issue `severity,issue_status,category,evidence_count,code_ref_count,merged_into_id`；report `report_kind,report_status,revision`；closure `closure_status`。保留 `additionalProperties:true` 兼容。
- `degraded_layers[]`：`requirement|design|execution|delivery|codeintel`（Provider 失败时）；健康层返回；全失败 503 `provider_unavailable`。
- `truncated_layers{requirements,cases,case_runs,issues,reports}`：布尔；`truncation` 保留既有 max/reason。
- `replay.event_limit` + `replay.truncated`（消费 `chain.replay_event_limit`）。

语义冻结：
- 每 case_version 只返回**最新 case_run**：按 `(wave_id DESC, id DESC)` 取最新（同一波次内以 id 最大）；无 case_run 不生成 case_run 节点。
- 过滤作用于节点并同步裁剪/保留诱导边；`focus_node` 及其祖先永不丢。
- 版本不存在 404；空版本 200 空图；wave 不属于版本 404；非法参数 400。
- Provider 失败保留客户端旧图；`degraded_layers` 提示；不显示空态。
- 查询预算 ≤10 SQL；API P95 ≤800ms（200 节点）；画布可交互 P95 ≤2.5s（200 节点/400 边）。
- `chain.max_nodes/max_edges/max_requirements` 从 settings **每请求/热缓存**读取；PATCH settings 后下一次请求生效。
- `chain.replay_event_limit` 用于历史回看读取 `/api/events` 的上限；超出 `replay.truncated=true`。

### 4.4 事件

- **不新增事件类型**。search/todos/quality/agent-center 读模型用轮询/手动刷新；chain 复用 `version:{id}` 既有事件；prompt/config 变更走审计，不发 SSE。
- 若实现中发现必须新增事件，停止并报告（破坏性契约变更），不自行加。

### 4.5 设置（69 → 72）

| key | type | default | 范围 | 说明 |
|---|---|---|---|---|
| `search.max_results_per_type` | number | 5 | 1–20 | 搜索每类型上限默认值（查询参数可覆盖） |
| `todos.max_items` | number | 50 | 1–200 | 待办默认上限（查询参数可覆盖） |
| `quality.eval_baseline_regression` | number | 0.05 | 0–1 | deterministic 基线回归容差 |

- 设置中心分类：search/workspace/quality；非 secret；热加载（读缓存失效）。
- 不新增 env；不新增 task type/profile/tool/executor 协议。

### 4.6 任务/profile/工具/executor

- 不新增 work_item type、team、profile、tool、executor 消息。
- Agent 提示词变量目录由代码定义，**不新增数据库表**；`agent-profiles-tools.md` 增加变量白名单/渲染/回退/长度/marker 规则。

### 4.7 OpenAPI 与冻结

- `info.version: 0.2.0-p6`，`x-frozen: P6`；新增 tags：`Search`、`Workspace`、`AgentCenter`；沿用 `Quality`、`Agents`、`Delivery`、`Chain`。
- 新增 **20 个 operation**（§4.2 #1–#20；#21 为既有 chain path 的参数/字段增量，#22 为既有 P5 visual 端点）；P0–P5 paths/schemas 语义不变，只加可选字段/新 path。
- P0–P5 paths/schemas 语义不变；只加可选字段/新 path。
- 契约门禁：新增 `scripts/p6-contract-check.cjs`；P2–P5 门禁前向兼容（不硬编码阶段总数/ops/events）。

---

## 5. 质量中心与评测

### 5.1 模块/权限/页面

- 新上下文 `internal/quality` 四层；`eval_run`/`eval_metric` 持久化；只读自己的表 + 通过 application 端口聚合其他上下文。
- `/quality` 页面：**登录可见**运营指标 + P5 视觉指标；**评测看板/run 列表/阈值/基线 admin-only**（P5 `visual-metrics` 语义不变）。非 admin 显示“评测看板仅管理员可见”，不请求 admin API。
- 质量中心不写业务表（除 eval_run/eval_metric）；指标服务端派生，不信任客户端 verdict。

### 5.2 运营指标（架构 §16，7 项）

`GET /api/quality/overview` 返回；`version_id` 可选（单版本）；每项 `{name,value|null,numerator,denominator,available,definition}`：

| name | 分子/分母 | 数据源 |
|---|---|---|
| `requirement_closure_rate` | closed requirements / (draft+clarified+closed，排除 obsolete) | version |
| `case_confirmation_rate` | current confirmed case_versions / current case_versions（draft+confirmed+stale） | design |
| `execution_pass_rate` | passed case_runs / (passed+failed+blocked+inconclusive) finished case_runs | execution |
| `issue_validity_rate` | confirmed / (confirmed+dismissed) issues（open/merged 排除） | delivery |
| `evidence_completeness_rate` | finished case_runs 有 ≥1 evidence / finished case_runs（同上四态） | execution |
| `context_compaction_count` | `context.compacted` 事件计数（版本频道或全局） | eventing |
| `task_failure_retry_rate` | attempts>1 的 work_items / attempts>=1 的 work_items | agent/work_item |

- 分母 0 → `available=false`、value=null（不显示 0 假数据）；`numerator/denominator` 仍返回。
- 定义写入契约；UI 显示口径 tooltip。

### 5.3 评测指标（6+1）

| name | 公式 | deterministic 阈值 | real_llm 阈值 | 方向 |
|---|---|---|---|---|
| `design_coverage` | covered_requirements/confirmed_requirements | 0.90 | 0.80 | ≥ |
| `impact_recall` | impact_hit/impact_expected | 0.90 | 0.70 | ≥ |
| `impact_precision` | impact_hit/impact_selected | 0.80 | 0.60 | ≥ |
| `classification_accuracy` | correct_classifications/injected_failures | 1.00 | 0.80 | ≥ |
| `issue_false_positive_rate` | (ai_issues-true_positive_issues)/ai_issues | 0.05 | 0.15 | ≤ |
| `supplement_hit_rate` | supplement_hits/supplement_cases | 0.80 | 0.50 | ≥ |
| `visual_classification_accuracy` | visual_correct/visual_injected（可选计数） | 1.00 | 0.80 | ≥ |

- 分母 0 → value=0（`impact_precision`/`issue_false_positive_rate` 分母 0 时 value=0 且 detail 标注 `zero_denominator=true`）；确定性语料保证门禁指标分母非 0。
- 浮点比较 epsilon `1e-9`；`reason ∈ ok|below_threshold|above_threshold|baseline_regression`。
- 回归：仅 deterministic；非 Max 指标 `value < baseline-0.05-eps` 或 Max 指标 `value > baseline+0.05+eps` → regressed=true/passed=false；real_llm 不应用回归。
- `passed = threshold_passed && !regressed`；run status = error（请求声明）或全部指标 passed→passed，否则 failed。
- `detail` 必含 numerator/denominator/threshold_passed/regressed/reason；有 baseline 时含 baseline。
- 服务端校验样本物理约束（§4.3.3）；拒绝客户端伪造 passed/阈值。

### 5.4 评测语料/基线与 runner

目录（重写 0.1 资产）：
- `eval/v02/corpus/`：environment、requirements、design、code、execution、issues、visual 的固定语料/真值。
- `eval/v02/ground-truth/`：impact.json、classification.json、issues.json、supplement.json、visual.json。
- `eval/v02/baseline.json`：corpus_version + 7 指标基线/阈值；基线只允许不下降更新。
- `eval/v02/README.md`：维护/更新/双轨说明。
- `scripts/eval.ps1` 重写为 0.2：
  - `-Mode deterministic`（默认）：独立 DB + 固定语料/种子数据 + 服务端派生；断言阈值/基线/回归；失败非零退出。**不依赖 fake LLM 驱动 P3/P5 agent**（0.2 fake LLM 未覆盖这些 profile；见 P6-D-12）。
  - `-Mode real-llm`：使用 P6 验收产生的真实 LLM 数据（或重跑验收流程），计算样本计数并 POST `mode=real_llm`；阈值仅报告，不阻断。
  - `-DryRun`：静态检查语料/基线/API/DB/工具链。
- 评测结果只经 admin API 写入，不直接写业务表；输出 `.tmp/p6-eval/{result,observation}.json`。
- **P6-D-12 说明**：0.1 P8 的“fake LLM 全流程评测”在 0.2 需扩展 fake LLM 的 P3/P5 profile（case_executor/failure_analyst/issue_triager/report_writer/version_steward/code worker）与 fixture executor；P6 先交付**确定性语料门禁 + 真实 LLM 观测**，全流程 fake LLM 评测列入 P8 backlog。若用户要求严格 0.1 全流程，需在 plan 前追加评估（见 §17 风险 R1）。

### 5.5 页面

- 页头：eyebrow/标题/“服务端派生”说明/admin badge/版本范围选择/刷新。
- 运营指标卡（7 项，available=false 显示“不可用”）。
- 视觉质量区：复用 P5 `QualityMetricsPanel`（visual-metrics 不变）。
- 评测看板（admin）：最近 run 列表（id/mode/status/时间/corpus）、指标表（值/阈值/基线/通过）、历史趋势表、基线卡、空态“尚未运行评测”。
- 错误/加载/拒绝态；不显示 0 假数据；指标行可展开 detail（分子/分母/reason）。

### 5.6 测试/验收

- domain：样本校验、公式、threshold/regression/reason/zero denominator、status 派生、拒绝客户端 verdict。
- integration：迁移幂等；POST/GET/权限；overview 指标口径；audit。
- runner：deterministic 门禁通过/失败/基线回归；real-llm 只报告。
- 前端：admin/非 admin、空/错/载、基线/阈值展示。
- 验收：S1/S7 场景（§12）。

---

## 6. 全局搜索

- 上下文 `internal/search`：application 聚合 + Provider 端口；无表、无事件。
- Provider：version（版本/需求）、design（场景/用例）、delivery（问题/报告）；每个 Provider 每类型 1 条 SQL 或 application 查询。
- 匹配/排序/limit/degraded 见 §4.3.1；不跨上下文直读表。
- 前端：Topbar 搜索框 + `Ctrl/Cmd+K`；300ms 防抖、请求取消、↑↓/Enter/Esc、最近搜索 localStorage 5 条、空/错/载、truncated/degraded 提示；结果按组、深链。
- 测试：q/types/limit 边界 400；5 组；降级；深链；快捷键；最近搜索；E2E 截图。

---

## 7. 待办中心

- 上下文 `internal/workspace`：只读聚合；无表、无事件。
- 数据源 Provider：version（pending_design_count/requirement draft）、design（case draft）、execution（human_request/work_item dead）、delivery（open issue）。
- 分组/权限/counts/排序见 §4.3.2；30s 轮询 + 路由刷新。
- 前端：Topbar TodoDrawer 升级为全局数据源；无版本也可打开；分组折叠/计数/条目深链/空错载；admin 额外组；不替换 P4 版本内行为，只扩展数据源与展示。
- 测试：6 kind、权限、counts pre-limit、deep link、30s 刷新、空态/错误、E2E 截图。

---

## 8. Agent 中心与提示词运行态定制

### 8.1 目录/运行/质量

- 14 个固定 profile（P1 3 + P2 5 + P3 3 + P4 3）；团队映射用 `teamForProfile`（clarification/design/execution/delivery/steward；code 无 profile）。
- catalog：profile 元数据 + stats + prompt_version/prompt_updated_at；不含 prompt/transcript/凭据。
- runs：`agent_run` join `work_item` 取 version/work_type；不返回 message/transcript；`before`+`limit`。
- quality：最新 eval 摘要 + 运行统计；无数据 null。
- 权限：登录只读；admin 配置。

### 8.2 profile 配置

- PATCH 可写：`model`（null/空=默认；非空必须命中默认 LLM 配置模型）、`budgets{max_turns,soft_seconds,hard_seconds}`（非负、soft≤hard、0=跟随全局）、`allowed_tools`（去重、⊆ P5 registry）。
- 不可写/不暴露：`enabled`（runner 未消费；defer）、`name`（defer）、`system_prompt`（走版本端点）、`key`。
- 写后下次 run 生效；当前 run 快照不变；显式 `updated_at=now()`；审计。

### 8.3 提示词版本/seed/回填

- `agent_profile.system_prompt` 为当前正文；`agent_profile_prompt_revision` append-only。
- 启动 seed：`EnsureProfiles` 后对无 revision 的 profile 插入 v1（内容=当前 DB 正文，source=seed）；不覆盖管理员编辑。
- 写路径：PUT prompt → `SELECT ... FOR UPDATE` → 校验 → version=max+1 → insert revision(source=admin) → UPDATE profile.system_prompt/updated_at（同一事务）；no-op 不插版本；`expected_version` 冲突 409。
- reset：追加默认正文（source=reset）；restore：追加旧版本正文（source=admin，note=restore vN）。
- 默认正文从 `Profiles()` seed 同源获取（`DefaultPrompt(key)`），不复制字符串。

### 8.4 变量目录/渲染/预览

- 变量目录（代码定义，admin API 返回；`source=runtime`）：
  - 所有 profile：`Now`（RFC3339 UTC）、`ProfileKey`、`ProfileName`、`VersionID`、`VersionTitle`、`VersionStatus`、`TaskType`、`TaskGoal`。
  - execution profiles（`case_executor` 等）：另加 `CaseRunID`、`WaveID`、`IntentID`、`CaseVersionID`、`CaseTitle`、`Priority`。
  - 变量值由 `ContextInput` 在 run 时生成；不可用字段为空串。
- 模板：Go `text/template`，`missingkey=error`；保存时 `validateTemplate`：语法 + 顶层 `{{.Var}}` ⊆ catalog；未知变量 400 `invalid_argument`（`details.field=system_prompt`）。
- 强制保留 `[profile:<key>]` 标记（缺失 400，`details.reason=missing_profile_marker`）；默认正文含该标记。
- 长度 1–32768 runes；非空。
- 运行期：`systemContent` 渲染正文后仍拼 `taskBlock`/`executionBlock`（代码固定，不可编辑）；渲染失败回退内置默认（再失败用原始默认），不半渲染、不失败 run。
- 预览：POST 用变量示例值 + 可选 sample 渲染；返回 rendered；无效 400。
- 不提供 `FuncMap`；不允许访问环境/IO。

### 8.5 API/权限/审计

- 见 §4.3.4；admin 写、登录读（catalog/runs/quality）；prompt 正文只在 admin API。
- 审计动作：`agent.profile.updated`（PATCH/reset/restore/PUT prompt 均记一条）；metadata 只含 `profile_key,fields[],prompt_version,prompt_sha256,prompt_length`，**不含正文**。
- 不新增事件；不新增设置键；不新增 profile/tool。
- 错误：400 `invalid_argument`（details.field/reason）、403、404、409 `state_conflict`（expected_version）。

### 8.6 UI

- `/agents`：标题/说明/admin 只读 badge/刷新；Tabs：目录、运行观察、质量、配置（admin）。
- 目录：按团队分组；14 profile；key/name/model/budgets/allowed_tools_count/prompt_version/updated_at/stats；普通用户只读、无 prompt/编辑。
- 运行：筛选 profile/status；表格（run/profile/version/status/turns/started/finished/terminal_reason/usage）；不显示 transcript。
- 质量：最新评测摘要 + run stats；空态“尚未运行评测”。
- 配置（admin）：profile 列表 + 编辑抽屉：
  - 提示词编辑器（等宽 textarea，`[profile:<key>]` 提示，字数/32768）；变量 chip 点击插入（tooltip 描述/示例）；预览按钮/弹窗；保存新版本（note 可选）；恢复默认；版本历史（version/source/actor/time/note；查看全文、与当前 diff、加载到编辑器/restore）；“保存后下一次 run 生效”。
  - 配置字段：model（默认配置模型下拉）、budgets 三输入、allowed_tools 多选（来自 tools 目录）；保存/错误内联；不显示 enabled/wrapup/triggers/MCP/Skill。
- 空/载/错/只读态；不复制 ARTEX 的 catch→null 合并；错误与 404 区分/可重试。

### 8.7 生效语义/回退/安全

- 下一次 run/新会话生效；进行中 run 不变（profile 每 run 现读）。
- 渲染失败回退默认；保存期校验降低失败面。
- admin 自担 prompt 行为变更；工具白名单仍由服务端强约束；审计可追溯。
- `context_packet.sections` 含 system 全文；Agent 中心 read model 不暴露；若未来观察面板展示，必须裁剪/脱敏。
- 注入风险已由用户接受（内网小团队），但仍不允许模板执行任意函数/IO；变量值服务端生成，不接受客户端传入运行期值。

### 8.8 测试/验收

- domain：prompt 校验（空/超长/缺 marker/未知变量/语法）、budgets/tools/model 校验、默认正文同源。
- 集成：迁移幂等；回填 v1；PUT/reset/restore/no-op/409；PATCH 下一 run 的 `agent_message(role=system)` 含新 prompt 与 marker；旧 run 不变；审计 metadata 无正文；权限。
- 前端：目录/运行/质量/配置；只读态；变量插入；预览；版本 diff/restore；错误 field；空态。
- E2E：admin 编辑 prompt（唯一 token）→ 下一次 fake/真实 run 的 system message 含 token；普通用户无 prompt/403；版本历史/恢复；截图。
- 验收：S4。

### 8.9 ARTEX 能力矩阵处置（include/defer/na）

| 能力 | 处置 | 说明 |
|---|---|---|
| 按 profile 编辑 system prompt（下一 run 生效） | include | 核心 |
| append-only 版本/当前/历史/查看 | include | `agent_profile_prompt_revision` |
| 版本 diff | include | 前端行级 |
| 恢复默认/版本恢复 | include | reset/restore 追加版本 |
| 模板变量目录/白名单/渲染/回退 | include | 受控变量层（U5） |
| 渲染预览（示例/sample） | include | admin API + UI |
| profile model/budgets/allowed_tools | include | PATCH + tools 目录 |
| 工具目录（名称/描述/参数） | include | registry 只读 |
| Agent 目录/运行观察/质量投影 | include | `/api/agent-center/*` |
| 权限/脱敏/审计/热生效 | include | admin-only；目录不含 prompt |
| 提示词长度/seed 不覆盖/下一 run 语义 | include | 契约/测试 |
| `enabled` 开关 | defer | runner 未消费；禁用后 work item 处置未定义 |
| profile 变更 SSE 事件 | defer | 审计+轮询足够；新增需冻结 |
| profile name 编辑 | defer | 无业务必要性 |
| 跨 LLM 配置绑定 | defer | 0.2 只支持模型名 |
| wrapup/收尾提示词（per-run/task-timeout） | defer | 0.2 无 settlement 阶段；不能只加字段 |
| effective system 预览（真实上下文） | defer | 依赖任务/版本，可能泄露；只做示例预览 |
| token/费用估算 | defer | UI 便利，非核心 |
| 自定义 agent CRUD/复制/导入导出 | na | 0.2 work type/会话 kind 固定路由 14 profile，无法调度新 profile |
| 触发器/run-merge/max_parallel | na | 0.2 是 work_item 租约队列，无触发器模型 |
| MCP/Skill 可见性 | na | 0.2 无对应运行时 |
| 工具描述/schema 覆盖 | na | 无工具表；代码 registry |
| web_search/interactive_shell | na | 无对应工具 |
| 多租户/按版本隔离 | na | 单组织；profile 全局 |
| transcript 暴露 | na | 0.1/0.2 脱敏红线 |

---

## 9. 全局问题/报告中心

- `internal/delivery` 增量：全局只读查询（跨版本）；version_title 通过 version application 端口获取；不建全局表。
- API/字段/过滤/分页见 §4.3.5；详情复用版本内接口；issue 支持 merged/reopen 展示；report 展示 current revision/HTML/status；不做写/导出/批量。
- 前端 `/issues`、`/reports`：替换占位页；筛选/表格/加载更多/空错载/深链；详情 Sheet 只读或“在版本中处理”深链；普通用户可读全部（单组织）。
- 测试/E2E：跨版本筛选、游标、merged/reopen、report revision/HTML 打开失败、深链、权限、截图。

---

## 10. 探索链路 v2

- 既有 P4 chain 基座增量，不重写 `BuildChain`/画布。
- 参数/字段/语义见 §4.3.6；新增 q/abnormal_only/issue_status/issue_severity/requirement_limit；counts/meta/degraded_layers/truncated_layers/replay 增量。
- 修复 P4 偏差：`chain.max_*` 热加载、`chain.replay_event_limit` 消费；补 status/result 枚举校验。
- 查询预算 ≤10 SQL；P95 ≤800ms API；200 节点/400 边可交互 ≤2.5s；慢查询日志。
- 前端：新增筛选（q 防抖 300ms、仅异常、问题状态/严重级）、counts/coverage、typed meta 展示、follow 自动居中（用户 pan/zoom 后暂停+恢复按钮）、列表视图、键盘/ARIA、分层错误/旧图保留/重连、按层截断横幅、1024 响应式；不退回 0.1 四节点。
- 测试：筛选/诱导边/focus 祖先/截断/counts/latest attempt/空版本 vs 404/degraded/热加载/replay_event_limit/性能/E2E/截图/axe（jsdom 下不强称全绿）。

---

## 11. 前端设计（汇总）

- 全局壳 8 项不变；搜索/待办在 Topbar；`/quality`、`/agents`、`/issues`、`/reports` 替换占位；`/knowledge` 保持占位。
- nav-items 移除 issues/reports/quality/agents 的 placeholder 标记/phase；knowledge 保留 placeholder。
- 页面/feature 目录：
  - `features/quality/QualityPage.tsx`（组合运营/视觉/评测；评测 admin）
  - `features/search/CommandPalette.tsx` + hook
  - `features/todos/TodoDrawer`（可并入 shell，建议 `features/shell/TodoDrawer` 扩展）
  - `features/agent-center/AgentCenterPage.tsx` + tabs/editor
  - `features/delivery/GlobalIssuesPage.tsx`、`GlobalReportsPage.tsx`
  - `features/chain/*` 增量
- 统一用 `api-client`/`sse-client`；禁止组件内 fetch/EventSource；URL 深链；空/错/载/重试；admin 权限；移动端 ≥1024 可用。
- 测试 testid 约定：`global-search-*`、`todo-*`、`agent-center-*`、`agent-prompt-*`、`global-issues-page`、`global-reports-page`、`quality-eval-*`、`chain-*`。

---

## 12. 验收与证据（真实 LLM + Chrome）

### 12.1 环境

- API：隔离库 `eztest_v02_p6`；端口与 P5 隔离（API 14345、executor 14347、fixture 13338，或另选空闲端口）；真实 `.env` DeepSeek `deepseek-flash`；真实 Chrome。
- 数据来源：优先 **复用 P5 验收真实数据**：先跑 `scripts/p5-acceptance.ps1`，停止 API；用 PostgreSQL `CREATE DATABASE eztest_v02_p6 TEMPLATE eztest_v02_p5` 克隆（无活动连接）；再启动 P6 API 应用 #32/#33 迁移/72 设置，运行 P6 断言。若模板克隆不可用，P6 验收脚本自行跑完整真实 LLM 闭环。
- 证据：`.tmp/p6-acceptance/*.log`、`scenarios.txt`、截图；提交 `docs/version-V0.2/acceptance/p6/{acceptance.md,capability-parity.md,artex-prompt-parity.md,screenshots/*}`。

### 12.2 场景（S0–S9）

- **S0 门禁**：33 migrations、72 settings、P2–P6 契约、真实 LLM 连通、executor active、fixture A/B。
- **S1 质量/评测**：admin POST deterministic sample → 7 指标/threshold/baseline/regression/status 派生；非法 sample 400；客户端 verdict 被忽略；权限（非 admin 403）；overview 7 运营指标口径；visual-metrics 不变；审计。
- **S2 搜索**：真实数据 5 组；per-type limit；truncated/degraded；q/types 400；深链；普通用户可用；E2E ⌘K。
- **S3 待办**：6 kind/计数/权限（takeover/dead admin-only）；draft 需求/用例、open issue、pending design、open human_request/dead；30s 轮询；深链。
- **S4 Agent 中心/提示词**：catalog 14/团队/stats；runs；quality；admin PATCH model/budgets/tools；PUT prompt（含变量/预览/版本/restore/reset/no-op/409）；下一次真实 LLM run 的 `agent_message(role=system)` 含新 prompt+marker；普通用户目录无 prompt、PATCH 403；审计无正文；Markdown/特殊字符安全。
- **S5 全局中心**：跨版本 issue/report；筛选/游标/深链；merged/reopen；report current revision/HTML；只读。
- **S6 链路 v2**：新筛选/counts/meta/truncation/degraded_layers；latest attempt；空版本 200/不存在 404；热加载；replay_event_limit；query budget/ P95。
- **S7 真实 LLM 评测观测**：`scripts/eval.ps1 -Mode real-llm` 使用 S1–S6 数据/验收流程；POST real_llm；阈值报告；observation JSON；不阻断。
- **S8 P5 回归**：visual/code 关键能力仍 PASS；P3/P4 关键 API 回归。
- **S9 全量门禁**：`scripts/test.ps1`、P2–P6 契约、前端/executor；E2E 截图；迁移幂等（重启 0 应用）。

### 12.3 浏览器 E2E（`scripts/p6-e2e.ps1` + `frontend/e2e/p6.spec.ts`）

- 管理员：质量评测看板；搜索；待办；Agent 目录/运行/质量/编辑提示词（保存/版本/预览）；全局问题/报告；链路筛选/follow/列表/截断。
- 普通用户：质量运营/视觉可见、评测拒绝；Agent 只读无 prompt；全局中心只读；待办无 admin 组。
- 截图至少：质量（admin/普通）、搜索、待办、Agent 目录/编辑/版本 diff、全局问题/报告、链路 v2；1280/1024 宽度；无控制台 error（允许既有 SSE 重连日志）。

### 12.4 证据规范

- 不得只写“已通过”；每个 S 场景给出命令、输出摘要、API/DB 断言、截图路径。
- 真实 LLM 失败先按系统缺陷定位并修复后重跑；不得以 fake 替代或把行为失败标“模型不稳定”跳过。
- 证据随 handoff 提交；`.tmp` 为本地运行产物，关键结论/截图入 `docs/version-V0.2/acceptance/p6/`。

---

## 13. 测试策略与门禁

- 命令：
  - `cd backend; ..\.tools\go\bin\go.exe build ./...; ..\.tools\go\bin\go.exe test -count=1 ./...`
  - 集成：`$env:EZTEST_TEST_DATABASE_URL=...; ..\.tools\go\bin\go.exe test -tags=integration -count=1 ./internal/quality/... ./internal/search/... ./internal/workspace/... ./internal/agent/... ./internal/delivery/... ./internal/chain/...`
  - `cd frontend; pnpm lint; pnpm typecheck; pnpm test`
  - `cd executor; pnpm test`
  - `powershell.exe -File scripts/test.ps1`（全量门禁）
  - `node scripts/p2-contract-check.cjs` … `node scripts/p6-contract-check.cjs`
  - `powershell.exe -File scripts/p6-acceptance.ps1`；`scripts/p6-e2e.ps1`
- 覆盖重点（phase plan §5 Review Focus 映射）：
  1. 契约漂移：P6 契约冻结、迁移顺序、P2–P5 门禁前向兼容。
  2. 多 SUT 身份混用：搜索/待办/全局中心/链路使用 `version_sut_id`；跨 SUT 数据展示。
  3. 上下文压缩丢事实：prompt 渲染/长度/回退；Agent 中心观察不暴露 system 全文。
  4. 执行幂等/重跑/收尾：P6 不改 P3 语义，回归证明。
  5. 旧假设回流：无 reviewing/gate/10 Tab/agent_session/版本内知识；矩阵审计。
- 每个任务必须给出验证命令与证据；TDD；小步提交。

---

## 14. 契约变更与冻结

| 文件 | 增量 |
|---|---|
| `contracts/openapi.yaml` | `0.2.0-p6`/`x-frozen: P6`；20 新 operation + chain v2 参数/字段；tags Search/Workspace/AgentCenter；P0–P5 语义不变 |
| `contracts/db-schema.md` | §14 quality（eval_run/eval_metric 逐列/约束/索引/不变量）；§15 agent prompt revision；说明无其他新表；`agent_profile.updated_at` 写路径 |
| `contracts/migrations-order.md` | §8/#32 quality_0001_eval、#33 agent_0005_profile_prompt_revision；`order.txt` 33 |
| `contracts/settings.md` | §4f 69→72（3 键，非 secret/热加载） |
| `contracts/agent-profiles-tools.md` | §9 P6：profile 配置/提示词版本/变量/渲染/回退/marker/长度/权限/审计/下一 run 语义 |
| `contracts/events.md` | 不变（明确无 P6 新事件） |
| `contracts/task-types.md` / `executor-protocol.md` | 不变 |
| `contracts/README.md` | P6 冻结状态/计数/证据 |

冻结规则：实现前 contracts 可标 draft；真实 LLM+Chrome 验收通过、全量门禁绿、P6 契约检查通过后翻 `frozen: P6`；P7 只能消费，破坏性变更需用户批准/决策记录。

---

## 15. 并行执行映射

### Wave 0（Lead 串行）

- spec/plan/自审；contracts 增量；迁移 #32/#33；settings 键；最小上下文骨架/wiring；nav/Topbar 共享点；P5 契约门禁前向兼容；基线测试。
- 产出：可编译骨架 + 契约 draft + 迁移可重放。

### Wave 1（并行，写作用域互斥）

| 流 | 写作用域 | 内容 |
|---|---|---|
| A 质量/评测 | `backend/internal/quality/**`、`frontend/src/features/quality/**`、`eval/v02/**`、`scripts/eval.ps1` | 上下文/表/API/指标/看板/语料/runner |
| B 搜索 | `backend/internal/search/**`、`frontend/src/features/search/**`、Topbar 搜索接线（Lead 收口） | API/Provider/CommandPalette |
| C 待办 | `backend/internal/workspace/**`、`frontend/src/features/shell/TodoDrawer*` | API/Provider/Drawer 全局化 |
| D Agent 中心/提示词 | `backend/internal/agent/{application,domain,infrastructure,interfaces}/**`、`frontend/src/features/agent-center/**` | catalog/runs/quality/profile/prompt/render/preview/UI |
| E 全局中心 | `backend/internal/delivery/{application,infrastructure,interfaces}/**`、`frontend/src/features/delivery/**` | `/api/issues`/`/api/reports` + 页面 |
| F 链路 v2 | `backend/internal/chain/**`、`frontend/src/features/chain/**` | 参数/counts/meta/降级/热加载/UI |

### Wave 2（验收/证据）

- `scripts/p6-acceptance.ps1`、`scripts/p6-e2e.ps1`、contract check、能力矩阵、截图、真实 LLM 评测观测。
- Lead + 子代理；共享脚本由 Lead 收口。

### Wave 3（冻结/合并）

- 全量门禁 → contracts frozen P6 → `phase-06-handoff.md` → 合并 `feat/v0.2-p6` → `feat/v0.2` → 停止报告。

### 提交策略

- 每任务小步提交；提交信息 `feat(p6): ...`/`test(p6): ...`/`docs(p6): ...`；不提交用户脏文件；不提交 `.tmp` 大产物（证据引用路径，关键截图入库）。

---

## 16. P6-D 裁决表

| ID | 裁决 | 依据 |
|---|---|---|
| P6-D-01 | 质量/评测完整对齐；6+1 指标、阈值/基线/回归、评测 API/看板 | U1；0.1 P8 spec §9；phase-06 L18 |
| P6-D-02 | `/quality` 登录可见运营+视觉；评测看板 admin-only | HLD §15；0.1 admin-only；P5 visual 登录可见；冲突裁决 |
| P6-D-03 | 搜索 5 类（version/requirement/case/issue/report），per-type limit，degraded | HLD §7.1；0.1 D55；phase-06 L19 |
| P6-D-04 | 待办 6 kind（确认/设计/接管/人工请求/dead），Drawer 不占导航 | U2；HLD §8.1；architecture §7.4；0.2 实体 |
| P6-D-05 | 链路增量 v2，不做任意 as-of | U3；P4 handoff；phase-06 L22 |
| P6-D-06 | 验收 B（真实 LLM+Chrome） | U4；P5 先例 |
| P6-D-07 | Agent 提示词 A1（版本/变量/预览/diff/恢复/审计/热生效） | U5；ARTEX 08/10；用户接受注入风险 |
| P6-D-08 | Agent 配置含 admin profile 编辑；普通用户只读无 prompt | phase-06 L21；HLD §7.1；0.1 D60 |
| P6-D-09 | `eval_run`/`eval_metric` HLD 字面表名；#32/#33 | HLD §2.2 L228；D34 |
| P6-D-10 | 新增 3 设置键（搜索/待办/质量） | 0.2 设置中心 D50；0.1 参数 |
| P6-D-11 | 不新增事件；读模型轮询/手动刷新 | 0.1 无事件；避免频道破坏 |
| P6-D-12 | 评测双轨：确定性语料门禁 + 真实 LLM 观测；0.1 fake-LLM 全流程列 P8 backlog | §5.4；风险 R1 |
| P6-D-13 | 全局中心只读、跨版本、`before` 分页、复用版本内详情 | HLD §7.1；0.1 全局中心 |
| P6-D-14 | 变量目录 code-defined（不新增表）；保存白名单/预览/回退 | U5；§8.4 |
| P6-D-15 | `[profile:<key>]` 保存强制；缺失 400 | 契约 TOOLS:35；fake LLM 依赖 |
| P6-D-16 | `enabled`/name/事件/wrapup/自定义 agent 等按矩阵 defer/na | ARTEX 10 §4；0.2 模型 |
| P6-D-17 | 迁移门禁前向兼容；P6 新 gate | P5 硬编码 31/162/12 |
| P6-D-18 | 验收证据入 `docs/version-V0.2/acceptance/p6/` + `.tmp` | phase plan §4.1；P5 先例 |
| P6-D-19 | 分支 `feat/v0.2-p6` → 合回 `feat/v0.2` | U7；P5 先例 |
| P6-D-20 | 不修改 0.2 设计文档；只更新 contracts/handoff/证据 | phase plan §4.1；用户要求 |

---

## 17. 风险、边界与回滚

| # | 风险 | 影响 | 缓解 |
|---|---|---|---|
| R1 | 评测确定性门禁不是 0.1 的 fake-LLM 全流程 | 与 0.1 P8 严格对齐有差距 | P6-D-12 明示；P8 补 fake P3/P5 全流程；若用户要求 P6 严格，需追加 fake LLM 扩展任务/评估 |
| R2 | 提示词模板层新增注入面 | prompt 可影响 Agent 行为 | 内网接受；保存白名单/无函数/无 IO/长度/marker；审计；工具白名单兜底 |
| R3 | 范围大（8 流） | 集成/验收风险 | 契约先行、写作用域互斥、Wave 0 骨架、Lead 收口、每流小步验收 |
| R4 | 真实 LLM 抖动 | 验收不稳定 | 重试/可观测/最终器；失败按系统缺陷定位；P5 fixtures 复用 |
| R5 | 迁移/契约门禁硬编码 | 门禁误报 | P5 gate 前向兼容；新增 P6 gate；迁移计数 33；CI 全量 |
| R6 | 全局读模型性能 | 页面慢 | Provider 批量/limit/索引评估；P95 证据；慢查询日志 |
| R7 | 0.1 旧假设回流 | 控制面错误 | §2.6 适配表；矩阵审计；review checklist |
| R8 | 旧库 profile/P5 工具增量不补 | Agent 中心显示旧工具 | P6 配置编辑器可补；不自动覆盖管理员编辑；handoff 说明 |
| R9 | `context_packet` 含 prompt | 观察泄露 | Agent 中心 read model 不返回；未来观察必须裁剪 |
| R10 | 验收数据复用 P5 DB | 数据依赖/隔离 | 克隆到 `eztest_v02_p6`；模板克隆失败则重跑完整闭环 |
| R11 | fake LLM/真实 LLM 行为差异 | E2E 与验收结论不一致 | 确定性 E2E 仅作 UI/流程；验收以真实 LLM 为准 |
| R12 | 契约冻结后返工 | 返工 | spec 前置裁决 + contract-first + P6 gate + 用户审阅 |

回滚：迁移只追加，失败按 DB 备份/新库重建；前端/后端功能开关不新增（无需 feature flag）；合并前保留 `feat/v0.2-p6` 分支，合回后按 P5 方式记录 merge。

---

## 18. Handoff 要求（`phase-06-handoff.md`）

必须包含：

1. 完成范围（8 块）与未完成/偏差（含 ARTEX defer/na）。
2. 契约变更清单：API/表/迁移/设置/事件/profile/工具；`frozen: P6`；P6 契约计数。
3. 测试/验收证据：命令、结果、S0–S9、E2E 截图、真实 LLM/Chrome、迁移幂等、契约门禁；能力矩阵/ARTEX 矩阵路径。
4. 已知缺口/偏差：P6-D-12、P5 已知限制、defer/na 项、是否影响 P7。
5. P7 输入：eval/质量/搜索/待办/Agent 中心/全局中心/链路接口、表、事件、设置、测试数据；P7 需要消费的契约。
6. 风险与下一阶段前置。
7. 分支/合并记录；用户脏文件未提交。
8. 停止声明（不自动进入 P7）。

---

## 19. 规格自审记录

- [x] 无 TBD/TODO/占位；所有 In 项有 API/表/UI/验收落点。
- [x] 与 phase-06 In/Out 对照：质量、搜索、待办、Agent 中心、全局中心、链路、矩阵全部覆盖；P9/knowledge/PDF/GC/多实例明确 Out。
- [x] 与 0.2 权威一致：三状态/波次/case_version/report_revision/issue 4 态/human_request/profile 14；未恢复 0.1 旧控制面。
- [x] 契约增量只追加；P0–P5 语义不变；迁移 #32/#33；设置 69→72；无新事件/task/profile/executor。
- [x] 权限：admin 写、登录读、普通用户无 prompt/transcript；评测 admin；质量页分层。
- [x] 验收可执行：真实 LLM+Chrome、E2E、截图、门禁、矩阵。
- [x] 冲突/漂移已记录（D54–D58、HLD 与实际、quality 权限、ARTEX 变量 na 与用户 U5）。
- [x] 用户裁决 U1–U7 全部映射到 P6-D/章节。
- [x] 风险/回滚/并行写作用域明确。
- [x] 未修改 `docs/version-V0.2/` 设计文档；本 spec 位于 `docs/superpowers/specs/`。

---

*本 spec 待用户审阅；批准后进入 writing-plans。*
