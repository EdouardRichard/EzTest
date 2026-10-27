# EzTest P1 Agent 运行时地基 — 设计规格

> **状态**：待用户评审（2026-10-27）
> **范围**：实施阶段 P1（`docs/design/implementation-phases.md` §2-P1）
> **设计权威**：`docs/design/system-blueprint.md` v2.2（D1–D27）；详细设计从属于主蓝图
> **配套**：`frontend-design.md` §4.2/§4.3.2/§6、`Agent.md` §5–§6、P0 规格（`2026-10-27-eztest-p0-foundation-design.md`）
> **性质**：P1 的实现依据（接口/DDL/契约/验收），不含任务拆解；任务拆解由 `writing-plans` 产出。

---

## 1. 目标与非目标

### 1.1 目标

交付所有业务 Agent 的公共底座，并用一个**运行时验证 Agent** 端到端证明它：

1. Agent 会话与上下文：system/user 组装、历史 transcript、上下文窗口截断。
2. 工具注册与权限：JSON Schema 描述、调用执行、错误模型、超时/取消、panic 隔离、审计。
3. 活动事件：thinking/text/tool_use/tool_result/decision/usage 写入 DB 并经 P0 SSE 实时推送、可回放。
4. 预算与中止：步数/时长软预算、硬超时兜底、手动中止（具名原因）、终态标记。
5. Agent 配置雏形：角色/提示词/模型/思考强度/输出上限/预算，管理员可配、下一次运行生效。
6. 内置**运行时验证 Agent**（`key=runtime-diagnostic`）：对 EzTest 自身做只读自检，驱动多轮对话 + 工具调用 + 结果。
7. 前端 `/agents` 控制台：会话列表、transcript（折叠/展开）、实时状态、中止按钮、活动回放、配置 Tab。
8. 风险 spike：真实的 `deepseek-flash` 思考模式 + 原生工具调用链路验证，以及 Playwright 最小执行 / Session 注入的最小验证。

### 1.2 非目标（P1 明确不做）

- 13 个业务 Agent、双图/黑板、多 Agent 协作、子 Agent。
- 运行中 steer、暂停/恢复（优雅/立即）、settlement 收尾轮。
- 上下文压缩（只做截断；压缩端口预留）。
- 浏览器正式执行、DB 断言、业务工具。
- 版本/需求/用例/执行/证据/问题/报告等领域实体与页面。
- 工具描述的 DB 化、提示词版本化、MCP/Skills 接入。
- 多实例 SSE 广播、消息队列、Redis/NATS。
- 资源级权限（P1 的 Agent 控制台仅管理员可用）。

---

## 2. 已确认约束与决策

| 项 | 决策 |
|---|---|
| 「诊断 Agent」定位 | **运行时验证夹具**，不进入蓝图 §6.1 十三角色；key=`runtime-diagnostic`；P2 起由业务 Agent 替换（见 §15 D27） |
| 工具调用协议 | 原生 function calling；P1 **只保证 `chat_completions`**；`responses` 协议带 tools → 明确失败，不做 JSON 文本兜底 |
| 最小工具集 | `file.list`、`file.read`、`http.get`、`test.echo`、`test.fail`（全部只读/无副作用） |
| 交互模型 | 用户消息驱动：一个 run 内自主「LLM ↔ 工具」循环；用户可继续追问；运行中不接收新消息（409）；可随时中止 |
| 思考模式 | **显式支持**：profile 配置 `thinking=enabled|auto|disabled`、`reasoning_effort=max|high|low|none`；内置 profile 默认 `enabled + max` |
| 输出上限 | profile `max_output_tokens` 默认 **32768**（max 档思考链需要空间；官方上限 128K） |
| 内置 profile 预算 | `max_turns=12`、`run_seconds=600`、`hard_timeout_seconds=60` |
| 运行时方案 | 持久转录（`agent_messages` 权威）+ 进程内 Runner + P0 events 投影 |
| 活动事件 | 单一事件类型 `agent.activity`，`detail.kind` 区分 9 类；通道 `agent:{session_id}` |
| 前端落点 | 升级现有 `/agents` 占位页为 Agent 控制台（会话 + 配置 Tab），不新增页面 |
| 权限 | P1 全部 admin-only；SSE `agent:` 前缀通道加管理员守卫 |
| 部署/运行 | 单实例；沿用 P0 Windows 便携工具链、端口（Web 6868 / API 13333 / PG 5433） |
| Git | `feat/p1-agent-runtime` 分支，小步提交，不 push；验收后本地合并回 `dev` |
| 上游模型 | `https://api.deepseek.com`、`deepseek-flash`；思考模式默认开启，支持 Tool Calls |

---

## 3. 架构与仓库结构

### 3.1 形态

- 新增限界上下文 `agent`，四层齐全（`Agent.md` §6.1/§6.5）。
- 跨上下文依赖用**本上下文端口 + 组装根适配**：
  - `LLMPort`（由 `llm/application.Client` 适配）；
  - `EventSink`（由 `eventing/application.Service` 适配）；
  - `AuditSink`（由 `audit/application.Recorder` 适配）。
- `cmd/eztest` 是唯一组装根；`server` 只做路由/中间件装配。
- 运行时**不承载业务逻辑**：不 import 任何领域上下文，不写版本/需求/用例表。

### 3.2 目录

```text
backend/internal/agent/
  domain/
    profile.go          # AgentProfile、校验、种子常量
    session.go          # Session
    run.go              # Run、Status、TerminalReason、中止原因
    message.go          # Message、Part、ToolCall、Usage
    activity.go         # 活动 kind 常量与构造
    tool.go             # Tool 接口、ToolResult、ToolError
    repository.go       # Profile/Session/Run/Message 仓储端口
  application/
    run_service.go      # 启动/中止/执行循环/收尾
    context_builder.go  # 上下文组装 + 截断
    tool_service.go     # 工具注册表、白名单、执行管线
    event_sink.go       # 活动事件发布（端口）
    ports.go            # LLMPort、EventSink、AuditSink、Clock
    prompt.go           # 内置 profile 的系统提示词
  infrastructure/
    postgres/
      profile_repository.go
      session_repository.go
      run_repository.go
      message_repository.go
    tools/
      file.go           # file.list / file.read
      http.go           # http.get
      test.go           # test.echo / test.fail
      registry.go       # 静态注册
  interfaces/httpapi/
    handlers.go         # /api/agents/*
    dto.go
frontend/src/features/agent-console/
  AgentConsole.tsx
  SessionList.tsx
  Transcript.tsx
  ActivityItem.tsx
  ToolCallBlock.tsx
  ThinkingBlock.tsx
  Composer.tsx
  ProfileEditor.tsx
  agent-api.ts
  useAgentSession.ts
scripts/fake-llm.cjs   # E2E 确定性上游
```

### 3.3 运行流

```text
POST /api/agents/sessions/{id}/messages
  -> 校验（会话/profile/LLM 就绪/单飞/并发额度）
  -> 一次 SQL 原子插入 user message + running run + 更新会话。失败（唯一索引）-> 409
  -> 202 返回 {run_id,status:"accepted"}
  -> 注册 cancel func，后台 goroutine：
       for 回合:
         预算检查 -> 构建上下文（截断）-> LLMPort.ChatStream
         -> 插入 assistant 占位消息（取得 message_id）-> 流式发 thinking/text 分片
         -> Done/中止时 UPDATE 完整内容/reasoning/tool_calls/usage
         -> 有 tool_calls: 逐个执行（白名单/超时/panic/取消）-> 落库 tool message -> 发活动
         无 tool_calls -> 终态
  -> 终态：更新 run 行 -> 发 run_finished 活动 -> 释放额度/注销
```

---

## 4. 运行、配置与本地开发

### 4.1 新增环境变量

| 变量 | 必填 | 默认 | 说明 |
|---|---|---|---|
| `EZTEST_AGENT_FILE_ROOT` | 否 | 空 | 文件工具只读根；**空 = 文件工具不注册**（安全默认）。本地开发设为仓库根 |
| `EZTEST_AGENT_HTTP_ALLOWLIST` | 否 | `127.0.0.1,localhost` | `http.get` 允许的 host 列表（逗号分隔，可含 `:port`） |
| `EZTEST_AGENT_TOOL_TIMEOUT_MS` | 否 | `10000` | 单工具超时上限（1000–60000） |
| `EZTEST_AGENT_MAX_CONCURRENT_RUNS` | 否 | `4` | 全局并发 run 上限（1–32） |

- 沿用 P0 的 `EZTEST_*` 校验风格：非法值启动失败并给出明确错误。
- 不新增 secret 类变量；工具不读取 `.env`。

### 4.2 本地开发与脚本

- `scripts/dev-api.ps1`：加载 `.env` 时补充 `EZTEST_AGENT_FILE_ROOT=<repo root>`。
- `scripts/e2e.ps1`：启动 `scripts/fake-llm.cjs`（默认端口 `13334`），并以进程环境覆盖 `llm_base_url=http://127.0.0.1:13334`、`llm_key=e2e-test`、`llm_model_name=deepseek-flash`，同时设置 `EZTEST_AGENT_FILE_ROOT=<repo root>`，使 E2E 不产生真实费用且文件工具可用；退出时停止 fake 上游。
- `scripts/acceptance.ps1`：追加 P1 非计费检查（迁移幂等、`/api/agents/*` 鉴权、fake 上游集成跑通）。
- 新增 `scripts/acceptance-agent-llm.ps1`：gated（`EZTEST_RUN_REAL_LLM=1`）真实 `deepseek-flash` 思考模式 + 工具调用验收，不进入默认门禁。

### 4.3 迁移与种子

- 新迁移 `backend/internal/db/migrations/agent_0001_profiles_sessions_runs_messages.sql`，加入 `order.txt` 末尾。
- 启动种子：内置 profile 幂等创建（`ON CONFLICT (key) DO NOTHING`），不覆盖管理员已修改内容。
- 启动清理：`UPDATE agent_runs SET status='interrupted', terminal_reason='interrupted', ended_at=now() WHERE status='running'`。
- 关闭流程：`RunService.Shutdown(ctx)` 用 `shutdown` 原因取消所有运行中的 run；`main` 在 SIGINT/SIGTERM 时先调用它，再关闭 HTTP。

---

## 5. 数据模型

### 5.1 DDL

```sql
CREATE TABLE IF NOT EXISTS agent_profiles (
    id                   BIGSERIAL PRIMARY KEY,
    key                  TEXT NOT NULL UNIQUE CHECK (key ~ '^[a-z][a-z0-9_-]*$'),
    name                 TEXT NOT NULL,
    description          TEXT NOT NULL DEFAULT '',
    builtin              BOOLEAN NOT NULL DEFAULT false,
    enabled              BOOLEAN NOT NULL DEFAULT true,
    system_prompt        TEXT NOT NULL,
    model                TEXT NOT NULL DEFAULT '',             -- '' = 用默认 LLM 配置的 model
    allowed_tools        TEXT[] NOT NULL DEFAULT '{}',
    max_turns            INT NOT NULL DEFAULT 0 CHECK (max_turns BETWEEN 0 AND 100),   -- 0 = 不限
    run_seconds          INT NOT NULL DEFAULT 600 CHECK (run_seconds BETWEEN 30 AND 3600),
    hard_timeout_seconds INT NOT NULL DEFAULT 60 CHECK (hard_timeout_seconds BETWEEN 5 AND 300),
    thinking             TEXT NOT NULL DEFAULT 'enabled'
                         CHECK (thinking IN ('auto','enabled','disabled')),
    reasoning_effort     TEXT NOT NULL DEFAULT 'max'
                         CHECK (reasoning_effort IN ('','none','low','high','max')),
    max_output_tokens    INT NOT NULL DEFAULT 32768 CHECK (max_output_tokens BETWEEN 1024 AND 131072),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS agent_sessions (
    id               BIGSERIAL PRIMARY KEY,
    profile_id       BIGINT NOT NULL REFERENCES agent_profiles(id) ON DELETE RESTRICT,
    title            TEXT NOT NULL DEFAULT '',
    created_by       BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_activity_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_agent_sessions_created ON agent_sessions(created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS agent_runs (
    id                   BIGSERIAL PRIMARY KEY,
    session_id           BIGINT NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    status               TEXT NOT NULL DEFAULT 'running'
                         CHECK (status IN ('running','done','failed','aborted','interrupted')),
    terminal_reason      TEXT CHECK (terminal_reason IN (
                           'completed','max_turns','timeout','model_error','context_overflow',
                           'aborted_streaming','aborted_tools','aborted','shutdown',
                           'run_hard_timeout','interrupted')),
    abort_code           TEXT,
    abort_message        TEXT,
    error_code           TEXT,
    error_message        TEXT,
    turns                INT NOT NULL DEFAULT 0,
    input_tokens         BIGINT NOT NULL DEFAULT 0,
    output_tokens        BIGINT NOT NULL DEFAULT 0,
    reasoning_tokens     BIGINT NOT NULL DEFAULT 0,
    last_tool_name       TEXT,
    last_tool_pending    BOOLEAN NOT NULL DEFAULT false,
    started_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at             TIMESTAMPTZ
);
-- 会话内单飞：同一会话最多一个 running run
CREATE UNIQUE INDEX IF NOT EXISTS uq_agent_runs_running
    ON agent_runs(session_id) WHERE status = 'running';
CREATE INDEX IF NOT EXISTS idx_agent_runs_session ON agent_runs(session_id, id DESC);

CREATE TABLE IF NOT EXISTS agent_messages (
    id           BIGSERIAL PRIMARY KEY,
    session_id   BIGINT NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    run_id       BIGINT REFERENCES agent_runs(id) ON DELETE SET NULL,
    role         TEXT NOT NULL CHECK (role IN ('system','user','assistant','tool')),
    content      JSONB NOT NULL DEFAULT '[]',    -- parts 数组，与 P0 llm.Message 同构
    reasoning    TEXT NOT NULL DEFAULT '',       -- 思考链；请求带 tools 时必须回传
    tool_calls   JSONB,                          -- assistant 的工具调用数组
    tool_call_id TEXT,                           -- tool 消息对应调用
    tool_name    TEXT,
    usage        JSONB,                          -- {prompt_tokens,completion_tokens,reasoning_tokens,total_tokens}
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_agent_messages_session ON agent_messages(session_id, id);
CREATE INDEX IF NOT EXISTS idx_agent_messages_run ON agent_messages(run_id, id);
```

### 5.2 迁移规则

- 只增不改；文件名 `<context>_<version>_<desc>.sql`；`order.txt` 定义全局顺序。
- 重复执行无副作用；失败回滚并终止启动。
- 不写业务样例数据；内置 profile 通过启动种子创建。

### 5.3 内置 profile 种子

| 字段 | 值 |
|---|---|
| `key` | `runtime-diagnostic` |
| `name` | `运行时验证 Agent` |
| `builtin` | `true` |
| `system_prompt` | 见 §5.4 |
| `model` | `''`（用默认 LLM 配置） |
| `allowed_tools` | `{file.list,file.read,http.get,test.echo,test.fail}` |
| `max_turns` / `run_seconds` / `hard_timeout_seconds` | `12` / `600` / `60` |
| `thinking` / `reasoning_effort` / `max_output_tokens` | `enabled` / `max` / `32768` |

### 5.4 内置系统提示词

```text
你是 EzTest 的运行时验证助手，用来验证 Agent 运行时（会话、工具、活动流、预算、中止）是否正常工作。
你可以只读地检查 EzTest 自身：用 http.get 访问本机公开端点（如 http://localhost:13333/api/health），
用 file.list / file.read 阅读仓库内文档与代码，用 test.echo / test.fail 验证工具链路。
规则：
1. 只读，不修改任何文件，不访问只读根之外或白名单之外的目标。
2. 需要事实时先调用工具，不要凭空推测；工具失败时如实说明失败原因。
3. 回答简洁：说明检查了什么、看到了什么、结论是什么；需要时可分点。
4. 不要尝试获取或输出任何密钥、令牌、密码或 .env 内容。
```

### 5.5 并发与原子性

- 创建一次用户回合（user message + running run + 会话时间/标题）用**单条 CTE 语句**原子完成，避免引入事务管理器：

```sql
WITH ins_run AS (
  INSERT INTO agent_runs (session_id, status) VALUES ($1, 'running') RETURNING id
), ins_msg AS (
  INSERT INTO agent_messages (session_id, run_id, role, content)
  SELECT $1, ins_run.id, 'user', $2 FROM ins_run RETURNING id
), upd AS (
  UPDATE agent_sessions
     SET last_activity_at = now(), updated_at = now(),
         title = CASE WHEN title = '' THEN $3 ELSE title END
   WHERE id = $1
)
SELECT ins_run.id, ins_msg.id FROM ins_run, ins_msg;
```

- 部分唯一索引是单飞的最终防线：并发冲突返回 409 `conflict` / `details.reason="run_active"`。
- 会话标题：首条消息前 40 个字符（**由应用截断后**作为 `$3` 传入；超长加省略号）。

---

## 6. 上下文、预算与 run 生命周期

### 6.1 上下文组装

- system = `agent_profiles.system_prompt`，**每轮重新拼装、永不参与截断**。
- 历史 = 该会话全部 `agent_messages`（按 id 升序），转换为 LLM 消息：
  - `role=assistant`：`content` parts + `reasoning`（**非空才回传**）+ `tool_calls`；
  - `role=tool`：`content` parts + `tool_call_id`；
  - `role=user`：`content` parts。
- 预算公式：`char_budget = context_window - max(2048, 25% × context_window) - 2000`；字符当 token 计（对中文保守）。
  - `context_window` 取自默认 LLM 配置的 `model_catalog`；缺失时用保守值 `100000`。
- 截断顺序：从最旧消息**整轮丢弃**（user + assistant + 其 tool 消息成组），保证 `tool_calls ↔ tool` 配对；永不丢 system 与最新 user 消息。
- 仍超限 → 该 run `failed/context_overflow`，**不静默截断用户输入**。
- 单条工具结果入上下文前截断到 32KB（保留头尾 + `...[truncated]`）。
- 截断发生时发 `agent.decision {decision:"context_truncated", dropped:N}`。
- 预留 `Compactor` 端口，P1 不实现压缩。

### 6.2 预算

| 预算 | 判定点 | 终态 |
|---|---|---|
| `max_turns`（0=不限） | 每次 LLM 调用前，`turns >= max_turns` | `done / max_turns` + `decision(budget_stop)` |
| `run_seconds` | 每次 LLM 调用前（软预算，不打断在飞调用） | `done / timeout` + `decision(budget_stop)` |
| `run_seconds + hard_timeout_seconds` | `context.WithTimeoutCause` 硬兜底，取消在飞调用 | `aborted / run_hard_timeout` |
| 全局并发额度 | 启动前非阻塞获取；满则 503 | 不创建 run |

### 6.3 中止语义

- 取消统一用 `context.WithCancelCause`，原因枚举：

| abort_code | 触发 | 终态 |
|---|---|---|
| `user_abort` | `POST /stop` 或删除会话 | 模型输出中 → `aborted / aborted_streaming`；工具中 → `aborted / aborted_tools`；回合间 → `aborted / aborted` |
| `shutdown` | 进程 SIGINT/SIGTERM | `aborted / shutdown` |
| `run_hard_timeout` | 硬兜底 | `aborted / run_hard_timeout` |

- **中止即停**：取消信号立即传给 LLM 流的 ctx 与工具 ctx；不等待剩余步骤。
- 终态事件携带 `abort_code` 与中断时在飞工具（`last_tool_name` / `last_tool_pending`）。
- **不做** graceful/立即两种暂停、收尾轮（P6/P7）；但保留 `OnTerminate(reason)` 钩子点。

### 6.4 run 状态机

```text
running ─┬─ completed ─────────────▶ done
         ├─ max_turns / timeout ───▶ done
         ├─ model_error / context_overflow ─▶ failed
         ├─ user_abort ────────────▶ aborted
         ├─ shutdown / run_hard_timeout ────▶ aborted
         └─ 进程重启 ──────────────▶ interrupted
```

- `status` 与 `terminal_reason` 双列（ARTEX 风格）；`error_code` 记录 `llm_*` 等具体错误。
- 每个模型回合后更新 run 的 `turns/input_tokens/output_tokens/reasoning_tokens`，崩溃时保留已完成回合的用量。
- 终态写库成功后再发 `run_finished` 事件；事件失败只记日志。

---

## 7. LLM 网关扩展

### 7.1 端口类型（`llm/application`，向后兼容新增）

```go
type ToolSpec struct {
    Name        string
    Description string
    Parameters  json.RawMessage // JSON Schema object
}

type ToolCall struct {
    ID        string
    Name      string
    Arguments string // 模型原始 JSON 字符串
}

type Message struct {
    Role             string
    Content          []Part
    ReasoningContent string    // 思考链；请求带 tools 时必须回传
    ToolCalls        []ToolCall
    ToolCallID       string
    Name             string
}

type ChatRequest struct {
    Model           string
    Messages        []Message
    Tools           []ToolSpec
    Stream          bool
    MaxTokens       int
    Thinking        string // "" | enabled | disabled
    ReasoningEffort string // "" | none | low | high | max
}

type ChatResponse struct {
    Text             string
    ReasoningContent string
    ToolCalls        []ToolCall
    FinishReason     string
    Usage            Usage
}

type StreamEvent struct {
    Delta          string
    ReasoningDelta string
    Done           bool
    ToolCalls      []ToolCall // Done 时携带装配结果
    FinishReason   string
    Usage          Usage
    Err            error
}
```

`Usage` 增加 `ReasoningTokens int`。

### 7.2 Chat Completions 适配

| 场景 | 行为 |
|---|---|
| assistant 消息 | `content` 可空；带 `tool_calls`；`reasoning_content` 非空才发 |
| tool 消息 | `content` 用 parts 数组；带 `tool_call_id` |
| 请求参数 | `tools` 非空才发；`thinking`/`reasoning_effort` 非空才发；`max_tokens` 来自 profile |
| 流式 | 累加 `delta.reasoning_content`；按 `index` 聚合 `delta.tool_calls`；`finish_reason=tool_calls` 时在 Done 携带装配后的调用 |
| usage | 解析 `[DONE]` 前最后 chunk 的 usage（含 `reasoning_tokens`）；缺失则该回合不发 usage 活动 |
| 空文本 | `content` 为空但 `tool_calls` 非空是合法响应（P0 原“空文本即错误”的判断需放宽） |
| 错误 | `insufficient_system_resource` / `aborted` / `length` / 无 tool_calls 的 `finish_reason=tool_calls` → `llm_invalid_response`（不可重试），run `failed/model_error` |
| 重试 | 沿用 P0 retry wrapper（429/5xx/超时、首字节前）；中止经 ctx |

- `thinking` 官方参数在 OpenAI SDK 中属 `extra_body`；EzTest 直接构造 JSON body，字段名为 `thinking` 与 `reasoning_effort`。
- **思考模式 + tools 的硬约束**：历史所有 assistant 消息的 `reasoning_content` 必须完整回传，否则上游 400。上下文构建与适配器测试必须覆盖。

### 7.3 Responses 协议

- P1 的 Agent 只使用 `chat_completions` 默认配置。
- 若默认配置协议为 `responses`：启动 run 前拒绝，503 `unavailable` / `details.reason="tools_require_chat_completions"`。
- `responses` 适配器在收到 `tools` 时同样返回 `llm_bad_request`（防御性双保险）。
- 上游其实也支持 Responses tools；这是 **P1 刻意裁剪**，后续可扩展。

### 7.4 默认 LLM 配置的选择

- 使用 `llm_configs.is_default=true` 的配置；无默认或 Key 不可解密 → 503 `llm_not_configured`。
- `agent_profiles.model` 非空时覆盖配置的 `model`。
- 每个 run 开始前读取一次配置（管理员改动下次 run 生效）；run 内使用同一客户端。
- 超时沿用配置的 `timeout_ms`。**运维提示**：max 档思考建议把默认配置超时调到 ≥180000ms（设置页可改，不改 P0 默认种子）。

---

## 8. 工具契约与安全

### 8.1 接口

```go
type Tool interface {
    Name() string
    Description() string
    InputSchema() json.RawMessage // JSON Schema object
    Execute(ctx context.Context, args json.RawMessage) (ToolResult, error)
}

type ToolResult struct {
    Content string // 回给模型的文本
    IsError bool   // true = 工具级失败，run 继续
}

type ToolError struct {
    Code    string // invalid_arguments | denied | not_found | too_large | timeout |
                   // unsupported_content | internal | not_configured
    Message string
}
```

### 8.2 执行管线

1. 白名单检查（`agent_profiles.allowed_tools`）；未授权 → `IsError` + `decision(tool_denied)`。
2. 解析 `arguments` JSON；非法 → `IsError(invalid_arguments)`。
3. `context.WithTimeout(min(全局工具超时, 工具自身超时))`。
4. `defer recover()` → panic 转 `IsError(internal)`，进程不崩。
5. 结果截断：入上下文 ≤32KB；活动 preview ≤1KB。
6. 审计 `agent.tool.invoked`：`{tool,status,duration_ms,run_id,session_id}`，**不写参数值与结果内容**。
7. 无论成功失败，都落库 `tool` 消息并发 `tool_use` / `tool_result` 活动。

- 同一 assistant 消息的多个工具调用**顺序执行**（V1 确定性）。
- 工具错误不终止 run；错误结果回给模型，模型可换参数或收尾。
- `turns` 只按 **LLM 回合**计数；工具调用不额外增加回合。
- `tool_result.status` 映射：正常 → `ok`；工具返回 `IsError` → `error`；超时 → `timeout`；ctx 取消 → `cancelled`；白名单拒绝 → `denied`。

### 8.3 工具清单

| 工具 | Schema 要点 | 边界 |
|---|---|---|
| `file.list` | `{path?: string, depth?: 1..3}` | 只读根内；单次 ≤200 项；返回 `name/type/size` |
| `file.read` | `{path: string, offset?: ≥1, limit?: 1..2000}` | ≤256KB；必须 UTF-8 文本；拒绝含 NUL 的二进制 |
| `http.get` | `{url: string}` | 仅 GET；host 白名单；≤1MB；5s；重定向 ≤3 且每跳重校验；拒绝 URL 内凭据；不接受模型自定义请求头 |
| `test.echo` | `{text: string}` | 原样回显 |
| `test.fail` | `{mode: error|timeout|panic, message?: string}` | 模拟三态；仅内置 profile 白名单包含 |

### 8.4 文件工具安全

- 根目录来自 `EZTEST_AGENT_FILE_ROOT`；为空则不注册文件工具。
- 解析：`filepath.Abs` → `filepath.EvalSymlinks` → 必须仍在根的 `EvalSymlinks` 前缀内；拒绝 `..` 逃逸。
- Deny（任一路径段/文件名匹配）：
  - 段名：`.git`、`.tools`、`.data`、`.tmp`、`node_modules`、`.next`、`.secrets`；
  - 文件名：`.env`、`.env.*`、`*.local.md`。
- 单文件 >256KB → `too_large`；不可读 → `not_found`/`internal`。

### 8.5 HTTP 工具安全

- 仅 `http`/`https`；拒绝 userinfo、非标准 scheme。
- host（含端口，若白名单项带端口）必须精确匹配白名单；默认 `127.0.0.1,localhost`。
- 重定向每跳重新校验 host；超过 3 跳失败。
- 响应仅接受文本类 Content-Type（`text/*`、`application/json`、`application/xml`、`application/yaml`）；其他类型返回 `unsupported_content`。
- 响应体 >1MB 截断（保留头 + 标记）。
- 不发送任何本地凭据；不转发 Agent 的 token。
- P1 不做 DNS rebinding 防护（白名单是控制点）；记录为已知风险。

### 8.6 审计动作

| action | 触发 | metadata |
|---|---|---|
| `agent.tool.invoked` | 每次工具调用 | `tool,status,duration_ms,run_id,session_id` |
| `agent.profile.updated` | PATCH profile | `profile_key,fields` |
| `agent.session.deleted` | DELETE 会话 | `session_id` |
| `agent.run.aborted` | 手动中止 | `run_id,session_id,abort_code` |

---

## 9. 活动事件契约 V1（冻结）

### 9.1 信封

- 复用 P0 `events` 表与 SSE；`type = agent.activity`；`channel = agent:{session_id}`（≤64 字符）。
- `agent_role = profile.key`；`session_id = strconv(session id)`。
- `summary` ≤500 字符；`detail` 为 JSON object，≤64KB；`refs` 四数组始终存在。
- 事件按 run 内单一 goroutine 顺序发布；通道 `seq` 单调。用户消息事件由 HTTP handler 发布，先于 goroutine 的 `run_started`。

### 9.2 `detail.kind`（V1 冻结）

| kind | 触发 | detail 字段 |
|---|---|---|
| `run_started` | run 开始 | `run_id, profile_key, model, thinking, reasoning_effort, max_output_tokens, max_turns, run_seconds` |
| `user_message` | 用户消息落库后 | `message_id, text`（≤4096，超出截断并标记）、`truncated` |
| `thinking` | 思考连续片段 | `run_id, message_id, part, final, text`（≤4096/片）、`truncated` |
| `text` | 回答连续片段 | 同上 |
| `tool_use` | 工具调用前 | `run_id, call_id, tool, args`（≤2048）、`args_truncated` |
| `tool_result` | 工具返回后 | `run_id, call_id, tool, status`(ok/error/timeout/cancelled/denied)、`duration_ms, result`（≤2048）、`result_truncated, error_code, message_id` |
| `usage` | 每个模型回合 | `run_id, turn, prompt_tokens, completion_tokens, reasoning_tokens, total_tokens` |
| `decision` | 预算/截断/拒绝 | `run_id, decision`(context_truncated/budget_stop/tool_denied)、`reason`、可选计数 |
| `run_finished` | run 终态 | `run_id, status, terminal_reason, turns, duration_ms, usage{prompt,completion,reasoning,total}, abort_code?, last_tool_name?, last_tool_pending?` |

### 9.3 文本分片

- thinking/text 按连续片段合并；片段结束时 flush。
- 长片段增量 flush：**每 1500ms 或累计 1024 字符**先发一条，`final=false`；片段结束时补 `final=true`。
- 每个 LLM 回合在流开始前插入 assistant **占位消息**（空内容）取得 `message_id`；分片引用它；Done/中止时 UPDATE 完整内容与 usage。中止保留已生成的部分输出。
- 前端按 `(message_id, part)` 拼接；完整文本仍以 `agent_messages` 为准。
- 不逐 token 落事件。

### 9.4 回放

- 历史：`GET /api/stream/events?channel=agent:{id}&limit=&before_seq=`（P0 快照，按 seq 升序）。
- 实时：`GET /api/stream?channel=agent:{id}`（P0 SSE；`Last-Event-ID` 断线续传）。
- 完整内容：`GET /api/agents/sessions/{id}/messages/{message_id}` 懒加载（text/reasoning/tool_calls/tool_result）。
- 事件 detail 只放预览/分片 + 引用，避免撞 64KB 上限。

### 9.5 通道权限

- server 装配层守卫：`channel` 以 `agent:` 开头时要求 admin，否则 403。P0 `system` 通道行为不变。
- P6 引入版本/执行会话时，用 `ChannelAuthorizer` 端口替换该前缀策略（记录为后续工作）。

---

## 10. API

### 10.1 端点

```text
GET    /api/agents/profiles
GET    /api/agents/profiles/{id}
PATCH  /api/agents/profiles/{id}
GET    /api/agents/tools
GET    /api/agents/sessions
POST   /api/agents/sessions
GET    /api/agents/sessions/{id}
PATCH  /api/agents/sessions/{id}
DELETE /api/agents/sessions/{id}
POST   /api/agents/sessions/{id}/messages
POST   /api/agents/sessions/{id}/stop
GET    /api/agents/sessions/{id}/messages/{message_id}
GET    /api/stream/events?channel=agent:{id}     # 复用 P0
GET    /api/stream?channel=agent:{id}            # 复用 P0
```

- P1 全部 admin-only（`RequireAdmin` + 全局 bearer 中间件）。
- 写操作沿用 P0 `Idempotency-Key` 中间件；POST message 幂等重放返回同一 202 响应。
- `DELETE` 会话：先以 `user_abort` 取消活跃 run，最多等待 **5s**；已停止则级联删除（消息/run），未停止返回 409 `run_active` 且不删除。

### 10.2 DTO

**Profile**

```json
{
  "id": 1, "key": "runtime-diagnostic", "name": "运行时验证 Agent",
  "description": "", "builtin": true, "enabled": true,
  "system_prompt": "...", "model": "",
  "allowed_tools": ["file.list","file.read","http.get","test.echo","test.fail"],
  "max_turns": 12, "run_seconds": 600, "hard_timeout_seconds": 60,
  "thinking": "enabled", "reasoning_effort": "max", "max_output_tokens": 32768,
  "created_at": "...", "updated_at": "..."
}
```

**Session**

```json
{
  "id": 1, "profile_id": 1, "profile_key": "runtime-diagnostic",
  "title": "检查运行时健康", "created_by": 1,
  "running": true,
  "active_run": {"id": 9, "status": "running", "started_at": "...", "turns": 2},
  "last_run": {"id": 8, "status": "done", "terminal_reason": "completed", "started_at": "...", "ended_at": "..."},
  "created_at": "...", "updated_at": "...", "last_activity_at": "..."
}
```

**Run**

```json
{
  "id": 9, "session_id": 1, "status": "running", "terminal_reason": null,
  "turns": 2, "input_tokens": 1200, "output_tokens": 300, "reasoning_tokens": 800,
  "abort_code": null, "error_code": null, "last_tool_name": "file.read", "last_tool_pending": false,
  "started_at": "...", "ended_at": null
}
```

**Send / Stop**

```json
POST /api/agents/sessions/1/messages  {"message":"检查运行时健康"}
-> 202 {"data":{"run_id":9,"status":"accepted"}}

POST /api/agents/sessions/1/stop
-> 200 {"data":{"status":"stopping"}}
-> 200 {"data":{"status":"idle"}}
```

**Message 详情**

```json
{
  "id": 42, "session_id": 1, "run_id": 9, "role": "assistant",
  "content": [{"type":"text","text":"..."}],
  "reasoning": "...", "tool_calls": [{"id":"call_1","name":"file.read","arguments":"{\"path\":\"README.md\"}"}],
  "tool_call_id": null, "tool_name": null,
  "usage": {"prompt_tokens":1200,"completion_tokens":300,"reasoning_tokens":800,"total_tokens":2300},
  "created_at": "..."
}
```

### 10.3 校验与错误

| 场景 | 状态/码 | details |
|---|---|---|
| 会话/profile/消息不存在 | 404 `not_found` | — |
| 会话已有 running run | 409 `conflict` | `reason="run_active"` |
| 删除时 run 5s 内未停 | 409 `conflict` | `reason="run_active"` |
| profile 字段非法 | 422 `invalid_argument` | `field` |
| profile 被禁用 | 409 `conflict` | `reason="profile_disabled"` |
| 默认 LLM 未配置/Key 不可解密 | 503 `unavailable` | `reason="llm_not_configured"` |
| 默认配置协议为 responses | 503 `unavailable` | `reason="tools_require_chat_completions"` |
| 全局并发满 | 503 `unavailable`, retryable | `reason="capacity"` |
| 非管理员 | 403 `forbidden` | — |

- 不新增公共错误码；内部错误码（tool `error_code`、run `error_code`）放在响应/事件 detail 内。
- PATCH profile 允许字段：`system_prompt`、`model`、`allowed_tools`、`max_turns`、`run_seconds`、`hard_timeout_seconds`、`thinking`、`reasoning_effort`、`max_output_tokens`、`enabled`；`key`/`builtin` 不可改。
- PATCH session 仅允许 `title`（0–120 字符）。
- 校验范围：`max_turns 0..100`、`run_seconds 30..3600`、`hard_timeout 5..300`、`max_output_tokens 1024..131072`、`system_prompt 1..32768` 字符、`allowed_tools ⊆ 注册表`、`thinking=disabled` 时 `reasoning_effort ∈ {"","none"}`。
- Profile 更新写审计；运行中修改只影响下一次 run。

### 10.4 OpenAPI

- `docs/openapi/p0.yaml` 改名 `docs/openapi/api.yaml`，补 P1 路径与 DTO；`EZTEST_OPENAPI_PATH` 默认值与测试同步更新。
- 保留 `GET /api/openapi.yaml` 公开只读。

---

## 11. 前端

### 11.1 路由与结构

- `/agents`：Agent 控制台（`?session={id}` 深链）。`nav-items.ts` 中该条目 phase 改为 `P1`，hint 改为「Agent 运行时控制台（P8 扩展为 Agent 中心）」。
- 普通用户：显示管理员专属空状态；不渲染控制台。
- 布局：顶部工具栏（profile 选择器、新建会话、连接状态）；左侧会话列表（280px）；右侧 transcript + composer；配置 Tab。

### 11.2 组件职责

| 组件 | 职责 |
|---|---|
| `AgentConsole` | 加载 profiles/sessions；维护选中会话；Tabs（会话/配置） |
| `SessionList` | 新建/选择/删除；running 脉冲；`limit/cursor` 分页 |
| `Transcript` | 按 run 分组渲染活动；自动滚底（用户上滚时暂停）；「加载更早」 |
| `ActivityItem` | 按 `detail.kind` 分发；未知 kind 走通用行 |
| `ThinkingBlock` | 默认折叠，标「含推理」；展开懒加载完整 reasoning |
| `ToolCallBlock` | 一行折叠：工具名/参数摘要/状态/耗时；展开懒加载完整参数与结果；`call_id` 配对 |
| `Composer` | 发送/停止；Enter 发送、Shift+Enter 换行；运行中禁用；409/503 提示 |
| `ProfileEditor` | 提示词/模型/预算/思考/输出上限/工具勾选/启用；保存提示「下次运行生效」 |

- 不新增前端依赖；文本用 `whitespace-pre-wrap`（Markdown 渲染属 P5/P8）。
- 所有请求走 `api-client`，SSE 只走 `sse-client`（`Agent.md` §6.4）。

### 11.3 数据流

1. 选中会话：并行 `GET session` + `GET /api/stream/events?channel=agent:{id}&limit=50`。
2. 渲染历史，记录 `snapshot_cursor`。
3. `connectSSE({url, token, lastEventId: snapshot_cursor})`。
4. `onEvent`：解析 JSON，仅处理 `type=agent.activity`；按 `seq` 去重；thinking/text 按 `(message_id, part)` 合并；`run_finished` 后重取 session。
5. `onStatus`：`connecting/reconnecting/closed` 显示连接条；**重连中不得显示“暂无消息”**。
6. `onReset`：重取快照并清空。
7. 发送：POST → 禁用输入；收到 `run_finished` 恢复；409 时提示「上一条仍在执行」。
8. 停止：POST → 按钮显示「停止中」；`run_finished` 后恢复。
9. 刷新页面 = 从事件完整回放。

### 11.4 状态与可访问性

- 空状态：无会话、无消息、profile 未配置 LLM、管理员专属。
- 错误：加载失败保留上次数据 + 重试；run 失败显示 `terminal_reason` 与错误码（不只靠颜色）。
- 折叠控件 `aria-expanded`；按钮有 `aria-label`；键盘可达；焦点保持在 composer。
- 断线显示「重连中」，`closed` 显示「已断开」+ 手动重连按钮。

### 11.5 前端测试

- Vitest：事件合并/去重/分片拼接、折叠展开、停止按钮状态、配置表单校验、错误/空状态。
- Playwright：见 §12.3。

---

## 12. 测试与验收

### 12.1 分层

| 层 | 覆盖 |
|---|---|
| Go 单元 | 上下文截断（reasoning/tool 配对）、预算、终态映射、活动构造与限长、工具白名单/路径穿越/软链/deny list/HTTP 白名单/重定向/大小限制、panic→错误、流式 tool_calls 装配、reasoning 回传、usage 解析 |
| Go 集成 | 迁移幂等；仓储；CreateTurn 原子性与单飞；run 生命周期（completed/max_turns/timeout/model_error/context_overflow/aborted/interrupted）；fake 上游断言请求体（thinking/effort/max_tokens、第二轮 reasoning_content）；工具超时/panic；abort 取消在飞流；事件回放；启动 interrupted 清理；401/403/404/409/503 |
| 前端单元 | 见 §11.5 |
| E2E | 见 §12.3 |
| Spike | Playwright 最小执行 + storageState 注入，见 §12.5 |
| Gated 真实 | `deepseek-flash` + thinking=max + 原生工具调用一轮（`scripts/acceptance-agent-llm.ps1`） |

### 12.2 fake 上游（E2E 与非 gated 集成）

- `scripts/fake-llm.cjs`：Node HTTP 服务，实现 `POST /v1/chat/completions`（流式/非流式）与 `GET /v1/models`。
- 行为脚本：
  - 首次请求：返回 `reasoning_content` + 一个 `file.read`（或 `http.get`）工具调用；
  - 工具结果回合：**断言历史 assistant 消息带 `reasoning_content`**，否则返回 400（让契约失败可见）；返回最终文本；
  - 断言 agent 请求含 `thinking.type=enabled`、`reasoning_effort=max`、`max_tokens=32768`（可在 E2E 中通过请求日志校验）；
  - 触发词 `SLOW`：慢速分片，用于中止测试；
  - 触发词 `FAILTOOL`：请求 `test.fail` 工具。
- 请求日志写入 `.tmp/fake-llm-requests.jsonl` 供 Playwright 断言；E2E 结束清理。

### 12.3 E2E 场景（`frontend/e2e/agent-console.spec.ts`）

1. 管理员登录 → `/agents` → 新建会话。
2. 发送诊断消息 → 实时看到 `run_started`、`thinking`、`text`、`tool_use`、`tool_result`、`usage`、`run_finished`。
3. 展开工具块 → 断言参数/结果；断言 `call_id` 配对。
4. 追问 → 同一会话第二个 run；断言上下文含前一轮。
5. 发送 `SLOW` → 点停止 → 断言 `aborted` 与原因；服务仍可继续发新消息。
6. 刷新页面 → 两次 run 完整回放。
7. 配置 Tab 改提示词 → 保存 → 下一次 run 生效（fake 断言 system prompt 含标记）。
8. 先由管理员创建一名普通用户，再以普通用户登录 → `/agents` 空状态；API 403。

### 12.4 手动验收（演示动线）

1. 管理员登录 → `/agents` → 新建会话。
2. 发送：`请检查 EzTest 运行时健康，并读取 README 的构建说明。`
   - 期望：`run_started` → `thinking` → `text` → `tool_use(http.get /api/health)` → `tool_result(200)` → `tool_use(file.read README.md)` → `tool_result` → `text` → `usage` → `run_finished(completed)`。
3. 追问：`把健康检查结果和构建步骤总结成三点。`
4. 再发一条并立即停止 → `aborted` + 正确原因；服务正常。
5. 断网/恢复 → 「重连中」→「实时」，seq 连续。
6. 配置 Tab 改提示词 → 下一次 run 生效。
7. 普通用户 → 空状态 + 403。
8. 运行中重启 API → 重启后 run 显示 `interrupted`。
9. 提示 Agent 调用 `test.fail(error/timeout/panic)` → run 继续，工具块显示失败原因。
10. `EZTEST_RUN_REAL_LLM=1 scripts/acceptance-agent-llm.ps1` → 真实思考模式 + 工具调用通过。

### 12.5 Playwright 最小执行 + Session 注入 spike（不接入业务）

- 目的：验证 P6 依赖的两个高风险假设，不把浏览器能力接入运行时。
- 脚本：`frontend/scripts/spike-playwright-session.mjs`（Node + 已安装的 `@playwright/test` 库，便于模块解析），使用本机 Chrome/Edge channel，**不下载 Chromium**。
- 步骤：
  1. 启动 API/Web（复用 `scripts/dev-api.ps1` / `dev-web.ps1`，由脚本前置检查）；
  2. 打开 `http://localhost:6868/login`，用管理员账号登录；
  3. `context.storageState({path: ".tmp/spike-storage-state.json"})` 保存会话；
  4. 新建 context 并注入该 storageState；**由于 P0 的 token 存在 `sessionStorage`（Playwright `storageState` 不含 `sessionStorage`），还需用 `addInitScript` 恢复登录态**；访问受保护页面 `/` 与 `/agents`；
  5. 断言页面显示已登录（用户菜单/控制台），截图 `.tmp/spike-shell.png`；
  6. 输出结构化结果（步骤、耗时、截图路径、成功/失败）。
- **Spike 结论（2026-10-27）**：`storageState` 只保存 cookies/localStorage，不含 `sessionStorage`；纯 storageState 注入无法恢复 EzTest 登录（状态文件 36B，`/` 被重定向 `/login`）。可行路径 = storageState + `addInitScript` 恢复 `eztest.token`/`eztest.user`。**P6 的 Session 复用必须持久化并注入 sessionStorage，或把 token 迁移到 localStorage/cookie；这是 P6 的输入约束。**
- 明确不做：不写入 Agent 工具、不进入 API、不用于 P1 验收的 Agent 链路；脚本失败只报告，不阻塞 P1 主验收（P6 前置风险提前暴露）。
- 成本：每轮几秒；不产生 LLM 费用。

### 12.6 DoD 映射

| DoD（`implementation-phases.md` L105–108） | 证据 |
|---|---|
| 多轮对话 + 工具调用 + 结果，实时可见、可回放 | E2E 场景 1–6；集成测试事件/消息一致 |
| 可中止；工具错误/超时不拖垮服务 | E2E 场景 5；集成 abort/timeout/panic；API 存活断言 |
| 配置改动可生效 | 集成 fake 断言 system prompt；E2E 场景 7 |
| 事件/工具契约冻结 V1 | 本规格 §9/§8 + 契约测试 + `api.yaml` |

---

## 13. 风险与降级

| 风险 | 处理 |
|---|---|
| 思考模式 + tools 的 `reasoning_content` 回传缺失 → 400 | 列为硬契约；fake 上游与真实 gated 测试都断言；上下文构建单测覆盖 |
| max 档思考延迟/成本高 | `max_output_tokens` 32768 上限；`run_seconds=600`；可随时中止；运维建议 LLM 超时 ≥180s |
| 网关兼容性（自定义 base_url） | `thinking=auto` 时不发思考参数；`reasoning_content` 仅在非空时回传 |
| 双写不一致 | messages 先落库为权威；事件失败仅告警；历史从事件、详情从 messages |
| 上下文溢出 | 整轮截断 + decision；仍超 → `failed/context_overflow` |
| 工具安全 | 根目录 + EvalSymlinks + deny list；HTTP 白名单 + 重定向校验；审计脱敏 |
| 管理端误配 responses 协议 | 启动前 503 明确原因，不产生半成品 run |
| 全局并发失控 | 信号量 + 503 capacity；单飞由唯一索引兜底 |
| P1 admin-only 过粗 | 规格标注；P6 引入资源级 ACL 与 ChannelAuthorizer |
| E2E fake 与真实网关漂移 | fake 按官方 wire 实现并校验 reasoning；真实调用 gated |
| P0 回归 | 不改 SSE V1 契约；P0 测试全量回归；OpenAPI 改名同步 |
| 进程崩溃留下 running run | 启动清理为 `interrupted`；用量保留已完成回合 |

---

## 14. 交付物

### 14.1 对 P0 的改动清单

1. `llm/application` 类型扩展（ToolSpec/ToolCall/Reasoning/Thinking/ReasoningEffort/Usage.ReasoningTokens）；`deepseek/chat.go` 适配；`responses.go` 拒绝 tools。**P0 管理端 LLM 测试行为不变**。
2. `config`：新增 4 个 agent env；`EZTEST_OPENAPI_PATH` 默认改 `docs/openapi/api.yaml`。
3. `server.Dependencies` 增加 agent 服务；挂载 `/api/agents/*`；为 `/api/stream` 与 `/api/stream/events` 增加 `agent:` 通道管理员守卫。
4. `cmd/eztest/main.go`：装配 agent 仓储/服务/工具注册表；启动种子与 interrupted 清理；SIGINT/SIGTERM 时调用 `RunService.Shutdown`。
5. 新迁移 `agent_0001_profiles_sessions_runs_messages.sql` + `order.txt`。
6. `audit` 新 action 常量与接线。
7. `docs/openapi/p0.yaml` → `api.yaml`（补 P1 路径）；P0 相关测试与 README 同步。
8. `frontend/src/navigation/nav-items.ts` 与 `nav-items.test.ts`：`/agents` phase 改 P1。
9. `scripts/dev-api.ps1` / `e2e.ps1` / `acceptance.ps1` 更新；新增 fake 上游与 gated 验收脚本。
10. **不改** P0 SSE V1 信封/端点语义、认证中间件、错误信封、幂等中间件。

### 14.2 交付清单

- `backend/internal/agent/**` 四层实现、迁移 `agent_0001_*`、启动种子与清理。
- `llm` 网关 tools/reasoning/thinking 扩展与适配器测试。
- `/api/agents/*`、SSE 通道守卫、`docs/openapi/api.yaml`。
- `frontend/src/features/agent-console/**`、`/agents` 页面、Vitest。
- `frontend/e2e/agent-console.spec.ts`、`scripts/fake-llm.cjs`、`scripts/e2e.ps1` 更新。
- `frontend/scripts/spike-playwright-session.mjs`（风险 spike，不接入运行时）。
- `scripts/acceptance-agent-llm.ps1`（gated）、`scripts/acceptance.ps1` P1 检查。
- 文档：本规格、计划、README、`.env.example`、`docs/dev-setup.local.md` 路径说明。
- 验收证据（命令输出、E2E 结果、gated 真实调用记录、spike 截图）。

---

## 15. 设计变更与文档同步（实现前完成）

> 依据 `Agent.md` §3/§5.3：设计变更先记录 D#、更新受影响章节，再实现；禁止代码先行。

### 15.1 D27（写入 `system-blueprint.md` §3.2 决策记录）

> **D27**：P1 内置的「诊断 Agent」= **运行时验证夹具**（key=`runtime-diagnostic`），不进入 §6.1 十三角色；提示词/模型/思考强度/预算/工具白名单管理员可配；工具仅只读白名单（文件只读、HTTP 只读、测试工具）；活动事件统一为 `agent.activity`（`detail.kind` 区分 run_started/user_message/thinking/text/tool_use/tool_result/usage/decision/run_finished）；`agent_messages` 为 LLM 上下文权威来源，`events` 为其 UI 投影；P2 起由业务 Agent 替换，不改变运行时契约。

### 15.2 同步更新

- `system-blueprint.md` §6.3：补一句「P1 提供不进入十三角色的运行时验证 Agent，仅用于验收运行时」。
- `system-blueprint.md` §15 P1 行注：指向 D27。
- `implementation-phases.md` §2-P1：在「诊断 Agent」处加括号「运行时验证夹具（非业务角色，见 D27）」。
- `frontend-design.md` §4.2 已注明「P1 起有 Agent 会话视图」，无需改。
- `Agent.md` §4 文档地图：无需新增文档（规格仍在 `docs/superpowers/specs/`）。

### 15.3 规格内修订（相对设计讨论稿）

- **R1**：思考模式与强度显式可配（`thinking`/`reasoning_effort`），内置 profile 默认 `enabled + max`（替换原「不显式发送 thinking」）。
- **R2**：输出上限进 profile（`max_output_tokens` 默认 32768），不留 8192 硬顶。
- **R3**：预算适配思考模式（`run_seconds=600`），并给出 LLM 超时运维建议。
- **R4**：thinking/text 长片段每 1500ms 或 1024 字符增量 flush（`final=false`），保证实时可见；仍不逐 token。
- **R5**：新增 `aborted`（回合间取消）终态原因，避免把“回合间中止”误标为流式/工具中止。

---

## 16. 明确不做（再次确认）

- 13 个业务 Agent、双图/黑板、多 Agent 协作、子 Agent、steer。
- 暂停/恢复（优雅/立即）、settlement 收尾轮、上下文压缩。
- 浏览器正式执行、DB 断言、业务工具。
- 版本/需求/用例/执行/证据/问题/报告实体与页面。
- 工具描述 DB 化、提示词版本化、MCP/Skills。
- 多实例 SSE、消息队列、多租户、复杂 RBAC。
- 资源级权限（P1 admin-only）。

---

## 17. 已知假设

1. 默认 LLM 配置为 `deepseek-flash`（`https://api.deepseek.com`，`chat_completions`），Key 可解密；上游行为与官方文档一致（Tool Calls、思考模式、reasoning_content 回传要求）。
2. `deepseek-flash` 思考模式默认开启；`reasoning_effort=max` 可用；工具调用与思考模式可同时工作。
3. 单实例部署；进程内 runner 与 hub 足够。
4. 管理员信任其配置的 `base_url`；P1 不针对 LLM 网关做 SSRF 白名单。
5. 本机 Chrome/Edge 可用于 Playwright；不下载 Chromium。
6. `EZTEST_AGENT_FILE_ROOT` 在本地开发指向仓库根；未配置时文件工具不注册（功能仍可通过 http/test 工具验收）。
7. P1 的「回放」= 从持久化事件/消息重建历史视图，不含动画式逐步回放。
8. 事件发布失败可接受（消息已落库），以历史接口补偿。

---

*文档版本：v1.0（2026-10-27）；与 system-blueprint v2.2（D1–D26，待增 D27）、implementation-phases v1.0 配套。*
