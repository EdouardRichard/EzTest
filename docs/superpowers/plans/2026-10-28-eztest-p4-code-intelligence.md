# EzTest P4 Code Intelligence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付「本地 Git T0 + AI T1 + 证据校验 + 缓存版本化 + 跨栈契约 + 影响分析 v1 + 代码与变更 Tab + 真实验收」垂直切片，达到 P4 退出标准与 CI-M0（用例/问题锚点部分按用户决策延后到 P5）。

**Architecture:** 新增 `backend/internal/codeintel` 限界上下文（DDD 四层）；复用 P2 代码仓与 P3 版本基线、P0 LLM 网关/事件/审计/雪花；T1 服务化直调 LLM（异步 run + 进程内 worker），不新增 Agent profile；前端新增 `features/code-intel` 并替换「代码与变更」占位；API 分仓库级 `/api/code/*` 与版本级 `/api/versions/{id}/code/*`。

**Tech Stack:** Go 1.24.6（net/http、pgx/v5）、PostgreSQL 17.6、Next.js 16 / React 19 / TypeScript / Tailwind / Radix、Vitest、Playwright、Node fixture（无新依赖）。

**Spec:** [docs/superpowers/specs/2026-10-28-eztest-p4-code-intelligence-design.md](../specs/2026-10-28-eztest-p4-code-intelligence-design.md)

## Global Constraints

- 范围严格按 spec；**不实现** parser/符号级/字段级分析、用例/问题实体、批次/执行、全仓 AI 分析、线上仓、monorepo 子项目、事实人工修正。
- **D29**：P4 新增 9 表主键统一雪花 ID（应用层生成，DDL 无序列/默认值）；codeintel 自有 ID、requirement/app_model 主体 ID 在 JSON/URL/事件/工具参数中为十进制字符串；`repoId`/`createdBy`/`selectionUpdatedBy` 为 number；禁止 `Number()` 精度丢失。
- **D30**：不把二进制/base64/data URL 写入 PostgreSQL；代码内容只按需读取，不落业务表；证据仅存 path/行号/哈希与摘要。
- **D32/D33**（spec §13）：T0 只读 Git；T1 服务化 + 异步 run + 不新增 Agent profile；证据按 commit 内容校验；影响对象 = 需求 + 应用模型 + unmapped（`affected.cases` 预留）；缓存键/事实版本/分析版本语义固定。
- **P2 边界**：`resource.code_repositories` 保持 BIGSERIAL/JSON number；P4 只经 resource application 新端口写 `last_indexed_at`，不直接 UPDATE P2 表、不改 P2 表结构；不建跨上下文 FK。
- **P3 契约**：`version/infrastructure/gitread` 与 `code.tree`/`code.read` 工具不改；codeintel 自带 `infrastructure/gitx`，不 import 其他上下文 infrastructure/interfaces。
- **迁移**：只追加 `codeintel_0001_code_facts.sql`、`codeintel_0002_code_impact.sql` 到 `order.txt` 第 15/16 位；已应用迁移与 checksum 不改。
- **API**：成功 `{"data":...}`；异步 202；错误复用 `platform/errors` + `details.reason`；权限 repo 配置/索引 admin、其余登录用户、未登录 401；OpenAPI 手工同步。
- 不新增 Go/前端依赖；前端不引入图库/表单库/状态库；HTTP 走 `api-client`，SSE 走 `sse-client`/`useVersionStream`。
- 事件只放 id/计数/路径摘要；`summary ≤500 rune`、`detail ≤64 KiB`、refs ≤20；不记录代码正文/提示词/模型响应。
- 每个任务：先写失败测试 → 跑失败 → 最小实现 → 跑通过 → 提交；提交前缀 `feat/fix/test/docs/chore`。
- 分支 `feat/p4-code-intelligence`；**并行实现者不 commit**，Lead 只 stage 该任务写作用域后提交，保证 per-task review range；验收后本地 ff 合回 `dev`，不 push。
- 不提交 `.env`、`.tools/`、`.tmp/`、`.data/`、`.superpowers/sdd/`、`*.local.md`。
- 端口沿用 Web `6868`、API `13333`、PostgreSQL `5433`、fixture `13335`；真实 LLM 验收 gated `EZTEST_RUN_REAL_LLM=1`。

### 命令约定

- Go 单元：`cd backend; ..\.tools\go\bin\go.exe test ./internal/codeintel/...`（或 `go test`）。
- 配置/领域：`cd backend; go test ./internal/config ./internal/codeintel/domain`.
- 集成：`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-integration.ps1 ./internal/codeintel/integration/...`.
- 前端：`cd frontend; pnpm vitest run src/features/code-intel`、`pnpm lint`、`pnpm typecheck`。
- 全量门禁：`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1`.
- E2E：`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1 code`.
- 完整验收：`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1`.
- 真实 LLM：`$env:EZTEST_RUN_REAL_LLM="1"; powershell -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance-codeintel-llm.ps1`.

## Concurrency Map（实现时并行执行的唯一依据）

| Wave | Tasks | 并行规则 |
|---|---|---|
| W0 | T1 ∥ T2 ∥ T3 | 文档 / 后端地基（config+domain+迁移）/ 前端 API 基础，写作用域互不重叠 |
| W1 | T4 ∥ T5 ∥ T6 | gitx / postgres 仓储 / 应用核心+LLM，互不重叠（均依赖 T2） |
| W2 | T7 ∥ T9 ∥ T17 | P2/P3/事件/审计适配 / 前端 Tab 容器 / fake LLM+fixture 脚本 |
| W3 | T8 ∥ T11 ∥ T12 | 索引+查询+worker / 前端事实与影响面板 / 代码仓索引 UI |
| W4 | T10 ∥ T14 | AnalysisService（T1/缓存/物化/契约）/ 仓库级 HTTP handlers |
| W5 | T13 | ImpactService + AnchorService（application 串行链） |
| W6 | T15 ∥ T16 | 版本级 HTTP handlers / Agent 工具薄壳（不同包） |
| W7 | T18 ∥ T19 | 组装根+server / OpenAPI+acceptance+README+gated 脚本 |
| W8 | T20 | E2E + 全量门禁修复 |
| W9 | T21 | 代码审查 + 真实人工验收 + 合回 dev |

- 同一 Wave 内任务派发给独立 subagent；`**写作用域**` 是该 subagent 唯一允许修改的路径前缀。
- **同包串行**：`codeintel/application` 链 T6→T8→T10→T13；`codeintel/interfaces/httpapi` 链 T14→T15；前端 `features/code-intel` 链 T3→T9→T11；`cmd/eztest`+`server` 仅 T18。这些链按顺序执行，不并发。
- 跨 Wave 依赖未完成不得启动；接口以各任务 `Interfaces → Produces` 为准。
- 并行实现者不 commit；Lead 按写作用域 stage/commit，再进入下一批。
- T2 统一创建两个迁移并追加 `order.txt`，避免并行写迁移目录冲突；其他任务不改迁移。
- T15 与 T16 可并发（前者 httpapi、后者 agent tools）；T18 依赖二者完成后统一装配。

## Review Focus（最容易被忽略、必须有测试钉住的 5 类输入）

1. **代码内容不可信**：证据行号越界/CRLF/删除文件/二进制/超大文件/路径穿越/symlink；必须按 commit 内容校验、无效降 low、拒绝路径、日志不泄露正文。
2. **ID 类型混用**：`repoId` 为 number，codeintel/requirement/app_model 主体 ID 为字符串；前端禁止 `Number()`；URL/JSON/事件/工具参数一致。
3. **P2/P3 衔接与迁移**：迁移只能追加（order.txt 15/16、checksum）；`last_indexed_at` 只经 resource application 端口写；base=版本 baseline，head 实时解析且必须是 base 后代。
4. **缓存/版本/确定性**：缓存键含 model+prompt；force 只新增 analysis 版本；fact 版本按 body 变化；同输入重跑候选集合/顺序/默认勾选完全一致。
5. **降级/异步/实时上限**：LLM 未配置/失败/超时 → done+degraded 且 T0 候选可用；queued/running 状态与启动恢复；队列满/并发冲突 reason；SSE 事件只含摘要且受 64 KiB/500 rune/refs≤20 限制。

---

## Pre-flight（一次性，不算任务）

- [ ] 确认在 `dev` 且工作区干净：`git status --short`；确认 spec 提交 `e17078c` 在历史中。
- [ ] 创建分支：`git switch -c feat/p4-code-intelligence`。
- [ ] 启动数据库：`powershell -File scripts/db.ps1 status`，必要时 `start`。
- [ ] 确认 `frontend/node_modules` 存在；否则 `cd frontend; pnpm install`。
- [ ] 确认基线绿：`powershell -File scripts/test.ps1`（若耗时，至少 Go 单测 + 前端 typecheck/vitest）。
- [ ] 确认 `.env` 含 `EZTEST_TEST_DATABASE_URL`（`eztest_test`）、可写 `.data/`；`llm_base_url/key/model_name` 非空（真实验收 gated）。
- [ ] 确认端口 `13333/6868/13334/13335` 空闲；`git --version` 可用。
- [ ] 记录当前 `git rev-parse HEAD` 作为 plan 基线。

---

## Shared Interfaces（由 T2/T6 定义；后续任务消费）

```go
// backend/internal/codeintel/domain
const (
    KindFile = "file"; KindPage = "page"; KindRoute = "route"; KindAPI = "api"
    KindClientCall = "client_call"; KindComponent = "component"
    KindModule = "module"; KindSymbol = "symbol"

    SourceGit = "git"; SourceAI = "ai"
    ConfidenceHigh = "high"; ConfidenceMedium = "medium"; ConfidenceLow = "low"

    RelContains = "contains"; RelImports = "imports"; RelDefines = "defines"
    RelCalls = "calls"; RelRenders = "renders"; RelRouteToAPI = "route_to_api"

    RunQueued = "queued"; RunRunning = "running"; RunDone = "done"; RunFailed = "failed"

    SubjectVersion = "version"; SubjectRequirement = "requirement"; SubjectAppModel = "app_model"

    CandidateRequirement = "requirement"; CandidateAppModel = "app_model"
    CandidateDirect = "direct"; CandidateContract = "contract"
    CandidateRelation = "relation"; CandidateAI = "ai"

    UnmappedDeleted = "deleted_file"; UnmappedUnsupported = "unsupported_content"
    UnmappedNoFact = "no_fact_or_anchor"; UnmappedAIUnavailable = "ai_unavailable"
    UnmappedContract = "unmatched_contract"
)

type Evidence struct {
    File        string `json:"file"`
    StartLine   int    `json:"startLine,omitempty"`
    EndLine     int    `json:"endLine,omitempty"`
    ExcerptHash string `json:"excerptHash,omitempty"`
}

type CodeEntity struct {
    ID          int64
    RepoID      int64
    Commit      string
    Kind        string
    Path        string
    Name        string
    StartLine   *int
    EndLine     *int
    Signature   string
    BlobHash    string
    Source      string
    Confidence  string
    Evidence    []Evidence
    Meta        []byte // JSONB
    FactKey     *string
    FactVersion *int
    AnalysisID  *int64
    CreatedAt   time.Time
    UpdatedAt   time.Time
}

type CodeEdge struct {
    ID, RepoID            int64
    Commit                string
    SrcEntityID, DstEntityID int64
    Rel, Source, Confidence  string
    Evidence              []Evidence
    Meta                  []byte
    FactKey               *string
    FactVersion           *int
    AnalysisID            *int64
    CreatedAt             time.Time
}

type ChangedFile struct {
    Path, Status       string
    OldPath            *string
    Additions, Deletions int
    Binary             bool
    HeadBlobHash       string
}

type CodeChangeSet struct {
    ID, RepoID             int64
    BaseCommit, HeadCommit string
    Stats                  []byte
    Files                  []ChangedFile
    Truncated              bool
    CreatedAt              time.Time
}

type FactVersionRef struct { FactKey string; Version int }

type AnalysisError struct { Code, Path, Message string }

type UnmappedChange struct { Path, Status, Reason, Suggestion string }

type CodeAnalysis struct {
    ID, VersionID, RepoID int64
    BaseCommit, HeadCommit string
    ChangeSetID           *int64
    Status                string
    Degraded              bool
    Model, PromptVersion  string
    AnalysisVersion       int
    FactVersions          []FactVersionRef
    Stats                 []byte
    Errors                []AnalysisError
    Unmapped              []UnmappedChange
    CreatedBy             int64
    CreatedAt             time.Time
    StartedAt, FinishedAt *time.Time
}

type ImpactCandidate struct {
    ID, AnalysisID      int64
    SubjectKind         string
    SubjectID           int64
    Reason              string
    Confidence          string
    Source              string
    Evidence            []Evidence
    Selected            bool
    SelectionUpdatedBy  *int64
    SelectionUpdatedAt  *time.Time
}

type CodeAnchor struct {
    ID, VersionID, RepoID int64
    SubjectKind           string
    SubjectID             int64
    FilePath              string
    StartLine, EndLine    *int
    EntityID              *int64
    CommitSHA             *string
    Confidence            string
    CreatedByKind         string
    CreatedBy             int64
    CreatedAt             time.Time
}

type IndexRun struct {
    ID, RepoID   int64
    CommitSHA    string
    Source       string
    Status       string
    Stats        []byte
    Errors       []AnalysisError
    StartedAt, FinishedAt *time.Time
    CreatedBy    int64
    CreatedAt    time.Time
}

type TreeEntry struct { Path, Type, GitObjectID, Mode string; Size int64; Unsupported bool }
type TreePage struct { Commit string; Entries []TreeEntry; NextCursor string; HasMore bool }
type FileWindow struct {
    Commit, Path string
    StartLine, EndLine, TotalLines int
    Truncated, Binary, Unsupported bool
    Content string
}
type FileDiff struct { BaseCommit, HeadCommit, Path, Status string; Binary, Truncated bool; Patch string }
type ChangeSetData struct {
    ChangeSetID *int64
    Stats       []byte
    Files       []ChangedFile
    Truncated   bool
}
type CommitInfo struct { SHA, Message, Author string; CommittedAt time.Time }
type CreateAnchorParams struct { SubjectKind string; SubjectID int64; FilePath string; StartLine, EndLine *int }
type EntityFilter struct { Commit, Kind, Path, Query string; Limit int; Cursor string }
type AnchorFilter struct { SubjectKind string; SubjectID *int64 }

// Repository interfaces (postgres 实现；方法名固定)
type ChangeSetRepository interface {
    GetOrCreate(ctx context.Context, cs CodeChangeSet) (CodeChangeSet, bool, error)
    GetByID(ctx context.Context, id int64) (CodeChangeSet, error)
}
type IndexRunRepository interface {
    Create(ctx context.Context, r IndexRun) error
    Update(ctx context.Context, r IndexRun) error
    GetByID(ctx context.Context, repoID, id int64) (IndexRun, error)
    GetLatest(ctx context.Context, repoID int64) (IndexRun, error)
    RecoverStale(ctx context.Context, before time.Time) (int64, error)
}
type EntityRepository interface {
    Upsert(ctx context.Context, e CodeEntity) error
    UpsertFile(ctx context.Context, repoID int64, commit string, files []CodeEntity) error
    GetByFactKey(ctx context.Context, repoID int64, commit, factKey string) (CodeEntity, error)
    List(ctx context.Context, repoID int64, f EntityFilter) ([]CodeEntity, string, bool, error)
    ListByPaths(ctx context.Context, repoID int64, commit string, paths []string) ([]CodeEntity, error)
    DeleteFact(ctx context.Context, repoID int64, commit, factKey string) error
}
type EdgeRepository interface {
    ReplaceFactEdges(ctx context.Context, repoID int64, commit, factKey string, version int, edges []CodeEdge) error
    ListByEntity(ctx context.Context, repoID int64, commit string, entityID int64) ([]CodeEdge, error)
    ListByRel(ctx context.Context, repoID int64, commit, rel string) ([]CodeEdge, error)
}
type FactVersionRepository interface {
    Current(ctx context.Context, repoID int64, commit, factKey string) (*CodeEntity, int, error) // entity + current version
    Create(ctx context.Context, repoID int64, commit, factKey, model, prompt string, version int, body []byte, analysisID int64) error
    Supersede(ctx context.Context, repoID int64, commit, factKey string, before int, at time.Time) error
}
type CacheRepository interface {
    Get(ctx context.Context, repoID int64, commit, path, fileHash, model, prompt string) (*CachedFile, bool, error)
    Upsert(ctx context.Context, c CachedFile) error
}
type AnalysisRepository interface {
    Create(ctx context.Context, a CodeAnalysis) error
    Update(ctx context.Context, a CodeAnalysis) error
    GetByID(ctx context.Context, versionID, id int64) (CodeAnalysis, error)
    GetLatest(ctx context.Context, versionID int64) (CodeAnalysis, error)
    FindReusable(ctx context.Context, versionID, repoID int64, base, head, model, prompt string) (*CodeAnalysis, error)
    HasActive(ctx context.Context, versionID int64) (bool, error)
    NextAnalysisVersion(ctx context.Context, versionID int64, base, head string) (int, error)
    RecoverStale(ctx context.Context, before time.Time) (int64, error)
    UpdateCandidateSelection(ctx context.Context, analysisID, candidateID, actorID int64, selected bool) error
}
type CandidateRepository interface {
    Replace(ctx context.Context, analysisID int64, items []ImpactCandidate) error
    List(ctx context.Context, analysisID int64) ([]ImpactCandidate, error)
    Get(ctx context.Context, analysisID, candidateID int64) (ImpactCandidate, error)
}
type AnchorRepository interface {
    Create(ctx context.Context, a CodeAnchor) error
    Delete(ctx context.Context, versionID, id int64) error
    Get(ctx context.Context, versionID, id int64) (CodeAnchor, error)
    List(ctx context.Context, versionID int64, f AnchorFilter) ([]CodeAnchor, error)
    ListByPaths(ctx context.Context, versionID int64, paths []string) ([]CodeAnchor, error)
}
type CachedFile struct {
    RepoID int64; Commit, Path, FileHash, Model, PromptVersion string
    Facts []byte; Stats []byte; CreatedAt, LastUsedAt time.Time
}

// application 端口（infrastructure 适配）
type GitReader interface {
    Head(ctx context.Context, localPath, branch string) (string, error)
    IsAncestor(ctx context.Context, localPath, base, head string) (bool, error)
    Tree(ctx context.Context, localPath, commit, prefix string, include, exclude []string, cursor string, limit int) (TreePage, error)
    File(ctx context.Context, localPath, commit, path string, startLine, endLine int) (FileWindow, error)
    ReadAll(ctx context.Context, localPath, commit, path string, maxBytes int) ([]byte, bool, error)
    Diff(ctx context.Context, localPath, base, head string) (ChangeSetData, error)
    FileDiff(ctx context.Context, localPath, base, head, path string, maxBytes int) (FileDiff, error)
    Commit(ctx context.Context, localPath, sha string) (CommitInfo, error)
}
type LLMCompleter interface {
    Complete(ctx context.Context, system, user string) (string, error)
    Model() string
}
type RepoInfo struct {
    ID int64; Key, Name, LocalPath, FixedBranch string
    IncludeGlobs, ExcludeGlobs []string
    Status string; LastIndexedAt *time.Time
}
type RepoCatalog interface { Get(ctx context.Context, repoID int64) (RepoInfo, error) }
type IndexStateWriter interface { MarkIndexed(ctx context.Context, repoID int64, commit string, at time.Time) error }
type AppModelNode struct { ID, SystemID int64; Kind, Key, Name, Path, Method string }
type AppModelCatalog interface { ListBySystem(ctx context.Context, systemID int64, kinds []string) ([]AppModelNode, error) }
type VersionInfo struct {
    ID, SystemID, RepoID int64
    HasSystem, HasRepo     bool
    Branch, BaselineCommit string
}
type RequirementInfo struct { ID int64; Title, Description, Status string }
type RelationInfo struct { FromID, ToID int64; Type string }
type VersionCatalog interface {
    Get(ctx context.Context, versionID int64) (VersionInfo, error)
    ListRequirements(ctx context.Context, versionID int64) ([]RequirementInfo, error)
    ListRelations(ctx context.Context, versionID int64) ([]RelationInfo, error)
}
type ChangedEvent struct { AnalysisID int64; BaseCommit, HeadCommit string; Stats []byte; Paths []string }
type ImpactEvent struct {
    AnalysisID, AnalysisVersion int64
    Status string; Degraded bool
    Counts map[string]int
    Paths  []string
}
type ImpactNotifier interface {
    Changed(ctx context.Context, versionID int64, e ChangedEvent) error
    ImpactComputed(ctx context.Context, versionID int64, e ImpactEvent) error
}
type AuditRecorder interface {
    Record(ctx context.Context, actorID int64, action, subject string, detail map[string]any) error
}
type IDGenerator interface { Next() int64 }
type Clock interface { Now() time.Time }

// application 服务（T8/T10/T13 产出；handlers/tools 消费）
type IndexService struct{ /* deps */ }
func (s *IndexService) Start(ctx context.Context, repoID, actorID int64) (IndexRun, error)
func (s *IndexService) Get(ctx context.Context, repoID, runID int64) (IndexRun, error)
func (s *IndexService) GetLatest(ctx context.Context, repoID int64) (IndexRun, error)
func (s *IndexService) Run(ctx context.Context, runID int64) error

type QueryService struct{ /* deps */ }
func (s *QueryService) Tree(ctx context.Context, repoID int64, prefix, commit, cursor string, limit int) (TreePage, error)
func (s *QueryService) File(ctx context.Context, actorID, repoID int64, commit, path string, startLine, endLine int) (FileWindow, error)
func (s *QueryService) Diff(ctx context.Context, repoID int64, base, head string, limit int, cursor string) (ChangeSetData, error)
func (s *QueryService) FileDiff(ctx context.Context, repoID int64, base, head, path string) (FileDiff, error)
func (s *QueryService) Facts(ctx context.Context, repoID int64, f EntityFilter) ([]CodeEntity, string, bool, error)
func (s *QueryService) Search(ctx context.Context, repoID int64, commit, q, kind string, limit int, cursor string) ([]CodeEntity, string, bool, error)
func (s *QueryService) VersionChanges(ctx context.Context, versionID int64, headCommit string) (ChangeSetData, string, string, error) // data, base, head

type AnalysisService struct{ /* deps */ }
func (s *AnalysisService) Start(ctx context.Context, versionID, actorID int64, headCommit string, force bool) (CodeAnalysis, bool, error)
func (s *AnalysisService) Get(ctx context.Context, versionID, analysisID int64) (CodeAnalysis, error)
func (s *AnalysisService) GetLatest(ctx context.Context, versionID int64) (CodeAnalysis, error)
func (s *AnalysisService) ListCandidates(ctx context.Context, analysisID int64) ([]ImpactCandidate, error)
func (s *AnalysisService) PatchCandidate(ctx context.Context, versionID, analysisID, candidateID, actorID int64, selected bool) (ImpactCandidate, error)
func (s *AnalysisService) Run(ctx context.Context, analysisID int64) error

type ImpactInput struct {
    Version VersionInfo
    Analysis CodeAnalysis
    Requirements []RequirementInfo
    Relations []RelationInfo
    AppModels []AppModelNode
    Anchors []CodeAnchor
    ChangedFiles []ChangedFile
    FactsAtHead []CodeEntity
    Edges []CodeEdge
    Degraded bool
}
type ImpactResult struct {
    Candidates []ImpactCandidate
    Unmapped   []UnmappedChange
    Stats      []byte
}
type ImpactService struct{ /* deps */ }
func (s *ImpactService) Plan(ctx context.Context, in ImpactInput) (ImpactResult, error)

type AnchorService struct{ /* deps */ }
func (s *AnchorService) Create(ctx context.Context, versionID, actorID int64, p CreateAnchorParams) (CodeAnchor, error)
func (s *AnchorService) Delete(ctx context.Context, versionID, anchorID int64) error
func (s *AnchorService) List(ctx context.Context, versionID int64, f AnchorFilter) ([]CodeAnchor, error)

type Runner interface {
    RunIndex(ctx context.Context, runID int64) error
    RunAnalysis(ctx context.Context, analysisID int64) error
}
type Enqueuer interface { Enqueue(job Job) error }
type Job struct { Kind string; ID int64 } // Kind: "index" | "analysis"
type Worker struct{ /* deps */ }
func (w *Worker) Enqueue(job Job) error // ErrQueueFull
func (w *Worker) SetRunner(r Runner)     // 组装根在全部服务构造后调用，避免循环依赖
func (w *Worker) Start()
func (w *Worker) Stop(ctx context.Context)
```

- 所有应用服务构造函数由 `cmd/eztest` 注入依赖，签名在任务 `Interfaces → Produces` 中给出。
- 时间/ID 复用 `platform/clock`、`platform/id`；错误用 `platform/errors` 包装为 HTTP 错误（仅 handlers）。
- 测试替身（fake repo/port）与生产实现同包或 `_test.go`；集成测试放 `codeintel/integration/`。

---

### Task 1: 设计决策与文档同步

**Wave:** W0 · **并发:** 可并发（文档轨） · **依赖:** 无
**写作用域:** `docs/design/`、`Agent.md`、`.env.example`

**Files:**
- Modify: `docs/design/system-blueprint.md`（§3.2 末尾追加 D32/D33；§7.3 补 P4 实施落点）
- Modify: `docs/design/code-intelligence.md`（头部状态；§13 DDL 按 D29/D32 定稿标注；新增 §14 P4 实施细化）
- Modify: `Agent.md`（§6.1 目录树新增 codeintel/前端 feature；§6.5 后补 P4 段落）
- Modify: `.env.example`（新增 8 个 `EZTEST_CODEINTEL_*` 键）

**Interfaces:**
- Consumes: spec §4.1/§5/§13 原文。
- Produces: 文档与 spec 一致；后续任务以 `.env.example` 键名与 `Agent.md` 目录树为准。

- [ ] **Step 1: 写失败检查**

```powershell
$d32 = Select-String -Path docs/design/system-blueprint.md -Pattern 'D32 P4 代码理解'
$tree = Select-String -Path Agent.md -Pattern 'codeintel/'
$env  = Select-String -Path .env.example -Pattern 'EZTEST_CODEINTEL_MAX_CHANGED_FILES'
if ($d32 -or $tree -or $env) { exit 1 }
```

- [ ] **Step 2: 运行检查确认失败**

Run: 上述脚本
Expected: 无输出、退出 0（三项均不存在）；若已存在则跳过重复写入。

- [ ] **Step 3: 按 spec §13.1/§13.2/§13.3 写入**

- 蓝图 §3.2 表格追加 D32、D33（原文见 spec §13.1/§13.2，一字不改）。
- 蓝图 §7.3 末尾补一段：P4 落点（T0+T1、影响对象需求/应用模型/unmapped、异步 run、`/api/code/*` 与 `/api/versions/{id}/code/*`、降级）。
- `code-intelligence.md`：头部注明"实施细化见 P4 spec"; §13 DDL 段落加说明"BIGSERIAL 为历史示例，实施按 D29/D32 使用 BIGINT 雪花 + 9 表；不建跨上下文 FK"; 新增 §14 列 P4 落点（证据按 commit、影响对象、缓存/版本、API/事件、降级、明确不做）。
- `Agent.md` §6.1 树新增 `backend/internal/codeintel/` 四层与 `frontend/src/features/code-intel/`；补 P4 段落（上下文名/端口/API 前缀/9 表）。
- `.env.example` 追加 spec §4.1 的 8 个键与注释。

- [ ] **Step 4: 运行检查确认通过**

```powershell
Select-String -Path docs/design/system-blueprint.md -Pattern 'D32 P4 代码理解|D33 P4 缓存'
Select-String -Path Agent.md -Pattern 'codeintel/'
Select-String -Path .env.example -Pattern 'EZTEST_CODEINTEL_MAX_CHANGED_FILES'
```

Expected: 均至少 1 条匹配；`git diff --check` 无空白错误。

- [ ] **Step 5: 提交**

```bash
git add docs/design/system-blueprint.md docs/design/code-intelligence.md Agent.md .env.example
git commit -m "docs: record D32/D33 and P4 code intelligence landing notes"
```

---

### Task 2: 配置 + 领域模型 + 迁移

**Wave:** W0 · **并发:** 可并发（后端地基轨） · **依赖:** 无
**写作用域:** `backend/internal/config/`、`backend/internal/codeintel/domain/`、`backend/internal/db/migrations/`

**Files:**
- Modify: `backend/internal/config/config.go`（8 个字段 + 校验）
- Modify: `backend/internal/config/config_test.go`（默认值/越界）
- Create: `backend/internal/codeintel/domain/{entity.go,edge.go,change_set.go,analysis.go,anchor.go,evidence.go,contract.go,limits.go,filters.go,errors.go}`
- Create: `backend/internal/codeintel/domain/domain_test.go`
- Create: `backend/internal/db/migrations/codeintel_0001_code_facts.sql`
- Create: `backend/internal/db/migrations/codeintel_0002_code_impact.sql`
- Modify: `backend/internal/db/migrations/order.txt`（末尾追加两行）

**Interfaces:**
- Consumes: spec §4.1/§4.2/§5 DDL；`platform/id`/`platform/errors` 既有类型。
- Produces（供全部后端任务）：
  - `config.Config` 新字段：`CodeIntelMaxChangedFiles, CodeIntelMaxFileBytes, CodeIntelMaxIndexFiles, CodeIntelAnalysisTimeoutMS, CodeIntelMaxConcurrentAnalyses, CodeIntelPromptVersion, CodeIntelMaxExcerptLines, CodeIntelMaxDiffBytes`
  - `domain` 全部类型/常量/`FactKey(kind, name, path string) (string, error)` / `NormalizeMethodPath(method, path string) (string, string)` / `SortCandidates([]ImpactCandidate)` / `DefaultSelected(confidence string) bool` / `domain.Err*`（`ErrNotFound`, `ErrConflict`, `ErrInvalid`）
  - `domain.Limits` 常量见 spec §4.2。

- [ ] **Step 1: 写失败测试**

`config_test.go`（复用现有 `Load(getenv)` 测试辅助）：

```go
func TestLoadCodeIntelDefaults(t *testing.T) {
    cfg := mustLoad(t, map[string]string{}) // 现有 helper 风格；仅补必要 env
    if cfg.CodeIntelMaxChangedFiles != 20 || cfg.CodeIntelMaxFileBytes != 65536 ||
        cfg.CodeIntelMaxIndexFiles != 5000 || cfg.CodeIntelAnalysisTimeoutMS != 180000 ||
        cfg.CodeIntelMaxConcurrentAnalyses != 1 || cfg.CodeIntelPromptVersion != "p4.1" ||
        cfg.CodeIntelMaxExcerptLines != 40 || cfg.CodeIntelMaxDiffBytes != 262144 {
        t.Fatalf("unexpected codeintel defaults: %+v", cfg)
    }
}
func TestLoadCodeIntelRejectsOutOfRange(t *testing.T) {
    for _, kv := range []struct{ k, v string }{
        {"EZTEST_CODEINTEL_MAX_CHANGED_FILES", "0"},
        {"EZTEST_CODEINTEL_MAX_FILE_BYTES", "999999999"},
        {"EZTEST_CODEINTEL_PROMPT_VERSION", "bad version!"},
        {"EZTEST_CODEINTEL_MAX_CONCURRENT_ANALYSES", "5"},
    } { if _, err := Load(mapEnv(kv.k, kv.v)); err == nil { t.Fatalf("%s=%s should fail", kv.k, kv.v) } }
}
```

`domain_test.go`：

```go
func TestFactKey(t *testing.T) {
    if got, _ := FactKey(KindRoute, "orders", ""); got != "route:/orders" { t.Fatalf("route key %q", got) }
    if got, _ := FactKey(KindAPI, "post /api/refund", ""); got != "api:POST /api/refund" { t.Fatalf("api key %q", got) }
    if got, _ := FactKey(KindComponent, "OrdersPage", "src/pages/OrdersPage.tsx"); got != "component:src/pages/OrdersPage.tsx#OrdersPage" { t.Fatalf("component key %q", got) }
}
func TestNormalizeMethodPath(t *testing.T) {
    for _, tc := range []struct{ in, method, path string }{
        {"https://x.test/api/orders/42?q=1", "POST", "/api/orders/{}"},
        {"post /api/refund/{id}", "", "/api/refund/{}"},
        {"GET /api//refund/", "", "/api/refund"},
        {"get /api/users/3f2a...uuid", "", "/api/users/{}"},
    } { /* assert NormalizeMethodPath 输出 */ }
}
func TestCandidateOrderAndDefaultSelection(t *testing.T) {
    // direct/high < contract/high < relation/medium < ai/low；low 默认 false
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd backend; go test ./internal/config ./internal/codeintel/domain`
Expected: 编译失败/字段不存在/`FactKey` undefined。

- [ ] **Step 3: 实现**

- 按 spec §4.1 增加配置字段与 `parseBoundedInt`/字符串校验（prompt version 正则 `^[a-z0-9.-]{1,32}$`）。
- 按 spec §5 逐字复制两个迁移 SQL（雪花 BIGINT、无序列；`order.txt` 末尾追加文件名的相对路径两行）。
- 实现 `domain` 类型/常量/`limits.go`/`errors.go`；`FactKey`/`NormalizeMethodPath`（spec §6.5 规则）/`SortCandidates`（source→confidence→kind→id）/`DefaultSelected`；`ValidateSubjectKind`（version/requirement/app_model）、`ValidateEvidenceRange`。
- 不在 domain 引入 pgx/HTTP/LLM。

- [ ] **Step 4: 运行测试确认通过**

Run: `cd backend; go test ./internal/config ./internal/codeintel/domain`
Expected: PASS；`go vet ./internal/codeintel/domain` 通过。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/config backend/internal/codeintel/domain backend/internal/db/migrations
git commit -m "feat: add P4 codeintel config, domain model and migrations"
```

---

### Task 3: 前端类型与 API 客户端

**Wave:** W0 · **并发:** 可并发（前端地基轨） · **依赖:** 无
**写作用域:** `frontend/src/features/code-intel/`

**Files:**
- Create: `frontend/src/features/code-intel/types.ts`
- Create: `frontend/src/features/code-intel/api.ts`
- Create: `frontend/src/features/code-intel/api.test.ts`

**Interfaces:**
- Consumes: `@/lib/api-client` 的 `apiGet/apiGetPage/apiPost/apiPatch/apiDelete`；spec §7 DTO。
- Produces（供 T9/T11/T12）：
  - `RepoSummary`, `IndexRun`, `TreePage`, `FileWindow`, `FileDiff`, `ChangeList`, `Analysis`, `Candidate`, `Anchor`, `UnmappedChange`, `Evidence`（ID 见 spec §7.1：codeintel/主体为 string，repoId/createdBy 为 number）
  - `getCodeRepos()`, `startIndex(repoId)`, `getLatestIndexRun(repoId)`, `getTree(repoId, params)`, `getFile(repoId, params)`, `getDiff(repoId, params)`, `getFileDiff(repoId, params)`, `getFacts(repoId, params)`, `searchFacts(repoId, params)`, `getVersionChanges(versionId, head?)`, `startAnalysis(versionId, body)`, `getLatestAnalysis(versionId)`, `getAnalysis(versionId, id)`, `patchCandidate(versionId, analysisId, candidateId, selected)`, `listAnchors(versionId, params)`, `createAnchor(versionId, body)`, `deleteAnchor(versionId, anchorId)`

- [ ] **Step 1: 写失败测试**

`api.test.ts`（mock `@/lib/api-client`，沿用 code-repos/api.test 模式）：

```ts
it("serialises ids without Number() and hits the right paths", async () => {
  await startAnalysis("1844", { headCommit: "def", force: false });
  expect(apiPost).toHaveBeenCalledWith("/api/versions/1844/code/analyses", { headCommit: "def", force: false });
  const anchor = await createAnchor("1844", { subjectKind: "requirement", subjectId: "9001", filePath: "src/api/refund.ts" });
  expect(anchor).toBeDefined();
  await patchCandidate("1844", "7001", "7002", true);
  expect(apiPatch).toHaveBeenCalledWith("/api/versions/1844/code/analyses/7001/candidates/7002", { selected: true });
});
it("keeps repoId as number and analysis ids as string", () => { /* type-level + runtime assertions */ });
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd frontend; pnpm vitest run src/features/code-intel/api.test.ts`
Expected: FAIL（模块不存在）。

- [ ] **Step 3: 实现**

- `types.ts` 按 spec §7.2/§7.3 DTO 定义；`repoId: number`，所有 codeintel ID/`subjectId` 用 `string`；时间 `string`。
- `api.ts` 薄封装：`apiGetPage` 用于分页列表；`apiPost`/`apiPatch`/`apiDelete`；路径编码 `encodeURIComponent(id)`；不把 string id 转 number。

- [ ] **Step 4: 运行测试确认通过**

Run: `cd frontend; pnpm vitest run src/features/code-intel/api.test.ts && pnpm typecheck`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/features/code-intel
git commit -m "feat: add codeintel frontend types and api client"
```

---

### Task 4: 只读 Git 适配器（gitx）

**Wave:** W1 · **并发:** 可并发（gitx 轨） · **依赖:** T2
**写作用域:** `backend/internal/codeintel/infrastructure/gitx/`

**Files:**
- Create: `backend/internal/codeintel/infrastructure/gitx/{reader.go,diff.go,paths.go,command.go}`
- Create: `backend/internal/codeintel/infrastructure/gitx/{reader_test.go,diff_test.go,paths_test.go}`

**Interfaces:**
- Consumes: `domain.TreePage/TreeEntry/FileWindow/FileDiff/ChangeSetData/ChangedFile/CommitInfo`；spec §6.1。
- Produces: `func NewGitReader(cfg GitConfig) *GitReader`，实现 `application.GitReader` 的全部方法；`GitConfig{CommandTimeout time.Duration, MaxTreeEntries, MaxReadLines int}`。

- [ ] **Step 1: 写失败测试**

`reader_test.go` 用 `newTestRepo(t)`（`git init` + 固定作者/时间 + 两个 commit，含 `src/api/refund.ts`、`src/utils/format.ts`、含空格/中文文件名、`assets/logo.png`、第二 commit rename/delete）：

```go
func TestTreeFiltersAndHashes(t *testing.T) {
    r := NewGitReader(GitConfig{CommandTimeout: 5 * time.Second})
    page, err := r.Tree(ctx, repo, commit1, "", []string{"src/**"}, []string{"assets/**"}, "", 100)
    // 断言 entries 仅 src、按 path 升序、blob entry 的 GitObjectID 非空且 Unsupported=false
}
func TestDiffStatuses(t *testing.T) {
    cs, err := r.Diff(ctx, repo, commit1, commit2)
    // 断言新增/修改/删除/重命名状态与管理；binary 文件 Binary=true；path 解析正确（-z 特殊文件名）
}
func TestFileWindowCRLFAndUTF8(t *testing.T) {
    // 读取含 CRLF 文件第 2-3 行；断言 content 不含 \r，start/end/totalLines 正确；超限 truncated=true
}
func TestRejectsEscapingPath(t *testing.T) {
    // "../secret", "/etc/passwd", "a\\b" 均返回 ErrInvalidPath；symlink/submodule 返回 unsupported
}
func TestHeadAndAncestor(t *testing.T) {
    // Head 返回固定分支 tip；IsAncestor(base, head)=true；IsAncestor(head, base)=false
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd backend; go test ./internal/codeintel/infrastructure/gitx`
Expected: FAIL（`NewGitReader` undefined）。

- [ ] **Step 3: 实现**

- 命令 argv 直接 `exec.CommandContext`，`GIT_OPTIONAL_LOCKS=0`、`--no-ext-diff`、`-z`；单命令 10s 超时；stdout/stderr 有界（≤4 MiB，超出错误）。
- 实现 spec §6.1 的 Tree（`ls-tree -r -t -l -z`，include/exclude、cursor=last path、limit≤200）、File（`git show commit:path`，UTF-8、行切片、CRLF 归一、64 KiB/400 行上限）、ReadAll（≤maxBytes，返回 truncated）、Diff（raw+numstat 两次调用按 path 合并）、FileDiff（`--unified=3`，≤maxBytes）、Commit（`show -s --format=%H%x00%ct%x00%an%x00%s`）、Head/IsAncestor。
- 路径安全：slash 归一、拒绝绝对/`..`/反斜杠逃逸、根内校验；symlink/submodule/binary 标记。

- [ ] **Step 4: 运行测试确认通过**

Run: `cd backend; go test ./internal/codeintel/infrastructure/gitx`
Expected: PASS；`go vet ./internal/codeintel/infrastructure/gitx`。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/codeintel/infrastructure/gitx
git commit -m "feat: add read-only codeintel git adapter"
```

---

### Task 5: PostgreSQL 仓储（9 表）

**Wave:** W1 · **并发:** 可并发（postgres 轨） · **依赖:** T2
**写作用域:** `backend/internal/codeintel/infrastructure/postgres/`、`backend/internal/codeintel/integration/`

**Files:**
- Create: `backend/internal/codeintel/infrastructure/postgres/{change_set_repo.go,index_run_repo.go,entity_repo.go,edge_repo.go,fact_repo.go,cache_repo.go,analysis_repo.go,candidate_repo.go,anchor_repo.go}`
- Create: `backend/internal/codeintel/integration/{migrate_integration_test.go,repositories_integration_test.go,helpers_test.go}`

**Interfaces:**
- Consumes: T2 的 domain 类型/仓储接口；`testutil.OpenTestDB`。
- Produces: `NewChangeSetRepository(pg *pgxpool.Pool) *ChangeSetRepo` 等 9 个构造函数，分别实现 Shared Interfaces 中的仓储接口；`helpers_test.go` 暴露 `seedRepoVersionRequirement(t, db)`（插入 P2 repo/版本/需求的最小行，供后续集成测试复用）。

- [ ] **Step 1: 写失败测试**

`migrate_integration_test.go`：

```go
func TestCodeIntelMigrations(t *testing.T) {
    db := testutil.OpenTestDB(t)
    for _, table := range []string{"code_change_sets","code_index_runs","code_entities","code_edges",
        "code_fact_versions","code_analysis_cache","code_analyses","code_impact_candidates","code_anchors"} {
        var n int; db.QueryRow(ctx, "SELECT count(*) FROM information_schema.tables WHERE table_name=$1", table).Scan(&n)
        if n != 1 { t.Fatalf("table %s missing", table) }
    }
    // order.txt 条目数 = 16；重复迁移无副作用（再次 Migrate 成功）
}
```

`repositories_integration_test.go`：

```go
func TestChangeSetGetOrCreate(t *testing.T) { /* 同 repo/base/head 二次调用返回同 id, created=false */ }
func TestEntityUpsertAndFactVersion(t *testing.T) { /* fact_key 唯一；version+1 更新 current */ }
func TestAnalysisReuseAndNextVersion(t *testing.T) { /* FindReusable 命中；NextAnalysisVersion=2；唯一约束 */ }
func TestCandidateSelection(t *testing.T) { /* UpdateCandidateSelection 写 updated_by/at */ }
func TestAnchorDuplicate(t *testing.T) { /* 同主体/文件/区间 → 冲突错误 */ }
func TestRecoverStaleRuns(t *testing.T) { /* running 超时 → failed(interrupted) */ }
```

- [ ] **Step 2: 运行测试确认失败**

Run: `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-integration.ps1 ./internal/codeintel/integration/...`
Expected: 编译失败/表不存在。

- [ ] **Step 3: 实现**

- 每仓储方法使用 `pgx`、显式列、`ON CONFLICT` 复用；不建跨上下文 FK；JSONB 用 `[]byte` 透传。
- `ChangeSetRepository.GetOrCreate` 用 `INSERT ... ON CONFLICT (repo_id, base_commit, head_commit) DO NOTHING` + SELECT，返回 `created`。
- `AnalysisRepository.FindReusable` 匹配 `(version_id, base, head, model, prompt_version, status='done')`；`NextAnalysisVersion = COALESCE(MAX,0)+1`；`HasActive` 匹配 queued/running。
- `EntityRepository.Upsert` 以 `(repo_id, commit_sha, fact_key)` 冲突更新（当前版本）；`UpsertFile` 批量。
- `FactVersionRepository.Current` 返回当前实体 + 版本；`Create`/`Supersede`。
- `RecoverStale` 更新 queued/running 且 `created_at/started_at < before` 的行（reason 写入 errors JSONB）。
- 种子 helper 只用于测试，不写入生产 seed。

- [ ] **Step 4: 运行测试确认通过**

Run: `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-integration.ps1 ./internal/codeintel/integration/...`
Expected: PASS（记录计数，如 6 passed）。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/codeintel/infrastructure/postgres backend/internal/codeintel/integration
git commit -m "feat: add codeintel postgres repositories"
```

---

### Task 6: 应用核心（端口/提示词/JSON 解析/证据校验）+ LLM 网关

**Wave:** W1 · **并发:** 可并发（应用核心轨） · **依赖:** T2
**写作用域:** `backend/internal/codeintel/application/`（仅 `ports.go`/`prompt.go`/`parser.go`/`evidence.go` 及其测试）、`backend/internal/codeintel/infrastructure/llm/`

**Files:**
- Create: `backend/internal/codeintel/application/{ports.go,prompt.go,prompt_test.go,parser.go,parser_test.go,evidence.go,evidence_test.go}`
- Create: `backend/internal/codeintel/infrastructure/llm/{gateway.go,gateway_test.go}`

**Interfaces:**
- Consumes: spec §6.2/§6.3/§8.3/§8.4；P0 `llm/application`（`ConfigService.GetDefault`、`ClientFactory`、`ChatRequest/Response`）、`platform/errors`。
- Produces（后续任务消费）：
  - `application` 包内全部端口接口（Shared Interfaces 的 `GitReader/LLMCompleter/RepoCatalog/IndexStateWriter/AppModelCatalog/VersionCatalog/ImpactNotifier/AuditRecorder`）
  - `const PromptVersionDefault = "p4.1"`、`func BuildFilePrompt(repoKey, commit, path string, content []byte) (system, user string)`、`func BuildImpactPrompt(in ImpactPromptInput) (system, user string)`
  - `func ParseFileAnalysis(raw string) (FileAnalysis, error)`；`type FileAnalysis struct{ ChangeSummary string; Facts []AIFact; Edges []AIEdge }`
  - `func HashExcerpt(content []byte, startLine, endLine int) (string, error)`；`func ValidateEvidence(content []byte, ev domain.Evidence, maxLines int) (domain.Evidence, error)`
  - `func NewLLMGateway(cfg LLMGatewayConfig) (*Gateway, error)`，实现 `LLMCompleter`（`Complete(ctx, system, user)` + `Model() string`）。

- [ ] **Step 1: 写失败测试**

```go
func TestParseFileAnalysisStrictJSON(t *testing.T) {
    // 接受 ```json 围栏；拒绝缺 facts 类型/负行号/未知 kind；合法 JSON 解析出 changeSummary/facts
}
func TestHashExcerptCRLFIsNormalised(t *testing.T) {
    content := []byte("a\r\nb\r\nc\n")
    h1, _ := HashExcerpt(content, 2, 3)
    h2, _ := HashExcerpt([]byte("a\nb\nc\n"), 2, 3)
    if h1 != h2 || !strings.HasPrefix(h1, "sha256:") { t.Fatalf("hash mismatch: %s %s", h1, h2) }
}
func TestValidateEvidenceRejectsOutOfRangeAndDeleted(t *testing.T) {
    // startLine=0 / endLine>totalLines / endLine-startLine>max → error；正常区间返回带 ExcerptHash 的 Evidence
}
func TestBuildFilePromptContainsSchemaAndPath(t *testing.T) {
    sys, user := BuildFilePrompt("web", "abc...", "src/a.ts", []byte("export const x = 1"))
    // 断言 system 含 "只读代码分析器"、user 含 path 与 JSON schema 关键字
}
func TestGatewayRequiresDefaultConfig(t *testing.T) {
    // 未配置默认 LLM → 返回错误（调用方随后降级）；正常配置 → Model() 返回配置模型名
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd backend; go test ./internal/codeintel/application ./internal/codeintel/infrastructure/llm`
Expected: FAIL（符号未定义）。

- [ ] **Step 3: 实现**

- `ports.go` 逐字实现 Shared Interfaces 中端口（不引入 pgx/HTTP；可 import `time/context/encoding/json`）。
- `prompt.go` 使用 spec §8.3/§8.4 原文（含 JSON schema）；`BuildFilePrompt` 给文件内容加 1-based 行号（≤2000 行，超出截断并标记）；不拼接其他文件。
- `parser.go`：容忍 code fence、截取首个平衡 JSON；必填字段/枚举/行号校验；未知字段忽略；超出上限截断并计数。
- `evidence.go`：按 spec §6.3 实现（按 `\n` 切分、去行尾 `\r`、1-based 闭区间、`sha256:<hex>`）；返回明确错误类型。
- `gateway.go`：读取默认 LLM 配置（`GetDefault`），构造 `llm.Client.Chat` 调用（system/user messages），重试/超时沿用配置；`Model()` 返回配置模型名；错误包装为 `platform` 语义错误（不泄露 Key/正文）。

- [ ] **Step 4: 运行测试确认通过**

Run: `cd backend; go test ./internal/codeintel/application ./internal/codeintel/infrastructure/llm`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/codeintel/application backend/internal/codeintel/infrastructure/llm
git commit -m "feat: add codeintel prompt, parser, evidence and llm gateway"
```

---

### Task 7: P2/P3/事件/审计适配 + `MarkIndexed`

**Wave:** W2 · **并发:** 可并发（适配器轨） · **依赖:** T6、T2
**写作用域:** `backend/internal/resource/application/`、`backend/internal/codeintel/infrastructure/{resource,version,eventing,audit}/`

**Files:**
- Modify: `backend/internal/resource/application/coderepo_service.go`（新增 `MarkIndexed`）
- Modify: `backend/internal/resource/application/coderepo_service_test.go`
- Create: `backend/internal/codeintel/infrastructure/resource/{catalog.go,index_state.go,catalog_test.go}`
- Create: `backend/internal/codeintel/infrastructure/version/{catalog.go,catalog_test.go}`
- Create: `backend/internal/codeintel/infrastructure/eventing/{publisher.go,publisher_test.go}`
- Create: `backend/internal/codeintel/infrastructure/audit/{recorder.go,recorder_test.go}`

**Interfaces:**
- Consumes: T6 端口接口；`resource/application` CodeRepo 服务与仓储；`version/application` Version/Requirement/Relation 服务；`eventing/application` 发布器；`audit/application`。
- Produces：
  - `resource/application`：`func (s *CodeRepoService) MarkIndexed(ctx context.Context, repoID int64, commit string, at time.Time) error`（校验 repo 存在/active；更新 `last_indexed_at`；不新增列）
  - `resource.NewCatalog(repoSvc) *Catalog` 实现 `RepoCatalog`/`AppModelCatalog`
  - `resource.NewIndexStateWriter(repoSvc) *IndexStateWriter` 实现 `IndexStateWriter`
  - `version.NewCatalog(versionSvc, requirementSvc, relationSvc) *Catalog` 实现 `VersionCatalog`（含 requirement relations）
  - `eventing.NewPublisher(pub) *Publisher` 实现 `ImpactNotifier`（channel=`version:{id}`，`code.changed`/`code.impact_computed`；summary/detail/refs 上限）
  - `audit.NewRecorder(rec) *Recorder` 实现 `AuditRecorder`（action `code.read`/`code.analyze`，不含正文）。

- [ ] **Step 1: 写失败测试**

```go
func TestCodeRepoServiceMarkIndexed(t *testing.T) { /* 写入 last_indexed_at；不存在/disabled → 领域错误；不改 last_commit */ }
func TestVersionCatalogMapsBindingAndRelations(t *testing.T) { /* 无绑定 → HasRepo=false；有绑定 → 字段正确；relations 两端 ID */ }
func TestImpactEventPayloadCaps(t *testing.T) { /* 21 个 paths → refs ≤20；summary ≤500 rune；detail ≤64KiB */ }
func TestAuditRecorderOmitsCodeContent(t *testing.T) { /* detail 不含 content/代码片段；含 path/commit/status */ }
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd backend; go test ./internal/resource/application ./internal/codeintel/infrastructure/...`
Expected: FAIL（方法/构造函数不存在）。

- [ ] **Step 3: 实现**

- 跨上下文只调用对方 application 接口；不 import 对方 infrastructure/interfaces。
- `MarkIndexed` 更新 `last_indexed_at` 使用仓储既有方法（检查 `code_repo_repository.go` 支持），不写其他字段。
- version catalog 只读；requirement/relation 通过 version application 查询（若无现成列表方法，新增只读方法并加测试，不改 P3 语义）。
- eventing：复用 P3 version publisher 的事件模型/通道；只放 id/计数/路径摘要；refs.code 去重排序，≤20。
- audit：复用 `audit` 应用记录器；detail 仅 repoId/commit/path/status/analysisId。

- [ ] **Step 4: 运行测试确认通过**

Run: `cd backend; go test ./internal/resource/application ./internal/codeintel/infrastructure/...`
Expected: PASS；`go test ./internal/version/...` 回归通过。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/resource backend/internal/codeintel/infrastructure
git commit -m "feat: add codeintel resource, version, eventing and audit adapters"
```

---

### Task 8: 索引服务 + 查询服务 + 异步 Worker

**Wave:** W3 · **并发:** 可并发（后端服务轨；application 串行链第 2 环） · **依赖:** T4、T5、T6、T7
**写作用域:** `backend/internal/codeintel/application/`（`index_service.go`/`query_service.go`/`run_worker.go` 及测试）、`backend/internal/codeintel/integration/index_integration_test.go`

**Files:**
- Create: `backend/internal/codeintel/application/{index_service.go,query_service.go,run_worker.go,index_service_test.go,query_service_test.go,run_worker_test.go}`
- Create: `backend/internal/codeintel/integration/index_integration_test.go`

**Interfaces:**
- Consumes: T4 `GitReader`、T5 9 仓储、T6 `AuditRecorder`/端口、T7 `RepoCatalog`/`IndexStateWriter`、`platform/id`/`clock`。
- Produces（handlers/T18 消费）：
  - `NewIndexService(deps IndexServiceDeps) *IndexService`；`Start/Get/GetLatest/Run` 签名见 Shared Interfaces。
  - `NewQueryService(deps QueryServiceDeps) *QueryService`；`Tree/File/Diff/DiffFile/Facts/Search/VersionChanges`。
  - `NewWorker(cfg WorkerConfig) *Worker`；`Enqueue(job Job) error`、`SetRunner(Runner)`、`Start()`、`Stop(ctx)`；`WorkerConfig{QueueSize int, Concurrency int, Timeout time.Duration, Logger *slog.Logger}`；`ErrQueueFull`。
  - `IndexServiceDeps{Git GitReader, Repos RepoCatalog, IndexRuns IndexRunRepository, Entities EntityRepository, ChangeSets ChangeSetRepository, State IndexStateWriter, Queue Enqueuer, Audit AuditRecorder, IDs IDGenerator, Clock Clock, MaxIndexFiles int, CommandTimeout time.Duration}`
  - `QueryServiceDeps{Git GitReader, Repos RepoCatalog, Entities EntityRepository, ChangeSets ChangeSetRepository, Versions VersionCatalog, Config QueryConfig}`

- [ ] **Step 1: 写失败测试**

```go
func TestIndexServiceStartCreatesQueuedRun(t *testing.T) { /* status=queued；已有 queued/running → ErrConflict */ }
func TestIndexServiceRunIndexesTreeAndMarksIndexed(t *testing.T) {
    // fake GitReader 返回 3 个文件 → Entities.UpsertFile 收到 3 条 kind=file/source=git；
    // State.MarkIndexed 调用一次；run done；stats.files=3
}
func TestIndexTruncatesAtMaxFiles(t *testing.T) { /* 超上限 → truncated=true、stats.truncatedFiles>0 */ }
func TestQueryServiceTreeAndFileAndDiff(t *testing.T) { /* 透传 gitx；file 读取写 audit；路径校验错误映射 */ }
func TestQueryServiceVersionChangesUsesBaseline(t *testing.T) { /* version 无绑定 → ErrInvalid；base=baseline；head 显式 */ }
func TestWorkerQueueFull(t *testing.T) { /* 队列 1 + 阻塞 runner → 第二次 Enqueue 返回 ErrQueueFull */ }
func TestWorkerRecovery(t *testing.T) { /* 启动时 RecoverStale 调用一次 */ }
```

`index_integration_test.go`：真实 DB + 临时 git repo（复用 T5 helper）验证迁移 16 表、索引 run 落库、`last_indexed_at` 更新。

- [ ] **Step 2: 运行测试确认失败**

Run: `cd backend; go test ./internal/codeintel/application ./internal/codeintel/integration`
Expected: FAIL（服务未定义）。

- [ ] **Step 3: 实现**

- `IndexService.Start`：校验 repo active → 解析固定分支 tip → 创建 run(queued) → `Queue.Enqueue`；队列满则 run 置 failed（reason=`analysis_queue_full`）并返回错误。
- `Run`：running → tree（include/exclude、limit）→ 构造 file entities（blob hash、size/mode meta）→ `UpsertFile` → `MarkIndexed` → done（stats/errors）；失败 failed；超时由 worker ctx 控制。
- `QueryService`：Tree/File/Diff/DiffFile/Facts/Search 透传 gitx/仓储并做上限/分页/audit；`VersionChanges` 读版本绑定，base=baseline，head=显式或固定分支 tip，返回 `ChangeSetData`（不落库）。
- `Worker`：有界 channel、并发受配置、每 job `context.WithTimeout`、panic recover、启动 `RecoverStale(now-10m)`；`Stop` 等待。

- [ ] **Step 4: 运行测试确认通过**

Run: `cd backend; go test ./internal/codeintel/application ./internal/codeintel/integration`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/codeintel/application backend/internal/codeintel/integration
git commit -m "feat: add codeintel index, query services and async worker"
```

---

### Task 9: 前端「代码与变更」容器 + 变更文件/diff

**Wave:** W2 · **并发:** 可并发（前端 Tab 轨） · **依赖:** T3
**写作用域:** `frontend/src/features/code-intel/`（容器/CommitHeader/ChangedFiles/DiffDrawer/轮询/空态）、`frontend/src/features/versions/{VersionTabs.tsx,VersionDetailPage.tsx,*.test.tsx}`

**Files:**
- Create: `frontend/src/features/code-intel/{CodeChangesTab.tsx,CommitHeader.tsx,ChangedFiles.tsx,DiffDrawer.tsx,useAnalysisPolling.ts,empty.tsx,CodeChangesTab.test.tsx}`
- Modify: `frontend/src/features/versions/{VersionTabs.tsx,VersionDetailPage.tsx,VersionDetailPage.test.tsx}`

**Interfaces:**
- Consumes: T3 `types/api`；`useVersionStream`；`api-client`；`ApiError`。
- Produces（供 T11/T12/T20）：`<CodeChangesTab versionId={string} />`；`VersionTabs` 新增 `code?: ReactNode` slot（从 `PLACEHOLDER_TABS` 移除 code）；容器内部状态/刷新 API 供子面板使用。

- [ ] **Step 1: 写失败测试**

```tsx
it("renders the six code tab states", () => { /* 未绑定/未索引/无变更/运行中/完成/降级分别断言文案与 CTA */ });
it("lists changed files and opens the unified diff drawer", async () => { /* mock getVersionChanges/getFileDiff；点击文件打开 dialog，含 patch 文本 */ });
it("stops polling after done/failed and on unmount", async () => { /* fake timers；断言调用次数不再增长 */ });
it("replaces the P4 placeholder in version detail", () => { /* 渲染 VersionDetailPage，code tab 出现真实容器，不再出现"阶段 P4" */ });
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd frontend; pnpm vitest run src/features/code-intel src/features/versions`
Expected: FAIL（组件不存在/占位断言仍在）。

- [ ] **Step 3: 实现**

- `CodeChangesTab`：加载 `getVersionChanges` → 渲染 `CommitHeader`（base→head、刷新索引 admin 仅 UI 提示、开始分析 CTA）；无绑定/无 baseline/无变更/未索引/降级/失败空态；不渲染用例/批次假数据。
- `ChangedFiles`：status 徽标（A/M/D/R/C/T）、additions/deletions、binary；点击 `DiffDrawer`。
- `DiffDrawer`：拉取 `getFileDiff`；`<pre>` 行着色；binary/truncated 提示；`role="dialog"` + ESC + 焦点管理。
- `useAnalysisPolling`：仅 queued/running 轮询 1.5s，`document.hidden` 暂停，完成/失败/卸载停止。
- `VersionTabs` 加 `code` slot，`VersionDetailPage` 传真实组件；更新受影响的 P3 测试（不再断言 code 占位）。

- [ ] **Step 4: 运行测试确认通过**

Run: `cd frontend; pnpm vitest run src/features/code-intel src/features/versions && pnpm typecheck`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/features/code-intel frontend/src/features/versions
git commit -m "feat: add code changes tab shell with diff viewer"
```

---

### Task 10: AnalysisService（T1/缓存/版本/物化/契约）

**Wave:** W4 · **并发:** 串行（application 链第 3 环） · **依赖:** T8
**写作用域:** `backend/internal/codeintel/application/`（`analysis_service.go`/`contract.go` 及测试）、`backend/internal/codeintel/integration/analysis_integration_test.go`

**Files:**
- Create: `backend/internal/codeintel/application/{analysis_service.go,contract.go,analysis_service_test.go,contract_test.go}`
- Create: `backend/internal/codeintel/integration/analysis_integration_test.go`

**Interfaces:**
- Consumes: T8 `IndexService`/`QueryService`/`Worker` 依赖；T5 仓储；T6 prompt/parser/evidence/`LLMCompleter`；T7 `VersionCatalog`/`ImpactNotifier`/`AuditRecorder`；`ImpactPlanner` 接口（本任务定义，T13 实现）。
- Produces：
  - `NewAnalysisService(deps AnalysisServiceDeps) *AnalysisService`；`Start/Get/GetLatest/ListCandidates/PatchCandidate/Run`（签名见 Shared Interfaces）。
  - `type ImpactPlanner interface { Plan(ctx context.Context, in ImpactInput) (ImpactResult, error) }`
  - `func MatchContracts(facts []domain.CodeEntity) []ContractPair`（spec §6.5；纯函数，供 T13/测试）
  - `AnalysisServiceDeps{Git GitReader, Repos RepoCatalog, Versions VersionCatalog, ChangeSets ChangeSetRepository, Entities EntityRepository, Edges EdgeRepository, Facts FactVersionRepository, Cache CacheRepository, Analyses AnalysisRepository, Candidates CandidateRepository, Anchors AnchorRepository, Queue Enqueuer, LLM LLMCompleter, Planner ImpactPlanner, Notifier ImpactNotifier, Audit AuditRecorder, IDs IDGenerator, Clock Clock, Config AnalysisConfig}`；`AnalysisConfig{MaxChangedFiles, MaxFileBytes, MaxIndexFiles, MaxExcerptLines, PromptVersion int/string, AnalysisTimeout time.Duration}`

- [ ] **Step 1: 写失败测试**

```go
func TestStartValidationAndReuse(t *testing.T) {
    // 无 repo 绑定 → ErrInvalid；base==head 允许；同输入已有 done → reused=true；
    // force → analysis_version=2；active → ErrConflict
}
func TestRunCacheHitSkipsLLM(t *testing.T) { /* fake cache hit → LLM 调用 0 次；facts 物化 */ }
func TestRunMaterialisesFactsAndVersions(t *testing.T) { /* 证据有效的 fact → entity source=ai+fact_key+version=1；同 body 重跑不新增版本 */ }
func TestRunEvidenceFailureDowngradesToLowClue(t *testing.T) { /* 行号越界/哈希不匹配 → confidence=low、meta.clue=true、无自动候选 */ }
func TestRunPartialFailureDegraded(t *testing.T) { /* 1/2 文件 LLM 失败 → done+degraded=true、errors 记录、成功文件事实入库 */ }
func TestRunAllLLMFailureDegradedT0Only(t *testing.T) { /* LLM 全部失败 → T0 候选由 Planner 产出、degraded=true */ }
func TestMatchContracts(t *testing.T) { /* POST /api/refund ↔ post /api/refund/{id} 归一匹配；不匹配计数 */ }
```

`analysis_integration_test.go`：真实 DB + 临时 git repo + fake LLM 文本 → 断言 analysis done、facts/edges/fact_versions、缓存复用（第二版本 cacheHits>0）、force 新版本、degraded 路径。

- [ ] **Step 2: 运行测试确认失败**

Run: `cd backend; go test ./internal/codeintel/application ./internal/codeintel/integration`
Expected: FAIL。

- [ ] **Step 3: 实现**

- `Start`：版本绑定/base/head 校验；同输入复用；active 409；`analysis_version=max+1`；创建 queued 并 enqueue；返回 `(analysis, reused, err)`。
- `Run`：T0 change set（GetOrCreate）→ 发布 `code.changed` → 选文件（有锚点优先、path 升序、上限）→ 缓存/LLM（cache key 含 model/prompt/file_hash）→ 解析→证据校验（commit 内容）→ 物化实体/边/fact 版本（事务）→ 调 `Planner.Plan`（传入事实/需求/关系/应用模型/锚点）→ 回写候选/unmapped/stats/status → 发布 `code.impact_computed`。
- `contract.go`：`NormalizeMethodPath`（domain）匹配；生成本任务内部契约边（T1 物化时写入 `route_to_api`）；不匹配计数。
- 降级：LLM 未配置/全部/部分失败→`done+degraded`；纯 T0 时 Planner 仍运行（fake/实现需支持 `Degraded`）。
- `PatchCandidate`：校验 analysis 属于 version、done、candidate 存在 → 更新 selected。

- [ ] **Step 4: 运行测试确认通过**

Run: `cd backend; go test ./internal/codeintel/application ./internal/codeintel/integration`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/codeintel/application backend/internal/codeintel/integration
git commit -m "feat: add codeintel analysis service with cache and fact versioning"
```

---

### Task 11: 前端事实 / 影响 / 未映射 / 锚点面板

**Wave:** W3 · **并发:** 可并发（前端面板轨；`features/code-intel` 串行链第 2 环） · **依赖:** T9
**写作用域:** `frontend/src/features/code-intel/`（面板组件与测试；可改 `CodeChangesTab.tsx` 挂载）

**Files:**
- Create: `frontend/src/features/code-intel/{FactsPanel.tsx,EvidenceLink.tsx,ImpactPanel.tsx,UnmappedChanges.tsx,AnchorDialog.tsx,*Panel.test.tsx,ImpactPanel.test.tsx}`
- Modify: `frontend/src/features/code-intel/CodeChangesTab.tsx`

**Interfaces:**
- Consumes: T3 `getLatestAnalysis/getAnalysis/patchCandidate/listAnchors/createAnchor/deleteAnchor/getFile`；T9 容器上下文（versionId、analysis、refresh）。
- Produces: `<FactsPanel analysis />`、`<ImpactPanel analysis onChanged />`、`<UnmappedChanges entries />`、`<AnchorDialog versionId filePath onCreated />`；供 T20 E2E 断言。

- [ ] **Step 1: 写失败测试**

```tsx
it("shows facts by kind with confidence and evidence links", () => { /* route/api/component 分组；low 显示"低置信线索"；点击证据打开 file:line 窗口 */ });
it("toggles candidate selection and rolls back on API failure", async () => { /* patchCandidate 成功更新；失败恢复原值并提示 */ });
it("keeps low candidates unselected by default and selectable", () => { /* low checkbox 初始 false，可点击 */ });
it("renders unmapped reasons", () => { /* no_fact_or_anchor/unsupported_content/deleted_file/ai_unavailable 文案 */ });
it("creates and deletes a requirement anchor", async () => { /* AnchorDialog 选择需求 → createAnchor；删除后列表移除 */ });
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd frontend; pnpm vitest run src/features/code-intel`
Expected: FAIL（组件不存在）。

- [ ] **Step 3: 实现**

- `FactsPanel`：按 kind 分组、confidence 徽标、low 灰显；证据链接 `file:line` → `getFile` 行窗口抽屉高亮。
- `ImpactPanel`：需求/应用模型分组；checkbox → `patchCandidate`；乐观更新 + 失败回滚 toast；reason/source/confidence/evidence；保存中禁用。
- `UnmappedChanges`：path/status/reason/suggestion；截断提示。
- `AnchorDialog`：需求下拉（版本需求 API 既有 `GET /api/versions/{id}/requirements`）、文件路径预填、行区间可选；创建/删除后调用容器 refresh；普通用户均可用。
- 容器挂载面板；无分析时展示 CTA（不渲染假数据）；降级横幅。

- [ ] **Step 4: 运行测试确认通过**

Run: `cd frontend; pnpm vitest run src/features/code-intel && pnpm typecheck`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/features/code-intel
git commit -m "feat: add codeintel facts, impact, unmapped and anchor panels"
```

---

### Task 12: 前端代码仓索引状态与刷新

**Wave:** W3 · **并发:** 可并发（代码仓 UI 轨） · **依赖:** T3
**写作用域:** `frontend/src/features/code-repos/`

**Files:**
- Modify: `frontend/src/features/code-repos/{CodeReposPage.tsx,api.ts,types.ts,CodeReposPage.test.tsx}`
- Create: `frontend/src/features/code-repos/useIndexStatus.ts`（或页面内 hook）

**Interfaces:**
- Consumes: `api-client`；P2 `GET /api/resources/code-repos`（既有）；P4 `GET /api/code/repos`、`POST /api/code/repos/{id}/index`、`GET /api/code/repos/{id}/index-runs/latest`。
- Produces: 代码仓页显示索引状态（未索引/running/done/failed + 时间/commit）；admin「刷新索引」触发并轮询；普通用户按钮隐藏/禁用（后端 403 兜底）。

- [ ] **Step 1: 写失败测试**

```tsx
it("shows last indexed status and removes the P4 placeholder copy", () => { /* 不再出现"未索引（P4）"；显示索引时间/commit */ });
it("lets admins refresh and polls until done", async () => { /* 点击 → startIndex；fake timer 轮询 → running→done；按钮恢复 */ });
it("hides the refresh action for non-admins", () => { /* role=user → 无按钮 */ });
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd frontend; pnpm vitest run src/features/code-repos`
Expected: FAIL（旧断言/按钮）。

- [ ] **Step 3: 实现**

- `api.ts` 增加 `getIndexSummary(repoId)`/`startIndex(repoId)`（复用 code-intel/api 或独立薄封装，二选一但避免重复请求逻辑）。
- 页面合并 P2 仓库数据与索引状态；状态徽标；刷新按钮仅 admin；轮询 1.5s（最多 60 次）；失败 toast；保留 P2 配置校验行为。
- 更新测试中旧的「未索引（P4）」断言。

- [ ] **Step 4: 运行测试确认通过**

Run: `cd frontend; pnpm vitest run src/features/code-repos && pnpm typecheck`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/features/code-repos
git commit -m "feat: show code repo index status and refresh action"
```

---

### Task 13: ImpactService + AnchorService

**Wave:** W5 · **并发:** 串行（application 链第 4 环） · **依赖:** T10
**写作用域:** `backend/internal/codeintel/application/`（`impact_service.go`/`anchor_service.go` 及测试）、`backend/internal/codeintel/integration/{impact_integration_test.go,anchor_integration_test.go}`

**Files:**
- Create: `backend/internal/codeintel/application/{impact_service.go,anchor_service.go,impact_service_test.go,anchor_service_test.go}`
- Create: `backend/internal/codeintel/integration/{impact_integration_test.go,anchor_integration_test.go}`

**Interfaces:**
- Consumes: T10 `AnalysisService` 物化结果/仓储；T6 `LLMCompleter`/`ValidateEvidence`；T7 `VersionCatalog`/`AppModelCatalog`；T5 仓储；`domain.SortCandidates`/`NormalizeMethodPath`。
- Produces：
  - `NewImpactService(deps ImpactServiceDeps) *ImpactService`，实现 T10 `ImpactPlanner.Plan(ctx, ImpactInput) (ImpactResult, error)`（候选/unmapped/stats 一次返回）。
  - `NewAnchorService(deps AnchorServiceDeps) *AnchorService`；`Create/Delete/List` 签名见 Shared Interfaces。
  - `ImpactServiceDeps{LLM LLMCompleter, Versions VersionCatalog, AppModels AppModelCatalog, Anchors AnchorRepository, IDs IDGenerator, Clock Clock, MaxRequirements int}`
  - `AnchorServiceDeps{Repos RepoCatalog, Versions VersionCatalog, AppModels AppModelCatalog, Git GitReader, Anchors AnchorRepository, IDs IDGenerator, Clock Clock}`

- [ ] **Step 1: 写失败测试**

```go
func TestImpactDirectAnchorAndAppModelMatch(t *testing.T) { /* 变更文件命中锚点 → requirement direct/high；api fact 匹配 app_model key → app_model direct */ }
func TestImpactContractPropagation(t *testing.T) { /* changed client_call → route_to_api → api app_model 候选 source=contract */ }
func TestImpactRelationOneHop(t *testing.T) { /* 已命中 R1 → relation R2 加入 source=relation；不递归 R3 */ }
func TestImpactDedupeSortAndLowSelection(t *testing.T) { /* 同 subject 合并取 direct；顺序 deterministic；low selected=false */ }
func TestImpactUnmappedReasons(t *testing.T) { /* deleted/unsupported/no_fact_or_anchor/ai_unavailable/unmatched_contract 各一条 */ }
func TestImpactAICandidateValidation(t *testing.T) { /* 未知 requirement 丢弃；无效证据降 low；有效证据 high */ }
func TestImpactDegradedSkipsAI(t *testing.T) { /* degraded=true → 不调用 LLM；确定性与 unmapped 仍产出 */ }
func TestAnchorCreateValidationAndDuplicate(t *testing.T) { /* requirement 不属于版本/app_model 不在 system/case 拒绝/重复 409 */ }
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd backend; go test ./internal/codeintel/application ./internal/codeintel/integration`
Expected: FAIL。

- [ ] **Step 3: 实现**

- 按 spec §6.6 流水线实现候选：锚点、应用模型 key 精确匹配（page/route/api/component）、契约一跳、需求关系一跳、AI 推理（需求清单 ≤100，证据按 commit 校验）；合并去重/排序/默认勾选；unmapped 五类；stats。
- AI 调用仅在 `!Degraded && len(Requirements)>0`；失败不使整个分析失败（降级/记录）。
- `AnchorService`：主体归属校验、文件存在（固定分支 tip）、行区间、唯一性、删除归属；case/issue → `unsupported_subject_kind`。
- 所有候选/锚点 ID 用雪花；时间用 clock。

- [ ] **Step 4: 运行测试确认通过**

Run: `cd backend; go test ./internal/codeintel/application ./internal/codeintel/integration`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/codeintel/application backend/internal/codeintel/integration
git commit -m "feat: add codeintel impact analysis and anchors"
```

---

### Task 14: 仓库级 HTTP API（repos/index/tree/file/diff/facts/search）

**Wave:** W4 · **并发:** 可并发（HTTP 轨第 1 环） · **依赖:** T8
**写作用域:** `backend/internal/codeintel/interfaces/httpapi/`

**Files:**
- Create: `backend/internal/codeintel/interfaces/httpapi/{handler.go,routes.go,handlers_repos.go,handlers_changes.go,dto.go,helpers.go}`
- Create: `backend/internal/codeintel/interfaces/httpapi/{handlers_repos_test.go,handlers_changes_test.go,helpers_test.go}`

**Interfaces:**
- Consumes: T8 `IndexService`/`QueryService`；T7 `RepoCatalog`；身份中间件 actor/role（沿用 P2/P3 handler 模式）；`platform/errors`/分页。
- Produces（T15/T18 消费）：
  - `type Handler struct`；`func NewHandler(deps HandlerDeps) *Handler`；`func (h *Handler) Register(mux *http.ServeMux)`（路由见 spec §7.2）。
  - `HandlerDeps{Index *application.IndexService, Query *application.QueryService, Repos application.RepoCatalog}`；handler 从 context 取 actor/role（沿用 version handlers 的认证中间件模式）。
  - DTO 类型：`RepoSummaryDTO/IndexRunDTO/ChangeListDTO/FileDiffDTO/FactsPageDTO/TreePageDTO/FileWindowDTO`（JSON 字段见 spec §7.2）。

- [ ] **Step 1: 写失败测试**

```go
func TestRepoRoutesRequireAuthAndAdminIndex(t *testing.T) { /* 未登录 401；user POST index 403；admin 202 */ }
func TestIndexReturnsRunAndDuplicate409(t *testing.T) { /* 202 {runId,status,commit}；第二次 queued/running → 409 index_in_progress */ }
func TestTreeFileDiffPaginateAndReasons(t *testing.T) { /* cursor/has_more；path 越界 422 path_escape；二进制 422 unsupported_content；not_changed 422 */ }
func TestFactsSearchSerializeIDs(t *testing.T) { /* entity id string、repoId number；fact evidence camelCase；search 子串 */ }
func TestFileReadAudit(t *testing.T) { /* Query.File 调用 AuditRecorder，detail 无 content */ }
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd backend; go test ./internal/codeintel/interfaces/httpapi`
Expected: FAIL。

- [ ] **Step 3: 实现**

- 路由按 spec §7.2；路径参数 `repoId` number、runId string；请求体严格解析（`platform.DecodeJSON`）；错误 reason 见 §7.1；`Idempotency-Key` 由既有中间件处理（若 server 级）。
- DTO 序列化：codeintel ID → string；repoId/createdBy → number；evidence `{file,startLine,endLine,excerptHash}`。
- `File` 读取调 `AuditRecorder`；`Search` 仅 path/name 子串（非全文）。
- 不写 SQL、不直调 Git/LLM。

- [ ] **Step 4: 运行测试确认通过**

Run: `cd backend; go test ./internal/codeintel/interfaces/httpapi`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/codeintel/interfaces/httpapi
git commit -m "feat: add codeintel repository HTTP API"
```

---

### Task 15: 版本级 HTTP API（analyses/candidates/anchors）

**Wave:** W6 · **并发:** 串行（HTTP 轨第 2 环） · **依赖:** T13、T14
**写作用域:** `backend/internal/codeintel/interfaces/httpapi/`

**Files:**
- Create: `backend/internal/codeintel/interfaces/httpapi/{handlers_analyses.go,handlers_anchors.go,handlers_analyses_test.go,handlers_anchors_test.go}`
- Modify: `backend/internal/codeintel/interfaces/httpapi/{routes.go,dto.go,handler.go}`

**Interfaces:**
- Consumes: T10 `AnalysisService`、T13 `AnchorService`；T14 `Handler`/`dto`/`helpers`；版本归属校验经 application。
- Produces：spec §7.3 全部路由与 DTO（`AnalysisDTO/CandidateDTO/AnchorDTO`）；错误 reason；供 T18 挂载、T19 OpenAPI、T20 E2E。

- [ ] **Step 1: 写失败测试**

```go
func TestAnalysisStartReuseForceAndConflicts(t *testing.T) { /* 202 新建；200 reused；409 analysis_in_progress；422 code_repository_not_bound/invalid_head */ }
func TestAnalysisLatestAndPatchCandidate(t *testing.T) { /* 404 无分析；PATCH 200 selected；未完成 409；candidate_not_found 404 */ }
func TestAnalysisDTOIDTypes(t *testing.T) { /* analysis/anchor/candidate id string；repoId number；subjectId string；evidence 可选字段 */ }
func TestAnchorsCRUDAndReason(t *testing.T) { /* 创建 201；重复 409 anchor_duplicate；case 422 unsupported_subject_kind；删除 204；不存在 404 */ }
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd backend; go test ./internal/codeintel/interfaces/httpapi`
Expected: FAIL。

- [ ] **Step 3: 实现**

- 路由按 spec §7.3；`POST /analyses` 请求 `{headCommit?, force?}`；复用返回 200、新建 202；错误映射含 `analysis_queue_full` 429。
- `GET latest/详情` 汇总 changeSet 摘要、stats、candidates、unmapped、factVersions；`PATCH candidate` 校验归属。
- 锚点 CRUD；创建请求 `subjectId` 字符串解析为 int64；返回 DTO 字符串。
- 不实现事件端点（复用版本 SSE）。

- [ ] **Step 4: 运行测试确认通过**

Run: `cd backend; go test ./internal/codeintel/interfaces/httpapi`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/codeintel/interfaces/httpapi
git commit -m "feat: add codeintel version analyses and anchors HTTP API"
```

---

### Task 16: Agent 只读工具薄壳

**Wave:** W6 · **并发:** 可并发（工具轨；与 T15 不同包） · **依赖:** T8、T13
**写作用域:** `backend/internal/agent/infrastructure/tools/`、`backend/internal/agent/infrastructure/tools/codeintel_test.go`

**Files:**
- Create: `backend/internal/agent/infrastructure/tools/codeintel.go`
- Create: `backend/internal/agent/infrastructure/tools/codeintel_test.go`

**Interfaces:**
- Consumes: T8 `QueryService/IndexService`、T10 `AnalysisService`、T13 `AnchorService`；沿用 `clarification.go` 的本地窄端口模式；`domain.Tool` 接口（`Name/Description/Parameters/Execute`）。
- Produces：`func NewCodeIntelTools(deps CodeIntelToolDeps) []domain.Tool`；工具 registry 名 `code.repos/code.search/code.diff/code.facts/code.routes/code.apis/code.analyze/code.impact/code.anchors`（spec §8.1）；不新增 profile、不改 P3 `allowed_tools`。

- [ ] **Step 1: 写失败测试**

```go
func TestCodeIntelToolNamesAndSchemas(t *testing.T) { /* 9 个 name；每个 Parameters 为合法 JSON schema 对象；描述非空 */ }
func TestCodeSearchClampsLimitAndOmitsContent(t *testing.T) { /* limit=1000 → 200；返回摘要无 content 字段 */ }
func TestCodeAnalyzeTriggersWithoutBlocking(t *testing.T) { /* fake AnalysisService.Start 返回 queued；Execute 立即返回 status/analysisId */ }
func TestCodeImpactReturnsCandidatesSummary(t *testing.T) { /* subjectId 字符串；selected 布尔；reason/confidence */ }
func TestToolsDoNotBreakExistingRegistry(t *testing.T) { /* 与 NewClarificationTools 合并后无重名；P3 工具仍可注册 */ }
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd backend; go test ./internal/agent/infrastructure/tools`
Expected: FAIL（`NewCodeIntelTools` undefined）。

- [ ] **Step 3: 实现**

- 定义 `CodeIntelToolPort` 窄接口（只含工具需要的方法），生产实现由 T18 用服务适配；测试用 fake。
- 每个工具：参数结构体 + `Execute` 调服务；输出摘要（路径/计数/置信度），不返回正文；`code.analyze` 只触发（不等待），`code.impact` 默认 latest。
- 所有工具输出受上限（≤200 条/摘要字段），错误用 `domain.ToolResult` 既有语义；不导入 codeintel infrastructure。

- [ ] **Step 4: 运行测试确认通过**

Run: `cd backend; go test ./internal/agent/infrastructure/tools ./internal/agent/...`
Expected: PASS（P3 工具测试不回归）。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/agent/infrastructure/tools
git commit -m "feat: add codeintel read-only agent tools"
```

---

### Task 17: fake LLM P4 场景 + fixture 仓库脚本 + E2E 接线

**Wave:** W2 · **并发:** 可并发（测试基建轨） · **依赖:** T6
**写作用域:** `scripts/`

**Files:**
- Create: `scripts/fixture-codeintel-repo.ps1`
- Modify: `scripts/fake-llm.cjs`
- Modify: `scripts/e2e.ps1`（fixture 初始化 + `EZTEST_FAKE_LLM_P4_MODE` + repo env）
- Create: `scripts/fake-llm.p4.test.cjs`（Node 单测，可选但本任务要求）

**Interfaces:**
- Consumes: T6 提示词标记（`EzTest 的只读代码分析器`/`EzTest 的只读变更影响分析器`）；spec §10.2 fixture 内容。
- Produces：
  - `.tmp/p4-e2e-repo`（gitignored）：初始 commit + 第二 commit（TS 前端调用点、Go 后端接口、页面/组件/路由、util、二进制、删除文件；固定作者/时间）。
  - fake LLM P4 场景：按文件 path 返回确定性 facts/edges（证据行号与 fixture 一致）；影响推理默认空数组，`EZTEST_FAKE_LLM_P4_MODE=impact-ai` 返回受影响需求；`ok|error|partial` 模式；请求日志 `.tmp/fake-llm-requests.jsonl`。
  - `scripts/e2e.ps1` 初始化 fixture 并注入环境变量（不改变 P1/P3 场景）。

- [ ] **Step 1: 写失败检查**

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/fixture-codeintel-repo.ps1
if (-not (Test-Path .tmp/p4-e2e-repo/.git)) { exit 1 }
git -C .tmp/p4-e2e-repo log --oneline | Select-String 'p4 fixture'
node scripts/fake-llm.p4.test.cjs
```

- [ ] **Step 2: 运行检查确认失败**

Run: 上述脚本
Expected: `fixture-codeintel-repo.ps1` 不存在/失败；fake LLM P4 测试失败。

- [ ] **Step 3: 实现**

- fixture 脚本：幂等（存在则删除重建）；`git init`、固定 `user.name/email` 与 `GIT_AUTHOR_DATE/GIT_COMMITTER_DATE`；提交 1/2 内容严格按 spec §10.2；输出 repo 路径/两个 commit SHA（供 E2E 读取）。
- fake-llm：新增 P4 路由（按 system prompt 识别），facts 的 `path/startLine/endLine` 与 fixture 文件真实行号一致；`evidence` 哈希不返回（服务端算）；记录请求；mode 开关；保留 P1/P3 场景与回落。
- e2e.ps1：设置 `EZTEST_FAKE_LLM_P4_MODE`、fixture repo 路径 env；启动 API 前确保迁移；不破坏现有 P3 流程。

- [ ] **Step 4: 运行检查确认通过**

Run: 同 Step 2
Expected: fixture 两 commit 存在；fake LLM P4 测试 PASS（含 facts JSON 可解析、未知请求回落）。

- [ ] **Step 5: 提交**

```bash
git add scripts/fixture-codeintel-repo.ps1 scripts/fake-llm.cjs scripts/fake-llm.p4.test.cjs scripts/e2e.ps1
git commit -m "test: add P4 fixture repo and fake LLM scenarios"
```

---

### Task 18: 组装根 + server 装配 + wiring 集成

**Wave:** W7 · **并发:** 可并发（装配轨） · **依赖:** T15、T16、T7、T8、T10、T13
**写作用域:** `backend/cmd/eztest/`、`backend/internal/server/`

**Files:**
- Modify: `backend/cmd/eztest/main.go`（构造 codeintel 仓储/服务/worker/工具；worker.SetRunner；优雅停机）
- Modify: `backend/internal/server/server.go`（`Dependencies.CodeIntel` + 路由挂载 + 权限中间件）
- Modify: `backend/internal/server/server_test.go`（路由/依赖）
- Create: `backend/internal/codeintel/integration/wiring_integration_test.go`

**Interfaces:**
- Consumes: T7/T8/T10/T13/T14/T15/T16 全部产物；`config.Config` 新字段；`platform/id` 雪花；既有身份中间件。
- Produces：运行中的 API：`/api/code/*`、`/api/versions/{id}/code/*` 可达；registry 含 9 个新工具；迁移自动执行；worker 启动/恢复；`server.Dependencies` 新字段。

- [ ] **Step 1: 写失败测试**

```go
func TestServerMountsCodeIntelRoutes(t *testing.T) { /* 未登录 /api/code/repos → 401；登录 → 200/404 而非 404 路由缺失 */ }
func TestAgentToolCatalogIncludesCodeIntel(t *testing.T) { /* GET /api/agents/tools 含 code_repos/code_analyze 等 wire 名 */ }
func TestCodeIntelWiringStartsWorkerAndRecovers(t *testing.T) { /* 启动后遗留 running 分析 → failed(interrupted)；健康检查通过 */ }
func TestP3RoutesUnchanged(t *testing.T) { /* 既有 versions/stream 路由仍注册 */ }
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd backend; go test ./cmd/eztest ./internal/server`
Expected: FAIL（依赖/字段不存在）。

- [ ] **Step 3: 实现**

- `main.go`：构造 9 仓储 → Worker（不设 runner）→ Index/Query/Analysis/Impact/Anchor 服务（注入 Queue=worker）→ `worker.SetRunner(Runner{Index: indexSvc, Analysis: analysisSvc})` → `worker.Start()`；注册工具到 registry（构建前 append）；`server.Dependencies.CodeIntel = handler`。
- `server.go`：新增 `CodeIntel` 依赖字段与 `Register`；认证中间件；admin-only 索引动作通过 handler 内/中间件校验；SSE 不变。
- 优雅停机顺序：HTTP → worker.Stop → DB。
- 不在 handler 里构造服务。

- [ ] **Step 4: 运行测试确认通过**

Run: `cd backend; go test ./cmd/eztest ./internal/server`
Expected: PASS；`powershell -File scripts/test-integration.ps1 ./internal/codeintel/integration/...` 通过。

- [ ] **Step 5: 提交**

```bash
git add backend/cmd/eztest backend/internal/server
git commit -m "feat: wire codeintel context into server and agent tools"
```

---

### Task 19: OpenAPI + acceptance 脚本 + README + gated 真实 LLM 脚本

**Wave:** W7 · **并发:** 可并发（文档/脚本轨） · **依赖:** T15
**写作用域:** `docs/openapi/`、`scripts/`、`README.md`

**Files:**
- Modify: `docs/openapi/api.yaml`（tag/paths/schemas/reason/事件说明）
- Modify: `scripts/acceptance.ps1`（迁移计数 16、P4 OpenAPI needle、事件/reason 检查）
- Create: `scripts/acceptance-codeintel-llm.ps1`（gated 真实 LLM 验收：fixture + EzTest 真实 diff）
- Modify: `README.md`（P4 功能/命令/只读挂载说明）
- Modify: `docs/dev-setup.local.md` 仅如需（不写密钥）

**Interfaces:**
- Consumes: spec §7/§10；T17 fixture 脚本；真实 `.env` llm 键。
- Produces：OpenAPI 与实现一致；`acceptance.ps1` 可检出缺失；gated 脚本未设 gate 时打印 SKIPPED；README 含 P4 运行/验收说明。

- [ ] **Step 1: 写失败检查**

```powershell
Select-String -Path docs/openapi/api.yaml -Pattern '/api/code/repos/\{repoId\}/index'
Select-String -Path scripts/acceptance.ps1 -Pattern 'code.impact_computed'
$env:EZTEST_RUN_REAL_LLM=''; powershell -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance-codeintel-llm.ps1
```

- [ ] **Step 2: 运行检查确认失败**

Run: 上述脚本
Expected: OpenAPI/acceptance 无匹配；gated 脚本不存在。

- [ ] **Step 3: 实现**

- OpenAPI：按 spec §7.2/§7.3 增加 paths、schemas（RepoSummary/IndexRun/ChangeList/FileDiff/FactsPage/Analysis/Candidate/Anchor）、reason 枚举、`code.changed`/`code.impact_computed` 事件描述；示例用占位路径。
- `acceptance.ps1`：迁移计数 16（order.txt 条目）；needle 覆盖两个 API 前缀、`analysis_in_progress`、`anchor_duplicate`、事件类型；不破坏 P0–P3 检查。
- gated 脚本：未设 `EZTEST_RUN_REAL_LLM=1` 打印 `SKIPPED` 退出 0；设置后校验 `.env` 三键、启动/复用 API、配置 fixture 或 EzTest 克隆仓库、创建绑定版本、触发分析、校验证据哈希/缓存复用/force 版本/降级可跳过（记录）；输出机器可读结果到 `.tmp/`。
- README：P4 能力、端口、迁移、索引/分析命令、只读挂载、测试/E2E/真实 LLM 说明。

- [ ] **Step 4: 运行检查确认通过**

Run: 同 Step 2（gated 不设 gate 应 SKIPPED）+ `scripts/acceptance.ps1 -SkipE2E`（若支持；否则运行其静态检查部分）
Expected: 均 PASS/SKIPPED 符合预期；OpenAPI YAML 可解析（脚本自带检查）。

- [ ] **Step 5: 提交**

```bash
git add docs/openapi/api.yaml scripts/acceptance.ps1 scripts/acceptance-codeintel-llm.ps1 README.md docs/dev-setup.local.md
git commit -m "docs: add P4 OpenAPI, acceptance and runbook"
```

---

### Task 20: P4 E2E + 全量门禁修复

**Wave:** W8 · **并发:** 串行（集成轨） · **依赖:** T18、T19、T17、T11、T12
**写作用域:** `frontend/e2e/`、`frontend/src/`（仅修复失败所需，Lead 协调）、`scripts/`（仅修复失败所需）

**Files:**
- Create: `frontend/e2e/{versions-code.spec.ts,versions-code-cache.spec.ts,versions-code-degraded.spec.ts,versions-code-anchors.spec.ts,code-repos-index.spec.ts}`
- Modify: `frontend/e2e/{versions.spec.ts,code-repos.spec.ts}`（P4 占位/未索引断言）
- Modify: 失败暴露的前端/后端文件（仅按需，Lead 记录 scope）

**Interfaces:**
- Consumes: T17 fixture/fake LLM；T11/T12 UI；T18 API；`scripts/e2e.ps1`。
- Produces: 全部 E2E 通过；`scripts/test.ps1` 全绿；已知失败修复提交（fix 前缀）。

- [ ] **Step 1: 写失败 E2E（spec §10.5 场景）**

按 spec §10.5 编写 5 个 spec；至少包含：
- 索引 → diff → fake ok 分析 → 候选（需求+应用模型）→ 勾选持久化 → unmapped → SSE 刷新。
- 缓存：同版本 reused；第二版本 cacheHits>0；force analysisVersion=2。
- 降级：`EZTEST_FAKE_LLM_P4_MODE=error` → degraded 横幅 + T0 候选 + `ai_unavailable` unmapped；恢复后 force。
- 锚点：创建 → direct 候选 → 删除 → force 后消失。
- 代码仓：普通用户不可索引；管理员 running→done。

- [ ] **Step 2: 运行 E2E 确认失败**

Run: `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1 code`
Expected: 失败（新 spec / 旧断言）。

- [ ] **Step 3: 修复实现/测试**

- 仅按失败原因修复：更新 P3 占位断言；修复 ID/轮询/SSE/权限/空态问题；必要时补 fake LLM 场景（写作用域内）。
- 每个修复遵循 TDD：先复现失败，再修，再跑。

- [ ] **Step 4: 运行全量门禁确认通过**

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1
```

Expected: 全部 PASS（迁移 16、OpenAPI needle、E2E 全绿）；记录计数/exit 到 `.superpowers/sdd/<plan>/progress.md`。

- [ ] **Step 5: 提交**

```bash
git add frontend/e2e frontend/src scripts
git commit -m "test: add P4 code intelligence end-to-end coverage"
```

---

### Task 21: 代码审查 + 真实人工验收 + 合回 dev

**Wave:** W9 · **并发:** 串行（收尾） · **依赖:** T20
**写作用域:** 全仓（仅审查修复与验收记录；Lead 统一提交）

**Files:**
- Create: `.superpowers/sdd/2026-10-28-eztest-p4-code-intelligence/{progress.md,acceptance.md,plan-path}`（gitignored）
- Modify: 审查发现的问题文件（按需）
- Modify: 文档（仅当验收发现文档不一致）

**Interfaces:**
- Consumes: T20 全绿门禁；spec §10.7 验收步骤；真实 `.env` LLM 配置；EzTest 克隆/真实 commit 对。
- Produces：review 报告与修复；真实人工验收证据（截图/命令/exit/结论）；合回 `dev` 的本地分支。

- [ ] **Step 1: 请求独立代码审查**

使用 `requesting-code-review` 技能：对整个分支 `dev..feat/p4-code-intelligence` 做独立审查（正确性/契约/安全/测试覆盖/文档）；记录 findings。

- [ ] **Step 2: 修复审查问题（测试先行）**

- 每个 finding：先补失败测试（可复现），再修复，再跑相关测试；记录 `fix:` 提交。
- 严重问题未解决前不得进入验收。

- [ ] **Step 3: 重跑全量门禁 + gated 真实 LLM 脚本**

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1
$env:EZTEST_RUN_REAL_LLM="1"; powershell -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance-codeintel-llm.ps1
```

Expected: 全部 PASS；真实 LLM 输出记录 provider/model/run/分析 ID/证据校验/缓存复用/降级结果。

- [ ] **Step 4: 真实人工验收（代替用户，按 spec §10.7）**

- 启动 dev-api/dev-web；管理员登录；配置 EzTest 克隆仓库 + 验收分支；刷新索引。
- 创建版本（system/app model/repo/branch/3 需求/1 关系）；查看变更预览并创建需求锚点。
- 触发真实 LLM 分析（真实后续 commit）；核对变更文件/diff、事实证据 `file:line` 内容、候选原因/置信度/证据、unmapped、SSE 自动刷新。
- 缓存：再次 POST reused=true 且候选一致；第二版本 cacheHits>0；force 新 analysis 版本且旧结果不变。
- 降级：临时改默认 LLM 为不可用地址 → degraded 横幅 + T0 候选 + 2xx；恢复后 force 成功。
- 权限：普通用户读/分析/调候选可用；索引 403；未登录 401。
- 截图存 `.tmp/p4-acceptance/`；把 Date/Branch/HEAD/Scope/Status、命令、exit、计数、结论、证据路径追加到 `.superpowers/sdd/2026-10-28-eztest-p4-code-intelligence/acceptance.md`；不粘贴代码正文/密钥。

- [ ] **Step 5: 合回 dev 并收尾**

```bash
git switch dev
git merge --ff-only feat/p4-code-intelligence
git log --oneline -5
git status --short
```

- 更新 `progress.md` 权威状态为 complete；核对 spec/蓝图/Agent.md/OpenAPI/README 与实现一致；最终向用户报告验收结论与证据路径（不 push）。

---

## Self-Review（写完计划后已执行）

### 1. Spec coverage

| Spec 章节 | 任务 |
|---|---|
| §1 目标/非目标、§2 决策 | Global Constraints；T1/T2/T6/T10/T13/T14/T15 |
| §3 架构/目录/端口/组装 | T2/T6/T7/T8/T10/T13/T14/T15/T16/T18 |
| §4 配置 | T2（config + limits + .env） |
| §5 数据模型/迁移 | T2（DDL/order.txt）、T5（仓储/集成）、T7（MarkIndexed） |
| §6.1 T0 | T4、T8 |
| §6.2 T1 / §6.3 证据 | T6、T10、T13 |
| §6.4 缓存/版本 | T5、T10、T20 |
| §6.5 契约 | T10、T13 |
| §6.6 影响 | T13、T20 |
| §6.7 锚点 | T13、T15、T11 |
| §6.8 异步 run | T8、T10、T18 |
| §6.9 事件 / §6.10 权限审计 | T7、T14、T15 |
| §6.11 并发 / §6.12 不变量 | T5、T8、T10、T13、T20 |
| §7 API | T14、T15、T19 |
| §8 工具/提示词 | T6、T16 |
| §9 前端 | T3、T9、T11、T12 |
| §10 测试/验收 | T17、T19、T20、T21 |
| §11 风险/§14 不做 | Global Constraints、Review Focus |
| §13 文档同步 | T1、T19 |
| §15 假设 | Global Constraints、T18/T21 |

结论：无 spec 要求无任务；P4 不做的内容明确不在任务内。

### 2. Step scan

- 每个任务 5 步（测试→跑失败→实现→跑通过→提交）；文档/脚本任务用可执行 grep/脚本替代单元测试，仍保留红/绿两阶段。
- 未写完整函数体；签名/类型/DDL/提示词/测试名与断言为计划固定内容；复杂算法（影响合并、契约归一、证据哈希）由 spec 规则 + 测试钉住。
- 测试步骤中部分场景以注释列出断言要点；实现者须按 spec 精确值展开（计划不重复 spec 的规则文本）。

### 3. Type consistency

- 统一使用 Shared Interfaces 的 `IndexRun/CodeAnalysis/ImpactCandidate/CodeAnchor/Evidence/ChangeSetData`；服务签名 T8/T10/T13 与 handlers/tools 一致。
- `Worker` 通过 `SetRunner` 解决 services↔worker 循环；服务依赖 `Enqueuer`。
- `ImpactPlanner.Plan` 返回 `ImpactResult`（候选+unmapped+stats），T10/T13 一致。
- `ChangeSetData.ChangeSetID *int64` 允许只读预览为空。
- 前端 `repoId: number`、其余 ID `string`；API DTO 与 spec §7.1 一致。

### 4. Review Focus

五类均落到任务：
- 代码内容不可信 → T4/T6/T10/T13/T14 测试。
- ID 类型 → T2/T3/T14/T15/T20 断言。
- P2/P3 衔接/迁移 → T2/T5/T7/T18。
- 缓存/版本/确定性 → T5/T10/T13/T20。
- 降级/异步/实时上限 → T7/T8/T10/T13/T20。

### 5. Proportion

计划约 1.7k 行 vs spec 约 1.39k 行；无大段完整实现代码；测试/接口/步骤为主。若执行中发现任务过大（T10/T13），按 review finding 拆分或追加 fix 任务，不改变 Wave 依赖。

---

*计划版本：v1.0（2026-10-28）；与 P4 spec v1.0 配套。执行中如调整任务/Wave，先更新本计划并记录原因。*
