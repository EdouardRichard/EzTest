# EzTest 0.2 P5（代码智能 + 视觉断言）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `subagent-driven-development` (recommended) or `executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
> **执行方式（用户已指定）**：无人值守、并行子代理、Lead 代验收；任务按写作用域分派，Lead 统一收口 contracts/migrations/装配/验收/handoff。

**Goal:** 交付 0.2 P5：代码索引/diff/事实/影响/锚点/复核 + 视觉配置/基线/审批/差异/四态 + 代码/视觉 UI + Agent 工具 + 页面/API 提议 + 质量指标占位，并通过真实 LLM + 真实 Chrome 验收、冻结 P5 契约。

**Architecture:** 新增 `codeintel` 与 `visual` 两个四层限界上下文；codeintel 通过 work_item（新增 `code` 团队 + 专用 worker）异步执行 T0 Git 与 T1 AI 事实/影响；visual 通过 executor `visual.compare` 做真实像素比对，基线由管理员从执行 actual 证据批准；execution 扩展视觉断言工具/证据/四态/代码基线 commit；agent 扩展只读代码工具与两个候选写工具；前端在既有 8 Tab IA 内落代码 Tab、执行视觉子视图、锚点/提议/质量入口。

**Tech Stack:** Go 1.2x + PostgreSQL 17 + Next.js 16/React 19 + Node/Playwright 1.64.x executor + DeepSeek（真实 LLM）。

**Spec:** `docs/superpowers/specs/2026-11-01-eztest-v02-p5-codeintel-visual-design.md`（P5 实施规格；executor 必须同时读 spec 与 plan）。

**Branch:** `feat/v0.2-p5`（从 `feat/v0.2` @ `ff1e176` 拉出；已完成 spec 提交 `d9ac066`）。

---

## Global Constraints

- 不改 `docs/version-V0.2/**` 设计文档；只更新 `contracts/`、`backend/internal/db/migrations/`、代码、测试、脚本、本 plan、`phase-05-handoff.md`。
- `.env` 严格 4 项 EzTest 启动 env；不新增第 5 个；其余配置进 DB settings（`codeintel.*`/`visual.*` 16 键）。
- 迁移只追加 #25–#31，共 31；不改 #1–#24；`order.txt` 末尾追加；启动 advisory lock/checksum；无 down migration。
- 新表 11（代码 8 + 视觉 3）；不建 `code_change_set`/`code_analysis_cache`/`code_fact_version`；跨上下文无 FK，同上下文 FK 显式；审计列；枚举 CHECK；雪花 `BIGINT`；JSON/URL/工具 ID 十进制字符串。
- 证据 kind 10 值（新增 `visual_actual`/`visual_diff`）；work_item team 6 值（新增 `code`）；`version_sut.code_baseline_commit` 可空 40 hex。
- API `openapi.yaml` `0.2.0-p5`/`x-frozen: P5`；38 新 ops；P0–P4 仅新增可选字段/枚举值；破坏性变更需停止报告。
- 事件 12 个；任务 `index.repo`/`code.analyze` + `code` 团队；工具 11 个（9 code + `execution.visual_assert` + `sut.page_propose`）；settings 53→69；profiles 不新增。
- 真实 LLM：验收脚本只用真实 DeepSeek；fake 仅单测/集成端口 stub；真实失败按系统缺陷修复，不用 fake 掩盖。
- 真实 Chrome：视觉验收必须真实 executor + 真实像素比对；环境/配置不一致判 inconclusive，不判 failed。
- 视觉首跑无基线 → inconclusive(missing_visual_baseline)；管理员从 `visual_actual` 批准基线；retest 后 passed；不自动基线/不自动跨版本复用。
- 代码仓只读；不 checkout/switch/worktree；不写工作树；argv 无 shell。
- 契约/迁移/cmd/server/settings/agent seed/tool registry 只由 Lead 写；子代理只写自己任务的文件；禁止 `git add -A`；用户 dirty 文件不提交。
- 每个任务 TDD：先写失败测试→运行失败→最小实现→运行通过→提交；测试名/断言/签名按任务写死。
- 阶段末必须 `scripts/test.ps1` 全绿、P5 契约检查、P5 真实 LLM/Chrome 验收、P1–P4 回归、handoff；然后停止，不进入 P6。

## Review Focus

1. **多 SUT / 多仓身份混用**：`version_sut_id`/`repo_id`/`commit_sha`/`subject_id` 必须成对校验；不得用裸 `sut_id` 执行域；多仓分析 union 带 repo/SUT 标签；同 repo 多 SUT 的 baseline commit 冲突要 409，不得静默选一个。测试：Task 6/9/10/17/25（S1/S3/S8）。
2. **真实 LLM 证据幻觉/JSON 漂移**：模型给的 path/line/excerpt hash 不可信；服务端从 commit 内容重算、非法降级 low、JSON 解析失败 degraded；工具结果 32 KiB 落盘；设计 Agent 必须真实调用 code.* 工具。测试：Task 8/9/13/25（S2/S4）。
3. **视觉确定性/环境误判**：viewport/DPR/selector/region/config revision/浏览器版本任一不一致 → inconclusive，不产 diff、不判 failed；首跑无基线不假通过；视觉未运行但 case 通过必须被 finalizer 拦住。测试：Task 14–18/25（S8–S11）。
4. **work_item 领取路由**：agent worker 不得领取 `code` 团队任务；codeintel worker 只领取 code；team CHECK/枚举迁移后既有 5 团队语义不变；lease/retry/dead 回写 run 状态。测试：Task 2/6/9/27。
5. **契约计数/旧行为回归**：38 ops/31 迁移/11 表/12 事件/2 任务/11 工具/16 设置/10 证据/6 team 必须由 `p5-contract-check.cjs` 钉住；P2/P3/P4 契约前向兼容；P3 visual skipped/P4 issue/report/chain 不得被视觉证据/team 改动破坏。测试：Task 0/1/27/28。

---

## File Structure（先锁定边界）

### Backend — codeintel（新上下文）
- `backend/internal/codeintel/domain/`：`run.go`、`entity.go`、`edge.go`、`fact.go`、`analysis.go`、`candidate.go`、`anchor.go`、`review.go`、`factkey.go`、`limits.go`、`errors.go`。
- `backend/internal/codeintel/application/`：`ports.go`（Git/LLM/Version/Design/Resource/Execution/Eventing/Audit/WorkQueue）、`index_service.go`、`analysis_service.go`、`impact_service.go`、`anchor_service.go`、`review_service.go`、`proposal_service.go`、`worker.go`。
- `backend/internal/codeintel/infrastructure/gitx/`：`git.go`、`tree.go`、`diff.go`、`read.go`、`errors.go`。
- `backend/internal/codeintel/infrastructure/postgres/`：`index_repo.go`、`entity_repo.go`、`fact_repo.go`、`analysis_repo.go`、`candidate_repo.go`、`anchor_repo.go`、`review_repo.go`、`helpers.go`。
- `backend/internal/codeintel/infrastructure/{llm,eventing,audit,version,design,resource,execution,workqueue}/`：端口适配。
- `backend/internal/codeintel/interfaces/httpapi/`：`routes.go`、`handlers.go`、`views.go`、`errors.go`、`dto.go`。
- `backend/internal/codeintel/domain/*_test.go`、`application/*_test.go`、`infrastructure/gitx/*_test.go`、`integration/*_test.go`（build tag `integration`）。

### Backend — visual（新上下文）
- `backend/internal/visual/domain/`：`assertion.go`、`baseline.go`、`diff.go`、`classification.go`、`limits.go`、`errors.go`。
- `backend/internal/visual/application/`：`ports.go`、`config_service.go`、`baseline_service.go`、`diff_service.go`、`overview_service.go`。
- `backend/internal/visual/infrastructure/postgres/`：`assertion_repo.go`、`baseline_repo.go`、`diff_repo.go`、`overview_repo.go`。
- `backend/internal/visual/infrastructure/{design,execution,eventing,audit}/`。
- `backend/internal/visual/interfaces/httpapi/`：`routes.go`、`handlers.go`、`views.go`、`errors.go`。
- 测试同层 `*_test.go` + `integration/*_test.go`。

### Backend — 既有上下文扩展
- `backend/internal/agent/domain/work_item.go`：Team `code`。
- `backend/internal/agent/application/workqueue.go`/`ports_agent.go`/`worker.go`：`ClaimForTeams`、任务类型、agent claim 排除 code。
- `backend/internal/agent/application/tools/registry.go` + `p5_registry.go`：11 工具注册。
- `backend/internal/agent/application/p5_profiles.go`/`profiles.go`：allowed_tools 增量。
- `backend/internal/execution/application/fingerprint.go`：`VersionSUTBinding.Commit`/`CodeBaseline` commit 哈希。
- `backend/internal/execution/application/intent.go`：binding 读取 commit。
- `backend/internal/execution/application/visual_assert.go` + `agent_tool/visual_assert.go`：视觉工具/证据/finalizer。
- `backend/internal/execution/domain/verdict.go`/`case_run.go`/`summary`：移除假通过、视觉计数。
- `backend/internal/execution/interfaces/httpapi/views.go`：summary 字段/evidence kind。
- `backend/internal/version/application`：`version_sut` code baseline resolver/API 字段。
- `backend/internal/resource/application` + `interfaces/httpapi`：page/API proposed 创建 + confirm/reject。
- `backend/internal/settings/application/catalog.go`：16 键。
- `backend/internal/server/server.go`：路由挂载。
- `backend/cmd/eztest/main.go`/`p5_wiring.go`：codeintel/visual context/worker/routes/adapters。
- `backend/internal/db/migrations/`：7 个 SQL + `order.txt`；`migrate_test.go`/`migrate_integration_test.go` 期望 31。
- `backend/internal/agent/application/*_test.go`、`execution/**/*_test.go`、`version/**/*_test.go`、`resource/**/*_test.go`、`settings/**/*_test.go`：扩展测试。

### Contracts（Lead 独占）
- `docs/version-V0.2/contracts/{README.md,openapi.yaml,events.md,db-schema.md,migrations-order.md,task-types.md,agent-profiles-tools.md,settings.md,executor-protocol.md}`。

### Executor
- `executor/src/actions.mjs`（注册 `visual.compare`）、`executor/src/visual.mjs`（0.2 适配：capture-only、body limit、环境指纹、evidence 返回）、`executor/src/security.mjs`/`server.mjs`（限额）、`executor/test/visual.test.mjs`。

### Frontend
- `frontend/src/features/code-intel/{api.ts,types.ts,CodeTab.tsx,components/*,__tests__/*}`。
- `frontend/src/features/visual/{api.ts,types.ts,VisualView.tsx,components/*,QualityMetricsPanel.tsx,__tests__/*}`。
- `frontend/src/features/versions/{tabs.ts,VersionDetailPage.tsx,types.ts}`（code Tab）。
- `frontend/src/features/execution/{ExecutionTab.tsx,components/*}`（`?view=visual`）。
- `frontend/src/features/design/CaseDetailPanel.tsx`（visual 配置 + case 锚点）。
- `frontend/src/features/clarification/{RequirementDetail*,api.ts}`（需求锚点）。
- `frontend/src/features/delivery/IssuesTab.tsx`（问题锚点）。
- `frontend/src/features/resources/PageAPIPanel.tsx`/`api.ts`（proposed/confirm/reject/生成）。
- `frontend/src/app/(app)/quality/page.tsx`（最小质量页）。
- `frontend/e2e/p5.spec.ts`。

### Scripts / evidence
- `scripts/p5-contract-check.cjs`、`scripts/fixture-p5-repo.ps1`、`scripts/fixture-p5-target.cjs`、`scripts/p5-acceptance.ps1`、`scripts/p5-e2e.ps1`。
- `.tmp/p5/`、`.tmp/p5-acceptance/`（gitignored 证据）。

---

## Task 0: P5 契约增量（Lead）

**Files:**
- Modify: `docs/version-V0.2/contracts/openapi.yaml`、`events.md`、`db-schema.md`、`migrations-order.md`、`task-types.md`、`agent-profiles-tools.md`、`settings.md`、`executor-protocol.md`、`README.md`
- Test: `node scripts/p2-contract-check.cjs`、`p3-contract-check.cjs`、`p4-contract-check.cjs`；OpenAPI/YAML 解析

**Interfaces:**
- Consumes: spec §3/§4/§14 的精确表/API/事件/任务/工具/设置/协议清单。
- Produces: `0.2.0-p5`/`x-frozen: P5` 契约；后续任务按此实现。

- [ ] **Step 1: 写 `db-schema.md` §11–§13 增量**
  - 8 张 code 表、3 张 visual 表、`version_sut.code_baseline_commit`、`evidence.kind` 10 值、`work_item.team` 6 值；逐列/约束/索引/不变量，按 spec §3.2–§3.9；标注 `frozen: P5`。
- [ ] **Step 2: 写 `migrations-order.md` §7**
  - #25 `codeintel_0001_code_index.sql`、#26 `codeintel_0002_code_analysis.sql`、#27 `codeintel_0003_anchor_review.sql`、#28 `visual_0001_visual_assertion.sql`、#29 `execution_0004_visual_evidence.sql`、#30 `version_0006_version_sut_code_baseline.sql`、#31 `agent_0004_code_team.sql`；总 31。
- [ ] **Step 3: 写 `task-types.md` §7、`agent-profiles-tools.md` §8、`settings.md` §4e**
  - `index.repo`/`code.analyze` + `code` team；11 工具/5 profile 白名单；16 设置 53→69。
- [ ] **Step 4: 写 `events.md` §14（12 事件）与 `executor-protocol.md` v1.2（visual.compare/body/证据/指纹）**
- [ ] **Step 5: 写 `openapi.yaml` 38 ops/schemas/tags/version**
  - `info.version: 0.2.0-p5`；根 `x-frozen: P5`；P0–P4 只加可选字段/枚举值。
- [ ] **Step 6: 写 `contracts/README.md` P5 冻结状态与计数**
  - 31 迁移/76 表/38 ops/12 事件/2 任务+code team/11 工具/16 设置/10 证据/6 team。
- [ ] **Step 7: 验证既有契约检查仍 PASS**
  - Run: `node scripts/p2-contract-check.cjs && node scripts/p3-contract-check.cjs && node scripts/p4-contract-check.cjs`
  - Expected: PASS（前向兼容；若 p4 硬编码迁移 24，先改成 `>=24` + P4 tail 仍存在，再 PASS）。
- [ ] **Step 8: Commit**
  - `git add docs/version-V0.2/contracts` + `git commit -m "docs(p5): add codeintel and visual contracts"`

---

## Task 1: 迁移 #25–#31（Lead）

**Files:**
- Create: `backend/internal/db/migrations/codeintel_0001_code_index.sql`、`codeintel_0002_code_analysis.sql`、`codeintel_0003_anchor_review.sql`、`visual_0001_visual_assertion.sql`、`execution_0004_visual_evidence.sql`、`version_0006_version_sut_code_baseline.sql`、`agent_0004_code_team.sql`
- Modify: `backend/internal/db/migrations/order.txt`、`backend/internal/db/migrate_test.go`、`backend/internal/db/migrate_integration_test.go`
- Test: `go test ./internal/db`；`go test -tags=integration ./internal/db/...`

**Interfaces:**
- Consumes: spec §3.2–§3.9 的逐列 DDL/约束/索引。
- Produces: 31 迁移；11 新表；`version_sut.code_baseline_commit`；`evidence.kind` 10 值；`work_item.team` 6 值。

- [ ] **Step 1: 先改测试期望**
  - `migrate_test.go`：`len(migrations) != 31`；`wantTail` 改为 P5 的 7 个文件（按 order.txt 顺序）。
  - `migrate_integration_test.go`：`len(applied) != 31`；`expected` 表清单追加 11 个新表（`code_index_run,code_entity,code_edge,code_fact,code_analysis,code_impact_candidate,code_anchor,code_fact_review,visual_assertion,visual_baseline,visual_diff`）。
- [ ] **Step 2: 运行测试确认失败**
  - Run: `cd backend; go test ./internal/db -run TestOrderCoversEverySQLFile`
  - Expected: FAIL `len = 24, want 31`。
- [ ] **Step 3: 按 spec §3.2–§3.8 写 7 个迁移**
  - 只做加法；同上下文 FK 显式；CHECK/UNIQUE/INDEX 命名按 D34；`execution_0004` 用 `DROP CONSTRAINT IF EXISTS evidence_kind_check` + 新 CHECK；`agent_0004` 用 `work_item_team_check` 重建。
- [ ] **Step 4: 追加 `order.txt`**
  - 末尾按 #25–#31 顺序加入 7 行；不改 #1–#24。
- [ ] **Step 5: 运行单元测试**
  - Run: `cd backend; go test ./internal/db -run TestOrderCoversEverySQLFile`
  - Expected: PASS。
- [ ] **Step 6: 运行集成迁移/幂等（需 `EZTEST_TEST_DATABASE_URL`）**
  - Run: `cd backend; go test -tags=integration ./internal/db/...`
  - Expected: PASS；31 applied；二次 0；11 新表存在。
- [ ] **Step 7: Commit**
  - `git add backend/internal/db/migrations backend/internal/db/migrate_test.go backend/internal/db/migrate_integration_test.go`
  - `git commit -m "feat(p5): add codeintel and visual migrations"`

---

## Task 2: P1 团队/领取/任务类型增量（Lead）

**Files:**
- Modify: `backend/internal/agent/domain/team_room.go`、`backend/internal/agent/domain/repository.go`、`backend/internal/agent/application/workqueue.go`、`backend/internal/agent/application/worker.go`、`backend/internal/agent/application/ports_agent.go`、`backend/internal/agent/infrastructure/postgres/workqueue.go`
- Test: `backend/internal/agent/domain/team_room_test.go`、`backend/internal/agent/application/worker_test.go`、`backend/internal/agent/infrastructure/postgres/workqueue_integration_test.go`

**Interfaces:**
- Consumes: P1 `Team`/`WorkItemRepository`/`WorkQueue`/`Worker` 现有实现。
- Produces:
  - `const (TeamClarification Team = "clarification"; ...; TeamCode Team = "code")`
  - `WorkItemRepository.ClaimForTeams(ctx context.Context, claimant string, teams []Team, lease time.Duration) (WorkItem, bool, error)`
  - `WorkQueue.ClaimForTeams(ctx context.Context, claimant string, teams []agent.Team) (agent.WorkItem, bool, error)`
  - `const (WorkTypeIndexRepo = "index.repo"; WorkTypeCodeAnalyze = "code.analyze")`
  - `WorkItemPayload` 追加 `RepoID/IndexRunID/AnalysisID/BaseCommitSHA/HeadCommitSHA`（`int64`/`string`）
  - `var agentProductTeams = []agent.Team{clarification, design, execution, delivery, steward}`

- [ ] **Step 1: 写失败测试**
  - `team_room_test.go`：`agent.TeamCode.Valid() == true`；`ParseTeam("code")` 成功；未知团队仍失败。
  - `worker_test.go`：fake queue 记录 claim teams；`Worker.Run` 只调用 `ClaimForTeams` 且 teams 不含 `code`。
  - `workqueue_integration_test.go`：插入 clarification/code 各一条 open item；`ClaimForTeams([...5 product...])` 只领 clarification；`ClaimForTeams([code])` 只领 code。
- [ ] **Step 2: 运行确认失败**
  - `cd backend; go test ./internal/agent/domain ./internal/agent/application`
  - Expected: FAIL（TeamCode/ClaimForTeams 不存在）。
- [ ] **Step 3: 实现 TeamCode/ClaimForTeams/任务类型/payload 字段**
  - `Team.Valid`/`ParseTeam` 增加 code；错误文案列出 6 值。
  - `WorkItemRepository` 增加 `ClaimForTeams`；postgres 复制 `Claim` 的 tx/`FOR UPDATE SKIP LOCKED`，WHERE 增加 `team = ANY($n::text[])`；空 teams 返回无任务；未知 team 返回 invalid。
  - `WorkQueue.ClaimForTeams` 校验 claimant/teams 后调用 repo；`Claim` 保留（兼容测试/旧调用）。
  - `Worker.claim` 改用 `ClaimForTeams(agentProductTeams)`；保留 `Claim` 供其他调用。
  - `ports_agent.go` 增加 2 个 work type 常量；`WorkItemPayload` 增加字段。
- [ ] **Step 4: 运行单元测试**
  - `cd backend; go test ./internal/agent/domain ./internal/agent/application`
  - Expected: PASS。
- [ ] **Step 5: 运行集成测试（需 DB）**
  - `cd backend; go test -tags=integration ./internal/agent/infrastructure/postgres`
  - Expected: PASS；既有 5 团队任务语义不变；code 任务不被 product worker 领取。
- [ ] **Step 6: Commit**
  - `git add backend/internal/agent`
  - `git commit -m "feat(p5): add code team and typed work claims"`

---

## Task 3: 设置与证据 kind 增量（Lead）

**Files:**
- Modify: `backend/internal/settings/application/catalog.go`、`backend/internal/settings/application/catalog_test.go`（或新增）、`backend/internal/execution/domain`（证据 kind 常量/校验）、`backend/internal/execution/application/evidence_service.go`、`backend/internal/execution/interfaces/httpapi/views.go`
- Test: `backend/internal/settings/application/*_test.go`、`backend/internal/execution/**/*_test.go`

**Interfaces:**
- Consumes: spec §4.5 的 16 键；§3.6 的 10 证据 kind。
- Produces:
  - settings catalog 69 键，分类 `codeintel`/`visual`；默认值精确。
  - `evidence.kind` 常量/视图枚举含 `visual_actual`/`visual_diff`；`EvidenceView.kind` API 值同步。

- [ ] **Step 1: 写失败测试**
  - settings：catalog 长度 69；`codeintel.max_changed_files` 默认 20、`codeintel.prompt_version` 默认 `p5.1`、`visual.default_threshold` 0.005、`visual.baseline_max_bytes` 5242880、`codeintel.exclude_globs` 为 JSON 数组。
  - execution：evidence kind `visual_actual`/`visual_diff` 被接受并映射；未知 kind 仍拒绝。
- [ ] **Step 2: 运行确认失败**
  - `cd backend; go test ./internal/settings/application ./internal/execution/...`
  - Expected: FAIL（键/kind 缺失）。
- [ ] **Step 3: 加 16 个 catalog 条目**
  - 按 spec §4.5 的类型/默认/范围/描述；`TypeJSON` 用于 `codeintel.exclude_globs`；不新增 secret。
- [ ] **Step 4: 扩证据 kind**
  - 在 evidence domain/application/views 增加 `visual_actual`/`visual_diff`；executor mapping 同步（Task 16）；保持 8 个旧 kind。
- [ ] **Step 5: 运行测试**
  - `cd backend; go test ./internal/settings/... ./internal/execution/...`
  - Expected: PASS。
- [ ] **Step 6: Commit**
  - `git add backend/internal/settings backend/internal/execution`
  - `git commit -m "feat(p5): add codeintel visual settings and evidence kinds"`

---

## Task 4: 代码基线 commit 升级（Lead）

**Files:**
- Modify: `backend/internal/version/domain/binding.go`、`backend/internal/version/application/service.go`、`backend/internal/version/application/views.go`、`backend/internal/version/application/ports.go`（或新建 `code_baseline.go`）、`backend/internal/version/infrastructure/postgres/*`、`backend/internal/version/interfaces/httpapi/*`、`backend/internal/execution/application/fingerprint.go`、`backend/internal/execution/application/intent.go`、`backend/internal/execution/infrastructure/adapters/version_adapter.go`
- Test: `backend/internal/version/application/service_test.go`、`backend/internal/execution/application/fingerprint_test.go`、`intent_scheduler_test.go`、`backend/internal/execution/infrastructure/adapters/adapters_test.go`

**Interfaces:**
- Consumes: Task 1 的 `version_sut.code_baseline_commit` 列；现有 `resourceapp.CodeRepositoryView`。
- Produces:
  - `version.Binding.CodeBaselineCommit string`（空=未解析；40 hex 或不安全为空）
  - `versionapp.CodeBaselineResolver interface { ResolveHeadCommit(ctx context.Context, repo resourceapp.CodeRepositoryView) (string, error) }`
  - `versionapp.BindingView.CodeBaselineCommit string`
  - `version.Repository.SetCodeBaselineCommit(ctx context.Context, versionSUTID int64, commitSHA string) error`
  - `execution.VersionSUTBinding.Commit string`
  - `execution.CodeBaseline(binding VersionBinding) string`：按 repo 去重、排序，`{repo_id,commit_sha}` 有 commit / `{repo_id,branch}` 无 commit；`sha256(canonical JSON)`；全无仓 `""`。

- [ ] **Step 1: 写失败测试**
  - version：fake resolver 返回 40hex；Create 后 `Bindings[0].CodeBaselineCommit` 与 `BindingView` 都有值；resolver 返回 error 时版本仍创建成功、commit 为空。
  - execution：两个 binding 带 commit → `CodeBaseline` 稳定（顺序无关）；同 repo 两 SUT 去重；一个 commit、一个 branch → 混合指纹；无仓 → `""`。
- [ ] **Step 2: 运行确认失败**
  - `cd backend; go test ./internal/version/application ./internal/execution/application`
  - Expected: FAIL（字段/接口/函数不存在）。
- [ ] **Step 3: 扩展 domain/repository**
  - `Binding.CodeBaselineCommit`；`Validate` 校验空或 40 hex；postgres insert/select 增加列；`SetCodeBaselineCommit` 更新单行。
- [ ] **Step 4: 扩展 version service/view/API**
  - `New` 增加 `resolver CodeBaselineResolver` 参数（或 `SetCodeBaselineResolver`），更新 `cmd/eztest` 与测试构造；`Create` 在持久化前解析每个带 repo 的 binding；resolver 失败不阻塞，记 audit warning、留空；`buildBindingView` 输出 `codeBaselineCommit`；OpenAPI DTO（Task 0）。
- [ ] **Step 5: 扩展 execution fingerprint/adapter**
  - `VersionSUTBinding.Commit`；adapter 从 version view 带 commit；`CodeBaseline` 新算法；`intent.go` 不变调用签名。
- [ ] **Step 6: 运行测试**
  - `cd backend; go test ./internal/version/... ./internal/execution/...`
  - Expected: PASS；既有 P3 intent 测试更新后通过。
- [ ] **Step 7: Commit**
  - `git add backend/internal/version backend/internal/execution`
  - `git commit -m "feat(p5): resolve version code baseline commits"`

---

## Task 5: Codeintel 领域模型与端口（Lead/W1）

**Files:**
- Create: `backend/internal/codeintel/domain/{run.go,entity.go,edge.go,fact.go,analysis.go,candidate.go,anchor.go,review.go,factkey.go,limits.go,errors.go,git.go}`
- Create: `backend/internal/codeintel/application/ports.go`
- Test: `backend/internal/codeintel/domain/*_test.go`

**Interfaces:**
- Consumes: spec §3.2–§3.4、§5.1–§5.5。
- Produces（后续任务依赖的精确名字）：
```go
type IndexStatus string
const (IndexQueued IndexStatus="queued"; IndexRunning="running"; IndexDone="done"; IndexFailed="failed")
type AnalysisStatus string // 同上四值
type EntityKind string // file/module/route/api/client_call/component/page/symbol
type EdgeRel string // contains/imports/defines/calls/renders/route_to_api
type Confidence string // low/medium/high
type CandidateSource string // direct/contract/relation/ai
type SubjectKind string // requirement/requirement_version/case_version/sut_page/sut_api
type AnchorSubject string // version/requirement/requirement_version/case_version/issue
type AnchorStatus string // valid/stale/broken
type ReviewVerdict string // correct/incorrect

type IndexRun struct { ID, VersionID, RepoID int64; CommitSHA, Branch, Status string; FilesTotal, FilesIndexed int; Truncated bool; Stats, Errors json.RawMessage; StartedAt, FinishedAt *time.Time; CreatedBy *int64; CreatedAt, UpdatedAt time.Time }
type Entity struct { ID, RepoID int64; CommitSHA string; IndexRunID *int64; Kind EntityKind; Path, Name, Signature string; StartLine, EndLine *int; BlobHash, Source string; Confidence Confidence; Evidence, Meta json.RawMessage; FactKey string; FactVersion int; AnalysisID *int64; CreatedAt, UpdatedAt time.Time }
type Edge struct { ID, RepoID int64; CommitSHA string; AnalysisID *int64; SrcEntityID, DstEntityID int64; Rel EdgeRel; Source string; Confidence Confidence; Evidence, Meta json.RawMessage; FactKey string; FactVersion int; CreatedAt, UpdatedAt time.Time }
type Fact struct { ID, RepoID int64; CommitSHA, FactKey string; Version int; Kind EntityKind; Path, Name, Signature string; StartLine, EndLine *int; BlobHash string; Body, Evidence json.RawMessage; Source string; Confidence Confidence; Model, PromptVersion string; AnalysisID *int64; SupersededAt *time.Time; CreatedAt, UpdatedAt time.Time }
type Analysis struct { ID, VersionID, RepoID int64; BaseCommitSHA, HeadCommitSHA string; Status AnalysisStatus; Degraded bool; Model, PromptVersion string; AnalysisVersion int; ChangedFiles, Stats, Unmapped, Errors, FactVersions json.RawMessage; CreatedBy *int64; StartedAt, FinishedAt *time.Time; CreatedAt, UpdatedAt time.Time }
type ImpactCandidate struct { ID, AnalysisID int64; SubjectKind SubjectKind; SubjectID int64; Reason string; Confidence Confidence; Source CandidateSource; Evidence json.RawMessage; Selected bool; SelectionUpdatedBy *int64; SelectionUpdatedAt *time.Time; CreatedAt, UpdatedAt time.Time }
type Anchor struct { ID, VersionID, RepoID int64; SubjectKind AnchorSubject; SubjectID int64; FilePath string; StartLine, EndLine *int; EntityID *int64; CommitSHA, ExcerptSHA, Status, Confidence, CreatedByKind string; CreatedBy *int64; LastValidatedAt *time.Time; CreatedAt, UpdatedAt time.Time }
type FactReview struct { ID, FactID int64; FactVersion int; RepoID int64; CommitSHA, Verdict, Reason string; ReviewerID int64; CreatedAt, UpdatedAt time.Time }

func NormalizeRepoPath(raw string) (string, error)
func NormalizeAPIPath(raw string) string
func FactKey(kind EntityKind, path, name, method, normalizedPath string) (string, bool)
func ExcerptHash(lines []string) string // "sha256:"+hex(sha256(join(lines,"\n")))

type TreeEntry struct { Path, Mode, Type, BlobSHA string; Size int64; Binary, Unsupported bool }
type TreeResult struct { Entries []TreeEntry; Total int; Truncated bool }
type ChangedFile struct { Path, OldPath, Status string; Additions, Deletions int; Binary bool; HeadBlobSHA *string }
type DiffResult struct { Files []ChangedFile; Truncated bool }
type FileWindow struct { Path, Commit string; Lines []string; StartLine, EndLine, TotalLines int; Truncated bool }
type FileDiff struct { Path, Patch string; Truncated, Binary, Deleted bool }
```
- 端口（codeintel 只依赖本上下文类型；适配器在 infrastructure 转换 agent/version/design/resource/execution 类型）：
```go
type GitClient interface {
  ResolveCommit(ctx context.Context, repoPath, ref string) (string, error)
  HeadCommit(ctx context.Context, repoPath, branch string) (string, error)
  IsAncestor(ctx context.Context, repoPath, base, head string) (bool, error)
  ListTree(ctx context.Context, repoPath, commit string, include, exclude []string, max int) (TreeResult, error)
  Diff(ctx context.Context, repoPath, base, head string, max int) (DiffResult, error)
  ReadFile(ctx context.Context, repoPath, commit, path string, start, end, maxBytes int) (FileWindow, error)
  FileDiff(ctx context.Context, repoPath, base, head, path string, maxBytes int) (FileDiff, error)
}
type FactExtractor interface { Extract(ctx context.Context, in FactExtractInput) (FactExtractOutput, error); Impact(ctx context.Context, in ImpactInferenceInput) (ImpactInferenceOutput, error) }
type WorkItem struct { ID, VersionID int64; Type string; Payload json.RawMessage; Attempts, MaxAttempts int; LeaseEpoch int64 }
type NewWorkItem struct { VersionID int64; Team, Type string; Priority int; DedupeKey string; Payload any; MaxAttempts int }
type WorkQueue interface { Ensure(ctx context.Context, in NewWorkItem) (WorkItem, bool, error); ClaimForTeams(ctx context.Context, claimant string, teams []string) (WorkItem, bool, error); MarkRunning(ctx context.Context, id, epoch int64) error; Renew(ctx context.Context, id, epoch int64, lease time.Duration) error; Complete(ctx context.Context, id, epoch int64) error; Fail(ctx context.Context, id, epoch int64, message string, retryable bool, backoff time.Duration) error }
type VersionReader interface { VersionSUTBindings(ctx context.Context, versionID int64) ([]VersionSUTBinding, error); SetCodeBaselineCommit(ctx context.Context, versionSUTID int64, commit string) error }
type DesignReader interface { CaseVersions(ctx context.Context, versionID int64) ([]CaseVersionRef, error); RequirementRelations(ctx context.Context, versionID int64) ([]RequirementRelationRef, error) }
type ResourceReader interface { SUTPages(ctx context.Context, versionSUTID int64) ([]PageRef, error); SUTAPIs(ctx context.Context, versionSUTID int64) ([]APIRef, error); CodeRepository(ctx context.Context, repoID int64) (RepoRef, error) }
type ExecutionReader interface { NetworkEvidence(ctx context.Context, versionID int64) ([]NetworkRequest, error); EvidenceMeta(ctx context.Context, versionID, evidenceID int64) (EvidenceRef, error) }
```
- 常量：spec §4.5 的硬上限（MaxFactsPerFile=200、MaxEdgesPerFile=200、MaxEvidencePerFact=8、MaxExcerptLines=200、MaxFactKeyChars=512、MaxReasonChars=2000、MaxPathChars=4096、MaxTreeEntriesPerPage=200、MaxReadLines=400、MaxFileReadBytes=64<<10、MaxRequirementsInPrompt=100、MaxAnalysisErrors=20、MaxFactVersionsSnapshot=500、MaxUnmappedEntries=200、MaxChangedFilesStored=2000）。
- 错误：`ErrNotFound`/`ErrConflict`/`ErrUnavailable` 或 `platform` 稳定码；应用层映射 404/409/422/503。

- [ ] **Step 1: 写失败测试**
  - `TestFactKeyNormalization`：route/page/api/client/component/symbol 的合法 key；空 name/非法 path 返回 false；API path `/api/orders/{id}` 归一化。
  - `TestRepoPathRejectsEscape`：绝对路径/`..`/反斜杠/symlink 标记。
  - `TestExcerptHash`：LF 归一化、尾 `\r`、1-based 闭区间、无尾换行。
  - `TestStatusEnums`：四状态/枚举 valid；非法拒绝。
  - `TestLimits`：硬上限值钉住。
- [ ] **Step 2: 运行确认失败**
  - `cd backend; go test ./internal/codeintel/domain`
  - Expected: FAIL（包不存在）。
- [ ] **Step 3: 实现 domain/ports**
  - 按 spec §3 字段/枚举；JSON 字段用 `json.RawMessage`；不引入 pgx/HTTP/LLM SDK。
  - Git DTO 定义在 `domain/git.go`；`gitx` 实现时导入 domain 类型，domain 不导入 infrastructure。
- [ ] **Step 4: 运行测试**
  - `cd backend; go test ./internal/codeintel/domain`
  - Expected: PASS。
- [ ] **Step 5: Commit**
  - `git add backend/internal/codeintel`
  - `git commit -m "feat(p5): add codeintel domain model and ports"`

---

## Task 6: 只读 Git 层（W1）

**Files:**
- Create: `backend/internal/codeintel/infrastructure/gitx/{git.go,tree.go,diff.go,read.go,errors.go}`
- Test: `backend/internal/codeintel/infrastructure/gitx/{git_test.go,diff_test.go,read_test.go}`

**Interfaces:**
- Consumes: `domain.TreeResult/ChangedFile/DiffResult/FileWindow/FileDiff`。
- Produces:
```go
type Runner interface { Run(ctx context.Context, dir string, args ...string) (stdout, stderr []byte, err error) }
type Git struct { /* runner, timeout */ }
func New(runner Runner, timeout time.Duration) *Git
func (g *Git) ResolveCommit(ctx context.Context, repoPath, ref string) (string, error)
func (g *Git) HeadCommit(ctx context.Context, repoPath, branch string) (string, error)
func (g *Git) IsAncestor(ctx context.Context, repoPath, base, head string) (bool, error)
func (g *Git) ListTree(ctx context.Context, repoPath, commit string, include, exclude []string, max int) (domain.TreeResult, error)
func (g *Git) Diff(ctx context.Context, repoPath, base, head string, max int) (domain.DiffResult, error)
func (g *Git) ReadFile(ctx context.Context, repoPath, commit, path string, start, end, maxBytes int) (domain.FileWindow, error)
func (g *Git) FileDiff(ctx context.Context, repoPath, base, head, path string, maxBytes int) (domain.FileDiff, error)
```
- 环境：argv 无 shell、`GIT_OPTIONAL_LOCKS=0`、cwd=repo、10s 超时、只读。

- [ ] **Step 1: 写失败测试（真实临时 git 仓）**
  - `TestListTreeParsesFilesAndFilters`：两个 commit fixture；include/exclude；二进制/符号链接标记；truncated。
  - `TestDiffParsesRawAndNumstat`：A/M/D/R/C/T、binary、rename old_path、head blob；`-z` 文件名含空格/换行不注入。
  - `TestResolveCommitAndAncestor`：branch/40hex/非祖先 false/不存在 error。
  - `TestReadFileWindowAndEscape`：start/end 1-based、行数/字节上限、绝对路径/`..`/反斜杠拒绝。
  - `TestFileDiffLimits`：patch 超限 truncated、binary/deleted 标志。
- [ ] **Step 2: 运行确认失败**
  - `cd backend; go test ./internal/codeintel/infrastructure/gitx`
  - Expected: FAIL（包不存在）。
- [ ] **Step 3: 实现 Git/解析**
  - `git ls-tree -r -t -l -z`、`git diff --raw -z --no-ext-diff --find-renames=50%` + `--numstat -z`、`git show`、`git diff --unified=3`；严格 UTF-8；错误映射 `repo_not_found/path_not_repo/git_unavailable/commit_not_found/invalid_head/path_escape/unsupported_content`。
- [ ] **Step 4: 运行测试**
  - `cd backend; go test ./internal/codeintel/infrastructure/gitx`
  - Expected: PASS。
- [ ] **Step 5: Commit**
  - `git add backend/internal/codeintel/infrastructure/gitx`
  - `git commit -m "feat(p5): add read-only git layer"`

---

## Task 7: 索引运行、worker 与 T0 API（W1）

**Files:**
- Create: `backend/internal/codeintel/application/{index_service.go,worker.go}`、`backend/internal/codeintel/infrastructure/postgres/{index_repo.go,entity_repo.go}`、`backend/internal/codeintel/interfaces/httpapi/{routes.go,index_handler.go,views.go,errors.go}`
- Test: `backend/internal/codeintel/application/index_service_test.go`、`backend/internal/codeintel/integration/index_integration_test.go`、`backend/internal/codeintel/interfaces/httpapi/index_handler_test.go`

**Interfaces:**
- Consumes: Task 5 ports/domain；Task 6 Git；Task 2 WorkQueue/任务类型；Task 4 VersionReader。
- Produces:
```go
type IndexService struct { /* repos, git, versions, resources, queue, events, audit, clock, ids, settings */ }
func (s *IndexService) Start(ctx context.Context, versionID, repoID int64, commit string) (domain.IndexRun, bool, error)
func (s *IndexService) Execute(ctx context.Context, runID int64) error
func (s *IndexService) Latest(ctx context.Context, repoID int64) (domain.IndexRun, error)
func (s *IndexService) Get(ctx context.Context, runID int64) (domain.IndexRun, error)
func (s *IndexService) RepoStatus(ctx context.Context, versionID int64) ([]RepoStatus, error)
func (s *IndexService) Tree(ctx context.Context, repoID int64, commit, path string, cursor string, limit int) ([]domain.Entity, string, bool, error)

type QueryService struct { /* entityRepo, git, versions, resources, audit */ }
func (s *QueryService) File(ctx context.Context, repoID int64, commit, path string, start, end int) (domain.FileWindow, error)
func (s *QueryService) Diff(ctx context.Context, repoID int64, base, head, path, cursor string, limit int) (domain.DiffResult, string, bool, error)
func (s *QueryService) FileDiff(ctx context.Context, repoID int64, base, head, path string) (domain.FileDiff, error)
func (s *QueryService) Commit(ctx context.Context, repoID int64, sha string) (CommitView, error)

type IndexRepository interface { CreateRun(ctx, run) (run, error); GetRun(ctx,id) (run,error); LatestRun(ctx,repoID) (run,error); UpdateRun(ctx,run) error; ActiveRun(ctx,repoID) (run,bool,error) }
type EntityRepository interface { UpsertFileEntities(ctx, runID, repoID, commit string, entries []domain.TreeEntry) error; ListTree(ctx, repoID, commit, path string, cursor string, limit int) ([]domain.Entity, string, bool, error) }

type Worker struct { queue WorkQueue; handlers map[string]Handler; logger; concurrency int; recoveryInterval; heartbeatInterval }
type Handler interface { Handle(ctx context.Context, item WorkItem) error }
func NewWorker(queue WorkQueue, handlers map[string]Handler, ...) *Worker
func (w *Worker) Run(ctx context.Context) error
```
- `index.repo` handler：payload 解码 `{index_run_id, repo_id, commit_sha, branch}`；`Execute` T0；成功后回填 `version_sut.code_baseline_commit`（若空）。

- [ ] **Step 1: 写失败测试**
  - service：repo 未绑定 422；active 409；done 同 repo+commit 复用 200；成功后 run done/file 实体/baseline 回填；git 不可用 failed。
  - worker：claim `code` 团队；`index.repo` 调用 handler；失败按 retry/dead；lease 续租调用。
  - handler：`POST /api/versions/{id}/code/index` 202/200/409/422；`GET /api/versions/{id}/code/repos` 200；`GET /api/code/repos/{repoId}/index-runs/latest|{runId}` 200/404；`GET /api/code/repos/{repoId}/tree|file|diff|diff/file|commits/{sha}` 200/400/404/422；ID 十进制字符串。
  - integration：真实临时 git 仓索引 → 31 迁移表/实体唯一/truncated。
- [ ] **Step 2: 运行确认失败**
  - `cd backend; go test ./internal/codeintel/application ./internal/codeintel/interfaces/httpapi`
  - Expected: FAIL。
- [ ] **Step 3: 实现仓储/服务/worker/handler**
  - `FOR UPDATE SKIP LOCKED` 由 WorkQueue 负责；index repo 用 active partial unique；实体 upsert；事件 `code.index_*`；审计。
  - Handler 只做 DTO/错误映射；repo 绑定校验通过 VersionReader；`QueryService` 走 Git/实体仓；文件读取审计。
- [ ] **Step 4: 运行测试**
  - `cd backend; go test ./internal/codeintel/...`
  - Expected: PASS。
- [ ] **Step 5: Commit**
  - `git add backend/internal/codeintel`
  - `git commit -m "feat(p5): add code index runs and T0 API"`

---

## Task 8: T1 AI 事实、分析运行与 API（W1/W2）

**Files:**
- Create: `backend/internal/codeintel/application/analysis_service.go`、`backend/internal/codeintel/infrastructure/llm/{prompt.go,extractor.go}`、`backend/internal/codeintel/infrastructure/postgres/{fact_repo.go,analysis_repo.go}`、`backend/internal/codeintel/interfaces/httpapi/analysis_handler.go`
- Test: `backend/internal/codeintel/application/analysis_service_test.go`、`backend/internal/codeintel/infrastructure/llm/extractor_test.go`、`backend/internal/codeintel/integration/analysis_integration_test.go`、`backend/internal/codeintel/interfaces/httpapi/analysis_handler_test.go`

**Interfaces:**
- Consumes: Task 5 ports/domain；Task 6 Git；Task 7 worker；Task 4 VersionReader。
- Produces:
```go
type AnalysisService struct { /* analysisRepo, factRepo, entityRepo, git, extractor, versions, resources, design, queue, events, audit, clock, ids, settings */ }
func (s *AnalysisService) Start(ctx context.Context, versionID, repoID int64, headCommit string, force bool) (domain.Analysis, bool, error)
func (s *AnalysisService) Execute(ctx context.Context, analysisID int64) error
func (s *AnalysisService) Latest(ctx context.Context, versionID, repoID int64) (domain.Analysis, error)
func (s *AnalysisService) Get(ctx context.Context, analysisID int64) (domain.Analysis, error)
func (s *AnalysisService) Facts(ctx context.Context, repoID int64, commit, kind, path, cursor string, limit int) ([]domain.Fact, string, bool, error)
func (s *AnalysisService) Search(ctx context.Context, repoID int64, commit, query, kind string, limit int) ([]domain.Entity, error)

type FactExtractInput struct { Repo RepoRef; CommitSHA, Path string; Lines []string; MaxFacts, MaxEdges, MaxExcerptLines int; PromptVersion string }
type FactExtractOutput struct { Facts []FactDraft; Edges []EdgeDraft; InvalidFacts int }
type FactDraft struct { Kind domain.EntityKind; Name, Signature, Path string; StartLine, EndLine int; Body json.RawMessage; Confidence domain.Confidence; Evidence []EvidenceDraft }
type EvidenceDraft struct { File string; StartLine, EndLine int }
type EdgeDraft struct { SrcFactKey, DstFactKey string; Rel domain.EdgeRel; Confidence domain.Confidence; Evidence []EvidenceDraft }
```
- 规则：只变更集可读文本；≤`codeintel.max_changed_files`；证据服务端重算；非法降级 low；同 fact_key 版本复用/递增；文件级 cache；LLM 失败 degraded；`analysis.stats` 计数。
- Handler：`GET changes` 200；`POST analyses` 202/200/409/422；`GET analyses/latest` 200/404；`GET analyses/{id}` 200/404；`GET /api/code/repos/{repoId}/facts` 200/400；`GET /api/code/repos/{repoId}/search` 200/400；`code.analyze` worker handler。

- [ ] **Step 1: 写失败测试**
  - extractor prompt/schema：合法 JSON→facts/edges；空/非法 JSON→error；无工具调用/超时→degraded 路径。
  - analysis service：base/head 校验；active 409；同 range 复用 200/force 新版本；cache 命中不调 extractor；证据非法降级 low；fact version supersede；degraded done；统计。
  - handler：202/200/409/422/404；ID 字符串；analysis detail。
- [ ] **Step 2: 运行确认失败**
  - `cd backend; go test ./internal/codeintel/application ./internal/codeintel/infrastructure/llm ./internal/codeintel/interfaces/httpapi`
  - Expected: FAIL。
- [ ] **Step 3: 实现 extractor/prompt/analysis service/repos/handler**
  - 每文件一次 LLM；严格 JSON；单文件 200/200/8/40 上限；server 重算 excerpt；`code_fact`/`code_entity`/`code_edge` 写入；`code_analysis` 状态/统计。
  - 失败/超时：可重试错误交 worker；`analysis_timeout_seconds`；最终 dead→failed。
- [ ] **Step 4: 运行测试**
  - `cd backend; go test ./internal/codeintel/...`
  - Expected: PASS。
- [ ] **Step 5: Commit**
  - `git add backend/internal/codeintel`
  - `git commit -m "feat(p5): add AI fact analysis"`

---

## Task 9: 锚点与事实复核（W2）

**Files:**
- Create: `backend/internal/codeintel/application/{anchor_service.go,review_service.go}`、`backend/internal/codeintel/infrastructure/postgres/{anchor_repo.go,review_repo.go}`、`backend/internal/codeintel/interfaces/httpapi/{anchor_handler.go,review_handler.go}`
- Test: `backend/internal/codeintel/application/{anchor_service_test.go,review_service_test.go}`、`backend/internal/codeintel/integration/{anchor_integration_test.go,review_integration_test.go}`、`backend/internal/codeintel/interfaces/httpapi/{anchor_handler_test.go,review_handler_test.go}`

**Interfaces:**
- Consumes: Task 5 domain/ports；Task 6 Git；Task 8 facts；Task 4 version bindings；design/issue readers。
- Produces:
```go
type AnchorService struct { /* anchorRepo, git, versions, design, delivery, events, audit, clock, ids, settings */ }
func (s *AnchorService) Create(ctx context.Context, in CreateAnchorInput) (domain.Anchor, error)
func (s *AnchorService) List(ctx context.Context, versionID int64, subjectKind string, subjectID int64) ([]domain.Anchor, error)
func (s *AnchorService) Validate(ctx context.Context, versionID, anchorID int64) (domain.Anchor, error)
func (s *AnchorService) Delete(ctx context.Context, versionID, anchorID int64) error

type ReviewService struct { /* reviewRepo, factRepo, versions, events, audit, clock, ids */ }
func (s *ReviewService) List(ctx context.Context, versionID, factID int64) ([]domain.FactReview, error)
func (s *ReviewService) Create(ctx context.Context, versionID, factID, reviewerID int64, verdict domain.ReviewVerdict, reason string) (domain.FactReview, error)

type AnchorRepository interface { Create(ctx, anchor) (domain.Anchor,error); List(ctx, versionID int64, subjectKind string, subjectID int64) ([]domain.Anchor,error); Get(ctx, id int64) (domain.Anchor,error); UpdateStatus(ctx, id int64, status string, at time.Time) (domain.Anchor,error); Delete(ctx, id int64) error }
type ReviewRepository interface { List(ctx, factID int64) ([]domain.FactReview,error); Create(ctx, review) (domain.FactReview,error); LatestVerdicts(ctx, repoID int64, commit string, factKeys []string) (map[string]string,error) }
```
- 规则：subject 归属版本；path/line/excerpt 校验；重复 409；valid/stale/broken；case_source 只读兼容；review 追加式、latest verdict、incorrect 排除自动影响；事件/审计。
- Handler：anchors 4 ops、reviews 2 ops。

- [ ] **Step 1: 写失败测试**
  - anchor：subject 不属于版本 422；path 逃逸 422；行越界 422；重复 409；valid→stale（改文件）→broken（删文件）；case_source 只读展示；删除 204/404。
  - review：追加式/最新结论；incorrect 后 impact 自动候选排除；audit；fact 不属于版本 404/422。
  - handler：200/201/204/400/401/403/404/409/422；ID 字符串。
- [ ] **Step 2: 运行确认失败**
  - `cd backend; go test ./internal/codeintel/application ./internal/codeintel/interfaces/httpapi`
  - Expected: FAIL。
- [ ] **Step 3: 实现 anchor/review service/repo/handler**
  - 使用 Task 6 Git 读目标 commit；`excerpt_sha` 自动计算；状态更新；review latest verdict 查询；事件 `code.anchor_validated`/`code.fact_reviewed`。
- [ ] **Step 4: 运行测试**
  - `cd backend; go test ./internal/codeintel/...`
  - Expected: PASS。
- [ ] **Step 5: Commit**
  - `git add backend/internal/codeintel`
  - `git commit -m "feat(p5): add code anchors and fact reviews"`

---

## Task 10: 影响分析（W2）

**Files:**
- Create: `backend/internal/codeintel/application/impact_service.go`、`backend/internal/codeintel/infrastructure/postgres/candidate_repo.go`、`backend/internal/codeintel/interfaces/httpapi/impact_handler.go`
- Test: `backend/internal/codeintel/application/impact_service_test.go`、`backend/internal/codeintel/integration/impact_integration_test.go`、`backend/internal/codeintel/interfaces/httpapi/impact_handler_test.go`

**Interfaces:**
- Consumes: Task 8 analysis/facts/edges；Task 9 anchors/reviews；Design/Resource readers。
- Produces:
```go
type ImpactService struct { /* analysisRepo, candidateRepo, factRepo, entityRepo, anchorRepo, reviewRepo, design, resources, extractor, events, clock, settings */ }
func (s *ImpactService) Compute(ctx context.Context, analysisID int64) ([]domain.ImpactCandidate, error)
func (s *ImpactService) List(ctx context.Context, analysisID int64) ([]domain.ImpactCandidate, error)
func (s *ImpactService) SetSelected(ctx context.Context, analysisID, candidateID, actorID int64, selected bool) (domain.ImpactCandidate, error)
func (s *ImpactService) Union(ctx context.Context, versionID int64) ([]domain.ImpactCandidate, error)
type CandidateRepository interface { ReplaceForAnalysis(ctx, analysisID int64, candidates []domain.ImpactCandidate) error; ListByAnalysis(ctx, analysisID int64) ([]domain.ImpactCandidate,error); UpdateSelection(ctx, analysisID, candidateID, actorID int64, selected bool) (domain.ImpactCandidate,error) }
```
- 算法：direct/contract/relation/case + AI；priority/排序/low 未选/unmapped；多仓 union 带 repo/SUT 标签。
- Handler：`GET impact`、`PATCH candidates/{id}`；detail 包含 candidates/unmapped。

- [ ] **Step 1: 写失败测试**
  - direct anchor/case_source；contract route_to_api 一跳；relation 一跳；case 候选；AI 候选校验/未知 ID 丢弃；priority/low 未选；unmapped；多仓 union；PATCH 404/409。
- [ ] **Step 2: 运行确认失败**
  - `cd backend; go test ./internal/codeintel/application ./internal/codeintel/interfaces/httpapi`
  - Expected: FAIL。
- [ ] **Step 3: 实现 impact service/repo/handler**
  - 确定性候选 + 一次 AI 推理（可 degraded）；`source/confidence/reason/evidence`；选择 CAS/审计；事件 `code.impact_computed`。
- [ ] **Step 4: 运行测试**
  - `cd backend; go test ./internal/codeintel/...`
  - Expected: PASS。
- [ ] **Step 5: Commit**
  - `git add backend/internal/codeintel`
  - `git commit -m "feat(p5): add impact analysis"`

---

## Task 11: 页面/API 提议与资源确认（W2）

**Files:**
- Create: `backend/internal/codeintel/application/proposal_service.go`、`backend/internal/codeintel/interfaces/httpapi/proposal_handler.go`
- Modify: `backend/internal/resource/application/service.go`（proposed 创建/confirm/reject）、`backend/internal/resource/interfaces/httpapi/*`（4 ops）
- Test: `backend/internal/codeintel/application/proposal_service_test.go`、`backend/internal/codeintel/interfaces/httpapi/proposal_handler_test.go`、`backend/internal/resource/application/*_test.go`、`backend/internal/resource/interfaces/httpapi/*_test.go`

**Interfaces:**
- Consumes: Task 8 facts、Task 9 anchors、ExecutionReader network evidence、resource catalog。
- Produces:
```go
type ProposalService struct { /* factRepo, analysisRepo, resources, execution, events, audit, clock, ids, settings */ }
func (s *ProposalService) Generate(ctx context.Context, versionID, repoID int64) (ProposalResult, error)
func (s *ProposalService) List(ctx context.Context, versionID int64) ([]ProposalView, error)
type ProposalResult struct { PagesCreated, PagesSkipped, APIsCreated, APISkipped int }
type ProposalView struct { Kind string; ID int64; Name, URL, RouteTemplate, Method, PathTemplate, Source, Status string }
```
- resource service 增量：
```go
func (s *Service) CreateProposedPage(ctx context.Context, in ProposedPageInput) (PageView, error)
func (s *Service) CreateProposedAPI(ctx context.Context, in ProposedAPIInput) (APIView, error)
func (s *Service) ConfirmPage(ctx context.Context, pageID, actorID int64) (PageView, error)
func (s *Service) RejectPage(ctx context.Context, pageID, actorID int64) (PageView, error)
func (s *Service) ConfirmAPI(ctx context.Context, apiID, actorID int64) (APIView, error)
func (s *Service) RejectAPI(ctx context.Context, apiID, actorID int64) (APIView, error)
```
- Handler：生成/列表 2 ops；resource confirm/reject 4 ops；admin-only；审计/事件。

- [ ] **Step 1: 写失败测试**
  - 生成：代码 page/route→page、api/client_call→api；network 证据 host 匹配；去重（approved 跳过/proposed 更新/rejected 不重提）；source ai/code；created/skipped。
  - 确认：admin confirm→approved；reject→rejected；非 admin 403；重复 409。
  - handler：200/201/403/404/409/422；ID 字符串。
- [ ] **Step 2: 运行确认失败**
  - `cd backend; go test ./internal/codeintel/application ./internal/resource/...`
  - Expected: FAIL。
- [ ] **Step 3: 实现 proposal service/handler + resource 增量**
  - 复用 resource 唯一索引去重；写 proposed/source；审计 actor；事件 `code.page_api_proposed`；不改既有 CRUD 语义。
- [ ] **Step 4: 运行测试**
  - `cd backend; go test ./internal/codeintel/... ./internal/resource/...`
  - Expected: PASS。
- [ ] **Step 5: Commit**
  - `git add backend/internal/codeintel backend/internal/resource`
  - `git commit -m "feat(p5): add page and API proposals"`

---

## Task 12: Agent 工具与 profile 白名单（W2）

**Files:**
- Create: `backend/internal/agent/application/tools/{p5_registry.go,code_tools.go,p5_visual_tools.go,p5_registry_test.go}`、`backend/internal/agent/application/p5_profiles.go`
- Modify: `backend/internal/agent/application/tools/registry.go`（`Deps` 增加 codeintel/visual 服务字段）、`backend/internal/agent/application/p4_registry.go`（`NewP5Registry` 组合）、profile seed 入口
- Test: `backend/internal/agent/application/tools/p5_registry_test.go`、`backend/internal/agent/application/p5_profiles_test.go`

**Interfaces:**
- Consumes: Task 7–11 codeintel services、Task 15/17 visual/execution services、Task 2 agent scope。
- Produces:
```go
func RegisterP5ToolGroup(fn func(deps Deps) []Tool)
func NewP5Registry(deps Deps) *Registry // P4 base + P5 groups
// tools: code.tree/code.read/code.search/code.facts/code.routes/code.apis/code.diff/code.impact/code.anchors
//        execution.visual_assert, sut.page_propose
type CodeTool struct { /* codeintel service interfaces */ }
func (t *CodeTool) Name() string; Description() string; Parameters() json.RawMessage; Execute(ctx, scope, raw) (any, error)
```
- profile 白名单：按 spec §4.4（context_scout/case_author/failure_analyst/design_planner/case_executor）。

- [ ] **Step 1: 写失败测试**
  - registry `NewP5Registry` 含 11 个新工具 + P1–P4 工具；每个新工具 `Parameters()` 是 object，不含 `versionId`；未知工具错误。
  - `code.tree` scope version_sut 解析 repo；多仓无 repoId 返回 `invalid_argument`；结果 >32 KiB 时返回 ref/head。
  - `sut.page_propose` 只写 proposed；`execution.visual_assert` 仅 case_run scope 可用。
  - profile 白名单：5 个 profile 含预期工具；其他 profile 不含 code/visual 写工具。
- [ ] **Step 2: 运行确认失败**
  - `cd backend; go test ./internal/agent/application/tools ./internal/agent/application`
  - Expected: FAIL。
- [ ] **Step 3: 实现 tools/registry/profiles**
  - 工具 handler 只调用 application 服务；scope 注入；参数校验/限流/错误码；结果超限走 `content_object` + `{head,sha256,content_ref,truncated}`。
  - `Deps` 增加 codeintel/visual 服务接口；`NewP5Registry` 组合；profile 白名单合并。
- [ ] **Step 4: 运行测试**
  - `cd backend; go test ./internal/agent/...`
  - Expected: PASS。
- [ ] **Step 5: Commit**
  - `git add backend/internal/agent`
  - `git commit -m "feat(p5): register code and visual agent tools"`

---

## Task 13: Visual 领域、仓储与服务（W3）

**Files:**
- Create: `backend/internal/visual/domain/{assertion.go,baseline.go,diff.go,classification.go,limits.go,errors.go}`、`backend/internal/visual/application/{ports.go,config_service.go,baseline_service.go,diff_service.go,overview_service.go}`、`backend/internal/visual/infrastructure/postgres/{assertion_repo.go,baseline_repo.go,diff_repo.go,overview_repo.go}`
- Test: `backend/internal/visual/domain/*_test.go`、`backend/internal/visual/application/*_test.go`、`backend/internal/visual/integration/*_test.go`

**Interfaces:**
- Consumes: spec §3.5、§7.1–§7.5；Task 1 表；design/execution readers。
- Produces:
```go
type Classification string // passed/failed/inconclusive/blocked
type Assertion struct { ID, VersionID, CaseVersionID, CaseAssertionID int64; StepSeq, AssertionSeq int; VersionSUTID int64; Selector string; Region json.RawMessage; Threshold float64; ViewportWidth, ViewportHeight int; DeviceScaleFactor float64; WaitPolicy json.RawMessage; ConfigRevision int; Status string; CreatedBy *int64; CreatedAt, UpdatedAt time.Time }
type Baseline struct { ID, VisualAssertionID, CaseVersionID, VersionSUTID, EvidenceID int64; SHA256 string; SizeBytes int64; Width, Height, ViewportWidth, ViewportHeight int; DeviceScaleFactor float64; ConfigRevision int; EnvironmentFingerprint string; Status string; ApprovedBy int64; ApprovedAt time.Time; SupersededAt *time.Time; CreatedAt, UpdatedAt time.Time }
type Diff struct { ID, VisualAssertionID, CaseRunID, AssertionResultID int64; BaselineID *int64; ActualEvidenceID int64; DiffEvidenceID *int64; DiffRatio, Threshold float64; Classification Classification; Reason string; EnvironmentMatch bool; Width, Height int; MismatchBoxes json.RawMessage; CreatedAt, UpdatedAt time.Time }

type ConfigService struct { /* repo, caseReader, audit, events, clock, ids, settings */ }
func (s *ConfigService) Upsert(ctx context.Context, versionID, caseAssertionID, actorID int64, in ConfigInput) (Assertion, error)
func (s *ConfigService) List(ctx context.Context, versionID int64, caseVersionID, versionSUTID *int64) ([]Assertion, error)
func (s *ConfigService) Get(ctx context.Context, id int64) (Assertion, error)

type BaselineService struct { /* repo, assertionRepo, evidenceReader, audit, events, clock, ids, settings */ }
func (s *BaselineService) Approve(ctx context.Context, versionID, assertionID, evidenceID, actorID int64) (Baseline, error)
func (s *BaselineService) List(ctx context.Context, assertionID int64) ([]Baseline, error)
func (s *BaselineService) Active(ctx context.Context, assertionID int64) (Baseline, bool, error)

type DiffService struct { /* repo, assertionRepo, clock, ids */ }
func (s *DiffService) Record(ctx context.Context, in RecordDiffInput) (Diff, error)
func (s *DiffService) List(ctx context.Context, versionID int64, filter DiffFilter) ([]Diff, error)
func (s *DiffService) Get(ctx context.Context, id int64) (Diff, error)

type OverviewService struct { /* diffRepo, assertionRepo, baselineRepo, clock */ }
func (s *OverviewService) Version(ctx context.Context, versionID int64) (Overview, error)
func (s *OverviewService) Global(ctx context.Context, versionID *int64) (Overview, error)

type CaseReader interface { VisualAssertionContext(ctx context.Context, versionID, caseAssertionID int64) (CaseAssertionRef, error) }
type EvidenceReader interface { VisualEvidence(ctx context.Context, versionID, evidenceID int64) (VisualEvidence, error) }
type AssertionRepository interface { Upsert(ctx, a Assertion) (Assertion,error); Get(ctx,id) (Assertion,error); List(ctx, versionID int64, caseVersionID, versionSUTID *int64) ([]Assertion,error); ArchiveOthers? }
type BaselineRepository interface { InsertApproved(ctx, b Baseline) (Baseline,error); SupersedeApproved(ctx, assertionID int64, at time.Time) error; List(ctx, assertionID int64) ([]Baseline,error); Active(ctx, assertionID int64) (Baseline,bool,error) }
type DiffRepository interface { Insert(ctx, d Diff) (Diff,error); List(ctx, versionID int64, filter DiffFilter) ([]Diff,error); Get(ctx,id) (Diff,error); Overview(ctx, versionID *int64) (Overview,error) }
```
- `ConfigInput{Upsert}`、`RecordDiffInput`、`Overview` 字段按 spec §4.2/§7.5；阈值/视口/DPR/region 校验；config_revision++ 时 supersede active baseline（同事务）。
- `classification.go`：`Classify(envMatch bool, hasBaseline bool, diffRatio, threshold float64) Classification` 固定顺序（无基线/环境不一致→inconclusive；diff>threshold→failed；else passed）；blocked 由执行错误单独设置。

- [ ] **Step 1: 写失败测试**
  - config：非法 selector/region/threshold/viewport/DPR 422；upsert revision++；旧 approved baseline superseded；未变化不升版。
  - baseline：非 visual_actual/跨 version/case/assertion/SUT 证据 403/422；PNG 尺寸/sha/size/config revision；部分唯一；supersede；admin-only 由 API 层测。
  - classification：无基线/环境不一致/diff 超阈/匹配四态；blocked 单独。
  - diff record：唯一 assertion_result；ratio/boxes；overview 指标（覆盖率/通过率/原因/p50/p95）。
- [ ] **Step 2: 运行确认失败**
  - `cd backend; go test ./internal/visual/domain ./internal/visual/application`
  - Expected: FAIL。
- [ ] **Step 3: 实现 domain/repos/services**
  - 只依赖 stdlib + platform；postgres 实现 3 表；baseline 事务 supersede+insert；overview SQL/聚合。
- [ ] **Step 4: 运行测试**
  - `cd backend; go test ./internal/visual/...`
  - Expected: PASS。
- [ ] **Step 5: Commit**
  - `git add backend/internal/visual`
  - `git commit -m "feat(p5): add visual domain and services"`

---

## Task 14: Visual HTTP API 与质量指标（W3）

**Files:**
- Create: `backend/internal/visual/interfaces/httpapi/{routes.go,handlers.go,views.go,errors.go}`
- Modify: `backend/internal/server/server.go`（挂载路由）、`backend/internal/visual/application/ports.go`（evidence reader 适配）
- Test: `backend/internal/visual/interfaces/httpapi/*_test.go`、`backend/internal/server/*_test.go`

**Interfaces:**
- Consumes: Task 13 services；Task 16 execution evidence reader 适配（可先 fake）。
- Produces：spec §4.2 #30–#38 的 9 个 visual ops + `GET /api/quality/visual-metrics`；admin 中间件用于 baseline approve；登录用于其他。
- Handler 只做 DTO/parse/error map；ID 字符串。

- [ ] **Step 1: 写失败测试**
  - config：GET/PUT/GET detail 200/400/401/404/422；upsert 返回最新 revision。
  - baseline：POST 201；非 admin 403；非法证据 422；GET history 200；重复批准 supersede。
  - diffs：列表过滤/分页；detail 200/404；overview/quality metrics 200。
  - 路由：未登录 401；错误信封；ID 字符串；无文件系统路径/正文。
- [ ] **Step 2: 运行确认失败**
  - `cd backend; go test ./internal/visual/interfaces/httpapi ./internal/server`
  - Expected: FAIL。
- [ ] **Step 3: 实现 handler/routes**
  - 按 spec §4.2 schema；admin baseline；`/api/quality/visual-metrics` 登录；错误映射；审计 actor。
- [ ] **Step 4: 运行测试**
  - `cd backend; go test ./internal/visual/... ./internal/server`
  - Expected: PASS。
- [ ] **Step 5: Commit**
  - `git add backend/internal/visual backend/internal/server`
  - `git commit -m "feat(p5): add visual HTTP API and quality metrics"`

---

## Task 15: Executor `visual.compare` 协议（W3）

**Files:**
- Modify: `executor/src/actions.mjs`、`executor/src/visual.mjs`、`executor/src/security.mjs`、`executor/src/server.mjs`（限额）、`executor/test/visual.test.mjs`、`docs/version-V0.2/contracts/executor-protocol.md`（Task 0 已写）
- Test: `cd executor; pnpm test -- visual`

**Interfaces:**
- Consumes: 0.1 `visual.mjs` pixelmatch/pngjs（已存在）；Task 0 协议 v1.2。
- Produces：
```js
// actions.mjs HANDLERS: "visual.compare": (entry, args, ctx) => runVisualCompare(entry, args, ctx.timeoutMs)
// visual.mjs 已有 runVisualCompare(entry,args,timeoutMs)；本任务补齐
// capture-only (args.baseline 空) / environmentMatch / actual_png+diff_png 返回值
```
- action `visual.compare` 注册到 `HANDLERS`/`KNOWN_ACTIONS`；本 action 请求上限 `visual.baseline_max_bytes`（默认 5 MiB；executor `MAX_ACTION_REQUEST_BYTES` 为 16 MiB，需在该 action 收紧）；PNG 校验；环境指纹 viewport/DPR/selector/region/config revision/浏览器版本；等待 networkidle/fonts/animation freeze。

- [ ] **Step 1: 写失败测试**
  - 相同 PNG→diffRatio 0/passed 输入；改像素→ratio>threshold/failed；region 裁剪；threshold 边界；环境 DPR/viewport 不一致→environmentMatch=false/no diff；capture-only（baseline null）→actual 只有；PNG 非法/超限→错误。
- [ ] **Step 2: 运行确认失败**
  - `cd executor; pnpm test -- visual`
  - Expected: FAIL（action/参数未接）。
- [ ] **Step 3: 实现/适配**
  - 将 `runVisualCompare` 接入 `HANDLERS`/`KNOWN_ACTIONS`；补齐 baseline 可选/capture-only、environment fingerprint、diff artifact；请求/证据限额；错误码。
- [ ] **Step 4: 运行测试**
  - `cd executor; pnpm test`
  - Expected: PASS。
- [ ] **Step 5: Commit**
  - `git add executor`
  - `git commit -m "feat(p5): add visual compare executor action"`

---

## Task 16: 执行侧视觉断言集成（W3）

**Files:**
- Create: `backend/internal/execution/application/visual_assert.go`、`backend/internal/execution/application/agent_tool/visual_assert.go`、`backend/internal/execution/infrastructure/visualadapter/*`
- Modify: `backend/internal/execution/domain/{verdict.go,case_run.go}`、`backend/internal/execution/application/evidence_service.go`、`backend/internal/execution/interfaces/httpapi/views.go`、`backend/internal/agent/application/p3_profiles.go`（case_executor allowed tools）
- Test: `backend/internal/execution/application/visual_assert_test.go`、`backend/internal/execution/domain/verdict_test.go`、`backend/internal/execution/interfaces/httpapi/*_test.go`

**Interfaces:**
- Consumes: Task 13 visual services、Task 15 executor action、Task 3 evidence kinds、Task 12 tool registry。
- Produces：
```go
type VisualAssertService struct { /* caseRuns, assertions, visual config/baseline, evidence, executor, clock, ids, events, audit */ }
func (s *VisualAssertService) Assert(ctx context.Context, in VisualAssertInput) (VisualAssertResult, error)
type VisualAssertInput struct { VersionID, CaseRunID, CaseVersionID, AssertionID, VersionSUTID int64; StepSeq, AssertionSeq int; AccountRole string }
type VisualAssertResult struct { AssertionResultID int64; Verdict string; Reason string; VisualDiffID *int64; ActualEvidenceID *int64; DiffEvidenceID *int64; EnvironmentMatch bool; DiffRatio float64 }
```
- 工具 `execution.visual_assert`：`{assertionId}`；idempotent replay；仅 case_run scope。
- 最终保护：visual 断言无 result→case inconclusive `visual_assertion_not_run`；summary 新增 `visualPassed/visualFailed/visualInconclusive`，`visualSkipped` 兼容为 0；case_run 四态聚合。
- 证据：actual 始终；diff 仅比对时；sha/size 校验；`visual_diff` 记录。

- [ ] **Step 1: 写失败测试**
  - 无基线：actual 落证据、inconclusive(missing_visual_baseline)、case inconclusive、不产问题。
  - 有基线匹配：passed、diff 0、case passed；超阈：failed(application)、diff boxes；环境不一致：inconclusive(environment)、无 diff；配置变更/基线失效：inconclusive。
  - 幂等：重复调用只一行 visual_diff/assertion_result；不重复证据。
  - finalizer：visual 未运行→case inconclusive；禁止 visual skipped 假通过。
  - summary/evidence kind/API view。
- [ ] **Step 2: 运行确认失败**
  - `cd backend; go test ./internal/execution/...`
  - Expected: FAIL。
- [ ] **Step 3: 实现 service/tool/finalizer**
  - 通过 visual app 端口读 config/baseline；execution evidence service 写 actual/diff；executor client 调 `visual.compare`；写 assertion_result/visual_diff；审计/事件。
- [ ] **Step 4: 运行测试**
  - `cd backend; go test ./internal/execution/... ./internal/agent/...`
  - Expected: PASS。
- [ ] **Step 5: Commit**
  - `git add backend/internal/execution backend/internal/agent`
  - `git commit -m "feat(p5): execute visual assertions"`

---

## Task 17: 前端代码与变更 Tab（W4）

**Files:**
- Create: `frontend/src/features/code-intel/{api.ts,types.ts,CodeTab.tsx}` + `components/{RepositoryStatus,IndexRunStatus,ChangedFilesPanel,DiffDrawer,FactsPanel,FactReviewDialog,ImpactPanel,UnmappedPanel,AnchorsPanel,ProposalPanel}.tsx` + `__tests__/*.test.tsx`
- Modify: `frontend/src/features/versions/VersionDetailPage.tsx`（code Tab 替换占位）、`frontend/src/features/versions/types.ts`（codeBaselineCommit/summary 视觉计数）
- Test: `cd frontend; pnpm test -- code-intel`

**Interfaces:**
- Consumes: Task 0 OpenAPI；Task 7–11 API；`lib/api-client.ts`；`lib/sse-client`/`useVersionEvents`。
- Produces:
```ts
export function CodeTab({ versionId }: { versionId: string }): JSX.Element
// api.ts: listCodeRepos, triggerIndex, latestIndexRun, listTree, readFile, listDiff, readDiffFile,
// listFacts, searchCode, startAnalysis, latestAnalysis, getAnalysis, patchCandidate,
// listAnchors, createAnchor, validateAnchor, deleteAnchor, listReviews, createReview,
// generateProposals, listProposals
```
- 状态：未绑定/无 baseline/未索引/无变更/无分析/degraded/truncated/空错载；`?tab=code&repo=&analysis=&anchor=&fact=`；SSE `code.*` debounce 刷新。

- [ ] **Step 1: 写失败测试**
  - `CodeTab.test.tsx`：空态/加载/错误/未绑定；索引触发 202→running→done；分析候选勾选 PATCH；diff 抽屉截断/二进制；事实复核；深链解析。
  - `api.test.ts`：路径/参数/ID 字符串/错误映射。
- [ ] **Step 2: 运行确认失败**
  - `cd frontend; pnpm test -- code-intel`
  - Expected: FAIL。
- [ ] **Step 3: 实现 feature + 组件 + VersionDetailPage 接线**
  - 只走 `api-client`/`sse-client`；无直接 fetch/EventSource；不新增全局 store；复用 `components/ui`。
- [ ] **Step 4: 运行测试/类型检查**
  - `cd frontend; pnpm test -- code-intel && pnpm typecheck`
  - Expected: PASS。
- [ ] **Step 5: Commit**
  - `git add frontend/src/features/code-intel frontend/src/features/versions`
  - `git commit -m "feat(p5): add code and changes tab"`

---

## Task 18: 前端视觉子视图与质量页（W5）

**Files:**
- Create: `frontend/src/features/visual/{api.ts,types.ts,VisualView.tsx,QualityMetricsPanel.tsx,components/{VisualAssertionList,VisualComparisonCard,BaselineApprovalDialog,VisualDiffViewer,VisualConfigEditor}.tsx,__tests__/*}`
- Modify: `frontend/src/features/execution/ExecutionTab.tsx`（`view=visual`）、`ExecutionTab` props/测试、`components/EvidenceViewer`（visual kind 标签/图像）、`frontend/src/app/(app)/quality/page.tsx`
- Test: `cd frontend; pnpm test -- visual execution quality`

**Interfaces:**
- Consumes: Task 14 visual API、Task 16 execution summary/diff API、Task 0 OpenAPI。
- Produces:
```ts
export function VisualView({ versionId }: { versionId: string }): JSX.Element
export function VisualConfigEditor({ versionId, caseVersionId, assertion, onSaved }): JSX.Element
export function QualityMetricsPanel({ versionId }: { versionId?: string }): JSX.Element
// ExecutionTab props 增加 view?: "waves"|"visual"；URL ?view=visual 同步
```
- 权限：admin 批准基线；非 admin 隐藏/403；四态徽标；actual/diff 查看；retest 入口；空/错/载；不新增一级路由。

- [ ] **Step 1: 写失败测试**
  - VisualView：四态/原因/baseline 状态/证据链接；无基线提示；admin 批准调用/非 admin 隐藏；retest。
  - ExecutionTab：`?view=visual` 切换、保留波次状态；QualityMetricsPanel 指标/空态；VisualConfigEditor 保存/复制。
- [ ] **Step 2: 运行确认失败**
  - `cd frontend; pnpm test -- visual execution quality`
  - Expected: FAIL。
- [ ] **Step 3: 实现 feature/子视图/质量页**
  - 复用 EvidenceViewer/Diff 抽屉；`api-client`；SSE `visual.*` 刷新；不编造指标。
- [ ] **Step 4: 运行测试/类型检查**
  - `cd frontend; pnpm test -- visual execution quality && pnpm typecheck`
  - Expected: PASS。
- [ ] **Step 5: Commit**
  - `git add frontend/src/features/visual frontend/src/features/execution frontend/src/app/(app)/quality`
  - `git commit -m "feat(p5): add visual assertion view and quality metrics"`

---

## Task 19: 前端锚点、提议与设计视觉配置（W5）

**Files:**
- Create: `frontend/src/features/{design,clarification,delivery,resources}/components/{AnchorPanel,ProposalPanel}.tsx` + tests（按归属拆分）
- Modify: `frontend/src/features/design/CaseDetailPanel.tsx`（visual config + case 锚点）、`frontend/src/features/clarification/RequirementEditor.tsx`（需求锚点）、`frontend/src/features/delivery/IssueDetailSheet.tsx`（问题锚点）、`frontend/src/features/resources/PageAPIPanel.tsx`（proposed/confirm/reject/生成）、`features/resources/api.ts`
- Test: `cd frontend; pnpm test -- design clarification delivery resources`

**Interfaces:**
- Consumes: Task 9 anchors/review API、Task 11 proposals/resource API、Task 18 VisualConfigEditor。
- Produces：AnchorPanel（list/add/validate/delete，valid/stale/broken）、ProposalPanel/资源页按钮、Design Case visual 配置入口。

- [ ] **Step 1: 写失败测试**
  - 锚点：valid/stale/broken 渲染；添加/校验/删除；路径复制；权限/错误。
  - 提议：proposed 行/来源；admin confirm/reject；“从代码生成提议” created/skipped；非 admin 隐藏。
  - 设计：visual assertion 配置保存/复制；不改变 P2 确认语义。
- [ ] **Step 2: 运行确认失败**
  - `cd frontend; pnpm test -- design clarification delivery resources`
  - Expected: FAIL。
- [ ] **Step 3: 实现组件/接线**
  - 只走 `api-client`；复用 `components/ui`；保持既有测试/交互。
- [ ] **Step 4: 运行测试/类型检查**
  - `cd frontend; pnpm test -- design clarification delivery resources && pnpm typecheck`
  - Expected: PASS。
- [ ] **Step 5: Commit**
  - `git add frontend/src/features`
  - `git commit -m "feat(p5): add anchors proposals and visual config UI"`

---

## Task 20: P5 fixtures（W6）

**Files:**
- Create: `scripts/fixture-p5-repo.ps1`、`scripts/fixture-p5-target.cjs`
- Test: 运行脚本 → 输出 SHA/`git status --porcelain` 空；fixture target healthz/页面/API。

**Interfaces:**
- `fixture-p5-repo.ps1` 输出 `P5_FIXTURE_REPO/BASE_COMMIT/HEAD_COMMIT`；两个固定 commit；`.tmp/p5-repo`；不污染工作树。
- `fixture-p5-target.cjs` 监听 13338；`/p5/orders`、`/p5/visual?theme=light|shifted`、`/p5/api/orders`、`/healthz`。

- [ ] **Step 1: 写脚本/自检**
  - 固定作者/日期/LF；commit A/B 内容按 spec §12.2；theme shifted 改变颜色/布局；输出机器可读行。
- [ ] **Step 2: 运行验证**
  - `powershell.exe -File scripts/fixture-p5-repo.ps1` → 两个 40hex SHA；二次运行同 SHA；`git -C .tmp/p5-repo status --porcelain` 空。
  - `node scripts/fixture-p5-target.cjs`（后台）+ `Invoke-WebRequest http://127.0.0.1:13338/healthz` → 200；`/p5/visual?theme=light` 与 `shifted` HTML 不同。
- [ ] **Step 3: Commit**
  - `git add scripts/fixture-p5-*`
  - `git commit -m "test(p5): add deterministic fixtures"`

---

## Task 21: P5 API/DB 验收脚本（W6）

**Files:**
- Create: `scripts/p5-acceptance.ps1`
- Test: `powershell.exe -File scripts/p5-acceptance.ps1 -PollTimeoutSeconds 900`

**Interfaces:**
- 复用 P4 验收 helper 模式：`Invoke-Api/Assert-Status/Wait-For/Start-Api/Start-Executor/Start-Fixture/Stop-Services`；参数 `-ApiPort 14345 -WebPort 16870 -ExecutorPort 14347 -FixturePort 13338 -DbName eztest_v02_p5 -PollTimeoutSeconds 900 -SkipBuild -KeepRunning`。
- 场景 S0–S12（spec §12.3）；真实 DeepSeek；真实 Chrome；`.tmp/p5-acceptance/{acceptance.log,scenarios.txt}`。

- [ ] **Step 1: 写脚本骨架与 S0/S1**
  - 启动 DB/API/Web/executor/fixture；登录 admin/user；建 SUT/environment/pages/apis/code repo/version bindings/requirements/cases；断言 31 迁移。
- [ ] **Step 2: 加 S2–S7（代码智能/锚点/复核/提议）**
  - 真实 LLM 索引/分析；事实/证据/影响/设计 Agent 读码；锚点 valid/stale/broken；复核；提议 confirm/reject。
- [ ] **Step 3: 加 S8–S11（视觉）**
  - 无基线 inconclusive；admin 批准/retest passed；theme shifted failed；viewport/config mismatch inconclusive；证据下载 sha/size。
- [ ] **Step 4: 加 S12 + 场景汇总**
  - 质量指标/code baseline commit；P1/P2 fake、P3/P4 真实回归；失败写 `scenarios.txt` 并 exit 1。
- [ ] **Step 5: 运行验收**
  - `powershell.exe -File scripts/p5-acceptance.ps1 -PollTimeoutSeconds 900`
  - Expected: EXIT=0；S0–S12 PASS；日志/证据路径。
- [ ] **Step 6: Commit**
  - `git add scripts/p5-acceptance.ps1`
  - `git commit -m "test(p5): add real LLM and Chrome acceptance"`

---

## Task 22: P5 浏览器 E2E（W6）

**Files:**
- Create: `scripts/p5-e2e.ps1`、`frontend/e2e/p5.spec.ts`
- Test: `powershell.exe -File scripts/p5-e2e.ps1`

**Interfaces:**
- 复用 P4 E2E helper/phase 模式；真实 API/DB/executor/fixture；Playwright 真实 Chrome；截图 `.tmp/p5-acceptance/screenshots/`；失败 trace。
- 覆盖：代码 Tab（索引/事实/影响/锚点/复核/提议）、视觉视图（无基线/批准/差异/证据）、资源确认、质量页、深链、权限、空错载。

- [ ] **Step 1: 写 spec/脚本骨架**
  - `test("phase A: ...")` 登录/建版本/打开代码 Tab；`test("phase B: ...")` 视觉/质量/资源；截图/无控制台错误。
- [ ] **Step 2: 运行 E2E**
  - `powershell.exe -File scripts/p5-e2e.ps1`
  - Expected: PASS；截图/HTML；`.tmp/p5-acceptance/e2e.log`。
- [ ] **Step 3: Commit**
  - `git add scripts/p5-e2e.ps1 frontend/e2e/p5.spec.ts`
  - `git commit -m "test(p5): add browser e2e"`

---

## Task 23: 装配、路由、API 版本与契约检查（Lead）

**Files:**
- Create: `backend/cmd/eztest/p5_wiring.go`、`backend/cmd/eztest/p5_wiring_test.go`、`scripts/p5-contract-check.cjs`
- Modify: `backend/cmd/eztest/main.go`（版本 `0.2.0-p5`、codeintel/visual contexts/workers/adapters、tools/profiles/seed、routes）、`backend/internal/server/server.go`（Deps + mounts）、`backend/internal/agent/application/{profiles.go,p4_registry.go}`（P5 registry/profiles）、`backend/internal/agent/application/tools/registry.go`（Deps）
- Test: `cd backend; go test ./cmd/eztest ./internal/server`；`node scripts/p5-contract-check.cjs`

**Interfaces:**
- Consumes: Task 1–22 全部。
- Produces：可启动的 P5 API/worker/路由/工具/设置；`p5-contract-check.cjs` 计数；`main.go` version p5。

- [ ] **Step 1: 写装配/路由失败测试**
  - `server.New` Deps 增加 CodeIntel/Visual；`/api/code/*`、`/api/versions/{id}/code/*`、`/api/versions/{id}/visual/*`、`/api/quality/visual-metrics` 401/200；API version `0.2.0-p5`。
  - codeintel/visual services/workers 由 main 装配；work item team code；tools/profiles seed。
- [ ] **Step 2: 运行确认失败**
  - `cd backend; go test ./internal/server ./cmd/eztest`
  - Expected: FAIL。
- [ ] **Step 3: 实现 wiring/routes/version/seed**
  - `p5_wiring.go` 构造 adapters（version/design/resource/execution/eventing/audit/workqueue）；启动 codeintel worker；注册 tools/profiles；server Deps；`main.go` version。
- [ ] **Step 4: 写 `p5-contract-check.cjs`**
  - 复用 P4 helper；断言 38 ops/31 迁移/11 表/12 事件/2 任务+code team/11 工具/16 设置/10 证据/6 team；p2/p3/p4 前向兼容。
- [ ] **Step 5: 运行检查**
  - `cd backend; go build ./...`
  - `node scripts/p5-contract-check.cjs`
  - Expected: PASS。
- [ ] **Step 6: Commit**
  - `git add backend/cmd/eztest backend/internal/server backend/internal/agent scripts/p5-contract-check.cjs`
  - `git commit -m "feat(p5): wire codeintel and visual"`

---

## Task 24: 全量门禁与回归（Lead）

**Files:**
- Modify: `scripts/p2-contract-check.cjs`/`p3`/`p4`（仅前向兼容需要处）、`backend/internal/db/migrate_*_test.go`（若 Task 1 未改）
- Test: `scripts/test.ps1`、`scripts/p5-acceptance.ps1`、`scripts/p5-e2e.ps1`、P1–P4 回归

- [ ] **Step 1: 运行全量门禁**
  - `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1` → EXIT=0；日志 `.tmp/p5/final-gate.log`。
- [ ] **Step 2: 运行契约检查**
  - `node scripts/p2-contract-check.cjs && node scripts/p3-contract-check.cjs && node scripts/p4-contract-check.cjs && node scripts/p5-contract-check.cjs` → PASS。
- [ ] **Step 3: 迁移幂等**
  - `cd backend; go test -tags=integration ./internal/db/...` + 验收二次启动 → 31 applied once；二次 0。
- [ ] **Step 4: 运行 P5 验收/E2E**
  - `scripts/p5-acceptance.ps1 -PollTimeoutSeconds 900`；`scripts/p5-e2e.ps1` → PASS；证据路径。
- [ ] **Step 5: 运行 P1–P4 回归**
  - P1/P2 fake；P3 真实；P4 真实；P2/P4 E2E；记录 `.tmp/p5/*-regression.log`。
- [ ] **Step 6: 修复后重跑并 Commit**
  - 真实失败按根因修复；提交修复；重跑受影响门禁。
  - `git commit -m "test(p5): pass final gate and regressions"`

---

## Task 25: 契约冻结与 Handoff（Lead）

**Files:**
- Modify: `docs/version-V0.2/contracts/README.md`（P5 frozen/计数）、`docs/version-V0.2/phases/phase-05-handoff.md`（新建）
- Test: `node scripts/p5-contract-check.cjs`；handoff 引用证据存在

- [ ] **Step 1: 冻结 contracts**
  - `openapi.yaml` 已 `0.2.0-p5`/`x-frozen: P5`；各 markdown 增量标 `frozen: P5`；README 记录计数与验收证据。
- [ ] **Step 2: 写 `phase-05-handoff.md`**
  - 按 spec §18：完成范围、契约变更、测试/验收证据、已知缺口/偏差、未交付移交表、P6 输入、前置检查、根因/流程修复、风险、停止声明。
- [ ] **Step 3: 核对证据路径**
  - `.tmp/p5/final-gate.log`、`p5-acceptance/acceptance.log`、`scenarios.txt`、`p5-acceptance/e2e.log`、`screenshots/`、P1–P4 回归日志；缺失则重跑。
- [ ] **Step 4: 最终检查**
  - `git status` 不含用户 dirty 文件；`git log` 阶段提交；P5 分支保留。
- [ ] **Step 5: Commit**
  - `git add docs/version-V0.2/contracts docs/version-V0.2/phases/phase-05-handoff.md`
  - `git commit -m "docs(p5): freeze contracts and add phase handoff"`
- [ ] **Step 6: 停止**
  - 报告完成/证据/风险/下一阶段输入；不自动进入 P6。

---

## Self-Review

**1. Spec coverage**

| Spec 节 | 任务 |
|---|---|
| §0 根因/阅读清单 | 0（契约依据）/25（handoff 根因） |
| §3 数据模型/迁移 | 0/1 |
| §4 API/事件/任务/工具/设置/协议 | 0/2/3/7–12/14/16/23 |
| §5 代码智能 T0/T1/索引/影响/基线 | 4/6/7/8/10 |
| §6 锚点/事实复核 | 9 |
| §7 视觉配置/基线/四态/证据 | 13/14/15/16/18 |
| §8 提议/质量页 | 11/14/18/19 |
| §9 前端 | 17/18/19 |
| §10 SSE/异步 | 7/8/10/16/17/18 |
| §11 配置/迁移/启动 | 0/1/3/23 |
| §12 验收 | 20/21/22/24 |
| §13 测试/门禁 | 1/24 |
| §14 契约冻结 | 0/25 |
| §15 并行映射 | 本 plan 的执行方式 |
| §16 P5-D | 各任务；偏差在 25 handoff |
| §17 风险 | 17/18/24/25 |
| §18 handoff | 25 |

**2. Step scan**：任务均按“失败测试→运行失败→实现→运行通过→提交”拆分；无 TBD/“handle edge cases”/未定义函数；新函数/类型在 Task 5/13 定义，后续任务引用其 Interfaces。

**3. Type consistency**：`FactExtractor/WorkQueue/VersionReader/DesignReader/ResourceReader/ExecutionReader` 在 Task 5 定义；`FactExtractInput/FactDraft` 在 Task 8；`ImpactService/CandidateRepository` Task 10；`AnchorRepository/ReviewRepository` Task 9；`ConfigService/BaselineService/DiffService/OverviewService` Task 13；`VisualAssertService` Task 16；`ExecutionTabProps`/`CodeTab` Task 17/18；`server.Deps`/`p5-contract-check` Task 23。名称与 Task 5/13 一致。

**4. Review Focus**：多 SUT/多仓→Task 4/6/7/10/16/21；LLM 证据→Task 8/9/10/12/21；视觉确定性→Task 13/15/16/21；work_item 路由→Task 2/7/23；契约/旧回归→Task 0/1/23/24。

**5. Proportion**：plan 约 80 KB，spec 约 107 KB；以文件/签名/测试/命令为主，未复写实现体；25 个任务对应 spec 的 P5 XL 范围。

**6. 已知实现期需核对（不阻塞）**：
- `cmd/eztest/main.go` 版本常量 `0.2.0-p4` 改 `0.2.0-p5`（Task 23）。
- `versionapp.New` 需增加 resolver 参数（Task 4）。
- `executor` 已有 `runVisualCompare`，Task 15 是接线/契约适配，不是从零实现。
- P1 `WorkQueue.Claim` 的现有调用点仅 `worker.go:188`；新增 `ClaimForTeams` 后确保旧 `Claim` 仍可用。
- 契约计数以 `p5-contract-check.cjs` 实际解析为准；spec 的 38/31/11/12/2/11/16/10/6 是目标，若 scripts 与 spec 解析差异，以 spec 为准修脚本，不反向改 spec 语义。

