# EzTest 0.2 P0 事件与 SSE 契约

> 状态：`frozen: P0`（P0 阶段末冻结；后续阶段只能增量）。
> 来源：`docs/superpowers/specs/2026-10-31-eztest-v02-p0-platform-foundation-design.md` §8。
> 相关：`contracts/openapi.yaml`（`/api/events`、`/api/stream`）、`contracts/db-schema.md`（`event_channel`、`event`）。

## 1. 持久化模型

事件日志是**频道内**持久有序的事件流，不是业务实体表。P0 两张表：

```sql
CREATE TABLE event_channel (
    channel    TEXT PRIMARY KEY,
    last_seq   BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE event (
    channel    TEXT NOT NULL REFERENCES event_channel(channel),
    seq        BIGINT NOT NULL,
    type       TEXT NOT NULL,
    envelope   JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (channel, seq)
);
```

规则：

1. `event_channel.channel` 是频道键；`last_seq` 是该频道已分配的最后一个序号的缓存。
2. `Publish` 在**同一事务**内分配 `seq`：
   `INSERT INTO event_channel (channel, last_seq, updated_at) VALUES ($1, 1, now())
    ON CONFLICT (channel) DO UPDATE SET last_seq = event_channel.last_seq + 1, updated_at = now()
    RETURNING last_seq`。
   插入 `event` 后再提交；提交成功后广播到进程内 Hub。
3. `seq` 是频道内严格递增的事件游标，**不是业务实体 ID**，不参与雪花 ID 规则；`(channel, seq)` 是 SSE 断线续传游标。
4. 跨频道无全局顺序；同一频道内按 `seq` 排序。
5. P0 没有归档/清理任务，`event` 只追加不删除。未来清理后“游标过旧”才可能真实发生，协议已预留 `stream.reset`。

## 2. 频道（channel）

P0 允许的频道集合（服务端必须以白名单校验，未知频道返回 400 `invalid_argument`）：

| 频道 | 含义 | P0 发布 |
|---|---|---|
| `system` | 平台级事件（登录、用户、设置、LLM、资源、内容） | 是 |
| `version:{id}` | 单个版本的事件（`{id}` 为十进制版本 ID） | 是（`version.created`） |
| `user:{id}` | 单用户事件 | 否（P1+ 预留） |

- 频道格式：`system` | `version:[1-9][0-9]*` | `user:[1-9][0-9]*`。
- P0 发布规则：
  - `version.created` 同时发布到 `system` 与 `version:{id}`（两条独立 `seq`）；
  - 其余 P0 事件一律发布到 `system`；
  - `user:{id}` 不发布，但订阅接口可以接受（收到空流/仅实时），便于后续阶段复用协议。
- 频道名中的 ID 是十进制字符串；服务端/客户端不得把它当数字游标。

## 3. 事件信封与线上表示

### 3.1 数据库中的 `envelope` JSONB

`event` 表列只存 `seq`、`channel`、`type`、`created_at`；其余进入 `envelope` JSONB：

```json
{
  "scope": {"version_id": "1001"},
  "payload": {"title": "订单中心 2026 Q4 迭代", "status": "initial", "sut_count": 2},
  "actor_id": "9001",
  "request_id": "req_01J..."
}
```

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `scope` | object | 是（可为空对象） | 作用域；实体 ID 一律十进制字符串。P0 常用 `version_id`、`user_id`；未知键忽略 |
| `payload` | object | 是（可为空对象） | 事件负载；**永不含密码、token、token hash、API key、header 值等秘密** |
| `actor_id` | string \| null | 否 | 触发者用户 ID 的十进制字符串；匿名事件（如登录失败）省略或 null |
| `request_id` | string | 否 | 触发请求的 `X-Request-ID` |

### 3.2 API/SSE 中返回的 Event JSON

`GET /api/events` 与 SSE `data:` 使用同一扁平形状（列字段 + 信封字段展开）：

```json
{
  "seq": 42,
  "channel": "version:1001",
  "type": "version.created",
  "scope": {"version_id": "1001"},
  "payload": {"title": "订单中心 2026 Q4 迭代", "status": "initial", "sut_count": 2},
  "actor_id": "9001",
  "request_id": "req_01J...",
  "created_at": "2026-10-31T09:00:00Z"
}
```

- `seq` 是整数（int64）；其余实体 ID 都是字符串。
- `created_at` 是 RFC3339 UTC。
- payload 形状由事件类型决定；P0 唯一被规格示例固定的是 `version.created` 的 `payload` 摘要
  （`title/status/sut_count`，`scope.version_id` 指向版本）。消费方必须忽略未知字段与未知事件类型。
- 事件 JSON 不套 `{"data": ...}` 信封；SSE 的 `data:` 直接是上述对象。

## 4. 事件类型（P0）

P0 只发布以下类型；`resource.<kind>.*` 的具体类型为 `created|updated|deleted`。
事件类型是持久化字符串，后续阶段只能新增，不得重命名已发布类型。

| type | channel | 触发 | payload 说明 |
|---|---|---|---|
| `auth.login` | `system` | 登录成功 | 摘要；不含密码/token |
| `auth.login_failed` | `system` | 登录失败 | 摘要；不含密码 |
| `auth.logout` | `system` | 登出 | 摘要；不含 token |
| `user.created` | `system` | admin 创建用户 | 用户摘要；不含密码/hash |
| `user.updated` | `system` | admin 更新用户 | 用户摘要 |
| `settings.updated` | `system` | 设置更新成功 | 设置变更摘要；不含 secret 值 |
| `llm.config_created` | `system` | 创建 LLM 配置 | 配置摘要；`api_key` 永不出现 |
| `llm.config_updated` | `system` | 更新 LLM 配置 | 配置摘要；`api_key` 永不出现 |
| `llm.config_deleted` | `system` | 删除 LLM 配置 | 配置摘要 |
| `resource.sut.created` / `.updated` / `.deleted` | `system` | SUT 写操作 | 资源摘要 |
| `resource.environment.created` / `.updated` / `.deleted` | `system` | 环境写操作 | 资源摘要 |
| `resource.page.created` / `.updated` / `.deleted` | `system` | 页面写操作 | 资源摘要 |
| `resource.api.created` / `.updated` / `.deleted` | `system` | API 写操作 | 资源摘要 |
| `resource.database.created` / `.updated` / `.deleted` | `system` | 目标库写操作 | 资源摘要；不含密码 |
| `resource.account.created` / `.updated` / `.deleted` | `system` | 测试账号写操作 | 资源摘要；不含密码 |
| `resource.code_repo.created` / `.updated` / `.deleted` | `system` | 代码仓写操作 | 资源摘要 |
| `content.uploaded` | `system` | 内容上传 | 上传摘要；不含文件内容 |
| `version.created` | `system` **和** `version:{id}` | 版本创建 | `title/status/sut_count`（规格示例） |

说明：

- `auth.password_changed`、`user.password_reset` 等属于**审计**动作（见 `audit_log`），不在 P0 事件类型清单内。
- 发布失败不得回滚已成功的业务写；P0 仅记录告警，不承诺可靠投递/重试；reconciler 属 P1+。

## 5. 快照：`GET /api/events`

权限：登录用户。查询参数：

| 参数 | 必填 | 说明 |
|---|---|---|
| `channel` | 是 | 白名单频道；未知 → 400 |
| `after` | 否 | 频道内 `seq`，≥0；省略/0 表示“最新一页” |
| `limit` | 否 | 默认 50，上限 200；非法/超限按实现回退/截断 |

语义：

1. `after=0`（或省略）：取该频道**最新** `limit` 条，内部按 seq 倒序取，再以 **seq 正序**返回。
2. `after>0`：取 `seq > after` 的事件，按 **seq 正序**返回。
3. 响应（**扁平**，不是标准分页信封）：

```json
{
  "data": [{"seq": 41, "channel": "system", "type": "settings.updated", "...": "..."}],
  "snapshot_cursor": 57,
  "has_more": false
}
```

- `snapshot_cursor` 是**请求时刻**该频道最新 `last_seq`（可能大于本页最大 seq）。
- `has_more=true` 表示还有更早（`after=0` 时）或更多（`after>0` 时）事件；客户端用本页最后一条 seq 继续拉取。
- 客户端标准流程：先取快照并记录 `snapshot_cursor`，再从该游标打开 SSE；历史 ≤ cursor、实时 > cursor，无缝无洞。
- 未知 channel 或非法 `after`/`limit` → 400 `invalid_argument`；未认证 → 401。

## 6. SSE：`GET /api/stream`

权限：登录用户；未认证返回 **JSON 错误信封 401**（不是 SSE 帧）。
查询/请求头：

| 来源 | 说明 |
|---|---|
| `Last-Event-ID` 请求头 | 断线续传游标；**优先级高于** `after` |
| `after` 查询参数 | 游标；`Last-Event-ID` 不存在时使用 |
| `channel` 查询参数 | 必填，白名单校验 |

### 6.1 连接建立顺序（subscribe-before-replay）

有游标时严格按以下顺序：

1. 校验认证与频道；
2. **先**向进程内 Hub 注册订阅；
3. 再查 DB 回放 `seq > cursor` 的事件（正序）；
4. 回放完成后接续实时推送；
5. 对同一 `(channel, seq)` 去重（Hub 实时消息与 DB 回放可能重叠），保证不重不漏。

无游标时：

- 从当前频道 `last_seq` 之后只订阅实时，不回放；
- 响应头 `X-Snapshot-Cursor` 给出连接建立时的当前末尾 seq，客户端可据此初始跳过快照。

### 6.2 帧格式

```
id: 42
event: version.created
data: {"seq":42,"channel":"version:1001","type":"version.created","scope":{"version_id":"1001"},"payload":{"title":"...","status":"initial","sut_count":2},"created_at":"2026-10-31T09:00:00Z"}

```

- `id` 为该事件在频道内的 `seq`（SSE 协议层字符串）。
- `event` 为事件 `type`。
- `data` 为单个 Event JSON（见 §3.2）。
- 服务端在每次写入后 flush。

### 6.3 心跳

- 每 **20 秒** 至少写一次 SSE 注释行（如 `: heartbeat\n\n`），用于保持连接与探测断线。
- 客户端若长时间（实现建议 45s）收不到任何字节，应视为断线并带 `Last-Event-ID` 重连。

### 6.4 游标过旧：`stream.reset`

- 触发条件（规格定义）：`after > last_seq`，或 `after < min(seq)-1`。
- 服务端发送合成控制帧：

```
event: stream.reset
data: <控制信息>

```

- **无 `id`**；**不写入 `event` 表**；**不进入普通事件回调/不当作业务事件**。
- P0 不冻结 data 字段；客户端收到任何 `stream.reset` 都必须重新拉取 `GET /api/events` 快照，
  并用新 `snapshot_cursor` 重连。
- 该控制帧在 P0 即定义，避免 P7 清理实现后修改协议。

### 6.5 慢消费者与断线

- Hub 采用有界进程内缓冲区；消费者持续落后时服务端可以断开该连接，不阻塞发布方与其他消费者。
- 客户端重连时必须带 `Last-Event-ID`（= 已处理的最大 seq），由回放补齐；`stream.reset` 则走全量快照。
- 重复投递由客户端按 `(channel, seq)` 去重。

## 7. 单实例限制（P0 明确不承诺跨实例实时）

- P0 的 Hub 是**单 API 进程内存**实现：Publish 提交后只广播给本进程订阅者。
- 多副本部署时：
  - 持久事件日志（PG）仍是全局真相，任意实例都可以快照/回放；
  - 但实例 A 写入的事件不会实时推送到连接在实例 B 上的 SSE 客户端；
  - 客户端通过断线重连（`Last-Event-ID`）或重新快照恢复，不能假设实时无延迟。
- 跨实例 fan-out（PG `LISTEN/NOTIFY` 或轮询）属 P7；P0 handoff 必须记录该限制。
- 不承诺跨频道全局顺序；多实例下 `seq` 仍由 DB 事务保证频道内唯一递增。

## 8. 安全与数据约束

1. 所有事件读取/订阅要求 Bearer 登录；P0 不做频道级授权（单团队），但 channel 必须在白名单内。
2. `envelope`/`payload` 中禁止出现：密码、token、token hash、API key、Authorization/header 值。
3. 事件只作为 UI 投影；不得把事件当业务状态真相（业务状态以各实体表为准）。
4. `X-Request-ID` 用于串联请求、审计与事件的 `request_id`。

## 9. 示例

快照（`GET /api/events?channel=version:1001&after=0&limit=50`）：

```json
{
  "data": [
    {
      "seq": 1,
      "channel": "version:1001",
      "type": "version.created",
      "scope": {"version_id": "1001"},
      "payload": {"title": "订单中心 2026 Q4 迭代", "status": "initial", "sut_count": 2},
      "actor_id": "1",
      "request_id": "req_01J",
      "created_at": "2026-10-31T09:00:00Z"
    }
  ],
  "snapshot_cursor": 1,
  "has_more": false
}
```

SSE：

```
HTTP/1.1 200 OK
Content-Type: text/event-stream; charset=utf-8
X-Snapshot-Cursor: 1

id: 2
event: settings.updated
data: {"seq":2,"channel":"system","type":"settings.updated","scope":{},"payload":{"key":"execution.global_concurrency"},"actor_id":"1","request_id":"req_01K","created_at":"2026-10-31T09:01:00Z"}

: heartbeat

```

---

## 10. P1 增量事件（`frozen: P1`，2026-10-31）

P0 的频道/信封/SSE/游标/心跳/`stream.reset` 语义不变。P1 业务事件一律发布到 `version:{id}`；版本状态类事件同时发 `system`（如 `version.started`/`version.status_changed`）。P1 **不新增频道**，使用 `scope` 携带会话/需求/run 等 ID。`seq` 与 `snapshot_cursor` 保持**整数**（D4 修代码以符合契约，见 spec §11）。

### 10.1 事件类型

| type | channel | 触发 | payload 要点 |
|---|---|---|---|
| `version.started` | `system` + `version:{id}` | 首个对话消息/需求/确认触发 initial→running | `from:"initial",to:"running"` |
| `version.status_changed` | `system` + `version:{id}` | 版本状态投影变化（含 started 的通用形式） | `from,to` |
| `conversation.created` | `version:{id}` | 新建澄清会话 | `conversation_id,title,kind` |
| `conversation.message` | `version:{id}` | 用户/assistant 消息落库 | 完整 message（id/role/parts/refs/created_at）；不含 prompt/密钥/路径 |
| `conversation.summary_updated` | `version:{id}` | 摘要版本更新 | `conversation_id,version,token_estimate` |
| `agent.run.started` | `version:{id}` | run 开始 | `run_id,profile_key,work_item_id,conversation_id` |
| `agent.activity` | `version:{id}` | 工具/思考活动（非 token 增量） | `run_id,conversation_id,status:"thinking|tool",tool?,summary?` |
| `agent.run.finished` | `version:{id}` | run 成功 | `run_id,turns,usage` |
| `agent.run.failed` | `version:{id}` | run 失败（非 context_compacted） | `run_id,terminal_reason,retryable` |
| `context.compacted` | `version:{id}` | 上下文压缩发生（过程事件） | `run_id,conversation_id,summary_version,token_before,token_after` |
| `requirement.created` | `version:{id}` | 需求逻辑对象创建 | `requirement_id,status,title` |
| `requirement.version_created` | `version:{id}` | 新不可变版本（创建/编辑/回退） | `requirement_id,requirement_version_id,revision,status` |
| `requirement.clarified` | `version:{id}` | 用户确认 | `requirement_id,requirement_version_id,scope,batch_id` |
| `requirement.rolled_back` | `version:{id}` | 用户确认回退到新 draft | `requirement_id,from_version_id,new_version_id,reason,affected_case_version_ids` |
| `requirement.relation_created` | `version:{id}` | 关系提议落库 | `from_requirement_id,to_requirement_id,kind` |
| `requirement.attachment_ingested` | `version:{id}` | 附件解析成功 | `attachment_id,message_id,chars` |
| `requirement.attachment_failed` | `version:{id}` | 附件解析失败 | `attachment_id,message_id,code,message` |
| `work_item.created` | `version:{id}` | 任务 ensure 新建 | `work_item_id,type,team,dedupe_key` |
| `work_item.claimed` | `version:{id}` | 领取 | `work_item_id,type,claimed_by,lease_epoch` |
| `work_item.failed` | `version:{id}` | 失败待重试 | `work_item_id,type,attempt,error` |
| `work_item.dead` | `version:{id}` | 超限/不可重试 | `work_item_id,type,attempt,error` |
| `team_room.post` | `version:{id}` | 结构化帖子 | `post_id,team,kind,refs` |
| `team_room.digest_updated` | `version:{id}` | 新摘要版本 | `team,digest_version,token_estimate` |

### 10.2 约束

1. `requirement.rolled_back` 的 `affected_case_version_ids` 在 P1 由 stale 端口返回（P1 固定 `[]`）；P2 接 case 表后填真实 ID，事件字段不变。
2. `agent.activity` 不承诺 token 级流式；UI 以完整 `conversation.message` 为准。
3. `context.compacted` 不是失败终态；`context_overflow_unrecoverable` 才走 `agent.run.failed`。
4. 事件中的附件只给 ID/名称/大小/状态，不给文件系统路径/提取全文。
5. `work_item.created` 由 ensure 是否新建决定；重复 ensure 不重复发事件。
6. 事件发布失败不回滚业务；reconciler 兜底；P1 仍为单实例 Hub，跨实例 fan-out 属 P7。
