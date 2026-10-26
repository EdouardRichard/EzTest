# EzTest P0 Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付 EzTest P0 工程与运行基础：Go/PostgreSQL 后端、认证与审计、SSE V1、LLM 双协议网关、Next.js 壳与设置页、测试与运行文档。

**Architecture:** 模块化单体。Go 标准库 `net/http`；PostgreSQL 事件表 + 进程内 hub；DDD 四层限界上下文（identity/eventing/llm/storage/audit）；Next.js App Router 前端壳；fetch SSE。

**Tech Stack:** Go 1.24.6、pgx/v5、bcrypt、AES-256-GCM、PostgreSQL 17.6、Next.js 16、React 19、Tailwind、Vitest、Playwright、PowerShell 脚本、Dockerfile/Compose（仅静态校验）。

**Spec:** [2026-10-27-eztest-p0-foundation-design.md](../specs/2026-10-27-eztest-p0-foundation-design.md)

## Global Constraints

- P0 范围严格按 `docs/design/implementation-phases.md` §2-P0；**不建**版本/需求/用例/执行/证据/问题/报告实体。
- 端口：Web `6868`、API `2333`、PostgreSQL `5433`；CORS 仅允许 `EZTEST_WEB_ORIGIN=http://localhost:6868`。
- Go module：`github.com/EdouardRichard/EzTest/backend`；Go 1.24.6；只用标准库 `net/http`（Go 1.22+ 方法路由）。
- 目录与导入方向：严格按 `Agent.md` §6（domain → application → infrastructure/interfaces；跨上下文只走 application 接口/事件）。
- 迁移：`backend/internal/db/migrations/`，文件名 `<context>_<version>_<desc>.sql`；`order.txt` 定义全局顺序；只增不改；启动 advisory lock。
- 认证：bcrypt cost 12；token 32B 随机、`ezt_` 前缀、DB 存 SHA-256、12h 过期、可撤销；单管理员 + user；限流 5 次/分钟。
- LLM：`https://api.deepseek.com`、`deepseek-flash`；Chat Completions + Responses；上下文从 `/v1/models.context_window` 读取；Key/headers AES-256-GCM 加密，`EZTEST_SECRET_KEYS` + active version。
- API：`/api`；成功 `{"data":...}`；错误信封 `{"error":{code,message,request_id,retryable,details}}`；`X-Request-ID`；`Idempotency-Key` 24h。
- SSE：`/api/stream`、`/api/stream/events`；通道内 seq；`Last-Event-ID`；20s 心跳；`stream.reset` 无 id、不持久化。
- 前端：Next 16 + React 19 + Tailwind + TypeScript；Biome；Vitest；Playwright 用本机 Chrome/Edge，不下载 Chromium。
- 开发管理员初始密码可明文记录在 `docs/dev-setup.local.md`（gitignored）；DB 仍 bcrypt，API/日志不回显。
- 不提交 `.env`、`.tools/`、`.secrets/`、`*.local.md`；Compose 不做容器运行验证。
- 每个任务先写失败测试，再实现，再跑测试，最后提交；提交信息用 `feat/fix/test/docs/chore` 前缀。

## Review Focus

实现者需特别验证以下五类输入/失败模式，每类在对应任务中有测试：

1. **SSE 断线回放窗口**：快照之后、SSE 订阅之前发布的事件；多发布者并发；慢消费者溢出。期望不丢、不重、seq 连续。
2. **LLM 多模态双协议**：Chat `image_url` 与 Responses `input_image` 的 payload；首字节后断流不得重试；429/5xx/超时映射。
3. **幂等并发**：同 key 并发请求、同 key 不同 body、响应体 >1MB/流式端点。期望只执行一次，冲突 409。
4. **迁移并发/重入**：两个进程同时启动迁移；重复执行；已应用校验和不匹配。期望串行、无副作用、校验失败即停。
5. **认证边界**：过期/撤销/禁用用户的 token；普通用户访问 admin；最后一个管理员自禁用；token 在 SSE 连接中的撤销。

---

## Pre-flight（一次性，不算任务）

- [ ] 切分支：`git switch -c feat/p0-foundation`。
- [ ] 下载 Go zip 到 `.tools/go`，PostgreSQL 17.6 binaries 到 `.tools/pgsql`；初始化 `.tools/pgdata`，端口 5433。
- [ ] 创建数据库 `eztest`、`eztest_test`；生成随机 DB 密码。
- [ ] 生成 `.env`（不提交）：`EZTEST_DATABASE_URL`、`EZTEST_TEST_DATABASE_URL`、`EZTEST_ADMIN_USERNAME=admin`、随机 `EZTEST_ADMIN_PASSWORD`、`EZTEST_SECRET_KEYS={"1":"<base64 32B>"}`、`EZTEST_SECRET_KEY_ACTIVE=1`、`EZTEST_STORAGE_DIR=.data/objects`、`EZTEST_WEB_ORIGIN=http://localhost:6868`、`EZTEST_ENABLE_TEST_ENDPOINTS=true`；保留现有 `llm_base_url`/`llm_key`/`llm_model_name`。
- [ ] 把开发管理员明文密码写入 `docs/dev-setup.local.md`（gitignored）。
- [ ] 确认 `.gitignore` 已包含 `.env`、`.tools/`、`.secrets/`、`*.local.md`。

---

## Task 1: Bootstrap、配置、日志、存活检查

**Files:**
- Create: `backend/go.mod`
- Create: `backend/cmd/eztest/main.go`
- Create: `backend/internal/config/config.go`
- Test: `backend/internal/config/config_test.go`
- Create: `backend/internal/platform/id/id.go`
- Create: `backend/internal/server/server.go`
- Create: `backend/internal/server/health.go`
- Create: `backend/internal/server/middleware.go`
- Test: `backend/internal/server/server_test.go`

**Interfaces:**
- Produces: `config.Load(getenv func(string) string) (Config, error)`；`Config` 字段见规格 §4.1。
- Produces: `server.New(cfg config.Config, logger *slog.Logger, deps Dependencies) http.Handler`；`type Dependencies struct { Pinger Pinger }`；后续任务扩展字段。
- Produces: `id.RequestID() string`；`id.New() string`（32B 随机 base64url）。
- Routes: `GET /healthz` → `200 {"status":"ok"}`；所有响应带 `X-Request-ID`。

- [ ] **Step 1: 写失败测试** `backend/internal/config/config_test.go`

```go
func TestLoadDefaultsAndValidation(t *testing.T) {
    env := map[string]string{
        "EZTEST_DATABASE_URL": "postgres://u:p@localhost:5433/db",
        "EZTEST_SECRET_KEYS":  `{"1":"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="}`,
        "EZTEST_SECRET_KEY_ACTIVE": "1",
    }
    cfg, err := Load(func(k string) string { return env[k] })
    if err != nil { t.Fatal(err) }
    if cfg.HTTPAddr != ":2333" { t.Fatalf("HTTPAddr=%q", cfg.HTTPAddr) }
    if cfg.WebOrigin != "http://localhost:6868" { t.Fatalf("WebOrigin=%q", cfg.WebOrigin) }
    if cfg.Env != "dev" { t.Fatalf("Env=%q", cfg.Env) }
    if len(cfg.SecretKeys[1]) == 0 { t.Fatal("missing secret key") }

    delete(env, "EZTEST_DATABASE_URL")
    _, err = Load(func(k string) string { return env[k] })
    if err == nil || !strings.Contains(err.Error(), "EZTEST_DATABASE_URL") {
        t.Fatalf("expected EZTEST_DATABASE_URL error, got %v", err)
    }
}
```

（测试辅助可用标准库；`require` 不存在时改成 `if` 断言。不要引入 testify。）

- [ ] **Step 2: 运行失败**

Run: `cd backend && go test ./internal/config -run TestLoadDefaultsAndValidation -v`
Expected: FAIL（`config.Load` 不存在/编译失败）

- [ ] **Step 3: 实现配置**

实现 `Config`（字段：Env、HTTPAddr、WebOrigin、DatabaseURL、TestDatabaseURL、AdminUsername、AdminPassword、SecretKeys map[int][]byte、SecretKeyActive int、StorageDir、EnableTestEndpoints bool、LogLevel slog.Level、LLMSeed{BaseURL,Key,Model}）。
解析规则：必填 `EZTEST_DATABASE_URL`、`EZTEST_SECRET_KEYS`、`EZTEST_SECRET_KEY_ACTIVE`；active 必须存在于 keys；`EZTEST_ENV` 默认 `dev`；`EZTEST_HTTP_ADDR` 默认 `:2333`；`EZTEST_WEB_ORIGIN` 默认 `http://localhost:6868`；`EZTEST_STORAGE_DIR` 默认 `.data/objects`；`EZTEST_ENABLE_TEST_ENDPOINTS` 默认 `EZTEST_ENV != "prod"`；`EZTEST_LOG_LEVEL` 默认 `info`。
`go.mod`：module `github.com/EdouardRichard/EzTest/backend`，go 1.24.6。

- [ ] **Step 4: 写健康/请求 ID 测试**

```go
func TestHealthzAndRequestID(t *testing.T) {
    h := New(config.Config{Env: "test"}, slog.New(slog.NewTextHandler(io.Discard, nil)), Dependencies{})
    req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
    rec := httptest.NewRecorder()
    h.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK { t.Fatalf("code=%d", rec.Code) }
    if rec.Header().Get("X-Request-ID") == "" { t.Fatal("missing request id") }
    if got := rec.Header().Get("X-Request-ID"); got != req.Header.Get("X-Request-ID") && req.Header.Get("X-Request-ID") != "" { t.Fatalf("request id mismatch") }
}
```

- [ ] **Step 5: 实现 server/middleware/main**

- `New` 创建 `http.ServeMux`，注册 `/healthz`；应用 `recover -> requestID -> logging`。
- `requestID`：读取 `X-Request-ID`，校验 `^[A-Za-z0-9_-]{8,128}$`，否则 `id.RequestID()`；写回响应。
- `logging`：`slog` 记录 method/path/status/duration/request_id；不要记录 Authorization/body。
- `main`：加载配置、创建 logger、启动 server、`signal.NotifyContext` 优雅关闭。
- `/api/health` 留到 Task 2。

- [ ] **Step 6: 验证**

Run:
`cd backend && go build ./... && go test ./...`
Expected: PASS（Task 1 范围内全部通过）

- [ ] **Step 7: 提交**

```bash
git add backend
git commit -m "feat: bootstrap Go service with config and health"
```

---

## Task 2: 数据库连接、迁移执行器、`/api/health`

**Files:**
- Create: `backend/internal/db/db.go`
- Create: `backend/internal/db/migrate.go`
- Create: `backend/internal/db/embed.go`
- Create: `backend/internal/db/migrations/order.txt`
- Test: `backend/internal/db/migrate_test.go`
- Modify: `backend/internal/server/server.go`, `health.go`
- Modify: `backend/cmd/eztest/main.go`
- Create: `backend/internal/testutil/db.go`

**Interfaces:**
- Produces: `db.Open(ctx context.Context, url string) (*DB, error)`；`(*DB).Close()`、`(*DB).Ping(ctx) error`。
- Produces: `db.Migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) error`；`db.MigrateEmbedded(ctx, pool) error`。
- Produces: `server.Pinger interface { Ping(ctx context.Context) error }`；`/api/health` 返回 `{data:{status,version,commit,db,llm_configured}}`。
- Produces: `testutil.OpenTestDB(t *testing.T) *pgxpool.Pool`（读取 `EZTEST_TEST_DATABASE_URL`，执行迁移，truncate 所有表）。

- [ ] **Step 1: 写迁移幂等测试** `migrate_test.go`

```go
func TestMigrateIdempotentAndChecksum(t *testing.T) {
    pool := testutil.OpenTestDB(t)
    fsys := fstest.MapFS{
        "migrations/order.txt": {Data: []byte("0001_test_a.sql\n0002_test_b.sql\n")},
        "migrations/0001_test_a.sql": {Data: []byte("CREATE TABLE test_a (id INT PRIMARY KEY);")},
        "migrations/0002_test_b.sql": {Data: []byte("CREATE TABLE test_b (id INT PRIMARY KEY);")},
    }
    if err := Migrate(ctx, pool, fsys); err != nil { t.Fatal(err) }
    if err := Migrate(ctx, pool, fsys); err != nil { t.Fatal(err) }
    var n int
    pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&n)
    if n != 2 { t.Fatalf("migrations=%d", n) }
    // 篡改已应用文件后应失败
    fsys["migrations/0001_test_a.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE changed (id INT);")}
    if err := Migrate(ctx, pool, fsys); err == nil { t.Fatal("expected checksum error") }
}
```

- [ ] **Step 2: 运行失败**

Run: `cd backend && EZTEST_TEST_DATABASE_URL=$env:EZTEST_TEST_DATABASE_URL go test -tags=integration ./internal/db -run TestMigrateIdempotentAndChecksum -v`
Expected: FAIL（包/函数不存在）

- [ ] **Step 3: 实现 db/migrate**

- `Open` 使用 `pgxpool.New` + `Ping`。
- `Migrate`：`pg_advisory_lock(4242)`；创建 `schema_migrations(version BIGINT PRIMARY KEY, checksum TEXT, applied_at)`；读 `migrations/order.txt`；逐文件事务执行；已应用校验 SHA-256，不匹配返回 `ErrMigrationChecksum`；重复执行跳过。
- `embed.go`：`//go:embed migrations/*`；`MigrateEmbedded`。
- `order.txt` 初始为空（后续任务追加）。

- [ ] **Step 4: 写 `/api/health` 测试**

```go
type fakePinger struct{ err error }
func (f fakePinger) Ping(ctx context.Context) error { return f.err }

func TestAPIHealth(t *testing.T) {
    h := New(config.Config{Env: "test"}, testLogger(), Dependencies{Pinger: fakePinger{}})
    rec := httptest.NewRecorder()
    h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
    if rec.Code != 200 { t.Fatalf("code=%d body=%s", rec.Code, rec.Body) }
}
func TestAPIHealthDBDown(t *testing.T) {
    h := New(config.Config{Env: "test"}, testLogger(), Dependencies{Pinger: fakePinger{err: errors.New("down")}})
    rec := httptest.NewRecorder()
    h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
    if rec.Code != http.StatusServiceUnavailable { t.Fatalf("code=%d", rec.Code) }
}
```

- [ ] **Step 5: 实现健康端点并接线 main**

`main`：`db.Open` → `db.MigrateEmbedded` → `server.New` with `Dependencies{Pinger: db}`。
`/api/health`：2s 超时 Ping；成功 `{"data":{"status":"ok","version":"dev","commit":"","db":"ok","llm_configured":false}}`；失败 503 `unavailable`。

- [ ] **Step 6: 验证**

Run:
`cd backend && go build ./... && go test ./...`
`$env:EZTEST_TEST_DATABASE_URL=...; go test -tags=integration ./internal/db -v`
Expected: PASS；重复迁移无副作用。

- [ ] **Step 7: 提交**

```bash
git add backend
git commit -m "feat: add postgres pool and idempotent migrations"
```

---

## Task 3: 用户领域、bcrypt、管理员种子

**Files:**
- Create: `backend/internal/identity/domain/user.go`
- Create: `backend/internal/identity/domain/repository.go`
- Create: `backend/internal/identity/infrastructure/password/bcrypt.go`
- Create: `backend/internal/identity/infrastructure/postgres/user_repository.go`
- Create: `backend/internal/identity/application/user_service.go`
- Test: `backend/internal/identity/application/user_service_test.go`
- Test: `backend/internal/identity/infrastructure/postgres/user_repository_integration_test.go`
- Create: `backend/internal/db/migrations/identity_0001_users.sql`
- Modify: `backend/internal/db/migrations/order.txt`
- Modify: `backend/cmd/eztest/main.go`

**Interfaces:**
- Produces: `identity.User{ID int64; Username, PasswordHash string; Role Role; Status Status; CreatedAt, UpdatedAt time.Time; LastLoginAt *time.Time}`；`RoleAdmin="admin"`、`RoleUser="user"`；`StatusActive="active"`、`StatusDisabled="disabled"`。
- Produces: `identity.Repository`：`Create(ctx, CreateParams) (User, error)`、`GetByUsername`、`GetByID`、`List(ctx, limit int, afterID int64) ([]User, bool, error)`、`UpdateStatus`、`UpdatePasswordHash`、`CountAdmins`。
- Produces: `password.Hasher{Hash(string)(string,error); Verify(hash,password string) bool}`；bcrypt cost 12。
- Produces: `identity.UserService`：`Create(ctx, username, password string) (User, error)`、`EnsureAdmin(ctx, username, password string) error`、`List/Get/SetStatus/ResetPassword`。
- Errors: `ErrUsernameTaken`、`ErrUserNotFound`、`ErrLastAdmin`、`ErrInvalidUsername`、`ErrWeakPassword`。

- [ ] **Step 1: 写 bcrypt 与创建用户测试**

```go
func TestPasswordHashAndVerify(t *testing.T) {
    h := password.NewHasher(12)
    hash, err := h.Hash("s3cret-pass")
    if err != nil { t.Fatal(err) }
    if !h.Verify(hash, "s3cret-pass") { t.Fatal("verify failed") }
    if h.Verify(hash, "wrong") { t.Fatal("wrong password accepted") }
}

func TestCreateUserRejectsDuplicateAndWeak(t *testing.T) {
    repo := newFakeUserRepo()
    svc := identity.NewUserService(repo, password.NewHasher(12))
    if _, err := svc.Create(ctx, "qa01", "1234567890"); err != nil { t.Fatal(err) }
    if _, err := svc.Create(ctx, "qa01", "1234567890"); !errors.Is(err, identity.ErrUsernameTaken) { t.Fatalf("err=%v", err) }
    if _, err := svc.Create(ctx, "qa02", "short"); !errors.Is(err, identity.ErrWeakPassword) { t.Fatalf("err=%v", err) }
}
```

- [ ] **Step 2: 运行失败**

Run: `cd backend && go test ./internal/identity/... -run 'TestPasswordHash|TestCreateUser' -v`
Expected: FAIL

- [ ] **Step 3: 实现 domain/hasher/service（fake repo 单元测试通过）**

规则：用户名 `^[a-zA-Z0-9_.-]{3,64}$`；密码 10–128；创建只允许 `RoleUser`；`EnsureAdmin` 在 `CountAdmins()==0` 时创建 admin，否则 no-op；`SetStatus` 禁止 actor 禁用自己；`ResetPassword` 更新 hash 并撤销 token（token 撤销在 Task 4 接线）。
迁移 SQL：

```sql
CREATE TABLE users (
  id BIGSERIAL PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  role TEXT NOT NULL CHECK (role IN ('admin','user')),
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_login_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX users_one_admin ON users(role) WHERE role = 'admin';
```

- [ ] **Step 4: 写 Postgres 仓储集成测试**

```go
func TestUserRepositoryAndAdminSeed(t *testing.T) {
    pool := testutil.OpenTestDB(t)
    repo := postgres.NewUserRepository(pool)
    hash, _ := password.NewHasher(12).Hash("admin-pass-123")
    if err := repo.Create(ctx, identity.CreateParams{Username: "admin", PasswordHash: hash, Role: identity.RoleAdmin}); err != nil { t.Fatal(err) }
    if _, err := repo.Create(ctx, identity.CreateParams{Username: "admin2", PasswordHash: hash, Role: identity.RoleAdmin}); err == nil { t.Fatal("expected single-admin index violation") }
    users, more, err := repo.List(ctx, 10, 0)
    if err != nil || len(users) != 1 || more { t.Fatalf("users=%v more=%v err=%v", users, more, err) }
}
```

- [ ] **Step 5: 实现仓储、迁移、main 种子**

`main` 在迁移后调用 `EnsureAdmin`；缺少 `EZTEST_ADMIN_USERNAME/PASSWORD` 且无管理员 → 启动失败。`order.txt` 追加 `identity_0001_users.sql`。

- [ ] **Step 6: 验证**

Run:
`cd backend && go test ./internal/identity/...`
`go test -tags=integration ./internal/identity/infrastructure/postgres -v`
Expected: PASS

- [ ] **Step 7: 提交**

```bash
git add backend
git commit -m "feat: add users, bcrypt hashing, and admin bootstrap"
```

---

## Task 4: Token、登录/登出/me、认证中间件与角色

**Files:**
- Create: `backend/internal/identity/domain/token.go`
- Create: `backend/internal/identity/domain/token_repository.go`
- Create: `backend/internal/identity/infrastructure/postgres/token_repository.go`
- Create: `backend/internal/identity/application/auth_service.go`
- Test: `backend/internal/identity/application/auth_service_test.go`
- Test: `backend/internal/identity/infrastructure/postgres/token_repository_integration_test.go`
- Create: `backend/internal/identity/interfaces/httpapi/auth_handlers.go`
- Test: `backend/internal/identity/interfaces/httpapi/auth_handlers_test.go`
- Create: `backend/internal/identity/interfaces/httpapi/auth_middleware.go`
- Create: `backend/internal/db/migrations/identity_0002_auth_tokens.sql`
- Modify: `backend/internal/db/migrations/order.txt`
- Modify: `backend/internal/server/server.go`, `backend/cmd/eztest/main.go`

**Interfaces:**
- Produces: `identity.Token{ID, UserID int64; TokenHash string; ExpiresAt time.Time; RevokedAt *time.Time}`。
- Produces: `identity.TokenRepository`：`Create(ctx, userID int64, tokenHash string, expiresAt time.Time) error`、`GetByHash`、`Revoke(ctx, hash string)`、`RevokeAllForUser`。
- Produces: `identity.AuthService.Login(ctx, username, password string) (LoginResult, error)`、`Authenticate(ctx, rawToken string) (User, error)`、`Logout(ctx, rawToken string) error`；`LoginResult{Token string; ExpiresAt time.Time; User User}`。
- Produces: `identityhttp.AuthMiddleware(authenticator Authenticator) func(http.Handler) http.Handler`、`RequireAdmin`、`UserFromContext(ctx) (identity.User, bool)`。
- Routes: `POST /api/auth/login`、`POST /api/auth/logout`、`GET /api/auth/me`。

- [ ] **Step 1: 写 AuthService 单元测试**

```go
func TestLoginCreatesHashedTokenAndAuthenticates(t *testing.T) {
    users := newFakeUserRepoWith(t, "admin", "admin-pass-123", identity.RoleAdmin)
    tokens := newFakeTokenRepo()
    svc := identity.NewAuthService(users, tokens, password.NewHasher(12), func() time.Time { return fixedNow })
    res, err := svc.Login(ctx, "admin", "admin-pass-123")
    if err != nil { t.Fatal(err) }
    if !strings.HasPrefix(res.Token, "ezt_") { t.Fatalf("token prefix: %s", res.Token) }
    if tokens.lastHash == res.Token { t.Fatal("raw token stored") }
    u, err := svc.Authenticate(ctx, res.Token)
    if err != nil || u.Username != "admin" { t.Fatalf("u=%v err=%v", u, err) }
}

func TestExpiredRevokedDisabledRejected(t *testing.T) { /* table-driven: expired, revoked, disabled, wrong password */ }
```

- [ ] **Step 2: 运行失败**

Run: `cd backend && go test ./internal/identity/application -run TestLogin -v`
Expected: FAIL

- [ ] **Step 3: 实现 Token/AuthService**

- token：`ezt_` + base64.RawURLEncoding(32 random bytes)；`sha256.Sum256([]byte(raw))` hex；TTL 12h；登录更新 `last_login_at`。
- Authenticate：按 hash 查 token；`RevokedAt != nil` 或 `now > ExpiresAt` → `ErrUnauthenticated`；加载 user；disabled → `ErrUnauthenticated`。
- Logout：revoke 当前 hash。
- 迁移：

```sql
CREATE TABLE auth_tokens (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash TEXT NOT NULL UNIQUE,
  expires_at TIMESTAMPTZ NOT NULL,
  revoked_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at TIMESTAMPTZ
);
CREATE INDEX auth_tokens_user_id_idx ON auth_tokens(user_id);
```

- [ ] **Step 4: 写 handler/中间件测试**

```go
func TestLoginAndMe(t *testing.T) {
    h := newTestAPI(t) // 注入 fake AuthService
    rec := postJSON(h, "/api/auth/login", `{"username":"admin","password":"admin-pass-123"}`)
    if rec.Code != 200 { t.Fatalf("login=%d %s", rec.Code, rec.Body) }
    token := decodeToken(t, rec.Body)
    rec = getWithBearer(h, "/api/auth/me", token)
    if rec.Code != 200 { t.Fatalf("me=%d", rec.Code) }
}
func TestAdminEndpointForbiddenForUser(t *testing.T) { /* user token -> 403 */ }
```

- [ ] **Step 5: 实现 handler/中间件并接线**

- `POST /api/auth/login` 返回 `{data:{token,expires_at,user}}`；错误 401 `unauthenticated`。
- `GET /api/auth/me` 返回当前用户。
- `POST /api/auth/logout` 204。
- `AuthMiddleware` 解析 Bearer；失败 401 + `WWW-Authenticate: Bearer`；成功把 user 放 context。
- `RequireAdmin` 检查角色。
- 登录失败限流：内存 map，5 次/分钟，锁 1 分钟；测试可注入 clock。P0 单实例。
- `order.txt` 追加 `identity_0002_auth_tokens.sql`。

- [ ] **Step 6: 验证**

Run:
`cd backend && go test ./internal/identity/... && go test -tags=integration ./internal/identity/infrastructure/postgres -v`
Expected: PASS（含过期、撤销、禁用、角色）

- [ ] **Step 7: 提交**

```bash
git add backend
git commit -m "feat: add token auth, login endpoints, and role middleware"
```

---

## Task 5: 审计上下文

**Files:**
- Create: `backend/internal/audit/domain/entry.go`
- Create: `backend/internal/audit/domain/repository.go`
- Create: `backend/internal/audit/infrastructure/postgres/repository.go`
- Create: `backend/internal/audit/application/recorder.go`
- Test: `backend/internal/audit/application/recorder_test.go`
- Test: `backend/internal/audit/infrastructure/postgres/repository_integration_test.go`
- Create: `backend/internal/db/migrations/audit_0001_logs.sql`
- Modify: `backend/internal/db/migrations/order.txt`
- Modify: `backend/internal/identity/application/{user_service,auth_service}.go`
- Modify: `backend/cmd/eztest/main.go`

**Interfaces:**
- Produces: `audit.Entry{ActorUserID *int64; Action, TargetType, TargetID, RequestID, IP, UserAgent string; Metadata map[string]any; CreatedAt time.Time}`。
- Produces: `audit.Recorder`：`Record(ctx, Entry) error`、`List(ctx, limit int, afterID int64) ([]Entry, bool, error)`。
- `audit.NopRecorder` 供测试/未接线使用。
- 签名变更：Task 3/4 的 `NewUserService` / `NewAuthService` 增加 `recorder audit.Recorder` 参数；同步更新它们的测试与 `cmd` 装配。

- [ ] **Step 1: 写 recorder 测试**

```go
func TestAuthAndUserActionsAreAudited(t *testing.T) {
    rec := &fakeRecorder{}
    svc := identity.NewAuthService(users, tokens, hasher, clock, rec)
    _, _ = svc.Login(ctx, "admin", "wrong")
    if len(rec.entries) != 1 || rec.entries[0].Action != "auth.login_failed" { t.Fatalf("%+v", rec.entries) }
    if rec.entries[0].Metadata["password"] != nil { t.Fatal("password leaked") }
}
```

- [ ] **Step 2: 运行失败**

Run: `cd backend && go test ./internal/audit/... ./internal/identity/application -run TestAuthAndUserActionsAreAudited -v`
Expected: FAIL

- [ ] **Step 3: 实现 audit 上下文与接线**

- `Entry` 字段；`Recorder` 接口；Postgres repo `Record`/`List`（按 id 倒序游标）。
- Auth/UserService 增加 `Recorder` 依赖；记录：`auth.login_succeeded/failed`、`auth.logout`、`user.created`、`user.updated`、`user.password_reset`。
- metadata 不写密码/token/Key/headers。
- 迁移：

```sql
CREATE TABLE audit_logs (
  id BIGSERIAL PRIMARY KEY,
  actor_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  action TEXT NOT NULL,
  target_type TEXT,
  target_id TEXT,
  request_id TEXT,
  ip TEXT,
  user_agent TEXT,
  metadata JSONB NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX audit_logs_created_at_idx ON audit_logs(created_at DESC);
```

- [ ] **Step 4: 集成测试**

验证：登录失败/成功、创建用户产生记录；`List` 分页；metadata 不含敏感字段。`order.txt` 追加 `audit_0001_logs.sql`。

- [ ] **Step 5: 验证**

Run:
`cd backend && go test ./internal/audit/... ./internal/identity/... && go test -tags=integration ./internal/audit/infrastructure/postgres -v`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add backend
git commit -m "feat: add audit log recorder and wire auth/user actions"
```

---

## Task 6: HTTP 约定、分页、幂等

**Files:**
- Create: `backend/internal/platform/errors.go`
- Create: `backend/internal/platform/pagination.go`
- Create: `backend/internal/platform/validation.go`
- Create: `backend/internal/platform/idempotency/store.go`
- Create: `backend/internal/platform/idempotency/middleware.go`
- Create: `backend/internal/platform/idempotency/postgres.go`
- Test: `backend/internal/platform/errors_test.go`
- Test: `backend/internal/platform/idempotency/middleware_test.go`
- Create: `backend/internal/server/cors.go`
- Test: `backend/internal/server/cors_test.go`
- Create: `backend/internal/db/migrations/platform_0001_idempotency.sql`
- Modify: `backend/internal/db/migrations/order.txt`
- Modify: `backend/internal/server/{server.go,middleware.go}`
- Modify: `backend/cmd/eztest/main.go`

**Interfaces:**
- Produces: `platform.APIError{Code,Message string; RequestID string; Retryable bool; Details map[string]any}`；`platform.WriteError(w, r, status int, code, message string, opts ...ErrorOption)`；`platform.DecodeJSON(r *http.Request, dst any, maxBytes int64) error`。
- Produces: `platform.WithRequestID(ctx, id string) context.Context`、`platform.RequestID(ctx) string`；`platform.ErrorOption`、`platform.WithDetails`、`platform.WithRetryable`。
- Produces: `platform.ParseLimit(raw string) (int, error)`、`platform.EncodeCursor(id int64) string`、`platform.DecodeCursor(string) (int64, error)`。
- Produces: `idempotency.Store`：`Get(ctx, userID int64, key, method, path string) (*StoredResponse, error)`、`Save(ctx, userID int64, key, method, path, requestHash string, status int, body []byte) error`；`StoredResponse{Status int; Body []byte; RequestHash string}`；`idempotency.Middleware(store Store, userID func(context.Context)(int64,bool)) func(http.Handler) http.Handler`。
- Produces: `server.CORS(origin string) func(http.Handler) http.Handler`：允许 `Authorization, Content-Type, X-Request-ID, Idempotency-Key, Last-Event-ID`；暴露 `X-Request-ID, X-Snapshot-Cursor, Idempotent-Replayed`；`credentials=false`；OPTIONS 204。

- [ ] **Step 1: 写错误/分页测试**

```go
func TestErrorEnvelopeAndRequestID(t *testing.T) {
    rec := httptest.NewRecorder()
    r := httptest.NewRequest(http.MethodGet, "/x", nil)
    r = r.WithContext(platform.WithRequestID(r.Context(), "req_12345678"))
    platform.WriteError(rec, r, http.StatusBadRequest, "invalid_argument", "bad", platform.WithDetails(map[string]any{"field":"username"}))
    var body struct{ Error platform.APIError `json:"error"` }
    json.Unmarshal(rec.Body.Bytes(), &body)
    if body.Error.RequestID != "req_12345678" || body.Error.Code != "invalid_argument" { t.Fatalf("%+v", body) }
}
func TestCursorRoundTrip(t *testing.T) { /* EncodeCursor/DecodeCursor */ }
func TestCORSPreflight(t *testing.T) {
    h := CORS("http://localhost:6868")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
    req := httptest.NewRequest(http.MethodOptions, "/api/users", nil)
    req.Header.Set("Origin", "http://localhost:6868")
    req.Header.Set("Access-Control-Request-Method", "POST")
    rec := httptest.NewRecorder(); h.ServeHTTP(rec, req)
    if rec.Code != 204 { t.Fatalf("code=%d", rec.Code) }
    if rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:6868" { t.Fatal("missing ACAO") }
    if !strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "Authorization") { t.Fatal("missing auth header") }
}
```

- [ ] **Step 2: 运行失败**

Run: `cd backend && go test ./internal/platform -run 'TestError|TestCursor' -v`
Expected: FAIL

- [ ] **Step 3: 实现 errors/pagination/validation/CORS**

错误码常量；`WriteError` 设置 `Content-Type: application/json`；`DecodeJSON` 限制 body 1MB、拒绝未知字段（`DisallowUnknownFields` 可选，按端点决定）；分页游标 base64(`id`) + limit 1–100 默认 20；CORS 中间件按规格头/来源。

- [ ] **Step 4: 写幂等测试**

```go
func TestIdempotencyReplayAndConflict(t *testing.T) {
    store := newFakeIdempotencyStore()
    h := idempotency.Middleware(store, func(ctx context.Context) (int64, bool) { return 1, true })(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json"); w.WriteHeader(201); w.Write([]byte(`{"data":{"id":1}}`))
    }))
    newReq := func() *http.Request {
        req := httptest.NewRequest(http.MethodPost, "/api/users", strings.NewReader(`{"username":"qa01"}`))
        req.Header.Set("Idempotency-Key", "key_12345678")
        return req
    }
    rec1 := httptest.NewRecorder(); h.ServeHTTP(rec1, newReq())
    rec2 := httptest.NewRecorder(); h.ServeHTTP(rec2, newReq())
    if rec2.Header().Get("Idempotent-Replayed") != "true" { t.Fatal("not replayed") }
    // 同 key 不同 body -> 409
}
func TestIdempotencyConcurrentSameKey(t *testing.T) { /* 两个 goroutine，handler 只执行一次 */ }
```

- [ ] **Step 5: 实现 store/middleware/migration**

- `idempotency_keys` 表（见规格）；`request_hash` = SHA-256(method+"\n"+path+"\n"+body)。
- Middleware：仅非 GET；从 context 取 userID；无 key 直接放行；同 key 同 body 重放；不同 body 409；缓存 <1MB JSON；跳过 `/api/stream`、`/api/auth/*`、`/api/admin/llm/configs/{id}/test` 且 `stream=true`。
- 响应缓冲 writer 捕获 status/body；`Idempotent-Replayed: true`。
- 接入 server 中间件链（CORS 在 auth 之前；idempotency 在 auth 之后）。
- `order.txt` 追加 `platform_0001_idempotency.sql`。

- [ ] **Step 6: 验证**

Run:
`cd backend && go test ./internal/platform/... && go test -tags=integration ./internal/platform/idempotency -v`
Expected: PASS（含并发、同 key 不同 body）

- [ ] **Step 7: 提交**

```bash
git add backend
git commit -m "feat: add API error envelope, pagination, and idempotency"
```

---

## Task 7: 用户/审计 HTTP 端点

**Files:**
- Create: `backend/internal/identity/interfaces/httpapi/user_handlers.go`
- Test: `backend/internal/identity/interfaces/httpapi/user_handlers_test.go`
- Create: `backend/internal/audit/interfaces/httpapi/handlers.go`
- Test: `backend/internal/audit/interfaces/httpapi/handlers_test.go`
- Modify: `backend/internal/server/server.go`

**Interfaces:**
- Routes: `GET/POST /api/users`、`GET/PATCH /api/users/{id}`、`POST /api/users/{id}/reset-password`、`GET /api/admin/audit`。
- `UserService` 增加 `TokenRevoker` 依赖（Task 4 的 `RevokeAllForUser`）；`ResetPassword` 更新 hash 后撤销该用户全部 token。
- 全部 admin；普通用户 403；创建只允许 `role=user`；PATCH 只改 status；管理员不能禁用自己。

- [ ] **Step 1: 写 handler 测试**

```go
func TestAdminUserCRUDAndForbidden(t *testing.T) {
    api := newTestAPI(t) // 真实 service + fake repos，或集成
    admin := loginAs(t, api, "admin", "admin-pass-123")
    rec := postJSONWithBearer(api, "/api/users", `{"username":"qa01","password":"qa-pass-123"}`, admin)
    if rec.Code != 201 { t.Fatalf("create=%d %s", rec.Code, rec.Body) }
    user := loginAs(t, api, "qa01", "qa-pass-123")
    rec = getWithBearer(api, "/api/users", user)
    if rec.Code != 403 { t.Fatalf("expected 403, got %d", rec.Code) }
}
```

- [ ] **Step 2: 运行失败**

Run: `cd backend && go test ./internal/identity/interfaces/httpapi ./internal/audit/interfaces/httpapi -run TestAdminUserCRUD -v`
Expected: FAIL

- [ ] **Step 3: 实现 handler + 路由**

- 使用 `platform.WriteError/DecodeJSON/ParseLimit`；响应 `{data:...}`。
- `GET /api/users` 返回 `data` + `next_cursor`/`has_more`。
- `POST /api/users` 创建 user；返回 201。
- `PATCH` 校验 status 枚举；禁止操作自己。
- `reset-password` 更新 hash，调用 token repo `RevokeAllForUser`。
- `GET /api/admin/audit` 返回审计列表。
- 在 server 注册路由并套 `AuthMiddleware + RequireAdmin`。

- [ ] **Step 4: 集成验证**

Run:
`cd backend && go test ./internal/identity/interfaces/httpapi ./internal/audit/interfaces/httpapi -v`
`go test -tags=integration ./...`
Expected: PASS（含权限、分页、重置密码撤销）

- [ ] **Step 5: 提交**

```bash
git add backend
git commit -m "feat: expose user management and audit APIs"
```

---

## Task 8: 事件存储、hub、SSE V1

**Files:**
- Create: `backend/internal/eventing/domain/event.go`
- Create: `backend/internal/eventing/domain/store.go`
- Create: `backend/internal/eventing/infrastructure/postgres/store.go`
- Create: `backend/internal/eventing/infrastructure/hub.go`
- Create: `backend/internal/eventing/application/service.go`
- Create: `backend/internal/eventing/interfaces/httpapi/sse.go`
- Create: `backend/internal/eventing/interfaces/httpapi/snapshot.go`
- Create: `backend/internal/eventing/interfaces/httpapi/test_publisher.go`
- Test: `backend/internal/eventing/infrastructure/postgres/store_integration_test.go`
- Test: `backend/internal/eventing/interfaces/httpapi/sse_test.go`
- Create: `backend/internal/db/migrations/eventing_0001_events.sql`
- Modify: `backend/internal/db/migrations/order.txt`
- Modify: `backend/internal/server/server.go`, `backend/cmd/eztest/main.go`

**Interfaces:**
- Produces: `eventing.Event{Seq int64; Channel, Type string; VersionID,BatchID,CaseID,StepID,AgentRole,SessionID *string; TS time.Time; Summary string; Detail json.RawMessage; Refs Refs}`；`Refs{Evidence,Case,Requirement,Code []string}`。
- Produces: `eventing.NewEvent{Channel,Type string; VersionID,BatchID,CaseID,StepID,AgentRole,SessionID *string; Summary string; Detail json.RawMessage; Refs Refs; TS time.Time}`；`eventing.ErrCursorTooOld`。
- Produces: `eventing.Store`：`Publish(ctx, NewEvent) (Event, error)`、`ListAfter(ctx, channel string, afterSeq int64, limit int) ([]Event, error)`、`ListBefore(ctx, channel string, beforeSeq int64, limit int) ([]Event, bool, error)`、`LatestSeq(ctx, channel string) (int64, error)`。
- Produces: `eventing.Service`：`Publish(ctx, NewEvent) (Event, error)`、`Snapshot(ctx, channel string, beforeSeq int64, limit int) ([]Event, int64, bool, error)`、`Subscribe(channel string) (<-chan Event, func())`。
- Routes: `GET /api/stream/events`、`GET /api/stream`、`POST /api/admin/stream/test-events`。

- [ ] **Step 1: 写并发 seq/回放集成测试**

```go
func TestPublishMonotonicAndReplay(t *testing.T) {
    pool := testutil.OpenTestDB(t)
    store := postgres.NewEventStore(pool)
    var wg sync.WaitGroup
    for i := 0; i < 20; i++ { wg.Add(1); go func(i int) { defer wg.Done(); _, err := store.Publish(ctx, eventing.NewEvent{Channel:"system", Type:"system.test", Summary: fmt.Sprint(i), TS: time.Now().UTC()}); if err != nil { t.Error(err) } }(i) }
    wg.Wait()
    all, err := store.ListAfter(ctx, "system", 0, 100)
    if err != nil || len(all) != 20 { t.Fatalf("len=%d err=%v", len(all), err) }
    for i, e := range all { if e.Seq != int64(i+1) { t.Fatalf("seq[%d]=%d", i, e.Seq) } }
}
```

- [ ] **Step 2: 运行失败**

Run: `cd backend && go test -tags=integration ./internal/eventing/... -run TestPublishMonotonicAndReplay -v`
Expected: FAIL

- [ ] **Step 3: 实现 store/hub/service**

- 迁移：`event_channels`、`events`（见规格）；`order.txt` 追加。
- Publish 事务：`INSERT ... ON CONFLICT ... RETURNING last_seq` → insert event → commit → hub broadcast。
- Hub：每通道订阅者 map；缓冲 256；溢出关闭。
- Snapshot：`ListBefore` + `LatestSeq`。
- Service 组合 store+hub。

- [ ] **Step 4: 写 SSE handler 测试**

```go
func TestSSEReplayNoGapAndHeartbeat(t *testing.T) {
    store := newFakeEventStore(...) // 已有 seq 1..3
    hub := eventing.NewHub()
    srv := httptest.NewServer(newSSEHandler(store, hub, 20*time.Millisecond))
    req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/stream?channel=system&since=1", nil)
    req.Header.Set("Authorization", "Bearer "+token)
    resp, err := http.DefaultClient.Do(req)
    // 读取事件 id 2,3；等待心跳注释；期间 publish seq 4；断言 4 到达；无重复
}
func TestSSEResetOnTooOldCursor(t *testing.T) { /* fake store returns ErrCursorTooOld；断言 event: stream.reset 且无 id */ }
```

- [ ] **Step 5: 实现 SSE/snapshot/test publisher**

- SSE handler：认证；`since`/`Last-Event-ID`；先订阅再回放；按 seq 去重；`id/event/data`；flush；20s 心跳（测试可注入）。
- 快照 handler：`{data:{items,snapshot_cursor,has_more}}`。
- 测试发布：仅 `EZTEST_ENABLE_TEST_ENDPOINTS=true` 且 admin；count 1–100；`type=system.test`；写审计 `sse.test_published`。
- 路由接线：`/api/stream` 需认证；`/api/admin/stream/test-events` 需 admin。

- [ ] **Step 6: 验证**

Run:
`cd backend && go test ./internal/eventing/... && go test -tags=integration ./internal/eventing/... -v`
Expected: PASS（并发 seq、回放、心跳、reset）

- [ ] **Step 7: 提交**

```bash
git add backend
git commit -m "feat: add persistent event stream and SSE v1 endpoints"
```

---

## Task 9: LLM 配置、AES-GCM、模型目录

**Files:**
- Create: `backend/internal/platform/crypto/aesgcm.go`
- Test: `backend/internal/platform/crypto/aesgcm_test.go`
- Create: `backend/internal/llm/domain/config.go`
- Create: `backend/internal/llm/domain/repository.go`
- Create: `backend/internal/llm/infrastructure/postgres/repository.go`
- Create: `backend/internal/llm/application/config_service.go`
- Test: `backend/internal/llm/application/config_service_test.go`
- Test: `backend/internal/llm/infrastructure/postgres/repository_integration_test.go`
- Create: `backend/internal/db/migrations/llm_0001_configs.sql`
- Modify: `backend/internal/db/migrations/order.txt`
- Modify: `backend/cmd/eztest/main.go`

**Interfaces:**
- Produces: `crypto.Keyring{Encrypt([]byte)([]byte,int,error); Decrypt([]byte,int)([]byte,error)}`；`crypto.NewKeyring(keys map[int][]byte, active int) (*Keyring, error)`。
- Produces: `llm.Config{ID int64; Name string; Protocol Protocol; BaseURL, Model, APIKey string; Headers map[string]string; RetryCount,RetryIntervalMS,TimeoutMS int; StreamEnabled,MultimodalEnabled,IsDefault bool; ModelCatalog []ModelInfo; CatalogFetchedAt *time.Time}`；`ModelInfo{ID string; ContextWindow *int64; MaxOutputTokens *int64; InputModalities []string}`。
- Produces: `llm.Repository`：`Create/Update/Delete/Get/List/GetDefault`。
- Produces: `llm.ConfigService`：`Create/Update/Delete/List/Get/GetDefault/SeedFromEnv`；校验 base_url/protocol/retry/timeout；设置默认同事务清除其他默认。

- [ ] **Step 1: 写 Keyring 测试**

```go
func TestKeyringRoundTripAndVersions(t *testing.T) {
    kr, err := crypto.NewKeyring(map[int][]byte{1: bytes.Repeat([]byte{1}, 32), 2: bytes.Repeat([]byte{2}, 32)}, 2)
    if err != nil { t.Fatal(err) }
    ct, v, err := kr.Encrypt([]byte("secret"))
    if err != nil || v != 2 { t.Fatalf("v=%d err=%v", v, err) }
    pt, err := kr.Decrypt(ct, v)
    if err != nil || string(pt) != "secret" { t.Fatalf("pt=%q err=%v", pt, err) }
    if _, err := kr.Decrypt(ct, 9); err == nil { t.Fatal("missing key version should fail") }
}
```

- [ ] **Step 2: 运行失败**

Run: `cd backend && go test ./internal/platform/crypto -run TestKeyring -v`
Expected: FAIL

- [ ] **Step 3: 实现 Keyring 与迁移**

AES-GCM：12B random nonce，`nonce||ciphertext`；AAD `eztest.llm.api_key`/`eztest.llm.headers`。迁移 `llm_configs`（见规格），部分唯一默认索引；`order.txt` 追加。

- [ ] **Step 4: 写配置仓储/种子测试**

```go
func TestConfigRepositoryEncryptsAndSeeds(t *testing.T) {
    pool := testutil.OpenTestDB(t)
    kr, _ := crypto.NewKeyring(map[int][]byte{1: bytes.Repeat([]byte{1}, 32)}, 1)
    repo := postgres.NewConfigRepository(pool, kr)
    cfg, err := repo.Create(ctx, llm.CreateParams{Name:"default", Protocol:llm.ProtocolChat, BaseURL:"https://api.deepseek.com", Model:"deepseek-flash", APIKey:"sk-test", Headers:map[string]string{"x-test":"1"}, IsDefault:true})
    if err != nil { t.Fatal(err) }
    var raw []byte
    pool.QueryRow(ctx, `SELECT api_key_encrypted FROM llm_configs WHERE id=$1`, cfg.ID).Scan(&raw)
    if bytes.Contains(raw, []byte("sk-test")) { t.Fatal("key stored in plaintext") }
    got, err := repo.Get(ctx, cfg.ID)
    if err != nil || got.APIKey != "sk-test" || got.Headers["x-test"] != "1" { t.Fatalf("got=%+v err=%v", got, err) }
}
```

- [ ] **Step 5: 实现 repo/service/main 种子**

- Repo 加密 Key/headers，存 `api_key_encrypted`/`headers_encrypted`/`key_version`。
- `SeedFromEnv`：无默认且 `.env` 三值齐全 → 创建 default（chat_completions，retry 2/500ms/timeout 60s/stream+multimodal true）；写审计 `llm.config_created`。
- `ConfigService` 注入 `audit.Recorder`；Create/Update/Delete 记录审计；headers 禁止覆盖 `Authorization`/`Content-Type`。
- 校验；默认切换事务。
- 模型目录暂空，Task 10 刷新。

- [ ] **Step 6: 验证**

Run:
`cd backend && go test ./internal/platform/crypto ./internal/llm/... && go test -tags=integration ./internal/llm/infrastructure/postgres -v`
Expected: PASS（加密、种子、默认唯一）

- [ ] **Step 7: 提交**

```bash
git add backend
git commit -m "feat: add encrypted LLM config storage and seeding"
```

---

## Task 10: LLM 双协议适配、重试、流式、多模态、管理端点

**Files:**
- Create: `backend/internal/llm/infrastructure/deepseek/client.go`
- Create: `backend/internal/llm/infrastructure/deepseek/chat.go`
- Create: `backend/internal/llm/infrastructure/deepseek/responses.go`
- Create: `backend/internal/llm/infrastructure/deepseek/models.go`
- Create: `backend/internal/llm/application/client.go`
- Create: `backend/internal/llm/application/retry.go`
- Test: `backend/internal/llm/infrastructure/deepseek/client_test.go`
- Test: `backend/internal/llm/application/retry_test.go`
- Create: `backend/internal/llm/interfaces/httpapi/handlers.go`
- Test: `backend/internal/llm/interfaces/httpapi/handlers_test.go`
- Modify: `backend/internal/server/server.go`, `backend/cmd/eztest/main.go`
- Create: `backend/testdata/pixel.png`（1×1 PNG）

**Interfaces:**
- Produces: `llm.Client`：`Chat(ctx, ChatRequest) (ChatResponse, error)`、`ChatStream(ctx, ChatRequest) (<-chan StreamEvent, error)`、`ListModels(ctx) ([]ModelInfo, error)`。
- `ChatRequest{Model string; Messages []Message; Stream bool; MaxTokens int}`；`Message{Role string; Content []Part}`；`Part{Type,Text,ImageURL string}`。
- Produces: `llm.NewDeepSeekClient(cfg llm.Config, hc *http.Client) llm.Client`；`llm.NewRetryClient(client, retryCount, interval, timeout)`。
- Routes: config CRUD、`catalog/refresh`、`test`（stream/non-stream）；admin only。

- [ ] **Step 1: 写 mock 适配测试**

```go
func TestChatCompletionsNonStreamAndImage(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        var body map[string]any
        json.NewDecoder(r.Body).Decode(&body)
        if r.URL.Path != "/v1/chat/completions" { t.Errorf("path=%s", r.URL.Path) }
        msgs := body["messages"].([]any)
        content := msgs[0].(map[string]any)["content"].([]any)
        if content[1].(map[string]any)["type"] != "image_url" { t.Errorf("no image_url") }
        w.Header().Set("Content-Type","application/json")
        w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
    }))
    c := llm.NewDeepSeekClient(llm.Config{BaseURL: srv.URL, Model:"deepseek-flash", Protocol:llm.ProtocolChat}, srv.Client())
    res, err := c.Chat(ctx, llm.ChatRequest{Model:"deepseek-flash", Messages:[]llm.Message{{Role:"user", Content:[]llm.Part{{Type:"text",Text:"describe"},{Type:"image_url",ImageURL:"data:image/png;base64,AA=="}}}}})
    if err != nil || res.Text != "ok" { t.Fatalf("res=%+v err=%v", res, err) }
}
func TestResponsesNonStreamAndImage(t *testing.T) { /* /v1/responses, input_image */ }
func TestRetryOn429ThenSuccess(t *testing.T) { /* 1st 429, 2nd 200; retries=1 */ }
func TestRetryAfterStreamStarted(t *testing.T) { /* 首字节后断流 -> 不重试，返回错误 */ }
func TestModelsContextWindow(t *testing.T) { /* /v1/models 返回 context_window=1048576 */ }
```

- [ ] **Step 2: 运行失败**

Run: `cd backend && go test ./internal/llm/... -run 'TestChatCompletions|TestResponses|TestRetry|TestModels' -v`
Expected: FAIL

- [ ] **Step 3: 实现适配器与重试**

- 路径归一化：base 以 `/v1` 结尾则直接拼，否则补 `/v1`。
- Chat：`messages[].content` 数组；Responses：`input_image`；解析文本/usage。
- 流式：解析 SSE；统一 `StreamEvent{Delta,Done,Err,Usage}`；首字节后不重试。
- 重试：429/5xx/timeout；固定间隔 + jitter；由 `RetryClient` 包装。
- 模型目录：解析 `data[].{id,context_window,max_output_tokens,input_modalities}`。

- [ ] **Step 4: 写管理端点测试**

```go
func TestLLMConfigAdminOnlyAndTestEndpoint(t *testing.T) {
    // admin: GET configs 返回 api_key 掩码/headers 名；普通用户 403
    // POST /test with image=true/stream=false 返回文本；stream=true 返回 SSE llm.done
    // catalog refresh 更新 context_window
}
```

- [ ] **Step 5: 实现 handlers 与接线**

- 配置 CRUD；GET 不返回明文（返回 `has_api_key`、`key_version`、header 名）。**注意：这是规格最终版，不是开发明文版。**
- `catalog/refresh` 调 `ListModels`，写回 `model_catalog`。
- `test`：用保存配置；`image=true` 读 `testdata/pixel.png`；`stream=true` 返回归一化 SSE；`stream_enabled`/`multimodal_enabled` 校验；成功/失败写审计 `llm.test_succeeded`/`llm.test_failed`。
- 错误映射 `llm_*`；`llm_not_configured`。
- 更新 `/api/health` 的 `llm_configured`：存在默认配置且 Key 可解密才为 true。
- 路由套 admin。
- 真实 LLM 验收脚本（gated `EZTEST_RUN_REAL_LLM=1`）：登录 admin → 调 `/test`（Chat 含图；Responses 文本）→ 断言 200/文本非空。

- [ ] **Step 6: 验证**

Run:
`cd backend && go test ./internal/llm/... && go test -tags=integration ./internal/llm/... -v`
Expected: PASS（mock 双协议/重试/流式/图片）

- [ ] **Step 7: 提交**

```bash
git add backend
git commit -m "feat: add DeepSeek chat/responses adapters and LLM admin APIs"
```

---

## Task 11: 对象存储抽象

**Files:**
- Create: `backend/internal/storage/domain/object.go`
- Create: `backend/internal/storage/domain/store.go`
- Create: `backend/internal/storage/infrastructure/localfs/store.go`
- Create: `backend/internal/storage/application/service.go`
- Create: `backend/internal/storage/interfaces/httpapi/handlers.go`
- Test: `backend/internal/storage/infrastructure/localfs/store_test.go`
- Test: `backend/internal/storage/interfaces/httpapi/handlers_test.go`
- Create: `backend/internal/db/migrations/storage_0001_objects.sql`
- Modify: `backend/internal/db/migrations/order.txt`
- Modify: `backend/internal/server/server.go`, `backend/cmd/eztest/main.go`

**Interfaces:**
- Produces: `storage.Object{SHA256 string; Size int64; ContentType, RelativePath string}`。
- Produces: `storage.Store`：`Put(ctx, io.Reader, contentType string) (Object, error)`、`Open(ctx, sha256 string) (io.ReadCloser, Object, error)`、`Stat(ctx, sha256 string) (Object, error)`。
- Routes: `POST /api/admin/storage/objects`（≤10MB，admin）、`GET /api/admin/storage/objects/{sha256}`。

- [ ] **Step 1: 写 localfs 测试**

```go
func TestPutOpenDedupeAndCorrupt(t *testing.T) {
    s := localfs.New(t.TempDir())
    obj, err := s.Put(ctx, strings.NewReader("hello"), "text/plain")
    if err != nil { t.Fatal(err) }
    obj2, err := s.Put(ctx, strings.NewReader("hello"), "text/plain")
    if err != nil || obj2.SHA256 != obj.SHA256 { t.Fatalf("dedupe failed") }
    rc, _, err := s.Open(ctx, obj.SHA256)
    if err != nil { t.Fatal(err) }
    b, _ := io.ReadAll(rc); rc.Close()
    if string(b) != "hello" { t.Fatalf("got=%q", b) }
    // 篡改文件后 Open 返回 storage_corrupted
}
```

- [ ] **Step 2: 运行失败**

Run: `cd backend && go test ./internal/storage/... -run TestPutOpenDedupe -v`
Expected: FAIL

- [ ] **Step 3: 实现 localfs/service**

- 路径 `ab/cd/<sha256>`；temp + rename；读取校验哈希；10MB 限制在 handler；`storage_objects` 表；`order.txt` 追加。
- Handler：admin；`Content-Type` 必填；返回 `{data:{sha256,size,content_type}}`；GET 流式；创建写审计 `storage.object_created`。

- [ ] **Step 4: 写 handler 测试 + 集成测试**

验证 admin/普通用户权限、大小限制、哈希不匹配错误 `storage_corrupted`。

- [ ] **Step 5: 验证**

Run:
`cd backend && go test ./internal/storage/... && go test -tags=integration ./internal/storage/... -v`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add backend
git commit -m "feat: add content-addressed local object storage"
```

---

## Task 12: 前端壳、登录、API client

**Files:**
- Create: `frontend/package.json`
- Create: `frontend/next.config.ts`
- Create: `frontend/tsconfig.json`
- Create: `frontend/postcss.config.mjs`
- Create: `frontend/src/styles/globals.css`
- Create: `frontend/src/app/layout.tsx`
- Create: `frontend/src/app/login/page.tsx`
- Create: `frontend/src/app/(app)/layout.tsx`
- Create: `frontend/src/app/(app)/page.tsx`
- Create: `frontend/src/app/(app)/settings/page.tsx`（先占位，Task 13 填充）
- Create: `frontend/src/app/(app)/{versions,issues,reports,code-repos,resources,agents}/page.tsx`
- Create: `frontend/src/lib/api-client.ts`
- Create: `frontend/src/lib/auth-storage.ts`
- Create: `frontend/src/features/auth/{AuthProvider.tsx,auth-api.ts}`
- Create: `frontend/src/features/shell/{AppShell.tsx,Sidebar.tsx,Topbar.tsx,StatusBadge.tsx,EmptyState.tsx}`
- Test: `frontend/src/lib/api-client.test.ts`
- Create: `frontend/vitest.config.ts`

**Interfaces:**
- `apiFetch<T>(path: string, init?: RequestInit & { token?: string | null }): Promise<T>`；`ApiError{status,code,requestId,retryable,details}`。
- `getToken/setToken/clearToken`（sessionStorage key `eztest.token`）。
- `AuthProvider/useAuth`：`login(username,password)`、`logout()`、`user`、`loading`。
- Routes per spec §10.2.

- [ ] **Step 1: 写 api-client 失败测试**

```ts
it('wraps error envelope and attaches bearer', async () => {
  const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({error:{code:'invalid_argument',message:'bad',request_id:'req_1',retryable:false}}), {status:400, headers:{'content-type':'application/json'}}))
  vi.stubGlobal('fetch', fetchMock)
  await expect(apiFetch('/api/users', {token:'ezt_x'})).rejects.toMatchObject({status:400, code:'invalid_argument'})
  expect(fetchMock.mock.calls[0][1].headers.Authorization).toBe('Bearer ezt_x')
})
```

- [ ] **Step 2: 运行失败**

Run: `cd frontend && pnpm test -- api-client`
Expected: FAIL（项目/文件不存在）

- [ ] **Step 3: 建立前端工程与 api-client**

- `package.json` scripts：`dev` = `next dev -p 6868`、`build`、`start`、`lint` = `biome check .`、`format`、`typecheck` = `tsc --noEmit`、`test` = `vitest run`、`e2e` = `playwright test`。
- 依赖：next 16、react 19、react-dom 19、tailwindcss、@tailwindcss/postcss、next-themes、lucide-react、clsx、tailwind-merge、radix 需要的包；dev：typescript、@types/*、vitest、jsdom、@testing-library/react、@testing-library/jest-dom、@biomejs/biome、@playwright/test。
- `apiFetch` 统一 `NEXT_PUBLIC_API_BASE_URL`；错误解析；401 时 `clearToken`。
- 单测通过。

- [ ] **Step 4: 实现登录/AuthProvider/壳/占位页**

- 登录表单：用户名/密码；提交 → 存 token/user；跳 `/`。
- `(app)/layout.tsx` 客户端保护：无 token → `/login`。
- 侧边栏 9 项；P0 仅 `/`、`/settings` 可点，其余占位页显示“P1/P2/P3 规划中”。
- 顶栏：环境徽章、用户菜单（角色/登出）、主题。
- 状态徽章/空状态组件。
- `/settings` 先放 Tab 骨架。

- [ ] **Step 5: 验证**

Run:
`cd frontend && pnpm lint && pnpm typecheck && pnpm test && pnpm build`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add frontend
git commit -m "feat: scaffold Next.js shell, login, and API client"
```

---

## Task 13: SSE 客户端与设置页（用户/LLM/SSE）

**Files:**
- Create: `frontend/src/lib/sse-client.ts`
- Test: `frontend/src/lib/sse-client.test.ts`
- Create: `frontend/src/features/users/UsersPanel.tsx`
- Create: `frontend/src/features/llm-settings/LLMPanel.tsx`
- Create: `frontend/src/features/sse-test/SSETestPanel.tsx`
- Create: `frontend/src/features/settings/SettingsTabs.tsx`
- Modify: `frontend/src/app/(app)/settings/page.tsx`
- Test: `frontend/src/features/sse-test/SSETestPanel.test.tsx`（可选）

**Interfaces:**
- `connectSSE(opts: {url:string; token:string; lastEventId?:number; onEvent:(e:SSEEvent)=>void; onStatus:(s:SSEStatus)=>void; onReset?:(d:unknown)=>void}): () => void`。
- `SSEEvent{id?:string; event:string; data:string}`；`SSEStatus='connecting'|'live'|'reconnecting'|'closed'`。
- Users panel：列表/创建/禁用/重置密码（admin）；不展示密码（按规格安全版）。
- LLM panel：配置列表/编辑、模型上下文、刷新目录、测试（含图片/流式）；Key 留空=保留。
- SSE panel：连接/断开、状态、最后 seq、发送 3 条测试事件、事件列表。

- [ ] **Step 1: 写 SSE client 测试**

```ts
it('parses events, dedupes seq, reconnects with Last-Event-ID', async () => {
  // mock fetch returning ReadableStream with id:1/id:2 then closing
  // assert onEvent called twice, lastEventId=2, status live then reconnecting
})
it('handles stream.reset without id', async () => { /* onReset called; lastEventId unchanged */ })
```

- [ ] **Step 2: 运行失败**

Run: `cd frontend && pnpm test -- sse-client`
Expected: FAIL

- [ ] **Step 3: 实现 sse-client**

- fetch + `ReadableStream` + `TextDecoder`；按行解析 `id:`/`event:`/`data:`；空行 dispatch；忽略 `:` 注释。
- 45s 无字节重连；退避 1s→30s + jitter；带 `Last-Event-ID`；401 停止并清 token。
- `stream.reset` 回调，不更新 lastSeq。

- [ ] **Step 4: 实现设置页**

- Tabs：用户（admin）、LLM（admin）、SSE 测试（admin）；普通用户只显示账户信息+主题。
- API 调用走 `apiFetch`；表单校验；错误展示。
- LLM 测试：图片切换、流式切换；流式用 fetch 读归一化 SSE。
- SSE 测试卡：连接 `/api/stream?channel=system`，发送测试事件，显示状态/seq。
- 占位页/空状态统一。

- [ ] **Step 5: 验证**

Run:
`cd frontend && pnpm lint && pnpm typecheck && pnpm test && pnpm build`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add frontend
git commit -m "feat: add SSE client and admin settings panels"
```

---

## Task 14: E2E 与验收脚本

**Files:**
- Create: `frontend/playwright.config.ts`
- Create: `frontend/e2e/auth.spec.ts`
- Create: `frontend/e2e/users.spec.ts`
- Create: `frontend/e2e/sse.spec.ts`
- Create: `frontend/e2e/browser-smoke.spec.ts`
- Create: `scripts/e2e.ps1`
- Create: `scripts/acceptance-llm.ps1`
- Create: `scripts/acceptance.ps1`

**Interfaces:**
- E2E 使用独立库 `eztest_e2e`；`scripts/e2e.ps1` 初始化/重置库、启动 API（2333）/Web（6868）、运行 Playwright、清理进程。
- Playwright 使用本机 Chrome/Edge channel；`baseURL=http://localhost:6868`。
- 真实 LLM 验收 gated：`EZTEST_RUN_REAL_LLM=1`；否则跳过并输出“SKIPPED”。

- [ ] **Step 1: 写 E2E 测试**

```ts
test('admin creates user and user is read-only', async ({ page }) => {
  await login(page, 'admin', process.env.EZTEST_E2E_ADMIN_PASSWORD!)
  await page.goto('/settings')
  await page.getByRole('tab', { name: '用户' }).click()
  await page.getByLabel('用户名').fill('qa_e2e')
  await page.getByLabel('密码').fill('qa-e2e-pass-123')
  await page.getByRole('button', { name: '创建用户' }).click()
  await expect(page.getByText('qa_e2e')).toBeVisible()
  await logout(page); await login(page, 'qa_e2e', 'qa-e2e-pass-123')
  await page.goto('/settings')
  await expect(page.getByText('无权限')).toBeVisible()
})
test('SSE reconnect replays events without gaps', async ({ page, context }) => {
  await login(page, 'admin', process.env.EZTEST_E2E_ADMIN_PASSWORD!)
  await page.goto('/settings'); await page.getByRole('tab', {name:'SSE 测试'}).click()
  await page.getByRole('button', {name:'连接'}).click()
  await page.getByRole('button', {name:'发送测试事件'}).click()
  await context.setOffline(true); await page.waitForTimeout(1000); await context.setOffline(false)
  await expect(page.getByTestId('sse-status')).toHaveText('实时')
  // 断言 seq 连续且无重复
})
test('browser environment produces a screenshot', async ({ page }) => {
  await page.setContent('<h1>EzTest browser spike</h1>')
  const png = await page.screenshot()
  expect(png.length).toBeGreaterThan(0)
})
```

- [ ] **Step 2: 运行失败**

Run: `pwsh scripts/e2e.ps1`
Expected: FAIL（脚本/spec 不存在）

- [ ] **Step 3: 实现 Playwright 配置与脚本**

- `webServer` 或脚本启动；`channel: 'chrome'`；browser-smoke 验证打开页面/截图可用（只验证不集成）。
- e2e 脚本设置 `EZTEST_E2E_ADMIN_PASSWORD`；重置 `eztest_e2e`；启动 API/Web。
- SSE 断线：先 `context.setOffline(true)`；若 fetch 未断流，改用 `page.route('**/api/stream*', r => r.abort())` 后 unroute，确保触发重连。
- 测试用户唯一；测试结束清理进程。

- [ ] **Step 4: 实现验收脚本**

- `acceptance.ps1`：`go test ./...`、集成测试、`pnpm lint/typecheck/test/build`、`pwsh scripts/e2e.ps1`、迁移重复执行、health/audit 断言。
- `acceptance-llm.ps1`：登录 admin → 找默认 LLM 配置 → 调 `/test`（Chat 含图；Responses 文本）→ 断言非空；gated。
- 输出 DoD 对照表。

- [ ] **Step 5: 验证**

Run:
`pwsh scripts/e2e.ps1`
`pwsh scripts/acceptance.ps1`
Expected: PASS（无 Docker 的 Compose 项标注 SKIPPED）

- [ ] **Step 6: 提交**

```bash
git add frontend/e2e frontend/playwright.config.ts scripts
git commit -m "test: add Playwright E2E and P0 acceptance scripts"
```

---

## Task 15: 部署文件、OpenAPI、文档、最终验证

**Files:**
- Create: `deploy/Dockerfile.api`
- Create: `deploy/Dockerfile.web`
- Create: `deploy/docker-compose.yml`
- Create: `deploy/README.md`
- Create: `docs/openapi/p0.yaml`
- Create: `.env.example`
- Create: `README.md`
- Create: `scripts/db.ps1`（init/start/stop/status）
- Create: `scripts/dev-api.ps1`
- Create: `scripts/dev-web.ps1`
- Create: `scripts/test.ps1`
- Create: `frontend/scripts/validate-compose.mjs`
- Modify: `frontend/package.json`（加 `yaml` dev dep，用于静态校验）
- Modify: `backend/internal/config/config.go`（`OpenAPIPath`，默认 `docs/openapi/p0.yaml`）
- Modify: `backend/internal/server/server.go`（`GET /api/openapi.yaml`）
- Test: `backend/internal/server/openapi_test.go`
- Test: `frontend/scripts/validate-compose.mjs`（运行即校验）

**Interfaces:**
- Compose：`api`（2333）、`web`（6868）、`postgres:17`（5433）；健康检查；卷 `pgdata`/`objects`；`EZTEST_DATABASE_URL`/`EZTEST_WEB_ORIGIN`。
- Dockerfile.api：Go 多阶段构建，非 root；镜像内复制 `docs/openapi/p0.yaml` 到工作目录同名路径。
- Dockerfile.web：Next standalone 构建。
- OpenAPI 覆盖规格 §9.2 全部端点；`GET /api/openapi.yaml` 读取 `cfg.OpenAPIPath`（默认 `docs/openapi/p0.yaml`），返回 `application/yaml`；缺失 404。

- [ ] **Step 1: 写 Compose 静态校验脚本**

`frontend/scripts/validate-compose.mjs` 使用 `yaml` 解析 `../deploy/docker-compose.yml`，断言 services `api/web/postgres`、端口、healthcheck、卷、depends_on；失败退出 1。

- [ ] **Step 2: 运行失败**

Run: `cd frontend && node scripts/validate-compose.mjs`
Expected: FAIL（文件不存在）

- [ ] **Step 3: 实现部署文件/OpenAPI/README**

- Dockerfile 多阶段；Compose 文件；不实际运行容器；Dockerfile.api 复制 `docs/openapi/p0.yaml` 到工作目录。
- OpenAPI 覆盖 auth/users/llm/stream/storage/audit/health；server 注册 `GET /api/openapi.yaml`（认证中间件之外，公开）读取 `cfg.OpenAPIPath`；测试用临时文件验证 200/404。
- README：架构、目录、环境变量、本地启动、测试、E2E、验收、Compose 未运行说明、开发密码位置（`.env`/local doc）。
- 本地脚本：`db.ps1` 管理便携 PG；`dev-api.ps1` 加载 `.env`+迁移+`go run`；`dev-web.ps1` 启 Next 6868；`test.ps1` 跑 Go/集成/前端检查。
- `.env.example` 不含真实秘密；注释说明 `EZTEST_SECRET_KEYS` 生成方式。

- [ ] **Step 4: 验证**

Run:
`cd frontend && node scripts/validate-compose.mjs`
`pwsh scripts/acceptance.ps1`
`git status --short`
Expected: PASS；无未提交秘密/构建产物。

- [ ] **Step 5: 提交**

```bash
git add deploy docs/openapi README.md .env.example scripts frontend/scripts frontend/package.json backend/internal/server backend/internal/config
git commit -m "docs: add deployment files, OpenAPI, and runbooks"
```

---

## 计划自审记录（写计划时完成）

- Spec §3 目录/DDD → Task 1–15 文件路径；§5 DDL → Task 2–11 迁移；§6 认证 → Task 3–4/7；§7 SSE → Task 8；§8 LLM → Task 9–10；§9 API → Task 6–7；§10 前端 → Task 12–13；§11 存储 → Task 11；§12 测试 → Task 14；§14 交付 → Task 15。
- Review Focus 五项分别落在：SSE → Task 8；LLM → Task 10；幂等 → Task 6；迁移 → Task 2；认证 → Task 4/7。
- 类型/名称一致性：`identity.User`、`eventing.Event`、`llm.Config`、`storage.Object` 在跨任务引用中保持同名。
- 未覆盖项：无。Compose 运行验证按规格明确不做，只做静态校验。
