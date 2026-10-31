# P4 交付 + 链路 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 `feat/v0.2-p4` 上实现 EzTest 0.2 P4：问题/报告/探索链路/需求闭环/版本收尾重开，真实 LLM 验收通过、contracts 冻结 `P4`、handoff 完成。

**Architecture:** 契约先行（contracts #0 → 迁移 #21–24 → 共享类型/端口 → 六条并行工作流）；新增 `delivery`/`chain` 限界上下文，扩展 `version`（closure/close/reopen）与 `agent`（3 profiles/5 tasks/8 tools/reconciler），前端替换三个 Tab 占位；Lead 独占共享文件（contracts、migrations、cmd、server、seed、handoff）。

**Tech Stack:** Go 1.24 + PostgreSQL 17 + Next.js/React + React Flow + Playwright executor + 真实 DeepSeek `deepseek-flash`。

**Spec:** [`docs/superpowers/specs/2026-11-01-eztest-v02-p4-delivery-chain-design.md`](../specs/2026-11-01-eztest-v02-p4-delivery-chain-design.md)

## Global Constraints

- **只做 P4**：不实现 P5–P7（PDF/模板/worker、知识/GC/多实例、全局问题/报告/待办、问题重开合并增强、代码/视觉、任意 as-of）；后续项只进 handoff/backlog。
- **不修改设计文档**：不改 `docs/version-V0.2/**`（contracts 除外）；不改 `Agent.md`；本 plan/spec/handoff/contracts/迁移/测试是仅有的写入面。
- **契约先行**：Task 0 未通过不得写业务代码；contracts 由 Lead 串行收口，子代理不并发写。
- **迁移只追加**：只新增 `delivery_0001_issue_lifecycle.sql`(#21)、`delivery_0002_report_revision.sql`(#22)、`delivery_0003_analysis_run.sql`(#23)、`version_0005_requirement_closure.sql`(#24)；不改 #1–#20；`order.txt` 末尾登记。
- **D34/Agent.md §6.9**：新表 `delivery_*`、`requirement_closure`；`pk_/uk_/idx_` 全列名；8 表 `created_at/updated_at`；同上下文 FK、跨上下文逻辑引用；ID 在 JSON/API/工具中为十进制字符串；禁止 BIGSERIAL/SERIAL。
- **导入方向**：domain 只依赖标准库/platform；application 不 import infrastructure/interfaces；跨上下文只依赖 application 或 consumer-side port；handler 只调 service、不写 SQL/不调 LLM。
- **真实 LLM**：验收脚本只用真实 DeepSeek；真实 LLM 失败=系统缺陷（上下文/提示词/工具/scope/校验），修复后重跑；不得用 fake 替代；单测/集成可用 fake port，但不属验收。
- **报告存储**：HTML 只入 content ledger（`content_ref` owner_kind=`report_revision`, role=`report_html`）；不写 `paths.report_dir`；PDF 不实现（`pdf_ref` 可空）。
- **问题边界**：P4 只做确认/驳回/基础合并/基础重开；`merged` 终态；跨波次去重增强、AI 语义合并、alias 迁移留 P7。
- **chain 只读**：不建 chain 表；不直读他域表；回放两档（SSE 补播 + 按波次/事件区间历史回看），不承诺任意 as-of。
- **版本收尾**：`version.close` 任务自身从“无开放任务”中排除；close 事务内重算谓词；completed 只做重开扫描。
- **分支/提交**：`feat/v0.2-p4`；小步提交；禁止 `git add -A`；未跟踪用户专用文件/dirty `docs/development.md`/`docs/startup.md`/`scripts/dev-all.ps1` 不纳入。
- **验收门禁**：`scripts/test.ps1`、`pnpm build`、P4 契约检查、迁移幂等、P1/P2/P3 真实 LLM 回归、P4 真实 LLM API 验收 + 浏览器 E2E、截图证据。
- **P4-D 裁决**：以 spec §16 为准；实现中发现新偏差先追加 `P4-D-16+` 并报告，不得静默改设计。

## Review Focus

1. **收尾谓词自排除与竞态**：`version.close` 任务本身不得阻塞 close；close 与新建 intent/需求/用例确认并发时只能一方赢；测试 `TestVersionClosePredicateExcludesOwnTask`、`TestVersionCloseConcurrentNewIntent`（Task 14）。
2. **报告 revision/as-of/append-only**：旧 revision HTML 字节不变、`input_fingerprint` 与 as-of 可验、stale 只标当前 revision、下载校验 owner/role/sha；测试 `TestReportPublishRevisionImmutable`、`TestReportStaleFingerprint`（Task 9/10）。
3. **链路历史/截断/深链**：superseded 用例标题非空、预算截断不静默丢节点、`focus_node` 保深链目标、无任意 as-of；测试 `TestChainHistorySupersededTitle`、`TestChainTruncationKeepsFocus`（Task 17/18）。
4. **多 SUT 归属**：问题 refs、报告 `by_version_sut`、链路 SUT 过滤都用 `version_sut_id`，不混用裸 `sut_id`；测试 `TestReportGroupsByVersionSUT`、`TestChainFiltersByVersionSUT`（Task 10/18）。
5. **真实 LLM 系统正确性**：Steward/Report/Issue Triager 的工具 scope/schema/上下文包必须让真实模型完成验收；失败按系统缺陷定位，不用 fake 绕过；验收 S1–S9（Task 24）。

---

## File Structure

```text
backend/internal/
├─ delivery/                          # 新上下文（四层）
│  ├─ domain/                         # issue/report/analysis 类型、状态机、候选/指纹纯函数
│  ├─ application/                    # issue/report/analysis 服务、ports、tools
│  ├─ infrastructure/
│  │  ├─ postgres/                    # issue/report/analysis 仓储
│  │  ├─ render/                      # 固定 HTML 模板/章节开关/转义
│  │  └─ content/                     # content ledger 适配
│  └─ interfaces/httpapi/             # issues/reports handler、DTO、Register
├─ chain/                             # 新上下文（只读）
│  ├─ domain/                         # Node/Edge/Filter/Budget/View
│  ├─ application/                    # ChainService、provider ports
│  ├─ infrastructure/providers/       # version/design/execution/delivery/resource 适配
│  └─ interfaces/httpapi/             # exploration-chain handler
├─ version/                           # 扩展：requirement closure、version close/reopen、tools
├─ design/application/                # 扩展：ReadCaseVersionsByIDs（历史 reader）
├─ agent/application/                 # 扩展：P4 profiles/work types/reconciler/task context/tools
├─ storage/infrastructure/            # FileStore symlink 收紧
├─ server/                            # Deps/registerDeliveryRoutes/registerChainRoutes
└─ cmd/eztest/main.go                 # 装配/后台启停
frontend/src/
├─ features/delivery/                 # IssuesTab/ReportsTab/api/types/组件
├─ features/chain/                    # ChainTab/Graph/Filters/Detail/Replay
├─ features/versions/                 # VersionHeader/VersionDetailPage/tabs 集成
├─ features/shell/                    # TodoDrawer/Topbar 入口
├─ features/clarification/useVersionEvents.ts  # SSE 事件分支扩展
└─ components/ui/                     # SutChip/PageChip/StatusDot/FilterChip/SideSheet
docs/superpowers/specs/...             # 已存在（只读）
docs/version-V0.2/contracts/*          # Task 0 更新并冻结 P4
docs/version-V0.2/phases/phase-04-handoff.md  # Task 25
scripts/p4-contract-check.cjs          # Task 0
scripts/p4-acceptance.ps1 scripts/p4-e2e.ps1 frontend/e2e/p4.spec.ts  # Task 24
backend/internal/db/migrations/        # Task 1: #21–#24
```

**并行写作用域**

| 流 | 任务 | 写作用域（不得越界） |
|---|---|---|
| Lead | 0,1,2,3,24 | contracts、migrations、cmd、server、agent seed/registry 骨架、FileStore、handoff |
| W1 issue | 4–8 | `delivery/**/issue*`、`delivery/**/analysis*`、`delivery/application/tools.go` |
| W2 report | 9–12 | `delivery/**/report*`、`delivery/**/render*`、`delivery/**/content*`、`delivery/application/report_tools.go` |
| W3 closure | 13–16 | `version/**/*closure*`、`version/**/*close*`、`version/**/tools*`、`version/interfaces/**` |
| W4 chain | 17–18 | `chain/**`、`design/application/history*` |
| W5 frontend | 19–22 | `frontend/src/features/{delivery,chain,versions,shell,clarification}`、`components/ui` |
| W6 acceptance | 23 | `scripts/p4-*`、`frontend/e2e/p4.spec.ts`、fixture 扩展、`.tmp/p4-acceptance/*` |

**并行 DAG**：0 → 1 → 2 → 3；之后 {W1:4→5→6→7, W2:9→10→11→12, W3:13→14→15→16, W4:17→18} 并行；W5 在 0 后可用契约/类型先行，API 就绪后集成；W6 在 3 后写脚本骨架；23 依赖全部；24 依赖 23。

---

## Task 0: P4 契约增量与契约检查（Lead）

**Files:**
- Modify: `docs/version-V0.2/contracts/db-schema.md`（§5、§10）
- Modify: `docs/version-V0.2/contracts/migrations-order.md`（§6）
- Modify: `docs/version-V0.2/contracts/events.md`（§13）
- Modify: `docs/version-V0.2/contracts/openapi.yaml`
- Modify: `docs/version-V0.2/contracts/task-types.md`（§6）
- Modify: `docs/version-V0.2/contracts/agent-profiles-tools.md`（§7）
- Modify: `docs/version-V0.2/contracts/settings.md`（§4d）
- Modify: `docs/version-V0.2/contracts/README.md`
- Create: `scripts/p4-contract-check.cjs`
- Modify: `scripts/p2-contract-check.cjs`、`scripts/p3-contract-check.cjs`

**Interfaces:**
- Consumes: spec §3/§4/§6/§7/§8/§14。
- Produces: 冻结契约名（下表）；`node scripts/p4-contract-check.cjs` 可执行。

| 契约项 | 值 |
|---|---|
| openapi | `info.version: 0.2.0-p4`、`x-frozen: P4`；14 ops：issues 6、reports 5、chain 1、version reopen 1、requirement reopen 1 |
| 表 | `delivery_issue`、`delivery_issue_ref`、`delivery_issue_status_history`、`delivery_report`、`delivery_report_revision`、`delivery_report_ref`、`delivery_analysis_run`、`requirement_closure` |
| 迁移 | #21–#24（§3） |
| 事件 | `issue.created/updated/reopened/merged`、`report.published/stale`、`requirement.closed/reopened`、`version.closed/reopened` |
| 任务 | `issue.triage`、`report.wave`、`report.version`、`requirement.close`、`version.close` |
| profiles | `issue_triager`、`report_writer`、`version_steward` |
| 工具 | `issue.read`、`issue.propose`、`report.data_read`、`report.narrative_propose`、`version.close_read`、`requirement.goal_met`、`version.close_propose`（加既有 `requirement.read`） |
| settings | `chain.max_nodes=300`、`chain.max_edges=600`、`chain.max_requirements=100`、`chain.replay_event_limit=500` |

- [ ] **Step 1: 写失败契约检查**

创建 `scripts/p4-contract-check.cjs`：读取 contracts 文本，断言上表每个值存在；断言 P0–P3 既有 paths/schemas/events/tables 仍在；输出缺失清单并以非 0 退出。

- [ ] **Step 2: 运行确认失败**

Run: `node scripts/p4-contract-check.cjs`
Expected: FAIL，首条类似 `missing openapi info.version 0.2.0-p4`，退出码非 0。

- [ ] **Step 3: 更新 contracts 增量**

按 spec §3/§4/§14 写入：db-schema §5 增 `delivery` 归属（7 表）+ §10 逐列/约束/索引/不变量；migrations-order §6 四迁移；events §13 十事件；openapi 14 ops/schemas/tags；task-types §6 五任务；profiles §7 三 profile/八工具/scope 注入；settings §4d 四键 + `report_dir/render_pdf` 注记；README P4 状态。

- [ ] **Step 4: 前向兼容 P2/P3 检查**

把 `p2-contract-check.cjs`/`p3-contract-check.cjs` 的精确总数断言改为“该阶段增量仍在”（路径/schema/表/事件/设置逐项存在，不锁死总数）；不删除既有断言项。

- [ ] **Step 5: 运行三份契约检查**

Run: `node scripts/p4-contract-check.cjs; node scripts/p3-contract-check.cjs; node scripts/p2-contract-check.cjs`
Expected: 全部 PASS；p4 输出 P4 增量齐全。

- [ ] **Step 6: Commit**

```bash
git add docs/version-V0.2/contracts scripts/p4-contract-check.cjs scripts/p2-contract-check.cjs scripts/p3-contract-check.cjs
git commit -m "docs(p4): freeze delivery-chain contracts and checks"
```

---

## Task 1: 迁移 #21–#24（Lead）

**Files:**
- Create: `backend/internal/db/migrations/delivery_0001_issue_lifecycle.sql`
- Create: `backend/internal/db/migrations/delivery_0002_report_revision.sql`
- Create: `backend/internal/db/migrations/delivery_0003_analysis_run.sql`
- Create: `backend/internal/db/migrations/version_0005_requirement_closure.sql`
- Modify: `backend/internal/db/migrations/order.txt`
- Modify: `backend/internal/db/migrate_integration_test.go:23,38-57,169`（数量/表清单）

**Interfaces:**
- Consumes: Task 0 契约表名/列名/索引名。
- Produces: 24 migrations; `schema_migrations` 二跑 no-op；8 张 P4 表。

- [ ] **Step 1: 更新迁移测试（失败）**

把 P3 断言改为 `len(applied)==24`，追加 8 表存在断言（`to_regclass('public.delivery_issue')` 等），保留 20 迁移表清单。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test -tags=integration ./internal/db/... -run 'TestMigrations|TestP4' -v`
Expected: FAIL（applied=20 / 表不存在）。

- [ ] **Step 3: 写四个迁移**

严格按 spec §3：雪花 BIGINT PK、命名约束/索引、CHECK、同上下文 FK、双审计列；#21 issue/ref/history，#22 report/revision/ref，#23 analysis_run，#24 requirement_closure。`order.txt` 按 21→24 追加。

- [ ] **Step 4: 运行迁移测试（两次）**

Run: `cd backend; ..\.tools\go\bin\go.exe test -tags=integration ./internal/db/... -run 'TestMigrations|TestP4' -v`（跑两遍）
Expected: PASS；第二遍 `applied` 为空、无副作用；8 表存在。

- [ ] **Step 5: Commit**

```bash
git add backend/internal/db/migrations backend/internal/db/migrate_integration_test.go
git commit -m "feat(p4): add delivery and closure migrations"
```

---

## Task 2: Delivery/Chain 共享类型与端口（Lead）

**Files:**
- Create: `backend/internal/delivery/domain/types.go`
- Create: `backend/internal/delivery/domain/types_test.go`
- Create: `backend/internal/delivery/application/ports.go`
- Create: `backend/internal/chain/domain/view.go`
- Create: `backend/internal/chain/domain/view_test.go`
- Create: `backend/internal/chain/application/ports.go`

**Interfaces:**
- Consumes: spec §3/§5/§6/§8；Task 1 表。
- Produces:

```go
// delivery/domain
type IssueStatus string // open/confirmed/dismissed/merged
type ReportKind string   // wave/version
type ReportStatus string // draft/published/stale
type AnalysisStatus string // queued/running/done/failed/degraded
type Issue struct { ID, VersionID int64; Title, Summary string; Status IssueStatus; Category, Severity string; DedupeKey, Fingerprint string; Source string; ExpectedRevision int32; MergedIntoID *int64; MergedAt *time.Time; MergedBy *int64; ReopenCount int; CreatedByRunID *int64; CreatedAt, UpdatedAt time.Time }
type IssueRef struct { ID, IssueID int64; Kind string; RefID int64; VersionSUTID *int64; Role string; CreatedAt, UpdatedAt time.Time }
type IssueStatusChange struct { ID, IssueID int64; From *IssueStatus; To IssueStatus; Reason string; ActorID *int64; ActorKind string; CreatedAt, UpdatedAt time.Time }
type Report struct { ID, VersionID int64; WaveID *int64; Kind ReportKind; CurrentRevisionID *int64; Status ReportStatus; InputFingerprint string; CreatedByRunID *int64; CreatedAt, UpdatedAt time.Time }
type ReportRevision struct { ID, ReportID int64; Revision int32; Status ReportStatus; HTMLRef, PDFRef *int64; InputFingerprint string; AsOf time.Time; TemplateVersion string; RenderStatus string; CreatedByRunID *int64; CreatedAt, UpdatedAt time.Time }
type ReportRef struct { ID, ReportID, ReportRevisionID int64; Kind string; RefID int64; CreatedAt, UpdatedAt time.Time }
type AnalysisRun struct { ID, VersionID int64; Kind, Status, DedupeKey string; InputRef, Output json.RawMessage; AgentRunID *int64; Error string; CreatedAt, UpdatedAt time.Time }

// chain/domain
type ChainFilter struct { VersionSUTID, PageID, WaveID *int64; Status, Result, FocusNode string; EventFrom, EventTo *int64 }
type NodeKind string; type EdgeKind string
type Node struct { ID string; Kind NodeKind; RefID string; Label string; Status string; VersionSUTID *int64; RequirementID, CaseVersionID, WaveID, CaseRunID, IssueID, ReportID *int64; CreatedAt time.Time; Meta map[string]any }
type Edge struct { From, To string; Kind EdgeKind }
type Budget struct { MaxNodes, MaxEdges, MaxRequirements int }
type ChainView struct { VersionID string; GeneratedAt time.Time; SnapshotCursor int64; Filters ChainFilter; Counts map[string]int; Nodes []Node; Edges []Edge; Truncated bool; Truncation Truncation; Replay ReplayInfo }
func ClampBudget(b Budget) Budget // 1..1000 / 1..2000 / 1..500，非法回默认 300/600/100
```

- `delivery/application/ports.go` 定义以下接口（实现任务只消费；所有方法带 `ctx context.Context` 返回 `error`）：

```go
type IssueFilter struct { Status, Category, Severity string; VersionSUTID *int64; Before int64; Limit int }
type IssueStatusUpdate struct { VersionID, IssueID int64; To domain.IssueStatus; Reason string; ActorID int64; ActorKind string; ExpectedRevision *int32 }
type IssueMerge struct { VersionID, SourceID, TargetID, ActorID int64; Reason string; ExpectedRevision *int32 }
type IssueRepository interface {
  CreateCandidate(ctx, issue domain.Issue, refs []domain.IssueRef) (domain.Issue, bool, error)
  Get(ctx, versionID, issueID int64) (domain.Issue, error)
  List(ctx, versionID int64, f IssueFilter) ([]domain.Issue, error)
  UpdateStatus(ctx, u IssueStatusUpdate) (domain.Issue, error)
  Merge(ctx, m IssueMerge) (domain.Issue, error)
  AppendHistory(ctx, h domain.IssueStatusChange) error
  ListRefs(ctx, issueID int64) ([]domain.IssueRef, error)
  ListHistory(ctx, issueID int64) ([]domain.IssueStatusChange, error)
  FindByCaseRun(ctx, versionID, caseRunID int64) (domain.Issue, bool, error)
}
type ReportFilter struct { Kind *domain.ReportKind; Before int64; Limit int }
type ReportRepository interface {
  CreateReport(ctx, r domain.Report) (domain.Report, error)
  GetReport(ctx, versionID, reportID int64) (domain.Report, error)
  ListReports(ctx, versionID int64, f ReportFilter) ([]domain.Report, error)
  NextRevision(ctx, reportID int64) (int32, error)
  CreateRevision(ctx, r domain.ReportRevision) (domain.ReportRevision, error)
  FinalizePublished(ctx, reportID int64, revision int32, htmlRef int64, fingerprint string) error
  MarkRenderMetadata(ctx, revisionID int64, status, error string) error
  MarkStale(ctx, reportID int64) (bool, error)
  GetRevision(ctx, reportID int64, revision int32) (domain.ReportRevision, error)
  ListRevisions(ctx, reportID int64) ([]domain.ReportRevision, error)
  InsertRefs(ctx, revisionID int64, refs []domain.ReportRef) error
  ListRefs(ctx, reportID int64) ([]domain.ReportRef, error)
  SetWaveReportID(ctx, versionID, waveID, reportID int64) error
}
type AnalysisRepository interface {
  CreateQueued(ctx, r domain.AnalysisRun) (domain.AnalysisRun, error)
  MarkRunning(ctx, id int64, agentRunID int64) error
  Complete(ctx, id int64, status domain.AnalysisStatus, output json.RawMessage, errMsg string) error
  LatestByIssue(ctx, issueID int64) (domain.AnalysisRun, bool, error)
}
type TaskEnsurer interface {
  EnsureIssueTriage(ctx, issueID int64) error
  EnsureWaveReport(ctx, versionID, waveID int64) error
  EnsureVersionReport(ctx, versionID int64) error
  EnsureRequirementClose(ctx, requirementID int64) error
  EnsureVersionClose(ctx, versionID int64) error
}
type ExecutionReader interface {
  ListFailedApplicationCaseRuns(ctx, versionID int64) ([]CaseRunSnapshot, error)
  CaseRunSnapshot(ctx, caseRunID int64) (CaseRunSnapshot, error)
  WaveSnapshot(ctx, versionID, waveID int64) (WaveSnapshot, error)
  VersionSnapshot(ctx, versionID int64) (VersionSnapshot, error)
}
type DesignReader interface { CurrentConfirmedCases(ctx, requirementID int64) ([]CaseVersionSnapshot, error); CaseVersionsByIDs(ctx, versionID int64, ids []int64) ([]CaseVersionSnapshot, error) }
type VersionReader interface { Get(ctx, versionID int64) (VersionSnapshot, error); ReopenIfCompleted(ctx, versionID int64, trigger, reason string) error }
type EventPublisher interface { Publish(ctx, eventType string, versionID int64, payload map[string]any) error }
type AuditSink interface { Write(ctx, actorID int64, action, targetKind, targetID string, metadata map[string]any) error }
type Clock interface { Now() time.Time }
```

- 快照类型（`CaseRunSnapshot/WaveSnapshot/VersionSnapshot/CaseVersionSnapshot/IssueSnapshot`）定义在 `delivery/application/ports.go`，字段与 spec §6.1/§7.2 对齐，ID 为 int64（API 层转字符串）。
- `chain/application/ports.go` 定义：
```go
type VersionChainProvider interface { Requirements(ctx, versionID int64, limit int) ([]domain.Node, error); VersionSUTs(ctx, versionID int64) ([]domain.Node, error) }
type DesignChainProvider interface { Scenarios(ctx, versionID int64) ([]domain.Node, error); Cases(ctx, versionID int64) ([]domain.Node, error); CaseVersionsByIDs(ctx, versionID int64, ids []int64) ([]domain.Node, error) }
type ExecutionChainProvider interface { Waves(ctx, versionID int64) ([]domain.Node, error); CaseRuns(ctx, versionID int64) ([]domain.Node, error) }
type DeliveryChainProvider interface { Issues(ctx, versionID int64) ([]domain.Node, error); Reports(ctx, versionID int64) ([]domain.Node, error); Closures(ctx, versionID int64) ([]domain.Node, error) }
type ResourceChainProvider interface { SUTNames(ctx, versionID int64) (map[int64]string, error); PageNames(ctx, versionID int64) (map[int64]string, error) }
type CursorReader interface { LatestCursor(ctx context.Context, channel string) (int64, error) }
```
`RequirementNode` 等具体节点类型不需要单独定义：provider 返回 `domain.Node`（Kind 区分）；`SourceGraph` 按 Kind 分组消费。

- [ ] **Step 1: 写失败测试**

`delivery/domain/types_test.go`：枚举 Valid 全值/非法值；`chain/domain/view_test.go`：`ClampBudget(0) == {300,600,100}`、负值/超限回退、`ChainFilter` 归一化。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/delivery/domain ./internal/chain/domain -v`
Expected: FAIL（类型/函数未定义）。

- [ ] **Step 3: 实现类型/端口**

按上面签名实现；端口只定义接口，不含实现；JSON 字段使用 `int64`，API 层再转字符串。

- [ ] **Step 4: 运行测试/编译**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/delivery/domain ./internal/chain/domain -v`
Expected: PASS。`..\.tools\go\bin\go.exe build ./...` 通过。

- [ ] **Step 5: Commit**

```bash
git add backend/internal/delivery backend/internal/chain
git commit -m "feat(p4): add delivery and chain shared types and ports"
```

---

## Task 3: Agent P4 接线 + FileStore symlink 收紧（Lead）

**Files:**
- Create: `backend/internal/agent/application/p4_profiles.go`
- Create: `backend/internal/agent/application/p4_profiles_test.go`
- Modify: `backend/internal/agent/application/ports_agent.go:114-176`
- Modify: `backend/internal/agent/application/profiles.go:274-294`
- Modify: `backend/internal/agent/application/p3_task_context.go`（P4 task context 注册回退）
- Modify: `backend/internal/agent/application/runner.go:460`（工具 registry 使用 `NewP4Registry`，P4 组 init 注册）
- Create: `backend/internal/agent/application/p4_task_context.go`（`RegisterP4TaskContext(fn func(ctx, task TaskRef) (any, bool))`，各域 init 注册）
- Create: `backend/internal/agent/application/tools/p4_registry.go`（组合 3 组工具；`RegisterP4ToolGroup(func() []tools.Tool)`，各域工具文件用 `init()` 注册）
- Modify: `backend/internal/storage/infrastructure/filestore.go:92-119`
- Create/modify: `backend/internal/storage/infrastructure/filestore_test.go`

**Interfaces:**
- Consumes: Task 2 类型/端口；既有 `ProfileKeyForWorkType`/`Profiles()`/tool registry 模式。
- Produces：
  - work types：`issue.triage`、`report.wave`、`report.version`、`requirement.close`、`version.close`（常量在 `agent/application`）。
  - mapping：上述 type → `issue_triager`/`report_writer`/`version_steward`。
  - `func P4Profiles() []domain.AgentProfile`；`Profiles()` 追加且去重（DB 编辑优先，不覆盖）。
  - `func NewP4Registry(deps tools.Deps) *tools.Registry`；`func RegisterP4ToolGroup(fn func() []tools.Tool)`（各域 init 注册，避免并行改同一文件）。
  - `type TaskRef struct { VersionID int64; Type string; Payload map[string]any }`；`func RegisterP4TaskContext(fn func(ctx context.Context, task TaskRef) (any, bool))`（各域 init 注册；Provider 在 P3 switch 未命中时回退遍历注册项）。
  - `FileStore.Open` 拒绝 symlink/根外路径（`Lstat`+`EvalSymlinks`+`filepath.Rel`）。

- [ ] **Step 1: 写失败测试**

`p4_profiles_test.go`：5 个 work type→profile 映射；`P4Profiles()` 含 3 profile 且预算/工具名正确；`Profiles()` 合并后不重复。
`p4_registry_test.go`：`NewP4Registry` 初始为空且可被 `RegisterP4ToolGroup` 注入；`p4_task_context_test.go`：未命中 P3 switch 时回退注册项、未注册返回 false。
`filestore_test.go`：根内普通文件可 Open；symlink 指向根外必须返回 `ErrInvalidPath`；`..`/绝对路径拒绝。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/agent/application ./internal/agent/application/tools ./internal/storage/infrastructure -run 'TestP4Profiles|TestProfileKeyForWorkTypeP4|TestP4Registry|TestP4TaskContext|TestFileStoreSymlink' -v`
Expected: FAIL（未定义/未拒绝 symlink）。

- [ ] **Step 3: 实现**

按 spec §4.4 定义 profile 预算/工具白名单；work type 常量/映射；registry 组合接口 + task context 回退；FileStore 校验。

- [ ] **Step 4: 运行测试/编译**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/agent/application ./internal/agent/application/tools ./internal/storage/infrastructure -v; ..\.tools\go\bin\go.exe build ./...`
Expected: PASS；build 通过。

- [ ] **Step 5: Commit**

```bash
git add backend/internal/agent backend/internal/storage
git commit -m "feat(p4): wire profiles and harden filestore symlinks"
```

---

## Task 4: Issue 领域（W1）

**Files:**
- Create: `backend/internal/delivery/domain/issue.go`
- Create: `backend/internal/delivery/domain/issue_candidate.go`
- Create: `backend/internal/delivery/domain/issue_test.go`
- Create: `backend/internal/delivery/domain/issue_candidate_test.go`
- Modify: `backend/internal/delivery/domain/types.go`（如需补充字段）

**Interfaces:**
- Consumes: Task 2 类型。
- Produces:

```go
func (s IssueStatus) Valid() bool
func CanTransitionIssue(from, to IssueStatus) bool // open→confirmed/dismissed；confirmed/dismissed→open；open/confirmed/dismissed→merged；其余 false
func DeriveDedupeKey(versionID, caseVersionID int64, stepSeq, assertionSeq int, failureClass, hint string) string
type CandidateInput struct { VersionID, CaseRunID, CaseVersionID int64; CaseTitle, CaseRunSummary string; StepSeq, AssertionSeq int; FailureClass, Category, Severity, DedupeHint string; VersionSUTID *int64; WaveID *int64; EvidenceIDs []int64 }
func NewIssueCandidate(in CandidateInput) (Issue, []IssueRef, error)
func ValidateMerge(source Issue, target Issue, lookup func(int64) (Issue, bool)) error
```

- [ ] **Step 1: 写失败测试**

`issue_test.go`：状态迁移表（合法 8/非法 4）；`merged` 为终态；`reopen_count` 语义。
`issue_candidate_test.go`：同 case_version 跨 wave 的 dedupe 相同；case_version 变化不同；`hint` 合法时优先；标题/摘要确定性；refs 上限 20；无 caseVersion/非法 ID 返回 `ErrInvalidArgument`。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/delivery/domain -run 'TestIssue|TestCandidate|TestDedupe|TestMerge' -v`
Expected: FAIL（未定义）。

- [ ] **Step 3: 实现**

按 spec §5.1/§5.2：dedupe 复用 P3 算法 `sha256(fmt.Sprintf("%d|%d|%d|%d|%s", ...))[:32]`；标题 `"{case_title} · step {seq}: {category} failure"`；摘要优先 case_run.summary；merge 自指/环/跨版本校验。

- [ ] **Step 4: 运行测试**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/delivery/domain -v`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add backend/internal/delivery/domain
git commit -m "feat(p4): add issue domain and candidate derivation"
```

---

## Task 5: Issue Postgres 仓储（W1）

**Files:**
- Create: `backend/internal/delivery/infrastructure/postgres/issue_repo.go`
- Create: `backend/internal/delivery/infrastructure/postgres/issue_repo_integration_test.go`
- Create: `backend/internal/delivery/infrastructure/postgres/helpers.go`

**Interfaces:**
- Consumes: Task 1 `delivery_issue*`；Task 2 `IssueRepository`；`testutil.NewDB`。
- Produces: `func NewIssueRepository(pool *pgxpool.Pool) *IssueRepository` 实现 `delivery/application.IssueRepository`：
  `CreateCandidate`、`Get`、`List`、`UpdateStatus`、`Merge`、`AppendHistory`、`ListRefs`、`ListHistory`、`FindByCaseRun`。

- [ ] **Step 1: 写失败集成测试（`//go:build integration`）**

`issue_repo_integration_test.go`：
- CreateCandidate 插入 issue+refs+history，`UNIQUE(version_id,dedupe_key)` 二次返回既有；
- CreateCandidate 同 key 不重复插 refs/history；
- UpdateStatus 用 `expected_revision` CAS，冲突 `ErrStateConflict`；
- Merge 写 `merged_into_id/merged_at/merged_by` + history；
- List 按 status/severity/before 过滤、默认 id DESC；
- FK cascade：删 issue 后 refs/history 清除（测试可事务回滚）。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; $env:EZTEST_TEST_DATABASE_URL='...'; ..\.tools\go\bin\go.exe test -tags=integration ./internal/delivery/infrastructure/postgres -run TestIssueRepo -v`
Expected: FAIL（未定义/表不存在）。

- [ ] **Step 3: 实现**

SQL 只用参数化查询；`CreateCandidate` 用 `INSERT ... ON CONFLICT (version_id,dedupe_key) DO NOTHING` + `RETURNING`，冲突时 `SELECT` 既有；refs/history `ON CONFLICT DO NOTHING`；状态更新 `WHERE id=$1 AND version_id=$2 AND expected_revision=$3`，0 row → `ErrStateConflict`。

- [ ] **Step 4: 运行集成测试**

Run: 同上
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add backend/internal/delivery/infrastructure/postgres
git commit -m "feat(p4): add issue postgres repository"
```

---

## Task 6: Issue 应用服务（W1）

**Files:**
- Create: `backend/internal/delivery/application/issue_service.go`
- Create: `backend/internal/delivery/application/issue_service_test.go`
- Create: `backend/internal/delivery/application/errors.go`

**Interfaces:**
- Consumes: Task 2 ports；Task 4 domain；Task 5 repo。
- Produces:

```go
type IssueService struct { /* repo, exec, events, audit, clock, tasks */ }
func NewIssueService(repo IssueRepository, exec ExecutionReader, events EventPublisher, audit AuditSink, clock Clock, tasks TaskEnsurer) *IssueService
func (s *IssueService) ScanFailures(ctx context.Context, versionID int64) (int, error)
func (s *IssueService) IngestCandidate(ctx context.Context, caseRunID int64) (domain.Issue, bool, error)
func (s *IssueService) List(ctx context.Context, versionID int64, f IssueFilter) ([]domain.Issue, error)
func (s *IssueService) Get(ctx context.Context, versionID, issueID int64) (IssueDetail, error)
func (s *IssueService) Confirm(ctx context.Context, versionID, issueID, actorID int64, reason string, expected *int32) (domain.Issue, error)
func (s *IssueService) Dismiss(...)
func (s *IssueService) Reopen(ctx context.Context, versionID, issueID, actorID int64, reason string, expected *int32) (domain.Issue, error)
func (s *IssueService) Merge(ctx context.Context, versionID, sourceID, targetID, actorID int64, reason string, expected *int32) (domain.Issue, error)
```

`IssueDetail`：`struct { Issue domain.Issue; Refs []domain.IssueRef; History []domain.IssueStatusChange; Triage *domain.AnalysisRun }`。

- 事件：`issue.created` 纳入、`issue.updated` 确认/驳回、`issue.reopened`、`issue.merged`；audit best-effort；`ScanFailures`/`IngestCandidate` 对新 issue 调 `tasks.EnsureIssueTriage(ctx, issue.ID)`。
- 错误映射 `ErrStateConflict/ErrNotFound/ErrInvalidArgument`。

- [ ] **Step 1: 写失败单测（fake repo/exec/events/audit/tasks）**

覆盖：ScanFailures 跳过非 application/已有 refs；IngestCandidate 幂等；Confirm/Dismiss/Reopen/Merge 调用 repo + 事件/审计；merge 自指/环；reopen reason 必填；状态冲突返回稳定错误。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/delivery/application -run 'TestIssueService|TestIngest|TestScan' -v`
Expected: FAIL。

- [ ] **Step 3: 实现**

按 spec §5.1–§5.3；`ScanFailures` 调 `ExecutionReader.ListFailedApplicationCaseRuns(versionID)`；Ingest 构造候选；所有写操作先状态机校验再 repo；事件 payload 只含 ID/状态/分类。

- [ ] **Step 4: 运行测试**

Run: 同上 → PASS。

- [ ] **Step 5: Commit**

```bash
git add backend/internal/delivery/application
git commit -m "feat(p4): add issue application service"
```

---

## Task 7: Issue HTTP API（W1）

**Files:**
- Create: `backend/internal/delivery/interfaces/httpapi/issue_handler.go`
- Create: `backend/internal/delivery/interfaces/httpapi/dto.go`
- Create: `backend/internal/delivery/interfaces/httpapi/routes.go`
- Create: `backend/internal/delivery/interfaces/httpapi/issue_handler_test.go`
- Create: `backend/internal/delivery/interfaces/httpapi/routes_test.go`（本地 `httptest` + Register）

**Interfaces:**
- Consumes: Task 6 `IssueService` 方法。
- Produces:
  - `GET /api/versions/{versionId}/issues`
  - `GET .../issues/{issueId}`
  - `POST .../issues/{issueId}/confirm|dismiss|reopen|merge`
  - `func New(s Service) *Handler`；`func Register(mux *http.ServeMux, h *Handler)`（鉴权沿用 server middleware）

- [ ] **Step 1: 写失败 handler 单测**

fake Service 断言：list 分页参数解析；confirm/dismiss/reopen/merge body 校验（reason 必填、expected_revision 可选）；错误映射 `404/409/400`；DTO ID 字符串、时间 RFC3339；refs/history/triage 投影。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/delivery/interfaces/httpapi -run TestIssueHandler -v`
Expected: FAIL。

- [ ] **Step 3: 实现 handler/routes**

复用 `httpx.JSON/List/Error`、`validation.NonEmptyID`、pagination；DTO 与 domain 分离；handler 不写 SQL。

- [ ] **Step 4: 运行单测 + 本地路由测试**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/delivery/interfaces/httpapi -v`
Expected: PASS（真实 handler + `httptest` + `Register`）。

- [ ] **Step 5: Commit**

```bash
git add backend/internal/delivery/interfaces/httpapi
git commit -m "feat(p4): expose issue API"
```

---

## Task 8: Issue 分诊（analysis_run + issue.triage + tools，W1）

**Files:**
- Create: `backend/internal/delivery/infrastructure/postgres/analysis_repo.go`
- Create: `backend/internal/delivery/infrastructure/postgres/analysis_repo_integration_test.go`
- Create: `backend/internal/delivery/application/analysis_service.go`
- Create: `backend/internal/delivery/application/analysis_service_test.go`
- Create: `backend/internal/delivery/application/issue_tools.go`
- Create: `backend/internal/agent/application/tools/p4_issue_tools.go`（`init(){ RegisterP4ToolGroup(...) }`，不改 p4_registry.go）
- Create: `backend/internal/agent/application/task_context_p4_issue.go`（`init(){ RegisterP4TaskContext(...) }`）
- Create: `backend/internal/agent/application/p4_issue_task_test.go`

**Interfaces:**
- Consumes: Task 2 `AnalysisRepository`；Task 6 `IssueService`；agent registry `tools.Tool`。
- Produces:
  - `func NewAnalysisService(repo AnalysisRepository, issues *IssueService, clock Clock) *AnalysisService`；`StartTriage(ctx, issueID int64) (domain.AnalysisRun, error)`、`Complete(ctx, id int64, status domain.AnalysisStatus, output json.RawMessage, errMsg string) error`。
  - tools：`issue.read`（scope issue）、`issue.propose`（仅 analysis_run output + 团队帖，不改状态）；`NewP4Registry` 包含 issue 组。
  - `issue.triage` 任务由 reconciler 在 Task 15 接线；本任务测试 task context 可读 issue/refs。

- [ ] **Step 1: 写失败测试**

analysis repo 集成：active dedupe 唯一、agent_run 1:1、status 迁移、output JSON。
tool 单测：`issue.propose` 拒绝跨版本/merged/self duplicate；合法输出写 analysis_run + 团队帖；不调用 issue 状态写方法。
task context：payload `{issue_id}` 生成含 issue/refs/关联 case_run 摘要的 task pack，不含全量会话。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/delivery/application ./internal/delivery/infrastructure/postgres ./internal/agent/application/tools -run 'TestAnalysis|TestIssueTool|TestP4IssueTask' -v`
Expected: FAIL。

- [ ] **Step 3: 实现**

按 spec §5.3；`issue.propose` 参数 schema 严格；`analysis_run.output` 服务端校验 JSON；工具错误用 `invalid_argument`。

- [ ] **Step 4: 运行测试**

Run: 同上 → PASS。

- [ ] **Step 5: Commit**

```bash
git add backend/internal/delivery backend/internal/agent/application/tools backend/internal/agent/application
git commit -m "feat(p4): add issue triage analysis and tools"
```

---

## Task 9: 报告领域 + 确定性 HTML 渲染（W2）

**Files:**
- Create: `backend/internal/delivery/domain/report_input.go`
- Create: `backend/internal/delivery/domain/report_fingerprint.go`
- Create: `backend/internal/delivery/domain/report_fingerprint_test.go`
- Create: `backend/internal/delivery/infrastructure/render/render.go`
- Create: `backend/internal/delivery/infrastructure/render/template.go`
- Create: `backend/internal/delivery/infrastructure/render/render_test.go`

**Interfaces:**
- Consumes: Task 2 domain types；spec §6.1–§6.2。
- Produces:

```go
type ReportSnapshot struct { Kind ReportKind; VersionID int64; WaveID *int64; Requirements []RequirementSnapshot; Waves []WaveSnapshot; CaseRuns []CaseRunSnapshot; Issues []IssueSnapshot; VersionSUTs []VersionSUTSnapshot; Coverage CoverageSnapshot; GeneratedAt time.Time }
func CanonicalJSON(s ReportSnapshot) ([]byte, error)
func Fingerprint(s ReportSnapshot) string // sha256(canonical JSON)
type SectionOptions struct { Summary, Results, Issues, Coverage, Evidence bool }
type ReportRenderer interface { Render(ctx context.Context, snapshot ReportSnapshot, narrative string, opts SectionOptions) ([]byte, error) }
func NewHTMLRenderer(templateVersion string) (*HTMLRenderer, error) // templateVersion 仅接受 "1"
```

- 渲染规则：固定模板；无外部资源；`html/template` 自动转义；narrative 纯文本（保留换行）；章节开关；2 MiB 上限后按 issues/waves/case_runs 减半并写“报告截断说明”；空数据合法。

- [ ] **Step 1: 写失败测试**

`report_fingerprint_test.go`：同 snapshot 同 hash；issue 状态变→hash 变；结构体字段顺序无关（CanonicalJSON 固定字段/数组 id 排序）；空数据 hash 稳定。
`render_test.go`：包含 `<script>alert(1)</script>` 的 title/narrative 必须转义；`section_issues=false` 不出现 issues 章节；2 MiB 超限触发截断说明；`template_version` 非 "1" 报错；输出以 `<!doctype html>` 开头。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/delivery/domain ./internal/delivery/infrastructure/render -run 'TestReportFingerprint|TestRender' -v`
Expected: FAIL。

- [ ] **Step 3: 实现**

按 spec §6.1/§6.2：canonical JSON 用固定结构体，数组按 id 升序；渲染器嵌入 CSS；narrative 用 `html/template` 转义为段落；超限截断逻辑写 `data-truncated` 说明。

- [ ] **Step 4: 运行测试**

Run: 同上 → PASS。

- [ ] **Step 5: Commit**

```bash
git add backend/internal/delivery/domain backend/internal/delivery/infrastructure/render
git commit -m "feat(p4): add report snapshot fingerprint and HTML renderer"
```

---

## Task 10: 报告仓储 + 发布/内容账本/stale（W2）

**Files:**
- Create: `backend/internal/delivery/infrastructure/postgres/report_repo.go`
- Create: `backend/internal/delivery/infrastructure/postgres/report_repo_integration_test.go`
- Create: `backend/internal/delivery/application/report_service.go`
- Create: `backend/internal/delivery/application/report_service_test.go`
- Create: `backend/internal/delivery/infrastructure/content/store.go`
- Create: `backend/internal/delivery/infrastructure/content/store_integration_test.go`
- Modify: `backend/internal/delivery/application/ports.go`（补 `ReportDataReader`、`ReportRepository` 方法）

**Interfaces:**
- Consumes: Task 1 `delivery_report*`；Task 2 ports；Task 9 renderer/fingerprint；storage `Upload/AddRef/Open`（`storage/application/service.go:69/118/156`）。
- Produces:

```go
type ContentStore interface {
  PutReportHTML(ctx context.Context, reportRevisionID int64, html []byte) (contentRefID int64, sha256 string, size int64, err error)
  OpenReportHTML(ctx context.Context, contentRefID int64) ([]byte, string, int64, error)
}
type ReportRepository interface { CreateReport; GetReport; ListReports; NextRevision; CreateRevision; FinalizePublished; MarkRenderMetadata; MarkStale; GetRevision; ListRevisions; InsertRefs; ListRefs; SetWaveReportID }
type ReportDataReader interface { WaveSnapshot(ctx, versionID, waveID int64) (domain.ReportSnapshot, error); VersionSnapshot(ctx, versionID int64) (domain.ReportSnapshot, error) }
type ReportService struct { /* repo, renderer, content, data, events, audit, clock, sections */ }
func NewReportService(repo ReportRepository, renderer ReportRenderer, content ContentStore, data ReportDataReader, events EventPublisher, audit AuditSink, clock Clock, tasks TaskEnsurer, opts SectionOptions) *ReportService
func (s *ReportService) EnsureWaveReport(ctx context.Context, versionID, waveID int64) (domain.Report, error) // 建 report+revision(draft) 并 ensure report.wave 任务
func (s *ReportService) EnsureVersionReport(ctx context.Context, versionID int64) (domain.Report, error)
func (s *ReportService) EnsureReports(ctx context.Context, versionID int64) error // reconciler：按条件 ensure wave/version 报告任务
func (s *ReportService) Publish(ctx context.Context, reportID int64, revision int32, narrative string) (domain.ReportRevision, error)
func (s *ReportService) Regenerate(ctx context.Context, versionID, reportID int64) (int32, error)
func (s *ReportService) MarkStaleIfChanged(ctx context.Context, versionID int64) (bool, error)
func (s *ReportService) List(ctx context.Context, versionID int64, kind *domain.ReportKind) ([]domain.Report, error)
func (s *ReportService) Get(ctx context.Context, versionID, reportID int64) (ReportDetail, error)
func (s *ReportService) OpenHTML(ctx context.Context, versionID, reportID int64, revision int32) ([]byte, domain.ReportRevision, error)
```

`ReportDetail`：`struct { Report domain.Report; Revisions []domain.ReportRevision; Refs []domain.ReportRef }`。

- 发布事务：`FinalizePublished` 写 html_ref/status/render_status + report current/status/fingerprint + refs + `run_wave.report_id`；事件 `report.published`；审计。旧 revision 不变。
- `MarkStaleIfChanged` 重算指纹，published 且不同 → stale + `report.stale`（只发一次）。
- `OpenHTML` 校验版本归属/owner/role/sha/size；不泄露路径。

- [ ] **Step 1: 写失败测试**

repo 集成：wave/version partial unique；revision 唯一；FinalizePublished 原子；append-only 内容字段更新失败；refs；wave.report_id。
service 单测（fake repo/renderer/content/data/events）：publish 写入顺序；旧 revision 不变；stale 只标当前；regenerate 下一 revision；OpenHTML 归属/sha 失败返回 409；空报告生成。
content 集成：Upload+AddRef(owner_kind=report_revision,role=report_html) 可 Open 并 sha 一致。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/delivery/application -run 'TestReportService' -v; $env:EZTEST_TEST_DATABASE_URL='...'; ..\.tools\go\bin\go.exe test -tags=integration ./internal/delivery/infrastructure/... -run 'TestReport|TestContent' -v`
Expected: FAIL。

- [ ] **Step 3: 实现**

按 spec §6.3–§6.5；`report` 行锁 `SELECT ... FOR UPDATE` + `NextRevision`；content 字节写文件系统（storage 服务），失败置 `render_status=failed` 不发布；下载头在 handler。

- [ ] **Step 4: 运行测试**

Run: 同上 → PASS。

- [ ] **Step 5: Commit**

```bash
git add backend/internal/delivery
git commit -m "feat(p4): add report repository, publish and content ledger"
```

---

## Task 11: 报告任务/工具 + 收尾钩子（W2）

**Files:**
- Create: `backend/internal/delivery/application/report_tools.go`
- Create: `backend/internal/delivery/application/report_tools_test.go`
- Create: `backend/internal/agent/application/tools/p4_report_tools.go`（`init(){ RegisterP4ToolGroup(...) }`，不改 p4_registry.go）
- Create: `backend/internal/agent/application/task_context_p4_report.go`（`init(){ RegisterP4TaskContext(...) }`）
- Create: `backend/internal/agent/application/p4_report_task_test.go`
- Modify: `backend/internal/agent/application/runner.go`（report.* run 结束 finalizer 钩子）
- Create: `backend/internal/agent/application/ports_report.go`（`ReportFinalizer`）

**Interfaces:**
- Consumes: Task 10 `ReportService`；agent task context registry。
- Produces:
  - tools `report.data_read`（返回当前 task 的 snapshot 摘要，不含字节/凭据）、`report.narrative_propose`（校验 1..4000 纯文本，写 run 临时输出，不直接发布）。
  - `ReportFinalizer interface { Finalize(ctx, reportID int64, revision int32) error }`；runner 对 `report.wave|version` 结束后调用（未发布则返回错误 → 任务重试）。
  - task context：payload `{report_id, revision, wave_id?, version_id}` 生成 task pack（wave/version 数据摘要）。

- [ ] **Step 1: 写失败测试**

tool 测试：`report.narrative_propose` 拒绝空/超长/HTML 标签仍允许文本；`report.data_read` 只返回计数/ID；越权 report_id 拒绝。
task context 测试：report.wave/version payload 能取到 report/revision/wave；缺失返回错误。
finalizer 测试：未 published → 返回错误；published → nil。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/delivery/application ./internal/agent/application/tools ./internal/agent/application -run 'TestReportTool|TestP4ReportTask|TestReportFinalizer' -v`
Expected: FAIL。

- [ ] **Step 3: 实现**

按 spec §4.4/§6；runner 钩子只在 task type report.wave/version 执行；finalizer 通过 `ReportService.Get` 检查当前 revision published；错误映射为任务可重试。

- [ ] **Step 4: 运行测试**

Run: 同上 → PASS。

- [ ] **Step 5: Commit**

```bash
git add backend/internal/delivery/application backend/internal/agent/application
git commit -m "feat(p4): add report tasks, tools and finalizer hook"
```

---

## Task 12: 报告 HTTP API + HTML 下载（W2）

**Files:**
- Create: `backend/internal/delivery/interfaces/httpapi/report_handler.go`
- Modify: `backend/internal/delivery/interfaces/httpapi/dto.go`
- Modify: `backend/internal/delivery/interfaces/httpapi/routes.go`
- Create: `backend/internal/delivery/interfaces/httpapi/report_handler_test.go`

**Interfaces:**
- Consumes: Task 10 `ReportService`。
- Produces：`GET /reports`、`GET /reports/{reportId}`、`GET .../revisions/{revision}`、`GET .../revisions/{revision}/html`、`POST .../regenerate`。
- HTML 响应头：`Content-Type: text/html; charset=utf-8`、`ETag`、`Cache-Control: private, no-store`、`X-Content-Type-Options: nosniff`、CSP（spec §6.4）。

- [ ] **Step 1: 写失败测试**

handler 单测：列表/详情/revision/as-of/render_status/html_available；regenerate 202 幂等；HTML 下载 200 头/字节；未发布 404；sha 不符 409；越权版本 404。
server 集成：真实 PG+content，发布后可下载，旧 revision 仍可读。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/delivery/interfaces/httpapi -run TestReportHandler -v`
Expected: FAIL。

- [ ] **Step 3: 实现**

复用 `httpx`；HTML 下载用 `http.ServeContent` 或显式写字节；CSP/Content-Disposition；不暴露内部路径。

- [ ] **Step 4: 运行测试**

Run: 同上 → PASS。

- [ ] **Step 5: Commit**

```bash
git add backend/internal/delivery/interfaces/httpapi
git commit -m "feat(p4): expose report API and HTML download"
```

---

## Task 13: 需求闭环领域/服务/仓储（W3）

**Files:**
- Create: `backend/internal/version/domain/requirement_closure.go`
- Create: `backend/internal/version/domain/requirement_closure_test.go`
- Create: `backend/internal/version/application/closure_service.go`
- Create: `backend/internal/version/application/closure_service_test.go`
- Create: `backend/internal/version/infrastructure/postgres/closure_repo.go`
- Create: `backend/internal/version/infrastructure/postgres/closure_repo_integration_test.go`
- Modify: `backend/internal/version/domain/requirement.go:186`（`CanTransitionRequirement` 增 `closed→draft`）
- Modify: `backend/internal/version/domain/repository.go`（`RequirementClosureRepository`）

**Interfaces:**
- Consumes: Task 1 `requirement_closure`；version/design/execution/delivery consumer ports。
- Produces:

```go
type ClosureChecks struct { RequirementState, HasConfirmedCase, CasesTerminal, FailedCaseHasIssue, NoOpenWork, NoActiveIntent, NoInflightCaseRun, CoverageRecorded bool }
func (c ClosureChecks) Passed() bool
type ClosureInput struct { Requirement domain.Requirement; CaseVersions []CaseVersionSnapshot; LatestRuns map[int64]CaseRunSnapshot; IssueRefs map[int64]bool; OpenWorkItems, ActiveIntents, InflightCaseRuns int; CoverageComplete bool }
func EvaluateRequirementClosure(in ClosureInput) ClosureChecks
func (s *ClosureService) Close(ctx context.Context, requirementID int64, runID *int64) (domain.RequirementClosure, error)
func (s *ClosureService) Reopen(ctx context.Context, requirementID int64, reason string) error
func (s *ClosureService) EnsureClosures(ctx context.Context, versionID int64) error // reconciler：满足预检的需求 ensure requirement.close 任务
```

- 服务端重算 checks；通过 insert closure（`close_round=max+1`）+ requirement closed + 事件 `requirement.closed`；失败 `ErrClosePredicateFailed{failed map[string]int}`；重开 `closed→draft`、closure.reopen_count++、事件 `requirement.reopened`、调 `VersionReader.ReopenIfCompleted`。构造 `NewClosureService(..., tasks TaskEnsurer)`，`EnsureClosures` 用 tasks 幂等 ensure `requirement.close`。

- [ ] **Step 1: 写失败测试**

domain：8 个 checks 的正/负矩阵；`Passed` 全真；`closed→draft` 合法、`draft→closed` 非法。
service（fake ports/repo）：通过写 closure/closed/事件；失败返回 failed 明细且不改状态；重复 Close 幂等/新轮次；Reopen 清 closed、reopen_count++。
repo 集成：唯一 `(requirement_id,close_round)`；FK；checks JSON 保存。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/version/domain ./internal/version/application -run 'TestClosure|TestRequirementTransition' -v; $env:EZTEST_TEST_DATABASE_URL='...'; ..\.tools\go\bin\go.exe test -tags=integration ./internal/version/infrastructure/postgres -run TestClosureRepo -v`
Expected: FAIL。

- [ ] **Step 3: 实现**

按 spec §7.1/§7.2；checks 的 self-task 排除（`requirement.close`）在 service 层；`coverage_recorded` 由 `CoverageReader` 返回；错误类型稳定。

- [ ] **Step 4: 运行测试**

Run: 同上 → PASS。

- [ ] **Step 5: Commit**

```bash
git add backend/internal/version
git commit -m "feat(p4): add requirement closure service and repository"
```

---

## Task 14: 版本 close 谓词/服务 + 重开（W3）

**Files:**
- Create: `backend/internal/version/domain/version_close.go`
- Create: `backend/internal/version/domain/version_close_test.go`
- Create: `backend/internal/version/application/version_close_service.go`
- Create: `backend/internal/version/application/version_close_service_test.go`
- Modify: `backend/internal/version/domain/repository.go`（`Close`/`ReopenIfCompleted`）
- Modify: `backend/internal/version/infrastructure/postgres.go`（条件 UPDATE）
- Create: `backend/internal/version/infrastructure/postgres/version_close_integration_test.go`

**Interfaces:**
- Consumes: Task 13 closure；Task 10 report 状态/指纹（通过 `ReportStatusReader` port）。
- Produces:

```go
type ClosePredicate struct { Running, NoBlockingWorkItems, NoActiveIntents, NoOpenHumanRequests, NoActiveWaves, NoInflightCaseRuns, RequirementsClosed, VersionReportPublished bool }
func (p ClosePredicate) Passed() bool
type ReportStatus struct { Status string; Fingerprint string }
type ReportStatusReader interface { VersionReportStatus(ctx context.Context, versionID int64) (ReportStatus, error) }
type VersionCloseInput struct { Version domain.Version; BlockingWorkItems, ActiveIntents, OpenHumanRequests, ActiveWaves, InflightCaseRuns int; Requirements []domain.Requirement; Report ReportStatus }
func EvaluateVersionClose(in VersionCloseInput) ClosePredicate
func (s *VersionCloseService) Evaluate(ctx context.Context, versionID int64) (ClosePredicate, error)
func (s *VersionCloseService) EnsureVersionClose(ctx context.Context, versionID int64) error // reconciler：谓词满足时 ensure version.close
func (s *VersionCloseService) Close(ctx context.Context, versionID, runID int64, reason string) (domain.Version, error)
func (s *VersionCloseService) ReopenIfCompleted(ctx context.Context, versionID int64, trigger, reason string) error
```

- `Close`：事务内重算谓词；条件 `UPDATE ... WHERE id=$1 AND status='running'`；0 row → `ErrStateConflict`；写 closed_at/close_reason/close_run_id；事件 `version.closed`（version+system）；审计。
- `ReopenIfCompleted`：条件 `completed→running`，清 closed_*；调 report stale 钩子；事件 `version.reopened`；非 completed no-op。
- 构造 `NewVersionCloseService(..., tasks TaskEnsurer, reportStatus ReportStatusReader)`；`EnsureVersionClose` 用 tasks 幂等 ensure `version.close`。
- 谓词排除 `version.close` 自身；`waiting_human` 以 open human_request/paused wave 等价。

- [ ] **Step 1: 写失败测试**

domain：8 条件逐条；`version.close` 自排除；`report` stale/指纹不匹配 → false。
service：close 成功/冲突；并发 `TestVersionCloseConcurrentNewIntent`（close 与新建 intent 只能一方最终生效）；reopen 清字段/事件；非 completed no-op；审计 actor。
repo 集成：条件 UPDATE 行为、close 字段、两连接竞态。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/version/domain ./internal/version/application -run 'TestVersionClose|TestClosePredicate' -v; $env:EZTEST_TEST_DATABASE_URL='...'; ..\.tools\go\bin\go.exe test -tags=integration ./internal/version/infrastructure/postgres -run TestVersionCloseRepo -v`
Expected: FAIL。

- [ ] **Step 3: 实现**

按 spec §7.3–§7.5；`version.close` 任务类型排除；`Close` 事务内 `FOR UPDATE` 复查；`ReopenIfCompleted` 供 design/intent/需求钩子复用。

- [ ] **Step 4: 运行测试**

Run: 同上 → PASS。

- [ ] **Step 5: Commit**

```bash
git add backend/internal/version
git commit -m "feat(p4): add version close predicate and reopen service"
```

---

## Task 15: Steward 工具 + P4 reconciler 规则（W3）

**Files:**
- Create: `backend/internal/version/application/tools.go`
- Create: `backend/internal/version/application/tools_test.go`
- Create: `backend/internal/agent/application/tools/p4_version_tools.go`（`init(){ RegisterP4ToolGroup(...) }`）
- Create: `backend/internal/agent/application/task_context_p4_closure.go`（`init(){ RegisterP4TaskContext(...) }`）
- Create: `backend/internal/agent/application/p4_ensure.go`
- Create: `backend/internal/agent/application/p4_ensure_test.go`
- Modify: `backend/internal/agent/application/reconciler.go:26-74`（P4 ensurer 调用）

**Interfaces:**
- Consumes: Task 13/14 services；delivery/report services 由 Lead 在 Task 23 装配。
- Produces:
  - tools：`requirement.goal_met`（调 ClosureService.Close）、`version.close_read`（调 VersionCloseService.Evaluate）、`version.close_propose`（调 Close）。
  - `P4Ensurer interface { ScanFailures(ctx, versionID int64) error; EnsureReports(ctx, versionID int64) error; EnsureClosures(ctx, versionID int64) error; EnsureVersionClose(ctx, versionID int64) error }`。
  - reconciler 每轮对 running 版本调用上述 4 个方法；实现由 `delivery.IssueService.ScanFailures`、`delivery.ReportService.EnsureReports`、`version.ClosureService.EnsureClosures`、`version.VersionCloseService.EnsureVersionClose` 提供（Task 23 装配）；本任务只定义接线与调用。
  - 任务 dedupe（由各 ensure 实现负责）：`triage:{issue_id}`、`report:{report_id}:r{revision}`、`req-close:{requirement_id}:{requirement_version_id}:{close_round}`、`ver-close:{version_id}:{report_revision_id}:{predicate_fingerprint}`。

- [ ] **Step 1: 写失败测试**

tool 单测（fake 服务）：goal_met 成功/失败 details；close_propose 成功/409；close_read 返回 8 项；工具白名单/参数 scope 注入。
reconciler 测试（fake P4Ensurer）：running 版本每轮 4 个方法被调用；completed 版本不调用；某方法报错不中断既有规则；P4Ensurer nil 安全。
ensure 单测不在此任务（由 Task 6/10/13/14 各自覆盖）。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/version/application ./internal/agent/application -run 'TestP4Tools|TestP4Ensure|TestReconcilerP4' -v`
Expected: FAIL。

- [ ] **Step 3: 实现**

工具参数不含 versionId（scope 注入）；写操作只走 service；reconciler 只调用 ensurer，不执行业务动作；错误/事件 best-effort。

- [ ] **Step 4: 运行测试**

Run: 同上 → PASS。

- [ ] **Step 5: Commit**

```bash
git add backend/internal/version backend/internal/agent
git commit -m "feat(p4): add steward tools and P4 reconciler rules"
```

---

## Task 16: 版本/需求 API 增量（W3）

**Files:**
- Modify: `backend/internal/version/interfaces/httpapi/handler.go`（version reopen；DTO 增 closed_at/close_reason）
- Modify: `backend/internal/version/interfaces/httpapi/requirement_handler.go`（requirement reopen；RequirementView 增 closed_at/closure）
- Modify: `backend/internal/version/interfaces/httpapi/version_handler_test.go`/`requirement_handler_test.go`
- Create: `backend/internal/version/interfaces/httpapi/routes_p4_test.go`（本地 `httptest` + Register）

**Interfaces:**
- Consumes: Task 14 `ReopenIfCompleted`/`Close`；Task 13 `Reopen`。
- Produces：
  - `POST /api/versions/{versionId}/reopen` body `{reason}` → VersionView；仅 completed→running；409 `state_conflict`。
  - `POST /api/versions/{versionId}/requirements/{requirementId}/reopen` body `{reason}` → RequirementView；仅 closed→draft；409。
  - `GET /api/versions/{id}` 增 `closed_at/close_reason`；RequirementView 增 `closed_at/closure{closure_id,close_round,closed_at,reopen_count}`。
  - PATCH version 不允许改 status（现有 `Update` 只 title/auto flag；补测试防回归）。

- [ ] **Step 1: 写失败 handler 测试**

version reopen：成功后 DTO 三态/closed 字段；非 completed → 409。requirement reopen：closed→draft + closure 投影；非 closed → 409。PATCH status → 400/忽略且不改变状态。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/version/interfaces/httpapi -run 'TestVersionReopen|TestRequirementReopen|TestVersionPatch' -v`
Expected: FAIL。

- [ ] **Step 3: 实现**

handler 只调 application；错误映射；DTO ID 字符串；reopen reason 必填；审计 actor。

- [ ] **Step 4: 运行单测 + 本地路由测试**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/version/interfaces/httpapi -v`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add backend/internal/version/interfaces/httpapi
git commit -m "feat(p4): expose version and requirement reopen APIs"
```

---

## Task 17: Chain 领域/应用构建器（W4）

**Files:**
- Create: `backend/internal/chain/domain/build.go`
- Create: `backend/internal/chain/domain/build_test.go`
- Create: `backend/internal/chain/application/service.go`
- Create: `backend/internal/chain/application/service_test.go`

**Interfaces:**
- Consumes: Task 2 `chain/domain` 类型/ports。
- Produces:

```go
type SourceGraph struct { Requirements []Node; Scenarios []Node; Cases []Node; Waves []Node; CaseRuns []Node; Issues []Node; Reports []Node; Closures []Node; SUTs map[int64]string; Pages map[int64]string }
func BuildChain(src SourceGraph, f ChainFilter, b Budget, snapshotCursor int64, now time.Time) ChainView
type ChainService struct { /* version/design/execution/delivery/resource providers, clock, events */ }
func NewChainService(version VersionChainProvider, design DesignChainProvider, execution ExecutionChainProvider, delivery DeliveryChainProvider, resource ResourceChainProvider, cursor CursorReader, clock Clock) *ChainService
func (s *ChainService) Read(ctx context.Context, versionID int64, f ChainFilter) (domain.ChainView, error)
```

- 构建：需求→场景→用例→case_run→wave→issue/report/closure 节点与边；`focus_node` 补祖先路径；过滤后只留诱导边；预算截断按 spec §8.3；`Replay.Approximate=true`，无 as-of 参数。
- `CursorReader.LatestCursor(ctx, channel string) (int64, error)` 由 eventing 适配。

- [ ] **Step 1: 写失败测试**

`build_test.go`：完整图节点/边；SUT/page/wave/status/result 过滤；`focus_node` 即使被过滤也保留并补祖先；节点 300/边 600/需求 100 截断顺序与 `truncated/reason`；空图；merged issue 边；跨 SUT 用例两个 SUT 标签。
`service_test.go`（fake providers）：Read 组装/版本归属校验/provider 错误传播；replay 模式 live/wave/range 与 `approximate=true`；`snapshot_cursor` 传递。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/chain/domain ./internal/chain/application -run 'TestBuildChain|TestChainService' -v`
Expected: FAIL。

- [ ] **Step 3: 实现**

按 spec §8.2–§8.4；domain 不导入其他上下文；provider 类型只含链需要的 ID/标签/状态；截断不静默丢 `focus_node`/requirement+closure。

- [ ] **Step 4: 运行测试**

Run: 同上 → PASS。

- [ ] **Step 5: Commit**

```bash
git add backend/internal/chain
git commit -m "feat(p4): add chain read-model builder and service"
```

---

## Task 18: Chain providers + 历史 CaseReader + API（W4）

**Files:**
- Create: `backend/internal/design/application/history.go`
- Create: `backend/internal/design/application/history_test.go`
- Create: `backend/internal/chain/infrastructure/providers/version.go`
- Create: `backend/internal/chain/infrastructure/providers/design.go`
- Create: `backend/internal/chain/infrastructure/providers/execution.go`
- Create: `backend/internal/chain/infrastructure/providers/delivery.go`
- Create: `backend/internal/chain/infrastructure/providers/resource.go`
- Create: `backend/internal/chain/infrastructure/providers/eventcursor.go`
- Create: `backend/internal/chain/interfaces/httpapi/handler.go`
- Create: `backend/internal/chain/interfaces/httpapi/dto.go`
- Create: `backend/internal/chain/interfaces/httpapi/handler_test.go`（本地 `httptest` + Register）
- Create: `backend/internal/chain/infrastructure/providers/providers_integration_test.go`（`//go:build integration`）

**Interfaces:**
- Consumes: Task 17 `ChainService`；Task 2 ports；design/execution/delivery/version/resource application methods。
- Produces:
  - design：`func (g *GraphService) ReadCaseVersionsByIDs(ctx context.Context, versionID int64, caseVersionIDs []int64) ([]CaseVersionHistoryView, error)`；`type CaseVersionHistoryView struct { ID int64; Revision int32; Title, Status string }`；不改变 `ListExecutable` 的 current+confirmed 语义。
  - `GET /api/versions/{versionId}/exploration-chain` 返回 `ChainView`（spec §4.2）。
  - 5 个 provider 适配；`EventCursorProvider` 读 eventing 最新 seq。
  - handler 解析 `sut_id/page_id/wave_id/status/result/event_from/event_to/focus_node/limit`，转 `ChainFilter`。

- [ ] **Step 1: 写失败测试**

`history_test.go`：插入 superseded case_version，按 id 读取返回非空标题/状态；不返回未授权版本；不存在 id 跳过。
handler 单测（fake service）：filter 解析、字符串 ID、空图、`truncated`、`replay.approximate=true`、`focus_node` 透传。
providers 集成（真实 PG）：插入 2 需求/2 用例/2 wave/1 issue/1 report；provider 组装节点/计数；design history 返回 superseded 标题。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/design/application -run TestReadCaseVersionsByIDs -v; ..\.tools\go\bin\go.exe test ./internal/chain/interfaces/httpapi -run TestChainHandler -v; $env:EZTEST_TEST_DATABASE_URL='...'; ..\.tools\go\bin\go.exe test -tags=integration ./internal/chain/infrastructure/providers -run TestProviders -v`
Expected: FAIL。

- [ ] **Step 3: 实现**

providers 只调对方 application 方法（不直读表）；chain service 做过滤/预算；handler 不写 SQL；event cursor 用 eventing Snapshot 最新 seq。

- [ ] **Step 4: 运行测试**

Run: 同上 → PASS。

- [ ] **Step 5: Commit**

```bash
git add backend/internal/chain backend/internal/design/application
git commit -m "feat(p4): add chain providers, history reader and API"
```

---

## Task 19: 前端问题 Tab（W5）

**Files:**
- Create: `frontend/src/features/delivery/types.ts`
- Create: `frontend/src/features/delivery/api.ts`
- Create: `frontend/src/features/delivery/IssuesTab.tsx`
- Create: `frontend/src/features/delivery/IssueDetailSheet.tsx`
- Create: `frontend/src/features/delivery/MergeIssueDialog.tsx`
- Create: `frontend/src/features/delivery/IssuesTab.test.tsx`
- Modify: `frontend/src/features/versions/VersionDetailPage.tsx:28-57`（替换问题占位）

**Interfaces:**
- Consumes: Task 7 API；`apiGet/apiGetPage/apiPost`（`src/lib/api-client.ts:217-324`）；既有 Tabs/Table/Dialog/EmptyState/ErrorState/LoadingCard。
- Produces: `IssuesTab({versionId})`；`types.ts` 中 `IssueView/IssueDetailView`（ID 字符串）；`api.ts`：`listIssues`、`getIssue`、`confirmIssue`、`dismissIssue`、`reopenIssue`、`mergeIssue`。

- [ ] **Step 1: 写失败测试**

mock `./api`：列表渲染标题/状态/severity/refs 计数；筛选调用参数；详情 Sheet 打开/关闭；确认/驳回/重开调用 API 并刷新；合并弹窗目标选择与 409 内联错误；`?issue=` 深链自动打开；空/加载/错误态。

- [ ] **Step 2: 运行确认失败**

Run: `cd frontend; pnpm test -- src/features/delivery/IssuesTab.test.tsx`
Expected: FAIL（组件/导出不存在）。

- [ ] **Step 3: 实现**

语义 token（不抄 mock hex）；URL 为详情真值；只走 api-client；错误 toast/inline；无 code anchor（P5）。

- [ ] **Step 4: 运行测试**

Run: 同上 → PASS。

- [ ] **Step 5: Commit**

```bash
git add frontend/src/features/delivery frontend/src/features/versions
git commit -m "feat(p4): add issues tab"
```

---

## Task 20: 前端报告 Tab（W5）

**Files:**
- Create: `frontend/src/features/delivery/ReportsTab.tsx`
- Create: `frontend/src/features/delivery/ReportDetailPanel.tsx`
- Create: `frontend/src/features/delivery/ReportsTab.test.tsx`
- Modify: `frontend/src/features/delivery/types.ts`（ReportView/ReportRevisionView）
- Modify: `frontend/src/features/delivery/api.ts`（listReports/getReport/getRevision/regenerate/downloadHTML）
- Modify: `frontend/src/features/versions/VersionDetailPage.tsx`（替换报告占位）

**Interfaces:**
- Consumes: Task 12 API；`apiDownload`（`api-client.ts:280`）。
- Produces: `ReportsTab({versionId})`；版本总结默认 + wave 切换；revision 下拉/as-of/stale/render_status；HTML 打开/下载；重生成 202 后刷新。

- [ ] **Step 1: 写失败测试**

mock api：默认选中 version 报告；revision 切换更新 as-of；stale 徽标/提示；重生成调用并禁用重复；HTML 下载调用 apiDownload；失败错误态；无报告空态；PDF 显示“P7”。

- [ ] **Step 2: 运行确认失败**

Run: `cd frontend; pnpm test -- src/features/delivery/ReportsTab.test.tsx`
Expected: FAIL。

- [ ] **Step 3: 实现**

左列表+右详情；URL `?report=`/`?revision=`；HTML 新窗口/下载按 api-client 现有方式；不嵌入大图。

- [ ] **Step 4: 运行测试**

Run: 同上 → PASS。

- [ ] **Step 5: Commit**

```bash
git add frontend/src/features/delivery frontend/src/features/versions
git commit -m "feat(p4): add reports tab"
```

---

## Task 21: 前端链路 Tab（W5）

**Files:**
- Create: `frontend/src/features/chain/types.ts`
- Create: `frontend/src/features/chain/api.ts`
- Create: `frontend/src/features/chain/ChainTab.tsx`
- Create: `frontend/src/features/chain/ChainGraph.tsx`
- Create: `frontend/src/features/chain/ChainFilters.tsx`
- Create: `frontend/src/features/chain/ChainDetailSheet.tsx`
- Create: `frontend/src/features/chain/layout.ts`
- Create: `frontend/src/features/chain/ChainTab.test.tsx`
- Modify: `frontend/src/features/versions/VersionDetailPage.tsx`（替换链路占位）

**Interfaces:**
- Consumes: Task 18 API；`@xyflow/react` 12.12；`useVersionEvents`；`apiGet`。
- Produces: `ChainTab({versionId})`；节点/边渲染、过滤、图例、统计、实时跟随、波次历史/事件时间线、截断 Alert、详情 Sheet、深链 `?node/issue/report/wave`。

- [ ] **Step 1: 写失败测试**

mock api/React Flow：节点/边数量与 label；SUT/波次/状态过滤参数；空/加载/错误；`truncated=true` 显示提示；`?node=` 选中详情；点击问题/报告节点跳转深链；实时开关；历史波次选择。

- [ ] **Step 2: 运行确认失败**

Run: `cd frontend; pnpm test -- src/features/chain/ChainTab.test.tsx`
Expected: FAIL。

- [ ] **Step 3: 实现**

布局复用 design/layout 经验但独立 `features/chain`；语义 token；全宽容器；`app-content` max-width 仅本 Tab 放宽；不显示“as-of”，历史回看标“近似”。

- [ ] **Step 4: 运行测试**

Run: 同上 → PASS。

- [ ] **Step 5: Commit**

```bash
git add frontend/src/features/chain frontend/src/features/versions
git commit -m "feat(p4): add exploration chain tab"
```

---

## Task 22: 版本头/待办/SSE 集成（W5）

**Files:**
- Create: `frontend/src/features/versions/VersionHeader.tsx`
- Create: `frontend/src/features/versions/VersionHeader.test.tsx`
- Create: `frontend/src/features/shell/TodoDrawer.tsx`
- Create: `frontend/src/features/shell/TodoDrawer.test.tsx`
- Modify: `frontend/src/features/versions/VersionDetailPage.tsx`（使用 VersionHeader；Tab 计数）
- Modify: `frontend/src/features/clarification/useVersionEvents.ts:81-149`（新增事件分支）
- Create: `frontend/src/features/clarification/useVersionEvents.test.ts`（扩展现有文件）

**Interfaces:**
- Consumes: `useVersionEvents(versionId, handlers)`；`apiGet` human-requests；既有 PendingDesignCTA。
- Produces:
  - VersionHeader：status/closed_at/close_reason/SUT chips/待办入口/“查看探索链路”/completed 时“重新打开”确认框（reason 必填）。
  - TodoDrawer：当前版本 open human_request + 待设计 CTA，只读+跳转；无版本空态。
  - `useVersionEvents` switch 增加 `issue.*`、`report.*`、`requirement.closed/reopened`、`version.closed/reopened` → 调用对应 handlers（不重建业务状态）。

- [ ] **Step 1: 写失败测试**

VersionHeader：三状态徽标；completed 显示 closed 信息/重开；reopen 调 API 后刷新；待办计数。
TodoDrawer：open human_request 列表/跳转/空态；resolved 后消失（mock）。
useVersionEvents：新事件类型调用 handlers、未知事件忽略、stream.reset 触发重连回调；不新增 EventSource。

- [ ] **Step 2: 运行确认失败**

Run: `cd frontend; pnpm test -- src/features/versions/VersionHeader.test.tsx src/features/shell/TodoDrawer.test.tsx src/features/clarification/useVersionEvents.test.ts`
Expected: FAIL。

- [ ] **Step 3: 实现**

只走 api-client/sse-client；URL 深链；reason 必填；不新增 `/api/todos`；Topbar 入口按当前版本上下文显示。

- [ ] **Step 4: 运行测试**

Run: 同上 → PASS。

- [ ] **Step 5: Commit**

```bash
git add frontend/src/features/versions frontend/src/features/shell frontend/src/features/clarification
git commit -m "feat(p4): add version header, todo drawer and SSE events"
```

---

## Task 23: 集成装配（Lead）

**Files:**
- Modify: `backend/internal/server/server.go:35-85`（Deps 增 `Delivery`、`Chain` 等；注册 P4 路由）
- Modify: `backend/cmd/eztest/main.go:505-780`（构造 delivery/chain/version 服务、providers、ensurers、P4 registry/finalizer、启动 reconciler；注入 design 的 VersionReopener）
- Create: `backend/internal/server/server_p4_integration_test.go`（`//go:build integration`）
- Create: `backend/internal/server/server_p4_wiring_test.go`（非集成：nil-safe/构造顺序）
- Modify: `backend/internal/design/application/planner_ports.go`（`VersionReopener` consumer port）与 `cmd/eztest/main.go:536` 构造

**Interfaces:**
- Consumes: Tasks 6/10/13/14 服务的 ensure 方法；Tasks 7/12/16/18 的 `Register`/handler；Task 11 `ReportFinalizer`；Task 3 registry。
- Produces:
  - `server.Deps` 新字段：`Delivery http.Handler`、`Chain http.Handler`（或具体 handler 类型）；`server.New` 注册 `/api/versions/{id}/issues|reports|exploration-chain`、version/requirement reopen、报告 HTML。
  - `cmd/eztest` 构造：`Delivery IssueService/ReportService`、`ChainService` + 5 providers、`ClosureService`、`VersionCloseService`、`P4Ensurer` 组合、`NewP4Registry`、`ReportFinalizer`；reconciler 随进程启停（WaitGroup）。
  - design `VersionReopener interface { ReopenIfCompleted(ctx, versionID int64, trigger, reason string) error }`；case 确认后 best-effort 调用，失败由 reconciler 兜底。

- [ ] **Step 1: 写失败集成测试**

`server_p4_integration_test.go`：真实 PG + 迁移 24 + 真实服务；断言 issues/reports/chain 列表 200、report HTML 200、version/requirement reopen 200/409、未授权 401/404；P4Profiles 与 task 映射可见；reconciler ensurer 被构造（通过 fake/端口断言）。
`server_p4_wiring_test.go`：nil handler 不 panic；构造顺序；registry 含 8 工具；profile 3 个。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test -tags=integration ./internal/server -run TestP4 -v; ..\.tools\go\bin\go.exe test ./internal/server -run TestP4Wiring -v`
Expected: FAIL（未装配）。

- [ ] **Step 3: 实现装配**

按 spec §2.1/§2.3：cmd 唯一组合根；后台 worker/reconciler 用 WaitGroup 启停；跨上下文只依赖 application/consumer port；design 构造注入 `VersionReopener`；report finalizer 只在 report.* run 后调用。

- [ ] **Step 4: 运行测试**

Run: 同上 → PASS。

- [ ] **Step 5: Commit**

```bash
git add backend/internal/server backend/internal/design backend/cmd/eztest
git commit -m "feat(p4): wire delivery, chain and steward into composition root"
```

---

## Task 24: 验收脚本 + 真实 LLM 运行（W6）

**Files:**
- Modify: `scripts/p4-contract-check.cjs`（最终断言/冻结模式）
- Create: `scripts/p4-acceptance.ps1`
- Create: `scripts/p4-e2e.ps1`
- Create: `frontend/e2e/p4.spec.ts`
- Modify: `scripts/fixture-target.cjs`（如需 P4 失败/多波次夹具）
- Create: `.tmp/p4-acceptance/scenarios.txt`（运行时）
- Create: `.tmp/p4-acceptance/screenshots/`（运行时）

**Interfaces:**
- Consumes: spec §12/§13；P3 acceptance/fixture 脚本模式。
- Produces：S1–S9 API/DB 断言 + S10 浏览器 E2E + 截图；真实 DeepSeek 配置；隔离库 `eztest_v02_p4`；端口 API 14344/Web 16869/executor 14346/fixtures 13335/13337。

- [ ] **Step 1: 写验收脚本（先跑失败）**

`p4-acceptance.ps1` 步骤：清理/建库→迁移 24→启动 API/executor/fixture→admin 登录→真实 LLM 配置→资源/版本/需求/用例→执行产生 `failed(application)`→S1 问题（去重/确认/驳回/重开/合并）→S2 wave 报告/HTML/ledger→S3 stale/regenerate/旧 revision 不变→S4 需求闭环（open issue 不阻断；无 issue 失败用例 409 用 SQL 夹具）→S5 version 报告+Steward close（自排除）→S6 新需求/人工 reopen→S7 链路过滤/深链/历史/截断→S8 human_request 待办→S9 待设计阻断→汇总 PASS/FAIL。
Run: `powershell.exe -File scripts/p4-acceptance.ps1`
Expected: 初期 FAIL（实现未齐）；逐项修复后 PASS，`scenarios.txt` 全 PASS。

- [ ] **Step 2: 写浏览器 E2E（先跑失败）**

`p4-e2e.ps1` 启动 Web/API/executor/fixture（真实 LLM），`frontend/e2e/p4.spec.ts` 覆盖 S10：版本 Tab、问题动作、报告打开/revision/stale、链路过滤/历史/深链/截断、completed/重开、待办 Drawer；截图存 `.tmp/p4-acceptance/screenshots/`。
Run: `powershell.exe -File scripts/p4-e2e.ps1`
Expected: 初期 FAIL；实现后 PASS（1 passed）。

- [ ] **Step 3: 真实 LLM 全量运行**

Run:
`powershell.exe -File scripts/p4-acceptance.ps1`
`powershell.exe -File scripts/p4-e2e.ps1`
Expected: S1–S9 PASS；E2E PASS；记录模型名/耗时/turn 数；失败不重试超过 3 次、不 fake。

- [ ] **Step 4: P1/P2/P3 真实 LLM 回归**

Run:
`powershell.exe -File scripts/p1-acceptance-llm.ps1`
`powershell.exe -File scripts/p2-acceptance-llm.ps1`
`powershell.exe -File scripts/p2-e2e-llm.ps1`
`powershell.exe -File scripts/p3-acceptance.ps1`
`powershell.exe -File scripts/p3-e2e.ps1`
Expected: 全部 PASS；日志写 `.tmp/p4-acceptance/p1|p2|p3-*.log`。失败按系统缺陷修复后重跑，不用 fake。

- [ ] **Step 5: Commit**

```bash
git add scripts/p4-contract-check.cjs scripts/p4-acceptance.ps1 scripts/p4-e2e.ps1 scripts/fixture-target.cjs frontend/e2e/p4.spec.ts
git commit -m "test(p4): add real-LLM acceptance and browser E2E"
```

---

## Task 25: 全量门禁、契约冻结与 handoff（Lead）

**Files:**
- Modify: `docs/version-V0.2/contracts/README.md`（P4 frozen 证据）
- Modify: `docs/version-V0.2/contracts/*`（仅冻结状态/版本号，无内容漂移）
- Create: `docs/version-V0.2/phases/phase-04-handoff.md`
- Create: `.tmp/p4/final-gate.log`（运行时）

**Interfaces:**
- Consumes: Tasks 0–23 全部交付。
- Produces：P4 handoff（spec §18 十项）；`frozen: P4`；合回 `feat/v0.2`（阶段完成点）；最终报告。

- [ ] **Step 1: 更新契约冻结状态**

`openapi.yaml` 根 `x-frozen: P4` 保持；contracts README 写 P4 冻结状态 + 证据（命令/结果/路径）；不改变已冻结内容。

- [ ] **Step 2: 跑全量门禁**

Run:
`powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1 2>&1 | Tee-Object .tmp/p4/final-gate.log`
`cd backend; ..\.tools\go\bin\go.exe test -tags=integration ./internal/db/... ./internal/delivery/... ./internal/chain/... ./internal/version/...`
`node scripts/p4-contract-check.cjs; node scripts/p3-contract-check.cjs; node scripts/p2-contract-check.cjs`
`cd frontend; pnpm lint; pnpm typecheck; pnpm test; pnpm build`
Expected: 全 PASS；迁移 24 幂等；无 lint/typecheck/build 错误。

- [ ] **Step 3: 写 handoff**

按 spec §18：完成范围、契约变更/冻结、测试证据（含真实 LLM）、缺口/偏差（P4-D 表）、未交付实体移交表、下一阶段输入、前置检查、风险、裁决索引、规范自检 backlog、停止声明。

- [ ] **Step 4: 合回集成分支**

```bash
git switch feat/v0.2
git merge --no-ff feat/v0.2-p4 -m "merge(p4): delivery chain"
git switch feat/v0.2-p4
git log --oneline -5
```
Expected：合并成功；`feat/v0.2` 含 P4 提交；不自动进入 P5。

- [ ] **Step 5: Commit handoff + 最终报告**

```bash
git add docs/version-V0.2/phases/phase-04-handoff.md docs/version-V0.2/contracts/README.md
git commit -m "docs(p4): add phase-04 handoff and freeze contracts"
```

最终回复：完成范围、验收证据路径、已知缺口/风险、下一阶段输入；停止，不开始 P5。

---

## 并行执行映射

| 阶段 | 可并行任务 | 依赖 | 说明 |
|---|---|---|---|
| Foundation | 0 → 1 → 2 → 3 | 串行 | 契约/迁移/共享类型/agent+storage 骨架；Lead 独占 |
| Backend 域 | 4、9、13、17 | 3 | 四条 domain 并行（W1/W2/W3/W4） |
| Backend 仓储 | 5、10、14 | 4/9/13 | 各自 repo/service；W4 history 可与 17 并行 |
| Chain | 17、18 | 3/17 | chain builder → providers/history/API（W4） |
| Backend 应用/API | 6、11、15、16 | 5/10/14 | 事件/工具/reconciler/API；W3 依赖 13/14 |
| 收尾集成 | 7、8、12 | 6/11/15/16 | issue/report API 与 triage |
| Frontend | 19、20、21、22 | 0（契约）+ 对应 API | 可提前用 mock；E2E 在 24 |
| 集成装配 | 23 | 6/7/8/10/11/12/13/14/15/16/18 | cmd/server/seed 唯一收口；不与其他任务并行写 |
| 验收 | 24 | 全部 | 真实 LLM S1–S10；P1/P2/P3 真实回归 |
| 收口 | 25 | 24 | 门禁/冻结/handoff/合回 |

**Subagent dispatch 规则**：每个任务一个 subagent（fresh）；任务开工前读 spec 对应节 + 本任务 + Interfaces；只写本任务 Files；完成后运行本任务测试并提交；Lead 每合并一个流先跑 `go build ./...` + 该流测试。共享文件冲突时停止并回报，不擅自改 contracts/migrations/cmd/server/seed。

---

## Self-Review

**1. Spec coverage**

| Spec 节 | 任务 |
|---|---|
| §3 数据模型/迁移 | 0/1 |
| §4 API/事件/任务/工具/设置 | 0/7/8/11/12/15/16/18/23 |
| §5 issue | 4/5/6/7/8 |
| §6 report | 9/10/11/12 |
| §7 closure/close/reopen | 13/14/15/16 |
| §8 chain | 17/18 |
| §9 frontend | 19/20/21/22 |
| §10 SSE | 22 |
| §12 验收 | 24 |
| §13 门禁 | 1/24/25 |
| §14 契约冻结 | 0/25 |
| §16 P4-D | 全部任务；P4-D-16 backlog 在 25 |
| §18 handoff | 25 |

**2. Step scan**：每个任务有失败测试→运行失败→实现→运行通过→提交；无 TBD/“handle edge cases”；测试名/断言/signature 已写死；未贴实现体。

**3. Type consistency**：`ReportKind/ReportStatus/AnalysisStatus`、`Issue.Status/ExpectedRevision`、`ChainFilter/ChainView`、`ClosureChecks/ClosePredicate`、`ReadCaseVersionsByIDs`、`ReopenIfCompleted`、profile/work type/tool 名称在任务间一致；表名统一 `delivery_*`/`requirement_closure`。

**4. Review Focus tests**：自排除/竞态→Task 14；report 不可变/stale→Task 10；chain 历史/截断/focus→Task 17/18；多 SUT→Task 10/18；真实 LLM→Task 24。

**5. Proportion**：plan 以签名/测试/命令为主，未复写实现；26 个任务（含 Task 0 与集成/验收/收口）对应 spec 的 P4 L/6 WU。


