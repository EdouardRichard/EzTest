# EzTest 0.2 P0 平台底座 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans (native) or superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax for tracking. Parallel groups are marked; shared-file writes are Lead-only.

**Goal:** 建立 EzTest 0.2 的新平台底座：严格 4 项 env + DB 设置中心、身份（固定默认管理员 + 自助改密）、LLM 配置、事件/SSE、内容账本、审计、资源域、版本多 SUT 绑定、前端壳与 P0 页面、冻结契约与测试证据。

**Architecture:** 就地重写 Go 模块化单体（net/http ServeMux + pgx）与 Next.js 前端；每个上下文四层目录，跨上下文只依赖 application 接口；所有业务 ID 为雪花 int64、JSON 字符串；事件 `seq` 作为 SSE 游标；P0 单实例、无任务板/Agent/执行。

**Tech Stack:** Go 1.24.6、PostgreSQL 17、pgx v5、bcrypt、net/http、Next.js 16 + React 19 + Tailwind 4 + Vitest + Playwright。

**Spec:** `docs/superpowers/specs/2026-10-31-eztest-v02-p0-platform-foundation-design.md`（必须与权威设计一起阅读）

## Global Constraints

- 权威：`docs/version-V0.2/architecture-design.md`、`high-level-design.md`、`research/decision-log.md`、`research/open-decisions.md`、`phases/phase-00-platform-foundation.md`。冲突停止报告，不改设计。
- `.env` 严格 4 项：`EZTEST_DATABASE_URL`、`EZTEST_API_PORT`、`EZTEST_WEB_PORT`、`EZTEST_EXECUTOR_PORT`；不得新增第 5 个 EzTest 运行时 env。
- 业务 ID：雪花 int64，JSON/URL 一律十进制字符串；`event.seq` 是数字游标；`content_object.sha256` 是 64 hex。
- 表名/字段/索引沿用 `Agent.md` §6.9 命名规约（0.1 约定；0.2 的 D34 是跨版本知识，不套用该编号）；迁移只追加、checksum 不可变；已应用迁移禁止修改。
- 不恢复 0.1 控制面：无 8 状态机、无人工批次、无整版冻结、无评审页签。
- Go 目录/导入方向遵守 Agent.md §6.2；每上下文 `domain/application/infrastructure/interfaces/httpapi` 四层。
- 不修改 `docs/version-V0.2/**` 设计文档；只写 `contracts/`、本 spec/plan、阶段 handoff、迁移与测试证据。
- 不提交 `.env`、`.data/`、`.tmp/`；不读取 `docs/用户专用文件，禁止AI阅读.md`。
- 并行代理不得运行 `git add/commit`（Lead 串行提交）；不得运行 `go mod tidy`/`pnpm install`；不得修改 go.mod/package.json（除非任务明确）。
- 每个任务先写失败测试，验证失败原因，再写最小实现，跑相关测试与全量测试，再进入下一个任务。
- 命令：Go 用 `.tools\go\bin\go.exe`；前端 `pnpm`；测试库 `eztest_v02_test`。

## Review Focus

1. **严格 4 env 与 secret 泄漏**：任何额外 `EZTEST_*`、API key/password 出现在 GET/事件/审计/错误体，都必须被测试抓住。
2. **多 SUT 身份混用**：`sut_id`、`environment_id`、页面/API/库/账号必须属于同一 SUT+环境；跨 SUT 的 page_id 必须 400。
3. **事件游标/回放**：`after`/`Last-Event-ID` 不得重复或漏事件；快照与 SSE 同 seq；无游标只订阅实时。
4. **权限绕过**：普通用户写资源/SUT/设置/LLM/用户必须 403；公开端点仅 login/health；默认 admin 凭据只在首启种子出现。
5. **ID 精度与迁移幂等**：所有 ID 响应为字符串；重复迁移不重复执行；checksum 变化被拒绝。

---

## 并行执行图

```text
P1 顺序（Lead）: T1 → T2 → T3 → T4
P2 并行（5 agents）: T5 契约 | T6 身份+审计 | T7 事件+存储 | T8 资源 | T9 LLM | T12 前端底座
P3 并行（4 agents）: T10 版本（依赖 T8） | T13 设置 UI | T14 资源 UI | T15 版本 UI
P4（Lead + agent）: T11 服务器装配/集成（依赖 T6–T10）→ T16 E2E/验收
P5（Lead）: T17 全量验证/契约冻结/handoff → T18 演示截图
```

**写作用域**：T6 只写 `internal/identity/**` 与 `internal/audit/**`；T7 只写 `internal/eventing/**` 与 `internal/storage/**`；T8 只写 `internal/resource/**`；T9 只写 `internal/llm/**`；T10 只写 `internal/version/**`；T11 只写 `internal/server/**`、`cmd/eztest/**`、`internal/testutil/**`、scripts；T12 写前端底座与壳；T13/T14/T15 只写各自 feature+route。共享文件（go.mod/package.json/contracts/根脚本）由 Lead 统一收口。

---

## Task 1: 新模块骨架、4 项 env 与健康检查

**Files:**
- Modify: `.env.example`（替换为 4 项）
- Create: `backend/internal/config/config.go`、`backend/internal/config/config_test.go`
- Create: `backend/internal/server/health.go`、`backend/internal/server/health_test.go`
- Create: `backend/cmd/eztest/main.go`（仅 config + health；DB 在 T3 接入）
- Create: `scripts/dev-api.ps1`、`scripts/dev-web.ps1`、`scripts/test.ps1`（骨架）
- Create: `.env`（gitignored；4 项，DB 指向 eztest_v02）

**Interfaces:**
```go
package config
type Config struct { DatabaseURL string; APIPort, WebPort, ExecutorPort int }
func Load() (Config, error)
func (c Config) WebOrigins() []string   // http://localhost:{WebPort}, http://127.0.0.1:{WebPort}
func (c Config) ExecutorURL() string    // http://127.0.0.1:{ExecutorPort}
package server
type Pinger interface { Ping(context.Context) error }
type Health struct { Version string; DB Pinger; SettingsLoaded bool; ExecutorURL string }
func (h *Health) Register(mux *http.ServeMux)
```
- `Load` 只读 4 个 env；缺 DB URL → error；端口缺失用默认；非法端口/URL scheme → error。
- `/healthz` 200 `{"status":"ok"}`；`/api/health` 返回 `data{status,version,db,settings_loaded,executor_url}`，DB nil/失败 503。

- [ ] **Step 1: 写 config 失败测试**：`TestLoadRequiresDatabaseURL`、`TestLoadDefaults`、`TestLoadRejectsPort`、`TestWebOrigins`（断言 4 env 之外变量不影响结果）。
- [ ] **Step 2: 运行失败**：`.tools\go\bin\go.exe test ./internal/config/...`，Expected FAIL（Load 未定义/字段缺失）。
- [ ] **Step 3: 实现 config**；`os.LookupEnv` + `strconv.Atoi` + URL parse；不读取其他 env。
- [ ] **Step 4: 写 health 失败测试**：fake Pinger 成功/失败、`/api/health` JSON 形状与 503。
- [ ] **Step 5: 实现 health 与最小 main**（`http.Server` 监听 `:{APIPort}`，SIGINT 优雅关闭）。
- [ ] **Step 6: 跑通**：`go test ./internal/config/... ./internal/server/...` 与 `go build ./...`。
- [ ] **Step 7: 提交**：`git add .env.example backend/internal/config backend/internal/server backend/cmd/eztest scripts && git commit -m "feat(p0): add backend skeleton with four-env config and health"`。

## Task 2: 平台原语（错误信封、请求 ID、分页、校验、时钟、雪花）

**Files:**
- Create: `backend/internal/platform/errors.go`、`errors_test.go`
- Create: `backend/internal/platform/httpx/respond.go`、`respond_test.go`
- Create: `backend/internal/platform/requestid/requestid.go`、`requestid_test.go`
- Create: `backend/internal/platform/pagination/pagination.go`、`pagination_test.go`
- Create: `backend/internal/platform/validation/validation.go`、`validation_test.go`
- Create: `backend/internal/platform/clock/clock.go`
- Create: `backend/internal/platform/id/snowflake.go`、`backend/internal/platform/id/id.go`、`snowflake_test.go`

**Interfaces:**
```go
package platform
type Error struct { Status int; Code, Message string; Retryable bool; Details map[string]any }
func (e *Error) Error() string
func NewError(status int, code, message string) *Error
func Invalid(field, message string) *Error
func Unauthenticated() *Error
func Forbidden() *Error
func NotFound(kind string) *Error
func Conflict(message string) *Error
func AsError(err error) *Error
package httpx
func JSON(w http.ResponseWriter, status int, data any)
func Error(w http.ResponseWriter, r *http.Request, err error)
func RequestID(ctx context.Context) string
package requestid
func New() string // req_ + 12 hex/URL-safe
func With(ctx context.Context, id string) context.Context
package pagination
func Limit(raw string, def, max int) int
func Cursor(raw string) (int64, error) // "" => 0
func NextCursor(hasMore bool, lastID int64) string
package validation
func String(field, v string, min, max int) error
func URL(field, v string) error
func HTTPMethod(v string) error
func StringList(field string, v []string, max int) error
func NonEmptyID(field, v string) (int64, error)
package clock
type Clock interface { Now() time.Time }
type System struct{}
func (System) Now() time.Time
package id
type Generator interface { Next() int64 }
func NewSnowflake(node int64) (*Snowflake, error) // node 0..1023, epoch 2024-01-01 UTC
func Format(v int64) string
func Parse(s string) (int64, error)
```
- 错误信封固定 `{"error":{"code","message","request_id","retryable","details"}}`；`httpx.Error` 从 context 取 request id。
- 雪花：41/10/12，mutex，同一毫秒 seq 溢出等待下一毫秒；时钟回拨返回错误（Next 可等待到上次+1）。

- [ ] **Step 1: 写 errors/httpx 失败测试**：状态映射、request_id 回显、details 合并。
- [ ] **Step 2: 实现并跑通**。
- [ ] **Step 3: 写 snowflake 失败测试**：10k 唯一、单调、node 越界 error、Format/Parse 往返、JSON 字符串断言。
- [ ] **Step 4: 实现并跑通**。
- [ ] **Step 5: 写 pagination/validation/requestid 失败测试**（边界：limit 0/负数/超大、cursor 非数字、URL 非 http、空字符串）。
- [ ] **Step 6: 实现并跑通**：`go test ./internal/platform/...`。
- [ ] **Step 7: 提交**：`feat(p0): add platform primitives`。

## Task 3: 迁移执行器、P0 全量表结构与测试库

**Files:**
- Create: `backend/internal/db/db.go`、`backend/internal/db/migrate.go`、`backend/internal/db/migrate_integration_test.go`
- Create: `backend/internal/db/migrations/order.txt`
- Create: `backend/internal/db/migrations/settings_0001_settings.sql`
- Create: `backend/internal/db/migrations/identity_0001_identity.sql`
- Create: `backend/internal/db/migrations/llm_0001_llm.sql`
- Create: `backend/internal/db/migrations/eventing_0001_events.sql`
- Create: `backend/internal/db/migrations/storage_0001_content_ledger.sql`
- Create: `backend/internal/db/migrations/audit_0001_audit_log.sql`
- Create: `backend/internal/db/migrations/resource_0001_catalog.sql`
- Create: `backend/internal/db/migrations/version_0001_versions.sql`
- Create: `backend/internal/testutil/db.go`、`backend/internal/testutil/db_integration_test.go`

**Interfaces:**
```go
package db
func Open(ctx context.Context, url string) (*pgxpool.Pool, error)
func Migrate(ctx context.Context, pool *pgxpool.Pool) ([]string, error) // 返回本次应用的版本名
func WithTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error
package testutil
func NewDB(t testing.TB) *pgxpool.Pool // 建随机 schema、search_path、跑迁移、cleanup drop
func Truncate(t testing.TB, pool *pgxpool.Pool, tables ...string)
```
- `schema_migrations(version TEXT PK, checksum TEXT NOT NULL, applied_at TIMESTAMPTZ)`；advisory lock `0x455A5430`（"EZT0"）；每个迁移独立事务；已应用 checksum 不一致 → error；`order.txt` 必须覆盖所有 `.sql` 且不重复。
- 表结构按 spec §6.2/§6.3 全量创建；所有业务 PK `BIGINT` 无自增；索引命名沿用 Agent.md §6.9；事件用 `event_channel(channel PK,last_seq)` + `event(channel,seq)` 复合主键（Publish 事务内分配 seq）；`app_user` 避免保留字。
- 集成测试用 `EZTEST_TEST_DATABASE_URL`；未设置则 `t.Skip`。

- [ ] **Step 1: 写迁移集成测试**（跳过后设 env 可跑）：`TestMigrateAppliesAllTables`、`TestMigrateIsIdempotent`、`TestMigrateRejectsChecksumChange`、`TestOrderCoversEverySQLFile`。
- [ ] **Step 2: 运行失败**：`go test -tags=integration ./internal/db/...`。
- [ ] **Step 3: 实现 db/migrate 与 8 个迁移**；校验表存在（information_schema）。
- [ ] **Step 4: 实现 testutil.NewDB**（`CREATE SCHEMA test_<nanoid>`，`SET search_path` 通过 pgx RuntimeParams，cleanup `DROP SCHEMA ... CASCADE`）。
- [ ] **Step 5: 跑通**：`go test -tags=integration ./internal/db/... ./internal/testutil/...`（设置 test DB URL）。
- [ ] **Step 6: 创建本地库**：`eztest_v02`、`eztest_v02_test`（psql；owner eztest）。
- [ ] **Step 7: 提交**：`feat(p0): add migration runner and P0 schema`。

## Task 4: 设置中心（catalog、seed、热加载、revision、HTTP）

**Files:**
- Create: `backend/internal/settings/domain/setting.go`、`repository.go`
- Create: `backend/internal/settings/application/catalog.go`、`service.go`、`ports.go`、tests
- Create: `backend/internal/settings/infrastructure/postgres.go`、`postgres_integration_test.go`
- Create: `backend/internal/settings/interfaces/httpapi/handler.go`、`dto.go`、`handler_test.go`

**Interfaces:**
```go
package settings // domain
type Type string; const TypeString/TypeNumber/TypeBool/TypeJSON
type Setting struct { Key string; Value json.RawMessage; Category string; Type Type; Secret, RestartRequired bool; Description string; Version int; UpdatedBy *int64; UpdatedAt time.Time }
type Revision struct { ID int64; Key string; OldValue, NewValue json.RawMessage; ActorID *int64; CreatedAt time.Time }
type Definition struct { Key, Category string; Type Type; Default json.RawMessage; Secret, RestartRequired bool; Description string; Min, Max *float64 }
type Repository interface { List(context.Context) ([]Setting,error); Get(context.Context,string) (Setting,error); Seed(context.Context, []Definition) error; Update(context.Context,string,json.RawMessage,int64,time.Time) (Setting,error); Revisions(context.Context,string,int,*int64) ([]Revision,error) }
package settingsapp
func Definitions(executorURL string) []Definition
func SeedDefaults(ctx context.Context, repo settings.Repository, defs []Definition) error
type Service struct { ... }
func New(repo settings.Repository, ids id.Generator, clock clock.Clock, audit auditapp.Recorder, events eventingapp.Publisher) *Service
func (s *Service) List(ctx) ([]settings.Setting,error)
func (s *Service) Get(ctx, key string) (settings.Setting,error)
func (s *Service) Update(ctx, key string, value any, actorID int64) (settings.Setting,error)
func (s *Service) Revisions(ctx, key string, limit int, beforeID *int64) ([]settings.Revision,error)
func (s *Service) String(ctx,key) (string,error); Int(ctx,key) (int64,error); Bool(ctx,key) (bool,error)
```
- catalog 键/默认/区间严格按 spec §5.2；`execution.executor_url` 默认用 `Definitions(executorURL)` 注入。
- 更新：类型/区间校验 → revision → setting → 缓存 reload → `settings.updated` 事件（channel=system）→ 审计；secret 值 GET/revision 置 null；PATCH null 不修改、`""` 清空。
- HTTP：`GET /api/admin/settings`、`GET/PATCH /api/admin/settings/{key}`、`GET /api/admin/settings/revisions`；DTO `{key,category,type,value,is_secret,restart_required,description,version,updated_at}`。

- [ ] **Step 1: 写 catalog/校验失败测试**：每个键唯一、默认类型匹配、区间边界、未知 key 404、secret 掩码。
- [ ] **Step 2: 写 service 失败测试**：seed 幂等、update revision、缓存立即读新值、事件/审计调用、null/空串语义。
- [ ] **Step 3: 实现 domain/application**，用 fake repo/audit/publisher 跑绿。
- [ ] **Step 4: 写 postgres 集成测试**：seed 两次不重复、并发 update version 递增、revision 顺序。
- [ ] **Step 5: 实现 postgres repo 并跑绿**。
- [ ] **Step 6: 写 HTTP handler 测试**（fake service）：信封、secret 掩码、400/404。
- [ ] **Step 7: 实现 handler/dto 并跑绿**；`go test ./internal/settings/...`。
- [ ] **Step 8: 提交**：`feat(p0): add settings center`。

---

## Task 5: contracts 骨架 + fake LLM（并行，只写 docs/contracts 与 scripts）

**Files:**
- Create: `docs/version-V0.2/contracts/openapi.yaml`
- Create: `docs/version-V0.2/contracts/events.md`
- Create: `docs/version-V0.2/contracts/db-schema.md`
- Create: `docs/version-V0.2/contracts/migrations-order.md`
- Create: `docs/version-V0.2/contracts/settings.md`
- Create: `scripts/fake-llm.cjs`
- Create: `scripts/fake-llm-smoke.ps1`

**Interfaces:** 文件契约必须与 spec §7/§8/§9 完全一致；OpenAPI 3.0.3 覆盖 P0 全部端点、schema、错误信封；events 定义 envelope/channel/游标/SSE；db-schema 覆盖 8 个迁移的表/列/索引；settings 覆盖 4 env 与键目录。
- fake LLM：`GET /health`、`GET /v1/models`、`POST /v1/chat/completions`（非流式与 `stream=true` SSE），确定性响应；端口由 `FAKE_LLM_PORT`（脚本内部变量，不是 EzTest env）控制，默认 13334。
- `scripts/fake-llm-smoke.ps1` 启动 fake、断言 `/v1/models` 与 chat 响应、停止。

- [ ] **Step 1: 写 `events.md`/`db-schema.md`/`migrations-order.md`/`settings.md`**（从 spec + 迁移文件抄写；不得发明端点/字段）。
- [ ] **Step 2: 写 `openapi.yaml`**（路径/方法/权限/请求/响应/错误码；ID 为 string）。
- [ ] **Step 3: 用 Node 解析 OpenAPI YAML 验证语法**：`node -e "const yaml=require('./frontend/node_modules/yaml'); ..."` 或在 frontend 跑 `pnpm exec vitest` 不适用；可用 `node --experimental-default-type=module` + `yaml` 包路径。若不可用，写最小 `scripts/check-openapi.cjs` 用 frontend/node_modules/yaml。
- [ ] **Step 4: 写 fake LLM + smoke**，跑通 smoke。
- [ ] **Step 5: 提交**：`docs(p0): freeze P0 contracts and fake LLM`。

## Task 6: 身份 + 审计后端（并行）

**Files:**
- Create: `backend/internal/identity/domain/user.go`、`token.go`、`repository.go`
- Create: `backend/internal/identity/application/service.go`、`ports.go`、`service_test.go`
- Create: `backend/internal/identity/infrastructure/postgres.go`、`bcrypt.go`、`postgres_integration_test.go`
- Create: `backend/internal/identity/interfaces/httpapi/handler.go`、`dto.go`、`handler_test.go`
- Create: `backend/internal/audit/domain/entry.go`、`repository.go`
- Create: `backend/internal/audit/application/service.go`、`service_test.go`
- Create: `backend/internal/audit/infrastructure/postgres.go`、`postgres_integration_test.go`
- Create: `backend/internal/audit/interfaces/httpapi/handler.go`、`dto.go`、`handler_test.go`

**Interfaces:**
```go
package auditapp
type Event struct { ActorID *int64; Action, TargetKind, TargetID, RequestID, IP, UserAgent string; Metadata map[string]any }
type Recorder interface { Record(context.Context, Event) error }
type Filter struct { Action string; ActorID *int64 }
func (s *Service) Record(ctx context.Context, e Event) error
func (s *Service) List(ctx context.Context, f Filter, limit int, beforeID *int64) ([]audit.Entry, bool, error)
package identityapp
type Session struct { Token string; ExpiresAt time.Time; User identity.User }
type Service struct{...}
func New(repo identity.Repository, hasher PasswordHasher, ids id.Generator, clock clock.Clock, audit auditapp.Recorder) *Service
func (s *Service) EnsureSeedAdmin(ctx context.Context) (created bool, err error) // 空 user 表时创建 admin/admin123
func (s *Service) Login(ctx context.Context, username, password, requestID, ip, ua string) (Session, error)
func (s *Service) Logout(ctx context.Context, token string) error
func (s *Service) Authenticate(ctx context.Context, token string) (identity.User, error)
func (s *Service) ChangePassword(ctx context.Context, userID int64, oldPassword, newPassword string) error
func (s *Service) CreateUser(ctx context.Context, actorID int64, username, displayName, password string) (identity.User, error)
func (s *Service) ListUsers(ctx context.Context, limit int, beforeID *int64) ([]identity.User, bool, error)
func (s *Service) UpdateUser(ctx context.Context, actorID, userID int64, displayName, status string) (identity.User, error)
func (s *Service) ResetPassword(ctx context.Context, actorID, userID int64, password string) error
func WithActor(ctx context.Context, u identity.User) context.Context
func ActorFrom(ctx context.Context) (identity.User, bool)
```
- Token TTL **7 天固定**；SHA-256；bcrypt cost **12** + SHA-256 预哈希；密码 **8–128**；单 admin（admin/admin123 种子，幂等）；禁用撤销 token；审计 login/login_failed/logout/user.created/user.updated/user.password_reset/auth.password_changed。
- HTTP：login、logout、me、`POST /api/auth/password`（自助改密）、admin users list/create/patch/reset-password、admin audit-logs。DTO 不含 password_hash/token_hash。
- 测试：种子只建一次且不覆盖已有用户、错误密码 401、过期 token 401、禁用 401、改密旧密码失败/新密码成功、普通用户 admin API 由 server 测（本任务测 service 权限方法）、token 不明文入库、审计字段、列表分页。

- [ ] **Step 1: 写 domain/service 失败测试**（种子幂等/login/token/密码边界/改密/单 admin/禁用/审计调用）。
- [ ] **Step 2: 实现 domain/application/ports**，fake repo/hasher/clock/audit 跑绿。
- [ ] **Step 3: 写 postgres 集成测试**（user/token CRUD、唯一约束、级联撤销）。
- [ ] **Step 4: 实现 postgres + bcrypt 并跑绿**。
- [ ] **Step 5: 写 handler 测试**（JSON 形状、密码不泄露、401/403/404、分页游标）。
- [ ] **Step 6: 实现 handler/dto 并跑绿**；`go test ./internal/identity/... ./internal/audit/...`。
- [ ] **Step 7: 提交**（Lead 串行）：`feat(p0): add identity and audit contexts`。

## Task 7: 事件/SSE + 内容账本（并行）

**Files:**
- Create: `backend/internal/eventing/domain/event.go`、`repository.go`
- Create: `backend/internal/eventing/application/service.go`、`hub.go`、`service_test.go`
- Create: `backend/internal/eventing/infrastructure/postgres.go`、`postgres_integration_test.go`
- Create: `backend/internal/eventing/interfaces/httpapi/handler.go`、`dto.go`、`handler_test.go`
- Create: `backend/internal/storage/domain/object.go`、`repository.go`
- Create: `backend/internal/storage/application/service.go`、`ports.go`、`service_test.go`
- Create: `backend/internal/storage/infrastructure/postgres.go`、`filestore.go`、`postgres_integration_test.go`、`filestore_test.go`
- Create: `backend/internal/storage/interfaces/httpapi/handler.go`、`dto.go`、`handler_test.go`

**Interfaces:**
```go
package eventingapp
type Event struct { Channel, Type string; Scope, Payload map[string]any; ActorID *int64; RequestID string }
type Publisher interface { Publish(context.Context, Event) error }
type Snapshot struct { Data []eventing.Event; Cursor int64; HasMore bool }
func (s *Service) Publish(ctx context.Context, e Event) error
func (s *Service) Snapshot(ctx context.Context, channel string, after int64, limit int) (Snapshot, error)
func (s *Service) Subscribe(channel string) (<-chan eventing.Event, func())
package storageapp
type Object struct { SHA256 string; Size int64; MIME string; RelativePath string; CreatedAt time.Time }
type Ref struct { ID int64; ObjectID, OwnerKind string; OwnerID int64; Role string; CreatedAt time.Time }
type SettingsReader interface { String(context.Context, string) (string, error) }
type FileStore interface { Put(context.Context, root, sha string, data []byte) (string, error); Open(context.Context, root, rel string) (io.ReadCloser, error) }
func (s *Service) Upload(ctx context.Context, filename, mime string, r io.Reader, maxBytes int64, actorID int64) (Object, error)
func (s *Service) Open(ctx context.Context, sha string) (Object, io.ReadCloser, error)
func (s *Service) AddRef(ctx context.Context, objectID, ownerKind string, ownerID int64, role string) (Ref, error)
func (s *Service) ListRefs(ctx context.Context, ownerKind string, ownerID int64) ([]Ref, error)
func (s *Service) SoftDeleteRef(ctx context.Context, refID int64) error
```
- 事件：`event_channel.last_seq` 事务内分配频道内 seq；Publish 提交后广播；Snapshot 规则按 spec §8.2；SSE 先订阅再查库、按 `(channel,seq)` 去重、20s 心跳、`stream.reset`、401 JSON、只接受 `system|version:{id}|user:{id}`。
- 存储：10MiB 上限；sha256；临时文件+rename；0600/0700；GET 校验 sha；content_ref 软删除；根来自 `paths.data_root`。
- 测试：after/游标/去重/SSE 帧、channel 校验、上传去重/超大/路径、下载 sha 不符 500、ref 生命周期。

- [ ] **Step 1: 写 eventing service/hub 失败测试**（快照 after/limit、最新、无游标无回放、订阅/取消、seq 去重）。
- [ ] **Step 2: 实现 eventing app/domain，fake repo 跑绿**。
- [ ] **Step 3: 写 eventing postgres 集成测试 + handler 测试**；实现并跑绿。
- [ ] **Step 4: 写 storage app 失败测试**（sha、去重、超大、根路径、ref、sha 校验）。
- [ ] **Step 5: 实现 storage app/domain/filestore/postgres**；跑绿。
- [ ] **Step 6: 写 storage handler 测试**（multipart/下载头/401/404/413）；实现并跑绿。
- [ ] **Step 7: 提交**：`feat(p0): add eventing SSE and content ledger`。

## Task 8: 资源域（并行）

**Files:**
- Create: `backend/internal/resource/domain/{sut.go,environment.go,page.go,api.go,database.go,account.go,repository.go}`
- Create: `backend/internal/resource/application/{service.go,views.go,ports.go,service_test.go}`
- Create: `backend/internal/resource/infrastructure/postgres.go`、`postgres_integration_test.go`
- Create: `backend/internal/resource/interfaces/httpapi/{handler.go,dto.go,handler_test.go}`

**Interfaces:**
```go
package resourceapp
type SUTView struct { ID, Name, Intro string; Environments []EnvironmentView; CreatedAt, UpdatedAt time.Time }
type EnvironmentView struct { ID, SUTID, Name, BaseURL string; IsDefault bool; Status string; Pages []PageView; APIs []APIView; Databases []DatabaseView; Accounts []AccountView; CreatedAt, UpdatedAt time.Time }
type PageView struct { ID, SUTID, EnvironmentID, Name, URL, RouteTemplate, PageType, AuthRole, Module, Status, Source string; CreatedAt, UpdatedAt time.Time }
type APIView struct { ID, SUTID, EnvironmentID, Method, PathTemplate, Name, AuthRole, Source, Status, SchemaRef string; CreatedAt, UpdatedAt time.Time }
type DatabaseView struct { ID, SUTID, EnvironmentID, Name, DSN, Username string; PasswordSet, ReadOnly bool; Status string; CreatedAt, UpdatedAt time.Time }
type AccountView struct { ID, SUTID, EnvironmentID, Username string; Roles []string; Notes, Status string; PasswordSet bool; CreatedAt, UpdatedAt time.Time }
type CodeRepositoryView struct { ID, Name, Path, Branch string; Include, Exclude []string; CreatedBy int64; Status string; CreatedAt, UpdatedAt time.Time }
type CatalogReader interface { GetSUT(context.Context,int64) (SUTView,error); GetEnvironment(context.Context,int64) (EnvironmentView,error); GetPage(context.Context,int64) (PageView,error); GetAPI(context.Context,int64) (APIView,error); GetDatabase(context.Context,int64) (DatabaseView,error); GetAccount(context.Context,int64) (AccountView,error); GetRepository(context.Context,int64) (CodeRepositoryView,error) }
type UsageChecker interface { SUTInUse(context.Context,int64)(bool,error); EnvironmentInUse(context.Context,int64)(bool,error); PageInUse(context.Context,int64)(bool,error); APIInUse(context.Context,int64)(bool,error); DatabaseInUse(context.Context,int64)(bool,error); AccountInUse(context.Context,int64)(bool,error); RepositoryInUse(context.Context,int64)(bool,error) }
```
- Service 方法 `CreateSUT/UpdateSUT/DeleteSUT/ListSUTs/GetSUT`、环境/页面/API/库/账号/仓库 CRUD、`Catalog(ctx)`、`ImportPages/ImportAPIs`；删除前调用 `UsageChecker`，被引用 `platform.Conflict`。
- 密码入参可选；响应只 `password_set`；列表分页按 id desc；SUT 详情聚合 env/pages/apis/dbs/accounts。
- HTTP 路径按 spec §7.2；handler 只做 DTO/参数解析/错误映射；权限由 server 中间件。

- [ ] **Step 1: 写 domain 校验失败测试**（名称/URL/method/route_template/枚举/密码）。
- [ ] **Step 2: 写 service 失败测试**（SUT+env 默认唯一、页面/API 去重、导入 skip、删除守卫、密码掩码）。
- [ ] **Step 3: 实现 domain/application，fake repo/usage 跑绿**。
- [ ] **Step 4: 写 postgres 集成测试**（唯一约束、级联、聚合查询、分页）。
- [ ] **Step 5: 实现 postgres 并跑绿**。
- [ ] **Step 6: 写 handler 测试**（CRUD 形状、导入、409、400）；实现并跑绿。
- [ ] **Step 7: 提交**：`feat(p0): add resource catalog`。

## Task 9: LLM 配置与模型目录（并行）

**Files:**
- Create: `backend/internal/llm/domain/{config.go,model.go,repository.go}`
- Create: `backend/internal/llm/application/{service.go,ports.go,service_test.go}`
- Create: `backend/internal/llm/infrastructure/{postgres.go,openai.go,postgres_integration_test.go,openai_test.go}`
- Create: `backend/internal/llm/interfaces/httpapi/{handler.go,dto.go,handler_test.go}`

**Interfaces:**
```go
package llmapp
type RemoteModel struct { ID string; ContextWindow, MaxOutput int; SupportsTools, SupportsVision bool }
type TestResult struct { OK bool; LatencyMS int64; Models []RemoteModel; Error string }
type Client interface { ListModels(context.Context, llm.Config) ([]RemoteModel, error); Test(context.Context, llm.Config) (TestResult, error) }
func (s *Service) ListConfigs(ctx) ([]llm.Config,error)
func (s *Service) GetConfig(ctx, id int64) (llm.Config,error)
func (s *Service) CreateConfig(ctx, cmd ConfigInput, actorID int64) (llm.Config,error)
func (s *Service) UpdateConfig(ctx, id int64, cmd ConfigInput, actorID int64) (llm.Config,error)
func (s *Service) DeleteConfig(ctx, id int64, actorID int64) error
func (s *Service) TestConfig(ctx, id int64) (TestResult,error)
func (s *Service) RefreshModels(ctx, id int64, actorID int64) ([]llm.Model,error)
func (s *Service) AddModel(ctx, configID int64, model string, contextWindow, maxOutput int, tools, vision bool, actorID int64) (llm.Model,error)
func (s *Service) DeleteModel(ctx, modelID int64, actorID int64) error
func (s *Service) ListModels(ctx) ([]llm.Model,error)
```
- `llm_config`: name/provider/protocol/base_url/api_key/headers/timeout/retries/is_default/status；api_key 明文入库、GET 掩码（`api_key_set:true`）；默认配置唯一。
- OpenAI client：`GET {base}/models`；测试 = models 成功或 chat 小请求成功；10s 超时；错误含状态码但不泄露 key。
- 测试：创建/更新/删除、默认切换、模型 upsert、refreshed models、测试成功/失败、key 不泄漏、事件/审计。

- [ ] **Step 1: 写 domain/service 失败测试**（必填、URL、默认唯一、模型去重、key 掩码、事件/审计）。
- [ ] **Step 2: 实现 domain/application，fake repo/client 跑绿**。
- [ ] **Step 3: 写 postgres 集成测试**（默认唯一事务、模型 upsert、级联删除）。
- [ ] **Step 4: 实现 postgres 与 openai client**；用 httptest fake 跑绿。
- [ ] **Step 5: 写 handler 测试**（CRUD、test、refresh、掩码）；实现并跑绿。
- [ ] **Step 6: 提交**：`feat(p0): add llm configuration and model catalog`。

## Task 10: 版本与多 SUT 绑定（并行，依赖 T8）

**Files:**
- Create: `backend/internal/version/domain/{version.go,binding.go,repository.go}`
- Create: `backend/internal/version/application/{service.go,views.go,service_test.go}`
- Create: `backend/internal/version/infrastructure/postgres.go`、`postgres_integration_test.go`
- Create: `backend/internal/version/interfaces/httpapi/{handler.go,dto.go,handler_test.go}`

**Interfaces:**
```go
package versionapp
type CreateCommand struct { Title string; Bindings []BindingInput }
type BindingInput struct { SUTID, EnvironmentID int64; CodeRepositoryID *int64; PageIDs, APIIDs, DatabaseIDs []int64; Accounts []AccountInput }
type AccountInput struct { AccountID int64; Role string }
type VersionView struct { ID int64; Title, Status string; AutoDesignNewRequirements bool; Bindings []BindingView; CreatedBy int64; CreatedAt, UpdatedAt time.Time }
type BindingView struct { ID int64; SUT resourceapp.SUTView; Environment resourceapp.EnvironmentView; Repository *resourceapp.CodeRepositoryView; Pages []resourceapp.PageView; APIs []resourceapp.APIView; Databases []resourceapp.DatabaseView; Accounts []resourceapp.AccountView }
func (s *Service) Create(ctx context.Context, cmd CreateCommand, actorID int64) (VersionView, error)
func (s *Service) Get(ctx context.Context, id int64) (VersionView, error)
func (s *Service) List(ctx context.Context, limit int, beforeID *int64) ([]VersionSummary, bool, error)
func (s *Service) Update(ctx context.Context, id int64, patch Patch, actorID int64) (VersionView, error)
// implements resourceapp.UsageChecker
func (s *Service) SUTInUse(ctx, id int64) (bool,error) ... (七项)
```
- 校验按 spec §7.3；错误 details.field；创建事务：version + bindings + resources；事件 `version.created` 到 `version:{id}` 与 `system`；审计。
- 详情用 resourceapp.CatalogReader 聚合；P0 status 只写 initial。
- 测试：成功 2 SUT、跨 SUT page 400、重复 sut 400、无效 repo 400、账号/库归属、usage checker true/false、列表分页、事件/审计。

- [ ] **Step 1: 写 domain/service 失败测试**（校验矩阵、聚合、usage、事件/审计）。
- [ ] **Step 2: 实现 domain/application，fake repo/catalog 跑绿**。
- [ ] **Step 3: 写 postgres 集成测试**（事务、唯一约束、scope JSON、资源行、usage 查询）。
- [ ] **Step 4: 实现 postgres 并跑绿**。
- [ ] **Step 5: 写 handler 测试**（创建/详情/列表/400 details）；实现并跑绿。
- [ ] **Step 6: 提交**：`feat(p0): add versions with multi-SUT bindings`。

## Task 11: 服务器装配、认证/CORS/健康、main 与集成动线（Lead）

**Files:**
- Create: `backend/internal/server/{server.go,middleware.go,cors.go,routes.go,health.go}`
- Create: `backend/internal/server/server_integration_test.go`、`middleware_test.go`、`cors_test.go`
- Modify: `backend/cmd/eztest/main.go`（完整装配）
- Modify: `scripts/dev-api.ps1`、`scripts/test.ps1`
- Create: `scripts/p0-acceptance.ps1`（API 动线）

**Interfaces:**
```go
package server
type Deps struct { Config config.Config; Pool *pgxpool.Pool; Settings *settingsapp.Service; Identity *identityapp.Service; Audit *auditapp.Service; Events *eventingapp.Service; Storage *storageapp.Service; LLM *llmapp.Service; Resources *resourceapp.Service; Versions *versionapp.Service; Usage resourceapp.UsageChecker }
func New(deps Deps) http.Handler
```
- 中间件顺序：request id → recover → access log → CORS → mux。CORS 允许 4 项派生 origins、`Authorization, Content-Type, X-Request-ID, Last-Event-ID`，暴露 `X-Request-ID, X-Snapshot-Cursor`。
- 路由按 spec §7.2；公开仅 login/health；admin 组套 admin 中间件；资源写按矩阵。
- 装配顺序：migrate → seed defaults → 读 node → snowflake → services → `Identity.EnsureSeedAdmin` → router。
- 健康 `/api/health` 检查 DB、settings loaded、executor_url。
- 集成测试：真实 test DB 下完整动线 seed admin → login/admin123 → settings → resource → page/api → version → events → content → audit → change-password；普通用户 403；CORS/请求 ID；重复迁移。
- `p0-acceptance.ps1`：启动 fake LLM + API（独立端口/DB），用 Invoke-RestMethod 跑动线，写 `.tmp/p0-acceptance/acceptance.log`。

- [ ] **Step 1: 写 middleware/CORS/请求 ID 失败测试**。
- [ ] **Step 2: 实现并跑绿**。
- [ ] **Step 3: 写路由集成测试**（上述动线；使用 testutil.NewDB）。
- [ ] **Step 4: 实现 router + main 装配，跑绿**。
- [ ] **Step 5: 跑 `go test ./...` 与 `go test -tags=integration ./...`**。
- [ ] **Step 6: 写 `p0-acceptance.ps1` 并跑通**。
- [ ] **Step 7: 提交**：`feat(p0): wire API server and acceptance flow`。

## Task 12: 前端底座、登录、全局壳（并行）

**Files:**
- Modify: `frontend/package.json`（dev/build/start 脚本用 `next dev` 等，保留工具链）
- Create: `frontend/src/lib/{api-client.ts,sse-client.ts,auth-storage.ts,format.ts}` + tests
- Create: `frontend/src/features/auth/{AuthProvider.tsx,LoginPage.tsx}` + tests
- Create: `frontend/src/features/shell/{AppShell.tsx,Sidebar.tsx,Topbar.tsx}` + tests
- Create: `frontend/src/navigation/nav-items.ts` + test
- Create: `frontend/src/components/ui/{Button,Input,Select,Textarea,Field,Dialog,Tabs,Badge,Table,EmptyState,ErrorState,Spinner,Alert}.tsx`
- Create: `frontend/src/app/{layout.tsx,login/page.tsx,(app)/layout.tsx,(app)/page.tsx}`
- Create: `frontend/src/app/(app)/{issues,reports,knowledge,quality,agents}/page.tsx`（占位）
- Modify: `frontend/src/styles/globals.css`（保留 tokens，补 shell/卡片样式）

**Interfaces:**
```ts
// api-client
export class ApiError extends Error { status:number; code:string; requestId:string; retryable:boolean; details:Record<string,unknown> }
export async function apiFetch<T>(path:string, init?:RequestInit & {idempotencyKey?:string}):Promise<T>
export function resolveApiUrl(path:string):string
// sse-client
export type SSEStatus = "connecting"|"live"|"reconnecting"|"closed"
export function connectSSE(opts:{url:string;token:string;lastEventId?:number;onEvent:(e:{id?:string;event:string;data:string})=>void;onStatus:(s:SSEStatus)=>void;onReset?:(d:unknown)=>void}):()=>void
// auth
export function AuthProvider({children}:{children:ReactNode}):JSX.Element
export function useAuth(): {user:User|null;token:string|null;login(u,p):Promise<void>;changePassword(oldP,newP):Promise<void>;logout():Promise<void>;loading:boolean}
```
- 登录页：固定默认凭据提示（admin/admin123）+ 登录表单；成功后 `router.push("/versions")`；不提供 bootstrap/初始化管理员表单。
- 壳：8 项导航（版本/问题/报告/知识/资源 + 质量/Agent 中心/设置）；顶栏 SSE 状态点（system 频道）+ 用户菜单（含改密/登出）。
- api-client 统一错误信封/`X-Request-ID`/Bearer；SSE fetch 流 + Last-Event-ID + 指数退避 + 45s 无字节超时。
- 测试：api-client 错误映射、SSE 帧解析/去重、默认凭据提示/登录表单、nav active 状态。

- [ ] **Step 1: 写 api-client/sse-client/auth-storage 失败测试**（错误信封、401、帧解析、去重、重连状态）。
- [ ] **Step 2: 实现 lib 并跑绿**：`pnpm vitest run src/lib`。
- [ ] **Step 3: 写 nav/shell/login 失败测试**（8 项、active、默认凭据提示）。
- [ ] **Step 4: 实现 ui 原语/auth/shell/routes 并跑绿**；`pnpm typecheck`、`pnpm lint`。
- [ ] **Step 5: 提交**：`feat(p0): add web shell and login`。

## Task 13: 设置中心 UI（并行，依赖 T12）

**Files:**
- Create: `frontend/src/features/settings/{SettingsPage.tsx,SystemSettings.tsx,LLMSettings.tsx,UsersPanel.tsx,AuditPanel.tsx,api.ts}` + tests
- Create: `frontend/src/app/(app)/settings/page.tsx`

**Interfaces:** 只用 T12 的 `apiFetch`/UI 原语；系统设置按 category 分组、保存单项、显示 restart 标记与掩码；LLM 配置 CRUD/test/refresh/手工模型；用户列表/创建/禁用/重置密码；审计日志分页。
- 测试：设置分组/保存调用/掩码、LLM 表单校验、用户创建。

- [ ] **Step 1: 写 api 映射/表单校验失败测试**。
- [ ] **Step 2: 实现并跑绿**；`pnpm vitest run src/features/settings`。
- [ ] **Step 3: 提交**：`feat(p0): add settings console`。

## Task 14: 资源页 UI（并行，依赖 T12）

**Files:**
- Create: `frontend/src/features/resources/{ResourcesPage.tsx,SUTPanel.tsx,PageAPIPanel.tsx,DataAccessPanel.tsx,CodeRepoPanel.tsx,dialogs.tsx,api.ts}` + tests
- Create: `frontend/src/app/(app)/resources/page.tsx`

**Interfaces:** Tab `suts|pages|data|repos`；SUT 列表 + 新建（自动建默认环境）；环境/页面/API/库/账号 CRUD；页面/API 批量导入（JSON textarea）；仓库 CRUD；密码只显示“已设置”。
- 测试：表单校验、导入解析、SUT→env 默认、删除 409 提示。

- [ ] **Step 1: 写 api/表单失败测试**。
- [ ] **Step 2: 实现并跑绿**；`pnpm vitest run src/features/resources`。
- [ ] **Step 3: 提交**：`feat(p0): add resource console`。

## Task 15: 版本列表/创建/详情壳 UI（并行，依赖 T12）

**Files:**
- Create: `frontend/src/features/versions/{VersionsPage.tsx,VersionDetailPage.tsx,CreateVersionDialog.tsx,BindingWizard.tsx,api.ts}` + tests
- Create: `frontend/src/app/(app)/versions/page.tsx`、`frontend/src/app/(app)/versions/[id]/page.tsx`

**Interfaces:** 列表（状态/SUT 数/时间）；新建对话框（标题 + 多 binding：SUT→环境→页面/API 多选→库/账号→仓库）；详情 header + 8 Tab，overview 展示绑定卡片，其他 Tab 显示 P1+ 占位；`?tab=` 深链。
- 测试：向导校验（无 SUT/无环境/空页面允许）、payload 形状、tab 切换/占位、详情绑定渲染。

- [ ] **Step 1: 写向导/payload/tab 失败测试**。
- [ ] **Step 2: 实现并跑绿**；`pnpm vitest run src/features/versions`。
- [ ] **Step 3: 提交**：`feat(p0): add versions workspace shell`。

## Task 16: Playwright E2E + 演示脚本（并行/最后由 agent）

**Files:**
- Create: `frontend/e2e/p0.spec.ts`、`frontend/e2e/helpers.ts`
- Create: `scripts/p0-e2e.ps1`
- Modify: `frontend/playwright.config.ts`（baseURL 从 env 读取，默认 6868）

**Interfaces:** E2E 动线：空库启动 → admin/admin123 登录 → 创建 SUT/环境/页面/API/库/账号/仓库 → 创建 2 SUT 版本 → 详情 → 设置改并发 → 刷新可见 → 自助改密 → 新密码重新登录。截图到 `.tmp/p0-acceptance/screenshots/`；控制台错误断言为空。
- `p0-e2e.ps1` 启动 test DB + API + Web + fake LLM，等待健康，跑 Playwright，停止，收集日志。

- [ ] **Step 1: 写 E2E 失败测试**（页面元素未实现时 fail）。
- [ ] **Step 2: 实现 helpers/脚本，跑绿**。
- [ ] **Step 3: 保存截图并检查无 console error**。
- [ ] **Step 4: 提交**：`test(p0): add end-to-end demo flow`。

## Task 17: 全量验证、契约冻结、handoff（Lead）

**Files:**
- Modify: `docs/version-V0.2/contracts/*`（与实现核对，标 `frozen: P0`）
- Create: `docs/version-V0.2/phases/phase-00-handoff.md`（按 contracts/handoff-template.md）
- Create: `.tmp/p0-acceptance/acceptance.log`、`e2e/`、`screenshots/`（证据）
- Modify: `DEVELOPMENT_STATUS.md`？不改（P0 handoff 足够）；如需启动说明放 handoff。

**Steps:**
- [ ] 跑 `scripts/test.ps1`（Go 单测/集成 + 前端 lint/typecheck/test）并保存日志。
- [ ] 跑 `scripts/p0-acceptance.ps1` 与 `scripts/p0-e2e.ps1` 并保存日志/截图。
- [ ] 对照 phase-00 验收清单逐项勾选；对照 spec §13 决策清单。
- [ ] 核对 contracts 与实现：OpenAPI 路径/字段、events、db-schema、migrations-order、settings；标 frozen。
- [ ] 写 handoff：完成范围、契约、测试证据、已知缺口/偏差、P1 输入、风险。
- [ ] 提交：`docs(p0): freeze contracts and add phase handoff`。
- [ ] 停止，报告，不开始 P1。

## Task 18: 演示截图与视觉验收（Lead）

- [ ] 启动真实 API/Web（4 env，eztest_v02），用默认 admin/admin123 登录，按动线配置资源/版本。
- [ ] Playwright 截图登录、资源页、版本创建、版本详情、设置页；用 `read_image` 人工检查布局/中文/空状态。
- [ ] 修复视觉/可用性问题并重跑相关测试。
- [ ] 证据路径写入 handoff。

---

## Self-Review

**1. Spec coverage**：spec §1–§15 均有任务：env/config T1；原语 T2；DB/migrations T3；settings T4/T13；contracts T5/T17；identity T6/T12/T13；eventing/storage T7; resource T8/T14; llm T9/T13; version T10/T15; server T11; frontend shell T12; E2E T16; evidence/handoff T17/T18。P0 明确不做的项没有任务。

**2. Type consistency**：`id.Generator`、`settingsapp.Service`、`identityapp.Service`、`eventingapp.Publisher`、`auditapp.Recorder`、`resourceapp.CatalogReader/UsageChecker`、`versionapp.VersionView` 在 T4–T11 中签名一致；前端 `apiFetch/connectSSE/useAuth` 在 T12–T16 一致。

**3. Review Focus tests**：4 env/secret 泄漏 → T1/T4/T6/T9/T12；多 SUT 混用 → T10；事件游标 → T7/T11/T16；权限绕过 → T11；ID 精度/迁移幂等 → T2/T3/T11。

**4. 并行性**：T5–T9、T12 写作用域互不重叠；T10 依赖 T8；T13–T15 依赖 T12；T11 依赖 T6–T10；T16 依赖 T11–T15。共享文件由 Lead 收口，避免 git index 冲突。

**5. Proportion**：本计划约与 spec 同量级，接口/测试是决策，不写实现体；实现细节留给执行者按测试落地。
