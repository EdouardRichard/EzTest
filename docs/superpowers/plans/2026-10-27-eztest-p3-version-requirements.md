# EzTest P3 Version / Requirements / Clarification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付「版本 / 多需求 / AI 引导澄清 / 人工确认」垂直切片：后端 `version` 上下文（7 表、雪花 ID、附件文件系统）、P1 运行时兼容扩展、3 个业务 Agent、版本工作台与实时 SSE，达到 P3 退出标准。

**Architecture:** 新增单个 `version` 限界上下文（DDD 四层），跨上下文只经 application 端口；P1 增加 `activity_channel`、`StartWithParts` + `image_ref`/`ImageResolver`、工具调用上下文与 3 个 profile；附件只落文件系统（D30），不碰 P0 对象存储；前端新增 `/versions` 列表与 `/versions/[id]` 详情容器/工作台，复用 P0 请求层/SSE 客户端。

**Tech Stack:** Go 1.24.6（标准库 `net/http`、pgx/v5）、PostgreSQL 17.6、Next.js 16 / React 19 / TypeScript / Tailwind / Radix、Vitest、Playwright、Node fixture（无新依赖）。

**Spec:** [docs/superpowers/specs/2026-10-27-eztest-p3-version-requirements-design.md](../specs/2026-10-27-eztest-p3-version-requirements-design.md)

## Global Constraints

- 范围严格按 spec；**不实现** P4 代码智能、P5 脑图、P6 执行、P7 问题/报告/`continuing`，不删除/归档版本。
- **D29**：P3 起新增表主键统一雪花 ID（64-bit，41+10+12），应用层生成；DDL 禁止 `BIGSERIAL/SERIAL/自增`；JSON/工具参数/URL 中 64-bit ID 一律十进制字符串；节点 `EZTEST_SNOWFLAKE_NODE_ID`（0–1023，默认 1）。
- **D30**：业务附件/截图/导出只落文件系统；禁止二进制/base64/data URL 写 PG；**不复用 P0 `storage` 对象存储**；附件根 `EZTEST_VERSION_ATTACHMENT_DIR`（默认 `.data/version-attachments`），临时文件 + 原子 rename、文件 0600/目录 0700、根内校验、拒绝 `..`/绝对路径/symlink 逃逸。
- **D31**：Version 是粗粒度容器；新增/确认/重开需求**永不**改变/回退 Version.status；P3 只实现 `draft→clarifying→designing` 与 Requirement `draft→clarified`；designing 下仍可独立澄清/确认；进入设计允许带未澄清需求但需 `acknowledgeUnclarified`。
- 单个 `version` 上下文四层齐全；跨上下文只依赖 application 接口或事件，禁止导入 P1/P2 的 infrastructure/interfaces。
- API 前缀 `/api/versions`；全部登录用户可读写；未登录 401；错误复用 `platform/errors` + `details.reason`；409 用于状态/并发冲突。
- 7 表固定：`versions / requirements / requirement_relations / requirement_attachments / clarification_sessions / clarification_questions / decisions`；迁移 `version_0001_versions_requirements.sql`、`version_0002_clarification.sql`、`agent_0002_activity_channel.sql`，追加 `order.txt`，只增不改。
- 澄清 SSE 固定 `channel=version:{id}`；`agent.activity` 经 `agent_sessions.activity_channel` 进入同一通道；事件先持久化后广播；20s 心跳。
- 截图以 `image_ref`（`attachment:<id>`）注入；`agent_messages` 只存引用；data URL 只允许在 LLM 调用内存中由 `ImageResolver` 瞬态生成，不落库/不写日志。
- 不新增 Go/前端依赖；不引入 React Flow/表单库/状态库；所有 HTTP 走 `api-client`，SSE 走 `sse-client`。
- 每个任务：先写失败测试 → 跑失败 → 最小实现 → 跑通过 → 提交；提交前缀 `feat/fix/test/docs/chore`。
- 不提交 `.env`、`.tools/`、`.tmp/`、`.data/`、`.superpowers/sdd/`、`*.local.md`；响应/日志不得出现附件内容、data URL、凭据原文。
- 分支 `feat/p3-version-requirements`；并行实现者不 commit，Lead 只 stage 该任务写作用域并提交；验收后本地 ff 合回 `dev`，不 push。
- 端口沿用 Web `6868`、API `13333`、PostgreSQL `5433`、fixture `13335`。

### 命令约定

- Go：`cd backend; go test ./internal/version/...`（本机可用 `..\.tools\go\bin\go.exe`）。
- 单元/集成：`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-integration.ps1 ./internal/version/...`。
- 前端：`cd frontend; pnpm vitest run src/features/versions`、`pnpm lint`、`pnpm typecheck`。
- 全量门禁：`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1`。
- E2E：`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1`。
- 完整验收：`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1`。

## Concurrency Map（实现时并行执行的唯一依据）

| Wave | Tasks | 并行规则 |
|---|---|---|
| W0 | T1 ∥ T2 ∥ T3 ∥ T5 | 文档 / 雪花+配置 / 迁移+领域 / 前端基础，写作用域互不重叠 |
| W1 | T4 ∥ T6 ∥ T8 | Agent 通道 / Version 仓储+服务 / 前端列表与容器；三条独立轨 |
| W2 | T7 ∥ T9 ∥ T17 | Agent 图片引用 / Requirement 仓储+服务 / 前端工作台 |
| W3 | T12 ∥ T10 → T11 ∥ T18 | Agent 工具上下文+profile / 后端澄清→附件串行链 / 前端澄清面板 |
| W4 | T13 ∥ T14 | P3 适配器+工具 / HTTP handlers v1（不同包） |
| W5 | T15 ∥ T20 | HTTP handlers v2+SSE / OpenAPI+acceptance+README |
| W6 | T16 | 组装根 + server 装配 + 集成（依赖全部后端） |
| W7 | T19 | fake LLM + E2E（依赖 T16 + 前端） |
| W8 | T21 | 全量门禁 + 人工验收 + 评审 + 合回 dev |

- 同一 Wave 的任务派发给独立 subagent；`**写作用域**` 是该 subagent 唯一允许修改的路径前缀。
- **同包串行**：T6→T9→T10→T11 同属 `version/application` + `version/infrastructure/postgres`；T4→T7→T12 同属 `agent/application`；T14→T15 同属 `version/interfaces/httpapi`；T8→T17→T18 同属前端 `features/versions`。这些链按表中顺序执行，不并发。
- 并行实现者**不 commit**；Lead 只 stage 该任务写作用域后提交，保证 per-task review range。
- 跨 Wave 依赖未完成不得启动；接口以各任务 `Interfaces → Produces` 为准。
- T3 统一创建三个迁移（含 `agent_0002`）并追加 `order.txt`，避免并行写 `order.txt` 冲突；T4 只改 agent 代码与测试。

## Review Focus（最容易被忽略、必须有测试钉住的 5 类输入）

1. **雪花 ID 精度与序列化**：所有 P3 自有 ID 在 JSON/URL/工具参数/前端均为字符串；`Number()` 转换、事件 refs、`before_id` 与 `next_cursor` 混用——必须不丢精度、不出现 number/string 混型。
2. **Version 状态容器语义**：designing 下新增/确认/重开需求不改变 Version.status；进入设计 warnings + `acknowledgeUnclarified`；不存在回退动作；`paused/continuing` 不可达。
3. **待澄清门禁**：需求确认要求自身 open questions=0；版本级问题只警告；回答/驳回/重开状态迁移；接受 proposed 决策同事务关闭关联问题。
4. **附件文件系统安全**：`..`/绝对路径/symlink/超大/错误 MIME/重复 sha/数量上限；目录 0700/文件 0600；删除 best-effort；`image_ref` 不把 data URL 落 PG。
5. **AI 降级与并发**：LLM 不可用时会话/消息/分析/侦察 503 但人工路径全可用；单版本单 active 会话/单运行 Agent；SSE 断线重连/`stream.reset`/刷新不丢消息。

---

## Pre-flight（一次性，不算任务）

- [ ] 确认在 `dev` 且工作区干净：`git status --short`。
- [ ] 创建分支：`git switch -c feat/p3-version-requirements`（含 spec/D29/D30 提交）。
- [ ] 启动数据库：`powershell -File scripts/db.ps1 status`，必要时 `start`。
- [ ] 确认 `frontend/node_modules` 存在；否则 `cd frontend; pnpm install`。
- [ ] 确认基线绿：`powershell -File scripts/test.ps1`。
- [ ] 确认 `.env` 含 `EZTEST_TEST_DATABASE_URL`（指向 `eztest_test`）与可写 `.data/`。
- [ ] 确认端口 `13333/6868/13335` 空闲。

---

## Shared Interfaces（由 T2/T3 定义；后续任务消费）

```go
// backend/internal/version/domain
const (
    VersionDraft = "draft"; VersionClarifying = "clarifying"; VersionDesigning = "designing"
    VersionReviewing = "reviewing"; VersionExecuting = "executing"; VersionContinuing = "continuing"
    VersionCompleted = "completed"; VersionArchived = "archived"

    RequirementDraft = "draft"; RequirementClarified = "clarified"
    RequirementDesigned = "designed"; RequirementVerified = "verified"; RequirementObsolete = "obsolete"

    RelationDependsOn = "depends_on"; RelationConflictsWith = "conflicts_with"
    RelationOverlaps = "overlaps"; RelationSharesScope = "shares_scope"

    PriorityMust = "must"; PriorityShould = "should"; PriorityCould = "could"
    SourceUser = "user"; SourceAI = "ai"

    SessionActive = "active"; SessionCompleted = "completed"; SessionCancelled = "cancelled"
    QuestionOpen = "open"; QuestionAnswered = "answered"; QuestionDismissed = "dismissed"
    DecisionProposed = "proposed"; DecisionAccepted = "accepted"; DecisionRejected = "rejected"
    AttachmentText = "text"; AttachmentImage = "image"
)

type Version struct {
    ID int64; Name, Label, Description, Status string
    SystemID, EnvironmentID, CodeRepositoryID *int64
    CodeBranch, CodeBaselineCommit *string
    Revision int; CreatedBy, UpdatedBy int64
    CreatedAt, UpdatedAt time.Time
}
type VersionFilter struct { Status string; Page Page }
type QuestionFilter struct { VersionID int64; Status string; RequirementID *int64; Page Page }
type DecisionFilter struct { VersionID int64; Status string; Page Page }
type StartRunResult struct { SessionID, AgentSessionID, RunID int64; Status string }
type VersionBindings struct { SystemID, EnvironmentID, CodeRepositoryID *int64; CodeBranch, CodeBaselineCommit *string }
type VersionResource struct { ID int64; Name string; Available bool }
type VersionBindingsView struct { System, Environment, CodeRepository *VersionResource }
type VersionCounts struct { RequirementCount, ClarifiedCount, OpenQuestionCount int }
type Warnings struct { UnclarifiedRequirementIDs []int64; OpenQuestionCount int }

type Requirement struct {
    ID, VersionID int64; Title, Description, AcceptanceCriteria, Priority, Status string
    Source, LastUpdateSource string; Revision int
    ConfirmedAt *time.Time; ConfirmedBy *int64
    CreatedBy, UpdatedBy int64; CreatedAt, UpdatedAt time.Time
}
type Relation struct {
    ID, VersionID, FromRequirementID, ToRequirementID int64
    RelationType, Description, Source string
    CreatedBy int64; CreatedAt, UpdatedAt time.Time
}
type ClarificationSession struct {
    ID, VersionID int64; Status string; AgentSessionID *int64
    ContextSummary string; SummaryUpdatedAt *time.Time
    StartedBy int64; StartedAt time.Time; EndedAt *time.Time; EndedReason string
}
type Question struct {
    ID, VersionID int64; RequirementID, SessionID *int64
    Question, Status, Answer, Source, AnsweredVia string
    AnsweredBy *int64; AnsweredAt *time.Time
    CreatedBy int64; CreatedAt, UpdatedAt time.Time
}
type Decision struct {
    ID, VersionID int64; RequirementID, QuestionID *int64
    Topic, Decision, Rationale, Status, Source string
    ProposedBy int64; DecidedBy *int64; DecidedAt *time.Time
    CreatedAt, UpdatedAt time.Time
}
type Attachment struct {
    ID, VersionID, RequirementID int64; Kind, Filename, MIMEType string
    SizeBytes int64; SHA256, StoragePath string; CreatedBy int64; CreatedAt time.Time
}
type Page struct { Limit int; Cursor string }
```

```go
// Repository ports (implemented in version/infrastructure/postgres; app tests use fakes)
type VersionRepository interface {
    Create(ctx context.Context, v *Version) error
    Get(ctx context.Context, id int64) (*Version, error)
    List(ctx context.Context, f VersionFilter) ([]Version, bool, error)
    Counts(ctx context.Context, versionID int64) (VersionCounts, error)
    UpdateMetadata(ctx context.Context, id int64, expected int, name, label, desc string, actor int64) (*Version, error)
    UpdateBindings(ctx context.Context, id int64, expected int, b VersionBindings, actor int64) (*Version, error)
    UpdateStatus(ctx context.Context, id int64, from, to string, actor int64) (*Version, error)
    ListUnclarified(ctx context.Context, versionID int64) ([]int64, int, error)
}
type RequirementRepository interface {
    Create(ctx context.Context, r *Requirement) error
    Get(ctx context.Context, id int64) (*Requirement, error)
    List(ctx context.Context, versionID int64, status string, p Page) ([]Requirement, bool, error)
    Update(ctx context.Context, r *Requirement, expected int) error
    Delete(ctx context.Context, id int64) error
    UpdateStatus(ctx context.Context, id int64, from, to string, actor int64) (*Requirement, error)
    CountOpenQuestions(ctx context.Context, requirementID int64) (int, error)
    ListRelations(ctx context.Context, versionID int64) ([]Relation, error)
    CreateRelation(ctx context.Context, r *Relation) error
    UpdateRelation(ctx context.Context, id int64, relType, desc string) (*Relation, error)
    DeleteRelation(ctx context.Context, id int64) error
    CreateAttachment(ctx context.Context, a *Attachment) error
    ListAttachments(ctx context.Context, requirementID int64) ([]Attachment, error)
    GetAttachment(ctx context.Context, id int64) (*Attachment, error)
    DeleteAttachment(ctx context.Context, id int64) error
    CountAttachments(ctx context.Context, requirementID, versionID int64) (int, int, error)
}
type ClarificationRepository interface {
    CreateSession(ctx context.Context, s *ClarificationSession) error
    GetActiveSession(ctx context.Context, versionID int64) (*ClarificationSession, error)
    GetSession(ctx context.Context, id int64) (*ClarificationSession, error)
    CompleteSession(ctx context.Context, id int64, actor int64) (*ClarificationSession, error)
    UpdateContextSummary(ctx context.Context, versionID int64, summary string, actor int64) error
    CreateQuestion(ctx context.Context, q *Question) error
    GetQuestion(ctx context.Context, id int64) (*Question, error)
    ListQuestions(ctx context.Context, f QuestionFilter) ([]Question, bool, error)
    UpdateQuestion(ctx context.Context, q *Question, fromStatus string) error
    CreateDecision(ctx context.Context, d *Decision) error
    GetDecision(ctx context.Context, id int64) (*Decision, error)
    ListDecisions(ctx context.Context, f DecisionFilter) ([]Decision, bool, error)
    UpdateDecision(ctx context.Context, d *Decision, fromStatus string) error
}

// Cross-context ports consumed by application (implemented in T13)
type ResourceCatalog interface {
    GetSystem(ctx context.Context, id int64) (ResourceRef, error)
    GetEnvironment(ctx context.Context, id int64) (ResourceRef, error)
    GetCodeRepository(ctx context.Context, id int64) (CodeRepoRef, error)
}
type ResourceRef struct { ID int64; Name, Status string; Available bool }
type CodeRepoRef struct { ID int64; Name, Status, LocalPath, FixedBranch string; IncludeGlobs, ExcludeGlobs []string; LastCommit string; Available bool }
type GitReader interface {
    ResolveCommit(ctx context.Context, repoPath, branch string) (string, error)
    Tree(ctx context.Context, repoPath, commit string, include, exclude []string, max int) ([]CodeEntry, error)
    Read(ctx context.Context, repoPath, commit, path string, maxBytes int) (string, error)
}
type CodeEntry struct { Path, Type string; Size int64 }
type VersionEvent struct { VersionID int64; Type, Summary string; RefRequirementID, RefSessionID, RefDecisionID, RefAttachmentID *int64; ActorID int64; Source string; Detail map[string]any }
type EventPublisher interface { Publish(ctx context.Context, e VersionEvent) error }
```

```go
// P3 application services
func NewVersionService(repo VersionRepository, resources ResourceCatalog, git GitReader, events EventPublisher) *VersionService
func (s *VersionService) Create(ctx context.Context, cmd CreateVersionCommand, actor int64) (*Version, error)
func (s *VersionService) EnterDesign(ctx context.Context, id int64, ack bool, actor int64) (*Version, Warnings, error)
func (s *VersionService) StartClarification(ctx context.Context, id int64, actor int64) (*Version, error)

func NewRequirementService(repo RequirementRepository, versions VersionRepository, events EventPublisher) *RequirementService
func (s *RequirementService) Confirm(ctx context.Context, versionID, reqID int64, actor int64) (*Requirement, error)
func (s *RequirementService) Reopen(ctx context.Context, versionID, reqID int64, actor int64) (*Requirement, error)

func NewClarificationService(repo ClarificationRepository, requirements RequirementRepository, versions VersionRepository, agents AgentSessions, events EventPublisher) *ClarificationService
func (s *ClarificationService) StartSession(ctx context.Context, versionID int64, actor int64) (*ClarificationSession, error)
func (s *ClarificationService) SendMessage(ctx context.Context, versionID int64, message string, actor int64) (StartRunResult, error)

// Agent ports (P3 application)
type AgentSessions interface {
    Create(ctx context.Context, profileKey, title string, actor int64, channel, versionID string) (int64, error)
    StartText(ctx context.Context, agentSessionID int64, text string, actor int64) (int64, error)
    StartParts(ctx context.Context, agentSessionID int64, parts []AgentPart, actor int64) (int64, error)
    Messages(ctx context.Context, agentSessionID int64, beforeID int64, limit int) ([]AgentMessage, bool, error)
    HasRunningByChannel(ctx context.Context, channel string) (bool, error)
}
type AgentPart struct { Type, Text, ImageRef string }
type AgentMessage struct { ID, RunID int64; Role string; Parts []AgentPart; Reasoning string; ToolCalls json.RawMessage; ToolName string; CreatedAt time.Time }
```

```ts
// frontend/src/features/versions/types.ts (all P3-owned IDs are string)
type VersionStatus = "draft"|"clarifying"|"designing"|"reviewing"|"executing"|"continuing"|"completed"|"archived";
type RequirementStatus = "draft"|"clarified"|"designed"|"verified"|"obsolete";
type Version = { id: string; name: string; label: string; description: string; status: VersionStatus;
  systemId?: number; environmentId?: number; codeRepositoryId?: number;
  codeBranch?: string; codeBaselineCommit?: string; bindings?: VersionBindingsView;
  requirementCount: number; clarifiedCount: number; openQuestionCount: number;
  revision: number; createdBy: number; updatedBy: number; createdAt: string; updatedAt: string };
type Requirement = { id: string; versionId: string; title: string; description: string;
  acceptanceCriteria: string; priority: "must"|"should"|"could"; status: RequirementStatus;
  source: "user"|"ai"; lastUpdateSource: "user"|"ai"; revision: number;
  openQuestionCount: number; relationCount: number; attachmentCount: number;
  confirmedAt?: string; confirmedBy?: number; createdBy: number; updatedBy: number;
  createdAt: string; updatedAt: string };
```

---

### Task 1: D31 与 P3 文档同步

**Wave:** W0 · **并发:** 可并发（文档轨） · **依赖:** 无
**写作用域:** `docs/design/`, `Agent.md`

**Files:**
- Modify: `docs/design/system-blueprint.md`（§3.2 D31、§5.6、§10.2、§11.1、§11.2、§12.2）
- Modify: `Agent.md`（§6.1 目录树新增 `version/` 四层与 `platform/id/snowflake.go`）
- Modify: `docs/design/implementation-phases.md`（§2-P3 范围补充）
- Modify: `docs/design/frontend-design.md`（§4.2/§4.4/§9）

**Interfaces:**
- Consumes: spec §13.1 D31 原文、§13.2 同步表。
- Produces: 后续任务可引用的 D31 与目录约定。

- [ ] **Step 1: 追加 D31 并修订 §5.6**

按 spec §13.1 原文追加 D31；§5.6 增加备注：Version 是粗粒度容器、需求操作不回退、designing 下可独立澄清/确认、新增需求不中断执行（P7）。

- [ ] **Step 2: 同步 §10.2/§11.1/§11.2/§12.2**

表组追加 `clarification_questions`/`requirement_attachments`；API 追加 `/api/versions` 集合/动作/绑定/事件/流；事件追加 P3 类型；P3 Tab 为 9 项（工作台 + D18 8 Tab）。

- [ ] **Step 3: 更新 Agent.md §6.1 与 implementation-phases/frontend-design**

`version/` 四层、`platform/id/snowflake.go`；P3 范围补附件/多模态、资源绑定、D31、问题/决策；frontend-design 写 P3 Tab/默认工作台/占位行为。

- [ ] **Step 4: 验证并提交**

Run: `Select-String -Path docs/design/system-blueprint.md -Pattern "D31"`；`Select-String -Path Agent.md -Pattern "internal/version"`

Expected: 均至少 1 处命中。

```bash
git add docs/design/system-blueprint.md docs/design/implementation-phases.md docs/design/frontend-design.md Agent.md
git commit -m "docs: record D31 P3 version requirements lifecycle"
```

---

### Task 2: 雪花 ID 生成器 + 配置 + 环境变量

**Wave:** W0 · **并发:** 可并发（平台轨） · **依赖:** 无
**写作用域:** `backend/internal/platform/id/`, `backend/internal/config/`, `.env.example`, `deploy/docker-compose.yml`

**Files:**
- Create: `backend/internal/platform/id/snowflake.go`
- Test: `backend/internal/platform/id/snowflake_test.go`
- Modify: `backend/internal/config/config.go:23-53,71-88`（新增 `VersionAttachmentDir`、`SnowflakeNodeID`）
- Test: `backend/internal/config/config_test.go`
- Modify: `.env.example`、`deploy/docker-compose.yml`

**Interfaces:**
- Produces:

```go
func NewSnowflake(nodeID int64) (*Snowflake, error) // node 0..1023
func (s *Snowflake) Next() int64                    // 41bit ms since 2024-01-01 UTC + 10bit node + 12bit seq
// config.Config += VersionAttachmentDir string; SnowflakeNodeID int64
// config.Load: EZTEST_VERSION_ATTACHMENT_DIR default ".data/version-attachments";
//             EZTEST_SNOWFLAKE_NODE_ID default 1, range 0..1023
```

- [ ] **Step 1: 写失败测试**

`snowflake_test.go`：`TestSnowflakeMonotonicAndUnique`（10k 次严格递增、无重复）、`TestSnowflakeRejectsInvalidNode`（-1、1024）、`TestSnowflakeClockRollback`（注入固定 clock，回拨后仍不重复）、`TestSnowflakeConcurrentUnique`（8 goroutine × 1000）。
`config_test.go`：`TestLoadP3Defaults`（默认 `.data/version-attachments` / node 1）、`TestLoadSnowflakeNodeBounds`（-1/1024 报错）。

- [ ] **Step 2: 跑失败**

Run: `cd backend; go test ./internal/platform/id ./internal/config -run 'Snowflake|P3Defaults' -v`
Expected: FAIL（`NewSnowflake` undefined / 字段不存在）

- [ ] **Step 3: 实现**

`Snowflake` 用 `sync.Mutex`；epoch `2024-01-01T00:00:00Z`；时钟回拨时等待 `lastMs`（或复用 lastMs+自增序列），序列溢出等到下一毫秒；`Next()` 返回 int64。config 解析并校验 node 范围，附件目录在 `main` 侧 `MkdirAll 0700`（T16）。

- [ ] **Step 4: 跑通过**

Run: `cd backend; go test ./internal/platform/id ./internal/config -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/platform/id backend/internal/config .env.example deploy/docker-compose.yml
git commit -m "feat: add snowflake id generator and P3 config"
```

---

### Task 3: 迁移 + version 领域模型/仓储端口

**Wave:** W0 · **并发:** 可并发（后端基础轨） · **依赖:** 无（T2 的 ID 类型为 int64，不阻塞）
**写作用域:** `backend/internal/db/migrations/`, `backend/internal/version/domain/`, `backend/internal/version/integration/`

**Files:**
- Create: `backend/internal/db/migrations/version_0001_versions_requirements.sql`
- Create: `backend/internal/db/migrations/version_0002_clarification.sql`
- Create: `backend/internal/db/migrations/agent_0002_activity_channel.sql`
- Modify: `backend/internal/db/migrations/order.txt`（追加 3 行）
- Create: `backend/internal/version/domain/{version,requirement,relation,clarification,decision,attachment,errors,repository,limits}.go`
- Create: `backend/internal/version/domain/{version,requirement,relation,clarification}_test.go`
- Create: `backend/internal/version/integration/migrate_integration_test.go`

**Interfaces:**
- Produces: `Shared Interfaces` 中全部 domain 类型/常量/仓储端口；`domain.NewInvalid/NewNotFound/NewConflict/NewStateConflict/NewDependency`。
- Consumes: spec §5 完整 DDL、§6 规则。

- [ ] **Step 1: 写失败测试**

`migrate_integration_test.go`：`TestP3MigrationsApply`（执行迁移两次无副作用；7 表存在；`uq_clarification_active`/`uq_relations_undirected` 存在）。
`version_test.go`：`TestVersionValidate`（name 空/超长、label/description 上限、commit 必须 40 hex、环境必须有系统）；`TestRequirementValidate`（title/priority/status 枚举）；`TestRelationValidate`（自环、类型枚举）。

- [ ] **Step 2: 跑失败**

Run: `powershell -File scripts/test-integration.ps1 ./internal/version/integration -run TestP3MigrationsApply -v`
Expected: FAIL（迁移文件不存在）

- [ ] **Step 3: 实现迁移与 domain**

按 spec §5.1/§5.2/§5.3 原文写 DDL：P3 主键 `id BIGINT PRIMARY KEY`（无 default），附件 `storage_path`；`agent_0002` 增加 `activity_channel/activity_version_id` + 部分索引。domain 实现枚举常量、结构体、`Validate()`、分页/筛选类型、仓储端口、错误构造器、limits 常量（spec §4.1）。

- [ ] **Step 4: 跑通过**

Run: `powershell -File scripts/test-integration.ps1 ./internal/version/integration -run TestP3MigrationsApply -v`；`cd backend; go test ./internal/version/domain -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/db/migrations backend/internal/version/domain backend/internal/version/integration
git commit -m "feat: add P3 migrations and version domain"
```

---

### Task 4: P1 Agent 通道扩展（activity_channel + running-by-channel）

**Wave:** W1 · **并发:** 可并发（Agent 轨） · **依赖:** T3（`agent_0002`）
**写作用域:** `backend/internal/agent/domain/session.go`, `backend/internal/agent/application/{session_service,activity,ports}.go`, `backend/internal/agent/infrastructure/postgres/`, `backend/internal/agent/infrastructure/eventing/sink.go`, 对应 `_test.go`

**Files:**
- Modify: `backend/internal/agent/domain/session.go`（+`ActivityChannel`, `ActivityVersionID`）
- Modify: `backend/internal/agent/application/session_service.go`（`SessionOptions`/`CreateWithOptions`）
- Modify: `backend/internal/agent/application/activity.go`（emitter 接收 channel/versionID）
- Modify: `backend/internal/agent/application/ports.go`（`ActivityEvent.VersionID`）
- Modify: `backend/internal/agent/application/run_service.go:148-205,372`（3 个 emitter 构造点）
- Modify: `backend/internal/agent/infrastructure/postgres/session_repository.go`（SELECT/RETURNING/scan）
- Modify: `backend/internal/agent/infrastructure/eventing/sink.go`（设置 `VersionID`）
- Test: `agent/application/{activity,session_service,run_service}_test.go`、`agent/infrastructure/postgres/repository_integration_test.go`

**Interfaces:**
- Produces:

```go
type SessionOptions struct { ActivityChannel string; ActivityVersionID *string }
func (s *SessionService) CreateWithOptions(ctx context.Context, profileID int64, title string, actor *int64, opts SessionOptions) (domain.Session, error)
func (s *RunService) HasRunningByChannel(ctx context.Context, channel string) (bool, error)
func newActivityEmitter(sink EventSink, sessionID int64, role, channel string, versionID *string) *activityEmitter
```

- [ ] **Step 1: 写失败测试**

`TestCreateWithOptionsPersistsChannel`（P1 默认空通道；P3 写 `version:42` + versionID）；`TestActivityEmitterFallsBackToAgentChannel`（空通道→`agent:<id>`）；`TestActivityEmitterUsesVersionChannel`；`TestRecoverInterruptedUsesSessionChannel`；`TestHasRunningByChannel`（插入 running run 后 true，完成后 false）。

- [ ] **Step 2: 跑失败**

Run: `cd backend; go test ./internal/agent/application ./internal/agent/infrastructure/eventing -run 'Channel|RunningByChannel' -v`
Expected: FAIL

- [ ] **Step 3: 实现**

通道为空回退 `agent:{session_id}`；sink 仅在 `ActivityEvent.VersionID` 非空时设置；`RecoverInterrupted` 从 session 读取通道；`HasRunningByChannel` 用 `agent_runs JOIN agent_sessions WHERE activity_channel=$1 AND status='running'`。保留 `Create` 委托 `CreateWithOptions` 空 options。

- [ ] **Step 4: 跑通过（含 P1 既有测试）**

Run: `cd backend; go test ./internal/agent/...`
Expected: PASS（P1 原测试不回归）

- [ ] **Step 5: 提交**

```bash
git add backend/internal/agent
git commit -m "feat: add optional agent activity channel"
```

---

### Task 5: 前端 API/types/ID 串 + apiGetPage + useVersionStream

**Wave:** W0 · **并发:** 可并发（前端基础轨） · **依赖:** 无
**写作用域:** `frontend/src/features/versions/{types.ts,api.ts,useVersionStream.ts,*.test.ts}`, `frontend/src/lib/api-client.ts`, `frontend/src/lib/api-client.test.ts`

**Files:**
- Create: `frontend/src/features/versions/types.ts`
- Create: `frontend/src/features/versions/api.ts`
- Create: `frontend/src/features/versions/useVersionStream.ts`
- Create: `frontend/src/features/versions/api.test.ts`
- Create: `frontend/src/features/versions/useVersionStream.test.tsx`
- Modify: `frontend/src/lib/api-client.ts`（新增 `apiGetPage`，保留既有 `apiGet`）
- Test: `frontend/src/lib/api-client.test.ts`

**Interfaces:**
- Produces（前端后续任务消费）：

```ts
export type ApiPage<T> = { items: T[]; nextCursor: string; hasMore: boolean };
export async function apiGetPage<T>(path: string, opts?: ApiFetchOptions): Promise<ApiPage<T>>;
export const listVersions: (q?: VersionQuery) => Promise<ApiPage<Version>>;
export const createVersion: (body: CreateVersionBody) => Promise<Version>;
export const getVersion: (id: string) => Promise<Version>;
export const updateVersion: (id: string, body: UpdateVersionBody) => Promise<Version>;
export const putBindings: (id: string, body: BindingsBody) => Promise<Version>;
export const startClarification: (id: string) => Promise<Version>;
export const enterDesign: (id: string, acknowledge: boolean) => Promise<{ version: Version; warnings: string[] }>;
export function useVersionStream(versionId: string): { events: VersionEvent[]; status: SSEStatus; reconnect: () => void };
```

- [ ] **Step 1: 写失败测试**

`api.test.ts`：断言 `/api/versions` 路径、`id` 为 string 原样、请求体含 `revision`、`apiGetPage` 保留 `next_cursor/has_more`。
`useVersionStream.test.tsx`：mock `fetch` 快照 + `connectSSE`，断言先 `GET events` 再连 `/stream`、`Last-Event-ID` 使用 snapshot cursor、按 seq 去重、`stream.reset` 重取快照。

- [ ] **Step 2: 跑失败**

Run: `cd frontend; pnpm vitest run src/features/versions/api.test.ts src/features/versions/useVersionStream.test.tsx src/lib/api-client.test.ts`
Expected: FAIL

- [ ] **Step 3: 实现**

types 中 P3 自有 ID 全部 `string`；P2 资源 ID `number`。`api.ts` 只走 `apiFetch`。`apiGetPage` 解包 `{data,next_cursor,has_more}`。`useVersionStream` 先 `GET /api/versions/{id}/events?limit=50`，再 `connectSSE` 到 `/api/versions/{id}/stream`，`lastEventId=snapshot_cursor`；`stream.reset` 触发重取；暴露 `live/reconnecting/closed`。

- [ ] **Step 4: 跑通过**

Run: `cd frontend; pnpm vitest run src/features/versions/api.test.ts src/features/versions/useVersionStream.test.tsx src/lib/api-client.test.ts`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add frontend/src/features/versions frontend/src/lib/api-client.ts frontend/src/lib/api-client.test.ts
git commit -m "feat: add P3 frontend API types and version stream hook"
```

---

### Task 6: Version 仓储 + VersionService（创建/绑定/动作/软门禁）

**Wave:** W1 · **并发:** 可并发（后端 Version 轨） · **依赖:** T3
**写作用域:** `backend/internal/version/infrastructure/postgres/version_repository.go`, `backend/internal/version/application/version_service.go`, 对应 `_test.go`, `backend/internal/version/integration/version_*_test.go`

**Files:**
- Create: `backend/internal/version/infrastructure/postgres/version_repository.go`
- Create: `backend/internal/version/application/version_service.go`
- Create: `backend/internal/version/application/version_service_test.go`
- Create: `backend/internal/version/infrastructure/postgres/version_repository_test.go`（集成）
- Test: `backend/internal/version/integration/version_actions_integration_test.go`

**Interfaces:**
- Consumes: `VersionRepository`、`ResourceCatalog`、`GitReader`、`EventPublisher`（P3 端口，T13 实现；本任务测试用 fake）。
- Produces:

```go
type CreateVersionCommand struct { Name, Label, Description string; SystemID, EnvironmentID, CodeRepositoryID *int64; CodeBranch, CodeBaselineCommit *string }
type VersionService struct{ /* repo, resources, git, events, clock */ }
func NewVersionService(repo VersionRepository, resources ResourceCatalog, git GitReader, events EventPublisher) *VersionService
func (s *VersionService) Create(ctx, cmd, actor int64) (*Version, error)
func (s *VersionService) List(ctx, filter VersionFilter) ([]Version, bool, error)
func (s *VersionService) Get(ctx, id int64) (*Version, error)
func (s *VersionService) UpdateMetadata(ctx, id int64, expected int, name, label, desc string, actor int64) (*Version, error)
func (s *VersionService) UpdateBindings(ctx, id int64, expected int, b VersionBindings, actor int64) (*Version, error)
func (s *VersionService) StartClarification(ctx, id int64, actor int64) (*Version, error)
func (s *VersionService) EnterDesign(ctx, id int64, acknowledge bool, actor int64) (*Version, Warnings, error)
```

- [ ] **Step 1: 写失败测试**

`TestCreateVersionValidatesBindings`（系统不存在/环境不匹配/仓库 commit 失败 → 422 reason；合法 → draft + branch/commit 快照）；`TestStartClarificationRequiresRequirement`；`TestStartClarificationOnlyFromDraft`；`TestEnterDesignWarningsRequireAck`（未确认需求 → 409 + details；ack → designing + warnings）；`TestVersionStatusIsNotChangedByRequirementOps`（通过 service 调用新增/确认后 status 不变）；`TestUpdateMetadataRevisionConflict`；`TestUpdateBindingsBlockedInDesigning`。

- [ ] **Step 2: 跑失败**

Run: `cd backend; go test ./internal/version/application -run 'Version|Clarification|Design' -v`
Expected: FAIL

- [ ] **Step 3: 实现**

repo 用 `pgx`，插入显式雪花 ID（由 service 生成）；`UpdateMetadata/UpdateBindings` 用 `WHERE id=$1 AND revision=$2`，无行影响 → `ErrRevisionConflict`；状态动作用 `WHERE id=$1 AND status=$2`；`EnterDesign` 先 `ListUnclarified` 聚合 warnings；事件 `version.created/updated/status_changed`（refs.version 为字符串 ID）。

- [ ] **Step 4: 跑通过**

Run: `powershell -File scripts/test-integration.ps1 ./internal/version/integration -run 'Version' -v`；`cd backend; go test ./internal/version/application -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/version/infrastructure/postgres/version_repository.go backend/internal/version/application/version_service.go backend/internal/version/infrastructure/postgres/version_repository_test.go backend/internal/version/application/version_service_test.go backend/internal/version/integration
git commit -m "feat: add version repository and service"
```

---

### Task 7: P1 图片引用 + ImageResolver + StartWithParts + 预算/多模态校验

**Wave:** W2 · **并发:** 可并发（Agent 轨） · **依赖:** T4
**写作用域:** `backend/internal/agent/domain/message.go`, `backend/internal/agent/application/{run_service,context_builder,ports}.go`, `backend/internal/agent/infrastructure/llm/gateway.go`, 对应 `_test.go`

**Files:**
- Modify: `backend/internal/agent/domain/message.go`（+`ImageRef`）
- Modify: `backend/internal/agent/application/run_service.go`（`StartWithParts`；`Start` 委托）
- Modify: `backend/internal/agent/application/context_builder.go`（`EstimateChars` 计图片固定 4096）
- Modify: `backend/internal/agent/application/ports.go`（`ImageResolver`）
- Modify: `backend/internal/agent/infrastructure/llm/gateway.go`（构造带 resolver；调用前解析；`MultimodalEnabled`）
- Test: `agent/application/{run_service,context_builder}_test.go`、`agent/infrastructure/llm/gateway_test.go`

**Interfaces:**
- Produces:

```go
type ImageResolver interface { ResolveImage(ctx context.Context, ref string) (dataURL string, err error) }
// domain.Part += ImageRef string `json:"image_ref,omitempty"`
func (s *RunService) StartWithParts(ctx context.Context, sessionID int64, parts []domain.Part, actor *int64) (domain.StartResult, error)
func NewGateway(source ConfigSource, factory llmapp.ClientFactory, resolver ImageResolver) *Gateway
```

- [ ] **Step 1: 写失败测试**

`TestStartWithPartsPersistsTextAndImageRef`（`agent_messages.content` 含 `image_ref`，不含 `data:image`）；`TestStartWithPartsRejectsEmpty`；`TestEstimateCharsCountsImages`；`TestGatewayResolvesImageRefs`（resolver 返回 data URL，LLM 请求含 `image_url`）；`TestGatewayRejectsImagesWhenMultimodalDisabled`；`TestGatewayWithoutResolverFailsImageRef`。

- [ ] **Step 2: 跑失败**

Run: `cd backend; go test ./internal/agent/application ./internal/agent/infrastructure/llm -run 'Parts|Image|Multimodal' -v`
Expected: FAIL

- [ ] **Step 3: 实现**

`StartWithParts` 校验“至少一个非空 text 或 image_ref”；`Start` 委托单 text part；`Part.ImageRef` 持久化；gateway 在构造 LLM 请求前把 `image_ref` 替换为 `image_url`（resolver），检查 `cfg.MultimodalEnabled`；resolver nil 时返回可读错误。P1 默认文本路径不变。

- [ ] **Step 4: 跑通过（含 P1 既有测试）**

Run: `cd backend; go test ./internal/agent/...`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/agent
git commit -m "feat: add image refs and multimodal resolution to agent runtime"
```

---

### Task 8: 前端版本列表 + 详情容器 + 总览 + 占位 Tab

**Wave:** W1 · **并发:** 可并发（前端轨） · **依赖:** T5
**写作用域:** `frontend/src/features/versions/{VersionsPage,CreateVersionDialog,VersionDetailPage,VersionHeader,VersionTabs,OverviewTab,PlaceholderTab}.tsx`, `frontend/src/app/(app)/versions/`, 对应 `_test.tsx`

**Files:**
- Create: components above + tests
- Modify: `frontend/src/app/(app)/versions/page.tsx`（替换占位页）
- Create: `frontend/src/app/(app)/versions/[id]/page.tsx`
- Test: `frontend/src/features/versions/{VersionsPage,VersionDetailPage}.test.tsx`

**Interfaces:**
- Consumes: T5 `listVersions/createVersion/getVersion/...`。
- Produces: `VersionDetailPage` 容器，T17/T18 在 Tab 内挂载工作台/聊天；`VersionHeader` 暴露刷新回调。

- [ ] **Step 1: 写失败测试**

`VersionsPage.test.tsx`：渲染列表（name/label/status/需求计数/待澄清）、空态、创建对话框必填校验、创建后跳转。`VersionDetailPage.test.tsx`：默认 Tab=`requirements`、9 个 Tab、`?tab=overview` 深链、header 显示状态/绑定/按钮、占位 Tab 显示阶段、进入设计弹出 warnings 确认。

- [ ] **Step 2: 跑失败**

Run: `cd frontend; pnpm vitest run src/features/versions/VersionsPage.test.tsx src/features/versions/VersionDetailPage.test.tsx`
Expected: FAIL

- [ ] **Step 3: 实现**

复用 `EmptyState`、Radix Tabs、`StatusBadge`（扩展 P3 状态色）；`VersionsPage` 支持 status 过滤与分页；`CreateVersionDialog` 级联选择 system→environment→code repo（可选，输入来自现有 resources API）；`VersionDetailPage` 读 `window.location.search` 的 `tab`（沿用 ResourcesPage 模式），默认 `requirements`；9 Tab 中 8 个占位。Header 的“开始澄清/进入设计”按状态显示；进入设计有 warnings 时弹确认后带 `acknowledgeUnclarified=true` 重试。

- [ ] **Step 4: 跑通过**

Run: `cd frontend; pnpm vitest run src/features/versions/VersionsPage.test.tsx src/features/versions/VersionDetailPage.test.tsx`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add frontend/src/features/versions frontend/src/app/\(app\)/versions
git commit -m "feat: add version list and detail container"
```

---

### Task 9: Requirement 仓储 + RequirementService/RelationService

**Wave:** W2 · **并发:** 可并发（后端需求轨） · **依赖:** T6
**写作用域:** `backend/internal/version/infrastructure/postgres/requirement_repository.go`, `backend/internal/version/application/{requirement_service,relation_service}.go`, 对应 `_test.go`, `version/integration/requirement_*_test.go`

**Files:**
- Create: postgres repo + services + tests
- Test: `version/integration/requirement_state_integration_test.go`

**Interfaces:**
- Produces:

```go
func NewRequirementService(repo RequirementRepository, versions VersionRepository, events EventPublisher) *RequirementService
func (s *RequirementService) Create(ctx context.Context, versionID int64, cmd CreateRequirementCommand, actor int64) (*Requirement, error)
func (s *RequirementService) Update(ctx context.Context, versionID, reqID int64, cmd UpdateRequirementCommand, actor int64) (*Requirement, error)
func (s *RequirementService) Delete(ctx context.Context, versionID, reqID int64, actor int64) error
func (s *RequirementService) Confirm(ctx context.Context, versionID, reqID int64, actor int64) (*Requirement, error)
func (s *RequirementService) Reopen(ctx context.Context, versionID, reqID int64, actor int64) (*Requirement, error)

func NewRelationService(repo RequirementRepository) *RelationService
func (s *RelationService) Create(ctx context.Context, versionID int64, cmd CreateRelationCommand, actor int64) (*Relation, error)
func (s *RelationService) Update(ctx context.Context, versionID, relID int64, relType, desc string) (*Relation, error)
func (s *RelationService) Delete(ctx context.Context, versionID, relID int64) error
```

- [ ] **Step 1: 写失败测试**

`TestCreateRequirementDoesNotChangeVersionStatus`（draft/clarifying/designing 三种版本状态）；`TestConfirmRequiresZeroOpenQuestions`；`TestConfirmReopenStateConflicts`；`TestClarifiedRequirementReadOnlyUntilReopen`；`TestDeleteOnlyDraftAndCascades`；`TestRelationNormalizationAndDuplicate`（无向归一化、depends_on 方向、自环、跨版本）；`TestRevisionConflict`。

- [ ] **Step 2: 跑失败**

Run: `cd backend; go test ./internal/version/application -run 'Requirement|Relation' -v`
Expected: FAIL

- [ ] **Step 3: 实现**

repo 事务内处理级联；确认前 `CountOpenQuestions(reqID)`；关系无向用 `LEAST/GREATEST` 去重；删除用 `ON DELETE CASCADE`；事件 `requirement.added/updated/clarified/reopened/deleted`、`requirement.relation_*`；ID 由 service 生成雪花。`source/last_update_source` 按调用方设置（HTTP=user；工具=ai，T13 使用）。

- [ ] **Step 4: 跑通过**

Run: `powershell -File scripts/test-integration.ps1 ./internal/version/integration -run 'Requirement' -v`；`cd backend; go test ./internal/version/application -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/version/infrastructure/postgres/requirement_repository.go backend/internal/version/application/requirement_service.go backend/internal/version/application/relation_service.go backend/internal/version/application/*_test.go backend/internal/version/integration
git commit -m "feat: add requirement pipeline and relations"
```

---

### Task 10: Clarification 仓储 + ClarificationService

**Wave:** W3 · **并发:** 串行（后端澄清→附件链） · **依赖:** T9
**写作用域:** `backend/internal/version/infrastructure/postgres/clarification_repository.go`, `backend/internal/version/application/clarification_service.go`, 对应 `_test.go`, `version/integration/clarification_*_test.go`

**Files:**
- Create: repo + service + tests
- Test: `version/integration/clarification_state_integration_test.go`

**Interfaces:**
- Consumes: `AgentSessions`、`RequirementRepository`、`VersionRepository`。
- Produces:

```go
func NewClarificationService(repo ClarificationRepository, reqs RequirementRepository, versions VersionRepository, agents AgentSessions, events EventPublisher) *ClarificationService
func (s *ClarificationService) StartSession(ctx context.Context, versionID int64, actor int64) (*ClarificationSession, error)
func (s *ClarificationService) CompleteSession(ctx context.Context, versionID, sessionID int64, actor int64) (*ClarificationSession, error)
func (s *ClarificationService) SendMessage(ctx context.Context, versionID int64, message string, actor int64) (StartRunResult, error)
func (s *ClarificationService) StartAnalysis(ctx context.Context, versionID int64, actor int64) (StartRunResult, error)
func (s *ClarificationService) StartRecon(ctx context.Context, versionID int64, actor int64) (StartRunResult, error)
func (s *ClarificationService) UpsertQuestion(ctx context.Context, versionID int64, q QuestionInput, actor int64, source string) (*Question, error)
func (s *ClarificationService) AnswerQuestion(ctx context.Context, versionID, qid int64, answer string, actor int64, via string) (*Question, error)
func (s *ClarificationService) CreateDecision(ctx context.Context, versionID int64, d DecisionInput, actor int64, source string) (*Decision, error)
func (s *ClarificationService) Decide(ctx context.Context, versionID, did int64, accept bool, actor int64) (*Decision, error)
```

- [ ] **Step 1: 写失败测试**

`TestStartSessionSingleActive`（第二次 409）；`TestCompleteSessionBlockedByRunningAgent`；`TestSendMessageRequiresActiveAndIdle`；`TestMessageStartsAgentRun`（fake AgentSessions 收到文本）；`TestQuestionTransitions`（open→answered 需 answer；dismiss；reopen 清空）；`TestDecisionAcceptClosesLinkedQuestion`（同事务）；`TestAnalyzeReconRequireActiveSession`；`TestAgentBusy`（HasRunningByChannel true → 409）。

- [ ] **Step 2: 跑失败**

Run: `cd backend; go test ./internal/version/application -run 'Clarification|Session|Question|Decision' -v`
Expected: FAIL

- [ ] **Step 3: 实现**

session 启动调用 `AgentSessions.Create("clarification-dialog", ..., "version:<id>", "<id>")`；消息调用 `StartText`；analysis/recon 调用 `AgentSessions.Create`（对应 profile）+ `StartText/StartParts`；问题/决策事务；接受 proposed 决策时同事务更新问题；所有写发版本事件。

- [ ] **Step 4: 跑通过**

Run: `powershell -File scripts/test-integration.ps1 ./internal/version/integration -run 'Clarification' -v`；`cd backend; go test ./internal/version/application -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/version/infrastructure/postgres/clarification_repository.go backend/internal/version/application/clarification_service.go backend/internal/version/application/*_test.go backend/internal/version/integration
git commit -m "feat: add clarification sessions/questions/decisions"
```

---

### Task 11: 附件文件系统存储 + AttachmentService

**Wave:** W3 · **并发:** 串行（后端澄清→附件链） · **依赖:** T9/T10
**写作用域:** `backend/internal/version/infrastructure/filestore/`, `backend/internal/version/application/attachment_service.go`, 对应 `_test.go`, `version/integration/attachment_*_test.go`

**Files:**
- Create: `backend/internal/version/infrastructure/filestore/store.go` + test
- Create: `backend/internal/version/application/attachment_service.go` + test
- Test: `version/integration/attachment_fs_integration_test.go`

**Interfaces:**
- Produces:

```go
type SavedAttachment struct { StoragePath, SHA256 string; Size int64 }
type AttachmentStore interface {
    Save(ctx context.Context, versionID, attachmentID int64, filename string, r io.Reader) (SavedAttachment, error)
    Open(ctx context.Context, storagePath string) (io.ReadSeekCloser, error)
    Remove(ctx context.Context, storagePath string) error
}
func NewAttachmentService(repo RequirementRepository, store AttachmentStore, limits Limits, events EventPublisher) *AttachmentService
func (s *AttachmentService) Upload(ctx context.Context, versionID, reqID int64, filename, declaredType string, r io.Reader, actor int64) (*Attachment, error)
func (s *AttachmentService) ReadText(ctx context.Context, versionID, attachmentID int64, maxBytes int) (string, error)
func (s *AttachmentService) ImageDataURL(ctx context.Context, attachmentID int64) (string, error)
func (s *AttachmentService) Delete(ctx context.Context, versionID, reqID, attachmentID int64, actor int64) error
```

- [ ] **Step 1: 写失败测试**

`TestSaveRejectsTraversalAndSymlink`（`../`、绝对路径、symlink 根内逃逸）；`TestSaveEnforces10MiB`；`TestSaveAtomicAndPermissions`（临时文件不残留、文件 0600/目录 0700）；`TestUploadMIMEAndExtension`（png/jpg/txt/md/json/yaml；错误类型 422）；`TestUploadDuplicateSHA`（409）；`TestUploadLimits`（每需求 10/每版本 50）；`TestDeleteRemovesMetadataAndFile`；`TestImageDataURLOnlyImages`；`TestReadTextRejectsNonUTF8`。

- [ ] **Step 2: 跑失败**

Run: `cd backend; go test ./internal/version/infrastructure/filestore ./internal/version/application -run 'Attachment|Save|Upload' -v`
Expected: FAIL

- [ ] **Step 3: 实现**

路径 `<root>/<versionID>/<attachmentID>_<safeName>`；临时文件 + sha256 + 限长 + `fsync` + rename；根内校验；MIME 用 `http.DetectContentType` + 扩展名白名单；文件 0600/目录 0700；DB 先写元数据后文件？按 spec：先文件后 DB，DB 失败删文件；删除先 DB 后文件 best-effort。`ImageDataURL` 读文件 + base64，仅内存返回。

- [ ] **Step 4: 跑通过**

Run: `powershell -File scripts/test-integration.ps1 ./internal/version/integration -run 'Attachment' -v`；`cd backend; go test ./internal/version/infrastructure/filestore ./internal/version/application -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/version/infrastructure/filestore backend/internal/version/application/attachment_service.go backend/internal/version/application/*_test.go backend/internal/version/integration
git commit -m "feat: add filesystem attachment storage"
```

---

### Task 12: P1 工具调用上下文 + 3 个 Profile seed + GetByKey

**Wave:** W3 · **并发:** 可并发（Agent 轨） · **依赖:** T7
**写作用域:** `backend/internal/agent/application/{tool_invocation,tool_executor,profile_service}.go`, `backend/internal/agent/domain/profile_p3.go`, 对应 `_test.go`

**Files:**
- Create: `agent/application/tool_invocation.go` + test
- Modify: `agent/application/tool_executor.go`（调用前注入 context）
- Create: `agent/domain/profile_p3.go`（3 个 prompt + `ClarificationProfiles()`）
- Modify: `agent/application/profile_service.go`（`GetByKey`）
- Test: `agent/domain/profile_p3_test.go`、`agent/application/{tool_invocation,profile_service}_test.go`

**Interfaces:**
- Produces:

```go
type Invocation struct { SessionID, RunID int64; ActorUserID *int64; ProfileKey string }
func WithToolInvocation(ctx context.Context, inv Invocation) context.Context
func InvocationFromContext(ctx context.Context) (Invocation, bool)
func ClarificationProfiles() []AgentProfile // clarification-dialog / requirement-analysis / context-recon, Builtin=false
func (s *ProfileService) GetByKey(ctx context.Context, key string) (AgentProfile, error)
```

- [ ] **Step 1: 写失败测试**

`TestToolInvocationContext`（executor 执行时工具能读到 session/run/actor）；`TestClarificationProfilesValidate`（key/prompt/tools/预算合法、Builtin=false）；`TestAllowedToolsRegistered`（与 registry 交集）；`TestGetByKey`（存在/不存在）；`TestEnsureSeedIdempotent`（两次 seed 不重复）。

- [ ] **Step 2: 跑失败**

Run: `cd backend; go test ./internal/agent/application ./internal/agent/domain -run 'Invocation|Profile|Seed' -v`
Expected: FAIL

- [ ] **Step 3: 实现**

prompts 使用 spec §8.5 原文；预算按 §8.1；`GetByKey` 用 `List` 匹配 key（或 repo 新查询）；`WithToolInvocation` 放入私有 ctx key；executor 在 `tool.Execute` 前注入 `Invocation{...}`。

- [ ] **Step 4: 跑通过**

Run: `cd backend; go test ./internal/agent/...`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/agent
git commit -m "feat: add P3 agent profiles and tool invocation context"
```

---

### Task 13: P3 适配器 + AgentToolService + 11 个工具

**Wave:** W4 · **并发:** 可并发（后端适配轨） · **依赖:** T9/T10/T11/T12
**写作用域:** `backend/internal/version/infrastructure/{agent,resource,gitread,eventing}/`, `backend/internal/version/application/agent_tool_service.go`, `backend/internal/agent/infrastructure/tools/clarification.go`, 对应 `_test.go`

**Files:**
- Create: `version/infrastructure/gitread/reader.go` + test
- Create: `version/infrastructure/resource/catalog.go` + test
- Create: `version/infrastructure/agent/sessions.go`, `image_resolver.go` + tests
- Create: `version/infrastructure/eventing/publisher.go` + test
- Create: `version/application/agent_tool_service.go` + test
- Create: `agent/infrastructure/tools/clarification.go` + test

**Interfaces:**
- Produces:

```go
func NewGitReader(timeout time.Duration) *GitReader
func (r *GitReader) ResolveCommit(ctx context.Context, repoPath, branch string) (string, error)
func (r *GitReader) Tree(ctx context.Context, repoPath, commit string, include, exclude []string, max int) ([]CodeEntry, error)
func (r *GitReader) Read(ctx context.Context, repoPath, commit, path string, maxBytes int) (string, error)

func NewEventPublisher(events *eventingapp.Service) *EventPublisher
func (p *EventPublisher) Publish(ctx context.Context, e VersionEvent) error

func NewAgentSessions(sessions *agentapp.SessionService, runs *agentapp.RunService, profiles *agentapp.ProfileService) *AgentSessionsAdapter
func (a *AgentSessionsAdapter) Create(...) (int64, error) // channel/versionID
func (a *AgentSessionsAdapter) StartText/StartParts/Messages/HasRunningByChannel(...)

func NewImageResolver(attachments *AttachmentService) *ImageResolver
func (r *ImageResolver) ResolveImage(ctx context.Context, ref string) (string, error) // attachment:<id> -> data URL

func NewAgentToolService(...) *AgentToolService // 11 primitives: ContextGet/UpdateDraft/QuestionUpsert/QuestionAnswer/DecisionPropose/RelationPropose/AttachmentList/AttachmentReadText/ContextSummaryUpdate/CodeTree/CodeRead
func NewClarificationTools(port VersionToolPort) []agentdomain.Tool // 11 tools, schemas per spec §8.3
```

- [ ] **Step 1: 写失败测试**

`TestGitReadAtCommit`（临时 repo：tree/read/commit pin；绝对路径/`..` 拒绝）；`TestCatalogAdapterMapsResources`；`TestAgentSessionsAdapterUsesVersionChannel`；`TestImageResolverReturnsDataURLAndRejectsNonImage`；`TestEventPublisherSetsVersionChannelAndRefs`；`TestToolServiceUpdateDraftCAS`；`TestClarificationToolsSpecsAndExecution`（11 个工具 schema 名/白名单/错误映射）；`TestToolsDoNotAcceptActorArg`。

- [ ] **Step 2: 跑失败**

Run: `cd backend; go test ./internal/version/infrastructure/... ./internal/version/application ./internal/agent/infrastructure/tools -run 'Git|Adapter|Tool|Resolver|Publisher' -v`
Expected: FAIL

- [ ] **Step 3: 实现**

GitReader 用只读 `git ls-tree`/`git show`，`GIT_OPTIONAL_LOCKS=0`、5s、路径前缀校验；EventPublisher 固定 `version:<id>`、`VersionID` 字符串、refs；AgentSessionsAdapter 用 T4/T7/T12 新 API；ImageResolver 解析 `attachment:<id>`，只允许 image、≤2MiB；AgentToolService 复用 T9/T10/T11 服务；工具适配器只依赖本地窄接口 + stdlib，不 import P3 domain 以外？工具 adapter 放 agent infra，使用本地 port（main 注入 P3 service）。

- [ ] **Step 4: 跑通过**

Run: `cd backend; go test ./internal/version/infrastructure/... ./internal/version/application ./internal/agent/infrastructure/tools -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/version/infrastructure/{agent,resource,gitread,eventing} backend/internal/version/application/agent_tool_service.go backend/internal/agent/infrastructure/tools/clarification.go
git commit -m "feat: add P3 adapters and clarification agent tools"
```

---

### Task 14: HTTP handlers v1（versions/requirements/relations/attachments）

**Wave:** W4 · **并发:** 可并发（HTTP v1 轨） · **依赖:** T6/T9/T10/T11
**写作用域:** `backend/internal/version/interfaces/httpapi/{routes,helpers,dto,handlers_versions,handlers_requirements,handlers_relations,handlers_attachments}.go`, 对应 `_test.go`

**Files:**
- Create: routes/helpers/dto/handlers + tests
- Test: `handlers_versions_test.go`、`handlers_requirements_test.go`、`handlers_attachments_test.go`

**Interfaces:**
- Produces:

```go
type Handlers struct { /* version/requirement/relation/attachment services + event snapshot/stream deps */ }
func NewHandlers(deps HandlersDeps) *Handlers
func RegisterRoutes(mux *http.ServeMux, h Handlers) // no admin guard; global auth
// Path params parsed via strconv.ParseInt (decimal string); DTO IDs use `json:",string"` or string
```

- [ ] **Step 1: 写失败测试**

`TestVersionRoutesCRUDAndActions`（401 未登录；创建/列表/详情/PATCH/bindings/start/enter）；`TestIDStringsInJSON`（`id` 为 JSON string，`systemId` 为 number）；`TestRequirementConfirmGate409`；`TestAttachmentMultipartUploadDownloadDelete`；`TestPaginationEnvelope`；`TestErrorReasons`（409/422/404）。

- [ ] **Step 2: 跑失败**

Run: `cd backend; go test ./internal/version/interfaces/httpapi -run 'Version|Requirement|Attachment|ID|Pagination' -v`
Expected: FAIL

- [ ] **Step 3: 实现**

复用 `platform.DecodeJSON/ParsePage/WriteError`；`WriteData`/`WritePage` 本地 helper；actor 从 identity context（参考 P2 `ActorID`）；multipart 用 `r.ParseMultipartForm(10<<20)`；下载 `http.ServeContent` + nosniff；版本不存在 404；ID DTO 字符串化。

- [ ] **Step 4: 跑通过**

Run: `cd backend; go test ./internal/version/interfaces/httpapi -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/version/interfaces/httpapi
git commit -m "feat: add version/requirement HTTP API"
```

---

### Task 15: HTTP handlers v2（clarification/questions/decisions/events/SSE）

**Wave:** W5 · **并发:** 可并发（HTTP v2 轨） · **依赖:** T13/T14
**写作用域:** `backend/internal/version/interfaces/httpapi/{routes,handlers_clarification,handlers_questions,handlers_decisions,handlers_events,sse}.go`, 对应 `_test.go`

**Files:**
- Create: handlers/sse + tests
- Modify: `routes.go`（挂载新端点）
- Test: `handlers_clarification_test.go`、`handlers_events_test.go`

**Interfaces:**
- Produces: `/api/versions/{id}/clarification*`、`/decisions*`、`/events`、`/stream`；`NewVersionStreamHandler(reader, hub)` 固定 channel 并校验版本存在。

- [ ] **Step 1: 写失败测试**

`TestClarificationSessionStartAndMessage`（201/202；active 409；agent busy 409）；`TestQuestionAndDecisionEndpoints`（answer/dismiss/accept 关闭问题）；`TestEventsSnapshot`（`snapshot_cursor`/`has_more`）；`TestStreamRejectsUnknownVersion`（404）与 `TestStreamFixesChannel`（客户端 `?channel=` 被忽略）；`TestStreamAuthRequiresToken`。

- [ ] **Step 2: 跑失败**

Run: `cd backend; go test ./internal/version/interfaces/httpapi -run 'Clarification|Question|Decision|Events|Stream' -v`
Expected: FAIL

- [ ] **Step 3: 实现**

SSE 包装器：解析 path ID→int64→`Get` 校验存在→把 `r.URL.Query()` 的 `channel` 覆盖为 `version:<id>`→交给 `eventinghttp.NewSSEHandler`。事件快照用 `eventingapp.Service.Snapshot`。clarification 消息列表走 `AgentSessions.Messages`（`before_id` 数字）。所有 ID 路径参数用十进制字符串解析。

- [ ] **Step 4: 跑通过**

Run: `cd backend; go test ./internal/version/interfaces/httpapi -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/version/interfaces/httpapi
git commit -m "feat: add clarification API and version SSE"
```

---

### Task 16: 组装根 + server 装配 + 集成

**Wave:** W6 · **并发:** 串行（组装） · **依赖:** T15（及全部后端）
**写作用域:** `backend/cmd/eztest/main.go`, `backend/internal/server/server.go`, `backend/internal/server/*_test.go`, `backend/internal/version/integration/wiring_integration_test.go`

**Files:**
- Modify: `backend/cmd/eztest/main.go`（构造雪花/附件目录/P3 repo/service/handlers，注入 server；注册工具 + 3 profiles）
- Modify: `backend/internal/server/server.go`（`Dependencies.Version *versionhttp.Handlers`；挂载 `/api/versions`，全局 auth）
- Test: `server/version_routes_test.go`、`version/integration/wiring_integration_test.go`

**Interfaces:**
- Produces: 可运行的 P3 API；`server.Dependencies.Version`。
- Consumes: T2–T15 全部接口。

- [ ] **Step 1: 写失败测试**

`TestVersionRoutesMounted`（server.New 带 Dependencies.Version 时 `/api/versions` 401/200）；`TestP3WiringEndToEnd`（创建版本→需求→确认→进入设计；fake agent/LLM；SSE 至少收到 version.created/requirement.added）；`TestAttachmentDirCreated0700`。

- [ ] **Step 2: 跑失败**

Run: `cd backend; go test ./internal/server -run 'Version' -v`；`powershell -File scripts/test-integration.ps1 ./internal/version/integration -run 'Wiring' -v`
Expected: FAIL（依赖未装配）

- [ ] **Step 3: 实现**

main 中按依赖顺序构造：雪花→config 校验→PG repos→adapters→services→handlers→server deps；`MkdirAll(cfg.VersionAttachmentDir, 0700)`；`EnsureBuiltin` 3 profiles 并校验 allowed_tools；注册 `NewClarificationTools(agentToolService)`；`ImageResolver` 注入 agent gateway。server 在 `deps.Version != nil` 时 `versionhttp.RegisterRoutes(mux, *deps.Version)`（无 admin guard）。

- [ ] **Step 4: 跑通过**

Run: `cd backend; go test ./internal/server ./internal/version/... -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/cmd/eztest/main.go backend/internal/server backend/internal/version/integration
git commit -m "feat: wire P3 version context into the service"
```

---

### Task 17: 前端工作台（需求/关系/附件/绑定）

**Wave:** W2 · **并发:** 可并发（前端工作台轨） · **依赖:** T8
**写作用域:** `frontend/src/features/versions/{Workbench,RequirementList,RequirementEditor,AttachmentPanel,RelationGraph,VersionBindingsForm}.tsx`, 对应 `_test.tsx`

**Files:**
- Create components + tests
- Modify: `VersionDetailPage.tsx`（requirements Tab 挂 Workbench）

**Interfaces:**
- Consumes: T5 API；T8 容器。
- Produces: `Workbench` props `{version,onChanged}`；`RelationGraph` props `{requirements,relations,onSelect}`。

- [ ] **Step 1: 写失败测试**

`TestWorkbenchRequirementFlow`（新增→编辑→确认按钮在 open=0 时可用）；`TestRequirementEditorReadOnlyWhenClarified`；`TestRelationGraphRendersNodesAndEdges`（depends_on 箭头/无向线/图例）；`TestAttachmentUploadDelete`；`TestBindingsEditOnlyDraftOrClarifying`；`TestEmptyStates`。

- [ ] **Step 2: 跑失败**

Run: `cd frontend; pnpm vitest run src/features/versions/Workbench.test.tsx src/features/versions/RelationGraph.test.tsx`
Expected: FAIL

- [ ] **Step 3: 实现**

三栏/两栏布局，需求列表 + 编辑器 + 附件 + 关系图 + 绑定；所有写请求带 revision；409 提示刷新；关系图用 SVG 圆形布局 + 图例 + 文本标签；附件上传 FormData；clarified 只读 + 重开；确认按钮禁用并显示 open 原因。

- [ ] **Step 4: 跑通过**

Run: `cd frontend; pnpm vitest run src/features/versions`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add frontend/src/features/versions
git commit -m "feat: add version workbench with requirements and relations"
```

---

### Task 18: 前端澄清对话 + 问题/决策面板

**Wave:** W3 · **并发:** 可并发（前端澄清轨） · **依赖:** T17
**写作用域:** `frontend/src/features/versions/{ClarificationChat,QuestionPanel,DecisionPanel}.tsx`, 对应 `_test.tsx`

**Files:**
- Create components + tests
- Modify: `Workbench.tsx`（挂载澄清区）

**Interfaces:**
- Consumes: T5 API/`useVersionStream`；T13 clarification endpoints（契约见 spec §7.5/§7.6）。
- Produces: 工作台右侧澄清面板；`ClarificationChat` props `{versionId, session, events}`。

- [ ] **Step 1: 写失败测试**

`TestChatSendAndStream`（发送后 POST messages；mock SSE 追加 assistant/tool activity；显示重连状态）；`TestChatSnapshotAndReload`；`TestQuestionPanelAnswerDismissReopen`；`TestDecisionPanelAcceptReject`；`TestAIUnavailableBanner`（503 保留输入）；`TestAnalyzeReconButtonsDisabledWhenBusy`。

- [ ] **Step 2: 跑失败**

Run: `cd frontend; pnpm vitest run src/features/versions/ClarificationChat.test.tsx src/features/versions/QuestionPanel.test.tsx`
Expected: FAIL

- [ ] **Step 3: 实现**

对话使用 `useVersionStream` + `GET messages` 快照；assistant text/tool activity 折叠；问题行内回答/驳回/重开；决策接受/驳回；分析/侦察按钮；AI 不可用横幅与重试；断线不显示“无消息”。

- [ ] **Step 4: 跑通过**

Run: `cd frontend; pnpm vitest run src/features/versions`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add frontend/src/features/versions
git commit -m "feat: add clarification chat and question/decision panels"
```

---

### Task 19: fake LLM 场景 + Playwright E2E

**Wave:** W7 · **并发:** 串行（E2E） · **依赖:** T16/T18
**写作用域:** `scripts/fake-llm.cjs`, `scripts/e2e.ps1`, `frontend/e2e/versions*.spec.ts`

**Files:**
- Modify: `scripts/fake-llm.cjs`（P3 场景：按用户消息返回工具调用）
- Modify: `scripts/e2e.ps1`（附件目录临时根、fake LLM 场景开关）
- Create: `frontend/e2e/versions.spec.ts`
- Create: `frontend/e2e/versions-clarification.spec.ts`
- Create: `frontend/e2e/versions-degraded.spec.ts`

**Interfaces:**
- Consumes: P3 API/UI 全链路；P2 resources fixture。
- Produces: 可重复 E2E 证据；fake LLM 请求日志。

- [ ] **Step 1: 写 E2E 场景**

1. `versions.spec.ts`：创建版本（system/env/repo）→3 需求+附件→关系→确认→进入设计（warnings/ack）→reload→Tab 切换。
2. `versions-clarification.spec.ts`：开始澄清→会话→fake LLM 两轮（问题/草案/关系/决策）→回答→接受决策→确认→SSE 实时→reload。
3. `versions-degraded.spec.ts`：fake LLM 失败/未配置→会话 503→人工问题/决策/确认可用。
4. 多模态：recon 请求含解析后的 image_url；`agent_messages` 无 data URL。

- [ ] **Step 2: 跑失败**

Run: `powershell -File scripts/e2e.ps1`
Expected: FAIL until fake LLM/UI wired（新 spec 未实现）

- [ ] **Step 3: 实现 fake LLM + 脚本**

fake LLM 按消息内容标记（如 `P3-CLARIFY-1`）返回 `tool_calls`：`clarification.question_upsert`、`requirement.update_draft`、`relation.propose`、`decision.propose`；记录请求到 `.tmp/fake-llm-requests.jsonl`；e2e.ps1 设置 `EZTEST_VERSION_ATTACHMENT_DIR=.tmp/p3-attachments` 并在结束时清理。

- [ ] **Step 4: 跑通过**

Run: `powershell -File scripts/e2e.ps1`
Expected: PASS（3 个 P3 spec 通过；无真实 LLM 费用）

- [ ] **Step 5: 提交**

```bash
git add scripts/fake-llm.cjs scripts/e2e.ps1 frontend/e2e/versions*.spec.ts
git commit -m "test: add P3 clarification e2e coverage"
```

---

### Task 20: OpenAPI + acceptance 脚本 + README/文档收尾

**Wave:** W5 · **并发:** 可并发（文档/脚本轨） · **依赖:** T14/T15（API 契约）
**写作用域:** `docs/openapi/api.yaml`, `scripts/acceptance.ps1`, `README.md`

**Files:**
- Modify: `docs/openapi/api.yaml`（全部 P3 路径/schema/错误 reason/SSE/字符串 ID）
- Modify: `scripts/acceptance.ps1`（P3 关键词与检查项）
- Modify: `README.md`（P3 阶段与验收命令）

**Interfaces:**
- Consumes: spec §7；T14/T15 路由。
- Produces: 可发现的 P3 契约与验收入口。

- [ ] **Step 1: 更新 OpenAPI**

追加 `/api/versions*`、clarification、questions、decisions、events、stream；schema 中 P3 ID 为 string；错误 reason 枚举与 spec §7.1 一致；multipart 请求体；SSE 说明。

- [ ] **Step 2: 扩展 acceptance 脚本**

在 `scripts/acceptance.ps1` 增加 P3 关键词（`/api/versions`、`clarification`、`enter-design`、`snowflake`、`version-attachments`）与 smoke 检查；真实 LLM 检查保持 gated。

- [ ] **Step 3: README**

P3 段：范围、启动、测试、验收、附件目录/雪花节点变量；不写进度细节。

- [ ] **Step 4: 验证并提交**

Run: `Select-String -Path docs/openapi/api.yaml -Pattern "/api/versions"`；`Select-String -Path scripts/acceptance.ps1 -Pattern "P3|versions"`

Expected: 命中；OpenAPI 可被现有加载器解析（`go test ./internal/server -run OpenAPI`）。

```bash
git add docs/openapi/api.yaml scripts/acceptance.ps1 README.md
git commit -m "docs: add P3 API contract and acceptance checks"
```

---

### Task 21: 全量门禁 + 人工验收 + 评审 + 合回 dev

**Wave:** W8 · **并发:** 串行（收尾） · **依赖:** T19/T20
**写作用域:** `.superpowers/sdd/`, `.tmp/`（证据）；必要时修复上述写作用域

**Files:**
- No production files expected; fix findings in owning task scope.
- Evidence: `.superpowers/sdd/2026-10-27-eztest-p3-version-requirements/{progress.md,acceptance.md}`（gitignored）
- Screenshots: `.tmp/p3-acceptance/*.png`（gitignored）

**Interfaces:**
- Consumes: 全部任务。
- Produces: 可验收的 P3 分支与证据。

- [ ] **Step 1: 全量门禁**

Run: `powershell -File scripts/test.ps1`；`powershell -File scripts/test-integration.ps1 ./internal/...`；`powershell -File scripts/e2e.ps1`
Expected: 全 PASS；记录输出到 `.superpowers/sdd/.../progress.md`

- [ ] **Step 2: 真实 LLM 人工验收**

启动 dev-api/dev-web + fixture；管理员登录；创建版本/绑定/3 需求/附件；真实 LLM ≥2 轮澄清；接受决策；确认需求；进入设计；普通用户写操作；截图到 `.tmp/p3-acceptance/`。gated：`EZTEST_ACCEPTANCE_REAL_LLM=1`。

- [ ] **Step 3: Code review**

按 `requesting-code-review`：review diff、spec 覆盖、错误码/ID/文件系统/并发/降级；修复后重跑相关门禁。

- [ ] **Step 4: 最终回归 + 提交**

Run: `powershell -File scripts/test.ps1`；`powershell -File scripts/e2e.ps1`
Expected: PASS

如有修复文件，仅 stage 这些文件（禁止 `git add -A`）：

```bash
git add <修复的源文件/测试/文档>
git commit -m "chore: finalize P3 acceptance"
```

- [ ] **Step 5: 合回 dev（本地 ff，不 push）**

Run: `git switch dev; git merge --ff-only feat/p3-version-requirements`
Expected: fast-forward 成功；`git log --oneline -5` 可见 P3 提交。

---

## Self-Review（写完计划后已执行）

- **Spec coverage**：spec §1–§15 均映射到任务；D29/D30/D31、雪花/字符串 ID、附件文件系统、image_ref、版本通道、3 profiles、11 工具、前端 9 Tab、降级、E2E、文档同步均有归属。
- **Step scan**：每个任务含失败测试→跑失败→实现→跑通过→提交；未留 TBD/“适当处理”。
- **Type consistency**：P3 自有 ID 全 int64/string 序列化；`AgentPart`/`ImageRef`、`HasRunningByChannel`、`StartWithParts`、`AgentSessions` 在任务间一致；前端 ID 全 string。
- **Review Focus**：5 类输入均在对应任务测试中钉住（T2/T5/T9/T11/T14/T17）。
- **Proportion**：计划按任务/文件/接口描述，不转录实现代码。

---

*计划版本：v1.0（2026-10-27）；Spec rev3；执行前需用户确认 spec 与计划。*
