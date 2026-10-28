# EzTest P5 Mindmap & Review Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付「已确认需求 + 应用模型 + 代码事实 → 可评审用例脑图 → 评审 → 冻结基线 → 四格式导出」垂直切片，包含 4 个运行时 Agent、HTTP 只读探索、旧表名规范化、真实 LLM 验收与真实人工验收，达到 P5 退出标准。

**Architecture:** 新增 `backend/internal/mindmap` 限界上下文（DDD 四层）+ 9 张 `case_*` 表；4 个 seed Agent profile 走 P1 运行时，唯一结构化提交工具 + 服务端单事务校验物化；`version` 上下文新增 3 个状态流转方法（唯一写者）；`codeintel` 锚点主体扩宽 `case`；`resource` 应用模型来源扩宽 `explored`；前端新增 `features/mindmap` 与全屏 `/versions/[id]/mindmap` 页（手写 SVG，无新依赖）；先做 P0–P4 表名单数化改名，再叠加 P5 功能。

**Tech Stack:** Go 1.24.6（net/http、pgx/v5）、PostgreSQL 17.6、Next.js 16 / React 19 / TypeScript / Tailwind / Radix、Vitest、Playwright、Node fixture（无新依赖）。

**Spec:** [docs/superpowers/specs/2026-10-28-eztest-p5-mindmap-review-design.md](../specs/2026-10-28-eztest-p5-mindmap-review-design.md)

## Global Constraints

- 范围严格按 spec；**不实现** 执行/批次/证据/失败分类（P6）、AI 补全/继续中/问题/报告（P7/P6）、浏览器探索/Session 注入、parser/符号级分析、PNG 服务端渲染、XMind。
- **D29**：`case_*` 9 表主键雪花 ID（应用层生成，DDL 无序列/默认值）；JSON/URL/事件/工具参数中所有 mindmap ID 为十进制字符串；前端禁止 `Number()`。
- **D30**：导出物走文件系统（`EZTEST_EXPORT_DIR`，默认 `.data/exports`）；DB 只存路径 + sha256 + 大小；路径根内校验、原子 rename、0600/0700；禁止 base64/二进制入库；事件/审计不含脑图正文与 LLM 原文。
- **D34**：表名/索引命名规约见 spec §3.1；旧表以新增 `*_rename_tables.sql` 改名，**不改已应用迁移、不改旧字段/约束/既有索引名**；`schema_migrations` 例外；新表沿用 `created_at/updated_at`。
- **D35**：`designing→reviewing→executing`（驳回回 `designing`）；基线唯一、冻结后只读、只评审一次（D5）；blocker 批准需 `acknowledgeFindings=true`；批准跨上下文写入顺序为"先 baseline 后版本状态 + 失败补偿"（spec §6.4）。
- **D36**：探索仅 GET/HEAD、环境 `allowed_hosts` 白名单、私网/跨域重定向拒绝、≤40 请求/深度 2/5s 超时/60s 总时长/512 KiB 解析上限；admin 触发；产物 `source='explored'`；LLM 不可用 → 确定性 BFS，`run=degraded`。
- **D37**：`code_anchor.subject_kind` 扩宽含 `case`；既有 `version|requirement|app_model` 行为不变；锚点只经 codeintel `AnchorService` 读写。
- **D38**：服务端 Markdown/Mermaid/OPML + 前端 PNG；同 `(version,format,baseline)` 复用；`mermaid` 只承载结构，详情在 Markdown。
- **API 契约**：成功 `{"data":...}`；错误 `{error:{code,message,request_id,retryable,details}}`，`details.reason` 用 spec §7.4；列表 `next_cursor/has_more`；`Idempotency-Key` 支持 runs/review/export/submit-review；mindmap 路径用 `{versionId}`。
- **事件**：`channel=version:{id}`，`summary ≤500 rune`、`detail ≤64 KiB`、`refs ≤20`；新增 8 个 `case.*` 事件 + 复用 `gate.pending/resolved`，同步 OpenAPI `VersionEventType`。
- **迁移只追加**：新迁移追加到 `db/migrations/order.txt` 末尾；一个文件只属一个上下文；已应用迁移 checksum 不变；`order.txt` 仅由 Lead 修改。
- **不新增 Go/前端依赖**；前端 HTTP 只走 `api-client`，SSE 只走 `sse-client`/`useVersionStream`；图库/表单库/状态库禁止引入。
- 每个任务：先写失败测试 → 跑失败 → 最小实现 → 跑通过 → 提交；提交前缀 `feat/fix/test/docs/chore/refactor`。
- 分支 `feat/p5-mindmap-review`；**并行实现者不 commit**，Lead 按每个任务的写作用域 stage/commit，保证 per-task review range；验收后本地 ff 合回 `dev`，不 push。
- 不提交 `.env`、`.tools/`、`.tmp/`、`.data/`、`.superpowers/sdd/`、`*.local.md`。
- 端口沿用 Web `6868`、API `13333`、PostgreSQL `5433`、fake LLM `13334`、fixture `13335`；真实 LLM 验收 gated `EZTEST_RUN_REAL_LLM=1`。

### 命令约定

- Go 单元：`cd backend; ..\.tools\go\bin\go.exe test ./internal/mindmap/...`（或 `go test`）。
- 领域/配置快速：`cd backend; go test ./internal/mindmap/domain ./internal/mindmap/application`
- 集成：`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-integration.ps1 ./internal/mindmap/integration/...`
- 前端：`cd frontend; pnpm vitest run src/features/mindmap`、`pnpm lint`、`pnpm typecheck`
- 全量门禁：`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1`
- E2E：`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1 mindmap`
- 完整验收：`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1`
- 真实 LLM：`$env:EZTEST_RUN_REAL_LLM="1"; powershell -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance-mindmap-llm.ps1`

## Concurrency Map（实现时并行执行的唯一依据）

| Wave | Tasks | 并行规则 |
|---|---|---|
| **W0** | T0 | Pre-flight（Lead，一次性） |
| **W1** | T1 ∥ T2 ∥ T3 ∥ T4 ∥ T5 | 按上下文改名；每任务只改本上下文迁移 + 本上下文 Go SQL/测试，写作用域互斥 |
| **W2** | T6 ∥ T7 | T6=共享引用+命名规约文档；T7=mindmap 领域+3 迁移+ports；文件不重叠 |
| **W3** | T8 ∥ T9 ∥ T17 ∥ T18 | 仓储 / 图应用服务 / version 状态方法 / resource+codeintel 小扩展；消费 T7 接口，写包不同 |
| **W4** | T10 ∥ T11 ∥ T12 ∥ T13 ∥ T20 | 物化+run / 评审+基线 / 导出 / 探索 / 前端数据层；application 内按文件拆分，前端包独立 |
| **W5** | T14 ∥ T15 ∥ T21 ∥ T25 | 适配器 / Agent 工具 / 画布布局 / fake LLM+fixture；互不重叠 |
| **W6** | T16 ∥ T22 | HTTP API+组装根 / 编辑器+来源面板；T16 依赖 T8–T15，T22 依赖 T21 |
| **W7** | T19 ∥ T23 ∥ T24 | OpenAPI / 评审+运行+导出 UI / 页面组装+入口；T19 依赖 T16，T24 依赖 T20–T23 |
| **W8** | T26 ∥ T27 | E2E spec / 验收脚本+README+env；均依赖 T16/T19/T24/T25 |
| **W9** | T28 | 文档同步 D35–D38 + 全量门禁（需要全部功能稳定） |
| **W10** | T29 | 真实 LLM 验收 + 真实人工验收 + 合回 `dev`（Lead，串行） |

- 同一 Wave 内任务派发给独立 subagent；每个任务的 `**写作用域**` 是该 subagent 唯一允许修改的路径前缀。
- 任务按 Wave 分组排列；编号 `T1–T29` 只作唯一标识，**执行顺序以 Wave 与依赖为准**，不按编号大小。
- **同包/同链串行**：`mindmap/application` 内部 `graph(T9)→materialize/run(T10)→review/baseline(T11)→export(T12)→explore(T13)` 按序演进；W4 内各文件独立可并行，但接口以 T7 的 `ports.go` 为准，**新增端口方法只能由 Lead 追加**；`mindmap/interfaces/httpapi` 仅 T16；`cmd/eztest`+`server` 仅 T16；前端 `features/mindmap` 链 T20→T21→T22→T23→T24。
- `db/migrations/order.txt` 只由 Lead 在每批任务完成后追加；任何 subagent 不得修改。
- 跨 Wave 依赖未完成不得启动；并行实现者不 commit；Lead 逐任务 stage/commit 后进入下一批。
- T17 与 T18 可并发（version ∥ resource/codeintel）；T19 在 T16 之后（OpenAPI 需最终路由/DTO 定型）。

## Review Focus（最容易被忽略、必须有测试钉住的 5 类输入）

1. **改名遗漏**：39 张旧表的 SQL 引用（仓储/测试/脚本/`testutil`/验收 psql）任何一处漏改 → 运行时 500；用"迁移后旧名不存在、新名存在"的集成断言 + `grep` 全仓检查兜底。
2. **跨上下文冻结竞态**：批准时版本状态被并发抢占/重复批准 → 不能出现"executing 无 baseline"或"baseline 无状态"；补偿必须删 baseline 并回滚节点状态，且重复批准 409。
3. **AI 产出不可信**：非法/超限/篡改的 proposal（重复 ref、未确认需求、越界 stepIndex、假文件锚点、超大文本、注入 JSON）必须 0 物化、run=failed、无半写。
4. **OCC 与子集合一致性**：节点 revision 过期、并发增删步骤/断言、删最后一条需求来源/最后一条断言 → 409 且不产生悬挂数据。
5. **SSRF 与导出路径**：探索非白名单/私网/跨域重定向/非 GET；导出路径穿越/哈希不符/复用并发；必须拒绝并可解释（reason）。

---

## Pre-flight（T0，一次性，不算任务）

- [ ] 确认在 `dev` 且工作区干净：`git status --short`；确认 spec 提交 `8dc81ee` 在历史中。
- [ ] 创建分支：`git switch -c feat/p5-mindmap-review`。
- [ ] 启动数据库：`powershell -File scripts/db.ps1 status`，必要时 `start`。
- [ ] 确认 `frontend/node_modules` 存在；否则 `cd frontend; pnpm install`。
- [ ] 确认基线绿：`powershell -File scripts/test.ps1`（至少 Go 单测 + 前端 typecheck/vitest）。
- [ ] 确认 `.env` 含 `EZTEST_TEST_DATABASE_URL`（`eztest_test`）、可写 `.data/`；`llm_base_url/key/model_name` 非空（真实验收 gated）。
- [ ] 确认端口 `13333/6868/13334/13335` 空闲；`git --version` 可用。
- [ ] 记录 `git rev-parse HEAD` 作为 plan 基线（记为 `<base>`），后续每个任务的 review range 使用 `git diff <prev>..<cur> -- <scope>`。
- [ ] 预建 13 条迁移的空文件占位？**不预建**；每个任务自己创建并指明文件名，Lead 在批次结束时统一追加 `order.txt`。

---

## Shared Interfaces（由 T7 定义；后续任务消费，不得改名）

```go
// backend/internal/mindmap/domain
const (
    NodeKindScenario = "scenario"; NodeKindCase = "case"
    NodeSourceUserInput = "user_input"; NodeSourceAIGenerated = "ai_generated"; NodeSourceAISupplement = "ai_supplement"
    NodeStatusDraft = "draft"; NodeStatusReviewed = "reviewed"; NodeStatusExecuted = "executed"
    NodeStatusSupplemented = "supplemented"; NodeStatusMerged = "merged"; NodeStatusRejected = "rejected"
    NodeStatusNeedsRevalidation = "needs_revalidation"

    PriorityP0, PriorityP1, PriorityP2, PriorityP3 = "P0", "P1", "P2", "P3"

    RefRequirement = "requirement"; RefAppModel = "app_model"; RefAIRun = "ai_run"; RefManual = "manual"

    RunKindExplore = "explore"; RunKindDesign = "design"; RunKindReview = "review"; RunKindDataPlan = "data_plan"
    RunQueued = "queued"; RunRunning = "running"; RunDone = "done"; RunFailed = "failed"; RunDegraded = "degraded"

    ReviewDraft = "draft"; ReviewApproved = "approved"; ReviewRejected = "rejected"
    FindingCoverage = "coverage"; FindingAssertion = "assertion"; FindingExecutability = "executability"
    FindingDuplicate = "duplicate"; FindingCodeUncovered = "code_uncovered"; FindingOther = "other"
    SeverityBlocker = "blocker"; SeverityMajor = "major"; SeverityMinor = "minor"; SeverityInfo = "info"
    FindingOpen = "open"; FindingFixed = "fixed"; FindingDismissed = "dismissed"

    ExportMarkdown = "markdown"; ExportMermaid = "mermaid"; ExportOPML = "opml"

    EventCaseCreated = "case.created"; EventCaseUpdated = "case.updated"; EventCaseDeleted = "case.deleted"
    EventCaseReviewed = "case.reviewed"; EventCaseBaselineFrozen = "case.baseline_frozen"
    EventCaseRunStarted = "case.run_started"; EventCaseRunFinished = "case.run_finished"
    EventCaseExportReady = "case.export_ready"; EventGatePending = "gate.pending"; EventGateResolved = "gate.resolved"
)

type CaseNode struct {
    ID, VersionID int64
    ParentID *int64
    Kind, Title, Description, Priority, Preconditions string
    TestData []byte // JSONB
    Source, Status string
    SortOrder, Revision int
    CreatedBy, UpdatedBy int64
    CreatedAt, UpdatedAt time.Time
}
type CaseStep struct { ID, NodeID int64; Seq int; Action, Expected, Data, Source string; CreatedAt, UpdatedAt time.Time }
type CaseAssertion struct { ID, NodeID int64; StepID *int64; Kind, Target, Expression, Expected, Source string; CreatedAt, UpdatedAt time.Time }
type CaseSource struct { ID, VersionID, NodeID int64; RefKind string; RefID *int64; RefKey, Source, Confidence string; CreatedAt, UpdatedAt time.Time }
type CaseReview struct { ID, VersionID int64; RunID *int64; Status, Comment string; ReviewerID int64; DecidedBy *int64; DecidedAt *time.Time; CreatedAt, UpdatedAt time.Time }
type CaseReviewFinding struct { ID, ReviewID int64; NodeID *int64; Category, Severity, Detail, Status, Source string; CreatedBy int64; CreatedAt, UpdatedAt time.Time }
type CaseBaseline struct { ID, VersionID, ReviewID int64; NodeCount, CaseCount int; GraphHash string; CodeBaselineCommit *string; FrozenBy int64; FrozenAt time.Time }
type CaseDesignRun struct { ID, VersionID int64; Kind, Status string; AgentSessionID, AgentRunID *int64; Model, PromptVersion string; Input, Result, Stats, Errors []byte; CreatedBy int64; CreatedAt, UpdatedAt time.Time; StartedAt, FinishedAt *time.Time }
type CaseExport struct { ID, VersionID int64; Format, FilePath, FileSHA256 string; SizeBytes int64; BaselineID *int64; NodeCount int; CreatedBy int64; CreatedAt, UpdatedAt time.Time }

// 树/覆盖/哈希纯函数
func ValidateGraph(nodes []CaseNode, steps map[int64][]CaseStep, assertions map[int64][]CaseAssertion) error
func CanonicalGraphHash(g GraphSnapshot) string // sha256(canonical JSON)
func BuildCoverage(versionID int64, confirmed map[int64]bool, nodes []CaseNode, sources []CaseSource) Coverage

// 领域错误（application 映射为 HTTP）
type Error struct { Kind, Reason, Field, Message string }
func NewInvalid(field, reason, message string) *Error
func NewNotFound(resource string, id int64) *Error
func NewConflict(reason, message string) *Error
func NewStateConflict(reason, message string) *Error
```

```go
// backend/internal/mindmap/application/ports.go —— T7 一次性定义齐全
type NodeRepository interface {
    Create(ctx, n domain.CaseNode) (domain.CaseNode, error)
    Get(ctx, versionID, nodeID int64) (domain.CaseNode, error)
    ListByVersion(ctx, versionID int64) ([]domain.CaseNode, error)
    Update(ctx, n domain.CaseNode, expectedRevision int) (domain.CaseNode, error) // CAS
    Delete(ctx, versionID, nodeID int64, expectedRevision int) error
    UpdateStatusByVersion(ctx, versionID int64, from, to string) (int, error)
    SetSortOrders(ctx, versionID int64, ordered []int64) error
    CountByVersion(ctx, versionID int64) (nodes int, cases int, err error)
}
type StepRepository interface {
    ListByVersion(ctx, versionID int64) ([]domain.CaseStep, error)
    Create(ctx, s domain.CaseStep) (domain.CaseStep, error)
    Update(ctx, s domain.CaseStep) error
    Delete(ctx, nodeID, stepID int64) error
    ReplaceSeq(ctx, nodeID int64, ordered []int64) error
}
type AssertionRepository interface {
    ListByVersion(ctx, versionID int64) ([]domain.CaseAssertion, error)
    Create/Update/Delete(...)
}
type SourceRepository interface {
    ListByVersion(ctx, versionID int64) ([]domain.CaseSource, error)
    Create/Delete(...)
}
type ReviewRepository interface {
    Create(ctx, r domain.CaseReview) (domain.CaseReview, error)
    GetLatest(ctx, versionID int64) (domain.CaseReview, error)
    GetApproved(ctx, versionID int64) (domain.CaseReview, error)
    Decide(ctx, reviewID int64, status string, comment string, actor int64) (domain.CaseReview, error)
    List(ctx, versionID int64, limit int, cursor string) ([]domain.CaseReview, string, bool, error)
}
type FindingRepository interface {
    CreateBatch(ctx, findings []domain.CaseReviewFinding) error
    List(ctx, versionID int64, status, severity string, nodeID *int64) ([]domain.CaseReviewFinding, error)
    UpdateStatus(ctx, findingID int64, status string, actor int64) (domain.CaseReviewFinding, error)
    DismissOpenAI(ctx, versionID int64) (int, error)
    CountOpenBlockers(ctx, versionID int64) (int, error)
}
type BaselineRepository interface {
    Create(ctx, b domain.CaseBaseline) (domain.CaseBaseline, error)
    GetByVersion(ctx, versionID int64) (domain.CaseBaseline, error)
    DeleteByVersion(ctx, versionID int64) error
}
type RunRepository interface {
    Create(ctx, r domain.CaseDesignRun) (domain.CaseDesignRun, error)
    Get(ctx, versionID, runID int64) (domain.CaseDesignRun, error)
    List(ctx, versionID int64, kind, status string, limit int, cursor string) ([]domain.CaseDesignRun, string, bool, error)
    UpdateStatus(ctx, runID int64, from, to string, patch RunPatch) (domain.CaseDesignRun, error)
    LatestByKind(ctx, versionID int64, kind string) (domain.CaseDesignRun, error)
    ActiveByKind(ctx, versionID int64, kind string) (domain.CaseDesignRun, error)
    RecoverStale(ctx, before time.Time) (int64, error)
}
type ExportRepository interface { Create; Get; List; FindByBaselineFormat(...) }
type GraphRepository interface { InTx(ctx, fn func(GraphTx) error) error } // 单事务物化

type VersionLifecycle interface {
    GetVersion(ctx, versionID int64) (VersionSnapshot, error)
    EnterReview(ctx, versionID, actor int64) (VersionSnapshot, error)
    RejectReview(ctx, versionID, actor int64) (VersionSnapshot, error)
    ApproveBaseline(ctx, versionID, actor int64) (VersionSnapshot, error)
}
type RequirementReader interface {
    ListConfirmed(ctx, versionID int64) ([]RequirementSnapshot, error)
    ListRelations(ctx, versionID int64) ([]RelationSnapshot, error)
}
type CatalogGateway interface {
    GetEnvironment(ctx, envID int64) (EnvironmentSnapshot, error)
    ListAppModels(ctx, systemID int64) ([]AppModelSnapshot, error)
    ImportAppModels(ctx, systemID int64, inputs []AppModelInput) (created, updated int, err error)
}
type SessionGateway interface { GetForExplore(ctx, sessionID int64) (SessionSnapshot, error) }
type CodeFactsGateway interface {
    GetLatestAnalysis(ctx, versionID int64) (ImpactSnapshot, error)
    ListCandidates(ctx, analysisID int64) ([]ImpactCandidate, error)
    FileExists(ctx, versionID int64, path string) (bool, error) // 按版本基线 commit 校验
    ListFacts(ctx, versionID int64, limit int) ([]FactSummary, error)
}
type CodeAnchorGateway interface {
    Create(ctx, versionID, actorID int64, p AnchorParams) (Anchor, error)
    List(ctx, versionID int64, nodeID int64) ([]Anchor, error)
    Delete(ctx, versionID, anchorID int64) error
}
type AgentRuntime interface {
    EnsureProfile(ctx, profile ProfileSeed) error
    CreateSession(ctx, profileKey, title string, actor int64, channel, versionID string) (int64, error)
    StartText(ctx, sessionID int64, text string, actor int64) (int64, error)
    GetSession(ctx, sessionID int64) (AgentSessionStatus, error)
    Abort(ctx, sessionID int64, actor int64) error
}
type EventPublisher interface { Publish(ctx, e Event) error }
type AuditRecorder interface { Record(ctx, e AuditEvent) error }
type IDGenerator interface { Next() int64 }
type Clock interface { Now() time.Time }
```

```ts
// frontend/src/features/mindmap/types.ts（ID 全部 string）
export type MindmapNode = { id: string; parentId: string | null; kind: "scenario" | "case"; title: string; description: string; priority: "P0"|"P1"|"P2"|"P3"; preconditions: string; testData: unknown; source: "user_input"|"ai_generated"|"ai_supplement"; status: string; sortOrder: number; revision: number; steps: CaseStep[]; assertions: CaseAssertion[]; sources: CaseSource[]; anchors: AnchorView[]; openBlockerCount: number }
export type MindmapGraph = { versionId: string; versionStatus: string; editable: boolean; nodes: MindmapNode[]; coverage: Coverage; baseline: BaselineView | null; latestRuns: Record<string, RunSummary>; impactCases: ImpactCase[] }
```

## Wave 1 — 表名规范化（T1–T5，可并行）

> 本 Wave 的唯一目的：按 D34 把 P0–P4 的 39 张表改为单数规范名。**只改表名，不改字段/约束/既有索引名**。每个任务只动本上下文的迁移与 Go 代码；`order.txt` 由 Lead 统一追加（spec §5.3 顺序）。

### Task 1: 改名 P0 基础设施表（identity/platform/eventing/audit/llm/storage）

**并发**：可与 T2/T3/T4/T5 并行；依赖 T0。

**写作用域**：`backend/internal/db/migrations/{identity_0003,platform_0002,eventing_0002,audit_0002,llm_0002,storage_0002}_rename_tables.sql`、`backend/internal/{identity,eventing,audit,llm,storage}/**`、`backend/internal/platform/**`、`backend/internal/db/rename_infra_integration_test.go`

**Files:**
- Create: 上列 6 个迁移、`backend/internal/db/rename_infra_integration_test.go`
- Modify: `identity/infrastructure/postgres/*.go`、`eventing/infrastructure/postgres/store.go`、`audit/infrastructure/postgres/*.go`、`llm/infrastructure/postgres/*.go`、`storage/infrastructure/postgres/*.go`、`platform/idempotency/**` 中的 SQL 表名

**Interfaces:**
- Consumes: 无
- Produces: 新表名 `user_account/auth_token/idempotency_key/event/event_channel/audit_log/llm_config/storage_object`

- [ ] **Step 1: 写失败测试** `backend/internal/db/rename_infra_integration_test.go`

```go
//go:build integration

package db_test

import (
    "context"
    "testing"

    "github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

func TestP5RenameInfraTables(t *testing.T) {
    pool := testutil.OpenTestDB(t) // 自带迁移 + truncate
    ctx := context.Background()
    pairs := [][2]string{
        {"user_account", "users"}, {"auth_token", "auth_tokens"},
        {"idempotency_key", "idempotency_keys"},
        {"event", "events"}, {"event_channel", "event_channels"},
        {"audit_log", "audit_logs"}, {"llm_config", "llm_configs"},
        {"storage_object", "storage_objects"},
    }
    for _, p := range pairs {
        var n int
        if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
            WHERE table_schema='public' AND table_name=$1`, p[0]).Scan(&n); err != nil { t.Fatal(err) }
        if n != 1 { t.Fatalf("new table %s missing", p[0]) }
        if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
            WHERE table_schema='public' AND table_name=$1`, p[1]).Scan(&n); err != nil { t.Fatal(err) }
        if n != 0 { t.Fatalf("old table %s still present", p[1]) }
    }
}
```

- [ ] **Step 2: 跑失败**：`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-integration.ps1 ./internal/db -run TestP5RenameInfraTables`；预期 FAIL（`new table user_account missing`）。

- [ ] **Step 3: 创建 6 个迁移文件**（只含本上下文；示例 identity）：

```sql
-- D34: table naming normalization (plural -> singular). Fields/constraints/indexes unchanged.
ALTER TABLE IF EXISTS users RENAME TO user_account;
ALTER SEQUENCE IF EXISTS users_id_seq RENAME TO user_account_id_seq;
ALTER TABLE IF EXISTS auth_tokens RENAME TO auth_token;
ALTER SEQUENCE IF EXISTS auth_tokens_id_seq RENAME TO auth_token_id_seq;
```

其余 5 个按 spec §3.2 映射生成（platform: `idempotency_keys→idempotency_key` + 序列；eventing: `events→event`、`event_channels→event_channel` + 序列；audit/llm/storage 同理）。**不加** `CREATE INDEX`/`ALTER INDEX`/数据变更。

- [ ] **Step 4: 更新本任务范围内全部 SQL 表名**：`grep -rn "users\|auth_tokens\|idempotency_keys\|event_channels\|audit_logs\|llm_configs\|storage_objects" backend/internal/{identity,eventing,audit,llm,storage,platform}`；逐处改为新名（含 `INSERT/UPDATE/DELETE/ON CONFLICT/FROM`）。`events` 表的跨包引用留给 T6（本任务只改 eventing 包内）。

- [ ] **Step 5: 跑通过**：Step 2 同命令；预期 PASS。

- [ ] **Step 6: 跑本上下文单测**：`cd backend; go test ./internal/identity/... ./internal/eventing/... ./internal/audit/... ./internal/llm/... ./internal/storage/... ./internal/platform/...`；预期全 PASS。

- [ ] **Step 7: 遗留检查**：`grep -rn "FROM users\b\|FROM auth_tokens\|INTO audit_logs\|INTO llm_configs\|INTO storage_objects" backend/internal/{identity,eventing,audit,llm,storage,platform}`；预期 0 命中（注释除外）。**不 commit**（并行实现者不 commit，Lead 统一提交）。

---

### Task 2: 改名 agent 上下文表

**并发**：可与 T1/T3/T4/T5 并行。

**写作用域**：`backend/internal/db/migrations/agent_0003_rename_tables.sql`、`backend/internal/agent/**`、`backend/internal/db/rename_agent_integration_test.go`

**Files:**
- Create: `backend/internal/db/migrations/agent_0003_rename_tables.sql`、`backend/internal/db/rename_agent_integration_test.go`
- Modify: `agent/infrastructure/postgres/*.go`、`agent/application/*.go`（SQL/表名常量）、相关 `*_test.go`

**Interfaces:**
- Produces: `agent_profile/agent_session/agent_run/agent_message`

- [ ] **Step 1: 写失败测试**（同 T1 结构，pairs：`agent_profile←agent_profiles`、`agent_session←agent_sessions`、`agent_run←agent_runs`、`agent_message←agent_messages`）
- [ ] **Step 2: 跑失败**：`... test-integration.ps1 ./internal/db -run TestP5RenameAgentTables`；预期 FAIL。
- [ ] **Step 3: 迁移 + SQL 改名**

```sql
ALTER TABLE IF EXISTS agent_profiles RENAME TO agent_profile;
ALTER SEQUENCE IF EXISTS agent_profiles_id_seq RENAME TO agent_profile_id_seq;
ALTER TABLE IF EXISTS agent_sessions RENAME TO agent_session;
ALTER SEQUENCE IF EXISTS agent_sessions_id_seq RENAME TO agent_session_id_seq;
ALTER TABLE IF EXISTS agent_runs RENAME TO agent_run;
ALTER SEQUENCE IF EXISTS agent_runs_id_seq RENAME TO agent_run_id_seq;
ALTER TABLE IF EXISTS agent_messages RENAME TO agent_message;
ALTER SEQUENCE IF EXISTS agent_messages_id_seq RENAME TO agent_message_id_seq;
```

- [ ] **Step 4: 跑通过** + `cd backend; go test ./internal/agent/...`。
- [ ] **Step 5: 遗留检查**：`grep -rn "agent_profiles\|agent_sessions\|agent_runs\|agent_messages" backend/internal/agent`；预期 0 命中。不 commit。

---

### Task 3: 改名 resource 上下文表

**并发**：可与 T1/T2/T4/T5 并行。

**写作用域**：`backend/internal/db/migrations/resource_0004_rename_tables.sql`、`backend/internal/resource/**`、`backend/internal/db/rename_resource_integration_test.go`

**Files:**
- Create: 迁移 + 集成测试
- Modify: `resource/infrastructure/**`、`resource/application/**`（SQL/表名）、`resource` 相关测试

**Interfaces:**
- Produces: `company/system/environment/app_model_node/account/account_system_access/session/session_snapshot/session_lease/manual_takeover/code_repository`

- [ ] **Step 1: 写失败测试**（pairs 11 组，按 spec §3.2）
- [ ] **Step 2: 跑失败**：`... ./internal/db -run TestP5RenameResourceTables`。
- [ ] **Step 3: 迁移**：对 11 张表 `ALTER TABLE IF EXISTS <old> RENAME TO <new>;` + 对应 `ALTER SEQUENCE IF EXISTS <old>_id_seq RENAME TO <new>_id_seq;`（如某表用 identity 列而非序列，则跳过该序列语句）。**不重命名索引/约束**。
- [ ] **Step 4: 更新 resource 包内 SQL 全部表名**（含 `app_model_nodes`、`code_repositories` 等）并跑 `go test ./internal/resource/...`。
- [ ] **Step 5: 跑集成测试通过**；`grep -rn "app_model_nodes\|code_repositories\|account_system_accesses\|session_snapshots\|session_leases\|manual_takeovers" backend/internal/resource` 预期 0。不 commit。

---

### Task 4: 改名 version 上下文表

**并发**：可与 T1/T2/T3/T5 并行。

**写作用域**：`backend/internal/db/migrations/version_0003_rename_tables.sql`、`backend/internal/version/**`、`backend/internal/db/rename_version_integration_test.go`

**Files:** Create 迁移 + 测试；Modify `version/infrastructure/postgres/*.go` 及测试

**Interfaces:** Produces `version/requirement/requirement_relation/requirement_attachment/clarification_session/clarification_question/decision`

- [ ] **Step 1: 写失败测试**（pairs 7 组）
- [ ] **Step 2: 跑失败**
- [ ] **Step 3: 迁移 + 改名**（`versions→version` 等 7 组 + 序列）
- [ ] **Step 4: 更新 version 包内 SQL**；`go test ./internal/version/...` 全 PASS。
- [ ] **Step 5: 遗留检查**：`grep -rn "FROM versions\|FROM requirements\|requirement_relations\|requirement_attachments\|clarification_sessions\|clarification_questions\|FROM decisions" backend/internal/version` 预期 0。不 commit。

---

### Task 5: 改名 codeintel 上下文表

**并发**：可与 T1/T2/T3/T4 并行。

**写作用域**：`backend/internal/db/migrations/codeintel_0003_rename_tables.sql`、`backend/internal/codeintel/**`、`backend/internal/db/rename_codeintel_integration_test.go`

**Files:** Create 迁移 + 测试；Modify `codeintel/infrastructure/postgres/*.go` 及测试

**Interfaces:** Produces `code_change_set/code_index_run/code_entity/code_edge/code_fact_version/code_analysis/code_impact_candidate/code_anchor`（`code_analysis_cache` 不变）

- [ ] **Step 1: 写失败测试**（pairs 8 组 + 断言 `code_analysis_cache` 仍存在）
- [ ] **Step 2: 跑失败**
- [ ] **Step 3: 迁移 + 改名**（8 组 + 序列；`code_analyses→code_analysis`）
- [ ] **Step 4: 更新 codeintel 包内 SQL**；`go test ./internal/codeintel/...` 全 PASS。
- [ ] **Step 5: 遗留检查**：`grep -rn "code_analyses\|code_anchors\|code_entities\|code_edges\|code_fact_versions\|code_change_sets\|code_index_runs\|code_impact_candidates" backend/internal/codeintel` 预期 0。不 commit。

## Wave 2 — 共享收尾 + P5 地基（T6 ∥ T7）

### Task 6: 共享引用收尾 + 命名规约落全局约定

**并发**：与 T7 并行；依赖 T1–T5（需要全仓 grep 结果）。

**写作用域**：`backend/internal/testutil/**`、`backend/cmd/**`、`scripts/**`、`Agent.md`、`docs/design/system-blueprint.md`（仅 §3.2 D34、§10.2 表名）、`backend/internal/db/rename_shared_integration_test.go`、`backend/internal/db/migrations/order.txt`（仅验证，Lead 写入）

**Files:**
- Modify: `scripts/acceptance.ps1`、`scripts/acceptance-codeintel-llm.ps1`、`scripts/fixture-*.ps1`（如含 psql 表名）、`backend/internal/testutil/db.go`（如硬编码表名）、`cmd/eztest/main.go`（如硬编码）
- Create: `backend/internal/db/rename_shared_integration_test.go`
- Docs: `Agent.md`（新增 §6.9、§6.3 引用、§6.1 预留 mindmap 条目）、`docs/design/system-blueprint.md`（§3.2 追加 D34、§10.2 规范名）

**Interfaces:** Consumes T1–T5 新表名；Produces 全仓无旧表名引用、`Agent.md §6.9` 规约、蓝图 D34。

- [ ] **Step 1: 全仓扫描**：`grep -rn "FROM users\b\|INTO users\b\|agent_profiles\|app_model_nodes\|FROM versions\b\|code_analyses" backend scripts`；排除历史迁移（`*_0001/0002*.sql`）与历史规格；逐条修正真实 SQL。
- [ ] **Step 2: 写集成测试** `TestP5RenameNoOldTables`：断言 39 个旧名均不存在（用完整列表，防遗漏）。
- [ ] **Step 3: 跑失败→修正→跑通过**（`... ./internal/db`）。
- [ ] **Step 4: 更新 `Agent.md`**：新增 §6.9「数据库命名规约（D34）」逐条写入 spec §3.1 的 8 条；§6.3 增"表名/索引命名遵循 §6.9"。
- [ ] **Step 5: 更新主蓝图**：§10.2 表名改规范名 + 一行说明；§3.2 追加 D34 原文（spec §13.1）。
- [ ] **Step 6: 全量门禁**：`powershell -File scripts/test.ps1`；预期全 PASS。
- [ ] **Step 7: 提交**（Lead）：`refactor: rename tables to singular per D34 and add naming convention`。

---

### Task 7: mindmap 领域模型 + 3 条迁移 + ports + 提示词常量

**并发**：与 T6 并行；依赖 T1–T5。

**写作用域**：`backend/internal/mindmap/domain/**`、`backend/internal/mindmap/application/ports.go`、`backend/internal/db/migrations/{resource_0005,codeintel_0004,mindmap_0001}_*.sql`、`backend/internal/db/mindmap_migration_integration_test.go`

**Files:**
- Create: `mindmap/domain/{node.go,step.go,assertion.go,source.go,review.go,baseline.go,run.go,export.go,graph.go,coverage.go,errors.go,prompts.go,profile.go}`、`mindmap/application/ports.go`、3 个迁移、`backend/internal/db/mindmap_migration_integration_test.go`
- Test: `mindmap/domain/{graph_test.go,coverage_test.go,prompts_test.go,profile_test.go}`

**Interfaces:**
- Consumes: `platform/id`（仅类型）
- Produces: Shared Interfaces 的全部 `domain` 常量/结构/`ValidateGraph`/`CanonicalGraphHash`/`BuildCoverage`/`Error`；`application/ports.go` 全部接口与值类型；`domain.P5Profiles() []AgentProfileSeed`；`PromptVersionDesign/Review/DataPlan/Explore` 常量

- [ ] **Step 1: 写领域测试** `graph_test.go`：

```go
func TestValidateGraphRejectsCaseWithChildren(t *testing.T)
func TestValidateGraphRejectsNonContiguousSteps(t *testing.T)
func TestValidateGraphRequiresStepsAndAssertions(t *testing.T)
func TestValidateGraphRejectsScenarioWithSteps(t *testing.T)
func TestValidateGraphRejectsStepAssertionFromOtherCase(t *testing.T)
func TestCanonicalGraphHashStableAcrossInputOrder(t *testing.T)
func TestCanonicalGraphHashChangesWithContent(t *testing.T)
func TestBuildCoverageMarksUncoveredAndStale(t *testing.T)
```

- [ ] **Step 2: 跑失败**：`cd backend; go test ./internal/mindmap/domain`；预期编译失败。
- [ ] **Step 3: 实现 domain**（`ValidateGraph` 返回 `*Error{Kind:"invalid", Reason:"invalid_case_tree"}`；`CanonicalGraphHash` 用固定字段的 `json.Marshal` + `sha256`）。
- [ ] **Step 4: 跑通过**：`go test ./internal/mindmap/domain`。
- [ ] **Step 5: 写迁移**：`mindmap_0001_cases.sql` 用 spec §5.2 原文；`resource_0005`/`codeintel_0004` 先查实际约束名再 DROP+ADD（见下）：

```sql
-- 先确认：SELECT conname FROM pg_constraint WHERE conrelid='app_model_node'::regclass;
ALTER TABLE app_model_node DROP CONSTRAINT IF EXISTS app_model_nodes_source_check;
ALTER TABLE app_model_node ADD CONSTRAINT ck_app_model_node_source
  CHECK (source IN ('manual','import','explored'));
ALTER TABLE code_anchor DROP CONSTRAINT IF EXISTS code_anchors_subject_kind_check;
ALTER TABLE code_anchor ADD CONSTRAINT ck_code_anchor_subject_kind
  CHECK (subject_kind IN ('version','requirement','app_model','case'));
```

- [ ] **Step 6: 迁移集成测试**：9 表存在 + 三处 CHECK 接受/拒绝断言；跑通过。
- [ ] **Step 7: `ports.go`** 一次性定义 Shared Interfaces 全部接口与值类型；`go build ./internal/mindmap/...` 通过。
- [ ] **Step 8: `prompts.go` + `profile.go`**：4 段系统提示词（含 spec §8.1 必须要素）与 4 个 seed（key/AllowedTools/预算按 spec §8.1）；`prompts_test.go`/`profile_test.go` 断言关键约束与工具名单。跑 PASS。
- [ ] **Step 9: 提交**（Lead）`feat: add P5 mindmap domain and migrations`。

---

## Wave 3 — 仓储 ∥ 图服务 ∥ 版本状态 ∥ 跨阶段扩展（T8 ∥ T9 ∥ T17 ∥ T18）

### Task 8: PostgreSQL 仓储（9 表）

**并发**：可与 T9/T17/T18 并行；依赖 T7。

**写作用域**：`backend/internal/mindmap/infrastructure/postgres/**`、`backend/internal/mindmap/integration/repository_test.go`

**Files:**
- Create: `postgres/{node_repository.go,step_repository.go,assertion_repository.go,source_repository.go,review_repository.go,finding_repository.go,baseline_repository.go,run_repository.go,export_repository.go,tx.go}`
- Test: `mindmap/integration/repository_test.go`（`//go:build integration`）

**Interfaces:**
- Consumes: T7 domain/ports
- Produces: `NewNodeRepository(pool) application.NodeRepository` 等 9 个构造函数 + `NewGraphTx(pool) GraphTx`

- [ ] **Step 1: 写集成测试**：

```go
func TestNodeRepositoryOCC(t *testing.T)
func TestActiveRunUnique(t *testing.T)
func TestApprovedReviewUnique(t *testing.T)
func TestCascadeDeleteScenario(t *testing.T)
func TestCaseSourceUniqueRef(t *testing.T)
func TestExportReuseLookup(t *testing.T)
func TestRunRecoverStale(t *testing.T)
```

- [ ] **Step 2: 跑失败**：`... ./internal/mindmap/integration -run TestNodeRepositoryOCC`。
- [ ] **Step 3: 实现**（pgx、显式列、`RETURNING`、唯一冲突映射 `domain.Error`；`Update` 用 `WHERE id=$1 AND revision=$2`）。
- [ ] **Step 4: 跑通过**：`... ./internal/mindmap/integration`。
- [ ] **Step 5: 提交** `feat: add P5 mindmap postgres repositories`。

---

### Task 9: 图应用服务（人工 CRUD/OCC/树校验/覆盖）

**并发**：可与 T8/T17/T18 并行；依赖 T7。

**写作用域**：`backend/internal/mindmap/application/{graph_service.go,graph_service_test.go}`

**Files:** Create 两文件（fake 仓储/端口写在测试文件内）

**Interfaces:**
- Consumes: T7 ports + domain；`EventPublisher/IDGenerator/Clock`；`VersionLifecycle.GetVersion`；`RequirementReader.ListConfirmed/ListRelations`
- Produces: `NewGraphService(deps GraphDeps) *GraphService`；`Get/CreateNode/UpdateNode/DeleteNode/AddStep/UpdateStep/DeleteStep/ReorderSteps/AddAssertion/UpdateAssertion/DeleteAssertion/AddSource/DeleteSource/Reorder`

- [ ] **Step 1: 写单测**：

```go
func TestCreateCaseRequiresConfirmedRequirement(t *testing.T)
func TestUpdateNodeStaleRevisionConflict(t *testing.T)
func TestDeleteLastRequirementSourceRejected(t *testing.T)
func TestDeleteLastAssertionRejected(t *testing.T)
func TestScenarioCannotHaveSteps(t *testing.T)
func TestFrozenVersionRejectsWrites(t *testing.T)
func TestReorderRequiresCompleteChildSet(t *testing.T)
```

- [ ] **Step 2: 跑失败**；**Step 3: 实现**（每个写操作：GetVersion 判状态 → 领域校验 → 仓储 → 事件；`editable` 由状态+baseline 推导）。
- [ ] **Step 4: 跑通过** `go test ./internal/mindmap/application -run TestGraph -run TestCreate`（按实际名前缀）。
- [ ] **Step 5: 提交** `feat: add P5 mindmap graph service`。

---

### Task 17: version 生命周期方法

**并发**：可与 T8/T9/T18 并行；依赖 T4。

**写作用域**：`backend/internal/version/application/{version_service.go,version_service_test.go}`、`backend/internal/version/domain/**`

**Files:** Modify `version_service.go`（追加 3 方法）、`version_service_test.go`；按需 `version/domain/version.go`

**Interfaces:**
- Consumes: `repo.UpdateStatus(ctx,id,from,to,actor)` CAS、既有 `publish`
- Produces: `EnterReview/RejectReview/ApproveBaseline(ctx,id,actor) (*Version,error)`；`version.status_changed` 事件；错误 `NewStateConflict("version_state_conflict", ...)`

- [ ] **Step 1: 写测试**（fake repo）：三个合法流转 + 事件；非法 from 409 reason；`EnterReview` 已 reviewing 幂等且不重复发事件。
- [ ] **Step 2: 跑失败**；**Step 3: 实现**（CAS 命中 0 行时再读状态：已是目标状态→幂等成功；否则 state conflict）。
- [ ] **Step 4: 跑通过** `go test ./internal/version/...`；**Step 5: 提交** `feat: add P5 version review lifecycle`。

---

### Task 18: resource 应用模型来源 + codeintel 锚点主体扩展

**并发**：可与 T8/T9/T17 并行；依赖 T3/T5。

**写作用域**：`backend/internal/resource/{domain,application,infrastructure}/**`、`backend/internal/codeintel/{domain,application,infrastructure}/**`

**Files:**
- Modify: `resource/application/catalog_service.go`（`AppModelInput.Source`、默认 `import`、校验三值）、`resource/domain/appmodel.go`（导出 `AppModelSourceExplored`）、测试
- Modify: `codeintel/domain/anchor.go`（`SubjectCase="case"`、`ValidateSubjectKind`）、测试

**Interfaces:**
- Produces: `AppModelInput.Source string`、`AppModelSourceExplored="explored"`、`codeintel.SubjectCase="case"`

- [ ] **Step 1: 写测试**：默认 import、显式 explored、非法 source 报错；锚点 case 通过、未知主体 422 `unsupported_subject_kind`。
- [ ] **Step 2: 跑失败**；**Step 3: 实现**；**Step 4: 跑通过** `go test ./internal/resource/... ./internal/codeintel/...`；**Step 5: 提交** `feat: extend app model source and case anchors`。

## Wave 4 — 物化/运行 ∥ 评审基线 ∥ 导出 ∥ 探索 ∥ 前端数据层（T10 ∥ T11 ∥ T12 ∥ T13 ∥ T20）

### Task 10: run 协调 + 结构化提交物化（design/review/data_plan 共用）

**并发**：可与 T11/T12/T13/T20 并行；依赖 T8/T9。

**写作用域**：`backend/internal/mindmap/application/{run_service.go,materialize.go,run_service_test.go,materialize_test.go}`

**Files:** Create 四文件（fake ports 在测试内）

**Interfaces:**
- Consumes: T8 仓储、T9 GraphService（复用树/来源校验）、`AgentRuntime`、`VersionLifecycle.EnterReview`、`RequirementReader`、`CodeFactsGateway.FileExists`、`CodeAnchorGateway`、`EventPublisher`、`IDGenerator`、`Clock`
- Produces: `NewRunService(RunDeps) *RunService`；`Start(ctx,versionID,actor,StartRunCommand) (CaseDesignRun,error)`；`ProcessOnce(ctx) error`；`Worker(ctx)`/`Stop()`；`Materialize(ctx,runID) error`；`RecoverStale(ctx) (int64,error)`

- [ ] **Step 1: 写单测**：

```go
func TestDesignProposalMaterializesInOneTx(t *testing.T)
func TestInvalidProposalWritesNothing(t *testing.T)     // 重复 ref / 未确认需求 / 越界 stepIndex
func TestAnchorMissingFileDroppedNotFail(t *testing.T)  // stats.anchorsDropped=1, run=done
func TestMaterializeIsIdempotent(t *testing.T)
func TestRunSingleFlight(t *testing.T)
func TestRunFailedWhenAgentAborts(t *testing.T)
func TestStaleRunRecovered(t *testing.T)
func TestReviewProposalDismissesPreviousAIFindings(t *testing.T)
func TestDataPlanSkipsHumanEditedNodes(t *testing.T)     // nodeRevision 不匹配 -> skipped，不覆盖
func TestInstructionIncludesConfirmedRequirementsAndFacts(t *testing.T) // instruction 文本含需求 id/关系/应用模型 key/代码事实摘要，且 ≤64 KiB
```

- [ ] **Step 2: 跑失败**；**Step 3: 实现**（Start：状态/确认需求校验→建 run(queued)→CreateSession→StartText→running，失败补偿；ProcessOnce：取 running→GetSession→按 LastRun.Status 终态；Materialize 走 `GraphRepository.InTx`；校验失败 `stats.validationErrors` ≤20）。
- [ ] **Step 4: 跑通过** `go test ./internal/mindmap/application -run 'Test(Design|Invalid|Anchor|Materialize|Run|Stale|ReviewProposal|DataPlan)'`。
- [ ] **Step 5: 提交** `feat: add P5 run coordinator and proposal materialization`。

---

### Task 11: 评审 + 基线冻结（含补偿）

**并发**：可与 T10/T12/T13/T20 并行；依赖 T8/T9。

**写作用域**：`backend/internal/mindmap/application/{review_service.go,baseline_service.go,review_service_test.go,baseline_service_test.go}`

**Files:** Create 四文件

**Interfaces:**
- Consumes: T8 `Review/Finding/Baseline/Node` 仓储、`VersionLifecycle`、`EventPublisher`、`Clock`、`IDGenerator`
- Produces: `NewReviewService/NewBaselineService`；`Approve(ctx,versionID,actor,ReviewCommand) (ApproveResult,error)`；`Reject(...)`；`List/ListFindings/UpdateFinding`；`Freeze(...)`

- [ ] **Step 1: 写单测**：

```go
func TestApproveBlockedByOpenBlocker(t *testing.T)
func TestApproveWithAcknowledgeSucceeds(t *testing.T)
func TestApproveCompensatesWhenVersionConflicts(t *testing.T) // 断言 baseline 已删、节点 status 回滚、返回 conflict
func TestSecondApproveRejected(t *testing.T)
func TestRejectReturnsToDesigning(t *testing.T)
func TestFreezeRequiresNoBaseline(t *testing.T)
```

- [ ] **Step 2: 跑失败**；**Step 3: 实现**（顺序：读版本/基线 → 校验 blocker → 计算 hash → InTx 写 baseline + 节点 reviewed → ApproveBaseline → 失败补偿事务；事件 `case.baseline_frozen`、`gate.resolved`）。
- [ ] **Step 4: 跑通过**；**Step 5: 提交** `feat: add P5 review and baseline freeze`。

---

### Task 12: 导出服务（Markdown/Mermaid/OPML）+ 文件存储

**并发**：可与 T10/T11/T13/T20 并行；依赖 T8/T9。

**写作用域**：`backend/internal/mindmap/application/{export_service.go,export_service_test.go}`、`backend/internal/mindmap/infrastructure/filestore/**`

**Files:** Create export service + `filestore/store.go` + tests

**Interfaces:**
- Consumes: T8 仓储、T9 `Get`（图/覆盖）、`BaselineRepository`
- Produces: `NewExportService(ExportDeps) *ExportService`；`Create(ctx,versionID,actor,format) (CaseExport,error)`；`List`；`Download(ctx,versionID,exportID) (io.ReadCloser,CaseExport,error)`；`filestore.New(root) (*Store,error)`、`Write/Open`

- [ ] **Step 1: 写单测**：Markdown 含用例/步骤/断言/覆盖矩阵；Mermaid 以 `mindmap` 开头且转义；OPML `xml.Unmarshal` 层级与 `_note`；复用同 baseline+format；路径穿越拒绝；sha256 与内容一致。
- [ ] **Step 2: 跑失败**；**Step 3: 实现**（spec §6.10 结构；文件路径 `versions/{id}/(baseline-{bid}|draft)/{format}-{exportID}.{ext}`；`filestore` 做根内校验/临时文件/rename/0600）。
- [ ] **Step 4: 跑通过**；**Step 5: 提交** `feat: add P5 mindmap exports`。

---

### Task 13: 探索服务（HTTP 只读 BFS + SSRF 防护）

**并发**：可与 T10/T11/T12/T20 并行；依赖 T7。

**写作用域**：`backend/internal/mindmap/application/{explore_service.go,explore_service_test.go}`、`backend/internal/mindmap/infrastructure/httpclient/**`

**Files:** Create explore service + httpclient + tests（`httptest`）

**Interfaces:**
- Consumes: `CatalogGateway`、`SessionGateway`、`EventPublisher`、`Clock/IDGenerator`
- Produces: `NewExploreService(deps) *ExploreService`；`Start(ctx,versionID,actor,ExploreCommand) (CaseDesignRun,error)`；`Run(ctx,run) (ExploreResult,error)`；`httpclient.New(Options) *Client`、`Fetch(...)`

- [ ] **Step 1: 写单测**：白名单/深度/请求上限/超大 body/循环链接/登出链接跳过/跨域重定向拒绝/私网拒绝/`source=explored` 导入/无 LLM 降级。
- [ ] **Step 2: 跑失败**；**Step 3: 实现**（仅 GET/HEAD；`net.ParseIP` 私网判定；解析用现有依赖或标准库；限额常量来自 env；`stats.truncated`）。
- [ ] **Step 4: 跑通过** `go test ./internal/mindmap/...`；**Step 5: 提交** `feat: add P5 HTTP-only app exploration`。

---

### Task 20: 前端数据层（api/types/hooks/error 文案）

**并发**：可与 T10–T13 并行；依赖 T7（契约）。

**写作用域**：`frontend/src/features/mindmap/{api.ts,types.ts,useMindmap.ts,error-messages.ts,*.test.ts}`

**Files:** Create 四源文件 + 两测试

**Interfaces:**
- Consumes: `src/lib/api-client.ts`、`src/lib/sse-client.ts`、`features/versions/useVersionStream.ts` 的既有钩子
- Produces: `mindmapApi`（27 端点薄封装）；`types.ts`（Shared Interfaces TS 契约）；`useMindmap(versionId)` 返回 `{graph,loading,error,streamStatus,refresh,applyGraph}`；`describeMindmapError(err)` 覆盖 spec §7.4 全部 reason

- [ ] **Step 1: 写测试** `error-messages.test.ts`（每个 reason 有中文文案、未知 reason 兜底）+ `useMindmap.test.ts`（mock api-client：加载成功/失败、SSE 事件触发 refresh 节流、reset 全量刷新）。
- [ ] **Step 2: 跑失败**：`cd frontend; pnpm vitest run src/features/mindmap`。
- [ ] **Step 3: 实现**（ID 保持 string；`apiStreamFetch` 用于 download；SSE 事件类型过滤 `case.*/version.status_changed/gate.*`；500ms 节流）。
- [ ] **Step 4: 跑通过**；**Step 5: 提交** `feat: add P5 mindmap frontend data layer`。

## Wave 5 — 适配器 ∥ Agent 工具 ∥ 画布 ∥ fake/fixture（T14 ∥ T15 ∥ T21 ∥ T25）

### Task 14: 跨上下文适配器

**并发**：可与 T15/T21/T25 并行；依赖 T7/T8/T18。

**写作用域**：`backend/internal/mindmap/infrastructure/{version,resource,codeintel,agent,eventing,audit}/**`

**Files:** Create `version/lifecycle.go`、`version/requirement_reader.go`、`resource/catalog.go`、`resource/session.go`、`codeintel/facts.go`、`codeintel/anchors.go`、`agent/runtime.go`、`eventing/publisher.go`、`audit/recorder.go` + 各 `*_test.go`

**Interfaces:**
- Consumes: 对方 application 接口（构造参数注入，不 import infrastructure）
- Produces: `NewLifecycleAdapter/NewRequirementReader/NewCatalogAdapter/NewSessionAdapter/NewCodeFactsAdapter/NewAnchorAdapter/NewAgentRuntimeAdapter/NewEventPublisher/NewAuditRecorder`

- [ ] **Step 1: 写测试**：ID 字符串↔int64 转换、错误映射、`SessionDetail.Running/LastRun.Status` 映射、事件 `channel=version:{id}` + detail 上限、`FileExists` 按版本基线 commit。
- [ ] **Step 2: 跑失败**；**Step 3: 实现**（`AppModelInput.Source="explored"`；`GetVersion` 返回 `VersionSnapshot{ID,Status,Name,CodeBaselineCommit,SystemID,EnvironmentID...}`）。
- [ ] **Step 4: 跑通过** `go test ./internal/mindmap/infrastructure/...`；**Step 5: 提交** `feat: add P5 cross-context adapters`。

---

### Task 15: Agent 工具 + 4 个 profile seed

**并发**：可与 T14/T21/T25 并行；依赖 T7/T9/T10/T11/T13。

**写作用域**：`backend/internal/mindmap/infrastructure/agenttools/**`

**Files:** Create `agenttools/{common.go,graph_read.go,graph_propose.go,review_submit.go,data_plan_submit.go,app_model_propose.go,explore_http.go}` + `agenttools_test.go`

**Interfaces:**
- Consumes: `agent/domain.Tool` 接口、T10 `RunService`/`RunRepository`、T13 `httpclient`、`CatalogGateway`
- Produces: `NewTools(deps ToolDeps) []domain.Tool`（6 个工具名与 schema 见 spec §8.2）；工具内从 P1 工具调用上下文解析 `actor/sessionID`，按 sessionID 找 running `case_design_run`

- [ ] **Step 1: 写测试**：proposal 超 1 MiB → `proposal_too_large`；无活动 run → `run_not_found`；findings >200 → `invalid_arguments`；`explore.http` 非白名单 → `explore_not_allowed`；6 个工具 `InputSchema()` 均合法 JSON 且 `additionalProperties:false`；工具名与 `P5Profiles()` allowlist 一致。
- [ ] **Step 2: 跑失败**；**Step 3: 实现**；**Step 4: 跑通过** `go test ./internal/mindmap/infrastructure/agenttools`；**Step 5: 提交** `feat: add P5 agent tools`。

---

### Task 16: HTTP API + DTO + 路由 + 组装根

**并发**：W7；依赖 T8–T15、T17。

**写作用域**：`backend/internal/mindmap/interfaces/httpapi/**`、`backend/internal/server/server.go`、`backend/cmd/eztest/main.go`

**Files:** Create `httpapi/{routes.go,handlers_graph.go,handlers_run.go,handlers_review.go,handlers_export.go,handlers_meta.go,dto.go,errors.go}` + `httpapi_test.go`；Modify `server/server.go`、`cmd/eztest/main.go`

**Interfaces:**
- Consumes: T9–T15 服务
- Produces: 27 端点（spec §7.2）；`Dependencies.Mindmap`；启动 seed 4 profile + 注册 6 工具 + worker 启停

- [ ] **Step 1: 写 handler 测试**（httptest + fake services）：401/404/403（explore 非 admin）/409 reason/202/201/200 复用/download `Content-Disposition`/JSON ID 为字符串。
- [ ] **Step 2: 跑失败**；**Step 3: 实现**（`platform/errors.WriteError` + `WithDetails`；DTO 全字符串；路由与 P3/P4 不冲突）。
- [ ] **Step 4: 组装根**：构造 mindmap 依赖图；`ensureP5Profiles`（校验 AllowedTools 后 `EnsureBuiltin`）；worker 用根 context 启停；`main` 关闭顺序正确。
- [ ] **Step 5: 跑通过**：`go build ./... && go test ./internal/mindmap/... ./internal/server/...`。
- [ ] **Step 6: 提交** `feat: add P5 HTTP API and wiring`。

---

### Task 19: OpenAPI 契约

**并发**：W8；依赖 T16（路由/DTO 定型）。

**写作用域**：`docs/openapi/api.yaml`

**Files:** Modify `docs/openapi/api.yaml`（paths/schemas/枚举）

**Interfaces:** Consumes T16 的 27 端点与 DTO；Produces OpenAPI 3.0.3 契约 + `VersionEventType` 8 个新值 + `MindmapErrorReason` 枚举。

- [ ] **Step 1: 写静态断言脚本**（先不接入 acceptance）：用 `node` + `yaml` 解析 `api.yaml`，校验 27 个 path、8 个事件、全部 reason 存在（`scripts/acceptance.ps1` 的 P5 区块在 T27 引用同一份清单）。
- [ ] **Step 2: 跑失败**；**Step 3: 编辑 `api.yaml`**：paths（27）、schemas（`MindmapGraph`、`CaseNodeView`、`CaseStepView`、`CaseAssertionView`、`CaseSourceView`、`CaseReviewView`、`CaseFindingView`、`CaseRunView`、`CaseExportView`、`MindmapEnvelope`、`MindmapErrorReason`）、`VersionEventType` 追加 8 值、错误响应引用。
- [ ] **Step 4: 跑通过**；**Step 5: 提交** `docs: add P5 OpenAPI contract`。

---

## Wave 5–7 — 前端画布/编辑器/评审/页面 ∥ fake/fixture ∥ E2E（T21 ∥ T25 → T22 → T23 ∥ T24 → T26）

### Task 21: 画布布局 + SVG 组件

**并发**：可与 T14/T15/T25 并行；依赖 T20。

**写作用域**：`frontend/src/features/mindmap/{mindmap-layout.ts,MindmapCanvas.tsx,mindmap-layout.test.ts,MindmapCanvas.test.tsx}`

**Files:** Create 四文件

**Interfaces:**
- Consumes: T20 `types.ts`
- Produces: `layoutMindmap(nodes, opts): LayoutResult`（坐标/边/折叠/边界）；`MindmapCanvas({graph,selectedId,onSelect,collapsed,onToggle})`（`role="tree"`、`data-testid` 见 spec §9.3）

- [ ] **Step 1: 写测试**：空图/单节点/场景嵌套/200 节点确定性坐标/折叠隐藏子树；画布渲染测试断言节点数、`aria-expanded`、键盘选择。
- [ ] **Step 2: 跑失败**；**Step 3: 实现**（tidy-tree：按深度 y、按兄弟顺序 x；SVG transform 缩放平移；只渲染可见节点）。
- [ ] **Step 4: 跑通过**；**Step 5: 提交** `feat: add P5 mindmap canvas`。

---

### Task 22: 用例/场景编辑器 + 来源/锚点面板

**并发**：W7；依赖 T21。

**写作用域**：`frontend/src/features/mindmap/{CaseEditor.tsx,ScenarioEditor.tsx,SourcePanel.tsx,SourcePanel.test.tsx,CaseEditor.test.tsx}`

**Files:** Create 五文件

**Interfaces:**
- Consumes: T20 `mindmapApi`、`describeMindmapError`
- Produces: `CaseEditor({node,editable,onSave,onAddStep,...})`；`SourcePanel({node,editable,onAddSource,onAddAnchor,onDeleteAnchor})`

- [ ] **Step 1: 写测试**：OCC 409 显示刷新横幅并保留草稿；最后一条断言不可删（按钮禁用 + 提示）；testData JSON 非法不提交；只读态禁用全部输入。
- [ ] **Step 2: 跑失败**；**Step 3: 实现**（受控表单，服务端权威；上下移调用 order 接口）。
- [ ] **Step 4: 跑通过**；**Step 5: 提交** `feat: add P5 mindmap editors`。

---

### Task 23: 评审/运行/导出面板 + PNG

**并发**：W7；依赖 T21/T22。

**写作用域**：`frontend/src/features/mindmap/{ReviewPanel.tsx,RunPanel.tsx,ExportMenu.tsx,png-export.ts,ReviewPanel.test.tsx,png-export.test.ts}`

**Files:** Create 六文件

**Interfaces:**
- Consumes: T20 `mindmapApi`、`useMindmap`
- Produces: `ReviewPanel({versionId,graph,onChanged})`；`RunPanel({versionId,graph,onRun})`；`ExportMenu({versionId,svgRef})`；`svgToPngBlob(svgEl): Promise<Blob>`

- [ ] **Step 1: 写测试**：blocker 未勾选批准按钮禁用；驳回必填评论；PNG 导出 helper（mock canvas/`toBlob`）文件名正确；导出菜单三格式调用对应 API。
- [ ] **Step 2: 跑失败**；**Step 3: 实现**（`apiStreamFetch` → blob 下载；PNG 用 `XMLSerializer` + `Image` + canvas）。
- [ ] **Step 4: 跑通过**；**Step 5: 提交** `feat: add P5 review, run and export panels`。

---

### Task 24: 页面组装 + 路由 + 入口 + 全幅壳

**并发**：W8；依赖 T20–T23。

**写作用域**：`frontend/src/features/mindmap/{MindmapPage.tsx,MindmapPage.test.tsx}`、`frontend/src/app/(app)/versions/[id]/mindmap/page.tsx`、`frontend/src/features/versions/{VersionHeader.tsx,VersionTabs.tsx,OverviewTab.tsx}`（仅加入口）、`frontend/src/navigation/nav-items.ts`、`frontend/src/features/shell/AppShell.tsx`

**Files:** Create 页面 + 路由；Modify 入口 5 文件

**Interfaces:**
- Consumes: T20–T23 全部
- Produces: `/versions/[id]/mindmap` 可访问；`VersionHeader` "用例脑图" 按钮（`data-testid="open-mindmap"`）；`nav-items` 指向 `/versions`；`AppShell` 对 mindmap 路由免 `max-w-6xl`

- [ ] **Step 1: 写测试**：页面渲染空态双入口；只读态 banner；入口按钮导航 URL。
- [ ] **Step 2: 跑失败**；**Step 3: 实现**（`MindmapPage` 组装工具栏/画布/右栏 + `useMindmap` + 选中态）。
- [ ] **Step 4: 跑通过** `pnpm vitest run src/features/mindmap` + `pnpm typecheck`。
- [ ] **Step 5: 提交** `feat: add P5 mindmap page and entry points`。

---

### Task 25: fake LLM P5 模式 + fixture 页面 + E2E helpers

**并发**：可与 T21 并行（W6 前段即可开始）；依赖 T7（提示词标记字符串）。

**写作用域**：`scripts/fake-llm.cjs`、`scripts/fixture-target.cjs`、`frontend/e2e/p5-helpers.ts`

**Files:** Modify 两个 cjs；Create `p5-helpers.ts`

**Interfaces:**
- Consumes: P5 提示词常量（T7）中的中文角色标记；P1 工具名下划线映射
- Produces: `detectP5Scenario`；`EZTEST_FAKE_LLM_P5_MODE`；fixture `/p5/*` 路由；`p5-helpers` 导出 `seedMindmapVersion/startDesignRun/waitDesignRun/getMindmap/approveMindmap/exportMindmap/readCaseEvents/skipMindmapSpecWhenHarnessMissing`

- [ ] **Step 1: 写自测** `scripts/fake-llm.p5.test.cjs`（node 直接跑）：4 种系统提示词 → 期望工具调用序列；下划线工具名；invalid 模式返回非法 proposal；degraded 模式返回空。
- [ ] **Step 2: 跑失败**（`node scripts/fake-llm.p5.test.cjs`）；**Step 3: 实现** fake 分支（在 P4 分支之后、`recordRequest` 之前；`run.step` 计数；P4 行为不变）。
- [ ] **Step 4: fixture 页面**：新增 `/p5/home`（3 链接+1 表单）、`/p5/orders`、`/p5/orders/{id}`、`/p5/admin`（cookie 守卫）、`/api/orders`、`/api/orders/{id}`；不破坏既有路由（既有 E2E 仍绿）。
- [ ] **Step 5: 跑通过**（node test + `scripts/e2e.ps1 sse` 快速回归）；**Step 6: 提交** `test: add P5 fake LLM scenarios and fixtures`。
- [ ] **Step 7: `p5-helpers.ts`**：封装 seed（建系统/环境/应用模型/代码仓/版本/2 已确认需求）、start/wait design、getMindmap、approve、export、readCaseEvents、skip 守卫；写完后 `pnpm typecheck` 通过（无测试则随 T26 使用）。

---

### Task 26: E2E `mindmap.spec.ts`

**并发**：W8；依赖 T24/T25。

**写作用域**：`frontend/e2e/{mindmap.spec.ts,p5-helpers.ts}`

**Files:** Create spec；Modify p5-helpers（如需）

**Interfaces:** Consumes T25 helpers、T24 页面、T16 API

- [ ] **Step 1: 写 spec**（单个 `test()`、`test.setTimeout(300_000)`、serial；步骤严格按 spec §10.5 的 1–8，含下载与事件 `expect.poll`）。
- [ ] **Step 2: 跑失败**：`powershell -File scripts/e2e.ps1 mindmap`；预期 FAIL（功能未通/选择器缺失）。
- [ ] **Step 3: 修实现或选择器**（仅修 P5 scope；不得改 fake 断言绕过）。
- [ ] **Step 4: 跑通过**：同命令；保存失败截图/trace 到 `.tmp/e2e-artifacts/playwright`。
- [ ] **Step 5: 提交** `test: add P5 mindmap E2E`。

## Wave 8–9 — 验收脚本 ∥ 文档同步（T27 ∥ T28）

### Task 27: 验收脚本 + README + env

**并发**：可与 T28 并行；依赖 T16/T19/T24/T26。

**写作用域**：`scripts/acceptance.ps1`、`scripts/acceptance-mindmap-llm.ps1`、`.env.example`、`README.md`、`docs/dev-setup.local.md`（如需）

**Files:**
- Modify: `scripts/acceptance.ps1`（P4 区块之后追加 P5 区块、SKIPPED 行、DoD 映射表、Synopsis/PARAMETER）
- Create: `scripts/acceptance-mindmap-llm.ps1`（复制 P4 骨架的辅助函数）
- Modify: `.env.example`（5 个新键）、`README.md`（P5 runbook）

**Interfaces:** Consumes 全部实现；Produces `acceptance.ps1` P5 12 个 check + gated 真实 LLM 脚本 16 个 check + runbook。

- [ ] **Step 1: 写 `acceptance.ps1` P5 静态检查**（先失败）：27 path、8 事件、全部 reason、13 迁移存在、`order.txt` 追加、README/env 键。
- [ ] **Step 2: 跑失败**：`powershell -File scripts/acceptance.ps1 -StaticOnly`；预期 P5 check FAIL。
- [ ] **Step 3: 实现静态 + API 检查**：迁移改名断言（旧名不存在/新名存在）、API smoke（401/404/409/字符串 ID）、人工路径（建/改/审/批/导出/只读 409）、事件快照、force 复用、权限、降级。
- [ ] **Step 4: 写 `acceptance-mindmap-llm.ps1`**：门禁 `EZTEST_RUN_REAL_LLM=1`（缺失 key = 失败）、独立库、健康、seed、真实 LLM 设计/评审/数据规划/探索、编辑/驳回/批准/导出三格式内容断言、SSE 事件、降级、幂等/并发；`-DryRun` 可静态跑通。
- [ ] **Step 5: `.env.example` + README**：新增 `EZTEST_EXPORT_DIR`、`EZTEST_MINDMAP_MAX_CASES`、`EZTEST_MINDMAP_WORKER_INTERVAL_MS`、`EZTEST_EXPLORE_MAX_REQUESTS`、`EZTEST_EXPLORE_TIMEOUT_MS`；README 增 P5 章节（迁移改名、导出、探索白名单、E2E/验收命令、已知限制）。
- [ ] **Step 6: 跑通过**：`scripts/acceptance.ps1 -StaticOnly` → PASS；`scripts/acceptance.ps1`（无真实 LLM）→ PASS。
- [ ] **Step 7: 提交** `test: add P5 acceptance scripts and runbook`。

---

### Task 28: 文档同步（D35–D38、前端设计、实施注记）+ 全量门禁

**并发**：可与 T27 并行；依赖 T16/T19（契约定型）。

**写作用域**：`docs/design/system-blueprint.md`（§5.1/§5.2/§5.4/§5.5/§11/§12/§17.4）、`docs/design/frontend-design.md`、`docs/design/implementation-phases.md`（仅 §2.9 注记）、`Agent.md`（§6.1 目录树 mindmap 条目）

**Files:** Modify 4 份文档

**Interfaces:** Consumes T19 OpenAPI、T7–T16 实现；Produces 设计文档与代码一致、`Agent.md §6.1` 含 mindmap。

- [ ] **Step 1: 主蓝图**：§3.2 追加 D35–D38 原文（spec §13.1）；§5.2 增补 CaseGraph 细化（树/来源/状态）；§5.4 锚点增 `case`；§5.5 CaseNode 状态注记 P5 已实现 draft/reviewed；§10.2 已由 T6 改名，补 P5 9 表；§11.1 增 mindmap 路径、§11.2 增 8 事件；§12.2 PG4 交付说明；§17.4 标记"已由 D38 关闭"。
- [ ] **Step 2: `Agent.md`**：§6.1 树加 `mindmap` 四层与表说明；§6.5 上下文接入要求已覆盖。
- [ ] **Step 3: `frontend-design.md`**：§3 表 PG4 状态改"P5 已交付"；§4.2 入口；§5.3 评审流；组件表补 `MindmapCanvas/ReviewPanel/ExportMenu`。
- [ ] **Step 4: `implementation-phases.md` §2.9**：#15/#21 备注"P5 已交付"（不改范围）。
- [ ] **Step 5: 全量门禁**：`powershell -File scripts/test.ps1` + `powershell -File scripts/e2e.ps1` + `powershell -File scripts/acceptance.ps1`；预期全 PASS（记录耗时与失败修复）。
- [ ] **Step 6: 提交** `docs: sync P5 design decisions and runbook`。

---

## Wave 10 — 真实 LLM 验收 + 真实人工验收 + 合回（T29，Lead 执行）

### Task 29: 真实 LLM 验收、真实人工验收、合并

**并发**：串行；依赖 T1–T28 全部完成。

**写作用域**：`.superpowers/sdd/2026-10-28-eztest-p5-mindmap/**`（gitignored 证据）、`scripts/acceptance-mindmap-llm.ps1`（仅缺陷修复）、代码缺陷修复（按需，严格限定）

**Files:** Create `.superpowers/sdd/2026-10-28-eztest-p5-mindmap/{progress.md,acceptance.md}`（gitignored）；按需修复代码

**Interfaces:** Consumes 全部；Produces 验收结论 + 证据索引 + 合回 `dev`。

- [ ] **Step 1: 真实 LLM 脚本验收**：`$env:EZTEST_RUN_REAL_LLM="1"; powershell -File scripts/acceptance-mindmap-llm.ps1`；记录每个 check 的 PASS/FAIL、耗时、LLM 统计（token/耗时若可得）；FAIL → 修 → 重跑关联 check。
- [ ] **Step 2: 真实人工验收（我代用户）**：按 spec §10.10 的 8 步在浏览器执行；截屏 ≥10 张到 `.tmp/p5-acceptance/`；保存 Markdown/OPML/Mermaid/PNG 导出物；记录 DB 关键行（`psql`）与事件快照；普通用户与降级演练各一次。
- [ ] **Step 3: 验收判定**：逐条对照 spec §10.9 DoD 表；任何一条无证据 = 不通过；修复后重跑该条及关联回归。
- [ ] **Step 4: 写 `acceptance.md`**（gitignored）：Date/Repo/Branch/HEAD/Scope/Status + 门禁表（命令、结果、耗时）+ DoD 表（证据链接/截图名/文件路径）+ 已知限制 + 缺陷记录。
- [ ] **Step 5: 分支收尾**：`git status` 干净；`git switch dev; git merge --ff-only feat/p5-mindmap-review`；`git log --oneline -1`；不 push。
- [ ] **Step 6: 向用户汇报**：通过/不通过、证据索引、遗留风险（XMind 不交付/探索无浏览器/生成质量归 P8 等）。

---

## Self-Review（写完计划后已执行）

**1. Spec coverage**
- §1 目标 1/2/3：T7/T9/T10/T11；4/5：T13/T18；6：T11/T16；7：T18/T19/前端；8：T12/T23；9：T16/T20；10：T10/T13/§8.5；11：T1–T6。
- §2.3 D34–D38：D34→T1–T6；D35→T7–T17；D36→T13/T18；D37→T18；D38→T12/T23。
- §3 改名映射/迁移：T1–T6；§5 DDL：T7/T8；§6 规则：T9–T13（每条不变量在任务测试中列出）；§7 API：T16/T19；§8 Agent：T7/T10/T14/T15；§9 前端：T20–T24；§10 测试验收：T26/T27/T29；§11 风险：Review Focus + 各任务负例；§13 文档：T6/T28；§14 不做：Global Constraints。
- 缺口检查：spec §8.3 上下文组装 → 未单列任务；已并入 T10（Start 构造 instruction 的测试断言含需求/关系/应用模型/代码事实摘要）+ T15（graph_read 摘要）。**已补**：T10 Step 1 增加 `TestInstructionIncludesConfirmedRequirementsAndFacts`。

**2. Step scan**
- 每个 Step 是单一动作 + 可验证结果；无"TBD/适当处理/补测试"类空话。
- 删除：初稿中"预建迁移占位"（无决定价值），已删；"代码块全文"仅保留测试与迁移/关键 DDL，其余给签名。

**3. Type consistency**
- `CaseNode/CaseStep/CaseAssertion/CaseSource/CaseReview/CaseReviewFinding/CaseBaseline/CaseDesignRun/CaseExport` 在 T7 定义、T8 仓储、T10/T11/T12 使用，命名一致。
- `Start/ProcessOnce/Materialize/RecoverStale/Approve/Reject/Create/Download/Get` 在 Interfaces Produces 中固定；前端 `mindmapApi` 方法名与 T16 路由一一对应（T20 引用路由表，T19 静态断言数量 27）。
- `github.com/EdouardRichard/EzTest/backend` 为测试导入路径；`testutil.OpenTestDB` 已确认会自动迁移 + truncate。

**4. Review Focus**
- 改名遗漏 → T1–T6（旧名不存在断言 + 全仓 grep）。
- 冻结竞态 → T11 `TestApproveCompensatesWhenVersionConflicts`。
- AI 不可信 → T10 `TestInvalidProposalWritesNothing`、T15 超限测试、T25 invalid 模式 + T27 check 7。
- OCC/子集合 → T8 `TestNodeRepositoryOCC`、T9 三个删除/重排测试、T10 data_plan skip。
- SSRF/导出路径 → T13 六个负例、T12 路径穿越/哈希、T27 check 8。

**5. Proportion**
- 计划约为 spec 的 1.2–1.5 倍；代码块仅测试/DDL/迁移/契约片段，正文以签名+验收命令为主，符合比例要求。

