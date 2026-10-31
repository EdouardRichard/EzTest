# EzTest 0.2 P1 Agent Profile 与工具契约

> 状态：`frozen: P1`（2026-10-31；P2+ 只允许增量，破坏性变更走变更控制）。
> 权威：`architecture-design.md` §7、`high-level-design.md` §4–§5、`phase-01-runtime-clarification.md`、`open-decisions.md`。
> 实现：`backend/internal/agent/application`（profiles/tools/runner）与 `backend/internal/llm`（chat gateway）。

## 1. 作用域注入（强制）

每次 run 由 runtime 注入：

```json
{
  "version_id": "1001",
  "conversation_id": "2001",
  "requirement_id": "3001",
  "agent_run_id": "4001",
  "version_sut_ids": ["5001"]
}
```

- 工具 JSON Schema **不得**包含 `version_id`/`versionId`；模型只传业务参数。
- 服务端用注入 scope 读写；跨版本/不存在/已删除引用 → 结构化 `not_found`/`invalid_argument`，不泄露其他版本数据。
- 写工具只能提交候选；服务端校验作用域、状态、引用、字段、去重；AI 不能确认/删除/关闭。
- 工具结果统一 JSON；超 `agent.tool_result_max_bytes`（32768）落盘为 text content_object，返回 `{head,sha256,content_ref,truncated:true}`。
- 所有工具调用写 `agent_message`（role=assistant tool_calls / role=tool）；写操作审计带 `agent_run_id`。

## 2. P1 Profiles（启动幂等 seed）

| key | 名称 | 模型 | 工具 | budgets |
|---|---|---|---|---|
| `clarification_host` | 澄清 Host | 默认 LLM 模型 | `context.version_pack`、`context.task_pack`、`context.team_room_read`、`context.summary_update`、`requirement.read`、`attachment.list`、`attachment.read_text`、`sut.list`、`sut.page_list` | `{max_turns:12,soft_seconds:300,hard_seconds:600}` |
| `requirement_curator` | 需求 Curator | 默认 LLM 模型 | Host 读工具 + `requirement.draft_create`、`requirement.draft_update`、`requirement.relation_propose`、`requirement.reopen_propose` | `{max_turns:8,soft_seconds:300,hard_seconds:600}` |
| `context_scout` | 上下文 Scout | 默认 LLM 模型 | `attachment.list`、`attachment.read_text`、`attachment.image_ref`、`sut.list`、`sut.page_list`、`context.team_room_read` | `{max_turns:4,soft_seconds:120,hard_seconds:300}` |

system prompt 必须包含 `[profile:<key>]`（fake LLM 识别；真实模型忽略标记），并声明角色边界：
- Host：只对话/解释/提问/复述，不确认需求、不删除；可更新会话摘要。
- Curator：只产 draft/关系/回退提议；不确认、不删除、不改 clarified 版本。
- Scout：只读附件/页面/团队帖，产 finding；不改需求。

## 3. P1 工具清单与 Schema

所有 schema 使用 JSON Schema（object；`additionalProperties:false`）。下表的 `scope` 由 runtime 注入，不出现于 schema。

### 3.1 上下文
| 工具 | 参数 | 返回 |
|---|---|---|
| `context.version_pack` | `{}` | 版本摘要、SUT/页面计数、需求摘要、附件元数据 |
| `context.task_pack` | `{}` | 当前任务目标/约束/验收/scope |
| `context.team_room_read` | `{max_posts?:1..50}` | 最新 digest + 相关帖子 |
| `context.summary_update` | `{summary:string(0..8000),pinned_facts:string[](0..20)}` | 新摘要版本 |

### 3.2 需求
| 工具 | 参数 | 校验/返回 |
|---|---|---|
| `requirement.read` | `{requirement_id?:string}` | 无 id 返回版本内列表；有 id 返回详情；跨版本 404 |
| `requirement.draft_create` | `{title:string(0..200),description:string(0..20000),acceptance_criteria:string(0..20000),priority:"P0".."P3",constraints?:object,target_version_suts?:string[],target_pages?:string[],target_apis?:string[]}` | 创建 requirement+revision1 draft；target 必须在版本绑定；返回 `{requirement_id,version_id,revision,status}` |
| `requirement.draft_update` | `{requirement_id:string, ...同上}` | 仅 draft；生成 revision+1；clarified → `state_conflict`（提示 reopen_propose） |
| `requirement.relation_propose` | `{from_requirement_id:string,to_requirement_id:string,kind:"depends_on"|"conflicts_with"|"overlaps"|"shares_scope",description?:string(0..1000)}` | 同版本、非自环、唯一；落库候选 |
| `requirement.reopen_propose` | `{requirement_id:string,reason:string(1..2000)}` | 只写 conversation_message part `rollback_proposal`；不改状态 |

### 3.3 附件
| 工具 | 参数 | 返回 |
|---|---|---|
| `attachment.list` | `{}` | 当前版本/会话附件元数据（id/name/mime/size/status） |
| `attachment.read_text` | `{attachment_id:string,offset?:int,max_chars?:int(1..20000)}` | 提取文本分页 + `truncated` |
| `attachment.image_ref` | `{attachment_id:string}` | `{sha256,url,mime}`；P1 不做视觉理解 |

### 3.4 资源只读
| 工具 | 参数 | 返回 |
|---|---|---|
| `sut.list` | `{}` | 当前版本绑定的 SUT/环境/version_sut 摘要 |
| `sut.page_list` | `{version_sut_id?:string}` | 页面/API 清单（只读） |

`conversation.reply` 在 P1 不是工具：assistant 最终文本直接写 `conversation_message`。

## 4. P2 Profiles（启动幂等 seed；`frozen: P2`，2026-10-31）

| key | 名称 | 工具 | budgets |
|---|---|---|---|
| `design_host` | 设计 Host | `context.version_pack`、`context.task_pack`、`context.team_room_read`、`context.summary_update`、`requirement.read`、`design.graph_read`、`case.graph_propose`、`attachment.list`、`attachment.read_text`、`sut.list`、`sut.page_list` | `{max_turns:12,soft_seconds:300,hard_seconds:600}` |
| `design_planner` | 设计 Planner | `context.version_pack`、`context.task_pack`、`context.team_room_read`、`requirement.read`、`design.graph_read` | `{max_turns:6,soft_seconds:180,hard_seconds:300}` |
| `case_author` | 用例 Author | `context.version_pack`、`context.task_pack`、`requirement.read`、`design.graph_read`、`case.graph_propose`、`sut.list`、`sut.page_list` | `{max_turns:8,soft_seconds:300,hard_seconds:600}` |
| `design_critic` | 设计 Critic | `context.task_pack`、`design.graph_read`、`design.review_submit` | `{max_turns:4,soft_seconds:120,hard_seconds:300}` |
| `data_planner` | 数据 Planner | `context.task_pack`、`design.graph_read`、`design.data_plan_submit` | `{max_turns:4,soft_seconds:120,hard_seconds:300}` |

system prompt 必须包含 `[profile:<key>]`，并声明：AI 只能提交 draft/候选，不能确认/删除/关闭；Host 只解释/修改脑图，不确认。

## 4a. P2 工具（注册；scope 由 runtime 注入）

- 读：`design.graph_read`（`{requirement_id?,case_id?}` → 图摘要/用例详情 + `graph_hash`）；复用 P1 `requirement.read`、`attachment.*`、`sut.*`、`context.*`。
- 写（候选提交 + 服务端校验）：
  - `case.graph_propose`：`{requirement_id,requirement_version_id,base_graph_hash?,scenarios[],cases[]}`；单事务、自然键去重、内容 hash 幂等；返回 `{scenario_ids,case_ids,case_version_ids,graph_hash}`。
  - `design.review_submit`：`{summary,findings[{severity,message,requirement_id?,case_id?}]}`；落团队 finding + `design.review_submitted`；不建人工评审表。
  - `design.data_plan_submit`：`{case_version_id,preconditions?,test_data?}`；仅当前 draft；生成新 draft revision。
- 不注册 `case.confirm`（仅服务端/用户）、`design.scenario_propose`/`design.case_propose`/`design.case_update`（由 `case.graph_propose` 统一承载）。
- 工具 schema 不包含 `version_id`/`versionId`；作用域键：`version_id/conversation_id/requirement_id/requirement_version_id/case_id/case_version_id/round_id/version_sut_ids`。

## 4b. P4+ 预留工具（不注册）

`requirement.goal_met`、`code.*`、`issue.*`、`report.*`、`knowledge.*`、`version.close_propose`、`budget.check`、`sut.page_propose`。P3 已启用的执行/等待/接管工具见 §6；其余 P4+ 工具写入本文件并实现服务端校验后启用。

## 5. 错误返回（给模型）

```json
{"error":{"code":"invalid_argument|not_found|state_conflict|lease_lost|ai_unavailable","message":"...","details":{"field":"..."}}}
```

模型可修正后重试；不可恢复错误（跨版本、越权、状态非法）直接返回，tool result 记录，run 可继续/结束。服务端不得把内部 SQL/密钥/路径放入错误。

---

## 6. P3 Profiles 与工具（`frozen: P3`，2026-11-01）

> 来源：`docs/superpowers/specs/2026-11-01-eztest-v02-p3-execution-design.md` §7/§6.1/§6.10（P3-D-10/P3-D-15/P3-D-41）。
> 实现：`backend/internal/agent/application`（P3 profile seed、任务上下文、工具注册）与 `backend/internal/execution`（服务端校验/提交）。
> 执行主体：API 内 Case Executor Agent 决定做什么，独立 executor 只执行动作/回传证据，不接触 LLM 上下文；执行 Planner 是确定性 runtime，不注册 profile。

### 6.1 Profiles（启动幂等 seed，不覆盖管理员修改）

| key | 名称 | budgets | 工具 |
|---|---|---|---|
| `case_executor` | 用例执行器 | `{max_turns:60, soft_seconds:780, hard_seconds:900}` | §6.3 执行工具 + §6.4 只读工具 |
| `failure_analyst` | 失败分析师 | `{max_turns:6, soft_seconds:180, hard_seconds:300}` | `context.task_pack`、`context.team_room_read`、`execution.result_read`、`execution.evidence_read`、`analysis.failure_submit` |
| `coverage_analyst` | 覆盖分析师 | `{max_turns:6, soft_seconds:180, hard_seconds:300}` | `context.task_pack`、`requirement.read`、`execution.coverage_read`、`analysis.coverage_submit` |

- system prompt 必须包含 `[profile:<key>]`（fake LLM 识别；真实模型忽略标记）。
- `case_executor` prompt 必须声明：严格按用例步骤/断言执行；每步完成后 `execution.step_record`；每条断言后 `execution.assertion_record`；**步骤要求等待/时间窗口时调用 `execution.wait`（不要用重复 snapshot 空转，也不要假设等待后结果）**；不得修改需求/用例/确认；登录失败/2FA 调 `human.request`；无法继续时 `execution.case_finish(blocked)`；不得虚构 passed。
- `case_executor` 不注册 `case.confirm`/`requirement.*` 写工具，不能关闭版本/合并问题；failure/coverage analyst 不注册浏览器或执行写工具。

### 6.2 作用域注入（强制）

每次 run 由 runtime 注入；工具 JSON Schema **不含** `version_id`/`versionId`：

```json
{
  "version_id": "1001",
  "wave_id": "8001",
  "case_run_id": "8101",
  "intent_id": "8201",
  "case_version_id": "3001",
  "version_sut_ids": ["5001", "5002"],
  "agent_run_id": "4001",
  "dispatch_epoch": "3",
  "dispatch_token": "0123456789abcdef0123456789abcdef"
}
```

- 工具参数只允许业务字段（`version_sut_id?`/`account_role?` 等局部选择器）；`version_id/wave_id/case_run_id/intent_id` 由 scope 注入。
- 跨 scope 引用、不存在、scope 已失效 → `not_found`/`state_conflict`；写结果必须匹配 `dispatch_epoch+dispatch_token`，否则 409 `lease_lost` 且不覆盖。
- 工具结果统一 JSON；超 `agent.tool_result_max_bytes`（32768）落盘为 text content_object，返回 `{head,sha256,content_ref,truncated:true}`；所有调用写 `agent_message`；写操作审计带 `agent_run_id`。

### 6.3 执行工具（`case_executor`；scope 由 runtime 注入）

**执行/浏览器**（每项可选 `version_sut_id?`/`account_role?` 选择 `(version_sut, account_role)` 上下文；必须属于 scope 的 version_sut 集合）

| 工具 | 参数 | 说明 |
|---|---|---|
| `browser.goto` | `{version_sut_id?,account_role?,url:string}` | 打开页面；懒建 context；host 必须属于该 SUT 环境 |
| `browser.snapshot` | `{version_sut_id?,account_role?}` | 可访问性快照（≤12KB） |
| `browser.click` | `{version_sut_id?,account_role?,target:Target}` | `Target={role?,name?,exact?}|{text?}|{test_id?}|{selector?}` 恰一种 |
| `browser.fill` | `{version_sut_id?,account_role?,target:Target,value:string,secret?:bool}` | `secret=true` 由执行器擦除并纳入脱敏集合 |
| `browser.press` | `{version_sut_id?,account_role?,target?:Target,key:string}` | Enter/Tab 等 |
| `browser.wait_for` | `{version_sut_id?,account_role?,target?:Target,timeout_ms?:int(100..60000)}` | 等待可见；≤60s 可见性等待 |
| `browser.screenshot` | `{version_sut_id?,account_role?,full_page?:bool}` | 自动落 `screenshot` 证据 |
| `browser.console` | `{version_sut_id?,account_role?,limit?:int(1..200)}` | 自动落 `console` 证据 |
| `browser.network` | `{version_sut_id?,account_role?,limit?:int(1..200)}` | 自动落 `network` 证据 |
| `browser.download` | `{version_sut_id?,account_role?,target:Target,filename?:string}` | 自动落 `download` 证据 |
| `browser.storage_state` | `{version_sut_id?,account_role?}` | 导出 storageState 摘要；P3 不落库、不返回明文 |
| `http.request` | `{version_sut_id?,api_id?,method?,path:string,headers?:object,body?:string}` | 只允许 SUT 环境 host（防 SSRF）；自动落 `http` 证据；敏感 header 脱敏 |
| `db.query` | `{version_sut_id?,database_id?,sql:string,max_rows?:int(1..200)}` | 只读 PostgreSQL（单条 SELECT/WITH、5s、≤200 行/≤64KB）；自动落 `db` 证据 |
| `database.schema_read` | `{version_sut_id?,database_id?,schema?:string}` | 表/列摘要（只读，≤64KB） |
| `account.credentials` | `{version_sut_id?,role:string}` | 按 role 解析账号并返回 username/password；审计 `execution.credentials_read`；不注入上下文/证据 |
| `execution.step_record` | `{seq:int,status,observed?:string,error?:string,version_sut_id?}` | upsert；发 `case_run.step_finished`；epoch/token CAS |
| `execution.assertion_record` | `{step_seq:int,assertion_seq:int,verdict,expected?:string,actual?:string,reason?:string,version_sut_id?}` | expected 由服务端用 P2 值覆写；发 `case_run.assertion_finished` |
| `execution.evidence` | `{kind:"text",text:string,step_seq?,assertion_seq?}` | 文本证据；二进制只经截图/下载 |
| `execution.wait` | `{seconds:int(1..300),reason?:string(0..200)}` | **API 侧可中断等待**：页面定时器继续、不占执行器 action 时限；累计计入 `execution.case_timeout_seconds`；暂停/取消/lease 丢失可打断（§6.10） |
| `execution.case_finish` | `{verdict,summary?,failure_class?}` | 只提交候选；服务端按四态聚合规则校验/覆写并返回结构化说明 |
| `human.request` | `{kind:"login_takeover",reason,version_sut_id?,account_role?,challenge_kind?}` | 暂停用例/波次等待用户；不能自我 resolve；同一 case 最多 3 次 |

**Target/限额**：`Target` 恰好一种；零匹配 `target_not_found`、多匹配 `target_ambiguous`；执行器请求体 ≤1MB、单 action 15s（max 60s，goto 30s）、artifact ≤10MB、snapshot ≤12KB。

### 6.4 只读/复用工具（`case_executor`）

`context.task_pack`、`context.version_pack`、`context.team_room_read`、`requirement.read`、`sut.list`、`sut.page_list`、`design.graph_read`。

- 只读；作用域同 scope；不返回密码、内部路径、执行器 token。

### 6.5 失败/覆盖分析工具

| 工具 | 参数 | 服务端校验/结果 |
|---|---|---|
| `execution.result_read` | `{case_run_id?}` | wave scope 内结果/断言/证据摘要（只读） |
| `execution.evidence_read` | `{evidence_id,offset?,max_chars?}` | 证据元数据/文本片段；不返回执行器路径 |
| `execution.coverage_read` | `{}` | 需求/用例/执行覆盖摘要 |
| `analysis.failure_submit` | `{case_run_id,failure_class,category,severity,summary,dedupe_hint?}` | 只能细化 wave 内 case_run；不得把非 `application` 升级为问题候选；写 `failure_class`/`summary` + 事件/帖 |
| `analysis.coverage_submit` | `{requirement_id,reason,evidence_case_run_ids?}` | requirement 须属于版本、reason 合法、证据 case_run 属于 wave；ensure `design.gap` + 帖/事件 |

- 确定性兜底（wave 终态）先写分类/缺口；Analyst 失败/超时不阻塞 wave（P3-D-18）。

### 6.6 工具通用校验与错误

1. 工具 schema 不出现 `version_id`/`versionId`；所有 scope 读写由 runtime 注入。
2. 写工具只能提交候选：`execution.case_finish` 不能直接改终态；`expected` 由服务端覆写；证据 sha/size 由 API 重算。
3. 证据：kind 8 类；每 `case_run` ≤100 条、单条 ≤ `execution.evidence_max_bytes`（5 MiB）；超限返回 `evidence_too_large`，用例继续；`paths.evidence_dir` 不使用。
4. `execution.wait`：单次 1–300s；累计计入 case 超时；不产生证据；不可用时返回 `state_conflict`/`lease_lost`。
5. 人机请求：`open` 时 case_run/wave `paused(reason=waiting_human)`、work item `waiting_human`；resolve 一次性响应不落事件/日志/证据。
6. 错误统一返回 `{"error":{"code":"invalid_argument|not_found|state_conflict|lease_lost|ai_unavailable|evidence_too_large|executor_unavailable","message":"...","details":{...}}}`；不含内部 SQL/密钥/路径。
7. `context_overflow` 不是失败类（P1 压缩/重试）；`execution.wait` 不占执行器 action 时限（执行器不实现 sleep）。
