# EzTest P2 Resources & Identity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付「公共资源与身份」域（系统/环境/应用模型/账号/Access/Session/本地 Git 仓）与 `/resources`、`/code-repos` 管理 UI，管理员可写、普通用户只读，并用本地 fixture 完成可复现验收。

**Architecture:** 新增单个 `resource` 限界上下文（DDD 四层），11 张表；凭据与 storageState 明文存储（D28），无加密/脱敏/审计；Session 采用人工导入 + 只读 HTTP login_check + 人工确认；代码仓只读 git 校验。前端两个路由页，复用 P0 请求层与 UI 原语。

**Tech Stack:** Go 1.24.6（标准库 `net/http`、pgx/v5）、PostgreSQL 17.6、Next.js 16 / React 19 / TypeScript / Tailwind / Radix、Vitest、Playwright（本机 Chrome/Edge）、Node fixture（无新依赖）。

**Spec:** [docs/superpowers/specs/2026-10-27-eztest-p2-resources-identity-design.md](../specs/2026-10-27-eztest-p2-resources-identity-design.md)

## Global Constraints

- P2 范围严格按 `implementation-phases.md` §2-P2 与本规格；**不建**版本/需求/用例/执行/证据/问题/报告领域实体。
- **D28**：凭据 `account_system_accesses.credential` 与 `sessions.storage_state` 明文存储；**禁止**引入加密、脱敏、`audit_logs` 写入、`session_events`。
- 11 张表固定：`companies/systems/environments/app_model_nodes/accounts/account_system_accesses/sessions/session_snapshots/session_leases/manual_takeovers/code_repositories`；迁移 `resource_0001_catalog.sql`、`resource_0002_accounts_sessions.sql`、`resource_0003_code_repositories.sql`，追加到 `order.txt`，只增不改。
- 单公司：服务固定使用 `companies(key='default')`；创建接口不接受 `companyId`。
- API 前缀 `/api/resources`；GET=任意登录用户；POST/PATCH/DELETE/动作=admin（`identityhttp.RequireAdmin`）。普通用户写→403。
- 错误复用 `platform/errors` 闭集：400/422=`invalid_argument`、404=`not_found`、409=`conflict`、403=`forbidden`；细分用 `details.reason`/`details.field`。
- Session 状态：`pending/acquiring/needs_manual/valid/refreshing/expired/invalid/revoked/failed`；`expiring` 派生不落库；`failed` P2 不产生（留给 P6）。
- 导入仅 `storage_state`（Playwright JSON）与 `token`（headers bundle），请求体 ≤5 MiB；`environmentId` 必填且属于 Access 的 system。
- 凭据与 storageState **不回显**；Access 只回 `credentialSet`，PATCH 时省略=不变、空串=清除。
- Git 校验只用只读命令（`rev-parse`），`GIT_OPTIONAL_LOCKS=0`、5s 超时；不限根目录；不写工作树。
- 应用模型 key 约定 `page:/route:/api:METHOD /path/component:Name`；kind 与前缀一致。
- 端口沿用 Web `6868`、API `13333`、PostgreSQL `5433`；fixture target `13335`。
- 不新增 Go/前端依赖；所有 HTTP 走 `src/lib/api-client.ts`；无 SSE。
- 每任务：先写失败测试 → 跑失败 → 最小实现 → 跑通过 → 提交；提交前缀 `feat/fix/test/docs/chore`。
- 不提交 `.env`、`.tools/`、`.tmp/`、`.superpowers/sdd/`、`*.local.md`；日志/响应不得出现 `credential` 或 `storage_state` 原文。
- 分支 `feat/p2-resources-identity`（已创建），小步提交，不 push；验收后本地 ff 合回 `dev`。

### 命令约定

- Go：`cd backend; & ..\.tools\go\bin\go.exe test ...`（回退 `go`）。
- 集成测试：`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-integration.ps1 <go test 参数>`（自动导入 `.env` 的 `EZTEST_TEST_DATABASE_URL`）。
- 前端：`cd frontend; pnpm vitest run <path>`、`pnpm lint`、`pnpm typecheck`。
- 全量门禁：`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1`。
- E2E：`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1`。
- 完整验收：`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1`。

## Concurrency Map（实现时并行执行的唯一依据）

| Wave | Tasks | 并行规则 |
|---|---|---|
| W0（串行门禁） | T1 → T2 | T2 未完成不进入 W1 |
| W1 | T3 → T4 → T5 → T6；T7 ∥ T8 ∥ T9 ∥ T10 | 同包后端串行链；T7/T8/T9/T10 并行 |
| W2 | T11 → T12 → T13 → T14；T15 | 同包后端串行链；T15 并行 |
| W3 | T16 ∥ T17 | 前端会话页与后端装配互斥，可并行 |
| W4 | T18 ∥ T19 | E2E 与验收脚本/文档互斥，可并行 |
| W5 | T20 | 串行（全量回归 + 人工验收 + 最终评审） |

- 同一 Wave 的任务派发给独立 subagent；每个 task 的 `**写作用域**` 是该 subagent 唯一允许修改的路径前缀。
- **同包串行**：T3–T6 同属 `infrastructure/postgres` 包、T11–T14 同属 `application` 与 `interfaces/httpapi` 包；并行写会让彼此编译到半成品文件，因此后端同包任务按表中的串行链执行。前端/脚本/独立包任务仍并行。
- 并行实现者**不 commit**；改动留工作区并报告文件清单；Lead 只 stage 该任务写作用域并提交，保证 per-task review range。
- 跨 Wave 依赖未完成前不得启动后续任务；接口以各任务 `Interfaces → Produces` 为准。
- T14/T15 依次修改 `ResourcesPage.tsx`，因此分属 W2/W3 串行；其余同 Wave 任务不共享文件。

## Review Focus（最容易被忽略、必须有测试钉住的 5 类输入）

1. **Session 导入边界**：非法/超大/深层 JSON、超长 cookie/localStorage、未知 kind、过期 cookie、`environmentId` 不匹配——必须 422 且不落半条数据，不得 500。
2. **login_check 误判**：登录页返回 200 但无 `body_contains`、重定向链、超时、token headers 非法——必须进入 `needs_manual`/`failed` 而不是 `valid`；不得 panic/挂起。
3. **状态并发与惰性过期**：并发 validate/confirm/revoke、过期 Session 的列表/详情/动作——CAS 必须 409 或单次成功，不得丢更新；`expired` 不得被 confirm。
4. **应用模型导入结构**：重复 key、父节点缺失、环、超过 5000 节点、kind 与前缀不符——必须 422 且不写部分节点。
5. **Git 校验**：非仓库目录、分支不存在、带空格/Unicode 路径、`-` 开头分支——必须 422 且不执行任何写命令；成功时 commit 为 40 位 hash。

---

## Pre-flight（一次性，不算任务）

- [ ] 确认分支：`git switch feat/p2-resources-identity`（已从 `dev` 创建，含 spec 提交）。
- [ ] 启动数据库：`powershell -File scripts/db.ps1 status`，必要时 `start`。
- [ ] 确认 `frontend/node_modules` 存在（否则 `cd frontend; pnpm install`）。
- [ ] 确认基线绿：`powershell -File scripts/test.ps1`。
- [ ] 确认 fixture 端口 `13335` 空闲：`Get-NetTCPConnection -LocalPort 13335 -ErrorAction SilentlyContinue`。
- [ ] 确认 `.env` 含 `EZTEST_TEST_DATABASE_URL`（指向 `eztest_test`）。

---

### Task 1: D28 设计变更落文档

**Wave:** W0（串行门禁） · **并发:** 不可并发 · **依赖:** 无
**写作用域:** `docs/design/`, `Agent.md`

**Files:**
- Modify: `docs/design/system-blueprint.md`（§3.2 D28、§5.1、§5.5、§9、§10.2、§11.1）
- Modify: `docs/design/auth-session-model.md`（顶部实施口径、§3/§10/§11 标注）
- Modify: `docs/design/implementation-phases.md`（§2-P2 范围/风险）
- Modify: `docs/design/frontend-design.md`（PG7/PG8）
- Modify: `Agent.md`（§6.1 目录树）

**Interfaces:**
- Consumes: spec §12.1 D28 原文、§12.2 同步表。
- Produces: 后续任务可引用的 D28 决策与目录约定。

- [ ] **Step 1: 在 `system-blueprint.md` §3.2 表格 D27 后追加 D28**

使用 spec §12.1 原文（单 `resource` 上下文、明文、无审计/加密、11 表、Session 枚举、`/api/resources/*`、`System.kind` 仅 `web|api`）。

- [ ] **Step 2: 同步主蓝图相关章节**

§5.1 去掉 `secrets/session_blobs/session_events` 作为 P2 实体；§5.5 Session 状态补全 `pending/acquiring/refreshing/failed` 并注明 `expiring` 派生；§9 标注 P2 明文与不做自动登录；§10.2 表清单注明 P2 为 11 表；§11.1 保留 `/api/resources/*`。

- [ ] **Step 3: 在 `auth-session-model.md` 顶部加「P2 实施口径（D28）」**

写明：凭据并入 `account_system_accesses.credential`、storageState 并入 `sessions.storage_state`、不建 `secrets/session_blobs/session_events`、不加密/不审计；§3/§10/§11 各加一行「P2 按 D28 裁剪」。

- [ ] **Step 4: 更新 `implementation-phases.md` §2-P2**

风险行改为「凭据/Session 明文（D28）；人工接管流程尽早打通」；范围注明 Session 事件/审计不做；§2.9 #16 保持不变。

- [ ] **Step 5: 更新 `frontend-design.md` PG7/PG8**

PG8 写明 `/resources` 四 Tab（系统与环境/应用模型/账号与访问/会话）；PG7 写明 `/code-repos` 的路径/分支/校验/commit/索引占位。

- [ ] **Step 6: 更新 `Agent.md` §6.1 目录树**

在 `internal/` 下新增 `resource/` 四层与 `infrastructure/{postgres,httpcheck,gitexec}`。

- [ ] **Step 7: 验证并提交**

Run: `Select-String -Path docs/design/system-blueprint.md -Pattern "D28"`；`Select-String -Path Agent.md -Pattern "internal/resource"`

Expected: 均至少 1 处命中。

```bash
git add docs/design/system-blueprint.md docs/design/auth-session-model.md docs/design/implementation-phases.md docs/design/frontend-design.md Agent.md
git commit -m "docs: record D28 P2 resource context and plaintext storage"
```

---

### Task 2: 迁移、领域模型、仓储端口与 HTTP 公共助手

**Wave:** W0（串行门禁） · **并发:** 不可并发 · **依赖:** T1
**写作用域:** `backend/internal/db/migrations/`, `backend/internal/resource/domain/`, `backend/internal/resource/application/ports.go`, `backend/internal/resource/application/errors.go`, `backend/internal/resource/interfaces/httpapi/helpers.go`, `backend/internal/resource/interfaces/httpapi/helpers_test.go`, `backend/internal/resource/integration/`

**Files:**
- Create: `backend/internal/db/migrations/resource_0001_catalog.sql`
- Create: `backend/internal/db/migrations/resource_0002_accounts_sessions.sql`
- Create: `backend/internal/db/migrations/resource_0003_code_repositories.sql`
- Modify: `backend/internal/db/migrations/order.txt`
- Create: `backend/internal/resource/domain/{company,system,environment,appmodel,account,access,session,snapshot,lease,takeover,coderepo,storage_state,errors,repository}.go`
- Create: `backend/internal/resource/application/{ports.go,errors.go}`
- Create: `backend/internal/resource/interfaces/httpapi/{helpers.go,helpers_test.go}`
- Create: `backend/internal/resource/integration/migrate_integration_test.go`
- Test: `backend/internal/resource/domain/{session,appmodel,coderepo,storage_state}_test.go`

**Interfaces:**
- Consumes: spec §5 DDL、§6 规则。
- Produces（供 T3–T20 使用）:

```go
// domain
const (
    StatePending = "pending"; StateAcquiring = "acquiring"
    StateNeedsManual = "needs_manual"; StateValid = "valid"
    StateRefreshing = "refreshing"; StateExpired = "expired"
    StateInvalid = "invalid"; StateRevoked = "revoked"; StateFailed = "failed"
)

type Page struct { Limit int; AfterID int64 }
type SystemFilter struct { Status, Kind string; Page Page }
type AppModelFilter struct { SystemID int64; Kind, ParentKey string; Page Page }
type AccountFilter struct { Status, Kind string; Page Page }
type SessionFilter struct { AccountID, SystemID, EnvironmentID, AccessID int64; State string; Page Page }
type TakeoverFilter struct { Status string; Page Page }
type CodeRepoFilter struct { Status string; Page Page }

type LoginCheckResult struct { Observed string; Status int; Degraded bool; Reason string }
type GitValidation struct { Commit string }
type StorageState struct {
    Kind string
    Cookies []Cookie
    Origins []Origin
    Headers map[string]string
}
type Cookie struct { Name, Value, Domain, Path string; Expires int64; HTTPOnly, Secure bool; SameSite string }
type Origin struct { Origin string; LocalStorage []LocalStorageItem }
type LocalStorageItem struct { Name, Value string }

func ParseStorageState(kind string, raw json.RawMessage) (*StorageState, error)
func (s *StorageState) EarliestExpiry(now time.Time) (time.Time, bool)
func HashJSON(v any) (string, error)
func ValidateResourceKey(key string) error
func ValidateLoginCheck(lc *LoginCheck) error
func ValidateAppModelKey(kind, key string) error
func ValidateBranch(branch string) error
func ValidateGlobs(globs []string) error
func (s Session) Expiring(now time.Time) bool
func (s Session) IsExpired(now time.Time) bool

type ErrorKind string
const (
    KindInvalid ErrorKind = "invalid"; KindNotFound = "not_found"
    KindConflict = "conflict"; KindDependency = "dependency"
    KindStateConflict = "state_conflict"; KindLeaseConflict = "lease_conflict"
)
type Error struct { Kind ErrorKind; Field, Reason, Message string }
func NewInvalid(field, reason, message string) *Error
func NewNotFound(resource string, id int64) *Error
func NewConflict(reason, message string) *Error
func NewDependency(reason, message string) *Error
func NewStateConflict(message string) *Error
func NewLeaseConflict(message string) *Error

type SystemRepository interface {
    CreateSystem(ctx context.Context, s *System) error
    GetSystem(ctx context.Context, id int64) (*System, error)
    ListSystems(ctx context.Context, f SystemFilter) ([]System, bool, error)
    UpdateSystem(ctx context.Context, s *System) error
    DeleteSystem(ctx context.Context, id int64) error
    CountEnvironments(ctx context.Context, systemID int64) (int, error)
    CountAccesses(ctx context.Context, systemID int64) (int, error)
}
type EnvironmentRepository interface {
    CreateEnvironment(ctx context.Context, e *Environment) error
    GetEnvironment(ctx context.Context, id int64) (*Environment, error)
    ListEnvironments(ctx context.Context, systemID int64) ([]Environment, error)
    UpdateEnvironment(ctx context.Context, e *Environment) error
    DeleteEnvironment(ctx context.Context, id int64) error
    CountSessions(ctx context.Context, environmentID int64) (int, error)
}
type AppModelRepository interface {
    CreateAppModel(ctx context.Context, n *AppModelNode) error
    GetAppModel(ctx context.Context, id int64) (*AppModelNode, error)
    ListAppModels(ctx context.Context, f AppModelFilter) ([]AppModelNode, bool, error)
    UpdateAppModel(ctx context.Context, n *AppModelNode) error
    DeleteAppModel(ctx context.Context, id int64) error
    ListAppModelsByKeys(ctx context.Context, systemID int64, keys []string) (map[string]AppModelNode, error)
}
type CompanyRepository interface { GetCompanyByKey(ctx context.Context, key string) (*Company, error) }
type AccountRepository interface {
    CreateAccount(ctx context.Context, a *Account) error
    GetAccount(ctx context.Context, id int64) (*Account, error)
    ListAccounts(ctx context.Context, f AccountFilter) ([]Account, bool, error)
    UpdateAccount(ctx context.Context, a *Account) error
    DeleteAccount(ctx context.Context, id int64) error
    CountAccesses(ctx context.Context, accountID int64) (int, error)
    CountSessions(ctx context.Context, accountID int64) (int, error)
}
type AccessRepository interface {
    CreateAccess(ctx context.Context, a *Access) error
    GetAccess(ctx context.Context, id int64) (*Access, error)
    ListAccesses(ctx context.Context, accountID int64) ([]Access, error)
    UpdateAccess(ctx context.Context, a *Access) error
    DeleteAccess(ctx context.Context, id int64) error
    CountSessions(ctx context.Context, accessID int64) (int, error)
}
type SessionRepository interface {
    CreateSession(ctx context.Context, s *Session) error
    GetSession(ctx context.Context, id int64) (*Session, error)
    ListSessions(ctx context.Context, f SessionFilter) ([]Session, bool, error)
    UpdateSession(ctx context.Context, s *Session, expectedState string) error
    CreateSnapshot(ctx context.Context, s *Snapshot) error
    GetLatestSnapshot(ctx context.Context, sessionID int64) (*Snapshot, error)
    ListSnapshots(ctx context.Context, sessionID int64, p Page) ([]Snapshot, bool, error)
    CreateLease(ctx context.Context, l *Lease) error
    GetActiveLease(ctx context.Context, sessionID int64) (*Lease, error)
    ReleaseLease(ctx context.Context, sessionID int64, holder, reason string) error
    ReleaseAllLeases(ctx context.Context, sessionID int64, reason string) error
    CreateTakeover(ctx context.Context, t *Takeover) error
    GetTakeover(ctx context.Context, id int64) (*Takeover, error)
    GetWaitingTakeover(ctx context.Context, sessionID int64) (*Takeover, error)
    GetLatestTakeover(ctx context.Context, sessionID int64) (*Takeover, error)
    ListTakeovers(ctx context.Context, f TakeoverFilter) ([]Takeover, bool, error)
    UpdateTakeover(ctx context.Context, t *Takeover, expectedStatus string) error
}
type CodeRepoRepository interface {
    CreateCodeRepo(ctx context.Context, r *CodeRepository) error
    GetCodeRepo(ctx context.Context, id int64) (*CodeRepository, error)
    ListCodeRepos(ctx context.Context, f CodeRepoFilter) ([]CodeRepository, bool, error)
    UpdateCodeRepo(ctx context.Context, r *CodeRepository) error
    DeleteCodeRepo(ctx context.Context, id int64) error
}

// application
type Clock interface { Now() time.Time }
type LoginChecker interface { Check(ctx context.Context, env Environment, state StorageState) (LoginCheckResult, error) }
type GitValidator interface { Validate(ctx context.Context, path, branch string) (GitValidation, error) }
const DefaultCompanyKey = "default"

// httpapi
func WriteData(w http.ResponseWriter, status int, data any)
func WritePage(w http.ResponseWriter, items any, next string, hasMore bool)
func WriteServiceError(w http.ResponseWriter, r *http.Request, err error)
func ParsePage(r *http.Request) (domain.Page, error)
func PathID(r *http.Request) (int64, error)
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error
func ActorID(ctx context.Context) *int64
```

- [ ] **Step 1: 写失败的领域测试**

```go
func TestSessionExpiring(t *testing.T) {
    now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
    s := Session{State: StateValid, CreatedAt: now.Add(-time.Hour), ExpiresAt: ptrTime(now.Add(4 * time.Minute))}
    if !s.Expiring(now) { t.Fatal("want expiring") }
}
func TestParseStorageStateRejectsOversizeCookieValue(t *testing.T) {
    raw := json.RawMessage(`{"cookies":[{"name":"c","value":"` + strings.Repeat("x", 4097) + `","domain":"d","path":"/"}]}`)
    if _, err := ParseStorageState("storage_state", raw); err == nil { t.Fatal("want error") }
}
func TestAppModelKeyMustMatchKind(t *testing.T) {
    if err := ValidateAppModelKey("api", "route:/orders"); err == nil { t.Fatal("want error") }
}
func TestValidateBranchRejectsLeadingDash(t *testing.T) {
    if err := ValidateBranch("-evil"); err == nil { t.Fatal("want error") }
}
```

- [ ] **Step 2: 跑失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/resource/domain -run "TestSessionExpiring|TestParseStorageState|TestAppModelKey|TestValidateBranch" -v`

Expected: FAIL（类型/函数未定义）。

- [ ] **Step 3: 实现领域类型与校验**

按 Interfaces 签名实现；DDL 见 spec §5。`ParseStorageState` 逐数组/字段校验大小上限；`CanonicalJSON` 用 `json.Marshal`（map 键排序）；`Session.Expiring` 用 `expires_at-created_at` 的 10% 与 5min 取大。

- [ ] **Step 4: 跑通过**

Run: 同 Step 2。Expected: PASS。

- [ ] **Step 5: 写失败的 HTTP 助手测试**

```go
func TestWriteServiceErrorMapsInvalid(t *testing.T) {
    rr := httptest.NewRecorder(); req := httptest.NewRequest("POST", "/api/resources/systems", nil)
    WriteServiceError(rr, req, domain.NewInvalid("key", "invalid_key", "bad key"))
    if rr.Code != http.StatusUnprocessableEntity { t.Fatalf("got %d", rr.Code) }
}
func TestPathIDRejectsNonNumeric(t *testing.T) {
    req := httptest.NewRequest("GET", "/api/resources/systems/abc", nil)
    req.SetPathValue("id", "abc")
    if _, err := PathID(req); err == nil { t.Fatal("want error") }
}
```

- [ ] **Step 6: 跑失败**：`go test ./internal/resource/interfaces/httpapi -run "TestWriteServiceError|TestPathID" -v` → FAIL。

- [ ] **Step 7: 实现 `helpers.go`**

映射：invalid→422 `invalid_argument`，not_found→404 `not_found`，conflict/state_conflict/lease_conflict→409 `conflict`，dependency→409 `conflict`；`details.field`/`details.reason`；`ActorID` 从 `identityhttp.UserFromContext` 取 `user.ID`。

- [ ] **Step 8: 跑通过**：同 Step 6。Expected: PASS。

- [ ] **Step 9: 写失败的迁移集成测试**

```go
//go:build integration
func TestResourceMigrationsCreateTablesAndSeedCompany(t *testing.T) {
    pool := testutil.OpenTestDB(t)
    var companyID int64
    if err := pool.QueryRow(context.Background(), `SELECT id FROM companies WHERE key='default'`).Scan(&companyID); err != nil { t.Fatal(err) }
    for _, table := range []string{"systems","environments","app_model_nodes","accounts","account_system_accesses","sessions","session_snapshots","session_leases","manual_takeovers","code_repositories"} {
        var n int
        if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n); err != nil { t.Fatalf("%s: %v", table, err) }
    }
}
```

- [ ] **Step 10: 跑失败**

Run: `powershell -File scripts/test-integration.ps1 ./internal/resource/integration -run TestResourceMigrations -v`

Expected: FAIL（`companies`/表不存在）。

- [ ] **Step 11: 添加三个迁移并追加 `order.txt`**

把 spec §5.1–§5.3 的 DDL 原样写入三个文件；`order.txt` 末尾追加三行文件名。

- [ ] **Step 12: 跑通过**：同 Step 10。Expected: PASS。

- [ ] **Step 13: 提交**

```bash
git add backend/internal/db/migrations backend/internal/resource/domain backend/internal/resource/application backend/internal/resource/interfaces/httpapi backend/internal/resource/integration
git commit -m "feat: add P2 resource schema, domain model, ports and HTTP helpers"
```

---

### Task 3: Catalog PostgreSQL 仓储（System/Environment/AppModel/Company）

**Wave:** W1 · **并发:** 串行（同 postgres 包；与 T7/T8/T9/T10 并行） · **依赖:** T2
**写作用域:** `backend/internal/resource/infrastructure/postgres/catalog_repository.go`, `backend/internal/resource/infrastructure/postgres/catalog_repository_integration_test.go`

**Files:**
- Create: `backend/internal/resource/infrastructure/postgres/catalog_repository.go`
- Test: `backend/internal/resource/infrastructure/postgres/catalog_repository_integration_test.go`

**Interfaces:**
- Consumes: `domain.SystemRepository/EnvironmentRepository/AppModelRepository/CompanyRepository`（T2）。
- Produces:
```go
type CatalogRepository struct { pool *pgxpool.Pool }
func NewCatalogRepository(pool *pgxpool.Pool) *CatalogRepository
// 实现四个 domain 接口；List* 多取 1 行判断 hasMore。
```

- [ ] **Step 1: 写失败的集成测试**

覆盖：创建 system→env→app model 并回读；`UNIQUE` 冲突映射 `domain.KindConflict`；`ListSystems` 分页 `hasMore`；`CountEnvironments`；默认公司查询；app model 按 key 批量查询。

```go
//go:build integration
func TestCatalogRepositoryRoundTrip(t *testing.T) {
    pool := testutil.OpenTestDB(t)
    repo := postgres.NewCatalogRepository(pool)
    company, err := repo.GetCompanyByKey(context.Background(), "default")
    if err != nil { t.Fatal(err) }
    sys := &domain.System{CompanyID: company.ID, Key: "shop", Name: "Shop", Kind: "web", BaseURL: "http://localhost:13335"}
    if err := repo.CreateSystem(context.Background(), sys); err != nil { t.Fatal(err) }
    // ... create env, list, assert hasMore=false
}
```

- [ ] **Step 2: 跑失败**

Run: `powershell -File scripts/test-integration.ps1 ./internal/resource/infrastructure/postgres -run TestCatalogRepository -v`

Expected: FAIL（`NewCatalogRepository` 未定义）。

- [ ] **Step 3: 实现仓储**

手写 SQL；`pgx.ErrNoRows`→`domain.NewNotFound`；`SQLSTATE 23505`→`domain.NewConflict("duplicate_key", ...)`；`23503`→`domain.NewDependency`。List 查询 `ORDER BY id` + `id > AfterID` + `LIMIT limit+1`。

- [ ] **Step 4: 跑通过**：同 Step 2。Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/resource/infrastructure/postgres/catalog_repository.go backend/internal/resource/infrastructure/postgres/catalog_repository_integration_test.go
git commit -m "feat: add catalog postgres repository"
```

---

### Task 4: Access PostgreSQL 仓储（Account/Access）

**Wave:** W1 · **并发:** 串行（依赖 T3；与 T7/T8/T9/T10 并行） · **依赖:** T3
**写作用域:** `backend/internal/resource/infrastructure/postgres/access_repository.go`, `backend/internal/resource/infrastructure/postgres/access_repository_integration_test.go`

**Files:**
- Create: `backend/internal/resource/infrastructure/postgres/access_repository.go`
- Test: `backend/internal/resource/infrastructure/postgres/access_repository_integration_test.go`

**Interfaces:**
- Consumes: `domain.AccountRepository/AccessRepository`（T2）。
- Produces:
```go
type AccessRepository struct { pool *pgxpool.Pool }
func NewAccessRepository(pool *pgxpool.Pool) *AccessRepository
// 实现 AccountRepository 与 AccessRepository。
```

- [ ] **Step 1: 写失败的集成测试**

覆盖：account + access 往返；`credential` 明文读写；`UNIQUE(account_id,system_id)` 冲突；`credential` 不出现在任何 List 投影之外（仓储层允许读取，HTTP 层禁止）；`CountSessions` 为 0；按 account 列 access；分页。

- [ ] **Step 2: 跑失败**：`powershell -File scripts/test-integration.ps1 ./internal/resource/infrastructure/postgres -run TestAccessRepository -v` → FAIL。

- [ ] **Step 3: 实现仓储**：同 T3 的错误映射与分页模式；`UpdateAccess` 用全字段 UPDATE。

- [ ] **Step 4: 跑通过**：同 Step 2。Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/resource/infrastructure/postgres/access_repository.go backend/internal/resource/infrastructure/postgres/access_repository_integration_test.go
git commit -m "feat: add access postgres repository"
```

---

### Task 5: Session PostgreSQL 仓储（Session/Snapshot/Lease/Takeover）

**Wave:** W1 · **并发:** 串行（依赖 T4；与 T7/T8/T9/T10 并行） · **依赖:** T4
**写作用域:** `backend/internal/resource/infrastructure/postgres/session_repository.go`, `backend/internal/resource/infrastructure/postgres/session_repository_integration_test.go`

**Files:**
- Create: `backend/internal/resource/infrastructure/postgres/session_repository.go`
- Test: `backend/internal/resource/infrastructure/postgres/session_repository_integration_test.go`

**Interfaces:**
- Consumes: `domain.SessionRepository`（T2）。
- Produces:
```go
type SessionRepository struct { pool *pgxpool.Pool }
func NewSessionRepository(pool *pgxpool.Pool) *SessionRepository
// UpdateSession: UPDATE ... WHERE id=$1 AND state=$2；0 行→domain.NewStateConflict
// CreateLease: 部分唯一索引冲突→domain.NewLeaseConflict
```

- [ ] **Step 1: 写失败的集成测试**

覆盖：session 创建/读取；`UpdateSession` 正确 CAS、旧 state 失败返回 `KindStateConflict`；snapshot 追加与 `GetLatestSnapshot`；lease 唯一冲突与释放；takeover 状态 CAS；`ListSessions` 过滤与分页。测试用**原生 SQL** seed account/system/env/access，避免依赖 T3/T4 的并行实现。

```go
//go:build integration
func TestSessionRepositoryCAS(t *testing.T) {
    pool := testutil.OpenTestDB(t); repo := postgres.NewSessionRepository(pool)
    // seed account/system/env/access via catalog/access repos
    sess := &domain.Session{ /* ... State: domain.StatePending ... */ }
    if err := repo.CreateSession(context.Background(), sess); err != nil { t.Fatal(err) }
    sess.State = domain.StateValid
    if err := repo.UpdateSession(context.Background(), sess, domain.StatePending); err != nil { t.Fatal(err) }
    if err := repo.UpdateSession(context.Background(), sess, domain.StatePending); err == nil { t.Fatal("want state conflict") }
}
```

- [ ] **Step 2: 跑失败**：`powershell -File scripts/test-integration.ps1 ./internal/resource/infrastructure/postgres -run TestSessionRepository -v` → FAIL。

- [ ] **Step 3: 实现仓储**；lease 过期回收仅用 `released_at IS NULL AND expires_at > now()` 过滤，不做后台任务。

- [ ] **Step 4: 跑通过**：同 Step 2。Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/resource/infrastructure/postgres/session_repository.go backend/internal/resource/infrastructure/postgres/session_repository_integration_test.go
git commit -m "feat: add session postgres repository"
```

---

### Task 6: CodeRepository 仓储与只读 Git 校验器

**Wave:** W1 · **并发:** 串行（依赖 T5；与 T7/T8/T9/T10 并行） · **依赖:** T5
**写作用域:** `backend/internal/resource/infrastructure/postgres/code_repo_repository.go`, `backend/internal/resource/infrastructure/postgres/code_repo_repository_integration_test.go`, `backend/internal/resource/infrastructure/gitexec/`

**Files:**
- Create: `backend/internal/resource/infrastructure/postgres/code_repo_repository.go`
- Create: `backend/internal/resource/infrastructure/gitexec/validator.go`
- Test: `backend/internal/resource/infrastructure/postgres/code_repo_repository_integration_test.go`
- Test: `backend/internal/resource/infrastructure/gitexec/validator_test.go`

**Interfaces:**
- Consumes: `domain.CodeRepoRepository`, `application.GitValidator`（T2）。
- Produces:
```go
type CodeRepository struct { pool *pgxpool.Pool }
func NewCodeRepository(pool *pgxpool.Pool) *CodeRepository

type Validator struct { timeout time.Duration }
func NewValidator() *Validator // timeout 5s
func (v *Validator) Validate(ctx context.Context, path, branch string) (domain.GitValidation, error)
// 错误：domain.NewInvalid("localPath", "path_not_found"|"path_not_git_repo", ...)
//       domain.NewInvalid("fixedBranch", "branch_not_found", ...)
```

- [ ] **Step 1: 写失败的 Git 校验测试**

```go
func TestValidateRejectsNonRepository(t *testing.T) {
    dir := t.TempDir()
    _, err := gitexec.NewValidator().Validate(context.Background(), dir, "main")
    var de *domain.Error
    if !errors.As(err, &de) || de.Reason != "path_not_git_repo" { t.Fatalf("got %v", err) }
}
func TestValidateReturnsCommit(t *testing.T) {
    dir := t.TempDir(); runGit(t, dir, "init"); runGit(t, dir, "config", "user.email", "a@b.c")
    runGit(t, dir, "config", "user.name", "a"); os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
    runGit(t, dir, "add", "."); runGit(t, dir, "commit", "-m", "init")
    got, err := gitexec.NewValidator().Validate(context.Background(), dir, "master")
    if err != nil || len(got.Commit) != 40 { t.Fatalf("commit=%q err=%v", got.Commit, err) }
}
```

- [ ] **Step 2: 跑失败**：`go test ./internal/resource/infrastructure/gitexec -run TestValidate -v` → FAIL。

- [ ] **Step 3: 实现 `gitexec.Validator`**

`exec.CommandContext` 固定参数；`cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")`；先 `os.Stat` 目录，再 `rev-parse --is-inside-work-tree`、`rev-parse --verify --quiet <branch>^{commit}`、`rev-parse <branch>^{commit}`；禁止 `--` 之外的用户参数拼接；分支正则先行校验。

- [ ] **Step 4: 跑通过**：同 Step 2。Expected: PASS。

- [ ] **Step 5: 写失败的仓储测试**

覆盖 code repo CRUD、分页、`key` 唯一冲突、`last_commit` 更新。

- [ ] **Step 6: 跑失败**：`powershell -File scripts/test-integration.ps1 ./internal/resource/infrastructure/postgres -run TestCodeRepository -v` → FAIL。

- [ ] **Step 7: 实现仓储**；同 T3 错误映射。

- [ ] **Step 8: 跑通过**：同 Step 6。Expected: PASS。

- [ ] **Step 9: 提交**

```bash
git add backend/internal/resource/infrastructure/postgres/code_repo_repository.go backend/internal/resource/infrastructure/postgres/code_repo_repository_integration_test.go backend/internal/resource/infrastructure/gitexec
git commit -m "feat: add code repository storage and read-only git validator"
```
---

### Task 7: HTTP login_check 客户端

**Wave:** W1 · **并发:** 可并发（与 T3–T6 串行链、T8/T9/T10 并行） · **依赖:** T2
**写作用域:** `backend/internal/resource/infrastructure/httpcheck/`

**Files:**
- Create: `backend/internal/resource/infrastructure/httpcheck/login_check.go`
- Test: `backend/internal/resource/infrastructure/httpcheck/login_check_test.go`

**Interfaces:**
- Consumes: `domain.Environment`、`domain.StorageState`、`domain.LoginCheckResult`（T2）。
- Produces:
```go
type Checker struct { client *http.Client }
func NewChecker() *Checker // timeout 10s，默认重定向策略
func (c *Checker) Check(ctx context.Context, env domain.Environment, state domain.StorageState) (domain.LoginCheckResult, error)
```

- [ ] **Step 1: 写失败的测试**

```go
func TestCheckPassesOnStatusAndBody(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.Header.Get("Cookie") != "sid=abc" { t.Errorf("cookie=%q", r.Header.Get("Cookie")) }
        fmt.Fprint(w, "Fixture Account")
    }))
    defer srv.Close()
    env := domain.Environment{BaseURL: srv.URL, LoginCheck: &domain.LoginCheck{URL: "/account", ExpectStatus: []int{200}, BodyContains: "Fixture Account"}}
    state := domain.StorageState{Kind: "storage_state", Cookies: []domain.Cookie{{Name: "sid", Value: "abc"}}}
    got, err := httpcheck.NewChecker().Check(context.Background(), env, state)
    if err != nil || got.Observed != "allowed" { t.Fatalf("got %+v err=%v", got, err) }
}
func TestCheckNotConfiguredIsDegraded(t *testing.T) {
    got, err := httpcheck.NewChecker().Check(context.Background(), domain.Environment{}, domain.StorageState{})
    if err != nil || !got.Degraded || got.Reason != "login_check_not_configured" { t.Fatalf("got %+v err=%v", got, err) }
}
```

- [ ] **Step 2: 跑失败**：`go test ./internal/resource/infrastructure/httpcheck -run TestCheck -v` → FAIL。

- [ ] **Step 3: 实现 Checker**

`LoginCheck` 为空/URL 空→degraded 结果；URL 绝对或与 `BaseURL` 拼接；storage_state 组装 `Cookie` 头，token 原样 `Headers`；GET；响应体最多读 1 MiB；状态码∈ExpectStatus 且 BodyContains 命中→`allowed`，否则 `denied`；网络/超时→`error`+`env_unreachable`。不因 HTTP 状态返回 error。

- [ ] **Step 4: 跑通过**：同 Step 2。Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/resource/infrastructure/httpcheck
git commit -m "feat: add read-only login check client"
```

---

### Task 8: 前端资源页骨架、系统与环境、共享 API 类型

**Wave:** W1 · **并发:** 可并发（与 T3–T7、T9/T10 并行） · **依赖:** T2
**写作用域:** `frontend/src/features/resources/`, `frontend/src/app/(app)/resources/page.tsx`

**Files:**
- Create: `frontend/src/app/(app)/resources/page.tsx`（替换占位）
- Create: `frontend/src/features/resources/types.ts`
- Create: `frontend/src/features/resources/api.ts`
- Create: `frontend/src/features/resources/ResourcesPage.tsx`
- Create: `frontend/src/features/resources/SystemsPanel.tsx`
- Create: `frontend/src/features/resources/EnvironmentsPanel.tsx`
- Create: `frontend/src/features/resources/ResourcesPage.test.tsx`
- Create: `frontend/src/features/resources/SystemsPanel.test.tsx`
- Create: `frontend/src/features/resources/EnvironmentsPanel.test.tsx`

**Interfaces:**
- Consumes: `apiGet/apiPost/apiPatch/apiDelete`（`@/lib/api-client`）、`useAuth`、`StatusBadge`、`EmptyState`。
- Produces（T14/T16 使用）:
```ts
// 组件 props
SystemsPanel({ canWrite, selectedId, onSelect }: { canWrite:boolean; selectedId:number|null; onSelect:(id:number)=>void })
EnvironmentsPanel({ systemId, canWrite }: { systemId:number; canWrite:boolean })
ResourcesPage(): JSX.Element // 读取 useAuth().user.role
```

```ts
// types.ts
export type SystemDTO = { id:number; key:string; name:string; kind:"web"|"api"; baseUrl:string; defaultSessionTtlSeconds:number; notes:string; status:string; createdAt:string; updatedAt:string }
export type EnvironmentDTO = { id:number; systemId:number; key:string; name:string; baseUrl:string; kind:string; isProd:boolean; allowedHosts:string[]; loginCheck: {url:string; expectStatus:number[]; bodyContains:string} | null; allowAutoLogin:boolean; requireApproval:boolean }
export type AppModelNodeDTO = { id:number; systemId:number; parentId:number|null; kind:string; key:string; name:string; path:string; method:string; source:string; meta:unknown }
export type AccountDTO = { id:number; key:string; label:string; username:string; kind:string; status:string; notes:string }
export type AccessDTO = { id:number; accountId:number; systemId:number; authType:string; credentialSet:boolean; usernameOverride:string; loginUrl:string; enabled:boolean; concurrencyLimit:number; sessionTtlSeconds:number|null; permissionNote:string }
export type SnapshotDTO = { id:number; sessionId:number; kind:string; items:unknown; summary:unknown; permissionHash:string; degraded:boolean; capturedAt:string }
export type SessionDTO = { id:number; accountId:number; systemId:number; environmentId:number; accessId:number; state:string; acquisition:string; storageStateKind:string; cookieCount:number; originCount:number; storageStateHash:string; expiresAt:string|null; lastValidatedAt:string|null; lastUsedAt:string|null; failureReason:string; invalidatedReason:string; expiring:boolean; latestSnapshot?:SnapshotDTO|null; activeLease?:{holder:string;expiresAt:string}|null; latestTakeover?:{id:number;status:string;reason:string}|null }
export type Page<T> = { items:T[]; nextCursor:string|null; hasMore:boolean }

// api.ts
export function listSystems(params?: {limit?:number; cursor?:string; status?:string}): Promise<Page<SystemDTO>>
export function createSystem(input: Partial<SystemDTO>): Promise<SystemDTO>
export function updateSystem(id:number, input: Partial<SystemDTO>): Promise<SystemDTO>
export function deleteSystem(id:number): Promise<void>
export function listEnvironments(systemId:number): Promise<EnvironmentDTO[]>
export function createEnvironment(systemId:number, input: Partial<EnvironmentDTO>): Promise<EnvironmentDTO>
export function updateEnvironment(id:number, input: Partial<EnvironmentDTO>): Promise<EnvironmentDTO>
export function deleteEnvironment(id:number): Promise<void>
export function listAppModels(systemId:number, params?: {kind?:string}): Promise<Page<AppModelNodeDTO>>
export function createAppModel(systemId:number, input: Partial<AppModelNodeDTO>): Promise<AppModelNodeDTO>
export function importAppModels(systemId:number, nodes: Partial<AppModelNodeDTO>[]): Promise<{created:number; updated:number}>
export function updateAppModel(id:number, input: Partial<AppModelNodeDTO>): Promise<AppModelNodeDTO>
export function deleteAppModel(id:number): Promise<void>
export function listAccounts(params?: {limit?:number; cursor?:string; status?:string}): Promise<Page<AccountDTO>>
export function createAccount(input: Partial<AccountDTO>): Promise<AccountDTO>
export function updateAccount(id:number, input: Partial<AccountDTO>): Promise<AccountDTO>
export function deleteAccount(id:number): Promise<void>
export function listAccesses(accountId:number): Promise<AccessDTO[]>
export function createAccess(accountId:number, input: Partial<AccessDTO> & {credential?:string}): Promise<AccessDTO>
export function updateAccess(id:number, input: Partial<AccessDTO> & {credential?:string}): Promise<AccessDTO>
export function deleteAccess(id:number): Promise<void>
export function listSessions(params?: {accountId?:number; systemId?:number; environmentId?:number; state?:string; limit?:number; cursor?:string}): Promise<Page<SessionDTO>>
export function getSession(id:number): Promise<SessionDTO>
export function importSession(accessId:number, input: {environmentId:number; kind:"storage_state"|"token"; storageState?:unknown; headers?:Record<string,string>; expiresAt?:string; note?:string}): Promise<SessionDTO>
export function validateSession(id:number): Promise<SessionDTO>
export function confirmSession(id:number, note?:string): Promise<SessionDTO>
export function invalidateSession(id:number, reason:string): Promise<SessionDTO>
export function revokeSession(id:number, reason:string): Promise<SessionDTO>
export function listSnapshots(sessionId:number): Promise<Page<SnapshotDTO>>
export function requestTakeover(sessionId:number, input:{reason:string; instructions?:unknown; expiresInSeconds?:number}): Promise<{takeover:{id:number;status:string}; session:SessionDTO}>
export function completeTakeover(id:number, input:{kind:"storage_state"|"token"; storageState?:unknown; headers?:Record<string,string>}): Promise<{takeover:{id:number;status:string}; session:SessionDTO}>
export function cancelTakeover(id:number): Promise<{id:number;status:string}>
```

- [ ] **Step 1: 写失败的前端测试**

```tsx
// SystemsPanel.test.tsx
vi.mock("./api", () => ({ listSystems: vi.fn().mockResolvedValue({items:[{id:1,key:"shop",name:"Shop",kind:"web",baseUrl:"http://localhost:13335",status:"active"}],nextCursor:null,hasMore:false}), createSystem: vi.fn() }))
it("renders systems and hides create for readonly", async () => {
  render(<SystemsPanel canWrite={false} onSelect={vi.fn()} />)
  expect(await screen.findByText("Shop")).toBeInTheDocument()
  expect(screen.queryByRole("button", {name:"新建系统"})).not.toBeInTheDocument()
})
// ResourcesPage.test.tsx
vi.mock("@/features/auth/AuthProvider", () => ({ useAuth: () => ({ user: { role: "user" } }) }))
it("shows readonly badge and all four tabs", () => {
  render(<ResourcesPage />)
  expect(screen.getByText("只读")).toBeInTheDocument()
  for (const name of ["系统与环境","应用模型","账号与访问","会话"]) expect(screen.getByRole("tab",{name})).toBeInTheDocument()
})
```

- [ ] **Step 2: 跑失败**：`cd frontend; pnpm vitest run src/features/resources` → FAIL。

- [ ] **Step 3: 实现 `types.ts`/`api.ts`**

`api.ts` 只做路径拼接与类型标注：列表 GET 返回 `{data,next_cursor,has_more}` 后映射为 `Page<T>`；创建/更新用 `apiPost/apiPatch`；删除 `apiDelete`；不直接 `fetch`。

- [ ] **Step 4: 实现 `ResourcesPage/SystemsPanel/EnvironmentsPanel` 与路由页**

`ResourcesPage` 使用 Radix Tabs，URL `?tab=` 深链；`canWrite = user.role === "admin"`；普通用户顶部渲染「只读」徽章。系统面板：列表 + 新建/编辑表单（key/name/kind/baseUrl/defaultSessionTtlSeconds/notes）；环境面板：选中系统后列表 + 表单（login_check 三个字段）。

- [ ] **Step 5: 跑通过**：同 Step 2。Expected: PASS。

- [ ] **Step 6: 跑前端门禁**：`pnpm lint && pnpm typecheck`。Expected: PASS。

- [ ] **Step 7: 提交**

```bash
git add frontend/src/features/resources frontend/src/app/\(app\)/resources/page.tsx
git commit -m "feat: add resource shell, systems and environments panels"
```

---

### Task 9: 前端代码仓页

**Wave:** W1 · **并发:** 可并发（与 T3–T8、T10 并行） · **依赖:** T2
**写作用域:** `frontend/src/features/code-repos/`, `frontend/src/app/(app)/code-repos/page.tsx`

**Files:**
- Create: `frontend/src/app/(app)/code-repos/page.tsx`（替换占位）
- Create: `frontend/src/features/code-repos/types.ts`
- Create: `frontend/src/features/code-repos/api.ts`
- Create: `frontend/src/features/code-repos/CodeReposPage.tsx`
- Create: `frontend/src/features/code-repos/CodeReposPage.test.tsx`

**Interfaces:**
- Consumes: `apiGet/apiPost/apiPatch/apiDelete`、`useAuth`、`StatusBadge`。
- Produces:
```ts
export type CodeRepoDTO = { id:number; key:string; name:string; localPath:string; fixedBranch:string; includeGlobs:string[]; excludeGlobs:string[]; status:string; lastCommit:string; lastValidatedAt:string|null; lastValidationError:string; lastIndexedAt:string|null }
export function listCodeRepos(): Promise<{items:CodeRepoDTO[];nextCursor:string|null;hasMore:boolean}>
export function createCodeRepo(input: Partial<CodeRepoDTO>): Promise<CodeRepoDTO>
export function updateCodeRepo(id:number, input: Partial<CodeRepoDTO>): Promise<CodeRepoDTO>
export function deleteCodeRepo(id:number): Promise<void>
export function validateCodeRepo(id:number): Promise<CodeRepoDTO>
```

- [ ] **Step 1: 写失败的测试**

```tsx
vi.mock("./api", () => ({ listCodeRepos: vi.fn().mockResolvedValue({items:[{id:1,key:"eztest",name:"EzTest",localPath:"D:/repo",fixedBranch:"dev",lastCommit:"abc",status:"active",lastIndexedAt:null}],nextCursor:null,hasMore:false}) }))
it("shows commit and index placeholder", async () => {
  render(<CodeReposPage />)
  expect(await screen.findByText("abc")).toBeInTheDocument()
  expect(screen.getByText(/未索引（P4）/)).toBeInTheDocument()
})
```

- [ ] **Step 2: 跑失败**：`cd frontend; pnpm vitest run src/features/code-repos` → FAIL。

- [ ] **Step 3: 实现页面与 API**

表格列：名称/本地路径/固定分支/当前 commit/最近校验/状态；表单含 key/name/localPath/fixedBranch/includeGlobs/excludeGlobs（逗号分隔）；「重新校验」调用 validate；保存失败显示 422 `reason`；普通用户只读。

- [ ] **Step 4: 跑通过**：同 Step 2。Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/features/code-repos frontend/src/app/\(app\)/code-repos/page.tsx
git commit -m "feat: add code repositories page"
```

---

### Task 10: fixture target 与 e2e 启动接线

**Wave:** W1 · **并发:** 可并发（与 T3–T9 并行） · **依赖:** T2
**写作用域:** `scripts/fixture-target.cjs`, `scripts/e2e.ps1`

**Files:**
- Create: `scripts/fixture-target.cjs`
- Modify: `scripts/e2e.ps1`（启动/探活/停止 fixture）

**Interfaces:**
- Consumes: Node 内置 `http`；无新依赖。
- Produces: `http://127.0.0.1:13335` 的 `/login`、`/account`、`/api/profile`、`/health`；E2E 依赖的固定凭据 `fixture / fixture-pass` 与 token `fixture-token`。

- [ ] **Step 1: 实现 fixture 脚本**

- `GET /login`：HTML 表单（username/password）。
- `POST /login`：解析 urlencoded；正确则 `Set-Cookie: fixture_session=ok; HttpOnly; Path=/`，302 `/account`；错误 401。
- `GET /account`：cookie 有效→200 且 body 含 `Fixture Account`；否则 302 `/login`。
- `GET /api/profile`：`Authorization: Bearer fixture-token`→200 JSON `{name:"fixture"}`；否则 401。
- `GET /health`→`ok`；端口 `process.env.FIXTURE_PORT || 13335`。

- [ ] **Step 2: 手动验证**

Run（两个终端）：
```powershell
node scripts/fixture-target.cjs
Invoke-WebRequest http://127.0.0.1:13335/health
Invoke-WebRequest http://127.0.0.1:13335/login -Method Post -Body @{username="fixture";password="fixture-pass"} -MaximumRedirection 0
```
Expected: `ok`；302 且带 `Set-Cookie`。

- [ ] **Step 3: 修改 `scripts/e2e.ps1`**

在启动 API/Web 之后、Playwright 之前启动 `node scripts/fixture-target.cjs`（后台进程，日志 `.tmp/fixture-target.log`），轮询 `/health` 最多 10s；`finally` 中停止进程。保持现有 fake LLM 与端口不变。

- [ ] **Step 4: 验证接线**

Run: `powershell -File scripts/e2e.ps1 -ListTests`（若脚本无此参数，则运行现有 E2E 冒烟 `frontend/e2e/browser-smoke.spec.ts`：`powershell -File scripts/e2e.ps1`）

Expected: fixture 启动、现有 E2E 不被破坏。

- [ ] **Step 5: 提交**

```bash
git add scripts/fixture-target.cjs scripts/e2e.ps1
git commit -m "test: add deterministic fixture target for P2 session flows"
```

---

### Task 11: Catalog 应用服务与 HTTP Handler

**Wave:** W2 · **并发:** 串行（同 application/httpapi 包；与 T15 并行） · **依赖:** T3
**写作用域:** `backend/internal/resource/application/catalog_service.go`, `backend/internal/resource/application/catalog_service_test.go`, `backend/internal/resource/interfaces/httpapi/handlers_catalog.go`, `backend/internal/resource/interfaces/httpapi/handlers_catalog_test.go`

**Files:**
- Create: `backend/internal/resource/application/catalog_service.go`
- Create: `backend/internal/resource/application/catalog_service_test.go`
- Create: `backend/internal/resource/interfaces/httpapi/handlers_catalog.go`
- Create: `backend/internal/resource/interfaces/httpapi/handlers_catalog_test.go`

**Interfaces:**
- Consumes: T2 的 domain 仓储接口与 HTTP helpers；T3 的 `postgres.CatalogRepository`。
- Produces:

```go
type CatalogService struct { /* company, systems, envs, appModels, clock */ }
func NewCatalogService(companies domain.CompanyRepository, systems domain.SystemRepository, envs domain.EnvironmentRepository, appModels domain.AppModelRepository, clock Clock) *CatalogService
type CreateSystemInput struct { Key, Name, Kind, BaseURL, Notes string; DefaultSessionTTLSeconds int }
type UpdateSystemInput struct { Name, Kind, BaseURL, Notes *string; DefaultSessionTTLSeconds *int; Status *string }
type CreateEnvironmentInput struct { Key, Name, BaseURL, Kind string; IsProd bool; AllowedHosts []string; LoginCheck *domain.LoginCheck; AllowAutoLogin, RequireApproval bool }
type UpdateEnvironmentInput struct { /* 指针字段 */ }
type AppModelInput struct { Key, Kind, Name, Path, Method, ParentKey string; Meta json.RawMessage }
func (s *CatalogService) GetSystem(ctx context.Context, id int64) (*domain.System, error)
func (s *CatalogService) ListSystems(ctx context.Context, f domain.SystemFilter) ([]domain.System, bool, error)
func (s *CatalogService) CreateSystem(ctx context.Context, in CreateSystemInput) (*domain.System, error)
func (s *CatalogService) UpdateSystem(ctx context.Context, id int64, in UpdateSystemInput) (*domain.System, error)
func (s *CatalogService) DeleteSystem(ctx context.Context, id int64) error
func (s *CatalogService) ListEnvironments(ctx context.Context, systemID int64) ([]domain.Environment, error)
func (s *CatalogService) GetEnvironment(ctx context.Context, id int64) (*domain.Environment, error)
func (s *CatalogService) CreateEnvironment(ctx context.Context, systemID int64, in CreateEnvironmentInput) (*domain.Environment, error)
func (s *CatalogService) UpdateEnvironment(ctx context.Context, id int64, in UpdateEnvironmentInput) (*domain.Environment, error)
func (s *CatalogService) DeleteEnvironment(ctx context.Context, id int64) error
func (s *CatalogService) ListAppModels(ctx context.Context, f domain.AppModelFilter) ([]domain.AppModelNode, bool, error)
func (s *CatalogService) GetAppModel(ctx context.Context, id int64) (*domain.AppModelNode, error)
func (s *CatalogService) CreateAppModel(ctx context.Context, systemID int64, in AppModelInput) (*domain.AppModelNode, error)
func (s *CatalogService) ImportAppModels(ctx context.Context, systemID int64, inputs []AppModelInput) (created, updated int, err error)
func (s *CatalogService) UpdateAppModel(ctx context.Context, id int64, in AppModelInput) (*domain.AppModelNode, error)
func (s *CatalogService) DeleteAppModel(ctx context.Context, id int64) error

type CatalogHandler struct { svc *CatalogService }
func NewCatalogHandler(svc *CatalogService) *CatalogHandler
func (h *CatalogHandler) RegisterRoutes(mux *http.ServeMux, guard func(http.Handler) http.Handler)
```

- [ ] **Step 1: 写失败的服务测试**

覆盖：创建 system 使用默认公司；无效 key/baseURL→422；删除有环境/访问的 system→409 `has_dependencies`；创建环境时 `allowedHosts` 空可保存、login_check 校验；应用模型导入重复 key 去重/父缺失/环→422；导入 5001 节点→422；列表 `hasMore`。

```go
func TestDeleteSystemRejectsDependencies(t *testing.T) {
    svc, systems := newCatalogServiceWithFakes(t)
    systems.countEnvs = 1
    err := svc.DeleteSystem(context.Background(), 7)
    var de *domain.Error
    if !errors.As(err, &de) || de.Reason != "has_dependencies" { t.Fatalf("got %v", err) }
}
```

- [ ] **Step 2: 跑失败**：`go test ./internal/resource/application -run TestCatalogService -v` → FAIL。

- [ ] **Step 3: 实现 `CatalogService`**

key/baseURL/login_check 校验；单公司 `DefaultCompanyKey`；删除前计数；应用模型 key 前缀/kind 校验、父节点存在性与环检测（DFS，深度 ≤32）、导入 ≤5000；upsert 用 `ListAppModelsByKeys` + create/update。

- [ ] **Step 4: 跑通过**：同 Step 2。Expected: PASS。

- [ ] **Step 5: 写失败的 Handler 测试**

覆盖：`POST /api/resources/systems` 201 + DTO；非法 body 422；`GET` 列表分页；写路由在 guard 下 403（用 `identityhttp.RequireAdmin` + 普通用户 context）。

- [ ] **Step 6: 跑失败**：`go test ./internal/resource/interfaces/httpapi -run TestCatalogHandler -v` → FAIL。

- [ ] **Step 7: 实现 Handler 与 DTO**

`RegisterRoutes` 中读路由直接 `mux.Handle`，写路由 `guard(http.HandlerFunc(...))`；body 上限 64 KiB；错误经 `WriteServiceError`；列表用 `WritePage`。

- [ ] **Step 8: 跑通过**：同 Step 6。Expected: PASS。

- [ ] **Step 9: 提交**

```bash
git add backend/internal/resource/application/catalog_service.go backend/internal/resource/application/catalog_service_test.go backend/internal/resource/interfaces/httpapi/handlers_catalog.go backend/internal/resource/interfaces/httpapi/handlers_catalog_test.go
git commit -m "feat: add catalog application service and resource handlers"
```

---

### Task 12: Access 应用服务与 HTTP Handler

**Wave:** W2 · **并发:** 串行（依赖 T11；与 T15 并行） · **依赖:** T4
**写作用域:** `backend/internal/resource/application/access_service.go`, `backend/internal/resource/application/access_service_test.go`, `backend/internal/resource/interfaces/httpapi/handlers_access.go`, `backend/internal/resource/interfaces/httpapi/handlers_access_test.go`

**Files:**
- Create: `backend/internal/resource/application/access_service.go`
- Create: `backend/internal/resource/application/access_service_test.go`
- Create: `backend/internal/resource/interfaces/httpapi/handlers_access.go`
- Create: `backend/internal/resource/interfaces/httpapi/handlers_access_test.go`

**Interfaces:**
- Consumes: T2 的 `CompanyRepository/AccountRepository/AccessRepository/SystemRepository`；T3/T4 仓储。
- Produces:

```go
type AccessService struct { /* companies, accounts, accesses, systems */ }
func NewAccessService(companies domain.CompanyRepository, accounts domain.AccountRepository, accesses domain.AccessRepository, systems domain.SystemRepository) *AccessService
type CreateAccountInput struct { Key, Label, Username, Kind, Notes string }
type UpdateAccountInput struct { Label, Username, Kind, Notes, Status *string }
type CreateAccessInput struct { SystemID int64; AuthType, Credential, UsernameOverride, LoginURL, PermissionNote string; Enabled bool; ConcurrencyLimit int; SessionTTLSeconds *int }
type UpdateAccessInput struct { AuthType, UsernameOverride, LoginURL, PermissionNote *string; Credential *string; Enabled *bool; ConcurrencyLimit *int; SessionTTLSeconds *int; ClearSessionTTL bool }
func (s *AccessService) GetAccount(ctx context.Context, id int64) (*domain.Account, error)
func (s *AccessService) ListAccounts(ctx context.Context, f domain.AccountFilter) ([]domain.Account, bool, error)
func (s *AccessService) CreateAccount(ctx context.Context, in CreateAccountInput) (*domain.Account, error)
func (s *AccessService) UpdateAccount(ctx context.Context, id int64, in UpdateAccountInput) (*domain.Account, error)
func (s *AccessService) DeleteAccount(ctx context.Context, id int64) error
func (s *AccessService) GetAccess(ctx context.Context, id int64) (*domain.Access, error)
func (s *AccessService) ListAccesses(ctx context.Context, accountID int64) ([]domain.Access, error)
func (s *AccessService) CreateAccess(ctx context.Context, accountID int64, in CreateAccessInput) (*domain.Access, error)
func (s *AccessService) UpdateAccess(ctx context.Context, id int64, in UpdateAccessInput) (*domain.Access, error)
func (s *AccessService) DeleteAccess(ctx context.Context, id int64) error

type AccessHandler struct { svc *AccessService }
func NewAccessHandler(svc *AccessService) *AccessHandler
func (h *AccessHandler) RegisterRoutes(mux *http.ServeMux, guard func(http.Handler) http.Handler)
```

- [ ] **Step 1: 写失败的服务测试**

覆盖：account 创建使用默认公司；删除有 access/session 的 account→409；access 创建校验 system 存在、`concurrencyLimit` 范围；PATCH credential 省略不变、空串清除、非空替换；响应 DTO 不含 credential（handler 测试断言）。

- [ ] **Step 2: 跑失败**：`go test ./internal/resource/application -run TestAccessService -v` → FAIL。

- [ ] **Step 3: 实现 `AccessService`**

建 account 用默认公司；建 access 校验 system；`UpdateAccess` 用指针语义：`Credential == nil` 保留，`*Credential == ""` 清除，否则替换；删除前计数。

- [ ] **Step 4: 跑通过**：同 Step 2。Expected: PASS。

- [ ] **Step 5: 写失败的 Handler 测试**

覆盖：创建 201；`credential` 不出现在 JSON；`credentialSet` true/false；普通用户 403；删除依赖 409。

- [ ] **Step 6: 跑失败**：`go test ./internal/resource/interfaces/httpapi -run TestAccessHandler -v` → FAIL。

- [ ] **Step 7: 实现 Handler 与 DTO**

路径：`/api/resources/accounts`、`/api/resources/accounts/{id}/accesses`、`/api/resources/accesses/{id}`；body 上限 64 KiB；DTO 无 credential 字段。

- [ ] **Step 8: 跑通过**：同 Step 6。Expected: PASS。

- [ ] **Step 9: 提交**

```bash
git add backend/internal/resource/application/access_service.go backend/internal/resource/application/access_service_test.go backend/internal/resource/interfaces/httpapi/handlers_access.go backend/internal/resource/interfaces/httpapi/handlers_access_test.go
git commit -m "feat: add account and access application service and handlers"
```

---

### Task 13: Session 应用服务与 HTTP Handler（含接管/租约）

**Wave:** W2 · **并发:** 串行（依赖 T12；与 T15 并行） · **依赖:** T5、T7
**写作用域:** `backend/internal/resource/application/session_service.go`, `backend/internal/resource/application/session_service_test.go`, `backend/internal/resource/interfaces/httpapi/handlers_session.go`, `backend/internal/resource/interfaces/httpapi/handlers_session_test.go`

**Files:**
- Create: `backend/internal/resource/application/session_service.go`
- Create: `backend/internal/resource/application/session_service_test.go`
- Create: `backend/internal/resource/interfaces/httpapi/handlers_session.go`
- Create: `backend/internal/resource/interfaces/httpapi/handlers_session_test.go`

**Interfaces:**
- Consumes: T2 `SessionRepository/AccessRepository/EnvironmentRepository/SystemRepository/LoginChecker/Clock`；T5 仓储；T7 Checker。
- Produces:

```go
type SessionService struct { /* sessions, accesses, environments, systems, checker, clock */ }
func NewSessionService(sessions domain.SessionRepository, accesses domain.AccessRepository, envs domain.EnvironmentRepository, systems domain.SystemRepository, checker LoginChecker, clock Clock) *SessionService
type ImportSessionInput struct { EnvironmentID int64; Kind string; StorageState json.RawMessage; Headers map[string]string; ExpiresAt *time.Time; Note string; ActorID *int64 }
type ReplaceSessionStateInput struct { Kind string; StorageState json.RawMessage; Headers map[string]string }
func (s *SessionService) Get(ctx context.Context, id int64) (*domain.Session, *domain.Snapshot, *domain.Lease, *domain.Takeover, error)
func (s *SessionService) List(ctx context.Context, f domain.SessionFilter) ([]domain.Session, bool, error)
func (s *SessionService) Import(ctx context.Context, accessID int64, in ImportSessionInput) (*domain.Session, *domain.Snapshot, error)
func (s *SessionService) Validate(ctx context.Context, id int64) (*domain.Session, *domain.Snapshot, error)
func (s *SessionService) Confirm(ctx context.Context, id int64, note string, actorID *int64) (*domain.Session, *domain.Snapshot, error)
func (s *SessionService) Invalidate(ctx context.Context, id int64, reason string) (*domain.Session, error)
func (s *SessionService) Revoke(ctx context.Context, id int64, reason string) (*domain.Session, error)
func (s *SessionService) ListSnapshots(ctx context.Context, sessionID int64, page domain.Page) ([]domain.Snapshot, bool, error)
func (s *SessionService) RequestTakeover(ctx context.Context, sessionID int64, reason string, instructions json.RawMessage, expiresIn time.Duration, actorID *int64) (*domain.Takeover, *domain.Session, error)
func (s *SessionService) CompleteTakeover(ctx context.Context, takeoverID int64, in ReplaceSessionStateInput, actorID *int64) (*domain.Takeover, *domain.Session, *domain.Snapshot, error)
func (s *SessionService) CancelTakeover(ctx context.Context, takeoverID int64) (*domain.Takeover, error)
func (s *SessionService) AcquireLease(ctx context.Context, sessionID int64, holder string, ttl time.Duration) (*domain.Lease, error)
func (s *SessionService) ReleaseLease(ctx context.Context, sessionID int64, holder, reason string) error

type SessionHandler struct { svc *SessionService }
func NewSessionHandler(svc *SessionService) *SessionHandler
func (h *SessionHandler) RegisterRoutes(mux *http.ServeMux, guard func(http.Handler) http.Handler)
```

- [ ] **Step 1: 写失败的服务测试**

覆盖：storage_state 导入 + checker allowed → `valid` + 快照 hash；checker 未配置 → `needs_manual` degraded；非法 JSON/超大 cookie/`environmentId` 不属于 access.system → 422；TTL/最早 cookie 过期计算；confirm 从 needs_manual → valid（degraded）；confirm 对 expired/revoked → 409；revoke 释放 lease；takeover complete 替换 storageState 并重跑 checker；lease 二次获取 → `KindLeaseConflict`；CAS 冲突 → `KindStateConflict`。

```go
func TestImportSessionWithPassingCheckIsValid(t *testing.T) {
    svc, f := newSessionServiceWithFakes(t)
    f.checker.result = domain.LoginCheckResult{Observed: "allowed", Status: 200}
    sess, snap, err := svc.Import(context.Background(), f.accessID, validStorageStateInput())
    if err != nil || sess.State != domain.StateValid || snap.Degraded { t.Fatalf("sess=%+v snap=%+v err=%v", sess, snap, err) }
}
```

- [ ] **Step 2: 跑失败**：`go test ./internal/resource/application -run TestSessionService -v` → FAIL。

- [ ] **Step 3: 实现 `SessionService` 核心**

导入：access→environment→system 校验；`ParseStorageState`；hash；TTL；创建 pending；check；快照；CAS 更新 valid/needs_manual。快照 items/summary/permission_hash 按 spec §6.3。confirm/invalidate/revoke/expiry 按 spec §6.1。takeover/lease 按 §6.4/§6.5。所有状态变更 `UpdateSession(..., expectedState)`；0 行→`NewStateConflict`。

- [ ] **Step 4: 跑通过**：同 Step 2。Expected: PASS。

- [ ] **Step 5: 写失败的 Handler 测试**

覆盖：导入 201 + DTO 含 state/latestSnapshot；`storage_state`/`credential` 不在 JSON；confirm 200；状态冲突 409 `session_state_conflict`；普通用户 403；导入 body 6 MiB 上限。

- [ ] **Step 6: 跑失败**：`go test ./internal/resource/interfaces/httpapi -run TestSessionHandler -v` → FAIL。

- [ ] **Step 7: 实现 Handler 与 DTO**

路由按 spec §7.5/§7.6；`DecodeJSON` 上限 `6 << 20`；SessionDTO 字段按 spec §7.5；详情附 `latestSnapshot/activeLease/latestTakeover`；动作 wrap guard。

- [ ] **Step 8: 跑通过**：同 Step 6。Expected: PASS。

- [ ] **Step 9: 提交**

```bash
git add backend/internal/resource/application/session_service.go backend/internal/resource/application/session_service_test.go backend/internal/resource/interfaces/httpapi/handlers_session.go backend/internal/resource/interfaces/httpapi/handlers_session_test.go
git commit -m "feat: add session import validation takeover and lease services"
```

---

### Task 14: CodeRepo 应用服务与 HTTP Handler

**Wave:** W2 · **并发:** 串行（依赖 T13；与 T15 并行） · **依赖:** T6
**写作用域:** `backend/internal/resource/application/coderepo_service.go`, `backend/internal/resource/application/coderepo_service_test.go`, `backend/internal/resource/interfaces/httpapi/handlers_coderepo.go`, `backend/internal/resource/interfaces/httpapi/handlers_coderepo_test.go`

**Files:**
- Create: `backend/internal/resource/application/coderepo_service.go`
- Create: `backend/internal/resource/application/coderepo_service_test.go`
- Create: `backend/internal/resource/interfaces/httpapi/handlers_coderepo.go`
- Create: `backend/internal/resource/interfaces/httpapi/handlers_coderepo_test.go`

**Interfaces:**
- Consumes: T2 `CodeRepoRepository/GitValidator/Clock`；T6 实现。
- Produces:

```go
type CodeRepoService struct { repos domain.CodeRepoRepository; git GitValidator; clock Clock }
func NewCodeRepoService(repos domain.CodeRepoRepository, git GitValidator, clock Clock) *CodeRepoService
type CreateCodeRepoInput struct { Key, Name, LocalPath, FixedBranch string; IncludeGlobs, ExcludeGlobs []string }
type UpdateCodeRepoInput struct { Name, LocalPath, FixedBranch *string; IncludeGlobs, ExcludeGlobs *[]string; Status *string }
func (s *CodeRepoService) Get(ctx context.Context, id int64) (*domain.CodeRepository, error)
func (s *CodeRepoService) List(ctx context.Context, f domain.CodeRepoFilter) ([]domain.CodeRepository, bool, error)
func (s *CodeRepoService) Create(ctx context.Context, in CreateCodeRepoInput) (*domain.CodeRepository, error)
func (s *CodeRepoService) Update(ctx context.Context, id int64, in UpdateCodeRepoInput) (*domain.CodeRepository, error)
func (s *CodeRepoService) Delete(ctx context.Context, id int64) error
func (s *CodeRepoService) Validate(ctx context.Context, id int64) (*domain.CodeRepository, error)

type CodeRepoHandler struct { svc *CodeRepoService }
func NewCodeRepoHandler(svc *CodeRepoService) *CodeRepoHandler
func (h *CodeRepoHandler) RegisterRoutes(mux *http.ServeMux, guard func(http.Handler) http.Handler)
```

- [ ] **Step 1: 写失败的服务测试**

覆盖：创建前校验并写 `last_commit/last_validated_at`；校验失败→422 且不落库；更新时校验失败保留旧值；重新校验刷新 commit；分支/glob 校验。

- [ ] **Step 2: 跑失败**：`go test ./internal/resource/application -run TestCodeRepoService -v` → FAIL。

- [ ] **Step 3: 实现服务**：调用 `GitValidator.Validate`；成功写 commit/validatedAt/error 清空；失败映射 `domain.NewInvalid("localPath"|"fixedBranch", reason, msg)`。

- [ ] **Step 4: 跑通过**：同 Step 2。Expected: PASS。

- [ ] **Step 5: 写失败的 Handler 测试**

覆盖：创建 201 + DTO；422 reason；`POST /code-repos/{id}/validate` 200；普通用户 403。

- [ ] **Step 6: 跑失败**：`go test ./internal/resource/interfaces/httpapi -run TestCodeRepoHandler -v` → FAIL。

- [ ] **Step 7: 实现 Handler 与 DTO**；body 上限 64 KiB。

- [ ] **Step 8: 跑通过**：同 Step 6。Expected: PASS。

- [ ] **Step 9: 提交**

```bash
git add backend/internal/resource/application/coderepo_service.go backend/internal/resource/application/coderepo_service_test.go backend/internal/resource/interfaces/httpapi/handlers_coderepo.go backend/internal/resource/interfaces/httpapi/handlers_coderepo_test.go
git commit -m "feat: add code repository service and handlers"
```

---

### Task 15: 前端应用模型、账号与 Access 面板

**Wave:** W2 · **并发:** 可并发（与 T11–T14 后端串行链） · **依赖:** T8
**写作用域:** `frontend/src/features/resources/AppModelsPanel.tsx`, `AppModelImportDialog.tsx`, `AccountsPanel.tsx`, `AccessForm.tsx`, `ResourcesPage.tsx`, `*.test.tsx`（仅新增上述组件的测试）

**Files:**
- Create: `frontend/src/features/resources/AppModelsPanel.tsx`
- Create: `frontend/src/features/resources/AppModelImportDialog.tsx`
- Create: `frontend/src/features/resources/AccountsPanel.tsx`
- Create: `frontend/src/features/resources/AccessForm.tsx`
- Modify: `frontend/src/features/resources/ResourcesPage.tsx`（挂载两个新 Tab）
- Test: `frontend/src/features/resources/AppModelsPanel.test.tsx`
- Test: `frontend/src/features/resources/AccountsPanel.test.tsx`
- Test: `frontend/src/features/resources/AccessForm.test.tsx`

**Interfaces:**
- Consumes: T8 的 `types.ts/api.ts` 中 app model/account/access 函数。
- Produces:
```ts
AppModelsPanel({ systemId, canWrite }: { systemId:number; canWrite:boolean })
AccountsPanel({ canWrite }: { canWrite:boolean })
AccessForm({ accountId, systems, access, canWrite, onSubmit }: { accountId:number; systems:SystemDTO[]; access?:AccessDTO; canWrite:boolean; onSubmit:(input:unknown)=>Promise<void> })
```

- [ ] **Step 1: 写失败的测试**

```tsx
vi.mock("./api", () => ({ listAppModels: vi.fn().mockResolvedValue({items:[{id:1,key:"route:/orders",kind:"route",name:"Orders",path:"/orders",method:"",source:"manual"}],nextCursor:null,hasMore:false}), importAppModels: vi.fn() }))
it("imports app model JSON and reports counts", async () => {
  render(<AppModelsPanel systemId={1} canWrite />)
  fireEvent.click(screen.getByRole("button", {name:"JSON 导入"}))
  fireEvent.change(screen.getByLabelText("JSON"), {target:{value:'{"nodes":[{"key":"route:/orders","kind":"route","name":"Orders"}]}'}})
  fireEvent.click(screen.getByRole("button", {name:"导入"}))
  await waitFor(() => expect(importAppModels).toHaveBeenCalled())
})
// AccessForm.test.tsx
it("keeps credential when left blank", async () => {
  render(<AccessForm access={{...}} canWrite onSubmit={vi.fn()} />)
  fireEvent.click(screen.getByRole("button", {name:"保存"}))
  expect(onSubmit).toHaveBeenCalledWith(expect.not.objectContaining({credential: expect.anything()}))
})
```

- [ ] **Step 2: 跑失败**：`cd frontend; pnpm vitest run src/features/resources/AppModelsPanel.test.tsx src/features/resources/AccountsPanel.test.tsx src/features/resources/AccessForm.test.tsx` → FAIL。

- [ ] **Step 3: 实现 `AppModelsPanel` + 导入对话框**

表格（kind/key/name/path/method/source）；新建/编辑；JSON 文本框或文件读取，≤5 MiB；提交后显示 `created/updated`；422 显示 reason；只读隐藏按钮。

- [ ] **Step 4: 实现 `AccountsPanel` + `AccessForm`**

账号列表 + 新建/编辑；Access 表格含 authType/system/enabled/concurrencyLimit/TTL/credentialSet；表单 credential 留空=不提交字段、勾选清除=空串；不显示已有 credential。

- [ ] **Step 5: 修改 `ResourcesPage` 挂载两个 Tab**；保持只读逻辑。

- [ ] **Step 6: 跑通过**：同 Step 2。Expected: PASS。

- [ ] **Step 7: 跑前端门禁**：`pnpm lint && pnpm typecheck`。Expected: PASS。

- [ ] **Step 8: 提交**

```bash
git add frontend/src/features/resources/AppModelsPanel.tsx frontend/src/features/resources/AppModelImportDialog.tsx frontend/src/features/resources/AccountsPanel.tsx frontend/src/features/resources/AccessForm.tsx frontend/src/features/resources/ResourcesPage.tsx frontend/src/features/resources/AppModelsPanel.test.tsx frontend/src/features/resources/AccountsPanel.test.tsx frontend/src/features/resources/AccessForm.test.tsx
git commit -m "feat: add app model import and account access panels"
```
---

### Task 16: 前端会话面板、导入与接管

**Wave:** W3 · **并发:** 可并发（与 T17） · **依赖:** T15
**写作用域:** `frontend/src/features/resources/SessionsPanel.tsx`, `SessionDetail.tsx`, `ImportSessionDialog.tsx`, `TakeoverDialog.tsx`, `ResourcesPage.tsx`, 对应 `*.test.tsx`

**Files:**
- Create: `frontend/src/features/resources/SessionsPanel.tsx`
- Create: `frontend/src/features/resources/SessionDetail.tsx`
- Create: `frontend/src/features/resources/ImportSessionDialog.tsx`
- Create: `frontend/src/features/resources/TakeoverDialog.tsx`
- Modify: `frontend/src/features/resources/ResourcesPage.tsx`（挂载会话 Tab）
- Test: `frontend/src/features/resources/SessionsPanel.test.tsx`
- Test: `frontend/src/features/resources/ImportSessionDialog.test.tsx`

**Interfaces:**
- Consumes: T8 的 `SessionDTO/SnapshotDTO` 与 `listSessions/getSession/importSession/validateSession/confirmSession/invalidateSession/revokeSession/listSnapshots/requestTakeover/completeTakeover/cancelTakeover/listAccounts/listAccesses/listEnvironments`。
- Produces:
```ts
SessionsPanel({ canWrite, initialSessionId }: { canWrite:boolean; initialSessionId?:number })
```

- [ ] **Step 1: 写失败的测试**

```tsx
vi.mock("./api", () => ({
  listSessions: vi.fn().mockResolvedValue({items:[{id:1,state:"valid",accountId:1,systemId:1,environmentId:1,accessId:1,cookieCount:2,originCount:0,expiresAt:null,expiring:false,failureReason:"",latestSnapshot:{summary:{degraded:false}}}],nextCursor:null,hasMore:false}),
  validateSession: vi.fn(), confirmSession: vi.fn(), revokeSession: vi.fn(),
}))
it("shows state badge and actions for admin", async () => {
  render(<SessionsPanel canWrite />)
  expect(await screen.findByText("valid")).toBeInTheDocument()
  expect(screen.getByRole("button", {name:"校验"})).toBeInTheDocument()
})
it("hides actions for readonly", async () => { render(<SessionsPanel canWrite={false} />); expect(screen.queryByRole("button",{name:"校验"})).not.toBeInTheDocument() })
```

- [ ] **Step 2: 跑失败**：`cd frontend; pnpm vitest run src/features/resources/SessionsPanel.test.tsx src/features/resources/ImportSessionDialog.test.tsx` → FAIL。

- [ ] **Step 3: 实现 `SessionsPanel` 与详情**

列表列：账号/系统/环境/状态徽章/有效期/最后校验/操作；筛选 account/system/state；选中后 `getSession`，显示 `latestSnapshot.summary`（degraded 显示「能力未验证」）、`activeLease.holder`、`latestTakeover.status` 与失败原因；动作按钮：校验、确认、失效、撤销、发起接管；操作后重取。

- [ ] **Step 4: 实现导入与接管对话框**

导入：选择账号→Access→环境（环境从 access.systemId 调 `listEnvironments`）→文件/文本框；文件读取 ≤5 MiB；提交 `importSession`；显示 state/快照/degraded。接管：发起（reason/instructions/expiresInSeconds）→ waiting；完成（新 storageState 文件）→ 调 `completeTakeover`；取消。

- [ ] **Step 5: 修改 `ResourcesPage` 挂载会话 Tab 与深链**；保持只读逻辑。

- [ ] **Step 6: 跑通过**：同 Step 2。Expected: PASS。

- [ ] **Step 7: 前端门禁**：`pnpm lint && pnpm typecheck`。Expected: PASS。

- [ ] **Step 8: 提交**

```bash
git add frontend/src/features/resources/SessionsPanel.tsx frontend/src/features/resources/SessionDetail.tsx frontend/src/features/resources/ImportSessionDialog.tsx frontend/src/features/resources/TakeoverDialog.tsx frontend/src/features/resources/ResourcesPage.tsx frontend/src/features/resources/SessionsPanel.test.tsx frontend/src/features/resources/ImportSessionDialog.test.tsx
git commit -m "feat: add session panel import validation and takeover UI"
```

---

### Task 17: 组装根、服务器路由、OpenAPI

**Wave:** W3 · **并发:** 可并发（与 T16） · **依赖:** T11、T12、T13、T14
**写作用域:** `backend/cmd/eztest/main.go`, `backend/internal/server/server.go`, `backend/internal/resource/interfaces/httpapi/routes.go`, `backend/internal/resource/interfaces/httpapi/routes_test.go`, `docs/openapi/api.yaml`

**Files:**
- Create: `backend/internal/resource/interfaces/httpapi/routes.go`
- Test: `backend/internal/resource/interfaces/httpapi/routes_test.go`
- Modify: `backend/cmd/eztest/main.go`（实例化仓储/服务/handlers，填入 Dependencies）
- Modify: `backend/internal/server/server.go`（`Dependencies.Resources`，挂载路由）
- Modify: `docs/openapi/api.yaml`（P2 路径与 schema）

**Interfaces:**
- Consumes: T11–T14 的四个 handler 与 constructor；T3–T7 的仓储/Checker/GitValidator。
- Produces:
```go
type Handlers struct {
    Catalog  *CatalogHandler
    Access   *AccessHandler
    Session  *SessionHandler
    CodeRepo *CodeRepoHandler
}
func RegisterRoutes(mux *http.ServeMux, h Handlers, adminGuard func(http.Handler) http.Handler)

// server.Dependencies 新增
Resources *resourcehttp.Handlers
```

- [ ] **Step 1: 写失败的路由守卫测试**

```go
type fakeAuth struct{ user identity.User }
func (f fakeAuth) Authenticate(context.Context, string) (identity.User, error) { return f.user, nil }

func TestResourceWriteRoutesRequireAdmin(t *testing.T) {
    mux := http.NewServeMux()
    RegisterRoutes(mux, Handlers{Catalog: NewCatalogHandler(nil), Access: NewAccessHandler(nil), Session: NewSessionHandler(nil), CodeRepo: NewCodeRepoHandler(nil)}, identityhttp.RequireAdmin)
    h := identityhttp.AuthMiddleware(fakeAuth{user: identity.User{ID: 2, Role: identity.RoleUser, Status: identity.StatusActive}})(mux)
    req := httptest.NewRequest("POST", "/api/resources/systems", strings.NewReader(`{}`))
    req.Header.Set("Authorization", "Bearer test")
    rr := httptest.NewRecorder(); h.ServeHTTP(rr, req)
    if rr.Code != http.StatusForbidden { t.Fatalf("got %d", rr.Code) }
}
```

- [ ] **Step 2: 跑失败**：`go test ./internal/resource/interfaces/httpapi -run TestResourceWriteRoutes -v` → FAIL。

- [ ] **Step 3: 实现 `routes.go`**：非 nil handler 调其 `RegisterRoutes(mux, adminGuard)`。

- [ ] **Step 4: 跑通过**：同 Step 2。Expected: PASS。

- [ ] **Step 5: 装配 `cmd/eztest/main.go`**

构造 `postgres.NewCatalogRepository/NewAccessRepository/NewSessionRepository/NewCodeRepository`、`httpcheck.NewChecker`、`gitexec.NewValidator`；构造四个服务与四个 handler；`server.Dependencies.Resources = &resourcehttp.Handlers{...}`。

- [ ] **Step 6: 挂载 `server.go`**

`Dependencies` 增加 `Resources *resourcehttp.Handlers`；在现有路由挂载之后：
```go
if deps.Resources != nil {
    resourcehttp.RegisterRoutes(mux, *deps.Resources, identityhttp.RequireAdmin)
}
```

- [ ] **Step 7: 追加 OpenAPI**

为 `/api/resources/systems`、`/environments`、`/app-models`、`/accounts`、`/accesses`、`/sessions`、`/takeovers`、`/code-repos` 及动作路径添加 path、schema（System/Environment/AppModel/Account/Access/Session/Snapshot/Takeover/CodeRepo）与错误响应；`Session` schema 不得含 `storageState`/`credential`。

- [ ] **Step 8: 验证**

Run:
```powershell
cd backend; ..\.tools\go\bin\go.exe build ./...
cd backend; ..\.tools\go\bin\go.exe test ./internal/resource/... ./internal/server/...
```
Expected: PASS。

- [ ] **Step 9: 提交**

```bash
git add backend/cmd/eztest/main.go backend/internal/server/server.go backend/internal/resource/interfaces/httpapi/routes.go backend/internal/resource/interfaces/httpapi/routes_test.go docs/openapi/api.yaml
git commit -m "feat: wire P2 resources into server and OpenAPI"
```

---

### Task 18: Playwright E2E（资源、会话动作、代码仓）

**Wave:** W4 · **并发:** 可并发（与 T19） · **依赖:** T10、T16、T17
**写作用域:** `frontend/e2e/resources.spec.ts`, `frontend/e2e/resources-session-actions.spec.ts`, `frontend/e2e/code-repos.spec.ts`

**Files:**
- Create: `frontend/e2e/resources.spec.ts`
- Create: `frontend/e2e/resources-session-actions.spec.ts`
- Create: `frontend/e2e/code-repos.spec.ts`

**Interfaces:**
- Consumes: 运行中的 EzTest（`http://localhost:6868`）、fixture（`http://127.0.0.1:13335`）、管理员凭据（`EZTEST_E2E_ADMIN_USERNAME/PASSWORD`）、`scripts/e2e.ps1`。
- Produces: P2 E2E 覆盖；截图到 `.tmp/p2-acceptance/`。

- [ ] **Step 1: 写 `resources.spec.ts`**

流程：
1. 管理员登录 EzTest。
2. `/resources` 新建 System（key=`fixture-web-<ts>`，kind=web，baseUrl=`http://127.0.0.1:13335`）。
3. 建 Environment（key=`local`，baseUrl 同上，loginCheck `{url:"/account",expectStatus:[200],bodyContains:"Fixture Account"}`）。
4. 建 Account + Access（authType=`manual`）。
5. 新开 fixture 上下文：登录 `fixture/fixture-pass`，取 `context.storageState()`；关闭 fixture 上下文。
6. 在 EzTest 导入 storageState（`setInputFiles` buffer）→ 断言状态 `valid`、快照 allowed；截图。
7. 普通用户登录 → `/resources` 无新建按钮、显示「只读」；`page.evaluate` 用其 token POST `/api/resources/systems` → 403。

- [ ] **Step 2: 写 `resources-session-actions.spec.ts`**

流程：复用上面的资源（每个 test 独立建）；对同一 Session 执行校验→确认（先让 login_check 失败或用无 login_check 的环境进入 needs_manual）→撤销；断言状态变化与按钮可用性；发起 takeover → 完成（上传新 storageState）→ `valid`。

- [ ] **Step 3: 写 `code-repos.spec.ts`**

`test.beforeAll` 用 Node `child_process.execFileSync` 在 `.tmp/e2e-repo` 创建 git 仓库并提交；UI 新建代码仓（localPath 绝对路径、branch=`master` 或 `main`）→ 断言 `lastCommit` 40 位、显示「未索引（P4）」；编辑路径为不存在目录 → 422 reason 可见。

- [ ] **Step 4: 跑 E2E**

Run: `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1`

Expected: 现有 5 个 spec + 新增 3 个 spec 全部 PASS；fixture 自动启动/停止。

- [ ] **Step 5: 提交**

```bash
git add frontend/e2e/resources.spec.ts frontend/e2e/resources-session-actions.spec.ts frontend/e2e/code-repos.spec.ts
git commit -m "test: add P2 resources and code repository e2e coverage"
```

---

### Task 19: 验收脚本与文档

**Wave:** W4 · **并发:** 可并发（与 T18） · **依赖:** T10、T17
**写作用域:** `scripts/acceptance.ps1`, `README.md`

**Files:**
- Modify: `scripts/acceptance.ps1`（OpenAPI 关键词检查）
- Modify: `README.md`（P2 功能、fixture、验收动线）

**Interfaces:**
- Consumes: T17 的 OpenAPI 路径；T10 的 fixture 脚本。
- Produces: 验收脚本检查 P2 契约；README 可供交接。

- [ ] **Step 1: 修改 `scripts/acceptance.ps1`**

在现有 OpenAPI needle 检查处增加：
- `/api/resources/systems`
- `/api/resources/sessions`
- `/api/resources/code-repos`
- `storageState`
- `credentialSet`

保持现有 SKIPPED 语义（Compose/真实 LLM/真实 Agent LLM）不变。

- [ ] **Step 2: 修改 `README.md`**

增加 P2 段落：`/resources` 四 Tab、`/code-repos`、Session 导入/确认/撤销/接管、D28 明文说明、fixture 启动方式、P2 验收命令。

- [ ] **Step 3: 验证静态检查**

Run:
```powershell
Select-String -Path docs/openapi/api.yaml -Pattern "/api/resources/systems","storageState","credentialSet"
Select-String -Path scripts/acceptance.ps1 -Pattern "resources/systems"
```
Expected: 均命中；不运行完整 acceptance（由 T20 执行）。

- [ ] **Step 4: 提交**

```bash
git add scripts/acceptance.ps1 README.md
git commit -m "docs: document P2 resources and extend acceptance checks"
```

---

### Task 20: 全量回归、人工验收与最终评审

**Wave:** W5 · **并发:** 串行 · **依赖:** T18、T19
**写作用域:** `.superpowers/sdd/2026-10-27-eztest-p2-resources-identity/`（gitignored）；允许修复 P2 写作用域内缺陷

**Files:**
- Create: `.superpowers/sdd/2026-10-27-eztest-p2-resources-identity/progress.md`
- Create: `.superpowers/sdd/2026-10-27-eztest-p2-resources-identity/acceptance.md`

**Interfaces:**
- Consumes: 全部任务交付物。
- Produces: 验收证据、最终 commit、可合并分支。

- [ ] **Step 1: 全量门禁**

Run: `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1`

Expected: 全部 PASS；SKIPPED 仅 Compose/真实 LLM/真实 Agent LLM。

- [ ] **Step 2: 人工验收动线（Agent 执行并截图）**

启动 `dev-api`/`dev-web` 与 fixture；用 Playwright 以管理员登录 EzTest：
1. 建系统/环境/账号/Access。
2. 登录 fixture 并导出 storageState；导入 EzTest；断言 `valid`；截图。
3. 校验、确认（degraded）、失效、撤销；截图。
4. 发起接管 → 完成（新 storageState）；截图。
5. 普通用户登录 → `/resources` 只读 + POST 403；截图。
6. `/code-repos` 建仓、校验、显示 commit/未索引；截图。

截图保存 `.tmp/p2-acceptance/*.png`；步骤与结果写 `acceptance.md`。

- [ ] **Step 3: 自审与评审**

用 `requesting-code-review` 流程检查：D28 无加密/审计残留、无 credential/storageState 回显、11 表/迁移顺序、权限守卫、错误 reason、DoD 覆盖。发现缺陷在 P2 写作用域内修复并补测试。

- [ ] **Step 4: 最终回归**

修改后重跑：`scripts/test.ps1`、`scripts/e2e.ps1`、`scripts/acceptance.ps1`。

Expected: 全部 PASS。

- [ ] **Step 5: 提交并记录**

```bash
git add -A
git commit -m "chore: finalize P2 resources and identity phase"
```
写 `progress.md`：任务完成清单、提交区间、门禁结果、遗留风险。

- [ ] **Step 6: 合并**

经用户确认后，按 `finishing-a-development-branch` 流程把 `feat/p2-resources-identity` fast-forward 合回 `dev`（不 push），删除分支。

---

## Self-Review

**1. Spec coverage**

| Spec 章节 | 任务 |
|---|---|
| §2 决策/D28 | T1、T2 |
| §3 上下文/目录/请求流 | T2、T17 |
| §4 配置/命令 | Pre-flight、T17 |
| §5 数据模型/迁移 | T2、T3–T6 |
| §6.1 状态机 | T2、T13 |
| §6.2 导入结构 | T2、T13 |
| §6.3 login_check | T7、T13 |
| §6.4 接管 | T13 |
| §6.5 租约 | T13 |
| §6.6 应用模型 | T11、T15 |
| §6.7 Git 校验 | T6、T14 |
| §6.8 删除保护 | T11、T12 |
| §7 API/错误/权限 | T11–T14、T17 |
| §8 前端 | T8、T9、T15、T16 |
| §9 测试/验收/DoD | T10、T18、T19、T20 |
| §10 风险 | T20（验收证据） |
| §11 交付物 | T1–T19 |
| §12 D28 同步 | T1 |
| §13 不做 | Global Constraints + T1 文档 |
| §14 假设 | Pre-flight |

**2. Step scan**：每个 Step 均为“写测试/跑失败/实现/跑通过/提交”之一；无 TBD、无空泛的“处理边界”；代码块只保留测试与签名。

**3. Type consistency**：`domain.SessionRepository.UpdateSession(s, expectedState)`、`application.LoginChecker.Check(env, state)`、`GitValidator.Validate(path, branch)`、`httpapi.WriteServiceError`、前端 `Page<T>`/`SessionDTO` 在各任务中名称一致。

**4. Review Focus 落点**：导入边界→T2/T13；login_check 误判→T7/T13；状态并发/过期→T5/T13；应用模型导入→T11/T15；Git 校验→T6/T14。

**5. Proportion**：计划约 1.1k 行，spec 约 0.9k 行；任务以接口/测试/命令为主，未逐行转录实现。


