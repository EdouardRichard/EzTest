# EzTest P1 Agent Runtime Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付可复用的 Agent 运行时（会话/上下文/工具/活动事件/预算/中止）与一个运行时验证 Agent，并在 `/agents` 控制台端到端跑通。

**Architecture:** 新增 `agent` 限界上下文（DDD 四层），`agent_messages` 为 LLM 上下文权威、P0 `events` 为 UI 投影；进程内 Runner 按「LLM ↔ 工具」循环执行 run，复用 P0 SSE；LLM 网关扩展原生 function calling 与 thinking/max effort。

**Tech Stack:** Go 1.24.6（标准库 `net/http`、pgx/v5）、PostgreSQL 17.6、Next.js 16 / React 19 / TypeScript / Tailwind / Radix、Vitest、Playwright（本机 Chrome/Edge）。

**Spec:** [docs/superpowers/specs/2026-10-27-eztest-p1-agent-runtime-design.md](../specs/2026-10-27-eztest-p1-agent-runtime-design.md)

## Global Constraints

- P1 范围严格按 `implementation-phases.md` §2-P1 与本规格；**不建**版本/需求/用例/执行/证据/问题/报告任何领域实体。
- Go module：`github.com/EdouardRichard/EzTest/backend`；只使用标准库 `net/http` 路由；导入方向严格按 `Agent.md` §6.2。
- 端口：Web `6868`、API `13333`、PostgreSQL `5433`；CORS 仅 `EZTEST_WEB_ORIGIN=http://localhost:6868`。
- 新上下文目录：`backend/internal/agent/{domain,application,infrastructure,interfaces/httpapi}`；每层职责按 `Agent.md` §6.1。
- 迁移：`backend/internal/db/migrations/agent_0001_profiles_sessions_runs_messages.sql`，追加到 `order.txt`；只增不改，重复执行无副作用。
- 事件 V1：只新增 `type=agent.activity`，通道 `agent:{session_id}`；`detail.kind` 固定 9 类；`summary ≤500`、`detail ≤64KB`；**不改** P0 SSE 信封/端点语义。
- 工具：仅 `file.list`、`file.read`、`http.get`、`test.echo`、`test.fail`；全部只读/无副作用；白名单由 profile 控制。
- LLM：只保证 `chat_completions`；请求带 tools 时**必须回传历史 `reasoning_content`**；内置 profile `thinking=enabled`、`reasoning_effort=max`、`max_output_tokens=32768`、`run_seconds=600`、`max_turns=12`。
- 权限：P1 全部 `/api/agents/*` 与 `agent:` SSE 通道 **admin-only**；普通用户 403。
- 不新增前端依赖；所有 HTTP 走 `api-client`，SSE 走 `sse-client`；不引入 Markdown 渲染。
- 每个任务：先写失败测试 → 跑失败 → 最小实现 → 跑通过 → 提交；提交前缀 `feat/fix/test/docs/chore`。
- 不提交 `.env`、`.tools/`、`.secrets/`、`*.local.md`、`.tmp/`；日志/事件/审计/错误中不得出现 token、Key、密码、文件内容。
- 分支：`feat/p1-agent-runtime`，小步提交，不 push；验收后本地合并回 `dev`。

### 命令约定

- Go 工具链：`D:\Project_new\EzTest\.tools\go\bin\go.exe`（回退 `go`）。单元测试示例：
  `cd backend; & ..\.tools\go\bin\go.exe test ./internal/agent/domain -run TestProfileValidate -v`
- 前端：`cd frontend; pnpm vitest run <path>`、`pnpm lint`、`pnpm typecheck`。
- 集成测试：`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-integration.ps1 <go test 参数>`（该脚本由 Task 4 创建，自动导入 `.env` 的 `EZTEST_TEST_DATABASE_URL`）。
- 全量门禁：`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1`；E2E：`scripts/e2e.ps1`；验收：`scripts/acceptance.ps1`。

## Concurrency Map（实现时并行执行的唯一依据）

| Wave | Tasks | 并行规则 |
|---|---|---|
| W0（串行门禁） | T1 → T2 | T2 失败则**停止**，不进入 W1 |
| W1 | T3 ∥ T4 | 写作用域互斥，可并行 |
| W2 | T5 ∥ T7 ∥ T8 ∥ T9 ∥ T10；T6 在 T5 后 | T5/T7/T8/T9/T10 可并行；T6 依赖 T5 |
| W3 | T11 ∥ T12 ∥ T13 ∥ T14 | 四个任务写作用域互斥，可并行 |
| W4 | T15 | 串行（组装根 + 路由 + 适配器） |
| W5 | T16 ∥ T17 ∥ T18 | 可并行 |
| W6 | T19 | 串行（全量回归 + 人工验收） |

- 同一 Wave 的任务派发给并行 subagent；每个 task 的 `**写作用域**` 是该 subagent 唯一允许修改的路径前缀。
- 跨 Wave 的依赖未完成前不得启动后续任务；接口以各任务 `Interfaces → Produces` 为准。

## Review Focus（最容易被忽略、必须有测试钉住的 5 类输入）

1. **工具续轮的 `reasoning_content` 缺失**：请求带 tools 时，历史 assistant 消息必须带回思考链，否则上游 400。测试在 T3（payload）与 T11（上下文重组）。
2. **流式工具调用分片不完整/非法 JSON**：首片带 id/name、后续只带 arguments，模型也可能给出非法 JSON。测试在 T3（装配）与 T5（执行器返回 `invalid_arguments` 的 `IsError`，不崩）。
3. **流式中止/超时**：取消必须立刻传递到 LLM 流与工具 ctx，终态原因正确，部分输出保留，无 goroutine 泄漏。测试在 T11。
4. **文件/HTTP 工具的越界读取**：`..`、软链逃逸、`.env`/`.secrets`，以及 `http.get` 重定向到白名单外。测试在 T6。
5. **超长文本/工具输出撞事件上限**：`detail ≤64KB`，长回答分片，工具结果 32KB 截断；事件发布失败不拖垮 run。测试在 T3/T5/T7/T11。

---

## Pre-flight（一次性，不算任务）

- [ ] `git switch -c feat/p1-agent-runtime`（从 `dev`）。
- [ ] 确认 PostgreSQL 运行：`powershell -File scripts/db.ps1 status`；`eztest`/`eztest_test` 存在。
- [ ] 在本地 `.env` 增加（不提交）：`EZTEST_AGENT_FILE_ROOT=<仓库根绝对路径>`；确认 `llm_base_url`/`llm_key`/`llm_model_name` 有效。
- [ ] 确认默认 LLM 配置超时足够思考模式：设置页把 `timeout_ms` 调到 `180000`（或启动后用 API PATCH）。
- [ ] 确认 `.env` 的 `EZTEST_TEST_DATABASE_URL` 指向 `eztest_test`。

---

### Task 1: 设计变更落文档（D27 / R1–R3）

**Wave:** W0（串行门禁） · **并发:** 不可并发 · **依赖:** 无
**写作用域:** `docs/design/`

**Files:**
- Modify: `docs/design/system-blueprint.md`（§3.2 决策记录、§6.3、§15）
- Modify: `docs/design/implementation-phases.md`（§2-P1）

**Interfaces:**
- Consumes: 规格 §15.1–§15.3。
- Produces: 主蓝图中的 D27 记录，后续任务据此实现。

- [ ] **Step 1: 新增 D27 决策行**

在 `system-blueprint.md` §3.2 表格 D26 之后追加一行，正文使用规格 §15.1 的 D27 原文（运行时验证夹具、不进入十三角色、只读工具白名单、`agent.activity` 9 类 kind、messages 权威 / events 投影、P2 起替换）。

- [ ] **Step 2: 补 §6.3 运行时底座说明**

在 §6.3 末尾追加一句：

```text
P1 提供一个**不进入 §6.1 十三角色**的运行时验证 Agent（key=`runtime-diagnostic`），仅用于验收运行时（会话/工具/活动流/预算/中止），P2 起由业务 Agent 替换（D27）。
```

- [ ] **Step 3: 更新 §15 P1 行注与 implementation-phases**

- `system-blueprint.md` §15 P1 行末尾加 `（诊断 Agent = 运行时验证夹具，见 D27）`。
- `implementation-phases.md` §2-P1 第 98 行「诊断 Agent」处改为 `诊断 Agent（运行时验证夹具，非业务角色，见蓝图 D27）`。

- [ ] **Step 4: 校验**

Run:
```powershell
Select-String -Path docs\design\system-blueprint.md -Pattern 'D27|运行时验证夹具' -Encoding UTF8
Select-String -Path docs\design\implementation-phases.md -Pattern '运行时验证夹具' -Encoding UTF8
```
Expected: 两文件各命中；`git diff --stat` 仅这两个文件。

- [ ] **Step 5: 提交**

```bash
git add docs/design/system-blueprint.md docs/design/implementation-phases.md
git commit -m "docs: record D27 runtime-validation agent decision"
```

---

### Task 2: 真实 DeepSeek 能力 spike（门禁，throwaway）

**Wave:** W0（串行门禁） · **并发:** 不可并发 · **依赖:** T1
**写作用域:** `.tmp/`（不提交）

**Files:**
- Create: `.tmp/spike-agent-llm.ps1`（临时，不提交）

**Interfaces:**
- Consumes: `.env` 的 `llm_base_url` / `llm_key` / `llm_model_name`。
- Produces: 结论「上游支持 thinking=max + 工具调用 + reasoning_content 回传」；失败则停止后续任务。

- [ ] **Step 1: 写 spike 脚本**

脚本行为（不打印 Key、不打印完整响应）：
1. 从 `.env` 读取三个值；
2. 第一次 `POST {base}/chat/completions`：`model`、`messages=[{role:user,content:"读取 /api/health 并告诉我状态"}]`、`tools=[{type:function,function:{name:"http_get",description:"GET a URL",parameters:{type:object,properties:{url:{type:string}},required:["url"]}}}]`、`thinking={type:enabled}`、`reasoning_effort=max`、`max_tokens=2048`、`stream=false`；
3. 断言响应 `choices[0].message.tool_calls` 非空、`reasoning_content` 非空；
4. 第二次请求：把第一次的 assistant 消息（含 `content`/`reasoning_content`/`tool_calls`）原样加入，再追加 `{role:"tool",tool_call_id:<id>,content:"{\"status\":\"ok\"}"}`，同样带 `tools`/`thinking`/`reasoning_effort`；
5. 断言第二次返回 200 且 `content` 非空；只打印 `SPIKE OK tool_calls=<n> reasoning_chars=<n> second_status=200`。

- [ ] **Step 2: 运行**

Run:
```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .tmp\spike-agent-llm.ps1
```
Expected: `SPIKE OK ...`；若 400/无 tool_calls/无 reasoning_content，**停止**并在报告中给出原始错误码与消息（不含 Key）。

- [ ] **Step 3: 记录结果**

把结论写入任务报告（命令输出摘要），不提交 `.tmp/`。

---

### Task 3: LLM 网关扩展（tools / reasoning / thinking / usage）

**Wave:** W1 · **并发:** 可并发（与 T4） · **依赖:** T2
**写作用域:** `backend/internal/llm/`

**Files:**
- Modify: `backend/internal/llm/application/client.go`
- Modify: `backend/internal/llm/infrastructure/deepseek/chat.go`
- Modify: `backend/internal/llm/infrastructure/deepseek/responses.go`
- Test: `backend/internal/llm/infrastructure/deepseek/tools_test.go`（新增）

**Interfaces:**
- Consumes: P0 `llm.Config`、`deepseek.NewDeepSeekClient(cfg, *http.Client)`、P0 测试助手 `testClient`/`decodeBody`/`writeSSE`/`collectStream`（同包 `_test`）。
- Produces（`llm/application`，后续 T11/T15 依赖）：
```go
type ToolSpec struct { Name, Description string; Parameters json.RawMessage }
type ToolCall struct { ID, Name, Arguments string }
type Message struct {
    Role string; Content []Part
    ReasoningContent string; ToolCalls []ToolCall; ToolCallID string; Name string
}
type ChatRequest struct {
    Model string; Messages []Message; Tools []ToolSpec; Stream bool
    MaxTokens int; Thinking string; ReasoningEffort string
}
type ChatResponse struct {
    Text, ReasoningContent string; ToolCalls []ToolCall; FinishReason string; Usage Usage
}
type StreamEvent struct {
    Delta, ReasoningDelta string; Done bool
    ToolCalls []ToolCall; FinishReason string; Usage Usage; Err error
}
type Usage struct { PromptTokens, CompletionTokens, ReasoningTokens, TotalTokens int }
```

- [ ] **Step 1: 写失败测试（请求体契约）**

`TestChatCompletionsSendsToolsThinkingAndReasoning`：`httptest` 捕获 body，断言：
- `tools[0].function.name == "file_read"`；
- `thinking.type == "enabled"`、`reasoning_effort == "max"`、`max_tokens == 2048`；
- 历史 assistant 消息含 `reasoning_content:"think-1"`、`tool_calls[0].id=="call_1"`；
- tool 消息含 `tool_call_id:"call_1"` 且 `content[0].type=="text"`。

- [ ] **Step 2: 跑失败**

Run: `cd backend; & ..\.tools\go\bin\go.exe test ./internal/llm/infrastructure/deepseek -run TestChatCompletionsSendsToolsThinkingAndReasoning -v`
Expected: FAIL（字段不存在/未序列化）。

- [ ] **Step 3: 写失败测试（响应与流式）**

```go
func TestChatCompletionsParsesToolCallsWithEmptyContent(t *testing.T) {
    // upstream: {"choices":[{"message":{"content":null,"reasoning_content":"r",
    //   "tool_calls":[{"id":"call_1","type":"function",
    //   "function":{"name":"file_read","arguments":"{\"path\":\"README.md\"}"}}]},
    //   "finish_reason":"tool_calls"}]}
    // assert Text==""、ReasoningContent=="r"、ToolCalls[0].Name=="file_read"、FinishReason=="tool_calls"
}

func TestChatCompletionsStreamAssemblesReasoningAndToolCalls(t *testing.T) {
    // SSE frames:
    // 1) delta.reasoning_content:"think-"
    // 2) delta.reasoning_content:"1"
    // 3) delta.tool_calls:[{index:0,id:"call_1",type:"function",function:{name:"file_read",arguments:"{\"pa"}}]
    // 4) delta.tool_calls:[{index:0,function:{arguments:"th\":\"README.md\"}"}}]
    // 5) delta.content:"" finish_reason:"tool_calls" usage:{...reasoning_tokens:7}
    // assert: 收集到 ReasoningDelta "think-","1"；Done.ToolCalls[0].Arguments == `{"path":"README.md"}`；
    //         Done.Usage.ReasoningTokens==7；FinishReason=="tool_calls"
}

func TestResponsesRejectsTools(t *testing.T) {
    // cfg.Protocol=responses，ChatRequest.Tools 非空 → 错误 code==platform.CodeLLMBadRequest
}
```

- [ ] **Step 4: 跑失败**

Run: `cd backend; & ..\.tools\go\bin\go.exe test ./internal/llm/infrastructure/deepseek -run 'TestChatCompletionsParsesToolCallsWithEmptyContent|TestChatCompletionsStreamAssemblesReasoningAndToolCalls|TestResponsesRejectsTools' -v`
Expected: FAIL。

- [ ] **Step 5: 实现**

- `client.go`：新增/扩展 §Interfaces 中的类型；`Usage.ReasoningTokens`。
- `chat.go`：
  - `chatRequestPayload` 增 `Tools []chatToolPayload`、`Thinking *chatThinkingPayload`、`ReasoningEffort string`；
  - `chatMessagePayload` 的 `Content` 改为可空（`omitempty`），增 `ToolCalls`、`ReasoningContent`、`ToolCallID`；
  - tool 消息以 parts 数组发送；assistant 的 `reasoning_content` 非空才发；
  - 解析 `choices[0].message.tool_calls` 与 `reasoning_content`；`finish_reason`；
  - 流式按 `index` 聚合 `delta.tool_calls`，累加 `reasoning_content`；Done 时带装配结果、usage、finish_reason；
  - 空 `content` 且有 `tool_calls` 不再视为错误；
  - usage 解析 `completion_tokens_details.reasoning_tokens`。
- `responses.go`：`Chat`/`ChatStream` 开头若 `len(req.Tools)>0` 返回 `llm_bad_request`。

- [ ] **Step 6: 跑通过 + 全包回归**

Run:
```powershell
cd backend; & ..\.tools\go\bin\go.exe test ./internal/llm/... -v
```
Expected: PASS（含 P0 原有 LLM 测试）。

- [ ] **Step 7: 提交**

```bash
git add backend/internal/llm
git commit -m "feat: extend llm gateway with native tool calls and reasoning"
```

---

### Task 4: Agent 领域模型、迁移、仓储与应用端口

**Wave:** W1 · **并发:** 可并发（与 T3） · **依赖:** T2
**写作用域:** `backend/internal/agent/domain/`、`backend/internal/agent/application/ports.go`、`backend/internal/agent/infrastructure/postgres/`、`backend/internal/db/migrations/`、`backend/internal/testutil/`（若需）、`scripts/test-integration.ps1`

**Files:**
- Create: `backend/internal/agent/domain/{profile.go,session.go,run.go,message.go,activity.go,tool.go,repository.go,errors.go}`
- Create: `backend/internal/agent/application/ports.go`
- Create: `backend/internal/agent/infrastructure/postgres/{profile_repository.go,session_repository.go,run_repository.go,message_repository.go}`
- Create: `backend/internal/db/migrations/agent_0001_profiles_sessions_runs_messages.sql`
- Modify: `backend/internal/db/migrations/order.txt`
- Create: `scripts/test-integration.ps1`
- Test: `backend/internal/agent/domain/profile_test.go`
- Test: `backend/internal/agent/domain/run_test.go`
- Test: `backend/internal/agent/infrastructure/postgres/repository_integration_test.go`

**Interfaces:**
- Consumes: `testutil.OpenTestDB(t)`（集成测试）、`db.MigrateEmbedded`。
- Produces（后续任务依赖的精确签名）：

```go
// domain
const BuiltinProfileKey = "runtime-diagnostic"
type AgentProfile struct {
    ID int64; Key, Name, Description string; Builtin, Enabled bool
    SystemPrompt, Model string; AllowedTools []string
    MaxTurns, RunSeconds, HardTimeoutSeconds int
    Thinking, ReasoningEffort string; MaxOutputTokens int
    CreatedAt, UpdatedAt time.Time
}
type UpdateParams struct {
    SystemPrompt *string; Model *string; AllowedTools *[]string
    MaxTurns *int; RunSeconds *int; HardTimeoutSeconds *int
    Thinking *string; ReasoningEffort *string; MaxOutputTokens *int; Enabled *bool
}
func BuiltinProfile() AgentProfile
func (p AgentProfile) Validate() error
func (p *AgentProfile) Apply(params UpdateParams) error

type Session struct { ID, ProfileID int64; ProfileKey, Title string; CreatedBy *int64; CreatedAt, UpdatedAt, LastActivityAt time.Time }

type RunStatus string
type TerminalReason string
const (
    RunRunning RunStatus = "running"; RunDone = "done"; RunFailed = "failed"
    RunAborted = "aborted"; RunInterrupted = "interrupted"
    ReasonCompleted TerminalReason = "completed"; ReasonMaxTurns = "max_turns"
    ReasonTimeout = "timeout"; ReasonModelError = "model_error"
    ReasonContextOverflow = "context_overflow"; ReasonAbortedStreaming = "aborted_streaming"
    ReasonAbortedTools = "aborted_tools"; ReasonAborted = "aborted"
    ReasonShutdown = "shutdown"; ReasonRunHardTimeout = "run_hard_timeout"
    ReasonInterrupted = "interrupted"
)
type Run struct {
    ID, SessionID int64; Status RunStatus; TerminalReason TerminalReason
    AbortCode, AbortMessage, ErrorCode, ErrorMessage string
    Turns int; InputTokens, OutputTokens, ReasoningTokens int64
    LastToolName string; LastToolPending bool
    StartedAt time.Time; EndedAt *time.Time
}
type RunResult struct {
    Status RunStatus; TerminalReason TerminalReason
    AbortCode, AbortMessage, ErrorCode, ErrorMessage string
    Turns int; Usage Usage; LastToolName string; LastToolPending bool
}
type TurnIDs struct { RunID, UserMessageID int64 }

type Part struct { Type, Text, ImageURL string }
type ToolCall struct { ID, Name, Arguments string }
type Usage struct { PromptTokens, CompletionTokens, ReasoningTokens, TotalTokens int }
type Message struct {
    ID, SessionID int64; RunID *int64; Role string; Parts []Part
    Reasoning string; ToolCalls []ToolCall
    ToolCallID, ToolName string; Usage *Usage; CreatedAt time.Time
}

const EventType = "agent.activity"
const (
    KindRunStarted="run_started"; KindUserMessage="user_message"; KindThinking="thinking"
    KindText="text"; KindToolUse="tool_use"; KindToolResult="tool_result"
    KindUsage="usage"; KindDecision="decision"; KindRunFinished="run_finished"
)
func ChannelForSession(sessionID int64) string

type Tool interface {
    Name() string; Description() string; InputSchema() json.RawMessage
    Execute(ctx context.Context, args json.RawMessage) (ToolResult, error)
}
type ToolResult struct { Content string; IsError bool }
type ToolError struct { Code, Message string }

var ErrNotFound = errors.New("agent: not found")

type ProfileRepository interface {
    List(ctx context.Context) ([]AgentProfile, error)
    Get(ctx context.Context, id int64) (AgentProfile, error)
    Update(ctx context.Context, id int64, params UpdateParams) (AgentProfile, error)
    EnsureBuiltin(ctx context.Context, profile AgentProfile) (bool, error)
}
type SessionRepository interface {
    List(ctx context.Context, cursor int64, limit int, profileID *int64) ([]SessionSummary, bool, error)
    Create(ctx context.Context, profileID int64, title string, createdBy *int64) (Session, error)
    Get(ctx context.Context, id int64) (Session, error)
    UpdateTitle(ctx context.Context, id int64, title string) (Session, error)
    Delete(ctx context.Context, id int64) error
}
type RunRepository interface {
    CreateTurn(ctx context.Context, sessionID int64, content []Part, titlePreview string) (TurnIDs, error)
    Get(ctx context.Context, id int64) (Run, error)
    Active(ctx context.Context, sessionID int64) (Run, error)
    Latest(ctx context.Context, sessionID int64) (Run, error)
    UpdateProgress(ctx context.Context, runID int64, turns int, usage Usage) error
    Finish(ctx context.Context, runID int64, result RunResult) error
    MarkRunningInterrupted(ctx context.Context) (int64, error)
}
type MessageRepository interface {
    ListBySession(ctx context.Context, sessionID int64) ([]Message, error)
    Get(ctx context.Context, id int64) (Message, error)
    Append(ctx context.Context, message Message) (Message, error)
    AppendAssistantPlaceholder(ctx context.Context, sessionID, runID int64) (int64, error)
    CompleteAssistant(ctx context.Context, messageID int64, parts []Part, reasoning string, calls []ToolCall, usage *Usage) error
}

// infrastructure/postgres 构造器（T15 组装根使用）
func NewProfileRepository(pool *pgxpool.Pool) *ProfileRepository
func NewSessionRepository(pool *pgxpool.Pool) *SessionRepository
func NewRunRepository(pool *pgxpool.Pool) *RunRepository
func NewMessageRepository(pool *pgxpool.Pool) *MessageRepository
type SessionSummary struct { Session Session; ActiveRun *Run; LastRun *Run } // domain/session.go
type SessionDetail struct { Session Session; Running bool; ActiveRun *Run; LastRun *Run }
type StartResult struct { RunID, UserMessageID int64 } // domain/run.go
type AbortResult struct { Status string }              // "stopping" | "idle"
type ToolInfo struct { Name, Description string; Schema json.RawMessage } // domain/tool.go

// domain/errors.go：所有哨兵错误（application/httpapi 直接使用）
var (
    ErrNotFound          = errors.New("agent: not found")
    ErrRunActive         = errors.New("agent: run already active")
    ErrProfileDisabled   = errors.New("agent: profile disabled")
    ErrCapacity          = errors.New("agent: run capacity exhausted")
    ErrEmptyMessage      = errors.New("agent: empty message")
    ErrLLMNotConfigured  = errors.New("agent: llm is not configured")
    ErrToolsUnsupported  = errors.New("agent: tools require the chat_completions protocol")
    ErrContextOverflow   = errors.New("agent: context overflow")
)

// application/ports.go：只放 LLM / 事件 / 审计 / 时钟端口类型
type LLMMessage struct {
    Role string; Parts []Part; ReasoningContent string
    ToolCalls []ToolCall; ToolCallID string
}
type LLMTool struct { Name, Description string; Parameters json.RawMessage }
type LLMRequest struct {
    Model string; Messages []LLMMessage; Tools []LLMTool
    MaxTokens int; Thinking, ReasoningEffort string
}
type LLMStreamEvent struct {
    Delta, ReasoningDelta string; Done bool
    ToolCalls []ToolCall; FinishReason string; Usage Usage; Err error
}
type LLMCaller interface {
    Model() string
    ContextWindowTokens() int
    ChatStream(ctx context.Context, req LLMRequest) (<-chan LLMStreamEvent, error)
}
type LLMRuntime interface {
    Prepare(ctx context.Context, modelOverride string) (LLMCaller, error)
}

type ActivityEvent struct {
    Channel, AgentRole, Summary string
    SessionID int64; Detail map[string]any
}
type EventSink interface { PublishActivity(ctx context.Context, event ActivityEvent) error }

type AuditEvent struct {
    Action string; ActorUserID *int64; TargetType, TargetID string; Metadata map[string]any
}
type AuditSink interface { Record(ctx context.Context, event AuditEvent) error }

type Clock func() time.Time
```

- [ ] **Step 1: 写领域失败测试**

`profile_test.go`：
```go
func TestProfileApplyValidatesEveryField(t *testing.T) {
    p := BuiltinProfile()
    if err := p.Validate(); err != nil { t.Fatalf("builtin profile invalid: %v", err) }
    bad := -1
    if err := p.Apply(UpdateParams{MaxTurns: &bad}); err == nil { t.Fatal("max_turns=-1 must be rejected") }
    disabled, none := "disabled", "max"
    if err := p.Apply(UpdateParams{Thinking: &disabled, ReasoningEffort: &none}); err == nil {
        t.Fatal("thinking=disabled with effort=max must be rejected")
    }
}
func TestBuiltinProfileSeedValues(t *testing.T) {
    p := BuiltinProfile()
    // key/name/builtin/max_turns=12/run_seconds=600/hard=60/thinking=enabled/effort=max/max_output=32768
    // allowed tools exactly file.list,file.read,http.get,test.echo,test.fail
}
```
`run_test.go`：`TestTerminalReasonSets`（合法值集合）、`TestRunResultDefaults`（零值不 panic；`Status` 必须非空）。

- [ ] **Step 2: 跑失败**

Run: `cd backend; & ..\.tools\go\bin\go.exe test ./internal/agent/domain -v`
Expected: FAIL（包或符号不存在）。

- [ ] **Step 3: 实现 domain + ports**

按 §Interfaces 实现；`BuiltinProfile()` 的系统提示词使用规格 §5.4 原文。`Validate()` 按规格 §10.3 校验范围。

- [ ] **Step 4: 写迁移与建表测试**

创建迁移文件，内容严格使用规格 §5.1 的 DDL；`order.txt` 追加文件名。
`repository_integration_test.go`：
```go
func TestMigrateAndCreateTurnIsAtomic(t *testing.T) {
    pool := testutil.OpenTestDB(t)
    ctx := context.Background()
    // migrate twice is covered by P0; here assert the four tables exist:
    // SELECT to_regclass('agent_profiles') 等全部非空
    // 插入 profile + session，然后 CreateTurn：
    //   ids, err := runRepo.CreateTurn(ctx, session.ID, []Part{{Type:"text",Text:"hello"}}, "hello")
    //   assert ids.RunID>0, ids.UserMessageID>0
    //   再次 CreateTurn 同一 session → 错误（唯一索引），且 user message 数量仍为 1
}
func TestEnsureBuiltinAndMarkInterrupted(t *testing.T) {
    // EnsureBuiltin 两次 → created=false 第二次；内容不被覆盖
    // 插入 running run → MarkRunningInterrupted → status=interrupted、terminal_reason=interrupted、ended_at 非空
}
func TestMessagePlaceholderLifecycle(t *testing.T) {
    // AppendAssistantPlaceholder → CompleteAssistant → Get 读回 parts/reasoning/calls/usage
}
```

- [ ] **Step 5: 跑失败**

Run（先创建 `scripts/test-integration.ps1`）：
```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-integration.ps1 ./internal/agent/infrastructure/postgres -run 'TestMigrateAndCreateTurnIsAtomic|TestEnsureBuiltinAndMarkInterrupted|TestMessagePlaceholderLifecycle' -v
```
Expected: FAIL。

- [ ] **Step 6: 实现 PostgreSQL 仓储**

- 所有查询使用 `ctx`、参数化 SQL；`ErrNotFound` 映射 `pgx.ErrNoRows`。
- `CreateTurn` 使用规格 §5.5 的单条 CTE；`titlePreview` 由调用方截断；**唯一索引冲突（SQLSTATE 23505）映射为 `domain.ErrRunActive`**。
- `Active` 查 `status='running'`；`Latest` 按 id 倒序取 1。
- `List` 用 `LEFT JOIN LATERAL` 取每个 session 的最新 run 与 running run，并 **JOIN `agent_profiles` 填充 `ProfileKey`**；按 `id DESC`，`cursor>0` 时 `id < cursor`，多取 1 条判断 `hasMore`。`Get` 同样返回 `ProfileKey`。
- `Finish` 更新所有终态字段与 `ended_at=now()`；`UpdateProgress` 更新 turns/tokens。
- `AppendAssistantPlaceholder` 插入空 assistant 消息并返回 id；`CompleteAssistant` 更新 content/reasoning/tool_calls/usage。
- `EnsureBuiltin` 使用 `INSERT ... ON CONFLICT (key) DO NOTHING`，返回是否插入。
- JSONB 扫描/写入使用 `[]byte`/`json.RawMessage`；`TEXT[]` 用 `pgx` 数组。

- [ ] **Step 7: 跑通过 + 全包回归**

Run:
```powershell
cd backend; & ..\.tools\go\bin\go.exe test ./internal/agent/...
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-integration.ps1 ./internal/agent/infrastructure/postgres -v
```
Expected: PASS。

- [ ] **Step 8: 提交**

```bash
git add backend/internal/agent backend/internal/db/migrations scripts/test-integration.ps1
git commit -m "feat: add agent domain, migration, and postgres repositories"
```

---

### Task 5: 工具注册表、执行管线与测试工具

**Wave:** W2 · **并发:** 可并发（与 T7/T8/T9/T10） · **依赖:** T4
**写作用域:** `backend/internal/agent/application/tool_executor.go`、`backend/internal/agent/infrastructure/tools/test*.go`、对应 `_test.go`

**Files:**
- Create: `backend/internal/agent/application/tool_executor.go`
- Create: `backend/internal/agent/infrastructure/tools/test.go`
- Test: `backend/internal/agent/application/tool_executor_test.go`
- Test: `backend/internal/agent/infrastructure/tools/test_test.go`

**Interfaces:**
- Consumes: `domain.Tool`、`domain.ToolResult`、`domain.ToolError`、`application.LLMTool`、`AuditSink`、`AuditEvent`（T4）。
- Produces（T6/T11/T15 依赖）：
```go
type ToolRegistry struct{}
func NewToolRegistry(tools ...domain.Tool) *ToolRegistry
func (r *ToolRegistry) Get(name string) (domain.Tool, bool)
func (r *ToolRegistry) Names() []string
func (r *ToolRegistry) Specs(allowed []string) []LLMTool
func (r *ToolRegistry) ToolInfos() []domain.ToolInfo

const (
    ToolStatusOK="ok"; ToolStatusError="error"; ToolStatusTimeout="timeout"
    ToolStatusCancelled="cancelled"; ToolStatusDenied="denied"
)
type ToolCallInput struct {
    RunID, SessionID int64; ActorUserID *int64
    Call domain.ToolCall; Allowed []string
}
type ToolExecution struct {
    Result domain.ToolResult; Status string; DurationMS int64; ErrorCode string
}
type ToolExecutor struct{}
func NewToolExecutor(registry *ToolRegistry, audit AuditSink, timeout time.Duration) *ToolExecutor
func (e *ToolExecutor) Execute(ctx context.Context, in ToolCallInput) ToolExecution

func NewTestEcho() domain.Tool
func NewTestFail() domain.Tool
```

- [ ] **Step 1: 写失败测试**

```go
func TestRegistrySpecsRespectsAllowlistAndOrder(t *testing.T) {
    reg := NewToolRegistry(NewTestEcho(), NewTestFail())
    specs := reg.Specs([]string{"test.echo", "unknown", "test.fail"})
    // want names ["test.echo","test.fail"] and each Parameters is a JSON object
}

func TestExecutorDeniesUnlistedToolWithoutAuditLeak(t *testing.T) {
    // allowed=[]，call test.echo → Status=="denied"、IsError==true
    // fake audit sink: 1 entry, action=="agent.tool.invoked",
    // metadata has tool/status/duration_ms/run_id/session_id, 不含 args/result
}

func TestExecutorMapsInvalidJSONArguments(t *testing.T) {
    // call test.echo with arguments `{"text":` → Status=="error"、ErrorCode=="invalid_arguments"、IsError
}

func TestExecutorTimesOut(t *testing.T) {
    // executor timeout 20ms; test.fail mode=timeout → Status=="timeout"、ErrorCode=="timeout"
}

func TestExecutorRecoversPanic(t *testing.T) {
    // test.fail mode=panic → Status=="error"、ErrorCode=="internal"，进程不 panic
}
```

`test_test.go`：echo 回显；fail 的 error 模式返回 `IsError`；timeout 模式尊重 ctx；panic 模式 panic。

- [ ] **Step 2: 跑失败**

Run: `cd backend; & ..\.tools\go\bin\go.exe test ./internal/agent/application ./internal/agent/infrastructure/tools -v`
Expected: FAIL（符号不存在）。

- [ ] **Step 3: 实现**

- `ToolRegistry.Specs`：按 `allowed` 顺序返回已注册工具；未知名字忽略；`Schema` 原样返回。
- `ToolExecutor.Execute`：
  1. `allowed` 不含工具名 → `denied`；
  2. `json.Valid(args)` 且为对象（空 → `{}`），否则 `invalid_arguments`；
  3. `ctx, cancel := context.WithTimeout(ctx, e.timeout)`；
  4. `defer recover()` → `internal`；
  5. 调 `Tool.Execute`；错误映射：`*domain.ToolError` 用其 Code；`context.DeadlineExceeded`/超时 → `timeout`；`context.Canceled` → `cancelled`；其他 → `internal`；
  6. `ToolResult.IsError` → `error`；
  7. 审计（忽略审计错误，只记日志）：`agent.tool.invoked` + `{tool,status,duration_ms,run_id,session_id}`；
  8. 返回 `ToolExecution`。
- `test.echo` schema：`{type:object,properties:{text:{type:string}},required:["text"]}`。
- `test.fail` schema：`{type:object,properties:{mode:{type:string,enum:["error","timeout","panic"]},message:{type:string}},required:["mode"]}`；timeout 模式 `select { case <-ctx.Done(): return ..., ctx.Err(); case <-time.After(30*time.Second): }`。

- [ ] **Step 4: 跑通过**

Run: `cd backend; & ..\.tools\go\bin\go.exe test ./internal/agent/application ./internal/agent/infrastructure/tools -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/agent/application/tool_executor.go backend/internal/agent/application/tool_executor_test.go backend/internal/agent/infrastructure/tools/test.go backend/internal/agent/infrastructure/tools/test_test.go
git commit -m "feat: add agent tool registry, executor, and test tools"
```

---

### Task 6: 文件只读工具与 HTTP 只读工具

**Wave:** W2 · **并发:** 串行（依赖 T5；与 T7/T8/T9/T10 可并行，但本任务在 T5 后启动） · **依赖:** T5
**写作用域:** `backend/internal/agent/infrastructure/tools/file*.go`、`http*.go`

**Files:**
- Create: `backend/internal/agent/infrastructure/tools/file.go`
- Create: `backend/internal/agent/infrastructure/tools/http.go`
- Test: `backend/internal/agent/infrastructure/tools/file_test.go`
- Test: `backend/internal/agent/infrastructure/tools/http_test.go`

**Interfaces:**
- Consumes: `domain.Tool`、`domain.ToolError`、`domain.ToolResult`。
- Produces（T15 依赖）：
```go
// 空 root 返回 (nil, nil)：文件工具不注册
func NewFileTools(root string) ([]domain.Tool, error)
func NewHTTPGet(allowlist []string, timeout time.Duration) domain.Tool
```

- [ ] **Step 1: 写文件工具失败测试**

```go
func TestFileReadRejectsEscapesAndSecrets(t *testing.T) {
    root := t.TempDir()
    // 正常文件 → 读到内容
    // ../escape、绝对路径、.env、.secrets/x、foo.local.md → IsError 或 error
    // 1MB 文件 → too_large；含 NUL 的文件 → 二进制拒绝
}
func TestFileListStopsAtDeniedEntries(t *testing.T) {
    // root 下建 a.txt、.env、sub/b.txt
    // file.list depth=2 → 只返回 a.txt 与 sub，sub 内 b.txt；不含 .env
}
func TestFileToolsUnavailableWithoutRoot(t *testing.T) {
    tools, err := NewFileTools("")
    // tools == nil && err == nil
}
```

- [ ] **Step 2: 跑失败**

Run: `cd backend; & ..\.tools\go\bin\go.exe test ./internal/agent/infrastructure/tools -run TestFile -v`
Expected: FAIL。

- [ ] **Step 3: 实现文件工具**

- 构造时 `filepath.Abs(root)` + `filepath.EvalSymlinks`；不是目录 → error。
- 每次读取：`filepath.Abs(join(root, rel))` → `EvalSymlinks` → `strings.HasPrefix(resolved, rootResolved+separator)`。
- Deny：路径段 `.git`/`.tools`/`.data`/`.tmp`/`node_modules`/`.next`/`.secrets`；文件名 `.env`/`.env.*`/`*.local.md`。
- `file.read`：≤256KB；`utf8.Valid`；前 8KB 含 NUL → 拒绝；返回带 `path (lines a-b of n)` 头的文本。
- `file.list`：depth 1..3，条目 ≤200，返回 JSON 文本 `[{name,type,size}]`。
- 错误用 `domain.ToolError{Code: "not_found"|"too_large"|"invalid_arguments"|"denied"}`。

- [ ] **Step 4: 写 HTTP 工具失败测试**

```go
func TestHTTPGetAllowlistAndRedirects(t *testing.T) {
    allowed := httptest.NewServer(...200 "ok"...)
    blocked := httptest.NewServer(...200 "secret"...)
    // allowlist 只含 allowed 的 127.0.0.1:port
    // 直接 GET blocked → denied
    // redirect allowed → blocked → denied（每跳校验）
}
func TestHTTPGetCapsBodyAndRejectsBinary(t *testing.T) {
    // 2MB text/html → 截断到 1MB；application/octet-stream → unsupported_content
}
func TestHTTPGetRejectsCredentialsAndNonGET(t *testing.T) {
    // url 含 user:pass@ → invalid_arguments
}
```

- [ ] **Step 5: 跑失败 → 实现 → 跑通过**

Run: `cd backend; & ..\.tools\go\bin\go.exe test ./internal/agent/infrastructure/tools -v`
Expected: 先 FAIL，后 PASS。

实现要点：
- 仅 `http`/`https`；`url.User != nil` 拒绝；host（含白名单项带端口时）逐项匹配。
- `http.Client{Timeout: timeout}`；`CheckRedirect` 超过 3 跳或目标 host 不在白名单 → 错误。
- 请求不带任何 Authorization/Cookie；响应 Content-Type 非文本类 → `unsupported_content`。
- 读取 `io.LimitReader(body, 1MB+1)`；超过则截断并加 `...[truncated]`。
- Schema：`{url: string}` required。

- [ ] **Step 6: 提交**

```bash
git add backend/internal/agent/infrastructure/tools/file.go backend/internal/agent/infrastructure/tools/file_test.go backend/internal/agent/infrastructure/tools/http.go backend/internal/agent/infrastructure/tools/http_test.go
git commit -m "feat: add read-only file and http agent tools"
```

---

### Task 7: 上下文构建与截断

**Wave:** W2 · **并发:** 可并发（与 T5/T8/T9/T10） · **依赖:** T4
**写作用域:** `backend/internal/agent/application/context_builder*.go`

**Files:**
- Create: `backend/internal/agent/application/context_builder.go`
- Test: `backend/internal/agent/application/context_builder_test.go`

**Interfaces:**
- Consumes: `domain.Message`、`domain.Part`、`domain.ToolCall`、`application.LLMMessage`（T4）、`domain.ErrContextOverflow`。
- Produces（T11 依赖）：
```go
type ContextBuilder struct{}
func NewContextBuilder() *ContextBuilder
type BuildResult struct {
    Messages []LLMMessage
    DroppedMessages int
}
func (b *ContextBuilder) Build(systemPrompt string, history []domain.Message, budgetChars int) (BuildResult, error)
func EstimateChars(messages []LLMMessage) int
```

- [ ] **Step 1: 写失败测试**

```go
func TestBuildConvertsHistoryWithReasoningAndTools(t *testing.T) {
    // history: user → assistant(reasoning+tool_calls) → tool → assistant(text)
    // 断言输出首条 system；assistant 带 reasoning_content/tool_calls；tool 带 tool_call_id
}
func TestBuildDropsOldestGroupsAndKeepsLatestUser(t *testing.T) {
    // 3 组对话，budget 只够 system+最后一组
    // 断言 DroppedMessages>0；输出仍含最新 user；不含最旧 user
}
func TestBuildKeepsToolCallPairing(t *testing.T) {
    // 旧组含 assistant(tool_calls)+tool；断言两者一起被丢弃，不会只剩 tool
}
func TestBuildFailsWhenLatestUserAloneExceedsBudget(t *testing.T) {
    // budget 很小 → errors.Is(err, domain.ErrContextOverflow)
}
func TestEstimateCharsCountsTextReasoningAndArguments(t *testing.T) {
    // 精确值按公式：sum(len(parts text)+len(reasoning)+len(tool args))+16*n
}
```

- [ ] **Step 2: 跑失败**

Run: `cd backend; & ..\.tools\go\bin\go.exe test ./internal/agent/application -run TestBuild -v`
Expected: FAIL。

- [ ] **Step 3: 实现**

- 分组：遇到 `role=user` 开新组；assistant/tool 归入当前组；无 user 的前缀（异常数据）单独成组。
- 预算：`system + 最新组` 超限 → `domain.ErrContextOverflow`；否则从最旧组开始丢弃直到 `EstimateChars ≤ budgetChars`。
- 输出：`[system] + 保留组的所有消息`；assistant 的 `Reasoning` 非空才写入 `ReasoningContent`；tool 消息带 `ToolCallID`。
- `EstimateChars` 按测试公式实现。

- [ ] **Step 4: 跑通过 + 全 application 回归**

Run: `cd backend; & ..\.tools\go\bin\go.exe test ./internal/agent/application -v`
Expected: PASS（含 T5 测试）。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/agent/application/context_builder.go backend/internal/agent/application/context_builder_test.go
git commit -m "feat: add agent context builder with whole-turn truncation"
```

---

### Task 8: 前端 Agent 控制台壳、会话列表与 API 客户端

**Wave:** W2 · **并发:** 可并发（与 T5/T7/T9/T10） · **依赖:** T1（接口契约以规格 §10 为准）
**写作用域:** `frontend/src/features/agent-console/`、`frontend/src/app/(app)/agents/page.tsx`、`frontend/src/navigation/nav-items.ts`、`frontend/src/navigation/nav-items.test.ts`

**Files:**
- Create: `frontend/src/features/agent-console/agent-api.ts`
- Create: `frontend/src/features/agent-console/agent-api.test.ts`
- Create: `frontend/src/features/agent-console/AgentConsole.tsx`
- Create: `frontend/src/features/agent-console/AgentConsole.test.tsx`
- Create: `frontend/src/features/agent-console/SessionList.tsx`
- Create: `frontend/src/features/agent-console/SessionList.test.tsx`
- Create: `frontend/src/features/agent-console/SessionPane.tsx`（占位，T12 替换）
- Create: `frontend/src/features/agent-console/ConfigPane.tsx`（占位，T13 替换）
- Modify: `frontend/src/app/(app)/agents/page.tsx`
- Modify: `frontend/src/navigation/nav-items.ts`
- Modify: `frontend/src/navigation/nav-items.test.ts`

**Interfaces:**
- Consumes: `apiGet`/`apiPost`/`apiPatch`/`apiDelete`（`@/lib/api-client`）、`EmptyState`、`StatusBadge`、Radix Tabs（已安装）。
- Produces（T12/T13/T16 依赖）：
```ts
export type AgentRun = { id:number; session_id:number; status:"running"|"done"|"failed"|"aborted"|"interrupted";
  terminal_reason:string|null; turns:number; input_tokens:number; output_tokens:number; reasoning_tokens:number;
  abort_code:string|null; error_code:string|null; last_tool_name:string|null; last_tool_pending:boolean;
  started_at:string; ended_at:string|null }
export type AgentSession = { id:number; profile_id:number; profile_key:string; title:string;
  created_by:number|null; running:boolean; active_run:AgentRun|null; last_run:AgentRun|null;
  created_at:string; updated_at:string; last_activity_at:string }
export type AgentProfile = { id:number; key:string; name:string; description:string; builtin:boolean; enabled:boolean;
  system_prompt:string; model:string; allowed_tools:string[]; max_turns:number; run_seconds:number;
  hard_timeout_seconds:number; thinking:"auto"|"enabled"|"disabled";
  reasoning_effort:""|"none"|"low"|"high"|"max"; max_output_tokens:number;
  created_at:string; updated_at:string }
export type AgentTool = { name:string; description:string; schema:unknown }
export type AgentUsage = { prompt_tokens:number; completion_tokens:number; reasoning_tokens:number; total_tokens:number }
export type AgentMessageDetail = { id:number; session_id:number; run_id:number|null;
  role:"system"|"user"|"assistant"|"tool";
  content:{ type:string; text?:string; image_url?:string }[];
  reasoning:string; tool_calls:{ id:string; name:string; arguments:string }[]|null;
  tool_call_id:string|null; tool_name:string|null; usage:AgentUsage|null; created_at:string }
export type AgentEvent = { seq:number; channel:string; type:string; ts:string; summary:string;
  agent_role?:string|null; session_id?:string|null; detail:Record<string,unknown> }
export type ActivitiesPage = { items:AgentEvent[]; snapshot_cursor:number; has_more:boolean }

export type ProfilePatch = Partial<Pick<AgentProfile,
  "system_prompt"|"model"|"allowed_tools"|"max_turns"|"run_seconds"|"hard_timeout_seconds"|
  "thinking"|"reasoning_effort"|"max_output_tokens"|"enabled">>

export function listProfiles(): Promise<AgentProfile[]>
export function updateProfile(id:number, patch:ProfilePatch): Promise<AgentProfile>
export function listTools(): Promise<AgentTool[]>
export function listSessions(params?:{limit?:number; cursor?:string}): Promise<{ items:AgentSession[]; has_more:boolean; next_cursor:string|null }>
export function createSession(input:{profile_id?:number; title?:string}): Promise<AgentSession>
export function getSession(id:number): Promise<AgentSession>
export function updateSessionTitle(id:number, title:string): Promise<AgentSession>
export function deleteSession(id:number): Promise<void>
export function sendMessage(id:number, message:string): Promise<{ run_id:number; status:string }>
export function stopSession(id:number): Promise<{ status:"stopping"|"idle" }>
export function getMessage(sessionId:number, messageId:number): Promise<AgentMessageDetail>
export function fetchActivities(sessionId:number, params?:{ before_seq?:number; limit?:number }): Promise<ActivitiesPage>
```

组件 props：
```ts
// SessionList
{ sessions: AgentSession[]; selectedId: number | null; loading: boolean; hasMore: boolean;
  onSelect: (id:number)=>void; onCreate: ()=>void; onLoadMore: ()=>void }
// SessionPane（T12 实现）
{ sessionId: number | null; session: AgentSession | null }
// ConfigPane（T13 实现）
{ profiles: AgentProfile[]; onSaved: (p:AgentProfile)=>void }
```

- [ ] **Step 1: 写 `agent-api` 失败测试**

用 `vi.stubGlobal("fetch", ...)` 断言：
- `listSessions({limit:20})` → GET `/api/agents/sessions?limit=20`，返回 `{data:{items:[...],has_more:false,next_cursor:null}}` 解包正确；
- `createSession({})` → POST `/api/agents/sessions`；
- `sendMessage(1,"hi")` → POST `/api/agents/sessions/1/messages` body `{"message":"hi"}`；
- `stopSession(1)` → POST `/api/agents/sessions/1/stop`；
- `fetchActivities(1,{before_seq:5,limit:20})` → GET `/api/stream/events?channel=agent%3A1&before_seq=5&limit=20`，返回 `{data:{items,snapshot_cursor,has_more}}`；
- 403 抛 `ApiError`。

- [ ] **Step 2: 跑失败**

Run: `cd frontend; pnpm vitest run src/features/agent-console/agent-api.test.ts`
Expected: FAIL。

- [ ] **Step 3: 实现 `agent-api.ts`**

- 全部经 `apiGet/apiPost/apiPatch/apiDelete`；路径按 §Interfaces；`encodeURIComponent("agent:"+id)` 或直接模板字符串（channel 含冒号，拼接时用 `encodeURIComponent`）。
- `listSessions` 返回 `{data:{...}}` 的 `data` 对象。

- [ ] **Step 4: 写 `AgentConsole` / `SessionList` 失败测试**

```tsx
it("renders sessions and the admin-only state on 403", async () => {
  // mock agent-api: listProfiles rejects ApiError(403)
  // render <AgentConsole/> → await "仅管理员可用"
})
it("creates and selects a session", async () => {
  // mock listProfiles → [profile]; listSessions → {items:[],...}
  // click "新建会话" → createSession called → 新会话被选中
})
```
`SessionList`：running 会话显示「运行中」点；点击触发 `onSelect`；空列表显示空状态。

- [ ] **Step 5: 跑失败**

Run: `cd frontend; pnpm vitest run src/features/agent-console/AgentConsole.test.tsx src/features/agent-console/SessionList.test.tsx`
Expected: FAIL。

- [ ] **Step 6: 实现控制台壳**

- `AgentConsole`：挂载时并行 `listProfiles`/`listSessions`；403 → 管理员专属 `EmptyState`；`?session=` 从 `window.location.search` 读取（`useEffect`，不用 `useSearchParams`），选择变化用 `window.history.replaceState` 写回；Radix Tabs「会话 / 配置」；会话 Tab 渲染 `<SessionPane …/>`，配置 Tab 渲染 `<ConfigPane …/>`。
- `SessionList`：新建按钮、列表、running 脉冲、分页「加载更早」、删除按钮（调用 `deleteSession` 后刷新）。
- **可访问名称（E2E 依赖，不得随意改）**：页面标题 `Agent 运行时控制台`；新建按钮 `新建会话`；删除按钮 `删除会话`；会话条目按钮名为会话标题；Tab 名 `会话`、`配置`；空状态文案 `仅管理员可用`。
- `SessionPane`：占位文案「选择或新建会话」；`ConfigPane`：占位文案「配置在后续任务接入」。
- `page.tsx`：`"use client"` + `<AgentConsole />`；移除 P8 占位。
- `nav-items.ts`：`/agents` 的 `phase` 改 `P1`，hint 改「Agent 运行时控制台（P8 扩展为 Agent 中心）」；`nav-items.test.ts` 增断言 `NAV_ITEMS.find(i=>i.href==="/agents")?.phase === "P1"`。

- [ ] **Step 7: 跑通过 + lint/typecheck**

Run:
```powershell
cd frontend; pnpm vitest run src/features/agent-console src/navigation/nav-items.test.ts
cd frontend; pnpm lint; pnpm typecheck
```
Expected: PASS。

- [ ] **Step 8: 提交**

```bash
git add frontend/src/features/agent-console frontend/src/app/\(app\)/agents/page.tsx frontend/src/navigation
git commit -m "feat: add agent console shell and session list"
```

---

### Task 9: E2E 确定性 fake LLM 上游与脚本接线

**Wave:** W2 · **并发:** 可并发（与 T5/T7/T8/T10） · **依赖:** T1（wire 契约以 T3 规格为准）
**写作用域:** `scripts/fake-llm.cjs`、`scripts/e2e.ps1`

**Files:**
- Create: `scripts/fake-llm.cjs`
- Modify: `scripts/e2e.ps1`
- Test: 无独立测试文件；用下列 smoke 命令验证。

**Interfaces:**
- Consumes: OpenAI 兼容 wire（`POST /v1/chat/completions`、`GET /v1/models`）。
- Produces: E2E 上游；请求日志 `.tmp/fake-llm-requests.jsonl`。

- [ ] **Step 1: 实现 fake 上游**

Node `http` 服务（无新依赖），端口 `process.env.EZTEST_FAKE_LLM_PORT || 13334`：
- `GET /v1/models` → `{"data":[{"id":"deepseek-flash","context_window":1000000,"max_output_tokens":131072,"input_modalities":["text","image"]}]}`。
- `POST /v1/chat/completions`：
  - 读取 JSON；`tools` 必须存在且非空，否则 400；
  - 记录请求体到 `.tmp/fake-llm-requests.jsonl`（每行 JSON）；
  - 最后一条消息 `role=="user"`：
    - 内容含 `SLOW` → 流式 20 个文本分片，每片间隔 300ms；
    - 内容含 `FAILTOOL` → 返回 `test.fail` 工具调用；
    - 内容含 `ECHO_SYSTEM` → 直接返回最终文本 `system=<system prompt 前 80 字符>`（用于配置生效断言）；
    - 否则 → 返回 `reasoning_content`（"需要读取 README"）+ `file.read` 工具调用；
  - 最后一条消息 `role=="tool"`：
    - 历史中必须存在带非空 `reasoning_content` 的 assistant 消息，否则 400 `{"error":{"message":"missing reasoning_content"}}`；
    - 返回最终文本（含 "诊断完成"）；
  - 流式与非流式都支持；流式分片：
    `data: {"choices":[{"delta":{"reasoning_content":"..."},"finish_reason":null}]}`
    `data: {"choices":[{"delta":{"tool_calls":[{index:0,id:"call_1",type:"function",function:{name:"file.read",arguments:"{\"path\":"}}]}}]}`
    … `finish_reason:"tool_calls"` + `usage`；最后 `data: [DONE]`。
- 客户端断开时停止写入（`res.on("close")`）。

- [ ] **Step 2: 更新 `scripts/e2e.ps1`**

- 顶部端口区加 `$fakeLlmPort = 13334`，日志 `.tmp/e2e-fake-llm.*.log`。
- 在启动 API 之前启动 fake：`Start-Process node -ArgumentList "`"scripts/fake-llm.cjs`"" -WorkingDirectory $repoRoot -PassThru`，`Wait-ForHttp "http://127.0.0.1:$fakeLlmPort/v1/models"`。
- 在现有 `foreach ($key in @(...))` 之后覆盖：
  ```powershell
  $env:llm_base_url = "http://127.0.0.1:$fakeLlmPort"
  $env:llm_key = "e2e-test"
  $env:llm_model_name = "deepseek-flash"
  $env:EZTEST_AGENT_FILE_ROOT = $repoRoot
  ```
- `finally` 中 `Stop-ProcessTree -Process $fakeProcess`，并把 `$fakeLlmPort` 加入端口清理检查。

- [ ] **Step 3: smoke 验证**

Run:
```powershell
$p = Start-Process node -ArgumentList "scripts/fake-llm.cjs" -WorkingDirectory (Get-Location) -PassThru
Start-Sleep -Seconds 1
$models = Invoke-RestMethod -Uri "http://127.0.0.1:13334/v1/models"
$body = @{ model="deepseek-flash"; messages=@(@{role="user";content="检查"}); tools=@(@{type="function";function=@{name="file.read";description="read";parameters=@{type="object";properties=@{path=@{type="string"}}}}}) ; stream=$false } | ConvertTo-Json -Depth 10
$r1 = Invoke-RestMethod -Uri "http://127.0.0.1:13334/v1/chat/completions" -Method Post -ContentType "application/json" -Body $body
$r1.choices[0].message.tool_calls.Count  # expect 1
Stop-Process -Id $p.Id -Force
```
Expected: `models.data[0].id == "deepseek-flash"`；`tool_calls` 为 1；`.tmp/fake-llm-requests.jsonl` 存在。

- [ ] **Step 4: 提交**

```bash
git add scripts/fake-llm.cjs scripts/e2e.ps1
git commit -m "test: add deterministic fake llm upstream for agent e2e"
```

---

### Task 10: Playwright 最小执行 + storageState 注入 spike

**Wave:** W2 · **并发:** 可并发（与 T5/T7/T8/T9） · **依赖:** 无（P0 服务可运行即可）
**写作用域:** `frontend/scripts/`

**Files:**
- Create: `frontend/scripts/spike-playwright-session.mjs`

**Interfaces:**
- Consumes: `@playwright/test` 的 `chromium`；环境变量 `EZTEST_E2E_BASE_URL`、`EZTEST_E2E_CHANNEL`、`EZTEST_E2E_ADMIN_USERNAME`、`EZTEST_E2E_ADMIN_PASSWORD`。
- Produces: `.tmp/spike-storage-state.json`、`.tmp/spike-shell.png`、结构化 stdout。

- [ ] **Step 1: 写 spike 脚本**

流程：
1. `chromium.launch({channel: process.env.EZTEST_E2E_CHANNEL || "chrome", headless: true})`；
2. `page.goto(baseURL + "/login")`；
3. `getByLabel("用户名")` / `getByLabel("密码")` 填入；`getByRole("button",{name:"登录",exact:true}).click()`；
4. `await expect` 或等待 `getByRole("heading",{name:"概览"})` 可见（用 `page.waitForSelector` 或 `expect` 均可；脚本用 `page.getByRole(...).waitFor()`）；
5. `context.storageState({path:"../.tmp/spike-storage-state.json"})`（相对 `frontend/` 解析）；
6. 新建 context 注入 storageState，访问 `/` 与 `/agents`，断言用户菜单按钮 `getByRole("button",{name:"打开用户菜单"})` 可见；
7. 截图 `../.tmp/spike-shell.png`；
8. `console.log("SPIKE-PW OK ...")`；失败时 `process.exitCode=1` 并输出错误。

- [ ] **Step 2: 运行**

前置：`scripts/dev-api.ps1` 与 `scripts/dev-web.ps1` 已启动；在 PowerShell 中导入 `.env` 的 admin 凭据后：

```powershell
cd frontend
$env:EZTEST_E2E_BASE_URL = "http://localhost:6868"
$env:EZTEST_E2E_CHANNEL = "chrome"
$env:EZTEST_E2E_ADMIN_USERNAME = (Get-Content ..\.env | Where-Object { $_ -match '^EZTEST_ADMIN_USERNAME=' } | ForEach-Object { ($_ -split '=',2)[1].Trim() })
$env:EZTEST_E2E_ADMIN_PASSWORD = (Get-Content ..\.env | Where-Object { $_ -match '^EZTEST_ADMIN_PASSWORD=' } | ForEach-Object { ($_ -split '=',2)[1].Trim() })
node scripts/spike-playwright-session.mjs
```
Expected: `SPIKE-PW OK`；`.tmp/spike-shell.png` 与 `.tmp/spike-storage-state.json` 存在。

- [ ] **Step 3: 提交**

```bash
git add frontend/scripts/spike-playwright-session.mjs
git commit -m "test: add playwright session-injection spike"
```

---

### Task 11: Run 服务与执行循环（核心）

**Wave:** W3 · **并发:** 可并发（与 T12/T13/T14） · **依赖:** T3、T4、T5、T6、T7
**写作用域:** `backend/internal/agent/application/run_service*.go`、`runner*.go`、`activity*.go`

**Files:**
- Create: `backend/internal/agent/application/run_service.go`
- Create: `backend/internal/agent/application/runner.go`
- Create: `backend/internal/agent/application/activity.go`
- Test: `backend/internal/agent/application/run_service_test.go`
- Test: `backend/internal/agent/application/activity_test.go`

**Interfaces:**
- Consumes: T4 仓储与端口；T7 `ContextBuilder`；T5 `ToolRegistry`/`ToolExecutor`；T3 的 LLM 端口类型（经 `LLMRuntime`/`LLMCaller`）。
- Produces（T14/T15 依赖）：
```go
type RunServiceDeps struct {
    Profiles domain.ProfileRepository
    Sessions domain.SessionRepository
    Runs     domain.RunRepository
    Messages domain.MessageRepository
    LLM      LLMRuntime
    Context  *ContextBuilder
    Tools    *ToolRegistry
    Executor *ToolExecutor
    Events   EventSink
    Audit    AuditSink
    Clock    Clock
    MaxConcurrentRuns int
    HardTimeout       time.Duration
}
func NewRunService(deps RunServiceDeps) *RunService
func (s *RunService) Start(ctx context.Context, sessionID int64, message string, actor *int64) (domain.StartResult, error)
func (s *RunService) Abort(ctx context.Context, sessionID int64, actor *int64) (domain.AbortResult, error)
func (s *RunService) WaitIdle(ctx context.Context, sessionID int64, timeout time.Duration) bool
func (s *RunService) Shutdown(ctx context.Context)
func (s *RunService) RecoverInterrupted(ctx context.Context) (int64, error)
```

- [ ] **Step 1: 写 activity 分片失败测试**

```go
func TestSegmentBufferFlushesOnIntervalOrSize(t *testing.T) {
    // 1500ms 间隔或 1024 runes 触发 final=false；片段结束 final=true
    // detail 含 kind/run_id/message_id/part/final/text，summary 为前 200 字
}
```
实现 `activity.go`：
```go
type segmentBuffer struct { kind string; messageID, runID int64; part int; buf []rune; lastFlush time.Time }
func newSegmentBuffer(kind string, runID, messageID int64, now time.Time) *segmentBuffer
func (b *segmentBuffer) Append(delta string)
func (b *segmentBuffer) ShouldFlush(now time.Time) bool   // len≥1024 或 now-lastFlush≥1500ms
func (b *segmentBuffer) Flush(now time.Time, final bool) (text string, part int, ok bool)
type activityEmitter struct{}
func newActivityEmitter(sink EventSink, sessionID int64, role string) *activityEmitter
func (e *activityEmitter) emit(ctx context.Context, runID int64, kind, summary string, detail map[string]any) error
```
`emit` 设置 `ChannelForSession`、summary 截断 500、detail 中所有字符串按调用方上限截断；发布失败只 log。

- [ ] **Step 2: 跑 activity 测试**

Run: `cd backend; & ..\.tools\go\bin\go.exe test ./internal/agent/application -run TestSegmentBuffer -v`
Expected: 先 FAIL，实现后 PASS。

- [ ] **Step 3: 写 run service 失败测试（关键路径）**

```go
func TestStartRunsToolLoopAndEmitsOrderedActivities(t *testing.T) {
    // fake LLM: turn1 reasoning+"我读取 README" + tool_call(test.echo)；turn2 文本"诊断完成"
    // 断言：run done/completed、turns=2
    // 消息顺序：user, assistant(tool_calls+reasoning), tool, assistant
    // 第二回合请求的 assistant 消息 ReasoningContent 非空  ← Review Focus #1
    // 事件 kind 顺序包含 run_started, thinking, text, tool_use, tool_result, usage, run_finished
}
func TestAbortDuringStreamKeepsPartialOutput(t *testing.T) {
    // fake stream 阻塞；Abort()；断言 aborted/aborted_streaming、abort_code=user_abort
    // assistant 消息保留已生成部分文本；run_finished 事件存在；WaitIdle 返回 true
}
func TestAbortDuringToolUsesAbortedTools(t *testing.T) {
    // tool 阻塞到 ctx.Done；Abort()；断言 aborted/aborted_tools
}
func TestMaxTurnsEmitsBudgetStop(t *testing.T) {
    // max_turns=1，fake 每次返回 tool_call；断言 done/max_turns + decision(budget_stop)
}
func TestToolErrorDoesNotFailRun(t *testing.T) {
    // 工具返回 IsError；下一回合终答；断言 done/completed 且 tool_result.status=="error"
}
func TestStartMapsNotConfiguredAndProtocol(t *testing.T) {
    // fake runtime 返回 ErrLLMNotConfigured / ErrToolsUnsupported → Start 原样返回
}
func TestStartCapacityAndRunActive(t *testing.T) {
    // MaxConcurrentRuns=1，两个并发 Start → 第二个 ErrCapacity
    // 同一会话第二次 Start → domain.ErrRunActive
}
func TestRecoverInterrupted(t *testing.T) {
    // fake repo 返回 2 → RecoverInterrupted 返回 2
}
```

- [ ] **Step 4: 跑失败**

Run: `cd backend; & ..\.tools\go\bin\go.exe test ./internal/agent/application -run 'TestStart|TestAbort|TestMaxTurns|TestToolError|TestRecover' -v`
Expected: FAIL。

- [ ] **Step 5: 实现 `RunService`**

- 构造 `rootCtx, rootCancel := context.WithCancelCause(context.Background())`；`sem chan struct{}` 容量 `MaxConcurrentRuns`；`cancel map[int64]context.CancelCauseFunc` + `wg sync.WaitGroup` + mutex。
- `Start`：
  1. `strings.TrimSpace(message)==""` → `domain.ErrEmptyMessage`；
  2. `Sessions.Get` → 不存在 `domain.ErrNotFound`；
  3. `Profiles.Get` → `!Enabled` → `domain.ErrProfileDisabled`；
  4. `LLM.Prepare(ctx, profile.Model)` → 原样返回 `ErrLLMNotConfigured`/`ErrToolsUnsupported`；
  5. 非阻塞取 sem → 满 `domain.ErrCapacity`；
  6. `Runs.CreateTurn`（repo 将唯一索引冲突映射为 `domain.ErrRunActive`）；
  7. 发布 `user_message` 活动（失败仅 log）；
  8. `runCtx, cancel := context.WithCancelCause(rootCtx)`；注册；`wg.Add(1)`；
  9. goroutine：`defer` 释放 sem/注销/wg.Done；调用 `run(...)`；
  10. 返回 `domain.StartResult`。
- `Abort`：查 cancel；无 → `{Status:"idle"}`；有 → `cancel(&abortCause{code:"user_abort"})`、写审计 `agent.run.aborted`、`{Status:"stopping"}`。
- `Shutdown`：`rootCancel(&abortCause{code:"shutdown"})`；在 ctx 到期前 `wg.Wait()`。
- `WaitIdle`：轮询 cancel map（20ms）直到消失或超时。
- `RecoverInterrupted`：调用 repo 方法。

- [ ] **Step 6: 实现 `runner.go`**

```go
func (s *RunService) run(ctx context.Context, runID int64, session domain.Session, profile domain.AgentProfile, caller LLMCaller, actor *int64)
```
- 记录 `startedAt`；`softDeadline = startedAt.Add(run_seconds)`；`hardCtx, cancelHard := context.WithTimeoutCause(ctx, run_seconds+hard_timeout, &abortCause{code:"run_hard_timeout"})`（`HardTimeout` 来自 deps，默认 60s）。
- `history, _ := Messages.ListBySession`；`turns := 0`；`stage := "between"`。
- 循环：
  1. `if cause := context.Cause(hardCtx); cause != nil` → 终态（按 stage：streaming→aborted_streaming，tools→aborted_tools，否则 aborted；code 从 `*abortCause` 取；`run_hard_timeout` → `run_hard_timeout`；shutdown → `shutdown`）。
  2. `if profile.MaxTurns>0 && turns >= profile.MaxTurns` → `finish(done, max_turns)` + decision(budget_stop)。
  3. `if s.now().After(softDeadline)` → `finish(done, timeout)` + decision(budget_stop)。
  4. `budgetChars := caller.ContextWindowTokens() - max(2048, w/4) - 2000`；`built, err := Context.Build(profile.SystemPrompt, history, budgetChars)`；`domain.ErrContextOverflow` → `finish(failed, context_overflow)`；`DroppedMessages>0` → decision(context_truncated)。
  5. `assistantID, _ := Messages.AppendAssistantPlaceholder`；构造请求：
     ```go
     req := LLMRequest{
         Model: caller.Model(), Messages: built.Messages,
         Tools: Tools.Specs(profile.AllowedTools), MaxTokens: profile.MaxOutputTokens,
         Thinking: profile.Thinking, ReasoningEffort: profile.ReasoningEffort,
     }
     ```
     `stage="streaming"`；`stream, err := caller.ChatStream(hardCtx, req)`；错误 → `failed/model_error`（用 `llmErrorCode(err)`）。
  6. 读流：累加 text/reasoning；用 `segmentBuffer` 按 1500ms/1024 runes 发 thinking/text（`select` 同时监听 `time.NewTicker(250ms)` 与 stream）；收到 `Done` 记录 calls/usage/finish；`Err` → 失败/中止。
  7. `Messages.CompleteAssistant(assistantID, parts, reasoning, calls, usage)`；flush 两个 buffer（final=true）；更新 `history`。**无论 Done、Err 还是 ctx 取消，都必须用已累计内容调用 `CompleteAssistant`**，保留部分输出。
  8. 有 usage → 发 `usage` 活动；`Runs.UpdateProgress`。
  9. `turns++`。
  10. 无 calls：`finishReason=="length"` → `failed/model_error`；否则 `done/completed`。
  11. 对每个 call：`stage="tools"`；发 `tool_use`；`Executor.Execute(ctx, ToolCallInput{RunID: runID, SessionID: session.ID, ActorUserID: actor, Call: call, Allowed: profile.AllowedTools})`；`Messages.Append(tool)`；发 `tool_result`；更新 `history`；若 ctx 取消 → 终态 aborted_tools。
  12. 继续下一回合。
- `finish`：`Runs.Finish(runID, domain.RunResult{...})`，然后发 `run_finished`（detail 含 status/reason/turns/duration/usage/abort_code/last_tool）。
- 任何 repo/event 错误：记录 log；终态写库失败则 log error。

- [ ] **Step 7: 跑通过 + race**

Run:
```powershell
cd backend; & ..\.tools\go\bin\go.exe test ./internal/agent/application -race -v
```
Expected: PASS（无 data race）。

- [ ] **Step 8: 提交**

```bash
git add backend/internal/agent/application/run_service.go backend/internal/agent/application/runner.go backend/internal/agent/application/activity.go backend/internal/agent/application/run_service_test.go backend/internal/agent/application/activity_test.go
git commit -m "feat: add agent run service and tool loop"
```

---

### Task 12: 前端 transcript、SSE 合并、composer 与中止

**Wave:** W3 · **并发:** 可并发（与 T11/T13/T14） · **依赖:** T8
**写作用域:** `frontend/src/features/agent-console/`（仅 `Transcript*`、`ActivityItem*`、`ToolCallBlock*`、`ThinkingBlock*`、`Composer*`、`SessionPane*`、`useAgentSession*` 及对应测试）

**Files:**
- Create: `Transcript.tsx`、`ActivityItem.tsx`、`ToolCallBlock.tsx`、`ThinkingBlock.tsx`、`Composer.tsx`、`useAgentSession.ts`
- Replace: `SessionPane.tsx`
- Test: `useAgentSession.test.tsx`、`Transcript.test.tsx`、`Composer.test.tsx`、`SessionPane.test.tsx`

**Interfaces:**
- Consumes: T8 `agent-api`、`@/lib/sse-client`、`@/lib/auth-storage`。
- Produces（T13/T16 依赖的 UI 语义）：
```ts
export function useAgentSession(sessionId: number | null, session: AgentSession | null): {
  activities: AgentEvent[]; status: SSEStatus; loading: boolean; error: string
  running: boolean
  hasMore: boolean; loadEarlier: () => Promise<void>; refresh: () => Promise<void>
  getDetail: (messageId: number) => Promise<AgentMessageDetail>
}
// SessionPane props 沿用 T8：{ sessionId, session }
```
- 可访问名称（E2E 依赖，不得随意改）：`发送消息`（textarea aria-label）、`发送`、`停止`、`加载更早`、`思考`（折叠按钮）、`工具 <name>`、`运行中`、`已中止`、`已完成`。

- [ ] **Step 1: 写 `useAgentSession` 失败测试**

Mock `@/lib/sse-client` 的 `connectSSE` 返回 stop 函数并捕获 `onEvent/onStatus`；mock `agent-api`。
```tsx
it("merges snapshot and live activities with seq dedupe", async () => {
  // snapshot: seq 1,2; live: seq 2 duplicate + seq 3; assert [1,2,3]
})
it("refetches on stream reset and exposes reconnect status", async () => {
  // onReset → fetchActivities 再调用一次；onStatus("reconnecting") → status
})
```
- [ ] **Step 2: 跑失败**

Run: `cd frontend; pnpm vitest run src/features/agent-console/useAgentSession.test.tsx`
Expected: FAIL。

- [ ] **Step 3: 实现 `useAgentSession`**

- `sessionId` 变化：清空、`fetchActivities(id)`、`connectSSE({url: resolveApiUrl("/api/stream?channel="+encodeURIComponent("agent:"+id)), token: getToken(), lastEventId: snapshot_cursor, onEvent, onStatus, onReset})`。
- `onEvent`：解析 `data` JSON；只接受 `type==="agent.activity"` 且 `seq` 数字；按 seq 去重排序；`run_started` 置 `running=true`，`run_finished` 置 `running=false` 并调用 `refresh()`。
- `running` 初值取 `session?.running ?? false`。
- 卸载/session 变化时调用 stop。
- `loadEarlier`：`before_seq=activities[0].seq`，结果前插并 dedupe。
- `getDetail`：缓存 `Map<number, AgentMessageDetail>`。
- 401/403：设置 error 文案；403 → 「仅管理员可用」。

- [ ] **Step 4: 写 Transcript / Composer 失败测试**

```tsx
it("renders thinking collapsed and tool calls paired with results", () => {
  // events: thinking(part0,final), tool_use(call_1), tool_result(call_1, ok)
  // 断言 "思考" 按钮存在且默认不显示全文；"工具 test.echo" 一行；展开后显示结果
})
it("concatenates text chunks by message_id and part", () => {
  // text parts 0,1 → 显示拼接后的完整文本
})
it("sends on Enter, keeps Shift+Enter as newline, and stops a running run", async () => {
  // fireEvent.keyDown(textarea,{key:"Enter"}) → onSend("...") 调用
  // Shift+Enter → 不调用；running=true → 发送禁用、停止可见
})
```
- [ ] **Step 5: 跑失败 → 实现 → 跑通过**

Run:
```powershell
cd frontend; pnpm vitest run src/features/agent-console
cd frontend; pnpm lint; pnpm typecheck
```
Expected: PASS。

实现要点：
- `SessionPane`：标题、状态徽章（running/done/aborted/failed）、停止按钮（运行中）、`Transcript`、`Composer`；发送/停止调用 `agent-api` 并刷新。
- `Transcript`：按 run 分组；text/thinking 先按 `(message_id, part)` 合并；tool_use/tool_result 按 `call_id` 配对；`decision` 用警告行；`usage` 折叠到 run 尾。
- `ToolCallBlock`：一行折叠；展开时 `getDetail(message_id)` 取完整参数/结果；加载中显示 skeleton；错误显示错误码。
- `Composer`：Enter 发送、Shift+Enter 换行；发送中禁用；409/503 显示 `ApiError.message`。
- 断线状态条：`connecting/live/reconnecting/closed` 文案；**重连中不显示空状态**。

- [ ] **Step 6: 提交**

```bash
git add frontend/src/features/agent-console
git commit -m "feat: add agent transcript, live sse merge, and composer"
```

---

### Task 13: 前端 Agent 配置 Tab

**Wave:** W3 · **并发:** 可并发（与 T11/T12/T14） · **依赖:** T8
**写作用域:** `frontend/src/features/agent-console/ProfileEditor*.tsx`、`ConfigPane*.tsx`

**Files:**
- Create: `ProfileEditor.tsx`、`ProfileEditor.test.tsx`
- Replace: `ConfigPane.tsx`、创建 `ConfigPane.test.tsx`

**Interfaces:**
- Consumes: T8 `listProfiles`/`updateProfile`/`listTools`、`AgentProfile`/`AgentTool`。
- Produces: 配置 Tab；E2E 依赖的可访问名称：`系统提示词`、`模型`、`最大回合`、`运行时长（秒）`、`硬超时（秒）`、`思考模式`、`思考强度`、`输出上限`、`允许工具`、`启用`、`保存配置`、`下次运行生效`。

- [ ] **Step 1: 写失败测试**

```tsx
it("renders the built-in profile values", async () => {
  // mock listProfiles → builtin profile；listTools → 5 工具
  // 断言 textarea 值、thinking=enabled、effort=max、max_output_tokens=32768
})
it("patch sends only changed fields and shows the next-run notice", async () => {
  // 改系统提示词 → 保存 → updateProfile 收到 {system_prompt:"..."}；显示 "下次运行生效"
})
it("rejects out-of-range numbers before calling the api", async () => {
  // max_turns=101 → 显示校验错误，updateProfile 不被调用
})
```
- [ ] **Step 2: 跑失败**

Run: `cd frontend; pnpm vitest run src/features/agent-console/ProfileEditor.test.tsx src/features/agent-console/ConfigPane.test.tsx`
Expected: FAIL。

- [ ] **Step 3: 实现**

- `ConfigPane`：加载 profiles + tools；左侧 profile 选择（P1 仅一条）；渲染 `ProfileEditor`。
- `ProfileEditor`：本地表单状态；数字/文本校验（与规格 §10.3 一致，前端只做提示，服务端仍校验）；`allowed_tools` 多选框；保存调用 `updateProfile`；成功后显示「下次运行生效」；`ApiError` 显示错误。
- 思考模式 select：`auto/enabled/disabled`；强度 select：`""/none/low/high/max`；`thinking=disabled` 时禁用强度并提示。

- [ ] **Step 4: 跑通过 + lint/typecheck**

Run:
```powershell
cd frontend; pnpm vitest run src/features/agent-console
cd frontend; pnpm lint; pnpm typecheck
```
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/features/agent-console
git commit -m "feat: add agent profile editor"
```

---

### Task 14: 应用服务与 HTTP API

**Wave:** W3 · **并发:** 可并发（与 T11/T12/T13） · **依赖:** T4、T5（ToolRegistry）；httpapi 的 `RunControl` 在 T14 本地定义，不依赖 T11 的具体实现
**写作用域:** `backend/internal/agent/application/profile_service*.go`、`session_service*.go`、`tool_catalog*.go`、`backend/internal/agent/interfaces/httpapi/`

**Files:**
- Create: `backend/internal/agent/application/profile_service.go`、`profile_service_test.go`
- Create: `backend/internal/agent/application/session_service.go`、`session_service_test.go`
- Create: `backend/internal/agent/application/tool_catalog.go`、`tool_catalog_test.go`
- Create: `backend/internal/agent/interfaces/httpapi/handlers.go`、`dto.go`、`handlers_test.go`

**Interfaces:**
- Consumes: T4 仓储；T5 `ToolRegistry`；T11 `RunService`（`Start`/`Abort`/`WaitIdle`）；`platform.WriteError`/`DecodeJSON`/`ParseLimit`/`DecodeCursor`/`EncodeCursor`；`identityhttp.RequireAdmin`（由 T15 传入 guard）。
- Produces（T15 依赖）：
```go
type ProfileService struct{}
func NewProfileService(profiles domain.ProfileRepository, audit AuditSink) *ProfileService
func (s *ProfileService) List(ctx context.Context) ([]domain.AgentProfile, error)
func (s *ProfileService) Get(ctx context.Context, id int64) (domain.AgentProfile, error)
func (s *ProfileService) Update(ctx context.Context, id int64, params domain.UpdateParams, actor *int64) (domain.AgentProfile, error)

type SessionService struct{}
type RunControl interface {
    Start(ctx context.Context, sessionID int64, message string, actor *int64) (domain.StartResult, error)
    Abort(ctx context.Context, sessionID int64, actor *int64) (domain.AbortResult, error)
    WaitIdle(ctx context.Context, sessionID int64, timeout time.Duration) bool
}
func NewSessionService(profiles domain.ProfileRepository, sessions domain.SessionRepository, runRepo domain.RunRepository, messages domain.MessageRepository, control RunControl, audit AuditSink) *SessionService
func (s *SessionService) List(ctx context.Context, cursor int64, limit int, profileID *int64) ([]domain.SessionSummary, bool, error)
func (s *SessionService) Create(ctx context.Context, profileID int64, title string, actor *int64) (domain.Session, error)
func (s *SessionService) Get(ctx context.Context, id int64) (domain.SessionDetail, error)
func (s *SessionService) UpdateTitle(ctx context.Context, id int64, title string) (domain.Session, error)
func (s *SessionService) Delete(ctx context.Context, id int64, actor *int64) error
func (s *SessionService) Message(ctx context.Context, sessionID, messageID int64) (domain.Message, error)

type ToolCatalog struct{}
func NewToolCatalog(registry *ToolRegistry) *ToolCatalog
func (c *ToolCatalog) Infos() []domain.ToolInfo

// httpapi（消费者定义的接口；application 的具体服务满足它们）
type ProfileService interface {
    List(ctx context.Context) ([]domain.AgentProfile, error)
    Get(ctx context.Context, id int64) (domain.AgentProfile, error)
    Update(ctx context.Context, id int64, params domain.UpdateParams, actor *int64) (domain.AgentProfile, error)
}
type SessionService interface {
    List(ctx context.Context, cursor int64, limit int, profileID *int64) ([]domain.SessionSummary, bool, error)
    Create(ctx context.Context, profileID int64, title string, actor *int64) (domain.Session, error)
    Get(ctx context.Context, id int64) (domain.SessionDetail, error)
    UpdateTitle(ctx context.Context, id int64, title string) (domain.Session, error)
    Delete(ctx context.Context, id int64, actor *int64) error
    Message(ctx context.Context, sessionID, messageID int64) (domain.Message, error)
}
type RunControl interface {
    Start(ctx context.Context, sessionID int64, message string, actor *int64) (domain.StartResult, error)
    Abort(ctx context.Context, sessionID int64, actor *int64) (domain.AbortResult, error)
}
type ToolCatalog interface { Infos() []domain.ToolInfo }

type Handler struct{}
func NewHandler(profiles ProfileService, sessions SessionService, runs RunControl, tools ToolCatalog) *Handler
func RegisterRoutes(mux *http.ServeMux, h *Handler, guard func(http.Handler) http.Handler)
```
其中 `application` 包的 `ProfileService`/`SessionService`/`ToolCatalog` 方法签名与上面一致；`SessionService` 依赖 application 包的 `RunControl`（含 `WaitIdle`）。

- [ ] **Step 1: 写应用服务失败测试**

- `TestProfileUpdateValidatesAndAudits`：非法字段 → 错误；合法 → repo 被调用 + 审计 `agent.profile.updated`。
- `TestSessionListUsesCursorAndLimit`：repo 返回 `hasMore`；游标透传。
- `TestSessionDeleteAbortsThenDeletes`：fake RunService 记录 Abort 调用；`WaitIdle` 返回 true 后才调用 repo.Delete；abort 后仍 running → 返回 `domain.ErrRunActive`。
- `TestSessionCreateDefaultsProfile`：`profileID=0` → 取内置 profile。
- `TestToolCatalogInfos`：返回 5 个工具信息（注册了什么返回什么）。

- [ ] **Step 2: 写 HTTP 失败测试**

用 fake service 实现接口，`httptest` 直接调 handler：
```go
func TestSendMessageReturns202AndMapsErrors(t *testing.T) {
    // 成功 → 202 {"data":{"run_id":9,"status":"accepted"}}
    // ErrRunActive → 409 details.reason=run_active
    // ErrCapacity → 503 retryable + reason=capacity
    // ErrLLMNotConfigured → 503 reason=llm_not_configured
    // ErrToolsUnsupported → 503 reason=tools_require_chat_completions
    // ErrEmptyMessage → 422
    // domain.ErrNotFound → 404
}
func TestListSessionsReturnsItemsCursorAndHasMore(t *testing.T) {
    // {"data":{"items":[...],"has_more":true,"next_cursor":"<base64 id>"}}
}
func TestProfilePatchRejectsUnknownFieldsAndBuiltinKey(t *testing.T) {
    // body 含 "key" → 422/400；含未知字段 → 400
}
func TestGetMessageRequiresMessageInSession(t *testing.T) {
    // service 返回 ErrNotFound → 404
}
func TestStopReturnsIdleOrStopping(t *testing.T)
func TestToolsList(t *testing.T)
```

- [ ] **Step 3: 跑失败**

Run: `cd backend; & ..\.tools\go\bin\go.exe test ./internal/agent/application ./internal/agent/interfaces/httpapi -v`
Expected: FAIL。

- [ ] **Step 4: 实现**

- `ProfileService.Update`：`Get` → `Apply(params)` → `Validate` → `repo.Update` → 审计（metadata 只含 `profile_key` 与变更字段名）。
- `SessionService`：创建时 `profileID==0` → 从 `Profiles.List` 找 `Builtin==true` 的一条；profile 不存在/禁用 → `domain.ErrNotFound`/`domain.ErrProfileDisabled`；`title` ≤120；列表 `limit` 由 handler 传入；删除先 `runs.Abort`，`WaitIdle` 最多 5s，未停 → `domain.ErrRunActive`，已停 → `repo.Delete` + 审计；`Get` 组装 `SessionDetail`（active/latest run）。
- HTTP DTO 映射严格按规格 §10.2；`writeJSON` 本地实现；错误映射表见规格 §10.3；`PATCH profile` 用 `domain.UpdateParams` 的指针字段，拒绝 `key`/`builtin`（这些不在 DTO 中，`DecodeJSON` 的 `DisallowUnknownFields` 已拒绝）。
- `RegisterRoutes` 把全部路由包在 `guard` 中；路由按规格 §10.1。
- `ToolCatalog.Infos` 返回 `registry.ToolInfos()`（按名字排序）。

- [ ] **Step 5: 跑通过 + lint**

Run:
```powershell
cd backend; & ..\.tools\go\bin\go.exe test ./internal/agent/... -v
cd backend; & ..\.tools\go\bin\go.exe vet ./internal/agent/...
```
Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add backend/internal/agent/application backend/internal/agent/interfaces/httpapi
git commit -m "feat: add agent application services and http api"
```

---

### Task 15: 适配器、路由装配、配置与启动接线

**Wave:** W4 · **并发:** 串行 · **依赖:** T3、T4、T5、T6、T11、T14
**写作用域:** `backend/internal/agent/infrastructure/{llm,eventing,audit}/`、`backend/internal/server/`、`backend/internal/config/`、`backend/cmd/eztest/`

**Files:**
- Create: `backend/internal/agent/infrastructure/llm/gateway.go`、`gateway_test.go`
- Create: `backend/internal/agent/infrastructure/eventing/sink.go`、`sink_test.go`
- Create: `backend/internal/agent/infrastructure/audit/sink.go`、`sink_test.go`
- Modify: `backend/internal/config/config.go`、`config_test.go`
- Modify: `backend/internal/server/server.go`
- Create: `backend/internal/server/agent_routes_test.go`
- Modify: `backend/cmd/eztest/main.go`

**Interfaces:**
- Consumes: `llmapp.ConfigService.GetDefault`、`llmapp.ClientFactory`、`llmapp.Client`、`llm.Config`；`eventingapp.Service.Publish`；`auditapp.Recorder.Record`；T11 `RunService`；T14 应用服务与 HTTP handler。
- Produces：
```go
// agent/infrastructure/llm（package agentllm）
type ConfigSource interface { GetDefault(ctx context.Context) (llmdomain.Config, error) }
type Gateway struct{}
func NewGateway(src ConfigSource, factory llmapp.ClientFactory) *Gateway
func (g *Gateway) Prepare(ctx context.Context, modelOverride string) (agentapp.LLMCaller, error)

// agent/infrastructure/eventing（package agentevent）
type Publisher interface {
    Publish(ctx context.Context, event eventing.NewEvent) (eventing.Event, error)
}
func NewSink(p Publisher) *Sink

// agent/infrastructure/audit（package agentaudit）
type Recorder interface { Record(ctx context.Context, entry audit.Entry) error }
func NewSink(r Recorder) *Sink

// config.Config 新增字段
AgentFileRoot        string
AgentHTTPAllowlist   []string
AgentToolTimeoutMS   int
AgentMaxConcurrentRuns int

// server.Dependencies 新增
type AgentDependencies struct {
    Profiles agenthttp.ProfileService
    Sessions agenthttp.SessionService
    Runs     agenthttp.RunControl
    Tools    agenthttp.ToolCatalog
}
Agent *AgentDependencies
```

- [ ] **Step 1: 写 config 失败测试**

```go
func TestLoadAgentDefaults(t *testing.T) {
    cfg := mustLoad(t, map[string]string{ ...required P0 vars... })
    // AgentFileRoot==""; AgentHTTPAllowlist==["127.0.0.1","localhost"]
    // AgentToolTimeoutMS==10000; AgentMaxConcurrentRuns==4
    // OpenAPIPath=="docs/openapi/api.yaml"
}
func TestLoadRejectsInvalidAgentValues(t *testing.T) {
    // EZTEST_AGENT_TOOL_TIMEOUT_MS=10  → error
    // EZTEST_AGENT_MAX_CONCURRENT_RUNS=0 → error
    // EZTEST_AGENT_HTTP_ALLOWLIST=" , " → error
}
```
- [ ] **Step 2: 跑失败 → 实现 config → 跑通过**

Run: `cd backend; & ..\.tools\go\bin\go.exe test ./internal/config -v`
Expected: 先 FAIL，后 PASS。

- [ ] **Step 3: 写 gateway 测试**

```go
func TestPrepareMapsRequestAndRejectsWrongProtocol(t *testing.T) {
    // ConfigSource 返回 chat_completions 配置（model_catalog: deepseek-flash/1000000）
    // caller.ContextWindowTokens()==1000000
    // caller.Model()==profile model override
    // ChatStream 请求映射：tools→llmapp.ToolSpec、thinking/reasoning_effort/max_tokens、
    //   assistant reasoning_content、tool_call_id
    // responses 协议 → errors.Is(err, domain.ErrToolsUnsupported)
    // GetDefault 返回错误 → errors.Is(err, domain.ErrLLMNotConfigured)
    // catalog 缺模型 → 100000
}
```
- [ ] **Step 4: 实现 gateway**

- `Prepare`：`GetDefault` → 错误映射；`Protocol != llm.ProtocolChat` → `domain.ErrToolsUnsupported`；model 覆盖；context window 查 catalog。
- `caller.ChatStream`：把 `agentapp.LLMRequest` 映射为 `llmapp.ChatRequest`，`llmapp.Client.ChatStream`；把 `llmapp.StreamEvent` 映射为 `agentapp.LLMStreamEvent`（含 ToolCalls/Usage/FinishReason/Err）。
- 错误码提取：`llmapp.AsLLMError(err)` → `Code`（写入 runner 的 `error_code`）。

- [ ] **Step 5: 写 event/audit sink 测试**

- event sink：fake publisher 捕获；断言 `Type=="agent.activity"`、Channel/AgentRole/SessionID/Summary/Detail 正确；publisher 错误透传。
- audit sink：fake recorder 捕获 `audit.Entry`；断言 Action/Actor/Target/Metadata 映射，metadata 不含参数/结果。

- [ ] **Step 6: 实现 sinks → 跑通过**

Run: `cd backend; & ..\.tools\go\bin\go.exe test ./internal/agent/infrastructure/... -v`
Expected: PASS。

- [ ] **Step 7: 写 server 路由/通道守卫测试**

```go
func TestAgentRoutesRequireAdmin(t *testing.T) {
    // Dependencies.Auth = acceptingAuth{token:"ezt_admin", user: admin}
    // Dependencies.Agent = fake deps
    // GET /api/agents/profiles with admin → 200；with user token → 403
}
func TestAgentChannelGuard(t *testing.T) {
    // 用 withMiddleware + mux.Handle("/api/stream", agentChannelAdminOnly(...))
    // admin ?channel=agent:1 → 200；user → 403；user ?channel=system → 200
}
func TestAgentRoutesAbsentWithoutDeps(t *testing.T) {
    // Agent=nil → GET /api/agents/profiles with admin → 404
}
```
- [ ] **Step 8: 实现 server 接线**

- `server.New`：`if deps.Agent != nil` → `agenthttp.NewHandler(...)`、`RegisterRoutes(mux, h, adminGuard)`，`adminGuard = RequireAdmin(auditRequestContext(next))`。
- `/api/stream` 与 `/api/stream/events` 的 handler 包一层 `agentChannelAdminOnly`。
- 守卫实现：`channel` 以 `agent:` 开头 → 要求 `identity.RoleAdmin`，否则 403 `forbidden`；其他通道放行。
- 测试用 fake services 实现 T14 的接口；fake 返回空列表/固定 DTO。

- [ ] **Step 9: 接线 `cmd/eztest/main.go`**

顺序（在 `eventService`/`llmConfigs` 之后、`cancelStartup()` 之前）：
1. `agentRepos`：4 个 postgres 构造器；
2. `auditSink := agentaudit.NewSink(auditor)`；
3. `sink := agentevent.NewSink(eventService)`；
4. `gateway := agentllm.NewGateway(llmConfigs, llmClientFactory)`；
5. `toolList := []domain.Tool{testEcho, testFail, httpGet}`；若 `cfg.AgentFileRoot != ""`，`fileTools, err := agenttools.NewFileTools(cfg.AgentFileRoot)`；err → 启动失败；追加到 `toolList`；
6. `registry := agentapp.NewToolRegistry(toolList...)`；`executor := agentapp.NewToolExecutor(registry, auditSink, time.Duration(cfg.AgentToolTimeoutMS)*time.Millisecond)`；
7. `runService := agentapp.NewRunService(agentapp.RunServiceDeps{... MaxConcurrentRuns: cfg.AgentMaxConcurrentRuns, HardTimeout: 60*time.Second})`；
8. `profileService := agentapp.NewProfileService(profileRepo, auditSink)`；`sessionService := agentapp.NewSessionService(profileRepo, sessionRepo, runRepo, messageRepo, runService, auditSink)`；`catalog := agentapp.NewToolCatalog(registry)`；
9. `profileRepo.EnsureBuiltin(startupCtx, domain.BuiltinProfile())`（记录是否创建，不覆盖已有）；
10. `runService.RecoverInterrupted(startupCtx)`（记录条数）；
11. `server.Dependencies.Agent = &server.AgentDependencies{...}`。
12. 关闭流程：`cancelBase()` 之后、`srv.Shutdown` 之前：
    ```go
    agentShutdownCtx, cancelAgent := context.WithTimeout(context.Background(), 5*time.Second)
    runService.Shutdown(agentShutdownCtx)
    cancelAgent()
    ```

- [ ] **Step 10: 全量构建/测试**

Run:
```powershell
cd backend; & ..\.tools\go\bin\go.exe build ./...
cd backend; & ..\.tools\go\bin\go.exe test ./...
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-integration.ps1 ./... -count=1
```
Expected: PASS。

- [ ] **Step 11: 提交**

```bash
git add backend/internal/agent/infrastructure backend/internal/server backend/internal/config backend/cmd/eztest/main.go
git commit -m "feat: wire agent runtime into server and composition root"
```

---

### Task 16: Playwright E2E — Agent 控制台全流程

**Wave:** W5 · **并发:** 可并发（与 T17/T18） · **依赖:** T15、T12、T13、T9
**写作用域:** `frontend/e2e/agent-console.spec.ts`

**Files:**
- Create: `frontend/e2e/agent-console.spec.ts`

**Interfaces:**
- Consumes: E2E 环境变量（`EZTEST_E2E_API_URL`/`EZTEST_E2E_BASE_URL`/`EZTEST_E2E_ADMIN_USERNAME`/`EZTEST_E2E_ADMIN_PASSWORD`）；T9 fake 上游；T12/T13 的可访问名称。
- Produces: P1 端到端验收证据。

- [ ] **Step 1: 写 E2E 场景**

复用 auth.spec 的登录模式；`test.describe.configure({ mode: "serial" })`。

```ts
test("admin runs the validation agent end to end", async ({ page, request }) => {
  // 1. login admin → goto /agents → heading "Agent 运行时控制台"
  // 2. click "新建会话" → fill getByLabel("发送消息") "检查运行时" → click "发送"
  // 3. await getByRole("button", { name: /工具 file\.read/ }) → click 展开
  //    expect getByText(/README/) → expect 状态 "已完成"
  // 4. 再发 "再总结一下" → 第二次 run 完成
  // 5. 发 "SLOW" → expect "运行中" → click "停止" → expect "已中止"
  // 6. page.reload() → expect 历史文本 "诊断完成" 与至少两个 run 分隔
});
test("prompt edit applies to the next run", async ({ page }) => {
  // 配置 Tab → 系统提示词追加 "E2E-MARKER-<ts>" → 保存 → "下次运行生效"
  // 会话 Tab → 发 "ECHO_SYSTEM" → expect 文本含 marker
});
test("regular user sees the admin-only state and gets 403", async ({ page, request }) => {
  // API 登录 admin → 创建普通用户（密码用 process.env.EZTEST_E2E_USER_PASSWORD）
  // → 新 context 登录 → /agents → "仅管理员可用"
  // request.get(`${API}/api/agents/sessions`, user token) → 403
});
```

- [ ] **Step 2: 运行全量 E2E**

Run: `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1`
Expected: P0 + P1 全部 PASS；`.tmp/e2e-artifacts/` 无失败 trace。

- [ ] **Step 3: 若失败，修的是产品或测试**

- 选择器不稳定 → 用 T12/T13 约定的可访问名称修正组件（不改测试绕过）；
- fake 行为不符 → 修 T9；
- 事件顺序/状态不符 → 修 T11；
- 不得降低断言（如删除 status/thinking/tool 断言）。

- [ ] **Step 4: 提交**

```bash
git add frontend/e2e/agent-console.spec.ts
git commit -m "test: add agent console e2e coverage"
```

---

### Task 17: OpenAPI、README、环境模板与验收脚本

**Wave:** W5 · **并发:** 可并发（与 T16/T18） · **依赖:** T15、T8
**写作用域:** `docs/openapi/`、`README.md`、`.env.example`、`deploy/`、`scripts/acceptance.ps1`

**Files:**
- Rename: `docs/openapi/p0.yaml` → `docs/openapi/api.yaml`（`git mv`）
- Modify: `docs/openapi/api.yaml`（补 P1）
- Modify: `README.md`
- Modify: `.env.example`
- Modify: `deploy/Dockerfile.api`、`deploy/docker-compose.yml`、`deploy/README.md`
- Modify: `scripts/acceptance.ps1`

**Interfaces:**
- Consumes: 规格 §10 端点/DTO；T15 的 `EZTEST_OPENAPI_PATH` 新默认值。
- Produces: 可发现的 API 文档与 P1 验收入口。

- [ ] **Step 1: 重命名并补 OpenAPI**

- `git mv docs/openapi/p0.yaml docs/openapi/api.yaml`；`info.title: EzTest API`、`version: 0.2.0`。
- 补齐 paths：`/api/agents/profiles`、`/api/agents/profiles/{id}`、`/api/agents/tools`、`/api/agents/sessions`、`/api/agents/sessions/{id}`、`/api/agents/sessions/{id}/messages`、`/api/agents/sessions/{id}/stop`、`/api/agents/sessions/{id}/messages/{message_id}`。
- components 增 `AgentProfile`、`AgentSession`、`AgentRun`、`AgentMessageDetail`、`AgentTool`、`AgentProfilePatch`、`SendMessageRequest`。
- 说明 `agent.activity` 9 类 kind 与 `agent:{session_id}` 通道。

- [ ] **Step 2: 更新部署与 README**

- `deploy/Dockerfile.api` 的 `COPY` 与 `EZTEST_OPENAPI_PATH` 改为 `docs/openapi/api.yaml`。
- `deploy/docker-compose.yml` 的 `EZTEST_OPENAPI_PATH` 同上；`deploy/README.md` 同步。
- `README.md`：新增「P1 Agent 运行时」章节（启动 `/agents`、4 个 agent env、fake 上游 E2E、gated 真实验收、事件契约摘要）；更新 OpenAPI 链接/目录树/env 表/`go run` 命令中的路径。
- `.env.example` 增加：
  ```text
  # ---- P1 Agent runtime ----
  # Read-only root for file.list/file.read; empty disables the file tools.
  EZTEST_AGENT_FILE_ROOT=
  # Comma-separated hosts allowed for http.get (default 127.0.0.1,localhost).
  EZTEST_AGENT_HTTP_ALLOWLIST=127.0.0.1,localhost
  EZTEST_AGENT_TOOL_TIMEOUT_MS=10000
  EZTEST_AGENT_MAX_CONCURRENT_RUNS=4
  ```

- [ ] **Step 3: 更新 `scripts/acceptance.ps1`**

- 把 `docs/openapi/p0.yaml` 改为 `docs/openapi/api.yaml`（第 574 行附近的文件清单）。
- 新增 P1 检查：
  ```powershell
  [void](Invoke-Check -Dod "P1-1" -CheckName "Agent unit tests" -Body {
      Invoke-LoggedCommand -Exe $goExe -Arguments @("test", "-count=1", "./internal/agent/...") -WorkingDirectory $backendDir
  })
  [void](Invoke-Check -Dod "P1-1" -CheckName "Agent integration tests" -Body {
      Invoke-LoggedCommand -Exe $goExe -Arguments @("test", "-tags=integration", "-count=1", "./internal/agent/...") -WorkingDirectory $backendDir
  })
  ```
  并在 P0 deliverables 检查中增加：`docs/openapi/api.yaml` 存在且含 `"/api/agents/sessions"`；`.env.example` 含 4 个 agent 变量。
- DoD 映射表追加 4 行（对应 `implementation-phases.md` L105–108）。

- [ ] **Step 4: 校验**

Run:
```powershell
cd backend; & ..\.tools\go\bin\go.exe test ./internal/config ./internal/server -v
Select-String -Path docs\openapi\api.yaml -Pattern '/api/agents/sessions|agent.activity' -Encoding UTF8
Select-String -Path .env.example -Pattern 'EZTEST_AGENT_FILE_ROOT|EZTEST_AGENT_HTTP_ALLOWLIST|EZTEST_AGENT_TOOL_TIMEOUT_MS|EZTEST_AGENT_MAX_CONCURRENT_RUNS' -Encoding UTF8
```
Expected: 全部命中；测试 PASS。

- [ ] **Step 5: 提交**

```bash
git add docs/openapi README.md .env.example deploy scripts/acceptance.ps1
git commit -m "docs: document and gate the P1 agent runtime"
```

---

### Task 18: Gated 真实 DeepSeek 验收脚本

**Wave:** W5 · **并发:** 可并发（与 T16/T17） · **依赖:** T15、T2
**写作用域:** `scripts/acceptance-agent-llm.ps1`

**Files:**
- Create: `scripts/acceptance-agent-llm.ps1`

**Interfaces:**
- Consumes: P0 `acceptance-llm.ps1` 的启动/登录模式；`.env` 的真实 LLM；`/api/agents/*`。
- Produces: 真实模型证据（thinking=max + 工具调用 + 终态）。

- [ ] **Step 1: 写脚本**

- `EZTEST_RUN_REAL_LLM != "1"` → 打印 `SKIPPED` 退出 0。
- 复用 `acceptance-llm.ps1` 的：读 `.env`、重置 `eztest_e2e`、构建 API、启动 API（真实 LLM + `EZTEST_AGENT_FILE_ROOT=$repoRoot`）、admin 登录取 token、清理。
- 流程：
  1. `GET /api/agents/profiles` → 找 `key=runtime-diagnostic`；
  2. `POST /api/agents/sessions`（profile_id）；
  3. `POST .../messages` `{message:"请检查 EzTest 运行时健康（调用 http.get 访问 http://localhost:13333/api/health），并读取 README 的构建说明。"}`；
  4. 每 2s 轮询 `GET /api/agents/sessions/{id}`，直到 `running=false` 或 300s；
  5. `GET /api/stream/events?channel=agent:{id}&limit=200`；
  6. 断言：`last_run.status=="done"`、`terminal_reason=="completed"`；事件 kind 至少包含 `run_started/thinking/text/tool_use/tool_result/usage/run_finished`；至少一个 `tool_result.status=="ok"`；usage 的 `reasoning_tokens>0`；
  7. 打印 `PASS run=<id> turns=<n> tools=<n> reason_tokens=<n>`（不打印任何 Key/文件内容）。
- 失败时输出最后一个 run 的 `status/terminal_reason/error_code` 与事件 kind 列表（不含正文）。

- [ ] **Step 2: 运行（gated）**

Run:
```powershell
$env:EZTEST_RUN_REAL_LLM = "1"
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance-agent-llm.ps1
```
Expected: `PASS ...`；若上游行为与 spike 不一致，报告具体 `error_code`，**不改断言绕过**。

- [ ] **Step 3: 提交**

```bash
git add scripts/acceptance-agent-llm.ps1
git commit -m "test: add gated real-llm agent acceptance"
```

---

### Task 19: 全量回归、人工验收与最终评审

**Wave:** W6 · **并发:** 串行 · **依赖:** T16、T17、T18
**写作用域:** 仅修复本轮发现的问题；不新增功能

- [ ] **Step 1: 后端全量**

Run:
```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1
```
Expected: 全部 PASS（Go unit + integration + frontend lint/typecheck/test）。

- [ ] **Step 2: E2E 全量**

Run: `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1`
Expected: 全部 PASS。

- [ ] **Step 3: 验收门禁**

Run: `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1`
Expected: 全部 PASS（Docker 相关 SKIPPED，与 P0 一致）。

- [ ] **Step 4: Gated 真实验收**

Run: `$env:EZTEST_RUN_REAL_LLM="1"; powershell -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance-agent-llm.ps1`
Expected: `PASS ...`。

- [ ] **Step 5: 手工演示（规格 §12.4）**

逐条执行并记录证据：多轮 + 工具调用；停止；刷新回放；配置生效；普通用户 403；运行中重启 API 后 `interrupted`；`test.fail` 三态不崩。

- [ ] **Step 6: 分支评审**

- `git diff dev...HEAD --stat` 确认只动 P1 范围（`agent` 上下文、`llm` 扩展、server/main/config、frontend agent-console、scripts/docs/deploy）。
- `git diff dev...HEAD | Select-String -Pattern 'sk-|Bearer |llm_key|password'` 确认无秘密。
- 检查未修改 P0 SSE/错误/认证契约。

- [ ] **Step 7: 最终提交与报告**

```bash
git add -A
git commit -m "chore: finish P1 agent runtime acceptance"
```
报告内容：命令输出摘要、E2E 结果、真实调用结果、手工验收清单、已知差异（如有）。

---

## Self-Review（写计划后自检结果）

**1. Spec coverage**

| 规格章节 | 覆盖任务 |
|---|---|
| §5 数据模型 / §5.5 原子性 | T4 |
| §6 上下文/预算/中止/状态机 | T7、T11 |
| §7 LLM 网关（tools/reasoning/thinking） | T3、T15 |
| §8 工具契约与安全 | T5、T6 |
| §9 活动事件 V1 / 分片 / 回放 | T5、T11 |
| §10 API / 错误 / OpenAPI | T14、T15、T17 |
| §11 前端控制台/transcript/配置 | T8、T12、T13 |
| §12 测试/E2E/fake/gated/spike | T2、T9、T10、T16、T18 |
| §13 风险与降级 | T2、T6、T11、T19 |
| §15 D27/R1–R5 | T1 |

**2. Step scan**：每个实现步骤都有文件名、签名或断言；无“handle edge cases”类空步；测试步骤含可执行的断言或明确的断言清单。

**3. Type consistency**：`domain.*` 读模型与哨兵错误在 T4 定义一次，T5–T15 只引用；`LLMMessage`/`LLMRequest`/`LLMStreamEvent` 只在 T4 `ports.go` 定义；`StartResult`/`AbortResult` 归属 domain；`ToolInfo` 归属 domain。

**4. Review Focus**：5 类输入分别在 T3（tool payload/流式分片）、T5（非法 JSON/panic/超时）、T6（路径穿越/SSRF）、T7（截断配对）、T11（reasoning 回传/中止/部分输出/事件限长）有测试。

**5. Proportion**：计划约 1550 行，规格约 915 行；代码块以签名、测试名、断言为主，未逐行转录实现（比例约 1.7×，属于阶段计划的可接受范围）。

