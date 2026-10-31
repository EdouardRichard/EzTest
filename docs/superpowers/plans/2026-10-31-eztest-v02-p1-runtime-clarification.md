# EzTest 0.2 P1 控制面 + 澄清 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans (Lead-native) or superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax. Parallel groups are marked; shared-file writes are Lead-only.

**Goal:** 建立共享任务板/租约/reconciler、Agent runtime/上下文/团队频道，打通“新增对话 → AI 澄清 → 需求版本 → 用户确认/回退”的垂直闭环，并冻结 P1 契约、产出测试与验收证据。

**Architecture:** 在 P0 模块化单体上新增 `agent` 上下文（任务板/团队频道/runtime/工具/worker）并扩展 `version` 上下文（需求/对话/附件）；新增 Claude/OpenAI-compatible chat 网关与确定性 fake LLM；前端新增 `clarification` feature，替换 requirements 占位 Tab。阶段内不实现 P2+ 的设计/用例/执行。

**Tech Stack:** Go 1.24.6 + PostgreSQL 17 + pgx v5 + net/http；`rsc.io/pdf v0.1.1`（PDF 文本）；Next.js 16 + React 19 + Tailwind 4 + Vitest + Playwright；Node fake LLM。

**Spec:** `docs/superpowers/specs/2026-10-31-eztest-v02-p1-runtime-clarification-design.md`（必须与权威设计一起阅读）

## Global Constraints

- 权威：`docs/version-V0.2/architecture-design.md`、`high-level-design.md`、`research/decision-log.md`、`research/open-decisions.md`、`phases/phase-01-runtime-clarification.md`、P0 frozen `contracts/`、`phase-00-handoff.md`。冲突按 spec §12.1 裁决；不改设计文档。
- 阶段按 **P1 = phase-01** 实施（提示词误用了 P0 模板；权威阶段表为准）。
- `.env` 严格 4 项；P1 不新增 env；不新增设置键（消费 P0 的 context/agent/team_room/llm 键）。真实 LLM 验收只由 `scripts/p1-acceptance-llm.ps1` 读取 `.env` 的小写键 `llm_api_key/llm_base_url/llm_model_name`（`deepseek-flash`），再经 admin API 写 `llm_config` 并 refresh；API 进程不读这些键。上下文窗口/输出优先从网关模型目录读取，缺失才回退设置。
- 前端新增 `react-markdown`/`remark-gfm` 由 Lead 在 T14 开始前执行 `pnpm add` 并提交 `package.json`/lockfile；其他代理不得改 `package.json`。
- 所有新业务 ID 雪花 int64；JSON/URL 一律十进制字符串；事件 `seq` 是数字游标。
- 迁移只追加；不改 P0 已应用迁移；迁移名 `<context>_<NNNN>_<desc>.sql`；`order.txt` 追加到末尾。P1 迁移：`agent_0001_work_item.sql`、`agent_0002_team_room.sql`、`agent_0003_runtime.sql`、`version_0002_requirement.sql`、`version_0003_conversation.sql`、`version_0004_requirement_attachment.sql`、`eventing_0002_event_index.sql`。
- 不恢复 0.1 控制面：无 8 状态版本机、无整版冻结/评审页签、无人工批次、无中心编排 Agent。
- 后端四层 + 导入方向遵守 `Agent.md §6.2`；`cmd/eztest` 唯一组装根；后台 worker/reconciler 由 main 启停，单实例假设写入包注释。
- 共享契约（`docs/version-V0.2/contracts/**`）、`go.mod`、`cmd/eztest`、`server.go`、`platform/**` 由 Lead 统一收口；并行代理不并发写这些文件。
- 并行代理**不得**运行 `git add/commit`（Lead 串行提交）；不得运行 `go mod tidy`/`pnpm install`；不得改 `go.mod`/`package.json`（T1 的 `rsc.io/pdf` 由 Lead 显式添加）。
- 每个任务先写失败测试、跑失败、再写最小实现、跑相关测试与 `scripts/test.ps1` 相关层；任务结束由 Lead 提交。
- Fake LLM 必须确定性且保持 P0 行为兼容；0.1 `scripts/e2e.ps1`/`acceptance.ps1`/`fake-llm.p*.test.cjs` 禁止运行/沿用。
- 不读取 `docs/用户专用文件，禁止AI阅读.md`；不提交 `.env`、`.data/`、`.tmp/`。

## Review Focus

1. **契约漂移**：P1 表/API/事件/工具必须与 spec §3/§8/§9 一致；T2 先写契约增量，T20 冻结前逐项比对 handler/DTO/迁移；禁止“代码先行、契约后补”。
2. **作用域注入/ID 混用**：Agent 工具 schema 不得出现 `version_id`；runtime 注入并校验 version/requirement/conversation/version_sut；跨版本 requirement/message 必须 404；页面/API 必须属于绑定 SUT+环境。
3. **压缩丢事实**：长会话压缩后 pinned facts、未决问题、任务约束、最近窗口必须保留；`context_overflow` 不得判失败终态。
4. **任务板幂等/租约**：并发领取不重复；重复扫描不重复建任务；租约过期恢复；旧 `lease_epoch` 写回 409；崩溃不批量中断 running。
5. **旧假设回流**：禁止 AI 替用户确认/删除/关闭；禁止整版冻结/8 状态/手工批次；确认/回退必须写审计与事件；P2+ 只留端口/事件占位。

---

## 并行执行图

```text
Lead（顺序）: T1 迁移 → T2 契约增量（gate）
并行 A: T3 agent domain | T5 team room | T6 context builder | T7 LLM gateway+fake | T10 requirement | T14 前端 API/壳
并行 B: T4 work queue（依赖 T3） | T8 tools/run loop（依赖 T6/T7） | T11 conversation（依赖 T10） | T15 chat UI（依赖 T14）
并行 C: T9 worker/reconciler（依赖 T4/T8） | T12 attachments（依赖 T11） | T16 sidebar/editor（依赖 T15）
Lead: T13 server/main/integration（依赖 T9/T12） → T18 E2E/验收（依赖 T13/T17）
前端：T17 page 集成（依赖 T16） → T18
验收: T18 → T19 独立评审 → T20 全量 gate/契约冻结/handoff
```

**写作用域**
- backend-runtime：`backend/internal/agent/**`（domain/workqueue/teamroom/context，T3–T6）。
- backend-agent：`backend/internal/llm/**`（仅 chat 增量）、`backend/internal/agent/application/{runner.go,profiles.go,summary.go,ports_agent.go,worker.go,reconciler.go}`、`backend/internal/agent/application/tools/**`、`scripts/fake-llm.cjs`、`scripts/fake-llm-p1.test.cjs`（T7–T9）。
- backend-domain：`backend/internal/version/**`（新增文件 + 必要的 counts/status 增量）。
- frontend：`frontend/src/features/clarification/**`、`frontend/src/lib/api-client.ts`、`frontend/src/features/versions/VersionDetailPage.tsx`、`VersionDetailPage.test.tsx`。
- Lead：`backend/internal/db/migrations/**`、`backend/internal/server/**`、`backend/cmd/eztest/**`、`backend/internal/platform/**`、`backend/internal/testutil/**`、`docs/version-V0.2/contracts/**`、`scripts/p1-*.ps1`、`docs/version-V0.2/phases/phase-01-handoff.md`。
- 迁移与共享契约只由 Lead 写；子代理发现需要改契约时停止并报告。

---

## Task 1: P1 迁移、依赖与迁移幂等（Lead）

**Files:**
- Create: `backend/internal/db/migrations/agent_0001_work_item.sql`
- Create: `backend/internal/db/migrations/agent_0002_team_room.sql`
- Create: `backend/internal/db/migrations/agent_0003_runtime.sql`
- Create: `backend/internal/db/migrations/version_0002_requirement.sql`
- Create: `backend/internal/db/migrations/version_0003_conversation.sql`
- Create: `backend/internal/db/migrations/version_0004_requirement_attachment.sql`
- Create: `backend/internal/db/migrations/eventing_0002_event_index.sql`
- Modify: `backend/internal/db/migrations/order.txt`
- Modify: `backend/go.mod`、`backend/go.sum`（添加 `rsc.io/pdf v0.1.1`，Lead 执行 `go get rsc.io/pdf@v0.1.1`）
- Modify: `backend/internal/db/migrate_integration_test.go`（更新 P0 的迁移数量/表清单断言；保留原测试意图并新增 P1 表断言）

**Interfaces:**
- 表结构与 spec §3 完全一致；所有新表有 PK、必要 CHECK、UNIQUE 与索引。
- 迁移必须能在 `testutil.NewDB(t)`（随机 schema）重复执行两次且 `schema_migrations` 不变。

- [ ] **Step 1: 创建分支**：`git switch -c feat/v0.2-p1`（从 `feat/v0.2-p0` HEAD）。
- [ ] **Step 2: 写迁移 SQL**（7 个文件，严格按 spec §3 列/约束/索引；跨上下文不建 FK；同上下文 FK 只用于 requirement_version→requirement、conversation_message→conversation 等）。
- [ ] **Step 3: 追加 order.txt**（7 行，末尾；不改已有行）。
- [ ] **Step 4: 写/更新失败测试**：`TestP1MigrationsApplyTwice`（空 schema 跑两次，第二次 applied 为空、表数量符合预期）、`TestP1RequiredConstraints`（重复 dedupe_key/requirement revision/confirmation 的 requirement_version_id 被拒）；更新 `migrate_integration_test.go` 的 P0 迁移数量/表清单期望（P1 后 15 个迁移、37 张业务表 + `schema_migrations` = 38 张 base table）。
- [ ] **Step 5: 跑测试**：`cd backend; .tools\..\.tools\go\bin\go.exe test -tags=integration ./internal/db/...`（用 `.tools\go\bin\go.exe`；Expected FAIL 先因迁移未写/表不存在）。
- [ ] **Step 6: 加 PDF 依赖**：`go get rsc.io/pdf@v0.1.1`；确认只新增该直接依赖，不 tidy 其他。
- [ ] **Step 7: 跑通并提交**：`git add backend/internal/db/migrations backend/go.mod backend/go.sum`；`git commit -m "feat(p1): add runtime and clarification migrations"`。

## Task 2: P1 契约增量（Lead）

**Files:**
- Modify: `docs/version-V0.2/contracts/db-schema.md`
- Modify: `docs/version-V0.2/contracts/migrations-order.md`
- Modify: `docs/version-V0.2/contracts/events.md`
- Modify: `docs/version-V0.2/contracts/openapi.yaml`
- Modify: `docs/version-V0.2/contracts/settings.md`
- Modify: `docs/version-V0.2/contracts/README.md`
- Create: `docs/version-V0.2/contracts/task-types.md`
- Create: `docs/version-V0.2/contracts/agent-profiles-tools.md`

**Interfaces:** 以 spec §3/§8/§9/§11 为准；P1 标记 `x-frozen: P1`（冻结在 T20，T2 先写 draft）。

- [ ] **Step 1: db-schema 追加 16 表 + 索引/约束 + 归属**（agent 8 表、version 8 表、eventing 索引；`requirement_attachment` 运行态字段注明 P1-D5；`version_sut.scope` 修正为 DB 数字/API 字符串）。
- [ ] **Step 2: migrations-order 追加 7 行 + ownership + P1 增量说明**。
- [ ] **Step 3: events 追加 P1 事件类型、scope 字段、payload 示例、单实例限制不变**。
- [ ] **Step 4: task-types.md**：P1 实现 `chat.turn/requirement.curate/attachment.ingest`；列 P2+ 保留类型但不实现；写 team/dedupe/输入/输出/失败语义。
- [ ] **Step 5: agent-profiles-tools.md**：3 个 profile、14 个 P1 工具、scope 注入、JSON schema、提交校验、错误返回；P2+ 工具标 reserved。
- [ ] **Step 6: openapi**：追加 11 个 P1 path（含只读 team-room）、23 个 schema、错误码；P0 LLM list/status 漂移按实现修正（非破坏性）；`info`/`x-frozen` 标 P1 draft。
- [ ] **Step 7: settings.md**：记录 P1 只消费 P0 键，不新增 env/键；列出 context/agent/team_room/llm 键。
- [ ] **Step 8: YAML 解析自检**：`node -e "const yaml=require('./frontend/node_modules/yaml'); yaml.parse(require('fs').readFileSync('docs/version-V0.2/contracts/openapi.yaml','utf8')); console.log('ok')"`。
- [ ] **Step 9: 提交**：`feat(p1): add frozen-ready P1 contracts`。

## Task 3: Agent domain 与 PostgreSQL 仓储（backend-runtime）

**Files:**
- Create: `backend/internal/agent/domain/work_item.go`、`work_item_test.go`
- Create: `backend/internal/agent/domain/team_room.go`、`team_room_test.go`
- Create: `backend/internal/agent/domain/runtime.go`（Profile/Run/Message/ContextPacket）、`runtime_test.go`
- Create: `backend/internal/agent/domain/repository.go`
- Create: `backend/internal/agent/infrastructure/postgres/workqueue.go`、`teamroom.go`、`runtime.go`
- Create: `backend/internal/agent/infrastructure/postgres/integration_test.go`
- Create: `backend/internal/agent/interfaces/httpapi/doc.go`（四层占位；无 P1 公开路由）
- Create: `backend/internal/agent/domain/doc.go`、`application/doc.go`、`infrastructure/doc.go`（包注释，单实例说明）

**Interfaces:**
```go
package agent

type WorkItemStatus string // open|claimed|running|waiting_human|done|failed|dead
type WorkItem struct {
    ID, VersionID int64
    Team, Type string
    Status WorkItemStatus
    Priority int
    DedupeKey string
    DependsOn *int64
    Payload json.RawMessage
    ClaimedBy *string
    LeaseUntil *time.Time
    LeaseEpoch int64
    Attempts, MaxAttempts int
    LastError *string
    AvailableAt, CreatedAt, UpdatedAt time.Time
}
type NewWorkItem struct { VersionID int64; Team, Type string; Priority int; DedupeKey string; Payload any; MaxAttempts int }
type WorkItemRepository interface {
    Ensure(ctx context.Context, in NewWorkItem) (WorkItem, bool, error)
    Claim(ctx context.Context, claimant string, lease time.Duration) (WorkItem, bool, error)
    Get(ctx context.Context, id int64) (WorkItem, error)
    MarkRunning(ctx context.Context, id, epoch int64) error
    Renew(ctx context.Context, id, epoch int64, lease time.Duration) error
    Complete(ctx context.Context, id, epoch int64) error
    Fail(ctx context.Context, id, epoch int64, msg string, retryable bool, backoff time.Duration) error
    RecoverExpired(ctx context.Context, lease time.Duration, maxAttempts int) (int64, error)
    ListByVersion(ctx context.Context, versionID int64, limit int) ([]WorkItem, error)
}
```
TeamRoom/Runtime repo interfaces：`EnsurePost/ListPosts/LatestDigest/SaveDigest/PruneDigests`、`EnsureProfile/GetProfile/ListProfiles/CreateRun/FinishRun/AppendMessage/ListRunMessages/SaveContextPacket/GetContextPacket`。

- [ ] **Step 1: 写 domain 失败测试**：状态合法性、`ValidateNewWorkItem`（version>0、type/team/dedupe 非空、max_attempts 1–10）、`Post.Validate`（kind 枚举）、`Profile.Valid`、`Run` 终态。
- [ ] **Step 2: 跑失败**：`go test ./internal/agent/domain/...`（Expected FAIL）。
- [ ] **Step 3: 实现 domain 类型**（无 DB/HTTP 依赖；只 import 标准库 + platform）。
- [ ] **Step 4: 写 postgres 集成失败测试**：Ensure 幂等、Claim 跳过已领、Renew 0 行 lease_lost、RecoverExpired 只回收过期、Profile seed 幂等、digest 多版本、context packet 读写。
- [ ] **Step 5: 实现 postgres 仓储**（SQL 手写；用 `pgxpool`；事务内 `SELECT ... FOR UPDATE SKIP LOCKED`；`lease_epoch` CAS）。
- [ ] **Step 6: 跑通**：`go test ./internal/agent/...` + `go test -tags=integration ./internal/agent/...`。
- [ ] **Step 7: Lead 提交**：`feat(p1): add agent domain and repositories`。

## Task 4: WorkQueue 服务（backend-runtime）

**Files:**
- Create: `backend/internal/agent/application/workqueue.go`、`workqueue_test.go`
- Create: `backend/internal/agent/application/ports.go`（Queue/Clock/Settings 端口；后续任务扩展）

**Interfaces:**
```go
type WorkQueue struct{ ... }
func NewWorkQueue(repo agent.WorkItemRepository, clk clock.Clock, lease time.Duration, maxAttempts int) *WorkQueue
func (q *WorkQueue) Ensure(ctx context.Context, in agent.NewWorkItem) (agent.WorkItem, bool, error)
func (q *WorkQueue) Claim(ctx context.Context, claimant string) (agent.WorkItem, bool, error)
func (q *WorkQueue) Heartbeat(ctx context.Context, id, epoch int64) error
func (q *WorkQueue) Complete(ctx context.Context, id, epoch int64) error
func (q *WorkQueue) Fail(ctx context.Context, id, epoch int64, cause error, retryable bool) error
func (q *WorkQueue) RecoverExpired(ctx context.Context) (int64, error)
func (q *WorkQueue) ListByVersion(ctx context.Context, versionID int64, limit int) ([]agent.WorkItem, error)
```
- `Fail` 退避 `min(2^attempts, 30)s`；达到 `max_attempts` 或 `!retryable` → dead；`lease_lost` 用 409 错误。
- 常量：lease 30s、heartbeat 10s、max_attempts 3（spec P1-D4）。

- [ ] **Step 1: 写失败测试**：Get→Claim 后 attempts/epoch/lease 正确；Complete 旧 epoch 返回 lease_lost；Fail 可重试→open+available_at 未来；不可重试→dead；RecoverExpired 重置过期且保留 attempts。
- [ ] **Step 2: 跑失败**：`go test ./internal/agent/application/ -run TestWorkQueue`。
- [ ] **Step 3: 实现 WorkQueue**（封装 repo；错误映射）。
- [ ] **Step 4: 写并发集成测试**：`TestWorkQueueConcurrentClaim`（20 个 worker 抢 50 个任务，每个 id 只被领一次）；`TestWorkQueueLeaseRecovery`（短租约→过期→Recover→新 worker 可领；旧 epoch 写回失败）。
- [ ] **Step 5: 跑通**：`go test ./internal/agent/...` + integration。
- [ ] **Step 6: Lead 提交**：`feat(p1): add lease-based work queue`。

## Task 5: 团队频道与摘要（backend-runtime）

**Files:**
- Create: `backend/internal/agent/application/teamroom.go`、`teamroom_test.go`
- Create: `backend/internal/agent/interfaces/httpapi/teamroom_handler.go`、`teamroom_handler_test.go`

**Interfaces:**
```go
type TeamRoom struct{ ... }
func NewTeamRoom(repo agent.TeamRoomRepository, clk clock.Clock, ids id.Generator, settings SettingsReader, events eventingapp.Publisher) *TeamRoom
func (s *TeamRoom) Post(ctx context.Context, p agent.TeamRoomPost) (agent.TeamRoomPost, error)
func (s *TeamRoom) Digest(ctx context.Context, versionID int64, team string) (agent.TeamRoomDigest, error)
func (s *TeamRoom) Context(ctx context.Context, versionID int64, team string, maxPosts int) (TeamRoomContext, error)
func (s *TeamRoom) Read(ctx context.Context, versionID int64, team string, limit int) (TeamRoomView, error)
```
- 只读 HTTP：`GET /api/versions/{id}/team-rooms/{team}?limit=`，登录用户；返回最新 digest + 最近帖子（id/kind/content/refs/agent_run_id/created_at）；team 枚举校验；无 digest 返回 null。
- 摘要阈值读 `team_room.digest_every_n_posts`/`digest_interval_minutes`/`digest_keep_versions`；确定性摘要：标题/kind/首 200 字，decision/blocker 进 pinned；保留最近 N 版并删除更旧。
- 发布 `team_room.post`/`team_room.digest_updated` 事件。

- [ ] **Step 1: 写失败测试**：不足 20 条且未超时不生成；第 20 条生成；超时生成；摘要含 pinned decision/blocker；N 版后旧版被删；Context 只返回最新 digest+相关帖子；Read 只读/limit/team 校验。
- [ ] **Step 2: 跑失败**、**Step 3: 实现**、**Step 4: 跑通** `go test ./internal/agent/application/ ./internal/agent/interfaces/httpapi/ -run TestTeamRoom`。
- [ ] **Step 5: Lead 提交** `feat(p1): add team room and digest`。

## Task 6: 上下文包、token 预算与压缩（backend-runtime）

**Files:**
- Create: `backend/internal/agent/application/context.go`、`context_test.go`
- Create: `backend/internal/agent/application/tokens.go`、`tokens_test.go`

**Interfaces:**
```go
type Section struct { Name string; Content string; Refs []string; Optional bool }
type ContextPacket struct { VersionID int64; Sections []Section; TokenCounts map[string]int; Hash string }
type ContextInput struct { Profile agent.Profile; Task TaskContext; VersionPack VersionPack; Conversation ConversationContext; TeamRoom TeamRoomContext; Artifacts []ArtifactRef; ModelBudget ModelBudget }
type ModelBudget struct { ContextWindow int; MaxOutput int }
type ContextBuilder struct{ ... }
func NewContextBuilder(settings SettingsReader, summarizer Summarizer) *ContextBuilder
func (b *ContextBuilder) Build(ctx context.Context, in ContextInput) (ContextPacket, error)
func EstimateTokens(s string) int // runes/2 + 16
func Compact(input ContextInput, budget int) (ContextInput, CompactInfo)
type Summarizer interface { Summarize(ctx context.Context, messages []Message) (string, error) }
```
- 预算 = `ModelBudget.ContextWindow`（缺失回退 `context.window_tokens`）- 输出预留（不超过 `ModelBudget.MaxOutput`，缺失回退 `context.output_reserve_tokens`）- 2048；阈值 `context.compact_threshold`；保留 pinned + 最近 `context.keep_recent_messages`；LLM 摘要失败→确定性裁剪；最终仍超→最小段。
- `context.compacted` 事件由 runtime 发，不在纯 builder 里；builder 返回 `Compacted bool`/`Summary` 供上层持久化。

- [ ] **Step 1: 写失败测试**：预算计算；小上下文不压缩；超阈值压缩后 pinned/最近窗口/任务约束保留；Summarizer 失败回退；极端超限仍输出 ≤budget；token 估算中文保守；hash 稳定。
- [ ] **Step 2-4: TDD 实现并跑通** `go test ./internal/agent/application/ -run 'TestContext|TestCompact|TestEstimate'`。
- [ ] **Step 5: Lead 提交** `feat(p1): add layered context builder`。

## Task 7: LLM chat 网关与确定性 fake LLM（backend-agent）

**Files:**
- Create: `backend/internal/llm/application/chat.go`、`chat_test.go`
- Create: `backend/internal/llm/infrastructure/openai_chat.go`、`openai_chat_test.go`
- Modify: `backend/internal/llm/application/ports.go`（新增 ChatClient 接口，不改现有 Client 接口）
- Modify: `backend/internal/llm/application/service.go`（新增 `Chat` 方法，复用 repo/client；不改变 P0 方法）
- Modify: `backend/internal/llm/infrastructure/openai.go`（实现 ChatClient；不改变 ListModels/Test）
- Modify: `scripts/fake-llm.cjs`（P1 agent 模式，保持 P0 兼容）
- Create: `scripts/fake-llm-p1.test.cjs`（node:test）

**Interfaces:**
```go
package llmapp
type ToolCall struct { ID, Name, Arguments string }
type ChatMessage struct { Role, Content string; ToolCalls []ToolCall; ToolCallID string }
type ToolDefinition struct { Name, Description string; Parameters json.RawMessage }
type ChatRequest struct { Model string; Messages []ChatMessage; Tools []ToolDefinition; MaxOutputTokens int }
type ChatResponse struct { Content string; ToolCalls []ToolCall; Usage map[string]int; FinishReason string }
type ChatGateway interface {
    Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
    ResolveModel(ctx context.Context) (ModelInfo, error)
}
type ModelInfo struct { ConfigID int64; Model string; ContextWindow int; MaxOutput int; SupportsTools bool }
func (s *Service) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
func (s *Service) ResolveModel(ctx context.Context) (ModelInfo, error)
```
- 默认 config/model 解析；超时/重试来自 config；错误脱敏；`context_length_exceeded` 映射 `context_overflow` 可识别错误。
- Fake 模式：system 含 `[profile:clarification_host]`→文本；`[profile:requirement_curator]`→第一次 `requirement.draft_create` tool_call，收到 tool 结果后最终文本；`[profile:context_scout]`→摘要文本；`[fake:context_overflow]`→首次 400 context_length_exceeded；无标记保持 P0 echo。

- [ ] **Step 1: 写 Go 失败测试**：默认 config/model 选择与 `ResolveModel` 元数据；模型元数据缺失回退；无 config→ai_unavailable；tool_calls 解析；timeout/retry；key 不出现在错误；context overflow 可识别。
- [ ] **Step 2: 跑失败**、**Step 3: 实现网关**、**Step 4: 跑通** `go test ./internal/llm/...`（含 P0 既有测试）。
- [ ] **Step 5: 写 fake LLM 测试**：node:test 启动脚本，断言 P0 echo 不变、Host 文本、Curator tool_call→最终文本、Scout 摘要、overflow 首次 400。
- [ ] **Step 6: 跑通** `node --test scripts/fake-llm-p1.test.cjs`。
- [ ] **Step 7: Lead 提交** `feat(p1): add chat gateway and deterministic fake agent`。

## Task 8: 工具注册表、run loop 与 profile 种子（backend-agent）

**Files:**
- Create: `backend/internal/agent/application/profiles.go`、`profiles_test.go`
- Create: `backend/internal/agent/application/tools/registry.go`、`registry_test.go`
- Create: `backend/internal/agent/application/tools/requirements.go`、`attachments.go`、`context.go`、`resources.go` + tests
- Create: `backend/internal/agent/application/runner.go`、`runner_test.go`
- Create: `backend/internal/agent/application/summary.go`、`summary_test.go`
- Create: `backend/internal/agent/application/ports.go`（扩展 RequirementWriter/ConversationPort/VersionReader/AttachmentReader/Audit/Events/Storage）

**Interfaces:**
```go
type Scope struct { VersionID, ConversationID, RequirementID, AgentRunID int64; VersionSUTIDs []int64 }
type Tool interface { Name() string; Description() string; Parameters() json.RawMessage; Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) }
type Registry struct{ ... }
func (r *Registry) Definitions(names []string) []llmapp.ToolDefinition
func (r *Registry) Execute(ctx context.Context, scope Scope, name string, raw json.RawMessage) (any, error)
type Runner struct{ ... }
func NewRunner(deps RunnerDeps) *Runner
func (r *Runner) Run(ctx context.Context, item agent.WorkItem) (RunResult, error)
```
- Tools 的 `Parameters()` 是 JSON Schema，**不得**含 `version_id/versionId`；runtime 把 scope 传给 Execute。
- 工具清单按 spec §5.5；`context.summary_update` 写 pinned/summary；`requirement.reopen_propose` 只写 `rollback_proposal` 消息 part。
- Runner：创建 agent_run、context packet、循环调 `ChatGateway`、执行 tool_calls、写 agent_message/tool message、Host 最终消息写 conversation、Curator 卡片、Scout 团队帖；每 run 先 `ResolveModel` 取 ContextWindow/MaxOutput 填入 `ContextInput.ModelBudget`；max turns/soft/hard timeout；context overflow 重试；tool result 超 32KiB 落盘；lease heartbeat 由 worker 负责。
- `profiles.go` 种子常量 + `EnsureProfiles`（key/name/system_prompt/allowed_tools/model/budgets）。

- [ ] **Step 1: 写 profile 失败测试**（3 个 key、工具集合、prompt 含 `[profile:...]`；seed 幂等）。
- [ ] **Step 2: 写 tool registry 失败测试**（schema 无 versionId；未知工具报错；scope 注入；requirement 工具用 scope version 而非参数；跨版本拒绝）。
- [ ] **Step 3: 写 runner 失败测试**（fake gateway/fake ports）：无 tool 结束；tool_call→结果→最终消息；max turns；soft timeout；overflow 压缩重试；tool result 落盘；Host/Curator/Scout 各自输出；旧 epoch 写回失败。
- [ ] **Step 4: TDD 实现并跑通** `go test ./internal/agent/application/...`（可用 fake ports，不依赖 DB）。
- [ ] **Step 5: Lead 提交** `feat(p1): add scoped tools and agent run loop`。

## Task 9: Worker 与 P1 Reconciler（backend-agent）

**Files:**
- Create: `backend/internal/agent/application/worker.go`、`worker_test.go`
- Create: `backend/internal/agent/application/reconciler.go`、`reconciler_test.go`
- Create: `backend/internal/agent/application/ports.go`（补 Reconciler 端口）

**Interfaces:**
```go
type Worker struct{ ... }
func NewWorker(queue *WorkQueue, runner Runner, settings SettingsReader, logger *slog.Logger, instanceID string) *Worker
func (w *Worker) Run(ctx context.Context) error
type Reconciler struct{ ... }
func NewReconciler(deps ReconcilerDeps) *Reconciler
func (r *Reconciler) Reconcile(ctx context.Context) error
```
- Worker：每 300ms claim；并发 ≤ `agent.max_concurrent_runs`；心跳 10s；按 type→profile；成功 Complete，失败 Fail；ctx 取消时不再 claim、等待在飞 run（有上限）。
- Reconciler P1 规则见 spec §4.3：版本投影、opening、用户消息回复、curate、附件 ingest；所有 ensure 用 dedupe key；重复执行不新增。
- Reconciler 端口：`VersionProjector`、`ConversationReader`、`AttachmentReader`、`WorkQueueEnsurer`、`Clock`。

- [ ] **Step 1: 写 worker 失败测试**（fake queue/runner）：领取→running→done；runner 错误→Fail；并发上限；取消停止；心跳失败→lease_lost 不覆盖。
- [ ] **Step 2: 写 reconciler 失败测试**（fake ports）：initial→running 一次；opening 只建一次；每条用户消息只 ensure 一次 reply/curate；已回复/已 curate 不重复；pending 附件 only once；第二次 Reconcile 无新增。
- [ ] **Step 3: TDD 实现**、**Step 4: 跑通** `go test ./internal/agent/application/ -run 'TestWorker|TestReconciler'`。
- [ ] **Step 5: Lead 提交** `feat(p1): add agent worker and reconciler`。

## Task 10: 需求域、确认/回退、版本投影与统计（backend-domain）

**Files:**
- Create: `backend/internal/version/domain/requirement.go`、`requirement_test.go`
- Create: `backend/internal/version/application/requirement.go`、`requirement_test.go`
- Create: `backend/internal/version/application/requirement_views.go`、`requirement_ports.go`
- Create: `backend/internal/version/infrastructure/postgres/requirement.go`、`requirement_integration_test.go`
- Create: `backend/internal/version/interfaces/httpapi/requirement_handler.go`、`requirement_dto.go`、`requirement_handler_test.go`
- Modify: `backend/internal/version/application/service.go`（新增 `MarkRunning`；不改 Create/Get/Update 语义）
- Modify: `backend/internal/version/application/views.go`（VersionView 增 `RequirementCounts`、`ConversationCount`、`PendingDesignCount`）
- Modify: `backend/internal/version/infrastructure/postgres/postgres.go`（实现 stats 查询；不改 P0 表）
- Modify: `backend/internal/version/interfaces/httpapi/handler.go`/`dto.go`（详情/列表补 counts；不破坏既有字段）

**Interfaces:**
```go
type RequirementStatus string // draft|clarified|closed|obsolete
type RequirementDraft struct { Title, Description, AcceptanceCriteria, Priority string; Constraints json.RawMessage; TargetVersionSUTs, TargetPages, TargetAPIs []string }
type RequirementService struct{ ... }
func NewRequirementService(repo RequirementRepository, versions VersionReader, resources ResourceCatalog, stale CaseStaleHook, audit auditapp.Recorder, events eventingapp.Publisher, ids id.Generator, clk clock.Clock) *RequirementService
func (s *RequirementService) Create(ctx, versionID int64, in RequirementDraft, actorID int64) (RequirementView, error)
func (s *RequirementService) Update(ctx, versionID, reqID int64, in RequirementDraft, reason string, actorID int64) (RequirementView, error)
func (s *RequirementService) Get(ctx, versionID, reqID int64) (RequirementDetail, error)
func (s *RequirementService) List(ctx, versionID int64, status string, limit int, beforeID *int64) ([]RequirementSummary, bool, error)
func (s *RequirementService) Confirm(ctx, versionID, reqID, actorID int64) (RequirementDetail, error)
func (s *RequirementService) BatchConfirm(ctx, versionID int64, reqIDs []int64, actorID int64) (BatchConfirmResult, error)
func (s *RequirementService) Rollback(ctx, versionID, reqID int64, reason string, proposalMessageID *int64, actorID int64) (RequirementDetail, error)
func (s *RequirementService) CreateRelation(ctx, versionID int64, in RelationInput, actorID int64) (RelationView, error)
type CaseStaleHook interface { MarkRequirementChanged(ctx context.Context, versionID, requirementID int64, reason string, revision int) ([]string, error) }
```
- 版本投影：Create/Confirm 首次触发 `MarkRunning`（首个 requirement/确认）；首条对话消息触发在 T11；事件 `version.started/status_changed`。
- 编辑/回退生成新 revision；确认写 requirement_confirmation（每 version 唯一）；批量事务；AI 不可确认（Agent 端口不暴露 Confirm）。
- P2 stale：调用 hook，事件带 `affected_case_version_ids`；P1 空实现返回 `[]`。
- 版本统计：requirement_counts、conversation_count、pending_design_count（=clarified 数）。
- 资源校验：target_version_suts ⊆ version_sut；target_pages/apis ⊆ 版本 scope（通过 VersionReader/CatalogReader）。
- HTTP DTO 使用 openapi P1 schemas：列表/创建/更新请求的 `targets:{version_suts,pages,apis}`；详情历史字段 `history`；确认返回 `{confirmed,requirement}`；批量返回 `{batch_id,confirmed,skipped}`。

- [ ] **Step 1: 写 domain 失败测试**（状态迁移、必填校验、priority 枚举、content_hash 稳定、revision 递增）。
- [ ] **Step 2: 写 service 失败测试**（fakes）：create draft；edit draft→revision+1；confirm 冻结/幂等；batch 全量校验/已确认跳过/非法 400；clarified edit→draft；rollback→新 draft + hook + 事件；跨版本 404；MarkRunning 只一次。
- [ ] **Step 3: 写 HTTP handler 失败测试**（DTO/ID 字符串/错误码/批量 body）。
- [ ] **Step 4: 写 postgres 集成失败测试**（唯一约束/事务/统计）。
- [ ] **Step 5: TDD 实现并跑通** `go test ./internal/version/...` + integration。
- [ ] **Step 6: Lead 提交** `feat(p1): add requirement versions and confirmation`。

## Task 11: 对话、消息、摘要与 API（backend-domain）

**Files:**
- Create: `backend/internal/version/domain/conversation.go`、`conversation_test.go`
- Create: `backend/internal/version/application/conversation.go`、`conversation_test.go`
- Create: `backend/internal/version/application/conversation_views.go`
- Create: `backend/internal/version/infrastructure/postgres/conversation.go`、`conversation_integration_test.go`
- Create: `backend/internal/version/interfaces/httpapi/conversation_handler.go`、`conversation_dto.go`、`conversation_handler_test.go`
- Create: `backend/internal/version/application/summary.go`、`summary_test.go`

**Interfaces:**
```go
type ConversationService struct{ ... }
func NewConversationService(repo ConversationRepository, queue TaskEnqueuer, versions VersionReader, clk clock.Clock, ids id.Generator, events eventingapp.Publisher) *ConversationService
func (s *ConversationService) Create(ctx, versionID int64, kind, title string, actorID int64) (ConversationView, error)
func (s *ConversationService) List(ctx, versionID int64, kind string, limit int, beforeID *int64) ([]ConversationSummary, bool, error)
func (s *ConversationService) Get(ctx, versionID, convID int64) (ConversationView, error)
func (s *ConversationService) Messages(ctx, versionID, convID int64, after int64, limit int) ([]MessageView, error)
func (s *ConversationService) PostUserMessage(ctx, versionID, convID int64, parts []MessagePart, clientMessageID string, actorID int64) (MessageView, error)
func (s *ConversationService) AppendAssistantMessage(ctx, convID int64, parts []MessagePart, runID int64) (MessageView, error)
func (s *ConversationService) Summary(ctx, convID int64) (SummaryView, error)
func (s *ConversationService) UpdateSummary(ctx, convID int64, summary string, pinned []string) error
```
- 消息 parts：text/attachment；附件校验/落 requirement_attachment + content_ref + ingest 任务；`client_message_id` 唯一幂等。
- 创建 conversation → opening 任务（best-effort）+ 事件；running 投影由首条对话消息（user 或 assistant）/首个 requirement/首次确认触发（reconciler 兜底）；仅创建空 conversation 不触发。
- 摘要服务：`pinned_facts` 上限 20/500 字；version 递增；事件 `conversation.summary_updated`。
- 列表/消息游标、计数、last_message_at、message_count。

- [ ] **Step 1: 写 domain 失败测试**（角色/parts 校验、标题/长度、summary pinned 限制）。
- [ ] **Step 2: 写 service 失败测试**（create/list/get；消息幂等；附件落库；opening/curate 任务；assistant 追加；cross-version 404；summary 版本）。
- [ ] **Step 3: 写 handler 失败测试**（创建/列表/消息 POST/分页/错误）。
- [ ] **Step 4: 写 postgres 集成失败测试**（唯一约束/游标/计数）。
- [ ] **Step 5: TDD 实现并跑通** `go test ./internal/version/...` + integration。
- [ ] **Step 6: Lead 提交** `feat(p1): add clarification conversations and messages`。

## Task 12: 附件存储、解析与 ingest（backend-domain）

**Files:**
- Create: `backend/internal/version/domain/attachment.go`、`attachment_test.go`
- Create: `backend/internal/version/application/attachment.go`、`attachment_test.go`
- Create: `backend/internal/version/infrastructure/attachments/parser.go`、`parser_test.go`
- Create: `backend/internal/version/infrastructure/postgres/attachment.go`、`attachment_integration_test.go`
- Modify: `backend/internal/version/interfaces/httpapi/conversation_dto.go`（附件 part 校验；如已在 T11 定义则对齐）
- Create: `backend/internal/version/infrastructure/attachments/fixtures_test.go`（生成 md/docx/pptx/简单 PDF）

**Interfaces:**
```go
type AttachmentService struct{ ... }
func NewAttachmentService(repo AttachmentRepository, storage StorageService, clk clock.Clock) *AttachmentService
func (s *AttachmentService) RegisterFromMessage(ctx, versionID, messageID int64, part MessagePart, actorID int64) (AttachmentView, error)
func (s *AttachmentService) Extract(ctx context.Context, attachmentID int64) (ExtractionResult, error)
func (s *AttachmentService) Get(ctx, versionID, attachmentID int64) (AttachmentView, error)
type Parser interface { Extract(ctx context.Context, name, mime string, r io.Reader) (string, error) }
```
- 支持 `.md/.markdown/.txt/.docx/.pptx/.pdf`；图片只存引用；`.doc/.ppt` → failed + `unsupported_attachment_format`；文本 ≤2MiB、PDF ≤50 页、XML ≤20MiB、zip bomb 防护。
- 解析文本上传 text content_object + content_ref（owner `requirement_attachment` role `extracted_text`）；不复制全文进需求版本。
- `Extract` 幂等：processing→done/failed；重复调用不重复上传（已有 extracted ref 直接返回）。
- 解析失败不阻塞消息；消息 POST 仍 201，附件状态 failed 可见。

- [ ] **Step 1: 写 parser 失败测试**：md/txt UTF-8；docx 段落/表格；pptx 多页顺序；PDF 简单文本；损坏 zip/PDF → error；超限 → error；文件名清洗不写盘。
- [ ] **Step 2: 写 service 失败测试**：注册附件/内容引用/ingest 任务；幂等；失败状态；不复制全文。
- [ ] **Step 3: TDD 实现**（`rsc.io/pdf`；stdlib archive/zip + encoding/xml）。
- [ ] **Step 4: 跑通** `go test ./internal/version/...` + integration。
- [ ] **Step 5: Lead 提交** `feat(p1): add attachment parsing and ingestion`。

## Task 13: 服务器装配、main worker 与后端集成动线（Lead）

**Files:**
- Modify: `backend/internal/server/server.go`（新 Deps + 注册 requirements/conversations/team-rooms 路由）
- Modify: `backend/internal/server/server_integration_test.go`（新增 P1 flow；保留 P0；更新 version 频道事件数量/详情字段期望）
- Modify: `backend/internal/eventing/interfaces/httpapi/handler.go`（SSE 补 `X-Snapshot-Cursor` 响应头；不改帧/游标语义）
- Modify: `backend/internal/eventing/interfaces/httpapi/dto.go`、`handler.go`、相关测试（D4：Event.seq/snapshot_cursor 输出整数，符合 P0 契约；同步前端事件类型）
- Create: `backend/internal/agent/infrastructure/versionadapter/`（Lead 装配：把 versionapp RequirementService/ConversationService/VersionService 适配为 agent 端口）
- Modify: `backend/cmd/eztest/main.go`（装配 agent/requirements/conversations/attachments/llm gateway；启动 worker/reconciler；优雅停止）
- Modify: `backend/internal/platform/errors.go`（新增稳定 reason 常量/映射；不改既有错误语义）
- Modify: `backend/internal/agent/README` 或包注释（单实例说明）
- Create: `backend/internal/server/p1_integration_test.go`

**Interfaces:**
- `server.Deps` 增 `Requirements *versionapp.RequirementService`、`Conversations *versionapp.ConversationService`。
- 路由：`/api/versions/{id}/conversations*`、`/api/versions/{id}/requirements*`、`/api/versions/{id}/team-rooms/{team}`，全部 `authenticated`。
- main 启动顺序：迁移→设置→ID→audit/events→identity→llm→storage→resource→version→requirement/conversation/attachment→agent(workqueue/teamroom/context/runner)→server→worker/reconciler goroutines；shutdown 先停 worker 再停 HTTP/DB。
- 集成测试：登录→建版本→建 conversation→POST 用户消息→（直接运行 reconciler+worker with fake runner 或 fake LLM）→需求 draft→确认→回退；SSE 事件存在；403/404 边界。

- [ ] **Step 1: 写编译/路由失败测试**（`TestP1RoutesRegistered` 405/404 行为；未登录 401；D4 seq/snapshot_cursor 为整数）。
- [ ] **Step 2: 实现装配与 main**；同时修 SSE `X-Snapshot-Cursor` 与 D4 整数输出；确保 P0 测试不回归。
- [ ] **Step 3: 写 P1 集成 flow**（使用 testutil DB + 真实 repo/service + fake LLM 或 fake runner；不依赖浏览器）。
- [ ] **Step 4: 跑通** `go test ./internal/server/...` + `go test -tags=integration ./internal/server/...`。
- [ ] **Step 5: Lead 提交** `feat(p1): wire runtime and clarification APIs`。

## Task 14: 前端 API/types、requirements 壳（frontend）

**Files:**
- Modify: `frontend/package.json`、`frontend/pnpm-lock.yaml`（Lead 在任务开始前执行 `pnpm add react-markdown remark-gfm` 并提交）
- Create: `frontend/src/features/clarification/types.ts`、`api.ts`、`api.test.ts`
- Create: `frontend/src/features/clarification/RequirementsTab.tsx`、`RequirementsTab.test.tsx`（先壳：加载/空/错误）
- Create: `frontend/src/features/clarification/index.ts`
- Modify: `frontend/src/lib/api-client.ts`（新增 `apiUpload`，支持 multipart；保持既有 API）
- Modify: `frontend/src/features/versions/VersionDetailPage.tsx`（requirements Tab 挂载 RequirementsTab；其他占位文案按真实阶段）
- Modify: `frontend/src/features/versions/VersionDetailPage.test.tsx`

**Interfaces:**
```ts
export type Version = { id: string; title: string; status: "initial"|"running"|"completed"; requirement_counts: {total:number;draft:number;clarified:number;closed:number;obsolete:number}; conversation_count: number; pending_design_count: number; bindings: ... }
export type Conversation = { id:string; title:string; kind:"clarification"|"design"; status:"active"|"closed"; message_count:number; last_message_at:string|null; created_at:string; summary?:string|null; updated_at?:string }
export type MessagePart = {type:"text";text:string}|{type:"attachment";sha256:string;name:string;mime:string;size:number}|{type:"requirement_card";requirement_id:string;title:string;status:string;revision:number}|{type:"rollback_proposal";requirement_id:string;requirement_title:string;reason:string}
export type Requirement = { id:string; current_version_id:string; revision:number; title:string; status:string; priority:string; targets:{version_suts:string[];pages:string[];apis:string[]}; updated_at:string; confirmed_at:string|null; current_version?:...; history?:...; confirmations?:...; relations?:...; attachments?:... }
```
- `apiUpload(path, formData)` uses `apiFetch` with FormData (no JSON content-type).
- `RequirementsTab` 初始壳只 fetch conversation/requirements counts；不实现 Chat/Sidebar（后续 T15/T16）。
- Vitest：api 路径/信封/错误、auth header、upload、tab 挂载/空态。

- [ ] **Step 1: 写 types/api 失败测试**；**Step 2: 实现**；**Step 3: 写壳测试**（render loading→empty→error；版本详情切 requirements 显示壳）。
- [ ] **Step 4: 跑通** `cd frontend; pnpm test -- src/features/clarification src/features/versions/VersionDetailPage.test.tsx`；`pnpm typecheck`。
- [ ] **Step 5: Lead 提交** `feat(p1): add clarification API client and tab shell`。

## Task 15: ChatPanel、Composer、附件与 SSE（frontend）

**Files:**
- Create: `frontend/src/features/clarification/ChatPanel.tsx`、`MessageList.tsx`、`Composer.tsx`、`AttachmentUploader.tsx`、`RichText.tsx`、`RollbackCard.tsx`、`RequirementCard.tsx`
- Create: `frontend/src/features/clarification/useConversation.ts`、`useVersionEvents.ts`
- Create: `ChatPanel.test.tsx`、`Composer.test.tsx`、`AttachmentUploader.test.tsx`、`RichText.test.tsx`、`useVersionEvents.test.ts`
- Modify: `RequirementsTab.tsx`

**Interfaces:**
- ChatPanel props `{versionId, conversationId, onRequirementSelect}`；内部 fetch messages、发送、SSE 刷新、activity 指示。
- Composer：Enter 发送、Shift+Enter 换行、发送中禁用、附件上传（`POST /api/content`）、错误内联。
- `useVersionEvents(versionId, handlers)` 用 `connectSSE`；只处理 `conversation.message/agent.activity/requirement.*/version.status_changed`；去重/重连交给 sse-client。
- RichText：用 `react-markdown` + `remark-gfm` 渲染 Markdown（禁 raw HTML、链接安全属性）；测试 XSS/HTML 输入不产生可执行节点。
- RollbackCard：`rollback_proposal` part → 确认回退（POST rollback）→ 回调刷新；暂不处理只本地隐藏。
- RequirementCard：显示 title/status/revision；点击选中。

- [ ] **Step 1: 写 RichText/Composer/Attachment 失败测试**（Enter/Shift+Enter、上传成功/失败、XSS 转义）。
- [ ] **Step 2: 写 ChatPanel/SSE 失败测试**（空态、消息渲染、发送后 append、SSE message 刷新、activity 指示、错误态、rollback 卡）。
- [ ] **Step 3: TDD 实现**；**Step 4: 跑通** `pnpm test -- src/features/clarification` + typecheck/lint。
- [ ] **Step 5: Lead 提交** `feat(p1): add clarification chat and attachments UI`。

## Task 16: 需求侧栏、编辑器、批量确认、团队面板与设计 CTA（frontend）

**Files:**
- Create: `RequirementSidebar.tsx`、`RequirementEditor.tsx`、`BulkConfirmBar.tsx`、`DesignCTA.tsx`、`TeamRoomPanel.tsx`
- Create: `RequirementSidebar.test.tsx`、`RequirementEditor.test.tsx`、`BulkConfirmBar.test.tsx`、`DesignCTA.test.tsx`、`TeamRoomPanel.test.tsx`
- Modify: `RequirementsTab.tsx`（组合 ChatPanel + Sidebar + CTA）

**Interfaces:**
- Sidebar：filter 全部/草案/已澄清；selection；batch confirm（POST batch）；list item 显示 title/status/priority/target chips；active 选中加载详情。
- Editor：fields + target multi-select（version_sut/page/api from version bindings）；保存→PATCH；确认→POST confirm；已澄清显示回退提示；错误内联。
- DesignCTA：`pending_design_count>0` 显示 “N 条需求待设计 · P2 提供设计对话”，按钮 disabled；不调用设计 API。
- 批量确认：确认后刷新列表/计数；部分已确认幂等；失败显示 details。
- TeamRoomPanel：折叠只读面板，fetch `GET /api/versions/{id}/team-rooms/clarification`；显示 digest/最近帖子；无数据空态；不提供发帖入口。

- [ ] **Step 1: 写 Sidebar/Bulk/CTA/TeamRoom 失败测试**（过滤/选择/批量调用/幂等提示/CTA 显隐/面板空态与只读）。
- [ ] **Step 2: 写 Editor 失败测试**（加载/保存/确认/已澄清回退提示/目标校验）。
- [ ] **Step 3: TDD 实现**；**Step 4: 跑通** `pnpm test -- src/features/clarification` + typecheck/lint。
- [ ] **Step 5: Lead 提交** `feat(p1): add requirement sidebar and editor`。

## Task 17: Requirements 页集成、深链与占位文案（frontend）

**Files:**
- Modify: `RequirementsTab.tsx`（URL `?conversation=&requirement=` 深链、布局、SSE 协调、错误边界）
- Modify: `frontend/src/features/versions/VersionDetailPage.tsx`（完全替换 requirements 占位；其余 Tab 文案 P2/P3/P4/P5）
- Create/Modify: `RequirementsTab.integration.test.tsx`、`VersionDetailPage.test.tsx`
- Modify: `frontend/src/app/(app)/versions/[id]/page.tsx`（如需传 searchParams）

**Interfaces:** URL 为真值；切换 conversation/requirement 写入 URL；刷新保持；移动端单列、桌面双列；团队面板折叠只读；无 console error；空状态/加载/错误/无权限一致。

- [ ] **Step 1: 写集成测试**（切换会话/选中需求写 URL；直接打开深链恢复；Tab 切换；团队面板展开/空态；空版本）。
- [ ] **Step 2: 实现/修正**；**Step 3: 跑通** `pnpm test`、`pnpm typecheck`、`pnpm lint`、`pnpm build`。
- [ ] **Step 4: Lead 提交** `feat(p1): integrate requirements workspace`。

## Task 18: P1 E2E、API 验收与演示证据（Lead）

**Files:**
- Create: `scripts/p1-e2e.ps1`（基于 `p0-e2e.ps1`：fake LLM + API + web + Playwright）
- Create: `frontend/e2e/p1-requirements.spec.ts`（若 P0 E2E 目录不同，按现有结构）
- Create: `scripts/p1-acceptance.ps1`（API 动线：任务板并发/恢复、对话/需求/确认/回退/SSE/附件）
- Create: `scripts/p1-acceptance-llm.ps1`（真实 LLM 必跑：读取 `.env` 的 `llm_api_key`/`llm_base_url`/`llm_model_name`（deepseek-flash），经 admin API 配置 `llm_config` 并 refresh，跑澄清闭环；缺失凭据则明确失败并阻塞 handoff）
- Modify: `scripts/fake-llm.cjs`（仅当 T7 未覆盖的 E2E 场景需要；Lead 与 runtime 协调后改）
- Create/Modify: `.tmp/p1-acceptance/` evidence（gitignored）

**Interfaces:** 演示路径：admin/admin123 → 版本 → 需求 Tab → 新建对话 → 发送含附件消息 → Host 回复 + 草案卡片 → 编辑 → 确认 → 新建第二条 → 批量确认 → 回退卡 → 设计 CTA；无 console error；截图。

- [ ] **Step 1: 写 API 验收脚本**并本地跑（fake LLM 配置 + admin API 建资源/版本；断言 draft/clarified/rollback/SSE/附件）。
- [ ] **Step 2: 写 Playwright spec**（data-testid 稳定选择器；上传生成的小 docx/md；等待 SSE 卡片）。
- [ ] **Step 3: 跑 E2E** 使用独立 `eztest_v02_e2e` 库、重建 `.tmp/p1/eztest.exe`、独立启动 fake LLM/API/Web；失败按 systematic-debugging 修。
- [ ] **Step 4: 截图** 登录/需求空态/对话草案/批量确认/回退卡/设计 CTA → `.tmp/p1-acceptance/screenshots/`。
- [ ] **Step 5: 真实 LLM 验收** 读取 `.env` 的 `llm_api_key/llm_base_url/llm_model_name`（`deepseek-flash`），经 admin API 写 `llm_config` 并 refresh-models，跑“新对话→真实模型澄清→draft→确认”动线；日志 `.tmp/p1-acceptance/llm.log`；凭据缺失则停止并在 handoff 标 blocked，不以 fake 结果替代。
- [ ] **Step 6: Lead 提交** `test(p1): add acceptance and e2e journeys`。

## Task 19: 独立评审（reviewer subagent / Lead）

**Files:** 无生产修改；评审报告 `.tmp/p1/review.md`（gitignored）

**Interfaces:** 按 `superpowers:requesting-code-review` 模板；评审 spec/契约/迁移/后端/前端/diff；必须给出阻塞项/非阻塞项/证据。

- [ ] **Step 1: 获取 diff**（`git diff feat/v0.2-p0...HEAD`）。
- [ ] **Step 2: 独立 reviewer 检查**：契约漂移、scope 注入、并发/租约、压缩丢事实、旧假设、权限/错误/幂等、前端 XSS/SSE 重连。
- [ ] **Step 3: Lead 处理阻塞项**（TDD 修复 + 提交）；非阻塞项进 handoff 风险。
- [ ] **Step 4: 复审通过**。

## Task 20: 全量 gate、契约冻结、handoff 与演示（Lead）

**Files:**
- Modify: `docs/version-V0.2/contracts/**`（冻结：`x-frozen: P1`，状态/首页 README）
- Create: `docs/version-V0.2/phases/phase-01-handoff.md`（按模板）
- Create: `.tmp/p1-acceptance/` 证据（日志/截图/测试输出；gitignored）
- Modify: `docs/version-V0.2/README.md`? **不改**（设计文档）；只在 handoff 记录
- Modify: `.tmp` 不提交

**Interfaces:** handoff 必须含：完成范围、契约变更/冻结、迁移顺序、测试命令/结果、演示路径、真实 LLM 验收结果（`.env` `llm_*`，`deepseek-flash`）、已知缺口（.doc/.ppt、stale P2、单实例、上下文估算、P0 D6–D10 backlog）、P2 输入（需求版本/确认/stale 端口/design_scope 占位/工具预留）、风险。

- [ ] **Step 1: 全量 gate** `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1`（Go 单测+集成、pnpm lint/typecheck/test）；日志 `.tmp/p1/final-gate.log`。
- [ ] **Step 2: 生产构建** `cd frontend; pnpm build`；记录路由。
- [ ] **Step 3: 跑 P1 E2E + API 验收**；保存日志/截图。
- [ ] **Step 4: 契约一致性检查**：openapi YAML 解析；P1 表/端点/事件/工具逐项对照实现；`x-frozen: P1`。
- [ ] **Step 5: 演示验收**：启动本机栈，按路径逐项截图/记录；admin 凭据；开发库说明。
- [ ] **Step 6: 写 handoff**（状态 completed；若遗留阻塞标 partial 并停止报告）。
- [ ] **Step 7: 最终报告**：完成项、证据路径、下一阶段输入/风险；**停止，不开始 P2**。

---

## Self-Review

**Spec coverage**（spec → task）
- §3 迁移/表 → T1；§8 API → T5（team room）/T10/T11/T13；§9 事件 → T5/T10/T11/T13；§4 任务板/租约/reconciler → T3/T4/T9；§5 runtime/context/tools → T6/T7/T8；§6 对话/需求/确认 → T10/T11；§7 附件 → T12；§10 前端 → T14–T17；§11 契约 → T2/T20；§13 测试 → 各任务 + T18/T20；§12 裁决 → 实现口径。
- P1 验收 1–4 → T8/T10/T11/T9/T18；压力/overflow → T6/T8/T18；团队频道可演示 → T5/T16/T17；真实 LLM → T18/T20。
- 五类 Review Focus → T2/T8/T6/T4/T10 + T19 独立评审。

**Step scan**：每个 Step 是可执行动作/命令/断言；无 “TBD/处理边界/加适当测试”。测试步骤带具体测试名；实现步骤带文件与接口。Task 内部按 TDD：失败测试→跑失败→实现→跑通→提交（Lead 代提交）。

**Type consistency**：`WorkItemStatus`/`WorkItem`/`NewWorkItem` 在 T3/T4/T9 一致；`ChatGateway/ChatRequest/ChatResponse/ModelInfo/ResolveModel` 在 T7/T8 一致；`ContextInput.ModelBudget` 由 T6 定义、T8 填充；`RequirementService` 方法与 T8 的 `RequirementWriter` 端口在 T8 实现时按 T10 接口名对齐（T10 先定义，T8 依赖该接口）；`ConversationService` 与 T8 `ConversationPort` 同名方法；`ContextBuilder.Build` 返回 `ContextPacket` 与 `context_packet` 表一致；API JSON 字段在 spec §8.5/T2/T10/T11/T14 一致（snake_case，ID 字符串）。
- 需在实现时锁定的跨任务签名：`RequirementWriter` 的 `AgentCreate/AgentUpdate/AgentReopenProposal/AgentRelation` 由 T8 定义，T10 的 service 方法通过 adapter（Lead 在 T13 装配）适配；若签名不一致，以 T10 service 为准改 T8 端口。

**Review Focus 测试映射**
1. 契约漂移 → T2 YAML 解析 + T20 逐项比对；handler/DTO 测试。
2. scope/ID → T8 registry 测试（schema 无 versionId）+ T10 跨版本 404 + T11 跨版本 404。
3. 压缩丢事实 → T6 压缩保留 pinned/最近/约束 + 模型窗口优先/回退测试 + T8 overflow 重试 + T18 长会话。
4. 幂等/租约 → T4 并发领取/过期/epoch + T9 reconciler 重复扫描 + T13 集成。
5. 旧假设 → T10 确认/回退权限测试 + T19 评审清单 + T20 契约审计。

**Proportion**：本计划 ~20 个任务，与 XL 阶段匹配；实现细节交给 spec 与任务内接口，未逐行写代码。

**Known plan risks**
- T2 的 openapi 增量较大，若与实现 DTO 漂移，T20 必须逐项核对；禁止以“代码为准”跳过契约。
- T3–T9 是单条 runtime 流水线，串行依赖多；可并行 T5/T6/T7 缩短关键路径。
- T10/T11 同属 version 上下文，必须一人串行写（backend-domain），避免 package 级冲突。
- T12 PDF/docx/pptx 解析是本阶段最高不确定项；先做 fixture 与失败测试，若 PDF 库不可用则用最小自研解析并记录缺口（不阻塞其他任务）。
- T18 真实 LLM 依赖 `.env` 的小写 `llm_*`（已存在 `llm_api_key/llm_base_url/llm_model_name=deepseek-flash`）；验收前若缺失，按 blocked 报告，不得伪造。上下文窗口以网关模型目录为准，缺失回退设置。
- T18 E2E 依赖本机 PostgreSQL/Playwright；若环境不满足，用集成测试 + API 验收替代并在 handoff 记录，不得伪造通过。
