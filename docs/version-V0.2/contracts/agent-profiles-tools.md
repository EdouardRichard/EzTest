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

## 4. 预留工具（P2+，不注册）

`requirement.goal_met`、`case.confirm`、`case.graph_propose`、`design.*`、`code.*`、`browser.*`、`http.request`、`db.query`、`execution.*`、`issue.*`、`report.*`、`knowledge.*`、`version.close_propose`、`human.request`、`budget.check`、`account.credentials`、`database.schema_read`、`sut.page_propose`。P2+ 写入本文件并实现服务端校验后启用。

## 5. 错误返回（给模型）

```json
{"error":{"code":"invalid_argument|not_found|state_conflict|lease_lost|ai_unavailable","message":"...","details":{"field":"..."}}}
```

模型可修正后重试；不可恢复错误（跨版本、越权、状态非法）直接返回，tool result 记录，run 可继续/结束。服务端不得把内部 SQL/密钥/路径放入错误。
