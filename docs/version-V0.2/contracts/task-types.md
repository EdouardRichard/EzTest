# EzTest 0.2 P1 任务类型契约

> 状态：`draft: P1`（P1 阶段末冻结；P0 未创建本文件）。
> 权威：`docs/version-V0.2/high-level-design.md` §3、`architecture-design.md` §5.3、`phases/phase-01-runtime-clarification.md`。
> 实现：`backend/internal/agent`（work_item/worker/reconciler）。P2+ 类型只保留，不在 P1 创建。

## 1. 通用模型

`work_item` 是唯一执行队列；状态：

`open → claimed → running → done | failed | dead`；`running → waiting_human`（P3+ 使用）；`failed → open`（重试）或 `dead`（不可重试/超限）。

| 字段 | 语义 |
|---|---|
| `version_id` | 版本作用域 |
| `team` | `clarification / design / execution / delivery / steward` |
| `type` | 本文件定义的任务类型 |
| `priority` | 越大越先（P1 默认 0） |
| `dedupe_key` | 幂等键；`UNIQUE(version_id,type,dedupe_key)` |
| `depends_on` | 可空；P1 不实现依赖调度（保留） |
| `payload` | 只放引用与小数据；不放聊天全文 |
| `claimed_by` | worker 实例标识 |
| `lease_until` / `lease_epoch` | 租约与栅栏令牌 |
| `attempts` / `max_attempts` | 尝试次数 / 上限（默认 3） |
| `available_at` | 可领取时间（退避） |
| `last_error` | 最近失败原因 |

**去重语义**：全时唯一；重试复用同一行并递增 `attempts`；需要“再次执行”的新业务使用新 key（消息 id、附件 id、需求 revision 等）。重复 ensure 返回既有行，不新增。

**租约/心跳（P1 常量）**：lease 30s；heartbeat 10s；reconciler 3s；失败退避 `min(2^attempts,30)s`；超过 `max_attempts` 或不可重试 → `dead`。写回必须匹配 `lease_epoch`；否则 `409 lease_lost`。

**失败分类**：`model_error / tool_failure / budget / aborted / interrupted / context_overflow_unrecoverable`。`context_compacted` 是过程事件，不是失败。

## 2. P1 任务类型（实现）

| type | team | dedupe_key | payload | 输出 | 失败语义 |
|---|---|---|---|---|---|
| `chat.turn` | clarification | `opening:{conversation_id}` 或 `reply:{message_id}` | `{conversation_id,message_id?,trigger}` | Host assistant `conversation_message`；`agent_run`/`agent_message` | 模型不可用可重试；参数/作用域错误不可重试 |
| `requirement.curate` | clarification | `curate:{message_id}` | `{conversation_id,message_id}` | draft 需求版本/关系；Curator assistant 消息（含 `requirement_card`） | 结构化校验失败可重试（模型修正）；作用域错误 dead |
| `attachment.ingest` | clarification | `attachment:{attachment_id}` | `{attachment_id,version_id,message_id}` | `requirement_attachment` 提取状态/文本引用；Scout 团队 finding | 解析失败 → 附件 `failed`，任务 done（不重试）；存储错误可重试 |

**任务→profile**：`chat.turn→clarification_host`、`requirement.curate→requirement_curator`、`attachment.ingest→context_scout`。

**创建者**：conversation 创建时 ensure opening；用户消息 POST 时 best-effort ensure reply/curate；附件注册时 ensure ingest；reconciler 兜底扫描（不依赖事务/事件）。

**完成语义**：业务产物写入成功且事件发布尝试完成后 `done`；事件失败不回滚业务，reconciler 兜底。

## 3. P2+ 保留类型（不实现、不创建）

`design.round`、`design.requirement`、`design.gap`、`design.critic`、`design.data_plan`、`requirement.close`、`execute.case`、`analyze.failure`、`analyze.coverage`、`issue.triage`、`report.wave`、`report.version`、`report.render_pdf`、`knowledge.review`、`version.close`、`gc.run`、`index.repo`、`visual.*`、`search.*`。

这些类型在 P2+ 写入本文件并扩展 work_item/worker；P1 不注册 profile、不创建任务。
